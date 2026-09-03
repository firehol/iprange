// Strict v1 envelope decoding, response encoding, and error payloads
// (iprange-jsonrpc-v1.md): requests carry `jsonrpc:"2.0"`, a string
// or integral id, a method with the `iprange.v1.` prefix, and
// object params; unknown members are rejected. Only the cancel
// notification may omit the id. Batches hold 1..=16 objects and
// execute in array order with valid notifications excluded from the
// response array. Responses require an id and exactly one of
// result/error; error objects carry integer code plus string message
// plus optional data. Ceilings: 65,000 bytes per response object and
// 1,048,576 bytes per response frame.

package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const (
	StdParseError          int64 = -32700
	StdInvalidRequest      int64 = -32600
	StdMethodNotFound      int64 = -32601
	StdInvalidParams       int64 = -32602
	TransportFrameTooLarge int64 = -32001
	TransportServerBusy    int64 = -32002
	ProductError           int64 = -32010

	MethodPrefix = "iprange.v1."
	CancelMethod = "iprange.v1.cancel"
)

// RequestId is an exact JSON string or an integral JSON number. The
// raw text is preserved so every integral literal the client sends is
// echoed byte-for-byte, except the literal -0, which is echoed as 0 to
// match the Rust serde_json normalization (both the response echo and
// the cancellation correlation key use the same canonical form).
type RequestId struct {
	// IsString selects the string form; otherwise the id is a number.
	IsString bool
	// Text is the exact JSON text (without quotes for strings).
	Text string
}

func RequestIdFromString(s string) RequestId { return RequestId{IsString: true, Text: s} }
func RequestIdFromNumber(n string) RequestId { return RequestId{IsString: false, Text: n} }

// AsJSON renders the id as a JSON value.
func (id RequestId) AsJSON() json.RawMessage {
	if id.IsString {
		b, _ := Marshal(id.Text)
		return b
	}
	return json.RawMessage(id.Text)
}

// Key is the canonical correlation key for cancellation and busy
// tracking: string ids and integral ids with the same text never
// collide.
func (id RequestId) Key() string {
	if id.IsString {
		return "s:" + id.Text
	}
	return "n:" + id.Text
}

// Request is one decoded, envelope-valid request or notification.
type Request struct {
	ID     *RequestId // nil for notifications
	Method string
	Params json.RawMessage
	// BatchIndex is set for members of a batch array.
	BatchIndex *int
}

// SchemaError is an envelope-level failure with a stable JSON-RPC
// code. Parse errors are -32700, invalid requests -32600.
type SchemaError struct {
	Code    int64
	Message string
}

func ParseError(message string) *SchemaError {
	return &SchemaError{Code: StdParseError, Message: message}
}
func InvalidRequest(message string) *SchemaError {
	return &SchemaError{Code: StdInvalidRequest, Message: message}
}

// Response builds the error envelope for id (or id null).
func (err *SchemaError) Response(id *RequestId) json.RawMessage {
	m := map[string]any{
		"jsonrpc": "2.0",
		"error":   map[string]any{"code": err.Code, "message": err.Message},
	}
	if id == nil {
		m["id"] = nil
	} else {
		m["id"] = json.RawMessage(id.AsJSON())
	}
	return mustMarshal(m)
}

// integralText reports whether the JSON number raw text is an integer
// literal (no '.', 'e', or 'E').
func integralText(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return false
	}
	for _, c := range t {
		switch c {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '-', '+':
		default:
			return false
		}
	}
	return true
}

