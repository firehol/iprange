"""Result schemas for every registered method (iprange-jsonrpc-v1.md).

Every result begins with `method` whose value is the exact method name.
The per-method `body` schemas describe the remaining fields. Result
validation rejects unknown members and wrong encodings exactly like
params validation.
"""

from . import common as C

# Common mechanical-conversion building blocks.
FILE_IDENTITY = {
    "type": "object",
    "properties": {"volume": C.U64, "file": C.U64},
    "required": ["volume", "file"],
    "additional": False,
}
ADDRESS_RANGE = {
    "type": "object",
    "properties": {"from": C.IP_ADDRESS, "to": C.IP_ADDRESS},
    "required": ["from", "to"],
    "additional": False,
}
OUTPUT_FACTS = {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "sha256": {"type": "string", "hex": 64},
        "bytes": {"type": "string", "decimal": True},
        "rows": {"type": "string", "decimal": True},
    },
    "required": ["path", "sha256", "bytes", "rows"],
    "additional": False,
}
WRITER_CLOSE = {"type": "object"}
SOURCE_CLOSE = {"type": "object"}
READER_CLOSE_RESULT = {"type": "object"}

COMMIT_RESULT = {
    "type": "object",
    "properties": {"outcome": C.LOGICAL_CHANGE, "committed": {"type": "boolean"}},
    "required": [],
    "additional": False,
}
PUBLICATION_RESULT = {
    "type": "object",
    "properties": {"published": {"type": "boolean"}},
    "required": [],
    "additional": False,
}

RESULTS = {}


def _register(method, body):
    RESULTS[method] = body


def _result(required_extra=(), body=None):
    schema = {
        "type": "object",
        "properties": {"method": {"type": "string"}},
        "required": ["method"] + list(required_extra),
        "additional": False,
    }
    if body:
        schema["properties"].update(body["properties"])
        schema["required"] += body.get("required", [])
    return schema


# system.describe
_register("iprange.v1.system.describe", _result(required_extra=(), body={
    "type": "object",
    "properties": {
        "product": {"type": "string"},
        "product_version": {"type": "string"},
        "implementation": {"type": "string", "enum": ["rust", "go"]},
        "jsonrpc_version": {"type": "string"},
        "api_version": {"type": "string"},
        "format": {"type": "string"},
        "platform": {"type": "string", "enum": ["linux", "macos", "windows", "freebsd", "other"]},
        "families": {"type": "array", "items": C.FAMILY, "min": 1},
        "methods": {"type": "array", "items": {"type": "string"}, "min": 1},
        "export_formats": {"type": "array", "items": {"type": "string"}, "min": 1},
        "limits": {
            "type": "object",
            "properties": {
                "input_frame_bytes": C.U64,
                "output_frame_bytes": C.U64,
                "response_object_bytes": C.U64,
                "batch_requests": C.U32,
                "queued_requests": C.U32,
                "reader_handles": C.U32,
                "cursor_handles": C.U32,
                "lookup_addresses": C.U32,
                "cursor_records": C.U32,
            },
            "required": ["input_frame_bytes", "output_frame_bytes", "response_object_bytes",
                         "batch_requests", "queued_requests", "reader_handles",
                         "cursor_handles", "lookup_addresses", "cursor_records"],
            "additional": False,
        },
        "fault_worker": {
            "type": "object",
            "properties": {"available": {"type": "boolean"}, "protocol": {"type": "string"}},
            "required": ["available", "protocol"],
            "additional": False,
        },
        "platform_result_fields": {"type": "array", "items": {"type": "string"}},
    },
    "required": ["product", "product_version", "implementation", "jsonrpc_version",
                 "api_version", "format", "platform", "families", "methods",
                 "export_formats", "limits", "fault_worker", "platform_result_fields"],
}))

