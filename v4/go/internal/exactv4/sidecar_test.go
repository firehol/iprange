package exactv4

import (
	"errors"
	"math"
	"testing"
)

func testPOSIXIdentity(device, inode uint64) [32]byte {
	var identity [32]byte
	putU64(identity[:], 0, device)
	putU64(identity[:], 8, inode)
	return identity
}

func testSidecarHeader(sequence uint64, state sidecarState) sidecarHeader {
	return sidecarHeader{
		identityKind: localIdentityPOSIX, capacity: 3, state: state, databaseID: [16]byte{1},
		mainIdentity: testPOSIXIdentity(7, 11), sidecarIdentity: testPOSIXIdentity(7, 12), sidecarID: [16]byte{2},
		origin: sidecarOriginCreateLive, attemptedTxnID: 1, attemptedCommitNonce: [16]byte{3},
		attemptedMainBytes: 8192, attemptedMainSHA512: [64]byte{4},
		processDomainKind: processDomainLinuxPIDNamespace, processDomainToken: testPOSIXIdentity(5, 9),
		basenameEncoding: 1, basenameLen: 7, basenameCommitment: [32]byte{6},
		creationSecurityKind: 1, creationSecurityCommitment: [32]byte{7}, headerSeq: sequence,
	}
}

func testSidecarImage(left, right sidecarHeader) []byte {
	size, ok := left.exactFileSize()
	if !ok {
		panic("test sidecar size overflow")
	}
	data := make([]byte, int(size))
	var block [PageSize]byte
	left.encodeInto(&block)
	copy(data[:PageSize], block[:])
	right.encodeInto(&block)
	copy(data[PageSize:headerRegionSize], block[:])
	return data
}

func testSidecarExpectations(header sidecarHeader) readySidecarExpectations {
	return readySidecarExpectations{
		databaseID: header.databaseID, mainIdentity: header.mainIdentity, sidecarIdentity: header.sidecarIdentity,
		processDomainKind: header.processDomainKind, processDomainToken: header.processDomainToken,
		basenameEncoding: header.basenameEncoding, basenameLen: header.basenameLen,
		basenameCommitment: header.basenameCommitment,
		hostLimits:         slotHostLimits{processIDMax: math.MaxUint32, taskIDMax: math.MaxUint32},
	}
}

func putTestSlot(data []byte, index uint32, slot activeSlot) {
	start := headerRegionSize + int(index)*int(sidecarSlotSize)
	encoded := encodeActiveSlot(slot)
	copy(data[start:start+int(sidecarSlotSize)], encoded[:])
}

func requireSidecarCode(t *testing.T, err error, want sidecarErrorCode) *sidecarError {
	t.Helper()
	var got *sidecarError
	if !errors.As(err, &got) {
		t.Fatalf("error = %T %v, want sidecar error %d", err, err, want)
	}
	if got.code != want {
		t.Fatalf("sidecar error = %d, want %d", got.code, want)
	}
	return got
}

func requireReadySidecarCode(t *testing.T, err error, want readySidecarErrorCode) *readySidecarError {
	t.Helper()
	var got *readySidecarError
	if !errors.As(err, &got) {
		t.Fatalf("error = %T %v, want ready sidecar error %d", err, err, want)
	}
	if got.code != want {
		t.Fatalf("ready sidecar error = %d, want %d", got.code, want)
	}
	return got
}

func TestSidecarExactHeaderOffsetsSelectionAndReadySize(t *testing.T) {
	data := testSidecarImage(testSidecarHeader(1, sidecarMainNamespaceAttempted), testSidecarHeader(2, sidecarReady))
	selected, err := decodeReadySidecarImage(data)
	if err != nil {
		t.Fatal(err)
	}
	if selected.headerSeq != 2 || selected.capacity != 3 {
		t.Fatalf("selected sequence/capacity = %d/%d", selected.headerSeq, selected.capacity)
	}
	size, ok := selected.exactFileSize()
	if !ok || size != 8448 {
		t.Fatalf("exact size = %d/%v", size, ok)
	}
	if string(data[:8]) != sidecarMagic || u16(data, 10) != 512 || u16(data, 12) != 64 || anyNonzero(data[512:PageSize]) {
		t.Fatal("sidecar header wire offsets do not match the exact format")
	}
}

