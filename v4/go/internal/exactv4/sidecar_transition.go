package exactv4

// Slot-transition provenance is process-local authority. The OS caller must
// retain the same sidecar operation lock continuously from arm until target or
// cleanup confirmation disarms the record. Releasing and reacquiring that lock
// while armed destroys the proof that an observed state-2 image is ours.

type deathProofKind uint8

const (
	deathProofPOSIXMissing deathProofKind = iota + 1
	deathProofPOSIXPIDReused
	deathProofWindowsSignaled
	deathProofWindowsPIDReused
)

type deathProof struct {
	kind         deathProofKind
	processID    uint64
	currentStart uint64
}

type slotTransitionSourceKind uint8

const (
	slotTransitionSourceZero slotTransitionSourceKind = iota + 1
	slotTransitionSourceOwnedActive
	slotTransitionSourceProvenDeadActive
)

type slotTransitionSource struct {
	kind   slotTransitionSourceKind
	active activeSlot
	proof  deathProof
}

func (source slotTransitionSource) activeClaim() (activeSlot, bool) {
	switch source.kind {
	case slotTransitionSourceOwnedActive, slotTransitionSourceProvenDeadActive:
		return source.active, true
	default:
		return activeSlot{}, false
	}
}

type slotTransitionKind uint8

const (
	slotTransitionClaim slotTransitionKind = iota + 1
	slotTransitionUpdate
	slotTransitionClear
)

type slotTransitionErrorCode uint8

const (
	slotTransitionErrNotArmed slotTransitionErrorCode = iota + 1
	slotTransitionErrHeaderNotReady
	slotTransitionErrSlotIndexOutOfRange
	slotTransitionErrSourceMalformed
	slotTransitionErrSourceMismatch
	slotTransitionErrInvalidTarget
	slotTransitionErrOwnerChanged
	slotTransitionErrInvalidDeathProof
	slotTransitionErrTargetReadbackMismatch
	slotTransitionErrCleanupConflict
	slotTransitionErrCleanupOwnerConflict
	slotTransitionErrCleanupReadbackMismatch
	slotTransitionErrPreparedConsumed
	slotTransitionErrProvenanceOccupied
)

type slotTransitionError struct {
	code    slotTransitionErrorCode
	problem slotProblem
}

func (e *slotTransitionError) Error() string { return "exact v4 sidecar slot transition failed" }

type preparedSlotTransition struct {
	noCopy      transitionNoCopy
	armState    *preparedSlotArmState
	header      sidecarHeader
	role        slotRole
	slotIndex   uint32
	kind        slotTransitionKind
	source      slotTransitionSource
	targetValid bool
	target      activeSlot
	targetImage [sidecarSlotSize]byte
}

type preparedSlotArmState struct{ consumed bool }

// transitionNoCopy makes accidental value copies of prepared or armed
// provenance visible to go vet. Both are created and used only through pointers;
// shared arm-state pointers also prevent copied values from arming independently.
type transitionNoCopy struct{}

func (*transitionNoCopy) Lock()   {}
func (*transitionNoCopy) Unlock() {}

type armedSlotTransition struct {
	noCopy      transitionNoCopy
	armState    *armedSlotState
	header      sidecarHeader
	role        slotRole
	slotIndex   uint32
	kind        slotTransitionKind
	source      slotTransitionSource
	targetValid bool
	target      activeSlot
	targetImage [sidecarSlotSize]byte
}

type armedSlotState struct{ armed bool }

type cleanupDisposition uint8

const (
	cleanupAlreadyAbsent cleanupDisposition = iota + 1
	cleanupClearOwned
)

type slotExecutionError struct {
	storage    error
	transition *slotTransitionError
}

func (e *slotExecutionError) Error() string { return "exact v4 sidecar slot I/O failed" }
func (e *slotExecutionError) Unwrap() error { return e.storage }

func (transition *preparedSlotTransition) headerValue() sidecarHeader { return transition.header }
func (transition *preparedSlotTransition) slotIndexValue() uint32     { return transition.slotIndex }

