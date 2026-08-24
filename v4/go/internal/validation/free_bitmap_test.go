package validation

// Slice-D free-bitmap validator tests: synthetic committed generations
// whose free bitmap marks the unclaimed pages, over single-leaf and
// branch+leaf roots, with the exact reason classes for the header, the
// reserved-page-pair, the item-count, and the summary-bit arms.

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// freeBitmapDB builds one committed generation: the given free bitmap
// root at page 2 and the given extra pages, with all remaining pages
// zero.
func freeBitmapDB(t *testing.T, pageCount int, pages ...[]byte) string {
	t.Helper()
	meta := metaPage(2, uint64(pageCount))
	format.PutU32(meta[176:180], 2) // FreeBitmapRoot
	format.PutU32(meta[252:256], format.MetaCRC32C(meta))
	return dbWithMeta(t, meta, uint64(pageCount), pages...)
}

// freeLeaf builds one free-kind bitmap leaf page carrying the given
// words (born 2, item count equal to the nonzero words).
func freeLeaf(t *testing.T, words ...uint64) []byte {
	t.Helper()
	page := make([]byte, format.PageSize)
	bitmap.Initialize(page, 2, 0, bitmap.KindFree)
	nonzero := 0
	for i, word := range words {
		if word != 0 {
			nonzero++
		}
		if err := bitmap.SetLeafWord(page, i, word); err != nil {
			t.Fatal(err)
		}
	}
	format.PutU16(page[format.HeaderCount:], uint16(nonzero))
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestValidateFreeBitmapLeafClean(t *testing.T) {
	// Six-page generation: the free bitmap marks pages 3..5 free, page 2
	// is the bitmap root, and the sweep is a clean PASS.
	leaf := freeLeaf(t, 0b111000) // bits 3, 4, 5; bits 0-1 reserved
	path := freeBitmapDB(t, 6, leaf)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 0 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateFreeBitmapReservedPairFinding(t *testing.T) {
	// The free bitmap marks page 1 (a committed meta page): the mask
	// excludes bits 0-1 at base zero, so the walk reports the summary
	// class on the bitmap page; pages 3..5 stay marked so the partition
	// stays clean.
	leaf := freeLeaf(t, 0b111010) // bits 1, 3, 4, 5: bit 1 is reserved
	path := freeBitmapDB(t, 6, leaf)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 1 || findings[0].Reason != ReasonBitmapSummaryInvalid ||
		findings[0].Object != ObjectFreeBitmap || findings[0].PageNumber == nil || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateFreeBitmapItemCountFinding(t *testing.T) {
	// The leaf declares two words but carries one: the header is healthy
	// (nonzero count) and the totals arm reports the header class on the
	// bitmap page after the free bits keep the partition claimed.
	leaf := freeLeaf(t, 0b111000) // bits 3, 4, 5
	format.PutU16(leaf[format.HeaderCount:], 2)
	if err := format.SealPageChecksum(leaf); err != nil {
		t.Fatal(err)
	}
	path := freeBitmapDB(t, 6, leaf)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 1 || findings[0].Reason != ReasonPageHeaderInvalid ||
		findings[0].Object != ObjectFreeBitmap || findings[0].PageNumber == nil || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

// sparseFreeBitmapDB builds one sparse 64000-page generation: the meta
// pair, the branch root at page 2, and one leaf at page 3 marking pages
// 4..31999 free; pages 32000..63999 stay as a file hole (zero pages)
// under the absent children of the branch. The generation is used for
// targeted free-bitmap walks only (the hole pages are not free-marked,
// so a full sweep would report them unclaimed).
func sparseFreeBitmapDB(t *testing.T, branch, leaf []byte) string {
	t.Helper()
	const pageCount = 64000 // two leaf spans under the branch, sparse
	meta := metaPage(2, pageCount)
	format.PutU32(meta[176:180], 2) // FreeBitmapRoot
	format.PutU32(meta[252:256], format.MetaCRC32C(meta))
	path := t.TempDir() + "/database.iprdb"
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range [][]byte{meta, make([]byte, format.PageSize), branch, leaf} {
		if _, err := f.Write(page); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Truncate(int64(pageCount) * format.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// freeBranchDB builds the one-leaf free bitmap of a 64000-page
// generation: the branch child 0 covers the leaf at page 3, children
// 1..255 are absent, and the leaf marks pages 4..31999 free.
func freeBranchDB(t *testing.T, mutate func(branch []byte)) (string, *context) {
	t.Helper()
	branch := make([]byte, format.PageSize)
	bitmap.Initialize(branch, 2, 1, bitmap.KindFree)
	leaf := make([]byte, format.PageSize)
	bitmap.Initialize(leaf, 2, 0, bitmap.KindFree)
	for word := 0; word < bitmap.LeafWords; word++ {
		value := ^uint64(0)
		if word == 0 {
			value = 0xFFFF_FFFF_FFFF_FFF0 // bits 4..63: pages 2 and 3 stay claimed by the walk
		}
		if err := bitmap.SetLeafWord(leaf, word, value); err != nil {
			t.Fatal(err)
		}
	}
	format.PutU16(leaf[format.HeaderCount:], bitmap.LeafWords)
	if err := format.SealPageChecksum(leaf); err != nil {
		t.Fatal(err)
	}
	if err := bitmap.SetBranchChild(branch, 0, 3); err != nil {
		t.Fatal(err)
	}
	if err := bitmap.SetSummary(branch, 0, true); err != nil {
		t.Fatal(err)
	}
	format.PutU16(branch[format.HeaderCount:], 1)
	if mutate != nil {
		mutate(branch)
	}
	if err := format.SealPageChecksum(branch); err != nil {
		t.Fatal(err)
	}
	path := sparseFreeBitmapDB(t, branch, leaf)
	return path, fixturePathContext(t, path, 2<<20)
}

func TestValidateFreeBitmapBranchClean(t *testing.T) {
	// The branch root over one leaf and 255 absent children: the
	// present child matches its has-one summary, the absent children
	// contribute no candidate span for the free kind, and the walk is
	// clean.
	_, ctx := freeBranchDB(t, nil)
	if findings := collectContextFindings(t, ctx, validateFreeBitmap); len(findings) != 0 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateFreeBitmapBranchSummaryFinding(t *testing.T) {
	// The summary bit of the present first child is cleared: the branch
	// expects has-one and reports the summary class on the branch page.
	_, ctx := freeBranchDB(t, func(branch []byte) {
		if err := bitmap.SetSummary(branch, 0, false); err != nil {
			t.Fatal(err)
		}
	})
	findings := collectContextFindings(t, ctx, validateFreeBitmap)
	if len(findings) != 1 || findings[0].Reason != ReasonBitmapSummaryInvalid ||
		findings[0].Object != ObjectFreeBitmap || findings[0].PageNumber == nil || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateFreeBitmapBranchAbsentSummaryFinding(t *testing.T) {
	// A summary bit on an absent child (the free kind expects no
	// candidate span from absent children) is the summary class.
	_, ctx := freeBranchDB(t, func(branch []byte) {
		if err := bitmap.SetSummary(branch, 1, true); err != nil {
			t.Fatal(err)
		}
	})
	findings := collectContextFindings(t, ctx, validateFreeBitmap)
	if len(findings) != 1 || findings[0].Reason != ReasonBitmapSummaryInvalid ||
		findings[0].Object != ObjectFreeBitmap || findings[0].PageNumber == nil || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}
