"""Seeded feed generator for milestone-5 scenarios.

The generator is the ground truth. It writes text the engines import, and
it reports the merged address count before either engine runs. A scenario
that copies the input instead of merging it fails against this count.
IPv4 and IPv6 text are two encodings of the same integer ranges.
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


def resolve_direct(rows):
    # Later rows replace overlapping addresses. The direct loader applies
    # the CSV in order, so a later value wins. Value 0 is a real provider
    # id; uncovered addresses are not in this list.
    events = []
    for index, (start, end, value) in enumerate(rows):
        if end < start:
            raise ValueError("direct row is reversed")
        events.append((start, 1, index, value))
        events.append((end + 1, 0, index, 0))
    events.sort()
    active = {}
    resolved = []
    previous = None
    for point, kind, index, value in events:
        if previous is not None and point > previous and active:
            winner = max(active)
            value_now = active[winner]
            if resolved and resolved[-1][1] + 1 == previous and resolved[-1][2] == value_now:
                resolved[-1] = (resolved[-1][0], point - 1, value_now)
            else:
                resolved.append((previous, point - 1, value_now))
        if kind == 1:
            active[index] = value
        else:
            active.pop(index, None)
        previous = point
    return resolved


def provider_cells(feed_ranges, resolved):
    cells = {}
    cursor_right = 0
    for start, end in covered(feed_ranges):
        while cursor_right < len(resolved) and resolved[cursor_right][1] < start:
            cursor_right += 1
        index = cursor_right
        cursor = start
        while index < len(resolved) and resolved[index][0] <= end:
            right_start, right_end, value = resolved[index]
            if cursor < right_start:
                cells[None] = cells.get(None, 0) + right_start - cursor
                cursor = right_start
            stop = min(end, right_end)
            if cursor <= stop:
                cells[value] = cells.get(value, 0) + stop - cursor + 1
                cursor = stop + 1
            if right_end >= end:
                break
            index += 1
        if cursor <= end:
            cells[None] = cells.get(None, 0) + end - cursor + 1
    return {key: count for key, count in cells.items() if count}


def provider_join(feeds, provider_rows):
    resolved = resolve_direct(provider_rows)
    provider_cover = [(start, end) for start, end, _value in resolved]
    all_ranges = [item for _name, ranges in feeds for item in ranges]
    selected = merged_count(all_ranges) if all_ranges else 0
    mapped = overlap_count(all_ranges, provider_cover) if all_ranges and provider_cover else 0
    cells = []
    for name, ranges in feeds:
        for key, count in provider_cells(ranges, resolved).items():
            cells.append((name, key, count))
    return {
        "selected": selected,
        "mapped": mapped,
        "unmapped": selected - mapped,
        "cells": cells,
    }


def churn(days):
    steps = []
    seen = []
    for day in days:
        counts = diff_counts(seen, day) if seen else {"unchanged": 0, "removed": 0, "added": merged_count(day)}
        steps.append(counts)
        seen = day
    return steps


def _segments(stored, current):
    # stored is non-overlapping (start, end, value). current is merged
    # (start, end). Yield atomic (start, end, old_value or None, in_current).
    points = set()
    for start, end, _value in stored:
        points.add(start)
        points.add(end + 1)
    for start, end in current:
        points.add(start)
        points.add(end + 1)
    ordered = sorted(points)
    for left, right in zip(ordered, ordered[1:]):
        if right <= left:
            continue
        old = None
        for start, end, value in stored:
            if start <= left <= end:
                old = value
                break
        inside = any(start <= left <= end for start, end in current)
        yield left, right - 1, old, inside


def last_seen_refresh(stored, current, refresh_value, cutoff):
    # An address in the current coverage takes max(old, refresh_value).
    # An address outside it is kept only when its stored value is greater
    # than the cutoff. A later row does not exist here: stored ranges do
    # not overlap.
    added = removed = unchanged = changed = after = 0
    nxt = []
    for start, end, old, inside in _segments(stored, current):
        if inside:
            new = refresh_value if old is None else max(old, refresh_value)
        elif old is not None and old > cutoff:
            new = old
        else:
            new = None
        count = end - start + 1
        if old is None and new is not None:
            added += count
        elif old is not None and new is None:
            removed += count
        elif old == new and new is not None:
            unchanged += count
        elif old is not None and new is not None:
            changed += count
        if new is not None:
            after += count
            if nxt and nxt[-1][1] + 1 == start and nxt[-1][2] == new:
                nxt[-1] = (nxt[-1][0], end, new)
            else:
                nxt.append((start, end, new))
    return {
        "added": added,
        "removed": removed,
        "unchanged": unchanged,
        "changed": changed,
        "after": after,
        "stored": nxt,
    }


def last_seen_days(days, refresh_values, cutoffs):
    stored = []
    steps = []
    for current, refresh_value, cutoff in zip(days, refresh_values, cutoffs):
        step = last_seen_refresh(stored, covered(current), refresh_value, cutoff)
        stored = step.pop("stored")
        steps.append(step)
    return steps


def day_feeds(seed, days, count, span, space=2**32):
    if days < 1:
        raise ValueError("days must be positive")
    return [generate(seed + day, count, span, space) for day in range(days)]


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


IPV6_BASE = 0x20010DB8000000000000000000000000


def ipv6_address(value):
    if value < 0 or IPV6_BASE + value > 2**128 - 1:
        raise ValueError("ipv6 offset does not fit in 2001:db8::")
    return str(ipaddress.IPv6Address(IPV6_BASE + value))


def write_text(ranges, stream):
    for start, end in ranges:
        left = ipaddress.IPv4Address(start)
        right = ipaddress.IPv4Address(end)
        if start == end:
            stream.write(f"{left}\n")
        else:
            stream.write(f"{left}-{right}\n")


def write_ipv6(ranges, stream):
    for start, end in ranges:
        left = ipv6_address(start)
        right = ipv6_address(end)
        if start == end:
            stream.write(f"{left}\n")
        else:
            stream.write(f"{left}-{right}\n")


def parse_ipv6(text):
    ranges = []
    for line in text.splitlines():
        if not line:
            continue
        left, separator, right = line.partition("-")
        start = int(ipaddress.IPv6Address(left)) - IPV6_BASE
        end = start if not separator else int(ipaddress.IPv6Address(right)) - IPV6_BASE
        ranges.append((start, end))
    return ranges


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seed", type=int, required=True)
    parser.add_argument("--count", type=int, required=True)
    parser.add_argument("--span", type=int, default=4)
    parser.add_argument("--space", type=int, default=2**32)
    parser.add_argument("--family", choices=("ipv4", "ipv6"), default="ipv4")
    parser.add_argument("--text")
    parser.add_argument("--report")
    args = parser.parse_args()
    ranges = generate(args.seed, args.count, args.span, args.space)
    report = {
        "seed": args.seed,
        "family": args.family,
        "input_records": len(ranges),
        "merged_ranges": len(covered(ranges)),
        "merged_addresses": merged_count(ranges),
    }
    if args.text:
        writer = write_ipv6 if args.family == "ipv6" else write_text
        with open(args.text, "w", encoding="utf-8") as stream:
            writer(ranges, stream)
    if args.report:
        with open(args.report, "w", encoding="utf-8") as stream:
            json.dump(report, stream, indent=2)
            stream.write("\n")
    else:
        print(json.dumps(report))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
