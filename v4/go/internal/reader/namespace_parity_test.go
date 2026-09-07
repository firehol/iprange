//go:build !windows

package reader

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/pathname"
)

// namespaceChecks must accept and reject the same shapes as Rust
// path::canonical_sidecar's file-name gate (open_immutable derives the
// sidecar with Path::file_name on the raw path): trailing "." is
// normalized away and accepted, a trailing ".." component is refused,
// and mid-path ".." is an ordinary component.
func TestNamespaceChecksParity(t *testing.T) {
	rejects := []string{
		"", ".", "..", "/", "//", "a/b/..", "/a/b/..", "x/y/../..",
	}
	accepts := []string{
		"a.iprange", "dir/a.iprange", "a/../b.iprange", "b.iprange/", "b.iprange/.",
		"b.iprange//.", "a/./b.iprange", "/a/b.iprange",
	}
	for _, path := range rejects {
		if err := namespaceChecks(path); err == nil {
			t.Errorf("namespaceChecks(%q) accepted, want rejection", path)
		}
	}
	for _, path := range accepts {
		if err := namespaceChecks(path); err != nil {
			t.Errorf("namespaceChecks(%q) rejected: %v", path, err)
		}
	}
}

// sidecarPath derives parent + main-name + ".readers" on the raw path
// (Rust Path::with_file_name over the unix separator): the
// trailing-dot shape keeps the real name, and mid-path ".." survives
// into the derived sidecar path. The Windows twin records the native
// backslash rendering (namespace_parity_windows_test.go).
func TestSidecarPathParity(t *testing.T) {
	for path, want := range map[string]string{
		"a.iprange":        "a.iprange.readers",
		"dir/a.iprange":    "dir/a.iprange.readers",
		"dir/a.iprange/.":  "dir/a.iprange.readers",
		"a/../b.iprange":   "a/../b.iprange.readers",
		"/a/b.iprange":     "/a/b.iprange.readers",
		"dir/a.iprange/..": "", // no file name; the caller must refuse first
	} {
		if want == "" {
			if _, ok := pathname.FileName(path); ok {
				t.Errorf("FileName(%q) found a name, want none", path)
			}
			continue
		}
		if got := sidecarPath(path); got != want {
			t.Errorf("sidecarPath(%q) = %q, want %q", path, got, want)
		}
	}
}
