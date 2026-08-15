// Package mapping owns the file-backed mapping lifetime of one v4 artifact.
// It is the only package that creates and destroys mappings of persistent
// content; every reader/writer/validation/recovery workflow consumes this
// owner instead of performing its own content I/O.
//
// Persistent content is never transferred through read/write/seek language
// APIs. Page views alias the mapping and are valid only for the lifetime of
// the Mapping. Retained slices (membership leaves) may survive the lookup
// operation that produced them, but only while a live pin guards the
// mapping: the reader cannot close while pins exist, and every public view
// checks its pin before touching the bytes, so a retained slice never
// outlives the mapping it aliases.
//
// Windows gets its own file in milestone 1; this POSIX implementation
// covers Linux and macOS (OFD lifetime lock). FreeBSD has no proven OFD
// byte-range primitive, so live coordination is unsupported there, but
// immutable readers keep the canonical whole-file shared flock lifetime lock
// (binary-format-v4.md platform table; Rust live_lock.rs freebsd_file_lock).
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
	file     *os.File
	data     []byte
	size     uint64 // currently mapped byte length
	physical uint64 // file size at open (locked extent)
}

// OpenImmutable opens path as a regular, symlink-free, page-aligned file and
// maps exactly its committed extent read-only under a shared lifetime lock.
// Geometry refusals carry CodeFormatInvalid; operating-system failures carry
// CodeIO. The optional check runs after the shared lifetime lock is held and
// before any byte of the file is mapped, so namespace decisions observe one
// consistent locking state. Path identity is verified after the open, after
// the lock, and after mapping; each check re-stats the path and requires it
// to still name the opened inode, mirroring Rust open_immutable
// (verify_path_any_link): a replacement race must never publish a mapping of
// an old unlinked inode while the path names a new database. An identity
// change or non-regular path entry under the lock is the WrongState class
// (Rust WrongMode maps to code 11).
func OpenImmutable(path string, check func(clean string) error) (*Mapping, error) {
	clean := filepath.Clean(path)
	// Stat the final name before opening so non-regular files (FIFOs,
	// directories) are refused without a blocking or surprising open, then
	// reopen with O_NOFOLLOW. The fd identity, not this first stat, is the
	// reference for every later path identity check: the initial stat may
	// already be stale, so it must never veto the opened file.
	before, err := os.Stat(clean)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if !before.Mode().IsRegular() {
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "not a regular file"}
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

	verifyPathIdentity := func() error {
		st, err := f.Stat()
		if err != nil {
			return &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
		}
		if !st.Mode().IsRegular() {
			return &format.Error{Code: format.CodeInvalidArgument, Detail: "not a regular file"}
		}
		// Re-stat the path itself (no symlink following, like Rust's
		// symlink_metadata-based directory entry) and compare against the
		// opened inode: this is the check that detects replacement after
		// the fd was opened.
		now, err := os.Lstat(clean)
		if err != nil {
			// An unlinked path is NameNotFound, mirroring Rust
			// verify_path_inner; any other stat failure stays IO.
			if os.IsNotExist(err) {
				return &format.Error{Code: format.CodeNameNotFound, Detail: "path removed while opening"}
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
	if err := lockLifetimeShared(int(f.Fd())); err != nil {
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
	// Stat the locked file for geometry validation, mirroring Rust
	// map_reader (database_file.rs:107-112): the size is sampled under
	// the shared lifetime lock, so a writer cannot change the extent
	// between stat and mmap.
	st, err := f.Stat()
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
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
	// Bootstrap maps exactly the two meta pages (O(1) bootstrap, spec
	// section 3). The committed extent is mapped by Remap after bootstrap
	// proves the meta pair, so a huge corrupt tail never costs VA.
	data, err := unix.Mmap(int(f.Fd()), 0, 2*format.PageSize, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "mmap: " + err.Error()}
	}
	// The path may have been replaced while the lock was taken or the
	// mapping was created; recheck identity and the namespace contract on
	// the mapped file before publishing the Mapping.
	if err := verifyPathIdentity(); err != nil {
		unix.Munmap(data)
		return nil, err
	}
	if check != nil {
		if err := check(clean); err != nil {
			unix.Munmap(data)
			return nil, err
		}
	}
	m := &Mapping{file: f, data: data, size: 2 * format.PageSize, physical: size}
	cleanup = false
	return m, nil
}

// Size returns the currently mapped byte length (2 pages during bootstrap,
// the committed extent after Remap).
func (m *Mapping) Size() uint64 { return m.size }

// PhysicalSize returns the file size recorded at open (the locked extent).
func (m *Mapping) PhysicalSize() uint64 { return m.physical }

// VerifyIdentity re-checks that the path still names the opened inode.
// Called after bootstrap+remap to mirror Rust's post-map_reader
// verify_path_any_link: a namespace replacement during the remap window
// must not publish a mapping of an old unlinked inode.
func (m *Mapping) VerifyIdentity(path string) error {
	if m.file == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "mapping closed"}
	}
	st, err := m.file.Stat()
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	now, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return &format.Error{Code: format.CodeNameNotFound, Detail: "path removed while open"}
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

// Remap resizes the mapping to exactly committedBytes. The file handle and
// lifetime lock are retained. The initial bootstrap mapping is exactly two
// meta pages; Remap grows it to the committed extent after bootstrap proves
// the meta pair. committedBytes must be page-aligned and must not exceed
// the physical file size recorded at open. Retained slices from the old
// mapping are invalidated; bootstrap does not retain any, so the caller is
// safe. On same-size the call is a no-op.
func (m *Mapping) Remap(committedBytes uint64) error {
	if m.data == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "mapping closed"}
	}
	if committedBytes%format.PageSize != 0 {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "committed size not page-aligned"}
	}
	if committedBytes > m.physical {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "committed exceeds physical extent"}
	}
	if committedBytes == m.size {
		return nil
	}
	// Re-stat the locked file to prove the committed extent still fits;
	// a rogue truncation between open and remap must not map past EOF
	// (Rust mapping.rs remap -> require_file_extent).
	st, err := m.file.Stat()
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if uint64(st.Size()) < committedBytes {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "committed extent exceeds current file size"}
	}
	// Nil the mapping before remapPages so a partial failure (munmap
	// succeeded but mmap failed on non-Linux) leaves the Mapping in a
	// closed state where View returns WrongState instead of slicing
	// unmapped memory. Linux mremap is atomic in the kernel, so the
	// nil-first ordering is safe on every platform.
	old := m.data
	m.data = nil
	data, err := remapPages(m.file, old, m.size, committedBytes)
	if err != nil {
		// Linux mremap failure returns the old slice (still valid);
		// restore it so Close can unmap it. Fallback failure returns
		// nil (old mapping already unmapped); leave m.data nil so
		// View returns WrongState and Close skips the munmap.
		if data != nil {
			m.data = data
		}
		m.size = 0
		return err
	}
	m.data = data
	m.size = committedBytes
	return nil
}

// View returns a checked view of [off, off+length) inside the mapping. The
// returned slice aliases the mapping; it must not outlive the Mapping
// (retained slices are permitted only under a live pin guard, per the
// package doc).
func (m *Mapping) View(off, length uint64) ([]byte, error) {
	if m.data == nil {
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "mapping closed"}
	}
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

// Close releases the mapping and the shared lifetime lock. Close is
// idempotent: a second Close returns nil, and every later View/Page access
// reports the typed wrong-state error instead of touching the released
// mapping.
func (m *Mapping) Close() error {
	if m.file == nil {
		return nil // already closed
	}
	var first error
	if err := unix.Munmap(m.data); err != nil && first == nil {
		first = &format.Error{Code: format.CodeIO, Detail: "munmap: " + err.Error()}
	}
	m.data = nil
	if err := unlockLifetime(int(m.file.Fd())); err != nil && first == nil {
		first = err
	}
	if err := m.file.Close(); err != nil && first == nil {
		first = &format.Error{Code: format.CodeIO, Detail: "close: " + err.Error()}
	}
	m.file = nil
	return first
}
