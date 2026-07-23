package exactv4

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

const (
	sidecarMagic            = "IPR4RDRS"
	sidecarVersion   uint16 = 1
	headerRecordSize uint16 = 512
	headerRegionSize        = 2 * PageSize
	sidecarSlotSize  uint16 = 64
	headerCRCOffset         = 508
	slotCRCOffset           = 60
)

var sidecarCRCZero [4]byte

type localIdentityKind uint16

const (
	localIdentityPOSIX   localIdentityKind = 1
	localIdentityWindows localIdentityKind = 2
)

func localIdentityKindFromWire(value uint16) (localIdentityKind, bool) {
	kind := localIdentityKind(value)
	return kind, kind == localIdentityPOSIX || kind == localIdentityWindows
}

type sidecarState uint32

const (
	sidecarReady                  sidecarState = 1
	sidecarInitializing           sidecarState = 2
	sidecarMainNamespaceAttempted sidecarState = 3
)

func sidecarStateFromWire(value uint32) (sidecarState, bool) {
	state := sidecarState(value)
	return state, state == sidecarReady || state == sidecarInitializing || state == sidecarMainNamespaceAttempted
}

type sidecarOrigin uint16

const (
	sidecarOriginCreateLive            sidecarOrigin = 1
	sidecarOriginInitializeLive        sidecarOrigin = 2
	sidecarOriginResetLiveCoordination sidecarOrigin = 3
)

func sidecarOriginFromWire(value uint16) (sidecarOrigin, bool) {
	origin := sidecarOrigin(value)
	return origin, origin >= sidecarOriginCreateLive && origin <= sidecarOriginResetLiveCoordination
}

type processDomainKind uint16

const (
	processDomainLinuxPIDNamespace processDomainKind = 1
	processDomainFreeBSDJail       processDomainKind = 2
	processDomainHostGlobal        processDomainKind = 3
)

func processDomainKindFromWire(value uint16) (processDomainKind, bool) {
	kind := processDomainKind(value)
	return kind, kind >= processDomainLinuxPIDNamespace && kind <= processDomainHostGlobal
}

type sidecarHeader struct {
	identityKind               localIdentityKind
	capacity                   uint32
	state                      sidecarState
	databaseID                 [16]byte
	mainIdentity               [32]byte
	sidecarIdentity            [32]byte
	sidecarID                  [16]byte
	origin                     sidecarOrigin
	attemptedTxnID             uint64
	attemptedCommitNonce       [16]byte
	attemptedMainBytes         uint64
	attemptedMainSHA512        [64]byte
	processDomainKind          processDomainKind
	processDomainToken         [32]byte
	basenameEncoding           uint16
	basenameLen                uint32
	basenameCommitment         [32]byte
	creationSecurityKind       uint16
	creationSecurityCommitment [32]byte
	headerSeq                  uint64
}

func (h sidecarHeader) exactFileSize() (uint64, bool) {
	slots, ok := checkedAdd(uint64(h.capacity), 1)
	if !ok {
		return 0, false
	}
	bytes, ok := checkedMul(slots, uint64(sidecarSlotSize))
	if !ok {
		return 0, false
	}
	return checkedAdd(headerRegionSize, bytes)
}

