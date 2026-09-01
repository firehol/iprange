#!/usr/bin/env python3
"""External qualification runner for the iprange v1 production API.

Standard-library Python client that drives real executables through the
released legacy CLI and the ``--jsonrpc`` stdio protocol.  The runner owns
strict client framing, declarative cases, deterministic fixtures, an
independent scalar interval oracle, and a machine-readable provenance report.
"""

import argparse
import hashlib
import json
import os
import platform as platform_module
import shutil
import subprocess
import sys
import tempfile
import threading

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from schema.engine import ValidationError  # noqa: E402
from schema import cases as case_schema  # noqa: E402
from schema import frame, methods, results  # noqa: E402
from schema import oracle  # noqa: E402

DEFAULT_CASE_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "cases")

WORK_PLACEHOLDER = "$WORK/"
CAPTURE_PLACEHOLDER = "$CAPTURE/"

# Keep subprocess setup deterministic while retaining locale, sanitizer, and
# platform variables needed by portable qualification runs.
ENV_ALLOWLIST = (
    "PATH",
    "LANG",
    "LC_ALL",
    "LC_CTYPE",
    "TZ",
    "ASAN_OPTIONS",
    "LSAN_OPTIONS",
    "MSAN_OPTIONS",
    "TSAN_OPTIONS",
    "UBSAN_OPTIONS",
    "SYSTEMROOT",
    "SYSTEMDRIVE",
    "USERPROFILE",
    "PROCESSOR_ARCHITECTURE",
    "PROCESSOR_IDENTIFIER",
)


def child_environment():
    """Return the documented subprocess environment allowlist."""

    source = os.environ
    result = {name: source[name] for name in ENV_ALLOWLIST if name in source}
    return result


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def safe_work_path(work_dir, relative, *, must_exist=False):
    """Resolve a case-relative path and reject lexical/symlink escapes."""

    if os.path.isabs(relative):
        raise ValueError(f"case path must be work-relative: {relative!r}")
    path = os.path.abspath(os.path.join(work_dir, relative))
    real_work = os.path.realpath(work_dir)
    if os.path.commonpath((path, real_work)) != real_work:
        raise ValueError(f"case path escapes the work directory: {relative!r}")
    if must_exist:
        real_path = os.path.realpath(path)
        if os.path.commonpath((real_path, real_work)) != real_work:
            raise ValueError(f"case path escapes the work directory: {relative!r}")
        if not os.path.exists(real_path):
            return path
    return path


