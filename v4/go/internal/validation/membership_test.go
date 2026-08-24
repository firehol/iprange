package validation

// Slice-D membership validator tests: the full sweep over the corpus
// membership/structured fixtures, targeted mutations of the dictionary
// records, the reverse-index records, the used bitmap, and the range
// values producing the exact Rust reason classes in walk order, and a
// synthetic blob-backed dictionary record proving the blob scan paths.

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// collectFindingsHeap runs one validation over a path with the given
// heap budget (the fixture metadata chain needs more than the sweep
// helper's 1 MiB).
func collectFindingsHeap(t *testing.T, path string, maxHeap uint64) []ValidationFinding {
	t.Helper()
	var findings []ValidationFinding
	_, failure := Validate(path, ValidationModeImmutableCurrent, HeapOnly(maxHeap, 1), nil, SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		findings = append(findings, *f)
		return SinkContinue, nil
	}))
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	return findings
}

// sweepFixture runs the full sweep over one corpus fixture with a 2 MiB
// heap budget (the metadata-chain fixtures reserve up to 1.1 MiB).
func sweepFixture(t *testing.T, fixture string) []ValidationFinding {
	t.Helper()
	return collectFindingsHeap(t, fixturePath(t, fixture), 2<<20)
}

// membershipIDLeaf returns the membership ID leaf page of one fixture
// and its page number.
func membershipIDLeaf(t *testing.T, raw []byte, pages int) ([]byte, int) {
	t.Helper()
	return descendToLeaf(t, raw, pages, int(format.U32(raw[160:164])), format.MembershipIDBranchSize)
}

// membershipHashLeaf returns the membership hash leaf page of one
// fixture and its page number.
func membershipHashLeaf(t *testing.T, raw []byte, pages int) ([]byte, int) {
	t.Helper()
	return descendToLeaf(t, raw, pages, int(format.U32(raw[164:168])), format.MembershipHashBranchSize)
}

// descendToLeaf walks one fixed-branch tree to its rightmost leaf.
func descendToLeaf(t *testing.T, raw []byte, pages int, pageNumber int, branchLen int) ([]byte, int) {
	t.Helper()
	for {
		page := raw[pageNumber*format.PageSize : (pageNumber+1)*format.PageSize]
		h := mutationHeader(page)
		if h.Level == 0 {
			return page, pageNumber
		}
		sl, err := format.OpenSlottedHeader(page, h, format.PageType(h.PageType), h.Aux, format.SlotItemsPerPage)
		if err != nil {
			t.Fatal(err)
		}
		cell := boundedRecord(sl, int(h.ItemCount)-1, branchLen)
		var child uint32
		switch h.PageType {
		case format.PageTypeMembershipIDBranch:
			_, child, err = format.DecodeMembershipIDBranchFields(cell)
		case format.PageTypeMembershipHashBranch:
			_, child, err = format.DecodeMembershipHashBranchFields(cell)
		case format.PageTypeRangeBranch:
			_, child, err = format.DecodeRangeEntryFieldsV4(cell)
		default:
			t.Fatalf("unexpected branch type %d", h.PageType)
		}
		if err != nil {
			t.Fatal(err)
		}
		pageNumber = int(child)
		if pageNumber < 2 || pageNumber >= pages {
			t.Fatal("child outside the database")
		}
	}
}

// membershipRecord returns one membership record of the ID leaf.
func membershipRecord(t *testing.T, raw []byte, pages int, index int) ([]byte, int, format.MembershipRecord) {
	t.Helper()
	page, pageNumber := membershipIDLeaf(t, raw, pages)
	sl, err := format.OpenSlottedHeader(page, mutationHeader(page), format.PageType(page[4]), format.U32(page[24:28]), format.SlotItemsPerPage)
	if err != nil {
		t.Fatal(err)
	}
	rec := boundedRecord(sl, index, 0)
	r, err := format.DecodeMembershipRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	return rec, pageNumber, r
}

// recomputeMembershipDigest rewrites the digest field of one membership
// record to the sha256 of its bitmap words.
func recomputeMembershipDigest(rec []byte) {
	r, err := format.DecodeMembershipRecord(rec)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(rec[format.MembershipIDRecordMin : format.MembershipIDRecordMin+int(r.WordCount)*8])
	copy(rec[32:64], sum[:])
}

// readFixtureRaw loads one fixture into mutable bytes.
func readFixtureRaw(t *testing.T, fixture string) ([]byte, int) {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	return raw, len(raw) / format.PageSize
}

