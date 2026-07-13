package iprangedb

import "fmt"

// Reader is a zero-copy view over a committed v4.3 image.
//
// Open performs the cheap O(1) validation (per-meta CRC32C, meta classify, and
// geometry) so a torn or grossly malformed file is rejected before any tree
// access. It does NOT walk the B+tree — call Validate for the full §9 structural
// walk + per-page CRC when the input is untrusted. lookup/scan are panic-safe by
// construction even on a corrupt tree (page bounds-checked, entry counts clamped
// to the page capacity): they return a miss/error rather than reading OOB.
type Reader struct {
	bytes []byte
	meta  meta
}

// Open constructs a Reader over a committed byte image.
//
// It selects the active meta via per-meta CRC32C (torn-write detection, §5.1),
// classifies each candidate (magic, version, page_size, checksum_algo, flags,
// meta_size, key_width, record_size, reserved tail), and checks geometry (§9
// step 2). The tree is NOT walked; call Validate for the full structural pass.
func Open(bytes []byte) (*Reader, error) {
	m, err := selectActiveMeta(bytes)
	if err != nil {
		return nil, err
	}
	if err := checkGeometry(bytes, m); err != nil {
		return nil, err
	}
	return &Reader{bytes: bytes, meta: m}, nil
}

// Validate performs the full §9 structural walk: re-verifies both meta CRC32Cs,
// checks the meta reserved tails, then walks every reachable IP-tree page
// verifying per-page CRC32C, page type at each depth, entry-count bounds,
// monotonically increasing separators, sorted/non-overlapping records, child
// page numbers in range and pairwise distinct, zero reserved tails, and a
// record_count tally. Call this when the input is untrusted. On a trusted
// (daemon-written) file it is a no-op success.
func (r *Reader) Validate() error {
	// Meta CRCs (re-checked here even though Open verified the active one).
	if !verifyPage(r.bytes[:PageSize]) || !verifyPage(r.bytes[PageSize:2*PageSize]) {
		return errf("checksum", "meta page")
	}
	for p := 0; p < 2; p++ {
		page := r.bytes[p*PageSize : (p+1)*PageSize]
		m := decodeMeta(page)
		for _, b := range page[m.metaSize:] {
			if b != 0 {
				return errf("reserved", "meta tail")
			}
		}
	}
	if err := r.validateTree(); err != nil {
		return err
	}
	return r.validateScopeTable()
}

func (r *Reader) RecordCount() uint64 { return r.meta.recordCount }
func (r *Reader) ScopeMode() uint8    { return r.meta.scopeMode }
func (r *Reader) KeyWidth() uint8     { return r.meta.keyWidth }

// page returns the pgno-th page slice, or nil if pgno is out of range. All
// callers treat nil as "missing page" (lookup returns a miss; scan/validate
// return an error) so that a corrupt tree never causes an out-of-bounds read.
func (r *Reader) page(pgno uint32) []byte {
	if uint64(pgno) >= r.meta.totalPages {
		return nil
	}
	off := int(pgno) * PageSize
	if off+PageSize > len(r.bytes) {
		return nil
	}
	return r.bytes[off : off+PageSize]
}

// LookupV4 finds the scope_id covering ip (IPv4). Returns (scope_id, true) or (0, false).
func (r *Reader) LookupV4(ip Ipv4Key) (uint32, bool) {
	return r.lookup(ip, 4)
}

// LookupV6 finds the scope_id covering ip (IPv6).
func (r *Reader) LookupV6(ip Ipv6Key) (uint32, bool) {
	return r.lookup(ip, 16)
}

