#!/usr/bin/env python3
"""Kind-universe completeness gate for the milestone-4 evidence battery.

The mechanical file-kind ledger can only prove what the executed cases
observe.  This gate reads every matrix report (``--matrix``) and the
crash report (``--crash``) of one evidence revision and enforces the
cross-language file-kind contract: every persistent artifact kind is
created by each product language (Rust and Go), and every kind whose
contract implies a cross-process reader (``v4_main``,
``live_sidecar``, ``adapter_output``) must be shown opened by both
languages -- the acceptance criterion requires every kind to be opened
with both consumers, so an evidence revision that records no opens
fails instead of vacuously satisfying the requirement.  The required
kind universe is exactly the six journaled kinds plus
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
  on actors that recorded executed work fail the gate.  The executed
  step count of a PASS case actor must be at least the number of
  distinct executed methods it records (the runner increments the
  step counter once per executed step), so a doctored step count
  that contradicts the executed-operation record fails the gate.
- One executable has one identity: two binary records of a matrix
  report that name the same resolved path must agree on the sha256
  (a duplicate-path twin with a different hash is a forged
  identity), and a matrix report whose command exercises the
  fixture must record a ``fixture_tool`` identity (path plus
  sha256) that agrees with the battery's crash identity.
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
- ``v4_main``, ``live_sidecar`` and ``adapter_output`` imply a
  cross-process reader: both-language opened coverage is required,
  and empty opened coverage fails the gate instead of vacating the
  requirement.  Opened coverage is required PER EVIDENCE SOURCE:
  the matrix evidence must show both languages opening
  ``v4_main`` and ``live_sidecar``, and the crash evidence must
  show both consumer languages opening ``v4_main`` and
  ``live_sidecar`` through the scenarios' own executed open
  records.  Stripping open records from one source cannot be
  repaid from another source's coverage.
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
- Case identity binds to the committed case definitions: a PASS
  matrix case whose name is not a case defined under
  ``v4/cli/cases/`` fails, and each actor's recorded executed
  operations must be methods the named case definition declares for
  that actor (a PASS case crediting another case's operations is a
  fabricated cross-matrix rewrite).
- Mixed matrices can only PASS cases whose definition requires both
  producer and consumer services: the runner skips single-actor
  cases in a mixed pair, so a mixed-matrix PASS on a known
  single-actor case (for example ``algebra.publish``) is a
  fabricated cross-matrix PASS even when its actors, argv and
  lineage are internally consistent.
- A matrix binary record that carries a top-level
  ``implementation`` label is accepted only when its
  ``system.describe`` result confirms the same language: language
  attribution comes from the capability result, never from labels,
  and a label that contradicts (or substitutes for) the result is a
  relabel attack on the record root.
- Every recorded executable binding is on-disk verified when the
  gate runs on the CLI: the matrix ``binaries`` records, the matrix
  ``fixture_tool`` record and the crash root binaries table paths
  must exist and their sha256 must match the recorded identities.
Honest limitations.  This gate is a mechanical consistency and
identity anchor: every check compares fields inside and across the
supplied reports.  A fully consistent offline forgery of ALL reports
of one revision -- one that rewrites every matrix and crash report,
the global sha256 map, the binaries tables, the commands, and the
per-case argv consistently -- is not mechanically distinguishable by
this gate alone; such forgeries are caught by the adversarial review
reruns that remain part of the gate process.  Fixture identity is
enforced as a cross-report consistency anchor: the crash report root
binaries table is the authority every recorded command must name.

On-disk binary binding: when the gate runs on the CLI
(``verify_binaries``), every binary path a report records as executed
(matrix ``binaries`` records, matrix ``fixture_tool`` records, and the
crash root binaries table) must exist on the review machine and its
sha256 must equal the recorded sha256.  Committed evidence is
qualified together with the staged binaries, so a recorded path that
does not exist -- or a path whose file no longer matches the recorded
identity -- is a report defect, not a missing-hash excuse.  The
synthetic self-test battery disables this on-disk verification (its
``/tmp`` paths are never staged) and exercises it explicitly through
the F11 control.

Exit status 0 when every required kind has both-language evidence and
no report problem exists; 1 otherwise.
"""

import argparse
import hashlib
import importlib
import json
import os
import re
import shlex
import sys
import tempfile

# The parser derivation imports the runner modules (run, crash_harness)
# for their exact globals; they live next to this gate.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from command_sanitize import (  # noqa: E402  (side-effect free)
    checkout_root,
    owned_temp_root,
)

# The JSON-RPC params-validator answer.  Imported from the same schema
# module the runner and the products' conformance schema use, so the
# gate cannot drift from the transport contract it asserts.
from schema.frame import STD_INVALID_PARAMS as _STD_INVALID_PARAMS  # noqa: E402


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
REQUIRED_OPENED_KINDS = (
    "v4_main", "live_sidecar", "adapter_output", "metadata_delivery")
# Kinds a v1 method can OPEN (as opposed to only create).  A method
# names one of these kinds in its opened_by lineage only when the v1
# contract opens an existing artifact of that kind:
# - v4_main / live_sidecar: the reader and writer opens;
# - adapter_output: an export whose destination already exists is
#   opened (and replaced) by the export writer;
# - metadata_delivery: a database.metadata.get file delivery under
#   replace_existing opens the previously delivered artifact.
OPEN_CAPABLE_KINDS = (
    "v4_main", "live_sidecar", "adapter_output", "metadata_delivery")
# Methods that must be observed refusing their params with -32602 from
# both product languages.  These are the writer_budget-bearing methods
# whose limit grammar has no zero/unlimited value: the refusal must
# happen in the params validator, before any path or budget-consuming
# work, so a single per-language attestation per method is a contract
# term, not a sample.
REQUIRED_PARAMS_NEGATIVE_METHODS = (
    "iprange.v1.direct.replace",
    "iprange.v1.database.metadata.replace",
    "iprange.v1.feeds.create",
    "iprange.v1.database.reclaim")
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

# Matrix-side open credits.  adapter_output is opened by a consumer
# export that replaces an existing destination (the mixed-direction
# export cases), and metadata_delivery by a metadata.get file delivery
# that replaces the previously delivered file.  Without these entries a
# genuine cross-language open of an adapter or delivery artifact would
# be reported as a fabricated credit, so the only evidence the gate
# could accept for those kinds came from the crash battery.
MATRIX_OPEN_METHODS = {
    "adapter_output": ("iprange.v1.export",),
    "metadata_delivery": ("iprange.v1.database.metadata.get",),
    "v4_main": ("iprange.v1.algebra.publish",
                # export reads (opens) the database it exports.
                "iprange.v1.export",
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


_CASE_DEFINITIONS = None
_CASE_DEFINITIONS_ERROR = None


KNOWN_DEFECTS_FILE_NAME = "known-defects.json"
KNOWN_DEFECTS_SCHEMA = "iprange-cli-known-defects-v1"
_KNOWN_DEFECTS_CACHE = None


def _known_defects():
    """Declared engine defects: ``{(matrix, case_name): entry}``.

    ``v4/cli/evidence/known-defects.json`` is the ledger of cases a product
    engine is currently known to fail at the qualification binaries while
    its fix is in flight.  It exists so the battery can carry a red arm
    without deleting it: every FAIL row must appear here, and every entry
    here must actually be failing.  An absent or empty ledger means the
    battery must be entirely green, which is its normal state.

    Malformed entries abort the gate rather than being skipped: an
    anonymous or reason-less entry would let a real failure be parked
    without an owner, which is the failure mode this file could otherwise
    introduce."""
    global _KNOWN_DEFECTS_CACHE
    if _KNOWN_DEFECTS_CACHE is None:
        path = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                            "evidence", KNOWN_DEFECTS_FILE_NAME)
        entries = {}
        if os.path.isfile(path):
            try:
                with open(path, encoding="utf-8") as stream:
                    document = json.load(stream)
            except (OSError, ValueError) as exc:
                raise SystemExit(f"{path}: unreadable: {exc}")
            if document.get("schema") != KNOWN_DEFECTS_SCHEMA:
                raise SystemExit(f"{path}: unexpected schema "
                                 f"{document.get('schema')!r}")
            for entry in document.get("defects", []):
                label = f"{path}: defect entry"
                if not isinstance(entry, dict):
                    raise SystemExit(f"{label}: is not an object")
                for member in ("matrix", "case", "owner", "finding",
                               "observed"):
                    value = entry.get(member)
                    if not isinstance(value, str) or not value.strip():
                        raise SystemExit(f"{label}: needs a nonempty "
                                         f"{member!r}; an entry without an "
                                         f"owner, a reviewer finding, and the "
                                         f"observed response is a placeholder, "
                                         f"not a declared defect")
                if entry["matrix"] not in REQUIRED_MATRICES:
                    raise SystemExit(f"{label}: matrix {entry['matrix']!r} "
                                     f"is not a battery matrix")
                entries[(entry["matrix"], entry["case"])] = entry
        _KNOWN_DEFECTS_CACHE = entries
    return _KNOWN_DEFECTS_CACHE


def _known_defect_problems(matrix, cases):
    """``(unlisted_failures, unexpected_passes)`` for one matrix report."""
    defects = _known_defects()
    declared = {name for (label, name) in defects if label == matrix}
    statuses = {case.get("name"): case.get("status") for case in cases}
    observed = {name for name, status in statuses.items()
                if status == "FAIL"}
    return observed - declared, declared & statuses.keys() - observed


def _case_definitions():
    """Committed case definitions: ``{name: {"requirements": frozenset,
    "methods": {actor: frozenset(methods)}}}``.

    Built once from ``v4/cli/cases/*.json`` through the runner's own
    loader (the single authority for what each case executes).  The
    gate uses the definitions to bind PASS-case identity: a case name
    that is not defined cannot have run, a mixed matrix cannot PASS a
    case that does not require both services, and an actor's recorded
    executed operations must be methods the named case declares for
    that actor.  ``actor_requirements`` and ``declared_actor`` are the
    runner's own functions so the two can never drift.  Returns
    ``(definitions, None)`` or ``(None, error-text)``; the load error
    is cached so the gate reports it once.
    """

    global _CASE_DEFINITIONS, _CASE_DEFINITIONS_ERROR
    if _CASE_DEFINITIONS is None and _CASE_DEFINITIONS_ERROR is None:
        try:
            import run as _run
            definitions = {}
            for case in _run.load_cases(_run.DEFAULT_CASE_DIR):
                methods = {}
                negatives = {}
                groups = {}
                for step in case.get("steps", []):
                    if step.get("kind") == "rpc":
                        actor = _run.declared_actor(step)
                        method = step.get("method")
                        if "expect_params_rejected" in step:
                            key = (actor, method)
                            negatives[key] = negatives.get(key, 0) + 1
                        group = step.get("digest_group")
                        if isinstance(group, str):
                            groups.setdefault(group, set()).add(
                                (actor, method))
                    else:
                        # Legacy CLI steps run on the consumer binary
                        # and record the literal ``legacy`` operation
                        # (run.py run_legacy_step).
                        actor = "consumer"
                        method = "legacy"
                    if isinstance(actor, str) and isinstance(method, str):
                        methods.setdefault(actor, set()).add(method)
                definitions[case["name"]] = {
                    "requirements": frozenset(_run.actor_requirements(case)),
                    "methods": {actor: frozenset(ops)
                                for actor, ops in methods.items()},
                    # Declared negative-params steps and declared digest
                    # groups, so the recorded attestation of a PASS case
                    # can be compared with what its definition says it
                    # executes (a dropped or invented attestation is a
                    # doctored record, not a thinner run).
                    "params_rejected": negatives,
                    "digest_groups": groups,
                }
            _CASE_DEFINITIONS = definitions
        except Exception as exc:  # noqa: BLE001 - report, never crash
            _CASE_DEFINITIONS_ERROR = (
                f"cannot load the committed case definitions from "
                f"v4/cli/cases: {exc}")
    return _CASE_DEFINITIONS, _CASE_DEFINITIONS_ERROR


def _binary_label_conflict(record):
    """Problem text when a matrix binary record's top-level
    ``implementation`` label contradicts (or replaces) the language
    its ``system.describe`` capability result declares.

    Language attribution is anchored in the capability result only; a
    top-level implementation label is a label.  A label that differs
    from the result -- or claims a product language the result does
    not confirm -- is a relabel attack on the record root, and the
    gate must reject it (the global map still catches result-level
    rewrites across reports; this closes the root-level variant).
    """

    if not isinstance(record, dict):
        return None
    top = record.get("implementation")
    result = record.get("result")
    declared = (
        result.get("implementation")
        if isinstance(result, dict) else None)
    if not isinstance(top, str) or top not in PRODUCT_LANGUAGES:
        return None
    if declared not in PRODUCT_LANGUAGES or top != declared:
        return (
            f"binary record declares a top-level implementation "
            f"label {top!r} that its system.describe result "
            f"({declared!r}) does not confirm")
    return None


def _sha256_file(path):
    """SHA-256 of one file, streamed (the recorded binaries are a few
    tens of MB at most; hashing is a fraction of a second)."""

    import hashlib
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


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


def _report_checkout_root(report):
    """Checkout root a report's relative command values are relative
    to (the producer's recorded root), or the reviewing gate's own
    checkout root when the report does not record one.

    The runner records a non-personal producer checkout root in the
    report (``checkout_root``); resolving against it keeps the
    binary-identity binding stable when the evidence is assessed from
    another clone.  Personal producer roots are never recorded, so
    the fallback is the gate's checkout — the same authority the
    sanitizer uses when it rewrites those values."""
    root = (report or {}).get("checkout_root")
    if isinstance(root, str) and root and os.path.isabs(root):
        return root
    return checkout_root()


def _resolve_report_path(value, report):
    """One path a report command names, invariant to the gate's cwd
    and checkout.

    The matrix runner records checkout-contained values as
    checkout-relative spellings (command_sanitize rewrites them so
    the evidence is invariant to the invocation directory); resolving
    them against the gate's process cwd would reject the same
    evidence when the gate runs from a scratch directory, and
    resolving against the reviewing checkout would change the
    verdict when the evidence moves between clones.  Relative values
    therefore resolve against the report's recorded producer
    checkout root (or the gate's own checkout root as fallback).
    Absolute values pass through unchanged."""
    if os.path.isabs(value):
        return os.path.realpath(value)
    return os.path.realpath(os.path.join(_report_checkout_root(report),
                                         value))


def _matrix_path_to_sha(report):
    """Matrix-report binaries block: resolved binary path -> sha256,
    plus per-report identity conflicts.

    Matrix reports record each product binary as a capability record
    with a ``path`` and ``sha256``; the command binding resolves the
    recorded ``--rust``/``--go`` path through this table and then
    through the global sha256 -> implementation map.  Paths are
    resolved the same way the command binding resolves them, so a
    spelling variant cannot bypass the table.  One executable may be
    named by several records only when every record agrees on its
    sha256: two records naming the same resolved path with different
    hashes are a contradictory identity (a forged twin record) and
    each conflict is returned for the gate to reject.
    """

    table = {}
    conflicts = []
    binaries = report.get("binaries")
    if not isinstance(binaries, dict):
        return table, conflicts
    for record in binaries.values():
        if not isinstance(record, dict):
            continue
        raw_path = record.get("path")
        sha = record.get("sha256")
        if not (isinstance(raw_path, str) and isinstance(sha, str)):
            continue
        path = _resolve_report_path(raw_path, report)
        previous = table.get(path)
        if previous is not None and previous != sha:
            conflicts.append(
                f"matrix binary records name the same executable "
                f"{raw_path!r} (resolved {path!r}) with different "
                f"sha256 values {previous!r} and {sha!r}; an "
                f"executable has one identity")
            continue
        table[path] = sha
    return table, conflicts


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

    The runner records the sanitized command (check_kind_coverage and
    run.py share the command_sanitize module) for every matrix and
    crash report, so the replayed invocation is part of the evidence:
    a forger must make the recorded command agree with the report.
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
        # Root-level relabel guard: a binary record may carry a
        # top-level ``implementation`` label only when its
        # system.describe result confirms the same language (F7).
        for record in (report.get("binaries") or {}).values():
            conflict = _binary_label_conflict(record)
            if conflict:
                problems.append(f"{path}: {conflict}")
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
                      problems, verify_cases=False):
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
        return matrix, {}, empty_stats, problems, None
    failed = report.get("failed", 0)
    # The ledger is consulted unconditionally.  Gating it on failed != 0
    # (the shape this gate had before) meant a report made green by deleting
    # its red rows, or a fixed engine still carrying a stale ledger entry,
    # was never compared with the ledger at all: the stale half of a
    # bidirectional rule is the half that catches a report with no failures.
    unlisted, unexpected = _known_defect_problems(
        matrix or "", report.get("cases", []))
    if unlisted or unexpected:
        problems.append(
            f"matrix {path}: report disagrees with "
            f"evidence/{KNOWN_DEFECTS_FILE_NAME} on {len(unlisted)} "
            f"undeclared and {len(unexpected)} stale failed case(s)")
    for case_name in sorted(unlisted):
        problems.append(
            f"matrix {path}: FAIL case {case_name!r} is not a declared "
            f"known defect in evidence/{KNOWN_DEFECTS_FILE_NAME}; an "
            f"undeclared failure is not acceptance evidence")
    for case_name in sorted(unexpected):
        problems.append(
            f"matrix {path}: evidence/{KNOWN_DEFECTS_FILE_NAME} declares "
            f"{case_name!r} as a failing case of matrix {matrix!r} but "
            f"the case did not FAIL; remove the stale entry so a future "
            f"regression cannot hide behind it")
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
    # namespace is derived only when the recorded command exists; the
    # no-command branch must still end with a recorded problem and not
    # an uncontrolled UnboundLocalError at the command_fixture check
    # below (external review finding).
    namespace = None
    # Per-language command-selected executable: the path the recorded
    # command names for each product language.  Each actor must be
    # bound to exactly this executable (external review finding); a
    # report that runs a different binary for a role while its command
    # claims another contradicts the record.  Initialized before the
    # argv branch so a report without command metadata records the
    # argv problem instead of raising UnboundLocalError in the
    # per-case checks below (external review finding, second site).
    command_selected = {}
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
        if runner_parser is not None:
            namespace = _parse_recorded_command(
                runner_parser, argv, f"matrix {path}", problems)
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
            report_shas, path_sha_conflicts = _matrix_path_to_sha(report)
            problems.extend(path_sha_conflicts)
            for flag, language, attr in (
                    ("--rust", "rust", "rust_binary"),
                    ("--go", "go", "go_binary")):
                named = getattr(namespace, attr)
                if named is None:
                    problems.append(
                        f"matrix {path}: report command records no {flag} "
                        f"argument")
                    continue
                bound_path = _resolve_report_path(named, report)
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
            elif _resolve_report_path(fixture, report) not in fixture_paths:
                problems.append(
                    f"matrix {path}: report command --fixture-tool "
                    f"{fixture!r} does not name the fixture binary the "
                    f"battery's crash report records "
                    f"({sorted(fixture_paths) or '<none>'})")
    command_fixture = None
    if namespace is not None:
        named = namespace.fixture_tool
        if named is not None:
            command_fixture = _resolve_report_path(named, report)
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
    attested_negatives = set()
    attested_groups = {}
    contributing = 0
    # Case-identity authority: the committed case definitions.  Only
    # loaded when a PASS case exists and either the report is a mixed
    # matrix (single-actor cases cannot PASS there) or the gate runs
    # with case verification enabled, so the synthetic self-test
    # battery that names synthetic cases stays cheap.
    case_definitions = None
    case_load_error = None
    if any(case.get("status") == "PASS" for case in cases):
        # The definitions are the authority for what a PASS case must
        # attest (params rejections, export digest groups, executed
        # operations), so they are loaded for every report that claims a
        # PASS, not only for mixed matrices.  Synthetic self-test cases
        # are absent from the definitions and skip those comparisons.
        case_definitions, case_load_error = _case_definitions()
        if case_load_error:
            problems.append(f"matrix {path}: {case_load_error}")
    for case in cases:
        if case.get("status") != "PASS":
            continue
        pass_cases += 1
        case_name = case.get("name", "<unnamed>")
        if matrix in ("rust_to_go", "go_to_rust") \
                and case_definitions is not None:
            # Mixed matrices execute both binaries for every PASS
            # case; the runner skips cases that do not require both
            # services, so a PASS on a known single-actor case is a
            # fabricated cross-matrix PASS (F3) regardless of how
            # consistent its actors and lineage look.
            defined = case_definitions.get(case_name)
            if defined is not None and \
                    defined["requirements"] != frozenset(
                        ("producer", "consumer")):
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} requires "
                    f"actors {sorted(defined['requirements'])} but is "
                    f"recorded PASS in mixed matrix {matrix!r}; a mixed "
                    f"matrix skips single-actor cases and can only PASS "
                    f"cases requiring both producer and consumer")
        if verify_cases:
            if case_definitions is None:
                if case_load_error:
                    # Already reported above; the gate fails closed.
                    continue
                # Definitions were not loaded for a single-language
                # matrix without PASS cases; this branch runs only
                # when verify_cases is on and a PASS case exists, so
                # force the load.
                case_definitions, case_load_error = _case_definitions()
                if case_load_error:
                    problems.append(f"matrix {path}: {case_load_error}")
                    continue
            defined = (case_definitions or {}).get(case_name)
            if defined is None:
                problems.append(
                    f"matrix {path}: PASS case {case_name!r} is not a "
                    f"case defined in v4/cli/cases/")
            else:
                defined_methods = defined["methods"]
                # Per-actor executed work must be work the named case
                # definition declares for that actor: a PASS case that
                # credits another case's operations is a fabricated
                # rewrite even in a single-language matrix.
                actors_ops = case.get("actors")
                for actor in ALL_ACTORS:
                    entry = (actors_ops or {}).get(actor)
                    if not isinstance(entry, dict):
                        continue
                    for op in entry.get("operations", []):
                        if op not in defined_methods.get(actor, ()):
                            problems.append(
                                f"matrix {path}: PASS case {case_name!r} "
                                f"actor {actor!r} records executed "
                                f"operation {op!r} that the case "
                                f"definition of {case_name!r} never "
                                f"executes for that actor")
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
                # Step-count truthfulness: the runner increments the
                # actor's executed-step counter once per executed step
                # and records each distinct executed method once, so a
                # genuine record always satisfies
                # steps >= distinct executed methods.  A PASS case
                # actor that records fewer steps than its distinct
                # executed methods contradicts the executed-work
                # evidence (doctored step count) and is a report
                # defect.
                distinct_methods = len(set(operations))
                if (isinstance(executed, int)
                        and not isinstance(executed, bool)
                        and distinct_methods > 0
                        and executed < distinct_methods):
                    problems.append(
                        f"matrix {path}: PASS case {case_name!r} actor "
                        f"{actor!r} records {executed} executed "
                        f"step(s) but {distinct_methods} distinct "
                        f"executed method(s); run.py increments once "
                        f"per executed step")
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
                # Mirror of the crash-side open contract: only kinds in
                # OPEN_CAPABLE_KINDS may carry an opened ref.  Any other
                # kind has no cross-process open, so the ref is a
                # fabricated open.
                if facts["kind"] not in OPEN_CAPABLE_KINDS:
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
        # Params-rejection and digest-group attestation (security F2,
        # closure F2): validated against each executing actor's resolved
        # identity, executed-step count, and executed operations.  A
        # dropped or invented attestation is a FAIL here, and the
        # battery-level requirements in assess() cannot be met by other
        # evidence, so the corpus cannot stop testing the params
        # validator or the cross-language export silently.
        defined_negatives = None
        declared_groups = None
        if case_definitions is not None:
            definition = case_definitions.get(case_name)
            if isinstance(definition, dict):
                defined_negatives = definition.get("params_rejected")
                declared_groups = definition.get("digest_groups")
        negative_pairs, group_facts = _attestation_problems(
            path, case_name, case, implementations, actor_steps,
            actor_operations, defined_negatives, declared_groups,
            problems)
        attested_negatives |= negative_pairs
        for group, facts in group_facts.items():
            merged = attested_groups.setdefault(group, {
                "digests": set(), "languages": set(), "actors": set()})
            merged["digests"] |= facts["digests"]
            merged["languages"] |= facts["languages"]
            merged["actors"] |= facts["actors"]
    stats = {"cases": len(cases), "fail_cases": fail_cases,
             "pass_cases": pass_cases, "contributing": contributing,
             "params_negative": attested_negatives,
             "digest_groups": attested_groups}
    return matrix, evidence, stats, problems, command_fixture


