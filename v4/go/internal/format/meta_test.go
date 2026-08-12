package format

import (
	"os"
	"path/filepath"
	"testing"
)

// Bootstrap verification tests. They mutate copies of the committed
// direct-ipv4 fixture and check the exact typed rejection or the exact
// selection; both meta-page CRCs are repaired in tests that require an
// identity-readable mutation.

func fixtureBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func fixMetaCRC(t *testing.T, page []byte) {
	t.Helper()
	if len(page) != PageSize {
		t.Fatalf("meta page length %d", len(page))
	}
	PutU32(page[252:256], MetaCRC32C(page))
}

func writeFixture(t *testing.T, mutate func(pages [][]byte)) string {
	t.Helper()
	raw := fixtureBytes(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "main.iprdb")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		pages := make([][]byte, len(raw)/PageSize)
		for i := range pages {
			pages[i] = make([]byte, PageSize)
			if _, err := file.ReadAt(pages[i], int64(i*PageSize)); err != nil {
				t.Fatal(err)
			}
		}
		mutate(pages)
		for i, page := range pages {
			if _, err := file.WriteAt(page, int64(i*PageSize)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return path
}

func TestMetaFixtureIdentity(t *testing.T) {
	raw := fixtureBytes(t)
	page := raw[:PageSize]
	m, ok := ParseIdentity(page)
	if !ok {
		t.Fatal("fixture meta not identity-readable")
	}
	if m.AddressFamily != 4 || m.ValueKind != ValueKindDirect || m.StructureKind != 0 {
		t.Fatalf("identity fields %d %d %d", m.AddressFamily, m.ValueKind, m.StructureKind)
	}
	if m.PageCount != 4 || m.RangeRecordCount != 4 || m.RangeRoot != 2 || m.MetadataRoot != 3 {
		t.Fatalf("meta scalars %d %d %d %d", m.PageCount, m.RangeRecordCount, m.RangeRoot, m.MetadataRoot)
	}
	if m.MetadataUncompressed != 48 {
		t.Fatalf("metadata length %d", m.MetadataUncompressed)
	}
	if err := m.ValidateKindInvariants(); err != nil {
		t.Fatal(err)
	}
	if m.TxnID != 2 {
		t.Fatalf("txn %d", m.TxnID)
	}
}

func TestMetaRejectsCorruptIdentity(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(page []byte)
	}{
		{"magic", func(p []byte) { p[0] = 'X' }},
		{"meta-size", func(p []byte) { PutU16(p[8:10], 128) }},
		{"page-shift", func(p []byte) { p[10] = 9 }},
		{"reserved", func(p []byte) { p[14] = 1 }},
		{"family", func(p []byte) { p[11] = 5 }},
		{"value-kind", func(p []byte) { p[12] = 9 }},
		{"database-id-zero", func(p []byte) {
			for i := 32; i < 48; i++ {
				p[i] = 0
			}
		}},
		{"tag-no-nul", func(p []byte) { p[16+15] = 'x' }},
		{"crc", func(p []byte) { p[252] ^= 0xff }},
		{"reserved-tail", func(p []byte) { p[240] = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := make([]byte, PageSize)
			copy(page, fixtureBytes(t)[:PageSize])
			tc.mutate(page)
			if _, ok := ParseIdentity(page); ok {
				t.Fatal("expected identity rejection")
			}
		})
	}
}

func TestMetaKindInvariants(t *testing.T) {
	valid := func() Meta {
		m, ok := ParseIdentity(fixtureBytes(t)[:PageSize])
		if !ok {
			t.Fatal("fixture not identity readable")
		}
		return m
	}
	// Direct with membership state.
	m := valid()
	m.MembershipIDLimit = 7
	if err := m.ValidateKindInvariants(); err == nil {
		t.Fatal("direct with membership limit accepted")
	}
	// Direct with any nonzero structure kind is the KindInvariant class:
	// a plain format error, never the typed unsupported classification.
	for _, kind := range []byte{1, 2} {
		m = valid()
		m.StructureKind = kind
		if err := m.ValidateKindInvariants(); err == nil {
			t.Fatalf("direct kind %d accepted", kind)
		} else if ferr, ok := err.(*errMeta); !ok || ferr.code != ErrFormat {
			t.Fatalf("direct kind %d: plain format error expected, got %v", kind, err)
		}
	}
	// Structured with an unknown nonzero structure kind and valid counts:
	// validation PASSES (bootstrap.rs validate_structured runs for any
	// nonzero kind); the reader reports UnsupportedStructure only after
	// pair selection (finish_open). Base the meta on the structured
	// fixture so all structured/membership relations are consistent.
	rawStructured, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "structured-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := ParseIdentity(rawStructured[:PageSize])
	if !ok {
		t.Fatal("structured fixture not identity readable")
	}
	m.StructureKind = 2
	if err := m.ValidateKindInvariants(); err != nil {
		t.Fatalf("structured unknown kind with valid counts must validate, got %v", err)
	}
	// Unknown kind with broken structure counts/roots is a plain format
	// error, never the typed unsupported error: other validation failures
	// win over the unknown-kind classification, exactly like Rust.
	m = valid()
	m.ValueKind = ValueKindStructured
	m.StructureKind = 2
	m.StructureEntryCount = 1
	m.StructureIDLimit = 1
	m.StructureIDRoot = 2
	m.StructureHashRoot = 3
	m.StructureUsedRoot = 4
	if m.PageCount < 6 {
		m.PageCount = 6
	}
	got := m.ValidateKindInvariants()
	if got == nil {
		t.Fatal("structured unknown kind with broken counts accepted")
	}
	if ferr, ok := got.(*errMeta); !ok || ferr.code != ErrFormat {
		t.Fatalf("structured unknown kind with broken counts: plain format error expected, got %v", got)
	}
	// Root out of range.
	m = valid()
	m.RangeRoot = 99 // above page_count 4
	if err := m.ValidateKindInvariants(); err == nil {
		t.Fatal("root above page count accepted")
	}
	// Range records without root.
	m = valid()
	m.RangeRecordCount = 3
	m.RangeRoot = 0
	if err := m.ValidateKindInvariants(); err == nil {
		t.Fatal("records without root accepted")
	}
	// Metadata lengths without root.
	m = valid()
	m.MetadataRoot = 0
	if err := m.ValidateKindInvariants(); err == nil {
		t.Fatal("metadata lengths without root accepted")
	}
	// Txn zero.
	m = valid()
	m.TxnID = 0
	if err := m.ValidateKindInvariants(); err == nil {
		t.Fatal("zero txn accepted")
	}
	// Allocator reserve aliasing a root.
	m = valid()
	m.AllocatorReserve[0] = m.RangeRoot
	if err := m.ValidateKindInvariants(); err == nil {
		t.Fatal("reserve aliasing root accepted")
	}
}

func TestMetaMembershipRelations(t *testing.T) {
	// The membership-ipv4 fixture exercises the dictionary relations.
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "membership-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := ParseIdentity(raw[:PageSize])
	if !ok {
		t.Fatal("membership fixture not identity readable")
	}
	if err := m.ValidateKindInvariants(); err != nil {
		t.Fatal(err)
	}
	if m.ActiveFeedCount != 70 || m.FeedIndexLimit != 70 {
		t.Fatalf("feeds %d limit %d", m.ActiveFeedCount, m.FeedIndexLimit)
	}
	// Nonzero entries require nonzero roots.
	m.MembershipIDRoot = 0
	if err := m.ValidateKindInvariants(); err == nil {
		t.Fatal("entries without roots accepted")
	}
	// Zero entries with nonzero ranges rejected (range records require
	// dictionary entries in membership files).
	m = membershipMeta(t)
	m.MembershipEntryCount = 0
	m.MembershipIDRoot = 0
	m.MembershipHashRoot = 0
	m.MembershipUsedRoot = 0
	m.MembershipIDLimit = 1
	if err := m.ValidateKindInvariants(); err == nil {
		t.Fatal("zero entries with ranges accepted")
	}
}

func membershipMeta(t *testing.T) Meta {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "membership-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := ParseIdentity(raw[:PageSize])
	if !ok {
		t.Fatal("membership fixture not identity readable")
	}
	return m
}

func TestMetaStructuredRelations(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "structured-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := ParseIdentity(raw[:PageSize])
	if !ok {
		t.Fatal("structured fixture not identity readable")
	}
	if err := m.ValidateKindInvariants(); err != nil {
		t.Fatal(err)
	}
	if m.StructureKind != StructureKindNetworkEnrichmentV1 || m.StructureEntryCount != 2 {
		t.Fatalf("structure fields %d %d", m.StructureKind, m.StructureEntryCount)
	}
}

func TestBlobAndChunkCodecs(t *testing.T) {
	page := make([]byte, PageSize)
	PutU64(page[32:40], 1234)
	PutU16(page[40:42], 100)
	for i := 42; i < 48; i++ {
		page[i] = 0
	}
	leaf, err := DecodeBlobLeaf(page)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.LogicalOffset != 1234 || leaf.DataLen != 100 || len(leaf.Data) != 100 {
		t.Fatalf("blob leaf %d %d %d", leaf.LogicalOffset, leaf.DataLen, len(leaf.Data))
	}
	// Reserved bytes nonzero rejected.
	page[44] = 1
	if _, err := DecodeBlobLeaf(page); err == nil {
		t.Fatal("blob reserved accepted")
	}

	chunk := make([]byte, PageSize)
	PutU32(chunk[32:36], 9)
	PutU16(chunk[36:38], 50)
	PutU64(chunk[40:48], 4096)
	c, err := DecodeMetadataChunk(chunk)
	if err != nil {
		t.Fatal(err)
	}
	if c.Next != 9 || c.ChunkLen != 50 || c.LogicalOffset != 4096 || len(c.Data) != 50 {
		t.Fatalf("chunk %d %d %d %d", c.Next, c.ChunkLen, c.LogicalOffset, len(c.Data))
	}
	PutU16(chunk[38:40], 1)
	if _, err := DecodeMetadataChunk(chunk); err == nil {
		t.Fatal("chunk reserved accepted")
	}
}
