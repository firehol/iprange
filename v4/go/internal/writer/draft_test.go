package writer

// DraftStore storage-surface tests, mirroring the Rust draft_store_tests.rs
// allocation/storage cases that do not need the range/membership edit
// workflows (those arrive with the 3b chunk). Every draft runs over the
// real opened mapping; no owned page exists anywhere.

import (
	"math"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/retire"
)

// makeEmptyDBPages writes a minimal valid empty direct database whose meta
// declares pages page-count and whose physical extent matches (the Rust
// empty_direct_meta(1) test database, parameterized by page count).
func makeEmptyDBPages(t *testing.T, pages uint64) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/empty.iprdb"
	raw := make([]byte, pages*format.PageSize)
	for i := uint64(0); i < 2; i++ {
		page := raw[i*format.PageSize : (i+1)*format.PageSize]
		copy(page, format.MainMagic[:])
		putMetaFieldsForTest(page, pages)
	}
	if err := osWriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func putMetaFieldsForTest(page []byte, pages uint64) {
	format.PutU16(page[8:10], format.MetaSize)
	page[10] = format.PageShift
	page[11] = format.AddressFamilyIPv4
	page[12] = format.ValueKindDirect
	copy(page[16:32], "direct\x00")
	copy(page[32:48], openTestDBID[:])
	format.PutU64(page[48:56], 1)
	copy(page[56:72], openTestNonce[:])
	format.PutU64(page[72:80], pages)
	format.PutU32(page[252:256], format.MetaCRC32C(page))
}