func (transition *preparedSlotTransition) confirmSource(
	observed *[sidecarSlotSize]byte,
	host slotHostLimits,
) *slotTransitionError {
	switch transition.source.kind {
	case slotTransitionSourceZero:
		stable, problem := decodeStableSlot(observed[:], transition.role, host)
		if problem != 0 {
			return &slotTransitionError{code: slotTransitionErrSourceMalformed, problem: problem}
		}
		if stable.active {
			return &slotTransitionError{code: slotTransitionErrSourceMismatch}
		}
		return nil
	case slotTransitionSourceOwnedActive, slotTransitionSourceProvenDeadActive:
		return requireExactTransitionActive(observed, transition.role, host, transition.source.active)
	default:
		return &slotTransitionError{code: slotTransitionErrSourceMismatch}
	}
}

func prepareSlotClaim(
	header sidecarHeader,
	role slotRole,
	slotIndex uint32,
	current *[sidecarSlotSize]byte,
	target activeSlot,
	host slotHostLimits,
) (*preparedSlotTransition, *slotTransitionError) {
	if err := validateTransitionHeaderSlot(header, role, slotIndex); err != nil {
		return nil, err
	}
	stable, problem := decodeStableSlot(current[:], role, host)
	if problem != 0 {
		return nil, &slotTransitionError{code: slotTransitionErrSourceMalformed, problem: problem}
	}
	if stable.active {
		return nil, &slotTransitionError{code: slotTransitionErrSourceMismatch}
	}
	targetImage, err := validatedTransitionActiveImage(target, role, host)
	if err != nil {
		return nil, err
	}
	return &preparedSlotTransition{
		armState: &preparedSlotArmState{},
		header:   header, role: role, slotIndex: slotIndex, kind: slotTransitionClaim,
		source:      slotTransitionSource{kind: slotTransitionSourceZero},
		targetValid: true, target: target, targetImage: targetImage,
	}, nil
}

func prepareSlotUpdate(
	header sidecarHeader,
	role slotRole,
	slotIndex uint32,
	current *[sidecarSlotSize]byte,
	owned activeSlot,
	target activeSlot,
	host slotHostLimits,
) (*preparedSlotTransition, *slotTransitionError) {
	if err := validateTransitionHeaderSlot(header, role, slotIndex); err != nil {
		return nil, err
	}
	if err := requireExactTransitionActive(current, role, host, owned); err != nil {
		return nil, err
	}
	if !sameSlotOwner(owned, target) {
		return nil, &slotTransitionError{code: slotTransitionErrOwnerChanged}
	}
	targetImage, err := validatedTransitionActiveImage(target, role, host)
	if err != nil {
		return nil, err
	}
	return &preparedSlotTransition{
		armState: &preparedSlotArmState{},
		header:   header, role: role, slotIndex: slotIndex, kind: slotTransitionUpdate,
		source:      slotTransitionSource{kind: slotTransitionSourceOwnedActive, active: owned},
		targetValid: true, target: target, targetImage: targetImage,
	}, nil
}

func prepareSlotClearOwned(
	header sidecarHeader,
	role slotRole,
	slotIndex uint32,
	current *[sidecarSlotSize]byte,
	owned activeSlot,
	host slotHostLimits,
) (*preparedSlotTransition, *slotTransitionError) {
	if err := validateTransitionHeaderSlot(header, role, slotIndex); err != nil {
		return nil, err
	}
	if err := requireExactTransitionActive(current, role, host, owned); err != nil {
		return nil, err
	}
	return prepareSlotClear(header, role, slotIndex, slotTransitionSource{
		kind: slotTransitionSourceOwnedActive, active: owned,
	}), nil
}

func prepareSlotClearProvenDead(
	header sidecarHeader,
	role slotRole,
	slotIndex uint32,
	current *[sidecarSlotSize]byte,
	active activeSlot,
	proof deathProof,
	host slotHostLimits,
) (*preparedSlotTransition, *slotTransitionError) {
	if err := validateTransitionHeaderSlot(header, role, slotIndex); err != nil {
		return nil, err
	}
	if err := requireExactTransitionActive(current, role, host, active); err != nil {
		return nil, err
	}
	if !validSlotDeathProof(header, active, proof) {
		return nil, &slotTransitionError{code: slotTransitionErrInvalidDeathProof}
	}
	return prepareSlotClear(header, role, slotIndex, slotTransitionSource{
		kind: slotTransitionSourceProvenDeadActive, active: active, proof: proof,
	}), nil
}

