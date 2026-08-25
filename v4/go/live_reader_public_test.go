// Public live reader round trip (Rust tests/live_roundtrip.rs reader
// cases through the public facade): OpenLiveReader, the pinned-generation
// lookups, pins, the close state machine, and the public ReaderCloseResult
// facts.

package iprangedb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// createLiveMembershipPair builds one live IPv4 membership pair with one
// committed feed: the membership database is written through the
// immutable writer and converted by InitializeLive (the live writer
// surface is direct-only, so live membership content arrives through the
// conversion path).
func createLiveMembershipPair(t *testing.T, capacity uint32) string {
	t.Helper()
	tag, err := NewValueTag([]byte("feeds"))
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(t.TempDir(), "membership.iprdb")
	if _, err := Create(main, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(main, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	create, err := w.BeginCreateFeed(FeedName("first"), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if err := create.AddRangesV4(feedRanges1000()); err != nil {
		t.Fatal(err)
	}
	finished, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	// The Rust populated_membership fixture carries the b"{}" metadata;
	// the live snapshot test asserts the snapshot preserves it.
	if _, err := finished.SetMetadataJSON([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	result, err := finished.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("commit status = %v, want committed", result.Status)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeLive(main, capacity, nil); err != nil {
		t.Fatalf("InitializeLive: %v", err)
	}
	return main
}

func TestPublicOpenLiveReaderRoundTrip(t *testing.T) {
	requireLiveCreation(t)
	main, created := createLivePublicPair(t, 2)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(10), IPv4(30), 1); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	if changed, err := tx.AssignV4(IPv4(22), IPv4(23), 3); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	if result, err := tx.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("commit: status=%v err=%v", result.Status, err)
	}

	r, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}

	info, err := r.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.TransactionID != 2 || info.DatabaseID != created.DatabaseID {
		t.Fatalf("info = %+v, want txn 2 of the created pair", info)
	}
	if info.MetaSelection != MetaSelectionProvenCurrent {
		t.Fatalf("meta selection = %v, want ProvenCurrent", info.MetaSelection)
	}

	device, inode, err := r.FileIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if created.MainIdentity == nil || created.MainIdentity.Kind != 1 {
		t.Fatal("created main identity missing")
	}
	// The public identity bytes are device little-endian, inode
	// little-endian (FileIdentity contract); the reader identity must
	// match the creation identity of the same inode.
	wantDevice := binary.LittleEndian.Uint64(created.MainIdentity.Bytes[0:8])
	wantInode := binary.LittleEndian.Uint64(created.MainIdentity.Bytes[8:16])
	if device != wantDevice || inode != wantInode {
		t.Fatalf("identity = %d/%d, want created %d/%d", device, inode, wantDevice, wantInode)
	}

	if value, found, err := r.LookupDirectV4(IPv4(19)); err != nil || !found || value != 1 {
		t.Fatalf("lookup 19 = %d/%v err=%v, want 1/true", value, found, err)
	}
	if value, found, err := r.LookupDirectV4(IPv4(22)); err != nil || !found || value != 3 {
		t.Fatalf("lookup 22 = %d/%v err=%v, want 3/true", value, found, err)
	}
	if _, found, err := r.LookupDirectV4(IPv4(200)); err != nil || found {
		t.Fatalf("lookup 200 = found=%v err=%v, want absent", found, err)
	}

	ranges := 0
	if err := r.DirectRangesV4(func(DirectRangeV4) error { ranges++; return nil }); err != nil {
		t.Fatal(err)
	}
	// The (22,23) assign splits the (10,30) range: 10-21=1, 22-23=3,
	// 24-30=1 (Rust ordered assign parity).
	if ranges != 3 {
		t.Fatalf("range records = %d, want 3", ranges)
	}

	count, err := r.Cardinality()
	if err != nil {
		t.Fatal(err)
	}
	value, err := count.Uint64()
	if err != nil {
		t.Fatal(err)
	}
	if value != 21 {
		t.Fatalf("cardinality = %d, want 21 (10..30 inclusive)", value)
	}

	// Feed access and membership pin lookups on a direct database report
	// the wrong-kind class (Rust reader pre-check parity).
	if _, _, err := r.LookupFeed("first"); lifecycleCode(err) != ErrorWrongValueKind {
		t.Fatalf("LookupFeed on direct = %v, want WrongValueKind", err)
	}
	pin, err := r.Pin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pin.LookupMembershipV4(IPv4(19)); lifecycleCode(err) != ErrorWrongValueKind {
		t.Fatalf("membership lookup on direct = %v, want WrongValueKind", err)
	}
	if err := pin.Close(); err != nil {
		t.Fatal(err)
	}

	// Metadata is absent on the created pair.
	if bytes, present, err := r.MetadataJSON(); err != nil || present || bytes != nil {
		t.Fatalf("metadata = %v/%v err=%v, want absent", bytes, present, err)
	}

	result, err := r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CloseOutcomeClosed {
		t.Fatalf("close outcome = %v, want closed", result.Outcome)
	}
	if result.CoordinationCleanup != CoordinationCleanupNone || result.CleanupState() != CleanupStateClean {
		t.Fatalf("close facts = %+v, want clean closed", result)
	}
	if result.Cause != nil {
		t.Fatalf("closed result carries cause %v", result.Cause)
	}

	// The closed reader reports WrongState on every operation and the
	// idempotent closed result on a second close.
	if _, err := r.Info(); lifecycleCode(err) != ErrorWrongState {
		t.Fatalf("Info after close = %v, want WrongState", err)
	}
	result, err = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CloseOutcomeClosed {
		t.Fatalf("second close outcome = %v, want idempotent closed", result.Outcome)
	}
	if _, err := w.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}
}

func TestPublicLiveReaderPinsBlockCloseAndReadPinnedMembership(t *testing.T) {
	requireLiveCreation(t)
	main := createLiveMembershipPair(t, 2)
	r, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}

	pin, err := r.Pin()
	if err != nil {
		t.Fatal(err)
	}
	view, found, err := pin.LookupMembershipV4(IPv4(10))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("membership view not found for a committed feed address")
	}
	if count, err := view.WordCount(); err != nil || count == 0 {
		t.Fatalf("word count = %d err=%v, want non-zero", count, err)
	}
	if member, err := view.ContainsIndex(0); err != nil || !member {
		t.Fatalf("ContainsIndex(0) = %v err=%v, want true", member, err)
	}

	// The reader cannot close while the pin is live (Rust HandleBusy
	// parity with the immutable reader).
	if _, err := r.Close(); lifecycleCode(err) != ErrorHandleBusy {
		t.Fatalf("close with live pin = %v, want HandleBusy", err)
	}
	// The reader still works after the refused close.
	if _, err := r.Info(); err != nil {
		t.Fatalf("Info after refused close: %v", err)
	}

	if err := pin.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CloseOutcomeClosed || result.CleanupState() != CleanupStateClean {
		t.Fatalf("close = %+v, want clean closed", result)
	}
}

