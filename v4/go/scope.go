package iprangedb

import "unicode/utf8"

// The v4.1 scope table (§C.2, §D): a fixed-record B+tree keyed by scope_id mapping each
// scope to its per-scope header {version, type, name, kv_root}.
//
// On disk it is a B+tree of page_type 4 (branch) / 5 (leaf): leaves hold sorted
// scopeRecordSize-byte records; branches have the same layout as an IPv4 branch (a u32
// scope_id separator + u32 child pgno), so the existing branchView is reused with Ipv4Key
// standing in for scope_id.
//
// The writer keeps the registry in memory (a []scopeRec sorted by scope_id, loaded by
// scanning the committed tree on open) and bulk-rebuilds the tree at commit — simpler than
// incremental split/merge, and valid because tree shape is implementation-defined (§D,
// conformance is cross-read behavioral). Reads descend the committed tree in O(log scopes).
// Updating a header preserves each record's kv_root, so it never rewrites a scope's KV
// (§C.2). This mirrors the Rust reference (iprange-livedb/src/scope.rs).

// scopeRec is an owned per-scope header record (the scope-table leaf payload, §C.2). name
// holds nameLen bytes (<= scopeNameMax); typ/version/kvRoot are the seekable header fields.
// id is the B+tree key. kvRoot is 0 when the scope has no KV, else the root pgno of its
// per-scope KV tree (§C.4).
type scopeRec struct {
	id      uint32
	version uint64
	typ     uint8
	name    []byte
	kvRoot  uint32
}

// encode writes this record into a scopeRecordSize-byte slot (LE; name zero-padded).
func (r *scopeRec) encode(out []byte) {
	for i := range out {
		out[i] = 0
	}
	le.PutUint32(out[scopeRecID:], r.id)
	le.PutUint64(out[scopeRecVersion:], r.version)
	out[scopeRecType] = r.typ
	le.PutUint16(out[scopeRecNameLen:], uint16(len(r.name)))
	copy(out[scopeRecName:scopeRecName+len(r.name)], r.name)
	le.PutUint32(out[scopeRecKVRoot:], r.kvRoot)
}

// scopeLeafView is a read view over a scope-table leaf page (page_type 5): count fixed
// records.
type scopeLeafView struct {
	page  []byte
	count int
}

func newScopeLeafView(page []byte, count int) scopeLeafView {
	return scopeLeafView{page: page, count: count}
}

// id returns the scope_id of record i (the leading 4 bytes — the B+tree key).
func (l scopeLeafView) id(i int) uint32 {
	off := pageHeaderSize + i*scopeRecordSize + scopeRecID
	return le.Uint32(l.page[off:])
}

// record decodes record i into an owned scopeRec (the reader/writer validates bounds).
func (l scopeLeafView) record(i int) (scopeRec, error) {
	base := pageHeaderSize + i*scopeRecordSize
	r := l.page[base : base+scopeRecordSize]
	nameLen := int(le.Uint16(r[scopeRecNameLen:]))
	if nameLen > scopeNameMax {
		return scopeRec{}, errInvariant("scope name_len > 256")
	}
	// The name slot beyond name_len MUST be zero (§C.2 / §D tail-zero discipline).
	for _, b := range r[scopeRecName+nameLen : scopeRecName+scopeNameMax] {
		if b != 0 {
			return scopeRec{}, errNonZeroReserved("scope name padding")
		}
	}
	name := append([]byte(nil), r[scopeRecName:scopeRecName+nameLen]...)
	return scopeRec{
		id:      le.Uint32(r[scopeRecID:]),
		version: le.Uint64(r[scopeRecVersion:]),
		typ:     r[scopeRecType],
		name:    name,
		kvRoot:  le.Uint32(r[scopeRecKVRoot:]),
	}, nil
}

// bodyLen returns the byte length of the populated body (for the tail-zero check).
func (l scopeLeafView) bodyLen() int { return l.count * scopeRecordSize }

// validateAt validates record i's name_len/padding without decoding (alloc-free, §9). It
// also enforces the name is valid UTF-8 (§C.2 "name … UTF-8"), so a hostile file cannot
// deliver non-UTF-8 names via scope_name/scope_list (F3).
func (l scopeLeafView) validateAt(i int) error {
	base := pageHeaderSize + i*scopeRecordSize
	nl := int(le.Uint16(l.page[base+scopeRecNameLen:]))
	if nl > scopeNameMax {
		return errInvariant("scope name_len > 256")
	}
	nameStart := base + scopeRecName
	if !utf8.Valid(l.page[nameStart : nameStart+nl]) {
		return errInvariant("scope name not valid UTF-8")
	}
	pad := nameStart + nl
	padEnd := base + scopeRecName + scopeNameMax
	for _, b := range l.page[pad:padEnd] {
		if b != 0 {
			return errNonZeroReserved("scope name padding")
		}
	}
	return nil
}

// scopeBranchView reads a scope-table branch as an IPv4 branch view (scope_id == Ipv4Key
// key): a u32 child_pgno[0], then sepCount × (u32 scope_id separator, u32 child_pgno).
func scopeBranchView(page []byte, sepCount int) branchView[Ipv4Key] {
	return newBranchView[Ipv4Key](page, sepCount)
}

