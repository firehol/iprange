package validation

// Slice-E structure validator tests: the full sweeps over the corpus
// structured fixtures, targeted mutations of the dense dictionary
// records, the reverse-index records, the used bitmap, and the range
// values producing the exact Rust reason classes in walk order, and a
// synthetic two-level table proving the dense directory walk.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// structureRecordCell returns the fixed record cell of one dense-table
// slot and its page number (the corpus roots are level-0 record pages;
// the directory descent covers synthetic roots).
func structureRecordCell(t *testing.T, raw []byte, pages int, slot uint64) ([]byte, int) {
	t.Helper()
	pageNumber := int(format.U32(raw[216:220])) // StructureIDRoot
	page := raw[pageNumber*format.PageSize : (pageNumber+1)*format.PageSize]
	for {
		h := mutationHeader(page)
		if h.Level == 0 {
			break
		}
		span, ok := format.StructureSpanOfLevel(uint32(h.Level))
		if !ok {
			t.Fatal("structure span overflow")
		}
		index := (slot / span) % format.StructureDirectoryChildCount
		child := format.U32(page[32+index*4 : 36+index*4])
		if child < 2 || uint64(child) >= uint64(pages) {
			t.Fatal("structure child outside the database")
		}
		pageNumber = int(child)
		page = raw[pageNumber*format.PageSize : (pageNumber+1)*format.PageSize]
	}
	offset := 32 + slot%format.StructureRecordSlots*format.StructureRecordSize
	return page[offset : offset+format.StructureRecordSize], pageNumber
}

// structureHashLeaf returns the structure hash leaf page of one fixture
// and its page number.
func structureHashLeaf(t *testing.T, raw []byte, pages int) ([]byte, int) {
	t.Helper()
	return descendToLeaf(t, raw, pages, int(format.U32(raw[220:224])), format.StructureHashBranchSize)
}

// structureHashRecord returns one reverse-index hash record.
func structureHashRecord(t *testing.T, raw []byte, pages int, index int) ([]byte, int) {
	t.Helper()
	page, pageNumber := structureHashLeaf(t, raw, pages)
	sl, err := format.OpenSlottedHeader(page, mutationHeader(page), format.PageType(page[4]), format.U32(page[24:28]), format.SlotItemsPerPage)
	if err != nil {
		t.Fatal(err)
	}
	return boundedRecord(sl, index, format.StructureHashKeySize), pageNumber
}

// structurePayloadOne is one canonical NetworkEnrichmentV1 payload (the
// fixture record shape: a private ASN with a location and no threat
// membership).
func structurePayloadOne() [32]byte {
	var payload [32]byte
	format.EncodeNetworkEnrichmentV1(payload[:], format.NetworkEnrichmentV1{
		ASN:                   64512,
		CountryID:             1,
		StateID:               2,
		CityID:                3,
		LatitudeMicrodegrees:  42_964_302,
		LongitudeMicrodegrees: 23_727_539,
		Flags:                 format.NetworkEnrichmentV1HasLocation,
	})
	return payload
}

// structureRecordBytes builds one fixed 80-byte dictionary record.
func structureRecordBytes(id uint32, refcount uint64, payload [32]byte, digest [32]byte) []byte {
	cell := make([]byte, format.StructureRecordSize)
	format.PutU16(cell[0:2], format.StructureRecordSize)
	format.PutU32(cell[4:8], id)
	format.PutU64(cell[8:16], refcount)
	copy(cell[16:48], digest[:])
	copy(cell[48:80], payload[:])
	return cell
}

