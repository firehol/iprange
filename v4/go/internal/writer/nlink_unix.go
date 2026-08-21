//go:build unix

package writer

import (
	"os"
	"syscall"
)

// regularLinkCount returns the hard-link count of a regular file
// (Rust namespace::regular_link_count: the attempt custody proof and
// the identity-guarded cleanup discard both require exactly one link).
func regularLinkCount(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}
