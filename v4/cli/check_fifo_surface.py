#!/usr/bin/env python3
"""Committed FIFO-surface gate: every user-path open arm must refuse a
named pipe promptly with a product error, never block.

Why this gate exists
--------------------
A database opened from a FIFO blocks forever when the open class is not
refused before ``open(2)``, and a hang is invisible to a case corpus:
the run either never returns or returns after a timeout, and neither
outcome produces a stable expected value.  The refusal class therefore
cannot live in prose ("the engines are expected to reject non-regular
files"); it has to be pinned by an executable check that names each arm,
its transport code, and its ``data.code`` class.

Authority and expectations
--------------------------
Rust observable behavior is the semantic authority, so each arm records
the class Rust answers today and the same class is asserted for Go:
arm-exact parity is a contract term, and an engine that answers with a
different class is a defect that this gate reports rather than forgives.

Placement is static: the FIFO exists before the request is written, so
every arm tests the pre-open refusal path rather than a race between
open and a writer appearing.

Usage
-----
    nice python3 v4/cli/check_fifo_surface.py --go BIN --rust BIN \
        --fixture BIN --work EMPTY_DIR [--json-report FILE]

    nice python3 v4/cli/check_fifo_surface.py --self-test

``--self-test`` is offline: it fabricates a report from the frozen table
and proves the verifier rejects doctored variants (a flipped class, a
dropped arm, an invented arm, a missing engine, a blocked arm, stripped
request bytes).  The live run additionally checks that each refusal
arrives within ``--deadline`` seconds and that the same binaries still
serve a regular-file control.
"""

import argparse
import hashlib
import json
import os
import platform
import selectors
import subprocess
import sys
import time

sys.dont_write_bytecode = True

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)
_SELF_DIR = os.path.dirname(os.path.abspath(__file__))

from command_sanitize import (  # noqa: E402
    audit_report_writers,
    report_provenance,
    require_paths_outside_profile,
    run_shared_self_test,
    sanitized_path_value,
    write_committed_report,
)

REPORT_SCHEMA = "iprange-cli-fifo-surface-report-v1"
DEFAULT_REPORT = os.path.join(_HERE, "evidence", "fifo-surface.json")

# Transport code for a product-level refusal (as opposed to -32602,
# which the params validator answers before the request reaches an
# open path at all).
PRODUCT_ERROR = -32010
# A refusal that arrives later than this is a hang with a tail, not a
# refusal: the open reached the kernel and waited.
BLOCKING_SECONDS = 3.0

WRITER_BUDGET = {"max_heap_bytes": "16777216", "max_private_pages": "256",
                 "max_growth_pages": "256", "max_open_files": 4}
VALIDATION_BUDGET = {"max_heap_bytes": "16777216", "max_open_files": 4,
                     "max_scratch_bytes": "0", "max_scratch_files": 0}
RECOVERY_BUDGET = {"max_heap_bytes": "16777216", "max_open_files": 4,
                   "max_output_pages": "20000", "max_scratch_bytes": "0",
                   "max_scratch_files": 0}
CANDIDATE = {"label": "newest", "meta_page": 1,
             "source_identity": {"volume": "1", "file": "2"},
             "database_id": "000102030405060708090a0b0c0d0e0f",
             "transaction_id": "3",
             "commit_nonce": "000102030405060708090a0b0c0d0e0f"}
SNAPSHOT_BUDGET = {"max_heap_bytes": "16777216", "max_output_pages": "512",
                   "max_open_files": 32}


def _findings(work):
    return {"path": os.path.join(work, "findings.jsonl"), "format": "jsonl",
            "publication_policy": "replace_existing",
            "result_budget": {"max_rows": "64", "max_output_bytes": "65536",
                              "max_open_files": 3}}


def _report_output(work):
    return {"format": "jsonl", "path": os.path.join(work, "report.jsonl"),
            "publication_policy": "replace_existing",
            "result_budget": {"max_open_files": 3,
                              "max_output_bytes": "65536", "max_rows": "64"}}


