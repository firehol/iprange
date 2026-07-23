package exactv4

import "sync/atomic"

type privateWriterCleanupErrorCode uint8

const (
	privateWriterCleanupErrInvalidState privateWriterCleanupErrorCode = iota + 1
	privateWriterCleanupErrLedgerFull
	privateWriterCleanupErrExecutorRequired
	privateWriterCleanupErrOwnerMismatch
	privateWriterCleanupErrExecutionFailed
	privateWriterCleanupErrBusy
	privateWriterCleanupErrExecutorPanicked
)

type privateWriterCleanupError struct {
	code         privateWriterCleanupErrorCode
	obligationID uint64
	detail       uint64
	semanticCode privateWriterStableErrorCode
}

func (e privateWriterCleanupError) failed() bool { return e.code != 0 }

// privateWriterCleanupObligation is fixed descriptive data. Step 5 chunk 2B
// adds the complete artifact description without changing ledger ownership.
type privateWriterCleanupObligation struct {
	id       uint64
	artifact privateWriterCleanupArtifactEvidence
}

// privateWriterCleanupOwner is retry authority stored separately from its
// descriptive obligation. Filesystem-specific ownership is added later.
type privateWriterCleanupOwner struct {
	obligationID uint64
	authority    privateWriterCleanupRetryAuthority
	lastError    privateWriterCleanupError
	provenClean  bool
}

type privateWriterCleanupRetryAuthority struct {
	state uint64
}

type privateWriterCleanupExecutor func(
	obligation privateWriterCleanupObligation,
	authority *privateWriterCleanupRetryAuthority,
) privateWriterCleanupError

type privateWriterCleanupRetryResult struct {
	attempted   uint64
	provenClean uint64
	retained    uint64
	firstCause  privateWriterCleanupError
}

type privateWriterCleanupLedger struct {
	self        *privateWriterCleanupLedger
	obligations []privateWriterCleanupObligation
	owners      []privateWriterCleanupOwner
	length      int
	retrying    bool
	// Sealing covers supported mutators. Typed backing slices remain an
	// exclusive private-package ownership transfer and are never public.
	sealedBy *privateWriterTerminalResult
}

func initPrivateWriterCleanupLedger(
	ledger *privateWriterCleanupLedger,
	obligations []privateWriterCleanupObligation,
	owners []privateWriterCleanupOwner,
) privateWriterCleanupError {
	if ledger == nil || ledger.self != nil || ledger.obligations != nil ||
		ledger.owners != nil || ledger.length != 0 || ledger.retrying ||
		ledger.sealedBy != nil || len(obligations) != len(owners) {
		return privateWriterCleanupError{code: privateWriterCleanupErrInvalidState}
	}
	clear(obligations)
	clear(owners)
	*ledger = privateWriterCleanupLedger{
		self: ledger, obligations: obligations, owners: owners,
	}
	return privateWriterCleanupError{}
}

func (ledger *privateWriterCleanupLedger) append(
	obligation privateWriterCleanupObligation,
	owner privateWriterCleanupOwner,
) privateWriterCleanupError {
	if ledger != nil && ledger.retrying {
		return privateWriterCleanupError{
			code: privateWriterCleanupErrBusy, obligationID: obligation.id,
		}
	}
	if ledger != nil && ledger.sealedBy != nil {
		return privateWriterCleanupError{
			code: privateWriterCleanupErrBusy, obligationID: obligation.id,
		}
	}
	if !ledger.valid() || obligation.id == 0 || owner.obligationID != obligation.id ||
		owner.lastError != (privateWriterCleanupError{}) || owner.provenClean {
		return privateWriterCleanupError{
			code: privateWriterCleanupErrOwnerMismatch, obligationID: obligation.id,
		}
	}
	if ledger.length == len(ledger.obligations) {
		return privateWriterCleanupError{
			code: privateWriterCleanupErrLedgerFull, obligationID: obligation.id,
			detail: uint64(len(ledger.obligations)),
		}
	}
	ledger.obligations[ledger.length] = obligation
	ledger.owners[ledger.length] = owner
	ledger.length++
	return privateWriterCleanupError{}
}

