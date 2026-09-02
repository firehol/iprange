// Validation, recovery-candidate inspection, and recovery (Rust
// handlers/recovery.rs parity): `iprange.v1.validate`,
// `iprange.v1.recovery.inspect`, and `iprange.v1.recover`.
//
// All three methods run through the public SDK worker path; the CLI
// process never scans a database page (spec validate, recovery
// inspect, recover). Findings and unknown envelopes stream into the
// caller-selected JSONL file under the row/byte budget and the export
// writer publishes atomically only on success; a failed stream keeps
// the partial progress and cleanup facts in the error details.

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/fileio"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// ---------------------------------------------------------------------------
// iprange.v1.validate
// ---------------------------------------------------------------------------

// ValidateValidateParams enforces the strict validate schema: path,
// the three-mode mode object, the scratch validation budget, and the
// JSONL findings output descriptor.
func ValidateValidateParams(params json.RawMessage) error {
	object, err := exactObject(params, "path", "mode", "validation_budget", "findings_output")
	if err != nil {
		return err
	}
	path, err := asString(object, "path")
	if err != nil {
		return err
	}
	if err := validatePath(path); err != nil {
		return err
	}
	mode, err := memberObject(object, "mode")
	if err != nil {
		return err
	}
	kind, err := asString(mode, "kind")
	if err != nil {
		return err
	}
	switch kind {
	case "immutable_current", "live_current":
		if err := exactObjectRaw(mode, "kind"); err != nil {
			return err
		}
	case "offline_candidate":
		if err := exactObjectRaw(mode, "kind", "candidate"); err != nil {
			return err
		}
		candidate, err := memberObject(mode, "candidate")
		if err != nil {
			return err
		}
		if err := validateCandidateObject(candidate); err != nil {
			return err
		}
	default:
		return errString("mode.kind must be immutable_current, live_current, or offline_candidate")
	}
	budget, err := memberObject(object, "validation_budget")
	if err != nil {
		return err
	}
	if err := validateScratchBudget(budget, false); err != nil {
		return err
	}
	output, err := memberObject(object, "findings_output")
	if err != nil {
		return err
	}
	return validateOutputDescriptor(output)
}

// validateCandidateObject enforces the exact opaque candidate wire
// shape returned by recovery.inspect (Rust recovery::candidate_from_value).
func validateCandidateObject(candidate rawObject) error {
	if err := exactObjectRaw(candidate, "label", "meta_page", "source_identity", "database_id", "transaction_id", "commit_nonce"); err != nil {
		return err
	}
	label, err := asString(candidate, "label")
	if err != nil {
		return err
	}
	switch label {
	case "newest", "previous", "unordered_meta_0", "unordered_meta_1":
	default:
		return errString("candidate.label is invalid")
	}
	metaPage, err := asUint32(candidate, "meta_page")
	if err != nil || metaPage > 1 {
		return errString("candidate.meta_page must be 0 or 1")
	}
	identity, err := memberObject(candidate, "source_identity")
	if err != nil {
		return err
	}
	if err := validateIdentityObject(identity); err != nil {
		return err
	}
	if _, err := asHexString(candidate, "database_id"); err != nil || len(mustString(candidate, "database_id")) != 32 {
		return errString("candidate.database_id must be 32 lowercase hexadecimal characters")
	}
	if _, err := asDecimalString(candidate, "transaction_id"); err != nil {
		return errString("candidate.transaction_id must be a canonical unsigned decimal string")
	}
	if _, err := asHexString(candidate, "commit_nonce"); err != nil || len(mustString(candidate, "commit_nonce")) != 32 {
		return errString("candidate.commit_nonce must be 32 lowercase hexadecimal characters")
	}
	return nil
}

func mustString(object rawObject, name string) string {
	value, _ := asString(object, name)
	return value
}

// validateIdentityObject enforces one FILE_IDENTITY wire object.
func validateIdentityObject(identity rawObject) error {
	if err := exactObjectRaw(identity, "volume", "file"); err != nil {
		return err
	}
	for _, field := range []string{"volume", "file"} {
		value, err := asDecimalString(identity, field)
		if err != nil {
			return errString("identity." + field + " must be a canonical unsigned decimal string")
		}
		if _, err := canonicalU64String(value); err != nil {
			return errString("identity." + field + " must be a canonical unsigned decimal string")
		}
	}
	return nil
}

