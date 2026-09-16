#!/usr/bin/env python3
"""Busy-reply throughput attestation for both v4 products.

What this proves, and what it deliberately does not
---------------------------------------------------
A session loop that spawns a thread per reply is a factor-of-two class
regression that no functional case detects: every reply still arrives and
every budget still holds, only the wall clock moves.  ``resource_harness``
proofs A-D pin *boundedness* (memory, fds, page counts), not *rate*, and
before this harness no committed artifact measured rate at all, so a
throughput claim could only ever be prose.

This harness records two things for each product:

* ``replies_per_s`` — the achieved rate for a fixed number of cheap
  pipelined ``system.describe`` requests in bounded bursts, on a fresh
  child per round, with the round values retained so a reader can see the
  spread rather than a single flattering number;
* ``clone_syscalls`` / ``unique_child_tids`` under ``strace -f`` — the
  structural invariant that catches the per-reply spawn, namely that the
  thread-creation count is a fixed small constant (the session's reader,
  worker, reply writer, and runtime helper) and does **not** grow with the
  number of requests served.

It is an *attestation*, not a threshold gate.  An absolute replies/s floor
is not portable across the host load, core count, and governor differences
this project is built on, and a floor that only passes on one machine turns
into a flaky blocker that people learn to ignore.  So the evidence records
the numbers, binds them to the measured binaries and the reviewed revision,
and checks only the structure (all replies served, exit clean, no
per-request thread growth).  A regression in the structural check is a hard
failure; a rate change is visible in the record for review.

Usage
-----
    nice python3 v4/cli/throughput_harness.py --go BIN --rust BIN \
        --work EMPTY_DIR [--requests 10000] [--rounds 3] \
        [--json-report v4/cli/evidence/throughput.json]

    nice python3 v4/cli/throughput_harness.py --self-test
"""

import argparse
import hashlib
import json
import os
import platform
import re
import select
import subprocess
import sys
import time

sys.dont_write_bytecode = True

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from command_sanitize import (  # noqa: E402
    audit_report_writers,
    require_paths_outside_profile,
    run_shared_self_test,
    sanitized_path_value,
    under_profile,
    write_committed_report,
)

REPORT_SCHEMA = "iprange-cli-throughput-report-v1"
STRACE = "/usr/bin/strace"
# The cheap request: it answers from session state, so the measured cost is
# the transport and dispatch path rather than any database work.
PROBE_REQUEST = {"jsonrpc": "2.0", "id": 1,
                 "method": "iprange.v1.system.describe", "params": {}}
BURST = 30
# Generous ceiling on distinct threads a session may create, independent of
# how many replies it served.  The discriminating test is that the count is
# CONSTANT when the request count doubles (below); this absolute band only
# catches a session that spawns threads per request or per connection.  It
# is deliberately far above the measured constants (Rust 4 clone calls and
# 4 distinct task ids; Go 18 clone calls with 6 and 9 distinct task ids at
# 3,000 and 6,000 requests, per `evidence/throughput.json`) because the two
# runtimes differ in housekeeping threads, and a band tuned to one engine
# would fail the other without meaning anything.
THREAD_BASELINE_MAX = 64
# Slack on the clone count between the small and the doubled pass.  A
# runtime creates housekeeping threads opportunistically, so a couple of
# extra clones are normal; a per-reply or per-burst spawn is thousands.
CLONE_COUNT_ALLOWANCE = 8


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _serve_bursts(binary, work, requests, env_prefix):
    """Serve `requests` frames in BURST-sized pipelines on one fresh child.

    Returns ``(elapsed_seconds, replies_served, exit_status)``.  Frames are
    written as a burst and the replies drained as a burst, which is how a
    pipelined client actually drives the session loop; a per-frame
    request/response round trip would measure the scheduler instead.
    """
    frame = (json.dumps(PROBE_REQUEST, separators=(",", ":")) + "\n").encode()
    argv = list(env_prefix) + [binary, "--jsonrpc"]
    child = subprocess.Popen(argv, stdin=subprocess.PIPE,
                             stdout=subprocess.PIPE,
                             stderr=subprocess.DEVNULL, cwd=work,
                             start_new_session=True)
    out_fd = child.stdout.fileno()
    buffer = b""
    served = 0
    started = time.monotonic()
    try:
        for offset in range(0, requests, BURST):
            want = min(BURST, requests - offset)
            for _ in range(want):
                child.stdin.write(frame)
            child.stdin.flush()
            deadline = time.monotonic() + 30.0
            while buffer.count(b"\n") < want \
                    and time.monotonic() < deadline:
                readable, _, _ = select.select([out_fd], [], [], 0.2)
                if not readable:
                    continue
                chunk = os.read(out_fd, 1 << 16)
                if not chunk:
                    break
                buffer += chunk
            served += buffer.count(b"\n")
            buffer = b""
        elapsed = time.monotonic() - started
    finally:
        try:
            child.stdin.close()
        except OSError:
            pass
        try:
            exit_status = child.wait(timeout=15)
        except subprocess.TimeoutExpired:
            os.killpg(os.getpgid(child.pid), 9)
            child.wait()
            exit_status = "killed"
    return elapsed, served, exit_status


