"""Fixed method registry and per-method params schemas (iprange-jsonrpc-v1.md)."""

from . import common as C
from .engine import ValidationError
from .frame import CANCEL_METHOD

METHODS = {}


def _register(name, params):
    METHODS[name] = {"params": params}


def _invalid(path, problem):
    raise ValidationError(path, problem)


def _require_unique_window_feeds(windows, path):
    names = [window["feed"] for window in windows]
    if len(set(names)) != len(names):
        _invalid(path, "window feed names must be unique")


def _require_nonself_pair(pair, path):
    if pair["left"] == pair["right"]:
        _invalid(path, "pair left and right feeds must differ")


def _require_unique_unordered_pairs(pairs, path):
    normalized = {frozenset((pair["left"], pair["right"])) for pair in pairs}
    if len(normalized) != len(pairs):
        _invalid(path, "unordered pairs must be unique")


def _require_valid_export(value, path):
    minimum = value.get("min_prefix")
    prefixes = value.get("prefixes")
    if minimum is not None and prefixes is not None:
        _invalid(path, "min_prefix and prefixes are mutually exclusive")
    if value["format"] != "netset" and (minimum is not None or prefixes is not None):
        _invalid(path, "prefix controls are valid only for netset format")


# system
_register("iprange.v1.system.describe", {"type": "object", "properties": {}, "required": [], "additional": False})

# reader family
_reader_source = {"type": "object", "properties": {"source": C.DATABASE_SOURCE}, "required": ["source"], "additional": False}
_handle_param = {"type": "object", "properties": {"reader": C.HANDLE}, "required": ["reader"], "additional": False}
_cursor_param = {"type": "object", "properties": {"cursor": C.HANDLE}, "required": ["cursor"], "additional": False}

_register("iprange.v1.reader.open", _reader_source)
_register("iprange.v1.reader.close", _handle_param)
_register("iprange.v1.reader.info", _handle_param)
_register("iprange.v1.reader.metadata", {
    "type": "object",
    "properties": {"reader": C.HANDLE, "delivery": C.METADATA_DELIVERY},
    "required": ["reader", "delivery"],
    "additional": False,
})
_register("iprange.v1.reader.lookup", {
    "type": "object",
    "properties": {
        "reader": C.HANDLE,
        "addresses": {"type": "array", "items": C.IP_ADDRESS, "min": 1, "max": 4096},
    },
    "required": ["reader", "addresses"],
    "additional": False,
})
_register("iprange.v1.reader.feeds.open", {
    "type": "object",
    "properties": {"reader": C.HANDLE, "batch_size": C.U32},
    "required": ["reader", "batch_size"],
    "additional": False,
})
_register("iprange.v1.reader.feeds.next", _cursor_param)
_register("iprange.v1.reader.feeds.close", _cursor_param)
_register("iprange.v1.reader.matching_feeds", {
    "type": "object",
    "properties": {"reader": C.HANDLE, "address": C.IP_ADDRESS},
    "required": ["reader", "address"],
    "additional": False,
})
_register("iprange.v1.reader.ranges.open", {
    "type": "object",
    "properties": {
        "reader": C.HANDLE,
        "view": {
            "type": "one_of",
            "options": [
                {
                    "type": "object",
                    "properties": {"kind": {"type": "string", "enum": ["direct", "structured"]}},
                    "required": ["kind"],
                    "additional": False,
                },
                {
                    "type": "object",
                    "properties": {
                        "kind": {"type": "string", "enum": ["feed"]},
                        "feed": C.FEED_NAME,
                    },
                    "required": ["kind", "feed"],
                    "additional": False,
                },
            ],
        },
        "direction": C.DIRECTION,
        "start": C.IP_ADDRESS,
        "batch_size": C.U32,
    },
    "required": ["reader", "view", "direction", "batch_size"],
    "additional": False,
})
_register("iprange.v1.reader.ranges.next", _cursor_param)
_register("iprange.v1.reader.ranges.close", _cursor_param)

