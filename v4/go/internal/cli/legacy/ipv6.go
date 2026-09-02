// IPv6 family implementation of the legacy `iprange` surface: the
// strict inet_pton6 address grammar, the mapped-IPv4 normalization
// of bare IPv4 input, the str2netaddr6 prefix rules, and the glibc
// inet_ntop6 formatting quirks, exactly as the C oracle behaves
// (src/iprange6.h str2netaddr6/netmask6/broadcast6,
// src/ipset6_load.c parse_address6 + classify_address,
// src/ipset6_print.c ip6str_r, and the glibc inet_pton/inet_ntop/
// inet_aton the C binary links against; the Rust port
// v4/rust/iprange-cli/src/legacy/ipv6.rs is the structural
// reference).

package legacy

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// mappedPrefix is the IPv4-mapped IPv6 prefix ::ffff:0:0/96 (bits
// 32..48 set), as in IPV6_MAPPED_PREFIX in src/iprange6.h.
const mappedPrefix = 0xFFFF_0000_0000

// Class is the token class of the C classify_address() helper in
// src/ipset6_load.c: ClassV6 marks IPv6 text, ClassV4 marks IPv4
// text (dots, slash, or pure digits), ClassOther marks a
// hostname-class token.
type Class uint8

const (
	ClassV6 Class = iota
	ClassV4
	ClassOther
)

// Classify replicates C classify_address: a token containing ':' is
// IPv6; a token containing '.' or '/' is IPv4; a non-empty token of
// only decimal digits is IPv4; everything else is a hostname-class
// token.
func Classify(token string) Class {
	if strings.IndexByte(token, ':') >= 0 {
		return ClassV6
	}
	if strings.IndexByte(token, '.') >= 0 || strings.IndexByte(token, '/') >= 0 {
		return ClassV4
	}
	allDigits := token != ""
	for i := 0; i < len(token); i++ {
		if token[i] < '0' || token[i] > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return ClassV4
	}
	return ClassOther
}

// splitToken splits token at its first '/', returning the address
// part and the prefix text, exactly like the strchr(ipstr, '/') split
// of the C parsers. hasPrefix is false when token has no '/'.
func splitToken(token string) (addr string, prefix string, hasPrefix bool) {
	if i := strings.IndexByte(token, '/'); i >= 0 {
		return token[:i], token[i+1:], true
	}
	return token, "", false
}

// isASCIISpace reports C-locale whitespace, the bytes glibc isspace
// accepts for ASCII input.
func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == 0x0b || b == 0x0c || b == '\r'
}

// strtolDecimal ports glibc strtol(s, &end, 10) with full-string
// semantics: optional C-locale whitespace, optional sign, base-10
// digits, nothing else. ok is false for empty input, trailing junk,
// or overflow (the C errno || end == nptr || *end != '\0' failure
// set).
func strtolDecimal(text string) (value int64, ok bool) {
	b := []byte(text)
	i := 0
	for i < len(b) && isASCIISpace(b[i]) {
		i++
	}
	negative := false
	if i < len(b) && (b[i] == '+' || b[i] == '-') {
		negative = b[i] == '-'
		i++
	}
	start := i
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		d := int64(b[i] - '0')
		if value > (1<<63-1-d)/10 {
			return 0, false
		}
		value = value*10 + d
		i++
	}
	if i == start || i != len(b) {
		return 0, false
	}
	if negative {
		value = -value
	}
	return value, true
}

// hexDigitValue is the hex-digit value of glibc's inet_pton6: 0-9,
// a-f, A-F, else none.
func hexDigitValue(b byte) (uint32, bool) {
	switch {
	case b >= '0' && b <= '9':
		return uint32(b - '0'), true
	case b >= 'a' && b <= 'f':
		return uint32(b-'a') + 10, true
	case b >= 'A' && b <= 'F':
		return uint32(b-'A') + 10, true
	}
	return 0, false
}

