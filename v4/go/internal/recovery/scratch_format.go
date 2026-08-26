package recovery

// Authorized recovery scratch header and name codecs (Rust
// recovery/scratch/format.rs + artifact_name.rs). Each scratch
// artifact is a fixed 128-byte ownership header followed by mapped
// payload bytes; the header binds the artifact to its database
// generation (database id, transaction id, commit nonce), its
// attempt identity and ordinal, and the creator-only security
// commitment, and seals the facts with the zeroed-field CRC-32C.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

const (
	// scratchHeaderSize is the complete ownership header extent (Rust
	// HEADER_SIZE).
	scratchHeaderSize = 128

	// scratchOwnerValidation and scratchOwnerRecovery are the header
	// owner kinds (Rust OWNER_VALIDATION = 1, OWNER_RECOVERY = 2).
	scratchOwnerValidation = 1
	scratchOwnerRecovery   = 2

	scratchMagicOffset              = 0
	scratchVersionOffset            = 8
	scratchHeaderSizeOffset         = 10
	scratchOwnerKindOffset          = 12
	scratchDatabaseIDOffset         = 16
	scratchTransactionIDOffset      = 32
	scratchCommitNonceOffset        = 40
	scratchAttemptIDOffset          = 56
	scratchOrdinalOffset            = 72
	scratchSecurityKindOffset       = 76
	scratchSecurityCommitmentOffset = 80
	scratchSecurityCommitmentEnd    = 112
	scratchHeaderCRCOffset          = 124
	scratchHeaderCRCSize            = 4
)

// scratchMagic is the exact header magic (Rust MAGIC).
var scratchMagic = [8]byte{'I', 'P', 'R', '4', 'S', 'C', 'R', '1'}

// scratchVersion is the header format version (Rust VERSION = 1).
const scratchVersion = 1

// scratchDecodedHeader is the validated ownership header of one
// scratch artifact (Rust DecodedHeader).
type scratchDecodedHeader struct {
	ownerKind          uint16
	attemptID          [16]byte
	ordinal            uint32
	securityKind       uint16
	securityCommitment [32]byte
}

// scratchHeader builds the fixed ownership header of one scratch
// artifact (Rust scratch::format::header): the source generation
// facts, the attempt and ordinal, and the creator-only commitment,
// sealed with the zeroed-field CRC-32C.
func scratchHeader(source format.Meta, attempt [16]byte, ordinal uint32, commitment [32]byte) [scratchHeaderSize]byte {
	var bytes [scratchHeaderSize]byte
	copy(bytes[scratchMagicOffset:scratchVersionOffset], scratchMagic[:])
	format.PutU16(bytes[scratchVersionOffset:], scratchVersion)
	format.PutU16(bytes[scratchHeaderSizeOffset:], scratchHeaderSize)
	format.PutU16(bytes[scratchOwnerKindOffset:], scratchOwnerRecovery)
	copy(bytes[scratchDatabaseIDOffset:scratchTransactionIDOffset], source.DatabaseID[:])
	format.PutU64(bytes[scratchTransactionIDOffset:], source.TxnID)
	copy(bytes[scratchCommitNonceOffset:scratchAttemptIDOffset], source.CommitNonce[:])
	copy(bytes[scratchAttemptIDOffset:scratchOrdinalOffset], attempt[:])
	format.PutU32(bytes[scratchOrdinalOffset:], ordinal)
	format.PutU16(bytes[scratchSecurityKindOffset:], scratchCreationSecurityKind())
	copy(bytes[scratchSecurityCommitmentOffset:scratchSecurityCommitmentEnd], commitment[:])
	checksum, ok := format.CRC32CWithZeroed(bytes[:], scratchHeaderCRCOffset, scratchHeaderCRCSize)
	if !ok {
		panic("fixed scratch header CRC range")
	}
	format.PutU32(bytes[scratchHeaderCRCOffset:], checksum)
	return bytes
}

// decodeScratchHeader validates and decodes one fixed ownership
// header (Rust scratch::format::decode_header): the fixed fields, the
// reserved zero ranges, the nonzero attempt and commitment, and the
// header CRC must all hold.
func decodeScratchHeader(bytes *[scratchHeaderSize]byte) (scratchDecodedHeader, bool) {
	ownerKind := format.U16(bytes[scratchOwnerKindOffset:])
	var attemptID [16]byte
	copy(attemptID[:], bytes[scratchAttemptIDOffset:scratchOrdinalOffset])
	var commitment [32]byte
	copy(commitment[:], bytes[scratchSecurityCommitmentOffset:scratchSecurityCommitmentEnd])
	valid := fixedHeaderValid(bytes, ownerKind) &&
		reservedHeaderValid(bytes) &&
		attemptID != [16]byte{} &&
		commitment != [32]byte{} &&
		headerCRCValid(bytes)
	if !valid {
		return scratchDecodedHeader{}, false
	}
	return scratchDecodedHeader{
		ownerKind:          ownerKind,
		attemptID:          attemptID,
		ordinal:            format.U32(bytes[scratchOrdinalOffset:]),
		securityKind:       format.U16(bytes[scratchSecurityKindOffset:]),
		securityCommitment: commitment,
	}, true
}

// fixedHeaderValid checks the exact fixed header field values (Rust
// fixed_header_valid): magic, version, header size, owner class, and
// the platform creator-only security kind.
func fixedHeaderValid(bytes *[scratchHeaderSize]byte, ownerKind uint16) bool {
	return string(bytes[scratchMagicOffset:scratchVersionOffset]) == string(scratchMagic[:]) &&
		format.U16(bytes[scratchVersionOffset:]) == scratchVersion &&
		format.U16(bytes[scratchHeaderSizeOffset:]) == scratchHeaderSize &&
		(ownerKind == scratchOwnerValidation || ownerKind == scratchOwnerRecovery) &&
		format.U16(bytes[scratchSecurityKindOffset:]) == scratchCreationSecurityKind()
}

