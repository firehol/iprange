// Package mapping owns the file-backed mapping lifetime of one v4 artifact.
// It is the only package that creates and destroys mappings of persistent
// content; every reader/writer/validation/recovery workflow consumes this
// owner instead of performing its own content I/O.
//
// Persistent content is never transferred through read/write/seek language
// APIs. Page views alias the mapping and are valid only for the lifetime of
// the Mapping. Writer views alias the mapping too, but internal/mapping has
// no pin guard: Grow, Remap, and Shrink invalidate every outstanding view
// (mremap may move the mapping), so writer code must re-fetch views after
// every resize and must never retain a view across Grow/Remap/Shrink (the
// reader facade's pins
// guard only reader views). Retained slices (membership leaves) may survive the lookup
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
	"sort"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Mapping is one file-backed mapping of a committed v4 artifact: read-only
// for immutable readers, read-write for the single live writer.
type Mapping struct {
	file     *os.File
	data     []byte
	size     uint64 // currently mapped byte length
	physical uint64 // file size at open (locked extent)
	prot     int    // mmap protection of the current mapping
	locked   bool   // whether the main-file lifetime lock is held
	// unreadablePages is the sorted, duplicate-free page list the
	// Page read path refuses with the io-unreadable class before any
	// range check (Rust mapping.rs unreadable_pages; nil when no page
	// is declared unreadable).
	unreadablePages []uint32
}

