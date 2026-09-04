#!/usr/bin/env python3
"""Kind-universe completeness gate for the milestone-4 evidence battery.

The mechanical file-kind ledger can only prove what the executed cases
observe.  This gate reads every matrix report (``--matrix``) and the
crash report (``--crash``) of one evidence revision and fails when a
required artifact kind has no observed evidence at all: the per-case
ledgers (path -> kind -> actor.method lineage) of PASS cases and the
crash scenarios' kind lists.  A green gate means the battery
mechanically exercised every artifact class the v4 contract can
produce (main files, live sidecars, publication reservations,
authorized scratch, adapter outputs, metadata deliveries); "zero
unknown" is only meaningful when every required kind was actually
observed.

Evidence integrity rules:

- Only PASS cases contribute: the runner merges partial kind ledgers
  into the report root even when a case FAILs, so the root aggregate
  is never consumed.  Per-case ``file_kinds`` of PASS cases are the
  only matrix evidence source.
- A matrix report with any failed case (``failed != 0``) is rejected;
  the battery must be all-green before its kind evidence is trusted.
- A crash report with any failed scenario (``failed != 0``) or with
  leftover product processes is rejected; only scenarios whose
  ``"pass"`` is true contribute kinds.

Exit status 0 when every required kind is covered and no report
problem exists; 1 otherwise.
"""

import argparse
import json
import os
import sys
import tempfile

REQUIRED_KINDS = [
    "v4_main",
    "live_sidecar",
    "publication_reservation",
    "authorized_scratch",
    "adapter_output",
    "metadata_delivery",
]


def matrix_evidence(path):
    """Kind -> set of "actor.method" producers observed by one matrix.

    Returns ``(evidence, case_count, problems)``.  Only PASS-case
    per-case ledgers are consulted; the report root aggregate is
    ignored because the runner merges partial ledgers into it even
    when a case FAILs.
    """

    with open(path, encoding="utf-8") as stream:
        report = json.load(stream)
    problems = []
    failed = report.get("failed", 0)
    if failed:
        problems.append(f"matrix {path}: report records {failed} failed case(s)")
    leftover = report.get("leftover_processes")
    if leftover:
        problems.append(
            f"matrix {path}: report records leftover product processes: {leftover}")
    evidence = {}
    cases = report.get("cases", [])
    for case in cases:
        if case.get("status") != "PASS":
            continue
        for _rel, facts in case.get("file_kinds", {}).items():
            bucket = evidence.setdefault(facts["kind"], set())
            bucket.update(facts.get("created_by", []))
    return evidence, len(cases), problems


def crash_evidence(path):
    """Kind set observed by the PASS crash scenarios of one crash report.

    Returns ``(evidence, scenario_count, problems)``.  Scenarios whose
    ``"pass"`` is not true never contribute kinds: a failed scenario
    stops before the artifact inventory runs, so its empty kind list
    must not water down the required universe.
    """

    with open(path, encoding="utf-8") as stream:
        report = json.load(stream)
    problems = []
    failed = report.get("failed", 0)
    if failed:
        problems.append(
            f"crash {path}: report records {failed} failed scenario(s)")
    leftover = report.get("leftover_processes")
    if leftover:
        problems.append(
            f"crash {path}: report records leftover product processes: {leftover}")
    evidence = {}
    for scenario in report.get("scenarios", []):
        if scenario.get("pass") is not True:
            continue
        for kind in scenario.get("kinds", []):
            evidence.setdefault(kind, set()).add(
                f"crash:{scenario.get('scenario', '?')}")
    return evidence, len(report.get("scenarios", [])), problems


def assess(matrix_paths, crash_paths):
    """Evaluate one evidence revision; testable without the CLI.

    Returns ``(problems, coverage, source_labels)`` where ``problems``
    collects every gate failure (report integrity problems and missing
    required kinds) and ``coverage`` maps each required kind to the
    set of observed producers.
    """

    coverage = {kind: set() for kind in REQUIRED_KINDS}
    sources = []
    problems = []
    for path in matrix_paths:
        evidence, cases, report_problems = matrix_evidence(path)
        sources.append(f"matrix {path} ({cases} cases)")
        problems.extend(report_problems)
        for kind, producers in evidence.items():
            coverage.setdefault(kind, set()).update(producers)
    for path in crash_paths:
        evidence, scenarios, report_problems = crash_evidence(path)
        sources.append(f"crash {path} ({scenarios} scenarios)")
        problems.extend(report_problems)
        for kind, producers in evidence.items():
            coverage.setdefault(kind, set()).update(producers)

    missing = [kind for kind in REQUIRED_KINDS if not coverage[kind]]
    if missing:
        problems.append(
            f"required kinds never observed: {sorted(missing)}")
    return problems, coverage, sources


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--matrix", action="append", default=[],
                        metavar="PATH", help="one matrix report (repeatable)")
    parser.add_argument("--crash", action="append", default=[],
                        metavar="PATH", help="one crash report (repeatable)")
    args = parser.parse_args()
    if not args.matrix and not args.crash:
        parser.error("at least one --matrix or --crash report is required")

    problems, coverage, sources = assess(args.matrix, args.crash)
    print("Artifact-kind coverage gate")
    print("Sources: " + "; ".join(sources))
    for kind in REQUIRED_KINDS:
        producers = sorted(coverage[kind])
        status = "OK  " if producers else "MISS"
        print(f"  {status} {kind}: {len(producers)} producer(s)")
        for producer in producers[:6]:
            print(f"         {producer}")
        if len(producers) > 6:
            print(f"         ... {len(producers) - 6} more")
    for problem in problems:
        print(f"FAIL: {problem}")
    if problems:
        return 1
    print("PASS: every required artifact kind was observed "
          "by the executed battery")
    return 0


