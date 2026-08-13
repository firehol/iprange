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

// DecodeRangeRecordV4 parses one 12-byte IPv4 range record.
func DecodeRangeRecordV4(b []byte) (RangeRecordV4, error) {
	if len(b) < RangeRecordV4Size {
		return RangeRecordV4{}, headerErr("short range record %d", len(b))
	}
	r := RangeRecordV4{From: U32(b[0:4]), To: U32(b[4:8]), Value: U32(b[8:12])}
	if r.From > r.To {
		return RangeRecordV4{}, headerErr("reversed range")
	}
	return r, nil
}

// DecodeRangeRecordV6 parses one 36-byte IPv6 range record.
func DecodeRangeRecordV6(b []byte) (RangeRecordV6, error) {
	if len(b) < RangeRecordV6Size {
		return RangeRecordV6{}, headerErr("short range record %d", len(b))
	}
	fromHi, fromLo := U128(b[0:16])
	toHi, toLo := U128(b[16:32])
	r := RangeRecordV6{FromHi: fromHi, FromLo: fromLo, ToHi: toHi, ToLo: toLo, Value: U32(b[32:36])}
	if fromHi > toHi || (fromHi == toHi && fromLo > toLo) {
		return RangeRecordV6{}, headerErr("reversed range")
	}
	return r, nil
}

// DecodeRangeEntryV4 parses one 8-byte IPv4 branch entry.
func DecodeRangeEntryV4(b []byte) (firstFrom uint32, child uint32, err error) {
	if len(b) < RangeEntryV4Size {
		return 0, 0, headerErr("short range entry %d", len(b))
	}
	first, child := U32(b[0:4]), U32(b[4:8])
	if !PageNumberValid(child, MaxPageCount) {
		return 0, 0, headerErr("range child out of range")
	}
	return first, child, nil
}

// DecodeRangeEntryV6 parses one 20-byte IPv6 branch entry.
func DecodeRangeEntryV6(b []byte) (firstFromHi, firstFromLo uint64, child uint32, err error) {
	if len(b) < RangeEntryV6Size {
		return 0, 0, 0, headerErr("short range entry %d", len(b))
	}
	hi, lo := U128(b[0:16])
	child = U32(b[16:20])
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
