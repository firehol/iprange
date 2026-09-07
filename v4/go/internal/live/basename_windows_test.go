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

// Rust Path::file_name normalizes trailing separators and "."
// components before taking the final component; the Go constructor
// must accept the same shapes on Windows and store the UTF-16LE
// units of the normalized name.
func TestLocalBasenameFromPathNormalizesDotSuffixWindows(t *testing.T) {
	for _, path := range []string{
		"C:/Temp/foo.txt", "C:/Temp/foo.txt/", "C:/Temp/foo.txt/.",
		"C:/Temp/foo.txt//.", "C:/Temp/a/../foo.txt",
		`C:\Temp\foo.txt`, `C:\Temp\foo.txt\.`,
	} {
		basename, err := LocalBasenameFromPath(path)
		if err != nil {
			t.Fatalf("LocalBasenameFromPath(%q) rejected: %v", path, err)
		}
		want := Utf16LEBytes("foo.txt")
		if string(basename.bytesValue()) != string(want) {
			t.Fatalf("bytes(%q) = % x, want % x", path, basename.bytesValue(), want)
		}
	}
}

// The Windows constructor must reject the same missing-component
// shapes as Rust Path::file_name (".", "..", the separator, empty),
// including a bare volume and a volume root (Rust maps the Prefix
// and RootDir components to None).
func TestLocalBasenameFromPathRejectsDotComponentsWindows(t *testing.T) {
	for _, path := range []string{".", "..", "C:/", "C:\\", "C:", ""} {
		if _, err := LocalBasenameFromPath(path); err == nil {
			t.Fatalf("LocalBasenameFromPath(%q) succeeded, want the Rust InvalidArgument error", path)
		}
	}
}

// Rust Path::file_name is None for every path that terminates in
// ".." on Windows too ("C:/x/.." terminates in ParentDir; the
// volume root is not a name).  The cleaned ancestor directory must
// not be returned as the basename (external review round 5 finding:
// filepath.Clean resolved "C:/x/.." to "C:" and the constructor
// accepted the volume).
func TestLocalBasenameFromPathRejectsTrailingParentWindows(t *testing.T) {
	for _, path := range []string{
		"C:/x/..", "C:/Temp/a/b/..", "C:/a/../b/c/..",
		`C:\x\..`, `C:\Temp\..\b\..`, "C:/x/./..",
	} {
		if _, err := LocalBasenameFromPath(path); err == nil {
			t.Fatalf("LocalBasenameFromPath(%q) succeeded, want the Rust InvalidArgument error for a trailing ..", path)
		}
	}
}

// Mid-path ".." components are ordinary components for Rust
// Path::file_name on Windows: never resolved, so the final component
// after them is still the basename.
func TestLocalBasenameFromPathMidParentKeptWindows(t *testing.T) {
	for path, want := range map[string]string{
		"C:/a/../b":       "b",
		"C:/a/../../b":    "b",
		`C:\a\..\b`:       "b",
		"C:/a/b/../c.txt": "c.txt",
		"C:/a/../b/../c":  "c",
	} {
		basename, err := LocalBasenameFromPath(path)
		if err != nil {
			t.Fatalf("LocalBasenameFromPath(%q) rejected: %v", path, err)
		}
		got := string(basename.bytesValue())
		wb := Utf16LEBytes(want)
		if got != string(wb) {
			t.Fatalf("LocalBasenameFromPath(%q) = % x, want % x", path, got, wb)
		}
	}
}
