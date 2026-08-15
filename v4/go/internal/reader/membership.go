package reader

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Membership dictionary access (binary-format-v4.md section 9).
//
// MembershipView is a checked handle: the dictionary record is validated and
// decoded once during the lookup (mirroring membership_tree.rs, which selects
// the checked Record at lookup time), and word reads slice the retained
// inline bitmap or walk the blob tree with one decode per covering leaf. The
// handle is valid for the lifetime of the reader and allocates no heap bytes.

// MembershipView exposes one canonical membership bitmap.
type MembershipView struct {
	r         *ImmutableReader
	id        uint32
	wordCount uint32
	bitmapLen uint32
	storage   format.MembershipStorage
	blobRoot  uint32                  // blob tree root (blob storage)
	leaf      format.MembershipIDLeaf // inline record, checked at lookup
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
// word_count is absent by definition. The word is read through
// readWordsInner so a ZERO FINAL WORD is corrupt here too (spec section 9),
// exactly like Word and ReadWords and the Rust contains_index.
func (v MembershipView) ContainsIndex(feedIndex uint32) (bool, error) {
	if uint64(feedIndex) >= v.r.meta.FeedIndexLimit {
		return false, &format.Error{Code: format.CodeInvalidArgument, Detail: "feed index exceeds this catalog generation"}
	}
	word := feedIndex / 64
	if word >= v.wordCount {
		return false, nil
	}
	var words [1]uint64
	if err := v.readWordsInner(word, words[:]); err != nil {
		return false, err
	}
	return words[0]&(uint64(1)<<(feedIndex%64)) != 0, nil
}

// readWordsInner copies words [start, start+len(output)) through the same
// mapped path as Word, then applies the trailing-word canonical check.
func (v MembershipView) readWordsInner(start uint32, output []uint64) error {
	if len(output) == 0 {
		return nil
	}
	byteOff := uint64(start) * 8
	byteLen := uint64(len(output)) * 8
	switch v.storage {
	case format.MembershipStorageInline:
		// The record was validated and decoded at lookup; slice the
		// retained checked bitmap directly (bounds are re-checked so the
		// caller cannot construct an out-of-range view).
		if byteOff+byteLen > uint64(v.bitmapLen) {
			return corrupt("inline bitmap offset out of range")
		}
		data := v.leaf.Inline[byteOff : byteOff+byteLen]
		for i := range output {
			work.WordRead(1)
			output[i] = format.U64(data[i*8:])
		}
	case format.MembershipStorageBlob:
		// One blob-tree descent per covering leaf, mirroring blob_tree.rs
		// read_words_from: each descent copies min(available, remaining)
		// words from the covering leaf and advances to the next leaf, so a
		// batched read crosses leaf boundaries instead of failing on them.
		totalBytes := uint64(v.wordCount) * 8
		pos := byteOff
		written := 0
		for written < len(output) {
			leafData, start, leafErr := v.r.blobLeaf(v.blobRoot, format.BlobKindMembership, totalBytes, pos)
			if leafErr != nil {
				return leafErr
			}
			local := pos - start
			count := int((uint64(len(leafData)) - local) / 8)
			if count > len(output)-written {
				count = len(output) - written
			}
			if count == 0 {
				return corrupt("membership blob cannot advance by a complete word")
			}
			for i := 0; i < count; i++ {
				work.WordRead(1)
				output[written+i] = format.U64(leafData[local+uint64(i)*8:])
			}
			written += count
			pos += uint64(count) * 8
		}
	}
	if uint64(start)+uint64(len(output)) == uint64(v.wordCount) && output[len(output)-1] == 0 {
		return corrupt("membership bitmap has a trailing zero word")
	}
	return nil
}

// wordBytes returns the 8 mapped bytes of word i, bounds-checked against
// the checked view retained from the lookup-time record decode.
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

// inlineBytes returns the 8 mapped bytes at byteOff inside the inline bitmap
// retained from the lookup-time record decode.
func (v MembershipView) inlineBytes(byteOff uint64) ([]byte, error) {
	if byteOff+8 > uint64(v.bitmapLen) {
		return nil, corrupt("inline bitmap offset out of range")
	}
	return v.leaf.Inline[byteOff : byteOff+8], nil
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
	work.TreeLookup(1)
	cur := root
	level := uint16(0)
	first := true
	for {
		page, err := r.page(cur)
		if err != nil {
			return MembershipView{}, err
		}
		h, err := format.DecodePageHeader(page, r.meta.TxnID)
		if err != nil {
			return MembershipView{}, err
		}
		if first {
			level = h.Level // the root's own level starts the descent
			first = false
		} else if h.Level != level {
			return MembershipView{}, corrupt("membership level %d expected %d", h.Level, level)
		}
		sl, err := format.OpenSlottedHeader(page, h, h.PageType, 0, format.SlotItemsPerPage)
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
			work.TreeDescent(1)
			cur, level = child, level-1
		case format.PageTypeMembershipIDLeaf:
			_, leaf, found, err := membershipLeafFind(sl, id, r.meta.MembershipIDLimit, r.meta.FeedIndexLimit, r.meta.PageCount)
			if err != nil {
				return MembershipView{}, err
			}
			if !found {
				return MembershipView{}, corrupt("range names an absent membership ID")
			}
			return MembershipView{
				r:         r,
				id:        leaf.MembershipID,
				wordCount: leaf.WordCount,
				bitmapLen: leaf.BitmapLen,
				storage:   leaf.Storage,
				blobRoot:  leaf.BlobRoot,
				leaf:      leaf,
			}, nil
		default:
			return MembershipView{}, corrupt("unexpected membership page type %d", h.PageType)
		}
	}
}

