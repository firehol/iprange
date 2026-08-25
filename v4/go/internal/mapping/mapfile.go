package mapping

import (
	"os"

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
	dup, err := dupFile(f)
	if err != nil {
		return nil, err
	}
	st, err := dup.Stat()
	if err != nil {
		dup.Close()
		return nil, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if uint64(st.Size()) < size {
		dup.Close()
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "mapping exceeds the file extent"}
	}
	prot := protRead
	if rdwr {
		prot |= protWrite
	}
	data, err := mmapShared(dup, int(size), prot)
	if err != nil {
		dup.Close()
		return nil, err
	}
	return &Mapping{
		file:     dup,
		data:     data,
		size:     size,
		physical: size,
		prot:     prot,
	}, nil
}
