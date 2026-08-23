//go:build linux || darwin

// Functional tests for the live reader open, the gate-serialized
// registration, the pinned-generation reads, and the close state machine
// (Rust tests/live_roundtrip.rs reader cases through the internal live
// reader).

package live

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// openTestLiveReader opens and registers a live reader with no
// cancellation.
func openTestLiveReader(t *testing.T, main string) *LiveReader {
	t.Helper()
	r, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatalf("OpenLiveReader: %v", err)
	}
	return r
}

// closeTestLiveReader closes a live reader and requires the clean closed
// outcome.
func closeTestLiveReader(t *testing.T, r *LiveReader) {
	t.Helper()
	result, err := r.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !result.Closed {
		t.Fatalf("close = %+v, want closed (cause %v)", result, result.Cause)
	}
	if result.CoordinationCleanup != CoordinationCleanupNone {
		t.Fatalf("coordination = %v, want none", result.CoordinationCleanup)
	}
}

// inspectTestSidecar opens the canonical sidecar of main and returns it
// for slot-state assertions (a separate descriptor, exactly like a real
// peer).
func inspectTestSidecar(t *testing.T, main string, databaseID [16]byte) *Sidecar {
	t.Helper()
	s, err := open(main, databaseID)
	if err != nil {
		t.Fatalf("sidecar open: %v", err)
	}
	return s
}

func TestLiveReaderOpenCloseRoundTrip(t *testing.T) {
	main := createLiveV4Pair(t, 2)
	r := openTestLiveReader(t, main)
	core, err := r.Core()
	if err != nil {
		t.Fatalf("Core: %v", err)
	}
	if got := core.Meta().TxnID; got != 1 {
		t.Fatalf("txn = %d, want 1", got)
	}

	// The registered slot is held while the reader is open (probed from
	// a separate sidecar descriptor; the slot lock reports an owner).
	sidecar := inspectTestSidecar(t, main, core.Meta().DatabaseID)
	defer sidecar.Close()
	if _, ok, err := sidecar.inspectSlot(0); err != nil || !ok {
		t.Fatalf("slot 0 active = %v err=%v, want an owned slot", ok, err)
	}

	closeTestLiveReader(t, r)

	// The slot is released after close: a fresh reader can claim it.
	r2 := openTestLiveReader(t, main)
	closeTestLiveReader(t, r2)

	// A second close on the closed reader is the idempotent closed
	// result (Rust closed-state parity).
	result, err := r.Close()
	if err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if !result.Closed || result.CoordinationCleanup != CoordinationCleanupNone {
		t.Fatalf("second close = %+v, want idempotent closed", result)
	}
}

func TestLiveReaderGenerationsAtomicAndPinned(t *testing.T) {
	// Rust live_roundtrip.rs live_generations_are_atomic_and_old_readers_stay_pinned:
	// an old reader stays pinned to its generation, a new reader sees
	// the committed one, capacity exhaustion refuses a third, and slot
	// reuse works after close.
	main := createLiveV4Pair(t, 2)
	old := openTestLiveReader(t, main)
	oldCore, err := old.Core()
	if err != nil {
		t.Fatalf("Core: %v", err)
	}
	if got := oldCore.Meta().TxnID; got != 1 {
		t.Fatalf("old txn = %d, want 1", got)
	}

	w := openTestLiveWriter(t, main)
	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if changed, err := w.AssignV4(10, 30, 1); err != nil || !changed {
		t.Fatalf("assign 10-30: changed=%v err=%v", changed, err)
	}
	if changed, err := w.AssignV4(20, 25, 2); err != nil || !changed {
		t.Fatalf("assign 20-25: changed=%v err=%v", changed, err)
	}
	if changed, err := w.AssignV4(22, 23, 3); err != nil || !changed {
		t.Fatalf("assign 22-23: changed=%v err=%v", changed, err)
	}
	result, err := w.Commit(nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Durability != CommitCommitted {
		t.Fatalf("durability = %v, want committed", result.Durability)
	}

	// The old reader is pinned to generation 1: the new ranges are
	// invisible to it.
	if value, found, err := oldCore.LookupDirect4(22); err != nil || found {
		t.Fatalf("old reader at 22: value=%d found=%v err=%v, want absent", value, found, err)
	}

	// A new reader sees generation 2 and the committed ranges.
	current := openTestLiveReader(t, main)
	currentCore, err := current.Core()
	if err != nil {
		t.Fatalf("Core: %v", err)
	}
	if got := currentCore.Meta().TxnID; got != 2 {
		t.Fatalf("current txn = %d, want 2", got)
	}
	for ip, want := range map[uint32]uint32{19: 1, 21: 2, 22: 3} {
		value, found, err := currentCore.LookupDirect4(ip)
		if err != nil || !found || value != want {
			t.Fatalf("current at %d: value=%d found=%v err=%v, want %d", ip, value, found, err, want)
		}
	}

	// Both slots are claimed: a third reader is refused with
	// ReaderCapacityExhausted (Rust Error::ReaderCapacityExhausted).
	if _, err := OpenLiveReader(main, nil); err == nil {
		t.Fatal("third reader open succeeded on a full reader table")
	} else {
		expectCode(t, err, format.CodeReaderCapacityExhausted)
	}

	// Closing the old reader frees a slot that a new reader reuses.
	closeTestLiveReader(t, old)
	reused := openTestLiveReader(t, main)
	reusedCore, err := reused.Core()
	if err != nil {
		t.Fatalf("Core: %v", err)
	}
	if got := reusedCore.Meta().TxnID; got != 2 {
		t.Fatalf("reused txn = %d, want 2", got)
	}
	closeTestLiveReader(t, reused)
	closeTestLiveReader(t, current)
	if _, err := w.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}
}