// writeRawPath writes one raw database image and returns its path.
func writeRawPath(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.iprdb")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateMembershipCorpusClean(t *testing.T) {
	// The full sweep over the membership fixtures and the direct
	// control: the dictionary walks, the used bitmaps, the feed
	// windows, the slots, and the free bitmap arm stay clean. The
	// structured fixture reaches a clean sweep with the slice-E
	// structure walk (it counts the membership owners and claims the
	// structure pages); only its dictionary walk is exercised here.
	for _, name := range []string{"membership-ipv4.iprdb", "membership-ipv6.iprdb", "direct-ipv4.iprdb"} {
		if findings := sweepFixture(t, name); len(findings) != 0 {
			t.Fatalf("%s findings: %+v", name, findings)
		}
	}
	ctx := fixturePathContext(t, fixturePath(t, "structured-ipv4.iprdb"), 2<<20)
	if findings := collectContextFindings(t, ctx, validateMembership); len(findings) != 2 {
		t.Fatalf("structured dictionary walk findings %+v (want the two pending-refcount classes)", findings)
	} else if findings[0].Reason != ReasonMembershipRefcountInvalid || findings[1].Reason != ReasonMembershipRefcountInvalid {
		t.Fatalf("structured dictionary walk findings %+v", findings)
	}
}

func TestValidateMembershipRefcountZeroFinding(t *testing.T) {
	// The record refcount is zero: the record arm reports it on the id
	// page and the slot finish repeats the class without a page.
	var pageNumber int
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		rec, page, _ := membershipRecord(t, raw, pages, 0)
		pageNumber = page
		format.PutU64(rec[8:16], 0)
		return []int{page}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{ReasonMembershipRefcountInvalid, ReasonMembershipRefcountInvalid}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	if findings[0].Reason != want[0] || findings[0].PageNumber == nil || *findings[0].PageNumber != uint32(pageNumber) ||
		findings[1].Reason != want[1] || findings[1].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateMembershipIDLimitFinding(t *testing.T) {
	// The record id moves to the declared limit: the record arm reports
	// the id window on the id page, the reverse mark then misses the
	// renamed slot, the slot refcount still disagrees with the untouched
	// range count, and the totals prove the id limit.
	var pageNumber int
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		rec, page, _ := membershipRecord(t, raw, pages, 0)
		pageNumber = page
		format.PutU32(rec[4:8], 4)
		return []int{page}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{
		ReasonMembershipBitmapInvalid,       // id window on the id page
		ReasonTreeOrderInvalid,              // leaf keys 4, 2, 3
		ReasonMembershipReverseIndexInvalid, // renamed id misses its hash
		ReasonMembershipRefcountInvalid,     // undefined range-counted slot 1
		ReasonMembershipRefcountInvalid,     // slot 4 stored 1 vs range 0
		ReasonMembershipReverseIndexInvalid, // slot 4 never reverse-marked
		ReasonMembershipBitmapInvalid,       // slot 4 used bit absent
		ReasonMembershipBitmapInvalid,       // expected id limit 5 vs 4
	}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v want %v", i, findings[i], reason)
		}
	}
	if *findings[0].PageNumber != uint32(pageNumber) || *findings[1].PageNumber != uint32(pageNumber) ||
		findings[2].PageNumber == nil || *findings[2].PageNumber != 7 {
		t.Fatalf("page attribution %+v", findings)
	}
	for i := 3; i < len(findings); i++ {
		if findings[i].PageNumber != nil {
			t.Fatalf("finding %d must be page-less: %+v", i, findings[i])
		}
	}
}

