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
if _CLI not in sys.path:
    sys.path.insert(0, _CLI)

from command_sanitize import (  # noqa: E402
    personal_path_in_report,
    recorded_checkout_root,
    recorded_git_identity,
    sanitized_command,
    under_profile,
)

REPORT_SCHEMA = "iprange-cli-race-battery-report-v1"
REPORT_NAME = "race-battery.json"


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
        document = {
            "schema": REPORT_SCHEMA,
            "command": sanitized_command(),
            "checkout_root": recorded_checkout_root(),
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
        """Write the report and the per-subject logs; refuse unsafe output."""

        report_dir = check_report_dir_policy(report_dir)
        os.makedirs(report_dir, exist_ok=True)
        document = self.report(provenance_note)
        personal = personal_path_in_report(document)
        if personal is not None:
            raise ReportPolicyError(
                "refusing to write a race report containing the operator's "
                f"profile path: {personal!r}")
        path = os.path.join(report_dir, REPORT_NAME)
        with open(path, "w", encoding="utf-8") as stream:
            json.dump(document, stream, indent=2, sort_keys=True)
            stream.write("\n")
        return path

    def write_log(self, report_dir, name, lines):
        """Persist one per-arm transcript beside the report."""

        safe = "".join(char if char.isalnum() or char in "-_." else "-"
                       for char in name) or "log"
        path = os.path.join(report_dir, f"{safe}.log")
        with open(path, "w", encoding="utf-8") as stream:
            for line in lines:
                stream.write(line.rstrip("\n") + "\n")
        return path


# ---- the judgment, kept apart from execution so it can be tested -------

def judge_arm(battery, subject, record, *, attempts):
    """Turn one collected arm record into aggregate failures.

    The runner only measures; every claim ("the race was raced", "the control
    answered what the product promises") is decided here, against the frozen
    expectations carried inside the record. Keeping the decision pure is what
    lets ``--self-test`` mutate a collected record and require that the battery
    notices.
    """

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
                         f"wedge={control['wedge']})")
    for hang in record.get("hangs", []):
        battery.fail("engine-hang", subject,
                     f"attempt blocked in the open window: "
                     f"blocked={hang['blocked']} wedge={hang['wedge']}")
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
    }


def _clean_detector_record():
    return {"attempts": 6, "hangs": 3, "confirmed_wedges": 2,
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

    problems = []

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
         lambda record: record.update(
             {"hangs": [{"kind": "timeout", "shape": "timeout",
                         "blocked": ["tid1=wchan=pipe_read"],
                         "wedge": "wedge-confirmed:answered-after-writer-attach:fifo-0001"}],
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

    if problems:
        for problem in problems:
            print(f"SELF-TEST FAIL: {problem}")
        return 1
    print(f"self-test ok: {len(mutations) + len(detector_mutations)} "
          f"mutation controls plus the clean-arm, clean-detector and "
          "report-location controls all behaved")
    return 0


def sha256_file(path):
    import hashlib
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()
