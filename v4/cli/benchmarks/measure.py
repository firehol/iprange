"""Child-only timing and peak RSS for one release binary.

Peak RSS is `VmHWM` from `/proc/PID/status`, sampled while the child
is alive. `getrusage(RUSAGE_CHILDREN)` is the largest waited-for child
so far, so it cannot compare a later smaller child with an earlier
larger one. The runner's own memory is excluded.

Process creation, framing, and response parsing are JsonRpcService's
(v4/cli/run.py) — the resolved harness decision forbids a second
protocol client, so `run_once` composes the shared service plus the
samplers instead of owning pipes itself.
"""

import json
import os
import subprocess
import sys
import threading
import time

# Foreign oracle for the join postcondition (r163 panel): the real
# Thread.is_alive captured at import — a forged sampler cannot lie
# to it.
_REAL_THREAD_IS_ALIVE = threading.Thread.is_alive
_REAL_THREAD_TYPE = threading.Thread
_REAL_LISTDIR = __import__("os").listdir

def assert_sampler_dead(sampler, _alive=_REAL_THREAD_IS_ALIVE,
                      _listdir=_REAL_LISTDIR,
                      _type=_REAL_THREAD_TYPE):
    """Runtime postcondition for the join contract (r161-r173
    panels): the sampler thread must be dead when the join returns.
    The oracles and the thread type are def-bound at import
    (rebinding-immune); an exact-class gate refuses every subclass;
    a forged or expiring timed join fails loudly. Named residual
    floors (status.md entry 27): the fork-shaped channel; harness-
    level tampering by trusted code; the TID-reuse amnesty assumes
    the def-bound oracle is honest on an exact-class thread. The
    source pin is a regression detector, not the boundary."""
    # Exact-class + two-sided disagreement rule (r169/r171 panels):
    # only a plain, unmodified threading.Thread is trusted at all
    # (every subclass can forge its own state); then the task list
    # (kernel truth) and the oracle must agree, with a bounded grace
    # for the two honest races: the task-exit window after join, and
    # TID reuse (a dead thread's TID recycled by a later thread —
    # observed as a false positive on s5-exclude).
    if type(sampler) is not _type:
        raise AssertionError(
            "sampler is not an exact threading.Thread: subclass "
            "state cannot be verified")
    native_id = getattr(sampler, "_native_id", None)
    if native_id is not None:
        try:
            key = str(native_id)
            for _ in range(3):
                in_tasks = key in _listdir("/proc/self/task")
                alive = _alive(sampler)
                if not in_tasks and not alive:
                    return  # both agree: dead
                if in_tasks and alive:
                    raise AssertionError(
                        "sampler thread still alive after join: the "
                        "join must outlive the sampler")
                time.sleep(0.01)
            # Persisting single-sided disagreement:
            in_tasks = key in _listdir("/proc/self/task")
            alive = _alive(sampler)
            if in_tasks and not alive:
                return  # TID reuse: the thread object says dead
            raise AssertionError(
                "sampler liveness disagrees with the task list "
                "(forged identity or racing exit): refusing to "
                "trust the sampler's own bookkeeping")
        except OSError:
            pass  # no /proc: the disagreement rule cannot run
    if _alive(sampler):
        raise AssertionError(
            "sampler thread still alive after join: the join must "
            "outlive the sampler (a timed or forged join races the "
            "sample reads)")



sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from run import JsonRpcService  # noqa: E402


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


def child_tree_cpu_seconds(pid):
    """CPU seconds of the process and its waited-for children.

    The engine delegates heavy work to a worker subprocess: its own
    utime+stime stays near zero while cutime/cstime (fields 13-14) add
    the children it has reaped. For a resident worker (never reaped
    during the exchange) this still reads what the engine itself
    accumulated — a measured 0.0, not a missing sample; the caller's
    guard distinguishes the two.
    """
    try:
        with open(f"/proc/{pid}/stat", "r", encoding="utf-8") as stream:
            fields = stream.read().rsplit(")", 1)[1].split()
            ticks = int(fields[11]) + int(fields[12]) + int(fields[13]) + int(fields[14])
            return ticks / os.sysconf("SC_CLK_TCK")
    except (OSError, ValueError, IndexError):
        return None