# database family
_register("iprange.v1.database.create", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "family": C.FAMILY,
        "value_kind": C.VALUE_KIND,
        "structure_kind": C.STRUCTURE_KIND,
        "value_tag": C.VALUE_TAG,
        "reader_capacity": C.U32,
    },
    "required": ["path", "family", "value_kind", "structure_kind", "value_tag", "reader_capacity"],
    "additional": False,
})
_register("iprange.v1.database.initialize_live", {
    "type": "object",
    "properties": {"path": C.PATH, "reader_capacity": C.U32},
    "required": ["path", "reader_capacity"],
    "additional": False,
})
_register("iprange.v1.database.reset_live", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "reader_capacity": C.U32,
        "policy": {"type": "string", "enum": ["rollback_safe", "discard_previous"]},
    },
    "required": ["path", "reader_capacity", "policy"],
    "additional": False,
})
_register("iprange.v1.database.create.resolve", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "create_result": {"type": "object"},
        "resolution_mode": {"type": "string", "enum": ["complete", "rollback"]},
    },
    "required": ["path", "create_result", "resolution_mode"],
    "additional": False,
})
_register("iprange.v1.database.live_transition.resolve", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "live_transition_result": {"type": "object"},
        "resolution_mode": {"type": "string", "enum": ["complete", "rollback"]},
    },
    "required": ["path", "live_transition_result", "resolution_mode"],
    "additional": False,
})
_register("iprange.v1.database.live_residue.resolve", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "resolution_mode": C.LIVE_TRANSITION_RESOLUTION_MODE,
    },
    "required": ["path", "resolution_mode"],
    "additional": False,
})
_register("iprange.v1.database.reclaim", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "max_transactions": C.PARAM_U64,
        "max_pages": C.PARAM_U64,
        "writer_budget": C.WRITER_BUDGET,
    },
    "required": ["path", "max_transactions", "max_pages", "writer_budget"],
    "additional": False,
})
_register("iprange.v1.database.info", {
    "type": "object", "properties": {"source": C.DATABASE_SOURCE}, "required": ["source"], "additional": False,
})
_register("iprange.v1.database.metadata.get", {
    "type": "object",
    "properties": {"source": C.DATABASE_SOURCE, "delivery": C.METADATA_DELIVERY},
    "required": ["source", "delivery"],
    "additional": False,
})
_register("iprange.v1.database.metadata.replace", {
    "type": "object",
    "properties": {"path": C.PATH, "metadata": C.METADATA_REPLACEMENT_INPUT, "writer_budget": C.WRITER_BUDGET},
    "required": ["path", "metadata", "writer_budget"],
    "additional": False,
})

# publisher mutations
_register("iprange.v1.current.publish", {
    "type": "object",
    "properties": {
        "input": C.TEXT_INPUT,
        "feed": C.FEED_NAME,
        "value_tag": C.VALUE_TAG,
        "metadata": C.METADATA_REPLACEMENT_INPUT,
        "destination": C.PATH,
        "publication_policy": C.PUBLICATION_POLICY,
        "immutable_feed_budget": C.IMMUTABLE_FEED_BUDGET,
    },
    "required": ["input", "feed", "value_tag", "metadata", "destination",
                 "publication_policy", "immutable_feed_budget"],
    "additional": False,
})
_register("iprange.v1.direct.replace", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "input": C.DIRECT_INPUT,
        "metadata": C.METADATA_INPUT,
        "writer_budget": C.WRITER_BUDGET,
    },
    "required": ["path", "input", "metadata", "writer_budget"],
    "additional": False,
})
_register("iprange.v1.retention.first_seen.refresh", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "current": C.CURRENT_COVERAGE_SOURCE,
        "refresh_value": C.U32,
        "removals_output": {
            "type": "object",
            "properties": {
                "path": C.PATH,
                "publication_policy": C.PUBLICATION_POLICY,
                "result_budget": C.RESULT_BUDGET,
            },
            "required": ["path", "publication_policy", "result_budget"],
            "additional": False,
        },
        "metadata": C.METADATA_INPUT,
        "writer_budget": C.WRITER_BUDGET,
    },
    "required": ["path", "current", "refresh_value", "metadata", "writer_budget"],
    "additional": False,
})
_register("iprange.v1.retention.last_seen.refresh", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "current": C.CURRENT_COVERAGE_SOURCE,
        "refresh_value": C.U32,
        "cutoff": C.U32,
        "metadata": C.METADATA_INPUT,
        "writer_budget": C.WRITER_BUDGET,
    },
    "required": ["path", "current", "refresh_value", "cutoff", "metadata", "writer_budget"],
    "additional": False,
})

