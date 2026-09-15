#!/usr/bin/env python3
"""Committed stat-to-open swap-race battery.

What it proves
--------------
Both engines refuse a named pipe at the file-opening boundary, and the
refusal must still hold when the name changes between the engine's pre-open
``stat`` and its ``open(2)``. This runner races a ``rename(2)`` swapper
against that window for each arm in ``arms.py`` and reports per arm.

Why it cannot be satisfied by "no hangs"
----------------------------------------
The wave-19.22 operations probe reported ``attempts=30 hangs=0`` and exited
0 while its helper had failed to start (it was copied without ``swapper.py``)
and while its positive control answered an error. Both are aggregate failures
here: a helper must publish an explicit startup marker and move its counters,
each control must answer its frozen shape, and the swapper must record that
it completed replacements *while a request was in flight*. See README.md for
the design and the cost budget.

Usage
-----
    nice python3 v4/cli/races/runner.py --report-dir EMPTY_DIR \
        --rust BIN --go BIN --fixture-tool BIN [--arms meta,csv,feed,atlist,reader] \
        [--attempts 30]

Exit status is 0 only when every arm, every control, and every helper check
passed; the reasons are printed and written to ``race-battery.json`` inside
the report directory, which is never a committed evidence path.
"""

import argparse
import json
import os
import selectors
import stat as stat_module
import subprocess
import sys
import time

_HERE = os.path.dirname(os.path.abspath(__file__))


if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

import arms as arm_module  # noqa: E402
import service as race_service  # noqa: E402
from aggregate import (  # noqa: E402
    Battery,
    ReportPolicyError,
    _self_test as battery_self_test,
    judge_arm,
    judge_detector,
    sha256_file,
    validate_report_dir,
)
from command_sanitize import personal_path_in_report  # noqa: E402


def battery_dir_display():
    """The battery directory in a form that is safe to put in a report.

    ``Battery.write`` refuses any artifact carrying the operator's profile
    path, and a missing-helper failure has to name where it looked -- so the
    artifact records the stable ``v4/cli/races`` form and the console records
    the absolute path.  Without this, the loudest failure the battery has was
    itself refused by the privacy guard and wrote no report at all.
    """

    try:
        root = os.path.dirname(os.path.dirname(os.path.dirname(_HERE)))
        shown = os.path.relpath(_HERE, root)
    except (OSError, ValueError):
        return "the battery directory"
    if shown.startswith(("..", os.sep)) or personal_path_in_report(shown):
        return "the battery directory"
    return shown

SWAPPER = os.path.join(_HERE, "swapper.py")
SENTINEL = os.path.join(_HERE, "sentinel.py")
HELPERS = (SWAPPER, SENTINEL)

# A helper that never published its state file, or published it and stopped
# moving, is an aggregate failure; these bounds are what makes that judgment.
HELPER_STARTUP_SECONDS = 5.0
HELPER_POLL_SECONDS = 0.02
MIN_IN_FLIGHT_REPLACEMENTS = 1
DETECTOR_ATTEMPTS = 40
DETECTOR_MIN_WEDGES = 1


class HelperError(Exception):
    """A spawned helper did not prove it was racing."""


def read_json_state(path, deadline):
    """Wait for a helper's atomic state publication and return its counters."""

    limit = time.monotonic() + deadline
    last_error = None
    while time.monotonic() < limit:
        try:
            with open(path, encoding="utf-8") as stream:
                return json.load(stream)
        except FileNotFoundError as exc:
            last_error = exc
        except ValueError as exc:
            last_error = exc
        time.sleep(HELPER_POLL_SECONDS)
    raise HelperError(f"helper state {path} never appeared: {last_error}")


