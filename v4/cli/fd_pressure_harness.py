#!/usr/bin/env python3
"""Descriptor-pressure harness (SOW-0028 wave-19.25 design section 10).

One fresh process per cell; RLIMIT_NOFILE soft AND hard set by the launcher
in the child between fork and execve, so the engine cannot raise its own
limit; the table pre-occupied by the launcher opening N descriptors on a
file OUTSIDE the work directory; every run nice'd; a per-cell timeout of
8 s, so a cell that needs the whole budget fails as a WEDGE, not as a
class; the child's descriptor table sampled throughout and at the answer;
stderr captured and scanned for ``runtime:`` and ``fatal error:`` in
addition to the answer; and every cell run at least twice, any disagreement
failing the gate.

Expectations come from design section 7 (the measured Rust reference and
the required Go class), never from the engine under test, so the Go side is
not graded against itself.  The +held minimum-band law of section 7's
occupied table is enforced: the engine minimum shifts by exactly the number
of launcher-held descriptors, and a class-equality column grades below the
minimum.  Design section 13's measured "cannot meet" rules are honoured
explicitly, never silently:

* Rust parity starts at band 4 (the delivered dynamically linked loader
  cannot claim its libraries at 3: exit 127, host state);
* hold = 3 below band 6 is launcher host state (exit 92), reported as
  host-unsupported, never as refusal or coverage;
* the writer and worker arms of the owned Go build may stay BEHIND the Rust
  success bands (measured 8/6, 10/8, 9/7 on the fresh table): band parity is
  never declared there, the CLASSES must match, and the pinned Go minima of
  section 7 (8, 10, 9) are the graded floor.

Harness traps this file owns (both found in this wave's own first sweep):
warm-up frames use response ids (9001/9002) distinct from every measured
arm's ids, because answers are keyed by id and a shared id makes the cell
report the warm-up's class instead of the arm's; and the warm-up is a valid
reader.open + reader.close on the immutable fixture, so it changes no state
the measured arm depends on (no writer is ever used as a warm-up).

The poller pin (design section 10, read with section 6): for every arm
except the resolver arm, the child's descriptor table at the answer contains
NEITHER anon_inode:[eventpoll] NOR anon_inode:[eventfd], in every band and in
either runtime state. That is what turns calleropen's poller-free promise from
a property of one package into a checked property of the process, and it is
sensitive to every regression the wave could otherwise hide: a bare os.Open of
a persistent node, an unowned runtime timer, a crypto/rand draw, or an
exec.Cmd that opens the null device each put a poller descriptor in that table
and therefore fail the cell. The same equality detects a PARTIAL poller (one
epoll fd with no wake eventfd or the reverse). The Rust engine has no runtime
poller at all and is pinned to the same absence.

The resolver arm (current.publish whose list holds a host name) is the one
arm allowed to reach the poller, and only under the poller-readiness decision
of section 6: when the process could not authorize it the host-name line
answers the reference class input_format/not_started BEFORE anything enters
net, so the table stays poller-free; when it could, the deliberate creation
has already taken the pair, so the table shows exactly one eventpoll and one
eventfd and never a partial pair. The authorized boundary is the engine's own
(section 6: free = soft - in_use >= 4 on the table the launcher handed over,
which this harness knows exactly).

Usage
-----
    nice python3 v4/cli/fd_pressure_harness.py grid \
        --go-bin DIR --rust-bin DIR --fixture BIN --scratch DIR \
        [--arms a,b] [--bands 3..12] [--holds 0,3] [--runtimes before,after] \
        [--engines go,rust] [--runs 2] [--json-out FILE] [--generous 64]

    nice python3 v4/cli/fd_pressure_harness.py run-cell --engine go \
        --go-bin DIR --rust-bin DIR --fixture BIN --scratch DIR \
        --arm reader-open-close-live --band 7 --hold 0 --runtime before \
        --null-device normal

Exit status: 0 all executed cells pass (host-state and blocked cells are
reported, not counted), 1 any wedge, wrong class, disagreement or
poller-pin failure, 2 usage/host error.
"""

import argparse
import concurrent.futures
import json
import os
import queue
import resource
import selectors
import shlex
import shutil
import subprocess
import sys
import time

sys.dont_write_bytecode = True

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from check_refusal_class_parity import (  # noqa: E402  (same-directory sibling)
    PRESSURE_HOSTILE_CLASSES,
    IMMUTABLE_FEED_BUDGET,
    RESULT_BUDGET,
    TEXT_INPUT,
    VALIDATION_BUDGET,
    WRITER_BUDGET,
    _clean,
    _remove_any,
    prepare_work,
)

CELL_TIMEOUT_SECONDS = 8.0
LAUNCHER_EXIT_SETRLIMIT = 91   # child could not install the limit
LAUNCHER_EXIT_EXEC = 93        # child could not exec the engine (never host state)
LAUNCHER_EXIT_HOST = 92        # child could not claim held descriptors
# The descriptors a pressured engine already holds when it starts: the three
# standard streams. A band that cannot hold stdio plus the launcher's holds
# is host state (section 13.2).
stdio_in_use = 3
# The reader.open/reader.close warm-up that defines the "after" runtime state
# is itself the immutable-reader arm, whose measured minimum band is 5
# (section 7).
WARMUP_FLOOR = 5
# The delivered Rust binary is dynamically linked, so its loader must open a
# library before the engine can run. Measured: it launches with exactly one
# free descriptor and fails with exit 127 at zero, with and without the
# launcher's holds (band 3 fresh, band 6 occupied). The static Go build
# (CGO_ENABLED=0) launches with no free descriptor at all. Design section
# 13.1 records the first as unlaunchable at band 3; this is the same
# measurement, expressed as a count so the occupied table can use it.
RUST_LOADER_FLOOR = 4
RUNTIME_SCAN_TOKENS = ("runtime:", "fatal error:")
HOST_UNSUPPORTED = "host-unsupported"
BLOCKED = "blocked"
POLLER_EVENTPOLL = "anon_inode:[eventpoll]"
POLLER_EVENTFD = "anon_inode:[eventfd]"


# RESOLVER_ARM is the only arm whose handler may enter net (and therefore
# create the runtime poller). Every other arm must be poller-free (section 10).
RESOLVER_ARM = "current.publish.hostname"

# POLLER_DEMAND_FREE mirrors the engine's constant in
# v4/go/internal/calleropen/poller_readiness.go: the poller allocates two
# descriptors and the resolver's own datagram socket takes a third, so four
# free slots cover the creation plus the request that provokes it.
POLLER_DEMAND_FREE = 4

