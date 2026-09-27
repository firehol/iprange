"""Measure one history projection on both product binaries.

The generator computes the retained address count before either engine
runs. The timed command is the projection. Loading the last-seen file
is not part of the sample.
"""

import argparse
import ipaddress
import json
import os
import subprocess
import sys
import tempfile

from generate import generate, retained_count
from measure import measure, median, ratio
from perf import fail, sample_binary


def call(binary, payload):
    proc = subprocess.Popen([binary, "--jsonrpc"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    proc.stdin.write(payload)
    proc.stdin.flush()
    line = proc.stdout.readline()
    proc.stdin.close()
    proc.wait()
    if proc.returncode != 0:
        raise AssertionError(proc.stderr.read().decode("utf-8", "replace")[-500:])
    response = json.loads(line)
    if "result" not in response:
        raise AssertionError(response.get("error"))
    return response["result"]


def write_seen(path, ranges):
    with open(path, "w", encoding="utf-8") as stream:
        stream.write("from,to,value\n")
        for start, end, value in ranges:
            left = ipaddress.IPv4Address(start)
            right = ipaddress.IPv4Address(end)
            stream.write(f"{left},{right},{value}\n")


def load_seen(binary, csv_path, database_path, work, index):
    create = {
        "jsonrpc": "2.0",
        "id": "create",
        "method": "iprange.v1.database.create",
        "params": {
            "path": database_path,
            "family": "ipv4",
            "value_kind": "direct",
            "structure_kind": "none",
            "value_tag": {"text": "last_seen"},
            "reader_capacity": 4,
        },
    }
    call(binary, json.dumps(create).encode("utf-8") + b"\n")
    replace = {
        "jsonrpc": "2.0",
        "id": "load",
        "method": "iprange.v1.direct.replace",
        "params": {
            "path": database_path,
            "input": {"path": csv_path, "max_line_bytes": 1024},
            "metadata": {"mode": "keep"},
            "writer_budget": {
                "max_heap_bytes": "268435456",
                "max_private_pages": "200000",
                "max_growth_pages": "200000",
                "max_open_files": 4,
            },
        },
    }
    call(binary, json.dumps(replace).encode("utf-8") + b"\n")


def prepare_projection(binary, work, ranges, cutoff, index):
    csv_path = os.path.join(work, f"seen-{index}.csv")
    seen_path = os.path.join(work, f"seen-{index}.iprange")
    history_path = os.path.join(work, f"history-{index}.iprange")
    write_seen(csv_path, ranges)
    load_seen(binary, csv_path, seen_path, work, index)
    create = {
        "jsonrpc": "2.0",
        "id": "history",
        "method": "iprange.v1.database.create",
        "params": {
            "path": history_path,
            "family": "ipv4",
            "value_kind": "membership",
            "structure_kind": "none",
            "value_tag": {"text": "membership"},
            "reader_capacity": 4,
        },
    }
    call(binary, json.dumps(create).encode("utf-8") + b"\n")
    request = os.path.join(work, f"project-{index}.json")
    payload = {
        "jsonrpc": "2.0",
        "id": "project",
        "method": "iprange.v1.history.project",
        "params": {
            "path": history_path,
            "last_seen": {"path": seen_path, "mode": "live"},
            "windows": [{"feed": "recent", "cutoff": cutoff}],
            "metadata": {"mode": "keep"},
            "writer_budget": {
                "max_heap_bytes": "268435456",
                "max_private_pages": "200000",
                "max_growth_pages": "200000",
                "max_open_files": 4,
            },
        },
    }
    with open(request, "w", encoding="utf-8") as stream:
        json.dump(payload, stream, separators=(",", ":"))
        stream.write("\n")
    return request


def valued_ranges(seed, count, span, space):
    ranges = generate(seed, count, span, space)
    valued = []
    for index, (start, end) in enumerate(ranges):
        valued.append((start, end, 10 if index % 2 == 0 else 5))
    return valued


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--count", type=int, default=1000)
    parser.add_argument("--span", type=int, default=4)
    parser.add_argument("--space", type=int, default=10000)
    parser.add_argument("--cutoff", type=int, default=7)
    args = parser.parse_args()
    ranges = valued_ranges(1, args.count, args.span, args.space)
    expected = retained_count(ranges, args.cutoff)

    def check(binary, work, index):
        request = prepare_projection(binary, work, ranges, args.cutoff, index)
        with open(request, "rb") as stream:
            result = call(binary, stream.read())
        got = int(result["report"]["windows"][0]["after_addresses"])
        if got != expected:
            raise AssertionError(f"retained {got}, generator says {expected}")
        return request

    with tempfile.TemporaryDirectory(prefix="iprange-history-") as work:
        check(args.rust, work, 0)
        check(args.go, work, 1)
        rust_requests = [prepare_projection(args.rust, work, ranges, args.cutoff, 10 + index) for index in range(args.rounds)]
        go_requests = [prepare_projection(args.go, work, ranges, args.cutoff, 100 + index) for index in range(args.rounds)]
        report = {
            "expected_retained": expected,
            "cutoff": args.cutoff,
            "rounds": args.rounds,
            "rust": sample_binary(args.rust, [(path, None) for path in rust_requests]),
            "go": sample_binary(args.go, [(path, None) for path in go_requests]),
        }
    report["ratio"] = ratio(report["rust"], report["go"])
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
