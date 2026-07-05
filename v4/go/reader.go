package iprangedb

import "bytes"

// The v4 reader: open over an in-memory image, validate per §9, and query.
//
// Open selects the active meta (§5.1 bootstrap), checks geometry (§9 step 2), and fully
// validates the reachable tree before exposing any result (§9 step 4 — the default): a
// hostile but checksum-valid structure cannot leak a wrong answer, and a
// corrupt/truncated image is rejected, never panics, loops, or reads out of bounds.
// LookupV4/V6 and ScanV4/V6 then navigate the validated structure, returning the borrowed
// scope (zero-copy, D11).

// Reader is a read-only view over a validated v4 image. It holds no lock and no
// allocation; lookups and scans return slices borrowed from the underlying bytes.
type Reader struct {
	bytes      []byte
	meta       meta
	version    IPVersion
	recordSize int
	leafMax    int
	branchMax  int
}

// Open opens and fully validates a v4 image (the default, §9). It returns a typed error
// (exposing nothing) on any malformed/hostile input.
func Open(b []byte) (*Reader, error) {
	m, err := selectActiveMeta(b)
	if err != nil {
		return nil, err
	}
	// flags reserved bits were already rejected in classify; only bit0 remains.
	version := ipVersionFromFlagBit(m.flags)

	// Geometry (§9 step 2). page_size/key_width/record_size/meta_size were cross-checked
	// in classify; here: page-count, file size, height/root.
	if m.totalPages < 2 || m.totalPages >= (uint64(1)<<32) {
		return nil, errStructural("total_pages out of range")
	}
	// Overflow-checked total_pages*page_size.
	if m.totalPages > maxUint64/pageSize {
		return nil, errOverflow("total_pages*page_size")
	}
	needed := m.totalPages * pageSize
	have := uint64(len(b))
	if have%pageSize != 0 {
		return nil, errFileSizeMismatch(needed, have)
	}
	if have < needed {
		return nil, errFileTooShort(needed, have)
	}
	if m.treeHeight > treeHeightMax {
		return nil, errStructural("tree_height > 32")
	}
	if (m.treeHeight == 0) != (m.rootPgno == 0) {
		return nil, errStructural("tree_height/root_pgno inconsistent")
	}
	if m.rootPgno != 0 && (uint64(m.rootPgno) < 2 || uint64(m.rootPgno) >= m.totalPages) {
		return nil, errStructural("root_pgno out of range")
	}

	r := &Reader{
		bytes:      b,
		meta:       m,
		version:    version,
		recordSize: int(m.recordSize),
		leafMax:    leafMax(m.recordSize),
		branchMax:  branchMax(m.keyWidth),
	}
	if err := r.validateTree(); err != nil {
		return nil, err
	}
	if err := r.validateScopeTableTree(); err != nil {
		return nil, err
	}
	return r, nil
}

// validateScopeTableTree validates the v4.1 metadata (§C.5) before exposing the reader: the
// scope table, then every scope's per-scope KV tree (incl. its overflow chains). It
// range-checks every kv_root/child/overflow pgno, enforces TREE_HEIGHT_MAX, checks sorted +
// disjoint keys, page-type + self-pgno + CRC32C, overflow read-by-count, and rejects type == 0
// values that are not valid UTF-8 over the whole reassembled value. A v4.0 file has
// scope_table_root == 0 (no metadata) and this is a no-op.
func (r *Reader) validateScopeTableTree() error {
	root := r.meta.scopeTableRoot
	if root == 0 {
		return nil
	}
	// F2: a single file-wide visitor threads through the scope table AND every per-scope KV
	// tree AND every overflow chain. The first visit marks a page; any second visit (a shared
	// overflow chain, a duplicate child pgno, or any other aliasing) is a structural error.
	// This proves the metadata page-forest disjoint + acyclic and subsumes the per-node
	// duplicate-child check. total_pages was bounded by Open (>= 2, < 2^32, file-backed).
	vis := &pageVisitor{visited: make([]bool, r.meta.totalPages)}
	if err := validateScopeTable(r.bytes, root, r.meta.totalPages, vis); err != nil {
		return err
	}
	// Each scope record's kv_root is a separate B+tree (§C.4). The scope table is now
	// validated, so loading the records is bounded and safe.
	recs, err := loadAllScopes(r.bytes, root)
	if err != nil {
		return err
	}
	for i := range recs {
		if err := validateKV(r.bytes, recs[i].kvRoot, r.meta.totalPages, vis); err != nil {
			return err
		}
	}
	return nil
}

