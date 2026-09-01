"""Declarative strict-validation engine (standard library only)."""

import base64 as _base64
import re as _re


class ValidationError(Exception):
    """A value failed schema validation. Path shows the JSON location."""

    def __init__(self, path, problem):
        super().__init__(f"{path}: {problem}")
        self.path = path
        self.problem = problem


_ASCII_DECIMAL = _re.compile(r"[0-9]+")


def _decimal_string(value):
    return isinstance(value, str) and _ASCII_DECIMAL.fullmatch(value) is not None and (
        value == "0" or value[0] != "0"
    )


def _lower_hex(value, width=None):
    if not isinstance(value, str):
        return False
    if width is not None:
        return len(value) == width and all(c in "0123456789abcdef" for c in value)
    # Variable-length hex permits the empty string (for example, a zero-byte
    # value tag); fixed-width identities do not.
    return all(c in "0123456789abcdef" for c in value)


def _run_validator(schema, value, path):
    validator = schema.get("validator")
    if validator is not None:
        validator(value, path)


def _key_error(path, problem):
    raise ValidationError(path, problem)


def validate(value, schema, path="$"):
    """Validate value against a declarative schema. Raises ValidationError.

    Schema forms:
      {"type": "object", "properties": {...}, "required": [...], "additional": false}
      {"type": "array", "items": SCHEMA, "min": N, "max": N,
       "unique": true, "validator": FUNCTION}
      {"type": "string", "enum": [...]}
      {"type": "string", "pattern": REGEX}
      {"type": "string", "decimal": true}   canonical unsigned decimal string
                                             optional decimal_min/decimal_max
      {"type": "string", "hex": N}          N lowercase hex digits
      {"type": "string", "base64": true}    canonical RFC 4648 with padding
      {"type": "string", "max_bytes": N}  UTF-8 byte-length bound
      {"type": "string", "reject_nul": true}    reject an embedded NUL byte
      {"type": "integer", "min": N, "max": N}  JSON integer (no fraction/exponent)
      {"type": "u32"}                       alias for integer 0..4294967295
      {"type": "boolean"}
      {"type": "null"}
      {"type": "any"}
      {"type": "one_of", "options": [SCHEMA, ...]}
    """
    if not isinstance(schema, dict):
        _key_error(path, f"invalid schema {schema!r}")
    vtype = schema.get("type")

    if vtype == "object":
        if not isinstance(value, dict):
            _key_error(path, f"expected object, got {type(value).__name__}")
        props = schema.get("properties", {})
        for name in schema.get("required", []):
            if name not in value:
                _key_error(f"{path}.{name}", "missing required field")
        if schema.get("additional") is False:
            for name in value:
                if name not in props:
                    _key_error(f"{path}.{name}", "unknown member")
        for name, sub in props.items():
            if name in value:
                validate(value[name], sub, f"{path}.{name}")
        _run_validator(schema, value, path)
        return value

    if vtype == "array":
        if not isinstance(value, list):
            _key_error(path, f"expected array, got {type(value).__name__}")
        lo, hi = schema.get("min", 0), schema.get("max", 1 << 63)
        if not (lo <= len(value) <= hi):
            _key_error(path, f"array length {len(value)} outside {lo}..{hi}")
        for i, item in enumerate(value):
            validate(item, schema["items"], f"{path}[{i}]")
        if schema.get("unique"):
            seen = set()
            for item in value:
                try:
                    duplicate = item in seen
                    seen.add(item)
                except TypeError:
                    duplicate = any(item == other for other in value[: value.index(item)])
                if duplicate:
                    _key_error(path, "array values must be unique")
        _run_validator(schema, value, path)
        return value

    if vtype == "string":
        if not isinstance(value, str):
            _key_error(path, f"expected string, got {type(value).__name__}")
        if "enum" in schema and value not in schema["enum"]:
            _key_error(path, f"value {value!r} not in enum {schema['enum']}")
        if "pattern" in schema and not schema["pattern"].fullmatch(value):
            _key_error(path, f"value {value!r} does not match {schema['pattern'].pattern!r}")
        if schema.get("decimal") and not _decimal_string(value):
            _key_error(path, f"value {value!r} is not a canonical unsigned decimal string")
        if schema.get("decimal") and _decimal_string(value):
            number = int(value)
            if "decimal_min" in schema and number < schema["decimal_min"]:
                _key_error(path, f"decimal {value} below minimum {schema['decimal_min']}")
            if "decimal_max" in schema and number > schema["decimal_max"]:
                _key_error(path, f"decimal {value} above maximum {schema['decimal_max']}")
        if "hex" in schema and not _lower_hex(value, schema["hex"]):
            _key_error(path, f"value {value!r} is not {schema['hex']} lowercase hex digits")
        if schema.get("hex_even"):
            if len(value) % 2 or not _lower_hex(value):
                _key_error(path, f"value {value!r} is not even-length lowercase hex")
        if schema.get("reject_nul") and "\0" in value:
            _key_error(path, "value contains a NUL byte")
        if schema.get("base64"):
            _check_base64(value, path)
        if "min_len" in schema and len(value) < schema["min_len"]:
            _key_error(path, f"string shorter than {schema['min_len']}")
        if "max_len" in schema and len(value) > schema["max_len"]:
            _key_error(path, f"string longer than {schema['max_len']}")
        if "max_bytes" in schema and len(value.encode("utf-8")) > schema["max_bytes"]:
            _key_error(path, f"string longer than {schema['max_bytes']} UTF-8 bytes")
        _run_validator(schema, value, path)
        return value

    if vtype == "json_integer":
        # Any integral JSON number, unbounded: JSON text may carry any
        # precision and the transport accepts it as a request id.
        if isinstance(value, bool) or not isinstance(value, int):
            _key_error(path, f"expected integer, got {type(value).__name__}")
        return value

    if vtype in ("integer", "u32"):
        if isinstance(value, bool) or not isinstance(value, int):
            _key_error(path, f"expected integer, got {type(value).__name__}")
        lo = schema.get("min", 0)
        hi = schema.get("max", 4294967295 if vtype == "u32" else (1 << 63) - 1)
        if not (lo <= value <= hi):
            _key_error(path, f"integer {value} outside {lo}..{hi}")
        return value

    if vtype == "boolean":
        if not isinstance(value, bool):
            _key_error(path, f"expected boolean, got {type(value).__name__}")
        return value

    if vtype == "null":
        if value is not None:
            _key_error(path, f"expected null, got {type(value).__name__}")
        return value

    if vtype == "any":
        return value

    if vtype == "one_of":
        successes = []
        errors = []
        for option in schema["options"]:
            try:
                validate(value, option, path)
                successes.append(option)
            except ValidationError as exc:
                errors.append((option, exc))
        if len(successes) == 1:
            return value
        if len(successes) > 1:
            _key_error(path, "value matches more than one schema option")
        # Exhaustive object unions commonly have a smaller fallback shape (for
        # example, an absent lookup fact). On failure, report the error from
        # the option recognizing the most supplied members, not the fallback.
        if isinstance(value, dict) and errors:
            def specificity(item):
                option, _ = item
                properties = option.get("properties", {}) if isinstance(option, dict) else {}
                return len(set(value) & set(properties))
            _, error = max(errors, key=specificity)
            raise error
        raise errors[-1][1]

    _key_error(path, f"unsupported schema type {vtype!r}")



