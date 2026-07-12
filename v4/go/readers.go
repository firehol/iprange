package iprangedb

import (
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
)

// Reader registration companion file (LMDB model).
//
// Each reader registers (pid, reader_id, txn_id) in a mmap'd companion file.
// reader_id comes from a process-local atomic counter — no thread_id dependency.
// Each Reader instance gets a unique reader_id, so same-process readers in
// different goroutines each get their own slot.
//
// Each slot: 32 bytes.
//
//	@0  pid: u32        (0 = free slot)
//	@4  reader_id: u32  (unique per Reader instance)
//	@8  txn_id: u64     (the committed generation this reader is using)
//	@16 padding: [u8;16]

const (
	slotSize       = 32
	maxSlots       = 4096 / slotSize
	slotPidOff     = 0
	slotReaderIDOff = 4
	slotTxnIDOff   = 8
	slotRootOff    = 16
	slotHeightOff  = 20
)

// readerIDCounter is a process-local counter for unique reader IDs.
var readerIDCounter uint64 = 1

func nextReaderID() uint32 {
	return uint32(atomic.AddUint64(&readerIDCounter, 1))
}

// ReaderTable holds a writable mmap of the companion file.
type ReaderTable struct {
	data   []byte
	file   *os.File
	mySlot int
	path   string
}

// ReaderGuard deregisters the slot on Close(). It stores pid+reader_id so it
// only clears the slot if they still match (avoids clobbering a different
// reader that reused the slot).
type ReaderGuard struct {
	Slot     int
	Pid      uint32
	ReaderID uint32
	path     string
}

func (g *ReaderGuard) Close() error {
	f, err := os.OpenFile(g.path, os.O_RDWR, 0644)
	if err != nil {
		return nil // file may be gone
	}
	defer f.Close()
	data, err := syscall.Mmap(int(f.Fd()), 0, 4096,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil
	}
	defer syscall.Munmap(data)
	off := g.Slot * slotSize
	if off+slotReaderIDOff+4 <= len(data) {
		// Only clear if our pid+reader_id still match.
		storedPid := u32le(data, off+slotPidOff)
		storedRid := u32le(data, off+slotReaderIDOff)
		if storedPid == g.Pid && storedRid == g.ReaderID {
			putU32(data, off+slotPidOff, 0)
		}
	}
	return nil
}

// OpenReaderTable opens (or creates) the reader table for dbPath.
func OpenReaderTable(dbPath string) (*ReaderTable, error) {
	readersPath := dbPath + ".readers"
	// Atomic creation: O_CREATE|O_EXCL prevents the stat-then-create race.
	f, err := os.OpenFile(readersPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0644)
	if err == nil {
		// Initialize size BEFORE closing so concurrent openers see a complete file.
		f.Truncate(4096)
		f.Close()
	} else if !os.IsExist(err) {
		return nil, fmt.Errorf("create readers: %w", err)
	}

	file, err := os.OpenFile(readersPath, os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open readers: %w", err)
	}

	data, err := syscall.Mmap(int(file.Fd()), 0, 4096,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("mmap readers: %w", err)
	}

	return &ReaderTable{
		data:   data,
		file:   file,
		mySlot: -1,
		path:   readersPath,
	}, nil
}

