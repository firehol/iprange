package rpc

import "testing"

// TestMarshalRustEscaping pins the serde_json-compatible escaping of
// the wire encoder: quotes, backslashes, and control characters
// escape, while '<', '>', '&', U+2028, and U+2029 pass through as raw
// UTF-8, byte for byte as the Rust product binary emits them.
func TestMarshalRustEscaping(t *testing.T) {
	cases := []struct{ input, want string }{
		{`<&>&`, `"<&>&"`},
		{"a\u2028b", "\"a\xe2\x80\xa8b\""},
		{"a\u2029b", "\"a\xe2\x80\xa9b\""},
		{`q"b\c`, `"q\"b\\c"`},
		{"a\tb\rc\nd", "\"a\\tb\\rc\\nd\""},
		{"ctl\u001f", "\"ctl\\u001f\""},
		{`plain`, `"plain"`},
	}
	for _, tc := range cases {
		got, err := Marshal(tc.input)
		if err != nil {
			t.Fatalf("%q: %v", tc.input, err)
		}
		if string(got) != tc.want {
			t.Fatalf("Marshal(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestMarshalEnvelopeParity pins a complete envelope with special
// characters in the echoed id and the message text.
func TestMarshalEnvelopeParity(t *testing.T) {
	id := RequestIdFromString("<&>&")
	envelope, err := Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id.AsJSON(),
		"error":   map[string]any{"code": int64(-32601), "message": "unknown iprange.v1.zzz<&>&"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"error":{"code":-32601,"message":"unknown iprange.v1.zzz<&>&"},"id":"<&>&","jsonrpc":"2.0"}`
	if string(envelope) != want {
		t.Fatalf("envelope = %s, want %s", envelope, want)
	}
}
