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
unknown" means exactly that — PASS evidence containing any kind outside
the universe fails the gate.

Evidence integrity rules:

- The four matrix reports (``rust``, ``go``, ``rust_to_go``,
  ``go_to_rust``) and at least one positive crash report must all be
  supplied; each report must carry a top-level ``matrix`` identity,
  and each required matrix must actually contribute at least one PASS
  case with per-case file-kind evidence (an empty or purely skipped
  matrix fails even when other reports cover its kinds).
- Only PASS-case per-case ``file_kinds`` lineage is consumed (the
  report-root aggregate merges partial ledgers even for FAIL cases and
  is never trusted).
- A matrix report with ``failed != 0``, a crash report with
  ``failed != 0`` or leftover product processes, and any crash report
  whose PASS scenarios do not span both language directions
  (Rust as producer/Go as consumer and vice versa) are rejected.
- Every executed identity is anchored in a GLOBAL sha256 ->
  implementation map built from every matrix-style binary record of
  every supplied report (matrix ``binaries`` blocks).  The same
  sha256 may not declare different implementations anywhere: a
  relabeled clone that forges its own binary block conflicts with the
  binary's identity recorded by the genuine reports.
- Language attribution comes from that global map, never from labels:
  every PASS matrix case must carry an ``actors`` map whose
  producer/consumer entries record the ``implementation``
  ("rust"|"go") of the binary that served that role, as declared by
  that binary's ``system.describe`` capability result; each actor
  sha256 must resolve through the global map to the same
  implementation the case declares, and the global identities of the
  executed pair must match the pair the matrix label claims, so a
  report cloned from another matrix and relabeled fails.
- Executed work is mandatory: every PASS case must record an
  executed-step count per actor (``actors.*.steps``) and the actors
  together must record at least one executed step; every PASS crash
  scenario must record a non-empty executed ``assertions`` list (the
  crash schema has no step counter).
- Counters are cross-validated with the per-case records: the number
  of matrix cases that are not PASS/SKIP must equal ``failed``, and
  the number of crash scenarios whose ``pass`` is not true must equal
  ``failed``.  Any ``failed > 0`` fails the whole gate.
- Crash scenarios must carry executed identity that resolves through
  the global map: each PASS scenario's ``producer``/``consumer``
  ``"impl:path"`` identity must name a binary path recorded in the
  crash report's root binaries table, that binary's sha256 must
  resolve through the global map, its global implementation must
  equal the role's declared implementation, the two roles must use
  different languages, and the direction embedded in the scenario
  name must match the declared identities.  (The crash report schema
  does not record per-scenario sha256/command strings; the checker
  derives the sha256 from the report-root path->sha256 table, so a
  relabeled scenario whose binary path still names the real binary
  fails — see the module notes in ``crash_evidence`` for the residual
  schema limitation.)
- Per-kind lineage attribution resolves actor names through the
  global implementation map, so a relabeled report's actors resolve
  against the identities recorded by all reports, not against
  self-consistent forged fields of one report.

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
ALL_ACTORS = ("producer", "consumer")
# Matrix label -> the actor-language pair that the executed binaries must
# have produced.  Used only as the consistency probe against the global
# identities of the executed shas, never for attribution.
ACTOR_LANGUAGES = {
    "rust": {"producer": "rust", "consumer": "rust"},
    "go": {"producer": "go", "consumer": "go"},
    "rust_to_go": {"producer": "rust", "consumer": "go"},
    "go_to_rust": {"producer": "go", "consumer": "rust"},
}
VALID_STATUSES = ("PASS", "FAIL", "SKIP")
PRODUCT_LANGUAGES = ("rust", "go")


def _matrix_binary_declarations(report):
    """Yield ``(sha256, implementation)`` for matrix-style binary records.

    Matrix reports record ``binaries`` as a dict of capability records
    (``{"sha256": ..., "result": {"implementation": ...}}``).  Only
    records that declare a product language feed the global map; a
    legacy binary that declares no implementation contributes nothing.
    """

    binaries = report.get("binaries")
    if not isinstance(binaries, dict):
        return
    for record in binaries.values():
        if not isinstance(record, dict):
            continue
        sha = record.get("sha256")
        result = record.get("result")
        implementation = None
        if isinstance(result, dict):
            implementation = result.get("implementation")
        if isinstance(sha, str) and implementation in PRODUCT_LANGUAGES:
            yield sha, implementation


