"""Measure one overlap join on both product binaries.

The generator computes the overlap before either engine runs. The timed
command is the join. The two publishes are not part of the sample.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile

from generate import generate, overlap_count, write_text
from measure import measure, median
from perf import PUBLISH, fail, sample_binary


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


def publish(binary, feed, destination, work, index):
    request = json.loads(json.dumps(PUBLISH))
    request["params"]["input"]["paths"] = [feed]
    request["params"]["destination"] = destination
    path = os.path.join(work, f"publish-{index}.json")
    with open(path, "w", encoding="utf-8") as stream:
        json.dump(request, stream, separators=(",", ":"))
        stream.write("\n")
    with open(path, "rb") as stream:
        call(binary, stream.read())


def prepare_join(binary, work, left, right, index):
    left_text = os.path.join(work, f"left-{index}.txt")
    right_text = os.path.join(work, f"right-{index}.txt")
    with open(left_text, "w", encoding="utf-8") as stream:
        write_text(left, stream)
    with open(right_text, "w", encoding="utf-8") as stream:
        write_text(right, stream)
    left_db = os.path.join(work, f"left-{index}.iprange")
    right_db = os.path.join(work, f"right-{index}.iprange")
    publish(binary, left_text, left_db, work, f"{index}-l")
    publish(binary, right_text, right_db, work, f"{index}-r")
    request = os.path.join(work, f"join-{index}.json")
    payload = {
        "jsonrpc": "2.0",
        "id": "join",
        "method": "iprange.v1.join.membership",
        "params": {
            "left": {
                "source": {"path": left_db, "mode": "immutable"},
                "selection": {"mode": "all"},
                "membership_query_budget": {"max_heap_bytes": "268435456"},
            },
            "right": {
                "source": {"path": right_db, "mode": "immutable"},
                "selection": {"mode": "all"},
                "membership_query_budget": {"max_heap_bytes": "268435456"},
            },
            "output": {
                "path": os.path.join(work, f"join-{index}.jsonl"),
                "format": "jsonl",
                "publication_policy": "fail_if_exists",
                "result_budget": {
                    "max_rows": "10000000",
                    "max_output_bytes": "1073741824",
                    "max_open_files": 3,
                },
            },
        },
    }
    with open(request, "w", encoding="utf-8") as stream:
        json.dump(payload, stream, separators=(",", ":"))
        stream.write("\n")
    return request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--count", type=int, default=1000)
    parser.add_argument("--span", type=int, default=4)
    parser.add_argument("--space", type=int, default=10000)
    args = parser.parse_args()
    left = generate(1, args.count, args.span, args.space)
    right = generate(2, args.count, args.span, args.space)
    expected = overlap_count(left, right)

    def check(binary, work, index):
        request = prepare_join(binary, work, left, right, index)
        with open(request, "rb") as stream:
            result = call(binary, stream.read())
        got = int(result["report"]["overlap_addresses"])
        if got != expected:
            raise AssertionError(f"overlap {got}, generator says {expected}")
        return request

    with tempfile.TemporaryDirectory(prefix="iprange-join-") as work:
        check(args.rust, work, 0)
        check(args.go, work, 1)
        rust_requests = [prepare_join(args.rust, work, left, right, 10 + index) for index in range(args.rounds)]
        go_requests = [prepare_join(args.go, work, left, right, 100 + index) for index in range(args.rounds)]
        report = {
            "expected_overlap": expected,
            "rounds": args.rounds,
            "rust": sample_binary(args.rust, [(path, None) for path in rust_requests]),
            "go": sample_binary(args.go, [(path, None) for path in go_requests]),
        }
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
