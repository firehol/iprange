package pathname

import (
	"os"
	"testing"
)

// TestPushSeparatorRules pins the separator decision of PathBuf::push
// (Rust PathBuf::_push): no separator when the base is empty or
// already ends with a path separator, one main separator otherwise.
// The bare-drive and verbatim cases are Windows shapes and are pinned
// in push_windows_test.go.
func TestPushSeparatorRules(t *testing.T) {
	sep := string(os.PathSeparator)
	cases := []struct {
		base, name, want string
	}{
		{"", "n", "n"},
		{"a", "n", "a" + sep + "n"},
		{"a/", "n", "a/n"},
		{"/", "n", "/n"},
		{".", "n", "." + sep + "n"},
		{"a" + sep, "n", "a" + sep + "n"},
		{"a/b", "n", "a/b" + sep + "n"},
	}
	for _, c := range cases {
		if got := Push(c.base, c.name); got != c.want {
			t.Errorf("Push(%q, %q) = %q, want %q", c.base, c.name, got, c.want)
		}
	}
}
