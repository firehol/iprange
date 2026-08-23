// Reservation codec tests (Go port of Rust
// publication/reservation_tests.rs plus independent CRC-32C
// known-answer vectors). Encode and decode run over owned test
// buffers; production paths use mapped page views.

package publication

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

func testIdentity(device, inode uint64) [32]byte {
	return localIdentityFromDeviceInode(device, inode).Bytes
}

func fill16(v byte) (out [16]byte) {
	for i := range out {
		out[i] = v
	}
	return out
}

func fill32(v byte) (out [32]byte) {
	for i := range out {
		out[i] = v
	}
	return out
}

func fill64(v byte) (out [64]byte) {
	for i := range out {
		out[i] = v
	}
	return out
}

// testReservationHeader builds one deterministic Prepared,1 record
// (Rust reservation_tests.rs header()).
func testReservationHeader(policy reservationPolicy) reservationHeader {
	return reservationHeader{
		state:               reservationStatePrepared,
		databaseID:          fill16(1),
		transactionID:       7,
		commitNonce:         fill16(2),
		attemptID:           fill16(3),
		reservationIdentity: testIdentity(4, 5),
		policy:              policy,
		outputByteLength:    3 * uint64(format.PageSize),
		outputIdentity:      testIdentity(4, 6),
		outputSHA512:        fill64(7),
		previous: func() reservationPrevious {
			if !policy.isReplacement() {
				return reservationPrevious{}
			}
			return reservationPrevious{identity: testIdentity(4, 8), sha512: fill64(9)}
		}(),
		previousPresent:    policy.isReplacement(),
		basenameLen:        7,
		basenameCommitment: fill32(10),
		securityCommitment: fill32(11),
		sequence:           1,
	}
}

// testReservationFile builds one complete 8192-byte reservation view
// with the tested block(s) encoded.
func testReservationFile(t *testing.T, block0, block1 *reservationHeader) []byte {
	t.Helper()
	file := make([]byte, reservationFileSize)
	if block0 != nil {
		if err := block0.encodeReservationHeader(file[0:format.PageSize]); err != nil {
			t.Fatalf("encode block 0: %v", err)
		}
	}
	if block1 != nil {
		if err := block1.encodeReservationHeader(file[format.PageSize:]); err != nil {
			t.Fatalf("encode block 1: %v", err)
		}
	}
	return file
}

// TestReservationKnownAnswerCRC pins the encode byte-exactness with
// CRC-32C values computed by an independent implementation (reflected
// Castagnoli, whole page, CRC field treated as zero).
func TestReservationKnownAnswerCRC(t *testing.T) {
	tests := []struct {
		policy reservationPolicy
		crc    uint32
	}{
		{reservationPolicyFailIfExists, 0x7bf19b18},
		{reservationPolicyReplaceExisting, 0xa3026650},
		{reservationPolicyReplaceExistingNoRollback, 0xad1e394f},
	}
	for _, tt := range tests {
		page := make([]byte, format.PageSize)
		if err := testReservationHeader(tt.policy).encodeReservationHeader(page); err != nil {
			t.Fatalf("encode: %v", err)
		}
		if got := format.U32(page[reservationCRCOffset:]); got != tt.crc {
			t.Errorf("policy %d crc = %08x, want %08x", tt.policy, got, tt.crc)
		}
	}
	// The state2 record (MainMayHaveBeenAttempted, sequence 2).
	first := testReservationHeader(reservationPolicyFailIfExists)
	second, ok := first.state2()
	if !ok {
		t.Fatal("state2 not derived")
	}
	page := make([]byte, format.PageSize)
	if err := second.encodeReservationHeader(page); err != nil {
		t.Fatalf("encode state2: %v", err)
	}
	if got := format.U32(page[reservationCRCOffset:]); got != 0x955492de {
		t.Errorf("state2 crc = %08x, want 955492de", got)
	}
	// state2 rejection rules (Rust Header::state2 None).
	notPrepared := first
	notPrepared.state = reservationStateMainMayHaveBeenAttempted
	if _, ok := notPrepared.state2(); ok {
		t.Error("state2 accepted non-prepared record")
	}
	notSequence1 := first
	notSequence1.sequence = 2
	if _, ok := notSequence1.state2(); ok {
		t.Error("state2 accepted non-1 sequence")
	}
}

