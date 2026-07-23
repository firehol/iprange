package exactv4

import (
	"errors"
	"testing"
)

var transitionTestHost = slotHostLimits{processIDMax: ^uint64(0) >> 32, taskIDMax: ^uint64(0)}

func testTransitionHeader(kind localIdentityKind) sidecarHeader {
	header := testSidecarHeader(9, sidecarReady)
	header.capacity = 3
	header.identityKind = kind
	if kind == localIdentityWindows {
		header.basenameEncoding = uint16(localIdentityWindows)
		header.basenameLen = 2
		header.creationSecurityKind = uint16(localIdentityWindows)
	}
	return header
}

func testTransitionActive(txnID uint64, nonce byte) activeSlot {
	return activeSlot{
		txnID: txnID, processID: 123, processStart: 456, taskID: 789, nonce: [16]byte{nonce},
	}
}

func requireSlotTransitionError(t *testing.T, err *slotTransitionError, want slotTransitionErrorCode) *slotTransitionError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected slot transition error %d", want)
	}
	if err.code != want {
		t.Fatalf("slot transition error = %d, want %d", err.code, want)
	}
	return err
}

func armTestTransition(t *testing.T, prepared *preparedSlotTransition) *armedSlotTransition {
	t.Helper()
	armed, err := prepared.arm()
	if err != nil {
		t.Fatal(err)
	}
	return armed
}

func applyTestTransitionTarget(t *testing.T, transition *armedSlotTransition) [sidecarSlotSize]byte {
	t.Helper()
	state2, err := transition.state2Bytes()
	if err != nil {
		t.Fatal(err)
	}
	body, err := transition.bodyBytes()
	if err != nil {
		t.Fatal(err)
	}
	publish, err := transition.publishStateBytes()
	if err != nil {
		t.Fatal(err)
	}
	var raw [sidecarSlotSize]byte
	copy(raw[:4], state2[:])
	copy(raw[4:], body[:])
	copy(raw[:4], publish[:])
	return raw
}

func TestSlotClaimArmsOnceOrdersWritesAndConfirmsExactTarget(t *testing.T) {
	header := testTransitionHeader(localIdentityPOSIX)
	target := testTransitionActive(0, 1)
	var zero [sidecarSlotSize]byte
	prepared, err := prepareSlotClaim(header, slotReader, 1, &zero, target, transitionTestHost)
	if err != nil {
		t.Fatal(err)
	}
	preparedAlias := prepared
	armed := armTestTransition(t, prepared)
	if !armed.isArmed() || armed.headerValue() != header || armed.roleValue() != slotReader ||
		armed.slotIndexValue() != 1 || armed.kindValue() != slotTransitionClaim ||
		armed.sourceValue().kind != slotTransitionSourceZero {
		t.Fatalf("armed provenance = %#v", armed)
	}
	if second, err := preparedAlias.arm(); second != nil || err == nil || err.code != slotTransitionErrPreparedConsumed {
		t.Fatalf("second arm = %#v, error %#v", second, err)
	}

	targetImage := applyTestTransitionTarget(t, armed)
	stable, problem := decodeStableSlot(targetImage[:], slotReader, transitionTestHost)
	if problem != 0 || !stable.active || stable.claim != target {
		t.Fatalf("published target = %#v, problem %d", stable, problem)
	}
	wrong := targetImage
	wrong[24] ^= 1
	requireSlotTransitionError(t, armed.confirmTarget(&wrong), slotTransitionErrTargetReadbackMismatch)
	if !armed.isArmed() {
		t.Fatal("wrong readback disarmed provenance")
	}
	if err := armed.confirmTarget(&targetImage); err != nil {
		t.Fatal(err)
	}
	if armed.isArmed() {
		t.Fatal("exact readback did not disarm provenance")
	}

	requireSlotTransitionError(t, armed.confirmTarget(&targetImage), slotTransitionErrNotArmed)
	if _, err := armed.state2Bytes(); err == nil || err.code != slotTransitionErrNotArmed {
		t.Fatalf("disarmed state2 error = %#v", err)
	}
	if _, err := armed.bodyBytes(); err == nil || err.code != slotTransitionErrNotArmed {
		t.Fatalf("disarmed body error = %#v", err)
	}
	if _, err := armed.publishStateBytes(); err == nil || err.code != slotTransitionErrNotArmed {
		t.Fatalf("disarmed publish error = %#v", err)
	}
	if _, err := armed.cleanupBodyBytes(); err == nil || err.code != slotTransitionErrNotArmed {
		t.Fatalf("disarmed cleanup body error = %#v", err)
	}
	if _, err := armed.cleanupPublishStateBytes(); err == nil || err.code != slotTransitionErrNotArmed {
		t.Fatalf("disarmed cleanup publish error = %#v", err)
	}
	if _, err := armed.cleanupDisposition(&zero, transitionTestHost); err == nil || err.code != slotTransitionErrNotArmed {
		t.Fatalf("disarmed cleanup disposition error = %#v", err)
	}
	if _, err := armed.confirmCleanup(&zero, transitionTestHost); err == nil || err.code != slotTransitionErrNotArmed {
		t.Fatalf("disarmed cleanup confirmation error = %#v", err)
	}
}