class CaseRunner:
    """One case, one consumer service, and one private work directory."""

    def __init__(self, binary, case, work_dir, implementation, fixture_tool=None):
        self.binary = binary
        self.fixture_tool = fixture_tool
        self.case = case
        self.work_dir = os.path.realpath(work_dir)
        self.implementation = implementation
        self.captures = {}
        self.service = None
        self.service_argv = [binary, "--jsonrpc"]
        self.cursors = {}
        self.readers = {}
        self.fixture_intervals = {}
        self.pending_lookup = []
        self.oracle_checks = 0

    # ---- fixtures -------------------------------------------------
    def build_fixtures(self):
        for fixture in self.case.get("fixtures", []):
            path = safe_work_path(self.work_dir, fixture["path"])
            os.makedirs(os.path.dirname(path), exist_ok=True)
            source = fixture["source"]
            if "text" in source:
                write_text(path, source["text"])
                intervals = parse_interval_text(source["text"])
                if intervals is not None:
                    self.fixture_intervals[os.path.realpath(path)] = intervals
            elif "base64" in source:
                import base64
                data = base64.b64decode(source["base64"], validate=True)
                write_bytes(path, data)
                intervals = parse_interval_text(data.decode("utf-8", "strict"))
                if intervals is not None:
                    self.fixture_intervals[os.path.realpath(path)] = intervals
            elif "generator" in source:
                # Generated v4 files intentionally have no independent text
                # representation here; oracle checks are skipped for them.
                generate_fixture(path, source, self.fixture_tool)
            else:  # case validation makes this unreachable
                raise ValueError(f"fixture {fixture['path']!r}: no source")

    # ---- substitutions --------------------------------------------
    def substitute(self, value):
        if isinstance(value, str):
            if value.startswith(WORK_PLACEHOLDER):
                return safe_work_path(self.work_dir, value[len(WORK_PLACEHOLDER):])
            if value.startswith(CAPTURE_PLACEHOLDER):
                name = value[len(CAPTURE_PLACEHOLDER):]
                if name not in self.captures:
                    raise ValueError(f"unresolved capture {name!r}")
                return self.captures[name]
            return value
        if isinstance(value, list):
            return [self.substitute(item) for item in value]
        if isinstance(value, dict):
            return {key: self.substitute(item) for key, item in value.items()}
        return value

    # ---- expectations ---------------------------------------------
    def matches_expected(self, expected, got):
        """Match expected values exhaustively, except explicit $ignore trees.

        An expected object is partial only when one of its named members is
        explicitly ignored.  This lets cases ignore random identities while a
        complete result schema still validates every unmentioned field.
        """

        if expected == {"$ignore": True}:
            return True
        if isinstance(expected, dict):
            if not isinstance(got, dict):
                return False
            partial = any(value == {"$ignore": True} for value in expected.values())
            if not partial and set(expected) != set(got):
                return False
            return all(key in got and self.matches_expected(value, got[key])
                       for key, value in expected.items())
        if isinstance(expected, list):
            return (isinstance(got, list)
                    and len(expected) == len(got)
                    and all(self.matches_expected(value, item)
                            for value, item in zip(expected, got)))
        return expected == got

    # ---- rpc steps ------------------------------------------------
    def run_rpc_step(self, step):
        method = step["method"]
        params = self.substitute(step["params"])
        if not methods.known(method):
            raise AssertionError(f"case {self.case['name']!r}: unknown method {method}")
        try:
            case_schema.validate_rpc_request(method, params)
        except ValidationError as exc:
            raise AssertionError(
                f"case {self.case['name']!r}: invalid request params: {exc}") from exc

        request_id = f"case-{self.case['name']}"
        response = self.service.call(request_id, method, params)
        if "error" in response:
            self.check_expected_error(step, method, response["error"])
            capture_root = {
                "code": response["error"].get("code"),
                "message": response["error"].get("message"),
                "data": response["error"].get("data"),
            }
            self.process_captures(step.get("capture", []), capture_root)
            for assertion in step.get("assert_files", []):
                self.assert_file(assertion)
            return

        result = response["result"]
        try:
            results.validate_result(method, result)
        except ValidationError as exc:
            raise AssertionError(
                f"case {self.case['name']!r}: invalid result for {method}: {exc}") from exc
        self.check_protocol(method, params, result)
        self.check_output_result(method, params, result)
        if "expect_result" in step:
            expected = step["expect_result"]
            for key, exp in expected.items():
                if key == "method":
                    continue
                if key not in result:
                    raise AssertionError(
                        f"case {self.case['name']!r}: result.{key} is absent")
                got = result[key]
                if not self.matches_expected(exp, got):
                    raise AssertionError(
                        f"case {self.case['name']!r}: result.{key} expected {exp!r}, got {got!r}")
        self.process_captures(step.get("capture", []), result)
        for assertion in step.get("assert_files", []):
            self.assert_file(assertion)

    def check_expected_error(self, step, method, error):
        expected = step.get("expect_error")
        if expected is None:
            raise AssertionError(
                f"case {self.case['name']!r}: method {method} failed unexpectedly: "
                f"{error.get('code')} {error.get('message')} data={error.get('data')}")
        if error.get("code") != frame.PRODUCT_ERROR:
            raise AssertionError(
                f"case {self.case['name']!r}: product error transport code must be "
                f"{frame.PRODUCT_ERROR}, got {error.get('code')!r}: "
                f"{error.get('message')}")
        data = error.get("data")
        if not isinstance(data, dict):
            raise AssertionError(
                f"case {self.case['name']!r}: product error data must be an object")
        if data.get("code") != expected["code"]:
            raise AssertionError(
                f"case {self.case['name']!r}: expected data.code {expected['code']!r}, "
                f"got {data.get('code')!r}")
        if "outcome" in expected and data.get("outcome") != expected["outcome"]:
            raise AssertionError(
                f"case {self.case['name']!r}: expected outcome {expected['outcome']!r}, "
                f"got {data.get('outcome')!r}")

    def process_captures(self, pointers, root):
        for pointer in pointers:
            value = root
            for part in pointer.split("."):
                if not isinstance(value, dict) or part not in value:
                    raise AssertionError(
                        f"case {self.case['name']!r}: capture {pointer!r} not found")
                value = value[part]
            self.captures[pointer] = value

    def check_output_result(self, method, params, result):
        """Verify response output facts against the requested local artifact."""

        request_path = None
        if isinstance(params.get("delivery"), dict) and params["delivery"].get("mode") == "file":
            request_path = params["delivery"].get("path")
        for name in ("output", "findings_output", "report_output"):
            if isinstance(params.get(name), dict):
                request_path = params[name].get("path")
        if method == "iprange.v1.export":
            request_path = params.get("destination")

        if request_path is not None and not os.path.isabs(request_path):
            request_path = safe_work_path(self.work_dir, request_path)
        facts = result.get("output") if isinstance(result.get("output"), dict) else None
        if facts is not None:
            self.verify_output_facts(facts, request_path)
        if method == "iprange.v1.export":
            self.verify_output_facts(result, request_path)

    def verify_output_facts(self, facts, request_path):
        if request_path is not None and facts.get("path") != request_path:
            raise AssertionError(
                f"output path {facts.get('path')!r} does not match request {request_path!r}")
        path = facts.get("path")
        if not isinstance(path, str) or not path:
            raise AssertionError("output facts have no usable path")
        if not os.path.isabs(path):
            path = safe_work_path(self.work_dir, path)
        if not os.path.isfile(path):
            raise AssertionError(f"output file is missing: {path}")
        digest = sha256_file(path)
        if facts.get("sha256") != digest:
            raise AssertionError(
                f"output sha256 {facts.get('sha256')!r} does not match file digest {digest}")
        size = os.path.getsize(path)
        if facts.get("bytes") != str(size):
            raise AssertionError(
                f"output bytes {facts.get('bytes')!r} do not match file size {size}")

    # ---- protocol semantics ---------------------------------------
    def check_protocol(self, method, params, result):
        """Check cross-request contracts not expressible by one result schema."""

        if method == "iprange.v1.system.describe":
            # validate_result checks the full schema; this explicit call keeps
            # capability semantics observable at the runner layer as well.
            results.validate_system_describe(result)
        elif method == "iprange.v1.algebra.count":
            self.check_algebra_oracle(method, params, result)
        elif method == "iprange.v1.algebra.compare":
            self.check_algebra_oracle(method, params, result)
        elif method == "iprange.v1.reader.open":
            handle = result["reader"]
            self.readers[handle] = result["info"]["value_kind"]
        elif method == "iprange.v1.reader.close":
            self.readers.pop(params["reader"], None)
        elif method == "iprange.v1.reader.lookup":
            addresses = params.get("addresses", [])
            matches = result.get("matches", [])
            if len(matches) != len(addresses):
                raise AssertionError(
                    f"case {self.case['name']!r}: lookup returned {len(matches)} matches "
                    f"for {len(addresses)} addresses")
            reader_kind = self.readers.get(params["reader"])
            for index, (want, got) in enumerate(zip(addresses, matches)):
                if got.get("address") != want:
                    raise AssertionError(
                        f"case {self.case['name']!r}: lookup match[{index}] address "
                        f"{got.get('address')!r} != requested {want!r}")
                self.check_lookup_payload_kind(reader_kind, got, index)
                self.pending_lookup.append((params["reader"], reader_kind, got))
        elif method == "iprange.v1.reader.matching_feeds":
            if result.get("address") != params.get("address"):
                raise AssertionError(
                    f"case {self.case['name']!r}: matching_feeds address "
                    f"{result.get('address')!r} != requested {params.get('address')!r}")
        elif method == "iprange.v1.reader.ranges.open":
            self.cursors[result["cursor"]] = {
                "kind": "ranges",
                "reader": params["reader"],
                "view": params["view"],
                "direction": params["direction"],
                "last": None,
                "closed": False,
                "complete": False,
                "records": [],
            }
        elif method == "iprange.v1.reader.ranges.next":
            cursor = self.require_cursor(params["cursor"], "ranges.next")
            forward = cursor["direction"] == "forward"
            for record in result.get("records", []):
                self.check_range_record_shape(cursor["view"], record)
                key = (ip_int(record["from"]), ip_int(record["to"]))
                if cursor["last"] is not None:
                    prev, now = (cursor["last"], key) if forward else (key, cursor["last"])
                    if now < prev:
                        raise AssertionError(
                            f"case {self.case['name']!r}: ranges records out of "
                            f"{'ascending' if forward else 'descending'} order: "
                            f"{key} after {cursor['last']}")
                cursor["last"] = key
                cursor["records"].extend(self.oracle_record(cursor, record))
            if result.get("done"):
                cursor["closed"] = True
                cursor["complete"] = True
        elif method == "iprange.v1.reader.ranges.close":
            cursor = self.cursors.get(params["cursor"])
            if cursor is not None:
                cursor["closed"] = True
        elif method == "iprange.v1.reader.feeds.open":
            self.cursors[result["cursor"]] = {
                "kind": "feeds", "last": None, "closed": False,
            }
        elif method == "iprange.v1.reader.feeds.next":
            # Feed rows follow feed-catalog order (insertion order), which
            # is not a lexical order. The case's strict expect_result rows
            # are the authoritative order assertion; no extra ordering
            # proxy exists here.
            cursor = self.require_cursor(params["cursor"], "feeds.next")
            for row in result.get("feeds", []):
                if not isinstance(row, dict) or not isinstance(row.get("name"), str):
                    raise AssertionError(
                        f"case {self.case['name']!r}: feeds rows must be {{name}} objects")
            if result.get("done"):
                cursor["closed"] = True
        elif method == "iprange.v1.reader.feeds.close":
            cursor = self.cursors.get(params["cursor"])
            if cursor is not None:
                cursor["closed"] = True

    @staticmethod
    def check_lookup_payload_kind(reader_kind, fact, index):
        """Bind a present lookup payload to the opened reader's value kind."""

        if reader_kind is None or fact.get("present") is not True:
            return
        keys = set(fact) - {"address", "present"}
        payloads = {
            "direct": [{"value"}],
            "membership": [{"feeds"}],
            "structured": [{"asn", "country_id", "state_id", "city_id",
                             "location", "threat_feeds"}],
        }
        if keys not in payloads[reader_kind]:
            raise AssertionError(
                f"case lookup match[{index}]: reader kind {reader_kind!r} "
                f"returned payload keys {sorted(keys)!r}")

    def check_algebra_oracle(self, method, params, result):
        """Check algebra against text fixtures with scalar interval models."""

        sources = []
        for source in params.get("sources", []):
            path = source.get("source", {}).get("path")
            intervals = self.fixture_intervals.get(
                os.path.realpath(path) if isinstance(path, str) else path)
            if intervals is None:
                return
            sources.append(intervals)
        if not sources:
            return

        if method == "iprange.v1.algebra.count":
            if params.get("selection") != {"mode": "all"}:
                return
            _, expected = oracle.algebra_count("union", sources)
            actual = result.get("cardinality")
            if actual != str(expected):
                raise AssertionError(
                    f"case {self.case['name']!r}: algebra.count oracle expected "
                    f"{expected}, got {actual!r}")
            self.oracle_checks += 1
            return

        if params.get("left") != {"mode": "all"} or params.get("right") != {"mode": "all"}:
            return
        expected = oracle.compare(union_all(sources), union_all(sources))
        report = result.get("report", {})
        for key, value in expected.items():
            actual = report.get(key)
            if actual != (str(value) if type(value) is int else value):
                raise AssertionError(
                    f"case {self.case['name']!r}: algebra.compare oracle "
                    f"{key} expected {value!r}, got {actual!r}")
        self.oracle_checks += 1

    def require_cursor(self, handle, operation):
        cursor = self.cursors.get(handle)
        if cursor is None:
            raise AssertionError(
                f"case {self.case['name']!r}: {operation} on unknown cursor {handle!r}")
        if cursor["closed"]:
            raise AssertionError(
                f"case {self.case['name']!r}: {operation} after done/close")
        return cursor

    @staticmethod
    def check_range_record_shape(view, record):
        kind = view["kind"]
        if kind == "direct" and not isinstance(record.get("value"), int):
            raise AssertionError("direct range record must carry exactly a u32 value")
        if kind == "structured" and not isinstance(record.get("value"), dict):
            raise AssertionError("structured range record must carry its complete value object")
        if kind == "feed" and "value" in record:
            raise AssertionError("feed range record must not carry a value")
        if ip_int(record["from"]) > ip_int(record["to"]):
            raise AssertionError("range record endpoints are reversed")

    @staticmethod
    def oracle_record(cursor, record):
        start, end = ip_int(record["from"]), ip_int(record["to"])
        if cursor["view"]["kind"] == "feed":
            value = cursor["view"]["feed"]
        elif cursor["view"]["kind"] == "direct":
            value = record.get("value")
        else:
            value = record.get("value")
        return [oracle.Interval(start, end, value)]

    # ---- independent scalar oracle --------------------------------
    def finish_oracle(self):
        for handle, kind, actual in self.pending_lookup:
            if kind == "direct":
                state = self.completed_ranges(handle, "direct")
                if state:
                    expected = oracle.lookup_fact(oracle.normalize_intervals(state), ip_int(actual["address"]))
                    self.check_oracle_lookup(actual, expected)
            elif kind == "structured":
                state = self.completed_ranges(handle, "structured")
                if state:
                    expected = oracle.lookup_fact(oracle.normalize_intervals(state), ip_int(actual["address"]))
                    self.check_oracle_lookup(actual, expected)
            elif kind == "membership":
                for feed, state in self.completed_feed_ranges(handle).items():
                    expected_present = oracle.lookup(state, ip_int(actual["address"])) == feed
                    actual_present = feed in actual.get("feeds", [])
                    if expected_present != actual_present:
                        raise AssertionError(
                            f"case {self.case['name']!r}: oracle mismatch for {actual['address']} "
                            f"and feed {feed!r}: expected membership {expected_present}, "
                            f"got {actual_present}")
                    self.oracle_checks += 1

    def completed_ranges(self, handle, view_kind):
        intervals = []
        for cursor in self.cursors.values():
            if (cursor.get("kind") == "ranges" and cursor.get("reader") == handle
                    and cursor.get("view", {}).get("kind") == view_kind
                    and cursor.get("complete")):
                intervals.extend(cursor["records"])
        return intervals

    def completed_feed_ranges(self, handle):
        feeds = {}
        for cursor in self.cursors.values():
            view = cursor.get("view", {})
            if (cursor.get("kind") == "ranges" and cursor.get("reader") == handle
                    and view.get("kind") == "feed" and cursor.get("complete")):
                feeds[view["feed"]] = cursor["records"]
        return feeds

    def check_oracle_lookup(self, actual, expected):
        if actual.get("present") != expected.get("present"):
            raise AssertionError(
                f"case {self.case['name']!r}: oracle presence mismatch for "
                f"{actual.get('address')!r}: expected {expected.get('present')}, "
                f"got {actual.get('present')}")
        if expected.get("present"):
            expected_value = expected.get("value")
            if isinstance(expected_value, dict):
                actual_value = {key: actual.get(key) for key in expected_value}
            else:
                actual_value = actual.get("value")
            if actual_value != expected_value:
                raise AssertionError(
                    f"case {self.case['name']!r}: oracle value mismatch for "
                    f"{actual.get('address')!r}: expected {expected_value!r}, "
                    f"got {actual_value!r}")
        self.oracle_checks += 1

    # ---- legacy steps ---------------------------------------------
    def run_legacy_step(self, step):
        argv = self.substitute(step["argv"])
        stdin_data = None
        if "stdin_fixture" in step:
            fixture = safe_work_path(self.work_dir, step["stdin_fixture"], must_exist=True)
            with open(fixture, "rb") as stream:
                stdin_data = stream.read()
        try:
            proc = subprocess.run(
                [self.binary] + argv,
                cwd=self.work_dir,
                input=stdin_data,
                capture_output=True,
                timeout=300,
                env=child_environment(),
            )
        except subprocess.TimeoutExpired as exc:
            raise AssertionError(f"case {self.case['name']!r}: legacy command timed out") from exc
        if proc.returncode != step["exit_status"]:
            raise AssertionError(
                f"case {self.case['name']!r}: exit {proc.returncode}, "
                f"expected {step['exit_status']}\n"
                f"stdout={proc.stdout[:400]!r}\nstderr={proc.stderr[:400]!r}")
        self.match_stream("stdout", proc.stdout, step["stdout"])
        self.match_stream("stderr", proc.stderr, step["stderr"])
        for assertion in step.get("assert_files", []):
            self.assert_file(assertion)

    def match_stream(self, name, data, expectation):
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError as exc:
            raise AssertionError(f"case {self.case['name']!r}: {name} is not UTF-8") from exc
        if "$exact" in expectation:
            if text != expectation["$exact"]:
                raise AssertionError(
                    f"case {self.case['name']!r}: {name} mismatch\n"
                    f"expected {expectation['$exact']!r}\ngot {text[:400]!r}")
        elif expectation["$contains"].encode("utf-8") not in data:
            raise AssertionError(
                f"case {self.case['name']!r}: {name} missing {expectation['$contains']!r}")

    # ---- filesystem assertions ------------------------------------
    def assert_file(self, assertion):
        path = safe_work_path(self.work_dir, assertion["path"], must_exist=True)
        if not os.path.isfile(path):
            raise AssertionError(
                f"case {self.case['name']!r}: assertion target is not a file {assertion['path']}")
        if "sha256" in assertion and sha256_file(path) != assertion["sha256"]:
            raise AssertionError(
                f"case {self.case['name']!r}: {assertion['path']} sha256 mismatch")
        if "equals_fixture" in assertion:
            other = safe_work_path(self.work_dir, assertion["equals_fixture"], must_exist=True)
            if open(path, "rb").read() != open(other, "rb").read():
                raise AssertionError(
                    f"case {self.case['name']!r}: {assertion['path']} differs from "
                    f"{assertion['equals_fixture']}")

    # ---- full case -------------------------------------------------
    def run(self):
        self.build_fixtures()
        for step in self.case["steps"]:
            if step["kind"] == "rpc":
                self.run_rpc_step(step)
            else:
                self.run_legacy_step(step)
        for assertion in self.case.get("assertions", {}).get("files", []):
            self.assert_file(assertion)
        self.finish_oracle()


