package iprangedb

import (
	"bytes"
	"testing"
)

// --- file builders (forge pages directly, like the Rust reader tests) ---

type v4rec struct {
	from, to Ipv4Key
	scope    []byte
}

func buildMeta(pgno uint32, version IPVersion, scopeWidth uint8, root, height uint32, totalPages, recCount, txn uint64) meta {
	return meta{
		pgno:            pgno,
		versionMinor:    0,
		metaSize:        metaSize,
		pageSize:        pageSize,
		checksumAlgo:    checksumAlgoCRC32C,
		flags:           version.flag(),
		keyWidth:        version.keyWidth(),
		scopeWidth:      scopeWidth,
		recordSize:      recordSize(version.keyWidth(), scopeWidth),
		createdUnixtime: 0,
		rootPgno:        root,
		treeHeight:      height,
		totalPages:      totalPages,
		recordCount:     recCount,
		txnID:           txn,
		updatedUnixtime: 0,
	}
}

func putLeaf(file []byte, pgno uint32, scopeWidth uint8, records []v4rec) {
	rs := int(recordSize(4, scopeWidth))
	base := int(pgno) * pageSize
	page := file[base : base+pageSize]
	writePageHeader(page, pageTypeLeaf, uint16(len(records)), pgno)
	for i, r := range records {
		off := pageHeaderSize + i*rs
		recordWrite[Ipv4Key](page[off:off+rs], r.from, r.to, r.scope)
	}
	finalizeChecksum(page)
}

// 3 pages: meta-A (active, txn 2), meta-B (txn 1), one root leaf at pgno 2.
func buildSingleLeaf(version IPVersion, scopeWidth uint8, records []v4rec) []byte {
	file := make([]byte, 3*pageSize)
	putLeaf(file, 2, scopeWidth, records)
	rc := uint64(len(records))
	ma := buildMeta(0, version, scopeWidth, 2, 1, 3, rc, 2)
	mb := buildMeta(1, version, scopeWidth, 2, 1, 3, rc, 1)
	ma.encodeInto(file[:pageSize])
	mb.encodeInto(file[pageSize : 2*pageSize])
	return file
}

func buildEmptyFile(version IPVersion, scopeWidth uint8) []byte {
	file := make([]byte, 2*pageSize)
	ma := buildMeta(0, version, scopeWidth, 0, 0, 2, 0, 2)
	mb := buildMeta(1, version, scopeWidth, 0, 0, 2, 0, 1)
	ma.encodeInto(file[:pageSize])
	mb.encodeInto(file[pageSize:])
	return file
}

// 5 pages: metas, a root branch at pgno 2 (one separator), leaves at pgno 3/4.
func buildTwoLevel(version IPVersion, scopeWidth uint8, sep Ipv4Key, left, right []v4rec) []byte {
	file := make([]byte, 5*pageSize)
	putLeaf(file, 3, scopeWidth, left)
	putLeaf(file, 4, scopeWidth, right)
	{
		page := file[2*pageSize : 3*pageSize]
		writePageHeader(page, pageTypeBranch, 1, 2)
		le.PutUint32(page[pageHeaderSize:], 3) // child[0]
		sepOff := pageHeaderSize + 4
		sep.writeLE(page[sepOff : sepOff+4])
		le.PutUint32(page[sepOff+4:], 4) // child[1]
		finalizeChecksum(page)
	}
	rc := uint64(len(left) + len(right))
	ma := buildMeta(0, version, scopeWidth, 2, 2, 5, rc, 2)
	mb := buildMeta(1, version, scopeWidth, 2, 2, 5, rc, 1)
	ma.encodeInto(file[:pageSize])
	mb.encodeInto(file[pageSize : 2*pageSize])
	return file
}

func mustLookupV4(t *testing.T, r *Reader, ip Ipv4Key) ([]byte, bool) {
	t.Helper()
	s, ok, err := r.LookupV4(ip)
	if err != nil {
		t.Fatalf("lookup %d: %v", ip, err)
	}
	return s, ok
}