func TestSlotPreparationRejectsHeaderRoleIndexSourceAndTargetViolations(t *testing.T) {
	header := testTransitionHeader(localIdentityPOSIX)
	validTarget := testTransitionActive(0, 1)
	var zero [sidecarSlotSize]byte

	notReady := header
	notReady.state = sidecarInitializing
	_, err := prepareSlotClaim(notReady, slotReader, 1, &zero, validTarget, transitionTestHost)
	requireSlotTransitionError(t, err, slotTransitionErrHeaderNotReady)

	for _, tc := range []struct {
		role  slotRole
		index uint32
	}{
		{slotWriter, 1}, {slotReader, 0}, {slotReader, 4}, {0, 1},
	} {
		_, err = prepareSlotClaim(header, tc.role, tc.index, &zero, validTarget, transitionTestHost)
		requireSlotTransitionError(t, err, slotTransitionErrSlotIndexOutOfRange)
	}

	zeroNonce := validTarget
	zeroNonce.nonce = [16]byte{}
	prepared, err := prepareSlotClaim(header, slotReader, 1, &zero, zeroNonce, transitionTestHost)
	if prepared != nil {
		t.Fatal("zero-nonce target was prepared")
	}
	got := requireSlotTransitionError(t, err, slotTransitionErrInvalidTarget)
	if got.problem != slotNonceZero {
		t.Fatalf("zero-nonce problem = %d", got.problem)
	}

	writerZeroTxn := validTarget
	_, err = prepareSlotClaim(header, slotWriter, 0, &zero, writerZeroTxn, transitionTestHost)
	got = requireSlotTransitionError(t, err, slotTransitionErrInvalidTarget)
	if got.problem != slotWriterTransactionZero {
		t.Fatalf("writer target problem = %d", got.problem)
	}

	occupied := encodeActiveSlot(testTransitionActive(3, 3))
	_, err = prepareSlotClaim(header, slotReader, 1, &occupied, validTarget, transitionTestHost)
	requireSlotTransitionError(t, err, slotTransitionErrSourceMismatch)

	transitioning := occupied
	putU32(transitioning[:], 0, 2)
	_, err = prepareSlotClaim(header, slotReader, 1, &transitioning, validTarget, transitionTestHost)
	got = requireSlotTransitionError(t, err, slotTransitionErrSourceMalformed)
	if got.problem != slotTransition {
		t.Fatalf("transition source problem = %d", got.problem)
	}

	limitedHost := slotHostLimits{processIDMax: 100, taskIDMax: transitionTestHost.taskIDMax}
	_, err = prepareSlotClaim(header, slotReader, 1, &zero, validTarget, limitedHost)
	got = requireSlotTransitionError(t, err, slotTransitionErrInvalidTarget)
	if got.problem != slotProcessIDUnrepresentable {
		t.Fatalf("host-limit target problem = %d", got.problem)
	}
}

