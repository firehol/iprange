package recovery

// Verified bitmap reads from recovered membership locators (Rust
// recovery/membership_words.rs + blob_tree.rs read_words parity): the
// inline bitmap is re-read from its source leaf and proven identical
// to the locator, and the blob bitmaps stream through the exact blob
// tree word walk. Every changed-generation proof is the
// candidate-changed class.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// readInlineBitmap re-reads and proves one inline locator (Rust
// read_inline: the checked leaf, the exact record, and the
// matches-inline proof; every refusal is the candidate-changed class).
func readInlineBitmap(m *mapping.Mapping, meta format.Meta, locator membershipLocator) ([]byte, error) {
	page, problem := checkedPage(m, locator.leafPage, meta.PageCount)
	if problem != nil {
		return nil, candidateChangedError()
	}
	var level uint16
	header, treeProblem := format.InspectTreeHeader(page, meta.TxnID,
		byte(format.PageTypeMembershipIDBranch), byte(format.PageTypeMembershipIDLeaf), 0, &level)
	if treeProblem != format.TreeHeaderProblemNone {
		return nil, candidateChangedError()
	}
	cell, err := format.SlottedRecord(page, &header, int(locator.leafIndex), format.MembershipIDRecordMin, format.MaxMembershipIDRecord)
	if err != nil {
		return nil, candidateChangedError()
	}
	record, err := format.DecodeMembershipRecord(cell)
	if err != nil {
		return nil, candidateChangedError()
	}
	if !matchesInline(record, locator) {
		return nil, candidateChangedError()
	}
	bytes, err := inlineBitmapBytes(record, cell)
	if err != nil {
		return nil, candidateChangedError()
	}
	return bytes, nil
}

// matchesInline proves one inline locator against its source record
// (Rust matches_inline).
func matchesInline(record format.MembershipRecord, locator membershipLocator) bool {
	return record.ID == locator.id &&
		record.WordCount == locator.wordCount &&
		record.Digest == locator.digest &&
		record.Storage == format.MembershipStorageInline
}

// inlineBitmapBytes returns the inline bitmap bytes of one record (Rust
// codec::inline_bytes: the exact inline layout).
func inlineBitmapBytes(record format.MembershipRecord, cell []byte) ([]byte, error) {
	if record.Storage != format.MembershipStorageInline {
		return nil, corruptError("membership record storage is not inline")
	}
	length := int(record.WordCount) * 8
	if format.MembershipIDRecordMin+length > len(cell) {
		return nil, corruptError("membership inline bitmap exceeds its record")
	}
	return cell[format.MembershipIDRecordMin : format.MembershipIDRecordMin+length], nil
}

// membershipWordReader reads the verified words of one recovered
// locator (Rust RecoveredWords over MembershipWords).
type membershipWordReader struct {
	m       *mapping.Mapping
	meta    format.Meta
	locator membershipLocator
}

// wordCount returns the locator word count.
func (r membershipWordReader) wordCount() uint32 {
	return r.locator.wordCount
}

// readWords reads one word range of the locator bitmap (Rust
// read_words: the range proof, then the inline or blob walk).
func (r membershipWordReader) readWords(start uint32, output []uint64) error {
	end, ok := checkedAdd(uint64(start), uint64(len(output)))
	if !ok {
		return overflowError("recovery membership read")
	}
	if end > uint64(r.locator.wordCount) {
		return corruptError("recovery membership read exceeds its bitmap")
	}
	switch r.locator.storage {
	case format.MembershipStorageInline:
		return readInlineWords(r.m, r.meta, r.locator, start, output)
	case format.MembershipStorageBlob:
		return readBlobWords(r.m, r.meta, r.locator.blobRoot, r.locator.wordCount, start, output)
	default:
		return corruptError("recovery membership storage is invalid")
	}
}

