#!/usr/bin/env python3
"""Failure aggregation and the exit-code contract of the race battery.

Every check in the battery reports into one :class:`Battery`. The exit code
is derived from the collected failures only -- never from "no hangs were
seen" -- so a helper that never started, an arm whose fixtures never built,
or a control that answered the wrong thing cannot be mistaken for a clean
race. The same object is what gets serialized, so the JSON report and the
exit status cannot disagree.
"""

import json
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
_CLI = os.path.dirname(_HERE)
for _directory in (_HERE, _CLI):
    if _directory not in sys.path:
        sys.path.insert(0, _directory)

import hostcontext  # noqa: E402

# ``command_sanitize`` is imported by name as well as by symbol because one
# self-test control has to remove this writer's registry entry for the length
# of the control: the rule it pins is what the audit says when a committed
# report has no registered producer.
import command_sanitize  # noqa: E402
from command_sanitize import (  # noqa: E402
    PRIVACY_SANITIZER_NAME,
    SHARED_TIER,
    audit_report_writers,
    committed_report_problems,
    personal_path_in_report,
    recorded_checkout_root,
    recorded_git_identity,
    registry_entry_for,
    run_shared_self_test,
    sanitized_command,
    under_profile,
    write_committed_report,
)

REPORT_SCHEMA = "iprange-cli-race-battery-report-v1"
REPORT_NAME = "race-battery.json"

# This battery's report is committed evidence, so it is registered in the
# shared writer's table under the module path the audit resolves against
# ``v4/cli``. ``WRITER_SCREENED`` is the battery's own set of path-valued
# options; the registry must agree with it, and ``--provenance-note`` belongs
# in it because free text is how a workstation path once reached this report.
WRITER_MODULE = "races/aggregate.py"
WRITER_SCREENED = ("--rust", "--go", "--fixture-tool", "--report-dir",
                   "--provenance-note")


class ReportPolicyError(Exception):
    """The requested report location is not allowed."""


def check_report_dir_policy(path):
    """Refuse a report location that could overwrite accepted evidence.

    The battery writes machine evidence, so it may only write where evidence
    is *staged*, never where evidence is *committed*: inside the checkout, or
    under the operator's profile, is rejected outright.
    """

    if not path:
        raise ReportPolicyError("--report-dir is mandatory")
    if not os.path.isabs(path):
        raise ReportPolicyError(f"--report-dir must be absolute: {path!r}")
    real = os.path.realpath(path)
    if under_profile(real):
        raise ReportPolicyError(
            f"--report-dir {path} is under the operator's profile; stage "
            "battery output in the authorized scratch area")
    checkout = recorded_checkout_root()
    if checkout:
        root = os.path.realpath(checkout) + os.sep
        if real == os.path.realpath(checkout) or real.startswith(root):
            raise ReportPolicyError(
                f"--report-dir {path} is inside the repository checkout "
                f"({checkout}); committed evidence paths are never battery "
                "output locations")
    return real


def validate_report_dir(path):
    """Return a fresh absolute report directory, or refuse.

    On top of the location policy, an existing directory must be empty, so a
    rerun cannot silently mix two rounds of reports.
    """

    real = check_report_dir_policy(path)
    if os.path.exists(real) and os.listdir(real):
        raise ReportPolicyError(f"--report-dir must be empty or absent: {real}")
    os.makedirs(real, exist_ok=True)
    return real


# ---- the host-context contract ------------------------------------------------
# Every record states what the host was doing while the battery waited on an
# answer. The names below are the contract, pinned from both sides by
# --self-test: a record that drops a field is refused, and a sample whose
# oversubscription flag disagrees with its own numbers is refused.
#
# None of this can soften a finding. The judgment reads no load number to
# decide anything -- an engine-hang fails whether the run queue was 1 or 10,000
# -- and the only effect of an untrustworthy sample is that the battery stops
# claiming success at all.
HOST_CONTEXT_KEYS = ("source", "nproc", "deadline", "samples", "run_queue",
                     "loadavg_1m", "over_cpus_samples", "oversubscribed")
HOST_SAMPLE_KEYS = ("source", "nproc", "deadline", "loadavg_1m", "run_queue",
                    "over_cpus")
HOST_HANG_KEYS = ("host_at_attempt_start", "host_at_detection")


def _sample_problem(sample, where):
    """``(class, why)`` one host sample cannot be trusted, or ``None``.

    The two classes are kept apart because they are different defects: a
    missing field is a battery that stopped measuring, and a contradictory one
    is a battery whose numbers cannot be believed. Neither may pass.
    """

    missing = "host-context-missing"
    conflict = "host-context-inconsistent"
    if not isinstance(sample, dict):
        return (missing, f"{where}: host sample is "
                        f"{type(sample).__name__}, not a mapping")
    absent = [key for key in HOST_SAMPLE_KEYS if key not in sample]
    if absent:
        return (missing, f"{where}: missing {sorted(absent)}")
    cpus = sample["nproc"]
    if not isinstance(cpus, int) or cpus < 1:
        return (missing, f"{where}: nproc is {cpus!r}; an oversubscription "
                         "claim cannot rest on a missing denominator")
    deadline = sample["deadline"]
    if not isinstance(deadline, (int, float)) or deadline <= 0:
        return (missing, f"{where}: deadline is {deadline!r}")
    if sample["loadavg_1m"] is None and sample["run_queue"] is None:
        return (missing, f"{where}: neither loadavg_1m nor run_queue was "
                        "obtainable")
    queue = sample["run_queue"]
    if queue is not None and sample["over_cpus"] != (queue > cpus):
        return (conflict, f"{where}: over_cpus says {sample['over_cpus']!r} but "
                         f"run_queue {queue} against nproc {cpus} says "
                         f"{queue > cpus}")
    return None


