//go:build windows

// Authenticated Windows housekeeping envelope bytes (Rust
// publication/gc_codec.rs, spec 14.4.1). The envelope is an 8,192-byte
// file of two independently selectable 4,096-byte blocks; each block
// carries one sequence/CRC-sealed 512-byte header followed by the
// source filename in the declared basename encoding. Selection reads
// both blocks and picks the higher sequence of two matching
// authorities, exactly like the sidecar header machine.

package live

import (
	"crypto/sha256"
	"hash"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// gcSha256New returns one SHA-256 digest for a GC commitment.
func gcSha256New() hash.Hash { return sha256.New() }

// gcEnvelopeSize is the exact GC envelope file size (two pages).
const gcEnvelopeSize = 2 * format.PageSize

const (
	gcMagic         = "IPR4GCA1"
	gcRecordSize    = 512
	gcVersion       = 1
	gcHeaderSize    = 512
	gcSourceOffset  = 512
	gcSourceCap     = format.PageSize - gcSourceOffset
	gcCRCOffset     = 508
	gcCRCWindow     = 4
	gcSourceLenOff  = 328
	gcSequenceOff   = 496
	gcKindOff       = 12
	gcEncodingOff   = 14
	gcAttemptOff    = 16
	gcOrdinalOff    = 32
	gcDirKindOff    = 36
	gcArtKindOff    = 38
	gcDirIdentity   = 40
	gcSrcCommitOff  = 72
	gcInertCommit   = 104
	gcPayloadPres   = 136
	gcArtIdentity   = 144
	gcPayloadLenOff = 176
	gcPayloadShaOff = 184
	gcPayloadDbOff  = 248
	gcPayloadTxOff  = 264
	gcPayloadNonce  = 272
	gcSecKindOff    = 288
	gcRoleOff       = 290
	gcSecCommitOff  = 296
)

// gcPayload is the optional exact content evidence of one retired
// artifact (Rust gc_codec::Payload; digest kind 0 keeps every field
// zero, kind 1 carries the exact tuple).
type gcPayload struct {
	byteLength    uint64
	sha512        [64]byte
	databaseID    [16]byte
	transactionID uint64
	commitNonce   [16]byte
}

// gcHeader is one decoded or encoded GC authority record (Rust
// gc_codec::Header).
type gcHeader struct {
	kind                   ArtifactKind
	basenameEncoding       uint16
	attemptID              [16]byte
	ordinal                uint32
	directoryIdentityKind  uint16
	artifactIdentityKind   uint16
	directoryIdentity      [32]byte
	sourceCommitment       [32]byte
	inertCommitment        [32]byte
	artifactIdentity       [32]byte
	payload                *gcPayload
	creationSecurityKind   uint16
	directoryRole          DirectoryRole
	creationSecurityCommit [32]byte
	sourceBasename         []byte
	sequence               uint64
}

// selectError classifies a failed GC envelope selection (Rust
// SelectError).
type selectError uint8

func (e selectError) Error() string {
	switch e {
	case selectWrongSize:
		return "GC envelope has the wrong size"
	case selectNoValidHeader:
		return "GC envelope has no valid header"
	case selectHeaderDisagreement:
		return "GC envelope headers disagree"
	}
	return "GC envelope selection failed"
}

const (
	selectWrongSize selectError = iota
	selectNoValidHeader
	selectHeaderDisagreement
)

// gcEncode writes one sequence/CRC-sealed header block (Rust
// Header::encode): the block is zeroed, fixed fields land at their
// specification offsets, the source basename fills bytes [512,512+n),
// and the CRC covers the whole block with its field zeroed. The block
// must be exactly one page.
func (h *gcHeader) gcEncode(block []byte) error {
	if len(block) != format.PageSize {
		return errGCBlockSize()
	}
	if len(h.sourceBasename) == 0 || len(h.sourceBasename) > gcSourceCap {
		return errGCBlockSize()
	}
	clear(block)
	copy(block[0:8], gcMagic)
	format.PutU16(block[8:10], gcRecordSize)
	format.PutU16(block[10:12], gcVersion)
	format.PutU16(block[gcKindOff:], gcKindCode(h.kind))
	format.PutU16(block[gcEncodingOff:], h.basenameEncoding)
	copy(block[gcAttemptOff:], h.attemptID[:])
	format.PutU32(block[gcOrdinalOff:], h.ordinal)
	format.PutU16(block[gcDirKindOff:], h.directoryIdentityKind)
	format.PutU16(block[gcArtKindOff:], h.artifactIdentityKind)
	copy(block[gcDirIdentity:], h.directoryIdentity[:])
	copy(block[gcSrcCommitOff:], h.sourceCommitment[:])
	copy(block[gcInertCommit:], h.inertCommitment[:])
	copy(block[gcArtIdentity:], h.artifactIdentity[:])
	if h.payload != nil {
		format.PutU16(block[gcPayloadPres:], 1)
		format.PutU64(block[gcPayloadLenOff:], h.payload.byteLength)
		copy(block[gcPayloadShaOff:], h.payload.sha512[:])
		copy(block[gcPayloadDbOff:], h.payload.databaseID[:])
		format.PutU64(block[gcPayloadTxOff:], h.payload.transactionID)
		copy(block[gcPayloadNonce:], h.payload.commitNonce[:])
	}
	format.PutU16(block[gcSecKindOff:], h.creationSecurityKind)
	format.PutU16(block[gcRoleOff:], gcDirectoryRoleCode(h.directoryRole))
	copy(block[gcSecCommitOff:], h.creationSecurityCommit[:])
	format.PutU32(block[gcSourceLenOff:], uint32(len(h.sourceBasename)))
	format.PutU64(block[gcSequenceOff:], h.sequence)
	copy(block[gcSourceOffset:], h.sourceBasename)
	checksum, ok := format.CRC32CWithZeroed(block, gcCRCOffset, gcCRCWindow)
	if !ok {
		return errGCBlockSize()
	}
	format.PutU32(block[gcCRCOffset:], checksum)
	return nil
}

// gcSelect reads the higher-sequence header of two matching blocks
// (Rust gc_codec::select): both blocks must decode, or exactly one may;
// two valid blocks must carry the same authority (sequence ignored).
func gcSelect(bytes []byte) (*gcHeader, error) {
	if len(bytes) != gcEnvelopeSize {
		return nil, selectError(selectWrongSize)
	}
	left, leftOK := gcDecodeBlock(bytes[0:format.PageSize])
	right, rightOK := gcDecodeBlock(bytes[format.PageSize:])
	switch {
	case !leftOK && !rightOK:
		return nil, selectError(selectNoValidHeader)
	case leftOK && !rightOK:
		return left, nil
	case !leftOK && rightOK:
		return right, nil
	default:
		if !gcSameAuthority(left, right) {
			return nil, selectError(selectHeaderDisagreement)
		}
		if left.sequence >= right.sequence {
			return left, nil
		}
		return right, nil
	}
}

// gcSourceCommitment hashes the exact source component (Rust
// gc_codec::source_commitment: SHA-256("IPR4GCAUTH" || encoding:u16le
// || length:u32le || name)).
func gcSourceCommitment(encoding uint16, name []byte) [32]byte {
	return gcNameDomainCommitment("IPR4GCAUTH", encoding, name)
}

// gcInertCommitment hashes the exact inert component (Rust
// gc_codec::inert_commitment: SHA-256("IPR4GCNAME" || ...)).
func gcInertCommitment(encoding uint16, name []byte) [32]byte {
	return gcNameDomainCommitment("IPR4GCNAME", encoding, name)
}

// gcNameDomainCommitment computes one GC name commitment (Rust
// name_commitment helper). The length is the raw byte length of the
// encoded name.
func gcNameDomainCommitment(domain string, encoding uint16, name []byte) [32]byte {
	length := uint32(len(name))
	h := gcSha256New()
	h.Write([]byte(domain))
	var encoded [2]byte
	var encodedLen [4]byte
	format.PutU16(encoded[:], encoding)
	format.PutU32(encodedLen[:], length)
	h.Write(encoded[:])
	h.Write(encodedLen[:])
	h.Write(name)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// gcDecodeBlock decodes one header block (Rust Header decode): all
// fixed fields, reserved zero ranges, checksum, and semantic
// validity must hold.
func gcDecodeBlock(block []byte) (*gcHeader, bool) {
	if len(block) != format.PageSize {
		return nil, false
	}
	if !gcFixedValid(block) || !gcReservedZero(block) || !gcChecksumValid(block) {
		return nil, false
	}
	kind, ok := gcDecodeKind(format.U16(block[gcKindOff:]))
	if !ok {
		return nil, false
	}
	payload, ok := gcDecodePayload(block)
	if !ok {
		return nil, false
	}
	source, ok := gcDecodeSource(block)
	if !ok {
		return nil, false
	}
	role, ok := gcDecodeDirectoryRole(format.U16(block[gcRoleOff:]))
	if !ok {
		return nil, false
	}
	header := &gcHeader{
		kind:                   kind,
		basenameEncoding:       format.U16(block[gcEncodingOff:]),
		attemptID:              gcArray16(block[gcAttemptOff:]),
		ordinal:                format.U32(block[gcOrdinalOff:]),
		directoryIdentityKind:  format.U16(block[gcDirKindOff:]),
		artifactIdentityKind:   format.U16(block[gcArtKindOff:]),
		directoryIdentity:      gcArray32(block[gcDirIdentity:]),
		sourceCommitment:       gcArray32(block[gcSrcCommitOff:]),
		inertCommitment:        gcArray32(block[gcInertCommit:]),
		artifactIdentity:       gcArray32(block[gcArtIdentity:]),
		payload:                payload,
		creationSecurityKind:   format.U16(block[gcSecKindOff:]),
		directoryRole:          role,
		creationSecurityCommit: gcArray32(block[gcSecCommitOff:]),
		sourceBasename:         source,
		sequence:               format.U64(block[gcSequenceOff:]),
	}
	if !gcValid(header) {
		return nil, false
	}
	return header, true
}

// gcFixedValid proves the fixed magic/record-size/version/encoding
// fields (Rust fixed_valid).
func gcFixedValid(block []byte) bool {
	return string(block[0:8]) == gcMagic &&
		format.U16(block[8:10]) == gcRecordSize &&
		format.U16(block[10:12]) == gcVersion &&
		gcBasenameEncodingKnown(format.U16(block[gcEncodingOff:]))
}

// gcReservedZero proves every reserved range stays zero (Rust
// reserved_zero: payload 138..144, security 292..296, header tail
// 332..496, pre-CRC 504..508).
func gcReservedZero(block []byte) bool {
	return gcAllZero(block, 138, 6) &&
		gcAllZero(block, 292, 4) &&
		gcAllZero(block, 332, 164) &&
		gcAllZero(block, 504, 4)
}

// gcChecksumValid proves the block CRC with its field zeroed (Rust
// checksum_valid).
func gcChecksumValid(block []byte) bool {
	checksum, ok := format.CRC32CWithZeroed(block, gcCRCOffset, gcCRCWindow)
	if !ok {
		return false
	}
	return checksum == format.U32(block[gcCRCOffset:])
}

// gcDecodePayload decodes the optional payload identity (Rust
// decode_payload): kind 0 requires every payload field zero, kind 1
// carries the exact tuple, any other kind is invalid.
func gcDecodePayload(block []byte) (*gcPayload, bool) {
	switch format.U16(block[gcPayloadPres:]) {
	case 0:
		if !gcAllZero(block, gcPayloadLenOff, gcSecKindOff-gcPayloadLenOff) {
			return nil, false
		}
		return nil, true
	case 1:
		return &gcPayload{
			byteLength:    format.U64(block[gcPayloadLenOff:]),
			sha512:        gcArray64(block[gcPayloadShaOff:]),
			databaseID:    gcArray16(block[gcPayloadDbOff:]),
			transactionID: format.U64(block[gcPayloadTxOff:]),
			commitNonce:   gcArray16(block[gcPayloadNonce:]),
		}, true
	}
	return nil, false
}

// gcDecodeSource decodes the stored source basename (Rust
// decode_source_basename): nonzero length within the block, and a
// zero tail after the name.
func gcDecodeSource(block []byte) ([]byte, bool) {
	length := int(format.U32(block[gcSourceLenOff:]))
	if length == 0 || length > gcSourceCap {
		return nil, false
	}
	end := gcSourceOffset + length
	if !gcAllZero(block, end, format.PageSize-end) {
		return nil, false
	}
	name := make([]byte, length)
	copy(name, block[gcSourceOffset:end])
	return name, true
}

// gcValid proves the semantic record validity (Rust valid): nonzero
// attempt, known kinds, nonzero identities and security commitment,
// bindable source name, matching source commitment, nonzero sequence,
// and a coherent payload tuple.
func gcValid(header *gcHeader) bool {
	if header.attemptID == [16]byte{} || header.sequence == 0 {
		return false
	}
	if !gcKindKnown(header.kind) ||
		header.directoryIdentityKind != 1 && header.directoryIdentityKind != 2 ||
		header.artifactIdentityKind != 1 && header.artifactIdentityKind != 2 ||
		header.creationSecurityKind != 1 && header.creationSecurityKind != 2 {
		return false
	}
	if header.directoryIdentity == [32]byte{} || header.artifactIdentity == [32]byte{} ||
		header.creationSecurityCommit == [32]byte{} {
		return false
	}
	if !gcBasenameEncodingKnown(header.basenameEncoding) {
		return false
	}
	if _, err := BasenameCommitment(BasenameEncoding(header.basenameEncoding), header.sourceBasename); err != nil {
		return false
	}
	if gcSourceCommitment(header.basenameEncoding, header.sourceBasename) != header.sourceCommitment {
		return false
	}
	return gcPayloadValid(header.payload)
}

// gcPayloadValid proves the payload tuple coherence (Rust valid
// payload arm): either every field is zero, or the tuple is complete.
func gcPayloadValid(payload *gcPayload) bool {
	if payload == nil {
		return true
	}
	if payload.byteLength == 0 || payload.sha512 == [64]byte{} {
		return false
	}
	if payload.databaseID == [16]byte{} && payload.transactionID == 0 && payload.commitNonce == [16]byte{} {
		return true
	}
	return payload.databaseID != [16]byte{} && payload.transactionID != 0 && payload.commitNonce != [16]byte{}
}

// gcKindCode maps an artifact kind to its specification number (Rust
// kind_code).
func gcKindCode(kind ArtifactKind) uint16 {
	switch kind {
	case ArtifactPrivateOutput:
		return 1
	case ArtifactPrivateReservation:
		return 2
	case ArtifactOwnedCoordination:
		return 3
	case ArtifactAuthorizedScratch:
		return 4
	case ArtifactOwnedMain:
		return 5
	}
	return 0
}

// gcDecodeKind maps a specification number back to an artifact kind
// (Rust decode_kind).
func gcDecodeKind(value uint16) (ArtifactKind, bool) {
	switch value {
	case 1:
		return ArtifactPrivateOutput, true
	case 2:
		return ArtifactPrivateReservation, true
	case 3:
		return ArtifactOwnedCoordination, true
	case 4:
		return ArtifactAuthorizedScratch, true
	case 5:
		return ArtifactOwnedMain, true
	}
	return 0, false
}

// gcKindKnown reports whether one artifact kind can be encoded.
func gcKindKnown(kind ArtifactKind) bool {
	_, ok := gcDecodeKind(gcKindCode(kind))
	return ok
}

// gcDirectoryRoleCode maps a directory role to its specification
// number (Rust directory_role_code).
func gcDirectoryRoleCode(role DirectoryRole) uint16 {
	switch role {
	case DirectoryRoleDestination:
		return 1
	case DirectoryRoleScratchDirectory:
		return 2
	case DirectoryRoleMainFile:
		return 3
	}
	return 0
}

// gcDecodeDirectoryRole maps a specification number back to a
// directory role (Rust decode_directory_role).
func gcDecodeDirectoryRole(value uint16) (DirectoryRole, bool) {
	switch value {
	case 1:
		return DirectoryRoleDestination, true
	case 2:
		return DirectoryRoleScratchDirectory, true
	case 3:
		return DirectoryRoleMainFile, true
	}
	return 0, false
}

// gcBasenameEncodingKnown reports whether one basename encoding tag is
// one of the two supported encodings (Rust basename_encoding).
func gcBasenameEncodingKnown(value uint16) bool {
	return value == 1 || value == 2
}

// gcSameAuthority compares two headers ignoring the sequence field
// (Rust same_authority).
func gcSameAuthority(left, right *gcHeader) bool {
	l := *left
	r := *right
	l.sequence = 1
	r.sequence = 1
	return gcHeaderEqual(&l, &r)
}

// gcHeaderEqual compares two headers field by field.
func gcHeaderEqual(a, b *gcHeader) bool {
	if a.kind != b.kind || a.basenameEncoding != b.basenameEncoding ||
		a.attemptID != b.attemptID || a.ordinal != b.ordinal ||
		a.directoryIdentityKind != b.directoryIdentityKind ||
		a.artifactIdentityKind != b.artifactIdentityKind ||
		a.directoryIdentity != b.directoryIdentity ||
		a.sourceCommitment != b.sourceCommitment ||
		a.inertCommitment != b.inertCommitment ||
		a.artifactIdentity != b.artifactIdentity ||
		a.creationSecurityKind != b.creationSecurityKind ||
		a.directoryRole != b.directoryRole ||
		a.creationSecurityCommit != b.creationSecurityCommit ||
		a.sequence != b.sequence ||
		string(a.sourceBasename) != string(b.sourceBasename) {
		return false
	}
	return gcPayloadEqual(a.payload, b.payload)
}

// gcPayloadEqual compares two optional payloads.
func gcPayloadEqual(a, b *gcPayload) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.byteLength == b.byteLength && a.sha512 == b.sha512 &&
		a.databaseID == b.databaseID && a.transactionID == b.transactionID &&
		a.commitNonce == b.commitNonce
}

// gcAllZero reports whether a byte range is all zero.
func gcAllZero(block []byte, offset, length int) bool {
	for _, b := range block[offset : offset+length] {
		if b != 0 {
			return false
		}
	}
	return true
}

func gcArray16(b []byte) [16]byte {
	var out [16]byte
	copy(out[:], b[:16])
	return out
}

func gcArray32(b []byte) [32]byte {
	var out [32]byte
	copy(out[:], b[:32])
	return out
}

func gcArray64(b []byte) [64]byte {
	var out [64]byte
	copy(out[:], b[:64])
	return out
}

// errGCBlockSize is the internal fixed-size violation of the codec.
func errGCBlockSize() error {
	return nsInvalidNameError()
}
