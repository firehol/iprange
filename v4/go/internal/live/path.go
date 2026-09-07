// Canonical live-coordination pathname rules (Rust path::canonical_sidecar
// and validate_main_name, spec section 15.1). The sidecar is the exact
// database pathname plus .readers; the main basename must be one valid
// path component that does not itself use the reserved coordination
// suffix or the reserved .iprange- prefix.

package live

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/pathname"
)

// sidecarSuffix is the canonical sidecar name suffix (spec section 15).
const sidecarSuffix = ".readers"

// canonicalSidecarPath is the exact database pathname plus .readers
// (Rust path::canonical_sidecar). The main basename must be one valid
// path component that does not itself use the reserved coordination
// suffix or the reserved .iprange- prefix.
func canonicalSidecarPath(main string) (string, error) {
	name, ok := pathname.FileName(main)
	if !ok {
		return "", &format.Error{Code: format.CodeInvalidArgument, Detail: "database path has no file name"}
	}
	if invalidCoordinationName(name) {
		return "", &format.Error{Code: format.CodeInvalidArgument, Detail: invalidCoordinationDetail(name)}
	}
	// The raw parent prefix is preserved (Rust with_file_name keeps
	// mid-path ".." and repeated interiors separators un-resolved).
	return pathname.WithFileName(main, name+sidecarSuffix), nil
}

// invalidCoordinationName mirrors Rust path::validate_main_name: one
// exact path component, never the reserved .iprange- prefix or the
// .readers coordination suffix. The reserved matches are byte-wise
// ASCII-case-insensitive, exactly like the writer destination-name
// validator (publication_staging.go invalidDestinationName).
func invalidCoordinationName(name string) bool {
	return invalidCoordinationDetail(name) != ""
}

// invalidCoordinationDetail reports the exact Rust rejection detail
// (path.rs validate_main_name: the platform name rules in
// coordination_name_rule_posix.go / _windows.go; the reserved prefix
// and suffix messages are common to both).
func invalidCoordinationDetail(name string) string {
	if detail := coordinationNameRule(name); detail != "" {
		return detail
	}
	if format.AsciiFoldHasPrefix(name, format.ReservedBasenamePrefix) {
		return "database file name uses the reserved .iprange- prefix"
	}
	if format.AsciiFoldHasSuffix(name, format.CoordinationSuffix) {
		return "database file name uses the reserved .readers suffix"
	}
	return ""
}

// liveTransitionTemp is the exact private name of one prepared reset
// sidecar: the main basename plus .readers.reset (Rust
// path::live_transition_temp). The main basename must be one valid
// path component that does not itself use the reserved coordination
// suffix or the reserved .iprange- prefix.
func liveTransitionTemp(main string) (string, error) {
	name, ok := pathname.FileName(main)
	if !ok {
		return "", &format.Error{Code: format.CodeInvalidArgument, Detail: "database path has no file name"}
	}
	if invalidCoordinationName(name) {
		return "", &format.Error{Code: format.CodeInvalidArgument, Detail: invalidCoordinationDetail(name)}
	}
	return pathname.WithFileName(main, name+transitionTempSuffix), nil
}

// transitionTempSuffix is the private reset sidecar name suffix (Rust
// path::live_transition_temp).
const transitionTempSuffix = ".readers.reset"
