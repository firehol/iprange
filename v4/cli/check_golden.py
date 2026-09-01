#!/usr/bin/env python3
"""Validate golden wire exchanges, coverage, and request/response links."""

import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

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
        return errors, []

    for index, exchange in enumerate(golden["exchanges"]):
        if not isinstance(exchange, dict) or set(exchange) != {"request", "response"}:
            errors.append(f"exchange {index}: members must be exactly request, response")
            continue
        for error in check_exchange(exchange, path):
            errors.append(f"exchange {index} {exchange['request'].get('method')}: {error}")
    if path.endswith("reader.json"):
        errors.extend(check_reader_sequence(golden["exchanges"], path))
    return errors, [item["request"]["method"] for item in golden["exchanges"]
                    if isinstance(item, dict) and isinstance(item.get("request"), dict)]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tree", default=os.path.dirname(os.path.abspath(__file__)))
    args = parser.parse_args()

    golden_dir = os.path.join(args.tree, "golden")
    case_dir = os.path.join(args.tree, "cases")
    failures = []
    checked_golden = 0
    covered_methods = []
    for name in sorted(os.listdir(golden_dir)):
        if not name.endswith(".json"):
            continue
        errors, methods_seen = check_golden_file(os.path.join(golden_dir, name))
        checked_golden += len(methods_seen)
        covered_methods.extend(methods_seen)
        failures.extend(f"{os.path.join(golden_dir, name)}: {error}" for error in errors)

    expected = set(methods.METHOD_NAMES)
    if sorted(covered_methods) != sorted(set(covered_methods)):
        failures.append("golden corpus contains duplicate method exchanges")
    missing = sorted(expected - set(covered_methods))
    extra = sorted(set(covered_methods) - expected)
    if missing:
        failures.append(f"golden corpus omits methods: {missing}")
    if extra:
        failures.append(f"golden corpus contains unknown methods: {extra}")

    checked_cases = 0
    case_names = set()
    for name in sorted(os.listdir(case_dir)):
        if not name.endswith(".json"):
            continue
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
