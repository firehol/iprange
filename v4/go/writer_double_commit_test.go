package iprangedb

// Regression test for the two-commits-on-one-writer path: the second
// Commit on the same open writer must succeed with the next transaction
// id. This pins the Go meta-struct parity rule that the in-memory Meta
// never carries the stored page checksum (Rust MetaV4 has no checksum
// field at all): a struct field holding the stale pre-encode checksum
// made the second commit's RequireUnchangedBase see a changed
// generation.

import (
	"path/filepath"
	"testing"
)

func TestWriterSecondCommitSameWriter(t *testing.T) {
	requireFileCreation(t)
	path := filepath.Join(t.TempDir(), "double.iprdb")
	if _, err := Create(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTagLastSeen()); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	commit := func(step string, lo uint32, value uint32) {
		t.Helper()
		tx, err := w.BeginDirect()
		if err != nil {
			t.Fatalf("%s BeginDirect: %v", step, err)
		}
		if _, err := tx.AssignV4(IPv4(lo), IPv4(lo), value); err != nil {
			t.Fatalf("%s AssignV4: %v", step, err)
		}
		res, err := tx.Commit()
		if err != nil {
			t.Fatalf("%s Commit: %v", step, err)
		}
		if res.Status != CommitCommitted {
			t.Fatalf("%s commit = %+v, want committed", step, res)
		}
	}
	commit("first", 1, 5)
	commit("second", 2, 6)

	info, err := w.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.TransactionID != 3 {
		t.Fatalf("transaction id after two commits = %d, want 3", info.TransactionID)
	}
}
