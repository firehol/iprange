// Public direct-workflow surface tests: per-record zero-allocation pins
// for the direct replacement and timestamp refresh input paths, the
// first-seen removal sink (Rust direct_workflows.rs removals parity),
// and the enrichment cursor lifetime and seek contract.

package iprangedb

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

// directRanges1000 builds 1000 strictly ascending single-address IPv4
// ranges starting at 0 (the ordered-prefix input shape).
func directRanges1000() []DirectRangeV4 {
	ranges := make([]DirectRangeV4, 1000)
	for index := 0; index < 1000; index++ {
		ranges[index] = DirectRangeV4{From: uint32(index * 2), To: uint32(index * 2), Value: uint32(index)}
	}
	return ranges
}

// assertZeroAllocWindow reports any allocation that exceeds the bounded
// Go-runtime one-time metadata window. The runtime allocates one-time
// 48-byte and 16-byte cache entries when new type paths are first
// exercised; a measurement window occasionally catches two 48-byte
// entries (96 bytes), so the bound is 160 bytes. A per-record
// regression allocates ~1.15 KB per record, far above the window.
func assertZeroAllocWindow(t *testing.T, before, after runtime.MemStats, context string) {
	t.Helper()
	if delta := after.TotalAlloc - before.TotalAlloc; delta > 160 {
		for i := range after.BySize {
			if n := int(after.BySize[i].Mallocs) - int(before.BySize[i].Mallocs); n > 0 {
				t.Logf("%s size %d: +%d mallocs", context, after.BySize[i].Size, n)
			}
		}
		t.Fatalf("%s allocated %d bytes, want <= 64", context, delta)
	}
}