func (h sidecarHeader) encodeInto(block *[PageSize]byte) {
	clear(block[:])
	copy(block[0:8], sidecarMagic)
	putU16(block[:], 8, sidecarVersion)
	putU16(block[:], 10, headerRecordSize)
	putU16(block[:], 12, sidecarSlotSize)
	putU16(block[:], 14, uint16(h.identityKind))
	putU32(block[:], 16, h.capacity)
	putU32(block[:], 20, uint32(h.state))
	copy(block[24:40], h.databaseID[:])
	copy(block[40:72], h.mainIdentity[:])
	copy(block[72:104], h.sidecarIdentity[:])
	copy(block[104:120], h.sidecarID[:])
	putU16(block[:], 120, uint16(h.origin))
	putU16(block[:], 122, 1)
	putU64(block[:], 124, h.attemptedTxnID)
	copy(block[132:148], h.attemptedCommitNonce[:])
	putU64(block[:], 148, h.attemptedMainBytes)
	copy(block[156:220], h.attemptedMainSHA512[:])
	putU16(block[:], 220, uint16(h.processDomainKind))
	copy(block[224:256], h.processDomainToken[:])
	putU16(block[:], 256, h.basenameEncoding)
	putU32(block[:], 260, h.basenameLen)
	copy(block[264:296], h.basenameCommitment[:])
	putU16(block[:], 296, h.creationSecurityKind)
	copy(block[300:332], h.creationSecurityCommitment[:])
	putU64(block[:], 496, h.headerSeq)
	putU32(block[:], headerCRCOffset, crc32cZeroed(block[:], headerCRCOffset, 4))
}

func (h sidecarHeader) conversionIdentityEqual(other sidecarHeader) bool {
	h.headerSeq = 0
	h.state = 0
	h.processDomainKind = 0
	h.processDomainToken = [32]byte{}
	other.headerSeq = 0
	other.state = 0
	other.processDomainKind = 0
	other.processDomainToken = [32]byte{}
	return h == other
}

func (h sidecarHeader) immutableIdentityEqual(other sidecarHeader) bool {
	return h.conversionIdentityEqual(other) &&
		h.processDomainKind == other.processDomainKind && h.processDomainToken == other.processDomainToken
}

type sidecarHeaderProblem uint8

const (
	sidecarHeaderMagic sidecarHeaderProblem = iota + 1
	sidecarHeaderFixedValue
	sidecarHeaderReserved
	sidecarHeaderChecksum
	sidecarHeaderIdentityKind
	sidecarHeaderIdentityEncoding
	sidecarHeaderCapacity
	sidecarHeaderState
	sidecarHeaderDatabaseID
	sidecarHeaderIdentityCollision
	sidecarHeaderSidecarID
	sidecarHeaderOrigin
	sidecarHeaderAttempt
	sidecarHeaderProcessDomain
	sidecarHeaderBasename
	sidecarHeaderCreationSecurity
	sidecarHeaderSequence
)

type sidecarErrorCode uint8

const (
	sidecarErrHeaderRegionTooShort sidecarErrorCode = iota + 1
	sidecarErrNoValidHeader
	sidecarErrEqualSequenceDisagreement
	sidecarErrNonAdjacentSequence
	sidecarErrImmutableIdentityMismatch
	sidecarErrInvalidStateTransition
	sidecarErrNotReady
	sidecarErrWrongFileSize
	sidecarErrSizeOverflow
)

type sidecarError struct {
	code           sidecarErrorCode
	block0, block1 sidecarHeaderProblem
	older, newer   uint64
	state          sidecarState
	expected       uint64
	actual         uint64
}

func (e *sidecarError) Error() string { return fmt.Sprintf("exact v4 sidecar: error %d", e.code) }

func selectSidecarHeader(data []byte) (sidecarHeader, *sidecarError) {
	if len(data) < headerRegionSize {
		return sidecarHeader{}, &sidecarError{code: sidecarErrHeaderRegionTooShort}
	}
	left, leftProblem := decodeSidecarHeader(data[:PageSize])
	right, rightProblem := decodeSidecarHeader(data[PageSize:headerRegionSize])
	if leftProblem != 0 && rightProblem != 0 {
		return sidecarHeader{}, &sidecarError{code: sidecarErrNoValidHeader, block0: leftProblem, block1: rightProblem}
	}
	if leftProblem != 0 {
		return right, nil
	}
	if rightProblem != 0 {
		return left, nil
	}
	if left.headerSeq == right.headerSeq {
		if bytes.Equal(data[:PageSize], data[PageSize:headerRegionSize]) {
			return left, nil
		}
		return sidecarHeader{}, &sidecarError{code: sidecarErrEqualSequenceDisagreement}
	}
	older, newer := left, right
	if right.headerSeq < left.headerSeq {
		older, newer = right, left
	}
	next, ok := checkedAdd(older.headerSeq, 1)
	if !ok || next != newer.headerSeq {
		return sidecarHeader{}, &sidecarError{code: sidecarErrNonAdjacentSequence, older: older.headerSeq, newer: newer.headerSeq}
	}
	if !left.immutableIdentityEqual(right) {
		return sidecarHeader{}, &sidecarError{code: sidecarErrImmutableIdentityMismatch}
	}
	if !sidecarStateTransitionValid(older.origin, older.state, newer.state) {
		return sidecarHeader{}, &sidecarError{code: sidecarErrInvalidStateTransition}
	}
	return newer, nil
}

