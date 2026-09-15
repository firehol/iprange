#!/usr/bin/env python3
"""Racing helper: toggle one name between a regular node and a FIFO.

The helper exists so the battery can put the target name into the other state
while a request is in flight, which is the window a pre-open ``stat`` cannot
close. It never touches anything but its own temporary names, and it reports
its own progress through an atomic state file so the runner can distinguish
"a helper that is racing" from "a helper that never started" -- the failure
mode that let the wave-19.22 battery report a clean race with no helper at
all.

Counters are split deliberately:

``to_fifo`` / ``to_regular``  -- replacement activity occurred at all.
``in_flight_to_fifo`` / ``in_flight_to_regular`` -- a replacement completed
  while the runner's in-flight marker existed, i.e. while a request was
  actually in the wire. Only the second one exercises the timing window.

Usage (the runner owns these arguments; ``--help`` is not a supported mode):

    python3 swapper.py --target PATH --regular-template PATH --state PATH
        [--inflight-marker PATH] [--interval SECONDS] [--stop-file PATH]
"""

import argparse
import errno
import json
import os
import sys
import time

# Tolerated races against the runner and the product process: the target may
# be open, renamed, or replaced by the other side at any moment.
RACE_ERRORS = (errno.ENOENT, errno.EEXIST, errno.EACCES, errno.EBUSY,
               errno.ENOTEMPTY, errno.EISDIR)


class State:
    """The helper's self-report, published by atomic replace."""

    def __init__(self, path):
        self.path = path
        self.counts = {"pid": os.getpid(), "phase": "starting",
                       "toggles": 0, "to_fifo": 0, "to_regular": 0,
                       "in_flight_to_fifo": 0, "in_flight_to_regular": 0,
                       "errors": 0, "started": time.time(),
                       "updated": time.time()}

    def publish(self, phase=None, **updates):
        self.counts.update(updates)
        self.counts["updated"] = time.time()
        if phase is not None:
            self.counts["phase"] = phase
        tmp = f"{self.path}.tmp.{os.getpid()}"
        with open(tmp, "w", encoding="utf-8") as stream:
            json.dump(self.counts, stream, sort_keys=True)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(tmp, self.path)

    def bump(self, key, amount=1):
        self.counts[key] = self.counts.get(key, 0) + amount


def build_regular(target, template, sequence):
    """Rename a hard link of the template onto the raced name."""

    temporary = f"{target}.sw-{os.getpid()}-{sequence}-r"
    try:
        if os.path.lexists(temporary):
            os.unlink(temporary)
        os.link(template, temporary)
        os.rename(temporary, target)
    except OSError as exc:
        if exc.errno in RACE_ERRORS:
            return False
        raise
    return True


def build_fifo(target, pool, sequence):
    """Rename a hard link of a pooled FIFO inode onto the raced name.

    The pool is what makes a parked reader findable. ``open(2)`` parks a
    blocked reader on the FIFO's *inode*, not on the name, so once the swapper
    renames a regular file over the target the only way to prove the park is
    to open that very inode for writing. Keeping a bounded set of permanent
    FIFO inodes and hard-linking them into the raced name gives that proof
    without letting the race allocate one inode per toggle.
    """

    resident = pool[sequence % len(pool)]
    if not os.path.lexists(resident):
        return False
    temporary = f"{target}.sw-{os.getpid()}-{sequence}-f"
    try:
        if os.path.lexists(temporary):
            os.unlink(temporary)
        os.link(resident, temporary)
        os.rename(temporary, target)
    except OSError as exc:
        if exc.errno in RACE_ERRORS:
            return False
        raise
    return True


def build_pool(stage_dir, size):
    """Create the permanent FIFO inodes the race hard-links from."""

    os.makedirs(stage_dir, exist_ok=True)
    pool = []
    for index in range(size):
        path = os.path.join(stage_dir, f"fifo-{index:04d}")
        if not os.path.lexists(path):
            os.mkfifo(path, 0o600)
        pool.append(path)
    return pool


def in_flight(marker):
    if not marker:
        return False
    try:
        return os.path.exists(marker)
    except OSError:
        return False


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--target", required=True)
    parser.add_argument("--regular-template", required=True)
    parser.add_argument("--state", required=True)
    parser.add_argument("--inflight-marker", default=None)
    parser.add_argument("--interval", type=float, default=0.001)
    parser.add_argument("--stop-file", default=None)
    parser.add_argument("--max-seconds", type=float, default=120.0)
    parser.add_argument("--stage-dir", default=None,
                        help="directory holding the permanent FIFO inodes the "
                             "race hard-links from; without it a reader parked "
                             "on a replaced FIFO would be unprovable")
    parser.add_argument("--pool-size", type=int, default=8,
                        help="permanent FIFO inodes to rotate between")
    arguments = parser.parse_args()

    state = State(arguments.state)
    # Startup validation happens before the ready marker: an unusable
    # fixture must never look like a running racer.
    if not os.path.isfile(arguments.regular_template):
        state.publish("failed", failure="regular-template-missing")
        return 2
    if not os.path.lexists(arguments.target):
        state.publish("failed", failure="target-missing")
        return 2
    if not arguments.stage_dir:
        state.publish("failed", failure="stage-dir-missing")
        return 2
    try:
        pool = build_pool(arguments.stage_dir, arguments.pool_size)
    except OSError as exc:
        state.publish("failed", failure=f"stage-dir:{exc.strerror}")
        return 2
    state.publish("ready")

    sequence = 0
    last_publish = time.monotonic()
    started = time.monotonic()
    try:
        while True:
            if arguments.stop_file and os.path.exists(arguments.stop_file):
                break
            if time.monotonic() - started > arguments.max_seconds:
                state.publish("expired")
                return 0
            for kind in ("fifo", "regular"):
                sequence += 1
                # The marker is sampled on both sides of the rename and both
                # samples must see it: an attempt only counts as raced when
                # the whole replacement completed inside its window, so the
                # runner can never be credited with a window it did not open.
                open_before = in_flight(arguments.inflight_marker)
                if kind == "fifo":
                    replaced = build_fifo(arguments.target, pool, sequence)
                else:
                    replaced = build_regular(arguments.target,
                                             arguments.regular_template,
                                             sequence)
                open_after = in_flight(arguments.inflight_marker)
                if replaced:
                    state.bump("toggles")
                    state.bump("to_fifo" if kind == "fifo" else "to_regular")
                    if open_before and open_after:
                        state.bump("in_flight_to_fifo" if kind == "fifo"
                                   else "in_flight_to_regular")
                else:
                    state.bump("errors")
                if arguments.interval:
                    time.sleep(arguments.interval)
            if time.monotonic() - last_publish >= 0.02:
                state.publish("racing")
                last_publish = time.monotonic()
    except Exception as exc:  # pragma: no cover - fail visible, never silent
        state.publish("crashed", failure=f"{type(exc).__name__}: {exc}")
        return 1
    state.publish("stopped")
    return 0


if __name__ == "__main__":
    sys.exit(main())
