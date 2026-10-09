#!/usr/bin/env python3
"""Committed create-mode shape gate: a creator-only export's private
temp is created with exactly mode 0600 at ``openat`` — never 0666 with
a later chmod.

Why this gate exists
--------------------
The mode of a file AFTER creation is pinned by ordinary end-state
tests, but the create-mode argument itself is transient: a regression
from ``openat(..., 0600)`` to ``openat(..., 0666)`` followed by the
same chmod leaves every end-state assertion green while reopening the
world-readable window between create and chmod (the review-round 2/4/5
recurrence on this exact line).  The only faithful detector is the
``openat`` mode argument itself, observed through ``strace`` on the
staged product binaries.

Authority and expectations
--------------------------
Both engines must create a creator-only export temp with mode 0600 at
creation.  An engine that creates it 0666 first fails this gate even
if the final mode is 0600.  Unprotected creates are out of scope here:
their contract is the process default plus the owner-bit floor, and
the floor's post-create re-assert is visible to ordinary tests.

Usage
-----
    nice python3 v4/cli/check_create_mode_shape.py --go BIN --rust BIN \\
        --work EMPTY_DIR [--json-report FILE]

    nice python3 v4/cli/check_create_mode_shape.py --self-test

The gate runs two traced passes per engine.  The default pass asserts
the export temp (the only artifact an end-state test can never see).
The switched pass (``IPRANGE_CREATOR_ONLY=1``, the create member
absent so the switch drives it) asserts every creator-only create in
the flow — the main file, the readers table, the worker control file,
and the export temp — because the SecureCreatorOnly re-assert masks
end-state tests for all of them (operations/security round 7).

Scope: this gate is POSIX/Linux-only by subject (openat modes do not
exist on Windows, whose protection is applied as a DACL at CreateFile
and is pinned by the per-assertion mode tests' Windows twins); the
Windows leg runs its own step list and does not run this gate.  The
unprotected exact-default contract (spec 15.6 "keeps the umask
default") is pinned by exact-mode assertions in the recovery/export
test twins, not here: this gate's default pass does not control the
service's umask.

``--self-test`` is offline: it fabricates traces from a frozen table
and proves the verifier rejects a 0666 create, a missing create, and a
trace whose export never ran.  The live run requires
``/usr/bin/strace`` (the throughput harness's convention); a host
without it fails rather than skipping, because a gate that silently
skips is a gate that does not exist.
"""

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
import time

sys.dont_write_bytecode = True

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from command_sanitize import (  # noqa: E402
    require_paths_outside_profile,
    report_provenance,
    run_shared_self_test,
    write_committed_report,
)

REPORT_SCHEMA = "iprange-cli-create-mode-report-v1"
DEFAULT_REPORT = os.path.join(_HERE, "evidence", "create-mode.json")
STRACE = "/usr/bin/strace"

# The export temp's private name (CLI scenario-E shape) and the
# publication namespace's attempt name; a creator-only export's temp is
# one of these two spellings depending on the writing path, and both
# must be created 0600.
TEMP_PATTERNS = (re.compile(r"\.export\.tmp$"),
                 re.compile(r"\.iprange-publish-[0-9a-f]+\.tmp$"))
# The switched pass also pins the flow's creator-only main file, its
# readers table, and the worker control file: SecureCreatorOnly's
# fchmod re-assert masks an openat-mode regression there for every
# end-state test.
SWITCHED_PATTERNS = TEMP_PATTERNS + (
    re.compile(r"\.readers$"),
    re.compile(r"\.iprange-v4-worker-[0-9a-f]+\.ctl$"),
)

# openat(AT_FDCWD, "path", FLAGS, 0MODE) = fd     (mode only with O_CREAT)
OPENAT_CREATE = re.compile(
    r"openat\([^,]+, \"([^\"]+)\", [^)]*O_CREAT[^)]*, 0([0-7]{3,4})\)")

CREATE_PARAMS = {"path": None, "family": "ipv4", "value_kind": "membership",
                 "structure_kind": "none",
                 "value_tag": {"text": "cmode"}, "reader_capacity": 8,
                 "creator_only": True}
EXPORT_PARAMS_SKELETON = {
    "source": {"path": None, "mode": "live"},
    "view": {"kind": "selection", "selection": {"mode": "all"}},
    "format": "ranges", "destination": None,
    "publication_policy": "fail_if_exists",
    "result_budget": {"max_rows": "1000000", "max_output_bytes": "104857600",
                      "max_open_files": 8},
}


