#!/usr/bin/env python3
"""External qualification runner for the iprange v1 production API.

Standard-library Python client that drives the real `iprange`
executables (Rust, Go, and/or the C legacy oracle) through the released
legacy CLI and the `--jsonrpc` stdio protocol. It imports no SDK, has no
test method in the production surface, and validates every request and
response against the strict schemas in v4/cli/schema/.

Usage:
  nice python3 v4/cli/run.py --rust /ABS/RUST_IPRANGE --go /ABS/GO_IPRANGE \\
      --c /ABS/C_IPRANGE --matrix all
"""
import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from schema.engine import ValidationError  # noqa: E402
from schema import frame, methods, results, cases as case_schema  # noqa: E402

DEFAULT_CASE_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "cases")
DEFAULT_GOLDEN_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "golden")

WORK_PLACEHOLDER = "$WORK/"
CAPTURE_PLACEHOLDER = "$CAPTURE/"


class CaseRunner:
    """One case, one binary, one private work directory."""

    def __init__(self, binary, case, work_dir, implementation, fixture_tool=None):
        self.binary = binary
        self.fixture_tool = fixture_tool
        self.case = case
        self.work_dir = work_dir
        self.implementation = implementation
        self.captures = {}
        self.service = None
        # Command that serves --jsonrpc. Production binaries are launched
        # as [binary, "--jsonrpc"]; the sensitivity gate injects a fake
        # server command whose argv[0] is the interpreter.
        self.service_argv = [binary, "--jsonrpc"]
        # Protocol state the runner verifies: cursor handle -> view info.
        self.cursors = {}

    # ---- fixtures -------------------------------------------------
    def build_fixtures(self):
        for fixture in self.case.get("fixtures", []):
            path = os.path.join(self.work_dir, fixture["path"])
            os.makedirs(os.path.dirname(path), exist_ok=True)
            source = fixture["source"]
            if "text" in source:
                mode = "w"
                data = source["text"]
                write = lambda: write_text(path, data, mode)  # noqa: E731
            elif "base64" in source:
                import base64
                data = base64.b64decode(source["base64"], validate=True)
                write = lambda: write_bytes(path, data)  # noqa: E731
            elif "generator" in source:
                write = lambda: generate_fixture(
                    path, source, self.fixture_tool, self.substitute)  # noqa: E731
            else:
                raise ValueError(f"fixture {fixture['path']!r}: no source")
            write()

    # ---- substitutions --------------------------------------------
    def substitute(self, value):
        if isinstance(value, str):
            if value.startswith(WORK_PLACEHOLDER):
                return os.path.join(self.work_dir, value[len(WORK_PLACEHOLDER):])
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

    def matches_expected(self, expected, got):
        if expected == {"$ignore": True}:
            return True
        if callable(expected):
            expected(expected, got)
            return True
        if isinstance(expected, dict):
            return isinstance(got, dict) and all(
                key in got and self.matches_expected(value, got[key])
                for key, value in expected.items())
        if isinstance(expected, list):
            return isinstance(got, list) and len(expected) == len(got) and all(
                self.matches_expected(value, item)
                for value, item in zip(expected, got))
        return expected == got

    # ---- rpc steps ------------------------------------------------
    def run_rpc_step(self, step):
        method = step["method"]
        params = self.substitute(step["params"])
        if not methods.known(method):
            raise AssertionError(f"case {self.case['name']!r}: unknown method {method}")
        try:
            methods.validate_params(method, params)
        except ValidationError as exc:
            raise AssertionError(f"case {self.case['name']!r}: invalid request params: {exc}") from exc

        request_id = f"case-{self.case['name']}"
        response = self.service.call(request_id, method, params)
        if "error" in response:
            err = response["error"]
            expected = step.get("expect_error")
            if expected is None:
                raise AssertionError(
                    f"case {self.case['name']!r}: method {method} failed: "
                    f"{err.get('code')} {err.get('message')} data={err.get('data')}")
            data = err.get("data") or {}
            if expected.get("code") is not None and data.get("code") != expected["code"]:
                raise AssertionError(
                    f"case {self.case['name']!r}: expected data.code {expected['code']}, "
                    f"got {data.get('code')}")
            if expected.get("outcome") is not None and data.get("outcome") != expected["outcome"]:
                raise AssertionError(
                    f"case {self.case['name']!r}: expected outcome {expected['outcome']}, "
                    f"got {data.get('outcome')}")
            return
        result = response["result"]
        try:
            results.validate_result(method, result)
        except ValidationError as exc:
            raise AssertionError(
                f"case {self.case['name']!r}: invalid result for {method}: {exc}") from exc
        self.check_protocol(method, params, result)
        if "expect_result" in step:
            expected = step["expect_result"]
            for key, exp in expected.items():
                if key == "method":
                    continue
                got = result.get(key)
                if not self.matches_expected(exp, got):
                    raise AssertionError(
                        f"case {self.case['name']!r}: result.{key} expected {exp!r}, got {got!r}")
        for pointer in step.get("capture", []):
            value = result
            for part in pointer.split("."):
                if not isinstance(value, dict) or part not in value:
                    raise AssertionError(
                        f"case {self.case['name']!r}: capture {pointer!r} not found")
                value = value[part]
            self.captures[pointer] = value
        for assertion in step.get("assert_files", []):
            self.assert_file(assertion)

    # ---- protocol semantics ---------------------------------------
    def check_protocol(self, method, params, result):
        """Verify cross-request ordering and cursor lifecycle contracts
        that per-result schemas cannot express."""
        if method == "iprange.v1.reader.lookup":
            addresses = params.get("addresses", [])
            matches = result.get("matches", [])
            if len(matches) != len(addresses):
                raise AssertionError(
                    f"case {self.case['name']!r}: lookup returned {len(matches)} matches "
                    f"for {len(addresses)} addresses")
            for index, (want, got) in enumerate(zip(addresses, matches)):
                if got.get("address") != want:
                    raise AssertionError(
                        f"case {self.case['name']!r}: lookup match[{index}] address "
                        f"{got.get('address')!r} != requested {want!r}")
        elif method == "iprange.v1.reader.matching_feeds":
            if result.get("address") != params.get("address"):
                raise AssertionError(
                    f"case {self.case['name']!r}: matching_feeds address "
                    f"{result.get('address')!r} != requested {params.get('address')!r}")
        elif method == "iprange.v1.reader.ranges.open":
            self.cursors[result["cursor"]] = {
                "kind": "ranges",
                "direction": params["direction"],
                "last": None,
                "closed": False,
            }
        elif method == "iprange.v1.reader.ranges.next":
            cursor = self.cursors.get(params["cursor"])
            if cursor is None:
                raise AssertionError(
                    f"case {self.case['name']!r}: ranges.next on unknown cursor {params['cursor']!r}")
            if cursor["closed"]:
                raise AssertionError(
                    f"case {self.case['name']!r}: ranges.next after done/close")
            forward = cursor["direction"] == "forward"
            for record in result.get("records", []):
                key = (ip_int(record["from"]), ip_int(record["to"]))
                if cursor["last"] is not None:
                    prev, now = (cursor["last"], key) if forward else (key, cursor["last"])
                    if now < prev:
                        raise AssertionError(
                            f"case {self.case['name']!r}: ranges records out of "
                            f"{'ascending' if forward else 'descending'} order: "
                            f"{key} after {cursor['last']}")
                cursor["last"] = key
            if result.get("done"):
                cursor["closed"] = True
        elif method == "iprange.v1.reader.ranges.close":
            cursor = self.cursors.get(params["cursor"])
            if cursor is not None:
                cursor["closed"] = True
        elif method == "iprange.v1.reader.feeds.open":
            self.cursors[result["cursor"]] = {
                "kind": "feeds", "last": None, "closed": False,
            }
        elif method == "iprange.v1.reader.feeds.next":
            cursor = self.cursors.get(params["cursor"])
            if cursor is None:
                raise AssertionError(
                    f"case {self.case['name']!r}: feeds.next on unknown cursor {params['cursor']!r}")
            if cursor["closed"]:
                raise AssertionError(
                    f"case {self.case['name']!r}: feeds.next after done/close")
            for row in result.get("feeds", []):
                name = row["name"]
                if cursor["last"] is not None and name < cursor["last"]:
                    raise AssertionError(
                        f"case {self.case['name']!r}: feeds rows out of catalog order: "
                        f"{name!r} after {cursor['last']!r}")
                cursor["last"] = name
            if result.get("done"):
                cursor["closed"] = True
        elif method == "iprange.v1.reader.feeds.close":
            cursor = self.cursors.get(params["cursor"])
            if cursor is not None:
                cursor["closed"] = True

    # ---- legacy steps ---------------------------------------------
    def run_legacy_step(self, step):
        argv = self.substitute(step["argv"])
        stdin_fixture = step.get("stdin_fixture")
        stdin_data = None
        if stdin_fixture:
            stdin_data = open(os.path.join(self.work_dir, stdin_fixture), "rb").read()
        proc = subprocess.run(
            [self.binary] + argv,
            input=stdin_data,
            capture_output=True,
            timeout=300,
        )
        if "exit_status" in step and proc.returncode != step["exit_status"]:
            raise AssertionError(
                f"case {self.case['name']!r}: exit {proc.returncode}, expected {step['exit_status']}\n"
                f"stdout={proc.stdout[:400]!r}\nstderr={proc.stderr[:400]!r}")
        for key, expectation in step.get("stdout", {}).items():
            self.match_stream("stdout", proc.stdout, expectation)
        for key, expectation in step.get("stderr", {}).items():
            self.match_stream("stderr", proc.stderr, expectation)
        for assertion in step.get("assert_files", []):
            self.assert_file(assertion)

    def match_stream(self, name, data, expectation):
        if "$exact" in expectation:
            if data.decode("utf-8", "replace") != expectation["$exact"]:
                raise AssertionError(
                    f"case {self.case['name']!r}: {name} mismatch\n"
                    f"expected {expectation['$exact']!r}\ngot {data[:400]!r}")
        elif "$contains" in expectation:
            if expectation["$contains"].encode() not in data:
                raise AssertionError(
                    f"case {self.case['name']!r}: {name} missing {expectation['$contains']!r}")

    # ---- filesystem assertions ------------------------------------
    def assert_file(self, assertion):
        path = os.path.join(self.work_dir, assertion["path"])
        if not os.path.exists(path):
            raise AssertionError(f"case {self.case['name']!r}: missing file {assertion['path']}")
        if "sha256" in assertion:
            digest = hashlib.sha256(open(path, "rb").read()).hexdigest()
            if digest != assertion["sha256"]:
                raise AssertionError(
                    f"case {self.case['name']!r}: {assertion['path']} sha256 {digest}, "
                    f"expected {assertion['sha256']}")
        if "equals_fixture" in assertion:
            other = os.path.join(self.work_dir, assertion["equals_fixture"])
            if open(path, "rb").read() != open(other, "rb").read():
                raise AssertionError(
                    f"case {self.case['name']!r}: {assertion['path']} differs from "
                    f"{assertion['equals_fixture']}")

    # ---- full case -------------------------------------------------
    def run(self):
        self.build_fixtures()
        for step in self.case["steps"]:
            kind = step["kind"]
            if kind == "rpc":
                self.run_rpc_step(step)
            elif kind == "legacy":
                self.run_legacy_step(step)
            else:
                raise AssertionError(f"unknown step kind {kind!r}")


