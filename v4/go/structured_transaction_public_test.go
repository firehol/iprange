// Public structured transaction tests (Rust
// tests/structured_values.rs parity): reference pinning across
// transactions and databases, abort/dedup/release/reuse of the
// structure dictionary, and the typed network_enrichment_v1 round trip
// with lazy threat membership, feed deletion, and snapshotting. The
// Rust vectors also cover recovery and validation, which arrive with
// the Go recovery milestone; the committed-file invariants are pinned
// here through the reader surface instead.

package iprangedb

import (
	"errors"
	"path/filepath"
	"testing"
)

// structuredDB creates one fresh empty IPv4 network_enrichment_v1
// structured database (Rust create_live structured + enrichment tag).
func structuredDB(t *testing.T) string {
	t.Helper()
	tag, err := NewValueTag([]byte("enrichment"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "structured.iprdb")
	if _, err := Create(path, AddressFamilyIPv4, ValueKindStructured, StructureKindNetworkEnrichmentV1, tag); err != nil {
		t.Fatal(err)
	}
	return path
}

// enrichmentValue builds one public enrichment value with only the ASN
// set (Rust NetworkEnrichmentV1 { asn, ..default }).
func enrichmentValue(asn uint32) NetworkEnrichmentV1 {
	return NetworkEnrichmentV1{ASN: asn}
}

// TestPublicStructuredTransactionReferencesBound pins the Rust
// structure-reference lifetime rules: a reference produced by an
// aborted transaction is stale for its writer (operation nonce
// mismatch) and foreign for another writer (database id mismatch).
func TestPublicStructuredTransactionReferencesBound(t *testing.T) {
	firstPath := structuredDB(t)
	secondPath := structuredDB(t)
	cancellation := NewCancellationToken()

	first, err := OpenWriter(firstPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	tx, err := first.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64512), MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}

	tx, err = first.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(0), IPv4(9), stale); !isPubCode(err, ErrorStaleReference) {
		t.Fatalf("assign with aborted-transaction structure = %v, want stale reference", err)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}

	second, err := OpenWriter(secondPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	tx, err = second.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(0), IPv4(9), stale); !isPubCode(err, ErrorForeignReference) {
		t.Fatalf("assign with foreign-database structure = %v, want foreign reference", err)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestPublicStructuredTransactionAbortDedupReleaseReuseCleanGraph pins
// the Rust abort/dedup/release/reuse lifecycle: an aborted transaction
// leaves no ranges behind, equal payloads deduplicate to one reference
// and one dictionary record, clearing every committed range releases
// the record at prepare, and the next intern reuses the lowest free
// structure id.
func TestPublicStructuredTransactionAbortDedupReleaseReuseCleanGraph(t *testing.T) {
	path := structuredDB(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Abort discards the whole draft: no range, no dictionary record.
	tx, err := w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	abandoned, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64512), MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(0), IPv4(9), abandoned); err != nil {
		t.Fatal(err)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}

	// The reader needs the exclusive writer lock released (Go has no
	// sidecar coordination yet), so the writer closes before every read.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := reader.Pin()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := pin.LookupNetworkEnrichmentV1V4(IPv4(5)); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("aborted transaction left a structured range behind")
	}
	pin.Close()
	reader.Close()

	w, err = OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}

	// Equal payloads deduplicate to the same transaction reference; the
	// shared record serves both assigns.
	tx, err = w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	value := enrichmentValue(64513)
	first, err := tx.InternNetworkEnrichmentV1(value, MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := tx.InternNetworkEnrichmentV1(value, MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	if first != duplicate {
		t.Fatal("deduplicated structure references are not equal")
	}
	if _, err := tx.AssignV4(IPv4(0), IPv4(9), first); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(20), IPv4(29), duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Clearing every committed range drops the record refcount to zero;
	// the prepare drain deletes the dictionary record, so the following
	// commit passes the entry-count invariant with an empty range tree
	// (Rust structure_entry_count <= range_record_count).
	tx, err = w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ClearV4(IPv4(0), IPv4(29)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// The released structure id is the lowest free id again; the fresh
	// record covers [40,49] while [0,29] stays empty.
	tx, err = w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64514), MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(40), IPv4(49), replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err = OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	pin, err = reader.Pin()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := pin.LookupNetworkEnrichmentV1V4(IPv4(5)); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("cleared range [0,29] still names a structure")
	}
	view, found, err := pin.LookupNetworkEnrichmentV1V4(IPv4(45))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("assigned range [40,49] is missing")
	}
	payload, err := view.Value()
	if err != nil {
		t.Fatal(err)
	}
	if payload.ASN != 64514 {
		t.Fatalf("lookup asn = %d, want 64514", payload.ASN)
	}
	pin.Close()
	reader.Close()
}

