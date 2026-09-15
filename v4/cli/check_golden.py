#!/usr/bin/env python3
"""Validate golden wire exchanges, coverage, and request/response links."""

import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
_SELF_DIR = os.path.dirname(os.path.abspath(__file__))

from command_sanitize import (  # noqa: E402  (side-effect free)
    audit_report_writers,
    owned_temp_dir,
    require_paths_outside_profile,
    run_shared_self_test,
    write_committed_report,
    write_scratch_json,
)
from schema import cases as case_schema  # noqa: E402
from schema import frame, methods, results  # noqa: E402
from schema.engine import ValidationError  # noqa: E402


def check_exchange(exchange, path):
    """Validate one golden exchange and its semantic request/response links."""

    errors = []
    request = exchange["request"]
    response = exchange.get("response")
    try:
        frame.decode_frame(json.dumps(request, separators=(",", ":")))
        methods.validate_params(request["method"], request["params"])
    except (frame.FrameError, ValidationError) as exc:
        errors.append(f"request: {exc}")

    method = request["method"]
    if method == methods.CANCEL_METHOD:
        if "id" in request:
            errors.append("request: cancel golden must be a notification without id")
        if response is not None:
            errors.append("request: cancel golden must have no response")
        return errors

    if response is None:
        errors.append("request: non-notification golden must include a response")
        return errors
    try:
        decoded = frame.decode_response(json.dumps(response, separators=(",", ":")))
        frame.encode_response_object(response)
        frame.encode_response_frame(response)
        if decoded.get("id") != request.get("id"):
            errors.append(
                f"response id {decoded.get('id')!r} != request id {request.get('id')!r}")
        results.validate_result(method, response["result"])
    except (frame.FrameError, ValidationError) as exc:
        errors.append(f"response: {exc}")
        return errors

    errors.extend(semantic_errors(method, request["params"], response["result"]))
    return errors


def semantic_errors(method, params, result):
    """Check links a wire-schema validator cannot express locally."""

    errors = []
    request_path = None
    result_path = None

    if method in (
        "iprange.v1.query.cardinalities",
        "iprange.v1.query.overlaps",
        "iprange.v1.query.matching_feeds",
        "iprange.v1.join.direct",
        "iprange.v1.join.membership",
        "iprange.v1.maintenance.list",
    ):
        request_path = params.get("output", {}).get("path")
        result_path = result.get("output", {}).get("path")
    elif method in (
        "iprange.v1.database.metadata.get",
        "iprange.v1.reader.metadata",
    ):
        delivery = params.get("delivery", {})
        if delivery.get("mode") == "file":
            request_path = delivery.get("path")
            result_path = result.get("output", {}).get("path")
    elif method == "iprange.v1.validate":
        request_path = params.get("findings_output", {}).get("path")
        result_path = result.get("findings", {}).get("path")
    elif method == "iprange.v1.recover":
        request_path = params.get("report_output", {}).get("path")
        # Recover's success facts do not carry an output-facts object; the
        # request/output relation is represented by the JSONL file workflow.
        result_path = request_path
    elif method == "iprange.v1.export":
        request_path = params.get("destination")
        result_path = result.get("path")

    if request_path is not None and result_path != request_path:
        errors.append(
            f"semantic: response path {result_path!r} does not match request {request_path!r}")

    if method in (
        "iprange.v1.algebra.count",
        "iprange.v1.algebra.compare",
        "iprange.v1.algebra.publish",
    ):
        source_count = str(len(params.get("sources", [])))
        actual = result.get("report", {}).get("source_count")
        if actual != source_count:
            errors.append(
                f"semantic: source_count {actual!r} does not match {source_count!r} sources")

    if method in ("iprange.v1.current.publish", "iprange.v1.algebra.publish"):
        if params.get("metadata", {}).get("mode") == "keep":
            errors.append("semantic: keep metadata is invalid for new immutable output")

    return errors