// scopePageAt returns the pgno-th page of an in-memory image (bounds already checked by the
// caller's geometry validation, like reader.pageBytes).
func scopePageAt(bytes []byte, pgno uint32) []byte {
	off := int(pgno) * pageSize
	return bytes[off : off+pageSize]
}

// validateScopeTable recursively validates the scope-table subtree (reader §9 for §D). The
// image geometry is already checked by the caller (file size, total_pages). It walks by
// page_type (no stored height): all leaves at the same depth, scope_ids globally strictly
// increasing, child pgnos in [2, total_pages), depth bounded by treeHeightMax. It never
// panics or loops on hostile-but-checksum-valid input.
func validateScopeTable(bytes []byte, rootPgno uint32, totalPages uint64, vis *pageVisitor) error {
	if rootPgno == 0 {
		return nil
	}
	if uint64(rootPgno) < 2 || uint64(rootPgno) >= totalPages {
		return errStructural("scope_table_root out of range")
	}
	var leafDepth uint32
	haveLeafDepth := false
	var prevID uint32
	havePrevID := false
	return validateScopeNode(bytes, rootPgno, 1, totalPages, vis, &leafDepth, &haveLeafDepth, &prevID, &havePrevID)
}

func validateScopeNode(bytes []byte, pgno, depth uint32, totalPages uint64, vis *pageVisitor,
	leafDepth *uint32, haveLeafDepth *bool, prevID *uint32, havePrevID *bool) error {
	if depth > treeHeightMax {
		return errInvariant("scope path deeper than TREE_HEIGHT_MAX")
	}
	// Mark this page in the file-wide visitor: a second visit (shared/duplicate page) is
	// corruption (F2). This subsumes the per-branch duplicate-child check. pgno was
	// range-checked by the caller (scope_table_root / child loop below).
	if err := vis.mark(pgno); err != nil {
		return err
	}
	page := scopePageAt(bytes, pgno)
	if !verifyPage(page) {
		return errChecksumFailed("scope page")
	}
	h := decodePageHeader(page)
	if h.reserved != 0 {
		return errNonZeroReserved("scope page header reserved")
	}
	if h.pgno != pgno {
		return errStructural("scope page self-pgno mismatch")
	}
	switch h.pageType {
	case pageTypeScopeLeaf:
		switch {
		case !*haveLeafDepth:
			*leafDepth = depth
			*haveLeafDepth = true
		case *leafDepth == depth:
		default:
			return errInvariant("scope leaves at differing depths")
		}
		count := int(h.entryCount)
		if count < 1 || count > scopeLeafMax() {
			return errInvariant("scope leaf entry_count out of range")
		}
		leaf := newScopeLeafView(page, count)
		for _, b := range page[pageHeaderSize+leaf.bodyLen():] {
			if b != 0 {
				return errNonZeroReserved("scope leaf tail")
			}
		}
		for i := 0; i < count; i++ {
			if err := leaf.validateAt(i); err != nil {
				return err
			}
			id := leaf.id(i)
			if *havePrevID && id <= *prevID {
				return errInvariant("scope ids not sorted/disjoint")
			}
			*prevID = id
			*havePrevID = true
		}
		return nil
	case pageTypeScopeBranch:
		s := int(h.entryCount)
		if s < 1 || s > scopeBranchMax() {
			return errInvariant("scope branch separator count out of range")
		}
		b := scopeBranchView(page, s)
		for _, x := range page[pageHeaderSize+b.bodyLen():] {
			if x != 0 {
				return errNonZeroReserved("scope branch tail")
			}
		}
		var prevSep uint32
		havePrevSep := false
		for i := 0; i < s; i++ {
			sep := uint32(b.sep(i))
			if havePrevSep && sep <= prevSep {
				return errInvariant("scope separators not increasing")
			}
			prevSep = sep
			havePrevSep = true
		}
		childCount := b.childCount()
		for j := 0; j < childCount; j++ {
			c := b.child(j)
			if uint64(c) < 2 || uint64(c) >= totalPages {
				return errStructural("scope child pgno out of range")
			}
		}
		for j := 0; j < childCount; j++ {
			if err := validateScopeNode(bytes, b.child(j), depth+1, totalPages, vis,
				leafDepth, haveLeafDepth, prevID, havePrevID); err != nil {
				return err
			}
		}
		return nil
	default:
		return errStructural("unexpected page_type in scope table")
	}
}

