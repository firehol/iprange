package format

import (
	"encoding/binary"
	"hash/crc32"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// Little-endian scalar codecs. On-disk integers are always little-endian;
// there is no native-endian or ABI-layout field anywhere in v4.

func U16(b []byte) uint16       { return binary.LittleEndian.Uint16(b) }
func U32(b []byte) uint32       { return binary.LittleEndian.Uint32(b) }
func U64(b []byte) uint64       { return binary.LittleEndian.Uint64(b) }
func PutU16(b []byte, v uint16) { binary.LittleEndian.PutUint16(b, v) }
func PutU32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }
func PutU64(b []byte, v uint64) { binary.LittleEndian.PutUint64(b, v) }

// U128 reads the unsigned 128-bit value encoded as 16 little-endian bytes
// (numeric network-address order). Returns the numeric high and low limbs.
func U128(b []byte) (hi, lo uint64) {
	lo = binary.LittleEndian.Uint64(b[0:8])
	hi = binary.LittleEndian.Uint64(b[8:16])
	return hi, lo
}

// PutU128 writes one unsigned 128-bit value as 16 little-endian bytes.
func PutU128(b []byte, hi, lo uint64) {
	binary.LittleEndian.PutUint64(b[0:8], lo)
	binary.LittleEndian.PutUint64(b[8:16], hi)
}

// CRC32C is the exact v4 checksum: reflected Castagnoli polynomial
// 0x82f63b78, initial 0xffffffff, final XOR 0xffffffff.
var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

func CRC32C(data []byte) uint32 {
	return crc32.Checksum(data, crc32cTable)
}

// MetaCRC32C computes the exact meta-page checksum: CRC-32C over the full
// 4,096-byte page with bytes [252,256) (the stored checksum field) treated as
// zero (binary-format-v4.md section 4).
// metaCRCPadding is the four zero bytes covering the stored checksum
// field of one meta page (package-level to keep MetaCRC32C allocation
// free).
var metaCRCPadding = [4]byte{}

func MetaCRC32C(page []byte) uint32 {
	crc := crc32.Checksum(page[:252], crc32cTable)
	crc = crc32.Update(crc, crc32cTable, metaCRCPadding[:])
	crc = crc32.Update(crc, crc32cTable, page[256:])
	return crc
}

// Page checksum (non-meta main-file page CRC sealing, binary-format-v4.md
// section 4; Rust page_checksum.rs). The checksum field sits at byte 28 of
// the page header and is excluded from its own CRC.
const (
	PageChecksumOffset = 28
	PageChecksumLength = 4
)

// crc32cZeroes feeds zero bytes through the CRC without allocating, mirroring
// the Rust crc32c_with_zeroed chunked-zero loop.
var crc32cZeroes = [64]byte{}

// CRC32CWithZeroed computes CRC-32C over data with the byte range
// [zeroAt, zeroAt+zeroLen) treated as zero (Rust checksum.rs
// crc32c_with_zeroed). It reports false when the range is invalid or exceeds
// data.
func CRC32CWithZeroed(data []byte, zeroAt, zeroLen int) (uint32, bool) {
	// Overflow-safe bounds check (Rust checksum.rs uses checked
	// arithmetic): zeroAt+zeroLen can wrap past MaxInt and must not
	// turn an invalid range into a slice panic.
	if zeroAt < 0 || zeroLen < 0 || zeroAt > len(data) || zeroLen > len(data)-zeroAt {
		return 0, false
	}
	zeroEnd := zeroAt + zeroLen
	crc := crc32.Checksum(data[:zeroAt], crc32cTable)
	remaining := zeroLen
	for remaining >= len(crc32cZeroes) {
		crc = crc32.Update(crc, crc32cTable, crc32cZeroes[:])
		remaining -= len(crc32cZeroes)
	}
	if remaining > 0 {
		crc = crc32.Update(crc, crc32cTable, crc32cZeroes[:remaining])
	}
	return crc32.Update(crc, crc32cTable, data[zeroEnd:]), true
}

// PageChecksumValid reports whether page carries a valid commit-time CRC-32C
// seal over the whole page with its checksum field treated as zero
// (Rust page_checksum.rs valid). Short or sealed-less pages are invalid.
func PageChecksumValid(page []byte) bool {
	if len(page) < PageChecksumOffset+PageChecksumLength {
		return false
	}
	crc, ok := CRC32CWithZeroed(page, PageChecksumOffset, PageChecksumLength)
	return ok && crc == U32(page[PageChecksumOffset:PageChecksumOffset+PageChecksumLength])
}

// SealPageChecksum zeroes the checksum field, computes the CRC-32C seal over
// the page with that field treated as zero, and writes the seal back
// (Rust page_checksum.rs seal_mapped). page must be a view into the
// file-backed mapping: pages are constructed and sealed at their final
// offsets, never in owned memory.
func SealPageChecksum(page []byte) error {
	if len(page) < PageChecksumOffset+PageChecksumLength {
		return &Error{Code: CodeFormatInvalid, Detail: "page too short for checksum seal"}
	}
	work.PageSealed(1)
	PutU32(page[PageChecksumOffset:], 0)
	crc, ok := CRC32CWithZeroed(page, PageChecksumOffset, PageChecksumLength)
	if !ok {
		return &Error{Code: CodeFormatInvalid, Detail: "page too short for checksum seal"}
	}
	PutU32(page[PageChecksumOffset:], crc)
	work.BytesMoved(8) // Rust seal_mapped: put_u32(0) + put_u32(checksum)
	return nil
}
