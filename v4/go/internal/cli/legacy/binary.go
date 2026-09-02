// Legacy binary save/load (v1.0 IPv4, v2.0 IPv6) with the exact C
// validation rules and diagnostics.
//
// Ported 1:1 from src/ipset_binary.c (v1.0) and src/ipset6_binary.c
// (v2.0); the Rust port v4/rust/iprange-cli/src/legacy/binary.rs is
// the structural reference. The v1 payload is:
//
//	iprange binary format v1.0\n
//	optimized\n | non-optimized\n
//	record size 8\n
//	records N\n
//	bytes N*8+4\n
//	lines L\n
//	unique ips U\n
//	<u32 marker 0x1A2B3C4D, native endian>
//	<N records: u32 lo, u32 hi, native endian>
//
// The v2 payload adds an "ipv6\n" family line after the header and
// uses 32-byte records; each u128 is written as the native
// uint128_t layout of the C (lo64 then hi64 on little-endian hosts),
// and "unique ips" is decimal u128. The header "unique ips" is
// validated against a recomputation over the records before it is
// trusted, exactly like the C loader.

package legacy

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/bits"
	"sort"
)

const (
	binaryHeaderV10 = "iprange binary format v1.0\n"
	binaryHeaderV20 = "iprange binary format v2.0\n"
	endianMarker    = 0x1A2B3C4D
)

var one128 = IP128{Lo: 1}

// lineReader is an fgets()-style line reader over an in-memory
// payload: each line is the bytes up to and including '\n', or the
// remaining bytes when the payload ends without a newline (the C
// buffer would hold that final chunk at EOF). A nil return means EOF
// (C fgets returning NULL), matching the Rust Option<&[u8]>.
type lineReader struct {
	data []byte
	pos  int
}

func (r *lineReader) next() []byte {
	if r.pos >= len(r.data) {
		return nil
	}
	rest := r.data[r.pos:]
	end := len(rest)
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		end = i + 1
	}
	line := rest[:end]
	r.pos += end
	return line
}

func (r *lineReader) remaining() int { return len(r.data) - r.pos }

func (r *lineReader) take(n int) []byte {
	out := r.data[r.pos : r.pos+n]
	r.pos += n
	return out
}

// lineText renders a line for the "found '%s'" diagnostics: EOF
// renders as an empty quoted string (the C `s ? s : ""`).
func lineText(line []byte) string {
	if line == nil {
		return ""
	}
	return string(line)
}

// parseU64Field is the C parse_binary_size_field /
// parse_binary_u64_field: the value must start with an ASCII digit,
// parse as an unsigned decimal, and end at the line end ('\n' or
// end-of-buffer). The C prints the raw rest-of-line (newline
// included) inside the quotes.
func parseU64Field(source, field string, value []byte) (uint64, error) {
	text := string(value)
	if len(value) == 0 || value[0] < '0' || value[0] > '9' {
		return 0, fmt.Errorf("iprange: %s: invalid %s value '%s'", source, field, text)
	}
	var parsed uint64
	rest := value
	for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
		digit := uint64(rest[0] - '0')
		if parsed > (math.MaxUint64-digit)/10 {
			return 0, fmt.Errorf("iprange: %s: invalid %s value '%s'", source, field, text)
		}
		parsed = parsed*10 + digit
		rest = rest[1:]
	}
	if !(len(rest) == 0 || string(rest) == "\n") {
		return 0, fmt.Errorf("iprange: %s: invalid %s value '%s'", source, field, text)
	}
	return parsed, nil
}

// parseU128Field is the C parse_binary6_u128_field: decimal u128
// with wrap detection ("value overflow") and the same line-end rule.
func parseU128Field(source, field string, value []byte) (IP128, error) {
	text := string(value)
	if len(value) == 0 || value[0] < '0' || value[0] > '9' {
		return IP128{}, fmt.Errorf("iprange: %s: invalid %s value '%s'", source, field, text)
	}
	var parsed IP128
	rest := value
	for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
		prev := parsed
		parsed = mulAddU128(parsed, 10, uint64(rest[0]-'0'))
		if parsed.Compare(prev) < 0 {
			return IP128{}, fmt.Errorf("iprange: %s: %s value overflow", source, field)
		}
		rest = rest[1:]
	}
	if !(len(rest) == 0 || string(rest) == "\n") {
		return IP128{}, fmt.Errorf("iprange: %s: invalid %s value '%s'", source, field, text)
	}
	return parsed, nil
}

