#!/usr/bin/env python3
"""External qualification runner for the iprange v1 production API.

Standard-library Python client that drives real executables through the
released legacy CLI and the ``--jsonrpc`` stdio protocol.  The runner owns
strict client framing, declarative cases, deterministic fixtures, an
independent scalar interval oracle, and a machine-readable provenance report.

Cross-language matrices (``rust_to_go``, ``go_to_rust``) are real
two-binary proofs: every rpc step declares the service role that executes
it (``actor: producer|consumer``, required by the case schema; a step
without one is a runner defect), and in a mixed matrix the producer
steps run on the producer binary while the consumer steps run on the
consumer binary, in separate service processes that share only the
per-case work directory.  The declared actor is the single routing
authority; method names never imply a role, so transformations
(snapshot, recover, history projection, algebra publication) can run on
either side.  Single-language matrices run both roles on the one
selected executable.  A case that cannot exercise both actors is skipped with its
reason, so a mixed-direction PASS always means both binaries actually
served; a mixed direction that executes no both-actor case fails as a
matrix.  Per-case PASS entries record each actor binary's SHA-256, canonical
executed path (argv), product-declared implementation ("rust"|"go"),
and executed-step count, so language attribution derives from the
executed binaries themselves, never from the report-level matrix label.
"""

import argparse
import hashlib
import json
import os
import platform as platform_module
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import traceback
import uuid

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from command_sanitize import (  # noqa: E402  (side-effect free)
    audit_report_writers,
    owned_temp_root,
    recorded_checkout_root,
    require_paths_outside_profile,
    run_shared_self_test,
    same_path,
    under_profile,
    write_committed_report,
)

from schema.engine import ValidationError  # noqa: E402
from schema import cases as case_schema  # noqa: E402
from schema import frame, methods, results  # noqa: E402
from schema import oracle  # noqa: E402

DEFAULT_CASE_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "cases")

WORK_PLACEHOLDER = "$WORK/"
CAPTURE_PLACEHOLDER = "$CAPTURE/"

# ---- mechanical file-kind ledger ---------------------------------
# Persistent artifact kinds driven by the v4 binary-format spec and by
# the method param schemas.  Name-based kinds match engine artifacts
# that are never declared in case params; declared-path kinds come from
# the step params (and case fixtures) of the executed cases.
KIND_V4_MAIN = "v4_main"                            # v4 database main file
KIND_LIVE_SIDECAR = "live_sidecar"                  # <main-basename>.readers
KIND_PUBLICATION_RESERVATION = "publication_reservation"  # .iprange-reservation-*.tmp
KIND_AUTHORIZED_SCRATCH = "authorized_scratch"      # .iprange-scratch-*.tmp
KIND_ADAPTER_OUTPUT = "adapter_output"              # csv/jsonl/netset/ipset/ranges...
KIND_METADATA_DELIVERY = "metadata_delivery"        # delivery.path metadata files
KIND_PUBLICATION_TEMP = "publication_temp"          # .iprange-publish-*.tmp
KIND_UNKNOWN = "unknown"

LIVE_SIDECAR_SUFFIX = ".readers"
LIVE_SIDECAR_RESET_SUFFIX = ".readers.reset"
RESERVATION_PREFIX = ".iprange-reservation-"
PUBLISH_TEMP_PREFIX = ".iprange-publish-"
SCRATCH_PREFIX = ".iprange-scratch-"
PRIVATE_TMP_SUFFIX = ".tmp"

# Parameter keys whose subtree carries filesystem paths, and the
# artifact kind the spec assigns to each slot.  Object keys recurse, so
# ``source.path`` inherits the v4_main kind of ``source`` and
# ``delivery.path`` inherits metadata_delivery from ``delivery``.
DECLARED_ADAPTER_OUTPUT_KEYS = frozenset(
    {"output", "findings_output", "report_output", "removals_output"})
DECLARED_METADATA_DELIVERY_KEYS = frozenset({"delivery"})
DECLARED_V4_MAIN_KEYS = frozenset({
    "path", "paths", "source_path", "directory", "source", "current",
    "last_seen", "input", "direct", "metadata", "candidate",
})

# Methods that OPEN A LIVE READER when their reader-source slot carries
# mode "live".  Engine-verified: a live source routes through the SDK
# LiveReader::open, which registers in and writes the ``<main>.readers``
# sidecar reader table (Rust reader_core/live.rs:62 ->
# live_sidecar.rs:170-191; Go live_reader.go:68 -> sidecar.go:146).
# The set is derived from the executed case files: every method a case
# executes with a live source opens the sidecar and is listed here; a
# live-open method added to a case without extending this set fails the
# kind gate (missing sidecar lineage) instead of silently under-
# reporting.
LIVE_OPEN_METHODS = frozenset({
    "iprange.v1.reader.open",
    "iprange.v1.database.info",
    "iprange.v1.database.metadata.get",
    "iprange.v1.history.project",
    "iprange.v1.join.direct",
    "iprange.v1.join.membership",
    "iprange.v1.query.overlaps",
    "iprange.v1.algebra.publish",
    "iprange.v1.feeds.create",
    "iprange.v1.snapshot",
})

# Param key paths of the LIVE READER SOURCE per live-open method; "*"
# walks every element of a list.  Only these slots open a live reader:
# the other declared v4-main slots of the same step are writer or
# immutable targets (feeds.create's ``path`` opens the live WRITER,
# never a reader; history.project's ``path`` is a writer) and must not
# credit the sidecar.
LIVE_READER_SOURCE_SLOTS = {
    "iprange.v1.reader.open": (("source",),),
    "iprange.v1.database.info": (("source",),),
    "iprange.v1.database.metadata.get": (("source",),),
    "iprange.v1.history.project": (("last_seen",),),
    "iprange.v1.join.direct": (("direct",), ("membership", "source")),
    "iprange.v1.join.membership": (("left", "source"), ("right", "source")),
    "iprange.v1.query.overlaps": (("source",),),
    "iprange.v1.algebra.publish": (("sources", "*", "source"),),
    "iprange.v1.feeds.create": (("current", "source"),),
    "iprange.v1.snapshot": (("source",),),
}

# Deadline-bounded JSON-RPC client stall guards (resource_harness.py
# parity).  Each bound applies to ONE read or ONE write operation,
# never to a whole run: normal calls complete in milliseconds and are
# unaffected, but a peer that stops answering or stops draining stdin
# must fail the run instead of blocking a runner forever.
RUNNER_IO_DEADLINE_SECONDS = 120.0
# The capability probe is a single short describe call under normal
# conditions, so its per-operation stall guard is shorter.
PROBE_IO_DEADLINE_SECONDS = 30.0


# Per-method peak wire-frame sizes observed by every JsonRpcService
# client in this process (one physical line per frame; the byte counts
# include the LF terminator for both directions).
FRAME_SIZES = {}


def record_frame_size(method, request_bytes, response_bytes=None):
    """Merge one measured exchange into the process frame-size table."""

    entry = FRAME_SIZES.setdefault(method, {})
    current = entry.get("max_request_bytes", 0)
    if request_bytes > current:
        entry["max_request_bytes"] = request_bytes
    if response_bytes is not None:
        current = entry.get("max_response_bytes", 0)
        if response_bytes > current:
            entry["max_response_bytes"] = response_bytes

# Cross-language matrix actor model.  A mixed matrix (rust_to_go,
# go_to_rust) runs every case through two real product services that share
# only the per-case work directory: the producer service executes artifact
# creation/mutation steps, the consumer service executes observation and
# transformation steps.  Every rpc step declares its actor explicitly
# (schema/cases.py requires it), and that declared actor is the single
# routing authority: the split is a property of the step, never of the
# method name.
ALL_ACTORS = ("producer", "consumer")


def declared_actor(step):
    """Return the declared service role of an rpc step.

    ``actor`` is required: no method-name fallback exists, so a step
    that omits it is a runner defect, not a routing decision.
    """

    actor = step.get("actor")
    if actor is None:
        raise ValueError(
            f"rpc step for method {step.get('method')!r} declares no actor")
    return actor


def actor_requirements(case):
    """Set of services a case needs: the declared actor of every rpc
    step, plus the consumer service for legacy CLI steps."""
    actors = set()
    for step in case.get("steps", []):
        if step.get("kind") == "rpc":
            actors.add(declared_actor(step))
        else:
            actors.add("consumer")
    return actors

# Keep subprocess setup deterministic while retaining locale, sanitizer, and
# platform variables needed by portable qualification runs.
ENV_ALLOWLIST = (
    "PATH",
    "LANG",
    "LC_ALL",
    "LC_CTYPE",
    "TZ",
    "ASAN_OPTIONS",
    "LSAN_OPTIONS",
    "MSAN_OPTIONS",
    "TSAN_OPTIONS",
    "UBSAN_OPTIONS",
    "SYSTEMROOT",
    "SYSTEMDRIVE",
    "USERPROFILE",
    "PROCESSOR_ARCHITECTURE",
    "PROCESSOR_IDENTIFIER",
    # Go source-level coverage is opt-in from the harness: a
    # ``go build -cover`` binary writes its counter block to the directory
    # this names at exit, and only ``coverage_harness.py`` sets it.  A normal
    # qualification run has no GOCOVERDIR in its environment, so the entry
    # forwards nothing and the child environment stays as documented; it is
    # listed here rather than injected because an injected variable would be
    # a second, undocumented source of child state.
    "GOCOVERDIR",
)


def child_environment():
    """Return the documented subprocess environment allowlist."""

    source = os.environ
    result = {name: source[name] for name in ENV_ALLOWLIST if name in source}
    return result


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def safe_work_path(work_dir, relative, *, must_exist=False):
    """Resolve a case-relative path and reject lexical/symlink escapes."""

    if os.path.isabs(relative):
        raise ValueError(f"case path must be work-relative: {relative!r}")
    path = os.path.abspath(os.path.join(work_dir, relative))
    real_work = os.path.realpath(work_dir)
    if os.path.commonpath((path, real_work)) != real_work:
        raise ValueError(f"case path escapes the work directory: {relative!r}")
    if must_exist:
        real_path = os.path.realpath(path)
        if os.path.commonpath((real_path, real_work)) != real_work:
            raise ValueError(f"case path escapes the work directory: {relative!r}")
        if not os.path.exists(real_path):
            return path
    return path


