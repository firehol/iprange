package format

import (
	"encoding/binary"
	"hash/crc32"
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
func MetaCRC32C(page []byte) uint32 {
	crc := crc32.Checksum(page[:252], crc32cTable)
	crc = crc32.Update(crc, crc32cTable, []byte{0, 0, 0, 0})
	crc = crc32.Update(crc, crc32cTable, page[256:])
	return crc
}
