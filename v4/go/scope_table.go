package iprangedb

import (
	"fmt"
	"sort"
)

// Scope table for mode 2 (indirect bitmap interning).
// Maps scope_id → bitmap. Stored as a B+tree (page types 4/5).

const MaxBitmapWidth = 256
const ScopeEntrySize = 4 + 2 + MaxBitmapWidth // 262

// ScopeEntry is an in-memory scope registry entry.
type ScopeEntry struct {
	ScopeID uint32
	Bitmap  []byte
}

// ScopeRegistry maintains scope_id → bitmap mappings during a transaction.
// Uses a HashMap for O(1) bitmap → scope_id lookup (fixes #6: was linear search).
type ScopeRegistry struct {
	entries     []ScopeEntry
	bitmapIndex map[string]uint32 // O(1) lookup by bitmap bytes
	nextID      uint32
}

func NewScopeRegistry() *ScopeRegistry {
	return &ScopeRegistry{
		bitmapIndex: make(map[string]uint32),
		nextID:      1,
	}
}

func ScopeRegistryFromEntries(entries []ScopeEntry) *ScopeRegistry {
	maxID := uint32(0)
	bitmapIndex := make(map[string]uint32, len(entries))
	for _, e := range entries {
		if e.ScopeID > maxID {
			maxID = e.ScopeID
		}
		bitmapIndex[string(e.Bitmap)] = e.ScopeID
	}
	return &ScopeRegistry{entries: entries, bitmapIndex: bitmapIndex, nextID: maxID + 1}
}

// Intern finds or creates a scope_id for the given bitmap.
// O(1) lookup via bitmapIndex.
func (r *ScopeRegistry) Intern(bitmap []byte) uint32 {
	if id, ok := r.bitmapIndex[string(bitmap)]; ok {
		return id
	}
	id := r.nextID
	r.nextID++
	stored := make([]byte, len(bitmap))
	copy(stored, bitmap)
	r.bitmapIndex[string(stored)] = id
	r.entries = append(r.entries, ScopeEntry{ScopeID: id, Bitmap: stored})
	return id
}

// Resolve returns the bitmap for a scope_id, or nil if not found.
func (r *ScopeRegistry) Resolve(scopeID uint32) []byte {
	for _, e := range r.entries {
		if e.ScopeID == scopeID {
			return e.Bitmap
		}
	}
	return nil
}

