#!/usr/bin/env python3
"""Correctness runner for milestone-5 SDK scenarios.

One scenario file drives both release binaries. The runner compares the
fields the scenario names. A difference is a failure. Correctness mode
runs each scenario once; performance mode (--mode perf) times the same
declarative file per engine with the correctness checks intact.
"""

import argparse
import base64
import hashlib
import threading
from collections import Counter
import json
import os
import shutil
import sys
import tempfile
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from run import JsonRpcService  # noqa: E402

from measure import child_hwm_kib, median, ratio  # noqa: E402

SCHEMA = "iprange-bench-scenario-v1"


def fail(message):
    print(f"FAIL {message}", file=sys.stderr)
    return 1


def readline_bounded(pipe, limit=1_048_578, seconds=120.0):
    """Read one newline-terminated frame under a wall-clock bound.

    A peer that never answers, never completes a frame, or has closed
    the stream fails at the deadline instead of hanging the proof.
    On a non-blocking descriptor `readline` returns whatever is
    buffered — including a partial line without its newline — so the
    helper accumulates chunks until one ends the frame, the same
    accumulating pattern the main harness reader uses; co-read bytes
    are never stranded.
    """
    descriptor = pipe.fileno()
    was_blocking = os.get_blocking(descriptor)
    os.set_blocking(descriptor, False)
    deadline = time.monotonic() + seconds
    pending = b""
    try:
        while True:
            try:
                chunk = pipe.readline(limit)
            except (BlockingIOError, OSError):
                chunk = b""
            if chunk:
                if chunk.endswith(b"\n"):
                    return pending + chunk
                pending += chunk
            if time.monotonic() >= deadline:
                raise AssertionError(
                    f"frame did not arrive within {seconds:g}s")
            time.sleep(0.001)
    finally:
        os.set_blocking(descriptor, was_blocking)


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


def refuse_escape(relative, label):
    if not isinstance(relative, str) or not relative or os.path.isabs(relative):
        raise ValueError(f"{label} escapes the work directory: {relative}")
    parts = relative.replace("\\", "/").split("/")
    if ".." in parts or any(":" in part for part in parts):
        raise ValueError(f"{label} escapes the work directory: {relative}")


def write_generated(scenario, work):
    for item in scenario.get("generate", []):
        from generate import covered, generate, merged_count, write_ipv6, write_text
        ranges = generate(item["seed"], item["count"], item["span"], item.get("space", 2**32))
        relative = item["path"]
        refuse_escape(relative, "generated path")
        family = item.get("family", "ipv4")
        if family not in ("ipv4", "ipv6"):
            raise ValueError(f"generated family must be ipv4 or ipv6: {family}")
        path = os.path.join(work, relative)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as stream:
            (write_ipv6 if family == "ipv6" else write_text)(ranges, stream)
        expected = item.get("expect_merged_addresses")
        if expected is not None and merged_count(ranges) != expected:
            raise AssertionError(
                f"{relative}: generator merged {merged_count(ranges)} addresses, scenario expects {expected}"
            )
        expected_ranges = item.get("expect_merged_ranges")
        if expected_ranges is not None and len(covered(ranges)) != expected_ranges:
            raise AssertionError(
                f"{relative}: generator merged {len(covered(ranges))} ranges, scenario expects {expected_ranges}"
            )


def write_fixtures(scenario, work):
    for fixture in scenario.get("fixtures", []):
        output = fixture["path"]
        refuse_escape(output, "fixture path")
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
        if fixture.get("expand_work"):
            text = text.replace("$WORK", work)
        if not text.endswith("\n"):
            text += "\n"
        with open(path, "w", encoding="utf-8") as stream:
            stream.write(text)