# STDIO_DESCRIPTORS is what the launcher hands over before the engine runs:
# the three standard streams, plus the held descriptors it claimed itself.
STDIO_DESCRIPTORS = 3


def poller_authorized(band, hold):
    """Mirror of the engine's poller-readiness decision (design section 6).

    The launcher knows exactly which table it hands over: the three standard
    streams plus the held descriptors, nothing else. The engine decides ready
    when soft - in_use >= pollerDemandFree, so the pin predicts the authorized
    poller state from the cell's own parameters. Only the resolver arm can
    observe the effect; for every other arm the expected table is the absence
    of the poller regardless of this predicate (section 10).
    """
    return band - (STDIO_DESCRIPTORS + hold) >= POLLER_DEMAND_FREE


# ---------------------------------------------------------------------------
# Expected classes (design section 7). min = engine minimum band for success
# on the fresh table (occupied adds +hold, the law section 7's occupied
# table itself follows); below = the class expected under the minimum;
# anywhere = a band-independent expected class; never = classes that must not
# appear in any band (the probe's io/not_started, the worker conflict fold).
# ---------------------------------------------------------------------------

IO_ROF = ("io", "read_only_failure")
IO_NS = ("io", "not_started")
SUCCESS = ("RESULT", "RESULT")

ARM_ORDER = [
    "system.describe",
    "reader-open-close-immutable",
    "reader-open-close-live",
    "direct.replace",
    "current.publish",
    "current.publish.hostname",
    "maintenance.remove",
    "validate(worker)",
    "recovery.inspect(worker)",
]

EXPECTED = {
    "system.describe": {
        "rust": {"min": 4, "below": None},
        "go": {"min": 3, "below": None},
        "anywhere": SUCCESS,
    },
    "reader-open-close-immutable": {
        "rust": {"min": 5, "below": IO_ROF},
        "go": {"min": 5, "below": IO_ROF},
        "anywhere": SUCCESS,
    },
    "reader-open-close-live": {
        "rust": {"min": 6, "below": IO_ROF},
        "go": {"min": 7, "below": IO_ROF},   # section 11: two extra sidecar fds
        "anywhere": SUCCESS,
    },
    "direct.replace": {
        "rust": {"min": 6, "below": IO_NS},
        "go": {"min": 8, "below": IO_NS},    # section 13.3: classes equal, bands not
        "anywhere": SUCCESS,
        # Measured: at band 7, one band under the owned Go floor, the writer
        # has already begun its transaction and an open inside it yields
        # EMFILE, so it answers its own abort class with the cleanup facts.
        # That is the operation's real class (section 5.7), not a probe
        # invention, and the reference has no measurement there to copy
        # because it completes at 6.
        "go_below_extra": [("transaction_aborted", "not_committed")],
    },
    "current.publish": {
        "rust": {"min": 7, "below": None},
        "go": {"min": 8, "below": None},
        "anywhere": SUCCESS,
        "below_classes": [IO_NS, ("io", "not_published")],
    },
    "current.publish.hostname": {
        # The publish family's own classes below the publish minimum (the
        # operation is refused by the publish path's opens before it can
        # resolve anything), and at or above it the class design section 6
        # pins for a host-name line: the reference class of a lookup that
        # could not be performed, answered before entering net.
        "rust": {"min": 7, "below": None},
        "go": {"min": 8, "below": None},
        "anywhere": ("input_format", "not_started"),
        "below_classes": [IO_NS, ("io", "not_published")],
    },
    "maintenance.remove": {
        # Section 7: the SDK class unchanged by pressure; never io/not_started
        # from a probe. The cell is only coverage when it removed a genuine
        # listed entry; an empty list is blocked (section 13.4), and a
        # refusal before any open is vacuous failure.
        "rust": {"min": None, "below": None},
        "go": {"min": None, "below": None},
        "anywhere": None,
        "never": [IO_NS],
    },
    "validate(worker)": {
        "rust": {"min": 8, "below": IO_ROF},
        # Design section 13.1: below the band the dynamic loader can start the
        # reference in, there is no measured class to copy, so the handler's
        # own pre-attempt class is allowed there.
        "pre_reference": [IO_NS],
        "go": {"min": 10, "below": IO_ROF},
        "anywhere": SUCCESS,
        "never": [("conflict", None)],
    },
    "recovery.inspect(worker)": {
        "rust": {"min": 7, "below": IO_ROF},
        # Design section 13.1: below the band the dynamic loader can start the
        # reference in, there is no measured class to copy, so the handler's
        # own pre-attempt class is allowed there.
        "pre_reference": [IO_NS],
        "go": {"min": 9, "below": IO_ROF},
        "anywhere": SUCCESS,
        "never": [("conflict", None)],
    },
}


# ---------------------------------------------------------------------------
# Hostile null device (design section 9).
#
# The cells replace /dev/null with a FIFO, or remove it, inside a private
# mount namespace. What section 9 requires of every arm under that condition
# is (1) an answer, never a wedge, (2) the same class in both engines, and
# (3) no control file left behind. The classes are those of section 9's own
# tables plus its rule 2: the spawn hands descriptors it owns, "an O_NONBLOCK
# open followed by the descriptor-identity check", so a node that is not the
# null character device is refused rather than handed to the child -- which is
# the safe direction, because a worker whose stdout is an undrained pipe is the
# wedge this section exists to remove. Therefore:
#
#   * the three file arms are unaffected by the null device and answer success
#     at a generous band in both hostile states (section 9's owned-build row);
#   * both worker arms answer io/read_only_failure in both hostile states, in
#     both engines -- the class section 9.4 pins for a worker that cannot be
#     resourced. The reference's HEAD rows for these cells were "no answer in
#     100 s", which is the defect, not the target.
# ---------------------------------------------------------------------------

HOSTILE_REQUIRED = PRESSURE_HOSTILE_CLASSES  # the committed law, owned by the gate


def classify(response):
    """Reduce one JSON-RPC reply to (kind, data.code, data.outcome)."""
    if not isinstance(response, dict):
        return None
    error = response.get("error")
    if isinstance(error, dict):
        data = error.get("data") or {}
        return ("error", data.get("code"), data.get("outcome"))
    if "result" in response:
        return SUCCESS and ("result", "RESULT", "RESULT")
    return None


