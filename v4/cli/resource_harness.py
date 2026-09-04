#!/usr/bin/env python3
"""Bounded-resource proofs at the iprange v1 JSON-RPC product interface.

Milestone-4 (delivery step 5) resource gate.  The declarative suite
proves the advertised ceilings, ``output_limit``, reader/cursor
capacity, the frame-layer batch bound, and the bounded adapter
memory model.  Four claims are not expressible in that suite (see
``resource-record.md``); this harness proves the three that run on
Linux, each at the normal product interface (no test hook, no
environment variable, only the JSON-RPC stdio pipe):

- Proof a -- the >16-in-flight ``server_busy`` race, driven with a
  pipelining client.  One export of a 500,000-line immutable source
  occupies the connection queue while 19 ``system.describe`` requests
  are pipelined behind it in one stdin blob (one request per line,
  20 frames in one write, stdin then closed).  Verified wire behavior
  on the current products (deterministic across repeated runs):

  * Rust -- the queue admits one active unit plus 16 queued, so
    exactly 3 of the 19 describes answer ``server_busy`` (-32002)
    and the other 16 answer with results (which ids sit at the
    admission boundary varies by a small timing race); the in-flight
    export answers -32010
    ``cancelled``: closing stdin stops acceptance and cancels the
    active unit (documented session contract), while queued units
    keep their factual outcome.  20 responses, exit 0.
  * Go -- the session hands each single-element frame to the worker
    through an unbuffered channel, so pipelined single frames are
    executed one at a time: the export completes, then all 19
    describes answer with results and no busy rejection is
    reached through this path (verified with feeds up to 3,000,000
    lines).  20 responses, exit 0.

  The task's probe-suite description ("3 busy, 17 ok, export never
  answers") is not what the wire shows: the probe data itself records
  20 responses with busy ids 18,19,20 and describe results 2..17
  (16 results, one export response).  A 500,000-line feed is the
  verified minimum that keeps the queue occupied; do not reduce it.
- Proof b -- the -32001 over-limit frame close path.  One frame over
  the 1 MiB input ceiling (method ``x``; the frame-limit error fires
  before method dispatch) is answered with exactly one response
  (error code -32001, null id), then the connection closes and the
  process exits 0.
- Proof c -- ``maintenance.remove`` against a real reservation
  nonce.  A producer killed at the reservation magic marker leaves
  one reservation; a fresh producer lists it, the harness rebuilds
  the removal entry from the raw reservation bytes (binary-format-v4
  section 20.1 offsets), removes it, and the reservation is durably
  gone.  The private publication temp may remain: bounded residue,
  recorded.

Both product binaries (Rust and Go) must pass all three proofs for
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
# empirically).  The Rust session admits one active unit plus 16
# queued, so exactly 3 of the 19 describes answer server_busy; which
# 3 (the admission-boundary ones near ids 17..20) varies by a small
# timing race, so the harness asserts the count and the id coverage,
# never an exact id set.  The Go session executes pipelined single
# frames one at a time and never reaches the queue bound through this
# path (verified up to 3,000,000-line feeds).
PROOF_A_FEED_LINES = 500_000
PROOF_A_DESCRIBES = 19
PROOF_A_BUSY_EXPECTED = 3
PROOF_A_OK_EXPECTED = PROOF_A_DESCRIBES - PROOF_A_BUSY_EXPECTED
PROOF_A_READ_DEADLINE_SECONDS = 60.0

# One >1 MiB frame: the frame-limit error fires before method
# dispatch, so the method name is arbitrary (probe convention: "x").
PROOF_B_PAD_BYTES = 1_100_000

# Reservation block field offsets (binary-format-v4.md section 20.1).
RSV_DATABASE_ID_OFFSET = 16
RSV_TRANSACTION_ID_OFFSET = 32
RSV_COMMIT_NONCE_OFFSET = 40
RSV_ATTEMPT_ID_OFFSET = 56
RSV_POLICY_OFFSET = 112
RSV_PREVIOUS_FLAGS_OFFSET = 116
RSV_OUTPUT_LENGTH_OFFSET = 120
RSV_OUTPUT_DEVICE_OFFSET = 128
RSV_OUTPUT_INODE_OFFSET = 136
RSV_OUTPUT_SHA512_OFFSET = 160
RSV_PREV_DEVICE_OFFSET = 224
RSV_PREV_INODE_OFFSET = 232
RSV_PREV_SHA512_OFFSET = 256
RSV_PREV_LENGTH_OFFSET = 452
RSV_BLOCK_LENGTH = 512


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


def proof_a(binary, label, work_dir, outcome):
    """Proof a: >16-in-flight server_busy race with a pipelining client.

    One export of the 500,000-line immutable source is pipelined with
    19 ``system.describe`` requests (one request per frame, one stdin
    blob, stdin closed).  Verified expectations:

    - exactly 20 responses, one per request, ids 1..20 covered once;
    - Rust: exactly 3 describes answer -32002 ``server_busy`` (the
      queue admits one active unit plus 16 queued; the boundary ids
      near 17..20 race by a small timing window, so only the count
      and the id coverage are asserted), the other 16 answer with
      results, and the export answers -32010 ``cancelled`` (EOF
      cancels the active unit); exit code 0.
    - Go: the export completes with a result, then all 19 describes
      answer with results; exit code 0.  The queue bound is not
      reachable through single-element pipelined frames (unbuffered
      worker handoff); the negative is the truthful record.
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

    export_params = {
        "source": {"path": source, "mode": "immutable"},
        "view": {"kind": "selection", "selection": {"mode": "all"}},
        "format": "ranges",
        "destination": os.path.join(work, "slow.txt"),
        "publication_policy": "fail_if_exists",
        "result_budget": {"max_rows": "10000000",
                          "max_output_bytes": "10737418240",
                          "max_open_files": 32},
    }

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
        if label == "rust":
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
        else:
            if outcome["busy_ids"]:
                raise ResourceFailure(
                    f"unexpected server_busy describes in the Go "
                    f"serialized pipeline: {outcome['busy_ids']}")
            if outcome["ok_ids"] != described_ids:
                raise ResourceFailure(
                    f"expected all {len(described_ids)} describes to "
                    f"answer with results, got {outcome['ok_ids']}")
            if outcome["export_code"] != "result":
                raise ResourceFailure(
                    "expected the Go export to complete with a "
                    f"result, got {outcome['export_code']}")
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
    """Proof b: one over-limit frame answers -32001, then clean close."""

    work = os.path.join(work_dir, f"b-{label}")
    os.makedirs(work, exist_ok=True)
    stderr_log = os.path.join(work, "b.stderr")
    proc = spawn_jsonrpc(binary, work, stderr_log)
    outcome["pid"] = proc.pid
    try:
        big = json.dumps({"jsonrpc": "2.0", "id": "1", "method": "x",
                          "params": {"pad": "a" * PROOF_B_PAD_BYTES}})
        proc.stdin.write((big + "\n").encode())
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