# named-feed lifecycle
_feed_mutation = {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "feed": C.FEED_NAME,
        "current": C.CURRENT_COVERAGE_SOURCE,
        "metadata": C.METADATA_INPUT,
        "writer_budget": C.WRITER_BUDGET,
    },
    "required": ["path", "feed", "current", "metadata", "writer_budget"],
    "additional": False,
}
_register("iprange.v1.feeds.create", _feed_mutation)
_register("iprange.v1.feeds.replace", _feed_mutation)
_register("iprange.v1.feeds.delete", {
    "type": "object",
    "properties": {"path": C.PATH, "feed": C.FEED_NAME, "metadata": C.METADATA_INPUT, "writer_budget": C.WRITER_BUDGET},
    "required": ["path", "feed", "metadata", "writer_budget"],
    "additional": False,
})
_register("iprange.v1.feeds.rename", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "old_feed": C.FEED_NAME,
        "new_feed": C.FEED_NAME,
        "metadata": C.METADATA_INPUT,
        "writer_budget": C.WRITER_BUDGET,
    },
    "required": ["path", "old_feed", "new_feed", "metadata", "writer_budget"],
    "additional": False,
})
_register("iprange.v1.feeds.import", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "source": C.DATABASE_SOURCE,
        "metadata": C.METADATA_INPUT,
        "writer_budget": C.WRITER_BUDGET,
    },
    "required": ["path", "source", "metadata", "writer_budget"],
    "additional": False,
})
_register("iprange.v1.history.project", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "last_seen": C.DATABASE_SOURCE,
        "windows": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {"feed": C.FEED_NAME, "cutoff": C.U32},
                "required": ["feed", "cutoff"],
                "additional": False,
            },
            "min": 1,
            "max": 4096,
            "validator": _require_unique_window_feeds,
        },
        "metadata": C.METADATA_INPUT,
        "writer_budget": C.WRITER_BUDGET,
    },
    "required": ["path", "last_seen", "windows", "metadata", "writer_budget"],
    "additional": False,
})

# query family
_register("iprange.v1.query.cardinalities", {
    "type": "object",
    "properties": {
        "source": C.DATABASE_SOURCE,
        "selection": C.FEED_SELECTION,
        "membership_query_budget": C.MEMBERSHIP_QUERY_BUDGET,
        "output": C.OUTPUT_DESCRIPTOR,
    },
    "required": ["source", "selection", "membership_query_budget", "output"],
    "additional": False,
})
_register("iprange.v1.query.overlaps", {
    "type": "object",
    "properties": {
        "source": C.DATABASE_SOURCE,
        "selection": C.FEED_SELECTION,
        "membership_query_budget": C.MEMBERSHIP_QUERY_BUDGET,
        "mode": {
            "type": "one_of",
            "options": [
                {"type": "object", "properties": {"kind": {"type": "string", "enum": ["all_pairs"]}}, "required": ["kind"], "additional": False},
                {"type": "object", "properties": {"kind": {"type": "string", "enum": ["target"]}, "target_feed": C.FEED_NAME}, "required": ["kind", "target_feed"], "additional": False},
                {
                    "type": "object",
                    "properties": {
                        "kind": {"type": "string", "enum": ["selected_pairs"]},
                        "pairs": {
                            "type": "array",
                            "items": {
                                "type": "object",
                                "properties": {"left": C.FEED_NAME, "right": C.FEED_NAME},
                                "required": ["left", "right"],
                                "additional": False,
                                "validator": _require_nonself_pair,
                            },
                            "min": 1,
                            "validator": _require_unique_unordered_pairs,
                        },
                    },
                    "required": ["kind", "pairs"],
                    "additional": False,
                },
            ],
        },
        "output": C.OUTPUT_DESCRIPTOR,
    },
    "required": ["source", "selection", "membership_query_budget", "mode", "output"],
    "additional": False,
})
_register("iprange.v1.query.matching_feeds", {
    "type": "object",
    "properties": {
        "source": C.DATABASE_SOURCE,
        "addresses": {"type": "array", "items": C.IP_ADDRESS, "min": 1, "max": 4096},
        "output": C.OUTPUT_DESCRIPTOR,
    },
    "required": ["source", "addresses", "output"],
    "additional": False,
})