// openMapping implements the shared open path for read-only and read-write
// mappings: pre-stat, O_NOFOLLOW open, path identity verification, the
// lifetime lock (shared for readers, exclusive for the writer), geometry
// checks, and the first mapping, each followed by the same identity and
// namespace re-checks as the Rust open_immutable path. The optional check
// runs after the lifetime lock is held and before any byte of the file is
// mapped, so namespace decisions observe one consistent locking state. A
// replacement race must never publish a mapping of an old unlinked inode
// while the path names a new database; an identity change or non-regular
// path entry under the lock is the WrongState class (Rust WrongMode maps to
// code 11).
//
// Both modes bootstrap-map exactly the two meta pages (O(1) bootstrap,
// spec section 3), mirroring Rust map_writer (database_file.rs:
// read_write(file, 2*PAGE_SIZE) then remap(committed)): the writer selects
// the committed extent from the meta pair and Remaps to it, so a huge
// corrupt or unpublished tail never costs VA and never becomes writable at
// open.
func openMapping(path string, rdwr bool, takeLock func(fd int) error, check func(clean string) error) (*Mapping, error) {
	clean := filepath.Clean(path)
	// Refuse read-write live opens on platforms without proven live
	// coordination before any path access, mirroring Rust
	// require_live_supported (binary-format-v4.md platform table). The
	// read-only immutable path never takes this gate: FreeBSD immutable
	// readers keep the canonical whole-file shared flock lifetime lock.
	// OpenLiveReader (also read-only) refuses explicitly before calling
	// openMapping, matching Rust LiveReaderCore::open -> require_live_supported.
	if rdwr {
		if err := requireLiveCoordination(); err != nil {
			return nil, err
		}
	}
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
	flags := os.O_RDONLY
	prot := unix.PROT_READ
	if rdwr {
		flags = os.O_RDWR
		prot = unix.PROT_READ | unix.PROT_WRITE
	}
	f, err := os.OpenFile(clean, flags|unix.O_NOFOLLOW, 0)
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
		// Re-stat the path itself (no symlink following) and compare
		// against the opened inode: this is the check that detects
		// replacement after the fd was opened.
		now, err := os.Lstat(clean)
		if err != nil {
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
	if err := takeLock(int(f.Fd())); err != nil {
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
	// Stat the locked file for geometry validation: the size is sampled
	// under the lifetime lock, so a concurrent writer cannot change the
	// extent between stat and mmap.
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
	data, err := unix.Mmap(int(f.Fd()), 0, 2*format.PageSize, prot, unix.MAP_SHARED)
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
	m := &Mapping{file: f, data: data, size: 2 * format.PageSize, physical: size, prot: prot, locked: true}
	cleanup = false
	return m, nil
}

// OpenImmutable opens path as a regular, symlink-free, page-aligned file and
// maps exactly its committed extent read-only under a shared lifetime lock.
// Geometry refusals carry CodeFormatInvalid; operating-system failures carry
// CodeIO. See openMapping for the full identity and namespace contract.
func OpenImmutable(path string, check func(clean string) error) (*Mapping, error) {
	return openMapping(path, false, lockLifetimeShared, check)
}

// OpenMutable opens path for the single live writer: O_RDWR under the
// exclusive lifetime lock (readers hold the same byte range shared, so a
// mapped reader and a truncating writer can never overlap) with a read-write
// bootstrap mapping of the two meta pages, mirroring Rust map_writer with
// live_lock exclusive mode. The writer selects the committed extent from the
// meta pair and calls Remap(committed) before editing; Grow extends the file
// and the mapping for allocations. Format and identity checks are identical
// to OpenImmutable. Only this package may create and destroy mappings; the
// descriptor never escapes it.
func OpenMutable(path string, check func(clean string) error) (*Mapping, error) {
	return openMapping(path, true, lockLifetimeExclusive, check)
}

// OpenMutableShared opens path read-write under the shared lifetime lock
// for the live writer (Rust open_main lock_file(MAIN_LIFETIME_LOCK,
// Shared)): the sidecar writer claim provides writer exclusivity, so the
// exclusive-lock substitution used by the chunk-1 writer open is not
// taken here. Geometry, identity, and namespace checks are identical to
// OpenMutable.
func OpenMutableShared(path string, check func(clean string) error) (*Mapping, error) {
	return openMapping(path, true, lockLifetimeShared, check)
}

// OpenLiveReader opens path read-only under the shared lifetime lock for
// one registered live reader (Rust LiveReaderCore::open: open_read_only,
// lock MAIN_LIFETIME_LOCK shared, map_reader OpenMode::LiveReader). The
// sidecar is required and is opened by the live reader core, never here;
// no sidecar-absence check runs. Geometry, identity, and namespace checks
// are identical to OpenImmutable.
func OpenLiveReader(path string, check func(clean string) error) (*Mapping, error) {
	// Live readers coordinate with the writer through the OFD lifetime
	// lock; on platforms without proven coordination they refuse before
	// any path access, exactly like the live writer (Rust
	// LiveReaderCore::open -> require_live_supported). The rdwr gate in
	// openMapping does not cover this read-only open, so the refusal is
	// explicit here.
	if err := requireLiveCoordination(); err != nil {
		return nil, err
	}
	return openMapping(path, false, lockLifetimeShared, check)
}

// Size returns the currently mapped byte length (2 pages during bootstrap,
// the committed extent after Remap).
func (m *Mapping) Size() uint64 { return m.size }

// PhysicalSize returns the physical file extent: the size recorded at
// open (the locked extent), extended by Grow.
func (m *Mapping) PhysicalSize() uint64 { return m.physical }

// FileIdentity returns the device+inode of the mapped file from the
// owner descriptor (Rust regular_identity over the held File). The
// writer uses it to capture the attempt-file identity at creation, so
// cleanup discard stays bound to the exact inode it created.
func (m *Mapping) FileIdentity() (device uint64, inode uint64, err error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(m.file.Fd()), &st); err != nil {
		return 0, 0, err
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}

// VerifyIdentity re-checks that the path still names the opened inode.
// Called after bootstrap+remap and after writer tail-trim to mirror Rust's
// post-map_reader verify_path_any_link and the writer's open_locked
// verify_pair: a namespace replacement during the remap window must not
// publish a reader or writer bound to an old unlinked inode.
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
// the physical file extent (opened size, extended by Grow). Retained slices from the old
// mapping are invalidated; bootstrap does not retain any, so the caller is
// safe. On same-size the call is a no-op.
func (m *Mapping) Remap(committedBytes uint64) error {
	if m.data == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "mapping closed"}
	}
	if committedBytes%format.PageSize != 0 {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "committed size not page-aligned"}
	}
	if committedBytes == m.size {
		return nil
	}
	// Re-stat the locked file to prove the committed extent still fits;
	// a rogue truncation between open and remap must not map past EOF
	// (Rust mapping.rs remap -> require_file_extent). The open-time
	// physical extent is intentionally not a constraint: a live reader
	// opens under the shared lifetime lock and may remap to a generation
	// the writer grew after the open stat, so only the current file size
	// (sampled here) bounds the remap, exactly like Rust.
	st, err := m.file.Stat()
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if uint64(st.Size()) < committedBytes {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "mapping exceeds the file extent"}
	}
	// Nil the mapping before remapPages so a partial failure (munmap
	// succeeded but mmap failed on non-Linux) leaves the Mapping in a
	// closed state where View returns WrongState instead of slicing
	// unmapped memory. Linux mremap is atomic in the kernel, so the
	// nil-first ordering is safe on every platform.
	old := m.data
	m.data = nil
	data, err := remapPages(m.file, old, m.size, committedBytes, m.prot)
	if err != nil {
		// Fail closed exactly like Rust replace_map (map=None, len=0):
		// the old mapping is torn down (Linux mremap failure leaves it
		// mapped; the fallback already unmapped it) and every later
		// access reports WrongState.
		if data != nil {
			unix.Munmap(data)
		}
		m.size = 0
		return err
	}
	m.data = data
	m.size = committedBytes
	work.MappingRemap(1)
	return nil
}

