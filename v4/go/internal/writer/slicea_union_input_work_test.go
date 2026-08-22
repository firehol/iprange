//go:build v4work

// Slice A union-input work pins (SOW-0025 chunk 3b-5 slice A),
// mirroring the Rust range_mutation_tests.rs work::measure vectors: the
// coalescing queue emits one normalized record, the ordered prefix
// builds packed pages without descent or splits, the monotonic union
// edges revalidate exactly once, and the leaf locator serves the hot
// private inserts without repeated tree descent. The vectors are frozen
// copies of the Rust ones, so the exact counter deltas are stable; a
// change that adds or removes hot-path work is visible.

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// directUnionRange applies one range through the monotonic union state
// without the buffered input, untracked, and reports whether the tree
// changed (Rust union_private over the MemoryStore; the value
// accounting is a no-op exactly like the Rust memory store).
func directUnionRange(store *DraftStore, state *unionState, from, to tree.Key, value uint32) (bool, error) {
	family, err := store.rangeFamily()
	if err != nil {
		return false, err
	}
	store.rangeRoot = store.draft.meta.RangeRoot
	store.rangeCount = store.draft.meta.RangeRecordCount
	ctx := &store.rangeCtx
	ctx.family = family
	ctx.store = store
	ctx.untracked = false
	ctx.root = &store.rangeRoot
	ctx.count = &store.rangeCount
	ctx.scratch = &store.rangeScratch
	ctx.markUntracked()
	changed, err := unionPrivateUntrackedGap(ctx, rangeRecord{from: from, to: to, value: value}, tree.EdgeFirst, false, state)
	if err != nil {
		return false, err
	}
	store.draft.meta.RangeRoot = store.rangeRoot
	store.draft.meta.RangeRecordCount = store.rangeCount
	return changed, nil
}

// TestWorkUnionInputQueueNormalizes pins the Rust
// buffered_ordered_coverage_persists_only_normalized_intervals vector:
// 2,000 touching single-key inputs coalesce in the queue into one
// normalized record, emitted exactly once into an empty tree.
func TestWorkUnionInputQueueNormalizes(t *testing.T) {
	const inputs = 2000
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	input := NewUnionInput(format.AddressFamilyIPv4, format.ValueKindMembership, 256*1024)
	work.Reset()
	for key := uint32(0); key < inputs; key++ {
		if err := pushWorkflowCoverage(store, &input, key4(key), key4(key), 42); err != nil {
			t.Fatal(err)
		}
	}
	if err := finishWorkflowInput(store, &input); err != nil {
		t.Fatal(err)
	}
	snapshot := work.Read()
	if snapshot.RangesEmitted != 1 || snapshot.RangesCoalesced != inputs-1 || snapshot.TreeLookups != 0 {
		t.Fatalf("queue normalize work = %+v, want emitted 1 coalesced %d lookups 0", snapshot, inputs-1)
	}
}

// TestWorkUnionInputAscendingPacked pins the Rust
// buffered_ascending_coverage_builds_packed_pages_without_tree_searches
// vector: 2,000 disjoint ascending pairs build one packed page chain
// without a single tree descent, page split, or output pass, and the
// ordered prefix reports the exact 4,000-address count.
func TestWorkUnionInputAscendingPacked(t *testing.T) {
	const inputs = 2000
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	input := NewUnionInput(format.AddressFamilyIPv4, format.ValueKindMembership, 256*1024)
	work.Reset()
	for ordinal := uint32(0); ordinal < inputs; ordinal++ {
		key := ordinal * 4
		if err := pushWorkflowCoverage(store, &input, key4(key), key4(key+1), 42); err != nil {
			t.Fatal(err)
		}
	}
	if err := finishWorkflowInput(store, &input); err != nil {
		t.Fatal(err)
	}
	snapshot := work.Read()
	if store.draft.workflowRangeCount != inputs {
		t.Fatalf("workflow range count = %d, want %d", store.draft.workflowRangeCount, inputs)
	}
	if len(readWorkflowTree(t, store)) != inputs {
		t.Fatal("packed workflow tree lost records")
	}
	if snapshot.TreeLookups != 0 || snapshot.PagesSplit != 0 || snapshot.OutputPasses != 0 {
		t.Fatalf("packed construction work = %+v, want no descent/split/pass", snapshot)
	}
	if ordered, has := input.orderedAddresses(); !has {
		t.Fatal("packed prefix did not report its address count")
	} else if count129(t, ordered) != inputs*2 {
		t.Fatalf("ordered addresses = %d, want %d", count129(t, ordered), inputs*2)
	}
}

