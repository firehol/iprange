// Public live-writer workflow surface tests (SOW-0027 slice 2a): the
// advanced transactions and high-level workflows bound to the live
// writer. The chain test proves the full normative path - CreateLive,
// OpenLiveWriter, BeginCreateFeed, FinishInput, Commit, SnapshotTo, and
// OpenImmutable reads the committed result - and the sibling tests cover
// feed lifecycle, direct workflows, the structured transaction, and the
// history projection through the LiveWriter facade.

package iprangedb

import (
	"path/filepath"
	"testing"
)

// liveFeedMembership creates one fresh empty IPv4 membership live pair
// through CreateLive (Rust create_live membership + feeds tag).
func liveFeedMembership(t *testing.T, readerCapacity uint32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "live-feed-workflow.iprdb")
	_, err := CreateLive(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, mustTag(t, "feeds"), readerCapacity, nil)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// liveDirectSource creates one fresh IPv4 direct live pair with the
// requested value tag through CreateLive.
func liveDirectSource(t *testing.T, tag ValueTag, readerCapacity uint32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "live-direct-workflow.iprdb")
	_, err := CreateLive(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, readerCapacity, nil)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPublicLiveFeedWorkflowEndToEnd runs the full normative public
// chain (Rust tests/live_feed_workflow_roundtrip.rs parity): the live
// membership pair is created, the named feed workflow runs over the
// live writer, the commit publishes through the reader-table gate, the
// live snapshot packs the committed generation, and the immutable
// reader resolves the feed membership.
func TestPublicLiveFeedWorkflowEndToEnd(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	main := liveFeedMembership(t, 2)
	destination := filepath.Join(t.TempDir(), "live-feed-output.iprdb")

	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	name := feedName(t, "feed")
	cancellation := NewCancellationToken()
	create, err := w.BeginCreateFeed(name, cancellation)
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
	if !finished.IsChanged() {
		t.Fatal("live feed creation on an empty base is not a change")
	}
	report := finished.Report()
	if report.InputRecordCount != 1000 {
		t.Fatalf("input record count = %d, want 1000", report.InputRecordCount)
	}
	result, err := finished.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("commit status = %v, want committed (result %+v, err %v)", result.Status, result, result.Err)
	}
	// The live commit retained the cleanup surface (Rust CommitResult):
	// a clean live commit leaves no residue.
	if state := result.CleanupState(); state != CleanupStateClean {
		t.Fatalf("cleanup state = %v, want clean", state)
	}
	// The spent handle refuses a second consumption.
	if _, err := finished.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("second commit = %v, want no pending transaction", err)
	}
	// The writer stayed healthy and clean after the commit.
	if err := w.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after commit = %v, want no pending transaction", err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	snap, err := SnapshotTo(main, SnapshotSourceLive, destination, PolicyFailIfExists, snapshotBudget(3), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Publication.Publication != PublicationPublished {
		t.Fatalf("snapshot status = %v, want published", snap.Publication.Publication)
	}
	reader, err := OpenImmutable(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	entry, found, err := reader.LookupFeed("feed")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("committed feed is absent from the snapshot")
	}
	pin, err := reader.Pin()
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	view, found, err := pin.LookupMembershipV4(IPv4(42))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("covered address has no membership bitmap")
	}
	contained, err := view.ContainsIndex(entry.Index)
	if err != nil {
		t.Fatal(err)
	}
	if !contained {
		t.Fatal("covered address does not contain the committed feed")
	}
	uncovered, found, err := pin.LookupMembershipV4(IPv4(2_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("uncovered address reported a membership bitmap")
	}
	_ = uncovered
}

