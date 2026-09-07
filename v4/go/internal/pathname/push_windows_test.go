//go:build windows

package pathname

import "testing"

// TestPushWindows pins Push against native Windows std PathBuf::push:
// a bare drive base gets no separator (drive-relative temporary,
// "C:" + name), a drive-relative base with a component keeps the main
// separator, and a verbatim-prefixed base is rebuilt with the main
// separator (root "\\" canonicalized and "/" inside a captured
// verbatim share/name preserved), exactly like Rust _push.
func TestPushWindows(t *testing.T) {
	cases := []struct{ base, name, want string }{
		{"C:", "n", `C:n`},
		{"C:cwd", "n", `C:cwd\n`},
		{"C:/dir", "n", `C:/dir\n`},
		{"C:/dir/", "n", `C:/dir/n`},
		{"C:\\dir", "n", `C:\dir\n`},
		{`\\?\C:`, "n", `\\?\C:\n`},
		{`\\?\C:/dir`, "n", `\\?\C:\dir\n`},
		{`\\?\C:/dir/`, "n", `\\?\C:\dir\n`},
		{`\\?\UNC\srv\sh`, "n", `\\?\UNC\srv\sh\n`},
		{`\\?\foo`, "n", `\\?\foo\n`},
	}
	for _, c := range cases {
		if got := Push(c.base, c.name); got != c.want {
			t.Errorf("Push(%q, %q) = %q, want %q", c.base, c.name, got, c.want)
		}
	}
}