// validateScratchBudget enforces the validation/recovery scratch
// budget: positive heap and open-file limits, scratch fully disabled
// or fully enabled, and (recovery only) a positive max_output_pages.
func validateScratchBudget(budget rawObject, recovery bool) error {
	required := []string{"max_heap_bytes", "max_open_files", "max_scratch_bytes", "max_scratch_files"}
	if recovery {
		required = append(required, "max_output_pages")
	}
	encoded, err := json.Marshal(budget)
	if err != nil {
		return fmt.Errorf("validation_budget must be an object")
	}
	if _, err := exactObjectOpt(encoded, required, []string{"scratch_directory"}); err != nil {
		return err
	}
	heap, err := asDecimalString(budget, "max_heap_bytes")
	if err != nil || !positiveDecimal(heap) {
		return errString("validation_budget.max_heap_bytes must be a positive canonical unsigned decimal string")
	}
	openFiles, err := asUint32(budget, "max_open_files")
	if err != nil || openFiles == 0 {
		return errString("validation_budget.max_open_files must be a positive u32 integer")
	}
	if recovery {
		pages, err := asDecimalString(budget, "max_output_pages")
		if err != nil || !positiveDecimal(pages) {
			return errString("recovery_budget.max_output_pages must be a positive canonical unsigned decimal string")
		}
	}
	scratchBytes, err := asDecimalString(budget, "max_scratch_bytes")
	if err != nil {
		return errString("validation_budget.max_scratch_bytes must be a canonical unsigned decimal string")
	}
	scratchFiles, err := asUint32(budget, "max_scratch_files")
	if err != nil {
		return errString("validation_budget.max_scratch_files must be a u32 integer")
	}
	directoryPresent := false
	if raw, ok := budget["scratch_directory"]; ok && !isRawNull(raw) {
		directoryPresent = true
		directory, err := asString(budget, "scratch_directory")
		if err != nil {
			return err
		}
		if err := validatePath(directory); err != nil {
			return err
		}
	}
	disabled := scratchBytes == "0" && scratchFiles == 0
	enabled := scratchBytes != "0" && scratchFiles != 0 && directoryPresent
	if (disabled && !directoryPresent) || enabled {
		return nil
	}
	return errString("scratch must be fully disabled or fully enabled")
}

// Validate implements iprange.v1.validate (Rust validate): one
// explicit full-file validation, findings streamed as one complete
// mechanically converted ValidationFinding per JSONL row.
func Validate(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	if herr := requireExistingPath(path); herr != nil {
		return nil, herr
	}
	mode, err := memberObject(object, "mode")
	if err != nil {
		return nil, rpc.InvalidParamsError("mode must be an object")
	}
	kind, err := asString(mode, "kind")
	if err != nil {
		return nil, rpc.InvalidParamsError("mode.kind is invalid")
	}
	budget, herr := decodeValidationBudget(object)
	if herr != nil {
		return nil, herr
	}
	outputObject, merr := memberObject(object, "findings_output")
	if merr != nil {
		return nil, rpc.InvalidParamsError("findings_output must be an object")
	}
	outputPath, policy, exportBudget, herr := decodeOutputDescriptor(outputObject)
	if herr != nil {
		return nil, herr
	}
	writer, herr := fileio.NewExportWriter(outputPath, policy, exportBudget)
	if herr != nil {
		return nil, herr
	}
	sink := sinkFunc(func(finding *iprangedb.ValidationFinding) (iprangedb.ValidationSinkControl, error) {
		row, err := json.Marshal(validationFindingValue(finding))
		if err != nil {
			return iprangedb.SinkContinue, &exportSinkError{message: "validation finding encoding failed"}
		}
		if herr := writer.WriteLine(row, fileio.U64(0)); herr != nil {
			return iprangedb.SinkContinue, &exportSinkError{message: herr.Message}
		}
		return iprangedb.SinkContinue, nil
	})

	switch kind {
	case "immutable_current":
		result, failure := iprangedb.Validate(path, iprangedb.ValidationModeImmutableCurrent, budget, st.Token(), sink)
		return validateTerminal(result, failure, writer)
	case "live_current":
		result, failure := iprangedb.Validate(path, iprangedb.ValidationModeLiveCurrent, budget, st.Token(), sink)
		return validateTerminal(result, failure, writer)
	default:
		candidateObject, merr := memberObject(mode, "candidate")
		if merr != nil {
			writer.Abort()
			return nil, rpc.InvalidParamsError("mode.candidate must be an object")
		}
		candidate, herr := decodeCandidateObject(candidateObject)
		if herr != nil {
			writer.Abort()
			return nil, herr
		}
		result, failure := iprangedb.ValidateOfflineCandidate(path, candidate, budget, st.Token(), sink)
		return validateTerminal(result, failure, writer)
	}
}

// validateTerminal finishes a validation through the export writer:
// the findings file is published before the result is reported, and
// every failing terminal aborts the private temporary and keeps the
// partial progress facts (Rust validate + validation_failure_error).
func validateTerminal(result *iprangedb.ValidationResult, failure *iprangedb.ValidationFailure, writer *fileio.ExportWriter) (any, *rpc.HandlerError) {
	if failure != nil {
		writer.Abort()
		code := validationFailureCode(failure.Cause)
		details := map[string]any{}
		if failure.Progress != nil {
			details["progress"] = progressValue(failure.Progress)
		}
		details["cleanup"] = CleanupArtifactsJSON(failure.Cleanup)
		details["coordination_cleanup"] = CoordinationCleanupJSON(failure.CoordinationCleanup)
		return nil, &rpc.HandlerError{
			Code: code, Outcome: "read_only_failure",
			Message: "validation failed: " + failure.Cause.Error(),
			Details: details,
		}
	}
	facts, herr := writer.Finish()
	if herr != nil {
		return nil, herr
	}
	return boundedResult(map[string]any{
		"method":   "iprange.v1.validate",
		"result":   validationResultValue(result),
		"findings": outputFactsValue(facts),
	})
}