// locatorEqual proves two locators byte-equal (Rust Locator::equal: the
// word count and digest proof, then the word-by-word compare).
func locatorEqual(left, right membershipLocator, m *mapping.Mapping, meta format.Meta) (bool, error) {
	if left.wordCount != right.wordCount || left.digest != right.digest {
		return false, nil
	}
	const wordBuffer = 64
	leftReader := membershipWordReader{m: m, meta: meta, locator: left}
	rightReader := membershipWordReader{m: m, meta: meta, locator: right}
	var leftWords [wordBuffer]uint64
	var rightWords [wordBuffer]uint64
	for start := uint32(0); start < left.wordCount; {
		count := left.wordCount - start
		if count > wordBuffer {
			count = wordBuffer
		}
		if err := leftReader.readWords(start, leftWords[:count]); err != nil {
			return false, err
		}
		if err := rightReader.readWords(start, rightWords[:count]); err != nil {
			return false, err
		}
		for index := 0; index < int(count); index++ {
			if leftWords[index] != rightWords[index] {
				return false, nil
			}
		}
		start += count
	}
	return true, nil
}

// readInlineWords reads one word range of an inline locator (Rust
// read_inline_words).
func readInlineWords(m *mapping.Mapping, meta format.Meta, locator membershipLocator, start uint32, output []uint64) error {
	bytes, err := readInlineBitmap(m, meta, locator)
	if err != nil {
		return err
	}
	startBytes := uint64(start) * 8
	if startBytes > uint64(len(bytes)) {
		return overflowError("recovery membership offset")
	}
	for index := range output {
		offset := startBytes + uint64(index)*8
		if offset+8 > uint64(len(bytes)) {
			return candidateChangedError()
		}
		output[index] = format.U64(bytes[offset : offset+8])
	}
	return nil
}

// readBlobWords reads one word range of a blob locator (Rust
// blob_tree::read_words_from).
func readBlobWords(m *mapping.Mapping, meta format.Meta, root uint32, totalWords uint32, start uint32, output []uint64) error {
	totalBytes := uint64(totalWords) * 8
	offset := uint64(start) * 8
	written := 0
	for written < len(output) {
		leaf, err := findBlobLeaf(m, meta, root, totalBytes, offset)
		if err != nil {
			return err
		}
		local := int(offset - leaf.offset)
		available := (leaf.dataLen - local) / 8
		count := available
		if count > len(output)-written {
			count = len(output) - written
		}
		if count == 0 {
			return corruptError("membership blob cannot advance by a complete word")
		}
		page, problem := checkedPage(m, leaf.pageNumber, meta.PageCount)
		if problem != nil {
			return candidateChangedError()
		}
		for index := 0; index < count; index++ {
			at := format.BlobLeafData + local + index*8
			output[written+index] = format.U64(page[at : at+8])
		}
		written += count
		next, ok := checkedAdd(offset, uint64(count)*8)
		if !ok {
			return overflowError("membership blob offset")
		}
		offset = next
	}
	return nil
}

// blobLeafInfo is the identified leaf of one blob word walk (Rust
// blob_tree::Leaf).
type blobLeafInfo struct {
	pageNumber uint32
	offset     uint64
	dataLen    int
}