class CaseRunner:
    """One case, one consumer service, and one private work directory."""

    def __init__(self, binary, case, work_dir, implementation, fixture_tool=None,
                 producer_binary=None, consumer_binary=None):
        self.binary = binary
        self.fixture_tool = fixture_tool
        self.case = case
        self.work_dir = os.path.realpath(work_dir)
        self.implementation = implementation
        self.mixed = producer_binary is not None or consumer_binary is not None
        self.actor_binaries = {
            "producer": producer_binary if self.mixed else binary,
            "consumer": consumer_binary if self.mixed else binary,
        }
        self.captures = {}          # capture name -> (owning actor, value)
        # Asserted negative-params steps of this case (transport code,
        # executed actor, and the exact request bytes), preserved in the
        # PASS entry so the kind gate can require the corpus really
        # exercised the params validator.
        self.negative_responses = []
        # digest group -> recorded artifact digests (see the case schema
        # digest_group contract), preserved in the PASS entry.
        self.digest_groups = {}
        self.services = {}          # actor -> JsonRpcService (mixed mode)
        # Services this runner spawned itself, as opposed to a service a
        # harness injected (the sensitivity controls install a fake server) or
        # one its own subclass deliberately kills (the crash battery).  Only
        # owned services carry the alive-at-teardown obligation, so a harness
        # that intends a peer to die keeps its documented behavior.
        self.owned_services = []
        self.actor_steps = {}       # actor -> executed step count
        self.actor_operations = {"producer": [], "consumer": []}
        # actor -> ordered unique executed method names ("legacy" for
        # CLI steps); the kind gate validates every lineage ref against
        # these lists.
        self.service = None         # single-actor mode; also sensitivity-gate hook
        self.service_argv = [binary, "--jsonrpc"]
        self.cursors = {}
        self.readers = {}
        self.reader_families = {}
        self.fixture_intervals = {}
        self.pending_lookup = []
        self.oracle_checks = 0
        # Mechanical file-kind ledger: kind -> created_by/opened_by
        # method -> count, derived from executed steps plus the observed
        # work-dir inventory (fixture inputs are never inventoried).
        self.file_kinds = {}
        # Per-case lineage: path -> kind -> created_by/opened_by lists of
        # "actor.method" strings, preserved in the case report entry so
        # the root aggregate never loses the case/actor/path evidence.
        self.file_kinds_paths = {}
        # Absolute real paths of runner- or fixture-tool-created inputs;
        # they are excluded from every inventory snapshot.
        self._fixture_inputs = set()

    # ---- fixtures -------------------------------------------------
    def build_fixtures(self):
        for fixture in self.case.get("fixtures", []):
            path = safe_work_path(self.work_dir, fixture["path"])
            os.makedirs(os.path.dirname(path), exist_ok=True)
            self._fixture_inputs.add(os.path.realpath(path))
            source = fixture["source"]
            if "text" in source:
                text = source["text"]
                if source.get("text_expand_work"):
                    text = text.replace(WORK_PLACEHOLDER,
                                        self.work_dir + os.sep)
                write_text(path, text)
                intervals = parse_interval_text(text)
                if intervals is not None:
                    self.fixture_intervals[os.path.realpath(path)] = intervals
            elif "base64" in source:
                import base64
                data = base64.b64decode(source["base64"], validate=True)
                write_bytes(path, data)
                try:
                    intervals = parse_interval_text(data.decode("utf-8", "strict"))
                except UnicodeDecodeError:
                    # Binary fixtures (damaged v4 pages) have no interval text.
                    intervals = None
                if intervals is not None:
                    self.fixture_intervals[os.path.realpath(path)] = intervals
            elif "csv_db" in source:
                csv_path = os.path.join(self.work_dir, f"{fixture['path']}.csv")
                self._fixture_inputs.add(os.path.realpath(csv_path))
                write_text(csv_path, source["csv_db"])
                csv_kind = source.get("csv_kind", "direct")
                if csv_kind not in ("direct", "membership"):
                    raise ValueError(f"unknown csv_kind {csv_kind!r}")
                proc = subprocess.run(
                    [self.fixture_tool, f"{csv_kind}-csv", path, csv_path],
                    capture_output=True,
                    timeout=300,
                    env=child_environment(),
                )
                if proc.returncode != 0:
                    detail = proc.stderr.decode("utf-8", "replace").strip()
                    raise ValueError(
                        f"v4-fixture direct-csv failed with exit {proc.returncode}: {detail}")
                intervals = parse_interval_text(source["csv_db"])
                if intervals is not None:
                    self.fixture_intervals[os.path.realpath(path)] = intervals
            elif "symlink_to" in source:
                # A symlink to another work-dir path.  The target is
                # spelled absolute at creation time so the link resolves
                # for a scan regardless of the process working
                # directory; a dangling link would make a
                # directory-expansion case vacuous, so the target must
                # exist (declare it before this fixture).
                target = safe_work_path(self.work_dir, source["symlink_to"])
                if os.path.realpath(target) not in self._fixture_inputs:
                    raise ValueError(
                        f"fixture {fixture['path']!r}: symlink target "
                        f"{source['symlink_to']!r} is not a fixture "
                        "declared earlier in this case")
                if not os.path.isfile(target):
                    raise ValueError(
                        f"fixture {fixture['path']!r}: symlink target "
                        f"{source['symlink_to']!r} is not a regular file")
                if os.path.lexists(path):
                    os.unlink(path)
                os.symlink(os.path.realpath(target), path)
                # The link itself needs no inventory exclusion: every
                # inventory lookup resolves a path, and the link
                # resolves to its target, which is already registered.
            elif "generator" in source:
                # Generated v4 files intentionally have no independent text
                # representation here; oracle checks are skipped for them.
                generate_fixture(path, source, self.fixture_tool)
            else:  # case validation makes this unreachable
                raise ValueError(f"fixture {fixture['path']!r}: no source")

    # ---- mechanical file-kind ledger ------------------------------
    def inventory(self):
        """Snapshot of every non-fixture file under the per-case work dir.

        Fixture inputs (declared in the case) are inputs, not artifacts,
        so they never enter the ledger.  Directories are ignored; only
        files are inventoried.
        """

        found = set()
        for root, dirs, names in os.walk(self.work_dir):
            for name in names:
                candidate = os.path.join(root, name)
                if os.path.realpath(candidate) in self._fixture_inputs:
                    continue
                found.add(candidate)
        return found

    def declared_paths(self, step):
        """Map every work-dir file path referenced by a step's params to
        its spec artifact kind.

        Resolution is lexical-first: ``$WORK/<rel>`` param values resolve
        under the work dir; any other string whose absolute form lands
        inside the work dir also counts (the ledger only ever reports
        work-dir files).  ``destination`` is the adapter output only for
        export; current.publish, algebra.publish, snapshot, and recover
        destinations are v4 main files.
        """

        method = step["method"]
        declared = {}

        def resolve(value):
            if isinstance(value, str) and value.startswith(WORK_PLACEHOLDER):
                return safe_work_path(self.work_dir, value[len(WORK_PLACEHOLDER):])
            if isinstance(value, str) and not value.startswith(CAPTURE_PLACEHOLDER):
                absolute = os.path.abspath(value)
                real = os.path.realpath(absolute)
                if os.path.commonpath((real, self.work_dir)) == self.work_dir:
                    return absolute
            return None

        def visit(value, kind_hint):
            if isinstance(value, str):
                located = resolve(value)
                if located is not None:
                    declared[located] = kind_hint
                return
            if isinstance(value, list):
                for item in value:
                    visit(item, kind_hint)
                return
            if not isinstance(value, dict):
                return
            for key, sub in value.items():
                if key == "destination":
                    kind = (KIND_ADAPTER_OUTPUT
                            if method == "iprange.v1.export" else KIND_V4_MAIN)
                elif key in DECLARED_ADAPTER_OUTPUT_KEYS:
                    kind = KIND_ADAPTER_OUTPUT
                elif key in DECLARED_METADATA_DELIVERY_KEYS:
                    kind = KIND_METADATA_DELIVERY
                elif key in DECLARED_V4_MAIN_KEYS and kind_hint == KIND_V4_MAIN:
                    # A v4-main slot under an adapter/delivery parent keeps
                    # the parent kind (output.path is the output file, not a
                    # database main file); otherwise the subtree is v4 main.
                    visit(sub, KIND_V4_MAIN)
                    continue
                else:
                    kind = kind_hint
                visit(sub, kind)

        visit(step.get("params", {}), KIND_V4_MAIN)
        return declared

    @staticmethod
    def name_kind(basename):
        """Name-derived kind for engine artifacts never declared in params."""

        if basename.endswith(LIVE_SIDECAR_SUFFIX) or basename.endswith(LIVE_SIDECAR_RESET_SUFFIX):
            return KIND_LIVE_SIDECAR
        if basename.startswith(RESERVATION_PREFIX) and basename.endswith(PRIVATE_TMP_SUFFIX):
            return KIND_PUBLICATION_RESERVATION
        if basename.startswith(PUBLISH_TEMP_PREFIX) and basename.endswith(PRIVATE_TMP_SUFFIX):
            return KIND_PUBLICATION_TEMP
        if basename.startswith(SCRATCH_PREFIX) and basename.endswith(PRIVATE_TMP_SUFFIX):
            return KIND_AUTHORIZED_SCRATCH
        return None

    @staticmethod
    def _ledger_increment(bucket, side, method):
        counts = bucket[side]
        counts[method] = counts.get(method, 0) + 1

    def record_ledger(self, before, step, credit_opens=True):
        """Merge one executed step's inventory delta into the ledger.

        ``credit_opens`` is cleared by a step the service refused in its
        params validator: that request never reached a file, so naming
        its declared paths as opens would fabricate lineage.  Created
        files are still recorded, because a refusal that writes is a
        defect the kind gate must reject (no method that can only be
        refused is in the create-capable sets).

        A file that appears by the end of the step is ``created_by`` that
        step's method; a file that already existed and whose path the step
        params reference is ``opened_by`` that method.  Files that appear
        and disappear inside one step are transient and never counted.
        Legacy CLI steps are recorded under the literal method name
        "legacy" (they are point-in-time commands, not JSON-RPC methods).
        The per-path lineage keeps the acting service role with every
        method (producer or consumer; "legacy" for CLI steps).
        """

        method = step.get("method", "legacy")
        actor = step.get("actor", "legacy")
        declared = self.declared_paths(step)
        after = self.inventory()
        for path in after - before:
            basename = os.path.basename(path)
            kind = self.name_kind(basename)
            if kind is None:
                kind = declared.get(path, KIND_UNKNOWN)
            bucket = self.file_kinds.setdefault(
                kind, {"created_by": {}, "opened_by": {}})
            self._ledger_increment(bucket, "created_by", method)
            entry = self.file_kinds_paths.setdefault(path, {
                "kind": kind, "created_by": [], "opened_by": []})
            entry["created_by"].append(f"{actor}.{method}")
        opened = {}
        if credit_opens:
            for path, kind in declared.items():
                if path in before:
                    opened.setdefault(kind, set()).add(path)
        for kind, paths in opened.items():
            bucket = self.file_kinds.setdefault(
                kind, {"created_by": {}, "opened_by": {}})
            for _ in paths:
                self._ledger_increment(bucket, "opened_by", method)
            for item in paths:
                entry = self.file_kinds_paths.setdefault(item, {
                    "kind": kind, "created_by": [], "opened_by": []})
                entry["opened_by"].append(f"{actor}.{method}")

        # Implicit live-reader opens: a live-open step whose reader
        # source carries an existing ``<main>.readers`` sidecar writes
        # that sidecar's reader table even though the sidecar is never
        # named in the step params (a declared-path step reopens only
        # the main).  The open is recorded with the step's actor and
        # method, exactly like the declared-path opens above; the
        # reader-source slots and methods are LIVE_READER_SOURCE_SLOTS /
        # LIVE_OPEN_METHODS (engine-verified: only a live-mode
        # reader-source open writes the sidecar; writer and immutable
        # slots never do).
        if credit_opens and method in LIVE_OPEN_METHODS:
            for path in self.live_reader_source_paths(step):
                sidecar = path + LIVE_SIDECAR_SUFFIX
                if sidecar not in before:
                    continue
                bucket = self.file_kinds.setdefault(
                    KIND_LIVE_SIDECAR, {"created_by": {}, "opened_by": {}})
                self._ledger_increment(bucket, "opened_by", method)
                entry = self.file_kinds_paths.setdefault(sidecar, {
                    "kind": KIND_LIVE_SIDECAR,
                    "created_by": [], "opened_by": []})
                entry["opened_by"].append(f"{actor}.{method}")

    def live_reader_source_paths(self, step):
        """Absolute v4-main paths a live-open step opens as LIVE readers.

        A live-open method (see LIVE_OPEN_METHODS) opens one live
        reader per reader-source slot (LIVE_READER_SOURCE_SLOTS) whose
        mode is "live".  The paths resolve exactly like
        ``declared_paths`` (work-relative ``$WORK/...`` or work-dir
        absolute); capture placeholders never resolve and are skipped.
        """

        method = step["method"]
        if method not in LIVE_OPEN_METHODS:
            return []
        opened = []

        def resolve(value):
            if isinstance(value, str) and value.startswith(WORK_PLACEHOLDER):
                return safe_work_path(self.work_dir, value[len(WORK_PLACEHOLDER):])
            if isinstance(value, str) and not value.startswith(CAPTURE_PLACEHOLDER):
                absolute = os.path.abspath(value)
                real = os.path.realpath(absolute)
                if os.path.commonpath((real, self.work_dir)) == self.work_dir:
                    return absolute
            return None

        def descend(nodes, keys):
            for key in keys:
                next_nodes = []
                for node in nodes:
                    if key == "*":
                        next_nodes.extend(node if isinstance(node, list) else [])
                    elif isinstance(node, dict):
                        value = node.get(key)
                        if value is not None:
                            next_nodes.append(value)
                nodes = next_nodes
            return nodes

        for keys in LIVE_READER_SOURCE_SLOTS.get(method, ()):
            for node in descend([step.get("params", {})], keys):
                if not isinstance(node, dict) or node.get("mode") != "live":
                    continue
                located = resolve(node.get("path"))
                if located is not None and located not in opened:
                    opened.append(located)
        return opened

    # ---- substitutions --------------------------------------------
    def substitute(self, value, actor="consumer"):
        if isinstance(value, str):
            # An "@"-prefixed work path is an @-expansion request path
            # (input.paths, @file-list).  The "@" is part of the CLI
            # grammar, not part of the path, so it is preserved and the
            # remainder resolves exactly like a plain $WORK path.
            if value.startswith("@" + WORK_PLACEHOLDER):
                return "@" + safe_work_path(
                    self.work_dir, value[len("@" + WORK_PLACEHOLDER):])
            if value.startswith(WORK_PLACEHOLDER):
                return safe_work_path(self.work_dir, value[len(WORK_PLACEHOLDER):])
            if value.startswith(CAPTURE_PLACEHOLDER):
                name = value[len(CAPTURE_PLACEHOLDER):]
                if name not in self.captures:
                    raise ValueError(f"unresolved capture {name!r}")
                owner, stored = self.captures[name]
                if self.mixed and owner != actor:
                    raise AssertionError(
                        f"case {self.case['name']!r}: capture {name!r} was produced "
                        f"by the {owner} service and cannot cross to the {actor} "
                        f"service (only filesystem paths cross actors; handles and "
                        f"result objects are connection-local)")
                return stored
            return value
        if isinstance(value, list):
            return [self.substitute(item, actor) for item in value]
        if isinstance(value, dict):
            return {key: self.substitute(item, actor) for key, item in value.items()}
        return value

    # ---- expectations ---------------------------------------------
    def matches_expected(self, expected, got):
        """Match expected values exhaustively, except explicit $ignore trees.

        An expected object is partial only when one of its named members is
        explicitly ignored.  This lets cases ignore random identities while a
        complete result schema still validates every unmentioned field.
        """

        if expected == {"$ignore": True}:
            return True
        if isinstance(expected, dict):
            if not isinstance(got, dict):
                return False
            partial = any(value == {"$ignore": True} for value in expected.values())
            if not partial and set(expected) != set(got):
                return False
            return all(key in got and self.matches_expected(value, got[key])
                       for key, value in expected.items())
        if isinstance(expected, list):
            return (isinstance(got, list)
                    and len(expected) == len(got)
                    and all(self.matches_expected(value, item)
                            for value, item in zip(expected, got)))
        return expected == got

    # ---- rpc steps ------------------------------------------------
    def run_rpc_step(self, step):
        method = step["method"]
        actor = declared_actor(step)
        params = self.substitute(step["params"], actor)
        before = self.inventory()
        if not methods.known(method):
            raise AssertionError(f"case {self.case['name']!r}: unknown method {method}")
        negative = step.get("expect_params_rejected")
        if negative is None:
            try:
                case_schema.validate_rpc_request(method, params)
            except ValidationError as exc:
                raise AssertionError(
                    f"case {self.case['name']!r}: invalid request params: {exc}") from exc
        else:
            # Negative-params mode.  Client-side validation is bypassed
            # so the service's own params validator answers, and the
            # bypass is only honest when that validator has something to
            # refuse: the committed schema must reject the same request.
            self.check_request_is_contract_invalid(method, params)

        service = self.service_for(actor)
        self.actor_steps[actor] = self.actor_steps.get(actor, 0) + 1
        if method not in self.actor_operations[actor]:
            self.actor_operations[actor].append(method)
        if step.get("notification"):
            service.notify(method, params)
            self.record_ledger(before, step)
            return

        request_id = f"case-{self.case['name']}"
        response = service.call(request_id, method, params)
        if "error" not in response and step.get("expect_error") is not None:
            # A success response on a step that declared expect_error is
            # a missing refusal, not a pass: the negative expectation
            # must fail here, before the digest recording and before the
            # fall-through to the (absent) expect_result assertions that
            # used to launder the divergence.
            raise AssertionError(
                f"case {self.case['name']!r}: method {method} succeeded but "
                f"the step declared expect_error "
                f"{step['expect_error'].get('code')!r}")
        if "digest_group" in step:
            self.record_digest(step, method, params)
        if negative is not None:
            self.check_expected_params_rejected(step, method, params, response)
            # No open credit: a params refusal precedes every path
            # access, so the declared paths were never opened.  A
            # refusal that still creates a file is recorded and then
            # rejected by the kind gate as a fabricated create.
            self.record_ledger(before, step, credit_opens=False)
            return
        if "error" in response:
            self.check_expected_error(step, method, response["error"])
            capture_root = {
                "code": response["error"].get("code"),
                "message": response["error"].get("message"),
                "data": response["error"].get("data"),
            }
            self.process_captures(step.get("capture", []), capture_root, actor)
            for assertion in step.get("assert_files", []):
                self.assert_file(assertion)
            self.record_ledger(before, step)
            return

        result = response["result"]
        try:
            results.validate_result(method, result)
        except ValidationError as exc:
            raise AssertionError(
                f"case {self.case['name']!r}: invalid result for {method}: {exc}") from exc
        self.check_protocol(method, params, result)
        self.check_output_result(method, params, result)
        self.check_source_close(method, params, result)
        if "expect_result" in step:
            expected = step["expect_result"]
            for key, exp in expected.items():
                if key == "method":
                    continue
                if key not in result:
                    raise AssertionError(
                        f"case {self.case['name']!r}: result.{key} is absent")
                got = result[key]
                if not self.matches_expected(exp, got):
                    raise AssertionError(
                        f"case {self.case['name']!r}: result.{key} expected {exp!r}, got {got!r}")
        self.process_captures(step.get("capture", []), result, actor)
        for assertion in step.get("assert_files", []):
            self.assert_file(assertion)
        self.record_ledger(before, step)

    def record_digest(self, step, method, params):
        """Record one export artifact under its declared digest group.

        The digest is taken from the file on disk after the service
        reported it, so the recorded value is the artifact a later
        consumer reads, not a number the service chose to advertise.
        ``check_output_result`` has already required the reported facts
        to equal that file.
        """

        destination = params.get("destination")
        if not isinstance(destination, str) or not destination:
            raise AssertionError(
                f"case {self.case['name']!r}: {method} declares a "
                f"digest_group but no destination path")
        path = (destination if os.path.isabs(destination)
                else safe_work_path(self.work_dir, destination))
        if not os.path.isfile(path):
            raise AssertionError(
                f"case {self.case['name']!r}: digest group target is not "
                f"a file: {destination}")
        self.digest_groups.setdefault(step["digest_group"], []).append({
            "actor": step["actor"],
            "method": method,
            "format": params.get("format"),
            "path": os.path.relpath(path, self.work_dir),
            "sha256": sha256_file(path),
        })

    def check_request_is_contract_invalid(self, method, params):
        """Require the committed request schema to reject a negative step.

        A step that marks ``expect_params_rejected`` asserts that the
        service refuses the params object with ``-32602``.  If the
        published schema accepted the same object, the assertion would
        be a coin flip about schema/service agreement rather than a
        pinned contract term, and the case would silently stop testing
        the validator once one side drifted.
        """

        try:
            case_schema.validate_rpc_request(method, params)
        except ValidationError:
            return
        raise AssertionError(
            f"case {self.case['name']!r}: {method} declares "
            f"expect_params_rejected but the committed request schema "
            f"accepts its params; the step asserts nothing about the "
            f"service validator (resolve the schema/service disagreement "
            f"instead of asserting the rejection)")

    def check_expected_params_rejected(self, step, method, params, response):
        """Assert the JSON-RPC params-validator answer for a negative step.

        Only the transport code is a cross-engine contract term here;
        message text is diagnostic (accepted P3) and is compared solely
        when the case pins a substring that both engines provably share.
        The observed envelope and the exact request bytes are included
        in every failure so a divergence is reproducible from the
        report alone.
        """

        expected = step["expect_params_rejected"]
        observed = json.dumps(response, sort_keys=True,
                              separators=(",", ":"), ensure_ascii=False)
        request = json.dumps(
            {"jsonrpc": "2.0", "id": f"case-{self.case['name']}",
             "method": method, "params": params},
            sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        error = response.get("error")
        if not isinstance(error, dict):
            raise AssertionError(
                f"case {self.case['name']!r}: {method} was expected to "
                f"answer {frame.STD_INVALID_PARAMS} invalid params; "
                f"response={observed} request={request}")
        if error.get("code") != frame.STD_INVALID_PARAMS:
            data = json.dumps(error.get("data"), sort_keys=True,
                              ensure_ascii=False)
            raise AssertionError(
                f"case {self.case['name']!r}: {method} was expected to "
                f"answer {frame.STD_INVALID_PARAMS} invalid params, got "
                f"code={error.get('code')!r} "
                f"message={error.get('message')!r} "
                f"data={data} request={request}")
        needle = expected.get("message_contains")
        if needle is not None and needle not in (error.get("message") or ""):
            raise AssertionError(
                f"case {self.case['name']!r}: {method} params rejection "
                f"message {error.get('message')!r} does not contain "
                f"{needle!r} request={request}")
        self.negative_responses.append({
            "actor": step["actor"],
            "method": method,
            "transport_code": error.get("code"),
            "request": request,
        })

    def check_expected_error(self, step, method, error):
        expected = step.get("expect_error")
        if expected is None:
            raise AssertionError(
                f"case {self.case['name']!r}: method {method} failed unexpectedly: "
                f"{error.get('code')} {error.get('message')} data={error.get('data')}")
        if error.get("code") != frame.PRODUCT_ERROR:
            raise AssertionError(
                f"case {self.case['name']!r}: product error transport code must be "
                f"{frame.PRODUCT_ERROR}, got {error.get('code')!r}: "
                f"{error.get('message')}")
        data = error.get("data")
        if not isinstance(data, dict):
            raise AssertionError(
                f"case {self.case['name']!r}: product error data must be an object")
        if data.get("code") != expected["code"]:
            raise AssertionError(
                f"case {self.case['name']!r}: expected data.code {expected['code']!r}, "
                f"got {data.get('code')!r}")
        if "outcome" in expected and data.get("outcome") != expected["outcome"]:
            raise AssertionError(
                f"case {self.case['name']!r}: expected outcome {expected['outcome']!r}, "
                f"got {data.get('outcome')!r}")
        if "details" in expected:
            details = data.get("details")
            if not isinstance(details, dict):
                raise AssertionError(
                    f"case {self.case['name']!r}: expected error details, "
                    f"got {details!r}")
            if set(expected["details"]) != set(details):
                raise AssertionError(
                    f"case {self.case['name']!r}: error details member set mismatch: "
                    f"expected {sorted(expected['details'])}, "
                    f"got {sorted(details)}")
            for name, value in expected["details"].items():
                if not self.matches_expected(value, details[name]):
                    raise AssertionError(
                        f"case {self.case['name']!r}: error details {name!r} "
                        f"mismatch: expected {value!r}, got {details[name]!r}")

    def capture_value(self, pointer, root):
        """Resolve a capture pointer inside a result object.

        A pointer is a dotted chain of member names with optional
        ``[index]`` list steps, e.g. ``result.candidates[0]``.  The
        index syntax is the only way to name a list element (recovery
        candidates); every other step descends a dict.
        """
        parts = case_schema.pointer_parts(pointer)
        if parts is None:
            raise AssertionError(
                f"case {self.case['name']!r}: capture {pointer!r} "
                "is not a valid member chain")
        value = root
        for part in parts:
            if part.startswith("["):
                index = int(part[1:-1])
                if not isinstance(value, list) or index >= len(value):
                    raise AssertionError(
                        f"case {self.case['name']!r}: capture {pointer!r} "
                        f"index {index} out of range")
                value = value[index]
            else:
                if not isinstance(value, dict) or part not in value:
                    raise AssertionError(
                        f"case {self.case['name']!r}: capture {pointer!r} not found")
                value = value[part]
        return value

    def process_captures(self, specs, root, actor):
        for spec in specs:
            if isinstance(spec, dict):
                name = spec["name"]
                pointer = spec["path"]
            else:
                name = pointer = spec
            value = self.capture_value(pointer, root)
            self.captures[name] = (actor, value)

    def check_output_result(self, method, params, result):
        """Verify response output facts against the requested local artifact."""

        request_path = None
        if isinstance(params.get("delivery"), dict) and params["delivery"].get("mode") == "file":
            request_path = params["delivery"].get("path")
        for name in ("output", "findings_output", "report_output"):
            if isinstance(params.get(name), dict):
                request_path = params[name].get("path")
        if method == "iprange.v1.export":
            request_path = params.get("destination")

        if request_path is not None and not os.path.isabs(request_path):
            request_path = safe_work_path(self.work_dir, request_path)
        facts = result.get("output") if isinstance(result.get("output"), dict) else None
        if facts is not None:
            self.verify_output_facts(facts, request_path)
        if method == "iprange.v1.export":
            self.verify_output_facts(result, request_path)

    def check_source_close(self, method, params, result):
        """Live sources must close internally and report source_close;
        immutable sources must not fabricate one (spec factual-close rules).

        For feeds create/replace and retention refreshes the coverage
        source is nested at params.current.source; feeds.import and the
        query/export families carry it at params.source. Both locations
        are checked for factual-close conformance.
        """

        # snapshot_to opens and closes internally and its result is a
        # complete SnapshotResult without close facts; the public SDK
        # supplies no close result for it.
        if method == "iprange.v1.snapshot":
            return
        # reader.open returns a reader handle that owns the source lifetime
        # (spec: result is method/reader/info); the live close facts arrive
        # on a later iprange.v1.reader.close.
        if method == "iprange.v1.reader.open":
            return
        sources = {}
        top = params.get("source") if isinstance(params.get("source"), dict) else None
        if top is not None:
            sources["params.source"] = top
        current = params.get("current") if isinstance(params.get("current"), dict) else None
        nested = (current.get("source")
                  if current is not None and isinstance(current.get("source"), dict) else None)
        if nested is not None:
            sources["params.current.source"] = nested
        # history.project carries the last-seen database directly in
        # params.last_seen ({path, mode}); it is one more live source.
        last_seen = params.get("last_seen") if isinstance(params.get("last_seen"), dict) else None
        if last_seen is not None:
            sources["params.last_seen"] = last_seen
        # history.project reports the live close facts as source_closes
        # (reader order); the other live-source families use source_close.
        close_member = "source_closes" if method == "iprange.v1.history.project" else "source_close"
        for label, source in sources.items():
            mode = source.get("mode") if isinstance(source, dict) else None
            if mode not in ("live", "immutable"):
                continue
            if mode == "live" and close_member not in result:
                raise AssertionError(
                    f"case {self.case['name']!r}: {method} opened a live source "
                    f"({label}) but returned no {close_member}")
            if mode == "immutable" and close_member in result:
                raise AssertionError(
                    f"case {self.case['name']!r}: {method} fabricated {close_member} "
                    f"for an immutable source ({label})")

    def verify_output_facts(self, facts, request_path):
        if request_path is not None and facts.get("path") != request_path:
            raise AssertionError(
                f"output path {facts.get('path')!r} does not match request {request_path!r}")
        path = facts.get("path")
        if not isinstance(path, str) or not path:
            raise AssertionError("output facts have no usable path")
        if not os.path.isabs(path):
            path = safe_work_path(self.work_dir, path)
        if not os.path.isfile(path):
            raise AssertionError(f"output file is missing: {path}")
        digest = sha256_file(path)
        if facts.get("sha256") != digest:
            raise AssertionError(
                f"output sha256 {facts.get('sha256')!r} does not match file digest {digest}")
        size = os.path.getsize(path)
        if facts.get("bytes") != str(size):
            raise AssertionError(
                f"output bytes {facts.get('bytes')!r} do not match file size {size}")

    # ---- protocol semantics ---------------------------------------
    def check_protocol(self, method, params, result):
        """Check cross-request contracts not expressible by one result schema."""

        if method == "iprange.v1.system.describe":
            # validate_result checks the full schema; this explicit call keeps
            # capability semantics observable at the runner layer as well.
            results.validate_system_describe(result)
        elif method == "iprange.v1.algebra.count":
            self.check_algebra_oracle(method, params, result)
        elif method == "iprange.v1.algebra.compare":
            self.check_algebra_oracle(method, params, result)
        elif method == "iprange.v1.reader.open":
            handle = result["reader"]
            self.readers[handle] = result["info"]["value_kind"]
            self.reader_families[handle] = result["info"]["address_family"]
        elif method == "iprange.v1.reader.close":
            self.readers.pop(params["reader"], None)
            self.reader_families.pop(params["reader"], None)
        elif method == "iprange.v1.reader.lookup":
            addresses = params.get("addresses", [])
            matches = result.get("matches", [])
            if len(matches) != len(addresses):
                raise AssertionError(
                    f"case {self.case['name']!r}: lookup returned {len(matches)} matches "
                    f"for {len(addresses)} addresses")
            reader_kind, reader_family = self.require_reader(
                params["reader"], "lookup")
            for index, (want, got) in enumerate(zip(addresses, matches)):
                self.check_address_family(reader_family, want, "lookup match", index)
                if got.get("address") != want:
                    raise AssertionError(
                        f"case {self.case['name']!r}: lookup match[{index}] address "
                        f"{got.get('address')!r} != requested {want!r}")
                self.check_lookup_payload_kind(reader_kind, got, index)
                self.pending_lookup.append((params["reader"], reader_kind, got))
        elif method == "iprange.v1.reader.matching_feeds":
            wanted = params.get("address")
            if result.get("address") != wanted:
                raise AssertionError(
                    f"case {self.case['name']!r}: matching_feeds address "
                    f"{result.get('address')!r} != requested {wanted!r}")
            _, reader_family = self.require_reader(params["reader"], "matching_feeds")
            self.check_address_family(
                reader_family,
                wanted,
                "matching_feeds address",
                None,
            )
        elif method == "iprange.v1.reader.ranges.open":
            if "start" in params:
                _, reader_family = self.require_reader(params["reader"], "ranges.open")
                self.check_address_family(
                    reader_family,
                    params["start"],
                    "ranges.open start",
                    None,
                )
            self.cursors[result["cursor"]] = {
                "kind": "ranges",
                "reader": params["reader"],
                "view": params["view"],
                "direction": params["direction"],
                "last": None,
                "closed": False,
                "complete": False,
                "records": [],
            }
        elif method == "iprange.v1.reader.ranges.next":
            cursor = self.require_cursor(params["cursor"], "ranges.next")
            forward = cursor["direction"] == "forward"
            _, reader_family = self.require_reader(cursor["reader"], "ranges.next")
            for record in result.get("records", []):
                self.check_range_record_shape(cursor["view"], record)
                for side in ("from", "to"):
                    self.check_address_family(
                        reader_family, record.get(side), "range record", side)
                key = (ip_int(record["from"]), ip_int(record["to"]))
                if cursor["last"] is not None:
                    prev, now = (cursor["last"], key) if forward else (key, cursor["last"])
                    if now < prev:
                        raise AssertionError(
                            f"case {self.case['name']!r}: ranges records out of "
                            f"{'ascending' if forward else 'descending'} order: "
                            f"{key} after {cursor['last']}")
                cursor["last"] = key
                cursor["records"].extend(self.oracle_record(cursor, record))
            if result.get("done"):
                cursor["closed"] = True
                cursor["complete"] = True
        elif method == "iprange.v1.reader.ranges.close":
            cursor = self.cursors.get(params["cursor"])
            if cursor is not None:
                cursor["closed"] = True
        elif method == "iprange.v1.reader.feeds.open":
            self.cursors[result["cursor"]] = {
                "kind": "feeds", "last": None, "closed": False,
            }
        elif method == "iprange.v1.reader.feeds.next":
            # Feed rows follow feed-catalog order (insertion order), which
            # is not a lexical order. The case's strict expect_result rows
            # are the authoritative order assertion; no extra ordering
            # proxy exists here.
            cursor = self.require_cursor(params["cursor"], "feeds.next")
            for row in result.get("feeds", []):
                if not isinstance(row, dict) or not isinstance(row.get("name"), str):
                    raise AssertionError(
                        f"case {self.case['name']!r}: feeds rows must be {{name}} objects")
            if result.get("done"):
                cursor["closed"] = True
        elif method == "iprange.v1.reader.feeds.close":
            cursor = self.cursors.get(params["cursor"])
            if cursor is not None:
                cursor["closed"] = True

    def require_reader(self, handle, operation):
        """A successful response must reference a reader this connection owns."""

        kind = self.readers.get(handle)
        family = self.reader_families.get(handle)
        if kind is None or family is None:
            raise AssertionError(
                f"case {self.case['name']!r}: {operation} succeeded on unknown "
                f"reader handle {handle!r}")
        return kind, family

    @staticmethod
    def check_address_family(expected, address, operation, index):
        """Require a canonical address to belong to an opened reader's family."""

        if expected is None:
            return
        import ipaddress

        actual = "ipv4" if ipaddress.ip_address(address).version == 4 else "ipv6"
        location = f"{operation}[{index}]" if index is not None else operation
        if actual != expected:
            raise AssertionError(
                f"case {location}: address {address!r} is {actual}, "
                f"but the reader family is {expected}")

    @staticmethod
    def check_lookup_payload_kind(reader_kind, fact, index):
        """Bind a present lookup payload to the opened reader's value kind."""

        if reader_kind is None or fact.get("present") is not True:
            return
        keys = set(fact) - {"address", "present"}
        payloads = {
            "direct": [{"value"}],
            "membership": [{"feeds"}],
            "structured": [{"asn", "country_id", "state_id", "city_id",
                             "location", "threat_feeds"}],
        }
        if keys not in payloads[reader_kind]:
            raise AssertionError(
                f"case lookup match[{index}]: reader kind {reader_kind!r} "
                f"returned payload keys {sorted(keys)!r}")

    def check_algebra_oracle(self, method, params, result):
        """Check algebra against text fixtures with scalar interval models."""

        sources = []
        for source in params.get("sources", []):
            path = source.get("source", {}).get("path")
            intervals = self.fixture_intervals.get(
                os.path.realpath(path) if isinstance(path, str) else path)
            if intervals is None:
                return
            sources.append(intervals)
        if not sources:
            return

        if method == "iprange.v1.algebra.count":
            if params.get("selection") != {"mode": "all"}:
                return
            _, expected = oracle.algebra_count("union", sources)
            actual = result.get("cardinality")
            if actual != str(expected):
                raise AssertionError(
                    f"case {self.case['name']!r}: algebra.count oracle expected "
                    f"{expected}, got {actual!r}")
            self.oracle_checks += 1
            return

        if params.get("left") != {"mode": "all"} or params.get("right") != {"mode": "all"}:
            return
        expected = oracle.compare(union_all(sources), union_all(sources))
        report = result.get("report", {})
        for key, value in expected.items():
            actual = report.get(key)
            if actual != (str(value) if type(value) is int else value):
                raise AssertionError(
                    f"case {self.case['name']!r}: algebra.compare oracle "
                    f"{key} expected {value!r}, got {actual!r}")
        self.oracle_checks += 1

    def require_cursor(self, handle, operation):
        cursor = self.cursors.get(handle)
        if cursor is None:
            raise AssertionError(
                f"case {self.case['name']!r}: {operation} on unknown cursor {handle!r}")
        if cursor["closed"]:
            raise AssertionError(
                f"case {self.case['name']!r}: {operation} after done/close")
        return cursor

    @staticmethod
    def check_range_record_shape(view, record):
        kind = view["kind"]
        if kind == "direct" and not isinstance(record.get("value"), int):
            raise AssertionError("direct range record must carry exactly a u32 value")
        if kind == "structured" and not isinstance(record.get("value"), dict):
            raise AssertionError("structured range record must carry its complete value object")
        if kind == "feed" and "value" in record:
            raise AssertionError("feed range record must not carry a value")
        if ip_int(record["from"]) > ip_int(record["to"]):
            raise AssertionError("range record endpoints are reversed")

    @staticmethod
    def oracle_record(cursor, record):
        start, end = ip_int(record["from"]), ip_int(record["to"])
        if cursor["view"]["kind"] == "feed":
            value = cursor["view"]["feed"]
        elif cursor["view"]["kind"] == "direct":
            value = record.get("value")
        else:
            value = record.get("value")
        return [oracle.Interval(start, end, value)]

    # ---- independent scalar oracle --------------------------------
    def finish_oracle(self):
        for handle, kind, actual in self.pending_lookup:
            if kind == "direct":
                state = self.completed_ranges(handle, "direct")
                if state:
                    expected = oracle.lookup_fact(oracle.normalize_intervals(state), ip_int(actual["address"]))
                    self.check_oracle_lookup(actual, expected)
            elif kind == "structured":
                state = self.completed_ranges(handle, "structured")
                if state:
                    expected = oracle.lookup_fact(oracle.normalize_intervals(state), ip_int(actual["address"]))
                    self.check_oracle_lookup(actual, expected)
            elif kind == "membership":
                for feed, state in self.completed_feed_ranges(handle).items():
                    expected_present = oracle.lookup(state, ip_int(actual["address"])) == feed
                    actual_present = feed in actual.get("feeds", [])
                    if expected_present != actual_present:
                        raise AssertionError(
                            f"case {self.case['name']!r}: oracle mismatch for {actual['address']} "
                            f"and feed {feed!r}: expected membership {expected_present}, "
                            f"got {actual_present}")
                    self.oracle_checks += 1

    def completed_ranges(self, handle, view_kind):
        intervals = []
        for cursor in self.cursors.values():
            if (cursor.get("kind") == "ranges" and cursor.get("reader") == handle
                    and cursor.get("view", {}).get("kind") == view_kind
                    and cursor.get("complete")):
                intervals.extend(cursor["records"])
        return intervals

    def completed_feed_ranges(self, handle):
        feeds = {}
        for cursor in self.cursors.values():
            view = cursor.get("view", {})
            if (cursor.get("kind") == "ranges" and cursor.get("reader") == handle
                    and view.get("kind") == "feed" and cursor.get("complete")):
                feeds[view["feed"]] = cursor["records"]
        return feeds

    def check_oracle_lookup(self, actual, expected):
        if actual.get("present") != expected.get("present"):
            raise AssertionError(
                f"case {self.case['name']!r}: oracle presence mismatch for "
                f"{actual.get('address')!r}: expected {expected.get('present')}, "
                f"got {actual.get('present')}")
        if expected.get("present"):
            expected_value = expected.get("value")
            if isinstance(expected_value, dict):
                actual_value = {key: actual.get(key) for key in expected_value}
            else:
                actual_value = actual.get("value")
            if actual_value != expected_value:
                raise AssertionError(
                    f"case {self.case['name']!r}: oracle value mismatch for "
                    f"{actual.get('address')!r}: expected {expected_value!r}, "
                    f"got {actual_value!r}")
        self.oracle_checks += 1

    # ---- legacy steps ---------------------------------------------
    def run_legacy_step(self, step):
        # Legacy CLI steps are point-in-time commands over work-dir files;
        # in a mixed matrix they belong to the consumer service binary.
        actor = "consumer"
        argv = self.substitute(step["argv"], actor)
        before = self.inventory()
        self.actor_steps[actor] = self.actor_steps.get(actor, 0) + 1
        if "legacy" not in self.actor_operations[actor]:
            self.actor_operations[actor].append("legacy")
        stdin_data = None
        if "stdin_fixture" in step:
            fixture = safe_work_path(self.work_dir, step["stdin_fixture"], must_exist=True)
            with open(fixture, "rb") as stream:
                stdin_data = stream.read()
        try:
            proc = subprocess.run(
                [self.binary] + argv,
                cwd=self.work_dir,
                input=stdin_data,
                capture_output=True,
                timeout=300,
                env=child_environment(),
            )
        except subprocess.TimeoutExpired as exc:
            raise AssertionError(f"case {self.case['name']!r}: legacy command timed out") from exc
        if proc.returncode != step["exit_status"]:
            raise AssertionError(
                f"case {self.case['name']!r}: exit {proc.returncode}, "
                f"expected {step['exit_status']}\n"
                f"stdout={proc.stdout[:400]!r}\nstderr={proc.stderr[:400]!r}")
        self.match_stream("stdout", proc.stdout, step["stdout"])
        self.match_stream("stderr", proc.stderr, step["stderr"])
        for assertion in step.get("assert_files", []):
            self.assert_file(assertion)
        self.record_ledger(before, step)

    def match_stream(self, name, data, expectation):
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError as exc:
            raise AssertionError(f"case {self.case['name']!r}: {name} is not UTF-8") from exc
        if "$exact" in expectation:
            if text != expectation["$exact"]:
                raise AssertionError(
                    f"case {self.case['name']!r}: {name} mismatch\n"
                    f"expected {expectation['$exact']!r}\ngot {text[:400]!r}")
        elif expectation["$contains"].encode("utf-8") not in data:
            raise AssertionError(
                f"case {self.case['name']!r}: {name} missing {expectation['$contains']!r}")

    # ---- filesystem assertions ------------------------------------
    def assert_file(self, assertion):
        path = safe_work_path(self.work_dir, assertion["path"], must_exist=True)
        if not os.path.isfile(path):
            raise AssertionError(
                f"case {self.case['name']!r}: assertion target is not a file {assertion['path']}")
        if "sha256" in assertion and sha256_file(path) != assertion["sha256"]:
            raise AssertionError(
                f"case {self.case['name']!r}: {assertion['path']} sha256 mismatch")
        if "equals_fixture" in assertion:
            other = safe_work_path(self.work_dir, assertion["equals_fixture"], must_exist=True)
            if open(path, "rb").read() != open(other, "rb").read():
                raise AssertionError(
                    f"case {self.case['name']!r}: {assertion['path']} differs from "
                    f"{assertion['equals_fixture']}")

    # ---- services -------------------------------------------------
    def service_for(self, actor):
        """Return (creating on first use) the service process for an actor.

        Single-actor mode keeps one connection and honors callers that
        pre-set ``service`` (the sensitivity gate installs a fake server
        there).  Mixed mode runs one real product service per actor; the
        services are separate processes that share only the work directory.
        """

        if not self.mixed:
            if self.service is None:
                self.service = self._spawn(
                    [self.binary, "--jsonrpc"], self.implementation)
            return self.service
        service = self.services.get(actor)
        if service is None:
            service = self._spawn(
                [self.actor_binaries[actor], "--jsonrpc"],
                f"{self.implementation}:{actor}")
            self.services[actor] = service
        return service

    def _spawn(self, argv, implementation):
        service = JsonRpcService(argv, implementation, cwd=self.work_dir,
                                 read_deadline=RUNNER_IO_DEADLINE_SECONDS,
                                 write_deadline=RUNNER_IO_DEADLINE_SECONDS)
        self.owned_services.append(service)
        return service

    # A peer that ends its own session does so asynchronously: the last answer
    # is already in our pipe while the peer is still on its way out, so a
    # zero-delay poll can report it alive.  One bounded settle per case makes
    # the liveness judgment repeatable instead of scheduler-dependent.
    DEATH_SETTLE_SECONDS = 0.02

    def owned_service_deaths(self):
        """Owned services that terminated before this runner tore them down.

        A product service lives from its first request until the runner closes
        its stdin, so a service already exited at that point did not end its
        own session: it crashed, was killed, or left its request loop.  The
        check cannot live in ``JsonRpcService.close()`` because the crash
        battery legitimately kills its own peers and then closes them, and the
        already-dead exemption is what lets those scenarios report a crash as
        evidence; the matrix runner never kills a peer, so the rule belongs
        here.
        """

        if self.owned_services and any(
                service.proc.poll() is None
                for service in self.owned_services):
            time.sleep(self.DEATH_SETTLE_SECONDS)
        deaths = []
        for service in self.owned_services:
            status = service.proc.poll()
            if status is None:
                continue
            deaths.append({
                "argv": list(service.argv),
                "implementation": service.implementation,
                "exit_status": status,
                "signal": -status if status < 0 else None,
                "stderr_tail": list(service.stderr_tail[-5:]),
            })
        return deaths

    def close_services(self):
        """Tear down every owned service and report teardown faults.

        A clean session ends with the peer exiting 0 after stdin EOF; anything
        else is a qualification failure.  Failures are returned instead of
        raised because raising from teardown used to escape ``run_one`` mid
        loop: the remaining cases, the summary, and the report write were all
        abandoned, so the run's behaviour depended on how fast a peer died
        rather than on what was measured.  Every service is still closed even
        when an earlier one faulted, so no child outlives the case.
        """

        problems = []
        owned = [("single", self.service)] if self.service is not None else []
        owned += sorted(self.services.items())
        for actor, service in owned:
            try:
                service.close()
            except AssertionError as exc:
                problems.append({
                    "actor": actor,
                    "argv": list(service.argv),
                    "implementation": service.implementation,
                    "error": str(exc),
                    "exit_status": service.proc.returncode,
                    "stderr_tail": list(service.stderr_tail[-5:]),
                })
        self.service = None
        self.services = {}
        return problems

    # ---- full case -------------------------------------------------
    def run(self):
        self.build_fixtures()
        for step in self.case["steps"]:
            if step["kind"] == "rpc":
                self.run_rpc_step(step)
            else:
                self.run_legacy_step(step)
        for assertion in self.case.get("assertions", {}).get("files", []):
            self.assert_file(assertion)
        self.finish_oracle()


class JsonRpcService:
    """Strict JSON-RPC stdio client over one persistent subprocess."""

    def __init__(self, argv, implementation, *, cwd=None,
                 start_new_session=False, read_deadline=None,
                 write_deadline=None):
        self.argv = list(argv)
        self.implementation = implementation
        self.proc = subprocess.Popen(
            self.argv,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=child_environment(),
            cwd=cwd,
            start_new_session=start_new_session,
        )
        # Deadline-bounded mode reads and writes the raw pipe fds
        # under selectors (POSIX); the buffered wrappers cannot be
        # time-boxed and would hide bytes from the selector loops, so
        # they are detached (the raw owners are kept in the service
        # object).  Windows cannot select() on pipes (WinError 10038),
        # so a Windows deadline-bounded service keeps the buffered
        # wrappers and applies the deadline with worker threads
        # instead; a timed-out thread poisons the service because its
        # bytes can no longer be attributed to a correlated read.
        self.read_deadline = read_deadline
        self.write_deadline = write_deadline
        self._read_buf = b""
        self._raw_stdin = None
        self._raw_stdout = None
        self._use_threads = bool(self.read_deadline or self.write_deadline) \
            and os.name == "nt"
        if write_deadline is not None and not self._use_threads:
            self._raw_stdin = self.proc.stdin.detach()
        if read_deadline is not None and not self._use_threads:
            self._raw_stdout = self.proc.stdout.detach()
        self._poisoned = False
        # Worker threads of deadline-bounded threaded I/O (Windows);
        # close() joins them under a bound after the peer is gone.
        self._io_threads = []
        self.lock = threading.Lock()
        self.stderr_tail = []

        def _drain():
            for raw in self.proc.stderr:
                self.stderr_tail.append(raw.decode("utf-8", "replace"))
                if len(self.stderr_tail) > 20:
                    self.stderr_tail.pop(0)

        self.drainer = threading.Thread(target=_drain, daemon=True)
        self.drainer.start()

    def call(self, request_id, method, params):
        with self.lock:
            request = {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}
            wire = json.dumps(request, separators=(",", ":"), ensure_ascii=False)
            wire_bytes = wire.encode("utf-8")
            # Raw wire bytes of the request frame, including the LF
            # terminator (the transport is one physical line per frame).
            request_bytes = len(wire_bytes) + 1
            if len(wire_bytes) > frame.INPUT_FRAME_LIMIT:
                raise AssertionError("client request frame over limit")
            if self._poisoned:
                raise AssertionError(
                    "service poisoned by a bounded I/O timeout; "
                    "a later exchange cannot be correlated")
            try:
                if self.write_deadline is None:
                    self.proc.stdin.write(wire_bytes + b"\n")
                    self.proc.stdin.flush()
                elif self._use_threads:
                    self._write_bounded_thread(wire_bytes + b"\n")
                else:
                    self._write_bounded(wire_bytes + b"\n")
            except TimeoutError as exc:
                raise AssertionError(
                    f"service did not accept the request within the "
                    f"bounded write deadline: {exc}") from exc
            except (BrokenPipeError, OSError) as exc:
                raise AssertionError(
                    f"service closed stdin: {''.join(self.stderr_tail[-5:])}") from exc
            if self.read_deadline is None:
                # Bound the no-deadline readline: the buffered
                # wrapper's readline(size) never buffers more than
                # size bytes, so a peer emitting an unterminated frame
                # can no longer accumulate unbounded output (external
                # review finding).
                line = self.proc.stdout.readline(
                    frame.OUTPUT_FRAME_LIMIT + 2)
            elif self._use_threads:
                try:
                    line = self._readline_bounded_thread()
                except TimeoutError as exc:
                    raise AssertionError(
                        f"service did not answer within the bounded read "
                        f"deadline: {exc}") from exc
                except frame.FrameError as exc:
                    self._poisoned = True
                    raise AssertionError(str(exc)) from exc
            else:
                try:
                    line = self._readline_bounded()
                except TimeoutError as exc:
                    raise AssertionError(
                        f"service did not answer within the bounded read "
                        f"deadline: {exc}") from exc
                except frame.FrameError as exc:
                    self._poisoned = True
                    raise AssertionError(str(exc)) from exc
            if not line:
                raise AssertionError(
                    f"service closed stdout; stderr={''.join(self.stderr_tail[-5:])}")
            if len(line) > frame.OUTPUT_FRAME_LIMIT + 1:
                # One response frame at or over the output ceiling
                # (payload + terminator): violating the ceiling on a
                # shared stream poisons it, later bytes cannot be
                # correlated (external review finding).
                self._poisoned = True
                raise AssertionError(
                    f"response frame of {len(line)} bytes exceeds the "
                    f"{frame.OUTPUT_FRAME_LIMIT} byte output ceiling")
            # Raw response frame as read, LF terminator included; same
            # unit as the request frame.
            record_frame_size(method, request_bytes, len(line))
            try:
                return self.decode_response_line(line, request_id)
            except (frame.FrameError, UnicodeDecodeError) as exc:
                raise AssertionError(str(exc)) from exc

    def notify(self, method, params):
        """Send one JSON-RPC notification: no id, no response expected.

        The transport accepts only iprange.v1.cancel as a notification
        (frame.py contract).  The client never reads a line for a
        notification; a server that answers one desynchronizes the
        stream, and the next correlated read fails with an id mismatch.
        """

        with self.lock:
            request = {"jsonrpc": "2.0", "method": method, "params": params}
            wire = json.dumps(request, separators=(",", ":"), ensure_ascii=False)
            wire_bytes = wire.encode("utf-8")
            request_bytes = len(wire_bytes) + 1
            if len(wire_bytes) > frame.INPUT_FRAME_LIMIT:
                raise AssertionError("client notification frame over limit")
            if self._poisoned:
                raise AssertionError(
                    "service poisoned by a bounded I/O timeout; "
                    "a later exchange cannot be correlated")
            try:
                if self.write_deadline is None:
                    self.proc.stdin.write(wire_bytes + b"\n")
                    self.proc.stdin.flush()
                elif self._use_threads:
                    self._write_bounded_thread(wire_bytes + b"\n")
                else:
                    self._write_bounded(wire_bytes + b"\n")
            except TimeoutError as exc:
                raise AssertionError(
                    f"service did not accept the notification within the "
                    f"bounded write deadline: {exc}") from exc
            except (BrokenPipeError, OSError) as exc:
                raise AssertionError(
                    f"service closed stdin: {''.join(self.stderr_tail[-5:])}") from exc
            # A notification has no response frame; only the request side
            # contributes to the per-method frame-size record.
            record_frame_size(method, request_bytes)

    def decode_response_line(self, line, request_id):
        if not line.endswith(b"\n"):
            raise frame.FrameError(frame.STD_PARSE_ERROR, "response frame is not LF terminated")
        encoded = line[:-1]
        if encoded.endswith(b"\r"):
            raise frame.FrameError(frame.STD_PARSE_ERROR, "response uses CRLF instead of LF")
        text = encoded.decode("utf-8")
        response = frame.decode_response(text)
        if len(text.encode("utf-8")) > frame.RESPONSE_OBJECT_LIMIT:
            raise frame.FrameError(
                frame.TRANSPORT_FRAME_TOO_LARGE, "response object exceeds 65000 bytes")
        if response.get("id") != request_id:
            raise frame.FrameError(
                frame.STD_INVALID_REQUEST,
                f"response id {response.get('id')!r} != request id {request_id!r}")
        return response

    def _write_bounded_thread(self, payload):
        """Write one request frame under the deadline via a worker
        thread (Windows: select() cannot wait on pipes).

        A worker thread performs the blocking write; the caller joins
        it for at most the configured deadline.  On timeout the
        service is poisoned: the stuck thread may later consume or
        interleave bytes, so no other exchange can be correlated.
        """

        done = threading.Event()
        result = {}

        def worker():
            try:
                self.proc.stdin.write(payload)
                self.proc.stdin.flush()
                result["ok"] = True
            except Exception as exc:  # noqa: BLE001 - propagated below
                result["err"] = exc
            finally:
                done.set()

        thread = threading.Thread(target=worker, daemon=True)
        self._io_threads.append(thread)
        thread.start()
        if not done.wait(self.write_deadline):
            self._poisoned = True
            raise TimeoutError(
                f"write deadline {self.write_deadline:.3f} s expired")
        if "err" in result:
            raise result["err"]

    def _readline_bounded_thread(self):
        """Read one LF-terminated frame under the deadline via a
        worker thread (Windows: select() cannot wait on pipes).

        The worker reads raw chunks off the pipe fd (never the
        buffered wrapper's readline, which can buffer a peer's output
        without bound) and the caller accumulates them in the same
        per-frame buffer as the POSIX path, so partial frames stay
        intact across calls and a frame that grows past the output
        ceiling is rejected instead of retained (external review
        finding).
        """

        done = threading.Event()
        result = {}

        def worker():
            try:
                fd = self.proc.stdout.fileno()
                while True:
                    if b"\n" in self._read_buf:
                        break
                    if len(self._read_buf) > frame.OUTPUT_FRAME_LIMIT:
                        break
                    chunk = os.read(fd, 65536)
                    if not chunk:
                        break
                    self._read_buf += chunk
                result["ok"] = True
            except Exception as exc:  # noqa: BLE001 - propagated below
                result["err"] = exc
            finally:
                done.set()

        thread = threading.Thread(target=worker, daemon=True)
        self._io_threads.append(thread)
        thread.start()
        if not done.wait(self.read_deadline):
            self._poisoned = True
            raise TimeoutError(
                f"read deadline {self.read_deadline:.3f} s expired")
        if "err" in result:
            raise result["err"]
        line_end = self._read_buf.find(b"\n")
        if line_end >= 0:
            line = self._read_buf[:line_end + 1]
            self._read_buf = self._read_buf[line_end + 1:]
            return line
        if len(self._read_buf) > frame.OUTPUT_FRAME_LIMIT:
            raise frame.FrameError(
                frame.TRANSPORT_FRAME_TOO_LARGE,
                "response frame grew past the output ceiling without "
                "a terminator")
        # EOF tail: return the remaining bytes exactly like the POSIX
        # path so a peer that closes stdout mid-frame surfaces the
        # closed-stdout error, not a hang.
        remaining, self._read_buf = self._read_buf, b""
        return remaining

    def _write_bounded(self, payload):
        """Write one request frame under ``self.write_deadline``.

        The raw stdin fd is switched to non-blocking once; a selector
        waits for writability only up to the remaining deadline, so a
        child that stops draining stdin raises TimeoutError instead
        of blocking the harness forever on a full pipe.
        """

        import selectors

        fd = self._raw_stdin.fileno()
        os.set_blocking(fd, False)
        sel = selectors.DefaultSelector()
        sel.register(fd, selectors.EVENT_WRITE)
        deadline = time.monotonic() + self.write_deadline
        view = memoryview(payload)
        written = 0
        try:
            while written < len(view):
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError(
                        f"write deadline {self.write_deadline:.3f} s "
                        "expired")
                if not sel.select(remaining):
                    continue
                try:
                    written += os.write(fd, view[written:])
                except BlockingIOError:
                    continue
        finally:
            sel.close()

    def _readline_bounded(self):
        """Read one LF-terminated response frame under the deadline.

        Raw fd with non-blocking reads under a selector; only complete
        LF-terminated frames are returned adequate to the caller, and
        bytes already read stay in ``_read_buf`` across calls.  A peer
        that holds a partial line open cannot block the client past
        the deadline (TimeoutError); EOF returns the remaining bytes.
        """

        import selectors

        fd = self._raw_stdout.fileno()
        os.set_blocking(fd, False)
        sel = selectors.DefaultSelector()
        sel.register(fd, selectors.EVENT_READ)
        deadline = time.monotonic() + self.read_deadline
        try:
            while True:
                line_end = self._read_buf.find(b"\n")
                if line_end >= 0:
                    line = self._read_buf[:line_end + 1]
                    self._read_buf = self._read_buf[line_end + 1:]
                    return line
                if len(self._read_buf) > frame.OUTPUT_FRAME_LIMIT:
                    raise frame.FrameError(
                        frame.TRANSPORT_FRAME_TOO_LARGE,
                        "response frame grew past the output ceiling "
                        "without a terminator")
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError(
                        f"read deadline {self.read_deadline:.3f} s "
                        "expired")
                if not sel.select(remaining):
                    continue
                try:
                    chunk = os.read(fd, 65536)
                except (BlockingIOError, InterruptedError):
                    continue
                if not chunk:
                    remaining_buf, self._read_buf = self._read_buf, b""
                    return remaining_buf
                self._read_buf += chunk
        finally:
            sel.close()

    def close(self, allow_forced=False, broken_exchange=False):
        """Close stdin and wait for this owned subprocess to terminate.

        Bounded teardown: a peer that does not exit after stdin EOF is
        reaped with a bounded kill.  A peer that had to be
        force-terminated BY THIS CALL is reported as a qualification
        failure (it did not finish its normal EOF shutdown) unless
        ``allow_forced`` is set (deliberate-stall controls).  Peers
        already terminated by the caller (crash scenarios' process
        groups) are reaped silently.

        An ordinary successful session ends with a bounded final drain
        of stdout and a clean exit: after the response set is
        satisfied, every remaining stdout byte is validated (any
        non-whitespace residue is a stray trailing frame) and the
        process exit status must be 0.  Both checks apply only to a
        session this call shuts down (the peer was still alive at
        entry) and only when ``allow_forced`` and ``broken_exchange``
        are not set, so the deliberate-stall controls, the
        deliberate-brokenness sensitivity controls (whose leftover
        frames are the evidence of the desync), and the harness's
        intentional crash sessions (peers already terminated by the
        caller) keep their documented behavior; a poisoned threaded
        peer's failure was already reported by ``call()``.

        In threaded mode (Windows deadlines), a peer poisoned by a
        bounded-I/O timeout is reaped before touching buffered
        wrappers whose locks a blocked worker may still hold; the
        worker threads are then joined under a bound (external review
        finding).
        """

        # Whether the peer was already gone before this call acted on
        # it.  Peers pre-terminated by the harness (crash scenarios'
        # process groups) are a separate, intentional class: their
        # exit status and any residue are the crash evidence, not a
        # clean-session violation.
        already_dead = self.proc.poll() is not None
        forced = False
        if self._use_threads and self._poisoned:
            # A timed-out writer may still hold the buffered stdin
            # lock, so closing the wrapper would block.  Reap the
            # child first: its death fails the blocked write (releasing
            # the lock) and ends a blocked read with EOF; then the
            # wrapped streams can be closed without waiting on a
            # worker whose bytes can no longer be correlated.  If the
            # child already exited (a poisoned peer self-terminated
            # before close), only reap it: kill() on a dead child
            # would escape close() and mask the original failure
            # (role-round finding), and this call did not force it.
            if self.proc.poll() is None:
                self.proc.kill()
                forced = True
            self.proc.wait(timeout=5)
        else:
            try:
                if self._raw_stdin is not None:
                    self._raw_stdin.close()
                elif self.proc.stdin and not self.proc.stdin.closed:
                    self.proc.stdin.close()
                if self.read_deadline is None and self.write_deadline is None:
                    self.proc.wait(timeout=30)
                else:
                    # A deadline-bounded service belongs to a harness
                    # whose every exchange is bounded; a stalled child
                    # must not pin the proof in cleanup either.  Real
                    # products exit at stdin EOF in milliseconds, so
                    # the short grace only affects stalled peers.
                    self.proc.wait(timeout=0.2)
            except Exception:
                forced = True
                if self.proc.poll() is None:
                    self.proc.kill()
                self.proc.wait(timeout=5)
        # Join bounded-I/O worker threads under a bound; they are
        # unblocked by the peer's exit or kill above.
        for thread in self._io_threads:
            thread.join(timeout=5)
        # The stderr drainer ends at EOF once the peer is gone; join it
        # before closing its stream so a closed-file race cannot appear.
        if getattr(self, "drainer", None) is not None:
            self.drainer.join(timeout=3)
        # Ordinary-session final validation: the response set is
        # complete, so any remaining stdout bytes are a stray trailing
        # frame and a nonzero exit is an unclean end to a session this
        # call shut down.  A peer that was already gone at entry (an
        # intentional crash session) and deliberate-stall controls are
        # exempt; a forced teardown reports itself instead.
        if forced and not allow_forced:
            raise AssertionError(
                "service did not terminate cleanly at stdin EOF and had "
                f"to be force-terminated (returncode "
                f"{self.proc.returncode})")
        if not already_dead and not allow_forced and \
                not broken_exchange and \
                not (self._use_threads and self._poisoned):
            trailing = self._drain_trailing_stdout()
            if trailing.strip():
                raise AssertionError(
                    f"service wrote {len(trailing)} unexpected "
                    f"trailing byte(s) on stdout after the response "
                    f"set (expected zero): {trailing[:120]!r}")
            status = self.proc.returncode
            if status != 0:
                raise AssertionError(
                    f"service exited with status {status} after a "
                    f"successful session (expected 0)")
        # Close the buffered wrappers only after the peer is gone: a
        # still-blocked writer's write fails once the peer's pipe end
        # closes, releasing the lock.
        for stream in (self.proc.stdin, self.proc.stdout, self.proc.stderr):
            try:
                if stream is not None and not stream.closed:
                    stream.close()
            except Exception:
                pass

    def _drain_trailing_stdout(self):
        """Every stdout byte remaining after the response set.

        Collects bytes the bounded reader already pulled into
        ``_read_buf`` (never yet validated), bytes sitting in the
        buffered wrapper, and bytes still in the pipe.  Callers run
        this only after the child has exited (or been killed), so the
        pipe delivers EOF and the drain cannot block; the buffered
        reads are additionally bounded by a byte ceiling so a
        descendant still holding the pipe open cannot accumulate
        unbounded memory.
        """

        parts = []
        if self._read_buf:
            parts.append(self._read_buf)
            self._read_buf = b""
        if self._raw_stdout is not None:
            # Deadline-bounded POSIX branch: the buffered wrapper was
            # detached, so the pipe is read directly.  The peer is
            # already gone, so the pipe delivers the remaining bytes
            # and then EOF; the deadline still bounds a descendant
            # that keeps a write end open.
            import selectors
            fd = self._raw_stdout.fileno()
            os.set_blocking(fd, False)
            sel = selectors.DefaultSelector()
            sel.register(fd, selectors.EVENT_READ)
            deadline = time.monotonic() + (self.read_deadline or 1.0)
            try:
                while time.monotonic() < deadline:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        break
                    if not sel.select(min(remaining, 0.5)):
                        continue
                    try:
                        chunk = os.read(fd, 65536)
                    except (BlockingIOError, InterruptedError):
                        continue
                    if not chunk:
                        break
                    parts.append(chunk)
            finally:
                sel.close()
            return b"".join(parts)
        stream = self.proc.stdout
        if stream is not None and not stream.closed:
            try:
                buffered = stream.peek()
            except (ValueError, OSError):
                buffered = b""
            if buffered:
                parts.append(stream.read(len(buffered)))
            total = len(buffered)
            ceiling = frame.OUTPUT_FRAME_LIMIT * 2
            while total <= ceiling:
                chunk = stream.read1(65536)
                if not chunk:
                    break
                total += len(chunk)
                parts.append(chunk)
        return b"".join(parts)


def parse_interval_text(text):
    """Parse a plain range/CIDR/single-IP fixture, or return None.

    Unsupported legacy text features are not errors here; the oracle simply
    remains silent for fixtures this independent scalar model cannot express.
    """

    import ipaddress

    intervals = []
    try:
        for raw_line in text.splitlines():
            line = raw_line.split("#", 1)[0].split(";", 1)[0].strip()
            if not line:
                continue
            if "," in line:
                fields = line.split(",")
                if fields[0].strip() == "from":
                    continue  # CSV header
                left, right = fields[0], fields[1]
                start = int(ipaddress.ip_address(left.strip()))
                end = int(ipaddress.ip_address(right.strip()))
            elif "-" in line:
                left, right = line.split("-", 1)
                start = int(ipaddress.ip_address(left.strip()))
                end = int(ipaddress.ip_address(right.strip()))
            elif "/" in line:
                network = ipaddress.ip_network(line, strict=False)
                start, end = int(network.network_address), int(network.broadcast_address)
            else:
                start = end = int(ipaddress.ip_address(line))
            if start > end:
                return None
            intervals.append((start, end))
    except (ValueError, UnicodeError):
        return None
    return intervals


def union_all(interval_lists):
    combined = [interval for intervals in interval_lists for interval in intervals]
    return oracle.union([combined])


def ip_int(address):
    """Numeric value of an IPv4/IPv6 address for order comparisons."""

    import ipaddress
    return int(ipaddress.ip_address(address))


def write_text(path, data):
    with open(path, "w", encoding="utf-8", newline="") as stream:
        stream.write(data)


def write_bytes(path, data):
    with open(path, "wb") as stream:
        stream.write(data)


def generate_fixture(path, source, fixture_tool):
    generator = source["generator"]
    seed = source.get("seed", 0)
    if generator == "ipv4_random_ranges":
        import ipaddress
        import random
        rng = random.Random(seed)
        lines = []
        for _ in range(1024):
            start = rng.randrange(0, 2**32)
            end = min(start + rng.randrange(0, 4096), 2**32 - 1)
            lines.append(f"{ipaddress.IPv4Address(start)}-{ipaddress.IPv4Address(end)}\n")
        write_text(path, "".join(lines))
        return
    if generator == "ipv6_random_ranges":
        import ipaddress
        import random
        rng = random.Random(seed)
        lines = []
        for _ in range(512):
            start = rng.getrandbits(128)
            end = min(start + rng.getrandbits(64), 2**128 - 1)
            lines.append(f"{ipaddress.IPv6Address(start)}-{ipaddress.IPv6Address(end)}\n")
        write_text(path, "".join(lines))
        return
    if generator != "v4_fixture":
        raise ValueError(f"unknown fixture generator {generator!r}")
    if fixture_tool is None:
        raise ValueError("case uses v4_fixture but --fixture-tool was not supplied")
    kinds = {0: "direct-v4", 1: "membership-v4", 2: "structured-v4"}
    try:
        kind = kinds[seed]
    except KeyError as exc:
        raise ValueError(
            f"v4_fixture seed {seed!r} has no fixed kind; expected one of {sorted(kinds)}") from exc
    try:
        proc = subprocess.run(
            [fixture_tool, kind, path],
            capture_output=True,
            timeout=300,
            env=child_environment(),
        )
    except subprocess.TimeoutExpired as exc:
        raise ValueError("v4_fixture timed out") from exc
    if proc.returncode != 0:
        detail = proc.stderr.decode("utf-8", "replace").strip()
        raise ValueError(f"v4_fixture failed with exit {proc.returncode}: {detail}")


def _self_test():
    """Exercise runner-side protocol helpers and the final-output
    contract of the shared JSON-RPC service.

    Runs on every runner invocation (main() calls it before loading
    cases): helper pins stay subprocess-free; the final-output
    controls spawn six stub services (a few tens of milliseconds
    each) to pin the clean-session end contract in both client I/O
    branches, and the negative-expectation controls spawn four more
    (two codes x two directions).
    """

    import tempfile

    with tempfile.TemporaryDirectory(dir=owned_temp_root()) as work:
        runner = CaseRunner(None, {
            "schema": "iprange-cli-case-v1",
            "name": "self-test",
            "fixtures": [],
            "steps": [],
        }, work, "test")
        runner.readers["r"] = "membership"
        runner.reader_families["r"] = "ipv4"
        runner.check_lookup_payload_kind("membership", {
            "address": "192.0.2.1", "present": False,
        }, 0)
        runner.check_lookup_payload_kind("membership", {
            "address": "192.0.2.1", "present": True, "feeds": ["feed-a"],
        }, 1)
        try:
            runner.check_lookup_payload_kind("membership", {
                "address": "192.0.2.1", "present": True, "value": 10,
            }, 2)
        except AssertionError:
            pass
        else:
            raise AssertionError("membership reader accepted a direct payload")

        try:
            runner.check_protocol("iprange.v1.reader.lookup", {
                "reader": "r", "addresses": ["2001:db8::1"],
            }, {
                "method": "iprange.v1.reader.lookup",
                "matches": [{"address": "2001:db8::1", "present": False}],
            })
        except AssertionError as exc:
            assert "reader family is ipv4" in str(exc)
        else:
            raise AssertionError("IPv4 reader accepted an IPv6 lookup address")

        try:
            runner.check_protocol("iprange.v1.reader.ranges.open", {
                "reader": "r", "view": {"kind": "direct"},
                "direction": "forward", "start": "2001:db8::1",
                "batch_size": 1,
            }, {"method": "iprange.v1.reader.ranges.open", "cursor": "c"})
        except AssertionError as exc:
            assert "reader family is ipv4" in str(exc)
        else:
            raise AssertionError("IPv4 reader accepted an IPv6 range start")

        fixture = os.path.join(work, "ranges.txt")
        write_text(fixture, "192.0.2.0-192.0.2.9\n198.51.100.0/30\n")
        intervals = parse_interval_text(open(fixture, encoding="utf-8").read())
        runner.fixture_intervals[os.path.realpath(fixture)] = intervals
        source = {"source": {"path": fixture, "mode": "immutable"},
                  "scope": {"mode": "all"},
                  "membership_query_budget": {"max_heap_bytes": "1"}}
        params = {"sources": [source], "selection": {"mode": "all"},
                  "algebra_budget": {"max_heap_bytes": "1", "max_sources": 1}}
        runner.check_algebra_oracle("iprange.v1.algebra.count", params, {
            "method": "iprange.v1.algebra.count", "cardinality": "14",
        })
        assert runner.oracle_checks == 1
        compare_params = {
            "sources": [source],
            "left": {"mode": "all"},
            "right": {"mode": "all"},
            "algebra_budget": {"max_heap_bytes": "1", "max_sources": 1},
        }
        runner.check_algebra_oracle("iprange.v1.algebra.compare", compare_params, {
            "method": "iprange.v1.algebra.compare",
            "report": {
                "left_addresses": "14", "right_addresses": "14",
                "overlap_addresses": "14", "left_only_addresses": "0",
                "right_only_addresses": "0", "union_addresses": "14",
                "equal": True,
            },
        })
        assert runner.oracle_checks == 2

    # Final-output negative controls (kind-gate finding 7): an
    # ordinary successful session must end with a clean stdout (no
    # unexpected non-whitespace bytes after the response set) and
    # exit status 0.  A controlled service that answers the request
    # correctly and then writes an extra non-JSON line, or exits with
    # status 7 after stdin EOF, must fail close() while call()
    # still returns the response; both client I/O branches (POSIX and
    # the forced threaded branch) are covered, and a clean session
    # keeps passing.  The stubs stay alive until close() closes
    # stdin, so the peer is still alive when close() shuts it down.
    # Stub answers go through sys.stdout.buffer with an explicit LF byte
    # terminator: a text-mode print() on native Windows translates the
    # terminator to CRLF, which decode_response_line correctly rejects,
    # so a natively executed self-test would die before its guard ran.
    read_resp = ("import sys,json;"
                 "r=json.loads(sys.stdin.buffer.readline());"
                 "sys.stdout.buffer.write(json.dumps({'jsonrpc':'2.0',"
                 "'id':r['id'],'result':{}}).encode()+b'\\n');"
                 "sys.stdout.buffer.flush();")
    for threaded in (False, True):
        for label, tail in (
                ("trailing-output", "print('not-json',flush=True);"
                                    "sys.stdin.buffer.read()"),
                ("nonzero-exit", "sys.stdin.buffer.read();sys.exit(7)")):
            # Deadline-bounded branch selection mirrors the harness:
            # the POSIX branch detaches the raw pipes at construction
            # (deadlines given up front), while the forced threaded
            # branch keeps the buffered wrappers and applies the
            # deadlines with worker threads (deadlines set after
            # construction, exactly like the reviewer control).
            service = JsonRpcService(
                [sys.executable, "-c", read_resp + tail], "stub",
                read_deadline=0.2 if not threaded else None,
                write_deadline=0.2 if not threaded else None)
            if threaded:
                service.read_deadline = 0.2
                service.write_deadline = 0.2
                service._use_threads = True
            try:
                response = service.call(
                    "1", "iprange.v1.system.describe", {})
                assert "result" in response, response
                try:
                    service.close()
                except AssertionError as exc:
                    close_failure = str(exc)
                else:
                    close_failure = None
                if close_failure is None:
                    raise AssertionError(
                        f"final-output control {label} "
                        f"(threaded={threaded}): close() accepted an "
                        f"unclean session end")
                if "trailing" not in close_failure and \
                        "exited with status" not in close_failure:
                    raise AssertionError(
                        f"final-output control {label} "
                        f"(threaded={threaded}) failed for an "
                        f"unrelated reason: {close_failure}")
            finally:
                if service.proc.poll() is None:
                    service.proc.kill()
                    service.proc.wait(timeout=2)
        clean = JsonRpcService(
            [sys.executable, "-c", read_resp + "sys.stdin.buffer.read()"],
            "stub",
            read_deadline=0.2 if not threaded else None,
            write_deadline=0.2 if not threaded else None)
        if threaded:
            clean.read_deadline = 0.2
            clean.write_deadline = 0.2
            clean._use_threads = True
        try:
            response = clean.call("1", "iprange.v1.system.describe", {})
            assert "result" in response, response
            clean.close()  # must not raise for a clean session
        finally:
            if clean.proc.poll() is None:
                clean.proc.kill()
                clean.proc.wait(timeout=2)

    # Negative-expectation control (tester round-10 P3-1): a step that
    # declares expect_error must FAIL when the service answers with a
    # success response -- the old fall-through to the absent
    # expect_result assertions laundered the missing refusal, so a
    # mutant engine that accepted refused input kept the case green.
    # The pair below pins both directions with stub services: a success
    # answer on a negative step raises, and a genuine product-error
    # answer still passes through check_expected_error untouched.  The
    # method is reader.close because its strict result schema
    # (method/closed) is fully satisfiable by the stub, so the
    # success-laundering arm must trip the guard itself and nothing
    # else; a weaker guard shape fails this control for the wrong reason.
    # Both arms run over TWO declared codes (tester round-11 F-A): a
    # guard narrowed to one hard-coded domain code survives a single-code
    # control while laundering every negative step that declares another
    # code.  input_format covers the streaming-refusal class;
    # invalid_argument is the corpus's most-used negative code (25 steps),
    # so a code-narrowed guard cannot survive the pair.
    ok_resp = ("import sys,json;"
               "r=json.loads(sys.stdin.buffer.readline());"
               "sys.stdout.buffer.write(json.dumps({'jsonrpc':'2.0',"
               "'id':r['id'],'result':{'method':'iprange.v1.reader.close',"
               "'closed':True}}).encode()+b'\\n');"
               "sys.stdout.buffer.flush();")

    def err_resp(domain_code):
        return ("import sys,json;"
                "r=json.loads(sys.stdin.buffer.readline());"
                "sys.stdout.buffer.write(json.dumps({'jsonrpc':'2.0',"
                "'id':r['id'],'error':{'code':-32010,'message':"
                "'stub refusal','data':{'code':'" + domain_code + "',"
                "'outcome':'not_started'}}}).encode()+b'\\n');"
                "sys.stdout.buffer.flush();")

    for code in ("input_format", "invalid_argument"):
        negative_step = {
            "method": "iprange.v1.reader.close",
            "actor": "consumer",
            "params": {"reader": "0" * 32},
            "expect_error": {"code": code, "outcome": "not_started"},
        }
        for label, stub_src, must_raise in (
                ("success-laundering", ok_resp, True),
                ("genuine-refusal", err_resp(code), False),
        ):
            service = JsonRpcService([sys.executable, "-c", stub_src
                                      + "sys.stdin.buffer.read()"], "stub")
            runner.service = service
            try:
                try:
                    runner.run_rpc_step(negative_step)
                except AssertionError as exc:
                    if not must_raise:
                        raise AssertionError(
                            f"expect_error control {label}/{code}: a genuine "
                            f"product error must pass, got: {exc}") from exc
                    if ("succeeded but the step declared expect_error"
                            not in str(exc)):
                        raise AssertionError(
                            f"expect_error control {label}/{code}: failed "
                            f"for an unrelated reason: {exc}") from exc
                else:
                    if must_raise:
                        raise AssertionError(
                            f"expect_error control {label}/{code}: a success "
                            "response on a step declaring expect_error was "
                            "accepted")
            finally:
                runner.service = None
                if service.proc.poll() is None:
                    service.proc.kill()
                    service.proc.wait(timeout=2)

    # Guard-shape pin (tester round-11 F-C): the behavioral controls
    # above sample one point per dimension, so a guard narrowed by any
    # added conjunction (pinned to this control's method, outcome, or
    # code set) survives them while laundering every negative step that
    # differs from the sampled point.  This pin reads the guard's
    # syntax tree instead: run_rpc_step must contain EXACTLY one `if`
    # whose test is the two-operand conjunction of the response-has-no-
    # error test and the expect_error-is-present test, with no extra
    # conjunct and no replaced operand shape.  Any conjunction a
    # future edit adds across any dimension fails here, not silently.
    import ast
    import inspect
    import textwrap

    guard_tree = ast.parse(
        textwrap.dedent(inspect.getsource(CaseRunner.run_rpc_step)))
    guard_shapes = []
    for node in ast.walk(guard_tree):
        if not (isinstance(node, ast.If)
                and isinstance(node.test, ast.BoolOp)
                and isinstance(node.test.op, ast.And)
                and len(node.test.values) == 2):
            continue
        no_error, declared = node.test.values
        if (isinstance(no_error, ast.Compare)
                and len(no_error.ops) == 1
                and isinstance(no_error.ops[0], ast.NotIn)
                and isinstance(no_error.left, ast.Constant)
                and no_error.left.value == "error"
                and isinstance(no_error.comparators[0], ast.Name)
                and no_error.comparators[0].id == "response"
                and isinstance(declared, ast.Compare)
                and len(declared.ops) == 1
                and isinstance(declared.ops[0], ast.IsNot)
                and isinstance(declared.left, ast.Call)
                and isinstance(declared.left.func, ast.Attribute)
                and declared.left.func.attr == "get"
                and isinstance(declared.left.func.value, ast.Name)
                and declared.left.func.value.id == "step"
                and len(declared.left.args) == 1
                and isinstance(declared.left.args[0], ast.Constant)
                and declared.left.args[0].value == "expect_error"
                and isinstance(declared.comparators[0], ast.Constant)
                and declared.comparators[0].value is None):
            guard_shapes.append(node)
    if len(guard_shapes) != 1:
        raise AssertionError(
            "expect_error guard shape pin: run_rpc_step must contain "
            "exactly one unqualified two-operand guard "
            "(`\"error\" not in response and "
            "step.get(\"expect_error\") is not None`); found "
            f"{len(guard_shapes)} — a narrowed or qualified guard "
            "launderes every negative step outside its qualification")
    if not (guard_shapes[0].body and
            isinstance(guard_shapes[0].body[0], ast.Raise)):
        raise AssertionError(
            "expect_error guard shape pin: the guard's body must raise "
            "immediately, not record and continue")
    # Position arms (tester round-11 F-D, F-E): shape and count do not
    # say where the guard executes.  F-D taught that the guard must
    # follow the service.call response assignment immediately; F-E
    # taught that the walk-based scan accepted a dead canonical pair
    # nested inside `if False:` (dead-in-position) and stayed silent on
    # pre-call suppression (a code-pinned return or an expect_error
    # erase placed BEFORE the call assignment).  Three fixes:
    # (1) the call-and-guard adjacency is pinned in the function's own
    #     top-level statement list only, so a pair nested anywhere
    #     (including a dead wrapper) can never satisfy position;
    # (2) no top-level statement before the call may reference
    #     expect_error in any syntactic role or mutate/rebind `step`
    #     (pop/clear/update/setdefault, subscript write, del, or
    #     reassignment), which is the pre-call suppression channel;
    # (3) The floor that remains after (1) and (2), stated rather than
    #     hidden: a pre-call exit or erase keyed on values OUTSIDE the
    #     behavioral control's sample (the control exercises method
    #     reader.close, actor consumer, and codes input_format /
    #     invalid_argument; an exit keyed on anything else, or a check
    #     delegated to a helper so no expect_error token appears in
    #     this function) is not visible to a single-function static
    #     pin.  An exit that does trigger on the control's own
    #     reader.close/consumer/sampled-code combination dies on the
    #     behavioral pair, so the
    #     pin and the pair close the class from opposite sides and only
    #     the off-sample residue survives; closing that would need
    #     whole-module taint analysis, which this kit deliberately
    #     prices out -- adversarial reviewer rounds are the control for
    #     edits that exotic.
    # The decoy floor, stated to the reach arm's proof boundary: an
    # exact-shape decoy at the pinned top-level position EXECUTES
    # unless something before it diverts -- executing, its immediate
    # unconditional Raise on exactly the guard's condition makes it the
    # working guard; diverting, the divert is either a dead-maker the
    # reach arm rejects or an off-sample/undecidable test inside the
    # declared static boundary.  A nested dead decoy fails (1) directly.
    func_def = (guard_tree.body[0] if guard_tree.body
                and isinstance(guard_tree.body[0], ast.FunctionDef)
                else None)
    if func_def is None:
        raise AssertionError(
            "expect_error guard shape pin: run_rpc_step source must "
            "parse to a single function definition")
    top = func_def.body
    call_index = None
    for index, stmt in enumerate(top):
        if (isinstance(stmt, ast.Assign)
                and len(stmt.targets) == 1
                and isinstance(stmt.targets[0], ast.Name)
                and stmt.targets[0].id == "response"
                and isinstance(stmt.value, ast.Call)
                and isinstance(stmt.value.func, ast.Attribute)
                and stmt.value.func.attr == "call"
                and isinstance(stmt.value.func.value, ast.Name)
                and stmt.value.func.value.id == "service"):
            call_index = index
            break
    if (call_index is None or call_index + 1 >= len(top)
            or top[call_index + 1] is not guard_shapes[0]):
        raise AssertionError(
            "expect_error guard shape pin: the canonical guard must be "
            "the statement immediately after the service.call response "
            "assignment in the function's top-level statement list; a "
            "guard reached only after earlier control flow, or a pair "
            "nested inside any wrapper, launders the flow it skips")
    for stmt in top[:call_index]:
        for node in ast.walk(stmt):
            references = ((isinstance(node, ast.Constant)
                           and node.value == "expect_error")
                          or (isinstance(node, ast.keyword)
                              and node.arg == "expect_error")
                          or (isinstance(node, ast.Attribute)
                              and node.attr == "expect_error"))
            mutates = False
            if isinstance(node, (ast.Assign, ast.AugAssign, ast.AnnAssign)):
                targets = (node.targets if isinstance(node, ast.Assign)
                           else [node.target])
                for target in targets:
                    if (isinstance(target, ast.Name) and target.id == "step") or (
                            isinstance(target, ast.Subscript)
                            and isinstance(target.value, ast.Name)
                            and target.value.id == "step"):
                        mutates = True
            elif isinstance(node, ast.Delete):
                mutates = any(isinstance(t, ast.Subscript)
                              and isinstance(t.value, ast.Name)
                              and t.value.id == "step"
                              for t in node.targets)
            elif (isinstance(node, ast.Call)
                  and isinstance(node.func, ast.Attribute)
                  and isinstance(node.func.value, ast.Name)
                  and node.func.value.id == "step"
                  and node.func.attr in ("pop", "clear", "update",
                                         "setdefault")):
                mutates = True
            if references or mutates:
                raise AssertionError(
                    "expect_error guard shape pin: a top-level statement "
                    "before the service.call response assignment "
                    "references expect_error or mutates step -- pre-call "
                    "suppression launders every negative step by never "
                    "reaching the guard")
    # Reach arm (tester round-11 F-F, extended for F-G): position pins
    # where the pair sits, but a top-level divert BEFORE the call makes
    # everything after it dead -- including a fully canonical,
    # top-level, adjacent call+guard decoy pair, which then satisfies
    # the shape, position, body, and count arms while the live path
    # hides ahead of the divert behind a dynamic-key narrowed guard (no
    # expect_error token, so purity never sees it).  The arm therefore
    # classifies every statement before the call and rejects any that
    # provably diverts control away: a bare Return/Raise; an If whose
    # provable truth value (folded through BoolOp/Not/Eq-Compare, so
    # `if True and True:`, `if not False:`, `if 1 == 1:` count) selects
    # a diverting branch; a While/For whose certain first iteration
    # diverts (while-true, for-over-nonempty-constant); a Match whose
    # unguarded wildcard case diverts; a Try whose first statement is
    # an exit (a bare return/raise cannot itself fail).  The shipped
    # pre-call flow has none of these (its returns live inside the
    # non-constant notification arm and the known-method raise inside a
    # non-provable if), so pristine passes.
    # Proof boundary, stated as the arm's actual reach (F-G): tests
    # whose truth cannot be folded (Name, Call, non-Eq Compare, and
    # compound tests mixing them), Try diverts below the first
    # statement, Match cases with a guard or a pattern narrower than
    # `_`, and any helper-delegated check (no expect_error token in
    # this function) are invisible to this static scan -- that residue
    # is the same declared off-sample class as the (3) floor, and the
    # adversarial review rounds are its control.
    _MATCH = getattr(ast, "Match", None)
    _MATCH_AS = getattr(ast, "MatchAs", None)
    _TRY_TYPES = (ast.Try,) + tuple(
        t for t in (getattr(ast, "TryStar", None),) if t is not None)

    def _const_bool(node):
        # (True, value) when a control-flow test provably folds to a
        # truth value; (False, None) otherwise.
        if isinstance(node, ast.Constant):
            return True, bool(node.value)
        if isinstance(node, ast.BoolOp):
            folded = [_const_bool(value) for value in node.values]
            if all(known for known, _ in folded):
                values = [value for _, value in folded]
                return True, (all(values) if isinstance(node.op, ast.And)
                              else any(values))
            return False, None
        if isinstance(node, ast.UnaryOp) and isinstance(node.op, ast.Not):
            known, value = _const_bool(node.operand)
            return (True, not value) if known else (False, None)
        if (isinstance(node, ast.Compare) and len(node.ops) == 1
                and isinstance(node.ops[0], ast.Eq)):
            left_k, left_v = _const_bool(node.left)
            right_k, right_v = _const_bool(node.comparators[0])
            return (True, left_v == right_v) if left_k and right_k \
                else (False, None)
        if isinstance(node, (ast.Tuple, ast.List)):
            return True, bool(node.elts)
        return False, None

    def _dead_maker(statements):
        # A statement list diverts control away unconditionally when it
        # holds a bare Return/Raise, a provable-test If whose taken
        # branch is itself a dead-maker, a certain-first-iteration
        # While/For with a diverting body, an always-matching unguarded
        # wildcard case with a diverting body, or a Try whose first
        # statement is an exit.
        for child in statements:
            if isinstance(child, (ast.Return, ast.Raise)):
                return True
            if isinstance(child, ast.If):
                known, value = _const_bool(child.test)
                if known and _dead_maker(
                        child.body if value else (child.orelse or [])):
                    return True
            elif isinstance(child, ast.While):
                known, value = _const_bool(child.test)
                if known and value and _dead_maker(child.body):
                    return True
            elif isinstance(child, ast.For):
                known, value = _const_bool(child.iter)
                if known and value and _dead_maker(child.body):
                    return True
            elif _MATCH is not None and isinstance(child, _MATCH):
                for case in child.cases:
                    if (case.guard is None
                            and _MATCH_AS is not None
                            and isinstance(case.pattern, _MATCH_AS)
                            and case.pattern.name is None
                            and case.pattern.pattern is None
                            and _dead_maker(case.body)):
                        return True
            elif isinstance(child, _TRY_TYPES):
                if (child.body and isinstance(child.body[0],
                                              (ast.Return, ast.Raise))):
                    return True
        return False

    for stmt in top[:call_index]:
        if isinstance(stmt, (ast.Return, ast.Raise)):
            raise AssertionError(
                "expect_error guard shape pin: a top-level bare "
                "return/raise before the service.call response "
                "assignment makes the pinned pair dead code; a dead "
                "canonical decoy passes every other arm and launders "
                "the live path hidden ahead of it")
        if _dead_maker([stmt]):
            raise AssertionError(
                "expect_error guard shape pin: a statement before the "
                "service.call response assignment provably diverts "
                "control away unconditionally; the pinned pair is dead "
                "code and a dead canonical decoy launders the live "
                "path hidden ahead of it")

    # Committed-report provenance.  These run here instead of behind a
    # ``--self-test`` flag the battery could omit, because the runner's helper
    # pins already run on every invocation: a matrix run that could not commit
    # its own report honestly would then be visible at once rather than at the
    # next audit.
    run_shared_self_test("run")
    audit = audit_report_writers(
        cli_dir=os.path.dirname(os.path.abspath(__file__)),
        writers=["run.py"], artifacts=False)
    if audit:
        raise AssertionError(
            "run.py does not commit through the shared report writer: "
            + "; ".join(audit))


CAPABILITIES_CACHE = {}


def describe_capabilities(binary):
    """Validate and cache one binary's system.describe capability result."""

    identity = os.path.realpath(binary)
    cache_key = (identity, sha256_file(identity))
    if cache_key in CAPABILITIES_CACHE:
        return CAPABILITIES_CACHE[cache_key]
    # ``probe`` records how the handshake ended, because two different facts
    # arrive as "no capability": a released legacy CLI executable that exits
    # normally without speaking JSON-RPC, and an engine that died while being
    # asked.  Only the first one may skip cases; the second is a dead product
    # and has to fail the run.  ``crashed`` is true when the service was killed
    # by a signal, which no argument-error path ever produces.
    record = {"path": binary, "sha256": cache_key[1], "methods": [],
              "available": False,
              "probe": {"returncode": None, "crashed": False, "reason": None}}
    service = None
    try:
        try:
            service = JsonRpcService(
                [binary, "--jsonrpc"], "probe",
                read_deadline=PROBE_IO_DEADLINE_SECONDS,
                write_deadline=PROBE_IO_DEADLINE_SECONDS)
            try:
                response = service.call(
                    "capability-probe", "iprange.v1.system.describe", {})
            finally:
                service.close()
            if "result" in response:
                results.validate_result(
                    "iprange.v1.system.describe", response["result"])
                record["methods"] = list(response["result"]["methods"])
                record["available"] = True
                record["result"] = response["result"]
        except (AssertionError, OSError) as exc:
            if "force-terminated" in str(exc):
                # A probe service that ran and then had to be
                # force-terminated by close() is not a legacy-only signal:
                # it is a stalled JSON-RPC service and must fail loudly
                # instead of being misclassified (role-round finding).
                raise
            record["methods"] = []
            record["available"] = False
            status = service.proc.returncode if service is not None else None
            record["probe"] = {
                "returncode": status,
                "crashed": status is not None and status < 0,
                "reason": str(exc)[:300]}
        # Legacy-only executables do not expose --jsonrpc.  A returned describe
        # result that fails the strict schema propagates ValidationError.
    finally:
        if service is not None and record["available"]:
            status = service.proc.returncode
            record["probe"] = {
                "returncode": status,
                "crashed": status is not None and status < 0,
                "reason": None}
    CAPABILITIES_CACHE[cache_key] = record
    return record


def load_cases(case_dir):
    cases = []
    names = set()
    for name in sorted(os.listdir(case_dir)):
        if not name.endswith(".json"):
            continue
        with open(os.path.join(case_dir, name), encoding="utf-8") as stream:
            case = json.load(stream)
        try:
            case_schema.validate_case(case)
        except ValidationError as exc:
            raise ValueError(f"{name}: {exc}") from exc
        if case["name"] in names:
            raise ValueError(f"duplicate case name {case['name']!r}")
        names.add(case["name"])
        cases.append(case)
    return cases


def runner_caller_paths(args):
    """The runner's path-valued options, in the shape the shared defences take.

    One tuple decides the input-side refusal, the ``privacy.checked_inputs``
    record, and what the committed-report audit requires this writer to keep
    screening; a label that dropped out of it is a gate failure rather than a
    silently unchecked input.  ``--c`` is included although the committed
    report's own registry entry names the other six: it is a path the report
    records, so it is screened like every other one.
    """
    return (("--c", args.c_binary),
            ("--go", args.go_binary),
            ("--rust", args.rust_binary),
            ("--fixture-tool", args.fixture_tool),
            ("--work-dir", args.work_dir),
            ("--cases", args.cases),
            ("--json-report", args.json_report))


def refuse_checkout_path(label, path):
    """Refuse an output location that could overwrite accepted evidence.

    Reports and work trees are staged, never committed: the qualification
    battery generates them into a scratch directory and rotates them into
    ``v4/cli/evidence/`` as a separate, deliberate step.  Accepting a path
    inside the checkout would let a routine run rewrite accepted evidence, and
    an omitted ``--json-report`` writes nothing at all, so there is no default
    that needs to be guarded -- only an explicit wrong choice.
    """

    root = recorded_checkout_root()
    if not root:
        return
    real = os.path.realpath(path)
    real_root = os.path.realpath(root)
    if real == real_root or real.startswith(real_root + os.sep):
        raise ValueError(
            f"{label} {path} is inside the repository checkout ({root}); "
            "write runner output to an explicit directory outside the tree "
            "(committed evidence is rotated in by the battery, never written "
            "by the runner)")


def validate_explicit_work_dir(path):
    if not os.path.isabs(path):
        raise ValueError("--work-dir must be absolute")
    real = os.path.realpath(path)
    if not os.path.isdir(real):
        raise ValueError("--work-dir must be an existing directory")
    if os.listdir(real):
        raise ValueError("--work-dir must be empty at the start of the run")
    return real


def case_work_dir(root, case, matrix):
    """Allocate one unique per-case directory under an explicit root.

    Fixture-producing cases would collide if every case wrote into the
    same root directory (NameExists on the second publication), so each
    case gets its own `<matrix>-<case>-<random>` subdirectory. The root
    itself is left untouched and is never deleted; the default
    temp-dir mode still allocates one fresh private directory per case.
    """

    def slug(text):
        cleaned = "".join(char if char.isalnum() else "-" for char in text)
        cleaned = "-".join(part for part in cleaned.split("-") if part)
        return cleaned.lower() or "case"

    matrix_slug = slug(matrix)
    case_slug = slug(case["name"])
    for _ in range(64):
        candidate = os.path.join(
            root, f"{matrix_slug}-{case_slug}-{uuid.uuid4().hex[:8]}")
        try:
            os.mkdir(candidate)
        except FileExistsError:
            continue
        return candidate
    raise ValueError(f"cannot allocate a unique case directory under {root}")


def binary_record(path):
    return {
        "path": path,
        "sha256": sha256_file(path),
    }


def merge_kind_ledger(target, ledger):
    """Merge one case's mechanical file-kind ledger into the report root."""

    for kind, counts in ledger.items():
        bucket = target.setdefault(kind, {"created_by": {}, "opened_by": {}})
        for side in ("created_by", "opened_by"):
            for method, count in counts[side].items():
                bucket[side][method] = bucket[side].get(method, 0) + count


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--c", dest="c_binary", metavar="PATH")
    parser.add_argument("--fixture-tool", metavar="PATH",
                        help="absolute v4-fixture producer executable")
    parser.add_argument("--rust", dest="rust_binary", metavar="PATH")
    parser.add_argument("--go", dest="go_binary", metavar="PATH")
    parser.add_argument("--matrix", default="all",
                        choices=["all", "c", "rust", "go", "rust_to_go", "go_to_rust"])
    parser.add_argument("--cases", default=DEFAULT_CASE_DIR)
    parser.add_argument("--work-dir", metavar="DIR",
                        help="absolute empty root directory; each case runs in a unique kept subdirectory")
    parser.add_argument("--filter", metavar="NAME")
    parser.add_argument("--allow-skips", action="store_true",
                        help="permit reported capability or unavailable-matrix skips")
    parser.add_argument("--json-report", metavar="PATH")
    args = parser.parse_args()

    try:
        if args.work_dir is not None:
            args.work_dir = validate_explicit_work_dir(args.work_dir)
        for label, path in (("work-dir", args.work_dir),
                           ("--json-report", args.json_report)):
            if path:
                refuse_checkout_path(label, path)
    except ValueError as exc:
        parser.error(str(exc))
    # Durable-artifact policy: committed evidence must never carry the
    # operator's home directory.  The shared check refuses the inputs first
    # (so nothing downstream can copy them into a report), and the
    # per-option messages below name the option the operator actually
    # passed, which is what a parser error is for.
    caller_paths = runner_caller_paths(args)
    require_paths_outside_profile(caller_paths)
    for label, path in (("c", args.c_binary),
                        ("rust", args.rust_binary),
                        ("go", args.go_binary)):
        if path and under_profile(path):
            parser.error(
                f"{label} binary {path} lives under the operator's "
                "profile; stage binaries under the authorized scratch "
                "area so committed evidence cannot carry personal paths")
    if args.fixture_tool and under_profile(args.fixture_tool):
        parser.error(
            f"fixture tool {args.fixture_tool} lives under the "
            "operator's profile; stage binaries under the authorized "
            "scratch area so committed evidence cannot carry personal "
            "paths")
    for path in (args.work_dir, args.json_report):
        if path and under_profile(path):
            parser.error(
                f"path {path} lives under the operator's profile; use "
                "the authorized scratch area so committed evidence "
                "cannot carry personal paths")
    # The explicit case corpus is recorded through the command array
    # (``--cases=PATH``); any spelling that resolves to the in-tree
    # default corpus stays usable (the relative ``v4/cli/cases``
    # invocation included).
    if (not same_path(args.cases, DEFAULT_CASE_DIR)
            and under_profile(args.cases)):
        parser.error(
            f"cases path {args.cases} lives under the operator's "
            "profile; stage corpora under the authorized scratch area "
            "so committed evidence cannot carry personal paths")

    def executable(value, label, *, require_absolute=True):
        if require_absolute and not os.path.isabs(value):
            parser.error(f"{label} is not an absolute executable file: {value}")
        if not os.path.isfile(value) or not os.access(value, os.X_OK):
            parser.error(f"{label} is not an absolute executable file: {value}")
        return os.path.realpath(value)

    fixture_tool = None
    if args.fixture_tool:
        fixture_tool = executable(args.fixture_tool, "fixture tool")

    binaries = {}
    for key, attr in (("c", "c_binary"), ("rust", "rust_binary"), ("go", "go_binary")):
        path = getattr(args, attr)
        if path:
            binaries[key] = executable(path, f"{key} binary")

    try:
        oracle._self_test()
        case_schema._self_test()
        _self_test()
        use_cases = load_cases(args.cases)
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        parser.error(str(exc))
    if args.filter:
        use_cases = [case for case in use_cases if args.filter in case["name"]]
    if not use_cases:
        parser.error("no cases selected")

    try:
        capabilities = {key: describe_capabilities(path) for key, path in binaries.items()}
    except ValidationError as exc:
        parser.error(f"invalid capability advertisement: {exc}")
    # ``command``, ``checkout_root``, ``git_head`` and the ``privacy`` block
    # are written by ``write_committed_report``: a report cannot record the
    # identity its producer prefers.
    report = {
        "schema": "iprange-cli-report-v3",
        "platform": {
            "system": platform_module.system(),
            "release": platform_module.release(),
            "machine": platform_module.machine(),
            "python": platform_module.python_version(),
        },
        "environment_allowlist": list(ENV_ALLOWLIST),
        "binaries": {key: dict(value) for key, value in capabilities.items()},
        "fixture_tool": binary_record(fixture_tool) if fixture_tool else None,
        "matrix": args.matrix,
        "cases": [],
        "passed": 0,
        "failed": 0,
        "skipped": 0,
        "oracle_checks": 0,
        # Additive evidence field: every product process that died outside the
        # runner's own teardown, plus every service that refused to end its
        # session cleanly.  A non-empty list fails the whole matrix run even
        # if the case that noticed it was later retried into a PASS, because
        # "the binary answered, then walked out" is not a qualification.
        "engine_deaths": [],
        # Additive evidence field: verdicts that belong to a whole matrix
        # direction rather than to a case.  They are deliberately NOT rows in
        # ``cases``: the kind-coverage gate requires every case row to name a
        # committed case under ``v4/cli/cases/`` ("an invented row is not
        # evidence") and requires the row count to equal the corpus size, so a
        # synthetic row would make the gate reject an honest report.  Keeping
        # them here also means the known-defects ledger, which is generated
        # from FAIL rows, can never park a dead engine as a known defect.
        "matrix_verdicts": [],
        # Additive evidence fields: mechanical file-kind ledger and
        # per-method peak wire-frame sizes (see README).
        "file_kinds": {},
        "frame_sizes": {},
    }

    def record_skip(name, matrix, reason):
        report["skipped"] += 1
        report["cases"].append({
            "name": name, "matrix": matrix, "status": "SKIP", "reason": reason,
        })
        print(f"SKIP {name} [{matrix}]: {reason}")

    def record_case_failure(case, matrix, label, reasons):
        """Record one failed case and count it.

        Every failure this runner can observe funnels through here, so the
        aggregate that decides the exit status can never drift away from the
        rows a reviewer reads: one FAIL row per failed case, one increment of
        ``failed`` per FAIL row.
        """

        report["failed"] += 1
        report["cases"].append({
            "name": case["name"], "matrix": matrix, "status": "FAIL",
            "error": "; ".join(reason for reason in reasons if reason),
        })
        print(f"FAIL {label}: " + "; ".join(reasons))

    def record_matrix_verdict(direction, kind, message):
        """Record a verdict against a whole matrix direction.

        A direction-level failure (a dead engine, a direction that executed
        nothing) has no case to attach to, so it is recorded apart from the
        case rows and fails the run on its own.  ``report["failed"]`` stays
        the count of failed cases so it keeps agreeing with the rows a
        reviewer reads.
        """

        report["matrix_verdicts"].append({
            "matrix": direction, "kind": kind, "error": message,
        })
        print(f"FAIL {direction}: {message}")

    def run_one(case, producer, consumer):
        """Run one case through real product services and record its result.

        Mixed matrices execute each rpc step on the service of its declared
        actor (producer or consumer) in separate service processes sharing
        the work directory; only filesystem paths may cross actors.  A case
        that cannot exercise both actors is skipped with its reason, so a
        mixed-direction PASS
        always means both binaries actually served.
        """

        consume_bin = binaries[consumer] if consumer else binaries[producer]
        matrix = producer if consumer is None else f"{producer}->{consumer}"
        mixed = consumer is not None
        needed = actor_requirements(case)
        if mixed and needed != set(ALL_ACTORS):
            missing = "producer" if "producer" not in needed else "consumer"
            record_skip(case["name"], matrix,
                        f"not cross-producer: case has no {missing} step")
            return "skip"
        owns_work = False
        if args.work_dir is None:
            work = tempfile.mkdtemp(prefix="iprange-cli-",
                                   dir=owned_temp_root())
            owns_work = True
        else:
            work = case_work_dir(args.work_dir, case, matrix)
        label = f"{case['name']} [{matrix}]"
        runner = None
        try:
            runner = CaseRunner(
                consume_bin, case, work, matrix, fixture_tool,
                producer_binary=binaries[producer] if mixed else None,
                consumer_binary=binaries[consumer] if mixed else None)
            try:
                runner.run()
            except (AssertionError, ValueError, ValidationError,
                    OSError) as exc:
                record_case_failure(case, matrix, label, [str(exc)])
                return "fail"
            except Exception as exc:  # noqa: BLE001 - recorded, never dropped
                # An unexpected runner-side exception is still one failed case:
                # it is recorded so the matrix keeps executing and the report
                # the gates read is still written, and the traceback is printed
                # so a runner bug cannot hide behind a red row.
                traceback.print_exc()
                record_case_failure(
                    case, matrix, label,
                    [f"runner raised {type(exc).__name__}: {exc}"])
                return "fail"
            # Liveness is judged while every peer this case started should
            # still be alive: after the last step and before teardown, when an
            # exit can only be the product's own.
            deaths = runner.owned_service_deaths()
            for death in deaths:
                report["engine_deaths"].append(
                    dict(death, case=case["name"], matrix=matrix,
                         phase="mid-case"))
            reasons = []
            if deaths:
                reasons.extend(
                    f"engine died mid-case: {death['argv']} "
                    f"(implementation {death['implementation']}, exit "
                    f"{death['exit_status']}, signal {death['signal']}, "
                    f"stderr tail {death['stderr_tail'][-1:]})"
                    for death in deaths)
            for problem in runner.close_services():
                reasons.append(
                    f"service teardown ({problem['actor']}): "
                    f"{problem['error']}")
                if problem["exit_status"] not in (None, 0):
                    report["engine_deaths"].append(
                        dict(problem, case=case["name"], matrix=matrix,
                             phase="teardown"))
            if reasons:
                record_case_failure(case, matrix, label, reasons)
                return "fail"
            entry = {
                "name": case["name"], "matrix": matrix, "status": "PASS",
                "oracle_checks": runner.oracle_checks,
                "params_rejected": list(runner.negative_responses),
                "digest_groups": {group: list(entries) for group, entries
                                  in sorted(runner.digest_groups.items())},
                # Per-case mechanical lineage: relative artifact path ->
                # kind and the acting "actor.method" lists, so the kind
                # universe can be verified case-by-case and the root
                # aggregate never loses producer/consumer identity.
                "file_kinds": {
                    os.path.relpath(path, work): {
                        "kind": facts["kind"],
                        "created_by": facts["created_by"],
                        "opened_by": facts["opened_by"],
                    }
                    for path, facts in sorted(runner.file_kinds_paths.items())},
            }
            if mixed:
                # Actor identity is pinned once at startup by
                # describe_capabilities (cache keyed on path+sha256); the
                # per-case entry reuses that hash instead of re-reading the
                # binaries from disk on every executed case.
                actor_keys = {"producer": producer, "consumer": consumer}
            else:
                # Single-language matrices run both roles on the one
                # selected executable, so both actors carry its record.
                actor_keys = {"producer": producer, "consumer": producer}
            # Every PASS case records the serving binary's own declared
            # implementation, so language attribution never depends on the
            # report-level matrix label (a relabeled clone cannot fake a
            # language it did not execute).  A binary that served a case
            # without a declared implementation fails the case.
            entry["actors"] = {}
            for actor, key in actor_keys.items():
                capability = capabilities[key]
                implementation = capability.get("result", {}).get("implementation")
                if implementation not in ("rust", "go"):
                    raise ValueError(
                        f"binary {key!r} served the case but declared no "
                        f"rust/go implementation")
                entry["actors"][actor] = {
                    "sha256": capability["sha256"],
                    "implementation": implementation,
                    # Canonical executed-binary path (realpath) for the
                    # binary that served this actor role, anchored at
                    # parse time; a relabeled clone cannot alias a
                    # different executable's evidence.
                    "argv": os.path.realpath(binaries[key]),
                    "steps": runner.actor_steps.get(actor, 0),
                    "operations": list(runner.actor_operations[actor]),
                }
            report["passed"] += 1
            report["oracle_checks"] += runner.oracle_checks
            report["cases"].append(entry)
            print(f"PASS {label} (oracle={runner.oracle_checks})")
            return "pass"
        finally:
            if runner is not None:
                merge_kind_ledger(report["file_kinds"], runner.file_kinds)
            if owns_work:
                shutil.rmtree(work, ignore_errors=True)

    matrix = {
        "c": (("c", None),),
        "rust": (("rust", None),),
        "go": (("go", None),),
        "rust_to_go": (("rust", "go"),),
        "go_to_rust": (("go", "rust"),),
        "all": (("c", None), ("rust", None), ("go", None),
                ("rust", "go"), ("go", "rust")),
    }[args.matrix]

    for producer, consumer in matrix:
        direction = producer if consumer is None else f"{producer}->{consumer}"
        missing = [key for key in (producer, consumer) if key is not None and key not in binaries]
        if missing:
            record_skip("matrix", direction, f"missing binary: {', '.join(missing)}")
            continue
        mixed = consumer is not None
        if mixed and fixture_tool is None:
            record_skip("matrix", direction, "mixed producer requires --fixture-tool")
            continue
        capability_key = consumer if consumer is not None else producer
        # A binary in a rust or go slot that cannot answer the capability
        # handshake is a dead engine, not a legacy-only executable: only the
        # released C ``iprange`` ever lacks a ``--jsonrpc`` surface.  Skipped
        # away, that condition used to end the direction with every case in
        # SKIP and (under ``--allow-skips``) an exit status of 0.
        dead = [key for key in (producer, consumer)
                if key is not None and not capabilities[key]["available"]
                and (key != "c"
                     or capabilities[key].get("probe", {}).get("crashed"))]
        if dead:
            detail = "; ".join(
                f"{key} binary {binaries[key]} is not a live engine "
                f"(probe exit "
                f"{capabilities[key].get('probe', {}).get('returncode')}, "
                f"crashed="
                f"{capabilities[key].get('probe', {}).get('crashed')}, reason="
                f"{capabilities[key].get('probe', {}).get('reason')!r})"
                for key in dead)
            for key in dead:
                probe = capabilities[key].get("probe", {})
                report["engine_deaths"].append({
                    "argv": [binaries[key], "--jsonrpc"],
                    "implementation": key,
                    "exit_status": probe.get("returncode"),
                    "signal": (-probe["returncode"]
                               if isinstance(probe.get("returncode"), int)
                               and probe["returncode"] < 0 else None),
                    "stderr_tail": [],
                    "case": None, "matrix": direction,
                    "phase": "capability-probe",
                })
            record_matrix_verdict(
                direction, "dead-engine",
                "matrix direction has a dead engine: " + detail)
            continue
        executed = 0
        for case in use_cases:
            if not mixed and not capabilities[capability_key]["available"]:
                # Legacy CLI-only binaries (C iprange) expose no --jsonrpc
                # surface; every JSON-RPC case is inapplicable and skips.
                # A broken binary in a mixed matrix must instead fail the
                # case (the /bin/false sensitivity), so this skip only
                # applies to single-language matrices.
                record_skip(case["name"], direction,
                            "binary has no jsonrpc capability")
                continue
            required = case.get("requires")
            if required:
                if mixed:
                    # Either declared actor may run the required method on
                    # its own binary, so both product binaries must
                    # advertise it.
                    keys = (producer, consumer)
                else:
                    keys = (capability_key,)
                keys = [key for key in keys if key is not None]
                # An unavailable actor binary must fail the case, not be
                # hidden by a capability skip (the /bin/false sensitivity);
                # only a binary that successfully advertises a method set
                # can prove the method is absent.
                if any(capabilities[key]["available"]
                       and required not in capabilities[key]["methods"]
                       for key in keys):
                    record_skip(case["name"], direction,
                                f"requires unadvertised method {required}")
                    continue
            executed += (run_one(case, producer, consumer) != "skip")
        if executed == 0 and not mixed and \
                capabilities[capability_key]["available"]:
            record_matrix_verdict(
                direction, "no-cases-executed",
                "matrix executed no case (every selected case skipped "
                "by a capability or requires rule)")
        if mixed and executed == 0:
            record_matrix_verdict(
                direction, "no-cases-executed",
                "matrix executed no cross-producer case "
                "(every case is single-actor or fixture-tool-produced)")

    report["frame_sizes"] = dict(FRAME_SIZES)

    print(
        f"\n{report['passed']} passed, {report['failed']} failed, "
        f"{report['skipped']} skipped; oracle checks={report['oracle_checks']}")
    if args.json_report:
        # The shared writer owns the provenance fields and applies the
        # durable-artifact policy net -- the personal-path scan over every
        # string value, run after the per-case fields are filled -- so a case
        # that recorded a profile-rooted path is refused rather than committed.
        write_committed_report(args.json_report, report,
                              caller_paths=caller_paths, indent=2)
    # Fail-closed: any recorded failure, and any product process that died or
    # refused to end its session outside the runner's teardown, fails the whole
    # matrix run.  The death check is deliberately independent of the failure
    # count so that a run whose rows were later rewritten still cannot exit 0.
    if report["failed"] or report["engine_deaths"] or report["matrix_verdicts"]:
        return 1
    if report["skipped"] and not args.allow_skips:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
