"""JSON-RPC 2.0 frame and envelope validation for the iprange v1 API.

Implements the transport contract of iprange-jsonrpc-v1.md:

- one physical line per request object or batch array; LF/CRLF input,
  LF output;
- input/output frame ceiling 1,048,576 bytes; response object ceiling
  65,000 bytes;
- id is a string or integral JSON number; null/fractional/exponent ids
  are invalid; a missing id is a notification and only
  iprange.v1.cancel is an accepted notification;
- unknown request members rejected; params must be an object;
- batch 1..16 elements, array-order execution, notifications omitted
  from the response array.

Clients use this module to validate every server response; servers use
it to validate every request before dispatch.
"""
import json
import re

from .engine import ValidationError

INPUT_FRAME_LIMIT = 1_048_576
OUTPUT_FRAME_LIMIT = 1_048_576
RESPONSE_OBJECT_LIMIT = 65_000
BATCH_LIMIT = 16
QUEUED_LIMIT = 16

METHOD_PREFIX = "iprange.v1."
CANCEL_METHOD = "iprange.v1.cancel"

_INTEGER_RE = re.compile(r"^-?[0-9]+$")

STD_PARSE_ERROR = -32700
STD_INVALID_REQUEST = -32600
STD_METHOD_NOT_FOUND = -32601
STD_INVALID_PARAMS = -32602
STD_INTERNAL_ERROR = -32603
TRANSPORT_FRAME_TOO_LARGE = -32001
TRANSPORT_SERVER_BUSY = -32002
PRODUCT_ERROR = -32010


class FrameError(Exception):
    """A malformed or invalid frame. Carries the JSON-RPC error code."""

    def __init__(self, code, message, *, request_id=None):
        super().__init__(message)
        self.code = code
        self.message = message
        self.request_id = request_id


# The transport accepts integral JSON numbers of any length; CPython
# caps int conversion at 4300 digits by default, so lift the limit.
import sys as _sys

if hasattr(_sys, "set_int_max_str_digits"):
    _sys.set_int_max_str_digits(0)


def decode_frame(line):
    """Decode one physical line into a request object or batch list.

    Raises FrameError with the standard JSON-RPC code on any envelope
    violation. Returns (elements, is_batch) where elements is a list of
    normalized request/notification dicts in array order.
    """
    # The byte ceiling applies to JSON payload bytes, not the LF/CRLF physical
    # terminator. LF alone or CR immediately before LF is a terminator; a bare
    # trailing CR remains payload and is rejected below as an unescaped break.
    if isinstance(line, (bytes, bytearray)):
        payload = bytes(line)
        if payload.endswith(b"\n"):
            payload = payload[:-1]
            if payload.endswith(b"\r"):
                payload = payload[:-1]
        try:
            text = payload.decode("utf-8", "strict")
        except UnicodeDecodeError as exc:
            raise FrameError(STD_PARSE_ERROR, "frame is not UTF-8") from exc
    elif isinstance(line, str):
        text = line
        if text.endswith("\n"):
            text = text[:-1]
            if text.endswith("\r"):
                text = text[:-1]
        try:
            payload = text.encode("utf-8")
        except UnicodeEncodeError as exc:
            raise FrameError(STD_PARSE_ERROR, "frame is not UTF-8") from exc
    else:
        raise FrameError(STD_INVALID_REQUEST, "frame must be text or bytes")
    if len(payload) > INPUT_FRAME_LIMIT:
        raise FrameError(TRANSPORT_FRAME_TOO_LARGE, "frame over input limit")
    if "\n" in text or "\r" in text:
        raise FrameError(STD_PARSE_ERROR, "unescaped line break inside frame")
    try:
        # Python's json decoder accepts NaN/Infinity by default; the
        # transport is strict JSON (JSON-RPC 2.0), so reject them.
        value = json.loads(text, parse_constant=_reject_nonfinite)
    except json.JSONDecodeError as exc:
        raise FrameError(STD_PARSE_ERROR, f"parse error: {exc}") from exc
    except ValueError as exc:
        # json.JSONDecodeError subclasses ValueError; other ValueErrors
        # (for example CPython's 4300-digit int conversion limit) are
        # still frame parse failures.
        raise FrameError(STD_PARSE_ERROR, f"parse error: {exc}") from exc

    if isinstance(value, list):
        if not (1 <= len(value) <= BATCH_LIMIT):
            raise FrameError(STD_INVALID_REQUEST, f"batch length {len(value)} outside 1..{BATCH_LIMIT}")
        elements = [_validate_envelope(item, index=i) for i, item in enumerate(value)]
        return elements, True
    elements = [_validate_envelope(value, index=None)]
    return elements, False


