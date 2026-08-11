package reader

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Structured-value lookup (binary-format-v4.md section 9A). The
// NetworkEnrichmentV1View is a logical handle: every value() or membership
// read re-derives a checked mapped view at call time.

// NetworkEnrichmentV1View exposes one network_enrichment_v1 structure entry.
type NetworkEnrichmentV1View struct {
	r            *ImmutableReader
	id           uint32
	recordPage   uint32
	recordOff    uint16
	membershipID uint32
}

// ID returns the internal structure ID.
func (v NetworkEnrichmentV1View) ID() uint32 { return v.id }

// MembershipID returns the payload's internal membership ID (zero = none).
func (v NetworkEnrichmentV1View) MembershipID() uint32 { return v.membershipID }

// Value returns the decoded network_enrichment_v1 payload.
func (v NetworkEnrichmentV1View) Value() (format.NetworkEnrichmentV1, error) {
	page, err := v.r.page(v.recordPage)
	if err != nil {
		return format.NetworkEnrichmentV1{}, err
	}
	rec, err := structureRecordAt(page, v.r.meta, v.id, v.recordOff)
	if err != nil {
		return format.NetworkEnrichmentV1{}, err
	}
	return format.DecodeNetworkEnrichmentV1(rec.Payload)
}

// ThreatMembership returns the linked membership bitmap, or a zero view when
// the payload has no threat membership.
func (v NetworkEnrichmentV1View) ThreatMembership() (MembershipView, error) {
	if v.membershipID == 0 {
		return MembershipView{}, nil
	}
	return v.r.lookupMembershipID(v.membershipID)
}

// LookupNetworkEnrichmentV14 returns the structure covering addr, or false
// when the address is absent.
func (r *ImmutableReader) LookupNetworkEnrichmentV14(addr uint32) (NetworkEnrichmentV1View, bool, error) {
	value, found, err := r.LookupDirect4(addr)
	if err != nil || !found {
		return NetworkEnrichmentV1View{}, false, err
	}
	view, err := r.lookupStructureID(value)
	if err != nil {
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
	view, err := r.lookupStructureID(value)
	if err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	return view, true, nil
}

// lookupStructureID resolves one nonzero structure ID through the sparse
// radix table.
func (r *ImmutableReader) lookupStructureID(id uint32) (NetworkEnrichmentV1View, error) {
	if id == 0 {
		return NetworkEnrichmentV1View{}, corrupt("zero structure id lookup")
	}
	root := r.meta.StructureIDRoot
	if root == 0 {
		return NetworkEnrichmentV1View{}, corrupt("structure dictionary empty")
	}
	expectedLevel, ok := format.StructureRootLevel(r.meta.StructureIDLimit)
	if !ok {
		return NetworkEnrichmentV1View{}, corrupt("structure root level overflow")
	}
	cur := root
	level, err := r.structureLevel(cur)
	if err != nil {
		return NetworkEnrichmentV1View{}, err
	}
	if level != expectedLevel {
		return NetworkEnrichmentV1View{}, corrupt("structure root level %d expected %d", level, expectedLevel)
	}
	for level > 0 {
		page, err := r.page(cur)
		if err != nil {
			return NetworkEnrichmentV1View{}, err
		}
		child, err := structureDirectoryChild(page, r.meta, level, id)
		if err != nil {
			return NetworkEnrichmentV1View{}, err
		}
		if child == 0 {
			// A stored range value naming an absent structure record is
			// corruption (mirroring structured_value/view.rs: absent
			// structure ID).
			return NetworkEnrichmentV1View{}, corrupt("range names an absent structure ID")
		}
		cur = child
		level--
	}
	page, err := r.page(cur)
	if err != nil {
		return NetworkEnrichmentV1View{}, err
	}
	rec, err := structureRecordAt(page, r.meta, id, 0)
	if err != nil {
		return NetworkEnrichmentV1View{}, err
	}
	return NetworkEnrichmentV1View{
		r:            r,
		id:           id,
		recordPage:   cur,
		recordOff:    rec.slotOff,
		membershipID: payloadMembershipID(page, rec),
	}, nil
}

// structureDirectoryChild follows one directory level for id. The child index
// derives directly from the ID and the level span.
func structureDirectoryChild(page []byte, meta format.Meta, level uint32, id uint32) (uint32, error) {
	h, err := format.DecodePageHeader(page, meta.TxnID)
	if err != nil {
		return 0, err
	}
	if h.PageType != format.PageTypeStructureIDDirectory || h.Aux != uint32(meta.StructureKind) || uint32(h.Level) != level {
		return 0, corrupt("structure directory page")
	}
	span, ok := format.StructureSpanOfLevel(level - 1)
	if !ok {
		return 0, corrupt("structure span overflow")
	}
	idx := (uint64(id) / span) % format.StructureDirectoryChildCount
	off := uint64(32) + idx*4
	if off+4 > 2080 {
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

// structureRecordAt decodes the record at the implied slot for id. When
// slotOff is zero, the slot is derived from the ID; otherwise the slot is
// used as-is (view re-reads).
func structureRecordAt(page []byte, meta format.Meta, id uint32, slotOff uint16) (structureRecord, error) {
	h, err := format.DecodePageHeader(page, meta.TxnID)
	if err != nil {
		return structureRecord{}, err
	}
	if h.PageType != format.PageTypeStructureIDRecord || h.Aux != uint32(meta.StructureKind) || h.Level != 0 {
		return structureRecord{}, corrupt("structure record page")
	}
	if slotOff == 0 {
		slotOff = uint16(uint64(id) % format.StructureRecordSlots)
	}
	slot := uint32(slotOff)
	if slot >= format.StructureRecordSlots {
		return structureRecord{}, corrupt("structure slot %d beyond capacity", slot)
	}
	off := uint32(32) + slot*format.StructureRecordSize
	if off+format.StructureRecordSize > format.PageSize {
		return structureRecord{}, corrupt("structure slot out of page")
	}
	rec, err := format.DecodeStructureIDRecord(page[off : off+format.StructureRecordSize])
	if err != nil {
		return structureRecord{}, err
	}
	if rec.StructureID != id {
		return structureRecord{}, corrupt("structure id %d at slot implying %d", rec.StructureID, id)
	}
	return structureRecord{slotOff: slotOff, StructureIDRecord: rec}, nil
}

// payloadMembershipID extracts the payload's membership reference without a
// second decode pass: record base 32 + slot*80, payload at +48, membership
// field at payload offset 24.
func payloadMembershipID(page []byte, rec structureRecord) uint32 {
	base := uint32(32) + uint32(rec.slotOff)*format.StructureRecordSize + 48 + 24
	return format.U32(page[base : base+4])
}

func (r *ImmutableReader) structureLevel(pgno uint32) (uint32, error) {
	page, err := r.page(pgno)
	if err != nil {
		return 0, err
	}
	h, err := format.DecodePageHeader(page, r.meta.TxnID)
	if err != nil {
		return 0, err
	}
	if h.Level > format.MaxTreeLevel {
		return 0, corrupt("structure level %d over max", h.Level)
	}
	return uint32(h.Level), nil
}
