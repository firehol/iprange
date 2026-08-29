// Slice A tests (SOW-0025 chunk 3b-5 slice A): the draft feed-merge
// machinery - the empty-map feed trio, the untracked workflow coverage
// input, the feed merge over a committed base, the feed lifecycle
// (rename/remove current feed), and add-feed-to-membership interning.
// Every draft runs over the real opened mapping; no owned page exists
// anywhere.

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// readDraftRangeTree decodes every record of one draft range tree
// through the mapping (the white-box cursor is used to assert the merge
// output without preparing a publication).
func readDraftRangeTree[K any](t *testing.T, store *DraftStore, codec rangeFamily[K], meta format.Meta) []rangeRecord[K] {
	t.Helper()
	cursor, err := newRangeCursor(store, meta, codec, false)
	if err != nil {
		t.Fatal(err)
	}
	var records []rangeRecord[K]
	for {
		record, ok, err := cursor.next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return records
		}
		records = append(records, record)
	}
}

// TestFeedAddToMembershipInterning pins add_feed_to_membership (Rust
// draft_store/membership.rs): the empty base creates the single-bit
// record, a repeat returns the same ID without a new record, and a
// second feed bit combines into a new bitmap.
func TestFeedAddToMembershipInterning(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	draft, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	if err := draft.beginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	alpha, _, err := store.ensureFeed("alpha")
	if err != nil {
		t.Fatal(err)
	}
	beta, _, err := store.ensureFeed("beta")
	if err != nil {
		t.Fatal(err)
	}

	member, err := store.addFeedToMembership(EmptyMembershipHandle(), alpha)
	if err != nil {
		t.Fatal(err)
	}
	if member.isEmpty() || member.wordCount != 1 {
		t.Fatalf("single-bit member = %+v, want nonempty with one word", member)
	}

	again, err := store.addFeedToMembership(EmptyMembershipHandle(), alpha)
	if err != nil {
		t.Fatal(err)
	}
	if again.id != member.id || again.wordCount != member.wordCount {
		t.Fatalf("repeat intern = %+v, want %+v", again, member)
	}

	combined, err := store.addFeedToMembership(member, beta)
	if err != nil {
		t.Fatal(err)
	}
	if combined.id == member.id || combined.wordCount != 1 {
		t.Fatalf("combined = %+v, want a new one-word bitmap", combined)
	}
	var probe [2]byte
	if err := store.selectedMembershipBits(combined.id, []uint32{alpha.Index, beta.Index}, probe[:], nilCheck); err != nil {
		t.Fatal(err)
	}
	if probe[0] != 1 || probe[1] != 1 {
		t.Fatalf("combined bits = %v %v, want both feeds present", probe[0], probe[1])
	}
	if err := store.selectedMembershipBits(member.id, []uint32{alpha.Index, beta.Index}, probe[:], nilCheck); err != nil {
		t.Fatal(err)
	}
	if probe[0] != 1 || probe[1] != 0 {
		t.Fatalf("base bits = %v %v, want only alpha", probe[0], probe[1])
	}
}

// TestInternAddedBitMissingAndChangedReferences pins the corrupt classes
// of intern_added_bit (Rust membership_dictionary.rs): a missing base
// record and a base bitmap whose stored length moved are both refused.
func TestInternAddedBitMissingAndChangedReferences(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	draft, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	if err := draft.beginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	state := store.membershipState()
	if _, err := internAddedBit(store, &state, 99, 1, 3); err == nil {
		t.Fatal("missing base record did not fail")
	} else if code := errCode(err); code != format.CodeFormatInvalid {
		t.Fatalf("missing base code = %d, want FormatInvalid (rust membership_dictionary.rs)", code)
	}

	alpha, _, err := store.ensureFeed("alpha")
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.addFeedToMembership(EmptyMembershipHandle(), alpha)
	if err != nil {
		t.Fatal(err)
	}
	state = store.membershipState()
	if _, err := internAddedBit(store, &state, member.id, member.wordCount+1, 3); err == nil {
		t.Fatal("changed base length did not fail")
	} else if code := errCode(err); code != format.CodeFormatInvalid {
		t.Fatalf("length code = %d, want FormatInvalid (rust membership_dictionary.rs)", code)
	}
	// A base bitmap that already contains the bit returns the base
	// record unchanged.
	reused, err := internAddedBit(store, &state, member.id, member.wordCount, alpha.Index)
	if err != nil {
		t.Fatal(err)
	}
	if reused.id != member.id || reused.wordCount != member.wordCount || reused.created {
		t.Fatalf("reused = %+v, want base %+v without creation", reused, member)
	}
}

