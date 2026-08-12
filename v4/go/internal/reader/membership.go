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
	dst := [1]uint64{}
	if err := v.readWordsInner(i, dst[:]); err != nil {
		return 0, false, err
	}
	return dst[0], true, nil
}

// ReadWords fills output with the sequential bitmap words starting at start
// and returns the copied count. start above word_count is InvalidArgument;
// reads that end exactly at the canonical end verify the trailing word is
// nonzero (mirroring the Rust reader).
func (v MembershipView) ReadWords(start uint32, output []uint64) (int, error) {
	if start > v.wordCount {
		return 0, &format.Error{Code: format.CodeInvalidArgument, Detail: "membership word start exceeds its length"}
	}
	remaining := v.wordCount - start
	count := int(remaining)
	if len(output) < count {
		count = len(output)
	}
	if count == 0 {
		return 0, nil
	}
	if err := v.readWordsInner(start, output[:count]); err != nil {
		return 0, err
	}
	return count, nil
}

// ContainsIndex reports whether feed_index is set in the bitmap. An index at
// or beyond the generation's feed index limit is InvalidArgument (the index
// is not an observable feed of this generation); otherwise a set bit beyond
// word_count is absent by definition.
func (v MembershipView) ContainsIndex(feedIndex uint32) (bool, error) {
	if uint64(feedIndex) >= v.r.meta.FeedIndexLimit {
		return false, &format.Error{Code: format.CodeInvalidArgument, Detail: "feed index exceeds this catalog generation"}
	}
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

// readWordsInner copies words [start, start+len(output)) through the same
// mapped path as Word, then applies the trailing-word canonical check.
func (v MembershipView) readWordsInner(start uint32, output []uint64) error {
	for i := range output {
		b, err := v.wordBytes(start + uint32(i))
		if err != nil {
			return err
		}
		output[i] = format.U64(b)
	}
	if uint64(start)+uint64(len(output)) == uint64(v.wordCount) && output[len(output)-1] == 0 {
		return corrupt("membership bitmap has a trailing zero word")
	}
	return nil
}

// wordBytes returns the 8 mapped bytes of word i, re-validated at call time.
func (v MembershipView) wordBytes(i uint32) ([]byte, error) {
	byteOff := uint64(i) * 8
	switch v.storage {
	case format.MembershipStorageInline:
		return v.inlineBytes(byteOff)
	case format.MembershipStorageBlob:
		return v.r.blobRead(v.blobRoot, format.BlobKindMembership, uint64(v.wordCount)*8, byteOff, 8)
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
			if child == 0 {
				// An absent branch key means the ID tree has no record for
				// this id; a stored range value naming it is corruption
				// (mirroring membership_view.rs).
				return MembershipView{}, corrupt("range names an absent membership ID")
			}
			cur, level = child, level-1
		case format.PageTypeMembershipIDLeaf:
			slot, leaf, found, err := membershipLeafFind(sl, id, r.meta.MembershipIDLimit, r.meta.FeedIndexLimit, r.meta.PageCount)
			if err != nil {
				return MembershipView{}, err
			}
			if !found {
				return MembershipView{}, corrupt("range names an absent membership ID")
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
		return 0, nil // no entry qualifies: absent from the ID tree
	}
	rec, err := probe(best)
	if err != nil || !format.PageNumberValid(rec.Child, pageCount) {
		return 0, corrupt("membership child out of range")
	}
	return rec.Child, nil
}

// membershipLeafFind finds the record with the exact membership ID and
// returns its slot number and decoded record. Every probed record is
// validated against the generation limits, mirroring
// membership_tree.rs require_record_fields: an id outside the namespace,
// a zero refcount, an oversized word count, or an out-of-range blob root is
// corruption.
func membershipLeafFind(sl format.SlottedPage, id uint32, idLimit, feedIndexLimit uint64, pageCount uint64) (uint16, format.MembershipIDLeaf, bool, error) {
	maxWords := feedIndexLimit / 64
	if feedIndexLimit%64 != 0 {
		maxWords++
	}
	probe := func(i int) (format.MembershipIDLeaf, error) {
		b, err := sl.Record(i)
		if err != nil {
			return format.MembershipIDLeaf{}, err
		}
		rec, err := format.DecodeMembershipIDLeaf(b)
		if err != nil {
			return format.MembershipIDLeaf{}, err
		}
		if rec.MembershipID == 0 || uint64(rec.MembershipID) >= idLimit {
			return format.MembershipIDLeaf{}, corrupt("membership ID is outside the declared namespace")
		}
		if rec.OwnerRef == 0 {
			return format.MembershipIDLeaf{}, corrupt("membership dictionary record is malformed")
		}
		if uint64(rec.WordCount) > maxWords {
			return format.MembershipIDLeaf{}, corrupt("membership word count beyond limit")
		}
		if rec.Storage == format.MembershipStorageBlob && !format.PageNumberValid(rec.BlobRoot, pageCount) {
			return format.MembershipIDLeaf{}, corrupt("membership blob root out of range")
		}
		return rec, nil
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
// (section 10), mirroring blob_tree.rs find_leaf + leaf_geometry: the walk
// verifies branch-first-offset continuity, child-level descent, leaf
// identity and geometry, 8-byte alignment, coverage of the requested span,
// and the end-vs-declared rules.
func (r *ImmutableReader) blobRead(root uint32, kind uint32, totalBytes uint64, off, length uint64) ([]byte, error) {
	if length == 0 {
		return nil, corrupt("zero-length blob read")
	}
	if off >= totalBytes {
		return nil, corrupt("membership blob request exceeds its length")
	}
	cur := root
	expectedStart := uint64(0)
	// Value pair instead of an escaping pointer: the walk must stay
	// allocation-free on the mapped hot path. haveExpected is false at the
	// root and true below a branch; expected is the parent branch level - 1.
	var expected uint16
	haveExpected := false
	for depth := 0; depth <= int(format.MaxTreeLevel); depth++ {
		page, err := r.page(cur)
		if err != nil {
			return nil, err
		}
		h, err := format.DecodePageHeader(page, r.meta.TxnID)
		if err != nil {
			return nil, err
		}
		if h.Level == 0 {
			if h.PageType != format.PageTypeBlobLeaf || h.Aux != kind {
				return nil, corrupt("blob leaf identity")
			}
			if haveExpected && expected != 0 {
				return nil, corrupt("blob leaf expected level %d", expected)
			}
			if h.ItemCount != 1 {
				return nil, corrupt("blob leaf item count %d", h.ItemCount)
			}
			leaf, err := format.DecodeBlobLeaf(page)
			if err != nil {
				return nil, err
			}
			// Fixed leaf geometry (blob_tree.rs leaf_geometry).
			if h.Lower != uint16(48+int(leaf.DataLen)) || h.Upper != format.PageSize {
				return nil, corrupt("blob leaf layout malformed")
			}
			if leaf.LogicalOffset != expectedStart || leaf.LogicalOffset%8 != 0 ||
				leaf.DataLen%8 != 0 {
				return nil, corrupt("blob leaf start or length not 8-byte aligned")
			}
			// Checked extent arithmetic: the leaf must lie inside the
			// declared blob and cover the requested span exactly.
			if leaf.LogicalOffset > totalBytes || uint64(leaf.DataLen) > totalBytes-leaf.LogicalOffset {
				return nil, corrupt("blob leaf exceeds declared length")
			}
			end := leaf.LogicalOffset + uint64(leaf.DataLen)
			if end < totalBytes && leaf.DataLen != format.MaxBlobLeafDataLen {
				return nil, corrupt("blob nonfinal leaf not full")
			}
			if off < leaf.LogicalOffset || length > end-off {
				return nil, corrupt("blob leaf does not cover the requested bytes")
			}
			base := off - leaf.LogicalOffset
			return leaf.Data[base : base+length], nil
		}
		if h.PageType != format.PageTypeBlobBranch || h.Aux != kind {
			return nil, corrupt("blob branch identity")
		}
		if haveExpected && h.Level != expected {
			return nil, corrupt("blob branch expected level %d got %d", expected, h.Level)
		}
		sl, err := format.OpenSlotted(page, r.meta.TxnID, format.PageTypeBlobBranch, kind, format.SlotItemsPerPage)
		if err != nil {
			return nil, err
		}
		first, err := sl.Record(0)
		if err != nil {
			return nil, err
		}
		firstRec, err := format.DecodeBlobBranch(first)
		if err != nil {
			return nil, err
		}
		if firstRec.LogicalOffset != expectedStart {
			return nil, corrupt("blob branch starts at a wrong offset")
		}
		child, offset, err := blobBranchChild(sl, off, r.meta.PageCount)
		if err != nil {
			return nil, err
		}
		cur = child
		expectedStart = offset
		expected = h.Level - 1
		haveExpected = true
	}
	return nil, corrupt("blob tree exceeds its maximum height")
}

// blobBranchChild finds the greatest branch entry with logical_offset <= off
// by binary search over the fixed 16-byte slotted records, returning the
// selected child and its first logical offset (the expected start of the
// next level, blob_tree.rs select_branch + find_leaf).
func blobBranchChild(sl format.SlottedPage, off uint64, pageCount uint64) (uint32, uint64, error) {
	probe := func(i int) (format.BlobBranchRecord, error) {
		b, err := sl.Record(i)
		if err != nil {
			return format.BlobBranchRecord{}, err
		}
		rec, err := format.DecodeBlobBranch(b)
		if err != nil {
			return format.BlobBranchRecord{}, err
		}
		if !format.PageNumberValid(rec.Child, pageCount) {
			return format.BlobBranchRecord{}, corrupt("blob branch child out of range")
		}
		return rec, nil
	}
	lo, hi := 0, int(sl.Header.ItemCount)
	best := -1
	for lo < hi {
		mid := lo + (hi-lo)/2
		rec, err := probe(mid)
		if err != nil {
			return 0, 0, err
		}
		if rec.LogicalOffset <= off {
			best = mid
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if best < 0 {
		return 0, 0, corrupt("blob branch has no qualifying child")
	}
	rec, err := probe(best)
	if err != nil {
		return 0, 0, err
	}
	if !format.PageNumberValid(rec.Child, pageCount) {
		return 0, 0, corrupt("blob child out of range")
	}
	return rec.Child, rec.LogicalOffset, nil
}
