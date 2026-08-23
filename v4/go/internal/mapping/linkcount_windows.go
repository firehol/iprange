//go:build windows

package mapping

import "os"

// RegularLinkCount is unsupported on the Windows stub (live coordination
// and publication are refused before any custody proof); the guarded
// checks skip.
func RegularLinkCount(fi os.FileInfo) (uint64, bool) {
	return 0, false
}
