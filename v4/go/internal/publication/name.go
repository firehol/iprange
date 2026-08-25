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

	"github.com/firehol/iprange/v4/go/internal/format"
)

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

// invalidMainName mirrors Rust path::validate_main_name through the
// platform component rule (name_rule_posix.go / name_rule_windows.go)
// plus the reserved prefix and suffix (Rust reserved matches are
// byte-wise ASCII-case-insensitive).
func invalidMainName(name string) bool {
	if mainNameComponentRule(name) {
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
