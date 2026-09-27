"""Measure feeds times provider sets on both product binaries.

The generator names every feed,value cell before either engine runs.
The timed command is one direct join. Publishing the feeds and loading
the providers are not part of the sample.
"""

import argparse
import ipaddress
import json
import os
import subprocess
import sys
import tempfile

from generate import generate, provider_join, resolve_direct, write_text
from measure import measure, median, ratio
from perf import fail


PUBLISH_BUDGET = {
    "max_heap_bytes": "268435456",
    "max_output_pages": "200000",
    "max_workspace_pages": "200000",
    "max_open_files": 3,
}
WRITER = {
    "max_heap_bytes": "268435456",
    "max_private_pages": "200000",
    "max_growth_pages": "200000",
    "max_open_files": 4,
}


from client import BenchSession as Session, call_json


def feed_name(index):
    return f"f{index:03d}"


def provider_rows(seed, count, span, space):
    ranges = generate(seed, count, span, space)
    return [(start, end, (index % 3) + 1) for index, (start, end) in enumerate(ranges)]


def write_csv(path, rows):
    with open(path, "w", encoding="utf-8") as stream:
        stream.write("from,to,value\n")
        for start, end, value in rows:
            stream.write(f"{ipaddress.IPv4Address(start)},{ipaddress.IPv4Address(end)},{value}\n")


def publish_feed(session, text_path, destination, name):
    session.call("iprange.v1.current.publish", {
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
        "immutable_feed_budget": PUBLISH_BUDGET,
    })


def load_database(binary, work, feeds, providers, index):
    session = Session(binary)
    try:
        membership = os.path.join(work, f"mem-{index}.iprange")
        session.call("iprange.v1.database.create", {
            "path": membership,
            "family": "ipv4",
            "value_kind": "membership",
            "structure_kind": "none",
            "value_tag": {"text": "membership"},
            "reader_capacity": 8,
        })
        published = []
        for name, ranges in feeds:
            text_path = os.path.join(work, f"{index}-{name}.txt")
            destination = os.path.join(work, f"{index}-{name}.iprange")
            with open(text_path, "w", encoding="utf-8") as stream:
                write_text(ranges, stream)
            publish_feed(session, text_path, destination, name)
            session.call("iprange.v1.feeds.create", {
                "path": membership,
                "feed": name,
                "current": {"source": {"path": destination, "mode": "immutable"}, "feed": name},
                "metadata": {"mode": "keep"},
                "writer_budget": WRITER,
            })
            published.append(destination)
        loaded = []
        for provider_index, rows in enumerate(providers):
            csv_path = os.path.join(work, f"{index}-p{provider_index}.csv")
            destination = os.path.join(work, f"{index}-p{provider_index}.iprange")
            write_csv(csv_path, rows)
            session.call("iprange.v1.database.create", {
                "path": destination,
                "family": "ipv4",
                "value_kind": "direct",
                "structure_kind": "none",
                "value_tag": {"text": f"p{provider_index}"},
                "reader_capacity": 4,
            })
            session.call("iprange.v1.direct.replace", {
                "path": destination,
                "input": {"path": csv_path, "max_line_bytes": 1024},
                "metadata": {"mode": "keep"},
                "writer_budget": WRITER,
            })
            loaded.append(destination)
        return membership, loaded
    finally:
        session.close()


def join_request(membership, provider, output):
    return {
        "jsonrpc": "2.0",
        "id": "join",
        "method": "iprange.v1.join.direct",
        "params": {
            "membership": {
                "source": {"path": membership, "mode": "live"},
                "selection": {"mode": "all"},
                "membership_query_budget": {"max_heap_bytes": "16777216"},
            },
            "direct": {"path": provider, "mode": "live"},
            "output": {
                "path": output,
                "format": "csv",
                "publication_policy": "fail_if_exists",
                "result_budget": {"max_rows": "100000", "max_output_bytes": "16777216", "max_open_files": 3},
            },
            "max_result_cells": "100000",
        },
    }


