#!/usr/bin/env python3
"""Negative-control battery for the kind-coverage gate (wave-19.18).

The gate (``check_kind_coverage.py``) must reject forged evidence, not
only genuine evidence.  This battery builds every known forgery class
from the GENUINE committed evidence (``v4/cli/evidence/``): each forged
report set is a deep copy of the genuine reports with one class of
mutation applied, then the gate CLI is invoked on the forged copies and
must reject them.  The genuine evidence is the positive control and must
be accepted.

Covered classes (kind-gate finding 7, wave-19.18 closure review):

- F3  fabricated cross-matrix PASS for a case the mixed runner skips
- F4  strip every ``live_sidecar`` open record from the matrix evidence
- F5  strip all consumer opens from the go->rust crash scenarios
- F6  empty assertions on a PASS crash scenario
- F7  relabel the Go binary record's top-level ``implementation``
- F8  actor sha256 tamper
- F9  lineage ref naming a non-executed method
- F10 matrix relabel
- F7a/F7b result-level implementation relabel
- F11 recorded binary path bound to a nonexistent file
- X1 the descriptor-pressure axis deleted from the parity verdict
- X2 a pressure rollup that contradicts its own executed cells
- X3 a cell whose recorded request frame belongs to another method

The gate is invoked as a subprocess, with the canonical flag set recorded
in ``v4/cli/evidence/README.md``:

    python3 check_kind_coverage.py --matrix <one per matrix, x4>
        --crash evidence/crash.json
        --fifo-surface evidence/fifo-surface.json
        --throughput evidence/throughput.json
        --refusal-class-parity evidence/refusal-class-parity.json
        --coverage-go evidence/coverage-go.json
        --windows-housekeeping evidence/windows-housekeeping.json
        --windows-guard evidence/windows-guard.json
        --resource evidence/resource.json
        --golden evidence/golden.json
        --sensitivity evidence/sensitivity.json
        --guard-posix evidence/guard-posix.json
        --race-battery evidence/race-battery.json [--sha256-ledger PATH]

``crash.json`` is the only crash report handed over: its scenarios are
the accepted crash evidence the gate consumes, and
``crash-negative.json`` is the crash harness's own all-fail control, so
handing it to the gate would make every run red for a reason this battery
does not test.  ``--fifo-surface`` and ``--throughput`` are required by
the gate because their verdicts are consumed evidence: omitting either is
a gate usage error, not a weaker check.  ``--sha256-ledger`` is off by
default so the battery stays runnable standalone against the committed
evidence; supply the staged ledger to bind every recorded binary digest
to it.

The gate's own doctored-report self-test is not run here: it is the
gate's ``--self-test`` step, which the wave battery runs separately with
its own exit code.  This battery judges only verdicts the gate reaches on
the reports handed to it.

A battery whose gate never ran is indistinguishable, by exit code alone,
from a battery that caught every class, so three guards make that
impossible here instead of relying on a reader noticing:

- G1 usage guard: the gate runs once on an unmutated copy of the genuine
  evidence before any class runs, and every gate call must produce a real
  verdict.  An argparse exit (rc 2), a missing verdict banner, an exit
  code outside {0, 1}, rc 1 with no ``FAIL:`` line, or rc 0 with no
  ``PASS:`` line aborts the whole battery as ``BATTERY HARNESS BROKEN``
  (exit 2).  This is what turns gate CLI drift -- a newly required flag,
  a renamed option, a gate that dies before reading the reports -- into a
  loud failure rather than N identical non-verdicts reported as CAUGHT.
- G2 mutation guard: each class must leave the report set different from
  the genuine one.  A mutator that matches nothing (a stale binary path or
  field name) is a defect in this battery, not a forgery the gate failed
  to catch.
- G3 reason guard: a class is CAUGHT only when the gate reports at least
  one ``FAIL:`` line that the unmutated baseline does not report.  A
  rejection carrying only baseline reasons proves nothing about the class
  and is reported as NOT-INFORMATIVE.

The guard self-test (``--self-test``, and run on every invocation) proves
each guard can fire, because a guard that cannot fire is not a guard.

Exit codes: 0 the positive control was accepted and every class was caught
on its own grounds; 1 a class was accepted, caught only for a shared
baseline reason, two classes share one reason set, or the positive control
was rejected (which includes committed evidence that is mid-rotation); 2
this battery's own harness is broken (G1, G2, or the guard self-test).

Run under ``nice`` (resource policy):
``nice python3 v4/cli/forgery_battery.py``.
Each gate invocation is sub-second over the small committed reports, so
the whole battery finishes in seconds.
"""

import argparse
import collections
import copy
import json
import os
import subprocess
import sys
import tempfile

# The sanitizer module provides the shared owned temp root used by all
# v4 harness scratch (keeps scratch outside the checkout).
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from command_sanitize import owned_temp_root  # noqa: E402

EVIDENCE = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                        "evidence")
GATE = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                    "check_kind_coverage.py")
MATRIX_FILES = [os.path.join(EVIDENCE, f"matrix-{name}.json")
                for name in ("rust", "go", "rust_to_go", "go_to_rust")]