def _validate_envelope(item, *, index):
    """Validate one request/notification envelope. Returns normalized dict."""
    if not isinstance(item, dict):
        raise FrameError(STD_INVALID_REQUEST, "request must be an object")
    known = {"jsonrpc", "id", "method", "params"}
    extra = set(item) - known
    if extra:
        raise FrameError(STD_INVALID_REQUEST, f"unknown request members: {sorted(extra)}")
    if item.get("jsonrpc") != "2.0":
        raise FrameError(STD_INVALID_REQUEST, "jsonrpc must be \"2.0\"")
    method = item.get("method")
    if not isinstance(method, str) or not method.startswith(METHOD_PREFIX):
        raise FrameError(STD_METHOD_NOT_FOUND
                         if isinstance(method, str) else STD_INVALID_REQUEST,
                         "method must start with iprange.v1.")
    # The v1 contract requires object-valued params on every request.
    if "params" not in item or item["params"] is None or not isinstance(item["params"], dict):
        raise FrameError(STD_INVALID_PARAMS, "params must be an object")

    request_id = _validate_id(item.get("id", _MISSING))
    if request_id is _MISSING:
        if method != CANCEL_METHOD:
            raise FrameError(STD_INVALID_REQUEST,
                             "notifications are not accepted except iprange.v1.cancel")
        normalized = {"jsonrpc": "2.0", "method": method, "params": item["params"], "id": None}
    else:
        normalized = {"jsonrpc": "2.0", "id": request_id, "method": method,
                      "params": item["params"]}
    if index is not None:
        normalized["_batch_index"] = index
    return normalized


_MISSING = object()


def _reject_nonfinite(token):
    raise json.JSONDecodeError(f"non-standard JSON constant {token}", "", 0)


def _validate_id(value):
    if value is _MISSING:
        return _MISSING
    if isinstance(value, str):
        return value
    if isinstance(value, bool) or not isinstance(value, int):
        raise FrameError(STD_INVALID_REQUEST, "id must be a string or integral number")
    return str(value)


def _self_test():
    import json as json_module

    def request(identifier_length):
        identifier = "x" * identifier_length
        return json_module.dumps({
            "jsonrpc": "2.0",
            "id": identifier,
            "method": "iprange.v1.system.describe",
            "params": {},
        }, separators=(",", ":"))

    base_length = len(request(0).encode("utf-8"))
    exact = request(INPUT_FRAME_LIMIT - base_length)
    assert len(exact.encode("utf-8")) == INPUT_FRAME_LIMIT
    for terminator in ("\n", "\r\n"):
        elements, batch = decode_frame(exact + terminator)
        assert not batch and elements[0]["method"] == "iprange.v1.system.describe"

    over = request(INPUT_FRAME_LIMIT - base_length + 1) + "\n"
    try:
        decode_frame(over)
    except FrameError as exc:
        assert exc.code == TRANSPORT_FRAME_TOO_LARGE
    else:
        raise AssertionError("over-limit payload was accepted")

    utf8_request = request(1).encode("utf-8")
    for encoding in ("utf-16le", "utf-16be", "utf-32le", "utf-32be"):
        try:
            decode_frame(request(1).encode(encoding) + b"\n")
        except FrameError as exc:
            assert exc.code == STD_PARSE_ERROR
        else:
            raise AssertionError(f"{encoding} frame was accepted")

    try:
        decode_frame(b'\xff\n')
    except FrameError as exc:
        assert exc.code == STD_PARSE_ERROR
    else:
        raise AssertionError("non-UTF-8 payload was accepted")

    for bare_cr in (utf8_request + b"\r", utf8_request.decode() + "\r"):
        try:
            decode_frame(bare_cr)
        except FrameError as exc:
            assert exc.code == STD_PARSE_ERROR
            assert "unescaped line break" in str(exc)
        else:
            raise AssertionError("bare-CR terminator was accepted")


