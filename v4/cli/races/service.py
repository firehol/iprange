#!/usr/bin/env python3
"""Bounded JSON-RPC session and outcome classification for the race battery.

One session is one product process: the raced ``stat``-then-``open`` sequence
happens once per request, so an attempt always starts a fresh service. The
session never blocks unboundedly: every read carries a deadline, and a
deadline trip is reported as an observation (``timeout``) rather than an
exception, because the whole point of the battery is to distinguish "answered
with a refusal" from "never answered".

Classification is shared by the product arms and the sentinel control, so a
weakened classifier weakens both and is caught by the control.
"""

import errno
import json
import os
import selectors
import stat as stat_module
import subprocess
import sys
import time

_HERE = os.path.dirname(os.path.abspath(__file__))
_CLI = os.path.dirname(_HERE)
if _CLI not in sys.path:
    sys.path.insert(0, _CLI)

import run as cli_run  # noqa: E402  (import is side-effect free)

TIMEOUT = "timeout"
EOF = "eof"
BROKEN_PIPE = "broken-pipe"


def shape(response):
    """Answer class of one decoded response: ``RESULT`` or ``code/data.code``."""

    if not isinstance(response, dict):
        return "malformed"
    error = response.get("error")
    if error is None:
        return "RESULT" if "result" in response else "malformed"
    return "{}/{}".format(error.get("code"),
                          ((error.get("data") or {}).get("code")))


class Observation:
    """What one attempt produced."""

    def __init__(self, kind, response=None, elapsed=0.0, blocked=None,
                 wedge=None):
        self.kind = kind
        self.response = response
        self.elapsed = elapsed
        self.blocked = blocked or []
        self.wedge = wedge

    @property
    def shape(self):
        return shape(self.response) if self.kind == "answered" else self.kind

    def record(self):
        return {"kind": self.kind, "shape": self.shape,
                "elapsed": round(self.elapsed, 3),
                "blocked": list(self.blocked), "wedge": self.wedge}


class EngineSession:
    """A product ``--jsonrpc`` service in its own process group."""

    def __init__(self, binary, work):
        self.binary = binary
        self.work = work
        self.proc = subprocess.Popen(
            [binary, "--jsonrpc"], stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, cwd=work,
            env=cli_run.child_environment(), start_new_session=True)
        self._buffer = b""
        self._dead_reason = None

    def call(self, request, deadline):
        """Write one frame, read one framed answer, never longer than ``deadline``."""

        start = time.monotonic()
        try:
            self.proc.stdin.write(
                (json.dumps(request, separators=(",", ":")) + "\n").encode())
            self.proc.stdin.flush()
        except (BrokenPipeError, OSError):
            self._dead_reason = "stdin"
            return Observation(BROKEN_PIPE, elapsed=time.monotonic() - start)
        selector = selectors.DefaultSelector()
        fd = self.proc.stdout.fileno()
        selector.register(fd, selectors.EVENT_READ)
        try:
            while True:
                newline = self._buffer.find(b"\n")
                if newline >= 0:
                    raw = self._buffer[:newline + 1]
                    self._buffer = self._buffer[newline + 1:]
                    try:
                        decoded = json.loads(raw)
                    except ValueError:
                        return Observation("malformed",
                                           elapsed=time.monotonic() - start)
                    return Observation("answered", decoded,
                                       time.monotonic() - start)
                remaining = deadline - (time.monotonic() - start)
                if remaining <= 0:
                    break
                if not selector.select(min(remaining, 0.05)):
                    continue
                try:
                    chunk = os.read(fd, 65536)
                except (BlockingIOError, InterruptedError):
                    continue
                if not chunk:
                    self._dead_reason = "stdout"
                    return Observation(EOF, elapsed=time.monotonic() - start)
                self._buffer += chunk
        finally:
            selector.close()
        return Observation(TIMEOUT, elapsed=time.monotonic() - start)

    def read_more(self, seconds):
        """Read for up to ``seconds`` after an external event (wedge probe)."""

        start = time.monotonic()
        selector = selectors.DefaultSelector()
        fd = self.proc.stdout.fileno()
        selector.register(fd, selectors.EVENT_READ)
        try:
            while time.monotonic() - start < seconds:
                newline = self._buffer.find(b"\n")
                if newline >= 0:
                    raw = self._buffer[:newline + 1]
                    self._buffer = self._buffer[newline + 1:]
                    try:
                        return Observation("answered", json.loads(raw),
                                           time.monotonic() - start)
                    except ValueError:
                        return Observation("malformed",
                                           elapsed=time.monotonic() - start)
                if not selector.select(0.05):
                    continue
                try:
                    chunk = os.read(fd, 65536)
                except OSError:
                    return Observation(EOF, elapsed=time.monotonic() - start)
                if not chunk:
                    return Observation(EOF, elapsed=time.monotonic() - start)
                self._buffer += chunk
        finally:
            selector.close()
        return Observation(TIMEOUT, elapsed=time.monotonic() - start)

    def blocked_threads(self):
        """Kernel wait channel of each thread, for the hang evidence trail."""

        found = []
        base = f"/proc/{self.proc.pid}/task"
        try:
            threads = os.listdir(base)
        except OSError:
            return found
        for thread in threads:
            try:
                with open(os.path.join(base, thread, "wchan"),
                          encoding="utf-8") as stream:
                    wchan = stream.read().strip()
                with open(os.path.join(base, thread, "syscall"),
                          encoding="utf-8") as stream:
                    syscall = stream.read().split()[0]
            except (OSError, IndexError):
                continue
            if wchan not in ("", "0"):
                found.append(f"tid{thread}:wchan={wchan}:syscall={syscall}")
        return found

    def alive(self):
        return self.proc.poll() is None

    def kill(self):
        """Terminate exactly this session (the battery owns no other group)."""

        if self.proc.poll() is None:
            try:
                os.killpg(self.proc.pid, 9)
            except ProcessLookupError:
                pass
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass


