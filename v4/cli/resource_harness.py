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
  process closes with zero further bytes on stdout and exits
  non-zero (the frame-limit path is a startup/framing failure; the
  session contract ``iprange-jsonrpc-v1.md`` mandates a non-zero
  exit when the service cannot continue).
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

Every stdio exchange with a product process is bounded by a
monotonic deadline.  Request payloads are written by
``write_all_bounded`` (non-blocking stdin, select-on-write,
``ResourceFailure`` when a child stops draining stdin instead of a
blocking ``write`` that fills the 64 KiB pipe forever); responses
are read by ``read_responses`` (non-blocking stdout, select-on-read,
only complete LF-terminated lines are decoded -- never
``readline()``, which blocks while a child holds a partial line
open; bytes that are not part of a returned response line are
retained for ``drain_stdout`` accounting and never parsed).
``--self-test`` runs two stub-child negative controls proving both
deadline bypasses now fail.

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
import signal
import subprocess
import sys
import tempfile
import time
import uuid

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from schema import frame  # noqa: E402  (shared response validator)
import run  # noqa: E402  (side-effect free; normal JSON-RPC client)

from crash_harness import (  # noqa: E402  (side-effect free)
    HarnessJsonRpcService,
    KillableJsonRpcService,
    RESERVATION_MAGIC,
    call_with_worker,
    child_environment,
    export_temp_basenames,
    maintenance_reports,
    no_leftover_processes,
    private_artifact_names,
    publish_params,
    record_spawn,
    reservation_seen,
    write_interval_feed,
)

# One export of a 500,000-line feed keeps the connection queue
# occupied long enough for 19 pipelined describes.  The harness
# first writes the export frame alone and waits for the export's
# private ``.<handle>.export.tmp`` (the durable marker that the
# export member is executing and its queue slot was already
# decremented), and only then pipelines the 19 describes: admission
# deterministically sees one active unit plus 16 free queue slots,
# so exactly 3 of the 19 describes answer server_busy.  Without the
# marker wait, a worker delayed by CPU contention can leave the
# export counted as a queued unit and shift one more describe into
# server_busy (observed on Go), so the count itself would race.
PROOF_A_FEED_LINES = 500_000
PROOF_A_DESCRIBES = 19
PROOF_A_BUSY_EXPECTED = 3
# Deadline for the export's private temp to appear after the export
# frame is written (the export start marker; appears in milliseconds
# on both products, 30 s is a generous bound for loaded machines).
PROOF_A_EXPORT_START_DEADLINE_SECONDS = 30.0
PROOF_A_OK_EXPECTED = PROOF_A_DESCRIBES - PROOF_A_BUSY_EXPECTED
PROOF_A_READ_DEADLINE_SECONDS = 60.0

# One >1 MiB frame: the frame-limit error fires before method
# dispatch, so the method name is arbitrary (probe convention: "x").
PROOF_B_PAD_BYTES = 1_100_000
# Bounded drain deadline after the single -32001 response: a product
# that emitted a second response (or kept stdout open) must fail the
# proof instead of hanging it.
PROOF_B_DRAIN_DEADLINE_SECONDS = 15.0

# Bounded deadline for every request-payload write: a product that
# stops draining stdin fills the 64 KiB pipe; the write must then
# fail the proof instead of blocking the harness.
PROOF_WRITE_DEADLINE_SECONDS = 30.0

# Full reservation block size: 2 x 4096-byte v4 pages (header page
# plus evidence page; binary-format-v4 publication namespace, Rust
# publication::reservation::FILE_SIZE and its Go counterpart).  The
# maintenance collectors list a reservation only when the file has
# this exact size, so a kill between the two page writes leaves a
# partial file that is skipped by the list.
RESERVATION_FILE_SIZE = 8192


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