// retry attempts every independent obligation. Only proven-clean pairs are
// removed; every failure is retained in original order with the exact first
// cause.
func (ledger *privateWriterCleanupLedger) retry(
	executor privateWriterCleanupExecutor,
) privateWriterCleanupRetryResult {
	if !ledger.valid() {
		return privateWriterCleanupRetryResult{
			firstCause: privateWriterCleanupError{code: privateWriterCleanupErrInvalidState},
		}
	}
	if ledger.retrying {
		return privateWriterCleanupRetryResult{
			retained:   uint64(ledger.length),
			firstCause: privateWriterCleanupError{code: privateWriterCleanupErrBusy},
		}
	}
	if ledger.sealedBy != nil {
		return privateWriterCleanupRetryResult{
			retained:   uint64(ledger.length),
			firstCause: privateWriterCleanupError{code: privateWriterCleanupErrBusy},
		}
	}
	if ledger.length == 0 {
		return privateWriterCleanupRetryResult{}
	}
	if executor == nil {
		return privateWriterCleanupRetryResult{
			retained:   uint64(ledger.length),
			firstCause: privateWriterCleanupError{code: privateWriterCleanupErrExecutorRequired},
		}
	}
	ledger.compactProvenClean()
	if ledger.length == 0 {
		return privateWriterCleanupRetryResult{}
	}

	ledger.retrying = true
	active := -1
	defer func() {
		if active >= 0 {
			owner := &ledger.owners[active]
			owner.provenClean = false
			owner.lastError = privateWriterCleanupError{
				code:         privateWriterCleanupErrExecutorPanicked,
				obligationID: ledger.obligations[active].id,
				semanticCode: privateWriterStableErrorPanic,
			}
		}
		ledger.retrying = false
	}()

	originalLength := ledger.length
	result := privateWriterCleanupRetryResult{}
	for read := 0; read < originalLength; read++ {
		obligation := ledger.obligations[read]
		owner := &ledger.owners[read]
		owner.lastError = privateWriterCleanupError{}
		owner.provenClean = false
		active = read
		result.attempted++
		cause := privateWriterCleanupError{}
		if obligation.id == 0 || owner.obligationID != obligation.id {
			cause = privateWriterCleanupError{
				code: privateWriterCleanupErrOwnerMismatch, obligationID: obligation.id,
			}
		} else {
			cause = executor(obligation, &owner.authority)
		}
		active = -1
		if cause.failed() &&
			(cause.obligationID != obligation.id ||
				cause.semanticCode != 0 && !cause.semanticCode.valid()) {
			cause = privateWriterCleanupError{
				code:         privateWriterCleanupErrOwnerMismatch,
				obligationID: obligation.id,
				detail:       cause.obligationID,
				semanticCode: privateWriterStableErrorWrongState,
			}
		}
		if !cause.failed() {
			result.provenClean++
			owner.provenClean = true
			continue
		}
		owner.lastError = cause
		if !result.firstCause.failed() {
			result.firstCause = cause
		}
	}
	ledger.compactProvenClean()
	result.retained = uint64(ledger.length)
	return result
}

func (ledger *privateWriterCleanupLedger) compactProvenClean() {
	originalLength := ledger.length
	write := 0
	for read := 0; read < originalLength; read++ {
		if ledger.owners[read].provenClean {
			continue
		}
		if write != read {
			ledger.obligations[write] = ledger.obligations[read]
			ledger.owners[write] = ledger.owners[read]
		}
		write++
	}
	clear(ledger.obligations[write:originalLength])
	clear(ledger.owners[write:originalLength])
	ledger.length = write
}

