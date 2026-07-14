package iprangedb

import (
	"fmt"
	"sort"
)

// Scope table for mode 2 (indirect bitmap interning).
// Maps scope_id → bitmap. Stored as a B+tree (page types 4/5).

const MaxBitmapWidth = 256
const ScopeEntrySize = 4 + 2 + MaxBitmapWidth // 262

// ScopeBitmapOverflow is the sentinel stored in the inline bitmap_len field when
// a scope entry's bitmap is too large for the inline slot (more than
// MaxBitmapWidth bytes). The true bitmap then lives in a chain of
// PageTypeOverflow pages; the inline record carries the true length and the
// first overflow page number.
const ScopeBitmapOverflow uint16 = 0xFFFF

const overflowPayloadOff = PageHeaderSize + 4

// readEntryBitmap reads the bitmap for the entry at recOff within page,
// following the overflow chain when the inline bitmap_len is the sentinel.
func readEntryBitmap(bytes, page []byte, recOff int) []byte {
	bitmapLen := u16le(page, recOff+4)
	if bitmapLen == ScopeBitmapOverflow {
		trueLen := int(u32le(page, recOff+6))
		payloadCap := PageSize - overflowPayloadOff
		out := make([]byte, 0, trueLen)
		pgno := u32le(page, recOff+10)
		guard := uint32(0)
		for pgno != 0 && len(out) < trueLen {
			guard++
			if guard > TreeHeightMax {
				break
			}
			base := int(pgno) * PageSize
			if base+PageSize > len(bytes) {
				break
			}
			opage := bytes[base : base+PageSize]
			next := u32le(opage, PageHeaderSize)
			need := trueLen - len(out)
			if need > payloadCap {
				need = payloadCap
			}
			out = append(out, opage[overflowPayloadOff:overflowPayloadOff+need]...)
			pgno = next
		}
		return out
	}
	n := int(bitmapLen)
	if n > MaxBitmapWidth {
		n = MaxBitmapWidth
	}
	out := make([]byte, n)
	copy(out, page[recOff+6:recOff+6+n])
	return out
}

// writeOverflowChain writes a bitmap that exceeds the inline slot to a fresh
// chain of overflow pages and returns the first page number.
func writeOverflowChain(store pageStore, bitmap []byte, allocated *[]uint32, freePool *[]uint32) (uint32, error) {
	payloadCap := PageSize - overflowPayloadOff
	nPages := (len(bitmap) + payloadCap - 1) / payloadCap
	if nPages == 0 {
		nPages = 1
	}
	pages := make([]uint32, 0, nPages)
	for i := 0; i < nPages; i++ {
		var pgno uint32
		if len(*freePool) > 0 {
			pgno = (*freePool)[len(*freePool)-1]
			*freePool = (*freePool)[:len(*freePool)-1]
		} else {
			var err error
			pgno, err = store.allocPage()
			if err != nil {
				return 0, err
			}
		}
		*allocated = append(*allocated, pgno)
		pages = append(pages, pgno)
	}
	for i, pgno := range pages {
		page := store.pageMut(pgno)
		for j := range page {
			page[j] = 0
		}
		var next uint32
		if i+1 < len(pages) {
			next = pages[i+1]
		}
		writeHeader(page, PageTypeOverflow, 0, pgno)
		putU32(page, PageHeaderSize, next)
		start := i * payloadCap
		end := start + payloadCap
		if end > len(bitmap) {
			end = len(bitmap)
		}
		copy(page[overflowPayloadOff:overflowPayloadOff+(end-start)], bitmap[start:end])
	}
	return pages[0], nil
}

// ScopeEntry is an in-memory scope registry entry.
type ScopeEntry struct {
	ScopeID uint32
	Bitmap  []byte
}

