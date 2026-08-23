// Registered-reader ownership for one selected live generation (Rust
// reader_core/live.rs LiveReaderCore): the main mapping under the shared
// lifetime lock, one claimed sidecar reader slot, and the exact close
// state machine. The public OpenLiveReader facade composes this owner;
// it never touches namespace internals.

package live

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// liveReaderState is one physical step of the reader close machine (Rust
// reader_core/live.rs State): the reader moves one state per successful
// close step, and every failure retains the exact state so a retried
// Close continues from the failed step.
type liveReaderState uint8

const (
	liveReaderOpen liveReaderState = iota
	liveReaderCloseOnly
	liveReaderGateHeldSlotActive
	liveReaderGateHeldSlotClearing
	liveReaderGateHeldSlotCleared
	liveReaderGateHeldSlotReleased
	liveReaderMainLockOnly
	liveReaderClosed
)

// LiveReaderClose is the factual, retryable close result of one live
// reader close attempt (Rust LiveReaderClose): whether the registration
// was fully cleared, which coordination residue the caller must still
// release, and the failure cause when the close is incomplete.
type LiveReaderClose struct {
	Closed              bool
	CoordinationCleanup CoordinationCleanup
	Cause               error
}

// LiveReader is one registered reader against one committed generation of
// a live database (Rust LiveReaderCore). It holds the main mapping under
// the shared lifetime lock, the opened reader table, and the claimed
// slot. A LiveReader is owned by one goroutine: methods must not run
// concurrently with each other or with Close, exactly like the writer
// and the mapping owner.
type LiveReader struct {
	core         *reader.ImmutableReader
	mainPath     string
	mainIdentity FileIdentity
	sidecar      *Sidecar
	slot         uint32
	state        liveReaderState
	ownerPID     int
}

// OpenLiveReader opens and registers one live reader (Rust
// LiveReaderCore::open): the main file is mapped read-only under the
// shared lifetime lock, the path identity is proven, the committed
// generation is bootstrapped for the database identity, the ready reader
// table of that identity is opened, the gate is held exclusive while the
// pair is proven against a freshly sampled file extent, the generation is
// re-selected, the reader table is scanned against it, the committed
// extent is remapped, one reader slot is claimed, and the pair is proven
// again. The gate is released before the reader returns; the slot is held
// until Close. check, when non-nil, runs between every bounded step.
func OpenLiveReader(path string, check func() error) (*LiveReader, error) {
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	// The shared lifetime lock is taken inside the mapping open, and
	// requireLiveCoordination refuses before path access on platforms
	// without proven live coordination (Rust require_live_supported;
	// the read-only open does not take the rdwr gate in openMapping).
	m, err := mapping.OpenLiveReader(path, nil)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*LiveReader, error) {
		m.Close()
		return nil, err
	}
	device, inode, err := m.FileIdentity()
	if err != nil {
		return fail(err)
	}
	mainIdentity := FileIdentity{device: device, inode: inode}
	if err := verifyPath(path, mainIdentity); err != nil {
		return fail(err)
	}
	// map_reader: bootstrap the committed generation over a freshly
	// sampled extent and remap to it, giving the database identity
	// needed before the sidecar is opened (Rust
	// database_file::map_reader with OpenMode::LiveReader re-stats the
	// descriptor at the bootstrap moment).
	core, err := reader.OpenLiveMapped(m)
	if err != nil {
		return fail(err)
	}
	// require_main_available is a pure POSIX no-op in Rust
	// (live_cleanup.rs require_available, non-windows arm), so Go omits
	// it, exactly like the live writer open.
	sidecar, err := open(path, core.Meta().DatabaseID)
	if err != nil {
		return fail(err)
	}
	fail = func(err error) (*LiveReader, error) {
		sidecar.Close()
		m.Close()
		return nil, err
	}
	if err := sidecar.lockGateCancellable(LockExclusive, check); err != nil {
		return fail(err)
	}
	unlockGate := func(err error) (*LiveReader, error) {
		return fail(combineErrors(err, sidecar.unlockGate()))
	}
	// register (Rust reader_core/live.rs register): select the
	// registered generation under the gate with a freshly sampled
	// physical extent, prove it belongs to this reader table, scan the
	// slots against it, remap to the selected committed bytes, claim one
	// slot, and prove the pair again. Every failure releases the gate
	// and the mapping.
	if err := verifyPath(path, mainIdentity); err != nil {
		return unlockGate(err)
	}
	if err := sidecar.verifyPath(); err != nil {
		return unlockGate(err)
	}
	physical, err := m.FileSize()
	if err != nil {
		return unlockGate(err)
	}
	if err := core.SelectRegisteredGeneration(physical); err != nil {
		return unlockGate(err)
	}
	if core.Meta().DatabaseID != sidecar.header.databaseID {
		return unlockGate(&format.Error{Code: format.CodeWrongState, Detail: "reader table belongs to a different database"})
	}
	if err := sidecar.scanAtMostCancellable(core.Meta().TxnID, check); err != nil {
		return unlockGate(err)
	}
	if err := checkpoint(check); err != nil {
		return unlockGate(err)
	}
	if err := m.Remap(core.Meta().PageCount * format.PageSize); err != nil {
		return unlockGate(err)
	}
	slot, err := sidecar.claimReaderCancellable(core.Meta().TxnID, check)
	if err != nil {
		return unlockGate(err)
	}
	if err := checkpoint(check); err != nil {
		return unlockGate(err)
	}
	if err := verifyPath(path, mainIdentity); err != nil {
		return unlockGate(err)
	}
	if err := sidecar.verifyPath(); err != nil {
		return unlockGate(err)
	}
	if err := sidecar.unlockGate(); err != nil {
		return fail(err)
	}
	return &LiveReader{
		core:         core,
		mainPath:     path,
		mainIdentity: mainIdentity,
		sidecar:      sidecar,
		slot:         slot,
		state:        liveReaderOpen,
		ownerPID:     currentPID,
	}, nil
}