func TestValidateMembershipTrailingZeroWordFinding(t *testing.T) {
	// The last bitmap word of the first record is zeroed and the digest
	// recomputed: only the shape class fires on the id page.
	var pageNumber int
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		rec, page, r := membershipRecord(t, raw, pages, 0)
		pageNumber = page
		format.PutU64(rec[64+8*(uint64(r.WordCount)-1):64+8*uint64(r.WordCount)], 0)
		recomputeMembershipDigest(rec)
		// The reverse index keeps matching once its digest follows the
		// recomputed record digest.
		hashPage, hp := membershipHashLeaf(t, raw, pages)
		sl, err := format.OpenSlottedHeader(hashPage, mutationHeader(hashPage), format.PageType(hashPage[4]), format.U32(hashPage[24:28]), format.SlotItemsPerPage)
		if err != nil {
			t.Fatal(err)
		}
		cell := boundedRecord(sl, 0, format.MembershipHashKeySize)
		copy(cell[0:32], rec[32:64])
		return []int{page, hp}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	if len(findings) != 1 || findings[0].Reason != ReasonMembershipBitmapInvalid ||
		findings[0].PageNumber == nil || *findings[0].PageNumber != uint32(pageNumber) {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateMembershipActiveFeedFinding(t *testing.T) {
	// A bitmap bit outside the active feed window: the record word 1
	// gains feed index 70 (the limit is 70); the digest is recomputed so
	// only the active-feed class fires.
	var pageNumber int
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		rec, page, r := membershipRecord(t, raw, pages, 0)
		pageNumber = page
		if r.WordCount < 2 {
			t.Fatal("fixture record must span two words")
		}
		word1 := format.U64(rec[72:80])
		format.PutU64(rec[72:80], word1|(1<<6))
		recomputeMembershipDigest(rec)
		hashPage, hp := membershipHashLeaf(t, raw, pages)
		sl, err := format.OpenSlottedHeader(hashPage, mutationHeader(hashPage), format.PageType(hashPage[4]), format.U32(hashPage[24:28]), format.SlotItemsPerPage)
		if err != nil {
			t.Fatal(err)
		}
		cell := boundedRecord(sl, 0, format.MembershipHashKeySize)
		copy(cell[0:32], rec[32:64])
		return []int{page, hp}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	if len(findings) != 1 || findings[0].Reason != ReasonMembershipActiveFeedInvalid ||
		findings[0].PageNumber == nil || *findings[0].PageNumber != uint32(pageNumber) {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateMembershipDigestAndReverseFindings(t *testing.T) {
	// The record digest diverges from its bitmap: the record arm reports
	// the hash class on the id page and the reverse-index walk then
	// fails its digest match on the hash page.
	var pageNumber int
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		rec, page, _ := membershipRecord(t, raw, pages, 0)
		pageNumber = page
		rec[32] ^= 1
		return []int{page}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{ReasonMembershipHashInvalid, ReasonMembershipReverseIndexInvalid, ReasonMembershipReverseIndexInvalid}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v want %v", i, findings[i], reason)
		}
	}
	if *findings[0].PageNumber != uint32(pageNumber) || findings[0].Object != ObjectMembershipDictionary ||
		*findings[1].PageNumber != 7 || findings[2].PageNumber != nil {
		t.Fatalf("attribution %+v", findings)
	}
}

func TestValidateMembershipDuplicateIDFinding(t *testing.T) {
	// The second record reuses the first record's id: the leaf order
	// class fires first, the duplicate define then reports the bitmap
	// class on the same page, and the totals prove two defined slots
	// against three dictionary records.
	var pageNumber int
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		_, page, first := membershipRecord(t, raw, pages, 0)
		pageNumber = page
		rec, _, _ := membershipRecord(t, raw, pages, 1)
		format.PutU32(rec[4:8], first.ID)
		return []int{page}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{
		ReasonTreeOrderInvalid,              // second record carries id 1 again
		ReasonMembershipBitmapInvalid,       // duplicate define
		ReasonMembershipReverseIndexInvalid, // id 1 already reverse-marked
		ReasonMembershipRefcountInvalid,     // slot 2 counted but undefined
		ReasonMembershipBitmapInvalid,       // two defined slots vs three records
	}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v want %v", i, findings[i], reason)
		}
	}
	if *findings[0].PageNumber != uint32(pageNumber) || *findings[1].PageNumber != uint32(pageNumber) ||
		findings[2].PageNumber == nil || *findings[2].PageNumber != 7 ||
		findings[3].PageNumber != nil || findings[4].PageNumber != nil {
		t.Fatalf("page attribution %+v", findings)
	}
}