def validate_response(request, response):
    """A timed operation must succeed: one correlated, non-empty result.

    The measurement contract times real work: an operation that
    answers an error (I/O failure, budget rejection) is not a valid
    sample, a partial or uncorrelated frame is a harness defect, and
    a null or empty result is not real work either — the sample must
    be an operation that did something. The spec answers every
    request exactly once.
    """
    if not isinstance(response, dict):
        raise AssertionError(f"response is not an object: {response!r}")
    if response.get("id") != request.get("id"):
        raise AssertionError(
            f"response id {response.get('id')!r} does not correlate "
            f"with request {request.get('id')!r}")
    if "error" in response:
        raise AssertionError(f"timed operation failed: {response['error']}")
    if "result" not in response:
        raise AssertionError("response carries neither result nor error")
    result = response["result"]
    if result is None:
        raise AssertionError("timed operation returned a null result")
    if isinstance(result, dict) and not result:
        raise AssertionError("timed operation returned an empty result")
    if isinstance(result, dict):
        report = result.get("report")
        if report is not None and isinstance(report, dict) and not report:
            raise AssertionError("timed operation returned an empty report")


def run_once(argv, stdin_bytes=None, cwd=None):
    """One timed round: spawn, one frame exchange, strict close.

    The timed window spans the spawn, the exchange (when a request is
    given), and the strict close — the same end-to-end scope the raw
    implementation measured. The exchange itself is JsonRpcService's;
    this function adds the VmHWM/CPU samplers and returns the timed
    response with the sample so callers can validate the semantic
    result (and the output it claims to have written), not just the
    frame shape.
    """
    request = None
    if stdin_bytes is not None:
        request = json.loads(stdin_bytes)
        if not isinstance(request, dict) or "method" not in request:
            raise AssertionError(
                "timed request must be exactly one JSON-RPC request frame")
    implementation = os.path.basename(argv[0])
    started = time.perf_counter()
    service = JsonRpcService(
        list(argv), implementation, cwd=cwd,
        read_deadline=120, write_deadline=30)
    peak = {"kib": 0, "observed": False}
    cpu = {"seconds": 0.0, "observed": False}

    def sample():
        while service.proc.poll() is None:
            current = child_hwm_kib(service.proc.pid)
            if current is not None:
                peak["observed"] = True
                if current > peak["kib"]:
                    peak["kib"] = current
            current = child_tree_cpu_seconds(service.proc.pid)
            if current is not None:
                # A measured 0.0 is a sample (an engine whose work runs
                # in a resident worker legitimately reads zero); only a
                # reader that observes nothing is a missing sample.
                cpu["observed"] = True
                if current > cpu["seconds"]:
                    cpu["seconds"] = current
            time.sleep(0.001)

    sampler = threading.Thread(target=sample, daemon=True)
    sampler.start()
    response = None
    try:
        if request is not None:
            response = service.call(
                request.get("id"), request["method"],
                request.get("params", {}))
            validate_response(request, response)
            service.close()
        else:
            # No frame: a bare point-in-time child. It owes the
            # protocol nothing, so the service's stalled-peer grace
            # (kill at 0.2 s past EOF) does not apply — wait for the
            # child's own natural end (the original patience, 120 s).
            raw = getattr(service, "_raw_stdin", None)
            if raw is not None:
                raw.close()
            elif service.proc.stdin and not service.proc.stdin.closed:
                service.proc.stdin.close()
            service.proc.wait(timeout=120)
    finally:
        service.close(allow_forced=True, broken_exchange=True)
        # Unconditional join (r143 panel): a timed join falls
        # through silently and reopens the sample-classification
        # race; this sampler loop provably exits at child reaping.
        # The runtime postcondition (r161 panel) closes the whole
        # indirection class: any join returning while the sampler
        # still runs fails loudly.
        sampler.join()
        assert_sampler_dead(sampler)
    elapsed = time.perf_counter() - started
    if service.proc.returncode != 0:
        raise AssertionError(f"child exited {service.proc.returncode}: {argv[0]}")
    if not peak["observed"]:
        raise AssertionError("child peak was not observed")
    if not cpu["observed"]:
        raise AssertionError("child cpu was not sampled")
    sample_result = {
        "elapsed_seconds": elapsed,
        "child_cpu_seconds": cpu["seconds"],
        "child_max_rss_kib": peak["kib"],
        "child_raised_peak": True,
    }
    if response is not None:
        sample_result["response"] = response
    return sample_result


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
    `cpu: 0.0` would read as a pass of a CPU-ratio acceptance check.
    The binding acceptance metric is elapsed time and peak RSS
    (SOW-0030, user decisions 1A/2A); CPU is additional data. If
    either engine carries CPU data, both must.
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
