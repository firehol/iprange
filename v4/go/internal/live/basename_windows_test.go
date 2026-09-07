//go:build windows

package live

import "testing"

// LocalBasenameFromPath must carry the UTF-16LE units of the
// file-name component under encoding 2 on Windows (Rust
// LocalBasename::from_path on windows): both products then record
// the same main-basename fact bytes for the same path.
func TestLocalBasenameFromPathWindowsUnits(t *testing.T) {
	cases := []string{"live.iprange", "caf\u00e9", "\u03b4"}
	for _, name := range cases {
		basename, err := LocalBasenameFromPath("C:/Temp/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if basename.encodingValue() != 2 {
			t.Fatalf("encoding(%q) = %d, want 2", name, basename.encodingValue())
		}
		want := Utf16LEBytes(name)
		got := basename.bytesValue()
		if string(got) != string(want) {
			t.Fatalf("bytes(%q) = % x, want % x", name, got, want)
		}
		if len(got)%2 != 0 {
			t.Fatalf("bytes(%q) has an odd length %d", name, len(got))
		}
	}
}