func TestSidecarSelectionRejectsTornDisagreeingGappedAndIllegalPairs(t *testing.T) {
	data := testSidecarImage(testSidecarHeader(1, sidecarReady), testSidecarHeader(2, sidecarReady))
	data[PageSize+headerCRCOffset] ^= 1
	selected, err := selectSidecarHeader(data)
	if err != nil || selected.headerSeq != 1 {
		t.Fatalf("torn fallback = sequence %d, error %v", selected.headerSeq, err)
	}

	right := testSidecarHeader(1, sidecarInitializing)
	_, err = selectSidecarHeader(testSidecarImage(testSidecarHeader(1, sidecarReady), right))
	requireSidecarCode(t, err, sidecarErrEqualSequenceDisagreement)

	for _, pair := range []struct {
		left, right sidecarHeader
		code        sidecarErrorCode
	}{
		{testSidecarHeader(1, sidecarReady), testSidecarHeader(3, sidecarReady), sidecarErrNonAdjacentSequence},
		{testSidecarHeader(1, sidecarReady), testSidecarHeader(math.MaxUint64, sidecarReady), sidecarErrNonAdjacentSequence},
		{testSidecarHeader(1, sidecarInitializing), testSidecarHeader(2, sidecarReady), sidecarErrInvalidStateTransition},
		{testSidecarHeader(1, sidecarReady), testSidecarHeader(2, sidecarInitializing), sidecarErrInvalidStateTransition},
	} {
		_, err = selectSidecarHeader(testSidecarImage(pair.left, pair.right))
		requireSidecarCode(t, err, pair.code)
	}

	left := testSidecarHeader(math.MaxUint64, sidecarReady)
	right = left
	var block [PageSize]byte
	right.encodeInto(&block)
	data = make([]byte, headerRegionSize)
	left.encodeInto(&block)
	copy(data[:PageSize], block[:])
	right.headerSeq = 1
	right.encodeInto(&block)
	copy(data[PageSize:], block[:])
	_, err = selectSidecarHeader(data)
	requireSidecarCode(t, err, sidecarErrNonAdjacentSequence)
}

func TestSidecarImmutableFieldsAndOriginPhases(t *testing.T) {
	left := testSidecarHeader(1, sidecarInitializing)
	right := testSidecarHeader(2, sidecarReady)
	right.creationSecurityCommitment[0] ^= 1
	_, err := selectSidecarHeader(testSidecarImage(left, right))
	requireSidecarCode(t, err, sidecarErrImmutableIdentityMismatch)

	for _, origin := range []sidecarOrigin{sidecarOriginInitializeLive, sidecarOriginResetLiveCoordination} {
		left = testSidecarHeader(1, sidecarInitializing)
		left.origin = origin
		right = left
		right.headerSeq, right.state = 2, sidecarReady
		if _, err := selectSidecarHeader(testSidecarImage(left, right)); err != nil {
			t.Fatalf("origin %d initializing->ready: %v", origin, err)
		}
	}

	left = testSidecarHeader(1, sidecarInitializing)
	right = left
	right.headerSeq, right.state = 2, sidecarMainNamespaceAttempted
	if _, err := selectSidecarHeader(testSidecarImage(left, right)); err != nil {
		t.Fatalf("create initializing->attempted: %v", err)
	}
	left = right
	right = left
	right.headerSeq, right.state = 3, sidecarReady
	if _, err := selectSidecarHeader(testSidecarImage(left, right)); err != nil {
		t.Fatalf("create attempted->ready: %v", err)
	}
}