class TracedProduct:
    """One ``--jsonrpc`` child under ``strace -f -e trace=openat``.

    The service-driving shape is the committed FIFO-surface gate's
    ``Product``: newline-delimited frames, one selector read with a
    deadline.  Only the spawn is different (the strace prefix).
    """

    def __init__(self, binary, work, trace_log, env=None):
        command = [STRACE, "-f", "-qq", "-e", "trace=openat",
                   "-o", trace_log, binary, "--jsonrpc"]
        child_env = env if env is not None else {
            "PATH": os.environ.get("PATH", "/usr/bin:/bin")}
        self.proc = subprocess.Popen(
            command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, cwd=work, env=child_env)

    def call(self, ident, method, params, timeout=30.0):
        request = {"jsonrpc": "2.0", "id": ident, "method": method,
                   "params": params}
        self.proc.stdin.write(
            (json.dumps(request, separators=(",", ":")) + "\n").encode())
        self.proc.stdin.flush()
        deadline = time.monotonic() + timeout
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise SystemExit(f"{method} timed out after {timeout}s")
            line = self.proc.stdout.readline()
            if not line:
                raise SystemExit(f"{method}: service closed stdout")
            response = json.loads(line)
            if response.get("id") == ident:
                return response
            # Notifications and unrelated ids are not expected on this
            # quiet service; anything else is a protocol defect.
            raise SystemExit(f"{method}: unexpected frame {response!r}")

    def close(self):
        try:
            self.proc.stdin.close()
        except OSError:
            pass
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()


def parse_create_modes(trace_path):
    """Return {path: [mode, ...]} for every O_CREAT openat in the trace."""
    creates = {}
    with open(trace_path, "r", errors="replace") as stream:
        for line in stream:
            match = OPENAT_CREATE.search(line)
            if not match:
                continue
            path, mode = match.group(1), int(match.group(2), 8)
            creates.setdefault(path, []).append(mode)
    return creates


def verifier(creates, engine, findings, patterns=TEMP_PATTERNS,
             subject="default pass", extra_names=()):
    """Assert the create-mode contract over one engine's parsed creates.

    ``extra_names`` names creates whose openat is dirfd-relative (the
    main file's create carries no distinguishing suffix), so the runner
    passes the exact basename it asked for.
    """
    names = set(extra_names)
    temps = {path: modes for path, modes in creates.items()
             if os.path.basename(path) in names
             or any(pattern.search(os.path.basename(path))
                    for pattern in patterns)}
    record = {"pass": subject, "engine": engine,
              "temp_creates": {path: [oct(m) for m in modes]
                               for path, modes in sorted(temps.items())}}
    if not temps:
        findings.append(
            f"{engine} ({subject}): no watched create appears in the "
            "trace — the flow never reached its private create, so the "
            "gate cannot attest the mode")
        record["verdict"] = "fail"
        return record
    # Presence is per artifact, not per set: a trace that lost exactly
    # one watched create (strace loss, a rename, a tamper) must fail,
    # not pass with the remaining three (security/fit-for-purpose
    # round 8). The main file is matched by basename suffix so an
    # absolute or dirfd-relative spelling is watched either way.
    watched_labels = {}
    for path in temps:
        base = os.path.basename(path)
        if base in names:
            watched_labels.setdefault("main", []).append(base)
        elif TEMP_PATTERNS[0].search(base) or TEMP_PATTERNS[1].search(base):
            watched_labels.setdefault("temp", []).append(base)
        elif SWITCHED_PATTERNS[2].search(base):
            watched_labels.setdefault("readers", []).append(base)
        elif SWITCHED_PATTERNS[3].search(base):
            watched_labels.setdefault("control", []).append(base)
    record["watched"] = {label: len(paths) for label, paths
                         in sorted(watched_labels.items())}
    # Record the class-to-key mapping with the FULL observed paths
    # (the same keys temp_creates carries) so the kind gate can bind
    # key identity absolutely — a same-basename dummy in another
    # directory must fail, not just a count change.
    record["watched_keys"] = {
        label: sorted(temps_key for temps_key in temps
                      if os.path.basename(temps_key) in set(paths))
        for label, paths in sorted(watched_labels.items())}
    if patterns is SWITCHED_PATTERNS:
        missing = [label for label in ("main", "temp", "readers", "control")
                   if not watched_labels.get(label)]
        if missing:
            findings.append(
                f"{engine} ({subject}): the switched pass must watch all "
                f"four artifact classes; missing {missing} — a trace that "
                "lost a watched create is not attestable")
            record["verdict"] = "fail"
            return record
    bad = {path: [oct(m) for m in modes if m != 0o600]
           for path, modes in temps.items() if any(m != 0o600 for m in modes)}
    if bad:
        findings.append(
            f"{engine} ({subject}): a creator-only create carried a mode "
            f"other than 0600 (the pre-create window class): {bad}")
        record["verdict"] = "fail"
    else:
        record["verdict"] = "pass"
    return record


