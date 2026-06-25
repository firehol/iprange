package iprangedb

import (
	"encoding/binary"
	"math/bits"
)

// ipKey is the constraint for the two key widths, mirroring the Rust IpKey trait. It uses
// the recursive type-parameter pattern (K ipKey[K]) so methods take and return the
// concrete key type. Keys are compared numerically (IPv6 = Hi then Lo, no native 128-bit
// type on the hot path, §4); checkedInc / checkedDec implement the §4 u128_inc / u128_dec
// boundary rules used by set / delete to trim at from-1 / to+1.
type ipKey[T any] interface {
	comparable
	writeLE(buf []byte)
	readLE(buf []byte) T
	cmp(o T) int           // <0, 0, >0 numeric
	checkedInc() (T, bool) // self+1, ok=false at family_max
	checkedDec() (T, bool) // self-1, ok=false at family_min
	minKey() T             // the family minimum (0.0.0.0 / ::)
	maxKey() T             // the family maximum (all-ones)
	width() int
	version() IPVersion
	// CIDR-math helpers (§v4.1.B), not on the hot compare path.
	bitWidth() int      // address bits: 32 (IPv4) or 128 (IPv6)
	toU128() Uint128    // numeric value widened to 128 bits
	fromU128(Uint128) T // construct from a 128-bit value (low bits per width)
}

// Ipv4Key is an IPv4 address as a big-endian-valued uint32 (192.0.2.1 = 0xC0000201),
// stored little-endian on disk. Compared numerically.
type Ipv4Key uint32

func (k Ipv4Key) writeLE(b []byte)      { binary.LittleEndian.PutUint32(b, uint32(k)) }
func (Ipv4Key) readLE(b []byte) Ipv4Key { return Ipv4Key(binary.LittleEndian.Uint32(b)) }
func (Ipv4Key) width() int              { return 4 }
func (Ipv4Key) version() IPVersion      { return V4 }
func (Ipv4Key) minKey() Ipv4Key         { return 0 }
func (Ipv4Key) maxKey() Ipv4Key         { return ^Ipv4Key(0) }

func (k Ipv4Key) cmp(o Ipv4Key) int {
	switch {
	case k < o:
		return -1
	case k > o:
		return 1
	default:
		return 0
	}
}

func (k Ipv4Key) checkedInc() (Ipv4Key, bool) {
	if k == ^Ipv4Key(0) {
		return 0, false
	}
	return k + 1, true
}

func (k Ipv4Key) checkedDec() (Ipv4Key, bool) {
	if k == 0 {
		return 0, false
	}
	return k - 1, true
}

func (Ipv4Key) bitWidth() int              { return 32 }
func (k Ipv4Key) toU128() Uint128          { return Uint128{Hi: 0, Lo: uint64(k)} }
func (Ipv4Key) fromU128(v Uint128) Ipv4Key { return Ipv4Key(uint32(v.Lo)) }

// Ipv6Key is an IPv6 address as (Hi, Lo) uint64 — Hi is the most-significant 64 bits.
// Stored as Hi little-endian then Lo little-endian (§4). Compared Hi then Lo, which is
// exactly the numeric 128-bit order.
type Ipv6Key struct {
	Hi uint64
	Lo uint64
}

// Ipv6FromUint128 constructs an Ipv6Key from its 64-bit halves (hi, lo).
func Ipv6FromUint128(hi, lo uint64) Ipv6Key { return Ipv6Key{Hi: hi, Lo: lo} }

func (k Ipv6Key) writeLE(b []byte) {
	binary.LittleEndian.PutUint64(b[0:8], k.Hi)
	binary.LittleEndian.PutUint64(b[8:16], k.Lo)
}
func (Ipv6Key) readLE(b []byte) Ipv6Key {
	return Ipv6Key{
		Hi: binary.LittleEndian.Uint64(b[0:8]),
		Lo: binary.LittleEndian.Uint64(b[8:16]),
	}
}
func (Ipv6Key) width() int         { return 16 }
func (Ipv6Key) version() IPVersion { return V6 }
func (Ipv6Key) minKey() Ipv6Key    { return Ipv6Key{} }
func (Ipv6Key) maxKey() Ipv6Key    { return Ipv6Key{Hi: maxUint64, Lo: maxUint64} }

func (k Ipv6Key) cmp(o Ipv6Key) int {
	if k.Hi != o.Hi {
		if k.Hi < o.Hi {
			return -1
		}
		return 1
	}
	switch {
	case k.Lo < o.Lo:
		return -1
	case k.Lo > o.Lo:
		return 1
	default:
		return 0
	}
}

func (k Ipv6Key) checkedInc() (Ipv6Key, bool) {
	// u128_inc (§4): lo' = lo + 1; hi' = hi + carry; ok=false at all-ones.
	if k.Hi == maxUint64 && k.Lo == maxUint64 {
		return Ipv6Key{}, false
	}
	lo, c := bits.Add64(k.Lo, 1, 0)
	return Ipv6Key{Hi: k.Hi + c, Lo: lo}, true
}

func (k Ipv6Key) checkedDec() (Ipv6Key, bool) {
	// u128_dec (§4): borrow from hi when lo underflows; ok=false at the minimum.
	if k.Hi == 0 && k.Lo == 0 {
		return Ipv6Key{}, false
	}
	lo, borrow := bits.Sub64(k.Lo, 1, 0)
	return Ipv6Key{Hi: k.Hi - borrow, Lo: lo}, true
}

func (Ipv6Key) bitWidth() int              { return 128 }
func (k Ipv6Key) toU128() Uint128          { return Uint128{Hi: k.Hi, Lo: k.Lo} }
func (Ipv6Key) fromU128(v Uint128) Ipv6Key { return Ipv6Key{Hi: v.Hi, Lo: v.Lo} }

const maxUint64 = ^uint64(0)
const maxUint32 = ^uint32(0)