# reader family
_register("iprange.v1.reader.open", _result(required_extra=("reader", "info"), body={
    "type": "object",
    "properties": {"reader": C.HANDLE, "info": {"type": "object"}},
    "required": ["reader", "info"],
}))
_register("iprange.v1.reader.close", _result(required_extra=("closed", "result"), body={
    "type": "object",
    "properties": {"closed": {"type": "boolean"}, "result": READER_CLOSE_RESULT},
    "required": ["closed", "result"],
}))
_register("iprange.v1.reader.info", _result(required_extra=("info",), body={
    "type": "object", "properties": {"info": {"type": "object"}}, "required": ["info"],
}))
_register("iprange.v1.reader.metadata", _result(required_extra=("present",), body={
    "type": "object",
    "properties": {
        "present": {"type": "boolean"},
        "base64": {"type": "string", "base64": True},
        "output": OUTPUT_FACTS,
    },
    "required": ["present"],
}))
_register("iprange.v1.reader.lookup", _result(required_extra=("matches",), body={
    "type": "object",
    "properties": {
        "matches": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "address": C.IP_ADDRESS,
                    "present": {"type": "boolean"},
                    "value": C.U32,
                    "feeds": {"type": "array", "items": C.FEED_NAME},
                },
                "required": ["address", "present"],
                "additional": False,
            },
            "min": 1,
        }
    },
    "required": ["matches"],
}))
_register("iprange.v1.reader.feeds.open", _result(required_extra=("cursor",), body={
    "type": "object", "properties": {"cursor": C.HANDLE}, "required": ["cursor"],
}))
_register("iprange.v1.reader.feeds.next", _result(required_extra=("feeds", "done"), body={
    "type": "object",
    "properties": {
        "feeds": {"type": "array", "items": {"type": "object", "properties": {"name": C.FEED_NAME}, "required": ["name"], "additional": False}},
        "done": {"type": "boolean"},
    },
    "required": ["feeds", "done"],
}))
_register("iprange.v1.reader.feeds.close", _result(required_extra=("closed",), body={
    "type": "object", "properties": {"closed": {"type": "boolean"}}, "required": ["closed"],
}))
_register("iprange.v1.reader.matching_feeds", _result(required_extra=("address", "feeds"), body={
    "type": "object",
    "properties": {
        "address": C.IP_ADDRESS,
        "feeds": {"type": "array", "items": C.FEED_NAME},
    },
    "required": ["address", "feeds"],
}))
_register("iprange.v1.reader.ranges.open", _result(required_extra=("cursor",), body={
    "type": "object", "properties": {"cursor": C.HANDLE}, "required": ["cursor"],
}))
_register("iprange.v1.reader.ranges.next", _result(required_extra=("records", "done"), body={
    "type": "object",
    "properties": {
        "records": {"type": "array", "items": ADDRESS_RANGE},
        "done": {"type": "boolean"},
    },
    "required": ["records", "done"],
}))
_register("iprange.v1.reader.ranges.close", _result(required_extra=("closed",), body={
    "type": "object", "properties": {"closed": {"type": "boolean"}}, "required": ["closed"],
}))

# database lifecycle: complete-result conversions are expansive; schemas
# require the method echo plus the documented top-level fields and reject
# unknown members at the top level. Nested SDK reports are validated by
# the producer and cross-checked by the external oracle.
_register("iprange.v1.database.create", _result(required_extra=(), body={
    "type": "object", "properties": {}, "required": [],
}))
_register("iprange.v1.database.initialize_live", _result(required_extra=(), body={
    "type": "object", "properties": {}, "required": [],
}))
_register("iprange.v1.database.reset_live", _result(required_extra=(), body={
    "type": "object", "properties": {}, "required": [],
}))
_register("iprange.v1.database.create.resolve", _result(required_extra=(), body={
    "type": "object", "properties": {}, "required": [],
}))
_register("iprange.v1.database.live_transition.resolve", _result(required_extra=(), body={
    "type": "object", "properties": {}, "required": [],
}))
_register("iprange.v1.database.live_residue.resolve", _result(required_extra=(), body={
    "type": "object", "properties": {}, "required": [],
}))
_register("iprange.v1.database.reclaim", _result(required_extra=("writer_close",), body={
    "type": "object", "properties": {"writer_close": WRITER_CLOSE}, "required": ["writer_close"],
}))
_register("iprange.v1.database.info", _result(required_extra=("info",), body={
    "type": "object", "properties": {"info": {"type": "object"}}, "required": ["info"],
}))
_register("iprange.v1.database.metadata.get", _result(required_extra=("present",), body={
    "type": "object",
    "properties": {
        "present": {"type": "boolean"},
        "base64": {"type": "string", "base64": True},
        "output": OUTPUT_FACTS,
    },
    "required": ["present"],
}))
_register("iprange.v1.database.metadata.replace", _result(required_extra=("logical_change", "commit", "writer_close"), body={
    "type": "object",
    "properties": {
        "logical_change": C.LOGICAL_CHANGE,
        "commit": COMMIT_RESULT,
        "writer_close": WRITER_CLOSE,
    },
    "required": ["logical_change", "commit", "writer_close"],
}))