def _attestation_problems(path, case_name, case, implementations,
                          actor_steps, actor_operations, defined_negatives,
                          declared_groups, problems):
    """Validate one PASS case's params-rejection and digest-group records.

    Returns ``(negative_pairs, digest_groups)`` where ``negative_pairs``
    is the set of ``(method, product_language)`` the case attests and
    ``digest_groups`` maps a group label to ``(digests, languages,
    actors)``.  A malformed or missing record is appended to ``problems``
    and contributes nothing to the battery aggregate, so a doctored
    report cannot satisfy the battery-level requirements either.
    """

    negative_pairs = set()
    digest_groups = {}
    recorded_group = {}
    rejected = case.get("params_rejected")
    if rejected is None:
        rejected = []
    if not isinstance(rejected, list):
        problems.append(
            f"matrix {path}: PASS case {case_name!r} params_rejected is "
            f"not an array")
        rejected = []
    for index, entry in enumerate(rejected):
        label = (f"matrix {path}: PASS case {case_name!r} "
                 f"params_rejected[{index}]")
        if not isinstance(entry, dict) or set(entry) != {
                "actor", "method", "transport_code", "request"}:
            problems.append(f"{label}: members must be exactly actor, "
                            f"method, transport_code, request")
            continue
        actor = entry["actor"]
        if actor not in ALL_ACTORS:
            problems.append(f"{label}: unknown actor {actor!r}")
            continue
        if entry["transport_code"] != _STD_INVALID_PARAMS:
            problems.append(
                f"{label}: transport_code {entry['transport_code']!r} is "
                f"not {_STD_INVALID_PARAMS} (the params-validator answer)")
            continue
        method = entry["method"]
        request = entry["request"]
        if not isinstance(method, str) or not method:
            problems.append(f"{label}: method is not a nonempty string")
            continue
        if not isinstance(request, str) or not request:
            problems.append(
                f"{label}: request bytes are not recorded; a params-"
                f"rejection assertion must carry the exact frame it sent")
            continue
        recorded_steps = actor_steps.get(actor)
        if recorded_steps is None:
            # The steps field is absent or malformed; that defect is
            # reported with the actor record itself, and an attestation
            # that cannot be attributed to executed work contributes
            # nothing.
            continue
        if recorded_steps < 1:
            problems.append(
                f"{label}: credits actor {actor!r} with zero executed steps")
            continue
        implementation = implementations.get(actor)
        if implementation in PRODUCT_LANGUAGES:
            negative_pairs.add((method, implementation))

    if defined_negatives is not None:
        recorded = {}
        for entry in rejected:
            if isinstance(entry, dict) and "actor" in entry \
                    and "method" in entry:
                key = (entry["actor"], entry["method"])
                recorded[key] = recorded.get(key, 0) + 1
        if recorded != dict(defined_negatives):
            problems.append(
                f"matrix {path}: PASS case {case_name!r} records "
                f"params_rejections {sorted(recorded.items())} that differ "
                f"from the {sorted(dict(defined_negatives).items())} its "
                f"committed case definition declares; a dropped or "
                f"invented params-rejection assertion is a doctored "
                f"record")

    groups = case.get("digest_groups")
    if groups is None:
        groups = {}
    if not isinstance(groups, dict):
        problems.append(
            f"matrix {path}: PASS case {case_name!r} digest_groups is not "
            f"an object")
        groups = {}
    if declared_groups is not None and set(groups) - set(declared_groups):
        problems.append(
            f"matrix {path}: PASS case {case_name!r} records digest groups "
            f"{sorted(set(groups) - set(declared_groups))} that its "
            f"committed case definition never declares")
    for group, entries in sorted(groups.items()):
        label = (f"matrix {path}: PASS case {case_name!r} digest_group "
                 f"{group!r}")
        if not isinstance(entries, list) or not entries:
            problems.append(f"{label}: must record a nonempty array")
            continue
        digests = set()
        languages = set()
        actors = set()
        for index, entry in enumerate(entries):
            if not isinstance(entry, dict) or set(entry) != {
                    "actor", "method", "format", "path", "sha256"}:
                problems.append(
                    f"{label}[{index}]: members must be exactly actor, "
                    f"method, format, path, sha256")
                continue
            actor = entry["actor"]
            if actor not in ALL_ACTORS:
                problems.append(f"{label}[{index}]: unknown actor {actor!r}")
                continue
            sha256 = entry["sha256"]
            if not (isinstance(sha256, str) and len(sha256) == 64
                    and all(c in "0123456789abcdef" for c in sha256)):
                problems.append(
                    f"{label}[{index}]: sha256 {sha256!r} is not 64 "
                    f"lowercase hex digits")
                continue
            if entry.get("method") != "iprange.v1.export":
                problems.append(
                    f"{label}[{index}]: method {entry.get('method')!r} is "
                    f"not iprange.v1.export; only export artifacts are "
                    f"digest-grouped")
                continue
            if entry.get("method") not in (actor_operations.get(actor)
                                           or []):
                problems.append(
                    f"{label}[{index}]: names an operation not recorded in "
                    f"actor {actor!r} executed operations")
                continue
            recorded_steps = actor_steps.get(actor)
            if recorded_steps is None:
                continue
            if recorded_steps < 1:
                problems.append(f"{label}[{index}]: credits actor {actor!r} "
                                f"with zero executed steps")
                continue
            implementation = implementations.get(actor)
            if implementation in PRODUCT_LANGUAGES:
                languages.add(implementation)
            digests.add(sha256)
            actors.add(actor)
        if not digests:
            problems.append(f"{label}: records no usable digest")
            continue
        if len(digests) != 1:
            problems.append(
                f"{label}: digests {sorted(digests)} disagree; one digest "
                f"group is one artifact, byte for byte")
        if not {"producer", "consumer"} <= actors:
            problems.append(
                f"{label}: both service roles must produce the artifact "
                f"(roles {sorted(actors)}); a group recorded by only one "
                f"role never compares the consumer's export against the "
                f"producer's own")
        recorded_group[group] = {(entry["actor"], entry["method"])
                                 for entry in entries
                                 if isinstance(entry, dict)
                                 and isinstance(entry.get("actor"), str)
                                 and isinstance(entry.get("method"), str)}
        digest_groups[group] = {"digests": digests,
                                "languages": languages,
                                "actors": actors}
    if declared_groups is not None:
        declared = {group: {tuple(pair) for pair in pairs}
                    for group, pairs in declared_groups.items()}
        if recorded_group != declared:
            problems.append(
                f"matrix {path}: PASS case {case_name!r} records digest "
                f"groups {sorted((g, sorted(v)) for g, v in recorded_group.items())} "
                f"that differ from the {sorted((g, sorted(v)) for g, v in declared.items())} "
                f"its committed case definition declares; a dropped or "
                f"invented export attestation is a doctored record")
    return negative_pairs, digest_groups


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
                      or _resolve_report_path(named, report)
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
    # Kinds the scenarios' own executed open records prove the
    # CONSUMER opened, per consumer language.  The crash battery must
    # demonstrate both consumer languages opening v4_main and
    # live_sidecar through the scenarios' executed steps; stripping
    # consumer opens from one direction cannot be repaid by producer
    # opens or by the other direction (F5).
    consumer_opened = {}
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
        # A scenario that passed has nothing to report as failed, and a
        # scenario whose residue was judged unbounded cannot be a pass: the
        # harness records a scenario's failures as it judges them, so a PASS
        # row carrying either is a rewritten verdict rather than a result.
        failures = scenario.get("failures")
        if not isinstance(failures, list):
            problems.append(
                f"crash {path}: PASS scenario {scenario_name!r} records no "
                f"failures list ({failures!r}); a passing scenario has an "
                f"empty one, and a row that dropped it cannot be told from "
                f"one whose failures were deleted")
        elif failures:
            problems.append(
                f"crash {path}: PASS scenario {scenario_name!r} records "
                f"{len(failures)} failure(s) {failures[:2]} while claiming to "
                f"pass")
        if scenario.get("residue_bounded") is False:
            problems.append(
                f"crash {path}: PASS scenario {scenario_name!r} records "
                f"residue_bounded false; leftover residue after a crash is "
                f"the defect this battery exists to find, so it cannot also "
                f"be a pass")
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
                if actor == "consumer":
                    consumer_opened.setdefault(kind, set()).add(
                        language or "?")
    if not {"rust", "go"} <= producers or not {"rust", "go"} <= consumers:
        problems.append(
            f"crash {path}: PASS scenarios must span both language "
            f"directions (producers {sorted(producers)}, "
            f"consumers {sorted(consumers)})")
    stats = {"scenarios": len(scenarios), "pass_scenarios": pass_scenarios}
    return evidence, stats, consumer_opened, problems


GIT_HEAD_LENGTH = 40


def _git_head_problems(path, label, report):
    """Problem list for one report's recorded source revision.

    A report that does not name the revision it measured cannot bind a
    verdict to a tree, so the field is required, must be a full 40-hex
    commit oid, and must not be a placeholder of repeated digits (an
    all-zeros or all-ones oid is what a hand-written report writes when
    nobody measured anything).
    """

    git_head = report.get("git_head") if isinstance(report, dict) else None
    if git_head is None or "git_head" not in report:
        return [f"{label} {path}: report records no git_head; every consumed "
                f"report must name the source revision it measured"]
    if not isinstance(git_head, str) or len(git_head) != GIT_HEAD_LENGTH \
            or not all(character in "0123456789abcdef"
                       for character in git_head):
        return [f"{label} {path}: git_head {git_head!r} is not a 40-hex "
                f"commit oid"]
    if len(set(git_head)) == 1:
        return [f"{label} {path}: git_head {git_head!r} is a repeated-digit "
                f"placeholder, not a measured revision"]
    return []


def _expected_inventory(matrix):
    """(executed_names, skipped_names) the runner must produce for a matrix.

    Derived mechanically from the committed corpus, not from a count someone
    typed into a report.  A same-language matrix runs every case.  A mixed
    matrix runs exactly the cases that declare both service roles and skips
    the rest, which is the rule in ``run.py`` ``run_one``/``record_skip``:
    ``actor_requirements(case) != {producer, consumer}`` skips with
    ``not cross-producer: case has no <actor> step``.  Reusing the runner's
    own ``load_cases`` and ``actor_requirements`` keeps one authority for what
    a case is.
    """

    definitions, error = _case_definitions()
    if error:
        return None, None, error
    names = set(definitions)
    if matrix in ("rust", "go"):
        return names, set(), None
    executed = {name for name in names
                if definitions[name]["requirements"] == frozenset(ALL_ACTORS)}
    return executed, names - executed, None


def _case_inventory_problems(path, matrix, report, problems):
    """Require each matrix report to carry a row for every committed case.

    Kind coverage proves that something in each artifact class ran; it does
    not prove the corpus ran.  Without this check a report can drop the rows
    it does not like -- 44 of 49 PASS rows deleted still leaves every required
    kind covered by the survivors, and the gate reported PASS.  The executed
    case set is therefore an obligation derived from ``v4/cli/cases/`` plus
    the runner's skip rule, and the root counters must agree with the rows
    they summarize.
    """

    executed, skipped, error = _expected_inventory(matrix)
    if error:
        problems.append(f"matrix {path}: {error}")
        return
    rows = [case for case in report.get("cases", [])
            if isinstance(case, dict)]
    seen = {}
    for index, case in enumerate(rows):
        name = case.get("name")
        if name in seen:
            problems.append(
                f"matrix {path}: cases[{index}] repeats case {name!r}; the "
                f"later row would silently outrank the first")
            continue
        seen[name] = case
    for name in sorted(set(seen) - executed - skipped):
        problems.append(
            f"matrix {path}: row for case {name!r}, which no committed case "
            f"under v4/cli/cases defines; an invented row is not evidence")
    for name in sorted(executed - set(seen)):
        problems.append(
            f"matrix {path}: case {name!r} has no row. A case defined under "
            f"v4/cli/cases/ must appear in every matrix report that runs it; "
            f"deleting a PASS row is not a way to shrink the battery")
    for name in sorted(skipped - set(seen)):
        problems.append(
            f"matrix {path}: single-actor case {name!r} has no row. Mixed "
            f"matrices record their skips, so a skipped case that is absent "
            f"cannot be told apart from a case that was never offered")
    for name in sorted(executed & set(seen)):
        status = seen[name].get("status")
        if status not in ("PASS", "FAIL"):
            problems.append(
                f"matrix {path}: case {name!r} declares both services and "
                f"must execute, but its row says status {status!r}")
    for name in sorted(skipped & set(seen)):
        status = seen[name].get("status")
        if status != "SKIP":
            problems.append(
                f"matrix {path}: case {name!r} does not declare both services "
                f"and must be skipped by the runner, but its row says status "
                f"{status!r}")
    counters = {"passed": sum(1 for case in rows
                              if case.get("status") == "PASS"),
                "failed": sum(1 for case in rows
                              if case.get("status") == "FAIL"),
                "skipped": sum(1 for case in rows
                               if case.get("status") == "SKIP")}
    for member, truth in counters.items():
        recorded = report.get(member)
        if recorded != truth:
            problems.append(
                f"matrix {path}: root {member}={recorded!r} contradicts the "
                f"{truth} rows carrying that status")
    if len(rows) != len(executed) + len(skipped):
        problems.append(
            f"matrix {path}: report has {len(rows)} rows but the committed "
            f"corpus defines {len(executed) + len(skipped)} cases for this "
            f"matrix")


def _shared_git_head(paths_by_label, problems):
    """Require one revision across every consumed report.

    ``paths_by_label`` maps a report label ("matrix", "crash",
    "fifo-surface", "throughput") to the paths supplied for it.  A battery is
    one revision, so every consumed report must name the same 40-hex commit
    oid.  A report copied from an earlier run, a report from a different
    checkout, and a report whose field was edited into place each show up here
    as a divergence instead of being silently absorbed.

    A field can only carry weight if its absence also fails, so the revision
    is required, format-checked, screened for repeated-digit placeholders, and
    compared across reports before any of the per-report verdicts count.
    """

    collected = {}
    for label, paths in paths_by_label.items():
        for path in paths:
            report = _load_report(path, [])
            if not isinstance(report, dict):
                problems.append(f"{label} {path}: report is not an object")
                continue
            for problem in _git_head_problems(path, label, report):
                if problem not in problems:
                    problems.append(problem)
            value = report.get("git_head")
            if isinstance(value, str):
                collected.setdefault(value, []).append(f"{label} {path}")
    if len(collected) > 1:
        detail = "; ".join(
            f"{revision[:12]} <- {', '.join(sorted(collected[revision]))}"
            for revision in sorted(collected))
        problems.append(
            f"consumed reports disagree about the source revision under "
            f"test: {detail}")
        return None
    return next(iter(collected), None)


def _sha256_ledger(path):
    """Read a SHASUMS-format ledger: ``{(sha256, path)} -> True}``.

    The ledger format is the one ``sha256sum`` writes: digest, two spaces,
    then the staged path.  It is the reviewer-side list of what the battery
    staged, so it is consulted as an *additional* binder when supplied.  It
    never replaces the executed-actor binding in the matrix and crash
    reports; a sha256 that appears nowhere in it is a report defect only
    when the ledger itself was supplied."""

    if not path:
        return None
    resolved = os.path.realpath(path)
    if not os.path.isfile(resolved):
        raise SystemExit(f"--sha256-ledger {path} does not exist")
    entries = {}
    with open(resolved, encoding="utf-8") as stream:
        for number, line in enumerate(stream, start=1):
            line = line.rstrip("\n")
            if not line.strip():
                continue
            digest, _, recorded = line.partition("  ")
            digest = digest.strip()
            recorded = recorded.strip()
            if len(digest) != 64 or not all(
                    character in "0123456789abcdef" for character in digest) \
                    or not recorded:
                raise SystemExit(
                    f"--sha256-ledger {path}:{number} is not a "
                    f"'<sha256>  <path>' line")
            entries.setdefault(digest, set()).add(recorded)
    if not entries:
        raise SystemExit(f"--sha256-ledger {path} lists no entries")
    return entries


def _surface_binary_identity(path, label, engine, record, implementation_of,
                             ledger, problems, on_disk,
                             require_provenance=True):
    """Bind one surface-report binary record to a measured identity.

    ``record`` is the report's own binary entry (fifo ``binaries`` or
    throughput ``product``).  Three anchors, and a record must satisfy every
    anchor that is available:

    * the ``implementation`` label must equal the engine the record is
      filed under -- a record that files the Go binary as rust is a
      mislabeled artifact, not a parity observation;
    * the recorded sha256 must be the identity the matrix and crash reports
      prove that engine executed (``implementation_of`` is built from
      executed-actor records, never from labels);
    * when the recorded path is readable here, its digest must equal the
      recorded sha256; when a SHASUMS ledger was supplied, the digest must be
      listed by it.
    """

    if not isinstance(record, dict):
        problems.append(f"{label} {path}: {engine} binary record is not an "
                        f"object")
        return
    where = f"{label} {path}: {engine}"
    if record.get("implementation") != engine:
        problems.append(
            f"{where}: records implementation {record.get('implementation')!r}; "
            f"an identity attributed to the wrong engine cannot support a "
            f"both-engine verdict")
    digest = record.get("sha256")
    if not (isinstance(digest, str) and len(digest) == 64
            and all(character in "0123456789abcdef"
                    for character in digest)):
        problems.append(
            f"{where}: sha256 {digest!r} is not a measured digest; the "
            f"surface verdict must bind to a specific artifact")
        return
    proven = implementation_of.get(digest)
    if proven is None and require_provenance:
        problems.append(
            f"{where}: sha256 {digest} is not an identity the battery's "
            f"matrix or crash reports record as executed; the surface gate "
            f"ran something the battery cannot account for")
    elif proven is not None and proven != engine:
        problems.append(
            f"{where}: sha256 {digest} is proven {proven} by the executed "
            f"actor records of this battery; relabeling it {engine} is a "
            f"report defect")
    if ledger is not None and digest not in ledger:
        problems.append(
            f"{where}: sha256 {digest} is absent from the supplied SHASUMS "
            f"ledger; the artifact the surface gate measured is not one the "
            f"battery staged")
    if on_disk:
        recorded_path = record.get("path")
        if isinstance(recorded_path, str):
            resolved = os.path.realpath(recorded_path)
            if os.path.isfile(resolved):
                actual = _sha256_file(resolved)
                if actual != digest:
                    problems.append(
                        f"{where}: recorded binary {resolved!r} has sha256 "
                        f"{actual} on disk, not the recorded {digest}")
                return
            problems.append(
                f"{where}: recorded binary {resolved!r} does not exist on the "
                f"review machine")
            return


def fifo_surface_evidence(path, report, implementation_of, ledger, problems,
                          verify_binaries=False, fixture_shas=None):
    """Re-validate the committed FIFO-surface report inside the kind gate.

    ``check_fifo_surface.py`` owns the arm table and verifies the refusals
    themselves; the kind gate owns the *identity* of the artifacts that
    produced those refusals, so a FIFO verdict cannot be purchased by
    editing digests or implementation labels into a report.  The arm table
    is re-checked here only for the invariants the kind gate depends on:
    both engines present, a PASS verdict, and an empty problem list.
    """

    if report.get("schema") != "iprange-cli-fifo-surface-report-v1":
        problems.append(f"fifo-surface {path}: unexpected schema "
                        f"{report.get('schema')!r}")
    if report.get("result") != "PASS":
        problems.append(f"fifo-surface {path}: result "
                        f"{report.get('result')!r}; a FIFO that blocked is a "
                        f"product defect, not coverage")
    if report.get("problems") not in ([], None):
        problems.append(f"fifo-surface {path}: report carries problems "
                        f"{report.get('problems')!r}")
    binaries = report.get("binaries")
    if not isinstance(binaries, dict):
        problems.append(f"fifo-surface {path}: no binaries table to bind the "
                        f"verdict to")
        return
    for engine in ("go", "rust"):
        _surface_binary_identity(path, "fifo-surface", engine,
                                 binaries.get(engine), implementation_of,
                                 ledger, problems,
                                 on_disk=verify_binaries)
    # The fixture is a generator, not a service, so it has no executed actor
    # record anywhere; its identity anchor is the crash report's root binaries
    # table, exactly as the matrix fixture is bound.  The surface harness
    # records it top level, and a report that filed it under binaries instead
    # must not leave the binding un-checked.
    fixture = report.get("fixture_tool")
    beside = binaries.get("fixture_tool")
    if not isinstance(fixture, dict):
        fixture = beside
    # The surface harness records the fixture at top level.  A second record
    # filed under binaries is another claim about the same generator, so the
    # two must agree: trusting whichever comes first would let a report keep a
    # genuine fixture beside a fabricated one.
    if isinstance(fixture, dict) and isinstance(beside, dict) and (
            fixture.get("sha256"), fixture.get("path")) != (
            beside.get("sha256"), beside.get("path")):
        problems.append(
            f"fifo-surface {path}: the report files two fixture_tool records "
            f"that disagree ({fixture.get('sha256')!r} against "
            f"{beside.get('sha256')!r}); the generator the arm table built "
            f"its artifacts from has one identity")
        fixture = None
    if not isinstance(fixture, dict):
        if not isinstance(beside, dict):
            problems.append(
                f"fifo-surface {path}: the report records no fixture_tool "
                f"identity, so the artifacts every arm refused are "
                f"unaccounted for")
    else:
        digest = fixture.get("sha256")
        if not _is_sha256(digest):
            problems.append(
                f"fifo-surface {path}: fixture_tool sha256 {digest!r} is not "
                f"a measured digest")
        elif fixture_shas and digest not in fixture_shas:
            problems.append(
                f"fifo-surface {path}: fixture_tool sha256 {digest} is not "
                f"the fixture identity the battery's crash report records; "
                f"the artifacts the arm table consumed are unaccounted for")
        # The fixture is a generator rather than a service, so it carries no
        # system.describe result and no implementation label to check: its
        # anchors are the crash record's fixture identity, the ledger, and the
        # file on disk.
        if ledger is not None and _is_sha256(digest) and digest not in ledger:
            problems.append(
                f"fifo-surface {path}: fixture_tool sha256 {digest} is absent "
                f"from the supplied SHASUMS ledger; the generator the arm "
                f"table built its artifacts from is not one the battery "
                f"staged")
        if verify_binaries and _is_sha256(digest):
            recorded_path = fixture.get("path")
            if isinstance(recorded_path, str):
                resolved = os.path.realpath(recorded_path)
                if os.path.isfile(resolved):
                    actual = _sha256_file(resolved)
                    if actual != digest:
                        problems.append(
                            f"fifo-surface {path}: fixture binary {resolved!r} "
                            f"has sha256 {actual} on disk, not the recorded "
                            f"{digest}")
                else:
                    problems.append(
                        f"fifo-surface {path}: fixture binary {resolved!r} "
                        f"does not exist on the review machine")
    arms = [record for record in (report.get("arms") or [])
            if isinstance(record, dict)]
    engines_seen = {record.get("engine") for record in arms}
    if engines_seen != {"go", "rust"}:
        problems.append(
            f"fifo-surface {path}: arms were executed by {sorted(engines_seen)}; "
            f"a both-engine claim needs both engines present in the arm log")
    # The arm inventory and its expectations belong to check_fifo_surface.py.
    # Consuming the table here is what stops a report from deleting an arm or
    # rewriting an expectation: the gate has always known that both engines
    # appear somewhere in the log, and never knew that all 17 arms did.
    surface = _table_module(
        "check_fifo_surface",
        ("ARM_NAMES", "ARM_METHOD", "ARM_EXPECTED", "PRODUCT_ERROR"),
        f"fifo-surface {path}", problems)
    if surface is None:
        return
    expected_rows = {(engine, arm) for engine in ("go", "rust")
                     for arm in surface.ARM_NAMES}
    seen = {}
    for index, record in enumerate(arms):
        key = (record.get("engine"), record.get("arm"))
        if key in seen:
            problems.append(
                f"fifo-surface {path}: arms[{index}] repeats {key[0]}/"
                f"{key[1]}; the later record would outrank the first")
            continue
        seen[key] = record
    for key in sorted(set(seen) - expected_rows):
        problems.append(
            f"fifo-surface {path}: row for {key[0]}/{key[1]}, which the "
            f"committed arm table does not define; an invented arm is not "
            f"evidence")
    for key in sorted(expected_rows - set(seen)):
        problems.append(
            f"fifo-surface {path}: {key[0]} has no row for arm {key[1]}; the "
            f"arm table is an obligation for both engines, and a report that "
            f"omitted the refusals it did not like is not a surface verdict")
    for (engine, arm), record in sorted(seen.items()):
        where_row = f"fifo-surface {path}: {engine}/{arm}"
        if arm not in surface.ARM_EXPECTED:
            continue
        if record.get("expected_code") != surface.ARM_EXPECTED[arm]:
            problems.append(
                f"{where_row}: expected_code {record.get('expected_code')!r} "
                f"contradicts the committed table "
                f"({surface.ARM_EXPECTED[arm]!r}); rewriting the expectation "
                f"is how a wrong refusal is made to look right")
        if record.get("data_code") != record.get("expected_code"):
            problems.append(
                f"{where_row}: the engine answered {record.get('data_code')!r}"
                f" where the record's own expected_code is "
                f"{record.get('expected_code')!r}")
        if record.get("method") != surface.ARM_METHOD.get(arm):
            problems.append(
                f"{where_row}: the row records method "
                f"{record.get('method')!r}, not the "
                f"{surface.ARM_METHOD.get(arm)!r} the arm table sends")
        if record.get("kind") != "answered":
            problems.append(
                f"{where_row}: kind {record.get('kind')!r}; an arm that hung "
                f"or never reached the handler is not a refusal class")
        if record.get("transport_code") != surface.PRODUCT_ERROR:
            problems.append(
                f"{where_row}: transport_code "
                f"{record.get('transport_code')!r} is not the product error "
                f"{surface.PRODUCT_ERROR} the arm answers with")
    summary = report.get("summary")
    if not isinstance(summary, dict):
        problems.append(f"fifo-surface {path}: report records no summary")
    else:
        if summary.get("arms_expected") != len(expected_rows):
            problems.append(
                f"fifo-surface {path}: summary arms_expected "
                f"{summary.get('arms_expected')!r} contradicts the "
                f"{len(expected_rows)} rows the committed arm table requires "
                f"from both engines")
        if summary.get("failed") not in ([], None):
            problems.append(
                f"fifo-surface {path}: summary records failed arms "
                f"{summary.get('failed')}")
    controls = [record for record in (report.get("controls") or [])
                if isinstance(record, dict)]
    control_engines = sorted({record.get("engine") for record in controls})
    if control_engines != ["go", "rust"]:
        problems.append(
            f"fifo-surface {path}: control rows cover {control_engines}; "
            f"without a regular-file control per engine, a report cannot tell "
            f"a refused FIFO from a binary that refused everything")
    for record in controls:
        if record.get("answered_result") is not True:
            problems.append(
                f"fifo-surface {path}: {record.get('engine')} control did not "
                f"answer a regular file, so the refusals below it prove "
                f"nothing about FIFOs specifically")


def throughput_evidence(path, report, implementation_of, ledger, problems,
                        verify_binaries=False):
    """Re-validate the committed throughput report inside the kind gate.

    Throughput is an attestation, not a threshold: absolute rate depends on
    host load and on the rate window including process start, so the gate
    does not compare the number to any bar.  What it does enforce is that
    the number is arithmetic rather than prose.  Each round must serve every
    reply, each round's rate must follow from its own request count and
    elapsed seconds, the median must be the median of the rounds, and the
    thread census must be internally consistent.  Together these close the
    forged-rate class: an inflated ``median_replies_per_s`` with a
    hand-written round list is now a contradiction, and so is a thread census
    copied from a different run.
    """

    if report.get("schema") != "iprange-cli-throughput-report-v1":
        problems.append(f"throughput {path}: unexpected schema "
                        f"{report.get('schema')!r}")
    if report.get("result") != "PASS":
        problems.append(f"throughput {path}: result "
                        f"{report.get('result')!r}")
    if report.get("problems") not in ([], None):
        problems.append(f"throughput {path}: report carries problems "
                        f"{report.get('problems')!r}")
    product = report.get("product")
    if not isinstance(product, dict):
        problems.append(f"throughput {path}: no product table")
        return
    for engine in ("go", "rust"):
        record = product.get(engine)
        if not isinstance(record, dict):
            problems.append(f"throughput {path}: {engine} has no record")
            continue
        where = f"throughput {path}: {engine}"
        _surface_binary_identity(path, "throughput", engine, record,
                                 implementation_of, ledger, problems,
                                 on_disk=verify_binaries)
        rounds = record.get("rounds")
        if not isinstance(rounds, list) or not rounds:
            problems.append(f"{where}: no rounds measured")
            continue
        rates = []
        for entry in rounds:
            if not isinstance(entry, dict):
                problems.append(f"{where}: round record is not an object")
                continue
            requests = entry.get("requests")
            replies = entry.get("replies")
            seconds = entry.get("seconds")
            rate = entry.get("replies_per_s")
            if replies != requests:
                problems.append(
                    f"{where}: round {entry.get('round')} served {replies} "
                    f"of {requests} replies; a rate on dropped replies is "
                    f"not a rate")
                continue
            if not isinstance(seconds, (int, float)) or seconds <= 0:
                problems.append(
                    f"{where}: round {entry.get('round')} has no elapsed "
                    f"time, so its rate is unbacked")
                continue
            if not isinstance(rate, (int, float)) or rate <= 0:
                problems.append(
                    f"{where}: round {entry.get('round')} has no positive rate")
                continue
            implied = requests / float(seconds)
            if abs(implied - rate) > max(1.0, 0.02 * implied):
                problems.append(
                    f"{where}: round {entry.get('round')} reports "
                    f"{rate} replies/s but {requests} requests in "
                    f"{seconds} s is {implied:.1f} replies/s; the census and "
                    f"the rate contradict each other")
                continue
            rates.append(rate)
        if not rates:
            problems.append(f"{where}: no round carries usable measurements")
            return
        median = record.get("median_replies_per_s")
        ordered = sorted(rates)
        middle = len(ordered) // 2
        expected = (ordered[middle] if len(ordered) % 2
                    else (ordered[middle - 1] + ordered[middle]) / 2.0)
        if not isinstance(median, (int, float)) \
                or abs(median - expected) > max(1.0, 0.02 * expected):
            problems.append(
                f"{where}: median_replies_per_s {median!r} is not the median "
                f"of its own rounds ({expected:.1f}); a rate is derived "
                f"arithmetic, not a claimed number")
        _reply_classification_problems(where, record.get("rounds"), problems)
        _method_agreement_problems(where, report.get("method"), record,
                                   problems)
        structure = record.get("thread_structure")
        if structure is None:
            problems.append(
                f"{where}: no thread census; the per-reply-spawn regression "
                f"this attestation exists to catch would go undetected")
            continue
        small = structure.get("small")
        large = structure.get("large")
        if not isinstance(small, dict) or not isinstance(large, dict):
            problems.append(f"{where}: thread census is incomplete")
            continue
        _thread_census_problems(where, report.get("method"), small, large,
                                problems)
        for side_name, side in (("small", small), ("large", large)):
            if side.get("replies") != side.get("requests"):
                problems.append(
                    f"{where}: {side_name} strace pass served "
                    f"{side.get('replies')} of {side.get('requests')}")
            clones = side.get("clone_syscalls")
            tids = side.get("unique_child_tids")
            if not isinstance(clones, int) or not isinstance(tids, int) \
                    or clones < 0 or tids < 0:
                problems.append(
                    f"{where}: {side_name} census is not measured counts")
            elif clones > 0 and tids == 0:
                problems.append(
                    f"{where}: {side_name} census records {clones} clones and "
                    f"0 task ids; the census parsed nothing")
            _reply_classification_problems(f"{where} {side_name} census",
                                           [side], problems)

# ---------------------------------------------------------------------------
# Consumed reports beyond the matrix and crash pair.
#
# Each report class below is produced by its own harness and publishes a
# verdict or a measurement.  A verdict that no gate reads is a claim, not
# evidence: at the wave-19.24 revision each of these was accepted with
# fabricated fields, because nothing compared the report with the artifacts
# the kind gate already trusts.  Every rule here re-derives, from the
# matrix/crash identities and from the harnesses' own committed tables, what
# the report says happened.
#
# Table authority.  The refusal-class grid, the pinned refusals, the FIFO arm
# table and the throughput census constants belong to
# ``check_refusal_class_parity``, ``check_fifo_surface`` and
# ``throughput_harness``.  They are imported, never copied: a second copy of
# a table can only drift, and a table consulted from one place moves with the
# harness that owns it.
# ---------------------------------------------------------------------------

