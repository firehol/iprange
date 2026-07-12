package iprangedb

import (
	"testing"
)

// --- file builders (forge pages directly, like the Rust reader tests) ---
//
// Adapted to v4.3: scope_id is a fixed u32 (scope_mode=0/scalar). These forge minimal
// committed images to exercise the Reader directly, bypassing the Writer.

type v4rec struct {
	from, to Ipv4Key
	scopeID  uint32
}

func buildMeta(pgno uint32, version IPVersion, scopeMode uint8, root, height uint32, totalPages, recCount, txn uint64) meta {
	return meta{
		pgno:         pgno,
		versionMinor: VersionMinor,
		metaSize:     MetaSize,
		pageSize:     PageSize,
		checksumAlgo: ChecksumAlgoCRC32C,
		flags:        version.Flag(),
		keyWidth:     version.KeyWidth(),
		scopeMode:    scopeMode,
		recordSize:   recordSize(version.KeyWidth()),
		createdUnix:  0,
		rootPgno:     root,
		treeHeight:   height,
		totalPages:   totalPages,
		recordCount:  recCount,
		txnID:        txn,
		updatedUnix:  0,
		freeListHead: 0,
	}
}

func putLeaf(file []byte, pgno uint32, records []v4rec) {
	rs := int(recordSize(4)) // IPv4: 12
	base := int(pgno) * PageSize
	page := file[base : base+PageSize]
	writeHeader(page, PageTypeLeaf, uint16(len(records)), pgno)
	var fromLE, toLE [4]byte
	for i, r := range records {
		off := PageHeaderSize + i*rs
		r.from.writeLE(fromLE[:])
		r.to.writeLE(toLE[:])
		recordWrite(page[off:off+rs], fromLE[:], toLE[:], r.scopeID, 4)
	}
	finalizeChecksum(page)
}

// 3 pages: meta-A (active, txn 2), meta-B (txn 1), one root leaf at pgno 2.
func buildSingleLeaf(records []v4rec) []byte {
	file := make([]byte, 3*PageSize)
	putLeaf(file, 2, records)
	rc := uint64(len(records))
	ma := buildMeta(0, V4, ScopeModeScalar, 2, 1, 3, rc, 2)
	mb := buildMeta(1, V4, ScopeModeScalar, 2, 1, 3, rc, 1)
	ma.encodeInto(file[:PageSize])
	mb.encodeInto(file[PageSize : 2*PageSize])
	return file
}

func buildEmptyFile() []byte {
	file := make([]byte, 2*PageSize)
	ma := buildMeta(0, V4, ScopeModeScalar, 0, 0, 2, 0, 2)
	mb := buildMeta(1, V4, ScopeModeScalar, 0, 0, 2, 0, 1)
	ma.encodeInto(file[:PageSize])
	mb.encodeInto(file[PageSize:])
	return file
}

// 5 pages: metas, a root branch at pgno 2 (one separator), leaves at pgno 3/4.
func buildTwoLevel(sep Ipv4Key, left, right []v4rec) []byte {
	file := make([]byte, 5*PageSize)
	putLeaf(file, 3, left)
	putLeaf(file, 4, right)
	{
		page := file[2*PageSize : 3*PageSize]
		writeHeader(page, PageTypeBranch, 1, 2)
		putU32(page, PageHeaderSize, 3) // child[0]
		sepOff := PageHeaderSize + 4
		sep.writeLE(page[sepOff : sepOff+4])
		putU32(page, sepOff+4, 4) // child[1]
		finalizeChecksum(page)
	}
	rc := uint64(len(left) + len(right))
	ma := buildMeta(0, V4, ScopeModeScalar, 2, 2, 5, rc, 2)
	mb := buildMeta(1, V4, ScopeModeScalar, 2, 2, 5, rc, 1)
	ma.encodeInto(file[:PageSize])
	mb.encodeInto(file[PageSize : 2*PageSize])
	return file
}

func TestSingleLeafLookupAndScan(t *testing.T) {
	recs := []v4rec{{10, 20, 1}, {30, 40, 2}}
	file := buildSingleLeaf(recs)
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 2 || r.KeyWidth() != 4 {
		t.Fatal("metadata")
	}
	cases := []struct {
		ip   Ipv4Key
		want uint32
		ok   bool
	}{
		{15, 1, true}, {10, 1, true}, {20, 1, true}, {25, 0, false},
		{30, 2, true}, {40, 2, true}, {9, 0, false}, {41, 0, false},
	}
	for _, c := range cases {
		s, ok := r.LookupV4(c.ip)
		if ok != c.ok || (ok && s != c.want) {
			t.Errorf("lookup %d = %d,%v want %d,%v", c.ip, s, ok, c.want, c.ok)
		}
	}
	var seen [][3]uint32
	r.ScanV4(func(f, to Ipv4Key, s uint32) { seen = append(seen, [3]uint32{uint32(f), uint32(to), s}) })
	want := [][3]uint32{{10, 20, 1}, {30, 40, 2}}
	if len(seen) != 2 || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("scan = %v", seen)
	}
}

func TestEmptyTree(t *testing.T) {
	file := buildEmptyFile()
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 0 {
		t.Fatal("not empty")
	}
	if _, ok := r.LookupV4(5); ok {
		t.Fatal("empty lookup must miss")
	}
	n := 0
	r.ScanV4(func(Ipv4Key, Ipv4Key, uint32) { n++ })
	if n != 0 {
		t.Fatal("empty scan")
	}
}

func TestTwoLevelLookupCrossesLeaves(t *testing.T) {
	left := []v4rec{{10, 20, 1}, {50, 60, 2}}
	right := []v4rec{{100, 110, 3}, {200, 210, 4}}
	file := buildTwoLevel(100, left, right)
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 4 {
		t.Fatal("count")
	}
	checks := []struct {
		ip   Ipv4Key
		want uint32
		ok   bool
	}{
		{15, 1, true}, {55, 2, true}, {105, 3, true}, {205, 4, true},
		{70, 0, false}, {150, 0, false},
	}
	for _, c := range checks {
		s, ok := r.LookupV4(c.ip)
		if ok != c.ok || (ok && s != c.want) {
			t.Errorf("lookup %d = %d,%v want %d,%v", c.ip, s, ok, c.want, c.ok)
		}
	}
	var seen []uint32
	r.ScanV4(func(f, _ Ipv4Key, _ uint32) { seen = append(seen, uint32(f)) })
	if len(seen) != 4 || seen[0] != 10 || seen[1] != 50 || seen[2] != 100 || seen[3] != 200 {
		t.Fatalf("scan order = %v", seen)
	}
}

// TestTornInactiveMetaRecovers: corrupting the inactive meta (lower txn_id) must not
// affect the reader, which selects the active meta by txn_id.
func TestTornInactiveMetaRecovers(t *testing.T) {
	file := buildSingleLeaf([]v4rec{{10, 20, 1}})
	file[PageSize+200] ^= 0xFF // corrupt inactive meta body (reserved tail)
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := r.LookupV4(15); !ok || s != 1 {
		t.Fatal("did not recover from torn inactive meta")
	}
}