# One entry per arm: ``(arm, method, expected_data_code, params)`` where
# ``params`` is called with the FIFO path, the work directory, and the
# paths the preparation phase created.  The expected class is what Rust
# answers today, measured against the qualification binaries:
#
#   invalid_argument  read-only opens (open_read_only require_regular_file)
#   wrong_state       quiescent/offline opens (live_namespace::open_rw
#                     NotRegular), where the state check precedes the
#                     regular-file check
#   invalid_path      writer inputs and metadata sources, refused by a
#                     pre-open stat (lifecycle::read_file_exact, the CSV
#                     and file-list input guards)
#   conflict          a publication destination that is not a regular
#                     file, refused by the publication namespace
ARMS = [
    ("reader.open", "iprange.v1.reader.open", "invalid_argument",
     lambda f, w, p: {"source": {"path": f, "mode": "immutable"}}),
    ("database.info", "iprange.v1.database.info", "invalid_argument",
     lambda f, w, p: {"source": {"path": f, "mode": "immutable"}}),
    ("metadata.get", "iprange.v1.database.metadata.get", "invalid_argument",
     lambda f, w, p: {"source": {"path": f, "mode": "immutable"},
                      "delivery": {"mode": "inline"}}),
    ("validate.immutable", "iprange.v1.validate", "invalid_argument",
     lambda f, w, p: {"path": f, "mode": {"kind": "immutable_current"},
                      "validation_budget": VALIDATION_BUDGET,
                      "findings_output": _findings(w)}),
    ("validate.live", "iprange.v1.validate", "invalid_argument",
     lambda f, w, p: {"path": f, "mode": {"kind": "live_current"},
                      "validation_budget": VALIDATION_BUDGET,
                      "findings_output": _findings(w)}),
    ("validate.offline_candidate", "iprange.v1.validate", "wrong_state",
     lambda f, w, p: {"path": f, "mode": {"kind": "offline_candidate",
                                          "candidate": CANDIDATE},
                      "validation_budget": VALIDATION_BUDGET,
                      "findings_output": _findings(w)}),
    ("inspect.immutable", "iprange.v1.recovery.inspect", "invalid_argument",
     lambda f, w, p: {"path": f, "mode": "immutable",
                      "validation_budget": VALIDATION_BUDGET}),
    ("inspect.live", "iprange.v1.recovery.inspect", "invalid_argument",
     lambda f, w, p: {"path": f, "mode": "live",
                      "validation_budget": VALIDATION_BUDGET}),
    ("inspect.offline", "iprange.v1.recovery.inspect", "wrong_state",
     lambda f, w, p: {"path": f, "mode": "caller_certified_offline",
                      "validation_budget": VALIDATION_BUDGET}),
    ("recover.immutable", "iprange.v1.recover", "invalid_argument",
     lambda f, w, p: {"source_path": f, "source_mode": "immutable",
                      "candidate": CANDIDATE,
                      "destination": os.path.join(w, "dest.iprange"),
                      "recovery_budget": RECOVERY_BUDGET,
                      "report_output": _report_output(w)}),
    ("recover.offline", "iprange.v1.recover", "wrong_state",
     lambda f, w, p: {"source_path": f, "source_mode":
                      "caller_certified_offline", "candidate": CANDIDATE,
                      "destination": os.path.join(w, "dest.iprange"),
                      "recovery_budget": RECOVERY_BUDGET,
                      "report_output": _report_output(w)}),
    # Writer-side surfaces: the metadata source, the CSV input, the @file
    # list a publisher expands, and the source a feed is created from.
    ("direct.replace-meta-fifo", "iprange.v1.direct.replace", "invalid_path",
     lambda f, w, p: {"path": p["meta_target"],
                      "input": {"path": p["data_csv"],
                                "max_line_bytes": 1048576},
                      "metadata": {"mode": "replace_file", "path": f},
                      "writer_budget": WRITER_BUDGET}),
    ("metadata.replace-file-fifo", "iprange.v1.database.metadata.replace",
     "invalid_path",
     lambda f, w, p: {"path": p["meta_target"],
                      "metadata": {"mode": "replace_file", "path": f},
                      "writer_budget": WRITER_BUDGET}),
    ("direct.csv-fifo", "iprange.v1.direct.replace", "invalid_path",
     lambda f, w, p: {"path": p["csv_target"],
                      "input": {"path": f, "max_line_bytes": 1048576},
                      "metadata": {"mode": "keep"},
                      "writer_budget": WRITER_BUDGET}),
    ("atfilelist.fifo", "iprange.v1.current.publish", "invalid_path",
     lambda f, w, p: {"input": {"paths": ["@" + p["list_file"]],
                               "family": "ipv4", "fix_network": False,
                               "default_prefix": 32,
                               "dns": {"threads": 1, "silent": True},
                               "expand_at_paths": True,
                               "max_line_bytes": 1024,
                               "max_expanded_paths": 16},
                      "feed": "at", "value_tag": {"text": "at"},
                      "metadata": {"mode": "clear"},
                      "destination": os.path.join(w, "atpub.iprange"),
                      "publication_policy": "fail_if_exists",
                      "immutable_feed_budget": {
                          "max_heap_bytes": "16777216",
                          "max_output_pages": "20000",
                          "max_workspace_pages": "20000",
                          "max_open_files": 3}}),
    ("feeds.create-source-fifo", "iprange.v1.feeds.create",
     "invalid_argument",
     lambda f, w, p: {"path": p["feed_target"], "feed": "gamma",
                      "current": {"source": {"path": f, "mode": "immutable"},
                                  "feed": "alpha"},
                      "metadata": {"mode": "keep"},
                      "writer_budget": WRITER_BUDGET}),
    ("snapshot.dest-fifo", "iprange.v1.snapshot", "conflict",
     lambda f, w, p: {"source": {"path": p["fixture_db"], "mode":
                                 "immutable"},
                      "destination": f,
                      "publication_policy": "replace_existing",
                      "snapshot_budget": SNAPSHOT_BUDGET}),
]