// Core returns the logical reader core after the owner and open-state
// checks (Rust LiveReaderCore::reader -> require_open); every public
// lookup runs through it so a closing or closed reader reports WrongState
// before any page is touched.
func (r *LiveReader) Core() (*reader.ImmutableReader, error) {
	if err := r.requireOpen(); err != nil {
		return nil, err
	}
	return r.core, nil
}

// CoreNoCheck returns the internal reader core without owner or state
// checks. The public pin paths run Core (requireOpen) once through the
// require* pre-checks and then touch the core through this accessor, so
// every public operation performs exactly one open-state check (Rust
// LiveReader::core -> reader() -> require_open); the immutable facade has
// the same shape with its plain inner field.
func (r *LiveReader) CoreNoCheck() *reader.ImmutableReader { return r.core }

// Identity returns the retained main-file identity of the registration
// (the identity captured from the opened descriptor at open; Rust
// live_namespace::identity over the opened file).
func (r *LiveReader) Identity() FileIdentity { return r.mainIdentity }

// Close clears the registration in the Rust order (reader_core/live.rs
// LiveReaderCore::close): the gate is taken shared, the registration is
// proven against the still-current pair, the mapping is unmapped, the
// slot is cleared and unlocked, the gate is released, and finally the
// shared lifetime lock is released. A fully closed reader reports the
// idempotent closed result; an incomplete close is retryable and keeps
// the reader usable for another Close. A non-owner process (structural
// ForkedHandle parity; Go cannot fork) reports an error instead of a
// close result.
func (r *LiveReader) Close() (LiveReaderClose, error) {
	if err := r.requireOwner(); err != nil {
		return LiveReaderClose{}, err
	}
	if r.state == liveReaderClosed {
		return readerClosed(), nil
	}
	if r.state == liveReaderOpen || r.state == liveReaderCloseOnly {
		if err := r.sidecar.lockGate(LockShared); err != nil {
			r.state = liveReaderCloseOnly
			return readerCloseIncomplete(err), nil
		}
		r.state = liveReaderGateHeldSlotActive
	}
	if r.state == liveReaderGateHeldSlotActive {
		if err := r.verifyRegistration(); err != nil {
			return r.releaseGateAfterFailure(err), nil
		}
		if err := r.core.Unmap(); err != nil {
			return readerCloseIncomplete(err), nil
		}
		r.state = liveReaderGateHeldSlotClearing
	}
	if r.state == liveReaderGateHeldSlotClearing {
		if err := r.sidecar.clearReader(r.slot); err != nil {
			return readerCloseIncomplete(err), nil
		}
		r.state = liveReaderGateHeldSlotCleared
	}
	if r.state == liveReaderGateHeldSlotCleared {
		if err := r.sidecar.unlockReader(r.slot); err != nil {
			return readerCloseIncomplete(err), nil
		}
		r.state = liveReaderGateHeldSlotReleased
	}
	if r.state == liveReaderGateHeldSlotReleased {
		if err := r.sidecar.unlockGate(); err != nil {
			return readerCloseIncomplete(err), nil
		}
		r.state = liveReaderMainLockOnly
	}
	if r.state == liveReaderMainLockOnly {
		// The mapping is already unmapped; Close releases the shared
		// lifetime lock and the descriptor (Rust unlock_file + drop).
		if err := r.core.Close(); err != nil {
			return readerCloseIncomplete(err), nil
		}
		r.state = liveReaderClosed
		// The terminal transition also releases the sidecar mapping and
		// descriptor, mirroring the Rust drop of the Sidecar field after
		// close completes (the live writer closes its sidecar at the same
		// terminal step). Retryable failure paths keep the sidecar open
		// because the close machine may still need the reader table.
		r.sidecar.Close()
	}
	return readerClosed(), nil
}

