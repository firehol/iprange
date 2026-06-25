package iprangedb

// Ordered cursor over a validated v4 image (§v4.1.A) and the standard SDK helpers built
// on it (§v4.1.B), mirroring the Rust reference (iprange-livedb/src/cursor.rs).
//
// The cursor is read-only: it navigates the structure that Open already validated, so it
// never reads out of bounds, never loops, never panics — it only walks a known-good tree.
// There are no leaf sibling pointers (D3), so the cursor keeps a root→leaf path stack (a
// fixed [treeHeightMax]frame, no per-step heap) and re-descends at leaf boundaries.
//
// Seek(key) positions at the successor — the first record with from >= key. Next/Prev step
// in key order; Current reads the positioned record. The struct is small and cheaply
// copyable, so the helpers here snapshot a position by copying it internally; CursorV4 /
// CursorV6 hand the caller a *Cursor.
//
// Helpers take a selector predicate func(scope) bool over the opaque scope bytes — the
// engine never interprets scope (D11) — and stream results to a visitor that returns an
// error: nil continues, Stop halts cleanly (the helper returns nil), any other error halts
// and is returned. query* emit; count* tally. CIDR output is the canonical minimal cover.

import (
	"errors"
	"math/bits"
)

// Stop is returned by a visitor to halt iteration cleanly; the helper then returns nil.
// Any other error from a visitor halts iteration and is returned by the helper.
var Stop = errors.New("iprangedb: stop iteration")

// frame is one level of the root→leaf path: the page and the chosen index within it (a
// child index in a branch, a record index in the leaf).
type frame struct {
	pgno uint32
	idx  uint32
}

// cursorState is where the cursor sits relative to the records, in key order.
type cursorState uint8

const (
	stEmpty       cursorState = iota // tree is empty (root_pgno == 0)
	stBeforeFirst                    // before the first record (Next → first; Prev → stays)
	stAt                             // positioned at a record (Current returns it)
	stAfterLast                      // past the last record (Prev → last; Next → stays)
)

// Cursor is an ordered cursor over a validated Reader (§v4.1.A). Construct with
// Reader.CursorV4 / Reader.CursorV6. The zero-value is invalid; the constructor sets it up.
type Cursor[K ipKey[K]] struct {
	r     *Reader
	path  [treeHeightMax]frame
	state cursorState
}

// CursorV4 returns a cursor over an IPv4 image (starts unpositioned: BeforeFirst, or Empty
// if the tree has no records). Error if the file is not IPv4.
func (r *Reader) CursorV4() (*Cursor[Ipv4Key], error) {
	if r.version != V4 {
		return nil, errInvalidInput("cursor key family mismatch")
	}
	return newCursor[Ipv4Key](r), nil
}

// CursorV6 returns a cursor over an IPv6 image. Error if the file is not IPv6.
func (r *Reader) CursorV6() (*Cursor[Ipv6Key], error) {
	if r.version != V6 {
		return nil, errInvalidInput("cursor key family mismatch")
	}
	return newCursor[Ipv6Key](r), nil
}

func newCursor[K ipKey[K]](r *Reader) *Cursor[K] {
	st := stBeforeFirst
	if r.meta.rootPgno == 0 {
		st = stEmpty
	}
	return &Cursor[K]{r: r, state: st}
}

// --- node access (over already-validated pages) ---

func (c *Cursor[K]) leaf(pgno uint32) leafView[K] {
	page := c.r.pageBytes(pgno)
	count := int(decodePageHeader(page).entryCount)
	return newLeafView[K](page, count, c.r.recordSize)
}

func (c *Cursor[K]) branch(pgno uint32) branchView[K] {
	page := c.r.pageBytes(pgno)
	count := int(decodePageHeader(page).entryCount)
	return newBranchView[K](page, count)
}

// leafLevel is the path index of the leaf (tree_height >= 1 when non-empty).
func (c *Cursor[K]) leafLevel() int { return int(c.r.meta.treeHeight) - 1 }

