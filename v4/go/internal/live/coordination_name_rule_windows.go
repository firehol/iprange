//go:build windows

package live

import (
	"strings"
	"unicode/utf8"
)

// coordinationNameRule is the platform component-shape rule of the
// main-name validator (Rust path.rs validate_windows_name): one exact
// well-formed component with no NUL, slash, backslash, or colon, no
// trailing dot or space, and no reserved device stem (CON, PRN, AUX,
// NUL, COM1-9, LPT1-9).
func coordinationNameRule(name string) string {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) {
		return "database file name is not one path component"
	}
	for _, r := range name {
		if r == 0 || r == '/' || r == '\\' || r == ':' {
			return "database file name is not one path component"
		}
	}
	last := name[len(name)-1]
	if last == '.' || last == ' ' {
		return "database file name is not one path component"
	}
	if isWindowsDeviceName(name) {
		return "database file name uses a reserved Windows spelling"
	}
	return ""
}

// isWindowsDeviceName matches the exact reserved device stems of Rust
// path.rs is_windows_device_name: the stem before the first dot,
// compared ASCII-case-insensitively.
func isWindowsDeviceName(name string) bool {
	stem := name
	for i := 0; i < len(stem); i++ {
		if stem[i] == '.' {
			stem = stem[:i]
			break
		}
	}
	if len(stem) == 3 {
		switch {
		case strings.EqualFold(stem, "CON"), strings.EqualFold(stem, "PRN"),
			strings.EqualFold(stem, "AUX"), strings.EqualFold(stem, "NUL"):
			return true
		}
		return false
	}
	if len(stem) != 4 {
		return false
	}
	head := stem[:3]
	digit := stem[3]
	if !strings.EqualFold(head, "COM") && !strings.EqualFold(head, "LPT") {
		return false
	}
	return digit >= '1' && digit <= '9'
}
