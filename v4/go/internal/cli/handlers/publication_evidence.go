// Publication evidence encoding (Rust handlers/publication_evidence.rs
// and lifecycle.rs artifact conversions parity). The complete
// PublicationResult wire object is the single encoder for every
// publication-producing handler family; the strict inverse (wire to
// SDK object) is a public Go SDK function because the SDK hides the
// evidence field types (publication.resolve uses it).

package handlers

import (
	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// PublicationPolicyName maps one publication policy to its wire name.
func PublicationPolicyName(policy iprangedb.PublicationPolicy) string {
	switch policy {
	case iprangedb.PolicyFailIfExists:
		return "fail_if_exists"
	case iprangedb.PolicyReplaceExisting:
		return "replace_existing"
	case iprangedb.PolicyReplaceExistingNoRollback:
		return "replace_existing_no_rollback"
	}
	return "unknown"
}

// PublicationStatusName maps one publication status to its wire name.
func PublicationStatusName(status iprangedb.PublicationStatus) string {
	switch status {
	case iprangedb.PublicationNotPublished:
		return "not_published"
	case iprangedb.PublicationPublished:
		return "published"
	case iprangedb.PublicationOutcomeUnknown:
		return "outcome_unknown"
	}
	return "outcome_unknown"
}

// DestinationContentName maps one destination-content class to its
// wire name.
func DestinationContentName(content iprangedb.DestinationContent) string {
	switch content {
	case iprangedb.DestinationContentDesired:
		return "desired"
	case iprangedb.DestinationContentPrevious:
		return "previous"
	case iprangedb.DestinationContentAbsent:
		return "absent"
	case iprangedb.DestinationContentOther:
		return "other"
	case iprangedb.DestinationContentUnclassified:
		return "unclassified"
	}
	return "unclassified"
}

// LaterCanonicalName maps one later-canonical class to its wire name.
func LaterCanonicalName(value iprangedb.LaterCanonical) string {
	switch value {
	case iprangedb.LaterCanonicalNone:
		return "none"
	case iprangedb.LaterCanonicalReservationOrTransition:
		return "reservation_or_transition"
	case iprangedb.LaterCanonicalReadyLiveSidecar:
		return "ready_live_sidecar"
	}
	return "none"
}

// AccessPolicyName maps one access-policy class to its wire name.
func AccessPolicyName(value iprangedb.AccessPolicy) string {
	switch value {
	case iprangedb.AccessPolicyCreatorOnly:
		return "creator_only"
	case iprangedb.AccessPolicyChangedOrUnproven:
		return "changed_or_unproven"
	case iprangedb.AccessPolicyUnclassified:
		return "unclassified"
	case iprangedb.AccessPolicyAbsent:
		return "absent"
	}
	return "absent"
}

// CreationSecurityJSON converts the creator-only security evidence.
func CreationSecurityJSON(value *iprangedb.CreationSecurity) map[string]any {
	if value == nil {
		return map[string]any{"kind": 0, "commitment": ""}
	}
	return map[string]any{
		"kind":       value.Kind,
		"commitment": HexBytes(value.Commitment[:]),
	}
}

// HousekeepingStateName maps one housekeeping state to its wire name.
func HousekeepingStateName(value iprangedb.HousekeepingState) string {
	switch value {
	case iprangedb.HousekeepingMovePending:
		return "move_pending"
	case iprangedb.HousekeepingMoveAmbiguous:
		return "move_ambiguous"
	case iprangedb.HousekeepingInert:
		return "inert"
	case iprangedb.HousekeepingConflict:
		return "conflict"
	}
	return "move_pending"
}

// DirectoryRoleName maps one directory role to its wire name.
func DirectoryRoleName(value iprangedb.DirectoryRole) string {
	switch value {
	case iprangedb.DirectoryRoleDestination:
		return "destination"
	case iprangedb.DirectoryRoleScratchDirectory:
		return "scratch_directory"
	case iprangedb.DirectoryRoleMainFile:
		return "main_file"
	}
	return "destination"
}

// ArtifactPresenceName maps one artifact-presence class to its wire
// name.
func ArtifactPresenceName(value iprangedb.ArtifactPresence) string {
	switch value {
	case iprangedb.ArtifactAbsent:
		return "absent"
	case iprangedb.ArtifactPresent:
		return "present"
	case iprangedb.ArtifactUnclassified:
		return "unclassified"
	}
	return "unclassified"
}

// ArtifactKindName maps one artifact kind to its wire name.
func ArtifactKindName(value iprangedb.ArtifactKind) string {
	switch value {
	case iprangedb.ArtifactPrivateOutput:
		return "private_output"
	case iprangedb.ArtifactPrivateReservation:
		return "private_reservation"
	case iprangedb.ArtifactOwnedCoordination:
		return "owned_coordination"
	case iprangedb.ArtifactAuthorizedScratch:
		return "authorized_scratch"
	case iprangedb.ArtifactOwnedMain:
		return "owned_main"
	case iprangedb.ArtifactUnpublishedMainTail:
		return "unpublished_main_tail"
	}
	return "private_output"
}

// HousekeepingArtifactJSON converts one housekeeping artifact.
func HousekeepingArtifactJSON(value *iprangedb.HousekeepingArtifact) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	result := map[string]any{
		"state":                      HousekeepingStateName(value.State),
		"directory_role":             DirectoryRoleName(value.DirectoryRole),
		"directory_identity":         FileIdentityJSONOrError(&value.DirectoryIdentity),
		"basename_encoding":          value.BasenameEncoding,
		"attempt_id":                 HexID(&value.AttemptID),
		"ordinal":                    value.Ordinal,
		"envelope_basename":          string(value.EnvelopeBasename),
		"envelope_identity":          FileIdentityJSONOrError(&value.EnvelopeIdentity),
		"source_basename":            string(value.SourceBasename),
		"inert_basename":             string(value.InertBasename),
		"source_presence":            ArtifactPresenceName(value.SourcePresence),
		"inert_presence":             ArtifactPresenceName(value.InertPresence),
		"kind":                       ArtifactKindName(value.Kind),
		"creation_security":          CreationSecurityJSON(&value.CreationSecurity),
		"selected_envelope_sequence": DecimalUint(value.SelectedEnvelopeSequence),
	}
	// Optional SDK fields are absent, never null (wire rule).
	if value.SourceIdentity != nil {
		result["source_identity"] = FileIdentityJSONOrError(value.SourceIdentity)
	}
	if value.InertIdentity != nil {
		result["inert_identity"] = FileIdentityJSONOrError(value.InertIdentity)
	}
	return result
}

