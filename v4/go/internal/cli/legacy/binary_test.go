package legacy

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
)

func binarySet4(ranges [][2]uint32) *IpSet {
	s := NewIpSet(V4)
	for _, r := range ranges {
		s.AddRange(Range{Lo: IP128{Lo: uint64(r[0])}, Hi: IP128{Lo: uint64(r[1])}})
	}
	s.Optimize()
	return s
}

func binarySet6(ranges [][2]IP128) *IpSet {
	s := NewIpSet(V6)
	for _, r := range ranges {
		s.AddRange(Range{Lo: r[0], Hi: r[1]})
	}
	s.Optimize()
	return s
}

// binaryU128 builds a u128 value from its 64-bit halves.
func binaryU128(hi, lo uint64) IP128 { return IP128{Hi: hi, Lo: lo} }

func neU32(v uint32) []byte {
	b := make([]byte, 4)
	binary.NativeEndian.PutUint32(b, v)
	return b
}

func neU128(hi, lo uint64) []byte {
	b := make([]byte, 16)
	binary.NativeEndian.PutUint64(b[0:8], lo)
	binary.NativeEndian.PutUint64(b[8:16], hi)
	return b
}

// binaryPayloadV1 assembles a v1 payload with the given header values
// and raw record bytes (the marker is appended automatically).
func binaryPayloadV1(optimized, recordSize, records, nbytes, lines, unique string, marker, recordsBytes []byte) []byte {
	var v bytes.Buffer
	v.WriteString("iprange binary format v1.0\n")
	v.WriteString(optimized)
	v.WriteByte('\n')
	fmt.Fprintf(&v, "record size %s\n", recordSize)
	fmt.Fprintf(&v, "records %s\n", records)
	fmt.Fprintf(&v, "bytes %s\n", nbytes)
	fmt.Fprintf(&v, "lines %s\n", lines)
	fmt.Fprintf(&v, "unique ips %s\n", unique)
	v.Write(marker)
	v.Write(recordsBytes)
	return v.Bytes()
}

// binaryPayloadV2 assembles a v2 payload (family line included).
func binaryPayloadV2(family, optimized, recordSize, records, nbytes, lines, unique string, recordsBytes []byte) []byte {
	var v bytes.Buffer
	v.WriteString("iprange binary format v2.0\n")
	v.WriteString(family)
	v.WriteByte('\n')
	v.WriteString(optimized)
	v.WriteByte('\n')
	fmt.Fprintf(&v, "record size %s\n", recordSize)
	fmt.Fprintf(&v, "records %s\n", records)
	fmt.Fprintf(&v, "bytes %s\n", nbytes)
	fmt.Fprintf(&v, "lines %s\n", lines)
	fmt.Fprintf(&v, "unique ips %s\n", unique)
	v.Write(neU32(endianMarker))
	v.Write(recordsBytes)
	return v.Bytes()
}

var endianMarkerBytes = neU32(endianMarker)
var wrongMarkerBytes = neU32(0xDEADBEEF)

// wantErr asserts that err is non-nil and carries the exact C
// diagnostic text.
func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %q, got nil", want)
	}
	if err.Error() != want {
		t.Fatalf("error mismatch:\n got %q\nwant %q", err.Error(), want)
	}
}

// loadErr adapts a (*IpSet, error) load result to its error so a
// load call can be used inline with wantErr (Go only allows a
// multi-valued call as a whole argument list).
func loadErr(got *IpSet, err error) error { return err }

func TestBinaryV1WriteMatchesOracleBytes(t *testing.T) {
	s := binarySet4([][2]uint32{{0xac106301, 0xac106301}})
	s.Lines = 1
	var buf bytes.Buffer
	if err := WriteV1(&buf, s); err != nil {
		t.Fatal(err)
	}
	var expected bytes.Buffer
	expected.WriteString("iprange binary format v1.0\noptimized\nrecord size 8\n")
	expected.WriteString("records 1\nbytes 12\nlines 1\nunique ips 1\n")
	expected.Write(neU32(0x1A2B3C4D))
	expected.Write(neU32(0xac106301))
	expected.Write(neU32(0xac106301))
	if !bytes.Equal(buf.Bytes(), expected.Bytes()) {
		t.Fatalf("payload mismatch:\n got %x\nwant %x", buf.Bytes(), expected.Bytes())
	}
}

