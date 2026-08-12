package format

// ErrorCode is the exact v4 SDK error code (sections 5.2.1 of the Rust
// sdk_error table; identical numeric values in all languages). This table is
// the single authority for code values in the Go peer; the public facade maps
// it to the public error type.
type ErrorCode uint32

const (
	CodeInvalidArgument ErrorCode = 1 + iota
	CodeNullPointer
	CodeMisalignedPointer
	CodeInvalidLength
	CodeInvalidEnum
	CodeReservedNonzero
	CodeBufferTooSmall
	CodeWrongHandleKind
	CodeHandleClosed
	CodeHandleBusy
	CodeWrongState
	CodeWrongAddressFamily
	CodeWrongValueKind
	CodeWrongValueTag
	CodeRangeReversed
	CodeNameInvalid
	CodeNameExists
	CodeNameNotFound
	CodeStaleReference
	CodeForeignReference
	CodeNoPendingTransaction
	CodeTransactionAborted
	CodeAbortIncomplete
	CodeInsufficientResourceBudget
	CodePageSpaceExhausted
	CodeWorkLimitTooSmall
	CodeCancelled
	CodeSourceFailed
	CodeSinkFailed
	CodeStoppedBySink
	CodeIO
	CodeFormatInvalid
	CodeNotV4
	CodeDurabilityUnsupported
	CodePublicationUnsupported
	CodeAccessPolicyUnsupported
	CodeConflict
	CodeUnresolvable
	CodeWriterBusy
	CodeDirectoryIdentityMismatch
	CodeDestinationNameMismatch
	CodeCleanupConflict
	CodeCoordinationSequenceExhausted
	CodeLiveCoordinationUnsupported
	CodeLiveCoordinationCleanupRequired
	// Code 46 is LiveCoordinationMalformedRequiresReset in the current
	// contract; the removed Go experiment called this code
	// ErrorLiveCoordinationDomainMismatchRequiresReset. The Rust name is
	// authority here.
	CodeLiveCoordinationMalformedRequiresReset
	CodeLiveOpenCleanupRequired
	CodeLiveRecoveryCoordinationUnavailable
	CodeLiveRecoveryCurrentGenerationUnprovable
	CodeLiveRecoveryCurrentGenerationUnreadable
	CodeRecoveryCandidateChanged
	CodeRecoveryPreparationFailed
	CodeSnapshotPreparationFailed
	CodeTransitionSuperseded
	CodeCurrentGenerationUnprovable
	CodeForkedHandle
	CodePanic
	CodeOSUnsupported
	CodeTransactionIdExhausted
	CodeArithmeticOverflow
	CodeFeedIndexExhausted
	CodeMembershipIdExhausted
	CodeReaderCapacityExhausted
	CodeCleanupInProgress
	CodeFaultWorkerUnavailable
	CodeFaultWorkerFailed
	CodeUnsupportedStructure
	CodeWrongStructureKind
	CodeStructureIdExhausted
)

// Error is one typed SDK failure carried by internal packages. The public
// facade converts it into the public error type.
type Error struct {
	Code   ErrorCode
	Detail string
}

func (e *Error) Error() string { return "iprange v4 error " + itoa(uint64(e.Code)) + ": " + e.Detail }

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