// mulAddU128 returns the low 128 bits of a*m + d (wrapping), the
// u128_mul_u64/u128_add of the C u128 arithmetic.
func mulAddU128(a IP128, m, d uint64) IP128 {
	hi, lo := bits.Mul64(a.Lo, m)
	hi += a.Hi * m
	lo += d
	if lo < d {
		hi++
	}
	return IP128{Hi: hi, Lo: lo}
}

// addU128 returns the low 128 bits of a + b (wrapping).
func addU128(a, b IP128) IP128 {
	lo := a.Lo + b.Lo
	hi := a.Hi + b.Hi
	if lo < a.Lo {
		hi++
	}
	return IP128{Hi: hi, Lo: lo}
}

// subU128 returns the low 128 bits of a - b (wrapping).
func subU128(a, b IP128) IP128 {
	hi := a.Hi - b.Hi
	if a.Lo < b.Lo {
		hi--
	}
	return IP128{Hi: hi, Lo: a.Lo - b.Lo}
}

// sizeWrapping is hi - lo + 1 mod 2^128 (the C u128 size of one
// record); the caller guarantees lo <= hi.
func sizeWrapping(lo, hi IP128) IP128 {
	return addU128(subU128(hi, lo), one128)
}

// decimalU128 renders a u128 value in decimal via the package helper
// (print.go formatU128, the C u128_to_dec).
func decimalU128(v IP128) string { return formatU128(v.Hi, v.Lo) }

// validatePayloadV1 is the C binary_validate_payload
// (src/ipset_binary.c): verifies record order/adjacency, recomputes
// the unique count (direct sum for an optimized payload, sort-and-
// merge sweep otherwise), rejects a header that claims optimized
// over a non-optimized payload, and reports whether the payload is
// actually optimized.
func validatePayloadV1(source string, headerOptimized bool, entries int, expected IP128, ranges []Range) (bool, error) {
	payloadOptimized := true
	if entries == 0 {
		if expected != (IP128{}) {
			return false, fmt.Errorf("iprange: %s: unique IPs (%s) do not match the binary payload (0)", source, decimalU128(expected))
		}
		return true, nil
	}

	for i, r := range ranges {
		if r.Lo.Compare(r.Hi) > 0 {
			return false, fmt.Errorf("iprange: %s: invalid binary record %d has addr > broadcast", source, i+1)
		}
	}

	for i := 1; i < entries; i++ {
		prev, curr := ranges[i-1], ranges[i]
		if curr.Lo.Compare(prev.Lo) < 0 ||
			curr.Lo.Compare(prev.Hi) <= 0 ||
			(!prev.Hi.IsMax(V4) && curr.Lo == prev.Hi.Add(1)) {
			payloadOptimized = false
			break
		}
	}

	var actual IP128
	if payloadOptimized {
		for _, r := range ranges {
			actual = addU128(actual, sizeWrapping(r.Lo, r.Hi))
		}
	} else {
		// The C sorts (addr asc, broadcast desc) and sweep-merges to
		// count unique IPs of a non-optimized payload.
		tmp := make([]Range, len(ranges))
		copy(tmp, ranges)
		sort.Slice(tmp, func(i, j int) bool {
			a, b := tmp[i], tmp[j]
			if c := a.Lo.Compare(b.Lo); c != 0 {
				return c < 0
			}
			return a.Hi.Compare(b.Hi) > 0
		})
		lo, hi := tmp[0].Lo, tmp[0].Hi
		for _, r := range tmp[1:] {
			if r.Hi.Compare(hi) <= 0 {
				continue
			}
			if r.Lo.Compare(hi) <= 0 || (!hi.IsMax(V4) && r.Lo == hi.Add(1)) {
				hi = r.Hi
				continue
			}
			actual = addU128(actual, sizeWrapping(lo, hi))
			lo, hi = r.Lo, r.Hi
		}
		actual = addU128(actual, sizeWrapping(lo, hi))
	}

	if expected != actual {
		return false, fmt.Errorf("iprange: %s: unique IPs (%s) do not match the binary payload (%s)", source, decimalU128(expected), decimalU128(actual))
	}

	if headerOptimized && !payloadOptimized {
		return false, fmt.Errorf("iprange: %s: binary payload claims to be optimized but contains overlapping, adjacent, or unsorted records", source)
	}

	return payloadOptimized, nil
}