func TestSingleLeafLookupAndScan(t *testing.T) {
	recs := []v4rec{{10, 20, []byte{1}}, {30, 40, []byte{2}}}
	file := buildSingleLeaf(V4, 1, recs)
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version() != V4 || r.RecordCount() != 2 || r.IsEmpty() {
		t.Fatal("metadata")
	}
	cases := []struct {
		ip   Ipv4Key
		want []byte
	}{
		{15, []byte{1}}, {10, []byte{1}}, {20, []byte{1}}, {25, nil},
		{30, []byte{2}}, {40, []byte{2}}, {9, nil}, {41, nil},
	}
	for _, c := range cases {
		s, ok := mustLookupV4(t, r, c.ip)
		if c.want == nil {
			if ok {
				t.Errorf("lookup %d: expected miss", c.ip)
			}
		} else if !ok || !bytes.Equal(s, c.want) {
			t.Errorf("lookup %d = %x ok=%v, want %x", c.ip, s, ok, c.want)
		}
	}
	var seen [][3]int
	r.ScanV4(func(f, to Ipv4Key, s []byte) { seen = append(seen, [3]int{int(f), int(to), int(s[0])}) })
	want := [][3]int{{10, 20, 1}, {30, 40, 2}}
	if len(seen) != 2 || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("scan = %v", seen)
	}
}

func TestEmptyTree(t *testing.T) {
	file := buildEmptyFile(V4, 1)
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsEmpty() || r.RecordCount() != 0 {
		t.Fatal("not empty")
	}
	if _, ok := mustLookupV4(t, r, 5); ok {
		t.Fatal("empty lookup must miss")
	}
	n := 0
	r.ScanV4(func(Ipv4Key, Ipv4Key, []byte) { n++ })
	if n != 0 {
		t.Fatal("empty scan")
	}
}

func TestTwoLevelLookupCrossesLeaves(t *testing.T) {
	left := []v4rec{{10, 20, []byte{1}}, {50, 60, []byte{2}}}
	right := []v4rec{{100, 110, []byte{3}}, {200, 210, []byte{4}}}
	file := buildTwoLevel(V4, 1, 100, left, right)
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 4 {
		t.Fatal("count")
	}
	checks := []struct {
		ip   Ipv4Key
		want []byte
	}{
		{15, []byte{1}}, {55, []byte{2}}, {105, []byte{3}}, {205, []byte{4}},
		{70, nil}, {150, nil},
	}
	for _, c := range checks {
		s, ok := mustLookupV4(t, r, c.ip)
		if c.want == nil {
			if ok {
				t.Errorf("lookup %d expected miss", c.ip)
			}
		} else if !ok || !bytes.Equal(s, c.want) {
			t.Errorf("lookup %d = %x, want %x", c.ip, s, c.want)
		}
	}
	var seen []int
	r.ScanV4(func(f, _ Ipv4Key, _ []byte) { seen = append(seen, int(f)) })
	if len(seen) != 4 || seen[0] != 10 || seen[1] != 50 || seen[2] != 100 || seen[3] != 200 {
		t.Fatalf("scan order = %v", seen)
	}
}

func TestTornInactiveMetaRecovers(t *testing.T) {
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	file[pageSize+200] ^= 0xFF // corrupt inactive meta -> class 1, discarded
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := mustLookupV4(t, r, 15); !ok || s[0] != 1 {
		t.Fatal("did not recover from torn inactive meta")
	}
}

func TestBothMetasCorruptRejects(t *testing.T) {
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	file[200] ^= 0xFF
	file[pageSize+200] ^= 0xFF
	// Trusted Open skips the meta CRC, so with magic intact the file opens OK; Validate
	// catches the CRC corruption in both meta bodies (exact message, not just the class).
	mustRejectMsg(t, file, "ChecksumFailed", "meta page", "both metas corrupt")
}

