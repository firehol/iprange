#!/usr/bin/env python3
"""Detection control: a deliberately naive opener that loses the race.

The sentinel reproduces the code shape the v4 engines used to have -- a
``stat`` that reports a regular file, then a bare blocking ``open(2)`` with
no ``O_NONBLOCK`` and no post-open identity check. When the swapper wins that
window, the sentinel blocks in ``open(2)`` until a FIFO writer appears.

It exists so the battery can prove its hang detector actually fires. An
engine arm that reports ``hangs=0`` only means something when the same
detector, on the same cadence, reports a confirmed wedge here.

Usage (runner-owned; ``--help`` is not a supported mode):

    python3 sentinel.py --target PATH [--startup-marker PATH]
"""

import argparse
import json
import os
import sys
import time

def publish(path, payload):
    if not path:
        return
    tmp = f"{path}.tmp.{os.getpid()}"
    with open(tmp, "w", encoding="utf-8") as stream:
        json.dump(payload, stream, sort_keys=True)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(tmp, path)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--target", required=True)
    parser.add_argument("--startup-marker", default=None)
    parser.add_argument("--read-bytes", type=int, default=4096)
    arguments = parser.parse_args()

    # Announce readiness *before* the blocking call: the runner must be able
    # to tell "the helper started and then blocked" from "the helper never
    # started at all".
    publish(arguments.startup_marker, {"event": "started", "pid": os.getpid(),
                                       "target": arguments.target,
                                       "at": time.time()})
    print("ARMED", flush=True)

    # The unfixed shape, on purpose: metadata first, then a bare open.
    try:
        os.stat(arguments.target)
    except OSError as exc:
        print(f"STAT-ERROR errno={exc.errno}", flush=True)
        return 2
    fd = os.open(arguments.target, os.O_RDONLY)          # blocks on a FIFO
    try:
        data = os.read(fd, arguments.read_bytes)           # blocks without a writer
    finally:
        os.close(fd)
    print(f"OPENED bytes={len(data)}", flush=True)
    return 0

if __name__ == "__main__":
    sys.exit(main())
