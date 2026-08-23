// Fixed live-reader table bound to one database identity (Rust
// live_sidecar.rs, spec section 15). The complete fixed-size sidecar is
// file-mapped; header and slot bytes are read or changed only through
// that retained mapping while the required byte-range locks are held.
// Sidecar content never uses a read/write/seek API.

package live

import (
	"os"
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// Sidecar lock ranges (spec 15.2): the gate at offset 0, the
// single-writer lease at offset 1, and one slot-ownership lock per
// reader slot at its byte offset.
const (
	gateLockOffset     = 0
	writerLockOffset   = 1
	mainLifetimeOffset = uint64(1) << 44
)

// Sidecar is one open reader table bound to one database identity. It
// retains the opened descriptor (byte-range locks), the path, the
// decoded header, the local identity, and one read-write mapping of the
// complete fixed extent.
//
// A Sidecar is owned by one goroutine: methods must not run
// concurrently with each other or with close, exactly like the mapping
// owner's close contract (mapping.go). The mapping pointer is set once
// by initializeCreating or openAny and replaced only by close.
type Sidecar struct {
	file     *os.File
	path     string
	header   header
	identity FileIdentity
	mapping  *mapping.Mapping
}

// reserve creates the sidecar pathname for a new coordination artifact
// without writing any byte (Rust Sidecar::reserve). The caller then
// runs initializeCreating and publishReady. A nil failure returns the
// reserved sidecar; a non-nil failure carries the exact Rust
// PrivateCreationFailure facts (the canonical-sidecar derivation
// failure has a clean cleanup and no identity, exactly like Rust
// reserve).
func reserve(main string, databaseID, sidecarID [16]byte, capacity uint32) (*Sidecar, *privateCreationFailure) {
	path, err := canonicalSidecarPath(main)
	if err != nil {
		return nil, &privateCreationFailure{cause: err}
	}
	return reserveAt(path, databaseID, sidecarID, capacity)
}

// reserveAt is reserve at an explicit path (Rust Sidecar::reserve_at).
func reserveAt(path string, databaseID, sidecarID [16]byte, capacity uint32) (*Sidecar, *privateCreationFailure) {
	if capacity == 0 {
		return nil, &privateCreationFailure{
			cause: &format.Error{Code: format.CodeInvalidArgument, Detail: "reader capacity must be greater than zero"},
		}
	}
	created, failure := createPrivate(path, cleanupAuthority{
		attemptID:     sidecarID,
		ordinal:       1,
		kind:          cleanupKindOwnedCoordination,
		directoryRole: cleanupRoleMainFile,
	})
	if failure != nil {
		return nil, failure
	}
	return &Sidecar{
		file:     created.file,
		path:     filepath.Clean(path),
		header:   header{capacity: capacity, databaseID: databaseID, sidecarID: sidecarID},
		identity: created.identity,
	}, nil
}

// initializeCreating sizes the sidecar, maps it read-write, writes the
// creating header, and synchronizes it (Rust
// Sidecar::initialize_creating, spec 15.5 creation ordering step 1).
func (s *Sidecar) initializeCreating() error {
	length, err := sidecarLength(s.header.capacity)
	if err != nil {
		return err
	}
	if err := s.file.Truncate(int64(length)); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "reader table resize: " + err.Error()}
	}
	m, err := mapping.MapFile(s.file, length, true)
	if err != nil {
		return err
	}
	if s.mapping != nil {
		m.Close()
		return &format.Error{Code: format.CodeWrongState, Detail: "reader table mapping already exists"}
	}
	s.mapping = m
	page, err := s.mapping.Page(0)
	if err != nil {
		return err
	}
	if err := writeHeaderMapping(page, s.header, stateCreating); err != nil {
		return err
	}
	if err := s.mapping.FlushPage(0); err != nil {
		return err
	}
	if err := s.file.Sync(); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "reader table sync: " + err.Error()}
	}
	fault.Crash("create.after_sidecar_sync")
	return nil
}

// publishReady rewrites the header as ready and synchronizes it (Rust
// Sidecar::publish_ready, spec 15.5 creation ordering step 3).
func (s *Sidecar) publishReady() error {
	m := s.mapping
	if m == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "reader table mapping is unavailable"}
	}
	page, err := m.Page(0)
	if err != nil {
		return err
	}
	if err := writeHeaderMapping(page, s.header, stateReady); err != nil {
		return err
	}
	if err := m.FlushPage(0); err != nil {
		return err
	}
	fault.Crash("create.after_ready_write")
	if err := s.file.Sync(); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "reader table sync: " + err.Error()}
	}
	return nil
}