// membershipBranchChild finds the greatest branch entry with first_id <= id.
// Probes read only the first_id key; the selected entry is decoded once and
// its child validated.
func membershipBranchChild(sl format.SlottedPage, id uint32, pageCount uint64) (uint32, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		first, err := format.MembershipIDBranchKey(b)
		if err != nil {
			return 0, err
		}
		return cmpU32(first, id), nil
	}
	best, err := greatestLE(int(sl.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return 0, err
	}
	b, err := sl.Record(best)
	if err != nil {
		return 0, err
	}
	rec, err := format.DecodeMembershipIDBranch(b)
	if err != nil || !format.PageNumberValid(rec.Child, pageCount) {
		return 0, corrupt("membership child out of range")
	}
	return rec.Child, nil
}

// membershipLeafFind finds the record with the exact membership ID and
// returns its slot number and decoded record. Probes read the id key only
// (mirroring membership_dictionary/codec.rs read_key at level 0); the
// selected record alone is decoded and validated against the generation
// limits (mirroring membership_tree.rs require_record_fields: an id
// outside the namespace, a zero refcount, an oversized word count, or an
// out-of-range blob root is corruption). A miss never decodes a record.
func membershipLeafFind(sl format.SlottedPage, id uint32, idLimit, feedIndexLimit uint64, pageCount uint64) (uint16, format.MembershipIDLeaf, bool, error) {
	maxWords := feedIndexLimit / 64
	if feedIndexLimit%64 != 0 {
		maxWords++
	}
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		key, err := format.MembershipIDLeafKey(b)
		if err != nil {
			return 0, err
		}
		return cmpU32(key, id), nil
	}
	best, err := greatestLE(int(sl.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return 0, format.MembershipIDLeaf{}, false, err
	}
	b, err := sl.Record(best)
	if err != nil {
		return 0, format.MembershipIDLeaf{}, false, err
	}
	key, err := format.MembershipIDLeafKey(b)
	if err != nil {
		return 0, format.MembershipIDLeaf{}, false, err
	}
	if key != id {
		return 0, format.MembershipIDLeaf{}, false, nil // clean miss, no decode
	}
	work.LeafValidation(1)
	rec, err := format.DecodeMembershipIDLeaf(b)
	if err != nil {
		return 0, format.MembershipIDLeaf{}, false, err
	}
	if rec.MembershipID == 0 || uint64(rec.MembershipID) >= idLimit {
		return 0, format.MembershipIDLeaf{}, false, corrupt("membership ID is outside the declared namespace")
	}
	if rec.OwnerRef == 0 {
		return 0, format.MembershipIDLeaf{}, false, corrupt("membership dictionary record is malformed")
	}
	if uint64(rec.WordCount) > maxWords {
		return 0, format.MembershipIDLeaf{}, false, corrupt("membership word count beyond limit")
	}
	if rec.Storage == format.MembershipStorageBlob && !format.PageNumberValid(rec.BlobRoot, pageCount) {
		return 0, format.MembershipIDLeaf{}, false, corrupt("membership blob root out of range")
	}
	return uint16(best), rec, true, nil
}

// blobRead returns the mapped bytes of [off, off+len) inside one blob tree
// (section 10), mirroring blob_tree.rs find_leaf + leaf_geometry: the walk
// verifies branch-first-offset continuity, child-level descent, leaf
// identity and geometry, 8-byte alignment, coverage of the requested span,
// and the end-vs-declared rules. A span crossing a leaf boundary is
// corruption here; batched readers loop over blobLeaf instead.
func (r *ImmutableReader) blobRead(root uint32, kind uint32, totalBytes uint64, off, length uint64) ([]byte, error) {
	if length == 0 {
		return nil, corrupt("zero-length blob read")
	}
	data, start, err := r.blobLeaf(root, kind, totalBytes, off)
	if err != nil {
		return nil, err
	}
	end := start + uint64(len(data))
	if length > end-off {
		return nil, corrupt("blob leaf does not cover the requested bytes")
	}
	base := off - start
	return data[base : base+length], nil
}

