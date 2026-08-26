//go:build windows

package live

import (
	"unicode/utf8"

	"github.com/firehol/iprange/v4/go/internal/format"
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
// compared ASCII-case-insensitively like Rust eq_ignore_ascii_case,
// is CON/PRN/AUX/NUL or COM1..9/LPT1..9.
func isWindowsDeviceName(name string) bool {
	stem := name
	for i := 0; i < len(stem); i++ {
		if stem[i] == '.' {
			stem = stem[:i]
			break
		}
	}
	switch len(stem) {
	case 3:
		return format.AsciiFoldEqual(stem, "CON") || format.AsciiFoldEqual(stem, "PRN") ||
			format.AsciiFoldEqual(stem, "AUX") || format.AsciiFoldEqual(stem, "NUL")
	case 4:
		head, digit := stem[:3], stem[3]
		return (format.AsciiFoldEqual(head, "COM") || format.AsciiFoldEqual(head, "LPT")) &&
			digit >= '1' && digit <= '9'
	}
	return false
}
