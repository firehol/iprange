// Live history projection source tests (Rust
// live_writer/history_projection/tests.rs HistoryProjectionSource::Live
// parity): the live arm binds the registered live reader core through
// its open-state check, runs the same require_compatible_source checks
// as the immutable arm, and drives the same projection machine. The
// source writer closes before the live reader opens because the reader
// claims the shared main lifetime lock.

package iprangedb

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// pubCode resolves the public error code from either the public Error
// type (publicError conversion) or the internal format error, matching
// the lifecycle test helper.
func pubCode(err error) ErrorCode {
	var fe *format.Error
	if errors.As(err, &fe) {
		return ErrorCode(fe.Code)
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return 0
}

// histCreateLiveSource4 writes one fresh last_seen direct live database
// with the given inclusive IPv4 ranges through the public facade. The
// public writer holds the exclusive main lifetime lock, so it closes
// before returning and the live reader can claim the shared lock.
func histCreateLiveSource4(t *testing.T, ranges [][3]uint32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history-live-source.iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTagLastSeen(), 2, nil); err != nil {
		t.Fatal(err)
	}
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect(nil)
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
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPublicProjectHistoryLiveSourceCreatesFeedsAndCommits runs the Rust
// multi-window vector through the live source arm: a registered live
// reader pins the source generation, the projection creates the three
// feeds with the exact before/after counts, the commit publishes them,
// and the identical rerun reports no change with the Rust
// NoPendingTransaction abort parity.
func TestPublicProjectHistoryLiveSourceCreatesFeedsAndCommits(t *testing.T) {
	requireLiveCreation(t)
	sourcePath := histCreateLiveSource4(t, ranges1000())
	destinationPath := histCreateMembership(t)

	w, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	source, err := OpenLiveReader(sourcePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: source}, windows3(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !handle.IsChanged() {
		t.Fatal("live projection of 1000 new addresses is not changed")
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
	if report.AfterIntervalCount != 1000 || histCount(t, report.AfterAddresses) != 1000 {
		t.Fatalf("after = %d intervals, %d addresses, want 1000/1000", report.AfterIntervalCount, histCount(t, report.AfterAddresses))
	}
	if histCount(t, report.AddedAddresses) != 1000 {
		t.Fatalf("added = %d, want 1000", histCount(t, report.AddedAddresses))
	}
	if len(report.Windows) != 3 {
		t.Fatalf("window reports = %d, want 3", len(report.Windows))
	}
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
	// writer first: live readers wait on the writer's exclusive
	// lifetime lock).
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenLiveReader(destinationPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two", "three"} {
		if _, found, err := r.LookupFeed(name); err != nil || !found {
			t.Fatalf("feed %q after live commit: found %v err %v", name, found, err)
		}
	}
	if _, err := r.Close(); err != nil {
		t.Fatal(err)
	}

	// The identical live rerun reports no change and the clean handle
	// reports NoPendingTransaction at Abort (Rust parity).
	reopened, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	source2, err := OpenLiveReader(sourcePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer source2.Close()
	rerun, err := reopened.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: source2}, windows3(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rerun.IsChanged() {
		t.Fatal("identical live rerun is changed")
	}
	if rerun.Report().LogicalChange != LogicalNoChange {
		t.Fatalf("rerun logical change = %d, want no change", rerun.Report().LogicalChange)
	}
	if err := rerun.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("rerun abort err = %v, want ErrorNoPendingTransaction", err)
	}
	if err := reopened.coreOf().Healthy(); err != nil {
		t.Fatalf("writer unhealthy after the live no-change rerun: %v", err)
	}
}

// TestPublicProjectHistoryLiveSourceMatchesImmutableProjects two
// identical 1000-range live source generations into two separate
// destinations and requires byte-identical reports: the live arm binds
// the source generation, and both independent projections of the same
// vector report exactly alike.
func TestPublicProjectHistoryLiveSourceMatchesImmutable(t *testing.T) {
	requireLiveCreation(t)
	ranges := ranges1000()

	firstSourcePath := histCreateSource4(t, ranges)
	secondSourcePath := histCreateLiveSource4(t, ranges)

	firstDest := histCreateMembership(t)
	secondDest := histCreateMembership(t)

	w1, err := OpenLiveWriter(firstDest, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w1.Close()
	w2, err := OpenLiveWriter(secondDest, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	firstSource := histSource(t, firstSourcePath)
	defer firstSource.Close()
	secondSource, err := OpenLiveReader(secondSourcePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer secondSource.Close()

	firstHandle, err := w1.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: firstSource}, windows3(), nil)
	if err != nil {
		t.Fatal(err)
	}
	secondHandle, err := w2.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: secondSource}, windows3(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if firstHandle.IsChanged() != secondHandle.IsChanged() {
		t.Fatal("change flags differ between the identical projections")
	}
	if !reflect.DeepEqual(firstHandle.Report(), secondHandle.Report()) {
		t.Fatalf("second report %+v differs from the first report %+v", secondHandle.Report(), firstHandle.Report())
	}
	// The first terminal commits; the second terminal aborts. Both
	// leave their destination clean, exactly like the Rust tests.
	res, err := firstHandle.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != CommitCommitted || res.Err != nil {
		t.Fatalf("parity commit = %+v err %v, want committed", res, err)
	}
	if err := secondHandle.Abort(); err != nil {
		t.Fatalf("parity abort: %v", err)
	}
}

// TestPublicProjectHistoryLiveSourceValidation pins the live arm's
// require_compatible_source checks and the bound-core open check.
func TestPublicProjectHistoryLiveSourceValidation(t *testing.T) {
	requireLiveCreation(t)
	destinationPath := histCreateMembership(t)
	w, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// A nil live reader is an invalid source.
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive}, windows3(), nil); pubCode(err) != ErrorInvalidArgument {
		t.Fatalf("nil live source err = %v, want ErrorInvalidArgument", err)
	}

	// A closed live reader refuses the core bind with WrongState (Rust
	// LiveReader::core -> require_open).
	closedPath := histCreateLiveSource4(t, [][3]uint32{{1, 1, 10}})
	closedSource, err := OpenLiveReader(closedPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := closedSource.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: closedSource}, windows3(), nil); pubCode(err) != ErrorWrongState {
		t.Fatalf("closed live source err = %v, want ErrorWrongState", err)
	}

	// A non-direct live source refuses with WrongValueKind.
	nonDirect := filepath.Join(t.TempDir(), "history-live-membership-source.iprdb")
	tag, err := NewValueTag([]byte("feeds"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateLive(nonDirect, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag, 2, nil); err != nil {
		t.Fatal(err)
	}
	nonDirectSource, err := OpenLiveReader(nonDirect, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer nonDirectSource.Close()
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: nonDirectSource}, windows3(), nil); pubCode(err) != ErrorWrongValueKind {
		t.Fatalf("non-direct live source err = %v, want ErrorWrongValueKind", err)
	}

	// A direct live source without the last_seen semantic refuses with
	// WrongValueTag.
	notLastSeen := filepath.Join(t.TempDir(), "history-live-notlastseen.iprdb")
	plainTag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateLive(notLastSeen, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, plainTag, 2, nil); err != nil {
		t.Fatal(err)
	}
	notLastSeenSource, err := OpenLiveReader(notLastSeen, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer notLastSeenSource.Close()
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: notLastSeenSource}, windows3(), nil); pubCode(err) != ErrorWrongValueTag {
		t.Fatalf("not-last-seen live source err = %v, want ErrorWrongValueTag", err)
	}

	// A live source of the other address family refuses with
	// WrongAddressFamily.
	otherFamily := filepath.Join(t.TempDir(), "history-live-v6-source.iprdb")
	if _, err := CreateLive(otherFamily, AddressFamilyIPv6, ValueKindDirect, StructureKindNone, ValueTagLastSeen(), 2, nil); err != nil {
		t.Fatal(err)
	}
	otherFamilySource, err := OpenLiveReader(otherFamily, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer otherFamilySource.Close()
	if _, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: otherFamilySource}, windows3(), nil); pubCode(err) != ErrorWrongAddressFamily {
		t.Fatalf("family-mismatched live source err = %v, want ErrorWrongAddressFamily", err)
	}
}