// validatePayloadV2 is the C binary6_validate_payload
// (src/ipset6_binary.c): same walk as v1, but non-optimized payloads
// trust the header count (the set is re-optimized after loading) and
// the unique total wraps like the C u128 arithmetic.
func validatePayloadV2(source string, headerOptimized bool, entries int, expected IP128, ranges []Range) (bool, error) {
	payloadOptimized := true
	if entries == 0 {
		if expected != (IP128{}) {
			return false, fmt.Errorf("iprange: %s: unique IPs do not match the binary payload", source)
		}
		return true, nil
	}

	for i, r := range ranges {
		if r.Lo.Compare(r.Hi) > 0 {
			return false, fmt.Errorf("iprange: %s: invalid binary record %d has addr > broadcast", source, i+1)
		}
	}

	for i := 1; i < entries; i++ {
		prev, curr := ranges[i-1], ranges[i]
		if curr.Lo.Compare(prev.Lo) < 0 ||
			curr.Lo.Compare(prev.Hi) <= 0 ||
			(!prev.Hi.IsMax(V6) && curr.Lo == addU128(prev.Hi, one128)) {
			payloadOptimized = false
			break
		}
	}

	if !payloadOptimized {
		if headerOptimized {
			return false, fmt.Errorf("iprange: %s: binary payload claims to be optimized but contains overlapping, adjacent, or unsorted records", source)
		}
		return false, nil
	}

	// The C u128 sum wraps (a full-universe single record is 2^128
	// and wraps to zero); replicate with wrapping arithmetic.
	var actual IP128
	for _, r := range ranges {
		actual = addU128(actual, sizeWrapping(r.Lo, r.Hi))
	}
	if expected != actual {
		return false, fmt.Errorf("iprange: %s: unique IPs do not match the binary payload", source)
	}
	return true, nil
}