def _context_problem(context):
    """Why a record's host_context cannot be trusted, or None when it can."""

    missing = "host-context-missing"
    conflict = "host-context-inconsistent"
    if not isinstance(context, dict):
        return (missing, f"host_context is {type(context).__name__}, not a "
                        "mapping; a battery that waits on a deadline has to "
                        "say what the host was doing while it waited")
    absent = [key for key in HOST_CONTEXT_KEYS if key not in context]
    if absent:
        return (missing, f"host_context is missing {sorted(absent)}")
    if not isinstance(context["samples"], int) or context["samples"] < 1:
        return (missing, f"host_context sampled {context['samples']!r} attempts")
    deadline = context["deadline"]
    if not isinstance(deadline, (int, float)) or deadline <= 0:
        return (missing, f"host_context deadline is {deadline!r}")
    if context["run_queue"] is None and context["loadavg_1m"] is None:
        return (missing, "host_context recorded neither a run queue nor a load "
                        "average")
    cpus = context["nproc"]
    if not isinstance(cpus, int) or cpus < 1:
        return (missing, f"host_context nproc is {cpus!r}")
    spread = context["run_queue"] or {}
    median = spread.get("median") if isinstance(spread, dict) else None
    if median is not None and context["oversubscribed"] != (median > cpus):
        return (conflict, f"host_context oversubscribed says "
                         f"{context['oversubscribed']!r} but the sampled run "
                         f"queue median {median} against nproc {cpus} says "
                         f"{median > cpus}")
    return None


def require_host_context(battery, subject, record):
    """Require one record to carry a trustworthy host context.

    Run before the hang judgment in both judges, so a missing sample cannot be
    hidden behind an otherwise clean arm.
    """

    problem = _context_problem(record.get("host_context"))
    if problem is not None:
        battery.fail(problem[0], subject, problem[1])
    for index, hang in enumerate(_hang_observations(record)):
        if not isinstance(hang, dict):
            battery.fail("host-context-missing", subject,
                         f"hang[{index}] is {type(hang).__name__}, not an "
                         "observation")
            continue
        for key in HOST_HANG_KEYS:
            problem = _sample_problem(hang.get(key), f"hang[{index}].{key}")
            if problem is not None:
                battery.fail(problem[0], subject, problem[1])


def _hang_observations(record):
    """Every silent attempt this record claims, as an observation per attempt.

    A product arm records its hangs as observations, so its list is used as it
    stands. The detector control counts them instead, so its count has to be
    matched one-for-one by ``hang_observations``: a detector that claims three
    silent attempts owes the host sample of each of the three.
    """

    hangs = record.get("hangs") or []
    if isinstance(hangs, list):
        return hangs
    if isinstance(hangs, int):
        observations = record.get("hang_observations")
        if not isinstance(observations, list):
            return [{"missing": f"hang_observations is "
                                f"{type(observations).__name__} although the "
                                f"record counts {hangs} hangs"} for _ in range(hangs)]
        if len(observations) != hangs:
            return list(observations) + [
                {"missing": f"the record counts {hangs} hangs but carries "
                            f"{len(observations)} observations"}] \
                   * abs(hangs - len(observations))
        return observations
    return [{"missing": f"hangs is {type(hangs).__name__}"}]


def host_note(hang):
    """The host sentence appended to an engine-hang finding.

    Presentation only -- the failure is already recorded. It states the run
    queue against the CPU count at both ends of the attempt and the deadline it
    failed to meet, so a starved process and a reader parked on a FIFO read
    differently in the artifact instead of in someone's memory.
    """

    def spell(key):
        sample = hang.get(key)
        if not isinstance(sample, dict):
            return "unrecorded"
        queue, cpus = sample.get("run_queue"), sample.get("nproc")
        if queue is None:
            return f"run queue unavailable (loadavg {sample.get('loadavg_1m')})"
        state = ("oversubscribed" if sample.get("over_cpus")
                 else "within cpu count") if cpus else "nproc unreported"
        return f"run_queue={queue}/nproc={cpus} ({state})"

    return (f"host at attempt start {spell('host_at_attempt_start')}, at "
            f"detection {spell('host_at_detection')}, deadline "
            f"{(hang.get('host_at_attempt_start') or {}).get('deadline')}")


class Failure:
    """One aggregate-level reason the battery may not claim success."""

    def __init__(self, klass, subject, detail):
        self.klass = klass
        self.subject = subject
        self.detail = detail

    def as_dict(self):
        return {"class": self.klass, "subject": self.subject,
                "detail": self.detail}

    def __str__(self):
        return f"{self.klass}[{self.subject}]: {self.detail}"


