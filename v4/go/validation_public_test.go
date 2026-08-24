package iprangedb

// Public validation facade tests (Rust tests/validation.rs through the
// SDK facade): the cancellation fold and the live-current clean sweep
// over the public writer surface. The live arm runs only on the
// proven live platforms, like the live writer suite.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// formatPageSizePublic is the v4 page size (binary-format-v4.md
// section 4) used by the lifecycle assertions.
const formatPageSizePublic = format.PageSize

func TestPublicValidateCancellation(t *testing.T) {
	token := NewCancellationToken()
	token.Cancel()
	_, failure := Validate("", ValidationModeImmutableCurrent, HeapOnly(1<<20, 1), token, nil)
	if failure == nil {
		t.Fatal("cancelled validation succeeded")
	}
	var e *Error
	if !errors.As(failure.Cause, &e) || e.Code != ErrorCancelled {
		t.Fatalf("cancelled cause %v", failure.Cause)
	}
	if failure.CleanupState() != CleanupStateClean {
		t.Fatalf("preflight failure reported residue: %+v", failure)
	}
}

// TestPublicValidateLiveCleanSweep mirrors Rust
// live_current_validation_pins_and_releases_its_reader_slot through
// the public surface: one committed direct generation validates
// cleanly, and the released reader slot is claimable again.
func TestPublicValidateLiveCleanSweep(t *testing.T) {
	main := filepath.Join(t.TempDir(), "db.iprdb")
	created, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTag{}, 1, nil)
	if err != nil {
		t.Fatalf("CreateLive: %v", err)
	}
	if created.State != CreationStateCreated {
		t.Fatalf("creation state %v", created.State)
	}
	writer, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatalf("OpenLiveWriter: %v", err)
	}
	transaction, err := writer.BeginDirect()
	if err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if changed, err := transaction.AssignV4(IPv4(100), IPv4(200), 9); err != nil || !changed {
		t.Fatalf("AssignV4: changed=%v err=%v", changed, err)
	}
	commit, err := transaction.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if commit.Status != CommitCommitted {
		t.Fatalf("commit not durable: %+v (cause %v)", commit, commit.Cause)
	}

	var findings []ValidationFinding
	result, failure := Validate(main, ValidationModeLiveCurrent, HeapOnly(1<<20, 2), nil, SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		findings = append(findings, *f)
		return SinkContinue, nil
	}))
	if failure != nil {
		t.Fatalf("live validation failed: %v", failure.Cause)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if !result.Valid {
		t.Fatal("committed live generation reported invalid")
	}
	if len(findings) != 0 {
		t.Fatalf("findings %d, want none", len(findings))
	}
	if result.Generation == nil || result.Generation.TransactionID != 2 {
		t.Fatalf("generation %+v, want txn 2", result.Generation)
	}

	reader, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatalf("OpenLiveReader after validation: %v", err)
	}
	value, ok, err := reader.LookupDirectV4(IPv4(150))
	if err != nil {
		t.Fatalf("LookupDirect4: %v", err)
	}
	if !ok || value != 9 {
		t.Fatalf("LookupDirectV4(150) = %v,%v, want 9,true", value, ok)
	}
	if _, err := reader.Close(); err != nil {
		t.Fatalf("reader close: %v", err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}
}