func TestValidateMembershipReverseMissingFinding(t *testing.T) {
	// The last hash record names an id no dictionary record defines: the
	// reverse mark misses on the hash page and the slot finish repeats
	// the missing reverse class without a page.
	var hashPageNumber int
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		hashPage, page := membershipHashLeaf(t, raw, pages)
		hashPageNumber = page
		sl, err := format.OpenSlottedHeader(hashPage, mutationHeader(hashPage), format.PageType(hashPage[4]), format.U32(hashPage[24:28]), format.SlotItemsPerPage)
		if err != nil {
			t.Fatal(err)
		}
		cell := boundedRecord(sl, 2, format.MembershipHashKeySize)
		format.PutU32(cell[36:40], 5)
		return []int{page}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{ReasonMembershipReverseIndexInvalid, ReasonMembershipReverseIndexInvalid}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	if findings[0].Reason != want[0] || *findings[0].PageNumber != uint32(hashPageNumber) ||
		findings[1].Reason != want[1] || findings[1].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateMembershipUsedBitmapFindings(t *testing.T) {
	// The used bitmap loses the bit of the first dictionary id: the walk
	// count drops and the slot used check repeats the class, both
	// without a page.
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		usedPage := raw[5*format.PageSize : 6*format.PageSize]
		format.PutU64(usedPage[32:40], 12) // 0b1100: bit 1 cleared
		return []int{5}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	if len(findings) != 2 || findings[0].Reason != ReasonMembershipBitmapInvalid ||
		findings[0].PageNumber != nil || findings[1].Reason != ReasonMembershipBitmapInvalid || findings[1].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateMembershipRangeRefcountFinding(t *testing.T) {
	// One range value moves to an undefined id: the dictionary id it
	// used to name keeps its stored refcount while its range count drops
	// to zero, so the slot finish reports the refcount class without a
	// page and nothing else.
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		rangePage, rangePageNumber := descendToLeaf(t, raw, pages, int(format.U32(raw[144:148])), format.RangeEntryV4Size)
		sl, err := format.OpenSlottedHeader(rangePage, mutationHeader(rangePage), format.PageType(rangePage[4]), format.U32(rangePage[24:28]), format.SlotItemsPerPage)
		if err != nil {
			t.Fatal(err)
		}
		record := boundedRecord(sl, 0, format.RangeRecordV4Size)
		format.PutU32(record[8:12], 4)
		return []int{rangePageNumber}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	// The id that lost its range keeps its stored refcount against a zero
	// range count; the newly-counted id has no dictionary record.
	if len(findings) != 2 || findings[0].Reason != ReasonMembershipRefcountInvalid ||
		findings[0].PageNumber != nil || findings[1].Reason != ReasonMembershipRefcountInvalid ||
		findings[1].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateMembershipBlobScanCleanAndDigest(t *testing.T) {
	// One dictionary record moves to blob storage over a synthetic
	// branch+leaf tree: the clean image sweeps clean (the blob span,
	// the digest, and the partition claims all pass), and a single
	// flipped data bit turns into the digest class on the record page.
	raw, pages := readFixtureRaw(t, "membership-ipv4.iprdb")
	rec, pageNumber, record := membershipRecord(t, raw, pages, 0)
	words := make([]byte, int(record.WordCount)*8)
	copy(words, rec[64:64+int(record.WordCount)*8])

	extended := make([]byte, (pages+2)*format.PageSize)
	copy(extended, raw)
	recomputed := append([]byte(nil), rec[:format.MembershipIDRecordMin]...)
	format.PutU16(recomputed[0:2], format.MembershipIDRecordMin)
	recomputed[2] = 1
	format.PutU32(recomputed[24:28], 9)
	// The record lives at its slot offset inside the id page; the blob
	// form is 64 bytes, so the old inline bitmap area becomes reserved
	// and is zeroed; the page is re-sealed after the edit.
	page := extended[pageNumber*format.PageSize : (pageNumber+1)*format.PageSize]
	sl, err := format.OpenSlottedHeader(page, mutationHeader(page), format.PageType(page[4]), format.U32(page[24:28]), format.SlotItemsPerPage)
	if err != nil {
		t.Fatal(err)
	}
	off := slotStart(sl, 0)
	copy(page[off:off+len(recomputed)], recomputed)
	for at := off + len(recomputed); at < off+len(rec); at++ {
		page[at] = 0
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	branch := make([]byte, format.PageSize)
	format.InitializePageHeader(branch, format.PageTypeBlobBranch, 3, 1, 1, 34, 4080, format.BlobKindMembership)
	format.PutU16(branch[32:34], 4080)
	format.PutU64(branch[4080:4088], 0)
	format.PutU32(branch[4088:4092], 10)
	if err := format.SealPageChecksum(branch); err != nil {
		t.Fatal(err)
	}
	leaf := make([]byte, format.PageSize)
	format.InitializePageHeader(leaf, format.PageTypeBlobLeaf, 3, 1, 0, uint16(48+len(words)), format.PageSize, format.BlobKindMembership)
	format.PutU64(leaf[32:40], 0)
	format.PutU16(leaf[40:42], uint16(len(words)))
	copy(leaf[48:], words)
	if err := format.SealPageChecksum(leaf); err != nil {
		t.Fatal(err)
	}
	copy(extended[9*format.PageSize:], branch)
	copy(extended[10*format.PageSize:], leaf)
	for m := 0; m < 2; m++ {
		meta := extended[m*format.PageSize : (m+1)*format.PageSize]
		format.PutU64(meta[72:80], uint64(pages+2))
		format.PutU32(meta[252:256], format.MetaCRC32C(meta))
	}
	path := writeRawPath(t, extended)
	if findings := collectFindingsHeap(t, path, 2<<20); len(findings) != 0 {
		t.Fatalf("blob clean findings: %+v", findings)
	}
	// Flip one data bit in the blob leaf and re-seal the leaf.
	extended[10*format.PageSize+48] ^= 1
	format.SealPageChecksum(extended[10*format.PageSize : 11*format.PageSize])
	path = writeRawPath(t, extended)
	findings := collectFindingsHeap(t, path, 2<<20)
	if len(findings) != 1 || findings[0].Reason != ReasonMembershipHashInvalid ||
		findings[0].PageNumber == nil || *findings[0].PageNumber != uint32(pageNumber) {
		t.Fatalf("blob digest findings %+v", findings)
	}
}
