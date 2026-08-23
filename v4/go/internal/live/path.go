// Canonical live-coordination pathname rules (Rust path::canonical_sidecar
// and validate_main_name, spec section 15.1). The sidecar is the exact
// database pathname plus .readers; the main basename must be one valid
// path component that does not itself use the reserved coordination
// suffix or the reserved .iprange- prefix.

package live

import (
	"path/filepath"
	"strings"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// sidecarSuffix is the canonical sidecar name suffix (spec section 15).
const sidecarSuffix = ".readers"

// canonicalSidecarPath is the exact database pathname plus .readers
// (Rust path::canonical_sidecar). The main basename must be one valid
// path component that does not itself use the reserved coordination
// suffix or the reserved .iprange- prefix.
func canonicalSidecarPath(main string) (string, error) {
	name := filepath.Base(main)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "", &format.Error{Code: format.CodeInvalidArgument, Detail: "database path has no file name"}
	}
	if invalidCoordinationName(name) {
		return "", &format.Error{Code: format.CodeInvalidArgument, Detail: "database file name is not one path component"}
	}
	return filepath.Join(filepath.Dir(main), name+sidecarSuffix), nil
}

// invalidCoordinationName mirrors Rust path::validate_main_name: one
// exact path component, never the reserved .iprange- prefix or the
// .readers coordination suffix. The reserved matches are byte-wise
// ASCII-case-insensitive, exactly like the writer destination-name
// validator (publication_staging.go invalidDestinationName).
func invalidCoordinationName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') || strings.IndexByte(name, 0) >= 0 {
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