# joins
_join_membership_side = {
    "type": "object",
    "properties": {
        "source": C.DATABASE_SOURCE,
        "selection": C.FEED_SELECTION,
        "membership_query_budget": C.MEMBERSHIP_QUERY_BUDGET,
    },
    "required": ["source", "selection", "membership_query_budget"],
    "additional": False,
}
_register("iprange.v1.join.direct", {
    "type": "object",
    "properties": {
        "membership": _join_membership_side,
        "direct": C.DATABASE_SOURCE,
        "output": C.OUTPUT_DESCRIPTOR,
        "max_result_cells": C.PARAM_U64,
    },
    "required": ["membership", "direct", "output", "max_result_cells"],
    "additional": False,
})
_register("iprange.v1.join.membership", {
    "type": "object",
    "properties": {
        "left": _join_membership_side,
        "right": _join_membership_side,
        "output": C.OUTPUT_DESCRIPTOR,
    },
    "required": ["left", "right", "output"],
    "additional": False,
})

# algebra
_ALGEBRA_SOURCE = {
    "type": "object",
    "properties": {
        "source": C.DATABASE_SOURCE,
        "scope": {
            "type": "object",
            "properties": {"mode": {"type": "string", "enum": ["all"]}},
            "required": ["mode"],
            "additional": False,
        },
        "membership_query_budget": C.MEMBERSHIP_QUERY_BUDGET,
    },
    "required": ["source", "scope", "membership_query_budget"],
    "additional": False,
}
_register("iprange.v1.algebra.count", {
    "type": "object",
    "properties": {
        "sources": {"type": "array", "items": _ALGEBRA_SOURCE, "min": 1},
        "selection": C.FEED_SELECTION,
        "algebra_budget": C.ALGEBRA_BUDGET,
    },
    "required": ["sources", "selection", "algebra_budget"],
    "additional": False,
})
_register("iprange.v1.algebra.compare", {
    "type": "object",
    "properties": {
        "sources": {"type": "array", "items": _ALGEBRA_SOURCE, "min": 1},
        "left": C.FEED_SELECTION,
        "right": C.FEED_SELECTION,
        "algebra_budget": C.ALGEBRA_BUDGET,
    },
    "required": ["sources", "left", "right", "algebra_budget"],
    "additional": False,
})
_ALGEBRA_OPERATION = {
    "type": "one_of",
    "options": [
        {"type": "object", "properties": {"kind": {"type": "string", "enum": ["union"]}, "selection": C.FEED_SELECTION}, "required": ["kind", "selection"], "additional": False},
        {"type": "object", "properties": {"kind": {"type": "string", "enum": ["intersection"]}, "selection": C.FEED_SELECTION}, "required": ["kind", "selection"], "additional": False},
        {"type": "object", "properties": {"kind": {"type": "string", "enum": ["exclusion"]}, "included": C.FEED_SELECTION, "excluded": C.FEED_SELECTION}, "required": ["kind", "included", "excluded"], "additional": False},
    ],
}
_ALGEBRA_OUTPUT_MODE = {
    "type": "one_of",
    "options": [
        {"type": "object", "properties": {"kind": {"type": "string", "enum": ["preserve_feeds"]}}, "required": ["kind"], "additional": False},
        {"type": "object", "properties": {"kind": {"type": "string", "enum": ["flat"]}, "feed": C.FEED_NAME}, "required": ["kind", "feed"], "additional": False},
    ],
}
_register("iprange.v1.algebra.publish", {
    "type": "object",
    "properties": {
        "sources": {"type": "array", "items": _ALGEBRA_SOURCE, "min": 1},
        "operation": _ALGEBRA_OPERATION,
        "output_mode": _ALGEBRA_OUTPUT_MODE,
        "value_tag": C.VALUE_TAG,
        "metadata": C.METADATA_REPLACEMENT_INPUT,
        "destination": C.PATH,
        "publication_policy": C.PUBLICATION_POLICY,
        "algebra_budget": C.ALGEBRA_BUDGET,
        "algebra_output_budget": C.ALGEBRA_OUTPUT_BUDGET,
    },
    "required": ["sources", "operation", "output_mode", "value_tag", "metadata",
                 "destination", "publication_policy", "algebra_budget", "algebra_output_budget"],
    "additional": False,
})

