package iprangedb

import "fmt"

// ErrorCode is the stable Phase-1 error classification shared with Rust and C.
type ErrorCode uint32

const (
	ErrorInvalidArgument ErrorCode = iota + 1
	ErrorNullPointer
	ErrorMisalignedPointer
	ErrorInvalidLength
	ErrorInvalidEnum
	ErrorReservedNonzero
	ErrorBufferTooSmall
	ErrorWrongHandleKind
	ErrorHandleClosed
	ErrorHandleBusy
	ErrorWrongState
	ErrorWrongAddressFamily
	ErrorWrongValueKind
	ErrorWrongValueTag
	ErrorRangeReversed
	ErrorNameInvalid
	ErrorNameExists
	ErrorNameNotFound
	ErrorStaleReference
	ErrorForeignReference
	ErrorNoPendingTransaction
	ErrorTransactionAborted
	ErrorAbortIncomplete
	ErrorInsufficientResourceBudget
	ErrorPageSpaceExhausted
	ErrorWorkLimitTooSmall
	ErrorCancelled
	ErrorSourceFailed
	ErrorSinkFailed
	ErrorStoppedBySink
	ErrorIO
	ErrorFormatInvalid
	ErrorNotV4
	ErrorDurabilityUnsupported
	ErrorPublicationUnsupported
	ErrorAccessPolicyUnsupported
	ErrorConflict
	ErrorUnresolvable
	ErrorWriterBusy
	ErrorDirectoryIdentityMismatch
	ErrorDestinationNameMismatch
	ErrorCleanupConflict
	ErrorCoordinationSequenceExhausted
	ErrorLiveCoordinationUnsupported
	ErrorLiveCoordinationCleanupRequired
	ErrorLiveCoordinationDomainMismatchRequiresReset
	ErrorLiveOpenCleanupRequired
	ErrorLiveRecoveryCoordinationUnavailable
	ErrorLiveRecoveryCurrentGenerationUnprovable
	ErrorLiveRecoveryCurrentGenerationUnreadable
	ErrorRecoveryCandidateChanged
	ErrorRecoveryPreparationFailed
	ErrorSnapshotPreparationFailed
	ErrorTransitionSuperseded
	ErrorCurrentGenerationUnprovable
	ErrorForkedHandle
	ErrorPanic
	ErrorOSUnsupported
	ErrorTransactionIDExhausted
	ErrorArithmeticOverflow
	ErrorFeedIndexExhausted
	ErrorMembershipIDExhausted
	ErrorReaderCapacityExhausted
	ErrorCleanupInProgress
)

// Error is one typed SDK failure. Detail is implementation-owned text and must
// not contain unescaped attacker-controlled file bytes.
type Error struct {
	Code   ErrorCode
	Detail string
	Cause  error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("iprange v4 error %d: %s", e.Code, e.Detail)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