CRASH_FILE = os.path.join(EVIDENCE, "crash.json")
FIFO_SURFACE_FILE = os.path.join(EVIDENCE, "fifo-surface.json")
THROUGHPUT_FILE = os.path.join(EVIDENCE, "throughput.json")
PARITY_FILE = os.path.join(EVIDENCE, "refusal-class-parity.json")
COVERAGE_FILE = os.path.join(EVIDENCE, "coverage-go.json")
WINDOWS_FILE = os.path.join(EVIDENCE, "windows-housekeeping.json")
WINDOWS_GUARD_FILE = os.path.join(EVIDENCE, "windows-guard.json")
RESOURCE_FILE = os.path.join(EVIDENCE, "resource.json")
GOLDEN_FILE = os.path.join(EVIDENCE, "golden.json")
SENSITIVITY_FILE = os.path.join(EVIDENCE, "sensitivity.json")
GUARD_POSIX_FILE = os.path.join(EVIDENCE, "guard-posix.json")
RACE_FILE = os.path.join(EVIDENCE, "race-battery.json")
MANIFEST_FILE = os.path.join(EVIDENCE, "battery-manifest.json")
# A negative control is one report per faked role, so the battery carries
# every crash-negative* file the evidence directory holds.
CRASH_NEGATIVE_FILES = sorted(
    os.path.join(EVIDENCE, name) for name in os.listdir(EVIDENCE)
    if name.startswith("crash-negative") and name.endswith(".json"))

# The report classes the gate consumes besides matrices and crash, with the
# flag that names each.  The battery passes every flag explicitly: a class
# left to the gate's own discovery would be read from the committed directory
# instead of from this run's sandbox, and a mutated copy would then be judged
# against the untouched original.
BATTERY_FLAGS = (("parity", "--refusal-class-parity"),
                 ("coverage", "--coverage-go"),
                 ("windows", "--windows-housekeeping"),
                 ("guard", "--windows-guard"),
                 ("resource", "--resource"),
                 ("golden", "--golden"),
                 ("sensitivity", "--sensitivity"),
                 ("posix_guard", "--guard-posix"),
                 ("race", "--race-battery"))

from check_kind_coverage import build_battery_manifest  # noqa: E402


class Bundle:
    """Every report one gate invocation consumes, as a class mutates it.

    The gate's verdict covers fourteen report classes, so the battery's
    comparison of a forged set against the genuine set -- and the command it
    hands the gate -- has to cover the same fourteen.  ``manifest`` is the report
    set's content binding; it stays ``None`` for every class except the one
    that attacks the binding itself, which points the gate at the committed
    manifest while it rewrites the reports.
    """

    __slots__ = ("matrices", "crash", "fifo", "throughput", "parity",
                 "coverage", "negatives", "windows", "guard", "resource",
                 "golden", "sensitivity", "posix_guard", "race", "manifest")

    def __init__(self, **fields):
        for name in self.__slots__:
            setattr(self, name, fields.get(name))

    def state(self):
        """What the G2 no-op guard compares between runs."""
        return (self.matrices, self.crash, self.fifo, self.throughput,
                self.parity, self.coverage, self.negatives, self.windows,
                self.guard, self.resource, self.golden, self.sensitivity,
                self.posix_guard, self.race, self.manifest)


def _read(path):
    with open(path, encoding="utf-8") as stream:
        return json.load(stream)


def _load_bundle():
    return Bundle(
        matrices=[_read(path) for path in MATRIX_FILES],
        crash=_read(CRASH_FILE),
        fifo=_read(FIFO_SURFACE_FILE),
        throughput=_read(THROUGHPUT_FILE),
        parity=_read(PARITY_FILE),
        coverage=_read(COVERAGE_FILE),
        negatives=[_read(path) for path in CRASH_NEGATIVE_FILES],
        windows=_read(WINDOWS_FILE),
        guard=_read(WINDOWS_GUARD_FILE),
        resource=_read(RESOURCE_FILE),
        golden=_read(GOLDEN_FILE),
        sensitivity=_read(SENSITIVITY_FILE),
        posix_guard=_read(GUARD_POSIX_FILE),
        race=_read(RACE_FILE))

# What the gate prints when it actually evaluated a report set.  Their
# presence is what separates a gate verdict from an argparse error or an
# interpreter traceback that exited 1 on the way to neither.
GATE_BANNER = "Artifact-kind coverage gate"
GATE_REJECT_PREFIX = "FAIL: "
GATE_PASS_MARKER = ("PASS: every required artifact kind has "
                      "both-language evidence")

# How many of a class's own failure reasons the summary prints in full.
MAX_REASONS_PER_CLASS = 3
REASON_WIDTH = 140

GateResult = collections.namedtuple("GateResult", "rc problems keys")


class BatteryHarnessError(RuntimeError):
    """This battery failed to ask the gate a valid question.

    Raised for gate CLI rejection (argparse rc 2), a gate run that printed
    no verdict, an unexpected exit code, and a mutation that changed
    nothing.  Each is a defect in the battery or its invocation, so it
    must stop the run rather than be counted as a rejection of a forgery.
    """


# Each class: (label, mutator(matrices, crash)).  The mutator edits the
# in-memory deep copies in place; the battery serializes them to the
# sandbox and runs the gate CLI on the copies.
FORGERIES = []