// Version returns the file's IP family.
func (r *Reader) Version() IPVersion { return r.version }

// ScopeWidth returns the fixed per-record scope width in bytes; 0 = presence map (§4).
func (r *Reader) ScopeWidth() int { return int(r.meta.scopeWidth) }

// RecordCount returns the exact record count (verified against the tree during Open).
func (r *Reader) RecordCount() uint64 { return r.meta.recordCount }

// IsEmpty reports whether the tree is empty (root_pgno == 0).
func (r *Reader) IsEmpty() bool { return r.meta.rootPgno == 0 }

// LookupV4 returns the scope of the range covering ip, and whether it was found. Error if
// the file is not IPv4.
func (r *Reader) LookupV4(ip Ipv4Key) ([]byte, bool, error) {
	if r.version != V4 {
		return nil, false, errInvalidInput("lookup key family mismatch")
	}
	scope, ok := readerLookup[Ipv4Key](r, ip)
	return scope, ok, nil
}

// LookupV6 returns the scope of the range covering ip, and whether it was found. Error if
// the file is not IPv6.
func (r *Reader) LookupV6(ip Ipv6Key) ([]byte, bool, error) {
	if r.version != V6 {
		return nil, false, errInvalidInput("lookup key family mismatch")
	}
	scope, ok := readerLookup[Ipv6Key](r, ip)
	return scope, ok, nil
}

// ScanV4 calls f(from, to, scope) for every record in key order. Error if not IPv4.
func (r *Reader) ScanV4(f func(from, to Ipv4Key, scope []byte)) error {
	if r.version != V4 {
		return errInvalidInput("scan key family mismatch")
	}
	if r.meta.rootPgno != 0 {
		readerScanNode[Ipv4Key](r, r.meta.rootPgno, 1, f)
	}
	return nil
}

// ScanV6 calls f(from, to, scope) for every record in key order. Error if not IPv6.
func (r *Reader) ScanV6(f func(from, to Ipv6Key, scope []byte)) error {
	if r.version != V6 {
		return errInvalidInput("scan key family mismatch")
	}
	if r.meta.rootPgno != 0 {
		readerScanNode[Ipv6Key](r, r.meta.rootPgno, 1, f)
	}
	return nil
}

// --- v4.1 metadata reads (§C.2/§C.4) ---
//
// These mirror the Writer's metadata getters but descend the on-disk committed scope table
// and per-scope KV trees (validated at Open), so a read-only shared-lock consumer reads a
// self-describing file's metadata. A v4.0 image (scope_table_root == 0) returns the empty/
// not-found result everywhere. All reads go through the bounds-safe byte views.

// scopeByID resolves one scope record from the committed scope tree, ignoring an error (the
// tree was validated at Open, so a descent error means not-found from a caller's view).
func (r *Reader) scopeByID(id uint32) (scopeRec, bool) {
	rec, found, err := findScopeByID(r.bytes, r.meta.scopeTableRoot, r.meta.totalPages, id)
	if err != nil {
		return scopeRec{}, false
	}
	return rec, found
}

// ScopeName returns the scope's name (UTF-8 bytes, a copy) and whether it exists. A missing
// scope returns (nil, false). Mirrors Writer.ScopeName.
func (r *Reader) ScopeName(scopeID uint32) ([]byte, bool) {
	if scopeID == fileScopeID {
		return nil, false
	}
	if rec, ok := r.scopeByID(scopeID); ok {
		return cloneBytes(rec.name), true
	}
	return nil, false
}

