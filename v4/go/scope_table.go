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

// ScopeRegistry maintains scope_id → bitmap mappings WITHOUT materializing the
// committed scope table into a heap HashMap at open time.
//
// Design (issue-1/issue-2 fix):
//   - The committed scope table stays on disk (a B+tree keyed by scope_id).
//     resolve() descends that tree via findScope → O(log S), zero heap.
//   - Only THIS transaction's newly-interned entries live in RAM (newEntries).
//   - intern() dedups against the new set (O(1)); against the committed set it
//     lazily builds a bitmap→scope_id index once (committedIndex), then O(1).
//     The index materializes only when an intern actually misses the new set
//     AND there is committed data — so read/record-write workloads never pay
//     the heap cost that used to be paid eagerly at open.
//
// The registry is the single source of truth for the committed scope-tree root.
type ScopeRegistry struct {
	// This transaction's not-yet-committed additions.
	newEntries     []ScopeEntry
	newBitmapIndex map[string]uint32 // O(1) bitmap → scope_id for new entries

	// committedRoot is the on-disk scope-table B+tree root (0 = none).
	committedRoot uint32

	// committedIndex is the lazily-built bitmap → scope_id map over the
	// committed table. nil means "not needed yet".
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

// ensureCommittedIndex lazily builds the committed bitmap→scope_id index by
// reading the committed scope tree once. O(S) one-time; idempotent.
func (r *ScopeRegistry) ensureCommittedIndex(committedBytes []byte) {
	if r.committedIndex != nil || r.committedRoot == 0 {
		return
	}
	entries, err := readAllScopes(committedBytes, r.committedRoot)
	if err != nil {
		return
	}
	idx := make(map[string]uint32, len(entries))
	for _, e := range entries {
		idx[string(e.Bitmap)] = e.ScopeID
	}
	r.committedIndex = idx
}

// Intern finds or creates a scope_id for the given bitmap.
// committedBytes is the committed page image (for lazy dedup against the
// on-disk table). Returns (scope_id, was_new).
func (r *ScopeRegistry) Intern(bitmap []byte, committedBytes []byte) (uint32, bool) {
	if id, ok := r.newBitmapIndex[string(bitmap)]; ok {
		return id, false
	}
	if r.committedRoot != 0 && r.committedIndex == nil {
		r.ensureCommittedIndex(committedBytes)
	}
	if r.committedIndex != nil {
		if id, ok := r.committedIndex[string(bitmap)]; ok {
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

// BitmapSetFeed sets a feed bit. Returns the new scope_id.
func (r *ScopeRegistry) BitmapSetFeed(scopeID uint32, feedBit uint32, committedBytes []byte) uint32 {
	bm := r.Resolve(scopeID, committedBytes)
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
	id, _ := r.Intern(newBm, committedBytes)
	return id
}

// BitmapClearFeed clears a feed bit. Returns the new scope_id (0 if empty).
func (r *ScopeRegistry) BitmapClearFeed(scopeID uint32, feedBit uint32, committedBytes []byte) uint32 {
	bm := r.Resolve(scopeID, committedBytes)
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
	id, _ := r.Intern(newBm, committedBytes)
	return id
}

// EntriesForCommit returns the full entry list (committed ∪ new) for the
// bulk rebuild at commit, and warms the committed index from the read.
func (r *ScopeRegistry) EntriesForCommit(committedBytes []byte) []ScopeEntry {
	var all []ScopeEntry
	if r.committedRoot != 0 {
		committed, err := readAllScopes(committedBytes, r.committedRoot)
		if err == nil {
			all = committed
			if r.committedIndex == nil {
				idx := make(map[string]uint32, len(committed))
				for _, e := range committed {
					idx[string(e.Bitmap)] = e.ScopeID
				}
				r.committedIndex = idx
			}
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
	var seps []uint32

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

	return buildBranchLevels(allocated, freePool, store, branchPgnos, seps, sepWidth, branchMax)
}

// buildBranchLevels recursively builds branch levels until a single root remains.
// This removes the old single-level 7635-leaf limit (fixes #6).
func buildBranchLevels(allocated *[]uint32, freePool *[]uint32, store pageStore, children []uint32, allSeps []uint32, sepWidth, branchMax int) (uint32, error) {
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
	return buildBranchLevels(allocated, freePool, store, branchPgnos, newSeps, sepWidth, branchMax)
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
			entry := decodeScopeEntry(page[recOff : recOff+ScopeEntrySize])
			*out = append(*out, entry)
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
// its per-page CRC32C WITHOUT materializing entries. O(S pages) time, O(log S)
// heap (descent stack only). Preserves the open-time corruption guard that the
// old eager readAllScopesChecked provided, while fixing the 256MB heap load.
func ValidateScopeCRC(bytes []byte, rootPgno uint32) error {
	if rootPgno == 0 {
		return nil
	}
	return validateScopeCRCNode(bytes, rootPgno, 0)
}

func validateScopeCRCNode(bytes []byte, pgno uint32, depth uint32) error {
	if depth > TreeHeightMax {
		return fmt.Errorf("scope table too deep")
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
	if h.pageType == PageTypeScopeBranch {
		bv := newBranchView(page, int(h.entryCount), int(ScopeKeyWidth))
		for j := 0; j < bv.childCount(); j++ {
			if err := validateScopeCRCNode(bytes, bv.child(j), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