func TestBinaryV2WriteMatchesOracleBytes(t *testing.T) {
	s := binarySet6([][2]IP128{{
		binaryU128(0x20010DB800000000, 0x0000000000000001),
		binaryU128(0x20010DB800000000, 0x0000000000000001),
	}})
	s.Lines = 1
	var buf bytes.Buffer
	if err := WriteV2(&buf, s); err != nil {
		t.Fatal(err)
	}
	var expected bytes.Buffer
	expected.WriteString("iprange binary format v2.0\nipv6\noptimized\nrecord size 32\n")
	expected.WriteString("records 1\nbytes 36\nlines 1\nunique ips 1\n")
	expected.Write(neU32(0x1A2B3C4D))
	expected.Write(neU128(0x20010DB800000000, 1))
	expected.Write(neU128(0x20010DB800000000, 1))
	if !bytes.Equal(buf.Bytes(), expected.Bytes()) {
		t.Fatalf("payload mismatch:\n got %x\nwant %x", buf.Bytes(), expected.Bytes())
	}
}

func TestBinaryV1WriteLoadRoundtrip(t *testing.T) {
	s := binarySet4([][2]uint32{{0x0a000001, 0x0a000001}, {0x0a000008, 0x0a00000b}})
	s.Lines = 2
	if s.Unique.Lo != 5 {
		t.Fatalf("unique = %d, want 5", s.Unique.Lo)
	}
	var buf bytes.Buffer
	if err := WriteV1(&buf, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadV1(buf.Bytes(), "rt.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Ranges, s.Ranges) {
		t.Fatalf("ranges differ: %+v vs %+v", loaded.Ranges, s.Ranges)
	}
	if loaded.Entries != 2 || loaded.Lines != 2 || loaded.Unique.Lo != 5 || !loaded.Optimized {
		t.Fatalf("loaded set differs: %+v", loaded)
	}
}

func TestBinaryV2WriteLoadRoundtripWithMappedV6(t *testing.T) {
	// Mapped IPv4 1.2.3.4 plus a 2^96 /32 block.
	s := binarySet6([][2]IP128{
		{
			binaryU128(0, 0x0000FFFF01020304),
			binaryU128(0, 0x0000FFFF01020304),
		},
		{
			binaryU128(0x20010DB800000000, 0),
			binaryU128(0x20010DB8FFFFFFFF, 0xFFFFFFFFFFFFFFFF),
		},
	})
	s.Lines = 2
	big := binaryU128(0x100000000, 0) // 2^96
	if s.Unique != addU128(big, one128) {
		t.Fatalf("unique = %s, want 1 + 2^96", formatU128(s.Unique.Hi, s.Unique.Lo))
	}
	var buf bytes.Buffer
	if err := WriteV2(&buf, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadV2(buf.Bytes(), "rt6.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Ranges, s.Ranges) {
		t.Fatalf("ranges differ: %+v vs %+v", loaded.Ranges, s.Ranges)
	}
	if loaded.Entries != 2 || loaded.Lines != 2 || loaded.Unique != addU128(big, one128) || !loaded.Optimized {
		t.Fatalf("loaded set differs: %+v", loaded)
	}
	// The decimal u128 header round-trips byte-exact: the bytes up to
	// the marker are pure text ending with the unique line.
	expectedText := "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\n" +
		"records 2\nbytes 68\nlines 2\nunique ips 79228162514264337593543950337\n"
	if !bytes.HasPrefix(buf.Bytes(), []byte(expectedText)) {
		t.Fatalf("header text mismatch: got %q", buf.Bytes())
	}
}

func TestBinaryV1EmptySetWritesNothingAndLoads(t *testing.T) {
	set := NewIpSet(V4)
	var buf bytes.Buffer
	if err := WriteV1(&buf, set); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("empty set wrote %d bytes", buf.Len())
	}

	data := binaryPayloadV1("optimized", "8", "0", "4", "0", "0", endianMarkerBytes, nil)
	loaded, err := LoadV1(data, "empty.bin")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Ranges) != 0 || loaded.Entries != 0 || loaded.Unique != (IP128{}) || !loaded.Optimized {
		t.Fatalf("loaded set differs: %+v", loaded)
	}

	// A claimed non-zero unique over zero records is rejected.
	bad := binaryPayloadV1("optimized", "8", "0", "4", "0", "5", endianMarkerBytes, nil)
	wantErr(t, loadErr(LoadV1(bad, "bad.bin")), "iprange: bad.bin: unique IPs (5) do not match the binary payload (0)")
}

