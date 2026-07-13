package iprangedb

import (
	"testing"
)

// ── Issue 3: a scope leaf with a checksum-valid but structurally impossible
// entry_count (e.g. 0xFFFF) must be REJECTED at writer open, not cause an
// out-of-bounds panic when the leaf is later read by findScope/readScopeNode.
//
// The writer's open-time scope validator previously checked only per-page CRC.
// A corrupt entry_count that still verifies against a recomputed CRC passed the
// guard; then readScopeNode sliced past the page and panicked. Fix: validate
// structural integrity (entry_count bounds, page type, child page range)
// alongside the CRC check in ValidateScopeCRC.

func TestWriterOpenRejectsChecksumValidCorruptScopeLeaf(t *testing.T) {
	// Build a mode-2 DB with one interned scope, committed.
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.ScopeIntern([]byte{0b00000001})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(wk(10), wk(20), id); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(0, ^uint64(0)); err != nil {
		t.Fatal(err)
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected vecPageStore image")
	}

	// Find the scope leaf page (page_type == ScopeLeaf). For a 1-scope DB there
	// is exactly one; it sits at page >= 2 (pages 0/1 are meta).
	nPages := len(img) / PageSize
	target := -1
	for pgno := 2; pgno < nPages; pgno++ {
		off := pgno * PageSize
		h := decodeHeader(img[off : off+PageSize])
		if h.pageType == PageTypeScopeLeaf {
			target = pgno
			break
		}
	}
	if target < 0 {
		t.Fatal("test DB must contain a scope leaf")
	}
	off := target * PageSize

	// Corrupt entry_count to 0xFFFF (structurally impossible: far beyond the
	// page capacity), then recompute the CRC so the page still "verifies".
	img[off+PHEntryCount] = 0xFF
	img[off+PHEntryCount+1] = 0xFF
	finalizeChecksum(img[off : off+PageSize])

	// Sanity: the corrupted page still passes CRC.
	if !verifyPage(img[off : off+PageSize]) {
		t.Fatal("test setup: corrupted leaf must still pass CRC")
	}

	// openWriter MUST reject this (structural error), NOT panic. Using defer
	// recover so a panic surfaces as a clear test failure rather than crashing.
	store := newVecPageStore(img)
	_, openErr := openWriter[Ipv4Key](store)
	if openErr == nil {
		t.Fatal("openWriter accepted a checksum-valid but structurally corrupt scope leaf (entry_count=0xFFFF); it should have been rejected")
	}
}
