package iprangedb

import "fmt"

// Reader is a zero-copy view over a committed v4.3 image.
type Reader struct {
	bytes   []byte
	meta    meta
}

// Open constructs a Reader over a committed byte image.
func Open(bytes []byte) (*Reader, error) {
	if len(bytes) < 2*PageSize {
		return nil, fmt.Errorf("image too small")
	}
	metaA := decodeMeta(bytes[:PageSize])
	metaB := decodeMeta(bytes[PageSize : 2*PageSize])
	var active meta
	if metaA.txnID >= metaB.txnID {
		active = metaA
	} else {
		active = metaB
	}
	if string(bytes[MetaMagic:MetaMagic+8]) != Magic {
		return nil, fmt.Errorf("bad magic")
	}
	return &Reader{bytes: bytes, meta: active}, nil
}

func (r *Reader) RecordCount() uint64 {
	return r.meta.recordCount
}

func (r *Reader) ScopeMode() uint8 {
	return r.meta.scopeMode
}

func (r *Reader) KeyWidth() uint8 {
	return r.meta.keyWidth
}

func (r *Reader) page(pgno uint32) []byte {
	off := int(pgno) * PageSize
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
		h := decodeHeader(page)
		bv := newBranchView(page, int(h.entryCount), kw)
		idx := branchFindChildInterface(bv, key, kw)
		pgno = bv.child(idx)
	}
	// Leaf level
	page := r.page(pgno)
	h := decodeHeader(page)
	lv := newLeafView(page, int(h.entryCount), kw)
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
	entries, err := readAllScopes(r.bytes, r.meta.scopeTableRoot)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.ScopeID == scopeID {
			return e.Bitmap
		}
	}
	return nil
}

func (r *Reader) scanNode(pgno uint32, kw int, f func([]byte, []byte, uint32)) error {
	page := r.page(pgno)
	h := decodeHeader(page)
	switch h.pageType {
	case PageTypeLeaf:
		lv := newLeafView(page, int(h.entryCount), kw)
		for i := 0; i < lv.len(); i++ {
			f(lv.recordFrom(i), lv.recordTo(i), lv.recordScopeID(i))
		}
		return nil
	case PageTypeBranch:
		bv := newBranchView(page, int(h.entryCount), kw)
		for j := 0; j < bv.childCount(); j++ {
			if err := r.scanNode(bv.child(j), kw, f); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unexpected page type %d", h.pageType)
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
