// Strict per-method params schema decoding (Rust
// handlers/reader.rs exact_object family parity).
//
// The wire schema is the frozen authority (v4/cli/schema/methods.py):
// members are exact sets, integral JSON numbers are exact decimals,
// and paths follow the common.PATH bounds. Every helper returns an
// error string that the transport turns into -32602 invalid_params.

package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/firehol/iprange/v4/go/internal/format"
)

var (
	errObject   = errors.New("params must be an object")
	errUnsigned = errors.New("not an unsigned integer")
)

// rawObject is one decoded params object; all member access goes
// through the helpers below so every value is re-validated against
// the strict wire types.
type rawObject map[string]json.RawMessage

// decodeObject parses raw JSON as an object. Error strings become
// invalid_params.
func decodeObject(raw json.RawMessage) (rawObject, error) {
	if isRawNull(raw) {
		return nil, errObject
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, errObject
	}
	return rawObject(object), nil
}

// exactObject requires exactly the listed fields.
func exactObject(raw json.RawMessage, fields ...string) (rawObject, error) {
	object, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	for key := range object {
		if !containsString(fields, key) {
			return nil, fmt.Errorf("unknown member %q", key)
		}
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return nil, fmt.Errorf("missing member %q", field)
		}
	}
	return object, nil
}

// exactObjectOpt requires the listed required fields and allows the
// listed optional fields.
func exactObjectOpt(raw json.RawMessage, required, optional []string) (rawObject, error) {
	object, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	for key := range object {
		if !containsString(required, key) && !containsString(optional, key) {
			return nil, fmt.Errorf("unknown member %q", key)
		}
	}
	for _, field := range required {
		if _, ok := object[field]; !ok {
			return nil, fmt.Errorf("missing member %q", field)
		}
	}
	return object, nil
}

func containsString(list []string, value string) bool {
	for _, entry := range list {
		if entry == value {
			return true
		}
	}
	return false
}