def check_join(binary, membership, provider, expected, output):
    request = join_request(membership, provider, output)
    report = call_json(binary, request)["report"]
    got = {
        "selected": int(report["selected_addresses"]),
        "mapped": int(report["mapped_addresses"]),
        "unmapped": int(report["unmapped_addresses"]),
        "cells": int(report["result_cell_count"]),
    }
    want = {
        "selected": expected["selected"],
        "mapped": expected["mapped"],
        "unmapped": expected["unmapped"],
        "cells": len(expected["cells"]),
    }
    if got != want:
        raise AssertionError(f"join totals {got}, generator says {want}")
    rows = set()
    with open(output, encoding="utf-8") as stream:
        next(stream)
        for line in stream:
            feed, value, count = line.rstrip("\n").split(",")
            rows.add((feed, None if value == "null" else int(value), int(count)))
    if rows != set(expected["cells"]):
        raise AssertionError("join cells differ from the generator")
    os.remove(output)


def sample_join(binary, membership, provider, work, rounds):
    requests = []
    for index in range(rounds):
        path = os.path.join(work, f"request-{os.path.basename(provider)}-{index}.json")
        output = os.path.join(work, f"out-{os.path.basename(provider)}-{index}.csv")
        with open(path, "w", encoding="utf-8") as stream:
            json.dump(join_request(membership, provider, output), stream, separators=(",", ":"))
            stream.write("\n")
        requests.append(path)
    samples = []
    for path in requests:
        with open(path, "rb") as stream:
            samples.append(measure([binary, "--jsonrpc"], 1, stream.read()))
    elapsed = [sample["elapsed_seconds"]["median"] for sample in samples]
    rss = [sample["child_max_rss_kib"]["median"] for sample in samples]
    return {
        "elapsed_seconds": {"median": median(elapsed), "min": min(elapsed), "max": max(elapsed)},
        "child_max_rss_kib": {"median": median(rss), "min": min(rss), "max": max(rss)},
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--feeds", type=int, default=100)
    parser.add_argument("--providers", type=int, default=3)
    parser.add_argument("--count", type=int, default=20)
    parser.add_argument("--span", type=int, default=4)
    parser.add_argument("--space", type=int, default=10000)
    parser.add_argument("--rounds", type=int, default=3)
    args = parser.parse_args()
    for label, path in (("rust", args.rust), ("go", args.go)):
        if not os.path.isabs(path):
            return fail(f"{label} binary must be absolute")
    feeds = [(feed_name(index), generate(1000 + index, args.count, args.span, args.space)) for index in range(args.feeds)]
    providers = [provider_rows(index + 1, args.count, args.span, args.space) for index in range(args.providers)]
    expected = [provider_join(feeds, rows) for rows in providers]
    with tempfile.TemporaryDirectory(prefix="iprange-providers-") as work:
        rust_mem, rust_providers = load_database(args.rust, work, feeds, providers, "rust")
        go_mem, go_providers = load_database(args.go, work, feeds, providers, "go")
        for index, want in enumerate(expected):
            check_join(args.rust, rust_mem, rust_providers[index], want, os.path.join(work, f"check-rust-{index}.csv"))
            check_join(args.go, go_mem, go_providers[index], want, os.path.join(work, f"check-go-{index}.csv"))
        report = {
            "feeds": args.feeds,
            "providers": args.providers,
            "resolved_provider_ranges": [len(resolve_direct(rows)) for rows in providers],
            "cells": [len(item["cells"]) for item in expected],
            "selected": [item["selected"] for item in expected],
            "mapped": [item["mapped"] for item in expected],
            "rounds": args.rounds,
            "rust": [sample_join(args.rust, rust_mem, path, work, args.rounds) for path in rust_providers],
            "go": [sample_join(args.go, go_mem, path, work, args.rounds) for path in go_providers],
        }
    report["ratio"] = [
        ratio(left, right) for left, right in zip(report["rust"], report["go"])
    ]
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