// inetPton4 is glibc inet_pton(AF_INET, ...): strict dotted quad,
// no leading zeros, exactly four octets <= 255, full-string
// consumption.
func inetPton4(b []byte) ([4]byte, bool) {
	var out [4]byte
	idx := 0
	sawDigit := false
	octets := 0
	for _, ch := range b {
		if ch >= '0' && ch <= '9' {
			cur := uint32(out[idx])
			new := cur*10 + uint32(ch-'0')
			if sawDigit && cur == 0 {
				return [4]byte{}, false
			}
			if new > 255 {
				return [4]byte{}, false
			}
			out[idx] = byte(new)
			if !sawDigit {
				octets++
				if octets > 4 {
					return [4]byte{}, false
				}
				sawDigit = true
			}
		} else if ch == '.' && sawDigit {
			if octets == 4 {
				return [4]byte{}, false
			}
			idx++
			out[idx] = 0
			sawDigit = false
		} else {
			return [4]byte{}, false
		}
	}
	if octets < 4 {
		return [4]byte{}, false
	}
	return out, true
}

// inetPton6 is glibc inet_pton(AF_INET6, s, ...) (resolv/inet_pton_
// length.c): strict RFC 4291 text with a single '::', at most 4 hex
// digits per group, optional strict dotted-quad tail, nothing else.
// The result is the address as a big-endian numeric value.
func inetPton6(text string) (IP128, bool) {
	b := []byte(text)
	end := len(b)
	if end == 0 {
		return IP128{}, false
	}
	src := 0
	// A leading single ':' is only valid as the start of '::'; glibc
	// leaves src at the second ':' and the loop consumes it as the
	// '::' marker (colonp) below. curtok tracks the text after the
	// last ':' for a possible dotted-quad tail.
	if b[src] == ':' {
		src++
		if src >= end || b[src] != ':' {
			return IP128{}, false
		}
	}
	curtok := src
	var out [16]byte
	tp := 0      // bytes written so far
	colonp := -1 // index of the '::' marker, or -1
	xdigits := 0 // hex digits in the current group
	var val uint32
	for src < end {
		ch := b[src]
		src++
		if digit, ok := hexDigitValue(ch); ok {
			if xdigits == 4 {
				return IP128{}, false
			}
			val = (val << 4) | digit
			if val > 0xffff {
				return IP128{}, false
			}
			xdigits++
			continue
		}
		if ch == ':' {
			curtok = src
			if xdigits == 0 {
				// '::' marker: only one allowed.
				if colonp != -1 {
					return IP128{}, false
				}
				colonp = tp
				continue
			}
			// A trailing single ':' is invalid.
			if src >= end {
				return IP128{}, false
			}
			if tp+2 > 16 {
				return IP128{}, false
			}
			out[tp] = byte(val >> 8)
			out[tp+1] = byte(val)
			tp += 2
			xdigits = 0
			val = 0
			continue
		}
		if ch == '.' && tp+4 <= 16 {
			if v4, ok := inetPton4(b[curtok:end]); ok {
				copy(out[tp:tp+4], v4[:])
				tp += 4
				xdigits = 0
				break // the dotted quad consumed the rest of the string
			}
		}
		return IP128{}, false
	}
	if xdigits > 0 {
		if tp+2 > 16 {
			return IP128{}, false
		}
		out[tp] = byte(val >> 8)
		out[tp+1] = byte(val)
		tp += 2
	}
	if colonp != -1 {
		// '::' would expand to a zero-width field.
		if tp == 16 {
			return IP128{}, false
		}
		n := tp - colonp
		copy(out[16-n:], out[colonp:colonp+n])
		for i := colonp; i < 16-n; i++ {
			out[i] = 0
		}
		tp = 16
	}
	if tp != 16 {
		return IP128{}, false
	}
	return IP128{
		Hi: binary.BigEndian.Uint64(out[0:8]),
		Lo: binary.BigEndian.Uint64(out[8:16]),
	}, true
}

