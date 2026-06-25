package iprangedb

import (
	"bytes"
	"unicode/utf8"
)

// The v4.1 per-scope KV store (§C.4, §D): a bulk-loaded B+tree behind each scope's kv_root,
// mapping a UTF-8 key to (type, value). Unlike the IP tree and the scope table (both
// fixed-record), KV pages are slot-directory pages with variable-length entries — a separate
// layout this module builds from scratch.
//
// On disk it is a B+tree of page_type 6 (branch) / 7 (leaf) with values inlined when small
// and chained through page_type 8 overflow pages when large. Each leaf/branch is a slot
// directory: a u16 slot array grows from the front (byte offsets into the page), the entry
// heap grows from the back, entries sorted by key.
//
// The writer buffers a scope's KV in memory and bulk-rebuilds the tree at commit (§C.4:
// full-rewrite per scope per commit, no incremental split/merge), so this module exposes
// page encoders and a measurer the writer drives, plus the read path (get/list), recursive
// validate, and collectKVPages — mirroring scope.go and the Rust reference (kv.rs).
//
// Tree shape (fanout, inline-vs-overflow threshold, bulk-load order) is implementation-
// defined; the page/entry encoding here is normative (§D) so the two impls cross-read these
// pages. value_kind makes inline-vs-overflow self-describing, so a reader parses either
// regardless of the writer's threshold.

// kvEntry is an owned KV entry, as buffered by the writer and returned by reads. value is the
// whole reassembled value (inline or overflow-spanning), opaque to the engine except for the
// type == 0 UTF-8 check.
type kvEntry struct {
	key   []byte
	typ   uint32
	value []byte
}

// checkKey validates a KV key per §C.4: 1..=1024 bytes, valid UTF-8, no NUL. InvalidInput on
// any violation (caller-facing).
func checkKey(key []byte) error {
	if len(key) < kvKeyMin || len(key) > kvKeyMax {
		return errInvalidInput("kv key length out of range (1..=1024)")
	}
	if bytes.IndexByte(key, 0) >= 0 {
		return errInvalidInput("kv key contains NUL")
	}
	if !utf8.Valid(key) {
		return errInvalidInput("kv key not valid UTF-8")
	}
	return nil
}

// checkTextValue validates a type == 0 value is text (valid UTF-8, no NUL), per §C.4. A no-op
// for any non-zero type (caller-defined binary). InvalidInput on a bad text value.
func checkTextValue(typ uint32, value []byte) error {
	if typ != kvTypeText {
		return nil
	}
	if bytes.IndexByte(value, 0) >= 0 {
		return errInvalidInput("kv text value contains NUL")
	}
	if !utf8.Valid(value) {
		return errInvalidInput("kv text value not valid UTF-8")
	}
	return nil
}

// kvPageAt returns the pgno-th page of an in-memory image (bounds already checked by the
// caller's geometry validation, like scopePageAt).
func kvPageAt(b []byte, pgno uint32) []byte {
	off := int(pgno) * pageSize
	return b[off : off+pageSize]
}

// pgnoInRange reports whether pgno is a valid non-meta page index: [2, total_pages).
func pgnoInRange(pgno uint32, totalPages uint64) bool {
	return uint64(pgno) >= 2 && uint64(pgno) < totalPages
}

// --- slot-directory views over an already-bounds-checked page (§D) ---

// kvLeafView is a read view over a KV leaf (page_type 7): count slots, each a u16 byte offset
// into the entry heap. Accessors return an error because offsets and lengths come from
// untrusted bytes; every read is range-checked against the page.
type kvLeafView struct {
	page  []byte
	count int
}

// leafEntryHdr is one parsed KV leaf entry header (the value is fetched separately by
// descriptor). inlineOK reports an inline value (inline holds it); else overflow holds
// (first_pgno, value_total_len).
type leafEntryHdr struct {
	key      []byte
	typ      uint32
	inlineOK bool
	inline   []byte
	first    uint32
	total    uint64
}

func newKVLeafView(page []byte, count int) kvLeafView {
	return kvLeafView{page: page, count: count}
}

// slot returns the byte offset of slot i (the entry start within the page). Range-checked.
func (l kvLeafView) slot(i int) (int, error) {
	so := pageHeaderSize + i*kvSlotSize
	// The slot directory must lie within the page body.
	if so+kvSlotSize > pageSize {
		return 0, errStructural("kv leaf slot out of page")
	}
	off := int(le.Uint16(l.page[so:]))
	// An entry must start after the slot directory and within the page.
	if off < pageHeaderSize+l.count*kvSlotSize || off >= pageSize {
		return 0, errStructural("kv leaf entry offset out of bounds")
	}
	return off, nil
}

