#!/usr/bin/env python3
"""Controllable fake iprange JSON-RPC server for the sensitivity gate.

Purpose: prove that the external client (v4/cli/run.py) rejects
malformed framing, missing fields, wrong integer encodings, false
outcomes, and incorrect rows BEFORE any production handler is written.

This is NOT a product binary. It never ships in an `iprange`
executable and contains no v4 persistence logic)Skip. It is a test
double that reads one mode word from argv[1] and then emits the
corresponding deliberately broken behavior on stdin/stdout.

Modes:
  describe_ok        reply system.describe with a minimal valid result
  describe_bad_json  reply with a non-JSON line
  describe_bad_id    reply id "wrong" to request id "case-x"
  describe_no_jsonrpc  reply without the jsonrpc member
  describe_bad_version reply jsonrpc "1.0"
  describe_bad_decimal  reply fraction number in limits.input_frame_bytes
  describe_missing_method  reply result without the method member
  describe_false_outcome  reply a schema-valid result whose method does
                     not match the request (outcome fabrication)
  rows_bad_order     reply incorrect cursor rows (out of order)
"""
import json
import sys

MODE = sys.argv[1] if len(sys.argv) > 1 else "describe_ok"

DESCRIBE_OK = {
    "jsonrpc": "2.0",
    "id": "CASE",
    "result": {
        "method": "iprange.v1.system.describe",
        "product": "iprange",
        "product_version": "0.0.0-fake",
        "implementation": "rust",
        "jsonrpc_version": "2.0",
        "api_version": "1",
        "format": "iprange-v4-phase1-unsigned",
        "platform": "linux",
        "families": ["ipv4", "ipv6"],
        "methods": ["iprange.v1.system.describe"],
        "export_formats": ["netset", "ipset", "ranges", "csv", "jsonl", "legacy_binary"],
        "limits": {
            "input_frame_bytes": "1048576",
            "output_frame_bytes": "1048576",
            "response_object_bytes": "65000",
            "batch_requests": 16,
            "queued_requests": 16,
            "reader_handles": 64,
            "cursor_handles": 64,
            "lookup_addresses": 4096,
            "cursor_records": 4096,
        },
        "fault_worker": {"available": True, "protocol": "0.0.0-fake"},
        "platform_result_fields": [],
    },
}