func (r *Reader) lookup(key interface{}, kw int) (uint32, bool) {
	if r.meta.rootPgno == 0 {
		return 0, false
	}
	pgno := r.meta.rootPgno
	for depth := uint32(0); depth < r.meta.treeHeight-1; depth++ {
		page := r.page(pgno)
		if page == nil {
			return 0, false
		}
		h := decodeHeader(page)
		count := min(int(h.entryCount), branchMax(r.meta.keyWidth))
		bv := newBranchView(page, count, kw)
		idx := branchFindChildInterface(bv, key, kw)
		pgno = bv.child(idx)
	}
	page := r.page(pgno)
	if page == nil {
		return 0, false
	}
	h := decodeHeader(page)
	count := min(int(h.entryCount), leafMax(r.meta.keyWidth))
	lv := newLeafView(page, count, kw)
	for i := 0; i < lv.len(); i++ {
		from := readKeyInterface(lv.recordFrom(i), kw)
		to := readKeyInterface(lv.recordTo(i), kw)
		if cmpInterface(from, key) <= 0 && cmpInterface(key, to) <= 0 {
			return lv.recordScopeID(i), true
		}
		if cmpInterface(from, key) > 0 {
			break
		}
	}
	return 0, false
}

// ScanV4 iterates all IPv4 records in order.
func (r *Reader) ScanV4(f func(from, to Ipv4Key, scopeID uint32)) error {
	return r.scan(4, func(fromLE, toLE []byte, scopeID uint32) {
		f(Ipv4Key(0).readLE(fromLE), Ipv4Key(0).readLE(toLE), scopeID)
	})
}

// ScanV6 iterates all IPv6 records in order.
func (r *Reader) ScanV6(f func(from, to Ipv6Key, scopeID uint32)) error {
	return r.scan(16, func(fromLE, toLE []byte, scopeID uint32) {
		f(Ipv6Key{}.readLE(fromLE), Ipv6Key{}.readLE(toLE), scopeID)
	})
}

func (r *Reader) scan(kw int, f func(fromLE, toLE []byte, scopeID uint32)) error {
	if r.meta.rootPgno == 0 {
		return nil
	}
	return r.scanNode(r.meta.rootPgno, kw, f)
}

// ScopeResolve resolves a scope_id to its interned bitmap (mode 2 / indirect
// only). Returns nil if the file is not in indirect mode, has no scope table,
// or the scope_id is not present. The bitmap is the bitset of feeds that cover
// the scope (fixes #7).
func (r *Reader) ScopeResolve(scopeID uint32) []byte {
	if r.meta.scopeMode != ScopeModeIndirect {
		return nil
	}
	if r.meta.scopeTableRoot == 0 {
		return nil
	}
	return findScope(r.bytes, r.meta.scopeTableRoot, scopeID)
}

