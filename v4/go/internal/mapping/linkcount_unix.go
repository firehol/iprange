//go:build unix

package mapping

import (
	"os"
	"syscall"
)

// RegularLinkCount returns the hard-link count of a regular file
// (Rust namespace::regular_link_count: the sidecar verify_path
// single-link rule, the attempt custody proofs, and the
// identity-guarded cleanup discard all require exactly one link).
func RegularLinkCount(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}