# Eleven classes cover the matrix and crash report forgeries this battery was
# written for; seven more, one per consumed-artifact item, cover the parity
# verdict, the coverage measurement, the negative crash controls, the FIFO
# inventory, the thread census, the Windows provenance, and the manifest that
# binds the report set to a revision.  Three further classes attack the two
# parity holes the external review closed: a verdict whose third (pressure)
# axis was deleted, a pressure rollup that disagrees with its own executed
# cells, and a cell whose recorded request frame is another method's.
EXPECTED_CLASSES = 21


def _forgery(label, whole_bundle=False):
    """Register one forgery class.

    ``whole_bundle`` classes edit the reports the wave added (parity,
    coverage, the negative controls, the surface reports, the Windows
    qualification) and receive the ``Bundle``; the older classes keep their
    ``(matrices, crash)`` signature and are adapted here rather than rewritten,
    so the existing classes stay untouched by this widening.
    """

    def register(mutator):
        if not whole_bundle:
            adapted = mutator

            def mutator(bundle, adapted=adapted):
                return adapted(bundle.matrices, bundle.crash)
        FORGERIES.append((label, mutator))
        return mutator
    return register


@_forgery("F3-fabricate-cross-matrix-pass")
def f3(matrices, crash):
    report = matrices[2]  # rust_to_go
    donor = next(c for c in report["cases"] if c["status"] == "PASS")
    target = next(c for c in report["cases"]
                  if c["status"] == "SKIP"
                  and c["name"] == "algebra.publish")
    forged = {
        "matrix": report["matrix"], "name": target["name"],
        "status": "PASS",
        "actors": copy.deepcopy(donor["actors"]),
        "file_kinds": copy.deepcopy(donor["file_kinds"]),
        "oracle_checks": donor.get("oracle_checks", 0),
    }
    report["cases"] = [
        c for c in report["cases"] if c["name"] != target["name"]
    ] + [forged]
    report["passed"] = sum(1 for c in report["cases"]
                           if c["status"] == "PASS")
    report["skipped"] = len(report["cases"]) - report["passed"]


@_forgery("F4-strip-matrix-sidecar-opens")
def f4(matrices, crash):
    for report in matrices:
        root_kinds = report.get("file_kinds") or {}
        if "live_sidecar" in root_kinds:
            root_kinds["live_sidecar"].pop("opened_by", None)
        for case in report["cases"]:
            if case.get("status") != "PASS":
                continue
            for facts in (case.get("file_kinds") or {}).values():
                if facts.get("kind") == "live_sidecar":
                    facts.pop("opened_by", None)


@_forgery("F5-strip-crash-consumer-opens")
def f5(matrices, crash):
    stripped = 0
    for scenario in crash["scenarios"]:
        if "->rust" in scenario.get("scenario", "") and stripped < 8:
            for facts in (scenario.get("kinds") or {}).values():
                if not isinstance(facts, dict):
                    continue
                facts["opened_by"] = [
                    ref for ref in facts.get("opened_by", [])
                    if not ref.startswith("consumer")]
            scenario.setdefault("live_reader_opens", {})["consumer"] = 0
            stripped += 1


@_forgery("F6-crash-empty-assertions")
def f6(matrices, crash):
    crash["scenarios"][0]["assertions"] = []


@_forgery("F7-binary-record-root-relabel")
def f7(matrices, crash):
    matrices[1]["binaries"]["go"]["implementation"] = "rust"


@_forgery("F8-actor-sha-tamper")
def f8(matrices, crash):
    matrices[1]["cases"][0]["actors"]["consumer"]["sha256"] = "a" * 64


@_forgery("F9-kind-ref-non-executed-method")
def f9(matrices, crash):
    for report in matrices:
        for case in report["cases"]:
            if case.get("status") != "PASS":
                continue
            for facts in (case.get("file_kinds") or {}).values():
                if facts.get("created_by"):
                    facts["created_by"] = [
                        "consumer.iprange.v1.system.describe"]


@_forgery("F10-matrix-relabel")
def f10(matrices, crash):
    matrices[2]["matrix"] = "go"
    matrices[2]["command"] = [
        "go" if value == "rust_to_go" else value
        for value in matrices[2]["command"]]


@_forgery("F7a-relabel-one-report")
def f7a(matrices, crash):
    matrices[1]["binaries"]["go"]["result"]["implementation"] = "rust"


@_forgery("F7b-relabel-all-reports")
def f7b(matrices, crash):
    for report in matrices:
        binary = report.get("binaries", {}).get("go")
        if isinstance(binary, dict) and isinstance(
                binary.get("result"), dict):
            binary["result"]["implementation"] = "rust"


@_forgery("F11-binary-path-missing")
def f11(matrices, crash):
    """Point the go binary identity at a file that does not exist.

    The path is read back out of the report rather than hardcoded: the
    staged binary location moves every wave, and a stale literal here
    would silently stop rewriting the command and actor bindings while the
    class still reported CAUGHT for the path change alone.
    """
    report = matrices[1]  # matrix-go
    recorded = report["binaries"]["go"]["path"]
    missing = "/nonexistent/iprange-go"
    report["binaries"]["go"]["path"] = missing
    report["command"] = [
        missing if value == recorded else value
        for value in report["command"]]
    for case in report["cases"]:
        if case.get("status") != "PASS":
            continue
        for entry in (case.get("actors") or {}).values():
            if entry.get("argv") == recorded:
                entry["argv"] = missing


