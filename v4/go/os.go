package iprangedb

import (
	"fmt"
	"os"
	"syscall"
)

// MmapReader is a read-only mmap of a v4 file. Registers in the reader table
// on open, deregisters on close (fixes #8: cross-process MVCC).
type MmapReader struct {
	file    *os.File
	data    []byte
	guard   *ReaderGuard
	table   *ReaderTable
	path    string
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

	// Determine the active txn_id for reader registration.
	metaA := decodeMeta(data[:PageSize])
	metaB := decodeMeta(data[PageSize : 2*PageSize])
	var activeTxnID uint64
	if metaA.txnID >= metaB.txnID {
		activeTxnID = metaA.txnID
	} else {
		activeTxnID = metaB.txnID
	}

	// Register in the reader table (best-effort; if the table can't be opened,
	// we proceed without registration — correctness is the writer's flock).
	mr := &MmapReader{file: file, data: data, path: path}
	if table, err := OpenReaderTable(path); err == nil {
		if guard, err := table.Register(activeTxnID); err == nil {
			mr.guard = guard
			mr.table = table
		} else {
			table.Close()
		}
	}
	return mr, nil
}

func (m *MmapReader) Bytes() []byte { return m.data }

func (m *MmapReader) Reader() (*Reader, error) {
	return Open(m.data)
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

// FileWriter is a file-backed writer using a writable MAP_SHARED mmap. Queries
// the reader table to determine safe page reclamation (fixes #8).
type FileWriter[K ipKey[K]] struct {
	w           *Writer[K]
	store       *mmapStore
	readerTable *ReaderTable
	path        string
}

func CreateFile[K ipKey[K]](path string, scopeMode uint8, createdUnix uint64) (*FileWriter[K], error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0644)
	if err != nil {
		return nil, fmt.Errorf("create: %w", err)
	}
	// Create initial image in memory.
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
	// Brief LOCK_EX|LOCK_NB to serialize writer-open.
	fd := int(file.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("locked: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		syscall.Flock(fd, syscall.LOCK_UN)
		file.Close()
		return nil, err
	}
	length := int(info.Size())
	if length < 2*PageSize {
		syscall.Flock(fd, syscall.LOCK_UN)
		file.Close()
		return nil, fmt.Errorf("file too small")
	}
	// Read meta to determine committed_pages.
	buf := make([]byte, 2*PageSize)
	if _, err := file.ReadAt(buf, 0); err != nil {
		syscall.Flock(fd, syscall.LOCK_UN)
		file.Close()
		return nil, err
	}
	if string(buf[MetaMagic:MetaMagic+8]) != Magic {
		syscall.Flock(fd, syscall.LOCK_UN)
		file.Close()
		return nil, fmt.Errorf("bad magic")
	}
	metaA := decodeMeta(buf[:PageSize])
	metaB := decodeMeta(buf[PageSize : 2*PageSize])
	var committedPages uint32
	if metaA.txnID >= metaB.txnID {
		committedPages = uint32(metaA.totalPages)
	} else {
		committedPages = uint32(metaB.totalPages)
	}
	// Release open-time lock.
	syscall.Flock(fd, syscall.LOCK_UN)

	store, err := newMmapStore(file, committedPages)
	if err != nil {
		file.Close()
		return nil, err
	}
	w, err := openWriter[K](store)
	if err != nil {
		store.close()
		return nil, err
	}

	fw := &FileWriter[K]{w: w, store: store, path: path}

	// Open the reader table and set safe reclaim (fixes #8).
	if rt, err := OpenReaderTable(path); err == nil {
		fw.readerTable = rt
		fw.w.SetSafeReclaimTxnID(rt.OldestReaderTxnID())
	}

	return fw, nil
}

func (fw *FileWriter[K]) Set(from, to K, scopeID uint32) error { return fw.w.Set(from, to, scopeID) }
func (fw *FileWriter[K]) Delete(from, to K) (Changed, error) { return fw.w.Delete(from, to) }
func (fw *FileWriter[K]) Append(from, to K, scopeID uint32) error { return fw.w.Append(from, to, scopeID) }
func (fw *FileWriter[K]) FeedAddRange(from, to K, feedBit uint32) error {
	return fw.w.FeedAddRange(from, to, feedBit)
}
func (fw *FileWriter[K]) FeedRemoveRange(from, to K, feedBit uint32) error {
	return fw.w.FeedRemoveRange(from, to, feedBit)
}
func (fw *FileWriter[K]) Commit(updatedUnix uint64) error {
	if err := fw.w.Commit(updatedUnix); err != nil {
		return err
	}
	// After commit, refresh safe reclaim from the reader table.
	if fw.readerTable != nil {
		fw.w.SetSafeReclaimTxnID(fw.readerTable.OldestReaderTxnID())
	}
	return nil
}
func (fw *FileWriter[K]) RecordCount() uint64 { return fw.w.RecordCount() }
func (fw *FileWriter[K]) Scan(f func(K, K, uint32)) error { return fw.w.Scan(f) }

func (fw *FileWriter[K]) Close() error {
	if fw.readerTable != nil {
		fw.readerTable.Close()
		fw.readerTable = nil
	}
	fw.store.close()
	return nil
}
