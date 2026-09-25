"""Child-only timing and peak RSS for one release binary.

getrusage(RUSAGE_CHILDREN) is read after the child exits. On Linux that
peak is the largest waited-for child, in KiB. It is not the runner.
"""

import resource
import subprocess
import time


def run_once(argv):
    before = resource.getrusage(resource.RUSAGE_CHILDREN)
    started = time.perf_counter()
    proc = subprocess.run(argv, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    elapsed = time.perf_counter() - started
    after = resource.getrusage(resource.RUSAGE_CHILDREN)
    if proc.returncode != 0:
        raise AssertionError(f"child exited {proc.returncode}: {argv[0]}")
    return {
        "elapsed_seconds": elapsed,
        "child_max_rss_kib": after.ru_maxrss - before.ru_maxrss,
    }


def median(values):
    ordered = sorted(values)
    middle = len(ordered) // 2
    if len(ordered) % 2:
        return ordered[middle]
    return (ordered[middle - 1] + ordered[middle]) / 2


def measure(argv, rounds):
    if rounds < 1:
        raise ValueError("rounds must be positive")
    samples = [run_once(argv) for _ in range(rounds)]
    elapsed = [sample["elapsed_seconds"] for sample in samples]
    rss = [sample["child_max_rss_kib"] for sample in samples]
    return {
        "rounds": rounds,
        "elapsed_seconds": {"median": median(elapsed), "min": min(elapsed), "max": max(elapsed)},
        "child_max_rss_kib": {"median": median(rss), "min": min(rss), "max": max(rss)},
    }