class Swapper:
    """The racing helper plus the runner-side proof that it is racing."""

    def __init__(self, target, template, work, label):
        self.target = target
        self.template = template
        self.work = work
        self.label = label
        self.state_path = os.path.join(work, f"swapper-{label}.json")
        self.stop_path = os.path.join(work, f"swapper-{label}.stop")
        self.marker_path = os.path.join(work, f"swapper-{label}.inflight")
        # One retained hard link per staged FIFO, so a reader that is parked
        # on a FIFO the raced name has already replaced stays reachable.
        self.stage_dir = os.path.join(work, f"stages-{label}")
        self.proc = None
        self.start = None
        self.log = []

    def start_racing(self, interval):
        for helper in HELPERS:
            if not os.path.isfile(helper):
                raise HelperError(f"helper missing from the battery: {helper}")
        for path in (self.state_path, self.stop_path, self.marker_path):
            if os.path.lexists(path):
                os.unlink(path)
        if os.path.isdir(self.stage_dir):
            import shutil
            shutil.rmtree(self.stage_dir)
        os.makedirs(self.stage_dir)
        self.proc = subprocess.Popen(
            [sys.executable, SWAPPER, "--target", self.target,
             "--regular-template", self.template, "--state", self.state_path,
             "--inflight-marker", self.marker_path,
             "--stop-file", self.stop_path, "--interval", str(interval),
             "--stage-dir", self.stage_dir],
            stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT,
            cwd=self.work, start_new_session=True)
        state = read_json_state(self.state_path, HELPER_STARTUP_SECONDS)
        # The marker must name the process the runner spawned: a stale state
        # file, or a helper that died before publishing, cannot impersonate a
        # running racer.
        if state.get("phase") not in ("ready", "racing", "stopped",
                                       "expired"):
            raise HelperError(f"helper never reported ready: {state}")
        if state.get("pid") != self.proc.pid:
            raise HelperError(
                f"helper startup marker pid {state.get('pid')!r} is not the "
                f"spawned pid {self.proc.pid}")
        if self.proc.poll() is not None:
            raise HelperError(
                f"helper exited at once with {self.proc.returncode}: {state}")
        self.start = dict(state)
        self.log.append(f"helper ready pid={self.proc.pid} counters={state}")
        return state

    def counters(self):
        state = read_json_state(self.state_path, HELPER_STARTUP_SECONDS)
        if state.get("phase") == "failed":
            raise HelperError(f"helper refused to start: {state}")
        return state

    def open_window(self):
        """Mark a request as in flight (both ends are sampled by the helper)."""

        with open(self.marker_path, "w", encoding="utf-8") as stream:
            stream.write("in-flight\n")

    def close_window(self):
        try:
            os.unlink(self.marker_path)
        except OSError:
            pass

    def stop(self):
        """Ask the helper to stop, reap it, and return its final counters."""

        if self.proc is None:
            raise HelperError("helper was never started")
        with open(self.stop_path, "w", encoding="utf-8") as stream:
            stream.write("stop\n")
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(self.proc.pid, 9)
            except ProcessLookupError:
                pass
            self.proc.wait(timeout=5)
        state = self.counters()
        if self.proc.returncode != 0:
            raise HelperError(
                f"helper exited with {self.proc.returncode}: {state}")
        if state.get("phase") not in ("stopped", "expired"):
            raise HelperError(f"helper did not close its own race: {state}")
        self.log.append(f"helper stopped rc=0 counters={state}")
        return state


def _control(arm, binary, kind, deadline):
    """One frozen control answer, with the swapper stopped."""

    if kind == "regular":
        arm_module._pin_regular(arm)
        expected = arm.stable_success
    else:
        arm_module.pin_fifo(arm)
        expected = arm.stable_refusal
    session = race_service.EngineSession(binary, arm.work)
    observation = session.call(arm.request(), deadline)
    if observation.kind == race_service.TIMEOUT:
        observation.blocked = session.blocked_threads()
        observation.wedge = race_service.confirm_wedge(
            arm.target, arm.payload(),
            lambda seconds: session.read_more(seconds).kind == "answered")
    session.kill()
    return {"control": kind, "kind": observation.kind,
            "shape": observation.shape, "expected": expected,
            "ok": observation.kind == "answered" and observation.shape in expected,
            "blocked": list(observation.blocked), "wedge": observation.wedge}