# --- the seven consumed-artifact classes (wave-19.25 items 1-7).
#
# Each is the exact forgery the gate was reported as accepting, on the report
# class the gate did not read at all before this wave.  Genuine evidence must
# still be accepted (the f0 baseline), and each class must be rejected for a
# reason the baseline does not report (guard G3).


@_forgery("W1-parity-one-binary-swept-twice", whole_bundle=True)
def w1(bundle):
    # --go <rust binary>: the sweep ran one executable twice, so every cell
    # compares the rust engine with itself and still reads as agreement.
    rust = bundle.parity["binaries"]["rust"]["sha256"]
    bundle.parity["binaries"]["go"]["sha256"] = rust


@_forgery("W2-coverage-percent-typed-not-derived", whole_bundle=True)
def w2(bundle):
    bundle.coverage["unit"]["percent"]["statements"] = (
        float(bundle.coverage["unit"]["percent"]["statements"]) + 5.0)


@_forgery("W3-negative-control-reports-a-pass", whole_bundle=True)
def w3(bundle):
    # The /bin/false control exists to prove the crash battery can see a
    # failure.  A PASS in it is the finding, not a result.
    for report in bundle.negatives:
        for scenario in report["scenarios"]:
            scenario["pass"] = True
            scenario["failures"] = []
        report["failed"] = 0


@_forgery("W4-fifo-arm-row-deleted", whole_bundle=True)
def w4(bundle):
    for row in bundle.fifo["arms"]:
        if row.get("engine") == "go":
            bundle.fifo["arms"] = [item for item in bundle.fifo["arms"]
                                   if item is not row]
            bundle.fifo["summary"]["arms_expected"] -= 1
            return


@_forgery("W5-threaded-round-one-child-identity", whole_bundle=True)
def w5(bundle):
    side = bundle.throughput["product"]["rust"]["thread_structure"]["large"]
    side["unique_child_tids"] = 1


@_forgery("W6-windows-native-test-names-red", whole_bundle=True)
def w6(bundle):
    bundle.windows["build_provenance"]["native_cargo_test"] = (
        "cargo test -p iprange-cli on the native Windows host returned rc=1: "
        "330 passed, 4 FAILED (path-separator cases)")


@_forgery("X1-parity-pressure-member-deleted", whole_bundle=True)
def x1(bundle):
    # The third axis is an obligation of the parity verdict.  Deleting the
    # block used to shrink the claim instead of contradicting it: the two
    # surviving axes still said 483 cells x 2 engines, and the gate agreed.
    bundle.parity.pop("pressure", None)


@_forgery("X2-parity-pressure-count-contradiction", whole_bundle=True)
def x2(bundle):
    # The axis stays, and its counters stop following its own cells: a
    # pressure section that reports fewer executed cells than it carries is
    # the same claim written after the fact.
    bundle.parity["pressure"]["cells_executed"] -= 1


@_forgery("X3-parity-request-frame-method-swapped", whole_bundle=True)
def x3(bundle):
    # A cell certifies the refusal class of the frame it says it sent.  Handing
    # the reader.open cell another method's request keeps the cell's own label,
    # the pinned class, and the agreement verdict, and is visible only against
    # the frame inside the record.
    describe = ('{"jsonrpc":"2.0","id":1,"method":'
                '"iprange.v1.system.describe","params":{}}')
    cell = next(entry for entry in bundle.parity["cells"]
                if entry.get("arm") == "reader.open")
    cell["go"]["request"] = describe


@_forgery("W7-uniform-git-head-rewrite", whole_bundle=True)
def w7(bundle):
    # Every report re-stamped to one fresh revision satisfies the rule that
    # compares reports with each other, and is only visible against the
    # committed binding of this report set to the revision it was produced on.
    forged = "ab" * 20
    for report in list(bundle.matrices) + [
        bundle.crash, bundle.fifo, bundle.throughput, bundle.parity,
        bundle.coverage, bundle.windows, bundle.guard, bundle.resource,
        bundle.golden, bundle.sensitivity, bundle.posix_guard, bundle.race]:
        report["git_head"] = forged
    for report in bundle.negatives:
        report["git_head"] = forged
        provenance = report.get("build_provenance")
        if isinstance(provenance, dict):
            provenance["revision"] = forged
    bundle.manifest = MANIFEST_FILE


def _parse_args(argv):
    parser = argparse.ArgumentParser(
        description="Negative-control battery for the kind-coverage gate.")
    parser.add_argument("--sha256-ledger", default=None, metavar="PATH",
                        help="sha256sum-format ledger of the staged "
                             "binaries, forwarded to the gate; when omitted "
                             "the battery runs standalone against the "
                             "committed evidence")
    parser.add_argument("--self-test", action="store_true",
                        help="prove the guards fire and exit")
    return parser.parse_args(argv)


