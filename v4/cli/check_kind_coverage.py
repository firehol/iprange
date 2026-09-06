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
- Executed-operation records are mandatory: every PASS matrix case
  must record per-actor ``operations`` lists (the method names each
  binary executed) and every PASS crash scenario must record a
  per-actor ``operations`` map (``{role: [method, ...]}``).  Lineage
  refs are checked against these records: a matrix ref
  ``actor.operation`` must name a real recorded operation of that
  actor (there is no free ``legacy`` marker) and a crash ref
  ``actor.ordinal`` must index that actor's list; every created ref
  and every opened ref must name a method that can actually create
  (respectively open) that kind when executed by that actor, per the
  per-kind per-actor capability maps MATRIX_CREATE_METHODS /
  MATRIX_OPEN_METHODS on the matrix side and CRASH_CREATE_METHODS /
  CRASH_OPEN_METHODS on the crash side -- crediting current.publish
  with recovery scratch or maintenance.list with a live reader open
  is a fabricated lineage, not merely an in-range ordinal.  Unknown
  actors, unknown operations,
  out-of-range ordinals, fabricated opens, and empty operation lists
  on actors that recorded executed work fail the gate.
- Mixed matrices (``rust_to_go``, ``go_to_rust``) execute both
  binaries in every PASS case: producer and consumer must each
  record at least one executed step independently.  Single-language
  matrices keep the aggregate step rule (one actor may legitimately
  be idle).
- Command provenance is single-authority and bound to the report's
  own binary records: every recorded command is replayed through the
  exact argparse option definitions of the runner that executed it
  (``run.py main()`` for matrices, ``crash_harness main()`` for
  crashes) with abbreviations disabled, so an abbreviated identity
  flag (``--mat``, ``--g``, ``--prod``, ``--fixture``) is rejected as
  non-canonical; repeated identity options (``--matrix``,
  ``--producer``, ``--consumer``, ``--rust``, ``--go``,
  ``--fixture-tool``) fail; the effective (final) ``--matrix`` value
  must equal the report matrix; the matrix command's ``--rust`` /
  ``--go`` paths and the crash command's ``--producer`` /
  ``--consumer`` paths must name binaries the report records, and
  those binaries must resolve through the global sha256 ->
  implementation map to the language the flag names; every command's
  ``--fixture-tool`` value must name the fixture binary the battery's
  crash report records.
- Crash scenarios must keep their artifact evidence: every PASS
  scenario records a non-empty ``destination_state`` object and a
  ``reopen_outcome`` object; emptied or missing state is a report
  defect.
- ``live_sidecar`` and ``adapter_output`` imply a cross-process
  reader: both-language opened coverage is required, and empty
  opened coverage fails the gate instead of vacating the
  requirement.
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
- Crash evidence is mandatory: at least one crash report must be
  supplied and at least one of its scenarios must PASS.  The three
  crash-only kinds (``publication_reservation``,
  ``publication_temp``, ``authorized_scratch``) additionally require
  at least one crash scenario contributing the kind: fabricating
  them through matrix file ledgers alone cannot satisfy the gate.
- Every case's per-case ``matrix`` field must name its report's
  matrix, and a matrix report's ``command`` argv must pass the same
  matrix label via ``--matrix``; a crash report's ``command`` argv
  must name the report-root binaries table paths for
  ``--producer``/``--consumer``/``--fixture-tool``.
- Matrix per-case argv is the execution anchor for the role a case
  claims: every PASS matrix case records ``actors.<role>.argv``
  (the realpath of the binary that served that role), each argv must
  be an absolute path, and each argv realpath must equal the path of
  the report binary record (matched by the actor sha256) that served
  the role -- whose record must itself carry a path.  A relabeled
  case whose argv names the binary it actually executed contradicts
  the role identity it claims; a record without argv, or whose binary
  record lacks a path, fails unconditionally (second role-round
  finding closed the pre-regen escape hatch: the committed evidence
  is argv-era).
- Every PASS crash scenario's producer/consumer ``impl:path`` must
  appear in the report-root binaries table and the table's sha256
  for that path must equal the scenario's sha256.
- Matrix kinds are credited only to actors that recorded positive
  executed-step counts: a kind credited to an actor with zero
  executed steps is a report defect and that credit is dropped.
- Crash scenarios record per-kind actor lineage
  (``kinds = {kind: {"created_by": ["producer.0"],
  "opened_by": ["consumer.0"]}}``): creation is credited only from
  ``created_by`` actors and opening only from ``opened_by`` actors.
  Malformed lineage (non-object kinds, missing keys, unknown actor
  prefixes, empty ``created_by``) fails; the old flat kind list
  carries no actor lineage and is rejected as legacy evidence.

Honest limitations.  This gate is a mechanical consistency and
identity anchor: every check compares fields inside and across the
supplied reports.  A fully consistent offline forgery of ALL reports
of one revision -- one that rewrites every matrix and crash report,
the global sha256 map, the binaries tables, the commands, and the
per-case argv consistently -- is not mechanically distinguishable by
this gate alone; such forgeries are caught by the adversarial review
reruns that remain part of the gate process.  Fixture identity is
enforced as a cross-report consistency anchor: the crash report root
binaries table is the authority every recorded command must name, but
no on-disk hash of the fixture binary can be required for committed
evidence whose build-time paths no longer exist on the review machine.