def run_arm(battery, label, binary, arm_name, options, report_dir):
    """Execute one arm on one engine and record its verdict."""

    subject = f"{label}/{arm_name}"
    work = os.path.join(report_dir, "work", f"{label}-{arm_name}")
    if os.path.isdir(work):
        import shutil
        shutil.rmtree(work)
    os.makedirs(work)
    session_factory = lambda directory: race_service.EngineSession(  # noqa: E731
        binary, directory)
    try:
        arm = arm_module.build_arm(arm_name, work, binary,
                                   options.fixture_tool, session_factory)
    except (RuntimeError, ValueError, OSError) as exc:
        battery.fail("fixture-setup", subject, f"{type(exc).__name__}: {exc}")
        return
    record = {"engine": label, "arm": arm_name,
              **arm_module.describe(arm)}

    # Controls first, with no helper running: they are what makes "hangs=0"
    # in the race phase mean something.
    controls = [_control(arm, binary, "regular", options.deadline),
                _control(arm, binary, "fifo", options.deadline)]
    record["controls"] = controls
    for control in controls:
        if not control["ok"]:
            battery.fail("control-unexpected", subject,
                         f"stable-{control['control']} answered "
                         f"{control['shape']} (kind {control['kind']}, "
                         f"expected one of {control['expected']}, "
                         f"blocked={control['blocked']}, "
                         f"wedge={control['wedge']})")

    swapper = Swapper(arm.target, arm.regular_template, work,
                      f"{label}-{arm_name}")
    final = None
    try:
        swapper.start_racing(options.interval)
        baseline = swapper.counters()
        for index in range(options.attempts):
            swapper.open_window()
            try:
                session = race_service.EngineSession(binary, work)
                observation = session.call(arm.request(), options.deadline)
                if observation.kind == race_service.TIMEOUT:
                    observation.blocked = session.blocked_threads()
                    observation.wedge = race_service.confirm_wedge(
                        arm.target, arm.payload(),
                        lambda seconds: (
                            session.read_more(seconds).kind == "answered"),
                        stage_dir=swapper.stage_dir)
                session.kill()
            finally:
                swapper.close_window()
            classes = record.setdefault("classes", {})
            classes[observation.shape] = classes.get(observation.shape, 0) + 1
            record["attempts_completed"] = record.get("attempts_completed", 0) + 1
            if observation.kind == race_service.TIMEOUT:
                record.setdefault("hangs", []).append(observation.record())
                if len(record["hangs"]) >= options.stop_after:
                    break
        final = swapper.stop()
    except HelperError as exc:
        battery.fail("helper-execution", subject, str(exc))
    except OSError as exc:
        battery.fail("helper-execution", subject, f"helper I/O: {exc}")
    finally:
        swapper.close_window()
        if final is not None:
            deltas = {key: final.get(key, 0) - baseline.get(key, 0)
                      for key in ("toggles", "to_fifo", "to_regular",
                                  "in_flight_to_fifo",
                                  "in_flight_to_regular", "errors")}
            record["replacement_activity"] = {
                "to_fifo": deltas["to_fifo"],
                "to_regular": deltas["to_regular"],
                "toggles": deltas["toggles"],
                "helper_errors": deltas["errors"],
                "observed": deltas["toggles"] > 0}
            record["timing_window"] = {
                "in_flight_to_fifo": deltas["in_flight_to_fifo"],
                "in_flight_to_regular": deltas["in_flight_to_regular"],
                "required_in_flight_to_fifo": MIN_IN_FLIGHT_REPLACEMENTS,
                "exercised": (deltas["in_flight_to_fifo"]
                              >= MIN_IN_FLIGHT_REPLACEMENTS)}
            record["refusal_observed_in_race"] = sum(
                count for shape, count in record.get("classes", {}).items()
                if shape in arm.raced_refusals)
            record["raced_only_refusals_observed"] = sum(
                count for shape, count in record.get("classes", {}).items()
                if shape in set(arm.raced_refusals) - set(arm.stable_refusal))
        battery.write_log(report_dir, subject, swapper.log)
    if final is None:
        record.setdefault("replacement_activity", {"observed": False})
        record.setdefault("timing_window", {"exercised": False})
        record.setdefault("refusal_observed_in_race", 0)
        record.setdefault("classes", {})
    judge_arm(battery, subject, record, attempts=options.attempts)
    battery.add(subject, record)


