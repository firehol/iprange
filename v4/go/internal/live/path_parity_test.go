//go:build !windows

package live

import (
	"testing"
)

// canonicalSidecarPath and liveTransitionTemp must derive the sidecar
// name with Rust path::canonical_sidecar / live_transition_temp
// semantics on the raw path: Path::file_name decides the accepted
// base, trailing "." is normalized away (accepted), a trailing ".."
// component has no file name (rejected), and mid-path ".." is an
// ordinary component that survives into the derived path.
func TestCanonicalSidecarParity(t *testing.T) {
	// Shapes Rust accepts: real final component, with trailing
	// separators, trailing ".", and mid-path ".." kept raw.
	for path, want := range map[string]string{
		"a":        "a.readers",
		"a/":       "a.readers",
		"a/.":      "a.readers",
		"a/b":      "a/b.readers",
		"a/b/.":    "a/b.readers",
		"a/../b":   "a/../b.readers",
		"./a":      "./a.readers",
		"/a":       "/a.readers",
		"/a/b":     "/a/b.readers",
		"a///b//c": "a///b/c.readers",
		"a/b/...":  "a/b/....readers",
		"a/b/..c":  "a/b/..c.readers",
		"x/..x/y":  "x/..x/y.readers",
	} {
		sidecar, err := canonicalSidecarPath(path)
		if err != nil {
			t.Errorf("canonicalSidecarPath(%q) rejected: %v (Rust accepts)", path, err)
			continue
		}
		if sidecar != want {
			t.Errorf("canonicalSidecarPath(%q) = %q, want %q", path, sidecar, want)
		}
		temp, err := liveTransitionTemp(path)
		if err != nil {
			t.Errorf("liveTransitionTemp(%q) rejected: %v (Rust accepts)", path, err)
			continue
		}
		if wantTemp := want[:len(want)-len(sidecarSuffix)] + transitionTempSuffix; temp != wantTemp {
			t.Errorf("liveTransitionTemp(%q) = %q, want %q", path, temp, wantTemp)
		}
	}
	// Shapes Rust rejects: empty, ".", "..", the root, and any
	// ".."-terminated path.
	for _, path := range []string{"", ".", "..", "/", "//", "a/b/..", "a/b/../", "/a/..", "x/y/../.."} {
		if _, err := canonicalSidecarPath(path); err == nil {
			t.Errorf("canonicalSidecarPath(%q) accepted, want InvalidArgument", path)
		}
		if _, err := liveTransitionTemp(path); err == nil {
			t.Errorf("liveTransitionTemp(%q) accepted, want InvalidArgument", path)
		}
	}
}

// The reserved-name rejection uses the derived file name, so a
// reserved final component is refused while the same spelling inside
// a parent keeps the path valid for the sidecar derivation.
func TestCanonicalSidecarReservedName(t *testing.T) {
	for _, path := range []string{".iprange-x", "a/.readers", "dir/x.readers"} {
		if _, err := canonicalSidecarPath(path); err == nil {
			t.Errorf("canonicalSidecarPath(%q) accepted, want InvalidArgument", path)
		}
	}
	if path := "dir/plain.iprange"; true {
		if _, err := canonicalSidecarPath(path); err != nil {
			t.Errorf("canonicalSidecarPath(%q) rejected: %v", path, err)
		}
	}
}
