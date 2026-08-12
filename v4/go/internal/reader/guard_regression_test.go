package reader

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Regression tests for the round-2 six-agent review findings. Each test
// fails on the pre-fix code.

// catalogNameLeaf builds one canonical catalog name leaf page with a single
// record for name "a" (record_len 13, feed index feedIndex).
func catalogNameLeaf(t *testing.T, feedIndex uint32) []byte {
	t.Helper()
	const upper = 4096 - 13
	p := make([]byte, format.PageSize)
	header(p, format.PageTypeCatalogNameLeaf, 0, 0, 1, 32+2, upper)
	format.PutU16(p[32:34], upper)
	format.PutU16(p[upper:upper+2], 13)  // record_len
	format.PutU16(p[upper+2:upper+4], 0) // flags
	format.PutU32(p[upper+4:upper+8], feedIndex)
	p[upper+8] = 1 // name_len
	p[upper+12] = 'a'
	return p
}

// TestCatalogFeedIndexLimit pins the feed_catalog.rs decode_leaf rule: any
// probed catalog record whose index is at or above the committed
// feed_index_limit is corruption, never a served entry.
func TestCatalogFeedIndexLimit(t *testing.T) {
	sl, err := format.OpenSlotted(catalogNameLeaf(t, 5), 2, format.PageTypeCatalogNameLeaf, 0, format.SlotItemsPerPage)
	if err != nil {
		t.Fatal(err)
	}
	entry, found, err := nameLeafLookup(sl, "a", 100)
	if err != nil || !found || entry.FeedIndex != 5 {
		t.Fatalf("valid record: %+v %v %v", entry, found, err)
	}
	// Absent name stays a clean miss.
	if _, found, err := nameLeafLookup(sl, "b", 100); err != nil || found {
		t.Fatalf("absent name: %v %v", found, err)
	}

	sl, err = format.OpenSlotted(catalogNameLeaf(t, 0xffffffff), 2, format.PageTypeCatalogNameLeaf, 0, format.SlotItemsPerPage)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := nameLeafLookup(sl, "a", 10); err == nil || !isFormatError(err, format.CodeFormatInvalid) {
		t.Fatalf("index beyond limit accepted: %v", err)
	}
}

// TestMetaKindClassification pins the bootstrap error classes: a registered
// but invalid kind combination is FormatInvalid, only an unknown kind is the
// typed UnsupportedStructure (spec section 4.1/4.3; bootstrap.rs
// KindInvariant).
func TestMetaKindClassification(t *testing.T) {
	// Direct file with the registered structured kind (1): invalid
	// combination -> FormatInvalid (32).
	t.Run("direct-kind1", func(t *testing.T) {
		path := copyFixture(t, "direct-ipv4.iprdb", "direct-kind1.iprdb")
		patchMeta(t, path, func(page []byte) { page[13] = 1 })
		if _, err := OpenImmutable(path); mustCode(err) != format.CodeFormatInvalid {
			t.Fatalf("code %v want 32", err)
		}
	})
	// Structured file with structure_kind == 0: a known wire value on a
	// structured file -> FormatInvalid, not UnsupportedStructure.
	t.Run("structured-kind0", func(t *testing.T) {
		path := copyFixture(t, "structured-ipv4.iprdb", "structured-kind0.iprdb")
		patchMeta(t, path, func(page []byte) { page[13] = 0 })
		if _, err := OpenImmutable(path); mustCode(err) != format.CodeFormatInvalid {
			t.Fatalf("code %v want 32", err)
		}
	})
	// Structured file with an unknown kind (2): typed UnsupportedStructure.
	t.Run("structured-kind2", func(t *testing.T) {
		path := copyFixture(t, "structured-ipv4.iprdb", "structured-kind2.iprdb")
		patchMeta(t, path, func(page []byte) { page[13] = 2 })
		if _, err := OpenImmutable(path); mustCode(err) != format.CodeUnsupportedStructure {
			t.Fatalf("code %v want 67", err)
		}
	})
	// Membership file with the registered structured kind (1): invalid
	// combination -> FormatInvalid.
	t.Run("membership-kind1", func(t *testing.T) {
		path := copyFixture(t, "membership-ipv4.iprdb", "membership-kind1.iprdb")
		patchMeta(t, path, func(page []byte) { page[13] = 1 })
		if _, err := OpenImmutable(path); mustCode(err) != format.CodeFormatInvalid {
			t.Fatalf("code %v want 32", err)
		}
	})
	// Membership file with an unknown kind (2): typed UnsupportedStructure.
	t.Run("membership-kind2", func(t *testing.T) {
		path := copyFixture(t, "membership-ipv4.iprdb", "membership-kind2.iprdb")
		patchMeta(t, path, func(page []byte) { page[13] = 2 })
		if _, err := OpenImmutable(path); mustCode(err) != format.CodeUnsupportedStructure {
			t.Fatalf("code %v want 67", err)
		}
	})
}