// TestPublicLiveFeedLifecycle runs rename, replace, and delete over the
// live writer (Rust LiveWriter::rename_feed/delete_feed +
// begin_replace_feed with the live sidecar lease): each prepared change
// commits through the live barrier and the final catalog is verified
// from a live snapshot.
func TestPublicLiveFeedLifecycle(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	main := liveFeedMembership(t, 2)
	destination := filepath.Join(t.TempDir(), "live-feed-lifecycle-output.iprdb")

	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := w.BeginCreateFeed(feedName(t, "first"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.AddRangesV4([]AddressRange4{{From: IPv4(1), To: IPv4(100)}}); err != nil {
		t.Fatal(err)
	}
	first, err := created.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := first.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("create commit: %v (result %+v)", err, result)
	}
	second, err := w.BeginCreateFeed(feedName(t, "second"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.AddRangesV4([]AddressRange4{{From: IPv4(200), To: IPv4(300)}}); err != nil {
		t.Fatal(err)
	}
	finished, err := second.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := finished.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("second create commit: %v (result %+v)", err, result)
	}

	renamed, err := w.RenameFeed(feedName(t, "first"), feedName(t, "renamed"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := renamed.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("rename commit: %v (result %+v)", err, result)
	}
	// The rename is proven only through the next committed generation
	// (the snapshot at the end of the test); the live base is
	// coordination-owned while the workflow draft is open.
	replaced, err := w.BeginReplaceFeed(feedName(t, "renamed"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaced.AddRangesV4([]AddressRange4{{From: IPv4(1000), To: IPv4(2000)}}); err != nil {
		t.Fatal(err)
	}
	replacement, err := replaced.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := replacement.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("replace commit: %v (result %+v)", err, result)
	}
	deleted, err := w.DeleteFeed(feedName(t, "second"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := deleted.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("delete commit: %v (result %+v)", err, result)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	snap, err := SnapshotTo(main, SnapshotSourceLive, destination, PolicyFailIfExists, snapshotBudget(3), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Publication.Publication != PublicationPublished {
		t.Fatalf("snapshot status = %v, want published", snap.Publication.Publication)
	}
	reader, err := OpenImmutable(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, found, err := reader.LookupFeed("renamed"); err != nil || !found {
		t.Fatalf("renamed feed: found=%v err=%v", found, err)
	}
	if _, found, err := reader.LookupFeed("first"); err != nil || found {
		t.Fatalf("old feed in snapshot: found=%v err=%v", found, err)
	}
	if _, found, err := reader.LookupFeed("second"); err != nil || found {
		t.Fatalf("deleted feed in snapshot: found=%v err=%v", found, err)
	}
}

// TestPublicLiveDirectWorkflowSurface runs the direct replacement and
// both timestamp refreshes through the LiveWriter facade (Rust
// LiveWriter::begin_direct_replacement / begin_first_seen_refresh /
// begin_last_seen_refresh): each workflow commits through the live
// barrier and the replacement result is verified from a live snapshot.
func TestPublicLiveDirectWorkflowSurface(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)

	replacementSource := liveDirectSource(t, mustTag(t, "direct"), 2)
	replacementOut := filepath.Join(t.TempDir(), "live-direct-output.iprdb")
	w, err := OpenLiveWriter(replacementSource, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := w.BeginDirectReplacement(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.AddRangesV4(directRanges1000()); err != nil {
		t.Fatal(err)
	}
	finished, err := replacement.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := finished.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("direct replacement commit: %v (result %+v)", err, result)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	snap, err := SnapshotTo(replacementSource, SnapshotSourceLive, replacementOut, PolicyFailIfExists, snapshotBudget(3), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Publication.Publication != PublicationPublished {
		t.Fatalf("replacement snapshot status = %v, want published", snap.Publication.Publication)
	}
	reader, err := OpenImmutable(replacementOut)
	if err != nil {
		t.Fatal(err)
	}
	value, found, err := reader.LookupDirectV4(IPv4(42))
	if err != nil {
		t.Fatal(err)
	}
	if !found || value != 21 {
		t.Fatalf("replacement lookup = (%d, %v), want (21, true)", value, found)
	}
	reader.Close()

	firstSeenSource := liveDirectSource(t, ValueTagFirstSeen(), 2)
	refreshOut := filepath.Join(t.TempDir(), "live-first-seen-output.iprdb")
	w, err = OpenLiveWriter(firstSeenSource, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := w.BeginFirstSeenRefresh(7, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := refresh.AddRangesV4([]AddressRange4{{From: IPv4(10), To: IPv4(20)}}); err != nil {
		t.Fatal(err)
	}
	finishedRefresh, err := refresh.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := finishedRefresh.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("first-seen commit: %v (result %+v)", err, result)
	}
	if err := w.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after first-seen commit = %v, want no pending transaction", err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	snap, err = SnapshotTo(firstSeenSource, SnapshotSourceLive, refreshOut, PolicyFailIfExists, snapshotBudget(3), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Publication.Publication != PublicationPublished {
		t.Fatalf("first-seen snapshot status = %v, want published", snap.Publication.Publication)
	}
	seen, err := OpenImmutable(refreshOut)
	if err != nil {
		t.Fatal(err)
	}
	value, found, err = seen.LookupDirectV4(IPv4(15))
	if err != nil {
		t.Fatal(err)
	}
	if !found || value != 7 {
		t.Fatalf("first-seen lookup = (%d, %v), want (7, true)", value, found)
	}
	seen.Close()

	lastSeenSource := liveDirectSource(t, ValueTagLastSeen(), 2)
	lastOut := filepath.Join(t.TempDir(), "live-last-seen-output.iprdb")
	w, err = OpenLiveWriter(lastSeenSource, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	lastSeen, err := w.BeginLastSeenRefresh(9, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := lastSeen.AddRangesV4([]AddressRange4{{From: IPv4(30), To: IPv4(40)}}); err != nil {
		t.Fatal(err)
	}
	lastFinished, err := lastSeen.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := lastFinished.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("last-seen commit: %v (result %+v)", err, result)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	snap, err = SnapshotTo(lastSeenSource, SnapshotSourceLive, lastOut, PolicyFailIfExists, snapshotBudget(3), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Publication.Publication != PublicationPublished {
		t.Fatalf("last-seen snapshot status = %v, want published", snap.Publication.Publication)
	}
	last, err := OpenImmutable(lastOut)
	if err != nil {
		t.Fatal(err)
	}
	defer last.Close()
	value, found, err = last.LookupDirectV4(IPv4(35))
	if err != nil {
		t.Fatal(err)
	}
	if !found || value != 9 {
		t.Fatalf("last-seen lookup = (%d, %v), want (9, true)", value, found)
	}
}

// TestPublicLiveStructuredTransaction runs one advanced structured
// transaction through the LiveWriter facade (Rust
// LiveWriter::begin_structured_transaction): the feed is ensured, the
// enrichment structure is interned, one range is assigned, and the
// commit publishes through the live barrier; the packed snapshot opens
// as a structured database.
func TestPublicLiveStructuredTransaction(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	path := filepath.Join(t.TempDir(), "live-structured.iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindStructured, StructureKindNetworkEnrichmentV1, mustTag(t, "enrichment"), 2, nil); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "live-structured-output.iprdb")
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := w.BeginStructuredTransaction(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.EnsureFeed(feedName(t, "feed")); err != nil {
		t.Fatal(err)
	}
	empty, err := transaction.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	structure, err := transaction.InternNetworkEnrichmentV1(enrichmentValue(65001), empty)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := transaction.AssignV4(IPv4(10), IPv4(20), structure); err != nil || !changed {
		t.Fatalf("assignment: changed=%v err=%v", changed, err)
	}
	if _, err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	snap, err := SnapshotTo(path, SnapshotSourceLive, destination, PolicyFailIfExists, snapshotBudget(3), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Publication.Publication != PublicationPublished {
		t.Fatalf("structured snapshot status = %v, want published", snap.Publication.Publication)
	}
	reader, err := OpenImmutable(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	info, err := reader.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.ValueKind != ValueKindStructured || info.StructureKind != StructureKindNetworkEnrichmentV1 {
		t.Fatalf("snapshot info = %+v, want structured network enrichment", info)
	}
}

// TestPublicLiveHistoryProjection runs one history projection through
// the LiveWriter facade (Rust LiveWriter::project_history with the live
// source mode): a live last_seen source is scanned into a live
// membership destination, the changed terminal commits, and the
// snapshot carries the projected window feed.
func TestPublicLiveHistoryProjection(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	sourcePath := liveDirectSource(t, ValueTagLastSeen(), 2)
	destPath := liveFeedMembership(t, 2)
	destination := filepath.Join(t.TempDir(), "live-history-output.iprdb")

	// Seed the direct source through the live writer.
	sourceWriter, err := OpenLiveWriter(sourcePath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := sourceWriter.BeginDirect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := seed.AssignV4(IPv4(1), IPv4(100), 3); err != nil || !changed {
		t.Fatalf("seed assignment: changed=%v err=%v", changed, err)
	}
	if _, err := seed.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceWriter.Close(); err != nil {
		t.Fatal(err)
	}
	source, err := OpenLiveReader(sourcePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	writer, err := OpenLiveWriter(destPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := writer.ProjectHistory(HistoryProjectionSource{
		Kind: HistoryProjectionSourceLive,
		Live: source,
	}, []HistoryWindow{{FeedName: "window", Cutoff: 0}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !projection.IsChanged() {
		t.Fatal("live history projection on an empty destination is not a change")
	}
	report := projection.Report()
	if report.SourceRangeCount != 1 || report.CreatedFeedCount != 1 {
		t.Fatalf("projection report = %+v, want one source range and one created feed", report)
	}
	if result, err := projection.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("projection commit: %v (result %+v)", err, result)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	snap, err := SnapshotTo(destPath, SnapshotSourceLive, destination, PolicyFailIfExists, snapshotBudget(3), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Publication.Publication != PublicationPublished {
		t.Fatalf("history snapshot status = %v, want published", snap.Publication.Publication)
	}
	reader, err := OpenImmutable(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, found, err := reader.LookupFeed("window"); err != nil || !found {
		t.Fatalf("projected window feed: found=%v err=%v", found, err)
	}
}