def _gate_command(paths, ledger):
    """The canonical gate invocation, as documented in evidence/README.md.

    ``paths`` maps each consumed report class to the files of this run's
    sandbox that hold it, so every class is named rather than discovered: the
    battery judges the copies it mutated, not the committed originals that
    discovery beside the battery would find.
    """
    command = [GATE]
    for path in paths["matrix"]:
        command += ["--matrix", path]
    for path in paths["crash"]:
        command += ["--crash", path]
    command += ["--fifo-surface", paths["fifo"][0],
                "--throughput", paths["throughput"][0]]
    for name, flag in BATTERY_FLAGS:
        for path in paths[name]:
            command += [flag, path]
    for path in paths["crash-negative"]:
        command += ["--crash-negative", path]
    command += ["--battery-manifest", paths["battery-manifest"][0]]
    if ledger:
        command += ["--sha256-ledger", ledger]
    return command


def _normalize(problems, tokens):
    """Rewrite every report path in a problem to its positional token.

    The gate names the report file in each problem, and each class runs on
    its own copy under its own tag, so the raw strings of a shared baseline
    defect differ from the baseline only in that filename.  Comparing them
    raw would let a baseline reason masquerade as a class's own reason,
    which is the false success G3 exists to prevent.  Display keeps the raw
    strings so a reader can find the exact report.
    """
    normalized = []
    for problem in problems:
        for path, token in tokens:
            problem = problem.replace(path, token)
            problem = problem.replace(os.path.basename(path), token)
        normalized.append(problem)
    return normalized


def _verdict(rc, stdout, stderr, command, tokens=()):
    """Gate problems for a run, or abort when there is no verdict (G1)."""
    if rc == 2:
        raise BatteryHarnessError(
            "the gate rejected the battery's command line with an argparse "
            "usage error (rc=2), so no report was ever evaluated and no "
            "class in this battery proved anything; the gate CLI contract "
            "changed and this battery was not updated with it\n"
            f"  command: {' '.join(command)}\n"
            f"  stderr: {stderr.strip()[:2000]}")
    if rc not in (0, 1):
        raise BatteryHarnessError(
            f"the gate exited {rc}, which is neither acceptance (0) nor "
            f"rejection (1)\n  command: {' '.join(command)}\n"
            f"  stderr: {stderr.strip()[:2000]}")
    if GATE_BANNER not in stdout:
        raise BatteryHarnessError(
            f"the gate exited {rc} without printing its verdict banner "
            f"{GATE_BANNER!r}; the run died before evaluating the reports\n"
            f"  command: {' '.join(command)}\n"
            f"  stderr: {stderr.strip()[:2000]}")
    raw = [line[len(GATE_REJECT_PREFIX):]
           for line in stdout.splitlines()
           if line.startswith(GATE_REJECT_PREFIX)]
    if rc == 1 and not raw:
        raise BatteryHarnessError(
            "the gate exited 1 without a single FAIL: line, so the "
            "rejection has no stated reason\n"
            f"  command: {' '.join(command)}\n"
            f"  stderr: {stderr.strip()[:2000]}")
    if rc == 0 and GATE_PASS_MARKER not in stdout:
        raise BatteryHarnessError(
            f"the gate exited 0 without printing {GATE_PASS_MARKER!r}; an "
            "acceptance with no verdict line is not an acceptance\n"
            f"  command: {' '.join(command)}")
    return GateResult(rc, raw, _normalize(raw, tokens))


def _invoke_gate(command):
    """Run the gate CLI as a child process and collect its verdict."""
    completed = subprocess.run(["python3"] + command, capture_output=True,
                               text=True)
    return completed.returncode, completed.stdout, completed.stderr


def _write(work_dir, tag, name, document, index=None):
    """Serialize one report of this run into the sandbox."""
    suffix = "" if index is None else f"{index}"
    path = os.path.join(work_dir, f"{tag}-{name}{suffix}.json")
    with open(path, "w", encoding="utf-8") as stream:
        json.dump(document, stream)
    return path


def _run_gate(work_dir, bundle, tag, ledger):
    """Serialize one report set to the sandbox and ask the gate about it.

    The content binding is written here, by the gate's own producer, over the
    reports this run actually hands over: the battery and the gate therefore
    agree on what the battery is without the battery keeping a second copy of
    the rule.  A class that attacks the binding sets ``bundle.manifest`` and
    this step honours that path instead.
    """
    paths = {}
    tokens = []
    paths["matrix"] = []
    for index, report in enumerate(bundle.matrices):
        path = _write(work_dir, tag, f"m{index}", report)
        paths["matrix"].append(path)
        tokens.append((path, f"matrix#{index + 1}"))
    paths["crash"] = [_write(work_dir, tag, "crash", bundle.crash)]
    tokens.append((paths["crash"][0], "crash#0"))
    for name, document in (("fifo", bundle.fifo),
                           ("throughput", bundle.throughput),
                           ("parity", bundle.parity),
                           ("coverage", bundle.coverage),
                           ("windows", bundle.windows),
                           ("guard", bundle.guard),
                           ("resource", bundle.resource),
                           ("golden", bundle.golden),
                           ("sensitivity", bundle.sensitivity),
                           ("posix_guard", bundle.posix_guard),
                           ("race", bundle.race)):
        paths[name] = [_write(work_dir, tag, name, document)]
        tokens.append((paths[name][0], f"{name}#0"))
    paths["crash-negative"] = []
    for index, report in enumerate(bundle.negatives):
        path = _write(work_dir, tag, f"negative{index}", report)
        paths["crash-negative"].append(path)
        tokens.append((path, f"crash-negative#{index}"))
    if bundle.manifest:
        paths["battery-manifest"] = [bundle.manifest]
    else:
        # The manifest speaks the gate's role names; the sandbox speaks the
        # battery's short names.  One mapping, in one place.
        document = build_battery_manifest(
            {"matrix": paths["matrix"], "crash": paths["crash"],
             "crash-negative": paths["crash-negative"],
             "fifo-surface": paths["fifo"],
             "throughput": paths["throughput"],
             "refusal-class-parity": paths["parity"],
             "coverage-go": paths["coverage"],
             "windows-housekeeping": paths["windows"],
             "windows-guard": paths["guard"],
             "resource": paths["resource"],
             "golden": paths["golden"],
             "sensitivity": paths["sensitivity"],
             "guard-posix": paths["posix_guard"],
             "race-battery": paths["race"]},
            ledger_path=ledger)
        manifest_path = os.path.join(work_dir, f"{tag}-manifest.json")
        with open(manifest_path, "w", encoding="utf-8") as stream:
            json.dump(document, stream, sort_keys=True, indent=1)
        paths["battery-manifest"] = [manifest_path]
    command = _gate_command(paths, ledger)
    rc, stdout, stderr = _invoke_gate(command)
    return _verdict(rc, stdout, stderr, command, tokens)