class Battery:
    """Collects arm/control records and decides the exit code."""

    def __init__(self, binaries=None, fixture_tool=None, options=None):
        self.failures = []
        self.entries = []
        self.binaries = dict(binaries or {})
        self.fixture_tool = fixture_tool
        self.options = dict(options or {})

    # ---- recording -------------------------------------------------
    def add(self, subject, record):
        """Attach one structured record (per arm, per control)."""

        merged = {"subject": subject}
        merged.update(record)
        self.entries.append(merged)
        return merged

    def fail(self, klass, subject, detail):
        self.failures.append(Failure(klass, subject, detail))
        return self.failures[-1]

    # ---- decision --------------------------------------------------
    @property
    def ok(self):
        return not self.failures

    def exit_code(self):
        return 0 if self.ok else 1

    def verdict(self):
        return "PASS" if self.ok else "FAIL"

    def summary_lines(self):
        lines = [f"race battery verdict: {self.verdict()}"]
        for entry in self.entries:
            status = "FAIL" if any(
                failure.subject == entry["subject"]
                for failure in self.failures) else "ok"
            digest = {key: value for key, value in entry.items()
                      if key not in ("subject", "classes", "controls")}
            lines.append(f"  [{status:4s}] {entry['subject']} {digest}")
        for failure in self.failures:
            lines.append(f"  FAILURE {failure}")
        return lines

    # ---- artifact --------------------------------------------------
    def report(self, provenance_note=None):
        # ``command``, ``checkout_root`` and ``git_head`` belong to the shared
        # committed-report writer: ``write_committed_report()`` clears and
        # rewrites all three.  ``command`` and ``git_head`` are still produced
        # here so the pre-write personal-path scan below sees the values this
        # producer actually holds, which the writer may then rewrite; a raw
        # argv is exactly the thing that scan exists to catch.
        # ``checkout_root`` is not produced here: the writer records null for
        # it unconditionally (``command_sanitize.report_provenance``), so a
        # value here would be discarded and would only suggest the producer
        # gets to name a machine directory in committed evidence.
        document = {
            "schema": REPORT_SCHEMA,
            "command": sanitized_command(),
            "git_head": recorded_git_identity(),
            "binaries": self.binaries,
            "fixture_tool": self.fixture_tool,
            "options": self.options,
            "verdict": self.verdict(),
            "entries": self.entries,
            "failures": [failure.as_dict() for failure in self.failures],
        }
        if provenance_note:
            document["provenance_note"] = provenance_note
        return document

    def write(self, report_dir, provenance_note=None):
        """Write the report through the shared committed-report writer.

        These bytes are committed evidence -- the kind gate and a later
        reviewer read them -- so the shared writer serializes them: it owns the
        ``command``/``checkout_root``/``git_head`` members, derives the privacy
        block from the paths actually screened, and re-runs the personal-path
        scan over the finished report. The location policy and the pre-write
        personal-path refusal stay here because they are this battery's own
        contract: a refusal must be a typed ``ReportPolicyError`` raised before
        anything exists on disk, not a ``SystemExit`` from inside the writer.
        """

        report_dir = check_report_dir_policy(report_dir)
        os.makedirs(report_dir, exist_ok=True)
        document = self.report(provenance_note)
        personal = personal_path_in_report(document)
        if personal is not None:
            raise ReportPolicyError(
                "refusing to write a race report containing the operator's "
                f"profile path: {personal!r}")
        path = os.path.join(report_dir, REPORT_NAME)
        write_committed_report(path, document,
                               caller_paths=self.caller_paths(
                                   report_dir, provenance_note),
                               indent=2)
        return path

    def caller_paths(self, report_dir, provenance_note):
        """The path-valued inputs handed to the shared writer for screening.

        The audit compares these labels with the registry's ``screened`` set
        and records them in ``privacy.checked_inputs``, so an option that
        quietly stopped being handed over becomes a gate failure instead of a
        silently narrower check.
        """

        def path_of(info):
            return (info or {}).get("path")

        return (("--rust", path_of(self.binaries.get("rust"))),
                ("--go", path_of(self.binaries.get("go"))),
                ("--fixture-tool", path_of(self.fixture_tool)),
                ("--report-dir", report_dir),
                ("--provenance-note", provenance_note))

    def write_log(self, report_dir, name, lines):
        """Persist one per-arm transcript beside the report."""

        safe = "".join(char if char.isalnum() or char in "-_." else "-"
                       for char in name) or "log"
        path = os.path.join(report_dir, f"{safe}.log")
        with open(path, "w", encoding="utf-8") as stream:
            for line in lines:
                stream.write(line.rstrip("\n") + "\n")
        return path


def committed_artifact_problems(document, where=None):
    """Why one staged report may not be installed as committed evidence.

    Two rule sets meet here, and the report has to satisfy both:

    * the shared writer's rules -- provenance members, a ``command`` that
      re-sanitizes to itself, a null ``checkout_root``, and the ``privacy``
      block recording that every registered path option was screened; and
    * this battery's own record contract -- every arm and control record
      states what the host was doing while the battery waited, and every
      counted hang carries its observations.

    The second half exists because the artifact audit is shared and cannot
    know the races report's schema. Without it a step that recorded its own
    MISMATCH could still promote bytes the producer would never have signed,
    and the installed artifact -- not the staged one -- is what a reviewer
    reads. Load numbers here are facts about the host and nothing else: the
    verdict was made by the runner, and this function cannot soften it, only
    refuse an artifact that stopped saying what it measured.
    """

    name = where or REPORT_NAME
    entry = command_sanitize.COMMITTED_REPORT_WRITERS.get(WRITER_MODULE) or {}
    problems = committed_report_problems(
        document, where=name,
        require_privacy=entry.get("tier") == SHARED_TIER,
        screened=entry.get("screened"))
    entries = document.get("entries")
    if not isinstance(entries, list) or not entries:
        return problems + [f"{name}: entries is "
                           f"{type(entries).__name__}; a report with no arm "
                           "records makes no measurement"]
    for record in entries:
        if not isinstance(record, dict):
            problems.append(f"{name}: an entry is "
                            f"{type(record).__name__}, not a record")
            continue
        subject = record.get("subject") or record.get("subject_label") or "record"
        checks = Battery()
        require_host_context(checks, subject, record)
        problems.extend(f"{name}: {failure}" for failure in checks.failures)
    return problems