func TestBinaryV1LoadRejectsWrongHeaderText(t *testing.T) {
	data := []byte("iprange binary format v9.9\noptimized\nrecord size 8\nrecords 0\nbytes 4\nlines 0\nunique ips 0\n")
	wantErr(t, loadErr(LoadV1(data, "h.bin")), "iprange: h.bin expecting binary header but found 'iprange binary format v9.9\n'.")
	// EOF at the header (no bytes at all).
	wantErr(t, loadErr(LoadV1(nil, "e.bin")), "iprange: e.bin expecting binary header but found ''.")
	// Truncated header without newline.
	wantErr(t, loadErr(LoadV1([]byte("iprange binary format v1.0"), "t.bin")), "iprange: t.bin expecting binary header but found 'iprange binary format v1.0'.")
}

func TestBinaryV1LoadRejectsBadOptimizedLine(t *testing.T) {
	data := []byte("iprange binary format v1.0\nmaybe-optimized\n")
	wantErr(t, loadErr(LoadV1(data, "o.bin")), "iprange: o.bin 2nd line should be the optimized flag, but found 'maybe-optimized\n'.")
}

func TestBinaryV1LoadRejectsBadRecordSize(t *testing.T) {
	data := binaryPayloadV1("optimized", "4", "0", "4", "0", "0", endianMarkerBytes, nil)
	wantErr(t, loadErr(LoadV1(data, "s.bin")), "iprange: s.bin: invalid record size 4 (expected 8)")
}

func TestBinaryV1LoadRejectsHexlikeRecordsField(t *testing.T) {
	// test 57: "records 0x10" must fail like the C strtoull tail.
	data := []byte("iprange binary format v1.0\noptimized\nrecord size 8\nrecords 0x10\nbytes 4\nlines 0\nunique ips 0\n")
	wantErr(t, loadErr(LoadV1(data, "x.bin")), "iprange: x.bin: invalid records value '0x10\n'")
}

func TestBinaryV1LoadRejectsWrongBytes(t *testing.T) {
	data := binaryPayloadV1("optimized", "8", "1", "4", "1", "1", endianMarkerBytes, neU32(1))
	wantErr(t, loadErr(LoadV1(data, "b.bin")), "iprange: b.bin invalid number of bytes, found 4, expected 12.")
}

func TestBinaryV1LoadRejectsMarkerIssues(t *testing.T) {
	// No marker bytes at all.
	data := binaryPayloadV1("optimized", "8", "0", "4", "0", "0", nil, nil)
	wantErr(t, loadErr(LoadV1(data, "m.bin")), "iprange: m.bin: cannot load ipset header")
	// Wrong marker value.
	data = binaryPayloadV1("optimized", "8", "0", "4", "0", "0", wrongMarkerBytes, nil)
	wantErr(t, loadErr(LoadV1(data, "m.bin")), "iprange: m.bin: incompatible endianness")
}

func TestBinaryV1LoadRejectsUniqueBelowEntries(t *testing.T) {
	data := binaryPayloadV1("optimized", "8", "2", "20", "2", "1", endianMarkerBytes,
		concatBytes(neU32(1), neU32(1), neU32(2), neU32(2)))
	wantErr(t, loadErr(LoadV1(data, "u.bin")), "iprange: u.bin: unique IPs (1) cannot be less than entries (2)")
}

func TestBinaryV1LoadRejectsLinesBelowEntries(t *testing.T) {
	data := binaryPayloadV1("optimized", "8", "2", "20", "1", "2", endianMarkerBytes,
		concatBytes(neU32(1), neU32(1), neU32(2), neU32(2)))
	wantErr(t, loadErr(LoadV1(data, "l.bin")), "iprange: l.bin: lines (1) cannot be less than entries (2)")
}

func TestBinaryV1LoadRejectsShortRecords(t *testing.T) {
	data := binaryPayloadV1("optimized", "8", "2", "20", "2", "2", endianMarkerBytes, neU32(1))
	wantErr(t, loadErr(LoadV1(data, "short.bin")), "iprange: short.bin: expected to load 2 entries, loaded 0")
}

