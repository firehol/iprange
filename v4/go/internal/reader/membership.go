package reader

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Membership dictionary access (binary-format-v4.md section 9).
//
// MembershipView is a logical handle, not a page view: every word read
// re-derives a checked mapped view at call time, so the handle is valid for
// the lifetime of the reader and allocates no heap bytes.

// MembershipView exposes one canonical membership bitmap.
type MembershipView struct {
	r          *ImmutableReader
	id         uint32
	wordCount  uint32
	bitmapLen  uint32
	storage    format.MembershipStorage
	recordPage uint32 // leaf page holding the record (inline)
	recordOff  uint16 // record offset within that page (inline)
	blobRoot   uint32 // blob tree root (blob storage)
}

// ID returns the internal membership ID.
func (v MembershipView) ID() uint32 { return v.id }

// WordCount returns the number of 64-bit words in the canonical bitmap.
func (v MembershipView) WordCount() uint32 { return v.wordCount }

// Word returns word i of the canonical bitmap (little-endian u64), or false
// when i is outside [0, word_count).
func (v MembershipView) Word(i uint32) (uint64, bool, error) {
	if i >= v.wordCount {
		return 0, false, nil
	}
	b, err := v.wordBytes(i)
	if err != nil {
		return 0, false, err
	}
	return format.U64(b), true, nil
}

// ContainsIndex reports whether feed_index is set in the bitmap. A set bit
// beyond word_count or at an unknown index is absent by definition.
func (v MembershipView) ContainsIndex(feedIndex uint32) (bool, error) {
	word := feedIndex / 64
	if word >= v.wordCount {
		return false, nil
	}
	b, err := v.wordBytes(word)
	if err != nil {
		return false, err
	}
	return format.U64(b)&(uint64(1)<<(feedIndex%64)) != 0, nil
}

// wordBytes returns the 8 mapped bytes of word i, re-validated at call time.
func (v MembershipView) wordBytes(i uint32) ([]byte, error) {
	byteOff := uint64(i) * 8
	switch v.storage {
	case format.MembershipStorageInline:
		return v.inlineBytes(byteOff)
	case format.MembershipStorageBlob:
		return v.r.blobRead(v.blobRoot, format.BlobKindMembership, byteOff, 8)
	default:
		return nil, corrupt("membership storage %d", v.storage)
	}
}

// inlineBytes re-opens the record page, re-decodes the record, and returns
// the 8 mapped bytes at byteOff inside the inline bitmap.
func (v MembershipView) inlineBytes(byteOff uint64) ([]byte, error) {
	if byteOff+8 > uint64(v.bitmapLen) {
		return nil, corrupt("inline bitmap offset out of range")
	}
	page, err := v.r.page(v.recordPage)
	if err != nil {
		return nil, err
	}
	sl, err := format.OpenSlotted(page, v.r.meta.TxnID, format.PageTypeMembershipIDLeaf, 0, format.SlotItemsPerPage)
	if err != nil {
		return nil, err
	}
	rec, err := sl.Record(int(v.recordOff))
	if err != nil {
		return nil, err
	}
	leaf, err := format.DecodeMembershipIDLeaf(rec)
	if err != nil {
		return nil, err
	}
	if leaf.Storage != format.MembershipStorageInline || leaf.MembershipID != v.id {
		return nil, corrupt("membership record changed")
	}
	if leaf.WordCount != v.wordCount {
		return nil, corrupt("membership word count changed")
	}
	return leaf.Inline[byteOff : byteOff+8], nil
}

// LookupMembership4 returns the membership bitmap covering addr, or false
// when the address is absent.
func (r *ImmutableReader) LookupMembership4(addr uint32) (MembershipView, bool, error) {
	value, found, err := r.LookupDirect4(addr)
	if err != nil || !found {
		return MembershipView{}, false, err
	}
	view, err := r.lookupMembershipID(value)
	if err != nil {
		return MembershipView{}, false, err
	}
	return view, true, nil
}

