package iprangedb

import (
	"bytes"
	"testing"
)

func TestIPv6KeyWorkedExample(t *testing.T) {
	// 2001:db8::1 -> hi=0x20010db800000000, lo=1 (key encoding shared with v3 §3).
	k := Ipv6Key{Hi: 0x20010db800000000, Lo: 0x1}
	var buf [16]byte
	k.writeLE(buf[:])
	want := [16]byte{
		0x00, 0x00, 0x00, 0x00, 0xb8, 0x0d, 0x01, 0x20, // hi, little-endian
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // lo, little-endian
	}
	if buf != want {
		t.Fatalf("IPv6 key on-disk bytes = %x, want %x", buf, want)
	}
	if Ipv6Key.readLE(Ipv6Key{}, buf[:]) != k {
		t.Fatal("round-trip")
	}
}

func TestIPv4KeyWorkedExample(t *testing.T) {
	// 192.0.2.1 = 0xC0000201 -> LE bytes 01 02 00 c0.
	k := Ipv4Key(0xC0000201)
	var buf [4]byte
	k.writeLE(buf[:])
	if !bytes.Equal(buf[:], []byte{0x01, 0x02, 0x00, 0xc0}) {
		t.Fatalf("IPv4 key bytes = %x", buf)
	}
	if Ipv4Key.readLE(0, buf[:]) != k {
		t.Fatal("round-trip")
	}
}

func TestIPv6NumericOrderNotBytewise(t *testing.T) {
	a := Ipv6Key{Hi: 1, Lo: 0}
	b := Ipv6Key{Hi: 0, Lo: maxUint64}
	if a.cmp(b) <= 0 {
		t.Fatal("compare hi then lo, not raw bytes: a should be > b")
	}
}

func TestCheckedIncV6CarryAndMax(t *testing.T) {
	got, ok := (Ipv6Key{Hi: 5, Lo: maxUint64}).checkedInc()
	if !ok || got != (Ipv6Key{Hi: 6, Lo: 0}) {
		t.Fatalf("carry from lo into hi: got %+v ok=%v", got, ok)
	}
	got, ok = (Ipv6Key{Hi: 0, Lo: 41}).checkedInc()
	if !ok || got != (Ipv6Key{Hi: 0, Lo: 42}) {
		t.Fatal("plain inc")
	}
	if _, ok := (Ipv6Key{Hi: maxUint64, Lo: maxUint64}).checkedInc(); ok {
		t.Fatal("no +1 at family_max")
	}
}

func TestCheckedDecV6BorrowAndMin(t *testing.T) {
	got, ok := (Ipv6Key{Hi: 6, Lo: 0}).checkedDec()
	if !ok || got != (Ipv6Key{Hi: 5, Lo: maxUint64}) {
		t.Fatalf("borrow from hi into lo: got %+v ok=%v", got, ok)
	}
	if _, ok := (Ipv6Key{}).checkedDec(); ok {
		t.Fatal("no -1 at family_min")
	}
}

func TestCheckedIncDecV4Bounds(t *testing.T) {
	if v, ok := Ipv4Key(41).checkedInc(); !ok || v != 42 {
		t.Fatal("v4 inc")
	}
	if _, ok := Ipv4Key(^Ipv4Key(0)).checkedInc(); ok {
		t.Fatal("v4 inc at max")
	}
	if v, ok := Ipv4Key(42).checkedDec(); !ok || v != 41 {
		t.Fatal("v4 dec")
	}
	if _, ok := Ipv4Key(0).checkedDec(); ok {
		t.Fatal("v4 dec at min")
	}
}
