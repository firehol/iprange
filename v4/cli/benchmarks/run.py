#!/usr/bin/env python3
"""Correctness runner for milestone-5 SDK scenarios.

One scenario file drives both release binaries. The runner compares the
fields the scenario names. A difference is a failure. Performance mode
is not implemented: this runner proves agreement, not speed.
"""

import argparse
import base64
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
    scenario["__path"] = path
    if scenario.get("schema") != SCHEMA:
        raise ValueError(f"{path}: schema is not {SCHEMA}")
    if not scenario.get("name") or not (scenario.get("calls") or scenario.get("write")):
        raise ValueError(f"{path}: name and calls or write are required")
    return scenario


def substitute(value, work, token="$WORK"):
    if isinstance(value, str):
        return value.replace(token, work)
    if isinstance(value, list):
        return [substitute(item, work, token) for item in value]
    if isinstance(value, dict):
        return {key: substitute(item, work, token) for key, item in value.items()}
    return value


def field(value, path):
    current = value
    for part in path.split("."):
        if part.isdigit():
            index = int(part)
            if not isinstance(current, list) or index >= len(current):
                raise KeyError(path)
            current = current[index]
            continue
        if not isinstance(current, dict) or part not in current:
            raise KeyError(path)
        current = current[part]
    return current


def write_fixtures(scenario, work):
    for fixture in scenario.get("fixtures", []):
        output = fixture["path"]
        if os.path.isabs(output) or ".." in output.split("/"):
            raise ValueError(f"fixture path escapes the work directory: {output}")
        path = os.path.join(work, output)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        if "base64" in fixture or "base64_file" in fixture:
            encoded = fixture.get("base64")
            if encoded is None:
                relative = fixture["base64_file"]
                root = os.path.realpath(os.path.dirname(os.path.dirname(os.path.dirname(scenario["__path"]))))
                encoded_path = os.path.realpath(os.path.join(os.path.dirname(scenario["__path"]), relative))
                if os.path.isabs(relative) or not encoded_path.startswith(root + os.sep):
                    raise ValueError(f"fixture encoding escapes the benchmark directory: {relative}")
                with open(encoded_path, "r", encoding="utf-8") as stream:
                    encoded = stream.read()
            with open(path, "wb") as stream:
                stream.write(base64.b64decode("".join(encoded.split()), validate=True))
            continue
        text = fixture["text"]
        if not text.endswith("\n"):
            text += "\n"
        with open(path, "w", encoding="utf-8") as stream:
            stream.write(text)


def published_files(scenario, work):
    names = []
    for relative in scenario.get("publish", []):
        if os.path.isabs(relative) or ".." in relative.split("/"):
            raise ValueError(f"publish path escapes the work directory: {relative}")
        names.append(relative)
        if not os.path.isfile(os.path.join(work, relative)):
            raise AssertionError(f"published file was not written: {relative}")
    return names


def stage_published(source_work, dest_work, names, incoming):
    # The reader opens files under incoming/. Those files were written by
    # the other engine, so this copy is the cross-open.
    for relative in names:
        source = os.path.join(source_work, relative)
        dest = os.path.join(dest_work, incoming, relative)
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        shutil.copy2(source, dest)


def run_calls(service, name, scenario, work, calls, peer):
    observed = []
    captured = {}
    for index, call in enumerate(calls, start=1):
        params = substitute(call["params"], work)
        if peer is not None:
            params = substitute(params, peer, token="$PEER")
        params = substitute_capture(params, captured)
        result = service.call(
            f"{scenario['name']}-{name}-{index}",
            call["method"],
            params,
        )
        if "error" in result:
            raise AssertionError(f"{name} {call['method']}: {result['error']}")
        for item in call.get("capture", []):
            captured[item["name"]] = field(result["result"], item["path"].replace("/", "."))
        observed.append(result["result"])
    return observed


def substitute_capture(value, captured):
    if isinstance(value, str) and value.startswith("$CAPTURE/"):
        name = value[len("$CAPTURE/"):]
        if name not in captured:
            raise KeyError(f"capture {name} was not produced by an earlier call")
        return captured[name]
    if isinstance(value, list):
        return [substitute_capture(item, captured) for item in value]
    if isinstance(value, dict):
        return {key: substitute_capture(item, captured) for key, item in value.items()}
    return value


def run_engine(binary, name, scenario, work, calls, peer=None, engine_work=None):
    # Each engine gets its own directory. Sharing one directory makes the
    # second create fail because the first engine already wrote the file.
    # A cross-open reader passes the writer directory so it can see the
    # copied snapshot.
    if engine_work is None:
        engine_work = os.path.join(work, name)
    os.makedirs(engine_work, exist_ok=True)
    write_fixtures(scenario, engine_work)
    service = JsonRpcService([binary, "--jsonrpc"], name, cwd=engine_work)
    try:
        return run_calls(service, name, scenario, engine_work, calls, peer)
    finally:
        service.close()


def scenario_calls(scenario):
    calls = list(scenario.get("write", scenario.get("calls", [])))
    calls.extend(scenario.get("read", []))
    return calls


def compare(scenario, rust, go):
    mismatches = []
    for index, call in enumerate(scenario_calls(scenario)):
        for path in call["compare"]:
            left = field(rust[index], path)
            right = field(go[index], path)
            if left != right:
                mismatches.append(f"{call['method']} {path}: rust={left!r} go={right!r}")
            expected = call.get("expect", {}).get(path)
            if expected is not None and left != expected:
                mismatches.append(f"{call['method']} {path}: got={left!r} expect={expected!r}")
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
        rust_work = os.path.join(work, "rust")
        go_work = os.path.join(work, "go")
        write_calls = scenario.get("write", scenario.get("calls", []))
        read_calls = scenario.get("read", [])
        rust = run_engine(args.rust, "rust", scenario, work, write_calls)
        go = run_engine(args.go, "go", scenario, work, write_calls)
        if read_calls:
            names = published_files(scenario, rust_work)
            published_files(scenario, go_work)
            stage_published(rust_work, go_work, names, "from-rust")
            stage_published(go_work, rust_work, names, "from-go")
            rust.extend(run_engine(args.go, "go-reads-rust", scenario, work, read_calls, peer="from-rust", engine_work=go_work))
            go.extend(run_engine(args.rust, "rust-reads-go", scenario, work, read_calls, peer="from-go", engine_work=rust_work))
        mismatches = compare(scenario, rust, go)
    finally:
        if own_work:
            shutil.rmtree(work, ignore_errors=True)
    if mismatches:
        return fail(scenario["name"] + "\n" + "\n".join(mismatches))
    print(f"PASS {scenario['name']}")
    for index, call in enumerate(scenario_calls(scenario)):
        shown = {path: field(rust[index], path) for path in call["compare"]}
        print(f"  {call['method']} {shown}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
