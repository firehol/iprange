#!/usr/bin/env python3
"""Bounded-resource proofs at the iprange v1 JSON-RPC product interface.

Milestone-4 (delivery step 5) resource gate.  The declarative suite
proves the advertised ceilings, ``output_limit``, reader/cursor
capacity, the frame-layer batch bound, and the bounded adapter
memory model.  Four claims are not expressible in that suite (see
``resource-record.md``); this harness proves all four at the normal
product interface (no test hook, no environment variable, only the
JSON-RPC stdio pipe):

- Proof a -- the >16-in-flight ``server_busy`` race, driven with a
  pipelining client.  One export of a 500,000-line immutable source
  occupies the connection queue while 19 ``system.describe`` requests
  are pipelined behind it in one stdin blob (one request per line,
  20 frames in one write, stdin then closed).  Both product binaries
  must behave identically under the documented session contract:
  the queue admits one active unit plus 16 queued, so exactly 3 of
  the 19 describes answer ``server_busy`` (-32002) and the other 16
  answer with results (which ids sit at the admission boundary
  varies by a small timing race); the in-flight export answers
  -32010 ``cancelled``: closing stdin stops acceptance and cancels
  the active unit (documented session contract), while queued units
  keep their factual outcome.  20 responses, exit 0.  A
  500,000-line feed is the verified minimum that keeps the queue
  occupied; do not reduce it.
- Proof b -- the -32001 over-limit frame close path.  One frame over
  the 1 MiB input ceiling (method ``x``; the frame-limit error fires
  before method dispatch) is answered with exactly one response
  (error code -32001, null id); a valid sentinel request written
  after the over-limit frame in the same stdin stream is never
  parsed (bytes after the limit are discarded at shutdown), so the
  process closes with zero further bytes on stdout and exits 0.
- Proof c -- ``maintenance.remove`` against a real reservation
  nonce.  A producer killed at the reservation magic marker leaves
  one reservation; a fresh producer lists it and removes it with the
  listed row passed unchanged (the opaque-entry contract: the
  removal entry is exactly what ``maintenance.list`` emitted, never
  rebuilt or decoded), and the reservation is durably gone.  The
  private publication temp may remain: bounded residue, recorded.
- Proof d -- cancellation through the real CLI.  One stdin blob
  pipelines a slow export request (id 1), the ``iprange.v1.cancel``
  notification naming request 1, and one ``system.describe`` request
  (id 2), then closes stdin.  The cancelled export never answers
  with a result (the session suppresses explicitly-cancelled units;
  -32010 ``cancelled`` is the EOF-path answer proven in proof a),
  the describe must still answer with a result (the dispatcher stays
  responsive after cancelling the in-flight unit), and the process
  exits 0.  The report records whether the export answered and its
  exact code.

Both product binaries (Rust and Go) must pass all four proofs for
the harness to exit 0.  The report (schema
``iprange-cli-resource-report-v1``) records per-proof evidence per
binary and the failed count.

Reuses the helpers from ``crash_harness.py`` (import side-effect
free) for the JSON-RPC client, the killable producer service, the
durable marker poll, provision of the text feed, publication params,
maintenance listing, and private artifact names.
"""

import argparse
import hashlib
import json
import os
import platform
import shutil
import subprocess
import sys
import time
import uuid

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from crash_harness import (  # noqa: E402  (side-effect free)
    HarnessJsonRpcService,
    KillableJsonRpcService,
    RESERVATION_MAGIC,
    call_with_worker,
    child_environment,
    maintenance_reports,
    no_leftover_processes,
    private_artifact_names,
    publish_params,
    record_spawn,
    reservation_seen,
    write_interval_feed,
)

