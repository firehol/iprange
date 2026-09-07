//go:build !windows

package live

import "testing"

// LocalBasenameFromPath must carry the POSIX raw bytes under
// encoding 1, including bytes that are not valid UTF-8 (Rust
// LocalBasename::from_path on unix).
func TestLocalBasenameFromPathPosixBytes(t *testing.T) {
	raw := "x\xe2\x82"
	basename, err := LocalBasenameFromPath("/tmp/" + raw)
	if err != nil {
		t.Fatal(err)
	}
	if basename.encodingValue() != 1 {
		t.Fatalf("encoding = %d, want 1", basename.encodingValue())
	}
	if string(basename.bytesValue()) != raw {
		t.Fatalf("bytes = % x, want % x", basename.bytesValue(), []byte(raw))
	}
}

// LocalBasenameFromPath must reject the component shapes Rust
// Path::file_name reports as missing: ".", "..", separators, empty
// names, and any path whose final component is ".." (Rust
// Components keeps ParentDir, and file_name maps it to None).
func TestLocalBasenameFromPathRejectsDotComponents(t *testing.T) {
	for _, path := range []string{".", "..", "/", "//", ""} {
		if _, err := LocalBasenameFromPath(path); err == nil {
			t.Fatalf("LocalBasenameFromPath(%q) succeeded, want the Rust InvalidArgument error", path)
		}
	}
	// A parent-relative name with a real final component is still valid.
	raw := "x\xe2\x82"
	if _, err := LocalBasenameFromPath("/tmp/../" + raw); err != nil {
		t.Fatalf("parent-relative path rejected: %v", err)
	}
}

// Rust Path::file_name normalizes trailing separators and "."
// components before taking the final component; the Go constructor
// must accept the same shapes ("foo.txt/." yields "foo.txt" on
// both platforms, verified against Rust on linux).
func TestLocalBasenameFromPathNormalizesDotSuffix(t *testing.T) {
	for _, path := range []string{
		"foo.txt", "foo.txt/", "foo.txt/.", "foo.txt//.",
		"a/../b", "a/b/./c.txt",
	} {
		basename, err := LocalBasenameFromPath(path)
		if err != nil {
			t.Fatalf("LocalBasenameFromPath(%q) rejected: %v", path, err)
		}
		if got := string(basename.bytesValue()); got != "c.txt" && got != "b" && got != "foo.txt" {
			t.Fatalf("LocalBasenameFromPath(%q) = %q, want the normalized final component", path, got)
		}
	}
	// A trailing parent component still has no file name (Rust
	// Path::file_name("foo/..") is None).
	for _, path := range []string{"foo/..", "foo.txt/../", "../../x/.."} {
		if _, err := LocalBasenameFromPath(path); err == nil {
			t.Fatalf("LocalBasenameFromPath(%q) succeeded, want the Rust InvalidArgument error", path)
		}
	}
}

// Rust Path::file_name is None for every path that terminates in
// "..", even when earlier components survive (Rust never resolves a
// trailing ParentDir against its parent, unlike a lexical cleaner).
// The cleaned ancestor directory must not be returned as the
// basename (external review round 5 finding: filepath.Clean resolved
// "a/b/.." to "a" and the constructor accepted it).
func TestLocalBasenameFromPathRejectsTrailingParent(t *testing.T) {
	for _, path := range []string{
		"a/b/..", "x/y/z/..", "x/y/../z/..", "a/./b/..",
		"a/../b/c/..", "a/b/.../..", "a//b//..",
	} {
		if _, err := LocalBasenameFromPath(path); err == nil {
			t.Fatalf("LocalBasenameFromPath(%q) succeeded, want the Rust InvalidArgument error for a trailing ..", path)
		}
	}
}

// A ".." component in the middle of the path is an ordinary
// component for Rust Path::file_name: it is never resolved, so the
// final component after it is still the basename (Rust
// file_name("a/../b") is Some("b"), matching the CWD-relative path
// "b").
func TestLocalBasenameFromPathMidParentKept(t *testing.T) {
	for path, want := range map[string]string{
		"a/../b":        "b",
		"a/../../b":     "b",
		"a/b/../c.txt":  "c.txt",
		"a/../b/../c":   "c",
		"a/../../b/./c": "c",
		"a/../b/../":    "", // rejected below via err != nil
	} {
		basename, err := LocalBasenameFromPath(path)
		if want == "" {
			if err == nil {
				t.Fatalf("LocalBasenameFromPath(%q) succeeded, want error", path)
			}
			continue
		}
		if err != nil {
			t.Fatalf("LocalBasenameFromPath(%q) rejected: %v", path, err)
		}
		if got := string(basename.bytesValue()); got != want {
			t.Fatalf("LocalBasenameFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}
