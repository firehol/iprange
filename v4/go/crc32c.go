package iprangedb

import (
	"encoding/binary"
	"hash/crc32"
)

// CRC32C (Castagnoli) — the per-page corruption checksum (D9).
//
// Parameters (D9): reflected polynomial 0x82F63B78, init = 0xFFFFFFFF, refin = refout =
// true, xorout = 0xFFFFFFFF — the iSCSI/Intel CRC32C. Test vector:
// crc32c("123456789") == 0xE3069283. The Go stdlib crc32.Castagnoli table implements the
// identical reflected algorithm (Update applies the init/xorout), so it is bit-for-bit
// compatible with the Rust software table.

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// checksumZeroField is the 8-byte zero stand-in for the checksum field when a
// page CRC is computed. Hoisted to package scope (rather than a per-call local)
// so it does NOT escape to the heap on every pageChecksum call — verifyPage is
// called once per page during open-time validation (ValidateScopeCRC / data
// validation), and a stack-local [8]byte passed to crc32.Update escaped, costing
// one heap allocation per page and making open-time heap scale with page count
// (Rule 1 violation). Read-only by convention; never mutated.
var checksumZeroField [8]byte

// crc32c returns the CRC32C/Castagnoli of b (full init + xorout, D9).
func crc32c(b []byte) uint32 {
	return crc32.Update(0, castagnoli, b)
}

// pageChecksum returns the D9 page checksum value to store: CRC32C over all pageSize
// bytes with the 8-byte checksum field ([8, 16)) taken as zero, in the low 4 bytes of a
// u64 (the high 4 bytes are 0). page MUST be exactly pageSize bytes.
func pageChecksum(page []byte) uint64 {
	crc := crc32.Update(0, castagnoli, page[:PHChecksum])     // [0, 8)
	crc = crc32.Update(crc, castagnoli, checksumZeroField[:]) // checksum field as zero
	crc = crc32.Update(crc, castagnoli, page[PHChecksum+8:])  // [16, pageSize)
	return uint64(crc)                                        // high 32 bits zero by construction
}

// verifyPage reports whether a page matches its stored checksum (D9), enforcing the
// high-32-bits-zero rule: a reader MUST reject a non-zero high half. page MUST be exactly
// pageSize bytes.
func verifyPage(page []byte) bool {
	stored := binary.LittleEndian.Uint64(page[PHChecksum:])
	if stored>>32 != 0 {
		return false // high 4 bytes MUST be zero (D9)
	}
	return pageChecksum(page) == stored
}
