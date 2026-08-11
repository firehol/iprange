// Package mapping owns the file-backed mapping lifetime of one v4 artifact.
// It is the only package that creates and destroys mappings of persistent
// content; every reader/writer/validation/recovery workflow consumes this
// owner instead of performing its own content I/O.
//
// Persistent content is never transferred through read/write/seek language
// APIs. Page views alias the mapping and are valid only for the lifetime of
// the Mapping; no view may escape the operation that owns it.
//
// Windows gets its own file in milestone 1; this POSIX implementation covers
// Linux, macOS, and FreeBSD.
//
//go:build !windows

package mapping

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Mapping is one read-only file-backed mapping of a committed v4 artifact.
type Mapping struct {
	file *os.File
	data []byte
	size uint64
}

// OpenImmutable opens path as a regular, symlink-free, page-aligned file and
// maps exactly its committed extent read-only under a shared lifetime lock.
// Geometry refusals carry CodeFormatInvalid; operating-system failures carry
// CodeIO. The optional check runs after the shared lifetime lock is held and
// before any byte of the file is mapped, so namespace decisions observe one
// consistent locking state.
func OpenImmutable(path string, check func(clean string) error) (*Mapping, error) {
	clean := filepath.Clean(path)
	// Stat the final name, then reopen with O_NOFOLLOW and verify the file
	// identity again after open: the EvalSymlinks+reopen pattern alone
	// leaves a swap race between check and open.
	before, err := os.Stat(clean)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if !before.Mode().IsRegular() {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "not a regular file"}
	}
	f, err := os.OpenFile(clean, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "open: " + err.Error()}
	}
	cleanup := true
	defer func() {
		if cleanup {
			f.Close()
		}
	}()

	st, err := f.Stat()
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if !st.Mode().IsRegular() {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "not a regular file"}
	}
	if !os.SameFile(before, st) {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "file replaced between stat and open"}
	}
	size := uint64(st.Size())
	if size < 2*format.PageSize {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "file smaller than two pages"}
	}
	if size%format.PageSize != 0 {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "file size not page-aligned"}
	}
	if size > uint64(^uint(0)>>1) {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "file larger than host address space"}
	}

	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH); err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "shared lock: " + err.Error()}
	}
	if check != nil {
		if err := check(clean); err != nil {
			return nil, err
		}
	}
	data, err := unix.Mmap(int(f.Fd()), 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "mmap: " + err.Error()}
	}
	m := &Mapping{file: f, data: data, size: size}
	cleanup = false
	return m, nil
}

// Size returns the mapped committed byte length.
func (m *Mapping) Size() uint64 { return m.size }

// File returns the underlying read-only file handle.
func (m *Mapping) File() *os.File { return m.file }

// View returns a checked view of [off, off+length) inside the mapping. The
// returned slice aliases the mapping and must not escape the calling
// operation.
func (m *Mapping) View(off, length uint64) ([]byte, error) {
	if off > m.size || length > m.size-off {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "view out of mapped extent"}
	}
	return m.data[off : off+length], nil
}

// Page returns the checked full page at pgno.
func (m *Mapping) Page(pgno uint32) ([]byte, error) {
	off := uint64(pgno) << format.PageShift
	return m.View(off, format.PageSize)
}

// Close releases the mapping and the shared lifetime lock.
func (m *Mapping) Close() error {
	var first error
	if err := unix.Munmap(m.data); err != nil && first == nil {
		first = &format.Error{Code: format.CodeIO, Detail: "munmap: " + err.Error()}
	}
	m.data = nil
	if err := unix.Flock(int(m.file.Fd()), unix.LOCK_UN); err != nil && first == nil {
		first = &format.Error{Code: format.CodeIO, Detail: "unlock: " + err.Error()}
	}
	if err := m.file.Close(); err != nil && first == nil {
		first = &format.Error{Code: format.CodeIO, Detail: "close: " + err.Error()}
	}
	return first
}
