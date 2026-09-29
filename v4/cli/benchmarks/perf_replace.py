"""Measure one feed replacement on both product binaries.

The generator computes the diff before either engine runs. The timed
command is the release binary performing the replacement. The import
that builds the two days is not part of the sample.
"""

import json
import os
import subprocess
import sys
import tempfile

from client import call_json
from generate import diff_counts, generate, write_text
from measure import measure, median, ratio
from perf import PUBLISH, fail, sample_binary


EXPECTED_DIFF = None  # set by main() before sampling


def check_replace_sample(sample, request, destination):
    """Every timed replace must report the generator's diff counts."""
    del request, destination
    report = sample["response"]["result"]["report"]
    got = {
        "unchanged": int(report["unchanged_value_addresses"]),
        "removed": int(report["removed_addresses"]),
        "added": int(report["added_addresses"]),
    }
    if got != EXPECTED_DIFF:
        raise AssertionError(f"timed replace diff {got}, generator says {EXPECTED_DIFF}")


def publish(binary, feed, destination, work, index):
    request = json.loads(json.dumps(PUBLISH))
    request["params"]["input"]["paths"] = [feed]
    request["params"]["destination"] = destination
    path = os.path.join(work, f"publish-{index}.json")
    with open(path, "w", encoding="utf-8") as stream:
        json.dump(request, stream, separators=(",", ":"))
        stream.write("\n")
    with open(path, "rb") as stream:
        call_json(binary, stream.read())


def database(binary, path):
    request = {
        "jsonrpc": "2.0",
        "id": "create",
        "method": "iprange.v1.database.create",
        "params": {
            "path": path,
            "family": "ipv4",
            "value_kind": "membership",
            "structure_kind": "none",
            "value_tag": {"text": "membership"},
            "reader_capacity": 4,
        },
    }
    call_json(binary, request)


def create_feed(binary, database_path, day1, work):
    request = {
        "jsonrpc": "2.0",
        "id": "create-feed",
        "method": "iprange.v1.feeds.create",
        "params": {
            "path": database_path,
            "feed": "alpha",
            "current": {"source": {"path": day1, "mode": "immutable"}, "feed": "alpha"},
            "metadata": {"mode": "keep"},
            "writer_budget": PUBLISH["params"]["immutable_feed_budget"] | {
                "max_private_pages": "200000",
                "max_growth_pages": "200000",
                "max_open_files": 4,
            },
        },
    }
    request["params"]["writer_budget"].pop("max_output_pages", None)
    request["params"]["writer_budget"].pop("max_workspace_pages", None)
    path = os.path.join(work, "create-feed.json")
    with open(path, "w", encoding="utf-8") as stream:
        json.dump(request, stream, separators=(",", ":"))
        stream.write("\n")
    with open(path, "rb") as stream:
        call_json(binary, stream.read())


def call(binary, payload):
    return call_json(binary, payload)


def prepare_replace(binary, work, before, after, index):
    day1 = os.path.join(work, f"day1-{index}.txt")
    day2 = os.path.join(work, f"day2-{index}.txt")
    with open(day1, "w", encoding="utf-8") as stream:
        write_text(before, stream)
    with open(day2, "w", encoding="utf-8") as stream:
        write_text(after, stream)
    published1 = os.path.join(work, f"day1-{index}.iprange")
    published2 = os.path.join(work, f"day2-{index}.iprange")
    database_path = os.path.join(work, f"db-{index}.iprange")
    publish(binary, day1, published1, work, f"{index}-a")
    publish(binary, day2, published2, work, f"{index}-b")
    database(binary, database_path)
    create_feed(binary, database_path, published1, work)
    request = os.path.join(work, f"replace-{index}.json")
    payload = {
        "jsonrpc": "2.0",
        "id": "replace",
        "method": "iprange.v1.feeds.replace",
        "params": {
            "path": database_path,
            "feed": "alpha",
            "current": {"source": {"path": published2, "mode": "immutable"}, "feed": "alpha"},
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


def main():
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--count", type=int, default=1000)
    parser.add_argument("--span", type=int, default=4)
    parser.add_argument("--space", type=int, default=10000)
    args = parser.parse_args()
    before = generate(1, args.count, args.span, args.space)
    after = generate(2, args.count, args.span, args.space)
    global EXPECTED_DIFF
    expected = diff_counts(before, after)
    EXPECTED_DIFF = expected
    def check(binary, work, index):
        request = prepare_replace(binary, work, before, after, index)
        with open(request, "rb") as stream:
            result = call(binary, stream.read())
        report = result["report"]
        got = {
            "unchanged": int(report["unchanged_value_addresses"]),
            "removed": int(report["removed_addresses"]),
            "added": int(report["added_addresses"]),
        }
        if got != expected:
            raise AssertionError(f"replace diff {got}, generator says {expected}")
        return request

    with tempfile.TemporaryDirectory(prefix="iprange-replace-") as work:
        check(args.rust, work, 0)
        check(args.go, work, 1)
        rust_requests = [prepare_replace(args.rust, work, before, after, 10 + index) for index in range(args.rounds)]
        go_requests = [prepare_replace(args.go, work, before, after, 100 + index) for index in range(args.rounds)]
        report = {
            "expected": expected,
            "rounds": args.rounds,
            "rust": sample_binary(args.rust, [(path, None) for path in rust_requests],
                                  check=check_replace_sample),
            "go": sample_binary(args.go, [(path, None) for path in go_requests],
                                check=check_replace_sample),
        }
    report["ratio"] = ratio(report["rust"], report["go"])
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