// BitmapSetFeed sets a feed bit. Returns the new scope_id.
func (r *ScopeRegistry) BitmapSetFeed(scopeID uint32, feedBit uint32) uint32 {
	bm := r.Resolve(scopeID)
	if bm == nil {
		return 0
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
	return r.Intern(newBm)
}

// BitmapClearFeed clears a feed bit. Returns the new scope_id (0 if empty).
func (r *ScopeRegistry) BitmapClearFeed(scopeID uint32, feedBit uint32) uint32 {
	bm := r.Resolve(scopeID)
	if bm == nil {
		return 0
	}
	newBm := append([]byte(nil), bm...)
	byteIdx := int(feedBit / 8)
	bitIdx := feedBit % 8
	if byteIdx < len(newBm) {
		newBm[byteIdx] &= ^(1 << bitIdx)
	}
	allZero := true
	for _, b := range newBm {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return 0
	}
	return r.Intern(newBm)
}

func (r *ScopeRegistry) Entries() []ScopeEntry { return r.entries }
func (r *ScopeRegistry) Len() int              { return len(r.entries) }
func (r *ScopeRegistry) IsEmpty() bool         { return len(r.entries) == 0 }

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

func encodeScopeEntry(out []byte, entry *ScopeEntry) {
	for i := range out {
		out[i] = 0
	}
	putU32(out, 0, entry.ScopeID)
	putU16(out, 4, uint16(len(entry.Bitmap)))
	n := len(entry.Bitmap)
	if n > MaxBitmapWidth {
		n = MaxBitmapWidth
	}
	copy(out[6:6+n], entry.Bitmap[:n])
}

func decodeScopeEntry(rec []byte) ScopeEntry {
	scopeID := u32le(rec, 0)
	bitmapLen := int(u16le(rec, 4))
	if bitmapLen > MaxBitmapWidth {
		bitmapLen = MaxBitmapWidth
	}
	bitmap := make([]byte, bitmapLen)
	copy(bitmap, rec[6:6+bitmapLen])
	return ScopeEntry{ScopeID: scopeID, Bitmap: bitmap}
}

// BuildScopeTree creates the scope table B+tree in the page store.
func buildScopeTree(store pageStore, entries []ScopeEntry, allocated *[]uint32) (uint32, error) {
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
	var seps []uint32

	for i := 0; i < len(sorted); i += leafMax {
		end := i + leafMax
		if end > len(sorted) {
			end = len(sorted)
		}
		chunk := sorted[i:end]
		pgno, err := store.allocPage()
		if err != nil {
			return 0, err
		}
		*allocated = append(*allocated, pgno)
		page := store.pageMut(pgno)
		for j := range page {
			page[j] = 0
		}
		writeHeader(page, PageTypeScopeLeaf, uint16(len(chunk)), pgno)
		for j, entry := range chunk {
			off := PageHeaderSize + j*ScopeEntrySize
			encodeScopeEntry(page[off:off+ScopeEntrySize], entry)
		}
		leafPgnos = append(leafPgnos, pgno)
	}
	for i := 1; i < len(leafPgnos); i++ {
		page := store.page(leafPgnos[i])
		seps = append(seps, u32le(page, PageHeaderSize))
	}

	if len(leafPgnos) == 1 {
		return leafPgnos[0], nil
	}

	sepWidth := ScopeKeyWidth // 4
	branchMax := (PageSize - PageHeaderSize - 4) / (sepWidth + 4)

	var branchPgnos []uint32
	childIdx := 0
	for childIdx < len(leafPgnos) {
		remaining := len(leafPgnos) - childIdx
		count := remaining
		if count > branchMax {
			count = branchMax
		}
		pgno, err := store.allocPage()
		if err != nil {
			return 0, err
		}
		*allocated = append(*allocated, pgno)
		page := store.pageMut(pgno)
		for j := range page {
			page[j] = 0
		}
		putU32(page, PageHeaderSize, leafPgnos[childIdx])
		sepIdx := childIdx
		for i := 0; i < count-1; i++ {
			off := PageHeaderSize + 4 + i*(sepWidth+4)
			putU32(page, off, seps[sepIdx])
			putU32(page, off+sepWidth, leafPgnos[childIdx+i+1])
			sepIdx++
		}
		writeHeader(page, PageTypeScopeBranch, uint16(count-1), pgno)
		branchPgnos = append(branchPgnos, pgno)
		childIdx += count
	}

	return buildBranchLevels(allocated, store, branchPgnos, seps, sepWidth, branchMax)
}

// buildBranchLevels recursively builds branch levels until a single root remains.
// This removes the old single-level 7635-leaf limit (fixes #6).
func buildBranchLevels(allocated *[]uint32, store pageStore, children []uint32, allSeps []uint32, sepWidth, branchMax int) (uint32, error) {
	if len(children) == 1 {
		return children[0], nil
	}

	// Build one level of branches.
	var branchPgnos []uint32
	var newSeps []uint32
	childIdx := 0
	sepIdx := 0

	for childIdx < len(children) {
		remaining := len(children) - childIdx
		count := remaining
		if count > branchMax {
			count = branchMax
		}
		pgno, err := store.allocPage()
		if err != nil {
			return 0, err
		}
		*allocated = append(*allocated, pgno)
		page := store.pageMut(pgno)
		for j := range page {
			page[j] = 0
		}
		writeHeader(page, PageTypeScopeBranch, uint16(count-1), pgno)
		putU32(page, PageHeaderSize, children[childIdx])
		for i := 0; i < count-1; i++ {
			off := PageHeaderSize + 4 + i*(sepWidth+4)
			if sepIdx < len(allSeps) {
				putU32(page, off, allSeps[sepIdx])
			}
			putU32(page, off+sepWidth, children[childIdx+i+1])
			sepIdx++
		}
		branchPgnos = append(branchPgnos, pgno)
		childIdx += count
	}

	// Separators for the next level: first separator stored in each branch
	// after the first is the boundary between subtrees.
	for i := 1; i < len(branchPgnos); i++ {
		page := store.page(branchPgnos[i])
		newSeps = append(newSeps, u32le(page, PageHeaderSize+4))
	}

	if len(branchPgnos) == 1 {
		return branchPgnos[0], nil
	}
	return buildBranchLevels(allocated, store, branchPgnos, newSeps, sepWidth, branchMax)
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
			entry := decodeScopeEntry(page[recOff : recOff+ScopeEntrySize])
			*out = append(*out, entry)
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
					entry := decodeScopeEntry(page[recOff : recOff+ScopeEntrySize])
					return entry.Bitmap
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
