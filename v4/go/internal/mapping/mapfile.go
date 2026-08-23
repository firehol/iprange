//go:build !windows

package mapping

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// MapFile maps an already-open descriptor without taking any lock.
// It exists for coordination artifacts whose byte-range locks are
// independent of the main-file lifetime lock: the live reader-table
// sidecar maps its complete fixed extent read-write and takes the gate,
// writer, and slot locks itself (Rust Mapping::read_write_view +
// live_sidecar.rs). The descriptor is duplicated so the Mapping owns
// its own close state; the caller retains the original descriptor for
// locking. Only this package may create mappings.
//
// The size is the exact byte extent, not a page multiple: the kernel
// rounds the mapping up to a page, and Mapping bounds (View/Page) never
// reach the padding. This matches memmap2, which maps the exact sidecar
// length and keeps its own length as the accessible extent (Rust
// mapping.rs map_nonempty + checked_subrange).
//
// The file must already extend at least size bytes: every Rust mapping
// constructor proves the extent before mmap (require_file_extent), so
// mapping past EOF returns a typed error instead of a later SIGBUS.
func MapFile(f *os.File, size uint64, rdwr bool) (*Mapping, error) {
	if size == 0 {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "mapping size is zero"}
	}
	if size > uint64(^uint(0)>>1) {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "file larger than host address space"}
	}
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "dup: " + err.Error()}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if st.Size < 0 || uint64(st.Size) < size {
		unix.Close(fd)
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "mapping exceeds the file extent"}
	}
	// The duplicated descriptor must not survive exec: spec 15.6
	// requires every live descriptor close-on-exec so a child process
	// cannot inherit the sidecar mapping and its OFD locks.
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		unix.Close(fd)
		return nil, &format.Error{Code: format.CodeIO, Detail: "fcntl cloexec: " + err.Error()}
	}
	prot := unix.PROT_READ
	if rdwr {
		prot |= unix.PROT_WRITE
	}
	data, err := unix.Mmap(fd, 0, int(size), prot, unix.MAP_SHARED)
	if err != nil {
		unix.Close(fd)
		return nil, &format.Error{Code: format.CodeIO, Detail: "mmap: " + err.Error()}
	}
	return &Mapping{
		file:     os.NewFile(uintptr(fd), f.Name()),
		data:     data,
		size:     size,
		physical: size,
		prot:     prot,
	}, nil
}
