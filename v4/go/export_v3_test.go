package iprangedb

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	v3 "github.com/firehol/iprange/v3/go"
)

func exportMeta() V3Meta {
	return V3Meta{
		FeedMeta: v3.FeedMeta{
			Name:     "export-test",
			Category: "attacks",
		},
		LicenseFlags:       0,
		GenerationUnixtime: 1_700_000_000,
	}
}

type rangeV4 struct {
	from, to uint32
	scope    []byte
}

type rangeV6 struct {
	from, to uint64
	scope    []byte
}

// v4ImageV4 builds a committed v4 IPv4 image from the given ranges.
func v4ImageV4(t *testing.T, scopeWidth uint8, ranges []rangeV4) []byte {
	t.Helper()
	w := CreateV4(scopeWidth, 0)
	for _, r := range ranges {
		if err := w.Set(Ipv4Key(r.from), Ipv4Key(r.to), r.scope); err != nil {
			t.Fatalf("v4 Set: %v", err)
		}
	}
	if err := w.Commit(0); err != nil {
		t.Fatalf("v4 Commit: %v", err)
	}
	return w.Image()
}

func v4ImageV6(t *testing.T, scopeWidth uint8, ranges []rangeV6) []byte {
	t.Helper()
	w := CreateV6(scopeWidth, 0)
	for _, r := range ranges {
		if err := w.Set(Ipv6Key{Lo: r.from}, Ipv6Key{Lo: r.to}, r.scope); err != nil {
			t.Fatalf("v6 Set: %v", err)
		}
	}
	if err := w.Commit(0); err != nil {
		t.Fatalf("v6 Commit: %v", err)
	}
	return w.Image()
}

// --- ROUND-TRIP: v4 -> ExportV3 -> v3 Reader, asserting parity ---

func TestExportRoundtripV4ScopeWidth4(t *testing.T) {
	ranges := []rangeV4{
		{10, 20, []byte{1, 0, 0, 0}},
		{30, 40, []byte{2, 0, 0, 0}},
		{100, 200, []byte{3, 0, 0, 0}},
	}
	img := v4ImageV4(t, 4, ranges)
	const typeID = uint32(7)
	out, err := ExportV3(img, typeID, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3: %v", err)
	}

	r, err := v3.Open(out)
	if err != nil {
		t.Fatalf("v3 Open: %v", err)
	}
	if r.RecordCount() != 3 {
		t.Fatalf("record_count = %d, want 3", r.RecordCount())
	}
	fm, err := r.FeedMeta()
	if err != nil {
		t.Fatalf("FeedMeta: %v", err)
	}
	if fm.Name != "export-test" || fm.Category != "attacks" {
		t.Fatalf("feed-meta not passed through: %+v", fm)
	}

	for _, rg := range ranges {
		for _, ip := range []uint32{rg.from, (rg.from + rg.to) / 2, rg.to} {
			hit, found, err := r.LookupV4(v3.Ipv4Key(ip))
			if err != nil || !found {
				t.Fatalf("lookup %d: found=%v err=%v", ip, found, err)
			}
			val, ok := r.Value(hit.ValueID)
			if !ok {
				t.Fatalf("lookup %d: no value", ip)
			}
			if val.TypeID != typeID {
				t.Fatalf("type_id = %d, want %d", val.TypeID, typeID)
			}
			if !bytes.Equal(val.Bytes, rg.scope) {
				t.Fatalf("value bytes %v != v4 scope %v", val.Bytes, rg.scope)
			}
		}
	}
	for _, ip := range []uint32{9, 25, 41, 99, 201} {
		if _, found, _ := r.LookupV4(v3.Ipv4Key(ip)); found {
			t.Fatalf("gap %d unexpectedly present", ip)
		}
	}
}