def _clip(text):
    return text if len(text) <= REASON_WIDTH else text[:REASON_WIDTH] + "..."


def _print_reasons(problems):
    for problem in problems[:MAX_REASONS_PER_CLASS]:
        print(f"      reason: {_clip(problem)}")
    extra = len(problems) - MAX_REASONS_PER_CLASS
    if extra > 0:
        print(f"      ... and {extra} more")


def _classify(result, baseline_keys):
    """Judge one class run against the unmutated baseline (G3).

    A rejection is worth what it states, not that it is non-zero: a class
    whose problems are all baseline problems was rejected by the evidence
    state, not by its own mutation, and proves nothing about the class.

    Returns ``(verdict, note, own)`` where ``own`` holds
    ``(normalized key, raw problem)`` pairs for the problems the baseline
    does not report.
    """
    own = [(key, problem) for problem, key
           in zip(result.problems, result.keys) if key not in baseline_keys]
    if result.rc == 0:
        return "ACCEPTED", "**GAP**", own
    if not own:
        return "NOT-INFORMATIVE", "**NO OWN REASON**", own
    return "CAUGHT", "OK", own


def _duplicate_groups(new_reasons):
    """Class labels rejected for one identical own-reason set.

    Two classes caught for byte-identical reasons means one of them is not
    testing what it claims; the pair is reported instead of being credited
    twice.
    """
    by_reasons = {}
    for label, reasons in new_reasons.items():
        if reasons:
            by_reasons.setdefault(reasons, []).append(label)
    return [group for group in by_reasons.values() if len(group) > 1]


def _run_battery(ledger):
    """Run the positive control and every class; return the exit code."""
    with tempfile.TemporaryDirectory(dir=owned_temp_root()) as work:
        # G1 + positive control: the unmutated genuine evidence must reach a
        # gate verdict before any forgery is worth judging.
        baseline = _run_gate(work, _load_bundle(), "f0-baseline", ledger)
        baseline_keys = set(baseline.keys)
        control_accepted = baseline.rc == 0
        print(f"[f0-genuine-evidence] rc={baseline.rc} -> "
              f"{'OK' if control_accepted else 'FAIL'}  expect_pass=True")
        for problem in baseline.problems:
            print(f"      baseline: {_clip(problem)}")
        if not control_accepted:
            print("      (baseline reasons are shared by every class and are "
                  "excluded from what counts as a class's own reason)")

        new_reasons = {}
        gaps = []
        uninformative = []
        for label, mutator in FORGERIES:
            bundle = _load_bundle()
            reference = copy.deepcopy(bundle.state())
            mutator(bundle)
            if bundle.state() == reference:
                raise BatteryHarnessError(
                    f"{label}: the mutator left the report set identical to "
                    "the genuine evidence, so this class tests nothing; a "
                    "field or path it rewrites no longer exists in the "
                    "committed reports")
            result = _run_gate(work, bundle, label, ledger)
            verdict, note, own = _classify(result, baseline_keys)
            new_reasons[label] = tuple(sorted(key for key, _ in own))
            if verdict == "ACCEPTED":
                gaps.append(label)
            elif verdict == "NOT-INFORMATIVE":
                uninformative.append(label)
            print(f"[{label}] rc={result.rc} -> {verdict}  "
                  f"expect_reject=True  {note}")
            _print_reasons([problem for _key, problem in own])

    duplicated = _duplicate_groups(new_reasons)

    print()
    print("Battery guards:")
    print(f"  G1 gate usage: OK -- {len(FORGERIES) + 1} gate calls, each one "
          "printed a verdict; no argparse (rc=2) error, no reason-less "
          "rejection, no acceptance without a PASS line")
    print(f"  G2 mutation: OK -- all {len(FORGERIES)} classes changed the "
          "report set they claim to change")
    caught = len(new_reasons) - len(gaps) - len(uninformative)
    print(f"  G3 own reasons: {caught}/{len(FORGERIES)} classes rejected on a "
          "reason the unmutated baseline does not report"
          + ("" if not duplicated else
             f"; {len(duplicated)} reason set(s) shared by more than one "
             "class"))
    preview = _gate_command(
        {"matrix": ["<matrix x4>"], "crash": ["<crash.json>"],
         "fifo": ["<fifo-surface.json>"],
         "throughput": ["<throughput.json>"],
         "parity": ["<refusal-class-parity.json>"],
         "coverage": ["<coverage-go.json>"],
         "windows": ["<windows-housekeeping.json>"],
         "guard": ["<windows-guard.json>"],
         "resource": ["<resource.json>"],
         "golden": ["<golden.json>"],
         "sensitivity": ["<sensitivity.json>"],
         "posix_guard": ["<guard-posix.json>"],
         "race": ["<race-battery.json>"],
         "crash-negative": ["<crash-negative.json>"],
         "battery-manifest": ["<battery-manifest.json>"]}, ledger)
    print("  gate invocation: python3 " + " ".join(preview))
    print("  positive control: "
          + ("accepted (rc=0)" if control_accepted else
             f"REJECTED (rc={baseline.rc}) on {len(baseline.problems)} "
             "baseline reason(s)"))

    if gaps:
        print("VERDICT: BATTERY GAP -- the gate accepted " + ", ".join(gaps))
        return 1
    if uninformative:
        print("VERDICT: BATTERY GAP -- caught only for a shared baseline "
              "reason: " + ", ".join(uninformative))
        return 1
    if duplicated:
        print("VERDICT: BATTERY GAP -- classes caught for an identical reason "
              "set: "
              + "; ".join(", ".join(group) for group in duplicated))
        return 1
    if not control_accepted:
        print("VERDICT: HARNESS OK, EVIDENCE RED -- the gate evaluated every "
              "report set and each class was rejected on its own reason, but "
              "the unmutated committed evidence is itself rejected; this is "
              "evidence mid-rotation, and it expects green once the wave "
              "regenerates all evidence in one pass")
        return 1
    print("VERDICT: PASS -- every class rejected on its own distinct reason, "
          "genuine evidence accepted")
    return 0


