"""Declarative strict-validation engine (standard library only)."""


class ValidationError(Exception):
    """A value failed schema validation. Path shows the JSON location."""

    def __init__(self, path, problem):
        super().__init__(f"{path}: {problem}")
        self.path = path
        self.problem = problem


def _decimal_string(value):
    return isinstance(value, str) and value.isdigit() and (value == "0" or value[0] != "0")


def _lower_hex(value, width=None):
    if not isinstance(value, str) or not value:
        return False
    if width is not None and len(value) != width:
        return False
    return all(c in "0123456789abcdef" for c in value)


def _key_error(path, problem):
    raise ValidationError(path, problem)


def validate(value, schema, path="$"):
    """Validate value against a declarative schema. Raises ValidationError.

    Schema forms:
      {"type": "object", "properties": {...}, "required": [...], "additional": false}
      {"type": "array", "items": SCHEMA, "min": N, "max": N}
      {"type": "string", "enum": [...]}
      {"type": "string", "pattern": REGEX}
      {"type": "string", "decimal": true}   canonical unsigned decimal string
      {"type": "string", "hex": N}          N lowercase hex digits
      {"type": "string", "base64": true}    canonical RFC 4648 with padding
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
        return value

    if vtype == "array":
        if not isinstance(value, list):
            _key_error(path, f"expected array, got {type(value).__name__}")
        lo, hi = schema.get("min", 0), schema.get("max", 1 << 63)
        if not (lo <= len(value) <= hi):
            _key_error(path, f"array length {len(value)} outside {lo}..{hi}")
        for i, item in enumerate(value):
            validate(item, schema["items"], f"{path}[{i}]")
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
        if "hex" in schema and not _lower_hex(value, schema["hex"]):
            _key_error(path, f"value {value!r} is not {schema['hex']} lowercase hex digits")
        if schema.get("hex_even"):
            if len(value) % 2 or not _lower_hex(value):
                _key_error(path, f"value {value!r} is not even-length lowercase hex")
        if schema.get("base64"):
            _check_base64(value, path)
        if "min_len" in schema and len(value) < schema["min_len"]:
            _key_error(path, f"string shorter than {schema['min_len']}")
        if "max_len" in schema and len(value) > schema["max_len"]:
            _key_error(path, f"string longer than {schema['max_len']}")
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
        last = None
        for option in schema["options"]:
            try:
                validate(value, option, path)
                return value
            except ValidationError as exc:
                last = exc
        raise last

    _key_error(path, f"unsupported schema type {vtype!r}")


_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"


def _check_base64(value, path):
    if not value or len(value) % 4:
        _key_error(path, "base64 length must be a nonzero multiple of 4")
    pad = value.count("=")
    if pad not in (0, 1, 2):
        _key_error(path, "invalid base64 padding")
    if pad and not value.endswith("=" * pad):
        _key_error(path, "base64 padding must be trailing")
    body = value[:-pad] if pad else value
    if any(c not in _ALPHABET for c in body):
        _key_error(path, "invalid base64 alphabet character")
    # Canonical: trailing bits in the last non-pad group must be zero.
    if pad:
        last = body[-1]
        leftover = 8 - pad * 2
        if leftover == 2 and _ALPHABET.index(last) & 0x3:
            _key_error(path, "non-canonical base64 trailing bits")
        if leftover == 4 and _ALPHABET.index(last) & 0xF:
            _key_error(path, "non-canonical base64 trailing bits")