// entryHdr parses entry i's header (key + type + value descriptor), fully range-checked.
func (l kvLeafView) entryHdr(i int) (leafEntryHdr, error) {
	off, err := l.slot(i)
	if err != nil {
		return leafEntryHdr{}, err
	}
	keyLen, err := readU16(l.page, &off)
	if err != nil {
		return leafEntryHdr{}, err
	}
	if int(keyLen) < kvKeyMin || int(keyLen) > kvKeyMax {
		return leafEntryHdr{}, errInvariant("kv key_len out of range")
	}
	key, err := readBytes(l.page, &off, int(keyLen))
	if err != nil {
		return leafEntryHdr{}, err
	}
	typ, err := readU32(l.page, &off)
	if err != nil {
		return leafEntryHdr{}, err
	}
	kind, err := readU8(l.page, &off)
	if err != nil {
		return leafEntryHdr{}, err
	}
	switch kind {
	case kvValueInline:
		vlen, err := readU32(l.page, &off)
		if err != nil {
			return leafEntryHdr{}, err
		}
		value, err := readBytes(l.page, &off, int(vlen))
		if err != nil {
			return leafEntryHdr{}, err
		}
		return leafEntryHdr{key: key, typ: typ, inlineOK: true, inline: value}, nil
	case kvValueOverflow:
		first, err := readU32(l.page, &off)
		if err != nil {
			return leafEntryHdr{}, err
		}
		total, err := readU64(l.page, &off)
		if err != nil {
			return leafEntryHdr{}, err
		}
		return leafEntryHdr{key: key, typ: typ, first: first, total: total}, nil
	default:
		return leafEntryHdr{}, errStructural("kv leaf unknown value_kind")
	}
}

// key returns the key of entry i (for ordered descent/scan). Range-checked.
func (l kvLeafView) key(i int) ([]byte, error) {
	h, err := l.entryHdr(i)
	if err != nil {
		return nil, err
	}
	return h.key, nil
}

// kvBranchView is a read view over a KV branch (page_type 6): a leftmost child pgno followed
// by count separators, each (sep_key, child_pgno), in a slot directory.
type kvBranchView struct {
	page  []byte
	count int
}

func newKVBranchView(page []byte, count int) kvBranchView {
	return kvBranchView{page: page, count: count}
}

// leftmost returns the leftmost child pgno (precedes every separator), in the 4 bytes
// immediately after the page header, before the slot directory (a fixed, non-slotted field —
// like the IP branch).
func (b kvBranchView) leftmost() uint32 {
	return le.Uint32(b.page[pageHeaderSize:])
}

// slot returns the byte offset of separator slot i. Range-checked.
func (b kvBranchView) slot(i int) (int, error) {
	so := kvBranchDirStart + i*kvSlotSize
	if so+kvSlotSize > pageSize {
		return 0, errStructural("kv branch slot out of page")
	}
	off := int(le.Uint16(b.page[so:]))
	if off < kvBranchDirStart+b.count*kvSlotSize || off >= pageSize {
		return 0, errStructural("kv branch entry offset out of bounds")
	}
	return off, nil
}

// sep returns separator i: (sep_key, child_pgno). Range-checked.
func (b kvBranchView) sep(i int) ([]byte, uint32, error) {
	off, err := b.slot(i)
	if err != nil {
		return nil, 0, err
	}
	sepLen, err := readU16(b.page, &off)
	if err != nil {
		return nil, 0, err
	}
	if int(sepLen) < kvKeyMin || int(sepLen) > kvKeyMax {
		return nil, 0, errInvariant("kv sep_len out of range")
	}
	key, err := readBytes(b.page, &off, int(sepLen))
	if err != nil {
		return nil, 0, err
	}
	child, err := readU32(b.page, &off)
	if err != nil {
		return nil, 0, err
	}
	return key, child, nil
}

// child returns the child pgno for descent index j (0 = leftmost, j>=1 follows sep[j-1]).
func (b kvBranchView) child(j int) (uint32, error) {
	if j == 0 {
		return b.leftmost(), nil
	}
	_, c, err := b.sep(j - 1)
	return c, err
}

// --- bounds-safe cursor reads over a single page ---

func readU8(page []byte, off *int) (uint8, error) {
	if *off+1 > pageSize {
		return 0, errStructural("kv entry read past page")
	}
	v := page[*off]
	*off++
	return v, nil
}

func readU16(page []byte, off *int) (uint16, error) {
	if *off+2 > pageSize {
		return 0, errStructural("kv entry read past page")
	}
	v := le.Uint16(page[*off:])
	*off += 2
	return v, nil
}