// TestZeroAllocationDirectWorkflowIngestion pins the direct replacement
// assignment input and the first-seen refresh private constant-range
// input at zero Go heap allocations per record (Rust
// allocate_nothing_per_record parity). The bound edit is created once at
// begin; a per-record binding regresses to one DraftStore allocation per
// record (~1.15 KB), far above the bounded runtime window. The direct
// replacement first batch also grows the leaf-locator hint table once
// per workflow (Rust Vec growth parity), so the replacement batches use
// a documented one-time ceiling and the continuation batches stay
// within the runtime window.
func TestZeroAllocationDirectWorkflowIngestion(t *testing.T) {
	if raceEnabled {
		t.Skip("race shadow memory inflates MemStats; the zero-allocation pin runs without -race")
	}
	replacementPath := directWorkflowDB(t, mustTag(t, "direct"))
	w, err := OpenWriter(replacementPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	replacement, err := w.BeginDirectReplacement(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := replacement.AddRangesV4(directRanges1000()); err != nil {
		t.Fatal(err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	// One-time leaf-locator hint growth for the first 1000 leaves; a
	// per-record regression would charge ~1.15 MB here.
	if delta := after.TotalAlloc - before.TotalAlloc; delta > 64<<10 {
		t.Fatalf("direct replacement first batch allocated %d bytes, want <= 64 KiB one-time locator growth", delta)
	}
	for run := 0; run < 50; run++ {
		runtime.ReadMemStats(&before)
		if err := replacement.AddRangesV4(directRanges1000()); err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		assertZeroAllocWindow(t, before, after, "direct replacement continuation batch")
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}

	refreshPath := directWorkflowDB(t, ValueTagFirstSeen())
	w, err = OpenWriter(refreshPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	refresh, err := w.BeginFirstSeenRefresh(1, NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 51; run++ {
		ranges := make([]AddressRange4, 1000)
		for index := 0; index < 1000; index++ {
			address := run*2000 + index*2
			ranges[index] = AddressRange4{From: IPv4(address), To: IPv4(address)}
		}
		runtime.ReadMemStats(&before)
		if err := refresh.AddRangesV4(ranges); err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		assertZeroAllocWindow(t, before, after, "first-seen refresh batch")
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestDirectWorkflowCommitAfterAbort pins the draftless commit class
// (Rust commit_attempt parity) on the shared FinishedWorkflow terminal
// used by direct replacement and the timestamp refreshes: the draft was
// discarded by Abort, so Commit and a second Abort report
// ErrorNoPendingTransaction.
func TestDirectWorkflowCommitAfterAbort(t *testing.T) {
	path := directWorkflowDB(t, mustTag(t, "direct"))
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	replacement, err := w.BeginDirectReplacement(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.AddRangesV4([]DirectRangeV4{{From: 1, To: 1, Value: 7}}); err != nil {
		t.Fatal(err)
	}
	finished, err := replacement.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if !finished.IsChanged() {
		t.Fatal("direct replacement with one range reported no change")
	}
	if err := finished.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := finished.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("commit after abort = %v, want no pending transaction", err)
	}
	if err := finished.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after abort = %v, want no pending transaction", err)
	}
}

// TestFirstSeenRefreshRemovalsV4 mirrors the Rust
// direct_workflows.rs first-seen removal sink test: an empty refresh
// input expires the whole map, the sink receives bounded 64-record
// batches, every removal carries the preserved first seen value, and a
// failing sink aborts the workflow with the sink error nested in
// TransactionAborted while the database stays intact.
func TestFirstSeenRefreshRemovalsV4(t *testing.T) {
	path := directWorkflowDB(t, ValueTagFirstSeen())
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	ranges := make([]AddressRange4, 130)
	for index := 0; index < 130; index++ {
		ranges[index] = AddressRange4{From: IPv4(index * 2), To: IPv4(index * 2)}
	}
	seed, err := w.BeginFirstSeenRefresh(77, NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.AddRangesV4(ranges); err != nil {
		t.Fatal(err)
	}
	finished, err := seed.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	finishWorkflowCommit(t, finished, "first-seen seed")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w, err = OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	var batchLengths []int
	var removals []FirstSeenRemoval4
	sink := func(batch []FirstSeenRemoval4) error {
		batchLengths = append(batchLengths, len(batch))
		removals = append(removals, batch...)
		return nil
	}
	// The refresh input is empty: every committed first-seen interval
	// expires and streams through the sink (Rust test shape).
	refresh, err := w.BeginFirstSeenRefresh(88, NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := refresh.FinishInputWithRemovalsV4(sink)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.IsChanged() {
		t.Fatal("refresh with removals reported no change")
	}
	if len(batchLengths) != 3 || batchLengths[0] != 64 || batchLengths[1] != 64 || batchLengths[2] != 2 {
		t.Fatalf("sink batch lengths = %v, want [64 64 2]", batchLengths)
	}
	if len(removals) != 130 {
		t.Fatalf("removal count = %d, want 130", len(removals))
	}
	for index, removal := range removals {
		if removal.From != IPv4(index*2) || removal.To != IPv4(index*2) {
			t.Fatalf("removal %d = %d..%d, want %d..%d", index, removal.From, removal.To, index*2, index*2)
		}
		if removal.FirstSeen != 77 {
			t.Fatalf("removal %d first_seen = %d, want 77", index, removal.FirstSeen)
		}
		if got := removal.Addresses.Lo(); got != 1 {
			t.Fatalf("removal %d addresses = %d, want 1", index, got)
		}
	}
	if got := prepared.Report().RemovedAddresses.Lo(); got != 130 {
		t.Fatalf("report removed_addresses = %d, want 130", got)
	}
	// Rust aborts the prepared expiry; the database keeps the seed.
	prepared.Abort()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// A failing sink aborts the workflow; the database keeps the last
	// committed generation and the writer can run a fresh refresh.
	w, err = OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	failing := func(batch []FirstSeenRemoval4) error {
		return &Error{Code: ErrorInvalidArgument, Detail: "removal sink failed"}
	}
	refresh, err = w.BeginFirstSeenRefresh(99, NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	var ab *abortError
	if _, err := refresh.FinishInputWithRemovalsV4(failing); !errors.As(err, &ab) || causeCode(ab.cause) != ErrorInvalidArgument {
		t.Fatalf("failing sink error = %v, want TransactionAborted wrapping the sink error", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, found, err := reader.LookupDirectV4(IPv4(0)); err != nil {
		t.Fatal(err)
	} else if !found || got != 77 {
		t.Fatalf("after sink abort lookup = (%d, %v), want (77, true)", got, found)
	}
	if got, found, err := reader.LookupDirectV4(IPv4(258)); err != nil {
		t.Fatal(err)
	} else if !found || got != 77 {
		t.Fatalf("last address lookup = (%d, %v), want (77, true)", got, found)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	// A fresh refresh after the sink abort commits an empty map.
	w, err = OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	again, err := w.BeginFirstSeenRefresh(101, NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	final, err := again.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	finishWorkflowCommit(t, final, "refresh after sink abort")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err = OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := reader.LookupDirectV4(IPv4(0)); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("committed empty refresh left a value behind")
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestFirstSeenRefreshRemovalsV6AndFamilyMismatch covers the IPv6 sink
// and the family gate: the IPv6 refresh streams IPv6 removals, and the
// IPv4 sink on an IPv6 database reports the exact Rust family error.
func TestFirstSeenRefreshRemovalsV6AndFamilyMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "first-seen-v6.iprdb")
	if _, err := Create(path, AddressFamilyIPv6, ValueKindDirect, StructureKindNone, ValueTagFirstSeen()); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	ranges := make([]AddressRange6, 3)
	for index := 0; index < 3; index++ {
		ranges[index] = AddressRange6{FromHi: uint64(index * 2), ToHi: uint64(index * 2)}
	}
	seed, err := w.BeginFirstSeenRefresh(7, NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.AddRangesV6(ranges); err != nil {
		t.Fatal(err)
	}
	finished, err := seed.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	finishWorkflowCommit(t, finished, "v6 seed")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w, err = OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	var removals []FirstSeenRemoval6
	sink6 := func(batch []FirstSeenRemoval6) error {
		removals = append(removals, batch...)
		return nil
	}
	refresh, err := w.BeginFirstSeenRefresh(8, NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := refresh.FinishInputWithRemovalsV6(sink6)
	if err != nil {
		t.Fatal(err)
	}
	if len(removals) != 3 {
		t.Fatalf("v6 removal count = %d, want 3", len(removals))
	}
	for index, removal := range removals {
		if removal.FromHi != uint64(index*2) || removal.FirstSeen != 7 {
			t.Fatalf("v6 removal %d = %#v", index, removal)
		}
	}
	prepared.Abort()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w, err = OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	mismatch, err := w.BeginFirstSeenRefresh(9, NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	var ab *abortError
	if _, err := mismatch.FinishInputWithRemovalsV4(func([]FirstSeenRemoval4) error { return nil }); !errors.As(err, &ab) || causeCode(ab.cause) != ErrorWrongAddressFamily {
		t.Fatalf("family mismatch error = %v, want TransactionAborted wrapping WrongAddressFamily", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestEnrichmentCursorLifetimeAndSeek pins the enrichment cursor
// contract (Rust borrow parity): the cursor holds one reader pin, Seek
// repositions, views refuse after Close, a second Close reports
// WrongState, and the reader cannot close while the cursor is open.
func TestEnrichmentCursorLifetimeAndSeek(t *testing.T) {
	path := structuredDB(t)
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginStructuredTransaction(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	first, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64512), MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64513), MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(0), IPv4(9), first); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(20), IPv4(29), second); err != nil {
		t.Fatal(err)
	}
	result, err := tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("commit status = %v (%v)", result.Status, result.Err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := reader.NetworkEnrichmentV1CursorV4(RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	// The cursor pin blocks reader Close.
	if err := reader.Close(); errorAsCode(err) != ErrorHandleBusy {
		t.Fatalf("reader close with open cursor = %v, want HandleBusy", err)
	}
	// Seek repositions into the second range.
	if err := cursor.Seek(IPv4(25)); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := cursor.NextRange()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || rec.From != 20 || rec.To != 29 {
		t.Fatalf("after seek NextRange = %d..%d (%v), want 20..29", rec.From, rec.To, ok)
	}
	value, err := rec.Value.Value()
	if err != nil {
		t.Fatal(err)
	}
	if value.ASN != 64513 {
		t.Fatalf("seeked range ASN = %d, want 64513", value.ASN)
	}
	if err := cursor.Close(); err != nil {
		t.Fatal(err)
	}
	// Views refuse after Close.
	if _, err := rec.Value.Value(); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("view after cursor close = %v, want WrongState", err)
	}
	// A second Close reports WrongState.
	if err := cursor.Close(); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("double cursor close = %v, want WrongState", err)
	}
	// NextRange after Close reports WrongState.
	if _, _, err := cursor.NextRange(); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("NextRange after close = %v, want WrongState", err)
	}
	// The reader closes cleanly once the cursor pin is released.
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}