func (ledger *privateWriterCleanupLedger) valid() bool {
	if ledger == nil || ledger.self != ledger || len(ledger.obligations) != len(ledger.owners) ||
		ledger.length < 0 || ledger.length > len(ledger.obligations) {
		return false
	}
	for index := 0; index < ledger.length; index++ {
		if ledger.obligations[index].id == 0 ||
			ledger.owners[index].obligationID != ledger.obligations[index].id ||
			(ledger.owners[index].provenClean &&
				ledger.owners[index].lastError != (privateWriterCleanupError{})) ||
			(!ledger.owners[index].lastError.failed() &&
				ledger.owners[index].lastError.semanticCode != 0) ||
			(ledger.owners[index].lastError.semanticCode != 0 &&
				!ledger.owners[index].lastError.semanticCode.valid()) ||
			(ledger.owners[index].lastError.failed() &&
				ledger.owners[index].lastError.obligationID != ledger.obligations[index].id) {
			return false
		}
	}
	return true
}

type privateWriterCleanupState uint8

const (
	privateWriterCleanupClean privateWriterCleanupState = iota + 1
	privateWriterCleanupResiduePossible
)

type privateWriterCoordinationDisposition uint8

const (
	privateWriterCoordinationNone privateWriterCoordinationDisposition = iota + 1
	privateWriterCoordinationCleanupGuard
	privateWriterCoordinationRetainedReaderCloseRequired
	privateWriterCoordinationRetainedWriterCloseRequired
)

type privateWriterCoordinationErrorCode uint8

const (
	privateWriterCoordinationErrInvalidState privateWriterCoordinationErrorCode = iota + 1
	privateWriterCoordinationErrGuardBusy
	privateWriterCoordinationErrGuardAlreadyTaken
	privateWriterCoordinationErrStaleGuard
	privateWriterCoordinationErrIncarnationExhausted
	privateWriterCoordinationErrGuardAlreadyResolved
)

type privateWriterCoordinationError struct {
	code privateWriterCoordinationErrorCode
}

func (e privateWriterCoordinationError) failed() bool { return e.code != 0 }

const (
	privateWriterCleanupGuardVacant uint32 = iota
	privateWriterCleanupGuardAvailable
	privateWriterCleanupGuardTaken
	privateWriterCleanupGuardInitializing
	privateWriterCleanupGuardResolved
)

// privateWriterCleanupGuardState is caller-owned stable storage. The owning
// coordination value is address-bound; shallow copies cannot transfer authority.
type privateWriterCleanupGuardState struct {
	epoch  atomic.Uint64
	status atomic.Uint32
}

type privateWriterCoordinationCleanup struct {
	self        *privateWriterCoordinationCleanup
	disposition privateWriterCoordinationDisposition
	guard       *privateWriterCleanupGuardState
	guardEpoch  uint64
}

type privateWriterTakenCleanupGuard struct {
	self  *privateWriterTakenCleanupGuard
	state *privateWriterCleanupGuardState
	epoch uint64
}

var privateWriterCleanupGuardIncarnation atomic.Uint64

func initPrivateWriterNoCoordinationCleanup(
	cleanup *privateWriterCoordinationCleanup,
) privateWriterCoordinationError {
	if problem := privateWriterCoordinationMayInitialize(cleanup); problem.failed() {
		return problem
	}
	*cleanup = privateWriterCoordinationCleanup{
		self: cleanup, disposition: privateWriterCoordinationNone,
	}
	return privateWriterCoordinationError{}
}

func initPrivateWriterRetainedCoordinationCleanup(
	cleanup *privateWriterCoordinationCleanup,
	disposition privateWriterCoordinationDisposition,
) privateWriterCoordinationError {
	if problem := privateWriterCoordinationMayInitialize(cleanup); problem.failed() {
		return problem
	}
	if disposition != privateWriterCoordinationRetainedReaderCloseRequired &&
		disposition != privateWriterCoordinationRetainedWriterCloseRequired {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrInvalidState}
	}
	*cleanup = privateWriterCoordinationCleanup{self: cleanup, disposition: disposition}
	return privateWriterCoordinationError{}
}