// --- positioning ---

// descendLeftmost fills path[level..=leafLevel] by taking the leftmost child to a leaf,
// positioning at its first record.
func (c *Cursor[K]) descendLeftmost(level int, pgno uint32) {
	leafLevel := c.leafLevel()
	for {
		if level == leafLevel {
			c.path[level] = frame{pgno: pgno, idx: 0}
			return
		}
		c.path[level] = frame{pgno: pgno, idx: 0}
		pgno = c.branch(pgno).child(0)
		level++
	}
}

// descendRightmost fills path[level..=leafLevel] by taking the rightmost child to a leaf,
// positioning at its last record.
func (c *Cursor[K]) descendRightmost(level int, pgno uint32) {
	leafLevel := c.leafLevel()
	for {
		if level == leafLevel {
			n := c.leaf(pgno).len()
			last := 0
			if n > 0 {
				last = n - 1
			}
			c.path[level] = frame{pgno: pgno, idx: uint32(last)}
			return
		}
		b := c.branch(pgno)
		last := b.childCount() - 1
		c.path[level] = frame{pgno: pgno, idx: uint32(last)}
		pgno = b.child(last)
		level++
	}
}

// First positions at the first record (key order). false if the tree is empty.
func (c *Cursor[K]) First() bool {
	if c.state == stEmpty {
		return false
	}
	c.descendLeftmost(0, c.r.meta.rootPgno)
	c.state = stAt
	return true
}

// Last positions at the last record (key order). false if the tree is empty.
func (c *Cursor[K]) Last() bool {
	if c.state == stEmpty {
		return false
	}
	c.descendRightmost(0, c.r.meta.rootPgno)
	c.state = stAt
	return true
}

// Seek positions at the successor: the first record with from >= key. Returns true if such
// a record exists (then Current yields it), false if key is past every record (the cursor
// becomes AfterLast) or the tree is empty.
//
// To get the record covering key (greatest from <= key, present iff key <= to): Seek(key),
// then if Current's from != key call Prev and check its to >= key.
func (c *Cursor[K]) Seek(key K) bool {
	if c.state == stEmpty {
		return false
	}
	leafLevel := c.leafLevel()
	level := 0
	pgno := c.r.meta.rootPgno
	for {
		if level == leafLevel {
			lf := c.leaf(pgno)
			idx := leafLowerBound[K](lf, key)
			if idx < lf.len() {
				c.path[level] = frame{pgno: pgno, idx: uint32(idx)}
				c.state = stAt
				return true
			}
			// No record >= key in this leaf; the successor (if any) is the first record of
			// the next leaf. Park at the last record and step forward.
			last := 0
			if lf.len() > 0 {
				last = lf.len() - 1
			}
			c.path[level] = frame{pgno: pgno, idx: uint32(last)}
			c.state = stAt
			return c.Next()
		}
		b := c.branch(pgno)
		ci := branchDescend[K](b, key)
		c.path[level] = frame{pgno: pgno, idx: uint32(ci)}
		pgno = b.child(ci)
		level++
	}
}

// Next steps to the next record in key order. BeforeFirst → first; At → next or AfterLast;
// Empty/AfterLast → stays, false.
func (c *Cursor[K]) Next() bool {
	switch c.state {
	case stEmpty, stAfterLast:
		return false
	case stBeforeFirst:
		return c.First()
	default: // stAt
		leafLevel := c.leafLevel()
		if int(c.path[leafLevel].idx)+1 < c.leaf(c.path[leafLevel].pgno).len() {
			c.path[leafLevel].idx++
			return true
		}
		level := leafLevel
		for {
			if level == 0 {
				c.state = stAfterLast
				return false
			}
			level--
			b := c.branch(c.path[level].pgno)
			if int(c.path[level].idx)+1 < b.childCount() {
				c.path[level].idx++
				child := b.child(int(c.path[level].idx))
				c.descendLeftmost(level+1, child)
				return true
			}
		}
	}
}

