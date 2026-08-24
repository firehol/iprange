package recovery

// Recovery range-tree scan (Rust recovery/range_scan.rs): one ordered
// tree walk over the mapped range tree which streams page and record
// events instead of findings. A refused page (claims, bounds, checksum,
// type, header, or layout) is reported as one unknown envelope and the
// walk continues; order, fence, and reversed-record defects stream
// their envelopes; readable records stream through the range event.
// The family-specific codecs mirror the Rust IpKey split.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// rangeKey is one inclusive address key of the recovery range surface
// (the Rust IpKey peer: IPv4 keys use the hi limb, IPv6 keys both).
type rangeKey struct {
	hi uint64
	lo uint64
}

// rangeRecord is one decoded range record (Rust range_tree::Record).
type rangeRecord struct {
	from  rangeKey
	to    rangeKey
	value uint32
}

// lessRecord orders records by from, to, then value (the Rust
// sort_unstable comparator).
func lessRecord(codec rangeCodec, left, right rangeRecord) bool {
	if codec.lessKey(left.from, right.from) {
		return true
	}
	if codec.lessKey(right.from, left.from) {
		return false
	}
	if codec.lessKey(left.to, right.to) {
		return true
	}
	if codec.lessKey(right.to, left.to) {
		return false
	}
	return left.value < right.value
}

// rangeCodec abstracts the family-specific range tree operations
// (Rust DirectKey + IpKey).
type rangeCodec interface {
	leafCell() int
	branchCell() int
	recordSize() int
	aux() uint32
	decodeRecord(cell []byte) (rangeRecord, bool)
	decodeBranch(cell []byte) (rangeKey, uint32, bool)
	nextKey(key rangeKey) (rangeKey, bool)
	lessKey(a, b rangeKey) bool
	fence(from, to rangeKey) validation.ValidationAddressFence
	pushRecord(builder *writer.OutputBuilder, record rangeRecord) error
	reportAccepted(rep *reporter, record rangeRecord) error
	reportRejected(rep *reporter, count uint64, from, to rangeKey) error
}

// rangeV4Codec is the IPv4 range codec (Rust Ipv4Key).
type rangeV4Codec struct{}

func (rangeV4Codec) leafCell() int   { return 12 }
func (rangeV4Codec) branchCell() int { return 8 }
func (rangeV4Codec) recordSize() int { return 12 }
func (rangeV4Codec) aux() uint32     { return uint32(format.AddressFamilyIPv4) }
func (rangeV4Codec) decodeRecord(cell []byte) (rangeRecord, bool) {
	if len(cell) < format.RangeRecordV4Size {
		return rangeRecord{}, false
	}
	decoded, err := format.DecodeRangeFieldsV4(cell[:format.RangeRecordV4Size])
	if err != nil {
		return rangeRecord{}, false
	}
	return rangeRecord{
		from:  rangeKey{hi: uint64(decoded.From)},
		to:    rangeKey{hi: uint64(decoded.To)},
		value: decoded.Value,
	}, true
}
func (rangeV4Codec) decodeBranch(cell []byte) (rangeKey, uint32, bool) {
	if len(cell) < format.RangeEntryV4Size {
		return rangeKey{}, 0, false
	}
	from, child, err := format.DecodeRangeEntryV4(cell[:format.RangeEntryV4Size])
	if err != nil {
		return rangeKey{}, 0, false
	}
	return rangeKey{hi: uint64(from)}, child, true
}
func (rangeV4Codec) nextKey(key rangeKey) (rangeKey, bool) {
	if key.hi == ^uint64(0) {
		return rangeKey{}, false
	}
	return rangeKey{hi: key.hi + 1}, true
}
func (rangeV4Codec) lessKey(a, b rangeKey) bool { return a.hi < b.hi }
func (rangeV4Codec) fence(from, to rangeKey) validation.ValidationAddressFence {
	return validation.ValidationAddressFence{IPv4: true, From: from.hi, To: to.hi}
}
func (rangeV4Codec) pushRecord(builder *writer.OutputBuilder, record rangeRecord) error {
	return builder.PushDirectV4(uint32(record.from.hi), uint32(record.to.hi), record.value)
}
func (rangeV4Codec) reportAccepted(rep *reporter, record rangeRecord) error {
	return rep.rangeAcceptedV4(uint32(record.from.hi), uint32(record.to.hi))
}
func (rangeV4Codec) reportRejected(rep *reporter, count uint64, from, to rangeKey) error {
	return rep.rangesRejectedV4(count, uint32(from.hi), uint32(to.hi))
}

