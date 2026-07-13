package iprangedb

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	v3 "github.com/firehol/iprange/v3/go"
)

// Export-v3 tests for the v4.3 → v3 bridge (export_v3.go). scope_id is a fixed u32;
// ExportV3 maps it to 4 little-endian bytes under the caller's type_id, then the v3
// writer owns coalescing, interning, and canonicalization.

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
	scopeID  uint32
}

type rangeV6 struct {
	from, to uint64
	scopeID  uint32
}

// v4ImageV4 builds a committed v4 IPv4 image from the given (range, scope_id) tuples.
func v4ImageV4(t *testing.T, ranges []rangeV4) []byte {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatalf("Create v4: %v", err)
	}
	for _, r := range ranges {
		if err := w.Set(Ipv4Key(r.from), Ipv4Key(r.to), r.scopeID); err != nil {
			t.Fatalf("v4 Set [%d,%d]: %v", r.from, r.to, err)
		}
	}
	if err := w.Commit(0, math.MaxUint64); err != nil {
		t.Fatalf("v4 Commit: %v", err)
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	return img
}

// v4ImageV6 builds a committed v4 IPv6 image from the given (range, scope_id) tuples
// (Lo half only; Hi is zero, enough for these fixtures).
func v4ImageV6(t *testing.T, ranges []rangeV6) []byte {
	t.Helper()
	w, err := Create[Ipv6Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatalf("Create v6: %v", err)
	}
	for _, r := range ranges {
		if err := w.Set(Ipv6Key{Lo: r.from}, Ipv6Key{Lo: r.to}, r.scopeID); err != nil {
			t.Fatalf("v6 Set [%d,%d]: %v", r.from, r.to, err)
		}
	}
	if err := w.Commit(0, math.MaxUint64); err != nil {
		t.Fatalf("v6 Commit: %v", err)
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	return img
}

// scopeLE is the 4 little-endian bytes ExportV3 encodes a scope_id as.
func scopeLE(id uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, id)
	return b
}

// --- ROUND-TRIP: v4 -> ExportV3 -> v3 Reader, asserting parity ---

func TestExportRoundtripV4(t *testing.T) {
	ranges := []rangeV4{{10, 20, 1}, {30, 40, 2}, {100, 200, 3}}
	img := v4ImageV4(t, ranges)
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
			if !bytes.Equal(val.Bytes, scopeLE(rg.scopeID)) {
				t.Fatalf("value bytes %v != scopeLE(%d) %v", val.Bytes, rg.scopeID, scopeLE(rg.scopeID))
			}
		}
	}
	for _, ip := range []uint32{9, 25, 41, 99, 201} {
		if _, found, _ := r.LookupV4(v3.Ipv4Key(ip)); found {
			t.Fatalf("gap %d unexpectedly present", ip)
		}
	}
}

// scope_id == 0 is a real value ([0,0,0,0]), not a presence-without-value sentinel.
func TestExportRoundtripV4ScopeIDZero(t *testing.T) {
	ranges := []rangeV4{{10, 20, 0}, {100, 110, 0}}
	img := v4ImageV4(t, ranges)
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
	for _, rg := range ranges {
		for _, ip := range []uint32{rg.from, rg.to} {
			hit, found, err := r.LookupV4(v3.Ipv4Key(ip))
			if err != nil || !found {
				t.Fatalf("lookup %d: found=%v err=%v", ip, found, err)
			}
			val, ok := r.Value(hit.ValueID)
			if !ok {
				t.Fatalf("ip %d: scope 0 must still resolve to a value", ip)
			}
			if val.TypeID != 1 || !bytes.Equal(val.Bytes, []byte{0, 0, 0, 0}) {
				t.Fatalf("ip %d: val=%+v", ip, val)
			}
		}
	}
}

func TestExportRoundtripV6(t *testing.T) {
	ranges := []rangeV6{{10, 20, 9}, {1000, 2000, 8}}
	img := v4ImageV6(t, ranges)
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
			if !ok || val.TypeID != typeID || !bytes.Equal(val.Bytes, scopeLE(rg.scopeID)) {
				t.Fatalf("lookup %d: val=%+v ok=%v", lo, val, ok)
			}
		}
	}
	if _, found, _ := r.LookupV6(v3.Ipv6Key{Lo: 500}); found {
		t.Fatalf("gap present")
	}
}