// Prev steps to the previous record in key order. AfterLast → last; At → prev or
// BeforeFirst; Empty/BeforeFirst → stays, false.
func (c *Cursor[K]) Prev() bool {
	switch c.state {
	case stEmpty, stBeforeFirst:
		return false
	case stAfterLast:
		return c.Last()
	default: // stAt
		leafLevel := c.leafLevel()
		if c.path[leafLevel].idx > 0 {
			c.path[leafLevel].idx--
			return true
		}
		level := leafLevel
		for {
			if level == 0 {
				c.state = stBeforeFirst
				return false
			}
			level--
			if c.path[level].idx > 0 {
				c.path[level].idx--
				child := c.branch(c.path[level].pgno).child(int(c.path[level].idx))
				c.descendRightmost(level+1, child)
				return true
			}
		}
	}
}

// Current returns the positioned record (from, to, scope) and ok=true, or ok=false unless
// the cursor is positioned at a record. scope is borrowed from the image (D11).
func (c *Cursor[K]) Current() (from, to K, scope []byte, ok bool) {
	if c.state != stAt {
		return from, to, nil, false
	}
	leafLevel := c.leafLevel()
	f := c.path[leafLevel]
	rec := c.leaf(f.pgno).record(int(f.idx))
	return rec.from(), rec.to(), rec.scope(), true
}

// --- helpers (§v4.1.B) ---

// forEachOverlap walks records overlapping [from, to] in key order, calling f(cf, ct,
// scope) with each record clamped to the window (cf <= ct). Stops if f returns false. The
// covering record (one whose from < from <= to) is included.
func (c *Cursor[K]) forEachOverlap(from, to K, f func(cf, ct K, scope []byte) bool) {
	if from.cmp(to) > 0 {
		return
	}
	c.Seek(from)
	// Back up to a covering record if the one before the successor overlaps from.
	probe := *c
	if probe.Prev() {
		if _, pt, _, ok := probe.Current(); ok && pt.cmp(from) >= 0 {
			*c = probe
		}
	}
	for {
		rf, rt, rs, ok := c.Current()
		if !ok || rf.cmp(to) > 0 {
			break
		}
		cf := rf
		if cf.cmp(from) < 0 {
			cf = from
		}
		ct := rt
		if ct.cmp(to) > 0 {
			ct = to
		}
		if !f(cf, ct, rs) {
			break
		}
		if !c.Next() {
			break
		}
	}
}

// QueryRanges emits each record overlapping [from, to] whose scope matches select, clamped
// to the window, as (from, to, scope). Per-record (not merged).
func (c *Cursor[K]) QueryRanges(from, to K, sel func([]byte) bool, visit func(from, to K, scope []byte) error) error {
	var verr error
	c.forEachOverlap(from, to, func(cf, ct K, scope []byte) bool {
		if !sel(scope) {
			return true
		}
		if err := visit(cf, ct, scope); err != nil {
			if err != Stop {
				verr = err
			}
			return false
		}
		return true
	})
	return verr
}

// QueryRangesMerged emits the maximal contiguous runs of matched key-space in [from, to] as
// (from, to) (coalesced across scopes; a non-matching or absent span breaks a run).
func (c *Cursor[K]) QueryRangesMerged(from, to K, sel func([]byte) bool, visit func(from, to K) error) error {
	var (
		open    bool
		of, ot  K
		verr    error
		stopped bool
	)
	emit := func(f, t K) bool { // returns continue
		if err := visit(f, t); err != nil {
			if err != Stop {
				verr = err
			}
			return false
		}
		return true
	}
	c.forEachOverlap(from, to, func(cf, ct K, scope []byte) bool {
		if !sel(scope) {
			if open {
				open = false
				if !emit(of, ot) {
					stopped = true
					return false
				}
			}
			return true
		}
		if open {
			if inc, ok := ot.checkedInc(); ok && inc.cmp(cf) == 0 {
				ot = ct // contiguous: extend the open run
			} else {
				if !emit(of, ot) {
					stopped = true
					return false
				}
				of, ot = cf, ct
			}
		} else {
			open, of, ot = true, cf, ct
		}
		return true
	})
	if !stopped && open {
		_ = emit(of, ot)
	}
	return verr
}

