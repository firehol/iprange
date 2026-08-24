// Exact dual-block namespace-publication reservation (Rust
// publication/reservation.rs). The reservation file is two mapped
// pages; every 512-byte record is decoded straight from the mapped
// view with the fixed offsets below and no intermediate copy.
// Production code never copies a page or record into owned memory.

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Fixed reservation constants (Rust reservation.rs; the identity and
// commitment kinds of publication/namespace/unix.rs). Windows refuses
// publication opens, so the Go codec only ever sees the unix kinds.
const (
	reservationMagic           = "IPR4RSV1" // [8]byte magic at offset 0
	reservationRecordSize      = 512
	reservationVersion         = 1
	reservationFileSize        = 2 * format.PageSize
	reservationOperationLock   = 0
	reservationPreviousPresent = uint32(1)

	basenameEncodingKind = 1
	creationSecurityKind = 1
)

// Reservation offsets (Rust reservation.rs offset table).
const (
	reservationMagicOffset                = 0
	reservationRecordSizeOffset           = 8
	reservationVersionOffset              = 10
	reservationStateOffset                = 12
	reservationDatabaseIDOffset           = 16
	reservationTransactionIDOffset        = 32
	reservationCommitNonceOffset          = 40
	reservationAttemptIDOffset            = 56
	reservationIdentityKindOffset         = 72
	reservationIdentityOffset             = 80
	reservationPolicyOffset               = 112
	reservationOutputIdentityKindOffset   = 114
	reservationPreviousFlagsOffset        = 116
	reservationOutputLengthOffset         = 120
	reservationOutputIdentityOffset       = 128
	reservationOutputSHA512Offset         = 160
	reservationPreviousIdentityOffset     = 224
	reservationPreviousSHA512Offset       = 256
	reservationBasenameEncodingOffset     = 412
	reservationBasenameLengthOffset       = 416
	reservationBasenameCommitmentOffset   = 420
	reservationPreviousLengthOffset       = 452
	reservationCreationSecurityKindOffset = 460
	reservationSecurityCommitmentOffset   = 464
	reservationSequenceOffset             = 496
	reservationCRCOffset                  = 508
	reservationCRCSize                    = 4
)

// reservationState is the wire state discriminant (Rust State).
type reservationState uint32

const (
	reservationStatePrepared                 reservationState = 1
	reservationStateMainMayHaveBeenAttempted reservationState = 2
)

func decodeReservationState(value uint32) (reservationState, bool) {
	switch value {
	case 1:
		return reservationStatePrepared, true
	case 2:
		return reservationStateMainMayHaveBeenAttempted, true
	}
	return 0, false
}

// reservationPolicy is the wire policy discriminant (Rust Policy).
type reservationPolicy uint16

const (
	reservationPolicyFailIfExists              reservationPolicy = 1
	reservationPolicyReplaceExisting           reservationPolicy = 2
	reservationPolicyReplaceExistingNoRollback reservationPolicy = 3
)

func decodeReservationPolicy(value uint16) (reservationPolicy, bool) {
	switch value {
	case 1:
		return reservationPolicyFailIfExists, true
	case 2:
		return reservationPolicyReplaceExisting, true
	case 3:
		return reservationPolicyReplaceExistingNoRollback, true
	}
	return 0, false
}

// reservationPolicyOf maps one published policy onto its
// reservation-wire peer (the three Rust Policy discriminants; an
// invalid published policy cannot be named by the exported surface
// and is refused defensively).
func reservationPolicyOf(policy PublicationPolicy) (reservationPolicy, bool) {
	switch policy {
	case PolicyFailIfExists:
		return reservationPolicyFailIfExists, true
	case PolicyReplaceExisting:
		return reservationPolicyReplaceExisting, true
	case PolicyReplaceExistingNoRollback:
		return reservationPolicyReplaceExistingNoRollback, true
	default:
		return 0, false
	}
}

func (p reservationPolicy) isReplacement() bool {
	return p == reservationPolicyReplaceExisting || p == reservationPolicyReplaceExistingNoRollback
}

// reservationPrevious is the exact previous-destination evidence of a
// replacement policy (Rust Previous).
type reservationPrevious struct {
	identity   [32]byte
	byteLength uint64
	sha512     [64]byte
}

// reservationHeader is one decoded 512-byte reservation record (Rust
// Header). Previous is carried as a value plus presence flag so the
// decode path stays allocation-free.
type reservationHeader struct {
	state               reservationState
	databaseID          [16]byte
	transactionID       uint64
	commitNonce         [16]byte
	attemptID           [16]byte
	reservationIdentity [32]byte
	policy              reservationPolicy
	outputByteLength    uint64
	outputIdentity      [32]byte
	outputSHA512        [64]byte
	previous            reservationPrevious
	previousPresent     bool
	basenameLen         uint32
	basenameCommitment  [32]byte
	securityCommitment  [32]byte
	sequence            uint64
}

// state2 derives the second-block header of the Prepared,1 record
// (Rust Header::state2: MainMayHaveBeenAttempted, sequence 2; None
// unless the record is exactly Prepared,1).
func (h reservationHeader) state2() (reservationHeader, bool) {
	if h.state != reservationStatePrepared || h.sequence != 1 {
		return reservationHeader{}, false
	}
	next := h
	next.state = reservationStateMainMayHaveBeenAttempted
	next.sequence = 2
	return next, true
}

// attemptEq compares the attempt identity of two records with state
// and sequence normalized (Rust Header::attempt_eq).
func (h reservationHeader) attemptEq(other reservationHeader) bool {
	h.state = reservationStatePrepared
	h.sequence = 1
	other.state = reservationStatePrepared
	other.sequence = 1
	return h == other
}

// selectedReservation is one selectable reservation record and its
// block index (Rust Selected).
type selectedReservation struct {
	header reservationHeader
	block  int
}

// reservationCodecProblem is one decode failure class (Rust
// reservation::Problem).
type reservationCodecProblem uint8

const (
	reservationCodecProblemMagic reservationCodecProblem = iota
	reservationCodecProblemFixed
	reservationCodecProblemReserved
	reservationCodecProblemChecksum
	reservationCodecProblemState
	reservationCodecProblemAttempt
	reservationCodecProblemIdentity
	reservationCodecProblemPolicy
	reservationCodecProblemOutput
	reservationCodecProblemPrevious
	reservationCodecProblemBasename
	reservationCodecProblemSecurity
	reservationCodecProblemSequence
)

// noneCodecProblem is the no-decode-failure marker (Rust decode has no
// Ok/Err payload to carry alongside the header).
const noneCodecProblem = reservationCodecProblem(0xff)

// reservationSelectErrorKind is one select() refusal class (Rust
// SelectError).
type reservationSelectErrorKind uint8

const (
	reservationSelectWrongSize reservationSelectErrorKind = iota
	reservationSelectNoValidHeader
	reservationSelectEqualSequenceDisagreement
	reservationSelectNonAdjacentSequence
	reservationSelectAttemptMismatch
	reservationSelectInvalidTransition
)

// reservationSelectError is one select() refusal with the exact
// per-block decode classes of the NoValidHeader arm (Rust
// SelectError).
type reservationSelectError struct {
	kind   reservationSelectErrorKind
	block0 reservationCodecProblem
	block1 reservationCodecProblem
}

func (e *reservationSelectError) Error() string {
	switch e.kind {
	case reservationSelectWrongSize:
		return "reservation file size mismatch"
	case reservationSelectNoValidHeader:
		return "reservation has no valid header"
	case reservationSelectEqualSequenceDisagreement:
		return "reservation blocks disagree at equal sequence"
	case reservationSelectNonAdjacentSequence:
		return "reservation blocks are not adjacent"
	case reservationSelectAttemptMismatch:
		return "reservation blocks name different attempts"
	case reservationSelectInvalidTransition:
		return "reservation blocks form an invalid transition"
	}
	return "reservation select refused"
}

// selectReservation picks the authoritative record of one complete
// reservation file view (Rust select: exact size, per-block decode,
// single-valid fallback, then the ordered pair rules). bytes must be
// the full mapped reservation view.
func selectReservation(bytes []byte) (selectedReservation, error) {
	if len(bytes) != reservationFileSize {
		return selectedReservation{}, &reservationSelectError{kind: reservationSelectWrongSize}
	}
	left := bytes[0:format.PageSize]
	right := bytes[format.PageSize : 2*format.PageSize]
	leftHeader, leftProblem := decodeReservation(left)
	rightHeader, rightProblem := decodeReservation(right)
	switch {
	case leftProblem != noneCodecProblem && rightProblem != noneCodecProblem:
		return selectedReservation{}, &reservationSelectError{
			kind:   reservationSelectNoValidHeader,
			block0: leftProblem,
			block1: rightProblem,
		}
	case rightProblem != noneCodecProblem:
		return selectedAt(leftHeader, 0)
	case leftProblem != noneCodecProblem:
		return selectedAt(rightHeader, 1)
	default:
		return selectReservationPair(left, right, leftHeader, rightHeader)
	}
}

// containsSelectableHeader reports whether either block of one complete
// reservation file view decodes (Rust contains_selectable_header).
func containsSelectableHeader(bytes []byte) bool {
	if len(bytes) != reservationFileSize {
		return false
	}
	_, p0 := decodeReservation(bytes[0:format.PageSize])
	_, p1 := decodeReservation(bytes[format.PageSize : 2*format.PageSize])
	return p0 == noneCodecProblem || p1 == noneCodecProblem
}

// selectedAt accepts one valid block when its sequence matches the
// block index (Rust selected_at: sequence == block+1).
func selectedAt(header reservationHeader, block int) (selectedReservation, error) {
	if header.sequence != uint64(block)+1 {
		return selectedReservation{}, &reservationSelectError{kind: reservationSelectInvalidTransition}
	}
	return selectedReservation{header: header, block: block}, nil
}

// selectReservationPair resolves two valid blocks (Rust select_pair):
// equal sequences require byte-identical pages; otherwise the newer
// sequence must be adjacent, name the same attempt, and form the exact
// Prepared,1 -> MainMayHaveBeenAttempted,2 transition.
func selectReservationPair(left, right []byte, leftHeader, rightHeader reservationHeader) (selectedReservation, error) {
	if leftHeader.sequence == rightHeader.sequence {
		if reservationBlocksEqual(left, right) {
			return selectedAt(leftHeader, 0)
		}
		return selectedReservation{}, &reservationSelectError{kind: reservationSelectEqualSequenceDisagreement}
	}
	var older, newer reservationHeader
	block := 0
	if leftHeader.sequence < rightHeader.sequence {
		older, newer, block = leftHeader, rightHeader, 1
	} else {
		older, newer, block = rightHeader, leftHeader, 0
	}
	if older.sequence+1 != newer.sequence {
		return selectedReservation{}, &reservationSelectError{kind: reservationSelectNonAdjacentSequence}
	}
	if !older.attemptEq(newer) {
		return selectedReservation{}, &reservationSelectError{kind: reservationSelectAttemptMismatch}
	}
	if !(older.state == reservationStatePrepared && older.sequence == 1 &&
		newer.state == reservationStateMainMayHaveBeenAttempted && newer.sequence == 2) {
		return selectedReservation{}, &reservationSelectError{kind: reservationSelectInvalidTransition}
	}
	return selectedReservation{header: newer, block: block}, nil
}

// reservationBlocksEqual compares two complete page views byte-wise
// (Rust ByteSource::same over PAGE_SIZE).
func reservationBlocksEqual(left, right []byte) bool {
	if len(left) < format.PageSize || len(right) < format.PageSize {
		return false
	}
	for i := 0; i < format.PageSize; i++ {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// encodeReservationHeader writes one record into a full mapped page
// view: the whole page is zeroed (the reserved tail [512, 4096) must
// stay zero), fixed fields are stamped, and the CRC-32C seal covers
// the page with its CRC field treated as zero (Rust Header::encode
// over mapping.page_mut). page must be a full page view.
func (h reservationHeader) encodeReservationHeader(page []byte) error {
	if len(page) != format.PageSize {
		return &format.Error{Code: format.CodeInvalidLength, Detail: "reservation encode requires a full page view"}
	}
	clear(page)
	copy(page[reservationMagicOffset:], reservationMagic)
	format.PutU16(page[reservationRecordSizeOffset:], reservationRecordSize)
	format.PutU16(page[reservationVersionOffset:], reservationVersion)
	format.PutU32(page[reservationStateOffset:], uint32(h.state))
	copy(page[reservationDatabaseIDOffset:], h.databaseID[:])
	format.PutU64(page[reservationTransactionIDOffset:], h.transactionID)
	copy(page[reservationCommitNonceOffset:], h.commitNonce[:])
	copy(page[reservationAttemptIDOffset:], h.attemptID[:])
	format.PutU16(page[reservationIdentityKindOffset:], identityKind)
	copy(page[reservationIdentityOffset:], h.reservationIdentity[:])
	format.PutU16(page[reservationPolicyOffset:], uint16(h.policy))
	format.PutU16(page[reservationOutputIdentityKindOffset:], identityKind)
	format.PutU64(page[reservationOutputLengthOffset:], h.outputByteLength)
	copy(page[reservationOutputIdentityOffset:], h.outputIdentity[:])
	copy(page[reservationOutputSHA512Offset:], h.outputSHA512[:])
	if h.previousPresent {
		format.PutU32(page[reservationPreviousFlagsOffset:], reservationPreviousPresent)
		copy(page[reservationPreviousIdentityOffset:], h.previous.identity[:])
		copy(page[reservationPreviousSHA512Offset:], h.previous.sha512[:])
		format.PutU64(page[reservationPreviousLengthOffset:], h.previous.byteLength)
	}
	format.PutU16(page[reservationBasenameEncodingOffset:], basenameEncodingKind)
	format.PutU32(page[reservationBasenameLengthOffset:], h.basenameLen)
	copy(page[reservationBasenameCommitmentOffset:], h.basenameCommitment[:])
	format.PutU16(page[reservationCreationSecurityKindOffset:], creationSecurityKind)
	copy(page[reservationSecurityCommitmentOffset:], h.securityCommitment[:])
	format.PutU64(page[reservationSequenceOffset:], h.sequence)
	crc, ok := format.CRC32CWithZeroed(page, reservationCRCOffset, reservationCRCSize)
	if !ok {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "reservation CRC field out of range"}
	}
	format.PutU32(page[reservationCRCOffset:], crc)
	return nil
}

// decodeReservation decodes one complete page view (Rust decode:
// fixed fields, reserved zeroes, checksum, then state, policy,
// attempt, identity, output, previous, basename, security, sequence).
// The none marker reports success.
func decodeReservation(block []byte) (reservationHeader, reservationCodecProblem) {
	core, prob := decodeReservationCore(block)
	if prob != noneCodecProblem {
		return reservationHeader{}, prob
	}
	previous, previousPresent, prob := decodeReservationPrevious(block, core.policy,
		core.reservationIdentity, core.output.identity)
	if prob != noneCodecProblem {
		return reservationHeader{}, prob
	}
	if !previousPresent {
		// Absent previous is the Rust Option::None: the payload must
		// not leak into comparisons.
		previous = reservationPrevious{}
	}
	basenameLen, prob := decodeReservationBasenameLen(block)
	if prob != noneCodecProblem {
		return reservationHeader{}, prob
	}
	securityCommitment := decodeReservationSecurity(block)
	if securityCommitment == [32]byte{} {
		return reservationHeader{}, reservationCodecProblemSecurity
	}
	sequence, prob := decodeReservationSequence(block, core.state)
	if prob != noneCodecProblem {
		return reservationHeader{}, prob
	}
	return reservationHeader{
		state:               core.state,
		databaseID:          core.attempt.databaseID,
		transactionID:       core.attempt.transactionID,
		commitNonce:         core.attempt.commitNonce,
		attemptID:           core.attempt.attemptID,
		reservationIdentity: core.reservationIdentity,
		policy:              core.policy,
		outputByteLength:    core.output.byteLength,
		outputIdentity:      core.output.identity,
		outputSHA512:        core.output.sha512,
		previous:            previous,
		previousPresent:     previousPresent,
		basenameLen:         basenameLen,
		basenameCommitment:  reservationArray32(block, reservationBasenameCommitmentOffset),
		securityCommitment:  securityCommitment,
		sequence:            sequence,
	}, noneCodecProblem
}

// decodeReservationCore runs the fixed-field, reserved-zero, checksum,
// state, policy, attempt, identity, and output checks (Rust
// decode_core).
func decodeReservationCore(block []byte) (reservationCore, reservationCodecProblem) {
	if !reservationFixed(block) {
		return reservationCore{}, reservationCodecProblemFixed
	}
	if !reservationZeroes(block) {
		return reservationCore{}, reservationCodecProblemReserved
	}
	if !reservationChecksum(block) {
		return reservationCore{}, reservationCodecProblemChecksum
	}
	state, ok := decodeReservationState(reservationU32(block, reservationStateOffset))
	if !ok {
		return reservationCore{}, reservationCodecProblemState
	}
	policy, ok := decodeReservationPolicy(reservationU16(block, reservationPolicyOffset))
	if !ok {
		return reservationCore{}, reservationCodecProblemPolicy
	}
	attempt, prob := decodeReservationAttempt(block)
	if prob != noneCodecProblem {
		return reservationCore{}, prob
	}
	reservationIdentity := reservationArray32(block, reservationIdentityOffset)
	if !validReservationIdentity(reservationIdentity) {
		return reservationCore{}, reservationCodecProblemIdentity
	}
	output, prob := decodeReservationOutput(block, reservationIdentity)
	if prob != noneCodecProblem {
		return reservationCore{}, prob
	}
	return reservationCore{
		state:               state,
		policy:              policy,
		attempt:             attempt,
		reservationIdentity: reservationIdentity,
		output:              output,
	}, noneCodecProblem
}

// reservationCore is the decoded core record (Rust CoreFields).
type reservationCore struct {
	state               reservationState
	policy              reservationPolicy
	attempt             reservationAttempt
	reservationIdentity [32]byte
	output              reservationOutput
}

// reservationAttempt is the attempt identity fields (Rust
// AttemptFields).
type reservationAttempt struct {
	databaseID    [16]byte
	transactionID uint64
	commitNonce   [16]byte
	attemptID     [16]byte
}

// reservationOutput is the output evidence fields (Rust OutputFields).
type reservationOutput struct {
	byteLength uint64
	identity   [32]byte
	sha512     [64]byte
}

// decodeReservationAttempt decodes and proves the attempt identity
// (Rust decode_attempt: every field nonzero).
func decodeReservationAttempt(block []byte) (reservationAttempt, reservationCodecProblem) {
	attempt := reservationAttempt{
		databaseID:    reservationArray16(block, reservationDatabaseIDOffset),
		transactionID: reservationU64(block, reservationTransactionIDOffset),
		commitNonce:   reservationArray16(block, reservationCommitNonceOffset),
		attemptID:     reservationArray16(block, reservationAttemptIDOffset),
	}
	if attempt.databaseID == [16]byte{} || attempt.transactionID == 0 ||
		attempt.commitNonce == [16]byte{} || attempt.attemptID == [16]byte{} {
		return reservationAttempt{}, reservationCodecProblemAttempt
	}
	return attempt, noneCodecProblem
}

// decodeReservationOutput decodes and proves the output evidence (Rust
// decode_output: valid geometry, valid identity, distinct from the
// reservation identity).
func decodeReservationOutput(block []byte, reservationIdentity [32]byte) (reservationOutput, reservationCodecProblem) {
	output := reservationOutput{
		byteLength: reservationU64(block, reservationOutputLengthOffset),
		identity:   reservationArray32(block, reservationOutputIdentityOffset),
		sha512:     reservationArray64(block, reservationOutputSHA512Offset),
	}
	if !reservationGeometryValid(output.byteLength) ||
		!validReservationIdentity(output.identity) ||
		output.identity == reservationIdentity {
		return reservationOutput{}, reservationCodecProblemOutput
	}
	return output, noneCodecProblem
}

// decodeReservationBasenameLen requires a nonzero basename length (Rust
// decode_basename_len).
func decodeReservationBasenameLen(block []byte) (uint32, reservationCodecProblem) {
	length := reservationU32(block, reservationBasenameLengthOffset)
	if length == 0 {
		return 0, reservationCodecProblemBasename
	}
	return length, noneCodecProblem
}

// decodeReservationSequence requires the exact state/sequence pairing
// (Rust decode_sequence: Prepared,1 or MainMayHaveBeenAttempted,2).
func decodeReservationSequence(block []byte, state reservationState) (uint64, reservationCodecProblem) {
	sequence := reservationU64(block, reservationSequenceOffset)
	switch {
	case state == reservationStatePrepared && sequence == 1:
		return sequence, noneCodecProblem
	case state == reservationStateMainMayHaveBeenAttempted && sequence == 2:
		return sequence, noneCodecProblem
	}
	return 0, reservationCodecProblemSequence
}

// reservationFixed proves the fixed magic, record size, version, and
// kind fields (Rust require_fixed).
func reservationFixed(block []byte) bool {
	return reservationBytes(block, reservationMagicOffset, 8) == reservationMagic &&
		reservationU16(block, reservationRecordSizeOffset) == reservationRecordSize &&
		reservationU16(block, reservationVersionOffset) == reservationVersion &&
		reservationU16(block, reservationIdentityKindOffset) == identityKind &&
		reservationU16(block, reservationOutputIdentityKindOffset) == identityKind &&
		reservationU16(block, reservationBasenameEncodingOffset) == basenameEncodingKind &&
		reservationU16(block, reservationCreationSecurityKindOffset) == creationSecurityKind
}

// reservationZeroes proves every reserved byte range is zero (Rust
// require_zeroes: the six fixed ranges plus the record tail).
func reservationZeroes(block []byte) bool {
	zeroes := [][2]int{
		{reservationIdentityKindOffset + 2, reservationIdentityOffset - reservationIdentityKindOffset - 2},
		{reservationPreviousSHA512Offset + 64, reservationBasenameEncodingOffset - reservationPreviousSHA512Offset - 64},
		{reservationBasenameEncodingOffset + 2, reservationBasenameLengthOffset - reservationBasenameEncodingOffset - 2},
		{reservationCreationSecurityKindOffset + 2, reservationSecurityCommitmentOffset - reservationCreationSecurityKindOffset - 2},
		{reservationSequenceOffset + 8, reservationCRCOffset - reservationSequenceOffset - 8},
		{reservationRecordSize, format.PageSize - reservationRecordSize},
	}
	for _, z := range zeroes {
		if !reservationAllZero(block, z[0], z[1]) {
			return false
		}
	}
	return true
}

// reservationChecksum proves the CRC-32C seal over the page with its
// CRC field treated as zero (Rust require_checksum).
func reservationChecksum(block []byte) bool {
	crc, ok := format.CRC32CWithZeroed(block, reservationCRCOffset, reservationCRCSize)
	return ok && crc == reservationU32(block, reservationCRCOffset)
}

// decodeReservationPrevious decodes the policy-exact previous evidence
// (Rust decode_previous: fail-if-exists requires the absent layout,
// replacement policies require the present layout with distinct
// identities).
func decodeReservationPrevious(block []byte, policy reservationPolicy,
	reservationIdentity, outputIdentity [32]byte) (reservationPrevious, bool, reservationCodecProblem) {
	flags := reservationU32(block, reservationPreviousFlagsOffset)
	identity := reservationArray32(block, reservationPreviousIdentityOffset)
	sha512 := reservationArray64(block, reservationPreviousSHA512Offset)
	byteLength := reservationU64(block, reservationPreviousLengthOffset)
	if !policy.isReplacement() {
		if flags != 0 || identity != [32]byte{} || sha512 != [64]byte{} || byteLength != 0 {
			return reservationPrevious{}, false, reservationCodecProblemPrevious
		}
		return reservationPrevious{}, false, noneCodecProblem
	}
	if flags != reservationPreviousPresent || !validReservationIdentity(identity) ||
		identity == reservationIdentity || identity == outputIdentity {
		return reservationPrevious{}, false, reservationCodecProblemPrevious
	}
	return reservationPrevious{identity: identity, byteLength: byteLength, sha512: sha512}, true, noneCodecProblem
}

// validReservationIdentity proves a nonzero payload with a zero tail
// beyond the device+inode pair (Rust valid_identity).
func validReservationIdentity(identity [32]byte) bool {
	if identity == [32]byte{} {
		return false
	}
	for _, b := range identity[16:] {
		if b != 0 {
			return false
		}
	}
	return true
}

// reservationGeometryValid proves the output byte length is a valid
// database geometry (Rust bootstrap::geometry_valid: at least two
// pages, page-aligned).
func reservationGeometryValid(length uint64) bool {
	return length >= 2*uint64(format.PageSize) && length%uint64(format.PageSize) == 0
}

// decodeReservationSecurity reads the security commitment (Rust
// decode_security; the caller rejects the all-zero commitment).
func decodeReservationSecurity(block []byte) [32]byte {
	return reservationArray32(block, reservationSecurityCommitmentOffset)
}

// reservationBytes compares the block bytes at offset with expected
// (Rust ByteSource::equals).
func reservationBytes(block []byte, at int, length int) string {
	if at < 0 || length < 0 || at+length > len(block) {
		return ""
	}
	return string(block[at : at+length])
}

// reservationAllZero reports whether the block range is all zero (Rust
// ByteSource::all_zero with the contiguous-pointer fast path).
func reservationAllZero(block []byte, at, length int) bool {
	if at < 0 || length < 0 || at+length > len(block) {
		return false
	}
	for _, b := range block[at : at+length] {
		if b != 0 {
			return false
		}
	}
	return true
}

func reservationU16(block []byte, at int) uint16 {
	return format.U16(block[at : at+2])
}

func reservationU32(block []byte, at int) uint32 {
	return format.U32(block[at : at+4])
}

func reservationU64(block []byte, at int) uint64 {
	return format.U64(block[at : at+8])
}

func reservationArray16(block []byte, at int) [16]byte {
	var out [16]byte
	copy(out[:], block[at:at+16])
	return out
}

func reservationArray32(block []byte, at int) [32]byte {
	var out [32]byte
	copy(out[:], block[at:at+32])
	return out
}

func reservationArray64(block []byte, at int) [64]byte {
	var out [64]byte
	copy(out[:], block[at:at+64])
	return out
}