# export
_register("iprange.v1.export", {
    "type": "object",
    "properties": {
        "source": C.DATABASE_SOURCE,
        "view": {
            "type": "one_of",
            "options": [
                {
                    "type": "object",
                    "properties": {"kind": {"type": "string", "enum": ["direct", "structured"]}},
                    "required": ["kind"],
                    "additional": False,
                },
                {
                    "type": "object",
                    "properties": {
                        "kind": {"type": "string", "enum": ["feed"]},
                        "feed": C.FEED_NAME,
                    },
                    "required": ["kind", "feed"],
                    "additional": False,
                },
                {
                    "type": "object",
                    "properties": {
                        "kind": {"type": "string", "enum": ["selection"]},
                        "selection": C.FEED_SELECTION,
                    },
                    "required": ["kind", "selection"],
                    "additional": False,
                },
            ],
        },
        "format": {"type": "string", "enum": ["netset", "ipset", "ranges", "csv", "jsonl", "legacy_binary"]},
        "destination": C.PATH,
        "publication_policy": C.PUBLICATION_POLICY,
        "min_prefix": {"type": "integer", "min": 0, "max": 128},
        "prefixes": {
            "type": "array",
            "items": {"type": "integer", "min": 0, "max": 128},
            "min": 1,
            "unique": True,
        },
        "result_budget": C.RESULT_BUDGET,
    },
    "validator": _require_valid_export,
    "required": ["source", "view", "format", "destination", "publication_policy", "result_budget"],
    "additional": False,
})

# snapshot / validation / recovery
_register("iprange.v1.snapshot", {
    "type": "object",
    "properties": {
        "source": C.DATABASE_SOURCE,
        "destination": C.PATH,
        "publication_policy": C.PUBLICATION_POLICY,
        "snapshot_budget": C.SNAPSHOT_BUDGET,
    },
    "required": ["source", "destination", "publication_policy", "snapshot_budget"],
    "additional": False,
})
_VALIDATION_MODE = {
    "type": "one_of",
    "options": [
        {"type": "object", "properties": {"kind": {"type": "string", "enum": ["immutable_current"]}}, "required": ["kind"], "additional": False},
        {"type": "object", "properties": {"kind": {"type": "string", "enum": ["live_current"]}}, "required": ["kind"], "additional": False},
        {"type": "object", "properties": {"kind": {"type": "string", "enum": ["offline_candidate"]}, "candidate": {"type": "object"}}, "required": ["kind", "candidate"], "additional": False},
    ],
}
_register("iprange.v1.validate", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "mode": _VALIDATION_MODE,
        "validation_budget": C.VALIDATION_BUDGET,
        "findings_output": C.OUTPUT_DESCRIPTOR_JSONL,
    },
    "required": ["path", "mode", "validation_budget", "findings_output"],
    "additional": False,
})
_register("iprange.v1.recovery.inspect", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "mode": {"type": "string", "enum": ["immutable", "live", "caller_certified_offline"]},
        "validation_budget": C.VALIDATION_BUDGET,
    },
    "required": ["path", "mode", "validation_budget"],
    "additional": False,
})
_register("iprange.v1.recover", {
    "type": "object",
    "properties": {
        "source_path": C.PATH,
        "source_mode": {"type": "string", "enum": ["immutable", "live", "caller_certified_offline"]},
        "candidate": {"type": "object"},
        "destination": C.PATH,
        "recovery_budget": C.RECOVERY_BUDGET,
        "report_output": C.OUTPUT_DESCRIPTOR_JSONL,
    },
    "required": ["source_path", "source_mode", "candidate", "destination",
                 "recovery_budget", "report_output"],
    "additional": False,
})

