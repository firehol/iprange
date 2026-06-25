package iprangedb

// Hostile-input robustness (§9 / §10): Open and OpenImageV4 must return only a value or a
// typed error — never panic, loop, or read out of bounds — on truncations, bit-flips, and
// arbitrary buffers. (OpenImageV4 runs the same validation, so this also guards the
// writer's open path.) This is the Go port of v4/rust/iprange-livedb/tests/robustness.rs,
// using the shared LCG (oracle_test.go) so the two suites explore comparable inputs. In Go
// a panic fails the test, so no recover() is needed — none must occur.

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

// validRobustnessFile builds a multi-level valid file with some freed (unreachable) pages,
// to exercise both the reachable-page reject path and the unreachable-page ignore path.
func validRobustnessFile(t *testing.T) []byte {
	t.Helper()
	w := CreateV4(1, 0)
	for i := uint32(0); i < 2000; i++ {
		must(t, w.Set(wk(i*7), wk(i*7+2), []byte{byte(i & 0xff)}))
	}
	for i := uint32(0); i < 2000; i += 5 {
		must(t, w.Delete(wk(i*7), wk(i*7+2))) // frees pages
	}
	must(t, w.Commit(0))
	return append([]byte(nil), w.Image()...) // copy: Image aliases the writer buffer
}

