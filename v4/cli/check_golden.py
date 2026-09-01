#!/usr/bin/env python3
"""Validate every golden exchange and declarative case against the strict
schemas without spawning any binary.

This is the cheap CI-grade wire gate for the v4/cli qualification tree
(runs in well under a second):

- every golden request parses as a JSON-RPC frame, passes its params
  schema, and (when a response exists) the response parses, echoes the
  request id, passes its result schema, and echoes the method;
- iprange.v1.cancel is a notification: it has no id and no response;
- every case file in v4/cli/cases validates against the
  iprange-cli-case-v1 schema.

Usage:
  nice python3 v4/cli/check_golden.py [--tree PATH]
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from schema import frame, methods, results, cases as case_schema  # noqa: E402
from schema.engine import ValidationError  # noqa: E402


def check_exchange(exchange, path):
    errors = []
    request = exchange["request"]
    response = exchange.get("response")
    try:
        frame.decode_frame(json.dumps(request))
        methods.validate_params(request["method"], request["params"])
    except (frame.FrameError, ValidationError) as exc:
        errors.append(f"request: {exc}")
    if response is None:
        return errors
    try:
        decoded = frame.decode_response(json.dumps(response))
        if decoded.get("id") != request.get("id"):
            errors.append(f"response id {decoded.get('id')!r} != request id {request.get('id')!r}")
        results.validate_result(request["method"], response["result"])
    except (frame.FrameError, ValidationError) as exc:
        errors.append(f"response: {exc}")
    return errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tree", default=os.path.dirname(os.path.abspath(__file__)))
    args = parser.parse_args()

    golden_dir = os.path.join(args.tree, "golden")
    case_dir = os.path.join(args.tree, "cases")

    failures = []
    checked_golden = 0
    for name in sorted(os.listdir(golden_dir)):
        if not name.endswith(".json"):
            continue
        path = os.path.join(golden_dir, name)
        with open(path) as f:
            golden = json.load(f)
        if golden.get("schema") != "iprange-golden-v1":
            failures.append(f"{path}: schema {golden.get('schema')!r} != iprange-golden-v1")
            continue
        for exchange in golden.get("exchanges", []):
            checked_golden += 1
            errors = check_exchange(exchange, path)
            method = exchange["request"].get("method")
            for error in errors:
                failures.append(f"{path} {method}: {error}")

    checked_cases = 0
    for name in sorted(os.listdir(case_dir)):
        if not name.endswith(".json"):
            continue
        path = os.path.join(case_dir, name)
        with open(path) as f:
            case = json.load(f)
        try:
            case_schema.validate_case(case)
            checked_cases += 1
        except ValidationError as exc:
            failures.append(f"{path}: {exc}")

    print(f"golden exchanges checked: {checked_golden}")
    print(f"case files checked:       {checked_cases}")
    if failures:
        for failure in failures:
            print(f"FAIL {failure}")
        print(f"\ncheck_golden FAILED: {len(failures)} problem(s)")
        return 1
    print("\ncheck_golden PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