def run_detector(battery, options, report_dir):
    """Prove the hang detector fires against real replacement activity.

    The sentinel is a naive opener (``stat`` then a bare blocking ``open``),
    so with a racing swapper it must lose the window sometimes. A detector
    that reports no confirmed wedge here cannot be trusted when a product arm
    reports no hangs.
    """

    subject = "detector/sentinel"
    work = os.path.join(report_dir, "work", "detector")
    if os.path.isdir(work):
        import shutil
        shutil.rmtree(work)
    os.makedirs(work)
    template = os.path.join(work, "template.ranges")
    with open(template, "w", encoding="utf-8") as stream:
        stream.write(arm_module.NETSET_ROWS)
    target = os.path.join(work, "sentinel.t")
    os.link(template, target)

    # Control: the sentinel must answer promptly when nothing races it.
    quiet = _sentinel_once(work, target, template, options)
    if not quiet["ok"]:
        battery.fail("control-unexpected", subject,
                     f"stable-regular sentinel answered {quiet['shape']} "
                     f"(expected OPENED promptly): {quiet}")
    swapper = Swapper(target, template, work, "detector")
    record = {"subject_label": "detector", "attempts": 0,
              "hangs": 0, "confirmed_wedges": 0}
    try:
        swapper.start_racing(options.interval)
        baseline = swapper.counters()
        for index in range(DETECTOR_ATTEMPTS):
            swapper.open_window()
            try:
                outcome = _sentinel_once(work, target, template, options,
                                         stage_dir=swapper.stage_dir)
            finally:
                swapper.close_window()
            record["attempts"] += 1
            if outcome["kind"] == "hang":
                record["hangs"] += 1
                if outcome["confirmed"]:
                    record["confirmed_wedges"] += 1
            if record["confirmed_wedges"] >= DETECTOR_MIN_WEDGES and \
                    record["hangs"] >= 2 and \
                    swapper.counters()["in_flight_to_fifo"] - \
                    baseline["in_flight_to_fifo"] >= MIN_IN_FLIGHT_REPLACEMENTS:
                break
        final = swapper.stop()
    except HelperError as exc:
        battery.fail("helper-execution", subject, str(exc))
        final = None
    finally:
        swapper.close_window()
        battery.write_log(report_dir, subject, swapper.log)
    if final is not None:
        deltas_in_flight = (final.get("in_flight_to_fifo", 0)
                            - baseline.get("in_flight_to_fifo", 0))
        record["replacement_activity"] = {
            "toggles": final.get("toggles", 0) - baseline.get("toggles", 0),
            "to_fifo": final.get("to_fifo", 0) - baseline.get("to_fifo", 0),
            "observed": final.get("toggles", 0) > baseline.get("toggles", 0)}
        record["timing_window"] = {
            "in_flight_to_fifo": deltas_in_flight,
            "exercised": deltas_in_flight >= MIN_IN_FLIGHT_REPLACEMENTS}
    else:
        record["replacement_activity"] = {"observed": False}
        record["timing_window"] = {"exercised": False}
    judge_detector(battery, subject, record,
                   minimum_wedges=DETECTOR_MIN_WEDGES)
    battery.add(subject, record)


def _sentinel_once(work, target, template, options, stage_dir=None):
    """Run one sentinel attempt and classify it.

    The sentinel must first announce that it started; without that marker the
    attempt is an aggregate failure rather than a hang, because a helper that
    never executed is indistinguishable from a helper that blocked. A silent
    attempt is then confirmed the same way a product attempt is: attach a
    writer to the FIFO inode it is parked on and see whether it answers.
    """

    marker = os.path.join(work, f"sentinel-{os.getpid()}-{time.monotonic_ns()}.json")
    if os.path.lexists(marker):
        os.unlink(marker)
    proc = subprocess.Popen([sys.executable, SENTINEL, "--target", target,
                             "--startup-marker", marker],
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                            stdin=subprocess.DEVNULL, cwd=work,
                            start_new_session=True)
    lines = []

    def collect(deadline):
        selector = selectors.DefaultSelector()
        selector.register(proc.stdout.fileno(), selectors.EVENT_READ)
        try:
            limit = time.monotonic() + deadline
            pending = b""
            while time.monotonic() < limit:
                if not selector.select(0.05):
                    continue
                try:
                    chunk = os.read(proc.stdout.fileno(), 4096)
                except OSError:
                    return False
                if not chunk:
                    return False
                pending += chunk
                while b"\n" in pending:
                    line, pending = pending.split(b"\n", 1)
                    lines.append(line.decode("utf-8", "replace"))
                    if line.startswith(b"OPENED"):
                        return True
            return False
        finally:
            selector.close()

    with open(template, "rb") as stream:
        payload = stream.read()
    try:
        try:
            read_json_state(marker, HELPER_STARTUP_SECONDS)
        except HelperError as exc:
            return {"ok": False, "kind": "helper-missing", "shape": "no-start",
                    "confirmed": False, "detail": str(exc)}
        if collect(options.deadline):
            return {"ok": True, "kind": "answered", "shape": "OPENED",
                    "confirmed": False, "lines": list(lines)}
        blocked = []
        try:
            for tid in os.listdir(f"/proc/{proc.pid}/task"):
                try:
                    with open(f"/proc/{proc.pid}/task/{tid}/wchan",
                              encoding="utf-8") as stream:
                        blocked.append(f"tid{tid}={stream.read().strip()}")
                except OSError:
                    continue
        except OSError:
            pass
        wedge = race_service.confirm_wedge(
            target, payload, collect, stage_dir=stage_dir)
        confirmed = wedge.startswith("wedge-confirmed")
        return {"ok": bool(confirmed), "kind": "hang",
                "shape": "OPENED" if confirmed else "blocked",
                "confirmed": bool(confirmed), "blocked": blocked,
                "wedge": wedge}
    finally:
        if proc.poll() is None:
            try:
                os.killpg(proc.pid, 9)
            except ProcessLookupError:
                pass
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass
        try:
            os.unlink(marker)
        except OSError:
            pass


