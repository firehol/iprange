package reader

// NetworkEnrichmentV1 range cursor surface (slice A of chunk 3b-3): the
// ordered walk, the require_kind guards, the per-range structure decode,
// and the absent-structure corruption, mirroring structured_value/cursor.rs
// plus view.rs by_id. The IPv4 walk runs over the frozen conformance
// fixture; the IPv6 walk runs over a hand-built database because no IPv6
// structured fixture exists in the corpus.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// buildStructuredV6CursorDatabase synthesizes a structured IPv6 database
// whose range leaf carries two enrichment records and whose structure
// dictionary is a single level-0 record root (id limit 3), the simplest
// layout the cursor can walk.
func buildStructuredV6CursorDatabase(t *testing.T) string {
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

	// Meta: IPv6 structured network enrichment, two ranges / two entries.
	file[0][11], file[1][11] = 6, 6 // address family: ipv6
	file[0][12], file[1][12] = 3, 3 // value kind: structured
	file[0][13], file[1][13] = 1, 1 // structure kind: network enrichment v1
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
		format.PutU64(file[0][offs.off:offs.off+8], offs.val)
		format.PutU64(file[1][offs.off:offs.off+8], offs.val)
	}
	format.PutU32(file[0][144:148], 2) // range root (leaf)
	format.PutU32(file[1][144:148], 2)
	format.PutU32(file[0][172:176], 0) // metadata root (removed)
	format.PutU32(file[1][172:176], 0)
	format.PutU32(file[0][216:220], 3) // structure id root (level-0 record page)
	format.PutU32(file[1][216:220], 3)
	format.PutU32(file[0][220:224], 4) // structure hash root (unread by the reader)
	format.PutU32(file[1][220:224], 4)
	format.PutU32(file[0][224:228], 5) // structure used root (unread by the reader)
	format.PutU32(file[1][224:228], 5)
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

	// Page 2: IPv6 range leaf, two records: 2001:db8::1-::2 (id 1) and
	// 2001:db8::3-::4 (id 2). Records are 36 bytes: 16-byte from, 16-byte
	// to, 4-byte value.
	header(file[2], format.PageTypeRangeLeaf, 6, 0, 2, 36, 4024)
	for i, v := range []struct{ fhi, flo, thi, tlo, id uint64 }{
		{0x20010db800000000, 1, 0x20010db800000000, 2, 1},
		{0x20010db800000000, 3, 0x20010db800000000, 4, 2},
	} {
		off := 4024 + i*format.RangeRecordV6Size
		format.PutU16(file[2][32+2*i:34+2*i], uint16(off))
		// 128-bit wire order is the low limb first (format.PutU128).
		format.PutU128(file[2][off:off+16], v.fhi, v.flo)
		format.PutU128(file[2][off+16:off+32], v.thi, v.tlo)
		format.PutU32(file[2][off+32:off+36], uint32(v.id))
	}

	// Page 3: level-0 structure record page, slots 1 and 2 (id 0 is
	// reserved). Each record: len, id, refcount, sha, 32-byte payload.
	header(file[3], format.PageTypeStructureIDRecord, 1, 0, 2, format.StructureLeafEnd, format.PageSize)
	for i, id := range []uint32{1, 2} {
		slot := uint64(id) % format.StructureRecordSlots
		rec := file[3][32+slot*format.StructureRecordSize : 32+(slot+1)*format.StructureRecordSize]
		format.PutU16(rec[0:2], format.StructureRecordSize)
		format.PutU32(rec[4:8], id)
		format.PutU64(rec[8:16], 2)
		// Payload: ASN 64500+i, no location, no membership.
		format.PutU32(rec[48:52], 64500+uint32(i))
		_ = i
	}

	// Pages 4/5: hash leaf and used bitmap stubs (never read by the reader).
	header(file[4], format.PageTypeStructureHashLeaf, 1, 0, 1, 4032, format.PageSize)
	header(file[5], format.PageTypeBitmapLeaf, 0, 0, 1, 4032, format.PageSize)

	dir := t.TempDir()
	path := filepath.Join(dir, "structured-ipv6-cursor.iprdb")
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

func TestNetworkEnrichmentV1Cursor4(t *testing.T) {
	r := openFixture(t, "structured-ipv4.iprdb")
	c, err := r.NewNetworkEnrichmentV1Cursor4(RangeForward)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		from, to uint32
		asn      uint32
	}{
		{0x0a010000, 0x0a01003f, 64512}, // 10.1.0.0-63, botnet
		{0x0a010040, 0x0a010063, 64513}, // 10.1.0.64-99, scanner
		{0x0a01006e, 0x0a01007f, 64513}, // 10.1.0.110-127, scanner
		{0x0a010080, 0x0a0100ff, 64512}, // 10.1.0.128-255, botnet
	}
	for i, w := range want {
		got, ok, err := c.Next()
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("record %d: cursor finished early", i)
		}
		if got.From != w.from || got.To != w.to {
			t.Fatalf("record %d: (%x,%x) want (%x,%x)", i, got.From, got.To, w.from, w.to)
		}
		value, err := got.Value.Value()
		if err != nil {
			t.Fatal(err)
		}
		if value.ASN != w.asn {
			t.Fatalf("record %d: ASN %d want %d", i, value.ASN, w.asn)
		}
		if value.MembershipID == 0 {
			t.Fatalf("record %d: expected a threat membership link", i)
		}
		if i > 0 && got.From <= want[i-1].from {
			t.Fatalf("record %d not strictly ascending", i)
		}
	}
	if got, ok, err := c.Next(); err != nil || ok {
		t.Fatalf("cursor past the last record: %v %v", got, ok)
	}
}

