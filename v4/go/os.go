package iprangedb

import (
	"fmt"
	"os"
	"syscall"
)

// MmapReader is a read-only mmap of a v4 file. Registers in the reader table
// on open, deregisters on close.
type MmapReader struct {
	file     *os.File
	data     []byte
	meta     meta
	guard    *ReaderGuard
	table    *ReaderTable
	path     string
}

func OpenMmap(path string) (*MmapReader, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	length := int(info.Size())
	if length < 2*PageSize {
		file.Close()
		return nil, fmt.Errorf("file too small")
	}
	data, err := syscall.Mmap(int(file.Fd()), 0, length,
		syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("mmap: %w", err)
	}

	// F4 fix: register in reader table BEFORE reading meta pages.
	// Use txn_id=0 as the provisional sentinel: it blocks ALL reclamation
	// (freed_txn_id < 0 is never true for unsigned). After reading meta,
	// update to the real txn_id. Distinct from MaxUint64 ("no readers").
	table, err := OpenReaderTable(path)
	if err != nil {
		syscall.Munmap(data)
		file.Close()
		return nil, fmt.Errorf("reader table: %w", err)
	}
	guard, err := table.Register(0, 0, 0)
	if err != nil {
		table.Close()
		syscall.Munmap(data)
		file.Close()
		return nil, fmt.Errorf("reader register: %w", err)
	}

	// CRC validation: only trust a meta whose checksum verifies.
	metaA := decodeMeta(data[:PageSize])
	metaB := decodeMeta(data[PageSize : 2*PageSize])
	crcA := verifyPage(data[:PageSize])
	crcB := verifyPage(data[PageSize : 2*PageSize])
	var pinnedMeta meta
	switch {
	case crcA && crcB:
		if metaA.txnID >= metaB.txnID { pinnedMeta = metaA } else { pinnedMeta = metaB }
	case crcA:
		pinnedMeta = metaA
	case crcB:
		pinnedMeta = metaB
	default:
		guard.Close()
		table.Close()
		syscall.Munmap(data)
		file.Close()
		return nil, fmt.Errorf("both meta pages fail CRC — corrupt file")
	}

	// Update the reader slot with the real txn_id.
	table.UpdateTxnID(guard.Slot, guard.Pid, guard.ReaderID,
		pinnedMeta.txnID, pinnedMeta.rootPgno, pinnedMeta.treeHeight)

	return &MmapReader{file: file, data: data, meta: pinnedMeta, guard: guard, table: table, path: path}, nil
}

func (m *MmapReader) Bytes() []byte { return m.data }

func (m *MmapReader) Reader() (*Reader, error) {
	// F6 fix: use the pinned meta, not a fresh Open() which could pick
	// up a newer transaction committed after we pinned.
	return &Reader{bytes: m.data, meta: m.meta}, nil
}

func (m *MmapReader) Close() error {
	if m.guard != nil {
		m.guard.Close()
		m.guard = nil
	}
	if m.table != nil {
		m.table.Close()
		m.table = nil
	}
	if m.data != nil {
		syscall.Munmap(m.data)
		m.data = nil
	}
	return m.file.Close()
}

// FileWriter is a file-backed writer. Holds LOCK_EX for its entire lifetime
// (serializes against other writers). Readers are never blocked.
type FileWriter[K ipKey[K]] struct {
	w           *Writer[K]
	store       *mmapStore
	file        *os.File // keeps LOCK_EX alive
	readerTable *ReaderTable
	path        string
}

func CreateFile[K ipKey[K]](path string, scopeMode uint8, createdUnix uint64) (*FileWriter[K], error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0644)
	if err != nil {
		return nil, fmt.Errorf("create: %w", err)
	}
	w, err := Create[K](scopeMode, createdUnix)
	if err != nil {
		file.Close()
		return nil, err
	}
	image, ok := w.IntoImage()
	if !ok {
		file.Close()
		return nil, fmt.Errorf("expected vecPageStore")
	}
	if err := file.Truncate(int64(len(image))); err != nil {
		file.Close()
		return nil, err
	}
	if _, err := file.Write(image); err != nil {
		file.Close()
		return nil, err
	}
	file.Close()
	return OpenFile[K](path)
}