Exit status 0 when every required kind has both-language evidence and
no report problem exists; 1 otherwise.
"""

import argparse
import json
import os
import shlex
import sys
import tempfile

# The parser derivation imports the runner modules (run, crash_harness)
# for their exact globals; they live next to this gate.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))


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
# Report matrix label -> the per-case ``matrix`` field values the
# runner may emit for that report (mixed matrices write the direction
# with '->' inside each case while the report root uses '_').
CASE_MATRIX_NAMES = {
    "rust": ("rust",),
    "go": ("go",),
    "rust_to_go": ("rust_to_go", "rust->go"),
    "go_to_rust": ("go_to_rust", "go->rust"),
}
# Kinds that only the crash battery observes; they must be backed by
# at least one complying crash scenario, never by matrices alone.
CRASH_ONLY_KINDS = (
    "publication_reservation",
    "publication_temp",
    "authorized_scratch",
)
# Kinds whose contract implies a cross-process reader: both-language
# opened coverage is mandatory; empty opened coverage is a FAIL, not a
# vacuous pass.
REQUIRED_OPENED_KINDS = ("live_sidecar", "adapter_output")
# Per-kind method-capability maps.  A lineage ref must name an
# operation that can actually create (respectively open) that kind;
# an in-range ordinal naming maintenance.list or reader.close is a
# fabricated credit, not a lineage.  Capability is a property of the
# method, NOT of the actor who executes it: the approved explicit-actor
# contract lets either actor create, transform, or read (external
# review finding), so the maps are per-kind method sets independent of
# actor naming.  The actor remains the execution and language
# attribution authority: which binary ran, and therefore which
# language is credited for a kind, comes exclusively from the
# per-case/scenario actor records.
#
# The sets are the AUTHORITATIVE sets, derived from the committed
# genuine evidence (v4/cli/evidence/matrix-*.json and crash.json:
# every recorded operation that actually creates/opens each kind per
# actor) and from the crash harness recording call sites
# (crash_harness.py _record_creation, _record_live_open,
# _record_adapter_open, _consumer_opened_main).  A kind absent from a
# map has no capable method: any ref naming it fails.

# Crash-side creation: current.publish creates the reservation/temp/
# main, recover creates scratch (and, when the kill lands in its
# output phase, the recovery reservation/temp), initialize_live
# creates the sidecar, export creates the adapter output.
CRASH_CREATE_METHODS = {
    "v4_main": ("iprange.v1.current.publish",),
    "live_sidecar": ("iprange.v1.database.initialize_live",),
    "publication_reservation": ("iprange.v1.current.publish",
                                "iprange.v1.recover"),
    "publication_temp": ("iprange.v1.current.publish",
                         "iprange.v1.recover"),
    "authorized_scratch": ("iprange.v1.recover",),
    "adapter_output": ("iprange.v1.export",),
}

# Crash-side opens: the consumer's successful reader.open for v4_main,
# the resolver's live-mode database.info plus the consumer's live
# reader.open for the sidecar, and the crashed export writer's open
# for adapter_output.
CRASH_OPEN_METHODS = {
    "v4_main": ("iprange.v1.reader.open",),
    "live_sidecar": ("iprange.v1.database.info",
                     "iprange.v1.reader.open"),
    "adapter_output": ("iprange.v1.export",),
}

# Matrix-side creation credits observed in the committed matrix
# evidence (run.py record_ledger credits the step method whose
# inventory delta produced the file).
MATRIX_CREATE_METHODS = {
    "v4_main": ("iprange.v1.algebra.publish",
                "iprange.v1.current.publish",
                "iprange.v1.database.create",
                "iprange.v1.recover",
                "iprange.v1.snapshot"),
    "live_sidecar": ("iprange.v1.database.create",
                     "iprange.v1.database.initialize_live"),
    "publication_reservation": ("iprange.v1.current.publish",),
    "publication_temp": ("iprange.v1.current.publish",
                         "iprange.v1.recover"),
    "authorized_scratch": ("iprange.v1.recover",),
    "adapter_output": ("iprange.v1.export",
                       "iprange.v1.maintenance.list",
                       "iprange.v1.recover",
                       "iprange.v1.retention.first_seen.refresh",
                       "iprange.v1.join.direct",
                       "iprange.v1.join.membership",
                       "iprange.v1.query.cardinalities",
                       "iprange.v1.query.matching_feeds",
                       "iprange.v1.query.overlaps",
                       "iprange.v1.validate"),
    "metadata_delivery": ("iprange.v1.database.metadata.get",),
}

# Matrix-side open credits observed in the committed matrix evidence.
# adapter_output has no matrix-side opener record: the only
# adapter-output opener is the producer export writer observed by the
# crash battery, so any matrix opened ref on adapter_output fails.
MATRIX_OPEN_METHODS = {
    "v4_main": ("iprange.v1.algebra.publish",
                "iprange.v1.commit.resolve",
                "iprange.v1.database.create.resolve",
                "iprange.v1.database.info",
                "iprange.v1.database.metadata.replace",
                "iprange.v1.database.metadata.get",
                "iprange.v1.database.reclaim",
                "iprange.v1.database.reset_live",
                "iprange.v1.direct.replace",
                "iprange.v1.feeds.create",
                "iprange.v1.feeds.delete",
                "iprange.v1.feeds.import",
                "iprange.v1.feeds.rename",
                "iprange.v1.feeds.replace",
                "iprange.v1.history.project",
                "iprange.v1.join.direct",
                "iprange.v1.join.membership",
                "iprange.v1.publication.inspect",
                "iprange.v1.publication.resolve",
                "iprange.v1.query.overlaps",
                "iprange.v1.reader.open",
                "iprange.v1.recovery.inspect",
                "iprange.v1.retention.first_seen.refresh",
                "iprange.v1.retention.last_seen.refresh",
                "iprange.v1.snapshot",
                "iprange.v1.validate"),
    "live_sidecar": ("iprange.v1.algebra.publish",
                     "iprange.v1.database.info",
                     "iprange.v1.database.metadata.get",
                     "iprange.v1.feeds.create",
                     "iprange.v1.history.project",
                     "iprange.v1.join.direct",
                     "iprange.v1.join.membership",
                     "iprange.v1.query.overlaps",
                     "iprange.v1.reader.open",
                     "iprange.v1.snapshot"),
}

# Parser caches for the single-authority command replay (lazy).
_RUN_PARSER = None
_CRASH_PARSER = None
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


def _crash_consumer_opened_main(scenario):
    """Mirror of crash_harness._consumer_opened_main: true when the
    scenario's recorded reopen_outcome proves the consumer opened the
    v4 main (probe_consumer_open, post-resolution reopen, or a live
    reader open)."""

    outcome = scenario.get("reopen_outcome") or {}
    if outcome.get("after_resolution") is not None:
        return True
    if (outcome.get("before_resolution") or {}).get(
            "opened_complete_destination") is True:
        return True
    if outcome.get("live") is not None:
        return True
    if outcome.get("consumer_live_reader_transaction_id") is not None:
        return True
    return False


def _open_fact_backed(facts, actor):
    """True when a scenario recorded an open fact for one actor.

    The crash harness records the open's operation ordinal at the call
    site (an int; 0 is a valid ordinal) and the committed pre-fix
    evidence records a plain True; both are backing evidence, while a
    missing record is not.
    """

    value = facts.get(actor) if isinstance(facts, dict) else None
    return value is True or (isinstance(value, int)
                             and not isinstance(value, bool))


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


def _matrix_path_to_sha(report):
    """Matrix-report binaries block: binary path -> sha256.

    Matrix reports record each product binary as a capability record
    with a ``path`` and ``sha256``; the command binding resolves the
    recorded ``--rust``/``--go`` path through this table and then
    through the global sha256 -> implementation map.
    """

    table = {}
    binaries = report.get("binaries")
    if not isinstance(binaries, dict):
        return table
    for record in binaries.values():
        if not isinstance(record, dict):
            continue
        path = record.get("path")
        sha = record.get("sha256")
        if isinstance(path, str) and isinstance(sha, str):
            table[path] = sha
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


def _command_argv(report):
    """Return the report ``command`` argv as a list, or None.

    The runner records ``sys.argv`` for every matrix and crash
    report, so the replayed invocation is part of the evidence: a
    forger must make the recorded command agree with the report.
    Both list argv (the runner output) and a string command
    (whitespace separated) are accepted.
    """

    command = report.get("command")
    if isinstance(command, str):
        try:
            command = shlex.split(command)
        except ValueError:
            return None
    if isinstance(command, list) and all(
            isinstance(token, str) for token in command):
        return command
    return None


def _argv_pairs(argv):
    """Yield ``(flag, value)`` for every ``--flag value`` / ``--flag=value`` pair."""

    for index, token in enumerate(argv):
        if not isinstance(token, str) or not token.startswith("--"):
            continue
        if "=" in token:
            flag, value = token.split("=", 1)
            yield flag, value
        elif index + 1 < len(argv):
            yield token, argv[index + 1]


def _lift_main_parser(runner_name, extra_env):
    """Derive a runner's argparse from its own ``main()`` function.

    Single-authority option semantics: the gate replays a recorded
    command through the exact option definitions the runner itself
    executes, lifted mechanically from the runner's ``main()`` so the
    two can never drift.  The runners accept abbreviated flags
    (argparse default), so a forged command can append a short flag
    (``--mat``, ``--g``, ``--prod``, ``--fixture``) whose effective
    value the real runner honors but a literal last-value scan never
    sees; the gate parses the lifted parser with ``allow_abbrev=False``,
    which makes every abbreviated token unrecognized and the command
    non-canonical.  If a runner's ``main()`` shape ever changes such
    that the lift fails, callers record a report problem instead of
    silently skipping the command check.
    """

    import ast
    runner_path = os.path.join(
        os.path.dirname(os.path.abspath(__file__)), runner_name)
    with open(runner_path, encoding="utf-8") as stream:
        tree = ast.parse(stream.read(), runner_path)
    main_fn = next(node for node in tree.body
                   if isinstance(node, ast.FunctionDef)
                   and node.name == "main")
    statements = []
    for node in main_fn.body:
        if (isinstance(node, ast.Assign)
                and any(isinstance(target, ast.Name)
                        and target.id == "args"
                        for target in node.targets)):
            break
        statements.append(node)
    env = dict(extra_env)
    env.setdefault("argparse", argparse)
    exec(compile(ast.Module(body=statements, type_ignores=[]),
                 f"{runner_path}:main[parser]", "exec"), env)
    parser = env["parser"]
    parser.allow_abbrev = False
    return parser


def _run_parser():
    """The run.py matrix-runner argparse with abbreviations off."""

    global _RUN_PARSER
    if _RUN_PARSER is None:
        import run as _run
        _RUN_PARSER = _lift_main_parser(
            "run.py",
            {"__doc__": _run.__doc__,
             "DEFAULT_CASE_DIR": _run.DEFAULT_CASE_DIR})
    return _RUN_PARSER


def _crash_parser():
    """The crash-harness argparse with abbreviations off."""

    global _CRASH_PARSER
    if _CRASH_PARSER is None:
        _CRASH_PARSER = _lift_main_parser("crash_harness.py", {})
    return _CRASH_PARSER


def _parse_recorded_command(parser, argv, label, problems):
    """Replay one recorded command through a runner-exact argparse.

    Returns the parsed namespace, or None after recording a problem.
    The runner executes argparse with abbreviations enabled, so an
    abbreviated flag token in the recorded command is either a replay
    defect or a forgery whose effective value the naive last-value
    scan would miss; with ``allow_abbrev=False`` the same token is
    unrecognized and the command is rejected as non-canonical.  The
    whole argv is replayed, so every literal flag occurrence is
    canonical too.  argparse diagnostics are suppressed; only the
    gate's own problem records surface.
    """

    import contextlib
    import io
    try:
        with contextlib.redirect_stderr(io.StringIO()):
            namespace, unknown = parser.parse_known_args(argv[1:])
    except SystemExit:
        problems.append(
            f"{label}: recorded command does not parse with the "
            f"runner's exact argparse (allow_abbrev=False)")
        return None
    if unknown:
        problems.append(
            f"{label}: recorded command carries non-canonical argparse "
            f"tokens {unknown} (abbreviated or unknown options are "
            f"rejected)")
        return None
    return namespace


def _argv_duplicates(argv, flags):
    """Return the identity flags supplied more than once in ``argv``.

    A repeated identity option is either a replay defect or a forgery
    ambiguity: argparse scalar options never consume two values, so
    the recorded command cannot be the executed command.  Callers
    fail the report when this returns non-empty.
    """

    duplicates = []
    for flag in flags:
        if sum(1 for seen_flag, _ in _argv_pairs(argv)
               if seen_flag == flag) > 1:
            duplicates.append(flag)
    return duplicates


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


def matrix_evidence(path, report, implementation_of, fixture_paths,
                      problems):
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

    Every case's per-case ``matrix`` field must name this report's
    matrix, and the recorded ``command`` argv must pass the same
    label via ``--matrix``, so the report identity is consistent
    inside each case and in the replayed invocation.  Kind credits
    are accepted only from actors that recorded positive executed
    step counts; a credit naming a zero-step actor is a report
    defect and that credit is dropped.
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
    # The per-case argv anchor is unconditional: the committed
    # evidence revision is argv-era, and the pre-regen escape hatch
    # (strip argv everywhere -> battery looks pre-argv -> pass) is a
    # closed bypass (second role-round finding).
    argv_required_here = True  # noqa: F841 - documents the rule
    argv = _command_argv(report)
    if argv is None:
        problems.append(
            f"matrix {path}: report records no command argv")
    else:
        duplicated = _argv_duplicates(
            argv, ("--matrix", "--rust", "--go", "--fixture-tool"))
        if duplicated:
            problems.append(
                f"matrix {path}: report command supplies the identity "
                f"option(s) {sorted(duplicated)} more than once")
        # Single-authority replay: the recorded command must parse with
        # the matrix runner's exact argparse and abbreviations off, so
        # an appended ``--mat``/``--g``/``--fixture`` override that the
        # real runner honors is a non-canonical command.
        try:
            runner_parser = _run_parser()
        except Exception as exc:
            problems.append(
                f"matrix {path}: cannot derive the matrix-runner parser "
                f"from run.py main(): {exc}")
            runner_parser = None
        namespace = None
        if runner_parser is not None:
            namespace = _parse_recorded_command(
                runner_parser, argv, f"matrix {path}", problems)
        # Per-language command-selected executable: the path the
        # recorded command names for each product language.  Each
        # actor must be bound to exactly this executable (external
        # review finding); a report that runs a different binary for a
        # role while its command claims another contradicts the
        # record.
        command_selected = {}
        if namespace is not None:
            command_matrix = namespace.matrix
            if command_matrix is None:
                problems.append(
                    f"matrix {path}: report command records no --matrix "
                    f"argument (report matrix is {matrix!r})")
            elif command_matrix != matrix:
                problems.append(
                    f"matrix {path}: report command --matrix "
                    f"{command_matrix!r} does not match report matrix "
                    f"{matrix!r}")
            report_shas = _matrix_path_to_sha(report)
            for flag, language, attr in (
                    ("--rust", "rust", "rust_binary"),
                    ("--go", "go", "go_binary")):
                named = getattr(namespace, attr)
                if named is None:
                    problems.append(
                        f"matrix {path}: report command records no {flag} "
                        f"argument")
                    continue
                bound_path = os.path.realpath(named)
                command_selected[language] = bound_path
                bound_sha = report_shas.get(bound_path)
                if bound_sha is None:
                    problems.append(
                        f"matrix {path}: report command {flag} {named!r} "
                        f"does not name any binary record of the report")
                    continue
                bound_implementation = implementation_of.get(bound_sha)
                if bound_implementation != language:
                    problems.append(
                        f"matrix {path}: report command {flag} {named!r} "
                        f"names binary {bound_path!r} (sha256 {bound_sha!r}) "
                        f"whose global implementation is "
                        f"{bound_implementation!r}, not {language!r}")
            # The fixture tool has no per-matrix binary record, so its
            # identity is anchored in the mandatory crash report's root
            # binaries table; a value that names no crash-recorded
            # fixture (e.g. /bin/false) changes the effective fixture
            # and fails the command binding.
            fixture = namespace.fixture_tool
            if fixture is None:
                problems.append(
                    f"matrix {path}: report command records no "
                    f"--fixture-tool argument")
            elif os.path.realpath(fixture) not in fixture_paths:
                problems.append(
                    f"matrix {path}: report command --fixture-tool "
                    f"{fixture!r} does not name the fixture binary the "
                    f"battery's crash report records "
                    f"({sorted(fixture_paths) or '<none>'})")
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
        case_matrix = case.get("matrix")
        if case_matrix not in CASE_MATRIX_NAMES[matrix]:
            if case_matrix is None and status != "PASS":
                # Non-PASS cases may omit the label; a PASS case must
                # record it so its evidence is attributable.
                continue
            problems.append(
                f"matrix {path}: case {case.get('name', '<unnamed>')!r} "
                f"records case matrix {case_matrix!r}, which does not "
                f"match report matrix {matrix!r} (expected "
                f"{' or '.join(repr(name) for name in CASE_MATRIX_NAMES[matrix])})")
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
        actor_steps = {}
        actor_operations = {}
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
            binary_path = None
            if isinstance(report.get("binaries"), dict) and isinstance(sha, str):
                for record in report["binaries"].values():
                    if isinstance(record, dict) and record.get("sha256") == sha:
                        declared = (record.get("result") or {}).get(
                            "implementation")
                        candidate_path = record.get("path")
                        if isinstance(candidate_path, str):
                            binary_path = candidate_path
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
            # Matrix-side execution anchor: in an argv-era revision
            # the runner records the realpath of the binary that
            # served each role; the argv must name the report binary
            # record the actor sha256 resolved to, so a relabeled
            # case carrying the argv of the binary it actually
            # executed contradicts the role it claims.
            if argv_required_here:
                actor_argv = entry.get("argv")
                if not (isinstance(actor_argv, str) and actor_argv):
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} actor "
                        f"{actor!r} records no argv (execution anchor)")
                    continue
                if not os.path.isabs(actor_argv):
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} actor "
                        f"{actor!r} argv {actor_argv!r} is not an "
                        f"absolute path")
                    continue
                if binary_path is None:
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} actor "
                        f"{actor!r} argv {actor_argv!r} resolves to no "
                        f"report binary record path (the record matched "
                        f"by sha256 {sha!r} carries no path)")
                    continue
                if os.path.realpath(actor_argv) != \
                        os.path.realpath(binary_path):
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} actor "
                        f"{actor!r} argv {actor_argv!r} does not resolve "
                        f"to the report binary path {binary_path!r} that "
                        f"served the role (sha256 {sha!r})")
            # Command/executable join (external review finding): the
            # binary that served the role must be the exact executable
            # the recorded command selected for its language.  The
            # per-case argv anchor and the command binding were two
            # independent authorities; joining them closes a report
            # that runs one binary for a role while its recorded
            # command claims another.
            if (implementation in PRODUCT_LANGUAGES
                    and binary_path is not None
                    and implementation in command_selected):
                if os.path.realpath(binary_path) !=                         os.path.realpath(command_selected[implementation]):
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} actor "
                        f"{actor!r} executable {binary_path!r} (sha256 "
                        f"{sha!r}) is not the binary the recorded "
                        f"command selected for {implementation} "
                        f"({command_selected[implementation]!r})")
            # Executed-work evidence: the runner records the executed
            # step count per actor; a case that executed nothing (or
            # was doctored to claim nothing) is not evidence.
            steps = entry.get("steps")
            if isinstance(steps, bool) or not isinstance(steps, int):
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} actor {actor!r} "
                    f"records no executed-step count")
                steps_complete = False
                actor_steps[actor] = None
            elif steps < 0:
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} actor {actor!r} "
                    f"records negative executed-step count {steps}")
                steps_complete = False
                actor_steps[actor] = None
            else:
                steps_sum += steps
                actor_steps[actor] = steps
            # The runner records every executed method of the actor;
            # without the executed-operation record a PASS case cannot
            # prove its lineage refs name executed work.
            operations = entry.get("operations")
            if not isinstance(operations, list) or any(
                    not isinstance(op, str) or not op
                    for op in operations):
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} actor "
                    f"{actor!r} records no executed-operation record")
                actor_operations[actor] = []
            else:
                actor_operations[actor] = operations
                executed = actor_steps.get(actor)
                if (isinstance(executed, int)
                        and not isinstance(executed, bool)
                        and executed > 0 and not operations):
                    # Executed work without any recorded operation is a
                    # report defect: lineage refs could not be checked
                    # against the actor's executed methods even if the
                    # refs were stripped to hide the emptiness.
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} actor "
                        f"{actor!r} records an empty executed-operation "
                        f"list despite {executed} executed step(s)")
        if steps_complete and steps_sum < 1:
            problems.append(
                f"matrix {path}: PASS case {case_name!r} records zero "
                f"executed steps (no executed-work evidence)")
        if matrix in ("rust_to_go", "go_to_rust") and steps_complete:
            # Mixed matrices execute both binaries for every PASS
            # case: each actor must record executed work on its own.
            # Single-language matrices legitimately leave one actor
            # idle (the aggregate step rule covers them).
            for actor in ALL_ACTORS:
                if actor_steps.get(actor, 0) < 1:
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} records "
                        f"zero executed {actor} steps (mixed matrix "
                        f"requires both actors to execute)")
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
            # A kind credit names the actor that performed the
            # operation; an actor that executed zero steps cannot be
            # credited anywhere (whole-case zero-step failures are
            # reported separately).  Unknown actor prefixes keep the
            # poisoned "?" marker so coverage checks must fail.
            for entry in facts.get("created_by", []):
                actor, operation = _matrix_ref(entry)
                if actor is None:
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} kind "
                        f"{facts['kind']!r} created_by ref {entry!r} "
                        f"carries an unknown actor or malformed "
                        f"operation")
                    bucket["created"].add("?")
                    continue
                if operation not in actor_operations.get(actor, ()):
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} kind "
                        f"{facts['kind']!r} created_by ref {entry!r} names "
                        f"an operation not recorded in actor {actor!r} "
                        f"executed operations (there is no free 'legacy' "
                        f"marker)")
                    bucket["created"].add("?")
                    continue
                if operation not in MATRIX_CREATE_METHODS.get(
                        facts["kind"], ()):
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} kind "
                        f"{facts['kind']!r} created_by ref {entry!r} names "
                        f"operation {operation!r} which is not a "
                        f"create-capable method of actor {actor!r} for "
                        f"this kind")
                    bucket["created"].add("?")
                    continue
                if actor_steps.get(actor) == 0:
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} kind "
                        f"{facts['kind']!r} credits creator actor {actor!r} "
                        f"with zero executed steps")
                    continue
                if actor_steps.get(actor) is None or \
                        actor_steps.get(actor) < 1:
                    continue
                bucket["created"].add(implementations.get(actor, "?"))
            for entry in facts.get("opened_by", []):
                actor, operation = _matrix_ref(entry)
                if actor is None:
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} kind "
                        f"{facts['kind']!r} opened_by ref {entry!r} "
                        f"carries an unknown actor or malformed "
                        f"operation")
                    bucket["opened"].add("?")
                    continue
                if operation not in actor_operations.get(actor, ()):
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} kind "
                        f"{facts['kind']!r} opened_by ref {entry!r} names "
                        f"an operation not recorded in actor {actor!r} "
                        f"executed operations (there is no free 'legacy' "
                        f"marker)")
                    bucket["opened"].add("?")
                    continue
                if actor_steps.get(actor) == 0:
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} kind "
                        f"{facts['kind']!r} credits opener actor {actor!r} "
                        f"with zero executed steps")
                    continue
                if actor_steps.get(actor) is None or \
                        actor_steps.get(actor) < 1:
                    continue
                # Mirror of the crash-side open contract: only kinds
                # the v1 open contract opens (v4_main, live_sidecar,
                # adapter_output) may carry an opened ref.  Any other
                # kind has no cross-process open, so the ref is a
                # fabricated open.
                if facts["kind"] not in ("live_sidecar",
                                         "adapter_output", "v4_main"):
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} kind "
                        f"{facts['kind']!r} records a cross-process open "
                        f"ref {entry!r} although no v1 open contract "
                        f"opens this kind")
                    continue
                if operation not in MATRIX_OPEN_METHODS.get(
                        facts["kind"], ()):
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} kind "
                        f"{facts['kind']!r} opened_by ref {entry!r} names "
                        f"operation {operation!r} which is not an "
                        f"open-capable method of actor {actor!r} for "
                        f"this kind")
                    continue
                bucket["opened"].add(implementations.get(actor, "?"))
    stats = {"cases": len(cases), "fail_cases": fail_cases,
             "pass_cases": pass_cases, "contributing": contributing}
    return matrix, evidence, stats, problems


def _matrix_ref(entry):
    """Split a matrix lineage ref ``actor.operation``.

    Returns ``(actor, operation)``, or ``(None, None)`` when the ref
    does not name a known actor (producer/consumer) with a non-empty
    operation.  The operation part is validated against the actor's
    recorded executed operations by the caller.
    """

    if not isinstance(entry, str) or "." not in entry:
        return None, None
    actor, operation = entry.split(".", 1)
    if actor not in ALL_ACTORS or not operation:
        return None, None
    return actor, operation


def _crash_ref_ordinal(ref):
    """Split a crash lineage ref ``actor.ordinal``.

    Crash refs index the per-actor executed-operation list recorded on
    the scenario; returns ``(actor, ordinal)`` or ``(None, None)``
    when the ref does not name a known actor with a decimal ordinal.
    """

    if not isinstance(ref, str) or "." not in ref:
        return None, None
    actor, tail = ref.split(".", 1)
    if actor not in ALL_ACTORS or not tail.isdigit():
        return None, None
    return actor, int(tail)


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
    stops before the artifact inventory runs.  Per-kind actor lineage
    decides attribution: creation is credited only from the
    ``created_by`` actors of each kind and opening only from its
    ``opened_by`` actors; the battery must span both language
    directions.  The legacy flat kind list carries no per-kind
    lineage and is rejected.

    Scenario identity comes from the per-scenario
    ``producer_sha256``/``consumer_sha256`` records the harness writes
    from the exact binaries it executes, never from the direction
    label: each role's ``impl:path`` must appear in the report-root
    binaries table, each role's sha256 must resolve through the
    global cross-report sha->implementation map to the implementation
    the scenario declares, and the table's sha256 for the path must
    equal the scenario's sha256.  A duplicated direction keeps the
    original binaries' shas, so its forged labels contradict the
    global identity of those binaries.
    """

    failed = report.get("failed", 0)
    if failed:
        problems.append(
            f"crash {path}: report records {failed} failed scenario(s)")
    leftover = report.get("leftover_processes")
    if leftover:
        problems.append(
            f"crash {path}: report records leftover product processes: {leftover}")
    root_binaries = report.get("binaries")
    if not isinstance(root_binaries, dict):
        root_binaries = {}
    argv = _command_argv(report)
    if argv is None:
        problems.append(
            f"crash {path}: report records no command argv")
    else:
        role_flags = {"producer": "--producer",
                      "consumer": "--consumer",
                      "fixture_tool": "--fixture-tool"}
        duplicated = _argv_duplicates(
            argv, ("--producer", "--consumer", "--fixture-tool"))
        if duplicated:
            problems.append(
                f"crash {path}: report command supplies the identity "
                f"option(s) {sorted(duplicated)} more than once")
        # Single-authority replay: the recorded command must parse with
        # the crash runner's exact shared argparse and abbreviations
        # off, so an appended ``--prod``/``--fixture`` override that
        # the real runner honors is a non-canonical command.
        namespace = _parse_recorded_command(
            _crash_parser(), argv, f"crash {path}", problems)
        if namespace is not None:
            for role in ("producer", "consumer", "fixture_tool"):
                table_path = root_binaries.get(role)
                flag = role_flags[role]
                named = getattr(namespace, role)
                if not isinstance(table_path, str):
                    problems.append(
                        f"crash {path}: report root binaries table records "
                        f"no {role} path")
                elif (not isinstance(named, str)
                      or os.path.realpath(named)
                      != os.path.realpath(table_path)):
                    problems.append(
                        f"crash {path}: report command {flag} {named!r} does "
                        f"not name the report root binaries table path "
                        f"{table_path!r}")
                elif role != "fixture_tool":
                    # The same path -> sha -> implementation binding the
                    # scenarios use: the named binary must resolve through
                    # the global map to a product language.
                    bound_sha = path_to_sha.get(table_path)
                    bound_implementation = None
                    if isinstance(bound_sha, str):
                        bound_implementation = implementation_of.get(
                            bound_sha)
                    if bound_implementation not in PRODUCT_LANGUAGES:
                        problems.append(
                            f"crash {path}: report command {flag} {named!r} "
                            f"names binary {table_path!r} sha256 "
                            f"{bound_sha!r} which does not resolve through "
                            f"the global implementation map")
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
        # Every PASS scenario must keep its recorded artifact state: a
        # scenario whose destination state or reopen outcome was
        # emptied has no evidence of what the crash left behind.
        destination_state = scenario.get("destination_state")
        if not isinstance(destination_state, dict) or not destination_state:
            problems.append(
                f"crash {path}: PASS scenario {scenario_name!r} records "
                f"no destination_state artifact evidence")
        reopen_outcome = scenario.get("reopen_outcome")
        if not isinstance(reopen_outcome, dict):
            problems.append(
                f"crash {path}: PASS scenario {scenario_name!r} records "
                f"no reopen_outcome (must be an object)")
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
        # Executed-operation record: the crash harness records the
        # executed methods per actor (``operations = {role: [method,
        # ...]}``); lineage refs are ordinals into those lists, so a
        # scenario without the record has no executed-operation
        # evidence.
        scenario_operations = scenario.get("operations")
        if not (isinstance(scenario_operations, dict)
                and all(isinstance(scenario_operations.get(role), list)
                        and all(isinstance(op, str) and op
                                for op in scenario_operations[role])
                        for role in ALL_ACTORS)):
            problems.append(
                f"crash {path}: PASS scenario {scenario_name!r} records "
                f"no executed-operation record")
            scenario_operations = None
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
            if root_sha is None:
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} "
                    f"{role} binary path {binary_path!r} is absent from "
                    f"the report root binaries table")
                continue
            if root_sha != sha:
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
        # Per-kind actor lineage: the harness records for every
        # observed kind which scenario actors created and opened it.
        # Only named creators and openers are credited; a legacy
        # flat kind list carries no per-kind lineage and is
        # rejected.
        kinds = scenario.get("kinds")
        if kinds is None:
            kinds = {}
        if isinstance(kinds, list):
            problems.append(
                f"crash {path}: PASS scenario {scenario_name!r} records a "
                f"legacy flat kinds list carries no actor lineage")
            kinds = {}
        elif not isinstance(kinds, dict):
            problems.append(
                f"crash {path}: PASS scenario {scenario_name!r} records "
                f"kinds that are neither a lineage object nor a list: "
                f"{kinds!r}")
            kinds = {}
        for kind, lineage in kinds.items():
            if not isinstance(lineage, dict):
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} kind "
                    f"{kind!r} records lineage that is not an object")
                continue
            created_by = lineage.get("created_by")
            opened_by = lineage.get("opened_by")
            if "created_by" not in lineage or "opened_by" not in lineage:
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} kind "
                    f"{kind!r} lineage lacks created_by/opened_by keys")
            if not isinstance(created_by, list):
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} kind "
                    f"{kind!r} created_by is not a list")
                created_by = []
            if not isinstance(opened_by, list):
                problems.append(
                    f"crash {path}: PASS scenario {scenario_name!r} kind "
                    f"{kind!r} opened_by is not a list")
                opened_by = []
            if not created_by:
                # Scenarios whose v4 main was produced by the external
                # v4-fixture tool (B, D) truthfully record no product
                # creator ref for v4_main; the kind coverage is met by
                # the publish scenarios (A1, A2, E, F).  Any other
                # kind, and any scenario without the flag, must name a
                # creator.
                fixture_main = (kind == "v4_main" and bool(
                    scenario.get("fixture_created_main")))
                if not fixture_main:
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} kind "
                        f"{kind!r} records empty created_by lineage")
            bucket = evidence.setdefault(kind, {"created": set(),
                                                "opened": set()})
            for entry in created_by:
                actor, ordinal = _crash_ref_ordinal(entry)
                if actor is None:
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} created_by ref {entry!r} carries "
                        f"an unknown or malformed actor prefix or ordinal")
                    continue
                if scenario_operations is not None and \
                        ordinal >= len(scenario_operations.get(actor, ())):
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} created_by ref {entry!r} names "
                        f"operation ordinal {ordinal} beyond the recorded "
                        f"executed operations of actor {actor!r}")
                    continue
                if scenario_operations is not None and \
                        scenario_operations.get(actor, ())[ordinal] not in \
                        CRASH_CREATE_METHODS.get(kind, ()):
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} created_by ref {entry!r} names "
                        f"operation "
                        f"{scenario_operations.get(actor, ())[ordinal]!r} "
                        f"which is not a create-capable method of actor "
                        f"{actor!r} for this kind")
                    continue
                # The ref must name the EXACT recorded creation event:
                # the scenario records the producer operation ordinal
                # whose call actually created the kind
                # (crash_harness _record_creation -> created_ordinals).
                # An in-range ordinal alone cannot distinguish a failed
                # earlier call from the later successful one (external
                # review finding), so a ref that does not match the
                # recorded ordinal -- or a report that omits the
                # created_ordinals record entirely -- fails.
                recorded_created = (
                    scenario.get("created_ordinals") or {}).get(kind)
                if not (isinstance(recorded_created, int)
                        and not isinstance(recorded_created, bool)):
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} created_by ref {entry!r} records "
                        f"no created ordinal for the kind (the exact "
                        f"creation event is unrecorded)")
                    continue
                if recorded_created != ordinal:
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} created_by ref {entry!r} names "
                        f"operation ordinal {ordinal} but the recorded "
                        f"creation of this kind is at ordinal "
                        f"{recorded_created}")
                    continue
                language = (producer_impl if actor == "producer"
                            else consumer_impl)
                bucket["created"].add(language or "?")
            for entry in opened_by:
                actor, ordinal = _crash_ref_ordinal(entry)
                if actor is None:
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} opened_by ref {entry!r} carries "
                        f"an unknown or malformed actor prefix or ordinal")
                    continue
                if scenario_operations is not None and \
                        ordinal >= len(scenario_operations.get(actor, ())):
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} opened_by ref {entry!r} names "
                        f"operation ordinal {ordinal} beyond the recorded "
                        f"executed operations of actor {actor!r}")
                    continue
                # Open refs must be semantically compatible with the
                # kind and actor: the operation at the ordinal must be
                # an open-capable method of that actor for that kind,
                # not merely in range (a ``reader.close`` or a
                # ``maintenance.list`` at the right index is a
                # fabricated open).  CRASH_OPEN_METHODS is the
                # per-kind per-actor authority; the multi-writer kinds
                # additionally keep the backing-fact checks below.
                if scenario_operations is not None and \
                        scenario_operations.get(actor, ())[ordinal] not in \
                        CRASH_OPEN_METHODS.get(kind, ()):
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} opened_by ref {entry!r} names "
                        f"operation "
                        f"{scenario_operations.get(actor, ())[ordinal]!r} "
                        f"which is not an open-capable method of actor "
                        f"{actor!r} for this kind")
                    continue
                # Open refs must be backed by the scenario's recorded
                # open facts: live_sidecar opens require the actor in
                # live_reader_opens, adapter_output opens require the
                # actor in adapter_output_opens, v4_main opens require
                # the consumer reopen proof, and every other kind has
                # no cross-process open contract (an opened ref is a
                # fabricated open).  The harness records these facts
                # at the call sites (crash_harness.py _record_live_open
                # / _record_adapter_open / _consumer_opened_main).
                open_facts = scenario.get("live_reader_opens") or {}
                adapter_facts = scenario.get(
                    "adapter_output_opens") or {}
                if kind == "live_sidecar" and \
                        not _open_fact_backed(open_facts, actor):
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} opened_by ref {entry!r} is not "
                        f"backed by a recorded live reader open of "
                        f"actor {actor!r}")
                    continue
                if kind == "adapter_output" and \
                        not _open_fact_backed(adapter_facts, actor):
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} opened_by ref {entry!r} is not "
                        f"backed by a recorded adapter-output open of "
                        f"actor {actor!r}")
                    continue
                if kind == "v4_main" and not (
                        actor == "consumer"
                        and _crash_consumer_opened_main(scenario)):
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} opened_by ref {entry!r} is not "
                        f"backed by a recorded consumer main open")
                    continue
                if kind not in ("live_sidecar", "adapter_output",
                                "v4_main"):
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} records a cross-process open "
                        f"ref {entry!r} although no v1 open contract "
                        f"opens this kind")
                    continue
                # The ref must name the EXACT recorded open event: the
                # scenario stores the service-call ordinal that
                # actually opened the kind (live_reader_opens /
                # adapter_output_opens / consumer_main_open_ordinal).
                # A ref naming a different executed operation is a
                # fabricated open even when the method is capable
                # (external review finding).
                recorded_open = None
                if kind == "live_sidecar":
                    value = open_facts.get(actor)
                    recorded_open = 0 if value is True else value
                elif kind == "adapter_output":
                    value = adapter_facts.get(actor)
                    recorded_open = 0 if value is True else value
                elif kind == "v4_main":
                    recorded_open = (
                        scenario.get("reopen_outcome") or {}).get(
                        "consumer_main_open_ordinal")
                if not (isinstance(recorded_open, int)
                        and not isinstance(recorded_open, bool)):
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} opened_by ref {entry!r} records "
                        f"no exact open ordinal for this kind and actor")
                    continue
                if recorded_open != ordinal:
                    problems.append(
                        f"crash {path}: PASS scenario {scenario_name!r} "
                        f"kind {kind!r} opened_by ref {entry!r} names "
                        f"operation ordinal {ordinal} but the recorded "
                        f"{kind} open of actor {actor!r} is at ordinal "
                        f"{recorded_open}")
                    continue
                language = (producer_impl if actor == "producer"
                            else consumer_impl)
                bucket["opened"].add(language or "?")
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
    crash_pass_seen = False
    kind_sources = {}
    implementation_of = _global_implementation_map(
        matrix_paths, crash_paths, problems)
    # Fixture identity of the battery: the crash report root binaries
    # table is the only record of the v4-fixture tool every report's
    # command names, so it is the authority for the matrix commands'
    # ``--fixture-tool`` binding.
    fixture_paths = set()
    fixture_shas = {}
    for path in crash_paths:
        report = _load_report(path, [])
        if not isinstance(report, dict):
            continue
        binaries = report.get("binaries")
        if isinstance(binaries, dict) and isinstance(
                binaries.get("fixture_tool"), str):
            real = os.path.realpath(binaries["fixture_tool"])
            sha = binaries.get("fixture_tool_sha256")
            if not isinstance(sha, str) or not sha:
                # The crash root binaries table is the authority for
                # the fixture identity; a fixture path recorded without
                # its sha256 must never make the matrix comparison
                # vacuous (external review finding: ``fixture_shas``
                # previously skipped the record, so any matrix sha
                # passed the cross-check).
                problems.append(
                    f"crash {path}: fixture_tool {binaries['fixture_tool']!r} "
                    f"is recorded without a fixture_tool_sha256, so the "
                    f"gate cannot verify the fixture identity")
                continue
            if real in fixture_shas and fixture_shas[real] != sha:
                # Two crash reports naming the same fixture path with
                # different hashes cannot both be the battery's fixture
                # (external review finding: the later record silently
                # overwrote the earlier one).
                problems.append(
                    f"crash {path}: fixture_tool sha256 {sha!r} contradicts "
                    f"the earlier identity {fixture_shas[real]!r} for the "
                    f"same fixture path {real!r}")
                continue
            fixture_paths.add(real)
            fixture_shas[real] = sha

    seen_matrices = {}
    matrix_stats = {}
    for path in matrix_paths:
        report = _load_report(path, problems)
        if report is None:
            continue
        matrix, evidence, stats, _ = matrix_evidence(
            path, report, implementation_of, fixture_paths, problems)
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
        # Cross-report fixture identity (external review finding): the
        # matrix report records the v4-fixture tool it used; the path
        # must be the battery's fixture and its sha256 must equal the
        # crash report root identity for the same path.
        fixture = report.get("fixture_tool")
        if isinstance(fixture, dict):
            fixture_path = fixture.get("path")
            fixture_sha = fixture.get("sha256")
            if isinstance(fixture_path, str) and isinstance(
                    fixture_sha, str):
                real = os.path.realpath(fixture_path)
                if real not in fixture_paths:
                    problems.append(
                        f"matrix {path}: fixture_tool {fixture_path!r} "
                        f"does not name the battery fixture "
                        f"({sorted(fixture_paths) or '<none>'})")
                else:
                    recorded = fixture_shas.get(real)
                    if recorded is None:
                        # The fixture path is the battery fixture but no
                        # crash report carried a sha256 for it, so the
                        # matrix sha256 cannot be verified (external
                        # review finding: the previous gate skipped the
                        # comparison when the crash identity was absent).
                        problems.append(
                            f"matrix {path}: fixture_tool sha256 "
                            f"{fixture_sha!r} cannot be verified: no "
                            f"crash report records an identity for "
                            f"{fixture_path!r}")
                    elif recorded != fixture_sha:
                        problems.append(
                            f"matrix {path}: fixture_tool sha256 "
                            f"{fixture_sha!r} contradicts the crash "
                            f"report identity {recorded!r} for "
                            f"{fixture_path!r}")
        # matrix_evidence appended its findings to ``problems``
        # directly and returned the same list; extending again would
        # duplicate every entry.
        for kind, sides in evidence.items():
            bucket = coverage.setdefault(kind,
                                         {"created": set(), "opened": set()})
            bucket["created"].update(sides["created"])
            bucket["opened"].update(sides["opened"])
            kind_sources.setdefault(
                kind, {"matrix": False, "crash": False})["matrix"] = True
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
        evidence, stats, _ = crash_evidence(
            path, report, _crash_path_to_sha(report), implementation_of,
            problems)
        sources.append(
            f"crash {path} ({stats['scenarios']} scenarios, "
            f"{stats['pass_scenarios']} PASS)")
        if stats["pass_scenarios"] > 0:
            crash_pass_seen = True
        # crash_evidence appended its findings to ``problems``
        # directly and returned the same list; extending again would
        # duplicate every entry.
        for kind, sides in evidence.items():
            bucket = coverage.setdefault(kind,
                                         {"created": set(), "opened": set()})
            bucket["created"].update(sides["created"])
            bucket["opened"].update(sides["opened"])
            kind_sources.setdefault(
                kind, {"matrix": False, "crash": False})["crash"] = True

    if not crash_paths:
        problems.append(
            "no crash report path supplied: crash evidence is mandatory")
    elif not crash_pass_seen:
        problems.append(
            "no crash report contributes a PASS scenario: crash evidence "
            "is mandatory")
    unknown = sorted(kind for kind in coverage if kind not in REQUIRED_KINDS)
    if unknown:
        problems.append(f"unknown kinds in PASS evidence: {unknown}")
    for kind in REQUIRED_KINDS:
        bucket = coverage[kind]
        if not {"rust", "go"} <= bucket["created"]:
            problems.append(
                f"kind {kind!r} must be created by both languages: "
                f"created by {sorted(bucket['created'])}")
        if kind in REQUIRED_OPENED_KINDS:
            # These kinds imply a cross-process reader; empty opened
            # coverage is a FAIL, never a vacuous pass.
            if not {"rust", "go"} <= bucket["opened"]:
                problems.append(
                    f"kind {kind!r} must be opened by both languages: "
                    f"opened by {sorted(bucket['opened'])}")
        elif bucket["opened"] and not {"rust", "go"} <= bucket["opened"]:
            problems.append(
                f"kind {kind!r} is opened by services and must be opened "
                f"by both languages: opened by {sorted(bucket['opened'])}")
        if (kind in CRASH_ONLY_KINDS
                and not kind_sources.get(kind, {}).get("crash")):
            problems.append(
                f"kind {kind!r} is crash-only and requires at least one "
                f"crash scenario contributing it (no crash source "
                f"observed)")
    return problems, coverage, sources


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--matrix", action="append", default=[],
                        metavar="PATH", help="one matrix report (repeatable)")
    parser.add_argument("--crash", action="append", default=[],
                        metavar="PATH", help="one crash report (repeatable)")
    parser.add_argument("--self-test", action="store_true",
                        help="run the doctored-report regression suite "
                             "and exit")
    args = parser.parse_args()
    if args.self_test:
        _self_test()
        return 0
    if not args.matrix and not args.crash:
        parser.error("at least one --matrix or --crash report is required")

    problems, coverage, sources = assess(args.matrix, args.crash)
    print("Artifact-kind coverage gate")
    print("Sources: " + "; ".join(sources))
    for kind in REQUIRED_KINDS:
        bucket = coverage[kind]
        if kind in REQUIRED_OPENED_KINDS:
            opened_ok = {"rust", "go"} <= bucket["opened"]
        else:
            opened_ok = (not bucket["opened"]
                         or {"rust", "go"} <= bucket["opened"])
        status = "OK  " if ({"rust", "go"} <= bucket["created"]
                            and opened_ok) else "MISS"
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
            "command": [
                "v4/cli/run.py",
                "--rust", BINARY_PATHS["rust"],
                "--go", BINARY_PATHS["go"],
                "--fixture-tool", CRASH_BINARIES["fixture_tool"],
                "--matrix", matrix,
                "--work-dir", "/tmp/kind-matrix-work",
                "--json-report", "/tmp/kind-matrix-report.json"],
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
        sha_of = {"rust": "1" * 64, "go": "2" * 64}
        scenarios = []

        def scenario(i, p, c, shape):
            """One PASS scenario mirroring the committed crash shapes.

            The operation lists and lineage ordinals are method-capable
            for the kinds the shape observes (CRASH_CREATE_METHODS /
            CRASH_OPEN_METHODS): A observes the publish residue
            (current.publish), B the live sidecar (initialize_live /
            live database.info / reader.open), C the recovery scratch
            (recover), E the export partial output (export writer).
            """
            state = {
                "A": {"class": "absent_after_crash", "exists": False,
                      "reservation_basename": [".reservation.tmp"],
                      "publish_temp_basenames": [".publication.tmp"]},
                "B": {"class": "main_unchanged_sidecar_present",
                      "exists": True},
                "C": {"class": "scratch_residue", "exists": True,
                      "scratch_basenames": ["scratch-1.tmp"],
                      "publish_temp_basenames": [".publication.tmp"]},
                "E": {"class": "export_partial_output",
                      "dest_absent_after_crash": True,
                      "export_temp_basenames": ["out.d.txt.tmp"]},
            }[shape]
            outcome = {
                "A": {"before_resolution": {"code": "invalid_path",
                                            "outcome": "not_started"},
                      "after_resolution": {"database_id": "doctored",
                                           "transaction_id": "1"}},
                "B": {"live": {"database_id": "doctored",
                               "transaction_id": "2"},
                      "immutable": {"code": "wrong_state",
                                    "outcome": "read_only_failure"}},
                "C": {"before_resolution": {"code": "invalid_path",
                                            "outcome": "not_started"}},
                "E": {"before_resolution": {"opened_complete_destination":
                                            True, "reader_closed": True}},
            }[shape]
            producer_ops = {
                "A": ["iprange.v1.current.publish",
                      "iprange.v1.maintenance.list",
                      "iprange.v1.publication.inspect",
                      "iprange.v1.publication.resolve",
                      "iprange.v1.publication.inspect",
                      "iprange.v1.maintenance.list"],
                "B": ["iprange.v1.database.initialize_live",
                      "iprange.v1.database.info",
                      "iprange.v1.database.info",
                      "iprange.v1.maintenance.list",
                      "iprange.v1.database.live_residue.resolve",
                      "iprange.v1.database.info"],
                "C": ["iprange.v1.current.publish",
                      "iprange.v1.recovery.inspect",
                      "iprange.v1.recover",
                      "iprange.v1.maintenance.list",
                      "iprange.v1.maintenance.remove",
                      "iprange.v1.maintenance.list"],
                "E": ["iprange.v1.current.publish",
                      "iprange.v1.export",
                      "iprange.v1.export",
                      "iprange.v1.export"],
            }[shape]
            consumer_ops = {
                "A": ["iprange.v1.reader.open", "iprange.v1.reader.open"],
                "B": ["iprange.v1.reader.open", "iprange.v1.reader.open"],
                "C": ["iprange.v1.reader.open"],
                "E": ["iprange.v1.reader.open", "iprange.v1.reader.close"],
            }[shape]
            main_open_ordinal = {"A": 1, "B": 0, "C": None, "E": 0}[shape]
            kinds = {}
            if shape == "A":
                kinds["publication_reservation"] = {
                    "created_by": ["producer.0"], "opened_by": []}
                kinds["publication_temp"] = {
                    "created_by": ["producer.0"], "opened_by": []}
                kinds["v4_main"] = {
                    "created_by": ["producer.0"],
                    "opened_by": ["consumer.1"]}
            if shape == "B":
                kinds["live_sidecar"] = {
                    "created_by": ["producer.0"],
                    "opened_by": ["producer.5", "consumer.0"]}
                kinds["v4_main"] = {"created_by": [], "opened_by": []}
            if shape == "C":
                kinds["authorized_scratch"] = {
                    "created_by": ["producer.2"], "opened_by": []}
                kinds["publication_temp"] = {
                    "created_by": ["producer.2"], "opened_by": []}
                kinds["v4_main"] = {"created_by": ["producer.0"],
                                    "opened_by": []}
            if shape == "E":
                kinds["adapter_output"] = {
                    "created_by": ["producer.2"],
                    "opened_by": ["producer.2"]}
                kinds["v4_main"] = {
                    "created_by": ["producer.0"],
                    "opened_by": ["consumer.0"]}
            created_ordinals = {
                "A": {"publication_reservation": 0,
                      "publication_temp": 0, "v4_main": 0},
                "B": {"live_sidecar": 0},
                "C": {"authorized_scratch": 2,
                      "publication_temp": 2, "v4_main": 0},
                "E": {"adapter_output": 2, "v4_main": 0},
            }[shape]
            live_reader_opens = (
                {"producer": 5, "consumer": 0} if shape == "B" else {})
            adapter_output_opens = (
                {"producer": 2} if shape == "E" else {})
            reopen_outcome = dict(outcome)
            if main_open_ordinal is not None:
                reopen_outcome["consumer_main_open_ordinal"] = (
                    main_open_ordinal)
            return {
                "scenario": f"S{i}.{p}->{c}", "pass": True,
                "producer": f"{p}:{BINARY_PATHS[p]}",
                "producer_sha256": sha_of[p],
                "consumer": f"{c}:{BINARY_PATHS[c]}",
                "consumer_sha256": sha_of[c],
                "fixture_created_main": shape == "B",
                "assertions": list(assertions),
                "failures": [],
                "destination_state": state,
                "reopen_outcome": reopen_outcome,
                "operations": {"producer": producer_ops,
                               "consumer": consumer_ops},
                "live_reader_opens": live_reader_opens,
                "adapter_output_opens": adapter_output_opens,
                "created_ordinals": created_ordinals,
                "kinds": kinds,
            }

        index = 0
        for p, c in zip(producers, consumers):
            for shape in ("A", "B", "C", "E"):
                scenarios.append(scenario(index, p, c, shape))
                index += 1
        return {"schema": "iprange-cli-crash-report-v1",
                "binaries": dict(CRASH_BINARIES),
                "command": [
                    "v4/cli/crash_harness.py",
                    "--producer", BINARY_PATHS["rust"],
                    "--consumer", BINARY_PATHS["go"],
                    "--fixture-tool", CRASH_BINARIES["fixture_tool"],
                    "--work-dir", "/tmp/kind-crash-work",
                    "--json-report", "/tmp/kind-crash-report.json"],
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
        kinds created by this matrix's actors and opened by its
        consumer wherever the kind has a cross-process open contract;
        the crash battery supplies the three crash-only kinds.  Every
        ref names a method that is create/open-capable for the kind
        and actor (MATRIX_CREATE_METHODS / MATRIX_OPEN_METHODS), and
        the case records the executed actor implementations,
        executed-step counts, SHA-256 values and the argv realpath of
        the binary that served each role, anchored in the report's
        binaries block, so language attribution never depends on the
        top-level label."""
        ledger = {}
        for i, kind in enumerate([
                "v4_main", "live_sidecar", "adapter_output",
                "metadata_delivery"]):
            created = {
                "v4_main": "producer.iprange.v1.database.create",
                "live_sidecar": "producer.iprange.v1.database.create",
                "adapter_output": "producer.iprange.v1.export",
                "metadata_delivery":
                    "consumer.iprange.v1.database.metadata.get",
            }[kind]
            # Only kinds with a v1 cross-process open contract record
            # openers.  adapter_output has NO matrix-side opener record
            # (the only adapter-output opener is the producer export
            # writer the crash battery observes) and metadata_delivery
            # has no open contract, so both truthfully record none.
            opened = {
                "v4_main": ["consumer.iprange.v1.reader.open"],
                "live_sidecar": ["consumer.iprange.v1.reader.open"],
                "adapter_output": [],
                "metadata_delivery": [],
            }[kind]
            ledger[f"k{i}.bin"] = {
                "kind": kind,
                "created_by": [created],
                "opened_by": opened,
            }
        expected = ACTOR_LANGUAGES[matrix]
        actor_sha = {"rust": "1" * 64, "go": "2" * 64}
        report = matrix_report(matrix, [{
            "name": "doctored", "matrix": matrix, "status": "PASS",
            "actors": {
                "producer": {
                    "sha256": actor_sha[expected["producer"]],
                    "implementation": expected["producer"],
                    "argv": BINARY_PATHS[expected["producer"]],
                    "steps": 1,
                    "operations": ["iprange.v1.database.create",
                                   "iprange.v1.export"],
                },
                "consumer": {
                    "sha256": actor_sha[expected["consumer"]],
                    "implementation": expected["consumer"],
                    "argv": BINARY_PATHS[expected["consumer"]],
                    "steps": 1,
                    "operations": ["iprange.v1.reader.open",
                                   "iprange.v1.database.metadata.get"],
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
        # Open coverage is contract-based: kinds with a cross-process
        # open contract must be opened by both languages; kinds
        # without one (publication temporaries, scratch) truthfully
        # record no openers and the gate requires a non-empty opener
        # set only for the required-opened kinds.
        for kind in REQUIRED_OPENED_KINDS:
            assert {"rust", "go"} <= coverage[kind]["opened"], (
                f"required-opened kind {kind} lacks both-language "
                f"open coverage: {coverage[kind]}")

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
        import copy as _copy
        crash_no_v4 = _copy.deepcopy(crash_report(
            ["rust", "go"], ["go", "rust"]))
        for scenario in crash_no_v4["scenarios"]:
            scenario["kinds"]["v4_main"]["created_by"] = []
        crash_no_v4_path = os.path.join(work, "one-lang-crash.json")
        assign(crash_no_v4_path, crash_no_v4)
        problems, _c, _s = assess(
            [rust_created_path, green["go"], r2g_created_path,
             green["go_to_rust"]], [crash_no_v4_path])
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
        import copy as _copy2
        crash_no_v4_open = _copy2.deepcopy(crash_report(
            ["rust", "go"], ["go", "rust"]))
        for scenario in crash_no_v4_open["scenarios"]:
            scenario["kinds"]["v4_main"]["opened_by"] = []
        crash_no_v4_open_path = os.path.join(work, "no-open-crash.json")
        assign(crash_no_v4_open_path, crash_no_v4_open)
        problems, _c, _s = assess(
            [rust_opened_path, green["go"], green["rust_to_go"],
             g2r_opened_path], [crash_no_v4_open_path])
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
        # The shared BINARIES dict is aliased by every green report;
        # the clone's binary block must be a private copy so the
        # mutation cannot poison the other reports of the battery.
        forge["binaries"] = _copy.deepcopy(BINARIES)
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
        dupe = dict(crash_report(["rust"], ["go"]))
        dupe["scenarios"] = []
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

        # 18. Per-case matrix lie: a PASS case whose ``matrix`` field
        #     contradicts its report is not attributable to this
        #     matrix and fails the gate.
        lie_matrix = green_report("rust")
        lie_matrix["cases"][0]["matrix"] = "go"
        lie_matrix_path = os.path.join(work, "lie-matrix.json")
        assign(lie_matrix_path, lie_matrix)
        problems, _c, _s = assess(
            [lie_matrix_path] + four[1:], [crash_path])
        assert problems and any("records case matrix" in p
                                and "does not match" in p
                                for p in problems), (
            f"per-case matrix lie did not fail the gate: {problems}")

        # 19. Command lie: a matrix report whose recorded command
        #     passes a different --matrix than the report matrix
        #     fails the gate.
        lie_command = green_report("rust")
        for index, token in enumerate(lie_command["command"]):
            if token == "--matrix":
                lie_command["command"][index + 1] = "go"
                break
        lie_command_path = os.path.join(work, "lie-command.json")
        assign(lie_command_path, lie_command)
        problems, _c, _s = assess(
            [lie_command_path] + four[1:], [crash_path])
        assert problems and any("report command --matrix" in p
                                and "does not match" in p
                                for p in problems), (
            f"command matrix lie did not fail the gate: {problems}")

        # 20. Crash root-table membership: a PASS scenario naming a
        #     producer path absent from the report-root binaries
        #     table fails the gate.
        absent_path = crash_report(["rust", "go"], ["go", "rust"])
        absent_path["scenarios"][0]["producer"] = (
            "rust:" + BINARY_PATHS["rust"] + ".absent")
        absent_path_path = os.path.join(work, "crash-absent-path.json")
        assign(absent_path_path, absent_path)
        problems, _c, _s = assess(four, [absent_path_path])
        assert problems and any("absent from the report root binaries "
                                "table" in p for p in problems), (
            f"unlisted crash binary path did not fail the gate: "
            f"{problems}")

        # 21. Crash-only kinds fabricated through matrix ledgers with
        #     no crash report at all must fail: crash evidence is
        #     mandatory and these kinds need a crash source.
        fabricated_rust = green_report("rust")
        fabricated_go = green_report("go")
        for report in (fabricated_rust, fabricated_go):
            for i, kind in enumerate(CRASH_ONLY_KINDS):
                report["cases"][0]["file_kinds"][f"crash{i}.bin"] = {
                    "kind": kind,
                    "created_by": ["producer.iprange.v1.selftest"],
                    "opened_by": ["consumer.iprange.v1.selftest"]}
        fabricated_rust_path = os.path.join(work, "fabricated-rust.json")
        assign(fabricated_rust_path, fabricated_rust)
        fabricated_go_path = os.path.join(work, "fabricated-go.json")
        assign(fabricated_go_path, fabricated_go)
        problems, _c, _s = assess(
            [fabricated_rust_path, fabricated_go_path,
             green["rust_to_go"], green["go_to_rust"]], [])
        assert problems and any("no crash report path supplied" in p
                                for p in problems) and any(
            "requires at least one crash scenario contributing" in p
            for p in problems), (
            f"matrix-fabricated crash kinds without crash evidence did "
            f"not fail the gate: {problems}")

        # 21b. Matrix-side fabricated cross-process open: an opened_by
        #      ref on a kind without a v1 open contract (a publication
        #      temporary here) is a fabricated cross-process open and
        #      fails the gate, mirroring the crash-side open-contract
        #      check.
        fab_open = green_report("go")
        fab_open["cases"][0]["file_kinds"]["fab.bin"] = {
            "kind": "publication_temp",
            "created_by": ["producer.iprange.v1.export"],
            "opened_by": ["consumer.iprange.v1.reader.open"]}
        fab_open_path = os.path.join(work, "fabricated-open.json")
        assign(fab_open_path, fab_open)
        problems, _c, _s = assess(
            [green["rust"], fab_open_path, green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any(
            "records a cross-process open ref" in p
            and "no v1 open contract opens this kind" in p
            for p in problems), (
            f"matrix-fabricated cross-process open did not fail the "
            f"gate: {problems}")

        # 22. Zero-step credit: a PASS case that executes zero steps
        #     as producer but still credits creation to the producer
        #     fails, and the producer credit is dropped (the consumer
        #     opening credit, with steps > 0, still counts).
        idle_producer = green_report("rust")
        idle_producer["cases"][0]["actors"]["producer"]["steps"] = 0
        idle_producer_path = os.path.join(work, "zero-producer.json")
        assign(idle_producer_path, idle_producer)
        problems, _c, _s = assess(
            [idle_producer_path] + four[1:], [crash_path])
        assert problems and any("credits creator actor" in p
                                and "zero executed steps" in p
                                for p in problems), (
            f"zero-step producer credit did not fail the gate: "
            f"{problems}")

        # 23. Legacy flat crash kinds: restoring the old flat kind
        #     list (which carried no per-kind actor lineage and let
        #     the gate credit every kind to the scenario consumer)
        #     fails the gate.
        flat = crash_report(["rust", "go"], ["go", "rust"])
        flat["scenarios"][0]["kinds"] = [
            "publication_temp", "publication_reservation",
            "authorized_scratch"]
        flat_path = os.path.join(work, "crash-flat-kinds.json")
        assign(flat_path, flat)
        problems, _c, _s = assess(four, [flat_path])
        assert problems and any("legacy flat kinds list carries no actor "
                                "lineage" in p for p in problems), (
            f"legacy flat crash kinds did not fail the gate: {problems}")

        # 24. Malformed per-kind lineage: unknown actor prefixes,
        #     empty created_by, missing lineage keys, non-object
        #     lineage, and non-object kinds all fail the gate.
        malformed = []
        broken_missing = crash_report(["rust", "go"], ["go", "rust"])
        del broken_missing["scenarios"][0]["kinds"][
            "publication_reservation"]["opened_by"]
        malformed.append(("missing-opened-by",
                          "lacks created_by/opened_by keys",
                          broken_missing))
        broken_unknown = crash_report(["rust", "go"], ["go", "rust"])
        broken_unknown["scenarios"][0]["kinds"][
            "publication_reservation"]["created_by"] = ["mystery.0"]
        malformed.append(("unknown-prefix",
                          "unknown or malformed actor prefix",
                          broken_unknown))
        broken_empty = crash_report(["rust", "go"], ["go", "rust"])
        broken_empty["scenarios"][0]["kinds"][
            "publication_reservation"]["created_by"] = []
        malformed.append(("empty-created-by",
                          "records empty created_by lineage",
                          broken_empty))
        broken_lineage_type = crash_report(["rust", "go"], ["go", "rust"])
        broken_lineage_type["scenarios"][0]["kinds"][
            "publication_reservation"] = ["producer.0"]
        malformed.append(("lineage-not-object",
                          "lineage that is not an object",
                          broken_lineage_type))
        broken_kinds_type = crash_report(["rust", "go"], ["go", "rust"])
        broken_kinds_type["scenarios"][0]["kinds"] = "junk"
        malformed.append(("kinds-not-object",
                          "neither a lineage object nor a list",
                          broken_kinds_type))
        for label, needle, report in malformed:
            malformed_path = os.path.join(work, f"crash-{label}.json")
            assign(malformed_path, report)
            problems, _c, _s = assess(four, [malformed_path])
            assert problems and any(needle in p for p in problems), (
                f"malformed lineage {label!r} did not fail the gate: "
                f"{problems}")

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

        # 25. Mixed matrices execute both actors: a PASS case of a
        #     mixed matrix with consumer steps=0 (and its consumer
        #     credits removed) fails even though the aggregate step
        #     sum would accept it.
        mixed_idle = green_report("rust_to_go")
        mixed_idle["cases"][0]["actors"]["consumer"]["steps"] = 0
        for facts in mixed_idle["cases"][0]["file_kinds"].values():
            for field in ("created_by", "opened_by"):
                facts[field] = [ref for ref in facts[field]
                                if not ref.startswith("consumer.")]
        mixed_idle_path = os.path.join(work, "mixed-idle-consumer.json")
        assign(mixed_idle_path, mixed_idle)
        problems, _c, _s = assess(
            [green["rust"], green["go"], mixed_idle_path,
             green["go_to_rust"]], [crash_path])
        assert problems and any("zero executed consumer steps" in p
                                for p in problems), (
            f"mixed zero-consumer-steps did not fail the gate: {problems}")

        # 26. A PASS case whose actor records no executed-operation
        #     record fails the gate even when the actor executed
        #     positive steps.
        no_ops = green_report("go")
        del no_ops["cases"][0]["actors"]["consumer"]["operations"]
        no_ops_path = os.path.join(work, "no-operations.json")
        assign(no_ops_path, no_ops)
        problems, _c, _s = assess(
            [green["rust"], no_ops_path, green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("no executed-operation record" in p
                                for p in problems), (
            f"missing operations record did not fail the gate: {problems}")

        # 27. Lineage refs must name recorded actors and executed
        #     operations: an unknown-actor ref and a known actor with
        #     an unrecorded operation both fail the gate.
        ghost_actor = green_report("rust")
        ghost_actor["cases"][0]["file_kinds"]["k0.bin"][
            "created_by"].append("ghost.no-such-operation")
        ghost_actor_path = os.path.join(work, "ghost-actor.json")
        assign(ghost_actor_path, ghost_actor)
        problems, _c, _s = assess(
            [ghost_actor_path] + four[1:], [crash_path])
        assert problems and any("unknown actor" in p
                                for p in problems), (
            f"ghost actor ref did not fail the gate: {problems}")
        ghost_operation = green_report("rust")
        ghost_operation["cases"][0]["file_kinds"]["k0.bin"][
            "created_by"].append("producer.no-such-executed-operation")
        ghost_operation_path = os.path.join(work, "ghost-operation.json")
        assign(ghost_operation_path, ghost_operation)
        problems, _c, _s = assess(
            [ghost_operation_path] + four[1:], [crash_path])
        assert problems and any("not recorded in actor" in p
                                for p in problems), (
            f"ghost operation ref did not fail the gate: {problems}")

        # 28. Duplicate identity options are rejected: a trailing
        #     --matrix override and a trailing --producer override
        #     both fail the gate as ambiguous commands.
        dupe_matrix = green_report("go")
        dupe_matrix["command"].extend(["--matrix", "rust"])
        dupe_matrix_path = os.path.join(work, "dupe-matrix.json")
        assign(dupe_matrix_path, dupe_matrix)
        problems, _c, _s = assess(
            [green["rust"], dupe_matrix_path, green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("more than once" in p for p in problems), (
            f"duplicate --matrix did not fail the gate: {problems}")
        dupe_crash = crash_report(["rust", "go"], ["go", "rust"])
        dupe_crash["command"].extend(["--producer", "/bin/false"])
        dupe_crash_path = os.path.join(work, "dupe-producer.json")
        assign(dupe_crash_path, dupe_crash)
        problems, _c, _s = assess(four, [dupe_crash_path])
        assert problems and any("more than once" in p for p in problems), (
            f"duplicate --producer did not fail the gate: {problems}")

        # 29. Executable arguments must name the report binary
        #     records: a --go /bin/false and a --producer /bin/false
        #     both fail the gate.
        false_matrix = green_report("rust_to_go")
        for index, token in enumerate(false_matrix["command"]):
            if token == "--go":
                false_matrix["command"][index + 1] = "/bin/false"
                break
        false_matrix_path = os.path.join(work, "false-matrix-binary.json")
        assign(false_matrix_path, false_matrix)
        problems, _c, _s = assess(
            [green["rust"], green["go"], false_matrix_path,
             green["go_to_rust"]], [crash_path])
        assert problems and any("does not name any binary record" in p
                                for p in problems), (
            f"/bin/false --go did not fail the gate: {problems}")
        false_crash = crash_report(["rust", "go"], ["go", "rust"])
        for index, token in enumerate(false_crash["command"]):
            if token == "--producer":
                false_crash["command"][index + 1] = "/bin/false"
                break
        false_crash_path = os.path.join(work, "false-crash-producer.json")
        assign(false_crash_path, false_crash)
        problems, _c, _s = assess(four, [false_crash_path])
        assert problems and any("does not name the report root binaries "
                                "table path" in p for p in problems), (
            f"/bin/false --producer did not fail the gate: {problems}")

        # 30. A PASS crash scenario must keep its artifact evidence:
        #     destination_state={} and reopen_outcome=None are both
        #     report defects and fail the gate.
        contradictory = crash_report(["rust", "go"], ["go", "rust"])
        for scenario in contradictory["scenarios"]:
            scenario["destination_state"] = {}
            scenario["reopen_outcome"] = None
        contradictory_path = os.path.join(work, "contradictory-state.json")
        assign(contradictory_path, contradictory)
        problems, _c, _s = assess(four, [contradictory_path])
        assert problems and any("destination_state" in p
                                for p in problems) and any(
            "reopen_outcome" in p for p in problems), (
            f"contradictory scenario state did not fail the gate: "
            f"{problems}")

        # 31. Required-opened kinds: live_sidecar and adapter_output
        #     imply a cross-process reader, so empty opened coverage
        #     fails instead of vacating the requirement.
        unopened = {}
        for m in REQUIRED_MATRICES:
            unopened_report = green_report(m)
            for facts in unopened_report["cases"][0]["file_kinds"].values():
                if facts["kind"] in REQUIRED_OPENED_KINDS:
                    facts["opened_by"] = []
            unopened[m] = os.path.join(work, f"unopened-{m}.json")
            assign(unopened[m], unopened_report)
        unopened_crash = _copy.deepcopy(crash_report(
            ["rust", "go"], ["go", "rust"]))
        for scenario in unopened_crash["scenarios"]:
            for kind in REQUIRED_OPENED_KINDS:
                if kind in scenario["kinds"]:
                    scenario["kinds"][kind]["opened_by"] = []
        unopened_crash_path = os.path.join(work, "unopened-crash.json")
        assign(unopened_crash_path, unopened_crash)
        problems, _c, _s = assess(
            [unopened[m] for m in REQUIRED_MATRICES],
            [unopened_crash_path])
        assert problems and any("must be opened by both languages: "
                                "opened by []" in p for p in problems), (
            f"empty opened coverage for required-opened kind did not "
            f"fail the gate: {problems}")

        # 32. Crash lineage ordinals must index the recorded executed
        #     operations: an ordinal past the end of the actor's list
        #     fails the gate.
        beyond_ops = crash_report(["rust", "go"], ["go", "rust"])
        beyond_ops["scenarios"][0]["kinds"]["publication_reservation"][
            "created_by"] = ["producer.9"]
        beyond_ops_path = os.path.join(work, "crash-ordinal.json")
        assign(beyond_ops_path, beyond_ops)
        problems, _c, _s = assess(four, [beyond_ops_path])
        assert problems and any("beyond the recorded executed operations"
                                in p for p in problems), (
            f"crash ordinal beyond operations did not fail the gate: "
            f"{problems}")

        # 33. Fixture-created mains: a PASS scenario that truthfully
        #     records fixture_created_main may leave v4_main with an
        #     empty created_by (B/D mains come from the external
        #     v4-fixture tool); without the flag the empty creator
        #     still fails the gate.
        fixture_scenario = crash_report(["rust", "go"], ["go", "rust"])
        scenario = fixture_scenario["scenarios"][0]
        scenario["fixture_created_main"] = True
        scenario["kinds"]["v4_main"] = {
            "created_by": [], "opened_by": ["consumer.0"]}
        fixture_path = os.path.join(work, "crash-fixture-main.json")
        assign(fixture_path, fixture_scenario)
        problems, _c, _s = assess(four, [fixture_path])
        assert not any("records empty created_by lineage" in p
                       for p in problems), (
            f"fixture-created v4_main empty creator failed the gate: "
            f"{problems}")
        unmarked = crash_report(["rust", "go"], ["go", "rust"])
        unmarked["scenarios"][0]["kinds"]["v4_main"] = {
            "created_by": [], "opened_by": ["consumer.0"]}
        unmarked_path = os.path.join(work, "crash-unmarked-main.json")
        assign(unmarked_path, unmarked)
        problems, _c, _s = assess(four, [unmarked_path])
        assert problems and any("records empty created_by lineage" in p
                                for p in problems), (
            f"unmarked empty v4_main creator did not fail the gate: "
            f"{problems}")

        # 34. P2-4/P2-5 regression controls: every mutation that the
        #     pre-fix gate accepted on GENUINE evidence must now fail,
        #     and the genuine committed evidence must keep passing.
        evidence_dir = os.path.join(
            os.path.dirname(os.path.abspath(__file__)), "evidence")
        genuine_paths = [os.path.join(evidence_dir, f"matrix-{m}.json")
                         for m in REQUIRED_MATRICES]
        genuine_crash = os.path.join(evidence_dir, "crash.json")

        def load_genuine():
            matrices = []
            for path in genuine_paths:
                with open(path, encoding="utf-8") as stream:
                    matrices.append(_json.load(stream))
            with open(genuine_crash, encoding="utf-8") as stream:
                crash = _json.load(stream)
            return matrices, crash

        problems, _c, _s = assess(genuine_paths, [genuine_crash])
        assert not problems, (
            f"genuine evidence failed the gate: {problems}")

        def genuine_mutation_fails(label, mutator):
            matrices, crash = load_genuine()
            mutator(matrices, crash)
            paths = []
            for index, report in enumerate(matrices):
                path = os.path.join(
                    work, f"genuine-{label}-{index}.json")
                assign(path, report)
                paths.append(path)
            crash_mutated = os.path.join(
                work, f"genuine-{label}-crash.json")
            assign(crash_mutated, crash)
            problems, _c, _s = assess(paths, [crash_mutated])
            assert problems, (
                f"mutation {label!r} did not fail the gate: {problems}")
            return problems

        # Abbreviated overrides: the real runners select these values
        # with argparse abbreviations enabled, so the mutated command
        # executes a different effective identity than the gate's
        # literal last-value scan could see.
        genuine_mutation_fails(
            "abbrev-matrix",
            lambda matrices, crash: matrices[0]["command"].extend(
                ["--mat", "go"]))
        genuine_mutation_fails(
            "abbrev-go",
            lambda matrices, crash: matrices[2]["command"].extend(
                ["--g", "/bin/false"]))
        genuine_mutation_fails(
            "abbrev-producer",
            lambda matrices, crash: crash["command"].extend(
                ["--prod", "/bin/false"]))
        genuine_mutation_fails(
            "abbrev-fixture",
            lambda matrices, crash: matrices[0]["command"].extend(
                ["--fixture", "/bin/false"]))

        # Fixture-tool substitution: replacing the matrix command's
        # recorded fixture path changes the effective fixture binary.
        def swap_matrix_fixture(matrices, crash):
            command = matrices[0]["command"]
            command[command.index("--fixture-tool") + 1] = "/bin/false"
        genuine_mutation_fails("fixture-substitution",
                               swap_matrix_fixture)

        # Empty consumer operation lists on actors with executed steps:
        # the consumer refs were stripped to hide the emptiness.
        def empty_consumer_ops(matrices, crash):
            for report in matrices[2:]:
                for case in report["cases"]:
                    if case.get("status") != "PASS":
                        continue
                    case["actors"]["consumer"]["operations"] = []
                    for facts in case.get("file_kinds", {}).values():
                        for field in ("created_by", "opened_by"):
                            facts[field] = [
                                ref for ref in facts[field]
                                if not ref.startswith("consumer.")]
        genuine_mutation_fails("empty-consumer-ops", empty_consumer_ops)

        # Invented legacy marker: every matrix ref rewritten to
        # actor.legacy, which the pre-fix gate exempted from the
        # recorded-operations check.
        def invented_legacy(matrices, crash):
            for report in matrices:
                for case in report["cases"]:
                    if case.get("status") != "PASS":
                        continue
                    for facts in case.get("file_kinds", {}).values():
                        for field in ("created_by", "opened_by"):
                            facts[field] = [
                                ref.split(".", 1)[0] + ".legacy"
                                for ref in facts[field]]
        genuine_mutation_fails("invented-legacy", invented_legacy)

        # False main-open operation: the A2 v4_main open ref indexes
        # reader.close, an in-range ordinal that is not an open-capable
        # method of the kind.
        def false_main_open(matrices, crash):
            for scenario in crash["scenarios"]:
                if scenario["scenario"].startswith("A2."):
                    scenario["kinds"]["v4_main"]["opened_by"] = [
                        "consumer.1"]
        genuine_mutation_fails("false-main-open-operation",
                               false_main_open)

        # 35. Matrix argv anchor (wave-10, unconditional since the
        #     second role round): every PASS case must carry
        #     actors.<role>.argv whose realpath names the report
        #     binary that served the role.  A battery stripped of all
        #     argv (the pre-regen escape hatch), a PASS case without
        #     argv, and an argv naming a different binary all fail.
        preargv = {}
        for m in REQUIRED_MATRICES:
            preargv_report = green_report(m)
            for actor in preargv_report["cases"][0]["actors"].values():
                del actor["argv"]
            preargv[m] = os.path.join(work, f"preargv-{m}.json")
            assign(preargv[m], preargv_report)
        problems, _c, _s = assess(
            [preargv[m] for m in REQUIRED_MATRICES], [crash_path])
        assert problems and any("records no argv" in p
                                for p in problems), (
            f"fully argv-stripped battery did not fail the "
            f"unconditional argv rule: {problems}")

        no_argv = green_report("go")
        del no_argv["cases"][0]["actors"]["producer"]["argv"]
        no_argv_path = os.path.join(work, "no-argv.json")
        assign(no_argv_path, no_argv)
        problems, _c, _s = assess(
            [green["rust"], no_argv_path, green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("records no argv" in p for p in problems), (
            f"argv-less PASS case did not fail the gate: {problems}")

        # 35b. Binary-record path anchor (second role round): the
        #      report binary record matched by the actor sha256 must
        #      carry a path; dropping every path breaks the argv
        #      resolution and fails the gate.
        pathless = green_report("go")
        pathless["binaries"] = _copy.deepcopy(BINARIES)
        for record in pathless["binaries"].values():
            record.pop("path", None)
        pathless_path = os.path.join(work, "pathless-binaries.json")
        assign(pathless_path, pathless)
        problems, _c, _s = assess(
            [green["rust"], pathless_path, green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("carries no path" in p for p in problems), (
            f"binary record without a path did not fail the gate: "
            f"{problems}")

        # 35c. Absolute-argv anchor (second role round): a relative
        #      argv cannot anchor the executed binary; the gate fails
        #      the PASS case instead of resolving it against cwd.
        relative_argv = green_report("rust")
        relative_argv["cases"][0]["actors"]["producer"]["argv"] = (
            "rust-iprange")
        relative_argv_path = os.path.join(work, "relative-argv.json")
        assign(relative_argv_path, relative_argv)
        problems, _c, _s = assess(
            [relative_argv_path, green["go"], green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("not an absolute path" in p
                                for p in problems), (
            f"relative actor argv did not fail the gate: {problems}")

        bad_argv = green_report("rust")
        bad_argv["cases"][0]["actors"]["producer"]["argv"] = BINARY_PATHS["go"]
        bad_argv_path = os.path.join(work, "bad-argv.json")
        assign(bad_argv_path, bad_argv)
        problems, _c, _s = assess(
            [bad_argv_path, green["go"], green["rust_to_go"],
             green["go_to_rust"]], [crash_path])
        assert problems and any("argv" in p
                                and "does not resolve" in p
                                for p in problems), (
            f"argv naming a different binary did not fail the gate: "
            f"{problems}")

        # 36. Command-path realpath symmetry (wave-10): recorded
        #     commands may name a symlink to the recorded binary; both
        #     the crash command and the matrix command resolve it to
        #     the real binary path (realpath on both sides), exactly
        #     like the matrix binding.  A symlink to another role's
        #     binary still fails.
        rust_link = os.path.join(work, "rust-iprange-link")
        go_link = os.path.join(work, "go-iprange-link")
        os.symlink(BINARY_PATHS["rust"], rust_link)
        os.symlink(BINARY_PATHS["go"], go_link)
        link_crash = crash_report(["rust", "go"], ["go", "rust"])
        link_crash["command"][
            link_crash["command"].index("--producer") + 1] = rust_link
        link_crash_path = os.path.join(work, "crash-symlink.json")
        assign(link_crash_path, link_crash)
        problems, _c, _s = assess(four, [link_crash_path])
        assert not problems, (
            f"crash command symlink to the recorded binary failed the "
            f"gate: {problems}")
        link_matrix = green_report("rust_to_go")
        link_matrix["command"][
            link_matrix["command"].index("--go") + 1] = go_link
        link_matrix_path = os.path.join(work, "matrix-symlink.json")
        assign(link_matrix_path, link_matrix)
        problems, _c, _s = assess(
            [green["rust"], green["go"], link_matrix_path,
             green["go_to_rust"]], [crash_path])
        assert not problems, (
            f"matrix command symlink to the recorded binary failed the "
            f"gate: {problems}")
        wrong_link_crash = crash_report(["rust", "go"], ["go", "rust"])
        wrong_link_crash["command"][
            wrong_link_crash["command"].index("--consumer") + 1] = rust_link
        wrong_link_path = os.path.join(work, "crash-wrong-symlink.json")
        assign(wrong_link_path, wrong_link_crash)
        problems, _c, _s = assess(four, [wrong_link_path])
        assert problems and any("does not name the report root binaries "
                                "table path" in p for p in problems), (
            f"crash command symlink to another role's binary did not "
            f"fail the gate: {problems}")
        false_link = os.path.join(work, "false-link")
        os.symlink("/bin/false", false_link)
        false_crash = crash_report(["rust", "go"], ["go", "rust"])
        false_crash["command"][
            false_crash["command"].index("--producer") + 1] = false_link
        false_crash_path = os.path.join(work, "crash-false-symlink.json")
        assign(false_crash_path, false_crash)
        problems, _c, _s = assess(four, [false_crash_path])
        assert problems and any("does not name the report root binaries "
                                "table path" in p for p in problems), (
            f"crash command symlink to /bin/false did not fail the gate: "
            f"{problems}")

        # 37. The verified crash-side forgery classes (wave-10): on
        #     the GENUINE evidence, crediting current.publish with the
        #     recovery scratch creator (scenario C) and
        #     maintenance.list with a live sidecar open (scenario B)
        #     is fabricated lineage and fails the gate.
        def forged_create_credit(matrices, crash):
            for scenario in crash["scenarios"]:
                if scenario["scenario"].startswith("C."):
                    scenario["kinds"]["authorized_scratch"][
                        "created_by"] = ["producer.0"]
        genuine_mutation_fails("fabricated-create-credit",
                               forged_create_credit)

        def forged_live_sidecar_open(matrices, crash):
            for scenario in crash["scenarios"]:
                if scenario["scenario"].startswith("B."):
                    scenario["kinds"]["live_sidecar"][
                        "opened_by"] = ["producer.3"]
        genuine_mutation_fails("fabricated-sidecar-open",
                               forged_live_sidecar_open)

        # 38. Matrix-side capability enforcement: a RECORDED consumer
        #     method that is not open-capable for live_sidecar (the
        #     workflow.publisher consumer records maintenance.list but
        #     the live_sidecar opener contract never credits it) is a
        #     fabricated matrix open on GENUINE evidence.
        def forged_matrix_sidecar_open(matrices, crash):
            for report in matrices:
                for case in report["cases"]:
                    if case.get("status") != "PASS":
                        continue
                    if case.get("name") != "workflow.publisher":
                        continue
                    case["file_kinds"]["wf-fake.bin"] = {
                        "kind": "live_sidecar",
                        "created_by": ["producer.iprange.v1.database.create"],
                        "opened_by": [
                            "consumer.iprange.v1.maintenance.list"]}
        genuine_mutation_fails("matrix-sidecar-open-capability",
                               forged_matrix_sidecar_open)

        # 39. Crash lineage ordinal anatomy (external review finding):
        #     a created/opened ref must name the EXACT recorded
        #     event, not merely an in-range capable ordinal.  The
        #     pre-fix gate accepted a ref to a failed earlier
        #     operation, a ref that contradicted the recorded open
        #     ordinal, a ref that contradicted created_ordinals, and a
        #     report with created_ordinals removed outright; on the
        #     GENUINE evidence every one of these must now fail.
        def forged_main_open_ordinal(matrices, crash):
            for scenario in crash["scenarios"]:
                if scenario["scenario"].startswith("A1."):
                    scenario["kinds"]["v4_main"]["opened_by"] = [
                        "consumer.0"]
        genuine_mutation_fails("failed-main-open-ordinal",
                               forged_main_open_ordinal)

        def forged_sidecar_open_ordinal(matrices, crash):
            for scenario in crash["scenarios"]:
                if scenario["scenario"].startswith("B."):
                    scenario["kinds"]["live_sidecar"]["opened_by"] = [
                        "producer.1", "consumer.0"]
        genuine_mutation_fails("failed-sidecar-open-ordinal",
                               forged_sidecar_open_ordinal)

        def forged_creation_ordinal(matrices, crash):
            for scenario in crash["scenarios"]:
                if scenario["scenario"].startswith("A2."):
                    scenario["kinds"]["publication_reservation"][
                        "created_by"] = ["producer.0"]
        genuine_mutation_fails("wrong-creation-ordinal",
                               forged_creation_ordinal)

        def missing_creation_ordinals(matrices, crash):
            for scenario in crash["scenarios"]:
                scenario.pop("created_ordinals", None)
        genuine_mutation_fails("missing-creation-ordinals",
                               missing_creation_ordinals)

        # 40. Command/executable join (external review finding): the
        #     actor that served a role must be the exact binary the
        #     recorded command selected for its language.  A second
        #     Go record whose path/sha the case actors adopt while the
        #     command still claims the real Go binary contradicts the
        #     record and fails the gate.
        def alternate_actor_binary(matrices, crash):
            report = matrices[2]  # go_to_rust
            stale = _copy.deepcopy(report["binaries"]["go"])
            stale["path"] = "/tmp/stale-go-iprange"
            stale["sha256"] = "f" * 64
            report["binaries"]["stale_go"] = stale
            for case in report["cases"]:
                if case.get("status") != "PASS":
                    continue
                case["actors"]["consumer"]["argv"] = stale["path"]
                case["actors"]["consumer"]["sha256"] = stale["sha256"]
        genuine_mutation_fails("alternate-actor-binary",
                               alternate_actor_binary)

        # 41. Fixture identity cross-report (external review
        #     finding): a matrix report that carries a fixture_tool
        #     record must agree with the crash report's recorded
        #     identity for the same path; a contradictory hash is an
        #     internal inconsistency, not a legitimate report.
        def fixture_hash_contradiction(matrices, crash):
            matrices[2]["fixture_tool"]["sha256"] = "f" * 64
        genuine_mutation_fails("fixture-hash-contradiction",
                               fixture_hash_contradiction)

        # 42. Positive control for the explicit-actor contract
        #     (external review finding): capability is a property of
        #     the method, not of the actor who executes it.  The
        #     role-inverted database.metadata workflow (the consumer
        #     creates the database and replaces metadata, the producer
        #     reads) runs with the real products in both directions;
        #     the gate must accept the recorded ledger instead of
        #     reporting per-actor capability errors.
        def actor_swapped_case():
            matrices, crash = load_genuine()
            report = matrices[3]  # go_to_rust
            producer = report["binaries"]["go"]
            consumer = report["binaries"]["rust"]
            setup = {
                "name": "database.metadata-role-inverted",
                "matrix": "go->rust",
                "status": "PASS",
                "actors": {
                    "producer": {
                        "sha256": producer["sha256"],
                        "implementation": "go",
                        "argv": producer["path"],
                        "steps": 1,
                        "operations": [
                            "iprange.v1.database.metadata.get"],
                    },
                    "consumer": {
                        "sha256": consumer["sha256"],
                        "implementation": "rust",
                        "argv": consumer["path"],
                        "steps": 2,
                        "operations": [
                            "iprange.v1.database.create",
                            "iprange.v1.database.metadata.replace"],
                    },
                },
                "file_kinds": {
                    "swapped-v4.bin": {
                        "kind": "v4_main",
                        "created_by": [
                            "consumer.iprange.v1.database.create"],
                        "opened_by": [
                            "consumer.iprange.v1.database.metadata.replace",
                            "producer.iprange.v1.database.metadata.get"],
                    },
                    "swapped-sidecar.bin": {
                        "kind": "live_sidecar",
                        "created_by": [
                            "consumer.iprange.v1.database.create"],
                        "opened_by": [
                            "producer.iprange.v1.database.metadata.get"],
                    },
                },
            }
            report["cases"].append(setup)
            paths = []
            for index, mutated in enumerate(matrices):
                path = os.path.join(work, f"actor-swap-{index}.json")
                assign(path, mutated)
                paths.append(path)
            crash_swapped = os.path.join(work, "actor-swap-crash.json")
            assign(crash_swapped, crash)
            problems, _c, _s = assess(paths, [crash_swapped])
            assert not problems, (
                f"actor-swapped database.metadata ledger rejected: "
                f"{problems}")
        actor_swapped_case()


if __name__ == "__main__":
    _self_test()
    sys.exit(main())
