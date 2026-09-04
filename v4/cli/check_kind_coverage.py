#!/usr/bin/env python3
"""Kind-universe completeness gate for the milestone-4 evidence battery.

The mechanical file-kind ledger can only prove what the executed cases
observe.  This gate reads every matrix report (``--matrix``) and the
crash report (``--crash``) of one evidence revision and enforces the
cross-language file-kind contract: every persistent artifact kind is
created by each product language (Rust and Go) and, whenever the suite
records any service opening one, opened by each language too; the
required kind universe is exactly the six journaled kinds plus
``publication_temp`` (a production maintenance kind); and "zero
unknown" means exactly that — PASS evidence containing any kind
outside the universe fails the gate.

Evidence integrity rules:

- The four matrix reports (``rust``, ``go``, ``rust_to_go``,
  ``go_to_rust``) and at least one positive crash report must all be
  supplied; the gate derives each report's identity from its top-level
  ``matrix`` field and every case entry's ``matrix`` value.
- Only PASS-case per-case ``file_kinds`` lineage is consumed (the
  report-root aggregate merges partial ledgers even for FAIL cases and
  is never trusted).
- A matrix report with ``failed != 0``, a crash report with
  ``failed != 0`` or leftover product processes, and any crash report
  whose PASS scenarios do not span both language directions
  (Rust as producer/Go as consumer and vice versa) are rejected.
- Language attribution: in a single-language matrix both actors are
  that language; in a mixed matrix the producer is the producer
  binary's implementation and the consumer the consumer's.  Crash
  scenarios attribute creation to the producer language and
  consumption to the consumer language.

Exit status 0 when every required kind has both-language evidence and
no report problem exists; 1 otherwise.
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
    "publication_temp",
    "authorized_scratch",
    "adapter_output",
    "metadata_delivery",
]
REQUIRED_MATRICES = ("rust", "go", "rust_to_go", "go_to_rust")
ACTOR_LANGUAGES = {
    "rust": {"producer": "rust", "consumer": "rust"},
    "go": {"producer": "go", "consumer": "go"},
    "rust_to_go": {"producer": "rust", "consumer": "go"},
    "go_to_rust": {"producer": "go", "consumer": "rust"},
}


def matrix_evidence(path):
    """Kind -> created/opened language sets observed by one matrix.

    Returns ``(matrix, evidence, case_count, problems)``.  Only
    PASS-case per-case ledgers are consulted; the report root
    aggregate is ignored because the runner merges partial ledgers
    into it even when a case FAILs.
    """

    with open(path, encoding="utf-8") as stream:
        report = json.load(stream)
    problems = []
    matrix = report.get("matrix")
    if matrix not in REQUIRED_MATRICES:
        problems.append(
            f"matrix {path}: report matrix {matrix!r} is not one of "
            f"{sorted(REQUIRED_MATRICES)}")
        return matrix, {}, len(report.get("cases", [])), problems
    failed = report.get("failed", 0)
    if failed:
        problems.append(f"matrix {path}: report records {failed} failed case(s)")
    leftover = report.get("leftover_processes")
    if leftover:
        problems.append(
            f"matrix {path}: report records leftover product processes: {leftover}")
    evidence = {}
    cases = report.get("cases", [])
    languages = ACTOR_LANGUAGES[matrix]
    for case in cases:
        if case.get("status") != "PASS":
            continue
        for _rel, facts in case.get("file_kinds", {}).items():
            bucket = evidence.setdefault(facts["kind"], {"created": set(), "opened": set()})
            for entry in facts.get("created_by", []):
                actor = entry.split(".", 1)[0]
                bucket["created"].add(languages.get(actor, "?"))
            for entry in facts.get("opened_by", []):
                actor = entry.split(".", 1)[0]
                bucket["opened"].add(languages.get(actor, "?"))
    return matrix, evidence, len(cases), problems


def crash_evidence(path):
    """Kind -> created/opened language sets from PASS crash scenarios.

    Returns ``(evidence, scenario_count, problems)``.  Scenarios whose
    ``"pass"`` is not true never contribute kinds: a failed scenario
    stops before the artifact inventory runs.  Creation is attributed
    to the scenario's producer language and consumption to its consumer
    language; the battery must span both language directions.
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
    producers, consumers = set(), set()
    scenarios = report.get("scenarios", [])
    for scenario in scenarios:
        if scenario.get("pass") is not True:
            continue
        producer = (scenario.get("producer") or "").split(":", 1)[0]
        consumer = (scenario.get("consumer") or "").split(":", 1)[0]
        producers.add(producer)
        consumers.add(consumer)
        for kind in scenario.get("kinds", []):
            bucket = evidence.setdefault(kind, {"created": set(), "opened": set()})
            if producer:
                bucket["created"].add(producer)
            if consumer:
                bucket["opened"].add(consumer)
    if not {"rust", "go"} <= producers or not {"rust", "go"} <= consumers:
        problems.append(
            f"crash {path}: PASS scenarios must span both language "
            f"directions (producers {sorted(producers)}, "
            f"consumers {sorted(consumers)})")
    return evidence, len(scenarios), problems