// parseHeader parses the seven header lines (v1) or eight (v2,
// including the family line) at the top of r and returns
// (headerOptimized, entries, bytes, lines, unique). isV2 selects the
// v2 grammar, family line, and error texts; recordBytes is the
// record size the caller expects (8 or 32).
func parseHeader(r *lineReader, source string, isV2 bool, recordBytes uint64) (bool, uint64, uint64, uint64, IP128, error) {
	expectedHeader := []byte(binaryHeaderV10)
	if isV2 {
		expectedHeader = []byte(binaryHeaderV20)
	}
	v1 := !isV2

	line := r.next()
	if line == nil || !bytes.Equal(line, expectedHeader) {
		what := "expecting binary header"
		if isV2 {
			what = "expecting binary v2 header"
		}
		return false, 0, 0, 0, IP128{}, fmt.Errorf("iprange: %s %s but found '%s'.", source, what, lineText(line))
	}

	if isV2 {
		line = r.next()
		if line == nil || !bytes.Equal(line, []byte("ipv6\n")) {
			return false, 0, 0, 0, IP128{}, fmt.Errorf("iprange: %s expected family 'ipv6' but found '%s'.", source, lineText(line))
		}
	}

	line = r.next()
	var headerOptimized bool
	switch string(line) {
	case "optimized\n":
		headerOptimized = true
	case "non-optimized\n":
		headerOptimized = false
	default:
		what := "expected optimized flag"
		if v1 {
			what = "2nd line should be the optimized flag,"
		}
		return false, 0, 0, 0, IP128{}, fmt.Errorf("iprange: %s %s but found '%s'.", source, what, lineText(line))
	}

	// The remaining five header lines carry a fixed key prefix; a
	// missing line or a wrong key uses the C per-line diagnostic.
	field := func(key, whatV1, whatV2 string) ([]byte, error) {
		line := r.next()
		if line != nil && bytes.HasPrefix(line, []byte(key)) {
			return line[len(key):], nil
		}
		what := whatV1
		if isV2 {
			what = whatV2
		}
		return nil, fmt.Errorf("iprange: %s %s but found '%s'.", source, what, lineText(line))
	}
	nextU64 := func(key, whatV1, whatV2, fieldName string) (uint64, error) {
		value, err := field(key, whatV1, whatV2)
		if err != nil {
			return 0, err
		}
		return parseU64Field(source, fieldName, value)
	}

	size, err := nextU64("record size ", "3rd line should be the record size,", "expected record size", "record size")
	if err != nil {
		return false, 0, 0, 0, IP128{}, err
	}
	if size != recordBytes {
		return false, 0, 0, 0, IP128{}, fmt.Errorf("iprange: %s: invalid record size %d (expected %d)", source, size, recordBytes)
	}

	entries, err := nextU64("records ", "4th line should be the number of records,", "expected records count", "records")
	if err != nil {
		return false, 0, 0, 0, IP128{}, err
	}

	bytes, err := nextU64("bytes ", "5th line should be the number of bytes,", "expected bytes count", "bytes")
	if err != nil {
		return false, 0, 0, 0, IP128{}, err
	}

	lines, err := nextU64("lines ", "6th line should be the number of lines read,", "expected lines count", "lines")
	if err != nil {
		return false, 0, 0, 0, IP128{}, err
	}

	value, err := field("unique ips ", "7th line should be the number of unique IPs,", "expected unique ips")
	if err != nil {
		return false, 0, 0, 0, IP128{}, err
	}
	var unique IP128
	if v1 {
		u, err := parseU64Field(source, "unique ips", value)
		if err != nil {
			return false, 0, 0, 0, IP128{}, err
		}
		unique = IP128{Lo: u}
	} else {
		unique, err = parseU128Field(source, "unique ips", value)
		if err != nil {
			return false, 0, 0, 0, IP128{}, err
		}
	}

	return headerOptimized, entries, bytes, lines, unique, nil
}

// checkEntriesOverflow is the C allocation-overflow guard
// (`invalid number of records`).
func checkEntriesOverflow(source string, entries, recordBytes uint64) (int, error) {
	max := (math.MaxUint64 - 4) / recordBytes
	if entries > max {
		return 0, fmt.Errorf("iprange: %s: invalid number of records (%d)", source, entries)
	}
	return int(entries), nil
}

// readRecords reads entries records of recordBytes bytes from r and
// decodes each with readOne. The C fread underflow diagnostic
// reports how many whole records were actually present.
func readRecords(r *lineReader, source string, entries, recordBytes int, readOne func([]byte) Range) ([]Range, error) {
	need := uint64(entries) * uint64(recordBytes)
	if uint64(r.remaining()) < need {
		loaded := r.remaining() / recordBytes
		return nil, fmt.Errorf("iprange: %s: expected to load %d entries, loaded %d", source, entries, loaded)
	}
	ranges := make([]Range, 0, entries)
	for i := 0; i < entries; i++ {
		ranges = append(ranges, readOne(r.take(recordBytes)))
	}
	return ranges, nil
}

// checkMarker reads the u32 marker and verifies it against the
// native-endianness marker (the C fread + `endian != endianness`
// check).
func checkMarker(r *lineReader, source string) error {
	if r.remaining() < 4 {
		return fmt.Errorf("iprange: %s: cannot load ipset header", source)
	}
	if binary.NativeEndian.Uint32(r.take(4)) != endianMarker {
		return fmt.Errorf("iprange: %s: incompatible endianness", source)
	}
	return nil
}