// TestSoleMetaGeometry pins the per-meta physical-geometry validity rule:
// a meta whose page_count*4096 exceeds the physical file length is not
// bootstrap-valid, so selection falls to the sole valid meta (Rust
// validate_generation PhysicalLength + select_candidates), and the
// immutable open then requires the exact committed size.
func TestSoleMetaGeometry(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "sole-geo.iprdb")
	patchMetaEach(t, path, func(pg int, page []byte) {
		if pg == 0 {
			format.PutU64(page[48:56], 4) // txn
			format.PutU64(page[72:80], 5) // page_count: 5*4096 > 4*4096 physical
		} else {
			format.PutU64(page[48:56], 3) // txn
			format.PutU64(page[72:80], 4) // page_count: fits the physical file
		}
	})
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal("open:", err)
	}
	defer r.Close()
	if r.Selection() != MetaSelectionSoleMeta1 {
		t.Fatalf("selection %v want SoleMeta1", r.Selection())
	}
	if r.Meta().TxnID != 3 || r.Meta().PageCount != 4 {
		t.Fatalf("meta txn=%d page_count=%d want 3/4", r.Meta().TxnID, r.Meta().PageCount)
	}
	v, found, err := r.LookupDirect4(0x0a00000a)
	if err != nil || !found || v != 2 {
		t.Fatalf("lookup: value=%d found=%v err=%v", v, found, err)
	}
}

// TestMetadataFCheckRejected pins the RFC 1950 header check: a stream whose
// (CMF*256+FLG) % 31 != 0 is not valid zlib and must be corruption even
// though Go's flate would strip the header without noticing.
func TestMetadataFCheckRejected(t *testing.T) {
	path := copyFixture(t, "membership-ipv6.iprdb", "meta-fcheck.iprdb")
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Chunk page 9 holds the single compressed stream at data offset 48;
	// 0x78 0x9b has CM=8, CINFO=7, FDICT=0, but (0x78*256+0x9b) % 31 = 30.
	if _, err := file.WriteAt([]byte{0x78, 0x9b}, int64(9*format.PageSize+48)); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, _, err := r.ReadMetadataJSON(); err == nil || !isFormatError(err, format.CodeFormatInvalid) {
		t.Fatalf("invalid FCHECK accepted: %v", err)
	}
}

// TestBlobBranchProbedChildValidation pins blob_tree.rs select_branch: every
// probed branch entry's child must be a valid non-meta page, not only the
// selected entry. The test patches the second branch entry (probed during a
// read of leaf 1, but not selected) to a meta page.
func TestBlobBranchProbedChildValidation(t *testing.T) {
	path := buildBlobDatabase(t)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Branch page 4, entry 1 (second entry, offset 4088:4092): child 6 -> 7,
	// a page number valid in global terms but beyond this database's
	// committed page count (7 pages: 0..6).
	if _, err := file.WriteAt([]byte{7, 0, 0, 0}, int64(4*format.PageSize+4088)); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Reading word 0 of leaf 1 probes branch entry 1 (offset 4048 > 0) and
	// selects entry 0; the malformed probed entry must abort the read.
	view, found, err := r.LookupMembership4(0x0a000000)
	if err != nil || !found {
		t.Fatalf("lookup: %v %v", found, err)
	}
	if _, _, err := view.Word(0); err == nil || !isFormatError(err, format.CodeFormatInvalid) {
		t.Fatalf("probed branch child accepted: %v", err)
	}
}

// TestBlobGapRejectedCorruption pins the blob coverage check against the
// unsigned-underflow class: a blob tree with a gap (second branch entry
// starting past the first leaf's end) must reject a request in the gap as
// corruption. Before the off > end guard this request wrapped end-off and
// either panicked on the leaf slice or returned bytes from outside the leaf
// extent.
func TestBlobGapRejectedCorruption(t *testing.T) {
	path := buildBlobDatabase(t)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Branch page 4, entry 1 (offset 4080:4088): first logical offset
	// 4048 -> 5000, leaving a gap (4048, 5000] in the declared 4800-byte
	// blob; word 512 (byte 4096) falls into the gap.
	if _, err := file.WriteAt([]byte{0x88, 0x13, 0, 0, 0, 0, 0, 0}, int64(4*format.PageSize+4080)); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	view, found, err := r.LookupMembership4(0x0a000000)
	if err != nil || !found {
		t.Fatalf("lookup: %v %v", found, err)
	}
	if _, _, err := view.Word(512); err == nil || !isFormatError(err, format.CodeFormatInvalid) {
		t.Fatalf("blob gap read accepted or wrong error: %v", err)
	}
}