// validScopeFile builds a valid v4.1 file carrying the full v4.1 metadata surface: an IP
// tree, a multi-level scope table (40 scopes, each with a header set), AND per-scope KV — a
// multi-level KV tree on one scope, small KV on others, a multi-page overflow value, and KV
// on FILE. Used to fuzz the v4.1 validation paths (scope-table walk + every kv_root walk +
// overflow read-by-count) against truncations / bit-flips.
func validScopeFile(t *testing.T) []byte {
	t.Helper()
	w := CreateV4(1, 0)
	for i := uint32(0); i < 400; i++ {
		must(t, w.Set(wk(i*11), wk(i*11+3), []byte{byte(i & 0xff)}))
	}
	for s := uint32(0); s < 40; s++ { // many scopes ⇒ a multi-level scope table
		id, err := w.ScopeDefine([]byte(strconv.FormatUint(uint64(s), 10)))
		must(t, err)
		if _, err := w.ScopeSetVersion(id, uint64(s)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.ScopeSetType(id, byte(s&1)); err != nil {
			t.Fatal(err)
		}
		// A small KV on every scope (text + binary), so most kv_roots are exercised.
		must(t, w.MetaSet(id, []byte("name"), 0, []byte("scope-"+strconv.FormatUint(uint64(s), 10))))
		must(t, w.MetaSet(id, []byte("flags"), 2, []byte{byte(s), 0xff}))
	}
	// Scope 1 gets a multi-level KV tree (many entries) so the KV branch walk is fuzzed.
	for i := 0; i < 300; i++ {
		must(t, w.MetaSet(1, []byte("k"+strconv.Itoa(i)), 0, []byte(strconv.Itoa(i*7))))
	}
	// Scope 2 gets a multi-page overflow value so the overflow read-by-count path is fuzzed.
	big := make([]byte, overflowPayload*2+50)
	for i := range big {
		big[i] = byte(i * 13)
	}
	must(t, w.MetaSet(2, []byte("blob"), 9, big))
	// KV on FILE (target 0) too.
	must(t, w.MetaSet(fileScopeID, []byte("dataset"), 0, []byte("fuzz")))
	must(t, w.Commit(0))
	return append([]byte(nil), w.Image()...) // copy: Image aliases the writer buffer
}

func TestTruncationsNeverPanic(t *testing.T) {
	for _, f := range [][]byte{validRobustnessFile(t), validScopeFile(t)} {
		two := 2 * pageSize
		// Every byte through the meta region (where bootstrap is most fragile), strided
		// beyond — opening any prefix must never panic.
		for length := 0; length < len(f); length++ {
			if length < two || length%37 == 0 {
				_, _ = Open(f[:length])
			}
		}
		_, _ = Open(f)
	}
}

func TestSingleBitFlipsNeverPanic(t *testing.T) {
	rng := lcg(0x9e3779b97f4a7c15)
	for _, f := range [][]byte{validRobustnessFile(t), validScopeFile(t)} {
		for i := 0; i < 5000; i++ {
			pos := int(rng()) % len(f)
			bit := rng() & 7
			g := append([]byte(nil), f...)
			g[pos] ^= 1 << bit
			_, _ = Open(g)
			_, _ = OpenImageV4(append([]byte(nil), g...))
		}
	}
}

func TestArbitraryBuffersNeverPanic(t *testing.T) {
	rng := lcg(0x1234567890abcdef)
	sizes := []int{0, 1, 16, 100, 4095, 4096, 4097, 8191, 8192, 8193, 12288, 20000}
	for _, size := range sizes {
		for i := 0; i < 40; i++ {
			buf := make([]byte, size)
			for j := range buf {
				buf[j] = byte(rng())
			}
			_, _ = Open(buf)
		}
	}
}

func TestTreeRegionFlipNeverSilentlyAccepted(t *testing.T) {
	// A bit flip in the data region (pages >= 2) is either detected (a reachable page's
	// checksum fails => reject) or ignored (an unreachable/free page => same data). It is
	// never accepted as a *different* reachable tree. (Meta-region flips are not tested
	// here: tearing the active meta legitimately recovers the previous committed state,
	// §6.3 — covered by the writer's crash-recovery tests.)
	f := validRobustnessFile(t)
	r0, err := Open(f)
	if err != nil {
		t.Fatalf("valid file rejected: %v", err)
	}
	base := robustScan(r0)

	two := 2 * pageSize
	rng := lcg(0xdeadbeefcafebabe)
	for i := 0; i < 4000; i++ {
		pos := two + int(rng())%(len(f)-two)
		bit := rng() & 7
		g := append([]byte(nil), f...)
		g[pos] ^= 1 << bit
		r, err := Open(g)
		if err != nil {
			continue // rejected: acceptable
		}
		assertScan(t, robustScan(r), base, "accepted a corrupted reachable tree")
	}
}

func TestScopeRegionFlipNeverSilentlyAccepted(t *testing.T) {
	// A bit flip anywhere in a v4.1 file (scope table or IP tree, pages >= 2) is either
	// detected (a reachable page's checksum/structure fails => reject) or ignored (an
	// unreachable/free page). It is never accepted as a *different* valid metadata view:
	// every accepted reopen must return byte-identical IP records + the same scope list.
	f := validScopeFile(t)
	r0, err := Open(f)
	if err != nil {
		t.Fatalf("valid v4.1 file rejected: %v", err)
	}
	baseIP := robustScan(r0)
	baseScopes := scopeListOf(t, f)

	two := 2 * pageSize
	rng := lcg(0xfeedface12345678)
	for i := 0; i < 4000; i++ {
		pos := two + int(rng())%(len(f)-two)
		bit := rng() & 7
		g := append([]byte(nil), f...)
		g[pos] ^= 1 << bit
		r, err := Open(g)
		if err != nil {
			continue // rejected: acceptable
		}
		assertScan(t, robustScan(r), baseIP, "accepted a corrupted reachable tree")
		if got := scopeListOf(t, g); !sameScopeList(got, baseScopes) {
			t.Fatalf("accepted a corrupted scope table at pos %d: %v != %v", pos, got, baseScopes)
		}
	}
}

// scopeListOf opens an image for write and returns its scope list (id+name), or nil on a
// (legitimately rejected) corrupt image.
func scopeListOf(t *testing.T, img []byte) []ScopeEntry {
	t.Helper()
	w, err := OpenImageV4(append([]byte(nil), img...))
	if err != nil {
		return nil
	}
	return w.ScopeList()
}

func sameScopeList(a, b []ScopeEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || string(a[i].Name) != string(b[i].Name) {
			return false
		}
	}
	return true
}

// robustScan collects an in-order scan as scanTriples for equality comparison.
func robustScan(r *Reader) []scanTriple {
	var out []scanTriple
	r.ScanV4(func(a, b Ipv4Key, sc []byte) {
		out = append(out, scanTriple{
			from:  strconv.FormatUint(uint64(a), 10),
			to:    strconv.FormatUint(uint64(b), 10),
			scope: append([]byte(nil), sc...),
		})
	})
	return out
}

