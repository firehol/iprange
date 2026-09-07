package handlers

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"runtime"
	"strconv"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
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

// artifactBasename must render encoding-1 bytes with the same lossy
// UTF-8 decode as the Rust renderer (one U+FFFD per maximal invalid
// run, from_utf8_lossy semantics): before the wave-15 round-3 repair
// Go's json marshal replaced each invalid byte separately and the
// products' wire text diverged for incomplete multibyte sequences.
func TestArtifactBasenameEncodingOneInvalidUtf8MatchesRust(t *testing.T) {
	// One incomplete two-byte run decodes to exactly one replacement
	// character on the Rust side; a per-byte replacement would emit
	// two.
	rendered := artifactBasename([]byte{0xe2, 0x82}, 1)
	if rendered != "\ufffd" {
		t.Fatalf("artifactBasename(E2 82, 1) = %q, want one U+FFFD", rendered)
	}
	wire, err := json.Marshal(rendered)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(wire, []byte("\xef\xbf\xbd")) {
		t.Fatalf("wire %q has no U+FFFD", wire)
	}
	// Valid UTF-8 passes through unchanged under encoding 1.
	if got := artifactBasename([]byte("caf\xc3\xa9.iprange"), 1); got != "caf\u00e9.iprange" {
		t.Fatalf("encoding 1 valid text = %q", got)
	}
}

// privateOutputAttemptValue must render the attempt basename with
// the encoding-aware wire form at every failure surface (snapshot,
// publish, recovery, algebra): encoding 2 unit bytes survive JSON
// without U+FFFD collapse and decode back to the exact stored bytes,
// and encoding 1 keeps the raw text (wave-15 round-3 finding: the Go
// renderer emitted string(bytes) raw).
func TestPrivateOutputAttemptValueBasenameWire(t *testing.T) {
	attempt := &iprangedb.PrivateOutputAttempt{
		PublicationAttemptID: [16]byte{7},
		BasenameEncoding:     2,
		Basename:             []byte{0xe9, 0x00},
	}
	value := privateOutputAttemptValue(attempt)
	text, ok := value["basename"].(string)
	if !ok {
		t.Fatalf("basename is not a string: %#v", value["basename"])
	}
	decoded, err := decodeArtifactBasename(text, 2, "basename")
	if err != nil {
		t.Fatalf("encoding 2 decode of %q: %v", text, err)
	}
	if len(decoded) != 2 || decoded[0] != 0xe9 || decoded[1] != 0x00 {
		t.Fatalf("encoding 2 round trip = % x, want e9 00", decoded)
	}
	wire, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(wire, []byte("\xef\xbf\xbd")) {
		t.Fatalf("wire %q contains U+FFFD, the pre-repair corruption", wire)
	}
	// The wire carries the raw UTF-8 of U+00E9 followed by the
	// escaped NUL unit of the per-byte form, never U+FFFD.
	if !bytes.Contains(wire, []byte("\xc3\xa9\\u0000")) {
		t.Fatalf("wire %q lacks the rendering of the E9 00 units", wire)
	}
	// Encoding 1 renders the raw UTF-8 text.
	attemptPosix := &iprangedb.PrivateOutputAttempt{
		PublicationAttemptID: [16]byte{8},
		BasenameEncoding:     1,
		Basename:             []byte("live.iprange"),
	}
	if got := privateOutputAttemptValue(attemptPosix)["basename"]; got != "live.iprange" {
		t.Fatalf("encoding 1 basename = %#v", got)
	}
}

// The main_basename wire render and the resolve comparison must
// round-trip every path, including POSIX names whose bytes are not
// valid UTF-8: the create/transition result renders the lossy wire
// text (Rust local_basename_text / from_utf8_lossy parity via
// utf8Lossy) and decodeMainBasename compares against the same
// rendered text (wave-15 round-4 finding: the Go compare used the
// raw bytes, so Go rejected its own result for invalid-UTF-8 names
// and emitted per-byte replacement text).
func TestMainBasenameRoundTripInvalidUtf8(t *testing.T) {
	path := "/tmp/x\xe2\x82"
	basename, err := iprangedb.BasenameFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	wire := LocalBasenameBytes(basename)
	// POSIX stores the two raw bytes and renders the Rust
	// maximal-subpart text (one U+FFFD); Windows stores the UTF-16LE
	// units of the name, and Go's string view already replaced each
	// invalid byte with U+FFFD before the constructor ran, so the
	// units decode to two replacement characters.  Both products
	// round-trip their own rendered text on both platforms.
	want := "x\ufffd"
	if runtime.GOOS == "windows" {
		want = "x\ufffd\ufffd"
	}
	if wire != want {
		t.Fatalf("wire = %q, want %q", wire, want)
	}
	object := rawObject{"main_basename": json.RawMessage(strconv.Quote(wire))}
	decoded, err := decodeMainBasename(object, path)
	if err != nil {
		t.Fatalf("decodeMainBasename rejected the product's own result: %v", err)
	}
	if decoded.Encoding() != basename.Encoding() || string(decoded.Bytes()) != string(basename.Bytes()) {
		t.Fatalf("decoded basename = % x/%d, want % x/%d",
			decoded.Bytes(), decoded.Encoding(), basename.Bytes(), basename.Encoding())
	}

	// Valid non-ASCII and ASCII names keep the raw text and round-trip.
	for _, name := range []string{"größe.iprange", "live.iprange"} {
		p := "/tmp/" + name
		b, err := iprangedb.BasenameFromPath(p)
		if err != nil {
			t.Fatal(err)
		}
		w := LocalBasenameBytes(b)
		if _, err := decodeMainBasename(rawObject{"main_basename": json.RawMessage(strconv.Quote(w))}, p); err != nil {
			t.Fatalf("%q round trip: %v", name, err)
		}
	}

	// A mismatching wire is still rejected with the exact error.
	if _, err := decodeMainBasename(rawObject{"main_basename": json.RawMessage(`"other.iprange"`)}, "/tmp/live.iprange"); err == nil {
		t.Fatal("mismatching main_basename was accepted")
	}

	// The Rust component-validation contract: ".", "..", and roots
	// have no file name, so the path cannot resolve at all.
	for _, bad := range []string{".", "..", "/"} {
		if _, err := decodeMainBasename(rawObject{"main_basename": json.RawMessage(`"x"`)}, bad); err == nil {
			t.Fatalf("decodeMainBasename accepted the component path %q", bad)
		}
	}
}

// utf16leText must decode UTF-16LE units exactly like Rust
// String::from_utf16_lossy, including surrogate pairs.
func TestUtf16leTextDecode(t *testing.T) {
	got := utf16leText([]byte{0xe9, 0x00})
	if got != "\u00e9" {
		t.Fatalf("utf16leText(E9 00) = %q", got)
	}
	got = utf16leText([]byte{0x3d, 0xd8, 0x00, 0xde}) // U+1F600 surrogate pair
	if got != "\U0001F600" {
		t.Fatalf("utf16leText(pair) = %q", got)
	}
	got = utf16leText([]byte{0x00, 0xd8}) // lone high surrogate
	if got != "\ufffd" {
		t.Fatalf("utf16leText(lone) = %q", got)
	}
}
