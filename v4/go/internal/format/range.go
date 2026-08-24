package format

// Range record codecs (binary-format-v4.md section 6).

// RangeRecordV4 is one IPv4 leaf record: from, to, value.
type RangeRecordV4 struct {
	From  uint32
	To    uint32
	Value uint32
}

// RangeRecordV6 is one IPv6 leaf record: from, to, value (value is u32).
type RangeRecordV6 struct {
	FromHi, FromLo uint64
	ToHi, ToLo     uint64
	Value          uint32
}

const (
	RangeRecordV4Size = 12
	RangeEntryV4Size  = 8
	RangeRecordV6Size = 36
	RangeEntryV6Size  = 20
)

// DecodeRangeFieldsV4 parses one 12-byte IPv4 range record without the
// reversed-endpoint check (Rust range_tree.rs decode_fields: the size is
// exact and the endpoints decode raw; the reversed rule is a caller check
// so validation can report it as the RangeReversed finding).
func DecodeRangeFieldsV4(b []byte) (RangeRecordV4, error) {
	if len(b) != RangeRecordV4Size {
		return RangeRecordV4{}, headerErr("range record has the wrong size %d", len(b))
	}
	return RangeRecordV4{From: U32(b[0:4]), To: U32(b[4:8]), Value: U32(b[8:12])}, nil
}

// DecodeRangeRecordV4 parses one 12-byte IPv4 range record: the raw
// fields plus the reversed-endpoint rejection of the reader contract.
func DecodeRangeRecordV4(b []byte) (RangeRecordV4, error) {
	r, err := DecodeRangeFieldsV4(b)
	if err != nil {
		return RangeRecordV4{}, err
	}
	if r.From > r.To {
		return RangeRecordV4{}, headerErr("reversed range")
	}
	return r, nil
}

// DecodeRangeFieldsV6 parses one 36-byte IPv6 range record without the
// reversed-endpoint check (Rust range_tree.rs decode_fields).
func DecodeRangeFieldsV6(b []byte) (RangeRecordV6, error) {
	if len(b) != RangeRecordV6Size {
		return RangeRecordV6{}, headerErr("range record has the wrong size %d", len(b))
	}
	fromHi, fromLo := U128(b[0:16])
	toHi, toLo := U128(b[16:32])
	return RangeRecordV6{FromHi: fromHi, FromLo: fromLo, ToHi: toHi, ToLo: toLo, Value: U32(b[32:36])}, nil
}

// DecodeRangeRecordV6 parses one 36-byte IPv6 range record: the raw
// fields plus the reversed-endpoint rejection of the reader contract.
func DecodeRangeRecordV6(b []byte) (RangeRecordV6, error) {
	r, err := DecodeRangeFieldsV6(b)
	if err != nil {
		return RangeRecordV6{}, err
	}
	if r.FromHi > r.ToHi || (r.FromHi == r.ToHi && r.FromLo > r.ToLo) {
		return RangeRecordV6{}, headerErr("reversed range")
	}
	return r, nil
}

// DecodeRangeEntryFieldsV4 parses one 8-byte IPv4 branch entry without
// the child-page-number check (Rust range_tree.rs decode_branch: the
// child decodes raw; the graph walk bounds-checks it). The caller's
// cell is bounded by the inspected record extents, so a shorter cell is
// malformed and a longer one is a prefix of the following records.
func DecodeRangeEntryFieldsV4(b []byte) (firstFrom uint32, child uint32, err error) {
	if len(b) < RangeEntryV4Size {
		return 0, 0, headerErr("short range entry %d", len(b))
	}
	return U32(b[0:4]), U32(b[4:8]), nil
}

// DecodeRangeEntryV4 parses one 8-byte IPv4 branch entry: the raw fields
// plus the child-page-number check of the reader contract (the original
// reader semantics are unchanged).
func DecodeRangeEntryV4(b []byte) (firstFrom uint32, child uint32, err error) {
	first, child, err := DecodeRangeEntryFieldsV4(b)
	if err != nil {
		return 0, 0, err
	}
	if !PageNumberValid(child, MaxPageCount) {
		return 0, 0, headerErr("range child out of range")
	}
	return first, child, nil
}

