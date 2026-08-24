package reader

// Reader codec parity for zero-refcount structure records (Rust
// codec::decode_record reads the refcount raw; a zero refcount is a
// validator finding class, never a decode failure). The reader must
// resolve the same files Rust resolves: the record decodes, the
// payload view returns, and only explicit validation flags the
// refcount.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// buildRefcountZeroStructuredDatabase synthesizes one structured IPv4
// database over the frozen direct fixture's meta (the proven builder
// shape of structured_cursor_test.go): one range leaf with two
// enrichment records, one level-0 structure dictionary page carrying
// two records (slot 1 has a zero refcount), and stub hash and used
// pages the reader never touches.
func buildRefcountZeroStructuredDatabase(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	const pages = 6
	file := make([][]byte, pages)
	for i := range file {
		file[i] = make([]byte, format.PageSize)
	}
	copy(file[0], raw[:format.PageSize])
	copy(file[1], raw[format.PageSize:2*format.PageSize])
	for i := 0; i < 2; i++ {
		m := file[i]
		m[12] = format.ValueKindStructured
		m[13] = format.StructureKindNetworkEnrichmentV1
		for _, offs := range []struct {
			off int
			val uint64
		}{
			{72, pages}, // page count
			{80, 2},     // range record count
			{104, 0},    // membership entry count (empty dictionary)
			{112, 1},    // membership id limit
			{120, 0},    // metadata uncompressed (removed)
			{128, 0},    // metadata compressed (removed)
			{200, 2},    // structure entry count
			{208, 3},    // structure id limit
		} {
			format.PutU64(m[offs.off:offs.off+8], offs.val)
		}
		format.PutU32(m[144:148], 2) // range root (leaf)
		format.PutU32(m[172:176], 0) // metadata root (removed)
		format.PutU32(m[216:220], 3) // structure id root (level-0 record page)
		format.PutU32(m[220:224], 4) // structure hash root (unread)
		format.PutU32(m[224:228], 5) // structure used root (unread)
		format.PutU32(m[252:256], format.MetaCRC32C(m))
	}
	header := func(p []byte, typ format.PageType, aux uint32, level uint16, itemCount, lower, upper uint16) {
		copy(p[0:4], format.PageMagic[:])
		p[4] = byte(typ)
		format.PutU16(p[6:8], 32)
		format.PutU64(p[8:16], 2)
		format.PutU16(p[16:18], itemCount)
		format.PutU16(p[18:20], level)
		format.PutU16(p[20:22], lower)
		format.PutU16(p[22:24], upper)
		format.PutU32(p[24:28], aux)
	}
	// Page 2: IPv4 range leaf, two records naming structure ids 1 and 2.
	header(file[2], format.PageTypeRangeLeaf, 4, 0, 2, 36, 4024)
	for i, v := range []struct{ from, to, id uint32 }{
		{1, 2, 1},
		{3, 4, 2},
	} {
		off := 4024 + i*format.RangeRecordV4Size
		format.PutU16(file[2][32+2*i:34+2*i], uint16(off))
		format.PutU32(file[2][off:off+4], v.from)
		format.PutU32(file[2][off+4:off+8], v.to)
		format.PutU32(file[2][off+8:off+12], v.id)
	}
	// Page 3: level-0 structure record page, slots 1 and 2.
	header(file[3], format.PageTypeStructureIDRecord, uint32(format.StructureKindNetworkEnrichmentV1), 0, 2, format.StructureLeafEnd, format.PageSize)
	for i, id := range []uint32{1, 2} {
		slot := uint64(id) % format.StructureRecordSlots
		rec := file[3][32+slot*format.StructureRecordSize : 32+(slot+1)*format.StructureRecordSize]
		format.PutU16(rec[0:2], format.StructureRecordSize)
		format.PutU32(rec[4:8], id)
		if i == 0 {
			format.PutU64(rec[8:16], 0) // zero refcount: decodes, the validator flags
		} else {
			format.PutU64(rec[8:16], 2)
		}
		format.PutU32(rec[48:52], 64500+uint32(i))
	}
	// Pages 4/5: hash leaf and used bitmap stubs (never read by the reader).
	header(file[4], format.PageTypeStructureHashLeaf, uint32(format.StructureKindNetworkEnrichmentV1), 0, 1, 4032, format.PageSize)
	header(file[5], format.PageTypeBitmapLeaf, 0, 0, 1, 4032, format.PageSize)

	path := filepath.Join(t.TempDir(), "structured-refcount-zero.iprdb")
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range file {
		if _, err := out.Write(page); err != nil {
			t.Fatal(err)
		}
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLookupStructureIDRefcountZero(t *testing.T) {
	path := buildRefcountZeroStructuredDatabase(t)
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// The zero-refcount record resolves exactly like the Rust
	// read_record path: found, payload view, no decode error.
	view, found, err := r.LookupStructureID(1)
	if err != nil {
		t.Fatalf("LookupStructureID(1) with zero refcount: %v", err)
	}
	if !found {
		t.Fatal("zero-refcount record reported absent")
	}
	value, err := view.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if got := value.ASN; got != 64500 {
		t.Fatalf("ASN = %d, want 64500", got)
	}
	// The healthy sibling still resolves too.
	view, found, err = r.LookupStructureID(2)
	if err != nil || !found {
		t.Fatalf("LookupStructureID(2): found=%v err=%v", found, err)
	}
	value, err = view.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if got := value.ASN; got != 64501 {
		t.Fatalf("ASN = %d, want 64501", got)
	}
}
