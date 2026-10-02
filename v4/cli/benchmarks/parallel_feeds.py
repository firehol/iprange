"""Publish two feeds at once. One bad file must not remove the other.

Each feed is its own process and its own destination. The good publish
and the bad publish start together. The good destination keeps the
generator count. The bad destination is absent. That is the per-feed
failure boundary.
"""

import argparse
import os
import sys
import tempfile
import threading

from client import BenchSession
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


def run_publish(binary, text_path, destination, name, box):
    session = BenchSession(binary)
    try:
        box["result"] = session.call("iprange.v1.current.publish", publish_request(
            text_path, destination, name)["params"])
    except Exception as exc:
        # Every publish failure lands in the result channel (r183):
        # an exception class outside the narrow pair must not escape
        # the worker thread unseen.
        box.setdefault("error", exc)
    finally:
        try:
            session.close()
        except Exception as exc:
            # Teardown is part of the publish (sol turn-2): a peer that
            # produced the destination and report and then exited
            # nonzero or left residue must not pass because the close
            # failure died with this thread — Thread.join propagates
            # nothing, so the result channel is the only path. An
            # earlier publish failure is the primary error; the
            # teardown failure does not overwrite it.
            box.setdefault("error", exc)


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
    good = {}
    bad = {}
    good_thread = threading.Thread(
        target=run_publish, args=(binary, good_text, good_dest, "good", good))
    bad_thread = threading.Thread(
        target=run_publish, args=(binary, bad_text, bad_dest, "bad", bad))
    good_thread.start()
    bad_thread.start()
    good_thread.join(timeout=60)
    bad_thread.join(timeout=60)
    if good_thread.is_alive() or bad_thread.is_alive():
        raise AssertionError("a publish did not finish within 60s")
    if "error" in good or "result" not in good:
        raise AssertionError(f"good publish failed: {good.get('error')}")
    if not os.path.isfile(good_dest):
        raise AssertionError("good destination is missing")
    if os.path.exists(bad_dest):
        raise AssertionError("bad publish left a destination")
    if "error" not in bad:
        raise AssertionError("bad publish was accepted")
    got = int(good["result"]["report"]["addresses"])
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
    except (OSError, AssertionError, KeyError) as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        sys.exit(1)
