package mapping

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// An empty publication destination is replaceable, so the mapping owner
// must accept a zero-length extent instead of refusing it as a format
// problem. Rust proves the extent with require_file_extent and then maps
// nothing at all: map_nonempty returns Ok(None) for len 0
// (iprange-livedb mapping.rs:384-388), and the publication destination
// inspection treats the resulting no-bytes view as an ordinary
// non-desired entry (publication/file_inspection.rs:241-256). Before
// this, MapFile answered format_invalid "mapping size is zero" and the
// whole replace_existing publication refused a valid request.
func TestMapFileAcceptsEmptyExtent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.iprange")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	m, err := MapFile(f, 0, false)
	if err != nil {
		t.Fatalf("MapFile(0) = %v, want an empty mapping", err)
	}
	if m.Size() != 0 {
		t.Fatalf("Size() = %d, want 0", m.Size())
	}
	// No byte is addressable: the mapping keeps the unavailable state
	// that every other empty-mapping consumer already handles.
	if _, err := m.View(0, 0); err == nil {
		t.Fatal("View(0,0) on an empty mapping must refuse")
	}
	if _, err := m.Page(0); err == nil {
		t.Fatal("Page(0) on an empty mapping must refuse")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// A read-write empty mapping is accepted on the same terms, so a caller
// that publishes into a zero-length destination can extend it in place.
func TestMapFileAcceptsEmptyExtentReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.iprange")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := MapFile(f, 0, true)
	if err != nil {
		t.Fatalf("MapFile(0, rdwr) = %v, want an empty mapping", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// The extent proof stays in force: accepting the empty case must not
// turn MapFile into a mapper that reaches past the file, which is what
// keeps a mapped reader from taking SIGBUS.
func TestMapFileStillRefusesBeyondExtent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.iprange")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = MapFile(f, uint64(format.PageSize), false)
	if err == nil {
		t.Fatal("MapFile accepted an extent past the file end")
	}
	var ferr *format.Error
	if !errors.As(err, &ferr) || ferr.Code != format.CodeFormatInvalid {
		t.Fatalf("MapFile(beyond extent) = %v, want format_invalid", err)
	}
	if ferr.Detail != "mapping exceeds the file extent" {
		t.Fatalf("detail = %q", ferr.Detail)
	}
}