// validationFailureCode maps one validation failure cause to its
// adapter code: a budget refusal inside the findings stream is
// output_limit, exactly like the Rust SinkFailed(BudgetExceeded)
// arm; every other class keeps the shared reader mapping.
func validationFailureCode(cause error) string {
	var typed *iprangedb.Error
	if errors.As(cause, &typed) {
		if typed.Code == iprangedb.ErrorSinkFailed && strings.HasPrefix(typed.Detail, "export refused before exceeding budget") {
			return "output_limit"
		}
		return sdkCode(typed.Code)
	}
	return "io"
}

// exportSinkError is the CLI-side sink failure the SDK worker folds
// into ErrorSinkFailed; the message carries the export writer's fixed
// budget text so validationFailureCode can classify it.
type exportSinkError struct{ message string }

func (e *exportSinkError) Error() string { return e.message }

// sinkFunc adapts one finding callback to the validation sink.
type sinkFunc func(finding *iprangedb.ValidationFinding) (iprangedb.ValidationSinkControl, error)

func (f sinkFunc) Finding(finding *iprangedb.ValidationFinding) (iprangedb.ValidationSinkControl, error) {
	return f(finding)
}

// validationResultValue converts the complete ValidationResult to its
// wire object; the page roots stay private (spec: the API never
// exposes roots or allocator state).
func validationResultValue(result *iprangedb.ValidationResult) map[string]any {
	value := map[string]any{
		"valid":         result.Valid,
		"file_identity": FileIdentityJSONOrError(&result.FileIdentity),
		"progress":      progressValue(&result.Progress),
	}
	if generation := result.Generation; generation != nil {
		value["generation"] = map[string]any{
			"address_family": AddressFamilyName(iprangedb.AddressFamily(generation.AddressFamily)),
			"value_kind":     ValueKindName(iprangedb.ValueKind(generation.ValueKind)),
			"structure_kind": StructureKindName(iprangedb.StructureKind(generation.StructureKind)),
			"value_tag":      valueTagHex(generation.ValueTag),
			"database_id":    HexID(&generation.DatabaseID),
			"transaction_id": DecimalUint(generation.TransactionID),
			"commit_nonce":   HexID(&generation.CommitNonce),
			"page_count":     DecimalUint(generation.PageCount),
		}
	}
	return value
}

// progressValue converts one validation progress to its wire object
// (the bounded possible span is an exact 129-bit decimal).
// valueTagHex renders one generation tag wire form: the content bytes
// before the mandatory NUL terminator (Rust convert::value_tag on
// ValueTag::bytes()).
func valueTagHex(wire [16]byte) map[string]any {
	for i, b := range wire {
		if b == 0 {
			return map[string]any{"hex": HexBytes(wire[:i])}
		}
	}
	return map[string]any{"hex": HexBytes(wire[:])}
}

func progressValue(progress *iprangedb.ValidationProgress) map[string]any {
	return map[string]any{
		"checked_unique_pages":            DecimalUint(progress.CheckedUniquePages),
		"finding_count":                   DecimalUint(progress.FindingCount),
		"untraversable_subgraphs":         DecimalUint(progress.UntraversableSubgraphs),
		"bounded_possible_span_addresses": progress.BoundedPossibleSpanAddresses.String(),
		"has_unbounded_unknown":           progress.HasUnboundedUnknown,
	}
}

// validationFindingValue converts one streamed finding to its wire
// row; optional fields are absent, never null (Rust
// validation_finding_value).
func validationFindingValue(finding *iprangedb.ValidationFinding) map[string]any {
	value := map[string]any{
		"sequence": DecimalUint(finding.Sequence),
		"reason":   reasonName(finding.Reason),
		"object":   objectName(finding.Object),
	}
	if finding.PageNumber != nil {
		value["page_number"] = *finding.PageNumber
	}
	if finding.PhysicalBytes != nil {
		value["physical_bytes"] = map[string]any{
			"start":         DecimalUint(finding.PhysicalBytes.Start),
			"end_exclusive": DecimalUint(finding.PhysicalBytes.EndExclusive),
		}
	}
	if finding.RelatedPageNumber != nil {
		value["related_page_number"] = *finding.RelatedPageNumber
	}
	if finding.AddressFence != nil {
		value["address_fence"] = addressFenceValue(finding.AddressFence)
	}
	return value
}

// unknownEnvelopeValue converts one streamed damage envelope to its
// wire row (Rust unknown_envelope_value).
func unknownEnvelopeValue(envelope *iprangedb.RecoveryUnknownEnvelope) map[string]any {
	value := map[string]any{
		"sequence":                     DecimalUint(envelope.Sequence),
		"reason":                       reasonName(envelope.Reason),
		"object":                       objectName(envelope.Object),
		"contributes_to_possible_span": envelope.ContributesToPossibleSpan,
		"has_unbounded_extent":         envelope.HasUnboundedExtent,
	}
	if envelope.PageNumber != nil {
		value["page_number"] = *envelope.PageNumber
	}
	if envelope.PhysicalBytes != nil {
		value["physical_bytes"] = map[string]any{
			"start":         DecimalUint(envelope.PhysicalBytes.Start),
			"end_exclusive": DecimalUint(envelope.PhysicalBytes.EndExclusive),
		}
	}
	if envelope.AddressFence != nil {
		value["address_fence"] = addressFenceValue(envelope.AddressFence)
	}
	return value
}

