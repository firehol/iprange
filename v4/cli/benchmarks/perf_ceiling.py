"""One close run: 1,000 feeds and one 10M-range import.

The generator names the merged address count before either engine runs.
Both engines must report that count and the same active feed count.
This is one workstation run, not a ratio verdict.
"""

import argparse
import json
import os
import shutil
import sys
import tempfile
import time

from client import BenchSession
from generate import generate, merged_count, write_text
from measure import child_cpu_seconds, child_hwm_kib, median, ratio
from perf import PUBLISH, fail


WRITER = {
    "max_heap_bytes": "268435456",
    "max_private_pages": "200000",
    "max_growth_pages": "200000",
    "max_open_files": 4,
}




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


def sample_peak(proc, peak):
    current = child_hwm_kib(proc.pid)
    if current is None:
        return peak
    return max(peak, current)


def sample_cpu(proc, cpu):
    current = child_cpu_seconds(proc.pid)
    if current is None:
        return cpu
    return max(cpu, current)


def prepare_feed_texts(work, feeds):
    prepared = []
    for name, ranges in feeds:
        text_path = os.path.join(work, f"{name}.txt")
        with open(text_path, "w", encoding="utf-8") as stream:
            write_text(ranges, stream)
        prepared.append((name, text_path, merged_count(ranges)))
    return prepared


def load_feeds(binary, work, feeds, wide=None):
    prepared = prepare_feed_texts(work, feeds)
    started = time.perf_counter()
    session = BenchSession(binary)
    peak = 0
    cpu = 0.0
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
        for name, text_path, expected_count in prepared:
            peak = sample_peak(session.proc, peak)
            cpu = sample_cpu(session.proc, cpu)
            destination = os.path.join(work, f"{name}.iprange")
            published = publish_one(session, text_path, destination, name, budget)
            if int(published["report"]["addresses"]) != expected_count:
                raise AssertionError(f"{name} import did not match the generator")
            session.call("iprange.v1.feeds.create", {
                "path": membership,
                "feed": name,
                "current": {"source": {"path": destination, "mode": "immutable"}, "feed": name},
                "metadata": {"mode": "keep"},
                "writer_budget": WRITER,
            })
        wide_addresses = None
        if wide is not None:
            text_path, expected = wide
            peak = sample_peak(session.proc, peak)
            cpu = sample_cpu(session.proc, cpu)
            destination = os.path.join(work, "wide.iprange")
            published = publish_one(
                session, text_path, destination, "wide",
                PUBLISH["params"]["immutable_feed_budget"])
            peak = sample_peak(session.proc, peak)
            cpu = sample_cpu(session.proc, cpu)
            wide_addresses = int(published["report"]["addresses"])
            if wide_addresses != expected:
                raise AssertionError(
                    f"wide import {wide_addresses}, generator says {expected}")
            session.call("iprange.v1.feeds.create", {
                "path": membership,
                "feed": "wide",
                "current": {"source": {"path": destination, "mode": "immutable"}, "feed": "wide"},
                "metadata": {"mode": "keep"},
                "writer_budget": WRITER,
            })
        info = session.call("iprange.v1.database.info", {
            "source": {"path": membership, "mode": "live"},
        })
        peak = sample_peak(session.proc, peak)
        cpu = sample_cpu(session.proc, cpu)
        return {
            "feeds": int(info["info"]["active_feed_count"]),
            "wide_addresses": wide_addresses,
            "elapsed_seconds": time.perf_counter() - started,
            "child_cpu_seconds": cpu,
            "child_max_rss_kib": peak,
        }
    finally:
        session.close()