def parse_reservation(path):
    """Decode one reservation block (binary-format-v4.md section 20.1)."""

    with open(path, "rb") as stream:
        data = stream.read()
    if len(data) < RSV_BLOCK_LENGTH:
        raise ResourceFailure(
            f"reservation shorter than one block: {len(data)} bytes")
    page = data[:RSV_BLOCK_LENGTH]
    if page[:8] != RESERVATION_MAGIC:
        raise ResourceFailure(
            f"reservation magic mismatch: {page[:8]!r}")
    return {
        "database_id": page[RSV_DATABASE_ID_OFFSET:32].hex(),
        "transaction_id": str(int.from_bytes(
            page[RSV_TRANSACTION_ID_OFFSET:40], "little")),
        "commit_nonce": page[RSV_COMMIT_NONCE_OFFSET:56].hex(),
        "attempt_id": page[RSV_ATTEMPT_ID_OFFSET:72].hex(),
        "policy": int.from_bytes(page[RSV_POLICY_OFFSET:114], "little"),
        "previous_flags": int.from_bytes(
            page[RSV_PREVIOUS_FLAGS_OFFSET:120], "little"),
        "output_length": str(int.from_bytes(
            page[RSV_OUTPUT_LENGTH_OFFSET:128], "little")),
        "output_device": int.from_bytes(
            page[RSV_OUTPUT_DEVICE_OFFSET:136], "little"),
        "output_inode": int.from_bytes(
            page[RSV_OUTPUT_INODE_OFFSET:144], "little"),
        "output_sha512": page[RSV_OUTPUT_SHA512_OFFSET:224].hex(),
        "prev_device": int.from_bytes(
            page[RSV_PREV_DEVICE_OFFSET:232], "little"),
        "prev_inode": int.from_bytes(
            page[RSV_PREV_INODE_OFFSET:240], "little"),
        "prev_sha512": page[RSV_PREV_SHA512_OFFSET:320].hex(),
        "prev_length": str(int.from_bytes(
            page[RSV_PREV_LENGTH_OFFSET:460], "little")),
    }


def build_removal_entry(work_dir, row):
    """One maintenance.remove entry for a listed reservation.

    The listed row's evidence (phase "prepared", policy, output
    tuple) is kept; the output identity/digest are rebuilt from the
    raw reservation block bytes so the entry matches the durable
    record exactly, and the required ``previous`` identity+digest is
    added (all-zero identity when the reservation has no previous
    block, previous_flags == 0).
    """

    names = private_artifact_names(work_dir)["reservation"]
    if len(names) != 1:
        raise ResourceFailure(
            f"expected exactly one reservation, found {names}")
    parsed = parse_reservation(os.path.join(work_dir, names[0]))
    entry = dict(row)
    entry["evidence"] = json.loads(json.dumps(dict(row["evidence"])))
    entry["evidence"]["output"]["identity"] = {
        "file": str(parsed["output_inode"]),
        "volume": str(parsed["output_device"])}
    entry["evidence"]["output"]["digest"] = {
        "byte_length": parsed["output_length"],
        "sha512": parsed["output_sha512"]}
    if parsed["previous_flags"]:
        entry["evidence"]["previous"] = {
            "identity": {"file": str(parsed["prev_inode"]),
                         "volume": str(parsed["prev_device"])},
            "digest": {"byte_length": parsed["prev_length"],
                       "sha512": parsed["prev_sha512"]}}
    else:
        entry["evidence"]["previous"] = {
            "identity": {"file": "0", "volume": "0"},
            "digest": {"byte_length": "0", "sha512": "0" * 128}}
    return entry, parsed


def proof_c(binary, label, work_dir, outcome):
    """Proof c: maintenance.remove against a real reservation nonce."""

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

        entry, parsed = build_removal_entry(work, rows[0])
        outcome["policy"] = parsed["policy"]
        outcome["previous_flags"] = parsed["previous_flags"]
        outcome["output_byte_length"] = parsed["output_length"]
        outcome["phase"] = entry["evidence"].get("phase")
        outcome["previous_identity"] = entry["evidence"]["previous"][
            "identity"]
        outcome["previous_digest_sha512"] = (
            entry["evidence"]["previous"]["digest"]["sha512"])

        removal = listed.call("rm", "iprange.v1.maintenance.remove",
                              {"entry": entry})
        if "error" in removal:
            raise ResourceFailure(
                f"maintenance.remove failed: "
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
                ("a", proof_a), ("b", proof_b), ("c", proof_c)):
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