# ---- the judgment, kept apart from execution so it can be tested -------

def judge_arm(battery, subject, record, *, attempts):
    """Turn one collected arm record into aggregate failures.

    The runner only measures; every claim ("the race was raced", "the control
    answered what the product promises") is decided here, against the frozen
    expectations carried inside the record. Keeping the decision pure is what
    lets ``--self-test`` mutate a collected record and require that the battery
    notices.
    """

    require_host_context(battery, subject, record)
    stable_success = record["stable_success"]
    stable_refusal = record["stable_refusal"]
    raced_refusals = record.get("raced_refusals", stable_refusal)
    for control in record.get("controls", []):
        if not control.get("ok"):
            battery.fail("control-unexpected", subject,
                         f"stable-{control['control']} answered "
                         f"{control['shape']} (kind {control['kind']}, "
                         f"expected one of {control['expected']}, "
                         f"blocked={control['blocked']}, "
                         f"wedge={control['wedge']}, "
                         f"{host_note(control)})")
    for hang in record.get("hangs", []):
        battery.fail("engine-hang", subject,
                     f"attempt blocked in the open window: "
                     f"blocked={hang['blocked']} wedge={hang['wedge']} "
                     f"{host_note(hang)}")
    activity = record.get("replacement_activity") or {}
    window = record.get("timing_window") or {}
    if not activity.get("observed"):
        battery.fail("replacement-activity-absent", subject,
                     f"the swapper completed no replacements: {activity}")
    elif activity.get("to_fifo", 0) <= 0 or activity.get("to_regular", 0) <= 0:
        battery.fail("replacement-activity-incomplete", subject,
                     f"the swapper only reached one node kind: {activity}")
    if not window.get("exercised"):
        battery.fail("timing-window-unexercised", subject,
                     f"no replacement completed inside a request window "
                     f"(need >= {window.get('required_in_flight_to_fifo', 1)}): "
                     f"{window}")
    if record.get("refusal_observed_in_race", 0) < 1:
        battery.fail("race-refusal-unobserved", subject,
                     f"the engine never answered a refusal class while the "
                     f"name was raced (expected one of {raced_refusals}): "
                     f"{record.get('classes')}")
    unexpected = sorted(set(record.get("classes", {}))
                        - set(stable_success) - set(raced_refusals))
    if unexpected:
        battery.fail("unexpected-answer", subject,
                     f"raced attempts answered {unexpected}, outside the "
                     f"frozen arm shapes "
                     f"{sorted(set(stable_success) | set(raced_refusals))}")
    if record.get("attempts_completed", 0) != attempts and \
            not record.get("hangs"):
        battery.fail("attempts-incomplete", subject,
                     f"completed {record.get('attempts_completed', 0)} of "
                     f"{attempts} attempts without a hang")
    return battery.failures


def judge_detector(battery, subject, record, *, minimum_wedges=1):
    """Require the hang detector to have seen a real wedge."""

    require_host_context(battery, subject, record)
    activity = record.get("replacement_activity") or {}
    window = record.get("timing_window") or {}
    if not activity.get("observed"):
        battery.fail("replacement-activity-absent", subject,
                     f"the detector phase saw no replacements: {activity}")
    if not window.get("exercised"):
        battery.fail("timing-window-unexercised", subject,
                     f"the detector phase never raced a request: {window}")
    if record.get("confirmed_wedges", 0) < minimum_wedges:
        battery.fail("detector-blind", subject,
                     f"the naive opener never produced a confirmed wedge "
                     f"(hangs={record.get('hangs', 0)} of "
                     f"{record.get('attempts', 0)} attempts); a detector that "
                     "cannot see a real wedge cannot certify 'no hangs' on a "
                     "product arm")
    if not record.get("stable_regular_ok", True):
        battery.fail("control-unexpected", subject,
                     "stable-regular sentinel did not answer OPENED promptly")
    return battery.failures


class _restored_registry_entry:
    """Hold one registry entry in place for the length of a control.

    The audits read the live table, so a control that removed this writer's
    entry has to be able to put it back while the rest of the control still
    runs, and take it away again on the way out -- including when the control
    raises. Leaving the entry behind would make this battery look like a
    committed-report producer to every later audit in the process, whether or
    not the registry still says so.
    """

    def __init__(self, module_name, entry):
        self.module_name = module_name
        self.entry = entry
        table = command_sanitize.COMMITTED_REPORT_WRITERS
        self.had = module_name in table
        self.previous = table.get(module_name)

    def __enter__(self):
        if self.entry is not None:
            command_sanitize.COMMITTED_REPORT_WRITERS[self.module_name] = \
                self.entry
        return self

    def __exit__(self, *exc_info):
        table = command_sanitize.COMMITTED_REPORT_WRITERS
        if self.had:
            table[self.module_name] = self.previous
        else:
            table.pop(self.module_name, None)
        return False