// open opens the canonical sidecar bound to the database identity and
// requires the ready state (Rust Sidecar::open).
func open(main string, databaseID [16]byte) (*Sidecar, error) {
	path, err := canonicalSidecarPath(main)
	if err != nil {
		return nil, err
	}
	sidecar, state, err := openAt(path, databaseID)
	if err != nil {
		return nil, err
	}
	if state != stateReady {
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "reader table is not ready"}
	}
	return sidecar, nil
}

// openAt opens the sidecar at an explicit path and requires the
// database identity (Rust Sidecar::open_at).
func openAt(path string, databaseID [16]byte) (*Sidecar, sidecarState, error) {
	sidecar, state, err := openAny(path)
	if err != nil {
		return nil, 0, err
	}
	if sidecar.header.databaseID != databaseID {
		sidecar.close()
		return nil, 0, &format.Error{Code: format.CodeWrongState, Detail: "reader table belongs to a different database"}
	}
	return sidecar, state, nil
}

// openAny opens any selectable sidecar regardless of state (Rust
// Sidecar::open_any): open the descriptor, prove identity, read and
// verify the header, check the exact length, map the complete extent.
func openAny(path string) (*Sidecar, sidecarState, error) {
	file, identity, err := openRw(path)
	if err != nil {
		return nil, 0, err
	}
	state, h, err := readSourceHeader(file)
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	length, err := sidecarLength(h.capacity)
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	st, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if st.Size() != int64(length) {
		file.Close()
		return nil, 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "reader table length is invalid"}
	}
	if err := requireAvailable(path, identity, cleanupAuthority{
		attemptID:     h.sidecarID,
		ordinal:       1,
		kind:          cleanupKindOwnedCoordination,
		directoryRole: cleanupRoleMainFile,
	}); err != nil {
		file.Close()
		return nil, 0, err
	}
	m, err := mapping.MapFile(file, length, true)
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	return &Sidecar{file: file, path: filepath.Clean(path), header: h, identity: identity, mapping: m}, state, nil
}

// readSourceHeader reads and verifies the header page of an
// unregistered sidecar file through a read-only mapping (Rust
// read_source_header).
func readSourceHeader(file *os.File) (sidecarState, header, error) {
	m, err := mapping.MapFile(file, format.PageSize, false)
	if err != nil {
		return 0, header{}, err
	}
	defer m.Close()
	page, err := m.Page(0)
	if err != nil {
		return 0, header{}, err
	}
	return readHeaderMapping(page)
}

// verifyPath re-checks the retained path identity (Rust
// Sidecar::verify_path).
func (s *Sidecar) verifyPath() error {
	return verifyPath(s.path, s.identity)
}

// localIdentity returns the retained identity (Rust
// Sidecar::local_identity).
func (s *Sidecar) localIdentity() FileIdentity { return s.identity }

// verifyHeader re-checks the mapped header against the retained one and
// requires the ready state (Rust Sidecar::verify_header).
func (s *Sidecar) verifyHeader() error {
	m := s.mapping
	if m == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "reader table mapping is unavailable"}
	}
	page, err := m.Page(0)
	if err != nil {
		return err
	}
	state, h, err := readHeaderMapping(page)
	if err != nil {
		return err
	}
	if state != stateReady || h != s.header {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "reader table header changed"}
	}
	return nil
}

// currentHeader reads the mapped header without comparing (Rust
// Sidecar::current_header).
func (s *Sidecar) currentHeader() (sidecarState, header, error) {
	m := s.mapping
	if m == nil {
		return 0, header{}, &format.Error{Code: format.CodeWrongState, Detail: "reader table mapping is unavailable"}
	}
	page, err := m.Page(0)
	if err != nil {
		return 0, header{}, err
	}
	return readHeaderMapping(page)
}

// lockGate takes the registration/publication gate (spec 15.2).
func (s *Sidecar) lockGate(mode lockMode) error {
	return lock(s.file, gateLockOffset, mode)
}

// lockGateCancellable takes the gate with a cancellation poll loop.
func (s *Sidecar) lockGateCancellable(mode lockMode, check func() error) error {
	return lockCancellable(s.file, gateLockOffset, mode, check)
}

// unlockGate releases the gate.
func (s *Sidecar) unlockGate() error {
	return unlock(s.file, gateLockOffset)
}

// claimWriter takes the single-writer lease or reports WriterBusy
// (Rust Sidecar::claim_writer).
func (s *Sidecar) claimWriter() error {
	acquired, err := tryLock(s.file, writerLockOffset, lockExclusive)
	if err != nil {
		return err
	}
	if !acquired {
		return &format.Error{Code: format.CodeWriterBusy, Detail: "live database writer lease is held"}
	}
	return nil
}

