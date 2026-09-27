"""Publish two feeds at once. One bad file must not remove the other.

Each feed is its own process and its own destination. The good publish
and the bad publish start together. The good destination keeps the
generator count. The bad destination is absent. That is the per-feed
failure boundary.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile

from generate import generate, merged_count, write_text


def publish_request(text_path, destination, name):
    return {
        "jsonrpc": "2.0",
        "id": name,
        "method": "iprange.v1.current.publish",
        "params": {
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
            "feed": name,
            "value_tag": {"text": "feed"},
            "metadata": {"mode": "replace_utf8", "text": name},
            "destination": destination,
            "publication_policy": "fail_if_exists",
            "immutable_feed_budget": {
                "max_heap_bytes": "16777216",
                "max_output_pages": "20000",
                "max_workspace_pages": "20000",
                "max_open_files": 3,
            },
        },
    }


def start(binary, request):
    proc = subprocess.Popen(
        [binary, "--jsonrpc"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    proc.stdin.write(json.dumps(request).encode("utf-8") + b"\n")
    proc.stdin.flush()
    return proc


def finish(proc):
    line = proc.stdout.readline()
    try:
        proc.stdin.close()
    except BrokenPipeError:
        pass
    err = proc.stderr.read()
    proc.wait(timeout=60)
    return proc.returncode, line.decode("utf-8", "replace"), err.decode("utf-8", "replace")


def prove(binary, work):
    good_text = os.path.join(work, "good.txt")
    bad_text = os.path.join(work, "bad.txt")
    ranges = generate(9, 20, 4, 64)
    expected = merged_count(ranges)
    with open(good_text, "w", encoding="utf-8") as stream:
        write_text(ranges, stream)
    with open(bad_text, "w", encoding="utf-8") as stream:
        stream.write("10.0.0.1\nnot-an-address\n")
    good_dest = os.path.join(work, "good.iprange")
    bad_dest = os.path.join(work, "bad.iprange")
    good = start(binary, publish_request(good_text, good_dest, "good"))
    bad = start(binary, publish_request(bad_text, bad_dest, "bad"))
    good_rc, good_out, good_err = finish(good)
    bad_rc, bad_out, _bad_err = finish(bad)
    if good_rc != 0 or "error" in good_out:
        raise AssertionError(f"good publish failed rc={good_rc} out={good_out[-300:]} err={good_err[-200:]}")
    if not os.path.isfile(good_dest):
        raise AssertionError("good destination is missing")
    if os.path.exists(bad_dest):
        raise AssertionError("bad publish left a destination")
    if bad_rc == 0 and "error" not in bad_out:
        raise AssertionError("bad publish was accepted")
    result = json.loads(good_out)
    got = int(result["result"]["report"]["addresses"])
    if got != expected:
        raise AssertionError(f"good publish imported {got}, generator says {expected}")
    return expected


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    args = parser.parse_args()
    for label, binary in (("rust", args.rust), ("go", args.go)):
        with tempfile.TemporaryDirectory(prefix=f"iprange-parallel-{label}-") as work:
            expected = prove(binary, work)
        print(f"PASS {label} parallel feeds kept {expected} and dropped the bad file")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, AssertionError, json.JSONDecodeError, subprocess.TimeoutExpired) as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        sys.exit(1)
