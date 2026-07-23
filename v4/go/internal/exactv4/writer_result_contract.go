package exactv4

type privateWriterStableErrorCode uint32

const (
	privateWriterStableErrorWrongState                 privateWriterStableErrorCode = 11
	privateWriterStableErrorInsufficientResourceBudget privateWriterStableErrorCode = 24
	privateWriterStableErrorIO                         privateWriterStableErrorCode = 31
	privateWriterStableErrorCleanupConflict            privateWriterStableErrorCode = 42
	privateWriterStableErrorPanic                      privateWriterStableErrorCode = 57
	privateWriterStableErrorCleanupInProgress          privateWriterStableErrorCode = 64
)

func (code privateWriterStableErrorCode) valid() bool {
	return code >= 1 && code <= privateWriterStableErrorCleanupInProgress
}

type privateWriterOptionalStableError struct {
	present bool
	code    privateWriterStableErrorCode
}

func (problem privateWriterOptionalStableError) valid() bool {
	if !problem.present {
		return problem.code == 0
	}
	return problem.code.valid()
}

type privateWriterResultContractErrorCode uint8

const (
	privateWriterResultErrInvalidState privateWriterResultContractErrorCode = iota + 1
	privateWriterResultErrInvalidIdentity
	privateWriterResultErrInvalidBasename
	privateWriterResultErrBasenameArenaFull
	privateWriterResultErrArithmeticOverflow
	privateWriterResultErrInvalidArtifact
	privateWriterResultErrCleanupErrorRequired
	privateWriterResultErrInvalidCommitAttempt
	privateWriterResultErrInvalidCommitResult
	privateWriterResultErrInvalidAbortResult
	privateWriterResultErrArtifactOutOfBounds
	privateWriterResultErrHandleBusy
	privateWriterResultErrCleanupGuardUnavailable
)

type privateWriterResultContractError struct {
	code   privateWriterResultContractErrorCode
	index  uint64
	needed uint64
	actual uint64
}

func (problem privateWriterResultContractError) failed() bool {
	return problem.code != 0
}

type privateWriterLocalIdentity struct {
	kind  localIdentityKind
	value [32]byte
}

func (identity privateWriterLocalIdentity) valid() bool {
	return validLocalIdentity(identity.kind, identity.value)
}

type privateWriterOptionalLocalIdentity struct {
	present  bool
	identity privateWriterLocalIdentity
}

func (identity privateWriterOptionalLocalIdentity) valid() bool {
	if !identity.present {
		return identity.identity == (privateWriterLocalIdentity{})
	}
	return identity.identity.valid()
}

type privateWriterCreationSecurityKind uint16

const (
	privateWriterCreationSecurityPOSIX   privateWriterCreationSecurityKind = 1
	privateWriterCreationSecurityWindows privateWriterCreationSecurityKind = 2
)

func (kind privateWriterCreationSecurityKind) valid() bool {
	return kind == privateWriterCreationSecurityPOSIX ||
		kind == privateWriterCreationSecurityWindows
}

type privateWriterOptionalCreationSecurity struct {
	present    bool
	kind       privateWriterCreationSecurityKind
	commitment [32]byte
}

func (security privateWriterOptionalCreationSecurity) valid() bool {
	if !security.present {
		return security.kind == 0 && security.commitment == ([32]byte{})
	}
	return security.kind.valid()
}

type privateWriterOptionalUnpublishedTail struct {
	present                  bool
	expectedDatabaseID       [16]byte
	committedTargetTxnID     uint64
	committedTargetNonce     [16]byte
	committedTargetLength    uint64
	observedTailEndExclusive uint64
}

func (tail privateWriterOptionalUnpublishedTail) valid() bool {
	if !tail.present {
		return tail == (privateWriterOptionalUnpublishedTail{})
	}
	return tail.expectedDatabaseID != ([16]byte{}) &&
		tail.committedTargetTxnID != 0 &&
		tail.committedTargetNonce != ([16]byte{}) &&
		tail.committedTargetLength >= 2*PageSize &&
		tail.committedTargetLength%PageSize == 0 &&
		tail.observedTailEndExclusive > tail.committedTargetLength &&
		tail.observedTailEndExclusive%PageSize == 0
}

