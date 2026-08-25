//go:build !linux && !windows

package mapping

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// remapPages resizes the mapping by unmapping the old extent and mapping the
// new one with the given protection (PROT_READ for immutable mappings,
// PROT_READ|PROT_WRITE for the live writer). On Munmap failure the old
// mapping is still valid and is returned as the data slice; callers fail
// closed by tearing it down (Munmap) and reporting the error. On Mmap
// failure the old mapping is already unmapped and nil is returned; the
// caller must not restore it.
func remapPages(f *os.File, old []byte, oldSize, newSize uint64, prot int) ([]byte, error) {
	// The Rust authority unmaps before every extent change; the old
	// view is normally already released by the caller, and Munmap of
	// an empty slice must not reach the kernel.
	if len(old) > 0 {
		if err := unix.Munmap(old); err != nil {
			return old, &format.Error{Code: format.CodeIO, Detail: "munmap: " + err.Error()}
		}
	}
	data, err := unix.Mmap(int(f.Fd()), 0, int(newSize), prot, unix.MAP_SHARED)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "mmap: " + err.Error()}
	}
	return data, nil
}