func sidecarStateTransitionValid(origin sidecarOrigin, older, newer sidecarState) bool {
	if older == newer {
		return true
	}
	switch origin {
	case sidecarOriginCreateLive:
		return (older == sidecarInitializing && newer == sidecarMainNamespaceAttempted) ||
			(older == sidecarMainNamespaceAttempted && newer == sidecarReady)
	case sidecarOriginInitializeLive, sidecarOriginResetLiveCoordination:
		return older == sidecarInitializing && newer == sidecarReady
	default:
		return false
	}
}

func decodeReadySidecarImage(data []byte) (sidecarHeader, *sidecarError) {
	header, err := selectSidecarHeader(data)
	if err != nil {
		return sidecarHeader{}, err
	}
	if header.state != sidecarReady {
		return sidecarHeader{}, &sidecarError{code: sidecarErrNotReady, state: header.state}
	}
	expected, ok := header.exactFileSize()
	if !ok {
		return sidecarHeader{}, &sidecarError{code: sidecarErrSizeOverflow}
	}
	actual := uint64(len(data))
	if expected != actual {
		return sidecarHeader{}, &sidecarError{code: sidecarErrWrongFileSize, expected: expected, actual: actual}
	}
	return header, nil
}

func decodeSidecarHeader(block []byte) (sidecarHeader, sidecarHeaderProblem) {
	if len(block) != PageSize || !bytes.Equal(block[:8], []byte(sidecarMagic)) {
		return sidecarHeader{}, sidecarHeaderMagic
	}
	if u16(block, 8) != sidecarVersion || u16(block, 10) != headerRecordSize ||
		u16(block, 12) != sidecarSlotSize || u16(block, 122) != 1 {
		return sidecarHeader{}, sidecarHeaderFixedValue
	}
	if u16(block, 222) != 0 || u16(block, 258) != 0 || u16(block, 298) != 0 ||
		anyNonzero(block[332:496]) || u32(block, 504) != 0 || anyNonzero(block[512:]) {
		return sidecarHeader{}, sidecarHeaderReserved
	}
	if crc32cZeroed(block, headerCRCOffset, 4) != u32(block, headerCRCOffset) {
		return sidecarHeader{}, sidecarHeaderChecksum
	}
	identityKind, ok := localIdentityKindFromWire(u16(block, 14))
	if !ok {
		return sidecarHeader{}, sidecarHeaderIdentityKind
	}
	capacity := u32(block, 16)
	if capacity == 0 {
		return sidecarHeader{}, sidecarHeaderCapacity
	}
	state, ok := sidecarStateFromWire(u32(block, 20))
	if !ok {
		return sidecarHeader{}, sidecarHeaderState
	}
	databaseID := read16(block, 24)
	if databaseID == [16]byte{} {
		return sidecarHeader{}, sidecarHeaderDatabaseID
	}
	mainIdentity, sidecarIdentity := read32(block, 40), read32(block, 72)
	if !validLocalIdentity(identityKind, mainIdentity) || !validLocalIdentity(identityKind, sidecarIdentity) {
		return sidecarHeader{}, sidecarHeaderIdentityEncoding
	}
	if mainIdentity == sidecarIdentity {
		return sidecarHeader{}, sidecarHeaderIdentityCollision
	}
	sidecarID := read16(block, 104)
	if sidecarID == [16]byte{} {
		return sidecarHeader{}, sidecarHeaderSidecarID
	}
	origin, ok := sidecarOriginFromWire(u16(block, 120))
	if !ok {
		return sidecarHeader{}, sidecarHeaderOrigin
	}
	if state == sidecarMainNamespaceAttempted && origin != sidecarOriginCreateLive {
		return sidecarHeader{}, sidecarHeaderState
	}
	attemptedTxnID := u64(block, 124)
	attemptedCommitNonce := read16(block, 132)
	attemptedMainBytes := u64(block, 148)
	if attemptedTxnID == 0 || attemptedCommitNonce == [16]byte{} ||
		attemptedMainBytes < headerRegionSize || attemptedMainBytes%PageSize != 0 {
		return sidecarHeader{}, sidecarHeaderAttempt
	}
	domainKind, ok := processDomainKindFromWire(u16(block, 220))
	if !ok {
		return sidecarHeader{}, sidecarHeaderProcessDomain
	}
	domainToken := read32(block, 224)
	if !validProcessDomain(domainKind, domainToken) ||
		(identityKind == localIdentityWindows && domainKind != processDomainHostGlobal) {
		return sidecarHeader{}, sidecarHeaderProcessDomain
	}
	basenameEncoding, basenameLen := u16(block, 256), u32(block, 260)
	if basenameEncoding != uint16(identityKind) || basenameLen == 0 ||
		(basenameEncoding == uint16(localIdentityWindows) && basenameLen%2 != 0) {
		return sidecarHeader{}, sidecarHeaderBasename
	}
	creationSecurityKind := u16(block, 296)
	if creationSecurityKind != uint16(identityKind) {
		return sidecarHeader{}, sidecarHeaderCreationSecurity
	}
	headerSeq := u64(block, 496)
	if headerSeq == 0 {
		return sidecarHeader{}, sidecarHeaderSequence
	}
	return sidecarHeader{
		identityKind: identityKind, capacity: capacity, state: state, databaseID: databaseID,
		mainIdentity: mainIdentity, sidecarIdentity: sidecarIdentity, sidecarID: sidecarID, origin: origin,
		attemptedTxnID: attemptedTxnID, attemptedCommitNonce: attemptedCommitNonce,
		attemptedMainBytes: attemptedMainBytes, attemptedMainSHA512: read64(block, 156),
		processDomainKind: domainKind, processDomainToken: domainToken,
		basenameEncoding: basenameEncoding, basenameLen: basenameLen, basenameCommitment: read32(block, 264),
		creationSecurityKind: creationSecurityKind, creationSecurityCommitment: read32(block, 300), headerSeq: headerSeq,
	}, 0
}

