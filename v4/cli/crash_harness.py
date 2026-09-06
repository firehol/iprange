#!/usr/bin/env python3
"""External process-crash harness for the iprange v1 JSON-RPC products.

Milestone-4 (delivery step 5) W5 gate: this script proves that the
product interface survives the producer process being killed mid-way
through a durable workflow, and that a fresh producer reports the
resulting state truthfully.

It drives only the normal JSON-RPC client (``run.JsonRpcService``), the
normal product executables (``--jsonrpc``), the documented resolution
methods, and the documented filesystem artifacts.  It adds no
production test method, hook, or environment variable.  Exact internal
crash-point coverage is owned by the SDK crash gates; this harness
covers the product interface.

Design (verified empirically against both product binaries; the exact
outcome values below are the observed wire values, one line per
scenario):

- Scenario A1 -- ``current.publish`` (fail_if_exists) killed at the
  reservation marker: the destination is absent or the exact complete
  reservation-recorded output; a fresh producer resolves with the
  retained reservation as the sole authority and the consumer reads
  the complete file.
- Scenario A2 -- ``current.publish`` (replace_existing) killed at the
  same marker: the destination is prior, absent, or the complete
  recorded output; ``inspect`` reconstructs the attempt and
  ``resolve`` completes it, then residue is bounded and absent.
- Scenario A3 -- negative control: a foreign destination planted
  after the kill classifies as ``foreign`` (the reservation is the
  sole authority; other bytes are never a completed publication).
- Scenario B -- ``database.initialize_live`` killed at the state-0
  sidecar marker: the main is unchanged, both ``database.info`` modes
  truthfully refuse (``wrong_state``), the resultless
  ``live_residue.resolve`` completes the transition and a live reader
  reopens on the resolved generation.
- Scenario C -- ``recover`` killed at the authorized-scratch marker:
  ``maintenance.list`` reports exactly the on-disk abandoned scratch,
  ``maintenance.remove`` returns the directory to empty, and the
  never-published destination still truthfully refuses.
- Scenario D -- ``direct.replace`` on a live database killed during
  uncommitted live-draft construction (a successful control run fixes
  the post-transition facts first: transaction advanced,
  ``range_record_count == line count``, and a live-reader lookup
  spot-check; the observable process-crash marker is main-file growth
  while the draft is being built, explicitly not a storage-sync
  durability claim; the plan's durable sidecar marker is impossible
  -- neither engine writes the ``.readers`` sidecar during a live
  commit, and exact commit/finish interruption is owned by the SDK
  fault gates, see scenario D): the interrupted draft never becomes
  the generation (``database.info`` live keeps the pre-transition
  T0/R0 and content probe, the control run's post-transition facts
  are absent, an immutable open truthfully refuses, zero maintenance
  residue, a consumer live reader sees the pre-transition
  generation) and a fresh producer commits T0+1 with one record.
- Scenario E -- ``export`` killed at the partial-output marker (real
  flushed output: the private temp stays 0 bytes until the 64 KiB
  export buffer flushes, and the kill waits for size > 0): the
  destination is absent, exactly one private ``.<handle>.export.tmp``
  orphan is the bounded residue (never a managed maintenance kind),
  the source is byte-identical, a retry lands byte-identical to the
  reference and removes its own private temp, and the consumer still
  opens the intact source.
- Scenario F -- ``validate`` killed at the findings-output flush
  marker during validation/findings delivery (a 1 500 000-range main
  is damaged by zeroing its last 1 400 derived range-tree leaf
  pages: >= 1000 findings, reference bytes strictly between one and
  two 64 KiB writer blocks, so the findings temp becomes non-empty
  exactly once mid-walk and the interrupted output is a strict
  prefix of the reference; temp existence alone is insufficient --
  both products buffer 64 KiB and flush after completion; the plan's
  worker-scratch marker is impossible -- neither engine's validate
  ever spills to authorized scratch, see scenario F): the findings
  destination is absent, one ``<id>.export.tmp`` orphan with
  strictly fewer bytes than the reference remains, the damaged main
  is byte-identical, a fresh validate reports the same findings and
  the committed generation still opens.

Every scenario runs in both directions (producer Rust with consumer Go,
producer Go with consumer Rust).  The report (schema
``iprange-cli-crash-report-v1``) records per-scenario marker timing,
kill method, destination state, inspect/resolve/reopen outcomes,
bounded-residue evidence, the additive per-role executed-operation
lists (``operations``) and open/creation facts that carry the
executing operation's ordinal (``live_reader_opens`` /
``adapter_output_opens`` / ``created_ordinals`` /
``reopen_outcome.consumer_main_open_ordinal``) that the kind gate's
lineage validation consumes, and the pass verdict.  Exit status is 0
only when every scenario passes and no owned process remains.
"""

import argparse
import hashlib
import json
import os
import platform
import shutil
import signal
import subprocess
import sys
import threading
import time
import uuid

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import run  # noqa: E402  (normal JSON-RPC client; import is side-effect free)
from schema import frame

# Durable artifact names (binary-format-v4.md sections 15, 15A, 20.1).
RESERVATION_PREFIX = ".iprange-reservation-"
PUBLISH_TEMP_PREFIX = ".iprange-publish-"
SCRATCH_PREFIX = ".iprange-scratch-"
PRIVATE_TMP_SUFFIX = ".tmp"
LIVE_SIDECAR_SUFFIX = ".readers"
RESERVATION_MAGIC = b"IPR4RSV1"
SIDECAR_MAGIC = b"IPRDRS4\x00"

# Marker polling: the reservation/sidecar file is the durable record
# only once its header block is on disk; the poll loop reads 8-16
# header bytes per iteration and kills within a fraction of a
# millisecond of the durable record appearing.  The reservation-to-
# destination window is a few milliseconds, so the loop is
# intentionally sleep-free.
POLL_DEADLINE_SECONDS = 30.0

# Feed size calibration (measured, both binaries): 200-300k IPv4 range
# lines finish in ~20-50 ms; 1,500,000 non-overlapping ranges make
# current.publish last ~340 ms (Rust) and ~830 ms (Go) on this
# workstation, giving the kill at the reservation marker a safe margin
# before the destination rename.
FEED_LINE_COUNT = 1_500_000
PRIOR_FEED_LINE_COUNT = 200_000

# Per-scenario feed sizes: A/B/C reuse FEED_LINE_COUNT; the live-replace
# crash (D) uses a 200 000-row direct CSV, the export crash (E) uses a
# 500 000-range text feed, and the validate crash (F) rebuilds the
# 1 500 000-range main so its range tree holds enough leaves for the
# deterministic damage (>= 1000 findings); each is calibrated so the
# observable marker window is wide enough on both product binaries.
EXPORT_FEED_LINE_COUNT = 500_000
DIRECT_FEED_LINE_COUNT = 200_000
F_DAMAGED_LEAF_PAGES = 1400
EXPORT_TEMP_SUFFIX = ".export.tmp"

# Every product process this harness spawns is recorded here so the
# final leftover check can match exactly our own PIDs (never a
# pkill-style pattern).
SPAWNED_PIDS = []

# Per-scenario service-call log (additive report field ``operations``):
# role -> ordered method names of every JSON-RPC call the scenario made
# on the producer/consumer binaries.  The harness spawns JSON-RPC
# services only on the two product binaries (the fixture tool is not a
# JSON-RPC service and the capability probe is not a scenario call), so
# ``argv[0]`` identifies the role; ``main()`` resets the log before
# every scenario.
OPERATIONS = {"producer": [], "consumer": []}

# Per-direction actor binaries, set by main() before each direction's
# scenarios; ``_service_role`` attributes every spawned service call.
_ACTOR_BINARIES = {"producer": None, "consumer": None}


def _service_role(argv):
    """Producer/consumer role of one spawned service argv.

    Returns None for a service the scenario did not spawn on a product
    binary (the capability probe), so the operation log only records
    scenario service calls.
    """

    binary = os.path.realpath(argv[0])
    for role in ("producer", "consumer"):
        if binary == _ACTOR_BINARIES[role]:
            return role
    return None


def _record_service_call(service, method):
    """Append one JSON-RPC method to its actor's operation log."""

    role = _service_role(service.argv)
    if role is not None:
        OPERATIONS[role].append(method)


def _current_ordinal(actor):
    """Index of the in-flight (or just-finished) service call of a role.

    ``_record_service_call`` appends the method name BEFORE the call
    executes, so while the request is being processed -- and right
    after it returns, before the same role issues another call -- the
    call's ordinal in the scenario's additive operation list is
    ``len(OPERATIONS[actor]) - 1``.
    """

    return len(OPERATIONS[actor]) - 1


def record_spawn(proc):
    """Record one owned product subprocess for the leftover check."""

    SPAWNED_PIDS.append(proc.pid)


def sha256_file(path):
    """Lowercase SHA-256 of one file (run.py parity)."""

    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def sha512_file(path):
    """Lowercase SHA-512 of one file (publication attempt digest unit)."""

    digest = hashlib.sha512()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def child_environment():
    """Documented subprocess environment allowlist (run.py parity)."""

    return run.child_environment()


def write_interval_feed(path, line_count, spacing=512, width=256):
    """Write a deterministic text feed of non-overlapping ranges.

    The text format matches the runner's text fixtures: one
    ``start-end`` range per line.  Ranges never overlap, so the
    normalized interval count equals the line count and the immutable
    builder performs its full sort/build pass (measured >= 200 ms at
    ``FEED_LINE_COUNT`` on both product binaries).
    """

    import ipaddress

    lines = []
    for index in range(line_count):
        start = index * spacing
        end = start + max(width - 1, 0)
        lines.append(
            f"{ipaddress.IPv4Address(start)}-{ipaddress.IPv4Address(end)}")
    with open(path, "w", encoding="utf-8", newline="") as stream:
        stream.write("\n".join(lines) + "\n")


