// Slice B tests (SOW-0025 chunk 3b-4): the ordered merge, the history
// plan/merge/policy, and the Draft/Core workflow state machine,
// mirroring the Rust history_projection integration vectors at the
// internal WriterEdit level. Destination feeds are built white-box
// through the draft feed catalog and membership interning (recorded
// chunk-3b-4 decision 3); the public projection facade is slice C.

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// lastSeenTag and feedsTag are the value tags of the Rust history test
// fixtures (ValueTag::LAST_SEEN and ValueTag::new(b"feeds")).
var (
	lastSeenTag = [16]byte{'l', 'a', 's', 't', '-', 's', 'e', 'e', 'n'}
	feedsTag    = [16]byte{'f', 'e', 'e', 'd', 's'}
)

func historyBudget() PageBudget {
	return PageBudget{MaxHeapBytes: 4 * 1024 * 1024, MaxPrivatePages: 20000, MaxGrowthPages: 20000}
}

// createValueDB writes one fresh database through the production Create
// path and returns its path.
func createValueDB(t *testing.T, family, kind, structure uint8, tag [16]byte) string {
	t.Helper()
	path := t.TempDir() + "/db.iprdb"
	if _, err := Create(path, family, kind, structure, tag, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

// commitDirectRangesV4 publishes one direct draft assigning every range
// (empty base, so assigns replace the empty tree).
func commitDirectRangesV4(t *testing.T, path string, ranges [][3]uint32) {
	t.Helper()
	c, err := Open(path, historyBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginDraft(); err != nil {
		t.Fatal(err)
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	for _, r := range ranges {
		if _, err := store.AssignV4(r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if err := c.RequireDraftLength(); err != nil {
		t.Fatal(err)
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("publish status = %v (%v), want committed", res.Status, res.Err)
	}
}

// singleBitWords is one feed bitmap of exactly one set bit at feedIndex
// (test-owned words; the dictionary reads only caller-owned output).
type singleBitWords struct {
	feedIndex uint32
}

func (w singleBitWords) WordCount() uint32 { return w.feedIndex/64 + 1 }

func (w singleBitWords) ReadChunk(start uint32) (words [membershipChunkWords]uint64, count uint32, err error) {
	count = membershipChunkWords
	if remaining := w.WordCount() - start; count > remaining {
		count = remaining
	}
	word := w.feedIndex / 64
	if word >= start && word-start < count {
		words[word-start] |= uint64(1) << (w.feedIndex % 64)
	}
	return words, count, nil
}

// destinationFeedIDs are the interned membership ids of the white-box
// destination fixture (Rust test feeds unrelated/recent/very-recent plus
// the [5,15] overlap union).
type destinationFeedIDs struct {
	unrelated  uint32
	recent     uint32
	combined   uint32
	veryRecent uint32
}

// setupDestinationFeeds builds the Rust history fixture destination in
// one membership workflow and publishes it: feeds "unrelated" [5,15],
// "recent" [0,19], "very-recent" [25,35], with the [5,15] overlap
// interned as the union bitmap (the white-box equivalent of the future
// create_feed workflow).
func setupDestinationFeeds(t *testing.T, path string) destinationFeedIDs {
	t.Helper()
	c, err := Open(path, historyBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	var ids destinationFeedIDs
	err = c.Mutate(func(edit *WriterEdit) error {
		store := edit.store
		unrelated, _, err := store.ensureFeed("unrelated")
		if err != nil {
			return err
		}
		recent, _, err := store.ensureFeed("recent")
		if err != nil {
			return err
		}
		veryRecent, _, err := store.ensureFeed("very-recent")
		if err != nil {
			return err
		}
		unrelatedBit, err := draftInternMembership(store, singleBitWords{feedIndex: unrelated.index})
		if err != nil {
			return err
		}
		recentBit, err := draftInternMembership(store, singleBitWords{feedIndex: recent.index})
		if err != nil {
			return err
		}
		veryRecentBit, err := draftInternMembership(store, singleBitWords{feedIndex: veryRecent.index})
		if err != nil {
			return err
		}
		combined, present, err := store.combineMemberships(recentBit.id, unrelatedBit.id, unrelatedBit.wordCount, membershipUnion)
		if err != nil {
			return err
		}
		if !present {
			return corrupt("empty combined feed bitmap")
		}
		ids = destinationFeedIDs{
			unrelated:  unrelatedBit.id,
			recent:     recentBit.id,
			combined:   combined,
			veryRecent: veryRecentBit.id,
		}
		// The destination partition in ascending order: the overlap
		// region carries the union bitmap.
		if _, err := store.AssignV4(0, 4, recentBit.id); err != nil {
			return err
		}
		if _, err := store.AssignV4(5, 15, combined); err != nil {
			return err
		}
		if _, err := store.AssignV4(16, 19, recentBit.id); err != nil {
			return err
		}
		if _, err := store.AssignV4(25, 35, veryRecentBit.id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Mutate(func(edit *WriterEdit) error {
		return edit.FinishMembershipWorkflow(nilCheck)
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if err := c.RequireDraftLength(); err != nil {
		t.Fatal(err)
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("publish status = %v (%v), want committed", res.Status, res.Err)
	}
	return ids
}

func nilCheck() error { return nil }

// count129 converts one cardinality to uint64 for assertions (the test
// vectors stay far below 2^64).
func count129(t *testing.T, c format.Cardinality129) uint64 {
	t.Helper()
	count, err := c.Uint64()
	if err != nil {
		t.Fatal(err)
	}
	return count
}

// runProjection drives one internal history projection over the open
// destination workflow and returns the finished report.
func runProjection(t *testing.T, c *Core, windows []HistoryWindow, ranges [][3]uint32) *HistoryProjectionReport {
	t.Helper()
	var plan *historyPlan
	if err := c.Mutate(func(edit *WriterEdit) error {
		var err error
		plan, err = edit.PrepareHistoryFrom(windows, nilCheck)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var merge *historyMerge
	if err := c.Mutate(func(edit *WriterEdit) error {
		var err error
		merge, err = edit.BeginHistory(plan, nilCheck)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var rangeCount uint64
	addresses := format.CardinalityZero()
	for _, r := range ranges {
		from := tree.Key{Hi: uint64(r[0])}
		to := tree.Key{Hi: uint64(r[1])}
		if err := c.Mutate(func(edit *WriterEdit) error {
			return edit.PushHistory(merge, from, to, r[2], nilCheck)
		}); err != nil {
			t.Fatal(err)
		}
		rangeCount++
		size, err := format.IPv4Inclusive(r[0], r[1])
		if err != nil {
			t.Fatal(err)
		}
		addresses, err = addresses.Add(size)
		if err != nil {
			t.Fatal(err)
		}
	}
	var report *HistoryProjectionReport
	if err := c.Mutate(func(edit *WriterEdit) error {
		var err error
		report, err = edit.FinishHistory(merge, rangeCount, addresses, nilCheck)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return report
}

// TestHistoryProjectionMultipleWindowsOncePreservesUnrelatedFeeds is the
// Go mirror of the Rust integration vector of the same name: the exact
// per-window and aggregate counts, the created feed, the committed
// destination coverage, and the no-change rerun.
func TestHistoryProjectionMultipleWindowsOncePreservesUnrelatedFeeds(t *testing.T) {
	sourcePath := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, lastSeenTag)
	destinationPath := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, format.StructureKindNone, feedsTag)
	commitDirectRangesV4(t, sourcePath, [][3]uint32{
		{0, 9, 10}, {10, 19, 20}, {20, 29, 30}, {40, 49, 20},
	})
	ids := setupDestinationFeeds(t, destinationPath)
	if ids.unrelated == 0 || ids.recent == 0 || ids.combined == 0 || ids.veryRecent == 0 {
		t.Fatalf("destination fixture has empty membership ids: %+v", ids)
	}

	c, err := Open(destinationPath, historyBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.BeginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	windows := []HistoryWindow{
		{FeedName: "recent", Cutoff: 15},
		{FeedName: "very-recent", Cutoff: 25},
		{FeedName: "future", Cutoff: 30},
	}
	report := runProjection(t, c, windows, [][3]uint32{
		{0, 9, 10}, {10, 19, 20}, {20, 29, 30}, {40, 49, 20},
	})

	if report.LogicalChange != LogicalChanged {
		t.Fatalf("logical change = %d, want changed", report.LogicalChange)
	}
	if report.SourceRangeCount != 4 {
		t.Fatalf("source range count = %d, want 4", report.SourceRangeCount)
	}
	if got := count129(t, report.SourceAddresses); got != 40 {
		t.Fatalf("source addresses = %d, want 40", got)
	}
	if report.CreatedFeedCount != 1 {
		t.Fatalf("created feed count = %d, want 1", report.CreatedFeedCount)
	}
	if report.BeforeIntervalCount != 2 {
		t.Fatalf("before interval count = %d, want 2", report.BeforeIntervalCount)
	}
	if report.AfterIntervalCount != 2 {
		t.Fatalf("after interval count = %d, want 2", report.AfterIntervalCount)
	}
	if got := count129(t, report.BeforeAddresses); got != 31 {
		t.Fatalf("before addresses = %d, want 31", got)
	}
	if got := count129(t, report.AfterAddresses); got != 30 {
		t.Fatalf("after addresses = %d, want 30", got)
	}
	if got := count129(t, report.UnchangedAddresses); got != 15 {
		t.Fatalf("unchanged addresses = %d, want 15", got)
	}
	if got := count129(t, report.AddedAddresses); got != 15 {
		t.Fatalf("added addresses = %d, want 15", got)
	}
	if got := count129(t, report.RemovedAddresses); got != 16 {
		t.Fatalf("removed addresses = %d, want 16", got)
	}
	if len(report.Windows) != 3 {
		t.Fatalf("window report count = %d, want 3", len(report.Windows))
	}

	recent := report.Windows[0]
	if recent.Created || recent.FeedName != "recent" || recent.Cutoff != 15 {
		t.Fatalf("recent window head = %+v", recent)
	}
	if recent.BeforeIntervalCount != 1 || recent.AfterIntervalCount != 2 {
		t.Fatalf("recent intervals = before %d after %d, want 1 and 2", recent.BeforeIntervalCount, recent.AfterIntervalCount)
	}
	if got := count129(t, recent.BeforeAddresses); got != 20 {
		t.Fatalf("recent before = %d, want 20", got)
	}
	if got := count129(t, recent.AfterAddresses); got != 30 {
		t.Fatalf("recent after = %d, want 30", got)
	}
	if got := count129(t, recent.UnchangedAddresses); got != 10 {
		t.Fatalf("recent unchanged = %d, want 10", got)
	}
	if got := count129(t, recent.AddedAddresses); got != 20 {
		t.Fatalf("recent added = %d, want 20", got)
	}
	if got := count129(t, recent.RemovedAddresses); got != 10 {
		t.Fatalf("recent removed = %d, want 10", got)
	}

	veryRecent := report.Windows[1]
	if veryRecent.Created || veryRecent.FeedName != "very-recent" {
		t.Fatalf("very-recent window head = %+v", veryRecent)
	}
	if got := count129(t, veryRecent.BeforeAddresses); got != 11 {
		t.Fatalf("very-recent before = %d, want 11", got)
	}
	if got := count129(t, veryRecent.AfterAddresses); got != 10 {
		t.Fatalf("very-recent after = %d, want 10", got)
	}
	if got := count129(t, veryRecent.UnchangedAddresses); got != 5 {
		t.Fatalf("very-recent unchanged = %d, want 5", got)
	}
	if got := count129(t, veryRecent.AddedAddresses); got != 5 {
		t.Fatalf("very-recent added = %d, want 5", got)
	}
	if got := count129(t, veryRecent.RemovedAddresses); got != 6 {
		t.Fatalf("very-recent removed = %d, want 6", got)
	}

	future := report.Windows[2]
	if !future.Created || future.FeedName != "future" {
		t.Fatalf("future window head = %+v", future)
	}
	if got := count129(t, future.AfterAddresses); got != 0 {
		t.Fatalf("future after = %d, want 0", got)
	}

	// The history merge retires the committed base range tree when it
	// publishes the merged tree (Rust OrderedMerge::finish).
	if !c.Draft().baseRangeTreeRetired {
		t.Fatal("history merge did not retire the base range tree")
	}

	// Finish the membership workflow and commit.
	if err := c.Mutate(func(edit *WriterEdit) error {
		return edit.FinishMembershipWorkflow(nilCheck)
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if err := c.RequireDraftLength(); err != nil {
		t.Fatal(err)
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("publish status = %v (%v), want committed", res.Status, res.Err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify the committed destination through the immutable reader.
	r, err := reader.OpenImmutable(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	assertFeedV4(t, r, "unrelated", [][2]uint32{{5, 15}})
	assertFeedV4(t, r, "recent", [][2]uint32{{10, 29}, {40, 49}})
	assertFeedV4(t, r, "very-recent", [][2]uint32{{20, 29}})
	assertFeedV4(t, r, "future", nil)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	// Rerunning the same projection over the committed destination is a
	// no change: the rerun discards the draft.
	c, err = Open(destinationPath, historyBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	rerun := runProjection(t, c, windows, [][3]uint32{
		{0, 9, 10}, {10, 19, 20}, {20, 29, 30}, {40, 49, 20},
	})
	if rerun.LogicalChange != LogicalNoChange {
		t.Fatalf("rerun logical change = %d, want no change", rerun.LogicalChange)
	}
	if rerun.CreatedFeedCount != 0 {
		t.Fatalf("rerun created feed count = %d, want 0", rerun.CreatedFeedCount)
	}
	if got := count129(t, rerun.Windows[0].UnchangedAddresses); got != 30 {
		t.Fatalf("rerun recent unchanged = %d, want 30", got)
	}
	if err := c.DiscardUnpublished(); err != nil {
		t.Fatal(err)
	}
	if c.HasDraft() {
		t.Fatal("discard left a draft open")
	}
}

// assertFeedV4 reads one committed feed and compares its coverage with
// the expected inclusive ranges.
func assertFeedV4(t *testing.T, r *reader.ImmutableReader, name string, expected [][2]uint32) {
	t.Helper()
	entry, found, err := r.LookupFeed(name)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("feed %q is missing from the committed catalog", name)
	}
	cursor, err := r.NewFeedRangeProjection4(entry.FeedIndex, reader.RangeForward)
	if err != nil {
		t.Fatal(err)
	}
	var got [][2]uint32
	for {
		next, ok, err := cursor.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, [2]uint32{next.From, next.To})
	}
	if len(got) != len(expected) {
		t.Fatalf("feed %q = %v, want %v", name, got, expected)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("feed %q interval %d = %v, want %v", name, i, got[i], expected[i])
		}
	}
}

// TestHistoryProjectionEmptySourcePreservesUnprojectedFeeds pins the
// no-input merge path: with no source records every projected feed is
// stripped out of the destination, while feed content outside the
// projected windows survives (Rust HistoryPolicy::transform difference
// against the all-windows prefix).
func TestHistoryProjectionEmptySourcePreservesUnprojectedFeeds(t *testing.T) {
	destinationPath := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, format.StructureKindNone, feedsTag)
	setupDestinationFeeds(t, destinationPath)

	c, err := Open(destinationPath, historyBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	report := runProjection(t, c, []HistoryWindow{
		{FeedName: "recent", Cutoff: 15},
	}, nil)
	if report.LogicalChange != LogicalChanged {
		t.Fatalf("logical change = %d, want changed", report.LogicalChange)
	}
	// The aggregate covers only the projected windows: with one
	// "recent" window the aggregate before is exactly its coverage.
	if got := count129(t, report.RemovedAddresses); got != 20 {
		t.Fatalf("removed = %d, want 20 (the recent window coverage)", got)
	}
	if got := count129(t, report.AfterAddresses); got != 0 {
		t.Fatalf("after = %d, want 0", got)
	}
	if err := c.Mutate(func(edit *WriterEdit) error {
		return edit.FinishMembershipWorkflow(nilCheck)
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("publish status = %v (%v), want committed", res.Status, res.Err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := reader.OpenImmutable(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	assertFeedV4(t, r, "recent", nil)
	assertFeedV4(t, r, "unrelated", [][2]uint32{{5, 15}})
	assertFeedV4(t, r, "very-recent", [][2]uint32{{25, 35}})
}

// TestHistoryPlanValidation pins the plan error classes (Rust
// HistoryPlan::prepare_from + require_unique_names + ensure_feeds).
func TestHistoryPlanValidation(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, format.StructureKindNone, feedsTag)
	c, err := Open(path, historyBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}

	if _, err := preparePlanOn(t, c, nil); err == nil {
		t.Fatal("empty windows accepted")
	} else if code := errCode(err); code != format.CodeInvalidArgument {
		t.Fatalf("empty windows code = %d, want InvalidArgument", code)
	}

	if _, err := preparePlanOn(t, c, []HistoryWindow{{FeedName: "same", Cutoff: 1}, {FeedName: "same", Cutoff: 2}}); err == nil {
		t.Fatal("duplicate feed names accepted")
	} else if code := errCode(err); code != format.CodeInvalidArgument {
		t.Fatalf("duplicate names code = %d, want InvalidArgument", code)
	}

	if _, err := preparePlanOn(t, c, []HistoryWindow{{FeedName: "Bad Name", Cutoff: 1}}); err == nil {
		t.Fatal("invalid feed name accepted")
	} else if code := errCode(err); code != format.CodeNameInvalid {
		t.Fatalf("invalid name code = %d, want NameInvalid", code)
	}
}

func preparePlanOn(t *testing.T, c *Core, windows []HistoryWindow) (*historyPlan, error) {
	t.Helper()
	var plan *historyPlan
	err := c.Mutate(func(edit *WriterEdit) error {
		var err error
		plan, err = edit.PrepareHistoryFrom(windows, nilCheck)
		return err
	})
	return plan, err
}

// TestHistoryPlanHeapBudget pins the heap charge: a destination whose
// budget cannot hold the retained plan fails with BudgetExceeded before
// any feed is created.
func TestHistoryPlanHeapBudget(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, format.StructureKindNone, feedsTag)
	budget := PageBudget{MaxHeapBytes: 1, MaxPrivatePages: 20000, MaxGrowthPages: 20000}
	c, err := Open(path, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	if _, err := preparePlanOn(t, c, []HistoryWindow{{FeedName: "recent", Cutoff: 15}}); err == nil {
		t.Fatal("plan prepared under a one-byte heap budget")
	} else if code := errCode(err); code != format.CodeInsufficientResourceBudget {
		t.Fatalf("budget code = %d, want InsufficientResourceBudget", code)
	}
}

// TestOrderedMergeRequiresCanonicalInput pins the merge input contract
// (Rust OrderedMerge::require_input): reversed and out-of-order inputs
// fail closed with the Corrupt class.
func TestOrderedMergeRequiresCanonicalInput(t *testing.T) {
	destinationPath := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, format.StructureKindNone, feedsTag)
	setupDestinationFeeds(t, destinationPath)

	c, err := Open(destinationPath, historyBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	windows := []HistoryWindow{{FeedName: "recent", Cutoff: 15}}
	var plan *historyPlan
	if err := c.Mutate(func(edit *WriterEdit) error {
		var err error
		plan, err = edit.PrepareHistoryFrom(windows, nilCheck)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var merge *historyMerge
	if err := c.Mutate(func(edit *WriterEdit) error {
		var err error
		merge, err = edit.BeginHistory(plan, nilCheck)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	push := func(from, to, value uint32) error {
		return c.Mutate(func(edit *WriterEdit) error {
			return edit.PushHistory(merge, tree.Key{Hi: uint64(from)}, tree.Key{Hi: uint64(to)}, value, nilCheck)
		})
	}
	if err := push(10, 20, 1); err != nil {
		t.Fatal(err)
	}
	if err := push(5, 9, 1); err == nil {
		t.Fatal("out-of-order input accepted")
	} else if code := errCode(err); code != format.CodeFormatInvalid {
		t.Fatalf("out-of-order code = %d, want FormatInvalid", code)
	}
	if err := push(20, 10, 1); err == nil {
		t.Fatal("reversed input accepted")
	} else if code := errCode(err); code != format.CodeFormatInvalid {
		t.Fatalf("reversed code = %d, want FormatInvalid", code)
	}
	// The failed pushes left the draft dirty: discard it.
	if err := c.DiscardUnpublished(); err != nil {
		t.Fatal(err)
	}
}

// TestWorkflowStateMachine pins the Draft/Core workflow gates (Rust
// WriterCore::begin_membership_workflow, workflow_input_open/active,
// operation_is/abandoned, mutate semantics).
func TestWorkflowStateMachine(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, format.StructureKindNone, feedsTag)
	c, err := Open(path, historyBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if c.HasDraft() || c.WorkflowInputOpen() || c.WorkflowActive() || c.DraftChanged() || c.OperationAbandoned() {
		t.Fatal("fresh core reports workflow state")
	}
	if err := c.BeginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	if !c.HasDraft() || !c.WorkflowInputOpen() || !c.WorkflowActive() {
		t.Fatal("membership workflow did not open input state")
	}
	if c.DraftChanged() {
		t.Fatal("fresh membership workflow reports changed")
	}
	nonce := c.Draft().Meta().CommitNonce
	if !c.OperationIs(nonce) {
		t.Fatal("operation_is misses the open draft nonce")
	}
	var other [16]byte
	other[0] = nonce[0] + 1
	if c.OperationIs(other) {
		t.Fatal("operation_is accepts a foreign nonce")
	}
	if err := c.BeginMembershipWorkflow(); err == nil {
		t.Fatal("second workflow accepted over the open one")
	}
	c.AbandonOperation()
	if !c.OperationAbandoned() {
		t.Fatal("abandon_operation did not brand the draft")
	}
	if !c.WorkflowInputOpen() || !c.OperationIs(nonce) {
		t.Fatal("abandon changed the workflow or the operation identity")
	}
	if err := c.DiscardUnpublished(); err != nil {
		t.Fatal(err)
	}
	if c.HasDraft() {
		t.Fatal("discard left a draft open")
	}

	// Mutate on a core without a draft starts a plain transaction (Rust
	// WriterCore::edit), never a workflow.
	if err := c.Mutate(func(edit *WriterEdit) error {
		_, err := edit.store.AssignV4(10, 20, 5)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !c.HasDraft() || !c.DraftChanged() || c.WorkflowActive() {
		t.Fatal("mutate did not open a changed plain transaction")
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("publish status = %v (%v), want committed", res.Status, res.Err)
	}
	if c.HasDraft() || c.WorkflowActive() {
		t.Fatal("commit left workflow state")
	}
}

// TestBeginRangeWorkflowPublishesDirectReplacement pins the direct
// workflow: the draft starts with an empty private range tree, the
// finish retires the committed base tree, and the committed generation
// carries exactly the assigned ranges (Rust WriterCore::begin_range_
// workflow + finish_direct_workflow).
func TestBeginRangeWorkflowPublishesDirectReplacement(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, lastSeenTag)
	commitDirectRangesV4(t, path, [][3]uint32{{0, 9, 1}})

	c, err := Open(path, historyBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginRangeWorkflow(); err != nil {
		t.Fatal(err)
	}
	if !c.WorkflowInputOpen() {
		t.Fatal("range workflow did not open input state")
	}
	if got := c.Draft().Meta().RangeRoot; got != 0 {
		t.Fatalf("range workflow root = %d, want 0", got)
	}
	if err := c.Mutate(func(edit *WriterEdit) error {
		_, err := edit.store.AssignV4(20, 29, 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Mutate(func(edit *WriterEdit) error {
		return edit.FinishDirectWorkflow(nilCheck)
	}); err != nil {
		t.Fatal(err)
	}
	if c.WorkflowInputOpen() {
		t.Fatal("finish_direct_workflow left input open")
	}
	if c.Draft().meta.RetirementRoot == 0 {
		t.Fatal("finish_direct_workflow did not retire the base tree")
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("publish status = %v (%v), want committed", res.Status, res.Err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var visits []reader.RangeVisit4
	if err := r.ScanDirect4(func(visit reader.RangeVisit4) error {
		visits = append(visits, visit)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(visits) != 1 || visits[0] != (reader.RangeVisit4{From: 20, To: 29, Value: 2}) {
		t.Fatalf("committed ranges = %v, want [{20 29 2}]", visits)
	}
}