// ScopeList returns all defined scopes as (id, name), ascending by scope_id. The FILE target
// (scope_id 0) is a dataset-metadata target, not a defined scope, so it is excluded (§C.2).
// Mirrors Writer.ScopeList.
func (r *Reader) ScopeList() []ScopeEntry {
	recs, err := loadAllScopes(r.bytes, r.meta.scopeTableRoot)
	if err != nil {
		return nil
	}
	out := make([]ScopeEntry, 0, len(recs))
	for i := range recs {
		if recs[i].id == fileScopeID {
			continue
		}
		out = append(out, ScopeEntry{ID: recs[i].id, Name: cloneBytes(recs[i].name)})
	}
	return out
}

// ScopeVersion returns the scope's version and whether it exists. Mirrors Writer.ScopeVersion.
func (r *Reader) ScopeVersion(scopeID uint32) (uint64, bool) {
	if scopeID == fileScopeID {
		return 0, false
	}
	if rec, ok := r.scopeByID(scopeID); ok {
		return rec.version, true
	}
	return 0, false
}

// ScopeType returns the scope's opaque type byte and whether it exists. Mirrors Writer.ScopeType.
func (r *Reader) ScopeType(scopeID uint32) (uint8, bool) {
	if scopeID == fileScopeID {
		return 0, false
	}
	if rec, ok := r.scopeByID(scopeID); ok {
		return rec.typ, true
	}
	return 0, false
}

// targetKVRoot returns the committed kv_root of target and whether the target has a record.
// FILE (scope_id 0) is looked up like any scope record. Mirrors Writer.targetKVRoot.
func (r *Reader) targetKVRoot(target uint32) (uint32, bool) {
	rec, ok := r.scopeByID(target)
	if !ok {
		return 0, false
	}
	return rec.kvRoot, true
}

// MetaGet gets key on target as (type, value) (the whole reassembled value) and found=true, or
// found=false if absent (§C.7). target = scope_id, 0 = FILE. A non-existent target → found=false.
// Mirrors Writer.MetaGet, descending the committed KV tree.
func (r *Reader) MetaGet(target uint32, key []byte) (typ uint32, value []byte, found bool, err error) {
	if err := checkKey(key); err != nil {
		return 0, nil, false, err
	}
	root, ok := r.targetKVRoot(target)
	if !ok {
		return 0, nil, false, nil
	}
	return kvGet(r.bytes, root, key, r.meta.totalPages)
}

// MetaList lists every (key, type, value) on target, ordered by key (§C.4). A non-existent
// target → an empty list. Mirrors Writer.MetaList, descending the committed KV tree.
func (r *Reader) MetaList(target uint32) ([]MetaEntry, error) {
	var out []MetaEntry
	root, ok := r.targetKVRoot(target)
	if !ok {
		return out, nil
	}
	var entries []kvEntry
	if err := kvList(r.bytes, root, r.meta.totalPages, &entries); err != nil {
		return nil, err
	}
	out = make([]MetaEntry, 0, len(entries))
	for i := range entries {
		out = append(out, MetaEntry{Key: entries[i].key, Type: entries[i].typ, Value: entries[i].value})
	}
	return out, nil
}

// --- internals ---

// pageBytes returns the pgno-th page. pgno < total_pages and total_pages*page_size <=
// len(bytes) were checked in Open / the validate walk, so the slice is always in bounds.
func (r *Reader) pageBytes(pgno uint32) []byte {
	off := int(pgno) * pageSize
	return r.bytes[off : off+pageSize]
}

func readerLookup[K ipKey[K]](r *Reader, ip K) ([]byte, bool) {
	if r.meta.rootPgno == 0 {
		return nil, false
	}
	height := r.meta.treeHeight
	pgno := r.meta.rootPgno
	depth := uint32(1)
	for {
		page := r.pageBytes(pgno)
		count := int(decodePageHeader(page).entryCount)
		if depth == height {
			leaf := newLeafView[K](page, count, r.recordSize)
			return leafLookup(leaf, ip)
		}
		branch := newBranchView[K](page, count)
		pgno = branch.child(branchDescend(branch, ip))
		depth++
	}
}

