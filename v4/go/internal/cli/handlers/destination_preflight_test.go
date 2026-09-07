package handlers

import (
	"os"
	"path/filepath"
	"strings"
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
	// accept half against a parent that exists and let the raw
	// spelling reach the handlers: filepath.Join would clean "."
	// and ".." out of the very shapes this test must deliver
	// verbatim.
	parent := t.TempDir()
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
		raw := withParent(path)
		// The parent of the raw spelling must exist so the handler
		// reaches the file-name step and must succeed afterwards;
		// a trailing-dot or trailing-".." rejection would otherwise
		// be masked by the parent-error envelope.
		mkRawParent(t, raw)
		if err := requirePublicationParent(raw); err != nil {
			t.Errorf("requirePublicationParent(%q) failed: code=%s outcome=%s, want success for a valid file name under an existing parent", path, err.Code, err.Outcome)
		}
		if err := requireCreateDestinationParent(raw); err != nil {
			t.Errorf("requireCreateDestinationParent(%q) failed: code=%s outcome=%s, want success for a valid file name under an existing parent", path, err.Code, err.Outcome)
		}
	}
}

// mkRawParent creates the raw parent spelling of path so the two
// preflights' parent-existence step succeeds.  It mirrors the kernel
// walk: each ordinary component between the existing root and the
// raw parent is created; "." and ".." components are left to the
// kernel, which resolves them against the components already made.
func mkRawParent(t *testing.T, path string) {
	t.Helper()
	rawParent := live.FileParent(path)
	sep := string(filepath.Separator)
	// Anchor at the absolute volume + separator (Windows "C:\",
	// UNC "\\server\share\", POSIX "/") so every ordinary
	// component is created under the real anchor; filepath.Join
	// seeds would go drive-relative on Windows ("C:Users\..." in
	// the process cwd).
	acc := filepath.VolumeName(rawParent)
	if acc != "" {
		if len(rawParent) == len(acc) || rawParent[len(acc)] != filepath.Separator {
			acc += sep
		}
	} else if strings.HasPrefix(rawParent, sep) {
		acc = sep
	}
	rest := rawParent[len(acc):]
	for _, part := range strings.Split(rest, sep) {
		if part == "" || part == "." || part == ".." {
			continue
		}
		if !strings.HasSuffix(acc, sep) {
			acc += sep
		}
		acc += part
		if err := os.MkdirAll(acc, 0o755); err != nil {
			t.Fatalf("mkRawParent(%q): create %q: %v", rawParent, acc, err)
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
	// directory itself (Rust parent of "sub/x/." is "sub").  Build
	// the spelling raw: filepath.Join cleans the trailing "." away
	// and the handlers would never see the trailing-dot shape that
	// the Rust reference accepts.
	ok := sub + string(filepath.Separator) + "publish.iprange" + string(filepath.Separator) + "."
	if err := requirePublicationParent(ok); err != nil {
		t.Errorf("requirePublicationParent(%q) failed: %v", ok, err)
	}
	if err := requireCreateDestinationParent(sub + string(filepath.Separator) + "live.iprange" + string(filepath.Separator) + "."); err != nil {
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