func TestBinaryV1LoadRejectsTrailingGarbage(t *testing.T) {
	data := binaryPayloadV1("optimized", "8", "1", "12", "1", "1", endianMarkerBytes,
		concatBytes(neU32(0x01020304), neU32(0x01020304), []byte("JUNK")))
	wantErr(t, loadErr(LoadV1(data, "trail.bin")), "iprange: trail.bin: trailing data found after binary payload")
}

func TestBinaryV1LoadRejectsUniqueMismatch(t *testing.T) {
	// test 59 fake-counts case: unique 999 over one 4.3.2.1 record.
	data := binaryPayloadV1("optimized", "8", "1", "12", "999", "999", endianMarkerBytes,
		concatBytes(neU32(0x04030201), neU32(0x04030201)))
	wantErr(t, loadErr(LoadV1(data, "fake.bin")), "iprange: fake.bin: unique IPs (999) do not match the binary payload (1)")
}

func TestBinaryV1LoadRejectsOptimizedClaimOverOverlapping(t *testing.T) {
	// test 59 duplicate-records case: header optimized, records
	// overlap (second addr <= first broadcast).
	data := binaryPayloadV1("optimized", "8", "2", "20", "2", "2", endianMarkerBytes,
		concatBytes(neU32(0x04030201), neU32(0x04030202), neU32(0x04030202), neU32(0x04030202)))
	wantErr(t, loadErr(LoadV1(data, "dup.bin")), "iprange: dup.bin: binary payload claims to be optimized but contains overlapping, adjacent, or unsorted records")
}

func TestBinaryV1NonOptimizedPayloadRecomputesUnique(t *testing.T) {
	// Overlapping records with a truthful non-optimized header: loads
	// with the flag clear; a later optimize merges them.
	data := binaryPayloadV1("non-optimized", "8", "2", "20", "2", "2", endianMarkerBytes,
		concatBytes(neU32(0x04030201), neU32(0x04030202), neU32(0x04030202), neU32(0x04030202)))
	loaded, err := LoadV1(data, "raw.bin")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Optimized || loaded.Entries != 2 || loaded.Unique.Lo != 2 {
		t.Fatalf("loaded set differs: %+v", loaded)
	}
	loaded.Optimize()
	if !reflect.DeepEqual(loaded.Ranges, []Range{{Lo: IP128{Lo: 0x04030201}, Hi: IP128{Lo: 0x04030202}}}) {
		t.Fatalf("optimized ranges differ: %+v", loaded.Ranges)
	}
	if loaded.Unique.Lo != 2 {
		t.Fatalf("unique = %d, want 2", loaded.Unique.Lo)
	}

	// A lying unique count is still caught for non-optimized v1
	// (unique >= entries so the earlier entries check passes).
	bad := binaryPayloadV1("non-optimized", "8", "2", "20", "2", "3", endianMarkerBytes,
		concatBytes(neU32(0x04030201), neU32(0x04030202), neU32(0x04030202), neU32(0x04030202)))
	wantErr(t, loadErr(LoadV1(bad, "raw.bin")), "iprange: raw.bin: unique IPs (3) do not match the binary payload (2)")
}

func TestBinaryV1AdjacentRecordsWithOptimizedHeaderLoad(t *testing.T) {
	// Adjacency is allowed only with the guarded form: prev hi below
	// MAX and curr lo == prev hi + 1 is NOT optimized disorder (test
	// the detection loop edge).
	data := binaryPayloadV1("optimized", "8", "2", "20", "2", "3", endianMarkerBytes,
		concatBytes(neU32(1), neU32(1), neU32(2), neU32(2)))
	wantErr(t, loadErr(LoadV1(data, "adj.bin")), "iprange: adj.bin: unique IPs (3) do not match the binary payload (2)")
}

func TestBinaryV2LoadRejectsFamilyAndFlagLines(t *testing.T) {
	data := []byte("iprange binary format v2.0\nipv4\n")
	wantErr(t, loadErr(LoadV2(data, "f.bin")), "iprange: f.bin expected family 'ipv6' but found 'ipv4\n'.")
	data = []byte("iprange binary format v2.0\nipv6\noptimize\n")
	wantErr(t, loadErr(LoadV2(data, "f.bin")), "iprange: f.bin expected optimized flag but found 'optimize\n'.")
}