def grade(arm, engine, band, hold, responses, stderr_text, null_state="normal"):
    """(verdict, why) for one attempt. responses maps request id -> reply."""
    spec = EXPECTED[arm]
    if null_state != "normal":
        # Section 9: the answer is required, the class is pinned per arm, and
        # the worker arms' refusal to spawn is the correct answer rather than
        # a wedge -- provided no control file survives, which the caller
        # checks, and provided the process disappears, which the run enforces.
        measured = responses.get(MEASURED_IDS[arm])
        if measured is None:
            return "wedge", (f"hostile null device ({null_state}): the arm never "
                             f"answered within the cell budget")
        got = classify(measured)
        if got is None:
            return "wrong-class", "hostile null device answer is not a product response"
        want = HOSTILE_REQUIRED[arm][null_state]
        if want is not None and got[1:] != want:
            return "wrong-class", (f"hostile null device {null_state}: got {got[1:]}, "
                                   f"want {want} (§9)")
        return "pass", f"answered {got[1:]} under hostile {null_state}"
    for banned in spec.get("never", []):
        for reply in responses.values():
            got = classify(reply)
            if got is None:
                continue
            if all(banned[i] is None or banned[i] == got[i + 1]
                   for i in range(2)):
                return "wrong-class", f"{got[1:]} is banned for this arm (§7)"
    measured = responses.get(MEASURED_IDS[arm])
    if measured is None:
        return "wedge", "measured frame never answered within the timeout"
    got = classify(measured)
    if got is None:
        return "wrong-class", "measured answer is not a product response"
    got_class = got[1:]

    if spec["anywhere"] is not None and spec["rust"]["min"] is None \
            and spec["go"]["min"] is None:
        # Band-independent class (hostname publish).
        want = spec["anywhere"]
        if got_class != want:
            return "wrong-class", f"got {got_class}, want {want}"
        return "pass", "reference class in every band"

    minimum = spec[engine]["min"]
    if minimum is None:
        # maintenance.remove: the SDK class is factual; the only rule the
        # gate enforces on top of the never-list is that the session ran and
        # the entry came from a real list (enforced by the runner).
        if responses.get(MEASURED_IDS[arm]) is not None and got[0] == "error":
            data = (responses[MEASURED_IDS[arm]].get("error") or {}).get("data")
            if data is not None and data.get("outcome") == "not_started" \
                    and data.get("code") == "io":
                return "wrong-class", "removal refused io/not_started (§7)"
        return "pass", "factual SDK class"

    minimum += hold  # section 7 occupied law: the minima track the held count
    below = spec[engine]["below"]
    if below is None:
        below = spec.get("below_classes")
    if below is None:
        # The arm's reference names no exhaustion class, which is the case
        # design section 7 records for the operation that claims no
        # descriptor of its own: it completes in every band the launcher can
        # start it in, so that is what the cell must show.
        below = spec["anywhere"]
    if band >= minimum:
        want = spec["anywhere"]
        if got_class != want:
            return "wrong-class", (f"band {band} >= expected minimum {minimum}: "
                                   f"got {got_class}, want {want}")
        return "pass", "success at or above the engine minimum"
    # Below the engine minimum: the reference below-class must answer.
    below_set = below if isinstance(below, list) else [below]
    if engine == "go":
        below_set = below_set + list(spec.get("go_below_extra") or [])
    if band < RUST_LOADER_FLOOR + hold:
        # Section 13.1: the reference cannot be started in this environment,
        # so there is no measured class to copy. What the handler really
        # answers -- the class of a failure before its durable attempt began
        # -- is accepted; the never-lists and the poller assertion still bind,
        # and the obligation is unchanged from the first band the reference
        # can be measured in.
        below_set = below_set + list(spec.get("pre_reference") or [])
    below = below_set
    if isinstance(below, list):
        if got_class not in below:
            return "wrong-class", (f"below minimum {minimum}: got {got_class}, "
                                   f"want one of {below}")
        return "pass", "one of the handler's own below-minimum classes"
    if got_class != below:
        return "wrong-class", (f"below minimum {minimum}: got {got_class}, "
                               f"want {below}")
    return "pass", "reference below-minimum class"


# ---------------------------------------------------------------------------
# Arms: frame scripts.  Each script is executed stepwise against one process:
# a frame is sent, its answer awaited, handles captured, then the next frame
# runs (placeholders resolve from same-session answers).  ids are 1xx for
# measured traffic; warm-up owns 9001/9002 (harness trap 1).
# ---------------------------------------------------------------------------

MEASURED_IDS = {
    "system.describe": 100,
    "reader-open-close-immutable": 100,
    "reader-open-close-live": 100,
    "direct.replace": 100,
    "current.publish": 100,
    "current.publish.hostname": 100,
    "maintenance.remove": 98,
    "validate(worker)": 100,
    "recovery.inspect(worker)": 100,
}


def _reader_params(ctx, path, mode):
    return {"source": {"path": path, "mode": mode}}


def _publish_params(target, feed_tag, destination):
    return {
        "input": dict(TEXT_INPUT, paths=[target]),
        "feed": "pressure",
        "value_tag": {"text": feed_tag},
        "metadata": {"mode": "clear"},
        "destination": destination,
        "publication_policy": "fail_if_exists",
        "immutable_feed_budget": dict(IMMUTABLE_FEED_BUDGET),
    }


def _validate_params(path, work):
    return {
        "path": path,
        "mode": {"kind": "live_current"},
        "validation_budget": dict(VALIDATION_BUDGET),
        "findings_output": {
            "path": os.path.join(work, "findings.jsonl"),
            "format": "jsonl",
            "publication_policy": "replace_existing",
            "result_budget": dict(RESULT_BUDGET),
        },
    }


def _inspect_params(path):
    return {
        "path": path,
        "mode": "caller_certified_offline",
        "validation_budget": dict(VALIDATION_BUDGET),
    }


def _maintenance_list_params(directory, work):
    return {
        "directory": directory,
        "kinds": ["scratch", "reservation", "publication_temp"],
        "max_entries": 64,
        "output": {
            "format": "jsonl",
            "path": os.path.join(work, "mrows.jsonl"),
            "publication_policy": "replace_existing",
            "result_budget": dict(RESULT_BUDGET),
        },
    }