func armPrivateWriterCleanupGuard(
	cleanup *privateWriterCoordinationCleanup,
	state *privateWriterCleanupGuardState,
) privateWriterCoordinationError {
	if cleanup == nil || state == nil {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrInvalidState}
	}
	if problem := privateWriterCoordinationMayInitialize(cleanup); problem.failed() {
		return problem
	}
	for {
		status := state.status.Load()
		if status == privateWriterCleanupGuardAvailable ||
			status == privateWriterCleanupGuardInitializing ||
			status == privateWriterCleanupGuardTaken {
			return privateWriterCoordinationError{code: privateWriterCoordinationErrGuardBusy}
		}
		if status != privateWriterCleanupGuardVacant &&
			status != privateWriterCleanupGuardResolved {
			return privateWriterCoordinationError{code: privateWriterCoordinationErrInvalidState}
		}
		epoch := state.epoch.Load()
		if status == privateWriterCleanupGuardVacant && epoch != 0 ||
			status == privateWriterCleanupGuardResolved && epoch == 0 {
			return privateWriterCoordinationError{code: privateWriterCoordinationErrInvalidState}
		}
		if state.status.CompareAndSwap(status, privateWriterCleanupGuardInitializing) {
			epoch, ok := reservePrivateWriterCleanupGuardIncarnation()
			if !ok {
				state.status.Store(status)
				return privateWriterCoordinationError{code: privateWriterCoordinationErrIncarnationExhausted}
			}
			state.epoch.Store(epoch)
			state.status.Store(privateWriterCleanupGuardAvailable)
			*cleanup = privateWriterCoordinationCleanup{
				self:        cleanup,
				disposition: privateWriterCoordinationCleanupGuard,
				guard:       state, guardEpoch: epoch,
			}
			return privateWriterCoordinationError{}
		}
	}
}

func privateWriterCoordinationMayInitialize(
	cleanup *privateWriterCoordinationCleanup,
) privateWriterCoordinationError {
	if cleanup == nil {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrInvalidState}
	}
	if *cleanup == (privateWriterCoordinationCleanup{}) {
		return privateWriterCoordinationError{}
	}
	if cleanup.self != cleanup {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrInvalidState}
	}
	if cleanup.disposition == privateWriterCoordinationCleanupGuard &&
		cleanup.guard != nil && cleanup.guardEpoch != 0 &&
		cleanup.guard.epoch.Load() == cleanup.guardEpoch &&
		(cleanup.guard.status.Load() == privateWriterCleanupGuardTaken ||
			cleanup.guard.status.Load() == privateWriterCleanupGuardResolved) {
		return privateWriterCoordinationError{}
	}
	return privateWriterCoordinationError{code: privateWriterCoordinationErrGuardBusy}
}

func reservePrivateWriterCleanupGuardIncarnation() (uint64, bool) {
	for {
		current := privateWriterCleanupGuardIncarnation.Load()
		if current == ^uint64(0) {
			return 0, false
		}
		if privateWriterCleanupGuardIncarnation.CompareAndSwap(current, current+1) {
			return current + 1, true
		}
	}
}