ARM_NAMES = [arm for arm, _m, _c, _f in ARMS]
ARM_BY_NAME = {entry[0]: entry for entry in ARMS}
ARM_METHOD = {arm: method for arm, method, _c, _f in ARMS}
ARM_EXPECTED = {arm: code for arm, _m, code, _f in ARMS}

# Files that must be absent before an arm so a leftover from an earlier
# arm cannot change the refusal being measured, plus the targets that an
# arm needs to exist as a regular file (direct.replace stats its target
# before reading the metadata file, so the metadata FIFO only refuses
# first when the target itself is regular).
_EPHEMERAL = ("dest.iprange", "report.jsonl", "findings.jsonl",
              "atpub.iprange")


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


class Product:
    """One ``--jsonrpc`` child process speaking newline-delimited frames."""

    def __init__(self, binary, work):
        self.proc = subprocess.Popen(
            [binary, "--jsonrpc"], stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, cwd=work,
            env={"PATH": os.environ.get("PATH", "/usr/bin:/bin")},
            start_new_session=True)
        self.buffer = b""

    def call(self, request, timeout):
        """Return ``(kind, response)`` where kind is answered, timeout, or
        exited.  The kind is part of the evidence: an arm that produced
        no frame is a blocked arm, not a failed assertion."""
        self.proc.stdin.write(
            (json.dumps(request, separators=(",", ":")) + "\n").encode())
        self.proc.stdin.flush()
        deadline = time.monotonic() + timeout
        selector = selectors.DefaultSelector()
        fd = self.proc.stdout.fileno()
        selector.register(fd, selectors.EVENT_READ)
        try:
            while time.monotonic() < deadline:
                index = self.buffer.find(b"\n")
                if index >= 0:
                    raw, self.buffer = (self.buffer[:index + 1],
                                        self.buffer[index + 1:])
                    return "answered", json.loads(raw)
                if self.proc.poll() is not None:
                    return "exited", None
                remaining = deadline - time.monotonic()
                if selector.select(min(remaining, 0.2)):
                    chunk = os.read(fd, 65536)
                    if not chunk:
                        return "exited", None
                    self.buffer += chunk
        finally:
            selector.close()
        return "timeout", None

    def close(self, timeout=5.0):
        try:
            self.proc.stdin.close()
        except OSError:
            pass
        try:
            return self.proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(os.getpgid(self.proc.pid), 9)
            return self.proc.wait()


def _require_absolute(label, value):
    """Refuse a relative harness path before anything runs.

    A relative ``--work`` makes the FIFO and the fixtures resolve under
    the invocation directory, so an arm can read the wrong object and
    the gate silently reports the opposite verdict.  Every path the gate
    writes or executes is absolute or the gate stops."""
    if not value or not os.path.isabs(value):
        raise SystemExit(f"{label} must be an absolute path, got {value!r}")
    return value


def prepare(work, fixture_tool):
    """Create the regular files the writer arms need; return their paths."""
    paths = {}
    fixture_db = os.path.join(work, "fixture.iprange")
    subprocess.run([fixture_tool, "direct-v4", fixture_db], check=True)
    paths["fixture_db"] = fixture_db
    with open(os.path.join(work, "data.csv"), "w", encoding="utf-8") as out:
        out.write("from,to,value\n192.0.2.0-192.0.2.3,5\n")
    paths["data_csv"] = os.path.join(work, "data.csv")
    list_file = os.path.join(work, "list.txt")
    with open(list_file, "w", encoding="utf-8") as out:
        out.write("@%s\n" % os.path.join(work, "entry.fifo"))
    paths["list_file"] = list_file
    os.mkfifo(os.path.join(work, "entry.fifo"), 0o600)
    return paths


def _create_target(work, name):
    """A live writer refuses a target that is not a regular file, so the
    arms whose refusal under test is elsewhere need the target to exist."""
    path = os.path.join(work, name)
    with open(path, "wb"):
        pass
    return path