// loadAllScopes loads every scope record (in scope_id order) from a validated committed
// scope tree — the writer's in-memory registry on open. rootPgno == 0 → empty.
func loadAllScopes(bytes []byte, rootPgno uint32) ([]scopeRec, error) {
	var out []scopeRec
	if rootPgno != 0 {
		if err := loadScopeNode(bytes, rootPgno, 0, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func loadScopeNode(bytes []byte, pgno, depth uint32, out *[]scopeRec) error {
	if depth > treeHeightMax {
		return errInvariant("scope path deeper than TREE_HEIGHT_MAX")
	}
	page := scopePageAt(bytes, pgno)
	h := decodePageHeader(page)
	switch h.pageType {
	case pageTypeScopeLeaf:
		count := int(h.entryCount)
		leaf := newScopeLeafView(page, count)
		for i := 0; i < count; i++ {
			rec, err := leaf.record(i)
			if err != nil {
				return err
			}
			*out = append(*out, rec)
		}
		return nil
	case pageTypeScopeBranch:
		s := int(h.entryCount)
		b := scopeBranchView(page, s)
		for j := 0; j < b.childCount(); j++ {
			if err := loadScopeNode(bytes, b.child(j), depth+1, out); err != nil {
				return err
			}
		}
		return nil
	default:
		return errStructural("unexpected page_type in scope table")
	}
}

// findScopeByID descends a validated committed scope tree for one scope_id, returning the
// record and found=true, or found=false. rootPgno == 0 → not found. It binary-searches each
// branch (child index = number of separators <= id) and the matching leaf, O(log scopes), and
// goes through the bounds-safe views — it never panics or loops on a validated image.
func findScopeByID(b []byte, rootPgno uint32, totalPages uint64, id uint32) (scopeRec, bool, error) {
	if rootPgno == 0 {
		return scopeRec{}, false, nil
	}
	pgno := rootPgno
	// Bound the descent by treeHeightMax+1 (depths 0..=treeHeightMax), matching Rust
	// scope::find and both kv-get loops; validate rejects any path deeper than treeHeightMax.
	for depth := uint32(0); depth <= treeHeightMax; depth++ {
		// Defense-in-depth: range-check every descended pgno (mirrors Rust scope::find).
		// On a validated image this never fires, but it guarantees no OOB slice if ever
		// called on unvalidated bytes.
		if !pgnoInRange(pgno, totalPages) {
			return scopeRec{}, false, errStructural("scope child pgno out of range")
		}
		page := scopePageAt(b, pgno)
		h := decodePageHeader(page)
		count := int(h.entryCount)
		switch h.pageType {
		case pageTypeScopeLeaf:
			leaf := newScopeLeafView(page, count)
			lo, hi := 0, count
			for lo < hi {
				mid := lo + (hi-lo)/2
				if leaf.id(mid) < id {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			if lo < count && leaf.id(lo) == id {
				rec, err := leaf.record(lo)
				if err != nil {
					return scopeRec{}, false, err
				}
				return rec, true, nil
			}
			return scopeRec{}, false, nil
		case pageTypeScopeBranch:
			br := scopeBranchView(page, count)
			lo, hi := 0, count
			for lo < hi {
				mid := lo + (hi-lo)/2
				if uint32(br.sep(mid)) <= id {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			pgno = br.child(lo)
		default:
			return scopeRec{}, false, errStructural("unexpected page_type in scope table")
		}
	}
	return scopeRec{}, false, errInvariant("scope path deeper than TREE_HEIGHT_MAX")
}

// collectScopePages collects every page number in the scope tree (for freeing on rebuild
// and the allocator reachable-set walk). The tree is validated, so the walk is bounded.
func collectScopePages(bytes []byte, rootPgno uint32, out *[]uint32) {
	if rootPgno == 0 {
		return
	}
	collectScopeNode(bytes, rootPgno, 0, out)
}

func collectScopeNode(bytes []byte, pgno, depth uint32, out *[]uint32) {
	if depth > treeHeightMax {
		return
	}
	*out = append(*out, pgno)
	page := scopePageAt(bytes, pgno)
	h := decodePageHeader(page)
	if h.pageType == pageTypeScopeBranch {
		s := int(h.entryCount)
		b := scopeBranchView(page, s)
		for j := 0; j < b.childCount(); j++ {
			collectScopeNode(bytes, b.child(j), depth+1, out)
		}
	}
}

// writeScopeLeaf builds a single scope-table leaf page in page from recs (must be
// <= scopeLeafMax).
func writeScopeLeaf(page []byte, pgno uint32, recs []scopeRec) {
	for i := range page {
		page[i] = 0
	}
	writePageHeader(page, pageTypeScopeLeaf, uint16(len(recs)), pgno)
	for i := range recs {
		off := pageHeaderSize + i*scopeRecordSize
		recs[i].encode(page[off : off+scopeRecordSize])
	}
	finalizeChecksum(page)
}

// writeScopeBranch builds a single scope-table branch page (IPv4-branch layout: scope_id
// separators). len(children) MUST equal len(seps) + 1.
func writeScopeBranch(page []byte, pgno uint32, seps, children []uint32) {
	for i := range page {
		page[i] = 0
	}
	writePageHeader(page, pageTypeScopeBranch, uint16(len(seps)), pgno)
	le.PutUint32(page[pageHeaderSize:], children[0])
	for i := range seps {
		sepOff := pageHeaderSize + 4 + i*(scopeKeyWidth+4)
		le.PutUint32(page[sepOff:], seps[i])
		le.PutUint32(page[sepOff+4:], children[i+1])
	}
	finalizeChecksum(page)
}