def published_files(scenario, work):
    names = []
    for relative in scenario.get("publish", []):
        refuse_escape(relative, "publish path")
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
        if "batch" in call:
            result = call_batch(service, call["batch"], work)
        else:
            params = substitute(call["params"], work)
        if "batch" not in call and peer is not None:
            params = substitute(params, peer, token="$PEER")
        if "batch" not in call:
            params = substitute_capture(params, captured)
            result = service.call(
            f"{scenario['name']}-{name}-{index}",
            call["method"],
            params,
        )
        if "expect_rpc_error" in call:
            error = result.get("error", {})
            if error.get("code") != call["expect_rpc_error"]:
                raise AssertionError(
                    f"{name} {call.get('method', 'batch')}: rpc code={error.get('code')!r}, "
                    f"want {call['expect_rpc_error']!r}"
                )
            observed.append({"error": {"code": error.get("code")}})
            continue
        if call.get("expect_file_contains"):
            relative = call["expect_file_contains"]["path"]
            text = open(os.path.join(work, relative), encoding="utf-8").read()
            for needle in call["expect_file_contains"]["text"]:
                if needle not in text:
                    raise AssertionError(f"{name} {relative} missing {needle!r}")
        if call.get("expect_file_csv_rows"):
            # Exact row oracle: rows are compared as a multiset, so a
            # missing row, an extra row, a duplicated row, or a row
            # whose fields merely prefix another ("alpha,1,5" vs
            # "alpha,1,50") all fail — a substring check cannot see
            # any of these.
            spec = call["expect_file_csv_rows"]
            relative = spec["path"]
            path = os.path.join(work, relative)
            with open(path, encoding="utf-8") as stream:
                next(stream)
                rows = Counter(
                    line.rstrip("\n") for line in stream if line.strip())
            want = Counter(spec["rows"])
            if rows != want:
                missing = list((want - rows).elements())
                unexpected = list((rows - want).elements())
                raise AssertionError(
                    f"{name} {relative}: rows differ (missing {missing!r}, "
                    f"unexpected {unexpected!r})")
        if call.get("expect_no_file"):
            relative = call["expect_no_file"]
            if os.path.exists(os.path.join(work, relative)):
                raise AssertionError(f"{name} left {relative} after a failed publish")
        if "expect_error" in call:
            if "error" not in result:
                raise AssertionError(f"{name} {call['method']}: expected error, got result")
            data = result["error"].get("data", {})
            for key, expected in call["expect_error"].items():
                if data.get(key) != expected:
                    raise AssertionError(
                        f"{name} {call['method']}: error {key}={data.get(key)!r}, want {expected!r}"
                    )
            observed.append({"error": data})
            continue
        if "error" in result:
            raise AssertionError(f"{name} {call['method']}: {result['error']}")
        if "batch" in call:
            observed.append(result)
            continue
        for item in call.get("capture", []):
            captured[item["name"]] = field(result["result"], item["path"].replace("/", "."))
        row = dict(result["result"])
        if call.get("expect_same_bytes"):
            relative = call["expect_same_bytes"]
            row["bytes"] = hashlib.sha256(open(os.path.join(work, relative), "rb").read()).hexdigest()
        observed.append(row)
    return observed


def batch_observation(decoded, count):
    """A batch of 16 must be 16 echoed describe results.

    A rejection must be one error object with a null id. Sixteen error
    objects, or a rejection that echoes an id, is not this contract.
    """

    if isinstance(decoded, list):
        if len(decoded) != count:
            raise AssertionError(f"batch response has {len(decoded)} members, want {count}")
        ids = []
        for index, item in enumerate(decoded):
            if not isinstance(item, dict) or item.get("id") != f"batch-{index}":
                raise AssertionError(f"batch member {index} did not echo its id")
            result = item.get("result")
            if not isinstance(result, dict) or result.get("method") != "iprange.v1.system.describe":
                raise AssertionError(f"batch member {index} is not a describe result")
            ids.append(item["id"])
        return {"result": {"count": count, "ids": ids}}
    if not isinstance(decoded, dict) or "error" not in decoded or decoded.get("id") is not None:
        raise AssertionError("batch rejection must be one error object with id null")
    return decoded


def call_batch(service, count, work):
    members = []
    for index in range(count):
        members.append({
            "jsonrpc": "2.0",
            "id": f"batch-{index}",
            "method": "iprange.v1.system.describe",
            "params": {},
        })
    wire = json.dumps(members, separators=(",", ":")).encode("utf-8")
    # A batch is one frame. The exchange (bounded write, bounded read)
    # is the service's own — the deadline-bounded modes detach the
    # buffered wrappers, so the pipes must not be driven directly.
    line = service.exchange_raw(wire)
    return batch_observation(json.loads(line), count)


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


def run_engine(binary, name, scenario, work, calls, peer=None, engine_work=None, peak=None):
    # Each engine gets its own directory. Sharing one directory makes the
    # second create fail because the first engine already wrote the file.
    # A cross-open reader passes the writer directory so it can see the
    # copied snapshot.
    if engine_work is None:
        engine_work = os.path.join(work, name)
    os.makedirs(engine_work, exist_ok=True)
    write_fixtures(scenario, engine_work)
    write_generated(scenario, engine_work)
    service = JsonRpcService(
        [binary, "--jsonrpc"], name, cwd=engine_work,
        read_deadline=120, write_deadline=30)
    # The call path is deadline-bounded like the frame reads: a peer
    # that never answers fails at 120 s instead of hanging the proof
    # (SilentPeerTest pins the mechanism).
    if peak is not None:
        # Performance mode samples the child's VmHWM while it runs;
        # the sampler stops when the child exits.
        def sample_peak():
            while service.proc.poll() is None:
                current = child_hwm_kib(service.proc.pid)
                if current is not None and current > peak["kib"]:
                    peak["kib"] = current
                time.sleep(0.001)
        sampler = threading.Thread(target=sample_peak, daemon=True)
        sampler.start()
    try:
        result = run_calls(service, name, scenario, engine_work, calls, peer)
        return result
    finally:
        service.close()
        # An engine that exits nonzero fails its own run. close()
        # waits for the process, so the returncode is settled here.
        # close() itself also checks the exit status of a peer that was
        # alive at its entry; this gate covers the peer that was
        # already dead when close() started. For an answered-then-died
        # engine the two checks race, so the pinned contract is the
        # union — either check's message fails the run
        # (test_detectors.py accepts both). The gate's already-dead
        # attribution is deterministic in the die-on-first-request
        # control, where close() exempts a peer dead at its entry.
        if service.proc.returncode != 0:
            raise AssertionError(
                f"{name} exited {service.proc.returncode}")


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
                label = call.get("method", "batch")
                mismatches.append(f"{label} {path}: rust={left!r} go={right!r}")
            expected = call.get("expect", {}).get(path)
            if expected is not None and left != expected:
                label = call.get("method", "batch")
                mismatches.append(f"{label} {path}: got={left!r} expect={expected!r}")
        if call.get("expect_same_bytes"):
            left = rust[index].get("bytes")
            right = go[index].get("bytes")
            if left != right:
                mismatches.append(f"{call.get('method', 'batch')} bytes differ")
    return mismatches