// LoadV1 parses the released IPv4 binary payload (v1.0), validating
// the exact header lines and native-endian record layout. Errors
// carry the exact C diagnostic text of src/ipset_binary.c (the parse
// layer adds the outer "Cannot fast load {name}" wrapper).
func LoadV1(data []byte, source string) (*IpSet, error) {
	r := &lineReader{data: data}
	headerOptimized, entries, bytes, lines, unique, err := parseHeader(r, source, false, 8)
	if err != nil {
		return nil, err
	}

	// The C `entries > ((SIZE_MAX - 4) / sizeof(network_addr_t))`
	// allocation guard.
	n, err := checkEntriesOverflow(source, entries, 8)
	if err != nil {
		return nil, err
	}

	expectedBytes := uint64(n)*8 + 4
	if bytes != expectedBytes {
		return nil, fmt.Errorf("iprange: %s invalid number of bytes, found %d, expected %d.", source, bytes, expectedBytes)
	}

	if err := checkMarker(r, source); err != nil {
		return nil, err
	}

	if unique.Compare(IP128{Lo: uint64(n)}) < 0 {
		return nil, fmt.Errorf("iprange: %s: unique IPs (%s) cannot be less than entries (%d)", source, decimalU128(unique), n)
	}
	if lines < uint64(n) {
		return nil, fmt.Errorf("iprange: %s: lines (%d) cannot be less than entries (%d)", source, lines, n)
	}

	ranges, err := readRecords(r, source, n, 8, func(raw []byte) Range {
		return Range{
			Lo: IP128{Lo: uint64(binary.NativeEndian.Uint32(raw[0:4]))},
			Hi: IP128{Lo: uint64(binary.NativeEndian.Uint32(raw[4:8]))},
		}
	})
	if err != nil {
		return nil, err
	}

	if r.remaining() != 0 {
		return nil, fmt.Errorf("iprange: %s: trailing data found after binary payload", source)
	}

	payloadOptimized, err := validatePayloadV1(source, headerOptimized, n, unique, ranges)
	if err != nil {
		return nil, err
	}

	return &IpSet{
		Fam:       V4,
		Ranges:    ranges,
		Entries:   n,
		Lines:     int(lines),
		Unique:    unique,
		Optimized: headerOptimized && payloadOptimized,
	}, nil
}

// LoadV2 parses the released IPv6 binary payload (v2.0). Errors
// carry the exact C diagnostic text of src/ipset6_binary.c (the
// parse layer adds the outer "Cannot load binary v2 {name}"
// wrapper).
func LoadV2(data []byte, source string) (*IpSet, error) {
	r := &lineReader{data: data}
	headerOptimized, entries, bytes, lines, unique, err := parseHeader(r, source, true, 32)
	if err != nil {
		return nil, err
	}

	// The C `entries > ((SIZE_MAX - 4) / sizeof(network_addr6_t))`
	// allocation guard.
	n, err := checkEntriesOverflow(source, entries, 32)
	if err != nil {
		return nil, err
	}

	expectedBytes := uint64(n)*32 + 4
	if bytes != expectedBytes {
		return nil, fmt.Errorf("iprange: %s invalid number of bytes, found %d, expected %d.", source, bytes, expectedBytes)
	}

	if err := checkMarker(r, source); err != nil {
		return nil, err
	}

	// The C `unique < entries && unique != 0` check (zero is exempt).
	if unique.Compare(IP128{Lo: uint64(n)}) < 0 && unique != (IP128{}) {
		return nil, fmt.Errorf("iprange: %s: unique IPs cannot be less than entries (%d)", source, n)
	}
	if lines < uint64(n) {
		return nil, fmt.Errorf("iprange: %s: lines (%d) cannot be less than entries (%d)", source, lines, n)
	}

	ranges, err := readRecords(r, source, n, 32, func(raw []byte) Range {
		return Range{
			Lo: IP128{Hi: binary.NativeEndian.Uint64(raw[8:16]), Lo: binary.NativeEndian.Uint64(raw[0:8])},
			Hi: IP128{Hi: binary.NativeEndian.Uint64(raw[24:32]), Lo: binary.NativeEndian.Uint64(raw[16:24])},
		}
	})
	if err != nil {
		return nil, err
	}

	if r.remaining() != 0 {
		return nil, fmt.Errorf("iprange: %s: trailing data found after binary payload", source)
	}

	payloadOptimized, err := validatePayloadV2(source, headerOptimized, n, unique, ranges)
	if err != nil {
		return nil, err
	}

	return &IpSet{
		Fam:       V6,
		Ranges:    ranges,
		Entries:   n,
		Lines:     int(lines),
		Unique:    unique,
		Optimized: headerOptimized && payloadOptimized,
	}, nil
}