func TestSlotUpdateRequiresExactSourceAndPreservesOwner(t *testing.T) {
	header := testTransitionHeader(localIdentityPOSIX)
	old := testTransitionActive(0, 1)
	target := testTransitionActive(11, 1)
	oldImage := encodeActiveSlot(old)
	prepared, err := prepareSlotUpdate(header, slotReader, 2, &oldImage, old, target, transitionTestHost)
	if err != nil || prepared.kind != slotTransitionUpdate || prepared.source.active != old || prepared.target != target {
		t.Fatalf("prepared update = %#v, error %v", prepared, err)
	}

	changedOwner := target
	changedOwner.processStart++
	_, err = prepareSlotUpdate(header, slotReader, 2, &oldImage, old, changedOwner, transitionTestHost)
	requireSlotTransitionError(t, err, slotTransitionErrOwnerChanged)

	wrongSource := encodeActiveSlot(testTransitionActive(1, 1))
	_, err = prepareSlotUpdate(header, slotReader, 2, &wrongSource, old, target, transitionTestHost)
	requireSlotTransitionError(t, err, slotTransitionErrSourceMismatch)

	corruptSource := oldImage
	corruptSource[60] ^= 1
	_, err = prepareSlotUpdate(header, slotReader, 2, &corruptSource, old, target, transitionTestHost)
	got := requireSlotTransitionError(t, err, slotTransitionErrSourceMalformed)
	if got.problem != slotChecksum {
		t.Fatalf("corrupt source problem = %d", got.problem)
	}
}

func TestArmedCleanupAcceptsOnlyOwnedInterruptedStates(t *testing.T) {
	header := testTransitionHeader(localIdentityPOSIX)
	old := testTransitionActive(0, 1)
	target := testTransitionActive(11, 1)
	oldImage := encodeActiveSlot(old)
	prepared, err := prepareSlotUpdate(header, slotReader, 1, &oldImage, old, target, transitionTestHost)
	if err != nil {
		t.Fatal(err)
	}
	armed := armTestTransition(t, prepared)
	var zero [sidecarSlotSize]byte
	if disposition, err := armed.cleanupDisposition(&zero, transitionTestHost); err != nil || disposition != cleanupAlreadyAbsent {
		t.Fatalf("zero cleanup = %d, %v", disposition, err)
	}
	state2 := oldImage
	putU32(state2[:], 0, 2)
	if disposition, err := armed.cleanupDisposition(&state2, transitionTestHost); err != nil || disposition != cleanupClearOwned {
		t.Fatalf("state2 cleanup = %d, %v", disposition, err)
	}
	for name, observed := range map[string][sidecarSlotSize]byte{
		"old":         oldImage,
		"prospective": encodeActiveSlot(target),
	} {
		disposition, err := armed.cleanupDisposition(&observed, transitionTestHost)
		if err != nil || disposition != cleanupClearOwned {
			t.Fatalf("%s cleanup = %d, %v", name, disposition, err)
		}
	}

	differentNonce := encodeActiveSlot(testTransitionActive(11, 2))
	_, err = armed.cleanupDisposition(&differentNonce, transitionTestHost)
	requireSlotTransitionError(t, err, slotTransitionErrCleanupOwnerConflict)

	foreignOwner := target
	foreignOwner.processID++
	foreignImage := encodeActiveSlot(foreignOwner)
	_, err = armed.cleanupDisposition(&foreignImage, transitionTestHost)
	requireSlotTransitionError(t, err, slotTransitionErrCleanupOwnerConflict)

	wrongTxn := target
	wrongTxn.txnID++
	wrongTxnImage := encodeActiveSlot(wrongTxn)
	_, err = armed.cleanupDisposition(&wrongTxnImage, transitionTestHost)
	requireSlotTransitionError(t, err, slotTransitionErrCleanupOwnerConflict)

	malformed := oldImage
	malformed[60] ^= 1
	_, err = armed.cleanupDisposition(&malformed, transitionTestHost)
	got := requireSlotTransitionError(t, err, slotTransitionErrCleanupConflict)
	if got.problem != slotChecksum {
		t.Fatalf("malformed cleanup problem = %d", got.problem)
	}

	if _, err := armed.confirmCleanup(&oldImage, transitionTestHost); err == nil || err.code != slotTransitionErrCleanupReadbackMismatch {
		t.Fatalf("owned readback error = %#v", err)
	}
	if !armed.isArmed() {
		t.Fatal("owned readback disarmed transition")
	}
	if disposition, err := armed.confirmCleanup(&zero, transitionTestHost); err != nil || disposition != cleanupAlreadyAbsent {
		t.Fatalf("zero confirmation = %d, %v", disposition, err)
	}

	claimTarget := testTransitionActive(0, 7)
	claimPrepared, err := prepareSlotClaim(header, slotReader, 1, &zero, claimTarget, transitionTestHost)
	if err != nil {
		t.Fatal(err)
	}
	claimArmed := armTestTransition(t, claimPrepared)
	claimImage := encodeActiveSlot(claimTarget)
	if disposition, err := claimArmed.cleanupDisposition(&claimImage, transitionTestHost); err != nil || disposition != cleanupClearOwned {
		t.Fatalf("claim target cleanup = %d, %v", disposition, err)
	}
}