// atonV4 parses one IPv4 token with glibc inet_aton semantics on top
// of the lead's inetAton (ipv4.go): glibc stops the number at the
// first C-locale whitespace byte and ignores everything after it
// (verified against glibc 2.44: "1.2.3.4 x" parses as 1.2.3.4), while
// inetAton rejects any trailing byte. CLI tokens never carry
// whitespace (the loaders split on it), so the wrapper only changes
// direct-API behavior.
func atonV4(text string) (uint32, error) {
	cut := text
	for i := 0; i < len(text); i++ {
		if isASCIISpace(text[i]) {
			cut = text[:i]
			break
		}
	}
	return inetAton(cut)
}

// MappedAddr builds the mapped IPv6 form of an IPv4 value (C
// ipv4_to_mapped6 in src/iprange6.h).
func MappedAddr(v4 IP128) IP128 {
	return IP128{Lo: mappedPrefix | v4.Lo}
}

// ParseAddrV6 parses one token with the C classify_address routing of
// parse_address6: v6-class tokens use the inet_pton6 grammar,
// v4-class tokens use the inet_aton grammar normalized to the mapped
// address ::ffff:A.B.C.D, and hostname-class tokens fail with the C
// DNS error text. An embedded '/prefix' is split off first and
// ignored (prefixes belong to ParseCIDRV6), exactly like C.
func ParseAddrV6(token string) (IP128, error) {
	addrText, _, _ := splitToken(token)
	switch Classify(token) {
	case ClassV6:
		addr, ok := inetPton6(addrText)
		if !ok {
			return IP128{}, fmt.Errorf("iprange: Invalid IPv6 address %s", addrText)
		}
		return addr, nil
	case ClassV4:
		v4, err := atonV4(addrText)
		if err != nil {
			return IP128{}, invalidAddress(addrText)
		}
		return MappedAddr(IP128{Lo: uint64(v4)}), nil
	default:
		return IP128{}, fmt.Errorf("iprange: Cannot parse address: %s", token)
	}
}

// netmask6 is the 128-bit netmask of prefix (C netmask6 in
// src/iprange6.h).
func netmask6(prefix uint32) IP128 {
	switch {
	case prefix == 0:
		return IP128{}
	case prefix >= 128:
		return ipMax6
	case prefix <= 64:
		return IP128{Hi: ^uint64(0) << (64 - prefix)}
	default:
		return IP128{Hi: ^uint64(0), Lo: ^uint64(0) << (128 - prefix)}
	}
}

// v6Range is the C str2netaddr6 range: [network addr, broadcast]
// over the full 128-bit space; fixNetwork == false (C
// --dont-fix-network) keeps the raw host start with the prefix
// broadcast end.
func v6Range(addr IP128, prefix uint32, fixNetwork bool) Range {
	mask := netmask6(prefix)
	lo := addr
	if fixNetwork {
		lo = IP128{Hi: addr.Hi & mask.Hi, Lo: addr.Lo & mask.Lo}
	}
	hi := IP128{Hi: lo.Hi | ^mask.Hi, Lo: lo.Lo | ^mask.Lo}
	return Range{Lo: lo, Hi: hi}
}

// v4PrefixFromText resolves a v4-class prefix text exactly like the C
// str2netaddr path parse_address6 uses for class-4 tokens: a
// full-string decimal in 0..=32 wins; anything else is interpreted as
// an inverted netmask whose trailing-one count becomes the prefix.
// The C error texts are "Invalid address %s." for unparseable text
// (a_to_hl runs first) and "Invalid netmask %s" for a non-contiguous
// bit pattern.
func v4PrefixFromText(text string) (uint32, error) {
	if v, ok := strtolDecimal(text); ok && v >= 0 && v <= 32 {
		return uint32(v), nil
	}
	maskValue, err := atonV4(text)
	if err != nil {
		return 0, invalidAddress(text)
	}
	mask := ^maskValue
	prefix := int32(32)
	for mask&1 == 1 {
		mask >>= 1
		prefix--
	}
	if mask != 0 {
		return 0, fmt.Errorf("iprange: Invalid netmask %s", text)
	}
	return uint32(prefix), nil
}

