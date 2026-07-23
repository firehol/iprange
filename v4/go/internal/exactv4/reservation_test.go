package exactv4

import (
	"errors"
	"math"
	"testing"
)

func testReservationHeader(operation reservationOperation, sequence uint64) reservationHeader {
	live := operation.live()
	replace := operation == reservationReplaceExisting
	reset := operation == reservationResetLiveCoordination
	header := reservationHeader{
		state: reservationPrepared, databaseID: [16]byte{1}, attemptedTxnID: 7, attemptedCommitNonce: [16]byte{2},
		reservationID: [16]byte{3}, identityKind: localIdentityPOSIX,
		reservationIdentity: testPOSIXIdentity(8, 100), operation: operation, outputIdentityKind: localIdentityPOSIX,
		attemptedOutputBytes: 8192, attemptedOutputIdentity: testPOSIXIdentity(8, 101), attemptedOutputSHA512: [64]byte{4},
		basenameEncoding: 1, basenameLen: 7, basenameCommitment: [32]byte{7},
		creationSecurityKind: 1, creationSecurityCommitment: [32]byte{8}, headerSeq: sequence,
	}
	if replace {
		header.recordFlags = flagPreviousDestination
		header.previousDestinationIdentity = testPOSIXIdentity(8, 102)
		header.previousDestinationSHA512 = [64]byte{5}
		header.previousDestinationBytes = 8192
	}
	if live {
		header.readerCapacity = 3
		header.processDomainKind = processDomainLinuxPIDNamespace
		header.processDomainToken = testPOSIXIdentity(9, 10)
	}
	if reset {
		header.recordFlags = flagPriorSidecar
		header.priorCoordinationIdentity = testPOSIXIdentity(8, 103)
		header.priorSidecarID = [16]byte{6}
		header.priorReaderCapacity = 2
	}
	return header
}

func testIncompleteCreate(sequence uint64) reservationHeader {
	header := testReservationHeader(reservationCreateLive, sequence)
	header.databaseID = [16]byte{}
	header.attemptedTxnID = 0
	header.attemptedCommitNonce = [16]byte{}
	header.outputIdentityKind = 0
	header.attemptedOutputBytes = 0
	header.attemptedOutputIdentity = [32]byte{}
	header.attemptedOutputSHA512 = [64]byte{}
	return header
}

func testReservationBlock(header reservationHeader) [PageSize]byte {
	var block [PageSize]byte
	header.encodeInto(&block)
	return block
}

func testReservationImage(left, right reservationHeader) []byte {
	data := make([]byte, headerRegionSize)
	block := testReservationBlock(left)
	copy(data[:PageSize], block[:])
	block = testReservationBlock(right)
	copy(data[PageSize:], block[:])
	return data
}

func testSidecarFromReservation(reservation reservationHeader, sequence uint64) sidecarHeader {
	origin, ok := reservation.operation.sidecarOrigin()
	if !ok {
		panic("non-live reservation")
	}
	return sidecarHeader{
		identityKind: reservation.identityKind, capacity: reservation.readerCapacity, state: sidecarInitializing,
		databaseID: reservation.databaseID, mainIdentity: reservation.attemptedOutputIdentity,
		sidecarIdentity: reservation.reservationIdentity, sidecarID: reservation.reservationID, origin: origin,
		attemptedTxnID: reservation.attemptedTxnID, attemptedCommitNonce: reservation.attemptedCommitNonce,
		attemptedMainBytes: reservation.attemptedOutputBytes, attemptedMainSHA512: reservation.attemptedOutputSHA512,
		processDomainKind: reservation.processDomainKind, processDomainToken: reservation.processDomainToken,
		basenameEncoding: reservation.basenameEncoding, basenameLen: reservation.basenameLen,
		basenameCommitment: reservation.basenameCommitment, creationSecurityKind: reservation.creationSecurityKind,
		creationSecurityCommitment: reservation.creationSecurityCommitment, headerSeq: sequence,
	}
}

func testConversionImage(left reservationHeader, right sidecarHeader) []byte {
	data := make([]byte, headerRegionSize)
	reservationBlock := testReservationBlock(left)
	copy(data[:PageSize], reservationBlock[:])
	var sidecarBlock [PageSize]byte
	right.encodeInto(&sidecarBlock)
	copy(data[PageSize:], sidecarBlock[:])
	return data
}