func TestSidecarHeaderFieldInvariantsFailClosed(t *testing.T) {
	valid := testSidecarHeader(1, sidecarReady)
	tests := []struct {
		name string
		edit func(*sidecarHeader)
		want sidecarHeaderProblem
	}{
		{"identity kind", func(h *sidecarHeader) { h.identityKind = 9 }, sidecarHeaderIdentityKind},
		{"identity padding", func(h *sidecarHeader) { h.mainIdentity[31] = 1 }, sidecarHeaderIdentityEncoding},
		{"capacity", func(h *sidecarHeader) { h.capacity = 0 }, sidecarHeaderCapacity},
		{"state", func(h *sidecarHeader) { h.state = 9 }, sidecarHeaderState},
		{"database id", func(h *sidecarHeader) { h.databaseID = [16]byte{} }, sidecarHeaderDatabaseID},
		{"identity collision", func(h *sidecarHeader) { h.sidecarIdentity = h.mainIdentity }, sidecarHeaderIdentityCollision},
		{"sidecar id", func(h *sidecarHeader) { h.sidecarID = [16]byte{} }, sidecarHeaderSidecarID},
		{"origin", func(h *sidecarHeader) { h.origin = 9 }, sidecarHeaderOrigin},
		{"origin state", func(h *sidecarHeader) {
			h.origin = sidecarOriginInitializeLive
			h.state = sidecarMainNamespaceAttempted
		}, sidecarHeaderState},
		{"transaction", func(h *sidecarHeader) { h.attemptedTxnID = 0 }, sidecarHeaderAttempt},
		{"nonce", func(h *sidecarHeader) { h.attemptedCommitNonce = [16]byte{} }, sidecarHeaderAttempt},
		{"short main", func(h *sidecarHeader) { h.attemptedMainBytes = 4096 }, sidecarHeaderAttempt},
		{"unaligned main", func(h *sidecarHeader) { h.attemptedMainBytes = 8193 }, sidecarHeaderAttempt},
		{"domain kind", func(h *sidecarHeader) { h.processDomainKind = 9 }, sidecarHeaderProcessDomain},
		{"domain padding", func(h *sidecarHeader) { h.processDomainToken[31] = 1 }, sidecarHeaderProcessDomain},
		{"basename kind", func(h *sidecarHeader) { h.basenameEncoding = 2 }, sidecarHeaderBasename},
		{"basename empty", func(h *sidecarHeader) { h.basenameLen = 0 }, sidecarHeaderBasename},
		{"security kind", func(h *sidecarHeader) { h.creationSecurityKind = 2 }, sidecarHeaderCreationSecurity},
		{"sequence", func(h *sidecarHeader) { h.headerSeq = 0 }, sidecarHeaderSequence},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := valid
			tc.edit(&h)
			var block [PageSize]byte
			h.encodeInto(&block)
			_, got := decodeSidecarHeader(block[:])
			if got != tc.want {
				t.Fatalf("problem = %d, want %d", got, tc.want)
			}
		})
	}

	var block [PageSize]byte
	valid.encodeInto(&block)
	for _, index := range []int{222, 258, 298, 332, 504, 512, PageSize - 1} {
		corrupt := block
		corrupt[index] = 1
		putU32(corrupt[:], headerCRCOffset, crc32cZeroed(corrupt[:], headerCRCOffset, 4))
		if _, got := decodeSidecarHeader(corrupt[:]); got != sidecarHeaderReserved {
			t.Fatalf("reserved byte %d problem = %d", index, got)
		}
	}
	block[headerCRCOffset] ^= 1
	if _, got := decodeSidecarHeader(block[:]); got != sidecarHeaderChecksum {
		t.Fatalf("checksum problem = %d", got)
	}
}

func TestSidecarWindowsCrossFieldRules(t *testing.T) {
	windows := testSidecarHeader(1, sidecarReady)
	windows.identityKind = localIdentityWindows
	windows.processDomainKind, windows.processDomainToken = processDomainHostGlobal, [32]byte{}
	windows.basenameEncoding, windows.basenameLen, windows.creationSecurityKind = 2, 8, 2
	var block [PageSize]byte
	windows.encodeInto(&block)
	if _, problem := decodeSidecarHeader(block[:]); problem != 0 {
		t.Fatalf("valid Windows header problem = %d", problem)
	}
	windows.processDomainKind = processDomainLinuxPIDNamespace
	windows.encodeInto(&block)
	if _, problem := decodeSidecarHeader(block[:]); problem != sidecarHeaderProcessDomain {
		t.Fatalf("Windows domain problem = %d", problem)
	}
	windows.processDomainKind = processDomainHostGlobal
	windows.basenameLen = 7
	windows.encodeInto(&block)
	if _, problem := decodeSidecarHeader(block[:]); problem != sidecarHeaderBasename {
		t.Fatalf("Windows odd basename problem = %d", problem)
	}
}

func TestReadySidecarRequiresExactCapacityDerivedSize(t *testing.T) {
	data := testSidecarImage(testSidecarHeader(1, sidecarReady), testSidecarHeader(2, sidecarReady))
	data = data[:len(data)-1]
	_, err := decodeReadySidecarImage(data)
	got := requireSidecarCode(t, err, sidecarErrWrongFileSize)
	if got.expected != 8448 || got.actual != 8447 {
		t.Fatalf("size error = expected %d actual %d", got.expected, got.actual)
	}

	notReady := testSidecarImage(testSidecarHeader(1, sidecarInitializing), testSidecarHeader(2, sidecarInitializing))
	_, err = decodeReadySidecarImage(notReady)
	requireSidecarCode(t, err, sidecarErrNotReady)
}