func TestExportEmptyV4(t *testing.T) {
	img := v4ImageV4(t, nil)
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

// --- COALESCING: adjacent equal-value scopes merge in the v3 file ---

func TestExportCoalescesAdjacentEqualScopes(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Set(Ipv4Key(10), Ipv4Key(40), 5))
	must(t, w.Set(Ipv4Key(41), Ipv4Key(60), 5)) // contiguous, same scope; v4 keeps them apart
	must(t, w.Set(Ipv4Key(100), Ipv4Key(110), 6))
	must(t, w.Commit(0, math.MaxUint64))
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	out, err := ExportV3(img, 1, exportMeta())
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
		if !bytes.Equal(val.Bytes, scopeLE(5)) {
			t.Fatalf("ip %d: wrong scope %v", ip, val.Bytes)
		}
	}
	if _, found, _ := r.LookupV4(v3.Ipv4Key(70)); found {
		t.Fatalf("gap present")
	}
	hit, _, _ := r.LookupV4(v3.Ipv4Key(105))
	val, _ := r.Value(hit.ValueID)
	if !bytes.Equal(val.Bytes, scopeLE(6)) {
		t.Fatalf("105: wrong scope %v", val.Bytes)
	}
}

func TestExportDistinctRangesShareInternedValue(t *testing.T) {
	img := v4ImageV4(t, []rangeV4{{10, 20, 42}, {100, 200, 42}})
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
		t.Fatalf("equal scope_ids not interned: %d vs %d", a.ValueID, b.ValueID)
	}
}

// --- type_id == 1 membership: a conforming single-feed-id scope round-trips ---

func TestExportMembershipValueWellFormed(t *testing.T) {
	// scope_id 5 -> LE [5,0,0,0] -> one feed-id {5}: a valid type_id 1 membership set.
	img := v4ImageV4(t, []rangeV4{{10, 20, 5}})
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
	if !ok || val.TypeID != 1 || !bytes.Equal(val.Bytes, scopeLE(5)) {
		t.Fatalf("membership value not round-tripped: val=%+v ok=%v", val, ok)
	}
}

// --- ExportUnrepresentable: the v3 writer rejects the stream ---

// The only v4.3 input the v3 writer rejects is the full IPv6 space (size 2^128, which
// does not fit unique_ip_count). A v4.3 scope_id is always 4 bytes, so the malformed /
// non-ascending membership rejections are unreachable from the export bridge.
func TestExportUnrepresentableFullV6Space(t *testing.T) {
	w, err := Create[Ipv6Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Set(Ipv6Key{}, Ipv6Key{Hi: math.MaxUint64, Lo: math.MaxUint64}, 1))
	must(t, w.Commit(0, math.MaxUint64))
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	_, err = ExportV3(img, 7, exportMeta())
	if err == nil {
		t.Fatalf("expected ExportUnrepresentable, got nil")
	}
	if !errors.Is(err, ErrExportUnrepresentable) {
		t.Fatalf("error not ExportUnrepresentable: %v", err)
	}
}

// A corrupt v4 image is surfaced as-is (not wrapped in ExportUnrepresentable).
func TestExportRejectsCorruptV4(t *testing.T) {
	_, err := ExportV3([]byte("not a v4 file"), 1, exportMeta())
	if err == nil {
		t.Fatal("expected error on corrupt input")
	}
	if errors.Is(err, ErrExportUnrepresentable) {
		t.Fatalf("corrupt input must not be ExportUnrepresentable: %v", err)
	}
}

// --- determinism ---

func TestExportDeterministic(t *testing.T) {
	ranges := []rangeV4{{10, 20, 1}, {100, 200, 2}}
	a, err := ExportV3(v4ImageV4(t, ranges), 9, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3 a: %v", err)
	}
	b, err := ExportV3(v4ImageV4(t, ranges), 9, exportMeta())
	if err != nil {
		t.Fatalf("ExportV3 b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("export not deterministic")
	}
}

// crossGoldenHex is the exact v3 export of the fixed v4 input below. The identical
// vector is asserted by the Rust suite (export::tests::export_cross_language_golden),
// so a drift in either the v4 scan, the v3 writer, or this bridge — in either
// language — breaks one of the two tests. v4.3 emits the same (range, scope_id) stream
// for this input, so the bytes are unchanged from the v4.0–v4.2 era golden.
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
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Set(Ipv4Key(10), Ipv4Key(20), 1))
	must(t, w.Set(Ipv4Key(30), Ipv4Key(40), 2))
	must(t, w.Set(Ipv4Key(100), Ipv4Key(200), 3))
	must(t, w.Commit(0, math.MaxUint64))
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	meta := V3Meta{
		FeedMeta:           v3.FeedMeta{Name: "cross", Category: "attacks"},
		GenerationUnixtime: 1_700_000_000,
	}
	out, err := ExportV3(img, 7, meta)
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