// releaseWriter releases the single-writer lease.
func (s *Sidecar) releaseWriter() error {
	return unlock(s.file, writerLockOffset)
}

// claimReaderCancellable locks one available reader slot and writes the
// selected transaction plus complement (Rust
// Sidecar::claim_reader_inner). Returns the slot index.
func (s *Sidecar) claimReaderCancellable(txn uint64, check func() error) (uint32, error) {
	if txn == 0 {
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "reader transaction cannot be zero"}
	}
	for slot := uint32(0); slot < s.header.capacity; slot++ {
		if err := checkpoint(check); err != nil {
			return 0, err
		}
		offset, err := slotOffset(slot)
		if err != nil {
			return 0, err
		}
		acquired, err := tryLock(s.file, offset, lockExclusive)
		if err != nil {
			return 0, err
		}
		if !acquired {
			continue
		}
		if err := checkpoint(check); err != nil {
			return 0, combineErrors(err, unlock(s.file, offset))
		}
		if err := s.writeSlotOffset(offset, txn); err != nil {
			return 0, combineErrors(err, unlock(s.file, offset))
		}
		return slot, nil
	}
	return 0, &format.Error{Code: format.CodeReaderCapacityExhausted, Detail: "the live reader table has no free slot"}
}

// writeSlotOffset writes one slot under the caller-held slot lock at
// the precomputed offset (Rust slot::write through the retained
// mapping; the claim loop computes the offset once).
func (s *Sidecar) writeSlotOffset(offset uint64, txn uint64) error {
	m := s.mapping
	if m == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "reader table mapping is unavailable"}
	}
	bytes, err := m.View(offset, slotSize)
	if err != nil {
		return err
	}
	return writeSlot(bytes, txn)
}

// clearReader zeroes the slot bytes (Rust Sidecar::clear_reader); the
// caller holds the slot lock and the gate shared.
func (s *Sidecar) clearReader(slot uint32) error {
	offset, err := slotOffsetChecked(slot, s.header.capacity)
	if err != nil {
		return err
	}
	m := s.mapping
	if m == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "reader table mapping is unavailable"}
	}
	bytes, err := m.View(offset, slotSize)
	if err != nil {
		return err
	}
	clear(bytes)
	return nil
}

// unlockReader releases the slot ownership lock (Rust
// Sidecar::unlock_reader).
func (s *Sidecar) unlockReader(slot uint32) error {
	offset, err := slotOffsetChecked(slot, s.header.capacity)
	if err != nil {
		return err
	}
	return unlock(s.file, offset)
}

// verifyReader re-checks that the locked slot still carries the exact
// transaction (Rust Sidecar::verify_reader).
func (s *Sidecar) verifyReader(slot uint32, transaction uint64) error {
	offset, err := slotOffsetChecked(slot, s.header.capacity)
	if err != nil {
		return err
	}
	active, ok, err := s.readActiveSlot(offset)
	if err != nil {
		return err
	}
	if !ok || active != transaction {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "owned reader slot changed"}
	}
	return nil
}

// scanReadersCancellable scans every slot, clearing stale bytes of
// unlocked slots and observing the transactions of locked ones (Rust
// Sidecar::scan_readers_inner).
func (s *Sidecar) scanReadersCancellable(check func() error, observe func(uint64) error) error {
	for slot := uint32(0); slot < s.header.capacity; slot++ {
		if err := checkpoint(check); err != nil {
			return err
		}
		txn, ok, err := s.scanSlot(slot)
		if err != nil {
			return err
		}
		if ok {
			if err := observe(txn); err != nil {
				return err
			}
		}
	}
	return nil
}

// scanAtMostCancellable verifies every active slot names a transaction
// no newer than the committed generation, tracking and discarding the
// oldest like Rust scan_at_most_cancellable.
func (s *Sidecar) scanAtMostCancellable(committedTxn uint64, check func() error) error {
	var oldest uint64
	haveOldest := false
	err := s.scanReadersCancellable(check, func(txn uint64) error {
		if txn > committedTxn {
			return &format.Error{Code: format.CodeFormatInvalid, Detail: "reader slot names an uncommitted transaction"}
		}
		if !haveOldest || txn < oldest {
			oldest = txn
			haveOldest = true
		}
		return nil
	})
	return err
}

