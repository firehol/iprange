"""Cancel a request that is still running.

Unknown-id cancel does not prove the producer honors cancel, and a
proof that closes stdin proves nothing: EOF shutdown itself requests
cancellation of active work, so a producer whose `iprange.v1.cancel`
is dead code still passes. This script keeps stdin open across the
whole discriminating window. The probe must answer while stdin is
still open, so only `iprange.v1.cancel` can have stopped the publish.

The cancel is sent only after execution is observed to have begun
(the engine's own CPU time crossed a floor), and the cancelled
request must answer with the factual cancelled outcome — the spec
answers every request exactly once, so a dropped request and a
cancelled one must not both read as a pass. A producer that ignores
cancel finishes the import and answers with a result, which fails;
the destination must never appear.
"""

import argparse
import json
import os
import signal
import sys
import tempfile
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from run import JsonRpcService  # noqa: E402

from generate import generate, write_text
from measure import child_cpu_seconds

PUBLISH_ID = "cancel-inflight-1"
PROBE_ID = "cancel-probe-1"


def cancelled_result(response, destination_exists):
    """Reason the cancelled request is not a pass, or "" if it is fine.

    The spec answers every request exactly once: a cancelled request
    must answer with the factual cancelled outcome. No answer at all
    is a dropped request — indistinguishable from a producer that
    ignored the cancel and lost the response, so it is a failure.
    A delivered answer must be the factual cancelled outcome; a
    result means the producer ignored the cancel. The outcome must
    be one the spec names (iprange-jsonrpc-v1.md) and must agree
    with the observed operation state: an outcome claiming the
    publication landed requires the destination, and one claiming it
    did not forbids it.
    """
    if response is None:
        return ("cancelled request never answered: the spec answers "
                "every request exactly once")
    if "result" in response:
        return "cancelled publish answered with a result"
    error = response.get("error", {})
    data = error.get("data", {})
    if error.get("code") != -32010:
        return f"cancelled request answered {error!r}"
    if data.get("code") != "cancelled":
        return f"cancelled outcome lost its code: {data!r}"
    outcome = data.get("outcome")
    if outcome is None:
        return f"cancelled outcome lost its state: {data!r}"
    permitted = {
        "not_started", "not_committed", "committed",
        "not_published", "published", "outcome_unknown",
        "read_only_failure",
    }
    if outcome not in permitted:
        return (f"cancelled outcome {outcome!r} is not one the spec "
                f"names: {data!r}")
    if outcome == "published" and not destination_exists:
        return ("cancelled outcome claims the publication landed, but "
                f"the destination is absent: {data!r}")
    if outcome in ("not_started", "not_committed", "not_published") \
            and destination_exists:
        return (f"cancelled outcome {outcome!r} claims no publication, "
                f"but the destination exists: {data!r}")
    if outcome == "read_only_failure":
        # The spec maps read_only_failure only from a read-only
        # operation; this proof cancels a publish — an operation the
        # proof already observed executing (the CPU floor) — so a
        # read_only outcome here is a method-outcome forgery.
        return ("cancelled publish claimed read_only_failure, but a "
                f"publish is never a read-only operation: {data!r}")
    return ""


def record_answer(seen, response):
    """File one response under its id, refusing a duplicate answer.

    The spec answers every request exactly once: a second terminal
    frame for the same id cannot silently replace the first — that
    would hide an exactly-once violation from the proof.
    """
    identifier = response.get("id")
    if identifier in seen:
        raise AssertionError(
            f"duplicate answer for {identifier!r}: the spec answers "
            f"every request exactly once (first={seen[identifier]!r}, "
            f"second={response!r})")
    seen[identifier] = response


