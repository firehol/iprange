package iprangedb

import "unsafe"

// readLE32 reads a little-endian uint32 from b[off:off+4] without a bounds check.
// Uses unsafe.Add to compute the address without indexing b (which would trigger
// a bounds check even with unsafe.Pointer). The caller MUST guarantee off+4 <= len(b).
func readLE32(b []byte, off int) uint32 {
	return *(*uint32)(unsafe.Add(unsafe.Pointer(&b[0]), off))
}

// readLE64 reads a little-endian uint64 from b[off:off+8] without a bounds check.
func readLE64(b []byte, off int) uint64 {
	return *(*uint64)(unsafe.Add(unsafe.Pointer(&b[0]), off))
}