// ScopeRegistry maintains scope_id → bitmap mappings WITHOUT materializing the
// committed scope table into a heap HashMap at open time.
//
// Design (issue-1/issue-2/issue-7 fix):
//   - The committed scope table stays on disk (a B+tree keyed by scope_id).
//     Resolve() descends that tree via findScope → O(log S), zero heap.
//   - Only THIS transaction's newly-interned entries live in RAM (newEntries).
//   - Intern() dedups against the new set (O(1)); against the committed set it
//     streams the on-disk scope tree via findScopeByBitmap (O(scope_pages)
//     time, O(1) heap). It NEVER materializes the whole committed table into a
//     HashMap — the old eager load allocated O(S) on the first intern after
//     reopen (issue-7).
//   - committedIndex is retained ONLY as an in-memory facility for
//     ScopeRegistryFromEntries (tests) and the incremental fold performed by
//     Promote (this-txn new entries, O(new)). It is never populated from disk.
//
// The registry is the single source of truth for the committed scope-tree root.
type ScopeRegistry struct {
	// This transaction's not-yet-committed additions.
	newEntries     []ScopeEntry
	newBitmapIndex map[string]uint32 // O(1) bitmap → scope_id for new entries

	// committedRoot is the on-disk scope-table B+tree root (0 = none).
	committedRoot uint32

	// committedIndex is the in-memory bitmap → scope_id map. NEVER loaded from
	// disk (issue-7). Populated only by ScopeRegistryFromEntries (tests) and
	// incrementally by Promote (this-session new entries). Production dedup of
	// committed bitmaps goes through findScopeByBitmap instead.
	committedIndex map[string]uint32

	nextID uint32
}

// NewScopeRegistry creates an empty registry (fresh create, no committed data).
func NewScopeRegistry() *ScopeRegistry {
	return &ScopeRegistry{
		newBitmapIndex: make(map[string]uint32),
		nextID:         1,
	}
}

// OpenScopeRegistry opens a registry over an existing committed scope table
// WITHOUT loading it. committedBytes is only needed later by Resolve/Intern;
// nextID must be max committed scope_id + 1 (caller computes via ReadMaxScopeID).
func OpenScopeRegistry(committedRoot uint32, nextID uint32) *ScopeRegistry {
	return &ScopeRegistry{
		newBitmapIndex: make(map[string]uint32),
		committedRoot:  committedRoot,
		nextID:         nextID,
	}
}

// ScopeRegistryFromEntries builds a registry with a pre-populated committed
// index. Used by tests; semantically equivalent to having lazily loaded the
// given committed entries (no on-disk root is referenced).
func ScopeRegistryFromEntries(entries []ScopeEntry) *ScopeRegistry {
	maxID := uint32(0)
	idx := make(map[string]uint32, len(entries))
	for _, e := range entries {
		if e.ScopeID > maxID {
			maxID = e.ScopeID
		}
		idx[string(e.Bitmap)] = e.ScopeID
	}
	return &ScopeRegistry{
		newBitmapIndex: make(map[string]uint32),
		committedIndex: idx,
		nextID:         maxID + 1,
	}
}

// Intern finds or creates a scope_id for the given bitmap.
// committedBytes is the committed page image (for dedup against the on-disk
// table). Returns (scope_id, was_new).
//
// Dedup order (issue-7):
//  1. this-txn new entries (O(1) via newBitmapIndex)
//  2. in-memory committedIndex if present (O(1); FromEntries/Promote)
//  3. committed on-disk scope tree via findScopeByBitmap
//     (O(scope_pages) time, O(1) heap — no eager O(S) load)
//  4. miss → mint a new id.
func (r *ScopeRegistry) Intern(bitmap []byte, committedBytes []byte) (uint32, bool) {
	if id, ok := r.newBitmapIndex[string(bitmap)]; ok {
		return id, false
	}
	if r.committedIndex != nil {
		if id, ok := r.committedIndex[string(bitmap)]; ok {
			return id, false
		}
	}
	if r.committedRoot != 0 {
		if id, ok := findScopeByBitmap(committedBytes, r.committedRoot, bitmap); ok {
			return id, false
		}
	}
	id := r.nextID
	r.nextID++
	// make+copy (not append) so an empty bitmap is stored as a non-nil empty
	// slice — distinguishable from the nil Resolve returns for "not found".
	stored := make([]byte, len(bitmap))
	copy(stored, bitmap)
	r.newBitmapIndex[string(stored)] = id
	r.newEntries = append(r.newEntries, ScopeEntry{ScopeID: id, Bitmap: stored})
	return id, true
}

// Resolve returns the bitmap for a scope_id, or nil if not found.
// O(log S) via findScope for committed scopes; linear over this-txn new
// entries (small). committedBytes is the committed page image.
func (r *ScopeRegistry) Resolve(scopeID uint32, committedBytes []byte) []byte {
	for i := range r.newEntries {
		if r.newEntries[i].ScopeID == scopeID {
			return r.newEntries[i].Bitmap
		}
	}
	if r.committedRoot != 0 {
		return findScope(committedBytes, r.committedRoot, scopeID)
	}
	return nil
}

