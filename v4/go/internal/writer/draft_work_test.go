//go:build v4work

package writer

// Necessary-work pins for the draft storage surface, mirroring the Rust
// draft_store_tests.rs work assertions: discard_private moves exactly the
// private-marker bytes and zeroes nothing, each fresh claim zeroes the
// page head and stamps the marker, sealing seals only data pages, and the
// first tail allocation grows the file once.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

func expectDraftCounters(t *testing.T, want work.Snapshot) {
	t.Helper()
	got := work.Read()
	if got != want {
		t.Fatalf("work counters = %+v, want %+v", got, want)
	}
}

// TestDiscardPrivateWorkPin mirrors the Rust bytes_zeroed == 0 assertion of
// reusing_a_current_transaction_page_keeps_one_dirty_chain_entry: pushing
// a page onto the private stack moves the 16 marker bytes and zeroes
// nothing.
func TestDiscardPrivateWorkPin(t *testing.T) {
	path := makeEmptyDBPages(t, 10)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	_, store, _ := openDraftStore(t, path, budget, [16]byte{3})

	if err := store.claimAllocated(5); err != nil {
		t.Fatal(err)
	}
	page, tag, err := store.Update(5)
	if err != nil {
		t.Fatal(err)
	}
	page[100] = 0xa5
	if err := store.RestoreDirty(5, tag); err != nil {
		t.Fatal(err)
	}
	work.Reset()
	if err := store.DiscardPrivate(5); err != nil {
		t.Fatal(err)
	}
	expectDraftCounters(t, work.Snapshot{BytesMoved: 16})
}

// TestClaimPageWorkPin pins the fresh-claim cost: the page head is zeroed,
// the 16 marker bytes are written, and one page is created.
func TestClaimPageWorkPin(t *testing.T) {
	path := makeEmptyDB(t)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 3}
	_, store, _ := openDraftStore(t, path, budget, [16]byte{3})

	work.Reset()
	if _, err := store.Allocate(); err != nil {
		t.Fatal(err)
	}
	expectDraftCounters(t, work.Snapshot{
		PagesCreated:   1,
		BytesZeroed:    32,
		BytesMoved:     16,
		MappingGrowths: 1,
	})
}

// TestSealDataPageWorkPin pins that sealing visits and stamps exactly the
// data pages of the dirty chain: two claims, one initialized data page
// and one discarded private-stack page, seal exactly one page.
func TestSealDataPageWorkPin(t *testing.T) {
	path := makeEmptyDB(t)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	draft, store, _ := openDraftStore(t, path, budget, [16]byte{3})

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

	work.Reset()
	if err := store.sealPrivatePages(nil); err != nil {
		t.Fatal(err)
	}
	// Two dirty-chain visits (checkpoint per entry) and one data-page
	// seal: 8 bytes moved per stamp (zero + checksum).
	expectDraftCounters(t, work.Snapshot{
		PagesSealed: 1,
		BytesMoved:  8,
	})
}

// TestSealSkipsUninitializedClaims pins the guard in the Rust seal walk:
// an uninitialized private page has no page magic and is never sealed.
func TestSealSkipsUninitializedClaims(t *testing.T) {
	path := makeEmptyDBPages(t, 10)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	_, store, core := openDraftStore(t, path, budget, [16]byte{3})

	if err := store.claimAllocated(5); err != nil {
		t.Fatal(err)
	}
	work.Reset()
	if err := store.sealPrivatePages(nil); err != nil {
		t.Fatal(err)
	}
	expectDraftCounters(t, work.Snapshot{})
	page, err := core.m.Page(5)
	if err != nil {
		t.Fatal(err)
	}
	if format.U32(page[format.PageChecksumOffset:]) == 0 {
		t.Fatal("private-stack page lost its dirty tag")
	}
}