def _clean_host_context(samples=4, nproc=8, run_queue=(1, 2, 3)):
    """A record-level host context that satisfies the contract, offline.

    Written out rather than sampled: whether the controls pass cannot depend on
    what else the workstation happens to be running, so the clean shapes are
    fixed and the mutations break them on purpose.
    """

    low, middle, high = run_queue
    return {"source": "proc/loadavg", "nproc": nproc, "deadline": 1.5,
            "samples": samples,
            "run_queue": {"min": low, "median": middle, "max": high},
            "loadavg_1m": {"min": 0.5, "median": 0.6, "max": 0.7},
            "over_cpus_samples": sum(1 for value in run_queue if value > nproc),
            "oversubscribed": middle > nproc}


def _clean_sample(run_queue=2, nproc=8, deadline=1.5):
    """One trustworthy host sample, with ``over_cpus`` derived not asserted."""

    return {"source": "proc/loadavg", "nproc": nproc, "deadline": deadline,
            "loadavg_1m": 0.6, "loadavg_5m": 0.5, "loadavg_15m": 0.4,
            "run_queue": run_queue, "over_cpus": run_queue > nproc}


def _clean_hang(**changes):
    """An attempt that stopped answering, with both host samples attached."""

    hang = {"kind": "timeout", "shape": "timeout",
            "blocked": ["tid1=wchan=pipe_read"],
            "wedge": "wedge-confirmed:answered-after-writer-attach:fifo-0001",
            "host_at_attempt_start": _clean_sample(),
            "host_at_detection": _clean_sample()}
    hang.update(changes)
    return hang


def _starved_hang(run_queue=4000, nproc=24):
    """A hang whose host samples say the run queue dwarved the CPU count.

    This is the shape a workstation under a hundred-fold load produces. It must
    still be judged an engine-hang: the sample explains a failure to a reader,
    it never excuses one.
    """

    def starved():
        sample = _clean_sample(run_queue=run_queue, nproc=nproc)
        sample.update({"loadavg_1m": 2576.9, "loadavg_5m": 1392.0,
                       "loadavg_15m": 585.4})
        return sample

    return _clean_hang(host_at_attempt_start=starved(),
                       host_at_detection=starved())


def _clean_arm_record(attempts=4):
    """A collected record for an arm that passed every check."""

    return {
        "engine": "rust", "arm": "meta", "method": "iprange.v1.direct.replace",
        "target": "/tmp/example/meta.t",
        "stable_success": ["RESULT"], "stable_refusal": ["-32010/invalid_path"],
        "raced_refusals": ["-32010/invalid_path"],
        "controls": [
            {"control": "regular", "kind": "answered", "shape": "RESULT",
             "expected": ["RESULT"], "ok": True, "blocked": [], "wedge": None},
            {"control": "fifo", "kind": "answered",
             "shape": "-32010/invalid_path", "expected": ["-32010/invalid_path"],
             "ok": True, "blocked": [], "wedge": None}],
        "classes": {"RESULT": 2, "-32010/invalid_path": 2},
        "attempts_completed": attempts,
        "replacement_activity": {"to_fifo": 900, "to_regular": 900,
                                 "toggles": 1800, "helper_errors": 0,
                                 "observed": True},
        "timing_window": {"in_flight_to_fifo": 870,
                          "in_flight_to_regular": 870,
                          "required_in_flight_to_fifo": MIN_IN_FLIGHT_REQUIRED,
                          "exercised": True},
        "refusal_observed_in_race": 2,
        "host_context": _clean_host_context(samples=attempts),
    }


def _clean_detector_record():
    return {"attempts": 6, "hangs": 3, "confirmed_wedges": 2,
            "hang_observations": [_clean_hang(), _clean_hang(), _clean_hang()],
            "host_context": _clean_host_context(samples=6),
            "stable_regular_ok": True,
            "replacement_activity": {"toggles": 4000, "to_fifo": 2000,
                                     "observed": True},
            "timing_window": {"in_flight_to_fifo": 1900, "exercised": True}}


MIN_IN_FLIGHT_REQUIRED = 1