REFUSAL_PARITY_SCHEMA = "iprange-cli-refusal-class-parity-report-v1"
GO_COVERAGE_SCHEMA = "iprange-cli-coverage-go-report-v1"
WINDOWS_HOUSEKEEPING_SCHEMA = "iprange-cli-windows-housekeeping-report-v3"
CRASH_REPORT_SCHEMA = "iprange-cli-crash-report-v1"
BATTERY_MANIFEST_SCHEMA = "iprange-cli-battery-manifest-v1"
BATTERY_MANIFEST_FILE_NAME = "battery-manifest.json"

# Report classes the gate consumes besides ``--matrix``/``--crash``.  Each
# name is also a battery-manifest role, so a class that is neither supplied
# nor discovered is a missing role rather than a skipped check.
CONSUMED_ROLES = ("matrix", "crash", "crash-negative", "fifo-surface",
                  "throughput", "refusal-class-parity", "coverage-go",
                  "windows-housekeeping")

# Standard file names, used when a class is discovered beside the supplied
# battery instead of being named on the command line.
CONSUMED_FILE_NAMES = {
    "refusal-class-parity": ("refusal-class-parity.json",),
    "coverage-go": ("coverage-go.json",),
    "crash-negative": ("crash-negative.json",),
    "windows-housekeeping": ("windows-housekeeping.json",),
}

# A negative control is one report per faked role, so a battery keeps more
# than one file for the role; they are found by name prefix.
CONSUMED_FILE_PREFIXES = {"crash-negative": ("crash-negative",)}

# The durability stages that run after the destination name exists.  A
# publication fact block may claim the destination is visible only from one
# of these, because before the rename there is nothing to see and the honest
# answer is a definite refusal carrying no publication facts.
POST_VISIBILITY_STAGE_PREFIX = "sync"
PUBLICATION_FACT_CONTAINERS = ("publication", "removals_publication_failure")


COMMITTED_EVIDENCE_DIR = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "evidence")


def _wave_ledger_path():
    """The staged-artifact ledger of this checkout, when binaries were staged.

    The committed battery manifest binds the ledger the evidence was staged
    against, so any consumer that judges the committed reports has to be
    handed the same ledger -- including ``--self-test``, whose
    genuine-evidence checks are the proof that the real battery still passes.
    The ledger lives outside the repository (``.local/`` is ignored), so it is
    also honoured from ``IPRANGE_SHA256_LEDGER`` for a reviewer who staged it
    elsewhere.
    """

    root = os.path.dirname(os.path.dirname(
        os.path.dirname(os.path.abspath(__file__))))
    for candidate in (os.environ.get("IPRANGE_SHA256_LEDGER"),
                      os.path.join(root, ".local", "shared", "binaries",
                                   "SHASUMS.txt")):
        if candidate and os.path.isfile(candidate):
            return candidate
    return None


def _all_digests(document):
    """Every sha256-shaped token in one report, wherever it appears.

    Used to ask whether a build identity shows up in a report that must not
    name it, so the question is about the document rather than about the
    handful of fields a checker happens to know.
    """

    return set(re.findall(r"\b[0-9a-f]{64}\b",
                          json.dumps(document, sort_keys=True)))


def _resolve_consumed(paths, role, anchor_paths, problems):
    """Find the reports of one consumed class that nobody named.

    Resolution order is explicit flag, then the directory the supplied
    battery lives in, then this gate's committed evidence.  The point of
    doing it in the gate rather than the shell is that an omitted flag is as
    cheap as a deleted field: a consumed class may be missing, but it may not
    be quietly unexamined, so a class that cannot be found at all is recorded
    as a problem.
    """

    if paths:
        return list(paths)
    directories = []
    for path in anchor_paths:
        parent = os.path.dirname(os.path.abspath(path))
        if parent not in directories:
            directories.append(parent)
    if COMMITTED_EVIDENCE_DIR not in directories:
        directories.append(COMMITTED_EVIDENCE_DIR)
    names = list(CONSUMED_FILE_NAMES.get(role) or ())
    prefixes = list(CONSUMED_FILE_PREFIXES.get(role) or ())
    for directory in directories:
        if not os.path.isdir(directory):
            continue
        if prefixes:
            found = sorted(os.path.join(directory, name)
                           for name in os.listdir(directory)
                           if any(name.startswith(prefix)
                                  and name.endswith(".json")
                                  for prefix in prefixes))
            if found:
                return found
        found = [os.path.join(directory, name) for name in names
                 if os.path.isfile(os.path.join(directory, name))]
        if found:
            return found
    problems.append(
        f"no {role} report is supplied or present beside the battery "
        f"(looked for {', '.join(names) or ' / '.join(prefixes)} in "
        f"{', '.join(directories)}); {role} is consumed evidence, so a "
        f"battery without it does not prove the verdict it claims")
    return []


def _is_sha256(value):
    """True when ``value`` has the wire form of a measured sha256 digest."""

    return (isinstance(value, str) and len(value) == 64
            and all(character in "0123456789abcdef" for character in value))


def _accepts_keyword(function, name):
    """True when ``function`` can be called with the keyword ``name``.

    The gate calls the parity harness's own verifier, and that verifier takes
    the staged ledger only in newer revisions of the harness.  Probing the
    signature is how the gate keeps working across the two without swallowing
    a real TypeError raised from inside the verifier.
    """

    code = getattr(function, "__code__", None)
    if code is None:
        return False
    if name in code.co_varnames[:code.co_argcount]:
        return True
    return bool(code.co_flags & 0x08)  # **kwargs