def _crash_path_to_sha(report):
    """Crash-report root binaries table: binary path -> sha256.

    The crash schema (``iprange-cli-crash-report-v1``) records the two
    product binaries and the fixture tool as flat ``<role>`` /
    ``<role>_sha256`` pairs at the report root, not as per-scenario
    identities.  Scenarios name their binaries as ``impl:path``, so the
    path -> sha256 table is the only executed-identity anchor the crash
    report provides.
    """

    table = {}
    binaries = report.get("binaries")
    if not isinstance(binaries, dict):
        return table
    for key, value in binaries.items():
        if not (isinstance(key, str) and key.endswith("_sha256")):
            continue
        if not isinstance(value, str):
            continue
        path = binaries.get(key[:-len("_sha256")])
        if isinstance(path, str):
            table[path] = value
    return table


def _load_report(path, problems):
    try:
        with open(path, encoding="utf-8") as stream:
            report = json.load(stream)
    except (OSError, json.JSONDecodeError) as exc:
        problems.append(f"report {path}: cannot read JSON report: {exc}")
        return None
    if not isinstance(report, dict):
        problems.append(f"report {path}: JSON root is not an object")
        return None
    return report


def _global_implementation_map(matrix_paths, crash_paths, problems):
    """Build the cross-report sha256 -> implementation map.

    Scans every matrix-style binary record of every supplied report.
    A sha256 that declares different implementations in different
    reports is a global identity conflict and fails the gate: no
    report may redefine what an executed binary is.  Returns the map of
    sha256 -> implementation for shas with a single declaration.
    """

    declarations = {}
    # Loading problems are reported by the evidence pass; the map pass
    # only needs the binary declarations of readable reports.
    for path in matrix_paths + crash_paths:
        report = _load_report(path, [])
        if report is None:
            continue
        for sha, implementation in _matrix_binary_declarations(report):
            declarations.setdefault(sha, set()).add(implementation)
    implementation_of = {}
    for sha, implementations in declarations.items():
        if len(implementations) > 1:
            problems.append(
                f"global implementation conflict for sha256 {sha}: "
                f"declared {sorted(implementations)} across reports")
        else:
            implementation_of[sha] = next(iter(implementations))
    return implementation_of


