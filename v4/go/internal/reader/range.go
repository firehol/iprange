package reader

import (
	"fmt"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// corrupt reports a locally detectable structural violation. Ordinary access
// performs these bounded checks; full graph/CRC validation is explicit only.
func corrupt(pattern string, args ...any) error {
	return &format.Error{Code: format.CodeFormatInvalid, Detail: fmt.Sprintf(pattern, args...)}
}

// exactRecordView bounds a page-end record view to its exact record
// size. The wire record decoders require exact cells (Rust
// decode_fields parity), while SlottedPage.Record returns a view that
// runs to the end of the page.
func exactRecordView(b []byte, size int) ([]byte, error) {
	if len(b) < size {
		return nil, corrupt("short range record %d", len(b))
	}
	return b[:size], nil
}

// Range tree access (binary-format-v4.md sections 5.3 and 6). Lookup
// allocates no heap bytes: branch and leaf records are decoded from checked
// mapped views into bounded scalars, and descent verifies the exact one-level
// decrease plus the expected page type and aux before following any pointer.

// LookupDirect4 returns the direct value covering addr, or false when the
// address is absent.
func (r *ImmutableReader) LookupDirect4(addr uint32) (uint32, bool, error) {
	if r.meta.RangeRoot == 0 {
		return 0, false, nil
	}
	work.TreeLookup(1)
	return r.lookupRange4(r.meta.RangeRoot, addr)
}

// LookupDirect6 returns the direct value covering addr, or false when the
// address is absent.
func (r *ImmutableReader) LookupDirect6(addrHi, addrLo uint64) (uint32, bool, error) {
	if r.meta.RangeRoot == 0 {
		return 0, false, nil
	}
	work.TreeLookup(1)
	return r.lookupRange6(r.meta.RangeRoot, addrHi, addrLo)
}

func (r *ImmutableReader) lookupRange4(root uint32, addr uint32) (uint32, bool, error) {
	cur := root
	level := uint16(0)
	first := true
	for {
		page, err := r.page(cur)
		if err != nil {
			return 0, false, err
		}
		h, err := format.DecodePageHeader(page, r.meta.TxnID)
		if err != nil {
			return 0, false, err
		}
		if first {
			level = h.Level // the root's own level starts the descent
			first = false
		} else if h.Level != level {
			return 0, false, corrupt("range level %d expected %d", h.Level, level)
		}
		sl, err := format.OpenSlottedHeader(page, h, h.PageType, uint32(r.meta.AddressFamily), format.SlotItemsPerPage)
		if err != nil {
			return 0, false, err
		}
		switch h.PageType {
		case format.PageTypeRangeBranch:
			if level == 0 {
				return 0, false, corrupt("zero-level range branch")
			}
			child, err := rangeBranchChild4(sl, addr, r.meta.PageCount)
			if err != nil {
				return 0, false, err
			}
			if child == 0 {
				return 0, false, nil // no branch key qualifies: absent
			}
			work.TreeDescent(1)
			cur, level = child, level-1
		case format.PageTypeRangeLeaf:
			rec, found, err := rangeLeafLookup4(sl, addr)
			if err != nil || !found {
				return 0, false, err
			}
			return rec.Value, true, nil
		default:
			return 0, false, corrupt("unexpected range page type %d", h.PageType)
		}
	}
}

func (r *ImmutableReader) lookupRange6(root uint32, addrHi, addrLo uint64) (uint32, bool, error) {
	cur := root
	level := uint16(0)
	first := true
	for {
		page, err := r.page(cur)
		if err != nil {
			return 0, false, err
		}
		h, err := format.DecodePageHeader(page, r.meta.TxnID)
		if err != nil {
			return 0, false, err
		}
		if first {
			level = h.Level // the root's own level starts the descent
			first = false
		} else if h.Level != level {
			return 0, false, corrupt("range level %d expected %d", h.Level, level)
		}
		sl, err := format.OpenSlottedHeader(page, h, h.PageType, uint32(r.meta.AddressFamily), format.SlotItemsPerPage)
		if err != nil {
			return 0, false, err
		}
		switch h.PageType {
		case format.PageTypeRangeBranch:
			if level == 0 {
				return 0, false, corrupt("zero-level range branch")
			}
			child, err := rangeBranchChild6(sl, addrHi, addrLo, r.meta.PageCount)
			if err != nil {
				return 0, false, err
			}
			if child == 0 {
				return 0, false, nil // no branch key qualifies: absent
			}
			work.TreeDescent(1)
			cur, level = child, level-1
		case format.PageTypeRangeLeaf:
			rec, found, err := rangeLeafLookup6(sl, addrHi, addrLo)
			if err != nil || !found {
				return 0, false, err
			}
			return rec.Value, true, nil
		default:
			return 0, false, corrupt("unexpected range page type %d", h.PageType)
		}
	}
}

// rangeBranchChild4 finds the greatest branch entry with first_from <= addr
// through the shared search primitive: every probe reads only the key
// (first_from); the selected entry is decoded once and its child validated.
func rangeBranchChild4(sl format.SlottedPage, addr uint32, pageCount uint64) (uint32, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		first, err := format.RangeEntryKeyV4(b)
		if err != nil {
			return 0, err
		}
		return cmpU32(first, addr), nil
	}
	best, err := greatestLE(int(sl.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return 0, err
	}
	b, err := sl.Record(best)
	if err != nil {
		return 0, err
	}
	_, child, err := format.DecodeRangeEntryV4(b)
	if err != nil || !format.PageNumberValid(child, pageCount) {
		return 0, corrupt("range child out of range")
	}
	return child, nil
}

// rangeBranchChild6 finds the greatest branch entry with first_from <= addr.
func rangeBranchChild6(sl format.SlottedPage, addrHi, addrLo uint64, pageCount uint64) (uint32, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		hi, lo, err := format.RangeEntryKeyV6(b)
		if err != nil {
			return 0, err
		}
		return cmpU128(hi, lo, addrHi, addrLo), nil
	}
	best, err := greatestLE(int(sl.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return 0, err
	}
	b, err := sl.Record(best)
	if err != nil {
		return 0, err
	}
	_, _, child, err := format.DecodeRangeEntryV6(b)
	if err != nil || !format.PageNumberValid(child, pageCount) {
		return 0, corrupt("range child out of range")
	}
	return child, nil
}

// rangeLeafLookup4 finds the greatest record with from <= addr by binary
// search and tests the inclusive upper bound. Probes read the from key
// only; the selected record is decoded once.
func rangeLeafLookup4(leaf format.SlottedPage, addr uint32) (format.RangeRecordV4, bool, error) {
	cmp := func(i int) (int, error) {
		b, err := leaf.Record(i)
		if err != nil {
			return 0, err
		}
		from, err := format.RangeRecordKeyV4(b)
		if err != nil {
			return 0, err
		}
		return cmpU32(from, addr), nil
	}
	best, err := greatestLE(int(leaf.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return format.RangeRecordV4{}, false, err
	}
	work.LeafValidation(1)
	b, err := leaf.Record(best)
	if err != nil {
		return format.RangeRecordV4{}, false, err
	}
	b, err = exactRecordView(b, format.RangeRecordV4Size)
	if err != nil {
		return format.RangeRecordV4{}, false, err
	}
	rec, err := format.DecodeRangeRecordV4(b)
	if err != nil {
		return format.RangeRecordV4{}, false, err
	}
	if rec.To < addr {
		return format.RangeRecordV4{}, false, nil
	}
	return rec, true, nil
}

// rangeLeafLookup6 finds the greatest record with from <= addr by binary
// search and tests the inclusive upper bound. Probes read the from key
// only; the selected record is decoded once.
func rangeLeafLookup6(leaf format.SlottedPage, addrHi, addrLo uint64) (format.RangeRecordV6, bool, error) {
	cmp := func(i int) (int, error) {
		b, err := leaf.Record(i)
		if err != nil {
			return 0, err
		}
		hi, lo, err := format.RangeRecordKeyV6(b)
		if err != nil {
			return 0, err
		}
		return cmpU128(hi, lo, addrHi, addrLo), nil
	}
	best, err := greatestLE(int(leaf.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return format.RangeRecordV6{}, false, err
	}
	work.LeafValidation(1)
	b, err := leaf.Record(best)
	if err != nil {
		return format.RangeRecordV6{}, false, err
	}
	b, err = exactRecordView(b, format.RangeRecordV6Size)
	if err != nil {
		return format.RangeRecordV6{}, false, err
	}
	rec, err := format.DecodeRangeRecordV6(b)
	if err != nil {
		return format.RangeRecordV6{}, false, err
	}
	if rec.ToHi < addrHi || (rec.ToHi == addrHi && rec.ToLo < addrLo) {
		return format.RangeRecordV6{}, false, nil
	}
	return rec, true, nil
}

// RangeVisit4 is one range record yielded by ScanDirect4.
type RangeVisit4 struct {
	From, To, Value uint32
}

// RangeVisit6 is one range record yielded by ScanDirect6.
type RangeVisit6 struct {
	FromHi, FromLo, ToHi, ToLo uint64
	Value                      uint32
}

// ScanDirect4 visits every committed range record in ascending key order.
func (r *ImmutableReader) ScanDirect4(visit func(RangeVisit4) error) error {
	if r.meta.RangeRoot == 0 {
		return nil
	}
	return r.walkRange4(r.meta.RangeRoot, visit)
}

func (r *ImmutableReader) walkRange4(pgno uint32, visit func(RangeVisit4) error) error {
	page, err := r.page(pgno)
	if err != nil {
		return err
	}
	h, err := format.DecodePageHeader(page, r.meta.TxnID)
	if err != nil {
		return err
	}
	sl, err := format.OpenSlottedHeader(page, h, h.PageType, uint32(r.meta.AddressFamily), format.SlotItemsPerPage)
	if err != nil {
		return err
	}
	switch h.PageType {
	case format.PageTypeRangeBranch:
		if h.Level == 0 {
			return corrupt("zero-level range branch")
		}
		for i := 0; i < int(sl.Header.ItemCount); i++ {
			b, err := sl.Record(i)
			if err != nil {
				return err
			}
			_, child, err := format.DecodeRangeEntryV4(b)
			if err != nil {
				return err
			}
			if !format.PageNumberValid(child, r.meta.PageCount) {
				return corrupt("range child out of range")
			}
			// The child must sit exactly one level below this branch; the
			// recursion depth is therefore bounded by MaxTreeLevel.
			if err := r.walkRangeDescend4(child, h.Level-1, visit); err != nil {
				return err
			}
		}
		return nil
	case format.PageTypeRangeLeaf:
		return r.walkRangeLeaf4(sl, visit)
	default:
		return corrupt("unexpected range page type %d", h.PageType)
	}
}

func (r *ImmutableReader) walkRangeDescend4(pgno uint32, expectedLevel uint16, visit func(RangeVisit4) error) error {
	page, err := r.page(pgno)
	if err != nil {
		return err
	}
	h, err := format.DecodePageHeader(page, r.meta.TxnID)
	if err != nil {
		return err
	}
	if h.Level != expectedLevel {
		return corrupt("range level %d expected %d", h.Level, expectedLevel)
	}
	sl, err := format.OpenSlottedHeader(page, h, h.PageType, uint32(r.meta.AddressFamily), format.SlotItemsPerPage)
	if err != nil {
		return err
	}
	switch h.PageType {
	case format.PageTypeRangeBranch:
		if h.Level == 0 {
			return corrupt("zero-level range branch")
		}
		for i := 0; i < int(sl.Header.ItemCount); i++ {
			b, err := sl.Record(i)
			if err != nil {
				return err
			}
			_, child, err := format.DecodeRangeEntryV4(b)
			if err != nil {
				return err
			}
			if !format.PageNumberValid(child, r.meta.PageCount) {
				return corrupt("range child out of range")
			}
			if err := r.walkRangeDescend4(child, h.Level-1, visit); err != nil {
				return err
			}
		}
		return nil
	case format.PageTypeRangeLeaf:
		if h.Level != 0 {
			return corrupt("range leaf level %d", h.Level)
		}
		return r.walkRangeLeaf4(sl, visit)
	default:
		return corrupt("unexpected range page type %d", h.PageType)
	}
}

func (r *ImmutableReader) walkRangeLeaf4(sl format.SlottedPage, visit func(RangeVisit4) error) error {
	for i := 0; i < int(sl.Header.ItemCount); i++ {
		b, err := sl.Record(i)
		if err != nil {
			return err
		}
		b, err = exactRecordView(b, format.RangeRecordV4Size)
		if err != nil {
			return err
		}
		rec, err := format.DecodeRangeRecordV4(b)
		if err != nil {
			return err
		}
		if err := visit(RangeVisit4{From: rec.From, To: rec.To, Value: rec.Value}); err != nil {
			return err
		}
	}
	return nil
}

// ScanDirect6 visits every committed range record in ascending key order.
func (r *ImmutableReader) ScanDirect6(visit func(RangeVisit6) error) error {
	if r.meta.RangeRoot == 0 {
		return nil
	}
	return r.walkRange6(r.meta.RangeRoot, visit)
}

func (r *ImmutableReader) walkRange6(pgno uint32, visit func(RangeVisit6) error) error {
	page, err := r.page(pgno)
	if err != nil {
		return err
	}
	h, err := format.DecodePageHeader(page, r.meta.TxnID)
	if err != nil {
		return err
	}
	sl, err := format.OpenSlottedHeader(page, h, h.PageType, uint32(r.meta.AddressFamily), format.SlotItemsPerPage)
	if err != nil {
		return err
	}
	switch h.PageType {
	case format.PageTypeRangeBranch:
		if h.Level == 0 {
			return corrupt("zero-level range branch")
		}
		for i := 0; i < int(sl.Header.ItemCount); i++ {
			b, err := sl.Record(i)
			if err != nil {
				return err
			}
			_, _, child, err := format.DecodeRangeEntryV6(b)
			if err != nil {
				return err
			}
			if !format.PageNumberValid(child, r.meta.PageCount) {
				return corrupt("range child out of range")
			}
			if err := r.walkRangeDescend6(child, h.Level-1, visit); err != nil {
				return err
			}
		}
		return nil
	case format.PageTypeRangeLeaf:
		return r.walkRangeLeaf6(sl, visit)
	default:
		return corrupt("unexpected range page type %d", h.PageType)
	}
}

func (r *ImmutableReader) walkRangeDescend6(pgno uint32, expectedLevel uint16, visit func(RangeVisit6) error) error {
	page, err := r.page(pgno)
	if err != nil {
		return err
	}
	h, err := format.DecodePageHeader(page, r.meta.TxnID)
	if err != nil {
		return err
	}
	if h.Level != expectedLevel {
		return corrupt("range level %d expected %d", h.Level, expectedLevel)
	}
	sl, err := format.OpenSlottedHeader(page, h, h.PageType, uint32(r.meta.AddressFamily), format.SlotItemsPerPage)
	if err != nil {
		return err
	}
	switch h.PageType {
	case format.PageTypeRangeBranch:
		if h.Level == 0 {
			return corrupt("zero-level range branch")
		}
		for i := 0; i < int(sl.Header.ItemCount); i++ {
			b, err := sl.Record(i)
			if err != nil {
				return err
			}
			_, _, child, err := format.DecodeRangeEntryV6(b)
			if err != nil {
				return err
			}
			if !format.PageNumberValid(child, r.meta.PageCount) {
				return corrupt("range child out of range")
			}
			if err := r.walkRangeDescend6(child, h.Level-1, visit); err != nil {
				return err
			}
		}
		return nil
	case format.PageTypeRangeLeaf:
		if h.Level != 0 {
			return corrupt("range leaf level %d", h.Level)
		}
		return r.walkRangeLeaf6(sl, visit)
	default:
		return corrupt("unexpected range page type %d", h.PageType)
	}
}

func (r *ImmutableReader) walkRangeLeaf6(sl format.SlottedPage, visit func(RangeVisit6) error) error {
	for i := 0; i < int(sl.Header.ItemCount); i++ {
		b, err := sl.Record(i)
		if err != nil {
			return err
		}
		b, err = exactRecordView(b, format.RangeRecordV6Size)
		if err != nil {
			return err
		}
		rec, err := format.DecodeRangeRecordV6(b)
		if err != nil {
			return err
		}
		if err := visit(RangeVisit6{FromHi: rec.FromHi, FromLo: rec.FromLo, ToHi: rec.ToHi, ToLo: rec.ToLo, Value: rec.Value}); err != nil {
			return err
		}
	}
	return nil
}

// Cardinality returns the exact inclusive sum over every committed range
// record.
func (r *ImmutableReader) Cardinality() (format.Cardinality129, error) {
	total := format.CardinalityZero()
	var err error
	if r.meta.AddressFamily == format.AddressFamilyIPv4 {
		err = r.ScanDirect4(func(v RangeVisit4) error {
			n, e := format.IPv4Inclusive(v.From, v.To)
			if e != nil {
				return corrupt("bad range during cardinality")
			}
			total, e = total.Add(n)
			return e
		})
	} else {
		err = r.ScanDirect6(func(v RangeVisit6) error {
			n, e := format.IPv6Inclusive(v.FromHi, v.FromLo, v.ToHi, v.ToLo)
			if e != nil {
				return corrupt("bad range during cardinality")
			}
			total, e = total.Add(n)
			return e
		})
	}
	return total, err
}