# publisher mutations
_register("iprange.v1.current.publish", _result(required_extra=("report", "publication"), body={
    "type": "object",
    "properties": {
        "report": {"type": "object"},
        "publication": PUBLICATION_RESULT,
    },
    "required": ["report", "publication"],
}))
_register("iprange.v1.direct.replace", _result(required_extra=("report", "logical_change", "commit", "writer_close"), body={
    "type": "object",
    "properties": {
        "report": {"type": "object"},
        "logical_change": C.LOGICAL_CHANGE,
        "commit": COMMIT_RESULT,
        "writer_close": WRITER_CLOSE,
    },
    "required": ["report", "logical_change", "commit", "writer_close"],
}))
_register("iprange.v1.retention.first_seen.refresh", _result(required_extra=("report", "commit", "writer_close"), body={
    "type": "object",
    "properties": {
        "report": {"type": "object"},
        "commit": COMMIT_RESULT,
        "writer_close": WRITER_CLOSE,
        "removals": {"type": "object"},
    },
    "required": ["report", "commit", "writer_close"],
}))
_register("iprange.v1.retention.last_seen.refresh", _result(required_extra=("report", "commit", "writer_close"), body={
    "type": "object",
    "properties": {
        "report": {"type": "object"},
        "commit": COMMIT_RESULT,
        "writer_close": WRITER_CLOSE,
    },
    "required": ["report", "commit", "writer_close"],
}))

# named-feed lifecycle
for _m in ("iprange.v1.feeds.create", "iprange.v1.feeds.replace",
           "iprange.v1.feeds.delete", "iprange.v1.feeds.rename",
           "iprange.v1.feeds.import"):
    _register(_m, _result(required_extra=("report", "logical_change", "commit", "writer_close"), body={
        "type": "object",
        "properties": {
            "report": {"type": "object"},
            "logical_change": C.LOGICAL_CHANGE,
            "commit": COMMIT_RESULT,
            "writer_close": WRITER_CLOSE,
        },
        "required": ["report", "logical_change", "commit", "writer_close"],
    }))
_register("iprange.v1.history.project", _result(required_extra=("report", "logical_change", "commit", "writer_close"), body={
    "type": "object",
    "properties": {
        "report": {"type": "object"},
        "logical_change": C.LOGICAL_CHANGE,
        "commit": COMMIT_RESULT,
        "writer_close": WRITER_CLOSE,
    },
    "required": ["report", "logical_change", "commit", "writer_close"],
}))

# query family
for _m, _rows in (
    ("iprange.v1.query.cardinalities", {"type": "array", "items": {"type": "array"}, "min": 1}),
    ("iprange.v1.query.overlaps", {"type": "array", "items": {"type": "array"}, "min": 1}),
    ("iprange.v1.query.matching_feeds", {"type": "array", "items": {"type": "array"}, "min": 1}),
):
    _register(_m, _result(required_extra=("output", "report"), body={
        "type": "object",
        "properties": {
            "output": OUTPUT_FACTS,
            "report": {"type": "object"},
        },
        "required": ["output", "report"],
    }))

