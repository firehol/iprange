//go:build windows

package recovery

import "github.com/firehol/iprange/v4/go/internal/format"

// scratchChangedLinkResidueClass is the expected residue class when
// one owned artifact carries a changed link count. On Windows the GC
// identity proof folds the link-count namespace error through the
// shared problem table exactly like Rust (problem.rs
// NamespaceError::LinkCount -> Conflict); Rust's own scratch test
// expecting cleanup-conflict is gated unix-only, so the Windows
// oracle is the Rust windows code path, not the test.
func scratchChangedLinkResidueClass() format.ErrorCode {
	return format.CodeConflict
}
