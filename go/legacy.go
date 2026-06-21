package iprangeformat

import (
	"bytes"
	"math/big"
	"strconv"
	"unicode/utf8"
)

// Legacy is a parsed legacy iprange binary file (v1.0 IPv4 / v2.0 IPv6), read for
// migration to v3. See .agents/sow/specs/legacy-binary-format.md. The IPv6 keys are
// already transposed to v3's hi-then-lo form.
type Legacy struct {
	IsV6      bool
	Optimized bool
	Lines     uint64
	UniqueIPs string // decimal, exactly as the header carried it
	RangesV4  [][2]Ipv4Key
	RangesV6  [][2]Ipv6Key
}

const (
	legacyMagicV4 = "iprange binary format v1.0"
	legacyMagicV6 = "iprange binary format v2.0"
)

var (
	legacyMarkerLE = [4]byte{0x4D, 0x3C, 0x2B, 0x1A} // 0x1A2B3C4D little-endian
	legacyMarkerBE = [4]byte{0x1A, 0x2B, 0x3C, 0x4D}
)

// ParseLegacy parses a legacy binary file or returns an error.
func ParseLegacy(b []byte) (*Legacy, error) {
	pos := 0
	magic, err := readLegacyLine(b, &pos)
	if err != nil {
		return nil, err
	}
	var isV6 bool
	switch magic {
	case legacyMagicV4:
		isV6 = false
	case legacyMagicV6:
		isV6 = true
	default:
		return nil, errInvalidInput("not a legacy iprange binary file")
	}

	if isV6 {
		fam, err := readLegacyLine(b, &pos)
		if err != nil {
			return nil, err
		}
		if fam != "ipv6" {
			return nil, errInvalidInput("legacy v2.0 missing ipv6 line")
		}
	}

	optLine, err := readLegacyLine(b, &pos)
	if err != nil {
		return nil, err
	}
	var optimized bool
	switch optLine {
	case "optimized":
		optimized = true
	case "non-optimized":
		optimized = false
	default:
		return nil, errInvalidInput("legacy optimized flag line malformed")
	}

	recordSize, err := parsePrefixed(b, &pos, "record size ")
	if err != nil {
		return nil, err
	}
	expectRS := uint64(8)
	if isV6 {
		expectRS = 32
	}
	if recordSize != expectRS {
		return nil, errInvalidInput("legacy record size mismatch for family")
	}
	records, err := parsePrefixed(b, &pos, "records ")
	if err != nil {
		return nil, err
	}
	bytesField, err := parsePrefixed(b, &pos, "bytes ")
	if err != nil {
		return nil, err
	}
	lines, err := parsePrefixed(b, &pos, "lines ")
	if err != nil {
		return nil, err
	}
	uniqueLine, err := readLegacyLine(b, &pos)
	if err != nil {
		return nil, err
	}
	uniqueStr, ok := stripPrefix(uniqueLine, "unique ips ")
	if !ok {
		return nil, errInvalidInput("legacy unique ips line prefix mismatch")
	}

	payload, ok1 := mul64(records, recordSize)
	payload, ok2 := add64(payload, 4)
	if !ok1 || !ok2 {
		return nil, errOverflow("legacy bytes field")
	}
	if bytesField != payload {
		return nil, errInvalidInput("legacy bytes field inconsistent")
	}
	if lines < records {
		return nil, errInvalidInput("legacy lines < records")
	}

	if pos+4 > len(b) {
		return nil, errInvalidInput("legacy truncated before marker")
	}
	var marker [4]byte
	copy(marker[:], b[pos:pos+4])
	var le bool
	switch marker {
	case legacyMarkerLE:
		le = true
	case legacyMarkerBE:
		le = false
	default:
		return nil, errInvalidInput("legacy endianness marker invalid")
	}
	pos += 4

	body := b[pos:]
	need, ok := mul64(records, recordSize)
	if !ok || uint64(len(body)) != need {
		return nil, errInvalidInput("legacy payload length mismatch / trailing data")
	}

	res := &Legacy{IsV6: isV6, Optimized: optimized, Lines: lines, UniqueIPs: uniqueStr}
	if isV6 {
		if err := parseLegacyV6(res, body, int(records), le, optimized, uniqueStr); err != nil {
			return nil, err
		}
	} else {
		if err := parseLegacyV4(res, body, int(records), le, optimized, uniqueStr); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func parseLegacyV4(res *Legacy, body []byte, n int, le, optimized bool, uniqueStr string) error {
	ranges := make([][2]Ipv4Key, 0, n)
	for i := 0; i < n; i++ {
		r := body[i*8 : i*8+8]
		addr := rdU32(r[0:4], le)
		bcast := rdU32(r[4:8], le)
		if addr > bcast {
			return errInvariant("legacy record addr > broadcast")
		}
		ranges = append(ranges, [2]Ipv4Key{Ipv4Key(addr), Ipv4Key(bcast)})
	}
	unique, okp := new(big.Int).SetString(uniqueStr, 10)
	if !okp {
		return errInvalidInput("legacy unique ips not a number")
	}
	if unique.Sign() < 0 || unique.BitLen() > 64 {
		return errInvalidInput("legacy v4 unique ips exceeds u64") // matches Rust's u64 parse
	}
	if big.NewInt(int64(len(ranges))).Cmp(unique) > 0 {
		return errInvalidInput("legacy unique ips < records")
	}
	if optimized {
		sum := new(big.Int)
		for i, rg := range ranges {
			if i > 0 && rg[0] <= ranges[i-1][1] {
				return errInvariant("legacy optimized records not sorted/disjoint")
			}
			sz := big.NewInt(int64(uint64(rg[1]-rg[0]) + 1))
			sum.Add(sum, sz)
		}
		if sum.Cmp(unique) != 0 {
			return errInvalidInput("legacy unique ips != sum of ranges")
		}
	}
	res.RangesV4 = ranges
	return nil
}

func parseLegacyV6(res *Legacy, body []byte, n int, le, optimized bool, uniqueStr string) error {
	ranges := make([][2]Ipv6Key, 0, n)
	for i := 0; i < n; i++ {
		r := body[i*32 : i*32+32]
		addr := rdV6(r[0:16], le)
		bcast := rdV6(r[16:32], le)
		if addr.cmp(bcast) > 0 {
			return errInvariant("legacy record addr > broadcast")
		}
		ranges = append(ranges, [2]Ipv6Key{addr, bcast})
	}
	unique, okp := new(big.Int).SetString(uniqueStr, 10)
	if !okp {
		return errInvalidInput("legacy unique ips not a number")
	}
	if unique.Sign() < 0 || unique.BitLen() > 128 {
		return errInvalidInput("legacy v6 unique ips exceeds u128") // matches Rust's u128 parse
	}
	if big.NewInt(int64(len(ranges))).Cmp(unique) > 0 {
		return errInvalidInput("legacy unique ips < records")
	}
	if optimized {
		sum := new(big.Int)
		one := big.NewInt(1)
		for i, rg := range ranges {
			if i > 0 && rg[0].cmp(ranges[i-1][1]) <= 0 {
				return errInvariant("legacy optimized records not sorted/disjoint")
			}
			sz := new(big.Int).Sub(v6Big(rg[1]), v6Big(rg[0]))
			sz.Add(sz, one)
			sum.Add(sum, sz)
		}
		if sum.Cmp(unique) != 0 {
			return errInvalidInput("legacy unique ips != sum of ranges")
		}
	}
	res.RangesV6 = ranges
	return nil
}

// rdV6 reads a legacy 16-byte IPv6 address into a v3 hi-then-lo key (transposition).
func rdV6(b []byte, le bool) Ipv6Key {
	if le {
		// little-endian writer stores {lo, hi}: bytes 0-7 = lo, 8-15 = hi.
		return Ipv6Key{Hi: rdU64(b[8:16], true), Lo: rdU64(b[0:8], true)}
	}
	// big-endian writer stores {hi, lo}: bytes 0-7 = hi, 8-15 = lo.
	return Ipv6Key{Hi: rdU64(b[0:8], false), Lo: rdU64(b[8:16], false)}
}

func rdU32(b []byte, le bool) uint32 {
	if le {
		return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	}
	return uint32(b[3]) | uint32(b[2])<<8 | uint32(b[1])<<16 | uint32(b[0])<<24
}

func rdU64(b []byte, le bool) uint64 {
	if le {
		return le64(b)
	}
	return be64(b)
}

func le64(b []byte) uint64 {
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}
func be64(b []byte) uint64 {
	return uint64(b[7]) | uint64(b[6])<<8 | uint64(b[5])<<16 | uint64(b[4])<<24 |
		uint64(b[3])<<32 | uint64(b[2])<<40 | uint64(b[1])<<48 | uint64(b[0])<<56
}

func v6Big(k Ipv6Key) *big.Int {
	x := new(big.Int).SetUint64(k.Hi)
	x.Lsh(x, 64)
	return x.Or(x, new(big.Int).SetUint64(k.Lo))
}

func readLegacyLine(b []byte, pos *int) (string, error) {
	start := *pos
	if start >= len(b) {
		return "", errInvalidInput("legacy header truncated")
	}
	nl := bytes.IndexByte(b[start:], '\n')
	if nl < 0 {
		return "", errInvalidInput("legacy header line missing newline")
	}
	line := b[start : start+nl]
	if !utf8.Valid(line) {
		return "", errInvalidInput("legacy header line not UTF-8")
	}
	*pos = start + nl + 1
	return string(line), nil
}

func stripPrefix(line, prefix string) (string, bool) {
	if len(line) < len(prefix) || line[:len(prefix)] != prefix {
		return "", false
	}
	return line[len(prefix):], true
}

func parsePrefixed(b []byte, pos *int, prefix string) (uint64, error) {
	line, err := readLegacyLine(b, pos)
	if err != nil {
		return 0, err
	}
	rest, ok := stripPrefix(line, prefix)
	if !ok {
		return 0, errInvalidInput("legacy header line prefix mismatch")
	}
	v, err := strconv.ParseUint(rest, 10, 64)
	if err != nil {
		return 0, errInvalidInput("legacy header numeric field malformed")
	}
	return v, nil
}

func mul64(a, b uint64) (uint64, bool) {
	if a != 0 && b > maxUint64/a {
		return 0, false
	}
	return a * b, true
}
