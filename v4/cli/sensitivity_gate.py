#!/usr/bin/env python3
"""Sensitivity gate: the external runner rejects broken JSON-RPC servers.

Spawns v4/cli/fake_server.py in every deliberate-brokenness mode and
drives it through the exact production client path (CaseRunner +
JsonRpcService from v4/cli/run.py). The gate proves, before any
production handler exists, that the external client:

- PASSes well-behaved describe/ranges/lookup responses;
- FAILs non-JSON frames, id/jsonrpc corruption, fractional decimal
  counters, missing/unknown result members, method-echo fabrication,
  out-of-order cursor rows, out-of-range values, and any response to
  a notification (cancel_replies mode);

with the documented failure reason in every case. Exit status is 1 when
any sensitivity expectation is violated.

Usage:
  nice python3 v4/cli/sensitivity_gate.py
"""
import os
import shutil
import subprocess
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
_SELF_DIR = os.path.dirname(os.path.abspath(__file__))

from command_sanitize import (  # noqa: E402  (side-effect free)
    audit_report_writers,
    owned_temp_root,
    require_paths_outside_profile,
    run_shared_self_test,
    write_committed_report,
)
from run import CaseRunner, JsonRpcService  # noqa: E402
from schema.engine import ValidationError  # noqa: E402

FAKE_SERVER = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fake_server.py")
READER = "a" * 32
CURSOR = "c" + "0" * 31

DESCRIBE_STEP = {
    "kind": "rpc",
    "actor": "consumer",
    "method": "iprange.v1.system.describe",
    "params": {},
    "expect_result": {"method": "iprange.v1.system.describe"},
}

CANCEL_NOTIFICATION_STEP = {
    "kind": "rpc",
    "actor": "consumer",
    "method": "iprange.v1.cancel",
    "params": {"request_id": "never-pending-cancel-id"},
    "notification": True,
}

# The runner now requires successful responses to reference a reader this
# connection actually opened, so every reader-using mode opens one through
# the fake server first and reuses the captured handle.
def _open_step():
    return {
        "kind": "rpc",
        "actor": "consumer",
        "method": "iprange.v1.reader.open",
        "params": {"source": {"path": "/fake/db.iprange", "mode": "immutable"}},
        "expect_result": {"reader": {"$ignore": True},
                          "info": {"$ignore": True}},
        "capture": ["reader"],
    }

# mode -> (steps, expected outcome, marker the FAIL reason must contain).
MODES = [
    # Positive controls: a well-behaved server must PASS.
    ("describe_ok", [DESCRIBE_STEP], "PASS", ""),
    ("rows_ok", [_open_step(), 
        {"kind": "rpc", "actor": "consumer", "method": "iprange.v1.reader.ranges.open",
         "params": {"reader": "$CAPTURE/reader", "view": {"kind": "feed", "feed": "feed-a"},
                    "direction": "forward", "batch_size": 4096},
         "expect_result": {"method": "iprange.v1.reader.ranges.open",
                           "cursor": {"$ignore": True}},
         "capture": ["cursor"]},
        {"kind": "rpc", "actor": "consumer", "method": "iprange.v1.reader.ranges.next",
         "params": {"cursor": "$CAPTURE/cursor"},
         "expect_result": {"method": "iprange.v1.reader.ranges.next",
                           "records": {"$ignore": True}, "done": {"$ignore": True}}},
    ], "PASS", ""),
    ("lookup_ok", [_open_step(), 
        {"kind": "rpc", "actor": "consumer", "method": "iprange.v1.reader.lookup",
         "params": {"reader": "$CAPTURE/reader", "addresses": ["10.0.0.1", "10.0.0.2"]},
         "expect_result": {"method": "iprange.v1.reader.lookup",
                           "matches": {"$ignore": True}}},
    ], "PASS", ""),
    # Envelope corruption.
    ("describe_bad_json", [DESCRIBE_STEP], "FAIL", "parse error:"),
    ("describe_bad_id", [DESCRIBE_STEP], "FAIL", "response id"),
    ("describe_no_jsonrpc", [DESCRIBE_STEP], "FAIL", 'jsonrpc must be "2.0"'),
    ("describe_bad_version", [DESCRIBE_STEP], "FAIL", 'jsonrpc must be "2.0"'),
    # Result schema corruption.
    ("describe_bad_decimal", [DESCRIBE_STEP], "FAIL", "not a canonical unsigned decimal"),
    ("describe_missing_method", [DESCRIBE_STEP], "FAIL", "missing required field"),
    ("describe_unknown_member", [DESCRIBE_STEP], "FAIL", "unknown member"),
    ("describe_false_outcome", [DESCRIBE_STEP], "FAIL", "does not match requested method"),
    # A response to a notification must never be accepted: the reply
    # desynchronizes the stream and the next correlated read fails.
    ("cancel_replies", [CANCEL_NOTIFICATION_STEP, DESCRIBE_STEP], "FAIL", "response id"),
    # Protocol semantics corruption.
    ("rows_bad_order", [_open_step(), 
        {"kind": "rpc", "actor": "consumer", "method": "iprange.v1.reader.ranges.open",
         "params": {"reader": "$CAPTURE/reader", "view": {"kind": "feed", "feed": "feed-a"},
                    "direction": "forward", "batch_size": 4096},
         "expect_result": {"method": "iprange.v1.reader.ranges.open",
                           "cursor": {"$ignore": True}},
         "capture": ["cursor"]},
        {"kind": "rpc", "actor": "consumer", "method": "iprange.v1.reader.ranges.next",
         "params": {"cursor": "$CAPTURE/cursor"},
         "expect_result": {"method": "iprange.v1.reader.ranges.next",
                           "records": {"$ignore": True}, "done": {"$ignore": True}}},
    ], "FAIL", "out of"),
    ("rows_wrong_value", [_open_step(), 
        {"kind": "rpc", "actor": "consumer", "method": "iprange.v1.reader.lookup",
         "params": {"reader": "$CAPTURE/reader", "addresses": ["10.0.0.1"]},
         "expect_result": {"method": "iprange.v1.reader.lookup",
                           "matches": {"$ignore": True}}},
    ], "FAIL", "outside 0..4294967295"),
]


