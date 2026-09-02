package rpc

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Marshal serializes v with the exact serde_json escaping rules of the
// Rust product binary: strings escape only the quote, backslash, and
// control characters below 0x20 (\b \t \n \f \r and \u00XX); every
// other rune, including '<', '>', '&', U+2028, and U+2029, is emitted
// as raw UTF-8. Object keys are sorted by byte order, the same order
// as the Rust BTreeMap serializer. This is the single byte authority
// for response envelopes, echoed request ids, and generated JSONL
// rows.
func Marshal(v any) ([]byte, error) {
	var builder strings.Builder
	if err := writeValue(&builder, v); err != nil {
		return nil, err
	}
	return []byte(builder.String()), nil
}

// MarshalJSONL is the handler-facing row encoder for generated JSONL
// outputs (export, validation/recovery findings, cursor rows).
func MarshalJSONL(v any) ([]byte, error) {
	return Marshal(v)
}

func writeValue(builder *strings.Builder, v any) error {
	switch value := v.(type) {
	case nil:
		builder.WriteString("null")
	case string:
		writeString(builder, value)
	case bool:
		if value {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case json.RawMessage:
		builder.Write(value)
	case int:
		builder.WriteString(strconv.FormatInt(int64(value), 10))
	case int64:
		builder.WriteString(strconv.FormatInt(value, 10))
	case uint64:
		builder.WriteString(strconv.FormatUint(value, 10))
	case float64:
		builder.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	case []any:
		builder.WriteByte('[')
		for index, item := range value {
			if index > 0 {
				builder.WriteByte(',')
			}
			if err := writeValue(builder, item); err != nil {
				return err
			}
		}
		builder.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				builder.WriteByte(',')
			}
			writeString(builder, key)
			builder.WriteByte(':')
			if err := writeValue(builder, value[key]); err != nil {
				return err
			}
		}
		builder.WriteByte('}')
	default:
		// Fall back for exotic payloads; the wire surfaces only emit
		// the types above, so this path is a safety net, not parity
		// for bytes.
		text, err := json.Marshal(v)
		if err != nil {
			return err
		}
		builder.Write(text)
	}
	return nil
}

// writeString emits one JSON string with the serde_json escape set.
func writeString(builder *strings.Builder, text string) {
	builder.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\b':
			builder.WriteString(`\b`)
		case '\t':
			builder.WriteString(`\t`)
		case '\n':
			builder.WriteString(`\n`)
		case '\f':
			builder.WriteString(`\f`)
		case '\r':
			builder.WriteString(`\r`)
		default:
			if r < 0x20 {
				const hex = "0123456789abcdef"
				builder.WriteString(`\u00`)
				builder.WriteByte(hex[r>>4])
				builder.WriteByte(hex[r&0xf])
			} else {
				builder.WriteRune(r)
			}
		}
	}
	builder.WriteByte('"')
}
