package live

import "testing"

// Utf16LEBytes must produce the OS name's UTF-16 code units in
// little-endian order with no terminator (Rust Name::bytes on
// windows, Python "utf-16-le"): ASCII names become one NUL-bounded
// byte pair per character and non-ASCII names use the proper UTF-16
// units, not per-UTF-8-byte projections.
func TestUTF16LEBytes(t *testing.T) {
	cases := []struct {
		name string
		want []byte
	}{
		{"live.iprange", []byte{'l', 0, 'i', 0, 'v', 0, 'e', 0, '.', 0, 'i', 0, 'p', 0, 'r', 0, 'a', 0, 'n', 0, 'g', 0, 'e', 0}},
		{"caf" + "\u00e9", []byte{'c', 0, 'a', 0, 'f', 0, 0xe9, 0x00}},
		{"\u03b4", []byte{0xb4, 0x03}},
	}
	for _, c := range cases {
		got := Utf16LEBytes(c.name)
		if string(got) != string(c.want) {
			t.Errorf("Utf16LEBytes(%q) = % x, want % x", c.name, got, c.want)
		}
	}
}
