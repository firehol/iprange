"""Child-only timing and peak RSS for one release binary.

Peak RSS is `VmHWM` from `/proc/PID/status`, sampled while the child
is alive. `getrusage(RUSAGE_CHILDREN)` is the largest waited-for child
so far, so it cannot compare a later smaller child with an earlier
larger one. The runner's own memory is excluded.
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


def parse_stat_cpu(stat_text):
    """CPU seconds from one `/proc/<pid>/stat` record.

    `getrusage(RUSAGE_CHILDREN)` accumulates across every waited child,
    so a delta is this child's CPU only when nothing else was waited
    for in between. `/proc/<pid>/stat` names one process, so it is the
    child under measurement and not an inherited total. Returns None
    when the record cannot be parsed; the caller decides what that
    means instead of silently reporting zero.
    """
    try:
        fields = stat_text.rsplit(")", 1)[1].split()
        # utime and stime are fields 14 and 15 of the full record,
        # which are indices 11 and 12 after the comm-field split.
        ticks = int(fields[11]) + int(fields[12])
        return ticks / os.sysconf("SC_CLK_TCK")
    except (ValueError, IndexError):
        return None


def child_cpu_seconds(pid):
    try:
        with open(f"/proc/{pid}/stat", "r", encoding="utf-8") as stream:
            return parse_stat_cpu(stream.read())
    except OSError:
        return None


def run_once(argv, stdin_bytes=None):
    started = time.perf_counter()
    proc = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    peak = 0
    cpu = 0.0
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
        current = child_cpu_seconds(proc.pid)
        if current is not None:
            cpu = max(cpu, current)
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
    if cpu <= 0:
        raise AssertionError("child cpu was not sampled")
    return {
        "elapsed_seconds": elapsed,
        "child_cpu_seconds": cpu,
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
    cpu = [sample["child_cpu_seconds"] for sample in samples]
    rss = [sample["child_max_rss_kib"] for sample in samples]
    return {
        "rounds": rounds,
        "elapsed_seconds": {"median": median(elapsed), "min": min(elapsed), "max": max(elapsed)},
        "child_cpu_seconds": {"median": median(cpu), "min": min(cpu), "max": max(cpu)},
        "child_max_rss_kib": {"median": median(rss), "min": min(rss), "max": max(rss)},
        "child_raised_peak": all(sample["child_raised_peak"] for sample in samples),
    }


def ratio(rust, go):
    """Go/Rust median ratio. Above 1 means Go used more time or RSS.

    A missing CPU sample is never turned into a zero: a fabricated
    `cpu: 0.0` would read as a pass of the 1.3x CPU ceiling. If either
    engine carries CPU data, both must.
    """
    elapsed = rust["elapsed_seconds"]["median"]
    rss = rust["child_max_rss_kib"]["median"]
    if elapsed <= 0 or rss <= 0:
        raise ValueError("rust median must be positive")
    result = {
        "elapsed": go["elapsed_seconds"]["median"] / elapsed,
        "rss": go["child_max_rss_kib"]["median"] / rss,
    }
    if go["elapsed_seconds"]["median"] <= 0 or go["child_max_rss_kib"]["median"] <= 0:
        raise ValueError("go median must be positive")
    rust_cpu = rust.get("child_cpu_seconds", {}).get("median")
    go_cpu = go.get("child_cpu_seconds", {}).get("median")
    if (rust_cpu is None) != (go_cpu is None):
        raise ValueError(
            "cpu was sampled for one engine only; refusing to fabricate "
            "a cpu ratio")
    if rust_cpu is not None:
        if rust_cpu <= 0 or go_cpu <= 0:
            raise ValueError("cpu medians must be positive when present")
        result["cpu"] = go_cpu / rust_cpu
    return result