class JsonRpcService:
    """Strict JSON-RPC stdio client over one persistent subprocess."""

    def __init__(self, argv, implementation, *, cwd=None):
        self.argv = list(argv)
        self.implementation = implementation
        self.proc = subprocess.Popen(
            self.argv,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=child_environment(),
            cwd=cwd,
        )
        self.lock = threading.Lock()
        self.stderr_tail = []

        def _drain():
            for raw in self.proc.stderr:
                self.stderr_tail.append(raw.decode("utf-8", "replace"))
                if len(self.stderr_tail) > 20:
                    self.stderr_tail.pop(0)

        self.drainer = threading.Thread(target=_drain, daemon=True)
        self.drainer.start()

    def call(self, request_id, method, params):
        with self.lock:
            request = {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}
            wire = json.dumps(request, separators=(",", ":"), ensure_ascii=False)
            if len(wire.encode("utf-8")) > frame.INPUT_FRAME_LIMIT:
                raise AssertionError("client request frame over limit")
            try:
                self.proc.stdin.write(wire.encode("utf-8") + b"\n")
                self.proc.stdin.flush()
            except (BrokenPipeError, OSError) as exc:
                raise AssertionError(
                    f"service closed stdin: {''.join(self.stderr_tail[-5:])}") from exc
            line = self.proc.stdout.readline()
            if not line:
                raise AssertionError(
                    f"service closed stdout; stderr={''.join(self.stderr_tail[-5:])}")
            try:
                return self.decode_response_line(line, request_id)
            except (frame.FrameError, UnicodeDecodeError) as exc:
                raise AssertionError(str(exc)) from exc

    def decode_response_line(self, line, request_id):
        if not line.endswith(b"\n"):
            raise frame.FrameError(frame.STD_PARSE_ERROR, "response frame is not LF terminated")
        encoded = line[:-1]
        if encoded.endswith(b"\r"):
            raise frame.FrameError(frame.STD_PARSE_ERROR, "response uses CRLF instead of LF")
        text = encoded.decode("utf-8")
        response = frame.decode_response(text)
        if len(text.encode("utf-8")) > frame.RESPONSE_OBJECT_LIMIT:
            raise frame.FrameError(
                frame.TRANSPORT_FRAME_TOO_LARGE, "response object exceeds 65000 bytes")
        if response.get("id") != request_id:
            raise frame.FrameError(
                frame.STD_INVALID_REQUEST,
                f"response id {response.get('id')!r} != request id {request_id!r}")
        return response

    def close(self):
        """Close stdin and wait for this owned subprocess to terminate."""

        try:
            if self.proc.stdin and not self.proc.stdin.closed:
                self.proc.stdin.close()
            self.proc.wait(timeout=30)
        except Exception:
            self.proc.kill()
            self.proc.wait(timeout=5)