# Bytes that ``read_responses`` took out of the pipe but that are not
# part of any returned response line (the start of a next response,
# or an unterminated trailing line when the deadline expires or EOF
# arrives).  Keyed by ``proc.pid``; ``drain_stdout`` consumes the
# entry so its byte count stays exact.  Never parsed as a response.
_PENDING_STDOUT_BYTES = {}


def read_responses(proc, expect, deadline_seconds,
                   max_line_bytes=None, raw_lines=None):
    """Read up to ``expect`` complete LF-terminated response lines.

    Reads are non-blocking under a monotonic deadline: the stdout fd
    is switched to non-blocking once, a selector waits for
    readability only up to the remaining time, and each wake reads at
    most 65536 bytes with ``os.read`` (never ``readline()``, which
    blocks for as long as a child holds a partial line open).  Only
    complete LF-terminated lines are decoded as responses, in wire
    order.  At most ``expect`` lines are decoded: once the count is
    met, any remaining bytes -- complete extra response lines, the
    start of a next response, or an unterminated trailing line when
    the deadline expires or EOF arrives -- are retained in
    ``_PENDING_STDOUT_BYTES`` under ``proc.pid`` for ``drain_stdout``
    byte accounting and are never parsed.

    ``max_line_bytes`` bounds one accumulated line: a response
    frame that grows past the ceiling without a terminator raises
    ``ResourceFailure`` instead of buffering a peer's unbounded
    output.  ``raw_lines``, when supplied, receives the exact wire
    bytes of every decoded line (LF included) so callers can apply
    the shared server-response envelope validator, which operates on
    raw text.

    Returns the list of decoded response objects read so far (the
    caller asserts the count); a product that stays alive without
    answering cannot block the harness past the deadline.
    """

    import selectors

    responses = []
    fd = proc.stdout.fileno()
    os.set_blocking(fd, False)
    sel = selectors.DefaultSelector()
    sel.register(fd, selectors.EVENT_READ)
    deadline = time.monotonic() + deadline_seconds
    buf = b""
    while len(responses) < expect:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        if not sel.select(remaining):
            continue
        try:
            chunk = os.read(fd, 65536)
        except (BlockingIOError, InterruptedError):
            continue
        if not chunk:
            break
        buf += chunk
        while len(responses) < expect:
            line_end = buf.find(b"\n")
            if line_end < 0:
                if max_line_bytes is not None and len(buf) > max_line_bytes:
                    raise ResourceFailure(
                        f"response frame exceeded {max_line_bytes} bytes "
                        f"without a terminator (output ceiling)")
                break
            line = buf[:line_end + 1]
            if max_line_bytes is not None and len(line) > max_line_bytes:
                raise ResourceFailure(
                    f"response frame of {len(line)} bytes exceeds the "
                    f"{max_line_bytes} byte output ceiling")
            if raw_lines is not None:
                raw_lines.append(line)
            responses.append(json.loads(buf[:line_end]))
            buf = buf[line_end + 1:]
    if buf:
        _PENDING_STDOUT_BYTES[proc.pid] = buf
    return responses


def write_all_bounded(proc, payload, deadline_seconds, proof):
    """Write ``payload`` to ``proc.stdin`` under a monotonic deadline.

    stdin is switched to non-blocking once; the loop waits on the
    write side with a selector -- never a blocking ``write``, which
    once a child stops draining stdin fills the 64 KiB pipe and
    blocks the harness forever -- and passes each writable wake to
    ``os.write`` until every byte is accepted.  A child that stops
    draining stdin raises a ``ResourceFailure`` naming the proof when
    the deadline expires.  The caller still closes stdin
    (``proc.stdin.close()``) after the payload is fully written.
    """

    import selectors

    fd = proc.stdin.fileno()
    os.set_blocking(fd, False)
    sel = selectors.DefaultSelector()
    sel.register(fd, selectors.EVENT_WRITE)
    deadline = time.monotonic() + deadline_seconds
    view = memoryview(payload)
    while view:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise ResourceFailure(
                f"{proof}: stdin write of {len(payload)} bytes did not "
                f"complete within {deadline_seconds} s "
                f"({len(view)} bytes pending; the child stopped "
                "draining stdin)")
        if not sel.select(remaining):
            continue
        try:
            written = os.write(fd, view)
        except (BlockingIOError, InterruptedError):
            continue
        if written <= 0:
            continue
        view = view[written:]


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


