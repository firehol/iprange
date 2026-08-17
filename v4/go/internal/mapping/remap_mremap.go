//go:build linux

package mapping

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// remapPages resizes the mapping in place via mremap(MREMAP_MAYMOVE). The
// kernel may move the mapping to a new virtual address; the returned slice
// is the new location. The protection of the existing mapping is preserved
// (prot is the caller's current protection), so read-write mappings stay
// writable across growth. On failure the old mapping is still valid and is returned as the data
// slice; callers fail closed by tearing it down (Munmap) and reporting the
// error, never by restoring the old view.
func remapPages(f *os.File, old []byte, oldSize, newSize uint64, prot int) ([]byte, error) {
	data, err := unix.Mremap(old, int(newSize), unix.MREMAP_MAYMOVE)
	if err != nil {
		return old, &format.Error{Code: format.CodeIO, Detail: "mremap: " + err.Error()}
	}
	return data, nil
}