// HousekeepingJSON converts the housekeeping evidence class plus its
// visible artifact ledger.
func HousekeepingJSON(state iprangedb.Housekeeping, visible []iprangedb.HousekeepingArtifact) map[string]any {
	artifacts := make([]any, 0, len(visible))
	for i := range visible {
		artifacts = append(artifacts, HousekeepingArtifactJSON(&visible[i]))
	}
	switch state {
	case iprangedb.HousekeepingNone:
		return map[string]any{"artifacts": []any{}}
	case iprangedb.HousekeepingCrashReappearancePossible:
		return map[string]any{"state": "crash_reappearance_possible", "artifacts": artifacts}
	case iprangedb.HousekeepingVisible:
		return map[string]any{"state": "visible", "artifacts": artifacts}
	}
	return map[string]any{"artifacts": []any{}}
}

// CleanupArtifactJSON converts one cleanup artifact.
func CleanupArtifactJSON(value *iprangedb.CleanupArtifact) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	result := map[string]any{
		"kind":               ArtifactKindName(value.Kind),
		"directory_role":     DirectoryRoleName(value.DirectoryRole),
		"directory_identity": FileIdentityJSONOrError(&value.DirectoryIdentity),
		"basename_encoding":  value.BasenameEncoding,
		"basename":           HexBytes(value.Basename),
		"error":              PublicationProblemJSON(value.Error),
	}
	if value.Identity != nil {
		result["identity"] = FileIdentityJSONOrError(value.Identity)
	}
	if value.CreationSecurity != nil {
		result["creation_security"] = CreationSecurityJSON(value.CreationSecurity)
	}
	if tail := value.UnpublishedTail; tail != nil {
		result["unpublished_tail"] = map[string]any{
			"expected_database_id":            HexID(&tail.ExpectedDatabaseID),
			"committed_target_transaction_id": DecimalUint(tail.CommittedTargetTransactionID),
			"committed_target_nonce":          HexID(&tail.CommittedTargetNonce),
			"committed_target_length":         DecimalUint(tail.CommittedTargetLength),
			"observed_tail_end_exclusive":     DecimalUint(tail.ObservedTailEndExclusive),
		}
	}
	return result
}

// PublicationProblemJSON converts the fixed publication problem of one
// failed operation.
func PublicationProblemJSON(err error) map[string]any {
	code := "io"
	detail := ""
	if typed, ok := err.(*iprangedb.Error); ok {
		code = sdkCode(typed.Code)
		detail = typed.Detail
	}
	// The Go SDK folds the OS-level code into the detail; the wire
	// field is omitted, which the schema treats as absent.
	return map[string]any{"code": code, "detail": detail}
}

