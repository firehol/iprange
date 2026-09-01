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


def decode_frame(line):
    """Decode one physical line into a request object or batch list.

    Raises FrameError with the standard JSON-RPC code on any envelope
    violation. Returns (elements, is_batch) where elements is a list of
    normalized request/notification dicts in array order.
    """
    if len(line.encode("utf-8")) > INPUT_FRAME_LIMIT:
        raise FrameError(TRANSPORT_FRAME_TOO_LARGE, "frame over input limit")
    # Strip one LF, then one CR (CRLF terminator). Any remaining line
    # break is embedded and unescaped, which JSON forbids.
    if line.endswith("\n"):
        line = line[:-1]
    if line.endswith("\r"):
        line = line[:-1]
    if "\n" in line or "\r" in line:
        raise FrameError(STD_PARSE_ERROR, "unescaped line break inside frame")
    try:
        value = json.loads(line)
    except json.JSONDecodeError as exc:
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


def _validate_id(value):
    if value is _MISSING:
        return _MISSING
    if isinstance(value, str):
        return value
    if isinstance(value, bool) or not isinstance(value, int):
        raise FrameError(STD_INVALID_REQUEST, "id must be a string or integral number")
    return str(value)


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
        value = json.loads(text)
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
        # The spec blesses id:null only for the transport frame-too-large
        # response (-32001); every other response must echo a real id.
        err = value.get("error") or {}
        if err.get("code") != TRANSPORT_FRAME_TOO_LARGE:
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
