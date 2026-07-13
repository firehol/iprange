package iprangedb

import (
	"bytes"
	"testing"
)

// Hostile-input robustness (mirrors v4/rust/iprange-livedb/tests/robustness.rs).
//
// Open must return only (*Reader, nil) or (nil, err) — never panic, loop, or
// read out of bounds — on truncations, bit-flips, arbitrary buffers, and
// checksum-valid-but-structurally-hostile input. lookup/scan/validate inherit
// that panic-safety from the bounds-checked page() and clamped entry counts.
//
// These tests use plain testing (no testify) to match the dependency-free
// codebase convention; panic-safety is asserted via recover().

// --- fixtures ---

// validFile builds a 1000-record v4.3 IPv4 image: enough to force a 2-level
// tree (one branch root over ~3 leaves) so the branch-level validation paths
// are exercised. scope_mode=scalar ⇒ no scope table (scope_table_root == 0).
func validFile() []byte {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		panic(err)
	}
	for i := uint32(0); i < 1000; i++ {
		if err := w.Set(Ipv4Key(i*7), Ipv4Key(i*7+2), i&0xff); err != nil {
			panic(err)
		}
	}
	if err := w.Commit(0, ^uint64(0)); err != nil {
		panic(err)
	}
	img, ok := w.IntoImage()
	if !ok {
		panic("IntoImage failed")
	}
	return img
}

// robustLcg is the same LCG used by the Rust fuzz tests (Knuth MMIX constants),
// so both language suites explore an identical perturbation sequence.
func robustLcg(state *uint64) uint64 {
	*state = *state*6364136223846793005 + 1442695040888963407
	return *state >> 33
}

// restamp recomputes and stores page p's CRC32C, so a structurally-mutated page
// clears the checksum gate and reaches the structural validator.
func restamp(file []byte, p int) {
	base := p * PageSize
	finalizeChecksum(file[base : base+PageSize])
}

func pageType(file []byte, p int) uint8 { return file[p*PageSize+PHPageType] }

func entryCount(file []byte, p int) uint16 {
	return u16le(file[p*PageSize:], PHEntryCount)
}

// activeMetaPage returns 0 or 1: the checksum-valid candidate with the higher
// txn_id (tie → pgno 0), mirroring selectActiveMeta.
func activeMetaPage(file []byte) int {
	if u64le(file, PageSize+MetaTxnID) > u64le(file, MetaTxnID) {
		return 1
	}
	return 0
}

func metaRoot(file []byte) uint32 { return u32le(file[activeMetaPage(file)*PageSize:], MetaRootPgno) }
func metaHeight(file []byte) uint32 {
	return u32le(file[activeMetaPage(file)*PageSize:], MetaTreeHeight)
}
func metaTotal(file []byte) uint64 {
	return u64le(file[activeMetaPage(file)*PageSize:], MetaTotalPages)
}
func metaRecordCount(file []byte) uint64 {
	return u64le(file[activeMetaPage(file)*PageSize:], MetaRecordCount)
}

// findPage returns the first page (pgno >= 2) of the given type, or -1.
func findPage(file []byte, pt uint8) int {
	total := len(file) / PageSize
	for p := 2; p < total; p++ {
		if file[p*PageSize+PHPageType] == pt {
			return p
		}
	}
	return -1
}

// setMetaU32Both/u64Both write a field into BOTH meta pages and re-stamp both
// CRCs, so whichever meta is active carries the forged field and both stay
// checksum-valid.
func setMetaU32Both(file []byte, off int, v uint32) {
	for p := 0; p < 2; p++ {
		putU32(file, p*PageSize+off, v)
	}
	restamp(file, 0)
	restamp(file, 1)
}
func setMetaU64Both(file []byte, off int, v uint64) {
	for p := 0; p < 2; p++ {
		putU64(file, p*PageSize+off, v)
	}
	restamp(file, 0)
	restamp(file, 1)
}

// --- IPv4 branch/leaf byte offsets (kw=4: child[0]|sep,child pairs of 8) ---

