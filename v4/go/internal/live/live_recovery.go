package live

// Recovery-candidate inspection gate (Rust recovery/inspection.rs
// inspect_live over live_sidecar::Sidecar): the ready reader table of
// the classified database identity is opened and the registration gate
// is held exclusive through the inspection, exactly like the Rust
// inspect_live_locked sequence. The recovery package composes this
// owner; it never touches sidecar internals.

// LiveRecoveryGate is one gate-held reader-table registration for the
// recovery candidate inspection of a live database (Rust
// inspect_live_locked's Sidecar::open + lock_gate_cancellable).
type LiveRecoveryGate struct {
	sidecar    *Sidecar
	gateLocked bool
}

// OpenLiveRecoveryGate opens the ready reader table of the database
// identity and takes the exclusive registration gate (Rust
// Sidecar::open, which refuses a missing, unreleased, or foreign
// reader table, followed by lock_gate_cancellable).
func OpenLiveRecoveryGate(path string, databaseID [16]byte, check func() error) (*LiveRecoveryGate, error) {
	sidecar, err := open(path, databaseID)
	if err != nil {
		return nil, err
	}
	gate := &LiveRecoveryGate{sidecar: sidecar}
	if err := sidecar.lockGateCancellable(LockExclusive, check); err != nil {
		sidecar.Close()
		return nil, err
	}
	gate.gateLocked = true
	return gate, nil
}

// DatabaseID reports the retained reader-table database identity (Rust
// sidecar.header.database_id).
func (g *LiveRecoveryGate) DatabaseID() [16]byte { return g.sidecar.header.databaseID }

// Verify re-proves the sidecar path and header under the held gate
// (Rust verify_live's sidecar arms).
func (g *LiveRecoveryGate) Verify() error {
	if err := g.sidecar.verifyPath(); err != nil {
		return err
	}
	return g.sidecar.verifyHeader()
}

// InspectAtMost verifies every active reader slot names a transaction
// no newer than committedTxn without clearing stale bytes (Rust
// Sidecar::inspect_at_most_cancellable).
func (g *LiveRecoveryGate) InspectAtMost(committedTxn uint64, check func() error) error {
	return g.sidecar.inspectAtMostCancellable(committedTxn, check)
}

// Release unlocks the gate and closes the reader table, folding the
// release error under the operation result exactly like the Rust
// release_live_gate arms (primary first).
func (g *LiveRecoveryGate) Release(primary error) error {
	unlockErr := g.unlock()
	g.sidecar.Close()
	if primary != nil {
		return primary
	}
	return unlockErr
}

func (g *LiveRecoveryGate) unlock() error {
	if !g.gateLocked {
		return nil
	}
	g.gateLocked = false
	return g.sidecar.unlockGate()
}
