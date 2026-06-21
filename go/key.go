package iprangeformat

import (
	"encoding/binary"
	"math/bits"
)

// ipKey is the constraint for the two key widths, mirroring the Rust IpKey trait.
// It uses the recursive type-parameter pattern (K ipKey[K]) so methods can take and
// return the concrete key type. All methods are unexported: keys are package
// mechanics, and the exported API takes the concrete Ipv4Key / Ipv6Key.
type ipKey[T any] interface {
	writeLE(buf []byte)
	readLE(buf []byte) T
	cmp(o T) int // <0, 0, >0 numeric
	checkedInc() (T, bool)
	width() int
	recordSize() int
	// rangeSize returns the count of [self, end] inclusive as a 128-bit (lo, hi),
	// with ok=false when unrepresentable (only the full IPv6 space, size 2^128).
	rangeSize(end T) (lo, hi uint64, ok bool)
}

// Ipv4Key is an IPv4 address as a big-endian-valued uint32 (192.0.2.1 = 0xC0000201),
// stored little-endian on disk.
type Ipv4Key uint32

func (k Ipv4Key) writeLE(b []byte)      { binary.LittleEndian.PutUint32(b, uint32(k)) }
func (Ipv4Key) readLE(b []byte) Ipv4Key { return Ipv4Key(binary.LittleEndian.Uint32(b)) }
func (Ipv4Key) width() int              { return 4 }
func (Ipv4Key) recordSize() int         { return v4RecordSize }
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
func (k Ipv4Key) rangeSize(end Ipv4Key) (uint64, uint64, bool) {
	return uint64(end-k) + 1, 0, true // max 2^32, fits uint64
}

// Ipv6Key is an IPv6 address as (Hi, Lo) uint64 — Hi is the most-significant 64 bits.
// Stored as Hi little-endian then Lo little-endian (§3). Compared Hi then Lo.
type Ipv6Key struct {
	Hi uint64
	Lo uint64
}

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
func (Ipv6Key) width() int      { return 16 }
func (Ipv6Key) recordSize() int { return v6RecordSize }
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
	if k.Hi == maxUint64 && k.Lo == maxUint64 {
		return Ipv6Key{}, false
	}
	lo, c := bits.Add64(k.Lo, 1, 0)
	hi := k.Hi + c
	return Ipv6Key{Hi: hi, Lo: lo}, true
}
func (k Ipv6Key) rangeSize(end Ipv6Key) (uint64, uint64, bool) {
	if k.Hi == 0 && k.Lo == 0 && end.Hi == maxUint64 && end.Lo == maxUint64 {
		return 0, 0, false // full space, size 2^128
	}
	lo, borrow := bits.Sub64(end.Lo, k.Lo, 0)
	hi, _ := bits.Sub64(end.Hi, k.Hi, borrow)
	lo, c := bits.Add64(lo, 1, 0)
	hi, _ = bits.Add64(hi, 0, c)
	return lo, hi, true
}

// add128 returns a+b as a 128-bit (lo, hi) with an overflow flag.
func add128(aLo, aHi, bLo, bHi uint64) (lo, hi uint64, overflow bool) {
	lo, c := bits.Add64(aLo, bLo, 0)
	hi, c2 := bits.Add64(aHi, bHi, c)
	return lo, hi, c2 != 0
}
