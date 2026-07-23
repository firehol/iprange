package exactv4

import (
	"bytes"
	"fmt"
)

const (
	reservationMagic               = "IPR4RSV1"
	reservationVersion      uint16 = 1
	flagPreviousDestination uint32 = 1 << 0
	flagPriorSidecar        uint32 = 1 << 1
)

type reservationState uint32

const (
	reservationPrepared               reservationState = 1
	reservationMainNamespaceAttempted reservationState = 2
)

func reservationStateFromWire(value uint32) (reservationState, bool) {
	state := reservationState(value)
	return state, state == reservationPrepared || state == reservationMainNamespaceAttempted
}

type reservationOperation uint16

const (
	reservationFailIfExists          reservationOperation = 1
	reservationReplaceExisting       reservationOperation = 2
	reservationCreateLive            reservationOperation = 3
	reservationInitializeLive        reservationOperation = 4
	reservationResetLiveCoordination reservationOperation = 5
)

func reservationOperationFromWire(value uint16) (reservationOperation, bool) {
	operation := reservationOperation(value)
	return operation, operation >= reservationFailIfExists && operation <= reservationResetLiveCoordination
}

func (operation reservationOperation) live() bool {
	return operation >= reservationCreateLive && operation <= reservationResetLiveCoordination
}

func (operation reservationOperation) sidecarOrigin() (sidecarOrigin, bool) {
	switch operation {
	case reservationCreateLive:
		return sidecarOriginCreateLive, true
	case reservationInitializeLive:
		return sidecarOriginInitializeLive, true
	case reservationResetLiveCoordination:
		return sidecarOriginResetLiveCoordination, true
	default:
		return 0, false
	}
}

type reservationHeader struct {
	state                       reservationState
	databaseID                  [16]byte
	attemptedTxnID              uint64
	attemptedCommitNonce        [16]byte
	reservationID               [16]byte
	identityKind                localIdentityKind
	reservationIdentity         [32]byte
	operation                   reservationOperation
	outputIdentityKind          localIdentityKind
	recordFlags                 uint32
	attemptedOutputBytes        uint64
	attemptedOutputIdentity     [32]byte
	attemptedOutputSHA512       [64]byte
	previousDestinationIdentity [32]byte
	previousDestinationSHA512   [64]byte
	readerCapacity              uint32
	priorCoordinationIdentity   [32]byte
	priorSidecarID              [16]byte
	priorReaderCapacity         uint32
	processDomainKind           processDomainKind
	processDomainToken          [32]byte
	basenameEncoding            uint16
	basenameLen                 uint32
	basenameCommitment          [32]byte
	previousDestinationBytes    uint64
	creationSecurityKind        uint16
	creationSecurityCommitment  [32]byte
	headerSeq                   uint64
}

func (h reservationHeader) attemptedOutputComplete() bool { return h.outputIdentityKind != 0 }

func (h reservationHeader) encodeInto(block *[PageSize]byte) {
	clear(block[:])
	copy(block[0:8], reservationMagic)
	putU16(block[:], 8, headerRecordSize)
	putU16(block[:], 10, reservationVersion)
	putU32(block[:], 12, uint32(h.state))
	copy(block[16:32], h.databaseID[:])
	putU64(block[:], 32, h.attemptedTxnID)
	copy(block[40:56], h.attemptedCommitNonce[:])
	copy(block[56:72], h.reservationID[:])
	putU16(block[:], 72, uint16(h.identityKind))
	copy(block[80:112], h.reservationIdentity[:])
	putU16(block[:], 112, uint16(h.operation))
	putU16(block[:], 114, uint16(h.outputIdentityKind))
	putU32(block[:], 116, h.recordFlags)
	putU64(block[:], 120, h.attemptedOutputBytes)
	copy(block[128:160], h.attemptedOutputIdentity[:])
	copy(block[160:224], h.attemptedOutputSHA512[:])
	copy(block[224:256], h.previousDestinationIdentity[:])
	copy(block[256:320], h.previousDestinationSHA512[:])
	putU32(block[:], 320, h.readerCapacity)
	copy(block[324:356], h.priorCoordinationIdentity[:])
	copy(block[356:372], h.priorSidecarID[:])
	putU32(block[:], 372, h.priorReaderCapacity)
	putU16(block[:], 376, uint16(h.processDomainKind))
	copy(block[380:412], h.processDomainToken[:])
	putU16(block[:], 412, h.basenameEncoding)
	putU32(block[:], 416, h.basenameLen)
	copy(block[420:452], h.basenameCommitment[:])
	putU64(block[:], 452, h.previousDestinationBytes)
	putU16(block[:], 460, h.creationSecurityKind)
	copy(block[464:496], h.creationSecurityCommitment[:])
	putU64(block[:], 496, h.headerSeq)
	putU32(block[:], headerCRCOffset, crc32cZeroed(block[:], headerCRCOffset, 4))
}