def _self_test():
    """Doctored-report regression tests for the gate integrity rules."""

    import json as _json

    def write(report):
        stream = tempfile.NamedTemporaryFile(
            "w", suffix=".json", delete=False, encoding="utf-8")
        _json.dump(report, stream, sort_keys=True)
        stream.close()
        return stream.name

    def matrix_report(cases, failed, root_kinds=None):
        return {
            "schema": "iprange-cli-report-v3",
            "cases": cases,
            "file_kinds": root_kinds or {},
            "failed": failed,
        }

    def pass_case(name, kinds):
        # kinds: relpath -> {"kind", "created_by", "opened_by"}
        return {"name": name, "matrix": "self-test", "status": "PASS",
                "file_kinds": kinds}

    all_kinds = {kind: {"kind": kind,
                        "created_by": ["producer.iprange.v1.selftest"],
                        "opened_by": []}
                 for kind in REQUIRED_KINDS}

    # 1. An all-failed matrix must fail the gate even when its root
    #    aggregate covers every required kind (the root merges FAIL
    #    ledgers and is never a trusted evidence source).
    with tempfile.TemporaryDirectory() as work:
        bad_matrix = matrix_report(
            [{"name": "doctored", "matrix": "self-test", "status": "FAIL",
              "error": "doctored"}],
            failed=1,
            root_kinds={kind: {"created_by": {"iprange.v1.selftest": 1},
                               "opened_by": {}} for kind in REQUIRED_KINDS})
        path = os.path.join(work, "matrix.json")
        with open(path, "w", encoding="utf-8") as stream:
            _json.dump(bad_matrix, stream, sort_keys=True)
        problems, coverage, _sources = assess([path], [])
        assert problems, "all-failed matrix report passed the gate"
        assert all(not coverage[kind] for kind in REQUIRED_KINDS), (
            "FAIL-case ledgers leaked into coverage")

        # 2. A crash report whose scenarios all failed must fail the
        #    gate even though their kind lists name every required kind.
        bad_crash = {
            "schema": "iprange-cli-crash-report-v1",
            "scenarios": [
                {"scenario": "A1.self-test", "pass": False,
                 "kinds": list(REQUIRED_KINDS)},
            ],
            "leftover_processes": [],
            "failed": 1,
        }
        path = os.path.join(work, "crash.json")
        with open(path, "w", encoding="utf-8") as stream:
            _json.dump(bad_crash, stream, sort_keys=True)
        problems, coverage, _sources = assess([], [path])
        assert problems, "all-failed crash report passed the gate"
        assert all(not coverage[kind] for kind in REQUIRED_KINDS), (
            "failed-scenario kind lists leaked into coverage")

        # 3. A crash report with leftover processes must fail the gate.
        leftover_crash = {
            "schema": "iprange-cli-crash-report-v1",
            "scenarios": [
                {"scenario": "A1.self-test", "pass": True,
                 "kinds": list(REQUIRED_KINDS)},
            ],
            "leftover_processes": ["iprange"],
            "failed": 0,
        }
        path = os.path.join(work, "leftover.json")
        with open(path, "w", encoding="utf-8") as stream:
            _json.dump(leftover_crash, stream, sort_keys=True)
        problems, _coverage, _sources = assess([], [path])
        assert problems, "crash report with leftover processes passed the gate"

        # 4. A green matrix with one PASS case whose per-case lineage
        #    covers the full universe passes, and a root aggregate
        #    alone (no PASS per-case ledger) does not.
        good_matrix = matrix_report(
            [pass_case("doctored", {f"k{i}.bin": facts
                                    for i, (kind, facts) in enumerate(all_kinds.items())})],
            failed=0)
        path = os.path.join(work, "good.json")
        with open(path, "w", encoding="utf-8") as stream:
            _json.dump(good_matrix, stream, sort_keys=True)
        problems, coverage, _sources = assess([path], [])
        assert not problems, f"green PASS-lineage report failed: {problems}"
        assert all(coverage[kind] for kind in REQUIRED_KINDS)

        root_only = matrix_report([], failed=0, root_kinds=all_kinds)
        path = os.path.join(work, "root-only.json")
        with open(path, "w", encoding="utf-8") as stream:
            _json.dump(root_only, stream, sort_keys=True)
        problems, coverage, _sources = assess([path], [])
        assert problems, "root-aggregate-only report passed the gate"
        assert all(not coverage[kind] for kind in REQUIRED_KINDS)


if __name__ == "__main__":
    _self_test()
    sys.exit(main())