// QueryCIDRs emits, for each matched record overlapping [from, to], the canonical CIDR
// cover of its clamped range, as (network, prefixLen, scope).
func (c *Cursor[K]) QueryCIDRs(from, to K, sel func([]byte) bool, visit func(network K, prefixLen uint8, scope []byte) error) error {
	var verr error
	c.forEachOverlap(from, to, func(cf, ct K, scope []byte) bool {
		if !sel(scope) {
			return true
		}
		if err := emitCIDRs[K](cf, ct, func(net K, pl uint8) error { return visit(net, pl, scope) }); err != nil {
			if err != Stop {
				verr = err
			}
			return false
		}
		return true
	})
	return verr
}

// QueryCIDRsMerged emits the canonical CIDR cover of each merged matched run in [from, to],
// as (network, prefixLen) — the netset view.
func (c *Cursor[K]) QueryCIDRsMerged(from, to K, sel func([]byte) bool, visit func(network K, prefixLen uint8) error) error {
	return c.QueryRangesMerged(from, to, sel, func(rf, rt K) error {
		return emitCIDRs[K](rf, rt, visit)
	})
}

// CountIPs returns the total distinct IPs in [from, to] whose scope matches select. Records
// are disjoint, so this is the sum of clamped sizes. Saturates at the 128-bit max (only a
// fully-covered IPv6 space, 2^128, would exceed it).
func (c *Cursor[K]) CountIPs(from, to K, sel func([]byte) bool) Uint128 {
	var total Uint128
	c.forEachOverlap(from, to, func(cf, ct K, scope []byte) bool {
		if sel(scope) {
			span := ct.toU128().sub(cf.toU128()) // ct - cf (cf <= ct)
			total = total.saturatingAdd(span).saturatingAdd(Uint128{Lo: 1})
		}
		return true
	})
	return total
}

// CountCIDRs returns the number of CIDRs the matched set in [from, to] decomposes to
// (netset entry count): the CIDR count of the merged runs.
func (c *Cursor[K]) CountCIDRs(from, to K, sel func([]byte) bool) uint64 {
	var total uint64
	_ = c.QueryRangesMerged(from, to, sel, func(rf, rt K) error {
		total += cidrCount[K](rf, rt)
		return nil
	})
	return total
}