func TestBinaryV2LoadRejectsUniqueMismatchWithoutValues(t *testing.T) {
	recs := concatBytes(neU128(0, 1), neU128(0, 1))
	data := binaryPayloadV2("ipv6", "optimized", "32", "1", "36", "1", "7", recs)
	wantErr(t, loadErr(LoadV2(data, "u6.bin")), "iprange: u6.bin: unique IPs do not match the binary payload")
}

func TestBinaryV2LoadRejectsUniqueOverflow(t *testing.T) {
	// 2^128 cannot be represented in the u128 header field.
	data := []byte("iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 0\nbytes 4\nlines 0\nunique ips 340282366920938463463374607431768211456\n")
	wantErr(t, loadErr(LoadV2(data, "o6.bin")), "iprange: o6.bin: unique ips value overflow")
}

func TestBinaryV2UniqueZeroBelowEntriesIsAllowed(t *testing.T) {
	// The C `unique < entries && unique != 0` — zero is exempt.
	recs := concatBytes(neU128(0, 1), neU128(0, 1), neU128(0, 2), neU128(0, 2))
	data := binaryPayloadV2("ipv6", "non-optimized", "32", "2", "68", "2", "0", recs)
	loaded, err := LoadV2(data, "z6.bin")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Entries != 2 || loaded.Unique != (IP128{}) || loaded.Optimized {
		t.Fatalf("loaded set differs: %+v", loaded)
	}
}

func TestBinaryV2LoadRejectsTrailingAndShort(t *testing.T) {
	recs := concatBytes(neU128(0, 1), neU128(0, 1), []byte("JUNK"))
	data := binaryPayloadV2("ipv6", "optimized", "32", "1", "36", "1", "1", recs)
	wantErr(t, loadErr(LoadV2(data, "t6.bin")), "iprange: t6.bin: trailing data found after binary payload")
	data = binaryPayloadV2("ipv6", "optimized", "32", "2", "68", "2", "2", nil)
	wantErr(t, loadErr(LoadV2(data, "s6.bin")), "iprange: s6.bin: expected to load 2 entries, loaded 0")
}

func TestBinaryV2WriteLoadRoundtripLargeU128Count(t *testing.T) {
	// 2001:db8::/32 covers 2^96 addresses: the decimal u128 header
	// line round-trips through both writers/readers.
	s := binarySet6([][2]IP128{{
		binaryU128(0x20010DB800000000, 0),
		binaryU128(0x20010DB8FFFFFFFF, 0xFFFFFFFFFFFFFFFF),
	}})
	s.Lines = 1
	big := binaryU128(0x100000000, 0) // 2^96
	if s.Unique != big {
		t.Fatalf("unique = %s, want 2^96", formatU128(s.Unique.Hi, s.Unique.Lo))
	}
	var buf bytes.Buffer
	if err := WriteV2(&buf, s); err != nil {
		t.Fatal(err)
	}
	headerLine := "unique ips 79228162514264337593543950336\n"
	if !bytes.Contains(buf.Bytes(), []byte(headerLine)) {
		t.Fatalf("header line missing: %q", buf.String())
	}
	loaded, err := LoadV2(buf.Bytes(), "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Unique != big || loaded.Entries != 1 || !loaded.Optimized {
		t.Fatalf("loaded set differs: %+v", loaded)
	}
}

func TestBinaryWriteV1NonOptimizedFlagRoundtrip(t *testing.T) {
	s := binarySet4([][2]uint32{{1, 2}, {5, 6}})
	s.Optimized = false
	s.Lines = 2
	var buf bytes.Buffer
	if err := WriteV1(&buf, s); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("iprange binary format v1.0\nnon-optimized\n")) {
		t.Fatalf("flag line missing: %q", buf.String())
	}
	loaded, err := LoadV1(buf.Bytes(), "raw.bin")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Optimized || loaded.Entries != 2 || loaded.Unique != s.Unique {
		t.Fatalf("loaded set differs: %+v", loaded)
	}
}

// concatBytes joins record chunks into one byte slice (the Rust test
// helper `[...].concat()`).
func concatBytes(chunks ...[]byte) []byte {
	var out []byte
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}