// DecodeRangeEntryFieldsV6 parses one 20-byte IPv6 branch entry without
// the child-page-number check (Rust range_tree.rs decode_branch).
func DecodeRangeEntryFieldsV6(b []byte) (firstFromHi, firstFromLo uint64, child uint32, err error) {
	if len(b) < RangeEntryV6Size {
		return 0, 0, 0, headerErr("short range entry %d", len(b))
	}
	hi, lo := U128(b[0:16])
	return hi, lo, U32(b[16:20]), nil
}

// DecodeRangeEntryV6 parses one 20-byte IPv6 branch entry: the raw fields
// plus the child-page-number check of the reader contract.
func DecodeRangeEntryV6(b []byte) (firstFromHi, firstFromLo uint64, child uint32, err error) {
	hi, lo, child, err := DecodeRangeEntryFieldsV6(b)
	if err != nil {
		return 0, 0, 0, err
	}
	if !PageNumberValid(child, MaxPageCount) {
		return 0, 0, 0, headerErr("range child out of range")
	}
	return hi, lo, child, nil
}

// Key-only codecs for the fixed-tree search primitive (search.go). Each
// probe reads exactly the key fields of one record, never the payload or
// the child; the selected record is decoded once by the caller after the
// search, mirroring fixed_tree/page.rs read_key + branch_child. The length
// guard keeps the key offset inside the checked record slice.

// RangeEntryKeyV4 reads the first_from key of one IPv4 branch entry.
func RangeEntryKeyV4(b []byte) (uint32, error) {
	if len(b) < RangeEntryV4Size {
		return 0, headerErr("short range entry %d", len(b))
	}
	return U32(b[0:4]), nil
}

// RangeEntryKeyV6 reads the first_from key of one IPv6 branch entry.
func RangeEntryKeyV6(b []byte) (uint64, uint64, error) {
	if len(b) < RangeEntryV6Size {
		return 0, 0, headerErr("short range entry %d", len(b))
	}
	hi, lo := U128(b[0:16])
	return hi, lo, nil
}

// RangeRecordKeyV4 reads the from key of one IPv4 range record.
func RangeRecordKeyV4(b []byte) (uint32, error) {
	if len(b) < RangeRecordV4Size {
		return 0, headerErr("short range record %d", len(b))
	}
	return U32(b[0:4]), nil
}

// RangeRecordKeyV6 reads the from key of one IPv6 range record.
func RangeRecordKeyV6(b []byte) (uint64, uint64, error) {
	if len(b) < RangeRecordV6Size {
		return 0, 0, headerErr("short range record %d", len(b))
	}
	hi, lo := U128(b[0:16])
	return hi, lo, nil
}

// EncodeRangeRecordV4 writes one 12-byte IPv4 range record into b,
// mirroring range_tree.rs encode_record (little-endian from, to, value).
func EncodeRangeRecordV4(r RangeRecordV4, b []byte) error {
	if r.From > r.To {
		return &Error{Code: CodeInvalidArgument, Detail: "range start is after its end"}
	}
	if len(b) < RangeRecordV4Size {
		return &Error{Code: CodeOSUnsupported, Detail: "range record buffer is too small"}
	}
	PutU32(b[0:4], r.From)
	PutU32(b[4:8], r.To)
	PutU32(b[8:12], r.Value)
	return nil
}

// EncodeRangeRecordV6 writes one 36-byte IPv6 range record into b,
// mirroring range_tree.rs encode_record (little-endian from, to, value).
func EncodeRangeRecordV6(r RangeRecordV6, b []byte) error {
	if r.FromHi > r.ToHi || (r.FromHi == r.ToHi && r.FromLo > r.ToLo) {
		return &Error{Code: CodeInvalidArgument, Detail: "range start is after its end"}
	}
	if len(b) < RangeRecordV6Size {
		return &Error{Code: CodeOSUnsupported, Detail: "range record buffer is too small"}
	}
	PutU128(b[0:16], r.FromHi, r.FromLo)
	PutU128(b[16:32], r.ToHi, r.ToLo)
	PutU32(b[32:36], r.Value)
	return nil
}