class KillableJsonRpcService(run.JsonRpcService):
    """JsonRpcService whose subprocess runs in its own session.

    The producer that is meant to crash is spawned with
    ``start_new_session=True`` so the harness can terminate exactly
    the spawned process tree (never pkill/killall).  On POSIX the
    whole session receives SIGKILL; on Windows the spawned tree is
    terminated with a targeted ``taskkill /F /T`` so the windows
    housekeeping harness can interrupt a publish at the observable
    reservation marker.  Every response and the close path behave
    exactly like the normal client.
    """

    def __init__(self, argv, implementation, *, cwd=None):
        super().__init__(argv, implementation, cwd=cwd,
                         start_new_session=True)
        record_spawn(self.proc)

    def call(self, request_id, method, params):
        _record_service_call(self, method)
        return super().call(request_id, method, params)

    def kill_process_group(self):
        """Terminate exactly the process group this service spawned."""

        if os.name == "nt":
            # TerminateProcess first: it delivers immediately, while
            # taskkill's process launch latency would let a mid-build
            # publish finish its retirement.  taskkill is the fallback
            # when the product spawned child processes (worker).
            try:
                os.kill(self.proc.pid, signal.SIGTERM)
            except OSError:
                pass
            try:
                self.proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                subprocess.run(
                    ["taskkill", "/F", "/T", "/PID", str(self.proc.pid)],
                    capture_output=True, check=False)
                try:
                    self.proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    pass
            return
        try:
            os.killpg(os.getpgid(self.proc.pid), signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass


class HarnessJsonRpcService(run.JsonRpcService):
    """Plain JsonRpcService (no process-group session) with PID tracking."""

    def __init__(self, argv, implementation, *, cwd=None,
                 read_deadline=None, write_deadline=None):
        super().__init__(argv, implementation, cwd=cwd,
                         read_deadline=read_deadline,
                         write_deadline=write_deadline)
        record_spawn(self.proc)

    def call(self, request_id, method, params):
        _record_service_call(self, method)
        return super().call(request_id, method, params)


class ScenarioFailure(AssertionError):
    """One failed assertion inside a crash scenario."""


def call_with_worker(service, request_id, method, params, deadline, seen):
    """Issue one request and observe process-crash markers while it runs.

    ``service.call`` runs in a worker thread (the killed producer never
    answers).  ``seen`` is a poll callback with no arguments that
    returns True once the observable marker of the operation appears.
    Returns ``(outcome, seen_ms, thread)`` where ``outcome`` is the
    worker result dict or an error string, and ``seen_ms`` is the
    elapsed milliseconds when ``seen`` first reported True (or None).
    """

    outcome = {}
    worker_done = threading.Event()

    def worker():
        try:
            outcome["response"] = service.call(request_id, method, params)
        except Exception as exc:  # noqa: BLE001 - reported as evidence
            outcome["error"] = str(exc)
        finally:
            worker_done.set()

    thread = threading.Thread(target=worker, daemon=True)
    started = time.monotonic()
    thread.start()
    seen_ms = None
    while time.monotonic() - started < deadline:
        if seen():
            seen_ms = (time.monotonic() - started) * 1000
            break
        if worker_done.is_set():
            break
    return outcome, seen_ms, thread


def private_artifact_names(work_dir):
    """Map of private publication artifact basenames in one directory."""

    found = {"reservation": [], "publish_temp": []}
    if not os.path.isdir(work_dir):
        return found
    for name in os.listdir(work_dir):
        if name.startswith(RESERVATION_PREFIX) and name.endswith(PRIVATE_TMP_SUFFIX):
            found["reservation"].append(name)
        elif name.startswith(PUBLISH_TEMP_PREFIX) and name.endswith(PRIVATE_TMP_SUFFIX):
            found["publish_temp"].append(name)
    return found


def maintenance_reports(service, work_dir, out_path, kinds):
    """Run maintenance.list for one directory; return (reports, error)."""

    if os.path.exists(out_path):
        os.remove(out_path)
    response = service.call("m", "iprange.v1.maintenance.list", {
        "directory": work_dir,
        "kinds": kinds,
        "max_entries": 4096,
        "output": {"path": out_path, "format": "jsonl",
                   "publication_policy": "fail_if_exists",
                   "result_budget": {"max_rows": "4096",
                                     "max_output_bytes": "1048576",
                                     "max_open_files": 3}},
    })
    if "error" in response:
        return None, response["error"].get("data", {})
    rows = []
    if os.path.isfile(out_path):
        with open(out_path, "r", encoding="utf-8") as stream:
            for line in stream:
                line = line.strip()
                if line:
                    rows.append(json.loads(line))
    return response["result"].get("reports", []), None


def count_kind(reports, kind):
    """Decimal entries count of one kind from maintenance.list reports."""

    for report in reports or []:
        if report.get("kind") == kind:
            return int(report.get("entries", "0"))
    return None


def publish_params(feed, dest, policy):
    """current.publish params over one text feed (publication.json form)."""

    return {
        "input": {"paths": [feed], "family": "ipv4", "fix_network": True,
                  "default_prefix": 32, "dns": {"threads": 1, "silent": True},
                  "expand_at_paths": False, "max_line_bytes": 1024,
                  "max_expanded_paths": 4},
        "feed": "pubfeed", "value_tag": {"text": "pubtag"},
        "metadata": {"mode": "replace_utf8", "text": "published"},
        "destination": dest, "publication_policy": policy,
        "immutable_feed_budget": {"max_heap_bytes": "16777216",
                                  "max_output_pages": "20000",
                                  "max_workspace_pages": "20000",
                                  "max_open_files": 3},
    }


def write_direct_csv_feed(path, line_count):
    """Write a deterministic direct-CSV feed (from,to,value header).

    Ranges start at 10.0.0.0 with 64-address spacing.  ``from`` is the
    four octets of ``lo = base + index * 64`` and ``to`` is the four
    octets of ``lo + 63``: natural carry, monotonic, non-overlapping,
    never invalid, so the CSV never carries duplicate rows or octets
    above 255 (the generator property verified for scenario D up to
    ``DIRECT_FEED_LINE_COUNT``).  The per-line value is ``index + 1``:
    adjacent same-value ranges would be coalesced by both engines
    (binary-format-v4.md section 6.1), collapsing the whole feed into
    one record, so distinct values keep ``range_record_count ==
    line_count`` after a completed replace (the scenario D control
    run asserts exactly that).
    """

    with open(path, "w", encoding="utf-8", newline="") as stream:
        stream.write("from,to,value\n")
        base = 0x0A000000  # 10.0.0.0
        for index in range(line_count):
            lo = base + index * 64
            stream.write(
                f"{(lo >> 24) & 255}.{(lo >> 16) & 255}.{(lo >> 8) & 255}."
                f"{lo & 255},"
                f"{((lo + 63) >> 24) & 255}.{((lo + 63) >> 16) & 255}."
                f"{((lo + 63) >> 8) & 255}.{(lo + 63) & 255},"
                f"{index + 1}\n")


def direct_replace_params(path, feed):
    """direct.replace params replacing one live database from one CSV."""

    return {
        "path": path,
        "input": {"path": feed, "max_line_bytes": 1024},
        "metadata": {"mode": "keep"},
        "writer_budget": {"max_heap_bytes": "16777216",
                          "max_private_pages": "20000",
                          "max_growth_pages": "20000",
                          "max_open_files": 4},
    }


def export_params(source, dest):
    """iprange.v1.export params: full selection to one text file."""

    return {
        "source": {"path": source, "mode": "immutable"},
        "view": {"kind": "selection", "selection": {"mode": "all"}},
        "format": "ranges",
        "destination": dest, "publication_policy": "fail_if_exists",
        "result_budget": {"max_rows": "10000000",
                          "max_output_bytes": "10737418240",
                          "max_open_files": 32},
    }


def validate_params(path, findings_out):
    """iprange.v1.validate params with one jsonl findings output.

    The findings result budget is sized for scenario F's deterministic
    leaf damage (>= 1000 findings, reference bytes strictly between
    one and two 64 KiB export-writer blocks); scenario F is the only
    caller.
    """

    return {
        "path": path, "mode": {"kind": "immutable_current"},
        "validation_budget": {"max_heap_bytes": "16777216",
                              "max_open_files": 4,
                              "max_scratch_bytes": "0",
                              "max_scratch_files": 0},
        "findings_output": {"format": "jsonl", "path": findings_out,
                            "publication_policy": "fail_if_exists",
                            "result_budget": {"max_open_files": 3,
                                              "max_output_bytes": "33554432",
                                              "max_rows": "200000"}},
    }


def export_temp_basenames(work_dir):
    """Export/validate partial-output temp basenames (sorted)."""

    if not os.path.isdir(work_dir):
        return []
    return sorted(
        name for name in os.listdir(work_dir)
        if name.endswith(EXPORT_TEMP_SUFFIX))


def assert_one_export_orphan(orphans, scenario_report):
    """Assert the interrupted export left EXACTLY one private temp.

    The export contract leaves exactly one private
    ``.<handle>.export.tmp`` (O_EXCL basename: a leading dot, a
    non-empty attempt handle, then EXPORT_TEMP_SUFFIX) behind on an
    interrupted attempt (Go
    v4/go/internal/cli/fileio/export_writer.go:106; Rust
    v4/rust/iprange-cli/src/io/export_writer.rs:100).  Accepting any
    larger residue would hide foreign or recreated temporaries, so
    the check is exact: one file whose basename matches the private
    pattern.  Returns the orphan basename.
    """

    assert_truthful(
        len(orphans) == 1,
        f"exactly one export temp must remain as the bounded residue "
        f"(the interrupted attempt's private .<handle>.export.tmp), got "
        f"{orphans}", scenario_report)
    name = orphans[0]
    assert_truthful(
        name.startswith(".")
        and name.endswith(EXPORT_TEMP_SUFFIX)
        and len(name) > len(EXPORT_TEMP_SUFFIX) + 1,
        f"the orphan must be a private .<handle>.export.tmp basename "
        f"(leading dot, non-empty handle, {EXPORT_TEMP_SUFFIX!r} suffix), "
        f"got {name!r}", scenario_report)
    return name


def _orphan_contract_self_test():
    """In-memory negative control for the exactly-one export-temp bound.

    Mirrors scenario A3's negative-control pattern (a planted artifact
    must be rejected): injecting N>1 export temporaries, an empty set,
    or a non-private basename into ``assert_one_export_orphan`` must
    fail its assertions, and the genuine single-orphan shape must pass.
    Runs once at harness startup (no processes, no files) so an
    unlimited-residue regression fails the harness before any scenario
    runs, not after a heavy battery.
    """

    report = {"assertions": [], "failures": []}
    assert assert_one_export_orphan(
        [".deadbeef.export.tmp"], report) == ".deadbeef.export.tmp"
    assert not report["failures"]
    for injected in ([".a.export.tmp", ".b.export.tmp"], [],
                     [".no-suffix"], [".export.tmp"], ["export.tmp"],
                     ["a.export.tmp"]):
        report = {"assertions": [], "failures": []}
        try:
            assert_one_export_orphan(injected, report)
        except ScenarioFailure:
            continue
        raise AssertionError(
            f"negative control: residue {injected} must fail the "
            "exactly-one private export-temp assertion")


def decimal_u64(value):
    """Wire decimal u64 (string or number) as an int."""

    return int(value)


def classify_destination(dest, prior_sha256, prior_sha512, expected_sha512):
    """Classify a crash-left destination.

    Returns "absent" when the destination does not exist, "prior_complete"
    when it is byte-identical to the pre-crash file, "attempt_complete"
    when it carries the reservation-recorded output digest, and
    "foreign" for any other existing file - the reservation is the sole
    authority on what the interrupted attempt wrote, so a digest
    mismatch is a scenario failure, never a completed attempt.
    """

    if not os.path.isfile(dest):
        return "absent"
    if (sha256_file(dest) == prior_sha256
            and sha512_file(dest) == prior_sha512):
        return "prior_complete"
    if expected_sha512 is not None and sha512_file(dest) == expected_sha512:
        return "attempt_complete"
    return "foreign"


def assert_truthful(condition, message, scenario_report):
    """Record one failed assertion and raise ScenarioFailure."""

    if not condition:
        scenario_report["failures"].append(message)
        raise ScenarioFailure(message)
    scenario_report["assertions"].append(message)


def reservation_seen(work_dir, magic):
    """Poll callback: True when a reservation carries its magic header."""

    for name in private_artifact_names(work_dir)["reservation"]:
        path = os.path.join(work_dir, name)
        try:
            with open(path, "rb") as stream:
                if stream.read(8) == magic:
                    return True
        except OSError:
            pass
    return False


def reservation_output_sha512(work_dir):
    """Output SHA-512 recorded by the retained reservation (offset 160).

    binary-format-v4.md section 20.1: the reservation's block carries
    the SHA-512 of the exact attempted output bytes at offset 160 as
    64 raw bytes.  Returns the lowercase hex digest, or None when no
    valid reservation is readable.
    """

    for name in private_artifact_names(work_dir)["reservation"]:
        path = os.path.join(work_dir, name)
        try:
            with open(path, "rb") as stream:
                data = stream.read(256)
            if data[:8] != RESERVATION_MAGIC or len(data) < 224:
                return None
            # The reservation stores the output SHA-512 as 64 raw
            # bytes; expose it as lowercase hex for comparison.
            return data[160:224].hex()
        except OSError:
            return None


def probe_consumer_open(consumer, work, dest, scenario_report,
                        expect_refusal, require_open=False):
    """Design step 6: consumer reader.open on the crash-left destination.

    With an absent destination the open must truthfully refuse
    (``invalid_path``/``not_started``): a half-published file never
    opens.  With an existing complete destination the open succeeds and
    the reader is closed again immediately, because an open immutable
    reader holds the destination lifetime lock and would block the
    producer's resolver.  ``require_open`` makes success mandatory: a
    scenario (E/F source reopen) that documents "the consumer still
    opens the intact source" fails when the existing source refuses.
    """

    consumer_service = HarnessJsonRpcService(
        [consumer, "--jsonrpc"], f"consumer-{os.path.basename(work)}",
        cwd=work)
    try:
        reader_open = consumer_service.call(
            "ro", "iprange.v1.reader.open",
            {"source": {"path": dest, "mode": "immutable"}})
        error = reader_open.get("error", {}).get("data", {})
        if expect_refusal:
            assert_truthful(
                error.get("code") == "invalid_path"
                and error.get("outcome") == "not_started",
                f"absent destination must refuse reader.open, got "
                f"{reader_open}", scenario_report)
            scenario_report["reopen_outcome"] = {
                "before_resolution": {
                    "code": error.get("code"),
                    "outcome": error.get("outcome")},
            }
            return
        if error:
            assert_truthful(
                not require_open,
                f"an existing source must open, got {reader_open}",
                scenario_report)
            assert_truthful(
                error.get("code") == "invalid_path"
                and error.get("outcome") == "not_started",
                f"unexpected reader.open refusal: {reader_open}",
                scenario_report)
            scenario_report["reopen_outcome"] = {
                "before_resolution": {
                    "code": error.get("code"),
                    "outcome": error.get("outcome")},
            }
            return
        reader = reader_open["result"].get("reader")
        assert_truthful(
            reader is not None,
            f"reader.open must return a handle, got {reader_open}",
            scenario_report)
        # The successful open's operation ordinal is the v4_main
        # consumer open credit; captured before the close call so the
        # ordinal cannot drift to reader.close.
        main_open_ordinal = _current_ordinal("consumer")
        consumer_service.call("rc", "iprange.v1.reader.close",
                              {"reader": reader})
        scenario_report["reopen_outcome"] = {
            "before_resolution": {
                "opened_complete_destination": True,
                "reader_closed": True},
            "consumer_main_open_ordinal": main_open_ordinal,
        }
    finally:
        consumer_service.close()


def resolve_interrupted_publication(producer, consumer, work, dest,
                                    scenario_report, inspect_absent):
    """Fresh-producer resolution of one interrupted publication.

    Runs on a fresh producer service: maintenance.list bounded-residue
    evidence, ``publication.inspect`` (either the truthful refusal for
    an absent destination or the reconstruction from the retained
    reservation), ``publication.resolve`` complete (the reservation is
    the sole authority; the outcome must be the truthful completed
    publication), empty inspect and maintenance after resolution, and
    the consumer reopen of the complete published file.
    """

    resolver = HarnessJsonRpcService(
        [producer, "--jsonrpc"], f"resolver-{os.path.basename(work)}",
        cwd=work)
    try:
        reports, error = maintenance_reports(
            resolver, work, os.path.join(work, "pre.jsonl"),
            ["scratch", "reservation", "publication_temp"])
        assert_truthful(
            error is None and count_kind(reports, "reservation") == 1
            and count_kind(reports, "publication_temp") in (0, 1),
            f"pre-resolution residue must be exactly reservation=1 and "
            f"publication_temp=0..1, got reports={reports} error={error}",
            scenario_report)
        scenario_report["residue_bounded"] = {
            "maintenance_list_pre": reports}

        inspect = resolver.call(
            "2", "iprange.v1.publication.inspect", {"path": dest})
        if inspect_absent:
            error = inspect.get("error", {}).get("data", {})
            assert_truthful(
                error.get("code") == "invalid_path"
                and error.get("outcome") == "not_started",
                f"inspect of an absent destination must be truthful, got "
                f"{inspect}", scenario_report)
            scenario_report["inspect_outcome"] = {
                "code": error.get("code"), "outcome": error.get("outcome")}
        else:
            assert_truthful(
                "error" not in inspect,
                f"inspect must truthfully report the retained attempt, got "
                f"{inspect}", scenario_report)
            inspection = inspect["result"]["inspection"]
            assert_truthful(
                inspection.get("coordination") in ("absent",
                                                   "publication_reservation"),
                f"unexpected coordination class: {inspection}",
                scenario_report)
            reconstructed = inspection.get("publication") or {}
            assert_truthful(
                bool(reconstructed.get("attempt")),
                "inspect must reconstruct the attempt from the reservation",
                scenario_report)
            handle = inspection.get("handle")
            scenario_report["inspect_outcome"] = {
                "coordination": inspection.get("coordination"),
                "publication_reconstructed": bool(reconstructed),
                "handle_present": handle is not None,
            }
        if inspect_absent:
            handle = None
        else:
            handle = inspection.get("handle")

        resolved = resolver.call(
            "4", "iprange.v1.publication.resolve",
            {"path": dest, "resolution_mode": "complete"})
        if "error" in resolved:
            raise ScenarioFailure(f"resolve must complete, got {resolved}")
        publication = resolved["result"]["publication"]
        attempt = publication["attempt"]
        assert_truthful(
            publication.get("publication") == "published"
            and publication.get("destination_content") == "desired",
            f"resolve must report the truthful completed outcome, got "
            f"{publication.get('publication')}/"
            f"{publication.get('destination_content')}", scenario_report)
        assert_truthful(
            os.path.isfile(dest)
            and sha512_file(dest) == attempt.get("output_sha512"),
            "resolved destination must be the exact complete output",
            scenario_report)
        assert_truthful(
            private_artifact_names(work) == {"reservation": [],
                                             "publish_temp": []},
            "no private publication artifact may survive resolution",
            scenario_report)
        scenario_report["resolve_outcome"] = {
            "publication": publication.get("publication"),
            "destination_content": publication.get("destination_content"),
            "main_namespace_may_have_been_attempted": publication.get(
                "main_namespace_may_have_been_attempted"),
            "output_sha512": attempt.get("output_sha512"),
            "destination_sha512": sha512_file(dest),
        }

        if handle is not None:
            removed = resolver.call(
                "6", "iprange.v1.publication.residue.remove",
                {"handle": handle})
            assert_truthful(
                "error" not in removed,
                f"retained residue must remove, got {removed}",
                scenario_report)
        inspect2 = resolver.call(
            "5", "iprange.v1.publication.inspect", {"path": dest})
        assert_truthful(
            inspect2.get("result", {}).get("inspection", {}).get(
                "coordination") == "absent",
            f"inspect after resolution must be empty, got {inspect2}",
            scenario_report)
        reports2, error2 = maintenance_reports(
            resolver, work, os.path.join(work, "post.jsonl"),
            ["scratch", "reservation", "publication_temp"])
        assert_truthful(
            error2 is None and count_kind(reports2, "reservation") == 0
            and count_kind(reports2, "publication_temp") == 0,
            f"post-resolution residue must be empty, got reports="
            f"{reports2}", scenario_report)
        scenario_report["residue_bounded"]["maintenance_list_post"] = reports2

        consumer_service = HarnessJsonRpcService(
            [consumer, "--jsonrpc"], f"consumer2-{os.path.basename(work)}",
            cwd=work)
        try:
            reopened = consumer_service.call(
                "8", "iprange.v1.reader.open",
                {"source": {"path": dest, "mode": "immutable"}})
            assert_truthful(
                "error" not in reopened,
                f"complete destination must reopen, got {reopened}",
                scenario_report)
            info = reopened["result"].get("info", {})
            assert_truthful(
                info.get("database_id") == attempt.get("database_id"),
                "reopened file must carry the resolved attempt database id",
                scenario_report)
            scenario_report["reopen_outcome"]["after_resolution"] = {
                "database_id": info.get("database_id"),
                "transaction_id": info.get("transaction_id"),
            }
            # The consumer main-open credit names this successful
            # reopen's operation ordinal (the failed A1 probe open is
            # never credited).
            scenario_report["reopen_outcome"][
                "consumer_main_open_ordinal"] = _current_ordinal(
                    "consumer")
        finally:
            consumer_service.close()
    finally:
        resolver.close()



def scenario_a1(direction, producer, consumer, work_dir, scenario_report):
    """Crash current.publish (fail_if_exists) at the reservation marker."""

    work = os.path.join(work_dir, f"a1-{direction}-{uuid.uuid4().hex[:8]}")
    os.makedirs(work)
    feed = os.path.join(work, "feed.txt")
    write_interval_feed(feed, FEED_LINE_COUNT)
    dest = os.path.join(work, "published.iprange")
    params = publish_params(feed, dest, "fail_if_exists")

    producer_service = KillableJsonRpcService(
        [producer, "--jsonrpc"], f"producer-{direction}", cwd=work)
    try:
        outcome, seen_ms, thread = call_with_worker(
            producer_service, "1", "iprange.v1.current.publish", params,
            POLL_DEADLINE_SECONDS,
            seen=lambda: reservation_seen(work, RESERVATION_MAGIC))
        if seen_ms is None:
            raise ScenarioFailure(
                "reservation marker was not observed; "
                f"worker outcome={outcome}")
        scenario_report["marker_seen_ms"] = round(seen_ms, 1)
        producer_service.kill_process_group()
        thread.join(timeout=5)
        # The crashed publish created the retained reservation, the
        # private publication output, and the (attempted) main; its
        # operation ordinal is the creation credit for those kinds.
        _record_creation(scenario_report, (
            "publication_reservation", "publication_temp", "v4_main"))

        # No partial replacement; the documented private artifacts are
        # the bounded residue.
        residue = private_artifact_names(work)
        assert_truthful(
            len(residue["reservation"]) == 1,
            f"exactly one reservation must remain, got {residue['reservation']}",
            scenario_report)
        assert_truthful(
            len(residue["publish_temp"]) <= 1,
            f"at most one private publication output may remain, got "
            f"{residue['publish_temp']}", scenario_report)
        recorded_sha512 = reservation_output_sha512(work)
        assert_truthful(
            recorded_sha512 is not None,
            "the retained reservation must carry the output digest",
            scenario_report)
        if not os.path.exists(dest):
            dest_class = "absent_after_crash"
        elif sha512_file(dest) == recorded_sha512:
            dest_class = "attempt_complete_after_crash"
        else:
            dest_class = "partial_or_foreign"
        assert_truthful(
            dest_class != "partial_or_foreign",
            f"destination must be absent or the exact complete attempt "
            f"output (atomic namespace publication), got {dest_class}",
            scenario_report)
        scenario_report["destination_state"] = {
            "class": dest_class,
            "exists": os.path.isfile(dest),
            "recorded_output_sha512": recorded_sha512,
            "reservation_basename": residue["reservation"],
            "publish_temp_basenames": residue["publish_temp"],
        }

        # The consumer must never open a half-published file: an absent
        # destination truthfully refuses.
        probe_consumer_open(consumer, work, dest, scenario_report, True)

        # Fresh producer: bounded residue, truthful inspect, resolve
        # with the retained reservation as the sole authority, then
        # empty residue and a consumer reopen of the complete file.
        resolve_interrupted_publication(
            producer, consumer, work, dest, scenario_report, True)
    finally:
        producer_service.kill_process_group()
        producer_service.close()
    return work


def scenario_a2(direction, producer, consumer, work_dir, scenario_report):
    """Crash current.publish (replace_existing) over an existing main."""

    work = os.path.join(work_dir, f"a2-{direction}-{uuid.uuid4().hex[:8]}")
    os.makedirs(work)
    prior_feed = os.path.join(work, "prior.txt")
    write_interval_feed(prior_feed, PRIOR_FEED_LINE_COUNT)
    dest = os.path.join(work, "published.iprange")
    prior_params = publish_params(prior_feed, dest, "fail_if_exists")

    # Build the pre-crash destination with a completed publish.
    builder = HarnessJsonRpcService(
        [producer, "--jsonrpc"], f"builder-{direction}", cwd=work)
    try:
        built = builder.call("1", "iprange.v1.current.publish", prior_params)
        assert_truthful(
            "error" not in built,
            f"prior publish must succeed, got {built}", scenario_report)
    finally:
        builder.close()
    prior_sha256 = sha256_file(dest)
    prior_sha512 = sha512_file(dest)

    feed = os.path.join(work, "feed.txt")
    write_interval_feed(feed, FEED_LINE_COUNT)
    replace_params = publish_params(feed, dest, "replace_existing")

    producer_service = KillableJsonRpcService(
        [producer, "--jsonrpc"], f"producer-{direction}", cwd=work)
    try:
        outcome, seen_ms, thread = call_with_worker(
            producer_service, "9", "iprange.v1.current.publish",
            replace_params, POLL_DEADLINE_SECONDS,
            seen=lambda: reservation_seen(work, RESERVATION_MAGIC))
        if seen_ms is None:
            raise ScenarioFailure(
                "reservation marker was not observed; "
                f"worker outcome={outcome}")
        scenario_report["marker_seen_ms"] = round(seen_ms, 1)
        producer_service.kill_process_group()
        thread.join(timeout=5)
        # The crashed replace publish created the retained reservation,
        # the private publication output, and the (attempted) main;
        # its operation ordinal is the creation credit for those kinds
        # (the prior publish is a different, earlier operation).
        _record_creation(scenario_report, (
            "publication_reservation", "publication_temp", "v4_main"))

        residue = private_artifact_names(work)
        assert_truthful(
            len(residue["reservation"]) == 1,
            f"exactly one reservation must remain, got {residue['reservation']}",
            scenario_report)
        recorded_sha512 = reservation_output_sha512(work)
        assert_truthful(
            recorded_sha512 is not None,
            "the retained reservation must carry the output digest",
            scenario_report)
        dest_state = classify_destination(
            dest, prior_sha256, prior_sha512, recorded_sha512)
        assert_truthful(
            dest_state in ("absent", "prior_complete", "attempt_complete"),
            "destination must be absent, prior, or match the "
            f"reservation-recorded output digest, got {dest_state!r}",
            scenario_report)
        scenario_report["destination_state"] = {
            "class": dest_state,
            "exists": os.path.isfile(dest),
            "prior_sha256": prior_sha256,
            "prior_sha512": prior_sha512,
            "reservation_basename": residue["reservation"],
            "publish_temp_basenames": residue["publish_temp"],
        }

        # The consumer must never open a half-published file: an
        # existing complete destination (prior or attempt) opens
        # truthfully and is closed before the resolver runs.
        probe_consumer_open(consumer, work, dest, scenario_report, False)

        # Fresh producer: bounded residue, truthful inspect that
        # reconstructs the retained attempt, resolve to the complete
        # publication, then empty residue and consumer reopen.
        resolve_interrupted_publication(
            producer, consumer, work, dest, scenario_report, False)
    finally:
        producer_service.kill_process_group()
        producer_service.close()
    return work


SCRATCH_MAGIC = b"IPR4SCR1"


_CRC32C_POLY = 0x82F63B78
_CRC32C_TABLE = []
for _index in range(256):
    _value = _index
    for _ in range(8):
        _value = (_value >> 1) ^ _CRC32C_POLY if _value & 1 else _value >> 1
    _CRC32C_TABLE.append(_value)


def crc32c(data):
    """CRC-32C (reflected Castagnoli), the exact v4 checksum."""

    crc = 0xFFFFFFFF
    for byte in data:
        crc = (crc >> 8) ^ _CRC32C_TABLE[(crc ^ byte) & 0xFF]
    return crc ^ 0xFFFFFFFF


def scratch_header_authentic(path):
    """True when a scratch file carries its complete CRC-valid header.

    binary-format-v4.md section 14.4 (authorized scratch): the
    128-byte ownership header is complete only when its CRC-32C field (last 4 bytes, computed over
    the whole header with the field zeroed) validates.  An
    unauthenticated partial header can never be removed by the engine
    API, so the crash marker must wait for the complete authenticated
    header: a kill before it would leave a lookalike that truthfully
    refuses removal.
    """

    try:
        with open(path, "rb") as stream:
            head = stream.read(128)
    except OSError:
        return False
    if len(head) < 128 or head[:8] != SCRATCH_MAGIC:
        return False
    if int.from_bytes(head[8:10], "little") != 1:
        return False
    if int.from_bytes(head[10:12], "little") != 128:
        return False
    stored = int.from_bytes(head[124:128], "little")
    return crc32c(head[:124] + b"\x00" * 4) == stored


def scratch_attempt_seen(scratch_dir):
    """Poll callback: True for an authorized scratch file with a complete header.

    The file must carry its complete authenticated ownership header:
    a partial header would leave an unremovable lookalike after the
    kill, which is not the marker contract being tested.
    """

    if not os.path.isdir(scratch_dir):
        return False
    return any(
        name.startswith(SCRATCH_PREFIX)
        and scratch_header_authentic(os.path.join(scratch_dir, name))
        for name in os.listdir(scratch_dir))


def scratch_basenames(scratch_dir):
    """Authorized scratch basenames under one directory (sorted)."""

    if not os.path.isdir(scratch_dir):
        return []
    return sorted(
        name for name in os.listdir(scratch_dir)
        if name.startswith(SCRATCH_PREFIX)
        and name.endswith(PRIVATE_TMP_SUFFIX))


def scratch_attempt_id(basename):
    """The scratch-attempt ID embedded in one scratch basename."""

    body = basename[len(SCRATCH_PREFIX):-len(PRIVATE_TMP_SUFFIX)]
    return body.rsplit("-", 1)[0]


def recover_scratch_params(source, dest, scratch_dir, candidate):
    """recover params with authorized scratch enabled.

    The heap value is calibrated so the recovery page tables spill to
    authorized scratch (visible within the operation) while the fixed
    structures still fit.  The scenario fails if either product does
    not reach the observable scratch marker at this heap.
    """

    return {
        "source_mode": "immutable", "source_path": source,
        "candidate": candidate, "destination": dest,
        "recovery_budget": {"max_heap_bytes": "524288", "max_open_files": 4,
                            "max_output_pages": "20000",
                            "max_scratch_bytes": "536870912",
                            "max_scratch_files": 8,
                            "scratch_directory": scratch_dir},
        "report_output": {"format": "jsonl", "path": os.path.join(
            os.path.dirname(dest), "recovery.jsonl"),
            "publication_policy": "fail_if_exists",
            "result_budget": {"max_open_files": 3,
                              "max_output_bytes": "67108864",
                              "max_rows": "64"}},
    }


def sidecar_creating_state_seen(sidecar):
    """Poll callback: True when the sidecar has a valid creating header."""

    if not os.path.isfile(sidecar):
        return False
    try:
        with open(sidecar, "rb") as stream:
            head = stream.read(16)
        return (head[:8] == SIDECAR_MAGIC
                and int.from_bytes(head[12:16], "little") == 0)
    except OSError:
        return False


def scenario_a3(direction, producer, consumer, work_dir, fixture_tool,
                scenario_report):
    """Negative control: a foreign destination must classify as foreign.

    Scenario A2 classifies a crash-left destination as absent, prior,
    or the reservation-recorded attempt output.  This scenario poisons
    the destination with a valid-but-unrelated v4 file after the kill
    and proves the classifier returns ``foreign`` (the reservation is
    the sole authority on what the interrupted attempt wrote; any
    other bytes are a scenario failure, never a completed
    publication).
    """

    work = os.path.join(work_dir, f"a3-{direction}-{uuid.uuid4().hex[:8]}")
    os.makedirs(work)
    prior_feed = os.path.join(work, "prior.txt")
    write_interval_feed(prior_feed, PRIOR_FEED_LINE_COUNT)
    dest = os.path.join(work, "published.iprange")
    prior_params = publish_params(prior_feed, dest, "fail_if_exists")

    builder = HarnessJsonRpcService(
        [producer, "--jsonrpc"], f"builder-{direction}", cwd=work)
    try:
        built = builder.call("1", "iprange.v1.current.publish", prior_params)
        assert_truthful(
            "error" not in built,
            f"prior publish must succeed, got {built}", scenario_report)
    finally:
        builder.close()
    prior_sha256 = sha256_file(dest)
    prior_sha512 = sha512_file(dest)

    feed = os.path.join(work, "feed.txt")
    write_interval_feed(feed, FEED_LINE_COUNT)
    replace_params = publish_params(feed, dest, "replace_existing")

    producer_service = KillableJsonRpcService(
        [producer, "--jsonrpc"], f"producer-{direction}", cwd=work)
    try:
        outcome, seen_ms, thread = call_with_worker(
            producer_service, "9", "iprange.v1.current.publish",
            replace_params, POLL_DEADLINE_SECONDS,
            seen=lambda: reservation_seen(work, RESERVATION_MAGIC))
        if seen_ms is None:
            raise ScenarioFailure(
                "reservation marker was not observed; "
                f"worker outcome={outcome}")
        scenario_report["marker_seen_ms"] = round(seen_ms, 1)
        producer_service.kill_process_group()
        thread.join(timeout=5)
        # The crashed replace publish created the retained reservation,
        # the private publication output, and the (attempted) main;
        # its operation ordinal is the creation credit for those kinds.
        _record_creation(scenario_report, (
            "publication_reservation", "publication_temp", "v4_main"))
        residue = private_artifact_names(work)
        assert_truthful(
            len(residue["reservation"]) == 1,
            f"exactly one reservation must remain, got {residue['reservation']}",
            scenario_report)

        # Poison the destination with a valid-but-unrelated v4 file
        # (a never-published fixture database).  The classifier must
        # reject it: the reservation digest is the only authority.
        foreign = os.path.join(work, "foreign.iprange")
        made = subprocess.run(
            [fixture_tool, "direct-v4", foreign], capture_output=True,
            timeout=300, env=child_environment())
        if made.returncode != 0:
            detail = made.stderr.decode("utf-8", "replace").strip()
            raise ScenarioFailure(
                f"v4-fixture direct-v4 failed with exit {made.returncode}: "
                f"{detail}")
        shutil.copyfile(foreign, dest)
        dest_state = classify_destination(
            dest, prior_sha256, prior_sha512,
            reservation_output_sha512(work))
        assert_truthful(
            dest_state == "foreign",
            "a destination that is neither prior nor the reservation "
            f"output must classify foreign, got {dest_state!r}",
            scenario_report)
        scenario_report["destination_state"] = {
            "class": dest_state,
            "exists": os.path.isfile(dest),
            "reservation_basename": residue["reservation"],
            "publish_temp_basenames": residue["publish_temp"],
        }

        # The consumer treats the poisoned destination as its own
        # (invalid) content: the probe reads it through the real
        # consumer binary, which fails the /bin/false negative control.
        probe_consumer_open(consumer, work, dest, scenario_report, False)
    finally:
        producer_service.kill_process_group()
        producer_service.close()
    return work


def scenario_b(direction, producer, consumer, work_dir, fixture_tool,
               scenario_report):
    """Crash database.initialize_live at the state-0 sidecar marker."""

    work = os.path.join(work_dir, f"b-{direction}-{uuid.uuid4().hex[:8]}")
    os.makedirs(work)
    main = os.path.join(work, "direct.iprange")
    sidecar = main + LIVE_SIDECAR_SUFFIX
    fixture = subprocess.run(
        [fixture_tool, "direct-v4", main], capture_output=True, timeout=300,
        env=child_environment())
    if fixture.returncode != 0:
        detail = fixture.stderr.decode("utf-8", "replace").strip()
        raise ScenarioFailure(
            f"v4-fixture direct-v4 failed with exit {fixture.returncode}: "
            f"{detail}")
    main_sha256 = sha256_file(main)
    scenario_report["fixture_created_main"] = True

    producer_service = KillableJsonRpcService(
        [producer, "--jsonrpc"], f"producer-{direction}", cwd=work)
    try:
        outcome, seen_ms, thread = call_with_worker(
            producer_service, "9", "iprange.v1.database.initialize_live",
            {"path": main, "reader_capacity": 8}, POLL_DEADLINE_SECONDS,
            seen=lambda: sidecar_creating_state_seen(sidecar))
        if seen_ms is None:
            raise ScenarioFailure(
                "creating-state sidecar marker was not observed; "
                f"worker outcome={outcome}")
        scenario_report["marker_seen_ms"] = round(seen_ms, 1)
        producer_service.kill_process_group()
        thread.join(timeout=5)
        # The crashed initialize_live created the sidecar; its
        # operation ordinal is the live_sidecar creation credit (the
        # main itself came from the external v4-fixture tool).
        _record_creation(scenario_report, ("live_sidecar",))

        assert_truthful(
            sha256_file(main) == main_sha256,
            "the v4 main must be byte-identical after the kill",
            scenario_report)
        assert_truthful(
            os.path.isfile(sidecar),
            "the canonical sidecar must remain as the documented residue",
            scenario_report)
        scenario_report["destination_state"] = {
            "class": "main_unchanged_sidecar_present",
            "main_sha256": main_sha256,
            "sidecar_present": True,
            "sidecar_basename": os.path.basename(sidecar),
        }

        resolver = HarnessJsonRpcService(
            [producer, "--jsonrpc"], f"resolver-{direction}", cwd=work)
        try:
            # Both database.info modes truthfully refuse an interrupted
            # transition (no false success before resolution).
            for mode in ("live", "immutable"):
                info = resolver.call(
                    "i" + mode, "iprange.v1.database.info",
                    {"source": {"path": main, "mode": mode}})
                error = info.get("error", {}).get("data", {})
                assert_truthful(
                    error.get("code") == "wrong_state"
                    and error.get("outcome") == "read_only_failure",
                    f"database.info {mode} must truthfully refuse an "
                    f"interrupted transition, got {info}", scenario_report)
            scenario_report["inspect_outcome"] = {
                "database_info_live": {"code": "wrong_state",
                                       "outcome": "read_only_failure"},
                "database_info_immutable": {"code": "wrong_state",
                                            "outcome": "read_only_failure"},
            }

            # Bounded residue: the sidecar is the only artifact; the
            # Linux-listable maintenance kinds are all empty.
            reports, error = maintenance_reports(
                resolver, work, os.path.join(work, "pre.jsonl"),
                ["scratch", "reservation", "publication_temp"])
            assert_truthful(
                error is None and count_kind(reports, "scratch") == 0
                and count_kind(reports, "reservation") == 0
                and count_kind(reports, "publication_temp") == 0,
                f"no maintenance residue may exist beside the sidecar, got "
                f"reports={reports} error={error}", scenario_report)
            scenario_report["residue_bounded"] = {
                "sidecar_only": True,
                "maintenance_list_pre": reports,
            }

            # Resultless interrupted-transition resolution: complete
            # advances the state-0 sidecar to ready (the in-memory
            # transition result was lost with the killed process, so
            # database.live_transition.resolve cannot be called with
            # evidence; live_residue.resolve is the documented
            # resultless resolver).
            resolved = resolver.call(
                "4", "iprange.v1.database.live_residue.resolve",
                {"path": main, "resolution_mode": "complete"})
            if "error" in resolved:
                raise ScenarioFailure(
                    f"live_residue.resolve must complete, got {resolved}")
            residue = resolved["result"]
            assert_truthful(
                residue.get("status") == "completed"
                and residue.get("kind") == "canonical"
                and residue.get("residue_possible") is False,
                f"residue resolution must report the truthful completed "
                f"transition, got {residue}", scenario_report)
            scenario_report["resolve_outcome"] = {
                "status": residue.get("status"),
                "kind": residue.get("kind"),
                "residue_possible": residue.get("residue_possible"),
            }

            # Reopen on the consumer binary: the database is now live;
            # a live reader succeeds and an immutable open truthfully
            # refuses (sidecar present).  The producer's database.info
            # is the truth for the resolved generation; the consumer's
            # reader must observe exactly that generation.
            info = resolver.call(
                "5", "iprange.v1.database.info",
                {"source": {"path": main, "mode": "live"}})
            assert_truthful(
                "error" not in info,
                f"database.info live must succeed after resolution, got "
                f"{info}", scenario_report)
            # The resolver's live database.info opened a live reader:
            # the producer-side sidecar reader-table open (recorded for
            # the live_sidecar opened lineage).
            _record_live_open(scenario_report, "producer")
            info_facts = info["result"].get("info", {})
            consumer_service2 = HarnessJsonRpcService(
                [consumer, "--jsonrpc"], f"consumer2-{direction}", cwd=work)
            try:
                open_live = consumer_service2.call(
                    "6", "iprange.v1.reader.open",
                    {"source": {"path": main, "mode": "live"}})
                assert_truthful(
                    "error" not in open_live,
                    f"live reader must reopen after resolution, got "
                    f"{open_live}", scenario_report)
                _record_live_open(scenario_report, "consumer")
                main_open_ordinal = _current_ordinal("consumer")
                reopen_info = open_live["result"].get("info", {})
                assert_truthful(
                    reopen_info.get("database_id") == info_facts.get(
                        "database_id")
                    and reopen_info.get("transaction_id") == info_facts.get(
                        "transaction_id"),
                    "consumer live reader must match the resolved "
                    "generation", scenario_report)
                open_immutable = consumer_service2.call(
                    "7", "iprange.v1.reader.open",
                    {"source": {"path": main, "mode": "immutable"}})
                immutable_error = open_immutable.get("error", {}).get(
                    "data", {})
                assert_truthful(
                    immutable_error.get("code") == "wrong_state"
                    and immutable_error.get("outcome") == "read_only_failure",
                    "immutable open must truthfully refuse a live database",
                    scenario_report)
            finally:
                consumer_service2.close()
            scenario_report["reopen_outcome"] = {
                "live": {
                    "database_id": reopen_info.get("database_id"),
                    "transaction_id": reopen_info.get("transaction_id")},
                "immutable": {
                    "code": immutable_error.get("code"),
                    "outcome": immutable_error.get("outcome")},
                "consumer_main_open_ordinal": main_open_ordinal,
            }
        finally:
            resolver.close()
    finally:
        producer_service.kill_process_group()
        producer_service.close()
    return work



def scenario_c(direction, producer, consumer, work_dir, scenario_report):
    """Crash recover at the authorized-scratch marker.

    Scenario A1/A2 prove interruption of the destination publication;
    this scenario proves the other authorized external artifact:
    recovery graph-safety scratch.  A recovery of a damaged large
    database with a constrained heap spills its page tables to
    authorized ``.iprange-scratch-<attempt>-<ordinal>.tmp`` files;
    the harness kills the producer while that observable marker is live.
    A fresh producer lists the abandoned scratch through
    ``maintenance.list`` (where the attempt ID is the authority),
    removes it, and the consumer still truthfully refuses to open the
    never-published destination.
    """

    work = os.path.join(work_dir, f"c-{direction}-{uuid.uuid4().hex[:8]}")
    os.makedirs(work)
    scratch_dir = os.path.join(work, "scratch")
    os.makedirs(scratch_dir)
    feed = os.path.join(work, "feed.txt")
    write_interval_feed(feed, FEED_LINE_COUNT)
    source = os.path.join(work, "big.iprange")
    params = publish_params(feed, source, "fail_if_exists")

    builder = HarnessJsonRpcService(
        [producer, "--jsonrpc"], f"builder-{direction}", cwd=work)
    try:
        built = builder.call("1", "iprange.v1.current.publish", params)
        assert_truthful(
            "error" not in built,
            f"big publish must succeed, got {built}", scenario_report)
        # The completed big publish created the damaged-main source;
        # its operation ordinal is the v4_main creation credit.
        _record_creation(scenario_report, ("v4_main",))
    finally:
        builder.close()
    # Damage the immutable main by cutting its final page; recovery is
    # the only way to salvage the committed generation.
    with open(source, "r+b") as stream:
        stream.truncate(os.path.getsize(source) - 4096)

    producer_service = KillableJsonRpcService(
        [producer, "--jsonrpc"], f"producer-{direction}", cwd=work)
    try:
        inspect = producer_service.call(
            "2", "iprange.v1.recovery.inspect",
            {"mode": "immutable", "path": source,
             "validation_budget": {"max_heap_bytes": "16777216",
                                   "max_open_files": 4,
                                   "max_scratch_bytes": "0",
                                   "max_scratch_files": 0}})
        assert_truthful(
            "error" not in inspect,
            f"recovery.inspect must succeed, got {inspect}",
            scenario_report)
        candidates = inspect["result"]["candidates"]
        assert_truthful(
            len(candidates) == 1,
            f"exactly one recovery candidate expected, got {len(candidates)}",
            scenario_report)
        dest = os.path.join(work, "recovered.iprange")
        outcome, seen_ms, thread = call_with_worker(
            producer_service, "3", "iprange.v1.recover",
            recover_scratch_params(source, dest, scratch_dir,
                                   candidates[0]),
            POLL_DEADLINE_SECONDS,
            seen=lambda: scratch_attempt_seen(scratch_dir))
        if seen_ms is None:
            raise ScenarioFailure(
                "authorized-scratch marker was not observed; "
                f"worker outcome={outcome}")
        scenario_report["marker_seen_ms"] = round(seen_ms, 1)
        producer_service.kill_process_group()
        thread.join(timeout=5)
        # The crashed recover created the authorized scratch (and, if
        # the kill landed in its output phase, the recovery-output
        # reservation/publication temp); its operation ordinal is the
        # creation credit for those kinds.
        _record_creation(scenario_report, (
            "authorized_scratch", "publication_temp",
            "publication_reservation"))
        scenario_report["kill_method"] = (
            "SIGKILL process group at authorized-scratch marker")

        on_disk = scratch_basenames(scratch_dir)
        assert_truthful(
            1 <= len(on_disk) <= 8,
            f"abandoned scratch must be bounded by max_scratch_files, "
            f"got {len(on_disk)} files: {on_disk}", scenario_report)
        assert_truthful(
            not os.path.isfile(dest),
            "the recovery destination must never appear before the kill",
            scenario_report)
        residue = private_artifact_names(work)
        scenario_report["destination_state"] = {
            "class": "scratch_residue",
            "scratch_basenames": on_disk,
            "publish_temp_basenames": residue["publish_temp"],
            "reservation_basenames": residue["reservation"],
        }

        resolver = HarnessJsonRpcService(
            [producer, "--jsonrpc"], f"resolver-{direction}", cwd=work)
        try:
            # The fresh process lists exactly the on-disk abandoned
            # scratch; the attempt ID embedded in the basename is the
            # listed authority.
            list_path = os.path.join(work, "scratch-list.jsonl")
            reports, error = maintenance_reports(
                resolver, scratch_dir, list_path, ["scratch"])
            assert_truthful(
                error is None,
                f"maintenance.list scratch must succeed, got {error}",
                scenario_report)
            rows = []
            if os.path.isfile(list_path):
                with open(list_path, encoding="utf-8") as stream:
                    for line in stream:
                        line = line.strip()
                        if line:
                            rows.append(json.loads(line))
            listed_ids = sorted(row["attempt_id"] for row in rows)
            disk_ids = sorted(scratch_attempt_id(n) for n in on_disk)
            assert_truthful(
                count_kind(reports, "scratch") == len(on_disk)
                and listed_ids == disk_ids,
                "maintenance.list must report exactly the on-disk "
                f"abandoned scratch: reports={reports} rows={rows} "
                f"disk={on_disk}", scenario_report)
            scenario_report["residue_bounded"] = {
                "scratch_listed": len(rows),
                "attempt_ids": listed_ids,
                "reports": reports,
            }

            # Removal returns the directory to empty; a second list
            # proves durable absence.
            for row in rows:
                removed = resolver.call(
                    "4", "iprange.v1.maintenance.remove", {"entry": row})
                assert_truthful(
                    "error" not in removed,
                    f"maintenance.remove must succeed, got {removed}",
                    scenario_report)
            assert_truthful(
                scratch_basenames(scratch_dir) == [],
                "removal must delete every abandoned scratch file",
                scenario_report)

            # The recovery output reservation/temp residue (if the kill
            # landed after the output phase began) is bounded and also
            # listable through the normal maintenance kinds.
            work_residue = private_artifact_names(work)
            residue_reports, error = maintenance_reports(
                resolver, work, os.path.join(work, "residue-list.jsonl"),
                ["publication_temp", "reservation"])
            assert_truthful(
                error is None,
                f"maintenance.list residue must succeed, got {error}",
                scenario_report)
            assert_truthful(
                count_kind(residue_reports, "publication_temp")
                == len(work_residue["publish_temp"])
                and count_kind(residue_reports, "reservation")
                == len(work_residue["reservation"]),
                "maintenance residue counts must match the on-disk "
                f"residue: reports={residue_reports} disk={work_residue}",
                scenario_report)
            scenario_report["residue_bounded"]["publication_temp"] = (
                len(work_residue["publish_temp"]))
            scenario_report["residue_bounded"]["reservation"] = (
                len(work_residue["reservation"]))

            # The consumer still truthfully refuses the never-published
            # destination after the residue is gone.
            if os.path.isfile(list_path):
                os.remove(list_path)
            probe_consumer_open(consumer, work, dest, scenario_report, True)
        finally:
            resolver.close()
    finally:
        producer_service.kill_process_group()
        producer_service.close()
    return work


def scenario_d(direction, producer, consumer, work_dir, fixture_tool,
               scenario_report):
    """Crash direct.replace during uncommitted live-draft construction.

    Scenario B interrupts the immutable-to-live transition; this
    scenario interrupts the construction of an uncommitted direct
    replacement draft on a live database, with a successful control
    run first.  ``database.initialize_live`` completes on two fresh
    ``direct-v4`` fixtures (one control database, one interrupted
    database); the control run replaces the repaired 200 000-range
    CSV without interruption and fixes the post-transition facts (the
    transaction advanced by one, ``range_record_count`` equals the
    line count, and a deterministic content spot-check through the
    live reader's lookup).  The interrupted run launches the same
    replace and the producer is killed as soon as the main file grows
    past its pre-replace size while the uncommitted draft is being
    built: the observable process-crash marker during uncommitted
    live-draft construction.  This is explicitly not a storage-sync
    durability claim -- draft growth is an observable process-crash
    marker, never proof that storage synchronized.

    After recovery the committed database must reflect the
    PRE-transition state (the uncommitted draft never became the
    committed generation): pre-transition facts (``database.info``
    T0/R0 plus a content probe of the fixture ranges through the live
    reader's lookup) are captured before the interrupted replace and
    asserted after recovery, and the post-transition facts measured
    by the control run are asserted absent.  An immutable
    ``database.info`` truthfully refuses
    (``wrong_state``/``read_only_failure``), no managed maintenance
    residue exists, a consumer live reader observes the
    pre-transition generation, and a fresh producer commits a
    one-record replacement (T0+1), proving the live dataset still
    resolves.

    The approved durable sidecar marker (SOW-0028:3851, "commit/finish
    at a durable sidecar marker") remains recorded as impossible at
    this boundary: a live commit never writes the ``.readers``
    sidecar.  Go: ``LiveWriter.finishCommitLocked``
    (v4/go/internal/live/live_writer.go:471) calls ``commitLocked``
    (:491), whose ``prepublicationChecks`` (:509) only verifies the
    main/sidecar pair and scans the reader table; the publication
    ``Core.Publish`` (v4/go/internal/writer/publication.go:163)
    mutates only the main mapping (Shrink / FlushRange / SyncFile /
    meta-page encode + flush + sync).  Rust:
    ``LiveWriter::commit_with``
    (v4/rust/iprange-livedb/src/live_writer/commit.rs:26) runs
    ``prepublication_checks`` (:120-126: ``verify_pair`` plus
    ``sidecar.scan_at_most_cancellable``, read-only) before
    ``core.publish``.  Sidecar writes exist only in
    lifecycle_create/initialize_live, which scenario B already
    covers.  Exact commit/finish interruption is owned by the SDK
    fault gates, not this harness: Go
    ``TestLiveWriterCommitCrashPointsSelectOnlyACompleteGeneration``
    (v4/go/internal/live/lifecycle_crash_test.go:201; points
    commit.before_private_sync / commit.after_private_sync /
    commit.after_meta_write / commit.after_meta_sync),
    ``TestCrashCommitSelectsCompleteGeneration``
    (v4/go/internal/writer/crash_v4work_test.go:199),
    ``TestLiveWriterOutcomeUnknownFailClosed``
    (v4/go/internal/live/lifecycle_crash_test.go:365), and
    ``TestLiveWriterCommitCancellationAbortsDraft``
    (v4/go/internal/live/live_writer_test.go:401); Rust
    ``live_crash_tests::commit_crashes_select_only_a_complete_generation``
    (v4/rust/iprange-livedb/src/live_crash_tests.rs:232; fault points
    ``commit.before_private_sync``/``after_private_sync``/
    ``after_meta_write``/``after_meta_sync`` in
    v4/rust/iprange-livedb/src/writer_core/publication.rs:72-115),
    driven through ``live_crash_tests::crash_child`` and
    ``run_live_crash_child``
    (v4/rust/iprange-livedb/tests/mixed_live.rs:182).
    """

    work = os.path.join(work_dir, f"d-{direction}-{uuid.uuid4().hex[:8]}")
    os.makedirs(work)
    control_dir = os.path.join(work, "control")
    interrupted_dir = os.path.join(work, "interrupted")
    os.makedirs(control_dir)
    os.makedirs(interrupted_dir)
    control_main = os.path.join(control_dir, "db.iprange")
    interrupted_main = os.path.join(interrupted_dir, "db.iprange")
    feed = os.path.join(work, "big.csv")
    write_direct_csv_feed(feed, DIRECT_FEED_LINE_COUNT)
    for main in (control_main, interrupted_main):
        fixture = subprocess.run(
            [fixture_tool, "direct-v4", main], capture_output=True,
            timeout=300, env=child_environment())
        if fixture.returncode != 0:
            detail = fixture.stderr.decode("utf-8", "replace").strip()
            raise ScenarioFailure(
                f"v4-fixture direct-v4 failed with exit {fixture.returncode}: "
                f"{detail}")

    scenario_report["fixture_created_main"] = True
    # Control run first: the repaired replace WITHOUT interruption.  It
    # fixes the post-transition facts (transaction advanced,
    # range_record_count == line count, deterministic lookup
    # spot-check) that the interrupted run must prove absent.
    control_service = HarnessJsonRpcService(
        [producer, "--jsonrpc"], f"control-{direction}", cwd=work)
    try:
        initialized = control_service.call(
            "1", "iprange.v1.database.initialize_live",
            {"path": control_main, "reader_capacity": 8})
        assert_truthful(
            "error" not in initialized,
            f"initialize_live must succeed, got {initialized}",
            scenario_report)
        control_info0 = control_service.call(
            "2", "iprange.v1.database.info",
            {"source": {"path": control_main, "mode": "live"}})
        assert_truthful(
            "error" not in control_info0,
            f"database.info live must succeed, got {control_info0}",
            scenario_report)
        _record_live_open(scenario_report, "producer")
        control_pre = control_info0["result"]["info"]
        replaced = control_service.call(
            "3", "iprange.v1.direct.replace",
            direct_replace_params(control_main, feed))
        assert_truthful(
            "error" not in replaced
            and replaced["result"].get("commit", {}).get(
                "attempted_transaction_id") is not None,
            f"the control replace must complete, got {replaced}",
            scenario_report)
        control_info1 = control_service.call(
            "4", "iprange.v1.database.info",
            {"source": {"path": control_main, "mode": "live"}})
        assert_truthful(
            "error" not in control_info1,
            f"database.info live must succeed, got {control_info1}",
            scenario_report)
        control_post = control_info1["result"]["info"]
        assert_truthful(
            decimal_u64(control_post.get("transaction_id"))
            == decimal_u64(control_pre.get("transaction_id")) + 1
            and decimal_u64(control_post.get("range_record_count"))
            == DIRECT_FEED_LINE_COUNT,
            "the control replace must advance the transaction by one "
            "and land exactly the repaired feed line count "
            f"({DIRECT_FEED_LINE_COUNT}), got "
            f"{control_post.get('transaction_id')}/"
            f"{control_post.get('range_record_count')}", scenario_report)
        control_spot = live_reader_lookup(
            control_service, "5", control_main,
            ["10.0.0.0", "10.0.0.63", "10.0.0.64", "10.195.79.192",
             "10.195.79.255", "192.0.2.10"], scenario_report)
        assert_truthful(
            normalized_lookup(control_spot) == [
                ("10.0.0.0", True, 1),
                ("10.0.0.63", True, 1),
                ("10.0.0.64", True, 2),
                ("10.195.79.192", True, DIRECT_FEED_LINE_COUNT),
                ("10.195.79.255", True, DIRECT_FEED_LINE_COUNT),
                ("192.0.2.10", False, None)],
            "the control spot-check must show the repaired feed ranges "
            f"with their per-line values, got {normalized_lookup(control_spot)}",
            scenario_report)
        scenario_report["control_run"] = {
            "pre_transition_info": {
                "transaction_id": control_pre.get("transaction_id"),
                "range_record_count":
                    control_pre.get("range_record_count")},
            "post_transition_info": {
                "transaction_id": control_post.get("transaction_id"),
                "range_record_count":
                    control_post.get("range_record_count")},
            "spot_check_normalized": normalized_lookup(control_spot),
        }
    finally:
        control_service.close()

    producer_service = KillableJsonRpcService(
        [producer, "--jsonrpc"], f"producer-{direction}", cwd=work)
    try:
        initialized = producer_service.call(
            "1", "iprange.v1.database.initialize_live",
            {"path": interrupted_main, "reader_capacity": 8})
        assert_truthful(
            "error" not in initialized,
            f"initialize_live must succeed, got {initialized}",
            scenario_report)
        # The interrupted database's sidecar was created by THIS
        # initialize_live (a later operation than the control run's);
        # its ordinal is the live_sidecar creation credit.
        _record_creation(scenario_report, ("live_sidecar",))
        info0 = producer_service.call(
            "2", "iprange.v1.database.info",
            {"source": {"path": interrupted_main, "mode": "live"}})
        assert_truthful(
            "error" not in info0,
            f"database.info live must succeed, got {info0}",
            scenario_report)
        _record_live_open(scenario_report, "producer")
        info_facts = info0["result"]["info"]
        t0 = info_facts.get("transaction_id")
        r0 = info_facts.get("range_record_count")
        assert_truthful(
            t0 is not None and r0 is not None,
            f"database.info must report T0 and range count, got "
            f"{info_facts}", scenario_report)
        pre_lookup = live_reader_lookup(
            producer_service, "3", interrupted_main,
            ["192.0.2.10", "192.0.2.20", "198.51.100.35", "10.0.0.0"],
            scenario_report)
        assert_truthful(
            normalized_lookup(pre_lookup) == [
                ("192.0.2.10", True, 10),
                ("192.0.2.20", True, 15),
                ("198.51.100.35", True, 30),
                ("10.0.0.0", False, None)],
            "the pre-transition fixture content probe must be intact, "
            f"got {normalized_lookup(pre_lookup)}", scenario_report)
        size_before = os.path.getsize(interrupted_main)

        outcome, seen_ms, thread = call_with_worker(
            producer_service, "4", "iprange.v1.direct.replace",
            direct_replace_params(interrupted_main, feed),
            POLL_DEADLINE_SECONDS,
            seen=lambda: os.path.isfile(interrupted_main)
            and os.path.getsize(interrupted_main) > size_before)
        if seen_ms is None:
            raise ScenarioFailure(
                "live draft-growth marker was not observed; "
                f"worker outcome={outcome}")
        scenario_report["marker_seen_ms"] = round(seen_ms, 1)
        producer_service.kill_process_group()
        thread.join(timeout=5)
        scenario_report["kill_method"] = (
            "SIGKILL process group at observable process-crash marker "
            "during uncommitted live-draft construction (main-file "
            "growth while the draft is being built; explicitly not a "
            "storage-sync durability claim).  The uncommitted draft "
            "must never become the committed generation: the committed "
            "database must reflect the pre-transition state.  The "
            "approved durable sidecar marker is impossible because "
            "neither engine writes the .readers sidecar during a live "
            "commit (evidence: Go internal/writer/publication.go "
            "Publish; Rust live_writer/commit.rs); exact "
            "commit/finish interruption is covered by the SDK fault "
            "gates (Go lifecycle_crash_test.go "
            "TestLiveWriterCommitCrashPointsSelectOnlyACompleteGeneration "
            "and TestLiveWriterOutcomeUnknownFailClosed; live_writer_test.go "
            "TestLiveWriterCommitCancellationAbortsDraft; Rust "
            "live_crash_tests::commit_crashes_select_only_a_complete_generation)")
        scenario_report["destination_state"] = {
            "class": "live_dataset_with_uncommitted_write",
            "main_size_before": size_before,
            "main_size_after": os.path.getsize(interrupted_main),
        }

        resolver = HarnessJsonRpcService(
            [producer, "--jsonrpc"], f"resolver-{direction}", cwd=work)
        try:
            info_live = resolver.call(
                "5", "iprange.v1.database.info",
                {"source": {"path": interrupted_main, "mode": "live"}})
            assert_truthful(
                "error" not in info_live,
                f"database.info live must succeed on a fresh producer, "
                f"got {info_live}", scenario_report)
            _record_live_open(scenario_report, "producer")
            live_facts = info_live["result"]["info"]
            assert_truthful(
                live_facts.get("transaction_id") == t0
                and live_facts.get("range_record_count") == r0,
                "an interrupted draft must never become the generation: "
                f"expected pre-transition T0={t0} R0={r0}, got "
                f"{live_facts.get('transaction_id')}/"
                f"{live_facts.get('range_record_count')}", scenario_report)
            post_facts = (scenario_report.get("control_run") or {}).get(
                "post_transition_info") or {}
            assert_truthful(
                not post_facts
                or (decimal_u64(live_facts.get("transaction_id"))
                    != decimal_u64(post_facts.get("transaction_id"))
                    and decimal_u64(live_facts.get("range_record_count"))
                    != decimal_u64(post_facts.get("range_record_count"))),
                "the control run's post-transition facts must be absent "
                "after the interrupted draft: got "
                f"{live_facts.get('transaction_id')}/"
                f"{live_facts.get('range_record_count')}, control post "
                f"{post_facts.get('transaction_id')}/"
                f"{post_facts.get('range_record_count')}", scenario_report)

            post_lookup = live_reader_lookup(
                resolver, "6", interrupted_main,
                ["192.0.2.10", "192.0.2.20", "198.51.100.35", "10.0.0.0"],
                scenario_report)
            assert_truthful(
                normalized_lookup(post_lookup) ==
                normalized_lookup(pre_lookup),
                "the committed database must keep the pre-transition "
                "content probe after the interrupted draft, got "
                f"{normalized_lookup(post_lookup)}", scenario_report)
            absent_lookup = live_reader_lookup(
                resolver, "7", interrupted_main,
                ["10.0.0.0", "10.0.0.64", "10.195.79.192"],
                scenario_report)
            assert_truthful(
                normalized_lookup(absent_lookup) == [
                    ("10.0.0.0", False, None),
                    ("10.0.0.64", False, None),
                    ("10.195.79.192", False, None)],
                "the control run's repaired-feed content must be absent "
                "after the interrupted draft, got "
                f"{normalized_lookup(absent_lookup)}", scenario_report)
            scenario_report["inspect_outcome"] = {
                "database_info_live_pre_transition": {
                    "transaction_id": live_facts.get("transaction_id"),
                    "range_record_count":
                        live_facts.get("range_record_count")},
                "control_post_transition_absent": True,
                "pre_transition_probe_after_recovery":
                    normalized_lookup(post_lookup),
            }

            info_immutable = resolver.call(
                "8", "iprange.v1.database.info",
                {"source": {"path": interrupted_main, "mode": "immutable"}})
            immutable_error = info_immutable.get("error", {}).get("data", {})
            assert_truthful(
                immutable_error.get("code") == "wrong_state"
                and immutable_error.get("outcome") == "read_only_failure",
                "immutable database.info must truthfully refuse a live "
                f"database, got {info_immutable}", scenario_report)
            scenario_report["inspect_outcome"]["database_info_immutable"] = {
                "code": immutable_error.get("code"),
                "outcome": immutable_error.get("outcome")}

            reports, error = maintenance_reports(
                resolver, interrupted_dir,
                os.path.join(interrupted_dir, "pre.jsonl"),
                ["scratch", "reservation", "publication_temp"])
            assert_truthful(
                error is None and count_kind(reports, "scratch") == 0
                and count_kind(reports, "reservation") == 0
                and count_kind(reports, "publication_temp") == 0,
                f"an interrupted draft must leave no managed maintenance "
                f"residue, got reports={reports} error={error}",
                scenario_report)
            scenario_report["residue_bounded"] = {
                "maintenance_list_pre": reports}

            consumer_service = HarnessJsonRpcService(
                [consumer, "--jsonrpc"], f"consumer-{direction}", cwd=work)
            try:
                open_live = consumer_service.call(
                    "9", "iprange.v1.reader.open",
                    {"source": {"path": interrupted_main, "mode": "live"}})
                assert_truthful(
                    "error" not in open_live,
                    f"consumer live reader must open, got {open_live}",
                    scenario_report)
                _record_live_open(scenario_report, "consumer")
                main_open_ordinal = _current_ordinal("consumer")
                reopen_info = open_live["result"].get("info", {})
                assert_truthful(
                    reopen_info.get("transaction_id") == t0,
                    "consumer live reader must observe the committed "
                    f"pre-transition T0, got "
                    f"{reopen_info.get('transaction_id')}",
                    scenario_report)
                consumer_service.call(
                    "10", "iprange.v1.reader.close",
                    {"reader": open_live["result"]["reader"]})
                scenario_report["reopen_outcome"] = {
                    "consumer_live_reader_transaction_id":
                        reopen_info.get("transaction_id"),
                    "consumer_main_open_ordinal": main_open_ordinal}
            finally:
                consumer_service.close()

            # A fresh producer commits a one-record replacement: the
            # interrupted draft attempt left no authority behind, so the
            # next commit is T0+1 with exactly the one new record.  On
            # the Go product the killed producer's worker closes the
            # sidecar asynchronously, so the first attempt may truthfully
            # refuse writer_busy while the dead process's writer lease is
            # still being released; retry with a bounded window (the
            # commit assertion below stays strict).
            small = os.path.join(work, "small.csv")
            with open(small, "w", encoding="utf-8", newline="") as stream:
                stream.write("from,to,value\n")
                stream.write("192.0.2.10,192.0.2.14,10\n")
            replaced = None
            transient_busy = 0
            for attempt_index in range(10):
                attempted = resolver.call(
                    f"11-{attempt_index}", "iprange.v1.direct.replace",
                    direct_replace_params(interrupted_main, small))
                error = attempted.get("error", {}).get("data", {})
                if error and error.get("code") == "writer_busy":
                    transient_busy += 1
                    time.sleep(0.25)
                    continue
                replaced = attempted
                break
            assert_truthful(
                replaced is not None and "error" not in replaced
                and replaced["result"].get("commit", {}).get(
                    "attempted_transaction_id") is not None,
                f"fresh replace must commit, got {replaced}",
                scenario_report)
            info_after = resolver.call(
                "12", "iprange.v1.database.info",
                {"source": {"path": interrupted_main, "mode": "live"}})
            assert_truthful(
                "error" not in info_after,
                f"database.info live must succeed after the commit, got "
                f"{info_after}", scenario_report)
            _record_live_open(scenario_report, "producer")
            after_facts = info_after["result"]["info"]
            assert_truthful(
                decimal_u64(after_facts.get("transaction_id"))
                == decimal_u64(t0) + 1
                and decimal_u64(after_facts.get("range_record_count")) == 1,
                "fresh replace must land as T0+1 with one record, got "
                f"{after_facts.get('transaction_id')}/"
                f"{after_facts.get('range_record_count')}", scenario_report)
            scenario_report["resolve_outcome"] = {
                "database_info_live_post_kill": {
                    "transaction_id": live_facts.get("transaction_id"),
                    "range_record_count":
                        live_facts.get("range_record_count")},
                "transient_writer_busy_attempts": transient_busy,
                "fresh_replace_commit": {
                    "attempted_transaction_id": replaced["result"][
                        "commit"].get("attempted_transaction_id"),
                    "transaction_id_after":
                        after_facts.get("transaction_id"),
                    "range_record_count_after":
                        after_facts.get("range_record_count")},
            }
        finally:
            resolver.close()
    finally:
        producer_service.kill_process_group()
        producer_service.close()
    return work


def scenario_e(direction, producer, consumer, work_dir, scenario_report):
    """Crash export at the partial-output marker (real flushed output).

    ``current.publish`` builds one 500 000-range immutable main; a
    completed reference export fixes the exact destination bytes, then
    the same export is killed as soon as its private
    ``<id>.export.tmp`` partial output carries real flushed bytes.
    Both exporters buffer 64 KiB (Go ``fileio.NewExportWriter``
    v4/go/internal/cli/fileio/export_writer.go:109-112; Rust
    ``ExportWriter::create`` v4/rust/iprange-cli/src/io/
    export_writer.rs:100-119), so the temp stays 0 bytes until the
    first buffer flush and ``size > 0`` means deterministic real
    partial output, never a mere empty temp creation.  The
    destination is absent after the kill; exactly one private
    ``.<handle>.export.tmp`` orphan (the interrupted attempt's
    O_EXCL temporary) is the bounded residue (an output scratch, not
    a managed maintenance kind), and the source main is
    byte-identical.  A fresh producer retries the same export, lands
    byte-identical to the reference, and removes its own private
    temp -- the orphaned temp never blocks and no new residue
    appears -- and the consumer still opens the intact source.
    """

    work = os.path.join(work_dir, f"e-{direction}-{uuid.uuid4().hex[:8]}")
    os.makedirs(work)
    feed = os.path.join(work, "feed.txt")
    write_interval_feed(feed, EXPORT_FEED_LINE_COUNT)
    source = os.path.join(work, "big.iprange")
    params = publish_params(feed, source, "fail_if_exists")

    builder = HarnessJsonRpcService(
        [producer, "--jsonrpc"], f"builder-{direction}", cwd=work)
    try:
        built = builder.call("1", "iprange.v1.current.publish", params)
        assert_truthful(
            "error" not in built,
            f"big publish must succeed, got {built}", scenario_report)
        # The completed big publish created the source main; its
        # operation ordinal is the v4_main creation credit.
        _record_creation(scenario_report, ("v4_main",))
    finally:
        builder.close()

    # Reference export: the exact complete destination bytes.
    ref_path = os.path.join(work, "ref.txt")
    ref_service = HarnessJsonRpcService(
        [producer, "--jsonrpc"], f"ref-{direction}", cwd=work)
    try:
        reference = ref_service.call(
            "2", "iprange.v1.export", export_params(source, ref_path))
        assert_truthful(
            "error" not in reference,
            f"reference export must succeed, got {reference}",
            scenario_report)
    finally:
        ref_service.close()
    ref_sha = sha256_file(ref_path)
    source_sha = sha256_file(source)

    dest = os.path.join(work, "out.txt")
    producer_service = KillableJsonRpcService(
        [producer, "--jsonrpc"], f"producer-{direction}", cwd=work)
    try:
        outcome, seen_ms, thread = call_with_worker(
            producer_service, "3", "iprange.v1.export",
            export_params(source, dest), POLL_DEADLINE_SECONDS,
            seen=lambda: any(
                os.path.getsize(os.path.join(work, name)) > 0
                for name in export_temp_basenames(work)))
        if seen_ms is None:
            raise ScenarioFailure(
                "export partial-output marker (real flushed output) was "
                f"not observed; worker outcome={outcome}")
        scenario_report["marker_seen_ms"] = round(seen_ms, 1)
        producer_service.kill_process_group()
        thread.join(timeout=5)
        # The crashed export created the observed adapter-output orphan
        # (its O_EXCL private temp); its operation ordinal is the
        # adapter_output creation credit.
        _record_creation(scenario_report, ("adapter_output",))
        scenario_report["kill_method"] = (
            "SIGKILL process group at export partial-output marker "
            "(real flushed output: the 64 KiB-buffered temp is 0 bytes "
            "until the first flush; the kill waits for size > 0)")

        orphans = export_temp_basenames(work)
        assert_truthful(
            not os.path.isfile(dest),
            "export destination must be absent after the kill",
            scenario_report)
        assert_one_export_orphan(orphans, scenario_report)
        # The interrupted attempt's export writer OPENED its private
        # adapter-output temp (O_EXCL create+open in both engines: Go
        # v4/go/internal/cli/fileio/export_writer.go:106, Rust
        # v4/rust/iprange-cli/src/io/export_writer.rs:100), and the
        # retry's writer opens its own temp the same way, so the
        # producer is the adapter_output opener actor.  The consumer
        # never opens the adapter output: adapter outputs are plain
        # text, not v4 mains, so reader.open cannot open them (the
        # scenario's consumer probe targets the v4 main source).
        _record_adapter_open(scenario_report, "producer")
        assert_truthful(
            sha256_file(source) == source_sha,
            "the immutable source must be unchanged by the interrupted "
            "export", scenario_report)
        scenario_report["destination_state"] = {
            "class": "export_partial_output",
            "dest_absent_after_crash": True,
            "export_temp_basenames": orphans,
        }
        scenario_report["residue_bounded"] = {
            "export_temp_orphans": orphans,
        }

        resolver = HarnessJsonRpcService(
            [producer, "--jsonrpc"], f"resolver-{direction}", cwd=work)
        try:
            retried = resolver.call(
                "4", "iprange.v1.export", export_params(source, dest))
            assert_truthful(
                "error" not in retried,
                f"export retry must complete, got {retried}",
                scenario_report)
            assert_truthful(
                os.path.isfile(dest) and sha256_file(dest) == ref_sha,
                "retried export must land byte-identical to the "
                "reference", scenario_report)
            scenario_report["resolve_outcome"] = {
                "retry_export_completed": True,
                "sha256_match": True,
            }
            # The completed retry removed its own private temp: the
            # post-retry orphan set must be EXACTLY the pre-retry set
            # (no new .export.tmp residue, and the interrupted attempt's
            # orphan is untouched).
            orphans_after_retry = export_temp_basenames(work)
            assert_truthful(
                orphans_after_retry == orphans,
                "the completed retry must remove its own private temp "
                "and leave exactly the pre-retry orphan set (no new "
                f"residue): pre-retry {orphans}, post-retry "
                f"{orphans_after_retry}", scenario_report)
            scenario_report["residue_bounded"]["orphans_after_retry"] = (
                orphans_after_retry)

            # The consumer still opens the intact immutable source
            # (mandatory: an existing source must never refuse).
            probe_consumer_open(consumer, work, source, scenario_report,
                                False, require_open=True)
        finally:
            resolver.close()
    finally:
        producer_service.kill_process_group()
        producer_service.close()
    return work


def live_reader_lookup(service, request_id, main, addresses,
                      scenario_report):
    """Open one live reader, lookup addresses, and close it again.

    Returns the ``matches`` list of ``iprange.v1.reader.lookup`` over
    the committed live generation of ``main``: the deterministic
    content probe used by scenario D's control run and interrupted
    run.  The successful live open is recorded for the scenario's
    ``live_reader_opens`` lineage (scenario D runs every probe on the
    producer binary's services).  Raises AssertionError on any
    protocol deviation.
    """

    opened = service.call(
        str(request_id), "iprange.v1.reader.open",
        {"source": {"path": main, "mode": "live"}})
    assert "error" not in opened, opened
    role = _service_role(service.argv)
    if role is not None:
        _record_live_open(scenario_report, role)
    reader = opened["result"]["reader"]
    try:
        looked = service.call(
            f"{request_id}-lookup", "iprange.v1.reader.lookup",
            {"reader": reader, "addresses": list(addresses)})
        assert "error" not in looked, looked
        matches = looked["result"].get("matches")
        assert matches is not None and len(matches) == len(addresses), looked
        return matches
    finally:
        service.call(
            f"{request_id}-close", "iprange.v1.reader.close",
            {"reader": reader})


def normalized_lookup(matches):
    """Lookup matches as (address, present, value-or-None) tuples."""

    return [
        (match.get("address"), match.get("present") is True,
         match.get("value") if match.get("present") is True else None)
        for match in matches]


def range_leaf_pages(path, keep_last):
    """Page numbers of the last ``keep_last`` range-tree leaf pages.

    Walks the committed range tree (meta ``range_root`` at byte offset
    144, slotted-page convention of binary-format-v4.md sections 6-7):
    branch slots at byte 32 carry ``item_count`` u16 record offsets
    and each IPv4 branch record is ``first_from:u32 child_pgno:u32``.
    Leaf pages are not decoded, only counted.  The walk makes the
    scenario F damage pattern independent of the builder's exact
    branch placement (both products build the same format, but the
    harness never assumes a fixed page layout).
    """

    page_size = 4096

    def u16(data, offset):
        return int.from_bytes(data[offset:offset + 2], "little")

    def u32(data, offset):
        return int.from_bytes(data[offset:offset + 4], "little")

    with open(path, "rb") as stream:
        data = stream.read()
    leaves = []

    def visit(page_number):
        offset = page_number * page_size
        page_type = data[offset + 4]
        item_count = u16(data, offset + 16)
        if page_type == 1:  # range branch
            for index in range(item_count):
                record_offset = u16(data, offset + 32 + 2 * index)
                visit(u32(data, offset + record_offset + 4))
        else:  # range leaf (page type 2)
            leaves.append(page_number)

    visit(u32(data, 144))
    leaves.sort()
    return leaves[-keep_last:]


def scenario_f(direction, producer, consumer, work_dir, scenario_report):
    """Crash validate during validation/findings delivery.

    One 1 500 000-range immutable main (``FEED_LINE_COUNT``) is
    damaged by zeroing its last 1 400 range-tree leaf pages
    (``F_DAMAGED_LEAF_PAGES``; the leaf page numbers are derived from
    the committed range tree at runtime, see ``range_leaf_pages``,
    so the pattern never depends on the builder's exact branch
    placement); a reference validate reports valid=false with >= 1000
    findings (page-CRC + count findings; the exact count is verified
    identical for both product binaries on this deterministic damage
    -- measured 1 402 findings / 125 057 bytes -- and the lead
    recalibrates it during the full battery).  The same validate is
    killed as soon as its private ``<id>.export.tmp`` findings-output
    temp carries real flushed bytes: the findings destination is
    absent after the kill, the temp content is a strict prefix of the
    reference findings with strictly fewer bytes, one temp orphan is
    the bounded residue, and the damaged main is byte-identical.  A
    fresh validate reports the same truthful findings and the
    consumer still opens the damaged main (the committed generation
    stays readable).

    The marker is flushed bytes, not temp existence: both products
    buffer 64 KiB (Go ``fileio.NewExportWriter``
    v4/go/internal/cli/fileio/export_writer.go:109-112; Rust
    ``ExportWriter::create`` v4/rust/iprange-cli/src/io/
    export_writer.rs:100-119) and flush a full block only when it
    fills, so a non-empty temporary alone is insufficient.  With
    reference bytes strictly between one and two blocks the temp
    becomes non-empty exactly once while the walk still runs and is
    always a strict prefix of the reference findings: the kill lands
    during validation/findings delivery, never at an exact internal
    walk point.  The plan-recorded worker-scratch marker
    (SOW-0028:3851, "validate at the worker scratch marker") is
    impossible: neither engine's validate ever spills to authorized
    scratch.  The scratch budget fields are API-parity only in both
    validation engines -- Go v4/go/internal/validation/types.go:28-68
    validates the fields and no validation file consumes them; a heap
    too small for the 2-bit claim bitmap is a truthful
    ``insufficient_resource_budget``/``read_only_failure`` refusal
    (``newClaims``, v4/go/internal/validation/context.go:46-50),
    never a spill.  Rust v4/rust/iprange-livedb/src/validation/
    types.rs:24-53 is the same parity-only surface, heap exhaustion
    is ``Claims::new``/``BudgetExceeded``
    (v4/rust/iprange-livedb/src/validation/context.rs:37,485), and
    the only authorized-scratch implementation in the crate is the
    recovery sweep
    (v4/rust/iprange-livedb/src/recovery/scratch_maintenance.rs).
    Calibrated empirically against both staged binaries: no scratch
    file ever appeared; below the claim-bitmap requirement validate
    refuses with the budget error and at every larger heap it
    completes fully in memory.

    Determinism calibration: the chosen damage is exactly
    ``F_DAMAGED_LEAF_PAGES`` (= 1 400) whole zeroed range-tree leaf
    pages of a ``FEED_LINE_COUNT`` (= 1 500 000)-range main; the same
    damage must give the same reference finding count (and bytes) for
    both product binaries.  The harness asserts the design bounds
    (>= 1000 findings, reference bytes strictly between 65 536 and
    131 072); the lead calibrates/verifies the exact count during the
    full battery.
    """

    work = os.path.join(work_dir, f"f-{direction}-{uuid.uuid4().hex[:8]}")
    os.makedirs(work)
    feed = os.path.join(work, "feed.txt")
    write_interval_feed(feed, FEED_LINE_COUNT)
    source = os.path.join(work, "big.iprange")
    params = publish_params(feed, source, "fail_if_exists")

    builder = HarnessJsonRpcService(
        [producer, "--jsonrpc"], f"builder-{direction}", cwd=work)
    try:
        built = builder.call("1", "iprange.v1.current.publish", params)
        assert_truthful(
            "error" not in built,
            f"big publish must succeed, got {built}", scenario_report)
        # The completed big publish created the damaged main; its
        # operation ordinal is the v4_main creation credit.
        _record_creation(scenario_report, ("v4_main",))
    finally:
        builder.close()
    # Deterministic damage: zero the last F_DAMAGED_LEAF_PAGES
    # range-tree leaf pages (whole pages; parent branches stay intact
    # and valid, so the walk keeps visiting every corrupted leaf).
    corrupt = range_leaf_pages(source, F_DAMAGED_LEAF_PAGES)
    assert_truthful(
        len(corrupt) == F_DAMAGED_LEAF_PAGES,
        f"the 1 500 000-range main must hold >= {F_DAMAGED_LEAF_PAGES} "
        f"leaf pages, got {len(corrupt)}", scenario_report)
    with open(source, "r+b") as stream:
        for page_number in corrupt:
            stream.seek(page_number * 4096)
            stream.write(b"\x00" * 4096)
    damaged_sha = sha256_file(source)

    ref_path = os.path.join(work, "ref-findings.jsonl")
    ref_service = HarnessJsonRpcService(
        [producer, "--jsonrpc"], f"ref-{direction}", cwd=work)
    try:
        reference = ref_service.call(
            "2", "iprange.v1.validate", validate_params(source, ref_path))
        assert_truthful(
            "error" not in reference,
            f"reference validate must succeed, got {reference}",
            scenario_report)
    finally:
        ref_service.close()
    ref_result = reference["result"]["result"]
    ref_findings = reference["result"].get("findings", {})
    ref_valid = ref_result.get("valid")
    ref_count = ref_result.get("progress", {}).get("finding_count")
    ref_rows = ref_findings.get("rows")
    ref_bytes = os.path.getsize(ref_path) if os.path.isfile(ref_path) else 0
    assert_truthful(
        ref_valid is False
        and decimal_u64(ref_count) >= 1000
        and decimal_u64(ref_rows) == decimal_u64(ref_count)
        and 65536 < ref_bytes < 131072,
        "reference validate on the deterministic leaf damage must find "
        f">= 1000 findings (got {ref_count}) with reference bytes "
        f"strictly between one and two 64 KiB writer blocks (got "
        f"{ref_bytes}); the lead recalibrates the exact finding count "
        "during the full battery", scenario_report)
    scenario_report["inspect_outcome"] = {
        "reference_validate": {
            "valid": ref_valid,
            "finding_count": ref_count,
            "rows": ref_rows,
            "bytes": ref_bytes,
            "damaged_leaf_pages": len(corrupt),
        },
    }

    findings_out = os.path.join(work, "f.jsonl")
    producer_service = KillableJsonRpcService(
        [producer, "--jsonrpc"], f"producer-{direction}", cwd=work)
    try:
        outcome, seen_ms, thread = call_with_worker(
            producer_service, "3", "iprange.v1.validate",
            validate_params(source, findings_out), POLL_DEADLINE_SECONDS,
            seen=lambda: any(
                os.path.getsize(os.path.join(work, name)) > 0
                for name in export_temp_basenames(work)))
        if seen_ms is None:
            raise ScenarioFailure(
                "validate findings-output flush marker (real flushed "
                f"bytes) was not observed; worker outcome={outcome}")
        scenario_report["marker_seen_ms"] = round(seen_ms, 1)
        producer_service.kill_process_group()
        thread.join(timeout=5)
        scenario_report["kill_method"] = (
            "SIGKILL process group at findings-output flush marker "
            "during validation/findings delivery (the private export "
            "temp carries real flushed bytes; interruption lands during "
            "delivery, not at an exact internal walk point).  The "
            "worker-scratch marker is impossible because neither "
            "engine's validate spills to authorized scratch (scratch is "
            "API-parity only in v4/go/internal/validation and "
            "v4/rust/iprange-livedb/src/validation; a heap below the "
            "claim-bitmap requirement is a truthful "
            "insufficient_resource_budget refusal, never a spill)")

        orphans = export_temp_basenames(work)
        assert_truthful(
            not os.path.isfile(findings_out),
            "findings destination must be absent after the kill",
            scenario_report)
        assert_truthful(
            len(orphans) == 1,
            f"exactly one findings-output temp must remain as the bounded "
            f"residue, got {orphans}", scenario_report)
        temp_path = os.path.join(work, orphans[0])
        temp_bytes = os.path.getsize(temp_path)
        assert_truthful(
            temp_bytes > 0,
            "the interrupted findings temp must carry real flushed "
            f"bytes, got {temp_bytes}", scenario_report)
        assert_truthful(
            temp_bytes < ref_bytes,
            "interrupted findings bytes must be strictly fewer than the "
            f"reference findings bytes ({temp_bytes} < {ref_bytes})",
            scenario_report)
        with open(temp_path, "rb") as stream, open(
                ref_path, "rb") as reference_stream:
            interrupted_head = stream.read()
            reference_head = reference_stream.read(len(interrupted_head))
        assert_truthful(
            interrupted_head == reference_head,
            "interrupted findings temp must be a strict prefix of the "
            "reference findings", scenario_report)
        assert_truthful(
            sha256_file(source) == damaged_sha,
            "the damaged main must be byte-identical after the kill",
            scenario_report)
        scenario_report["destination_state"] = {
            "class": "validate_findings_aborted",
            "main_sha256_unchanged": True,
            "findings_temp_basenames": orphans,
            "findings_temp_bytes": temp_bytes,
            "reference_bytes": ref_bytes,
        }
        scenario_report["residue_bounded"] = {
            "findings_dest_absent": True,
            "export_temp_orphans": orphans,
            "findings_temp_bytes": temp_bytes,
            "main_sha256_unchanged": True,
        }

        resolver = HarnessJsonRpcService(
            [producer, "--jsonrpc"], f"resolver-{direction}", cwd=work)
        try:
            fresh_path = os.path.join(work, "fresh-findings.jsonl")
            fresh = resolver.call(
                "4", "iprange.v1.validate",
                validate_params(source, fresh_path))
            assert_truthful(
                "error" not in fresh,
                f"fresh validate must succeed, got {fresh}",
                scenario_report)
            fresh_result = fresh["result"]["result"]
            fresh_findings = fresh["result"].get("findings", {})
            fresh_valid = fresh_result.get("valid")
            fresh_count = fresh_result.get("progress", {}).get(
                "finding_count")
            fresh_rows = fresh_findings.get("rows")
            assert_truthful(
                fresh_valid is False
                and decimal_u64(fresh_count) == decimal_u64(ref_count)
                and decimal_u64(fresh_rows) == decimal_u64(ref_rows),
                "fresh validate must report the same truthful findings "
                f"as the reference (valid={ref_valid} "
                f"count={ref_count} rows={ref_rows}), got "
                f"valid={fresh_valid} count={fresh_count} rows={fresh_rows}",
                scenario_report)
            assert_truthful(
                sha256_file(fresh_path) == sha256_file(ref_path),
                "fresh validate findings must be byte-identical to the "
                "reference findings (same deterministic damage)",
                scenario_report)
            scenario_report["resolve_outcome"] = {
                "fresh_validate_reported_same": True,
                "findings_content_sha256_identical": True,
                "reference_finding_count": ref_count,
                "fresh_finding_count": fresh_count,
                "fresh_rows": fresh_rows,
            }

            # The consumer still opens the damaged main: the committed
            # generation stays readable (mandatory: an existing source
            # must never refuse).
            probe_consumer_open(consumer, work, source, scenario_report,
                                False, require_open=True)
        finally:
            resolver.close()
    finally:
        producer_service.kill_process_group()
        producer_service.close()
    return work


def no_leftover_processes():
    """Return every owned product PID that is still alive.

    The check matches exactly the PIDs recorded from our own spawns
    (module-level ``SPAWNED_PIDS``), never a pkill-style pattern.
    """

    leftover = []
    for pid in SPAWNED_PIDS:
        try:
            os.kill(pid, 0)
            leftover.append(pid)
        except ProcessLookupError:
            pass
        except PermissionError:
            leftover.append(pid)
    return leftover


def product_implementation(binary, fallback):
    """Probe one product binary's declared implementation name.

    A binary that cannot answer the capability probe (for example the
    /bin/false negative control) is labeled with the caller's
    fallback name; its scenarios fail anyway, so the label is only
    descriptive.  The probe runs under the same deadline as the
    scenarios so a silent-but-alive binary cannot hang the harness.
    """

    try:
        service = run.JsonRpcService([binary, "--jsonrpc"], "probe")
        try:
            outcome, _seen_ms, thread = call_with_worker(
                service, "implementation-probe",
                "iprange.v1.system.describe", {},
                POLL_DEADLINE_SECONDS, lambda: False)
            thread.join(timeout=1)
            response = outcome.get("response")
            impl = response.get("result", {}).get("implementation")
            if impl in ("rust", "go"):
                return impl
        finally:
            service.close()
    except (AssertionError, AttributeError, OSError, TypeError,
            ValueError, frame.FrameError):
        pass
    return fallback


def executable(value, label):
    """Validate one absolute executable path (run.py parity)."""

    if not os.path.isabs(value):
        raise SystemExit(f"{label} is not an absolute executable file: {value}")
    if not os.path.isfile(value) or not os.access(value, os.X_OK):
        raise SystemExit(f"{label} is not an absolute executable file: {value}")
    return os.path.realpath(value)




def _consumer_opened_main(scenario_report):
    """True when the scenario's consumer successfully opened the main.

    The evidence comes from the scenario report's ``reopen_outcome``:
    ``probe_consumer_open`` on an existing destination (A2/A3/E/F),
    the post-resolution consumer reopen of the resolved publication
    (A1/A2), the consumer live reader after the resolved transition
    (B), or scenario D's consumer live reader of the committed
    generation.
    """

    outcome = scenario_report.get("reopen_outcome") or {}
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


def _record_live_open(scenario_report, actor):
    """Record one successful live reader open by a scenario actor.

    A live reader open registers in and writes the ``<main>.readers``
    sidecar reader table (Rust reader_core/live.rs:62 ->
    live_sidecar.rs:170-191; Go live_reader.go:68 -> sidecar.go:146),
    so every successful live-mode ``reader.open`` / ``database.info``
    call the scenario executes is the sidecar-open lineage evidence.
    The record stores the ordinal of the service call that performed
    the open (``_current_ordinal`` at the call site), so the emitted
    opened refs name the exact executed operation.  Idempotent;
    ``observed_kinds`` derives the ``live_sidecar`` opened lineage
    from this record.
    """

    scenario_report.setdefault("live_reader_opens", {})[actor] = \
        _current_ordinal(actor)


def _record_adapter_open(scenario_report, actor):
    """Record one adapter-output open by a scenario actor.

    An adapter output (export/validate destination family, including
    the writer's private ``.<handle>.export.tmp``) is opened by the
    export/validate WRITER of the producing actor; ``observed_kinds``
    derives the ``adapter_output`` opened lineage from this record.
    The record stores the ordinal of the service call that performed
    the open (the crashed export/validate writer), so the emitted
    opened ref names the exact executed operation.
    """

    scenario_report.setdefault("adapter_output_opens", {})[actor] = \
        _current_ordinal(actor)


def _record_creation(scenario_report, kinds):
    """Record the producer operation ordinal that created artifact kinds.

    Called at the creation call site once the observable marker proves
    the call's durable work created the artifacts; ``observed_kinds``
    emits the created refs from these records, so a kind is credited to
    the operation that actually produced it, never to a hardcoded
    first call.  Scenarios whose v4 main came from the external
    v4-fixture tool keep recording no v4_main creation
    (``fixture_created_main``).
    """

    ordinal = _current_ordinal("producer")
    for kind in kinds:
        scenario_report.setdefault("created_ordinals", {})[kind] = ordinal


def _opened_by_actors(scenario_report, field):
    """``actor.<ordinal>`` opened refs for every actor that recorded an open.

    The ordinal is the index of the service call that performed the
    open in the actor's recorded operation list (the call sites store
    it via ``_record_live_open``/``_record_adapter_open``), so each
    lineage ref names the exact executed operation, never a hardcoded
    first call.  Pre-ordinal evidence records (plain ``True``) keep
    the legacy ``actor.0`` convention.
    """

    refs = []
    opens = scenario_report.get(field) or {}
    for actor in ("producer", "consumer"):
        ordinal = opens.get(actor)
        if ordinal is True:
            refs.append(f"{actor}.0")
        elif isinstance(ordinal, int) and not isinstance(ordinal, bool):
            refs.append(f"{actor}.{ordinal}")
    return refs


def observed_kinds(scenario_report):
    """Per-kind actor lineage recorded by one crash scenario.

    The kind gate consumes exactly this shape: each observed artifact
    kind maps to ``{"created_by": [...], "opened_by": [...]}`` in the
    matrix-case format.  ``created_by`` is normally the scenario's
    producer actor (``["producer.0"]``; the producer builds the main
    and the crashed attempt).  Scenarios whose v4 main was created by
    the external v4-fixture tool (B and D, ``fixture_created_main``)
    record no product creator ref for ``v4_main``: crediting the
    producer would be a false attribution, and the kind coverage is
    satisfied by the publish scenarios (A1, A2, E, F).

    Opened lineage is DERIVED from the scenario's recorded outcomes,
    never hardcoded:

    - ``v4_main``: ``["consumer.0"]`` when the consumer opened the
      main (``probe_consumer_open`` or the scenario's consumer
      reader.open succeeded; see ``_consumer_opened_main``).
    - ``live_sidecar``: every actor recorded in ``live_reader_opens``.
      A live reader open registers in and writes the
      ``<main>.readers`` sidecar reader table in both engines, so the
      live-transition scenarios record the resolver's
      ``database.info`` live open (producer) and the consumer's live
      ``reader.open`` (consumer) at their call sites.
    - ``adapter_output``: every actor recorded in
      ``adapter_output_opens``.  Scenario E records the producer:
      the interrupted attempt's export writer opened its private
      adapter-output temp (O_EXCL create+open in both engines) and
      the retry's writer opens its own the same way.  The consumer
      never opens adapter outputs -- they are plain text, not v4
      mains, so ``reader.open`` cannot open them; the scenario's
      consumer probe targets the v4 main source.

    The kind-universe coverage follows the declarative matrices' file
    ledgers: publication crashes observe the retained reservation and
    private publication output, the live-transition crashes observe
    the sidecar, and the recovery crash observes authorized scratch.

    Lineage ordinals are recorded at the call sites
    (``_record_creation`` for creations, ``_record_live_open`` /
    ``_record_adapter_open`` / the consumer main-open recording for
    opens) and name the exact executed operation of the producer /
    consumer operation lists, never a hardcoded first call.  A
    scenario that fails to record the ordinal it needs emits no ref
    for that credit, so a harness gap is a loud missing-lineage
    defect, not a silently wrong ``.0`` ref.
    """

    def kind(kind_name, opened):
        created = []
        if not (kind_name == "v4_main"
                and scenario_report.get("fixture_created_main")):
            ordinal = (scenario_report.get("created_ordinals") or {}).get(
                kind_name)
            if isinstance(ordinal, int) and not isinstance(ordinal, bool):
                created = [f"producer.{ordinal}"]
        opened_by = []
        main_open_ordinal = (
            scenario_report.get("reopen_outcome") or {}).get(
            "consumer_main_open_ordinal")
        if (opened and isinstance(main_open_ordinal, int)
                and not isinstance(main_open_ordinal, bool)):
            opened_by = [f"consumer.{main_open_ordinal}"]
        return {"created_by": created, "opened_by": opened_by}

    kinds = {}
    state = scenario_report.get("destination_state") or {}
    if state.get("reservation_basenames") or state.get("reservation_basename"):
        kinds["publication_reservation"] = kind(
            "publication_reservation", False)
    if state.get("publish_temp_basenames"):
        kinds["publication_temp"] = kind("publication_temp", False)
    if state.get("scratch_basenames"):
        kinds["authorized_scratch"] = kind("authorized_scratch", False)
    if state.get("class") in (
            "absent_after_crash", "attempt_complete_after_crash",
            "scratch_residue") or state.get("exists"):
        kinds["v4_main"] = kind("v4_main",
                                _consumer_opened_main(scenario_report))
    if state.get("class") in ("main_unchanged_sidecar_present",
                              "live_dataset_with_uncommitted_write"):
        kinds["live_sidecar"] = {
            "created_by": kind("live_sidecar", False)["created_by"],
            "opened_by": _opened_by_actors(
                scenario_report, "live_reader_opens")}
        kinds["v4_main"] = kind("v4_main",
                                _consumer_opened_main(scenario_report))
    if state.get("class") == "export_partial_output":
        kinds["adapter_output"] = {
            "created_by": kind("adapter_output", False)["created_by"],
            "opened_by": _opened_by_actors(
                scenario_report, "adapter_output_opens")}
        kinds["v4_main"] = kind("v4_main",
                                _consumer_opened_main(scenario_report))
    if state.get("class") == "validate_findings_aborted":
        kinds["v4_main"] = kind("v4_main",
                                _consumer_opened_main(scenario_report))
    return kinds


def main():
    parser = argparse.ArgumentParser(
        description="External process-crash harness for the iprange v1 "
                    "JSON-RPC product interface (milestone-4 W5 gate).")
    parser.add_argument("--producer", metavar="PATH", required=True,
                        help="absolute producer executable (iprange --jsonrpc)")
    parser.add_argument("--consumer", metavar="PATH", required=True,
                        help="absolute consumer executable (iprange --jsonrpc)")
    parser.add_argument("--fixture-tool", metavar="PATH", required=True,
                        help="absolute v4-fixture producer executable")
    parser.add_argument("--work-dir", metavar="DIR", required=True,
                        help="absolute existing root; every scenario runs in "
                             "a fresh unique subdirectory")
    parser.add_argument("--json-report", metavar="PATH",
                        help="write the JSON crash report to this file")
    parser.add_argument("--keep-work", action="store_true",
                        help="keep per-scenario work directories (default: "
                             "remove them after the run)")
    args = parser.parse_args()

    if not os.path.isdir(args.work_dir) or not os.path.isabs(args.work_dir):
        parser.error("--work-dir must be an absolute existing directory")
    try:
        _orphan_contract_self_test()
    except AssertionError as exc:
        parser.error(f"export-orphan negative control failed: {exc}")
    producer = executable(args.producer, "producer binary")
    consumer = executable(args.consumer, "consumer binary")
    fixture_tool = executable(args.fixture_tool, "fixture tool")

    report = {
        "schema": "iprange-cli-crash-report-v1",
        "command": sys.argv,
        "platform": {
            "system": platform.system(),
            "release": platform.release(),
            "machine": platform.machine(),
            "python": platform.python_version(),
        },
        "binaries": {"producer": producer, "consumer": consumer,
                     "fixture_tool": fixture_tool,
                     "producer_sha256": sha256_file(producer),
                     "consumer_sha256": sha256_file(consumer),
                     "fixture_tool_sha256": sha256_file(fixture_tool)},
        "scenarios": [],
        "leftover_processes": [],
        "failed": 0,
    }

    failed = 0
    work_dirs = []
    producer_impl = product_implementation(producer, "producer")
    consumer_impl = product_implementation(consumer, "consumer")
    pairs = (
        (producer_impl, consumer_impl, producer, consumer),
        (consumer_impl, producer_impl, consumer, producer),
    )
    for producer_name, consumer_name, producer_bin, consumer_bin in pairs:
        direction = f"{producer_name}->{consumer_name}"
        _ACTOR_BINARIES["producer"] = os.path.realpath(producer_bin)
        _ACTOR_BINARIES["consumer"] = os.path.realpath(consumer_bin)
        # Per-scenario binary identity: every scenario records the exact
        # sha256 of the producer and consumer binaries it executes, so
        # the kind gate can validate crash identities against the
        # cross-report sha->implementation map instead of trusting the
        # scenario direction label.
        producer_sha = sha256_file(producer_bin)
        consumer_sha = sha256_file(consumer_bin)
        work_base = os.path.join(
            args.work_dir, f"{producer_name}-{consumer_name}")
        for name, runner in (
                ("A1", lambda report, w=work_base, p=producer_bin,
                 c=consumer_bin: scenario_a1(
                     direction, p, c, w, report)),
                ("A2", lambda report, w=work_base, p=producer_bin,
                 c=consumer_bin: scenario_a2(
                     direction, p, c, w, report)),
                ("A3", lambda report, w=work_base, p=producer_bin,
                 c=consumer_bin: scenario_a3(
                     direction, p, c, w, fixture_tool, report)),
                ("B", lambda report, w=work_base, p=producer_bin,
                 c=consumer_bin: scenario_b(
                     direction, p, c, w, fixture_tool, report)),
                ("C", lambda report, w=work_base, p=producer_bin,
                 c=consumer_bin: scenario_c(
                     direction, p, c, w, report)),
                ("D", lambda report, w=work_base, p=producer_bin,
                 c=consumer_bin: scenario_d(
                     direction, p, c, w, fixture_tool, report)),
                ("E", lambda report, w=work_base, p=producer_bin,
                 c=consumer_bin: scenario_e(
                     direction, p, c, w, report)),
                ("F", lambda report, w=work_base, p=producer_bin,
                 c=consumer_bin: scenario_f(
                     direction, p, c, w, report))):
            scenario_report = {
                "scenario": f"{name}.{direction}",
                "producer": f"{producer_name}:{producer_bin}",
                "producer_sha256": producer_sha,
                "consumer": f"{consumer_name}:{consumer_bin}",
                "consumer_sha256": consumer_sha,
                "marker_seen_ms": None,
                "kill_method": "SIGKILL process group at observable "
                             "process-crash marker",
                "destination_state": None,
                "inspect_outcome": None,
                "resolve_outcome": None,
                "residue_bounded": None,
                "reopen_outcome": None,
                "pass": False,
                "assertions": [],
                "failures": [],
                "kinds": {},
                # Additive lineage evidence: per-role executed operation
                # lists and per-role live-reader / adapter-output opens
                # (the kind gate validates lineage refs against these).
                # fixture_created_main: truthfully records that the
                # scenario's v4 main was produced by the external
                # v4-fixture tool (scenarios B and D), so no product
                # creator ref is recorded for it.
                "fixture_created_main": False,
                "operations": {"producer": [], "consumer": []},
                "live_reader_opens": {},
                "adapter_output_opens": {},
            }
            OPERATIONS["producer"] = []
            OPERATIONS["consumer"] = []
            work_dirs.append(work_base)
            try:
                runner(scenario_report)
                scenario_report["pass"] = True
                scenario_report["kinds"] = observed_kinds(scenario_report)
                print(f"PASS {scenario_report['scenario']}")
            except (ScenarioFailure, AssertionError, OSError,
                    ValueError) as exc:
                failed += 1
                scenario_report["failures"].append(str(exc))
                print(f"FAIL {scenario_report['scenario']}: {exc}")
            scenario_report["operations"] = {
                "producer": list(OPERATIONS["producer"]),
                "consumer": list(OPERATIONS["consumer"])}
            report["scenarios"].append(scenario_report)

    leftover = no_leftover_processes()
    report["leftover_processes"] = leftover
    if leftover:
        failed += 1
        print(f"FAIL leftover product processes: {leftover}")
    report["failed"] = failed

    if not args.keep_work:
        for work in work_dirs:
            shutil.rmtree(work, ignore_errors=True)

    if args.json_report:
        with open(args.json_report, "w", encoding="utf-8") as stream:
            json.dump(report, stream, indent=2, sort_keys=True)
            stream.write("\n")

    total = len(report["scenarios"])
    print(f"{total - failed} passed, {failed} failed "
          f"({total} crash scenarios)")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
