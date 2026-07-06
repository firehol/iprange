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
			func() { defer func() { _ = recover() }(); _, _ = OpenImageV4(append([]byte(nil), g...)) }()
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
			continue
		}
		if err := r.Validate(); err != nil {
			continue
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
			continue
		}
		if err := r.Validate(); err != nil {
			continue
		}
		assertScan(t, robustScan(r), baseIP, "accepted a corrupted reachable tree")
		if got := scopeListOf(t, g); !sameScopeList(got, baseScopes) {
			t.Fatalf("accepted a corrupted scope table at pos %d: %v != %v", pos, got, baseScopes)
		}
	}
}
// scopeListOf opens an image for write and returns its scope list (id+name), or nil on a
// (legitimately rejected) corrupt image. The writer trusts the file and may panic on
// corrupt input — recover protects the test.
func scopeListOf(t *testing.T, img []byte) (result []ScopeEntry) {
	t.Helper()
	defer func() { _ = recover() }()
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

// v4View is the full observable view of an opened image: the IP records, the scope list, and
// the per-target KV lists (FILE first, then each scope in scope_list order). It is the
// "answer" a reader returns; the no-wrong-answer fuzz asserts a checksum-valid structural
// mutation is either rejected or returns this exact view — never a different valid answer.
type v4View struct {
	ip     []scanTriple
	scopes []ScopeEntry
	metas  [][]MetaEntry // aligned with [FILE, scopes[0].ID, scopes[1].ID, ...]
}

// captureView reads the whole observable view of an opened reader (panic-free; the image is
// validated). metas[0] is FILE (scope_id 0); the rest follow scope_list order.
func captureView(r *Reader) v4View {
	v := v4View{ip: robustScan(r), scopes: r.ScopeList()}
	v.metas = append(v.metas, metaListOrEmpty(r, fileScopeID))
	for _, s := range v.scopes {
		v.metas = append(v.metas, metaListOrEmpty(r, s.ID))
	}
	return v
}

func metaListOrEmpty(r *Reader, target uint32) []MetaEntry {
	m, err := r.MetaList(target)
	if err != nil {
		return nil // a typed read error is acceptable here; the view comparison treats it as empty
	}
	return m
}

func sameTriples(a, b []scanTriple) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].from != b[i].from || a[i].to != b[i].to || !bytes.Equal(a[i].scope, b[i].scope) {
			return false
		}
	}
	return true
}

func sameMetaList(a, b []MetaEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i].Key, b[i].Key) || a[i].Type != b[i].Type || !bytes.Equal(a[i].Value, b[i].Value) {
			return false
		}
	}
	return true
}

// sameView reports whether two views are byte-identical (IP records, scopes, and every
// per-target KV list). A mismatch on any axis means a corrupted file was silently accepted
// as a *different* valid answer.
func sameView(a, b v4View) bool {
	if !sameTriples(a.ip, b.ip) || !sameScopeList(a.scopes, b.scopes) || len(a.metas) != len(b.metas) {
		return false
	}
	for i := range a.metas {
		if !sameMetaList(a.metas[i], b.metas[i]) {
			return false
		}
	}
	return true
}

// TestStructuralMutationCRCValidNeverPanics mutates structural bytes of a reachable page (any
// page type, IP tree INCLUDED), RE-STAMPS the page CRC (so it passes the checksum gate), then
// opens — asserting Open never panics and returns only a value or a typed *Error (F5). It also
// closes the wrong-answer gap (§9): against a pristine baseline view captured once, every
// accepted reopen MUST return a byte-identical view — reject OR identical, never a different
// valid answer. It fuzzes a multi-level IP tree (IP branch + leaf pages) and a metadata-rich
// v4.1 file (scope table + multi-level KV + overflow), so every reachable page type is perturbed.
func TestStructuralMutationCRCValidNeverPanics(t *testing.T) {
	// validIPTreeFile gives IP branch + leaf pages; validScopeFile gives the scope table,
	// multi-level KV, and overflow pages (plus one IP leaf). Together they perturb every
	// reachable page type that feeds the structural validators.
	for _, base := range [][]byte{validIPTreeFile(t), validScopeFile(t)} {
		fuzzStructuralMutations(t, base)
	}
}

// reachablePages returns every page the validators actually walk: the IP tree (root + all
// branches + leaves), the scope table, every per-scope KV tree, and every overflow chain.
// Fuzzing only these keeps the cost bounded (a freshly built file has many free pages whose
// mutation the bit-flip fuzz already covers) while still perturbing every structural page.
func reachablePages(t *testing.T, img []byte) []uint32 {
	t.Helper()
	m := activeMetaOf(img)
	var pages []uint32
	if m.rootPgno != 0 {
		var walk func(pgno, depth uint32)
		walk = func(pgno, depth uint32) {
			pages = append(pages, pgno)
			if depth == m.treeHeight {
				return
			}
			page := pageOf(img, pgno)
			bv := newBranchView[Ipv4Key](page, int(decodePageHeader(page).entryCount))
			for j := 0; j < bv.childCount(); j++ {
				walk(bv.child(j), depth+1)
			}
		}
		walk(m.rootPgno, 1)
	}
	if m.scopeTableRoot != 0 {
		collectScopePages(img, m.scopeTableRoot, &pages)
		recs, err := loadAllScopes(img, m.scopeTableRoot)
		must(t, err)
		for i := range recs {
			collectKVPages(img, recs[i].kvRoot, m.totalPages, &pages)
		}
	}
	return pages
}

// structuralOffsets returns byte offsets of REDUNDANT structural fields for page type pt —
// fields the validator cross-checks, so a re-CRC'd mutation there must be REJECTED or be a
// no-op, never a different valid view. Data-authority bytes are deliberately excluded:
//   - record / key / value / scope-name bytes (changing them + re-CRC is a legitimately
//     different valid file, not a wrong answer).
//
// IP/scope leaf+branch entry_count IS redundant (the tail after the live body must be zero, so a
// wrong count is rejected). KV leaf/branch entry_count is now redundant too: the canonical-
// packing check (Part A / SOW-0010) rejects a shrunk count (its orphaned slot lands in the
// must-be-zero free gap) and a grown count (the extra "slots" read as zero ⇒ out-of-bounds), so
// it is included for every page type.
func structuralOffsets(pt uint8) []int {
	h := []int{phPageType, phReserved, phPgno}
	switch pt {
	case pageTypeLeaf, pageTypeScopeLeaf:
		return append(h, phEntryCount) // fixed-record leaf: count is tail-zero-checked; body is data
	case pageTypeBranch, pageTypeScopeBranch:
		// IPv4-layout branch: count (tail) + child[0] / sep[0] / child[1] (pointers + routing key).
		return append(h, phEntryCount, ipBranchChildOff(0), ipBranchSepOff(0), ipBranchChildOff(1))
	case pageTypeKVBranch:
		// count (now canonical-packing-checked) + leftmost child + the first two slot-directory bytes.
		return append(h, phEntryCount, pageHeaderSize, kvBranchDirStart, kvBranchDirStart+2)
	case pageTypeKVLeaf:
		// count (now canonical-packing-checked) + slot directory (front) — repointing a slot is
		// rejected (key order/bounds) and a wrong count by the free-gap/tiling checks.
		return append(h, phEntryCount, pageHeaderSize, pageHeaderSize+2)
	case pageTypeOverflow:
		// count is unused on overflow pages; next_pgno is cross-checked by read-by-count.
		return append(h, phEntryCount, overflowNextPgno, overflowNextPgno+1)
	default:
		return h
	}
}

func fuzzStructuralMutations(t *testing.T, base []byte) {
	t.Helper()
	r0, err := Open(base)
	if err != nil {
		t.Fatalf("valid file rejected: %v", err)
	}
	want := captureView(r0) // pristine baseline: the only acceptable accepted answer
	rng := lcg(0xa5a5f00d12345678)
	for _, p := range reachablePages(t, base) {
		offsets := structuralOffsets(decodePageHeader(pageOf(base, p)).pageType)
		// Every reachable page type is fuzzed: IP branch/leaf, scope branch/leaf, KV branch/leaf,
		// and overflow — each restricted to its REDUNDANT structural fields (so an accepted reopen
		// is provably identical to the baseline; see structuralOffsets).
		for iter := 0; iter < 60; iter++ {
			g := append([]byte(nil), base...)
			page := pageOf(g, p)
			// Apply 1-3 structural byte mutations.
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
				continue // rejected: acceptable
			}
			if err := r.Validate(); err != nil {
				if _, ok := err.(*Error); !ok {
					t.Fatalf("page %d: non-typed error %T: %v", p, err, err)
				}
				continue
			}
			// Accepted: the view MUST equal the baseline. A redundant-structural mutation either
			// fails validation (rejected above) or is a no-op that leaves the view identical —
			// never a different valid answer (§9 no-wrong-answer).
			if got := captureView(r); !sameView(got, want) {
				t.Fatalf("page %d (type %d): accepted a corrupted image with a different view",
					p, decodePageHeader(pageOf(base, p)).pageType)
			}
			// OpenImageV4 (the writer open path) delegates to Open, so the same validation gates
		// The writer trusts the file (no validation); a corrupt image may panic internally.
		func() {
			defer func() { _ = recover() }()
			if w, werr := OpenImageV4(append([]byte(nil), g...)); werr == nil {
				_ = w.ScopeList()
			}
		}()
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
	// The file-wide visitor marks entry 0's overflow page, then re-reaches it from entry 1's
	// aliased chain ⇒ the exact F2 disjointness message.
	mustRejectMsg(t, img, "Structural", "metadata page reached twice (shared/duplicate page)",
		"shared overflow chain")
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
	// The overflow-safe div_ceil yields a page budget > total_pages ⇒ the exact cap message.
	mustRejectMsg(t, img, "Structural", "kv overflow chain longer than file",
		"overflow value_total_len=u64::MAX")
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
	// child[1] := child[0] (leftmost): the walk validates child[0] first (its ids fit
	// [lo, sep[0])), marking its subtree in the file-wide visitor; then child[1] (now the same
	// page) re-enters it ⇒ the F2 disjointness check fires. Mutating the LEFTMOST instead (the
	// old direction) would push child[1]'s ids under child[0]'s bound and trip the separator
	// misroute check FIRST — vacuous w.r.t. F2. child[1] is the trailing u32 of separator 0:
	// header + leftmost u32 + sep_id u32.
	le.PutUint32(page[pageHeaderSize+4+scopeKeyWidth:], bv.child(0))
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "metadata page reached twice (shared/duplicate page)",
		"duplicate scope-branch child")
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
	leftmost := bv.leftmost()
	off, err := bv.slot(0)
	must(t, err)
	sepLen := int(le.Uint16(page[off:]))
	// separator-0's child := leftmost: the walk validates child[0]=leftmost first (its keys fit
	// [b"", sep[0])), marking its subtree; then child[1] (== sep[0].child == leftmost) re-enters
	// it ⇒ the F2 disjointness check fires. Mutating the LEFTMOST instead (the old direction)
	// would trip the separator-bound misroute check at child[0] FIRST — vacuous w.r.t. F2.
	// sep[0]'s child u32 follows sep_len(2)·sep_key.
	le.PutUint32(page[off+2+sepLen:], leftmost)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "metadata page reached twice (shared/duplicate page)",
		"duplicate KV-branch child")
}

