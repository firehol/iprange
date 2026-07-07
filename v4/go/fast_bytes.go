package iprangedb

import "unsafe"

// readLE32 reads a little-endian uint32 from b[off:off+4] without a bounds check.
// The caller MUST guarantee off+4 <= len(b) (tree structure guarantees this for page
// reads). On little-endian platforms (x86_64, aarch64-LE — the only targets), this is
// a single mov instruction. Replaces binary.LittleEndian.Uint32 which includes a
// redundant bounds check that showed up at 10% of append time in profiling.
func readLE32(b []byte, off int) uint32 {
	return *(*uint32)(unsafe.Pointer(&b[off]))
}

// readLE64 reads a little-endian uint64 from b[off:off+8] without a bounds check.
func readLE64(b []byte, off int) uint64 {
	return *(*uint64)(unsafe.Pointer(&b[off]))
}
