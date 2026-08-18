// DraftStore range edit entry-point tests over the real opened mapping:
// the draft privacy dispatch, the meta bookkeeping, the changed flag, and
// the value-kind accounting fence.

package writer

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestDraftStoreAssignClearRanges runs the public range edit entry points
// over one mapped draft and verifies the draft meta and the changed flag
// (Rust draft_store.rs assign_v4/clear_v4 flows; the empty private tree
// takes the gap path).
func TestDraftStoreAssignClearRanges(t *testing.T) {
	path := makeEmptyDBPages(t, 64)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	draft, store, _ := openDraftStore(t, path, budget, [16]byte{4})

	if !draft.rangeTreePrivate {
		t.Fatal("empty committed range tree must be draft-private")
	}

	changed, err := store.AssignV4(0, 100, 1)
	if err != nil || !changed {
		t.Fatalf("AssignV4(0,100,1) = %v, %v", changed, err)
	}
	if !draft.Changed() {
		t.Fatal("draft changed flag not set by AssignV4")
	}
	meta := draft.Meta()
	if meta.RangeRoot == 0 {
		t.Fatal("AssignV4 left the range root empty")
	}
	if meta.RangeRecordCount != 1 {
		t.Fatalf("range record count = %d, want 1", meta.RangeRecordCount)
	}

	if changed, err := store.AssignV4(20, 30, 2); err != nil || !changed {
		t.Fatalf("AssignV4(20,30,2) = %v, %v", changed, err)
	}
	if meta := draft.Meta(); meta.RangeRecordCount != 3 {
		t.Fatalf("range record count = %d, want 3", meta.RangeRecordCount)
	}

	if changed, err := store.ClearV4(20, 30); err != nil || !changed {
		t.Fatalf("ClearV4(20,30) = %v, %v", changed, err)
	}
	if meta := draft.Meta(); meta.RangeRecordCount != 2 {
		t.Fatalf("range record count after clear = %d, want 2", meta.RangeRecordCount)
	}

	// Same-value assignment over an unchanged interval is a no-op.
	if changed, err := store.AssignV4(0, 19, 1); err != nil || changed {
		t.Fatalf("same-value AssignV4 = %v, %v; want false", changed, err)
	}
	if meta := draft.Meta(); meta.RangeRecordCount != 2 {
		t.Fatalf("range record count after no-op = %d, want 2", meta.RangeRecordCount)
	}
}

// TestDraftStoreAssignV6RunsTheV6Family exercises the IPv6 entry point on
// a v6 direct draft.
func TestDraftStoreAssignV6RunsTheV6Family(t *testing.T) {
	path := makeEmptyDBPagesKind(t, 64, format.AddressFamilyIPv6)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	draft, store, _ := openDraftStore(t, path, budget, [16]byte{5})

	changed, err := store.AssignV6(0, 0, 0, 100, 7)
	if err != nil || !changed {
		t.Fatalf("AssignV6 = %v, %v", changed, err)
	}
	if meta := draft.Meta(); meta.RangeRecordCount != 1 || meta.RangeRoot == 0 {
		t.Fatalf("v6 assign meta = root %d count %d", meta.RangeRoot, meta.RangeRecordCount)
	}
	if changed, err := store.ClearV6(0, 0, 0, 100); err != nil || !changed {
		t.Fatalf("ClearV6 = %v, %v", changed, err)
	}
	if meta := draft.Meta(); meta.RangeRecordCount != 0 || meta.RangeRoot != 0 {
		t.Fatalf("v6 clear meta = root %d count %d", meta.RangeRoot, meta.RangeRecordCount)
	}
}

// TestDraftStoreRangeAccountingFailsClosedOnMembershipKinds pins the
// fail-closed fence: until the membership/structure accounting cores
// arrive, range edits on those kinds are refused instead of silently
// corrupting refcounts.
func TestDraftStoreRangeAccountingFailsClosedOnMembershipKinds(t *testing.T) {
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
	_, store, _ := openDraftStore(t, path, budget, [16]byte{6})
	rootBefore := store.draft.meta.RangeRoot
	countBefore := store.draft.meta.RangeRecordCount
	if _, err := store.AssignV4(0, 10, 1); err == nil {
		t.Fatal("membership-kind range assign did not fail closed")
	}
	// Rust draft_store.rs snapshots root/count into locals and commits
	// them only after the edit succeeds: a failed assign must leave the
	// draft meta at its pre-call state, and retrying the same edit must
	// be clean (chunk 3b level-1 F-1 regression pin).
	if store.draft.meta.RangeRoot != rootBefore || store.draft.meta.RangeRecordCount != countBefore {
		t.Fatalf("failed assign mutated draft meta: root %d->%d count %d->%d",
			rootBefore, store.draft.meta.RangeRoot, countBefore, store.draft.meta.RangeRecordCount)
	}
	if _, err := store.AssignV4(0, 10, 1); err == nil {
		t.Fatal("membership-kind range assign retry did not fail closed")
	}
	if store.draft.meta.RangeRoot != rootBefore || store.draft.meta.RangeRecordCount != countBefore {
		t.Fatalf("failed assign retry mutated draft meta: root %d->%d count %d->%d",
			rootBefore, store.draft.meta.RangeRoot, countBefore, store.draft.meta.RangeRecordCount)
	}
}

func makeEmptyDBPagesKind(t *testing.T, pages uint64, family uint8) string {
	t.Helper()
	path := makeEmptyDBPages(t, pages)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 2; i++ {
		page := raw[i*format.PageSize : (i+1)*format.PageSize]
		page[11] = family
		format.PutU32(page[252:256], format.MetaCRC32C(page))
	}
	if err := osWriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