def sha256_file(path):
    """The staged binary's digest, so the kind gate can bind the report
    to the exact binaries that produced it (the fifo-surface binding
    shape: a shape-correct report from other binaries is fabrication).
    """
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run_engine(binary, engine, work, switched=False):
    """Drive one create+export under strace; return the evidence record."""
    suffix = "-switched" if switched else ""
    trace_log = os.path.join(work, f"trace-{engine}{suffix}.log")
    main = os.path.join(work, f"source-{engine}{suffix}.v4")
    destination = os.path.join(work, f"out-{engine}{suffix}.ranges")
    env = dict(os.environ, IPRANGE_CREATOR_ONLY="1") if switched else os.environ
    service = TracedProduct(binary, work, trace_log, env=env)
    try:
        create_params = dict(CREATE_PARAMS, path=main)
        if switched:
            # The member is absent so the process switch drives the
            # create (spec: a missing member follows IPRANGE_CREATOR_ONLY).
            del create_params["creator_only"]
        created = service.call(1, "iprange.v1.database.create", create_params)
        if "error" in created:
            raise SystemExit(f"{engine}: create failed: {created['error']}")
        export_params = dict(EXPORT_PARAMS_SKELETON, source={
            "path": main, "mode": "live"}, destination=destination)
        exported = service.call(2, "iprange.v1.export", export_params)
        if "error" in exported:
            raise SystemExit(f"{engine}: export failed: {exported['error']}")
    finally:
        service.close()
    creates = parse_create_modes(trace_log)
    findings = []
    subject = ("switched pass (creator-only main/readers/control/temp)"
               if switched else "default pass (creator-only export temp)")
    record = verifier(creates, engine, findings,
                      patterns=SWITCHED_PATTERNS if switched else TEMP_PATTERNS,
                      subject=subject,
                      extra_names=(os.path.basename(main),) if switched else ())
    record["trace"] = trace_log
    for line in findings:
        print(f"PROBLEM {line}")
    return record, findings


SELF_TEST_TRACES = {
    "pass": {
        "work/source-rust.v4": [0o600],
        "work/.1.export.tmp": [0o600],
        "work/out-rust.ranges": [0o600],
    },
    "window": {
        "work/source-rust.v4": [0o600],
        "work/.1.export.tmp": [0o666],
    },
    "no-export": {
        "work/source-rust.v4": [0o600],
    },
    "unrelated-only": {
        "work/unrelated.tmp": [0o666],
    },
}
SELF_TEST_EXPECT = {"pass": "pass", "window": "fail", "no-export": "fail",
                    "unrelated-only": "fail"}

SWITCHED_SELF_TEST_TRACES = {
    "switched-pass": {
        "work/source-rust.v4": [0o600],
        "work/source-rust.v4.readers": [0o600],
        "/tmp/.iprange-v4-worker-abc.ctl": [0o600],
        "work/.1.export.tmp": [0o600],
    },
    # A trace that lost exactly one watched create is NOT attestable.
    "switched-missing-control": {
        "work/source-rust.v4": [0o600],
        "work/source-rust.v4.readers": [0o600],
        "work/.1.export.tmp": [0o600],
    },
    # The main create spelled absolutely: still watched (suffix match).
    "switched-absolute-main": {
        "/abs/work/source-rust.v4": [0o600],
        "work/source-rust.v4.readers": [0o600],
        "/tmp/.iprange-v4-worker-abc.ctl": [0o600],
        "work/.1.export.tmp": [0o600],
    },
    # The main create spelled absolutely AND regressed: must fail.
    "switched-absolute-window": {
        "/abs/work/source-rust.v4": [0o666],
        "work/source-rust.v4.readers": [0o600],
        "/tmp/.iprange-v4-worker-abc.ctl": [0o600],
        "work/.1.export.tmp": [0o600],
    },
}
SWITCHED_SELF_TEST_EXPECT = {"switched-pass": "pass",
                             "switched-missing-control": "fail",
                             "switched-absolute-main": "pass",
                             "switched-absolute-window": "fail"}