// ResolveRef is the zero-copy variant of Resolve (issue-6): for committed
// scopes it returns a sub-slice of committedBytes (no per-call allocation).
// Used by the all-to-all / foreign-vs-all overlap scans, which resolve one
// bitmap per record and only need to iterate set bits.
func (r *ScopeRegistry) ResolveRef(scopeID uint32, committedBytes []byte) []byte {
	for i := range r.newEntries {
		if r.newEntries[i].ScopeID == scopeID {
			return r.newEntries[i].Bitmap
		}
	}
	if r.committedRoot != 0 {
		return findScopeRef(committedBytes, r.committedRoot, scopeID)
	}
	return nil
}

// canonicalize drops trailing zero bytes from a scope bitmap so two bitmaps
// with the same set of feeds map to the same scope_id.
func canonicalize(bitmap []byte) []byte {
	end := len(bitmap)
	for end > 0 && bitmap[end-1] == 0 {
		end--
	}
	return bitmap[:end]
}

// BitmapSetFeed sets a feed bit. Returns the new scope_id and false if the
// input scope_id does not resolve to a known scope.
func (r *ScopeRegistry) BitmapSetFeed(scopeID uint32, feedBit uint32, committedBytes []byte) (uint32, bool) {
	bm := r.Resolve(scopeID, committedBytes)
	if bm == nil {
		return 0, false
	}
	newBm := append([]byte(nil), bm...)
	byteIdx := int(feedBit / 8)
	bitIdx := feedBit % 8
	if byteIdx >= len(newBm) {
		padded := make([]byte, byteIdx+1)
		copy(padded, newBm)
		newBm = padded
	}
	newBm[byteIdx] |= 1 << bitIdx
	id, _ := r.Intern(newBm, committedBytes)
	return id, true
}

// BitmapClearFeed clears a feed bit. Returns the new scope_id (0 if empty) and
// false if the input scope_id does not resolve to a known scope. Trailing zero
// bytes are trimmed so clearing the highest set bit returns to the canonical
// (original) scope.
func (r *ScopeRegistry) BitmapClearFeed(scopeID uint32, feedBit uint32, committedBytes []byte) (uint32, bool) {
	bm := r.Resolve(scopeID, committedBytes)
	if bm == nil {
		return 0, false
	}
	newBm := append([]byte(nil), bm...)
	byteIdx := int(feedBit / 8)
	bitIdx := feedBit % 8
	if byteIdx < len(newBm) {
		newBm[byteIdx] &= ^(1 << bitIdx)
	}
	trimmed := canonicalize(newBm)
	allZero := true
	for _, b := range trimmed {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return 0, true
	}
	id, _ := r.Intern(trimmed, committedBytes)
	return id, true
}

// EntriesForCommit returns the full entry list (committed ∪ new) for the
// bulk rebuild at commit. Reads the committed table from disk (no index
// warming — issue-7) and appends this-txn new entries.
func (r *ScopeRegistry) EntriesForCommit(committedBytes []byte) []ScopeEntry {
	var all []ScopeEntry
	if r.committedRoot != 0 {
		committed, err := readAllScopes(committedBytes, r.committedRoot)
		if err == nil {
			all = committed
		}
	}
	all = append(all, r.newEntries...)
	return all
}

// Promote advances the registry to the newly-committed root. Folds this-txn
// new entries into the (warm) committed index and clears the new set.
func (r *ScopeRegistry) Promote(newRoot uint32) {
	if r.committedIndex != nil {
		for _, e := range r.newEntries {
			r.committedIndex[string(e.Bitmap)] = e.ScopeID
		}
	} else if len(r.newEntries) > 0 {
		// Index was never built (no intern-miss occurred). Build it now from
		// the new entries so post-commit interns dedup without a re-read.
		idx := make(map[string]uint32, len(r.newEntries))
		for _, e := range r.newEntries {
			idx[string(e.Bitmap)] = e.ScopeID
		}
		r.committedIndex = idx
	}
	r.newEntries = nil
	r.newBitmapIndex = make(map[string]uint32)
	r.committedRoot = newRoot
}

