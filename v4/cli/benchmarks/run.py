#!/usr/bin/env python3
"""Correctness runner for milestone-5 SDK scenarios.

One scenario file drives both release binaries. The runner compares the
fields the scenario names. A difference is a failure. Performance mode
is not implemented: this runner proves agreement, not speed.
"""

import argparse
import json
import os
import shutil
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from run import JsonRpcService  # noqa: E402

SCHEMA = "iprange-bench-scenario-v1"


def fail(message):
    print(f"FAIL {message}", file=sys.stderr)
    return 1


def load_scenario(path):
    with open(path, "r", encoding="utf-8") as stream:
        scenario = json.load(stream)
    if scenario.get("schema") != SCHEMA:
        raise ValueError(f"{path}: schema is not {SCHEMA}")
    if not scenario.get("name") or not scenario.get("calls"):
        raise ValueError(f"{path}: name and calls are required")
    return scenario


def substitute(value, work):
    if isinstance(value, str):
        return value.replace("$WORK", work)
    if isinstance(value, list):
        return [substitute(item, work) for item in value]
    if isinstance(value, dict):
        return {key: substitute(item, work) for key, item in value.items()}
    return value


def field(value, path):
    current = value
    for part in path.split("."):
        if not isinstance(current, dict) or part not in current:
            raise KeyError(path)
        current = current[part]
    return current


def write_fixtures(scenario, work):
    for fixture in scenario.get("fixtures", []):
        relative = fixture["path"]
        if os.path.isabs(relative) or ".." in relative.split("/"):
            raise ValueError(f"fixture path escapes the work directory: {relative}")
        path = os.path.join(work, relative)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        text = fixture["text"]
        if not text.endswith("\n"):
            text += "\n"
        with open(path, "w", encoding="utf-8") as stream:
            stream.write(text)


def run_engine(binary, name, scenario, work):
    # Each engine gets its own directory. Sharing one directory makes the
    # second create fail because the first engine already wrote the file.
    engine_work = os.path.join(work, name)
    os.makedirs(engine_work, exist_ok=False)
    write_fixtures(scenario, engine_work)
    service = JsonRpcService([binary, "--jsonrpc"], name, cwd=engine_work)
    observed = []
    try:
        for index, call in enumerate(scenario["calls"], start=1):
            result = service.call(
                f"{scenario['name']}-{index}",
                call["method"],
                substitute(call["params"], engine_work),
            )
            if "error" in result:
                raise AssertionError(f"{name} {call['method']}: {result['error']}")
            observed.append(result["result"])
        return observed
    finally:
        service.close()


def compare(scenario, rust, go):
    mismatches = []
    for index, call in enumerate(scenario["calls"]):
        for path in call["compare"]:
            left = field(rust[index], path)
            right = field(go[index], path)
            if left != right:
                mismatches.append(f"{call['method']} {path}: rust={left!r} go={right!r}")
    return mismatches


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--scenario", required=True)
    parser.add_argument("--work-dir")
    args = parser.parse_args()
    for label, path in (("rust", args.rust), ("go", args.go)):
        if not os.path.isabs(path) or not os.access(path, os.X_OK):
            return fail(f"{label} is not an absolute executable: {path}")
    scenario = load_scenario(args.scenario)
    own_work = args.work_dir is None
    work = args.work_dir or tempfile.mkdtemp(prefix="iprange-bench-")
    os.makedirs(work, exist_ok=True)
    try:
        rust = run_engine(args.rust, "rust", scenario, work)
        go = run_engine(args.go, "go", scenario, work)
        mismatches = compare(scenario, rust, go)
    finally:
        if own_work:
            shutil.rmtree(work, ignore_errors=True)
    if mismatches:
        return fail(scenario["name"] + "\n" + "\n".join(mismatches))
    print(f"PASS {scenario['name']}")
    for index, call in enumerate(scenario["calls"]):
        shown = {path: field(rust[index], path) for path in call["compare"]}
        print(f"  {call['method']} {shown}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