func validLocalIdentity(kind localIdentityKind, identity [32]byte) bool {
	switch kind {
	case localIdentityPOSIX:
		return !anyNonzero(identity[16:])
	case localIdentityWindows:
		return !anyNonzero(identity[24:])
	default:
		return false
	}
}

func validProcessDomain(kind processDomainKind, token [32]byte) bool {
	switch kind {
	case processDomainLinuxPIDNamespace:
		return !anyNonzero(token[16:])
	case processDomainFreeBSDJail:
		return !anyNonzero(token[4:])
	case processDomainHostGlobal:
		return token == [32]byte{}
	default:
		return false
	}
}

type slotRole uint8

const (
	slotWriter slotRole = iota + 1
	slotReader
)

type activeSlot struct {
	txnID        uint64
	processID    uint64
	processStart uint64
	taskID       uint64
	nonce        [16]byte
}

type stableSlot struct {
	active bool
	claim  activeSlot
}

type slotProblem uint8

const (
	slotFreeNonzero slotProblem = iota + 1
	slotTransition
	slotUnknownState
	slotReserved
	slotWriterTransactionZero
	slotProcessIDZero
	slotProcessIDUnrepresentable
	slotTaskIDUnrepresentable
	slotNonceZero
	slotChecksum
)

type slotHostLimits struct {
	processIDMax uint64
	taskIDMax    uint64
}