def self_test():
    """Offline: the verifier's table, doctored one mutation at a time."""
    failures = []
    for label, creates in SELF_TEST_TRACES.items():
        findings = []
        record = verifier(creates, "self-test", findings)
        want = SELF_TEST_EXPECT[label]
        if record["verdict"] != want:
            failures.append(f"self-test {label}: verdict "
                            f"{record['verdict']}, want {want}")
        print(f"self-test {label}: {record['verdict']}")
    for label, creates in SWITCHED_SELF_TEST_TRACES.items():
        findings = []
        record = verifier(creates, "self-test", findings,
                          patterns=SWITCHED_PATTERNS,
                          subject="switched self-test",
                          extra_names=("source-rust.v4",))
        want = SWITCHED_SELF_TEST_EXPECT[label]
        if record["verdict"] != want:
            failures.append(f"self-test {label}: verdict "
                            f"{record['verdict']}, want {want}")
        print(f"self-test {label}: {record['verdict']}")
    return failures


def main():
    parser = argparse.ArgumentParser(
        description=__doc__.splitlines()[0],
        formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--go", help="Go CLI binary")
    parser.add_argument("--rust", help="Rust CLI binary")
    parser.add_argument("--work", help="empty working directory")
    parser.add_argument("--json-report", default=DEFAULT_REPORT)
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()

    if args.self_test:
        failures = self_test()
        executed = run_shared_self_test("check_create_mode_shape")
        if executed is not None and not isinstance(executed, int):
            failures.append("shared self-test returned an unexpected shape")
        if failures:
            for line in failures:
                print(f"FAIL self-test: {line}")
            return 1
        print("PASS self-test: create-mode verifier controls")
        return 0

    missing = [name for name, value in (("--go", args.go),
                                        ("--rust", args.rust),
                                        ("--work", args.work))
               if not value]
    if missing:
        parser.error(f"missing arguments: {', '.join(missing)}")
    if not os.path.exists(STRACE):
        print(f"FAIL: {STRACE} is required (this gate observes openat "
              "modes; a silent skip would make it nonexistent)")
        return 1
    # The input-side privacy net (the shared tier's standing shape): a
    # personal path can only reach the report through a caller-supplied
    # option, so refuse it before any arm executes.
    require_paths_outside_profile((("--go", args.go),
                                   ("--rust", args.rust),
                                   ("--work", args.work),
                                   ("--json-report", args.json_report)))
    # Absolute work dir: the create resolution's parent-directory identity
    # proof is driven with absolute paths everywhere else, and a relative
    # --work would trip the relative-path create limitation instead of this
    # gate's subject.
    args.work = os.path.abspath(args.work)
    os.makedirs(args.work, exist_ok=True)

    args.rust = os.path.abspath(args.rust)
    args.go = os.path.abspath(args.go)
    binaries = {
        "rust": {"implementation": "rust", "sha256": sha256_file(args.rust)},
        "go": {"implementation": "go", "sha256": sha256_file(args.go)},
    }
    records = []
    findings = []
    for engine, binary in (("rust", args.rust), ("go", args.go)):
        for switched in (False, True):
            record, engine_findings = run_engine(binary, engine, args.work,
                                                 switched=switched)
            records.append(record)
            findings.extend(engine_findings)

    report = {"schema": REPORT_SCHEMA,
              "provenance": report_provenance(),
              "binaries": binaries,
              "engines": records,
              "verdict": "pass" if not findings else "fail"}
    write_committed_report(args.json_report, report,
                           caller_paths=(("--go", args.go),
                                         ("--rust", args.rust),
                                         ("--work", args.work),
                                         ("--json-report", args.json_report)))
    print(f"CREATE-MODE verdict={report['verdict']} "
          f"report={os.path.relpath(args.json_report)}")
    return 0 if not findings else 1


if __name__ == "__main__":
    sys.exit(main())
