"""Common v1 types shared by every method params/results schema."""

import re as _re

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

PATH = {
    "type": "string",
    "pattern": _re.compile(r"(?!-$)[^\x00]+"),
    "min_len": 1,
    "max_len": 65536,
}
HANDLE = {"type": "string", "hex": 32}
# Result cardinalities can exceed 64 bits; parameters using the common U64
# vocabulary are bounded separately by PARAM_U64.
U64 = {"type": "string", "decimal": True}
PARAM_U64 = {"type": "string", "decimal": True, "decimal_max": (1 << 64) - 1}
POSITIVE_U64 = {
    "type": "string",
    "decimal": True,
    "decimal_min": 1,
    "decimal_max": (1 << 64) - 1,
}
U32 = {"type": "u32"}
POSITIVE_U32 = {"type": "integer", "min": 1, "max": 4294967295}
IP_ADDRESS = {"type": "string", "min_len": 1, "max_len": 64}

FEED_NAME = {
    "type": "string",
    "pattern": _re.compile(r"^[a-z0-9](?:[a-z0-9_.-]*[a-z0-9])?$"),
    "min_len": 1,
    "max_len": 255,
}

VALUE_TAG = {
    "type": "one_of",
    "options": [
        {"type": "object",
         "properties": {"text": {"type": "string", "no_control": True, "max_len": 15, "max_bytes": 15}},
         "required": ["text"], "additional": False},
        {"type": "object",
         "properties": {"hex": {"type": "string", "hex_even": True, "max_len": 30}},
         "required": ["hex"], "additional": False},
    ],
}

def _metadata_form(mode, extra):
    option = {"type": "object",
              "properties": {"mode": {"type": "string", "enum": [mode]}},
              "required": ["mode"], "additional": False}
    for name, sub in extra.items():
        option["properties"][name] = sub
        option["required"].append(name)
    return option


METADATA_INPUT = {
    "type": "one_of",
    "options": [
        _metadata_form("keep", {}),
        _metadata_form("clear", {}),
        _metadata_form("replace_utf8", {"text": {"type": "string"}}),
        _metadata_form("replace_base64", {"base64": {"type": "string", "base64": True}}),
        _metadata_form("replace_file", {"path": PATH}),
    ],
}

# Methods that create a new immutable destination cannot inherit metadata;
# "keep" is therefore excluded. Empty replace_base64 is the valid empty blob.
METADATA_REPLACEMENT_INPUT = {
    "type": "one_of",
    "options": [
        _metadata_form("clear", {}),
        _metadata_form("replace_utf8", {"text": {"type": "string"}}),
        _metadata_form("replace_base64", {"base64": {"type": "string", "base64": True}}),
        _metadata_form("replace_file", {"path": PATH}),
    ],
}

WRITER_BUDGET = {
    "type": "object",
    "properties": {
        "max_heap_bytes": POSITIVE_U64,
        "max_private_pages": POSITIVE_U64,
        "max_growth_pages": POSITIVE_U64,
        "max_open_files": POSITIVE_U32,
    },
    "required": ["max_heap_bytes", "max_private_pages", "max_growth_pages", "max_open_files"],
    "additional": False,
}
SNAPSHOT_BUDGET = {
    "type": "object",
    "properties": {
        "max_heap_bytes": POSITIVE_U64,
        "max_output_pages": POSITIVE_U64,
        "max_open_files": POSITIVE_U32,
    },
    "required": ["max_heap_bytes", "max_output_pages", "max_open_files"],
    "additional": False,
}
def scratch_fields(*, recovery):
    names = ["max_scratch_bytes", "max_scratch_files", "scratch_directory"]
    props = {
        "max_scratch_bytes": PARAM_U64,
        "max_scratch_files": U32,
        "scratch_directory": PATH,
    }
    base = {
        "max_heap_bytes": POSITIVE_U64,
        "max_open_files": POSITIVE_U32,
    }
    if recovery:
        base["max_output_pages"] = POSITIVE_U64
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
    "properties": {"max_heap_bytes": POSITIVE_U64},
    "required": ["max_heap_bytes"],
    "additional": False,
}
ALGEBRA_BUDGET = {
    "type": "object",
    "properties": {"max_heap_bytes": POSITIVE_U64, "max_sources": POSITIVE_U32},
    "required": ["max_heap_bytes", "max_sources"],
    "additional": False,
}
ALGEBRA_OUTPUT_BUDGET = {
    "type": "object",
    "properties": {"max_output_pages": POSITIVE_U64, "max_open_files": POSITIVE_U32},
    "required": ["max_output_pages", "max_open_files"],
    "additional": False,
}
RESULT_BUDGET = {
    "type": "object",
    "properties": {
        "max_rows": POSITIVE_U64,
        "max_output_bytes": POSITIVE_U64,
        "max_open_files": POSITIVE_U32,
    },
    "required": ["max_rows", "max_output_bytes", "max_open_files"],
    "additional": False,
}
IMMUTABLE_FEED_BUDGET = {
    "type": "object",
    "properties": {
        "max_heap_bytes": POSITIVE_U64,
        "max_output_pages": POSITIVE_U64,
        "max_workspace_pages": POSITIVE_U64,
        "max_open_files": POSITIVE_U32,
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
                "feeds": {"type": "array", "items": FEED_NAME, "min": 1, "unique": True},
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
        "max_line_bytes": {"type": "integer", "min": 1, "max": 1048576},
        "max_expanded_paths": {"type": "integer", "min": 1, "max": 1000000},
    },
    "required": ["paths", "family", "fix_network", "default_prefix", "dns", "expand_at_paths",
                 "max_line_bytes", "max_expanded_paths"],
    "additional": False,
}