def _check_base64(value, path):
    try:
        decoded = _base64.b64decode(value, validate=True)
    except Exception:
        _key_error(path, "value is not canonical RFC 4648 base64")
    if _base64.b64encode(decoded).decode("ascii") != value:
        _key_error(path, "value is not canonical RFC 4648 base64")


def _self_test():
    from .common import PATH, VALUE_TAG
    from .methods import validate_params

    def rejects(schema, value):
        try:
            validate(value, schema)
        except ValidationError:
            return True
        return False

    assert validate("", {"type": "string", "base64": True}) == ""
    assert validate("Zg==", {"type": "string", "base64": True}) == "Zg=="
    for bad in ["Z", "Zg", "Zg== ", "Zg =", "Zh==", "AB", "A?B="]:
        assert rejects({"type": "string", "base64": True}, bad), bad

    decimal = {"type": "string", "decimal": True, "decimal_max": (1 << 64) - 1}
    assert rejects(decimal, "١٢٣")
    assert rejects(decimal, "01")
    assert rejects(decimal, "-1")
    assert rejects(decimal, "18446744073709551616")
    assert validate("18446744073709551615", decimal) == "18446744073709551615"
    exhaustive = {
        "type": "one_of",
        "options": [
            {"type": "object", "properties": {"present": {"type": "boolean"},
                                              "value": {"type": "u32"}},
             "required": ["present", "value"], "additional": False},
            {"type": "object", "properties": {"present": {"type": "boolean"}},
             "required": ["present"], "additional": False},
        ],
    }
    try:
        validate({"present": True, "value": 2**32}, exhaustive)
    except ValidationError as exc:
        assert "outside 0..4294967295" in str(exc)
    else:
        raise AssertionError("exhaustive union accepted an invalid direct value")

    assert rejects(PATH, "")
    assert rejects(PATH, "-")
    assert rejects(PATH, "a\0b")
    assert validate("-x", PATH) == "-x"

    assert validate({"text": ""}, VALUE_TAG) == {"text": ""}
    assert validate({"hex": ""}, VALUE_TAG) == {"hex": ""}
    assert rejects(VALUE_TAG, {"text": "a\0b"})
    assert validate({"text": "a\tb"}, VALUE_TAG) == {"text": "a\tb"}
    assert rejects(VALUE_TAG, {"hex": "0g"})
    assert rejects(VALUE_TAG, {"hex": "6100"})

    reader = "iprange.v1.reader.ranges.open"
    assert rejects(
        METHODS_SCHEMA(reader),
        {
            "reader": "0" * 32,
            "view": {"kind": "direct", "feed": "feed-a"},
            "direction": "forward",
            "batch_size": 1,
        },
    )
    assert rejects(
        METHODS_SCHEMA(reader),
        {
            "reader": "0" * 32,
            "view": {"kind": "feed"},
            "direction": "forward",
            "batch_size": 1,
        },
    )

    current = "iprange.v1.current.publish"
    params = {
        "input": {
            "paths": ["/tmp/input"],
            "family": "ipv4",
            "fix_network": False,
            "default_prefix": 32,
            "dns": {"threads": 1, "silent": True},
            "expand_at_paths": False,
            "max_line_bytes": 1,
            "max_expanded_paths": 1,
        },
        "feed": "feed-a",
        "value_tag": {"text": "tag"},
        "metadata": {"mode": "keep"},
        "destination": "/tmp/output",
        "publication_policy": "fail_if_exists",
        "immutable_feed_budget": {
            "max_heap_bytes": "1",
            "max_output_pages": "2",
            "max_workspace_pages": "2",
            "max_open_files": 3,
        },
    }
    assert rejects(METHODS_SCHEMA(current), params)
    params["metadata"] = {"mode": "clear"}
    validate_params(current, params)

    assert rejects(
        METHODS_SCHEMA("iprange.v1.history.project"),
        {
            "path": "/tmp/live",
            "last_seen": {"path": "/tmp/last", "mode": "immutable"},
            "windows": [
                {"feed": "feed-a", "cutoff": 1},
                {"feed": "feed-a", "cutoff": 2},
            ],
            "metadata": {"mode": "keep"},
            "writer_budget": {"max_heap_bytes": "1", "max_private_pages": "1",
                              "max_growth_pages": "1", "max_open_files": 2},
        },
    )

    overlap = {
        "source": {"path": "/tmp/db", "mode": "immutable"},
        "selection": {"mode": "all"},
        "membership_query_budget": {"max_heap_bytes": "1"},
        "mode": {"kind": "selected_pairs", "pairs": []},
        "output": {
            "path": "/tmp/out", "format": "csv",
            "publication_policy": "fail_if_exists",
            "result_budget": {"max_rows": "1", "max_output_bytes": "1", "max_open_files": 1},
        },
    }
    for pairs in (
        [{"left": "a", "right": "a"}],
        [{"left": "a", "right": "b"}, {"left": "b", "right": "a"}],
    ):
        overlap["mode"]["pairs"] = pairs
        assert rejects(METHODS_SCHEMA("iprange.v1.query.overlaps"), overlap)

    maintenance = {
        "directory": "/tmp",
        "kinds": ["scratch", "scratch"],
        "max_entries": 65537,
        "output": {
            "path": "/tmp/out", "format": "jsonl",
            "publication_policy": "fail_if_exists",
            "result_budget": {"max_rows": "1", "max_output_bytes": "1", "max_open_files": 1},
        },
    }
    assert rejects(METHODS_SCHEMA("iprange.v1.maintenance.list"), maintenance)

    export = {
        "source": {"path": "/tmp/db", "mode": "immutable"},
        "view": {"kind": "selection", "selection": {"mode": "all"}},
        "format": "netset",
        "destination": "/tmp/out",
        "publication_policy": "fail_if_exists",
        "result_budget": {"max_rows": "1", "max_output_bytes": "1", "max_open_files": 1},
    }
    assert validate_params("iprange.v1.export", export) == export
    both = dict(export, min_prefix=1, prefixes=[1, 32])
    assert rejects(METHODS_SCHEMA("iprange.v1.export"), both)
    wrong_format = dict(export, format="ranges", min_prefix=1)
    assert rejects(METHODS_SCHEMA("iprange.v1.export"), wrong_format)
    bad_prefix = dict(export, prefixes=[1, 129])
    assert rejects(METHODS_SCHEMA("iprange.v1.export"), bad_prefix)


def METHODS_SCHEMA(method):
    from .methods import METHODS
    return METHODS[method]["params"]


if __name__ == "__main__":
    _self_test()
