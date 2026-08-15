//go:build linux

package mapping

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// remapPages resizes the mapping in place via mremap(MREMAP_MAYMOVE). The
// kernel may move the mapping to a new virtual address; the returned slice
// is the new location. The old slice is invalidated.
func remapPages(f *os.File, old []byte, oldSize, newSize uint64) ([]byte, error) {
	data, err := unix.Mremap(old, int(newSize), unix.MREMAP_MAYMOVE)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "mremap: " + err.Error()}
	}
	return data, nil
}