// Grow extends the file and the mapping to newSize for a mutable mapping,
// mirroring Rust mapping.rs resize: ftruncate first, then remap. It refuses
// read-only mappings, any request below the opened physical extent (a Grow
// must never truncate the file; Remap covers sizes within the extent),
// non-page-aligned or oversized requests, and growth on a closed mapping.
// On remap failure the file may already be extended but the mapping is left
// fail-closed exactly like Rust replace_map: the old mapping is unmapped
// and every later access reports WrongState.
func (m *Mapping) Grow(newSize uint64) error {
	if m.data == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "mapping closed"}
	}
	if m.prot&unix.PROT_WRITE == 0 {
		return &format.Error{Code: format.CodeWrongState, Detail: "mapping is read-only"}
	}
	if newSize%format.PageSize != 0 {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "new size not page-aligned"}
	}
	if newSize > uint64(^uint(0)>>1) {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "size larger than host address space"}
	}
	if newSize == m.size {
		return nil
	}
	if newSize < m.size {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "shrink is not supported by Grow"}
	}
	if newSize < m.physical {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "grow below the opened physical extent"}
	}
	if err := unix.Ftruncate(int(m.file.Fd()), int64(newSize)); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "ftruncate: " + err.Error()}
	}
	old := m.data
	m.data = nil
	data, err := remapPages(m.file, old, m.size, newSize, m.prot)
	if err != nil {
		// Fail closed exactly like Rust replace_map (map=None, len=0).
		if data != nil {
			unix.Munmap(data)
		}
		m.size = 0
		return err
	}
	m.data = data
	m.size = newSize
	m.physical = newSize
	work.MappingGrowth(1)
	return nil
}

// Flush synchronizes the mapped pages to the file (msync MS_SYNC), mirroring
// Rust mapping.rs flush_range over the whole mapped extent.
func (m *Mapping) Flush() error {
	if m.data == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "mapping closed"}
	}
	if err := unix.Msync(m.data, unix.MS_SYNC); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "msync: " + err.Error()}
	}
	work.MappingFlush(1)
	return nil
}

// FlushRange makes the mapped pages of [offset, offset+length) durable
// (msync MS_SYNC; Rust mapping.rs flush_range). The request must be
// page-aligned and inside the mapped extent, exactly like Rust
// checked_range + flush_range; the caller is the publication path (data
// flush of the committed extent, meta-page flush).
//
// The synced range is [0, offset+length), not the literal subrange:
// macOS/XNU msync rejects any range that does not start at the mapping
// base with EINVAL (verified natively on darwin 25.5: aligned subranges
// [1:2], [2:7] fail, [0:n] succeeds), so the base-prefix shape is the
// only single implementation that is portable across linux, darwin, and
// freebsd. Pages before offset are already clean in the durability flows
// (their own flush or the create write), so the wider msync is a no-op
// scan there; the Rust subrange shape is affected by the same macOS
// limitation (Rust follow-up recorded in SOW-0025).
func (m *Mapping) FlushRange(offset, length uint64) error {
	if m.data == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "mapping closed"}
	}
	if offset%format.PageSize != 0 || length%format.PageSize != 0 {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "flush range not page-aligned"}
	}
	if offset > m.size || length > m.size-offset {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "flush range out of mapped extent"}
	}
	if err := unix.Msync(m.data[:offset+length], unix.MS_SYNC); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "msync: " + err.Error()}
	}
	work.MappingFlush(1)
	return nil
}

// FlushPage synchronizes one mapped page to the file (Rust mapping.rs
// flush_page: flush_range over one page at page_number).
func (m *Mapping) FlushPage(pgno uint32) error {
	return m.FlushRange(uint64(pgno)<<format.PageShift, format.PageSize)
}