// TestEitherLegitimateSurvivingBlockIsAuthoritative ports the Rust
// either_legitimate_surviving_block_is_authoritative test.
func TestEitherLegitimateSurvivingBlockIsAuthoritative(t *testing.T) {
	prepared := testReservationHeader(reservationPolicyFailIfExists)
	first := testReservationFile(t, &prepared, nil)
	selected, err := selectReservation(first)
	if err != nil {
		t.Fatalf("select first: %v", err)
	}
	if selected.header != prepared || selected.block != 0 {
		t.Errorf("first = %+v block %d", selected.header, selected.block)
	}

	attempted, ok := prepared.state2()
	if !ok {
		t.Fatal("state2 not derived")
	}
	second := testReservationFile(t, nil, &attempted)
	selected, err = selectReservation(second)
	if err != nil {
		t.Fatalf("select second: %v", err)
	}
	if selected.header != attempted || selected.block != 1 {
		t.Errorf("second = %+v block %d", selected.header, selected.block)
	}
}

// TestAdjacentState2IsSelectedAndATornCopyFallsBack ports the Rust
// adjacent_state2_is_selected_and_a_torn_copy_falls_back test.
func TestAdjacentState2IsSelectedAndATornCopyFallsBack(t *testing.T) {
	first := testReservationHeader(reservationPolicyFailIfExists)
	second, ok := first.state2()
	if !ok {
		t.Fatal("state2 not derived")
	}
	file := testReservationFile(t, &first, &second)
	selected, err := selectReservation(file)
	if err != nil {
		t.Fatalf("select pair: %v", err)
	}
	if selected.header != second || selected.block != 1 {
		t.Errorf("pair = %+v block %d", selected.header, selected.block)
	}

	// A torn newer copy (one flipped payload byte) must fall back to
	// the intact prepared block.
	file[format.PageSize+160] ^= 1
	selected, err = selectReservation(file)
	if err != nil {
		t.Fatalf("select torn: %v", err)
	}
	if selected.header != first || selected.block != 0 {
		t.Errorf("torn = %+v block %d", selected.header, selected.block)
	}
}

// TestSelectionRejectsDisagreementGapsAndInvalidTransitions ports the
// Rust selection_rejects_disagreement_gaps_and_invalid_transitions
// test.
func TestSelectionRejectsDisagreementGapsAndInvalidTransitions(t *testing.T) {
	first := testReservationHeader(reservationPolicyFailIfExists)

	// Equal sequences with different payloads disagree.
	different := first
	different.outputSHA512[0] ^= 1
	file := testReservationFile(t, &first, &different)
	if _, err := selectReservation(file); !isSelectError(err, reservationSelectEqualSequenceDisagreement) {
		t.Errorf("disagreement: got %v", err)
	}

	// A gap (sequence 3) makes the newer block undecodable; the
	// intact prepared copy must be selected (Rust: "an invalid newer
	// copy must not hide the intact prepared copy").
	gap := first
	gap.sequence = 3
	file = testReservationFile(t, &first, &gap)
	selected, err := selectReservation(file)
	if err != nil {
		t.Fatalf("gap select: %v", err)
	}
	if selected.header != first || selected.block != 0 {
		t.Errorf("gap = %+v block %d", selected.header, selected.block)
	}

	// Adjacent sequences naming a different attempt disagree.
	changed, _ := first.state2()
	changed.attemptID[0] ^= 1
	file = testReservationFile(t, &first, &changed)
	if _, err := selectReservation(file); !isSelectError(err, reservationSelectAttemptMismatch) {
		t.Errorf("attempt mismatch: got %v", err)
	}

	// A sole state2 block at index 0 is an invalid transition.
	file = make([]byte, reservationFileSize)
	second, _ := first.state2()
	if err := second.encodeReservationHeader(file[0:format.PageSize]); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := selectReservation(file); !isSelectError(err, reservationSelectInvalidTransition) {
		t.Errorf("invalid transition: got %v", err)
	}
}

// TestEveryPolicyHasExactPreviousFields ports the Rust
// every_policy_has_exact_previous_fields test.
func TestEveryPolicyHasExactPreviousFields(t *testing.T) {
	absent := testReservationFile(t, ptr(testReservationHeader(reservationPolicyFailIfExists)), nil)
	selected, err := selectReservation(absent)
	if err != nil {
		t.Fatalf("select absent: %v", err)
	}
	if selected.header.previousPresent {
		t.Error("fail-if-exists invented prior bytes")
	}

	for _, policy := range []reservationPolicy{
		reservationPolicyReplaceExisting,
		reservationPolicyReplaceExistingNoRollback,
	} {
		replacement := testReservationHeader(policy)
		file := testReservationFile(t, &replacement, nil)
		selected, err := selectReservation(file)
		if err != nil {
			t.Fatalf("select replacement %d: %v", policy, err)
		}
		if selected.header != replacement {
			t.Errorf("policy %d: selected %+v, want %+v", policy, selected.header, replacement)
		}

		invalid := append([]byte(nil), file...)
		for i := 116; i < 120; i++ {
			invalid[i] = 0
		}
		rewriteTestCRC(invalid[:format.PageSize])
		_, err = selectReservation(invalid)
		var serr *reservationSelectError
		if !(asSelectError(err, &serr) && serr.kind == reservationSelectNoValidHeader &&
			serr.block0 == reservationCodecProblemPrevious) {
			t.Errorf("policy %d zeroed flags: got %v", policy, err)
		}
	}
}