func decodeStableSlot(data []byte, role slotRole, host slotHostLimits) (stableSlot, slotProblem) {
	if len(data) != int(sidecarSlotSize) {
		return stableSlot{}, slotUnknownState
	}
	state := u32(data, 0)
	if state == 0 {
		if anyNonzero(data) {
			return stableSlot{}, slotFreeNonzero
		}
		return stableSlot{}, 0
	}
	if state == 2 {
		return stableSlot{}, slotTransition
	}
	if state != 1 {
		return stableSlot{}, slotUnknownState
	}
	if u32(data, 4) != 0 || u32(data, 56) != 0 {
		return stableSlot{}, slotReserved
	}
	txnID := u64(data, 8)
	if role == slotWriter && txnID == 0 {
		return stableSlot{}, slotWriterTransactionZero
	}
	processID := u64(data, 16)
	if processID == 0 {
		return stableSlot{}, slotProcessIDZero
	}
	if processID > host.processIDMax {
		return stableSlot{}, slotProcessIDUnrepresentable
	}
	taskID := u64(data, 32)
	if taskID > host.taskIDMax {
		return stableSlot{}, slotTaskIDUnrepresentable
	}
	nonce := read16(data, 40)
	if nonce == [16]byte{} {
		return stableSlot{}, slotNonceZero
	}
	if crc32cZeroed(data, slotCRCOffset, 4) != u32(data, slotCRCOffset) {
		return stableSlot{}, slotChecksum
	}
	return stableSlot{active: true, claim: activeSlot{
		txnID: txnID, processID: processID, processStart: u64(data, 24), taskID: taskID, nonce: nonce,
	}}, 0
}

func encodeActiveSlot(slot activeSlot) [sidecarSlotSize]byte {
	var data [sidecarSlotSize]byte
	putU32(data[:], 0, 1)
	putU64(data[:], 8, slot.txnID)
	putU64(data[:], 16, slot.processID)
	putU64(data[:], 24, slot.processStart)
	putU64(data[:], 32, slot.taskID)
	copy(data[40:56], slot.nonce[:])
	putU32(data[:], slotCRCOffset, crc32cZeroed(data[:], slotCRCOffset, 4))
	return data
}

type readySidecarExpectations struct {
	databaseID         [16]byte
	mainIdentity       [32]byte
	sidecarIdentity    [32]byte
	processDomainKind  processDomainKind
	processDomainToken [32]byte
	basenameEncoding   uint16
	basenameLen        uint32
	basenameCommitment [32]byte
	hostLimits         slotHostLimits
}

type readySidecarInspection struct {
	header               sidecarHeader
	writerActive         bool
	writer               activeSlot
	activeReaders        uint32
	registeringReaders   uint32
	oldestReaderTxnValid bool
	oldestReaderTxn      uint64
	newestReaderTxnValid bool
	newestReaderTxn      uint64
	lowestFreeSlotValid  bool
	lowestFreeSlot       uint32
	slotBytes            []byte
	hostLimits           slotHostLimits
}

type readySidecarErrorCode uint8

const (
	readySidecarErrSidecar readySidecarErrorCode = iota + 1
	readySidecarErrDatabaseIDMismatch
	readySidecarErrMainIdentityMismatch
	readySidecarErrSidecarIdentityMismatch
	readySidecarErrProcessDomainMismatch
	readySidecarErrBasenameMismatch
	readySidecarErrHeaderChanged
	readySidecarErrSlotOffsetOverflow
	readySidecarErrSlot
	readySidecarErrSelectedTransactionZero
	readySidecarErrWriterTransactionMismatch
	readySidecarErrReaderTransactionFuture
)