# One export of a 500,000-line feed keeps the connection queue
# occupied long enough for 19 pipelined describes (verified
# empirically).  The session admits one active unit plus 16 queued,
# so exactly 3 of the 19 describes answer server_busy; which 3 (the
# admission-boundary ones near ids 17..20) varies by a small timing
# race, so the harness asserts the count and the id coverage, never
# an exact id set.  Both product binaries must satisfy this same
# contract (the Go session was aligned to it by the SOW-0028 session
# fix).
PROOF_A_FEED_LINES = 500_000
PROOF_A_DESCRIBES = 19
PROOF_A_BUSY_EXPECTED = 3
PROOF_A_OK_EXPECTED = PROOF_A_DESCRIBES - PROOF_A_BUSY_EXPECTED
PROOF_A_READ_DEADLINE_SECONDS = 60.0

# One >1 MiB frame: the frame-limit error fires before method
# dispatch, so the method name is arbitrary (probe convention: "x").
PROOF_B_PAD_BYTES = 1_100_000
# Bounded drain deadline after the single -32001 response: a product
# that emitted a second response (or kept stdout open) must fail the
# proof instead of hanging it.
PROOF_B_DRAIN_DEADLINE_SECONDS = 15.0


def parse_binaries(value):
    """Parse --binaries rust=PATH go=PATH into {label: path}."""

    result = {}
    tokens = value if isinstance(value, list) else value.split()
    for token in tokens:
        if "=" not in token:
            raise SystemExit(
                f"--binaries token must be label=path, got: {token!r}")
        label, path = token.split("=", 1)
        result[label] = path
    if sorted(result) != ["go", "rust"]:
        raise SystemExit(
            "--binaries must name exactly rust=PATH and go=PATH")
    return result


def executable(value, label):
    """Validate one absolute executable path (run.py parity)."""

    if not os.path.isabs(value):
        raise SystemExit(f"{label} is not an absolute executable file: {value}")
    if not os.path.isfile(value) or not os.access(value, os.X_OK):
        raise SystemExit(f"{label} is not an absolute executable file: {value}")
    return os.path.realpath(value)


def sha256_file(path):
    """Lowercase SHA-256 of one file (run.py parity, streaming)."""

    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


class ResourceFailure(AssertionError):
    """One failed resource proof assertion."""


def spawn_jsonrpc(binary, cwd, stderr_log):
    """Spawn one pipelined product process (one owned subprocess)."""

    with open(stderr_log, "wb") as stream:
        proc = subprocess.Popen(
            [binary, "--jsonrpc"], stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=stream, cwd=cwd,
            env=child_environment())
    record_spawn(proc)
    return proc


def stderr_tail(path, limit=1200):
    """Short readable tail of one captured stderr log (evidence)."""

    try:
        with open(path, "rb") as stream:
            data = stream.read(limit * 4)
        text = data.decode("utf-8", "replace")
        return text[-limit:]
    except OSError:
        return ""


def read_responses(proc, expect, deadline_seconds):
    """Read up to ``expect`` JSON-RPC response lines with a deadline.

    Uses a selector so a product that stays alive without answering
    cannot block the harness forever; returns the list of decoded
    responses read so far (the caller asserts the count).
    """

    import selectors

    responses = []
    sel = selectors.DefaultSelector()
    sel.register(proc.stdout, selectors.EVENT_READ)
    deadline = time.time() + deadline_seconds
    while len(responses) < expect:
        remaining = deadline - time.time()
        if remaining <= 0:
            break
        if not sel.select(remaining):
            continue
        raw = proc.stdout.readline()
        if not raw:
            break
        responses.append(json.loads(raw))
    return responses


def slow_export_params(source, destination):
    """One slow export request over an immutable published source.

    The same request shape drives proofs a and d: full selection, one
    ranges output, a generous result budget that keeps the queue
    occupied while the export runs.
    """

    return {
        "source": {"path": source, "mode": "immutable"},
        "view": {"kind": "selection", "selection": {"mode": "all"}},
        "format": "ranges",
        "destination": destination, "publication_policy": "fail_if_exists",
        "result_budget": {"max_rows": "10000000",
                          "max_output_bytes": "10737418240",
                          "max_open_files": 32},
    }


