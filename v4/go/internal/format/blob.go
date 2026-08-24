package format

// Generic blob tree records (binary-format-v4.md section 10).

const (
	BlobBranchSize     = 16
	MaxBlobLeafDataLen = 4048
	blobLeafFixed      = 48
)

// BlobBranchRecord is one fixed 16-byte blob branch entry.
type BlobBranchRecord struct {
	LogicalOffset uint64
	Child         uint32
}

// DecodeBlobBranch parses one fixed blob branch entry.
func DecodeBlobBranch(b []byte) (BlobBranchRecord, error) {
	if len(b) < BlobBranchSize {
		return BlobBranchRecord{}, headerErr("short blob branch %d", len(b))
	}
	r := BlobBranchRecord{LogicalOffset: U64(b[0:8]), Child: U32(b[8:12])}
	if U32(b[12:16]) != 0 {
		return BlobBranchRecord{}, headerErr("blob branch reserved")
	}
	if !PageNumberValid(r.Child, MaxPageCount) {
		return BlobBranchRecord{}, headerErr("blob child out of range")
	}
	return r, nil
}

// BlobLeaf is one parsed blob leaf. Data aliases the page view and must not
// outlive the operation.
type BlobLeaf struct {
	LogicalOffset uint64
	DataLen       uint16
	Data          []byte
}

// DecodeBlobLeaf parses one blob leaf page body.
func DecodeBlobLeaf(page []byte) (BlobLeaf, error) {
	if len(page) != PageSize {
		return BlobLeaf{}, headerErr("blob leaf length %d", len(page))
	}
	off := U64(page[32:40])
	dataLen := U16(page[40:42])
	for i := 42; i < 48; i++ {
		if page[i] != 0 {
			return BlobLeaf{}, headerErr("blob leaf reserved")
		}
	}
	if dataLen < 1 || dataLen > MaxBlobLeafDataLen {
		return BlobLeaf{}, headerErr("blob data length %d", dataLen)
	}
	return BlobLeaf{LogicalOffset: off, DataLen: dataLen, Data: page[blobLeafFixed : blobLeafFixed+int(dataLen)]}, nil
}

// Raw blob codecs for the validation walkers (Rust blob_tree.rs
// decode_branch_record and leaf_geometry parity): the branch entry
// decodes without the reader's page-range check, and the leaf geometry
// is proved from the raw header fields so the validators can report the
// reserved-tail and span classes as findings.

// BlobLeafGeometry is the proved data span of one blob leaf (Rust
// LeafGeometry).
type BlobLeafGeometry struct {
	Start   uint64
	End     uint64
	DataLen int
}

// BlobLeafData is the data offset of one blob leaf (Rust LEAF_DATA).
const BlobLeafData = blobLeafFixed

// BlobLeafCapacity is the largest blob leaf data length (Rust
// LEAF_CAPACITY).
const BlobLeafCapacity = PageSize - blobLeafFixed

// DecodeBlobBranchFields parses one fixed 16-byte blob branch entry
// without the child-page-number check (Rust blob_tree::
// decode_branch_record: the reserved word must be zero and the fields
// decode raw; the graph walk bounds-checks the child).
func DecodeBlobBranchFields(b []byte) (offset uint64, child uint32, err error) {
	if len(b) != BlobBranchSize {
		return 0, 0, headerErr("blob branch size %d", len(b))
	}
	if U32(b[12:16]) != 0 {
		return 0, 0, headerErr("blob branch reserved")
	}
	return U64(b[0:8]), U32(b[8:12]), nil
}

// DecodeBlobLeafGeometry proves the blob leaf layout from the raw
// header fields (Rust blob_tree::leaf_geometry): the fixed leaf
// identity (type, kind, item count, level, lower, upper), the reserved
// span, the 8-byte aligned start and length windows, and the
// non-final-full-leaf rule. The common header identity (magic, flags,
// header size, born transaction) is the caller's check.
func DecodeBlobLeafGeometry(page []byte, expectedLevel *uint16, expectedStart uint64, totalBytes uint64) (BlobLeafGeometry, error) {
	if len(page) != PageSize {
		return BlobLeafGeometry{}, headerErr("blob leaf length %d", len(page))
	}
	dataLen := int(U16(page[40:42]))
	start := U64(page[32:40])
	end := start + uint64(dataLen)
	if end < start {
		return BlobLeafGeometry{}, headerErr("blob leaf end overflows")
	}
	if (expectedLevel != nil && *expectedLevel != 0) ||
		U16(page[16:18]) != 1 ||
		U16(page[18:20]) != 0 ||
		U16(page[20:22]) != uint16(blobLeafFixed+dataLen) ||
		U16(page[22:24]) != PageSize ||
		page[4] != byte(PageTypeBlobLeaf) ||
		U32(page[24:28]) != BlobKindMembership {
		return BlobLeafGeometry{}, headerErr("membership blob leaf identity is malformed")
	}
	for i := 42; i < 48; i++ {
		if page[i] != 0 {
			return BlobLeafGeometry{}, headerErr("membership blob leaf layout is malformed")
		}
	}
	if dataLen < 1 || dataLen > BlobLeafCapacity || dataLen%8 != 0 {
		return BlobLeafGeometry{}, headerErr("membership blob leaf layout is malformed")
	}
	if start != expectedStart || start%8 != 0 || end > totalBytes {
		return BlobLeafGeometry{}, headerErr("membership blob leaf layout is malformed")
	}
	if end < totalBytes && dataLen != BlobLeafCapacity {
		return BlobLeafGeometry{}, headerErr("membership blob leaf layout is malformed")
	}
	return BlobLeafGeometry{Start: start, End: end, DataLen: dataLen}, nil
}

// BlobCommonValid reports the non-born common identity of one blob page
// (Rust page_header::common_valid: page size, magic, zero flags, and
// the exact 32-byte header).
func BlobCommonValid(page []byte) bool {
	return len(page) == PageSize && magic4(page, PageMagic[:]) &&
		page[5] == 0 && U16(page[6:8]) == SlottedHeaderSize
}

// BlobBornValid reports the born-transaction window of one blob page
// (Rust page_header::born_valid).
func BlobBornValid(page []byte, selectedTxn uint64) bool {
	born := U64(page[8:16])
	return born != 0 && born <= selectedTxn
}