func TestPublicLiveReaderRetryClose(t *testing.T) {
	requireLiveCreation(t)
	main, _ := createLivePublicPair(t, 2)
	r, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	replaced := main + ".replaced"
	if err := os.Rename(main, replaced); err != nil {
		t.Fatal(err)
	}
	result, err := r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CloseOutcomeCloseIncomplete {
		t.Fatalf("outcome = %v, want close incomplete", result.Outcome)
	}
	if result.CoordinationCleanup != CoordinationCleanupRetainedReaderCloseRequired {
		t.Fatalf("coordination = %v, want retained reader close", result.CoordinationCleanup)
	}
	if result.Cause == nil {
		t.Fatal("incomplete close without a cause")
	}
	if result.CleanupState() != CleanupStateResiduePossible {
		t.Fatal("incomplete close reports clean")
	}
	// The close-only reader refuses operations (Rust require_open).
	if _, err := r.Info(); lifecycleCode(err) != ErrorWrongState {
		t.Fatalf("Info on close-only reader = %v, want WrongState", err)
	}

	if err := os.Rename(replaced, main); err != nil {
		t.Fatal(err)
	}
	result, err = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CloseOutcomeClosed || result.CleanupState() != CleanupStateClean {
		t.Fatalf("retried close = %+v, want clean closed", result)
	}
}

