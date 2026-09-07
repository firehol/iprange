//go:build windows

package reader

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/pathname"
)

// sidecarPath must render the sidecar with the native separator on
// the raw parent prefix, exactly like Rust PathBuf::with_file_name on
// Windows (push inserts the backslash separator; the raw parent keeps
// its original forward slashes).
func TestSidecarPathParityWindows(t *testing.T) {
	for path, want := range map[string]string{
		"a.iprange":        "a.iprange.readers",
		"dir/a.iprange":    `dir\a.iprange.readers`,
		"dir/a.iprange/.":  `dir\a.iprange.readers`,
		"a/../b.iprange":   `a/..\b.iprange.readers`,
		"/a/b.iprange":     `/a\b.iprange.readers`,
		`dir\a.iprange`:    `dir\a.iprange.readers`,
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