func (h reservationHeader) immutableAttemptEqual(other reservationHeader) bool {
	// These fields are the only mutable parts of a selected reservation pair.
	h.state, h.headerSeq = 0, 0
	h.databaseID, other.databaseID = [16]byte{}, [16]byte{}
	h.attemptedTxnID, other.attemptedTxnID = 0, 0
	h.attemptedCommitNonce, other.attemptedCommitNonce = [16]byte{}, [16]byte{}
	h.outputIdentityKind, other.outputIdentityKind = 0, 0
	h.attemptedOutputBytes, other.attemptedOutputBytes = 0, 0
	h.attemptedOutputIdentity, other.attemptedOutputIdentity = [32]byte{}, [32]byte{}
	h.attemptedOutputSHA512, other.attemptedOutputSHA512 = [64]byte{}, [64]byte{}
	h.processDomainKind, other.processDomainKind = 0, 0
	h.processDomainToken, other.processDomainToken = [32]byte{}, [32]byte{}
	other.state, other.headerSeq = 0, 0
	return h == other
}

func (h reservationHeader) attemptedOutputEqual(other reservationHeader) bool {
	return h.databaseID == other.databaseID && h.attemptedTxnID == other.attemptedTxnID &&
		h.attemptedCommitNonce == other.attemptedCommitNonce && h.outputIdentityKind == other.outputIdentityKind &&
		h.attemptedOutputBytes == other.attemptedOutputBytes && h.attemptedOutputIdentity == other.attemptedOutputIdentity &&
		h.attemptedOutputSHA512 == other.attemptedOutputSHA512
}

type reservationHeaderProblem uint8

const (
	reservationHeaderMagic reservationHeaderProblem = iota + 1
	reservationHeaderFixedValue
	reservationHeaderReserved
	reservationHeaderChecksum
	reservationHeaderState
	reservationHeaderDatabaseAttempt
	reservationHeaderReservationID
	reservationHeaderIdentityKind
	reservationHeaderIdentityEncoding
	reservationHeaderOperation
	reservationHeaderFlags
	reservationHeaderAttemptedOutput
	reservationHeaderPreviousDestination
	reservationHeaderReaderCapacity
	reservationHeaderPriorCoordination
	reservationHeaderProcessDomain
	reservationHeaderBasename
	reservationHeaderCreationSecurity
	reservationHeaderSequence
)

type domainSelectionPolicy struct {
	resolverMayReplace bool
	currentKind        processDomainKind
	currentToken       [32]byte
}

type reservationErrorCode uint8

const (
	reservationErrWrongFileSize reservationErrorCode = iota + 1
	reservationErrNoValidHeader
	reservationErrEqualSequenceDisagreement
	reservationErrNonAdjacentSequence
	reservationErrImmutableAttemptMismatch
	reservationErrInvalidTransition
)

type reservationError struct {
	code           reservationErrorCode
	actual         uint64
	block0, block1 reservationHeaderProblem
	older, newer   uint64
}

func (e *reservationError) Error() string {
	return fmt.Sprintf("exact v4 reservation: error %d", e.code)
}

func selectReservationHeader(data []byte, policy domainSelectionPolicy) (reservationHeader, error) {
	if len(data) != headerRegionSize {
		return reservationHeader{}, &reservationError{code: reservationErrWrongFileSize, actual: uint64(len(data))}
	}
	left, leftProblem := decodeReservationHeader(data[:PageSize])
	right, rightProblem := decodeReservationHeader(data[PageSize:])
	if leftProblem != 0 && rightProblem != 0 {
		return reservationHeader{}, &reservationError{code: reservationErrNoValidHeader, block0: leftProblem, block1: rightProblem}
	}
	if leftProblem != 0 {
		return right, nil
	}
	if rightProblem != 0 {
		return left, nil
	}
	return selectReservationPair(data[:PageSize], left, data[PageSize:], right, policy)
}