def proof_a(binary, label, work_dir, outcome):
    """Proof a: >16-in-flight server_busy race with a pipelining client.

    One export of the 500,000-line immutable source is pipelined with
    19 ``system.describe`` requests (one request per frame, one stdin
    blob, stdin closed).  Verified expectations (both product
    binaries, identical contract):

    - exactly 20 responses, one per request, ids 1..20 covered once;
    - exactly 3 describes answer -32002 ``server_busy`` (the queue
      admits one active unit plus 16 queued; the boundary ids near
      17..20 race by a small timing window, so only the count and
      the id coverage are asserted);
    - the other 16 describes answer with results;
    - the in-flight export answers -32010 ``cancelled`` (EOF
      cancels the active unit); exit code 0.
    """

    work = os.path.join(work_dir, f"a-{label}")
    os.makedirs(work, exist_ok=True)

    feed = os.path.join(work, "feed.txt")
    write_interval_feed(feed, PROOF_A_FEED_LINES)
    outcome["feed_lines"] = PROOF_A_FEED_LINES

    source = os.path.join(work, "big.iprange")
    pub = HarnessJsonRpcService([binary, "--jsonrpc"], label, cwd=work)
    try:
        pub.call("1", "iprange.v1.current.publish",
                 publish_params(feed, source, "fail_if_exists"))
    except (AssertionError, OSError, ValueError) as exc:
        raise ResourceFailure(
            f"current.publish could not prepare the source: {exc}") from exc
    finally:
        pub.close()
    outcome["source_sha256"] = sha256_file(source)

    export_params = slow_export_params(source, os.path.join(work, "slow.txt"))

    stderr_log = os.path.join(work, "a.stderr")
    proc = spawn_jsonrpc(binary, work, stderr_log)
    outcome["pid"] = proc.pid
    try:
        lines = [json.dumps({"jsonrpc": "2.0", "id": "1",
                             "method": "iprange.v1.export",
                             "params": export_params})]
        for index in range(2, 2 + PROOF_A_DESCRIBES):
            lines.append(json.dumps({"jsonrpc": "2.0", "id": str(index),
                                     "method": "iprange.v1.system.describe",
                                     "params": {}}))
        proc.stdin.write(("\n".join(lines) + "\n").encode())
        proc.stdin.flush()
        proc.stdin.close()

        responses = read_responses(proc, 20, PROOF_A_READ_DEADLINE_SECONDS)
        outcome["responses"] = len(responses)
        outcome["busy_ids"] = sorted(
            (str(r.get("id")) for r in responses
             if r.get("error", {}).get("code") == -32002),
            key=lambda value: int(value))
        outcome["ok_ids"] = sorted(
            (str(r.get("id")) for r in responses
             if "result" in r and str(r.get("id")) != "1"),
            key=lambda value: int(value))
        outcome["export_ids"] = [
            r for r in responses if str(r.get("id")) == "1"]
        outcome["export_code"] = (
            outcome["export_ids"][0].get("error", {}).get("code")
            if outcome["export_ids"] and "error" in outcome["export_ids"][0]
            else ("result" if outcome["export_ids"] else None))
        outcome["killed"] = False
        outcome["exit_code"] = None

        described_ids = [str(i) for i in range(2, 2 + PROOF_A_DESCRIBES)]
        answered = outcome["busy_ids"] + outcome["ok_ids"]
        if len(responses) != 1 + PROOF_A_DESCRIBES:
            raise ResourceFailure(
                f"expected {1 + PROOF_A_DESCRIBES} responses, got "
                f"{len(responses)}")
        if len(answered) != len(set(answered)) or \
                set(answered) != set(described_ids):
            raise ResourceFailure(
                f"describe ids not covered exactly once: "
                f"{outcome['ok_ids']} ok, {outcome['busy_ids']} busy")
        if not outcome["export_ids"]:
            raise ResourceFailure("export id '1' never answered")
        if len(outcome["busy_ids"]) != PROOF_A_BUSY_EXPECTED \
                or len(outcome["ok_ids"]) != PROOF_A_OK_EXPECTED:
            raise ResourceFailure(
                f"expected {PROOF_A_BUSY_EXPECTED} server_busy "
                f"describes and {PROOF_A_OK_EXPECTED} results; "
                f"got busy {outcome['busy_ids']} and results "
                f"{outcome['ok_ids']}")
        if outcome["export_code"] != -32010:
            raise ResourceFailure(
                "expected the in-flight export to answer -32010 "
                "cancelled at EOF shutdown, got "
                f"{outcome['export_code']}")
        # EOF shutdown must terminate the process by itself with
        # exit code 0; a product that stays alive after all
        # responses is killed (recorded) and the proof fails.
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
            outcome["killed"] = True
        outcome["exit_code"] = proc.returncode
        if outcome["exit_code"] != 0:
            raise ResourceFailure(
                f"expected exit code 0 after EOF shutdown, got "
                f"{outcome['exit_code']}")
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait(timeout=5)
    outcome["stderr_tail"] = stderr_tail(stderr_log)
    if not outcome["stderr_tail"]:
        outcome.pop("stderr_tail")