// TestReservedBytesUnknownKindsAndMalformedOutputFailClosed ports the
// Rust reserved_bytes_unknown_kinds_and_malformed_output_fail_closed
// test.
func TestReservedBytesUnknownKindsAndMalformedOutputFailClosed(t *testing.T) {
	original := testReservationFile(t, ptr(testReservationHeader(reservationPolicyFailIfExists)), nil)
	tests := []struct {
		offset  int
		problem reservationCodecProblem
	}{
		{600, reservationCodecProblemReserved},
		{112, reservationCodecProblemPolicy},
		{114, reservationCodecProblemFixed},
		{128 + 16, reservationCodecProblemOutput},
	}
	for _, tt := range tests {
		file := append([]byte(nil), original...)
		file[tt.offset] ^= 1
		rewriteTestCRC(file[:format.PageSize])
		_, err := selectReservation(file)
		var serr *reservationSelectError
		if !(asSelectError(err, &serr) && serr.kind == reservationSelectNoValidHeader &&
			serr.block0 == tt.problem) {
			t.Errorf("offset %d: got %v, want block0 %v", tt.offset, err, tt.problem)
		}
	}
}

// TestWrongSizeAndCRCCorruptionAreDistinct ports the Rust
// wrong_size_and_crc_corruption_are_distinct test.
func TestWrongSizeAndCRCCorruptionAreDistinct(t *testing.T) {
	if _, err := selectReservation(make([]byte, 1)); !isSelectError(err, reservationSelectWrongSize) {
		t.Errorf("wrong size: got %v", err)
	}

	file := testReservationFile(t, ptr(testReservationHeader(reservationPolicyFailIfExists)), nil)
	file[20] ^= 1
	_, err := selectReservation(file)
	var serr *reservationSelectError
	if !(asSelectError(err, &serr) && serr.kind == reservationSelectNoValidHeader &&
		serr.block0 == reservationCodecProblemChecksum) {
		t.Errorf("crc corruption: got %v", err)
	}
}

// TestEmptyCreationSecurityCommitmentIsRejected ports the Rust
// empty_creation_security_commitment_is_rejected test.
func TestEmptyCreationSecurityCommitmentIsRejected(t *testing.T) {
	file := testReservationFile(t, ptr(testReservationHeader(reservationPolicyFailIfExists)), nil)
	for i := 464; i < 496; i++ {
		file[i] = 0
	}
	rewriteTestCRC(file[:format.PageSize])
	_, err := selectReservation(file)
	var serr *reservationSelectError
	if !(asSelectError(err, &serr) && serr.kind == reservationSelectNoValidHeader &&
		serr.block0 == reservationCodecProblemSecurity) {
		t.Errorf("empty security commitment: got %v", err)
	}
}

// TestContainsSelectableHeader pins contains_selectable_header on both
// outcomes.
func TestContainsSelectableHeader(t *testing.T) {
	file := testReservationFile(t, ptr(testReservationHeader(reservationPolicyFailIfExists)), nil)
	if !containsSelectableHeader(file) {
		t.Error("valid file not selectable")
	}
	if containsSelectableHeader(make([]byte, reservationFileSize)) {
		t.Error("all-zero file reported selectable")
	}
	if containsSelectableHeader(make([]byte, 1)) {
		t.Error("short file reported selectable")
	}
	// A single undecodable block (a gap record fails its sequence
	// check) but a valid other block stays selectable.
	first := testReservationHeader(reservationPolicyFailIfExists)
	gap := first
	gap.sequence = 3
	file2 := testReservationFile(t, &first, &gap)
	if !containsSelectableHeader(file2) {
		t.Error("single valid block not selectable")
	}
}

func ptr(h reservationHeader) *reservationHeader { return &h }

func rewriteTestCRC(block []byte) {
	crc, ok := format.CRC32CWithZeroed(block, reservationCRCOffset, reservationCRCSize)
	if !ok {
		panic("rewrite crc bounds")
	}
	format.PutU32(block[reservationCRCOffset:], crc)
}

func isSelectError(err error, kind reservationSelectErrorKind) bool {
	var serr *reservationSelectError
	return asSelectError(err, &serr) && serr.kind == kind
}

func asSelectError(err error, target **reservationSelectError) bool {
	if err == nil {
		return false
	}
	serr, ok := err.(*reservationSelectError)
	if !ok {
		return false
	}
	*target = serr
	return true
}