// LookupMembership6 returns the membership bitmap covering addr, or false
// when the address is absent.
func (r *ImmutableReader) LookupMembership6(addrHi, addrLo uint64) (MembershipView, bool, error) {
	value, found, err := r.LookupDirect6(addrHi, addrLo)
	if err != nil || !found {
		return MembershipView{}, false, err
	}
	view, err := r.lookupMembershipID(value)
	if err != nil {
		return MembershipView{}, false, err
	}
	return view, true, nil
}

// lookupMembershipID resolves one nonzero membership ID through the ID tree.
func (r *ImmutableReader) lookupMembershipID(id uint32) (MembershipView, error) {
	if id == 0 {
		return MembershipView{}, corrupt("zero membership id lookup")
	}
	root := r.meta.MembershipIDRoot
	if root == 0 {
		return MembershipView{}, corrupt("membership dictionary empty")
	}
	cur := root
	level, err := r.membershipLevel(cur)
	if err != nil {
		return MembershipView{}, err
	}
	for {
		page, err := r.page(cur)
		if err != nil {
			return MembershipView{}, err
		}
		h, err := format.DecodePageHeader(page, r.meta.TxnID)
		if err != nil {
			return MembershipView{}, err
		}
		if h.Level != level {
			return MembershipView{}, corrupt("membership level %d expected %d", h.Level, level)
		}
		sl, err := format.OpenSlotted(page, r.meta.TxnID, h.PageType, 0, format.SlotItemsPerPage)
		if err != nil {
			return MembershipView{}, err
		}
		switch h.PageType {
		case format.PageTypeMembershipIDBranch:
			if level == 0 {
				return MembershipView{}, corrupt("zero-level membership branch")
			}
			child, err := membershipBranchChild(sl, id, r.meta.PageCount)
			if err != nil {
				return MembershipView{}, err
			}
			cur, level = child, level-1
		case format.PageTypeMembershipIDLeaf:
			slot, leaf, found, err := membershipLeafFind(sl, id)
			if err != nil || !found {
				return MembershipView{}, err
			}
			v := MembershipView{
				r:          r,
				id:         leaf.MembershipID,
				wordCount:  leaf.WordCount,
				bitmapLen:  leaf.BitmapLen,
				storage:    leaf.Storage,
				recordPage: cur,
				recordOff:  slot,
				blobRoot:   leaf.BlobRoot,
			}
			return v, nil
		default:
			return MembershipView{}, corrupt("unexpected membership page type %d", h.PageType)
		}
	}
}

func (r *ImmutableReader) membershipLevel(pgno uint32) (uint16, error) {
	page, err := r.page(pgno)
	if err != nil {
		return 0, err
	}
	h, err := format.DecodePageHeader(page, r.meta.TxnID)
	if err != nil {
		return 0, err
	}
	if h.Level > format.MaxTreeLevel {
		return 0, corrupt("membership level %d over max", h.Level)
	}
	return h.Level, nil
}

