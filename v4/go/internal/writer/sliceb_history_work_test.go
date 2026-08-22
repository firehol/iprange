//go:build v4work

// Necessary-work pins for the slice-B history surface (SOW-0025 chunk
// 3b-4), mirroring the Rust work::measure vectors: one ordered
// projection merge opens exactly one selected-store cursor, consumes
// every committed base record exactly once, and tests every window on
// every emitted segment; the membership workflow finish spills each
// buffered refcount delta into the operation-private delta tree exactly
// once. The destination fixture is frozen, so the exact counter deltas
// below are stable; a change that adds or removes hot-path work is
// visible.

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// TestWorkHistoryProjectionMergePins pins the merge scan of the Rust
// multi-window vector: the base cursor charges one source pass, each of
// the four committed base records charges one range consume, and the
// merge emits eight segments (the four input ranges split against the
// four base records), each testing all three windows.
func TestWorkHistoryProjectionMergePins(t *testing.T) {
	sourcePath := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, lastSeenTag)
	destinationPath := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, format.StructureKindNone, feedsTag)
	commitDirectRangesV4(t, sourcePath, [][3]uint32{
		{0, 9, 10}, {10, 19, 20}, {20, 29, 30}, {40, 49, 20},
	})
	setupDestinationFeeds(t, destinationPath)

	c, err := Open(destinationPath, historyBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	windows := []historyWindow{
		{feedName: "recent", cutoff: 15},
		{feedName: "very-recent", cutoff: 25},
		{feedName: "future", cutoff: 30},
	}
	var plan *historyPlan
	if err := c.Mutate(func(edit *WriterEdit) error {
		var err error
		plan, err = edit.PrepareHistoryFrom(windows, nilCheck)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	work.Reset()
	var merge *historyMerge
	if err := c.Mutate(func(edit *WriterEdit) error {
		var err error
		merge, err = edit.BeginHistory(plan, nilCheck)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ranges := [][3]uint32{{0, 9, 10}, {10, 19, 20}, {20, 29, 30}, {40, 49, 20}}
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
	if err := c.Mutate(func(edit *WriterEdit) error {
		_, err := edit.FinishHistory(merge, rangeCount, addresses, nilCheck)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	snap := work.Read()
	if snap.SourcePasses != 1 {
		t.Fatalf("history merge source passes = %d, want 1", snap.SourcePasses)
	}
	if snap.RangesConsumed != 4 {
		t.Fatalf("history merge ranges consumed = %d, want 4", snap.RangesConsumed)
	}
	if snap.HistoryWindowTests != 24 {
		t.Fatalf("history window tests = %d, want 24 (8 segments x 3 windows)", snap.HistoryWindowTests)
	}
	if err := c.DiscardUnpublished(); err != nil {
		t.Fatal(err)
	}
}

// TestWorkMembershipWorkflowSpillsEachDeltaOnce pins the refcount delta
// path of one membership workflow: the two-slot pending buffer spills
// the oldest slot per overflow and the finish flushes the remainder,
// each spill charging exactly one delta-tree write (Rust
// membership_delta.rs track).
func TestWorkMembershipWorkflowSpillsEachDeltaOnce(t *testing.T) {
	path := makeEmptyDBPagesKind(t, 64, format.AddressFamilyIPv4)
	raw := make([]byte, 64*format.PageSize)
	for i := uint64(0); i < 2; i++ {
		page := raw[i*format.PageSize : (i+1)*format.PageSize]
		copy(page, format.MainMagic[:])
		putMetaFieldsForTest(page, 64)
		page[12] = format.ValueKindMembership
		format.PutU64(page[112:120], 1) // MembershipIDLimit
		format.PutU32(page[252:256], format.MetaCRC32C(page))
	}
	if err := osWriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	_, store, _ := openDraftStore(t, path, budget, [16]byte{8})

	// The whole measured flow: every buffered spill and the finish
	// flush charge exactly one delta-tree write.
	work.Reset()
	unrelated, _, err := store.ensureFeed("unrelated")
	if err != nil {
		t.Fatal(err)
	}
	recent, _, err := store.ensureFeed("recent")
	if err != nil {
		t.Fatal(err)
	}
	veryRecent, _, err := store.ensureFeed("very-recent")
	if err != nil {
		t.Fatal(err)
	}
	unrelatedBit, err := store.internMembership(singleBitWords{feedIndex: unrelated.index})
	if err != nil {
		t.Fatal(err)
	}
	recentBit, err := store.internMembership(singleBitWords{feedIndex: recent.index})
	if err != nil {
		t.Fatal(err)
	}
	veryRecentBit, err := store.internMembership(singleBitWords{feedIndex: veryRecent.index})
	if err != nil {
		t.Fatal(err)
	}
	combined, present, err := store.combineMemberships(recentBit.id, unrelatedBit.id, unrelatedBit.wordCount, membershipUnion)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("empty combined feed bitmap")
	}

	// Four interning records (three single-bit feeds plus the union
	// bitmap) fill the buffer before the four range records account
	// their refcounts: four spills during buffering plus the two-slot
	// finish flush.
	if _, err := store.AssignV4(0, 4, recentBit.id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignV4(5, 15, combined); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignV4(16, 19, recentBit.id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignV4(25, 35, veryRecentBit.id); err != nil {
		t.Fatal(err)
	}

	if err := store.finishMembershipDeltasWithCheckpoint(nilCheck); err != nil {
		t.Fatal(err)
	}
	snap := work.Read()
	if snap.MembershipDeltaSpills != 6 {
		t.Fatalf("membership delta spills = %d, want 6", snap.MembershipDeltaSpills)
	}
}