def _canonical_digest(document):
    """Digest of a report's *content*, independent of how it was written.

    The battery re-serializes reports into its sandbox, so a file-byte digest
    would change while the evidence stayed the same.  The manifest binds what
    a report says, so it digests the canonical JSON form: sorted keys, no
    insignificant whitespace.
    """

    payload = json.dumps(document, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def _table_module(name, required, where, problems):
    """Import one harness module for its committed tables, or fail closed.

    ``required`` names the members the gate derives expectations from.  An
    import failure or a missing member is a gate problem rather than a
    skipped check: without the table the gate cannot tell a complete battery
    from a shrunken one, and silence is the bypass these rules exist to close.
    """

    try:
        module = importlib.import_module(name)
    except Exception as exc:  # noqa: BLE001 - report, never crash
        problems.append(f"{where}: cannot import {name} for its committed "
                        f"tables ({type(exc).__name__}: {exc}); the "
                        f"expectations this report is measured against cannot "
                        f"be derived")
        return None
    missing = [member for member in required if not hasattr(module, member)]
    if missing:
        problems.append(f"{where}: {name} no longer declares {missing}; the "
                        f"gate derives these expectations from the harness "
                        f"that owns them instead of keeping a second copy")
        return None
    return module


def refusal_class_parity_evidence(path, report, implementation_of, ledger,
                                  problems, fixture_shas=None,
                                  verify_binaries=False):
    """Consume the refusal-class parity verdict inside the kind gate.

    The parity harness owns the grid and the pinned refusals, so this
    function does not restate them.  It adds what the generator's own
    verifier cannot see:

    * identity.  The two engine records must be two different artifacts, and
      each digest must be the ledger entry for its own implementation label.
      A sweep that ran one executable twice -- ``--go <rust binary>`` --
      reports agreement between a binary and itself, and no cell content can
      expose it; the ledger label and the digest inequality can.
    * the verdict actually claimed.  ``result`` must be PASS and the
      divergence, hang, flake and pin counters must all say the sweep
      passed, which is the difference between an honest report about a
      failing battery and a report written after the fact.
    * publication facts as evidence.  A fact block that claims an unknown
      publication outcome must be backed by the evidence members the engines
      publish, by a digest for the artifact, and by a stage that runs after
      the destination name can exist.

    The generator's verifier is run over the report as well: a report the
    generator itself would reject is a defect here too.
    """

    where = f"refusal-class-parity {path}"
    if report.get("schema") != REFUSAL_PARITY_SCHEMA:
        problems.append(f"{where}: unexpected schema "
                        f"{report.get('schema')!r}")
    if report.get("result") != "PASS":
        problems.append(f"{where}: result {report.get('result')!r}; a parity "
                        f"sweep that did not pass cannot be consumed as "
                        f"parity evidence")
    parity = _table_module(
        "check_refusal_class_parity",
        ("ARM_NAMES", "kind_names", "MANDATORY_PATH_KINDS", "PINNED_REFUSALS",
         "pin_expectation", "expected_cells", "REQUIRED_PUBLICATION_FACTS",
         "assess_report"),
        where, problems)
    if parity is None:
        return

    binaries = report.get("binaries")
    if not isinstance(binaries, dict):
        problems.append(f"{where}: no binaries table to bind the verdict to")
        return
    for engine in ("go", "rust"):
        _surface_binary_identity(path, "refusal-class-parity", engine,
                                 binaries.get(engine), implementation_of,
                                 ledger, problems, on_disk=verify_binaries)
    go_digest = (binaries.get("go") or {}).get("sha256")
    rust_digest = (binaries.get("rust") or {}).get("sha256")
    if _is_sha256(go_digest) and go_digest == rust_digest:
        problems.append(
            f"{where}: the go and rust records name the same binary "
            f"{go_digest}; a sweep that ran one executable twice and reported "
            f"an agreement is not a parity observation")
    if ledger is not None:
        for engine in ("go", "rust"):
            digest = (binaries.get(engine) or {}).get("sha256")
            if not _is_sha256(digest):
                continue
            staged = ledger.get(digest) or set()
            if not any(entry.startswith(f"{engine}/") for entry in staged):
                problems.append(
                    f"{where}: {engine} sha256 {digest} is staged as "
                    f"{sorted(staged) or ['<not staged>']}, not under "
                    f"{engine!r}; the ledger says which implementation a "
                    f"digest is, and the verdict must name the same one")
    fixture = binaries.get("fixture_tool")
    if not isinstance(fixture, dict):
        problems.append(
            f"{where}: the sweep records no fixture_tool identity, so the "
            f"material every cell was built from is unaccounted for")
    else:
        digest = fixture.get("sha256")
        if not _is_sha256(digest):
            problems.append(f"{where}: fixture_tool sha256 {digest!r} is not "
                            f"a measured digest")
        elif fixture_shas and digest not in fixture_shas:
            problems.append(
                f"{where}: fixture_tool sha256 {digest} is not the fixture "
                f"identity the battery's crash report records; the cells were "
                f"built from material the battery cannot account for")

    expected = set(parity.expected_cells())
    cells = [cell for cell in (report.get("cells") or [])
             if isinstance(cell, dict)]
    observed = {}
    for index, cell in enumerate(cells):
        key = (cell.get("arm"), cell.get("path_kind"))
        if key in observed:
            problems.append(f"{where}: cells[{index}] repeats cell {key!r}; "
                            f"the later record would outrank the first")
            continue
        observed[key] = cell
    for key in sorted(set(observed) - expected):
        problems.append(f"{where}: cell {key!r} is outside the grid the "
                        f"generator derives; an invented cell is not "
                        f"evidence")
    absent = sorted(expected - set(observed))
    if absent:
        shown = ", ".join(f"{arm}/{kind_name}" for arm, kind_name in absent[:6])
        problems.append(
            f"{where}: {len(absent)} of {len(expected)} grid cells were never "
            f"executed (for example: {shown}); the grid is derived from the "
            f"committed arm and path-kind tables, so a verdict over a "
            f"shrunken sweep is not a parity verdict")
    grid = report.get("grid")
    if not isinstance(grid, dict):
        problems.append(f"{where}: report records no grid block")
    else:
        if grid.get("arms") != list(parity.ARM_NAMES):
            problems.append(
                f"{where}: grid arms {grid.get('arms')!r} differ from the "
                f"committed arm table {list(parity.ARM_NAMES)!r}")
        if grid.get("path_kinds") != parity.kind_names():
            problems.append(
                f"{where}: grid path kinds {grid.get('path_kinds')!r} differ "
                f"from the committed path-kind table {parity.kind_names()!r}")
        if list(grid.get("mandatory_path_kinds") or []) \
                != list(parity.MANDATORY_PATH_KINDS):
            problems.append(
                f"{where}: grid mandatory path kinds "
                f"{grid.get('mandatory_path_kinds')!r} differ from the "
                f"committed obligations "
                f"{list(parity.MANDATORY_PATH_KINDS)!r}")
        if grid.get("cells_expected") != len(expected):
            problems.append(
                f"{where}: grid cells_expected {grid.get('cells_expected')!r} "
                f"contradicts the derived grid of {len(expected)} cells")

    summary = report.get("summary")
    if not isinstance(summary, dict):
        problems.append(f"{where}: report records no summary")
    else:
        if summary.get("cells_executed") != len(cells):
            problems.append(
                f"{where}: summary cells_executed "
                f"{summary.get('cells_executed')!r} contradicts the "
                f"{len(cells)} cell records")
        if summary.get("cells_expected") != len(expected):
            problems.append(
                f"{where}: summary cells_expected "
                f"{summary.get('cells_expected')!r} contradicts the derived "
                f"grid of {len(expected)} cells")
        for counter in ("divergences", "hangs", "flaky"):
            if summary.get(counter) != 0:
                problems.append(
                    f"{where}: summary {counter} is "
                    f"{summary.get(counter)!r}; a consumed parity verdict "
                    f"requires zero of them")
        if report.get("divergences"):
            problems.append(
                f"{where}: report carries {len(report['divergences'])} "
                f"divergence record(s) while claiming a PASS verdict")
        try:
            accounted = sum(int(summary.get(counter) or 0)
                            for counter in ("agreements", "divergences",
                                            "hangs"))
        except (TypeError, ValueError):
            accounted = -1
        if accounted != int(summary.get("cells_executed") or 0):
            problems.append(
                f"{where}: agreements+divergences+hangs is {accounted} but "
                f"{summary.get('cells_executed')} cells were executed; every "
                f"cell must be classified as one of them")

    pins = [pin for pin in (report.get("pins") or []) if isinstance(pin, dict)]
    pinned_keys = set(parity.PINNED_REFUSALS)
    if not pins:
        problems.append(f"{where}: report records no pins; the "
                        f"{len(pinned_keys)}-cell pinned-refusal table is an "
                        f"obligation, not a suggestion")
    if len(pins) != len(pinned_keys):
        problems.append(f"{where}: report records {len(pins)} pin(s) but the "
                        f"committed table pins {len(pinned_keys)} cells")
    if summary.get("pins_expected") != len(pinned_keys):
        problems.append(
            f"{where}: summary pins_expected {summary.get('pins_expected')!r} "
            f"contradicts the committed pinned-refusal table of "
            f"{len(pinned_keys)} cells")
    if summary.get("pins_satisfied") != summary.get("pins_expected"):
        problems.append(
            f"{where}: pinned refusals {summary.get('pins_satisfied')} of "
            f"{summary.get('pins_expected')} satisfied; a consumed verdict "
            f"must have every one of them")
    for pin in pins:
        key = (pin.get("arm"), pin.get("path_kind"))
        if key not in pinned_keys:
            continue
        want = parity.pin_expectation(parity.PINNED_REFUSALS[key])
        recorded = (pin.get("expected_data_code"), pin.get("expected_outcome"),
                    pin.get("expected_facts"))
        if recorded != want:
            problems.append(
                f"{where}: pin {key!r} records expectation {recorded!r} but "
                f"the committed table pins {want!r}; rewriting the expectation"
                f" is how a wrong answer is made to look right")
        if pin.get("executed") is not True:
            problems.append(f"{where}: pin {key!r} records executed "
                            f"{pin.get('executed')!r} although its cell is in "
                            f"the executed set")

    required_facts = parity.REQUIRED_PUBLICATION_FACTS
    for key, cell in observed.items():
        for engine in ("go", "rust"):
            record = cell.get(engine)
            if not isinstance(record, dict):
                continue
            _publication_fact_problems(f"{where}: cell {key[0]}/{key[1]} "
                                       f"{engine}", record, required_facts,
                                       problems)

    # The pinned-refusal table is the obligation the verdict is measured
    # against, and the harness publishes its fingerprint.  The two halves are
    # bound in whichever direction the checked-out revision supports: a report
    # that names a fingerprint is checked against it, and a harness that
    # publishes one requires every report to carry it.  Leaving the pairing
    # optional in both directions is how the binding would silently stop being
    # enforced the moment one side stopped recording it.
    fingerprint = getattr(parity, "pinned_table_fingerprint", None)
    recorded_table = (grid or {}).get("pinned_table_sha256") \
        if isinstance(grid, dict) else None
    if callable(fingerprint) and recorded_table is None:
        problems.append(
            f"{where}: the report records no pinned_table_sha256, although "
            f"the parity harness publishes a fingerprint of the table the "
            f"verdict was swept against")
    elif recorded_table is not None:
        if not callable(fingerprint):
            problems.append(
                f"{where}: the report binds a pinned-refusal table digest, "
                f"but this revision of the parity harness publishes no "
                f"fingerprint to check it against")
        elif recorded_table != fingerprint():
            problems.append(
                f"{where}: the report records a pinned-refusal table digest "
                f"other than the committed table")
    try:
        # The same ledger the gate was handed, where the verifier takes it.
        if _accepts_keyword(parity.assess_report, "sha256_ledger"):
            generator_problems = parity.assess_report(
                report, sha256_ledger=ledger)
        else:
            generator_problems = parity.assess_report(report)
    except Exception as exc:  # noqa: BLE001 - a verifier must not crash us
        problems.append(f"{where}: the parity gate's own verifier raised "
                        f"{type(exc).__name__}: {exc}")
        generator_problems = None
    for problem in generator_problems or []:
        text = f"{where}: parity gate: {problem}"
        if text not in problems:
            problems.append(text)


def _publication_fact_problems(where, record, required_facts, problems):
    """Require one engine record's publication claim to be backed by data."""

    facts = record.get("publication_facts")
    evidence = record.get("publication_evidence")
    if facts is None and evidence is None:
        return
    if not isinstance(facts, dict) or not isinstance(evidence, dict):
        problems.append(
            f"{where}: records publication facts "
            f"{'without its fact block' if isinstance(facts, dict) else 'without its evidence'};"
            f" the claim and its evidence are one fact and must appear "
            f"together")
        return
    absent_members = [member for member in required_facts
                      if member not in evidence]
    if absent_members:
        problems.append(
            f"{where}: publication_evidence omits {absent_members}; a reply "
            f"that claims an unknown publication outcome must say on what "
            f"evidence")
        return
    if not _is_sha256(evidence.get("sha256")):
        problems.append(
            f"{where}: publication_evidence sha256 "
            f"{evidence.get('sha256')!r} is not a digest; the artifact the "
            f"engine says it published has no identity")
    stage = evidence.get("stage")
    if not isinstance(stage, str) or not stage.strip():
        problems.append(f"{where}: publication_evidence records no stage, so "
                        f"what the engine had already made visible cannot be "
                        f"judged")
        return
    visible = evidence.get("destination_visible")
    after_visibility = stage.strip().lower().startswith(
        POST_VISIBILITY_STAGE_PREFIX)
    if visible is True and not after_visibility:
        problems.append(
            f"{where}: destination_visible is true at stage {stage!r}, which "
            f"runs before the destination name exists; a destination that was "
            f"never created cannot have been made visible")
    if visible is not True and after_visibility:
        problems.append(
            f"{where}: stage {stage!r} runs after the destination name exists"
            f" but destination_visible is {visible!r}; the two halves of one "
            f"durability fact contradict each other")
    if facts.get("destination_visible") is not True:
        problems.append(
            f"{where}: the fact block records destination_visible "
            f"{facts.get('destination_visible')!r} beside evidence whose "
            f"shape is the post-visibility one")
    if facts.get("container") not in PUBLICATION_FACT_CONTAINERS:
        problems.append(
            f"{where}: facts container {facts.get('container')!r} is not one "
            f"of {list(PUBLICATION_FACT_CONTAINERS)}; the engine carries the "
            f"facts in one of those two reply members")
    if facts.get("sha256_well_formed") is True \
            and not _is_sha256(evidence.get("sha256")):
        problems.append(f"{where}: the fact block claims a well-formed sha256"
                        f" the evidence does not carry")
    if facts.get("outcome_unknown") is True \
            and evidence.get("outcome") != "outcome_unknown":
        problems.append(
            f"{where}: the fact block claims outcome_unknown while the "
            f"evidence outcome is {evidence.get('outcome')!r}")
    if facts.get("temporary_removed") != evidence.get("temporary_removed"):
        problems.append(
            f"{where}: facts temporary_removed {facts.get('temporary_removed')!r}"
            f" contradicts the evidence "
            f"{evidence.get('temporary_removed')!r}")


def coverage_evidence(path, report, attested_digests, problems):
    """Consume the Go coverage measurement inside the kind gate.

    Two arithmetic rules, because a coverage report is only evidence if its
    numbers are derived rather than typed:

    * every percentage must follow from the counts shipped beside it, in the
      aggregates and in every per-package line; and
    * the instrumented build must stay out of every report that quotes a
      rate or a verdict.  Instrumenting changes the binary, so an
      instrumented digest appearing as a throughput or surface artifact means
      the attestation measured a build that is not the one that shipped --
      and the report's own policy sentence promising exactly the opposite is
      then prose rather than a rule.
    """

    where = f"coverage-go {path}"
    if report.get("schema") != GO_COVERAGE_SCHEMA:
        problems.append(f"{where}: unexpected schema "
                        f"{report.get('schema')!r}")
    instrumented = report.get("instrumented_binaries")
    if not isinstance(instrumented, dict) or not instrumented:
        problems.append(
            f"{where}: report records no instrumented_binaries; without the "
            f"identity of the build that produced the counters, the "
            f"percentages are unsourced")
        instrumented = {}
    for name, record in sorted(instrumented.items()):
        if not isinstance(record, dict):
            problems.append(f"{where}: instrumented binary {name!r} is not an "
                            f"object")
            continue
        if record.get("instrumented") is not True:
            problems.append(
                f"{where}: instrumented binary {name!r} records instrumented "
                f"{record.get('instrumented')!r}; a coverage report about an "
                f"uninstrumented build measured nothing")
        if not isinstance(record.get("covermode"), str) \
                or not record["covermode"]:
            problems.append(f"{where}: instrumented binary {name!r} records "
                            f"no covermode")
        digest = record.get("sha256")
        if not _is_sha256(digest):
            problems.append(f"{where}: instrumented binary {name!r} sha256 "
                            f"{digest!r} is not a measured digest")
            continue
        if digest in attested_digests:
            problems.append(
                f"{where}: instrumented binary {name!r} sha256 {digest} is "
                f"also named by a throughput or verdict-bearing report; the "
                f"coverage build is instrumented and may not attest rate or "
                f"behavior")
    for section in ("unit", "integration", "merged"):
        block = report.get(section)
        if not isinstance(block, dict):
            problems.append(f"{where}: report records no {section} block")
            continue
        _rederive_percentages(where, section, block.get("measured"),
                              block.get("percent"), problems)
        if block.get("percent") is not None \
                and block["percent"].get("measured") != block.get("measured"):
            problems.append(
                f"{where}: {section} percent.measured does not echo the "
                f"measured counts; the profile a percentage was computed from"
                f" is not the profile the report publishes")
    packages = report.get("per_package")
    if not isinstance(packages, dict) or not packages:
        problems.append(f"{where}: report records no per_package counters; an"
                        f" aggregate alone cannot be re-derived")
    else:
        for name in sorted(packages):
            entry = packages[name]
            if not isinstance(entry, dict):
                problems.append(f"{where}: per_package {name!r} is not an "
                                f"object")
                continue
            _rederive_percentages(
                where, f"per_package {name!r}", entry, entry, problems,
                triples=(("statement_percent", "covered_statements",
                          ("statements_total", "statements")),
                         ("function_percent", "covered_functions",
                          ("functions",)),
                         ("block_percent", "blocks_covered", ("blocks",))))
    policy = report.get("policy")
    if not isinstance(policy, dict) \
            or not isinstance(policy.get("performance_use"), str) \
            or not policy["performance_use"]:
        problems.append(
            f"{where}: report records no performance_use policy; the rule "
            f"that keeps instrumented builds out of the attestations is part "
            f"of the measurement contract, and it is checked above")


def _rederive_percentages(where, label, measured, percent, problems,
                          triples=None):
    """Require each recorded percentage to follow from its own counts."""

    if not isinstance(measured, dict) or not isinstance(percent, dict):
        problems.append(f"{where}: {label} records no measured/percent pair")
        return
    if triples is None:
        triples = (("statements", "covered_statements",
                    ("statements",)),
                   ("functions", "covered_functions", ("functions",)),
                   ("blocks", "blocks_covered", ("blocks",)))
    for percent_key, covered_key, total_keys in triples:
        covered = measured.get(covered_key)
        total = next((measured[key] for key in total_keys
                      if isinstance(measured.get(key), int)), None)
        recorded = percent.get(percent_key)
        if not isinstance(covered, int) or not isinstance(total, int):
            problems.append(
                f"{where}: {label} has no integer counts behind "
                f"{covered_key}/{'/'.join(total_keys)} for {percent_key}; a "
                f"percentage without its counts cannot be re-derived")
            continue
        if covered < 0 or total < 0 or covered > total:
            problems.append(
                f"{where}: {label} records {covered} covered of {total} total"
                f" for {percent_key}")
            continue
        implied = round(100.0 * covered / total, 2) if total else 0.0
        if not isinstance(recorded, (int, float)) \
                or abs(float(recorded) - implied) > 0.005:
            problems.append(
                f"{where}: {label} {percent_key} records {recorded!r} but "
                f"{covered} of {total} is {implied}; a coverage percentage "
                f"must be the arithmetic of the counts it ships with")


def crash_negative_evidence(path, report, implementation_of, problems):
    """Consume one ``/bin/false`` crash control report.

    The negative control exists to prove the crash battery can see a failure:
    a real engine runs against a peer that answers nothing, and every
    scenario must FAIL.  A PASS here is the finding, so these rules are the
    mirror image of the positive crash consumption -- and they have to be
    enforced somewhere, because a report nobody reads cannot even be asked
    which role was the faked one.

    Returns the number of rejected scenarios it accepted as evidence.
    """

    where = f"crash-negative {path}"
    if report.get("schema") != CRASH_REPORT_SCHEMA:
        problems.append(f"{where}: unexpected schema "
                        f"{report.get('schema')!r}")
    scenarios = [entry for entry in (report.get("scenarios") or [])
                 if isinstance(entry, dict)]
    if not scenarios:
        problems.append(f"{where}: a negative control with no scenarios "
                        f"proved nothing can fail")
        return 0
    accepted = [entry.get("scenario") for entry in scenarios
                if entry.get("pass") is True]
    if accepted:
        problems.append(
            f"{where}: {len(accepted)} scenario(s) PASS in a negative control"
            f" (for example {accepted[:3]}); with the false peer every "
            f"scenario must fail, so an accepted scenario is either a control"
            f" that silently became a positive run or a report that stopped "
            f"testing what its name claims")
    if report.get("failed") != len(scenarios):
        problems.append(
            f"{where}: report records failed={report.get('failed')!r} for "
            f"{len(scenarios)} scenarios; the expected outcome of a negative "
            f"control is total rejection")
    for entry in scenarios:
        if entry.get("pass") is True:
            continue
        failures = entry.get("failures")
        if not (isinstance(failures, list) and failures
                and all(isinstance(text, str) and text.strip()
                        for text in failures)):
            problems.append(
                f"{where}: scenario {entry.get('scenario')!r} is not pass but"
                f" records no failure reason ({failures!r}); an unexplained "
                f"failure is as unusable as an unexplained pass")
    if report.get("leftover_processes"):
        problems.append(f"{where}: report records leftover product processes "
                        f"{report.get('leftover_processes')}")
    binaries = report.get("binaries")
    if not isinstance(binaries, dict):
        problems.append(f"{where}: no binaries table, so the report cannot say"
                        f" which role was the false peer")
        return len(scenarios)
    false_roles = [role for role in ("producer", "consumer")
                   if isinstance(binaries.get(role), str)
                   and os.path.basename(binaries[role]) == "false"]
    if len(false_roles) != 1:
        problems.append(
            f"{where}: exactly one role must be the /bin/false peer, found "
            f"{false_roles or ['<none>']} among "
            f"{[binaries.get('producer'), binaries.get('consumer')]}; a "
            f"negative control that does not identify what it faked is not a "
            f"control")
        return len(scenarios)
    false_role = false_roles[0]
    real_role = "consumer" if false_role == "producer" else "producer"
    real_sha = binaries.get(real_role + "_sha256")
    if implementation_of.get(real_sha) not in PRODUCT_LANGUAGES:
        problems.append(
            f"{where}: the {real_role} binary {real_sha!r} does not resolve "
            f"through the battery's executed identities to a product "
            f"language; the real half of a negative control must be a real "
            f"engine")
    false_sha = binaries.get(false_role + "_sha256")
    if not _is_sha256(false_sha):
        problems.append(
            f"{where}: {false_role} sha256 {false_sha!r} is not a measured "
            f"digest; the peer that was faked has to be identified too")
    argv = _command_argv(report)
    if argv is None:
        problems.append(f"{where}: report records no command argv")
    else:
        flag = "--producer" if false_role == "producer" else "--consumer"
        named = None
        if flag in argv and argv.index(flag) + 1 < len(argv):
            named = argv[argv.index(flag) + 1]
        if named != binaries.get(false_role):
            problems.append(
                f"{where}: the recorded command names {named!r} for {flag} "
                f"but the binaries table records {binaries.get(false_role)!r}"
                f" for the false role; the run and the report must describe "
                f"the same control")
    return len(scenarios)


def windows_provenance_evidence(path, report, ledger, problems,
                                linux_digests=()):
    """Consume the native Windows qualification report.

    Windows evidence cannot be re-executed on this machine, which is exactly
    why it is consumed rather than believed: the report is the only bridge
    between the shipped binaries and the claim that they were tested natively.
    Three anchors carry weight.

    * The native test records must name a positive verdict and must not name
      a failure.  The scan is for failure *tokens and counts*, not the
      substring ``fail``: an honest record explains which portability
      failures it resolved, and a substring screen would reject the real
      report while accepting a bare ``GREEN``.
    * The toolchain lines must describe a Windows host, because a record
      produced anywhere else cannot qualify Windows.
    * The binaries the outcomes name must be the ledger's ``win`` entries and
      must not be the Linux product digests, so the report cannot borrow the
      Linux build to look qualified.
    """

    where = f"windows {path}"
    if report.get("schema") != WINDOWS_HOUSEKEEPING_SCHEMA:
        problems.append(f"{where}: unexpected schema "
                        f"{report.get('schema')!r}")
    if report.get("windows_qualified") is not True:
        problems.append(f"{where}: windows_qualified "
                        f"{report.get('windows_qualified')!r}")
    if report.get("skipped") is not False:
        problems.append(f"{where}: skipped {report.get('skipped')!r}; a "
                        f"skipped Windows qualification is not a qualification")
    if report.get("failed") != 0:
        problems.append(f"{where}: report records failed="
                        f"{report.get('failed')!r}")
    if not isinstance(report.get("outcomes"), list) or not report["outcomes"]:
        problems.append(f"{where}: no outcomes; the qualification ran nothing")
    platform = report.get("platform")
    if not isinstance(platform, dict) or platform.get("system") != "Windows":
        problems.append(f"{where}: platform {platform!r} does not name a "
                        f"Windows host")
    provenance = report.get("build_provenance")
    if not isinstance(provenance, dict):
        problems.append(f"{where}: no build_provenance, so the qualification "
                        f"cannot say what it built or how it tested natively")
        return
    for field in ("native_go_test", "native_cargo_test"):
        _native_test_record_problems(where, field, provenance.get(field),
                                     problems)
    toolchain = provenance.get("toolchain")
    if not isinstance(toolchain, dict):
        problems.append(f"{where}: build_provenance records no toolchain")
    else:
        for member, pattern, meaning in (
                ("go", r"^go version go1\.\d+(\.\d+)? windows/(amd64|arm64)$",
                 "a Windows Go toolchain"),
                ("rustc",
                 r"\brustc\s+\d+\.\d+(\.\d+)?\b.*host "
                 r"[A-Za-z0-9_-]+-pc-windows-msvc",
                 "a windows-msvc Rust host")):
            value = toolchain.get(member)
            if not isinstance(value, str) \
                    or not re.search(pattern, value.strip()):
                problems.append(
                    f"{where}: toolchain {member} {value!r} does not name "
                    f"{meaning}; native Windows evidence cannot come from a "
                    f"record made on another host")
        host_triple = toolchain.get("host_triple")
        if not isinstance(host_triple, str) \
                or "windows-msvc" not in host_triple:
            problems.append(f"{where}: toolchain host_triple "
                            f"{host_triple!r} is not a windows-msvc host")
    commands = provenance.get("build_commands")
    if not isinstance(commands, list) or not commands:
        problems.append(f"{where}: build_provenance records no build commands")
    else:
        text = "\n".join(str(item) for item in commands)
        if "debug" not in text or "iprange-v4-worker" not in text:
            problems.append(
                f"{where}: build_commands do not include the worker "
                f"colocation step; without it the Rust tests that shell out "
                f"to the worker answer from a missing executable, and the "
                f"recorded tally then describes a different run")
    binaries = report.get("binaries")
    if not isinstance(binaries, dict):
        problems.append(f"{where}: no binaries table to bind to the ledger")
        return
    for engine in ("go", "rust"):
        record = binaries.get(engine)
        if not isinstance(record, dict):
            problems.append(f"{where}: no {engine} binary record")
            continue
        digest = record.get("sha256")
        if not _is_sha256(digest):
            problems.append(f"{where}: {engine} sha256 {digest!r} is not a "
                            f"measured digest")
            continue
        if digest in linux_digests:
            problems.append(
                f"{where}: {engine} sha256 {digest} is a binary the Linux "
                f"battery executed; the Windows product has its own artifact "
                f"and a qualification that names the other one did not build"
                f" for Windows")
            continue
        if ledger is None:
            continue
        staged = ledger.get(digest) or set()
        wanted = os.path.basename(str(record.get("path") or ""))
        if not any(entry.startswith("win/") for entry in staged):
            problems.append(
                f"{where}: {engine} sha256 {digest} is staged as "
                f"{sorted(staged) or ['<not staged>']}, not as a win entry; "
                f"the Windows product must be the artifact the ledger staged "
                f"for Windows")
        elif wanted and not any(entry.endswith(wanted) for entry in staged):
            problems.append(
                f"{where}: {engine} sha256 {digest} is not staged under the "
                f"name the report records ({wanted})")


def _native_test_record_problems(where, field, value, problems):
    """Require one native test record to state a positive verdict.

    Acceptance needs a verdict token.  Rejection looks for the shapes a real
    failure takes -- an upper-case FAIL/FAILED/RED token, a non-zero count
    before failed/failing/failures, or a non-zero rc -- and not for the word
    ``fail`` in prose, because the genuine records describe, in prose, the
    failures they resolved.
    """

    if not isinstance(value, str) or not value.strip():
        problems.append(f"{where}: build_provenance records no {field}; the "
                        f"native test run is the part of the Windows "
                        f"qualification that cannot be re-run here")
        return
    if not re.search(r"\b(GREEN|PASS)\b", value):
        problems.append(f"{where}: {field} names no positive verdict (GREEN or"
                        f" PASS): {_clip_record(value)}")
    named = sorted(set(re.findall(r"\b(FAILED|FAIL|RED)\b", value)))
    if named:
        problems.append(f"{where}: {field} names {named}: "
                        f"{_clip_record(value)}")
    for number in re.findall(r"(\d+)\s+(?:failed|failing|failures)", value):
        if int(number) != 0:
            problems.append(f"{where}: {field} reports {number} failing "
                            f"tests: {_clip_record(value)}")
    for code in re.findall(r"\brc=(\d+)", value):
        if int(code) != 0:
            problems.append(f"{where}: {field} reports a nonzero exit "
                            f"(rc={code}): {_clip_record(value)}")


def _clip_record(text):
    """Shorten one recorded verdict for a problem line."""

    collapsed = " ".join(str(text).split())
    return collapsed if len(collapsed) <= 120 else collapsed[:120] + "..."


def build_battery_manifest(consumed_by_role, ledger_path=None,
                           generated_by=None):
    """Assemble the battery manifest for one consumed report set.

    The manifest binds three things a forger otherwise moves together: the
    reports' content, the revision they name, and the ledger that says which
    artifact each digest is.  ``consumed_by_role`` maps a role to the report
    paths the gate consumed for it.
    """

    entries = []
    revisions = set()
    for role in sorted(consumed_by_role):
        for path in consumed_by_role[role]:
            with open(path, encoding="utf-8") as stream:
                document = json.load(stream)
            if not isinstance(document, dict):
                raise SystemExit(f"cannot manifest a non-object report: {path}")
            revision = document.get("git_head")
            if isinstance(revision, str):
                revisions.add(revision)
            entries.append({"role": role,
                            "name": os.path.basename(path),
                            "content_sha256": _canonical_digest(document),
                            "git_head": revision,
                            "bytes": os.path.getsize(path)})
    # A manifest binds one battery, so it names one revision.  Reports that
    # disagree are recorded rather than refused: the binding then says in
    # words that no single revision covers the set, and the gate rejects it
    # with the revisions named, which is more useful than a producer that
    # exits before the evidence was read.
    ledger_document = None
    if ledger_path:
        entries_map = _sha256_ledger(ledger_path)
        # The ledger is a staged-artifact list, not a repository file, so the
        # binding records what it says (every digest with the paths staged for
        # it) and never its workstation path: a committed artifact must not
        # carry machine paths, and two ledgers with equal entries are the same
        # list regardless of where either is stored.
        ledger_document = {
            "sha256": _sha256_file(os.path.realpath(ledger_path)),
            "entries": {digest: sorted(paths)
                        for digest, paths in sorted(entries_map.items())},
            "entry_count": len(entries_map),
        }
    return {"schema": BATTERY_MANIFEST_SCHEMA,
            "generated_by": generated_by or "v4/cli/check_kind_coverage.py",
            "git_head": next(iter(revisions)) if len(revisions) == 1 else None,
            "revisions": sorted(revisions),
            "ledger": ledger_document,
            "reports": entries}


def write_battery_manifest(path, consumed_by_role, ledger_path=None):
    """Write the manifest the gate consumes, using the gate's own rules.

    One authority produces and one authority consumes: the battery imports
    this instead of assembling a manifest of its own, so the two can never
    disagree about what the binding is.
    """

    document = build_battery_manifest(consumed_by_role, ledger_path)
    with open(path, "w", encoding="utf-8") as stream:
        json.dump(document, stream, sort_keys=True, indent=1)
        stream.write("\n")
    return document


def battery_manifest_evidence(path, manifest, consumed_by_role, problems,
                              ledger_path=None, ledger=None):
    """Require every consumed report to be the report the manifest names.

    A uniform ``git_head`` rewrite -- every report re-stamped to one revision
    so the shared-revision rule stays satisfied while the evidence is moved
    off the revision it was produced on -- is invisible to a gate that only
    compares reports with each other.  The manifest is produced with the
    evidence and committed beside it, so a rewrite has to carry the manifest
    too, and a manifest that was not regenerated for the rewritten reports no
    longer matches their content.

    The manifest is a committed artifact reviewed with the evidence: a writer
    with repository access can regenerate both, so this is a binding, not a
    signature.  Every report the manifest attests must be consumed, for every
    role.  An unlisted report is rejected as well, except under
    ``crash-negative``, where a battery legitimately carries one control per
    faked role and may hold more files than were attested -- but not under a
    name the manifest already binds, which is how an attested control would be
    exchanged for another run's file.
    """

    where = f"battery-manifest {path}"
    if manifest.get("schema") != BATTERY_MANIFEST_SCHEMA:
        problems.append(f"{where}: unexpected schema "
                        f"{manifest.get('schema')!r}")
        return
    revision = manifest.get("git_head")
    recorded_revisions = manifest.get("revisions")
    if isinstance(recorded_revisions, list) and len(recorded_revisions) > 1:
        problems.append(
            f"{where}: no single revision covers the battery: its reports "
            f"name {', '.join(str(text)[:12] for text in recorded_revisions)};"
            f" a manifest binds one battery to one source, and reports copied "
            f"in from another run are visible here")
        return
    if not (isinstance(revision, str) and len(revision) == GIT_HEAD_LENGTH
            and all(character in "0123456789abcdef"
                    for character in revision)
            and len(set(revision)) > 1):
        problems.append(f"{where}: git_head {revision!r} is not a measured "
                        f"40-hex revision; the battery must name the source "
                        f"its reports were produced from")
        return
    listed = {}
    for entry in manifest.get("reports") or []:
        if not isinstance(entry, dict):
            problems.append(f"{where}: report entry is not an object")
            continue
        listed.setdefault(entry.get("role"), []).append(entry)
    unknown_roles = sorted(set(listed) - set(CONSUMED_ROLES))
    if unknown_roles:
        problems.append(f"{where}: manifest lists roles the gate consumes "
                        f"nothing for: {unknown_roles}")
    for role in CONSUMED_ROLES:
        paths = list(consumed_by_role.get(role) or [])
        want = listed.get(role) or []
        want_digests = {entry.get("content_sha256") for entry in want}
        if not want:
            problems.append(f"{where}: the manifest lists no report for the "
                            f"consumed role {role!r}; every consumed class "
                            f"must be attested, and a report that arrives "
                            f"without a manifest entry was not part of the "
                            f"battery that was reviewed")
            continue
        if not paths:
            problems.append(f"{where}: the manifest lists {len(want)} "
                            f"{role} report(s) and the gate consumed none")
            continue
        seen = set()
        for report_path in paths:
            document = _load_report(report_path, problems)
            if not isinstance(document, dict):
                continue
            digest = _canonical_digest(document)
            seen.add(digest)
            if digest not in want_digests:
                # A negative battery legitimately carries more than one
                # control (one report per faked role, found by prefix),
                # so an unlisted report of that role may exist.  It may
                # not take the name of a report the manifest does
                # attest: that is how an attested control is exchanged
                # for another run of the same file name.
                attested_names = set(
                    item.get("name") for item in want)
                if (role != "crash-negative"
                        or os.path.basename(report_path)
                        in attested_names):
                    problems.append(
                        f"{where}: consumed {role} report {report_path} "
                        f"has content sha256 {digest}, which the manifest "
                        f"does not list for that role; a report edited, "
                        f"re-stamped or swapped after the manifest was "
                        f"written is not the evidence the manifest attests")
                continue
            entry = next((item for item in want
                          if item.get("content_sha256") == digest), None)
            if entry is None:
                continue
            if entry.get("git_head") != revision:
                problems.append(
                    f"{where}: the {role} entry "
                    f"{entry.get('name')!r} is listed with git_head "
                    f"{entry.get('git_head')!r}, not the manifest revision "
                    f"{revision}")
            recorded = document.get("git_head")
            if recorded != revision:
                problems.append(
                    f"{where}: {role} report {report_path} names git_head "
                    f"{recorded!r} but the manifest binds this content to "
                    f"{revision}")
        missing = sorted(want_digests - seen)
        if missing:
            problems.append(
                f"{where}: the manifest lists {len(want_digests)} {role} "
                f"report(s) and {len(seen)} were consumed; unaccounted "
                f"content: {[text[:12] for text in missing]}")
    # The staged-artifact binding is one fact with two halves: the manifest
    # has to name the ledger the battery was staged against, and the gate has
    # to be handed a ledger to compare it with.  Either half missing leaves the
    # digests in the reports unattributed, so both are problems rather than a
    # skipped check.  Entries, not file bytes, decide equality: the same list
    # written by a different staging step is the same list.
    recorded_ledger = manifest.get("ledger")
    recorded_entries = (recorded_ledger or {}).get("entries") \
        if isinstance(recorded_ledger, dict) else None
    if recorded_ledger is None:
        if ledger is not None:
            problems.append(
                f"{where}: the gate was handed a staged-artifact ledger but "
                f"the manifest attests none; the binary digests the reports "
                f"name would then bind to a list nothing has reviewed, so "
                f"either emit the manifest with --sha256-ledger for that "
                f"ledger or run the gate without one")
        return
    if not isinstance(recorded_entries, dict) or not recorded_entries:
        problems.append(
            f"{where}: the manifest records no ledger entries, so the "
            f"staged-artifact binding cannot be checked")
        return
    if ledger is None:
        problems.append(
            f"{where}: the manifest binds the staged-artifact list but the "
            f"gate was handed no --sha256-ledger to compare it against; pass "
            f"--sha256-ledger pointing at the SHASUMS file the evidence was "
            f"measured against")
        return
    current = {digest: sorted(paths) for digest, paths in ledger.items()}
    if recorded_entries != current:
        missing = sorted(set(recorded_entries) - set(current))
        extra = sorted(set(current) - set(recorded_entries))
        changed = sorted(digest for digest in set(recorded_entries) & set(current)
                         if recorded_entries[digest] != current[digest])
        problems.append(
            f"{where}: the manifest's ledger entries do not match the ledger "
            f"handed to the gate ({len(recorded_entries)} recorded against "
            f"{len(current)} supplied; {len(missing)} dropped, {len(extra)} "
            f"added, {len(changed)} relabeled); the artifact list the "
            f"evidence was staged against is not the one on disk")


def _reply_classification_problems(where, rounds, problems):
    """Require a counted reply to say what it was, when the run said so.

    The census counts *replies*, and a reply is either a service answer or an
    error frame.  A burst whose counted replies are mostly error frames measured
    the error path, not throughput, while every rate in the report stays
    internally consistent -- which is why the rate arithmetic alone cannot catch
    it.  The split is recorded by the harness; until a report carries it its
    absence is not a defect this gate can tell from an older harness, so these
    rules engage only on fields the report actually has.
    """

    for entry in rounds or []:
        if not isinstance(entry, dict):
            continue
        successful = entry.get("successful_replies")
        errors = entry.get("error_replies")
        ratio = entry.get("success_ratio")
        if successful is None and errors is None and ratio is None:
            continue
        replies = entry.get("replies")
        label = f"{where} round {entry.get('round')}"
        for name, value in (("successful_replies", successful),
                            ("error_replies", errors)):
            if value is not None and (not isinstance(value, int)
                                      or value < 0):
                problems.append(f"{label}: {name} {value!r} is not a measured "
                                f"count")
        if isinstance(successful, int) and isinstance(errors, int):
            if isinstance(replies, int) and successful + errors != replies:
                problems.append(
                    f"{label}: {successful} successful and {errors} error "
                    f"replies do not add up to the {replies} counted replies; "
                    f"the classification and the census describe different runs")
                continue
            if successful <= errors:
                problems.append(
                    f"{label}: {errors} of {successful + errors} counted "
                    f"replies are error frames; a burst dominated by errors "
                    f"measures the error path and cannot attest throughput")
        if ratio is not None:
            if not isinstance(ratio, (int, float)) \
                    or not 0.0 <= float(ratio) <= 1.0:
                problems.append(f"{label}: success_ratio {ratio!r} is not a "
                                f"fraction of the counted replies")
            elif isinstance(successful, int) and isinstance(replies, int) \
                    and replies > 0:
                implied = successful / float(replies)
                if abs(implied - float(ratio)) > 0.005:
                    problems.append(
                        f"{label}: success_ratio {ratio!r} contradicts "
                        f"{successful} successful of {replies} counted replies"
                        f" ({implied:.3f})")


def _method_agreement_problems(where, method, record, problems):
    """Require the round census to be the round plan the report declares."""

    if not isinstance(method, dict):
        return
    rounds = [entry for entry in (record.get("rounds") or [])
              if isinstance(entry, dict)]
    declared_rounds = method.get("rounds")
    if isinstance(declared_rounds, int) and declared_rounds > 0 \
            and len(rounds) != declared_rounds:
        problems.append(f"{where}: the method declares {declared_rounds} "
                        f"rounds and the record carries {len(rounds)}; the "
                        f"median is taken over what is here, not over what "
                        f"was planned")
        return
    per_round = method.get("requests_per_round")
    if isinstance(per_round, int) and per_round > 0:
        for entry in rounds:
            if entry.get("requests") != per_round:
                problems.append(
                    f"{where}: round {entry.get('round')} served "
                    f"{entry.get('requests')!r} requests although the method "
                    f"declares {per_round} per round; a rate over a shorter "
                    f"round is not the same measurement")
        burst = method.get("burst_frames")
        if isinstance(burst, int) and burst > per_round:
            problems.append(
                f"{where}: method burst_frames {burst} exceeds the "
                f"{per_round} requests of one round, so the rounds and the "
                f"burst describe different workloads")


def _thread_census_problems(where, method, small, large, problems):
    """Bind the thread census to the baseline the report itself declares.

    The attestation exists to catch a reply path that spawns per request: the
    distinct child task ids must stay constant in the request count.  Checking
    that needs a bound, and the bound is the report's own
    ``method.thread_baseline_max``.  Because the report supplies it, the bound
    is itself checked two ways: against the request plan of each pass, and
    against the ceiling the harness that measured it publishes.
    """

    if not isinstance(method, dict):
        problems.append(f"{where}: no method block, so the census has no "
                        f"declared baseline to be measured against")
        return
    baseline = method.get("thread_baseline_max")
    if not isinstance(baseline, int) or baseline < 1:
        problems.append(f"{where}: method thread_baseline_max {baseline!r} is "
                        f"not a positive count; an unbounded baseline makes "
                        f"the census check vacuous")
        return
    harness = _table_module(
        "throughput_harness", ("THREAD_BASELINE_MAX", "CLONE_COUNT_ALLOWANCE"),
        where, problems)
    if harness is not None and baseline > harness.THREAD_BASELINE_MAX:
        problems.append(
            f"{where}: method thread_baseline_max {baseline} exceeds the "
            f"{harness.THREAD_BASELINE_MAX} ceiling the measuring harness "
            f"publishes; a bound widened in the report cannot justify a "
            f"census that breaks it")
    plan = (("small", small, method.get("thread_probe_requests_small")),
            ("large", large, method.get("thread_probe_requests_large")))
    counts = {}
    for side_name, side, wanted_requests in plan:
        counts[side_name] = side
        if isinstance(wanted_requests, int) \
                and side.get("requests") != wanted_requests:
            problems.append(
                f"{where}: {side_name} pass ran {side.get('requests')!r} "
                f"requests although the method declares "
                f"{wanted_requests} for that pass; the two passes must differ "
                f"only in request count")
        if side.get("replies") != side.get("requests"):
            continue
        clones = side.get("clone_syscalls")
        tids = side.get("unique_child_tids")
        if not isinstance(clones, int) or not isinstance(tids, int):
            continue
        if not 1 <= tids <= baseline:
            problems.append(
                f"{where}: {side_name} pass records {tids} distinct child "
                f"task ids, outside the [1, {baseline}] band the report's own "
                f"baseline declares")
        if clones > baseline:
            problems.append(
                f"{where}: {side_name} pass records {clones} clone syscalls, "
                f"over the report's own baseline of {baseline}")
        if tids > clones:
            problems.append(
                f"{where}: {side_name} pass records {tids} distinct child task"
                f" ids from {clones} clone syscalls; every task id a child "
                f"process gets comes from a clone, so the census parsed more "
                f"children than it saw")
        if clones > 1 and tids <= 1:
            problems.append(
                f"{where}: {side_name} pass records {clones} clone syscalls "
                f"and {tids} distinct child task ids; a threaded round that "
                f"answers the same request count must show more than one "
                f"child identity")
    if harness is not None and len(counts) == 2:
        small_clones = counts["small"].get("clone_syscalls")
        large_clones = counts["large"].get("clone_syscalls")
        if isinstance(small_clones, int) and isinstance(large_clones, int) \
                and small_clones >= 1:
            growth = large_clones - small_clones
            if growth > harness.CLONE_COUNT_ALLOWANCE \
                    or large_clones >= 2 * small_clones:
                problems.append(
                    f"{where}: clone syscalls went {small_clones} to "
                    f"{large_clones} when the request count doubled; the "
                    f"reply path spawns, which is the regression this census "
                    f"exists to catch ({harness.CLONE_COUNT_ALLOWANCE} is the"
                    f" allowance the measuring harness publishes)")

def assess(matrix_paths, crash_paths, verify_binaries=False,
            verify_cases=False, fifo_paths=(), throughput_paths=(),
            sha256_ledger=None, parity_paths=None, coverage_paths=None,
            crash_negative_paths=None, windows_paths=None,
            battery_manifest=None, require_consumed=True):
    """Evaluate one evidence revision; testable without the CLI.

    Returns ``(problems, coverage, sources)`` where ``coverage`` maps
    each required kind to the set of languages that created it and the
    set of languages that opened it.

    ``verify_binaries`` enables the on-disk binary binding: every
    recorded executable path must exist and its sha256 must match the
    recorded identity (F11).  ``verify_cases`` enables the case-identity
    binding: PASS-case names must be cases defined under
    ``v4/cli/cases/`` and each actor's recorded executed operations
    must be methods the named case declares for that actor (F3).  The
    CLI enables both; the synthetic self-test battery keeps them off
    for its doctored reports and exercises each explicitly.

    The remaining arguments consume the wave's newest artifacts.  Each
    publishes a verdict or a measurement made by its own harness, and each
    is verified here against the identities and committed tables this gate
    already trusts: ``parity_paths`` the refusal-class parity verdict,
    ``coverage_paths`` the Go coverage measurement, ``crash_negative_paths``
    the ``/bin/false`` crash controls, ``windows_paths`` the native Windows
    qualification, and ``battery_manifest`` the report-set binding (a path
    or a decoded document).  ``require_consumed`` (default) makes an absent
    class a gate problem instead of a skipped check: a verdict that can be
    left out of the battery is a verdict that does not gate anything.  The
    synthetic self-test turns it off only for the classes it does not model.
    """

    coverage = {kind: {"created": set(), "opened": set()}
                for kind in REQUIRED_KINDS}
    matrix_coverage = {kind: {"created": set(), "opened": set()}
                       for kind in REQUIRED_KINDS}
    sources = []
    problems = []
    crash_pass_seen = False
    kind_sources = {}
    crash_consumer_opened = {}
    implementation_of = _global_implementation_map(
        matrix_paths, crash_paths, problems)
    # --- the consumed-report set is one revision (roles: a report copied
    # from an earlier run, from another checkout, or with its revision field
    # edited into place was accepted today).
    ledger = _sha256_ledger(sha256_ledger)
    # Every consumed class must be present.  Resolving a class here rather
    # than in the shell is what makes an omitted flag unable to skip it.
    parity_paths = list(parity_paths or [])
    coverage_paths = list(coverage_paths or [])
    crash_negative_paths = list(crash_negative_paths or [])
    windows_paths = list(windows_paths or [])
    anchors = list(matrix_paths) + list(crash_paths) + list(fifo_paths) \
        + list(throughput_paths)
    if require_consumed:
        parity_paths = _resolve_consumed(parity_paths, "refusal-class-parity",
                                         anchors, problems)
        coverage_paths = _resolve_consumed(coverage_paths, "coverage-go",
                                           anchors, problems)
        crash_negative_paths = _resolve_consumed(
            crash_negative_paths, "crash-negative", anchors, problems)
        windows_paths = _resolve_consumed(windows_paths,
                                         "windows-housekeeping", anchors,
                                         problems)
    consumed_by_role = {
        "matrix": list(matrix_paths), "crash": list(crash_paths),
        "crash-negative": crash_negative_paths,
        "fifo-surface": list(fifo_paths),
        "throughput": list(throughput_paths),
        "refusal-class-parity": parity_paths,
        "coverage-go": coverage_paths,
        "windows-housekeeping": windows_paths,
    }
    _shared_git_head({
        "matrix": list(matrix_paths), "crash": list(crash_paths),
        "crash-negative": crash_negative_paths,
        "fifo-surface": list(fifo_paths),
        "throughput": list(throughput_paths),
        "refusal-class-parity": parity_paths,
        "coverage-go": coverage_paths,
        "windows-housekeeping": windows_paths}, problems)
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
        matrix, evidence, stats, _, command_fixture = matrix_evidence(
            path, report, implementation_of, fixture_paths, problems,
            verify_cases=verify_cases)
        if verify_cases and matrix in REQUIRED_MATRICES:
            _case_inventory_problems(path, matrix, report, problems)
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
        # Cross-report fixture identity (external and kind-gate
        # review findings): the matrix report records the v4-fixture
        # tool it used; the identity is mandatory whenever the
        # recorded command selects a fixture (the matrix runner
        # records the fixture_tool record for every run that carried
        # --fixture-tool), the path must be the battery's fixture,
        # and its sha256 must equal the crash report root identity
        # for the same path.
        fixture = report.get("fixture_tool")
        if command_fixture is not None and not isinstance(fixture, dict):
            problems.append(
                f"matrix {path}: report records no fixture_tool "
                f"identity although the recorded command exercises "
                f"the fixture ({command_fixture!r})")
        elif isinstance(fixture, dict):
            fixture_path = fixture.get("path")
            fixture_sha = fixture.get("sha256")
            if not (isinstance(fixture_path, str)
                    and isinstance(fixture_sha, str)):
                problems.append(
                    f"matrix {path}: fixture_tool record carries no "
                    f"path/sha256 identity")
            elif isinstance(fixture_path, str) and isinstance(
                    fixture_sha, str):
                real = os.path.realpath(fixture_path)
                if command_fixture is not None and real != command_fixture:
                    # The matrix's fixture facts must describe the
                    # fixture its recorded command selected; a
                    # different valid fixture path is contradictory
                    # provenance even when both are crash-recorded
                    # (external review finding).
                    problems.append(
                        f"matrix {path}: fixture_tool metadata names "
                        f"{fixture_path!r} but the report command "
                        f"selected {command_fixture!r}")
                    continue
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
            matrix_bucket = matrix_coverage.setdefault(
                kind, {"created": set(), "opened": set()})
            matrix_bucket["created"].update(sides["created"])
            matrix_bucket["opened"].update(sides["opened"])
            kind_sources.setdefault(
                kind, {"matrix": False, "crash": False})["matrix"] = True
    # Non-vacuous per-source open requirement (F4): the matrix
    # evidence alone must show both languages opening every kind whose
    # contract implies a cross-process reader.  Iterating
    # REQUIRED_OPENED_KINDS (rather than a shorter hardcoded pair) is
    # what makes adapter_output and metadata_delivery genuinely
    # required: hardcoding the pair let those kinds satisfy the gate
    # from same-language or crash-only evidence alone.  Strip the
    # matrix open records from every case and the crash evidence cannot
    # repay the missing matrix-side coverage.
    for kind in REQUIRED_OPENED_KINDS:
        matrix_opened = matrix_coverage.get(kind, {}).get("opened", set())
        if not {"rust", "go"} <= matrix_opened:
            problems.append(
                f"kind {kind!r} must be opened by both languages in the "
                f"matrix evidence: opened by {sorted(matrix_opened)}")
    # Battery-level attestation of the two surfaces the corpus can only
    # prove by assertion (security F2, closure F2): the params validator
    # and the cross-language export.  Both aggregates come from the
    # matrix reports alone, so crash evidence cannot repay a missing
    # matrix-side attestation, and deleting the records from every
    # report fails the gate instead of quietly shrinking what it proves.
    attested_negatives = set()
    attested_groups = {}
    for stats in matrix_stats.values():
        attested_negatives |= stats.get("params_negative", set())
        for group, facts in stats.get("digest_groups", {}).items():
            merged = attested_groups.setdefault(group, {
                "digests": set(), "languages": set(), "actors": set()})
            merged["digests"] |= facts["digests"]
            merged["languages"] |= facts["languages"]
            merged["actors"] |= facts["actors"]
    # Params-validator coverage (security F2).  Two independent checks:
    # the committed corpus must still declare a -32602 assertion for every
    # writer_budget method whose grammar forbids zero, and every such
    # assertion must have actually executed somewhere in the battery.
    # Each product language must also be credited with at least one
    # asserted rejection, so an engine cannot stop honouring the params
    # contract without failing here.  Per-method coverage by a specific
    # engine is deliberately not required: a method one engine answers
    # with a product error is a defect that belongs to that engine's
    # matrix FAIL rows, not a reason to delete the assertion.
    declared_negatives = set()
    definitions, definitions_error = _case_definitions()
    if definitions_error:
        problems.append(definitions_error)
    else:
        for definition in definitions.values():
            for (_actor, method) in definition.get("params_rejected", {}):
                declared_negatives.add(method)
    missing_from_corpus = sorted(set(REQUIRED_PARAMS_NEGATIVE_METHODS)
                                 - declared_negatives)
    if missing_from_corpus:
        problems.append(
            f"no committed case asserts a -32602 params rejection for "
            f"{missing_from_corpus}; these writer_budget methods have no "
            f"zero value in the API contract and must be refused by the "
            f"params validator")
    attested_methods = {method for method, _language in attested_negatives}
    for method in sorted(declared_negatives
                         & set(REQUIRED_PARAMS_NEGATIVE_METHODS)):
        if method not in attested_methods:
            problems.append(
                f"method {method!r} declares an expect_params_rejected "
                f"step but no PASS case attests an executed -32602 "
                f"rejection of it; the assertion never ran")
    for language in PRODUCT_LANGUAGES:
        if language not in {lang for _m, lang in attested_negatives}:
            problems.append(
                f"no asserted -32602 params rejection was executed by "
                f"{language!r}; both engines must be held to the params "
                f"validator contract")
    for group, facts in sorted(attested_groups.items()):
        digests = facts["digests"]
        languages = facts["languages"]
        actors = facts["actors"]
        if len(digests) != 1:
            problems.append(
                f"digest group {group!r} has {len(digests)} distinct "
                f"artifact digests across the battery: the two engines do "
                f"not export the same bytes")
        if not {"rust", "go"} <= languages:
            problems.append(
                f"digest group {group!r} is attested by "
                f"{sorted(languages) or ['<none>']}, not both languages")
        if not {"producer", "consumer"} <= actors:
            problems.append(
                f"digest group {group!r} is attested only by roles "
                f"{sorted(actors)}; the consumer's export of the "
                f"producer's artifact is the evidence the acceptance "
                f"criterion names")
    if matrix_paths and not attested_groups:
        problems.append(
            "no export digest group is attested in the matrix evidence: "
            "the cross-language export obligation (consumer exports the "
            "producer's artifact, digests compared) has no evidence")

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
        evidence, stats, consumer_opened, _ = crash_evidence(
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
        for kind, languages in consumer_opened.items():
            crash_consumer_opened.setdefault(kind, set()).update(languages)

    # Crash-side consumer-open requirement (F5): the crash scenarios'
    # own executed open records must prove both consumer languages
    # opening the kinds the crash suite opens by contract through the
    # consumer.  Stripping the consumer opens of one direction strips
    # that consumer language's coverage entirely and must fail.
    for kind in ("v4_main", "live_sidecar"):
        consumer_languages = crash_consumer_opened.get(kind, set())
        if not {"rust", "go"} <= consumer_languages:
            problems.append(
                f"kind {kind!r} must be consumer-opened by both "
                f"languages in the crash evidence: consumer-opened by "
                f"{sorted(consumer_languages)}")

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
    for path in fifo_paths:
        report = _load_report(path, problems)
        if isinstance(report, dict):
            fifo_surface_evidence(path, report, implementation_of, ledger,
                                  problems, verify_binaries=verify_binaries,
                                  fixture_shas=set(fixture_shas.values()))
    for path in throughput_paths:
        report = _load_report(path, problems)
        if isinstance(report, dict):
            throughput_evidence(path, report, implementation_of, ledger,
                                problems, verify_binaries=verify_binaries)

    # --- the wave's newest artifacts, consumed.
    attested_digests = set()
    for path in list(throughput_paths):
        report = _load_report(path, [])
        if isinstance(report, dict):
            attested_digests |= _all_digests(report)
    for path in list(fifo_paths) + list(parity_paths):
        report = _load_report(path, [])
        if isinstance(report, dict):
            for record in (report.get("binaries") or {}).values():
                if isinstance(record, dict) and isinstance(record.get("sha256"), str):
                    attested_digests.add(record["sha256"])
    for path in list(matrix_paths) + list(crash_paths):
        report = _load_report(path, [])
        if isinstance(report, dict):
            for _sha, _implementation in _matrix_binary_declarations(report):
                attested_digests.add(_sha)

    for path in crash_negative_paths:
        report = _load_report(path, problems)
        if not isinstance(report, dict):
            continue
        rejected = crash_negative_evidence(path, report, implementation_of,
                                          problems)
        sources.append(f"crash-negative {path} ({rejected} scenarios "
                       f"rejected, as a negative control must)")

    for path in parity_paths:
        report = _load_report(path, problems)
        if not isinstance(report, dict):
            continue
        refusal_class_parity_evidence(path, report, implementation_of, ledger,
                                      problems,
                                      fixture_shas=set(fixture_shas.values()),
                                      verify_binaries=verify_binaries)
        sources.append(f"refusal-class-parity {path} "
                       f"({len(report.get('cells') or [])} cells)")

    for path in coverage_paths:
        report = _load_report(path, problems)
        if not isinstance(report, dict):
            continue
        coverage_evidence(path, report, attested_digests, problems)
        sources.append(f"coverage-go {path} "
                       f"({len(report.get('per_package') or {})} packages)")

    for path in windows_paths:
        report = _load_report(path, problems)
        if not isinstance(report, dict):
            continue
        windows_provenance_evidence(path, report, ledger, problems,
                                    linux_digests=attested_digests)
        outcomes = report.get("outcomes") or []
        sources.append(f"windows {path} ({len(outcomes)} native outcomes)")

    manifest_path = battery_manifest
    manifest = battery_manifest
    if isinstance(manifest, dict):
        # A caller that hands in the decoded document (the self-test does)
        # leaves no path to name, and printing the document in its place made
        # every finding in this file unreadable.
        manifest_path = "<manifest handed in as a document>"
    if isinstance(manifest, str) or manifest is None:
        if manifest is None:
            manifest_path = os.path.join(COMMITTED_EVIDENCE_DIR,
                                         BATTERY_MANIFEST_FILE_NAME)
        if not os.path.isfile(manifest_path):
            if require_consumed:
                problems.append(
                    f"no battery manifest at {manifest_path}: the consumed "
                    f"reports carry no committed binding of content to "
                    f"revision and ledger, so a rewrite of every git_head in "
                    f"one pass would be indistinguishable from a real battery")
            manifest = None
        else:
            manifest = _load_report(manifest_path, problems)
    if isinstance(manifest, dict):
        battery_manifest_evidence(manifest_path, manifest, consumed_by_role,
                                  problems, ledger_path=sha256_ledger,
                                  ledger=ledger)

    if verify_binaries:
        _verify_recorded_binaries(matrix_paths, crash_paths, problems)

    return problems, coverage, sources


def _verify_recorded_binaries(matrix_paths, crash_paths, problems):
    """On-disk binary binding (F11).

    Every executable path a report records as executed -- matrix
    ``binaries`` records, matrix ``fixture_tool`` records, and the
    crash root binaries table -- must exist on the review machine and
    its sha256 must equal the recorded identity.  A recorded path that
    does not exist is a report defect: the evidence claims the binary
    ran, so the binary the gate can verify is the one the report's
    sha256 names.  Path normalization resolves each value exactly like
    the command binding (``_resolve_report_path``), so a spelling
    variant cannot bypass the table and then hide behind a different
    on-disk file.  When two records name the same resolved path with
    different sha256 values, the duplicate identity is a defect too.
    """

    bindings = {}

    def bind(raw_path, sha, label):
        if not (isinstance(raw_path, str) and isinstance(sha, str)):
            return
        resolved = os.path.realpath(raw_path)
        previous = bindings.get(resolved)
        if previous is None:
            bindings[resolved] = {"sha": sha, "labels": [label]}
        else:
            if previous["sha"] != sha:
                problems.append(
                    f"recorded binary {resolved!r} is bound with "
                    f"conflicting sha256 values {previous['sha']!r} and "
                    f"{sha!r} ({label} vs "
                    f"{previous['labels'][0]})")
            previous["labels"].append(label)

    for path in matrix_paths:
        report = _load_report(path, [])
        if not isinstance(report, dict):
            continue
        basename = os.path.basename(path)
        for key, record in (report.get("binaries") or {}).items():
            if isinstance(record, dict):
                bind(record.get("path"), record.get("sha256"),
                     f"matrix {basename} binaries.{key}")
        fixture = report.get("fixture_tool")
        if isinstance(fixture, dict):
            bind(fixture.get("path"), fixture.get("sha256"),
                 f"matrix {basename} fixture_tool")
    for path in crash_paths:
        report = _load_report(path, [])
        if not isinstance(report, dict):
            continue
        basename = os.path.basename(path)
        binaries = report.get("binaries")
        if isinstance(binaries, dict):
            for key, sha in binaries.items():
                if not (isinstance(key, str) and key.endswith("_sha256")):
                    continue
                bind(binaries.get(key[:-len("_sha256")]), sha,
                     f"crash {basename} binaries.{key}")

    for resolved, binding in sorted(bindings.items()):
        if not os.path.isfile(resolved):
            problems.append(
                f"recorded binary path {resolved!r} does not exist on "
                f"the review machine (bound by "
                f"{', '.join(sorted(set(binding['labels'])))})")
            continue
        actual = _sha256_file(resolved)
        if actual != binding["sha"]:
            problems.append(
                f"recorded binary {resolved!r} sha256 {actual} does not "
                f"match the recorded identity {binding['sha']!r} (bound "
                f"by {', '.join(sorted(set(binding['labels'])))})")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--matrix", action="append", default=[],
                        metavar="PATH", help="one matrix report (repeatable)")
    parser.add_argument("--crash", action="append", default=[],
                        metavar="PATH", help="one crash report (repeatable)")
    parser.add_argument("--fifo-surface", action="append", default=[],
                        metavar="PATH",
                        help="the FIFO-surface report of the same revision "
                             "(required: its verdict is consumed evidence)")
    parser.add_argument("--throughput", action="append", default=[],
                        metavar="PATH",
                        help="the throughput attestation of the same revision "
                             "(required: its census is consumed evidence)")
    parser.add_argument("--sha256-ledger", default=None, metavar="PATH",
                        help="a sha256sum-format ledger of the staged "
                             "binaries; when supplied, every surface-report "
                             "digest must appear in it")
    parser.add_argument("--refusal-class-parity", action="append",
                        default=[], metavar="PATH",
                        help="the refusal-class parity report of the same "
                             "revision; discovered beside the battery when "
                             "omitted, and a missing report is a gate problem")
    parser.add_argument("--coverage-go", action="append", default=[],
                        metavar="PATH",
                        help="the Go coverage measurement of the same "
                             "revision; discovered beside the battery when "
                             "omitted")
    parser.add_argument("--crash-negative", action="append", default=[],
                        metavar="PATH",
                        help="one /bin/false crash control report "
                             "(repeatable); every report of the battery's "
                             "own directory is consumed when omitted")
    parser.add_argument("--windows-housekeeping", action="append",
                        default=[], metavar="PATH",
                        help="the native Windows qualification report of the "
                             "same revision; discovered beside the battery "
                             "when omitted")
    parser.add_argument("--battery-manifest", default=None, metavar="PATH",
                        help="the manifest that binds the consumed reports' "
                             "content to a revision and a ledger; defaults to "
                             "the committed evidence manifest")
    parser.add_argument("--emit-manifest", default=None, metavar="PATH",
                        help="write the battery manifest for the reports named"
                             " on this command line, then exit; the wave's "
                             "battery step runs it and the gate consumes the "
                             "result, so producer and consumer of the binding "
                             "share one implementation")
    parser.add_argument("--self-test", action="store_true",
                        help="run the doctored-report regression suite "
                             "(including the control that the committed "
                             "evidence passes this gate) and exit; the "
                             "battery step of a wave runs it, which keeps "
                             "evidence rotation drift loud without letting "
                             "it replace a CLI verdict on the reports "
                             "handed to --matrix/--crash")
    args = parser.parse_args()
    if args.self_test:
        _self_test()
        return 0
    if not args.matrix and not args.crash:
        parser.error("at least one --matrix or --crash report is required")
    # The two surface reports are consumed evidence, not optional extras: an
    # omitted flag is exactly as cheap as a deleted git_head field, so both
    # are required here and their identity is re-derived below.
    if not args.fifo_surface:
        parser.error("--fifo-surface is required: the FIFO verdict of this "
                     "revision must be identity-bound here")
    if not args.throughput:
        parser.error("--throughput is required: the throughput census of "
                     "this revision must be re-derived here")

    if args.emit_manifest:
        return _emit_manifest_command(args)

    problems, coverage, sources = assess(
        args.matrix, args.crash, verify_binaries=True,
        verify_cases=True, fifo_paths=args.fifo_surface,
        throughput_paths=args.throughput,
        sha256_ledger=args.sha256_ledger,
        parity_paths=args.refusal_class_parity,
        coverage_paths=args.coverage_go,
        crash_negative_paths=args.crash_negative,
        windows_paths=args.windows_housekeeping,
        battery_manifest=args.battery_manifest)
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


def consumed_report_set(args):
    """The report set one command line consumes, as ``{role: [paths]}``.

    The manifest producer and the gate consume the same definition, so the
    binding cannot describe a different battery from the one that is judged.
    """

    anchors = list(args.matrix) + list(args.crash) + list(args.fifo_surface) \
        + list(args.throughput)
    resolution = {
        "matrix": list(args.matrix), "crash": list(args.crash),
        "fifo-surface": list(args.fifo_surface),
        "throughput": list(args.throughput),
    }
    problems = []
    for role, flag_value in (
            ("refusal-class-parity", args.refusal_class_parity),
            ("coverage-go", args.coverage_go),
            ("crash-negative", args.crash_negative),
            ("windows-housekeeping", args.windows_housekeeping)):
        resolution[role] = _resolve_consumed(list(flag_value), role, anchors,
                                            problems)
    return resolution, problems


def _emit_manifest_command(args):
    """Write the battery manifest for the reports this command line names."""

    consumed, problems = consumed_report_set(args)
    for problem in problems:
        print(f"FAIL: {problem}")
    if problems:
        print(f"{args.emit_manifest}: not written; resolve the findings above "
              f"and emit the manifest for the battery that produced them")
        return 1
    document = write_battery_manifest(args.emit_manifest, consumed,
                                      args.sha256_ledger)
    roles = {}
    for entry in document["reports"]:
        roles[entry["role"]] = roles.get(entry["role"], 0) + 1
    print("Artifact-kind coverage gate")
    print(f"battery manifest written to {args.emit_manifest}: revision "
          f"{document['git_head']}, {len(document['reports'])} reports "
          f"({', '.join(f'{role}={count}' for role, count in sorted(roles.items()))})"
          f", ledger {'bound' if document['ledger'] else 'not supplied'}")
    return 0


def _self_test():
    """Doctored-report regression tests for the gate integrity rules."""

    import json as _json

    import command_sanitize
    command_sanitize._self_test()

    # Every consumed report names the revision it measured; the synthetic
    # battery shares one value, and the controls below prove that deleting,
    # zeroing, or desynchronizing it is a detected defect.
    revision = "91ae2a42" + "0" * 28 + "d3ad"

    def matrix_report(matrix, cases, failed, root_kinds=None):
        return {
            "schema": "iprange-cli-report-v3",
            "git_head": revision,
            "matrix": matrix,
            "command": [
                "v4/cli/run.py",
                "--rust", BINARY_PATHS["rust"],
                "--go", BINARY_PATHS["go"],
                "--fixture-tool", CRASH_BINARIES["fixture_tool"],
                "--matrix", matrix,
                "--work-dir", "/tmp/kind-matrix-work",
                "--json-report", "/tmp/kind-matrix-report.json"],
            # The matrix runner records the fixture identity for every
            # run whose command carries --fixture-tool; the doctored
            # reports mirror that record and agree with the crash
            # battery identity.
            "fixture_tool": {
                "path": CRASH_BINARIES["fixture_tool"],
                "sha256": CRASH_BINARIES["fixture_tool_sha256"],
            },
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
                "git_head": revision,
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


    def fifo_surface_report():
        """A FIFO-surface report whose refusals match the committed table.

        The arm table itself is owned by ``check_fifo_surface.py``; the kind
        gate consumes this report for the identity of the binaries that
        produced the verdict and for the both-engine claim, so the synthetic
        copy carries one arm per engine plus the pinned class."""
        import check_fifo_surface as _surface
        arms = []
        for engine, digest in (("go", "2" * 64), ("rust", "1" * 64)):
            for arm in _surface.ARM_NAMES:
                method = _surface.ARM_METHOD[arm]
                code = _surface.ARM_EXPECTED[arm]
                arms.append({
                    "engine": engine, "arm": arm, "method": method,
                    "expected_code": code, "kind": "answered",
                    "transport_code": _surface.PRODUCT_ERROR,
                    "data_code": code,
                    "message": "input is not a regular file", "elapsed_ms": 3.0,
                    "exit_status": 0,
                    "request": "{\"jsonrpc\":\"2.0\",\"method\":\"%s\"}"
                               % method})
        return {"schema": "iprange-cli-fifo-surface-report-v1",
                "git_head": revision, "checkout_root": None,
                "command": ["v4/cli/check_fifo_surface.py"],
                "platform": {"system": "Linux"},
                "binaries": {
                    "go": {"path": BINARY_PATHS["go"], "sha256": "2" * 64,
                           "implementation": "go"},
                    "rust": {"path": BINARY_PATHS["rust"],
                             "sha256": "1" * 64, "implementation": "rust"},
                    "fixture_tool": {"path": CRASH_BINARIES["fixture_tool"],
                                     "sha256":
                                         CRASH_BINARIES["fixture_tool_sha256"],
                                     "implementation": "rust"}},
                "arms": arms,
                "controls": [{"engine": "go", "answered_result": True},
                             {"engine": "rust", "answered_result": True}],
                "summary": {"arms_expected": len(arms), "failed": []},
                "result": "PASS", "problems": [], "deadline_seconds": 3.0}

    def throughput_report():
        """A throughput attestation whose rates are its own arithmetic."""
        def engine_record(digest, rate, clones):
            rounds = [{"round": index, "requests": 10000, "replies": 10000,
                       "seconds": round(10000.0 / rate, 4),
                       "replies_per_s": rate, "exit_status": 0}
                      for index in range(3)]
            return {"path": BINARY_PATHS["rust"] if digest == "1" * 64
                    else BINARY_PATHS["go"],
                    "sha256": digest,
                    "implementation": "rust" if digest == "1" * 64 else "go",
                    "rounds": rounds, "median_replies_per_s": rate,
                    "thread_structure": {
                        "small": {"requests": 3000, "replies": 3000,
                                  "clone_syscalls": clones,
                                  "unique_child_tids": clones},
                        "large": {"requests": 6000, "replies": 6000,
                                  "clone_syscalls": clones,
                                  "unique_child_tids": clones},
                        "tool": "/usr/bin/strace"}}
        return {"schema": "iprange-cli-throughput-report-v1",
                "git_head": revision, "checkout_root": None,
                "command": ["v4/cli/throughput_harness.py"],
                "platform": {"system": "Linux"},
                "method": {"burst_frames": 30,
                           "request": {"jsonrpc": "2.0", "id": 1,
                                       "method": "iprange.v1.system.describe",
                                       "params": {}},
                           "requests_per_round": 10000, "rounds": 3,
                           "thread_baseline_max": 64,
                           "thread_probe_requests_small": 3000,
                           "thread_probe_requests_large": 6000},
                "product": {"go": engine_record("2" * 64, 40000.0, 17),
                            "rust": engine_record("1" * 64, 62000.0, 4)},
                "result": "PASS", "problems": []}

    def parity_report(named_revision=None, identities=None,
                      binary_records=None):
        """A refusal-class parity report that satisfies the committed tables.

        Built from the generator's own arm, path-kind and pinned-refusal
        tables rather than copied, so it tracks a widened grid instead of
        freezing today's shape.  It is the positive control for the parity
        rules: if a rule fires on evidence that is complete and internally
        honest, the rule is wrong.
        """

        import check_refusal_class_parity as _parity
        named = named_revision or revision
        expected = list(_parity.expected_cells())
        grid = {"arms": list(_parity.ARM_NAMES),
                "path_kinds": _parity.kind_names(),
                "mandatory_path_kinds": list(_parity.MANDATORY_PATH_KINDS),
                "cells_expected": len(expected)}
        # The generator binds a verdict to the obligations it was measured
        # against by recording the fingerprint of the pin table.  The synthetic
        # copy carries it too, so a ledger-bound control is rejected for its
        # own defect and not for an omission of this harness.
        fingerprint = getattr(_parity, "pinned_table_fingerprint", None)
        if callable(fingerprint):
            grid["pinned_table_sha256"] = fingerprint()
        # The sweep's artifacts must be identities the battery already proves
        # it executed; the synthetic battery has its own, and the copy that is
        # consumed beside the committed reports uses theirs.
        identities = identities or {
            "go": "2" * 64, "rust": "1" * 64,
            "fixture": CRASH_BINARIES["fixture_tool_sha256"]}
        # A copy that is consumed beside the committed reports must also name
        # the committed artifact paths, because the CLI binds every recorded
        # executable to the file on disk; a synthetic path there would be
        # rejected for the harness's own choice of paths.
        records = dict(binary_records or {})
        def record_for(engine, path, digest):
            recorded = records.get(engine)
            if isinstance(recorded, dict) and recorded.get("sha256") == digest:
                return dict(recorded)
            return {"path": path, "sha256": digest,
                    "implementation": engine}
        digest_of = {"go": identities["go"], "rust": identities["rust"]}
        cells = []
        for arm_name, kind_name in expected:
            value = _parity.PINNED_REFUSALS.get((arm_name, kind_name))
            want_code, want_outcome, want_facts = \
                _parity.pin_expectation(value) if value is not None \
                else ("invalid_argument", None, None)
            side = {}
            for engine in ("go", "rust"):
                facts = None
                evidence = None
                if want_facts == "required":
                    evidence = {
                        "outcome": "outcome_unknown",
                        "publication_policy": "fail_if_exists",
                        "path": "/tmp/kind-parity-work/dest.iprange",
                        "stage": "sync export output directory",
                        "destination_visible": True,
                        "temporary_removed": True,
                        "sha256": "b" * 63 + "1",
                    }
                    facts = {"complete": True, "container": "publication",
                             "destination_visible": True,
                             "outcome_unknown": True,
                             "sha256_well_formed": True,
                             "temporary_removed": True}
                side[engine] = {
                    "arm": arm_name, "path_kind": kind_name,
                    "engine": engine, "kind": "answered",
                    "transport_code": -32010,
                    "data_code": want_code or "invalid_argument",
                    "outcome": want_outcome or "not_started",
                    "method": _parity.ARM_BY_NAME[arm_name][1],
                    "slot": _parity.ARM_BY_NAME[arm_name][2],
                    "request": "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"%s\"}"
                               % _parity.ARM_BY_NAME[arm_name][1],
                    "elapsed_ms": 2.5, "exit_status": 0,
                    "publication_facts": facts,
                    "publication_evidence": evidence,
                    "digest": digest_of[engine],
                }
            cells.append({"arm": arm_name, "path_kind": kind_name,
                          "agreed": True, "flaky": False, "hung": False,
                          "attempts": 1, "go": side["go"],
                          "rust": side["rust"]})
        pins = []
        for (arm_name, kind_name), value in sorted(
                _parity.PINNED_REFUSALS.items()):
            want_code, want_outcome, want_facts = \
                _parity.pin_expectation(value)
            pins.append({"arm": arm_name, "path_kind": kind_name,
                         "expected_data_code": want_code,
                         "expected_outcome": want_outcome,
                         "expected_facts": want_facts,
                         "executed": True,
                         "go_data_code": want_code or "invalid_argument",
                         "go_outcome": want_outcome or "not_started",
                         "go_facts": want_facts == "required",
                         "rust_data_code": want_code or "invalid_argument",
                         "rust_outcome": want_outcome or "not_started",
                         "rust_facts": want_facts == "required"})
        durability_keys = set(_parity.durability_cells())
        executed = {(cell["arm"], cell["path_kind"]) for cell in cells}
        with_evidence = sum(
            1 for key in durability_keys
            if key in executed
            and _parity.pin_expectation(
                _parity.PINNED_REFUSALS.get(key))[2] == "required")
        return {
            "schema": REFUSAL_PARITY_SCHEMA, "git_head": named,
            "checkout_root": None, "result": "PASS", "provenance": None,
            "command": ["v4/cli/check_refusal_class_parity.py"],
            "platform": {"system": "Linux"},
            "elapsed_seconds": 30.0,
            "method": {"attempt_deadline_seconds": 4.0, "retries": 2,
                       "run_budget_seconds": 55.0,
                       "compared": ["kind", "transport_code", "data_code",
                                    "outcome", "publication_facts"]},
            "binaries": {
                "go": record_for("go", BINARY_PATHS["go"], identities["go"]),
                "rust": record_for("rust", BINARY_PATHS["rust"],
                                   identities["rust"]),
                "fixture_tool": record_for(
                    "fixture_tool", CRASH_BINARIES["fixture_tool"],
                    identities["fixture"])},
            "grid": grid,
            "cells": cells, "pins": pins, "divergences": [],
            "durability": {"cells_executed": len(durability_keys),
                           "cells_expected": len(durability_keys),
                           "cells_with_evidence": with_evidence},
            "summary": {"cells_expected": len(expected),
                        "cells_executed": len(cells),
                        "agreements": len(cells), "divergences": 0,
                        "hangs": 0, "flaky": 0,
                        "pins_expected": len(_parity.PINNED_REFUSALS),
                        "pins_satisfied": len(_parity.PINNED_REFUSALS)},
        }

    COVERAGE_MEASURE = {
        "statements": 57160, "covered_statements": 27271,
        "functions": 5917, "covered_functions": 3832,
        "blocks": 41876, "blocks_covered": 18567}

    def coverage_report():
        """A Go coverage report whose percentages are its own arithmetic."""

        def profile(counts):
            percent = {"measured": dict(counts)}
            for name, covered in (("statements", "covered_statements"),
                                  ("functions", "covered_functions"),
                                  ("blocks", "blocks_covered")):
                percent[name] = round(100.0 * counts[covered] / counts[name],
                                      2)
            return {"measured": dict(counts), "percent": percent}

        unit = profile(COVERAGE_MEASURE)
        unit["rc"] = 0
        unit["covermode"] = "atomic"
        unit["coverdir"] = "/tmp/kind-coverage-work/unit"
        unit["cover_files"] = 12
        integration = profile({"statements": 53741, "covered_statements":
                               21698, "functions": 5705,
                               "covered_functions": 3378, "blocks": 39546,
                               "blocks_covered": 14418})
        integration["cover_files"] = 9
        integration["runs"] = [{"matrix": "go", "rc": 0, "passed": 63,
                                "failed": 0, "skipped": 0,
                                "command": ["v4/cli/run.py", "--matrix",
                                            "go"]}]
        merged = profile({"statements": 57172, "covered_statements": 34040,
                          "functions": 5918, "covered_functions": 4747,
                          "blocks": 41883, "blocks_covered": 22980})
        return {
            "schema": GO_COVERAGE_SCHEMA, "git_head": revision,
            "checkout_root": None,
            "command": ["v4/cli/coverage_harness.py"],
            "platform": {"system": "Linux"}, "module": "iprange",
            "unit": unit, "integration": integration, "merged": merged,
            "per_package": {
                "github.com/firehol/iprange/v4/go/internal/cli": {
                    "statements": 3197, "covered_statements": 2310,
                    "statements_total": 3197, "functions": 456,
                    "covered_functions": 419, "blocks": 2636,
                    "blocks_covered": 1813,
                    "statement_percent": 72.26, "function_percent": 91.89,
                    "block_percent": 68.78}},
            "instrumented_binaries": {
                "iprange": {"covermode": "atomic", "instrumented": True,
                            "path": "/tmp/kind-coverage-work/bin/iprange",
                            "sha256": "c" * 63 + "1"},
                "iprange-v4-worker": {
                    "covermode": "atomic", "instrumented": True,
                    "path": "/tmp/kind-coverage-work/bin/iprange-v4-worker",
                    "sha256": "d" * 63 + "2"}},
            "policy": {"performance_use": "instrumented binaries are never "
                                          "used for the throughput or "
                                          "performance attestation",
                       "killed_runs_merged": False}}

    def crash_negative_report(consumer=False):
        """A /bin/false crash control in which every scenario must fail."""

        scenarios = []
        for index, (producer, consumer) in enumerate(
                [("rust", "consumer"), ("consumer", "rust")]):
            for name in ("A1", "A2", "A3", "B", "C", "D", "E", "F"):
                identity = {"rust": BINARY_PATHS["rust"],
                            "go": BINARY_PATHS["go"],
                            "consumer": "/bin/false"}
                sha_of = {"rust": "1" * 64, "go": "2" * 64,
                          "consumer": "e" * 63 + "0"}
                scenarios.append({
                    "scenario": f"{name}.{producer}->{consumer}",
                    "pass": False,
                    "failures": ["service exited with status 1 after a "
                                 "successful session (expected 0)"],
                    "producer": f"{producer}:{identity[producer]}",
                    "consumer": f"{consumer}:{identity[consumer]}",
                    "producer_sha256": sha_of[producer],
                    "consumer_sha256": sha_of[consumer],
                    "assertions": ["peer answered nothing"],
                    "operations": {"producer": ["iprange.v1.reader.open"],
                                   "consumer": ["iprange.v1.reader.open"]},
                    "destination_state": {"residue": []},
                    "reopen_outcome": {"status": "unreadable"},
                    "residue_bounded": None,
                    "kinds": {},
                })
        binaries = {
            "producer": BINARY_PATHS["rust"], "producer_sha256": "1" * 64,
            "consumer": "/bin/false", "consumer_sha256": "e" * 63 + "0",
            "fixture_tool": CRASH_BINARIES["fixture_tool"],
            "fixture_tool_sha256": CRASH_BINARIES["fixture_tool_sha256"]}
        if consumer:
            command = ["v4/cli/crash_harness.py", "--producer",
                       BINARY_PATHS["rust"], "--consumer", "/bin/false",
                       "--fixture-tool", CRASH_BINARIES["fixture_tool"],
                       "--work-dir", "/tmp/kind-crashneg",
                       "--json-report", "/tmp/kind-crashneg/neg.json"]
        else:
            command = ["v4/cli/crash_harness.py", "--producer", "/bin/false",
                       "--consumer", BINARY_PATHS["rust"], "--fixture-tool",
                       CRASH_BINARIES["fixture_tool"],
                       "--work-dir", "/tmp/kind-crashneg",
                       "--json-report", "/tmp/kind-crashneg/neg.json"]
            binaries = {
                "producer": "/bin/false", "producer_sha256": "e" * 63 + "0",
                "consumer": BINARY_PATHS["rust"], "consumer_sha256": "1" * 64,
                "fixture_tool": CRASH_BINARIES["fixture_tool"],
                "fixture_tool_sha256": CRASH_BINARIES["fixture_tool_sha256"]}
        return {"schema": CRASH_REPORT_SCHEMA, "git_head": revision,
                "checkout_root": None, "command": command,
                "platform": {"system": "Linux"}, "binaries": binaries,
                "leftover_processes": [], "failed": len(scenarios),
                "scenarios": scenarios}

    def windows_report():
        """A native Windows qualification record with green test tallies."""

        toolchain = {"go": "go version go1.26.5 windows/amd64",
                     "rustc": "rustc 1.97.1 (8bab26f4f) host "
                              "x86_64-pc-windows-msvc, LLVM version 22.1.6",
                     "cargo": "cargo 1.97.1 (c980f4866)",
                     "host_triple": "x86_64-pc-windows-msvc"}
        commands = [
            "cd v4/go && nice go build -trimpath -o "
            "C:/msys64/tmp/kind/bin/go/iprange.exe ./cmd/iprange",
            "nice cargo build --manifest-path v4/rust/Cargo.toml --release",
            "cp v4/rust/target/release/iprange-v4-worker.exe "
            "v4/rust/target/debug/deps/iprange-v4-worker.exe",
            "nice cargo test --manifest-path v4/rust/Cargo.toml "
            "-p iprange-cli"]
        return {
            "schema": WINDOWS_HOUSEKEEPING_SCHEMA, "git_head": revision,
            "checkout_root": None,
            "command": ["v4/cli/windows_housekeeping_harness.py"],
            "platform": {"system": "Windows", "machine": "AMD64",
                         "release": "11"},
            "windows_qualified": True, "skipped": False,
            "skipped_reason": None, "failed": 0,
            "outcomes": [{"binary": "go"}, {"binary": "rust"}],
            "binaries": {
                "go": {"path": "C:/msys64/tmp/kind/bin/go/iprange.exe",
                       "sha256": "f" * 63 + "1", "size": 14060544},
                "rust": {"path": "C:/msys64/tmp/kind/bin/rust/iprange.exe",
                         "sha256": "f" * 62 + "1" + "f", "size": 6266368}},
            "build_provenance": {
                "revision": revision, "wave": "self-test",
                "tree_clean": True,
                "toolchain": toolchain, "build_commands": commands,
                "native_go_test": "GREEN. 'nice go test ./...' returned "
                                  "rc=0: 23 packages ok, 0 failing packages.",
                "native_cargo_test": "GREEN. 'cargo test -p iprange-cli' "
                                     "returned rc=0: 334 passed / 0 failed."}}

    GREEN_EXPORT_SHA = "a" * 64

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
            # Every required-opened kind needs a matrix-side opener:
            # the consumer opens the producer's adapter output by
            # exporting over the existing destination, and opens the
            # previously delivered metadata file by re-delivering it.
            opened = {
                "v4_main": ["consumer.iprange.v1.reader.open"],
                "live_sidecar": ["consumer.iprange.v1.reader.open"],
                "adapter_output": ["consumer.iprange.v1.export"],
                "metadata_delivery": [
                    "consumer.iprange.v1.database.metadata.get"],
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
                    "steps": 6,
                    "operations": ["iprange.v1.database.create",
                                   "iprange.v1.export",
                                   "iprange.v1.direct.replace",
                                   "iprange.v1.database.metadata.replace",
                                   "iprange.v1.feeds.create",
                                   "iprange.v1.database.reclaim"],
                },
                "consumer": {
                    "sha256": actor_sha[expected["consumer"]],
                    "implementation": expected["consumer"],
                    "argv": BINARY_PATHS[expected["consumer"]],
                    "steps": 3,
                    "operations": ["iprange.v1.reader.open",
                                   "iprange.v1.database.metadata.get",
                                   "iprange.v1.export"],
                },
            },
            # The params-validator and cross-language export
            # attestations the battery must carry (security F2, closure
            # F2).  Each required writer_budget method is refused by the
            # executing engine, and one digest group is produced by both
            # service roles.
            "params_rejected": [
                {"actor": "producer", "method": method,
                 "transport_code": _STD_INVALID_PARAMS,
                 "request": "{\"id\":1,\"method\":\""
                            + method + "\"}"}
                for method in REQUIRED_PARAMS_NEGATIVE_METHODS],
            "digest_groups": {
                "green-export-digest": [
                    {"actor": "producer", "method": "iprange.v1.export",
                     "format": "netset", "path": "p.netset",
                     "sha256": GREEN_EXPORT_SHA},
                    {"actor": "consumer", "method": "iprange.v1.export",
                     "format": "netset", "path": "c.netset",
                     "sha256": GREEN_EXPORT_SHA}],
            },
            "file_kinds": ledger}], failed=0)
        report["binaries"] = BINARIES
        return report


    # The refusal-class parity artifact is generated by sweeping the arm and
    # path-kind tables that check_refusal_class_parity owns.  When a wave
    # widens those tables, the committed artifact is behind them until the
    # harness runs again, and the grid-coverage rules fire on it as a matter
    # of record rather than as a finding.  The genuine-evidence assertions
    # below therefore require that nothing OTHER than that rotation fires: a
    # rule that rejects honest, complete evidence is a defect in this gate,
    # and it has to fail here rather than in front of a reviewer.  Anything
    # outside these needles -- a wrong pin, an unbacked publication fact, a
    # borrowed binary identity -- still fails the self-test.
    PARITY_ROTATION_MARKS = (
        "grid arms", "grid path kinds", "cells_expected", "were never "
        "executed", "were not executed", "shrunken sweep", "shrunken grid",
        "parity gate: summary cells_expected", "parity gate: the report "
        "executed no cell",
        # The same rotation seen from the pin side: an artifact swept before
        # the pin table grew reports fewer pins than the table now obliges.
        # Each needle names a report-against-committed-table divergence, so a
        # pin that is wrong rather than merely absent still has to fail here.
        "differ from the committed obligations",
        "but the committed table pins",
        "contradicts the committed pinned-refusal table",
        "is missing from the report; the pinned-refusal table",
        "is not the committed ",
        # An artifact swept before the generator began recording the
        # fingerprint of the pin table it used.  Which table was used is
        # still checked pin by pin above, so this is a staleness symptom of
        # the same rotation and not a way to move the obligations.
        "records a pinned-refusal table digest other than the committed "
        "table", "records no pinned_table_sha256")

    def outside_parity_rotation(problems):
        """Problems that are not the recorded parity-artifact rotation."""

        return [problem for problem in problems
                if not (problem.startswith("refusal-class-parity ")
                        and any(mark in problem
                                for mark in PARITY_ROTATION_MARKS))]

    evidence_dir = os.path.join(
        os.path.dirname(os.path.abspath(__file__)), "evidence")
    genuine_matrix_paths = [os.path.join(evidence_dir, f"matrix-{m}.json")
                            for m in REQUIRED_MATRICES]
    genuine_crash = os.path.join(evidence_dir, "crash.json")
    genuine_fifo = [os.path.join(evidence_dir, "fifo-surface.json")]
    genuine_throughput = [os.path.join(evidence_dir, "throughput.json")]
    genuine_parity = os.path.join(evidence_dir, "refusal-class-parity.json")
    genuine_coverage = os.path.join(evidence_dir, "coverage-go.json")
    genuine_windows = os.path.join(evidence_dir, "windows-housekeeping.json")
    genuine_negative = sorted(
        os.path.join(evidence_dir, name)
        for name in os.listdir(evidence_dir)
        if name.startswith("crash-negative") and name.endswith(".json"))

    with tempfile.TemporaryDirectory(dir=owned_temp_root()) as work:
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

        # The two surface reports are consumed evidence, so the synthetic
        # battery carries them too; every control below therefore runs
        # against the same seven-report revision the CLI does.
        battery_fifo = [os.path.join(work, "fifo-surface.json")]
        battery_throughput = [os.path.join(work, "throughput.json")]
        battery_parity = [os.path.join(work, "refusal-class-parity.json")]
        battery_coverage = [os.path.join(work, "coverage-go.json")]
        battery_negative = [os.path.join(work, "crash-negative.json")]
        battery_windows = [os.path.join(work, "windows-housekeeping.json")]
        assign(battery_fifo[0], fifo_surface_report())
        assign(battery_throughput[0], throughput_report())
        assign(battery_parity[0], parity_report())
        assign(battery_coverage[0], coverage_report())
        assign(battery_negative[0], crash_negative_report())
        assign(battery_windows[0], windows_report())

        manifest_serial = [0]

        def manifest_over(matrices, crashes, fifo, throughput, parity,
                          coverage, negative, windows, ledger=None):
            """Manifest one report set, so a control tests only its defect.

            The CLI always reads the committed manifest; here each control
            hands in mutated copies, and a digest mismatch with a stale
            manifest would report the harness's bookkeeping instead of the
            forgery the control stands for.  The controls that attack the
            binding itself name a manifest explicitly.
            """

            manifest_serial[0] += 1
            path = os.path.join(work, f"battery-manifest-{manifest_serial[0]}.json")
            write_battery_manifest(
                path, consumed_set(matrices, crashes, fifo, throughput,
                                   parity, coverage, negative, windows),
                ledger_path=ledger)
            return path

        def consumed_set(matrices, crashes, fifo, throughput, parity,
                         coverage, negative, windows):
            """The role map of one battery, as the manifest records it."""

            return {"matrix": list(matrices), "crash": list(crashes),
                    "crash-negative": list(negative), "fifo-surface": list(fifo),
                    "throughput": list(throughput),
                    "refusal-class-parity": list(parity),
                    "coverage-go": list(coverage),
                    "windows-housekeeping": list(windows)}
        battery_manifest_path = os.path.join(work, "battery-manifest.json")
        write_battery_manifest(
            battery_manifest_path,
            consumed_set(four, [crash_path], battery_fifo, battery_throughput,
                         battery_parity, battery_coverage, battery_negative,
                         battery_windows))
        genuine_manifest_path = os.path.join(
            evidence_dir, BATTERY_MANIFEST_FILE_NAME)

        outer_assess = globals()["assess"]

        # The committed reports all name one revision; the synthetic battery
        # names another.  A control that mutates the genuine reports must be
        # handed the genuine surface pair and a synthetic control the
        # synthetic pair, otherwise the shared-revision rule fires on the
        # harness's own bookkeeping instead of on the mutation under test.
        # Controls that attack that rule pass fifo_paths/throughput_paths
        # explicitly and are unaffected by this choice.
        with open(genuine_matrix_paths[0], encoding="utf-8") as stream:
            committed_revision = _json.load(stream).get("git_head")

        # One table-conforming parity report per revision the battery runs
        # under: the synthetic reports name the synthetic revision, and the
        # mutated copies of the committed reports name the committed one.  A
        # control that mutates another class consumes these instead of the
        # committed artifact, because with the committed artifact every run is
        # rejected for the recorded grid rotation before its own defect is
        # reached, and a control rejected before it is reached proves nothing.
        conforming_parity = [os.path.join(
            work, "refusal-class-parity-synthetic.json")]
        genuine_conforming_parity = [os.path.join(
            work, "refusal-class-parity-conforming.json")]
        assign(conforming_parity[0], parity_report(revision))
        with open(genuine_parity, encoding="utf-8") as stream:
            recorded_binaries = _json.load(stream).get("binaries") or {}
        committed_identities = {
            "go": (recorded_binaries.get("go") or {}).get("sha256"),
            "rust": (recorded_binaries.get("rust") or {}).get("sha256"),
            "fixture": (recorded_binaries.get("fixture_tool")
                        or {}).get("sha256")}
        assign(genuine_conforming_parity[0], parity_report(
            committed_revision, committed_identities,
            binary_records=recorded_binaries))

        # Controls report themselves.  A silent self-test cannot be
        # distinguished from a self-test whose controls never ran, and the
        # gate's value rests on those controls actually executing.
        # Every control records its outcome here instead of asserting in
        # place, and the battery is judged once at the end: an assertion
        # deleted from one helper must not be able to convert an accepted
        # forgery into a passing self-test.  MIN_CONTROLS is the guard
        # against deleting a control, which would otherwise lower the
        # requirement silently instead of failing it.
        results: list = []
        min_controls = 106

        def assess(matrix_paths, crash_paths, **kwargs):
            head = None
            for path in matrix_paths:
                try:
                    with open(path, encoding="utf-8") as stream:
                        head = _json.load(stream).get("git_head")
                except (OSError, ValueError):
                    head = None
                break
            if head == committed_revision:
                kwargs.setdefault("fifo_paths", genuine_fifo)
                kwargs.setdefault("throughput_paths", genuine_throughput)
                kwargs.setdefault("parity_paths", genuine_conforming_parity)
                kwargs.setdefault("coverage_paths", [genuine_coverage])
                kwargs.setdefault("crash_negative_paths",
                                  list(genuine_negative))
                kwargs.setdefault("windows_paths", [genuine_windows])
            else:
                kwargs.setdefault("fifo_paths", battery_fifo)
                kwargs.setdefault("throughput_paths", battery_throughput)
                kwargs.setdefault("parity_paths", battery_parity)
                kwargs.setdefault("coverage_paths", battery_coverage)
                kwargs.setdefault("crash_negative_paths", battery_negative)
                kwargs.setdefault("windows_paths", battery_windows)
            if "battery_manifest" not in kwargs:
                # The binding is regenerated over the reports actually handed
                # in, so a control is rejected for the defect it introduces and
                # not for the harness writing a copy.  The controls that attack
                # the binding name a manifest explicitly.
                kwargs["battery_manifest"] = manifest_over(
                    matrix_paths, crash_paths, kwargs["fifo_paths"],
                    kwargs["throughput_paths"], kwargs["parity_paths"],
                    kwargs["coverage_paths"], kwargs["crash_negative_paths"],
                    kwargs["windows_paths"])
            return outer_assess(matrix_paths, crash_paths, **kwargs)

        # 0a. The surface reports on their own pass the consumed-report rules.
        problems, _c, _s = assess(four, [crash_path])
        assert not problems, f"surface reports failed the gate: {problems}"

        # 0b. Dropping either consumed report is a defect the CLI rejects at
        #     the parser and the gate rejects here: an omitted flag must not
        #     be a way to leave a surface verdict unbound.
        problems, _c, _s = assess(four, [crash_path])
        assert not problems, (
            "a battery with no surface report supplied must still gate the "
            f"matrix and crash evidence: {problems}")

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


        def load_genuine():
            matrices = []
            for path in genuine_matrix_paths:
                with open(path, encoding="utf-8") as stream:
                    matrices.append(_json.load(stream))
            with open(genuine_crash, encoding="utf-8") as stream:
                crash = _json.load(stream)
            return matrices, crash

        problems, _c, _s = outer_assess(
            genuine_matrix_paths, [genuine_crash], fifo_paths=genuine_fifo,
            throughput_paths=genuine_throughput,
            sha256_ledger=_wave_ledger_path())
        blocking = outside_parity_rotation(problems)
        assert not blocking, (
            f"genuine evidence failed the gate: {blocking}")
        parity_rotation = [problem for problem in problems
                           if problem not in blocking]

        _INHERIT = object()

        def genuine_mutation_fails(label, mutator, mutator_kind=None,
                                   ledger=None, manifest_ledger=_INHERIT,
                                   **assess_kwargs):
            """Mutate one committed report and require the gate to reject it.

            ``mutator_kind`` selects which consumed report the mutator edits:
            ``None`` (the historical shape) mutates the four matrices plus the
            crash report, ``"fifo-surface"`` and ``"throughput"`` mutate the
            single surface report of that name.  The remaining reports stay
            genuine so a control isolates one defect.
            """

            matrices, crash = load_genuine()
            fifo_reports = []
            throughput_reports = []
            parity_reports = []
            coverage_reports = []
            negative_reports = []
            windows_reports = []
            for name, bucket, paths in (
                    ("fifo", fifo_reports, genuine_fifo),
                    ("throughput", throughput_reports, genuine_throughput),
                    ("parity", parity_reports, genuine_conforming_parity),
                    ("coverage", coverage_reports, [genuine_coverage]),
                    ("negative", negative_reports, genuine_negative),
                    ("windows", windows_reports, [genuine_windows])):
                for path in paths:
                    with open(path, encoding="utf-8") as stream:
                        bucket.append(_json.load(stream))
            chosen = {"fifo-surface": fifo_reports,
                      "throughput": throughput_reports,
                      "refusal-class-parity": parity_reports,
                      "coverage-go": coverage_reports,
                      "crash-negative": negative_reports,
                      "windows-housekeeping": windows_reports}
            if mutator_kind in chosen:
                mutator(chosen[mutator_kind])
            else:
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
            fifo_paths = []
            for index, report in enumerate(fifo_reports):
                path = os.path.join(
                    work, f"genuine-{label}-fifo-{index}.json")
                assign(path, report)
                fifo_paths.append(path)
            throughput_paths = []
            for index, report in enumerate(throughput_reports):
                path = os.path.join(
                    work, f"genuine-{label}-throughput-{index}.json")
                assign(path, report)
                throughput_paths.append(path)
            negative_paths = assess_kwargs.pop(
                "crash_negative_paths", None) or [
                    os.path.join(work, f"genuine-{label}-neg-{index}.json")
                    for index in range(len(negative_reports))]
            for index, report in enumerate(negative_reports):
                assign(negative_paths[index], report)
            parity_paths = assess_kwargs.pop("parity_paths", None) or [
                os.path.join(work, f"genuine-{label}-parity.json")]
            assign(parity_paths[0], parity_reports[0])
            coverage_paths = assess_kwargs.pop("coverage_paths", None) or [
                os.path.join(work, f"genuine-{label}-coverage.json")]
            assign(coverage_paths[0], coverage_reports[0])
            windows_paths = assess_kwargs.pop("windows_paths", None) or [
                os.path.join(work, f"genuine-{label}-windows.json")]
            assign(windows_paths[0], windows_reports[0])
            if "battery_manifest" not in assess_kwargs:
                # The manifest is normally regenerated over the reports
                # actually handed in, so a control is rejected for the defect
                # it introduces.  ``manifest_ledger`` separates the ledger the
                # binding attests from the ledger the gate is handed: the two
                # halves of that binding are each a control of their own.
                bound = ledger if manifest_ledger is _INHERIT \
                    else manifest_ledger
                assess_kwargs["battery_manifest"] = manifest_over(
                    paths, [crash_mutated], fifo_paths, throughput_paths,
                    parity_paths, coverage_paths, negative_paths,
                    windows_paths, ledger=bound)
            if ledger is not None:
                assess_kwargs.setdefault("sha256_ledger", ledger)
            else:
                # An explicit manifest is the control's own evidence: a string
                # path or an in-memory document both reach the gate as written.
                pass
            problems, _c, _s = outer_assess(
                paths, [crash_mutated], fifo_paths=fifo_paths,
                throughput_paths=throughput_paths,
                parity_paths=parity_paths, coverage_paths=coverage_paths,
                crash_negative_paths=negative_paths,
                windows_paths=windows_paths, **assess_kwargs)
            results.append((label, bool(problems), problems))
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

        # Duplicate-path-different-sha (kind-gate finding): a second
        # binary record that names the same executable path with a
        # different sha256 is a contradictory identity; the pre-fix
        # gate let the later record overwrite the earlier one and
        # accepted the report.
        def duplicate_path_different_sha(matrices, crash):
            report = matrices[2]
            twin = _copy.deepcopy(report["binaries"]["go"])
            twin["sha256"] = "f" * 64
            report["binaries"]["stale_go"] = twin
        genuine_mutation_fails("duplicate-path-different-sha",
                               duplicate_path_different_sha)

        # Duplicate-c-path-different-sha (kind-gate finding): the same
        # contradictory identity inserted through the optional ``c``
        # record and a ``--c`` command argument must fail the gate too.
        def duplicate_c_path_different_sha(matrices, crash):
            report = matrices[2]
            twin = _copy.deepcopy(report["binaries"]["go"])
            twin["sha256"] = "f" * 64
            report["binaries"]["c"] = twin
            report["command"].extend(["--c", twin["path"]])
        genuine_mutation_fails("duplicate-c-path-different-sha",
                               duplicate_c_path_different_sha)

        # Missing-fixture identity (kind-gate finding): a matrix report
        # whose recorded command exercises the fixture must carry the
        # ``fixture_tool`` identity record; popping it must fail the
        # gate instead of being silently accepted.
        def missing_fixture_identity(matrices, crash):
            matrices[2].pop("fixture_tool", None)
        genuine_mutation_fails("missing-fixture-identity",
                               missing_fixture_identity)

        # Step-count contradiction (kind-gate finding): a PASS case
        # actor that records fewer executed steps than its distinct
        # executed methods contradicts the executed-work record
        # (run.py increments the step counter once per executed step).
        def step_count_contradiction(matrices, crash):
            for report in matrices:
                for case in report["cases"]:
                    if case.get("status") != "PASS":
                        continue
                    for actor in case["actors"].values():
                        if actor.get("operations"):
                            actor["steps"] = 1
        genuine_mutation_fails("step-count-contradiction",
                               step_count_contradiction)

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

        # 42. Crash-side fixture identity (external review finding):
        #     the crash root binaries table is the authority for the
        #     battery fixture; a fixture path recorded without its
        #     sha256 must fail the gate (the pre-fix gate skipped the
        #     matrix comparison when the sha was absent).
        def missing_crash_fixture_sha(matrices, crash):
            crash["binaries"].pop("fixture_tool_sha256", None)
        genuine_mutation_fails("crash-fixture-missing-sha",
                               missing_crash_fixture_sha)

        # 43. Cross-crash fixture conflict (external review finding):
        #     two crash reports naming the same fixture path with
        #     different sha256 values contradict each other; the gate
        #     must fail instead of letting a later record silently
        #     override the earlier identity.
        matrices, crash = load_genuine()
        crash_conflict = _copy.deepcopy(crash)
        crash_conflict["binaries"]["fixture_tool_sha256"] = "f" * 64
        paths = []
        for index, report in enumerate(matrices):
            path = os.path.join(work, f"fixture-conflict-{index}.json")
            assign(path, report)
            paths.append(path)
        for label, first, second in (
                ("conflict-first", crash_conflict, crash),
                ("genuine-first", crash, crash_conflict)):
            crash_first = os.path.join(
                work, f"fixture-conflict-{label}-1.json")
            crash_second = os.path.join(
                work, f"fixture-conflict-{label}-2.json")
            assign(crash_first, first)
            assign(crash_second, second)
            problems, _c, _s = assess(
                paths, [crash_first, crash_second])
            assert any("contradicts the earlier identity" in problem
                       for problem in problems), (
                f"cross-crash fixture sha conflict ({label}) did not "
                f"fail the gate: {problems}")

        # 44. Matrix fixture facts bound to the command-selected
        #     fixture (external review finding): a matrix whose
        #     command selects fixture A but whose fixture_tool
        #     metadata claims a different crash-recorded fixture B
        #     (path and hash) is contradictory provenance and must
        #     fail the gate.
        matrices, crash = load_genuine()
        crash_other = _copy.deepcopy(crash)
        crash_other["binaries"]["fixture_tool"] = "/tmp/v4-fixture-b"
        crash_other["binaries"]["fixture_tool_sha256"] = "4" * 64
        other_command = crash_other["command"]
        other_command[other_command.index("--fixture-tool") + 1] = \
            "/tmp/v4-fixture-b"
        matrices[2]["fixture_tool"] = {
            "path": "/tmp/v4-fixture-b", "sha256": "4" * 64}
        paths = []
        for index, report in enumerate(matrices):
            path = os.path.join(work, f"fixture-bound-{index}.json")
            assign(path, report)
            paths.append(path)
        crash_bound = os.path.join(work, "fixture-bound-crash.json")
        crash_other_path = os.path.join(work, "fixture-bound-crash-2.json")
        assign(crash_bound, crash)
        assign(crash_other_path, crash_other)
        problems, _c, _s = assess(
            paths, [crash_bound, crash_other_path])
        assert any("metadata names" in problem for problem in problems), (
            f"matrix fixture facts claiming a fixture the command did "
            f"not select did not fail the gate: {problems}")

        # 45. Positive control for the explicit-actor contract
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
            # The genuine matrices and crash report are being mutated here, so
            # the consumed surface reports must be the genuine pair as well:
            # one revision, one set of measured artifacts.
            problems, _c, _s = assess(paths, [crash_swapped])
            assert not problems, (
                f"actor-swapped database.metadata ledger rejected: "
                f"{problems}")
        actor_swapped_case()

        # 46. A matrix report without command metadata must end with a
        #     recorded problem, not an uncontrolled exception
        #     (external review finding): the gate stays fail-closed
        #     and the diagnosis is stable.  Exercised with both an
        #     empty case list and the populated report shape, because
        #     the per-case executable-binding checks only run when
        #     cases exist (second UnboundLocalError site).
        for populated in (False, True):
            if populated:
                no_command = json.loads(json.dumps(green_report("go")))
            else:
                no_command = matrix_report("go", [], 0)
            del no_command["command"]
            no_command_path = os.path.join(
                work, "matrix-no-command-%s.json" %
                ("pop" if populated else "empty"))
            assign(no_command_path, no_command)
            problems, _c, _s = assess([no_command_path], [])
            assert any("records no command argv" in problem
                       for problem in problems), (
                f"matrix report without command did not record the argv "
                f"problem: {problems}")

        # 47. Command-path resolution is cwd-invariant (external
        #     review finding): the matrix runner records
        #     checkout-contained binary values as checkout-relative
        #     spellings (``.local/qual/...``) while the binary
        #     identity records stay absolute, so the gate must
        #     resolve those values against the checkout root, never
        #     the gate's process cwd.  The committed battery stages
        #     binaries outside the checkout (absolute values are
        #     cwd-invariant by themselves), so the genuine evidence
        #     is re-homed under the checkout first: every binary
        #     path is moved to ``<checkout>/.local/qual/...`` and the
        #     command arrays are rewritten the way the sanitizer
        #     records them (checkout-relative spellings).  Assessed
        #     from a scratch cwd, the pre-fix ``os.path.realpath``
        #     resolution resolved those spellings against the gate's
        #     cwd and rejected the evidence.

        def rehome_strings(node, old_root, new_root):
            """Deep string replacement over a report structure.

            Text replacement after ``json.dumps`` would insert
            unescaped characters (a checkout path containing a quote
            or backslash is legal on POSIX); the rewrite walks the
            decoded structure instead and replaces binary path
            spellings wherever they appear."""
            if isinstance(node, str):
                return node.replace(old_root, new_root)
            if isinstance(node, list):
                return [rehome_strings(item, old_root, new_root)
                        for item in node]
            if isinstance(node, dict):
                return {key: rehome_strings(value, old_root, new_root)
                        for key, value in node.items()}
            return node

        def rehome_evidence(matrices, crash, new_root):
            """Move every binary path of the genuine evidence under
            new_root and rewrite the command arrays the way the
            sanitizer records checkout-contained values (relative
            spellings); returns (matrices, crash) with the recorded
            producer checkout root set on the reports."""
            first_bin = None
            for report in matrices:
                for record in (report.get("binaries") or {}).values():
                    candidate = (record or {}).get("path")
                    if candidate:
                        first_bin = candidate
                        break
                if first_bin:
                    break
            assert first_bin, "genuine evidence records no binary path"
            old_root = os.path.dirname(os.path.dirname(first_bin))
            out_matrices = [rehome_strings(report, old_root, new_root)
                            for report in matrices]
            out_crash = rehome_strings(crash, old_root, new_root)
            return out_matrices, out_crash, old_root

        def cwd_invariant_case():
            matrices, crash, _old = rehome_evidence(
                *load_genuine(), os.path.join(
                    checkout_root(), ".local", "qual"))
            for report in matrices + [crash]:
                report["checkout_root"] = checkout_root()
                command = report.get("command") or []
                for option in ("--rust", "--go", "--fixture-tool"):
                    if option not in command:
                        continue
                    index = command.index(option)
                    command[index + 1] = os.path.relpath(
                        command[index + 1], checkout_root())
            paths = []
            for index, report in enumerate(matrices):
                path = os.path.join(work, f"cwd-invariant-{index}.json")
                assign(path, report)
                paths.append(path)
            crash_invariant = os.path.join(
                work, "cwd-invariant-crash.json")
            assign(crash_invariant, crash)
            saved_cwd = os.getcwd()
            try:
                os.chdir(work)
                problems, _c, _s = assess(paths, [crash_invariant])
            finally:
                os.chdir(saved_cwd)
            assert not problems, (
                f"checkout-relative command paths failed from a "
                f"different cwd: {problems}")
        cwd_invariant_case()

        # 48. Evidence binding follows the producer's recorded
        #     checkout root, not the reviewing checkout (external
        #     review finding): a neutral checkout can legitimately
        #     record checkout-relative command arguments alongside
        #     absolute binary identities; copying those reports to
        #     another clone must not change the verdict.  The
        #     re-homed evidence records a synthetic producer root
        #     (``checkout_root``) that differs from the gate's own
        #     checkout, and the gate assesses it from its own
        #     checkout: with the field the bindings resolve against
        #     the producer root and pass; without the field the
        #     same evidence must fail (the reviewing checkout cannot
        #     name the binaries).
        def cross_checkout_case():
            # A fixed ``owned_temp_root()/qual-producer`` spelling can
            # equal the reviewing checkout (a clone staged at
            # ``/tmp/qual-producer``), which would make the negative
            # control vacuous and block every gate run; the unique
            # per-run scratch directory guarantees a distinct root.
            producer_root = os.path.join(work, "qual-producer")
            matrices, crash, _old = rehome_evidence(
                *load_genuine(),
                os.path.join(producer_root, ".local", "qual"))
            for report in matrices + [crash]:
                report["checkout_root"] = producer_root
                command = report.get("command") or []
                for option in ("--rust", "--go", "--fixture-tool"):
                    if option not in command:
                        continue
                    index = command.index(option)
                    command[index + 1] = os.path.relpath(
                        command[index + 1], producer_root)
            paths = []
            for index, report in enumerate(matrices):
                path = os.path.join(work, f"cross-checkout-{index}.json")
                assign(path, report)
                paths.append(path)
            crash_cross = os.path.join(work, "cross-checkout-crash.json")
            assign(crash_cross, crash)
            problems, _c, _s = assess(paths, [crash_cross])
            assert not problems, (
                f"evidence bound to its recorded producer root failed "
                f"from the reviewing checkout: {problems}")
            # Negative control: without the recorded producer root the
            # relative spellings resolve against the reviewing checkout
            # and the identity binding must fail.
            unrecorded = _copy.deepcopy(crash)
            unrecorded["checkout_root"] = None
            crash_unrecorded = os.path.join(
                work, "cross-checkout-unrecorded.json")
            assign(crash_unrecorded, unrecorded)
            problems, _c, _s = assess(paths, [crash_unrecorded])
            assert any(
                "does not name the report root binaries table path"
                in problem for problem in problems), (
                f"evidence without a recorded producer root did not "
                f"fail the binding from the reviewing checkout: "
                f"{problems}")
        cross_checkout_case()

        # 49. F3 (kind-gate finding, wave-19.18): a fabricated
        #     cross-matrix PASS.  The forgery rewrites the skipped
        #     ``algebra.publish`` case (a producer-only case the mixed
        #     runner skips) into a PASS case carrying another PASS
        #     case's actors, operations and lineage.  The committed
        #     case definitions bind PASS identity: a mixed matrix can
        #     only PASS cases whose definition requires both services,
        #     so the relabeled single-actor case must fail even though
        #     its actors, argv and counters are internally consistent.
        def forged_cross_matrix_pass(matrices, crash):
            report = matrices[2]  # rust_to_go
            donor = next(c for c in report["cases"]
                         if c["status"] == "PASS")
            target = next(c for c in report["cases"]
                          if c["status"] == "SKIP"
                          and c["name"] == "algebra.publish")
            forged = {
                "matrix": report["matrix"], "name": target["name"],
                "status": "PASS",
                "actors": _copy.deepcopy(donor["actors"]),
                "file_kinds": _copy.deepcopy(donor["file_kinds"]),
                "oracle_checks": donor.get("oracle_checks", 0),
            }
            report["cases"] = [
                c for c in report["cases"] if c["name"] != target["name"]
            ] + [forged]
            report["passed"] = sum(
                1 for c in report["cases"] if c["status"] == "PASS")
            report["skipped"] = len(report["cases"]) - report["passed"]
        genuine_mutation_fails("f3-forged-cross-matrix-pass",
                               forged_cross_matrix_pass)

        # 49b. F3 variant: a PASS case name that is not a committed
        #      case definition (case-identity binding, CLI-enabled).
        def invented_case_name(matrices, crash):
            report = matrices[2]
            case = next(c for c in report["cases"]
                        if c["status"] == "PASS")
            case["name"] = "invented.case"
        genuine_mutation_fails("f3-invented-case-name",
                               invented_case_name, verify_cases=True)

        # 49c. F3 variant: a PASS case whose actor records an
        #      executed operation the named case definition never
        #      declares for that actor (case-identity binding,
        #      CLI-enabled).
        def foreign_operation_case(matrices, crash):
            report = matrices[2]
            case = next(c for c in report["cases"]
                        if c["status"] == "PASS")
            case["actors"]["producer"]["operations"].append(
                "iprange.v1.export")
        genuine_mutation_fails("f3-foreign-operation",
                               foreign_operation_case,
                               verify_cases=True)

        # 50. F4 (kind-gate finding, wave-19.18): strip every
        #     live_sidecar open record from the matrix evidence
        #     (per-case lineage, by kind).  The open requirement is
        #     non-vacuous per evidence source: the matrix evidence
        #     itself must show both languages opening live_sidecar,
        #     and the crash evidence cannot repay the missing
        #     matrix-side coverage.
        def stripped_matrix_sidecar_opens(matrices, crash):
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
        genuine_mutation_fails("f4-matrix-sidecar-opens-stripped",
                               stripped_matrix_sidecar_opens)

        # 51. F5 (kind-gate finding, wave-19.18): strip the consumer
        #     opens from the go->rust crash scenarios.  The crash
        #     evidence must itself prove both consumer languages
        #     opening v4_main and live_sidecar through the scenarios'
        #     own executed open records.
        def stripped_crash_consumer_opens(matrices, crash):
            stripped = 0
            for scenario in crash["scenarios"]:
                if "->rust" in scenario.get("scenario", "") \
                        and stripped < 8:
                    for facts in (scenario.get("kinds") or {}).values():
                        if not isinstance(facts, dict):
                            continue
                        facts["opened_by"] = [
                            ref for ref in facts.get("opened_by", [])
                            if not ref.startswith("consumer")]
                    open_facts = scenario.setdefault(
                        "live_reader_opens", {})
                    open_facts["consumer"] = 0
                    stripped += 1
        genuine_mutation_fails("f5-crash-consumer-opens-stripped",
                               stripped_crash_consumer_opens)

        # 52. F7 (kind-gate finding, wave-19.18): relabel the Go
        #     binary record's top-level ``implementation`` to rust
        #     while its system.describe result still declares go.
        #     Language attribution comes from the capability result,
        #     never from labels: a root-level label that contradicts
        #     the result is a relabel attack.
        def relabel_binary_record_root(matrices, crash):
            matrices[1]["binaries"]["go"]["implementation"] = "rust"
        genuine_mutation_fails("f7-binary-record-root-relabel",
                               relabel_binary_record_root)

        # 53. F11 (kind-gate finding, wave-19.18): bind the Go
        #     binary record, command and per-case argv to a
        #     nonexistent path while keeping the recorded sha256.
        #     With on-disk binary verification enabled (the CLI
        #     default) the recorded executable must exist and match;
        #     a recorded path that does not exist is a report defect.
        def missing_binary_path(matrices, crash):
            report = matrices[1]  # matrix-go
            report["binaries"]["go"]["path"] = "/nonexistent/iprange-go"
            report["command"] = [
                "/nonexistent/iprange-go"
                if arg == "/tmp/qualsvc/w1916/bin/go/iprange" else arg
                for arg in report["command"]]
            for case in report["cases"]:
                if case.get("status") != "PASS":
                    continue
                for entry in (case.get("actors") or {}).values():
                    if entry.get("argv") == \
                            "/tmp/qualsvc/w1916/bin/go/iprange":
                        entry["argv"] = "/nonexistent/iprange-go"
        genuine_mutation_fails("f11-binary-path-missing",
                               missing_binary_path,
                               verify_binaries=True)

        # 55. Cross-language export attestation (closure F2).  The
        #     acceptance criterion names export among the obligations the
        #     battery must prove; until now zero cross-language export
        #     evidence existed and nothing noticed.  Stripping the
        #     consumer's export of the producer's artifact, or the digest
        #     records themselves, must fail the battery.
        def strip_export_digests(matrices, crash):
            for report in matrices:
                for case in report["cases"]:
                    if case.get("status") != "PASS":
                        continue
                    case.pop("digest_groups", None)
        genuine_mutation_fails("export-digests-dropped",
                               strip_export_digests)

        def consumer_only_export(matrices, crash):
            for report in matrices:
                for case in report["cases"]:
                    groups = case.get("digest_groups")
                    if not isinstance(groups, dict):
                        continue
                    for group, entries in groups.items():
                        keep = [entry for entry in entries
                                if entry.get("actor") == "consumer"]
                        groups[group] = keep or entries
        genuine_mutation_fails("export-producer-role-dropped",
                               consumer_only_export)

        # A digest group whose two records no longer name the same bytes
        # is the report-level form of "the engines disagree", so the group
        # must fail rather than report two passing artifacts.
        def diverged_digest(matrices, crash):
            for report in matrices:
                for case in report["cases"]:
                    for entries in (case.get("digest_groups") or {}).values():
                        if len(entries) > 1:
                            entries[1]["sha256"] = "0" * 64
                            return
        genuine_mutation_fails("export-digests-diverged", diverged_digest)

        # Removing the export opens for one language in the crash evidence
        # as well as the matrix evidence is what empties a required-opened
        # kind; either source alone still carries the obligation.
        def adapter_never_opened(matrices, crash):
            for report in matrices:
                for case in report["cases"]:
                    for facts in case.get("file_kinds", {}).values():
                        if facts.get("kind") == "adapter_output":
                            facts["opened_by"] = []
            for scenario in crash.get("scenarios", []):
                for kind, facts in scenario.get("kinds", {}).items():
                    if kind == "adapter_output":
                        facts["opened_by"] = []
        genuine_mutation_fails("adapter-output-never-opened",
                               adapter_never_opened)

        # 56. Params-validator attestation (security F2).  The corpus now
        #     has an explicit negative-params mode; these controls prove
        #     the records behind it are load-bearing rather than decorative.
        def drop_one_negative(matrices, crash):
            for report in matrices:
                for case in report["cases"]:
                    rejected = case.get("params_rejected")
                    if isinstance(rejected, list) and len(rejected) > 0:
                        rejected.pop()
                        return
        genuine_mutation_fails("params-negative-record-dropped",
                               drop_one_negative)

        def invent_negative(matrices, crash):
            for report in matrices:
                for case in report["cases"]:
                    if case.get("status") != "PASS":
                        continue
                    case.setdefault("params_rejected", []).append({
                        "actor": "producer",
                        "method": "iprange.v1.system.describe",
                        "transport_code": -32602,
                        "request": "{\"jsonrpc\":\"2.0\"}"})
                    return
        genuine_mutation_fails("params-negative-invented", invent_negative)

        def flip_negative_code(matrices, crash):
            for report in matrices:
                for case in report["cases"]:
                    for entry in case.get("params_rejected") or []:
                        entry["transport_code"] = -32010
                        return
        genuine_mutation_fails("params-negative-code-flipped",
                               flip_negative_code)

        def strip_negative_request(matrices, crash):
            for report in matrices:
                for case in report["cases"]:
                    for entry in case.get("params_rejected") or []:
                        entry["request"] = ""
                        return
        genuine_mutation_fails("params-negative-request-stripped",
                               strip_negative_request)

        # 57. The declared-defect ledger is only honest in both
        #     directions: an undeclared failure must fail, and a declared
        #     defect that silently PASSes must fail too.  Controls live
        #     here because the ledger is a gate input, not a report field.
        def undeclared_failure(matrices, crash):
            report = matrices[0]
            for case in report["cases"]:
                if case.get("status") == "PASS":
                    case["status"] = "FAIL"
                    case["error"] = "injected"
                    report["failed"] = report.get("failed", 0) + 1
                    return
        genuine_mutation_fails("undeclared-failure", undeclared_failure)

        stale_case = None
        for report in json.load(open(
                os.path.join(evidence_dir, KNOWN_DEFECTS_FILE_NAME))
        ).get("defects", []):
            stale_case = report
            break
        stale_entry_injected = stale_case is None
        if stale_entry_injected:
            # An empty ledger is the battery's normal (all-green) state, so a
            # control that registered only while somebody had parked a defect
            # would silently shrink the battery by one whenever the ledger was
            # cleared -- and the exact-count assertion at the end of this
            # battery would then abort the gate.  Supply a well-formed entry
            # for a case this battery genuinely runs, and consult it through
            # the same cache the ledger reader uses, so the stale-entry rule
            # is tested whether or not the committed ledger happens to be
            # populated.
            _stale_report = json.load(open(genuine_matrix_paths[0]))
            _stale_name = next(
                case.get("name") for case in _stale_report.get("cases", [])
                if case.get("status") == "PASS")
            stale_case = {"matrix": _stale_report.get("matrix"),
                          "case": _stale_name, "owner": "self-test",
                          "finding": "control: a declared defect that PASSes "
                                     "is a stale entry",
                          "observed": "fabricated for the control"}

        def declared_defect_passes(matrices, crash, _entry=stale_case):
            for report in matrices:
                if report.get("matrix") != _entry["matrix"]:
                    continue
                for case in report["cases"]:
                    if case.get("name") == _entry["case"]:
                        case["status"] = "PASS"
                        case.pop("error", None)
                        report["failed"] = max(
                            0, report.get("failed", 1) - 1)
                        return

        if stale_entry_injected:
            # The ledger reader caches the committed file, so the synthetic
            # entry is supplied by binding that cache for the duration of the
            # control.  Binding happens in a nested scope because this
            # function already declares the name global further down, and
            # Python rejects a second declaration after an assignment.
            def _bind_ledger(entries):
                global _KNOWN_DEFECTS_CACHE
                _KNOWN_DEFECTS_CACHE = entries

            _saved_ledger = _known_defects()
            try:
                _bind_ledger({(stale_case["matrix"], stale_case["case"]):
                              stale_case})
                genuine_mutation_fails("stale-known-defect",
                                       declared_defect_passes)
            finally:
                _bind_ledger(_saved_ledger)
        else:
            genuine_mutation_fails("stale-known-defect",
                                   declared_defect_passes)

        # 58. A required-opened kind may not satisfy its obligation from
        #     crash evidence alone: the matrix-side opens are the
        #     non-vacuous requirement, and removing them for one language
        #     is exactly the blind spot the hardcoded pair used to allow.
        def matrix_side_opens_only(matrices, crash):
            # Open records survive in the crash evidence only, which is
            # what the hardcoded two-kind list used to allow: crash
            # scenarios open these kinds too, so the aggregate looks
            # complete while no matrix case ever did.
            for report in matrices:
                for case in report["cases"]:
                    for facts in case.get("file_kinds", {}).values():
                        if facts.get("kind") in REQUIRED_OPENED_KINDS:
                            facts["opened_by"] = []
        genuine_mutation_fails("matrix-opens-stripped-crash-only",
                               matrix_side_opens_only)

        # 54. Negative control for the strengthened gate: the genuine
        #     committed evidence must still pass with BOTH CLI
        #     verification modes enabled (case identity + on-disk
        #     binary binding).  This is the exact CLI configuration;
        #     the earlier genuine-pass assert covers the
        #     self-test defaults only.
        # 60. Wave-19.24 controls.  Each of these mutations was ACCEPTED by
        #     the pre-fix gate on genuine evidence, and each was reported by a
        #     reviewing role at the wave-19.23 revision: the git_head field
        #     carried no weight at all (security), the known-defects ledger was
        #     never consulted for a green report so a fabricated entry alongside
        #     an all-PASS battery produced zero problems (security), deleting
        #     44 of 49 PASS rows still passed (glm F5c), and the two surface
        #     reports were identity-free, so a wrong or missing sha256, a
        #     foreign implementation label, or an inflated rate with a
        #     fabricated census all passed (performance).
        def load_surface():
            reports = {}
            for path in genuine_fifo + genuine_throughput:
                with open(path, encoding="utf-8") as stream:
                    reports[path] = _json.load(stream)
            return reports

        def assess_with_surfaces(label, mutate_matrices, mutate_surfaces,
                                 mutate_surfaces_extra=None, ledger=None):
            matrices, crash = load_genuine()
            if mutate_matrices is not None:
                mutate_matrices(matrices, crash)
            surfaces = load_surface()
            if mutate_surfaces is not None:
                mutate_surfaces(surfaces)
            paths = []
            for index, report in enumerate(matrices):
                path = os.path.join(work, f"w24-{label}-{index}.json")
                assign(path, report)
                paths.append(path)
            crash_path_local = os.path.join(work, f"w24-{label}-crash.json")
            assign(crash_path_local, crash)
            fifo_paths = []
            for index, path in enumerate(genuine_fifo):
                target = os.path.join(work, f"w24-{label}-fifo-{index}.json")
                assign(target, surfaces[path])
                fifo_paths.append(target)
            throughput_paths = []
            for index, path in enumerate(genuine_throughput):
                target = os.path.join(
                    work, f"w24-{label}-throughput-{index}.json")
                assign(target, surfaces[path])
                throughput_paths.append(target)
            extra = mutate_surfaces_extra or {}
            parity_paths = [extra.get("parity")
                            or os.path.join(work, f"w24-{label}-parity.json")]
            # The copy that carries the committed revision: the matrices,
            # crash, coverage and Windows reports handed to this control are
            # the committed ones, and a synthetic-revision parity report
            # beside them would be rejected for the harness's own revision
            # split instead of for the defect the control stands for.
            assign(parity_paths[0], extra.get("parity_report")
                   or _json.load(open(genuine_conforming_parity[0],
                                       encoding="utf-8")))
            coverage_paths = [extra.get("coverage")
                             or os.path.join(work, f"w24-{label}-coverage.json")]
            assign(coverage_paths[0], extra.get("coverage_report")
                   or _json.load(open(genuine_coverage, encoding="utf-8")))
            negative_paths = extra.get("crash_negative") or [
                os.path.join(work, f"w24-{label}-neg-{index}.json")
                for index in range(len(genuine_negative))]
            for index, source in enumerate(genuine_negative):
                if extra.get("crash_negative_reports") is None:
                    assign(negative_paths[index],
                           _json.load(open(source, encoding="utf-8")))
                else:
                    assign(negative_paths[index],
                           extra["crash_negative_reports"][index])
            windows_paths = [extra.get("windows")
                             or os.path.join(work, f"w24-{label}-windows.json")]
            assign(windows_paths[0], extra.get("windows_report")
                   or _json.load(open(genuine_windows, encoding="utf-8")))
            manifest = extra.get("battery_manifest") or manifest_over(
                paths, [crash_path_local], fifo_paths, throughput_paths,
                parity_paths, coverage_paths, negative_paths, windows_paths,
                ledger=ledger)
            problems, _c, _s = outer_assess(
                paths, [crash_path_local], fifo_paths=fifo_paths,
                throughput_paths=throughput_paths,
                parity_paths=parity_paths, coverage_paths=coverage_paths,
                crash_negative_paths=negative_paths,
                windows_paths=windows_paths, battery_manifest=manifest,
                sha256_ledger=ledger,
                verify_binaries=True, verify_cases=True)
            # Recorded, not asserted here: the verdict is taken once at the
            # end of the battery, so dropping an assertion in one helper
            # cannot turn an accepted forgery into a passing self-test.
            results.append((label, bool(problems), problems))
            return problems

        def set_all_heads(matrices_and_crash, value):
            for report in matrices_and_crash:
                if value is None:
                    report.pop("git_head", None)
                else:
                    report["git_head"] = value

        def surfaces_set_head(surfaces, value):
            for report in surfaces.values():
                if value is None:
                    report.pop("git_head", None)
                else:
                    report["git_head"] = value

        # N1: an all-zeros revision everywhere.  A placeholder is not a
        # measurement, and a report that names no real commit cannot bind a
        # verdict to a tree.
        zeros = "0" * 40
        assess_with_surfaces(
            "git-head-all-zeros",
            lambda m, c: set_all_heads(m + [c], zeros),
            lambda s: surfaces_set_head(s, zeros))
        assess_with_surfaces(
            "git-head-all-ones",
            lambda m, c: set_all_heads(m + [c], "1" * 40),
            lambda s: surfaces_set_head(s, "1" * 40))

        # N2: the field deleted from every consumed report.
        assess_with_surfaces(
            "git-head-deleted",
            lambda m, c: set_all_heads(m + [c], None),
            lambda s: surfaces_set_head(s, None))

        # N3: one report desynced from the rest.  This is the shape of
        # evidence copied in from an earlier battery while the rest is fresh.
        assess_with_surfaces(
            "git-head-desynced-matrix",
            lambda m, c: m[0].__setitem__("git_head", "a" * 40),
            None)
        assess_with_surfaces(
            "git-head-desynced-fifo",
            None,
            lambda s: surfaces_set_head(s, "b" * 40))
        assess_with_surfaces(
            "git-head-malformed-crash",
            lambda m, c: set_all_heads([c], "cfbee78"),
            None)

        # F5c: 44 of the 49 PASS rows deleted from one matrix.  The survivors
        # still cover every required kind, which is exactly why kind coverage
        # alone could not see this.
        def delete_pass_rows(matrices, crash):
            report = matrices[0]
            passed = [case for case in report["cases"]
                      if case.get("status") == "PASS"]
            keep = {case["name"] for case in passed[:5]}
            report["cases"] = [case for case in report["cases"]
                               if case.get("status") != "PASS"
                               or case["name"] in keep]
            report["passed"] = len([case for case in report["cases"]
                                    if case.get("status") == "PASS"])

        problems = assess_with_surfaces("pass-rows-deleted",
                                        delete_pass_rows, None)
        assert any("has no row" in problem for problem in problems), (
            f"deleting 44 of 49 PASS rows must be reported as missing rows, "
            f"got {problems}")

        # A single deleted row is already enough: the corpus is an obligation,
        # not a sample.
        def delete_one_row(matrices, crash):
            report = matrices[2]
            report["cases"] = [case for case in report["cases"]
                               if case.get("name") != "reader.open_missing"]
            report["skipped"] = sum(1 for case in report["cases"]
                                    if case.get("status") == "SKIP")

        assess_with_surfaces("one-skip-row-deleted", delete_one_row, None)

        # Surface identity forgeries.  Each was accepted while the surface
        # reports carried an unchecked sha256 and implementation label.
        def wrong_sha(surfaces):
            for path in genuine_fifo:
                surfaces[path]["binaries"]["rust"]["sha256"] = "f" * 64

        assess_with_surfaces("fifo-wrong-sha", None, wrong_sha)

        def missing_sha(surfaces):
            for path in genuine_fifo:
                surfaces[path]["binaries"]["go"].pop("sha256", None)

        assess_with_surfaces("fifo-missing-sha", None, missing_sha)

        def foreign_label(surfaces):
            for path in genuine_fifo:
                surfaces[path]["binaries"]["go"]["implementation"] = "rust"

        assess_with_surfaces("fifo-foreign-implementation", None, foreign_label)

        def foreign_fixture(surfaces):
            # A fixture identity the crash report does not record: the arm
            # table consumed artifacts the battery cannot account for.
            for path in genuine_fifo:
                surfaces[path]["binaries"]["fixture_tool"] = {
                    "path": "/tmp/fixture.iprange", "sha256": "e" * 64,
                    "implementation": "rust"}

        assess_with_surfaces("fifo-foreign-fixture", None, foreign_fixture)

        def dead_sha(surfaces):
            for path in genuine_fifo:
                surfaces[path]["binaries"]["go"]["sha256"] = "0" * 64

        assess_with_surfaces("fifo-unexecuted-sha", None, dead_sha)

        def inflated_rate(surfaces):
            # 9,999,999 replies/s with a census written by hand: the number is
            # arithmetic, so it must follow from its own round list.
            for path in genuine_throughput:
                record = surfaces[path]["product"]["go"]
                record["median_replies_per_s"] = 9999999
                for entry in record["rounds"]:
                    entry["replies_per_s"] = 9999999

        assess_with_surfaces("throughput-inflated-rate", None, inflated_rate)

        def fabricated_census(surfaces):
            # Replies and requests disagree with the printed rate, and the
            # census is copied from an unrelated run.
            for path in genuine_throughput:
                record = surfaces[path]["product"]["go"]
                record["rounds"][0]["replies"] = 1
                record["thread_structure"]["large"]["unique_child_tids"] = 0

        assess_with_surfaces("throughput-fabricated-census", None,
                             fabricated_census)

        def throughput_foreign_label(surfaces):
            for path in genuine_throughput:
                surfaces[path]["product"]["rust"]["implementation"] = "go"

        assess_with_surfaces("throughput-foreign-implementation", None,
                             throughput_foreign_label)

        def throughput_missing_sha(surfaces):
            for path in genuine_throughput:
                surfaces[path]["product"]["go"].pop("sha256", None)

        assess_with_surfaces("throughput-missing-sha", None,
                             throughput_missing_sha)

        def throughput_absent_census(surfaces):
            for path in genuine_throughput:
                surfaces[path]["product"]["rust"]["thread_structure"] = None

        assess_with_surfaces("throughput-no-thread-census", None,
                             throughput_absent_census)

        # The known-defects ledger, consulted even when the report is green.
        # An entry naming a case that did not fail is a stale entry: it parks
        # a future regression on a defect nobody is fixing any more.  The
        # committed ledger is normally empty, so the control supplies its own
        # well-formed entry and requires that the entry actually be consulted.
        global _KNOWN_DEFECTS_CACHE
        saved_ledger = _known_defects()
        ledger_case = "reader.open_missing"
        try:
            _KNOWN_DEFECTS_CACHE = {
                ("rust", ledger_case): {
                    "matrix": "rust", "case": ledger_case,
                    "owner": "self-test", "finding": "control",
                    "observed": "fabricated for the control",
                }}
            problems, _c, _s = outer_assess(
                genuine_matrix_paths, [genuine_crash],
                fifo_paths=genuine_fifo, throughput_paths=genuine_throughput)
            # Match the entry the control itself injected, by case name and
            # by the verdict word, so this cannot be satisfied by the
            # unrelated "undeclared or stale failed case(s)" counter that the
            # unlisted-FAIL path emits.  Matching on the bare word "stale"
            # let this control pass with the ledger never consulted at all.
            stale = [problem for problem in problems
                     if f"{ledger_case!r}" in problem
                     and "did not FAIL" in problem]
            assert stale, (
                "a well-formed known-defects entry beside an all-PASS "
                f"battery was accepted without naming the stale entry "
                f"{ledger_case!r}: {problems}")
        finally:
            _KNOWN_DEFECTS_CACHE = saved_ledger

        # ...and the reverse direction still works: an unlisted FAIL is a
        # defect, not coverage.
        def make_row_fail(matrices, crash):
            for case in matrices[0]["cases"]:
                if case.get("name") == ledger_case:
                    case["status"] = "FAIL"
                    matrices[0]["failed"] = 1
                    matrices[0]["passed"] = sum(
                        1 for entry in matrices[0]["cases"]
                        if entry.get("status") == "PASS")
                    return

        assess_with_surfaces("undeclared-fail-row", make_row_fail, None)

        # ==================================================================
        # Wave-19.25 controls (items 1-7).  Each mutation below was ACCEPTED
        # by the wave-19.24 gate on genuine evidence and named by a reviewing
        # role; the control runs that exact forgery and requires a rejection,
        # so the hardening itself is regression-tested.
        # ==================================================================

        # --- item 1: the refusal-class parity verdict.
        def parity_one_binary(reports):
            # The --go <rust binary> sweep: one executable run twice, and the
            # agreement it reports is a binary agreeing with itself.
            for report in reports:
                report["binaries"]["go"]["sha256"] = (
                    report["binaries"]["rust"]["sha256"])

        genuine_mutation_fails("parity-single-binary-sweep",
                               parity_one_binary,
                               mutator_kind="refusal-class-parity")

        def parity_swap_digests(reports):
            for report in reports:
                go = report["binaries"]["go"]["sha256"]
                report["binaries"]["go"]["sha256"] = (
                    report["binaries"]["rust"]["sha256"])
                report["binaries"]["rust"]["sha256"] = go

        genuine_mutation_fails("parity-engine-digests-swapped",
                               parity_swap_digests,
                               mutator_kind="refusal-class-parity")

        def parity_foreign_label(reports):
            for report in reports:
                report["binaries"]["go"]["implementation"] = "rust"

        genuine_mutation_fails("parity-foreign-implementation-label",
                               parity_foreign_label,
                               mutator_kind="refusal-class-parity")

        def parity_shrunken_grid(reports):
            # Drop a cell the pin table does not oblige and restate the
            # counters, so only the derived grid can reveal the gap.
            for report in reports:
                pinned = {(pin.get("arm"), pin.get("path_kind"))
                          for pin in report.get("pins") or []}
                victim = next(cell for cell in report["cells"]
                              if (cell["arm"], cell["path_kind"])
                              not in pinned)
                report["cells"] = [cell for cell in report["cells"]
                                   if cell is not victim]
                report["summary"]["cells_executed"] -= 1
                report["summary"]["agreements"] -= 1

        genuine_mutation_fails("parity-shrunken-grid-restamped",
                               parity_shrunken_grid,
                               mutator_kind="refusal-class-parity")

        def parity_invented_cell(reports):
            for report in reports:
                donor = report["cells"][0]
                invented = _json.loads(_json.dumps(donor))
                invented["arm"] = "reader.open"
                invented["path_kind"] = "invented-path-kind"
                invented["go"]["arm"] = invented["arm"]
                invented["go"]["path_kind"] = invented["path_kind"]
                invented["rust"]["arm"] = invented["arm"]
                invented["rust"]["path_kind"] = invented["path_kind"]
                report["cells"].append(invented)
                report["summary"]["cells_executed"] += 1
                report["summary"]["agreements"] += 1

        genuine_mutation_fails("parity-invented-cell", parity_invented_cell,
                               mutator_kind="refusal-class-parity")

        def parity_no_pins(reports):
            # The pinned-refusal table is an obligation: a report may answer
            # it, restate it, or omit it, and omitting it must not be a way
            # to make the verdict smaller than the table.
            for report in reports:
                report["pins"] = []
                report["summary"]["pins_expected"] = 0
                report["summary"]["pins_satisfied"] = 0

        genuine_mutation_fails("parity-pins-deleted", parity_no_pins,
                               mutator_kind="refusal-class-parity")

        def parity_pin_expectation_rewritten(reports):
            for report in reports:
                victim = report["pins"][0]
                victim["expected_data_code"] = "not_a_class_any_engine_gives"

        genuine_mutation_fails("parity-pin-expectation-rewritten",
                               parity_pin_expectation_rewritten,
                               mutator_kind="refusal-class-parity")

        def parity_hidden_divergence(reports):
            for report in reports:
                pinned = {(pin.get("arm"), pin.get("path_kind"))
                          for pin in report.get("pins") or []}
                for cell in report["cells"]:
                    if (cell["arm"], cell["path_kind"]) in pinned:
                        continue
                    cell["rust"]["data_code"] = "policy_denied"
                    break

        genuine_mutation_fails("parity-divergence-recorded-as-agreement",
                               parity_hidden_divergence,
                               mutator_kind="refusal-class-parity")

        def parity_declared_divergence(reports):
            for report in reports:
                report["divergences"] = [{"arm": report["cells"][0]["arm"],
                                          "path_kind":
                                              report["cells"][0]["path_kind"]}]

        genuine_mutation_fails("parity-divergence-under-a-pass-verdict",
                               parity_declared_divergence,
                               mutator_kind="refusal-class-parity")

        def parity_strip_evidence(reports):
            for report in reports:
                for cell in report["cells"]:
                    if cell["go"].get("publication_evidence") is not None:
                        cell["go"]["publication_evidence"] = None
                        break

        genuine_mutation_fails("parity-facts-without-evidence",
                               parity_strip_evidence,
                               mutator_kind="refusal-class-parity")

        def parity_null_evidence_member(reports):
            for report in reports:
                for cell in report["cells"]:
                    evidence = cell["go"].get("publication_evidence")
                    if isinstance(evidence, dict):
                        evidence["sha256"] = None
                        break

        genuine_mutation_fails("parity-evidence-without-a-digest",
                               parity_null_evidence_member,
                               mutator_kind="refusal-class-parity")

        def parity_visible_before_rename(reports):
            for report in reports:
                for cell in report["cells"]:
                    evidence = cell["go"].get("publication_evidence")
                    if isinstance(evidence, dict):
                        evidence["stage"] = "open temporary for write"
                        break

        genuine_mutation_fails("parity-visible-at-pre-visibility-stage",
                               parity_visible_before_rename,
                               mutator_kind="refusal-class-parity")

        def parity_forged_pin_table(reports):
            for report in reports:
                report["grid"]["pinned_table_sha256"] = "0" * 64

        genuine_mutation_fails("parity-forged-pinned-table-fingerprint",
                               parity_forged_pin_table,
                               mutator_kind="refusal-class-parity",
                               ledger=_wave_ledger_path())

        # --- item 2: the Go coverage measurement.
        def coverage_percent_lifted(reports):
            for report in reports:
                report["unit"]["percent"]["statements"] += 5.0

        genuine_mutation_fails("coverage-percent-not-derived",
                               coverage_percent_lifted,
                               mutator_kind="coverage-go")

        def coverage_counts_lifted(reports):
            for report in reports:
                report["unit"]["measured"]["covered_statements"] += 500

        genuine_mutation_fails("coverage-counts-doctored",
                               coverage_counts_lifted,
                               mutator_kind="coverage-go")

        def coverage_not_instrumented(reports):
            for report in reports:
                for record in (report.get("instrumented_binaries") or {}).values():
                    record["instrumented"] = False

        genuine_mutation_fails("coverage-build-not-instrumented",
                               coverage_not_instrumented,
                               mutator_kind="coverage-go")

        with open(genuine_coverage, encoding="utf-8") as stream:
            _committed_coverage = _json.load(stream)
        _instrumented_digest = sorted(
            record["sha256"]
            for record in (_committed_coverage.get("instrumented_binaries")
                           or {}).values()
            if isinstance(record, dict) and _is_sha256(record.get("sha256")))[0]

        def instrumented_attests_rate(surfaces):
            # The coverage build carries counters into the binary; naming it as
            # the artifact a rate was measured on means the attestation
            # describes a build that is not the one that shipped.
            for path in genuine_throughput:
                surfaces[path]["product"]["go"]["coverage_build_sha256"] = (
                    _instrumented_digest)

        assess_with_surfaces("coverage-build-attests-throughput", None,
                             instrumented_attests_rate)

        # --- item 3: the crash batteries.
        def crash_pass_keeps_failures(matrices, crash):
            for scenario in crash["scenarios"]:
                if scenario.get("pass") is True:
                    scenario["failures"] = ["resolved: peer restarted clean"]
                    return

        genuine_mutation_fails("crash-pass-with-recorded-failures",
                               crash_pass_keeps_failures)

        def crash_pass_unbounded(matrices, crash):
            for scenario in crash["scenarios"]:
                if scenario.get("pass") is True:
                    scenario["residue_bounded"] = False
                    return

        genuine_mutation_fails("crash-pass-with-unbounded-residue",
                               crash_pass_unbounded)

        def crash_pass_failures_deleted(matrices, crash):
            for scenario in crash["scenarios"]:
                if scenario.get("pass") is True:
                    scenario.pop("failures", None)
                    return

        genuine_mutation_fails("crash-pass-without-a-failures-list",
                               crash_pass_failures_deleted)

        def negative_reports_pass(reports):
            # A PASS in a negative control is the finding: the faked peer
            # answered, or the run stopped being what its name claims.
            for report in reports:
                for scenario in report["scenarios"]:
                    scenario["pass"] = True
                    scenario["failures"] = []
                report["failed"] = 0

        genuine_mutation_fails("crash-negative-scenarios-pass",
                               negative_reports_pass,
                               mutator_kind="crash-negative")

        def negative_failures_silenced(reports):
            for report in reports:
                for scenario in report["scenarios"]:
                    scenario["failures"] = []

        genuine_mutation_fails("crash-negative-without-reasons",
                               negative_failures_silenced,
                               mutator_kind="crash-negative")

        # --- item 4: the FIFO refusal surface inventory.
        def fifo_drop_one_arm(surfaces):
            for path in genuine_fifo:
                report = surfaces[path]
                victim = next(row for row in report["arms"]
                              if row["engine"] == "go")
                report["arms"] = [row for row in report["arms"]
                                  if row is not victim]
                report["summary"]["arms_expected"] -= 1

        assess_with_surfaces("fifo-arm-engine-row-dropped", None,
                             fifo_drop_one_arm)

        def fifo_expected_rewritten(surfaces):
            import check_fifo_surface as _surface
            for path in genuine_fifo:
                for row in surfaces[path]["arms"]:
                    other = next(code for code in set(
                        _surface.ARM_EXPECTED.values())
                        if code != row["expected_code"])
                    row["expected_code"] = other
                    row["data_code"] = other
                    break
                break

        assess_with_surfaces("fifo-expected-code-rewritten", None,
                             fifo_expected_rewritten)

        def fifo_answer_rewritten(surfaces):
            import check_fifo_surface as _surface
            for path in genuine_fifo:
                for row in surfaces[path]["arms"]:
                    row["data_code"] = next(
                        code for code in set(_surface.ARM_EXPECTED.values())
                        if code != row["expected_code"])
                    break
                break

        assess_with_surfaces("fifo-answer-contradicts-its-own-expectation",
                             None, fifo_answer_rewritten)

        # --- item 5: the throughput census against its own baseline.
        def census_over_report_baseline(surfaces):
            for path in genuine_throughput:
                side = surfaces[path]["product"]["rust"]["thread_structure"]
                side["large"]["clone_syscalls"] = 300
                side["large"]["unique_child_tids"] = 300

        assess_with_surfaces("throughput-child-count-over-baseline", None,
                             census_over_report_baseline)

        def census_widened_baseline(surfaces):
            for path in genuine_throughput:
                surfaces[path]["method"]["thread_baseline_max"] = 4096

        assess_with_surfaces("throughput-baseline-widened-in-report", None,
                             census_widened_baseline)

        def census_one_child_identity(surfaces):
            for path in genuine_throughput:
                surfaces[path]["product"]["rust"]["thread_structure"][
                    "large"]["unique_child_tids"] = 1

        assess_with_surfaces("throughput-threaded-round-single-identity",
                             None, census_one_child_identity)

        def census_tids_past_clones(surfaces):
            for path in genuine_throughput:
                surfaces[path]["product"]["rust"]["thread_structure"][
                    "large"]["unique_child_tids"] = 40

        assess_with_surfaces("throughput-child-ids-exceed-clones", None,
                             census_tids_past_clones)

        def census_clones_grow_with_requests(surfaces):
            for path in genuine_throughput:
                side = surfaces[path]["product"]["rust"]["thread_structure"]
                side["large"]["clone_syscalls"] = 40
                side["large"]["unique_child_tids"] = 40

        assess_with_surfaces("throughput-reply-path-spawns", None,
                             census_clones_grow_with_requests)

        def round_shorter_than_plan(surfaces):
            for path in genuine_throughput:
                rounds = surfaces[path]["product"]["rust"]["rounds"]
                rounds[0]["requests"] = 5000
                rounds[0]["replies"] = 5000
                rounds[0]["seconds"] = rounds[0]["replies"] / float(
                    rounds[0]["replies_per_s"])

        assess_with_surfaces("throughput-round-shorter-than-declared-plan",
                             None, round_shorter_than_plan)

        def rounds_are_errors(surfaces):
            for path in genuine_throughput:
                for entry in surfaces[path]["product"]["rust"]["rounds"]:
                    entry["successful_replies"] = 200
                    entry["error_replies"] = entry["replies"] - 200
                    entry["success_ratio"] = 200 / float(entry["replies"])

        assess_with_surfaces("throughput-error-frames-counted-as-replies",
                             None, rounds_are_errors)

        # --- item 6: the native Windows qualification.
        def windows_drop_cargo_record(reports):
            for report in reports:
                del report["build_provenance"]["native_cargo_test"]

        genuine_mutation_fails("windows-native-cargo-test-unrecorded",
                               windows_drop_cargo_record,
                               mutator_kind="windows-housekeeping")

        def windows_record_red(reports):
            for report in reports:
                report["build_provenance"]["native_go_test"] = (
                    "go test ./... on the Windows host: 4 packages FAILED, "
                    "rc=1 (known portability gaps)")

        genuine_mutation_fails("windows-native-test-names-failure",
                               windows_record_red,
                               mutator_kind="windows-housekeeping")

        def windows_go_toolchain(reports):
            for report in reports:
                report["build_provenance"]["toolchain"]["go"] = (
                    "go version go1.26.5 linux/amd64")

        genuine_mutation_fails("windows-go-toolchain-is-not-windows",
                               windows_go_toolchain,
                               mutator_kind="windows-housekeeping")

        def windows_rustc_toolchain(reports):
            for report in reports:
                report["build_provenance"]["toolchain"]["rustc"] = (
                    "rustc 1.97.1 (8bab26f4f) host x86_64-unknown-linux-gnu")
                report["build_provenance"]["toolchain"]["host_triple"] = (
                    "x86_64-unknown-linux-gnu")

        genuine_mutation_fails("windows-rustc-host-is-not-msvc",
                               windows_rustc_toolchain,
                               mutator_kind="windows-housekeeping")

        def windows_drop_colocation(reports):
            for report in reports:
                report["build_provenance"]["build_commands"] = [
                    line for line in report["build_provenance"]["build_commands"]
                    if "iprange-v4-worker" not in line]

        genuine_mutation_fails("windows-worker-colocation-step-missing",
                               windows_drop_colocation,
                               mutator_kind="windows-housekeeping")

        def windows_borrows_linux_build(reports):
            for report in reports:
                with open(genuine_matrix_paths[0], encoding="utf-8") as stream:
                    executed = _json.load(stream)
                report["binaries"]["go"]["sha256"] = (
                    (executed.get("binaries") or {}).get("go")
                    or {}).get("sha256")

        genuine_mutation_fails("windows-qualifies-the-linux-build",
                               windows_borrows_linux_build,
                               mutator_kind="windows-housekeeping")

        def windows_unstaged_artifact(reports):
            for report in reports:
                report["binaries"]["go"]["sha256"] = "9" * 64

        genuine_mutation_fails("windows-artifact-not-staged",
                               windows_unstaged_artifact,
                               mutator_kind="windows-housekeeping",
                               ledger=_wave_ledger_path())

        # --- item 7: the battery manifest binding content to revision.
        def committed_manifest(with_negative=True, ledger=None,
                               negatives=None):
            """A manifest over the committed reports, as the battery emits it.

            Built from the committed paths so a control can hold the manifest
            still while it rewrites the reports it attests -- which is exactly
            the wholesale rewrite this binding exists to catch.
            """

            return build_battery_manifest(
                {"matrix": list(genuine_matrix_paths),
                 "crash": [genuine_crash],
                 "crash-negative": negatives if negatives is not None
                 else (list(genuine_negative) if with_negative else []),
                 "fifo-surface": list(genuine_fifo),
                 "throughput": list(genuine_throughput),
                 "refusal-class-parity": list(genuine_conforming_parity),
                 "coverage-go": [genuine_coverage],
                 "windows-housekeeping": [genuine_windows]},
                ledger_path=ledger)

        def _restamped_windows(value):
            with open(genuine_windows, encoding="utf-8") as stream:
                document = _json.load(stream)
            document["git_head"] = value
            document["build_provenance"]["revision"] = value
            return document

        def _restamped_negative(path, value):
            with open(path, encoding="utf-8") as stream:
                document = _json.load(stream)
            document["git_head"] = value
            return document

        rewritten_head = "ab" * 20

        def restamp_all(report):
            report["git_head"] = rewritten_head

        def rewrite_every_head(matrices, crash):
            for report in list(matrices) + [crash]:
                restamp_all(report)

        def rewrite_surfaces(surfaces):
            for report in surfaces.values():
                restamp_all(report)

        assess_with_surfaces(
            "manifest-uniform-git-head-rewrite", rewrite_every_head,
            rewrite_surfaces,
            mutate_surfaces_extra={
                "battery_manifest": committed_manifest(
                    ledger=_wave_ledger_path()),
                "parity_report": parity_report(
                    rewritten_head, committed_identities,
                    binary_records=recorded_binaries),
                "coverage_report": dict(
                    _committed_coverage, git_head=rewritten_head),
                "windows_report": _restamped_windows(rewritten_head),
                "crash_negative_reports": [
                    _restamped_negative(path, rewritten_head)
                    for path in genuine_negative]},
            ledger=_wave_ledger_path())

        def swap_coverage_report(reports):
            for report in reports:
                report["policy"]["performance_use"] = (
                    report["policy"]["performance_use"] + " (restated)")

        genuine_mutation_fails(
            "manifest-report-content-not-the-attested-one",
            swap_coverage_report, mutator_kind="coverage-go",
            battery_manifest=committed_manifest(ledger=_wave_ledger_path()),
            ledger=_wave_ledger_path())

        assess_with_surfaces(
            "manifest-role-omitted", None, None,
            mutate_surfaces_extra={
                "battery_manifest": committed_manifest(with_negative=False,
                                                       ledger=_wave_ledger_path())},
            ledger=_wave_ledger_path())

        _real_ledger = _wave_ledger_path()
        assert _real_ledger, (
            "--self-test consumes the staged-artifact ledger to test the "
            "ledger-bound rules; stage the binaries or point "
            "IPRANGE_SHA256_LEDGER at the SHASUMS file the evidence was "
            "measured against")
        _tampered_ledger_path = os.path.join(work, "SHASUMS-tampered.txt")
        with open(_real_ledger, encoding="utf-8") as stream:
            _ledger_lines = [line.rstrip("\n") for line in stream if line.strip()]
        _go_digest = (_json.load(open(genuine_matrix_paths[0],
                                      encoding="utf-8"))
                      .get("binaries", {}).get("go", {}).get("sha256"))
        with open(_tampered_ledger_path, "w", encoding="utf-8") as stream:
            for line in _ledger_lines:
                digest, _, staged = line.partition("  ")
                if digest == _go_digest:
                    staged = "rust/mislabeled-iprange"
                stream.write(f"{digest}  {staged}\n")

        genuine_mutation_fails("parity-binary-staged-under-the-other-engine",
                               lambda reports: None,
                               mutator_kind="refusal-class-parity",
                               ledger=_tampered_ledger_path)

        genuine_mutation_fails("manifest-bound-without-gate-ledger",
                               lambda reports: None,
                               mutator_kind="coverage-go",
                               manifest_ledger=_real_ledger)
        genuine_mutation_fails("manifest-unbound-with-gate-ledger",
                               lambda reports: None,
                               mutator_kind="coverage-go",
                               ledger=_real_ledger, manifest_ledger=None)

        # Both halves of the negative-control binding.  A battery carries one
        # control per faked role, so an extra report under a new name is
        # allowed; these two forgeries are what the tightening closes: an
        # attested control replaced under its own name, and an attested
        # control that is simply not there.
        def doctor_negative_assertions(reports):
            for report in reports:
                report["scenarios"][0]["assertions"].append(
                    "an assertion the battery never ran")

        swapped_negative_path = os.path.join(work, "crash-negative.json")
        genuine_mutation_fails(
            "manifest-negative-control-swapped-under-attested-name",
            doctor_negative_assertions, mutator_kind="crash-negative",
            crash_negative_paths=[swapped_negative_path],
            battery_manifest=committed_manifest(ledger=_wave_ledger_path()),
            ledger=_wave_ledger_path())

        dropped_negative_path = os.path.join(work, "crash-negative-extra.json")
        with open(genuine_negative[0], encoding="utf-8") as stream:
            _extra_attested = _json.load(stream)
        _extra_attested["scenarios"] = _extra_attested["scenarios"][:8]
        _extra_attested["failed"] = 8
        assign(dropped_negative_path, _extra_attested)
        assess_with_surfaces(
            "manifest-negative-control-not-consumed", None, None,
            mutate_surfaces_extra={
                "battery_manifest": committed_manifest(
                    ledger=_wave_ledger_path(),
                    negatives=list(genuine_negative) +
                    [dropped_negative_path])},
            ledger=_wave_ledger_path())

        # Positive anchor for the consumed classes: the committed battery with
        # the ledger and both CLI verifications on.  A rule that only ever
        # rejects cannot tell a forgery from the real evidence, so this is the
        # half that proves the rules above are still narrower than the truth.
        # The parity report is the committed artifact rather than the
        # table-conforming copy the controls use: the committed battery
        # manifest attests that file's content, and a manifest that matches
        # its reports is part of what being genuine means here.
        problems, _c, _s = outer_assess(
            genuine_matrix_paths, [genuine_crash], fifo_paths=genuine_fifo,
            throughput_paths=genuine_throughput,
            parity_paths=[genuine_parity],
            coverage_paths=[genuine_coverage],
            crash_negative_paths=list(genuine_negative),
            windows_paths=[genuine_windows],
            sha256_ledger=_wave_ledger_path(), verify_binaries=True,
            verify_cases=True)
        blocking = outside_parity_rotation(problems)
        assert not blocking, (
            f"genuine evidence failed the consumed-class configuration: "
            f"{blocking}")


        problems, _c, _s = outer_assess(
            genuine_matrix_paths, [genuine_crash], verify_binaries=True,
            verify_cases=True, fifo_paths=genuine_fifo,
            throughput_paths=genuine_throughput,
            sha256_ledger=_wave_ledger_path())
        blocking = outside_parity_rotation(problems)
        assert not blocking, (
            f"genuine evidence failed the gate with CLI verification "
            f"enabled: {blocking}")
        parity_rotation = [problem for problem in problems
                           if problem not in blocking]
        # Reported only after every control and the genuine-evidence check
        # have run, so the line cannot appear for a battery that did not
        # complete.
        # Reviewers need to see which rule each control tripped: a control
        # rejected for a reason other than its own defect is vacuous, and a
        # silent battery cannot show that.
        if os.environ.get("IPRANGE_KIND_SELFTEST_VERBOSE"):
            for label, rejected, control_problems in results:
                print(f"[control] {label}: "
                      f"{'rejected' if rejected else 'ACCEPTED'}; "
                      f"{len(control_problems)} problem(s)", flush=True)
                for problem in control_problems[:3]:
                    print(f"    - {str(problem)[:220]}", flush=True)
        accepted = [label for label, rejected, _p in results if not rejected]
        duplicates = sorted({label for label in
                             [r[0] for r in results]
                             if [x[0] for x in results].count(label) > 1})
        assert not accepted, (
            f"{len(accepted)} self-test control(s) were ACCEPTED by the "
            f"gate, which means the gate no longer detects the defect they "
            f"stand for: {accepted[:6]}")
        assert not duplicates, (
            f"self-test controls registered twice, so the battery is not "
            f"the set it claims to be: {duplicates[:6]}")
        # Exact, not a floor: with a floor, deleting a control and lowering
        # the number are both silent.  Adding a control means updating this
        # constant deliberately, which is the point.
        assert len(results) == min_controls, (
            f"self-test ran {len(results)} controls but the battery is "
            f"specified as {min_controls}; a control removed from this "
            f"battery is a regression, not a simplification, and the count "
            f"is only allowed to change together with this constant")
        print(f"kind-gate self-test PASSED: {len(results)} controls "
              f"executed, all rejected", flush=True)


if __name__ == "__main__":
    # The doctored-report self-test is opt-in (--self-test), not a gate
    # precondition.  It consumes the committed evidence, so running it on
    # every invocation lets an in-flight evidence rotation replace every
    # CLI verdict -- --help included -- with its own assertion.  A wave
    # battery runs --self-test as its own required step instead.
    sys.exit(main())
