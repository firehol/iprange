"""Child-only timing and peak RSS for one release binary.

getrusage(RUSAGE_CHILDREN) is read after the child exits. On Linux that
peak is the largest waited-for child, in KiB. It is not the runner.
"""

import os
import subprocess
import time


def child_hwm_kib(pid):
    try:
        with open(f"/proc/{pid}/status", "r", encoding="utf-8") as stream:
            for line in stream:
                if line.startswith("VmHWM:"):
                    return int(line.split()[1])
    except FileNotFoundError:
        return None
    return None


def run_once(argv, stdin_bytes=None):
    started = time.perf_counter()
    proc = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    peak = 0
    deadline = time.perf_counter() + 120
    if stdin_bytes is not None:
        proc.stdin.write(stdin_bytes)
        proc.stdin.flush()
        os.set_blocking(proc.stdout.fileno(), False)
    while proc.poll() is None:
        if time.perf_counter() > deadline:
            proc.kill()
            proc.wait(timeout=5)
            raise AssertionError("child did not finish within 120s")
        current = child_hwm_kib(proc.pid)
        if current is not None:
            peak = max(peak, current)
        if stdin_bytes is not None:
            try:
                if proc.stdout.readline():
                    proc.stdin.close()
                    stdin_bytes = None
            except BlockingIOError:
                pass
        time.sleep(0.001)
    try:
        proc.stdin.close()
    except BrokenPipeError:
        pass
    proc.stdout.read()
    proc.wait()
    proc.stdout.close()
    elapsed = time.perf_counter() - started
    if proc.returncode != 0:
        raise AssertionError(f"child exited {proc.returncode}: {argv[0]}")
    if peak == 0:
        raise AssertionError("child peak was not observed")
    return {
        "elapsed_seconds": elapsed,
        "child_max_rss_kib": peak,
        "child_raised_peak": True,
    }


def median(values):
    ordered = sorted(values)
    middle = len(ordered) // 2
    if len(ordered) % 2:
        return ordered[middle]
    return (ordered[middle - 1] + ordered[middle]) / 2


def measure(argv, rounds, stdin_bytes=None):
    if rounds < 1:
        raise ValueError("rounds must be positive")
    samples = [run_once(argv, stdin_bytes) for _ in range(rounds)]
    elapsed = [sample["elapsed_seconds"] for sample in samples]
    rss = [sample["child_max_rss_kib"] for sample in samples]
    return {
        "rounds": rounds,
        "elapsed_seconds": {"median": median(elapsed), "min": min(elapsed), "max": max(elapsed)},
        "child_max_rss_kib": {"median": median(rss), "min": min(rss), "max": max(rss)},
        "child_raised_peak": all(sample["child_raised_peak"] for sample in samples),
    }


def ratio(rust, go):
    """Go/Rust median ratio. Above 1 means Go used more time or RSS."""
    elapsed = rust["elapsed_seconds"]["median"]
    rss = rust["child_max_rss_kib"]["median"]
    if elapsed <= 0 or rss <= 0:
        raise ValueError("rust median must be positive")
    return {
        "elapsed": go["elapsed_seconds"]["median"] / elapsed,
        "rss": go["child_max_rss_kib"]["median"] / rss,
    }