func testSidecarPairImage(left, right sidecarHeader) []byte {
	data := make([]byte, headerRegionSize)
	var block [PageSize]byte
	left.encodeInto(&block)
	copy(data[:PageSize], block[:])
	right.encodeInto(&block)
	copy(data[PageSize:], block[:])
	return data
}

func requireReservationCode(t *testing.T, err error, want reservationErrorCode) *reservationError {
	t.Helper()
	var got *reservationError
	if !errors.As(err, &got) {
		t.Fatalf("error = %T %v, want reservation error %d", err, err, want)
	}
	if got.code != want {
		t.Fatalf("reservation error = %d, want %d", got.code, want)
	}
	return got
}

func requireConversionCode(t *testing.T, err error, want conversionErrorCode) *conversionError {
	t.Helper()
	var got *conversionError
	if !errors.As(err, &got) {
		t.Fatalf("error = %T %v, want conversion error %d", err, err, want)
	}
	if got.code != want {
		t.Fatalf("conversion error = %d, want %d", got.code, want)
	}
	return got
}

func TestReservationExactOffsetsCRCAndDualSelection(t *testing.T) {
	left := testReservationHeader(reservationFailIfExists, 1)
	right := left
	right.headerSeq = 2
	data := testReservationImage(left, right)
	if string(data[:8]) != reservationMagic || u16(data, 8) != 512 || u16(data, 10) != 1 || u32(data, 12) != 1 ||
		read16(data, 16) != [16]byte{1} || u64(data, 32) != 7 || read16(data, 40) != [16]byte{2} ||
		read16(data, 56) != [16]byte{3} || u16(data, 72) != 1 || anyNonzero(data[74:80]) ||
		read32(data, 80) != left.reservationIdentity || u16(data, 112) != 1 || u16(data, 114) != 1 ||
		u32(data, 116) != 0 || u64(data, 120) != 8192 || read32(data, 128) != left.attemptedOutputIdentity ||
		read64(data, 160) != [64]byte{4} || anyNonzero(data[224:320]) || u32(data, 320) != 0 ||
		anyNonzero(data[324:412]) || u16(data, 412) != 1 || u32(data, 416) != 7 ||
		read32(data, 420) != [32]byte{7} || u64(data, 452) != 0 || u16(data, 460) != 1 ||
		read32(data, 464) != [32]byte{8} || u64(data, 496) != 1 || u32(data, 504) != 0 || anyNonzero(data[512:PageSize]) {
		t.Fatal("reservation header wire offsets do not match the exact format")
	}
	selected, err := selectReservationHeader(data, domainSelectionPolicy{})
	if err != nil || selected != right {
		t.Fatalf("selection = %#v, error %v", selected, err)
	}
	data[PageSize+headerCRCOffset] ^= 1
	selected, err = selectReservationHeader(data, domainSelectionPolicy{})
	if err != nil || selected != left {
		t.Fatalf("torn fallback = %#v, error %v", selected, err)
	}
}