// --- F5: structural-mutation fuzz (CRC-recomputed) + deterministic invariant tests ---
//
// The byte-flip fuzz above never recomputes a page CRC, so a mutated page is rejected at the
// CRC gate and the *structural* validator is never exercised on a checksum-VALID hostile
// file — which is the real threat model (and why F1/F2 slipped through review). The tests
// below close that gap: a fuzz that re-stamps the CRC after a structural mutation, plus
// deterministic unit tests for the now-covered invariants (shared overflow chain, duplicate
// branch children, and the F1 lone-last-child bulk build).

// pageOf returns a mutable view of pgno-th page within img (a copy's page).
func pageOf(img []byte, pgno uint32) []byte {
	off := int(pgno) * pageSize
	return img[off : off+pageSize]
}

// totalPagesOf returns the active meta's total_pages.
func totalPagesOf(img []byte) uint32 { return uint32(activeMetaOf(img).totalPages) }

// findPageOfType returns the first data page (>= 2) whose page_type == typ, or 0 if none.
func findPageOfType(img []byte, typ uint8) uint32 {
	for p := uint32(2); p < totalPagesOf(img); p++ {
		if decodePageHeader(pageOf(img, p)).pageType == typ {
			return p
		}
	}
	return 0
}

// TestStructuralMutationCRCValidNeverPanics mutates structural bytes of a metadata page,
// RE-STAMPS the page CRC (so it passes the checksum gate), then opens — asserting Open never
// panics and returns only a value or a typed *Error, never a wrong answer / OOB / loop (F5).
func TestStructuralMutationCRCValidNeverPanics(t *testing.T) {
	base := validScopeFile(t)
	total := totalPagesOf(base)
	rng := lcg(0xa5a5f00d12345678)
	// Offsets we corrupt: header pgno / entry_count, the leftmost-child u32, the first few
	// slot-directory + entry-heap bytes, and overflow next_pgno / payload-length fields.
	offsets := []int{phEntryCount, phPgno, pageHeaderSize, pageHeaderSize + 2, pageHeaderSize + 4,
		pageHeaderSize + 6, overflowNextPgno, overflowNextPgno + 4, pageSize - 8, pageSize - 2}
	for p := uint32(2); p < total; p++ {
		pt := decodePageHeader(pageOf(base, p)).pageType
		// Only the v4.1 metadata page types feed the new structural validator.
		if pt < pageTypeScopeBranch || pt > pageTypeOverflow {
			continue
		}
		for iter := 0; iter < 40; iter++ {
			g := append([]byte(nil), base...)
			page := pageOf(g, p)
			// Apply 1-3 structural byte mutations within the page body.
			for m := 0; m < 1+int(rng()%3); m++ {
				off := offsets[int(rng())%len(offsets)]
				page[off] ^= byte(1 + rng()%255)
			}
			finalizeChecksum(page) // re-stamp: this page now passes the CRC gate
			// Must never panic; either opens or returns a typed *Error.
			r, err := Open(g)
			if err != nil {
				if _, ok := err.(*Error); !ok {
					t.Fatalf("page %d: non-typed error %T: %v", p, err, err)
				}
				continue
			}
			// If it opened, every metadata read must also stay panic-free and typed.
			_ = r.ScanV4(func(a, b Ipv4Key, sc []byte) {})
			for _, e := range r.ScopeList() {
				_, _ = r.MetaList(e.ID)
			}
			_, _ = r.MetaList(fileScopeID)
		}
	}
}

