//go:build !windows

// Publication destination name rules (Rust path.rs
// validate_main_name + name_binding.rs). The main name must be one
// exact path component: never ".", "..", a NUL or separator byte,
// never the reserved ".iprange-" artifact prefix, and never the
// ".readers" coordination suffix; the reserved matches are byte-wise
// ASCII-case-insensitive exactly like the Rust eq_ignore_ascii_case
// (Unicode folding is not applied).

package publication

import (
	"strings"

	"github.com/firehol/iprange/v4/go/internal/format"
)

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