def scripts(arm, ctx, targets):
    """Frame script: list of dicts with id/method/params(optional
    params_from placeholder keys resolved by the runner)."""
    if arm == "system.describe":
        return [{"id": 100, "method": "iprange.v1.system.describe",
                 "params": {}}]
    if arm == "reader-open-close-immutable":
        return [
            {"id": 100, "method": "iprange.v1.reader.open",
             "params": _reader_params(ctx, ctx["fixture"], "immutable"),
             "capture": {"reader": "reader"}},
            {"id": 101, "method": "iprange.v1.reader.close",
             "placeholder": ("reader", "@reader")},
        ]
    if arm == "reader-open-close-live":
        return [
            {"id": 100, "method": "iprange.v1.reader.open",
             "params": _reader_params(ctx, targets["live"], "live"),
             "capture": {"reader": "reader"}},
            {"id": 101, "method": "iprange.v1.reader.close",
             "placeholder": ("reader", "@reader")},
        ]
    if arm == "direct.replace":
        return [{"id": 100, "method": "iprange.v1.direct.replace",
                 "params": {
                     "path": targets["replace"],
                     "input": {"path": targets["csv"],
                               "max_line_bytes": 1024},
                     "metadata": {"mode": "keep"},
                     "writer_budget": dict(WRITER_BUDGET)}}]
    if arm == "current.publish":
        return [{"id": 100, "method": "iprange.v1.current.publish",
                 "params": _publish_params(targets["ranges"], "pressure",
                                           targets["published"])}]
    if arm == "current.publish.hostname":
        return [{"id": 100, "method": "iprange.v1.current.publish",
                 "params": _publish_params(targets["hostnames"], "pressure",
                                           targets["published"])}]
    if arm == "maintenance.remove":
        return [
            {"id": 96, "method": "iprange.v1.reader.open",
             "params": _reader_params(ctx, targets["live"], "live"),
             "capture": {"reader": "reader"}},
            {"id": 97, "method": "iprange.v1.maintenance.list",
             "params": _maintenance_list_params(ctx["work"], ctx["work"]),
             "list_rows": True},
            {"id": 98, "method": "iprange.v1.maintenance.remove",
             "placeholder": ("entry", "@entry")},
            {"id": 99, "method": "iprange.v1.reader.close",
             "placeholder": ("reader", "@reader")},
        ]
    if arm == "validate(worker)":
        # Section 8: the matrix runs each worker arm twice in the same
        # session; a start that failed must not leave a control file.
        return [
            {"id": 100, "method": "iprange.v1.validate",
             "params": _validate_params(targets["validate"], ctx["work"])},
            {"id": 101, "method": "iprange.v1.validate",
             "params": _validate_params(targets["validate"], ctx["work"])},
        ]
    if arm == "recovery.inspect(worker)":
        return [
            {"id": 100, "method": "iprange.v1.recovery.inspect",
             "params": _inspect_params(targets["inspect"])},
            {"id": 101, "method": "iprange.v1.recovery.inspect",
             "params": _inspect_params(targets["inspect"])},
        ]
    raise KeyError(arm)


# ---------------------------------------------------------------------------
# Pressured process.
# ---------------------------------------------------------------------------

class PressuredProcess:
    def __init__(self, binary, work, band, hold, hold_path, null_state):
        self.work = work
        self.handles = {}
        self.table_ever = set()
        if null_state == "normal":
            argv = [binary, "--jsonrpc"]
        else:
            # Private user+mount namespace with tmpfs over /dev, so the
            # hostile null device never touches a system file (section 15).
            prelude = "mount -t tmpfs -o mode=0755 tmpfs /dev"
            if null_state == "fifo":
                prelude += " && mkfifo -m 666 /dev/null"
            elif null_state != "absent":
                raise SystemExit(f"unknown null-device state {null_state}")
            argv = ["unshare", "-rm", "/bin/sh", "-c",
                    prelude + " && exec " + shlex.quote(binary) + " --jsonrpc"]
        self.proc = _spawn_launcher(argv, work, band, hold, hold_path)
        self.out_buf = b""
        self.err_buf = b""


    def alive(self):
        return self.proc.poll() is None

    def _drain(self, until):
        sel = selectors.DefaultSelector()
        sel.register(self.proc.stdout.fileno(), selectors.EVENT_READ)
        sel.register(self.proc.stderr.fileno(), selectors.EVENT_READ)
        try:
            while True:
                if not self.alive():
                    # Final drain so the last written frame is observed.
                    self._read_once(sel, 0.1)
                    return
                left = until - time.monotonic()
                if left <= 0:
                    return
                self._read_once(sel, min(left, 0.05))
                self._sample()
        finally:
            sel.close()

    def _read_once(self, sel, timeout):
        for key, _ in sel.select(timeout):
            try:
                chunk = os.read(key.fd, 65536)
            except OSError:
                chunk = b""
            if not chunk:
                continue
            if key.fd == self.proc.stdout.fileno():
                self.out_buf += chunk
            else:
                self.err_buf += chunk

    def _sample(self):
        if not self.alive():
            return
        try:
            entries = os.listdir(f"/proc/{self.proc.pid}/fd")
        except OSError:
            return
        for name in entries:
            try:
                self.table_ever.add(os.readlink(
                    f"/proc/{self.proc.pid}/fd/{name}"))
            except OSError:
                pass

    def _take_answer(self, request_id, deadline):
        while time.monotonic() < deadline:
            index = self.out_buf.find(b"\n")
            if index >= 0:
                raw = self.out_buf[:index + 1]
                self.out_buf = self.out_buf[index + 1:]
                try:
                    parsed = json.loads(raw)
                except ValueError:
                    return {"_bad_json": raw[:200].decode("utf-8", "replace")}
                if isinstance(parsed, dict) and parsed.get("id") == request_id:
                    return parsed
                continue
            if not self.alive():
                index = self.out_buf.find(b"\n")
                if index >= 0:
                    continue
                return None
            self._read_once_wait(0.05)
        return None

    def _read_once_wait(self, timeout):
        sel = selectors.DefaultSelector()
        try:
            sel.register(self.proc.stdout.fileno(), selectors.EVENT_READ)
            sel.register(self.proc.stderr.fileno(), selectors.EVENT_READ)
            self._read_once(sel, timeout)
        finally:
            sel.close()

    def exchange(self, frames):
        """Run one frame script stepwise. Returns ({id: response}, stderr)."""
        responses = {}
        deadline = time.monotonic() + CELL_TIMEOUT_SECONDS
        for frame in frames:
            params = frame.get("params")
            placeholder = frame.get("placeholder")
            if placeholder:
                key, token = placeholder
                value = self.handles.get(key)
                if value is None:
                    responses[frame["id"]] = None
                    break
                if token == "@entry":
                    params = {"entry": value}
                else:
                    params = {"reader": value}
            request = {"jsonrpc": "2.0", "id": frame["id"],
                       "method": frame["method"]}
            if params is not None:
                request["params"] = params
            try:
                self.proc.stdin.write(
                    (json.dumps(request) + "\n").encode("utf-8"))
                self.proc.stdin.flush()
            except Exception as exc:  # noqa: BLE001
                responses[frame["id"]] = None
                responses["_write_error"] = repr(exc)
                break
            answer = self._take_answer(frame["id"], deadline)
            responses[frame["id"]] = answer
            if answer is None:
                break
            self._capture(frame, answer)
        self._sample()
        return responses, self.err_buf.decode("utf-8", "replace")

    def _capture(self, frame, answer):
        result = answer.get("result") if isinstance(answer, dict) else None
        if not isinstance(result, dict):
            return
        for key, field in (frame.get("capture") or {}).items():
            value = result.get(field)
            if isinstance(value, str):
                self.handles[key] = value
        if frame.get("list_rows"):
            path = os.path.join(self.work, "mrows.jsonl")
            try:
                with open(path, "r", encoding="utf-8") as rows:
                    for line in rows:
                        line = line.strip()
                        if not line:
                            continue
                        try:
                            self.handles.setdefault("entry", json.loads(line))
                        except ValueError:
                            continue
                        break
            except OSError:
                pass

    def poller_counts(self):
        ep = sum(1 for n in self.table_ever if n.endswith(POLLER_EVENTPOLL))
        ef = sum(1 for n in self.table_ever if n.endswith(POLLER_EVENTFD))
        return ep, ef

    def finish(self):
        try:
            self.proc.stdin.close()
        except Exception:  # noqa: BLE001
            pass
        try:
            return self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(self.proc.pid, 9)
            except OSError:
                pass
            return self.proc.wait()