func (r *ScopeRegistry) CommittedRoot() uint32 { return r.committedRoot }
func (r *ScopeRegistry) Len() int              { return len(r.newEntries) }
func (r *ScopeRegistry) IsEmpty() bool {
	if len(r.newEntries) > 0 {
		return false
	}
	return r.committedRoot == 0
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Encode/decode scope entries for the on-disk format.

// encodeScopeEntry encodes an inline scope entry. Only valid for bitmaps that
// fit inline (len <= MaxBitmapWidth); larger bitmaps are spilled by
// writeOverflowChain at build time.
func encodeScopeEntry(out []byte, entry *ScopeEntry) {
	for i := range out {
		out[i] = 0
	}
	putU32(out, 0, entry.ScopeID)
	n := len(entry.Bitmap)
	if n > MaxBitmapWidth {
		n = MaxBitmapWidth
	}
	putU16(out, 4, uint16(n))
	copy(out[6:6+n], entry.Bitmap[:n])
}

// decodeScopeEntry decodes an inline scope entry. Overflow entries (sentinel
// bitmap_len) cannot be decoded from the bare record — use readEntryBitmap.
func decodeScopeEntry(rec []byte) ScopeEntry {
	scopeID := u32le(rec, 0)
	bitmapLen := u16le(rec, 4)
	var bitmap []byte
	if bitmapLen == ScopeBitmapOverflow {
		bitmap = []byte{}
	} else {
		n := int(bitmapLen)
		if n > MaxBitmapWidth {
			n = MaxBitmapWidth
		}
		bitmap = make([]byte, n)
		copy(bitmap, rec[6:6+n])
	}
	return ScopeEntry{ScopeID: scopeID, Bitmap: bitmap}
}

// BuildScopeTree creates the scope table B+tree in the page store.
func buildScopeTree(store pageStore, entries []ScopeEntry, allocated *[]uint32, freePool *[]uint32) (uint32, error) {
	if len(entries) == 0 {
		return 0, nil
	}

	sorted := make([]*ScopeEntry, len(entries))
	for i := range entries {
		sorted[i] = &entries[i]
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ScopeID < sorted[j].ScopeID
	})

	leafMax := (PageSize - PageHeaderSize) / ScopeEntrySize
	var leafPgnos []uint32

	for i := 0; i < len(sorted); i += leafMax {
		end := i + leafMax
		if end > len(sorted) {
			end = len(sorted)
		}
		chunk := sorted[i:end]
		var pgno uint32
		if len(*freePool) > 0 {
			pgno = (*freePool)[len(*freePool)-1]
			*freePool = (*freePool)[:len(*freePool)-1]
		} else {
			var err error
			pgno, err = store.allocPage()
			if err != nil {
				return 0, err
			}
		}
		*allocated = append(*allocated, pgno)
		// Spill any oversized bitmaps to overflow chains BEFORE fetching the
		// leaf page: allocating overflow pages may grow the store and invalidate
		// a page slice fetched now.
		type ov struct {
			idx   int
			first uint32
		}
		var overflows []ov
		for j, entry := range chunk {
			if len(entry.Bitmap) > MaxBitmapWidth {
				first, err := writeOverflowChain(store, entry.Bitmap, allocated, freePool)
				if err != nil {
					return 0, err
				}
				overflows = append(overflows, ov{idx: j, first: first})
			}
		}
		page := store.pageMut(pgno)
		for j := range page {
			page[j] = 0
		}
		writeHeader(page, PageTypeScopeLeaf, uint16(len(chunk)), pgno)
		for j, entry := range chunk {
			off := PageHeaderSize + j*ScopeEntrySize
			rec := page[off : off+ScopeEntrySize]
			if len(entry.Bitmap) > MaxBitmapWidth {
				for k := range rec {
					rec[k] = 0
				}
				putU32(rec, 0, entry.ScopeID)
				putU16(rec, 4, ScopeBitmapOverflow)
				putU32(rec, 6, uint32(len(entry.Bitmap)))
				first := uint32(0)
				for _, ov := range overflows {
					if ov.idx == j {
						first = ov.first
						break
					}
				}
				putU32(rec, 10, first)
			} else {
				encodeScopeEntry(rec, entry)
			}
		}
		leafPgnos = append(leafPgnos, pgno)
	}

	if len(leafPgnos) == 1 {
		return leafPgnos[0], nil
	}

	// Build branch levels bottom-up. Each level is a list of (pgno, minScopeID)
	// pairs. The separator between two sibling subtrees is the MIN scope_id of
	// the right subtree (carried up from the level below). A branch's min for
	// the parent level is the min of its whole subtree = its first child's min.
	sepWidth := ScopeKeyWidth // 4
	branchMax := (PageSize - PageHeaderSize - 4) / (sepWidth + 4)

	type subtree struct {
		pgno uint32
		min  uint32
	}
	level := make([]subtree, len(leafPgnos))
	for i, pgno := range leafPgnos {
		page := store.page(pgno)
		level[i] = subtree{pgno: pgno, min: u32le(page, PageHeaderSize)}
	}

	for len(level) > 1 {
		var next []subtree
		childIdx := 0
		for childIdx < len(level) {
			remaining := len(level) - childIdx
			count := remaining
			if count > branchMax {
				count = branchMax
			}
			var pgno uint32
			if len(*freePool) > 0 {
				pgno = (*freePool)[len(*freePool)-1]
				*freePool = (*freePool)[:len(*freePool)-1]
			} else {
				var err error
				pgno, err = store.allocPage()
				if err != nil {
					return 0, err
				}
			}
			*allocated = append(*allocated, pgno)
			page := store.pageMut(pgno)
			for j := range page {
				page[j] = 0
			}
			putU32(page, PageHeaderSize, level[childIdx].pgno)
			for i := 0; i < count-1; i++ {
				off := PageHeaderSize + 4 + i*(sepWidth+4)
				putU32(page, off, level[childIdx+i+1].min)
				putU32(page, off+sepWidth, level[childIdx+i+1].pgno)
			}
			writeHeader(page, PageTypeScopeBranch, uint16(count-1), pgno)
			next = append(next, subtree{pgno: pgno, min: level[childIdx].min})
			childIdx += count
		}
		level = next
	}
	return level[0].pgno, nil
}