// TestPublicStructuredTransactionRoundTrip runs the Rust
// typed_structure_and_lazy_membership_round_trip vector: feeds,
// memberships, a broad value with a location and threat membership, a
// narrow value without one, assign/clear segmentation, the committed
// reader surface, the snapshot copy, and the feed deletion that
// re-interns every stored payload without the removed feed.
func TestPublicStructuredTransactionRoundTrip(t *testing.T) {
	requirePublicationSecurity(t)
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
	threat, err := tx.EnsureFeed(feedName(t, "threat-a"))
	if err != nil {
		t.Fatal(err)
	}
	threatIndex := threat.Index()
	empty, err := tx.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	membership, err := tx.AddFeed(empty, threat)
	if err != nil {
		t.Fatal(err)
	}
	broad, err := tx.InternNetworkEnrichmentV1(NetworkEnrichmentV1{
		ASN:       64512,
		CountryID: 1,
		StateID:   2,
		CityID:    3,
		Location: NetworkEnrichmentV1Location{
			LatitudeMicrodegrees:  37_983_810,
			LongitudeMicrodegrees: 23_727_539,
		},
		HasLocation: true,
	}, membership)
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64513), MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(0), IPv4(100), broad); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(20), IPv4(30), narrow); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ClearV4(IPv4(25), IPv4(27)); err != nil {
		t.Fatal(err)
	}
	result, err := tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("commit status = %v, want committed", result.Status)
	}
	// The reader needs the exclusive writer lock released (Go has no
	// sidecar coordination yet), so the writer closes before every read.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := reader.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.ValueKind != ValueKindStructured {
		t.Fatalf("value kind = %v, want structured", info.ValueKind)
	}
	if info.StructureKind != StructureKindNetworkEnrichmentV1 {
		t.Fatalf("structure kind = %v, want network enrichment v1", info.StructureKind)
	}
	pin, err := reader.Pin()
	if err != nil {
		t.Fatal(err)
	}

	// [10] carries the broad value with the exact location and the
	// lazy threat membership.
	view, found, err := pin.LookupNetworkEnrichmentV1V4(IPv4(10))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("lookup at 10 found nothing")
	}
	payload, err := view.Value()
	if err != nil {
		t.Fatal(err)
	}
	if payload.ASN != 64512 || !payload.HasLocation ||
		payload.Location.LatitudeMicrodegrees != 37_983_810 ||
		payload.Location.LongitudeMicrodegrees != 23_727_539 {
		t.Fatalf("broad value = %+v, want asn 64512 with the Athens location", payload)
	}
	threatView, present, err := view.ThreatMembership()
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("broad threat membership is missing")
	}
	contains, err := threatView.ContainsIndex(threatIndex)
	if err != nil {
		t.Fatal(err)
	}
	if !contains {
		t.Fatal("broad membership does not contain the feed")
	}

	// [24] carries the narrow value without any threat membership.
	view, found, err = pin.LookupNetworkEnrichmentV1V4(IPv4(24))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("lookup at 24 found nothing")
	}
	payload, err = view.Value()
	if err != nil {
		t.Fatal(err)
	}
	if payload.ASN != 64513 {
		t.Fatalf("narrow asn = %d, want 64513", payload.ASN)
	}
	if _, present, err := view.ThreatMembership(); err != nil {
		t.Fatal(err)
	} else if present {
		t.Fatal("narrow value reports a threat membership")
	}

	// [26] was cleared; [31] is broad again.
	if _, found, err := pin.LookupNetworkEnrichmentV1V4(IPv4(26)); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("cleared range [25,27] still names a structure")
	}
	view, found, err = pin.LookupNetworkEnrichmentV1V4(IPv4(31))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("lookup at 31 found nothing")
	}
	payload, err = view.Value()
	if err != nil {
		t.Fatal(err)
	}
	if payload.ASN != 64512 {
		t.Fatalf("lookup at 31 asn = %d, want 64512", payload.ASN)
	}

	// The feed projection coalesces the broad segments: [0,19] and
	// [31,100] (Rust feed_range_cursor_v4; the narrow [20,30] gap and
	// the cleared [25,27] both belong to the feed but carry no
	// membership there).
	threatRanges, err := reader.FeedRangeCursorV4("threat-a", RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	firstRange, ok, err := threatRanges.NextRange()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || uint32(firstRange.From) != 0 || uint32(firstRange.To) != 19 {
		t.Fatalf("first threat range = %d..%d, want 0..19", firstRange.From, firstRange.To)
	}
	secondRange, ok, err := threatRanges.NextRange()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || uint32(secondRange.From) != 31 || uint32(secondRange.To) != 100 {
		t.Fatalf("second threat range = %d..%d, want 31..100", secondRange.From, secondRange.To)
	}
	if _, ok, err := threatRanges.NextRange(); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("threat cursor produced a third range")
	}
	pin.Close()
	reader.Close()

	// The committed file snapshots into an immutable copy preserving the
	// structured values and their lazy threat membership (Rust
	// snapshot_to with a live source; Go uses the immutable source
	// because live-source coordination is milestone 4).
	snapshotPath := filepath.Join(t.TempDir(), "structured-snapshot.iprdb")
	if _, err := SnapshotTo(path, SnapshotSourceImmutable, snapshotPath, PolicyFailIfExists, snapshotBudget(2), cancellation); err != nil {
		t.Fatal(err)
	}
	assertEnrichmentSnapshot(t, snapshotPath, threatIndex)

	// Deleting the feed re-interns every stored payload without the
	// feed's bit: [10] keeps its value but loses the threat membership,
	// and every reference produced before the delete is stale.
	w, err = OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err = w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	threat, found, err = tx.LookupFeed(feedName(t, "threat-a"))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("threat-a feed missing after the snapshot round trip")
	}
	empty, err = tx.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	membership, err = tx.AddFeed(empty, threat)
	if err != nil {
		t.Fatal(err)
	}
	staleStructure, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64514), membership)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.DeleteFeed(threat); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(200), IPv4(200), staleStructure); !isPubCode(err, ErrorStaleReference) {
		t.Fatalf("assign with pre-delete structure = %v, want stale reference", err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err = OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := reader.LookupFeed("threat-a"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("deleted feed still resolves")
	}
	pin, err = reader.Pin()
	if err != nil {
		t.Fatal(err)
	}
	view, found, err = pin.LookupNetworkEnrichmentV1V4(IPv4(10))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("lookup at 10 after feed delete found nothing")
	}
	payload, err = view.Value()
	if err != nil {
		t.Fatal(err)
	}
	if payload.ASN != 64512 {
		t.Fatalf("value at 10 after feed delete = %d, want 64512", payload.ASN)
	}
	if _, present, err := view.ThreatMembership(); err != nil {
		t.Fatal(err)
	} else if present {
		t.Fatal("payload after feed delete still reports a threat membership")
	}
	pin.Close()
	reader.Close()
}