# resolution attempts
_register("iprange.v1.commit.resolve", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "commit_result": {"type": "object"},
        "mode": {"type": "string", "enum": ["live", "immutable"]},
    },
    "required": ["path", "commit_result", "mode"],
    "additional": False,
})
_register("iprange.v1.publication.inspect", {
    "type": "object", "properties": {"path": C.PATH}, "required": ["path"], "additional": False,
})
_register("iprange.v1.publication.resolve", {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "publication_result": {"type": "object"},
        "resolution_mode": C.PUBLICATION_RESOLUTION_MODE,
    },
    "required": ["path", "resolution_mode"],
    "additional": False,
})
_register("iprange.v1.publication.residue.remove", {
    "type": "object",
    "properties": {"handle": {"type": "object"}},
    "required": ["handle"],
    "additional": False,
})

# maintenance
_register("iprange.v1.maintenance.list", {
    "type": "object",
    "properties": {
        "directory": C.PATH,
        "kinds": {
            "type": "array",
            "items": {"type": "string", "enum": ["scratch", "reservation", "publication_temp", "windows_housekeeping"]},
            "min": 1,
            "max": 4,
            "unique": True,
        },
        "max_entries": {"type": "integer", "min": 1, "max": 65536},
        "output": C.OUTPUT_DESCRIPTOR_JSONL,
    },
    "required": ["directory", "kinds", "max_entries", "output"],
    "additional": False,
})
_register("iprange.v1.maintenance.remove", {
    "type": "object",
    "properties": {"entry": {"type": "object"}},
    "required": ["entry"],
    "additional": False,
})

# cancellation notification (transport control)
_register(CANCEL_METHOD, {
    "type": "object",
    "properties": {"request_id": {"type": "any"}},
    "required": ["request_id"],
    "additional": False,
})

METHOD_NAMES = sorted(METHODS)


def validate_params(method, params):
    """Validate params against the fixed schema. Raises ValidationError."""
    from .engine import validate
    schema = METHODS[method]["params"]
    validate(params, schema, f"params[{method}]")
    return params


def known(method):
    return method in METHODS


def _self_test():
    from .engine import validate

    def rejects(method, params):
        try:
            validate_params(method, params)
        except ValidationError:
            return True
        return False

    reader = {
        "reader": "0" * 32,
        "view": {"kind": "feed", "feed": "feed-a"},
        "direction": "forward",
        "batch_size": 1,
    }
    assert validate_params("iprange.v1.reader.ranges.open", reader) == reader
    for view in (
        {"kind": "direct", "feed": "feed-a"},
        {"kind": "structured", "selection": {"mode": "all"}},
        {"kind": "feed"},
    ):
        bad = dict(reader, view=view)
        assert rejects("iprange.v1.reader.ranges.open", bad)

    export = {
        "source": {"path": "/tmp/db", "mode": "immutable"},
        "view": {"kind": "selection", "selection": {"mode": "all"}},
        "format": "netset",
        "destination": "/tmp/out",
        "publication_policy": "fail_if_exists",
        "result_budget": {"max_rows": "1", "max_output_bytes": "1", "max_open_files": 1},
    }
    assert validate_params("iprange.v1.export", export) == export
    for changes in (
        {"min_prefix": 1, "prefixes": [1, 32]},
        {"format": "ranges", "min_prefix": 1},
        {"prefixes": [1, 129]},
        {"prefixes": [1, 1]},
    ):
        bad = dict(export)
        bad.pop("min_prefix", None)
        bad.pop("prefixes", None)
        bad.update(changes)
        assert rejects("iprange.v1.export", bad)

    export["prefixes"] = [1, 32]
    assert validate_params("iprange.v1.export", export)

    maintenance = {
        "directory": "/tmp",
        "kinds": ["scratch"],
        "max_entries": 65536,
        "output": {
            "path": "/tmp/out",
            "format": "jsonl",
            "publication_policy": "fail_if_exists",
            "result_budget": {"max_rows": "1", "max_output_bytes": "1", "max_open_files": 1},
        },
    }
    assert validate_params("iprange.v1.maintenance.list", maintenance)
    assert rejects("iprange.v1.maintenance.list", dict(maintenance, max_entries=65537))
    assert rejects(
        "iprange.v1.maintenance.list",
        dict(maintenance, kinds=["scratch", "scratch"]),
    )


if __name__ == "__main__":
    _self_test()