// membershipBranchChild finds the greatest branch entry with first_id <= id.
func membershipBranchChild(sl format.SlottedPage, id uint32, pageCount uint64) (uint32, error) {
	probe := func(i int) (format.MembershipIDBranchRecord, error) {
		b, err := sl.Record(i)
		if err != nil {
			return format.MembershipIDBranchRecord{}, err
		}
		return format.DecodeMembershipIDBranch(b)
	}
	lo, hi := 0, int(sl.Header.ItemCount)
	best := -1
	for lo < hi {
		mid := lo + (hi-lo)/2
		rec, err := probe(mid)
		if err != nil {
			return 0, err
		}
		if rec.FirstID <= id {
			best = mid
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if best < 0 {
		return 0, corrupt("membership branch has no qualifying child")
	}
	rec, err := probe(best)
	if err != nil || !format.PageNumberValid(rec.Child, pageCount) {
		return 0, corrupt("membership child out of range")
	}
	return rec.Child, nil
}

// membershipLeafFind finds the record with the exact membership ID and
// returns its slot number and decoded record.
func membershipLeafFind(sl format.SlottedPage, id uint32) (uint16, format.MembershipIDLeaf, bool, error) {
	probe := func(i int) (format.MembershipIDLeaf, error) {
		b, err := sl.Record(i)
		if err != nil {
			return format.MembershipIDLeaf{}, err
		}
		return format.DecodeMembershipIDLeaf(b)
	}
	lo, hi := 0, int(sl.Header.ItemCount)
	for lo < hi {
		mid := lo + (hi-lo)/2
		rec, err := probe(mid)
		if err != nil {
			return 0, format.MembershipIDLeaf{}, false, err
		}
		switch {
		case rec.MembershipID == id:
			return uint16(mid), rec, true, nil
		case rec.MembershipID < id:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return 0, format.MembershipIDLeaf{}, false, nil
}

// blobRead returns the mapped bytes of [off, off+len) inside one blob tree
// (section 10), re-validating leaf geometry at call time.
func (r *ImmutableReader) blobRead(root uint32, kind uint32, off, length uint64) ([]byte, error) {
	if length == 0 {
		return nil, corrupt("zero-length blob read")
	}
	cur := root
	level, err := r.blobLevel(cur)
	if err != nil {
		return nil, err
	}
	for {
		page, err := r.page(cur)
		if err != nil {
			return nil, err
		}
		h, err := format.DecodePageHeader(page, r.meta.TxnID)
		if err != nil {
			return nil, err
		}
		if h.Level != level {
			return nil, corrupt("blob level %d expected %d", h.Level, level)
		}
		if h.Aux != kind {
			return nil, corrupt("blob kind %d expected %d", h.Aux, kind)
		}
		switch h.PageType {
		case format.PageTypeBlobBranch:
			if level == 0 {
				return nil, corrupt("zero-level blob branch")
			}
			if h.ItemCount < 1 {
				return nil, corrupt("empty blob branch")
			}
			child, err := blobBranchChild(page, off, kind, r.meta.PageCount, r.meta.TxnID)
			if err != nil {
				return nil, err
			}
			cur, level = child, level-1
		case format.PageTypeBlobLeaf:
			if h.ItemCount != 1 {
				return nil, corrupt("blob leaf item count %d", h.ItemCount)
			}
			leaf, err := format.DecodeBlobLeaf(page)
			if err != nil {
				return nil, err
			}
			if off < leaf.LogicalOffset {
				return nil, corrupt("blob leaf offset %d above read %d", leaf.LogicalOffset, off)
			}
			base := off - leaf.LogicalOffset
			if base+length > uint64(leaf.DataLen) {
				return nil, corrupt("blob read beyond leaf")
			}
			return leaf.Data[base : base+length], nil
		default:
			return nil, corrupt("unexpected blob page type %d", h.PageType)
		}
	}
}

// blobBranchChild finds the greatest branch entry with logical_offset <= off
// by binary search over the fixed 16-byte slotted records. selectedTxn is
// the committed generation the entire traversal is bound to.
func blobBranchChild(page []byte, off uint64, kind uint32, pageCount uint64, selectedTxn uint64) (uint32, error) {
	sl, err := format.OpenSlotted(page, selectedTxn, format.PageTypeBlobBranch, kind, format.SlotItemsPerPage)
	if err != nil {
		return 0, err
	}
	probe := func(i int) (format.BlobBranchRecord, error) {
		b, err := sl.Record(i)
		if err != nil {
			return format.BlobBranchRecord{}, err
		}
		return format.DecodeBlobBranch(b)
	}
	lo, hi := 0, int(sl.Header.ItemCount)
	best := -1
	for lo < hi {
		mid := lo + (hi-lo)/2
		rec, err := probe(mid)
		if err != nil {
			return 0, err
		}
		if rec.LogicalOffset <= off {
			best = mid
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if best < 0 {
		return 0, corrupt("blob branch has no qualifying child")
	}
	rec, err := probe(best)
	if err != nil || !format.PageNumberValid(rec.Child, pageCount) {
		return 0, corrupt("blob child out of range")
	}
	return rec.Child, nil
}

func (r *ImmutableReader) blobLevel(pgno uint32) (uint16, error) {
	page, err := r.page(pgno)
	if err != nil {
		return 0, err
	}
	h, err := format.DecodePageHeader(page, r.meta.TxnID)
	if err != nil {
		return 0, err
	}
	if h.Level > format.MaxTreeLevel {
		return 0, corrupt("blob level %d over max", h.Level)
	}
	return h.Level, nil
}
