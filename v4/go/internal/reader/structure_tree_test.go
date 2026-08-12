package reader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// buildStructureTreeDatabase synthesizes a structured v4 database whose
// structure table has a level-2 root (structure_id_limit = 25,601), the
// smallest limit that forces directory levels 2 and 1. The record for
// structure ID 25,600 lives under child 1 of the level-2 root: the child
// index that the pre-fix radix descent (divisor R*512^(L-2)) got wrong by
// a factor of 512.
func buildStructureTreeDatabase(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	const pages = 9
	file := make([][]byte, pages)
	for i := range file {
		file[i] = make([]byte, format.PageSize)
	}
	copy(file[0], raw[:format.PageSize])
	copy(file[1], raw[format.PageSize:2*format.PageSize])

	// Meta: structured value kind, one structure entry, limit 25601.
	file[0][12], file[1][12] = 3, 3 // value kind: structured
	file[0][13], file[1][13] = 1, 1 // structure kind: network enrichment v1
	for _, offs := range []struct {
		off int
		val uint64
	}{
		{72, pages},  // page count
		{80, 5},      // range record count
		{104, 0},     // membership entry count
		{112, 1},     // membership id limit (empty dictionary)
		{120, 0},     // metadata uncompressed (removed)
		{128, 0},     // metadata compressed (removed)
		{200, 1},     // structure entry count
		{208, 25601}, // structure id limit
	} {
		format.PutU64(file[0][offs.off:offs.off+8], offs.val)
		format.PutU64(file[1][offs.off:offs.off+8], offs.val)
	}
	format.PutU32(file[0][144:148], 2) // range root
	format.PutU32(file[1][144:148], 2)
	format.PutU32(file[0][172:176], 0) // metadata root (removed)
	format.PutU32(file[1][172:176], 0)
	format.PutU32(file[0][216:220], 3) // structure id root (level-2 directory)
	format.PutU32(file[1][216:220], 3)
	format.PutU32(file[0][220:224], 7) // structure hash root (unread by the reader)
	format.PutU32(file[1][220:224], 7)
	format.PutU32(file[0][224:228], 8) // structure used root (unread by the reader)
	format.PutU32(file[1][224:228], 8)
	format.PutU32(file[0][252:256], format.MetaCRC32C(file[0]))
	format.PutU32(file[1][252:256], format.MetaCRC32C(file[1]))

	header := func(p []byte, typ format.PageType, aux uint32, level uint16, itemCount, lower, upper uint16) {
		copy(p[0:4], format.PageMagic[:])
		p[4] = byte(typ)
		format.PutU16(p[6:8], 32)
		format.PutU64(p[8:16], 2) // born txn
		format.PutU16(p[16:18], itemCount)
		format.PutU16(p[18:20], level)
		format.PutU16(p[20:22], lower)
		format.PutU16(p[22:24], upper)
		format.PutU32(p[24:28], aux)
	}

	// Page 2: range leaf, five records mapping 10.9.k.0/24 to structure IDs.
	header(file[2], format.PageTypeRangeLeaf, 4, 0, 5, 42, 4036)
	for i, v := range []uint32{25600, 25601, 0, 5, 1} {
		format.PutU16(file[2][32+2*i:34+2*i], uint16(4036+12*i))
		format.PutU32(file[2][4036+12*i:4040+12*i], 0x0a090000+uint32(i)<<8)
		format.PutU32(file[2][4040+12*i:4044+12*i], 0x0a0900ff+uint32(i)<<8)
		format.PutU32(file[2][4044+12*i:4048+12*i], v)
	}

	// Page 3: level-2 directory root: children 0 -> page 4 (ids 0..25599),
	// child 1 -> page 5 (ids 25600..51199).
	header(file[3], format.PageTypeStructureIDDirectory, 1, 2, 1, format.StructureBranchEnd, format.PageSize)
	format.PutU32(file[3][32:36], 4)
	format.PutU32(file[3][36:40], 5)

	// Page 4: level-1 directory, every child empty (ids 0..25599 absent).
	header(file[4], format.PageTypeStructureIDDirectory, 1, 1, 1, format.StructureBranchEnd, format.PageSize)

	// Page 5: level-1 directory; child 0 -> page 6 covers ids 25600..25649.
	header(file[5], format.PageTypeStructureIDDirectory, 1, 1, 1, format.StructureBranchEnd, format.PageSize)
	format.PutU32(file[5][32:36], 6)

	// Page 6: level-0 record leaf; slot 0 holds the record for id 25600.
	header(file[6], format.PageTypeStructureIDRecord, 1, 0, 1, format.StructureLeafEnd, format.PageSize)
	slot := uint64(25600) % format.StructureRecordSlots
	rec := file[6][32+slot*format.StructureRecordSize : 32+(slot+1)*format.StructureRecordSize]
	format.PutU16(rec[0:2], format.StructureRecordSize)
	format.PutU32(rec[4:8], 25600)
	format.PutU64(rec[8:16], 1)
	// Payload: ASN 1234, no location, no membership.
	format.PutU32(rec[48:52], 1234)

	// Pages 7/8: hash leaf and used bitmap stubs (never read by the reader).
	header(file[7], format.PageTypeStructureHashLeaf, 1, 0, 1, 4032, format.PageSize)
	header(file[8], format.PageTypeBitmapLeaf, 0, 0, 1, 4032, format.PageSize)

	dir := t.TempDir()
	path := filepath.Join(dir, "structure-tree.iprdb")
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

// TestMultiLevelStructureTree pins the radix descent at directory level 2:
// the child index must divide by R*512^(L-1) (25600 at level 2), so
// structure ID 25,600 resolves through child 1 of the level-2 root.
func TestMultiLevelStructureTree(t *testing.T) {
	path := buildStructureTreeDatabase(t)
	db, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cases := []struct {
		addr  uint32
		found bool
	}{
		{0x0a090001, true},  // 10.9.0.1 -> id 25600: the level-2 child-1 path
		{0x0a0900ff, true},  // last address of the record
		{0x0a090101, false}, // 10.9.1.1 -> id 25601: empty slot cell
		{0x0a090201, false}, // 10.9.2.1 -> id 0: absent by contract
		{0x0a090301, false}, // 10.9.3.1 -> id 5: empty level-1 directory child
		{0x0a090401, false}, // 10.9.4.1 -> id 1: empty level-1 directory child
	}
	for _, tc := range cases {
		view, found, err := db.LookupNetworkEnrichmentV14(tc.addr)
		if err != nil {
			t.Fatalf("addr %x: %v", tc.addr, err)
		}
		if found != tc.found {
			t.Errorf("addr %x: found %v want %v", tc.addr, found, tc.found)
			continue
		}
		if found {
			value, err := view.Value()
			if err != nil {
				t.Fatal(err)
			}
			if value.ASN != 1234 {
				t.Errorf("addr %x: ASN %d want 1234", tc.addr, value.ASN)
			}
		}
	}

	// An address without a range record is absent at the range level.
	if _, found, err := db.LookupNetworkEnrichmentV14(0x0a0a0001); err != nil || found {
		t.Fatalf("uncovered address: %v %v", found, err)
	}
}