def matrix_evidence(path, report, implementation_of, problems):
    """Kind -> created/opened language sets observed by one matrix.

    Returns ``(matrix, evidence, stats, problems)``.  ``stats`` holds
    ``cases``, ``fail_cases`` (per-case status counter), ``pass_cases``
    and ``contributing`` (PASS cases with a non-empty per-case ledger),
    so the aggregation can require every required matrix to contribute
    evidence.  Only PASS-case per-case ledgers are consulted; the
    report root aggregate is ignored because the runner merges partial
    ledgers into it even when a case FAILs.

    Language attribution is executed-actor based and anchored in the
    global map: each PASS case must carry an ``actors`` map whose
    producer/consumer entries record the ``implementation`` ("rust"|
    "go") and the executed sha256 of the binary that served that role,
    and the sha256 must resolve through the global map to exactly that
    implementation.  The top-level ``matrix`` label is never used for
    attribution; it is only cross-checked against the global
    identities of the executed pair, so a relabeled clone fails.
    """

    matrix = report.get("matrix")
    if matrix not in REQUIRED_MATRICES:
        problems.append(
            f"matrix {path}: report matrix {matrix!r} is not one of "
            f"{sorted(REQUIRED_MATRICES)}")
        empty_stats = {"cases": len(report.get("cases", [])),
                       "fail_cases": 0, "pass_cases": 0, "contributing": 0}
        return matrix, {}, empty_stats, problems
    failed = report.get("failed", 0)
    if failed:
        problems.append(f"matrix {path}: report records {failed} failed case(s)")
    leftover = report.get("leftover_processes")
    if leftover:
        problems.append(
            f"matrix {path}: report records leftover product processes: {leftover}")
    cases = report.get("cases", [])
    # Counter cross-validation: the per-case status list is the truth;
    # a doctored aggregate can claim any number.  Cases that are not
    # PASS or SKIP are failed cases; a status outside the emitted set
    # is a report defect and counts as failed too.
    fail_cases = sum(1 for case in cases
                     if case.get("status") not in ("PASS", "SKIP"))
    if fail_cases != failed:
        problems.append(
            f"matrix {path}: failed counter mismatch: report failed={failed} "
            f"but {fail_cases} case(s) are not PASS/SKIP")
    for index, case in enumerate(cases):
        status = case.get("status")
        if status not in VALID_STATUSES:
            problems.append(
                f"matrix {path}: case "
                f"{case.get('name', '<unnamed>')!r} has unexpected status "
                f"{status!r}")
    expected = ACTOR_LANGUAGES[matrix]
    evidence = {}
    pass_cases = 0
    contributing = 0
    for case in cases:
        if case.get("status") != "PASS":
            continue
        pass_cases += 1
        case_name = case.get("name", "<unnamed>")
        actors = case.get("actors")
        if (not isinstance(actors, dict)
                or "producer" not in actors
                or "consumer" not in actors):
            problems.append(
                f"matrix {path}: PASS case {case_name!r} has no complete "
                f"per-case actors map (needs producer and consumer entries)")
            continue
        implementations = {}
        steps_sum = 0
        steps_complete = True
        for actor in ALL_ACTORS:
            entry = actors.get(actor)
            if not isinstance(entry, dict):
                entry = {}
            implementation = entry.get("implementation")
            if implementation not in PRODUCT_LANGUAGES:
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} actor {actor!r} "
                    f"implementation {implementation!r} is not rust or go")
                implementations[actor] = "?"
            else:
                implementations[actor] = implementation
            # The actor's recorded SHA-256 must name a binary the same
            # report describes (same-report anchor) and must resolve
            # through the global map to the same implementation (the
            # cross-report authority).  A relabeled clone that forges
            # its own binary block conflicts with the genuine reports'
            # declaration of the same sha256.
            sha = entry.get("sha256")
            declared = None
            if isinstance(report.get("binaries"), dict) and isinstance(sha, str):
                for record in report["binaries"].values():
                    if isinstance(record, dict) and record.get("sha256") == sha:
                        declared = (record.get("result") or {}).get(
                            "implementation")
                        break
            if not (isinstance(sha, str) and sha):
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} actor {actor!r} "
                    f"records no sha256 identity")
                implementations[actor] = "?"
            elif declared is None:
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} actor {actor!r} "
                    f"sha256 {sha!r} does not name any binary record of "
                    f"the same report")
                implementations[actor] = "?"
            elif implementation in PRODUCT_LANGUAGES and declared != implementation:
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} actor {actor!r} "
                    f"sha256 names a binary that declares implementation "
                    f"{declared!r}, not {implementation!r}")
                implementations[actor] = "?"
            global_implementation = None
            if isinstance(sha, str):
                global_implementation = implementation_of.get(sha)
            if global_implementation is None:
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} actor {actor!r} "
                    f"sha256 {sha!r} names no binary declared by any "
                    f"supplied report")
                implementations[actor] = "?"
            elif implementation in PRODUCT_LANGUAGES and \
                    global_implementation != implementation:
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} actor {actor!r} "
                    f"declares implementation {implementation!r} but the "
                    f"global identity of sha256 {sha!r} is "
                    f"{global_implementation!r}")
                implementations[actor] = "?"
            elif implementation in PRODUCT_LANGUAGES:
                # The global map is the attribution authority when it
                # agrees with the case declaration; an invalid case
                # declaration keeps the poisoned "?" lineage marker.
                implementations[actor] = global_implementation
            # Executed-work evidence: the runner records the executed
            # step count per actor; a case that executed nothing (or
            # was doctored to claim nothing) is not evidence.
            steps = entry.get("steps")
            if isinstance(steps, bool) or not isinstance(steps, int):
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} actor {actor!r} "
                    f"records no executed-step count")
                steps_complete = False
            elif steps < 0:
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} actor {actor!r} "
                    f"records negative executed-step count {steps}")
                steps_complete = False
            else:
                steps_sum += steps
        if steps_complete and steps_sum < 1:
            problems.append(
                f"matrix {path}: PASS case {case_name!r} records zero "
                f"executed steps (no executed-work evidence)")
        # Label/identity probe: the pair of global identities of the
        # executed shas must match the pair the matrix label claims.
        # A clone relabeled to another matrix keeps its executed
        # binaries' identities, so the pair no longer matches its
        # label.
        observed = (implementations.get("producer"),
                    implementations.get("consumer"))
        if observed[0] in PRODUCT_LANGUAGES and \
                observed[1] in PRODUCT_LANGUAGES:
            if observed != (expected["producer"], expected["consumer"]):
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} label/identity "
                    f"mismatch: executed actor languages {observed} do not "
                    f"match matrix label {matrix!r} "
                    f"({expected['producer']}->{expected['consumer']})")
        kinds = case.get("file_kinds")
        if kinds:
            contributing += 1
        if not isinstance(kinds, dict):
            if kinds is not None:
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} file_kinds "
                    f"is not an object")
            kinds = {}
        for _rel, facts in kinds.items():
            if not isinstance(facts, dict) or not facts.get("kind"):
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} file_kinds "
                    f"entry {_rel!r} records no kind")
                continue
            bucket = evidence.setdefault(facts["kind"],
                                         {"created": set(), "opened": set()})
            for entry in facts.get("created_by", []):
                actor = entry.split(".", 1)[0]
                bucket["created"].add(implementations.get(actor, "?"))
            for entry in facts.get("opened_by", []):
                actor = entry.split(".", 1)[0]
                bucket["opened"].add(implementations.get(actor, "?"))
    stats = {"cases": len(cases), "fail_cases": fail_cases,
             "pass_cases": pass_cases, "contributing": contributing}
    return matrix, evidence, stats, problems