def _self_test():
    """Prove each guard can fire: a guard that cannot fire is not a guard.

    P1 drives the real gate CLI with each flag it declares required removed
    and requires the usage-error classification.  That is the regression
    this battery carries: twelve classes reported CAUGHT while the gate had
    read nothing.  P2 and P3 push bare exit codes through a real subprocess,
    so the "no verdict was produced" classification is checked against a
    process rather than a mock.  P4 to P7 are pure cases for the verdict and
    reason comparison, and P8 proves the G2 comparison discriminates.
    """
    def expect_harness_error(action, needle, case):
        try:
            action()
        except BatteryHarnessError as exc:
            assert needle in str(exc), (
                f"{case}: the guard fired for the wrong reason: {exc}")
            return exc
        raise AssertionError(f"{case}: the guard accepted what it must stop")

    def gate_result(rc, raw, tokens):
        return GateResult(rc, raw, _normalize(raw, tokens))

    labels = [label for label, _mutator in FORGERIES]
    assert labels and len(set(labels)) == len(labels), (
        f"the battery registers {len(labels)} classes with duplicate or "
        f"empty labels: {labels}")

    # P1: the gate declares --fifo-surface and --throughput required; an
    # invocation without either must be classified as a broken harness, not
    # as a rejection of the forgery under test.
    canonical = _gate_command(
        {"matrix": list(MATRIX_FILES), "crash": [CRASH_FILE],
         "fifo": [FIFO_SURFACE_FILE], "throughput": [THROUGHPUT_FILE],
         "parity": [PARITY_FILE], "coverage": [COVERAGE_FILE],
         "windows": [WINDOWS_FILE],
         "guard": [WINDOWS_GUARD_FILE],
         "resource": [RESOURCE_FILE],
         "golden": [GOLDEN_FILE],
         "sensitivity": [SENSITIVITY_FILE],
         "posix_guard": [GUARD_POSIX_FILE],
         "race": [RACE_FILE],
         "crash-negative": list(CRASH_NEGATIVE_FILES),
         "battery-manifest": [MANIFEST_FILE]}, None)
    for flag in ("--fifo-surface", "--throughput"):
        cut = canonical.index(flag)
        truncated = canonical[:cut] + canonical[cut + 2:]
        rc, stdout, stderr = _invoke_gate(truncated)
        assert rc == 2, (
            f"P1 {flag} omitted: the gate exited {rc}, so it no longer "
            "declares this flag required and this control is stale")
        expect_harness_error(
            lambda rc=rc, stdout=stdout, stderr=stderr,
                   truncated=truncated: _verdict(
                       rc, stdout, stderr, truncated),
            "rc=2", f"P1 {flag} omitted")

    # P2/P3: real subprocesses that exit without ever printing a verdict.
    for code, needle in ((2, "rc=2"), (1, "verdict banner")):
        rc, stdout, stderr = _invoke_gate(
            ["-c", f"import sys; sys.stderr.write('synthetic\\n'); "
                   f"sys.exit({code})"])
        expect_harness_error(
            lambda rc=rc, stdout=stdout, stderr=stderr: _verdict(
                rc, stdout, stderr, ["<synthetic>"]),
            needle, f"P synthetic exit rc={code}")

    # P4/P5: rc 1 with no stated reason and rc 0 with no verdict line are
    # both unreadable outcomes, not rejections and not acceptances.
    expect_harness_error(
        lambda: _verdict(1, GATE_BANNER + "\n", "", ["<synthetic>"]),
        "without a single FAIL", "P4 reason-less rejection")
    expect_harness_error(
        lambda: _verdict(0, GATE_BANNER + "\n", "", ["<synthetic>"]),
        "without printing", "P5 acceptance without a PASS line")

    # P6: a shared baseline defect reported against a differently named copy
    # must not count as the class's own reason.
    shared = ("report disagrees with evidence/known-defects.json on 6 "
              "undeclared and 0 stale failed case(s)")
    baseline = gate_result(1, [f"matrix /tmp/w/f0-baseline-m1.json: "
                               f"{shared}"],
                           [("/tmp/w/f0-baseline-m1.json", "matrix#1")])
    copy_of_it = gate_result(1, [f"matrix /tmp/w/F3-fake-m1.json: {shared}"],
                             [("/tmp/w/F3-fake-m1.json", "matrix#1")])
    verdict, _note, own = _classify(copy_of_it, set(baseline.keys))
    assert verdict == "NOT-INFORMATIVE" and not own, (
        f"a baseline reason leaked in as a class's own reason: {verdict} "
        f"{own}")

    # P7: an own reason is credited, and two classes sharing one reason set
    # are surfaced rather than credited twice.
    forged = gate_result(1, [f"matrix /tmp/w/F3-fake-m1.json: {shared}",
                             "kind 'live_sidecar' opened by []"],
                         [("/tmp/w/F3-fake-m1.json", "matrix#1")])
    verdict, _note, own = _classify(forged, set(baseline.keys))
    assert verdict == "CAUGHT" and len(own) == 1, (
        f"a class with its own reason was not credited: {verdict} {own}")
    duplicated = _duplicate_groups({"A": ("kind x",), "B": ("kind x",),
                                    "C": ("kind y",)})
    assert duplicated == [["A", "B"]], (
        f"identical reason sets were not surfaced: {duplicated}")
    assert not _duplicate_groups({"A": ("kind x",), "B": ("kind y",)}), (
        "distinct reason sets were reported as duplicates")

    # P8: the G2 no-op detection compares what it must compare -- all eight
    # consumed classes and the binding, not only the two report classes the
    # battery started with.  A class that mutates only the Windows
    # qualification, or only the manifest it points at, must be visible here.
    bundle = _load_bundle()
    reference = copy.deepcopy(bundle.state())
    assert bundle.state() == reference, (
        "the G2 comparison is inverted: a fresh copy already differs")
    bundle.matrices[0]["cases"][0]["status"] = "FORGED-BY-SELF-TEST"
    assert bundle.state() != reference, (
        "the G2 comparison never fires, so a no-op mutator would pass "
        "unnoticed")
    untouched = _load_bundle()
    untouched.windows["build_provenance"]["native_go_test"] = "RED"
    assert untouched.state() != copy.deepcopy(_load_bundle().state()), (
        "the G2 comparison ignores the Windows qualification, so a class "
        "that edits only it would be stopped as a no-op")
    binding = _load_bundle()
    binding.manifest = MANIFEST_FILE
    assert binding.state() != copy.deepcopy(_load_bundle().state()), (
        "the G2 comparison ignores the content binding, so the class that "
        "attacks it would be stopped as a no-op")

    # The class count is pinned exactly: a floor would let a deleted class
    # pass as a battery that still covers every item it claims to cover.
    assert len(labels) == EXPECTED_CLASSES, (
        f"the battery registers {len(labels)} classes but is specified as "
        f"{EXPECTED_CLASSES}; each of the seven consumed-artifact items has "
        f"its own class, and losing one is a regression rather than a "
        f"simplification")
    return len(labels)


def main(argv=None):
    args = _parse_args(argv)
    ledger = args.sha256_ledger
    if ledger:
        # Resolve once, here: the battery and the gate each run under their
        # own caller's cwd, and a silently dropped ledger would weaken the
        # run without changing its verdict.
        ledger = os.path.realpath(ledger)
        if not os.path.isfile(ledger):
            print(f"--sha256-ledger {args.sha256_ledger}: no such file; the "
                  "battery refuses to run with the digest binding silently "
                  "disabled (staged binaries are re-staged by the wave "
                  "battery)")
            return 2
    # The guards are proven before a single verdict is printed, so a guard
    # that stopped working cannot be mistaken for a green battery.
    try:
        classes = _self_test()
    except AssertionError as exc:
        print()
        print(f"VERDICT: BATTERY HARNESS BROKEN -- the guard self-test "
              f"failed: {exc}")
        return 2
    print(f"battery guard self-test PASSED: {classes} classes registered, "
          f"every guard proven able to fire")
    if args.self_test:
        return 0
    try:
        return _run_battery(ledger)
    except BatteryHarnessError as exc:
        print()
        print(f"VERDICT: BATTERY HARNESS BROKEN -- {exc}")
        return 2


if __name__ == "__main__":
    sys.exit(main())
