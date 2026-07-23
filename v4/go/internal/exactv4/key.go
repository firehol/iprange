package exactv4

import (
	"encoding/binary"
	"math/bits"
)

type rangeKey[K any] interface {
	comparable
	writeLE([]byte)
	readLE([]byte) K
	fromHalves(uint64, uint64) K
	compare(K) int
	minimum() K
	maximum() K
	width() int
	family() AddressFamily
	halves() (uint64, uint64)
}

// IPv4 is one IPv4 address in numeric network-address order.
type IPv4 uint32

func (k IPv4) writeLE(dst []byte)                { binary.LittleEndian.PutUint32(dst, uint32(k)) }
func (IPv4) readLE(src []byte) IPv4              { return IPv4(binary.LittleEndian.Uint32(src)) }
func (IPv4) fromHalves(_ uint64, lo uint64) IPv4 { return IPv4(uint32(lo)) }
func (IPv4) minimum() IPv4                       { return 0 }
func (IPv4) maximum() IPv4                       { return ^IPv4(0) }
func (IPv4) width() int                          { return 4 }
func (IPv4) family() AddressFamily               { return AddressFamilyIPv4 }
func (k IPv4) halves() (uint64, uint64)          { return 0, uint64(k) }

func (k IPv4) compare(other IPv4) int {
	switch {
	case k < other:
		return -1
	case k > other:
		return 1
	default:
		return 0
	}
}

// Next returns the following IPv4 address, or false at the family maximum.
func (k IPv4) Next() (IPv4, bool) {
	if k == ^IPv4(0) {
		return 0, false
	}
	return k + 1, true
}

// Previous returns the preceding IPv4 address, or false at the family minimum.
func (k IPv4) Previous() (IPv4, bool) {
	if k == 0 {
		return 0, false
	}
	return k - 1, true
}

// IPv6 is one IPv6 address as numeric high and low 64-bit halves.
type IPv6 struct {
	Hi uint64
	Lo uint64
}

// IPv6FromHalves constructs an IPv6 address from its numeric halves.
func IPv6FromHalves(hi, lo uint64) IPv6 { return IPv6{Hi: hi, Lo: lo} }

// Exact v4 stores one little-endian u128, so the low limb comes first.
func (k IPv6) writeLE(dst []byte) {
	binary.LittleEndian.PutUint64(dst[0:8], k.Lo)
	binary.LittleEndian.PutUint64(dst[8:16], k.Hi)
}

func (IPv6) readLE(src []byte) IPv6 {
	return IPv6{
		Hi: binary.LittleEndian.Uint64(src[8:16]),
		Lo: binary.LittleEndian.Uint64(src[0:8]),
	}
}

func (IPv6) fromHalves(hi, lo uint64) IPv6 { return IPv6{Hi: hi, Lo: lo} }

func (IPv6) minimum() IPv6              { return IPv6{} }
func (IPv6) maximum() IPv6              { return IPv6{Hi: ^uint64(0), Lo: ^uint64(0)} }
func (IPv6) width() int                 { return 16 }
func (IPv6) family() AddressFamily      { return AddressFamilyIPv6 }
func (k IPv6) halves() (uint64, uint64) { return k.Hi, k.Lo }

func (k IPv6) compare(other IPv6) int {
	if k.Hi < other.Hi {
		return -1
	}
	if k.Hi > other.Hi {
		return 1
	}
	if k.Lo < other.Lo {
		return -1
	}
	if k.Lo > other.Lo {
		return 1
	}
	return 0
}

// Next returns the following IPv6 address, or false at the family maximum.
func (k IPv6) Next() (IPv6, bool) {
	if k == (IPv6{Hi: ^uint64(0), Lo: ^uint64(0)}) {
		return IPv6{}, false
	}
	lo, carry := bits.Add64(k.Lo, 1, 0)
	return IPv6{Hi: k.Hi + carry, Lo: lo}, true
}

// Previous returns the preceding IPv6 address, or false at the family minimum.
func (k IPv6) Previous() (IPv6, bool) {
	if k == (IPv6{}) {
		return IPv6{}, false
	}
	lo, borrow := bits.Sub64(k.Lo, 1, 0)
	return IPv6{Hi: k.Hi - borrow, Lo: lo}, true
}