type readySidecarError struct {
	code     readySidecarErrorCode
	sidecar  *sidecarError
	index    uint32
	problem  slotProblem
	expected uint64
	actual   uint64
}

func (e *readySidecarError) Error() string {
	return fmt.Sprintf("exact v4 ready sidecar: error %d", e.code)
}

func inspectReadySidecar(data []byte, expected readySidecarExpectations) (readySidecarInspection, *readySidecarError) {
	header, err := decodeReadySidecarImage(data)
	if err != nil {
		return readySidecarInspection{}, &readySidecarError{code: readySidecarErrSidecar, sidecar: err}
	}
	if header.databaseID != expected.databaseID {
		return readySidecarInspection{}, &readySidecarError{code: readySidecarErrDatabaseIDMismatch}
	}
	if header.mainIdentity != expected.mainIdentity {
		return readySidecarInspection{}, &readySidecarError{code: readySidecarErrMainIdentityMismatch}
	}
	if header.sidecarIdentity != expected.sidecarIdentity {
		return readySidecarInspection{}, &readySidecarError{code: readySidecarErrSidecarIdentityMismatch}
	}
	if header.processDomainKind != expected.processDomainKind || header.processDomainToken != expected.processDomainToken {
		return readySidecarInspection{}, &readySidecarError{code: readySidecarErrProcessDomainMismatch}
	}
	if header.basenameEncoding != expected.basenameEncoding || header.basenameLen != expected.basenameLen ||
		header.basenameCommitment != expected.basenameCommitment {
		return readySidecarInspection{}, &readySidecarError{code: readySidecarErrBasenameMismatch}
	}
	return scanReadySidecarSlots(data, header, expected.hostLimits)
}

func scanReadySidecarSlots(data []byte, header sidecarHeader, host slotHostLimits) (readySidecarInspection, *readySidecarError) {
	inspection := readySidecarInspection{header: header, slotBytes: data, hostLimits: host}
	writer, slotErr := sidecarSlotAt(data, 0, slotWriter, host)
	if slotErr != nil {
		return readySidecarInspection{}, slotErr
	}
	if writer.active {
		inspection.writerActive, inspection.writer = true, writer.claim
	}
	for rawIndex := uint64(1); rawIndex <= uint64(header.capacity); rawIndex++ {
		index := uint32(rawIndex)
		slot, slotErr := sidecarSlotAt(data, index, slotReader, host)
		if slotErr != nil {
			return readySidecarInspection{}, slotErr
		}
		if !slot.active {
			if !inspection.lowestFreeSlotValid {
				inspection.lowestFreeSlotValid, inspection.lowestFreeSlot = true, index
			}
			continue
		}
		inspection.activeReaders++
		if slot.claim.txnID == 0 {
			inspection.registeringReaders++
			continue
		}
		if !inspection.oldestReaderTxnValid || slot.claim.txnID < inspection.oldestReaderTxn {
			inspection.oldestReaderTxnValid, inspection.oldestReaderTxn = true, slot.claim.txnID
		}
		if !inspection.newestReaderTxnValid || slot.claim.txnID > inspection.newestReaderTxn {
			inspection.newestReaderTxnValid, inspection.newestReaderTxn = true, slot.claim.txnID
		}
	}
	return inspection, nil
}

// stableSlot exposes one already structurally scanned slot to the OS liveness
// layer without allocating a per-slot collection. Transaction consistency is
// intentionally checked only after proven-dead claims have been reaped.
func (inspection readySidecarInspection) stableSlot(index uint32) (stableSlot, *readySidecarError) {
	if index > inspection.header.capacity {
		return stableSlot{}, &readySidecarError{code: readySidecarErrSlotOffsetOverflow}
	}
	role := slotReader
	if index == 0 {
		role = slotWriter
	}
	return sidecarSlotAt(inspection.slotBytes, index, role, inspection.hostLimits)
}