func selectReservationPair(leftBlock []byte, left reservationHeader, rightBlock []byte, right reservationHeader, policy domainSelectionPolicy) (reservationHeader, error) {
	if left.headerSeq == right.headerSeq {
		if bytes.Equal(leftBlock, rightBlock) {
			return left, nil
		}
		return reservationHeader{}, &reservationError{code: reservationErrEqualSequenceDisagreement}
	}
	older, newer := left, right
	if right.headerSeq < left.headerSeq {
		older, newer = right, left
	}
	next, ok := checkedAdd(older.headerSeq, 1)
	if !ok || next != newer.headerSeq {
		return reservationHeader{}, &reservationError{code: reservationErrNonAdjacentSequence, older: older.headerSeq, newer: newer.headerSeq}
	}
	if !reservationTransitionValid(older, newer, policy) {
		code := reservationErrInvalidTransition
		if !older.immutableAttemptEqual(newer) {
			code = reservationErrImmutableAttemptMismatch
		}
		return reservationHeader{}, &reservationError{code: code}
	}
	return newer, nil
}

func reservationTransitionValid(older, newer reservationHeader, policy domainSelectionPolicy) bool {
	if !older.immutableAttemptEqual(newer) ||
		!reservationStateTransitionValid(older.operation, older.state, newer.state) ||
		!domainTransitionValid(older.operation, older.processDomainKind, older.processDomainToken, newer.processDomainKind, newer.processDomainToken, policy) {
		return false
	}
	if older.attemptedOutputEqual(newer) {
		return true
	}
	return older.operation == reservationCreateLive && !older.attemptedOutputComplete() && newer.attemptedOutputComplete() &&
		older.state == reservationPrepared && newer.state == reservationPrepared
}

func reservationStateTransitionValid(operation reservationOperation, older, newer reservationState) bool {
	if older == newer {
		return true
	}
	return (operation == reservationFailIfExists || operation == reservationReplaceExisting) &&
		older == reservationPrepared && newer == reservationMainNamespaceAttempted
}

func domainTransitionValid(operation reservationOperation, olderKind processDomainKind, olderToken [32]byte, newerKind processDomainKind, newerToken [32]byte, policy domainSelectionPolicy) bool {
	if olderKind == newerKind && olderToken == newerToken {
		return true
	}
	return operation.live() && policy.resolverMayReplace && newerKind == policy.currentKind && newerToken == policy.currentToken
}