def check_expect_error(exchange):
    """Validate an intentionally invalid request whose only acceptable
    wire outcome is the recorded error response: the request must be
    rejected by the shared frame schema, and the recorded response must
    be a valid error frame with an integer code and a null id (the
    invalid-notification -32600 contract, D3 wave-10)."""

    errors = []
    request = exchange["request"]
    expect = exchange["expect_error"]
    try:
        frame.decode_frame(json.dumps(request, separators=(",", ":")))
        errors.append("request: expected a frame-schema rejection, decoded ok")
    except frame.FrameError:
        pass
    try:
        decoded = frame.decode_response(json.dumps(expect, separators=(",", ":")))
        if decoded.get("id") is not None:
            errors.append("expect_error: id must be null")
        err = decoded.get("error")
        if not isinstance(err, dict) or not isinstance(err.get("code"), int):
            errors.append("expect_error: error.code must be an integer")
        frame.encode_response_object(expect)
        frame.encode_response_frame(expect)
    except (frame.FrameError, ValidationError) as exc:
        errors.append(f"expect_error: {exc}")
    return errors


def check_reader_sequence(exchanges, path):
    """Check handle/kind/cursor coherence across reader golden sequences."""

    errors = []
    readers = {}
    cursors = {}
    for index, exchange in enumerate(exchanges):
        request = exchange["request"]
        method = request["method"]
        if method not in methods.METHODS or method == methods.CANCEL_METHOD:
            continue
        response = exchange.get("response")
        if response is None:
            continue
        params = request["params"]
        result = response["result"]

        if method == "iprange.v1.reader.open":
            reader = result.get("reader")
            info = result.get("info", {})
            readers[reader] = info.get("value_kind")
        elif method in (
            "iprange.v1.reader.info", "iprange.v1.reader.lookup",
            "iprange.v1.reader.metadata", "iprange.v1.reader.matching_feeds",
            "iprange.v1.reader.feeds.open", "iprange.v1.reader.ranges.open",
        ):
            reader = params.get("reader")
            if reader not in readers:
                errors.append(f"exchange {index}: {method} uses unknown reader {reader!r}")
        elif method in (
            "iprange.v1.reader.feeds.next", "iprange.v1.reader.feeds.close",
            "iprange.v1.reader.ranges.next", "iprange.v1.reader.ranges.close",
        ):
            cursor = params.get("cursor")
            if cursor not in cursors:
                errors.append(f"exchange {index}: {method} uses unknown cursor {cursor!r}")

        if method == "iprange.v1.reader.feeds.open":
            cursor = result.get("cursor")
            if readers.get(params.get("reader")) == "direct":
                errors.append(
                    f"exchange {index}: direct reader cannot successfully enumerate feeds")
            cursors[cursor] = "feeds"
        elif method == "iprange.v1.reader.ranges.open":
            cursors[result.get("cursor")] = params.get("view", {}).get("kind")
        elif method in ("iprange.v1.reader.feeds.close", "iprange.v1.reader.ranges.close"):
            cursors.pop(params.get("cursor"), None)
        elif method in ("iprange.v1.reader.feeds.next", "iprange.v1.reader.ranges.next"):
            cursor = params.get("cursor")
            if result.get("done"):
                cursors.pop(cursor, None)
    return errors


def check_golden_file(path):
    errors = []
    with open(path, encoding="utf-8") as stream:
        golden = json.load(stream)
    if set(golden) != {"schema", "family", "note", "exchanges"}:
        errors.append("golden object members must be exactly schema, family, note, exchanges")
    if golden.get("schema") != "iprange-golden-v1":
        errors.append(f"schema {golden.get('schema')!r} != iprange-golden-v1")
    if not isinstance(golden.get("exchanges"), list) or not golden["exchanges"]:
        errors.append("exchanges must be a nonempty array")
        # Same shape as the success path: callers unpack three values, and a
        # two-tuple here turned a malformed golden into a crash instead of a
        # reported problem.
        return errors, [], 0

    for index, exchange in enumerate(golden["exchanges"]):
        if not isinstance(exchange, dict) or set(exchange) not in (
                {"request", "response"}, {"request", "expect_error"}):
            errors.append(
                "exchange %d: members must be exactly request+response or "
                "request+expect_error" % index)
            continue
        if "expect_error" in exchange:
            for error in check_expect_error(exchange):
                errors.append(f"exchange {index} {exchange['request'].get('method')}: {error}")
            continue
        for error in check_exchange(exchange, path):
            errors.append(f"exchange {index} {exchange['request'].get('method')}: {error}")
    if path.endswith("reader.json"):
        errors.extend(check_reader_sequence(golden["exchanges"], path))
    error_exchanges = sum(
        1 for item in golden["exchanges"]
        if isinstance(item, dict) and "expect_error" in item)
    return (errors,
            [item["request"]["method"] for item in golden["exchanges"]
             if isinstance(item, dict) and "response" in item
             and isinstance(item.get("request"), dict)],
            error_exchanges)


