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
