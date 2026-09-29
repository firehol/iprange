"""Measure one day of a seven-day first-seen refresh.

The generator names every day's diff before either engine runs. The
timed command is the seventh refresh. Publishing the days and applying
the first six refreshes are not part of the sample.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile

from client import call_json
from generate import churn, day_feeds, merged_count, write_text
from measure import ratio
from perf import PUBLISH, fail, sample_binary

EXPECTED_LAST_STEP = None  # set by main() before sampling


def check_churn_sample(sample, request, destination):
    """Every timed final-day refresh must report the generator's diff."""
    del request, destination
    report = sample["response"]["result"]["report"]
    got = {
        "unchanged": int(report["unchanged_value_addresses"]),
        "removed": int(report["removed_addresses"]),
        "added": int(report["added_addresses"]),
    }
    if got != EXPECTED_LAST_STEP:
        raise AssertionError(f"timed refresh diff {got}, generator says {EXPECTED_LAST_STEP}")


WRITER = {
    "max_heap_bytes": "268435456",
    "max_private_pages": "200000",
    "max_growth_pages": "200000",
    "max_open_files": 4,
}


def call(binary, payload):
    return call_json(binary, payload)


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


def create_seen(binary, path):
    request = {
        "jsonrpc": "2.0",
        "id": "create",
        "method": "iprange.v1.database.create",
        "params": {
            "path": path,
            "family": "ipv4",
            "value_kind": "direct",
            "structure_kind": "none",
            "value_tag": {"text": "first_seen"},
            "reader_capacity": 4,
        },
    }
    call(binary, json.dumps(request).encode("utf-8") + b"\n")


def refresh_request(database_path, current, value):
    return {
        "jsonrpc": "2.0",
        "id": "refresh",
        "method": "iprange.v1.retention.first_seen.refresh",
        "params": {
            "path": database_path,
            "current": {"source": {"path": current, "mode": "immutable"}, "feed": "alpha"},
            "refresh_value": value,
            "metadata": {"mode": "keep"},
            "writer_budget": WRITER,
        },
    }


def apply_refresh(binary, database_path, current, value):
    request = refresh_request(database_path, current, value)
    return call(binary, json.dumps(request).encode("utf-8") + b"\n")


def counts(report):
    return {
        "unchanged": int(report["unchanged_value_addresses"]),
        "removed": int(report["removed_addresses"]),
        "added": int(report["added_addresses"]),
    }


def prepare_days(binary, work, days, index):
    published = []
    for day_index, ranges in enumerate(days, start=1):
        text = os.path.join(work, f"day-{index}-{day_index}.txt")
        destination = os.path.join(work, f"day-{index}-{day_index}.iprange")
        with open(text, "w", encoding="utf-8") as stream:
            write_text(ranges, stream)
        publish(binary, text, destination, work, f"{index}-{day_index}")
        published.append(destination)
    return published


def check_corpus(binary, work, days, steps, index):
    published = prepare_days(binary, work, days, index)
    database_path = os.path.join(work, f"seen-{index}.iprange")
    create_seen(binary, database_path)
    for day_index, (current, expected) in enumerate(zip(published, steps), start=1):
        got = counts(apply_refresh(binary, database_path, current, day_index)["report"])
        if got != expected:
            raise AssertionError(f"day {day_index} diff {got}, generator says {expected}")


def prepare_sample(binary, work, days, index):
    published = prepare_days(binary, work, days, f"sample-{index}")
    database_path = os.path.join(work, f"sample-{index}.iprange")
    create_seen(binary, database_path)
    for day_index, current in enumerate(published[:-1], start=1):
        apply_refresh(binary, database_path, current, day_index)
    request = os.path.join(work, f"refresh-{index}.json")
    payload = refresh_request(database_path, published[-1], len(days))
    with open(request, "w", encoding="utf-8") as stream:
        json.dump(payload, stream, separators=(",", ":"))
        stream.write("\n")
    return request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--days", type=int, default=7)
    parser.add_argument("--count", type=int, default=1000)
    parser.add_argument("--span", type=int, default=4)
    parser.add_argument("--space", type=int, default=10000)
    parser.add_argument("--seed", type=int, default=11)
    args = parser.parse_args()
    for label, path in (("rust", args.rust), ("go", args.go)):
        if not os.path.isabs(path):
            return fail(f"{label} binary must be absolute")
    days = day_feeds(args.seed, args.days, args.count, args.span, args.space)
    global EXPECTED_LAST_STEP
    steps = churn(days)
    EXPECTED_LAST_STEP = steps[-1]
    with tempfile.TemporaryDirectory(prefix="iprange-churn-") as work:
        check_corpus(args.rust, work, days, steps, "rust-check")
        check_corpus(args.go, work, days, steps, "go-check")
        rust_requests = [prepare_sample(args.rust, work, days, index) for index in range(args.rounds)]
        go_requests = [prepare_sample(args.go, work, days, 100 + index) for index in range(args.rounds)]
        report = {
            "days": args.days,
            "count": args.count,
            "expected": steps,
            "merged_addresses": [merged_count(day) for day in days],
            "rounds": args.rounds,
            "rust": sample_binary(args.rust, [(path, None) for path in rust_requests],
                                  check=check_churn_sample),
            "go": sample_binary(args.go, [(path, None) for path in go_requests],
                                check=check_churn_sample),
        }
    report["ratio"] = ratio(report["rust"], report["go"])
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
