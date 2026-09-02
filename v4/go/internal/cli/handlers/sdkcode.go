// SDK error mapping and response-ceiling preflight helpers shared by
// every handler family (Rust handlers/reader.rs parity).

package handlers

import (
	"encoding/json"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// sdkCode maps one SDK error code to its stable wire adapter code
// (the closed product code list of iprange-jsonrpc-v1.md).
func sdkCode(code iprangedb.ErrorCode) string {
	switch code {
	case iprangedb.ErrorInvalidArgument:
		return "invalid_argument"
	case iprangedb.ErrorNullPointer:
		return "null_pointer"
	case iprangedb.ErrorMisalignedPointer:
		return "misaligned_pointer"
	case iprangedb.ErrorInvalidLength:
		return "invalid_length"
	case iprangedb.ErrorInvalidEnum:
		return "invalid_enum"
	case iprangedb.ErrorReservedNonzero:
		return "reserved_nonzero"
	case iprangedb.ErrorBufferTooSmall:
		return "buffer_too_small"
	case iprangedb.ErrorWrongHandleKind:
		return "handle_wrong_kind"
	case iprangedb.ErrorHandleClosed:
		return "handle_closed"
	case iprangedb.ErrorHandleBusy:
		return "handle_busy"
	case iprangedb.ErrorWrongState:
		return "wrong_state"
	case iprangedb.ErrorWrongAddressFamily:
		return "wrong_address_family"
	case iprangedb.ErrorWrongValueKind:
		return "wrong_value_kind"
	case iprangedb.ErrorWrongValueTag:
		return "wrong_value_tag"
	case iprangedb.ErrorRangeReversed:
		return "range_reversed"
	case iprangedb.ErrorNameInvalid:
		return "name_invalid"
	case iprangedb.ErrorNameExists:
		return "name_exists"
	case iprangedb.ErrorNameNotFound:
		return "name_not_found"
	case iprangedb.ErrorStaleReference:
		return "stale_reference"
	case iprangedb.ErrorForeignReference:
		return "foreign_reference"
	case iprangedb.ErrorNoPendingTransaction:
		return "no_pending_transaction"
	case iprangedb.ErrorTransactionAborted:
		return "transaction_aborted"
	case iprangedb.ErrorAbortIncomplete:
		return "abort_incomplete"
	case iprangedb.ErrorInsufficientResourceBudget:
		return "insufficient_resource_budget"
	case iprangedb.ErrorPageSpaceExhausted:
		return "page_space_exhausted"
	case iprangedb.ErrorWorkLimitTooSmall:
		return "work_limit_too_small"
	case iprangedb.ErrorCancelled:
		return "cancelled"
	case iprangedb.ErrorSourceFailed:
		return "source_failed"
	case iprangedb.ErrorSinkFailed:
		return "sink_failed"
	case iprangedb.ErrorStoppedBySink:
		return "stopped_by_sink"
	case iprangedb.ErrorIO:
		return "io"
	case iprangedb.ErrorFormatInvalid:
		return "format_invalid"
	case iprangedb.ErrorNotV4:
		return "not_v4"
	case iprangedb.ErrorDurabilityUnsupported:
		return "durability_unsupported"
	case iprangedb.ErrorPublicationUnsupported:
		return "publication_unsupported"
	case iprangedb.ErrorAccessPolicyUnsupported:
		return "access_policy_unsupported"
	case iprangedb.ErrorConflict:
		return "conflict"
	case iprangedb.ErrorUnresolvable:
		return "unresolvable"
	case iprangedb.ErrorWriterBusy:
		return "writer_busy"
	case iprangedb.ErrorDirectoryIdentityMismatch:
		return "directory_identity_mismatch"
	case iprangedb.ErrorDestinationNameMismatch:
		return "destination_name_mismatch"
	case iprangedb.ErrorCleanupConflict:
		return "cleanup_conflict"
	case iprangedb.ErrorCoordinationSequenceExhausted:
		return "coordination_sequence_exhausted"
	case iprangedb.ErrorLiveCoordinationUnsupported:
		return "live_coordination_unsupported"
	case iprangedb.ErrorLiveCoordinationCleanupRequired:
		return "live_coordination_cleanup_required"
	case iprangedb.ErrorLiveCoordinationMalformedRequiresReset:
		return "live_coordination_malformed_requires_reset"
	case iprangedb.ErrorLiveOpenCleanupRequired:
		return "live_open_cleanup_required"
	case iprangedb.ErrorLiveRecoveryCoordinationUnavailable:
		return "live_recovery_coordination_unavailable"
	case iprangedb.ErrorLiveRecoveryCurrentGenerationUnprovable:
		return "live_recovery_current_generation_unprovable"
	case iprangedb.ErrorLiveRecoveryCurrentGenerationUnreadable:
		return "live_recovery_current_generation_unreadable"
	case iprangedb.ErrorRecoveryCandidateChanged:
		return "recovery_candidate_changed"
	case iprangedb.ErrorRecoveryPreparationFailed:
		return "recovery_preparation_failed"
	case iprangedb.ErrorSnapshotPreparationFailed:
		return "snapshot_preparation_failed"
	case iprangedb.ErrorTransitionSuperseded:
		return "transition_superseded"
	case iprangedb.ErrorCurrentGenerationUnprovable:
		return "current_generation_unprovable"
	case iprangedb.ErrorForkedHandle:
		return "forked_handle"
	case iprangedb.ErrorPanic:
		return "panic"
	case iprangedb.ErrorOSUnsupported:
		return "os_unsupported"
	case iprangedb.ErrorTransactionIdExhausted:
		return "transaction_id_exhausted"
	case iprangedb.ErrorArithmeticOverflow:
		return "arithmetic_overflow"
	case iprangedb.ErrorFeedIndexExhausted:
		return "feed_index_exhausted"
	case iprangedb.ErrorMembershipIdExhausted:
		return "membership_id_exhausted"
	case iprangedb.ErrorReaderCapacityExhausted:
		return "reader_capacity_exhausted"
	case iprangedb.ErrorCleanupInProgress:
		return "cleanup_in_progress"
	case iprangedb.ErrorFaultWorkerUnavailable:
		return "fault_worker_unavailable"
	case iprangedb.ErrorFaultWorkerFailed:
		return "fault_worker_failed"
	case iprangedb.ErrorUnsupportedStructure:
		return "unsupported_structure"
	case iprangedb.ErrorWrongStructureKind:
		return "wrong_structure_kind"
	case iprangedb.ErrorStructureIdExhausted:
		return "structure_id_exhausted"
	}
	return "io"
}

// readError converts one SDK failure of a read-only operation.
func readError(err error) *rpc.HandlerError {
	if typed, ok := err.(*iprangedb.Error); ok {
		return rpc.NewHandlerError(sdkCode(typed.Code), "read_only_failure", typed.Error())
	}
	return rpc.NewHandlerError("io", "read_only_failure", err.Error())
}

// sdk converts an SDK result of a read-only operation.
func sdk[T any](result T, err error) (T, *rpc.HandlerError) {
	if err != nil {
		var zero T
		return zero, readError(err)
	}
	return result, nil
}

// boundedResult enforces the 65,000-byte response-object ceiling on a
// complete result before it is returned.
func boundedResult(result any) (any, *rpc.HandlerError) {
	probe := map[string]any{"result": result}
	if _, serr := rpc.EncodeResponseObjectProbe(probe); serr != nil {
		return nil, rpc.NewHandlerError("output_limit", "read_only_failure",
			"response object exceeds the 65000-byte limit")
	}
	return result, nil
}

// WidestU64 is the longest portably representable decimal of a u64.
const WidestU64 = "18446744073709551615"

// Widest129 is the longest decimal of the exact 129-bit address
// cardinality (binary-format-v4.md section 17).
const Widest129 = "680564733841876926926749214863536422911"

// WidestIdentity is the largest observable {volume, file} identity pair.
func WidestIdentity() map[string]any {
	return map[string]any{"volume": WidestU64, "file": WidestU64}
}

// WidestCloseFact is the largest observable live-close fact emitted by
// the adapter close owners.
func WidestCloseFact() map[string]any {
	return map[string]any{
		"outcome":              "close_incomplete",
		"cleanup":              map[string]any{},
		"coordination_cleanup": map[string]any{"kind": "retained_writer_close_required"},
	}
}

// PreflightResponseMargin covers constants the preflight template does
// not model; refusing slightly early is the honest direction.
const PreflightResponseMargin = 2048

// preflightResponse bounds the complete response object of a method
// before any work runs: the envelope carries the real echoed request
// id and the caller's worst-case result object. A response whose real
// report passes this probe always fits the ceiling; a request that
// cannot fit is refused with output_limit/not_started before any
// writer is opened or file is published.
func preflightResponse(state *rpc.SessionState, worst any) *rpc.HandlerError {
	envelope := map[string]any{"jsonrpc": "2.0", "result": worst}
	if id := state.ActiveRequestID; id != nil {
		envelope["id"] = json.RawMessage(id.AsJSON())
	}
	text, serr := rpc.EncodeResponseObjectProbe(envelope)
	if serr == nil && len(text) <= rpc.ResponseObjectLimit-PreflightResponseMargin {
		return nil
	}
	return rpc.NewHandlerError("output_limit", "not_started",
		"response object exceeds the 65000-byte limit")
}