// TestPublicValidateLiveAfterRetainedCapacity mirrors Rust
// mapped_reader_retains_abort_capacity_for_reuse_then_allows_shrink
// (commit_lifecycle.rs): while one live reader stays pinned the
// writer retains the grown main extent across commits; once the pin
// is released the next writer open trims back to the committed
// length; the LiveCurrent validation in between proves exactly the
// committed generation (page_count * page size equals the current
// reader's committed length) and leaves no findings.
func TestPublicValidateLiveAfterRetainedCapacity(t *testing.T) {
	type pair struct {
		main string
	}
	main := filepath.Join(t.TempDir(), "db.iprdb")
	if _, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTag{}, 2, nil); err != nil {
		t.Fatalf("CreateLive: %v", err)
	}
	pinned, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatalf("pinned reader: %v", err)
	}

	writer, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatalf("OpenLiveWriter: %v", err)
	}
	transaction, err := writer.BeginDirect()
	if err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if _, err := transaction.AssignV4(IPv4(10), IPv4(20), 7); err != nil {
		t.Fatalf("AssignV4: %v", err)
	}
	if result, err := transaction.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("Commit: %v (result %+v)", err, result)
	}
	transaction, err = writer.BeginDirect()
	if err != nil {
		t.Fatalf("BeginDirect 2: %v", err)
	}
	if _, err := transaction.AssignV4(IPv4(30), IPv4(40), 9); err != nil {
		t.Fatalf("AssignV4 2: %v", err)
	}
	if result, err := transaction.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("Commit 2: %v (result %+v)", err, result)
	}

	current, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatalf("current reader: %v", err)
	}
	info, err := current.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.TransactionID != 3 {
		t.Fatalf("txn = %d, want 3", info.TransactionID)
	}
	committedLength := info.PageCount * formatPageSizePublic
	if _, err := current.Close(); err != nil {
		t.Fatalf("current close: %v", err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}

	var findings []ValidationFinding
	result, failure := Validate(main, ValidationModeLiveCurrent, HeapOnly(1<<20, 2), nil, SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		findings = append(findings, *f)
		return SinkContinue, nil
	}))
	if failure != nil {
		t.Fatalf("live validation failed: %v", failure.Cause)
	}
	if !result.Valid || len(findings) != 0 {
		t.Fatalf("live validation valid=%v findings=%d", result.Valid, len(findings))
	}
	if result.Generation == nil || result.Generation.PageCount*formatPageSizePublic != committedLength {
		t.Fatalf("validated generation %+v does not match committed length %d", result.Generation, committedLength)
	}

	// The pin is released: the next writer open trims the retained
	// extent back to the committed length (Rust asserts the same
	// fs::metadata length right after LiveWriter::open).
	if _, err := pinned.Close(); err != nil {
		t.Fatalf("pinned close: %v", err)
	}
	writer, err = OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatalf("reopen writer: %v", err)
	}
	if size, err := os.Stat(main); err != nil {
		t.Fatal(err)
	} else if uint64(size.Size()) != committedLength {
		t.Fatalf("reopen length %d, want the committed length %d", size.Size(), committedLength)
	}
	transaction, err = writer.BeginDirect()
	if err != nil {
		t.Fatalf("BeginDirect 3: %v", err)
	}
	if _, err := transaction.AssignV4(IPv4(50), IPv4(60), 11); err != nil {
		t.Fatalf("AssignV4 3: %v", err)
	}
	if result, err := transaction.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("Commit 3: %v (result %+v)", err, result)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatalf("writer close 2: %v", err)
	}

	finalReader, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatalf("final reader: %v", err)
	}
	finalInfo, err := finalReader.Info()
	if err != nil {
		t.Fatalf("final Info: %v", err)
	}
	// The retained extent is trimmed to exactly the final committed
	// page count (Rust asserts physical length == page_count * 4096).
	if size, err := os.Stat(main); err != nil {
		t.Fatal(err)
	} else if uint64(size.Size()) != finalInfo.PageCount*formatPageSizePublic {
		t.Fatalf("final physical length %d, want final page count %d * page size", size.Size(), finalInfo.PageCount)
	}
	for _, want := range []struct {
		addr IPv4
		val  uint32
		ok   bool
	}{
		{IPv4(15), 7, true},
		{IPv4(35), 9, true},
		{IPv4(55), 11, true},
	} {
		value, ok, err := finalReader.LookupDirectV4(want.addr)
		if err != nil {
			t.Fatalf("LookupDirectV4(%d): %v", want.addr, err)
		}
		if ok != want.ok || value != want.val {
			t.Fatalf("LookupDirectV4(%d) = %v,%v, want %v,%v", want.addr, value, ok, want.val, want.ok)
		}
	}
	if _, err := finalReader.Close(); err != nil {
		t.Fatalf("final reader close: %v", err)
	}
}
