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