// isObjectRaw reports whether the raw JSON value is an object.
func isObjectRaw(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

// isStringRaw reports whether the raw JSON value is a string.
func isStringRaw(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '"'
}

// isRawNull reports whether the raw JSON value is null.
func isRawNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// validCancelParams implements the strict CANCEL schema: params must
// be an object with exactly one member `request_id` whose value is a
// string or an integral JSON number. The transport ignores an invalid
// cancel notification (it is a notification: no cancellation, no
// response).
func validCancelParams(params json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err != nil || len(obj) != 1 {
		return false
	}
	raw, ok := obj["request_id"]
	if !ok {
		return false
	}
	if isStringRaw(raw) {
		return true
	}
	return isIntegralNumberRaw(raw)
}

// canonicalIntegralText returns the canonical text of an integral JSON
// number. serde_json normalizes the literal -0 to 0, so both the
// request-id echo and the cancel correlation key must use this same
// canonical text for same-batch cancellation to be byte-parity with the
// Rust binary.
func canonicalIntegralText(raw json.RawMessage) (string, bool) {
	t := bytes.TrimSpace(raw)
	if !integralText(t) {
		return "", false
	}
	if bytes.Equal(t, []byte("-0")) {
		t = []byte("0")
	}
	return string(t), true
}

// numberCancelKey maps the `cancel.request_id` number to its
// canonical key, or returns ok==false for a non-integral value.
func numberCancelKey(raw json.RawMessage) (string, bool) {
	text, ok := canonicalIntegralText(raw)
	if !ok {
		return "", false
	}
	return "n:" + text, true
}

// isIntegralNumberRaw reports whether the raw JSON value is an
// integral number literal (a JSON number with no '.', 'e', or 'E').
func isIntegralNumberRaw(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return false
	}
	c := t[0]
	if c != '-' && (c < '0' || c > '9') {
		return false
	}
	return integralText(t)
}

// validID accepts a JSON string or an integral JSON number.
func validID(raw json.RawMessage) (*RequestId, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return nil, false
	}
	if t[0] == '"' {
		var s string
		if json.Unmarshal(t, &s) != nil {
			return nil, false
		}
		id := RequestIdFromString(s)
		return &id, true
	}
	if t[0] == '-' || (t[0] >= '0' && t[0] <= '9') {
		text, ok := canonicalIntegralText(t)
		if !ok {
			return nil, false
		}
		id := RequestIdFromNumber(text)
		return &id, true
	}
	return nil, false
}

func decodeEnvelope(item json.RawMessage, batchIndex *int) (*Request, *SchemaError) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(item, &obj); err != nil {
		return nil, InvalidRequest("request must be an object")
	}
	for key := range obj {
		switch key {
		case "jsonrpc", "id", "method", "params":
		default:
			return nil, InvalidRequest(fmt.Sprintf("unknown request member %q", key))
		}
	}
	vr, ok := obj["jsonrpc"]
	if !ok || !isStringRaw(vr) {
		return nil, InvalidRequest(`jsonrpc must be "2.0"`)
	}
	var version string
	if json.Unmarshal(vr, &version) != nil || version != "2.0" {
		return nil, InvalidRequest(`jsonrpc must be "2.0"`)
	}
	mr, ok := obj["method"]
	if !ok || !isStringRaw(mr) {
		return nil, InvalidRequest("method must be a string starting with iprange.v1.")
	}
	var method string
	if err := json.Unmarshal(mr, &method); err != nil {
		return nil, InvalidRequest("method must be a string starting with iprange.v1.")
	}
	if len(method) < len(MethodPrefix) || method[:len(MethodPrefix)] != MethodPrefix {
		return nil, &SchemaError{Code: StdMethodNotFound, Message: "method must start with iprange.v1."}
	}
	var id *RequestId
	if ir, ok := obj["id"]; ok {
		if isRawNull(ir) {
			return nil, InvalidRequest("id must be a string or integral number")
		}
		valid, accepted := validID(ir)
		if !accepted {
			return nil, InvalidRequest("id must be a string or integral number")
		}
		id = valid
	}
	params, ok := obj["params"]
	if !ok || !isObjectRaw(params) {
		return nil, &SchemaError{Code: StdInvalidParams, Message: "params must be an object"}
	}
	if id == nil && method != CancelMethod {
		return nil, InvalidRequest("notifications are not accepted except iprange.v1.cancel")
	}
	return &Request{ID: id, Method: method, Params: params, BatchIndex: batchIndex}, nil
}

