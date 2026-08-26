//go:build windows

package publication

import (
	"unicode/utf8"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// destinationNameMax is the component-length bound of NTFS (255 UTF-16
// units; Rust _PC_NAME_MAX on the supported volumes).
const destinationNameMax = 255

// mainNameComponentRule reports a component that is not one exact
// well-formed Windows component (Rust validate_windows_name): ".", "..",
// invalid UTF-8, NUL, slash, backslash, colon, a trailing dot or space,
// or a reserved device stem.
func mainNameComponentRule(name string) bool {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) {
		return true
	}
	for _, r := range name {
		if r == 0 || r == '/' || r == '\\' || r == ':' {
			return true
		}
	}
	if last := name[len(name)-1]; last == '.' || last == ' ' {
		return true
	}
	return windowsDeviceName(name)
}

// windowsDeviceName matches the reserved device stems (Rust
// is_windows_device_name, path.rs): the stem before the first dot,
// compared ASCII-case-insensitively like Rust eq_ignore_ascii_case,
// is CON/PRN/AUX/NUL or COM1..9/LPT1..9.
func windowsDeviceName(name string) bool {
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
