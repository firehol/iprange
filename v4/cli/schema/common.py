"""Common v1 types shared by every method params/results schema."""

from .engine import validate

FAMILY = {"type": "string", "enum": ["ipv4", "ipv6"]}
SOURCE_MODE = {"type": "string", "enum": ["immutable", "live"]}
VALUE_KIND = {"type": "string", "enum": ["direct", "membership", "structured"]}
STRUCTURE_KIND = {"type": "string", "enum": ["none", "network_enrichment_v1"]}
DIRECTION = {"type": "string", "enum": ["forward", "reverse"]}
PUBLICATION_POLICY = {
    "type": "string",
    "enum": ["fail_if_exists", "replace_existing", "replace_existing_no_rollback"],
}
PUBLICATION_RESOLUTION_MODE = {"type": "string", "enum": ["complete", "remove"]}
LIVE_TRANSITION_RESOLUTION_MODE = {"type": "string", "enum": ["complete", "rollback"]}
LOGICAL_CHANGE = {"type": "string", "enum": ["changed", "unchanged"]}

PATH = {"type": "string", "min_len": 1, "max_len": 65536}
HANDLE = {"type": "string", "hex": 32}
U64 = {"type": "string", "decimal": True}
U32 = {"type": "u32"}
IP_ADDRESS = {"type": "string", "min_len": 1, "max_len": 64}

import re as _re
FEED_NAME = {
    "type": "string",
    "pattern": _re.compile(r"^[a-z0-9](?:[a-z0-9_.-]*[a-z0-9])?$"),
    "min_len": 1,
    "max_len": 255,
}

VALUE_TAG = {
    "type": "object",
    "properties": {
        "text": {"type": "string", "max_len": 15},
        "hex": {"type": "string", "hex_even": True, "max_len": 30},
    },
    "required": [],
    "additional": False,
}

METADATA_INPUT = {
    "type": "object",
    "properties": {
        "mode": {"type": "string", "enum": ["keep", "clear", "replace_utf8", "replace_base64", "replace_file"]},
        "text": {"type": "string"},
        "base64": {"type": "string", "base64": True},
        "path": PATH,
    },
    "required": ["mode"],
    "additional": False,
}

WRITER_BUDGET = {
    "type": "object",
    "properties": {
        "max_heap_bytes": U64,
        "max_private_pages": U64,
        "max_growth_pages": U64,
        "max_open_files": U32,
    },
    "required": ["max_heap_bytes", "max_private_pages", "max_growth_pages", "max_open_files"],
    "additional": False,
}
SNAPSHOT_BUDGET = {
    "type": "object",
    "properties": {
        "max_heap_bytes": U64,
        "max_output_pages": U64,
        "max_open_files": U32,
    },
    "required": ["max_heap_bytes", "max_output_pages", "max_open_files"],
    "additional": False,
}
def scratch_fields(*, recovery):
    names = ["max_scratch_bytes", "max_scratch_files", "scratch_directory"]
    props = {
        "max_scratch_bytes": U64,
        "max_scratch_files": U32,
        "scratch_directory": PATH,
    }
    base = {
        "max_heap_bytes": U64,
        "max_open_files": U32,
    }
    if recovery:
        base["max_output_pages"] = U64
    props.update(base)
    return {
        "type": "object",
        "properties": props,
        "required": list(base) + ["max_scratch_bytes", "max_scratch_files"],
        "additional": False,
    }


VALIDATION_BUDGET = scratch_fields(recovery=False)
RECOVERY_BUDGET = scratch_fields(recovery=True)
MEMBERSHIP_QUERY_BUDGET = {
    "type": "object",
    "properties": {"max_heap_bytes": U64},
    "required": ["max_heap_bytes"],
    "additional": False,
}
ALGEBRA_BUDGET = {
    "type": "object",
    "properties": {"max_heap_bytes": U64, "max_sources": U32},
    "required": ["max_heap_bytes", "max_sources"],
    "additional": False,
}
ALGEBRA_OUTPUT_BUDGET = {
    "type": "object",
    "properties": {"max_output_pages": U64, "max_open_files": U32},
    "required": ["max_output_pages", "max_open_files"],
    "additional": False,
}
RESULT_BUDGET = {
    "type": "object",
    "properties": {
        "max_rows": U64,
        "max_output_bytes": U64,
        "max_open_files": U32,
    },
    "required": ["max_rows", "max_output_bytes", "max_open_files"],
    "additional": False,
}
IMMUTABLE_FEED_BUDGET = {
    "type": "object",
    "properties": {
        "max_heap_bytes": U64,
        "max_output_pages": U64,
        "max_workspace_pages": U64,
        "max_open_files": U32,
    },
    "required": ["max_heap_bytes", "max_output_pages", "max_workspace_pages", "max_open_files"],
    "additional": False,
}

DATABASE_SOURCE = {
    "type": "object",
    "properties": {"path": PATH, "mode": SOURCE_MODE},
    "required": ["path", "mode"],
    "additional": False,
}

FEED_SELECTION = {
    "type": "one_of",
    "options": [
        {"type": "object", "properties": {"mode": {"type": "string", "enum": ["all"]}}, "required": ["mode"], "additional": False},
        {
            "type": "object",
            "properties": {
                "mode": {"type": "string", "enum": ["named"]},
                "feeds": {"type": "array", "items": FEED_NAME, "min": 1},
            },
            "required": ["mode", "feeds"],
            "additional": False,
        },
    ],
}

CURRENT_COVERAGE_SOURCE = {
    "type": "object",
    "properties": {"source": DATABASE_SOURCE, "feed": FEED_NAME},
    "required": ["source", "feed"],
    "additional": False,
}

TEXT_INPUT = {
    "type": "object",
    "properties": {
        "paths": {"type": "array", "items": PATH, "min": 1},
        "family": FAMILY,
        "fix_network": {"type": "boolean"},
        "default_prefix": U32,
        "dns": {
            "type": "object",
            "properties": {"threads": U32, "silent": {"type": "boolean"}},
            "required": ["threads", "silent"],
            "additional": False,
        },
        "expand_at_paths": {"type": "boolean"},
        "max_line_bytes": U32,
        "max_expanded_paths": U32,
    },
    "required": ["paths", "family", "fix_network", "default_prefix", "dns", "expand_at_paths",
                 "max_line_bytes", "max_expanded_paths"],
    "additional": False,
}

DIRECT_INPUT = {
    "type": "object",
    "properties": {"path": PATH, "max_line_bytes": U32},
    "required": ["path", "max_line_bytes"],
    "additional": False,
}

OUTPUT_DESCRIPTOR = {
    "type": "object",
    "properties": {
        "path": PATH,
        "format": {"type": "string", "enum": ["jsonl", "csv"]},
        "publication_policy": PUBLICATION_POLICY,
        "result_budget": RESULT_BUDGET,
    },
    "required": ["path", "format", "publication_policy", "result_budget"],
    "additional": False,
}

METADATA_DELIVERY = {
    "type": "object",
    "properties": {
        "mode": {"type": "string", "enum": ["inline", "file"]},
        "path": PATH,
        "publication_policy": PUBLICATION_POLICY,
        "max_output_bytes": U64,
        "max_open_files": U32,
    },
    "required": ["mode"],
    "additional": False,
}