def emit(payload):
    sys.stdout.write(json.dumps(payload, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def handle(line):
    """Dispatch one request. Returns True to keep serving, False to
    exit after this frame."""
    keep = True
    try:
        request = json.loads(line)
    except json.JSONDecodeError:
        emit({"jsonrpc": "2.0", "id": None,
              "error": {"code": -32700, "message": "parse error"}})
        return
    if not isinstance(request, dict):
        emit({"jsonrpc": "2.0", "id": None,
              "error": {"code": -32600, "message": "invalid request"}})
        return
    request_id = request.get("id", "CASE")
    method = request.get("method", "")
    if MODE == "describe_ok":
        result = dict(DESCRIBE_OK["result"])
        result["methods"] = json.loads(json.dumps(result["methods"]))
        emit({"jsonrpc": "2.0", "id": request_id, "result": result})
        return keep
    elif MODE == "describe_bad_json":
        sys.stdout.write("{not json at all}\n")
        sys.stdout.flush()
    elif MODE == "describe_bad_id":
        emit({"jsonrpc": "2.0", "id": "wrong", "result": dict(DESCRIBE_OK["result"])})
    elif MODE == "describe_no_jsonrpc":
        emit({"id": request_id, "result": dict(DESCRIBE_OK["result"])})
    elif MODE == "describe_bad_version":
        emit({"jsonrpc": "1.0", "id": request_id, "result": dict(DESCRIBE_OK["result"])})
    elif MODE == "describe_bad_decimal":
        result = dict(DESCRIBE_OK["result"])
        result["limits"] = dict(result["limits"])
        result["limits"]["input_frame_bytes"] = "1048576.5"  # non-canonical
        emit({"jsonrpc": "2.0", "id": request_id, "result": result})
    elif MODE == "describe_missing_method":
        result = dict(DESCRIBE_OK["result"])
        del result["method"]
        emit({"jsonrpc": "2.0", "id": request_id, "result": result})
    elif MODE == "describe_unknown_member":
        result = dict(DESCRIBE_OK["result"])
        result["test_only_field"] = True
        emit({"jsonrpc": "2.0", "id": request_id, "result": result})
    elif MODE == "describe_false_outcome":
        result = dict(DESCRIBE_OK["result"])
        result["method"] = "iprange.v1.database.metadata.replace"
        emit({"jsonrpc": "2.0", "id": request_id, "result": result})
    elif method == "iprange.v1.reader.open":
        # Modes which exercise reader-bound protocol checks open a fake
        # reader first; the runner needs a schema-valid info response.
        value_kind = "direct" if MODE in ("lookup_ok", "rows_wrong_value") else "membership"
        emit({"jsonrpc": "2.0", "id": request_id, "result": {
            "method": "iprange.v1.reader.open",
            "reader": "a" * 32,
            "info": {
                "address_family": "ipv4",
                "value_kind": value_kind,
                "structure_kind": "none",
                "value_tag": {"hex": "66616b65"},
                "database_id": "b0000000000000000000000000000001",
                "transaction_id": "1",
                "commit_nonce": "c0000000000000000000000000000001",
                "page_count": "1",
                "range_record_count": "2",
                "active_feed_count": "1",
                "meta_selection": "proven_current",
            },
        }})
        return keep
    elif MODE == "rows_bad_order":
        if method == "iprange.v1.reader.ranges.open":
            emit({"jsonrpc": "2.0", "id": request_id, "result": {
                "method": "iprange.v1.reader.ranges.open",
                "cursor": "c0000000000000000000000000000000"}})
            return keep
        result = {
            "method": "iprange.v1.reader.ranges.next",
            "records": [
                {"from": "10.0.0.10", "to": "10.0.0.20"},
                {"from": "10.0.0.1", "to": "10.0.0.5"},
            ],
            "done": True,
        }
        emit({"jsonrpc": "2.0", "id": request_id, "result": result})
    elif MODE == "rows_ok":
        if method == "iprange.v1.reader.ranges.open":
            emit({"jsonrpc": "2.0", "id": request_id, "result": {
                "method": "iprange.v1.reader.ranges.open",
                "cursor": "c0000000000000000000000000000000"}})
            return keep
        emit({"jsonrpc": "2.0", "id": request_id, "result": {
            "method": "iprange.v1.reader.ranges.next",
            "records": [
                {"from": "10.0.0.1", "to": "10.0.0.5"},
                {"from": "10.0.0.10", "to": "10.0.0.20"},
            ],
            "done": True,
        }})
    elif MODE == "lookup_ok":
        emit({"jsonrpc": "2.0", "id": request_id, "result": {
            "method": "iprange.v1.reader.lookup",
            "matches": [
                {"address": "10.0.0.1", "present": True, "value": 5},
                {"address": "10.0.0.2", "present": True, "value": 7},
            ],
        }})
    elif MODE == "rows_wrong_value":
        result = {
            "method": "iprange.v1.reader.lookup",
            "matches": [
                {"address": "10.0.0.1", "present": True, "value": 4294967296},
            ],
        }
        emit({"jsonrpc": "2.0", "id": request_id, "result": result})
    else:
        raise SystemExit(f"unknown fake mode {MODE!r}")


def serve():
    """Run the fake JSON-RPC server on stdin/stdout until EOF."""
    for raw in sys.stdin:
        line = raw.rstrip("\n")
        if line.endswith("\r"):
            line = line[:-1]
        if not line:
            continue
        if not handle(line):
            # A broken frame was emitted for this request; exit so the
            # client observes EOF but already received the frame.
            return


if __name__ == "__main__":
    serve()