// TestScopeBranchSeparatorMisrouteRejected (codex Finding 3): a scope-table branch whose
// separator no longer matches the child boundaries. Separators stay strictly increasing and
// CRCs valid, but a lookup would misroute. The validator must confine each child's ids to its
// separator-derived bound and reject (parity with the IP-tree validator).
func TestScopeBranchSeparatorMisrouteRejected(t *testing.T) {
	w := CreateV4(1, 0)
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
	// Shrink separator 0 to id 1: child[0] (leftmost leaf, ids >= 1) now exceeds its bound
	// [lo, sep0-1] = [0, 0], yet sep0 = 1 stays > lo and < sep1 — only the new bound check
	// fails (the old validator accepted this). sep[0] id follows the leftmost child u32.
	le.PutUint32(page[pageHeaderSize+4:], 1)
	finalizeChecksum(page)
	// child[0]'s ids (>= 1) now exceed its separator-derived bound [0, 0] ⇒ the exact misroute message.
	mustRejectMsg(t, img, "Invariant", "scope id outside node bound", "scope branch separator misroute")
}

// TestKVBranchSeparatorMisrouteRejected (codex Finding 3, KV side): a KV branch whose separator
// key is shifted below its child's real key range. Separators stay strictly increasing and CRCs
// valid, but child[0]'s keys would now fall at/above the (shrunken) separator → misroute.
func TestKVBranchSeparatorMisrouteRejected(t *testing.T) {
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
	off, err := bv.slot(0)
	must(t, err)
	// Decrement the first byte of separator 0's key ("k…" → "j…"): still a valid, strictly-
	// smaller key (separators stay increasing), but below every key in child[0] (all start
	// "k"), so child[0]'s keys fall at/above the node bound → reject. off+2 skips sep_len (u16).
	page[off+2]--
	finalizeChecksum(page)
	// child[0]'s keys (all start "k") now fall at/above the shrunken separator ⇒ the exact misroute message.
	mustRejectMsg(t, img, "Invariant", "kv key at/above node bound", "kv branch separator misroute")
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
	// Scope a's KV tree marks its pages; scope b's tree (same root) re-reaches them ⇒ the exact
	// F2 disjointness message (the per-tree key-ordering checks cannot catch cross-tree sharing).
	mustRejectMsg(t, img, "Structural", "metadata page reached twice (shared/duplicate page)",
		"shared kv_root across scopes")
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

// --- targeted single-invariant rejection tests (mandatory structure, §9) ---
//
// Each test below builds a VALID file, asserts it opens (the built-in non-vacuity guard:
// only the one corrupted field differs), mutates EXACTLY ONE field to violate EXACTLY ONE
// invariant, re-stamps the page CRC so the structural validator is reached, and asserts Open
// now rejects with the SPECIFIC typed error. They prove a future refactor that deletes one
// validator check is caught (the structural fuzz above proves it never silently accepts a
// wrong answer; these pin the exact error each check produces).

// --- shared helpers for the targeted tests ---

// activeMetaPageIdx returns the page index (0 or 1) of the active meta (higher txn_id).
func activeMetaPageIdx(img []byte) int {
	if decodeMeta(img[pageSize:2*pageSize]).txnID > decodeMeta(img[:pageSize]).txnID {
		return 1
	}
	return 0
}

// editActiveMeta mutates the active meta page in place and re-stamps its CRC, so the edit is
// CRC-valid and the meta is the one selectActiveMeta picks (its fields drive Open's geometry).
func editActiveMeta(img []byte, mutate func(mp []byte)) {
	idx := activeMetaPageIdx(img)
	mp := img[idx*pageSize : (idx+1)*pageSize]
	mutate(mp)
	finalizeChecksum(mp)
}

// validIPTreeFile builds a multi-level IPv4 tree: a root branch over 6 leaves (height 2, 5
// separators). The targeted tests navigate from the root so they only ever touch reachable
// pages. 1500 disjoint records (5-address gaps prevent coalescing) committed in chunks keeps
// the free-page churn (and the file) small enough to copy cheaply in the fuzz.
func validIPTreeFile(t *testing.T) []byte {
	t.Helper()
	w := CreateV4(1, 0)
	for i := uint32(0); i < 1500; i++ {
		must(t, w.Set(wk(i*7), wk(i*7+2), []byte{byte(i & 0xff)}))
		if (i+1)%50 == 0 { // periodic commits reuse freed pages ⇒ a compact file
			must(t, w.Commit(uint64(i)))
		}
	}
	must(t, w.Commit(1500))
	img := append([]byte(nil), w.Image()...)
	if m := activeMetaOf(img); m.treeHeight < 2 {
		t.Fatalf("expected a multi-level IP tree, got tree_height %d", m.treeHeight)
	}
	if n := len(ipLeaves(t, img)); n < 3 {
		t.Fatalf("expected >= 3 IP leaves (>= 2 separators), got %d", n)
	}
	return img
}

// ipRootBranch returns the IP tree's root branch pgno (its tree_height >= 2, so the root is a
// branch). It is reachable by definition.
func ipRootBranch(t *testing.T, img []byte) uint32 {
	t.Helper()
	m := activeMetaOf(img)
	if m.treeHeight < 2 {
		t.Fatalf("no IP branch: tree_height %d", m.treeHeight)
	}
	return m.rootPgno
}

// ipLeaves walks the committed IP tree and returns the reachable leaf pgnos in scan order.
func ipLeaves(t *testing.T, img []byte) []uint32 {
	t.Helper()
	m := activeMetaOf(img)
	if m.rootPgno == 0 {
		return nil
	}
	var leaves []uint32
	var walk func(pgno, depth uint32)
	walk = func(pgno, depth uint32) {
		if depth == m.treeHeight {
			leaves = append(leaves, pgno)
			return
		}
		page := pageOf(img, pgno)
		bv := newBranchView[Ipv4Key](page, int(decodePageHeader(page).entryCount))
		for j := 0; j < bv.childCount(); j++ {
			walk(bv.child(j), depth+1)
		}
	}
	walk(m.rootPgno, 1)
	return leaves
}

// ipBranchSepOff / ipBranchChildOff are the byte offsets of separator i / child j inside an
// IPv4 (key_width 4) branch page body: child[0] at the header; then (sep, child) pairs of
// 4+4 bytes (§5.2).
func ipBranchSepOff(i int) int { return pageHeaderSize + 4 + i*8 }
func ipBranchChildOff(j int) int {
	if j == 0 {
		return pageHeaderSize
	}
	return pageHeaderSize + 8 + (j-1)*8
}

// validMetaFile builds a small valid v4.1 file: a one-leaf IP tree, two scopes (alpha id 1,
// beta id 2), and a small inline text KV on alpha — so scope_table_root != 0, the scope leaf
// carries a UTF-8 name, and at least one scope record has kv_root != 0.
func validMetaFile(t *testing.T) []byte {
	t.Helper()
	w := CreateV4(1, 0)
	for i := uint32(0); i < 10; i++ {
		must(t, w.Set(wk(i*10), wk(i*10+3), []byte{byte(i)}))
	}
	a, err := w.ScopeDefine([]byte("alpha"))
	must(t, err)
	_, err = w.ScopeDefine([]byte("beta"))
	must(t, err)
	must(t, w.MetaSet(a, []byte("color"), 0, []byte("green"))) // inline text ⇒ alpha gets a kv_root
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if activeMetaOf(img).versionMinor != versionMinorMetadata {
		t.Fatalf("expected a v4.1 file, got minor %d", activeMetaOf(img).versionMinor)
	}
	return img
}

// validOverflowKVFile builds a valid v4.1 file whose single scope has one large TEXT (type 0)
// KV value, all-ASCII so it is valid UTF-8, large enough to force a multi-page overflow chain.
func validOverflowKVFile(t *testing.T) []byte {
	t.Helper()
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	big := bytes.Repeat([]byte("a"), overflowPayload+200) // > kvInlineMax and > one page ⇒ 2 overflow pages
	must(t, w.MetaSet(a, []byte("blob"), 0, big))
	must(t, w.Commit(1))
	return append([]byte(nil), w.Image()...)
}

// mustReject opens img and asserts a typed *Error whose Class matches want (the §9 contract:
// reject malformed input with a typed error, never panic / wrong answer).
// mustReject opens+validates img and asserts a typed *Error whose Class matches want.
func mustReject(t *testing.T, img []byte, want, what string) {
	t.Helper()
	r, err := Open(img)
	if err == nil {
		err = r.Validate()
	}
	if err == nil {
		t.Fatalf("%s: accepted a corrupted image", what)
	}
	if _, ok := err.(*Error); !ok {
		t.Fatalf("%s: non-typed error %T: %v", what, err, err)
	}
	if want != "" && errorClass(err) != want {
		t.Fatalf("%s: expected %s, got %v", what, want, err)
	}
}

// mustRejectMsg opens img and asserts a typed *Error whose Class equals wantClass AND whose
// message equals wantMsg EXACTLY — the EXACT location tag of the single validator check the
// test targets. This is the de-vacuization (§9): a class-only assertion can pass even if the
// named check is deleted, because a NEIGHBOURING check of the same class rejects the same
// file with a DIFFERENT message (e.g. "cross-leaf overlap" vs "record outside node bound",
// both Invariant). Pinning the EXACT message makes the test fail if its check is removed (the
// file then opens OK or rejects with a different message). Mirrors the Rust
// `matches!(.., Err(Error::Invariant(m)) if m == "...")` variant+string assertion (exact
// `&'static str`). Every validator location tag is a fixed string (the only attacker-derived
// values, e.g. "meta_size 92", are still fully determined by the test's single mutation), so
// exact equality — not a substring — is the correct, strongest assertion across the suite.
// The typed *Error renders "Class: Msg" via Error(); here we check the parsed Class and Msg.
func mustRejectMsg(t *testing.T, img []byte, wantClass, wantMsg, what string) {
	t.Helper()
	r, err := Open(img)
	if err == nil {
		err = r.Validate()
	}
	if err == nil {
		t.Fatalf("%s: accepted a corrupted image", what)
	}
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("%s: non-typed error %T: %v", what, err, err)
	}
	if e.Class != wantClass || e.Msg != wantMsg {
		t.Fatalf("%s: expected %s/%q, got %v", what, wantClass, wantMsg, err)
	}
}

