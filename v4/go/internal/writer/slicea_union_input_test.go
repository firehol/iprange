// Slice A coverage-union input tests (SOW-0025 chunk 3b-5 slice A),
// mirroring the Rust range_mutation_tests.rs vectors: the streamed
// union input routes ascending disjoint ranges into the ordered-prefix
// bulk build, re-checks bridged gaps through the pending-range
// machinery, falls back to the general assignment input on late
// overlap in both families, and matches a scalar reference under
// random order. Every draft runs over the real opened mapping; no
// owned page exists anywhere.

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// readWorkflowTree decodes every record of the private IPv4 workflow
// coverage tree through the mapping (the white-box cursor with the
// workflow roots substituted into the draft meta).
func readWorkflowTree(t *testing.T, store *DraftStore) []rangeRecord[key4] {
	t.Helper()
	meta := store.draft.meta
	meta.RangeRoot = store.draft.workflowRangeRoot
	meta.RangeRecordCount = store.draft.workflowRangeCount
	return readDraftRangeTree(t, store, rangeCodec4{}, meta)
}

// pushWorkflowCoverage pushes one value-bearing range into the private
// IPv4 workflow coverage tree, untracked (Rust push_private_untracked
// with an explicit value; DraftStore::add_feed_coverage hardcodes value
// 1, the coverage tests need the Rust vectors' values).
func pushWorkflowCoverage(store *DraftStore, input *UnionInput, from, to key4, value uint32) error {
	ctx := store.beginRangeEdit4(store.draft.workflowRangeRoot, store.draft.workflowRangeCount)
	if _, err := pushPrivateUntracked(ctx, from, to, value, &input.v4); err != nil {
		return err
	}
	store.draft.workflowRangeRoot = store.rangeRoot
	store.draft.workflowRangeCount = store.rangeCount
	return nil
}

// finishWorkflowInput seals the pending IPv4 coverage input (Rust
// finish_input_untracked / DraftStore::finish_feed_coverage).
func finishWorkflowInput(store *DraftStore, input *UnionInput) error {
	ctx := store.beginRangeEdit4(store.draft.workflowRangeRoot, store.draft.workflowRangeCount)
	if _, err := finishInputUntracked(ctx, &input.v4); err != nil {
		return err
	}
	store.draft.workflowRangeRoot = store.rangeRoot
	store.draft.workflowRangeCount = store.rangeCount
	return nil
}

// TestUnionInputRandomBufferedMatchesScalarReference is the Go mirror of
// the Rust buffered_coverage_union_matches_a_scalar_reference LCG
// vector: 2,000 random ranges over a 512-address space, applied through
// queue/prove/apply, must cover exactly the scalar bitmap with disjoint
// canonical records.
func TestUnionInputRandomBufferedMatchesScalarReference(t *testing.T) {
	const space = 512
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	input := NewUnionInput(format.AddressFamilyIPv4, format.ValueKindMembership, 256*1024)
	random := uint32(0x243f6a88)
	var expected [space]bool
	for operation := 0; operation < 2000; operation++ {
		random = random*1664525 + 1013904223
		first := int(random % space)
		random = random*1664525 + 1013904223
		second := int(random % space)
		from, to := first, second
		if from > to {
			from, to = to, from
		}
		if err := pushWorkflowCoverage(store, &input, key4(uint32(from)), key4(uint32(to)), 1); err != nil {
			t.Fatalf("operation %d: %v", operation, err)
		}
		for address := from; address <= to; address++ {
			expected[address] = true
		}
	}
	if err := finishWorkflowInput(store, &input); err != nil {
		t.Fatal(err)
	}
	if int(store.draft.workflowRangeCount) != len(readWorkflowTree(t, store)) {
		t.Fatal("workflow range count does not match the decoded record count")
	}
	records := readWorkflowTree(t, store)
	for address, wanted := range expected {
		found := false
		for _, record := range records {
			if uint32(record.From) <= uint32(address) && uint32(address) <= uint32(record.To) {
				found = true
				break
			}
		}
		if found != wanted {
			t.Fatalf("address %d covered = %v, want %v", address, found, wanted)
		}
	}
	for _, record := range records {
		if record.Value != 1 {
			t.Fatalf("record %+v has value %d, want 1", record, record.Value)
		}
	}
	for index := 1; index < len(records); index++ {
		if !(uint32(records[index-1].To)+1 < uint32(records[index].From)) {
			t.Fatalf("records %+v and %+v overlap or touch", records[index-1], records[index])
		}
	}
}