// FileSize re-stats the locked file and returns its physical length (Rust
// mapping.rs file().metadata().len()). The tracked PhysicalSize is kept in
// sync by Grow/Shrink and is authoritative for sizing; FileSize observes
// the real locked extent where the caller must see external truncation
// (committed selection, draft-length proof, and tail-evidence paths).
func (m *Mapping) FileSize() (uint64, error) {
	if m.file == nil {
		return 0, &format.Error{Code: format.CodeWrongState, Detail: "mapping closed"}
	}
	st, err := m.file.Stat()
	if err != nil {
		return 0, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	return uint64(st.Size()), nil
}

// SyncFile forces the file's data to stable storage, mirroring Rust
// mapping.rs sync_file: fsync on POSIX platforms, fcntl(F_FULLFSYNC) on
// macOS (plain fsync on macOS can return before the drive's volatile cache
// is flushed).
func (m *Mapping) SyncFile() error {
	if m.file == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "mapping closed"}
	}
	if err := syncFile(int(m.file.Fd())); err != nil {
		return err
	}
	work.FileSync(1)
	return nil
}

// View returns a checked view of [off, off+length) inside the mapping. The
// returned slice aliases the mapping; it must not outlive the Mapping
// (retained slices are permitted only under a live pin guard, per the
// package doc).
func (m *Mapping) View(off, length uint64) ([]byte, error) {
	if m.data == nil {
		// Also reached after Unmap: the mapping object is alive but has
		// no mapped bytes (Rust map=None), so the state is unavailable,
		// not closed.
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "mapping unavailable"}
	}
	if off > m.size || length > m.size-off {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "view out of mapped extent"}
	}
	return m.data[off : off+length], nil
}

// Page returns the checked full page at pgno.
// VisitPage runs fn over one mapped page view, keeping the underlying
// mapping alive for the callback (used by the output builder's seal loop).
func (m *Mapping) VisitPage(pgno uint32, fn func(page []byte) error) error {
	page, err := m.Page(pgno)
	if err != nil {
		return err
	}
	return fn(page)
}

// SetUnreadablePages declares the mapped pages the Page read path must
// refuse with the io-unreadable class (Rust mapping.rs
// set_unreadable_pages). pages must be sorted and strictly increasing;
// the refusal is the exact Rust InvalidArgument detail. An empty list
// clears the declaration. Only the full-page read path refuses: View
// and the writer views are unaffected, exactly like the Rust page()
// before bytes().
func (m *Mapping) SetUnreadablePages(pages []uint32) error {
	for index := 1; index < len(pages); index++ {
		if pages[index-1] >= pages[index] {
			return &format.Error{Code: format.CodeInvalidArgument, Detail: "unreadable mapped pages must be sorted and unique"}
		}
	}
	m.unreadablePages = nil
	if len(pages) > 0 {
		m.unreadablePages = append([]uint32(nil), pages...)
	}
	return nil
}

// Page returns the checked full page at pgno. A page declared
// unreadable by SetUnreadablePages is refused with the io-unreadable
// class (CodeIO, the Rust EIO semantics) before any range check, so a
// declared page is refused even when its number lies outside the mapped
// extent, exactly like Rust page(): the unreadable binary search runs
// before page_offset. Non-declared pages keep the plain range check.
func (m *Mapping) Page(pgno uint32) ([]byte, error) {
	if len(m.unreadablePages) > 0 {
		found := sort.Search(len(m.unreadablePages), func(i int) bool {
			return m.unreadablePages[i] >= pgno
		})
		if found < len(m.unreadablePages) && m.unreadablePages[found] == pgno {
			return nil, &format.Error{Code: format.CodeIO, Detail: "unreadable mapped page"}
		}
	}
	off := uint64(pgno) << format.PageShift
	return m.View(off, format.PageSize)
}

// Unmap releases the current mapping without touching the descriptor or
// the lifetime lock (Rust Mapping::unmap). The live reader close unmaps
// before clearing its slot and releases the lifetime lock only at the
// final close step; the immutable reader never calls Unmap separately.
func (m *Mapping) Unmap() error {
	if m.data == nil {
		return nil
	}
	if err := unix.Munmap(m.data); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "munmap: " + err.Error()}
	}
	m.data = nil
	return nil
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
	if m.data != nil {
		if err := unix.Munmap(m.data); err != nil && first == nil {
			first = &format.Error{Code: format.CodeIO, Detail: "munmap: " + err.Error()}
		}
	}
	m.data = nil
	if m.locked {
		if err := unlockLifetime(int(m.file.Fd())); err != nil && first == nil {
			first = err
		}
	}
	if err := m.file.Close(); err != nil && first == nil {
		first = &format.Error{Code: format.CodeIO, Detail: "close: " + err.Error()}
	}
	m.file = nil
	return first
}