// mustOpenOK is the non-vacuity guard: the base file MUST open before any mutation.
func mustOpenOK(t *testing.T, img []byte, what string) {
	t.Helper()
	if _, err := Open(img); err != nil {
		t.Fatalf("%s: valid base rejected: %v", what, err)
	}
}

// --- TIER 1b: IP-tree rejection tests ---

func TestIPBranchDuplicateChildRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip dup child")
	page := pageOf(img, ipRootBranch(t, img))
	child0 := le.Uint32(page[ipBranchChildOff(0):])
	le.PutUint32(page[ipBranchChildOff(1):], child0) // child[1] = child[0]
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "duplicate child pgno", "ip branch duplicate child")
}

func TestIPBranchSeparatorMisrouteRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip sep misroute")
	page := pageOf(img, ipRootBranch(t, img))
	// Shrink sep[0] to 1: it stays > lo (0) and < sep[1], so the separator checks pass, but
	// child[0]'s records (from 0, to 2, …) now exceed its derived bound [0, sep[0]-1] = [0, 0].
	le.PutUint32(page[ipBranchSepOff(0):], 1)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "record outside node bound", "ip branch separator misroute")
}

func TestIPLeafRecordToLtFromRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip to<from")
	page := pageOf(img, ipLeaves(t, img)[0])
	// Record 1 has from = 7; set its `to` (record_size 9: from@0,to@4,scope@8) below from.
	rec1 := pageHeaderSize + 1*9
	le.PutUint32(page[rec1+4:], 0)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "record to < from", "ip leaf record to < from")
}

func TestIPCrossLeafOverlapRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip cross-leaf")
	leaves := ipLeaves(t, img)
	if len(leaves) < 2 {
		t.Fatalf("need >= 2 leaves, got %d", len(leaves))
	}
	// Drop the SECOND leaf's first `from` to 0: it is now <= the first leaf's last `to`, so the
	// cross-leaf disjointness check fires (before the per-record bound check).
	page := pageOf(img, leaves[1])
	le.PutUint32(page[pageHeaderSize:], 0)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "cross-leaf overlap", "ip cross-leaf overlap")
}

func TestIPBranchSeparatorsNotIncreasingRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip sep order")
	page := pageOf(img, ipRootBranch(t, img))
	sep0 := le.Uint32(page[ipBranchSepOff(0):])
	le.PutUint32(page[ipBranchSepOff(1):], sep0) // sep[1] = sep[0] ⇒ not strictly increasing
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "separators not strictly increasing", "ip branch separators not increasing")
}

func TestIPChildPgnoOutOfRangeRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip child range")
	page := pageOf(img, ipRootBranch(t, img))
	le.PutUint32(page[ipBranchChildOff(0):], uint32(totalPagesOf(img))) // child >= total_pages
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "child pgno out of range", "ip child pgno out of range")
}

func TestIPWrongPageTypeAtDepthRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip wrong page type")
	page := pageOf(img, ipLeaves(t, img)[0])
	page[phPageType] = pageTypeBranch // a branch where the walk expects a leaf at tree_height
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "expected leaf at tree_height", "ip wrong page_type at depth")
}

func TestIPLeafTailNonzeroRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip leaf tail")
	page := pageOf(img, ipLeaves(t, img)[0])
	off := pageHeaderSize + int(decodePageHeader(page).entryCount)*9
	if off >= pageSize {
		t.Fatalf("leaf has no tail (off %d)", off)
	}
	page[off] = 0xFF
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "leaf tail", "ip leaf tail nonzero")
}

func TestIPBranchTailNonzeroRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip branch tail")
	page := pageOf(img, ipRootBranch(t, img))
	s := int(decodePageHeader(page).entryCount)
	off := pageHeaderSize + 4 + s*8 // body = child[0] + s*(sep+child)
	page[off] = 0xFF
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "branch tail", "ip branch tail nonzero")
}

func TestIPPageHeaderReservedNonzeroRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip header reserved")
	page := pageOf(img, ipRootBranch(t, img))
	page[phReserved] = 1
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "page header reserved", "ip page header reserved nonzero")
}

func TestIPSelfPgnoMismatchRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip self-pgno")
	leaf := ipLeaves(t, img)[0]
	page := pageOf(img, leaf)
	le.PutUint32(page[phPgno:], leaf+1) // stored pgno != actual
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "page self-pgno mismatch", "ip self-pgno mismatch")
}

func TestIPLeafEntryCountOutOfRangeRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip leaf count")
	page := pageOf(img, ipLeaves(t, img)[0])
	le.PutUint16(page[phEntryCount:], uint16(leafMax(9)+1)) // > leaf_max
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "leaf entry_count out of range", "ip leaf entry_count out of range")
}

// TestIPBranchChildCycleRejected points the root branch's child[0] back at the root. A cycle in
// the IP tree never silently loops: the descent re-reads the root (a branch) at depth ==
// tree_height where a leaf is required, so it is rejected (Structural "expected leaf at
// tree_height"). Note: "path deeper than tree_height" is defense-in-depth and structurally
// unreachable in this validator — the recursion stops at depth == tree_height via the leaf
// path, so a cycle always surfaces first as a page-type / self-pgno / duplicate-child error.
func TestIPBranchChildCycleRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip cycle")
	root := ipRootBranch(t, img)
	page := pageOf(img, root)
	le.PutUint32(page[ipBranchChildOff(0):], root) // child[0] -> the root branch (a cycle)
	finalizeChecksum(page)
	mustReject(t, img, "", "ip branch child cycle") // rejected with any typed error
}

// --- TIER 2a: hostile non-UTF-8 / NUL on the read/validate path ---

func TestScopeNameNonUTF8Rejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "scope name utf8")
	leafPgno := findPageOfType(img, pageTypeScopeLeaf)
	if leafPgno == 0 {
		t.Fatal("no scope leaf")
	}
	page := pageOf(img, leafPgno)
	count := int(decodePageHeader(page).entryCount)
	sl := newScopeLeafView(page, count)
	rec := -1
	for i := 0; i < count; i++ {
		if sl.id(i) == 1 { // scope alpha ("alpha")
			rec = i
		}
	}
	if rec < 0 {
		t.Fatal("scope id 1 not in this leaf")
	}
	base := pageHeaderSize + rec*scopeRecordSize
	page[base+scopeRecName] = 0xFF // first name byte within name_len ⇒ invalid UTF-8
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "scope name not valid UTF-8", "scope name non-UTF-8")
}

func TestKVInlineTextValueNonUTF8Rejected(t *testing.T) {
	leafPgno := func(img []byte) uint32 {
		p := findPageOfType(img, pageTypeKVLeaf)
		if p == 0 {
			t.Fatal("no KV leaf")
		}
		return p
	}
	// inlineValueOff returns the byte offset of entry 0's inline value within a KV leaf page.
	inlineValueOff := func(img []byte, p uint32) int {
		page := pageOf(img, p)
		leaf := newKVLeafView(page, int(decodePageHeader(page).entryCount))
		h, err := leaf.entryHdr(0)
		must(t, err)
		if !h.inlineOK {
			t.Fatal("expected an inline entry")
		}
		off, err := leaf.slot(0)
		must(t, err)
		return off + 2 + len(h.key) + 4 + 1 + 4 // key_len·key·type·value_kind·value_len
	}
	// non-UTF-8 variant: a lone 0xFF in the value.
	{
		img := validMetaFile(t)
		mustOpenOK(t, img, "kv inline utf8")
		p := leafPgno(img)
		page := pageOf(img, p)
		page[inlineValueOff(img, p)] = 0xFF
		finalizeChecksum(page)
		mustRejectMsg(t, img, "Invariant", "kv text value not valid UTF-8", "kv inline text value non-UTF-8")
	}
	// NUL variant: a NUL byte in the value.
	{
		img := validMetaFile(t)
		mustOpenOK(t, img, "kv inline nul")
		p := leafPgno(img)
		page := pageOf(img, p)
		page[inlineValueOff(img, p)] = 0x00
		finalizeChecksum(page)
		mustRejectMsg(t, img, "Invariant", "kv text value contains NUL", "kv inline text value NUL")
	}
}

func TestKVOverflowTextValueNonUTF8Rejected(t *testing.T) {
	img := validOverflowKVFile(t)
	mustOpenOK(t, img, "kv overflow utf8")
	ovf := findPageOfType(img, pageTypeOverflow)
	if ovf == 0 {
		t.Fatal("no overflow page")
	}
	page := pageOf(img, ovf)
	body := pageHeaderSize + 4 // after the header + next_pgno
	page[body+10] = 0xFF       // a standalone non-UTF-8 byte mid-payload (not straddling)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv text value not valid UTF-8", "kv overflow text value non-UTF-8")
}

// --- TIER 2b: meta / bootstrap + geometry rejects ---

func TestMetaPageSizeNot4096Rejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "page_size")
	editActiveMeta(img, func(mp []byte) { le.PutUint32(mp[metaPageSize:], 8192) })
	mustRejectMsg(t, img, "Incompatible", "page_size", "meta page_size != 4096")
}

func TestMetaChecksumAlgoUnknownRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "checksum_algo")
	editActiveMeta(img, func(mp []byte) { mp[metaChecksumAlgo] = 2 })
	mustRejectMsg(t, img, "Incompatible", "checksum_algo", "meta checksum_algo unknown")
}

func TestMetaUnknownFlagsBitRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "flags bit")
	editActiveMeta(img, func(mp []byte) { mp[metaFlags] |= 0x80 }) // a reserved high flags bit
	mustRejectMsg(t, img, "Incompatible", "unknown flags bit", "meta unknown flags bit")
}

func TestMetaKeyWidthVsFlagsRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "key_width")
	// flags bit0 = 0 ⇒ IPv4 ⇒ key_width must be 4; set 16 to disagree.
	editActiveMeta(img, func(mp []byte) { mp[metaKeyWidth] = 16 })
	mustRejectMsg(t, img, "Structural", "key_width disagrees with flags", "meta key_width vs flags")
}

func TestMetaRecordSizeMismatchRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "record_size")
	// record_size must equal 2*key_width + scope_width = 9; set 10.
	editActiveMeta(img, func(mp []byte) { le.PutUint32(mp[metaRecordSize:], 10) })
	mustRejectMsg(t, img, "Structural", "record_size mismatch", "meta record_size mismatch")
}

func TestMetasDisagreeStaticIdentityRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "static identity")
	// Edit the INACTIVE meta's created_unixtime (a static-identity field, [42,50)) and re-stamp
	// it: both metas are now CRC-valid (class 3) but disagree on static identity.
	inactive := 1 - activeMetaPageIdx(img)
	mp := img[inactive*pageSize : (inactive+1)*pageSize]
	le.PutUint64(mp[metaCreatedUnixtime:], 0xDEAD)
	finalizeChecksum(mp)
	mustRejectMsg(t, img, "Structural", "metas disagree on static identity", "metas disagree on static identity")
}