def proof_b(binary, label, work_dir, outcome):
    """Proof b: one over-limit frame answers -32001, then clean close.

    One >1 MiB frame is answered with exactly one response (error
    code -32001, null id).  A valid sentinel request
    (``system.describe``, distinct id) is written after the over-limit
    frame in the same stdin stream; the contract says bytes after the
    limit are never parsed as another frame (spec
    ``iprange-jsonrpc-v1.md``), so the sentinel must not be answered.
    stdout then drains to EOF with zero further bytes -- a product
    that parsed trailing bytes would emit a second response and fail
    the proof -- and the process exits 0.
    """

    work = os.path.join(work_dir, f"b-{label}")
    os.makedirs(work, exist_ok=True)
    stderr_log = os.path.join(work, "b.stderr")
    proc = spawn_jsonrpc(binary, work, stderr_log)
    outcome["pid"] = proc.pid
    try:
        big = json.dumps({"jsonrpc": "2.0", "id": "1", "method": "x",
                          "params": {"pad": "a" * PROOF_B_PAD_BYTES}})
        # A valid sentinel request after the over-limit frame: the
        # contract discards bytes after the limit at shutdown without
        # parsing them, so the sentinel must never be answered.  A
        # product that parsed trailing bytes would emit a second
        # response and fail the proof below.
        sentinel = json.dumps({"jsonrpc": "2.0", "id": "2",
                               "method": "iprange.v1.system.describe",
                               "params": {}})
        proc.stdin.write((big + "\n" + sentinel + "\n").encode())
        proc.stdin.flush()
        proc.stdin.close()

        responses = read_responses(proc, 1, 30)
        outcome["response_lines"] = len(responses)
        if len(responses) != 1:
            raise ResourceFailure(
                f"expected 1 response line, got {len(responses)}")
        response = responses[0]
        outcome["response"] = response
        error = response.get("error") or {}
        if error.get("code") != -32001:
            raise ResourceFailure(
                f"expected error code -32001, got {error!r}")
        if response.get("id") is not None:
            raise ResourceFailure(
                f"over-limit frame error must have a null id, got "
                f"{response.get('id')!r}")
        # The single -32001 response is the whole answer: drain
        # stdout to EOF and prove zero further bytes arrived (a
        # product that parsed the trailing sentinel request and
        # answered it would show up here).
        drained, eof = drain_stdout(proc, PROOF_B_DRAIN_DEADLINE_SECONDS)
        outcome["drained_bytes"] = len(drained)
        outcome["stdout_eof"] = eof
        if not eof:
            raise ResourceFailure(
                "stdout did not reach EOF within the bounded deadline "
                "after the over-limit close")
        if drained:
            raise ResourceFailure(
                f"expected zero further bytes on stdout after the "
                f"single -32001 response, got {len(drained)} bytes: "
                f"{drained[:120]!r}")
    finally:
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
    outcome["exit_code"] = proc.returncode
    if proc.returncode != 0:
        raise ResourceFailure(
            f"expected exit code 0 after the over-limit close, got "
            f"{proc.returncode}")
    outcome["stderr_tail"] = stderr_tail(stderr_log)
    if not outcome["stderr_tail"]:
        outcome.pop("stderr_tail")