// writeBinaryHeader writes the text header lines of the released
// binary payloads: header (v2 passes the family line inside it),
// optimized flag, record size, records, bytes, lines, unique ips.
func writeBinaryHeader(w io.Writer, set *IpSet, header string, recordBytes int) error {
	flag := "optimized\n"
	if !set.Optimized {
		flag = "non-optimized\n"
	}
	for _, s := range []string{header, flag} {
		if _, err := io.WriteString(w, s); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "record size %d\n", recordBytes); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "records %d\n", set.Entries); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "bytes %d\n", set.Entries*recordBytes+4); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "lines %d\n", set.Lines); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "unique ips %s\n", decimalU128(set.Unique)); err != nil {
		return err
	}
	return nil
}

// writeBinaryRecords writes the endian marker followed by one
// native-endian record per range, encoded by writeOne.
func writeBinaryRecords(w io.Writer, set *IpSet, writeOne func(io.Writer, Range) error) error {
	var marker [4]byte
	binary.NativeEndian.PutUint32(marker[:], endianMarker)
	if _, err := w.Write(marker[:]); err != nil {
		return err
	}
	for _, r := range set.Ranges {
		if err := writeOne(w, r); err != nil {
			return err
		}
	}
	return nil
}

// writeV1Record encodes one 8-byte native-endian IPv4 record (the
// u128 values of the Go core truncate to the u32 width of the
// format, like the Rust `as u32`).
func writeV1Record(w io.Writer, r Range) error {
	var rec [8]byte
	binary.NativeEndian.PutUint32(rec[0:4], uint32(r.Lo.Lo))
	binary.NativeEndian.PutUint32(rec[4:8], uint32(r.Hi.Lo))
	_, err := w.Write(rec[:])
	return err
}

// writeV2Record encodes one 32-byte native-endian IPv6 record: each
// u128 uses the C uint128_t layout (lo64, then hi64).
func writeV2Record(w io.Writer, r Range) error {
	var rec [32]byte
	binary.NativeEndian.PutUint64(rec[0:8], r.Lo.Lo)
	binary.NativeEndian.PutUint64(rec[8:16], r.Lo.Hi)
	binary.NativeEndian.PutUint64(rec[16:24], r.Hi.Lo)
	binary.NativeEndian.PutUint64(rec[24:32], r.Hi.Hi)
	_, err := w.Write(rec[:])
	return err
}

// WriteV1 serializes an IPv4 set as the released v1.0 binary payload
// (header lines, marker, native-endian records). An empty set writes
// nothing (`test -s file` semantics of the C saver). The caller
// guarantees the set is an IPv4 set and, like the C printer,
// optimized before printing.
func WriteV1(w io.Writer, set *IpSet) error {
	if set.Entries == 0 {
		return nil
	}
	if err := writeBinaryHeader(w, set, binaryHeaderV10, 8); err != nil {
		return err
	}
	return writeBinaryRecords(w, set, writeV1Record)
}

// WriteV2 serializes an IPv6 set as the released v2.0 binary
// payload. An empty set writes nothing. The caller guarantees the
// set is an IPv6 set and, like the C printer, optimized before
// printing.
func WriteV2(w io.Writer, set *IpSet) error {
	if set.Entries == 0 {
		return nil
	}
	if err := writeBinaryHeader(w, set, binaryHeaderV20+"ipv6\n", 32); err != nil {
		return err
	}
	return writeBinaryRecords(w, set, writeV2Record)
}
