package format

// Feed catalog records (binary-format-v4.md section 8).

const (
	MinFeedNameLen  = 1
	MaxFeedNameLen  = 255
	nameRecordFixed = 12
)

// FeedNameValid checks the exact v4 feed-name grammar: 1..255 lowercase
// ASCII letters, digits, `_`, `-`, or `.`, beginning and ending with a letter
// or digit.
func FeedNameValid(name []byte) bool {
	return FeedNameValidString(string(name))
}

// FeedNameValidString is the zero-allocation string variant used by the
// reader hot path.
func FeedNameValidString(name string) bool {
	n := len(name)
	if n < MinFeedNameLen || n > MaxFeedNameLen {
		return false
	}
	for i := 0; i < n; i++ {
		c := name[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '_' || c == '-' || c == '.'
		if !ok {
			return false
		}
		if (i == 0 || i == n-1) && !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// CatalogNameRecord is a name-tree leaf record (feed_index + exact name).
type CatalogNameRecord struct {
	FeedIndex uint32
	Name      []byte
}

// DecodeCatalogNameRecord parses one variable-length name record. The record
// bytes are the remainder of the slotted record slice; the returned name
// aliases the page view and must not outlive the operation.
func DecodeCatalogNameRecord(b []byte) (CatalogNameRecord, error) {
	if len(b) < nameRecordFixed+1 {
		return CatalogNameRecord{}, headerErr("short catalog record %d", len(b))
	}
	recordLen := U16(b[0:2])
	if U16(b[2:4]) != 0 {
		return CatalogNameRecord{}, headerErr("catalog record flags nonzero")
	}
	if int(recordLen) < nameRecordFixed+1 || int(recordLen) > len(b) {
		return CatalogNameRecord{}, headerErr("catalog record length %d", recordLen)
	}
	feedIndex := U32(b[4:8])
	nameLen := int(b[8])
	if b[9] != 0 || b[10] != 0 || b[11] != 0 {
		return CatalogNameRecord{}, headerErr("catalog record reserved bytes")
	}
	if int(recordLen) != nameRecordFixed+nameLen {
		return CatalogNameRecord{}, headerErr("catalog record length %d vs name %d", recordLen, nameLen)
	}
	if !FeedNameValid(b[12 : 12+nameLen]) {
		return CatalogNameRecord{}, headerErr("invalid feed name")
	}
	return CatalogNameRecord{FeedIndex: feedIndex, Name: b[12 : 12+nameLen]}, nil
}

// CatalogNameBranchRecord is a name-tree branch record (first name of the
// child subtree).
type CatalogNameBranchRecord struct {
	Child     uint32
	FirstName []byte
}

// DecodeCatalogNameBranch parses one variable-length name branch record.
func DecodeCatalogNameBranch(b []byte) (CatalogNameBranchRecord, error) {
	if len(b) < nameRecordFixed+1 {
		return CatalogNameBranchRecord{}, headerErr("short catalog branch %d", len(b))
	}
	recordLen := U16(b[0:2])
	if U16(b[2:4]) != 0 {
		return CatalogNameBranchRecord{}, headerErr("catalog branch flags nonzero")
	}
	if int(recordLen) < nameRecordFixed+1 || int(recordLen) > len(b) {
		return CatalogNameBranchRecord{}, headerErr("catalog branch length %d", recordLen)
	}
	child := U32(b[4:8])
	if !PageNumberValid(child, MaxPageCount) {
		return CatalogNameBranchRecord{}, headerErr("catalog child out of range")
	}
	nameLen := int(b[8])
	if b[9] != 0 || b[10] != 0 || b[11] != 0 {
		return CatalogNameBranchRecord{}, headerErr("catalog branch reserved bytes")
	}
	if int(recordLen) != nameRecordFixed+nameLen {
		return CatalogNameBranchRecord{}, headerErr("catalog branch name mismatch")
	}
	if !FeedNameValid(b[12 : 12+nameLen]) {
		return CatalogNameBranchRecord{}, headerErr("invalid catalog branch name")
	}
	return CatalogNameBranchRecord{Child: child, FirstName: b[12 : 12+nameLen]}, nil
}

// CatalogIndexBranchRecord is a numeric-tree branch record: fixed 8 bytes.
type CatalogIndexBranchRecord struct {
	FirstIndex uint32
	Child      uint32
}

const CatalogIndexBranchSize = 8

// DecodeCatalogIndexBranch parses one fixed 8-byte numeric branch entry.
func DecodeCatalogIndexBranch(b []byte) (CatalogIndexBranchRecord, error) {
	if len(b) < CatalogIndexBranchSize {
		return CatalogIndexBranchRecord{}, headerErr("short index branch %d", len(b))
	}
	r := CatalogIndexBranchRecord{FirstIndex: U32(b[0:4]), Child: U32(b[4:8])}
	if !PageNumberValid(r.Child, MaxPageCount) {
		return CatalogIndexBranchRecord{}, headerErr("index child out of range")
	}
	return r, nil
}
