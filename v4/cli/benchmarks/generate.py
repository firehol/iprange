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


def covered(ranges):
    ordered = sorted(ranges)
    merged = []
    current_start, current_end = ordered[0]
    for start, end in ordered[1:]:
        if start <= current_end + 1:
            current_end = max(current_end, end)
            continue
        merged.append((current_start, current_end))
        current_start, current_end = start, end
    merged.append((current_start, current_end))
    return merged


def diff_counts(before, after):
    left = covered(before)
    right = covered(after)
    unchanged = 0
    removed = 0
    added = 0
    i = 0
    j = 0
    while i < len(left) or j < len(right):
        if j == len(right) or (i < len(left) and left[i][1] < right[j][0]):
            removed += left[i][1] - left[i][0] + 1
            i += 1
            continue
        if i == len(left) or right[j][1] < left[i][0]:
            added += right[j][1] - right[j][0] + 1
            j += 1
            continue
        start = max(left[i][0], right[j][0])
        end = min(left[i][1], right[j][1])
        if start <= end:
            unchanged += end - start + 1
        if left[i][1] <= right[j][1]:
            if left[i][0] < start:
                removed += start - left[i][0]
            if right[j][0] < start:
                added += start - right[j][0]
            i += 1
            if end == right[j][1]:
                j += 1
            else:
                right[j] = (end + 1, right[j][1])
        else:
            if right[j][0] < start:
                added += start - right[j][0]
            if left[i][0] < start:
                removed += start - left[i][0]
            j += 1
            left[i] = (end + 1, left[i][1])
    return {"unchanged": unchanged, "removed": removed, "added": added}


def retained_count(ranges, cutoff):
    # Later rows replace overlapping addresses. The direct loader applies
    # the CSV in order, so a later value wins. The sweep is one pass over
    # range endpoints, not one pass over addresses.
    events = []
    for index, (start, end, value) in enumerate(ranges):
        events.append((start, 1, index, value))
        events.append((end + 1, 0, index, value))
    events.sort()
    active = {}
    total = 0
    previous = None
    for point, kind, index, value in events:
        if previous is not None and point > previous and active:
            winner = max(active)
            if active[winner] > cutoff:
                total += point - previous
        if kind == 1:
            active[index] = value
        else:
            active.pop(index, None)
        previous = point
    return total


def churn(days):
    steps = []
    seen = []
    for day in days:
        counts = diff_counts(seen, day) if seen else {"unchanged": 0, "removed": 0, "added": merged_count(day)}
        steps.append(counts)
        seen = day
    return steps


def overlap_count(left, right):
    return diff_counts(left, right)["unchanged"]


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


def ipv6_text(index, span):
    start = index * span
    end = start + span - 1
    return f"2001:db8::{start:x}-2001:db8::{end:x}\n"


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