// TestFeedCatalogRenameAndRemoveLifecycle pins draft_store/catalog.rs
// rename_current_feed and remove_current_feed: the dual index records
// move together, the used bit is cleared so the hole is reused, and the
// active count follows the deletions.
func TestFeedCatalogRenameAndRemoveLifecycle(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	draft, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	alpha, _, err := store.ensureFeed("alpha")
	if err != nil {
		t.Fatal(err)
	}
	beta, _, err := store.ensureFeed("beta")
	if err != nil {
		t.Fatal(err)
	}
	if draft.meta.ActiveFeedCount != 2 {
		t.Fatalf("active feed count = %d, want 2", draft.meta.ActiveFeedCount)
	}

	renamed, err := store.renameCurrentFeed(alpha, "gamma")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "gamma" || renamed.Index != alpha.Index {
		t.Fatalf("renamed = %+v, want gamma@%d", renamed, alpha.Index)
	}
	if _, found, err := store.lookupFeed("alpha"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("old name still resolves after rename")
	}
	if found, ok, err := store.lookupFeed("gamma"); err != nil {
		t.Fatal(err)
	} else if !ok || found.Index != alpha.Index {
		t.Fatalf("renamed lookup = %+v ok %v, want index %d", found, ok, alpha.Index)
	}

	if _, err := store.renameCurrentFeed(beta, "gamma"); err == nil {
		t.Fatal("rename onto an existing name did not fail")
	} else if code := errCode(err); code != format.CodeNameExists {
		t.Fatalf("rename conflict code = %d, want NameExists (rust feed_catalog.rs)", code)
	}

	if err := store.removeCurrentFeed(beta); err != nil {
		t.Fatal(err)
	}
	if draft.meta.ActiveFeedCount != 1 {
		t.Fatalf("active feed count after remove = %d, want 1", draft.meta.ActiveFeedCount)
	}
	if _, found, err := store.lookupFeed("beta"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("removed feed still resolves")
	}

	delta, created, err := store.ensureFeed("delta")
	if err != nil {
		t.Fatal(err)
	}
	if !created || delta.Index != beta.Index {
		t.Fatalf("reused hole = %+v created %v, want index %d", delta, created, beta.Index)
	}
	if draft.meta.ActiveFeedCount != 2 {
		t.Fatalf("active feed count after reuse = %d, want 2", draft.meta.ActiveFeedCount)
	}
}