func TestMetaMinor0MetaSizeNot90Rejected(t *testing.T) {
	// A v4.0 file (minor 0) requires meta_size exactly 90.
	img := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	mustOpenOK(t, img, "minor0 meta_size")
	editActiveMeta(img, func(mp []byte) { le.PutUint16(mp[metaMetaSize:], 92) }) // in [90, page_size], != 90
	mustRejectMsg(t, img, "BadMetaSize", "meta_size 92", "minor0 meta_size != 90")
}

func TestTotalPagesOutOfRangeRejected(t *testing.T) {
	img := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	mustOpenOK(t, img, "total_pages")
	editActiveMeta(img, func(mp []byte) { le.PutUint64(mp[metaTotalPages:], 1) })
	mustRejectMsg(t, img, "Structural", "total_pages out of range", "total_pages out of range")
}

func TestTreeHeightGt32Rejected(t *testing.T) {
	img := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	mustOpenOK(t, img, "tree_height")
	editActiveMeta(img, func(mp []byte) { le.PutUint32(mp[metaTreeHeight:], 33) })
	mustRejectMsg(t, img, "Structural", "tree_height > 32", "tree_height > 32")
}

func TestRootPgnoOutOfRangeRejected(t *testing.T) {
	img := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	mustOpenOK(t, img, "root_pgno")
	// root_pgno >= total_pages (nonzero, so the height/root consistency check still passes).
	editActiveMeta(img, func(mp []byte) { le.PutUint32(mp[metaRootPgno:], uint32(totalPagesOf(img))) })
	mustRejectMsg(t, img, "Structural", "root_pgno out of range", "root_pgno out of range")
}

func TestScopeTableRootOutOfRangeRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "scope_table_root")
	editActiveMeta(img, func(mp []byte) { le.PutUint32(mp[metaScopeTableRoot:], uint32(totalPagesOf(img))) })
	mustRejectMsg(t, img, "Structural", "scope_table_root out of range", "scope_table_root out of range")
}

func TestKVRootOutOfRangeRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv_root")
	leafPgno := findPageOfType(img, pageTypeScopeLeaf)
	if leafPgno == 0 {
		t.Fatal("no scope leaf")
	}
	page := pageOf(img, leafPgno)
	count := int(decodePageHeader(page).entryCount)
	rec := -1
	for i := 0; i < count; i++ {
		if le.Uint32(page[pageHeaderSize+i*scopeRecordSize+scopeRecKVRoot:]) != 0 {
			rec = i // a scope record that actually has a KV tree (alpha)
		}
	}
	if rec < 0 {
		t.Fatal("no scope record with a kv_root")
	}
	le.PutUint32(page[pageHeaderSize+rec*scopeRecordSize+scopeRecKVRoot:], uint32(totalPagesOf(img)))
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv_root out of range", "kv_root out of range")
}

// --- SOW-0010 FIX ROUND: gap-closing single-field CRC-valid rejection tests ---
//
// These cover validator checks that the structural fuzz deliberately never reaches: the KV
// entry-header / overflow-descriptor / scope-name HEAP bytes (the fuzz's structuralOffsets
// touches only the slot directory + page header, never the variable-length entry bodies), the
// KV/scope slot-count checks, and the exact IP separator bounds. Each builds a VALID file,
// asserts it opens, corrupts EXACTLY ONE field, re-stamps the page CRC so the structural
// validator is reached, and asserts the EXACT typed error that check produces (read directly
// from kv.go / scope.go / reader.go — class verified, not assumed).

// --- builders + locators for the gap-closing tests ---

// putBranch1 forges a single-separator IPv4 branch page (child[0], sep, child[1]) and stamps
// its CRC. Used by buildThreeLevelV4 to reach a nested branch with a finite hi bound.
func putBranch1(file []byte, pgno, child0 uint32, sep Ipv4Key, child1 uint32) {
	base := int(pgno) * pageSize
	page := file[base : base+pageSize]
	writePageHeader(page, pageTypeBranch, 1, pgno)
	le.PutUint32(page[pageHeaderSize:], child0)
	sepOff := pageHeaderSize + 4
	sep.writeLE(page[sepOff : sepOff+4])
	le.PutUint32(page[sepOff+4:], child1)
	finalizeChecksum(page)
}

// buildThreeLevelV4 forges a height-3 IPv4 tree (root branch -> 2 inner branches -> 4 leaves)
// so a NESTED branch (depth 2) carries a finite hi bound — the only place a "separator > hi"
// violation is reachable, since the root branch's hi is the family max which a u32 separator
// can never exceed. Pages: 0/1 meta, 2 root branch (sep 1000), 3/4 inner branches (sep 500 /
// 2000), 5..8 leaves.
func buildThreeLevelV4(t *testing.T) []byte {
	t.Helper()
	const total = 9
	file := make([]byte, total*pageSize)
	putLeaf(file, 5, 1, []v4rec{{10, 20, []byte{1}}, {30, 40, []byte{2}}})         // bound [0,499]
	putLeaf(file, 6, 1, []v4rec{{510, 520, []byte{3}}, {530, 540, []byte{4}}})     // bound [500,999]
	putLeaf(file, 7, 1, []v4rec{{1010, 1020, []byte{5}}, {1030, 1040, []byte{6}}}) // bound [1000,1999]
	putLeaf(file, 8, 1, []v4rec{{2010, 2020, []byte{7}}})                          // bound [2000,max]
	putBranch1(file, 3, 5, 500, 6)                                                 // depth-2, covers [0,999]
	putBranch1(file, 4, 7, 2000, 8)                                                // depth-2, covers [1000,max]
	putBranch1(file, 2, 3, 1000, 4)                                                // root, covers [0,max]
	ma := buildMeta(0, V4, 1, 2, 3, total, 7, 2)
	mb := buildMeta(1, V4, 1, 2, 3, total, 7, 1)
	ma.encodeInto(file[:pageSize])
	mb.encodeInto(file[pageSize : 2*pageSize])
	if _, err := Open(file); err != nil {
		t.Fatalf("buildThreeLevelV4 base rejected: %v", err)
	}
	return file
}

// validMultiLevelKVFile builds a v4.1 file whose single scope has a multi-level KV tree (KV
// branch nodes present), for the KV-branch targeted tests.
func validMultiLevelKVFile(t *testing.T) []byte {
	t.Helper()
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	for i := 0; i < 400; i++ { // many entries ⇒ a multi-level KV tree with branch nodes
		must(t, w.MetaSet(a, []byte("k"+strconv.Itoa(i)), 0, []byte(strconv.Itoa(i))))
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if findPageOfType(img, pageTypeKVBranch) == 0 {
		t.Fatal("expected a multi-level KV tree (no KV branch)")
	}
	return img
}

// kvLeaf0 returns the first KV leaf page and entry 0's (slot offset, parsed header) for in-place
// edits of the entry-header / value-descriptor heap bytes.
func kvLeaf0(t *testing.T, img []byte) (page []byte, off int, hdr leafEntryHdr) {
	t.Helper()
	p := findPageOfType(img, pageTypeKVLeaf)
	if p == 0 {
		t.Fatal("no KV leaf")
	}
	page = pageOf(img, p)
	leaf := newKVLeafView(page, int(decodePageHeader(page).entryCount))
	h, err := leaf.entryHdr(0)
	must(t, err)
	o, err := leaf.slot(0)
	must(t, err)
	return page, o, h
}

// kvBranch0 returns the first KV branch page and separator 0's slot offset for in-place edits.
func kvBranch0(t *testing.T, img []byte) (page []byte, off int) {
	t.Helper()
	p := findPageOfType(img, pageTypeKVBranch)
	if p == 0 {
		t.Fatal("no KV branch")
	}
	page = pageOf(img, p)
	bv := newKVBranchView(page, int(decodePageHeader(page).entryCount))
	o, err := bv.slot(0)
	must(t, err)
	return page, o
}

// --- KV slot directory / entry_count ---

func TestKVLeafEmptyRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv leaf empty")
	p := findPageOfType(img, pageTypeKVLeaf)
	if p == 0 {
		t.Fatal("no KV leaf")
	}
	page := pageOf(img, p)
	le.PutUint16(page[phEntryCount:], 0) // a KV leaf with zero entries
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv leaf empty", "kv leaf empty")
}

func TestKVLeafSlotDirectoryOverflowRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv leaf slot dir overflow")
	p := findPageOfType(img, pageTypeKVLeaf)
	if p == 0 {
		t.Fatal("no KV leaf")
	}
	page := pageOf(img, p)
	// 16 (header) + count*2 > 4096 ⇒ count > 2040; the u16 slot directory can't fit the page.
	le.PutUint16(page[phEntryCount:], 2100)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv leaf slot directory overflows page", "kv leaf slot dir overflow")
}

func TestKVBranchEmptyRejected(t *testing.T) {
	img := validMultiLevelKVFile(t)
	mustOpenOK(t, img, "kv branch empty")
	p := findPageOfType(img, pageTypeKVBranch)
	if p == 0 {
		t.Fatal("no KV branch")
	}
	page := pageOf(img, p)
	le.PutUint16(page[phEntryCount:], 0) // a KV branch with zero separators
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv branch has no separators", "kv branch empty")
}

func TestKVBranchSlotDirectoryOverflowRejected(t *testing.T) {
	img := validMultiLevelKVFile(t)
	mustOpenOK(t, img, "kv branch slot dir overflow")
	p := findPageOfType(img, pageTypeKVBranch)
	if p == 0 {
		t.Fatal("no KV branch")
	}
	page := pageOf(img, p)
	// 20 (header + leftmost child) + count*2 > 4096 ⇒ count > 2038.
	le.PutUint16(page[phEntryCount:], 2100)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv branch slot directory overflows page", "kv branch slot dir overflow")
}

// --- KV canonical packing (SOW-0010): a CRC-valid page that shrinks entry_count or an entry's
// length must NOT be accepted as a smaller view. The free-gap check catches an entry_count
// shrink (the dropped slot lands in the must-be-zero gap); the tiling check catches a length
// shrink (an uncovered heap byte). The writer fill(0)s + packs contiguously, so a valid file
// always passes (proven by the byte-identical goldens). ---