func readU32(page []byte, off *int) (uint32, error) {
	if *off+4 > pageSize {
		return 0, errStructural("kv entry read past page")
	}
	v := le.Uint32(page[*off:])
	*off += 4
	return v, nil
}

func readU64(page []byte, off *int) (uint64, error) {
	if *off+8 > pageSize {
		return 0, errStructural("kv entry read past page")
	}
	v := le.Uint64(page[*off:])
	*off += 8
	return v, nil
}

func readBytes(page []byte, off *int, n int) ([]byte, error) {
	if n < 0 || *off+n > pageSize {
		return nil, errStructural("kv entry read past page")
	}
	s := page[*off : *off+n]
	*off += n
	return s, nil
}

// --- overflow chains (read by count, never to a terminator; §C.5) ---

// pageVisitor tracks which metadata pages a single validate walk has reached, so the whole
// page-forest (scope table + every per-scope KV tree + every overflow chain) is proven
// disjoint and acyclic (F2). visited is sized total_pages and shared across the entire walk;
// the first visit marks a page, a second visit to any page is a structural error. On the
// read path (no walk) it is nil, and readOverflow falls back to a per-call within-chain set.
type pageVisitor struct {
	visited []bool
}

// mark records a visit to pgno (already range-checked by the caller), returning a structural
// error if the page was already reached anywhere in this walk. Nil receiver is a no-op (read
// path). It subsumes the per-node duplicate-child check and the per-chain revisit check.
func (v *pageVisitor) mark(pgno uint32) error {
	if v == nil || v.visited == nil {
		return nil
	}
	if v.visited[pgno] {
		return errStructural("metadata page reached twice (shared/duplicate page)")
	}
	v.visited[pgno] = true
	return nil
}

// readOverflow reassembles an overflow value: concatenate payloads along the chain from
// first, truncated to total. It reads exactly ceil(total/payload) pages; a revisit, cycle,
// length mismatch, bad pgno, or page-type/self-pgno error → a corruption error. It never
// loops to a terminator (§C.5). vis is the file-wide visitor on the validate path (so a chain
// page shared with any other entry/chain is rejected); nil on the read path, where a local
// O(1) set guards only within this chain (F2/F4).
func readOverflow(b []byte, first uint32, total uint64, totalPages uint64, vis *pageVisitor) ([]byte, error) {
	if total == 0 {
		// A zero-length value uses no overflow pages (the writer stores it inline), so a
		// chain claiming total == 0 is malformed.
		return nil, errStructural("kv overflow chain for empty value")
	}
	payload := uint64(overflowPayload)
	// div_ceil computed WITHOUT `total + payload - 1` (that addition wraps for `total` near
	// uint64 max → a tiny wantPages that slips past the cap below and returns a truncated/empty
	// value, i.e. a wrong answer on a checksum-valid file). This form cannot overflow.
	wantPages := total / payload
	if total%payload != 0 {
		wantPages++
	}
	// Cap the page budget: a chain longer than the file cannot be valid. This also makes the
	// out capacity safe — total can never exceed wantPages*payload <= totalPages*payload.
	if wantPages > totalPages {
		return nil, errStructural("kv overflow chain longer than file")
	}
	out := make([]byte, 0, total)
	pgno := first
	// Revisit/cycle defense (O(1) per page, F4): on the validate path the file-wide visitor
	// catches both cross-entry sharing and within-chain cycles; on the read path a small local
	// map bounded by the chain length catches within-chain cycles.
	var local map[uint32]struct{}
	if vis == nil || vis.visited == nil {
		local = make(map[uint32]struct{}, wantPages)
	}
	for step := uint64(0); step < wantPages; step++ {
		if !pgnoInRange(pgno, totalPages) {
			return nil, errStructural("kv overflow pgno out of range")
		}
		if local != nil {
			if _, dup := local[pgno]; dup {
				return nil, errStructural("kv overflow chain revisits a page")
			}
			local[pgno] = struct{}{}
		} else if err := vis.mark(pgno); err != nil {
			return nil, err
		}
		page := kvPageAt(b, pgno)
		if !verifyPage(page) {
			return nil, errChecksumFailed("kv overflow page")
		}
		h := decodePageHeader(page)
		if h.pageType != pageTypeOverflow {
			return nil, errStructural("kv overflow wrong page_type")
		}
		if h.reserved != 0 {
			return nil, errNonZeroReserved("kv overflow header reserved")
		}
		if h.pgno != pgno {
			return nil, errStructural("kv overflow self-pgno mismatch")
		}
		next := le.Uint32(page[overflowNextPgno:])
		remaining := total - step*payload
		take := remaining
		if take > payload {
			take = payload
		}
		body := pageHeaderSize + 4
		out = append(out, page[body:body+int(take)]...)
		if step+1 == wantPages {
			// The last page MUST terminate the chain (next == 0) and its unused payload tail
			// MUST be zero — read-by-count means a non-zero next is corruption.
			if next != 0 {
				return nil, errStructural("kv overflow chain longer than length")
			}
			for _, x := range page[body+int(take):] {
				if x != 0 {
					return nil, errNonZeroReserved("kv overflow last-page tail")
				}
			}
		} else {
			if next == 0 {
				return nil, errStructural("kv overflow chain shorter than length")
			}
			pgno = next
		}
	}
	return out, nil
}