// TestSharedOverflowChainRejected is the glm PoC (§9 wrong-answer): two KV entries whose
// overflow chains point at the SAME pages. Before F2 this opened and meta_get returned the
// wrong entry's bytes from a checksum-valid file. Now the file-wide visitor rejects it.
func TestSharedOverflowChainRejected(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	// Two single-page overflow values (each > kvInlineMax, <= overflowPayload) ⇒ two distinct
	// one-page chains in one KV leaf. Equal length so the read-by-count walk is consistent
	// once we alias the heads.
	va := bytes.Repeat([]byte{0x11}, kvInlineMax+10)
	vb := bytes.Repeat([]byte{0x22}, kvInlineMax+10)
	must(t, w.MetaSet(a, []byte("ka"), 9, va))
	must(t, w.MetaSet(a, []byte("kb"), 9, vb))
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if _, err := Open(img); err != nil {
		t.Fatalf("valid two-overflow file rejected: %v", err)
	}
	// Find the single KV leaf and alias kb's first_pgno+total_len onto ka's chain head.
	leafPgno := findPageOfType(img, pageTypeKVLeaf)
	if leafPgno == 0 {
		t.Fatal("no KV leaf found")
	}
	page := pageOf(img, leafPgno)
	count := int(decodePageHeader(page).entryCount)
	if count != 2 {
		t.Fatalf("expected 2 KV entries in one leaf, got %d", count)
	}
	leaf := newKVLeafView(page, count)
	h0, err := leaf.entryHdr(0)
	must(t, err)
	h1, err := leaf.entryHdr(1)
	must(t, err)
	if h0.inlineOK || h1.inlineOK {
		t.Fatalf("expected both entries to be overflow (got inline)")
	}
	// Point entry 1's chain at entry 0's head (and matching length): now both chains share
	// the same overflow page.
	off1, err := leaf.slot(1)
	must(t, err)
	// Skip key_len(2) · key · type(4) · value_kind(1) to land on first_pgno(4)·total_len(8).
	fp := off1 + 2 + len(h1.key) + 4 + 1
	le.PutUint32(page[fp:], h0.first)
	le.PutUint64(page[fp+4:], h0.total)
	finalizeChecksum(page)
	if _, err := Open(img); err == nil {
		t.Fatal("shared overflow chain accepted (F2 wrong-answer bug not caught)")
	} else if _, ok := err.(*Error); !ok {
		t.Fatalf("shared overflow chain: non-typed error %T: %v", err, err)
	}
}

// TestOverflowTotalLenU64MaxRejected patches a KV overflow entry's value_total_len to uint64
// max. A naive `(total+payload-1)/payload` div_ceil wraps to a tiny page count that slips past
// the "chain longer than file" cap and returns a truncated/empty value — a wrong answer on a
// checksum-valid file (and the chain's pages go unmarked, bypassing the F2 disjointness guard).
// The overflow-safe div_ceil must reject it.
func TestOverflowTotalLenU64MaxRejected(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	must(t, w.MetaSet(a, []byte("k"), 9, bytes.Repeat([]byte{0x33}, kvInlineMax+10)))
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if _, err := Open(img); err != nil {
		t.Fatalf("valid overflow file rejected: %v", err)
	}
	leafPgno := findPageOfType(img, pageTypeKVLeaf)
	if leafPgno == 0 {
		t.Fatal("no KV leaf found")
	}
	page := pageOf(img, leafPgno)
	count := int(decodePageHeader(page).entryCount)
	leaf := newKVLeafView(page, count)
	h0, err := leaf.entryHdr(0)
	must(t, err)
	if h0.inlineOK {
		t.Fatal("expected overflow entry")
	}
	off0, err := leaf.slot(0)
	must(t, err)
	// total_len follows key_len(2)·key·type(4)·value_kind(1)·first_pgno(4).
	tp := off0 + 2 + len(h0.key) + 4 + 1 + 4
	le.PutUint64(page[tp:], ^uint64(0)) // value_total_len = u64::MAX
	finalizeChecksum(page)
	if _, err := Open(img); err == nil {
		t.Fatal("overflow value_total_len=u64::MAX accepted (div_ceil overflow not caught)")
	} else if _, ok := err.(*Error); !ok {
		t.Fatalf("div_ceil-overflow: non-typed error %T: %v", err, err)
	}
}