func TestExportRoundtripV4ScopeWidth0Presence(t *testing.T) {
	ranges := []rangeV4{{10, 20, nil}, {100, 110, nil}}
	img := v4ImageV4(t, 0, ranges)
	out, err := ExportV3(img, 1 /* ignored */, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3: %v", err)
	}
	r, err := v3.Open(out)
	if err != nil {
		t.Fatalf("v3 Open: %v", err)
	}
	if r.RecordCount() != 2 {
		t.Fatalf("record_count = %d, want 2", r.RecordCount())
	}
	for _, rg := range ranges {
		for _, ip := range []uint32{rg.from, rg.to} {
			hit, found, err := r.LookupV4(v3.Ipv4Key(ip))
			if err != nil || !found {
				t.Fatalf("lookup %d: found=%v err=%v", ip, found, err)
			}
			if hit.ValueID != 0xFFFF_FFFF {
				t.Fatalf("ip %d: value_id = %#x, want sentinel", ip, hit.ValueID)
			}
			if _, ok := r.Value(hit.ValueID); ok {
				t.Fatalf("sentinel resolved to a value")
			}
		}
	}
	if _, found, _ := r.LookupV4(v3.Ipv4Key(50)); found {
		t.Fatalf("gap present")
	}
}

func TestExportRoundtripV6ScopeWidth4(t *testing.T) {
	ranges := []rangeV6{
		{10, 20, []byte{9, 9, 9, 9}},
		{1000, 2000, []byte{8, 8, 8, 8}},
	}
	img := v4ImageV6(t, 4, ranges)
	const typeID = uint32(2)
	out, err := ExportV3(img, typeID, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3: %v", err)
	}
	r, err := v3.Open(out)
	if err != nil {
		t.Fatalf("v3 Open: %v", err)
	}
	if r.RecordCount() != 2 {
		t.Fatalf("record_count = %d, want 2", r.RecordCount())
	}
	for _, rg := range ranges {
		for _, lo := range []uint64{rg.from, rg.to} {
			hit, found, err := r.LookupV6(v3.Ipv6Key{Lo: lo})
			if err != nil || !found {
				t.Fatalf("lookup %d: found=%v err=%v", lo, found, err)
			}
			val, ok := r.Value(hit.ValueID)
			if !ok || val.TypeID != typeID || !bytes.Equal(val.Bytes, rg.scope) {
				t.Fatalf("lookup %d: val=%+v ok=%v", lo, val, ok)
			}
		}
	}
	if _, found, _ := r.LookupV6(v3.Ipv6Key{Lo: 500}); found {
		t.Fatalf("gap present")
	}
}

func TestExportRoundtripV6ScopeWidth0Presence(t *testing.T) {
	img := v4ImageV6(t, 0, []rangeV6{{5, 9, nil}, {50, 60, nil}})
	out, err := ExportV3(img, 1, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3: %v", err)
	}
	r, err := v3.Open(out)
	if err != nil {
		t.Fatalf("v3 Open: %v", err)
	}
	if r.RecordCount() != 2 {
		t.Fatalf("record_count = %d, want 2", r.RecordCount())
	}
	hit, found, err := r.LookupV6(v3.Ipv6Key{Lo: 7})
	if err != nil || !found || hit.ValueID != 0xFFFF_FFFF {
		t.Fatalf("lookup 7: found=%v vid=%#x err=%v", found, hit.ValueID, err)
	}
	if _, found, _ := r.LookupV6(v3.Ipv6Key{Lo: 100}); found {
		t.Fatalf("gap present")
	}
}

func TestExportEmptyV4(t *testing.T) {
	img := v4ImageV4(t, 4, nil)
	out, err := ExportV3(img, 1, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3: %v", err)
	}
	r, err := v3.Open(out)
	if err != nil {
		t.Fatalf("v3 Open: %v", err)
	}
	if r.RecordCount() != 0 {
		t.Fatalf("record_count = %d, want 0", r.RecordCount())
	}
	if _, found, _ := r.LookupV4(v3.Ipv4Key(42)); found {
		t.Fatalf("empty file has a hit")
	}
}