// addressFenceValue converts one inclusive logical address fence to
// canonical IP text.
func addressFenceValue(fence *iprangedb.ValidationAddressFence) map[string]any {
	if fence.IPv4 {
		return map[string]any{
			"kind": "ipv4",
			"from": ipv4TextFromUint64(fence.From),
			"to":   ipv4TextFromUint64(fence.To),
		}
	}
	return map[string]any{
		"kind": "ipv6",
		"from": ipv6TextFromBytes(fence.FromV6),
		"to":   ipv6TextFromBytes(fence.ToV6),
	}
}

func ipv4TextFromUint64(value uint64) string {
	return strconv.FormatUint(value>>24, 10) + "." +
		strconv.FormatUint((value>>16)&0xff, 10) + "." +
		strconv.FormatUint((value>>8)&0xff, 10) + "." +
		strconv.FormatUint(value&0xff, 10)
}

func ipv6TextFromBytes(bytes [16]byte) string {
	return netip.AddrFrom16(bytes).String()
}

// reasonName maps one validation reason to its stable wire name.
func reasonName(reason iprangedb.ValidationReason) string {
	switch reason {
	case iprangedb.ReasonMetaUnavailable:
		return "meta_unavailable"
	case iprangedb.ReasonMetaInvalid:
		return "meta_invalid"
	case iprangedb.ReasonMetaStaticMismatch:
		return "meta_static_mismatch"
	case iprangedb.ReasonFileGeometryInvalid:
		return "file_geometry_invalid"
	case iprangedb.ReasonRootCountInvalid:
		return "root_count_invalid"
	case iprangedb.ReasonIoError:
		return "io_error"
	case iprangedb.ReasonArithmeticOverflow:
		return "arithmetic_overflow"
	case iprangedb.ReasonPageOutOfBounds:
		return "page_out_of_bounds"
	case iprangedb.ReasonPageHeaderInvalid:
		return "page_header_invalid"
	case iprangedb.ReasonPageCrcMismatch:
		return "page_crc_mismatch"
	case iprangedb.ReasonPageTypeMismatch:
		return "page_type_mismatch"
	case iprangedb.ReasonPageBornTxnInvalid:
		return "page_born_txn_invalid"
	case iprangedb.ReasonPageReservedNonzero:
		return "page_reserved_nonzero"
	case iprangedb.ReasonTreeCycle:
		return "tree_cycle"
	case iprangedb.ReasonPageAlias:
		return "page_alias"
	case iprangedb.ReasonTreeLevelInvalid:
		return "tree_level_invalid"
	case iprangedb.ReasonTreeOrderInvalid:
		return "tree_order_invalid"
	case iprangedb.ReasonTreeFenceInvalid:
		return "tree_fence_invalid"
	case iprangedb.ReasonRangeReversed:
		return "range_reversed"
	case iprangedb.ReasonRangeOverlap:
		return "range_overlap"
	case iprangedb.ReasonRangeNotCoalesced:
		return "range_not_coalesced"
	case iprangedb.ReasonCatalogNameInvalid:
		return "catalog_name_invalid"
	case iprangedb.ReasonCatalogBijectionInvalid:
		return "catalog_bijection_invalid"
	case iprangedb.ReasonCatalogBitmapInvalid:
		return "catalog_bitmap_invalid"
	case iprangedb.ReasonMembershipBitmapInvalid:
		return "membership_bitmap_invalid"
	case iprangedb.ReasonMembershipHashInvalid:
		return "membership_hash_invalid"
	case iprangedb.ReasonMembershipReverseIndexInvalid:
		return "membership_reverse_index_invalid"
	case iprangedb.ReasonMembershipRefcountInvalid:
		return "membership_refcount_invalid"
	case iprangedb.ReasonMembershipActiveFeedInvalid:
		return "membership_active_feed_invalid"
	case iprangedb.ReasonBlobInvalid:
		return "blob_invalid"
	case iprangedb.ReasonMetadataZlibInvalid:
		return "metadata_zlib_invalid"
	case iprangedb.ReasonMetadataLengthInvalid:
		return "metadata_length_invalid"
	case iprangedb.ReasonBitmapSummaryInvalid:
		return "bitmap_summary_invalid"
	case iprangedb.ReasonAllocationPartitionInvalid:
		return "allocation_partition_invalid"
	case iprangedb.ReasonRetirementOrderInvalid:
		return "retirement_order_invalid"
	case iprangedb.ReasonRetirementListInvalid:
		return "retirement_list_invalid"
	case iprangedb.ReasonCatalogInvalid:
		return "catalog_invalid"
	case iprangedb.ReasonMembershipMissing:
		return "membership_missing"
	case iprangedb.ReasonMembershipInvalid:
		return "membership_invalid"
	case iprangedb.ReasonMetadataInvalid:
		return "metadata_invalid"
	case iprangedb.ReasonStructurePayloadInvalid:
		return "structure_payload_invalid"
	case iprangedb.ReasonStructureHashInvalid:
		return "structure_hash_invalid"
	case iprangedb.ReasonStructureReverseIndexInvalid:
		return "structure_reverse_index_invalid"
	case iprangedb.ReasonStructureRefcountInvalid:
		return "structure_refcount_invalid"
	case iprangedb.ReasonStructureMembershipInvalid:
		return "structure_membership_invalid"
	case iprangedb.ReasonStructureMissing:
		return "structure_missing"
	case iprangedb.ReasonStructureInvalid:
		return "structure_invalid"
	}
	return "unknown"
}