func TestSidecarSlotsFailClosedAndReaderZeroTransactionIsValid(t *testing.T) {
	host := slotHostLimits{processIDMax: math.MaxUint32, taskIDMax: math.MaxUint32}
	active := activeSlot{txnID: 0, processID: 42, processStart: 123, taskID: 7, nonce: [16]byte{9}}
	data := encodeActiveSlot(active)
	got, problem := decodeStableSlot(data[:], slotReader, host)
	if problem != 0 || !got.active || got.claim != active {
		t.Fatalf("reader slot = %#v problem %d", got, problem)
	}
	if _, problem = decodeStableSlot(data[:], slotWriter, host); problem != slotWriterTransactionZero {
		t.Fatalf("writer zero txn problem = %d", problem)
	}

	tests := []struct {
		name string
		edit func(*[sidecarSlotSize]byte)
		want slotProblem
	}{
		{"transition", func(b *[sidecarSlotSize]byte) { putU32(b[:], 0, 2) }, slotTransition},
		{"unknown state", func(b *[sidecarSlotSize]byte) { putU32(b[:], 0, 3) }, slotUnknownState},
		{"reserved", func(b *[sidecarSlotSize]byte) { b[4] = 1 }, slotReserved},
		{"pid zero", func(b *[sidecarSlotSize]byte) {
			putU64(b[:], 16, 0)
			putU32(b[:], slotCRCOffset, crc32cZeroed(b[:], slotCRCOffset, 4))
		}, slotProcessIDZero},
		{"pid too large", func(b *[sidecarSlotSize]byte) {
			putU64(b[:], 16, uint64(math.MaxUint32)+1)
			putU32(b[:], slotCRCOffset, crc32cZeroed(b[:], slotCRCOffset, 4))
		}, slotProcessIDUnrepresentable},
		{"task too large", func(b *[sidecarSlotSize]byte) {
			putU64(b[:], 32, uint64(math.MaxUint32)+1)
			putU32(b[:], slotCRCOffset, crc32cZeroed(b[:], slotCRCOffset, 4))
		}, slotTaskIDUnrepresentable},
		{"nonce zero", func(b *[sidecarSlotSize]byte) {
			clear(b[40:56])
			putU32(b[:], slotCRCOffset, crc32cZeroed(b[:], slotCRCOffset, 4))
		}, slotNonceZero},
		{"checksum", func(b *[sidecarSlotSize]byte) { b[24] ^= 1 }, slotChecksum},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			corrupt := data
			tc.edit(&corrupt)
			if _, got := decodeStableSlot(corrupt[:], slotReader, host); got != tc.want {
				t.Fatalf("problem = %d, want %d", got, tc.want)
			}
		})
	}
	var nonzeroFree [sidecarSlotSize]byte
	nonzeroFree[4] = 1
	if _, problem := decodeStableSlot(nonzeroFree[:], slotReader, host); problem != slotFreeNonzero {
		t.Fatalf("nonzero free problem = %d", problem)
	}
}

func TestReadySidecarInspectionBindsAllIdentityAndScansLastSlot(t *testing.T) {
	ready := testSidecarHeader(2, sidecarReady)
	data := testSidecarImage(testSidecarHeader(1, sidecarReady), ready)
	writer := activeSlot{txnID: 5, processID: 10, processStart: 11, taskID: 12, nonce: [16]byte{1}}
	registering := activeSlot{txnID: 0, processID: 20, processStart: 21, taskID: 22, nonce: [16]byte{2}}
	reader := activeSlot{txnID: 3, processID: 30, processStart: 31, taskID: 32, nonce: [16]byte{3}}
	putTestSlot(data, 0, writer)
	putTestSlot(data, 1, registering)
	putTestSlot(data, 3, reader)
	expected := testSidecarExpectations(ready)
	inspection, err := inspectReadySidecar(data, expected)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.writerActive || inspection.writer != writer || inspection.activeReaders != 2 ||
		inspection.registeringReaders != 1 || !inspection.oldestReaderTxnValid || inspection.oldestReaderTxn != 3 ||
		!inspection.lowestFreeSlotValid || inspection.lowestFreeSlot != 2 {
		t.Fatalf("inspection = %#v", inspection)
	}

	mismatches := []struct {
		name string
		edit func(*readySidecarExpectations)
		want readySidecarErrorCode
	}{
		{"database", func(e *readySidecarExpectations) { e.databaseID[0] ^= 1 }, readySidecarErrDatabaseIDMismatch},
		{"main identity", func(e *readySidecarExpectations) { e.mainIdentity[0] ^= 1 }, readySidecarErrMainIdentityMismatch},
		{"sidecar identity", func(e *readySidecarExpectations) { e.sidecarIdentity[0] ^= 1 }, readySidecarErrSidecarIdentityMismatch},
		{"domain", func(e *readySidecarExpectations) { e.processDomainToken[0] ^= 1 }, readySidecarErrProcessDomainMismatch},
		{"basename", func(e *readySidecarExpectations) { e.basenameCommitment[0] ^= 1 }, readySidecarErrBasenameMismatch},
	}
	for _, tc := range mismatches {
		t.Run(tc.name, func(t *testing.T) {
			wrong := expected
			tc.edit(&wrong)
			_, err := inspectReadySidecar(data, wrong)
			requireReadySidecarCode(t, err, tc.want)
		})
	}

	last := headerRegionSize + 3*int(sidecarSlotSize)
	putU32(data, last, 2)
	_, err = inspectReadySidecar(data, expected)
	gotErr := requireReadySidecarCode(t, err, readySidecarErrSlot)
	if gotErr.index != 3 || gotErr.problem != slotTransition {
		t.Fatalf("slot error = index %d problem %d", gotErr.index, gotErr.problem)
	}
}