type privateWriterCleanupArtifactKind uint8

const (
	privateWriterArtifactPrivateOutput privateWriterCleanupArtifactKind = iota + 1
	privateWriterArtifactPrivateReservation
	privateWriterArtifactOwnedCoordination
	privateWriterArtifactAuthorizedScratch
	privateWriterArtifactUnpublishedMainTail
)

func (kind privateWriterCleanupArtifactKind) valid() bool {
	return kind >= privateWriterArtifactPrivateOutput &&
		kind <= privateWriterArtifactUnpublishedMainTail
}

type privateWriterCleanupDirectoryRole uint8

const (
	privateWriterDirectoryDestination privateWriterCleanupDirectoryRole = iota + 1
	privateWriterDirectoryScratch
	privateWriterDirectoryMainFile
)

func (role privateWriterCleanupDirectoryRole) valid() bool {
	return role >= privateWriterDirectoryDestination &&
		role <= privateWriterDirectoryMainFile
}

type privateWriterBasenameRef struct {
	encoding   basenameEncoding
	offset     uint64
	length     uint32
	commitment [32]byte
}

type privateWriterBasenameArena struct {
	self     *privateWriterBasenameArena
	storage  []byte
	used     uint64
	sealedBy *privateWriterTerminalResult
}

func initPrivateWriterBasenameArena(
	arena *privateWriterBasenameArena,
	storage []byte,
) privateWriterResultContractError {
	if arena == nil || arena.self != nil || arena.storage != nil ||
		arena.used != 0 || arena.sealedBy != nil {
		return privateWriterResultContractError{code: privateWriterResultErrInvalidState}
	}
	clear(storage)
	*arena = privateWriterBasenameArena{self: arena, storage: storage}
	return privateWriterResultContractError{}
}

func (arena *privateWriterBasenameArena) valid() bool {
	return arena != nil && arena.self == arena && arena.used <= uint64(len(arena.storage))
}

func (arena *privateWriterBasenameArena) append(
	encoding basenameEncoding,
	name []byte,
) (privateWriterBasenameRef, privateWriterResultContractError) {
	if !arena.valid() {
		return privateWriterBasenameRef{}, privateWriterResultContractError{
			code: privateWriterResultErrInvalidState,
		}
	}
	if arena.sealedBy != nil {
		return privateWriterBasenameRef{}, privateWriterResultContractError{
			code: privateWriterResultErrInvalidState,
		}
	}
	commitment, bindingProblem := basenameCommitment(encoding, name)
	if bindingProblem != nil {
		return privateWriterBasenameRef{}, privateWriterResultContractError{
			code: privateWriterResultErrInvalidBasename,
		}
	}
	nameLength := uint64(len(name))
	if nameLength > ^uint64(0)-arena.used {
		return privateWriterBasenameRef{}, privateWriterResultContractError{
			code: privateWriterResultErrArithmeticOverflow,
		}
	}
	end := arena.used + nameLength
	if end > uint64(len(arena.storage)) {
		return privateWriterBasenameRef{}, privateWriterResultContractError{
			code:   privateWriterResultErrBasenameArenaFull,
			needed: end, actual: uint64(len(arena.storage)),
		}
	}
	start := arena.used
	copy(arena.storage[int(start):int(end)], name)
	arena.used = end
	return privateWriterBasenameRef{
		encoding: encoding, offset: start, length: uint32(nameLength),
		commitment: commitment,
	}, privateWriterResultContractError{}
}

