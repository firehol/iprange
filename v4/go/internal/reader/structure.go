package reader

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Structured-value lookup (binary-format-v4.md section 9A). The
// NetworkEnrichmentV1View is a logical handle: every value() or membership
// read re-derives a checked mapped view at call time.

// NetworkEnrichmentV1View exposes one network_enrichment_v1 structure entry.
// The payload is validated and decoded during the lookup, mirroring
// structured_value/view.rs (decode_mapped inside the lookup).
type NetworkEnrichmentV1View struct {
	r     *ImmutableReader
	id    uint32
	value format.NetworkEnrichmentV1
}

// ID returns the internal structure ID.
func (v NetworkEnrichmentV1View) ID() uint32 { return v.id }

// MembershipID returns the payload's internal membership ID (zero = none).
func (v NetworkEnrichmentV1View) MembershipID() uint32 { return v.value.MembershipID }

// Value returns the payload decoded at lookup time; it performs no further
// mapped access.
func (v NetworkEnrichmentV1View) Value() (format.NetworkEnrichmentV1, error) {
	return v.value, nil
}

// ThreatMembership returns the linked membership bitmap, or a zero view when
// the payload has no threat membership.
func (v NetworkEnrichmentV1View) ThreatMembership() (MembershipView, error) {
	if v.value.MembershipID == 0 {
		return MembershipView{}, nil
	}
	return v.r.lookupMembershipID(v.value.MembershipID)
}

// LookupNetworkEnrichmentV14 returns the structure covering addr, or false
// when the address is absent.
func (r *ImmutableReader) LookupNetworkEnrichmentV14(addr uint32) (NetworkEnrichmentV1View, bool, error) {
	value, found, err := r.LookupDirect4(addr)
	if err != nil || !found {
		return NetworkEnrichmentV1View{}, false, err
	}
	view, found, err := r.lookupStructureID(value)
	if err != nil || !found {
		return NetworkEnrichmentV1View{}, false, err
	}
	return view, true, nil
}

// LookupNetworkEnrichmentV16 returns the structure covering addr, or false
// when the address is absent.
func (r *ImmutableReader) LookupNetworkEnrichmentV16(addrHi, addrLo uint64) (NetworkEnrichmentV1View, bool, error) {
	value, found, err := r.LookupDirect6(addrHi, addrLo)
	if err != nil || !found {
		return NetworkEnrichmentV1View{}, false, err
	}
	view, found, err := r.lookupStructureID(value)
	if err != nil || !found {
		return NetworkEnrichmentV1View{}, false, err
	}
	return view, true, nil
}

// lookupStructureID resolves one structure ID through the sparse radix
// table, mirroring structured_value/table.rs: an id of zero or at/above the
// limit, an empty root, an empty directory child, or a zero slot cell is a
// clean miss; inconsistent pages or slots are corruption.
func (r *ImmutableReader) lookupStructureID(id uint32) (NetworkEnrichmentV1View, bool, error) {
	if id == 0 || uint64(id) >= r.meta.StructureIDLimit {
		return NetworkEnrichmentV1View{}, false, nil
	}
	root := r.meta.StructureIDRoot
	if root == 0 {
		return NetworkEnrichmentV1View{}, false, nil
	}
	level, ok := format.StructureRootLevel(r.meta.StructureIDLimit)
	if !ok {
		return NetworkEnrichmentV1View{}, false, corrupt("structure root level overflow")
	}
	cur := root
	for level > 0 {
		page, err := r.page(cur)
		if err != nil {
			return NetworkEnrichmentV1View{}, false, err
		}
		child, err := structureDirectoryChild(page, r.meta, level, id)
		if err != nil {
			return NetworkEnrichmentV1View{}, false, err
		}
		if child == 0 {
			return NetworkEnrichmentV1View{}, false, nil // empty child: clean miss
		}
		cur, level = child, level-1
	}
	page, err := r.page(cur)
	if err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	rec, found, err := structureRecordAt(page, r.meta, id)
	if err != nil || !found {
		return NetworkEnrichmentV1View{}, false, err
	}
	value, err := format.DecodeNetworkEnrichmentV1(rec.Payload)
	if err != nil {
		return NetworkEnrichmentV1View{}, false, corrupt("structure payload: %v", err)
	}
	return NetworkEnrichmentV1View{r: r, id: id, value: value}, true, nil
}