def run_perf(scenario, rust, go, rounds, work):
    """The same declarative scenario, timed (performance mode).

    Each round executes the full scenario flow — writes, then the
    staged cross-open reads when the scenario has them — with every
    correctness check intact. Per engine: wall time per round, the
    child's peak RSS (VmHWM sampled while alive, max across rounds),
    throughput, and the Go/Rust ratio as the honest ≤1.3x input.
    """
    write_calls = scenario.get("write", scenario.get("calls", []))
    read_calls = scenario.get("read", [])
    elapsed = {"rust": [], "go": []}
    peaks = {"rust": {"kib": 0}, "go": {"kib": 0}}
    for round_index in range(rounds):
        rust_work = os.path.join(work, f"perf-rust-{round_index}")
        go_work = os.path.join(work, f"perf-go-{round_index}")
        for label, binary, engine_work in (
                ("rust", rust, rust_work),
                ("go", go, go_work)):
            started = time.perf_counter()
            run_engine(binary, label, scenario, work, write_calls,
                       engine_work=engine_work, peak=peaks[label])
            elapsed[label].append(time.perf_counter() - started)
        if read_calls:
            names = published_files(scenario, rust_work)
            published_files(scenario, go_work)
            stage_published(rust_work, go_work, names, "from-rust")
            stage_published(go_work, rust_work, names, "from-go")
            started = time.perf_counter()
            run_engine(go, "go-reads-rust", scenario, work, read_calls,
                       peer="from-rust", engine_work=go_work,
                       peak=peaks["go"])
            elapsed["go"][-1] += time.perf_counter() - started
            started = time.perf_counter()
            run_engine(rust, "rust-reads-go", scenario, work, read_calls,
                       peer="from-go", engine_work=rust_work,
                       peak=peaks["rust"])
            elapsed["rust"][-1] += time.perf_counter() - started
    report = {"scenario": scenario["name"], "rounds": rounds}
    for label in ("rust", "go"):
        times = elapsed[label]
        report[label] = {
            "rounds": rounds,
            "elapsed_seconds": {
                "median": median(times), "min": min(times), "max": max(times)},
            "child_max_rss_kib": {
                "median": peaks[label]["kib"],
                "min": peaks[label]["kib"],
                "max": peaks[label]["kib"]},
            "throughput_rounds_per_s": rounds / sum(times),
        }
    report["ratio"] = ratio(report["rust"], report["go"])
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--scenario", required=True)
    parser.add_argument("--work-dir")
    parser.add_argument("--mode", choices=("correctness", "perf"),
                        default="correctness",
                        help="correctness: one run, compared across engines. "
                             "perf: the same scenario file, timed per engine "
                             "with the correctness checks intact")
    parser.add_argument("--rounds", type=int, default=3,
                        help="performance-mode rounds per engine")
    args = parser.parse_args()
    for label, path in (("rust", args.rust), ("go", args.go)):
        if not os.path.isabs(path) or not os.access(path, os.X_OK):
            return fail(f"{label} is not an absolute executable: {path}")
    scenario = load_scenario(args.scenario)
    own_work = args.work_dir is None
    work = args.work_dir or tempfile.mkdtemp(prefix="iprange-bench-")
    os.makedirs(work, exist_ok=True)
    if args.mode == "perf":
        if args.rounds < 1:
            return fail("rounds must be positive")
        try:
            report = run_perf(scenario, args.rust, args.go, args.rounds, work)
        finally:
            if own_work:
                shutil.rmtree(work, ignore_errors=True)
        print(json.dumps(report, sort_keys=True))
        return 0
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
        print(f"  {call.get('method', 'batch')} {shown}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
