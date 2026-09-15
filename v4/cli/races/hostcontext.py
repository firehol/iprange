#!/usr/bin/env python3
"""Host conditions recorded alongside every race attempt.

The battery measures a per-attempt answer deadline (``--deadline``, default
1.5 s). A fresh product process has to be *scheduled* before it can answer, so
on a host whose run queue is longer than it has CPUs the deadline can expire
while the engine is still in ``execve``. The sample recorded with each attempt
is what lets a reader tell that apart from a genuine blocked ``open(2)``
without re-running the battery.

These are recorded facts, never an escape hatch. No judgment in this battery
reads a load number: an ``engine-hang`` is a failure whether the run queue was
1 or 10,000. The sample's only power is to make the failure legible -- and a
record whose sample is missing, incomplete, or self-contradictory is itself a
battery failure (``aggregate.require_host_context``).
"""

import os

_PROC_LOADAVG = "/proc/loadavg"

# The sample is one small procfs read taken per attempt; that is the whole
# cost, and it is taken at attempt start (and again at hang-detection time),
# never in a loop.
SAMPLE_KEYS = ("source", "nproc", "loadavg_1m", "loadavg_5m", "loadavg_15m",
               "run_queue", "over_cpus")


def nproc():
    """Online CPU count, or 0 when the platform cannot report one.

    Zero is recorded rather than guessed: a missing count makes
    ``over_cpus`` false-certifiable, and ``require_host_context`` refuses the
    record instead of letting a load claim rest on an invented denominator.
    """

    for getter in (lambda: os.sysconf("SC_NPROCESSORS_ONLN"), os.cpu_count):
        try:
            value = getter()
        except (OSError, ValueError, TypeError, AttributeError):
            value = None
        if isinstance(value, int) and value > 0:
            return value
    return 0


def _from_proc():
    """``(loadavg triple, run_queue)`` from procfs, or None."""

    try:
        with open(_PROC_LOADAVG, encoding="utf-8") as stream:
            fields = stream.read().split()
        runnable, _sep, _total = fields[3].partition("/")
        return ([float(fields[0]), float(fields[1]), float(fields[2])],
                int(runnable))
    except (OSError, ValueError, IndexError):
        return None


def _from_libc():
    """``(loadavg triple, run_queue)`` from ``os.getloadavg()``, or None.

    The portable interface carries no run queue, so ``run_queue`` stays None
    and ``over_cpus`` is refused rather than defaulted to a comforting false.
    """

    try:
        triple = os.getloadavg()
    except (AttributeError, OSError):
        return None
    return [float(triple[0]), float(triple[1]), float(triple[2])], None


def sample(deadline):
    """One host snapshot, shaped for the report.

    ``deadline`` is the effective per-attempt answer deadline the sample
    belongs to: a hang is only interpretable against the budget it failed to
    meet, so the number travels inside the sample instead of beside it.
    """

    cpus = nproc()
    read = _from_proc() or _from_libc()
    if read is None:
        triple, run_queue, source = [None, None, None], None, "unavailable"
    else:
        triple, run_queue = read
        source = "proc/loadavg" if run_queue is not None else "getloadavg"
    return {
        "source": source,
        "nproc": cpus,
        "deadline": float(deadline),
        "loadavg_1m": triple[0],
        "loadavg_5m": triple[1],
        "loadavg_15m": triple[2],
        "run_queue": run_queue,
        "over_cpus": (run_queue > cpus) if (run_queue is not None and cpus)
                     else None,
    }


def _spread(values):
    """min/median/max of a sample column, or None when nothing was sampled."""

    values = [value for value in values if value is not None]
    if not values:
        return None
    values.sort()
    middle = values[len(values) // 2]
    return {"min": values[0], "median": middle, "max": values[-1]}


def summarize(deadline, start_samples):
    """The host context one record carries for the attempts it just ran.

    The per-attempt samples are reduced, not dropped: an attempt-by-attempt
    list of a hundred identical numbers is noise, while min/median/max and the
    count of attempts that started on an oversubscribed host are what a reader
    asks about. The exact sample of any attempt that hung is retained in full
    beside that hang, and that is the pair that decides the case.
    """

    start_samples = list(start_samples)
    queues = [entry.get("run_queue") for entry in start_samples]
    loads = [entry.get("loadavg_1m") for entry in start_samples]
    cpus = nproc()
    over = sum(1 for queue in queues
               if queue is not None and cpus and queue > cpus)
    median_queue = _spread(queues) or {}
    return {
        "source": start_samples[0]["source"] if start_samples else "none",
        "nproc": cpus,
        "deadline": float(deadline),
        "samples": len(start_samples),
        "run_queue": _spread(queues),
        "loadavg_1m": _spread(loads),
        "over_cpus_samples": over,
        "oversubscribed": (median_queue.get("median") > cpus)
                          if (median_queue.get("median") is not None and cpus)
                          else None,
    }