def parse_interval_text(text):
    """Parse a plain range/CIDR/single-IP fixture, or return None.

    Unsupported legacy text features are not errors here; the oracle simply
    remains silent for fixtures this independent scalar model cannot express.
    """

    import ipaddress

    intervals = []
    try:
        for raw_line in text.splitlines():
            line = raw_line.split("#", 1)[0].split(";", 1)[0].strip()
            if not line:
                continue
            if "-" in line:
                left, right = line.split("-", 1)
                start = int(ipaddress.ip_address(left.strip()))
                end = int(ipaddress.ip_address(right.strip()))
            elif "/" in line:
                network = ipaddress.ip_network(line, strict=False)
                start, end = int(network.network_address), int(network.broadcast_address)
            else:
                start = end = int(ipaddress.ip_address(line))
            if start > end:
                return None
            intervals.append((start, end))
    except (ValueError, UnicodeError):
        return None
    return intervals


def union_all(interval_lists):
    combined = [interval for intervals in interval_lists for interval in intervals]
    return oracle.union([combined])


def ip_int(address):
    """Numeric value of an IPv4/IPv6 address for order comparisons."""

    import ipaddress
    return int(ipaddress.ip_address(address))


def write_text(path, data):
    with open(path, "w", encoding="utf-8", newline="") as stream:
        stream.write(data)