// Register claims a free/stale slot for this reader at txnID.
//
// The find+claim is wrapped in a brief exclusive flock on the companion file
// to prevent a cross-process TOCTOU race: without it, two processes could
// both observe the same free slot and clobber each other. flock is acquired
// on a fresh open() of the file (flock locks are per-open-file-description,
// so it is independent of the mmap-backed t.file). This serializes only
// registration, not normal reads.
func (t *ReaderTable) Register(txnID uint64, root uint32, height uint32) (*ReaderGuard, error) {
	pid := uint32(os.Getpid())
	readerID := nextReaderID()

	flockFile, err := os.OpenFile(t.path, os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open readers for lock: %w", err)
	}
	defer flockFile.Close()
	if err := syscall.Flock(int(flockFile.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("flock readers: %w", err)
	}
	defer syscall.Flock(int(flockFile.Fd()), syscall.LOCK_UN)

	// Find a free slot under the lock.
	slot, err := t.findFreeSlot()
	if err != nil {
		return nil, err
	}
	t.writeSlot(slot, pid, readerID, txnID, root, height)
	t.mySlot = slot
	return &ReaderGuard{Slot: slot, Pid: pid, ReaderID: readerID, path: t.path}, nil
}

// UpdateTxnID updates the txn_id in a reader's slot after the reader has
// determined its pinned transaction. Used with the F4 fix: register with
// MaxUint64 first (preventing reclamation), then update to the real txn_id.
func (t *ReaderTable) UpdateTxnID(slot int, pid, readerID uint32, txnID uint64, root uint32, height uint32) {
	flockFile, err := os.OpenFile(t.path, os.O_RDWR, 0644)
	if err != nil {
		return
	}
	defer flockFile.Close()
	if err := syscall.Flock(int(flockFile.Fd()), syscall.LOCK_EX); err != nil {
		return
	}
	defer syscall.Flock(int(flockFile.Fd()), syscall.LOCK_UN)

	storedPid := t.slotPid(slot)
	storedRid := t.slotReaderID(slot)
	if storedPid == pid && storedRid == readerID {
		t.writeSlot(slot, pid, readerID, txnID, root, height)
	}
}

// OldestReaderTxnID returns the oldest active reader generation.
// ReaderRoots returns all active reader (root, height) pairs.
func (t *ReaderTable) ReaderRoots() [][2]uint32 {
	var roots [][2]uint32
	for i := 0; i < maxSlots; i++ {
		pid := t.slotPid(i)
		if pid != 0 && isProcessAlive(int(pid)) {
			root := u32le(t.data, i*slotSize+slotRootOff)
			height := u32le(t.data, i*slotSize+slotHeightOff)
			if root != 0 {
				roots = append(roots, [2]uint32{root, height})
			}
		}
	}
	return roots
}

func (t *ReaderTable) OldestReaderTxnID() uint64 {
	oldest := uint64(^uint64(0))
	for i := 0; i < maxSlots; i++ {
		pid := t.slotPid(i)
		if pid != 0 && isProcessAlive(int(pid)) {
			txnID := t.slotTxnID(i)
			if txnID < oldest {
				oldest = txnID
			}
		}
	}
	return oldest
}

// ReapStale clears slots from crashed processes.
func (t *ReaderTable) ReapStale() int {
	cleared := 0
	for i := 0; i < maxSlots; i++ {
		pid := t.slotPid(i)
		if pid != 0 && !isProcessAlive(int(pid)) {
			t.clearSlot(i)
			cleared++
		}
	}
	return cleared
}

func (t *ReaderTable) Close() error {
	// F5 fix: do NOT clear the slot here. ReaderGuard.Close already
	// clears it. If both clear, a reader that reused the slot between
	// the two closes would be incorrectly deregistered.
	if t.data != nil {
		syscall.Munmap(t.data)
		t.data = nil
	}
	return t.file.Close()
}

// findFreeSlot returns the first free or stale slot.
func (t *ReaderTable) findFreeSlot() (int, error) {
	for i := 0; i < maxSlots; i++ {
		sp := t.slotPid(i)
		if sp == 0 || !isProcessAlive(int(sp)) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("reader table full")
}

func (t *ReaderTable) writeSlot(slot int, pid, readerID uint32, txnID uint64, root uint32, height uint32) {
	off := slot * slotSize
	// Write fields in reverse publication order: height, root, txnID, readerID, PID last.
	// PID is the "publish" marker — a scanning writer only considers a slot valid
	// when PID != 0, by which point all other fields are already written.
	putU32(t.data, off+slotHeightOff, height)
	putU32(t.data, off+slotRootOff, root)
	putU64(t.data, off+slotTxnIDOff, txnID)
	putU32(t.data, off+slotReaderIDOff, readerID)
	putU32(t.data, off+slotPidOff, pid) // publish marker — write last
}

func (t *ReaderTable) clearSlot(slot int) {
	off := slot * slotSize
	// Clear PID first (unpublish), then clear other fields.
	putU32(t.data, off+slotPidOff, 0)
	putU32(t.data, off+slotReaderIDOff, 0)
	putU64(t.data, off+slotTxnIDOff, 0)
	putU32(t.data, off+slotRootOff, 0)
	putU32(t.data, off+slotHeightOff, 0)
}

func (t *ReaderTable) slotPid(slot int) uint32 {
	return u32le(t.data, slot*slotSize+slotPidOff)
}

func (t *ReaderTable) slotReaderID(slot int) uint32 {
	return u32le(t.data, slot*slotSize+slotReaderIDOff)
}

func (t *ReaderTable) slotTxnID(slot int) uint64 {
	return u64le(t.data, slot*slotSize+slotTxnIDOff)
}

func isProcessAlive(pid int) bool {
	// kill(pid, 0) checks existence.
	err := syscall.Kill(pid, 0)
	return err == nil
}