// verifyRegistration proves the registration is still current before the
// slot is cleared (Rust LiveReaderCore::verify_registration): the main
// path still names the opened inode, the sidecar path and header are
// intact, and the slot still names this reader's transaction.
func (r *LiveReader) verifyRegistration() error {
	if err := verifyPath(r.mainPath, r.mainIdentity); err != nil {
		return err
	}
	if err := r.sidecar.verifyPath(); err != nil {
		return err
	}
	if err := r.sidecar.verifyHeader(); err != nil {
		return err
	}
	return r.sidecar.verifyReader(r.slot, r.core.Meta().TxnID)
}

// releaseGateAfterFailure runs the close unwind when the registration
// proof failed under the shared gate (Rust
// LiveReaderCore::release_gate_after_failure): the gate is released and
// the reader is left close-only; a gate-release failure nests
// CleanupInProgress around the original cause (Rust
// Error::CleanupIncomplete).
func (r *LiveReader) releaseGateAfterFailure(cause error) LiveReaderClose {
	if err := r.sidecar.unlockGate(); err != nil {
		return readerCloseIncomplete(combineErrors(cause, err))
	}
	r.state = liveReaderCloseOnly
	return readerCloseIncomplete(cause)
}

// requireOpen proves the reader is used by the process that opened it and
// is still in the open state (Rust require_owner + require_open).
func (r *LiveReader) requireOpen() error {
	if err := r.requireOwner(); err != nil {
		return err
	}
	if r.state != liveReaderOpen {
		return &format.Error{Code: format.CodeWrongState, Detail: "live reader is closing or closed"}
	}
	return nil
}

// requireOwner proves the reader is used by the process that opened it
// (Rust require_owner, Error::ForkedHandle). Go cannot fork, so the check
// is structural parity and can never fire; currentPID is sampled once at
// package init, so the check costs no syscall per operation.
func (r *LiveReader) requireOwner() error {
	if currentPID != r.ownerPID {
		return &format.Error{Code: format.CodeForkedHandle, Detail: "live reader was opened by a different process"}
	}
	return nil
}

// readerClosed builds the idempotent success close result (Rust
// reader_closed).
func readerClosed() LiveReaderClose {
	return LiveReaderClose{Closed: true, CoordinationCleanup: CoordinationCleanupNone}
}

// readerCloseIncomplete builds the retryable incomplete close result
// (Rust reader_close_incomplete): the caller must retry Close to release
// the retained coordination.
func readerCloseIncomplete(cause error) LiveReaderClose {
	return LiveReaderClose{Closed: false, CoordinationCleanup: CoordinationCleanupRetainedReaderCloseRequired, Cause: cause}
}