func decodeReservationHeader(block []byte) (reservationHeader, reservationHeaderProblem) {
	if len(block) != PageSize || !bytes.Equal(block[:8], []byte(reservationMagic)) {
		return reservationHeader{}, reservationHeaderMagic
	}
	if u16(block, 8) != headerRecordSize || u16(block, 10) != reservationVersion {
		return reservationHeader{}, reservationHeaderFixedValue
	}
	if anyNonzero(block[74:80]) || u16(block, 378) != 0 || u16(block, 414) != 0 ||
		u16(block, 462) != 0 || u32(block, 504) != 0 || anyNonzero(block[512:]) {
		return reservationHeader{}, reservationHeaderReserved
	}
	if crc32cZeroed(block, headerCRCOffset, 4) != u32(block, headerCRCOffset) {
		return reservationHeader{}, reservationHeaderChecksum
	}
	state, ok := reservationStateFromWire(u32(block, 12))
	if !ok {
		return reservationHeader{}, reservationHeaderState
	}
	reservationID := read16(block, 56)
	if reservationID == [16]byte{} {
		return reservationHeader{}, reservationHeaderReservationID
	}
	identityKind, ok := localIdentityKindFromWire(u16(block, 72))
	if !ok {
		return reservationHeader{}, reservationHeaderIdentityKind
	}
	reservationIdentity := read32(block, 80)
	if !validLocalIdentity(identityKind, reservationIdentity) {
		return reservationHeader{}, reservationHeaderIdentityEncoding
	}
	operation, ok := reservationOperationFromWire(u16(block, 112))
	if !ok {
		return reservationHeader{}, reservationHeaderOperation
	}
	if state == reservationMainNamespaceAttempted && operation != reservationFailIfExists && operation != reservationReplaceExisting {
		return reservationHeader{}, reservationHeaderState
	}

	databaseID := read16(block, 16)
	attemptedTxnID := u64(block, 32)
	attemptedCommitNonce := read16(block, 40)
	outputIdentityWire := u16(block, 114)
	attemptedOutputBytes := u64(block, 120)
	attemptedOutputIdentity := read32(block, 128)
	attemptedOutputSHA512 := read64(block, 160)
	emptyOutput := databaseID == [16]byte{} && attemptedTxnID == 0 && attemptedCommitNonce == [16]byte{} &&
		outputIdentityWire == 0 && attemptedOutputBytes == 0 && attemptedOutputIdentity == [32]byte{} && attemptedOutputSHA512 == [64]byte{}
	var outputIdentityKind localIdentityKind
	if emptyOutput && operation == reservationCreateLive {
		outputIdentityKind = 0
	} else {
		if databaseID == [16]byte{} || attemptedTxnID == 0 || attemptedCommitNonce == [16]byte{} {
			return reservationHeader{}, reservationHeaderDatabaseAttempt
		}
		var valid bool
		outputIdentityKind, valid = localIdentityKindFromWire(outputIdentityWire)
		if !valid || outputIdentityKind != identityKind || attemptedOutputBytes < headerRegionSize ||
			attemptedOutputBytes%PageSize != 0 || !validLocalIdentity(outputIdentityKind, attemptedOutputIdentity) ||
			attemptedOutputIdentity == reservationIdentity {
			return reservationHeader{}, reservationHeaderAttemptedOutput
		}
	}
	if outputIdentityKind == 0 && state != reservationPrepared {
		return reservationHeader{}, reservationHeaderAttemptedOutput
	}

	recordFlags := u32(block, 116)
	if recordFlags&^(flagPreviousDestination|flagPriorSidecar) != 0 {
		return reservationHeader{}, reservationHeaderFlags
	}
	previousDestinationIdentity := read32(block, 224)
	previousDestinationSHA512 := read64(block, 256)
	previousDestinationBytes := u64(block, 452)
	if operation == reservationReplaceExisting {
		if recordFlags != flagPreviousDestination || previousDestinationBytes < headerRegionSize ||
			previousDestinationBytes%PageSize != 0 || !validLocalIdentity(identityKind, previousDestinationIdentity) {
			return reservationHeader{}, reservationHeaderPreviousDestination
		}
	} else if recordFlags&flagPreviousDestination != 0 || previousDestinationIdentity != [32]byte{} ||
		previousDestinationSHA512 != [64]byte{} || previousDestinationBytes != 0 {
		return reservationHeader{}, reservationHeaderPreviousDestination
	}

	readerCapacity := u32(block, 320)
	if operation.live() != (readerCapacity != 0) {
		return reservationHeader{}, reservationHeaderReaderCapacity
	}
	priorCoordinationIdentity := read32(block, 324)
	priorSidecarID := read16(block, 356)
	priorReaderCapacity := u32(block, 372)
	if operation == reservationResetLiveCoordination {
		if priorCoordinationIdentity == [32]byte{} || !validLocalIdentity(identityKind, priorCoordinationIdentity) ||
			priorCoordinationIdentity == reservationIdentity ||
			(outputIdentityKind != 0 && priorCoordinationIdentity == attemptedOutputIdentity) {
			return reservationHeader{}, reservationHeaderPriorCoordination
		}
		if recordFlags&flagPriorSidecar != 0 {
			if priorSidecarID == [16]byte{} || priorReaderCapacity == 0 {
				return reservationHeader{}, reservationHeaderPriorCoordination
			}
		} else if priorSidecarID != [16]byte{} || priorReaderCapacity != 0 {
			return reservationHeader{}, reservationHeaderPriorCoordination
		}
	} else if recordFlags&flagPriorSidecar != 0 || priorCoordinationIdentity != [32]byte{} ||
		priorSidecarID != [16]byte{} || priorReaderCapacity != 0 {
		return reservationHeader{}, reservationHeaderPriorCoordination
	}

	domainWire := u16(block, 376)
	domainToken := read32(block, 380)
	var domainKind processDomainKind
	if operation.live() {
		var valid bool
		domainKind, valid = processDomainKindFromWire(domainWire)
		if !valid || !validProcessDomain(domainKind, domainToken) ||
			(identityKind == localIdentityWindows && domainKind != processDomainHostGlobal) {
			return reservationHeader{}, reservationHeaderProcessDomain
		}
	} else if domainWire != 0 || domainToken != [32]byte{} {
		return reservationHeader{}, reservationHeaderProcessDomain
	}

	basenameEncoding, basenameLen := u16(block, 412), u32(block, 416)
	if basenameEncoding != uint16(identityKind) || basenameLen == 0 ||
		(basenameEncoding == uint16(localIdentityWindows) && basenameLen%2 != 0) {
		return reservationHeader{}, reservationHeaderBasename
	}
	creationSecurityKind := u16(block, 460)
	if creationSecurityKind != uint16(identityKind) {
		return reservationHeader{}, reservationHeaderCreationSecurity
	}
	headerSeq := u64(block, 496)
	if headerSeq == 0 {
		return reservationHeader{}, reservationHeaderSequence
	}
	return reservationHeader{
		state: state, databaseID: databaseID, attemptedTxnID: attemptedTxnID, attemptedCommitNonce: attemptedCommitNonce,
		reservationID: reservationID, identityKind: identityKind, reservationIdentity: reservationIdentity,
		operation: operation, outputIdentityKind: outputIdentityKind, recordFlags: recordFlags,
		attemptedOutputBytes: attemptedOutputBytes, attemptedOutputIdentity: attemptedOutputIdentity,
		attemptedOutputSHA512: attemptedOutputSHA512, previousDestinationIdentity: previousDestinationIdentity,
		previousDestinationSHA512: previousDestinationSHA512, readerCapacity: readerCapacity,
		priorCoordinationIdentity: priorCoordinationIdentity, priorSidecarID: priorSidecarID,
		priorReaderCapacity: priorReaderCapacity, processDomainKind: domainKind, processDomainToken: domainToken,
		basenameEncoding: basenameEncoding, basenameLen: basenameLen, basenameCommitment: read32(block, 420),
		previousDestinationBytes: previousDestinationBytes, creationSecurityKind: creationSecurityKind,
		creationSecurityCommitment: read32(block, 464), headerSeq: headerSeq,
	}, 0
}