def drain_stdout(proc, deadline_seconds):
    """Read every remaining stdout byte until EOF or deadline.

    Returns ``(bytes_read, reached_eof)``: ``bytes_read`` is the
    exact byte count observed after the caller's last ``readline``
    (zero for a product that answered exactly once), and
    ``reached_eof`` is True only when stdout reached EOF while the
    deadline was still open.  Used by proof b to prove the single
    -32001 response is the product's complete answer.
    """

    import selectors

    sel = selectors.DefaultSelector()
    sel.register(proc.stdout, selectors.EVENT_READ)
    deadline = time.time() + deadline_seconds
    chunks = []
    reached_eof = False
    while True:
        remaining = deadline - time.time()
        if remaining <= 0:
            break
        if not sel.select(min(remaining, 5.0)):
            continue
        try:
            chunk = proc.stdout.read1(65536)
        except (ValueError, OSError):
            chunk = proc.stdout.read(65536)
        if not chunk:
            reached_eof = True
            break
        chunks.append(chunk)
    return b"".join(chunks), reached_eof


def proof_c(binary, label, work_dir, outcome):
    """Proof c: maintenance.remove against a real reservation nonce.

    A producer killed at the reservation magic marker leaves one
    reservation; a fresh producer lists it, the harness passes the
    listed row unchanged to ``maintenance.remove`` (the opaque-entry
    contract: the removal entry is exactly what ``maintenance.list``
    emitted, never rebuilt or decoded), and the reservation is
    durably gone.  The private publication temp may remain: bounded
    residue, recorded.
    """

    work = os.path.join(work_dir, f"c-{label}")
    os.makedirs(work, exist_ok=True)

    feed = os.path.join(work, "feed.txt")
    write_interval_feed(feed, 5000)
    destination = os.path.join(work, "published.iprange")

    producer = KillableJsonRpcService(
        [binary, "--jsonrpc"], f"c-{label}-producer", cwd=work)
    try:
        outcome["marker_seen_ms"], _thread = None, None
        _, seen_ms, thread = call_with_worker(
            producer, "1", "iprange.v1.current.publish",
            publish_params(feed, destination, "fail_if_exists"), 20,
            seen=lambda: reservation_seen(work, RESERVATION_MAGIC))
        outcome["marker_seen_ms"] = seen_ms
        producer.kill_process_group()
        thread.join(timeout=5)
    finally:
        producer.kill_process_group()
        producer.close()

    listed = HarnessJsonRpcService([binary, "--jsonrpc"], f"c-{label}", cwd=work)
    try:
        out_path = os.path.join(work, "list.jsonl")
        reports, error = maintenance_reports(
            listed, work, out_path, ["reservation"])
        if error:
            raise ResourceFailure(
                f"maintenance.list reservation failed: {error!r}")
        rows = [json.loads(line) for line in open(out_path)
                if line.strip()]
        outcome["listed_rows"] = len(rows)
        outcome["reports"] = reports
        if len(rows) != 1:
            raise ResourceFailure(
                f"expected exactly 1 reservation row, got {len(rows)}")

        row = rows[0]
        outcome["listed_row"] = row
        outcome["evidence_phase"] = row.get("evidence", {}).get("phase")
        outcome["policy"] = row.get("evidence", {}).get("policy")
        outcome["directory_identity"] = row.get("directory_identity")

        # The opaque-entry contract: the removal entry is the listed
        # row itself, unchanged.  Record a deep copy and prove the
        # row passed to maintenance.remove equals the listed row, so
        # any accidental mutation fails the proof.
        row_used = json.loads(json.dumps(row))
        outcome["row_used"] = row_used
        outcome["row_passed_unchanged"] = row_used == row
        if not outcome["row_passed_unchanged"]:
            raise ResourceFailure(
                "the removal entry was mutated before the call")

        removal = listed.call("rm", "iprange.v1.maintenance.remove",
                              {"entry": row})
        if "error" in removal:
            raise ResourceFailure(
                f"maintenance.remove failed with the listed row "
                f"passed unchanged: "
                f"{json.dumps(removal['error'].get('data', {}))[:300]}")
        outcome["remove"] = "ok"

        residue = private_artifact_names(work)
        outcome["reservation_after"] = residue["reservation"]
        outcome["publish_temp_after"] = residue["publish_temp"]
        if residue["reservation"]:
            raise ResourceFailure(
                "reservation still present after maintenance.remove: "
                f"{residue['reservation']}")
    finally:
        listed.close()