func TestAllReservationKindsAndOptionalFieldRulesFailClosed(t *testing.T) {
	for _, operation := range []reservationOperation{
		reservationFailIfExists, reservationReplaceExisting, reservationCreateLive,
		reservationInitializeLive, reservationResetLiveCoordination,
	} {
		block := testReservationBlock(testReservationHeader(operation, 1))
		if _, problem := decodeReservationHeader(block[:]); problem != 0 {
			t.Fatalf("operation %d problem = %d", operation, problem)
		}
	}
	block := testReservationBlock(testIncompleteCreate(1))
	if _, problem := decodeReservationHeader(block[:]); problem != 0 {
		t.Fatalf("incomplete CreateLive problem = %d", problem)
	}
	resetWithoutReadableSidecar := testReservationHeader(reservationResetLiveCoordination, 1)
	resetWithoutReadableSidecar.recordFlags = 0
	resetWithoutReadableSidecar.priorSidecarID = [16]byte{}
	resetWithoutReadableSidecar.priorReaderCapacity = 0
	block = testReservationBlock(resetWithoutReadableSidecar)
	if _, problem := decodeReservationHeader(block[:]); problem != 0 {
		t.Fatalf("reset without readable prior sidecar problem = %d", problem)
	}

	tests := []struct {
		name string
		base reservationOperation
		edit func(*reservationHeader)
		want reservationHeaderProblem
	}{
		{"invalid state", reservationFailIfExists, func(h *reservationHeader) { h.state = 9 }, reservationHeaderState},
		{"live attempted state", reservationCreateLive, func(h *reservationHeader) { h.state = reservationMainNamespaceAttempted }, reservationHeaderState},
		{"partial database attempt", reservationCreateLive, func(h *reservationHeader) { h.databaseID = [16]byte{} }, reservationHeaderDatabaseAttempt},
		{"reservation id", reservationCreateLive, func(h *reservationHeader) { h.reservationID = [16]byte{} }, reservationHeaderReservationID},
		{"identity kind", reservationCreateLive, func(h *reservationHeader) { h.identityKind = 9 }, reservationHeaderIdentityKind},
		{"identity encoding", reservationCreateLive, func(h *reservationHeader) { h.reservationIdentity[31] = 1 }, reservationHeaderIdentityEncoding},
		{"operation", reservationCreateLive, func(h *reservationHeader) { h.operation = 9 }, reservationHeaderOperation},
		{"unknown flag", reservationCreateLive, func(h *reservationHeader) { h.recordFlags = 4 }, reservationHeaderFlags},
		{"output kind", reservationCreateLive, func(h *reservationHeader) { h.outputIdentityKind = localIdentityWindows }, reservationHeaderAttemptedOutput},
		{"output size", reservationCreateLive, func(h *reservationHeader) { h.attemptedOutputBytes = 8193 }, reservationHeaderAttemptedOutput},
		{"output collision", reservationCreateLive, func(h *reservationHeader) { h.attemptedOutputIdentity = h.reservationIdentity }, reservationHeaderAttemptedOutput},
		{"replace flag", reservationReplaceExisting, func(h *reservationHeader) { h.recordFlags = 0 }, reservationHeaderPreviousDestination},
		{"replace bytes", reservationReplaceExisting, func(h *reservationHeader) { h.previousDestinationBytes = 4096 }, reservationHeaderPreviousDestination},
		{"foreign previous fields", reservationCreateLive, func(h *reservationHeader) { h.previousDestinationIdentity = testPOSIXIdentity(8, 200) }, reservationHeaderPreviousDestination},
		{"live capacity", reservationCreateLive, func(h *reservationHeader) { h.readerCapacity = 0 }, reservationHeaderReaderCapacity},
		{"immutable capacity", reservationFailIfExists, func(h *reservationHeader) { h.readerCapacity = 1 }, reservationHeaderReaderCapacity},
		{"reset prior identity", reservationResetLiveCoordination, func(h *reservationHeader) { h.priorCoordinationIdentity = [32]byte{} }, reservationHeaderPriorCoordination},
		{"reset prior collision", reservationResetLiveCoordination, func(h *reservationHeader) { h.priorCoordinationIdentity = h.attemptedOutputIdentity }, reservationHeaderPriorCoordination},
		{"reset sidecar id", reservationResetLiveCoordination, func(h *reservationHeader) { h.priorSidecarID = [16]byte{} }, reservationHeaderPriorCoordination},
		{"foreign prior fields", reservationCreateLive, func(h *reservationHeader) { h.priorReaderCapacity = 1 }, reservationHeaderPriorCoordination},
		{"live domain padding", reservationInitializeLive, func(h *reservationHeader) { h.processDomainToken[31] = 1 }, reservationHeaderProcessDomain},
		{"immutable domain", reservationFailIfExists, func(h *reservationHeader) { h.processDomainKind = processDomainHostGlobal }, reservationHeaderProcessDomain},
		{"basename kind", reservationCreateLive, func(h *reservationHeader) { h.basenameEncoding = 2 }, reservationHeaderBasename},
		{"basename empty", reservationCreateLive, func(h *reservationHeader) { h.basenameLen = 0 }, reservationHeaderBasename},
		{"security kind", reservationCreateLive, func(h *reservationHeader) { h.creationSecurityKind = 2 }, reservationHeaderCreationSecurity},
		{"sequence", reservationCreateLive, func(h *reservationHeader) { h.headerSeq = 0 }, reservationHeaderSequence},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			header := testReservationHeader(tc.base, 1)
			tc.edit(&header)
			block := testReservationBlock(header)
			if _, got := decodeReservationHeader(block[:]); got != tc.want {
				t.Fatalf("problem = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReservationWindowsCrossFieldRules(t *testing.T) {
	windows := testReservationHeader(reservationInitializeLive, 1)
	windows.identityKind = localIdentityWindows
	windows.outputIdentityKind = localIdentityWindows
	windows.processDomainKind, windows.processDomainToken = processDomainHostGlobal, [32]byte{}
	windows.basenameEncoding, windows.basenameLen, windows.creationSecurityKind = 2, 8, 2
	block := testReservationBlock(windows)
	if _, problem := decodeReservationHeader(block[:]); problem != 0 {
		t.Fatalf("valid Windows reservation problem = %d", problem)
	}
	windows.processDomainKind = processDomainLinuxPIDNamespace
	block = testReservationBlock(windows)
	if _, problem := decodeReservationHeader(block[:]); problem != reservationHeaderProcessDomain {
		t.Fatalf("Windows domain problem = %d", problem)
	}
	windows.processDomainKind = processDomainHostGlobal
	windows.basenameLen = 7
	block = testReservationBlock(windows)
	if _, problem := decodeReservationHeader(block[:]); problem != reservationHeaderBasename {
		t.Fatalf("Windows odd basename problem = %d", problem)
	}
}

func TestReservationCreateOutputProgressionAndImmutableAttempt(t *testing.T) {
	older := testIncompleteCreate(1)
	newer := testReservationHeader(reservationCreateLive, 2)
	selected, err := selectReservationHeader(testReservationImage(older, newer), domainSelectionPolicy{})
	if err != nil || selected != newer {
		t.Fatalf("empty->complete selection = %#v, error %v", selected, err)
	}
	changed := newer
	changed.headerSeq = 3
	changed.basenameCommitment[0] ^= 1
	_, err = selectReservationHeader(testReservationImage(newer, changed), domainSelectionPolicy{})
	requireReservationCode(t, err, reservationErrImmutableAttemptMismatch)

	regressed := older
	regressed.headerSeq = 3
	_, err = selectReservationHeader(testReservationImage(newer, regressed), domainSelectionPolicy{})
	requireReservationCode(t, err, reservationErrInvalidTransition)

	changedOutput := newer
	changedOutput.headerSeq = 3
	changedOutput.attemptedOutputSHA512[0] ^= 1
	_, err = selectReservationHeader(testReservationImage(newer, changedOutput), domainSelectionPolicy{})
	requireReservationCode(t, err, reservationErrInvalidTransition)
}

func TestReservationNamespaceAttemptPhasesAreMonotonic(t *testing.T) {
	for _, operation := range []reservationOperation{reservationFailIfExists, reservationReplaceExisting} {
		prepared := testReservationHeader(operation, 1)
		attempted := prepared
		attempted.headerSeq, attempted.state = 2, reservationMainNamespaceAttempted
		selected, err := selectReservationHeader(testReservationImage(prepared, attempted), domainSelectionPolicy{})
		if err != nil || selected != attempted {
			t.Fatalf("operation %d prepared->attempted = %#v, error %v", operation, selected, err)
		}
		regressed := prepared
		regressed.headerSeq = 3
		_, err = selectReservationHeader(testReservationImage(attempted, regressed), domainSelectionPolicy{})
		requireReservationCode(t, err, reservationErrInvalidTransition)
	}
}

func TestReservationSelectionRejectsReservedEqualGappedWrappedAndWrongSize(t *testing.T) {
	left := testReservationHeader(reservationFailIfExists, 1)
	right := left
	right.state = reservationMainNamespaceAttempted
	_, err := selectReservationHeader(testReservationImage(left, right), domainSelectionPolicy{})
	requireReservationCode(t, err, reservationErrEqualSequenceDisagreement)

	block := testReservationBlock(left)
	for _, index := range []int{74, 79, 378, 379, 414, 415, 462, 463, 504, 507, 512, PageSize - 1} {
		corrupt := block
		corrupt[index] = 1
		putU32(corrupt[:], headerCRCOffset, crc32cZeroed(corrupt[:], headerCRCOffset, 4))
		if _, problem := decodeReservationHeader(corrupt[:]); problem != reservationHeaderReserved {
			t.Fatalf("reserved byte %d problem = %d", index, problem)
		}
	}
	corrupt := block
	corrupt[headerCRCOffset] ^= 1
	if _, problem := decodeReservationHeader(corrupt[:]); problem != reservationHeaderChecksum {
		t.Fatalf("checksum problem = %d", problem)
	}

	for _, sequence := range []uint64{3, math.MaxUint64} {
		right = left
		right.headerSeq = sequence
		_, err = selectReservationHeader(testReservationImage(left, right), domainSelectionPolicy{})
		got := requireReservationCode(t, err, reservationErrNonAdjacentSequence)
		if got.older != 1 || got.newer != sequence {
			t.Fatalf("sequence error = %d -> %d", got.older, got.newer)
		}
	}
	short := testReservationImage(left, left)
	_, err = selectReservationHeader(short[:len(short)-1], domainSelectionPolicy{})
	got := requireReservationCode(t, err, reservationErrWrongFileSize)
	if got.actual != 8191 {
		t.Fatalf("actual size = %d", got.actual)
	}
}

func TestMixedConversionRequiresAdjacentMatchingInitializingSidecar(t *testing.T) {
	for _, operation := range []reservationOperation{
		reservationCreateLive, reservationInitializeLive, reservationResetLiveCoordination,
	} {
		reservation := testReservationHeader(operation, 4)
		sidecar := testSidecarFromReservation(reservation, 5)
		selected, err := selectConversionHeader(testConversionImage(reservation, sidecar), domainSelectionPolicy{})
		if err != nil || selected.kind != conversionSidecar || selected.sidecar != sidecar {
			t.Fatalf("operation %d conversion = %#v, error %v", operation, selected, err)
		}
	}

	reservation := testReservationHeader(reservationCreateLive, 4)
	sidecar := testSidecarFromReservation(reservation, 5)
	equalSequenceSidecar := sidecar
	equalSequenceSidecar.headerSeq = reservation.headerSeq
	_, err := selectConversionHeader(testConversionImage(reservation, equalSequenceSidecar), domainSelectionPolicy{})
	requireConversionCode(t, err, conversionErrEqualSequenceDisagreement)
	for _, sequence := range []uint64{6, math.MaxUint64} {
		gap := sidecar
		gap.headerSeq = sequence
		_, err := selectConversionHeader(testConversionImage(reservation, gap), domainSelectionPolicy{})
		requireConversionCode(t, err, conversionErrNonAdjacentSequence)
	}
	olderSidecar := sidecar
	olderSidecar.headerSeq = 3
	_, err = selectConversionHeader(testConversionImage(reservation, olderSidecar), domainSelectionPolicy{})
	requireConversionCode(t, err, conversionErrInvalidTransition)

	for _, edit := range []func(*sidecarHeader){
		func(h *sidecarHeader) { h.state = sidecarReady },
		func(h *sidecarHeader) { h.basenameCommitment[0] ^= 1 },
		func(h *sidecarHeader) { h.capacity++ },
		func(h *sidecarHeader) { h.mainIdentity[0] ^= 1 },
	} {
		mismatch := sidecar
		edit(&mismatch)
		_, err = selectConversionHeader(testConversionImage(reservation, mismatch), domainSelectionPolicy{})
		requireConversionCode(t, err, conversionErrInvalidTransition)
	}

	// A completed sidecar phase can never regress back to reservation magic.
	data := testConversionImage(reservation, sidecar)
	copy(data[:PageSize], data[PageSize:])
	reservation.headerSeq = 6
	block := testReservationBlock(reservation)
	copy(data[PageSize:], block[:])
	_, err = selectConversionHeader(data, domainSelectionPolicy{})
	requireConversionCode(t, err, conversionErrInvalidTransition)
}

func TestResolverPolicyAloneMayReplaceLiveProcessDomain(t *testing.T) {
	older := testReservationHeader(reservationInitializeLive, 1)
	newer := older
	newer.headerSeq = 2
	newer.processDomainToken = testPOSIXIdentity(9, 11)
	policy := domainSelectionPolicy{resolverMayReplace: true, currentKind: processDomainLinuxPIDNamespace, currentToken: newer.processDomainToken}
	_, err := selectReservationHeader(testReservationImage(older, newer), domainSelectionPolicy{})
	requireReservationCode(t, err, reservationErrInvalidTransition)
	selected, err := selectReservationHeader(testReservationImage(older, newer), policy)
	if err != nil || selected != newer {
		t.Fatalf("resolver domain selection = %#v, error %v", selected, err)
	}

	wrongPolicy := policy
	wrongPolicy.currentToken[0] ^= 1
	_, err = selectReservationHeader(testReservationImage(older, newer), wrongPolicy)
	requireReservationCode(t, err, reservationErrInvalidTransition)

	mixedSidecar := testSidecarFromReservation(newer, 2)
	_, err = selectConversionHeader(testConversionImage(older, mixedSidecar), domainSelectionPolicy{})
	requireConversionCode(t, err, conversionErrInvalidTransition)
	converted, err := selectConversionHeader(testConversionImage(older, mixedSidecar), policy)
	if err != nil || converted.kind != conversionSidecar || converted.sidecar != mixedSidecar {
		t.Fatalf("resolver mixed domain selection = %#v, error %v", converted, err)
	}

	oldSidecar := testSidecarFromReservation(older, 3)
	newSidecar := oldSidecar
	newSidecar.headerSeq = 4
	newSidecar.processDomainToken = newer.processDomainToken
	_, err = selectConversionHeader(testSidecarPairImage(oldSidecar, newSidecar), domainSelectionPolicy{})
	requireConversionCode(t, err, conversionErrInvalidTransition)
	converted, err = selectConversionHeader(testSidecarPairImage(oldSidecar, newSidecar), policy)
	if err != nil || converted.sidecar != newSidecar {
		t.Fatalf("resolver sidecar domain selection = %#v, error %v", converted, err)
	}
	oldSidecar.state = sidecarReady
	_, err = selectConversionHeader(testSidecarPairImage(oldSidecar, newSidecar), policy)
	requireConversionCode(t, err, conversionErrInvalidTransition)

	nonLiveOld := testReservationHeader(reservationFailIfExists, 1)
	nonLiveNew := nonLiveOld
	nonLiveNew.headerSeq, nonLiveNew.processDomainKind = 2, processDomainHostGlobal
	selected, err = selectReservationHeader(testReservationImage(nonLiveOld, nonLiveNew), policy)
	if err != nil || selected != nonLiveOld {
		t.Fatalf("invalid newer non-live domain did not fall back to the valid block: %#v, %v", selected, err)
	}
}

func TestMixedSidecarPhasesAreMonotonicForEveryOrigin(t *testing.T) {
	create := testReservationHeader(reservationCreateLive, 1)
	initializing := testSidecarFromReservation(create, 2)
	attempted := initializing
	attempted.headerSeq, attempted.state = 3, sidecarMainNamespaceAttempted
	ready := attempted
	ready.headerSeq, ready.state = 4, sidecarReady
	selected, err := selectConversionHeader(testSidecarPairImage(initializing, attempted), domainSelectionPolicy{})
	if err != nil || selected.sidecar != attempted {
		t.Fatalf("create init->attempted = %#v, error %v", selected, err)
	}
	selected, err = selectConversionHeader(testSidecarPairImage(attempted, ready), domainSelectionPolicy{})
	if err != nil || selected.sidecar != ready {
		t.Fatalf("create attempted->ready = %#v, error %v", selected, err)
	}

	for _, operation := range []reservationOperation{reservationInitializeLive, reservationResetLiveCoordination} {
		reservation := testReservationHeader(operation, 1)
		initializing = testSidecarFromReservation(reservation, 2)
		ready = initializing
		ready.headerSeq, ready.state = 3, sidecarReady
		selected, err = selectConversionHeader(testSidecarPairImage(initializing, ready), domainSelectionPolicy{})
		if err != nil || selected.sidecar != ready {
			t.Fatalf("operation %d init->ready = %#v, error %v", operation, selected, err)
		}
		regressed := ready
		regressed.headerSeq, regressed.state = 4, sidecarInitializing
		_, err = selectConversionHeader(testSidecarPairImage(ready, regressed), domainSelectionPolicy{})
		requireConversionCode(t, err, conversionErrInvalidTransition)
	}
}

func TestConversionSelectorSoleValidBlockAndExactHeaderRegion(t *testing.T) {
	reservation := testReservationHeader(reservationCreateLive, 1)
	sidecar := testSidecarFromReservation(reservation, 2)
	data := testConversionImage(reservation, sidecar)
	data[PageSize+headerCRCOffset] ^= 1
	selected, err := selectConversionHeader(data, domainSelectionPolicy{})
	if err != nil || selected.kind != conversionReservation || selected.reservation != reservation {
		t.Fatalf("sole reservation = %#v, error %v", selected, err)
	}
	extra := append(data, make([]byte, 64)...)
	selected, err = selectConversionHeader(extra, domainSelectionPolicy{})
	if err != nil || selected.kind != conversionReservation {
		t.Fatalf("conversion header with phase bytes = %#v, error %v", selected, err)
	}
	_, err = selectConversionHeader(data[:headerRegionSize-1], domainSelectionPolicy{})
	requireConversionCode(t, err, conversionErrHeaderRegionTooShort)
}