def _new_database(binary, work, path, tag):
    """Create an empty live database with `binary` so a writer arm reaches
    its input check instead of its destination check."""
    product = Product(binary, work)
    try:
        kind, response = product.call(
            {"jsonrpc": "2.0", "id": 1, "method": "iprange.v1.database.create",
             "params": {"path": path, "family": "ipv4", "value_kind": "direct",
                        "structure_kind": "none", "value_tag": {"text": tag},
                        "reader_capacity": 8}}, timeout=10.0)
    finally:
        product.close()
    if kind != "answered" or not response.get("result"):
        raise SystemExit(f"cannot create {path}: {kind} {response}")
    return path


def run_arm(binary, work, fifo, arm, context, deadline):
    """Execute one arm on one engine and return its evidence record.

    The arm is looked up in :data:`ARMS`, the single authority for both the
    request shape and its expected class; the record always carries the
    table's expectation so a report cannot assert a class the gate never
    pinned."""
    for name in _EPHEMERAL:
        try:
            os.unlink(os.path.join(work, name))
        except FileNotFoundError:
            pass
    definition = ARM_BY_NAME.get(arm)
    if definition is None:  # pragma: no cover - ARMS owns the table
        raise SystemExit(f"unknown arm {arm!r}")
    _name, method, expected, build = definition
    request = {"jsonrpc": "2.0", "id": 1, "method": method,
               "params": build(fifo, work, context)}
    product = Product(binary, work)
    started = time.monotonic()
    kind, response = product.call(request, timeout=deadline)
    elapsed = time.monotonic() - started
    exit_status = product.close(timeout=deadline)
    transport_code = data_code = message = None
    if kind == "answered" and isinstance(response, dict):
        error = response.get("error")
        if isinstance(error, dict):
            transport_code = error.get("code")
            data = error.get("data")
            if isinstance(data, dict):
                data_code = data.get("code")
            message = error.get("message")
    return {"arm": arm, "method": method, "expected_code": expected,
            "kind": kind, "transport_code": transport_code,
            "data_code": data_code, "message": message,
            "elapsed_ms": round(elapsed * 1000, 1),
            "exit_status": exit_status,
            "request": json.dumps(request, separators=(",", ":"))}


def arm_is_correct(record, deadline):
    """True when the record shows the pinned prompt refusal."""
    return (record["kind"] == "answered"
            and record["transport_code"] == PRODUCT_ERROR
            and record["data_code"] == record["expected_code"]
            and record["exit_status"] == 0
            and record["elapsed_ms"] < deadline * 1000)


