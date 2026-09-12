#!/usr/bin/env python3
"""Negative-control battery for the kind-coverage gate (wave-19.18).

The gate (``check_kind_coverage.py``) must reject forged evidence, not
only genuine evidence.  This battery builds every known forgery class
from the GENUINE committed evidence (``v4/cli/evidence/``): each forged
report set is a deep copy of the genuine matrix/crash reports with one
class of mutation applied, then the gate CLI is invoked on the forged
copies and must exit 1 (reject).  The genuine evidence is the positive
control and must exit 0.

Covered classes (kind-gate finding 7, wave-19.18 closure review):

- F3  fabricated cross-matrix PASS for a case the mixed runner skips
- F4  strip every ``live_sidecar`` open record from the matrix evidence
- F5  strip all consumer opens from the go->rust crash scenarios
- F6  empty assertions on a PASS crash scenario (already caught)
- F7  relabel the Go binary record's top-level ``implementation``
- F8  actor sha256 tamper (already caught)
- F9  lineage ref naming a non-executed method (already caught)
- F10 matrix relabel (already caught)
- F7a/F7b result-level implementation relabel (already caught)
- F11 recorded binary path bound to a nonexistent file

Run under ``nice`` (resource policy): ``nice python3 v4/cli/forgery_battery.py``.
Each gate invocation is sub-second over the small committed reports, so
the whole battery finishes in seconds.
"""

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
# The binary paths the committed evidence records as executed (the
# staged battery binaries of the wave-19.18 revision).
GO_BINARY = "/tmp/qualsvc/w1916/bin/go/iprange"

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
    report = matrices[1]  # matrix-go
    report["binaries"]["go"]["path"] = "/nonexistent/iprange-go"
    report["command"] = [
        "/nonexistent/iprange-go" if value == GO_BINARY else value
        for value in report["command"]]
    for case in report["cases"]:
        if case.get("status") != "PASS":
            continue
        for entry in (case.get("actors") or {}).values():
            if entry.get("argv") == GO_BINARY:
                entry["argv"] = "/nonexistent/iprange-go"


def _load_genuine():
    matrices = [json.load(open(path)) for path in MATRIX_FILES]
    crash = json.load(open(CRASH_FILE))
    return matrices, crash


def _run_gate(work_dir, matrices, crash, tag):
    paths = []
    for index, report in enumerate(matrices):
        path = os.path.join(work_dir, f"{tag}-m{index}.json")
        with open(path, "w", encoding="utf-8") as stream:
            json.dump(report, stream)
        paths.append(path)
    crash_path = os.path.join(work_dir, f"{tag}-crash.json")
    with open(crash_path, "w", encoding="utf-8") as stream:
        json.dump(crash, stream)
    result = subprocess.run(
        ["python3", GATE]
        + sum([["--matrix", path] for path in paths], [])
        + ["--crash", crash_path],
        capture_output=True, text=True)
    return result.returncode


def main():
    failures = 0
    with tempfile.TemporaryDirectory(dir=owned_temp_root()) as work:
        # Positive control: the genuine committed evidence must pass
        # the strengthened gate on the CLI (both verification modes).
        matrices, crash = _load_genuine()
        rc = _run_gate(work, matrices, crash, "f0-baseline")
        status = "OK" if rc == 0 else "FAIL"
        if rc != 0:
            failures += 1
        print(f"[f0-genuine-evidence] rc={rc} -> {status}  "
              f"expect_pass=True")
        for label, mutator in FORGERIES:
            matrices, crash = _load_genuine()
            mutator(matrices, crash)
            rc = _run_gate(work, matrices, crash, label)
            caught = rc != 0
            if not caught:
                failures += 1
            print(f"[{label}] rc={rc} -> "
                  f"{'CAUGHT' if caught else 'ACCEPTED'}  "
                  f"expect_reject=True  "
                  f"{'OK' if caught else '**GAP**'}")
    if failures:
        print(f"forgery battery FAILED: {failures} class(es) accepted")
        return 1
    print("forgery battery PASS: every class rejected, "
          "genuine evidence accepted")
    return 0


if __name__ == "__main__":
    sys.exit(main())