func (arena *privateWriterBasenameArena) bytes(
	reference privateWriterBasenameRef,
) ([]byte, privateWriterResultContractError) {
	if !arena.valid() || reference.length == 0 {
		return nil, privateWriterResultContractError{code: privateWriterResultErrInvalidState}
	}
	length := uint64(reference.length)
	if length > ^uint64(0)-reference.offset {
		return nil, privateWriterResultContractError{
			code: privateWriterResultErrArithmeticOverflow,
		}
	}
	end := reference.offset + length
	if end > arena.used || end > uint64(len(arena.storage)) {
		return nil, privateWriterResultContractError{
			code: privateWriterResultErrArtifactOutOfBounds,
		}
	}
	name := arena.storage[int(reference.offset):int(end)]
	commitment, bindingProblem := basenameCommitment(reference.encoding, name)
	if bindingProblem != nil || commitment != reference.commitment {
		return nil, privateWriterResultContractError{
			code: privateWriterResultErrInvalidBasename,
		}
	}
	return name, privateWriterResultContractError{}
}

type privateWriterCleanupArtifactEvidence struct {
	kind             privateWriterCleanupArtifactKind
	directoryRole    privateWriterCleanupDirectoryRole
	directory        privateWriterLocalIdentity
	basename         privateWriterBasenameRef
	identity         privateWriterOptionalLocalIdentity
	creationSecurity privateWriterOptionalCreationSecurity
	tail             privateWriterOptionalUnpublishedTail
}

func makePrivateWriterCleanupArtifactEvidence(
	arena *privateWriterBasenameArena,
	kind privateWriterCleanupArtifactKind,
	role privateWriterCleanupDirectoryRole,
	directory privateWriterLocalIdentity,
	encoding basenameEncoding,
	basename []byte,
	identity privateWriterOptionalLocalIdentity,
	security privateWriterOptionalCreationSecurity,
	tail privateWriterOptionalUnpublishedTail,
) (privateWriterCleanupArtifactEvidence, privateWriterResultContractError) {
	if !validPrivateWriterCleanupArtifactShape(
		kind, role, directory, encoding, identity, security, tail,
	) {
		return privateWriterCleanupArtifactEvidence{}, privateWriterResultContractError{
			code: privateWriterResultErrInvalidArtifact,
		}
	}
	reference, problem := arena.append(encoding, basename)
	if problem.failed() {
		return privateWriterCleanupArtifactEvidence{}, problem
	}
	return privateWriterCleanupArtifactEvidence{
		kind: kind, directoryRole: role, directory: directory,
		basename: reference, identity: identity,
		creationSecurity: security, tail: tail,
	}, privateWriterResultContractError{}
}

func validPrivateWriterCleanupArtifactShape(
	kind privateWriterCleanupArtifactKind,
	role privateWriterCleanupDirectoryRole,
	directory privateWriterLocalIdentity,
	encoding basenameEncoding,
	identity privateWriterOptionalLocalIdentity,
	security privateWriterOptionalCreationSecurity,
	tail privateWriterOptionalUnpublishedTail,
) bool {
	if !kind.valid() || !role.valid() || !directory.valid() ||
		!identity.valid() || !security.valid() || !tail.valid() ||
		basenameEncoding(uint16(directory.kind)) != encoding ||
		identity.present && identity.identity.kind != directory.kind ||
		security.present &&
			privateWriterCreationSecurityKind(directory.kind) != security.kind {
		return false
	}
	separatelyCreated := kind != privateWriterArtifactUnpublishedMainTail
	if separatelyCreated {
		if !security.present || tail.present {
			return false
		}
	} else if security.present || !tail.present || !identity.present {
		return false
	}
	switch kind {
	case privateWriterArtifactPrivateOutput, privateWriterArtifactPrivateReservation:
		if role != privateWriterDirectoryDestination {
			return false
		}
	case privateWriterArtifactOwnedCoordination:
		if role != privateWriterDirectoryDestination &&
			role != privateWriterDirectoryMainFile {
			return false
		}
	case privateWriterArtifactAuthorizedScratch:
		if role != privateWriterDirectoryScratch {
			return false
		}
	case privateWriterArtifactUnpublishedMainTail:
		if role != privateWriterDirectoryMainFile {
			return false
		}
	}
	return true
}