def assess_report(report, deadline=BLOCKING_SECONDS):
    """Verify one FIFO report against the frozen table.

    The table is the single authority: an arm missing from the report, an
    arm the table does not define, an engine that did not run, a flipped
    ``expected_code``, a ``data_code`` that does not match its own
    expectation, a blocked arm, and stripped request bytes are each a
    problem.  This is the function ``--self-test`` attacks."""
    problems = []
    if not isinstance(report, dict):
        return [f"report is {type(report).__name__}, not an object"]
    if report.get("schema") != REPORT_SCHEMA:
        problems.append(f"unexpected schema {report.get('schema')!r}")
    for member in ("git_head", "checkout_root", "command", "platform",
                   "binaries", "arms", "controls", "summary"):
        if member not in report:
            problems.append(f"report is missing {member!r}")
    binaries = report.get("binaries") or {}
    engines = sorted(binaries)
    if engines != ["go", "rust"]:
        problems.append(f"report covers engines {engines}, expected "
                        f"['go', 'rust']: each engine must answer for the "
                        f"same arms")
    for label, record in sorted(binaries.items()):
        if not isinstance(record, dict):
            problems.append(f"binary {label!r} is not an object")
            continue
        if record.get("implementation") != label:
            problems.append(f"binary {label!r} records implementation "
                            f"{record.get('implementation')!r}")
        digest = record.get("sha256")
        if not (isinstance(digest, str)
                and len(digest) == 64
                and all(c in "0123456789abcdef" for c in digest)):
            problems.append(f"binary {label!r} has no sha256: the FIFO "
                            f"verdict must bind to a measured artifact")
    observed = {}
    for index, record in enumerate(report.get("arms") or []):
        if not isinstance(record, dict):
            problems.append(f"arms[{index}] is not an object")
            continue
        engine, arm = record.get("engine"), record.get("arm")
        if engine not in ("go", "rust"):
            problems.append(f"arms[{index}]: unknown engine {engine!r}")
            continue
        if arm not in ARM_EXPECTED:
            problems.append(f"arms[{index}]: arm {arm!r} is not defined by "
                            f"the gate table; an invented arm cannot "
                            f"purchase a PASS")
            continue
        if (engine, arm) in observed:
            problems.append(f"arms[{index}]: duplicate record for "
                            f"{engine}/{arm}; the later record would "
                            f"silently outrank the first")
            continue
        observed[(engine, arm)] = record
        pinned = ARM_EXPECTED[arm]
        if record.get("expected_code") != pinned:
            problems.append(
                f"{engine}/{arm}: expected_code "
                f"{record.get('expected_code')!r} contradicts the gate "
                f"table, which pins {pinned!r} from the Rust reference")
            continue
        if not isinstance(record.get("request"), str) or \
                '"method"' not in record["request"]:
            problems.append(f"{engine}/{arm}: request bytes are not "
                            f"recorded; a refusal claim must carry the "
                            f"frame that produced it")
            continue
        if not arm_is_correct(record, deadline):
            problems.append(
                f"{engine}/{arm}: did not answer promptly with "
                f"{PRODUCT_ERROR}/{pinned} (kind={record.get('kind')!r} "
                f"code={record.get('transport_code')!r} "
                f"data={record.get('data_code')!r} "
                f"rc={record.get('exit_status')!r} "
                f"elapsed_ms={record.get('elapsed_ms')!r})")
    for engine in ("go", "rust"):
        for arm in ARM_NAMES:
            if (engine, arm) not in observed:
                problems.append(f"{engine}/{arm}: the arm is absent from the "
                                f"report; dropping an arm is not passing it")
    controls = report.get("controls") or []
    for engine in ("go", "rust"):
        matching = [c for c in controls if isinstance(c, dict)
                    and c.get("engine") == engine
                    and c.get("arm") == "regular-file-control"]
        if len(matching) != 1:
            problems.append(f"{engine}: no regular-file control; a gate "
                            f"that only ever sees refusals cannot tell a "
                            f"refusal from a dead service")
            continue
        control = matching[0]
        if control.get("kind") != "answered" \
                or control.get("answered_result") is not True \
                or control.get("exit_status") != 0:
            problems.append(f"{engine}: regular-file control did not "
                            f"succeed")
    summary = report.get("summary")
    if isinstance(summary, dict):
        if summary.get("arms_expected") != len(ARM_NAMES) * 2:
            problems.append(f"summary arms_expected "
                            f"{summary.get('arms_expected')!r} != "
                            f"{len(ARM_NAMES) * 2}")
        if summary.get("failed") not in ([], None) and report.get("result") \
                == "PASS":
            problems.append("summary records failures but result is PASS")
    else:
        problems.append("summary is not an object")
    if not problems and report.get("result") != "PASS":
        problems.append("every arm succeeded but result is not PASS")
    return problems


def _control_record(engine, binary, work, fixture_db):
    """The regular-file control: the same service that refuses a FIFO must
    still answer for a regular file, so a crash-on-start cannot read as a
    correct refusal."""
    product = Product(binary, work)
    kind, response = product.call(
        {"jsonrpc": "2.0", "id": 1, "method": "iprange.v1.database.info",
         "params": {"source": {"path": fixture_db, "mode": "immutable"}}},
        timeout=10.0)
    exit_status = product.close()
    return {"engine": engine, "arm": "regular-file-control", "kind": kind,
            "answered_result": bool(isinstance(response, dict)
                                    and response.get("result")),
            "exit_status": exit_status}


def _fabricated_report():
    """A report built straight from the frozen table: the baseline the
    self-test tampers with."""
    arms = []
    for engine in ("go", "rust"):
        for arm in ARM_NAMES:
            arms.append({"engine": engine, "arm": arm,
                         "method": ARM_METHOD[arm],
                         "expected_code": ARM_EXPECTED[arm],
                         "kind": "answered",
                         "transport_code": PRODUCT_ERROR,
                         "data_code": ARM_EXPECTED[arm],
                         "message": "pinned refusal",
                         "elapsed_ms": 1.0, "exit_status": 0,
                         "request": json.dumps(
                             {"jsonrpc": "2.0", "id": 1,
                              "method": ARM_METHOD[arm], "params": {}},
                             separators=(",", ":"))})
    return {"schema": REPORT_SCHEMA, "git_head": "0" * 40,
            "checkout_root": None,
            "command": ["v4/cli/check_fifo_surface.py"],
            "platform": {"system": "Linux", "release": "x", "machine": "x86_64",
                         "python": platform.python_version()},
            "binaries": {"go": {"implementation": "go", "path": "/b/go",
                                "sha256": "a" * 64},
                         "rust": {"implementation": "rust", "path": "/b/rust",
                                  "sha256": "b" * 64}},
            "arms": arms,
            "controls": [{"engine": engine, "arm": "regular-file-control",
                          "kind": "answered", "answered_result": True,
                          "exit_status": 0} for engine in ("go", "rust")],
            "summary": {"arms_expected": len(ARM_NAMES) * 2, "failed": []},
            "result": "PASS"}


