//go:build !windows

// Retained-directory scan facts (Rust namespace_scan.rs): the scan
// enumerates every entry of the directory in constant memory, skips
// the "." and ".." entries, verifies the directory before and after
// the stream, and passes visitor errors through unchanged.

package live

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryScanEnumeratesEntries(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.iprdb", "b.iprdb", "sub"} {
		if name == "sub" {
			if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d, err := OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var names []string
	if err := d.Scan(func(name []byte) error {
		names = append(names, string(name))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 {
		t.Fatalf("scan returned %d names, want 3: %v", len(names), names)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if name == "." || name == ".." {
			t.Fatalf("scan leaked dot entry %q", name)
		}
		seen[name] = true
	}
	for _, want := range []string{"a.iprdb", "b.iprdb", "sub"} {
		if !seen[want] {
			t.Fatalf("scan missed %q (got %v)", want, names)
		}
	}
}

func TestDirectoryScanVisitorErrorPassesThrough(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sentinel := errors.New("visitor stopped")
	if err := d.Scan(func([]byte) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("scan error = %v, want the visitor error", err)
	}
}

func TestDirectoryScanReportsStreamOpenFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A closed retained descriptor fails the pre-scan verify first
	// (Rust Directory::scan verifies before Stream::open; the metadata
	// failure is the plain Io class).
	d.Close()
	err = d.Scan(func([]byte) error { return nil })
	nerr, ok := AsNamespaceError(err)
	if !ok || nerr.Kind != NamespaceIo {
		t.Fatalf("scan error = %v, want Io", err)
	}
}
