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

The gate is invoked as a subprocess, with the canonical flag set recorded
in ``v4/cli/evidence/README.md``:

    python3 check_kind_coverage.py --matrix <one per matrix, x4>
        --crash evidence/crash.json
        --fifo-surface evidence/fifo-surface.json
        --throughput evidence/throughput.json [--sha256-ledger PATH]

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


def _forgery(label):
    def register(mutator):
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


def _load_genuine():
    matrices = [json.load(open(path)) for path in MATRIX_FILES]
    crash = json.load(open(CRASH_FILE))
    return matrices, crash


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


def _gate_command(matrix_paths, crash_path, ledger):
    """The canonical gate invocation, as documented in evidence/README.md."""
    command = [GATE]
    for path in matrix_paths:
        command += ["--matrix", path]
    command += ["--crash", crash_path,
                "--fifo-surface", FIFO_SURFACE_FILE,
                "--throughput", THROUGHPUT_FILE]
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


def _run_gate(work_dir, matrices, crash, tag, ledger):
    """Serialize one report set to the sandbox and ask the gate about it."""
    paths = []
    for index, report in enumerate(matrices):
        path = os.path.join(work_dir, f"{tag}-m{index}.json")
        with open(path, "w", encoding="utf-8") as stream:
            json.dump(report, stream)
        paths.append(path)
    crash_path = os.path.join(work_dir, f"{tag}-crash.json")
    with open(crash_path, "w", encoding="utf-8") as stream:
        json.dump(crash, stream)
    tokens = ([(path, f"matrix#{index + 1}")
               for index, path in enumerate(paths)]
              + [(crash_path, "crash#0")])
    command = _gate_command(paths, crash_path, ledger)
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
        baseline = _run_gate(work, *_load_genuine(), "f0-baseline", ledger)
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
            matrices, crash = _load_genuine()
            reference = copy.deepcopy((matrices, crash))
            mutator(matrices, crash)
            if (matrices, crash) == reference:
                raise BatteryHarnessError(
                    f"{label}: the mutator left the report set identical to "
                    "the genuine evidence, so this class tests nothing; a "
                    "field or path it rewrites no longer exists in the "
                    "committed reports")
            result = _run_gate(work, matrices, crash, label, ledger)
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
    preview = _gate_command(["<matrix x4>"], "<crash.json>", ledger)
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
    canonical = _gate_command(MATRIX_FILES, CRASH_FILE, None)
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

    # P8: the G2 no-op detection compares what it must compare.
    matrices, crash = _load_genuine()
    reference = copy.deepcopy((matrices, crash))
    assert (matrices, crash) == reference, (
        "the G2 comparison is inverted: a fresh copy already differs")
    matrices[0]["cases"][0]["status"] = "FORGED-BY-SELF-TEST"
    assert (matrices, crash) != reference, (
        "the G2 comparison never fires, so a no-op mutator would pass "
        "unnoticed")
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