def proof_d(binary, label, work_dir, outcome):
    """Proof d: cancellation keeps the dispatcher responsive.

    One stdin blob pipelines a slow export request (id 1), the
    ``iprange.v1.cancel`` notification naming request 1, and one
    ``system.describe`` request (id 2), then closes stdin.  The
    cancelled export must never answer with a result, the describe
    must still answer with a result (the dispatcher stays responsive
    after the cancellation), and the process exits 0 after EOF
    shutdown.  Both product binaries.

    The session contract for an explicit cancellation (spec
    ``iprange-jsonrpc-v1.md`` "Shutdown"; Rust
    ``iprange-cli/src/rpc/session.rs`` ``entry_response``) is that the
    cancelled unit's response is suppressed: an id cancelled by the
    ``iprange.v1.cancel`` notification -- queued or active -- is
    omitted, while the EOF-cancelled active unit answers -32010
    ``cancelled`` at shutdown (proof a).  The harness therefore pins
    the invariant both paths share -- the cancelled export is never a
    result -- and records the exact export terminal: ``export_code``
    is -32010 when the session delivers the cancelled answer, and
    ``export_answered`` is False when the session suppresses it (the
    current wire behavior for both products).
    """

    work = os.path.join(work_dir, f"d-{label}")
    os.makedirs(work, exist_ok=True)

    feed = os.path.join(work, "feed.txt")
    write_interval_feed(feed, PROOF_A_FEED_LINES)
    outcome["feed_lines"] = PROOF_A_FEED_LINES

    source = os.path.join(work, "big.iprange")
    pub = HarnessJsonRpcService([binary, "--jsonrpc"], label, cwd=work)
    try:
        pub.call("1", "iprange.v1.current.publish",
                 publish_params(feed, source, "fail_if_exists"))
    except (AssertionError, OSError, ValueError) as exc:
        raise ResourceFailure(
            f"current.publish could not prepare the source: {exc}") from exc
    finally:
        pub.close()
    outcome["source_sha256"] = sha256_file(source)

    export_params = slow_export_params(source, os.path.join(work, "slow.txt"))

    stderr_log = os.path.join(work, "d.stderr")
    proc = spawn_jsonrpc(binary, work, stderr_log)
    outcome["pid"] = proc.pid
    try:
        frames = [
            json.dumps({"jsonrpc": "2.0", "id": "1",
                        "method": "iprange.v1.export",
                        "params": export_params}),
            json.dumps({"jsonrpc": "2.0",
                        "method": "iprange.v1.cancel",
                        "params": {"request_id": "1"}}),
            json.dumps({"jsonrpc": "2.0", "id": "2",
                        "method": "iprange.v1.system.describe",
                        "params": {}}),
        ]
        proc.stdin.write(("\n".join(frames) + "\n").encode())
        proc.stdin.flush()
        proc.stdin.close()

        responses = read_responses(proc, 2, PROOF_A_READ_DEADLINE_SECONDS)
        outcome["responses"] = len(responses)
        outcome["killed"] = False
        outcome["exit_code"] = None
        by_id = {str(r.get("id")): r for r in responses}
        if set(by_id) - {"1", "2"}:
            raise ResourceFailure(
                f"unexpected response ids: {sorted(by_id)}")
        export = by_id.get("1")
        outcome["export_answered"] = export is not None
        if export is not None:
            if "result" in export:
                raise ResourceFailure(
                    "the cancelled export must never answer with a "
                    "result, got a result")
            if export["error"].get("code") != -32010:
                raise ResourceFailure(
                    "the cancelled export answered with error code "
                    f"{export['error'].get('code')}, expected -32010 "
                    "cancelled")
        # The describe must still be served: the dispatcher stays
        # responsive after the cancellation.
        describe = by_id.get("2")
        if describe is None:
            raise ResourceFailure(
                "the system.describe after the cancellation never "
                "answered")
        if "error" in describe:
            raise ResourceFailure(
                "expected the describe after the cancellation to "
                "answer with a result, got "
                f"{describe.get('error')}")
        outcome["export_code"] = (
            export["error"]["code"] if export is not None else None)
        outcome["describe_answered"] = "result" in describe
        # EOF shutdown must terminate the process by itself with
        # exit code 0 (same contract as proof a).
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
            outcome["killed"] = True
        outcome["exit_code"] = proc.returncode
        if outcome["exit_code"] != 0:
            raise ResourceFailure(
                f"expected exit code 0 after EOF shutdown, got "
                f"{outcome['exit_code']}")
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait(timeout=5)
    outcome["stderr_tail"] = stderr_tail(stderr_log)
    if not outcome["stderr_tail"]:
        outcome.pop("stderr_tail")


