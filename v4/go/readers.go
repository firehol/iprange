package iprangedb

import (
	"fmt"
	"os"
	"syscall"
)

// Reader registration companion file (LMDB model).
//
// Each reader registers (PID, thread_id, txn_id). Multiple readers in the same
// process get separate slots via thread_id differentiation. Registration
// failure is propagated as an error (not silently ignored).
//
// Each slot: 32 bytes.
//
//	@0  pid: u32        (0 = free slot)
//	@4  thread_id: u32  (differentiates same-process readers)
//	@8  txn_id: u64     (the committed generation this reader is using)
//	@16 padding: [u8;16]

const (
	slotSize        = 32
	maxSlots        = 4096 / slotSize
	slotPidOff      = 0
	slotThreadIDOff = 4
	slotTxnIDOff    = 8
)

// ReaderTable holds a writable mmap of the companion file.
type ReaderTable struct {
	data   []byte
	file   *os.File
	mySlot int
	path   string
}

// ReaderGuard deregisters the slot on Close(). It stores the pid+thread_id so
// it only clears the slot if they still match (avoids clobbering a different
// reader that reused the slot).
type ReaderGuard struct {
	slot     int
	pid      uint32
	threadID uint32
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
	off := g.slot * slotSize
	if off+slotThreadIDOff+4 <= len(data) {
		// Only clear if our pid+thread_id still match.
		storedPid := u32le(data, off+slotPidOff)
		storedTid := u32le(data, off+slotThreadIDOff)
		if storedPid == g.pid && storedTid == g.threadID {
			putU32(data, off+slotPidOff, 0)
		}
	}
	return nil
}

// OpenReaderTable opens (or creates) the reader table for dbPath.
func OpenReaderTable(dbPath string) (*ReaderTable, error) {
	readersPath := dbPath + ".readers"
	if _, err := os.Stat(readersPath); os.IsNotExist(err) {
		f, err := os.OpenFile(readersPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return nil, fmt.Errorf("create readers: %w", err)
		}
		f.Truncate(4096)
		f.Close()
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

// Register claims a slot for this process+thread at txnID. Errors are
// propagated (a full reader table is a hard failure).
func (t *ReaderTable) Register(txnID uint64) (*ReaderGuard, error) {
	pid := uint32(os.Getpid())
	threadID := getThreadID()
	slot, err := t.findOrClaimSlot(pid, threadID)
	if err != nil {
		return nil, err
	}
	t.writeSlot(slot, pid, threadID, txnID)
	t.mySlot = slot
	return &ReaderGuard{slot: slot, pid: pid, threadID: threadID, path: t.path}, nil
}

// OldestReaderTxnID returns the oldest active reader generation.
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
	if t.mySlot >= 0 {
		t.clearSlot(t.mySlot)
	}
	if t.data != nil {
		syscall.Munmap(t.data)
		t.data = nil
	}
	return t.file.Close()
}

// findOrClaimSlot returns the slot matching pid+threadID, or the first free/
// stale slot. Same-process different-thread readers do NOT reuse each other's
// slots.
func (t *ReaderTable) findOrClaimSlot(pid, threadID uint32) (int, error) {
	freeSlot := -1
	for i := 0; i < maxSlots; i++ {
		sp := t.slotPid(i)
		st := t.slotThreadID(i)
		if sp == pid && st == threadID {
			return i, nil // reuse our exact slot
		}
		if sp == pid && st != threadID {
			continue // same process, different thread — find a free slot
		}
		if sp == 0 || !isProcessAlive(int(sp)) {
			if freeSlot == -1 {
				freeSlot = i
			}
		}
	}
	if freeSlot == -1 {
		return 0, fmt.Errorf("reader table full")
	}
	return freeSlot, nil
}

func (t *ReaderTable) writeSlot(slot int, pid, threadID uint32, txnID uint64) {
	off := slot * slotSize
	putU32(t.data, off+slotPidOff, pid)
	putU32(t.data, off+slotThreadIDOff, threadID)
	putU64(t.data, off+slotTxnIDOff, txnID)
}

func (t *ReaderTable) clearSlot(slot int) {
	off := slot * slotSize
	putU32(t.data, off+slotPidOff, 0)
}

func (t *ReaderTable) slotPid(slot int) uint32 {
	return u32le(t.data, slot*slotSize+slotPidOff)
}

func (t *ReaderTable) slotThreadID(slot int) uint32 {
	return u32le(t.data, slot*slotSize+slotThreadIDOff)
}

func (t *ReaderTable) slotTxnID(slot int) uint64 {
	return u64le(t.data, slot*slotSize+slotTxnIDOff)
}

func isProcessAlive(pid int) bool {
	// kill(pid, 0) checks existence.
	err := syscall.Kill(pid, 0)
	return err == nil
}