func (evidence privateWriterCleanupArtifactEvidence) valid(
	arena *privateWriterBasenameArena,
) bool {
	if _, problem := arena.bytes(evidence.basename); problem.failed() {
		return false
	}
	return validPrivateWriterCleanupArtifactShape(
		evidence.kind, evidence.directoryRole, evidence.directory,
		evidence.basename.encoding, evidence.identity,
		evidence.creationSecurity, evidence.tail,
	)
}

type privateWriterCleanupArtifactView struct {
	evidence     privateWriterCleanupArtifactEvidence
	cleanupState privateWriterCleanupState
	cleanupError privateWriterStableErrorCode
}

type privateWriterCommitAttempt struct {
	attemptedDatabaseID  [16]byte
	directory            privateWriterLocalIdentity
	main                 privateWriterLocalIdentity
	attemptedTxnID       uint64
	attemptedCommitNonce [16]byte
}

func (attempt privateWriterCommitAttempt) valid() bool {
	return attempt.attemptedDatabaseID != ([16]byte{}) &&
		attempt.directory.valid() && attempt.main.valid() &&
		attempt.directory.kind == attempt.main.kind &&
		attempt.attemptedTxnID != 0 &&
		attempt.attemptedCommitNonce != ([16]byte{})
}

type privateWriterCommitDurability uint8

const (
	privateWriterNotCommitted privateWriterCommitDurability = iota + 1
	privateWriterCommitted
	privateWriterOutcomeUnknown
)

func (durability privateWriterCommitDurability) valid() bool {
	return durability >= privateWriterNotCommitted &&
		durability <= privateWriterOutcomeUnknown
}

type privateWriterAbortOutcome uint8

const (
	privateWriterAborted privateWriterAbortOutcome = iota + 1
	privateWriterAbortIncomplete
)

func (outcome privateWriterAbortOutcome) valid() bool {
	return outcome == privateWriterAborted ||
		outcome == privateWriterAbortIncomplete
}

type privateWriterTerminalResult struct {
	self                      *privateWriterTerminalResult
	ledger                    *privateWriterCleanupLedger
	ledgerObligationsIdentity *privateWriterCleanupObligation
	ledgerOwnersIdentity      *privateWriterCleanupOwner
	ledgerLength              int
	arena                     *privateWriterBasenameArena
	arenaStorageIdentity      *byte
	arenaUsed                 uint64
	coordination              privateWriterCoordinationCleanup
	cause                     privateWriterOptionalStableError
}

func privateWriterCleanupObligationsIdentity(
	obligations []privateWriterCleanupObligation,
) *privateWriterCleanupObligation {
	if len(obligations) == 0 {
		return nil
	}
	return &obligations[0]
}

func privateWriterCleanupOwnersIdentity(
	owners []privateWriterCleanupOwner,
) *privateWriterCleanupOwner {
	if len(owners) == 0 {
		return nil
	}
	return &owners[0]
}

func privateWriterBasenameStorageIdentity(storage []byte) *byte {
	if len(storage) == 0 {
		return nil
	}
	return &storage[0]
}

func privateWriterTerminalInputsValid(
	ledger *privateWriterCleanupLedger,
	arena *privateWriterBasenameArena,
	cause privateWriterOptionalStableError,
) bool {
	if !ledger.valid() || ledger.retrying || ledger.sealedBy != nil ||
		!arena.valid() || arena.sealedBy != nil || !cause.valid() {
		return false
	}
	for index := 0; index < ledger.length; index++ {
		owner := ledger.owners[index]
		if owner.provenClean || !owner.lastError.failed() ||
			!ledger.obligations[index].artifact.valid(arena) ||
			!owner.lastError.semanticCode.valid() {
			return false
		}
	}
	return true
}

