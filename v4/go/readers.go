package iprangedb

import (
	"fmt"
	"os"
	"syscall"
)

// Reader registration companion file (LMDB model).
// Each slot: 32 bytes (pid:u32, txn_id:u64, padding:24 bytes).
// 128 slots in the default 4096-byte file.

const (
	slotSize     = 32
	maxSlots     = 4096 / slotSize
	slotPidOff   = 0
	slotTxnIDOff = 4
)

// ReaderTable holds a writable mmap of the companion file.
type ReaderTable struct {
	data   []byte
	file   *os.File
	mySlot int
	path   string
}

// ReaderGuard deregisters the slot on Close().
type ReaderGuard struct {
	slot int
	path string
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
	off := g.slot * slotSize
	if off+4 <= len(data) {
		copy(data[off:off+4], []byte{0, 0, 0, 0})
	}
	syscall.Munmap(data)
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

// Register claims a slot for this process at txnID.
func (t *ReaderTable) Register(txnID uint64) (*ReaderGuard, error) {
	pid := uint32(os.Getpid())
	slot, err := t.findOrClaimSlot(pid)
	if err != nil {
		return nil, err
	}
	t.writeSlot(slot, pid, txnID)
	t.mySlot = slot
	return &ReaderGuard{slot: slot, path: t.path}, nil
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

func (t *ReaderTable) findOrClaimSlot(pid uint32) (int, error) {
	var freeSlot = -1
	for i := 0; i < maxSlots; i++ {
		sp := t.slotPid(i)
		if sp == pid {
			return i, nil
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

func (t *ReaderTable) writeSlot(slot int, pid uint32, txnID uint64) {
	off := slot * slotSize
	putU32(t.data, off+slotPidOff, pid)
	putU64(t.data, off+slotTxnIDOff, txnID)
}

func (t *ReaderTable) clearSlot(slot int) {
	off := slot * slotSize
	putU32(t.data, off+slotPidOff, 0)
}

func (t *ReaderTable) slotPid(slot int) uint32 {
	return u32le(t.data, slot*slotSize+slotPidOff)
}

func (t *ReaderTable) slotTxnID(slot int) uint64 {
	return u64le(t.data, slot*slotSize+slotTxnIDOff)
}

func isProcessAlive(pid int) bool {
	// kill(pid, 0) checks existence.
	err := syscall.Kill(pid, 0)
	return err == nil
}

