package handlers

import (
	"encoding/binary"
	"encoding/json"
	"runtime"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go/internal/publication"
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

// artifactBasename + decodeArtifactBasename must be mutual inverses
// for both encodings on non-ASCII names (Rust lifecycle::basename
// parity): encoding 2 renders every stored unit byte as the
// same-numbered U+00xx character, encoding 1 keeps the bytes as the
// text's UTF-8 encoding.
func TestArtifactBasenameRenderRoundTrip(t *testing.T) {
	// Encoding 1: raw UTF-8 text round trip.
	posix := []byte("gr\u00f6\u00dfe.iprange")
	rendered := artifactBasename(posix, 1)
	if rendered != "gr\u00f6\u00dfe.iprange" {
		t.Fatalf("encoding-1 render = %q", rendered)
	}
	got, err := decodeArtifactBasename(rendered, 1, "source_basename")
	if err != nil {
		t.Fatal("encoding-1 decode:", err)
	}
	if string(got) != string(posix) {
		t.Fatalf("encoding-1 round trip = % x, want % x", got, posix)
	}

	// Encoding 2: per-byte wire form round trip (UTF-16LE units of
	// "caf\u00e9" with no terminator).
	units := []byte{'c', 0, 'a', 0, 'f', 0, 0xe9, 0x00}
	rendered = artifactBasename(units, 2)
	if rendered != "c\u0000a\u0000f\u0000\u00e9\u0000" {
		t.Fatalf("encoding-2 render = %q", rendered)
	}
	got, err = decodeArtifactBasename(rendered, 2, "source_basename")
	if err != nil {
		t.Fatal("encoding-2 decode:", err)
	}
	if string(got) != string(units) {
		t.Fatalf("encoding-2 round trip = % x, want % x", got, units)
	}
}

// The full housekeeping row wire path must preserve encoding-2 unit
// bytes: the row renderer emits the per-byte text, the response JSON
// preserves it, and the strict decoder recovers the exact units (the
// pre-fix Go renderer collapsed bytes >= 0x80 to U+FFFD and the
// decoder then rejected the row).
func TestHousekeepingRowBasenamesRoundTripJSON(t *testing.T) {
	row := &iprangedb.HousekeepingArtifact{
		State:            iprangedb.HousekeepingInert,
		BasenameEncoding: 2,
		EnvelopeBasename: []byte{'e', 0, 'v', 0, 0xe9, 0x00},
		SourceBasename:   []byte{'s', 0, 'r', 0, 0xe9, 0x00},
		InertBasename:    []byte{'i', 0, 'n', 0, 0xe9, 0x00},
	}
	wire, err := json.Marshal(HousekeepingArtifactJSON(row))
	if err != nil {
		t.Fatal("marshal:", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal("unmarshal:", err)
	}
	for _, field := range []string{"envelope_basename", "source_basename", "inert_basename"} {
		text, ok := decoded[field].(string)
		if !ok {
			t.Fatalf("%s is not a string: %#v", field, decoded[field])
		}
		units, err := decodeArtifactBasename(text, 2, field)
		if err != nil {
			t.Fatalf("%s decode of %q: %v", field, text, err)
		}
		want := row.EnvelopeBasename
		switch field {
		case "source_basename":
			want = row.SourceBasename
		case "inert_basename":
			want = row.InertBasename
		}
		if string(units) != string(want) {
			t.Fatalf("%s round trip = % x, want % x", field, units, want)
		}
	}
}
