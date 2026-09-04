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

Design (verified empirically against both product binaries before this
file was written; the exact outcome values below are the observed
wire values):

- Scenario A1 -- ``current.publish`` (fail_if_exists) killed once the
  publication reservation ``.iprange-reservation-<id>.tmp`` carries a
  valid state-1 block: the destination main does not exist after the
  kill (no partial replacement), and the reservation plus the private
  publication output ``.iprange-publish-<id>.tmp`` are the bounded
  residue.  A fresh producer resolves with the retained reservation as
  the sole authority: ``publication.resolve`` completes the interrupted
  publication and reports ``publication == "published"`` and
  ``destination_content == "desired"``; the final destination is the
  exact complete output recorded in the reservation (SHA-512 match).
  The consumer binary cannot open the absent destination before
  resolution (``invalid_path``/``not_started``) and reads the complete
  file after resolution.
- Scenario A2 -- ``current.publish`` (replace_existing) over an
  existing immutable main, killed at the same durable marker: the
  destination is byte-identical to the prior file, absent, or the
  complete new output recorded in the reservation (atomic namespace
  publication; a partial file is impossible).  ``inspect`` truthfully
  reconstructs the attempt from the retained reservation, ``resolve``
  completes it, residue is bounded and then absent.
- Scenario B -- ``database.initialize_live`` on an immutable v4
  fixture, killed as soon as the canonical ``<main>.readers`` sidecar
  exists with its valid creating-state (0) header.  The main is
  unchanged after the kill; the sidecar is the only residue.  Both
  ``database.info`` modes truthfully refuse before resolution
  (``wrong_state``); ``database.live_residue.resolve`` (the resultless
  resolver; the in-memory transition result was lost with the killed
  process) advances the state-0 sidecar to ready and reports
  ``status == "completed"``, ``kind == "canonical"`` and
  ``residue_possible == false``; a live reader reopens successfully.

Every scenario runs in both directions (producer Rust with consumer Go,
producer Go with consumer Rust).  The report (schema
``iprange-cli-crash-report-v1``) records per-scenario marker timing,
kill method, destination state, inspect/resolve/reopen outcomes,
bounded-residue evidence, and the pass verdict.  Exit status is 0
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

# Every product process this harness spawns is recorded here so the
# final leftover check can match exactly our own PIDs (never a
# pkill-style pattern).
SPAWNED_PIDS = []


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
    ``start_new_session=True`` so the harness can terminate the whole
    process group with a targeted ``os.killpg`` (never pkill/killall).
    Every response and the close path behave exactly like the normal
    client.
    """

    def __init__(self, argv, implementation, *, cwd=None):
        super().__init__(argv, implementation, cwd=cwd,
                         start_new_session=True)
        record_spawn(self.proc)

    def kill_process_group(self):
        """SIGKILL exactly the process group this service spawned."""

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

    def __init__(self, argv, implementation, *, cwd=None):
        super().__init__(argv, implementation, cwd=cwd)
        record_spawn(self.proc)


class ScenarioFailure(AssertionError):
    """One failed assertion inside a crash scenario."""


def call_with_worker(service, request_id, method, params, deadline, seen):
    """Issue one request and observe durable markers while it runs.

    ``service.call`` runs in a worker thread (the killed producer never
    answers).  ``seen`` is a poll callback with no arguments that
    returns True once the durable marker of the operation appears.
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
    started = time.time()
    thread.start()
    seen_ms = None
    while time.time() - started < deadline:
        if seen():
            seen_ms = (time.time() - started) * 1000
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


