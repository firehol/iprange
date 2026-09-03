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
import json
import os
import shutil
import subprocess
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

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


def run_mode(mode, steps):
    case = {
        "schema": "iprange-cli-case-v1",
        "name": f"sensitivity:{mode}",
        "fixtures": [],
        "steps": steps,
    }
    work = tempfile.mkdtemp(prefix="iprange-sens-")
    runner = CaseRunner(binary=None, case=case, work_dir=work, implementation="fake")
    runner.service_argv = [sys.executable, FAKE_SERVER, mode]
    runner.service = JsonRpcService(runner.service_argv, "fake")
    try:
        runner.run()
        return True, "PASS"
    except (AssertionError, ValueError, ValidationError) as exc:
        return False, f"FAIL {exc}"
    finally:
        runner.service.close()
        shutil.rmtree(work, ignore_errors=True)


def main():
    failures = []
    for mode, steps, want, marker in MODES:
        passed, detail = run_mode(mode, steps)
        ok = (passed and want == "PASS") or (not passed and want == "FAIL"
                                             and marker in detail)
        status = "OK " if ok else "BAD"
        print(f"{status} {mode:24s} want={want:4s} got={detail[:90]}")
        if not ok:
            failures.append((mode, want, marker, detail))
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