func initPrivateWriterTerminalResult(
	result *privateWriterTerminalResult,
	ledger *privateWriterCleanupLedger,
	arena *privateWriterBasenameArena,
	disposition privateWriterCoordinationDisposition,
	guardState *privateWriterCleanupGuardState,
	cause privateWriterOptionalStableError,
) privateWriterResultContractError {
	if result == nil || *result != (privateWriterTerminalResult{}) ||
		!privateWriterTerminalInputsValid(ledger, arena, cause) {
		return privateWriterResultContractError{code: privateWriterResultErrInvalidState}
	}
	var coordinationProblem privateWriterCoordinationError
	switch disposition {
	case privateWriterCoordinationNone:
		if guardState != nil {
			return privateWriterResultContractError{code: privateWriterResultErrInvalidState}
		}
		coordinationProblem = initPrivateWriterNoCoordinationCleanup(
			&result.coordination,
		)
	case privateWriterCoordinationCleanupGuard:
		if guardState == nil {
			return privateWriterResultContractError{
				code: privateWriterResultErrCleanupGuardUnavailable,
			}
		}
		coordinationProblem = armPrivateWriterCleanupGuard(
			&result.coordination, guardState,
		)
	case privateWriterCoordinationRetainedReaderCloseRequired,
		privateWriterCoordinationRetainedWriterCloseRequired:
		if guardState != nil {
			return privateWriterResultContractError{code: privateWriterResultErrInvalidState}
		}
		coordinationProblem = initPrivateWriterRetainedCoordinationCleanup(
			&result.coordination, disposition,
		)
	default:
		return privateWriterResultContractError{code: privateWriterResultErrInvalidState}
	}
	if coordinationProblem.failed() {
		return privateWriterResultContractError{code: privateWriterResultErrInvalidState}
	}
	result.self = result
	result.ledger = ledger
	result.ledgerObligationsIdentity =
		privateWriterCleanupObligationsIdentity(ledger.obligations)
	result.ledgerOwnersIdentity = privateWriterCleanupOwnersIdentity(ledger.owners)
	result.ledgerLength = ledger.length
	result.arena = arena
	result.arenaStorageIdentity = privateWriterBasenameStorageIdentity(arena.storage)
	result.arenaUsed = arena.used
	result.cause = cause
	result.coordination.self = &result.coordination
	ledger.sealedBy = result
	arena.sealedBy = result
	return privateWriterResultContractError{}
}

func (result *privateWriterTerminalResult) valid() bool {
	if result == nil || result.self != result || result.ledger == nil ||
		result.ledger.self != result.ledger || result.ledger.retrying ||
		result.ledger.sealedBy != result ||
		len(result.ledger.obligations) != len(result.ledger.owners) ||
		result.ledger.length != result.ledgerLength ||
		result.ledgerLength < 0 ||
		result.ledgerLength > len(result.ledger.obligations) ||
		privateWriterCleanupObligationsIdentity(result.ledger.obligations) !=
			result.ledgerObligationsIdentity ||
		privateWriterCleanupOwnersIdentity(result.ledger.owners) !=
			result.ledgerOwnersIdentity ||
		result.arena == nil || result.arena.self != result.arena ||
		result.arena.sealedBy != result ||
		result.arena.used != result.arenaUsed ||
		result.arenaUsed > uint64(len(result.arena.storage)) ||
		privateWriterBasenameStorageIdentity(result.arena.storage) !=
			result.arenaStorageIdentity ||
		!result.cause.valid() {
		return false
	}
	return result.coordination.validShape()
}

func (result *privateWriterTerminalResult) cleanupState() privateWriterCleanupState {
	if !result.valid() {
		return privateWriterCleanupResiduePossible
	}
	if result.ledgerLength == 0 &&
		result.coordination.disposition == privateWriterCoordinationNone {
		return privateWriterCleanupClean
	}
	return privateWriterCleanupResiduePossible
}