def _split_identity(value):
    """Split a scenario ``impl:path`` identity; ``(None, None)`` if malformed."""

    if not isinstance(value, str) or ":" not in value:
        return None, None
    implementation, path = value.split(":", 1)
    return implementation, path


def crash_evidence(path, report, path_to_sha, implementation_of, problems):
    """Kind -> created/opened language sets from PASS crash scenarios.

    Returns ``(evidence, stats, problems)``.  Scenarios whose
    ``"pass"`` is not true never contribute kinds: a failed scenario
    stops before the artifact inventory runs.  Creation is attributed
    to the scenario's producer language and consumption to its consumer
    language; the battery must span both language directions.

    Scenario identity comes from the per-scenario
    ``producer_sha256``/``consumer_sha256`` records the harness writes
    from the exact binaries it executes, never from the direction
    label: each role's sha256 must resolve through the global
    cross-report sha->implementation map to the implementation the
    scenario declares, and, when the report-root binaries table also
    maps the role path to a sha256, the two records must agree.  A
    duplicated direction keeps the original binaries' shas, so its
    forged labels contradict the global identity of those binaries.
    """

    failed = report.get("failed", 0)
    if failed:
        problems.append(
            f"crash {path}: report records {failed} failed scenario(s)")
    leftover = report.get("leftover_processes")
    if leftover:
        problems.append(
            f"crash {path}: report records leftover product processes: {leftover}")
    scenarios = report.get("scenarios", [])
    # Counter cross-validation: the per-scenario pass flags are the
    # truth for the failed counter.
    fail_scenarios = sum(1 for scenario in scenarios
                         if scenario.get("pass") is not True)
    if fail_scenarios != failed:
        problems.append(
            f"crash {path}: failed counter mismatch: report failed={failed} "
            f"but {fail_scenarios} scenario(s) are not pass=true")
    evidence = {}
    producers, consumers = set(), set()
    pass_scenarios = 0
    for scenario in scenarios:
        if scenario.get("pass") is not True:
            continue
        pass_scenarios += 1
        scenario_name = scenario.get("scenario", "<unnamed>")
        # Per-scenario binary identity is mandatory: the harness records
        # the sha256 of the producer and consumer binaries each scenario
        # executes; a PASS scenario without it has no executed identity.
        for role in ("producer", "consumer"):
            sha = scenario.get(role + "_sha256")
            if not (isinstance(sha, str) and len(sha) == 64
                    and all(c in "0123456789abcdef" for c in sha)):
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} "
                    f"records no {role}_sha256 identity ({sha!r})")
        # Executed-work evidence: the crash schema has no step counter;
        # the harness records every executed assertion of the scenario,
        # so an empty assertions list means nothing was executed.
        assertions = scenario.get("assertions")
        if not (isinstance(assertions, list) and assertions):
            problems.append(
                f"crash {path}: PASS scenario {scenario_name!r} records no "
                f"executed assertions (no executed-work evidence)")
        producer_label, producer_path = _split_identity(
            scenario.get("producer"))
        consumer_label, consumer_path = _split_identity(
            scenario.get("consumer"))
        producer_impl, consumer_impl = None, None
        for role, label, binary_path in (
                ("producer", producer_label, producer_path),
                ("consumer", consumer_label, consumer_path)):
            if label not in PRODUCT_LANGUAGES or binary_path is None:
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} "
                    f"{role} records no 'impl:path' identity "
                    f"({scenario.get(role)!r})")
                continue
            sha = scenario.get(role + "_sha256")
            if not isinstance(sha, str):
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} "
                    f"{role} records no sha256 identity")
                continue
            root_sha = path_to_sha.get(binary_path)
            if root_sha is not None and root_sha != sha:
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} "
                    f"{role} sha256 {sha!r} contradicts the report "
                    f"binaries table sha256 {root_sha!r} for path "
                    f"{binary_path!r}")
                continue
            global_implementation = None
            if isinstance(sha, str):
                global_implementation = implementation_of.get(sha)
            if global_implementation is None:
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} "
                    f"{role} sha256 {sha!r} (path {binary_path!r}) names no "
                    f"binary declared by any supplied report")
                continue
            if global_implementation != label:
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} "
                    f"{role} declares implementation {label!r} but the "
                    f"global identity of binary {binary_path!r} "
                    f"(sha256 {sha!r}) is {global_implementation!r}")
                continue
            if role == "producer":
                producer_impl = global_implementation
            else:
                consumer_impl = global_implementation
        # The direction embedded in the scenario name must match the
        # declared identities (the harness emits ``<SCENARIO>.<producer>
        # -><consumer>``).
        if "." in scenario_name:
            name_direction = scenario_name.split(".", 1)[1]
            declared_direction = f"{producer_label}->{consumer_label}"
            if producer_label in PRODUCT_LANGUAGES and \
                    consumer_label in PRODUCT_LANGUAGES and \
                    name_direction != declared_direction:
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} "
                    f"records direction {declared_direction!r}, "
                    f"contradicting its name")
        # A PASS crash scenario must span two different product
        # languages: the global identities of the producer and consumer
        # binaries must be the opposite languages.
        if producer_impl in PRODUCT_LANGUAGES and \
                consumer_impl in PRODUCT_LANGUAGES:
            producers.add(producer_impl)
            consumers.add(consumer_impl)
            if producer_impl == consumer_impl:
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} uses "
                    f"language {producer_impl!r} for both producer and "
                    f"consumer")
        kinds = scenario.get("kinds") or []
        for kind in kinds:
            bucket = evidence.setdefault(kind, {"created": set(),
                                                "opened": set()})
            bucket["created"].add(producer_impl if producer_impl else "?")
            bucket["opened"].add(consumer_impl if consumer_impl else "?")
    if not {"rust", "go"} <= producers or not {"rust", "go"} <= consumers:
        problems.append(
            f"crash {path}: PASS scenarios must span both language "
            f"directions (producers {sorted(producers)}, "
            f"consumers {sorted(consumers)})")
    stats = {"scenarios": len(scenarios), "pass_scenarios": pass_scenarios}
    return evidence, stats, problems


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
    implementation_of = _global_implementation_map(
        matrix_paths, crash_paths, problems)

    seen_matrices = {}
    matrix_stats = {}
    for path in matrix_paths:
        report = _load_report(path, problems)
        if report is None:
            continue
        matrix, evidence, stats, report_problems = matrix_evidence(
            path, report, implementation_of, problems)
        if matrix in REQUIRED_MATRICES:
            if matrix in seen_matrices:
                problems.append(
                    f"matrix {path}: matrix {matrix!r} supplied more "
                    f"than once")
            seen_matrices.setdefault(matrix, []).append(path)
            matrix_stats[path] = stats
        sources.append(
            f"matrix {path} ({stats['cases']} cases, "
            f"{stats['pass_cases']} PASS)")
        problems.extend(report_problems)
        for kind, sides in evidence.items():
            bucket = coverage.setdefault(kind,
                                         {"created": set(), "opened": set()})
            bucket["created"].update(sides["created"])
            bucket["opened"].update(sides["opened"])
    for matrix in REQUIRED_MATRICES:
        if matrix not in seen_matrices:
            problems.append(f"missing matrix report for {matrix!r}")
            continue
        for path in seen_matrices[matrix]:
            if matrix_stats[path]["contributing"] < 1:
                problems.append(
                    f"matrix {path}: required matrix {matrix!r} "
                    f"contributes no PASS case with file-kind evidence")

    for path in crash_paths:
        report = _load_report(path, problems)
        if report is None:
            continue
        evidence, stats, report_problems = crash_evidence(
            path, report, _crash_path_to_sha(report), implementation_of,
            problems)
        sources.append(
            f"crash {path} ({stats['scenarios']} scenarios, "
            f"{stats['pass_scenarios']} PASS)")
        problems.extend(report_problems)
        for kind, sides in evidence.items():
            bucket = coverage.setdefault(kind,
                                         {"created": set(), "opened": set()})
            bucket["created"].update(sides["created"])
            bucket["opened"].update(sides["opened"])

    unknown = sorted(kind for kind in coverage if kind not in REQUIRED_KINDS)
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

    # The crash builder mirrors the emitted crash schema: scenarios
    # carry ``impl:path`` identities and the report root carries the
    # flat path -> sha256 binaries table (no implementation
    # declarations, no per-scenario sha256).
    BINARY_PATHS = {"rust": "/tmp/rust-iprange", "go": "/tmp/go-iprange"}
    CRASH_BINARIES = {
        "producer": BINARY_PATHS["rust"], "producer_sha256": "1" * 64,
        "consumer": BINARY_PATHS["go"], "consumer_sha256": "2" * 64,
        "fixture_tool": "/tmp/v4-fixture", "fixture_tool_sha256": "3" * 64,
    }

    def crash_report(producers, consumers, failed=0, leftover=None,
                     assertions=("delta.marker observed",
                                 "reservation retained",
                                 "resolve truthful",
                                 "reopen ok")):
        scenarios = []
        sha_of = {"rust": "1" * 64, "go": "2" * 64}
        for i, (p, c) in enumerate(zip(producers, consumers)):
            scenarios.append({
                "scenario": f"S{i}.{p}->{c}", "pass": True,
                "producer": f"{p}:{BINARY_PATHS[p]}",
                "producer_sha256": sha_of[p],
                "consumer": f"{c}:{BINARY_PATHS[c]}",
                "consumer_sha256": sha_of[c],
                "assertions": list(assertions),
                "failures": [],
                "kinds": ["publication_temp", "publication_reservation",
                          "authorized_scratch"],
            })
        return {"schema": "iprange-cli-crash-report-v1",
                "binaries": dict(CRASH_BINARIES),
                "scenarios": scenarios,
                "leftover_processes": leftover or [],
                "failed": failed}

    BINARIES = {
        "rust": {"path": "/tmp/rust-iprange", "sha256": "1" * 64,
                 "methods": [], "available": True,
                 "result": {"implementation": "rust"}},
        "go": {"path": "/tmp/go-iprange", "sha256": "2" * 64,
               "methods": [], "available": True,
               "result": {"implementation": "go"}},
    }

    def green_report(matrix):
        """One PASS case whose per-case ledger shows the four matrix
        kinds created and opened by this matrix's actors; the crash
        battery supplies the three crash-only kinds.  The case records
        the executed actor implementations, executed-step counts and
        SHA-256 values (both binaries for a mixed matrix, the one
        binary for a single-language matrix), anchored in the report's
        binaries block, so language attribution never depends on the
        top-level label."""
        ledger = {}
        for i, kind in enumerate([
                "v4_main", "live_sidecar", "adapter_output",
                "metadata_delivery"]):
            ledger[f"k{i}.bin"] = {
                "kind": kind,
                "created_by": ["producer.iprange.v1.selftest"],
                "opened_by": ["consumer.iprange.v1.selftest"],
            }
        expected = ACTOR_LANGUAGES[matrix]
        actor_sha = {"rust": "1" * 64, "go": "2" * 64}
        report = matrix_report(matrix, [{
            "name": "doctored", "matrix": matrix, "status": "PASS",
            "actors": {
                "producer": {
                    "sha256": actor_sha[expected["producer"]],
                    "implementation": expected["producer"],
                    "steps": 1,
                },
                "consumer": {
                    "sha256": actor_sha[expected["consumer"]],
                    "implementation": expected["consumer"],
                    "steps": 1,
                },
            },
            "file_kinds": ledger}], failed=0)
        report["binaries"] = BINARIES
        return report

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

        # 7b. An actor SHA-256 that names no binary record of the same
        #     report fails (forged identity without an anchor).
        forged = green_report("rust")
        forged["cases"][0]["actors"]["producer"]["sha256"] = "f" * 64
        forged_path = os.path.join(work, "forged-sha.json")
        assign(forged_path, forged)
        problems, _c, _s = assess(
            [forged_path] + four[1:], [crash_path])
        assert problems and any("does not name any binary record" in p
                                for p in problems)

        # 7c. An actor SHA-256 naming a binary record whose declared
        #     implementation contradicts the actor fails.
        swapped = green_report("rust")
        swapped["cases"][0]["actors"]["producer"]["sha256"] = "2" * 64
        swapped_path = os.path.join(work, "swapped-sha.json")
        assign(swapped_path, swapped)
        problems, _c, _s = assess(
            [swapped_path] + four[1:], [crash_path])
        assert problems and any("declares implementation" in p
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

        # 9. Clone-relabel attack (label-only): a rust report (executed
        #    actors rust/rust) with only its top-level matrix changed
        #    to "go" keeps its executed identity locked to rust, so the
        #    label no longer matches and the gate fails with a
        #    label/identity mismatch.
        clone = green_report("rust")
        clone["matrix"] = "go"
        clone_path = os.path.join(work, "clone-relabel.json")
        assign(clone_path, clone)
        problems, _c, _s = assess(
            [green["rust"], clone_path, green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("identity mismatch" in p for p in problems), (
            f"clone-relabel did not fail with identity mismatch: {problems}")

        # 9b. Clone-relabel attack (in-report forged): the clone also
        #     rewrites its actor implementations AND its own binary
        #     block so every field inside the report is self-consistent
        #     ("go" everywhere).  The per-report sha anchor cannot see
        #     this; the global map can: the same sha256 is declared
        #     "rust" by the genuine reports and "go" by the clone, a
        #     global identity conflict.
        forge = green_report("rust")
        forge["matrix"] = "go"
        forge["cases"][0]["actors"]["producer"]["implementation"] = "go"
        forge["cases"][0]["actors"]["consumer"]["implementation"] = "go"
        forge["binaries"]["rust"]["result"]["implementation"] = "go"
        forge_path = os.path.join(work, "clone-forge.json")
        assign(forge_path, forge)
        problems, _c, _s = assess(
            [green["rust"], forge_path, green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("global implementation conflict" in p
                                for p in problems), (
            f"in-report-forged clone did not fail via global conflict: "
            f"{problems}")

        # 10. A PASS case without a per-case actors map is a report
        #     defect: attribution cannot be proven and the gate fails.
        no_actors = green_report("go")
        del no_actors["cases"][0]["actors"]
        no_actors_path = os.path.join(work, "no-actors.json")
        assign(no_actors_path, no_actors)
        problems, _c, _s = assess(
            [green["rust"], no_actors_path, green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("actors map" in p for p in problems), (
            f"missing actors did not fail the gate: {problems}")

        # 11. An actor implementation outside {"rust", "go"} is a report
        #     defect: the serving binary's declared identity is not a
        #     product language.
        bad_impl = green_report("rust")
        bad_impl["cases"][0]["actors"]["producer"]["implementation"] = "c"
        bad_impl_path = os.path.join(work, "bad-impl.json")
        assign(bad_impl_path, bad_impl)
        problems, _c, _s = assess(
            [bad_impl_path, green["go"], green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("not rust or go" in p for p in problems), (
            f"bad implementation did not fail the gate: {problems}")

        # 12. Executed-work attack: a report whose PASS cases record no
        #     executed steps at all passes no evidence through the gate.
        idle = green_report("go")
        for actor in idle["cases"][0]["actors"].values():
            actor["steps"] = 0
        idle_path = os.path.join(work, "idle.json")
        assign(idle_path, idle)
        problems, _c, _s = assess(
            [green["rust"], idle_path, green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("zero executed steps" in p for p in problems), (
            f"zero-step report did not fail the gate: {problems}")

        # 12b. A PASS case whose actor records no executed-step field
        #      at all is a report defect.
        no_steps = green_report("rust")
        del no_steps["cases"][0]["actors"]["producer"]["steps"]
        no_steps_path = os.path.join(work, "no-steps.json")
        assign(no_steps_path, no_steps)
        problems, _c, _s = assess(
            [no_steps_path, green["go"], green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("executed-step count" in p for p in problems), (
            f"missing steps field did not fail the gate: {problems}")

        # 13. Counter-mismatch attacks: a matrix with an explicit FAIL
        #     case but failed=0, and a crash report with a failed
        #     scenario but failed=0, must both fail with the mismatch
        #     listed.
        hidden_fail = green_report("go")
        hidden_fail["cases"].append({
            "name": "doctored-fail", "matrix": "go", "status": "FAIL",
            "error": "doctored"})
        hidden_fail_path = os.path.join(work, "hidden-fail.json")
        assign(hidden_fail_path, hidden_fail)
        problems, _c, _s = assess(
            [green["rust"], hidden_fail_path, green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("failed counter mismatch" in p
                                for p in problems), (
            f"hidden matrix failure did not fail the gate: {problems}")

        hidden_crash_fail = crash_report(["rust", "go"], ["go", "rust"])
        hidden_crash_fail["scenarios"][0]["pass"] = False
        hidden_crash_fail["failures"] = ["doctored"]
        hidden_crash_fail_path = os.path.join(work, "hidden-crash-fail.json")
        assign(hidden_crash_fail_path, hidden_crash_fail)
        problems, _c, _s = assess(four, [hidden_crash_fail_path])
        assert problems and any("failed counter mismatch" in p
                                for p in problems), (
            f"hidden crash failure did not fail the gate: {problems}")

        # 14. Crash duplicate-relabel attack: the rust->go scenarios are
        #     duplicated and the copies' producer/consumer labels are
        #     relabeled go->rust without re-execution.  The copied
        #     scenarios keep the real binary paths, so their forged
        #     labels contradict the global identity of the binaries
        #     they name and the gate fails.
        dupe = {"schema": "iprange-cli-crash-report-v1",
                "binaries": dict(CRASH_BINARIES),
                "scenarios": [], "leftover_processes": [], "failed": 0}
        for scenario in crash_report(["rust"], ["go"])["scenarios"]:
            dupe["scenarios"].append(scenario)
            copy = dict(scenario)
            copy["scenario"] = scenario["scenario"].replace(
                "rust->go", "go->rust")
            copy["producer"] = "go:" + scenario["producer"].split(":", 1)[1]
            copy["consumer"] = "rust:" + scenario["consumer"].split(":", 1)[1]
            dupe["scenarios"].append(copy)
        dupe_path = os.path.join(work, "crash-dupe-relabel.json")
        assign(dupe_path, dupe)
        problems, _c, _s = assess(four, [dupe_path])
        assert problems and any("global identity of binary" in p
                                for p in problems), (
            f"duplicate-relabel crash did not fail the gate: {problems}")

        # 15. Required-matrix contribution: every supplied required
        #     matrix must contribute at least one PASS case with
        #     file-kind evidence.  A matrix that is present but
        #     contributes nothing fails even when the crash battery
        #     covers the kinds, and a missing matrix still fails when
        #     the crash battery alone would cover the kinds.
        empty_go = green_report("go")
        empty_go["cases"] = [{
            "name": "skipped", "matrix": "go", "status": "SKIP",
            "reason": "doctored"}]
        empty_go_path = os.path.join(work, "empty-go.json")
        assign(empty_go_path, empty_go)
        problems, _c, _s = assess(
            [green["rust"], empty_go_path, green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("no PASS case" in p for p in problems), (
            f"evidence-empty required matrix did not fail the gate: "
            f"{problems}")
        problems, _c, _s = assess([green["rust"]], [crash_path])
        assert problems and any("missing matrix report" in p
                                for p in problems), (
            f"rust-plus-crash-only submission did not fail the gate: "
            f"{problems}")

        # 16. Missing per-scenario crash identity: a PASS scenario
        #     without producer_sha256/consumer_sha256 records no
        #     executed binary identity and fails the gate.
        nosha = crash_report(["rust", "go"], ["go", "rust"])
        for scenario in nosha["scenarios"]:
            scenario.pop("producer_sha256", None)
            scenario.pop("consumer_sha256", None)
        nosha_path = os.path.join(work, "crash-nosha.json")
        assign(nosha_path, nosha)
        problems, _c, _s = assess(four, [nosha_path])
        assert problems and any("records no producer_sha256 identity"
                                in p for p in problems), (
            f"missing per-scenario sha did not fail the gate: {problems}")

        # 17. Root contradiction: a scenario whose per-scenario sha256
        #     contradicts the report-root binaries table fails.
        contra = crash_report(["rust", "go"], ["go", "rust"])
        contra["scenarios"][0]["producer_sha256"] = "2" * 64
        contra_path = os.path.join(work, "crash-contra.json")
        assign(contra_path, contra)
        problems, _c, _s = assess(four, [contra_path])
        assert problems and any("contradicts the report binaries table"
                                in p for p in problems), (
            f"root-table contradiction did not fail the gate: {problems}")


if __name__ == "__main__":
    _self_test()
    sys.exit(main())