// TestDuplicateScopeBranchChildRejected duplicates a child pgno inside a scope-table branch
// (the visitor reaches the same subtree twice ⇒ structural error, F2).
func TestDuplicateScopeBranchChildRejected(t *testing.T) {
	w := CreateV4(1, 0)
	// scopeLeafMax*2 + 1 scopes ⇒ a multi-level scope table with branch nodes.
	for i := 0; i < scopeLeafMax()*2+1; i++ {
		_, err := w.ScopeDefine([]byte(strconv.Itoa(i)))
		must(t, err)
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if _, err := Open(img); err != nil {
		t.Fatalf("valid multi-level scope table rejected: %v", err)
	}
	brPgno := findPageOfType(img, pageTypeScopeBranch)
	if brPgno == 0 {
		t.Fatal("no scope branch found (table not multi-level)")
	}
	page := pageOf(img, brPgno)
	s := int(decodePageHeader(page).entryCount)
	bv := scopeBranchView(page, s)
	// Duplicate child[1] onto the leftmost child slot (the 4 bytes after the header).
	le.PutUint32(page[pageHeaderSize:], bv.child(1))
	finalizeChecksum(page)
	if _, err := Open(img); err == nil {
		t.Fatal("duplicate scope-branch child accepted")
	} else if _, ok := err.(*Error); !ok {
		t.Fatalf("duplicate scope-branch child: non-typed error %T: %v", err, err)
	}
}

// TestDuplicateKVBranchChildRejected duplicates a child pgno inside a KV branch (the visitor
// reaches the same subtree twice ⇒ structural error, F2).
func TestDuplicateKVBranchChildRejected(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	for i := 0; i < 400; i++ { // many entries ⇒ a multi-level KV tree with branch nodes
		must(t, w.MetaSet(a, []byte("k"+strconv.Itoa(i)), 0, []byte(strconv.Itoa(i))))
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if _, err := Open(img); err != nil {
		t.Fatalf("valid multi-level KV tree rejected: %v", err)
	}
	brPgno := findPageOfType(img, pageTypeKVBranch)
	if brPgno == 0 {
		t.Fatal("no KV branch found (tree not multi-level)")
	}
	page := pageOf(img, brPgno)
	count := int(decodePageHeader(page).entryCount)
	bv := newKVBranchView(page, count)
	child1, err := bv.child(1)
	must(t, err)
	// Duplicate child[1] onto the leftmost child (4 bytes after the header).
	le.PutUint32(page[pageHeaderSize:], child1)
	finalizeChecksum(page)
	if _, err := Open(img); err == nil {
		t.Fatal("duplicate KV-branch child accepted")
	} else if _, ok := err.(*Error); !ok {
		t.Fatalf("duplicate KV-branch child: non-typed error %T: %v", err, err)
	}
}

// kvBranchFanout / kvLeafFanout compute the FIXED bulk-load fanout for keys of one fixed
// length L (so a deterministic entry count can force a branch level onto the lone-last-child
// remainder). They mirror buildKVTree's greedy packers for the constant-size case.
func kvBranchFanout(l int) int {
	body := kvPageBody - 4 // after the leftmost-child u32
	add := kvBranchSepSize(l) + kvSlotSize
	children := 1 // leftmost
	for used := 0; used+add <= body; used += add {
		children++
	}
	return children
}

func kvLeafFanout(l, valLen int) int {
	es := kvInlineEntrySize(l, valLen) + kvSlotSize
	n := 0
	for used := 0; used+es <= kvPageBody; used += es {
		n++
	}
	return n
}

// TestSharedKVRootAcrossScopesRejected makes two scope records share one kv_root (so two
// scopes alias the same KV tree). Without the file-wide visitor each scope's KV tree
// validates independently and both pass — a wrong answer (both scopes report the same KV).
// The visitor reaches the shared tree's pages twice ⇒ structural error (F2). This isolates
// the visitor: the per-tree key-ordering checks cannot catch cross-tree page sharing.
func TestSharedKVRootAcrossScopesRejected(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a")) // id 1
	must(t, err)
	b, err := w.ScopeDefine([]byte("b")) // id 2
	must(t, err)
	must(t, w.MetaSet(a, []byte("ka"), 0, []byte("va")))
	must(t, w.MetaSet(b, []byte("kb"), 0, []byte("vb")))
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if _, err := Open(img); err != nil {
		t.Fatalf("valid two-scope-KV file rejected: %v", err)
	}
	// Find the scope leaf and copy scope a's kv_root into scope b's record.
	leafPgno := findPageOfType(img, pageTypeScopeLeaf)
	if leafPgno == 0 {
		t.Fatal("no scope leaf found")
	}
	page := pageOf(img, leafPgno)
	count := int(decodePageHeader(page).entryCount)
	sl := newScopeLeafView(page, count)
	var recA, recB int = -1, -1
	for i := 0; i < count; i++ {
		switch sl.id(i) {
		case a:
			recA = i
		case b:
			recB = i
		}
	}
	if recA < 0 || recB < 0 {
		t.Fatalf("scope records not in one leaf (a=%d b=%d)", recA, recB)
	}
	rootA := le.Uint32(page[pageHeaderSize+recA*scopeRecordSize+scopeRecKVRoot:])
	le.PutUint32(page[pageHeaderSize+recB*scopeRecordSize+scopeRecKVRoot:], rootA)
	finalizeChecksum(page)
	if _, err := Open(img); err == nil {
		t.Fatal("shared kv_root across two scopes accepted (F2 not caught)")
	} else if _, ok := err.(*Error); !ok {
		t.Fatalf("shared kv_root: non-typed error %T: %v", err, err)
	}
}

// TestF1LoneLastChildKVBuildRoundTrips deterministically forces the KV branch level onto the
// lone-last-child remainder (leaf count == branch_fanout + 1, so the last branch would have a
// single child) using fixed-length keys, then asserts the validator accepts the file (F1
// rebalanced instead of emitting a 0-separator branch) and meta_list round-trips.
func TestF1LoneLastChildKVBuildRoundTrips(t *testing.T) {
	const keyLen = 1000 // ⇒ small fixed branch fanout, so the case is reachable at modest n
	const valLen = 3
	bf := kvBranchFanout(keyLen)
	lf := kvLeafFanout(keyLen, valLen)
	leaves := bf + 1       // one past a full branch ⇒ a lone last child pre-F1
	n := lf*(leaves-1) + 1 // ceil(n/lf) == leaves
	if (n+lf-1)/lf != leaves || bf < 3 {
		t.Fatalf("geometry off: bf=%d lf=%d leaves=%d n=%d", bf, lf, leaves, n)
	}
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("s"))
	must(t, err)
	want := make(map[string]string, n)
	for i := 0; i < n; i++ {
		// Fixed-length unique keys (zero-padded numeric prefix sorts numerically), so every
		// leaf/branch packs to the constant fanout above.
		key := strconv.Itoa(1_000_000+i) + strings.Repeat("k", keyLen-7)
		val := strconv.Itoa(i)
		must(t, w.MetaSet(a, []byte(key), 0, []byte(val)))
		want[key] = val
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	r, err := Open(img) // full structural validation (rejects a 0-separator branch)
	if err != nil {
		t.Fatalf("n=%d (bf=%d,leaves=%d): lone-last-child KV build rejected: %v", n, bf, leaves, err)
	}
	got, err := r.MetaList(a)
	must(t, err)
	if len(got) != n {
		t.Fatalf("meta_list len %d != %d", len(got), n)
	}
	for _, e := range got {
		if want[string(e.Key)] != string(e.Value) {
			t.Fatalf("key %q mismatch (got %q)", e.Key, e.Value)
		}
	}
}

// TestF1LoneLastChildScopeBuildRoundTrips deterministically forces the scope-table branch
// level onto the lone-last-child remainder: scope_leaf_max * scope_fanout + 1 scopes ⇒
// (scope_fanout + 1) leaves ⇒ the final branch would have a single child pre-F1. Commit,
// reopen, scope_list must match (the build must rebalance, not emit a 0-separator branch).
func TestF1LoneLastChildScopeBuildRoundTrips(t *testing.T) {
	lm := scopeLeafMax()
	fanout := scopeBranchMax() + 1
	leaves := fanout + 1   // one past a full branch
	n := lm*(leaves-1) + 1 // ceil(n/lm) == leaves == fanout+1
	if (n+lm-1)/lm != leaves {
		t.Fatalf("geometry off: lm=%d fanout=%d leaves=%d n=%d", lm, fanout, leaves, n)
	}
	w := CreateV4(1, 0)
	want := make(map[uint32]string, n)
	for i := 0; i < n; i++ {
		name := "scope-" + strconv.Itoa(i)
		id, err := w.ScopeDefine([]byte(name))
		must(t, err)
		want[id] = name
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if _, err := Open(img); err != nil { // rejects a 0-separator scope branch
		t.Fatalf("n=%d (leaves=%d): lone-last-child scope build rejected: %v", n, leaves, err)
	}
	w2, err := OpenImageV4(img)
	must(t, err)
	got := w2.ScopeList()
	if len(got) != n {
		t.Fatalf("scope_list len %d != %d", len(got), n)
	}
	for _, e := range got {
		if want[e.ID] != string(e.Name) {
			t.Fatalf("scope %d name mismatch: %q != %q", e.ID, e.Name, want[e.ID])
		}
	}
}