// ReadAllScopes reads all scope entries from a committed scope table.
func readAllScopes(bytes []byte, rootPgno uint32) ([]ScopeEntry, error) {
	if rootPgno == 0 {
		return nil, nil
	}
	var entries []ScopeEntry
	err := readScopeNode(bytes, rootPgno, 0, &entries)
	return entries, err
}

func readScopeNode(bytes []byte, pgno uint32, depth uint32, out *[]ScopeEntry) error {
	if depth > TreeHeightMax {
		return fmt.Errorf("scope table too deep")
	}
	off := int(pgno) * PageSize
	if off+PageSize > len(bytes) {
		return fmt.Errorf("scope page out of bounds")
	}
	page := bytes[off : off+PageSize]
	h := decodeHeader(page)
	switch h.pageType {
	case PageTypeScopeLeaf:
		count := int(h.entryCount)
		for i := 0; i < count; i++ {
			recOff := PageHeaderSize + i*ScopeEntrySize
			scopeID := u32le(page, recOff)
			bitmap := readEntryBitmap(bytes, page, recOff)
			*out = append(*out, ScopeEntry{ScopeID: scopeID, Bitmap: bitmap})
		}
		return nil
	case PageTypeScopeBranch:
		bv := newBranchView(page, int(h.entryCount), int(ScopeKeyWidth))
		for j := 0; j < bv.childCount(); j++ {
			if err := readScopeNode(bytes, bv.child(j), depth+1, out); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unexpected page type %d in scope table", h.pageType)
	}
}

// readAllScopesChecked reads all scope entries from a committed scope table,
// verifying the per-page CRC32C of every scope page walked. A corrupt scope
// page (CRC failure) is an error so that openWriter rejects the file instead
// of silently loading garbage scope data.
func readAllScopesChecked(bytes []byte, rootPgno uint32, total uint32) ([]ScopeEntry, error) {
	if rootPgno == 0 {
		return nil, nil
	}
	var entries []ScopeEntry
	if err := readScopeNodeChecked(bytes, rootPgno, 0, total, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func readScopeNodeChecked(bytes []byte, pgno uint32, depth uint32, total uint32, out *[]ScopeEntry) error {
	if depth > TreeHeightMax {
		return fmt.Errorf("scope table too deep")
	}
	if uint64(pgno) >= uint64(total) {
		return fmt.Errorf("scope page out of bounds")
	}
	off := int(pgno) * PageSize
	if off+PageSize > len(bytes) {
		return fmt.Errorf("scope page out of bounds")
	}
	page := bytes[off : off+PageSize]
	if !verifyPage(page) {
		return fmt.Errorf("scope table page %d fails CRC", pgno)
	}
	h := decodeHeader(page)
	switch h.pageType {
	case PageTypeScopeLeaf:
		count := int(h.entryCount)
		for i := 0; i < count; i++ {
			recOff := PageHeaderSize + i*ScopeEntrySize
			scopeID := u32le(page, recOff)
			bitmap := readEntryBitmap(bytes, page, recOff)
			*out = append(*out, ScopeEntry{ScopeID: scopeID, Bitmap: bitmap})
		}
		return nil
	case PageTypeScopeBranch:
		bv := newBranchView(page, int(h.entryCount), int(ScopeKeyWidth))
		for j := 0; j < bv.childCount(); j++ {
			if err := readScopeNodeChecked(bytes, bv.child(j), depth+1, total, out); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unexpected page type %d in scope table", h.pageType)
	}
}

// scopeIDExists reports whether targetID is a defined scope, without
// materializing its bitmap. O(log S) time, O(1) heap — used by the open-time
// data-record scope validation so it does not allocate per record.
func scopeIDExists(bytes []byte, rootPgno uint32, targetID uint32) bool {
	if rootPgno == 0 {
		return false
	}
	pgno := rootPgno
	for guard := 0; guard < TreeHeightMax; guard++ {
		off := int(pgno) * PageSize
		if off+PageSize > len(bytes) {
			return false
		}
		page := bytes[off : off+PageSize]
		h := decodeHeader(page)
		switch h.pageType {
		case PageTypeScopeLeaf:
			count := min(int(h.entryCount), (PageSize-PageHeaderSize)/ScopeEntrySize)
			lo, hi := 0, count
			for lo < hi {
				mid := lo + (hi-lo)/2
				recOff := PageHeaderSize + mid*ScopeEntrySize
				id := u32le(page, recOff)
				if id < targetID {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			if lo < count {
				recOff := PageHeaderSize + lo*ScopeEntrySize
				return u32le(page, recOff) == targetID
			}
			return false
		case PageTypeScopeBranch:
			bv := newBranchView(page, min(int(h.entryCount), (PageSize-PageHeaderSize-4)/(int(ScopeKeyWidth)+4)), int(ScopeKeyWidth))
			lo, hi := 0, bv.sepCount
			for lo < hi {
				mid := lo + (hi-lo)/2
				sep := u32le(bv.sep(mid), 0)
				if sep <= targetID {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			pgno = bv.child(lo)
		default:
			return false
		}
	}
	return false
}

// FindScope finds a single scope entry by scope_id via B+tree descent.
// O(log S) — reads ~3-4 pages for thousands of entries.
func findScope(bytes []byte, rootPgno uint32, targetID uint32) []byte {
	if rootPgno == 0 {
		return nil
	}
	pgno := rootPgno
	for guard := 0; guard < TreeHeightMax; guard++ {
		off := int(pgno) * PageSize
		if off+PageSize > len(bytes) {
			return nil
		}
		page := bytes[off : off+PageSize]
		h := decodeHeader(page)
		switch h.pageType {
		case PageTypeScopeLeaf:
			count := int(h.entryCount)
			lo, hi := 0, count
			for lo < hi {
				mid := lo + (hi-lo)/2
				recOff := PageHeaderSize + mid*ScopeEntrySize
				id := u32le(page, recOff)
				if id < targetID {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			if lo < count {
				recOff := PageHeaderSize + lo*ScopeEntrySize
				id := u32le(page, recOff)
				if id == targetID {
					return readEntryBitmap(bytes, page, recOff)
				}
			}
			return nil
		case PageTypeScopeBranch:
			bv := newBranchView(page, int(h.entryCount), int(ScopeKeyWidth))
			lo, hi := 0, bv.sepCount
			for lo < hi {
				mid := lo + (hi-lo)/2
				sep := Ipv4Key(u32le(bv.sep(mid), 0))
				if uint32(sep) <= targetID {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			pgno = bv.child(lo)
		default:
			return nil
		}
	}
	return nil
}

// findScopeRef is the zero-copy variant of findScope (issue-6): it returns a
// sub-slice of bytes (the committed page image) instead of an allocated copy.
// Used by the all-to-all overlap scan which only needs to iterate set bits.
func findScopeRef(bytes []byte, rootPgno uint32, targetID uint32) []byte {
	if rootPgno == 0 {
		return nil
	}
	pgno := rootPgno
	for guard := 0; guard < TreeHeightMax; guard++ {
		off := int(pgno) * PageSize
		if off+PageSize > len(bytes) {
			return nil
		}
		page := bytes[off : off+PageSize]
		h := decodeHeader(page)
		switch h.pageType {
		case PageTypeScopeLeaf:
			count := int(h.entryCount)
			lo, hi := 0, count
			for lo < hi {
				mid := lo + (hi-lo)/2
				recOff := PageHeaderSize + mid*ScopeEntrySize
				id := u32le(page, recOff)
				if id < targetID {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			if lo < count {
				recOff := PageHeaderSize + lo*ScopeEntrySize
				id := u32le(page, recOff)
				if id == targetID {
					bmLen := u16le(page, recOff+4)
					// Overflow entries span pages and cannot be returned as a
					// single borrowed slice; the overlap scans that use this
					// zero-copy path only encounter inline bitmaps.
					if bmLen == ScopeBitmapOverflow {
						return nil
					}
					n := int(bmLen)
					if n > MaxBitmapWidth {
						n = MaxBitmapWidth
					}
					return page[recOff+6 : recOff+6+n]
				}
			}
			return nil
		case PageTypeScopeBranch:
			bv := newBranchView(page, int(h.entryCount), int(ScopeKeyWidth))
			lo, hi := 0, bv.sepCount
			for lo < hi {
				mid := lo + (hi-lo)/2
				sep := Ipv4Key(u32le(bv.sep(mid), 0))
				if uint32(sep) <= targetID {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			pgno = bv.child(lo)
		default:
			return nil
		}
	}
	return nil
}

// findScopeByBitmap returns the scope_id of an existing entry whose bitmap
// equals target by streaming the committed scope tree (issue-7). O(scope_pages)
// time, O(1) heap — replaces the old eager O(S) HashMap materialization. The
// scope tree is keyed by scope_id (not bitmap), so this is a linear leaf scan;
// it is only reached on a newBitmapIndex miss, which is rare (new feed
// combinations). Returns (0, false) if no committed entry matches.
func findScopeByBitmap(bytes []byte, rootPgno uint32, target []byte) (uint32, bool) {
	if rootPgno == 0 {
		return 0, false
	}
	return findScopeByBitmapNode(bytes, rootPgno, 0, target)
}

func findScopeByBitmapNode(bytes []byte, pgno uint32, depth uint32, target []byte) (uint32, bool) {
	if depth > TreeHeightMax {
		return 0, false
	}
	off := int(pgno) * PageSize
	if off+PageSize > len(bytes) {
		return 0, false
	}
	page := bytes[off : off+PageSize]
	h := decodeHeader(page)
	switch h.pageType {
	case PageTypeScopeLeaf:
		count := int(h.entryCount)
		for i := 0; i < count; i++ {
			recOff := PageHeaderSize + i*ScopeEntrySize
			rawLen := u16le(page, recOff+4)
			// Fast path: inline entries (the common case) compare without
			// allocating. Only overflow entries need readEntryBitmap.
			if rawLen != ScopeBitmapOverflow {
				n := int(rawLen)
				if n > MaxBitmapWidth {
					n = MaxBitmapWidth
				}
				if n == len(target) && bytesEqual(page[recOff+6:recOff+6+n], target) {
					return u32le(page, recOff), true
				}
				continue
			}
			if bytesEqual(readEntryBitmap(bytes, page, recOff), target) {
				return u32le(page, recOff), true
			}
		}
		return 0, false
	case PageTypeScopeBranch:
		bv := newBranchView(page, int(h.entryCount), int(ScopeKeyWidth))
		for j := 0; j < bv.childCount(); j++ {
			if id, ok := findScopeByBitmapNode(bytes, bv.child(j), depth+1, target); ok {
				return id, true
			}
		}
		return 0, false
	default:
		return 0, false
	}
}

// ReadMaxScopeID returns the highest scope_id in the committed scope table by
// descending to the rightmost leaf (O(log S)). Used at open to compute
// nextID = max + 1 without loading the table.
func ReadMaxScopeID(bytes []byte, rootPgno uint32) (uint32, bool) {
	if rootPgno == 0 {
		return 0, false
	}
	pgno := rootPgno
	for guard := 0; guard < TreeHeightMax; guard++ {
		off := int(pgno) * PageSize
		if off+PageSize > len(bytes) {
			return 0, false
		}
		page := bytes[off : off+PageSize]
		h := decodeHeader(page)
		switch h.pageType {
		case PageTypeScopeLeaf:
			count := int(h.entryCount)
			if count == 0 {
				return 0, false
			}
			recOff := PageHeaderSize + (count-1)*ScopeEntrySize
			return u32le(page, recOff), true
		case PageTypeScopeBranch:
			bv := newBranchView(page, int(h.entryCount), int(ScopeKeyWidth))
			cc := bv.childCount()
			if cc == 0 {
				return 0, false
			}
			pgno = bv.child(cc - 1)
		default:
			return 0, false
		}
	}
	return 0, false
}

// ValidateScopeCRC walks every page of the committed scope tree and verifies
// its per-page CRC32C AND structural integrity WITHOUT materializing entries.
// O(S pages) time, O(log S) heap (descent stack only). Preserves the open-time
// corruption guard that the old eager readAllScopesChecked provided, while
// fixing the 256MB heap load.
//
// Structural checks (entry_count within page capacity, page type matches a
// scope node, child page numbers in range) are essential: a corrupt entry_count
// that still verifies against a recomputed CRC would otherwise pass the CRC
// guard and later cause an out-of-bounds slice access (panic) when the leaf is
// read by findScope/readScopeNode.
func ValidateScopeCRC(bytes []byte, rootPgno uint32) error {
	if rootPgno == 0 {
		return nil
	}
	totalPages := uint32(len(bytes) / PageSize)
	prevMax := uint32(0)
	havePrev := false
	return validateScopeCRCNode(bytes, rootPgno, 0, totalPages, &prevMax, &havePrev)
}

func validateScopeCRCNode(bytes []byte, pgno uint32, depth uint32, totalPages uint32, prevMax *uint32, havePrev *bool) error {
	if depth > TreeHeightMax {
		return fmt.Errorf("scope table too deep")
	}
	if uint64(pgno) >= uint64(totalPages) {
		return fmt.Errorf("scope page out of bounds")
	}
	off := int(pgno) * PageSize
	if off+PageSize > len(bytes) {
		return fmt.Errorf("scope page out of bounds")
	}
	page := bytes[off : off+PageSize]
	if !verifyPage(page) {
		return fmt.Errorf("scope table page %d fails CRC", pgno)
	}
	h := decodeHeader(page)
	switch h.pageType {
	case PageTypeScopeLeaf:
		// entry_count MUST be within the page capacity, or a later read
		// computes an offset past the page and panics.
		count := int(h.entryCount)
		maxEntries := (PageSize - PageHeaderSize) / ScopeEntrySize
		if count < 1 || count > maxEntries {
			return fmt.Errorf("scope leaf entry_count out of range: %d (max %d)", count, maxEntries)
		}
		for i := 0; i < count; i++ {
			recOff := PageHeaderSize + i*ScopeEntrySize
			id := u32le(page, recOff)
			bmLen := u16le(page, recOff+4)
			// An inline bitmap_len beyond the on-disk slot would read past the
			// entry's payload. The overflow sentinel is the one legitimate
			// value > MaxBitmapWidth.
			if bmLen != ScopeBitmapOverflow && int(bmLen) > MaxBitmapWidth {
				return fmt.Errorf("scope bitmap_len exceeds payload")
			}
			if *havePrev && id <= *prevMax {
				return fmt.Errorf("scope_ids not strictly increasing across leaves")
			}
			*prevMax = id
			*havePrev = true
		}
		return nil
	case PageTypeScopeBranch:
		sepCount := int(h.entryCount)
		sepWidth := ScopeKeyWidth // 4
		maxSeps := (PageSize - PageHeaderSize - 4) / (sepWidth + 4)
		if sepCount < 1 || sepCount > maxSeps {
			return fmt.Errorf("scope branch separator count out of range: %d (max %d)", sepCount, maxSeps)
		}
		bv := newBranchView(page, sepCount, sepWidth)
		// child_count = sep_count + 1. Each child MUST be a valid page number in
		// [2, totalPages); otherwise descent reads garbage.
		for j := 0; j < bv.childCount(); j++ {
			child := bv.child(j)
			if child < 2 || uint64(child) >= uint64(totalPages) {
				return fmt.Errorf("scope child pgno out of range: %d", child)
			}
		}
		for j := 0; j < bv.childCount(); j++ {
			if err := validateScopeCRCNode(bytes, bv.child(j), depth+1, totalPages, prevMax, havePrev); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unexpected page type %d in scope table", h.pageType)
	}
}
