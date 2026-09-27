"""One close run: 1,000 feeds and one 10M-range import.

The generator names the merged address count before either engine runs.
Both engines must report that count and the same active feed count.
This is one workstation run, not a ratio verdict.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile

from generate import generate, merged_count, write_text
from perf import PUBLISH, fail


WRITER = {
    "max_heap_bytes": "268435456",
    "max_private_pages": "200000",
    "max_growth_pages": "200000",
    "max_open_files": 4,
}


class Session:
    def __init__(self, binary):
        self.proc = subprocess.Popen(
            [binary, "--jsonrpc"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )

    def call(self, method, params):
        request = {"jsonrpc": "2.0", "id": method, "method": method, "params": params}
        self.proc.stdin.write(json.dumps(request).encode("utf-8") + b"\n")
        self.proc.stdin.flush()
        line = self.proc.stdout.readline()
        if not line:
            err = self.proc.stderr.read().decode("utf-8", "replace")[-500:]
            raise AssertionError(f"{method} returned no response: {err}")
        response = json.loads(line)
        if "error" in response:
            raise AssertionError(response["error"])
        return response["result"]

    def close(self):
        self.proc.stdin.close()
        self.proc.wait(timeout=30)
        self.proc.stdout.close()
        self.proc.stderr.close()


def feed_name(index):
    return f"f{index:04d}"


def publish_one(session, text_path, destination, name, budget):
    request = json.loads(json.dumps(PUBLISH))
    request["params"]["input"]["paths"] = [text_path]
    request["params"]["input"]["family"] = "ipv4"
    request["params"]["destination"] = destination
    request["params"]["feed"] = name
    request["params"]["metadata"] = {"mode": "replace_utf8", "text": name}
    request["params"]["immutable_feed_budget"] = budget
    # publish expects the request already framed; call() frames it.
    return session.call("iprange.v1.current.publish", request["params"])


def load_feeds(binary, work, feeds):
    session = Session(binary)
    try:
        membership = os.path.join(work, "mem.iprange")
        session.call("iprange.v1.database.create", {
            "path": membership,
            "family": "ipv4",
            "value_kind": "membership",
            "structure_kind": "none",
            "value_tag": {"text": "membership"},
            "reader_capacity": 4,
        })
        budget = {
            "max_heap_bytes": "16777216",
            "max_output_pages": "20000",
            "max_workspace_pages": "20000",
            "max_open_files": 3,
        }
        for name, ranges in feeds:
            text_path = os.path.join(work, f"{name}.txt")
            destination = os.path.join(work, f"{name}.iprange")
            with open(text_path, "w", encoding="utf-8") as stream:
                write_text(ranges, stream)
            published = publish_one(session, text_path, destination, name, budget)
            if int(published["report"]["addresses"]) != merged_count(ranges):
                raise AssertionError(f"{name} import did not match the generator")
            session.call("iprange.v1.feeds.create", {
                "path": membership,
                "feed": name,
                "current": {"source": {"path": destination, "mode": "immutable"}, "feed": name},
                "metadata": {"mode": "keep"},
                "writer_budget": WRITER,
            })
        info = session.call("iprange.v1.database.info", {
            "source": {"path": membership, "mode": "live"},
        })
        return int(info["info"]["active_feed_count"])
    finally:
        session.close()


def import_ranges(binary, work, ranges, expected):
    text_path = os.path.join(work, "wide.txt")
    destination = os.path.join(work, "wide.iprange")
    with open(text_path, "w", encoding="utf-8") as stream:
        write_text(ranges, stream)
    session = Session(binary)
    try:
        published = publish_one(session, text_path, destination, "wide", PUBLISH["params"]["immutable_feed_budget"])
    finally:
        session.close()
    got = int(published["report"]["addresses"])
    if got != expected:
        raise AssertionError(f"imported {got} addresses, generator says {expected}")
    return got


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--feeds", type=int, default=1000)
    parser.add_argument("--ranges", type=int, default=10000000)
    parser.add_argument("--span", type=int, default=4)
    parser.add_argument("--space", type=int, default=100000000)
    args = parser.parse_args()
    for label, path in (("rust", args.rust), ("go", args.go)):
        if not os.path.isabs(path):
            return fail(f"{label} binary must be absolute")
    feeds = [(feed_name(index), [(index, index)]) for index in range(args.feeds)]
    wide = generate(7, args.ranges, args.span, args.space)
    expected = merged_count(wide)
    with tempfile.TemporaryDirectory(prefix="iprange-ceiling-") as work:
        rust_feed_dir = os.path.join(work, "rust-feeds")
        go_feed_dir = os.path.join(work, "go-feeds")
        rust_wide_dir = os.path.join(work, "rust-wide")
        go_wide_dir = os.path.join(work, "go-wide")
        for path in (rust_feed_dir, go_feed_dir, rust_wide_dir, go_wide_dir):
            os.makedirs(path)
        rust_feeds = load_feeds(args.rust, rust_feed_dir, feeds)
        go_feeds = load_feeds(args.go, go_feed_dir, feeds)
        if rust_feeds != args.feeds or go_feeds != args.feeds:
            raise AssertionError(f"active feeds rust={rust_feeds} go={go_feeds}, want {args.feeds}")
        rust_ranges = import_ranges(args.rust, rust_wide_dir, wide, expected)
        go_ranges = import_ranges(args.go, go_wide_dir, wide, expected)
    print(json.dumps({
        "feeds": args.feeds,
        "ranges": args.ranges,
        "merged_addresses": expected,
        "rust_feeds": rust_feeds,
        "go_feeds": go_feeds,
        "rust_addresses": rust_ranges,
        "go_addresses": go_ranges,
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