func validateReadySidecarTransactions(
	data []byte,
	expectedHeader sidecarHeader,
	selectedTxn uint64,
	host slotHostLimits,
) (readySidecarInspection, *readySidecarError) {
	header, err := decodeReadySidecarImage(data)
	if err != nil {
		return readySidecarInspection{}, &readySidecarError{code: readySidecarErrSidecar, sidecar: err}
	}
	if header != expectedHeader {
		return readySidecarInspection{}, &readySidecarError{code: readySidecarErrHeaderChanged}
	}
	inspection, scanErr := scanReadySidecarSlots(data, header, host)
	if scanErr != nil {
		return readySidecarInspection{}, scanErr
	}
	if inspection.writerActive && inspection.writer.txnID != selectedTxn {
		return readySidecarInspection{}, &readySidecarError{
			code: readySidecarErrWriterTransactionMismatch, expected: selectedTxn, actual: inspection.writer.txnID,
		}
	}
	if inspection.newestReaderTxnValid && inspection.newestReaderTxn > selectedTxn {
		return readySidecarInspection{}, &readySidecarError{
			code: readySidecarErrReaderTransactionFuture, expected: selectedTxn, actual: inspection.newestReaderTxn,
		}
	}
	return inspection, nil
}

func sidecarSlotAt(data []byte, index uint32, role slotRole, host slotHostLimits) (stableSlot, *readySidecarError) {
	relative, ok := checkedMul(uint64(index), uint64(sidecarSlotSize))
	if !ok {
		return stableSlot{}, &readySidecarError{code: readySidecarErrSlotOffsetOverflow}
	}
	start, ok := checkedAdd(headerRegionSize, relative)
	if !ok {
		return stableSlot{}, &readySidecarError{code: readySidecarErrSlotOffsetOverflow}
	}
	end, ok := checkedAdd(start, uint64(sidecarSlotSize))
	if !ok || end > uint64(len(data)) {
		return stableSlot{}, &readySidecarError{code: readySidecarErrSlotOffsetOverflow}
	}
	startInt, endInt := int(start), int(end)
	if uint64(startInt) != start || uint64(endInt) != end {
		return stableSlot{}, &readySidecarError{code: readySidecarErrSlotOffsetOverflow}
	}
	slot, problem := decodeStableSlot(data[startInt:endInt], role, host)
	if problem != 0 {
		return stableSlot{}, &readySidecarError{code: readySidecarErrSlot, index: index, problem: problem}
	}
	return slot, nil
}

func crc32cZeroed(data []byte, offset, size int) uint32 {
	crc := crc32.Update(0, castagnoliTable, data[:offset])
	crc = crc32.Update(crc, castagnoliTable, sidecarCRCZero[:size])
	return crc32.Update(crc, castagnoliTable, data[offset+size:])
}

func u16(data []byte, at int) uint16           { return binary.LittleEndian.Uint16(data[at : at+2]) }
func u32(data []byte, at int) uint32           { return binary.LittleEndian.Uint32(data[at : at+4]) }
func u64(data []byte, at int) uint64           { return binary.LittleEndian.Uint64(data[at : at+8]) }
func putU16(data []byte, at int, value uint16) { binary.LittleEndian.PutUint16(data[at:at+2], value) }
func putU32(data []byte, at int, value uint32) { binary.LittleEndian.PutUint32(data[at:at+4], value) }
func putU64(data []byte, at int, value uint64) { binary.LittleEndian.PutUint64(data[at:at+8], value) }

func read16(data []byte, at int) [16]byte {
	var value [16]byte
	copy(value[:], data[at:at+16])
	return value
}

func read32(data []byte, at int) [32]byte {
	var value [32]byte
	copy(value[:], data[at:at+32])
	return value
}

func read64(data []byte, at int) [64]byte {
	var value [64]byte
	copy(value[:], data[at:at+64])
	return value
}