// ParseCIDRV6 parses one token with the full C str2netaddr6 /
// str2netaddr routing of parse_address6. An embedded '/prefix' text
// wins (v6 class: full-string decimal 0..=128; v4 class: decimal
// 0..=32 or netmask text, applied to the 32-bit value before
// mapping); without one, the prefix argument is used with the v6
// default 128 (v4-class tokens use C's fixed /32, so a caller
// passing the v6 default gets the mapped single address). The
// defaultPrefix argument is accepted for trait uniformity with the
// v4 family (ParseCIDRV4) and the Rust reference, but never
// consulted: C hardcodes 128 for IPv6 text and 32 for IPv4 text in
// v6 mode. fixNetwork masks the host bits; false keeps the raw host
// start.
func ParseCIDRV6(token string, prefix uint32, fixNetwork bool, defaultPrefix uint32) (Range, error) {
	addrText, prefixText, hasPrefix := splitToken(token)
	switch Classify(token) {
	case ClassV6:
		if hasPrefix {
			v, ok := strtolDecimal(prefixText)
			if !ok || v < 0 || v > 128 {
				return Range{}, fmt.Errorf("iprange: Invalid IPv6 prefix /%s", prefixText)
			}
			prefix = uint32(v)
		} else if prefix > 128 {
			return Range{}, fmt.Errorf("iprange: Invalid IPv6 prefix /%d", prefix)
		}
		addr, ok := inetPton6(addrText)
		if !ok {
			return Range{}, fmt.Errorf("iprange: Invalid IPv6 address %s", addrText)
		}
		return v6Range(addr, prefix, fixNetwork), nil

	case ClassV4:
		if hasPrefix {
			v, err := v4PrefixFromText(prefixText)
			if err != nil {
				return Range{}, err
			}
			prefix = v
		} else if prefix == 128 || prefix == 32 {
			// Bare v4 token in v6 mode: C's fixed IPv4 default is
			// 32; tolerate the v6 family default 128 as "no
			// prefix" for callers passing the v6 default.
			prefix = 32
		} else if prefix > 32 {
			return Range{}, fmt.Errorf("iprange: Invalid netmask %d", prefix)
		}
		v4, err := atonV4(addrText)
		if err != nil {
			return Range{}, invalidAddress(addrText)
		}
		var mask uint32
		if prefix == 0 {
			mask = 0
		} else if prefix >= 32 {
			mask = 0xFFFF_FFFF
		} else {
			mask = 0xFFFF_FFFF << (32 - prefix)
		}
		lo := v4
		if fixNetwork {
			lo = v4 & mask
		}
		return Range{
			Lo: MappedAddr(IP128{Lo: uint64(lo)}),
			Hi: MappedAddr(IP128{Lo: uint64(lo | ^mask)}),
		}, nil

	default:
		return Range{}, fmt.Errorf("iprange: Cannot parse address: %s", token)
	}
}

// ParsePrefixV6 parses a prefix length with the C str2netaddr6
// validation: full-string strtol base 10 (whitespace and sign
// accepted, trailing junk and overflow rejected), range 0..=128.
func ParsePrefixV6(text string) (uint32, error) {
	if v, ok := strtolDecimal(text); ok && v >= 0 && v <= 128 {
		return uint32(v), nil
	}
	return 0, fmt.Errorf("iprange: Invalid IPv6 prefix /%s", text)
}

