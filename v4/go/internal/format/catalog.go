package format

// Feed catalog records (binary-format-v4.md section 8).

const (
	MinFeedNameLen  = 1
	MaxFeedNameLen  = 255
	nameRecordFixed = 12

	// MinCatalogNameRecord and MaxCatalogNameRecord bound the variable
	// name records of the catalog trees (Rust feed_catalog
	// MIN_NAME_RECORD/MAX_NAME_RECORD; the validation layout proof uses
	// them).
	MinCatalogNameRecord = nameRecordFixed + MinFeedNameLen
	MaxCatalogNameRecord = nameRecordFixed + MaxFeedNameLen
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

// DecodeCatalogEntry parses one variable-length name record (Rust
// feed_catalog codec decode_entry): the record length proves the name
// span, the flags and reserved bytes are zero, and the name grammar is
// exact; the index field is raw and is not a page number here (the name
// branch record reuses the index field for the child page, and the
// reader checks that contract at the branch layer). The returned name
// aliases the page view and must not outlive the operation.
func DecodeCatalogEntry(b []byte) (feedIndex uint32, name []byte, err error) {
	if len(b) < nameRecordFixed+1 {
		return 0, nil, headerErr("short catalog record %d", len(b))
	}
	recordLen := U16(b[0:2])
	if U16(b[2:4]) != 0 {
		return 0, nil, headerErr("catalog record flags nonzero")
	}
	if int(recordLen) < nameRecordFixed+1 || int(recordLen) > len(b) {
		return 0, nil, headerErr("catalog record length %d", recordLen)
	}
	feedIndex = U32(b[4:8])
	nameLen := int(b[8])
	if b[9] != 0 || b[10] != 0 || b[11] != 0 {
		return 0, nil, headerErr("catalog record reserved bytes")
	}
	if int(recordLen) != nameRecordFixed+nameLen {
		return 0, nil, headerErr("catalog record length %d vs name %d", recordLen, nameLen)
	}
	if !FeedNameValid(b[12 : 12+nameLen]) {
		return 0, nil, headerErr("invalid feed name")
	}
	return feedIndex, b[12 : 12+nameLen], nil
}

// DecodeCatalogNameRecord parses one variable-length name leaf record.
func DecodeCatalogNameRecord(b []byte) (CatalogNameRecord, error) {
	feedIndex, name, err := DecodeCatalogEntry(b)
	if err != nil {
		return CatalogNameRecord{}, err
	}
	return CatalogNameRecord{FeedIndex: feedIndex, Name: name}, nil
}

// CatalogNameBranchRecord is a name-tree branch record (first name of the
// child subtree).
type CatalogNameBranchRecord struct {
	Child     uint32
	FirstName []byte
}

// DecodeCatalogNameBranch parses one variable-length name branch record:
// the raw entry plus the child-page-number check of the reader contract.
func DecodeCatalogNameBranch(b []byte) (CatalogNameBranchRecord, error) {
	child, name, err := DecodeCatalogEntry(b)
	if err != nil {
		return CatalogNameBranchRecord{}, err
	}
	if !PageNumberValid(child, MaxPageCount) {
		return CatalogNameBranchRecord{}, headerErr("catalog child out of range")
	}
	return CatalogNameBranchRecord{Child: child, FirstName: name}, nil
}

// CatalogIndexBranchRecord is a numeric-tree branch record: fixed 8 bytes.
type CatalogIndexBranchRecord struct {
	FirstIndex uint32
	Child      uint32
}

const CatalogIndexBranchSize = 8

// DecodeCatalogIndexBranchFields parses one fixed 8-byte numeric branch
// entry without the child-page-number check (Rust feed_catalog codec
// decode_index_branch: the fields are raw; the graph walk bounds-checks
// the child). The caller's cell is bounded by the inspected record
// extents, so a shorter cell is malformed and a longer one is a prefix
// of the following records.
func DecodeCatalogIndexBranchFields(b []byte) (firstIndex, child uint32, err error) {
	if len(b) < CatalogIndexBranchSize {
		return 0, 0, headerErr("short index branch %d", len(b))
	}
	return U32(b[0:4]), U32(b[4:8]), nil
}

// DecodeCatalogIndexBranch parses one fixed 8-byte numeric branch entry:
// the raw fields plus the child-page-number check of the reader contract
// (the original reader semantics are unchanged).
func DecodeCatalogIndexBranch(b []byte) (CatalogIndexBranchRecord, error) {
	firstIndex, child, err := DecodeCatalogIndexBranchFields(b)
	if err != nil {
		return CatalogIndexBranchRecord{}, err
	}
	if !PageNumberValid(child, MaxPageCount) {
		return CatalogIndexBranchRecord{}, headerErr("index child out of range")
	}
	return CatalogIndexBranchRecord{FirstIndex: firstIndex, Child: child}, nil
}

// CatalogNameBranchKey decodes the first name of one name-branch record
// without touching (or validating) the child field. The record shape and
// name grammar are the key: they are validated on every probe, mirroring
// feed_catalog.rs read_key = decode_entry; the child is read and validated
// only when the record is selected.
func CatalogNameBranchKey(b []byte) ([]byte, error) {
	if len(b) < nameRecordFixed+1 {
		return nil, headerErr("short catalog branch %d", len(b))
	}
	recordLen := U16(b[0:2])
	if U16(b[2:4]) != 0 {
		return nil, headerErr("catalog branch flags nonzero")
	}
	if int(recordLen) < nameRecordFixed+1 || int(recordLen) > len(b) {
		return nil, headerErr("catalog branch length %d", recordLen)
	}
	nameLen := int(b[8])
	if b[9] != 0 || b[10] != 0 || b[11] != 0 {
		return nil, headerErr("catalog branch reserved bytes")
	}
	if int(recordLen) != nameRecordFixed+nameLen {
		return nil, headerErr("catalog branch name mismatch")
	}
	if !FeedNameValid(b[12 : 12+nameLen]) {
		return nil, headerErr("invalid catalog branch name")
	}
	return b[12 : 12+nameLen], nil
}
