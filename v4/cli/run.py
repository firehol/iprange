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
import uuid

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

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
        self.services = {}          # actor -> JsonRpcService (mixed mode)
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
                write_text(path, source["text"])
                intervals = parse_interval_text(source["text"])
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

    def record_ledger(self, before, step):
        """Merge one executed step's inventory delta into the ledger.

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
        if method in LIVE_OPEN_METHODS:
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
        try:
            case_schema.validate_rpc_request(method, params)
        except ValidationError as exc:
            raise AssertionError(
                f"case {self.case['name']!r}: invalid request params: {exc}") from exc

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
                self.service = JsonRpcService(
                    [self.binary, "--jsonrpc"], self.implementation,
                    cwd=self.work_dir,
                    read_deadline=RUNNER_IO_DEADLINE_SECONDS,
                    write_deadline=RUNNER_IO_DEADLINE_SECONDS)
            return self.service
        service = self.services.get(actor)
        if service is None:
            service = JsonRpcService(
                [self.actor_binaries[actor], "--jsonrpc"],
                f"{self.implementation}:{actor}", cwd=self.work_dir,
                read_deadline=RUNNER_IO_DEADLINE_SECONDS,
                write_deadline=RUNNER_IO_DEADLINE_SECONDS)
            self.services[actor] = service
        return service

    def close_services(self):
        if self.service is not None:
            self.service.close()
            self.service = None
        for service in self.services.values():
            service.close()
        self.services = {}

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

    def close(self, allow_forced=False):
        """Close stdin and wait for this owned subprocess to terminate.

        Bounded teardown: a peer that does not exit after stdin EOF is
        reaped with a bounded kill.  A peer that had to be
        force-terminated BY THIS CALL is reported as a qualification
        failure (it did not finish its normal EOF shutdown) unless
        ``allow_forced`` is set (deliberate-stall controls).  Peers
        already terminated by the caller (crash scenarios' process
        groups) are reaped silently.

        In threaded mode (Windows deadlines), a peer poisoned by a
        bounded-I/O timeout is reaped before touching buffered
        wrappers whose locks a blocked worker may still hold; the
        worker threads are then joined under a bound (external review
        finding).
        """

        forced = False
        if self._use_threads and self._poisoned:
            # A timed-out writer may still hold the buffered stdin
            # lock, so closing the wrapper would block.  Reap the
            # child first: its death fails the blocked write (releasing
            # the lock) and ends a blocked read with EOF; then the
            # wrapped streams can be closed without waiting on a
            # worker whose bytes can no longer be correlated.
            forced = True
            self.proc.kill()
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
        # Close the buffered wrappers only after the peer is gone: a
        # still-blocked writer's write fails once the peer's pipe end
        # closes, releasing the lock.
        for stream in (self.proc.stdin, self.proc.stdout, self.proc.stderr):
            try:
                if stream is not None and not stream.closed:
                    stream.close()
            except Exception:
                pass
        if forced and not allow_forced:
            raise AssertionError(
                "service did not terminate cleanly at stdin EOF and had "
                f"to be force-terminated (returncode "
                f"{self.proc.returncode})")


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
    """Exercise runner-side protocol helpers without spawning a service."""

    import tempfile

    with tempfile.TemporaryDirectory() as work:
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


CAPABILITIES_CACHE = {}


def describe_capabilities(binary):
    """Validate and cache one binary's system.describe capability result."""

    identity = os.path.realpath(binary)
    cache_key = (identity, sha256_file(identity))
    if cache_key in CAPABILITIES_CACHE:
        return CAPABILITIES_CACHE[cache_key]
    record = {"path": binary, "sha256": cache_key[1], "methods": [], "available": False}
    try:
        service = JsonRpcService(
            [binary, "--jsonrpc"], "probe",
            read_deadline=PROBE_IO_DEADLINE_SECONDS,
            write_deadline=PROBE_IO_DEADLINE_SECONDS)
        try:
            response = service.call(
                "capability-probe", "iprange.v1.system.describe", {})
            if "result" in response:
                results.validate_result(
                    "iprange.v1.system.describe", response["result"])
                record["methods"] = list(response["result"]["methods"])
                record["available"] = True
                record["result"] = response["result"]
        finally:
            service.close()
    except (AssertionError, OSError):
        # Legacy-only executables do not expose --jsonrpc.  A returned describe
        # result that fails the strict schema propagates ValidationError.
        record["methods"] = []
        record["available"] = False
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
    except ValueError as exc:
        parser.error(str(exc))

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
    report = {
        "schema": "iprange-cli-report-v3",
        "command": sys.argv,
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
            work = tempfile.mkdtemp(prefix="iprange-cli-")
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
            runner.run()
            entry = {
                "name": case["name"], "matrix": matrix, "status": "PASS",
                "oracle_checks": runner.oracle_checks,
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
        except (AssertionError, ValueError, ValidationError, OSError) as exc:
            report["failed"] += 1
            report["cases"].append({
                "name": case["name"], "matrix": matrix, "status": "FAIL",
                "error": str(exc),
            })
            print(f"FAIL {label}: {exc}")
            return "fail"
        finally:
            if runner is not None:
                runner.close_services()
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
        if mixed and executed == 0:
            message = ("matrix executed no cross-producer case "
                       "(every case is single-actor or fixture-tool-produced)")
            report["failed"] += 1
            report["cases"].append({
                "name": "matrix", "matrix": direction, "status": "FAIL",
                "error": message,
            })
            print(f"FAIL {direction}: {message}")

    report["frame_sizes"] = dict(FRAME_SIZES)

    print(
        f"\n{report['passed']} passed, {report['failed']} failed, "
        f"{report['skipped']} skipped; oracle checks={report['oracle_checks']}")
    if args.json_report:
        with open(args.json_report, "w", encoding="utf-8") as stream:
            json.dump(report, stream, indent=2, sort_keys=True)
            stream.write("\n")
    if report["failed"]:
        return 1
    if report["skipped"] and not args.allow_skips:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