def _self_test():
    """Mutation controls: every weakening must be visible in the exit code.

    Each control starts from a clean collected record, mutates exactly one
    fact the battery depends on, and requires a failure of the named class.
    Without these, the battery's own PASS would be an unfalsified claim.
    """

    # The shared provenance controls run here too, in the counted form every
    # other committed-report writer uses: this battery now owes those
    # guarantees, so it is measured by them in its own self-test rather than
    # only in command_sanitize.
    shared_executed = run_shared_self_test("aggregate")

    problems = []
    if shared_executed != command_sanitize.PROVENANCE_SELF_TEST_CHECKS:
        problems.append(
            f"the shared provenance controls ran {shared_executed} times, "
            f"expected {command_sanitize.PROVENANCE_SELF_TEST_CHECKS}: the "
            "committed-report guarantees this battery owes were not all "
            "executed")

    def verdicts(battery):
        return {failure.klass for failure in battery.failures}

    battery = Battery()
    judge_arm(battery, "rust/meta", _clean_arm_record(), attempts=4)
    if verdicts(battery):
        problems.append(f"clean arm judged as failing: {battery.failures}")
    if battery.exit_code() != 0:
        problems.append("clean arm must exit 0")

    mutations = [
        ("stable-regular answered an error",
         lambda record: record["controls"][0].update(
             {"shape": "-32010/name_not_found", "ok": False}),
         "control-unexpected"),
        ("stable-fifo control went silent",
         lambda record: record["controls"][1].update(
             {"shape": "timeout", "kind": "timeout", "ok": False}),
         "control-unexpected"),
        ("the swapper never replaced anything",
         lambda record: record.update(
             {"replacement_activity": {"to_fifo": 0, "to_regular": 0,
                                      "toggles": 0, "helper_errors": 0,
                                      "observed": False}}),
         "replacement-activity-absent"),
        ("the swapper only reached one node kind",
         lambda record: record.update(
             {"replacement_activity": {"to_fifo": 0, "to_regular": 500,
                                       "toggles": 500, "helper_errors": 0,
                                       "observed": True}}),
         "replacement-activity-incomplete"),
        ("no replacement landed inside a request window",
         lambda record: record.update(
             {"timing_window": {"in_flight_to_fifo": 0,
                                "in_flight_to_regular": 0,
                                "required_in_flight_to_fifo":
                                    MIN_IN_FLIGHT_REQUIRED,
                                "exercised": False}}),
         "timing-window-unexercised"),
        ("the engine never saw the raced FIFO",
         lambda record: record.update({"classes": {"RESULT": 4},
                                       "refusal_observed_in_race": 0}),
         "race-refusal-unobserved"),
        ("an attempt answered something the arm does not allow",
         lambda record: record.update(
             {"classes": {"RESULT": 2, "-32010/invalid_path": 1,
                          "io": 1}}),
         "unexpected-answer"),
        ("a product attempt stopped answering",
         lambda record: record.update({"hangs": [_clean_hang()],
                                       "attempts_completed": 3}),
         "engine-hang"),
        ("the record never said what the host was doing",
         lambda record: record.pop("host_context"),
         "host-context-missing"),
        ("the host context lost its sampled run queue",
         lambda record: record["host_context"].pop("run_queue"),
         "host-context-missing"),
        ("a hang was recorded without its detection-time sample",
         lambda record: record.update(
             {"hangs": [_clean_hang(host_at_detection=None)]}),
         "host-context-missing"),
        ("a hang sample disagrees with its own numbers",
         lambda record: record.update({"hangs": [_clean_hang(
             host_at_attempt_start=dict(_clean_sample(run_queue=4000, nproc=24),
                                        over_cpus=False))]}),
         "host-context-inconsistent"),
        ("the record's oversubscription flag disagrees with its median",
         lambda record: record["host_context"].update({"oversubscribed": True}),
         "host-context-inconsistent"),
        ("a hang on a massively oversubscribed host",
         lambda record: record.update({"hangs": [_starved_hang()],
                                       "attempts_completed": 3}),
         "engine-hang"),
        ("the arm quietly ran fewer attempts",
         lambda record: record.update({"attempts_completed": 2}),
         "attempts-incomplete"),
    ]
    for label, mutate, expected in mutations:
        record = _clean_arm_record()
        mutate(record)
        battery = Battery()
        judge_arm(battery, "rust/meta", record, attempts=4)
        if expected not in verdicts(battery):
            problems.append(f"mutation '{label}' did not produce {expected} "
                            f"(got {sorted(verdicts(battery))})")
        if battery.exit_code() != 1:
            problems.append(f"mutation '{label}' still exited 0")

    detector_mutations = [
        ("the detector never confirmed a wedge",
         lambda record: record.update({"confirmed_wedges": 0, "hangs": 3}),
         "detector-blind"),
        ("the detector phase never raced",
         lambda record: record.update(
             {"timing_window": {"in_flight_to_fifo": 0, "exercised": False}}),
         "timing-window-unexercised"),
        ("the naive opener saw no replacement activity",
         lambda record: record.update(
             {"replacement_activity": {"toggles": 0, "to_fifo": 0,
                                       "observed": False}}),
         "replacement-activity-absent"),
        ("the sentinel could not answer a regular file",
         lambda record: record.update({"stable_regular_ok": False}),
         "control-unexpected"),
        ("the detector record never said what the host was doing",
         lambda record: record.pop("host_context"),
         "host-context-missing"),
        ("the detector counted hangs it cannot show an observation for",
         lambda record: record.pop("hang_observations"),
         "host-context-missing"),
        ("the detector's observations do not match its hang count",
         lambda record: record.update({"hang_observations": [_clean_hang()]}),
         "host-context-missing"),
    ]
    for label, mutate, expected in detector_mutations:
        record = _clean_detector_record()
        mutate(record)
        battery = Battery()
        judge_detector(battery, "detector/sentinel", record)
        if expected not in verdicts(battery):
            problems.append(f"detector mutation '{label}' did not produce "
                            f"{expected} (got {sorted(verdicts(battery))})")
        if battery.exit_code() != 1:
            problems.append(f"detector mutation '{label}' still exited 0")
    battery = Battery()
    judge_detector(battery, "detector/sentinel", _clean_detector_record())
    if battery.failures:
        problems.append(f"clean detector judged as failing: {battery.failures}")

    # Report-location policy: the battery may never write committed evidence.
    for rejected in ("", "relative/dir"):
        try:
            check_report_dir_policy(rejected)
            problems.append(f"report-dir {rejected!r} was accepted")
        except ReportPolicyError:
            pass

    # ---- committed-report writer controls ---------------------------------
    # The report is committed evidence now, so three things have to fail
    # loudly instead of being assumed: the registry must claim the artifact and
    # agree with the options this battery screens; the shared writer must be
    # the only commit path -- for this module and for any module that claims
    # otherwise; and the provenance and privacy members the writer owns must
    # survive into the bytes a reviewer reads.
    def staged_report(directory, note="aggregate self-test"):
        """Write one artifact-shaped report: identities plus real records.

        The committed-report controls judge the bytes a reviewer reads, so the
        fixture has to look like a battery output: both identity blocks and one
        clean record per arm and control.
        """

        battery = Battery(
            binaries={"rust": {"path": os.path.join(directory, "rust-iprange"),
                               "sha256": "0" * 64}},
            fixture_tool={"path": os.path.join(directory, "v4-fixture"),
                          "sha256": "1" * 64},
            options={"arms": ["meta"], "attempts": 1, "deadline": 1.5})
        battery.add("rust/meta", _clean_arm_record(attempts=4))
        battery.add("detector/sentinel", _clean_detector_record())
        return battery.write(directory, note)

    def check_registry_owns_the_report():
        entry = registry_entry_for(REPORT_NAME)
        if entry is None:
            return [f"no registered writer claims {REPORT_NAME}, so the "
                    "battery is writing committed evidence nobody owns"]
        local = []
        if REPORT_NAME not in (entry.get("artifacts") or ()):
            local.append(f"the claiming entry does not list {REPORT_NAME!r}")
        if entry.get("tier") != SHARED_TIER:
            local.append(f"the entry sits on tier {entry.get('tier')!r}, not "
                         f"{SHARED_TIER!r}, so the call-site rules are waived")
        if tuple(entry.get("screened") or ()) != WRITER_SCREENED:
            local.append(f"the registry screens "
                         f"{tuple(entry.get('screened') or ())} while the "
                         f"battery hands {WRITER_SCREENED}; the two drift apart "
                         "the moment one of them is edited alone")
        return local

    def check_unregistered_writer_is_refused():
        import shutil, tempfile
        local = []
        root = None
        saved = command_sanitize.COMMITTED_REPORT_WRITERS.pop(WRITER_MODULE,
                                                              None)
        try:
            root = tempfile.mkdtemp(prefix="iprange-races-unregistered-")
            cli_dir = os.path.join(root, "cli")
            evidence = os.path.join(cli_dir, "evidence")
            os.makedirs(evidence)
            staged_report(evidence)

            def unclaimed():
                return [problem for problem
                        in command_sanitize.audit_committed_reports(
                            cli_dir=cli_dir)
                        if REPORT_NAME in problem
                        and "unregistered writer" in problem]

            with _restored_registry_entry(WRITER_MODULE, saved):
                if unclaimed():
                    local.append("a registered writer was reported as "
                                 "unregistered: " + "; ".join(unclaimed()))
            if not unclaimed():
                local.append("removing the registry entry changed nothing: a "
                             "committed report that no writer claims would be "
                             "accepted as evidence")
        except (OSError, ReportPolicyError, SystemExit) as exc:
            local.append(f"the control could not run "
                         f"({type(exc).__name__}: {exc})")
        finally:
            if saved is not None:
                command_sanitize.COMMITTED_REPORT_WRITERS[WRITER_MODULE] = saved
            if root is not None:
                shutil.rmtree(root, ignore_errors=True)
        return local

    def check_direct_serialization_is_refused():
        import shutil, tempfile
        local = []
        root = None
        synthetic = "races_synthetic_writer.py"
        try:
            root = tempfile.mkdtemp(prefix="iprange-races-writer-")
            os.makedirs(os.path.join(root, "evidence"), exist_ok=True)
            # Imports the sanctioned writer, screens its paths, calls it -- and
            # then serializes its own report anyway.  The import- and call-shape
            # rules alone do not catch that, which is why the bypass rule exists.
            with open(os.path.join(root, synthetic), "w",
                      encoding="utf-8") as stream:
                stream.write(
                    "import json\n"
                    "from command_sanitize import (require_paths_outside_profile,\n"
                    "                              write_committed_report)\n"
                    "def commit(path, tree):\n"
                    "    require_paths_outside_profile(('--tree', tree))\n"
                    "    write_committed_report(path, {'schema': 's'},\n"
                    "                           caller_paths=(('--tree', tree),))\n"
                    "    with open(path, 'w', encoding='utf-8') as handle:\n"
                    "        json.dump({'schema': 's'}, handle)\n"
                    "def shared(label):\n"
                    "    from command_sanitize import run_shared_self_test\n"
                    "    run_shared_self_test(label)\n")
            command_sanitize.COMMITTED_REPORT_WRITERS[synthetic] = {
                "artifacts": ("races-synthetic.json",),
                "screened": ("--tree",),
                "tier": SHARED_TIER,
                "owner": "aggregate self-test"}
            reported = audit_report_writers(cli_dir=root, writers=[synthetic],
                                            artifacts=False)
            if not any("serializes a report outside write_committed_report"
                       in problem for problem in reported):
                local.append("a writer that serialized its own json.dump() was "
                             "not named: " + "; ".join(reported))
        except OSError as exc:
            local.append(f"the control could not run ({exc})")
        finally:
            command_sanitize.COMMITTED_REPORT_WRITERS.pop(synthetic, None)
            if root is not None:
                shutil.rmtree(root, ignore_errors=True)
        return local

    def check_this_module_obeys_the_rules_it_is_registered_under():
        # Resolved against v4/cli, not this file's own directory: the registry
        # key carries the races subdirectory, exactly as the audit spells it.
        reported = audit_report_writers(
            cli_dir=_CLI, writers=[WRITER_MODULE], artifacts=False)
        return ([] if not reported else
                ["this module does not satisfy the writer rules it is "
                 "registered under: " + "; ".join(reported)])

    def check_privacy_block_survives_into_the_artifact():
        import copy, shutil, tempfile
        local = []
        root = None
        try:
            root = tempfile.mkdtemp(prefix="iprange-races-artifact-")
            path = staged_report(root)
            with open(path, encoding="utf-8") as stream:
                stored = json.load(stream)
            screened = (registry_entry_for(REPORT_NAME) or {}).get("screened")
            local.extend(committed_report_problems(
                stored, where=REPORT_NAME, require_privacy=True,
                screened=screened))
            privacy = stored.get("privacy") or {}
            if privacy.get("sanitizer") != PRIVACY_SANITIZER_NAME:
                local.append(f"privacy block was not written by "
                             f"{PRIVACY_SANITIZER_NAME}: "
                             f"{privacy.get('sanitizer')!r}")
            if sorted(privacy.get("checked_inputs") or []) != sorted(WRITER_SCREENED):
                local.append(f"privacy.checked_inputs "
                             f"{sorted(privacy.get('checked_inputs') or [])} is "
                             f"not the battery's path options "
                             f"{sorted(WRITER_SCREENED)}")
            stripped = copy.deepcopy(stored)
            stripped.pop("privacy", None)
            if not any("privacy" in problem for problem in
                       committed_report_problems(stripped, where="stripped",
                                                 require_privacy=True,
                                                 screened=screened)):
                local.append("an artifact with no privacy block was accepted")
            narrowed = copy.deepcopy(stored)
            narrowed["privacy"]["checked_inputs"] = [
                name for name in narrowed["privacy"]["checked_inputs"]
                if name != "--go"]
            if not any("stopped screening" in problem for problem in
                       committed_report_problems(narrowed, where="narrowed",
                                                 require_privacy=True,
                                                 screened=screened)):
                local.append("an artifact that stopped screening --go was "
                             "accepted")
        except (OSError, ReportPolicyError, SystemExit) as exc:
            local.append(f"the control could not run "
                         f"({type(exc).__name__}: {exc})")
        finally:
            if root is not None:
                shutil.rmtree(root, ignore_errors=True)
        return local

    def check_install_gate_refuses_what_the_step_must_refuse():
        """The rule-set step [16h] applies must actually bite.

        That step is the only path from a staged report to committed evidence,
        and it calls this module rather than restating the rules, so the rules
        are pinned here: the clean artifact passes, and each weakening that must
        stop an install does.
        """

        import copy, shutil, tempfile
        local = []
        root = None
        try:
            root = tempfile.mkdtemp(prefix="iprange-races-installegate-")
            path = staged_report(root)
            with open(path, encoding="utf-8") as stream:
                document = json.load(stream)
            if committed_artifact_problems(document):
                local.append("the clean artifact was refused by its own "
                             "producer: "
                             + "; ".join(committed_artifact_problems(document)))
            must_refuse = [
                ("no privacy block", lambda doc: doc.pop("privacy", None)),
                ("no host context on an arm record",
                 lambda doc: doc["entries"][0].pop("host_context", None)),
                ("a host context missing its run queue",
                 lambda doc: doc["entries"][0]["host_context"].pop(
                     "run_queue", None)),
                ("a hang counted without its observations",
                 lambda doc: doc["entries"][-1].update({"hangs": 5})),
                ("checkout_root recorded as a directory",
                 lambda doc: doc.__setitem__("checkout_root", root)),
                ("a command the sanitizer would rewrite",
                 lambda doc: doc.__setitem__(
                     "command", ["./" + doc["command"][0]]
                     if not doc["command"][0].startswith("./")
                     else doc["command"])),
            ]
            for label, mutate in must_refuse:
                mutated = copy.deepcopy(document)
                mutate(mutated)
                if not committed_artifact_problems(mutated, where=label):
                    local.append(f"{label} was accepted for install")
        except (OSError, ReportPolicyError, SystemExit) as exc:
            local.append(f"the control could not run "
                         f"({type(exc).__name__}: {exc})")
        finally:
            if root is not None:
                shutil.rmtree(root, ignore_errors=True)
        return local

    writer_controls = [
        ("the registry does not own this report", check_registry_owns_the_report),
        ("unregistered committed report", check_unregistered_writer_is_refused),
        ("non-shared-writer commit", check_direct_serialization_is_refused),
        ("own writer discipline", check_this_module_obeys_the_rules_it_is_registered_under),
        ("committed privacy block", check_privacy_block_survives_into_the_artifact),
        ("install gate", check_install_gate_refuses_what_the_step_must_refuse),
    ]
    for label, control in writer_controls:
        problems.extend(f"{label}: {problem}" for problem in control())

    if problems:
        for problem in problems:
            print(f"SELF-TEST FAIL: {problem}")
        return 1
    print(f"self-test ok: {len(mutations) + len(detector_mutations)} mutation "
          f"controls ({len(mutations)} arm, {len(detector_mutations)} detector) "
          f"plus {len(writer_controls)} committed-report writer controls and the "
          "clean-arm, clean-detector and report-location controls all behaved")
    return 0


def sha256_file(path):
    import hashlib
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()