// rangeV6Codec is the IPv6 range codec (Rust Ipv6Key).
type rangeV6Codec struct{}

func (rangeV6Codec) leafCell() int   { return 36 }
func (rangeV6Codec) branchCell() int { return 24 }
func (rangeV6Codec) recordSize() int { return 40 }
func (rangeV6Codec) aux() uint32     { return uint32(format.AddressFamilyIPv6) }
func (rangeV6Codec) decodeRecord(cell []byte) (rangeRecord, bool) {
	if len(cell) < format.RangeRecordV6Size {
		return rangeRecord{}, false
	}
	decoded, err := format.DecodeRangeFieldsV6(cell[:format.RangeRecordV6Size])
	if err != nil {
		return rangeRecord{}, false
	}
	return rangeRecord{
		from:  rangeKey{hi: decoded.FromHi, lo: decoded.FromLo},
		to:    rangeKey{hi: decoded.ToHi, lo: decoded.ToLo},
		value: decoded.Value,
	}, true
}
func (rangeV6Codec) decodeBranch(cell []byte) (rangeKey, uint32, bool) {
	if len(cell) < format.RangeEntryV6Size {
		return rangeKey{}, 0, false
	}
	fromHi, fromLo, child, err := format.DecodeRangeEntryV6(cell[:format.RangeEntryV6Size])
	if err != nil {
		return rangeKey{}, 0, false
	}
	return rangeKey{hi: fromHi, lo: fromLo}, child, true
}
func (rangeV6Codec) nextKey(key rangeKey) (rangeKey, bool) {
	if key.lo == ^uint64(0) {
		if key.hi == ^uint64(0) {
			return rangeKey{}, false
		}
		return rangeKey{hi: key.hi + 1}, true
	}
	return rangeKey{hi: key.hi, lo: key.lo + 1}, true
}
func (rangeV6Codec) lessKey(a, b rangeKey) bool {
	if a.hi != b.hi {
		return a.hi < b.hi
	}
	return a.lo < b.lo
}
func (rangeV6Codec) fence(from, to rangeKey) validation.ValidationAddressFence {
	var fromV6, toV6 [16]byte
	putUint64(fromV6[0:8], from.hi)
	putUint64(fromV6[8:16], from.lo)
	putUint64(toV6[0:8], to.hi)
	putUint64(toV6[8:16], to.lo)
	return validation.ValidationAddressFence{IPv4: false, FromV6: fromV6, ToV6: toV6}
}
func (rangeV6Codec) pushRecord(builder *writer.OutputBuilder, record rangeRecord) error {
	return builder.PushDirectV6(record.from.hi, record.from.lo, record.to.hi, record.to.lo, record.value)
}
func (rangeV6Codec) reportAccepted(rep *reporter, record rangeRecord) error {
	return rep.rangeAcceptedV6(record.from.hi, record.from.lo, record.to.hi, record.to.lo)
}
func (rangeV6Codec) reportRejected(rep *reporter, count uint64, from, to rangeKey) error {
	return rep.rangesRejectedV6(count, from.hi, from.lo, to.hi, to.lo)
}

// putUint64 writes one little-endian limb (the validation fence
// encoding of the recovery report helpers).
func putUint64(dst []byte, value uint64) {
	for i := 0; i < 8; i++ {
		dst[i] = byte(value >> (8 * i))
	}
}

// rangeEvents consumes one recovery range scan (Rust RangeEvents).
type rangeEvents interface {
	pageAccepted() error
	pageRejected(ioUnreadable bool) error
	unknown(reason validation.ValidationReason, page *uint32, unbounded bool) error
	rangeEvent(page uint32, record *rangeRecord) error
}