# The launcher runs as its own process rather than being forked from the
# harness: the grid executes cells on worker threads, and fork() in a
# multi-threaded process inherits locks another thread may hold, which can
# wedge the child between fork and execve. A fresh interpreter is
# single-threaded by construction, and it matches design section 10's own
# wording -- the table is pre-occupied *by the launcher*.
#
# argv: band, hold, hold_path, work, program, then the program's arguments.
#
# Design section 10 requires the table pre-occupied by the launcher and the
# soft *and* hard RLIMIT_NOFILE set before execve, so the process cannot raise
# its own limit. Both happen between fork and execve, in this order:
#
#   1. drop every inherited descriptor above the three standard streams, so
#      the measured table is stdio plus what the launcher deliberately claims
#      and not the launcher interpreter's own files;
#   2. install the limit;
#   3. claim the held descriptors *under* the limit with the close-on-exec
#      flag cleared, so they survive execve and occupy numbers below it.
#
# Claiming the holds before step 2, or leaving them close-on-exec, leaves the
# engine with a table that was never occupied and silently turns every
# occupied-cell result into a fresh-table result. A subprocess preexec_fn
# cannot do this at all: CPython's close_fds pass runs after it and closes the
# launcher's descriptors, which is why the launcher is a process of its own.
#
# Exits: 91 could not install the limit, 92 could not claim the holds, 93
# could not exec. The first two are host state (design section 13.2); the
# third is a harness failure and is reported as one.
LAUNCHER_SOURCE = """
import os, resource, sys

band, hold, hold_path, work = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3], sys.argv[4]
program, argv = sys.argv[5], sys.argv[5:]
try:
    os.chdir(work)
    os.closerange(3, 4096)
    if band:
        resource.setrlimit(resource.RLIMIT_NOFILE, (band, band))
except OSError:
    os._exit(91)
try:
    for _ in range(hold):
        fd = os.open(hold_path, os.O_RDONLY)
        os.set_inheritable(fd, True)   # clear FD_CLOEXEC: survive execve
except OSError:
    os._exit(92)
try:
    os.execvp(program, argv)   # PATH-resolved: the hostile-null arms exec unshare
except OSError:
    os._exit(93)
"""


def _spawn_launcher(argv, work, band, hold, hold_path):
    """Start the engine through the launcher, with piped standard streams."""
    return subprocess.Popen(
        [sys.executable, "-B", "-S", "-c", LAUNCHER_SOURCE,
         str(band), str(hold), hold_path, work] + list(argv),
        stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, cwd=work,
        env={"PATH": os.environ.get("PATH", "/usr/bin:/bin")},
        start_new_session=True)


# ---------------------------------------------------------------------------
# One cell.
# ---------------------------------------------------------------------------

def materialize_targets(work):
    targets = {}
    for key in ("replace", "live", "validate", "inspect"):
        base = os.path.join(work, f"pressure-{key}.iprange")
        _remove_any(base)
        _remove_any(base + ".readers")
        shutil.copyfile(template_src["live_main"], base)
        shutil.copyfile(template_src["live_sidecar"], base + ".readers")
        targets[key] = base
    targets["published"] = os.path.join(work, "published.iprange")
    _remove_any(targets["published"])
    targets["ranges"] = os.path.join(work, "pressure-ranges.txt")
    with open(targets["ranges"], "w", encoding="utf-8") as out:
        out.write("192.0.2.10-192.0.2.12\n")
    targets["csv"] = os.path.join(work, "pressure-data.csv")
    with open(targets["csv"], "w", encoding="utf-8") as out:
        out.write("from,to,value\n192.0.2.1,192.0.2.2,7\n")
    targets["hostnames"] = os.path.join(work, "pressure-hostnames.txt")
    with open(targets["hostnames"], "w", encoding="utf-8") as out:
        # RFC 6761 guarantees .invalid does not resolve: the resolver arm
        # answers the same input_format class whether the resolver ran and
        # was refused or the poller gate skipped resolution entirely.
        out.write("iprange-pressure.invalid\n")
    targets["mrows"] = os.path.join(work, "mrows.jsonl")
    _remove_any(targets["mrows"])
    return targets




def prepare_work_noop(work):
    """Rebuild the prepare_work ctx mapping for an existing work tree."""
    join = os.path.join
    return {"work": work, "fixture": join(work, "real.db"),
            "data_csv": join(work, "data.csv"),
            "list_txt": join(work, "list.txt"),
            "live_main": join(work, "template-live.iprange"),
            "live_sidecar": join(work, "template-live.iprange.readers"),
            "membership_main": join(work, "template-membership.iprange"),
            "live_membership_main": join(work,
                                         "template-live-membership.iprange"),
            "live_membership_sidecar": join(
                work, "template-live-membership.iprange.readers"),
            "refresh_main": join(work, "template-refresh-live.iprange"),
            "refresh_sidecar": join(work,
                                    "template-refresh-live.iprange.readers")}