func OpenFile[K ipKey[K]](path string) (*FileWriter[K], error) {
	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	// LOCK_EX held for the entire lifetime — serializes writers.
	fd := int(file.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("locked: %w", err)
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	length := int(info.Size())
	if length < 2*PageSize {
		file.Close()
		return nil, fmt.Errorf("file too small")
	}
	// Read meta to determine committed_pages.
	buf := make([]byte, 2*PageSize)
	if _, err := file.ReadAt(buf, 0); err != nil {
		file.Close()
		return nil, err
	}
	if string(buf[MetaMagic:MetaMagic+8]) != Magic {
		file.Close()
		return nil, fmt.Errorf("bad magic")
	}
	metaA := decodeMeta(buf[:PageSize])
	metaB := decodeMeta(buf[PageSize : 2*PageSize])
	// Pick the authoritative meta, preferring one whose page checksum verifies.
	validA := verifyPage(buf[:PageSize])
	validB := verifyPage(buf[PageSize : 2*PageSize])
	// If BOTH meta pages fail CRC, the file is corrupt. Reject BEFORE calling
	// newMmapStore — newMmapStore truncates the file to (committedPages+chunk)
	// pages, and a garbage total_pages from the corrupt meta would damage the
	// file before openWriter ever gets to reject it.
	if !validA && !validB {
		file.Close()
		return nil, fmt.Errorf("both meta pages fail CRC — corrupt file")
	}
	var committedPages uint32
	switch {
	case validA && !validB:
		committedPages = uint32(metaA.totalPages)
	case !validA && validB:
		committedPages = uint32(metaB.totalPages)
	case metaA.txnID >= metaB.txnID:
		committedPages = uint32(metaA.totalPages)
	default:
		committedPages = uint32(metaB.totalPages)
	}
	// Defensive cap: never let a (possibly corrupt) total_pages exceed the real
	// file size, so newMmapStore never extends/truncates the file erroneously.
	if pageLimit := uint32(length / PageSize); committedPages > pageLimit {
		committedPages = pageLimit
	}

	store, err := newMmapStore(file, committedPages)
	if err != nil {
		file.Close()
		return nil, err
	}

	readerTable, err := OpenReaderTable(path)
	if err != nil {
		store.close()
		file.Close()
		return nil, fmt.Errorf("reader table: %w", err)
	}

	w, err := openWriter[K](store)
	if err != nil {
		readerTable.Close()
		store.close()
		file.Close()
		return nil, err
	}

	// Load the persistent free-list with current reader MVCC state.
	if err := w.LoadFreeList(readerTable.OldestReaderTxnID()); err != nil {
		readerTable.Close()
		store.close()
		file.Close()
		return nil, fmt.Errorf("corrupt free-list on open: %w", err)
	}

	return &FileWriter[K]{
		w:           w,
		store:       store,
		file:        file,
		readerTable: readerTable,
		path:        path,
	}, nil
}

// Delegated API (core operations)
func (fw *FileWriter[K]) Set(from, to K, scopeID uint32) error { return fw.w.Set(from, to, scopeID) }
func (fw *FileWriter[K]) Delete(from, to K) (Changed, error)   { return fw.w.Delete(from, to) }
func (fw *FileWriter[K]) Append(from, to K, scopeID uint32) error {
	return fw.w.Append(from, to, scopeID)
}
func (fw *FileWriter[K]) Commit(updatedUnix uint64) error {
	// I1 fix: hold LOCK_SH on the reader companion file for the entire commit.
	// This blocks reader Register (LOCK_EX) during the query→meta-flip window,
	// so a reader cannot register after the oldest-txn snapshot. Any reader
	// that arrives during the commit is forced to wait until the meta flip +
	// LoadFreeList complete — after which it pins the new txn and is unaffected
	// by page reuse. Without this, the writer could reuse pages the reader's
	// pinned transaction still references.
	lockFile, err := fw.readerTable.LockForCommit()
	if err != nil {
		return fmt.Errorf("commit lock: %w", err)
	}
	defer func() {
		syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
	}()
	// Query the oldest live reader fresh at commit time (under the lock).
	oldest := fw.readerTable.OldestReaderTxnID()
	return fw.w.Commit(updatedUnix, oldest)
}
func (fw *FileWriter[K]) RecordCount() uint64              { return fw.w.RecordCount() }
func (fw *FileWriter[K]) Scan(f func(K, K, uint32)) error  { return fw.w.Scan(f) }

// Delegated API (feed operations)
func (fw *FileWriter[K]) FeedAddRange(from, to K, feedBit uint32) error {
	return fw.w.FeedAddRange(from, to, feedBit)
}
func (fw *FileWriter[K]) FeedRemoveRange(from, to K, feedBit uint32) error {
	return fw.w.FeedRemoveRange(from, to, feedBit)
}

// Delegated API (scope operations — mode 2)
func (fw *FileWriter[K]) ScopeIntern(bitmap []byte) (uint32, error) {
	return fw.w.ScopeIntern(bitmap)
}
func (fw *FileWriter[K]) ScopeResolve(scopeID uint32) []byte {
	return fw.w.ScopeResolve(scopeID)
}

// Delegated API (migration)
func (fw *FileWriter[K]) Migrate(desired DesiredStream[K], opts *MigrateOptions[K]) (*MigrateCounters, error) {
	return Migrate(fw.w, desired, opts)
}

func (fw *FileWriter[K]) MigrateRetention(desired DesiredStream[K]) (*MigrateCounters, error) {
	return MigrateRetention(fw.w, desired)
}

func (fw *FileWriter[K]) MigrateFeed(feedBit uint32, desired DesiredStream[K], opts *MigrateOptions[K]) (*MigrateCounters, error) {
	return MigrateFeed(fw.w, feedBit, desired, opts)
}

// Delegated API (overlap)
func (fw *FileWriter[K]) AllToAllOverlap(onOverlap func(FeedOverlap)) error {
	return AllToAllOverlap(fw.w, onOverlap)
}

// ForeignVsAll streams foreign ranges via nextForeign (closure form), avoiding
// a materialized slice. See overlap.ForeignVsAll.
func (fw *FileWriter[K]) ForeignVsAll(nextForeign func() (K, K, bool), onOverlap func(feed, foreignID uint32, ipCount uint64)) error {
	return ForeignVsAll(fw.w, nextForeign, onOverlap)
}

// ForeignVsAllFromSlice is the slice-based convenience wrapper.
func (fw *FileWriter[K]) ForeignVsAllFromSlice(foreign []ForeignRange[K], onOverlap func(feed, foreignID uint32, ipCount uint64)) error {
	return ForeignVsAllFromSlice(fw.w, foreign, onOverlap)
}

func (fw *FileWriter[K]) Close() error {
	if fw.readerTable != nil {
		fw.readerTable.Close()
		fw.readerTable = nil
	}
	if fw.store != nil {
		fw.store.close()
		fw.store = nil
	}
	if fw.file != nil {
		fw.file.Close() // releases LOCK_EX
		fw.file = nil
	}
	return nil
}