class JsonRpcService:
    """Strict JSON-RPC stdio client over one persistent subprocess."""

    def __init__(self, argv, implementation):
        self.argv = list(argv)
        self.implementation = implementation
        self.proc = subprocess.Popen(
            self.argv,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
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
            wire = json.dumps(request, separators=(",", ":"), ensure_ascii=False) + "\n"
            if len(wire.encode("utf-8")) > frame.INPUT_FRAME_LIMIT:
                raise AssertionError("client request frame over limit")
            self.proc.stdin.write(wire.encode("utf-8"))
            self.proc.stdin.flush()
            line = self.proc.stdout.readline()
            if not line:
                raise AssertionError(
                    f"service closed stdout; stderr={''.join(self.stderr_tail[-5:])}")
            text = line.decode("utf-8", "replace").rstrip("\n")
            try:
                response = json.loads(text)
            except json.JSONDecodeError as exc:
                raise AssertionError(f"non-JSON response line: {exc}: {text[:200]!r}") from exc
            if not isinstance(response, dict):
                raise AssertionError(f"response is not an object: {text[:200]!r}")
            if response.get("jsonrpc") != "2.0":
                raise AssertionError(f"response jsonrpc != 2.0: {text[:200]!r}")
            if response.get("id") != request_id:
                raise AssertionError(
                    f"response id {response.get('id')!r} != request id {request_id!r}")
            if ("result" in response) == ("error" in response):
                raise AssertionError(
                    f"response must have exactly one of result/error: {text[:200]!r}")
            unknown = set(response) - {"jsonrpc", "id", "result", "error"}
            if unknown:
                raise AssertionError(
                    f"unknown response members {sorted(unknown)}: {text[:200]!r}")
            if "error" in response:
                err = response["error"]
                if not isinstance(err, dict):
                    raise AssertionError(f"error is not an object: {text[:200]!r}")
                unknown = set(err) - {"code", "message", "data"}
                if unknown or not isinstance(err.get("code"), int) \
                        or not isinstance(err.get("message"), str):
                    raise AssertionError(f"malformed error object: {text[:200]!r}")
            return response

    def close(self):
        try:
            self.proc.stdin.close()
            self.proc.wait(timeout=30)
        except Exception:
            self.proc.kill()


def ip_int(address):
    """Numeric value of an IPv4/IPv6 address for order comparisons."""
    import ipaddress
    return int(ipaddress.ip_address(address))


def write_text(path, data, mode="w"):
    with open(path, mode, encoding="utf-8") as f:
        f.write(data)


def write_bytes(path, data):
    with open(path, "wb") as f:
        f.write(data)


def generate_fixture(path, source, fixture_tool, substitute):
    generator = source["generator"]
    seed = source.get("seed", 0)
    if generator == "ipv4_random_ranges":
        import random
        rng = random.Random(seed)
        lines = []
        for _ in range(1024):
            a = rng.randrange(0, 2**32)
            b = min(a + rng.randrange(0, 4096), 2**32 - 1)
            import ipaddress
            lines.append(f"{ipaddress.IPv4Address(a)}-{ipaddress.IPv4Address(b)}\n")
        write_text(path, "".join(lines))
        return
    if generator == "ipv6_random_ranges":
        import ipaddress
        import random
        rng = random.Random(seed)
        lines = []
        for _ in range(512):
            a = rng.getrandbits(128)
            b = min(a + rng.getrandbits(64), 2**128 - 1)
            lines.append(f"{ipaddress.IPv6Address(a)}-{ipaddress.IPv6Address(b)}\n")
        write_text(path, "".join(lines))
        return
    if generator != "v4_fixture":
        raise ValueError(f"unknown fixture generator {generator!r}")

    if fixture_tool is None:
        raise ValueError("case uses v4_fixture but --fixture-tool was not supplied")
    # The declarative case schema represents a generator as its name plus
    # an integer seed; the fixed fixture kinds use stable seed values.
    seeds = {0: "direct-v4", 1: "membership-v4", 2: "structured-v4"}
    try:
        argv = [seeds[seed], path]
    except KeyError as exc:
        raise ValueError(
            f"v4_fixture seed {seed!r} has no fixed kind; expected one of {sorted(seeds)}"
        ) from exc
    proc = subprocess.run(
        [fixture_tool, *argv],
        capture_output=True,
        timeout=300,
    )
    if proc.returncode != 0:
        raise ValueError(
            f"v4_fixture failed with exit {proc.returncode}: "
            f"{proc.stderr.decode('utf-8', 'replace').strip()}"
        )


CAPABILITIES_CACHE = {}


def describe_methods(binary):
    """Return the advertised method set of one executable, cached.

    A binary that does not speak JSON-RPC (or that fails describe)
    advertises nothing; cases requiring capabilities then skip instead
    of failing, which keeps legacy-only oracles in the matrix honest.
    """
    if binary in CAPABILITIES_CACHE:
        return CAPABILITIES_CACHE[binary]
    methods = set()
    try:
        service = JsonRpcService([binary, "--jsonrpc"], "probe")
        try:
            response = service.call("capability-probe", "iprange.v1.system.describe", {})
            if "result" in response:
                methods = set(response["result"].get("methods", []))
        finally:
            service.close()
    except (AssertionError, OSError):
        methods = set()
    CAPABILITIES_CACHE[binary] = methods
    return methods


def load_cases(case_dir):
    cases = []
    for name in sorted(os.listdir(case_dir)):
        if not name.endswith(".json"):
            continue
        with open(os.path.join(case_dir, name)) as f:
            case = json.load(f)
        case_schema.validate_case(case)
        cases.append(case)
    return cases


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--c", dest="c_binary", metavar="PATH")
    parser.add_argument("--fixture-tool", metavar="PATH",
                        help="absolute v4-fixture executable used by generated fixtures")
    parser.add_argument("--rust", dest="rust_binary", metavar="PATH")
    parser.add_argument("--go", dest="go_binary", metavar="PATH")
    parser.add_argument("--matrix", default="all",
                        choices=["all", "c", "rust", "go", "rust_to_go", "go_to_rust"])
    parser.add_argument("--cases", default=DEFAULT_CASE_DIR)
    parser.add_argument("--work-dir", metavar="DIR",
                        help="existing empty directory kept after the run (never deleted)")
    parser.add_argument("--filter", metavar="NAME")
    parser.add_argument("--json-report", metavar="PATH")
    args = parser.parse_args()

    fixture_tool = None
    if args.fixture_tool:
        if not os.path.isabs(args.fixture_tool) or not os.path.isfile(args.fixture_tool) \
                or not os.access(args.fixture_tool, os.X_OK):
            parser.error(f"fixture tool is not an absolute executable file: {args.fixture_tool}")
        fixture_tool = args.fixture_tool

    binaries = {}
    for key, attr in (("c", "c_binary"), ("rust", "rust_binary"), ("go", "go_binary")):
        path = getattr(args, attr)
        if path:
            if not os.path.isfile(path) or not os.access(path, os.X_OK):
                parser.error(f"{key} binary is not an absolute executable file: {path}")
            binaries[key] = os.path.abspath(path)

    use_cases = load_cases(args.cases)
    if args.filter:
        use_cases = [case for case in use_cases if args.filter in case["name"]]
    if not use_cases:
        print("no cases selected")
        return 0

    # Capability handshake: ask each executable once which methods it
    # advertises, then skip cases whose required method is unshipped.
    capabilities = {}
    for key in binaries:
        capabilities[key] = describe_methods(binaries[key])

    report = {"cases": [], "passed": 0, "failed": 0}

    def run_matrix(single=None):
        matrix = {
            "c": [("c", None)],
            "rust": [("rust", None)],
            "go": [("go", None)],
            "rust_to_go": [("rust", "go")],
            "go_to_rust": [("go", "rust")],
            "all": [("c", None), ("rust", None), ("go", None),
                    ("rust", "go"), ("go", "rust")],
        }[single or args.matrix]
        for producer, consumer in matrix:
            if producer not in binaries or (consumer and consumer not in binaries):
                continue
            for case in use_cases:
                required = case.get("requires")
                if required and required not in capabilities[producer]:
                    label = f"{case['name']} [{producer}" + (f"->{consumer}]" if consumer else "]")
                    print(f"SKIP {label}: requires unadvertised method {required}")
                    continue
                run_one(case, producer, consumer)

    def run_one(case, producer, consumer):
        produce_bin = binaries[producer]
        consume_bin = binaries[consumer] if consumer else produce_bin
        work = args.work_dir
        owns_work = False
        if work is None:
            work = tempfile.mkdtemp(prefix="iprange-cli-")
            owns_work = True
        label = f"{case['name']} [{producer}" + (f"->{consumer}]" if consumer else "]")
        try:
            runner = CaseRunner(consume_bin, case, work, producer, fixture_tool)
            if any(step["kind"] == "rpc" for step in case["steps"]):
                runner.service = JsonRpcService(runner.service_argv, producer)
            runner.run()
            report["passed"] += 1
            report["cases"].append({"name": case["name"], "matrix": label, "status": "PASS"})
            print(f"PASS {label}")
        except (AssertionError, ValueError, ValidationError) as exc:
            report["failed"] += 1
            report["cases"].append({"name": case["name"], "matrix": label,
                                    "status": "FAIL", "error": str(exc)})
            print(f"FAIL {label}: {exc}")
        finally:
            if runner.service is not None:
                runner.service.close()
            if owns_work:
                shutil.rmtree(work, ignore_errors=True)

    run_matrix()
    print(f"\n{report['passed']} passed, {report['failed']} failed")
    if args.json_report:
        with open(args.json_report, "w") as f:
            json.dump(report, f, indent=2)
    return 1 if report["failed"] else 0


if __name__ == "__main__":
    sys.exit(main())