type conversionHeaderKind uint8

const (
	conversionReservation conversionHeaderKind = iota + 1
	conversionSidecar
)

type conversionHeader struct {
	kind        conversionHeaderKind
	reservation reservationHeader
	sidecar     sidecarHeader
}

func (h conversionHeader) sequence() uint64 {
	if h.kind == conversionReservation {
		return h.reservation.headerSeq
	}
	return h.sidecar.headerSeq
}

type conversionBlockProblem uint8

const (
	conversionBlockUnknownMagic conversionBlockProblem = iota + 1
	conversionBlockReservation
	conversionBlockSidecar
)

type conversionBlockIssue struct {
	problem            conversionBlockProblem
	reservationProblem reservationHeaderProblem
	sidecarProblem     sidecarHeaderProblem
}

type conversionErrorCode uint8

const (
	conversionErrHeaderRegionTooShort conversionErrorCode = iota + 1
	conversionErrNoValidHeader
	conversionErrEqualSequenceDisagreement
	conversionErrNonAdjacentSequence
	conversionErrInvalidTransition
)

type conversionError struct {
	code           conversionErrorCode
	block0, block1 conversionBlockIssue
	older, newer   uint64
}

func (e *conversionError) Error() string {
	return fmt.Sprintf("exact v4 reservation conversion: error %d", e.code)
}

func selectConversionHeader(data []byte, policy domainSelectionPolicy) (conversionHeader, error) {
	if len(data) < headerRegionSize {
		return conversionHeader{}, &conversionError{code: conversionErrHeaderRegionTooShort}
	}
	left, leftIssue := decodeConversionBlock(data[:PageSize])
	right, rightIssue := decodeConversionBlock(data[PageSize:headerRegionSize])
	if leftIssue.problem != 0 && rightIssue.problem != 0 {
		return conversionHeader{}, &conversionError{
			code: conversionErrNoValidHeader, block0: leftIssue, block1: rightIssue,
		}
	}
	if leftIssue.problem != 0 {
		return right, nil
	}
	if rightIssue.problem != 0 {
		return left, nil
	}
	if left.sequence() == right.sequence() {
		if bytes.Equal(data[:PageSize], data[PageSize:headerRegionSize]) {
			return left, nil
		}
		return conversionHeader{}, &conversionError{code: conversionErrEqualSequenceDisagreement}
	}
	older, newer := left, right
	if right.sequence() < left.sequence() {
		older, newer = right, left
	}
	next, ok := checkedAdd(older.sequence(), 1)
	if !ok || next != newer.sequence() {
		return conversionHeader{}, &conversionError{code: conversionErrNonAdjacentSequence, older: older.sequence(), newer: newer.sequence()}
	}
	if !conversionTransitionValid(older, newer, policy) {
		return conversionHeader{}, &conversionError{code: conversionErrInvalidTransition}
	}
	return newer, nil
}