def _candidate_fifos(target, stage_dir, limit):
    """Names whose FIFO inode might hold a parked reader, newest first.

    The raced name is only useful while it still *is* the FIFO; a reader that
    lost the race stays parked on the old inode, which remains reachable
    through the hard link the swapper retained. Sequence-ordered names let the
    scan start where the current attempt can only be.
    """

    candidates = []
    if target and os.path.lexists(target):
        try:
            if stat_module.S_ISFIFO(os.stat(target).st_mode):
                candidates.append(target)
        except OSError:
            pass
    if stage_dir and os.path.isdir(stage_dir):
        try:
            names = sorted(os.listdir(stage_dir), reverse=True)[:limit]
        except OSError:
            names = []
        for name in names:
            path = os.path.join(stage_dir, name)
            if path not in candidates:
                candidates.append(path)
    return candidates


def confirm_wedge(target, payload, read_again, stage_dir=None,
                  settle=2.0, scan_limit=200000):
    """Decide whether a silent attempt was parked in ``open(2)`` on a FIFO.

    Opening a FIFO for writing with ``O_NONBLOCK`` fails with ``ENXIO`` unless
    a reader is already blocked on that inode, so a successful attach is the
    discriminator: it names the exact FIFO that had a waiter, and the payload
    that follows releases the waiter. ``read_again(seconds)`` returns true when
    the silent process then answers, which is the confirmed wedge.

    Returns a classification string; every branch is an observation, never a
    pass, so an unexercised confirmation path is visible in the report.
    """

    attached = None
    errors = 0
    for path in _candidate_fifos(target, stage_dir, scan_limit):
        try:
            fd = os.open(path, os.O_WRONLY | os.O_NONBLOCK, 0o600)
        except FileNotFoundError:
            continue
        except PermissionError:
            return "writer-attach-denied"
        except OSError as exc:
            if exc.errno == errno.ENXIO:
                continue          # nobody parked on this FIFO
            errors += 1
            continue
        try:
            try:
                os.write(fd, payload)
            except OSError:
                pass
            attached = path
        finally:
            os.close(fd)
        break
    if attached is None:
        if errors:
            return f"writer-attach-errors={errors}"
        return "no-parked-reader-at-detection"
    if read_again(settle):
        return f"wedge-confirmed:answered-after-writer-attach:{os.path.basename(attached)}"
    return f"writer-attached-but-still-silent:{os.path.basename(attached)}"