// openDraftStore opens path as a writer and binds a fresh draft over the
// committed generation. The mapping, the draft store, and the base meta
// are returned; the core is closed with the test.
func openDraftStore(t testing.TB, path string, budget PageBudget, nonce [16]byte) (*Draft, *DraftStore, *Core) {
	t.Helper()
	c, err := Open(path, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	draft, err := NewDraft(c.base.Meta, nonce)
	if err != nil {
		t.Fatal(err)
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, budget, draft)
	return draft, store, c
}

// initRangeLeaf initializes one claimed page as a data page (page magic,
// draft transaction, empty range leaf geometry), mirroring the Rust
// initialize_test_range_page helper.
func initRangeLeaf(t testing.TB, store *DraftStore, pageNumber uint32, txn uint64) {
	t.Helper()
	t.Helper()
	if err := store.Update(pageNumber, func(page []byte) error {
		format.InitializePageHeader(page, format.PageTypeRangeLeaf, txn, 0, 0,
			format.SlottedHeaderSize, format.PageSize, uint32(format.AddressFamilyIPv4))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestNewDraftAdvancesTransactionID pins the Rust Draft::new contract: the
// transaction ID advances by one, the nonce is staged, and the base is
// preserved for publication-time comparisons.
func TestNewDraftAdvancesTransactionID(t *testing.T) {
	base := format.Meta{TxnID: 1}
	draft, err := NewDraft(base, [16]byte{3})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Meta().TxnID != 2 {
		t.Fatalf("txn = %d, want 2", draft.Meta().TxnID)
	}
	if draft.Meta().CommitNonce != [16]byte{3} {
		t.Fatal("commit nonce not staged")
	}
	if draft.Changed() {
		t.Fatal("fresh draft reports changed")
	}

	base.TxnID = math.MaxUint64
	if _, err := NewDraft(base, [16]byte{}); err == nil {
		t.Fatal("transaction ID exhaustion accepted")
	} else if err.Error() == "" {
		t.Fatal("empty error")
	} else if code := errCode(err); code != format.CodeArithmeticOverflow {
		t.Fatalf("exhaustion code = %d, want ArithmeticOverflow", code)
	}
}

func errCode(err error) format.ErrorCode {
	f := &format.Error{}
	if e, ok := err.(*format.Error); ok {
		return e.Code
	}
	_ = f
	return 0
}

// TestBudgetFailureHappensBeforeTheFirstAllocation mirrors
// page_budget_failure_happens_before_the_first_allocation: a zero budget
// refuses before any allocation and leaves the draft untouched.
func TestBudgetFailureHappensBeforeTheFirstAllocation(t *testing.T) {
	path := makeEmptyDB(t)
	draft, store, _ := openDraftStore(t, path, PageBudget{}, [16]byte{3})
	if _, err := store.Allocate(); err == nil {
		t.Fatal("allocation succeeded with a zero budget")
	} else if errCode(err) != format.CodeInsufficientResourceBudget {
		t.Fatalf("error code = %d, want InsufficientResourceBudget", errCode(err))
	}
	if draft.meta.PageCount != 2 {
		t.Fatalf("page count = %d, want 2", draft.meta.PageCount)
	}
	if draft.meta.FreeBitmapRoot != 0 {
		t.Fatal("free bitmap root moved on refusal")
	}
	if draft.Changed() {
		t.Fatal("draft reports changed after refusal")
	}
}

// TestMappedPageCannotBypassTheCurrentPageLimit mirrors
// mapped_page_cannot_bypass_the_current_page_limit: shrinking the draft
// limit under an allocated page makes every access refuse, even though the
// page is still mapped.
func TestMappedPageCannotBypassTheCurrentPageLimit(t *testing.T) {
	path := makeEmptyDB(t)
	budget := PageBudget{MaxHeapBytes: 2 * format.PageSize, MaxPrivatePages: 1, MaxGrowthPages: 1}
	draft, store, _ := openDraftStore(t, path, budget, [16]byte{3})
	pageNumber, err := store.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if pageNumber != 2 {
		t.Fatalf("first allocation = %d, want 2", pageNumber)
	}
	if err := store.Inspect(pageNumber, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	draft.meta.PageCount = uint64(pageNumber)
	if err := store.Inspect(pageNumber, func([]byte) error { return nil }); err == nil {
		t.Fatal("inspect passed beyond the draft page limit")
	} else if errCode(err) != format.CodeFormatInvalid {
		t.Fatalf("error code = %d, want FormatInvalid", errCode(err))
	}
}

// TestReusingACurrentTransactionPageKeepsOneDirtyChainEntry mirrors
// reusing_a_current_transaction_page_keeps_one_dirty_chain_entry: a page
// that is discarded, freed, and reallocated inside one draft keeps exactly
// one dirty-chain entry and survives sealing.
func TestReusingACurrentTransactionPageKeepsOneDirtyChainEntry(t *testing.T) {
	path := makeEmptyDBPages(t, 10)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	draft, store, _ := openDraftStore(t, path, budget, [16]byte{3})

	if err := store.claimAllocated(5); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(5, func(page []byte) error {
		page[100] = 0xa5
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DiscardPrivate(5); err != nil {
		t.Fatal(err)
	}
	if err := store.Inspect(5, func(page []byte) error {
		if page[100] != 0xa5 {
			t.Fatalf("discarded page content at 100 = %#x, want 0xa5", page[100])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if page, err := store.popPrivate(); err != nil || page != 5 {
		t.Fatalf("popPrivate = %d, %v; want 5", page, err)
	}
	if err := store.freeOne(5); err != nil {
		t.Fatal(err)
	}
	page, err := store.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if page != 5 {
		t.Fatalf("reallocation = %d, want 5", page)
	}
	if draft.privatePages != 2 {
		t.Fatalf("private pages = %d, want 2", draft.privatePages)
	}
	if err := store.sealPrivatePages(nil); err != nil {
		t.Fatal(err)
	}
	if draft.dirtyHead != 0 {
		t.Fatal("dirty chain not cleared by seal")
	}
}

// TestRetainedAbortedFreePageIsRelinkedBeforeOrAfterSealing mirrors
// retained_aborted_free_page_is_relinked_before_or_after_sealing: a page
// retained from an aborted draft relinks into the retry draft's dirty
// chain whether or not the aborted draft sealed it, and ends checksummed
// exactly once.
func TestRetainedAbortedFreePageIsRelinkedBeforeOrAfterSealing(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsealed", true: "sealed"}[sealed], func(t *testing.T) {
			path := makeEmptyDBPages(t, 10)
			budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 8, MaxGrowthPages: 8}
			core, err := Open(path, budget, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer core.Close()

			aborted, err := NewDraft(core.base.Meta, [16]byte{3})
			if err != nil {
				t.Fatal(err)
			}
			store := NewDraftStore(core.m, core.base.Meta.PageCount, budget, aborted)
			if err := store.claimAllocated(5); err != nil {
				t.Fatal(err)
			}
			initRangeLeaf(t, store, 5, aborted.meta.TxnID)
			if sealed {
				if err := store.sealPrivatePages(nil); err != nil {
					t.Fatal(err)
				}
			}
			if aborted.meta.TxnID != 2 || aborted.meta.PageCount != 10 {
				t.Fatalf("aborted meta = txn %d pages %d", aborted.meta.TxnID, aborted.meta.PageCount)
			}
			wantHead := uint32(5)
			if sealed {
				wantHead = 0
			}
			if aborted.dirtyHead != wantHead {
				t.Fatalf("aborted dirty head = %d, want %d", aborted.dirtyHead, wantHead)
			}
			if aborted.privatePages != 1 {
				t.Fatalf("aborted private pages = %d, want 1", aborted.privatePages)
			}

			retry, err := NewDraft(core.base.Meta, [16]byte{4})
			if err != nil {
				t.Fatal(err)
			}
			if retry.meta.TxnID != aborted.meta.TxnID {
				t.Fatalf("retry txn = %d, want %d", retry.meta.TxnID, aborted.meta.TxnID)
			}
			store2 := NewDraftStore(core.m, core.base.Meta.PageCount, budget, retry)
			if err := store2.claimAllocated(5); err != nil {
				t.Fatal(err)
			}
			initRangeLeaf(t, store2, 5, retry.meta.TxnID)
			if err := store2.sealPrivatePages(nil); err != nil {
				t.Fatal(err)
			}
			if retry.dirtyHead != 0 {
				t.Fatal("retry dirty head not cleared")
			}
			if retry.privatePages != 1 {
				t.Fatalf("retry private pages = %d, want 1", retry.privatePages)
			}
			page, err := core.m.Page(5)
			if err != nil {
				t.Fatal(err)
			}
			if !format.PageChecksumValid(page) {
				t.Fatal("retried page is not checksum-valid after seal")
			}
		})
	}
}

// TestDirectPageUpdateRejectsACommittedPageBeforeMutation mirrors
// direct_page_update_rejects_a_committed_page_before_mutation: a page
// committed by a previous draft cannot be updated in the next draft, and
// the refusal happens before any mutation.
func TestDirectPageUpdateRejectsACommittedPageBeforeMutation(t *testing.T) {
	path := makeEmptyDBPages(t, 10)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	core, err := Open(path, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()

	first, err := NewDraft(core.base.Meta, [16]byte{3})
	if err != nil {
		t.Fatal(err)
	}
	store := NewDraftStore(core.m, core.base.Meta.PageCount, budget, first)
	if err := store.claimAllocated(5); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(5, func(page []byte) error {
		format.InitializePageHeader(page, format.PageTypeRangeLeaf, first.meta.TxnID, 0, 0,
			format.SlottedHeaderSize, format.PageSize, uint32(format.AddressFamilyIPv4))
		page[100] = 0xa5
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	committed := first.Meta()

	next, err := NewDraft(committed, [16]byte{4})
	if err != nil {
		t.Fatal(err)
	}
	store2 := NewDraftStore(core.m, committed.PageCount, budget, next)
	before := make([]byte, 1)
	if err := store2.Inspect(5, func(page []byte) error {
		before[0] = page[100]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if before[0] != 0xa5 {
		t.Fatalf("committed byte = %#x, want 0xa5", before[0])
	}
	if err := store2.Update(5, func(page []byte) error {
		page[100] = 0x5a
		return nil
	}); err == nil {
		t.Fatal("update of a committed page succeeded")
	} else if errCode(err) != format.CodeFormatInvalid {
		t.Fatalf("error code = %d, want FormatInvalid", errCode(err))
	}
	if err := store2.Inspect(5, func(page []byte) error {
		if page[100] != before[0] {
			t.Fatalf("committed page mutated by the refused update: %#x", page[100])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSealPrivatePagesSealsOnlyDataPages mirrors the sealing half of
// mutation_defers_each_data_page_checksum_until_prepare: private-stack
// pages are never checksummed, data pages are, and the chain clears.
func TestSealPrivatePagesSealsOnlyDataPages(t *testing.T) {
	path := makeEmptyDB(t)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	draft, store, core := openDraftStore(t, path, budget, [16]byte{3})

	first, err := store.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	initRangeLeaf(t, store, first, draft.meta.TxnID)
	if err := store.DiscardPrivate(second); err != nil {
		t.Fatal(err)
	}

	if err := store.sealPrivatePages(func() error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if draft.dirtyHead != 0 {
		t.Fatal("dirty chain not cleared")
	}
	dataPage, err := core.m.Page(first)
	if err != nil {
		t.Fatal(err)
	}
	stackPage, err := core.m.Page(second)
	if err != nil {
		t.Fatal(err)
	}
	if !format.PageChecksumValid(dataPage) {
		t.Fatal("data page is not sealed")
	}
	if format.PageChecksumValid(stackPage) {
		t.Fatal("private-stack page was sealed")
	}
	if format.U32(dataPage[format.PageChecksumOffset:]) == format.U32(stackPage[format.PageChecksumOffset:]) {
		t.Fatal("sealed checksum equals the private marker tag")
	}
}

// TestSealPrivatePagesRejectsCorruptChains covers the three dirty-chain
// corruption classes of the Rust walk: a self-referential link, a data
// page born in another transaction, and a chain longer than the charge.
func TestSealPrivatePagesRejectsCorruptChains(t *testing.T) {
	t.Run("self-link", func(t *testing.T) {
		path := makeEmptyDBPages(t, 10)
		budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
		_, store, core := openDraftStore(t, path, budget, [16]byte{3})
		if err := store.claimAllocated(5); err != nil {
			t.Fatal(err)
		}
		if err := store.claimAllocated(6); err != nil {
			t.Fatal(err)
		}
		page, err := core.m.Page(6)
		if err != nil {
			t.Fatal(err)
		}
		format.PutU32(page[format.PageChecksumOffset:], 6)
		if err := store.sealPrivatePages(nil); err == nil {
			t.Fatal("self-linking dirty chain accepted")
		}
	})

	t.Run("wrong transaction", func(t *testing.T) {
		path := makeEmptyDBPages(t, 10)
		budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
		draft, store, core := openDraftStore(t, path, budget, [16]byte{3})
		if err := store.claimAllocated(5); err != nil {
			t.Fatal(err)
		}
		initRangeLeaf(t, store, 5, draft.meta.TxnID)
		page, err := core.m.Page(5)
		if err != nil {
			t.Fatal(err)
		}
		format.PutU64(page[format.HeaderBorn:], draft.meta.TxnID+1)
		if err := store.sealPrivatePages(nil); err == nil {
			t.Fatal("data page born in another transaction accepted")
		}
	})

	t.Run("cyclic charge", func(t *testing.T) {
		path := makeEmptyDBPages(t, 10)
		budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
		draft, store, _ := openDraftStore(t, path, budget, [16]byte{3})
		if err := store.claimAllocated(5); err != nil {
			t.Fatal(err)
		}
		if err := store.claimAllocated(6); err != nil {
			t.Fatal(err)
		}
		draft.privatePages = 1
		if err := store.sealPrivatePages(nil); err == nil {
			t.Fatal("chain longer than the private charge accepted")
		}
	})
}

// TestRetireOneFeedsTheRetirementRoot mirrors retire_one in the Rust draft
// store: retired pages land in the retirement tree as coalesced extents,
// and the tree COW path retires its generated pages out of band.
func TestRetireOneFeedsTheRetirementRoot(t *testing.T) {
	path := makeEmptyDB(t)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	draft, store, _ := openDraftStore(t, path, budget, [16]byte{3})

	if err := store.retireOne(2); err != nil {
		t.Fatal(err)
	}
	if draft.meta.RetirementRoot == 0 {
		t.Fatal("retirement root not created")
	}
	if draft.meta.RetiredExtentCount != 1 {
		t.Fatalf("retired extent count = %d, want 1", draft.meta.RetiredExtentCount)
	}

	// A neighbor coalesces into the same extent; a distant page opens one
	// more extent.
	if err := store.retireOne(3); err != nil {
		t.Fatal(err)
	}
	if draft.meta.RetiredExtentCount != 1 {
		t.Fatalf("neighbor did not coalesce: count = %d", draft.meta.RetiredExtentCount)
	}
	if err := store.retireOne(5); err != nil {
		t.Fatal(err)
	}
	if draft.meta.RetiredExtentCount != 2 {
		t.Fatalf("extent count = %d, want 2", draft.meta.RetiredExtentCount)
	}

	first, err := retire.First(store, draft.meta.RetirementRoot)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil {
		t.Fatal("no retirement extent")
	}
	if first.Key.Txn != draft.meta.TxnID || first.Key.First != 2 || first.Count != 2 {
		t.Fatalf("extent = txn %d first %d count %d, want %d/2/2",
			first.Key.Txn, first.Key.First, first.Count, draft.meta.TxnID)
	}
	if draft.privatePages == 0 {
		t.Fatal("retirement tree pages were not charged")
	}
}

// TestRetirePagesDrainsTheAllocatorBacklog pins drain_allocator_retired:
// pages that bitmap edits defer are retired before RetirePages returns.
func TestRetirePagesDrainsTheAllocatorBacklog(t *testing.T) {
	path := makeEmptyDB(t)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	draft, store, _ := openDraftStore(t, path, budget, [16]byte{3})

	if err := draft.allocatorRetired.Push(2); err != nil {
		t.Fatal(err)
	}
	if err := store.RetirePages(nil); err != nil {
		t.Fatal(err)
	}
	if draft.allocatorRetired.Len() != 0 {
		t.Fatal("allocator backlog not drained")
	}
	if draft.meta.RetirementRoot == 0 || draft.meta.RetiredExtentCount != 1 {
		t.Fatalf("backlog page not retired: root %d count %d",
			draft.meta.RetirementRoot, draft.meta.RetiredExtentCount)
	}
}

// TestAllocateUsesThePrivateStackFirst pins the allocation order: a
// discarded page is reused before any new page is claimed.
func TestAllocateUsesThePrivateStackFirst(t *testing.T) {
	path := makeEmptyDBPages(t, 10)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	draft, store, _ := openDraftStore(t, path, budget, [16]byte{3})

	if err := store.claimAllocated(5); err != nil {
		t.Fatal(err)
	}
	if err := store.DiscardPrivate(5); err != nil {
		t.Fatal(err)
	}
	page, err := store.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if page != 5 {
		t.Fatalf("private-stack allocation = %d, want 5", page)
	}
	if draft.meta.PageCount != 10 {
		t.Fatalf("page count = %d, want 10 (no tail growth)", draft.meta.PageCount)
	}
}

// TestAllocateBitmapPageUsesTheReserve pins the bitmap allocation order:
// the allocator reserve is consumed before the file tail.
func TestAllocateBitmapPageUsesTheReserve(t *testing.T) {
	path := makeEmptyDBPages(t, 10)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	draft, store, _ := openDraftStore(t, path, budget, [16]byte{3})

	draft.meta.AllocatorReserve[0] = 5
	if draft.meta.PageCount != 10 {
		t.Fatalf("page count = %d, want 10", draft.meta.PageCount)
	}
	page, err := store.AllocateBitmapPage()
	if err != nil {
		t.Fatal(err)
	}
	if page != 5 {
		t.Fatalf("reserve allocation = %d, want 5", page)
	}
	if draft.meta.AllocatorReserve[0] != 0 {
		t.Fatal("reserve slot not consumed")
	}
	if draft.meta.PageCount != 10 {
		t.Fatalf("page count = %d, want 10 after reserve use", draft.meta.PageCount)
	}
	if draft.privatePages != 1 {
		t.Fatalf("private pages = %d, want 1", draft.privatePages)
	}

	page, err = store.AllocateBitmapPage()
	if err != nil {
		t.Fatal(err)
	}
	if page != 10 {
		t.Fatalf("second bitmap allocation = %d, want tail page 10", page)
	}
}

// TestAllocationForbiddenPinsTheProtectedSet: meta pages, the reserve, and
// every persistent root can never be handed out by the free bitmap.
func TestAllocationForbiddenPinsTheProtectedSet(t *testing.T) {
	path := makeEmptyDBPages(t, 10)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	draft, store, _ := openDraftStore(t, path, budget, [16]byte{3})

	draft.meta.FreeBitmapRoot = 5
	draft.meta.RetirementRoot = 6
	draft.meta.RangeRoot = 7
	draft.meta.AllocatorReserve[2] = 8

	allowed := []uint32{2, 4, 9}
	forbidden := []uint32{0, 1, 5, 6, 7, 8}
	for _, page := range allowed {
		if store.AllocationForbidden(page) {
			t.Fatalf("page %d wrongly forbidden", page)
		}
	}
	for _, page := range forbidden {
		if !store.AllocationForbidden(page) {
			t.Fatalf("page %d not forbidden", page)
		}
	}
}

// TestDiscardPrivateRefusals covers the three discard rejection classes:
// the stack head, a committed page, and a page absent from the dirty chain.
func TestDiscardPrivateRefusals(t *testing.T) {
	t.Run("stack head", func(t *testing.T) {
		path := makeEmptyDBPages(t, 10)
		budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
		_, store, _ := openDraftStore(t, path, budget, [16]byte{3})
		if err := store.claimAllocated(5); err != nil {
			t.Fatal(err)
		}
		if err := store.DiscardPrivate(5); err != nil {
			t.Fatal(err)
		}
		if err := store.DiscardPrivate(5); err == nil {
			t.Fatal("discard of the stack head accepted")
		}
	})

	t.Run("committed page", func(t *testing.T) {
		path := makeEmptyDBPages(t, 10)
		budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
		_, store, _ := openDraftStore(t, path, budget, [16]byte{3})
		if err := store.claimAllocated(5); err != nil {
			t.Fatal(err)
		}
		if err := store.DiscardPrivate(6); err == nil {
			t.Fatal("discard of an unclaimed page accepted")
		} else if errCode(err) != format.CodeFormatInvalid {
			t.Fatalf("error code = %d, want FormatInvalid", errCode(err))
		}
	})

	t.Run("absent from dirty chain", func(t *testing.T) {
		path := makeEmptyDBPages(t, 10)
		budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
		_, store, core := openDraftStore(t, path, budget, [16]byte{3})
		if err := store.claimAllocated(5); err != nil {
			t.Fatal(err)
		}
		page, err := core.m.Page(5)
		if err != nil {
			t.Fatal(err)
		}
		format.PutU32(page[format.PageChecksumOffset:], 0)
		if err := store.DiscardPrivate(5); err == nil {
			t.Fatal("discard of an untagged private page accepted")
		}
	})
}

// TestTailAllocationGrowsTheMappingToCapacity pins ensure_tail_capacity:
// the first tail allocation extends the file and the mapping to the whole
// transaction capacity (committed + growth budget), and the draft charges
// the growth.
func TestTailAllocationGrowsTheMappingToCapacity(t *testing.T) {
	path := makeEmptyDB(t)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 5}
	draft, store, core := openDraftStore(t, path, budget, [16]byte{3})

	page, err := store.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if page != 2 {
		t.Fatalf("tail allocation = %d, want 2", page)
	}
	wantBytes := (2 + 5) * format.PageSize
	if core.m.Size() != uint64(wantBytes) {
		t.Fatalf("mapped size = %d, want %d", core.m.Size(), wantBytes)
	}
	if core.m.PhysicalSize() != uint64(wantBytes) {
		t.Fatalf("physical size = %d, want %d", core.m.PhysicalSize(), wantBytes)
	}
	if draft.growthPages != 1 || draft.privatePages != 1 {
		t.Fatalf("charges = growth %d private %d, want 1/1", draft.growthPages, draft.privatePages)
	}
}

// TestPrivateBudgetChargesEveryNewClaim pins charge_private: the private
// budget gates every fresh claim, including tail pages.
func TestPrivateBudgetChargesEveryNewClaim(t *testing.T) {
	path := makeEmptyDB(t)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 1, MaxGrowthPages: 10}
	draft, store, _ := openDraftStore(t, path, budget, [16]byte{3})

	if _, err := store.Allocate(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Allocate(); err == nil {
		t.Fatal("second claim accepted beyond the private budget")
	} else if errCode(err) != format.CodeInsufficientResourceBudget {
		t.Fatalf("error code = %d, want InsufficientResourceBudget", errCode(err))
	}
	if draft.meta.PageCount != 4 {
		t.Fatalf("page count = %d, want 4 (Rust mutates before the charge)", draft.meta.PageCount)
	}
	if draft.growthPages != 2 {
		t.Fatalf("growth = %d, want 2", draft.growthPages)
	}
}