def write_bytes(path, data):
    with open(path, "wb") as stream:
        stream.write(data)


def generate_fixture(path, source, fixture_tool):
    generator = source["generator"]
    seed = source.get("seed", 0)
    if generator == "ipv4_random_ranges":
        import ipaddress
        import random
        rng = random.Random(seed)
        lines = []
        for _ in range(1024):
            start = rng.randrange(0, 2**32)
            end = min(start + rng.randrange(0, 4096), 2**32 - 1)
            lines.append(f"{ipaddress.IPv4Address(start)}-{ipaddress.IPv4Address(end)}\n")
        write_text(path, "".join(lines))
        return
    if generator == "ipv6_random_ranges":
        import ipaddress
        import random
        rng = random.Random(seed)
        lines = []
        for _ in range(512):
            start = rng.getrandbits(128)
            end = min(start + rng.getrandbits(64), 2**128 - 1)
            lines.append(f"{ipaddress.IPv6Address(start)}-{ipaddress.IPv6Address(end)}\n")
        write_text(path, "".join(lines))
        return
    if generator != "v4_fixture":
        raise ValueError(f"unknown fixture generator {generator!r}")
    if fixture_tool is None:
        raise ValueError("case uses v4_fixture but --fixture-tool was not supplied")
    kinds = {0: "direct-v4", 1: "membership-v4", 2: "structured-v4"}
    try:
        kind = kinds[seed]
    except KeyError as exc:
        raise ValueError(
            f"v4_fixture seed {seed!r} has no fixed kind; expected one of {sorted(kinds)}") from exc
    try:
        proc = subprocess.run(
            [fixture_tool, kind, path],
            capture_output=True,
            timeout=300,
            env=child_environment(),
        )
    except subprocess.TimeoutExpired as exc:
        raise ValueError("v4_fixture timed out") from exc
    if proc.returncode != 0:
        detail = proc.stderr.decode("utf-8", "replace").strip()
        raise ValueError(f"v4_fixture failed with exit {proc.returncode}: {detail}")


