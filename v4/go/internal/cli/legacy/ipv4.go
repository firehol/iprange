// IPv4 family implementation: inet_aton numeric forms, netmask
// prefixes, and dotted-quad formatting, exactly as the C oracle
// behaves (src/iprange.h a_to_hl/str2netaddr, src/ipset_load.c
// mapped-IPv6 extraction, src/ipset_print.c ip2str_r).

package legacy

import (
	"fmt"
	"strings"
)

// invalidAddress is the C a_to_hl failure text (token without any
// /prefix suffix, trailing period included).
func invalidAddress(token string) error {
	return fmt.Errorf("iprange: Invalid address %s.", token)
}

// parseComponent consumes one inet_aton component at *pos with the
// C-literal base choice (0x hex, 0 octal, otherwise decimal). Hex
// requires at least one digit after 0x; component overflow above u64
// is rejected. The caller ensures *pos < len(bytes).
func parseComponent(bytes []byte, pos *int) (uint64, error) {
	start := *pos
	var base uint64
	switch {
	case bytes[start] == '0':
		if start+1 < len(bytes) && (bytes[start+1] == 'x' || bytes[start+1] == 'X') {
			base = 16
		} else {
			base = 8
		}
	case bytes[start] >= '1' && bytes[start] <= '9':
		base = 10
	default:
		return 0, fmt.Errorf("bad component")
	}
	i := start
	if base == 16 {
		i = start + 2
	}
	var value, hexDigits uint64
	for i < len(bytes) {
		var digit uint64
		switch base {
		case 16:
			switch {
			case bytes[i] >= '0' && bytes[i] <= '9':
				digit = uint64(bytes[i] - '0')
			case bytes[i] >= 'a' && bytes[i] <= 'f':
				digit = uint64(bytes[i]-'a') + 10
			case bytes[i] >= 'A' && bytes[i] <= 'F':
				digit = uint64(bytes[i]-'A') + 10
			default:
				goto done
			}
		case 8:
			if bytes[i] < '0' || bytes[i] > '7' {
				goto done
			}
			digit = uint64(bytes[i] - '0')
		default:
			if bytes[i] < '0' || bytes[i] > '9' {
				goto done
			}
			digit = uint64(bytes[i] - '0')
		}
		// Accumulation overflow: the oracle rejects any component
		// magnitude above u32, so a u64 overflow is invalid.
		if value > (1<<64-1-digit)/base {
			return 0, fmt.Errorf("component overflow")
		}
		value = value*base + digit
		if base == 16 {
			hexDigits++
		}
		i++
	}
done:
	if base == 16 && hexDigits == 0 {
		// A bare 0x/0X is rejected by the oracle.
		return 0, fmt.Errorf("empty hex")
	}
	*pos = i
	return value, nil
}

// inetAton ports glibc inet_aton exactly: 1..=4 dot-separated
// components, no empty/leading/trailing dots; component base 0x hex,
// leading-0 octal, else decimal; shortened forms a.b = 8.24 and
// a.b.c = 8.8.16; field bounds 8/24, 8/8/16, 8/8/8/8.
func inetAton(text string) (uint32, error) {
	if len(text) == 0 || text[0] < '0' || text[0] > '9' {
		return 0, fmt.Errorf("not an address")
	}
	bytes := []byte(text)
	var parts [3]uint64
	partsCount := 0
	pos := 0
	var value uint64
	for {
		part, err := parseComponent(bytes, &pos)
		if err != nil {
			return 0, err
		}
		if pos < len(bytes) && bytes[pos] == '.' {
			if partsCount >= 3 {
				return 0, fmt.Errorf("too many components")
			}
			parts[partsCount] = part
			partsCount++
			pos++
			if pos == len(bytes) {
				return 0, fmt.Errorf("trailing dot")
			}
			continue
		}
		if pos != len(bytes) {
			return 0, fmt.Errorf("trailing junk")
		}
		value = part
		break
	}
	fits := func(v uint64, bits uint) bool { return v <= (1<<bits)-1 }
	var out uint32
	switch partsCount + 1 {
	case 1:
		if value > 0xFFFF_FFFF {
			return 0, fmt.Errorf("overflow")
		}
		out = uint32(value)
	case 2:
		if !fits(parts[0], 8) || !fits(value, 24) {
			return 0, fmt.Errorf("bounds")
		}
		out = uint32(parts[0])<<24 | uint32(value)
	case 3:
		if !fits(parts[0], 8) || !fits(parts[1], 8) || !fits(value, 16) {
			return 0, fmt.Errorf("bounds")
		}
		out = uint32(parts[0])<<24 | uint32(parts[1])<<16 | uint32(value)
	default:
		if !fits(parts[0], 8) || !fits(parts[1], 8) || !fits(parts[2], 8) || !fits(value, 8) {
			return 0, fmt.Errorf("bounds")
		}
		out = uint32(parts[0])<<24 | uint32(parts[1])<<16 | uint32(parts[2])<<8 | uint32(value)
	}
	return out, nil
}