// objectName maps one validation object class to its stable wire name.
func objectName(object iprangedb.ValidationObject) string {
	switch object {
	case iprangedb.ObjectFileGeometry:
		return "file_geometry"
	case iprangedb.ObjectMeta:
		return "meta"
	case iprangedb.ObjectRangeTree:
		return "range_tree"
	case iprangedb.ObjectCatalogNameTree:
		return "catalog_name_tree"
	case iprangedb.ObjectCatalogIndexTree:
		return "catalog_index_tree"
	case iprangedb.ObjectMembershipDictionary:
		return "membership_dictionary"
	case iprangedb.ObjectMembershipReverseIndex:
		return "membership_reverse_index"
	case iprangedb.ObjectMembershipBlob:
		return "membership_blob"
	case iprangedb.ObjectMetadata:
		return "metadata"
	case iprangedb.ObjectFreeBitmap:
		return "free_bitmap"
	case iprangedb.ObjectFeedUsedBitmap:
		return "feed_used_bitmap"
	case iprangedb.ObjectMembershipUsedBitmap:
		return "membership_used_bitmap"
	case iprangedb.ObjectRetirementTree:
		return "retirement_tree"
	case iprangedb.ObjectRetirementBlob:
		return "retirement_blob"
	case iprangedb.ObjectStructureDictionary:
		return "structure_dictionary"
	case iprangedb.ObjectStructureReverseIndex:
		return "structure_reverse_index"
	case iprangedb.ObjectStructureUsedBitmap:
		return "structure_used_bitmap"
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// iprange.v1.recovery.inspect
// ---------------------------------------------------------------------------

// ValidateRecoveryInspectParams enforces the strict inspect schema.
func ValidateRecoveryInspectParams(params json.RawMessage) error {
	object, err := exactObject(params, "path", "mode", "validation_budget")
	if err != nil {
		return err
	}
	path, err := asString(object, "path")
	if err != nil {
		return err
	}
	if err := validatePath(path); err != nil {
		return err
	}
	mode, err := asString(object, "mode")
	if err != nil {
		return err
	}
	if mode != "immutable" && mode != "live" && mode != "caller_certified_offline" {
		return errString("mode must be immutable, live, or caller_certified_offline")
	}
	budget, err := memberObject(object, "validation_budget")
	if err != nil {
		return err
	}
	return validateScratchBudget(budget, false)
}

// RecoveryInspect implements iprange.v1.recovery.inspect: the source
// identity, the classification progress, and the opaque candidate
// tokens (Rust recovery_inspect).
func RecoveryInspect(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	if herr := requireExistingPath(path); herr != nil {
		return nil, herr
	}
	modeName, err := asString(object, "mode")
	if err != nil {
		return nil, rpc.InvalidParamsError("mode must be immutable, live, or caller_certified_offline")
	}
	mode := iprangedb.RecoveryInspectionImmutable
	switch modeName {
	case "live":
		mode = iprangedb.RecoveryInspectionLive
	case "caller_certified_offline":
		mode = iprangedb.RecoveryInspectionOffline
	}
	budget, herr := decodeValidationBudget(object)
	if herr != nil {
		return nil, herr
	}
	inspection, inspectErr := iprangedb.InspectRecoveryCandidates(path, mode, budget, st.Token())
	if inspectErr != nil {
		return nil, readError(inspectErr)
	}
	candidates := make([]any, 0, inspection.CandidateCount())
	for i := 0; i < inspection.CandidateCount(); i++ {
		candidates = append(candidates, candidateValue(inspection.Candidate(i)))
	}
	return boundedResult(map[string]any{
		"method":          "iprange.v1.recovery.inspect",
		"source_identity": FileIdentityJSONOrError(&inspection.SourceIdentity),
		"progress":        progressValue(&inspection.Progress),
		"candidates":      candidates,
	})
}

// candidateValue converts one recovery candidate token to its opaque
// wire object (Rust candidate_value).
func candidateValue(candidate *iprangedb.RecoveryCandidate) map[string]any {
	return map[string]any{
		"label":           candidateLabelName(candidate.Label),
		"meta_page":       candidate.MetaPage,
		"source_identity": FileIdentityJSONOrError(&candidate.SourceIdentity),
		"database_id":     HexID(&candidate.DatabaseID),
		"transaction_id":  DecimalUint(candidate.TransactionID),
		"commit_nonce":    HexID(&candidate.CommitNonce),
	}
}

// candidateLabelName maps one candidate label to its wire name.
func candidateLabelName(label iprangedb.RecoveryCandidateLabel) string {
	switch label {
	case iprangedb.RecoveryCandidateNewest:
		return "newest"
	case iprangedb.RecoveryCandidatePrevious:
		return "previous"
	case iprangedb.RecoveryCandidateUnorderedMeta0:
		return "unordered_meta_0"
	case iprangedb.RecoveryCandidateUnorderedMeta1:
		return "unordered_meta_1"
	}
	return "unknown"
}

// decodeCandidateObject converts one validated opaque candidate wire
// object into the SDK token (Rust candidate_from_value).
func decodeCandidateObject(candidate rawObject) (*iprangedb.RecoveryCandidate, *rpc.HandlerError) {
	labelName, err := asString(candidate, "label")
	if err != nil {
		return nil, rpc.InvalidParamsError("candidate.label is invalid")
	}
	label := iprangedb.RecoveryCandidateNewest
	switch labelName {
	case "previous":
		label = iprangedb.RecoveryCandidatePrevious
	case "unordered_meta_0":
		label = iprangedb.RecoveryCandidateUnorderedMeta0
	case "unordered_meta_1":
		label = iprangedb.RecoveryCandidateUnorderedMeta1
	}
	metaPage, err := asUint32(candidate, "meta_page")
	if err != nil || metaPage > 1 {
		return nil, rpc.InvalidParamsError("candidate.meta_page must be 0 or 1")
	}
	identityObject, err := memberObject(candidate, "source_identity")
	if err != nil {
		return nil, rpc.InvalidParamsError("candidate.source_identity must be an object")
	}
	sourceIdentity, err := decodeIdentityFromObject(identityObject)
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	databaseID, err := hex16FromWire(candidate, "database_id")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	transactionID, err := canonicalU64(candidate["transaction_id"])
	if err != nil {
		return nil, rpc.InvalidParamsError("candidate.transaction_id must be a canonical unsigned decimal string")
	}
	commitNonce, err := hex16FromWire(candidate, "commit_nonce")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	return &iprangedb.RecoveryCandidate{
		Label:          label,
		MetaPage:       uint8(metaPage),
		SourceIdentity: sourceIdentity,
		DatabaseID:     databaseID,
		TransactionID:  transactionID,
		CommitNonce:    commitNonce,
	}, nil
}

// ---------------------------------------------------------------------------
// iprange.v1.recover
// ---------------------------------------------------------------------------

// ValidateRecoverParams enforces the strict recover schema.
func ValidateRecoverParams(params json.RawMessage) error {
	object, err := exactObject(params, "source_path", "source_mode", "candidate", "destination", "recovery_budget", "report_output")
	if err != nil {
		return err
	}
	sourcePath, err := asString(object, "source_path")
	if err != nil {
		return err
	}
	if err := validatePath(sourcePath); err != nil {
		return err
	}
	sourceMode, err := asString(object, "source_mode")
	if err != nil {
		return err
	}
	if sourceMode != "immutable" && sourceMode != "live" && sourceMode != "caller_certified_offline" {
		return errString("source_mode must be immutable, live, or caller_certified_offline")
	}
	candidate, err := memberObject(object, "candidate")
	if err != nil {
		return err
	}
	if err := validateCandidateObject(candidate); err != nil {
		return err
	}
	destination, err := asString(object, "destination")
	if err != nil {
		return err
	}
	if err := validatePath(destination); err != nil {
		return err
	}
	budget, err := memberObject(object, "recovery_budget")
	if err != nil {
		return err
	}
	if err := validateScratchBudget(budget, true); err != nil {
		return err
	}
	output, err := memberObject(object, "report_output")
	if err != nil {
		return err
	}
	return validateOutputDescriptor(output)
}

// Recover implements iprange.v1.recover: one bounded recovery of the
// exact candidate into a fresh absent destination; damage envelopes
// stream as JSONL rows under the report budget (Rust recover).
func Recover(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	sourcePath, err := asString(object, "source_path")
	if err != nil {
		return nil, rpc.InvalidParamsError("source_path must be a string")
	}
	if herr := requireExistingPath(sourcePath); herr != nil {
		return nil, herr
	}
	sourceMode, err := asString(object, "source_mode")
	if err != nil {
		return nil, rpc.InvalidParamsError("source_mode must be immutable, live, or caller_certified_offline")
	}
	candidateObject, merr := memberObject(object, "candidate")
	if merr != nil {
		return nil, rpc.InvalidParamsError("candidate must be an object")
	}
	candidate, herr := decodeCandidateObject(candidateObject)
	if herr != nil {
		return nil, herr
	}
	destination, err := asString(object, "destination")
	if err != nil {
		return nil, rpc.InvalidParamsError("destination must be a string")
	}
	budget, herr := decodeRecoveryBudget(object)
	if herr != nil {
		return nil, herr
	}
	reportObject, merr := memberObject(object, "report_output")
	if merr != nil {
		return nil, rpc.InvalidParamsError("report_output must be an object")
	}
	reportPath, policy, exportBudget, herr := decodeOutputDescriptor(reportObject)
	if herr != nil {
		return nil, herr
	}
	writer, herr := fileio.NewExportWriter(reportPath, policy, exportBudget)
	if herr != nil {
		return nil, herr
	}
	sink := recoverySinkFunc(func(envelope *iprangedb.RecoveryUnknownEnvelope) (iprangedb.RecoverySinkControl, error) {
		row, err := json.Marshal(unknownEnvelopeValue(envelope))
		if err != nil {
			return iprangedb.RecoverySinkContinue, &exportSinkError{message: "recovery envelope encoding failed"}
		}
		if herr := writer.WriteLine(row, fileio.U64(0)); herr != nil {
			return iprangedb.RecoverySinkContinue, &exportSinkError{message: herr.Message}
		}
		return iprangedb.RecoverySinkContinue, nil
	})

	var result *iprangedb.RecoveryResult
	var failure *iprangedb.RecoveryPreparationFailure
	switch sourceMode {
	case "immutable":
		result, failure = iprangedb.RecoverImmutable(sourcePath, candidate, destination, budget, sink, st.Token())
	case "live":
		result, failure = iprangedb.RecoverLive(sourcePath, candidate, destination, budget, sink, st.Token())
	default:
		result, failure = iprangedb.RecoverOffline(sourcePath, candidate, destination, iprangedb.CallerCertified, budget, sink, st.Token())
	}
	if failure != nil {
		writer.Abort()
		return nil, recoveryFailureError(failure)
	}
	// The report file is published before the publication-cause check
	// so a damaged publication still reports the completed facts.
	facts, herr := writer.Finish()
	if herr != nil {
		return nil, herr
	}
	publication, convErr := PublicationResultJSON(&result.Publication)
	if convErr != nil {
		return nil, convErr
	}
	if result.Publication.Cause != nil {
		code := sdkCodeOf(result.Publication.Cause)
		details := map[string]any{
			"report":        recoveryReportValue(&result.Report),
			"publication":   publication,
			"report_output": outputFactsValue(facts),
		}
		if result.Scratch != nil {
			details["scratch"] = recoveryScratchValue(result.Scratch)
		}
		return nil, &rpc.HandlerError{
			Code: code, Outcome: publicationStatusOutcome(result.Publication.Publication),
			Message: "recovery publication failed: " + result.Publication.Cause.Error(),
			Details: details,
		}
	}
	value := map[string]any{
		"method":      "iprange.v1.recover",
		"report":      recoveryReportValue(&result.Report),
		"publication": publication,
	}
	if result.Scratch != nil {
		value["scratch"] = recoveryScratchValue(result.Scratch)
	}
	return boundedResult(value)
}

// recoverySinkFunc adapts one envelope callback to the recovery sink.
type recoverySinkFunc func(envelope *iprangedb.RecoveryUnknownEnvelope) (iprangedb.RecoverySinkControl, error)

func (f recoverySinkFunc) Unknown(envelope *iprangedb.RecoveryUnknownEnvelope) (iprangedb.RecoverySinkControl, error) {
	return f(envelope)
}

// recoveryFailureError converts one failed recovery preparation: the
// outcome is not_published when any output artifact exists or the
// cleanup ledger is non-empty, else not_started (Rust
// recovery_failure_error).
func recoveryFailureError(failure *iprangedb.RecoveryPreparationFailure) *rpc.HandlerError {
	outcome := "not_started"
	if failure.Output != nil || !failure.Cleanup.Empty() {
		outcome = "not_published"
	}
	details := map[string]any{
		"report":               recoveryReportValue(&failure.Report),
		"cleanup":              CleanupArtifactsJSON(failure.Cleanup),
		"coordination_cleanup": CoordinationCleanupJSON(failure.CoordinationCleanup),
		"housekeeping":         HousekeepingJSON(failure.Housekeeping, failure.VisibleHousekeeping),
		"visible_housekeeping": VisibleHousekeepingJSON(failure.VisibleHousekeeping),
	}
	if failure.Scratch != nil {
		details["scratch"] = recoveryScratchValue(failure.Scratch)
	}
	if failure.Output != nil {
		details["output"] = privateOutputAttemptValue(failure.Output)
	}
	return &rpc.HandlerError{
		Code: sdkCodeOf(failure.Cause), Outcome: outcome,
		Message: "recovery preparation failed: " + failure.Cause.Error(),
		Details: details,
	}
}

// recoveryReportValue converts the complete recovery report to its
// wire object (results.py RECOVERY_REPORT).
func recoveryReportValue(report *iprangedb.RecoveryReport) map[string]any {
	return map[string]any{
		"pages":                           pageCountsValue(&report.Pages),
		"ranges":                          logicalCountsValue(&report.Ranges),
		"catalog_entries":                 logicalCountsValue(&report.CatalogEntries),
		"membership_entries":              logicalCountsValue(&report.MembershipEntries),
		"structure_entries":               logicalCountsValue(&report.StructureEntries),
		"metadata_chunks":                 logicalCountsValue(&report.MetadataChunks),
		"retirement_records":              logicalCountsValue(&report.RetirementRecords),
		"verified_addresses":              report.VerifiedAddresses.String(),
		"rejected_addresses":              report.RejectedAddresses.String(),
		"bounded_possible_span_addresses": report.BoundedPossibleSpanAddresses.String(),
		"has_unbounded_unknown":           report.HasUnboundedUnknown,
		"unknown_envelopes":               DecimalUint(report.UnknownEnvelopes),
	}
}

func pageCountsValue(value *iprangedb.RecoveryPageCounts) map[string]any {
	return map[string]any{
		"examined":      DecimalUint(value.Examined),
		"accepted":      DecimalUint(value.Accepted),
		"rejected":      DecimalUint(value.Rejected),
		"io_unreadable": DecimalUint(value.IOUnreadable),
	}
}

func logicalCountsValue(value *iprangedb.RecoveryLogicalCounts) map[string]any {
	return map[string]any{
		"examined": DecimalUint(value.Examined),
		"accepted": DecimalUint(value.Accepted),
		"rejected": DecimalUint(value.Rejected),
	}
}

// recoveryScratchValue converts one authorized scratch attempt to its
// wire object (Rust recovery_scratch_value).
func recoveryScratchValue(value *iprangedb.RecoveryScratchAttempt) map[string]any {
	return map[string]any{
		"attempt_id":         HexID(&value.AttemptID),
		"directory_identity": FileIdentityJSONOrError(&value.DirectoryIdentity),
		"creation_security":  CreationSecurityJSON(&value.CreationSecurity),
	}
}

// decodeValidationBudget converts the validated wire budget into the
// SDK ValidationBudget.
func decodeValidationBudget(object rawObject) (*iprangedb.ValidationBudget, *rpc.HandlerError) {
	budget, err := memberObject(object, "validation_budget")
	if err != nil {
		return nil, rpc.InvalidParamsError("validation_budget must be an object")
	}
	heap, err := canonicalU64(budget["max_heap_bytes"])
	if err != nil {
		return nil, rpc.InvalidParamsError("validation_budget.max_heap_bytes is invalid")
	}
	openFiles, err := asUint32(budget, "max_open_files")
	if err != nil {
		return nil, rpc.InvalidParamsError("validation_budget.max_open_files must be a u32 integer")
	}
	scratchBytes, err := canonicalU64(budget["max_scratch_bytes"])
	if err != nil {
		return nil, rpc.InvalidParamsError("validation_budget.max_scratch_bytes is invalid")
	}
	scratchFiles, err := asUint32(budget, "max_scratch_files")
	if err != nil {
		return nil, rpc.InvalidParamsError("validation_budget.max_scratch_files must be a u32 integer")
	}
	scratchDirectory := ""
	if raw, ok := budget["scratch_directory"]; ok && !isRawNull(raw) {
		scratchDirectory, err = asString(budget, "scratch_directory")
		if err != nil {
			return nil, rpc.InvalidParamsError("validation_budget.scratch_directory must be a string")
		}
	}
	return &iprangedb.ValidationBudget{
		MaxHeapBytes:     heap,
		MaxOpenFiles:     openFiles,
		MaxScratchBytes:  scratchBytes,
		MaxScratchFiles:  scratchFiles,
		ScratchDirectory: scratchDirectory,
	}, nil
}

// decodeRecoveryBudget converts the validated wire budget into the
// SDK RecoveryBudget.
func decodeRecoveryBudget(object rawObject) (*iprangedb.RecoveryBudget, *rpc.HandlerError) {
	budget, err := memberObject(object, "recovery_budget")
	if err != nil {
		return nil, rpc.InvalidParamsError("recovery_budget must be an object")
	}
	heap, err := canonicalU64(budget["max_heap_bytes"])
	if err != nil {
		return nil, rpc.InvalidParamsError("recovery_budget.max_heap_bytes is invalid")
	}
	outputPages, err := canonicalU64(budget["max_output_pages"])
	if err != nil {
		return nil, rpc.InvalidParamsError("recovery_budget.max_output_pages is invalid")
	}
	openFiles, err := asUint32(budget, "max_open_files")
	if err != nil {
		return nil, rpc.InvalidParamsError("recovery_budget.max_open_files must be a u32 integer")
	}
	scratchBytes, err := canonicalU64(budget["max_scratch_bytes"])
	if err != nil {
		return nil, rpc.InvalidParamsError("recovery_budget.max_scratch_bytes is invalid")
	}
	scratchFiles, err := asUint32(budget, "max_scratch_files")
	if err != nil {
		return nil, rpc.InvalidParamsError("recovery_budget.max_scratch_files must be a u32 integer")
	}
	scratchDirectory := ""
	if raw, ok := budget["scratch_directory"]; ok && !isRawNull(raw) {
		scratchDirectory, err = asString(budget, "scratch_directory")
		if err != nil {
			return nil, rpc.InvalidParamsError("recovery_budget.scratch_directory must be a string")
		}
	}
	return &iprangedb.RecoveryBudget{
		MaxHeapBytes:     heap,
		MaxOutputPages:   outputPages,
		MaxOpenFiles:     openFiles,
		MaxScratchBytes:  scratchBytes,
		MaxScratchFiles:  scratchFiles,
		ScratchDirectory: scratchDirectory,
	}, nil
}

// RegisterRecovery installs the validation and recovery handler
// family.
func RegisterRecovery() {
	rpc.Register("iprange.v1.validate", ValidateValidateParams, Validate)
	rpc.Register("iprange.v1.recovery.inspect", ValidateRecoveryInspectParams, RecoveryInspect)
	rpc.Register("iprange.v1.recover", ValidateRecoverParams, Recover)
}
