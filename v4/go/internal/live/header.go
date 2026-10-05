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
	policyGenOff    = 20
	policyOff       = 22
	policyTailOff   = 23
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

// policy is the create-flag record (Rust live_sidecar Policy).
// Legacy is a sidecar written before the flag existed. Unprotected
// skips the creator-only proof even when umask produced mode 0600.
type policy uint8

const (
	policyLegacy policy = iota
	policyUnprotected
	policyProtected
)

// header is the decoded sidecar header (Rust live_sidecar Header).
type header struct {
	capacity   uint32
	databaseID [16]byte
	sidecarID  [16]byte
	policy     policy
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
	encodePolicy(page, h.policy)
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
	decoded, err := decodePolicy(page)
	if err != nil {
		return 0, header{}, err
	}
	h.policy = decoded
	if h.databaseID == [16]byte{} || h.sidecarID == [16]byte{} {
		return 0, header{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "reader table identity is invalid"}
	}
	return state, h, nil
}

// SidecarHeader is the decoded reader-table header of one
// coordination file (Rust live_sidecar Header): the database and
// sidecar identity words.
type SidecarHeader struct {
	DatabaseID [16]byte
	SidecarID  [16]byte
}

// ReadHeader reads and verifies the reader-table header page of one
// coordination file (Rust live_sidecar::read_header): shaped,
// checksummed, valid state, non-zero identities. The exported form
// feeds the publication residue coordination barrier.
func ReadHeader(f *os.File) (SidecarHeader, error) {
	st, err := f.Stat()
	if err != nil {
		return SidecarHeader{}, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if st.Size() < int64(sidecarPageSize) {
		return SidecarHeader{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "reader table header is invalid"}
	}
	m, err := mapping.MapFile(f, sidecarPageSize, false)
	if err != nil {
		return SidecarHeader{}, err
	}
	defer m.Close()
	page, err := m.Page(0)
	if err != nil {
		return SidecarHeader{}, err
	}
	_, h, err := readHeaderMapping(page)
	if err != nil {
		return SidecarHeader{}, err
	}
	return SidecarHeader{DatabaseID: h.databaseID, SidecarID: h.sidecarID}, nil
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
	if !policySpanCanonical(page) {
		return false
	}
	return allZero(page, headerSize, sidecarPageSize-headerSize)
}

func creatorPolicy(creatorOnly bool) policy {
	if creatorOnly {
		return policyProtected
	}
	return policyUnprotected
}

func encodePolicy(page []byte, value policy) {
	switch value {
	case policyUnprotected:
		format.PutU16(page[policyGenOff:], 1)
		page[policyOff] = 0
	case policyProtected:
		format.PutU16(page[policyGenOff:], 1)
		page[policyOff] = 1
	default:
		format.PutU16(page[policyGenOff:], 0)
		page[policyOff] = 0
	}
}

func decodePolicy(page []byte) (policy, error) {
	generation := format.U16(page[policyGenOff:])
	switch {
	case generation == 0 && page[policyOff] == 0:
		return policyLegacy, nil
	case generation == 1 && page[policyOff] == 0:
		return policyUnprotected, nil
	case generation == 1 && page[policyOff] == 1:
		return policyProtected, nil
	default:
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "reader table protection policy is invalid"}
	}
}

func policySpanCanonical(page []byte) bool {
	if !allZero(page, policyTailOff, databaseIDOff-policyTailOff) {
		return false
	}
	_, err := decodePolicy(page)
	return err == nil
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