// validTwoEntryKVLeafFile builds a v4.1 file whose single scope's KV is one leaf with exactly
// two small inline BINARY entries (type != 0 ⇒ no UTF-8 check perturbs a value-length edit) —
// the minimal page for the entry_count- and value_len-shrink canonical-packing tests.
func validTwoEntryKVLeafFile(t *testing.T) []byte {
	t.Helper()
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	must(t, w.MetaSet(a, []byte("k1"), 9, []byte{1, 2, 3, 4}))
	must(t, w.MetaSet(a, []byte("k2"), 9, []byte{5, 6, 7, 8}))
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	p := findPageOfType(img, pageTypeKVLeaf)
	if p == 0 || decodePageHeader(pageOf(img, p)).entryCount != 2 {
		t.Fatal("expected a single KV leaf with exactly 2 entries")
	}
	if findPageOfType(img, pageTypeKVBranch) != 0 {
		t.Fatal("expected a single-leaf KV tree (got a branch)")
	}
	return img
}

// validSingleEmptyTextKVLeafFile builds a v4.1 file whose single scope's KV is one leaf with a
// single TEXT (type 0) entry holding an EMPTY value. With an empty value, decrementing the
// entry's key_len reparses cleanly (the shifted value_kind reads 0/inline and value_len reads 0)
// yet computes a one-byte-shorter span — a tiling gap reachable from a key_len shrink that stays
// in [1, 1024], distinct from the key_len-out-of-range test.
func validSingleEmptyTextKVLeafFile(t *testing.T) []byte {
	t.Helper()
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	must(t, w.MetaSet(a, []byte("alpha"), 0, nil)) // type 0 text, empty value ⇒ inline, value_len 0
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	p := findPageOfType(img, pageTypeKVLeaf)
	if p == 0 || decodePageHeader(pageOf(img, p)).entryCount != 1 {
		t.Fatal("expected a single KV leaf with exactly 1 entry")
	}
	return img
}

// validMultiSepKVBranchFile builds a v4.1 file whose single scope's KV tree has a branch with
// >= 2 separators: long fixed-width keys give a low leaf fanout, so a modest entry count yields
// several leaves under one branch. (validMultiLevelKVFile's short keys pack ~200 entries/leaf,
// so its 400 entries make only a 1-separator root branch — too few for the entry_count-shrink
// free-gap test, which needs a dropped slot to remain inside the gap.)
func validMultiSepKVBranchFile(t *testing.T) []byte {
	t.Helper()
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	pad := strings.Repeat("x", 200) // ~210-byte keys ⇒ ~18 entries/leaf
	for i := 0; i < 80; i++ {
		// 1_000_000+i ⇒ fixed-width 7-digit numeric prefix that sorts numerically.
		must(t, w.MetaSet(a, []byte("key-"+strconv.Itoa(1_000_000+i)+"-"+pad), 0, []byte("v")))
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	for p := uint32(2); p < totalPagesOf(img); p++ {
		h := decodePageHeader(pageOf(img, p))
		if h.pageType == pageTypeKVBranch && h.entryCount >= 2 {
			return img
		}
	}
	t.Fatal("expected a KV branch with >= 2 separators")
	return nil
}

func TestKVLeafEntryCountShrinkRejected(t *testing.T) {
	img := validTwoEntryKVLeafFile(t)
	mustOpenOK(t, img, "kv leaf entry_count shrink")
	page := pageOf(img, findPageOfType(img, pageTypeKVLeaf))
	c := decodePageHeader(page).entryCount
	// Drop the last entry: its now-orphaned slot byte(s) lie at the new slot_dir_end, inside the
	// must-be-zero free gap ⇒ the free-gap check rejects (closing the proven wrong-answer hole).
	le.PutUint16(page[phEntryCount:], c-1)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "kv leaf free gap not zero", "kv leaf entry_count shrink")
}

func TestKVBranchEntryCountShrinkRejected(t *testing.T) {
	img := validMultiSepKVBranchFile(t)
	mustOpenOK(t, img, "kv branch entry_count shrink")
	// A KV branch with >= 2 separators, so dropping one strands an orphaned slot in the free gap.
	var brPgno uint32
	for p := uint32(2); p < totalPagesOf(img); p++ {
		h := decodePageHeader(pageOf(img, p))
		if h.pageType == pageTypeKVBranch && h.entryCount >= 2 {
			brPgno = p
			break
		}
	}
	if brPgno == 0 {
		t.Fatal("no KV branch with >= 2 separators")
	}
	page := pageOf(img, brPgno)
	c := decodePageHeader(page).entryCount
	le.PutUint16(page[phEntryCount:], c-1)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "kv branch free gap not zero", "kv branch entry_count shrink")
}

func TestKVInlineValueLenShrinkRejected(t *testing.T) {
	img := validTwoEntryKVLeafFile(t)
	mustOpenOK(t, img, "kv inline value_len shrink")
	page := pageOf(img, findPageOfType(img, pageTypeKVLeaf))
	leaf := newKVLeafView(page, int(decodePageHeader(page).entryCount))
	// Entry 1 is the lower-offset (internal) entry; shrinking its inline value_len by 1 leaves a
	// one-byte hole before entry 0 ⇒ the heap no longer tiles contiguously (tiling check).
	off1, err := leaf.slot(1)
	must(t, err)
	h1, err := leaf.entryHdr(1)
	must(t, err)
	if !h1.inlineOK {
		t.Fatal("expected an inline entry")
	}
	// value_len follows key_len(2)·key·type(4)·value_kind(1).
	le.PutUint32(page[off1+2+len(h1.key)+4+1:], uint32(len(h1.inline)-1))
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv leaf entries not canonically packed", "kv inline value_len shrink")
}

func TestKVLeafKeyLenShrinkLeavesGapRejected(t *testing.T) {
	img := validSingleEmptyTextKVLeafFile(t)
	mustOpenOK(t, img, "kv leaf key_len shrink")
	page, off, _ := kvLeaf0(t, img)
	kl := le.Uint16(page[off:])
	// Shrink key_len by 1 (still in [1, 1024], so it passes the range check). The entry parses
	// one byte shorter ⇒ its span no longer reaches PAGE_SIZE ⇒ the tiling check rejects.
	le.PutUint16(page[off:], kl-1)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv leaf entries not canonically packed", "kv leaf key_len shrink")
}

// --- KV entry-header heap fields (slot dir untouched; the fuzz never reaches these) ---

func TestKVLeafKeyLenOutOfRangeRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv leaf key_len")
	page, off, _ := kvLeaf0(t, img)
	le.PutUint16(page[off:], 2000) // key_len > kvKeyMax (1024)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv key_len out of range", "kv leaf key_len out of range")
}

func TestKVLeafUnknownValueKindRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv value_kind")
	page, off, hdr := kvLeaf0(t, img)
	// value_kind follows key_len(2)·key·type(4); 2 is neither inline (0) nor overflow (1).
	page[off+2+len(hdr.key)+4] = 2
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv leaf unknown value_kind", "kv leaf unknown value_kind")
}

func TestKVBranchSepLenOutOfRangeRejected(t *testing.T) {
	img := validMultiLevelKVFile(t)
	mustOpenOK(t, img, "kv branch sep_len")
	page, off := kvBranch0(t, img)
	le.PutUint16(page[off:], 2000) // sep_len > kvKeyMax (1024)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv sep_len out of range", "kv branch sep_len out of range")
}

// --- KV key / separator hostile text (validated on the read/validate path) ---

func TestKVLeafKeyNULRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv leaf key NUL")
	page, off, _ := kvLeaf0(t, img)
	page[off+2] = 0 // first key byte ⇒ a NUL inside the key
	finalizeChecksum(page)
	mustRejectMsg(t, img, "InvalidInput", "kv key contains NUL", "kv leaf key NUL")
}

func TestKVLeafKeyNonUTF8Rejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv leaf key utf8")
	page, off, _ := kvLeaf0(t, img)
	page[off+2] = 0xFF // first key byte ⇒ invalid UTF-8 (not NUL)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "InvalidInput", "kv key not valid UTF-8", "kv leaf key non-UTF-8")
}

func TestKVBranchSeparatorKeyNULRejected(t *testing.T) {
	img := validMultiLevelKVFile(t)
	mustOpenOK(t, img, "kv branch sep NUL")
	page, off := kvBranch0(t, img)
	page[off+2] = 0 // first separator-key byte ⇒ a NUL
	finalizeChecksum(page)
	mustRejectMsg(t, img, "InvalidInput", "kv key contains NUL", "kv branch separator key NUL")
}

func TestKVBranchSeparatorKeyNonUTF8Rejected(t *testing.T) {
	img := validMultiLevelKVFile(t)
	mustOpenOK(t, img, "kv branch sep utf8")
	page, off := kvBranch0(t, img)
	page[off+2] = 0xFF // first separator-key byte ⇒ invalid UTF-8
	finalizeChecksum(page)
	mustRejectMsg(t, img, "InvalidInput", "kv key not valid UTF-8", "kv branch separator key non-UTF-8")
}

// --- KV overflow descriptor / payload tail ---

func TestKVOverflowTotalLenZeroRejected(t *testing.T) {
	img := validOverflowKVFile(t)
	mustOpenOK(t, img, "kv overflow total_len 0")
	page, off, hdr := kvLeaf0(t, img)
	if hdr.inlineOK {
		t.Fatal("expected an overflow entry")
	}
	// total_len follows key_len(2)·key·type(4)·value_kind(1)·first_pgno(4).
	le.PutUint64(page[off+2+len(hdr.key)+4+1+4:], 0)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv overflow chain for empty value", "kv overflow total_len zero")
}

func TestKVOverflowFirstPgnoOutOfRangeRejected(t *testing.T) {
	img := validOverflowKVFile(t)
	mustOpenOK(t, img, "kv overflow first_pgno")
	page, off, hdr := kvLeaf0(t, img)
	if hdr.inlineOK {
		t.Fatal("expected an overflow entry")
	}
	// first_pgno follows key_len(2)·key·type(4)·value_kind(1); set it to total_pages (>= end).
	le.PutUint32(page[off+2+len(hdr.key)+4+1:], uint32(totalPagesOf(img)))
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv overflow pgno out of range", "kv overflow first_pgno out of range")
}

func TestKVOverflowTextValueNULRejected(t *testing.T) {
	img := validOverflowKVFile(t)
	mustOpenOK(t, img, "kv overflow text NUL")
	ovf := findPageOfType(img, pageTypeOverflow)
	if ovf == 0 {
		t.Fatal("no overflow page")
	}
	page := pageOf(img, ovf)
	page[pageHeaderSize+4+10] = 0 // a NUL mid-payload of a text (type 0) overflow value
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv text value contains NUL", "kv overflow text value NUL")
}