func (cleanup *privateWriterCoordinationCleanup) takeGuard(
	guard *privateWriterTakenCleanupGuard,
) privateWriterCoordinationError {
	if guard == nil || *guard != (privateWriterTakenCleanupGuard{}) {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrInvalidState}
	}
	if cleanup == nil || cleanup.self != cleanup ||
		cleanup.disposition != privateWriterCoordinationCleanupGuard ||
		cleanup.guard == nil || cleanup.guardEpoch == 0 {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrInvalidState}
	}
	if cleanup.guard.epoch.Load() != cleanup.guardEpoch {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrStaleGuard}
	}
	if cleanup.guard.status.CompareAndSwap(
		privateWriterCleanupGuardAvailable, privateWriterCleanupGuardTaken,
	) {
		*guard = privateWriterTakenCleanupGuard{
			self: guard, state: cleanup.guard, epoch: cleanup.guardEpoch,
		}
		return privateWriterCoordinationError{}
	}
	if cleanup.guard.epoch.Load() != cleanup.guardEpoch {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrStaleGuard}
	}
	if status := cleanup.guard.status.Load(); status == privateWriterCleanupGuardTaken ||
		status == privateWriterCleanupGuardResolved {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrGuardAlreadyTaken}
	}
	return privateWriterCoordinationError{code: privateWriterCoordinationErrGuardBusy}
}

func (cleanup *privateWriterCoordinationCleanup) destroy() privateWriterCoordinationError {
	if !cleanup.validShape() {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrInvalidState}
	}
	if cleanup.disposition != privateWriterCoordinationCleanupGuard {
		*cleanup = privateWriterCoordinationCleanup{}
		return privateWriterCoordinationError{}
	}
	if cleanup.guard.epoch.Load() != cleanup.guardEpoch {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrStaleGuard}
	}
	if cleanup.guard.status.Load() == privateWriterCleanupGuardAvailable {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrGuardBusy}
	}
	if status := cleanup.guard.status.Load(); status != privateWriterCleanupGuardTaken &&
		status != privateWriterCleanupGuardResolved {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrInvalidState}
	}
	*cleanup = privateWriterCoordinationCleanup{}
	return privateWriterCoordinationError{}
}

func (cleanup *privateWriterCoordinationCleanup) validShape() bool {
	if cleanup == nil || cleanup.self != cleanup {
		return false
	}
	switch cleanup.disposition {
	case privateWriterCoordinationNone,
		privateWriterCoordinationRetainedReaderCloseRequired,
		privateWriterCoordinationRetainedWriterCloseRequired:
		return cleanup.guard == nil && cleanup.guardEpoch == 0
	case privateWriterCoordinationCleanupGuard:
		return cleanup.guard != nil && cleanup.guardEpoch != 0
	default:
		return false
	}
}

func (guard *privateWriterTakenCleanupGuard) valid() bool {
	return guard != nil && guard.self == guard &&
		guard.state != nil && guard.epoch != 0 &&
		guard.state.epoch.Load() == guard.epoch &&
		guard.state.status.Load() == privateWriterCleanupGuardTaken
}

// resolve releases the caller-provided guard state only after the future
// platform executor has proved the transferred cleanup obligation complete.
func (guard *privateWriterTakenCleanupGuard) resolve() privateWriterCoordinationError {
	if !guard.valid() {
		if guard != nil && guard.self == guard && guard.state != nil &&
			guard.state.epoch.Load() == guard.epoch &&
			guard.state.status.Load() == privateWriterCleanupGuardResolved {
			return privateWriterCoordinationError{
				code: privateWriterCoordinationErrGuardAlreadyResolved,
			}
		}
		return privateWriterCoordinationError{code: privateWriterCoordinationErrStaleGuard}
	}
	if !guard.state.status.CompareAndSwap(
		privateWriterCleanupGuardTaken, privateWriterCleanupGuardResolved,
	) {
		return privateWriterCoordinationError{code: privateWriterCoordinationErrStaleGuard}
	}
	return privateWriterCoordinationError{}
}

func derivePrivateWriterCleanupState(
	ledger *privateWriterCleanupLedger,
	coordination *privateWriterCoordinationCleanup,
) privateWriterCleanupState {
	if ledger != nil && ledger.valid() && ledger.length == 0 &&
		coordination.validShape() &&
		coordination.disposition == privateWriterCoordinationNone {
		return privateWriterCleanupClean
	}
	return privateWriterCleanupResiduePossible
}