// findBlobLeaf walks one blob tree to the leaf covering one byte
// offset (Rust blob_tree::find_leaf: the leaf geometry and the branch
// binary search; every refusal is the Corrupt class).
func findBlobLeaf(m *mapping.Mapping, meta format.Meta, root uint32, totalBytes, target uint64) (blobLeafInfo, error) {
	if target >= totalBytes {
		return blobLeafInfo{}, corruptError("membership blob request exceeds its length")
	}
	pageNumber := root
	var expected *uint16
	expectedOffset := uint64(0)
	for level := 0; level <= int(format.MaxTreeLevel); level++ {
		page, err := m.Page(pageNumber)
		if err != nil {
			return blobLeafInfo{}, corruptError("membership blob page is unreadable")
		}
		levelValue := format.U16(page[18:20])
		if levelValue == 0 {
			// Rust parse_leaf_info: the common and born identity arms
			// of require_leaf_identity precede the geometry proof.
			if !format.BlobCommonValid(page) || !format.BlobBornValid(page, meta.TxnID) {
				return blobLeafInfo{}, corruptError("membership blob leaf identity is malformed")
			}
			geometry, err := format.DecodeBlobLeafGeometry(page, expected, expectedOffset, totalBytes)
			if err != nil {
				return blobLeafInfo{}, err
			}
			return blobLeafInfo{pageNumber: pageNumber, offset: geometry.Start, dataLen: geometry.DataLen}, nil
		}
		header, err := format.DecodePageHeader(page, meta.TxnID)
		if err != nil || header.PageType != format.PageTypeBlobBranch || header.Aux != format.BlobKindMembership ||
			(expected != nil && header.Level != *expected) {
			return blobLeafInfo{}, corruptError("membership blob branch is invalid")
		}
		cells := format.InspectLayout(page, &header, format.FixedLayout(format.BlobBranchSize)).Cells()
		if cells == nil {
			return blobLeafInfo{}, corruptError("membership blob branch layout is invalid")
		}
		firstCell, ok := cells.Next()
		if !ok {
			return blobLeafInfo{}, corruptError("membership blob branch is empty")
		}
		firstOffset, _, err := format.DecodeBlobBranchFields(firstCell)
		_ = firstOffset
		if err != nil || firstOffset != expectedOffset {
			return blobLeafInfo{}, corruptError("membership blob branch starts at a wrong offset")
		}
		selected, err := selectBlobBranch(m, page, &header, target)
		if err != nil {
			return blobLeafInfo{}, err
		}
		pageNumber = selected.child
		expectedOffset = selected.offset
		lower := header.Level - 1
		expected = &lower
	}
	return blobLeafInfo{}, corruptError("membership blob tree exceeds its maximum height")
}

// blobBranchRecord is one selected blob branch record (Rust
// BranchRecord).
type blobBranchRecord struct {
	offset uint64
	child  uint32
}

// selectBlobBranch binary-searches one branch for the record covering
// the target byte (Rust select_branch).
func selectBlobBranch(m *mapping.Mapping, page []byte, header *format.PageHeader, target uint64) (blobBranchRecord, error) {
	lower := 0
	upper := int(header.ItemCount)
	for lower < upper {
		middle := lower + (upper-lower)/2
		record, err := blobBranchRecordAt(m, page, header, middle)
		if err != nil {
			return blobBranchRecord{}, err
		}
		if record.offset <= target {
			lower = middle + 1
		} else {
			upper = middle
		}
	}
	if lower == 0 {
		return blobBranchRecord{}, corruptError("membership blob target precedes its branch")
	}
	return blobBranchRecordAt(m, page, header, lower-1)
}

// blobBranchRecordAt reads one branch record with the child bound
// proof (Rust branch_record).
func blobBranchRecordAt(m *mapping.Mapping, page []byte, header *format.PageHeader, index int) (blobBranchRecord, error) {
	cells := format.InspectLayout(page, header, format.FixedLayout(format.BlobBranchSize)).Cells()
	for cellIndex := 0; cellIndex <= index; cellIndex++ {
		cell, ok := cells.Next()
		if !ok {
			return blobBranchRecord{}, corruptError("membership blob branch record is missing")
		}
		if cellIndex == index {
			offset, child, err := format.DecodeBlobBranchFields(cell)
			if err != nil {
				return blobBranchRecord{}, err
			}
			if !format.PageNumberValid(child, m.Size()/format.PageSize) {
				return blobBranchRecord{}, corruptError("membership blob child is out of range")
			}
			return blobBranchRecord{offset: offset, child: child}, nil
		}
	}
	return blobBranchRecord{}, corruptError("membership blob branch record is missing")
}