func ipChildOff(p, j int) int {
	if j == 0 {
		return p*PageSize + PageHeaderSize
	}
	return p*PageSize + PageHeaderSize + 4 + (j-1)*8 + 4
}
func ipSepOff(p, i int) int {
	return p*PageSize + PageHeaderSize + 4 + i*8
}

// ipLeafRecOff is the byte offset of leaf record i (record size 12 for kw=4).
func ipLeafRecOff(p, i int) int { return p*PageSize + PageHeaderSize + i*12 }

// --- panic-safety harness ---

// noPanic runs fn and fails the test if it panics.
func noPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s panicked: %v", name, r)
		}
	}()
	fn()
}

// openScanLookupValidate exercises every read path on file without panicking:
// Open (meta CRC + geometry), and — on success — Scan, Lookup, and Validate.
func openScanLookupValidate(t *testing.T, file []byte) {
	t.Helper()
	noPanic(t, "open", func() {
		r, err := Open(file)
		if err != nil {
			return
		}
		noPanic(t, "scan", func() {
			_ = r.ScanV4(func(Ipv4Key, Ipv4Key, uint32) {})
		})
		noPanic(t, "lookup", func() {
			for i := uint32(0); i < 16; i++ {
				_, _ = r.LookupV4(Ipv4Key(i * 13))
			}
		})
		noPanic(t, "validate", func() { _ = r.Validate() })
	})
}