// scanRanges walks the range tree of the selected generation (Rust
// range_scan::scan: an absent root is the empty tree).
func scanRanges(codec rangeCodec, m *mapping.Mapping, meta format.Meta, pages *pageSet, check func() error, events rangeEvents) error {
	if meta.RangeRoot == 0 {
		return nil
	}
	var path [format.MaxTreeLevel + 1]uint32
	_, err := scanRangeNode(codec, m, meta, meta.RangeRoot, nil, &path, 0, pages, check, events)
	return err
}

// scanRangeNode walks one node of the range tree (Rust scan_node: a
// refused node is reported through the events and the walk returns
// its first key, if any).
func scanRangeNode(codec rangeCodec, m *mapping.Mapping, meta format.Meta, pageNumber uint32, expectedLevel *uint16, path *[format.MaxTreeLevel + 1]uint32, depth int, pages *pageSet, check func() error, events rangeEvents) (*rangeKey, error) {
	if err := live.Checkpoint(check); err != nil {
		return nil, err
	}
	claimed, reason, err := claimRangePage(meta, pageNumber, path, depth, pages)
	if err != nil {
		return nil, err
	}
	if !claimed {
		if err := events.unknown(reason, &pageNumber, true); err != nil {
			return nil, err
		}
		return nil, nil
	}
	page, header, err := readRangePage(codec, m, meta, pageNumber, expectedLevel, events)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, nil
	}
	if header.Level == 0 {
		return scanRangeLeaf(codec, pageNumber, page, header, events)
	}
	return scanRangeBranch(codec, m, meta, pageNumber, page, header, path, depth, pages, check, events)
}

// claimRangePage claims one tree node through the page set (Rust
// claim_page in range_scan.rs).
func claimRangePage(meta format.Meta, pageNumber uint32, path *[format.MaxTreeLevel + 1]uint32, depth int, pages *pageSet) (bool, validation.ValidationReason, error) {
	return pages.claim(pageNumber, meta.PageCount, path[:], depth)
}

// readRangePage reads and parses one range tree node (Rust
// read_range_page: a refused page or header streams its envelope and
// returns nil, an accepted page counts the page event).
func readRangePage(codec rangeCodec, m *mapping.Mapping, meta format.Meta, pageNumber uint32, expectedLevel *uint16, events rangeEvents) ([]byte, *format.PageHeader, error) {
	page, problem := checkedPage(m, pageNumber, meta.PageCount)
	if problem != nil {
		if err := rejectRangePage(events, pageNumber, problem.reason, problem.ioUnreadable); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	}
	header, ok, err := parseRangePage(codec, page, meta, pageNumber, expectedLevel, events)
	if err != nil || !ok {
		return nil, nil, err
	}
	if err := events.pageAccepted(); err != nil {
		return nil, nil, err
	}
	return page, header, nil
}