func prepareSlotClear(
	header sidecarHeader,
	role slotRole,
	slotIndex uint32,
	source slotTransitionSource,
) *preparedSlotTransition {
	return &preparedSlotTransition{
		armState: &preparedSlotArmState{}, header: header, role: role, slotIndex: slotIndex,
		kind: slotTransitionClear, source: source,
	}
}

// arm must be called immediately before the first attempt to write state 2,
// with the operation lock already held. That same lock remains held while the
// returned provenance is armed, including across retry cleanup.
func (transition *preparedSlotTransition) arm() (*armedSlotTransition, *slotTransitionError) {
	if transition.armState == nil || transition.armState.consumed {
		return nil, &slotTransitionError{code: slotTransitionErrPreparedConsumed}
	}
	transition.armState.consumed = true
	return &armedSlotTransition{
		armState: &armedSlotState{armed: true},
		header:   transition.header, role: transition.role, slotIndex: transition.slotIndex,
		kind: transition.kind, source: transition.source, targetValid: transition.targetValid,
		target: transition.target, targetImage: transition.targetImage,
	}, nil
}

// execute is the only legal state-2/body/final-state orchestration path. The
// caller supplies positional I/O bound to a continuously held exclusive
// operation lock. A post-arm failure leaves provenance populated; exact target
// readback disarms and clears it.
func (transition *preparedSlotTransition) execute(
	provenance **armedSlotTransition,
	write func(offset int, data []byte) error,
	read func(observed *[sidecarSlotSize]byte) error,
) *slotExecutionError {
	if provenance == nil || *provenance != nil {
		return &slotExecutionError{transition: &slotTransitionError{code: slotTransitionErrProvenanceOccupied}}
	}
	armed, transitionErr := transition.arm()
	if transitionErr != nil {
		return &slotExecutionError{transition: transitionErr}
	}
	*provenance = armed
	executionErr := armed.executeArmed(write, read)
	if executionErr == nil {
		*provenance = nil
	}
	return executionErr
}

func (transition *armedSlotTransition) executeArmed(
	write func(offset int, data []byte) error,
	read func(observed *[sidecarSlotSize]byte) error,
) *slotExecutionError {
	state2, transitionErr := transition.state2Bytes()
	if transitionErr != nil {
		return &slotExecutionError{transition: transitionErr}
	}
	if err := write(0, state2[:]); err != nil {
		return &slotExecutionError{storage: err}
	}
	body, transitionErr := transition.bodyBytes()
	if transitionErr != nil {
		return &slotExecutionError{transition: transitionErr}
	}
	if err := write(4, body[:]); err != nil {
		return &slotExecutionError{storage: err}
	}
	publish, transitionErr := transition.publishStateBytes()
	if transitionErr != nil {
		return &slotExecutionError{transition: transitionErr}
	}
	if err := write(0, publish[:]); err != nil {
		return &slotExecutionError{storage: err}
	}
	var observed [sidecarSlotSize]byte
	if err := read(&observed); err != nil {
		return &slotExecutionError{storage: err}
	}
	if transitionErr := transition.confirmTarget(&observed); transitionErr != nil {
		return &slotExecutionError{transition: transitionErr}
	}
	return nil
}

func (transition *armedSlotTransition) isArmed() bool {
	return transition.armState != nil && transition.armState.armed
}

func (transition *armedSlotTransition) headerValue() sidecarHeader { return transition.header }
func (transition *armedSlotTransition) roleValue() slotRole        { return transition.role }
func (transition *armedSlotTransition) slotIndexValue() uint32     { return transition.slotIndex }
func (transition *armedSlotTransition) kindValue() slotTransitionKind {
	return transition.kind
}
func (transition *armedSlotTransition) sourceValue() slotTransitionSource {
	return transition.source
}

// state2Bytes is the first positional write for every transition.
func (transition *armedSlotTransition) state2Bytes() ([4]byte, *slotTransitionError) {
	if !transition.isArmed() {
		return [4]byte{}, &slotTransitionError{code: slotTransitionErrNotArmed}
	}
	return [4]byte{2, 0, 0, 0}, nil
}

// bodyBytes is the second positional write and covers exact slot bytes [4,64).
func (transition *armedSlotTransition) bodyBytes() ([sidecarSlotSize - 4]byte, *slotTransitionError) {
	if !transition.isArmed() {
		return [sidecarSlotSize - 4]byte{}, &slotTransitionError{code: slotTransitionErrNotArmed}
	}
	var body [sidecarSlotSize - 4]byte
	copy(body[:], transition.targetImage[4:])
	return body, nil
}