# Executed-control counts for ``--self-test``, as literals.  The case list is
# built by appends, so a control that stopped being appended -- a rename, a
# mutator whose target row vanished from the fabricated baseline -- would leave
# a shorter list and still print "self-test PASSED: 0 failures".  The pins make
# the drop visible: adding a rejection class to ``assess_report`` or to
# ``ARMS`` means adding its control here and raising these numbers.
FIFO_SELF_TEST_CASES = 18
# Of the 18 cases, 16 doctored reports must be refused and 2 must be accepted
# (the genuine baseline and the refusal that is just inside the deadline).  A
# drift in either direction means a control stopped asserting what it claimed.
FIFO_SELF_TEST_REJECTED = 16
FIFO_SELF_TEST_ARMS = 34
FIFO_SELF_TEST_STRUCTURAL = 5


def _self_test():
    """Prove the verifier rejects doctored FIFO reports.

    Runs offline against the frozen arm table (``_fabricated_report``), so no
    product binary is needed: the subject is ``assess_report``, the same
    verifier the live run applies to its own artifact.
    """
    import copy

    cases = []
    mutated = {}

    baseline = _fabricated_report()
    cases.append(("genuine report passes", baseline, False))

    def one(mutate, description, expect_problem):
        report = copy.deepcopy(baseline)
        mutate(report)
        # A mutator whose target row is absent would assert nothing at all:
        # the verifier would reject the report for some other reason, or
        # accept an untouched baseline.  Comparing against the baseline turns
        # "the control never fired" into a counted failure below.
        mutated[description] = report != baseline
        cases.append((description, report, expect_problem))

    def flip_class(report):
        for record in report["arms"]:
            if record["arm"] == "reader.open" and record["engine"] == "rust":
                record["data_code"] = "wrong_state"
                return

    one(flip_class, "class flipped against the table", True)

    def flip_expected(report):
        for record in report["arms"]:
            if record["arm"] == "reader.open" and record["engine"] == "rust":
                record["expected_code"] = "wrong_state"
                record["data_code"] = "wrong_state"
                return

    one(flip_expected, "expected_code rewritten to match a wrong answer", True)

    def drop_arm(report):
        report["arms"] = [r for r in report["arms"]
                          if not (r["engine"] == "go"
                                  and r["arm"] == "validate.live")]
        report["summary"]["arms_expected"] = len(report["arms"])

    one(drop_arm, "arm dropped from the report", True)

    def drop_engine(report):
        report["arms"] = [r for r in report["arms"]
                          if r["engine"] == "rust"]
        report["controls"] = [c for c in report["controls"]
                              if c["engine"] == "rust"]
        del report["binaries"]["go"]
        report["summary"]["arms_expected"] = len(report["arms"])

    one(drop_engine, "one engine removed entirely", True)

    def add_invented(report):
        report["arms"].append({"engine": "rust", "arm": "made.up",
                               "method": "iprange.v1.reader.open",
                               "expected_code": "invalid_argument",
                               "kind": "answered",
                               "transport_code": PRODUCT_ERROR,
                               "data_code": "invalid_argument",
                               "message": "x", "elapsed_ms": 1.0,
                               "exit_status": 0,
                               "request": "{\"jsonrpc\":2.0,\"method\":\"x\"}"})

    one(add_invented, "arm invented outside the table", True)

    def block_arm(report):
        for record in report["arms"]:
            if record["arm"] == "recover.immutable" \
                    and record["engine"] == "go":
                record["kind"] = "timeout"
                record["transport_code"] = None
                record["data_code"] = None
                record["elapsed_ms"] = 30000.0
                return

    one(block_arm, "arm that blocked reported as if it refused", True)

    def slow_arm(report):
        for record in report["arms"]:
            if record["arm"] == "inspect.live" and record["engine"] == "go":
                record["elapsed_ms"] = 2900.0
                return

    one(slow_arm, "refusal just inside the deadline is accepted", False)

    def blocked_arm(report):
        for record in report["arms"]:
            if record["arm"] == "validate.immutable" \
                    and record["engine"] == "rust":
                record["elapsed_ms"] = 3100.0
                return

    one(blocked_arm, "refusal past the deadline is a hang, not a refusal",
        True)

    def strip_request(report):
        for record in report["arms"]:
            if record["engine"] == "rust":
                record["request"] = ""
                return

    one(strip_request, "request bytes stripped from an arm", True)

    def kill_control(report):
        report["controls"] = [c for c in report["controls"]
                              if c["engine"] != "go"]

    one(kill_control, "regular-file control dropped for one engine", True)

    def dead_service_control(report):
        for control in report["controls"]:
            if control["engine"] == "go":
                control["answered_result"] = False
                return

    one(dead_service_control, "regular-file control that never answered", True)

    def crash_not_refusal(report):
        for record in report["arms"]:
            if record["engine"] == "go":
                record["exit_status"] = 1
                return

    one(crash_not_refusal, "service exit 1 counted as a refusal", True)

    def wrong_transport(report):
        for record in report["arms"]:
            if record["engine"] == "rust":
                record["transport_code"] = -32602
                return

    one(wrong_transport, "params-validator code dressed as an open refusal",
        True)

    def unhashed(report):
        report["binaries"]["go"]["sha256"] = "not-a-digest"

    one(unhashed, "binary without a measured sha256", True)

    def mislabeled(report):
        report["binaries"]["go"]["implementation"] = "rust"

    one(mislabeled, "binary label contradicts its implementation", True)

    def no_summary(report):
        report["summary"] = {}

    one(no_summary, "summary dropped", True)

    def hiding_member(report):
        del report["arms"][0]
        report["summary"]["arms_expected"] = len(report["arms"])

    one(hiding_member, "first arm dropped with the counter rewritten", True)

    failures = 0
    for description, report, expect_problem in cases:
        problems = assess_report_for_self_test(report)
        rejected = bool(problems)
        ok = rejected if expect_problem else not rejected
        print(f"{'ok  ' if ok else 'BAD '} {description:52} "
              f"rejected={rejected} expected_rejected={expect_problem}")
        if not ok:
            failures += 1
            for problem in problems[:3]:
                print(f"       {problem}")

    # Count and non-vacuity pins.  Each is a case in its own right, so a
    # dropped control cannot hide behind "everything that ran passed".
    structural = [
        ("the pinned number of cases ran",
         len(cases) == FIFO_SELF_TEST_CASES,
         f"{len(cases)} cases, expected {FIFO_SELF_TEST_CASES}"),
        ("the pinned number of doctored reports were refused",
         sum(1 for _, _, expect in cases if expect) == FIFO_SELF_TEST_REJECTED,
         f"{sum(1 for _, _, expect in cases if expect)} rejected, expected "
         f"{FIFO_SELF_TEST_REJECTED}"),
        ("every mutator actually changed its baseline",
         len(mutated) == FIFO_SELF_TEST_CASES - 1
         and all(mutated.values()),
         f"{[name for name, fired in mutated.items() if not fired]}"),
        ("the fabricated report covers the whole arm table on both engines",
         len(baseline["arms"]) == FIFO_SELF_TEST_ARMS
         and len({record["engine"] for record in baseline["arms"]}) == 2,
         f"{len(baseline['arms'])} arm records, expected "
         f"{FIFO_SELF_TEST_ARMS}"),
        ("this writer commits through the shared provenance owner",
         not audit_report_writers(cli_dir=_SELF_DIR, writers=["check_fifo_surface.py"],
                                  artifacts=False),
         "see command_sanitize.audit_report_writers"),
    ]
    for description, condition, detail in structural:
        print(f"{'ok  ' if condition else 'BAD '} {description:52} {detail}")
        if not condition:
            failures += 1
    if len(structural) != FIFO_SELF_TEST_STRUCTURAL:
        print(f"BAD  the pinned number of structural controls ran: "
              f"{len(structural)} != {FIFO_SELF_TEST_STRUCTURAL}")
        failures += 1
    run_shared_self_test("check_fifo_surface")
    print()
    if failures:
        print(f"FIFO surface self-test FAILED: {failures} case(s)")
        return 1
    print(f"FIFO surface self-test PASSED: {len(cases)} cases + "
          f"{len(structural)} structural, "
          f"{len(ARM_NAMES)} arms x 2 engines")
    return 0


