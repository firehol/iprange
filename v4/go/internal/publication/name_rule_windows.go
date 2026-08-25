//go:build windows

package publication

import "unicode/utf8"

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
// is_windows_device_name): the stem before the first dot, compared
// ASCII-case-insensitively, is CON/PRN/AUX/NUL or COM1..9/LPT1..9.
func windowsDeviceName(name string) bool {
	stem := name
	for i := 0; i < len(stem); i++ {
		if stem[i] == '.' {
			stem = stem[:i]
			break
		}
	}
	if len(stem) == 3 {
		switch stem {
		case "CON", "PRN", "AUX", "NUL":
			return true
		}
		return false
	}
	if len(stem) != 4 {
		return false
	}
	head, digit := stem[:3], stem[3]
	if head != "COM" && head != "LPT" {
		return false
	}
	return digit >= '1' && digit <= '9'
}