def scan_corpus(tree):
    """Check every golden exchange and every case file under one tree.

    Returns the counts and the problem list, without printing or deciding an
    exit status.  Splitting the walk from the reporting is what lets
    ``--self-test`` run the *same* checker over a fabricated tree: a self-test
    that re-implemented the expectations would keep passing while the checker
    it meant to test silently stopped visiting exchanges.
    """
    golden_dir = os.path.join(tree, "golden")
    case_dir = os.path.join(tree, "cases")
    failures = []
    checked_golden = 0
    golden_files = 0
    covered_methods = []
    for name in sorted(os.listdir(golden_dir)):
        if not name.endswith(".json"):
            continue
        golden_files += 1
        errors, methods_seen, error_exchanges = check_golden_file(
            os.path.join(golden_dir, name))
        checked_golden += len(methods_seen) + error_exchanges
        covered_methods.extend(methods_seen)
        failures.extend(
            f"{os.path.join(golden_dir, name)}: {error}" for error in errors)

    expected = set(methods.METHOD_NAMES)
    if sorted(covered_methods) != sorted(set(covered_methods)):
        failures.append(
            "golden corpus contains duplicate happy-path method exchanges")
    missing = sorted(expected - set(covered_methods))
    extra = sorted(set(covered_methods) - expected)
    if missing:
        failures.append(f"golden corpus omits methods: {missing}")
    if extra:
        failures.append(f"golden corpus contains unknown methods: {extra}")

    checked_cases = 0
    case_files = 0
    case_names = set()
    for name in sorted(os.listdir(case_dir)):
        if not name.endswith(".json"):
            continue
        case_files += 1
        path = os.path.join(case_dir, name)
        with open(path, encoding="utf-8") as stream:
            case = json.load(stream)
        try:
            case_schema.validate_case(case)
            checked_cases += 1
            if case["name"] in case_names:
                failures.append(f"{path}: duplicate case name {case['name']!r}")
            case_names.add(case["name"])
        except ValidationError as exc:
            failures.append(f"{path}: {exc}")

    return {
        "golden_files": golden_files,
        "golden_exchanges": checked_golden,
        "case_files": checked_cases,
        "case_files_seen": case_files,
        "covered_methods": sorted(set(covered_methods)),
        "failures": failures,
    }


# Executed-control counts for ``--self-test``, per class, as literals.  The
# corpus itself is not pinned to a number of files or exchanges -- teammates
# add cases, and a pin that breaks on every corpus edit gets loosened rather
# than honoured.  What is pinned is how many *controls ran*, which is the
# defect the wave-19.25 review found: a harness self-test that prints "0
# failures" cannot distinguish a run where everything checked passed from a run
# where nothing checked executed at all.
GOLDEN_SELF_TEST_WALK = 7
GOLDEN_SELF_TEST_REJECT = 17