def assess(matrix_paths, crash_paths):
    """Evaluate one evidence revision; testable without the CLI.

    Returns ``(problems, coverage, sources)`` where ``coverage`` maps
    each required kind to the set of languages that created it and the
    set of languages that opened it.
    """

    coverage = {kind: {"created": set(), "opened": set()}
                for kind in REQUIRED_KINDS}
    sources = []
    problems = []
    seen_matrices = set()
    for path in matrix_paths:
        matrix, evidence, cases, report_problems = matrix_evidence(path)
        if matrix in REQUIRED_MATRICES:
            if matrix in seen_matrices:
                problems.append(
                    f"matrix {path}: matrix {matrix!r} supplied more than once")
            seen_matrices.add(matrix)
        sources.append(f"matrix {path} ({cases} cases)")
        problems.extend(report_problems)
        for kind, sides in evidence.items():
            bucket = coverage.setdefault(kind, {"created": set(), "opened": set()})
            bucket["created"].update(sides["created"])
            bucket["opened"].update(sides["opened"])
    for matrix in REQUIRED_MATRICES:
        if matrix not in seen_matrices:
            problems.append(f"missing matrix report for {matrix!r}")

    for path in crash_paths:
        evidence, scenarios, report_problems = crash_evidence(path)
        sources.append(f"crash {path} ({scenarios} scenarios)")
        problems.extend(report_problems)
        for kind, sides in evidence.items():
            bucket = coverage.setdefault(kind, {"created": set(), "opened": set()})
            bucket["created"].update(sides["created"])
            bucket["opened"].update(sides["opened"])

    unknown = sorted(kind for kind in coverage
                     if kind not in REQUIRED_KINDS)
    if unknown:
        problems.append(f"unknown kinds in PASS evidence: {unknown}")
    for kind in REQUIRED_KINDS:
        bucket = coverage[kind]
        if not {"rust", "go"} <= bucket["created"]:
            problems.append(
                f"kind {kind!r} must be created by both languages: "
                f"created by {sorted(bucket['created'])}")
        if bucket["opened"] and not {"rust", "go"} <= bucket["opened"]:
            problems.append(
                f"kind {kind!r} is opened by services and must be opened "
                f"by both languages: opened by {sorted(bucket['opened'])}")
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
        bucket = coverage[kind]
        status = "OK  " if ({"rust", "go"} <= bucket["created"]
                            and (not bucket["opened"]
                                 or {"rust", "go"} <= bucket["opened"])) else "MISS"
        print(f"  {status} {kind}: created by "
              f"{sorted(bucket['created']) or ['<none>']}, opened by "
              f"{sorted(bucket['opened']) or ['<none>']}")
    for problem in problems:
        print(f"FAIL: {problem}")
    if problems:
        return 1
    print("PASS: every required artifact kind has both-language evidence")
    return 0