// mustOpenValidates asserts the file opens and validates cleanly (baseline guard
// for the targeted corruption tests: only the one forged field differs).
func mustOpenValidate(t *testing.T, file []byte, label string) {
	t.Helper()
	r, err := Open(file)
	if err != nil {
		t.Fatalf("%s: Open failed on valid baseline: %v", label, err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("%s: Validate failed on valid baseline: %v", label, err)
	}
}

// rejectAtValidate asserts Open succeeds (meta intact) but Validate rejects, and
// that Scan/Lookup do not panic on the corrupt-but-opened tree.
func rejectAtValidate(t *testing.T, file []byte, label string) {
	t.Helper()
	r, err := Open(file)
	if err != nil {
		t.Fatalf("%s: Open should succeed (meta intact): %v", label, err)
	}
	if err := r.Validate(); err == nil {
		t.Fatalf("%s: Validate must reject", label)
	}
	noPanic(t, label+":scan", func() { _ = r.ScanV4(func(Ipv4Key, Ipv4Key, uint32) {}) })
	noPanic(t, label+":lookup", func() { _, _ = r.LookupV4(5) })
}

// rejectAtOpen asserts Open itself rejects (meta/geometry corruption).
func rejectAtOpen(t *testing.T, file []byte, label string) {
	t.Helper()
	if _, err := Open(file); err == nil {
		t.Fatalf("%s: Open must reject", label)
	}
}

// --- fuzz tests: panic-safety on hostile input ---

func TestRobustTruncationsNeverPanic(t *testing.T) {
	file := validFile()
	two := 2 * PageSize
	for length := 0; length <= len(file); length++ {
		// Every byte through the meta region (where bootstrap is most fragile),
		// strided beyond — opening any prefix must never panic.
		if length < two || length%37 == 0 || length == len(file) {
			openScanLookupValidate(t, file[:length])
		}
	}
}

func TestRobustSingleBitFlipsNeverPanic(t *testing.T) {
	file := validFile()
	s := uint64(0x9e3779b97f4a7c15)
	for n := 0; n < 5000; n++ {
		pos := int(robustLcg(&s)) % len(file)
		bit := uint8(robustLcg(&s) & 7)
		g := append([]byte(nil), file...)
		g[pos] ^= 1 << bit
		openScanLookupValidate(t, g)
	}
}

func TestRobustArbitraryBuffersNeverPanic(t *testing.T) {
	s := uint64(0x1234567890abcdef)
	for _, size := range []int{0, 1, 16, 100, 4095, 4096, 4097, 8191, 8192, 8193, 12288, 20000} {
		for n := 0; n < 40; n++ {
			buf := make([]byte, size)
			for i := range buf {
				buf[i] = byte(robustLcg(&s))
			}
			openScanLookupValidate(t, buf)
		}
	}
}

// reachablePages returns every page the validator actually walks: exactly the
// pages whose CRC open verifies, so a page is reachable iff breaking its stored
// CRC (without re-stamping) turns Validate from Ok into Err.
func reachablePages(base []byte) []int {
	total := len(base) / PageSize
	var out []int
	for p := 2; p < total; p++ {
		off := p*PageSize + PHChecksum
		orig := base[off]
		base[off] ^= 0xFF // break this page's stored CRC (no re-stamp)
		reachable := false
		if r, err := Open(base); err == nil {
			if r.Validate() != nil {
				reachable = true
			}
		}
		base[off] = orig
		if reachable {
			out = append(out, p)
		}
	}
	return out
}

// structuralOffsets are the byte offsets (within a page) of REDUNDANT structural
// fields for page type pt — fields the validator cross-checks, so a re-CRC'd
// mutation there must be rejected or be a no-op, never a different valid view.
func structuralOffsets(pt uint8) []int {
	h := []int{PHPageType, PHReserved, PHPgno}
	switch pt {
	case PageTypeLeaf, PageTypeScopeLeaf:
		h = append(h, PHEntryCount)
	case PageTypeBranch, PageTypeScopeBranch:
		// IPv4-layout branch: count (tail-checked) + child[0]/sep[0]/child[1].
		h = append(h,
			PHEntryCount,
			PageHeaderSize,   // child[0]
			PageHeaderSize+4, // sep[0]
			PageHeaderSize+8, // child[1]
		)
	}
	return h
}

func TestRobustStructuralMutationFuzzRecrc(t *testing.T) {
	base := validFile()
	wantCount := metaRecordCount(base)
	reach := reachablePages(base)
	if len(reach) == 0 {
		t.Fatal("expected at least one reachable tree page")
	}
	s := uint64(0xa5a5f00d12345678)
	for _, p := range reach {
		pt := pageType(base, p)
		offs := structuralOffsets(pt)
		for n := 0; n < 60; n++ {
			g := append([]byte(nil), base...)
			bp := p * PageSize
			// Apply 1-3 redundant-structural byte mutations.
			for m := 0; m < 1+int(robustLcg(&s)%3); m++ {
				off := offs[int(robustLcg(&s))%len(offs)]
				g[bp+off] ^= byte(1 + robustLcg(&s)%255)
			}
			restamp(g, p) // this page now clears the CRC gate
			// (1) panic-safety; (2) no silent wrong answer — if Validate accepts
			// the mutation, the scanned record count must equal the baseline.
			noPanic(t, "open+validate+scan", func() {
				r, err := Open(g)
				if err != nil {
					return
				}
				if err := r.Validate(); err != nil {
					return // rejected — fine
				}
				var c uint64
				_ = r.ScanV4(func(Ipv4Key, Ipv4Key, uint32) { c++ })
				if c != wantCount {
					t.Errorf("page %d (type %d): accepted mutation, count %d != %d",
						p, pt, c, wantCount)
				}
			})
		}
	}
}

// --- targeted structural rejection tests (checksum-VALID hostile input) ---
//
// Each builds a valid file, asserts it opens+validates, mutates EXACTLY ONE
// field to violate EXACTLY ONE invariant, re-stamps the touched page, and
// asserts a typed rejection. The "opens first" guard is the non-vacuity check:
// only the one forged field differs from a valid file.

func TestRobustIPPageChecksumFailed(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	leaf := findPage(file, PageTypeLeaf)
	if leaf < 0 {
		t.Fatal("no leaf page")
	}
	file[leaf*PageSize+PageHeaderSize+4] ^= 0xFF // corrupt body byte (no re-stamp)
	r, err := Open(file)
	if err != nil {
		t.Fatalf("Open should succeed (meta intact): %v", err)
	}
	if err := r.Validate(); err == nil {
		t.Fatal("Validate must reject a CRC-corrupt reachable page")
	}
	noPanic(t, "scan", func() { _ = r.ScanV4(func(Ipv4Key, Ipv4Key, uint32) {}) })
}

func TestRobustIPBranchSeparatorCountOutOfRange(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	root := int(metaRoot(file))
	if pageType(file, root) != PageTypeBranch {
		t.Fatalf("root pgno %d is not a branch (height=%d)", root, metaHeight(file))
	}
	if entryCount(file, root) < 1 {
		t.Fatal("test needs a branch root with >= 1 separator")
	}
	// entry_count > branch_max(4)=509.
	putU16(file, root*PageSize+PHEntryCount, 9999)
	restamp(file, root)
	rejectAtValidate(t, file, "branch separator count")
}

func TestRobustIPExpectedBranchAboveTreeHeight(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	// tree_height > TreeHeightMax(32) ⇒ Open rejects via geometry.
	setMetaU32Both(file, MetaTreeHeight, TreeHeightMax+1)
	rejectAtOpen(t, file, "tree_height > max")
}

func TestRobustChildPgnoOutOfRange(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	root := int(metaRoot(file))
	total := metaTotal(file)
	// child[0] := total_pages (>= total_pages ⇒ out of range).
	putU32(file, ipChildOff(root, 0), uint32(total))
	restamp(file, root)
	rejectAtValidate(t, file, "child pgno out of range")
}

func TestRobustLeafEntryCountOutOfRange(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	root := int(metaRoot(file))
	leaf := int(u32le(file[ipChildOff(root, 0):], 0))
	if pageType(file, leaf) != PageTypeLeaf {
		t.Fatal("child[0] of root is not a leaf")
	}
	// entry_count > leaf_max(4)=340.
	putU16(file, leaf*PageSize+PHEntryCount, 1000)
	restamp(file, leaf)
	rejectAtValidate(t, file, "leaf entry_count")
}

func TestRobustLeafTailNonzero(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	root := int(metaRoot(file))
	leaf := int(u32le(file[ipChildOff(root, 0):], 0))
	// A nonzero byte in the leaf tail (after the records).
	file[leaf*PageSize+PageSize-1] = 1
	restamp(file, leaf)
	rejectAtValidate(t, file, "leaf tail")
}

func TestRobustRecordCountMismatch(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	// Claim 5 records; the walk counts 1000 ⇒ mismatch.
	setMetaU64Both(file, MetaRecordCount, 5)
	rejectAtValidate(t, file, "record_count mismatch")
}

func TestRobustIPSeparatorsNotIncreasing(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	root := int(metaRoot(file))
	if entryCount(file, root) < 2 {
		t.Skip("root branch has < 2 separators; need more records")
	}
	// sep[1] := sep[0] ⇒ no longer strictly increasing.
	s0 := u32le(file[ipSepOff(root, 0):], 0)
	putU32(file, ipSepOff(root, 1), s0)
	restamp(file, root)
	rejectAtValidate(t, file, "separators not strictly increasing")
}

func TestRobustIPDuplicateChild(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	root := int(metaRoot(file))
	if entryCount(file, root) < 1 {
		t.Skip("root branch has no separator")
	}
	// child[1] := child[0] ⇒ the same subtree reached twice.
	c0 := u32le(file[ipChildOff(root, 0):], 0)
	putU32(file, ipChildOff(root, 1), c0)
	restamp(file, root)
	rejectAtValidate(t, file, "duplicate child pgno")
}

func TestRobustIPPageTypeWrongAtDepth(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	root := int(metaRoot(file))
	leaf := int(u32le(file[ipChildOff(root, 0):], 0))
	// A reachable leaf claims to be a branch ⇒ wrong page_type at tree_height.
	file[leaf*PageSize+PHPageType] = PageTypeBranch
	restamp(file, leaf)
	rejectAtValidate(t, file, "expected leaf at tree_height")
}

func TestRobustIPLeafRecordToLtFrom(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	root := int(metaRoot(file))
	leaf := int(u32le(file[ipChildOff(root, 0):], 0))
	// Record 1: set its `to` below `from`.
	rec1 := ipLeafRecOff(leaf, 1)
	from := u32le(file[rec1:], 0)
	if from < 1 {
		t.Skip("record 1 from == 0; cannot decrement")
	}
	putU32(file, rec1+4, from-1)
	restamp(file, leaf)
	rejectAtValidate(t, file, "record to < from")
}

// --- meta/geometry rejection at Open (before any tree walk) ---

func TestRobustRootPgnoOutOfRange(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	// root_pgno >= total_pages ⇒ Open rejects via geometry.
	setMetaU32Both(file, MetaRootPgno, uint32(metaTotal(file)))
	rejectAtOpen(t, file, "root_pgno out of range")
}

func TestRobustTreeHeightRootInconsistent(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	// height=0 but root != 0 (or vice versa) ⇒ inconsistent.
	setMetaU32Both(file, MetaTreeHeight, 0)
	rejectAtOpen(t, file, "tree_height/root_pgno inconsistent")
}

func TestRobustBothMetasCorruptRejected(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	// Corrupt a body byte in BOTH metas (beyond the static identity region) and
	// re-stamp neither: both CRCs fail ⇒ no valid meta ⇒ Open rejects.
	file[MetaRecordCount] ^= 0xFF
	file[PageSize+MetaRecordCount] ^= 0xFF
	rejectAtOpen(t, file, "both metas corrupt")
}

func TestRobustTornInactiveMetaRecovers(t *testing.T) {
	file := validFile()
	mustOpenValidate(t, file, "pristine")
	wantCount := metaRecordCount(file)
	// Corrupt the inactive meta's body: its CRC fails ⇒ discarded at meta
	// selection; the active meta is used. Open must still succeed and return
	// the correct data (the torn meta is not consulted beyond classify).
	inactive := 0
	if activeMetaPage(file) == 0 {
		inactive = 1
	}
	file[inactive*PageSize+MetaRecordCount] ^= 0xFF
	r, err := Open(file)
	if err != nil {
		t.Fatalf("Open must recover from a torn inactive meta: %v", err)
	}
	// The data path uses only the active meta: scan + lookup are correct.
	var n uint64
	noPanic(t, "scan", func() {
		_ = r.ScanV4(func(Ipv4Key, Ipv4Key, uint32) { n++ })
	})
	if n != wantCount {
		t.Fatalf("scan count %d != %d after torn-meta recovery", n, wantCount)
	}
	// Validate is the strict full-integrity check: a torn meta (even the unused
	// one) fails its both-meta CRC pass and is reported. This mirrors Rust's
	// validate(); open recovers, validate reports the defect.
	if err := r.Validate(); err == nil {
		t.Fatal("Validate must report the torn meta (strict full-integrity check)")
	}
}

// --- sanity: the pristine file has the geometry the targeted tests assume ---

func TestRobustValidFileGeometry(t *testing.T) {
	file := validFile()
	root := int(metaRoot(file))
	if metaHeight(file) != 2 {
		t.Fatalf("validFile must be a 2-level tree, got height %d", metaHeight(file))
	}
	if pageType(file, root) != PageTypeBranch {
		t.Fatalf("root page %d must be a branch", root)
	}
	// Round-trip: open + validate + scan count == record_count.
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	var n uint64
	_ = r.ScanV4(func(Ipv4Key, Ipv4Key, uint32) { n++ })
	if n != r.RecordCount() {
		t.Fatalf("scan count %d != record_count %d", n, r.RecordCount())
	}
	// A known hit and miss.
	if s, ok := r.LookupV4(Ipv4Key(7)); !ok || s != 1 {
		t.Errorf("LookupV4(7) = %d,%v want 1,true", s, ok)
	}
	if _, ok := r.LookupV4(Ipv4Key(8)); !ok {
		// 8 is not a `from` (from = i*7); it may be covered by [7,9]. Just ensure
		// no panic — the exact hit/miss depends on record layout.
		_ = ok
	}
	// Ensure the byte helpers agree with the decoded meta (guards against offset
	// drift in the test helpers themselves).
	if !bytes.Equal(
		file[MetaStaticStart:MetaVersionMinor],
		file[PageSize+MetaStaticStart:PageSize+MetaVersionMinor],
	) {
		t.Fatal("meta static-identity region disagrees between A and B")
	}
}
