"""Cancel a request that is still running.

Unknown-id cancel does not prove the producer honors cancel. This
script submits current.publish, cancels that request id, and shows the
session stays responsive. The cancelled request must never answer with
a result. A producer that ignores cancel finishes the import and
answers with one, which fails the proof.
"""

import argparse
import json
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from run import JsonRpcService  # noqa: E402

from generate import generate, write_text


def prove(binary, work):
    text_path = os.path.join(work, "slow.txt")
    destination = os.path.join(work, "slow.iprange")
    with open(text_path, "w", encoding="utf-8") as stream:
        write_text(generate(3, 800000, 4, 4_000_000), stream)
    service = JsonRpcService([binary, "--jsonrpc"], "bench", start_new_session=True)
    seen = []
    try:
        service.submit("cancel-inflight-1", "iprange.v1.current.publish", {
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
        service.notify("iprange.v1.cancel", {"request_id": "cancel-inflight-1"})
        service.submit("cancel-probe-1", "iprange.v1.system.describe", {})
        service.proc.stdin.close()
        while True:
            line = service.proc.stdout.readline(1_048_578)
            if not line:
                break
            seen.append(json.loads(line))
    finally:
        if service.proc.poll() is None:
            service.kill_process_group()
        service.close(allow_forced=True, broken_exchange=True)
    for response in seen:
        if response.get("id") == "cancel-inflight-1" and "result" in response:
            raise AssertionError("cancelled publish answered with a result")
    if not any(
        response.get("id") == "cancel-probe-1" and "result" in response
        for response in seen
    ):
        raise AssertionError("the session did not stay responsive after cancel")


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