// PublicationAttemptJSON converts one exact publication attempt.
func PublicationAttemptJSON(attempt *iprangedb.PublicationAttempt) (map[string]any, *rpc.HandlerError) {
	if attempt == nil {
		return nil, rpc.NewHandlerError("io", "not_started", "missing publication attempt")
	}
	result := map[string]any{
		"database_id":                   HexID(&attempt.DatabaseID),
		"transaction_id":                DecimalUint(attempt.TransactionID),
		"commit_nonce":                  HexID(&attempt.CommitNonce),
		"publication_attempt_id":        HexID(&attempt.PublicationAttemptID),
		"directory_identity":            FileIdentityJSONOrError(&attempt.DirectoryIdentity),
		"destination_basename_encoding": attempt.DestinationBasenameEncoding,
		"destination_basename":          Base64Padded(attempt.DestinationBasename),
		"output_identity":               FileIdentityJSONOrError(&attempt.OutputIdentity),
		"output_byte_length":            DecimalUint(attempt.OutputByteLength),
		"output_sha512":                 HexBytes(attempt.OutputSHA512[:]),
		"publication_policy":            PublicationPolicyName(attempt.PublicationPolicy),
		"reservation_identity":          FileIdentityJSONOrError(&attempt.ReservationIdentity),
		"creation_security":             CreationSecurityJSON(&attempt.CreationSecurity),
	}
	if attempt.PreviousDestination != nil {
		result["previous_destination"] = map[string]any{
			"identity":    FileIdentityJSONOrError(&attempt.PreviousDestination.Identity),
			"byte_length": DecimalUint(attempt.PreviousDestination.ByteLength),
			"sha512":      HexBytes(attempt.PreviousDestination.SHA512[:]),
		}
	}
	return result, nil
}

// PublicationResultJSON is the complete mechanical PublicationResult
// conversion; the single encoder for every publication-producing
// handler family.
func PublicationResultJSON(result *iprangedb.PublicationResult) (map[string]any, *rpc.HandlerError) {
	if result == nil {
		return nil, rpc.NewHandlerError("io", "not_started", "missing publication result")
	}
	attempt, herr := PublicationAttemptJSON(&result.Attempt)
	if herr != nil {
		return nil, herr
	}
	value := map[string]any{
		"attempt":                                attempt,
		"main_namespace_may_have_been_attempted": result.MainNamespaceMayHaveBeenAttempted,
		"publication":                            PublicationStatusName(result.Publication),
		"destination_content":                    DestinationContentName(result.DestinationContent),
		"later_canonical":                        LaterCanonicalName(result.LaterCanonical),
		"main_access_policy":                     AccessPolicyName(result.MainAccessPolicy),
		"coordination_access_policy":             AccessPolicyName(result.CoordinationAccessPolicy),
		"cleanup":                                CleanupArtifactsJSON(result.Cleanup),
		"coordination_cleanup":                   CoordinationCleanupJSON(result.CoordinationCleanup),
		"housekeeping":                           HousekeepingJSON(result.Housekeeping, result.VisibleHousekeeping),
		"visible_housekeeping":                   VisibleHousekeepingJSON(result.VisibleHousekeeping),
	}
	if result.LiveLineage != nil {
		value["live_lineage"] = map[string]any{"kind": LiveLineageName(*result.LiveLineage)}
	}
	if result.LaterAttemptOrSidecarID != nil {
		value["later_attempt_or_sidecar_id"] = HexID(result.LaterAttemptOrSidecarID)
	}
	if result.LaterSelectedTransactionID != nil {
		value["later_selected_transaction_id"] = DecimalUint(*result.LaterSelectedTransactionID)
	}
	if result.LaterSelectedCommitNonce != nil {
		value["later_selected_commit_nonce"] = HexID(result.LaterSelectedCommitNonce)
	}
	return value, nil
}

// LiveLineageName maps one live-lineage class to its wire name.
func LiveLineageName(value iprangedb.LiveLineage) string {
	switch value {
	case iprangedb.LiveLineageSameGenerationExactBytes:
		return "same_generation_exact_bytes"
	case iprangedb.LiveLineageSameGenerationPhysicalBytesChanged:
		return "same_generation_physical_bytes_changed"
	case iprangedb.LiveLineageAdvancedGeneration:
		return "advanced_generation"
	}
	return "same_generation_exact_bytes"
}

// CleanupArtifactsJSON converts the cleanup ledger; an empty ledger
// yields {} (absent, never null).
func CleanupArtifactsJSON(cleanup iprangedb.CleanupArtifacts) map[string]any {
	if cleanup.Empty() {
		return map[string]any{}
	}
	artifacts := make([]any, 0, cleanup.Len())
	for i := 0; i < cleanup.Len(); i++ {
		artifacts = append(artifacts, CleanupArtifactJSON(cleanup.At(i)))
	}
	return map[string]any{"artifacts": artifacts}
}

// VisibleHousekeepingJSON converts the visible artifact ledger.
func VisibleHousekeepingJSON(visible []iprangedb.HousekeepingArtifact) []any {
	artifacts := make([]any, 0, len(visible))
	for i := range visible {
		artifacts = append(artifacts, HousekeepingArtifactJSON(&visible[i]))
	}
	return artifacts
}