func TestKVOverflowLastPageTailNonzeroRejected(t *testing.T) {
	img := validOverflowKVFile(t)
	mustOpenOK(t, img, "kv overflow last-page tail")
	// The terminal chain page (next_pgno == 0) zero-pads its unused payload tail; corrupt it.
	var last uint32
	for p := uint32(2); p < totalPagesOf(img); p++ {
		pg := pageOf(img, p)
		if decodePageHeader(pg).pageType == pageTypeOverflow && le.Uint32(pg[overflowNextPgno:]) == 0 {
			last = p
			break
		}
	}
	if last == 0 {
		t.Fatal("no terminal overflow page")
	}
	page := pageOf(img, last)
	page[pageSize-1] = 0xFF // the final byte is always within the last page's unused tail
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "kv overflow last-page tail", "kv overflow last-page tail nonzero")
}

// --- scope record (name slot) ---

func TestScopeNameLenGt256Rejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "scope name_len")
	leafPgno := findPageOfType(img, pageTypeScopeLeaf)
	if leafPgno == 0 {
		t.Fatal("no scope leaf")
	}
	page := pageOf(img, leafPgno)
	le.PutUint16(page[pageHeaderSize+scopeRecNameLen:], 257) // record 0 name_len > scopeNameMax (256)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "scope name_len > 256", "scope name_len > 256")
}

func TestScopeNamePaddingNonzeroRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "scope name padding")
	leafPgno := findPageOfType(img, pageTypeScopeLeaf)
	if leafPgno == 0 {
		t.Fatal("no scope leaf")
	}
	page := pageOf(img, leafPgno)
	// A nonzero byte deep in record 0's fixed 256-byte name slot, past any short name and
	// before the slot end ⇒ the name-padding-must-be-zero check fires.
	page[pageHeaderSize+scopeRecName+200] = 0xFF
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "scope name padding", "scope name padding nonzero")
}

// --- meta geometry ---

func TestEmptyTreeRecordCountNonzeroRejected(t *testing.T) {
	img := buildEmptyFile(V4, 1)
	mustOpenOK(t, img, "empty tree record_count")
	editActiveMeta(img, func(mp []byte) { le.PutUint64(mp[metaRecordCount:], 5) })
	mustRejectMsg(t, img, "Invariant", "record_count nonzero for empty tree", "empty tree record_count nonzero")
}

func TestTreeHeightRootPgnoInconsistentRejected(t *testing.T) {
	img := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	mustOpenOK(t, img, "tree_height/root_pgno")
	// tree_height = 0 while root_pgno stays 2 (nonzero): exactly one is zero ⇒ inconsistent.
	editActiveMeta(img, func(mp []byte) { le.PutUint32(mp[metaTreeHeight:], 0) })
	mustRejectMsg(t, img, "Structural", "tree_height/root_pgno inconsistent", "tree_height/root_pgno inconsistent")
}

func TestMetaSizeOutsideRangeLowRejected(t *testing.T) {
	img := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	mustOpenOK(t, img, "meta_size low")
	// version_minor = 2 bypasses the minor-0 (==90) and minor-1 (==94) pins, isolating the
	// generic [90, page_size] bound; meta_size = 89 is below it.
	editActiveMeta(img, func(mp []byte) {
		le.PutUint16(mp[metaVersionMinor:], 2)
		le.PutUint16(mp[metaMetaSize:], 89)
	})
	mustRejectMsg(t, img, "BadMetaSize", "meta_size 89", "meta_size below [90, page_size]")
}

func TestMetaSizeOutsideRangeHighRejected(t *testing.T) {
	img := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	mustOpenOK(t, img, "meta_size high")
	editActiveMeta(img, func(mp []byte) {
		le.PutUint16(mp[metaVersionMinor:], 2)
		le.PutUint16(mp[metaMetaSize:], 4097) // > page_size (4096)
	})
	mustRejectMsg(t, img, "BadMetaSize", "meta_size 4097", "meta_size above [90, page_size]")
}

// --- exact IP separator bounds (distinct from the ordering/misroute tests above) ---

func TestIPSeparatorLeLoRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip separator <= lo")
	page := pageOf(img, ipRootBranch(t, img))
	// At the root, lo is the family minimum (0); a separator of 0 is <= lo. Distinct from the
	// misroute test (sep > lo, which trips the per-record child-bound check instead).
	le.PutUint32(page[ipBranchSepOff(0):], 0)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "separator <= lo", "ip separator <= lo")
}

func TestIPSeparatorGtHiRejected(t *testing.T) {
	img := buildThreeLevelV4(t)
	mustOpenOK(t, img, "ip separator > hi")
	// Inner branch at pgno 3 covers [0, 999] (hi = root_sep - 1). A separator of 1000 stays
	// > lo but exceeds hi ⇒ "separator > hi" — only reachable in a nested branch, since the
	// root branch's hi is the family max.
	page := pageOf(img, 3)
	le.PutUint32(page[ipBranchSepOff(0):], 1000)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "separator > hi", "ip separator > hi")
}

// --- SOW-0010 SYSTEMATIC CLOSURE: every remaining validator rejection gets a dedicated
// exact-message test. The Part-1 coverage matrix (in the SOW) enumerates every `errX(...)` in
// reader.go / scope.go / kv.go and maps each to its test here (or documents it unreachable by a
// single CRC-valid mutation, e.g. a defense-in-depth check another check always pre-empts).
// Pattern unchanged: build a valid (or minimally forged-valid) file, assert it opens, corrupt
// EXACTLY ONE field, re-stamp the page CRC so the structural validator is reached (except the
// checksum-gate tests, which deliberately do NOT re-stamp), assert the EXACT typed error.

// --- forging helpers for the checks only reachable in a nested branch / non-uniform tree ---
// (mirrors buildThreeLevelV4 for the IP tree: a hand-forged but fully CRC-valid base). The
// scope/KV page encoders (writeScopeLeaf/Branch, writeKVLeaf/Branch) are the writer's own, so a
// forged base is packed exactly as a committed file and opens OK — only the one mutation differs.

// buildV41Meta is buildMeta upgraded to the v4.1 metadata contract (minor 1, meta_size 94,
// scope_table_root set) — for forged files that carry a scope table.
func buildV41Meta(pgno uint32, scopeWidth uint8, rootPgno, treeHeight uint32, totalPages, recCount, txn uint64, scopeTableRoot uint32) meta {
	m := buildMeta(pgno, V4, scopeWidth, rootPgno, treeHeight, totalPages, recCount, txn)
	m.versionMinor = versionMinorMetadata
	m.metaSize = metaSizeV41
	m.scopeTableRoot = scopeTableRoot
	return m
}

// kvKV is one (key, binary value) pair for forging a KV leaf (type 9 ⇒ no UTF-8 check).
type kvKV struct{ key, val []byte }

// putKVLeafBin forges a KV leaf page at pgno from kvs (must be sorted by key) via the writer's
// own leaf encoder, so the page is canonically packed and CRC-valid.
func putKVLeafBin(file []byte, pgno uint32, kvs []kvKV) {
	slots := make([]leafSlot, len(kvs))
	for i, e := range kvs {
		slots[i] = leafSlot{key: e.key, typ: 9, value: e.val}
	}
	writeKVLeaf(file[int(pgno)*pageSize:(int(pgno)+1)*pageSize], pgno, slots)
}

// forgeHeight3KVFile forges a uniform height-3 per-scope KV tree (root branch -> 2 inner
// branches -> 4 leaves) so a NESTED KV branch carries a real lo AND a finite hi — the only
// place "kv separator <= lo" / "kv separator >= hi" are reachable (the root branch has lo=b"",
// hi=+inf). Keys a..f route: root sep "d"; inner-4 sep "b" (hi "d"); inner-5 sep "f" (lo "d").
// Pages: 0/1 meta, 2 scope leaf (scope 1, kv_root 3), 3 root KV branch, 4/5 inner KV branches,
// 6..9 KV leaves. Empty IP tree (root_pgno 0).
func forgeHeight3KVFile(t *testing.T) []byte {
	t.Helper()
	const total = 10
	file := make([]byte, total*pageSize)
	writeScopeLeaf(file[2*pageSize:3*pageSize], 2, []scopeRec{{id: 1, name: []byte("s"), kvRoot: 3}})
	putKVLeafBin(file, 6, []kvKV{{[]byte("a"), []byte{1}}})
	putKVLeafBin(file, 7, []kvKV{{[]byte("b"), []byte{2}}, {[]byte("c"), []byte{3}}})
	putKVLeafBin(file, 8, []kvKV{{[]byte("d"), []byte{4}}, {[]byte("e"), []byte{5}}})
	putKVLeafBin(file, 9, []kvKV{{[]byte("f"), []byte{6}}})
	writeKVBranch(file[4*pageSize:5*pageSize], 4, 6, []branchSep{{[]byte("b"), 7}}) // [b"", "d")
	writeKVBranch(file[5*pageSize:6*pageSize], 5, 8, []branchSep{{[]byte("f"), 9}}) // ["d", +inf)
	writeKVBranch(file[3*pageSize:4*pageSize], 3, 4, []branchSep{{[]byte("d"), 5}}) // root
	ma := buildV41Meta(0, 1, 0, 0, total, 0, 2, 2)
	mb := buildV41Meta(1, 1, 0, 0, total, 0, 1, 2)
	ma.encodeInto(file[:pageSize])
	mb.encodeInto(file[pageSize : 2*pageSize])
	if _, err := Open(file); err != nil {
		t.Fatalf("forgeHeight3KVFile base rejected: %v", err)
	}
	return file
}

// putScopeLeaf1 forges a one-record scope leaf (the record's kv_root is 0 ⇒ no KV).
func putScopeLeaf1(file []byte, pgno, id uint32) {
	writeScopeLeaf(file[int(pgno)*pageSize:(int(pgno)+1)*pageSize], pgno,
		[]scopeRec{{id: id, name: []byte(strconv.FormatUint(uint64(id), 10))}})
}

// forgeHeight3ScopeFile forges a uniform height-3 scope table (root branch -> 2 inner branches
// -> 4 single-record leaves) so a NESTED scope branch carries a finite hi — the only place
// "scope separator > hi" is reachable (the root branch's hi is the u32 max). ids 1..4 route:
// root sep 3; inner-3 sep 2 (covers [0,2]); inner-4 sep 4 (covers [3,max]). Pages: 0/1 meta,
// 2 root branch, 3/4 inner branches, 5..8 leaves. Empty IP tree (root_pgno 0).
func forgeHeight3ScopeFile(t *testing.T) []byte {
	t.Helper()
	const total = 9
	file := make([]byte, total*pageSize)
	putScopeLeaf1(file, 5, 1)
	putScopeLeaf1(file, 6, 2)
	putScopeLeaf1(file, 7, 3)
	putScopeLeaf1(file, 8, 4)
	writeScopeBranch(file[3*pageSize:4*pageSize], 3, []uint32{2}, []uint32{5, 6}) // covers [0,2]
	writeScopeBranch(file[4*pageSize:5*pageSize], 4, []uint32{4}, []uint32{7, 8}) // covers [3,max]
	writeScopeBranch(file[2*pageSize:3*pageSize], 2, []uint32{3}, []uint32{3, 4}) // root
	ma := buildV41Meta(0, 1, 0, 0, total, 0, 2, 2)
	mb := buildV41Meta(1, 1, 0, 0, total, 0, 1, 2)
	ma.encodeInto(file[:pageSize])
	mb.encodeInto(file[pageSize : 2*pageSize])
	if _, err := Open(file); err != nil {
		t.Fatalf("forgeHeight3ScopeFile base rejected: %v", err)
	}
	return file
}