func TestOwnedClearCleanupOrderingPublishesExactZero(t *testing.T) {
	header := testTransitionHeader(localIdentityPOSIX)
	owned := testTransitionActive(5, 1)
	ownedImage := encodeActiveSlot(owned)
	prepared, err := prepareSlotClearOwned(header, slotReader, 1, &ownedImage, owned, transitionTestHost)
	if err != nil || prepared.kind != slotTransitionClear || prepared.targetValid {
		t.Fatalf("prepared clear = %#v, error %v", prepared, err)
	}
	armed := armTestTransition(t, prepared)
	if disposition, err := armed.cleanupDisposition(&ownedImage, transitionTestHost); err != nil || disposition != cleanupClearOwned {
		t.Fatalf("owned clear source cleanup = %d, %v", disposition, err)
	}
	state2, err := armed.state2Bytes()
	if err != nil {
		t.Fatal(err)
	}
	body, err := armed.cleanupBodyBytes()
	if err != nil {
		t.Fatal(err)
	}
	publish, err := armed.cleanupPublishStateBytes()
	if err != nil {
		t.Fatal(err)
	}
	raw := ownedImage
	copy(raw[:4], state2[:])
	copy(raw[4:], body[:])
	copy(raw[:4], publish[:])
	if raw != [sidecarSlotSize]byte{} {
		t.Fatalf("cleanup target = %x", raw)
	}
	if err := armed.confirmTarget(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := armed.cleanupBodyBytes(); err == nil || err.code != slotTransitionErrNotArmed {
		t.Fatalf("disarmed cleanup body error = %#v", err)
	}
}

func TestProvenDeadClearRequiresExactPlatformProofAndSource(t *testing.T) {
	active := testTransitionActive(8, 1)
	image := encodeActiveSlot(active)
	posix := testTransitionHeader(localIdentityPOSIX)
	validPOSIX := []deathProof{
		{kind: deathProofPOSIXMissing, processID: active.processID},
		{kind: deathProofPOSIXPIDReused, processID: active.processID, currentStart: 999},
	}
	for index, proof := range validPOSIX {
		prepared, err := prepareSlotClearProvenDead(posix, slotWriter, 0, &image, active, proof, transitionTestHost)
		if err != nil || prepared.source.kind != slotTransitionSourceProvenDeadActive ||
			prepared.source.active != active || prepared.source.proof != proof {
			t.Fatalf("POSIX proof %#v = %#v, %v", proof, prepared, err)
		}
		if index == 0 {
			armed := armTestTransition(t, prepared)
			if disposition, err := armed.cleanupDisposition(&image, transitionTestHost); err != nil || disposition != cleanupClearOwned {
				t.Fatalf("proven-dead cleanup = %d, %v", disposition, err)
			}
		}
	}
	invalidPOSIX := []deathProof{
		{kind: deathProofPOSIXMissing, processID: active.processID + 1},
		{kind: deathProofPOSIXMissing, processID: active.processID, currentStart: 1},
		{kind: deathProofPOSIXPIDReused, processID: active.processID, currentStart: active.processStart},
		{kind: deathProofPOSIXPIDReused, processID: active.processID, currentStart: 0},
		{kind: deathProofWindowsSignaled, processID: active.processID},
	}
	for _, proof := range invalidPOSIX {
		_, err := prepareSlotClearProvenDead(posix, slotWriter, 0, &image, active, proof, transitionTestHost)
		requireSlotTransitionError(t, err, slotTransitionErrInvalidDeathProof)
	}

	windows := testTransitionHeader(localIdentityWindows)
	for _, proof := range []deathProof{
		{kind: deathProofWindowsSignaled, processID: active.processID},
		{kind: deathProofWindowsPIDReused, processID: active.processID, currentStart: 999},
	} {
		if _, err := prepareSlotClearProvenDead(windows, slotWriter, 0, &image, active, proof, transitionTestHost); err != nil {
			t.Fatalf("Windows proof %#v: %v", proof, err)
		}
	}
	_, err := prepareSlotClearProvenDead(windows, slotWriter, 0, &image, active,
		deathProof{kind: deathProofPOSIXMissing, processID: active.processID}, transitionTestHost)
	requireSlotTransitionError(t, err, slotTransitionErrInvalidDeathProof)

	wrong := testTransitionActive(8, 2)
	_, err = prepareSlotClearProvenDead(posix, slotWriter, 0, &image, wrong,
		deathProof{kind: deathProofPOSIXMissing, processID: wrong.processID}, transitionTestHost)
	requireSlotTransitionError(t, err, slotTransitionErrSourceMismatch)

	zeroStart := active
	zeroStart.processStart = 0
	zeroStartImage := encodeActiveSlot(zeroStart)
	_, err = prepareSlotClearProvenDead(posix, slotWriter, 0, &zeroStartImage, zeroStart,
		deathProof{kind: deathProofPOSIXPIDReused, processID: zeroStart.processID, currentStart: 999}, transitionTestHost)
	requireSlotTransitionError(t, err, slotTransitionErrInvalidDeathProof)
}

func TestUnarmedState2RemainsMalformedToOrdinaryInspection(t *testing.T) {
	raw := encodeActiveSlot(testTransitionActive(1, 1))
	putU32(raw[:], 0, 2)
	if _, problem := decodeStableSlot(raw[:], slotReader, transitionTestHost); problem != slotTransition {
		t.Fatalf("state2 problem = %d", problem)
	}
}

func TestSlotExecutorEnforcesWriteOrderAndExactReadback(t *testing.T) {
	header := testTransitionHeader(localIdentityPOSIX)
	target := testTransitionActive(0, 1)
	var slot [sidecarSlotSize]byte
	prepared, err := prepareSlotClaim(header, slotReader, 1, &slot, target, transitionTestHost)
	if err != nil {
		t.Fatal(err)
	}
	type writeRecord struct {
		offset int
		data   []byte
	}
	var writes []writeRecord
	var provenance *armedSlotTransition
	executionErr := prepared.execute(
		&provenance,
		func(offset int, data []byte) error {
			writes = append(writes, writeRecord{offset: offset, data: append([]byte(nil), data...)})
			copy(slot[offset:offset+len(data)], data)
			return nil
		},
		func(observed *[sidecarSlotSize]byte) error {
			*observed = slot
			return nil
		},
	)
	if executionErr != nil {
		t.Fatal(executionErr)
	}
	if provenance != nil {
		t.Fatal("successful execution retained provenance")
	}
	if len(writes) != 3 || writes[0].offset != 0 || u32(writes[0].data, 0) != 2 ||
		writes[1].offset != 4 || len(writes[1].data) != int(sidecarSlotSize)-4 ||
		writes[2].offset != 0 || u32(writes[2].data, 0) != 1 {
		t.Fatalf("write sequence = %#v", writes)
	}
	stable, problem := decodeStableSlot(slot[:], slotReader, transitionTestHost)
	if problem != 0 || !stable.active || stable.claim != target {
		t.Fatalf("published target = %#v, problem %d", stable, problem)
	}
}

func TestSlotExecutorFailureRetainsAuthorityUntilOrderedCleanup(t *testing.T) {
	header := testTransitionHeader(localIdentityPOSIX)
	var slot [sidecarSlotSize]byte
	prepared, err := prepareSlotClaim(header, slotReader, 2, &slot, testTransitionActive(0, 1), transitionTestHost)
	if err != nil {
		t.Fatal(err)
	}
	bodyFailure := errors.New("body failure")
	writeCount := 0
	var provenance *armedSlotTransition
	executionErr := prepared.execute(
		&provenance,
		func(offset int, data []byte) error {
			writeCount++
			if writeCount == 2 {
				return bodyFailure
			}
			copy(slot[offset:offset+len(data)], data)
			return nil
		},
		func(observed *[sidecarSlotSize]byte) error { *observed = slot; return nil },
	)
	if executionErr == nil || !errors.Is(executionErr, bodyFailure) || provenance == nil || !provenance.isArmed() {
		t.Fatalf("interrupted execution = (%#v, %#v)", executionErr, provenance)
	}
	if u32(slot[:], 0) != 2 {
		t.Fatalf("interrupted state = %d, want 2", u32(slot[:], 0))
	}
	var cleanupOffsets []int
	disposition, cleanupErr := provenance.retryCleanup(
		transitionTestHost,
		func(offset int, data []byte) error {
			cleanupOffsets = append(cleanupOffsets, offset)
			copy(slot[offset:offset+len(data)], data)
			return nil
		},
		func(observed *[sidecarSlotSize]byte) error { *observed = slot; return nil },
	)
	if cleanupErr != nil || disposition != cleanupAlreadyAbsent || provenance.isArmed() {
		t.Fatalf("cleanup = (%d, %#v), armed=%v", disposition, cleanupErr, provenance.isArmed())
	}
	if slot != [sidecarSlotSize]byte{} || len(cleanupOffsets) != 3 ||
		cleanupOffsets[0] != 0 || cleanupOffsets[1] != 4 || cleanupOffsets[2] != 0 {
		t.Fatalf("cleanup result = %x, offsets=%v", slot, cleanupOffsets)
	}
}

func TestSlotExecutorReadbackMismatchAndOccupiedProvenanceFailClosed(t *testing.T) {
	header := testTransitionHeader(localIdentityPOSIX)
	var zero [sidecarSlotSize]byte
	prepared, err := prepareSlotClaim(header, slotReader, 1, &zero, testTransitionActive(0, 1), transitionTestHost)
	if err != nil {
		t.Fatal(err)
	}
	var provenance *armedSlotTransition
	executionErr := prepared.execute(
		&provenance,
		func(int, []byte) error { return nil },
		func(observed *[sidecarSlotSize]byte) error { clear(observed[:]); return nil },
	)
	if executionErr == nil || executionErr.transition == nil ||
		executionErr.transition.code != slotTransitionErrTargetReadbackMismatch ||
		provenance == nil || !provenance.isArmed() {
		t.Fatalf("readback mismatch = (%#v, %#v)", executionErr, provenance)
	}

	second, err := prepareSlotClaim(header, slotReader, 1, &zero, testTransitionActive(0, 2), transitionTestHost)
	if err != nil {
		t.Fatal(err)
	}
	executionErr = second.execute(&provenance, func(int, []byte) error { return nil }, func(*[sidecarSlotSize]byte) error { return nil })
	if executionErr == nil || executionErr.transition == nil ||
		executionErr.transition.code != slotTransitionErrProvenanceOccupied {
		t.Fatalf("occupied provenance = %#v", executionErr)
	}
	if armed, err := second.arm(); err != nil || armed == nil {
		t.Fatalf("pre-arm rejection consumed prepared transition: (%#v, %#v)", armed, err)
	}
}
