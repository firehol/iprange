package iprangedb

import "testing"

func TestRecordV4RoundTripWithScope(t *testing.T) {
	sz := recordSizeBytes(4) // 12
	buf := make([]byte, sz)
	var fromLE, toLE [4]byte
	Ipv4Key(0x0a000000).writeLE(fromLE[:])
	Ipv4Key(0x0a0000ff).writeLE(toLE[:])
	recordWrite(buf, fromLE[:], toLE[:], 0xDDCCBBAA, 4)

	gotFrom := Ipv4Key(0).readLE(buf[0:4])
	gotTo := Ipv4Key(0).readLE(buf[4:8])
	gotScope := u32le(buf, 8)
	if gotFrom != Ipv4Key(0x0a000000) || gotTo != Ipv4Key(0x0a0000ff) {
		t.Fatal("v4 from/to round-trip")
	}
	if gotScope != 0xDDCCBBAA {
		t.Fatalf("scope = %x", gotScope)
	}
}

func TestRecordV6RoundTripWithScope(t *testing.T) {
	sz := recordSizeBytes(16) // 36
	buf := make([]byte, sz)
	from := Ipv6Key{Hi: 0x20010db800000000, Lo: 0}
	to := Ipv6Key{Hi: 0x20010db800000000, Lo: 0xffff}
	var fromLE, toLE [16]byte
	from.writeLE(fromLE[:])
	to.writeLE(toLE[:])
	recordWrite(buf, fromLE[:], toLE[:], 0x7F, 16)

	gotFrom := Ipv6Key{}.readLE(buf[0:16])
	gotTo := Ipv6Key{}.readLE(buf[16:32])
	gotScope := u32le(buf, 32)
	if gotFrom != from || gotTo != to {
		t.Fatal("v6 from/to round-trip")
	}
	if gotScope != 0x7F {
		t.Fatalf("scope = %x", gotScope)
	}
}

// v4.3: scope_id is always a fixed 4-byte u32 — record_size = 2*key_width + ScopeIDSize
// regardless of scope_mode (the mode only changes how the u32 is interpreted).
func TestRecordSizeAlwaysIncludesScopeID(t *testing.T) {
	if recordSizeBytes(4) != 2*4+ScopeIDSize {
		t.Fatalf("v4 record_size = %d, want %d", recordSizeBytes(4), 2*4+ScopeIDSize)
	}
	if recordSizeBytes(16) != 2*16+ScopeIDSize {
		t.Fatalf("v6 record_size = %d, want %d", recordSizeBytes(16), 2*16+ScopeIDSize)
	}
}
