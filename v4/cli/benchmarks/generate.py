"""Seeded IPv4 feed generator for milestone-5 scenarios.

The generator is the ground truth. It writes text the engines import, and
it reports the merged address count before either engine runs. A scenario
that copies the input instead of merging it fails against this count.
"""

import argparse
import ipaddress
import json
import random


def generate(seed, count, span, space=2**32):
    if count < 1 or span < 1 or span > 256 or space <= span:
        raise ValueError("count must be positive, span must be 1..256, and space must hold one range")
    rng = random.Random(seed)
    ranges = []
    for _ in range(count):
        start = rng.randrange(0, space - span)
        ranges.append((start, start + span - 1))
    return ranges


def merged_count(ranges):
    ordered = sorted(ranges)
    total = 0
    current_start, current_end = ordered[0]
    for start, end in ordered[1:]:
        if start <= current_end + 1:
            current_end = max(current_end, end)
            continue
        total += current_end - current_start + 1
        current_start, current_end = start, end
    return total + current_end - current_start + 1


def write_text(ranges, stream):
    for start, end in ranges:
        left = ipaddress.IPv4Address(start)
        right = ipaddress.IPv4Address(end)
        if start == end:
            stream.write(f"{left}\n")
        else:
            stream.write(f"{left}-{right}\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seed", type=int, required=True)
    parser.add_argument("--count", type=int, required=True)
    parser.add_argument("--span", type=int, default=4)
    parser.add_argument("--space", type=int, default=2**32)
    parser.add_argument("--text")
    parser.add_argument("--report")
    args = parser.parse_args()
    ranges = generate(args.seed, args.count, args.span, args.space)
    report = {
        "seed": args.seed,
        "input_records": len(ranges),
        "merged_addresses": merged_count(ranges),
    }
    if args.text:
        with open(args.text, "w", encoding="utf-8") as stream:
            write_text(ranges, stream)
    if args.report:
        with open(args.report, "w", encoding="utf-8") as stream:
            json.dump(report, stream, indent=2)
            stream.write("\n")
    else:
        print(json.dumps(report))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
