//go:build !windows

package mapping

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Create makes a brand-new empty file-backed mapping for a live database
// creation (Rust live_namespace::create_private + database_file.rs
// write_empty): O_RDWR|O_CREATE|O_EXCL so an existing destination is
// refused (Rust require_absent parity), the exclusive lifetime lock, the
// requested page-aligned extent (set_len = ftruncate), and a read-write
// mapping of exactly that extent. Every *os.File lifecycle step stays
// inside the mapping owner; the descriptor never escapes it. The caller
// writes the two meta pages through Page() and seals durability with
// FlushRange + SyncFile (Rust write_empty), then closes.
func Create(path string, size uint64, check func(clean string) error) (*Mapping, error) {
	if err := requireLiveWriter(); err != nil {
		return nil, err
	}
	clean := filepath.Clean(path)
	if size < 2*format.PageSize {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "creation size smaller than two pages"}
	}
	if size%format.PageSize != 0 {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "creation size not page-aligned"}
	}
	if size > uint64(^uint(0)>>1) {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "size larger than host address space"}
	}
	f, err := os.OpenFile(clean, os.O_RDWR|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, &format.Error{Code: format.CodeNameExists, Detail: "destination exists"}
		}
		return nil, &format.Error{Code: format.CodeIO, Detail: "create: " + err.Error()}
	}
	createdStat, statErr := f.Stat()
	cleanup := true
	defer func() {
		if cleanup {
			f.Close()
			// The destination was exclusively created by us and was
			// never published: remove it so a retried Create is not
			// poisoned by a partial file (Rust live_cleanup::remove
			// parity via remove_exact). Remove only when the path
			// still names the file we created: a concurrent
			// replacement of the destination must never be removed
			// by our cleanup, and a path already removed means
			// nothing to clean.
			if statErr == nil {
				if fi, err := os.Lstat(clean); err == nil && fi.Mode().IsRegular() && os.SameFile(fi, createdStat) {
					_ = os.Remove(clean)
				}
			}
		}
	}()
	verifyPathIdentity := func() error {
		st, err := f.Stat()
		if err != nil {
			return &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
		}
		if !st.Mode().IsRegular() {
			return &format.Error{Code: format.CodeInvalidArgument, Detail: "not a regular file"}
		}
		now, err := os.Lstat(clean)
		if err != nil {
			if os.IsNotExist(err) {
				return &format.Error{Code: format.CodeNameNotFound, Detail: "path removed while creating"}
			}
			return &format.Error{Code: format.CodeIO, Detail: "lstat: " + err.Error()}
		}
		if !now.Mode().IsRegular() {
			return &format.Error{Code: format.CodeWrongState, Detail: "path no longer names a regular file"}
		}
		if !os.SameFile(now, st) {
			return &format.Error{Code: format.CodeWrongState, Detail: "path no longer names the opened file"}
		}
		return nil
	}
	if err := verifyPathIdentity(); err != nil {
		return nil, err
	}
	if err := lockLifetimeExclusive(int(f.Fd())); err != nil {
		return nil, err
	}
	if err := verifyPathIdentity(); err != nil {
		return nil, err
	}
	if check != nil {
		if err := check(clean); err != nil {
			return nil, err
		}
	}
	if err := unix.Ftruncate(int(f.Fd()), int64(size)); err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "ftruncate: " + err.Error()}
	}
	data, err := unix.Mmap(int(f.Fd()), 0, int(size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "mmap: " + err.Error()}
	}
	if err := verifyPathIdentity(); err != nil {
		unix.Munmap(data)
		return nil, err
	}
	m := &Mapping{file: f, data: data, size: size, physical: size, prot: unix.PROT_READ | unix.PROT_WRITE, locked: true}
	cleanup = false
	work.MappingGrowth(1)
	return m, nil
}