func (r *Reader) scanNode(pgno uint32, kw int, f func([]byte, []byte, uint32)) error {
	page := r.page(pgno)
	if page == nil {
		return errf("structural", "page out of bounds")
	}
	h := decodeHeader(page)
	switch h.pageType {
	case PageTypeLeaf:
		count := min(int(h.entryCount), leafMax(r.meta.keyWidth))
		lv := newLeafView(page, count, kw)
		for i := 0; i < lv.len(); i++ {
			f(lv.recordFrom(i), lv.recordTo(i), lv.recordScopeID(i))
		}
		return nil
	case PageTypeBranch:
		count := min(int(h.entryCount), branchMax(r.meta.keyWidth))
		bv := newBranchView(page, count, kw)
		for j := 0; j < bv.childCount(); j++ {
			if err := r.scanNode(bv.child(j), kw, f); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("structural: unexpected page type %d", h.pageType)
	}
}

// --- helpers for interface{} key comparison ---

func branchFindChildInterface(bv branchView, key interface{}, kw int) int {
	lo, hi := 0, bv.sepCount
	for lo < hi {
		mid := lo + (hi-lo)/2
		sep := readKeyInterface(bv.sep(mid), kw)
		if cmpInterface(sep, key) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func readKeyInterface(b []byte, kw int) interface{} {
	if kw == 4 {
		return Ipv4Key(u32le(b, 0))
	}
	return Ipv6Key{Hi: u64le(b, 0), Lo: u64le(b, 8)}
}

func cmpInterface(a, b interface{}) int {
	switch av := a.(type) {
	case Ipv4Key:
		bv := b.(Ipv4Key)
		return av.cmp(bv)
	case Ipv6Key:
		bv := b.(Ipv6Key)
		return av.cmp(bv)
	}
	return 0
}

// versionFromFlag inverts flags bit0 → IP family.
func versionFromFlag(flags uint8) IPVersion {
	if flags&FlagIPVersion != 0 {
		return V6
	}
	return V4
}

// --- active-meta selection (§5.1 bootstrap, with per-meta CRC) ---

// selectActiveMeta reads both 4096-byte candidates independently, classifies
// each (CRC + magic + structural fields), and picks the active one. A torn /
// not-a-meta candidate is discarded; an intact-but-incompatible candidate
// rejects the whole file; among the valid candidates the higher txn_id wins
// (tie → pgno 0). Both valid candidates MUST agree on the static identity
// region (except version_minor/meta_size, which may differ during an upgrade).
func selectActiveMeta(bytes []byte) (meta, error) {
	if len(bytes) < 2*PageSize {
		return meta{}, errf("filesize", "image too small")
	}
	a, okA, errA := classify(bytes[:PageSize], 0)
	if errA != nil {
		return meta{}, errA
	}
	b, okB, errB := classify(bytes[PageSize:2*PageSize], 1)
	if errB != nil {
		return meta{}, errB
	}
	if !okA && !okB {
		return meta{}, errf("structural", "no valid meta page")
	}
	if okA && !okB {
		return a, nil
	}
	if !okA {
		return b, nil
	}
	// Both valid: the static identity region [16,26) ∪ [30,50) MUST match
	// (version_minor at 26 and meta_size at 28 may legitimately differ during a
	// v4.0→v4.x in-place upgrade, so they are excluded).
	loA := bytes[MetaStaticStart:MetaVersionMinor]
	loB := bytes[PageSize+MetaStaticStart : PageSize+MetaVersionMinor]
	hiA := bytes[MetaPageSize:MetaStaticEnd]
	hiB := bytes[PageSize+MetaPageSize : PageSize+MetaStaticEnd]
	if !bytesEqual(loA, loB) || !bytesEqual(hiA, hiB) {
		return meta{}, errf("structural", "metas disagree on static identity")
	}
	if b.txnID > a.txnID {
		return b, nil
	}
	return a, nil
}

// classify one meta candidate. Returns (meta, true, nil) for a valid meta;
// (meta{}, false, nil) for a torn/not-a-meta candidate (discarded, never
// rejects the file by itself); a non-nil error for an intact-but-incompatible
// candidate (fail closed).
func classify(page []byte, expectedPgno uint32) (meta, bool, error) {
	// Class 1: torn / not a meta — CRC fail or wrong magic/header.
	if !verifyPage(page) {
		return meta{}, false, nil
	}
	if string(readMagic(page)) != Magic {
		return meta{}, false, nil
	}
	h := decodeHeader(page)
	if h.pageType != PageTypeMeta || h.reserved != 0 || h.entryCount != 0 || h.pgno != expectedPgno {
		return meta{}, false, nil
	}
	// Class 2: a genuine, undamaged v4 meta that is incompatible/malformed.
	if readVersionMajor(page) != VersionMajor {
		return meta{}, false, errf("incompatible", "unsupported version major")
	}
	m := decodeMeta(page)
	if m.pageSize != PageSize {
		return meta{}, false, errf("incompatible", "page_size")
	}
	if m.checksumAlgo != ChecksumAlgoCRC32C {
		return meta{}, false, errf("incompatible", "checksum_algo")
	}
	if m.flags&^uint8(FlagIPVersion) != 0 {
		return meta{}, false, errf("incompatible", "unknown flags bit")
	}
	if m.metaSize < MetaSize || int(m.metaSize) > PageSize {
		return meta{}, false, errf("meta", "bad meta_size")
	}
	if m.keyWidth != versionFromFlag(m.flags).KeyWidth() {
		return meta{}, false, errf("structural", "key_width disagrees with flags")
	}
	if m.recordSize != recordSize(m.keyWidth) {
		return meta{}, false, errf("structural", "record_size mismatch")
	}
	for _, b := range page[m.metaSize:] {
		if b != 0 {
			return meta{}, false, errf("reserved", "meta tail")
		}
	}
	return m, true, nil
}

// checkGeometry validates the page-count, file size, height, and root fields
// (§9 step 2). total_pages was already bounded to < 2^32 by selectActiveMeta's
// caller expectations; here we enforce the file-size contract and tree shape.
func checkGeometry(bytes []byte, m meta) error {
	if m.totalPages < 2 || m.totalPages >= (1<<32) {
		return errf("structural", "total_pages out of range")
	}
	needed := m.totalPages * PageSize // total_pages < 2^32, PageSize=2^12 ⇒ < 2^44, no overflow
	have := uint64(len(bytes))
	if have%PageSize != 0 {
		return errf("filesize", "file size not page-aligned")
	}
	if have < needed {
		return errf("filesize", "file too short")
	}
	if m.treeHeight > TreeHeightMax {
		return errf("structural", "tree_height > max")
	}
	if (m.treeHeight == 0) != (m.rootPgno == 0) {
		return errf("structural", "tree_height/root_pgno inconsistent")
	}
	if m.rootPgno != 0 && (uint64(m.rootPgno) < 2 || uint64(m.rootPgno) >= m.totalPages) {
		return errf("structural", "root_pgno out of range")
	}
	return nil
}

// --- full structural validation (§9 step 4) ---

// walkState threads the largest `to` seen across the whole in-order leaf walk
// (global cross-leaf disjointness) and accumulates the leaf record count.
type walkState[K ipKey[K]] struct {
	prevTo  K
	hasPrev bool
	count   uint64
}

func (r *Reader) validateTree() error {
	if r.meta.rootPgno == 0 {
		if r.meta.recordCount != 0 {
			return errf("invariant", "record_count nonzero for empty tree")
		}
		return nil
	}
	var count uint64
	switch r.meta.keyWidth {
	case 4:
		var z Ipv4Key
		st := &walkState[Ipv4Key]{}
		if err := validateNode[Ipv4Key](r, r.meta.rootPgno, 1, z.minKey(), z.maxKey(), st); err != nil {
			return err
		}
		count = st.count
	case 16:
		var z Ipv6Key
		st := &walkState[Ipv6Key]{}
		if err := validateNode[Ipv6Key](r, r.meta.rootPgno, 1, z.minKey(), z.maxKey(), st); err != nil {
			return err
		}
		count = st.count
	default:
		return errf("structural", "unknown key_width")
	}
	if count != r.meta.recordCount {
		return errf("invariant", "record_count mismatch")
	}
	return nil
}

// validateScopeTable walks the scope table (mode 2) if present. The scope-table
// readers (findScope/readScopeNode) are already bounds-safe and depth-bounded;
// here we ensure the committed table is walkable. KV-tree validation is deferred
// to Phase 4c.
func (r *Reader) validateScopeTable() error {
	if r.meta.scopeTableRoot == 0 {
		return nil
	}
	if _, err := readAllScopes(r.bytes, r.meta.scopeTableRoot); err != nil {
		return errf("structural", "scope table validation failed")
	}
	return nil
}

// validateNode is the recursive structural walk (§9 step 4). lo/hi are the
// inherited inclusive key bound; st threads cross-leaf disjointness and the
// record tally. It is panic-safe: page() bounds-checks every access and
// entry counts are validated before any record/separator is read.
func validateNode[K ipKey[K]](r *Reader, pgno uint32, depth uint32, lo, hi K, st *walkState[K]) error {
	if depth > r.meta.treeHeight {
		return errf("invariant", "path deeper than tree_height")
	}
	page := r.page(pgno)
	if page == nil {
		return errf("structural", "page out of bounds")
	}
	if !verifyPage(page) {
		return errf("checksum", "reachable page")
	}
	h := decodeHeader(page)
	if h.reserved != 0 {
		return errf("reserved", "page header reserved")
	}
	if h.pgno != pgno {
		return errf("structural", "page self-pgno mismatch")
	}
	kw := int(r.meta.keyWidth)

	if depth == r.meta.treeHeight {
		// Leaf level: page MUST be a leaf.
		if h.pageType != PageTypeLeaf {
			return errf("structural", "expected leaf at tree_height")
		}
		rc := int(h.entryCount)
		if rc < 1 || rc > leafMax(r.meta.keyWidth) {
			return errf("invariant", "leaf entry_count out of range")
		}
		lv := newLeafView(page, rc, kw)
		for _, b := range page[PageHeaderSize+lv.bodyLen():] {
			if b != 0 {
				return errf("reserved", "leaf tail")
			}
		}
		firstFrom := readKey[K](lv.recordFrom(0))
		if st.hasPrev && st.prevTo.cmp(firstFrom) >= 0 {
			return errf("invariant", "cross-leaf overlap")
		}
		var prevTo K
		havePrev := false
		for i := 0; i < rc; i++ {
			from := readKey[K](lv.recordFrom(i))
			to := readKey[K](lv.recordTo(i))
			if to.cmp(from) < 0 {
				return errf("invariant", "record to < from")
			}
			if from.cmp(lo) < 0 || to.cmp(hi) > 0 {
				return errf("invariant", "record outside node bound")
			}
			if havePrev && from.cmp(prevTo) <= 0 {
				return errf("invariant", "leaf records not sorted/disjoint")
			}
			prevTo = to
			havePrev = true
		}
		st.prevTo = prevTo
		st.hasPrev = true
		st.count += uint64(rc)
		return nil
	}

	// Interior level: page MUST be a branch.
	if h.pageType != PageTypeBranch {
		return errf("structural", "expected branch above tree_height")
	}
	s := int(h.entryCount)
	if s < 1 || s > branchMax(r.meta.keyWidth) {
		return errf("invariant", "branch separator count out of range")
	}
	bv := newBranchView(page, s, kw)
	for _, b := range page[PageHeaderSize+bv.bodyLen():] {
		if b != 0 {
			return errf("reserved", "branch tail")
		}
	}
	// Separators strictly increasing, within (lo, hi].
	var prevSep K
	havePrev := false
	for i := 0; i < s; i++ {
		sep := readKey[K](bv.sep(i))
		if sep.cmp(lo) <= 0 {
			return errf("invariant", "separator <= lo")
		}
		if sep.cmp(hi) > 0 {
			return errf("invariant", "separator > hi")
		}
		if havePrev && sep.cmp(prevSep) <= 0 {
			return errf("invariant", "separators not strictly increasing")
		}
		prevSep = sep
		havePrev = true
	}
	// Children in [2, total_pages) and pairwise distinct.
	cc := bv.childCount()
	for j := 0; j < cc; j++ {
		cj := bv.child(j)
		if uint64(cj) < 2 || uint64(cj) >= r.meta.totalPages {
			return errf("structural", "child pgno out of range")
		}
		for k := j + 1; k < cc; k++ {
			if bv.child(k) == cj {
				return errf("structural", "duplicate child pgno")
			}
		}
	}
	// Recurse with inherited bounds: child[0]=[lo, sep[0]-1]; child[i]=[sep[i-1],
	// sep[i]-1]; child[s]=[sep[s-1], hi].
	lower := lo
	for i := 0; i < s; i++ {
		sep := readKey[K](bv.sep(i))
		upper, ok := sep.checkedDec()
		if !ok {
			return errf("invariant", "separator has no predecessor")
		}
		if err := validateNode[K](r, bv.child(i), depth+1, lower, upper, st); err != nil {
			return err
		}
		lower = sep
	}
	return validateNode[K](r, bv.child(s), depth+1, lower, hi, st)
}
