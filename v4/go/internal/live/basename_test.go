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
// Path::file_name reports as missing: ".", "..", separators, and
// empty names (Rust parity of the public constructor).
func TestLocalBasenameFromPathRejectsDotComponents(t *testing.T) {
	for _, path := range []string{".", "..", "/", "//", ""} {
		if _, err := LocalBasenameFromPath(path); err == nil {
			t.Fatalf("LocalBasenameFromPath(%q) succeeded, want the Rust InvalidArgument error", path)
		}
	}
	// A parent-relative name with a real component is still valid.
	raw := "x\xe2\x82"
	if _, err := LocalBasenameFromPath("/tmp/../" + raw); err != nil {
		t.Fatalf("parent-relative path rejected: %v", err)
	}
}