func readerScanNode[K ipKey[K]](r *Reader, pgno, depth uint32, f func(from, to K, scope []byte)) {
	page := r.pageBytes(pgno)
	count := int(decodePageHeader(page).entryCount)
	if depth == r.meta.treeHeight {
		leaf := newLeafView[K](page, count, r.recordSize)
		for i := 0; i < leaf.len(); i++ {
			rec := leaf.record(i)
			f(rec.from(), rec.to(), rec.scope())
		}
		return
	}
	branch := newBranchView[K](page, count)
	for j := 0; j < branch.childCount(); j++ {
		readerScanNode[K](r, branch.child(j), depth+1, f)
	}
}

func (r *Reader) validateTree() error {
	if r.meta.rootPgno == 0 {
		// Empty tree: the full pass enforces the exact record_count (§9 step 5).
		if r.meta.recordCount != 0 {
			return errInvariant("record_count nonzero for empty tree")
		}
		return nil
	}
	var count uint64
	switch r.version {
	case V4:
		var prevTo Ipv4Key
		havePrev := false
		if err := validateNode[Ipv4Key](r, r.meta.rootPgno, 1, Ipv4Key(0).minKey(), Ipv4Key(0).maxKey(), &prevTo, &havePrev, &count); err != nil {
			return err
		}
	case V6:
		var prevTo Ipv6Key
		havePrev := false
		if err := validateNode[Ipv6Key](r, r.meta.rootPgno, 1, Ipv6Key{}.minKey(), Ipv6Key{}.maxKey(), &prevTo, &havePrev, &count); err != nil {
			return err
		}
	}
	if count != r.meta.recordCount {
		return errInvariant("record_count mismatch")
	}
	return nil
}

// validateNode is the recursive structural walk (§9 step 4). lo/hi are the inherited
// inclusive key bound; (prevTo, havePrev) thread the largest to seen so far across the
// whole in-order walk (global cross-leaf disjointness); count accumulates leaf records.
func validateNode[K ipKey[K]](r *Reader, pgno, depth uint32, lo, hi K, prevTo *K, havePrev *bool, count *uint64) error {
	// Cycle/DoS defense: a too-deep path (incl. any pgno cycle) exceeds tree_height.
	if depth > r.meta.treeHeight {
		return errInvariant("path deeper than tree_height")
	}
	page := r.pageBytes(pgno)
	if !verifyPage(page) {
		return errChecksumFailed("reachable page")
	}
	h := decodePageHeader(page)
	if h.reserved != 0 {
		return errNonZeroReserved("page header reserved")
	}
	if h.pgno != pgno {
		return errStructural("page self-pgno mismatch")
	}

	if depth == r.meta.treeHeight {
		// MUST be a leaf.
		if h.pageType != pageTypeLeaf {
			return errStructural("expected leaf at tree_height")
		}
		rc := int(h.entryCount)
		if rc < 1 || rc > r.leafMax {
			return errInvariant("leaf entry_count out of range")
		}
		leaf := newLeafView[K](page, rc, r.recordSize)
		// Tail after the records MUST be zero (full pass).
		for _, b := range page[pageHeaderSize+leaf.bodyLen():] {
			if b != 0 {
				return errNonZeroReserved("leaf tail")
			}
		}
		// Cross-leaf disjointness: prev_to < this leaf's first from.
		firstFrom := leaf.record(0).from()
		if *havePrev && (*prevTo).cmp(firstFrom) >= 0 {
			return errInvariant("cross-leaf overlap")
		}
		// Records sorted, disjoint, within [lo, hi].
		var prevRecTo K
		havePrevRec := false
		for i := 0; i < rc; i++ {
			rec := leaf.record(i)
			from, to := rec.from(), rec.to()
			if to.cmp(from) < 0 {
				return errInvariant("record to < from")
			}
			if from.cmp(lo) < 0 || to.cmp(hi) > 0 {
				return errInvariant("record outside node bound")
			}
			if havePrevRec && from.cmp(prevRecTo) <= 0 {
				return errInvariant("leaf records not sorted/disjoint")
			}
			prevRecTo = to
			havePrevRec = true
		}
		*prevTo = prevRecTo // last record's to
		*havePrev = true
		*count += uint64(rc)
		return nil
	}

	// MUST be a branch.
	if h.pageType != pageTypeBranch {
		return errStructural("expected branch above tree_height")
	}
	s := int(h.entryCount)
	if s < 1 || s > r.branchMax {
		return errInvariant("branch separator count out of range")
	}
	branch := newBranchView[K](page, s)
	for _, b := range page[pageHeaderSize+branch.bodyLen():] {
		if b != 0 {
			return errNonZeroReserved("branch tail")
		}
	}
	// Separators: lo < sep[0] < … < sep[s-1] <= hi (strictly increasing, in bound).
	var prevSep K
	havePrevSep := false
	for i := 0; i < s; i++ {
		sep := branch.sep(i)
		if sep.cmp(lo) <= 0 {
			return errInvariant("separator <= lo")
		}
		if sep.cmp(hi) > 0 {
			return errInvariant("separator > hi")
		}
		if havePrevSep && sep.cmp(prevSep) <= 0 {
			return errInvariant("separators not strictly increasing")
		}
		prevSep = sep
		havePrevSep = true
	}
	// Children in [2, total_pages) and pairwise distinct.
	childCount := branch.childCount()
	for j := 0; j < childCount; j++ {
		cj := branch.child(j)
		if uint64(cj) < 2 || uint64(cj) >= r.meta.totalPages {
			return errStructural("child pgno out of range")
		}
		for k := j + 1; k < childCount; k++ {
			if branch.child(k) == cj {
				return errStructural("duplicate child pgno")
			}
		}
	}
	// Recurse with inherited bounds: child[0]=[lo, sep[0]-1]; child[i]=[sep[i-1],
	// sep[i]-1]; child[s]=[sep[s-1], hi]. sep > lo >= family_min ⇒ sep-1 exists.
	lower := lo
	for i := 0; i < s; i++ {
		sep := branch.sep(i)
		upper, ok := sep.checkedDec()
		if !ok {
			return errInvariant("separator has no predecessor")
		}
		if err := validateNode[K](r, branch.child(i), depth+1, lower, upper, prevTo, havePrev, count); err != nil {
			return err
		}
		lower = sep
	}
	return validateNode[K](r, branch.child(s), depth+1, lower, hi, prevTo, havePrev, count)
}