DIRECT_INPUT = {
    "type": "object",
    "properties": {"path": PATH, "max_line_bytes": {"type": "integer", "min": 1, "max": 1048576}},
    "required": ["path", "max_line_bytes"],
    "additional": False,
}

def _output_descriptor(formats):
    return {
        "type": "object",
        "properties": {
            "path": PATH,
            "format": {"type": "string", "enum": formats},
            "publication_policy": PUBLICATION_POLICY,
            "result_budget": RESULT_BUDGET,
        },
        "required": ["path", "format", "publication_policy", "result_budget"],
        "additional": False,
    }


# Most tabular methods accept JSONL or CSV. validate, recover, and
# maintenance.list are JSONL-only because their rows carry nested
# evidence that must not be flattened.
OUTPUT_DESCRIPTOR = _output_descriptor(["jsonl", "csv"])
OUTPUT_DESCRIPTOR_JSONL = _output_descriptor(["jsonl"])

METADATA_DELIVERY = {
    "type": "one_of",
    "options": [
        {"type": "object",
         "properties": {"mode": {"type": "string", "enum": ["inline"]}},
         "required": ["mode"], "additional": False},
        {"type": "object",
         "properties": {
             "mode": {"type": "string", "enum": ["file"]},
             "path": PATH,
             "publication_policy": PUBLICATION_POLICY,
             "max_output_bytes": POSITIVE_U64,
             "max_open_files": POSITIVE_U32,
         },
         "required": ["mode", "path", "publication_policy", "max_output_bytes", "max_open_files"],
         "additional": False},
    ],
}


def _self_test():
    from .engine import ValidationError

    def rejects(schema, value):
        try:
            validate(value, schema)
        except ValidationError:
            return True
        return False

    assert validate("18446744073709551615", PARAM_U64) == "18446744073709551615"
    assert rejects(PARAM_U64, "18446744073709551616")
    assert rejects(PATH, "-")
    assert rejects(PATH, "a\x00b")
    assert validate({"text": ""}, VALUE_TAG) == {"text": ""}
    assert rejects(VALUE_TAG, {"text": "bad\x00tag"})
    assert rejects(VALUE_TAG, {"text": "bad\ntag"})
    assert validate({"mode": "replace_base64", "base64": ""}, METADATA_REPLACEMENT_INPUT)
    assert rejects(METADATA_REPLACEMENT_INPUT, {"mode": "keep"})
    assert validate({
        "mode": "named", "feeds": ["feed-a", "feed-b"]
    }, FEED_SELECTION)
    assert rejects(FEED_SELECTION, {
        "mode": "named", "feeds": ["feed-a", "feed-a"]
    })
    assert rejects(TEXT_INPUT, _text_input(max_line_bytes=0))
    assert rejects(TEXT_INPUT, _text_input(max_line_bytes=1048577))
    assert rejects(TEXT_INPUT, _text_input(1, max_expanded_paths=0))
    assert rejects(TEXT_INPUT, _text_input(1, max_expanded_paths=1000001))
    assert rejects(DIRECT_INPUT, {"path": "/tmp/in", "max_line_bytes": 0})
    assert rejects(DIRECT_INPUT, {"path": "/tmp/in", "max_line_bytes": 1048577})
    valid_budget = {"max_rows": "1", "max_output_bytes": "1", "max_open_files": 1}
    assert validate(valid_budget, RESULT_BUDGET) == valid_budget
    for field, value in (
        ("max_rows", "0"),
        ("max_output_bytes", "0"),
        ("max_open_files", 0),
    ):
        bad = dict(valid_budget, **{field: value})
        assert rejects(RESULT_BUDGET, bad)


def _text_input(max_line_bytes, max_expanded_paths=1):
    return {
        "paths": ["/tmp/input"],
        "family": "ipv4",
        "fix_network": False,
        "default_prefix": 32,
        "dns": {"threads": 1, "silent": True},
        "expand_at_paths": False,
        "max_line_bytes": max_line_bytes,
        "max_expanded_paths": max_expanded_paths,
    }


if __name__ == "__main__":
    _self_test()
