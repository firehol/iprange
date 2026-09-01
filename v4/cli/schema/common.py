"""Common v1 types shared by every method params/results schema."""

import ipaddress as _ipaddress
import re as _re

from .engine import ValidationError, validate

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
def _canonical_ip_address(value, path):
    try:
        parsed = _ipaddress.ip_address(value)
    except ValueError as exc:
        raise ValidationError(path, "value is not canonical IPv4 or IPv6 text") from exc
    if str(parsed) != value:
        raise ValidationError(path, "value is not canonical IPv4 or IPv6 text")


IP_ADDRESS = {
    "type": "string",
    "validator": _canonical_ip_address,
    "min_len": 1,
    "max_len": 64,
}

FEED_NAME = {
    "type": "string",
    "pattern": _re.compile(r"^[a-z0-9](?:[a-z0-9_.-]*[a-z0-9])?$"),
    "min_len": 1,
    "max_len": 255,
}

def _value_tag_hex(value, path):
    # Shape checks prove even lowercase hex and at most 15 bytes; decoding is
    # needed to reject a NUL byte anywhere in the represented tag.
    if 0 in bytes.fromhex(value):
        raise ValidationError(path, "value-tag hex must not encode a NUL byte")


VALUE_TAG_HEX = {
    "type": "string",
    "hex_even": True,
    "max_len": 30,
    "validator": _value_tag_hex,
}
VALUE_TAG = {
    "type": "one_of",
    "options": [
        {"type": "object",
         "properties": {"text": {"type": "string", "reject_nul": True,
                                  "max_len": 15, "max_bytes": 15}},
         "required": ["text"], "additional": False},
        {"type": "object",
         "properties": {"hex": VALUE_TAG_HEX},
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
def _require_scratch_policy(value, path):
    disabled = value["max_scratch_bytes"] == "0" and value["max_scratch_files"] == 0
    directory_present = "scratch_directory" in value
    enabled = (
        value["max_scratch_bytes"] != "0"
        and value["max_scratch_files"] != 0
        and directory_present
    )
    if (disabled and not directory_present) or enabled:
        return
    raise ValidationError(path, "scratch must be fully disabled or fully enabled")


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
        "validator": _require_scratch_policy,
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

def _require_text_input(value, path):
    family = value["family"]
    maximum = 32 if family == "ipv4" else 128
    if value["default_prefix"] > maximum:
        raise ValidationError(
            f"{path}.default_prefix",
            f"default_prefix must be 0 through {maximum} for {family}",
        )


TEXT_INPUT = {
    "type": "object",
    "properties": {
        "paths": {"type": "array", "items": PATH, "min": 1},
        "family": FAMILY,
        "fix_network": {"type": "boolean"},
        "default_prefix": {"type": "integer", "min": 0, "max": 128},
        "dns": {
            "type": "object",
            "properties": {"threads": {"type": "integer", "min": 1, "max": 2147483647},
                            "silent": {"type": "boolean"}},
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
    "validator": _require_text_input,
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
    assert validate({"text": "bad\ntag"}, VALUE_TAG) == {"text": "bad\ntag"}
    assert rejects(VALUE_TAG, {"hex": "6100"})
    assert validate({"mode": "replace_base64", "base64": ""}, METADATA_REPLACEMENT_INPUT)
    assert validate("192.0.2.1", IP_ADDRESS) == "192.0.2.1"
    assert validate("::ffff:192.0.2.1", IP_ADDRESS) == "::ffff:192.0.2.1"
    for address in ("192.000.2.1", "not-an-ip", "2001:DB8::1", "2001:0db8::1"):
        assert rejects(IP_ADDRESS, address)
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
    disabled = {"max_heap_bytes": "1", "max_open_files": 1,
                "max_scratch_bytes": "0", "max_scratch_files": 0}
    assert validate(disabled, VALIDATION_BUDGET) == disabled
    enabled = dict(disabled, max_scratch_bytes="1", max_scratch_files=1,
                   scratch_directory="/tmp/scratch")
    assert validate(enabled, VALIDATION_BUDGET) == enabled
    for bad in (
        dict(disabled, max_scratch_bytes="1"),
        dict(disabled, max_scratch_files=1),
        dict(disabled, scratch_directory="/tmp/scratch"),
        dict(enabled, max_scratch_files=0),
    ):
        assert rejects(VALIDATION_BUDGET, bad)
    text_v4 = _text_input(1)
    text_v6 = _text_input(1, family="ipv6", default_prefix=128, threads=2147483647)
    assert validate(text_v4, TEXT_INPUT) == text_v4
    assert validate(text_v6, TEXT_INPUT) == text_v6
    assert rejects(TEXT_INPUT, _text_input(1, family="ipv4", default_prefix=33))
    assert rejects(TEXT_INPUT, _text_input(1, family="ipv6", default_prefix=129))
    assert rejects(TEXT_INPUT, _text_input(1, threads=0))
    assert rejects(TEXT_INPUT, _text_input(1, threads=2147483648))
    valid_budget = {"max_rows": "1", "max_output_bytes": "1", "max_open_files": 1}
    assert validate(valid_budget, RESULT_BUDGET) == valid_budget
    for field, value in (
        ("max_rows", "0"),
        ("max_output_bytes", "0"),
        ("max_open_files", 0),
    ):
        bad = dict(valid_budget, **{field: value})
        assert rejects(RESULT_BUDGET, bad)


def _text_input(max_line_bytes, max_expanded_paths=1, family="ipv4", default_prefix=32, threads=1):
    return {
        "paths": ["/tmp/input"],
        "family": family,
        "fix_network": False,
        "default_prefix": default_prefix,
        "dns": {"threads": threads, "silent": True},
        "expand_at_paths": False,
        "max_line_bytes": max_line_bytes,
        "max_expanded_paths": max_expanded_paths,
    }


if __name__ == "__main__":
    _self_test()
