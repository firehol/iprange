package recovery

import "encoding/binary"

// AsUint64 reads one 8-byte slice as a little-endian uint64 (the Go
// address-fence limb reader of the recovery report).
func asUint64(bytes []byte) uint64 {
	return binary.LittleEndian.Uint64(bytes)
}