// leafLowerBound returns the first index in leaf whose record from >= key (lower bound);
// len if all are < key.
func leafLowerBound[K ipKey[K]](leaf leafView[K], key K) int {
	lo, hi := 0, leaf.len()
	for lo < hi {
		mid := lo + (hi-lo)/2
		if leaf.record(mid).from().cmp(key) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// --- CIDR decomposition (canonical minimal cover, §v4.1.B) ---

// emitCIDRs emits the canonical minimal CIDR cover of the inclusive range [a, b]:
// repeatedly take the largest aligned prefix that fits the remaining range. emit(network,
// prefixLen); a non-nil emit error short-circuits and is returned.
func emitCIDRs[K ipKey[K]](a, b K, emit func(network K, prefixLen uint8) error) error {
	var zero K
	bw := zero.bitWidth() // 32 or 128
	cur := a.toU128()
	end := b.toU128() // inclusive; cur <= end
	for {
		alignBits := bw
		if !cur.isZero() {
			if tz := cur.trailingZeros(); tz < alignBits {
				alignBits = tz
			}
		}
		nbits := alignBits
		if sb := sizeBits(end.sub(cur), bw); sb < nbits {
			nbits = sb
		}
		if err := emit(zero.fromU128(cur), uint8(bw-nbits)); err != nil {
			return err
		}
		if nbits >= bw {
			return nil // covered the whole space in one block
		}
		blockEnd := cur.add(maskU128(nbits)) // cur + (2^nbits - 1)
		if blockEnd.cmp(end) >= 0 {
			return nil
		}
		cur = blockEnd.add(Uint128{Lo: 1})
	}
}

// cidrCount returns the number of CIDRs in the canonical cover of [a, b].
func cidrCount[K ipKey[K]](a, b K) uint64 {
	var n uint64
	_ = emitCIDRs[K](a, b, func(K, uint8) error {
		n++
		return nil
	})
	return n
}

// sizeBits is the largest n in [0, bw] with 2^n - 1 <= gap (i.e. 2^n <= gap + 1).
func sizeBits(gap Uint128, bw int) int {
	if bw == 128 && gap.isMax() {
		return 128 // gap + 1 == 2^128
	}
	cap := gap.add(Uint128{Lo: 1}) // safe: gap < 2^bw, full-128 handled above
	n := 127 - cap.leadingZeros()  // floor(log2(cap))
	if n > bw {
		n = bw
	}
	return n
}

// --- Uint128: a 128-bit unsigned integer (Hi:Lo) for IP counts and CIDR math ---

// Uint128 is a 128-bit unsigned integer, Hi the most-significant 64 bits. It is the return
// type of CountIPs (a fully-covered IPv6 space holds 2^128 addresses).
type Uint128 struct {
	Hi, Lo uint64
}

func (a Uint128) isZero() bool { return a.Hi == 0 && a.Lo == 0 }
func (a Uint128) isMax() bool  { return a.Hi == maxUint64 && a.Lo == maxUint64 }

// add returns a + b, wrapping (callers ensure no overflow where it would matter).
func (a Uint128) add(b Uint128) Uint128 {
	lo, c := bits.Add64(a.Lo, b.Lo, 0)
	hi, _ := bits.Add64(a.Hi, b.Hi, c)
	return Uint128{Hi: hi, Lo: lo}
}

// sub returns a - b for a >= b.
func (a Uint128) sub(b Uint128) Uint128 {
	lo, borrow := bits.Sub64(a.Lo, b.Lo, 0)
	hi, _ := bits.Sub64(a.Hi, b.Hi, borrow)
	return Uint128{Hi: hi, Lo: lo}
}

// saturatingAdd returns a + b, clamped to the 128-bit maximum on overflow.
func (a Uint128) saturatingAdd(b Uint128) Uint128 {
	lo, c := bits.Add64(a.Lo, b.Lo, 0)
	hi, c2 := bits.Add64(a.Hi, b.Hi, c)
	if c2 != 0 {
		return Uint128{Hi: maxUint64, Lo: maxUint64}
	}
	return Uint128{Hi: hi, Lo: lo}
}

func (a Uint128) cmp(b Uint128) int {
	if a.Hi != b.Hi {
		if a.Hi < b.Hi {
			return -1
		}
		return 1
	}
	switch {
	case a.Lo < b.Lo:
		return -1
	case a.Lo > b.Lo:
		return 1
	default:
		return 0
	}
}

func (a Uint128) trailingZeros() int {
	if a.Lo != 0 {
		return bits.TrailingZeros64(a.Lo)
	}
	if a.Hi != 0 {
		return 64 + bits.TrailingZeros64(a.Hi)
	}
	return 128
}

func (a Uint128) leadingZeros() int {
	if a.Hi != 0 {
		return bits.LeadingZeros64(a.Hi)
	}
	return 64 + bits.LeadingZeros64(a.Lo)
}

// maskU128 returns 2^nbits - 1 (a low-nbits-ones mask), for nbits in [0, 128].
func maskU128(nbits int) Uint128 {
	switch {
	case nbits <= 0:
		return Uint128{}
	case nbits < 64:
		return Uint128{Lo: (uint64(1) << nbits) - 1}
	case nbits == 64:
		return Uint128{Lo: maxUint64}
	case nbits < 128:
		return Uint128{Hi: (uint64(1) << (nbits - 64)) - 1, Lo: maxUint64}
	default:
		return Uint128{Hi: maxUint64, Lo: maxUint64}
	}
}