def parse_arguments():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--report-dir", default=None,
                        help="mandatory (except with --self-test) empty (or "
                             "absent) output directory outside the checkout; "
                             "the battery never writes committed evidence "
                             "paths")
    parser.add_argument("--rust", metavar="PATH", help="rust iprange binary")
    parser.add_argument("--go", metavar="PATH", help="go iprange binary")
    parser.add_argument("--fixture-tool", metavar="PATH",
                        help="v4-fixture producer (needed by the reader arm)")
    parser.add_argument("--arms", default=",".join(arm_module.ARM_NAMES),
                        help="comma separated subset of "
                             + ",".join(arm_module.ARM_NAMES))
    parser.add_argument("--attempts", type=int, default=30)
    parser.add_argument("--deadline", type=float, default=1.5)
    parser.add_argument("--stop-after", type=int, default=3)
    parser.add_argument("--interval", type=float, default=0.0002,
                        help="swapper sleep between renames, seconds; the "
                             "default keeps thousands of completed "
                             "replacements per second so every attempt window "
                             "contains several, without pinning a core")
    parser.add_argument("--provenance-note", default=None)
    parser.add_argument("--self-test", action="store_true",
                        help="run the offline mutation controls that prove a "
                             "weakened battery cannot report success, and "
                             "exit without touching any binary")
    return parser.parse_args()


def main():
    options = parse_arguments()
    if options.self_test:
        return battery_self_test()
    engines = {}
    for label, path in (("rust", options.rust), ("go", options.go)):
        if path:
            if not os.path.isabs(path) or not os.access(path, os.X_OK):
                print(f"FAIL: {label} binary is not an absolute executable: "
                      f"{path}", file=sys.stderr)
                return 2
            engines[label] = path
    if not engines:
        print("FAIL: at least one of --rust/--go is required", file=sys.stderr)
        return 2
    if options.attempts < 1:
        print("FAIL: --attempts must be >= 1", file=sys.stderr)
        return 2
    selected = [name for name in options.arms.split(",") if name]
    for name in selected:
        if name not in arm_module.ARM_NAMES:
            print(f"FAIL: unknown arm {name!r}; choose from "
                  f"{','.join(arm_module.ARM_NAMES)}", file=sys.stderr)
            return 2
    if "reader" in selected and not options.fixture_tool:
        print("FAIL: the reader arm requires --fixture-tool", file=sys.stderr)
        return 2
    try:
        report_dir = validate_report_dir(options.report_dir)
    except ReportPolicyError as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 2

    binaries = {label: {"path": path, "sha256": sha256_file(path)}
                for label, path in engines.items()}
    fixture_tool = ({"path": options.fixture_tool,
                     "sha256": sha256_file(options.fixture_tool)}
                    if options.fixture_tool else None)
    battery = Battery(binaries=binaries, fixture_tool=fixture_tool,
                      options={"arms": selected, "attempts": options.attempts,
                               "deadline": options.deadline,
                               "stop_after": options.stop_after,
                               "interval": options.interval})
    # A missing helper is decided before any attempt, so "no hangs" can never
    # be produced by a battery that cannot race at all.
    for helper in HELPERS:
        if not os.path.isfile(helper):
            battery.fail("helper-missing", "preflight",
                         f"{os.path.basename(helper)} is not present in "
                         f"{battery_dir_display()}; the battery cannot race "
                         "without it")
            print(f"FAIL: {battery.failures[-1]}", file=sys.stderr)
            # The operator still gets the absolute path; only the staged
            # artifact is kept free of it.
            print(f"FAIL: helper directory is {_HERE}", file=sys.stderr)
            try:
                battery.write(report_dir, options.provenance_note)
            except ReportPolicyError as exc:
                print(f"FAIL: {exc}", file=sys.stderr)
            return 1

    for label, path in engines.items():
        for arm_name in selected:
            run_arm(battery, label, path, arm_name, options, report_dir)
    run_detector(battery, options, report_dir)

    try:
        written = battery.write(report_dir, options.provenance_note)
    except ReportPolicyError as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 2
    for line in battery.summary_lines():
        print(line)
    print(f"report: {written}")
    return battery.exit_code()


if __name__ == "__main__":
    sys.exit(main())