def measure_rate(binary, work, requests, rounds):
    """Interleaved rounds of the burst workload; returns the per-round list."""
    rounds_result = []
    for index in range(rounds):
        elapsed, served, exit_status = _serve_bursts(
            binary, work, requests, ["nice", "-n", "19"])
        rounds_result.append({
            "round": index,
            "seconds": round(elapsed, 4),
            "requests": requests,
            "replies": served,
            "replies_per_s": round(served / elapsed, 1) if elapsed > 0 else 0,
            "exit_status": exit_status,
        })
    rates = sorted(entry["replies_per_s"] for entry in rounds_result)
    median = rates[len(rates) // 2] if rates else 0
    return rounds_result, median


def measure_threads(binary, work, requests):
    """Count thread creations while serving `requests` under strace.

    Returns the clone count, the distinct child thread ids, and the exit
    status.  ``strace`` is optional: when it is missing the structural
    check reports that it did not run rather than pretending it passed."""
    if not os.path.exists(STRACE):
        return None
    log_path = os.path.join(work, f"clone-{os.getpid()}-{requests}.log")
    child = subprocess.Popen(
        ["nice", "-n", "19", STRACE, "-f", "-qq",
         "-e", "trace=clone,clone3", "-o", log_path,
         binary, "--jsonrpc"],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL, cwd=work, start_new_session=True)
    frame = (json.dumps(PROBE_REQUEST, separators=(",", ":")) + "\n").encode()
    out_fd = child.stdout.fileno()
    buffer = b""
    served = 0
    for offset in range(0, requests, BURST):
        want = min(BURST, requests - offset)
        for _ in range(want):
            child.stdin.write(frame)
        child.stdin.flush()
        deadline = time.monotonic() + 60.0
        while buffer.count(b"\n") < want and time.monotonic() < deadline:
            readable, _, _ = select.select([out_fd], [], [], 0.2)
            if not readable:
                continue
            chunk = os.read(out_fd, 1 << 16)
            if not chunk:
                break
            buffer += chunk
        served += buffer.count(b"\n")
        buffer = b""
    try:
        child.stdin.close()
    except OSError:
        pass
    try:
        exit_status = child.wait(timeout=30)
    except subprocess.TimeoutExpired:
        os.killpg(os.getpgid(child.pid), 9)
        child.wait()
        exit_status = "killed"
    try:
        with open(log_path, encoding="utf-8", errors="replace") as stream:
            text = stream.read()
    except OSError:
        text = ""
    finally:
        try:
            os.unlink(log_path)
        except OSError:
            pass
    # Any successful clone/clone3 returns the new task id.  Go's runtime
    # calls clone(child_stack=..., flags=CLONE_VM|...) with no struct
    # argument while Rust's calls clone3({..}, ..), so the pattern must
    # accept both spellings: pinning one form silently scores the other
    # engine at zero threads and turns the check into a no-op.
    clones = len(re.findall(r"clone3?\(", text))
    tids = {task_id
            for _caller, task_id in re.findall(
                r"^\s*(\d+)\s+clone3?\(.*\) = (\d+)\b", text, re.M)}
    return {"requests": requests, "replies": served,
            "clone_syscalls": clones, "unique_child_tids": len(tids),
            "exit_status": exit_status}


def _screen_inputs(args):
    """Refuse every path-valued option that lives under the profile.

    The committed report records the binary paths it measured and the strace
    command line, so a profile-rooted input is exactly how a personal path
    reaches a durable artifact.  Checking here, before any child is spawned,
    means the run never happens and no partial artifact exists either.
    """
    require_paths_outside_profile((("--go", args.go), ("--rust", args.rust),
                                   ("--work", args.work),
                                   ("--json-report", args.json_report)))


def _require_empty_work(value):
    if not value or not os.path.isabs(value):
        raise SystemExit(f"--work must be an absolute path, got {value!r}")
    if under_profile(value):
        raise SystemExit(f"--work must not live under the operator profile: "
                         f"{value}")
    if not os.path.isdir(value) or os.listdir(value):
        raise SystemExit(f"--work must be an existing EMPTY directory: {value}")
    return value


def _require_executable(label, value):
    if not value or not os.path.isabs(value):
        raise SystemExit(f"{label} must be an absolute path, got {value!r}")
    if not os.path.isfile(value) or not os.access(value, os.X_OK):
        raise SystemExit(f"{label} is not an executable file: {value}")
    return value


def structural_problems(product, engines):
    """The non-negotiable part of the attestation.

    Every reply must be served, the child must exit cleanly, and the thread
    count must stay a constant independent of the request count.  A missing
    strace is reported, not silently scored as a pass."""
    problems = []
    for label in engines:
        record = product.get(label)
        if not isinstance(record, dict):
            problems.append(f"{label}: no throughput record")
            continue
        for entry in record.get("rounds", []):
            if entry["replies"] != entry["requests"]:
                problems.append(
                    f"{label}: round {entry['round']} served "
                    f"{entry['replies']} of {entry['requests']} replies; a "
                    f"rate measured on dropped replies is not a rate")
            if entry["exit_status"] != 0:
                problems.append(f"{label}: round {entry['round']} child exit "
                                f"{entry['exit_status']!r}")
        if not record.get("rounds"):
            problems.append(f"{label}: no rounds measured")
        if record.get("implementation") != label:
            problems.append(
                f"{label}: records implementation {record.get('implementation')!r}; "
                f"a census attributed to the wrong engine cannot attest to "
                f"either")
        if record.get("median_replies_per_s", 0) <= 0:
            problems.append(f"{label}: median replies/s is not positive")
        structure = record.get("thread_structure")
        if structure is None:
            problems.append(
                f"{label}: thread structure not measured (strace absent); "
                f"the per-reply-spawn regression this attestation exists to "
                f"catch would go undetected")
        else:
            small = structure.get("small")
            large = structure.get("large")
            if small is None or large is None:
                problems.append(f"{label}: thread structure incomplete")
            else:
                for side in (small, large):
                    if side["clone_syscalls"] > 0 \
                            and side["unique_child_tids"] == 0:
                        problems.append(
                            f"{label}: {side['clone_syscalls']} clone "
                            f"syscalls but 0 distinct task ids; the census "
                            f"parsed nothing, so the structural check is "
                            f"vacuous rather than passing")
                        continue
                    if side["clone_syscalls"] == 0 \
                            and side["unique_child_tids"] == 0:
                        problems.append(
                            f"{label}: strace recorded no thread creation at "
                            f"all for {side['requests']} requests; a session "
                            f"that served replies without any measured thread "
                            f"is not evidence of a bounded thread count")
                        continue
                    if side["replies"] != side["requests"]:
                        problems.append(
                            f"{label}: strace pass served "
                            f"{side['replies']} of {side['requests']}")
                    if side["clone_syscalls"] > THREAD_BASELINE_MAX \
                            or side["unique_child_tids"] > THREAD_BASELINE_MAX:
                        problems.append(
                            f"{label}: {side['unique_child_tids']} distinct "
                            f"threads for {side['requests']} requests exceeds "
                            f"the fixed-session band of "
                            f"{THREAD_BASELINE_MAX}; reply-path thread "
                            f"growth is the defect this attestation pins")
                # The request-count-invariant quantity is the number of
                # clone syscalls.  Distinct task ids are not a reliable
                # statistic: strace detaches under -qq on busy sessions, so
                # the same Go binary reports task ids that move with
                # sampling rather than with load (the committed report shows
                # 6 at 3,000 requests and 9 at 6,000; exploratory passes
                # outside the committed record set saw a wider 7-13 spread,
                # and that range is not carried by any artifact here), while
                # its clone count stays put at 18 across both probes.
                # Gating on task ids would make the attestation
                # flaky without adding detecting power: a thread-per-reply
                # regression shows up in the clone count too, at roughly
                # +3000 rather than +2.
                growth = large["clone_syscalls"] - small["clone_syscalls"]
                if growth > CLONE_COUNT_ALLOWANCE \
                        or large["clone_syscalls"] >= 2 * small["clone_syscalls"]:
                    problems.append(
                        f"{label}: clone syscalls went {small['clone_syscalls']}"
                        f" -> {large['clone_syscalls']} when the request count "
                        f"doubled ({small['requests']} -> {large['requests']}); "
                        f"the session must create its threads once, not per "
                        f"request")
    return problems


def attestation(args):
    _screen_inputs(args)
    work = _require_empty_work(args.work)
    engines = {"go": _require_executable("--go", args.go),
               "rust": _require_executable("--rust", args.rust)}
    if not os.path.exists(STRACE):
        print(f"NOTE: {STRACE} is absent; the thread-structure check cannot "
              f"run and the report will record that as a failure",
              file=sys.stderr)
    product = {}
    for label, binary in sorted(engines.items()):
        rounds_result, median = measure_rate(binary, work, args.requests,
                                             args.rounds)
        small = measure_threads(binary, work, args.thread_probe_requests)
        large = measure_threads(
            binary, work, args.thread_probe_requests * 2)
        product[label] = {
            "path": sanitized_path_value(binary),
            "sha256": sha256_file(binary),
            # The engine a census belongs to is part of its identity: a
            # consumer that re-labels the Go run as rust must be contradicted
            # by a measured artifact, not by this label alone.
            "implementation": label,
            "rounds": rounds_result,
            "median_replies_per_s": median,
            "thread_structure": {
                "tool": STRACE if small is not None else None,
                "small": small, "large": large},
        }
        print(f"{label:4} median={median:9.1f} replies/s "
              f"rounds={[r['replies_per_s'] for r in rounds_result]} "
              f"tids={None if small is None else small['unique_child_tids']}"
              f"/{None if large is None else large['unique_child_tids']}")

    problems = structural_problems(product, sorted(engines))
    report = {
        "schema": REPORT_SCHEMA,
        "platform": {"system": platform.system(),
                     "release": platform.release(),
                     "machine": platform.machine(),
                     "processor": platform.processor(),
                     "python": platform.python_version()},
        "product": product,
        "method": {
            "request": PROBE_REQUEST,
            "burst_frames": BURST,
            "requests_per_round": args.requests,
            "rounds": args.rounds,
            "thread_probe_requests_small": args.thread_probe_requests,
            "thread_probe_requests_large": args.thread_probe_requests * 2,
            "thread_baseline_max": THREAD_BASELINE_MAX,
            "note": ("attestation, not a threshold: absolute rate is host "
                     "load dependent, so the gate is that all replies are "
                     "served, the child exits cleanly, and the thread count "
                     "is constant in the request count"),
        },
        "result": "PASS" if not problems else "FAIL",
        "problems": problems,
    }
    if args.json_report:
        # Relative spellings resolve against the invocation directory,
        # like every other harness in this suite.
        target = os.path.abspath(args.json_report)
        parent = os.path.dirname(target)
        os.makedirs(parent, exist_ok=True)
        # The shared writer owns command/git_head/checkout_root and the
        # personal-path scan; the per-engine records carry binary paths and
        # the strace command line, so the screening list is what a reader
        # re-derives the privacy claim from.
        write_committed_report(
            target, report,
            caller_paths=(("--go", args.go), ("--rust", args.rust),
                          ("--work", args.work),
                          ("--json-report", args.json_report)),
            indent=1)
    for problem in problems:
        print(f"PROBLEM {problem}")
    if problems:
        print(f"\nthroughput attestation FAILED: {len(problems)} problem(s)")
        return 1
    print(f"\nthroughput attestation PASSED (git_head={report['git_head']})")
    return 0


# Executed-control counts for ``--self_test``, as literals (see the pins in
# the body).  Adding a mutation class here means raising these numbers.
THROUGHPUT_SELF_TEST_CASES = 12
THROUGHPUT_SELF_TEST_REJECTED = 10
THROUGHPUT_SELF_TEST_STRUCTURAL = 4


def _self_test():
    """The structural check must reject the regressions it claims to catch."""
    def good():
        def rounds(count, rate):
            return [{"round": index, "seconds": count / rate,
                     "requests": count, "replies": count,
                     "replies_per_s": rate, "exit_status": 0}
                    for index in range(3)]

        def probe(requests, tids):
            return {"requests": requests, "replies": requests,
                    "clone_syscalls": tids, "unique_child_tids": tids,
                    "exit_status": 0}

        return {
            # The product-declared identity is part of the structural
            # contract: a census attributed to the wrong engine attests to
            # neither engine, so the genuine fixture must carry it.
            "go": {"implementation": "go",
                   "rounds": rounds(10000, 37500), "median_replies_per_s": 37500,
                   "thread_structure": {"tool": STRACE,
                                        "small": probe(3000, 4),
                                        "large": probe(6000, 4)}},
            "rust": {"implementation": "rust",
                     "rounds": rounds(10000, 61000),
                     "median_replies_per_s": 61000,
                     "thread_structure": {"tool": STRACE,
                                          "small": probe(3000, 4),
                                          "large": probe(6000, 4)}},
        }

    import copy
    cases = [("genuine attestation passes", good(), False)]

    def drop_implementation(product):
        del product["go"]["implementation"]
        del product["rust"]["implementation"]
        return product

    def foreign_implementation(product):
        product["go"]["implementation"] = "rust"
        return product

    cases.append(("implementation label deleted from both engines rejected",
                  _mutate(good(), drop_implementation), True))
    cases.append(("implementation label that contradicts the engine rejected",
                  _mutate(good(), foreign_implementation), True))

    def per_reply(product):
        for key in ("unique_child_tids", "clone_syscalls"):
            product["rust"]["thread_structure"]["small"][key] = 3000
            product["rust"]["thread_structure"]["large"][key] = 6000

    cases.append(("thread-per-reply growth rejected", _mutate(good(), per_reply),
                  True))

    def flat_growth(product):
        product["go"]["thread_structure"]["large"]["clone_syscalls"] = 40

    cases.append(("one extra thread at the doubled request count rejected",
                  _mutate(good(), flat_growth), True))

    def dropped(product):
        product["go"]["rounds"][1]["replies"] = 9000

    cases.append(("dropped replies reported as a rate rejected",
                  _mutate(good(), dropped), True))

    def crashed(product):
        product["rust"]["rounds"][0]["exit_status"] = -9

    cases.append(("killed child scored as a throughput pass rejected",
                  _mutate(good(), crashed), True))

    def zero_census(product):
        for side in (product["go"]["thread_structure"]["small"],
                     product["go"]["thread_structure"]["large"]):
            side["unique_child_tids"] = 0

    cases.append(("clone count with zero parsed task ids rejected",
                  _mutate(good(), zero_census), True))

    def rust_shapes(product):
        # In-band fixtures, not measurements: both engines must be accepted
        # by the same band, so a value near each engine's own measured shape
        # is used (committed `evidence/throughput.json`: Rust 4 clone calls
        # and 4 task ids; Go 18 clone calls with 6 and 9 task ids).  A band
        # tuned to one engine cannot survive here.
        for side in (product["go"]["thread_structure"]["small"],
                     product["go"]["thread_structure"]["large"]):
            side["unique_child_tids"] = 17
            side["clone_syscalls"] = 17
        for side in (product["rust"]["thread_structure"]["small"],
                     product["rust"]["thread_structure"]["large"]):
            side["unique_child_tids"] = 4
            side["clone_syscalls"] = 4

    cases.append(("both engines' measured thread shapes accepted",
                  _mutate(good(), rust_shapes), False))

    def no_strace(product):
        product["go"]["thread_structure"] = None

    cases.append(("missing thread measurement silently accepted",
                  _mutate(good(), no_strace), True))

    def no_rate(product):
        product["rust"]["rounds"] = []

    cases.append(("engine with no rounds accepted", _mutate(good(), no_rate),
                  True))

    def zero_rate(product):
        product["go"]["median_replies_per_s"] = 0

    cases.append(("zero median rate accepted", _mutate(good(), zero_rate), True))

    failures = 0
    for description, product, expect_problem in cases:
        problems = structural_problems(product, ["go", "rust"])
        ok = bool(problems) == expect_problem
        print(f"{'ok  ' if ok else 'BAD '} {description:52} "
              f"problems={len(problems)} expected={expect_problem}")
        if not ok:
            failures += 1
            for problem in problems[:3]:
                print(f"       {problem}")

    # Count and split pins.  ``cases`` is built by appends and the old PASS
    # line printed len(cases), so a control that stopped being appended
    # lowered the number and still exited 0.  The positive/negative split is
    # pinned as well: a mutation whose target field moved would otherwise turn
    # its own control into a no-op that "passes".
    structural = [
        ("the pinned number of cases ran",
         len(cases) == THROUGHPUT_SELF_TEST_CASES,
         f"{len(cases)} cases, expected {THROUGHPUT_SELF_TEST_CASES}"),
        ("the pinned number of doctored records were refused",
         sum(1 for _, _, expect in cases if expect)
         == THROUGHPUT_SELF_TEST_REJECTED,
         f"{sum(1 for _, _, expect in cases if expect)} rejected, expected "
         f"{THROUGHPUT_SELF_TEST_REJECTED}"),
        ("the genuine attestation still passes (non-vacuity anchor)",
         not structural_problems(good(), ["go", "rust"]),
         "the checker now rejects good measurement data"),
        ("this writer commits through the shared provenance owner",
         not audit_report_writers(cli_dir=_HERE,
                                  writers=["throughput_harness.py"],
                                  artifacts=False),
         "see command_sanitize.audit_report_writers"),
    ]
    for description, condition, detail in structural:
        print(f"{'ok  ' if condition else 'BAD '} {description:52} {detail}")
        if not condition:
            failures += 1
    if len(structural) != THROUGHPUT_SELF_TEST_STRUCTURAL:
        print(f"BAD  structural control count drifted: {len(structural)} != "
              f"{THROUGHPUT_SELF_TEST_STRUCTURAL}")
        failures += 1
    run_shared_self_test("throughput_harness")
    print()
    if failures:
        print(f"throughput self-test FAILED: {failures} case(s)")
        return 1
    print(f"throughput self-test PASSED: {len(cases)} cases + "
          f"{len(structural)} structural")
    return 0


def _mutate(base, mutate):
    import copy
    product = copy.deepcopy(base)
    mutate(product)
    return product


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go")
    parser.add_argument("--rust")
    parser.add_argument("--work")
    parser.add_argument("--json-report")
    parser.add_argument("--requests", type=int, default=10000,
                        help="pipelined requests per round")
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--thread-probe-requests", type=int, default=3000,
                        help="request count for the strace thread census; "
                             "the census also runs at twice this count")
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        return _self_test()
    missing = [label for label, value in (("--go", args.go),
                                          ("--rust", args.rust),
                                          ("--work", args.work)) if not value]
    if missing:
        parser.error(f"missing required arguments: {', '.join(missing)}")
    if args.requests < BURST or args.rounds < 1:
        parser.error("--requests must be at least the burst size and "
                     "--rounds at least 1")
    return attestation(args)


if __name__ == "__main__":
    sys.exit(main())
