package exactv4

import (
	"bytes"
	"testing"
)

func TestIPv6WireIsOneLittleEndianUint128(t *testing.T) {
	address := IPv6{Hi: 0x20010db800000000, Lo: 1}
	var wire [16]byte
	address.writeLE(wire[:])
	want := []byte{
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0xb8, 0x0d, 0x01, 0x20,
	}
	if !bytes.Equal(wire[:], want) {
		t.Fatalf("wire = %x, want %x", wire, want)
	}
	if got := (IPv6{}).readLE(wire[:]); got != address {
		t.Fatalf("decoded = %#v, want %#v", got, address)
	}
}

func TestAddressBoundaries(t *testing.T) {
	if _, ok := IPv4(0).Previous(); ok {
		t.Fatal("IPv4 minimum has a predecessor")
	}
	if _, ok := (^IPv4(0)).Next(); ok {
		t.Fatal("IPv4 maximum has a successor")
	}
	if _, ok := (IPv6{}).Previous(); ok {
		t.Fatal("IPv6 minimum has a predecessor")
	}
	if _, ok := (IPv6{Hi: ^uint64(0), Lo: ^uint64(0)}).Next(); ok {
		t.Fatal("IPv6 maximum has a successor")
	}
	if got, ok := (IPv6{Hi: 5, Lo: ^uint64(0)}).Next(); !ok || got != (IPv6{Hi: 6}) {
		t.Fatalf("IPv6 carry = %#v/%v", got, ok)
	}
}
