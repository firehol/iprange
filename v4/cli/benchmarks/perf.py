"""Measure one import scenario on the product binary.

The timed command is the release binary. It reads one JSON-RPC publish
request from stdin and exits when stdin closes. The runner is not in
the sample.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile

from generate import generate, merged_count, write_text
from measure import measure, median


def median_of(samples, key):
    return median([sample[key]["median"] for sample in samples])


PUBLISH = {
    "jsonrpc": "2.0",
    "id": "perf-import",
    "method": "iprange.v1.current.publish",
    "params": {
        "input": {
            "paths": ["FEED"],
            "family": "ipv4",
            "fix_network": True,
            "default_prefix": 32,
            "dns": {"threads": 1, "silent": True},
            "expand_at_paths": False,
            "max_line_bytes": 1024,
            "max_expanded_paths": 4,
        },
        "feed": "alpha",
        "value_tag": {"text": "feed"},
        "metadata": {"mode": "replace_utf8", "text": "alpha"},
        "destination": "OUT",
        "publication_policy": "fail_if_exists",
        "immutable_feed_budget": {
            "max_heap_bytes": "16777216",
            "max_output_pages": "20000",
            "max_workspace_pages": "20000",
            "max_open_files": 3,
        },
    },
}


def write_request(path, feed, destination):
    request = json.loads(json.dumps(PUBLISH))
    request["params"]["input"]["paths"] = [feed]
    request["params"]["destination"] = destination
    with open(path, "w", encoding="utf-8") as stream:
        json.dump(request, stream, separators=(",", ":"))
        stream.write("\n")


def prepare(work, seed, count, span, space, rounds):
    ranges = generate(seed, count, span, space)
    feed = os.path.join(work, "feed.txt")
    with open(feed, "w", encoding="utf-8") as stream:
        write_text(ranges, stream)
    requests = []
    for index in range(rounds + 1):
        request = os.path.join(work, f"request-{index}.json")
        destination = os.path.join(work, f"current-{index}.iprange")
        write_request(request, feed, destination)
        requests.append((request, destination))
    return requests, merged_count(ranges)


def check_output(binary, request, destination, expected):
    with open(request, "rb") as stream:
        payload = stream.read()
    proc = subprocess.Popen(
        [binary, "--jsonrpc"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    proc.stdin.write(payload)
    proc.stdin.flush()
    line = proc.stdout.readline()
    proc.stdin.close()
    proc.wait()
    if proc.returncode != 0:
        raise AssertionError(proc.stderr.read().decode("utf-8", "replace")[-500:])
    response = json.loads(line)
    if "result" not in response:
        raise AssertionError(f"import failed: {response.get('error')}")
    got = int(response["result"]["report"]["addresses"])
    if got != expected:
        raise AssertionError(f"imported {got} addresses, generator says {expected}")
    os.remove(destination)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--seed", type=int, default=7)
    parser.add_argument("--count", type=int, default=20)
    parser.add_argument("--span", type=int, default=4)
    parser.add_argument("--space", type=int, default=64)
    args = parser.parse_args()
    if not os.path.isabs(args.binary):
        return fail("binary must be absolute")
    with tempfile.TemporaryDirectory(prefix="iprange-perf-") as work:
        requests, expected = prepare(work, args.seed, args.count, args.span, args.space, args.rounds)
        check_output(args.binary, requests[0][0], requests[0][1], expected)
        samples = []
        for request, _destination in requests[1:]:
            with open(request, "rb") as stream:
                samples.append(measure([args.binary, "--jsonrpc"], 1, stream.read()))
        report = {
            "rounds": args.rounds,
            "elapsed_seconds": {
                "median": median_of(samples, "elapsed_seconds"),
                "min": min(sample["elapsed_seconds"]["min"] for sample in samples),
                "max": max(sample["elapsed_seconds"]["max"] for sample in samples),
            },
            "child_max_rss_kib": {
                "median": median_of(samples, "child_max_rss_kib"),
                "min": min(sample["child_max_rss_kib"]["min"] for sample in samples),
                "max": max(sample["child_max_rss_kib"]["max"] for sample in samples),
            },
        }
    report["expected_addresses"] = expected
    print(json.dumps(report, sort_keys=True))
    return 0


def fail(message):
    print(f"FAIL {message}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