// assertEnrichmentSnapshot verifies one snapshot copy carries the same
// structured ranges and lazy threat membership as the committed file
// (Rust assert_enrichment).
func assertEnrichmentSnapshot(t *testing.T, path string, threatIndex uint32) {
	t.Helper()
	reader, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	pin, err := reader.Pin()
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	view, found, err := pin.LookupNetworkEnrichmentV1V4(IPv4(10))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("snapshot lookup at 10 found nothing")
	}
	payload, err := view.Value()
	if err != nil {
		t.Fatal(err)
	}
	if payload.ASN != 64512 {
		t.Fatalf("snapshot asn at 10 = %d, want 64512", payload.ASN)
	}
	threatView, present, err := view.ThreatMembership()
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("snapshot threat membership is missing")
	}
	contains, err := threatView.ContainsIndex(threatIndex)
	if err != nil {
		t.Fatal(err)
	}
	if !contains {
		t.Fatal("snapshot membership does not contain the feed")
	}
	view, found, err = pin.LookupNetworkEnrichmentV1V4(IPv4(24))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("snapshot lookup at 24 found nothing")
	}
	payload, err = view.Value()
	if err != nil {
		t.Fatal(err)
	}
	if payload.ASN != 64513 {
		t.Fatalf("snapshot asn at 24 = %d, want 64513", payload.ASN)
	}
	if _, found, err := pin.LookupNetworkEnrichmentV1V4(IPv4(26)); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("snapshot cleared range [25,27] still names a structure")
	}
}