// blobLeaf walks one blob tree to the leaf covering off and returns its
// mapped data and the leaf's logical start offset (blob_tree.rs find_leaf):
// the traversal verifies branch-first-offset continuity, child-level
// descent, leaf identity and geometry, 8-byte alignment, and the
// end-vs-declared rules before the caller touches any byte.
func (r *ImmutableReader) blobLeaf(root uint32, kind uint32, totalBytes uint64, off uint64) ([]byte, uint64, error) {
	if off >= totalBytes {
		return nil, 0, corrupt("membership blob request exceeds its length")
	}
	work.TreeLookup(1)
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
			return nil, 0, err
		}
		h, err := format.DecodePageHeader(page, r.meta.TxnID)
		if err != nil {
			return nil, 0, err
		}
		if h.Level == 0 {
			if h.PageType != format.PageTypeBlobLeaf || h.Aux != kind {
				return nil, 0, corrupt("blob leaf identity")
			}
			if haveExpected && expected != 0 {
				return nil, 0, corrupt("blob leaf expected level %d", expected)
			}
			if h.ItemCount != 1 {
				return nil, 0, corrupt("blob leaf item count %d", h.ItemCount)
			}
			work.LeafValidation(1)
			leaf, err := format.DecodeBlobLeaf(page)
			if err != nil {
				return nil, 0, err
			}
			// Fixed leaf geometry (blob_tree.rs leaf_geometry).
			if h.Lower != uint16(48+int(leaf.DataLen)) || h.Upper != format.PageSize {
				return nil, 0, corrupt("blob leaf layout malformed")
			}
			if leaf.LogicalOffset != expectedStart || leaf.LogicalOffset%8 != 0 ||
				leaf.DataLen%8 != 0 {
				return nil, 0, corrupt("blob leaf start or length not 8-byte aligned")
			}
			// Checked extent arithmetic: the leaf must lie inside the
			// declared blob and cover the requested span exactly.
			if leaf.LogicalOffset > totalBytes || uint64(leaf.DataLen) > totalBytes-leaf.LogicalOffset {
				return nil, 0, corrupt("blob leaf exceeds declared length")
			}
			end := leaf.LogicalOffset + uint64(leaf.DataLen)
			if end < totalBytes && leaf.DataLen != format.MaxBlobLeafDataLen {
				return nil, 0, corrupt("blob nonfinal leaf not full")
			}
			// The request must lie inside [leaf.LogicalOffset, end]; the
			// explicit off > end guard keeps the end-off subtraction free of
			// unsigned underflow (a blob-tree gap must be corruption, never
			// an out-of-leaf read or a slice panic).
			if off < leaf.LogicalOffset || off > end {
				return nil, 0, corrupt("blob leaf does not cover the requested bytes")
			}
			return leaf.Data, leaf.LogicalOffset, nil
		}
		if h.PageType != format.PageTypeBlobBranch || h.Aux != kind {
			return nil, 0, corrupt("blob branch identity")
		}
		if haveExpected && h.Level != expected {
			return nil, 0, corrupt("blob branch expected level %d got %d", expected, h.Level)
		}
		sl, err := format.OpenSlottedHeader(page, h, format.PageTypeBlobBranch, kind, format.SlotItemsPerPage)
		if err != nil {
			return nil, 0, err
		}
		first, err := sl.Record(0)
		if err != nil {
			return nil, 0, err
		}
		firstRec, err := format.DecodeBlobBranch(first)
		if err != nil {
			return nil, 0, err
		}
		if firstRec.LogicalOffset != expectedStart {
			return nil, 0, corrupt("blob branch starts at a wrong offset")
		}
		child, offset, err := blobBranchChild(sl, off, r.meta.PageCount)
		if err != nil {
			return nil, 0, err
		}
		work.TreeDescent(1)
		cur = child
		expectedStart = offset
		expected = h.Level - 1
		haveExpected = true
	}
	return nil, 0, corrupt("blob tree exceeds its maximum height")
}

// blobBranchChild finds the greatest branch entry with logical_offset <= off
// over the fixed 16-byte slotted records, returning the selected child and
// its first logical offset (the expected start of the next level,
// blob_tree.rs select_branch + find_leaf). Every probed record is decoded
// and its child validated: a malformed probe aborts the walk (pinned by
// TestBlobBranchProbedChildValidation; Rust branch_record validates every
// probed record here too, unlike the fixed key-only trees).
func blobBranchChild(sl format.SlottedPage, off uint64, pageCount uint64) (uint32, uint64, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		rec, err := format.DecodeBlobBranch(b)
		if err != nil {
			return 0, err
		}
		if !format.PageNumberValid(rec.Child, pageCount) {
			return 0, corrupt("blob branch child out of range")
		}
		return cmpU64(rec.LogicalOffset, off), nil
	}
	best, err := greatestLE(int(sl.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return 0, 0, err
	}
	b, err := sl.Record(best)
	if err != nil {
		return 0, 0, err
	}
	rec, err := format.DecodeBlobBranch(b)
	if err != nil {
		return 0, 0, err
	}
	if !format.PageNumberValid(rec.Child, pageCount) {
		return 0, 0, corrupt("blob child out of range")
	}
	return rec.Child, rec.LogicalOffset, nil
}