// TestWorkUnionInputMonotonicEdges pins the Rust
// private_coverage_union_reuses_monotonic_edges vector in both
// directions: the cached edge serves every single-point insert, only
// split refreshes descend the tree, the first-key fence work stays
// structural, and the edge is revalidated exactly once at the finish.
func TestWorkUnionInputMonotonicEdges(t *testing.T) {
	const inputs = 2000
	for _, descending := range []bool{false, true} {
		path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
		_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
		var state unionState
		work.Reset()
		for ordinal := uint32(0); ordinal < inputs; ordinal++ {
			key := uint32(0)
			if descending {
				key = inputs - 1 - ordinal
			} else {
				key = ordinal
			}
			key *= 4
			if _, err := directUnionRange(store, &state, key4(key), key4(key+1), 1); err != nil {
				t.Fatal(err)
			}
		}
		if err := finishPrivateUntracked(&rangeCtx{family: rangeCodec4{}, store: store, root: &store.draft.meta.RangeRoot, count: &store.draft.meta.RangeRecordCount, scratch: &store.rangeScratch}, &state); err != nil {
			t.Fatal(err)
		}
		snapshot := work.Read()
		if store.draft.meta.RangeRecordCount != inputs {
			t.Fatalf("descending=%v count = %d, want %d", descending, store.draft.meta.RangeRecordCount, inputs)
		}
		if snapshot.PagesSplit == 0 {
			t.Fatalf("descending=%v monotonic build never split a page", descending)
		}
		if snapshot.TreeLookups != snapshot.PagesSplit {
			t.Fatalf("descending=%v lookups %d != splits %d", descending, snapshot.TreeLookups, snapshot.PagesSplit)
		}
		if snapshot.FirstFenceUpdates == 0 {
			t.Fatalf("descending=%v first-fence work did not observe structural writes", descending)
		}
		structuralBound := (snapshot.PagesSplit + 1) * (uint64(format.MaxTreeLevel) + 1)
		if snapshot.FirstFenceUpdates > structuralBound {
			t.Fatalf("descending=%v first-fence updates %d above structural bound %d", descending, snapshot.FirstFenceUpdates, structuralBound)
		}
		if snapshot.FirstFenceUpdates >= inputs/10 {
			t.Fatalf("descending=%v first-fence updates %d >= %d, range-proportional fence writes", descending, snapshot.FirstFenceUpdates, inputs/10)
		}
		if snapshot.EdgePathChecks != 1 {
			t.Fatalf("descending=%v edge path checks = %d, want the single monotonic revalidation", descending, snapshot.EdgePathChecks)
		}
	}
}

