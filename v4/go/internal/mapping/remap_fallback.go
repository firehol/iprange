//go:build !linux && !windows

package mapping

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// remapPages resizes the mapping by unmapping the old extent and mapping the
// new one. On Munmap failure the old mapping is still valid and the old
// slice is returned so the caller can restore it for Close. On Mmap failure
// the old mapping is already unmapped and nil is returned; the caller must
// not restore it.
func remapPages(f *os.File, old []byte, oldSize, newSize uint64) ([]byte, error) {
	if err := unix.Munmap(old); err != nil {
		return old, &format.Error{Code: format.CodeIO, Detail: "munmap: " + err.Error()}
	}
	data, err := unix.Mmap(int(f.Fd()), 0, int(newSize), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "mmap: " + err.Error()}
	}
	return data, nil
}