func TestPublicLiveReaderCancelledOpenLeavesNoResidue(t *testing.T) {
	requireLiveCreation(t)
	main, _ := createLivePublicPair(t, 2)
	cancellation := NewCancellationToken()
	cancellation.Cancel()
	if _, err := OpenLiveReader(main, cancellation); lifecycleCode(err) != ErrorCancelled {
		t.Fatalf("cancelled open = %v, want ErrorCancelled", err)
	}
	// No lock or slot residue: a clean open and close follow.
	r, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := r.Close(); err != nil || result.Outcome != CloseOutcomeClosed {
		t.Fatalf("close = %+v err=%v, want closed", result, err)
	}
}

// TestPublicLiveReaderCursorSurfaceDirect covers the direct cursors on
// the live facade (Rust LiveReader::direct_cursor_v4/v6) plus the
// wrong-kind refusals of the membership and enrichment surfaces.
func TestPublicLiveReaderCursorSurfaceDirect(t *testing.T) {
	requireLiveCreation(t)
	main, _ := createLivePublicPair(t, 2)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(10), IPv4(30), 1); err != nil || !changed {
		t.Fatalf("assign 10-30: changed=%v err=%v", changed, err)
	}
	if changed, err := tx.AssignV4(IPv4(22), IPv4(23), 3); err != nil || !changed {
		t.Fatalf("assign 22-23: changed=%v err=%v", changed, err)
	}
	if result, err := tx.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("commit = %+v err=%v, want committed", result, err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if result, err := r.Close(); err != nil || result.Outcome != CloseOutcomeClosed {
			t.Fatalf("close = %+v err=%v, want closed", result, err)
		}
	}()

	cursor, err := r.DirectCursorV4(RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	var got []DirectRangeV4
	for {
		rec, ok, err := cursor.NextRange()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, rec)
	}
	// The (22,23) assign splits the (10,30) range: 10-21=1, 22-23=3,
	// 24-30=1 (Rust ordered assign parity).
	if len(got) != 3 || got[0] != (DirectRangeV4{10, 21, 1}) || got[1] != (DirectRangeV4{22, 23, 3}) || got[2] != (DirectRangeV4{24, 30, 1}) {
		t.Fatalf("forward walk = %+v, want 10-21/22-23/24-30", got)
	}

	// Backward walk returns the same records in reverse order.
	back, err := r.DirectCursorV4(RangeDirectionBackward)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := back.NextRange()
	if err != nil || !ok {
		t.Fatalf("backward first = %+v ok=%v err=%v", rec, ok, err)
	}
	if rec != (DirectRangeV4{24, 30, 1}) {
		t.Fatalf("backward first = %+v, want 24-30", rec)
	}

	// Seek repositions to the containing range.
	forward, err := r.DirectCursorV4(RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	if err := forward.Seek(IPv4(22)); err != nil {
		t.Fatal(err)
	}
	if rec, ok, err := forward.NextRange(); err != nil || !ok || rec != (DirectRangeV4{22, 23, 3}) {
		t.Fatalf("seek(22) = %+v ok=%v err=%v, want 22-23", rec, ok, err)
	}

	// Wrong family and wrong kind refusals (Rust reader pre-checks).
	if _, err := r.DirectCursorV6(RangeDirectionForward); lifecycleCode(err) != ErrorWrongAddressFamily {
		t.Fatalf("DirectCursorV6 on v4 = %v, want WrongAddressFamily", err)
	}
	if _, err := r.FeedCursor(); lifecycleCode(err) != ErrorWrongValueKind {
		t.Fatalf("FeedCursor on direct = %v, want WrongValueKind", err)
	}
	if _, err := r.MembershipQuery(); lifecycleCode(err) != ErrorWrongValueKind {
		t.Fatalf("MembershipQuery on direct = %v, want WrongValueKind", err)
	}
	if _, err := r.NetworkEnrichmentV1CursorV4(RangeDirectionForward); lifecycleCode(err) != ErrorWrongStructureKind {
		t.Fatalf("enrichment cursor on direct = %v, want WrongStructureKind", err)
	}
}

