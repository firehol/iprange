// Publication destination name rules (Rust path.rs
// validate_main_name + name_binding.rs). The main name must be one
// exact path component: never ".", "..", a NUL or separator byte,
// never the reserved ".iprange-" artifact prefix, and never the
// ".readers" coordination suffix; the reserved matches are byte-wise
// ASCII-case-insensitive exactly like the Rust eq_ignore_ascii_case
// (Unicode folding is not applied).

package publication

import (
	"path/filepath"
	"strings"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// destinationNameMax is the component-length bound of the targeted
// POSIX filesystems (Rust _PC_NAME_MAX: 255 on the supported linux,
// darwin, freebsd, and netbsd filesystems). The Windows stub refuses
// opens, so no Windows bound is needed.
const destinationNameMax = 255

// ValidDestinationName reports whether one destination path satisfies
// the Rust Destination::bind name rules (path::validate_main_name
// plus require_name_lengths over the cleaned main component).
// CreatePublishAttempt and the live snapshot self-replacement probe
// apply it before any filesystem access, exactly like Rust, which
// binds and validates the destination before opening anything.
func ValidDestinationName(destination string) bool {
	clean := filepath.Clean(destination)
	name := filepath.Base(clean)
	if invalidMainName(name) {
		return false
	}
	return len(name) <= destinationNameMax && len(name)+len(format.CoordinationSuffix) <= destinationNameMax
}

// invalidMainName mirrors Rust path::validate_main_name: true when the
// name is not one valid publication main component.
func invalidMainName(name string) bool {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsRune(name, '/') || strings.IndexByte(name, 0) >= 0 {
		return true
	}
	if format.AsciiFoldHasPrefix(name, format.ReservedBasenamePrefix) {
		return true
	}
	if format.AsciiFoldHasSuffix(name, format.CoordinationSuffix) {
		return true
	}
	return false
}