func decodeConversionBlock(block []byte) (conversionHeader, conversionBlockIssue) {
	if len(block) != PageSize {
		return conversionHeader{}, conversionBlockIssue{problem: conversionBlockUnknownMagic}
	}
	if bytes.Equal(block[:8], []byte(reservationMagic)) {
		header, problem := decodeReservationHeader(block)
		if problem != 0 {
			return conversionHeader{}, conversionBlockIssue{problem: conversionBlockReservation, reservationProblem: problem}
		}
		return conversionHeader{kind: conversionReservation, reservation: header}, conversionBlockIssue{}
	}
	if bytes.Equal(block[:8], []byte(sidecarMagic)) {
		header, problem := decodeSidecarHeader(block)
		if problem != 0 {
			return conversionHeader{}, conversionBlockIssue{problem: conversionBlockSidecar, sidecarProblem: problem}
		}
		return conversionHeader{kind: conversionSidecar, sidecar: header}, conversionBlockIssue{}
	}
	return conversionHeader{}, conversionBlockIssue{problem: conversionBlockUnknownMagic}
}

func conversionTransitionValid(older, newer conversionHeader, policy domainSelectionPolicy) bool {
	switch {
	case older.kind == conversionReservation && newer.kind == conversionReservation:
		return reservationTransitionValid(older.reservation, newer.reservation, policy)
	case older.kind == conversionReservation && newer.kind == conversionSidecar:
		return reservationToSidecarValid(older.reservation, newer.sidecar, policy)
	case older.kind == conversionSidecar && newer.kind == conversionSidecar:
		return sidecarConversionTransitionValid(older.sidecar, newer.sidecar, policy)
	default:
		return false
	}
}

func reservationToSidecarValid(reservation reservationHeader, sidecar sidecarHeader, policy domainSelectionPolicy) bool {
	origin, ok := reservation.operation.sidecarOrigin()
	return ok && reservation.state == reservationPrepared && reservation.attemptedOutputComplete() &&
		sidecar.state == sidecarInitializing && sidecar.identityKind == reservation.identityKind &&
		sidecar.capacity == reservation.readerCapacity && sidecar.databaseID == reservation.databaseID &&
		sidecar.mainIdentity == reservation.attemptedOutputIdentity && sidecar.sidecarIdentity == reservation.reservationIdentity &&
		sidecar.sidecarID == reservation.reservationID && sidecar.origin == origin &&
		sidecar.attemptedTxnID == reservation.attemptedTxnID && sidecar.attemptedCommitNonce == reservation.attemptedCommitNonce &&
		sidecar.attemptedMainBytes == reservation.attemptedOutputBytes && sidecar.attemptedMainSHA512 == reservation.attemptedOutputSHA512 &&
		sidecar.basenameEncoding == reservation.basenameEncoding && sidecar.basenameLen == reservation.basenameLen &&
		sidecar.basenameCommitment == reservation.basenameCommitment && sidecar.creationSecurityKind == reservation.creationSecurityKind &&
		sidecar.creationSecurityCommitment == reservation.creationSecurityCommitment &&
		domainTransitionValid(reservation.operation, reservation.processDomainKind, reservation.processDomainToken,
			sidecar.processDomainKind, sidecar.processDomainToken, policy)
}

func sidecarConversionTransitionValid(older, newer sidecarHeader, policy domainSelectionPolicy) bool {
	if !older.conversionIdentityEqual(newer) || !sidecarStateTransitionValid(older.origin, older.state, newer.state) {
		return false
	}
	if older.processDomainKind == newer.processDomainKind && older.processDomainToken == newer.processDomainToken {
		return true
	}
	var operation reservationOperation
	switch newer.origin {
	case sidecarOriginCreateLive:
		operation = reservationCreateLive
	case sidecarOriginInitializeLive:
		operation = reservationInitializeLive
	case sidecarOriginResetLiveCoordination:
		operation = reservationResetLiveCoordination
	default:
		return false
	}
	return older.state == sidecarInitializing && newer.state == sidecarInitializing &&
		domainTransitionValid(operation, older.processDomainKind, older.processDomainToken,
			newer.processDomainKind, newer.processDomainToken, policy)
}