func TestNetworkEnrichmentV1Cursor4NoThreat(t *testing.T) {
	r := openFixture(t, "structured-ipv4-nothreat.iprdb")
	c, err := r.NewNetworkEnrichmentV1Cursor4(RangeForward)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		from, to uint32
		asn      uint32
	}{
		{0x0a020000, 0x0a02007f, 64514},
		{0x0a020080, 0x0a0200ff, 64515},
	}
	for i, w := range want {
		got, ok, err := c.Next()
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("record %d: cursor finished early", i)
		}
		value, err := got.Value.Value()
		if err != nil {
			t.Fatal(err)
		}
		if value.ASN != w.asn || got.From != w.from || got.To != w.to {
			t.Fatalf("record %d: (%x,%x,asn %d) want (%x,%x,asn %d)", i, got.From, got.To, value.ASN, w.from, w.to, w.asn)
		}
		if value.MembershipID != 0 {
			t.Fatalf("record %d: unexpected threat membership %d", i, value.MembershipID)
		}
		if _, err := got.Value.ThreatMembership(); err != nil {
			t.Fatalf("record %d: threat membership on an unlinked value: %v", i, err)
		}
	}
}

func TestNetworkEnrichmentV1Cursor6(t *testing.T) {
	path := buildStructuredV6CursorDatabase(t)
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	c, err := r.NewNetworkEnrichmentV1Cursor6(RangeForward)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		fhi, flo, thi, tlo uint64
		asn                uint32
	}{
		{0x20010db800000000, 1, 0x20010db800000000, 2, 64500},
		{0x20010db800000000, 3, 0x20010db800000000, 4, 64501},
	}
	for i, w := range want {
		got, ok, err := c.Next()
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("record %d: cursor finished early", i)
		}
		value, err := got.Value.Value()
		if err != nil {
			t.Fatal(err)
		}
		if got.FromHi != w.fhi || got.FromLo != w.flo || got.ToHi != w.thi || got.ToLo != w.tlo || value.ASN != w.asn {
			t.Fatalf("record %d: (%x:%x-%x:%x, asn %d) want (%x:%x-%x:%x, asn %d)", i, got.FromHi, got.FromLo, got.ToHi, got.ToLo, value.ASN, w.fhi, w.flo, w.thi, w.tlo, w.asn)
		}
	}
	if got, ok, err := c.Next(); err != nil || ok {
		t.Fatalf("cursor past the last record: %v %v", got, ok)
	}
}

func TestNetworkEnrichmentV1CursorGuards(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		new     func(*ImmutableReader) error
		code    format.ErrorCode
	}{
		{"ipv4 cursor on direct db", "direct-ipv4.iprdb", func(r *ImmutableReader) error { _, err := r.NewNetworkEnrichmentV1Cursor4(RangeForward); return err }, format.CodeWrongStructureKind},
		{"ipv4 cursor on membership db", "membership-ipv4.iprdb", func(r *ImmutableReader) error { _, err := r.NewNetworkEnrichmentV1Cursor4(RangeForward); return err }, format.CodeWrongStructureKind},
		{"ipv6 cursor on ipv4 db", "structured-ipv4.iprdb", func(r *ImmutableReader) error { _, err := r.NewNetworkEnrichmentV1Cursor6(RangeForward); return err }, format.CodeWrongAddressFamily},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.new(openFixture(t, tc.fixture)); mustCode(err) != tc.code {
				t.Fatalf("code %v want %v (err %v)", mustCode(err), tc.code, err)
			}
		})
	}
	// IPv6-family guard on the synthetic IPv6 database.
	t.Run("ipv4 cursor on ipv6 db", func(t *testing.T) {
		r, err := OpenImmutable(buildStructuredV6CursorDatabase(t))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if _, err := r.NewNetworkEnrichmentV1Cursor4(RangeForward); mustCode(err) != format.CodeWrongAddressFamily {
			t.Fatalf("code %v want %v (err %v)", mustCode(err), format.CodeWrongAddressFamily, err)
		}
	})
}

func TestNetworkEnrichmentV1CursorDangling(t *testing.T) {
	// The multi-level structure fixture maps 10.9.0.0/24 to structure id
	// 25600 and 10.9.1.0/24 to id 25601, whose slot is empty: the first
	// range decodes, the second is the Rust "range names an absent
	// structure ID" corruption.
	r, err := OpenImmutable(buildStructureTreeDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	c, err := r.NewNetworkEnrichmentV1Cursor4(RangeForward)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Next()
	if err != nil || !ok {
		t.Fatalf("first record: ok %v err %v", ok, err)
	}
	if got.From != 0x0a090000 || got.To != 0x0a0900ff {
		t.Fatalf("first record (%x,%x)", got.From, got.To)
	}
	if _, _, err := c.Next(); mustCode(err) != format.CodeFormatInvalid {
		t.Fatalf("dangling id: code %v want %v (err %v)", mustCode(err), format.CodeFormatInvalid, err)
	}
}