// validateOverflowValue is the streaming structural walk of an overflow chain for the validate
// path (§C.5): the same checks as readOverflow (pgno range, revisit/cycle via the shared
// visitor, per-page CRC, page_type, self-pgno, reserved, read-by-count budget, last-page
// next==0 + zero tail) but it does NOT materialize the value — peak extra memory is O(1), not
// O(value). When text (type 0), each page payload chunk is checked for NUL and fed to an
// incremental UTF-8 validator (a multibyte char may straddle a page boundary).
func validateOverflowValue(b []byte, first uint32, total uint64, totalPages uint64, vis *pageVisitor, text bool) error {
	if total == 0 {
		return errStructural("kv overflow chain for empty value")
	}
	payload := uint64(overflowPayload)
	wantPages := total / payload
	if total%payload != 0 {
		wantPages++
	}
	if wantPages > totalPages {
		return errStructural("kv overflow chain longer than file")
	}
	pgno := first
	var u utf8Stream
	for step := uint64(0); step < wantPages; step++ {
		if !pgnoInRange(pgno, totalPages) {
			return errStructural("kv overflow pgno out of range")
		}
		if err := vis.mark(pgno); err != nil {
			return err
		}
		page := kvPageAt(b, pgno)
		if !verifyPage(page) {
			return errChecksumFailed("kv overflow page")
		}
		h := decodePageHeader(page)
		if h.pageType != pageTypeOverflow {
			return errStructural("kv overflow wrong page_type")
		}
		if h.reserved != 0 {
			return errNonZeroReserved("kv overflow header reserved")
		}
		if h.pgno != pgno {
			return errStructural("kv overflow self-pgno mismatch")
		}
		next := le.Uint32(page[overflowNextPgno:])
		remaining := total - step*payload
		take := remaining
		if take > payload {
			take = payload
		}
		body := pageHeaderSize + 4
		if text {
			chunk := page[body : body+int(take)]
			if bytes.IndexByte(chunk, 0) >= 0 {
				return errInvariant("kv text value contains NUL")
			}
			if err := u.feed(chunk); err != nil {
				return err
			}
		}
		if step+1 == wantPages {
			if next != 0 {
				return errStructural("kv overflow chain longer than length")
			}
			for _, x := range page[body+int(take):] {
				if x != 0 {
					return errNonZeroReserved("kv overflow last-page tail")
				}
			}
		} else {
			if next == 0 {
				return errStructural("kv overflow chain shorter than length")
			}
			pgno = next
		}
	}
	if text {
		if err := u.finish(); err != nil {
			return err
		}
	}
	return nil
}

// utf8Stream is an incremental UTF-8 validator (no allocation): feed payload chunks in order.
// A multibyte sequence may straddle a chunk boundary, so up to 3 trailing bytes of an
// incomplete-but-valid prefix are carried; finish rejects a value ending mid-sequence.
// Equivalent accept/reject to utf8.Valid over the whole value.
type utf8Stream struct {
	carry [3]byte
	n     int
}

func (u *utf8Stream) feed(chunk []byte) error {
	data := chunk
	if u.n > 0 {
		// Complete the carried partial char from the front of this chunk. carry[0] is a valid
		// multibyte lead (the prior tail was incomplete-but-valid), so its length is known; a
		// char is ≤4 bytes, spanning at most two chunks.
		need := utf8SeqLen(u.carry[0])
		if need == 0 {
			return errInvariant("kv text value not valid UTF-8")
		}
		take := need - u.n
		if take > len(data) {
			take = len(data)
		}
		var buf [4]byte
		m := copy(buf[:], u.carry[:u.n])
		m += copy(buf[m:need], data[:take])
		if m < need {
			// This chunk was shorter than the missing bytes (a tiny last page); still partial.
			copy(u.carry[:], buf[:m])
			u.n = m
			return nil
		}
		if !utf8.Valid(buf[:need]) {
			return errInvariant("kv text value not valid UTF-8")
		}
		u.n = 0
		data = data[take:]
	}
	i := validUTF8Prefix(data)
	if i < len(data) {
		rest := data[i:]
		// rest[0] is the first non-validating byte: a full (but invalid) rune ⇒ reject; an
		// incomplete-but-valid prefix (≤3 bytes) ⇒ carry to the next chunk.
		if utf8.FullRune(rest) || len(rest) > 3 {
			return errInvariant("kv text value not valid UTF-8")
		}
		copy(u.carry[:], rest)
		u.n = len(rest)
	}
	return nil
}