// validMultiLevelScopeFile builds a real committed file whose scope table is multi-level (a
// scope branch over >= 2 separators) — for the scope-branch targeted tests.
func validMultiLevelScopeFile(t *testing.T) []byte {
	t.Helper()
	w := CreateV4(1, 0)
	for i := 0; i < scopeLeafMax()*2+1; i++ { // > 2 leaves ⇒ a root branch with >= 2 separators
		_, err := w.ScopeDefine([]byte(strconv.Itoa(i)))
		must(t, err)
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if findPageOfType(img, pageTypeScopeBranch) == 0 {
		t.Fatal("expected a multi-level scope table")
	}
	return img
}

// scopeBranchPgno returns a scope-branch page with >= minSeps separators (the root branch in a
// modest table).
func scopeBranchPgno(t *testing.T, img []byte, minSeps int) uint32 {
	t.Helper()
	for p := uint32(2); p < totalPagesOf(img); p++ {
		h := decodePageHeader(pageOf(img, p))
		if h.pageType == pageTypeScopeBranch && int(h.entryCount) >= minSeps {
			return p
		}
	}
	t.Fatalf("no scope branch with >= %d separators", minSeps)
	return 0
}

// kvBranchPgno returns a KV-branch page with >= minSeps separators.
func kvBranchPgno(t *testing.T, img []byte, minSeps int) uint32 {
	t.Helper()
	for p := uint32(2); p < totalPagesOf(img); p++ {
		h := decodePageHeader(pageOf(img, p))
		if h.pageType == pageTypeKVBranch && int(h.entryCount) >= minSeps {
			return p
		}
	}
	t.Fatalf("no KV branch with >= %d separators", minSeps)
	return 0
}

// --- reader.go (IP tree) gap-closers ---

func TestIPReachablePageChecksumRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip reachable checksum")
	page := pageOf(img, ipLeaves(t, img)[0])
	page[pageHeaderSize] ^= 0xFF // corrupt a reachable IP leaf and DO NOT re-stamp ⇒ CRC gate fires
	mustRejectMsg(t, img, "ChecksumFailed", "reachable page", "ip reachable page checksum")
}

func TestIPExpectedBranchAboveTreeHeightRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip expected branch")
	page := pageOf(img, ipRootBranch(t, img))
	page[phPageType] = pageTypeLeaf // a leaf where the walk expects a branch above tree_height
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "expected branch above tree_height", "ip expected branch above tree_height")
}

func TestIPBranchSeparatorCountOutOfRangeRejected(t *testing.T) {
	img := validIPTreeFile(t)
	mustOpenOK(t, img, "ip branch sep count")
	page := pageOf(img, ipRootBranch(t, img))
	// branch_max(4) == 509; set 510 (> branch_max). The byte-flip fuzz only flips the low byte,
	// so it can never reach > 509 — this needs the full u16.
	le.PutUint16(page[phEntryCount:], uint16(branchMax(4)+1))
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "branch separator count out of range", "ip branch separator count out of range")
}

// --- scope.go gap-closers ---

func TestScopePageChecksumRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "scope checksum")
	page := pageOf(img, findPageOfType(img, pageTypeScopeLeaf))
	page[pageHeaderSize] ^= 0xFF // corrupt a reachable scope page, DO NOT re-stamp
	mustRejectMsg(t, img, "ChecksumFailed", "scope page", "scope page checksum")
}

func TestScopePageHeaderReservedNonzeroRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "scope reserved")
	page := pageOf(img, findPageOfType(img, pageTypeScopeLeaf))
	page[phReserved] = 1
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "scope page header reserved", "scope page header reserved nonzero")
}

func TestScopePageSelfPgnoMismatchRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "scope self-pgno")
	pgno := findPageOfType(img, pageTypeScopeLeaf)
	page := pageOf(img, pgno)
	le.PutUint32(page[phPgno:], pgno+1) // stored pgno != actual
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "scope page self-pgno mismatch", "scope page self-pgno mismatch")
}

func TestScopeLeafEntryCountOutOfRangeRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "scope leaf count")
	page := pageOf(img, findPageOfType(img, pageTypeScopeLeaf))
	le.PutUint16(page[phEntryCount:], uint16(scopeLeafMax()+1)) // > scope_leaf_max
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "scope leaf entry_count out of range", "scope leaf entry_count out of range")
}

func TestScopeLeafTailNonzeroRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "scope leaf tail")
	page := pageOf(img, findPageOfType(img, pageTypeScopeLeaf))
	count := int(decodePageHeader(page).entryCount)
	off := pageHeaderSize + count*scopeRecordSize
	if off >= pageSize {
		t.Fatalf("scope leaf has no tail (off %d)", off)
	}
	page[off] = 0xFF
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "scope leaf tail", "scope leaf tail nonzero")
}

func TestScopeIdsNotSortedDisjointRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "scope ids sorted")
	page := pageOf(img, findPageOfType(img, pageTypeScopeLeaf))
	// records are id 1 (alpha), id 2 (beta) in scope_id order. Bump record 0's id to 3 (> rec 1's
	// id 2, still within [0, max]) ⇒ the global strictly-increasing scope_id check fires. The fuzz
	// excludes these record-id heap bytes.
	le.PutUint32(page[pageHeaderSize+0*scopeRecordSize+scopeRecID:], 3)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "scope ids not sorted/disjoint", "scope ids not sorted/disjoint")
}

func TestScopeBranchSeparatorCountOutOfRangeRejected(t *testing.T) {
	img := validMultiLevelScopeFile(t)
	mustOpenOK(t, img, "scope branch sep count")
	page := pageOf(img, scopeBranchPgno(t, img, 1))
	le.PutUint16(page[phEntryCount:], uint16(scopeBranchMax()+1)) // > scope_branch_max
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "scope branch separator count out of range", "scope branch separator count out of range")
}

func TestScopeBranchTailNonzeroRejected(t *testing.T) {
	img := validMultiLevelScopeFile(t)
	mustOpenOK(t, img, "scope branch tail")
	page := pageOf(img, scopeBranchPgno(t, img, 1))
	s := int(decodePageHeader(page).entryCount)
	off := pageHeaderSize + 4 + s*8 // IPv4-layout branch body: child[0] + s*(sep+child)
	page[off] = 0xFF
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "scope branch tail", "scope branch tail nonzero")
}

func TestScopeSeparatorLeLoRejected(t *testing.T) {
	img := validMultiLevelScopeFile(t)
	mustOpenOK(t, img, "scope separator <= lo")
	page := pageOf(img, scopeBranchPgno(t, img, 1))
	le.PutUint32(page[ipBranchSepOff(0):], 0) // sep 0 <= lo (>= 0 at any node) ⇒ the lower-bound check
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "scope separator <= lo", "scope separator <= lo")
}

func TestScopeSeparatorsNotIncreasingRejected(t *testing.T) {
	img := validMultiLevelScopeFile(t)
	mustOpenOK(t, img, "scope separators increasing")
	page := pageOf(img, scopeBranchPgno(t, img, 2))
	le.PutUint32(page[ipBranchSepOff(1):], le.Uint32(page[ipBranchSepOff(0):])) // sep[1] = sep[0]
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "scope separators not increasing", "scope separators not increasing")
}

func TestScopeChildPgnoOutOfRangeRejected(t *testing.T) {
	img := validMultiLevelScopeFile(t)
	mustOpenOK(t, img, "scope child range")
	page := pageOf(img, scopeBranchPgno(t, img, 1))
	le.PutUint32(page[ipBranchChildOff(0):], uint32(totalPagesOf(img))) // child >= total_pages
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "scope child pgno out of range", "scope child pgno out of range")
}

func TestScopeUnexpectedPageTypeRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "scope unexpected type")
	page := pageOf(img, findPageOfType(img, pageTypeScopeLeaf))
	page[phPageType] = 0xEE // neither scope-branch (4) nor scope-leaf (5)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "unexpected page_type in scope table", "scope unexpected page_type")
}

func TestScopeSeparatorGtHiRejected(t *testing.T) {
	img := forgeHeight3ScopeFile(t)
	mustOpenOK(t, img, "scope separator > hi")
	// Inner branch at pgno 3 covers [0, 2] (hi = root_sep - 1 = 2). A separator of 3 stays > lo
	// but exceeds hi ⇒ "scope separator > hi" — only reachable in a nested scope branch (the root
	// branch's hi is the u32 max).
	page := pageOf(img, 3)
	le.PutUint32(page[ipBranchSepOff(0):], 3)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "scope separator > hi", "scope separator > hi")
}

func TestScopeLeavesAtDifferingDepthsRejected(t *testing.T) {
	img := forgeHeight3ScopeFile(t)
	mustOpenOK(t, img, "scope differing depths")
	// Root child[0] (pgno 3) is an inner branch whose leaves sit at depth 3, fixing leaf_depth=3.
	// Retype root child[1] (pgno 4) from branch to leaf: it is now a leaf at depth 2 ⇒ differing
	// depths (the leaf-depth check is the first thing the scope-leaf arm does).
	page := pageOf(img, 4)
	page[phPageType] = pageTypeScopeLeaf
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "scope leaves at differing depths", "scope leaves at differing depths")
}

// --- kv.go gap-closers ---

func TestKVPageChecksumRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv checksum")
	page := pageOf(img, findPageOfType(img, pageTypeKVLeaf))
	page[pageHeaderSize] ^= 0xFF // corrupt a reachable KV page, DO NOT re-stamp
	mustRejectMsg(t, img, "ChecksumFailed", "kv page", "kv page checksum")
}

func TestKVPageHeaderReservedNonzeroRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv reserved")
	page := pageOf(img, findPageOfType(img, pageTypeKVLeaf))
	page[phReserved] = 1
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "kv page header reserved", "kv page header reserved nonzero")
}

func TestKVPageSelfPgnoMismatchRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv self-pgno")
	pgno := findPageOfType(img, pageTypeKVLeaf)
	page := pageOf(img, pgno)
	le.PutUint32(page[phPgno:], pgno+1)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv page self-pgno mismatch", "kv page self-pgno mismatch")
}