func (result *privateWriterTerminalResult) artifact(
	index uint64,
) (privateWriterCleanupArtifactView, privateWriterResultContractError) {
	if !result.valid() || index >= uint64(result.ledgerLength) {
		return privateWriterCleanupArtifactView{}, privateWriterResultContractError{
			code: privateWriterResultErrArtifactOutOfBounds, index: index,
		}
	}
	obligation := result.ledger.obligations[index]
	owner := result.ledger.owners[index]
	if obligation.id == 0 || owner.obligationID != obligation.id ||
		owner.provenClean || !owner.lastError.failed() ||
		owner.lastError.obligationID != obligation.id ||
		!owner.lastError.semanticCode.valid() ||
		!obligation.artifact.valid(result.arena) {
		return privateWriterCleanupArtifactView{}, privateWriterResultContractError{
			code: privateWriterResultErrInvalidArtifact, index: index,
		}
	}
	return privateWriterCleanupArtifactView{
		evidence:     obligation.artifact,
		cleanupState: privateWriterCleanupResiduePossible,
		cleanupError: owner.lastError.semanticCode,
	}, privateWriterResultContractError{}
}

func (result *privateWriterTerminalResult) takeCleanupGuard(
	guard *privateWriterTakenCleanupGuard,
) privateWriterResultContractError {
	if !result.valid() ||
		result.coordination.disposition != privateWriterCoordinationCleanupGuard {
		return privateWriterResultContractError{
			code: privateWriterResultErrCleanupGuardUnavailable,
		}
	}
	if problem := result.coordination.takeGuard(guard); problem.failed() {
		return privateWriterResultContractError{
			code: privateWriterResultErrCleanupGuardUnavailable,
		}
	}
	return privateWriterResultContractError{}
}

func (result *privateWriterTerminalResult) destroy() privateWriterResultContractError {
	if !result.valid() {
		return privateWriterResultContractError{code: privateWriterResultErrInvalidState}
	}
	if problem := result.coordination.destroy(); problem.failed() {
		if problem.code == privateWriterCoordinationErrGuardBusy {
			return privateWriterResultContractError{code: privateWriterResultErrHandleBusy}
		}
		return privateWriterResultContractError{code: privateWriterResultErrInvalidState}
	}
	result.ledger.sealedBy = nil
	result.arena.sealedBy = nil
	*result = privateWriterTerminalResult{}
	return privateWriterResultContractError{}
}

type privateWriterCommitResult struct {
	terminal   privateWriterTerminalResult
	attempt    privateWriterCommitAttempt
	durability privateWriterCommitDurability
}

func initPrivateWriterCommitResult(
	result *privateWriterCommitResult,
	attempt privateWriterCommitAttempt,
	durability privateWriterCommitDurability,
	ledger *privateWriterCleanupLedger,
	arena *privateWriterBasenameArena,
	disposition privateWriterCoordinationDisposition,
	guardState *privateWriterCleanupGuardState,
	cause privateWriterOptionalStableError,
) privateWriterResultContractError {
	if result == nil || *result != (privateWriterCommitResult{}) ||
		!attempt.valid() || !durability.valid() {
		return privateWriterResultContractError{code: privateWriterResultErrInvalidCommitAttempt}
	}
	if durability == privateWriterOutcomeUnknown &&
		disposition != privateWriterCoordinationRetainedWriterCloseRequired {
		return privateWriterResultContractError{code: privateWriterResultErrInvalidCommitResult}
	}
	if durability != privateWriterOutcomeUnknown &&
		disposition == privateWriterCoordinationRetainedReaderCloseRequired {
		return privateWriterResultContractError{code: privateWriterResultErrInvalidCommitResult}
	}
	if problem := initPrivateWriterTerminalResult(
		&result.terminal, ledger, arena, disposition, guardState, cause,
	); problem.failed() {
		return problem
	}
	result.attempt = attempt
	result.durability = durability
	return privateWriterResultContractError{}
}

func (result *privateWriterCommitResult) valid() bool {
	return result != nil && result.terminal.self == &result.terminal &&
		result.terminal.valid() && result.attempt.valid() &&
		result.durability.valid() &&
		result.terminal.coordination.disposition !=
			privateWriterCoordinationRetainedReaderCloseRequired &&
		(result.durability != privateWriterOutcomeUnknown ||
			result.terminal.coordination.disposition ==
				privateWriterCoordinationRetainedWriterCloseRequired)
}