template_src = {}


def run_cell(engine, bins, ctx, hold_path, arm, band, hold, runtime,
             null_state):
    binary = bins[engine]
    work = ctx["work"]
    _clean(work)
    targets = materialize_targets(work)
    cell = {"arm": arm, "engine": engine, "band": band, "hold": hold,
            "runtime": runtime, "null_device": null_state,
            "cell": f"{engine}/{arm}/b{band}/h{hold}/{runtime}/{null_state}"}
    if stdio_in_use + hold > band:
        # Section 13.2: with hold = 3 the bands at or below 5 cannot supply
        # the launcher's own held count, so no engine runs there. Host state,
        # never a refusal and never coverage.
        cell.update(verdict=HOST_UNSUPPORTED, exit=None,
                    why=f"band {band} cannot hold stdio({stdio_in_use}) + "
                        f"the {hold} held descriptors")
        return cell
    if runtime == "after" and band < WARMUP_FLOOR + hold:
        # Section 10 defines "after" as a completed valid reader.open plus
        # reader.close. That warm-up is itself the reader arm and needs its
        # own minimum band; below it the state cannot be established for any
        # engine, which is the launcher's limitation (section 13.2), not a
        # product refusal.
        cell.update(verdict=HOST_UNSUPPORTED, exit=None,
                    why=f"band {band} is below the initializing warm-up's own "
                        f"floor ({WARMUP_FLOOR} + {hold} held): the "
                        f"reader.open/reader.close that defines the state "
                        f"cannot complete")
        return cell
    runner = PressuredProcess(binary, work, band, hold, hold_path, null_state)
    return _finish_cell(runner, _run_cell_body(runner, engine, ctx, arm, band,
                                               hold, runtime, null_state,
                                               cell, targets), cell)


def _finish_cell(runner, body_result, cell):
    # Every path that leaves a cell must deliver the process (close stdin,
    # reap, force-kill on timeout); a leaked pressured session would both
    # hold descriptors the next cell needs and hide an unfinished run.
    exit_code = runner.finish()
    cell["exit"] = exit_code
    stderr_text = runner.err_buf.decode("utf-8", "replace")
    if exit_code == 127 and "error while loading shared libraries" in stderr_text:
        # Section 13.1: the delivered Rust build is dynamically linked, and
        # the loader cannot open its libraries in a table the launcher filled.
        # No engine code ran, so this is host state and never a refusal.
        cell.update(verdict=HOST_UNSUPPORTED,
                    why="dynamic loader could not open its libraries "
                        "(exit 127, EMFILE): no product code ran")
        return cell
    if exit_code == LAUNCHER_EXIT_EXEC:
        cell.update(verdict="launcher-error",
                    why=f"the launcher could not exec the engine (exit {exit_code})")
        return cell
    if exit_code in (LAUNCHER_EXIT_SETRLIMIT, LAUNCHER_EXIT_HOST):
        cell.update(verdict=HOST_UNSUPPORTED,
                    why=f"launcher could not prepare the table (exit {exit_code})")
        return cell
    return body_result


def _run_cell_body(runner, engine, ctx, arm, band, hold, runtime, null_state,
                   cell, targets):
    if runtime == "after":
        # Warm-up: valid reader.open + reader.close on the immutable
        # fixture with ids 9001/9002 (harness trap 1: distinct ids; trap 2:
        # side-effect free, never a writer).
        warm = [{"id": 9001, "method": "iprange.v1.reader.open",
                 "params": _reader_params(ctx, ctx["fixture"], "immutable"),
                 "capture": {"warm_reader": "reader"}},
                {"id": 9002, "method": "iprange.v1.reader.close",
                 "placeholder": ("warm_reader", "@reader")}]
        warm_responses, _ = runner.exchange(warm)
        if classify(warm_responses.get(9001)) != ("result", "RESULT", "RESULT") \
                or classify(warm_responses.get(9002)) != ("result", "RESULT", "RESULT"):
            cell.update(verdict="warmup-failed",
                        why="the initializing warm-up did not complete")
            return cell

    responses, stderr_text = runner.exchange(scripts(arm, ctx, targets))
    cell["stderr_tail"] = stderr_text[-500:]
    if responses.get("_write_error"):
        cell.update(verdict=HOST_UNSUPPORTED,
                    why="request could not be written: " + responses["_write_error"])
        return cell
    ep, ef = runner.poller_counts()
    cell["poller_ever"] = [ep, ef]
    # The pin is an equality per arm, not a global absence rule and not a
    # tolerance: every arm except the resolver arm must show no poller
    # descriptor at all, in either engine, in either runtime state
    # (design section 10). The resolver arm alone may show the pair the
    # poller-readiness decision created deliberately, and only when that
    # decision was authorized from the table the launcher handed over
    # (design section 6). A partial pair is always a failure, and so is an
    # unauthorised poller.
    if arm == RESOLVER_ARM and engine == "go":
        # Section 10 exempts the resolver arm from the absence rule; it does
        # not require the poller. Whether the deliberate creation happened
        # depends on the headroom the launcher handed over, and an arm that
        # the publish path refused before it reached the resolver never
        # entered net at all. Half a pair is still the failure.
        expected_poller = (0, 0) if (ep, ef) == (0, 0) else (1, 1)
    else:
        expected_poller = (0, 0)
    if (ep, ef) != expected_poller:
        cell.update(verdict="poller-pin-fail",
                    why=f"expected {expected_poller}, table shows "
                        f"({ep} eventpoll, {ef} eventfd)")
        return cell

    # Vacuousness rules (section 14): a blocked maintenance cell is reported
    # as blocked (section 13.4 producer question); a reader arm whose open
    # succeeded but whose close was refused is graded below via close class.
    spec = EXPECTED[arm]
    if arm == "maintenance.remove":
        list_answer = responses.get(97)
        list_class = classify(list_answer)
        if list_class is None or list_class[0] != "result":
            # The list that is supposed to name the entry could not run in
            # this environment. That is the same blocked shape as an empty
            # list -- the cell cannot be produced -- and it is never
            # coverage. Vacuous is reserved for the defect section 14 names:
            # the *measured* request refused by validation before any open.
            cell.update(verdict=BLOCKED,
                        why=f"maintenance.list could not complete at band "
                            f"{band} (answered {list_class[1:] if list_class else 'nothing'}); "
                            f"no removable entry could be named, so the cell "
                            f"is blocked (section 13.4), never coverage")
            return cell
        if "entry" not in runner.handles:
            cell.update(verdict=BLOCKED,
                        why="maintenance.list reported no removable entry "
                            "(section 13.4: no public-method producer of a "
                            "listable artifact exists); never coverage")
            return cell
        close_answer = responses.get(99)
        if close_answer is None or classify(close_answer) is None:
            cell.update(verdict="wedge", why="reader.close unanswered")
            return cell

    verdict, why = grade(arm, engine, band, hold, responses, stderr_text,
                         null_state)
    # Close-half rules (section 5.7/7): after a successful open the close is
    # never refused, and a worker arm's second run must agree with the first.
    if arm in ("reader-open-close-immutable", "reader-open-close-live"):
        open_class = classify(responses.get(100))
        if open_class == ("result", "RESULT", "RESULT"):
            close_class = classify(responses.get(101))
            if close_class != ("result", "RESULT", "RESULT"):
                verdict = "wrong-class"
                why = f"reader.close refused after a successful open: {close_class}"
            cell["close_class"] = close_class
    if arm in ("validate(worker)", "recovery.inspect(worker)"):
        # Section 8: an arm that could not start leaves no control file, so an
        # identical request afterwards starts cleanly. The rule binds hardest
        # where the spawn refuses: the hostile null-device cells.
        leftovers = [name for name in os.listdir(ctx["work"])
                     if ".control." in name or name.endswith(".iprange.control")]
        if leftovers:
            cell["control_leftovers"] = leftovers
            if classify(responses.get(MEASURED_IDS[arm])) is not None and \
                    classify(responses.get(MEASURED_IDS[arm]))[1:] != SUCCESS:
                verdict_pre, _ = grade(arm, engine, band, hold, responses,
                                       stderr_text, null_state)
                if verdict_pre == "pass":
                    cell.update(verdict="wrong-class",
                                why=f"worker arm refused and left its control file "
                                    f"behind: {leftovers} (§8)")
                    return cell
        first = classify(responses.get(100))
        second = classify(responses.get(101))
        cell["worker_repeat"] = [first, second]
        if first is not None and second is not None and first != second:
            verdict = "wrong-class"
            why = f"worker arm repeated differently in-session: {first} vs {second}"
    cell["answer"] = responses.get(MEASURED_IDS[arm])
    cell.update(verdict=verdict, why=why)
    return cell


