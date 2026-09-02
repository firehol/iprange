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

// errorCodeWireNames is the single authority for the stable JSON-RPC
// wire names of the SDK error codes (the closed product code list of
// iprange-jsonrpc-v1.md; the same vocabulary the adapters use for
// responses). The wire decoder validates preserved evidence with it.
func errorCodeWireNames() map[ErrorCode]string {
	return map[ErrorCode]string{
		CodeInvalidArgument: "invalid_argument",
		CodeNullPointer: "null_pointer",
		CodeMisalignedPointer: "misaligned_pointer",
		CodeInvalidLength: "invalid_length",
		CodeInvalidEnum: "invalid_enum",
		CodeReservedNonzero: "reserved_nonzero",
		CodeBufferTooSmall: "buffer_too_small",
		CodeWrongHandleKind: "handle_wrong_kind",
		CodeHandleClosed: "handle_closed",
		CodeHandleBusy: "handle_busy",
		CodeWrongState: "wrong_state",
		CodeWrongAddressFamily: "wrong_address_family",
		CodeWrongValueKind: "wrong_value_kind",
		CodeWrongValueTag: "wrong_value_tag",
		CodeRangeReversed: "range_reversed",
		CodeNameInvalid: "name_invalid",
		CodeNameExists: "name_exists",
		CodeNameNotFound: "name_not_found",
		CodeStaleReference: "stale_reference",
		CodeForeignReference: "foreign_reference",
		CodeNoPendingTransaction: "no_pending_transaction",
		CodeTransactionAborted: "transaction_aborted",
		CodeAbortIncomplete: "abort_incomplete",
		CodeInsufficientResourceBudget: "insufficient_resource_budget",
		CodePageSpaceExhausted: "page_space_exhausted",
		CodeWorkLimitTooSmall: "work_limit_too_small",
		CodeCancelled: "cancelled",
		CodeSourceFailed: "source_failed",
		CodeSinkFailed: "sink_failed",
		CodeStoppedBySink: "stopped_by_sink",
		CodeIO: "io",
		CodeFormatInvalid: "format_invalid",
		CodeNotV4: "not_v4",
		CodeDurabilityUnsupported: "durability_unsupported",
		CodePublicationUnsupported: "publication_unsupported",
		CodeAccessPolicyUnsupported: "access_policy_unsupported",
		CodeConflict: "conflict",
		CodeUnresolvable: "unresolvable",
		CodeWriterBusy: "writer_busy",
		CodeDirectoryIdentityMismatch: "directory_identity_mismatch",
		CodeDestinationNameMismatch: "destination_name_mismatch",
		CodeCleanupConflict: "cleanup_conflict",
		CodeCoordinationSequenceExhausted: "coordination_sequence_exhausted",
		CodeLiveCoordinationUnsupported: "live_coordination_unsupported",
		CodeLiveCoordinationCleanupRequired: "live_coordination_cleanup_required",
		CodeLiveCoordinationMalformedRequiresReset: "live_coordination_malformed_requires_reset",
		CodeLiveOpenCleanupRequired: "live_open_cleanup_required",
		CodeLiveRecoveryCoordinationUnavailable: "live_recovery_coordination_unavailable",
		CodeLiveRecoveryCurrentGenerationUnprovable: "live_recovery_current_generation_unprovable",
		CodeLiveRecoveryCurrentGenerationUnreadable: "live_recovery_current_generation_unreadable",
		CodeRecoveryCandidateChanged: "recovery_candidate_changed",
		CodeRecoveryPreparationFailed: "recovery_preparation_failed",
		CodeSnapshotPreparationFailed: "snapshot_preparation_failed",
		CodeTransitionSuperseded: "transition_superseded",
		CodeCurrentGenerationUnprovable: "current_generation_unprovable",
		CodeForkedHandle: "forked_handle",
		CodePanic: "panic",
		CodeOSUnsupported: "os_unsupported",
		CodeTransactionIdExhausted: "transaction_id_exhausted",
		CodeArithmeticOverflow: "arithmetic_overflow",
		CodeFeedIndexExhausted: "feed_index_exhausted",
		CodeMembershipIdExhausted: "membership_id_exhausted",
		CodeReaderCapacityExhausted: "reader_capacity_exhausted",
		CodeCleanupInProgress: "cleanup_in_progress",
		CodeFaultWorkerUnavailable: "fault_worker_unavailable",
		CodeFaultWorkerFailed: "fault_worker_failed",
		CodeUnsupportedStructure: "unsupported_structure",
		CodeWrongStructureKind: "wrong_structure_kind",
		CodeStructureIdExhausted: "structure_id_exhausted",
	}
}

// ErrorCodeWireName returns the stable JSON-RPC wire name of one error
// code, or ok=false when the code is not part of the closed product
// vocabulary.
func ErrorCodeWireName(code ErrorCode) (string, bool) {
	name, ok := errorCodeWireNames()[code]
	return name, ok
}

// ErrorCodeFromWireName resolves one stable JSON-RPC wire error name to
// its SDK code, or ok=false when the name is not canonical.
func ErrorCodeFromWireName(name string) (ErrorCode, bool) {
	for code, candidate := range errorCodeWireNames() {
		if candidate == name {
			return code, true
		}
	}
	return 0, false
}