def prove(binary, work):
    text_path = os.path.join(work, "slow.txt")
    destination = os.path.join(work, "slow.iprange")
    with open(text_path, "w", encoding="utf-8") as stream:
        write_text(generate(3, 800000, 4, 4_000_000), stream)
    service = JsonRpcService([binary, "--jsonrpc"], "bench", start_new_session=True)

    def forward(signum, frame):
        del signum, frame
        service.kill_process_group()
        raise SystemExit(143)

    previous = signal.signal(signal.SIGTERM, forward)
    seen = {}
    try:
        service.submit(PUBLISH_ID, "iprange.v1.current.publish", {
            "input": {
                "paths": [text_path],
                "family": "ipv4",
                "fix_network": True,
                "default_prefix": 32,
                "dns": {"threads": 1, "silent": True},
                "expand_at_paths": False,
                "max_line_bytes": 1024,
                "max_expanded_paths": 4,
            },
            "feed": "slow",
            "value_tag": {"text": "feed"},
            "metadata": {"mode": "replace_utf8", "text": "slow"},
            "destination": destination,
            "publication_policy": "fail_if_exists",
            "immutable_feed_budget": {
                "max_heap_bytes": "268435456",
                "max_output_pages": "200000",
                "max_workspace_pages": "200000",
                "max_open_files": 3,
            },
        })
        # Observed start: the cancel must land while the import is
        # executing, not before admission — otherwise "cancelled"
        # proves nothing about in-flight work. The engine's own CPU
        # time is the observable that execution began.
        start_deadline = time.monotonic() + 30
        started = False
        while time.monotonic() < start_deadline:
            cpu = child_cpu_seconds(service.proc.pid)
            if cpu is not None and cpu >= 0.3:
                started = True
                break
            if service.proc.poll() is not None:
                break
            time.sleep(0.02)
        if not started:
            raise AssertionError(
                "publish never began executing; the cancel window is vacuous")
        service.notify("iprange.v1.cancel", {"request_id": PUBLISH_ID})
        # The probe is submitted before stdin closes. Until it answers,
        # the only thing that can have stopped the publish is the cancel
        # notification above. Then the publish itself must reach a
        # terminal state while stdin is still open: a producer that
        # ignores cancel finishes the import and answers with a result,
        # so the classifier below is reachable, not dead code. Killing
        # the producer first would hide exactly that answer.
        service.submit(PROBE_ID, "iprange.v1.system.describe", {})
        # Read without select: a buffered readline driven by a raw-fd
        # select can strand a co-written frame inside the Python buffer,
        # where select never sees it again. Non-blocking reads drain the
        # buffer itself.
        os.set_blocking(service.proc.stdout.fileno(), False)
        deadline = time.monotonic() + 60
        probe_at = None
        while time.monotonic() < deadline:
            try:
                line = service.proc.stdout.readline(1_048_578)
            except (BlockingIOError, OSError):
                line = ""
            if line:
                response = json.loads(line)
                record_answer(seen, response)
                if PROBE_ID in seen and probe_at is None:
                    probe_at = time.monotonic()
                if probe_at is not None and PUBLISH_ID in seen:
                    # Terminal pair seen. The spec answers every request
                    # exactly once: a second frame for either id would be
                    # an exactly-once violation, so drain the remaining
                    # stdout under a short quiet bound and file every
                    # frame through record_answer before closing (a
                    # duplicate hidden behind the first terminal answer
                    # used to escape with the forced teardown).
                    quiet = time.monotonic() + 0.5
                    while time.monotonic() < quiet:
                        try:
                            extra = service.proc.stdout.readline(1_048_578)
                        except (BlockingIOError, OSError):
                            extra = ""
                        if extra:
                            record_answer(seen, json.loads(extra))
                            quiet = time.monotonic() + 0.5
                            continue
                        if service.proc.poll() is not None:
                            break
                        time.sleep(0.01)
                    break
            else:
                if service.proc.poll() is not None:
                    break
                time.sleep(0.05)
            if probe_at is not None and time.monotonic() - probe_at > 20:
                break
        if PROBE_ID not in seen:
            raise AssertionError("the probe never answered while stdin was open")
        if "result" not in seen[PROBE_ID]:
            raise AssertionError(f"probe was not answered: {seen[PROBE_ID]!r}")
        cancelled = seen.get(PUBLISH_ID)
        # The cancelled request must reach a terminal answer within the
        # window: the spec answers every request exactly once, and the
        # engines answer a cancelled request with the factual -32010
        # outcome (verified against both staged binaries). Silence is a
        # dropped request, not a cancelled one. The outcome is checked
        # against the observed publication state (destination on disk).
        reason = cancelled_result(cancelled, os.path.exists(destination))
        if reason:
            raise AssertionError(reason)
        # The session answered everything it was asked: close strictly.
        # The ordinary close's residue and exit-status checks are part
        # of the proof; the forced teardown below is only the failure
        # path's cleanup.
        service.close()
    finally:
        signal.signal(signal.SIGTERM, previous)
        if service.proc.poll() is None:
            service.kill_process_group()
        service.close(allow_forced=True, broken_exchange=True)
    if os.path.exists(destination):
        raise AssertionError("cancelled publish left a destination")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    args = parser.parse_args()
    for label, binary in (("rust", args.rust), ("go", args.go)):
        with tempfile.TemporaryDirectory(prefix=f"iprange-cancel-{label}-") as work:
            prove(binary, work)
        print(f"PASS {label} in-flight publish was cancelled")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, AssertionError, json.JSONDecodeError, KeyError) as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        sys.exit(1)