// TestWorkUnionInputRandomOrderBounds pins the Rust
// private_coverage_union_random_order_bounds_lookups_by_inputs_and_splits
// vector: 2,000 random-order single points never repeat a tree search
// beyond the inputs plus the split refreshes.
func TestWorkUnionInputRandomOrderBounds(t *testing.T) {
	const inputs = 2000
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	var state unionState
	work.Reset()
	for ordinal := uint32(0); ordinal < inputs; ordinal++ {
		key := (ordinal * 1597 % inputs) * 4
		if _, err := directUnionRange(store, &state, key4(key), key4(key+1), 1); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := work.Read()
	if store.draft.meta.RangeRecordCount != inputs {
		t.Fatalf("count = %d, want %d", store.draft.meta.RangeRecordCount, inputs)
	}
	if snapshot.TreeLookups > inputs+snapshot.PagesSplit {
		t.Fatalf("random union lookups %d > inputs %d + splits %d", snapshot.TreeLookups, inputs, snapshot.PagesSplit)
	}
}

// TestWorkUnionInputLeafLocatorHints pins the Rust
// buffered_random_disjoint_coverage_reuses_private_leaf_hints vector:
// the buffered general fallback serves most of the 20,000 disjoint
// inserts from the leaf-locator hints, and tree descents stay bounded
// by the fallbacks plus the split refreshes.
func TestWorkUnionInputLeafLocatorHints(t *testing.T) {
	const inputs = 20000
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	input := NewUnionInput(format.AddressFamilyIPv4, format.ValueKindMembership, 256*1024)
	work.Reset()
	for ordinal := uint32(0); ordinal < inputs; ordinal++ {
		key := (ordinal * 15997 % inputs) * 4
		if err := pushWorkflowCoverage(store, &input, key4(key), key4(key+1), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := finishWorkflowInput(store, &input); err != nil {
		t.Fatal(err)
	}
	snapshot := work.Read()
	if store.draft.workflowRangeCount != inputs {
		t.Fatalf("count = %d, want %d", store.draft.workflowRangeCount, inputs)
	}
	if len(readWorkflowTree(t, store)) != inputs {
		t.Fatal("locator build lost records")
	}
	if snapshot.LeafLocatorHits <= inputs/4 {
		t.Fatalf("locator hits %d <= %d, hot private inserts missed the hints", snapshot.LeafLocatorHits, inputs/4)
	}
	if snapshot.TreeLookups > snapshot.LeafLocatorFallbacks*2+snapshot.PagesSplit {
		t.Fatalf("locator fallback repeated descent: lookups %d fallbacks %d splits %d", snapshot.TreeLookups, snapshot.LeafLocatorFallbacks, snapshot.PagesSplit)
	}
}

// TestWorkUnionAssignmentLocatorV4V6 pins the Rust
// private_assignment_locator_avoids_ipv4_descent_without_adding_ipv6_work
// vector: the eager IPv4 locator serves most of the 100,000 disjoint
// assignments without descent, and the IPv6 locator stays entirely
// disabled so IPv6 pays no hint work at all.
func TestWorkUnionAssignmentLocatorV4V6(t *testing.T) {
	const inputs = 100000
	for _, family := range []uint8{format.AddressFamilyIPv4, format.AddressFamilyIPv6} {
		path := createValueDB(t, family, format.ValueKindDirect, format.StructureKindNone, feedsTag)
		_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
		if err := store.beginEmptyMapFeed(); err != nil {
			t.Fatal(err)
		}
		input := newAssignmentInput(family, 256*1024)
		work.Reset()
		for ordinal := uint32(0); ordinal < inputs; ordinal++ {
			key := uint64((ordinal * 1597 % inputs) * 4)
			var from, to tree.Key
			if family == format.AddressFamilyIPv4 {
				from, to = key4(uint32(key)), key4(uint32(key+1))
			} else {
				from, to = key6(key), key6(key+1)
			}
			if _, err := store.assignInput(from, to, ordinal, &input); err != nil {
				t.Fatal(err)
			}
		}
		snapshot := work.Read()
		if store.draft.meta.RangeRecordCount != inputs {
			t.Fatalf("%v count = %d, want %d", family, store.draft.meta.RangeRecordCount, inputs)
		}
		if len(readDraftRangeTree(t, store, store.draft.meta)) != inputs {
			t.Fatal("assignment build lost records")
		}
		if family == format.AddressFamilyIPv4 {
			if snapshot.LeafLocatorHits <= inputs/3 {
				t.Fatalf("IPv4 locator hits %d <= %d", snapshot.LeafLocatorHits, inputs/3)
			}
			if snapshot.LeafLocatorHits+snapshot.LeafLocatorFallbacks != inputs-1 {
				t.Fatalf("IPv4 locator total %d, want %d", snapshot.LeafLocatorHits+snapshot.LeafLocatorFallbacks, inputs-1)
			}
			if snapshot.TreeLookups > snapshot.LeafLocatorFallbacks*2 {
				t.Fatalf("IPv4 fallback repeated descent: lookups %d fallbacks %d", snapshot.TreeLookups, snapshot.LeafLocatorFallbacks)
			}
		} else {
			if snapshot.LeafLocatorHits != 0 || snapshot.LeafLocatorMisses != 0 || snapshot.LeafLocatorFallbacks != 0 {
				t.Fatalf("IPv6 charged locator work: %+v", snapshot)
			}
			if snapshot.TreeLookups == 0 {
				t.Fatal("IPv6 skipped canonical descent")
			}
		}
	}
}

// TestWorkUnionInputSpliceLargeRun pins the Rust
// private_constant_union_splices_a_large_run_without_per_record_searches
// counters (range_mutation_tests.rs:697-714): the covering range splices
// a 2,000-record constant run into one record, coalescing every record
// exactly once, performing exactly one tree lookup per affected leaf,
// and never rescanning records.
func TestWorkUnionInputSpliceLargeRun(t *testing.T) {
	const inputs = 2000
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	var state unionState
	for ordinal := uint32(0); ordinal < inputs; ordinal++ {
		key := ordinal * 4
		if _, err := directUnionRange(store, &state, key4(key), key4(key+1), 42); err != nil {
			t.Fatal(err)
		}
	}
	work.Reset()
	changed, err := directUnionRange(store, &state, key4(0), key4(8000), 42)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := work.Read()
	if !changed {
		t.Fatal("covering splice reported no change")
	}
	if store.draft.meta.RangeRecordCount != 1 {
		t.Fatalf("spliced count = %d, want 1", store.draft.meta.RangeRecordCount)
	}
	records := readDraftRangeTree(t, store, store.draft.meta)
	if len(records) != 1 || records[0].from.Hi != 0 || records[0].to.Hi != 8000 || records[0].value != 42 {
		t.Fatalf("spliced tree = %+v, want the single covering record [0,8000] value 42", records)
	}
	if snapshot.RangesCoalesced != inputs {
		t.Fatalf("coalesced = %d, want %d", snapshot.RangesCoalesced, inputs)
	}
	if snapshot.TreeLookups != 8 {
		t.Fatalf("tree lookups = %d, want one per affected leaf (8)", snapshot.TreeLookups)
	}
	if snapshot.CellProbes >= inputs+200 {
		t.Fatalf("cell probes = %d >= %d, records were rescanned", snapshot.CellProbes, inputs+200)
	}
}

// TestWorkUnionInputFlushKeepsEdge pins the Rust finish_private
// contract: flushing the pending edge writes the leaf metadata but
// keeps the cached edge in the union state, so a later edge-proven
// insert reuses it instead of descending again.
func TestWorkUnionInputFlushKeepsEdge(t *testing.T) {
	const inputs = 100
	path := createValueDB(t, format.AddressFamilyIPv4, format.ValueKindMembership, 0, feedsTag)
	_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
	var state unionState
	for ordinal := uint32(0); ordinal < inputs; ordinal++ {
		key := ordinal * 4
		if _, err := directUnionRange(store, &state, key4(key), key4(key+1), 42); err != nil {
			t.Fatal(err)
		}
	}
	family, err := store.rangeFamily()
	if err != nil {
		t.Fatal(err)
	}
	store.rangeRoot = store.draft.meta.RangeRoot
	store.rangeCount = store.draft.meta.RangeRecordCount
	ctx := &store.rangeCtx
	ctx.family = family
	ctx.store = store
	ctx.untracked = false
	ctx.root = &store.rangeRoot
	ctx.count = &store.rangeCount
	ctx.scratch = &store.rangeScratch
	ctx.markUntracked()
	if err := finishPrivateUntracked(ctx, &state); err != nil {
		t.Fatal(err)
	}
	store.draft.meta.RangeRoot = store.rangeRoot
	store.draft.meta.RangeRecordCount = store.rangeCount
	if !state.hasEdge {
		t.Fatal("finish_private dropped the cached edge (Rust keeps it after the flush)")
	}
	if _, err := directUnionRange(store, &state, key4(inputs*4), key4(inputs*4+1), 42); err != nil {
		t.Fatal(err)
	}
	if store.draft.meta.RangeRecordCount != inputs+1 {
		t.Fatalf("count = %d, want %d", store.draft.meta.RangeRecordCount, inputs+1)
	}
}
