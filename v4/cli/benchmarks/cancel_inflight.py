"""Cancel a request that is still running.

Unknown-id cancel does not prove the producer honors cancel, and a
proof that closes stdin proves nothing: EOF shutdown itself requests
cancellation of active work, so a producer whose `iprange.v1.cancel`
is dead code still passes. This script keeps stdin open across the
whole discriminating window. The probe must answer while stdin is
still open, so only `iprange.v1.cancel` can have stopped the publish.

A producer that ignores cancel finishes the import and answers the
cancelled id with a result, which fails. A producer that drops the
request without cancelling fails the probe. If the cancelled id is
answered at all, it must carry the factual cancelled outcome.
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

PUBLISH_ID = "cancel-inflight-1"
PROBE_ID = "cancel-probe-1"


def cancelled_result(response):
    """Reason the cancelled request is not a pass, or "" if it is fine.

    A cancelled request that answers with a result means the producer
    ignored the cancel. A delivered answer must be the factual cancelled
    outcome. A suppressed request is fine.
    """
    if response is None:
        return ""
    if "result" in response:
        return "cancelled publish answered with a result"
    error = response.get("error", {})
    data = error.get("data", {})
    if error.get("code") != -32010:
        return f"cancelled request answered {error!r}"
    if data.get("code") != "cancelled":
        return f"cancelled outcome lost its code: {data!r}"
    if data.get("outcome") is None:
        return f"cancelled outcome lost its state: {data!r}"
    return ""


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
                seen[response.get("id")] = response
                if PROBE_ID in seen and probe_at is None:
                    probe_at = time.monotonic()
                if probe_at is not None and PUBLISH_ID in seen:
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
        # If the publish never answered: this corpus imports in about
        # half a second un-cancelled, so twenty seconds without an
        # answer means the import was aborted by the cancel. The
        # process staying alive is normal — the session waits for
        # stdin to close.
        reason = cancelled_result(cancelled)
        if reason:
            raise AssertionError(reason)
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