func TestIncompatibleMajorFailsClosed(t *testing.T) {
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	page := file[:pageSize]
	le.PutUint16(page[metaVersionMajor:], 5)
	finalizeChecksum(page)
	if _, err := Open(file); errorClass(err) != "UnsupportedMajor" {
		t.Fatalf("expected UnsupportedMajor, got %v", err)
	}
}

func TestMalformedUnsortedLeafRejects(t *testing.T) {
	// Records written out of order -> the validate walk rejects (exact message, not just the class).
	file := buildSingleLeaf(V4, 1, []v4rec{{30, 40, []byte{2}}, {10, 20, []byte{1}}})
	mustRejectMsg(t, file, "Invariant", "leaf records not sorted/disjoint", "unsorted leaf records")
}

func TestRecordCountMismatchRejects(t *testing.T) {
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	page := file[:pageSize]
	le.PutUint64(page[metaRecordCount:], 5)
	finalizeChecksum(page)
	// The full-pass walk counts 1 record vs the declared 5 (exact message, not just the class).
	mustRejectMsg(t, file, "Invariant", "record_count mismatch", "record_count mismatch")
}

func TestLookupFamilyMismatchErrors(t *testing.T) {
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.LookupV6(Ipv6Key{Hi: 0, Lo: 5}); errorClass(err) != "InvalidInput" {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

func TestTruncatedFileRejects(t *testing.T) {
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	// Drop the leaf page: total_pages (3) exceeds the file -> reject.
	if _, err := Open(file[:2*pageSize]); errorClass(err) != "FileTooShort" {
		t.Fatalf("expected FileTooShort dropping leaf, got %v", err)
	}
	// Sub-2-page file -> reject.
	if _, err := Open(file[:pageSize]); errorClass(err) != "FileTooShort" {
		t.Fatalf("expected FileTooShort sub-2-page, got %v", err)
	}
}

func TestNonMultipleOfPageSizeRejects(t *testing.T) {
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	if _, err := Open(file[:len(file)-1]); errorClass(err) != "FileSizeMismatch" {
		t.Fatalf("expected FileSizeMismatch, got %v", err)
	}
}

func TestMetaTailNonzeroRejected(t *testing.T) {
	// A crafted non-zero byte in the active meta's reserved tail [metaSize, pageSize),
	// with the CRC recomputed, must be rejected (§5/§9).
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}})
	file[metaSize+7] = 0xAB // meta-A (active, txn 2) reserved tail
	finalizeChecksum(file[:pageSize])
	// The active meta's reserved tail must be zero (exact message, not just the class).
	mustRejectMsg(t, file, "NonZeroReserved", "meta tail", "meta tail nonzero")
}

// TestMinor1MetaSizePinned checks F7: at version_minor == 1 the reader requires meta_size
// exactly 94 (the v4.1 size), mirroring the minor-0 rule. A minor-1 meta declaring any other
// (otherwise in-range) meta_size is rejected as BadMetaSize even with a valid CRC.
func TestMinor1MetaSizePinned(t *testing.T) {
	// A real v4.1 file: any metadata write upgrades it to minor 1 / meta_size 94.
	w := CreateV4(1, 0)
	if _, err := w.ScopeDefine([]byte("s")); err != nil {
		t.Fatal(err)
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if activeMetaOf(img).versionMinor != versionMinorMetadata {
		t.Fatalf("expected a v4.1 file, got minor %d", activeMetaOf(img).versionMinor)
	}
	// Patch BOTH metas' meta_size away from 94 (still in [90, page_size]) keeping minor 1,
	// re-stamp each CRC. Both must be rejected (neither classifies as valid).
	for _, badSize := range []uint16{90, 92, 95, 100} {
		g := append([]byte(nil), img...)
		for p := 0; p < 2; p++ {
			le.PutUint16(g[p*pageSize+metaMetaSize:], badSize)
			finalizeChecksum(g[p*pageSize : (p+1)*pageSize])
		}
		if _, err := Open(g); errorClass(err) != "BadMetaSize" {
			t.Fatalf("minor1 meta_size=%d: expected BadMetaSize, got %v", badSize, err)
		}
	}
}