// reservedHeaderValid checks the three reserved header ranges stay
// zero (Rust reserved_header_valid).
func reservedHeaderValid(bytes *[scratchHeaderSize]byte) bool {
	return allZero(bytes[scratchOwnerKindOffset+2:scratchDatabaseIDOffset]) &&
		allZero(bytes[scratchSecurityKindOffset+2:scratchSecurityCommitmentOffset]) &&
		allZero(bytes[scratchSecurityCommitmentEnd:scratchHeaderCRCOffset])
}

// headerCRCValid recomputes the zeroed-field CRC over the header
// (Rust header_crc_valid).
func headerCRCValid(bytes *[scratchHeaderSize]byte) bool {
	checksum, ok := format.CRC32CWithZeroed(bytes[:], scratchHeaderCRCOffset, scratchHeaderCRCSize)
	return ok && checksum == format.U32(bytes[scratchHeaderCRCOffset:])
}

// allZero reports whether every byte of one fixed range is zero.
func allZero(bytes []byte) bool {
	for _, b := range bytes {
		if b != 0 {
			return false
		}
	}
	return true
}

// Scratch name grammar (Rust artifact_name.rs): ".iprange-scratch-"
// + 32 lowercase hex attempt characters + "-" + 8 lowercase hex
// ordinal characters + ".tmp" = 62 bytes.
const (
	scratchPrefix     = ".iprange-scratch-"
	scratchSuffix     = ".tmp"
	scratchNameLength = 62
)

// scratchNameOf builds the exact 62-byte basename of one scratch
// artifact and proves it binds under the platform name encoding (Rust
// scratch_name + Name::new; a fixed grammar name is always valid).
func scratchNameOf(attempt [16]byte, ordinal uint32) (string, error) {
	bytes := encodeScratchName(attempt, ordinal)
	if err := live.ValidateEncodingBinding(scratchBasenameEncoding(), bytes); err != nil {
		return "", &format.Error{Code: format.CodeInvalidArgument, Detail: "invalid recovery scratch name"}
	}
	return string(bytes), nil
}

// encodeScratchName builds the exact fixed scratch basename bytes
// (Rust artifact_name::scratch_name): the 17-byte prefix, the
// 32-hex attempt, the separator, the 8-hex ordinal, and the 4-byte
// suffix. The field offsets derive from the prefix length so the
// encoder and decoder agree by construction.
func encodeScratchName(attempt [16]byte, ordinal uint32) []byte {
	out := make([]byte, scratchNameLength)
	copy(out, scratchPrefix)
	for i, b := range attempt {
		out[len(scratchPrefix)+i*2] = hexNibble(b >> 4)
		out[len(scratchPrefix)+i*2+1] = hexNibble(b & 0x0f)
	}
	out[len(scratchPrefix)+32] = '-'
	for index := 0; index < 8; index++ {
		shift := uint(28 - index*4)
		out[len(scratchPrefix)+33+index] = hexNibble(byte((ordinal >> shift) & 0x0f))
	}
	copy(out[len(scratchPrefix)+41:], scratchSuffix)
	return out
}

// decodeScratchName parses and exact-checks one scratch basename
// (Rust artifact_name::decode_scratch_name): the prefix, separator,
// suffix, and the lowercase hex attempt and ordinal fields.
func decodeScratchName(bytes []byte) ([16]byte, uint32, bool) {
	if len(bytes) != scratchNameLength {
		return [16]byte{}, 0, false
	}
	if string(bytes[:len(scratchPrefix)]) != scratchPrefix ||
		bytes[len(scratchPrefix)+32] != '-' ||
		string(bytes[len(scratchPrefix)+33+8:]) != scratchSuffix {
		return [16]byte{}, 0, false
	}
	attempt, ok := decodeHexAttempt(bytes[len(scratchPrefix) : len(scratchPrefix)+32])
	if !ok {
		return [16]byte{}, 0, false
	}
	ordinal, ok := decodeHexOrdinal(bytes[len(scratchPrefix)+33 : len(scratchPrefix)+33+8])
	if !ok {
		return [16]byte{}, 0, false
	}
	return attempt, ordinal, true
}

// hexNibble encodes one nibble to its lowercase hex digit (Rust
// encode_nibble).
func hexNibble(value byte) byte {
	if value < 10 {
		return '0' + value
	}
	return 'a' + value - 10
}

// decodeHexNibble decodes one lowercase hex digit (Rust
// decode_nibble).
func decodeHexNibble(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	}
	return 0, false
}

// decodeHexAttempt decodes the 32-character attempt field (Rust
// decode_attempt).
func decodeHexAttempt(bytes []byte) ([16]byte, bool) {
	if len(bytes) != 32 {
		return [16]byte{}, false
	}
	var attempt [16]byte
	for i := range attempt {
		high, ok := decodeHexNibble(bytes[i*2])
		if !ok {
			return [16]byte{}, false
		}
		low, ok := decodeHexNibble(bytes[i*2+1])
		if !ok {
			return [16]byte{}, false
		}
		attempt[i] = high<<4 | low
	}
	return attempt, true
}

// decodeHexOrdinal decodes the 8-character ordinal field (Rust
// decode_ordinal).
func decodeHexOrdinal(bytes []byte) (uint32, bool) {
	if len(bytes) != 8 {
		return 0, false
	}
	var value uint32
	for _, b := range bytes {
		nibble, ok := decodeHexNibble(b)
		if !ok {
			return 0, false
		}
		value = value<<4 | uint32(nibble)
	}
	return value, true
}
