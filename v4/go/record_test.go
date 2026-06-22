package iprangedb

import (
	"bytes"
	"testing"
)

func TestRecordV4RoundTripWithScope(t *testing.T) {
	sz := int(recordSize(4, 4)) // 12
	buf := make([]byte, sz)
	scope := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	recordWrite[Ipv4Key](buf, Ipv4Key(0x0a000000), Ipv4Key(0x0a0000ff), scope)
	r := newRecordRef[Ipv4Key](buf)
	if r.from() != Ipv4Key(0x0a000000) || r.to() != Ipv4Key(0x0a0000ff) {
		t.Fatal("v4 from/to round-trip")
	}
	if !bytes.Equal(r.scope(), scope) {
		t.Fatalf("scope = %x", r.scope())
	}
}

func TestRecordV6RoundTripWithScope(t *testing.T) {
	sz := int(recordSize(16, 1)) // 33
	buf := make([]byte, sz)
	from := Ipv6Key{Hi: 0x20010db800000000, Lo: 0}
	to := Ipv6Key{Hi: 0x20010db800000000, Lo: 0xffff}
	recordWrite[Ipv6Key](buf, from, to, []byte{0x7F})
	r := newRecordRef[Ipv6Key](buf)
	if r.from() != from || r.to() != to {
		t.Fatal("v6 from/to round-trip")
	}
	if !bytes.Equal(r.scope(), []byte{0x7F}) {
		t.Fatalf("scope = %x", r.scope())
	}
}

func TestRecordScopeWidthZero(t *testing.T) {
	sz := int(recordSize(4, 0)) // 8
	buf := make([]byte, sz)
	recordWrite[Ipv4Key](buf, Ipv4Key(1), Ipv4Key(2), nil)
	r := newRecordRef[Ipv4Key](buf)
	if r.from() != 1 || r.to() != 2 {
		t.Fatal("from/to")
	}
	if len(r.scope()) != 0 {
		t.Fatalf("presence map scope must be empty, got %x", r.scope())
	}
}
