package iprangedb

// Public Writer.ProjectHistory facade tests (SOW-0025 chunk 3b-4 slice
// C): created feeds on empty destinations, the exact aggregate and
// per-window report of the Rust multi-window vector, the full IPv6
// space count, the no-change rerun, the aborted-draft recovery, the
// metadata stage, cancellation, and the invalid request classes. The
// destination feeds are produced through the public projection itself;
// the white-box destination fixture of the Rust tests stays covered by
// the internal slice-B tests (there is no public create_feed workflow
// yet).

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// histCreateSource4 writes one fresh last_seen direct database with the
// given inclusive IPv4 ranges through the public facade.
func histCreateSource4(t *testing.T, ranges [][3]uint32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history-source.iprdb")
	if _, err := Create(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTagLastSeen()); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ranges {
		if _, err := tx.AssignV4(IPv4(r[0]), IPv4(r[1]), r[2]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return path
}

// histCreateSource6 writes one fresh last_seen direct database with one
// inclusive IPv6 range through the public facade.
func histCreateSource6(t *testing.T, fromHi, fromLo, toHi, toLo uint64, value uint32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history-source6.iprdb")
	if _, err := Create(path, AddressFamilyIPv6, ValueKindDirect, StructureKindNone, ValueTagLastSeen()); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV6(IPv6{Hi: fromHi, Lo: fromLo}, IPv6{Hi: toHi, Lo: toLo}, value); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return path
}

// histCreateMembership writes one fresh empty IPv4 membership database
// through the public facade (Rust destination ValueTag::new(b"feeds")).
func histCreateMembership(t *testing.T) string {
	t.Helper()
	tag, err := NewValueTag([]byte("feeds"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "history-dest.iprdb")
	if _, err := Create(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag); err != nil {
		t.Fatal(err)
	}
	return path
}

// histSource opens one immutable source reader for a projection.
func histSource(t *testing.T, path string) *ImmutableReader {
	t.Helper()
	source, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// histCount converts one cardinality for assertions (test vectors stay
// far below 2^64 except the explicit full-space test).
func histCount(t *testing.T, c Cardinality129) uint64 {
	t.Helper()
	count, err := c.Uint64()
	if err != nil {
		t.Fatal(err)
	}
	return count
}

// windows3 is the Rust one_source_pass window set.
func windows3() []HistoryWindow {
	return []HistoryWindow{
		{FeedName: "one", Cutoff: 9},
		{FeedName: "two", Cutoff: 10},
		{FeedName: "three", Cutoff: 11},
	}
}

// ranges1000 is the Rust one_source_pass source vector: 1000 single
// points with last_seen 10 + index%3.
func ranges1000() [][3]uint32 {
	ranges := make([][3]uint32, 1000)
	for index := 0; index < 1000; index++ {
		ranges[index] = [3]uint32{uint32(index * 2), uint32(index * 2), 10 + uint32(index%3)}
	}
	return ranges
}

// TestPublicProjectHistoryCreatesFeedsAndCommits runs the Rust
// multi-window vector through the public facade: three feeds created on
// an empty destination with the exact per-window and aggregate counts,
// then the commit, the committed reader evidence, and the no-change
// rerun with the Rust Abort-on-clean parity.
func TestPublicProjectHistoryCreatesFeedsAndCommits(t *testing.T) {
	sourcePath := histCreateSource4(t, ranges1000())
	destinationPath := histCreateMembership(t)

	w, err := OpenWriter(destinationPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	source := histSource(t, sourcePath)
	defer source.Close()

	handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows3(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !handle.IsChanged() {
		t.Fatal("projection of 1000 new addresses is not changed")
	}
	report := handle.Report()
	if report.LogicalChange != LogicalChanged {
		t.Fatalf("logical change = %d, want changed", report.LogicalChange)
	}
	if report.SourceRangeCount != 1000 || histCount(t, report.SourceAddresses) != 1000 {
		t.Fatalf("source = %d ranges, %d addresses, want 1000/1000", report.SourceRangeCount, histCount(t, report.SourceAddresses))
	}
	if report.CreatedFeedCount != 3 {
		t.Fatalf("created feed count = %d, want 3", report.CreatedFeedCount)
	}
	if report.BeforeIntervalCount != 0 || histCount(t, report.BeforeAddresses) != 0 {
		t.Fatalf("before = %d intervals, %d addresses, want empty destination", report.BeforeIntervalCount, histCount(t, report.BeforeAddresses))
	}
	// The aggregate is the union of every window feed: every address is
	// in at least feed "one", so the aggregate after coverage is the
	// full source (Rust HistoryPolicy aggregate parity, the same
	// semantics the slice-B vector pins at 30).
	if report.AfterIntervalCount != 1000 || histCount(t, report.AfterAddresses) != 1000 {
		t.Fatalf("after = %d intervals, %d addresses, want 1000/1000", report.AfterIntervalCount, histCount(t, report.AfterAddresses))
	}
	if histCount(t, report.UnchangedAddresses) != 0 || histCount(t, report.RemovedAddresses) != 0 {
		t.Fatalf("unchanged = %d removed = %d, want 0/0", histCount(t, report.UnchangedAddresses), histCount(t, report.RemovedAddresses))
	}
	if histCount(t, report.AddedAddresses) != 1000 {
		t.Fatalf("added = %d, want 1000", histCount(t, report.AddedAddresses))
	}
	if len(report.Windows) != 3 {
		t.Fatalf("window reports = %d, want 3", len(report.Windows))
	}
	// last_seen 10+index%3 with exclusive cutoffs (cutoff < value):
	// index%3=0 gives 10, =1 gives 11, =2 gives 12, so cutoff 9 keeps
	// all 1000, cutoff 10 keeps the 666 values 11/12, cutoff 11 keeps
	// the 333 values 12 only.
	expected := []struct {
		name      string
		cutoff    uint32
		intervals uint64
		addresses uint64
	}{
		{name: "one", cutoff: 9, intervals: 1000, addresses: 1000},
		{name: "two", cutoff: 10, intervals: 666, addresses: 666},
		{name: "three", cutoff: 11, intervals: 333, addresses: 333},
	}
	for index, want := range expected {
		window := report.Windows[index]
		if window.FeedName != want.name || window.Cutoff != want.cutoff || !window.Created {
			t.Fatalf("window %d = %q cutoff %d created %v, want %q/%d/true", index, window.FeedName, window.Cutoff, window.Created, want.name, want.cutoff)
		}
		if window.BeforeIntervalCount != 0 || histCount(t, window.BeforeAddresses) != 0 {
			t.Fatalf("window %s before = %d/%d, want empty", want.name, window.BeforeIntervalCount, histCount(t, window.BeforeAddresses))
		}
		if window.AfterIntervalCount != want.intervals || histCount(t, window.AfterAddresses) != want.addresses {
			t.Fatalf("window %s after = %d intervals / %d addresses, want %d/%d", want.name, window.AfterIntervalCount, histCount(t, window.AfterAddresses), want.intervals, want.addresses)
		}
		if histCount(t, window.AddedAddresses) != want.addresses || histCount(t, window.RemovedAddresses) != 0 || histCount(t, window.UnchangedAddresses) != 0 {
			t.Fatalf("window %s adds = %d removes = %d unchanged = %d, want %d/0/0", want.name, histCount(t, window.AddedAddresses), histCount(t, window.RemovedAddresses), histCount(t, window.UnchangedAddresses), want.addresses)
		}
	}

	res, err := handle.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != CommitCommitted || res.TransactionID != 2 || res.Err != nil {
		t.Fatalf("commit = %+v err %v, want committed txn 2", res, err)
	}

	// The committed destination carries the three feeds (close the
	// writer first: immutable opens wait on the writer's exclusive
	// lifetime lock).
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two", "three"} {
		if _, found, err := r.LookupFeed(name); err != nil || !found {
			t.Fatalf("feed %q after commit: found %v err %v", name, found, err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	// its clean handle reports NoPendingTransaction at Abort (Rust
	// FinishedHistoryProjection::abort parity).
	reopened, err := OpenWriter(destinationPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	source2 := histSource(t, sourcePath)
	defer source2.Close()
	rerun, err := reopened.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source2}, windows3(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rerun.IsChanged() {
		t.Fatal("identical rerun is changed")
	}
	if rerun.Report().LogicalChange != LogicalNoChange {
		t.Fatalf("rerun logical change = %d, want no change", rerun.Report().LogicalChange)
	}
	if err := rerun.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("rerun abort err = %v, want ErrorNoPendingTransaction", err)
	}
	if err := reopened.core.Healthy(); err != nil {
		t.Fatalf("writer unhealthy after the no-change rerun: %v", err)
	}
}

// TestPublicProjectHistoryFullIPv6Space verifies one `::/0` source
// range counts as 2^128 addresses (format.IPv6Inclusive parity).
func TestPublicProjectHistoryFullIPv6Space(t *testing.T) {
	sourcePath := histCreateSource6(t, 0, 0, ^uint64(0), ^uint64(0), 7)
	tag, err := NewValueTag([]byte("feeds"))
	if err != nil {
		t.Fatal(err)
	}
	destinationPath := filepath.Join(t.TempDir(), "history-dest6.iprdb")
	if _, err := Create(destinationPath, AddressFamilyIPv6, ValueKindMembership, StructureKindNone, tag); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(destinationPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	source := histSource(t, sourcePath)
	defer source.Close()

	handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, []HistoryWindow{{FeedName: "all", Cutoff: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	report := handle.Report()
	if report.SourceRangeCount != 1 || report.SourceAddresses.Compare(format.FullIPv6Space()) != 0 {
		t.Fatalf("source = %d ranges, %s addresses, want 1 and 2^128", report.SourceRangeCount, report.SourceAddresses.String())
	}
	full := format.FullIPv6Space()
	if report.Windows[0].AfterAddresses.Compare(full) != 0 || report.AfterAddresses.Compare(full) != 0 {
		t.Fatalf("after addresses = %s / %s, want 2^128", report.Windows[0].AfterAddresses.String(), report.AfterAddresses.String())
	}
	if err := handle.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestPublicProjectHistoryAbortedDraftRecovery verifies an aborted
// changed projection leaves the writer healthy for a fresh projection
// and commit.
func TestPublicProjectHistoryAbortedDraftRecovery(t *testing.T) {
	sourcePath := histCreateSource4(t, [][3]uint32{{0, 9, 10}, {10, 19, 20}})
	destinationPath := histCreateMembership(t)
	w, err := OpenWriter(destinationPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	source := histSource(t, sourcePath)
	handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows3(), nil)
	if err != nil {
		t.Fatal(err)
	}
	source.Close()
	if !handle.IsChanged() {
		t.Fatal("projection is not changed")
	}
	if err := handle.Abort(); err != nil {
		t.Fatal(err)
	}
	// The spent handle reports the draftless commit class (Rust
	// commit_attempt parity: the draft was discarded by Abort).
	if _, err := handle.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("commit after abort = %v, want no pending transaction", err)
	}
	if err := handle.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after abort = %v, want no pending transaction", err)
	}
	if err := w.core.Healthy(); err != nil || w.core.HasDraft() {
		t.Fatalf("writer not healthy and draft-free after abort: %v", err)
	}

	// The same projection succeeds again and commits.
	source2 := histSource(t, sourcePath)
	defer source2.Close()
	again, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source2}, windows3(), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := again.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != CommitCommitted {
		t.Fatalf("commit status = %v, want committed (err %v)", res.Status, res.Err)
	}
}

// TestPublicProjectHistoryMetadataStage stages and clears metadata on
// the changed handle and verifies the committed metadata through the
// public reader (Rust PreparedHistoryProjection set/clear parity).
func TestPublicProjectHistoryMetadataStage(t *testing.T) {
	sourcePath := histCreateSource4(t, [][3]uint32{{5, 5, 1}})
	destinationPath := histCreateMembership(t)
	w, err := OpenWriter(destinationPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	source := histSource(t, sourcePath)
	defer source.Close()
	handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, []HistoryWindow{{FeedName: "seen", Cutoff: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// An absent destination reports false for a clear stage (Rust
	// stage_clear_metadata_json no-op), then one set stage. A second
	// set in the same transaction is refused; because a draft exists,
	// Rust abort_on_error aborts it and the returned class is
	// TransactionAborted (the handle is spent, the writer stays
	// healthy for a fresh projection).
	if changed, err := handle.ClearMetadataJSON(); err != nil || changed {
		t.Fatalf("clear on absent metadata = changed %v err %v, want false no-op", changed, err)
	}
	meta := []byte(`{"history":"projected"}`)
	if changed, err := handle.SetMetadataJSON(meta); err != nil || !changed {
		t.Fatalf("set metadata = changed %v err %v", changed, err)
	}
	if _, err := handle.SetMetadataJSON([]byte(`{"again":true}`)); !isPubCode(err, ErrorTransactionAborted) {
		t.Fatalf("second set err = %v, want ErrorTransactionAborted (single stage)", err)
	}
	if err := w.core.Healthy(); err != nil || w.core.HasDraft() {
		t.Fatalf("writer not healthy and draft-free after the refused stage: %v", err)
	}

	// A fresh projection stages the metadata and commits it (the
	// public reader sees the committed value after the writer closes).
	fresh, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, []HistoryWindow{{FeedName: "seen", Cutoff: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := fresh.SetMetadataJSON(meta); err != nil || !changed {
		t.Fatalf("fresh set metadata = changed %v err %v", changed, err)
	}
	res, err := fresh.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("commit = %+v err %v", res, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, present, err := r.MetadataJSON()
	if err != nil || !present || string(got) != string(meta) {
		t.Fatalf("metadata = %q present %v err %v", got, present, err)
	}
}

// TestPublicProjectHistoryInvalidRequests pins the request
// classification order of the Rust feed-workflow and source
// preconditions, plus the live-source refusal and the empty-window
// count gate.
func TestPublicProjectHistoryInvalidRequests(t *testing.T) {
	sourcePath := histCreateSource4(t, [][3]uint32{{0, 0, 1}})
	destinationPath := histCreateMembership(t)
	w, err := OpenWriter(destinationPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	source := histSource(t, sourcePath)
	defer source.Close()

	// Live source refusal happens at the API boundary.
	_, liveErr := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Reader: source}, windows3(), nil)
	var public *Error
	if !errors.As(liveErr, &public) || public.Code != ErrorOSUnsupported {
		t.Fatalf("live source err = %v, want public ErrorOSUnsupported", liveErr)
	}

	// Invalid enum and nil reader.
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: 99}, windows3(), nil); !isPubCode(err, ErrorInvalidArgument) {
		t.Fatalf("invalid kind err = %v, want ErrorInvalidArgument", err)
	}
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable}, windows3(), nil); !isPubCode(err, ErrorInvalidArgument) {
		t.Fatalf("nil reader err = %v, want ErrorInvalidArgument", err)
	}

	// Empty window count is rejected before any source work.
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, nil, nil); !isPubCode(err, ErrorInvalidArgument) {
		t.Fatalf("empty windows err = %v, want ErrorInvalidArgument", err)
	}

	// Wrong source value kind: a second membership database (never the
	// writer-locked destination) as the source.
	membershipSource := histSource(t, histCreateMembership(t))
	defer membershipSource.Close()
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: membershipSource}, windows3(), nil); !isPubCode(err, ErrorWrongValueKind) {
		t.Fatalf("membership source err = %v, want ErrorWrongValueKind", err)
	}

	// Wrong source semantic: a generic direct tag is not last_seen.
	genericPath := filepath.Join(t.TempDir(), "generic.iprdb")
	if _, err := Create(genericPath, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTag{}); err != nil {
		t.Fatal(err)
	}
	generic, err := OpenImmutable(genericPath)
	if err != nil {
		t.Fatal(err)
	}
	defer generic.Close()
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: generic}, windows3(), nil); !isPubCode(err, ErrorWrongValueTag) {
		t.Fatalf("generic source err = %v, want ErrorWrongValueTag", err)
	}

	// Wrong destination value kind: a direct writer (on a separate
	// file, never the reader-locked source path) refuses the named
	// feed workflow.
	directWriter, err := OpenWriter(histCreateSource4(t, [][3]uint32{{0, 0, 1}}), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer directWriter.Close()
	if _, err := directWriter.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows3(), nil); !isPubCode(err, ErrorWrongValueKind) {
		t.Fatalf("direct destination err = %v, want ErrorWrongValueKind", err)
	}

	// Wrong source family.
	source6 := histCreateSource6(t, 0, 0, 1, 1, 1)
	six, err := OpenImmutable(source6)
	if err != nil {
		t.Fatal(err)
	}
	defer six.Close()
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: six}, windows3(), nil); !isPubCode(err, ErrorWrongAddressFamily) {
		t.Fatalf("v6 source err = %v, want ErrorWrongAddressFamily", err)
	}

	// A pending transaction on the membership writer refuses the
	// workflow (Rust require_feed_workflow_ready).
	if err := w.core.BeginDraft(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows3(), nil); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("pending draft err = %v, want ErrorWrongState", err)
	}
	if err := w.core.DiscardUnpublished(); err != nil {
		t.Fatal(err)
	}

	// A closed writer refuses with ErrorWrongState.
	closed, err := OpenWriter(histCreateMembership(t), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows3(), nil); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("closed writer err = %v, want ErrorWrongState", err)
	}
}

// TestPublicProjectHistoryCancellation verifies the pre-draft
// cancellation check reports ErrorCancelled (Rust project_history_state
// cancellation.check before start_feed_workflow_draft).
func TestPublicProjectHistoryCancellation(t *testing.T) {
	sourcePath := histCreateSource4(t, [][3]uint32{{0, 0, 1}})
	destinationPath := histCreateMembership(t)
	w, err := OpenWriter(destinationPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	source := histSource(t, sourcePath)
	defer source.Close()

	token := NewCancellationToken()
	token.Cancel()
	_, err = w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows3(), token)
	var public *Error
	if !errors.As(err, &public) || public.Code != ErrorCancelled {
		t.Fatalf("cancelled err = %v, want ErrorCancelled", err)
	}
	if err := w.core.Healthy(); err != nil || w.core.HasDraft() {
		t.Fatalf("cancellation before the draft left the writer dirty: %v", err)
	}
}

// TestPublicProjectHistoryUnrelatedFeedsSurviveRerun mirrors the Rust
// multi-window preserving behavior at the public boundary: a second
// projection with one extra window keeps the first window's committed
// coverage, and the identical third rerun is a no change.
func TestPublicProjectHistoryUnrelatedFeedsSurviveRerun(t *testing.T) {
	sourcePath := histCreateSource4(t, [][3]uint32{{0, 9, 10}, {10, 19, 20}, {20, 29, 30}, {40, 49, 20}})
	destinationPath := histCreateMembership(t)
	w, err := OpenWriter(destinationPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	firstSource := histSource(t, sourcePath)
	first, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: firstSource}, []HistoryWindow{{FeedName: "recent", Cutoff: 15}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstSource.Close()
	if got := histCount(t, first.Report().Windows[0].AfterAddresses); got != 30 {
		t.Fatalf("first recent after = %d, want 30", got)
	}
	if res, err := first.Commit(); err != nil || res.Status != CommitCommitted {
		t.Fatalf("first commit = %+v err %v", res, err)
	}

	secondSource := histSource(t, sourcePath)
	second, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: secondSource}, []HistoryWindow{{FeedName: "recent", Cutoff: 15}, {FeedName: "very-recent", Cutoff: 25}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondSource.Close()
	if !second.IsChanged() {
		t.Fatal("second projection with a new window is not changed")
	}
	recent := second.Report().Windows[0]
	if recent.FeedName != "recent" || histCount(t, recent.AfterAddresses) != 30 || histCount(t, recent.UnchangedAddresses) != 30 {
		t.Fatalf("rerun recent = %d after, %d unchanged, want 30/30 preserved", histCount(t, recent.AfterAddresses), histCount(t, recent.UnchangedAddresses))
	}
	veryRecent := second.Report().Windows[1]
	if veryRecent.FeedName != "very-recent" || histCount(t, veryRecent.AfterAddresses) != 10 {
		t.Fatalf("very-recent after = %d, want 10 (only the last_seen 30 range)", histCount(t, veryRecent.AfterAddresses))
	}
	if res, err := second.Commit(); err != nil || res.Status != CommitCommitted {
		t.Fatalf("second commit = %+v err %v", res, err)
	}

	thirdSource := histSource(t, sourcePath)
	defer thirdSource.Close()
	third, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: thirdSource}, []HistoryWindow{{FeedName: "recent", Cutoff: 15}, {FeedName: "very-recent", Cutoff: 25}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if third.IsChanged() {
		t.Fatal("identical rerun after two commits is changed")
	}
	if err := third.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("third abort err = %v, want ErrorNoPendingTransaction", err)
	}

	// The destination still carries the unrelated recent coverage plus
	// the newer very-recent coverage, verifiable through the reader
	// (close the writer first: immutable opens wait on the exclusive
	// lifetime lock).
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, name := range []string{"recent", "very-recent"} {
		if _, found, err := r.LookupFeed(name); err != nil || !found {
			t.Fatalf("feed %q after reruns: found %v err %v", name, found, err)
		}
	}
}
