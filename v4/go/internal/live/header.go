// Exact reader-table header codec and file geometry (Rust
// live_sidecar/header.rs, spec section 15.1). The first 4096 bytes of
// the sidecar are one checksummed header page; ordinary open accepts
// only state ready and the exact sidecar length, everything else fails
// closed.

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

const (
	headerMagic     = "IPRDRS4\x00"
	headerSize      = 68
	headerMagicOff  = 0
	headerSizeOff   = 8
	slotSizeOff     = 10
	stateOff        = 12
	capacityOff     = 16
	databaseIDOff   = 32
	sidecarIDOff    = 48
	headerCRCOff    = 64
	headerCRCLen    = 4
	sidecarPageSize = format.PageSize
)

// sidecarState is the header state (Rust live_sidecar State): creating
// during CreateLive/InitializeLive, ready for ordinary open.
type sidecarState uint32

const (
	stateCreating sidecarState = iota
	stateReady
)

// header is the decoded sidecar header (Rust live_sidecar Header).
type header struct {
	capacity   uint32
	databaseID [16]byte
	sidecarID  [16]byte
}

// sidecarLength is the exact sidecar file length: one header page plus
// capacity 16-byte slots (spec 15.1). Overflow refuses.
func sidecarLength(capacity uint32) (uint64, error) {
	// 16-byte slots with a uint32 capacity cannot overflow uint64, and
	// one header page cannot overflow the sum; the compare forms mirror
	// Rust's checked arithmetic without per-call divisions.
	if uint64(capacity) > uint64(^uint64(0))/slotSize {
		return 0, &format.Error{Code: format.CodeInvalidArgument, Detail: "reader table length overflows"}
	}
	return uint64(capacity)*uint64(slotSize) + uint64(sidecarPageSize), nil
}

// writeHeaderMapping encodes the header into the first page of a
// writable mapping with the checksum field zeroed during the CRC
// (Rust write_header_mapping).
func writeHeaderMapping(page []byte, h header, state sidecarState) error {
	if len(page) != sidecarPageSize {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "reader table header page is not one page"}
	}
	clear(page)
	copy(page[headerMagicOff:], headerMagic)
	format.PutU16(page[headerSizeOff:], headerSize)
	format.PutU16(page[slotSizeOff:], slotSize)
	format.PutU32(page[stateOff:], uint32(state))
	format.PutU32(page[capacityOff:], h.capacity)
	copy(page[databaseIDOff:], h.databaseID[:])
	copy(page[sidecarIDOff:], h.sidecarID[:])
	crc, ok := format.CRC32CWithZeroed(page, headerCRCOff, headerCRCLen)
	if !ok {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "reader table checksum field is invalid"}
	}
	format.PutU32(page[headerCRCOff:], crc)
	return nil
}

// readHeaderMapping decodes and verifies the header page of an existing
// mapping (Rust read_header_mapping).
func readHeaderMapping(page []byte) (sidecarState, header, error) {
	if !headerShapeValid(page) || !headerChecksumValid(page) {
		return 0, header{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "reader table header is invalid"}
	}
	state := sidecarState(format.U32(page[stateOff:]))
	if state != stateCreating && state != stateReady {
		return 0, header{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "reader table state is invalid"}
	}
	var h header
	h.capacity = format.U32(page[capacityOff:])
	copy(h.databaseID[:], page[databaseIDOff:databaseIDOff+16])
	copy(h.sidecarID[:], page[sidecarIDOff:sidecarIDOff+16])
	if h.databaseID == [16]byte{} || h.sidecarID == [16]byte{} {
		return 0, header{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "reader table identity is invalid"}
	}
	return state, h, nil
}

// HasSelectableHeader reports whether the file's first page carries a
// shape- and checksum-valid header regardless of state (Rust
// has_selectable_header); used by the offline transition resolvers to
// find a coordination artifact without opening it. The exported form
// is for the publication residue machine.
func HasSelectableHeader(f *os.File) (bool, error) {
	st, err := f.Stat()
	if err != nil {
		return false, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if st.Size() < int64(sidecarPageSize) {
		return false, nil
	}
	m, err := mapping.MapFile(f, sidecarPageSize, false)
	if err != nil {
		return false, err
	}
	defer m.Close()
	page, err := m.Page(0)
	if err != nil {
		return false, err
	}
	return headerShapeValid(page) && headerChecksumValid(page), nil
}

func headerShapeValid(page []byte) bool {
	if len(page) < sidecarPageSize {
		return false
	}
	if string(page[headerMagicOff:headerMagicOff+8]) != headerMagic {
		return false
	}
	if format.U16(page[headerSizeOff:]) != headerSize {
		return false
	}
	if format.U16(page[slotSizeOff:]) != slotSize {
		return false
	}
	state := sidecarState(format.U32(page[stateOff:]))
	if state != stateCreating && state != stateReady {
		return false
	}
	if format.U32(page[capacityOff:]) == 0 {
		return false
	}
	if !allZero(page, capacityOff+4, databaseIDOff-capacityOff-4) {
		return false
	}
	return allZero(page, headerSize, sidecarPageSize-headerSize)
}

func headerChecksumValid(page []byte) bool {
	crc, ok := format.CRC32CWithZeroed(page, headerCRCOff, headerCRCLen)
	return ok && crc == format.U32(page[headerCRCOff:])
}

// allZero reports whether page[off:off+length] is entirely zero.
func allZero(page []byte, off, length int) bool {
	if off < 0 || length < 0 || off+length > len(page) {
		return false
	}
	for _, b := range page[off : off+length] {
		if b != 0 {
			return false
		}
	}
	return true
}