def _self_test():
    """Exercise runner-side protocol helpers without spawning a service."""

    import tempfile

    with tempfile.TemporaryDirectory() as work:
        runner = CaseRunner(None, {
            "schema": "iprange-cli-case-v1",
            "name": "self-test",
            "fixtures": [],
            "steps": [],
        }, work, "test")
        runner.readers["r"] = "membership"
        runner.check_lookup_payload_kind("membership", {
            "address": "192.0.2.1", "present": False,
        }, 0)
        runner.check_lookup_payload_kind("membership", {
            "address": "192.0.2.1", "present": True, "feeds": ["feed-a"],
        }, 1)
        try:
            runner.check_lookup_payload_kind("membership", {
                "address": "192.0.2.1", "present": True, "value": 10,
            }, 2)
        except AssertionError:
            pass
        else:
            raise AssertionError("membership reader accepted a direct payload")

        fixture = os.path.join(work, "ranges.txt")
        write_text(fixture, "192.0.2.0-192.0.2.9\n198.51.100.0/30\n")
        intervals = parse_interval_text(open(fixture, encoding="utf-8").read())
        runner.fixture_intervals[os.path.realpath(fixture)] = intervals
        source = {"source": {"path": fixture, "mode": "immutable"},
                  "scope": {"mode": "all"},
                  "membership_query_budget": {"max_heap_bytes": "1"}}
        params = {"sources": [source], "selection": {"mode": "all"},
                  "algebra_budget": {"max_heap_bytes": "1", "max_sources": 1}}
        runner.check_algebra_oracle("iprange.v1.algebra.count", params, {
            "method": "iprange.v1.algebra.count", "cardinality": "14",
        })
        assert runner.oracle_checks == 1
        compare_params = {
            "sources": [source],
            "left": {"mode": "all"},
            "right": {"mode": "all"},
            "algebra_budget": {"max_heap_bytes": "1", "max_sources": 1},
        }
        runner.check_algebra_oracle("iprange.v1.algebra.compare", compare_params, {
            "method": "iprange.v1.algebra.compare",
            "report": {
                "left_addresses": "0", "right_addresses": "0",
                "overlap_addresses": "14", "left_only_addresses": "0",
                "right_only_addresses": "0", "union_addresses": "14",
                "equal": True,
            },
        })
        assert runner.oracle_checks == 2