// selectActiveMeta selects the active meta (§5.1 bootstrap). It reads both 4096-byte
// candidates independently; class 2 (intact-but-incompatible) on either rejects the file;
// class 1 (torn/not-a-meta) is discarded; among the valid metas the higher txn_id wins
// (tie → pgno 0). Both valid metas MUST agree on the static identity region.
func selectActiveMeta(b []byte) (meta, error) {
	if len(b) < 2*pageSize {
		return meta{}, errFileTooShort(2*pageSize, uint64(len(b)))
	}
	ma, aok, err := classify(b[:pageSize], 0)
	if err != nil {
		return meta{}, err
	}
	mb, bok, err := classify(b[pageSize:2*pageSize], 1)
	if err != nil {
		return meta{}, err
	}
	switch {
	case !aok && !bok:
		return meta{}, errStructural("no valid meta page")
	case aok && !bok:
		return ma, nil
	case !aok && bok:
		return mb, nil
	default:
		// Both valid metas MUST agree on the static identity region [16,50) — EXCEPT
		// version_minor (26) and meta_size (28): a v4.0→v4.1 in-place upgrade (§C.6) writes
		// the new minor/meta_size into one meta while the other still holds the old values,
		// so they legitimately differ there during the transition (the active/higher-txn_id
		// meta is authoritative, and each field is still CRC-protected). The rest of the
		// static identity must match byte-for-byte. Ranges: [16,26) and [30,50)
		// (metaPageSize == 30 is the field after meta_size).
		loA := b[metaStaticStart:metaVersionMinor]
		loB := b[pageSize+metaStaticStart : pageSize+metaVersionMinor]
		hiA := b[metaPageSize:metaStaticEnd]
		hiB := b[pageSize+metaPageSize : pageSize+metaStaticEnd]
		if !bytes.Equal(loA, loB) || !bytes.Equal(hiA, hiB) {
			return meta{}, errStructural("metas disagree on static identity")
		}
		// Higher txn_id active; on an (illegal) tie pick pgno 0 (== ma).
		if mb.txnID > ma.txnID {
			return mb, nil
		}
		return ma, nil
	}
}

