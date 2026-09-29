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

from client import BenchSession
from generate import generate, merged_count, write_ipv6, write_text
from measure import median, ratio, run_once


def median_of(samples, key):
    # run_once samples carry flat scalars (the per-request aggregate of
    # one round is the round); the median spans the request population.
    return median([sample[key] for sample in samples])


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
            "max_heap_bytes": "268435456",
            "max_output_pages": "200000",
            "max_workspace_pages": "200000",
            "max_open_files": 3,
        },
    },
}


def write_request(path, feed, destination, family):
    request = json.loads(json.dumps(PUBLISH))
    request["params"]["input"]["paths"] = [feed]
    request["params"]["input"]["family"] = family
    request["params"]["input"]["default_prefix"] = 128 if family == "ipv6" else 32
    request["params"]["destination"] = destination
    with open(path, "w", encoding="utf-8") as stream:
        json.dump(request, stream, separators=(",", ":"))
        stream.write("\n")


def prepare(work, seed, count, span, space, requests, family):
    ranges = generate(seed, count, span, space)
    feed = os.path.join(work, "feed.txt")
    with open(feed, "w", encoding="utf-8") as stream:
        (write_ipv6 if family == "ipv6" else write_text)(ranges, stream)
    paths = []
    for index in range(requests):
        request = os.path.join(work, f"request-{index}.json")
        destination = os.path.join(work, f"current-{index}.iprange")
        write_request(request, feed, destination, family)
        paths.append((request, destination))
    return paths, merged_count(ranges)


def check_output(binary, request, destination, expected):
    with open(request, "r", encoding="utf-8") as stream:
        payload = json.load(stream)
    session = BenchSession(binary)
    try:
        response = session.raw(payload["method"], payload["params"])
    finally:
        session.close()
    if "result" not in response:
        raise AssertionError(f"import failed: {response.get('error')}")
    got = int(response["result"]["report"]["addresses"])
    if got != expected:
        raise AssertionError(f"imported {got} addresses, generator says {expected}")
    os.remove(destination)


def check_sample(sample, request, destination, expected):
    """A timed sample is only valid when the operation did its work.

    The publish answered (a non-empty result whose report matches the
    generator) and the destination exists on disk. Without this, a
    warm-up could validate the workload while every timed round
    silently failed into a null/empty result.
    """
    response = sample.get("response")
    if not isinstance(response, dict) or "result" not in response:
        raise AssertionError(f"timed publish did not answer: {response!r}")
    result = response["result"]
    got = int(result["report"]["addresses"])
    if got != expected:
        raise AssertionError(
            f"timed publish imported {got} addresses, generator says {expected}")
    if not os.path.isfile(destination):
        raise AssertionError(
            f"timed publish reported success but wrote no destination: "
            f"{destination}")
    os.remove(destination)


def sample_binary(binary, requests, check=None):
    """Time one operation per request, validating every sample.

    `check` receives the sample (whose "response" is the timed
    operation's parsed result frame) and raises on a semantic
    mismatch; None means the structural validation validate_response
    already applied (non-null, non-empty result) is the whole
    contract for this operation.
    """
    samples = []
    for request, destination in requests:
        with open(request, "rb") as stream:
            sample = run_once([binary, "--jsonrpc"], stream.read())
        if not sample["child_raised_peak"]:
            raise AssertionError("child did not raise the process peak; sample is inherited")
        if check is not None:
            check(sample, request, destination)
        samples.append(sample)
    return {
        "elapsed_seconds": {
            "median": median_of(samples, "elapsed_seconds"),
            "min": min(sample["elapsed_seconds"] for sample in samples),
            "max": max(sample["elapsed_seconds"] for sample in samples),
        },
        "child_max_rss_kib": {
            "median": median_of(samples, "child_max_rss_kib"),
            "min": min(sample["child_max_rss_kib"] for sample in samples),
            "max": max(sample["child_max_rss_kib"] for sample in samples),
        },
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--seed", type=int, default=7)
    parser.add_argument("--count", type=int, default=20)
    parser.add_argument("--span", type=int, default=4)
    parser.add_argument("--space", type=int, default=64)
    parser.add_argument("--family", choices=("ipv4", "ipv6"), default="ipv4")
    args = parser.parse_args()
    for label, path in (("rust", args.rust), ("go", args.go)):
        if not os.path.isabs(path):
            return fail(f"{label} binary must be absolute")
    with tempfile.TemporaryDirectory(prefix="iprange-perf-") as work:
        requests, expected = prepare(
            work, args.seed, args.count, args.span, args.space, 2 + 2 * args.rounds, args.family
        )
        check_output(args.rust, requests[0][0], requests[0][1], expected)
        check_output(args.go, requests[1][0], requests[1][1], expected)
        # Exactly two warm-ups plus N samples per engine: the medians
        # compare equal sample populations.
        rust_requests = requests[2:2 + args.rounds]
        go_requests = requests[2 + args.rounds:2 + 2 * args.rounds]
        if len(rust_requests) != args.rounds or len(go_requests) != args.rounds:
            raise AssertionError("sample populations differ between engines")
        def check_publish(sample, request, destination):
            check_sample(sample, request, destination, expected)

        report = {
            "rounds": args.rounds,
            "rust": sample_binary(args.rust, rust_requests, check=check_publish),
            "go": sample_binary(args.go, go_requests, check=check_publish),
        }
    report["family"] = args.family
    report["expected_addresses"] = expected
    report["ratio"] = ratio(report["rust"], report["go"])
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