// inetNtop6 is glibc inet_ntop(AF_INET6, ...) (resolv/inet_ntop.c,
// inet_ntop6_format): the first longest zero run of >= 2 words is
// compressed (ties: the first), groups print lowercase hex without
// leading zeros, and a '::'-leading zero run of length 6 (or of
// length 5 with word 5 == 0xffff) prints the low 32 bits as a
// decimal dotted quad. Hence mapped IPv4 prints ::ffff:A.B.C.D,
// ::ffff:0:1 prints ::ffff:0.0.0.1, and ::1234:5678 prints
// ::18.52.86.120.
func inetNtop6(addr IP128) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[0:8], addr.Hi)
	binary.BigEndian.PutUint64(b[8:16], addr.Lo)
	var words [8]uint16
	for i := range words {
		words[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}

	// First longest run of zero words (length >= 2).
	bestBase, bestLen := -1, 0
	curBase, curLen := -1, 0
	for i, w := range words {
		if w == 0 {
			if curBase == -1 {
				curBase, curLen = i, 1
			} else {
				curLen++
			}
		} else if curBase != -1 {
			if bestLen == 0 || curLen > bestLen {
				bestBase, bestLen = curBase, curLen
			}
			curBase = -1
		}
	}
	if curBase != -1 && (bestLen == 0 || curLen > bestLen) {
		bestBase, bestLen = curBase, curLen
	}
	if bestLen < 2 {
		bestBase, bestLen = -1, 0
	}

	var out strings.Builder
	out.Grow(46)
	for i, w := range words {
		if bestBase != -1 && i >= bestBase && i < bestBase+bestLen {
			if i == bestBase {
				out.WriteByte(':')
			}
			continue
		}
		if i != 0 {
			out.WriteByte(':')
		}
		if i == 6 && bestBase == 0 && (bestLen == 6 || (bestLen == 5 && words[5] == 0xffff)) {
			fmt.Fprintf(&out, "%d.%d.%d.%d", b[12], b[13], b[14], b[15])
			break
		}
		out.WriteString(strconv.FormatUint(uint64(w), 16))
	}
	if bestBase != -1 && bestBase+bestLen == 8 {
		out.WriteByte(':')
	}
	return out.String()
}

// FmtAddrV6 renders the glibc inet_ntop form of addr.
func FmtAddrV6(addr IP128) string {
	return inetNtop6(addr)
}

// FmtCIDRV6 renders addr/prefix; the full width (or anything above
// it) prints as a bare address (C print_addr6 rule).
func FmtCIDRV6(addr IP128, prefix uint32) string {
	if prefix >= 128 {
		return FmtAddrV6(addr)
	}
	return fmt.Sprintf("%s/%d", FmtAddrV6(addr), prefix)
}

// ConvertForeignV4 extracts an IPv4 range from the mapped-IPv6 forms
// the C v4-mode loader converts (src/ipset_load.c): exactly "::ffff:"
// (four f's, case-insensitive, colon at offset 6) followed by a
// non-empty [0-9./]+ tail and end of record (spaces/tabs then end,
// newline, CR, '#' or ';'), parsed with the C str2netaddr defaults
// (fix-network on, prefix 32). Everything else yields ok == false,
// matching the C lines dropped as IPv6. "::ffff:0102:0304" is NOT
// converted (the tail scan stops at the second colon).
func ConvertForeignV4(token string) (Range, bool) {
	if len(token) < 8 || token[0] != ':' || token[1] != ':' || token[6] != ':' {
		return Range{}, false
	}
	for i := 2; i < 6; i++ {
		if token[i] != 'f' && token[i] != 'F' {
			return Range{}, false
		}
	}
	tail := token[7:]
	end := 0
	for end < len(tail) {
		c := tail[end]
		if (c >= '0' && c <= '9') || c == '.' || c == '/' {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return Range{}, false
	}
	rest := strings.TrimLeft(tail[end:], " \t")
	if rest != "" && rest[0] != '\n' && rest[0] != '\r' && rest[0] != '#' && rest[0] != ';' {
		return Range{}, false
	}
	v4Text := tail[:end]
	var (
		addr   uint32
		prefix uint32
		err    error
	)
	if i := strings.IndexByte(v4Text, '/'); i >= 0 {
		addr, err = atonV4(v4Text[:i])
		if err != nil {
			return Range{}, false
		}
		prefix, err = v4PrefixFromText(v4Text[i+1:])
		if err != nil {
			return Range{}, false
		}
	} else {
		addr, err = atonV4(v4Text)
		if err != nil {
			return Range{}, false
		}
		prefix = 32
	}
	return prefixRange(addr, prefix, true), true
}