// ParseAddrV4 parses one bare IPv4 address (inet_aton forms).
func ParseAddrV4(token string) (IP128, error) {
	v, err := inetAton(token)
	if err != nil {
		return IP128{}, invalidAddress(token)
	}
	return IP128{Lo: uint64(v)}, nil
}

// prefixRange computes addr/prefix under the cidr_use_network policy
// (fix-network masks the host bits; --dont-fix-network keeps the raw
// host start and its broadcast end).
func prefixRange(addr uint32, prefix uint32, fixNetwork bool) Range {
	var mask uint32
	if prefix != 0 {
		mask = 0xFFFF_FFFF << (32 - prefix)
	}
	lo := addr
	if fixNetwork {
		lo = addr & mask
	}
	return Range{Lo: IP128{Lo: uint64(lo)}, Hi: IP128{Lo: uint64(lo | ^mask)}}
}

// ParseCIDRV4 parses ADDR/PREFIX (or the pre-split address with the
// given prefix) with the C str2netaddr semantics: an embedded '/'
// carries the prefix text (decimal 0..=32 or a netmask), otherwise
// the caller's default applies.
func ParseCIDRV4(token string, prefix uint32, fixNetwork bool, defaultPrefix uint32) (Range, error) {
	address := token
	if slash := strings.IndexByte(token, '/'); slash >= 0 {
		p, err := ParsePrefixV4(token[slash+1:])
		if err != nil {
			return Range{}, err
		}
		prefix = p
		address = token[:slash]
	}
	addr, err := ParseAddrV4(address)
	if err != nil {
		return Range{}, err
	}
	return prefixRange(uint32(addr.Lo), prefix, fixNetwork), nil
}

// ParsePrefixV4 parses a prefix length: full-string strtol semantics
// (sign allowed, overflow rejected), value 0..=32 wins; anything
// else falls back to the netmask text form.
func ParsePrefixV4(text string) (uint32, error) {
	bytes := []byte(text)
	i := 0
	negative := false
	if i < len(bytes) && (bytes[i] == '-' || bytes[i] == '+') {
		negative = bytes[i] == '-'
		i++
	}
	start := i
	var value int64
	overflow := false
	for i < len(bytes) && bytes[i] >= '0' && bytes[i] <= '9' {
		d := int64(bytes[i] - '0')
		if value > (1<<63-1-d)/10 {
			overflow = true
		} else {
			value = value*10 + d
		}
		i++
	}
	if !overflow && i == len(bytes) && i > start {
		if negative {
			value = -value
		}
		if value >= 0 && value <= 32 {
			return uint32(value), nil
		}
	}
	// Netmask fallback (C str2netaddr / netmask()): contiguous 1-bits
	// of !inet_aton(text) count the prefix; the C loop starts at 32
	// and shifts out trailing 1-bits, then rejects any remaining
	// bits. An unparseable netmask reports the plain-address error
	// first (C a_to_hl() runs before the contiguity check).
	v, err := inetAton(text)
	if err != nil {
		return 0, invalidAddress(text)
	}
	mask := ^v
	prefix := uint32(32)
	for mask&1 == 1 {
		mask >>= 1
		prefix--
	}
	if mask != 0 {
		return 0, fmt.Errorf("iprange: Invalid netmask %s", text)
	}
	return prefix, nil
}

// FmtAddrV4 renders the canonical dotted quad.
func FmtAddrV4(addr IP128) string {
	v := uint32(addr.Lo)
	return fmt.Sprintf("%d.%d.%d.%d", v>>24, v>>16&0xFF, v>>8&0xFF, v&0xFF)
}

// FmtCIDRV4 renders addr/prefix; the full-width prefix prints as a
// bare address (C print_addr rule).
func FmtCIDRV4(addr IP128, prefix uint32) string {
	if prefix == 32 {
		return FmtAddrV4(addr)
	}
	return fmt.Sprintf("%s/%d", FmtAddrV4(addr), prefix)
}
