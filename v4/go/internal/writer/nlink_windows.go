//go:build windows

package writer

import "os"

// regularLinkCount is unsupported on the Windows stub (publication is
// refused before any custody proof); the guarded checks skip.
func regularLinkCount(fi os.FileInfo) (uint64, bool) {
	return 0, false
}