// TestFeedMergeEmptyMapCreate pins the empty-map feed trio (Rust
// DraftStore::begin/add/finish_empty_map_feed_ranges): the sealed tree
// carries one constant member record per range, the ordered-prefix
// address count is exact, and the member refcount is charged for every
// record.
// TestFeedMergeEmptyMapSpliceStaysUntracked mirrors the Rust
// private_constant_union_splices_a_large_run_without_per_record_searches
// vector through the empty-map feed: 2,000 ascending single-key
// coverage records, then one large covering range that splices the run
// into a single record through the general path (union run insert). The
// splice insert must not charge membership refcounts (coverage stays
// untracked); the only accounting is the sealed tree record count
// charged by finishEmptyMapFeedRanges.
func TestFeedMergeEmptyMapSpliceStaysUntracked(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	draft, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	if err := draft.beginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	alpha, _, err := store.ensureFeed("alpha")
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.addFeedToMembership(EmptyMembershipHandle(), alpha)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.beginEmptyMapFeed(); err != nil {
		t.Fatal(err)
	}
	input := NewUnionInput(format.AddressFamilyIPv4, format.ValueKindMembership, 1<<20)
	for ordinal := 0; ordinal < 2000; ordinal++ {
		key := uint32(ordinal) * 4
		if err := store.addEmptyMapFeedRange4(key4(key), key4(key+1), member, &input.v4); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.addEmptyMapFeedRange4(key4(0), key4(8000), member, &input.v4); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.finishEmptyMapFeedRanges4(member, &input.v4); err != nil {
		t.Fatal(err)
	}
	if draft.meta.RangeRecordCount != 1 {
		t.Fatalf("spliced tree count = %d, want 1", draft.meta.RangeRecordCount)
	}
	delta := store.draft.membershipDeltaPending.slot(member.id)
	if delta == nil || delta.change != 1 {
		t.Fatalf("membership delta after the splice = %+v, want exactly +1 (the coverage splice must stay untracked)", delta)
	}
	for index := range store.draft.membershipDeltaPending.used {
		if store.draft.membershipDeltaPending.used[index] && store.draft.membershipDeltaPending.slots[index].id != member.id {
			t.Fatalf("a second membership id was charged by the coverage splice: %+v", store.draft.membershipDeltaPending.slots[index])
		}
	}
	if store.draft.membershipDeltaRoot != 0 {
		t.Fatal("the membership delta tree was allocated; more ids were charged than the pending slots")
	}
}

func TestFeedMergeEmptyMapCreate(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	draft, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	if err := draft.beginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	alpha, _, err := store.ensureFeed("alpha")
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.addFeedToMembership(EmptyMembershipHandle(), alpha)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.beginEmptyMapFeed(); err != nil {
		t.Fatal(err)
	}
	input := NewUnionInput(format.AddressFamilyIPv4, format.ValueKindMembership, 1<<20)
	if err := store.addEmptyMapFeedRange4(key4(0), key4(9), member, &input.v4); err != nil {
		t.Fatal(err)
	}
	if err := store.addEmptyMapFeedRange4(key4(20), key4(29), member, &input.v4); err != nil {
		t.Fatal(err)
	}
	ordered, hasOrdered, err := store.finishEmptyMapFeedRanges4(member, &input.v4)
	if err != nil {
		t.Fatal(err)
	}
	if !hasOrdered {
		t.Fatal("ascending empty-map ranges did not build the ordered prefix")
	}
	if count129(t, ordered) != 20 {
		t.Fatalf("ordered addresses = %d, want 20", count129(t, ordered))
	}
	if draft.meta.RangeRoot == 0 || draft.meta.RangeRecordCount != 2 {
		t.Fatalf("sealed empty-map tree root=%d count=%d, want two records", draft.meta.RangeRoot, draft.meta.RangeRecordCount)
	}
	records := readDraftRangeTree(t, store, rangeCodec4{}, draft.meta)
	if len(records) != 2 {
		t.Fatalf("sealed records = %d, want 2", len(records))
	}
	if uint32(records[0].From) != 0 || uint32(records[0].To) != 9 || records[0].Value != member.id ||
		uint32(records[1].From) != 20 || uint32(records[1].To) != 29 || records[1].Value != member.id {
		t.Fatalf("sealed records = %+v, want member@[0,9] and member@[20,29]", records)
	}
}

// TestFeedMergeEmptyMapOnNonEmptyBase pins the begin_empty_map_feed
// guard: a committed or drafted range tree refuses the empty-map feed.
func TestFeedMergeEmptyMapOnNonEmptyBase(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	draft, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	draft.meta.RangeRoot = 1
	if err := store.beginEmptyMapFeed(); err == nil {
		t.Fatal("empty-map feed over a nonempty range tree did not fail")
	} else if code := errCode(err); code != format.CodeFormatInvalid {
		t.Fatalf("guard code = %d, want FormatInvalid (rust feed_merge.rs)", code)
	}
}

// TestFeedMergeCreateEmptyCoverage returns the exact no-change result
// for a created feed with no coverage input (Rust empty_result).
func TestFeedMergeCreateEmptyCoverage(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	draft, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	if err := draft.beginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	alpha, _, err := store.ensureFeed("alpha")
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.addFeedToMembership(EmptyMembershipHandle(), alpha)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := store.mergeFeed(draft.base, member, true, nilCheck)
	if err != nil {
		t.Fatal(err)
	}
	if merged.InputIntervals != 0 || count129(t, merged.InputAddresses) != 0 {
		t.Fatalf("empty coverage intervals = %d addresses = %d, want zero", merged.InputIntervals, count129(t, merged.InputAddresses))
	}
	if count129(t, merged.Comparison.Comparison.Before) != 0 ||
		count129(t, merged.Comparison.Comparison.After) != 0 ||
		merged.Comparison.BeforeIntervals != 0 || merged.Comparison.AfterIntervals != 0 {
		t.Fatalf("empty Comparison = %+v, want zero", merged.Comparison)
	}
}

// TestFeedMergeCoverageAppliesMember pins the created-feed merge over a
// committed destination (Rust create_feed + FeedPolicy): the workflow
// coverage [4,12]+[20,22] unions the member into the covered segments
// only, keeps every uncovered destination segment, and produces the
// exact Comparison (added = covered cardinality, nothing removed) plus
// the merged draft tree.
func TestFeedMergeCoverageAppliesMember(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	dests := setupDestinationFeeds(t, path)
	recentBit := dests.recent
	combined := dests.combined
	veryRecentBit := dests.veryRecent

	c, err := Open(path, historyBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginMembershipWorkflow(); err != nil {
		t.Fatal(err)
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)

	memberFeed, _, err := store.ensureFeed("member")
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.addFeedToMembership(EmptyMembershipHandle(), memberFeed)
	if err != nil {
		t.Fatal(err)
	}

	input := NewUnionInput(format.AddressFamilyIPv4, format.ValueKindMembership, 1<<20)
	if err := store.addFeedCoverage4(key4(4), key4(12), &input.v4); err != nil {
		t.Fatal(err)
	}
	if err := store.addFeedCoverage4(key4(20), key4(22), &input.v4); err != nil {
		t.Fatal(err)
	}
	if err := store.finishFeedCoverage4(&input.v4); err != nil {
		t.Fatal(err)
	}
	if store.draft.membershipDeltaRoot != 0 {
		t.Fatal("coverage input charged membership deltas (must stay untracked)")
	}

	merged, err := store.mergeFeed(c.base.Meta, member, true, nilCheck)
	if err != nil {
		t.Fatal(err)
	}
	if store.draft.workflowRangeRoot != 0 || store.draft.workflowRangeCount != 0 {
		t.Fatal("merge did not consume the workflow coverage tree")
	}
	if merged.InputIntervals != 2 {
		t.Fatalf("input intervals = %d, want 2", merged.InputIntervals)
	}
	if count129(t, merged.InputAddresses) != 12 {
		t.Fatalf("input addresses = %d, want 12", count129(t, merged.InputAddresses))
	}
	Comparison := merged.Comparison.Comparison
	if count129(t, Comparison.Added) != 12 || count129(t, Comparison.Removed) != 0 {
		t.Fatalf("added/removed = %d/%d, want 12/0", count129(t, Comparison.Added), count129(t, Comparison.Removed))
	}
	if count129(t, Comparison.Unchanged) != 0 || count129(t, Comparison.Changed) != 0 {
		t.Fatalf("unchanged/changed = %d/%d, want zero", count129(t, Comparison.Unchanged), count129(t, Comparison.Changed))
	}
	if count129(t, Comparison.Before) != 0 || count129(t, Comparison.After) != 12 {
		t.Fatalf("before/after = %d/%d, want 0/12", count129(t, Comparison.Before), count129(t, Comparison.After))
	}
	if merged.Comparison.BeforeIntervals != 0 || merged.Comparison.AfterIntervals != 2 {
		t.Fatalf("before/after intervals = %d/%d, want 0/2", merged.Comparison.BeforeIntervals, merged.Comparison.AfterIntervals)
	}
	records := readDraftRangeTree(t, store, rangeCodec4{}, store.draft.meta)
	if len(records) != 7 {
		t.Fatalf("merged records = %d, want 7", len(records))
	}
	// The created member unions into the covered segments only; every
	// uncovered destination segment keeps its committed bitmap (Rust
	// FeedPolicy transform/observe). The four unchanged segments reuse
	// the committed IDs; the two covered segments intern fresh union
	// bitmaps, asserted below by their feed bits.
	want := [][2]uint64{
		{0, 3}, {4, 4}, {5, 12}, {13, 15}, {16, 19}, {20, 22}, {25, 35},
	}
	for index, record := range records {
		if uint32(record.From) != uint32(want[index][0]) || uint32(record.To) != uint32(want[index][1]) {
			t.Fatalf("merged record %d = %+v, want range %v", index, record, want[index])
		}
	}
	if records[0].Value != recentBit || records[3].Value != combined || records[4].Value != recentBit ||
		records[5].Value != member.id || records[6].Value != veryRecentBit {
		t.Fatalf("unchanged segment values = %d %d %d %d %d, want the committed ids",
			records[0].Value, records[3].Value, records[4].Value, records[5].Value, records[6].Value)
	}
	if records[1].Value == records[2].Value || records[1].Value == recentBit || records[2].Value == combined ||
		records[1].Value == member.id || records[2].Value == member.id {
		t.Fatal("covered segments did not intern fresh union bitmaps")
	}
	unrelated, _, err := store.lookupFeed("unrelated")
	if err != nil {
		t.Fatal(err)
	}
	recent, _, err := store.lookupFeed("recent")
	if err != nil {
		t.Fatal(err)
	}
	var probe [3]byte
	if err := store.selectedMembershipBits(records[1].Value, []uint32{unrelated.Index, recent.Index, memberFeed.Index}, probe[:], nilCheck); err != nil {
		t.Fatal(err)
	}
	if probe[0] != 0 || probe[1] != 1 || probe[2] != 1 {
		t.Fatalf("[4,4] bits = %v %v %v, want recent+member only", probe[0], probe[1], probe[2])
	}
	if err := store.selectedMembershipBits(records[2].Value, []uint32{unrelated.Index, recent.Index, memberFeed.Index}, probe[:], nilCheck); err != nil {
		t.Fatal(err)
	}
	if probe[0] != 1 || probe[1] != 1 || probe[2] != 1 {
		t.Fatalf("[5,12] bits = %v %v %v, want recent+member+unrelated", probe[0], probe[1], probe[2])
	}
}
