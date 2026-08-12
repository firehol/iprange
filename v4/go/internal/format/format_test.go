package format

import (
	"testing"
)

// Literal vector tests for the exact little-endian codecs and constants.

func TestFixedConstants(t *testing.T) {
	if PageSize != 4096 || PageShift != 12 || MetaSize != 256 {
		t.Fatalf("page geometry: %d/%d/%d", PageSize, PageShift, MetaSize)
	}
	if MaxPageCount != 1<<32 || MaxTreeLevel != 31 {
		t.Fatalf("limits: %d/%d", MaxPageCount, MaxTreeLevel)
	}
	if MaxMetadataUncompressed != 20_971_520 {
		t.Fatalf("metadata limit %d", MaxMetadataUncompressed)
	}
	if BitmapLeafWords != 500 || BitmapLeafBits != 32_000 || BitmapFanout != 256 {
		t.Fatalf("bitmap constants")
	}
	if MainMagic != [8]byte{'I', 'P', 'R', 'A', 'N', 'G', 'E', '4'} ||
		PageMagic != [4]byte{'I', 'P', '4', 'P'} ||
		SidecarMagic != [8]byte{'I', 'P', 'R', 'D', 'R', 'S', '4', 0} {
		t.Fatalf("magics")
	}
}

func TestCRC32CStandardVector(t *testing.T) {
	// Standard CRC-32C check value for "123456789".
	if got := CRC32C([]byte("123456789")); got != 0xe3069283 {
		t.Fatalf("crc32c check value %08x", got)
	}
}

func TestU128RoundTrip(t *testing.T) {
	var b [16]byte
	PutU128(b[:], 0x0123456789abcdef, 0xfedcba9876543210)
	hi, lo := U128(b[:])
	if hi != 0x0123456789abcdef || lo != 0xfedcba9876543210 {
		t.Fatalf("u128 round trip %x %x", hi, lo)
	}
	// Little-endian first: the low limb occupies bytes [0,8).
	if b[0] != 0x10 || b[8] != 0xef {
		t.Fatalf("u128 byte order %x", b)
	}
}

func TestCardinality129Vectors(t *testing.T) {
	zero := CardinalityZero()
	if zero.String() != "0" {
		t.Fatalf("zero string %q", zero.String())
	}
	full := FullIPv6Space() // 2^128
	if full.String() != "340282366920938463463374607431768211456" {
		t.Fatalf("2^128 string %q", full.String())
	}
	max, err := SubFullIPv6One(full)
	if err != nil {
		t.Fatal(err)
	}
	if max.String() != "340282366920938463463374607431768211455" {
		t.Fatalf("2^128-1 string %q", max.String())
	}
	// Addition overflow above 2^129-1.
	top := Cardinality129{bit128: 1, hi: ^uint64(0), lo: ^uint64(0)}
	if _, err := top.Add(CardinalityFromUint64(1)); err == nil {
		t.Fatal("expected overflow")
	}
	// 2^129-1 is representable.
	if top.String() != "680564733841876926926749214863536422911" {
		t.Fatalf("2^129-1 string %q", top.String())
	}
	// Subtraction underflow.
	if _, err := zero.Sub(CardinalityFromUint64(1)); err == nil {
		t.Fatal("expected underflow")
	}
	// IPv4 inclusive bounds.
	n, err := IPv4Inclusive(0, 0xffffffff)
	if err != nil {
		t.Fatal(err)
	}
	if n.String() != "4294967296" {
		t.Fatalf("ipv4 full %q", n.String())
	}
	if _, err := IPv4Inclusive(5, 4); err == nil {
		t.Fatal("expected reversed range error")
	}
}

// SubFullIPv6One computes 2^128 - 1 for the vector tests.
func SubFullIPv6One(v Cardinality129) (Cardinality129, error) {
	return v.Sub(CardinalityFromUint64(1))
}

func TestIPv6InclusiveFullSpace(t *testing.T) {
	n, err := IPv6Inclusive(0, 0, ^uint64(0), ^uint64(0))
	if err != nil {
		t.Fatal(err)
	}
	if n.String() != "340282366920938463463374607431768211456" {
		t.Fatalf("ipv6 full %q", n.String())
	}
	if _, err := IPv6Inclusive(1, 0, 0, 0); err == nil {
		t.Fatal("expected reversed range error")
	}
}

func TestFeedNameGrammar(t *testing.T) {
	valid := []string{"a", "z9", "feed-000", "a.b_c-d", "0"}
	invalid := []string{"", "-a", "a-", "A", "a b", "ä", "a:b"}
	for _, s := range valid {
		if !FeedNameValid([]byte(s)) {
			t.Errorf("expected valid %q", s)
		}
	}
	for _, s := range invalid {
		if FeedNameValid([]byte(s)) {
			t.Errorf("expected invalid %q", s)
		}
	}
}

func TestStructureGeometry(t *testing.T) {
	if StructureRecordSlots != 50 {
		t.Fatalf("R = %d want 50", StructureRecordSlots)
	}
	// Root level for limits at exactly 50 and 51.
	if l, _ := StructureRootLevel(50); l != 0 {
		t.Fatalf("root level 50 = %d", l)
	}
	if l, _ := StructureRootLevel(51); l != 1 {
		t.Fatalf("root level 51 = %d", l)
	}
	span, _ := StructureSpanOfLevel(1)
	if span != 50 {
		t.Fatalf("span(1) = %d", span)
	}
	span, _ = StructureSpanOfLevel(2)
	if span != 25600 {
		t.Fatalf("span(2) = %d", span)
	}
	// Coverage(level) = R * 512^level; the root is the smallest level whose
	// coverage is at least the ID limit.
	if l, _ := StructureRootLevel(25600); l != 1 {
		t.Fatalf("root level 25600 = %d", l)
	}
	if l, _ := StructureRootLevel(25601); l != 2 {
		t.Fatalf("root level 25601 = %d", l)
	}
}

func TestMetadataCompressedBound(t *testing.T) {
	// A stored-block writer can always satisfy the bound (section 11):
	// blocks = max(1, ceil(uncompressed/65535)), bound = u + 5*blocks + 6.
	if got := MetadataCompressedBound(48); got != 48+5+6 {
		t.Fatalf("bound(48) = %d", got)
	}
	if got := MetadataCompressedBound(0); got != 11 {
		t.Fatalf("bound(0) = %d", got)
	}
	// The bound must cover a real stored empty stream (8 bytes).
	if MetadataCompressedBound(0) < 8 {
		t.Fatal("bound below real empty stream size")
	}
}

func TestErrorCodeTable(t *testing.T) {
	// Spot values of the canonical 1-69 table.
	checks := map[ErrorCode]ErrorCode{
		1:  CodeInvalidArgument,
		31: CodeIO,
		32: CodeFormatInvalid,
		33: CodeNotV4,
		44: CodeLiveCoordinationUnsupported,
		46: CodeLiveCoordinationMalformedRequiresReset,
		64: CodeCleanupInProgress,
		65: CodeFaultWorkerUnavailable,
		66: CodeFaultWorkerFailed,
		67: CodeUnsupportedStructure,
		68: CodeWrongStructureKind,
		69: CodeStructureIdExhausted,
	}
	for want, got := range checks {
		if got != want {
			t.Errorf("code %d = %d", want, got)
		}
	}
	if CodeStructureIdExhausted != 69 {
		t.Fatalf("table ends at %d", CodeStructureIdExhausted)
	}
}