func (u *utf8Stream) finish() error {
	if u.n != 0 {
		return errInvariant("kv text value not valid UTF-8")
	}
	return nil
}

// utf8SeqLen returns the expected length (2..=4) of a UTF-8 sequence from its lead byte, or 0
// for a non-lead / invalid lead.
func utf8SeqLen(lead byte) int {
	switch {
	case lead&0xE0 == 0xC0:
		return 2
	case lead&0xF0 == 0xE0:
		return 3
	case lead&0xF8 == 0xF0:
		return 4
	default:
		return 0
	}
}

// validUTF8Prefix returns the length of the longest prefix of b that is valid UTF-8.
func validUTF8Prefix(b []byte) int {
	i := 0
	for i < len(b) {
		if b[i] < utf8.RuneSelf {
			i++
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			break // invalid or incomplete at i
		}
		i += size
	}
	return i
}

// --- read path: get / list ---

// kvGet looks up key in the KV tree rooted at rootPgno (already validated). It returns the
// reassembled (type, value) and found=true, or found=false. rootPgno == 0 → not found.
func kvGet(b []byte, rootPgno uint32, key []byte, totalPages uint64) (typ uint32, value []byte, found bool, err error) {
	if rootPgno == 0 {
		return 0, nil, false, nil
	}
	pgno := rootPgno
	for d := uint32(0); d <= treeHeightMax; d++ {
		if !pgnoInRange(pgno, totalPages) {
			return 0, nil, false, errStructural("kv child pgno out of range")
		}
		page := kvPageAt(b, pgno)
		h := decodePageHeader(page)
		count := int(h.entryCount)
		switch h.pageType {
		case pageTypeKVLeaf:
			leaf := newKVLeafView(page, count)
			// Binary search the sorted slots.
			lo, hi := 0, count
			for lo < hi {
				mid := lo + (hi-lo)/2
				mk, e := leaf.key(mid)
				if e != nil {
					return 0, nil, false, e
				}
				if bytes.Compare(mk, key) < 0 {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			if lo < count {
				hdr, e := leaf.entryHdr(lo)
				if e != nil {
					return 0, nil, false, e
				}
				if bytes.Equal(hdr.key, key) {
					v, e := materializeValue(b, hdr, totalPages, nil)
					if e != nil {
						return 0, nil, false, e
					}
					return hdr.typ, v, true, nil
				}
			}
			return 0, nil, false, nil
		case pageTypeKVBranch:
			br := newKVBranchView(page, count)
			// child index = number of separators with sep_key <= key.
			lo, hi := 0, count
			for lo < hi {
				mid := lo + (hi-lo)/2
				sk, _, e := br.sep(mid)
				if e != nil {
					return 0, nil, false, e
				}
				if bytes.Compare(sk, key) <= 0 {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			c, e := br.child(lo)
			if e != nil {
				return 0, nil, false, e
			}
			pgno = c
		default:
			return 0, nil, false, errStructural("kv unexpected page_type")
		}
	}
	return 0, nil, false, errInvariant("kv path deeper than TREE_HEIGHT_MAX")
}

// materializeValue returns the whole value for an entry header (inline copy or overflow
// reassembly). vis is the file-wide visitor on the validate path; nil on the read path.
func materializeValue(b []byte, hdr leafEntryHdr, totalPages uint64, vis *pageVisitor) ([]byte, error) {
	if hdr.inlineOK {
		return cloneBytes(hdr.inline), nil
	}
	return readOverflow(b, hdr.first, hdr.total, totalPages, vis)
}

// kvList appends every entry in the KV tree (in key order) to out as full kvEntrys (overflow
// values reassembled). The tree is validated, so the walk is bounded.
func kvList(b []byte, rootPgno uint32, totalPages uint64, out *[]kvEntry) error {
	if rootPgno == 0 {
		return nil
	}
	return kvListNode(b, rootPgno, 0, totalPages, out)
}

func kvListNode(b []byte, pgno, depth uint32, totalPages uint64, out *[]kvEntry) error {
	if depth > treeHeightMax {
		return errInvariant("kv path deeper than TREE_HEIGHT_MAX")
	}
	page := kvPageAt(b, pgno)
	h := decodePageHeader(page)
	count := int(h.entryCount)
	switch h.pageType {
	case pageTypeKVLeaf:
		leaf := newKVLeafView(page, count)
		for i := 0; i < count; i++ {
			hdr, err := leaf.entryHdr(i)
			if err != nil {
				return err
			}
			value, err := materializeValue(b, hdr, totalPages, nil)
			if err != nil {
				return err
			}
			*out = append(*out, kvEntry{key: cloneBytes(hdr.key), typ: hdr.typ, value: value})
		}
		return nil
	case pageTypeKVBranch:
		br := newKVBranchView(page, count)
		for j := 0; j <= count; j++ {
			c, err := br.child(j)
			if err != nil {
				return err
			}
			if !pgnoInRange(c, totalPages) {
				return errStructural("kv child pgno out of range")
			}
			if err := kvListNode(b, c, depth+1, totalPages, out); err != nil {
				return err
			}
		}
		return nil
	default:
		return errStructural("kv unexpected page_type")
	}
}

// --- allocator support: collect every page reachable from a kv_root ---

// collectKVPages collects every page number in the KV tree AND its overflow chains (for
// freeing on rebuild and for the allocator reachable-set walk). The tree is validated, so the
// walk is bounded. Best-effort on a not-yet-validated tree (used only on validated images).
func collectKVPages(b []byte, rootPgno uint32, totalPages uint64, out *[]uint32) {
	if rootPgno == 0 {
		return
	}
	collectKVNode(b, rootPgno, 0, totalPages, out)
}

func collectKVNode(b []byte, pgno, depth uint32, totalPages uint64, out *[]uint32) {
	if depth > treeHeightMax || !pgnoInRange(pgno, totalPages) {
		return
	}
	*out = append(*out, pgno)
	page := kvPageAt(b, pgno)
	h := decodePageHeader(page)
	count := int(h.entryCount)
	switch h.pageType {
	case pageTypeKVLeaf:
		leaf := newKVLeafView(page, count)
		for i := 0; i < count; i++ {
			if hdr, err := leaf.entryHdr(i); err == nil && !hdr.inlineOK {
				collectOverflow(b, hdr.first, hdr.total, totalPages, out)
			}
		}
	case pageTypeKVBranch:
		br := newKVBranchView(page, count)
		for j := 0; j <= count; j++ {
			if c, err := br.child(j); err == nil {
				collectKVNode(b, c, depth+1, totalPages, out)
			}
		}
	}
}

// collectOverflow collects the overflow chain pages from first, bounded by the computed page
// count and an O(1) within-chain revisit guard (no terminator-driven loop; §C.5). On a
// validated tree chains are disjoint, so a local within-chain set suffices (F4: no O(n²) scan
// of the whole accumulator).
func collectOverflow(b []byte, first uint32, total uint64, totalPages uint64, out *[]uint32) {
	if total == 0 {
		return
	}
	payload := uint64(overflowPayload)
	// Overflow-safe div_ceil (see readOverflow): `total + payload - 1` would wrap for large total.
	wantPages := total / payload
	if total%payload != 0 {
		wantPages++
	}
	pgno := first
	seen := make(map[uint32]struct{}, wantPages)
	for step := uint64(0); step < wantPages; step++ {
		if _, dup := seen[pgno]; !pgnoInRange(pgno, totalPages) || dup {
			return
		}
		seen[pgno] = struct{}{}
		*out = append(*out, pgno)
		page := kvPageAt(b, pgno)
		if decodePageHeader(page).pageType != pageTypeOverflow {
			return
		}
		pgno = le.Uint32(page[overflowNextPgno:])
		if pgno == 0 {
			return
		}
	}
}

// --- reader validation (§C.5, never-panic) ---

// validateKV recursively validates a KV subtree (reader §9 for §D): per-page CRC32C,
// page-type + self-pgno + reserved checks, slot directory in bounds, entries sorted +
// key-disjoint across the whole tree, child pgnos in [2, total_pages), depth bounded by
// TREE_HEIGHT_MAX, overflow chains read-by-count, and type == 0 text validation over the
// whole reassembled value. Never panics or loops on hostile-but-checksum-valid input. vis is
// the file-wide page visitor (F2): every KV page and overflow page is marked through it, so a
// page shared with the scope table, another scope's KV tree, or another entry's chain is
// rejected as corruption (it also subsumes the per-node duplicate-child check).
func validateKV(b []byte, rootPgno uint32, totalPages uint64, vis *pageVisitor) error {
	if rootPgno == 0 {
		return nil
	}
	if !pgnoInRange(rootPgno, totalPages) {
		return errStructural("kv_root out of range")
	}
	var leafDepth uint32
	haveLeafDepth := false
	var prevKey []byte
	havePrevKey := false
	return validateKVNode(b, rootPgno, 1, totalPages, vis, &leafDepth, &haveLeafDepth, &prevKey, &havePrevKey)
}

func validateKVNode(b []byte, pgno, depth uint32, totalPages uint64, vis *pageVisitor,
	leafDepth *uint32, haveLeafDepth *bool, prevKey *[]byte, havePrevKey *bool) error {
	if depth > treeHeightMax {
		return errInvariant("kv path deeper than TREE_HEIGHT_MAX")
	}
	// Mark this page in the file-wide visitor: a second visit (shared/duplicate page) is
	// corruption (F2). pgno was range-checked by the caller (kv_root / child loop below).
	if err := vis.mark(pgno); err != nil {
		return err
	}
	page := kvPageAt(b, pgno)
	if !verifyPage(page) {
		return errChecksumFailed("kv page")
	}
	h := decodePageHeader(page)
	if h.reserved != 0 {
		return errNonZeroReserved("kv page header reserved")
	}
	if h.pgno != pgno {
		return errStructural("kv page self-pgno mismatch")
	}
	count := int(h.entryCount)
	switch h.pageType {
	case pageTypeKVLeaf:
		switch {
		case !*haveLeafDepth:
			*leafDepth = depth
			*haveLeafDepth = true
		case *leafDepth == depth:
		default:
			return errInvariant("kv leaves at differing depths")
		}
		if count < 1 {
			return errInvariant("kv leaf empty")
		}
		// The slot directory must fit; each slot's entry must lie after it.
		if pageHeaderSize+count*kvSlotSize > pageSize {
			return errStructural("kv leaf slot directory overflows page")
		}
		leaf := newKVLeafView(page, count)
		for i := 0; i < count; i++ {
			hdr, err := leaf.entryHdr(i)
			if err != nil {
				return err
			}
			if err := checkKey(hdr.key); err != nil {
				return err
			}
			// Globally strictly increasing keys (sorted + disjoint across the tree).
			if *havePrevKey && bytes.Compare(hdr.key, *prevKey) <= 0 {
				return errInvariant("kv keys not sorted/disjoint")
			}
			*prevKey = cloneBytes(hdr.key)
			*havePrevKey = true
			// Validate the value WITHOUT materializing an overflow-spanning one (§C.4/§C.5):
			// text (type 0) must be NUL-free valid UTF-8. The overflow chain (if any) is
			// walked through the file-wide visitor, so a chain page shared with any other
			// entry/chain/tree is rejected (F2).
			text := hdr.typ == kvTypeText
			if hdr.inlineOK {
				if text {
					if bytes.IndexByte(hdr.inline, 0) >= 0 {
						return errInvariant("kv text value contains NUL")
					}
					if !utf8.Valid(hdr.inline) {
						return errInvariant("kv text value not valid UTF-8")
					}
				}
			} else if err := validateOverflowValue(b, hdr.first, hdr.total, totalPages, vis, text); err != nil {
				return err
			}
		}
		return nil
	case pageTypeKVBranch:
		if count < 1 {
			return errInvariant("kv branch has no separators")
		}
		if kvBranchDirStart+count*kvSlotSize > pageSize {
			return errStructural("kv branch slot directory overflows page")
		}
		br := newKVBranchView(page, count)
		// Separators strictly increasing.
		var prevSep []byte
		havePrevSep := false
		for i := 0; i < count; i++ {
			sep, _, err := br.sep(i)
			if err != nil {
				return err
			}
			if err := checkKey(sep); err != nil {
				return err
			}
			if havePrevSep && bytes.Compare(sep, prevSep) <= 0 {
				return errInvariant("kv separators not increasing")
			}
			prevSep = cloneBytes(sep)
			havePrevSep = true
		}
		// Children in range.
		for j := 0; j <= count; j++ {
			c, err := br.child(j)
			if err != nil {
				return err
			}
			if !pgnoInRange(c, totalPages) {
				return errStructural("kv child pgno out of range")
			}
		}
		for j := 0; j <= count; j++ {
			c, err := br.child(j)
			if err != nil {
				return err
			}
			if err := validateKVNode(b, c, depth+1, totalPages, vis, leafDepth, haveLeafDepth, prevKey, havePrevKey); err != nil {
				return err
			}
		}
		return nil
	default:
		return errStructural("kv unexpected page_type")
	}
}

// --- page encoders (writer-driven; §D) ---

// leafSlot is the encoded body of one KV leaf entry, for bulk-load packing. value_kind is
// decided by the caller (inline vs overflow) before this point. overflow is set when the
// value lives in a chain.
type leafSlot struct {
	key       []byte
	typ       uint32
	overflow  bool
	value     []byte // inline value (overflow == false)
	firstPgno uint32 // chain head (overflow == true)
	totalLen  uint64 // value total length (overflow == true)
}

// slotKey returns the slot's sort/separator key.
func (s *leafSlot) slotKey() []byte { return s.key }

// entrySize returns the encoded entry size in the heap (excluding the 2-byte slot), §D.
func (s *leafSlot) entrySize() int {
	if s.overflow {
		return kvOverflowEntrySize(len(s.key))
	}
	return kvInlineEntrySize(len(s.key), len(s.value))
}

// footprint returns the total page footprint of this entry: heap bytes + its slot, §D.
func (s *leafSlot) footprint() int { return s.entrySize() + kvSlotSize }

// encode writes this entry's heap bytes at out (caller positions out correctly).
func (s *leafSlot) encode(out []byte) {
	p := 0
	if !s.overflow {
		putU16(out, &p, uint16(len(s.key)))
		putBytes(out, &p, s.key)
		putU32(out, &p, s.typ)
		out[p] = kvValueInline
		p++
		putU32(out, &p, uint32(len(s.value)))
		putBytes(out, &p, s.value)
		return
	}
	putU16(out, &p, uint16(len(s.key)))
	putBytes(out, &p, s.key)
	putU32(out, &p, s.typ)
	out[p] = kvValueOverflow
	p++
	putU32(out, &p, s.firstPgno)
	putU64(out, &p, s.totalLen)
}

// writeKVLeaf builds one KV leaf page in page from slots (sorted by key). The slot directory
// grows from the front; entries are packed from the back. The caller has sized slots to fit.
func writeKVLeaf(page []byte, pgno uint32, slots []leafSlot) {
	for i := range page {
		page[i] = 0
	}
	writePageHeader(page, pageTypeKVLeaf, uint16(len(slots)), pgno)
	heapEnd := pageSize // entries grow downward from the page end
	for i := range slots {
		sz := slots[i].entrySize()
		start := heapEnd - sz
		slots[i].encode(page[start:heapEnd])
		so := pageHeaderSize + i*kvSlotSize
		le.PutUint16(page[so:], uint16(start))
		heapEnd = start
	}
	finalizeChecksum(page)
}

// branchSep is a KV branch separator for bulk-load: (sep_key, child_pgno).
type branchSep struct {
	sep   []byte
	child uint32
}

// writeKVBranch builds one KV branch page: a leftmost child pgno (fixed field) + seps
// separators in a slot directory (sorted by sep). The caller has sized seps to fit.
func writeKVBranch(page []byte, pgno, leftmost uint32, seps []branchSep) {
	for i := range page {
		page[i] = 0
	}
	writePageHeader(page, pageTypeKVBranch, uint16(len(seps)), pgno)
	le.PutUint32(page[pageHeaderSize:], leftmost)
	heapEnd := pageSize
	for i := range seps {
		sz := kvBranchSepSize(len(seps[i].sep))
		start := heapEnd - sz
		out := page[start:heapEnd]
		p := 0
		putU16(out, &p, uint16(len(seps[i].sep)))
		putBytes(out, &p, seps[i].sep)
		putU32(out, &p, seps[i].child)
		so := kvBranchDirStart + i*kvSlotSize
		le.PutUint16(page[so:], uint16(start))
		heapEnd = start
	}
	finalizeChecksum(page)
}

// writeOverflow writes one overflow page: header, next_pgno, and payload (zero-padded to the
// page).
func writeOverflow(page []byte, pgno, next uint32, payload []byte) {
	for i := range page {
		page[i] = 0
	}
	writePageHeader(page, pageTypeOverflow, 0, pgno)
	le.PutUint32(page[overflowNextPgno:], next)
	body := pageHeaderSize + 4
	copy(page[body:body+len(payload)], payload)
	finalizeChecksum(page)
}

// --- in-page write cursor helpers ---

func putU16(out []byte, p *int, v uint16) {
	le.PutUint16(out[*p:], v)
	*p += 2
}

func putU32(out []byte, p *int, v uint32) {
	le.PutUint32(out[*p:], v)
	*p += 4
}

func putU64(out []byte, p *int, v uint64) {
	le.PutUint64(out[*p:], v)
	*p += 8
}

func putBytes(out []byte, p *int, v []byte) {
	copy(out[*p:], v)
	*p += len(v)
}