def run_mode(mode, steps, want):
    case = {
        "schema": "iprange-cli-case-v1",
        "name": f"sensitivity:{mode}",
        "fixtures": [],
        "steps": steps,
    }
    work = tempfile.mkdtemp(prefix="iprange-sens-",
                             dir=owned_temp_root())
    runner = CaseRunner(binary=None, case=case, work_dir=work, implementation="fake")
    runner.service_argv = [sys.executable, FAKE_SERVER, mode]
    runner.service = JsonRpcService(runner.service_argv, "fake")
    try:
        runner.run()
        return True, "PASS"
    except (AssertionError, ValueError, ValidationError) as exc:
        return False, f"FAIL {exc}"
    finally:
        # Deliberate-brokenness controls leave surplus frames in the
        # stream (the desync evidence); the ordinary-session final
        # validation must not mask the exchange-level failure reason.
        runner.service.close(broken_exchange=(want != "PASS"))
        shutil.rmtree(work, ignore_errors=True)


# Executed-control counts for ``--self-test``, per class.  They are literals,
# not a recount of ``MODES``: the point of the pin is that a mode dropped from
# the table, or skipped by a guard that started matching, fails the self-test
# with the same authority as a mode that behaves wrongly.  Updating the table
# therefore means updating the pin, which is the review signal the wave-19.25
# closure report asked for ("0 failures" alone is not proof anything ran).
SELF_TEST_RUNS = 14
SELF_TEST_INVERSIONS = 2
SELF_TEST_STRUCTURAL = 6


def _verdict(mode, steps, want, marker):
    """One mode's outcome judged against its recorded expectation."""
    passed, detail = run_mode(mode, steps, want)
    ok = ((passed and want == "PASS" and not marker)
          or (not passed and want == "FAIL" and marker in detail))
    return ok, detail