// classify classifies one meta candidate (§5.1): (_, false, nil) = class 1 (torn/not-a-
// meta, discard), (_, _, err) = class 2 (intact but incompatible — fail closed),
// (m, true, nil) = class 3 (valid).
func classify(page []byte, expectedPgno uint32) (meta, bool, error) {
	// Class 1: torn / not a meta — discarded, never rejects the file by itself.
	if !verifyPage(page) {
		return meta{}, false, nil
	}
	if readMagic(page) != magic {
		return meta{}, false, nil
	}
	h := decodePageHeader(page)
	if h.pageType != pageTypeMeta || h.reserved != 0 || h.entryCount != 0 || h.pgno != expectedPgno {
		return meta{}, false, nil
	}

	// A genuine, undamaged v4 meta. Class 2: incompatible / malformed ⇒ fail closed.
	vMajor := readVersionMajor(page)
	if vMajor != versionMajor {
		return meta{}, false, errUnsupportedMajor(vMajor)
	}
	m := decodeMeta(page)
	if m.pageSize != pageSize {
		return meta{}, false, errIncompatible("page_size")
	}
	if m.checksumAlgo != checksumAlgoCRC32C {
		return meta{}, false, errIncompatible("checksum_algo")
	}
	if m.flags&^flagIPVersion != 0 {
		return meta{}, false, errIncompatible("unknown flags bit")
	}
	if m.metaSize < metaSize || int(m.metaSize) > pageSize {
		return meta{}, false, errBadMetaSize(m.metaSize)
	}
	if m.versionMinor == 0 && m.metaSize != metaSize {
		return meta{}, false, errBadMetaSize(m.metaSize)
	}
	// v4.1 declares exactly meta_size 94 (F7): pin it like the minor-0 rule. minor >= 2
	// keeps the >= 90 + tail-skip path, so a future minor declaring a larger meta_size
	// stays forward-compatible.
	if m.versionMinor == versionMinorMetadata && m.metaSize != metaSizeV41 {
		return meta{}, false, errBadMetaSize(m.metaSize)
	}
	expectKW := ipVersionFromFlagBit(m.flags).keyWidth()
	if m.keyWidth != expectKW {
		return meta{}, false, errStructural("key_width disagrees with flags")
	}
	if m.recordSize != recordSize(m.keyWidth, m.scopeWidth) {
		return meta{}, false, errStructural("record_size mismatch")
	}
	// The meta's reserved tail after its declared fields MUST be zero (§5/§9). Check the
	// FILE's metaSize (not the constant): a future minor declares a larger metaSize, so
	// this skips that minor's appended fields and only enforces the still-reserved region
	// beyond them — staying forward-compatible (§5.1).
	for _, b := range page[m.metaSize:] {
		if b != 0 {
			return meta{}, false, errNonZeroReserved("meta tail")
		}
	}
	return m, true, nil
}

// leafLookup binary-searches a leaf for the record covering ip: the record with greatest
// from <= ip, a hit iff ip <= to. Returns the borrowed scope.
func leafLookup[K ipKey[K]](leaf leafView[K], ip K) ([]byte, bool) {
	// First index whose from is > ip; the candidate is the one before it.
	lo, hi := 0, leaf.len()
	for lo < hi {
		mid := lo + (hi-lo)/2
		if leaf.record(mid).from().cmp(ip) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return nil, false
	}
	rec := leaf.record(lo - 1)
	if ip.cmp(rec.to()) <= 0 {
		return rec.scope(), true
	}
	return nil, false
}

// branchDescend returns the child index for ip (§5.2): the number of separators <= ip
// (binary search). child[i] covers [sep[i-1], sep[i]-1].
func branchDescend[K ipKey[K]](branch branchView[K], ip K) int {
	lo, hi := 0, branch.sepCountOf()
	for lo < hi {
		mid := lo + (hi-lo)/2
		if branch.sep(mid).cmp(ip) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
