package handlers

import (
	"encoding/binary"
	"runtime"
	"testing"
)

// decodeArtifactBasename must keep encoding-1 bytes as the text's
// UTF-8 bytes (the Go SDK's single-byte store) and must recover the
// exact stored units for the encoding-2 per-byte wire form (the Rust
// Windows store), never collapse them through UTF-8 lossy.
func TestDecodeArtifactBasename(t *testing.T) {
	utf8Text := "größe.iprange"
	bytes, err := decodeArtifactBasename(utf8Text, 1, "source_basename")
	if err != nil {
		t.Fatalf("encoding 1 decode: %v", err)
	}
	if string(bytes) != utf8Text {
		t.Fatalf("encoding 1 round trip: got %q want %q", bytes, utf8Text)
	}
	// "é" in UTF-16LE is E9 00; the per-byte wire form renders the
	// units as U+00E9 U+0000 and decoding must recover the exact
	// units (a UTF-8 path would produce EF BF BD 00).
	units := "\u00e9\u0000"
	bytes, err = decodeArtifactBasename(units, 2, "source_basename")
	if err != nil {
		t.Fatalf("encoding 2 decode: %v", err)
	}
	if len(bytes) != 2 || bytes[0] != 0xe9 || bytes[1] != 0x00 {
		t.Fatalf("encoding 2 round trip: got % x want e9 00", bytes)
	}
	if _, err := decodeArtifactBasename("caf\u0100", 2, "source_basename"); err == nil {
		t.Fatal("encoding 2 decode accepted a character above U+00FF")
	}
}

// decodeFileIdentity rebuilds the platform's local identity kind: the
// Windows namespace records kind 2; every other platform records
// kind 1 (the match pattern of maintenance.go decodeIdentityFromObject).
func TestDecodeFileIdentityKind(t *testing.T) {
	object := rawObject{
		"volume": []byte(`"123"`),
		"file":   []byte(`"456"`),
	}
	identity, err := decodeFileIdentity(object, "directory_identity")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := uint16(1)
	if runtime.GOOS == "windows" {
		want = 2
	}
	if identity.Kind != want {
		t.Fatalf("identity kind = %d, want %d", identity.Kind, want)
	}
	if got := binary.LittleEndian.Uint64(identity.Bytes[0:8]); got != 123 {
		t.Fatalf("volume = %d, want 123", got)
	}
	if got := binary.LittleEndian.Uint64(identity.Bytes[8:16]); got != 456 {
		t.Fatalf("file = %d, want 456", got)
	}
}