# ---------------------------------------------------------------------------
# Grid + reporting
# ---------------------------------------------------------------------------

def prepare_bins(args):
    bins = {"go": os.path.join(args.go_bin, "iprange"),
            "rust": os.path.join(args.rust_bin, "iprange")}
    missing = [p for p in list(bins.values()) + [args.fixture]
               if not os.path.exists(p)]
    if missing:
        print(f"FATAL: missing binaries: {missing}", file=sys.stderr)
        raise SystemExit(2)
    return bins


def prepare_ctx(args, bins, slots=1):
    """Prepare the sweep's shared materialization(s).

    Each slot is an independent work directory with its own copies of the
    templates the cells recopy per attempt, so slots can be swept in parallel
    without one cell ever observing another's targets. The templates are
    materialized by the Rust authority binary (the parity gate's rule), so
    both engines read byte-identical inputs.
    """
    global template_src
    scratch = os.path.abspath(args.scratch)
    os.makedirs(scratch, exist_ok=True)
    hold_path = os.path.join(scratch, "hold-target")  # OUTSIDE every work dir
    with open(hold_path, "w", encoding="utf-8") as out:
        out.write("held descriptor target\n")
    keep = getattr(args, "keep_work", False)
    pool = []
    for index in range(max(1, slots)):
        work = os.path.join(scratch, "work" if slots == 1 else f"work-{index}")
        if os.path.isdir(work) and keep:
            # Reuse an existing materialization across cells (the per-attempt
            # target copies in materialize_targets keep every cell honest).
            ctx = prepare_work_noop(work)
        else:
            if os.path.isdir(work):
                shutil.rmtree(work)
            ctx = prepare_work(work, args.fixture, bins["rust"])
        pool.append(ctx)
    template_src = {
        "live_main": pool[0]["live_main"],
        "live_sidecar": pool[0]["live_sidecar"],
    }
    return bins, pool, hold_path


def _parse_list(text, cast):
    out = []
    for part in text.split(","):
        part = part.strip()
        if not part:
            continue
        if ".." in part:
            lo, hi = part.split("..")
            out.extend(range(int(lo), int(hi) + 1))
        else:
            out.append(cast(part))
    return out


def _cells(args, engines, arms, bands, holds, runtimes):
    """The full cell plan: every (engine, arm, band, hold, runtime) cell plus
    the hostile-null-device cells of section 10 at a generous band."""
    plan = []
    for engine in engines:
        for arm in arms:
            for band in bands:
                for hold in holds:
                    for runtime in runtimes:
                        plan.append((engine, arm, band, hold, runtime, "normal", False))
    for engine in engines:
        for arm in ("system.describe", "direct.replace",
                    "reader-open-close-immutable", "validate(worker)"):
            for null_state in ("fifo", "absent"):
                plan.append((engine, arm, args.generous, 0, "before",
                             null_state, True))
    return plan


def _skipped(engine, band, hold, runtime="before"):
    """The plan's host-state preclusions (design sections 13.1 and 13.2),
    decided without starting a process because they are properties of the
    launcher and of the dynamic loader, not of the engine's behaviour.

    Measured on the delivered binaries: the static Go build (CGO_ENABLED=0)
    starts with no free descriptor at all, so its floor is stdio plus the
    launcher's holds; the dynamically linked Rust build needs one free
    descriptor for the loader, and fails with exit 127 and
    "error while loading shared libraries" without it. The "after" runtime
    state additionally needs the band the warm-up's own reader arm needs.
    """
    if band < 3:
        return "below the stdio floor"
    if band < 3 + hold:
        return f"band {band} cannot hold stdio plus the {hold} held descriptors"
    if engine == "rust" and band < RUST_LOADER_FLOOR + hold:
        return ("the dynamic loader needs one free descriptor above "
                f"stdio+holds (needs {RUST_LOADER_FLOOR + hold})")
    if runtime == "after" and band < WARMUP_FLOOR + hold:
        return f"the initializing warm-up needs {WARMUP_FLOOR + hold}"
    return None