func TestKVLeafEntryOffsetOutOfBoundsRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv leaf entry offset")
	page := pageOf(img, findPageOfType(img, pageTypeKVLeaf))
	le.PutUint16(page[pageHeaderSize:], 0) // slot 0's offset -> 0 (< slot_dir_end) ⇒ out of bounds
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv leaf entry offset out of bounds", "kv leaf entry offset out of bounds")
}

func TestKVBranchEntryOffsetOutOfBoundsRejected(t *testing.T) {
	img := validMultiLevelKVFile(t)
	mustOpenOK(t, img, "kv branch entry offset")
	page := pageOf(img, kvBranchPgno(t, img, 1))
	le.PutUint16(page[kvBranchDirStart:], 0) // separator slot 0's offset -> 0 ⇒ out of bounds
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv branch entry offset out of bounds", "kv branch entry offset out of bounds")
}

func TestKVInlineValueLenTooLargeRejected(t *testing.T) {
	img := validTwoEntryKVLeafFile(t)
	mustOpenOK(t, img, "kv value_len too large")
	page, off, hdr := kvLeaf0(t, img)
	if !hdr.inlineOK {
		t.Fatal("expected an inline entry")
	}
	// GROW value_len far past the page: the bounds-safe cursor (readBytes) must return a typed
	// "kv entry read past page", proving no OOB read / panic. value_len follows key_len(2)·key·
	// type(4)·value_kind(1).
	le.PutUint32(page[off+2+len(hdr.key)+4+1:], 5000)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv entry read past page", "kv inline value_len too large")
}

func TestKVBranchSepLenShrinkRejected(t *testing.T) {
	img := validMultiSepKVBranchFile(t)
	mustOpenOK(t, img, "kv branch sep_len shrink")
	page := pageOf(img, kvBranchPgno(t, img, 2))
	bv := newKVBranchView(page, int(decodePageHeader(page).entryCount))
	off, err := bv.slot(0)
	must(t, err)
	sepLen := le.Uint16(page[off:])
	// Shrink separator 0's sep_len by 1 (still in [1, 1024]): the entry parses one byte shorter, so
	// its heap span no longer tiles to PAGE_SIZE ⇒ the canonical-packing (tiling) check rejects.
	le.PutUint16(page[off:], sepLen-1)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv branch entries not canonically packed", "kv branch sep_len shrink")
}

func TestKVKeysNotSortedDisjointRejected(t *testing.T) {
	img := validTwoEntryKVLeafFile(t)
	mustOpenOK(t, img, "kv keys sorted")
	page := pageOf(img, findPageOfType(img, pageTypeKVLeaf))
	leaf := newKVLeafView(page, int(decodePageHeader(page).entryCount))
	// Entry 0 is the lower-offset (sorted-first) entry with key "k1"; entry 1 is "k2". Rewrite
	// entry 0's key to "k3" (same length ⇒ packing intact, valid UTF-8) so the in-order keys go
	// k3, k2 ⇒ the global strictly-increasing check fires. The fuzz excludes these key heap bytes.
	off0, err := leaf.slot(0)
	must(t, err)
	page[off0+2+1] = '3' // second key byte of "k1" -> '3'
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv keys not sorted/disjoint", "kv keys not sorted/disjoint")
}

func TestKVKeyBelowNodeBoundRejected(t *testing.T) {
	img := validMultiLevelKVFile(t)
	mustOpenOK(t, img, "kv key below bound")
	page, off := kvBranch0(t, img)
	// Raise separator 0's first key byte ("k…" -> "l…"): child[1]'s lo becomes "l…", but its keys
	// all start "k" ⇒ they fall below the node's lower bound. (Mirror of the at/above misroute
	// test, opposite direction.) Separators stay strictly increasing and CRC-valid.
	page[off+2]++
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv key below node bound", "kv key below node bound")
}

func TestKVChildPgnoOutOfRangeRejected(t *testing.T) {
	img := validMultiLevelKVFile(t)
	mustOpenOK(t, img, "kv child range")
	page := pageOf(img, kvBranchPgno(t, img, 1))
	le.PutUint32(page[pageHeaderSize:], uint32(totalPagesOf(img))) // leftmost child >= total_pages
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv child pgno out of range", "kv child pgno out of range")
}

func TestKVUnexpectedPageTypeRejected(t *testing.T) {
	img := validMetaFile(t)
	mustOpenOK(t, img, "kv unexpected type")
	page := pageOf(img, findPageOfType(img, pageTypeKVLeaf))
	page[phPageType] = 0xEE // neither KV-branch (6) nor KV-leaf (7)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv unexpected page_type", "kv unexpected page_type")
}

// kvOverflowHeadTail returns the chain head (the leaf entry's first_pgno) and the terminal page
// (next == 0) of the single overflow value in a validOverflowKVFile.
func kvOverflowHeadTail(t *testing.T, img []byte) (head, tail uint32) {
	t.Helper()
	_, _, hdr := kvLeaf0(t, img)
	if hdr.inlineOK {
		t.Fatal("expected an overflow entry")
	}
	head = hdr.first
	for p := head; ; {
		pg := pageOf(img, p)
		if decodePageHeader(pg).pageType != pageTypeOverflow {
			t.Fatalf("pgno %d not an overflow page", p)
		}
		next := le.Uint32(pg[overflowNextPgno:])
		if next == 0 {
			return head, p
		}
		p = next
	}
}

func TestKVOverflowPageChecksumRejected(t *testing.T) {
	img := validOverflowKVFile(t)
	mustOpenOK(t, img, "kv overflow checksum")
	head, _ := kvOverflowHeadTail(t, img)
	page := pageOf(img, head)
	page[pageHeaderSize+4] ^= 0xFF // corrupt an overflow payload byte, DO NOT re-stamp
	mustRejectMsg(t, img, "ChecksumFailed", "kv overflow page", "kv overflow page checksum")
}

func TestKVOverflowWrongPageTypeRejected(t *testing.T) {
	img := validOverflowKVFile(t)
	mustOpenOK(t, img, "kv overflow type")
	head, _ := kvOverflowHeadTail(t, img)
	page := pageOf(img, head)
	page[phPageType] = pageTypeKVLeaf // not page_type 8 (overflow)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv overflow wrong page_type", "kv overflow wrong page_type")
}

func TestKVOverflowHeaderReservedNonzeroRejected(t *testing.T) {
	img := validOverflowKVFile(t)
	mustOpenOK(t, img, "kv overflow reserved")
	head, _ := kvOverflowHeadTail(t, img)
	page := pageOf(img, head)
	page[phReserved] = 1
	finalizeChecksum(page)
	mustRejectMsg(t, img, "NonZeroReserved", "kv overflow header reserved", "kv overflow header reserved nonzero")
}

func TestKVOverflowSelfPgnoMismatchRejected(t *testing.T) {
	img := validOverflowKVFile(t)
	mustOpenOK(t, img, "kv overflow self-pgno")
	head, _ := kvOverflowHeadTail(t, img)
	page := pageOf(img, head)
	le.PutUint32(page[phPgno:], head+1)
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv overflow self-pgno mismatch", "kv overflow self-pgno mismatch")
}

func TestKVOverflowChainLongerThanLengthRejected(t *testing.T) {
	img := validOverflowKVFile(t)
	mustOpenOK(t, img, "kv overflow chain longer")
	head, tail := kvOverflowHeadTail(t, img)
	page := pageOf(img, tail)
	le.PutUint32(page[overflowNextPgno:], head) // terminal page's next != 0 ⇒ chain longer than length
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv overflow chain longer than length", "kv overflow chain longer than length")
}

func TestKVOverflowChainShorterThanLengthRejected(t *testing.T) {
	img := validOverflowKVFile(t)
	mustOpenOK(t, img, "kv overflow chain shorter")
	head, tail := kvOverflowHeadTail(t, img)
	if head == tail {
		t.Fatal("need a multi-page overflow chain")
	}
	page := pageOf(img, head)
	le.PutUint32(page[overflowNextPgno:], 0) // a non-terminal page's next == 0 ⇒ chain shorter than length
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Structural", "kv overflow chain shorter than length", "kv overflow chain shorter than length")
}

func TestKVSeparatorsNotIncreasingRejected(t *testing.T) {
	img := validMultiSepKVBranchFile(t)
	mustOpenOK(t, img, "kv separators increasing")
	page := pageOf(img, kvBranchPgno(t, img, 2))
	bv := newKVBranchView(page, int(decodePageHeader(page).entryCount))
	off0, err := bv.slot(0)
	must(t, err)
	off1, err := bv.slot(1)
	must(t, err)
	len0 := int(le.Uint16(page[off0:]))
	len1 := int(le.Uint16(page[off1:]))
	if len0 != len1 {
		t.Fatalf("expected equal-length separators (%d != %d)", len0, len1)
	}
	// Overwrite separator 1's key with separator 0's (equal length ⇒ packing intact) ⇒ sep[1] ==
	// sep[0], so the strictly-increasing separator check fires.
	copy(page[off1+2:off1+2+len1], page[off0+2:off0+2+len0])
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv separators not increasing", "kv separators not increasing")
}

func TestKVSeparatorLeLoRejected(t *testing.T) {
	img := forgeHeight3KVFile(t)
	mustOpenOK(t, img, "kv separator <= lo")
	// Inner branch pgno 5 has lo "d" (root's right child). Set its separator "f" -> "d" (<= lo).
	page := pageOf(img, 5)
	bv := newKVBranchView(page, int(decodePageHeader(page).entryCount))
	off, err := bv.slot(0)
	must(t, err)
	page[off+2] = 'd'
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv separator <= lo", "kv separator <= lo")
}

func TestKVSeparatorGeHiRejected(t *testing.T) {
	img := forgeHeight3KVFile(t)
	mustOpenOK(t, img, "kv separator >= hi")
	// Inner branch pgno 4 has hi "d" (root's left child). Set its separator "b" -> "e" (>= hi).
	page := pageOf(img, 4)
	bv := newKVBranchView(page, int(decodePageHeader(page).entryCount))
	off, err := bv.slot(0)
	must(t, err)
	page[off+2] = 'e'
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv separator >= hi", "kv separator >= hi")
}

func TestKVLeavesAtDifferingDepthsRejected(t *testing.T) {
	img := forgeHeight3KVFile(t)
	mustOpenOK(t, img, "kv differing depths")
	// Root child[0] (pgno 4) is an inner branch whose leaves sit at depth 3, fixing leaf_depth=3.
	// Retype root child[1] (pgno 5) from branch to leaf: it is now a leaf at depth 2 ⇒ differing
	// depths (the leaf-depth check is the first thing the KV-leaf arm does).
	page := pageOf(img, 5)
	page[phPageType] = pageTypeKVLeaf
	finalizeChecksum(page)
	mustRejectMsg(t, img, "Invariant", "kv leaves at differing depths", "kv leaves at differing depths")
}