// --- COALESCING: adjacent byte-equal scopes merge in the v3 file ---

func TestExportCoalescesAdjacentEqualScopes(t *testing.T) {
	w := CreateV4(4, 0)
	mustSet(t, w, 10, 40, []byte{5, 0, 0, 0})
	mustSet(t, w, 41, 60, []byte{5, 0, 0, 0}) // contiguous, same scope
	mustSet(t, w, 100, 110, []byte{6, 0, 0, 0})
	if err := w.Commit(0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	out, err := ExportV3(w.Image(), 1, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3: %v", err)
	}
	r, err := v3.Open(out)
	if err != nil {
		t.Fatalf("v3 Open: %v", err)
	}
	if r.RecordCount() != 2 {
		t.Fatalf("record_count = %d, want 2 (coalesced)", r.RecordCount())
	}
	for _, ip := range []uint32{10, 40, 41, 60} {
		hit, found, _ := r.LookupV4(v3.Ipv4Key(ip))
		if !found {
			t.Fatalf("ip %d absent", ip)
		}
		val, _ := r.Value(hit.ValueID)
		if !bytes.Equal(val.Bytes, []byte{5, 0, 0, 0}) {
			t.Fatalf("ip %d: wrong scope %v", ip, val.Bytes)
		}
	}
	if _, found, _ := r.LookupV4(v3.Ipv4Key(70)); found {
		t.Fatalf("gap present")
	}
	hit, _, _ := r.LookupV4(v3.Ipv4Key(105))
	val, _ := r.Value(hit.ValueID)
	if !bytes.Equal(val.Bytes, []byte{6, 0, 0, 0}) {
		t.Fatalf("105: wrong scope %v", val.Bytes)
	}
}

func TestExportDistinctScopesShareInternedValue(t *testing.T) {
	img := v4ImageV4(t, 4, []rangeV4{
		{10, 20, []byte{1, 1, 1, 1}},
		{100, 200, []byte{1, 1, 1, 1}},
	})
	out, err := ExportV3(img, 3, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3: %v", err)
	}
	r, err := v3.Open(out)
	if err != nil {
		t.Fatalf("v3 Open: %v", err)
	}
	a, _, _ := r.LookupV4(v3.Ipv4Key(15))
	b, _, _ := r.LookupV4(v3.Ipv4Key(150))
	if a.ValueID != b.ValueID {
		t.Fatalf("byte-equal scopes not interned: %d vs %d", a.ValueID, b.ValueID)
	}
}

// --- ExportUnrepresentable: the v3 writer rejects the stream ---

func TestExportUnrepresentableBadMembershipValue(t *testing.T) {
	// type_id == 1 membership set: bytes must be a non-empty %4==0 ascending LE uint32
	// list. A 3-byte scope under type_id 1 is not %4 == 0 => v3 rejects.
	img := v4ImageV4(t, 3, []rangeV4{{10, 20, []byte{1, 2, 3}}})
	_, err := ExportV3(img, 1, exportMeta())
	if err == nil {
		t.Fatalf("expected ExportUnrepresentable, got nil")
	}
	if !errors.Is(err, ErrExportUnrepresentable) {
		t.Fatalf("error not ExportUnrepresentable: %v", err)
	}
}

func TestExportUnrepresentableMembershipNotAscending(t *testing.T) {
	// Two LE uint32 feed-ids that are not strictly ascending (5, 5) => v3 rejects.
	scope := []byte{5, 0, 0, 0, 5, 0, 0, 0}
	img := v4ImageV4(t, 8, []rangeV4{{10, 20, scope}})
	_, err := ExportV3(img, 1, exportMeta())
	if !errors.Is(err, ErrExportUnrepresentable) {
		t.Fatalf("error not ExportUnrepresentable: %v", err)
	}
}

func TestExportMembershipValueWellFormed(t *testing.T) {
	// A conforming type_id 1 scope (ascending LE uint32 ids) exports fine.
	scope := []byte{1, 0, 0, 0, 2, 0, 0, 0}
	img := v4ImageV4(t, 8, []rangeV4{{10, 20, scope}})
	out, err := ExportV3(img, 1, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3: %v", err)
	}
	r, err := v3.Open(out)
	if err != nil {
		t.Fatalf("v3 Open: %v", err)
	}
	hit, _, _ := r.LookupV4(v3.Ipv4Key(15))
	val, ok := r.Value(hit.ValueID)
	if !ok || val.TypeID != 1 || !bytes.Equal(val.Bytes, scope) {
		t.Fatalf("membership value not round-tripped: val=%+v ok=%v", val, ok)
	}
}

// --- determinism ---

func TestExportDeterministic(t *testing.T) {
	ranges := []rangeV4{
		{10, 20, []byte{1, 0, 0, 0}},
		{100, 200, []byte{2, 0, 0, 0}},
	}
	a, err := ExportV3(v4ImageV4(t, 4, ranges), 9, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3 a: %v", err)
	}
	b, err := ExportV3(v4ImageV4(t, 4, ranges), 9, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3 b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("export not deterministic")
	}
}

func mustSet(t *testing.T, w *Writer[Ipv4Key], from, to uint32, scope []byte) {
	t.Helper()
	if err := w.Set(Ipv4Key(from), Ipv4Key(to), scope); err != nil {
		t.Fatalf("Set [%d,%d]: %v", from, to, err)
	}
}

// crossGoldenHex is the exact v3 export of the fixed v4 input in
// TestExportCrossLanguageGolden. The identical vector is asserted by the Rust suite
// (export::tests::export_cross_language_golden), so a drift in either the v4 writer, the
// v3 writer, or this bridge — in either language — breaks one of the two tests. This is
// the in-repo proof that Go and Rust ExportV3 produce byte-identical v3 files.
const crossGoldenHex = "495052414e474533030000004800000000020000000000004800000000000000" +
	"0400000000000000030000000000000000f15365000000007b00000000000000" +
	"0000000000000000010000000100000068010000000000002800000000000000" +
	"0800000000000000000000000000000008c7c19038fe37bc38e43319e29a397c" +
	"90db31b02273878f11cabac3d06a9aa402000000010000009001000000000000" +
	"440000000000000010000000000000000000000000000000f2e428b622f379c6" +
	"7609ceb5e410a8650b4093511aabf7c416fa041345de9aa00300000001000000" +
	"d801000000000000280000000000000008000000000000000000000000000000" +
	"0d2957ad5396b26d66ad391625bfb507f81f37af57c81d67a605fb489e04a7b6" +
	"0500000000000000000200000000000000000000000000000800000000000000" +
	"0000000000000000e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934c" +
	"a495991b7852b855060000000500000063726f73730700000061747461636b73" +
	"000000000000000000000000000000000c000000040000000300000000000000" +
	"000000000000000000000000000000000a00000014000000000000001e000000" +
	"280000000100000064000000c800000002000000000000000300000007000000" +
	"0400000001000000070000000400000002000000070000000400000003000000"

func TestExportCrossLanguageGolden(t *testing.T) {
	w := CreateV4(4, 0)
	mustSet(t, w, 10, 20, []byte{1, 0, 0, 0})
	mustSet(t, w, 30, 40, []byte{2, 0, 0, 0})
	mustSet(t, w, 100, 200, []byte{3, 0, 0, 0})
	if err := w.Commit(0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	meta := V3Meta{
		FeedMeta:           v3.FeedMeta{Name: "cross", Category: "attacks"},
		GenerationUnixtime: 1_700_000_000,
	}
	out, err := ExportV3(w.Image(), 7, meta)
	if err != nil {
		t.Fatalf("ExportV3: %v", err)
	}
	want, err := hex.DecodeString(crossGoldenHex)
	if err != nil {
		t.Fatalf("bad golden hex: %v", err)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("cross-language golden mismatch:\n got %x\nwant %x", out, want)
	}
}