// structureDigest returns the payload identity of the kind-1 codec.
func structureDigest(t *testing.T, payload [32]byte) [32]byte {
	t.Helper()
	digest, err := format.StructurePayloadDigest(format.StructureKindNetworkEnrichmentV1, payload[:])
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// rawContext builds a validation context directly over raw bytes,
// bypassing the bootstrap kind gate (for the unknown-structure-kind arm,
// which the open path refuses before validation).
func rawContext(t *testing.T, raw []byte, maxHeap uint64) *context {
	t.Helper()
	path := writeRawPath(t, raw)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	m, err := mapping.MapFile(file, uint64(len(raw)), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	p0, err := m.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	meta, ok := format.ParseIdentity(p0)
	if !ok {
		t.Fatal("fixture meta is not identity-readable")
	}
	ctx, err := newContext(m, meta, HeapOnly(maxHeap, 1), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestValidateStructureCorpusClean(t *testing.T) {
	// The full sweep over both structured corpus fixtures is clean: the
	// dense dictionary, the reverse index, the membership owners, the
	// used bitmap, the slot finish, and the partition claims all agree.
	for _, name := range []string{"structured-ipv4.iprdb", "structured-ipv4-nothreat.iprdb"} {
		if findings := sweepFixture(t, name); len(findings) != 0 {
			t.Fatalf("%s findings: %+v", name, findings)
		}
	}
}

func TestValidateStructurePayloadInvalidFinding(t *testing.T) {
	// The payload breaks the kind semantic rules (an unknown flag bit):
	// the record arm reports the payload class on the record page and
	// nothing else (the reverse mark still matches the stored digest).
	var pageNumber int
	path := corpusCopy(t, "structured-ipv4.iprdb", func(raw []byte, pages int) []int {
		cell, page := structureRecordCell(t, raw, pages, 1)
		pageNumber = page
		cell[48+31] |= 2 // flags gains a bit above the location bit
		return []int{page}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{
		ReasonStructurePayloadInvalid,      // payload breaks the kind rules
		ReasonStructureReverseIndexInvalid, // the refused id misses its hash mark
		ReasonStructureRefcountInvalid,     // range-counted slot never defined
		ReasonStructureInvalid,             // one defined slot vs two records
		ReasonMembershipRefcountInvalid,    // membership 1 lost its counted owner
	}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v want %v", i, findings[i], reason)
		}
	}
	if *findings[0].PageNumber != uint32(pageNumber) || findings[0].Object != ObjectStructureDictionary ||
		findings[1].PageNumber == nil || *findings[1].PageNumber != 10 ||
		findings[2].PageNumber != nil || findings[3].PageNumber != nil || findings[4].PageNumber != nil {
		t.Fatalf("page attribution %+v", findings)
	}
}

func TestValidateStructureWrongSlotIDFinding(t *testing.T) {
	// The second record carries the first record's id: the dense slot
	// record refuses twice on the record page (Rust validate_record:
	// decode_record, then the implied-slot id proof and the duplicate
	// insert proof, both the structure-invalid class), the renamed id
	// then misses its hash mark, the range-counted id reports the
	// refcount class in the slots, and the totals prove the
	// disagreement. The duplicated record still counts its membership
	// owner (Rust validate_record counts regardless of the insert
	// result), so the membership totals balance and emit no finding.
	var pageNumber int
	path := corpusCopy(t, "structured-ipv4.iprdb", func(raw []byte, pages int) []int {
		cell, page := structureRecordCell(t, raw, pages, 2)
		pageNumber = page
		format.PutU32(cell[4:8], 1)
		return []int{page}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{
		ReasonStructureInvalid,             // id 1 at the slot implying id 2
		ReasonStructureInvalid,             // id 1 defined twice
		ReasonStructureReverseIndexInvalid, // id 2 misses its reverse mark
		ReasonStructureRefcountInvalid,     // slot 2 counted but undefined
		ReasonStructureInvalid,             // one defined slot vs two records
	}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v want %v", i, findings[i], reason)
		}
	}
	if *findings[0].PageNumber != uint32(pageNumber) ||
		*findings[1].PageNumber != uint32(pageNumber) ||
		findings[2].PageNumber == nil || *findings[2].PageNumber != 10 ||
		findings[3].PageNumber != nil || findings[4].PageNumber != nil {
		t.Fatalf("page attribution %+v", findings)
	}
}

func TestValidateStructureRefcountZeroFinding(t *testing.T) {
	// The record refcount is zero: the record arm reports it on the
	// record page and the slot finish repeats the class without a page.
	var pageNumber int
	path := corpusCopy(t, "structured-ipv4.iprdb", func(raw []byte, pages int) []int {
		cell, page := structureRecordCell(t, raw, pages, 1)
		pageNumber = page
		format.PutU64(cell[8:16], 0)
		return []int{page}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{ReasonStructureRefcountInvalid, ReasonStructureRefcountInvalid}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	if findings[0].Reason != want[0] || findings[0].PageNumber == nil || *findings[0].PageNumber != uint32(pageNumber) ||
		findings[1].Reason != want[1] || findings[1].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateStructureDigestAndReverseFindings(t *testing.T) {
	// The record digest diverges from its payload: the record arm
	// reports the hash class on the record page and the reverse-index
	// walk then fails its digest match on the hash page.
	var pageNumber int
	path := corpusCopy(t, "structured-ipv4.iprdb", func(raw []byte, pages int) []int {
		cell, page := structureRecordCell(t, raw, pages, 1)
		pageNumber = page
		cell[17] ^= 1
		return []int{page}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{
		ReasonStructureHashInvalid,         // stored digest vs payload digest
		ReasonStructureReverseIndexInvalid, // hash mark fails on the hash page
		ReasonStructureReverseIndexInvalid, // slot reverse never seen
	}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v want %v", i, findings[i], reason)
		}
	}
	if *findings[0].PageNumber != uint32(pageNumber) || findings[0].Object != ObjectStructureDictionary ||
		*findings[1].PageNumber != 10 || findings[1].Object != ObjectStructureReverseIndex ||
		findings[2].PageNumber != nil {
		t.Fatalf("attribution %+v", findings)
	}
}

func TestValidateStructureRecordBeyondLimitFinding(t *testing.T) {
	// A third record appears at slot 3, at the declared id limit: the
	// record arm reports the id window on the record page, the dense
	// leaf shape disagrees with its item count, the walk count
	// disagrees with the entry count, and the finishing slots report
	// the unproven record (the remaining walks still run after the
	// count finding, like the Rust composition).
	path := corpusCopy(t, "structured-ipv4.iprdb", func(raw []byte, pages int) []int {
		payload := structurePayloadOne()
		digest := structureDigest(t, payload)
		cell, page := structureRecordCell(t, raw, pages, 3)
		copy(cell, structureRecordBytes(3, 1, payload, digest))
		return []int{page}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{
		ReasonStructureInvalid,             // slot 3 at the id limit
		ReasonPageHeaderInvalid,            // three cells vs the declared item count
		ReasonRootCountInvalid,             // 3 dictionary records vs the entry count
		ReasonStructureRefcountInvalid,     // slot 3 has no range count
		ReasonStructureReverseIndexInvalid, // slot 3 never reverse-marked
		ReasonStructureInvalid,             // slot 3 used bit absent
		ReasonStructureInvalid,             // totals disagreement
	}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v want %v", i, findings[i], reason)
		}
	}
	if *findings[0].PageNumber != 9 || findings[0].Object != ObjectStructureDictionary ||
		*findings[1].PageNumber != 9 || findings[1].Object != ObjectStructureDictionary ||
		findings[2].PageNumber != nil {
		t.Fatalf("attribution %+v", findings)
	}
	for i := 3; i < len(findings); i++ {
		if findings[i].PageNumber != nil {
			t.Fatalf("finding %d must be page-less: %+v", i, findings[i])
		}
	}
}

func TestValidateStructureHashCollisionFinding(t *testing.T) {
	// Both dictionary records carry the same payload and digest: the
	// adjacent same-digest pair proves equal payloads and reports the
	// hash class on the hash page (the nothreat fixture keeps the
	// membership arms out of the way).
	var hashPageNumber int
	path := corpusCopy(t, "structured-ipv4-nothreat.iprdb", func(raw []byte, pages int) []int {
		first, _ := structureRecordCell(t, raw, pages, 1)
		payload := [32]byte(first[48:80])
		digest := [32]byte(first[16:48])
		cell, page := structureRecordCell(t, raw, pages, 2)
		copy(cell, structureRecordBytes(2, 1, payload, digest))
		// Both hash records carry the shared digest, ids in key order.
		hashPage, hp := structureHashLeaf(t, raw, pages)
		hashPageNumber = hp
		sl, err := format.OpenSlottedHeader(hashPage, mutationHeader(hashPage), format.PageType(hashPage[4]), format.U32(hashPage[24:28]), format.SlotItemsPerPage)
		if err != nil {
			t.Fatal(err)
		}
		slot0 := boundedRecord(sl, 0, format.StructureHashKeySize)
		copy(slot0[0:32], digest[:])
		format.PutU32(slot0[32:36], 1)
		slot1 := boundedRecord(sl, 1, format.StructureHashKeySize)
		copy(slot1[0:32], digest[:])
		format.PutU32(slot1[32:36], 2)
		return []int{page, hashPageNumber}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	if len(findings) != 1 || findings[0].Reason != ReasonStructureHashInvalid ||
		findings[0].Object != ObjectStructureDictionary ||
		findings[0].PageNumber == nil || *findings[0].PageNumber != uint32(hashPageNumber) {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateStructureRangeRefcountFinding(t *testing.T) {
	// One range value moves to an undefined id: the id that used to own
	// it keeps its stored refcount while its range count drops, so the
	// slot finish reports the refcount class twice without pages.
	path := corpusCopy(t, "structured-ipv4.iprdb", func(raw []byte, pages int) []int {
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
	if len(findings) != 2 || findings[0].Reason != ReasonStructureRefcountInvalid ||
		findings[0].PageNumber != nil || findings[1].Reason != ReasonStructureRefcountInvalid ||
		findings[1].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateStructureMembershipMissingFinding(t *testing.T) {
	// The first payload's membership id names an absent membership: the
	// record arm reports the membership class on the record page, and
	// the membership slot finish reports both the id that lost its
	// owner and the newly-counted absent id.
	var pageNumber int
	path := corpusCopy(t, "structured-ipv4.iprdb", func(raw []byte, pages int) []int {
		cell, page := structureRecordCell(t, raw, pages, 1)
		pageNumber = page
		format.PutU32(cell[48+24:48+28], 7)
		digest, err := format.StructurePayloadDigest(format.StructureKindNetworkEnrichmentV1, cell[48:80])
		if err != nil {
			t.Fatal(err)
		}
		copy(cell[16:48], digest[:])
		// The reverse-index record follows the recomputed digest.
		hashPage, hashPageNumber := structureHashLeaf(t, raw, pages)
		sl, err := format.OpenSlottedHeader(hashPage, mutationHeader(hashPage), format.PageType(hashPage[4]), format.U32(hashPage[24:28]), format.SlotItemsPerPage)
		if err != nil {
			t.Fatal(err)
		}
		copy(boundedRecord(sl, 1, format.StructureHashKeySize)[0:32], digest[:])
		return []int{page, hashPageNumber}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{
		ReasonStructureMembershipInvalid, // absent membership id
		ReasonMembershipRefcountInvalid,  // membership 1 lost its owner
		ReasonMembershipRefcountInvalid,  // membership 7 counted but undefined
	}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v want %v", i, findings[i], reason)
		}
	}
	if *findings[0].PageNumber != uint32(pageNumber) ||
		findings[1].PageNumber != nil || findings[2].PageNumber != nil {
		t.Fatalf("page attribution %+v", findings)
	}
}

func TestValidateStructureUsedBitmapFindings(t *testing.T) {
	// The used bitmap loses the bit of the first dictionary id: the
	// walk count drops and the slot used check repeats the class, both
	// without a page.
	path := corpusCopy(t, "structured-ipv4.iprdb", func(raw []byte, pages int) []int {
		usedPage := raw[8*format.PageSize : 9*format.PageSize]
		format.PutU64(usedPage[32:40], 4) // 0b100: bit 1 cleared
		return []int{8}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	if len(findings) != 2 || findings[0].Reason != ReasonStructureInvalid ||
		findings[0].PageNumber != nil || findings[1].Reason != ReasonStructureInvalid ||
		findings[1].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateStructureReverseMissingFinding(t *testing.T) {
	// The last hash record names an id no dictionary record defines: the
	// reverse mark misses on the hash page and the slot finish repeats
	// the missing reverse class without a page.
	var hashPageNumber int
	path := corpusCopy(t, "structured-ipv4.iprdb", func(raw []byte, pages int) []int {
		hashPage, page := structureHashLeaf(t, raw, pages)
		hashPageNumber = page
		sl, err := format.OpenSlottedHeader(hashPage, mutationHeader(hashPage), format.PageType(hashPage[4]), format.U32(hashPage[24:28]), format.SlotItemsPerPage)
		if err != nil {
			t.Fatal(err)
		}
		cell := boundedRecord(sl, 1, format.StructureHashKeySize)
		format.PutU32(cell[32:36], 5)
		return []int{page}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{ReasonStructureReverseIndexInvalid, ReasonStructureReverseIndexInvalid}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	if findings[0].Reason != want[0] || *findings[0].PageNumber != uint32(hashPageNumber) ||
		findings[1].Reason != want[1] || findings[1].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateStructureUnknownKindFinding(t *testing.T) {
	// An unknown nonzero structure kind reaches the validator only when
	// the bootstrap kind gate is bypassed (the open path refuses it as
	// UnsupportedStructure first); the validator reports the invalid
	// class without pages, exactly like Rust structure::validate.
	raw, _ := readFixtureRaw(t, "structured-ipv4.iprdb")
	raw[13] = 5
	format.PutU32(raw[252:256], format.MetaCRC32C(raw[0:format.PageSize]))
	ctx := rawContext(t, raw, 2<<20)
	findings := collectContextFindings(t, ctx, validateStructure)
	if len(findings) != 1 || findings[0].Reason != ReasonStructureInvalid ||
		findings[0].Object != ObjectStructureDictionary || findings[0].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

// structureBranchMeta builds the meta of a synthetic two-level
// structure database: two structure entries at ids 1 and 50 under a
// level-1 root (a level-1 limit requires an id at or above 50, and the
// id-limit proof requires the limit to equal max-id plus one), their
// used bits, their hash records, and the two ranges that own them.
func structureBranchMeta() []byte {
	meta := metaPage(2, 8)
	meta[12] = format.ValueKindStructured
	meta[13] = format.StructureKindNetworkEnrichmentV1
	format.PutU64(meta[80:88], 2)   // RangeRecordCount
	format.PutU64(meta[96:104], 0)  // FeedIndexLimit
	format.PutU64(meta[104:112], 0) // MembershipEntryCount
	format.PutU64(meta[112:120], 1) // MembershipIDLimit
	format.PutU32(meta[144:148], 7) // RangeRoot
	format.PutU64(meta[200:208], 2) // StructureEntryCount
	format.PutU64(meta[208:216], 51)
	format.PutU32(meta[216:220], 2) // StructureIDRoot
	format.PutU32(meta[220:224], 6) // StructureHashRoot
	format.PutU32(meta[224:228], 5) // StructureUsedRoot
	format.PutU32(meta[252:256], format.MetaCRC32C(meta))
	return meta
}

// structureDirectoryAt builds one structure directory page of the
// given level over the (child index, page) entries: a level-1 node
// covers R*512^(L-1) IDs per child (table.rs coverage(level-1)).
func structureDirectoryAt(t *testing.T, level uint16, children ...[2]uint32) []byte {
	t.Helper()
	page := make([]byte, format.PageSize)
	copy(page[:4], format.PageMagic[:])
	page[4] = byte(format.PageTypeStructureIDDirectory)
	format.PutU16(page[6:8], 32)
	format.PutU64(page[8:16], 2)
	format.PutU16(page[16:18], uint16(len(children)))
	format.PutU16(page[18:20], level)
	format.PutU16(page[20:22], format.StructureBranchEnd)
	format.PutU16(page[22:24], format.PageSize)
	format.PutU32(page[24:28], uint32(format.StructureKindNetworkEnrichmentV1))
	for _, child := range children {
		format.PutU32(page[32+child[0]*4:36+child[0]*4], child[1])
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	return page
}

// structureDirectory builds one level-1 structure directory page over
// the given (child index, page) entries.
func structureDirectory(t *testing.T, children ...[2]uint32) []byte {
	t.Helper()
	return structureDirectoryAt(t, 1, children...)
}

// structureRecordLeaf builds one level-0 structure record page over the
// given dense slots.
func structureRecordLeaf(t *testing.T, slots map[uint64][]byte) []byte {
	t.Helper()
	page := make([]byte, format.PageSize)
	copy(page[:4], format.PageMagic[:])
	page[4] = byte(format.PageTypeStructureIDRecord)
	format.PutU16(page[6:8], 32)
	format.PutU64(page[8:16], 2)
	format.PutU16(page[16:18], uint16(len(slots)))
	format.PutU16(page[18:20], 0) // level
	format.PutU16(page[20:22], format.StructureLeafEnd)
	format.PutU16(page[22:24], format.PageSize)
	format.PutU32(page[24:28], uint32(format.StructureKindNetworkEnrichmentV1))
	for slot, cell := range slots {
		copy(page[32+slot*format.StructureRecordSize:], cell)
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	return page
}

// structureHashRecord is one fixed 36-byte reverse-index record.
type structureHashEntry struct {
	digest [32]byte
	id     uint32
}

// structureHashLeafPage builds one level-0 structure hash leaf page.
func structureHashLeafPage(t *testing.T, records ...structureHashEntry) []byte {
	t.Helper()
	page := make([]byte, format.PageSize)
	copy(page[:4], format.PageMagic[:])
	page[4] = byte(format.PageTypeStructureHashLeaf)
	format.PutU16(page[6:8], 32)
	format.PutU64(page[8:16], 2)
	format.PutU16(page[16:18], uint16(len(records)))
	format.PutU16(page[18:20], 0) // level
	lower := format.SlottedHeaderSize + 2*len(records)
	upper := format.PageSize - len(records)*format.StructureHashKeySize
	format.PutU16(page[20:22], uint16(lower))
	format.PutU16(page[22:24], uint16(upper))
	format.PutU32(page[24:28], uint32(format.StructureKindNetworkEnrichmentV1))
	for i, record := range records {
		at := upper + i*format.StructureHashKeySize
		copy(page[at:at+32], record.digest[:])
		format.PutU32(page[at+32:at+36], record.id)
		format.PutU16(page[32+i*2:34+i*2], uint16(at))
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	return page
}

// structureUsedLeaf builds one structure-kind used bitmap leaf.
func structureUsedLeaf(t *testing.T, words ...uint64) []byte {
	t.Helper()
	page := make([]byte, format.PageSize)
	bitmap.Initialize(page, 2, 0, bitmap.KindStructure)
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

// structureBranchDB builds the synthetic two-level generation: meta
// pair, directory at page 2, record leaves at pages 3 and 4 (child
// spans 0 and 1), used leaf at page 5, hash leaf at page 6, range leaf
// at page 7.
func structureBranchDB(t *testing.T, mutate func(dir, leafA, leafB []byte)) string {
	t.Helper()
	payloadA := structurePayloadOne()
	payloadB := structurePayloadOne()
	payloadB[0] = 0x01 // distinct ASN low byte
	digestA := structureDigest(t, payloadA)
	digestB := structureDigest(t, payloadB)
	recordA := structureRecordBytes(1, 1, payloadA, digestA)
	recordB := structureRecordBytes(50, 1, payloadB, digestB)
	dir := structureDirectory(t, [2]uint32{0, 3}, [2]uint32{1, 4})
	leafA := structureRecordLeaf(t, map[uint64][]byte{1: recordA})
	leafB := structureRecordLeaf(t, map[uint64][]byte{0: recordB})
	hash := structureHashLeafPage(t,
		structureHashEntry{digest: digestA, id: 1},
		structureHashEntry{digest: digestB, id: 50})
	used := structureUsedLeaf(t, (uint64(1)<<1)|(uint64(1)<<50))
	rangeLeaf := rangeTreeLeaf(t, 2, []format.RangeRecordV4{
		{From: 1, To: 1, Value: 1},
		{From: 2, To: 2, Value: 50},
	}, 4056)
	if mutate != nil {
		mutate(dir, leafA, leafB)
	}
	path := filepath.Join(t.TempDir(), "database.iprdb")
	if err := writePages(path, structureBranchMeta(), dir, leafA, leafB, used, hash, rangeLeaf); err != nil {
		t.Fatal(err)
	}
	return path
}

// structureLevelTwoMeta builds the meta of a synthetic three-level
// structure database: structure ids 1 and 25600 under a level-2 root
// (the smallest limit above one level-1 coverage, 50*512 = 25600), the
// level-1 directory child indexes 0 of both root child spans, their
// used bits, their hash records, and the two ranges that own them.
func structureLevelTwoMeta() []byte {
	meta := metaPage(2, 10)
	meta[12] = format.ValueKindStructured
	meta[13] = format.StructureKindNetworkEnrichmentV1
	format.PutU64(meta[80:88], 2)   // RangeRecordCount
	format.PutU64(meta[96:104], 0)  // FeedIndexLimit
	format.PutU64(meta[104:112], 0) // MembershipEntryCount
	format.PutU64(meta[112:120], 1) // MembershipIDLimit
	format.PutU32(meta[144:148], 9) // RangeRoot
	format.PutU64(meta[200:208], 2) // StructureEntryCount
	format.PutU64(meta[208:216], 25_601)
	format.PutU32(meta[216:220], 2) // StructureIDRoot
	format.PutU32(meta[220:224], 8) // StructureHashRoot
	format.PutU32(meta[224:228], 7) // StructureUsedRoot
	format.PutU32(meta[252:256], format.MetaCRC32C(meta))
	return meta
}

// structureLevelTwoDB builds the synthetic three-level generation: meta
// pair, the level-2 directory root at page 2 (child spans 0 and 1 over
// 25600 ids each), its two level-1 directories at pages 3 and 4, the
// record leaves at pages 5 and 6, the used leaf at page 7, the hash
// leaf at page 8, and the range leaf at page 9.
func structureLevelTwoDB(t *testing.T, mutate func(root, dirA, dirB, leafA, leafB []byte)) string {
	t.Helper()
	payloadA := structurePayloadOne()
	payloadB := structurePayloadOne()
	payloadB[0] = 0x01 // distinct ASN low byte
	digestA := structureDigest(t, payloadA)
	digestB := structureDigest(t, payloadB)
	recordA := structureRecordBytes(1, 1, payloadA, digestA)
	recordB := structureRecordBytes(25_600, 1, payloadB, digestB)
	root := structureDirectoryAt(t, 2, [2]uint32{0, 3}, [2]uint32{1, 4})
	dirA := structureDirectoryAt(t, 1, [2]uint32{0, 5})
	dirB := structureDirectoryAt(t, 1, [2]uint32{0, 6})
	leafA := structureRecordLeaf(t, map[uint64][]byte{1: recordA})
	leafB := structureRecordLeaf(t, map[uint64][]byte{0: recordB})
	hash := structureHashLeafPage(t,
		structureHashEntry{digest: digestA, id: 1},
		structureHashEntry{digest: digestB, id: 25_600})
	used := make([]uint64, 401)
	used[0] = uint64(1) << 1
	used[400] = 1 // bit 25600
	usedLeaf := structureUsedLeaf(t, used...)
	rangeLeaf := rangeTreeLeaf(t, 2, []format.RangeRecordV4{
		{From: 1, To: 1, Value: 1},
		{From: 2, To: 2, Value: 25_600},
	}, 4056)
	if mutate != nil {
		mutate(root, dirA, dirB, leafA, leafB)
	}
	path := filepath.Join(t.TempDir(), "database.iprdb")
	if err := writePages(path, structureLevelTwoMeta(), root, dirA, dirB, leafA, leafB, usedLeaf, hash, rangeLeaf); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestValidateStructureLevelTwoRootClean proves the dense directory
// walk scales the child base by the full coverage of the level below
// (25600 ids per level-2 child, Rust coverage(level-1)): record id
// 25600 resolves only when the root's second child spans 25600, not
// the 50-id level-0 span.
func TestValidateStructureLevelTwoRootClean(t *testing.T) {
	path := structureLevelTwoDB(t, nil)
	if findings := collectFindingsHeap(t, path, 2<<20); len(findings) != 0 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateStructureBranchClean(t *testing.T) {
	// The synthetic two-level generation: the directory root at level 1
	// walks both record children and the whole sweep stays clean.
	path := structureBranchDB(t, nil)
	if findings := collectFindingsHeap(t, path, 2<<20); len(findings) != 0 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateStructureBranchReservedFinding(t *testing.T) {
	// A nonzero byte in the reserved tail of the directory page: the
	// reserved class is reported on the directory page and the walk
	// continues.
	path := structureBranchDB(t, func(dir, leafA, leafB []byte) {
		dir[format.StructureBranchEnd] = 1
		if err := format.SealPageChecksum(dir); err != nil {
			t.Fatal(err)
		}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	if len(findings) != 1 || findings[0].Reason != ReasonPageReservedNonzero ||
		findings[0].Object != ObjectStructureDictionary ||
		findings[0].PageNumber == nil || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateStructureBranchShapeFinding(t *testing.T) {
	// The directory declares three children but carries two: the found
	// count disagrees with the item count on the directory page.
	path := structureBranchDB(t, func(dir, leafA, leafB []byte) {
		format.PutU16(dir[format.HeaderCount:], 3)
		if err := format.SealPageChecksum(dir); err != nil {
			t.Fatal(err)
		}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	if len(findings) != 1 || findings[0].Reason != ReasonPageHeaderInvalid ||
		findings[0].Object != ObjectStructureDictionary ||
		findings[0].PageNumber == nil || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateStructureBranchLevelFinding(t *testing.T) {
	// The directory root claims level 2 under a level-1 limit: the level
	// class fires on the directory page, the refused subgraph yields no
	// dictionary records, and the remaining walks still run (the hash
	// marks, the slot refcounts, and the totals all disagree, and the
	// unvisited record leaves merge into one partition interval).
	path := structureBranchDB(t, func(dir, leafA, leafB []byte) {
		format.PutU16(dir[format.HeaderLevel:], 2)
		if err := format.SealPageChecksum(dir); err != nil {
			t.Fatal(err)
		}
	})
	findings := collectFindingsHeap(t, path, 2<<20)
	want := []ValidationReason{
		ReasonTreeLevelInvalid,             // root level 2 vs expected 1
		ReasonRootCountInvalid,             // zero dictionary records vs two
		ReasonStructureReverseIndexInvalid, // id 1 hash mark misses
		ReasonStructureReverseIndexInvalid, // id 50 hash mark misses
		ReasonStructureRefcountInvalid,     // slot 1 counted but undefined
		ReasonStructureRefcountInvalid,     // slot 50 counted but undefined
		ReasonStructureInvalid,             // defined zero vs two entries
		ReasonAllocationPartitionInvalid,   // record leaves 3 and 4 unclaimed
	}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v want %v", i, findings[i], reason)
		}
	}
	if *findings[0].PageNumber != 2 || findings[0].Object != ObjectStructureDictionary ||
		findings[1].PageNumber != nil ||
		*findings[2].PageNumber != 6 || *findings[3].PageNumber != 6 {
		t.Fatalf("attribution %+v", findings)
	}
	for i := 4; i < 7; i++ {
		if findings[i].PageNumber != nil {
			t.Fatalf("finding %d must be page-less: %+v", i, findings[i])
		}
	}
	if findings[7].PhysicalBytes == nil || findings[7].PhysicalBytes.Start != 3*format.PageSize ||
		findings[7].PhysicalBytes.EndExclusive != 5*format.PageSize {
		t.Fatalf("partition interval %+v", findings[7].PhysicalBytes)
	}
}