// publishStateBytes is the final positional write and publishes state 1 or 0.
func (transition *armedSlotTransition) publishStateBytes() ([4]byte, *slotTransitionError) {
	if !transition.isArmed() {
		return [4]byte{}, &slotTransitionError{code: slotTransitionErrNotArmed}
	}
	if transition.targetValid {
		return [4]byte{1, 0, 0, 0}, nil
	}
	return [4]byte{}, nil
}

func (transition *armedSlotTransition) confirmTarget(observed *[sidecarSlotSize]byte) *slotTransitionError {
	if !transition.isArmed() {
		return &slotTransitionError{code: slotTransitionErrNotArmed}
	}
	if *observed != transition.targetImage {
		return &slotTransitionError{code: slotTransitionErrTargetReadbackMismatch}
	}
	transition.armState.armed = false
	return nil
}

func (transition *armedSlotTransition) cleanupDisposition(
	observed *[sidecarSlotSize]byte,
	host slotHostLimits,
) (cleanupDisposition, *slotTransitionError) {
	if !transition.isArmed() {
		return 0, &slotTransitionError{code: slotTransitionErrNotArmed}
	}
	if !anyNonzero(observed[:]) {
		return cleanupAlreadyAbsent, nil
	}
	if u32(observed[:], 0) == 2 {
		// Continuous ownership of the operation lock is the authority that
		// identifies this otherwise indistinguishable state-2 image as ours.
		return cleanupClearOwned, nil
	}
	stable, problem := decodeStableSlot(observed[:], transition.role, host)
	if problem != 0 {
		return 0, &slotTransitionError{code: slotTransitionErrCleanupConflict, problem: problem}
	}
	if !stable.active {
		return cleanupAlreadyAbsent, nil
	}
	expected, ok := transition.source.activeClaim()
	if !ok && transition.targetValid {
		expected, ok = transition.target, true
	}
	if !ok {
		return 0, &slotTransitionError{code: slotTransitionErrCleanupOwnerConflict}
	}
	if stable.claim.nonce != expected.nonce || !sameSlotOwner(stable.claim, expected) {
		return 0, &slotTransitionError{code: slotTransitionErrCleanupOwnerConflict}
	}
	source, sourceValid := transition.source.activeClaim()
	sourceTxnMatches := sourceValid && stable.claim.txnID == source.txnID
	targetTxnMatches := transition.targetValid && stable.claim.txnID == transition.target.txnID
	if !sourceTxnMatches && !targetTxnMatches {
		return 0, &slotTransitionError{code: slotTransitionErrCleanupOwnerConflict}
	}
	return cleanupClearOwned, nil
}

// Cleanup always uses state2Bytes, cleanupBodyBytes, then
// cleanupPublishStateBytes, independently of the interrupted target.
func (transition *armedSlotTransition) cleanupBodyBytes() ([sidecarSlotSize - 4]byte, *slotTransitionError) {
	if !transition.isArmed() {
		return [sidecarSlotSize - 4]byte{}, &slotTransitionError{code: slotTransitionErrNotArmed}
	}
	return [sidecarSlotSize - 4]byte{}, nil
}

func (transition *armedSlotTransition) cleanupPublishStateBytes() ([4]byte, *slotTransitionError) {
	if !transition.isArmed() {
		return [4]byte{}, &slotTransitionError{code: slotTransitionErrNotArmed}
	}
	return [4]byte{}, nil
}

func (transition *armedSlotTransition) confirmCleanup(
	observed *[sidecarSlotSize]byte,
	host slotHostLimits,
) (cleanupDisposition, *slotTransitionError) {
	disposition, err := transition.cleanupDisposition(observed, host)
	if err != nil {
		return 0, err
	}
	switch disposition {
	case cleanupAlreadyAbsent:
		transition.armState.armed = false
		return disposition, nil
	default:
		return 0, &slotTransitionError{code: slotTransitionErrCleanupReadbackMismatch}
	}
}