def _self_test(tree):
    """Drive the checker over the real corpus and over doctored copies.

    Two classes of control, both counted:

    * ``walk`` -- the independent recount.  Every golden file, every exchange
      and every case file the tree holds must have been *visited*, proven by
      recounting the tree with plain listdir/json and comparing against what
      the walk reported.  A checker that silently skipped an exchange, or a
      directory whose name changed, fails here rather than reporting success.
    * ``reject`` -- the documented rejection classes.  Each one is produced by
      the mutation that should produce it, applied to a scratch copy of the
      corpus and re-checked by the *same* ``scan_corpus`` the live run uses.
      A control whose mutation silently stops applying (a fixture renamed, a
      method no longer in the corpus) raises rather than passing vacuously.

    Runs offline against committed fixtures: no product binary is involved.
    """
    import shutil

    controls = {"walk": 0, "reject": 0, "structural": 0}
    failures = []

    def expect(kind, label, condition, detail=""):  # kind in walk/reject/structural
        controls[kind] += 1
        ok = bool(condition)
        print(f"{'ok  ' if ok else 'BAD '} [{kind}] {label:60} {detail[:90]}")
        if not ok:
            failures.append(f"[{kind}] {label}: {detail}")
        return ok

    scan = scan_corpus(tree)
    if not expect("walk", "the committed corpus is clean",
                  not scan["failures"], "; ".join(scan["failures"][:3])):
        # Everything below assumes a clean baseline; continuing would blame
        # the corpus for a checker defect.
        for failure in scan["failures"][:10]:
            print(f"       {failure}")

    # --- walk: independent recount ------------------------------------------
    golden_dir = os.path.join(tree, "golden")
    case_dir = os.path.join(tree, "cases")
    golden_names = [n for n in sorted(os.listdir(golden_dir))
                    if n.endswith(".json")]
    case_names = [n for n in sorted(os.listdir(case_dir))
                  if n.endswith(".json")]
    expect("walk", "every golden file was visited",
           scan["golden_files"] == len(golden_names),
           f"{scan['golden_files']} of {len(golden_names)}")
    expect("walk", "every case file was visited",
           scan["case_files_seen"] == len(case_names),
           f"{scan['case_files_seen']} of {len(case_names)}")

    exchanges = happy = errored = 0
    for name in golden_names:
        with open(os.path.join(golden_dir, name), encoding="utf-8") as stream:
            golden = json.load(stream)
        for exchange in golden["exchanges"]:
            exchanges += 1
            if "expect_error" in exchange:
                errored += 1
            elif "response" in exchange:
                happy += 1
    expect("walk", "every exchange was checked",
           scan["golden_exchanges"] == exchanges,
           f"{scan['golden_exchanges']} of {exchanges} exchanges")
    expect("walk", "happy-path and expect_error exchanges are disjoint and total",
           happy + errored == exchanges and errored > 0 and happy > 0,
           f"happy={happy} expect_error={errored}")
    expect("walk", "the covered-method list is exactly the published method set",
           scan["covered_methods"] == sorted(methods.METHOD_NAMES),
           f"missing {sorted(set(methods.METHOD_NAMES) - set(scan['covered_methods']))}")
    expect("walk", "every case file validated against the case schema",
           scan["case_files"] == len(case_names),
           f"{scan['case_files']} of {len(case_names)}")

    # --- reject: doctored copies of the corpus ------------------------------
    scratch = os.path.join(owned_temp_dir("qual-golden-selftest-"), "corpus")
    shutil.copytree(tree, scratch,
                    ignore=shutil.ignore_patterns("__pycache__"))
    try:
        def goldens():
            return [n for n in sorted(os.listdir(os.path.join(scratch, "golden")))
                    if n.endswith(".json")]

        def load(name):
            with open(os.path.join(scratch, "golden", name),
                      encoding="utf-8") as stream:
                return json.load(stream)

        def store(name, document):
            # Scratch fixture, not a committed report: the named
            # scratch helper keeps the shared-tier rule (no direct
            # json.dump in a report writer) absolute.
            write_scratch_json(os.path.join(scratch, "golden", name),
                               document)

        def restore(name):
            shutil.copyfile(os.path.join(golden_dir, name),
                            os.path.join(scratch, "golden", name))

        def expect_rejected(label, name, mutate, marker):
            """Apply one mutation, require exactly the named rejection back."""
            document = load(name)
            if not mutate(document):
                # A mutation that cannot find its target fixture proves
                # nothing; treat it as a failed control, not a skipped one.
                expect("reject", label, False,
                       f"{name} no longer holds the exchange this control "
                       "mutates; the control must be rewritten, not skipped")
                return
            store(name, document)
            try:
                problems = scan_corpus(scratch)["failures"]
            finally:
                restore(name)
            # The same tree scanned clean moments ago, so every problem
            # here is attributable to this mutation.  A mutation may
            # cascade legitimately (deleting reader.open invalidates
            # every later handle), so the rule is that the named class
            # was reported, not that one line was printed.
            expect("reject", label,
                   bool(problems) and any(marker in p for p in problems),
                   str(problems[:3]))

        def pick(document, method, predicate=lambda exchange: True):
            for exchange in document["exchanges"]:
                if (exchange.get("request", {}).get("method") == method
                        and predicate(exchange)):
                    return exchange
            return None

        def rename_response_id(document):
            exchange = pick(document, "iprange.v1.reader.open")
            if exchange is None:
                return False
            exchange["response"]["id"] = "not-the-request-id"
            return True

        expect_rejected(
            "a response whose id does not echo the request is refused",
            "reader.json", rename_response_id, "response id")

        def drop_response(document):
            exchange = pick(document, "iprange.v1.reader.open",
                            lambda item: "response" in item)
            if exchange is None:
                return False
            # Explicit null, not a deleted member: an exchange object must
            # hold exactly request+response (or request+expect_error), so a
            # missing member is refused for the shape rule and would mask the
            # rule under test.
            exchange["response"] = None
            return True

        expect_rejected(
            "a non-notification exchange without a response is refused",
            "reader.json", drop_response, "must include a response")

        def cancel_gains_response(document):
            exchange = pick(document, methods.CANCEL_METHOD)
            if exchange is None:
                return False
            exchange["response"] = {"jsonrpc": "2.0", "id": "1",
                                    "result": {"method": methods.CANCEL_METHOD}}
            return True

        expect_rejected(
            "a cancel golden that carries a response is refused",
            "system.json", cancel_gains_response,
            "cancel golden must have no response")

        def mismatch_output_path(document):
            exchange = pick(document, "iprange.v1.query.cardinalities")
            if exchange is None:
                return False
            exchange["response"]["result"]["output"]["path"] += ".other"
            return True

        expect_rejected(
            "a delivery response whose path is not the requested one is refused",
            "query.json", mismatch_output_path, "does not match request")

        def break_source_count(document):
            exchange = pick(document, "iprange.v1.algebra.count")
            if exchange is None:
                return False
            exchange["response"]["result"]["report"]["source_count"] = "99"
            return True

        expect_rejected(
            "an algebra source_count that contradicts its request is refused",
            "algebra.json", break_source_count, "source_count")

        def keep_metadata(document):
            exchange = pick(document, "iprange.v1.current.publish")
            if exchange is None:
                return False
            metadata = exchange["request"]["params"].get("metadata")
            if not isinstance(metadata, dict):
                return False
            # Flip the recorded mode rather than adding a member: a params
            # object the request schema rejects would report a second,
            # unrelated problem.
            metadata["mode"] = "keep"
            return True

        expect_rejected(
            "keep metadata on a new immutable output is refused",
            "publisher.json", keep_metadata,
            "invalid for new immutable")

        def unknown_reader(document):
            exchange = pick(document, "iprange.v1.reader.lookup")
            if exchange is None:
                return False
            exchange["request"]["params"]["reader"] = "ff" * 16
            return True

        expect_rejected(
            "a reader handle that was never opened is refused",
            "reader.json", unknown_reader, "unknown reader")

        def unknown_cursor(document):
            exchange = pick(document, "iprange.v1.reader.ranges.next")
            if exchange is None:
                return False
            exchange["request"]["params"]["cursor"] = "ff" * 16
            return True

        expect_rejected(
            "a cursor that was never opened is refused",
            "reader.json", unknown_cursor, "unknown cursor")

        def reader_becomes_direct(document):
            # The coherence rule reads the value kind recorded by
            # reader.open, so that is the exchange that has to lie.
            opened = pick(document, "iprange.v1.reader.open")
            if opened is None:
                return False
            try:
                opened["response"]["result"]["info"]["value_kind"] = "direct"
            except (KeyError, TypeError):
                return False
            return True

        expect_rejected(
            "a direct reader that enumerates feeds is refused",
            "reader.json", reader_becomes_direct,
            "direct reader cannot successfully enumerate feeds")

        def error_request_becomes_valid(document):
            if not document["exchanges"]:
                return False
            document["exchanges"][0]["request"]["id"] = "suddenly-valid"
            return True

        expect_rejected(
            "an expect_error request that the frame schema accepts is refused",
            "errors.json", error_request_becomes_valid,
            "expected a frame-schema rejection")

        def wrong_schema(document):
            document["schema"] = "iprange-golden-v2"
            return True

        expect_rejected("a golden file with the wrong schema is refused",
                        "export.json", wrong_schema, "iprange-golden-v1")

        def extra_member(document):
            document["extra"] = 1
            return True

        expect_rejected("a golden file with an extra member is refused",
                        "export.json", extra_member, "members must be exactly")

        def empty_exchanges(document):
            document["exchanges"] = []
            return True

        expect_rejected("a golden file with no exchanges is refused",
                        "export.json", empty_exchanges, "nonempty array")

        def lost_open(document):
            document["exchanges"] = [
                item for item in document["exchanges"]
                if item.get("request", {}).get("method")
                != "iprange.v1.reader.open"]
            return True

        expect_rejected(
            "a reader sequence missing its open is refused",
            "reader.json", lost_open, "unknown reader")

        def forget_a_method(document):
            document["exchanges"] = []
            return True

        # Removing a whole golden file is the control for the corpus-level
        # completeness rule: the walk must notice a published method that no
        # happy-path exchange exercises any more.
        removed = os.path.join(scratch, "golden", "export.json")
        os.remove(removed)
        try:
            problems = scan_corpus(scratch)["failures"]
        finally:
            shutil.copyfile(os.path.join(golden_dir, "export.json"), removed)
        expect("reject", "a corpus that omits a published method is refused",
               len(problems) == 1 and "omits methods" in problems[0]
               and "iprange.v1.export" in problems[0], str(problems[:2]))

        # Case-side controls: the schema, and the duplicate-name rule that
        # only the walk can see.
        duplicated = os.path.join(scratch, "cases", "_selftest-copy.json")
        shutil.copyfile(os.path.join(case_dir, case_names[0]), duplicated)
        try:
            problems = scan_corpus(scratch)["failures"]
        finally:
            os.remove(duplicated)
        expect("reject", "two case files with one name are refused",
               len(problems) == 1 and "duplicate case name" in problems[0],
               str(problems[:2]))

        broken_case_path = os.path.join(case_dir, case_names[0])
        with open(broken_case_path, encoding="utf-8") as stream:
            real_case = json.load(stream)
        broken = dict(real_case, steps=[dict(real_case["steps"][0],
                                             kind="not-a-step-kind")])
        target = os.path.join(scratch, "cases", case_names[0])
        write_scratch_json(target, broken)
        try:
            problems = scan_corpus(scratch)["failures"]
            validated = scan_corpus(scratch)["case_files"]
        finally:
            shutil.copyfile(broken_case_path, target)
        expect("reject", "a case whose step kind is unknown is refused",
               len(problems) == 1 and validated == len(case_names) - 1
               and "not-a-step-kind" in problems[0], str(problems[:2]))
    finally:
        shutil.rmtree(os.path.dirname(scratch), ignore_errors=True)

    audit = audit_report_writers(cli_dir=_SELF_DIR, writers=["check_golden.py"], artifacts=False)
    if audit:
        failures.extend(f"[structural] {problem}" for problem in audit)
    expect("structural", "this writer commits through the shared "
                        "provenance owner",
           not audit, "; ".join(audit))

    expected = {"walk": GOLDEN_SELF_TEST_WALK,
                "reject": GOLDEN_SELF_TEST_REJECT, "structural": 1}
    for problem in failures:
        print(f"FAIL {problem}")
    if controls != expected:
        print(f"check_golden self-test FAILED: executed={controls}, "
              f"expected={expected}")
        return 1
    if failures:
        print(f"check_golden self-test FAILED: {len(failures)} problem(s)")
        return 1
    print(f"check_golden self-test PASSED: {controls['walk']} walk + "
          f"{controls['reject']} reject + {controls['structural']} structural "
          "controls")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tree", default=os.path.dirname(os.path.abspath(__file__)))
    parser.add_argument("--json-report", metavar="PATH",
                        help="write the counts, the covered method list, and "
                             "the reviewed revision to a JSON evidence file")
    parser.add_argument("--self-test", action="store_true",
                        help="re-check the committed corpus and doctored "
                             "copies of it, pinning the executed-control "
                             "counts (no product binary)")
    args = parser.parse_args()
    if args.self_test:
        run_shared_self_test("check_golden")
        return _self_test(args.tree)

    require_paths_outside_profile((("--tree", args.tree),
                                   ("--json-report", args.json_report)))
    scan = scan_corpus(args.tree)
    checked_golden = scan["golden_exchanges"]
    checked_cases = scan["case_files"]
    failures = scan["failures"]

    if args.json_report:
        target = args.json_report
        if not os.path.isabs(target):
            target = os.path.join(args.tree, target)
        report = {
            "schema": "iprange-cli-golden-report-v1",
            "golden_files": scan["golden_files"],
            "case_files_seen": scan["case_files_seen"],
            "golden_exchanges": checked_golden,
            "covered_methods": scan["covered_methods"],
            "case_files": checked_cases,
            "problems": failures,
            "result": "PASS" if not failures else "FAIL",
        }
        write_committed_report(
            target, report,
            caller_paths=(("--tree", args.tree),
                          ("--json-report", args.json_report)),
            indent=1)

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
