//go:build !windows

package publication

import "strings"

// destinationNameMax is the component-length bound of the targeted
// POSIX filesystems (Rust _PC_NAME_MAX: 255 on the supported linux,
// darwin, freebsd, and netbsd filesystems).
const destinationNameMax = 255

// mainNameComponentRule reports a component that is not one exact
// unix path component (Rust validate_posix_name): ".", "..", a
// separator, or a NUL byte.
func mainNameComponentRule(name string) bool {
	return name == "" || name == "." || name == ".." ||
		strings.ContainsRune(name, '/') || strings.IndexByte(name, 0) >= 0
}