// TestPublicStructuredTransactionSurface pins the structured begin
// guards and error classes (Rust begin_structured_transaction +
// MembershipState surface): the structure-kind outer guard fires on
// direct and membership databases and precedes the cancellation state
// (a fired token cannot mask the wrong-kind class), and the mutation
// surface reports the exact invalid-argument, wrong-family, stale,
// inactive, cancelled, and no-pending-transaction classes. The
// value-kind inner guard (Rust begin_structured_state) is not directly
// reachable here: bootstrap open refuses every kind combination that
// could carry a structure kind without the structured value kind
// (meta.go ValidateKindInvariants, Rust bootstrap.rs KindInvariant),
// so the guard is Rust-parity defense and the wrong-kind open class is
// FormatInvalid, pinned by the open tests.
func TestPublicStructuredTransactionSurface(t *testing.T) {
	cancellation := NewCancellationToken()

	// Outer guard: a direct database has no structure kind.
	directPath := filepath.Join(t.TempDir(), "direct.iprdb")
	tag, err := NewValueTag([]byte("direct"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(directPath, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(directPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.BeginStructuredTransaction(cancellation); !isPubCode(err, ErrorWrongStructureKind) {
		t.Fatalf("begin on direct database = %v, want wrong structure kind", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Outer guard: a membership database also has no structure kind.
	membershipPath := testFeedMembership(t)
	w, err = OpenWriter(membershipPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.BeginStructuredTransaction(cancellation); !isPubCode(err, ErrorWrongStructureKind) {
		t.Fatalf("begin on membership database = %v, want wrong structure kind", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// A fired token still reports the outer structure-kind guard
	// first: Rust begin_structured_transaction checks the kind before
	// the cancellation state, so the fired token cannot mask the
	// wrong-kind class.
	fired := NewCancellationToken()
	fired.Cancel()
	w, err = OpenWriter(membershipPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.BeginStructuredTransaction(fired); !isPubCode(err, ErrorWrongStructureKind) {
		t.Fatalf("begin with fired token on membership database = %v, want wrong structure kind", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Closed writer.
	path := structuredDB(t)
	w, err = OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.BeginStructuredTransaction(cancellation); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("begin on closed writer = %v, want wrong state", err)
	}

	// Surface errors on a healthy structured database.
	w, err = OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tx, err := w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	structure, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64512), MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(10), IPv4(9), structure); !isPubCode(err, ErrorInvalidArgument) {
		t.Fatalf("reversed range = %v, want invalid argument", err)
	}
	if _, err := tx.AssignV6(IPv6{Hi: 1}, IPv6{Hi: 2}, structure); !isPubCode(err, ErrorWrongAddressFamily) {
		t.Fatalf("wrong family = %v, want wrong address family", err)
	}

	// A membership reference produced before a feed deletion is stale
	// for the later intern (Rust require_current_membership).
	threat, err := tx.EnsureFeed(feedName(t, "threat-a"))
	if err != nil {
		t.Fatal(err)
	}
	empty, err := tx.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	membership, err := tx.AddFeed(empty, threat)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.DeleteFeed(threat); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64513), membership); !isPubCode(err, ErrorStaleReference) {
		t.Fatalf("intern with pre-delete membership = %v, want stale reference", err)
	}

	// Metadata staging reports exact changes: one stage per
	// transaction, so a second set or a clear after the set reports
	// WrongState exactly like Rust require_metadata_available.
	if changed, err := tx.SetMetadataJSON([]byte(`{"structured":1}`)); err != nil {
		t.Fatal(err)
	} else if !changed {
		t.Fatal("set metadata on an absent database reported no change")
	}
	if _, err := tx.SetMetadataJSON([]byte(`{"structured":2}`)); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("second metadata set = %v, want wrong state", err)
	}
	if _, err := tx.ClearMetadataJSON(); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("clear after set = %v, want wrong state", err)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(0), IPv4(1), structure); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("assign after abort = %v, want wrong state", err)
	}
	if err := tx.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("second abort = %v, want no pending transaction", err)
	}

	// A fresh transaction clears the absent metadata as a no-op.
	tx, err = w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.ClearMetadataJSON(); err != nil {
		t.Fatal(err)
	} else if changed {
		t.Fatal("clear metadata on an absent database reported a change")
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}

	// Cancellation aborts the transaction through the writer (Rust
	// check_or_abort).
	tx, err = w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64514), MembershipRef{}); err != nil {
		t.Fatal(err)
	}
	cancellation.Cancel()
	if _, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64515), MembershipRef{}); !isPubCode(err, ErrorTransactionAborted) {
		t.Fatalf("intern after cancel = %v, want transaction aborted", err)
	}
}