// structureDirectoryChild follows one directory level for id. The child
// index derives directly from the ID and the level span: a directory at
// level L covers R*512^(L-1) IDs per child (table.rs child_index divides
// by coverage(level-1)).
func structureDirectoryChild(page []byte, meta format.Meta, level uint32, id uint32) (uint32, error) {
	h, err := format.DecodePageHeader(page, meta.TxnID)
	if err != nil {
		return 0, err
	}
	if h.PageType != format.PageTypeStructureIDDirectory || h.Aux != uint32(meta.StructureKind) || uint32(h.Level) != level {
		return 0, corrupt("structure directory page")
	}
	if h.Lower != format.StructureBranchEnd || h.Upper != format.PageSize {
		return 0, corrupt("structure branch geometry")
	}
	if h.ItemCount < 1 || h.ItemCount > format.StructureDirectoryChildCount {
		return 0, corrupt("structure branch item count %d", h.ItemCount)
	}
	span, ok := format.StructureSpanOfLevel(level)
	if !ok {
		return 0, corrupt("structure span overflow")
	}
	idx := (uint64(id) / span) % format.StructureDirectoryChildCount
	off := uint64(32) + idx*4
	if off+4 > uint64(format.StructureBranchEnd) {
		return 0, corrupt("structure child index out of range")
	}
	child := format.U32(page[off : off+4])
	if child != 0 && !format.PageNumberValid(child, meta.PageCount) {
		return 0, corrupt("structure child out of range")
	}
	return child, nil
}

// structureRecord is a decoded structure-ID record plus its slot offset.
type structureRecord struct {
	slotOff uint16
	format.StructureIDRecord
}

// structureRecordAt decodes the record at the implied slot for id. An
// all-zero slot cell is a clean miss; any stored id other than the slot's
// is corruption (table.rs read_record).
func structureRecordAt(page []byte, meta format.Meta, id uint32) (structureRecord, bool, error) {
	h, err := format.DecodePageHeader(page, meta.TxnID)
	if err != nil {
		return structureRecord{}, false, err
	}
	if h.PageType != format.PageTypeStructureIDRecord || h.Aux != uint32(meta.StructureKind) || h.Level != 0 {
		return structureRecord{}, false, corrupt("structure record page")
	}
	if h.Lower != format.StructureLeafEnd || h.Upper != format.PageSize {
		return structureRecord{}, false, corrupt("structure leaf geometry")
	}
	if h.ItemCount < 1 || h.ItemCount > format.StructureRecordSlots {
		return structureRecord{}, false, corrupt("structure leaf item count %d", h.ItemCount)
	}
	slot := uint64(id) % format.StructureRecordSlots
	off := uint32(32) + uint32(slot*format.StructureRecordSize)
	if off+format.StructureRecordSize > format.PageSize {
		return structureRecord{}, false, corrupt("structure slot out of page")
	}
	cell := page[off : off+format.StructureRecordSize]
	if allZero(cell) {
		return structureRecord{}, false, nil
	}
	rec, err := format.DecodeStructureIDRecord(cell)
	if err != nil {
		return structureRecord{}, false, err
	}
	if rec.StructureID != id {
		return structureRecord{}, false, corrupt("structure id %d at slot implying %d", rec.StructureID, id)
	}
	return structureRecord{slotOff: uint16(slot), StructureIDRecord: rec}, true, nil
}

// allZero reports whether b is entirely zero.
func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
