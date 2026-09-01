"""Independent scalar interval oracle for external iprange cases.

The model uses inclusive integer intervals and arbitrary scalar values.  It
intentionally knows nothing about v4 files or SDK APIs: callers translate wire
addresses to integers and compare the oracle's semantic result with responses.
"""

from dataclasses import dataclass
from typing import Callable, Dict, Iterable, List, Sequence, Tuple


@dataclass(frozen=True)
class Interval:
    """One inclusive integer interval carrying one semantic value."""

    start: int
    end: int
    value: object = None

    def __post_init__(self):
        if self.start < 0 or self.end < self.start:
            raise ValueError(f"invalid inclusive interval [{self.start}, {self.end}]")


IntervalLike = Sequence[Tuple[int, int]]
Intervals = Sequence[Interval]


def normalize_intervals(intervals: Intervals) -> List[Interval]:
    """Sort intervals and join touching/overlapping equal-value intervals.

    Overlapping different values are ambiguous in a final valued state and are
    rejected instead of being silently resolved by an implementation rule.
    """

    ordered = sorted(intervals, key=lambda item: (item.start, item.end))
    result: List[Interval] = []
    for item in ordered:
        if not result:
            result.append(item)
            continue
        previous = result[-1]
        if item.start <= previous.end + 1 and item.value == previous.value:
            result[-1] = Interval(previous.start, max(previous.end, item.end), item.value)
        elif item.start > previous.end:
            result.append(item)
        else:
            raise ValueError(
                f"overlapping intervals have different values at {item.start}: "
                f"{previous.value!r} and {item.value!r}"
            )
    return result


def assigned_intervals(assignments: Intervals) -> List[Interval]:
    """Apply ordered assignments with later values replacing earlier values."""

    result: List[Interval] = []
    for assignment in assignments:
        start, end, value = assignment.start, assignment.end, assignment.value
        remaining = [Interval(start, end, value)]
        next_result: List[Interval] = []
        for old in result:
            # Pieces strictly outside the replacement survive unchanged.
            if old.end < start:
                next_result.append(old)
                continue
            if old.start > end:
                next_result.append(old)
                continue
            if old.start < start:
                next_result.append(Interval(old.start, start - 1, old.value))
            if old.end > end:
                next_result.append(Interval(end + 1, old.end, old.value))
        # Preserve arrival order only within the new assignment; final sorting
        # and equal-value coalescing happens after every assignment is applied.
        result = next_result + remaining
    return normalize_intervals(result)


def _boolean_intervals(
    inputs: Sequence[IntervalLike],
    predicate: Callable[[Tuple[bool, ...]], bool],
) -> List[Tuple[int, int]]:
    events: Dict[int, List[int]] = {}
    for index, intervals in enumerate(inputs):
        for raw in intervals:
            if len(raw) < 2:
                raise ValueError(f"invalid interval tuple {raw!r}")
            start, end = raw[0], raw[1]
            if end < start:
                raise ValueError(f"invalid interval [{start}, {end}]")
            events.setdefault(start, [0] * len(inputs))[index] += 1
            events.setdefault(end + 1, [0] * len(inputs))[index] -= 1

    active = [0] * len(inputs)
    segment_start = None
    result: List[Tuple[int, int]] = []
    for point, deltas in sorted(events.items()):
        active = [value + delta for value, delta in zip(active, deltas)]
        if predicate(tuple(value > 0 for value in active)):
            if segment_start is None:
                segment_start = point
        elif segment_start is not None:
            result.append((segment_start, point - 1))
            segment_start = None
    return result


def union(intervals: Iterable[IntervalLike]) -> List[Tuple[int, int]]:
    """Return the ordered disjoint union of inclusive integer intervals."""

    return _boolean_intervals(list(intervals), lambda active: any(active))


def intersection(intervals: Iterable[IntervalLike]) -> List[Tuple[int, int]]:
    """Intersect all input interval lists; empty input is the empty set."""

    inputs = list(intervals)
    if not inputs:
        return []
    return _boolean_intervals(inputs, lambda active: all(active))


def exclude(left: IntervalLike, right: IntervalLike) -> List[Tuple[int, int]]:
    """Return addresses covered by left and not by right."""

    return _boolean_intervals([left, right], lambda active: active[0] and not active[1])


def compare(left: IntervalLike, right: IntervalLike) -> dict:
    """Return exact comparison facts for two address sets."""

    left_total = union([left])
    right_total = union([right])
    common = intersection([left, right])
    left_only = exclude(left, right)
    right_only = exclude(right, left)
    both = union([left_only, common, right_only])
    return {
        # Side totals include each side's complete selected address set: its
        # overlap segments plus its side-only segments, matching the SDK's
        # analytical comparison contract.
        "left_addresses": address_count(left_total),
        "right_addresses": address_count(right_total),
        "overlap_addresses": address_count(common),
        "left_only_addresses": address_count(left_only),
        "right_only_addresses": address_count(right_only),
        "union_addresses": address_count(both),
        "equal": not left_only and not right_only,
    }


def address_count(intervals: IntervalLike) -> int:
    """Count inclusive addresses in an interval list."""

    return sum(end - start + 1 for start, end in intervals)


def lookup(intervals: Intervals, point: int) -> object:
    """Return the unique value containing point, or None when absent."""

    matches = [item for item in intervals if item.start <= point <= item.end]
    if not matches:
        return None
    if len(matches) > 1 and any(item.value != matches[0].value for item in matches[1:]):
        raise ValueError(f"ambiguous valued state at {point}")
    return matches[0].value


def lookup_fact(intervals: Intervals, point: int) -> dict:
    """Return the wire-shaped lookup fact for a valued interval state."""

    value = lookup(intervals, point)
    if value is None:
        return {"present": False}
    return {"present": True, "value": value}


def algebra_count(operation: str, sources: Sequence[IntervalLike], **_ignored) -> Tuple[List[Tuple[int, int]], int]:
    """Evaluate count, union, intersection, or exclusion address algebra."""

    if not sources:
        return [], 0
    if operation == "union":
        result = union(sources)
    elif operation == "intersection":
        result = intersection(sources)
    elif operation == "exclusion":
        if len(sources) != 2:
            raise ValueError("exclusion requires included and excluded sources")
        result = exclude(sources[0], sources[1])
    else:
        raise ValueError(f"unknown interval operation {operation!r}")
    return result, address_count(result)


def _self_test():
    """Focused hand-computed algebra oracle checks."""

    _, count = algebra_count("union", [[(0, 9)], [(5, 14)]])
    assert count == 15
    _, count = algebra_count("union", [[(0, 4)], [(10, 14)]])
    assert count == 10
    _, count = algebra_count("intersection", [[(0, 9)], [(5, 14)]])
    assert count == 5
    _, count = algebra_count("exclusion", [[(0, 9)], [(5, 14)]])
    assert count == 5

    facts = compare([(0, 9)], [(5, 14)])
    assert facts == {
        "left_addresses": 10,
        "right_addresses": 10,
        "overlap_addresses": 5,
        "left_only_addresses": 5,
        "right_only_addresses": 5,
        "union_addresses": 15,
        "equal": False,
    }
    duplicate_side = compare([(0, 9), (2, 12)], [(5, 14)])
    assert duplicate_side["left_addresses"] == 13
    assert duplicate_side["right_addresses"] == 10


if __name__ == "__main__":
    _self_test()