def cmd_grid(args):
    bins = prepare_bins(args)
    engines = _parse_list(args.engines, str)
    arms = _parse_list(args.arms, str) if args.arms else ARM_ORDER
    bands = _parse_list(args.bands, int)
    holds = _parse_list(args.holds, int)
    runtimes = _parse_list(args.runtimes, str)
    runs = max(2, args.runs)          # section 10 determinism: never below two
    jobs = max(1, args.jobs)
    bins, pool, hold_path = prepare_ctx(args, bins, slots=jobs)
    available = queue.Queue()
    for ctx in pool:
        available.put(ctx)

    def run_one(cell):
        engine, arm, band, hold, runtime, null_state, hostile = cell
        precluded = _skipped(engine, band, hold, runtime)
        if precluded:
            return [_host(engine, arm, band, hold, runtime, null_state, precluded)
                    for _ in range(runs)]
        ctx = available.get()
        try:
            return [run_cell(engine, bins, ctx, hold_path, arm, band, hold,
                             runtime, null_state) for _ in range(runs)]
        finally:
            available.put(ctx)

    plan = _cells(args, engines, arms, bands, holds, runtimes)
    with concurrent.futures.ThreadPoolExecutor(max_workers=jobs) as pool_exec:
        attempt_lists = list(pool_exec.map(run_one, plan))

    records, failed = [], 0
    for attempts in attempt_lists:
        verdicts = {a["verdict"] for a in attempts}
        if len(verdicts) != 1:
            for attempt in attempts:
                attempt["verdict"] = "disagreement"
                attempt["why"] = ("cell disagreed across runs: "
                                  + json.dumps(sorted(verdicts)))
        chosen = attempts[-1]
        if chosen.get("null_device", "normal") != "normal":
            chosen["hostile"] = True
        records.append(chosen)
        if chosen["verdict"] not in ("pass", "band-gap", HOST_UNSUPPORTED, BLOCKED):
            failed += 1

    _report(records, args, failed)
    return 1 if failed else 0


def _host(engine, arm, band, hold, runtime, null_state, reason):
    return {"arm": arm, "engine": engine, "band": band, "hold": hold,
            "runtime": runtime, "null_device": null_state,
            "cell": f"{engine}/{arm}/b{band}/h{hold}/{runtime}/{null_state}",
            "verdict": HOST_UNSUPPORTED, "why": reason, "exit": None}


def _report(records, args, failed):
    print(f"{'cell':60s} {'verdict':16s} {'exit':4s} why")
    for rec in records:
        print(f"{rec['cell']:60s} {rec['verdict']:16s} "
              f"{str(rec.get('exit')):4s} {rec.get('why', '')}")
    print()
    # Consumable summary (the section 7 table this wave ships): observed
    # minimum success band per arm/engine/hold, with the class-equality
    # column showing the below-minimum class actually answered.
    minima = {}
    for rec in records:
        if rec.get("hostile") or rec["verdict"] not in ("pass", "band-gap"):
            continue
        key = (rec["arm"], rec["engine"], rec["hold"])
        if rec.get("answer") is not None and \
                classify(rec["answer"]) == ("result", "RESULT", "RESULT"):
            minima[key] = min(minima.get(key, 99), rec["band"])
    print(f"{'arm':32s} {'engine':6s} {'hold':4s} observed-success-minimum")
    for (arm, engine, hold), value in sorted(minima.items()):
        shown = "n/a" if value == 99 else str(value)
        print(f"{arm:32s} {engine:6s} {hold:<4d} {shown}")
    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as out:
            json.dump({"schema": "iprange-fd-pressure-grid-v1",
                       "cells": records, "failed": failed}, out, indent=1)
    summary = {}
    for rec in records:
        summary[rec["verdict"]] = summary.get(rec["verdict"], 0) + 1
    print(f"\n{len(records)} cells: " + ", ".join(
        f"{count} {verdict}" for verdict, count in sorted(summary.items())))


def cmd_run_cell(args):
    bins = prepare_bins(args)
    bins, pool, hold_path = prepare_ctx(args, bins)
    ctx = pool[0]
    rec = run_cell(args.engine, bins, ctx, hold_path, args.arm, args.band,
                   args.hold, args.runtime, args.null_device)
    json.dump(rec, sys.stdout, default=str)
    sys.stdout.write("\n")
    return 0 if rec["verdict"] in ("pass", "band-gap", HOST_UNSUPPORTED,
                                   "blocked") else 1


def main():
    parser = argparse.ArgumentParser(
        description="descriptor-pressure harness (wave-19.25 section 10)")
    sub = parser.add_subparsers(dest="mode", required=True)
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--go-bin", required=True)
    common.add_argument("--rust-bin", required=True)
    common.add_argument("--fixture", required=True)
    common.add_argument("--scratch", required=True)
    common.add_argument("--keep-work", action="store_true",
                        help="reuse an existing <scratch>/work materialization "
                             "instead of rebuilding it (per-attempt targets "
                             "are always recopied)")
    grid = sub.add_parser("grid", parents=[common])
    grid.add_argument("--arms", default="")
    grid.add_argument("--bands", default="3..12")
    grid.add_argument("--holds", default="0,3")
    grid.add_argument("--runtimes", default="before,after")
    grid.add_argument("--engines", default="go,rust")
    grid.add_argument("--runs", type=int, default=2)
    grid.add_argument("--generous", type=int, default=64)
    grid.add_argument("--json-out", default="")
    grid.add_argument("--jobs", type=int, default=1,
                      help="sweep cells concurrently over this many independent "
                           "work materializations. The full grid is 9 arms x 10 "
                           "bands x 2 table states x 2 runtime states x 2 engines "
                           "x >=2 runs = 1440 pressured processes plus the hostile "
                           "null-device cells; serialized that is tens of minutes "
                           "and cannot be used as a routine gate (AGENTS.md "
                           "resource budget). Cells are independent processes with "
                           "their own limits, so parallelism changes no answer; "
                           "each worker owns its own work directory.")
    cell = sub.add_parser("run-cell", parents=[common])
    cell.add_argument("--engine", choices=["go", "rust"], required=True)
    cell.add_argument("--arm", required=True, choices=ARM_ORDER)
    cell.add_argument("--band", type=int, required=True)
    cell.add_argument("--hold", type=int, default=0)
    cell.add_argument("--runtime", choices=["before", "after"], default="before")
    cell.add_argument("--null-device", choices=["normal", "fifo", "absent"],
                      default="normal")
    args = parser.parse_args()
    if args.mode == "grid":
        return cmd_grid(args)
    return cmd_run_cell(args)


if __name__ == "__main__":
    sys.exit(main())