def _self_test():
    """Drive every deliberate-brokenness mode offline, with count pins.

    Offline here means real but cheap: each mode spawns
    ``v4/cli/fake_server.py``, a pure-Python responder, and drives it
    through the production client path (``CaseRunner`` +
    ``JsonRpcService``), so the gate exercises the same client code the
    battery uses without needing a built product binary.

    Three classes of control:

    * every ``MODES`` entry must reach its recorded verdict (3 PASS-want,
      11 FAIL-want, each FAIL-want also matching its required reason marker);
    * a non-vacuity class: two expectations are run *inverted*, and the gate
      must report the mismatch -- without this, a client that accepted
      everything would still print 14 ok lines; and
    * structural controls that pin the table's shape, so the mode list cannot
      shrink, gain a duplicate, or lose the marker that makes a FAIL verdict
      meaningful.
    """
    executed = {"run": 0, "inversion": 0, "structural": 0}
    problems = []

    def check(kind, label, condition, detail=""):
        executed[kind] += 1
        ok = bool(condition)
        print(f"{'ok  ' if ok else 'BAD '} {label:56} {detail[:80]}")
        if not ok:
            problems.append(label)

    for mode, steps, want, marker in MODES:
        ok, detail = _verdict(mode, steps, want, marker)
        check("run", f"{mode} reaches its recorded {want} verdict", ok,
              detail)

    # Non-vacuity: ask for the opposite of what the fake server really does.
    # A PASS-want mode must then be reported as a mismatch; if the client
    # under test were indiscriminating, the loop above would look identical.
    for mode, steps, want, marker in MODES[:SELF_TEST_INVERSIONS]:
        flipped = "FAIL" if want == "PASS" else "PASS"
        wrong, detail = _verdict(mode, steps, flipped, marker)
        check("inversion", f"{mode} judged against the wrong expectation",
              not wrong, f"expected a mismatch; got {detail[:60]}")

    check("structural", "the mode table holds exactly the pinned mode count",
          len(MODES) == SELF_TEST_RUNS, f"{len(MODES)} modes")
    check("structural", "every mode name is unique",
          len({entry[0] for entry in MODES}) == len(MODES), "duplicate mode")
    pass_want = sum(1 for entry in MODES if entry[2] == "PASS")
    fail_want = sum(1 for entry in MODES if entry[2] == "FAIL")
    check("structural", "the positive/negative split is the pinned one",
          (pass_want, fail_want) == (3, 11),
          f"{pass_want} PASS-want, {fail_want} FAIL-want")
    check("structural",
          "every FAIL-want mode carries the reason marker it is judged by",
          all(entry[3] for entry in MODES if entry[2] == "FAIL"),
          "a FAIL-want mode has no marker, so any failure would satisfy it")
    check("structural", "the fake server this gate drives is the committed one",
          os.path.isfile(FAKE_SERVER), FAKE_SERVER)
    check("structural", "this writer commits through the shared provenance owner",
          not audit_report_writers(cli_dir=_SELF_DIR, writers=["sensitivity_gate.py"],
                                   artifacts=False),
          "see command_sanitize.audit_report_writers")

    run_shared_self_test("sensitivity_gate")
    for problem in problems:
        print(f"FAIL sensitivity self-test: {problem}")
    expected = {"run": SELF_TEST_RUNS, "inversion": SELF_TEST_INVERSIONS,
                "structural": SELF_TEST_STRUCTURAL}
    if executed != expected:
        print(f"FAIL sensitivity self-test: executed={executed}, "
              f"expected={expected}")
        return 1
    if problems:
        print(f"sensitivity self-test FAILED: {len(problems)} problem(s)")
        return 1
    print(f"sensitivity self-test PASSED: {SELF_TEST_RUNS} modes, "
          f"{SELF_TEST_INVERSIONS} inversions, "
          f"{SELF_TEST_STRUCTURAL} structural controls")
    return 0


def main():
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--json-report", metavar="PATH",
                        help="write the per-mode outcomes and the reviewed "
                             "revision to a JSON evidence file")
    parser.add_argument("--self-test", action="store_true",
                        help="drive every mode against the committed fake "
                             "server and pin the executed-control counts")
    args = parser.parse_args()
    if args.self_test:
        return _self_test()

    require_paths_outside_profile((("--json-report", args.json_report),))
    failures = []
    modes = []
    for mode, steps, want, marker in MODES:
        ok, detail = _verdict(mode, steps, want, marker)
        status = "OK " if ok else "BAD"
        print(f"{status} {mode:24s} want={want:4s} got={detail[:90]}")
        modes.append({"mode": mode, "want": want, "got": detail,
                     "expected_marker": marker, "ok": ok})
        if not ok:
            failures.append((mode, want, marker, detail))
    if args.json_report:
        target = args.json_report
        if not os.path.isabs(target):
            target = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                  target)
        report = {
            "schema": "iprange-cli-sensitivity-report-v1",
            "modes": modes,
            "mode_count": len(modes),
            "failures": [list(entry) for entry in failures],
            "result": "PASS" if not failures else "FAIL",
        }
        # Provenance and the privacy scan are owned by the shared writer:
        # a per-mode record is a measurement of the operator's tree, and the
        # modes list carries command lines and paths.
        write_committed_report(
            target, report,
            caller_paths=(("--json-report", args.json_report),), indent=1)

    print()
    if failures:
        for mode, want, marker, detail in failures:
            print(f"MISMATCH {mode}: wanted {want}"
                  + (f" marker {marker!r}" if marker else "")
                  + f"; got {detail}")
        print(f"\nsensitivity gate FAILED: {len(failures)} mismatch(es)")
        return 1
    print(f"sensitivity gate PASSED: {len(MODES)} modes")
    return 0


if __name__ == "__main__":
    sys.exit(main())