// TestPublicStructuredTransactionStaleEmptyMembership pins the intern
// None sentinel: only the literal zero MembershipRef is None, while the
// empty membership of an aborted transaction is a real reference that
// another transaction refuses as stale (Rust
// intern_network_enrichment_v1 Some require_current_membership).
func TestPublicStructuredTransactionStaleEmptyMembership(t *testing.T) {
	path := structuredDB(t)
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	tx1, err := w.BeginStructuredTransaction(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	empty, err := tx1.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	if err := tx1.Abort(); err != nil {
		t.Fatal(err)
	}

	tx2, err := w.BeginStructuredTransaction(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx2.InternNetworkEnrichmentV1(enrichmentValue(64512), empty); !isPubCode(err, ErrorStaleReference) {
		t.Fatalf("intern with aborted-transaction empty membership = %v, want stale reference", err)
	}
}

// TestPublicStructuredTransactionOpErrorAborts pins the Rust mutate
// abort contract (live_writer.rs mutate -> abort_after): an error
// raised inside the edit, after the pre-mutate require checks,
// discards the draft, spends the transaction, and reports
// TransactionAborted wrapping the cause. The transaction is then dead
// (next op WrongState, Commit and Abort report the writer's clean
// state), exactly like Rust require_active / commit_attempt / abort
// after abort_after.
func TestPublicStructuredTransactionOpErrorAborts(t *testing.T) {
	path := structuredDB(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	tx, err := w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	// Out-of-range coordinates fail inside the intern encode (Rust
	// edit.intern_network_enrichment_v1 inside mutate), so the op
	// aborts with TransactionAborted wrapping InvalidArgument.
	bad := NetworkEnrichmentV1{ASN: 64512, HasLocation: true, Location: NetworkEnrichmentV1Location{LatitudeMicrodegrees: 90_000_001}}
	if _, err := tx.InternNetworkEnrichmentV1(bad, MembershipRef{}); err == nil {
		t.Fatal("intern with out-of-range coordinates was accepted")
	} else {
		var ab *abortError
		if !errors.As(err, &ab) || !isPubCode(ab.cause, ErrorInvalidArgument) {
			t.Fatalf("intern coordinates = %v, want transaction aborted wrapping invalid argument", err)
		}
	}
	// The transaction is dead and the writer is clean.
	if _, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64513), MembershipRef{}); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("intern on aborted transaction = %v, want wrong state", err)
	}
	if _, err := tx.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("commit on aborted transaction = %v, want no pending transaction", err)
	}
	if err := tx.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort on aborted transaction = %v, want no pending transaction", err)
	}

	// The same contract covers the membership catalog surface: a
	// rename onto an existing name fails inside the edit.
	tx, err = w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	first, err := tx.EnsureFeed(feedName(t, "first-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.EnsureFeed(feedName(t, "second-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.RenameFeed(first, feedName(t, "second-a")); err == nil {
		t.Fatal("rename onto existing name was accepted")
	} else {
		var ab *abortError
		if !errors.As(err, &ab) || !isPubCode(ab.cause, ErrorNameExists) {
			t.Fatalf("rename onto existing = %v, want transaction aborted wrapping name exists", err)
		}
	}
	if _, err := tx.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("commit on aborted transaction = %v, want no pending transaction", err)
	}
}