// memberObject returns the named member as an object.
func memberObject(object rawObject, name string) (rawObject, error) {
	raw, ok := object[name]
	if !ok {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	decoded, err := decodeObject(raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	return decoded, nil
}

// asString decodes a strict string member.
func asString(object rawObject, name string) (string, error) {
	raw, ok := object[name]
	if !ok {
		return "", fmt.Errorf("%s must be a string", name)
	}
	if isRawNull(raw) {
		return "", fmt.Errorf("%s must be a string", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return value, nil
}

// asOptionalString decodes a string member or returns "" when absent
// (absent is the only absent form; a present null is rejected).
func asOptionalString(object rawObject, name string) (string, error) {
	raw, ok := object[name]
	if !ok {
		return "", nil
	}
	if isRawNull(raw) {
		return "", fmt.Errorf("%s must be a string; null is not valid", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return value, nil
}

// asBool decodes a strict boolean member.
func asBool(object rawObject, name string) (bool, error) {
	raw, ok := object[name]
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	if isRawNull(raw) {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return value, nil
}

// asUint64 decodes a strict non-negative integral number member.
func asUint64(object rawObject, name string) (uint64, error) {
	raw, ok := object[name]
	if !ok {
		return 0, fmt.Errorf("%s must be an unsigned integer", name)
	}
	value, err := decodeUint64(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an unsigned integer", name)
	}
	return value, nil
}

// asUint32 decodes a strict non-negative integral number member into a
// u32 (a value above 2^32-1 is invalid).
func asUint32(object rawObject, name string) (uint32, error) {
	value, err := asUint64(object, name)
	if err != nil {
		return 0, err
	}
	if value > 0xffffffff {
		return 0, fmt.Errorf("%s must be an unsigned integer", name)
	}
	return uint32(value), nil
}

// decodeUint64 parses a strict JSON decimal number (no fraction, no
// exponent, no sign) into a u64.
func decodeUint64(raw json.RawMessage) (uint64, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" || text[0] == '-' || strings.ContainsAny(text, ".eE") {
		return 0, errUnsigned
	}
	var value uint64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, errUnsigned
	}
	return value, nil
}

// asUint128 members are exact decimal strings on the wire (a JSON
// number cannot express 2^64..2^128 exactly in every client); the
// helper returns the raw text for the family-specific decoders.
func asDecimalString(object rawObject, name string) (string, error) {
	raw, ok := object[name]
	if !ok {
		return "", fmt.Errorf("%s must be a decimal string", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a decimal string", name)
	}
	if value == "" || strings.ContainsAny(value, ".eE+-") {
		return "", fmt.Errorf("%s must be a decimal string", name)
	}
	return value, nil
}

// asHexString decodes a lowercase hex string member.
func asHexString(object rawObject, name string) (string, error) {
	raw, ok := object[name]
	if !ok {
		return "", fmt.Errorf("%s must be a hex string", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a hex string", name)
	}
	if value == "" {
		return "", fmt.Errorf("%s must be a hex string", name)
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", fmt.Errorf("%s must be a hex string", name)
		}
	}
	return value, nil
}

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return 0 // unreachable after asHexString
}

// asStringArray decodes a JSON array of strings.
func asStringArray(object rawObject, name string) ([]string, error) {
	raw, ok := object[name]
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", name)
	}
	if isRawNull(raw) {
		return nil, fmt.Errorf("%s must be an array of strings", name)
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("%s must be an array of strings", name)
	}
	return values, nil
}

// asObjectArray decodes a JSON array of objects.
func asObjectArray(object rawObject, name string) ([]rawObject, error) {
	raw, ok := object[name]
	if !ok {
		return nil, fmt.Errorf("%s must be an array of objects", name)
	}
	if isRawNull(raw) {
		return nil, fmt.Errorf("%s must be an array of objects", name)
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(raw, &raws); err != nil {
		return nil, fmt.Errorf("%s must be an array of objects", name)
	}
	result := make([]rawObject, 0, len(raws))
	for _, item := range raws {
		decoded, err := decodeObject(item)
		if err != nil {
			return nil, fmt.Errorf("%s must be an array of objects", name)
		}
		result = append(result, decoded)
	}
	return result, nil
}

// validatePath enforces the frozen path bounds (common.PATH): non
// empty, not "-", no NUL, at most 65,536 code points.
func validatePath(path string) error {
	if path == "" || path == "-" || strings.IndexByte(path, 0) >= 0 || utf8.RuneCountInString(path) > 65_536 {
		return fmt.Errorf("path is empty, '-', over 65536 characters, or contains NUL")
	}
	return nil
}

// validateHandle enforces the 32-lowercase-hex handle shape.
func validateHandle(handle string) error {
	if !validHandle(handle) {
		return fmt.Errorf("handle must be 32 lowercase hexadecimal characters")
	}
	return nil
}

func validHandle(handle string) bool {
	if len(handle) != 32 {
		return false
	}
	for i := 0; i < len(handle); i++ {
		c := handle[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// isRawNull reports whether the raw JSON value is null.
func isRawNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// asOptionalObject decodes an optional object member (absent is the
// only absent form; a present null is rejected).
func asOptionalObject(object rawObject, name string) (rawObject, bool, error) {
	raw, ok := object[name]
	if !ok {
		return nil, false, nil
	}
	if isRawNull(raw) {
		return nil, false, fmt.Errorf("%s must not be null; absent is the only absent form", name)
	}
	decoded, err := decodeObject(raw)
	if err != nil {
		return nil, false, fmt.Errorf("%s must be an object", name)
	}
	return decoded, true, nil
}

// feedNameValid reports whether name matches the exact v4 FeedName
// grammar (Rust FeedNameValidString): 1 through 255 ASCII bytes, the
// first and last byte a-z or 0-9, and the interior additionally
// '_', '-', '.'.
func feedNameValid(name string) bool {
	if len(name) < 1 || len(name) > 255 {
		return false
	}
	edge := func(b byte) bool {
		return b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
	}
	if !edge(name[0]) || !edge(name[len(name)-1]) {
		return false
	}
	for i := 1; i < len(name)-1; i++ {
		c := name[i]
		if !edge(c) && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// canonicalU64String is the wire's canonical unsigned decimal string
// parser (Rust u64_string), delegated to the single internal/format
// authority.
func canonicalU64String(text string) (uint64, error) {
	value, err := format.ParseCanonicalUint64(text)
	if err != nil {
		return 0, fmt.Errorf("must be a canonical unsigned decimal string")
	}
	return value, nil
}

// canonicalU64RawMember parses one raw object member as a canonical
// unsigned decimal string (Rust u64_string; zero allowed).
func canonicalU64RawMember(object rawObject, name string) (uint64, error) {
	return canonicalU64FromRaw(object[name])
}

// canonicalU64FromRaw parses one raw JSON member as a canonical
// unsigned decimal string.
func canonicalU64FromRaw(raw json.RawMessage) (uint64, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, fmt.Errorf("must be a canonical unsigned decimal string")
	}
	return canonicalU64String(text)
}

// asPositiveU64String parses the wire's positive u64 decimal-string
// member (common.POSITIVE_U64): canonical decimal within [1, 2^64-1].
func asPositiveU64String(object rawObject, name string) (uint64, error) {
	text, err := asDecimalString(object, name)
	if err != nil {
		return 0, err
	}
	if text == "0" {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	value, err := canonicalU64String(text)
	if err != nil {
		return 0, fmt.Errorf("%s must be a canonical unsigned decimal string", name)
	}
	return value, nil
}

// asPositiveU32 decodes one positive u32 JSON integer member (Rust
// positive_u32): a strict integral number in [1, 2^32-1].
func asPositiveU32(object rawObject, name string) (uint32, error) {
	value, err := asUint32(object, name)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be a positive u32 integer", name)
	}
	return value, nil
}