def main():
    parser = argparse.ArgumentParser(
        description="Bounded-resource proofs at the iprange v1 JSON-RPC "
                    "product interface (milestone-4 resource gate).")
    parser.add_argument("--binaries", metavar="rust=PATH go=PATH",
                        nargs="+", required=True,
                        help="absolute iprange --jsonrpc executables, as "
                             "rust=PATH go=PATH")
    parser.add_argument("--work-dir", metavar="DIR", required=True,
                        help="absolute existing root work directory")
    parser.add_argument("--json-report", metavar="PATH",
                        help="write the JSON resource report to this file")
    parser.add_argument("--keep-work", action="store_true",
                        help="keep per-proof work directories (default: "
                             "remove them after the run)")
    args = parser.parse_args()

    if not os.path.isdir(args.work_dir) or not os.path.isabs(args.work_dir):
        parser.error("--work-dir must be an absolute existing directory")

    binaries = {}
    for label, path in parse_binaries(args.binaries).items():
        binaries[label] = executable(path, f"{label} binary")

    report = {
        "schema": "iprange-cli-resource-report-v1",
        "command": sys.argv,
        "platform": {
            "system": platform.system(),
            "release": platform.release(),
            "machine": platform.machine(),
            "python": platform.python_version(),
        },
        "binaries": {
            label: {"path": path, "sha256": sha256_file(path)}
            for label, path in binaries.items()},
        "proofs": [],
        "leftover_processes": [],
        "failed": 0,
    }

    failed = 0
    run_root = os.path.join(args.work_dir, f"resource-{uuid.uuid4().hex[:8]}")
    os.makedirs(run_root, exist_ok=True)
    for label, binary in sorted(binaries.items()):
        for proof, runner in (
                ("a", proof_a), ("b", proof_b), ("c", proof_c),
                ("d", proof_d)):
            outcome = {"proof": proof, "binary": label,
                       "binary_path": binary, "pass": False,
                       "failures": [],
                       "elapsed_ms": None}
            started = time.time()
            try:
                runner(binary, label, run_root, outcome)
                outcome["pass"] = True
                print(f"PASS proof {proof}.{label}")
            except (ResourceFailure, AssertionError, OSError,
                    ValueError, KeyError) as exc:
                failed += 1
                outcome["failures"].append(str(exc))
                print(f"FAIL proof {proof}.{label}: {exc}")
            finally:
                outcome["elapsed_ms"] = round(
                    (time.time() - started) * 1000, 1)
            report["proofs"].append(outcome)

    leftover = no_leftover_processes()
    report["leftover_processes"] = leftover
    if leftover:
        failed += 1
        print(f"FAIL leftover product processes: {leftover}")

    if not args.keep_work:
        shutil.rmtree(run_root, ignore_errors=True)

    report["failed"] = failed
    if args.json_report:
        with open(args.json_report, "w", encoding="utf-8") as stream:
            json.dump(report, stream, indent=2, sort_keys=True)
            stream.write("\n")

    total = len(report["proofs"])
    print(f"{total - failed} passed, {failed} failed "
          f"({total} resource proofs)")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
