//go:build !windows

package recovery

import "github.com/firehol/iprange/v4/go/internal/format"

// scratchChangedLinkResidueClass is the expected residue class when
// one owned artifact carries a changed link count: cleanup-conflict
// on POSIX, matching the unix cleanup-prove machine and the Rust
// scratch_tests expectation (that module is gated unix-only).
func scratchChangedLinkResidueClass() format.ErrorCode {
	return format.CodeCleanupConflict
}
