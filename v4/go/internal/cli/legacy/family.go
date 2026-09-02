// Package legacy implements the released `iprange` command-line
// surface (SOW-0028 delivery step 4): grammar, text input, interval
// algebra, DNS, formatting, binary v1/v2 payloads, diagnostics and
// exit codes, byte-for-byte against the C oracle. It contains no v4
// persistence logic and creates no v4 artifact. Authority: the C
// sources under src/ (iprange.c, iprange6_main.c, ipset{,6}_*.c),
// tests.d/, and the wiki pages; the Rust port is a structural
// reference only.
package legacy

// Family selects the address width of one run. -4 and the default
// are identical; -6 selects IPv6 semantics (mapped-IPv4 input
// normalization, /128 default prefix, 128-bit counts).
type Family uint8

const (
	V4 Family = iota
	V6
)

// IP128 is a closed-address-space value. IPv4 values use Lo only
// (the high 64 bits stay zero); IPv6 uses both halves. Keeping one
// concrete type for both families mirrors the single authoritative
// core of the Rust port (the C code duplicated the pipeline).
type IP128 struct {
	Hi uint64
	Lo uint64
}

var (
	ip0    = IP128{}
	ipMax4 = IP128{Lo: 0xFFFF_FFFF}
	ipMax6 = IP128{Hi: 0xFFFF_FFFF_FFFF_FFFF, Lo: 0xFFFF_FFFF_FFFF_FFFF}
)

// Max returns the family maximum address.
func Max(fam Family) IP128 {
	if fam == V4 {
		return ipMax4
	}
	return ipMax6
}

// Compare returns -1, 0, or +1 (unsigned 128-bit order).
func (a IP128) Compare(b IP128) int {
	switch {
	case a.Hi < b.Hi:
		return -1
	case a.Hi > b.Hi:
		return 1
	case a.Lo < b.Lo:
		return -1
	case a.Lo > b.Lo:
		return 1
	}
	return 0
}

// Add adds v to the value; the caller guarantees the result fits the
// family width (wrap is not masked).
func (a IP128) Add(v uint64) IP128 {
	lo := a.Lo + v
	return IP128{Hi: a.Hi + carry64(a.Lo, lo), Lo: lo}
}

// Sub subtracts v; the caller guarantees a >= v.
func (a IP128) Sub(v uint64) IP128 {
	lo := a.Lo - v
	hi := a.Hi
	if a.Lo < v {
		hi--
	}
	return IP128{Hi: hi, Lo: lo}
}

func carry64(a, b uint64) uint64 {
	if b < a {
		return 1
	}
	return 0
}

// IsMax reports whether a is the family maximum (the adjacency guard
// of the C/Rust merge).
func (a IP128) IsMax(fam Family) bool {
	return a == Max(fam)
}

// Inc returns the next address, or none at the family maximum.
func (a IP128) Inc(fam Family) (IP128, bool) {
	if a.IsMax(fam) {
		return IP128{}, false
	}
	return a.Add(1), true
}

// Sub128 subtracts b from a with wraparound; the caller guarantees
// a >= b (the C end - start size checks and row subtractions).
func Sub128(a, b IP128) IP128 {
	lo := a.Lo - b.Lo
	hi := a.Hi - b.Hi
	if a.Lo < b.Lo {
		hi--
	}
	return IP128{Hi: hi, Lo: lo}
}

// Dec returns the previous address, or none at zero.
func (a IP128) Dec() (IP128, bool) {
	if a == ip0 {
		return IP128{}, false
	}
	return a.Sub(1), true
}

// As128 returns the numeric value as a math/big-free u128 pair
// (unused; kept for symmetry with the parse/fmt hooks).
func (a IP128) As128() (uint64, uint64) { return a.Hi, a.Lo }

// FromU128 builds a value, masking to the family width.
func FromU128(fam Family, hi, lo uint64) IP128 {
	if fam == V4 {
		return IP128{Lo: lo}
	}
	return IP128{Hi: hi, Lo: lo}
}
