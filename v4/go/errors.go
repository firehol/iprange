package iprangedb

import (
	"fmt"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// ErrorCode is the stable Phase-1 error classification shared with Rust and
// C. The numeric authority is the single internal/format error-code table;
// this public table re-exports every code by name so the two can never
// drift.
type ErrorCode = format.ErrorCode

const (
	ErrorInvalidArgument                         ErrorCode = format.CodeInvalidArgument
	ErrorNullPointer                             ErrorCode = format.CodeNullPointer
	ErrorMisalignedPointer                       ErrorCode = format.CodeMisalignedPointer
	ErrorInvalidLength                           ErrorCode = format.CodeInvalidLength
	ErrorInvalidEnum                             ErrorCode = format.CodeInvalidEnum
	ErrorReservedNonzero                         ErrorCode = format.CodeReservedNonzero
	ErrorBufferTooSmall                          ErrorCode = format.CodeBufferTooSmall
	ErrorWrongHandleKind                         ErrorCode = format.CodeWrongHandleKind
	ErrorHandleClosed                            ErrorCode = format.CodeHandleClosed
	ErrorHandleBusy                              ErrorCode = format.CodeHandleBusy
	ErrorWrongState                              ErrorCode = format.CodeWrongState
	ErrorWrongAddressFamily                      ErrorCode = format.CodeWrongAddressFamily
	ErrorWrongValueKind                          ErrorCode = format.CodeWrongValueKind
	ErrorWrongValueTag                           ErrorCode = format.CodeWrongValueTag
	ErrorRangeReversed                           ErrorCode = format.CodeRangeReversed
	ErrorNameInvalid                             ErrorCode = format.CodeNameInvalid
	ErrorNameExists                              ErrorCode = format.CodeNameExists
	ErrorNameNotFound                            ErrorCode = format.CodeNameNotFound
	ErrorStaleReference                          ErrorCode = format.CodeStaleReference
	ErrorForeignReference                        ErrorCode = format.CodeForeignReference
	ErrorNoPendingTransaction                    ErrorCode = format.CodeNoPendingTransaction
	ErrorTransactionAborted                      ErrorCode = format.CodeTransactionAborted
	ErrorAbortIncomplete                         ErrorCode = format.CodeAbortIncomplete
	ErrorInsufficientResourceBudget              ErrorCode = format.CodeInsufficientResourceBudget
	ErrorPageSpaceExhausted                      ErrorCode = format.CodePageSpaceExhausted
	ErrorWorkLimitTooSmall                       ErrorCode = format.CodeWorkLimitTooSmall
	ErrorCancelled                               ErrorCode = format.CodeCancelled
	ErrorSourceFailed                            ErrorCode = format.CodeSourceFailed
	ErrorSinkFailed                              ErrorCode = format.CodeSinkFailed
	ErrorStoppedBySink                           ErrorCode = format.CodeStoppedBySink
	ErrorIO                                      ErrorCode = format.CodeIO
	ErrorFormatInvalid                           ErrorCode = format.CodeFormatInvalid
	ErrorNotV4                                   ErrorCode = format.CodeNotV4
	ErrorDurabilityUnsupported                   ErrorCode = format.CodeDurabilityUnsupported
	ErrorPublicationUnsupported                  ErrorCode = format.CodePublicationUnsupported
	ErrorAccessPolicyUnsupported                 ErrorCode = format.CodeAccessPolicyUnsupported
	ErrorConflict                                ErrorCode = format.CodeConflict
	ErrorUnresolvable                            ErrorCode = format.CodeUnresolvable
	ErrorWriterBusy                              ErrorCode = format.CodeWriterBusy
	ErrorDirectoryIdentityMismatch               ErrorCode = format.CodeDirectoryIdentityMismatch
	ErrorDestinationNameMismatch                 ErrorCode = format.CodeDestinationNameMismatch
	ErrorCleanupConflict                         ErrorCode = format.CodeCleanupConflict
	ErrorCoordinationSequenceExhausted           ErrorCode = format.CodeCoordinationSequenceExhausted
	ErrorLiveCoordinationUnsupported             ErrorCode = format.CodeLiveCoordinationUnsupported
	ErrorLiveCoordinationCleanupRequired         ErrorCode = format.CodeLiveCoordinationCleanupRequired
	ErrorLiveCoordinationMalformedRequiresReset  ErrorCode = format.CodeLiveCoordinationMalformedRequiresReset
	ErrorLiveOpenCleanupRequired                 ErrorCode = format.CodeLiveOpenCleanupRequired
	ErrorLiveRecoveryCoordinationUnavailable     ErrorCode = format.CodeLiveRecoveryCoordinationUnavailable
	ErrorLiveRecoveryCurrentGenerationUnprovable ErrorCode = format.CodeLiveRecoveryCurrentGenerationUnprovable
	ErrorLiveRecoveryCurrentGenerationUnreadable ErrorCode = format.CodeLiveRecoveryCurrentGenerationUnreadable
	ErrorRecoveryCandidateChanged                ErrorCode = format.CodeRecoveryCandidateChanged
	ErrorRecoveryPreparationFailed               ErrorCode = format.CodeRecoveryPreparationFailed
	ErrorSnapshotPreparationFailed               ErrorCode = format.CodeSnapshotPreparationFailed
	ErrorTransitionSuperseded                    ErrorCode = format.CodeTransitionSuperseded
	ErrorCurrentGenerationUnprovable             ErrorCode = format.CodeCurrentGenerationUnprovable
	ErrorForkedHandle                            ErrorCode = format.CodeForkedHandle
	ErrorPanic                                   ErrorCode = format.CodePanic
	ErrorOSUnsupported                           ErrorCode = format.CodeOSUnsupported
	ErrorTransactionIdExhausted                  ErrorCode = format.CodeTransactionIdExhausted
	ErrorArithmeticOverflow                      ErrorCode = format.CodeArithmeticOverflow
	ErrorFeedIndexExhausted                      ErrorCode = format.CodeFeedIndexExhausted
	ErrorMembershipIdExhausted                   ErrorCode = format.CodeMembershipIdExhausted
	ErrorReaderCapacityExhausted                 ErrorCode = format.CodeReaderCapacityExhausted
	ErrorCleanupInProgress                       ErrorCode = format.CodeCleanupInProgress
	ErrorFaultWorkerUnavailable                  ErrorCode = format.CodeFaultWorkerUnavailable
	ErrorFaultWorkerFailed                       ErrorCode = format.CodeFaultWorkerFailed
	ErrorUnsupportedStructure                    ErrorCode = format.CodeUnsupportedStructure
	ErrorWrongStructureKind                      ErrorCode = format.CodeWrongStructureKind
	ErrorStructureIdExhausted                    ErrorCode = format.CodeStructureIdExhausted
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