# joins
_register("iprange.v1.join.direct", _result(required_extra=("output", "report"), body={
    "type": "object",
    "properties": {"output": OUTPUT_FACTS, "report": {"type": "object"}},
    "required": ["output", "report"],
}))
_register("iprange.v1.join.membership", _result(required_extra=("output", "report"), body={
    "type": "object",
    "properties": {"output": OUTPUT_FACTS, "report": {"type": "object"}},
    "required": ["output", "report"],
}))

# algebra
_register("iprange.v1.algebra.count", _result(required_extra=("report", "cardinality"), body={
    "type": "object",
    "properties": {
        "report": {"type": "object"},
        "cardinality": C.U64,
    },
    "required": ["report", "cardinality"],
}))
_register("iprange.v1.algebra.compare", _result(required_extra=("report",), body={
    "type": "object", "properties": {"report": {"type": "object"}}, "required": ["report"],
}))
_register("iprange.v1.algebra.publish", _result(required_extra=("report", "publication"), body={
    "type": "object",
    "properties": {
        "report": {"type": "object"},
        "publication": PUBLICATION_RESULT,
    },
    "required": ["report", "publication"],
}))

# export
_register("iprange.v1.export", _result(required_extra=("path", "format", "sha256", "rows", "addresses", "bytes", "identity"), body={
    "type": "object",
    "properties": {
        "path": C.PATH,
        "format": {"type": "string", "enum": ["netset", "ipset", "ranges", "csv", "jsonl", "legacy_binary"]},
        "sha256": {"type": "string", "hex": 64},
        "rows": C.U64,
        "addresses": C.U64,
        "bytes": C.U64,
        "identity": FILE_IDENTITY,
    },
    "required": ["path", "format", "sha256", "rows", "addresses", "bytes", "identity"],
}))

# snapshot / validation / recovery
_register("iprange.v1.snapshot", _result(required_extra=("result",), body={
    "type": "object", "properties": {"result": {"type": "object"}}, "required": ["result"],
}))
_register("iprange.v1.validate", _result(required_extra=("result", "findings"), body={
    "type": "object",
    "properties": {
        "result": {"type": "object"},
        "findings": OUTPUT_FACTS,
    },
    "required": ["result", "findings"],
}))
_register("iprange.v1.recovery.inspect", _result(required_extra=("inspection", "candidate"), body={
    "type": "object",
    "properties": {
        "inspection": {"type": "object"},
        "candidate": {"type": "object"},
    },
    "required": ["inspection", "candidate"],
}))
_register("iprange.v1.recover", _result(required_extra=("result", "report"), body={
    "type": "object",
    "properties": {
        "result": {"type": "object"},
        "report": OUTPUT_FACTS,
    },
    "required": ["result", "report"],
}))

# resolution attempts
_register("iprange.v1.commit.resolve", _result(required_extra=("resolution",), body={
    "type": "object", "properties": {"resolution": {"type": "object"}}, "required": ["resolution"],
}))
_register("iprange.v1.publication.inspect", _result(required_extra=("inspection",), body={
    "type": "object",
    "properties": {
        "inspection": {"type": "object"},
        "handle": {"type": "object"},
    },
    "required": ["inspection"],
}))
_register("iprange.v1.publication.resolve", _result(required_extra=("publication",), body={
    "type": "object", "properties": {"publication": PUBLICATION_RESULT}, "required": ["publication"],
}))
_register("iprange.v1.publication.residue.remove", _result(required_extra=("removal",), body={
    "type": "object", "properties": {"removal": {"type": "object"}}, "required": ["removal"],
}))

# maintenance
_register("iprange.v1.maintenance.list", _result(required_extra=("output",), body={
    "type": "object", "properties": {"output": OUTPUT_FACTS}, "required": ["output"],
}))
_register("iprange.v1.maintenance.remove", _result(required_extra=("removal",), body={
    "type": "object", "properties": {"removal": {"type": "object"}}, "required": ["removal"],
}))


def validate_result(method, result):
    from .engine import ValidationError, validate
    body = RESULTS.get(method)
    if body is None:
        return result
    validate(result, body, f"result[{method}]")
    if result.get("method") != method:
        raise ValidationError(
            f"result[{method}].method",
            f"echo {result.get('method')!r} does not match requested method {method!r}")
    return result