// inspectAtMostCancellable verifies every slot without clearing stale
// bytes (Rust Sidecar::inspect_at_most_inner).
func (s *Sidecar) inspectAtMostCancellable(committedTxn uint64, check func() error) error {
	for slot := uint32(0); slot < s.header.capacity; slot++ {
		if err := checkpoint(check); err != nil {
			return err
		}
		txn, ok, err := s.inspectSlot(slot)
		if err != nil {
			return err
		}
		if ok && txn > committedTxn {
			return &format.Error{Code: format.CodeFormatInvalid, Detail: "reader slot names an uncommitted transaction"}
		}
	}
	return nil
}

// oldestReaderCancellable returns the minimum active transaction no
// newer than the committed generation (Rust
// Sidecar::oldest_reader_cancellable).
func (s *Sidecar) oldestReaderCancellable(committedTxn uint64, check func() error) (uint64, bool, error) {
	var oldest uint64
	haveOldest := false
	err := s.scanReadersCancellable(check, func(txn uint64) error {
		if txn > committedTxn {
			return &format.Error{Code: format.CodeFormatInvalid, Detail: "reader slot names an uncommitted transaction"}
		}
		if !haveOldest || txn < oldest {
			oldest = txn
			haveOldest = true
		}
		return nil
	})
	return oldest, haveOldest, err
}

// scanSlot tries the slot lock: success proves no owner and clears
// stale bytes; contention proves an owner and reads the active record
// (Rust Sidecar::scan_slot). The second result reports an active
// locked slot.
func (s *Sidecar) scanSlot(slot uint32) (uint64, bool, error) {
	offset, err := slotOffset(slot)
	if err != nil {
		return 0, false, err
	}
	acquired, err := tryLock(s.file, offset, lockExclusive)
	if err != nil {
		return 0, false, err
	}
	if acquired {
		cleared := s.clearStale(offset)
		unlockErr := unlock(s.file, offset)
		if cleared != nil {
			return 0, false, combineErrors(cleared, unlockErr)
		}
		if unlockErr != nil {
			return 0, false, unlockErr
		}
		return 0, false, nil
	}
	return s.readActiveSlot(offset)
}

// inspectSlot probes the slot lock without clearing (Rust
// Sidecar::inspect_slot). The second result reports an active locked
// slot.
func (s *Sidecar) inspectSlot(slot uint32) (uint64, bool, error) {
	offset, err := slotOffset(slot)
	if err != nil {
		return 0, false, err
	}
	acquired, err := tryLock(s.file, offset, lockExclusive)
	if err != nil {
		return 0, false, err
	}
	if acquired {
		if err := unlock(s.file, offset); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	return s.readActiveSlot(offset)
}

// readActiveSlot decodes a locked active slot (Rust
// Sidecar::read_active_slot). The second result reports whether the
// slot carried an active transaction.
func (s *Sidecar) readActiveSlot(offset uint64) (uint64, bool, error) {
	m := s.mapping
	if m == nil {
		return 0, false, &format.Error{Code: format.CodeWrongState, Detail: "reader table mapping is unavailable"}
	}
	bytes, err := m.View(offset, slotSize)
	if err != nil {
		return 0, false, err
	}
	txn, err := activeTransaction(bytes)
	if err != nil {
		return 0, false, err
	}
	return txn, true, nil
}

// clearStale zeroes a slot that no lock protects (Rust
// Sidecar::clear_stale).
func (s *Sidecar) clearStale(offset uint64) error {
	m := s.mapping
	if m == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "reader table mapping is unavailable"}
	}
	bytes, err := m.View(offset, slotSize)
	if err != nil {
		return err
	}
	if !slotIsClear(bytes) {
		clear(bytes)
	}
	return nil
}

// close unmaps the sidecar and closes the descriptor. It never unlocks
// byte ranges (spec 15.6: automatic destructors perform no file or
// namespace I/O); the caller releases the locks it holds explicitly.
// close is exclusive: no other method may run concurrently.
func (s *Sidecar) close() {
	if s.mapping != nil {
		_ = s.mapping.Close()
		s.mapping = nil
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

func slotOffset(slot uint32) (uint64, error) {
	// 16-byte slots with a uint32 index cannot overflow uint64; the
	// compare form mirrors Rust's checked_mul (which LLVM proves dead)
	// without a per-slot division.
	if uint64(slot) > uint64(^uint64(0))/slotSize {
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "reader slot offset overflows"}
	}
	return uint64(slot)*uint64(slotSize) + uint64(format.PageSize), nil
}

func slotOffsetChecked(slot, capacity uint32) (uint64, error) {
	if slot >= capacity {
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "reader slot index is invalid"}
	}
	return slotOffset(slot)
}
