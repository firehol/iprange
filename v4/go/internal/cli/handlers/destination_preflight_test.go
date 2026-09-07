package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/live"
)

// TestPreflightFileNameParity pins the Rust file_name semantics in
// both destination preflights: requirePublicationParent and
// requireCreateDestinationParent must reject exactly the shapes Rust
// Path::file_name reports as missing, and accept every shape with a
// real final component — including mid-path ".." (an ordinary
// component) and trailing "." (normalized away).  This is the
// round-5 sibling class: filepath.Clean-based checks resolved a
// trailing ".." onto an ancestor and accepted it, while the raw
// filepath.Base checks rejected trailing-dot shapes Rust accepts
// (external review round 5 finding).
func TestPreflightFileNameParity(t *testing.T) {
	rejects := []string{
		"", ".", "..", "/", "//", "a/b/..", "x/y/z/..", "x/y/../z/..",
		"C:/x/..", "a/../b/..", "foo/..", "foo.txt/../",
	}
	accepts := []string{
		"a.txt", "dir/a.txt", "a/../b.txt", "a/../../b.txt", "a/b/../c.txt",
		"d.txt/", "d.txt/.", "d.txt//.", "a/./b.txt",
	}
	// Both preflights require the parent to exist, so run the
	// file-name half against a parent that exists (t.TempDir).
	parent := t.TempDir()
	// Concatenate instead of filepath.Join: Join cleans the path and
	// would resolve the very ".." components these shapes test.
	withParent := func(name string) string {
		if name == "" {
			return ""
		}
		if filepath.IsAbs(name) {
			return name
		}
		return parent + string(filepath.Separator) + name
	}
	// The file-name check runs before any parent stat, so the bare
	// shapes already exercise the rejection half.
	for _, path := range rejects {
		if err := requirePublicationParent(path); err == nil {
			t.Errorf("requirePublicationParent(%q) accepted, want invalid_path", path)
		}
		if err := requireCreateDestinationParent(path); err == nil {
			t.Errorf("requireCreateDestinationParent(%q) accepted, want invalid_path", path)
		}
	}
	for _, path := range accepts {
		// The preflight may only fail with the parent error here;
		// the file-name check must pass.
		if err := requirePublicationParent(withParent(path)); err != nil {
			if err.Code != "invalid_path" || err.Outcome != "not_started" {
				t.Errorf("requirePublicationParent(%q) failed at the parent step (code=%s outcome=%s), want the valid file name to pass", path, err.Code, err.Outcome)
			}
		}
	}
}

// TestPreflightParentParity pins the Rust parent() semantics used by
// both preflights: the parent of "name/." is "." (Rust normalizes
// trailing "." away), the parent of "a/b/.." is "a/b", and the parent
// of "a/../b" is "a/.." (Rust never resolves "..").
func TestPreflightParentParity(t *testing.T) {
	parent := t.TempDir()
	sub, err := os.MkdirTemp(parent, "sub")
	if err != nil {
		t.Fatal(err)
	}
	// "name/." inside an existing directory: the parent is the
	// directory itself (Rust parent of "sub/x/." is "sub").
	ok := filepath.Join(sub, "publish.iprange", ".")
	if err := requirePublicationParent(ok); err != nil {
		t.Errorf("requirePublicationParent(%q) failed: %v", ok, err)
	}
	if err := requireCreateDestinationParent(filepath.Join(sub, "live.iprange", ".")); err != nil {
		t.Errorf("requireCreateDestinationParent(trailing dot) failed: %v", err)
	}
	// Parent extraction itself must mirror Rust:
	for path, want := range map[string]string{
		"a/b":      "a",
		"foo.txt":  ".",
		"/a":       "/",
		"a/b/..":   "a/b",
		"a/../b":   "a/..",
		"name/.":   ".",
		"a/b/..//": "a/b",
		"//a//b":   "//a",
		"a///b//c": "a///b",
	} {
		if got := live.FileParent(path); got != want {
			t.Errorf("FileParent(%q) = %q, want %q", path, got, want)
		}
	}
}