// retryCleanup resolves an interrupted transition to exact all-zero state. It
// retains armed authority on every error and disarms only after zero readback.
func (transition *armedSlotTransition) retryCleanup(
	host slotHostLimits,
	write func(offset int, data []byte) error,
	read func(observed *[sidecarSlotSize]byte) error,
) (cleanupDisposition, *slotExecutionError) {
	var observed [sidecarSlotSize]byte
	if err := read(&observed); err != nil {
		return 0, &slotExecutionError{storage: err}
	}
	disposition, transitionErr := transition.cleanupDisposition(&observed, host)
	if transitionErr != nil {
		return 0, &slotExecutionError{transition: transitionErr}
	}
	if disposition == cleanupAlreadyAbsent {
		disposition, transitionErr = transition.confirmCleanup(&observed, host)
		if transitionErr != nil {
			return 0, &slotExecutionError{transition: transitionErr}
		}
		return disposition, nil
	}
	state2, transitionErr := transition.state2Bytes()
	if transitionErr != nil {
		return 0, &slotExecutionError{transition: transitionErr}
	}
	if err := write(0, state2[:]); err != nil {
		return 0, &slotExecutionError{storage: err}
	}
	body, transitionErr := transition.cleanupBodyBytes()
	if transitionErr != nil {
		return 0, &slotExecutionError{transition: transitionErr}
	}
	if err := write(4, body[:]); err != nil {
		return 0, &slotExecutionError{storage: err}
	}
	publish, transitionErr := transition.cleanupPublishStateBytes()
	if transitionErr != nil {
		return 0, &slotExecutionError{transition: transitionErr}
	}
	if err := write(0, publish[:]); err != nil {
		return 0, &slotExecutionError{storage: err}
	}
	if err := read(&observed); err != nil {
		return 0, &slotExecutionError{storage: err}
	}
	disposition, transitionErr = transition.confirmCleanup(&observed, host)
	if transitionErr != nil {
		return 0, &slotExecutionError{transition: transitionErr}
	}
	return disposition, nil
}

func validateTransitionHeaderSlot(header sidecarHeader, role slotRole, slotIndex uint32) *slotTransitionError {
	if header.state != sidecarReady {
		return &slotTransitionError{code: slotTransitionErrHeaderNotReady}
	}
	valid := role == slotWriter && slotIndex == 0 || role == slotReader && slotIndex != 0 && slotIndex <= header.capacity
	if !valid {
		return &slotTransitionError{code: slotTransitionErrSlotIndexOutOfRange}
	}
	return nil
}

func requireExactTransitionActive(
	current *[sidecarSlotSize]byte,
	role slotRole,
	host slotHostLimits,
	expected activeSlot,
) *slotTransitionError {
	stable, problem := decodeStableSlot(current[:], role, host)
	if problem != 0 {
		return &slotTransitionError{code: slotTransitionErrSourceMalformed, problem: problem}
	}
	if !stable.active || stable.claim != expected {
		return &slotTransitionError{code: slotTransitionErrSourceMismatch}
	}
	return nil
}

func validatedTransitionActiveImage(
	active activeSlot,
	role slotRole,
	host slotHostLimits,
) ([sidecarSlotSize]byte, *slotTransitionError) {
	image := encodeActiveSlot(active)
	stable, problem := decodeStableSlot(image[:], role, host)
	if problem != 0 {
		return [sidecarSlotSize]byte{}, &slotTransitionError{code: slotTransitionErrInvalidTarget, problem: problem}
	}
	if !stable.active || stable.claim != active {
		return [sidecarSlotSize]byte{}, &slotTransitionError{code: slotTransitionErrSourceMismatch}
	}
	return image, nil
}

func sameSlotOwner(left, right activeSlot) bool {
	return left.processID == right.processID && left.processStart == right.processStart &&
		left.taskID == right.taskID && left.nonce == right.nonce
}

func validSlotDeathProof(header sidecarHeader, active activeSlot, proof deathProof) bool {
	windows := false
	reused := false
	switch proof.kind {
	case deathProofPOSIXMissing:
		if proof.currentStart != 0 {
			return false
		}
	case deathProofPOSIXPIDReused:
		reused = true
	case deathProofWindowsSignaled:
		windows = true
		if proof.currentStart != 0 {
			return false
		}
	case deathProofWindowsPIDReused:
		windows, reused = true, true
	default:
		return false
	}
	if windows != (header.identityKind == localIdentityWindows) || proof.processID != active.processID {
		return false
	}
	return !reused || active.processStart != 0 && proof.currentStart != 0 && active.processStart != proof.currentStart
}
