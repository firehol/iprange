//go:build !windows

package live

import "strings"

// coordinationNameRule is the platform component-shape rule of the
// main-name validator (Rust path.rs validate_posix_name): one exact
// component with no separator and no NUL byte.
func coordinationNameRule(name string) string {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') || strings.IndexByte(name, 0) >= 0 {
		return "database file name is not one path component"
	}
	return ""
}
