//go:build !windows

// Destination binding and private-name facts (Rust namespace_tests.rs
// destination arms + name_binding.rs KATs): raw posix bytes survive
// binding, the commitment matches the normative byte formula, private
// attempt names are byte-exact, the fail-if-exists availability proof
// refuses either twin, and the main-name rules are the Rust
// validate_main_name rules.

package publication

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/live"
)

func TestDestinationBindUsesRawPosixBytesAndExactAttemptNames(t *testing.T) {
	dir := t.TempDir()
	raw := string([]byte{'f', 0x80})
	d, err := bindDestination(filepath.Join(dir, raw))
	if err != nil {
		t.Fatal(err)
	}
	defer d.dir.Close()
	if d.main != raw {
		t.Fatalf("main = %q, want the raw posix bytes", d.main)
	}
	commitment, err := basenameCommitment(basenameEncodingPosixBytes, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if d.basenameCommitment != commitment {
		t.Fatalf("basename commitment mismatch")
	}
	output, err := d.outputName(sixteen(0xab))
	if err != nil {
		t.Fatal(err)
	}
	if output != ".iprange-publish-abababababababababababababababab.tmp" {
		t.Fatalf("output name = %q", output)
	}
	reservation, err := d.reservationName(sixteen(0xcd))
	if err != nil {
		t.Fatal(err)
	}
	if reservation != ".iprange-reservation-cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd.tmp" {
		t.Fatalf("reservation name = %q", reservation)
	}
	if d.coordination != raw+".readers" {
		t.Fatalf("coordination = %q, want %q", d.coordination, raw+".readers")
	}
}

func TestPosixCommitmentMatchesTheNormativeByteFormula(t *testing.T) {
	got, err := basenameCommitment(basenameEncodingPosixBytes, []byte("main.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	want := [32]byte{
		0x58, 0x1c, 0x42, 0x34, 0xbf, 0xf2, 0x93, 0x4f, 0xab, 0x8a, 0x83, 0x4b, 0x0c, 0x4b,
		0x38, 0x98, 0xac, 0xc6, 0xe6, 0xe0, 0x01, 0x92, 0x7a, 0xe1, 0xc0, 0x9d, 0x09, 0xb6,
		0xf4, 0xa8, 0x3c, 0x20,
	}
	if got != want {
		t.Fatalf("commitment = %x, want the normative vector", got)
	}
	for _, invalid := range [][]byte{{'.'}, []byte("a/b")} {
		if _, err := basenameCommitment(basenameEncodingPosixBytes, invalid); err == nil {
			t.Fatalf("commitment accepted %q", invalid)
		}
	}
}

func TestMainNameRules(t *testing.T) {
	valid := []string{"main.iprdb", "a", ".iprangex", "x.readers-only", "weird name"}
	for _, name := range valid {
		if invalidMainName(name) {
			t.Fatalf("valid name %q rejected", name)
		}
	}
	invalid := []string{"", ".", "..", "/", "a/b", "a\x00b", ".iprange-x", ".IPRANGE-x", "x.readers", "x.READERS"}
	for _, name := range invalid {
		if !invalidMainName(name) {
			t.Fatalf("invalid name %q accepted", name)
		}
	}
}

func TestBindDestinationNameAndParentClasses(t *testing.T) {
	dir := t.TempDir()
	if _, err := bindDestination(dir + "/.."); !isNamespace(err, live.NamespaceInvalidName) {
		t.Fatalf("bind(..) = %v, want InvalidName", err)
	}
	if _, err := bindDestination(filepath.Join(dir, ".iprange-reserved")); !isNamespace(err, live.NamespaceInvalidName) {
		t.Fatalf("bind(reserved prefix) = %v, want InvalidName", err)
	}
	if _, err := bindDestination(filepath.Join(dir, "nope", "db.iprdb")); !isNamespace(err, live.NamespaceMissing) {
		t.Fatalf("bind(missing parent) = %v, want Missing", err)
	}
	file := filepath.Join(dir, "plain")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bindDestination(filepath.Join(file, "db.iprdb")); !isNamespace(err, live.NamespaceIo) {
		t.Fatalf("bind(parent is a file) = %v, want Io", err)
	}
	// A main name over the name_max bound (255 + 8 for the twin)
	// fails the length proof as InvalidName.
	long := string(make([]byte, 255))
	for i := range long {
		long = long[:i] + "a" + long[i+1:]
	}
	if _, err := bindDestination(filepath.Join(dir, long)); !isNamespace(err, live.NamespaceInvalidName) {
		t.Fatalf("bind(overlong twin) = %v, want InvalidName", err)
	}
}

func TestRequireFailIfExistsAvailable(t *testing.T) {
	dir := t.TempDir()
	d, err := bindDestination(filepath.Join(dir, "db.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.dir.Close()
	if err := d.requireFailIfExistsAvailable(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db.iprdb"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.requireFailIfExistsAvailable(); !isNamespace(err, live.NamespaceExists) {
		t.Fatalf("main present = %v, want Exists", err)
	}
	if err := os.Remove(filepath.Join(dir, "db.iprdb")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db.iprdb.readers"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.requireFailIfExistsAvailable(); !isNamespace(err, live.NamespaceExists) {
		t.Fatalf("coordination present = %v, want Exists", err)
	}
}

func TestPrivateAttemptNamesRejectZeroAndDecodeExactly(t *testing.T) {
	dir := t.TempDir()
	d, err := bindDestination(filepath.Join(dir, "db.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.dir.Close()
	if _, err := d.outputName([16]byte{}); !isNamespace(err, live.NamespaceInvalidName) {
		t.Fatalf("zero output attempt = %v, want InvalidName", err)
	}
	if _, err := d.reservationName([16]byte{}); !isNamespace(err, live.NamespaceInvalidName) {
		t.Fatalf("zero reservation attempt = %v, want InvalidName", err)
	}
	attempt := [16]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10}
	for _, prefix := range []string{outputPrefix, reservationPrefix} {
		name, err := privateName(prefix, attempt)
		if err != nil {
			t.Fatal(err)
		}
		decoded, ok := privateAttempt(prefix, []byte(name))
		if !ok || decoded != attempt {
			t.Fatalf("round trip %q = (%x, %v)", name, decoded, ok)
		}
	}
	// Wrong prefix, wrong suffix, uppercase hex, and truncated hex all
	// refuse.
	name, _ := privateName(outputPrefix, attempt)
	if _, ok := privateAttempt(reservationPrefix, []byte(name)); ok {
		t.Fatal("wrong prefix decoded")
	}
	if _, ok := privateAttempt(outputPrefix, []byte(name[:len(name)-1])); ok {
		t.Fatal("truncated name decoded")
	}
	upper := []byte(name)
	for i := len(upper) - len(".tmp") - 1; i >= len(".iprange-publish-"); i-- {
		if upper[i] >= 'a' && upper[i] <= 'f' {
			upper[i] = upper[i] - 'a' + 'A'
		}
	}
	if _, ok := privateAttempt(outputPrefix, upper); ok {
		t.Fatal("uppercase hex decoded")
	}
	zero, _ := privateName(outputPrefix, attempt)
	zero = zero[:len(".iprange-publish-")] + "00000000000000000000000000000000" + zero[len(zero)-len(".tmp"):]
	if _, ok := privateAttempt(outputPrefix, []byte(zero)); ok {
		t.Fatal("zero attempt decoded")
	}
}

func sixteen(b byte) [16]byte {
	var out [16]byte
	for i := range out {
		out[i] = b
	}
	return out
}

func isNamespace(err error, kind live.NamespaceErrorKind) bool {
	var nerr *live.NamespaceError
	return errors.As(err, &nerr) && nerr.Kind == kind
}