def probe_consumer_open(consumer, work, dest, scenario_report, expect_refusal):
    """Design step 6: consumer reader.open on the crash-left destination.

    With an absent destination the open must truthfully refuse
    (``invalid_path``/``not_started``): a half-published file never
    opens.  With an existing complete destination the open succeeds and
    the reader is closed again immediately, because an open immutable
    reader holds the destination lifetime lock and would block the
    producer's resolver.
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
        consumer_service.call("rc", "iprange.v1.reader.close",
                              {"reader": reader})
        scenario_report["reopen_outcome"] = {
            "before_resolution": {
                "opened_complete_destination": True,
                "reader_closed": True},
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
                "durable reservation marker was not observed; "
                f"worker outcome={outcome}")
        scenario_report["marker_seen_ms"] = round(seen_ms, 1)
        producer_service.kill_process_group()
        thread.join(timeout=5)

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
                "durable reservation marker was not observed; "
                f"worker outcome={outcome}")
        scenario_report["marker_seen_ms"] = round(seen_ms, 1)
        producer_service.kill_process_group()
        thread.join(timeout=5)

        residue = private_artifact_names(work)
        assert_truthful(
            len(residue["reservation"]) == 1,
            f"exactly one reservation must remain, got {residue['reservation']}",
            scenario_report)
        dest_state = classify_destination(
            dest, prior_sha256, prior_sha512,
            reservation_output_sha512(work))
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


def crc32c(data):
    """CRC-32C (reflected Castagnoli), the exact v4 checksum."""

    poly = 0x82F63B78
    table = []
    for index in range(256):
        value = index
        for _ in range(8):
            value = (value >> 1) ^ poly if value & 1 else value >> 1
        table.append(value)
    crc = 0xFFFFFFFF
    for byte in data:
        crc = (crc >> 8) ^ table[(crc ^ byte) & 0xFF]
    return crc ^ 0xFFFFFFFF


def scratch_header_authentic(path):
    """True when a scratch file carries its complete CRC-valid header.

    binary-format-v4.md section 20.3: the 128-byte ownership header is
    complete only when its CRC-32C field (last 4 bytes, computed over
    the whole header with the field zeroed) validates.  An
    unauthenticated partial header can never be removed by the engine
    API, so the crash marker must wait for the durable complete
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
    """Poll callback: True when an authorized scratch file is durable.

    The file must carry its complete authenticated ownership header:
    a partial header would leave an unremovable lookalike after the
    kill, which is not the durable-marker contract being tested.
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
    authorized scratch (durable within the operation) while the fixed
    structures still fit; a larger heap completes without scratch and a
    smaller one aborts before the tables are built.
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
                "durable reservation marker was not observed; "
                f"worker outcome={outcome}")
        scenario_report["marker_seen_ms"] = round(seen_ms, 1)
        producer_service.kill_process_group()
        thread.join(timeout=5)
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
    the harness kills the producer while that durable marker is live.
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




def observed_kinds(scenario_report):
    """Mechanical artifact-kind evidence recorded by one crash scenario.

    The gate battery derives the kind-universe coverage from these
    per-scenario lists together with the declarative matrices' file
    ledgers: publication crashes observe the retained reservation and
    private publication output, the live-transition crash observes the
    sidecar, and the recovery crash observes authorized scratch.
    """

    kinds = []
    state = scenario_report.get("destination_state") or {}
    if state.get("reservation_basenames"):
        kinds.append("publication_reservation")
    if state.get("reservation_basename"):
        kinds.append("publication_reservation")
    if state.get("publish_temp_basenames"):
        kinds.append("publication_temp")
    if state.get("scratch_basenames"):
        kinds.append("authorized_scratch")
    if state.get("class") in (
            "absent_after_crash", "attempt_complete_after_crash",
            "scratch_residue") or state.get("exists"):
        kinds.append("v4_main")
    if state.get("class") == "main_unchanged_sidecar_present":
        kinds.append("live_sidecar")
        kinds.append("v4_main")
    return sorted(set(kinds))


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
                     direction, p, c, w, report))):
            scenario_report = {
                "scenario": f"{name}.{direction}",
                "producer": f"{producer_name}:{producer_bin}",
                "consumer": f"{consumer_name}:{consumer_bin}",
                "marker_seen_ms": None,
                "kill_method": "SIGKILL process group at durable marker",
                "destination_state": None,
                "inspect_outcome": None,
                "resolve_outcome": None,
                "residue_bounded": None,
                "reopen_outcome": None,
                "pass": False,
                "assertions": [],
                "failures": [],
                "kinds": [],
            }
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
            report["scenarios"].append(scenario_report)

    leftover = no_leftover_processes()
    report["leftover_processes"] = leftover
    if leftover:
        failed += 1
        print(f"FAIL leftover product processes: {leftover}")

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