CAPABILITIES_CACHE = {}


def describe_capabilities(binary):
    """Validate and cache one binary's system.describe capability result."""

    identity = os.path.realpath(binary)
    cache_key = (identity, sha256_file(identity))
    if cache_key in CAPABILITIES_CACHE:
        return CAPABILITIES_CACHE[cache_key]
    record = {"path": binary, "sha256": cache_key[1], "methods": [], "available": False}
    try:
        service = JsonRpcService([binary, "--jsonrpc"], "probe")
        try:
            response = service.call(
                "capability-probe", "iprange.v1.system.describe", {})
            if "result" in response:
                results.validate_result(
                    "iprange.v1.system.describe", response["result"])
                record["methods"] = list(response["result"]["methods"])
                record["available"] = True
                record["result"] = response["result"]
        finally:
            service.close()
    except (AssertionError, OSError):
        # Legacy-only executables do not expose --jsonrpc.  A returned describe
        # result that fails the strict schema propagates ValidationError.
        record["methods"] = []
        record["available"] = False
    CAPABILITIES_CACHE[cache_key] = record
    return record


def load_cases(case_dir):
    cases = []
    names = set()
    for name in sorted(os.listdir(case_dir)):
        if not name.endswith(".json"):
            continue
        with open(os.path.join(case_dir, name), encoding="utf-8") as stream:
            case = json.load(stream)
        try:
            case_schema.validate_case(case)
        except ValidationError as exc:
            raise ValueError(f"{name}: {exc}") from exc
        if case["name"] in names:
            raise ValueError(f"duplicate case name {case['name']!r}")
        names.add(case["name"])
        cases.append(case)
    return cases


def validate_explicit_work_dir(path):
    if not os.path.isabs(path):
        raise ValueError("--work-dir must be absolute")
    real = os.path.realpath(path)
    if not os.path.isdir(real):
        raise ValueError("--work-dir must be an existing directory")
    if os.listdir(real):
        raise ValueError("--work-dir must be empty at the start of the run")
    return real


