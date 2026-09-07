package handlers

import "testing"

// utf8Lossy must emit exactly the wire text of Rust
// String::from_utf8_lossy (the reference implementation, maximal-
// subpart rule) for every byte class: valid text passes through
// unchanged, incomplete tails replace the maximal valid prefix with
// one U+FFFD, and structurally complete but invalid sequences
// (overlong, surrogate, above U+10FFFF) replace only their lead
// byte and re-scan the continuation bytes.  The expectations below
// were measured from the Rust implementation.
func TestUtf8LossyMatchesRustFromUtf8Lossy(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
		want  string
	}{
		{"valid text", []byte("größe.iprange"), "größe.iprange"},
		{"ascii", []byte("live.iprange"), "live.iprange"},
		{"incomplete 3-byte tail", []byte{0xe2, 0x82}, "\ufffd"},
		{"lone 3-byte lead", []byte{0xe9}, "\ufffd"},
		{"broken continuation", []byte{0xc3, 0x28}, "\ufffd("},
		{"valid prefix then break", []byte{0xf0, 0x9f, 0x28}, "\ufffd("},
		{"lead plus one valid continuation", []byte{0xe2, 0x82, 0x28}, "\ufffd("},
		{"overlong 2-byte", []byte{0xc0, 0xaf}, "\ufffd\ufffd"},
		{"overlong 3-byte", []byte{0xe0, 0x80, 0x80}, "\ufffd\ufffd\ufffd"},
		{"overlong 4-byte", []byte{0xf0, 0x80, 0x80, 0x80}, "\ufffd\ufffd\ufffd\ufffd"},
		{"surrogate lead", []byte{0xed, 0xa0, 0x80}, "\ufffd\ufffd\ufffd"},
		{"overlong 3-byte second byte 9f", []byte{0xe0, 0x9f, 0x80}, "\ufffd\ufffd\ufffd"},
		{"out-of-range first continuation f4 bd", []byte{0xf4, 0xbd, 0xc5}, "\ufffd\ufffd\ufffd"},
		{"low surrogate lead", []byte{0xed, 0xb0, 0x80}, "\ufffd\ufffd\ufffd"},
		{"out of range", []byte{0xf4, 0x90, 0x80, 0x80}, "\ufffd\ufffd\ufffd\ufffd"},
		{"lone continuation then overlong", []byte{0x80, 0xc0, 0xaf}, "\ufffd\ufffd\ufffd"},
		{"overlong 2-byte lead c1", []byte{0xc1, 0xbf}, "\ufffd\ufffd"},
		{"lead above plane 16", []byte{0xf5}, "\ufffd"},
		{"valid boundary d7c0", []byte{0xed, 0x9f, 0x80}, "\uD7C0"},
		{"valid boundary 10ffff", []byte{0xf4, 0x8f, 0xbf, 0xbf}, "\U0010FFFF"},
		{"valid boundary 0800", []byte{0xe0, 0xa0, 0x80}, "\u0800"},
		{"invalid continuation after 4-byte lead", []byte{0xf1, 0x28}, "\ufffd("},
	}
	for _, c := range cases {
		if got := utf8Lossy(c.bytes); got != c.want {
			t.Errorf("utf8Lossy(% x) = %q, want %q (%s)",
				c.bytes, got, c.want, c.name)
		}
	}
}