def assess_report_for_self_test(report):
    """``assess_report`` with the deliberate-slowness case re-pinned.

    ``assess_report`` takes the run's deadline so the same verification
    covers a slower host; the self-test uses the committed constant."""
    return assess_report(report, deadline=BLOCKING_SECONDS)


def live_run(args):
    """Run every arm on both engines and write the evidence report."""
    # Durable-artifact policy, applied before anything runs: the report
    # records the measured binary paths and the fixture digest, so a
    # profile-rooted input is how an operator-home path reaches a committed
    # file.  The shared writer repeats the check at commit time; doing it here
    # means the arms never execute on a run whose artifact is refused anyway.
    require_paths_outside_profile((("--go", args.go), ("--rust", args.rust),
                                   ("--fixture", args.fixture),
                                   ("--work", args.work),
                                   ("--json-report", args.json_report)))
    work = _require_absolute("--work", args.work)
    if os.listdir(work):
        raise SystemExit(f"--work must be an empty directory: {work}")
    go = _require_absolute("--go", args.go)
    rust = _require_absolute("--rust", args.rust)
    fixture = _require_absolute("--fixture", args.fixture)
    for label, path in (("go", go), ("rust", rust), ("fixture", fixture)):
        if not os.path.isfile(path) or not os.access(path, os.X_OK):
            raise SystemExit(f"--{label} is not an executable file: {path}")

    fifo = os.path.join(work, "db.fifo")
    os.mkfifo(fifo, 0o600)
    context = prepare(work, fixture)
    context["meta_target"] = _new_database(
        rust, work, os.path.join(work, "meta_target.iprange"), "mt")
    context["csv_target"] = _new_database(
        rust, work, os.path.join(work, "csv_target.iprange"), "ct")
    context["feed_target"] = _new_database(
        rust, work, os.path.join(work, "feed_target.iprange"), "ft")

    arms = []
    controls = []
    for engine, binary in (("rust", rust), ("go", go)):
        for arm in ARM_NAMES:
            record = run_arm(binary, work, fifo, arm, context,
                             args.deadline)
            record["engine"] = engine
            arms.append(record)
            status = "ok" if arm_is_correct(record, args.deadline) else "FAIL"
            print(f"{engine:4} {arm:26} {status:4} kind={record['kind']:8}"
                  f" code={record['transport_code']} "
                  f"data={record['data_code']} want={record['expected_code']}"
                  f" rc={record['exit_status']} "
                  f"elapsed={record['elapsed_ms']}ms")
        controls.append(_control_record(engine, binary, work,
                                        context["fixture_db"]))
        print(f"{engine:4} {'regular-file-control':26} "
              f"{'ok' if controls[-1]['answered_result'] else 'FAIL'}")

    failed = sorted(f"{r['engine']}/{r['arm']}" for r in arms
                    if not arm_is_correct(r, args.deadline))
    report = {
        "schema": REPORT_SCHEMA,
        "platform": {
            "system": platform.system(), "release": platform.release(),
            "machine": platform.machine(),
            "python": platform.python_version()},
        "binaries": {
            "go": {"implementation": "go",
                   "path": sanitized_path_value(go),
                   "sha256": sha256_file(go)},
            "rust": {"implementation": "rust",
                     "path": sanitized_path_value(rust),
                     "sha256": sha256_file(rust)}},
        "fixture_tool": {"path": sanitized_path_value(fixture),
                         "sha256": sha256_file(fixture)},
        "deadline_seconds": args.deadline,
        "arms": sorted(arms, key=lambda r: (r["engine"], r["arm"])),
        "controls": controls,
        "summary": {"arms_expected": len(ARM_NAMES) * 2, "failed": failed},
        "result": "PASS" if not failed else "FAIL",
    }
    # Verify our own report with the same rules a reviewer would apply, so
    # the harness cannot emit a report its own gate rejects.
    # Verified against the report as it will be committed: the shared writer
    # owns command/git_head/checkout_root, and this gate's own rules require
    # all three, so the copy is seeded with exactly what the writer will add.
    problems = assess_report(dict(report, **report_provenance()),
                             deadline=args.deadline)
    if problems and not failed:
        report["result"] = "FAIL"
        report["verification_problems"] = problems
    if args.json_report:
        write_committed_report(
            args.json_report, report,
            caller_paths=(("--go", args.go), ("--rust", args.rust),
                          ("--fixture", args.fixture),
                          ("--work", args.work),
                          ("--json-report", args.json_report)),
            indent=1)
    if report["result"] != "PASS":
        for problem in report.get("verification_problems", []):
            print(f"VERIFY {problem}")
        for entry in failed:
            print(f"FAILED {entry}")
        print(f"\nfifo surface gate FAILED: {len(failed)} arm(s)")
        return 1
    print(f"\nfifo surface gate PASSED: {len(ARM_NAMES)} arms x 2 engines, "
          f"git_head={report['git_head']}")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go")
    parser.add_argument("--rust")
    parser.add_argument("--fixture", "--fixture-tool",
                        dest="fixture",
                        help="v4-fixture tool, same spelling as run.py")
    parser.add_argument("--work")
    parser.add_argument("--json-report")
    parser.add_argument("--deadline", type=float, default=BLOCKING_SECONDS,
                        help="seconds within which a refusal must arrive")
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        return _self_test()
    missing = [label for label, value in (("--go", args.go),
                                          ("--rust", args.rust),
                                          ("--fixture", args.fixture),
                                          ("--work", args.work))
               if not value]
    if missing:
        parser.error(f"missing required arguments: {', '.join(missing)}")
    return live_run(args)


if __name__ == "__main__":
    sys.exit(main())