// parseRangePage runs the range tree page inspection (Rust
// parse_range_page: the type class maps to PageTypeMismatch, every
// header or layout refusal to PageHeaderInvalid).
func parseRangePage(codec rangeCodec, page []byte, meta format.Meta, pageNumber uint32, expectedLevel *uint16, events rangeEvents) (*format.PageHeader, bool, error) {
	header, problem := format.InspectTreeHeader(page, meta.TxnID, byte(format.PageTypeRangeBranch), byte(format.PageTypeRangeLeaf), uint32(meta.AddressFamily), expectedLevel)
	if problem != format.TreeHeaderProblemNone {
		reason := validation.ReasonPageHeaderInvalid
		if problem == format.TreeHeaderProblemType {
			reason = validation.ReasonPageTypeMismatch
		}
		if err := rejectRangePage(events, pageNumber, reason, false); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	cellLen := codec.leafCell()
	if header.Level != 0 {
		cellLen = codec.branchCell()
	}
	inspection := format.InspectLayout(page, &header, format.FixedLayout(cellLen))
	if inspection == nil || inspection.ReservedNonzero {
		if err := rejectRangePage(events, pageNumber, validation.ReasonPageHeaderInvalid, false); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	return &header, true, nil
}

// rejectRangePage streams one refused page (Rust reject_page in
// range_scan.rs).
func rejectRangePage(events rangeEvents, pageNumber uint32, reason validation.ValidationReason, ioUnreadable bool) error {
	if err := events.pageRejected(ioUnreadable); err != nil {
		return err
	}
	return events.unknown(reason, &pageNumber, true)
}

// scanRangeLeaf streams the records of one leaf page (Rust scan_leaf:
// the order and reversed-record defects stream their envelopes, the
// readable records stream the range event).
func scanRangeLeaf(codec rangeCodec, pageNumber uint32, page []byte, header *format.PageHeader, events rangeEvents) (*rangeKey, error) {
	var first *rangeKey
	var previous *rangeKey
	slotted, err := format.OpenSlottedHeader(page, *header, format.PageTypeRangeLeaf, codec.aux(), format.SlotItemsPerPage)
	if err != nil {
		return nil, err
	}
	for index := 0; index < int(header.ItemCount); index++ {
		cell, err := slotted.Record(index)
		if err != nil {
			return nil, err
		}
		decoded, ok := codec.decodeRecord(cell)
		if !ok {
			return nil, pageDecodeError()
		}
		if first == nil {
			value := decoded.from
			first = &value
		}
		if previous != nil && !codec.lessKey(*previous, decoded.from) {
			if err := events.unknown(validation.ReasonTreeOrderInvalid, &pageNumber, false); err != nil {
				return nil, err
			}
		}
		value := decoded.from
		previous = &value
		var record *rangeRecord
		if codec.lessKey(decoded.from, decoded.to) || decoded.from == decoded.to {
			r := decoded
			record = &r
		} else {
			if err := events.unknown(validation.ReasonRangeReversed, &pageNumber, true); err != nil {
				return nil, err
			}
		}
		if err := events.rangeEvent(pageNumber, record); err != nil {
			return nil, err
		}
	}
	return first, nil
}

// pageDecodeError is the fixed decode failure of one validated cell
// (unreachable for a layout-valid page; the machine class keeps the
// sweep honest).
func pageDecodeError() error {
	return &format.Error{Code: format.CodeFormatInvalid, Detail: "recovery range cell decode failed"}
}

// scanRangeBranch walks one branch page (Rust scan_branch: the order
// and fence defects stream their envelopes, and the walk descends into
// every child).
func scanRangeBranch(codec rangeCodec, m *mapping.Mapping, meta format.Meta, pageNumber uint32, page []byte, header *format.PageHeader, path *[format.MaxTreeLevel + 1]uint32, depth int, pages *pageSet, check func() error, events rangeEvents) (*rangeKey, error) {
	var first *rangeKey
	var previous *rangeKey
	slotted, err := format.OpenSlottedHeader(page, *header, format.PageTypeRangeBranch, codec.aux(), format.SlotItemsPerPage)
	if err != nil {
		return nil, err
	}
	for index := 0; index < int(header.ItemCount); index++ {
		if err := live.Checkpoint(check); err != nil {
			return nil, err
		}
		cell, err := slotted.Record(index)
		if err != nil {
			return nil, err
		}
		key, child, ok := codec.decodeBranch(cell)
		if !ok {
			return nil, pageDecodeError()
		}
		if first == nil {
			value := key
			first = &value
		}
		if previous != nil && !codec.lessKey(*previous, key) {
			if err := events.unknown(validation.ReasonTreeOrderInvalid, &pageNumber, false); err != nil {
				return nil, err
			}
		}
		value := key
		previous = &value
		expected := header.Level - 1
		actual, err := scanRangeNode(codec, m, meta, child, &expected, path, depth+1, pages, check, events)
		if err != nil {
			return nil, err
		}
		if actual != nil && *actual != key {
			if err := events.unknown(validation.ReasonTreeFenceInvalid, &pageNumber, false); err != nil {
				return nil, err
			}
		}
	}
	return first, nil
}