def _wait_for_export_temp(work_dir, proc, deadline_seconds):
    """Wait for the running export's private temp under a deadline.

    Returns the elapsed milliseconds.  Raises ``ResourceFailure`` if
    the product process exits first or the marker does not appear
    within ``deadline_seconds`` (the export member never started).
    """

    started = time.monotonic()
    while True:
        if proc.poll() is not None:
            raise ResourceFailure(
                f"proof a: the product exited before the export's "
                f"private temp appeared (returncode "
                f"{proc.returncode})")
        if export_temp_basenames(work_dir):
            return round((time.monotonic() - started) * 1000, 1)
        remaining = deadline_seconds - (time.monotonic() - started)
        if remaining <= 0:
            raise ResourceFailure(
                f"proof a: the export's private temp did not appear "
                f"within {deadline_seconds} s")
        time.sleep(min(0.02, remaining))


def proof_a(binary, label, work_dir, outcome):
    """Proof a: >16-in-flight server_busy race with a pipelining client.

    One export of the 500,000-line immutable source is pipelined with
    19 ``system.describe`` requests (one request per frame, one stdin
    blob, stdin closed).  Verified expectations (both product
    binaries, identical contract):

    - exactly 20 responses, one per request, ids 1..20 covered once;
    - exactly 3 describes answer -32002 ``server_busy`` (the queue
      admits one active unit plus 16 queued; the export frame is
      written alone first and the describes are pipelined only after
      the export's private temp appeared, so the split is
      deterministic; the harness asserts the count and the id
      coverage, never an exact id set);
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
    pub = HarnessJsonRpcService(
        [binary, "--jsonrpc"], label, cwd=work,
        read_deadline=PROOF_A_READ_DEADLINE_SECONDS,
        write_deadline=PROOF_WRITE_DEADLINE_SECONDS)
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
        # Start the slow export alone.  Its private temp appears only
        # while the export member is executing -- the session has then
        # already decremented its queue slot -- so pipelining the
        # describes after the marker makes the 16-admit/3-busy split
        # deterministic (without it, a worker delayed by CPU
        # contention can leave the export counted as a queued unit).
        export_frame = json.dumps({"jsonrpc": "2.0", "id": "1",
                                   "method": "iprange.v1.export",
                                   "params": export_params})
        write_all_bounded(proc, (export_frame + "\n").encode(),
                          PROOF_WRITE_DEADLINE_SECONDS,
                          "proof a export frame")
        outcome["export_start_marker_ms"] = _wait_for_export_temp(
            work, proc, PROOF_A_EXPORT_START_DEADLINE_SECONDS)
        lines = [json.dumps({"jsonrpc": "2.0", "id": str(index),
                             "method": "iprange.v1.system.describe",
                             "params": {}})
                 for index in range(2, 2 + PROOF_A_DESCRIBES)]
        payload = ("\n".join(lines) + "\n").encode()
        write_all_bounded(proc, payload, PROOF_WRITE_DEADLINE_SECONDS,
                          "proof a describes")
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
    the proof -- and the process exits non-zero (the frame-limit path
    is a startup/framing failure; the session contract
    ``iprange-jsonrpc-v1.md`` mandates a non-zero exit when the
    service cannot continue).
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
        payload = (big + "\n" + sentinel + "\n").encode()
        write_all_bounded(proc, payload, PROOF_WRITE_DEADLINE_SECONDS,
                          "proof b")
        proc.stdin.close()

        raw_lines = []
        responses = read_responses(
            proc, 1, 30,
            max_line_bytes=frame.OUTPUT_FRAME_LIMIT,
            raw_lines=raw_lines)
        outcome["response_lines"] = len(responses)
        if len(responses) != 1:
            raise ResourceFailure(
                f"expected 1 response line, got {len(responses)}")
        # The single -32001 answer must satisfy the exact server
        # response contract before any proof assertion: jsonrpc 2.0,
        # null id blessed only for the framing error, a well-formed
        # error object, the 65,000-byte object ceiling (shared
        # client validation, run.py decode_response_line) and the
        # 1,048,576-byte frame ceiling.  A malformed or oversized
        # response fails the proof instead of being accepted.
        try:
            frame.decode_response(raw_lines[0].rstrip(b"\n").decode("utf-8"))
        except (frame.FrameError, UnicodeDecodeError) as exc:
            raise ResourceFailure(
                f"over-limit close response failed server-envelope "
                f"validation: {exc}") from exc
        if len(raw_lines[0].rstrip(b"\n")) > frame.RESPONSE_OBJECT_LIMIT:
            raise ResourceFailure(
                f"over-limit close response of {len(raw_lines[0])} bytes "
                f"exceeds the 65,000-byte response-object ceiling")
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
        # Snapshot the primary failure before waiting: if an earlier
        # check already failed (the killed child was often blocked
        # producing that failure) it stays the reported defect.
        primary_failure = sys.exc_info()[0]
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
            # A forced kill is the harness cleaning up, never the
            # product's exit: a service that does not terminate on
            # its own fails the proof regardless of cleanup success.
            if primary_failure is None:
                raise ResourceFailure(
                    "product process did not exit on its own after the "
                    "over-limit close; the harness killed it after 10 s "
                    "(a forced kill is not a product exit)")
    outcome["exit_code"] = proc.returncode
    if proc.returncode == 0:
        raise ResourceFailure(
            "expected a non-zero exit code after the over-limit close "
            f"(startup/framing failure), got {proc.returncode}")
    outcome["stderr_tail"] = stderr_tail(stderr_log)
    if not outcome["stderr_tail"]:
        outcome.pop("stderr_tail")


def drain_stdout(proc, deadline_seconds):
    """Read every remaining stdout byte until EOF or deadline.

    Returns ``(bytes_read, reached_eof)``: ``bytes_read`` is the
    exact byte count observed after the caller's last
    ``read_responses`` -- including any bytes ``read_responses`` had
    already taken out of the pipe and retained in
    ``_PENDING_STDOUT_BYTES``, so the accounting stays exact (zero
    for a product that answered exactly once) -- and ``reached_eof``
    is True only when stdout reached EOF while the deadline was still
    open.  Used by proof b to prove the single -32001 response is the
    product's complete answer.  Reads are per-wake ``read1`` under
    ``select(min(remaining, 5.0))``; the stdout fd may already be
    non-blocking from ``read_responses``, so a spurious EAGAIN/INTR
    wake is retried, never fatal.
    """

    import selectors

    pending = _PENDING_STDOUT_BYTES.pop(proc.pid, b"")
    sel = selectors.DefaultSelector()
    sel.register(proc.stdout, selectors.EVENT_READ)
    deadline = time.monotonic() + deadline_seconds
    chunks = [pending] if pending else []
    reached_eof = False
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        if not sel.select(min(remaining, 5.0)):
            continue
        try:
            chunk = proc.stdout.read1(65536)
        except (BlockingIOError, InterruptedError):
            continue
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

    The kill waits for the reservation file's full block size, not
    just the magic header: the magic is written with the header page
    first, and the maintenance collectors list a reservation only at
    the exact full block size (8192 bytes), so killing between the
    header and evidence page writes leaves a partial file the list
    skips (a latency flake observed on the Rust product).
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

        def reservation_complete():
            # The magic header alone is not the kill point: the
            # reservation must be listable when the fresh producer
            # runs maintenance.list, and the collectors list only
            # full-size reservation blocks (see
            # RESERVATION_FILE_SIZE above).
            if not reservation_seen(work, RESERVATION_MAGIC):
                return False
            return any(
                os.path.getsize(os.path.join(work, name))
                >= RESERVATION_FILE_SIZE
                for name in private_artifact_names(work)["reservation"])

        _, seen_ms, thread = call_with_worker(
            producer, "1", "iprange.v1.current.publish",
            publish_params(feed, destination, "fail_if_exists"), 20,
            seen=reservation_complete)
        outcome["marker_seen_ms"] = seen_ms
        producer.kill_process_group()
        thread.join(timeout=5)
    finally:
        producer.kill_process_group()
        producer.close()

    listed = HarnessJsonRpcService(
        [binary, "--jsonrpc"], f"c-{label}", cwd=work,
        read_deadline=PROOF_A_READ_DEADLINE_SECONDS,
        write_deadline=PROOF_WRITE_DEADLINE_SECONDS)
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
    pub = HarnessJsonRpcService(
        [binary, "--jsonrpc"], label, cwd=work,
        read_deadline=PROOF_A_READ_DEADLINE_SECONDS,
        write_deadline=PROOF_WRITE_DEADLINE_SECONDS)
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
        payload = ("\n".join(frames) + "\n").encode()
        write_all_bounded(proc, payload, PROOF_WRITE_DEADLINE_SECONDS,
                          "proof d")
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


def _kill_process_group(proc):
    """Kill one owned setsid subprocess group and wait for it to exit."""

    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=5)


def self_test():
    """Bounded-I/O negative controls (no product binaries required).

    Two stub children prove the previously accepted deadline bypasses
    now fail instead of hanging the harness:

    1. Read control: a child that writes one unterminated line
       (``printf "{"``) and then sleeps 2 s.  ``read_responses``
       with a 0.1 s deadline must return within a bounded wall window
       (asserted under 1.5 s), must decode zero responses, and must
       retain the unterminated bytes in ``_PENDING_STDOUT_BYTES``
       (reported, never parsed).
    2. Write control: a child that never drains stdin (``sleep 2``).
       ``write_all_bounded`` of 1 MiB with a 0.5 s deadline must raise
       ``ResourceFailure`` within the window instead of blocking on
       the full 64 KiB pipe.

    Prints one line per control and returns 0 only when both controls
    pass and no owned child survives.  The lead runs this with
    ``--self-test`` during integration.
    """

    import selectors

    global spawn_jsonrpc  # proof_b resolves this module global

    root = tempfile.mkdtemp(prefix="iprange-self-test-proofb-")
    failures = []

    # Read control: partial line plus a sleeping child.
    stub = subprocess.Popen(
        ["/bin/sh", "-c", 'printf "{" ; sleep 2'],
        stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL, start_new_session=True)
    record_spawn(stub)
    try:
        sel = selectors.DefaultSelector()
        sel.register(stub.stdout, selectors.EVENT_READ)
        ready = sel.select(2.0)
        sel.close()
        started = time.monotonic()
        responses = read_responses(stub, 1, 0.1)
        elapsed = time.monotonic() - started
        partial = _PENDING_STDOUT_BYTES.pop(stub.pid, b"")
        print(f"self-test read control: read_responses returned in "
              f"{elapsed:.3f} s with {len(responses)} responses and "
              f"unterminated bytes retained {partial!r}")
        if elapsed >= 1.5:
            failures.append(
                f"read control returned in {elapsed:.3f} s "
                "(bounded window 1.5 s)")
        if responses:
            failures.append(
                f"read control decoded the unterminated line as a "
                f"response: {responses!r}")
        if ready and partial != b"{":
            failures.append(
                f"read control retained {partial!r}, expected b'{{'")
    finally:
        _kill_process_group(stub)
        stub.stdout.close()

    # Write control: child that never drains stdin.
    stub = subprocess.Popen(
        ["/bin/sh", "-c", "sleep 2"], stdin=subprocess.PIPE,
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        start_new_session=True)
    record_spawn(stub)
    try:
        payload = b"x" * (1024 * 1024)
        started = time.monotonic()
        try:
            write_all_bounded(stub, payload, 0.5,
                              "self-test write control")
        except ResourceFailure as exc:
            failure_text = str(exc)
        else:
            failure_text = None
        elapsed = time.monotonic() - started
        print(f"self-test write control: "
              f"{failure_text or 'unexpected success'} "
              f"in {elapsed:.3f} s")
        if failure_text is None:
            failures.append(
                "write control completed against a child that never "
                "drains stdin")
        elif elapsed >= 1.5:
            failures.append(
                f"write control raised in {elapsed:.3f} s "
                "(bounded window 1.5 s)")
    finally:
        _kill_process_group(stub)
        stub.stdin.close()

    # Proof-b controls: the single -32001 answer must satisfy the
    # server response envelope (shared frame.decode_response plus the
    # response-object ceiling), the frame must stay under the output
    # ceiling, and the product must terminate on its own.  A valid
    # stub passes; malformed, oversized, and hang-after-close stubs
    # must fail proof_b instead of being accepted.
    original_spawn = spawn_jsonrpc
    proof_b_controls = [
        ("missing-envelope",
         "import sys,json;sys.stdin.buffer.read();"
         "print(json.dumps({'error':{'code':-32001}}),flush=True);sys.exit(1)",
         False),
        ("oversized-response",
         "import sys,json;sys.stdin.buffer.read();"
         "print(json.dumps({'jsonrpc':'2.0','id':None,'error':"
         "{'code':-32001,'message':'a'*2100000}}),flush=True);sys.exit(1)",
         False),
        ("hang-after-stdout-close",
         "import sys,json,os,time;sys.stdin.buffer.read();"
         "print(json.dumps({'jsonrpc':'2.0','id':None,'error':"
         "{'code':-32001,'message':'frame too large'}}),flush=True);"
         "os.close(1);time.sleep(30)",
         False),
        ("valid-envelope",
         "import sys,json;sys.stdin.buffer.read();"
         "print(json.dumps({'jsonrpc':'2.0','id':None,'error':"
         "{'code':-32001,'message':'frame too large'}}),flush=True);"
         "sys.exit(1)",
         True),
    ]
    for label, body, should_pass in proof_b_controls:
        def spawn_stub(binary, cwd, stderr_log, _body=body):
            child = subprocess.Popen(
                [sys.executable, "-c", _body],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL, cwd=cwd)
            record_spawn(child)
            return child
        spawn_jsonrpc = spawn_stub
        outcome = {}
        started = time.monotonic()
        try:
            proof_b("/unused", label, root, outcome)
        except ResourceFailure as exc:
            failed_now = str(exc)
        else:
            failed_now = None
        elapsed = time.monotonic() - started
        print(f"self-test proof-b {label}: "
              f"{failed_now or 'accepted'} in {elapsed:.2f} s")
        if should_pass and failed_now is not None:
            failures.append(f"proof-b {label}: valid stub rejected: "
                            f"{failed_now}")
        if not should_pass and failed_now is None:
            failures.append(f"proof-b {label}: defective stub accepted")
        if elapsed >= 15:
            failures.append(f"proof-b {label}: took {elapsed:.2f} s "
                            "(bounded window 15 s)")
    spawn_jsonrpc = original_spawn

    # Shared-path read-deadline control: JsonRpcService with a bounded
    # read deadline must fail a service that answers a partial line
    # and stalls, instead of blocking on readline forever.
    service_root = tempfile.mkdtemp(prefix="iprange-self-test-rpc-")
    partial_stub = ('import sys,os,time;sys.stdin.readline();'
                    'os.write(1,b"{");time.sleep(60)')
    stalled_read = run.JsonRpcService(
        [sys.executable, "-c", partial_stub], "stub",
        cwd=service_root, read_deadline=0.2, write_deadline=0.2)
    started = time.monotonic()
    try:
        stalled_read.call("1", "iprange.v1.current.publish", {})
    except AssertionError as exc:
        read_failure = str(exc)
    except Exception as exc:  # noqa: BLE001 - control reports any exit
        read_failure = f"{type(exc).__name__}: {exc}"
    else:
        read_failure = None
    elapsed = time.monotonic() - started
    stalled_read.close()
    print(f"self-test shared-path read deadline: "
          f"{read_failure or 'unexpected success'} in {elapsed:.2f} s")
    if read_failure is None:
        failures.append("shared-path read control: stalled service "
                        "answered (unexpected)")
    elif "deadline" not in read_failure:
        failures.append(f"shared-path read control: {read_failure}")
    elif elapsed >= 1.5:
        failures.append(f"shared-path read control: {elapsed:.2f} s "
                        "(bounded window 1.5 s)")
    shutil.rmtree(service_root, ignore_errors=True)

    # Shared-path write-deadline control: a child that never drains
    # stdin must fail a bounded-write request instead of blocking on
    # the full pipe.
    service_root = tempfile.mkdtemp(prefix="iprange-self-test-write-")
    stalled_write = run.JsonRpcService(
        ["/bin/sh", "-c", "sleep 2"], "stub",
        cwd=service_root, read_deadline=0.2, write_deadline=0.25)
    started = time.monotonic()
    try:
        stalled_write.call(
            "1", "iprange.v1.system.describe",
            {"pad": "a" * 900_000})
    except AssertionError as exc:
        write_failure = str(exc)
    except Exception as exc:  # noqa: BLE001 - control reports any exit
        write_failure = f"{type(exc).__name__}: {exc}"
    else:
        write_failure = None
    elapsed = time.monotonic() - started
    stalled_write.close()
    print(f"self-test shared-path write deadline: "
          f"{write_failure or 'unexpected success'} in {elapsed:.2f} s")
    if write_failure is None:
        failures.append("shared-path write control: stalled service "
                        "accepted the request (unexpected)")
    elif "deadline" not in write_failure:
        failures.append(f"shared-path write control: {write_failure}")
    elif elapsed >= 1.5:
        failures.append(f"shared-path write control: {elapsed:.2f} s "
                        "(bounded window 1.5 s)")
    shutil.rmtree(service_root, ignore_errors=True)

    shutil.rmtree(root, ignore_errors=True)

    leftover = no_leftover_processes()
    if leftover:
        failures.append(f"self-test left owned children alive: {leftover}")

    for failure in failures:
        print(f"FAIL self-test: {failure}")
    if failures:
        print(f"FAIL self-test: {len(failures)} failure(s)")
        return 1
    print("PASS self-test: bounded read and write controls")
    return 0


def main():
    parser = argparse.ArgumentParser(
        description="Bounded-resource proofs at the iprange v1 JSON-RPC "
                    "product interface (milestone-4 resource gate).")
    parser.add_argument("--self-test", action="store_true",
                        help="run the bounded-I/O negative controls "
                             "(no product binaries) and exit")
    parser.add_argument("--binaries", metavar="rust=PATH go=PATH",
                        nargs="+",
                        help="absolute iprange --jsonrpc executables, as "
                             "rust=PATH go=PATH (required unless "
                             "--self-test is given)")
    parser.add_argument("--work-dir", metavar="DIR",
                        help="absolute existing root work directory "
                             "(required unless --self-test is given)")
    parser.add_argument("--json-report", metavar="PATH",
                        help="write the JSON resource report to this file")
    parser.add_argument("--keep-work", action="store_true",
                        help="keep per-proof work directories (default: "
                             "remove them after the run)")
    args = parser.parse_args()

    if args.self_test:
        return self_test()

    if not args.binaries:
        parser.error("--binaries is required unless --self-test is given")
    if not args.work_dir:
        parser.error("--work-dir is required unless --self-test is given")
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
            started = time.monotonic()
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
                    (time.monotonic() - started) * 1000, 1)
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