def _self_test():
    """Doctored-report regression tests for the gate integrity rules."""

    import json as _json

    def matrix_report(matrix, cases, failed, root_kinds=None):
        return {
            "schema": "iprange-cli-report-v3",
            "matrix": matrix,
            "cases": cases,
            "file_kinds": root_kinds or {},
            "failed": failed,
        }

    def crash_report(producers, consumers, failed=0, leftover=None):
        scenarios = []
        for i, (p, c) in enumerate(zip(producers, consumers)):
            scenarios.append({
                "scenario": f"S{i}.{p}->{c}", "pass": True,
                "producer": f"{p}:/tmp/bin", "consumer": f"{c}:/tmp/bin",
                "kinds": ["publication_temp", "publication_reservation",
                          "authorized_scratch"],
            })
        return {"schema": "iprange-cli-crash-report-v1",
                "scenarios": scenarios,
                "leftover_processes": leftover or [],
                "failed": failed}

    def green_report(matrix):
        """One PASS case whose per-case ledger shows the four matrix
        kinds created and opened by this matrix's actors; the crash
        battery supplies the three crash-only kinds."""
        ledger = {}
        for i, kind in enumerate([
                "v4_main", "live_sidecar", "adapter_output",
                "metadata_delivery"]):
            ledger[f"k{i}.bin"] = {
                "kind": kind,
                "created_by": ["producer.iprange.v1.selftest"],
                "opened_by": ["consumer.iprange.v1.selftest"],
            }
        return matrix_report(matrix, [{
            "name": "doctored", "matrix": matrix, "status": "PASS",
            "file_kinds": ledger}], failed=0)

    with tempfile.TemporaryDirectory() as work:
        green = {}
        for m in REQUIRED_MATRICES:
            green[m] = os.path.join(work, f"{m}.json")
            with open(green[m], "w", encoding="utf-8") as stream:
                _json.dump(green_report(m), stream, sort_keys=True)
        crash_path = os.path.join(work, "crash.json")
        with open(crash_path, "w", encoding="utf-8") as stream:
            _json.dump(crash_report(["rust", "go"], ["go", "rust"]),
                       stream, sort_keys=True)
        four = [green[m] for m in REQUIRED_MATRICES]

        def assign(path, report):
            with open(path, "w", encoding="utf-8") as stream:
                _json.dump(report, stream, sort_keys=True)

        # 1. The full green battery passes.
        problems, coverage, _sources = assess(four, [crash_path])
        assert not problems, f"green battery failed: {problems}"
        assert all({"rust", "go"} <= coverage[k]["created"]
                   for k in REQUIRED_KINDS)
        assert all({"rust", "go"} <= coverage[k]["opened"]
                   for k in REQUIRED_KINDS)

        # 2. Missing any matrix report fails the gate.
        problems, _c, _s = assess(four[:1] + four[2:], [crash_path])
        assert problems and any("missing matrix report" in p for p in problems)

        # 3. A report without a matrix identity fails.
        bare = matrix_report("nonesuch", [], failed=0)
        bare_path = os.path.join(work, "bare.json")
        assign(bare_path, bare)
        problems, _c, _s = assess([bare_path] + four[1:], [crash_path])
        assert problems and any("not one of" in p for p in problems)

        # 4. An unknown kind in a PASS ledger fails.
        bad = green_report("rust")
        bad["cases"][0]["file_kinds"]["x.bin"] = {
            "kind": "unknown",
            "created_by": ["producer.iprange.v1.selftest"],
            "opened_by": []}
        unknown_path = os.path.join(work, "unknown.json")
        assign(unknown_path, bad)
        problems, _c, _s = assess([unknown_path] + four[1:], [crash_path])
        assert problems and any("unknown kinds" in p for p in problems)

        # 5. Creation by only one language fails.  v4_main is created
        #    by the producer actor, which is Rust in the rust and
        #    rust_to_go matrices, so both rust-attributed reports must
        #    lose their v4_main creation ledger.
        rust_created = green_report("rust")
        r2g_created = green_report("rust_to_go")
        rust_created["cases"][0]["file_kinds"]["k0.bin"]["created_by"] = []
        r2g_created["cases"][0]["file_kinds"]["k0.bin"]["created_by"] = []
        rust_created_path = os.path.join(work, "one-lang.json")
        assign(rust_created_path, rust_created)
        r2g_created_path = os.path.join(work, "one-lang-r2g.json")
        assign(r2g_created_path, r2g_created)
        problems, _c, _s = assess(
            [rust_created_path, green["go"], r2g_created_path,
             green["go_to_rust"]], [crash_path])
        assert problems and any("created by both languages" in p
                                for p in problems)

        # 6. Consumer (opened_by) lineage must span both languages when
        #    any service opens the kind.  The consumer actor is Rust in
        #    the rust and go_to_rust matrices, so both rust-attributed
        #    reports must lose their opened_by ledger.
        rust_opened = green_report("rust")
        g2r_opened = green_report("go_to_rust")
        for entry in rust_opened["cases"][0]["file_kinds"].values():
            entry["opened_by"] = []
        for entry in g2r_opened["cases"][0]["file_kinds"].values():
            entry["opened_by"] = []
        rust_opened_path = os.path.join(work, "no-open.json")
        assign(rust_opened_path, rust_opened)
        g2r_opened_path = os.path.join(work, "no-open-g2r.json")
        assign(g2r_opened_path, g2r_opened)
        problems, _c, _s = assess(
            [rust_opened_path, green["go"], green["rust_to_go"],
             g2r_opened_path], [crash_path])
        assert problems and any("opened by both languages" in p
                                for p in problems)

        # 7. A crash report with only one direction fails.
        one_dir_path = os.path.join(work, "one-dir.json")
        assign(one_dir_path, crash_report(["rust"], ["go"]))
        problems, _c, _s = assess(four, [one_dir_path])
        assert problems and any("both language directions" in p
                                for p in problems)

        # 8. The previous false-positive classes still fail: all-failed
        #    matrix with a full root aggregate, all-failed crash with
        #    kind lists, and leftover processes.
        bad_matrix = matrix_report(
            "rust",
            [{"name": "doctored", "matrix": "rust", "status": "FAIL",
              "error": "doctored"}],
            failed=1,
            root_kinds={kind: {"created_by": {"iprange.v1.selftest": 1},
                               "opened_by": {}} for kind in REQUIRED_KINDS})
        bad_matrix_path = os.path.join(work, "bad-matrix.json")
        assign(bad_matrix_path, bad_matrix)
        problems, _c, _s = assess([bad_matrix_path] + four[1:], [crash_path])
        assert problems and any("failed case" in p for p in problems)

        bad_crash_path = os.path.join(work, "bad-crash.json")
        assign(bad_crash_path, crash_report(
            ["rust", "go"], ["go", "rust"], failed=1))
        problems, _c, _s = assess(four, [bad_crash_path])
        assert problems and any("failed scenario" in p for p in problems)

        leftover_path = os.path.join(work, "leftover.json")
        assign(leftover_path, crash_report(
            ["rust", "go"], ["go", "rust"], leftover=["iprange"]))
        problems, _c, _s = assess(four, [leftover_path])
        assert problems and any("leftover" in p for p in problems)


if __name__ == "__main__":
    _self_test()
    sys.exit(main())