func TestReadySidecarInspectionRejectsFutureReaderWithoutAllocatingPerSlot(t *testing.T) {
	ready := testSidecarHeader(2, sidecarReady)
	data := testSidecarImage(testSidecarHeader(1, sidecarReady), ready)
	expected := testSidecarExpectations(ready)
	putTestSlot(data, 3, activeSlot{txnID: 6, processID: 1, nonce: [16]byte{1}})
	inspection, err := inspectReadySidecar(data, expected)
	if err != nil {
		t.Fatalf("structural inspection rejected future reader before liveness: %v", err)
	}
	if !inspection.newestReaderTxnValid || inspection.newestReaderTxn != 6 {
		t.Fatalf("structural inspection did not expose future reader: %#v", inspection)
	}
	_, transactionErr := validateReadySidecarTransactions(data, ready, 5, expected.hostLimits)
	requireReadySidecarCode(t, transactionErr, readySidecarErrReaderTransactionFuture)

	clear(data[headerRegionSize:])
	putTestSlot(data, 0, activeSlot{txnID: 5, processID: 10, nonce: [16]byte{1}})
	putTestSlot(data, 1, activeSlot{txnID: 0, processID: 11, nonce: [16]byte{2}})
	putTestSlot(data, 2, activeSlot{txnID: 4, processID: 12, nonce: [16]byte{3}})
	putTestSlot(data, 3, activeSlot{txnID: 5, processID: 13, nonce: [16]byte{4}})
	allocations := testing.AllocsPerRun(100, func() {
		inspection, err := inspectReadySidecar(data, expected)
		if err != nil {
			panic(err)
		}
		validated, err := validateReadySidecarTransactions(data, ready, 5, expected.hostLimits)
		if err != nil {
			panic(err)
		}
		if validated.activeReaders != inspection.activeReaders {
			panic("refreshed inspection differs")
		}
	})
	if allocations != 0 {
		t.Fatalf("ready inspection allocations = %v, want 0", allocations)
	}
}

func TestDeadWriterWithPreviousTransactionIsExposedBeforeConsistencyCheck(t *testing.T) {
	ready := testSidecarHeader(2, sidecarReady)
	data := testSidecarImage(testSidecarHeader(1, sidecarReady), ready)
	staleWriter := activeSlot{txnID: 4, processID: 42, processStart: 99, nonce: [16]byte{8}}
	putTestSlot(data, 0, staleWriter)

	inspection, err := inspectReadySidecar(data, testSidecarExpectations(ready))
	if err != nil {
		t.Fatalf("structural inspection rejected stale writer before liveness: %v", err)
	}
	exposed, err := inspection.stableSlot(0)
	if err != nil || !exposed.active || exposed.claim != staleWriter {
		t.Fatalf("exposed writer = %#v, error %v", exposed, err)
	}
	_, transactionErr := validateReadySidecarTransactions(data, ready, 5, inspection.hostLimits)
	requireReadySidecarCode(t, transactionErr, readySidecarErrWriterTransactionMismatch)

	// This simulates the OS layer proving that exact owner dead and clearing it.
	clear(data[headerRegionSize : headerRegionSize+int(sidecarSlotSize)])
	inspection, err = validateReadySidecarTransactions(data, ready, 5, inspection.hostLimits)
	if err != nil {
		t.Fatalf("post-reap transaction validation: %v", err)
	}
	if inspection.writerActive {
		t.Fatal("post-reap inspection retained stale writer")
	}

	changed := ready
	changed.headerSeq++
	changed.encodeInto((*[PageSize]byte)(data[:PageSize]))
	if _, err := validateReadySidecarTransactions(data, ready, 5, inspection.hostLimits); err == nil || err.code != readySidecarErrHeaderChanged {
		t.Fatalf("changed header error = %v", err)
	}
}