// TestUnionInputRebridgesGap pins the pending-gap machinery (Rust
// buffered_coverage_rechecks_a_gap_when_a_later_range_bridges_it): the
// third range bridges the gap between the two queued sides, so the
// sealed tree is one canonical interval.
func TestUnionInputRebridgesGap(t *testing.T) {
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	input := NewUnionInput(format.AddressFamilyIPv4, format.ValueKindMembership, 256*1024)
	for _, interval := range [][2]uint32{{35, 45}, {15, 32}, {30, 38}} {
		if err := pushWorkflowCoverage(store, &input, key4(interval[0]), key4(interval[1]), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := finishWorkflowInput(store, &input); err != nil {
		t.Fatal(err)
	}
	records := readWorkflowTree(t, store)
	if len(records) != 1 {
		t.Fatalf("sealed records = %+v, want one canonical interval", records)
	}
	if uint32(records[0].From) != 15 || uint32(records[0].To) != 45 || records[0].Value != 1 {
		t.Fatalf("sealed record = %+v, want [15,45] value 1", records[0])
	}
	if store.draft.workflowRangeCount != 1 {
		t.Fatalf("workflow range count = %d, want 1", store.draft.workflowRangeCount)
	}
}

// TestUnionInputOrderedNormalizesToSingleInterval pins the ordered
// prefix of the Rust buffered_ordered_coverage vector: 2,000 ascending
// single-key inputs coalesce into one canonical record and report the
// exact ordered address count.
func TestUnionInputOrderedNormalizesToSingleInterval(t *testing.T) {
	const inputs = 2000
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	input := NewUnionInput(format.AddressFamilyIPv4, format.ValueKindMembership, 256*1024)
	for key := uint32(0); key < inputs; key++ {
		if err := pushWorkflowCoverage(store, &input, key4(key), key4(key), 42); err != nil {
			t.Fatal(err)
		}
	}
	if err := finishWorkflowInput(store, &input); err != nil {
		t.Fatal(err)
	}
	records := readWorkflowTree(t, store)
	if len(records) != 1 {
		t.Fatalf("sealed records = %+v, want one canonical interval", records)
	}
	if uint32(records[0].From) != 0 || uint32(records[0].To) != inputs-1 || records[0].Value != 42 {
		t.Fatalf("sealed record = %+v, want [0,%d] value 42", records[0], inputs-1)
	}
	if store.draft.workflowRangeCount != 1 {
		t.Fatalf("workflow range count = %d, want 1", store.draft.workflowRangeCount)
	}
	// The queue coalesces every touching input before the prefix can
	// prove ascending, so no ordered prefix is built for this vector -
	// the Rust test of the same name pins the same behavior through
	// work, not through ordered_addresses (Rust assert_eq!(count, 1);
	// ranges_emitted == 1 while the prefix stays unbuilt).
	if _, has := input.orderedAddresses(); has {
		t.Fatal("coalesced input unexpectedly built an ordered prefix")
	}
}

// TestUnionInputLateOverlapFallsBackBothFamilies pins the Rust
// buffered_ordered_prefix_falls_back_for_late_overlap vector in both
// address families: a range proving the prefix no longer ordered flips
// the input general, drops the ordered count, and the sealed tree is
// the canonical union.
func TestUnionInputLateOverlapFallsBackBothFamilies(t *testing.T) {
	for _, family := range []uint8{format.AddressFamilyIPv4, format.AddressFamilyIPv6} {
		path := createValueDB(t, family, format.ValueKindMembership, 0, feedsTag)
		_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
		input := NewUnionInput(family, format.ValueKindMembership, 256*1024)
		intervals := [][2]uint64{{0, 1}, {4, 5}, {8, 9}, {2, 10}, {20, 21}}
		for _, interval := range intervals {
			if family == format.AddressFamilyIPv4 {
				if err := pushWorkflowCoverage(store, &input, key4(uint32(interval[0])), key4(uint32(interval[1])), 7); err != nil {
					t.Fatal(err)
				}
			} else {
				ctx := store.beginRangeEdit6(store.draft.workflowRangeRoot, store.draft.workflowRangeCount)
				if _, err := pushPrivateUntracked(ctx, key6{Hi: 0, Lo: interval[0]}, key6{Hi: 0, Lo: interval[1]}, 7, &input.v6); err != nil {
					t.Fatal(err)
				}
				store.draft.workflowRangeRoot = store.rangeRoot
				store.draft.workflowRangeCount = store.rangeCount
			}
		}
		if family == format.AddressFamilyIPv4 {
			if err := finishWorkflowInput(store, &input); err != nil {
				t.Fatal(err)
			}
		} else {
			ctx := store.beginRangeEdit6(store.draft.workflowRangeRoot, store.draft.workflowRangeCount)
			if _, err := finishInputUntracked(ctx, &input.v6); err != nil {
				t.Fatal(err)
			}
			store.draft.workflowRangeRoot = store.rangeRoot
			store.draft.workflowRangeCount = store.rangeCount
		}
		if !input.IsGeneral() {
			t.Fatal("late overlap did not flip the input general")
		}
		if _, has := input.orderedAddresses(); has {
			t.Fatal("general input still reports an ordered address count")
		}
		if family == format.AddressFamilyIPv4 {
			records := readWorkflowTree(t, store)
			if len(records) != 2 {
				t.Fatalf("%v sealed records = %+v, want two canonical intervals", family, records)
			}
			from0, to0 := uint64(records[0].From), uint64(records[0].To)
			from1, to1 := uint64(records[1].From), uint64(records[1].To)
			if from0 != 0 || to0 != 10 || from1 != 20 || to1 != 21 ||
				records[0].Value != 7 || records[1].Value != 7 {
				t.Fatalf("%v sealed records = %+v, want [0,10] and [20,21] value 7", family, records)
			}
		} else {
			meta := store.draft.meta
			meta.RangeRoot = store.draft.workflowRangeRoot
			meta.RangeRecordCount = store.draft.workflowRangeCount
			records := readDraftRangeTree(t, store, rangeCodec6{}, meta)
			if len(records) != 2 {
				t.Fatalf("%v sealed records = %+v, want two canonical intervals", family, records)
			}
			from0, to0 := records[0].From.Lo, records[0].To.Lo
			from1, to1 := records[1].From.Lo, records[1].To.Lo
			if from0 != 0 || to0 != 10 || from1 != 20 || to1 != 21 ||
				records[0].Value != 7 || records[1].Value != 7 {
				t.Fatalf("%v sealed records = %+v, want [0,10] and [20,21] value 7", family, records)
			}
		}
	}
}

// TestUnionInputEmptyMapUnorderedRanges pins the empty-map feed trio
// over an unordered overlapping input (Rust empty-map feed + the union
// gap machinery): the sealed draft tree carries one canonical member
// record, and the member refcount is charged for it.
func TestUnionInputEmptyMapUnorderedRanges(t *testing.T) {
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
	for _, interval := range [][2]uint32{{35, 45}, {15, 32}, {30, 38}} {
		if err := store.addEmptyMapFeedRange4(key4(interval[0]), key4(interval[1]), member, &input.v4); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.finishEmptyMapFeedRanges4(member, &input.v4); err != nil {
		t.Fatal(err)
	}
	records := readDraftRangeTree(t, store, rangeCodec4{}, draft.meta)
	if len(records) != 1 {
		t.Fatalf("sealed records = %+v, want one canonical member record", records)
	}
	if uint32(records[0].From) != 15 || uint32(records[0].To) != 45 || records[0].Value != member.id {
		t.Fatalf("sealed record = %+v, want [15,45] member %d", records[0], member.id)
	}
	if draft.meta.RangeRecordCount != 1 {
		t.Fatalf("range record count = %d, want 1", draft.meta.RangeRecordCount)
	}
}
