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
import subprocess
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
    if not scenario.get("name") or not (
            scenario.get("calls") or scenario.get("write") or scenario.get("cli")):
        raise ValueError(f"{path}: name and calls, write, or cli steps are required")
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
        if fixture.get("directory"):
            # An empty directory fixture (a @directory error arm needs
            # a directory with no expandable files in it).
            relative = fixture["path"]
            refuse_escape(relative, "fixture directory")
            os.makedirs(os.path.join(work, relative), exist_ok=True)
            continue
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


def run_cli(binary, name, call, work, peak=None):
    """Run one legacy CLI step: the engine as a point-in-time process.

    The step's args substitute $WORK like every other path; optional
    stdin_file feeds the process; redirect_stdout writes the captured
    stdout to a file (a binary artifact to hash across engines). The
    observation is {exit, stdout} and rides the same compare/expect
    machinery the JSON-RPC calls use. The CLI child is the workload:
    its VmHWM is sampled into the round's accumulator (the rpc
    sampler watches the service process, not this subprocess).
    """
    spec = call["cli"]
    argv = [binary] + [substitute(argument, work) for argument in spec["args"]]
    stdin_path = spec.get("stdin_file")
    stdin_stream = open(os.path.join(work, stdin_path), "rb") if stdin_path else subprocess.DEVNULL
    try:
        proc = subprocess.Popen(
            argv, cwd=work, stdin=stdin_stream,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        # Drain while sampling: a CLI step can write more than the pipe
        # buffer to stdout (the realistic large-output workload), and a
        # sampler that only polls would deadlock the child on a full
        # pipe. The whole wait is bounded (deadline); reads are
        # non-blocking; the sampler folds VmHWM into the round
        # accumulator whenever it observes the child.
        import selectors as _selectors
        deadline = time.monotonic() + 300
        chunks = []
        err_chunks = []
        os.set_blocking(proc.stdout.fileno(), False)
        os.set_blocking(proc.stderr.fileno(), False)
        selector = _selectors.DefaultSelector()
        selector.register(proc.stdout.fileno(), _selectors.EVENT_READ)
        selector.register(proc.stderr.fileno(), _selectors.EVENT_READ)
        try:
            while True:
                if time.monotonic() > deadline:
                    proc.kill()
                    proc.wait(timeout=5)
                    raise AssertionError(
                        "cli step did not finish within 300 s")
                # Sample before draining: a cli step can exit within the
                # first millisecond, and a sampler that slept first would
                # never read its VmHWM (VmHWM exists from process
                # creation).
                if peak is not None:
                    current = child_hwm_kib(proc.pid)
                    if current is not None:
                        peak["observed"] = True
                        if current > peak["kib"]:
                            peak["kib"] = current
                for key, _mask in selector.select(0.001):
                    sink = chunks if key.fd == proc.stdout.fileno() else err_chunks
                    try:
                        chunk = os.read(key.fd, 1 << 20)
                    except BlockingIOError:
                        chunk = b""
                    if chunk:
                        sink.append(chunk)
                # NOTE on peak RSS: VmHWM is sampled at ~1 ms granularity
                # while the child lives (the loop-top sample); wait4's
                # ru_maxrss is NOT usable here — a forked child inherits
                # the parent's address space pre-exec, so its ru_maxrss
                # floor is the parent python's RSS (~14-25 MiB), not the
                # exec'd engine's peak. Sub-10ms CLI steps may therefore
                # report an early-instant VmHWM (a lower bound); the
                # acceptance ceiling (seconds-long children) is
                # unaffected — recorded as a measurement limitation.
                if proc.poll() is not None:
                    # Drain any residue after the child's exit, then stop —
                    # bounded by the same deadline (a leaked writer holding
                    # the pipe's other end cannot pin this loop).
                    while time.monotonic() <= deadline:
                        moved = False
                        for fd, sink in ((proc.stdout.fileno(), chunks),
                                         (proc.stderr.fileno(), err_chunks)):
                            try:
                                chunk = os.read(fd, 1 << 20)
                            except BlockingIOError:
                                chunk = b""
                            if chunk:
                                sink.append(chunk)
                                moved = True
                        if not moved:
                            break
                    break
        finally:
            selector.close()
        stdout = b"".join(chunks)
        stderr = b"".join(err_chunks)
        proc.stderr.close()
        proc.stdout.close()
        returncode = proc.returncode
    finally:
        if stdin_stream is not subprocess.DEVNULL:
            stdin_stream.close()
    redirect = spec.get("redirect_stdout")
    if redirect:
        refuse_escape(redirect, "cli stdout path")
        with open(os.path.join(work, redirect), "wb") as stream:
            stream.write(stdout)
    if returncode != 0 and stderr:
        # A failed CLI run's stderr names the reason; keep it in the
        # observation so the failure output carries it.
        sys.stderr.write(f"{name} cli stderr: {stderr.decode('utf-8', 'replace')[:400]}\n")
    return {
        "exit": returncode,
        "stdout": stdout.decode("utf-8", "replace"),
    }


def run_calls(service, name, scenario, work, calls, peer, deferred=None, binary=None, peak=None):
    observed = []
    captured = {}
    for index, call in enumerate(calls, start=1):
        if "cli" in call:
            # A legacy CLI step: a point-in-time invocation of the same
            # engine binary. The file oracles below apply to it like to
            # any call (a redirected stdout is hashed across engines);
            # the rpc-only frame checks do not.
            if binary is None:
                raise AssertionError(
                    "a cli step needs the engine binary (harness defect)")
            cli_row = run_cli(binary, name, call, work, peak=peak)
            file_oracles(call, name, work, deferred, cli_row)
            observed.append(cli_row)
            continue
        if "batch" in call:
            result = call_batch(service, call["batch"], work)
        else:
            params = substitute(call["params"], work)
            if peer is not None:
                params = substitute(params, peer, token="$PEER")
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
            if call.get("expect_same_bytes"):
                # An error answer produces no artifact to hash; a call
                # declaring both would compare None == None across
                # engines — refuse the combination instead of passing
                # vacuously (r95: the None==None digest hole).
                raise AssertionError(
                    f"{name} {call.get('method', 'batch')}: expect_rpc_error "
                    f"cannot combine with expect_same_bytes (no artifact "
                    f"is produced to hash)")
            observed.append({"error": {"code": error.get("code")}})
            continue
        row = rpc_result_row(call, name, result, captured, work, deferred)
        file_oracles(call, name, work, deferred, row)
        observed.append(row)
    return observed


def rpc_result_row(call, name, result, captured, work, deferred):
    """Validate one rpc result frame into its comparison row."""
    del work, deferred
    if "expect_error" in call:
        if "error" not in result:
            raise AssertionError(f"{name} {call['method']}: expected error, got result")
        data = result["error"].get("data", {})
        for key, expected in call["expect_error"].items():
            if data.get(key) != expected:
                raise AssertionError(
                    f"{name} {call['method']}: error {key}={data.get(key)!r}, want {expected!r}"
                )
        return {"error": data}
    if "error" in result:
        raise AssertionError(f"{name} {call['method']}: {result['error']}")
    if "batch" in call:
        return result
    for item in call.get("capture", []):
        captured[item["name"]] = field(result["result"], item["path"].replace("/", "."))
    return dict(result["result"])


def file_oracles(call, name, work, deferred, row):
    """Apply the scenario's file oracles to one call's row.

    Exact row oracle (multiset), absence oracle, and the cross-engine
    artifact digest. Performance mode defers the reads and hashes out
    of its timed window via `deferred`; correctness mode runs them
    inline. Applies to rpc calls and cli steps alike.
    """
    if call.get("expect_file_csv_rows"):
        spec = call["expect_file_csv_rows"]
        relative = spec["path"]

        def csv_check(spec=spec, relative=relative, name=name, work=work):
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

        if deferred is None:
            csv_check()
        else:
            deferred.append(csv_check)
    if call.get("expect_no_file"):
        relative = call["expect_no_file"]
        if os.path.exists(os.path.join(work, relative)):
            raise AssertionError(f"{name} left {relative} after a failed publish")
    if call.get("expect_same_bytes"):
        relative = call["expect_same_bytes"]

        def bytes_check(row=row, relative=relative, work=work):
            with open(os.path.join(work, relative), "rb") as stream:
                row["bytes"] = hashlib.sha256(stream.read()).hexdigest()

        if deferred is None:
            bytes_check()
        else:
            deferred.append(bytes_check)


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


def exit_gate(name, status):
    """An engine that exits nonzero fails its own run (the one gate).

    Called both after a successful session and on a failure path whose
    peer died nonzero (the sharper, engine-attributed message); the
    mutation check deletes exactly this raise.
    """
    if status not in (None, 0):
        raise AssertionError(f"{name} exited {status}")


def assert_sampler_dead(sampler):
    """Runtime postcondition for the join contract (r161 panel): the
    sampler thread must be dead when the join returns. A forged or
    expiring timed join returns with the thread still folding and
    fails here; a join that waited long enough delivers the correct
    semantics. This closes the indirection class at runtime — the
    source pin is a regression detector, not the boundary."""
    if sampler.is_alive():
        raise AssertionError(
            "sampler thread still alive after join: the join must "
            "outlive the sampler (a timed or forged join races the "
            "sample reads)")


def prepare_engine_dirs(*engine_works, may_wipe=frozenset()):
    """Start engine directories clean without ever deleting foreign
    content. Deletion authority is in-memory provenance only: a
    directory this process created for an earlier attempt (the
    spawn-race retry's restart) may be wiped; any other pre-existing
    path is refused loudly and left untouched — `rust` and `go`
    collide with real directory names, so the runner never deletes
    what it did not create in this process (the r145 panel named the
    on-disk marker seam's forged, stale, and symlinked defeats and
    offered hardened markers or refusal as remedies; this design
    closes the class by removing the mechanism — a lead choice
    strictly stronger than either). All paths are verified before any is
    touched, so a refusal has no side effects. The wipe is
    unconditional: a swallowed failure would resurrect the
    dirty-directory defect the wipe exists to close."""
    for engine_work in engine_works:
        if os.path.islink(engine_work):
            raise AssertionError(
                f"refusing to touch {engine_work}: it is a symlink")
        if os.path.exists(engine_work) and engine_work not in may_wipe:
            raise AssertionError(
                f"refusing to touch {engine_work}: it already exists "
                f"and this run did not create it; remove it yourself "
                f"if it is disposable scratch")
    for engine_work in engine_works:
        if os.path.exists(engine_work):
            shutil.rmtree(engine_work)
        if os.path.exists(engine_work):
            # Postcondition: the wipe contract is self-enforcing
            # against swallows INSIDE this function (ignore_errors,
            # a quiet handler) — those leave the path behind and
            # fail here loudly instead of resurrecting the
            # dirty-directory defect. A caller wrapping the call
            # site in its own blanket except can still swallow the
            # failure; that is outside this function's control and
            # is the named residual (operations-r155 F2).
            raise AssertionError(
                f"wipe did not remove {engine_work}: refusing to "
                f"continue with state this run did not create clean")
        os.makedirs(engine_work, exist_ok=True)


def run_engine(binary, name, scenario, work, calls, peer=None, engine_work=None, peak=None,
               prepare=True, deferred=None):
    # Each engine gets its own directory. Sharing one directory makes the
    # second create fail because the first engine already wrote the file.
    # A cross-open reader passes the writer directory so it can see the
    # copied snapshot.
    if engine_work is None:
        engine_work = os.path.join(work, name)
    os.makedirs(engine_work, exist_ok=True)
    if prepare:
        # Performance mode prepares the directory before its timed
        # window; `prepare=False` requires an already-prepared dir.
        write_fixtures(scenario, engine_work)
        write_generated(scenario, engine_work)
    cli_only = all("cli" in call for call in calls) if calls else False
    service = None
    if not cli_only:
        # A cli-only scenario never speaks JSON-RPC: spawning an idle
        # --jsonrpc service would ride the timed window and pollute the
        # round's RSS with a process that does no work (r95).
        service = JsonRpcService(
            [binary, "--jsonrpc"], name, cwd=engine_work,
            read_deadline=120, write_deadline=30)
    # The call path is deadline-bounded like the frame reads: a peer
    # that never answers fails at 120 s instead of hanging the proof
    # (SilentPeerTest pins the mechanism).
    sampler = None
    if peak is not None and service is not None:
        # Performance mode samples the service child's VmHWM while it
        # runs; the sampler stops when the child exits. A cli-only run
        # has no service — its workload child is sampled inside run_cli.
        def sample_peak():
            while service.proc.poll() is None:
                current = child_hwm_kib(service.proc.pid)
                if current is not None:
                    peak["observed"] = True
                    if current > peak["kib"]:
                        peak["kib"] = current
                time.sleep(0.001)
        sampler = threading.Thread(target=sample_peak, daemon=True)
        sampler.start()
    try:
        result = run_calls(service, name, scenario, engine_work, calls, peer,
                           deferred=deferred, binary=binary, peak=peak)
    except BaseException as exc:
        # The exchange or an oracle already failed: close must not
        # mask it with its own strict-session verdict (a peer that
        # died mid-call is broken by that failure). The cleanup close
        # is best-effort: a teardown fault (e.g. I/O on a pipe the
        # dead peer already closed) must not replace the original
        # failure either.
        if service is not None:
            try:
                service.close(broken_exchange=True)
            except Exception:
                pass
        # When the peer is also dead with a nonzero status, the exit
        # gate's message is the sharper, engine-attributed failure
        # (the die-on-first-request control's deterministic
        # attribution); the original failure rides along as the
        # exception context. A hung or cleanly-exited peer keeps the
        # original failure (deadline, oracle) as the message.
        exit_status = service.proc.poll() if service is not None else None
        if exit_status not in (None, 0):
            try:
                exit_gate(name, exit_status)
            except AssertionError as gate:
                raise gate from exc
        raise
    if service is not None:
        service.close()
    # An engine that exits nonzero fails its own run. close() waits
    # for the process, so the returncode is settled here. close()
    # itself validates the exit status and residue of every ordinary
    # close — including a peer that already exited before the close
    # began (answered-then-died is a clean-session violation wherever
    # its death lands, and test_detectors.py pins the union: either
    # this gate's or close()'s message fails the run). Intentional
    # crash/stall sessions use close_forced and are the only exempt
    # class.
    exit_gate(name, service.proc.returncode if service is not None else 0)
    if sampler is not None:
        # The round's peak flags are read after this call returns:
        # join the sampler so no late fold can race those reads. The
        # join is unconditional — the loop provably exits when the
        # child is reaped. The runtime postcondition closes the
        # whole indirection class (r161 panel): any join spelling
        # that returns while the sampler still runs — a forged or
        # expiring timed join — fails here loudly instead of racing
        # the reads.
        sampler.join()
        assert_sampler_dead(sampler)
    return result


def scenario_calls(scenario):
    # The order matches execution order: write, then cli (both run in
    # the write phase; a cli step is a point-in-time invocation of the
    # same binary), then read. compare() consumes observations
    # index-wise, so a spec order that diverged from execution order
    # would mispair them (r93: latent for any scenario mixing
    # cross-open reads with cli steps).
    calls = list(scenario.get("write", scenario.get("calls", [])))
    calls.extend(scenario.get("cli", []))
    calls.extend(scenario.get("read", []))
    return calls


def compare(scenario, rust, go):
    mismatches = []
    for index, call in enumerate(scenario_calls(scenario)):
        for path in call["compare"]:
            left = field(rust[index], path)
            right = field(go[index], path)
            if left != right:
                label = call.get("method", "cli" if "cli" in call else "batch")
                mismatches.append(f"{label} {path}: rust={left!r} go={right!r}")
            expected = call.get("expect", {}).get(path)
            if expected is not None and left != expected:
                label = call.get("method", "cli" if "cli" in call else "batch")
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
    staged cross-open reads when the scenario has them — and every
    correctness check runs: protocol checks inside the timed window,
    file oracles and the cross-engine comparison after it. Timing
    boundaries: input preparation (fixtures, generated files,
    cross-open staging) is un-timed; the timed window carries the
    protocol exchanges and the engine work they drive. Per engine and
    round: wall time, the child's peak RSS (VmHWM sampled while
    alive, one accumulator per round), then actual min/median/max
    over the rounds, and the Go/Rust ratio as the honest ≤1.3x input.

    RSS-sample spawn race: a child that completes before the
    sampler's first read leaves the round's peak mechanically
    unrecoverable (a zombie carries no VmHWM). A round whose sample
    is missing from a child the sampler never observed is
    re-measured exactly once, the failed attempt's data discarded
    and the retry named on stderr. A missing sample from a child
    that WAS observed is a real defect and refuses at once; two
    consecutive misses refuse as before.
    """
    write_calls = scenario.get("write", scenario.get("calls", []))
    # Legacy CLI steps execute inside the timed write phase: they are
    # point-in-time subprocess runs of the same engine binary.
    write_calls = write_calls + scenario.get("cli", [])
    read_calls = scenario.get("read", [])
    elapsed = {"rust": [], "go": []}
    peaks = {"rust": [], "go": []}

    def run_round(round_index):
        """One engine-order-alternated round: un-timed input
        preparation, the timed legs, the deferred oracles, and the
        cross-engine comparison. All state is round-local so a
        spawn-race retry can discard the whole attempt."""
        rust_work = os.path.join(work, f"perf-rust-{round_index}")
        go_work = os.path.join(work, f"perf-go-{round_index}")
        # Un-timed input preparation: one directory per engine per
        # round, so the timed window never carries fixture or
        # generator work. Every attempt starts from clean
        # directories: a retried round must not see the discarded
        # attempt's engine artifacts (a created database or published
        # name would make the retry fail with a product-looking
        # error). Only directories this process created may be wiped
        # (the retry's own earlier attempt).
        prepare_engine_dirs(rust_work, go_work, may_wipe=created_dirs)
        created_dirs.update((rust_work, go_work))
        for engine_work in (rust_work, go_work):
            write_fixtures(scenario, engine_work)
            write_generated(scenario, engine_work)
        round_peak = {"rust": {"kib": 0, "observed": False},
                      "go": {"kib": 0, "observed": False}}
        round_elapsed = {"rust": [], "go": []}
        deferred = []

        def timed(label, binary, name, calls, engine_work, peer=None, extend=False):
            started = time.perf_counter()
            observations = run_engine(
                binary, name, scenario, work, calls, peer=peer,
                engine_work=engine_work, peak=round_peak[label],
                prepare=False, deferred=deferred)
            span = time.perf_counter() - started
            # A cross-open read joins its engine's round: the round's
            # elapsed is that engine's write plus read time.
            if extend:
                round_elapsed[label][-1] += span
            else:
                round_elapsed[label].append(span)
            return observations

        # Alternate the engine order each round (run-order-correlated
        # drift — e.g. a first-run warm-up effect — must not bias every
        # timed pair the same direction; the ceiling alternates too).
        if round_index % 2 == 0:
            rust_obs = timed("rust", rust, "rust", write_calls, rust_work)
            go_obs = timed("go", go, "go", write_calls, go_work)
        else:
            go_obs = timed("go", go, "go", write_calls, go_work)
            rust_obs = timed("rust", rust, "rust", write_calls, rust_work)
        rust_reads = []
        go_reads = []
        if read_calls:
            names = published_files(scenario, rust_work)
            published_files(scenario, go_work)
            stage_published(rust_work, go_work, names, "from-rust")
            stage_published(go_work, rust_work, names, "from-go")
            # Cross-open reads, paired exactly like correctness mode:
            # the go engine reads what rust published (joining the go
            # round), and vice versa.
            # Alternate the read leg with the write leg's order.
            if round_index % 2 == 0:
                rust_reads = timed("go", go, "go-reads-rust", read_calls,
                                   go_work, peer="from-rust", extend=True)
                go_reads = timed("rust", rust, "rust-reads-go", read_calls,
                                 rust_work, peer="from-go", extend=True)
            else:
                go_reads = timed("rust", rust, "rust-reads-go", read_calls,
                                 rust_work, peer="from-go", extend=True)
                rust_reads = timed("go", go, "go-reads-rust", read_calls,
                                   go_work, peer="from-rust", extend=True)
        # Every clock is stopped: run the deferred file oracles, then
        # the same comparison correctness mode applies to the same
        # observation pairing — a wrong answer fails the perf run.
        for check in deferred:
            check()
        missing = [label for label in ("rust", "go")
                   if round_peak[label]["kib"] <= 0]
        if missing:
            return round_elapsed, round_peak, missing
        mismatches = compare(scenario, rust_obs + rust_reads, go_obs + go_reads)
        if mismatches:
            raise AssertionError(
                f"{scenario['name']} round {round_index}: "
                + "; ".join(mismatches))
        return round_elapsed, round_peak, []

    created_dirs = set()
    for round_index in range(rounds):
        for attempt in (0, 1):
            round_elapsed, round_peak, missing = run_round(round_index)
            if not missing:
                break
            unobserved = all(not round_peak[label].get("observed")
                             for label in missing)
            if attempt == 1 or not unobserved:
                raise AssertionError(
                    f"{missing[0]} round {round_index}: no RSS sample was "
                    f"observed for the round")
            print(
                f"note: {', '.join(missing)} round {round_index}: the RSS "
                f"sampler never observed the child (spawn race); the round "
                f"is re-measured once", file=sys.stderr)
        for label in ("rust", "go"):
            elapsed[label].extend(round_elapsed[label])
            peaks[label].append(round_peak[label]["kib"])
    report = {"scenario": scenario["name"], "rounds": rounds}
    for label in ("rust", "go"):
        times = elapsed[label]
        report[label] = {
            "rounds": rounds,
            "elapsed_seconds": {
                "median": median(times), "min": min(times), "max": max(times)},
            "child_max_rss_kib": {
                "median": median(peaks[label]),
                "min": min(peaks[label]),
                "max": max(peaks[label])},
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
    # A work dir names harness-cwd-relative $WORK paths while the
    # engine runs with cwd=work_dir: normalize once at the entry so a
    # relative --work-dir cannot split the two views.
    work = (os.path.abspath(args.work_dir) if args.work_dir
            else tempfile.mkdtemp(prefix="iprange-bench-"))
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
        # A reused work dir is refused, never cleaned (r145 panel):
        # a second run over the same --work-dir fails with this exact
        # message instead of the misattributed create/publish error,
        # and nothing the run did not create is ever deleted.
        prepare_engine_dirs(rust_work, go_work)
        write_calls = scenario.get("write", scenario.get("calls", []))
        write_calls = write_calls + scenario.get("cli", [])
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