// TestPublicLiveReaderCursorSurfaceMembership covers the catalog and
// named-feed cursors plus the membership query on the live facade (Rust
// LiveReader::feed_cursor, feed_range_cursor_v4/v6, membership_query).
func TestPublicLiveReaderCursorSurfaceMembership(t *testing.T) {
	requireLiveCreation(t)
	main := createLiveMembershipPair(t, 2)
	r, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if result, err := r.Close(); err != nil || result.Outcome != CloseOutcomeClosed {
			t.Fatalf("close = %+v err=%v, want closed", result, err)
		}
	}()

	// FeedCursor enumerates the catalog: one feed named "first".
	feeds, err := r.FeedCursor()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok, err := feeds.NextFeed()
	if err != nil || !ok {
		t.Fatalf("first feed = %+v ok=%v err=%v", entry, ok, err)
	}
	if entry.Name != "first" || entry.Index != 0 {
		t.Fatalf("feed = %+v, want first/0", entry)
	}
	if _, ok, err := feeds.NextFeed(); err != nil || ok {
		t.Fatalf("second feed = ok=%v err=%v, want absent", ok, err)
	}

	// FeedRangeCursorV4 walks the 1000 single-point ranges of "first".
	projection, err := r.FeedRangeCursorV4("first", RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := projection.NextRange()
	if err != nil || !ok {
		t.Fatalf("first interval = %+v ok=%v err=%v", first, ok, err)
	}
	if first.From != 0 || first.To != 0 {
		t.Fatalf("first interval = %+v, want 0-0", first)
	}
	last := first
	count := 1
	for {
		rec, ok, err := projection.NextRange()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		last = rec
		count++
	}
	if count != 1000 || last.From != 1998 || last.To != 1998 {
		t.Fatalf("projection = %d intervals, last %+v, want 1000 ending 1998", count, last)
	}

	// Unknown feed and wrong family refusals.
	if _, err := r.FeedRangeCursorV4("missing", RangeDirectionForward); lifecycleCode(err) != ErrorNameNotFound {
		t.Fatalf("missing feed = %v, want NameNotFound", err)
	}
	if _, err := r.FeedRangeCursorV6("first", RangeDirectionForward); lifecycleCode(err) != ErrorWrongAddressFamily {
		t.Fatalf("FeedRangeCursorV6 on v4 = %v, want WrongAddressFamily", err)
	}

	// MembershipQuery: the full scope resolves the single active feed,
	// and the point match emits it.
	query, err := r.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := query.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if scope.FeedCount() != 1 {
		t.Fatalf("scope feeds = %d, want 1", scope.FeedCount())
	}
	names := scope.Feeds()
	if len(names) != 1 || names[0].Name != "first" {
		t.Fatalf("scope names = %+v, want [first]", names)
	}
	report, err := query.MatchingFeedsV4(IPv4(10), func(name string) error {
		if name != "first" {
			t.Fatalf("matched %q, want first", name)
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.MatchingFeedCount != 1 {
		t.Fatalf("matching count = %d, want 1", report.MatchingFeedCount)
	}
}

// TestPublicLiveReaderEnrichmentCursorPinsReader covers the structured
// enrichment cursor on the live facade (Rust
// LiveReader::network_enrichment_v1_cursor_v4): the cursor walks the
// committed enrichment ranges, holds one reader pin for its lifetime
// (Close reports HandleBusy while it is open), and releases the pin at
// cursor Close.
func TestPublicLiveReaderEnrichmentCursorPinsReader(t *testing.T) {
	requireLiveCreation(t)
	// A structured live pair arrives through conversion: the live
	// writer is direct-only, so the enrichment content is written by
	// the immutable writer and converted by InitializeLive.
	path := structuredDB(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	firstRef, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64513), MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	secondRef, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64514), MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(0), IPv4(9), firstRef); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(20), IPv4(29), secondRef); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeLive(path, 2, nil); err != nil {
		t.Fatalf("InitializeLive: %v", err)
	}

	r, err := OpenLiveReader(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := r.NetworkEnrichmentV1CursorV4(RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}

	// The cursor holds one reader pin: Close reports HandleBusy.
	if _, err := r.Close(); lifecycleCode(err) != ErrorHandleBusy {
		t.Fatalf("close with open cursor = %v, want HandleBusy", err)
	}

	rec, ok, err := cursor.NextRange()
	if err != nil || !ok {
		t.Fatalf("first enrichment range = %+v ok=%v err=%v", rec, ok, err)
	}
	if rec.From != 0 || rec.To != 9 {
		t.Fatalf("first range = %+v, want 0-9", rec)
	}
	value, err := rec.Value.Value()
	if err != nil {
		t.Fatal(err)
	}
	if value.ASN != 64513 {
		t.Fatalf("first ASN = %d, want 64513", value.ASN)
	}
	rec, ok, err = cursor.NextRange()
	if err != nil || !ok {
		t.Fatalf("second enrichment range = %+v ok=%v err=%v", rec, ok, err)
	}
	value, err = rec.Value.Value()
	if err != nil {
		t.Fatal(err)
	}
	if value.ASN != 64514 {
		t.Fatalf("second ASN = %d, want 64514", value.ASN)
	}

	if err := cursor.Close(); err != nil {
		t.Fatal(err)
	}
	if result, err := r.Close(); err != nil || result.Outcome != CloseOutcomeClosed {
		t.Fatalf("close after cursor = %+v err=%v, want closed", result, err)
	}
}

// TestPublicLiveReaderPinCloseRace hammers Pin against Close on one live
// reader (run under -race). The closed transition is atomic (SOW-0025
// chunk 4-5 review P1-1 regression pin): every Pin either succeeds while
// the reader is open or reports WrongState, Close either reports
// HandleBusy while pins exist or completes the close exactly once, and
// the internal reader is never touched concurrently by Pin and Close.
func TestPublicLiveReaderPinCloseRace(t *testing.T) {
	requireLiveCreation(t)
	main, _ := createLivePublicPair(t, 2)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(10), IPv4(30), 1); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	if result, err := tx.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("commit = %+v err=%v, want committed", result, err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var pinned int64
	var refused int64
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				pin, err := r.Pin()
				if err != nil {
					if lifecycleCode(err) != ErrorWrongState {
						t.Errorf("Pin = %v, want WrongState or success", err)
						return
					}
					atomic.AddInt64(&refused, 1)
					continue
				}
				atomic.AddInt64(&pinned, 1)
				if err := pin.Close(); err != nil {
					t.Errorf("pin close: %v", err)
					return
				}
			}
		}()
	}
	// Let the pin workers establish a steady stream of pins, then start
	// the closer so both arbitrations are exercised: Close racing live
	// pins (HandleBusy) and Close winning the transition (all later
	// Pins report WrongState).
	for atomic.LoadInt64(&pinned) < 64 {
		time.Sleep(time.Millisecond)
	}
	// The closer retries until the close completes: every HandleBusy is
	// a correct arbitration against a concurrently registered pin (the
	// public Close reports it as an error, like the immutable reader).
	for {
		result, err := r.Close()
		if err != nil {
			if lifecycleCode(err) == ErrorHandleBusy {
				time.Sleep(time.Millisecond)
				continue
			}
			t.Fatal(err)
		}
		if result.Outcome == CloseOutcomeClosed {
			break
		}
		if result.Outcome != CloseOutcomeCloseIncomplete {
			t.Fatalf("close outcome = %v, want closed or incomplete", result.Outcome)
		}
	}
	close(stop)
	wg.Wait()
	if pinned == 0 || refused == 0 {
		t.Fatalf("pin outcomes: %d pinned %d refused, want both sides", pinned, refused)
	}
	// The closed reader refuses further pins.
	if _, err := r.Pin(); lifecycleCode(err) != ErrorWrongState {
		t.Fatalf("Pin after close = %v, want WrongState", err)
	}
}