func TestLiveReaderCloseRetryAfterMainReplacement(t *testing.T) {
	// A close whose registration proof fails (the main path no longer
	// names the opened inode) reports the incomplete close with the
	// retained close authority; after the pair is restored the retried
	// close completes (Rust release_gate_after_failure + CloseOnly
	// retry).
	main := createLiveV4Pair(t, 2)
	r := openTestLiveReader(t, main)
	replaced := main + ".replaced"
	if err := os.Rename(main, replaced); err != nil {
		t.Fatalf("rename away: %v", err)
	}
	result, err := r.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if result.Closed {
		t.Fatal("close succeeded while the main path was replaced")
	}
	if result.CoordinationCleanup != CoordinationCleanupRetainedReaderCloseRequired {
		t.Fatalf("coordination = %v, want retained reader close", result.CoordinationCleanup)
	}
	if result.Cause == nil {
		t.Fatal("incomplete close without a cause")
	}

	// The reader refuses operations while close-only.
	if _, err := r.Core(); err == nil {
		t.Fatal("Core succeeded on a close-only reader")
	} else {
		expectCode(t, err, format.CodeWrongState)
	}

	if err := os.Rename(replaced, main); err != nil {
		t.Fatalf("rename back: %v", err)
	}
	closeTestLiveReader(t, r)
}

func TestLiveReaderOpenCancellationLeavesNoResidue(t *testing.T) {
	// A cancellation between the mapping open and the slot claim must
	// leave no reader slot and no lifetime residue: the gate is
	// released, the mapping is closed, and a clean open follows.
	main := createLiveV4Pair(t, 2)
	if _, err := OpenLiveReader(main, cancelledCheck); err == nil {
		t.Fatal("open with a pre-cancelled check succeeded")
	} else {
		expectCode(t, err, format.CodeCancelled)
	}
	// No slot residue and no lock residue: a clean reader open and
	// writer open both succeed.
	r := openTestLiveReader(t, main)
	closeTestLiveReader(t, r)
	w := openTestLiveWriter(t, main)
	if _, err := w.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}
}

func TestLiveReaderOpenRefusesMissingSidecar(t *testing.T) {
	// A live database whose .readers sidecar was removed must refuse
	// the live reader open with the sidecar absence class (Rust
	// Sidecar::open -> NameNotFound through the canonical path).
	main := createLiveV4Pair(t, 1)
	if err := os.Remove(main + ".readers"); err != nil {
		t.Fatalf("remove sidecar: %v", err)
	}
	if _, err := OpenLiveReader(main, nil); err == nil {
		t.Fatal("open succeeded without the reader table")
	} else {
		expectCode(t, err, format.CodeNameNotFound)
	}
}

func TestLiveReaderOpenRefusesDifferentDatabase(t *testing.T) {
	// A main file whose canonical sidecar belongs to a different
	// database must refuse with the WrongState class (Rust Sidecar::open
	// database-id proof). The other pair's main bytes are moved onto
	// this pair's path; the identity checks pass (the bytes are the
	// opened inode) and the sidecar header proof fires.
	main := createLiveV4Pair(t, 1)
	other := createLiveV4Pair(t, 1)
	if err := os.Remove(main); err != nil {
		t.Fatalf("remove main: %v", err)
	}
	raw, err := os.ReadFile(other)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := os.WriteFile(main, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := OpenLiveReader(main, nil); err == nil {
		t.Fatal("open succeeded across database identities")
	} else {
		expectCode(t, err, format.CodeWrongState)
	}
}

func TestLiveReaderForkedHandleStructural(t *testing.T) {
	// Go cannot fork, so the owner check is structural parity with Rust
	// ProcessIdentity (live_reader.rs require_owner): the PID is sampled
	// at package init and compared per operation. Overriding the sampled
	// PID drives the ForkedHandle class exactly like a cross-process
	// handle in Rust.
	main := createLiveV4Pair(t, 1)
	r := openTestLiveReader(t, main)
	saved := currentPID
	currentPID = saved + 1
	defer func() { currentPID = saved }()

	if _, err := r.Core(); err == nil {
		t.Fatal("Core succeeded on a foreign-process handle")
	} else {
		expectCode(t, err, format.CodeForkedHandle)
	}
	if _, err := r.Close(); err == nil {
		t.Fatal("Close succeeded on a foreign-process handle")
	} else {
		expectCode(t, err, format.CodeForkedHandle)
	}

	currentPID = saved
	closeTestLiveReader(t, r)
}