def decode_response(text):
    """Validate one server response object. Returns the response dict.

    Enforces the documented envelope: jsonrpc exactly "2.0"; string or
    integral id; no unknown members; exactly one of result/error; error
    object with integer code, string message, and optional data.
    Raises FrameError on any violation.
    """
    if len(text.encode("utf-8")) > OUTPUT_FRAME_LIMIT:
        raise FrameError(TRANSPORT_FRAME_TOO_LARGE, "response frame over output limit")
    try:
        # Python's json decoder accepts NaN/Infinity by default; the
        # transport is strict JSON (JSON-RPC 2.0), so reject them.
        value = json.loads(text, parse_constant=_reject_nonfinite)
    except json.JSONDecodeError as exc:
        raise FrameError(STD_PARSE_ERROR, f"parse error: {exc}") from exc
    if not isinstance(value, dict):
        raise FrameError(STD_INVALID_REQUEST, "response must be an object")
    unknown = set(value) - {"jsonrpc", "id", "result", "error"}
    if unknown:
        raise FrameError(STD_INVALID_REQUEST, f"unknown response members: {sorted(unknown)}")
    if value.get("jsonrpc") != "2.0":
        raise FrameError(STD_INVALID_REQUEST, "jsonrpc must be \"2.0\"")
    if "id" not in value:
        raise FrameError(STD_INVALID_REQUEST, "response id is required")
    if value["id"] is None:
        # The spec blesses id:null for responses whose id cannot be
        # echoed: the transport frame-too-large response (-32001) and
        # the invalid-notification response (-32600, an id-less
        # non-cancel request is answered with id null).
        err = value.get("error") or {}
        if err.get("code") not in (TRANSPORT_FRAME_TOO_LARGE, STD_INVALID_REQUEST):
            raise FrameError(STD_INVALID_REQUEST, "response id must be a string or integral number")
    else:
        _validate_id(value["id"])
    if ("result" in value) == ("error" in value):
        raise FrameError(STD_INVALID_REQUEST, "response needs exactly one of result/error")
    if "error" in value:
        err = value["error"]
        if not isinstance(err, dict):
            raise FrameError(STD_INVALID_REQUEST, "error must be an object")
        unknown = set(err) - {"code", "message", "data"}
        if unknown or not isinstance(err.get("code"), int) \
                or isinstance(err.get("code"), bool) \
                or not isinstance(err.get("message"), str):
            raise FrameError(STD_INVALID_REQUEST, "malformed error object")
    return value


def encode_response_object(payload):
    """Serialize one response object (result or error) with the 65,000
    byte object ceiling. Raises FrameError(TRANSPORT_FRAME_TOO_LARGE)
    when the object exceeds the ceiling."""
    text = json.dumps(payload, separators=(",", ":"), ensure_ascii=False)
    if len(text.encode("utf-8")) > RESPONSE_OBJECT_LIMIT:
        raise FrameError(TRANSPORT_FRAME_TOO_LARGE, "response object over 65,000-byte limit",
                         request_id=payload.get("id"))
    return text


def encode_response_frame(payload_or_list):
    """Serialize one response frame with the 1,048,576 byte ceiling."""
    text = json.dumps(payload_or_list, separators=(",", ":"), ensure_ascii=False)
    if len(text.encode("utf-8")) > OUTPUT_FRAME_LIMIT:
        raise FrameError(TRANSPORT_FRAME_TOO_LARGE, "response frame over 1,048,576-byte limit")
    return text + "\n"


def success_response(request_id, result):
    return {"jsonrpc": "2.0", "id": request_id, "result": result}


def error_response(request_id, code, message, data=None):
    payload = {"jsonrpc": "2.0", "id": request_id, "error": {"code": code, "message": message}}
    if data is not None:
        payload["error"]["data"] = data
    return payload


def product_error_response(request_id, domain_code, message, outcome, details=None):
    data = {"code": domain_code, "outcome": outcome}
    if details is not None:
        data["details"] = details
    return error_response(request_id, PRODUCT_ERROR, message, data)


if __name__ == "__main__":
    _self_test()