func (result *privateWriterCommitResult) cleanupState() privateWriterCleanupState {
	if !result.valid() {
		return privateWriterCleanupResiduePossible
	}
	return result.terminal.cleanupState()
}

func (result *privateWriterCommitResult) takeCleanupGuard(
	guard *privateWriterTakenCleanupGuard,
) privateWriterResultContractError {
	if !result.valid() {
		return privateWriterResultContractError{code: privateWriterResultErrInvalidState}
	}
	return result.terminal.takeCleanupGuard(guard)
}

func (result *privateWriterCommitResult) destroy() privateWriterResultContractError {
	if !result.valid() {
		return privateWriterResultContractError{code: privateWriterResultErrInvalidState}
	}
	if problem := result.terminal.destroy(); problem.failed() {
		return problem
	}
	*result = privateWriterCommitResult{}
	return privateWriterResultContractError{}
}

type privateWriterAbortResult struct {
	terminal privateWriterTerminalResult
	outcome  privateWriterAbortOutcome
}

func initPrivateWriterAbortResult(
	result *privateWriterAbortResult,
	outcome privateWriterAbortOutcome,
	ledger *privateWriterCleanupLedger,
	arena *privateWriterBasenameArena,
	disposition privateWriterCoordinationDisposition,
	cause privateWriterOptionalStableError,
) privateWriterResultContractError {
	if result == nil || *result != (privateWriterAbortResult{}) || !outcome.valid() {
		return privateWriterResultContractError{code: privateWriterResultErrInvalidAbortResult}
	}
	hasArtifacts := ledger != nil && ledger.valid() && ledger.length != 0
	switch outcome {
	case privateWriterAborted:
		if hasArtifacts && disposition != privateWriterCoordinationRetainedWriterCloseRequired ||
			!hasArtifacts && disposition != privateWriterCoordinationNone &&
				disposition != privateWriterCoordinationRetainedWriterCloseRequired {
			return privateWriterResultContractError{code: privateWriterResultErrInvalidAbortResult}
		}
	case privateWriterAbortIncomplete:
		if disposition != privateWriterCoordinationRetainedWriterCloseRequired {
			return privateWriterResultContractError{code: privateWriterResultErrInvalidAbortResult}
		}
	}
	if problem := initPrivateWriterTerminalResult(
		&result.terminal, ledger, arena, disposition, nil, cause,
	); problem.failed() {
		return problem
	}
	result.outcome = outcome
	return privateWriterResultContractError{}
}

func (result *privateWriterAbortResult) valid() bool {
	if result == nil || result.terminal.self != &result.terminal ||
		!result.terminal.valid() || !result.outcome.valid() {
		return false
	}
	hasArtifacts := result.terminal.ledger.length != 0
	if result.outcome == privateWriterAbortIncomplete {
		return result.terminal.coordination.disposition ==
			privateWriterCoordinationRetainedWriterCloseRequired
	}
	if hasArtifacts {
		return result.terminal.coordination.disposition ==
			privateWriterCoordinationRetainedWriterCloseRequired
	}
	return result.terminal.coordination.disposition == privateWriterCoordinationNone ||
		result.terminal.coordination.disposition ==
			privateWriterCoordinationRetainedWriterCloseRequired
}

func (result *privateWriterAbortResult) cleanupState() privateWriterCleanupState {
	if !result.valid() {
		return privateWriterCleanupResiduePossible
	}
	return result.terminal.cleanupState()
}

func (result *privateWriterAbortResult) destroy() privateWriterResultContractError {
	if !result.valid() {
		return privateWriterResultContractError{code: privateWriterResultErrInvalidState}
	}
	if problem := result.terminal.destroy(); problem.failed() {
		return problem
	}
	*result = privateWriterAbortResult{}
	return privateWriterResultContractError{}
}