def import_ranges(binary, work, ranges, expected):
    text_path = os.path.join(work, "wide.txt")
    destination = os.path.join(work, "wide.iprange")
    with open(text_path, "w", encoding="utf-8") as stream:
        write_text(ranges, stream)
    started = time.perf_counter()
    session = BenchSession(binary)
    peak = 0
    cpu = 0.0
    try:
        peak = sample_peak(session.proc, peak)
        cpu = sample_cpu(session.proc, cpu)
        published = publish_one(
            session, text_path, destination, "wide",
            PUBLISH["params"]["immutable_feed_budget"])
        peak = sample_peak(session.proc, peak)
        cpu = sample_cpu(session.proc, cpu)
    finally:
        session.close()
    got = int(published["report"]["addresses"])
    if got != expected:
        raise AssertionError(f"imported {got} addresses, generator says {expected}")
    return {
        "addresses": got,
        "elapsed_seconds": time.perf_counter() - started,
        "child_cpu_seconds": cpu,
        "child_max_rss_kib": peak,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--feeds", type=int, default=1000)
    parser.add_argument("--ranges", type=int, default=10000000)
    parser.add_argument("--span", type=int, default=4)
    parser.add_argument("--space", type=int, default=100000000)
    parser.add_argument("--combined", action="store_true")
    parser.add_argument("--rounds", type=int, default=1)
    args = parser.parse_args()
    for label, path in (("rust", args.rust), ("go", args.go)):
        if not os.path.isabs(path):
            return fail(f"{label} binary must be absolute")
    if args.rounds < 1:
        return fail("rounds must be positive")
    small_count = args.feeds - 1 if args.combined else args.feeds
    if small_count < 1:
        return fail("combined mode needs at least 2 feeds")
    feeds = [(feed_name(index), [(index, index)]) for index in range(small_count)]
    wide = generate(7, args.ranges, args.span, args.space)
    expected = merged_count(wide)
    def spread(samples):
        elapsed = [sample["elapsed_seconds"] for sample in samples]
        cpu = [sample["child_cpu_seconds"] for sample in samples]
        rss = [sample["child_max_rss_kib"] for sample in samples]
        if any(value <= 0 for value in cpu):
            raise AssertionError("a ceiling round recorded no cpu sample")
        return {
            "rounds": len(samples),
            "elapsed_seconds": {"median": median(elapsed), "min": min(elapsed), "max": max(elapsed)},
            "child_cpu_seconds": {"median": median(cpu), "min": min(cpu), "max": max(cpu)},
            "child_max_rss_kib": {"median": median(rss), "min": min(rss), "max": max(rss)},
        }

    with tempfile.TemporaryDirectory(prefix="iprange-ceiling-") as work:
        if args.combined:
            wide_text = os.path.join(work, "wide.txt")
            with open(wide_text, "w", encoding="utf-8") as stream:
                write_text(wide, stream)
            samples = {"rust": [], "go": []}
            for index in range(args.rounds):
                order = [("rust", args.rust), ("go", args.go)]
                if index % 2:
                    order.reverse()
                for label, binary in order:
                    sample_dir = os.path.join(work, f"{label}-{index}")
                    os.makedirs(sample_dir)
                    local_wide = os.path.join(sample_dir, "wide.txt")
                    shutil.copyfile(wide_text, local_wide)
                    sample = load_feeds(binary, sample_dir, feeds, wide=(local_wide, expected))
                    if sample["feeds"] != args.feeds or sample["wide_addresses"] != expected:
                        raise AssertionError(
                            f"{label} round {index} feeds={sample['feeds']} "
                            f"addresses={sample['wide_addresses']}")
                    samples[label].append(sample)
            rust_summary = spread(samples["rust"])
            go_summary = spread(samples["go"])
            report = {
                "combined": True,
                "feeds": args.feeds,
                "ranges": args.ranges,
                "merged_addresses": expected,
                "rust_observed": {
                    "feeds": samples["rust"][-1]["feeds"],
                    "addresses": samples["rust"][-1]["wide_addresses"],
                },
                "go_observed": {
                    "feeds": samples["go"][-1]["feeds"],
                    "addresses": samples["go"][-1]["wide_addresses"],
                },
                "rust": rust_summary,
                "go": go_summary,
                "ratio": ratio(rust_summary, go_summary),
            }
        else:
            rust_feed_dir = os.path.join(work, "rust-feeds")
            go_feed_dir = os.path.join(work, "go-feeds")
            rust_wide_dir = os.path.join(work, "rust-wide")
            go_wide_dir = os.path.join(work, "go-wide")
            for path in (rust_feed_dir, go_feed_dir, rust_wide_dir, go_wide_dir):
                os.makedirs(path)
            rust_feeds = load_feeds(args.rust, rust_feed_dir, feeds)
            go_feeds = load_feeds(args.go, go_feed_dir, feeds)
            if rust_feeds["feeds"] != args.feeds or go_feeds["feeds"] != args.feeds:
                raise AssertionError(
                    f"active feeds rust={rust_feeds} go={go_feeds}, want {args.feeds}")
            rust_ranges = import_ranges(args.rust, rust_wide_dir, wide, expected)
            go_ranges = import_ranges(args.go, go_wide_dir, wide, expected)
            report = {
                "combined": False,
                "feeds": args.feeds,
                "ranges": args.ranges,
                "merged_addresses": expected,
                "rust_feeds": rust_feeds,
                "go_feeds": go_feeds,
                "rust_import": rust_ranges,
                "go_import": go_ranges,
                "import_ratio": ratio(
                    {"elapsed_seconds": {"median": rust_ranges["elapsed_seconds"]},
                     "child_cpu_seconds": {"median": rust_ranges["child_cpu_seconds"]},
                     "child_max_rss_kib": {"median": rust_ranges["child_max_rss_kib"]}},
                    {"elapsed_seconds": {"median": go_ranges["elapsed_seconds"]},
                     "child_cpu_seconds": {"median": go_ranges["child_cpu_seconds"]},
                     "child_max_rss_kib": {"median": go_ranges["child_max_rss_kib"]}},
                ),
            }
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