def binary_record(path):
    return {
        "path": path,
        "sha256": sha256_file(path),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--c", dest="c_binary", metavar="PATH")
    parser.add_argument("--fixture-tool", metavar="PATH",
                        help="absolute v4-fixture producer executable")
    parser.add_argument("--rust", dest="rust_binary", metavar="PATH")
    parser.add_argument("--go", dest="go_binary", metavar="PATH")
    parser.add_argument("--matrix", default="all",
                        choices=["all", "c", "rust", "go", "rust_to_go", "go_to_rust"])
    parser.add_argument("--cases", default=DEFAULT_CASE_DIR)
    parser.add_argument("--work-dir", metavar="DIR",
                        help="absolute empty directory kept after the run")
    parser.add_argument("--filter", metavar="NAME")
    parser.add_argument("--allow-skips", action="store_true",
                        help="permit reported capability or unavailable-matrix skips")
    parser.add_argument("--json-report", metavar="PATH")
    args = parser.parse_args()

    try:
        if args.work_dir is not None:
            args.work_dir = validate_explicit_work_dir(args.work_dir)
    except ValueError as exc:
        parser.error(str(exc))

    def executable(value, label, *, require_absolute=True):
        if require_absolute and not os.path.isabs(value):
            parser.error(f"{label} is not an absolute executable file: {value}")
        if not os.path.isfile(value) or not os.access(value, os.X_OK):
            parser.error(f"{label} is not an absolute executable file: {value}")
        return os.path.realpath(value)

    fixture_tool = None
    if args.fixture_tool:
        fixture_tool = executable(args.fixture_tool, "fixture tool")

    binaries = {}
    for key, attr in (("c", "c_binary"), ("rust", "rust_binary"), ("go", "go_binary")):
        path = getattr(args, attr)
        if path:
            binaries[key] = executable(path, f"{key} binary")

    try:
        use_cases = load_cases(args.cases)
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        parser.error(str(exc))
    if args.filter:
        use_cases = [case for case in use_cases if args.filter in case["name"]]
    if not use_cases:
        parser.error("no cases selected")

    try:
        capabilities = {key: describe_capabilities(path) for key, path in binaries.items()}
    except ValidationError as exc:
        parser.error(f"invalid capability advertisement: {exc}")
    report = {
        "schema": "iprange-cli-report-v2",
        "command": sys.argv,
        "platform": {
            "system": platform_module.system(),
            "release": platform_module.release(),
            "machine": platform_module.machine(),
            "python": platform_module.python_version(),
        },
        "environment_allowlist": list(ENV_ALLOWLIST),
        "binaries": {key: dict(value) for key, value in capabilities.items()},
        "fixture_tool": binary_record(fixture_tool) if fixture_tool else None,
        "cases": [],
        "passed": 0,
        "failed": 0,
        "skipped": 0,
        "oracle_checks": 0,
    }

    def record_skip(name, matrix, reason):
        report["skipped"] += 1
        report["cases"].append({
            "name": name, "matrix": matrix, "status": "SKIP", "reason": reason,
        })
        print(f"SKIP {name} [{matrix}]: {reason}")

    def run_one(case, producer, consumer):
        consume_bin = binaries[consumer] if consumer else binaries[producer]
        work = args.work_dir
        owns_work = False
        if work is None:
            work = tempfile.mkdtemp(prefix="iprange-cli-")
            owns_work = True
        matrix = producer if consumer is None else f"{producer}->{consumer}"
        label = f"{case['name']} [{matrix}]"
        runner = None
        try:
            runner = CaseRunner(consume_bin, case, work, producer, fixture_tool)
            if any(step["kind"] == "rpc" for step in case["steps"]):
                runner.service = JsonRpcService(runner.service_argv, producer, cwd=runner.work_dir)
            runner.run()
            report["passed"] += 1
            report["oracle_checks"] += runner.oracle_checks
            report["cases"].append({
                "name": case["name"], "matrix": matrix, "status": "PASS",
                "oracle_checks": runner.oracle_checks,
            })
            print(f"PASS {label} (oracle={runner.oracle_checks})")
        except (AssertionError, ValueError, ValidationError, OSError) as exc:
            report["failed"] += 1
            report["cases"].append({
                "name": case["name"], "matrix": matrix, "status": "FAIL",
                "error": str(exc),
            })
            print(f"FAIL {label}: {exc}")
        finally:
            if runner is not None and runner.service is not None:
                runner.service.close()
            if owns_work:
                shutil.rmtree(work, ignore_errors=True)

    matrix = {
        "c": (("c", None),),
        "rust": (("rust", None),),
        "go": (("go", None),),
        "rust_to_go": (("rust", "go"),),
        "go_to_rust": (("go", "rust"),),
        "all": (("c", None), ("rust", None), ("go", None),
                ("rust", "go"), ("go", "rust")),
    }[args.matrix]

    for producer, consumer in matrix:
        direction = producer if consumer is None else f"{producer}->{consumer}"
        missing = [key for key in (producer, consumer) if key is not None and key not in binaries]
        if missing:
            record_skip("matrix", direction, f"missing binary: {', '.join(missing)}")
            continue
        mixed = consumer is not None
        if mixed and fixture_tool is None:
            record_skip("matrix", direction, "mixed producer requires --fixture-tool")
            continue
        capability_key = consumer if consumer is not None else producer
        for case in use_cases:
            required = case.get("requires")
            if required and required not in capabilities[capability_key]["methods"]:
                record_skip(case["name"], direction,
                            f"requires unadvertised method {required}")
                continue
            run_one(case, producer, consumer)

    print(
        f"\n{report['passed']} passed, {report['failed']} failed, "
        f"{report['skipped']} skipped; oracle checks={report['oracle_checks']}")
    if args.json_report:
        with open(args.json_report, "w", encoding="utf-8") as stream:
            json.dump(report, stream, indent=2, sort_keys=True)
            stream.write("\n")
    if report["failed"]:
        return 1
    if report["skipped"] and not args.allow_skips:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