// DecodeFrame decodes one physical line into a single request or an
// ordered batch. The frame must already have its LF/CRLF terminator
// stripped by the LineReader.
func DecodeFrame(line []byte) ([]*Request, *SchemaError) {
	if len(line) > InputFrameLimit {
		return nil, &SchemaError{Code: TransportFrameTooLarge, Message: "frame over input limit"}
	}
	if bytes.IndexByte(line, '\n') >= 0 || bytes.IndexByte(line, '\r') >= 0 {
		return nil, ParseError("unescaped line break inside frame")
	}
	if !utf8.Valid(line) {
		return nil, ParseError("frame is not valid UTF-8")
	}
	t := bytes.TrimSpace(line)
	if len(t) == 0 {
		return nil, ParseError("parse error: empty frame")
	}
	value := json.RawMessage(t)
	var probe any
	if err := json.Unmarshal(value, &probe); err != nil {
		return nil, ParseError("parse error: " + err.Error())
	}
	if t[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(value, &arr); err != nil {
			return nil, ParseError("parse error: invalid array")
		}
		if len(arr) == 0 || len(arr) > BatchLimit {
			return nil, InvalidRequest(fmt.Sprintf("batch length %d outside 1..%d", len(arr), BatchLimit))
		}
		requests := make([]*Request, 0, len(arr))
		for i, item := range arr {
			idx := i
			req, serr := decodeEnvelope(item, &idx)
			if serr != nil {
				return nil, serr
			}
			requests = append(requests, req)
		}
		return requests, nil
	}
	if t[0] != '{' {
		return nil, InvalidRequest("request must be an object")
	}
	req, serr := decodeEnvelope(value, nil)
	if serr != nil {
		return nil, serr
	}
	return []*Request{req}, nil
}

// encodeResponseObject encodes one response object with the
// 65,000-byte ceiling.
func encodeResponseObject(payload any) (string, *SchemaError) {
	text, err := Marshal(payload)
	if err != nil {
		return "", &SchemaError{Code: TransportFrameTooLarge, Message: "response object encoding failed"}
	}
	if len(text) > ResponseObjectLimit {
		return "", &SchemaError{Code: TransportFrameTooLarge, Message: "response object over 65,000-byte limit"}
	}
	return string(text), nil
}

// encodeResponseFrame encodes one response frame with the
// 1,048,576-byte ceiling.
func encodeResponseFrame(payload any) (string, *SchemaError) {
	text, err := Marshal(payload)
	if err != nil {
		return "", &SchemaError{Code: TransportFrameTooLarge, Message: "response frame encoding failed"}
	}
	if len(text) > OutputFrameLimit {
		return "", &SchemaError{Code: TransportFrameTooLarge, Message: "response frame over 1,048,576-byte limit"}
	}
	return string(text), nil
}

// SuccessResponse builds the standard success envelope.
func SuccessResponse(id *RequestId, result any) json.RawMessage {
	m := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id.AsJSON()),
		"result":  result,
	}
	return mustMarshal(m)
}

// ErrorResponse builds the standard error envelope with optional data.
func ErrorResponse(id *RequestId, code int64, message string, data any) json.RawMessage {
	m := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id.AsJSON()),
		"error":   map[string]any{"code": code, "message": message},
	}
	if data != nil {
		m["error"].(map[string]any)["data"] = data
	}
	return mustMarshal(m)
}

// mustMarshal serializes a response envelope; the payloads here are
// built from bounded constants and validated by the callers.
func mustMarshal(v any) json.RawMessage {
	b, err := Marshal(v)
	if err != nil {
		panic("rpc: marshal of response envelope failed: " + err.Error())
	}
	return b
}

// Canonical error data payloads used by the session transport.
func outputLimitErrorData() map[string]any {
	return map[string]any{"code": "output_limit", "outcome": "read_only_failure"}
}

// EncodeResponseObjectProbe exports the object-ceiling check for the
// handler preflight helpers (envelope probes sized before any work).
func EncodeResponseObjectProbe(payload any) (string, *SchemaError) {
	return encodeResponseObject(payload)
}
