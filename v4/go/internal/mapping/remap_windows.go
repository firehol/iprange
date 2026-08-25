//go:build windows

package mapping

import (
	"os"
)

// remapPages resizes the mapping by unmapping the old view and mapping
// the new one with the given protection (Windows has no mremap; the
// Rust authority re-establishes the section view the same way). On
// Munmap failure the old mapping is still valid and is returned as the
// data slice; callers fail closed by tearing it down and reporting the
// error. On Mmap failure the old mapping is already unmapped and nil is
// returned; the caller must not restore it.
func remapPages(f *os.File, old []byte, oldSize, newSize uint64, prot int) ([]byte, error) {
	if err := munmapShared(old); err != nil {
		return old, err
	}
	data, err := mmapShared(f, int(newSize), prot)
	if err != nil {
		return nil, err
	}
	return data, nil
}
