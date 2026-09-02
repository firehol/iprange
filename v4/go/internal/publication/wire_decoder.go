// Strict JSON-RPC wire decoder for PublicationResult
// (iprange-jsonrpc-v1.md `publication_result`; Rust
// publication_evidence::decode_publication_result parity).
//
// The decoder is the single public entry point for reconstructed
// evidence: the CLI product adapters may use only the public module,
// and the SDK hides the evidence field types, so this wire decoder
// lives here. It is strict: missing, extra, stale, or foreign
// evidence fails before any destructive action.

package publication

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// DecodePublicationResultWire reconstructs the exact SDK publication
// result from its preserved wire object.
func DecodePublicationResultWire(data []byte) (*PublicationResult, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("publication_result must be an object")
	}
	required := []string{
		"attempt", "main_namespace_may_have_been_attempted", "publication",
		"destination_content", "later_canonical", "main_access_policy",
		"coordination_access_policy", "cleanup", "coordination_cleanup",
		"housekeeping", "visible_housekeeping",
	}
	optional := []string{
		"live_lineage", "later_attempt_or_sidecar_id",
		"later_selected_transaction_id", "later_selected_commit_nonce",
	}
	if err := exactMembers(object, required, optional); err != nil {
		return nil, err
	}
	attempt, err := decodePublicationAttempt(object["attempt"])
	if err != nil {
		return nil, err
	}
	mainNamespace, err := decodeBool(object["main_namespace_may_have_been_attempted"], "main_namespace_may_have_been_attempted")
	if err != nil {
		return nil, err
	}
	publication, err := decodePublicationStatus(object["publication"])
	if err != nil {
		return nil, err
	}
	destinationContent, err := decodeDestinationContent(object["destination_content"])
	if err != nil {
		return nil, err
	}
	laterCanonical, err := decodeLaterCanonical(object["later_canonical"])
	if err != nil {
		return nil, err
	}
	liveLineage, err := decodeLiveLineage(rawOr(object, "live_lineage"))
	if err != nil {
		return nil, err
	}
	var laterAttemptOrSidecarID *[16]byte
	if raw, ok := object["later_attempt_or_sidecar_id"]; ok {
		value, err := decodeHex16(raw, "later_attempt_or_sidecar_id")
		if err != nil {
			return nil, err
		}
		laterAttemptOrSidecarID = &value
	}
	var laterSelectedTransactionID *uint64
	if raw, ok := object["later_selected_transaction_id"]; ok {
		value, err := decodeDecimalUint64(raw, "later_selected_transaction_id")
		if err != nil {
			return nil, err
		}
		laterSelectedTransactionID = &value
	}
	var laterSelectedCommitNonce *[16]byte
	if raw, ok := object["later_selected_commit_nonce"]; ok {
		value, err := decodeHex16(raw, "later_selected_commit_nonce")
		if err != nil {
			return nil, err
		}
		laterSelectedCommitNonce = &value
	}
	mainAccessPolicy, err := decodeAccessPolicy(object["main_access_policy"])
	if err != nil {
		return nil, err
	}
	coordinationAccessPolicy, err := decodeAccessPolicy(object["coordination_access_policy"])
	if err != nil {
		return nil, err
	}
	cleanup, err := decodePublicationCleanup(object["cleanup"])
	if err != nil {
		return nil, err
	}
	coordinationCleanup, err := decodeCoordinationCleanup(object["coordination_cleanup"])
	if err != nil {
		return nil, err
	}
	housekeeping, err := decodeHousekeeping(object["housekeeping"])
	if err != nil {
		return nil, err
	}
	visibleHousekeeping, err := decodeHousekeepingArtifacts(object["visible_housekeeping"])
	if err != nil {
		return nil, err
	}
	return &PublicationResult{
		Attempt:                           attempt,
		MainNamespaceMayHaveBeenAttempted: mainNamespace,
		Publication:                       publication,
		DestinationContent:                destinationContent,
		LaterCanonical:                    laterCanonical,
		LiveLineage:                       liveLineage,
		LaterAttemptOrSidecarID:           laterAttemptOrSidecarID,
		LaterSelectedTransactionID:        laterSelectedTransactionID,
		LaterSelectedCommitNonce:          laterSelectedCommitNonce,
		MainAccessPolicy:                  mainAccessPolicy,
		CoordinationAccessPolicy:          coordinationAccessPolicy,
		Cleanup:                           cleanup,
		CoordinationCleanup:               coordinationCleanup,
		Housekeeping:                      housekeeping,
		VisibleHousekeeping:               visibleHousekeeping,
	}, nil
}

func decodePublicationAttempt(raw json.RawMessage) (PublicationAttempt, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return PublicationAttempt{}, fmt.Errorf("attempt must be an object")
	}
	if err := exactMembers(object, []string{
		"database_id", "transaction_id", "commit_nonce", "publication_attempt_id",
		"directory_identity", "destination_basename_encoding", "destination_basename",
		"output_identity", "output_byte_length", "output_sha512", "publication_policy",
		"reservation_identity", "creation_security",
	}, []string{"previous_destination"}); err != nil {
		return PublicationAttempt{}, err
	}
	databaseID, err := decodeHex16(object["database_id"], "database_id")
	if err != nil {
		return PublicationAttempt{}, err
	}
	transactionID, err := decodeDecimalUint64(object["transaction_id"], "transaction_id")
	if err != nil {
		return PublicationAttempt{}, err
	}
	commitNonce, err := decodeHex16(object["commit_nonce"], "commit_nonce")
	if err != nil {
		return PublicationAttempt{}, err
	}
	publicationAttemptID, err := decodeHex16(object["publication_attempt_id"], "publication_attempt_id")
	if err != nil {
		return PublicationAttempt{}, err
	}
	directoryIdentity, err := decodeFileIdentity(object["directory_identity"])
	if err != nil {
		return PublicationAttempt{}, err
	}
	basenameEncoding, err := decodeUint16(object["destination_basename_encoding"], "destination_basename_encoding")
	if err != nil {
		return PublicationAttempt{}, err
	}
	basenameText, err := decodeString(object["destination_basename"], "destination_basename")
	if err != nil {
		return PublicationAttempt{}, err
	}
	destinationBasename, err := base64.StdEncoding.DecodeString(basenameText)
	if err != nil {
		return PublicationAttempt{}, fmt.Errorf("destination_basename: %v", err)
	}
	outputIdentity, err := decodeFileIdentity(object["output_identity"])
	if err != nil {
		return PublicationAttempt{}, err
	}
	outputByteLength, err := decodeDecimalUint64(object["output_byte_length"], "output_byte_length")
	if err != nil {
		return PublicationAttempt{}, err
	}
	outputSHA512, err := decodeHex64(object["output_sha512"], "output_sha512")
	if err != nil {
		return PublicationAttempt{}, err
	}
	publicationPolicy, err := decodePublicationPolicy(object["publication_policy"])
	if err != nil {
		return PublicationAttempt{}, err
	}
	previousDestination, err := decodePreviousDestination(rawOr(object, "previous_destination"))
	if err != nil {
		return PublicationAttempt{}, err
	}
	reservationIdentity, err := decodeFileIdentity(object["reservation_identity"])
	if err != nil {
		return PublicationAttempt{}, err
	}
	creationSecurity, err := decodeCreationSecurity(object["creation_security"])
	if err != nil {
		return PublicationAttempt{}, err
	}
	return PublicationAttempt{
		DatabaseID:                  databaseID,
		TransactionID:               transactionID,
		CommitNonce:                 commitNonce,
		PublicationAttemptID:        publicationAttemptID,
		DirectoryIdentity:           directoryIdentity,
		DestinationBasenameEncoding: basenameEncoding,
		DestinationBasename:         destinationBasename,
		OutputIdentity:              outputIdentity,
		OutputByteLength:            outputByteLength,
		OutputSHA512:                outputSHA512,
		PublicationPolicy:           publicationPolicy,
		PreviousDestination:         previousDestination,
		ReservationIdentity:         reservationIdentity,
		CreationSecurity:            creationSecurity,
	}, nil
}

func decodePublicationPolicy(raw json.RawMessage) (PublicationPolicy, error) {
	value, err := decodeString(raw, "publication_policy")
	if err != nil {
		return 0, err
	}
	switch value {
	case "fail_if_exists":
		return PolicyFailIfExists, nil
	case "replace_existing":
		return PolicyReplaceExisting, nil
	case "replace_existing_no_rollback":
		return PolicyReplaceExistingNoRollback, nil
	}
	return 0, fmt.Errorf("publication_policy must be fail_if_exists, replace_existing, or replace_existing_no_rollback")
}

func decodePreviousDestination(raw json.RawMessage) (*PreviousDestination, error) {
	if raw == nil {
		return nil, nil
	}
	if isNull(raw) {
		return nil, fmt.Errorf("previous_destination must not be null; absent is the only absent form")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("previous_destination must be an object")
	}
	if err := exactMembers(object, []string{"identity", "byte_length", "sha512"}, nil); err != nil {
		return nil, err
	}
	identity, err := decodeFileIdentity(object["identity"])
	if err != nil {
		return nil, err
	}
	byteLength, err := decodeDecimalUint64(object["byte_length"], "previous_destination.byte_length")
	if err != nil {
		return nil, err
	}
	sha512, err := decodeHex64(object["sha512"], "previous_destination.sha512")
	if err != nil {
		return nil, err
	}
	return &PreviousDestination{Identity: identity, ByteLength: byteLength, SHA512: sha512}, nil
}

func decodePublicationStatus(raw json.RawMessage) (PublicationStatus, error) {
	value, err := decodeString(raw, "publication")
	if err != nil {
		return 0, err
	}
	switch value {
	case "not_published":
		return PublicationNotPublished, nil
	case "published":
		return PublicationPublished, nil
	case "outcome_unknown":
		return PublicationOutcomeUnknown, nil
	}
	return 0, fmt.Errorf("publication must be not_published, published, or outcome_unknown")
}

func decodeDestinationContent(raw json.RawMessage) (DestinationContent, error) {
	value, err := decodeString(raw, "destination_content")
	if err != nil {
		return 0, err
	}
	switch value {
	case "desired":
		return DestinationContentDesired, nil
	case "previous":
		return DestinationContentPrevious, nil
	case "absent":
		return DestinationContentAbsent, nil
	case "other":
		return DestinationContentOther, nil
	case "unclassified":
		return DestinationContentUnclassified, nil
	}
	return 0, fmt.Errorf("destination_content must be desired, previous, absent, other, or unclassified")
}

func decodeLaterCanonical(raw json.RawMessage) (LaterCanonical, error) {
	value, err := decodeString(raw, "later_canonical")
	if err != nil {
		return 0, err
	}
	switch value {
	case "none":
		return LaterCanonicalNone, nil
	case "reservation_or_transition":
		return LaterCanonicalReservationOrTransition, nil
	case "ready_live_sidecar":
		return LaterCanonicalReadyLiveSidecar, nil
	}
	return 0, fmt.Errorf("later_canonical must be none, reservation_or_transition, or ready_live_sidecar")
}

func decodeLiveLineage(raw json.RawMessage) (*LiveLineage, error) {
	if raw == nil {
		return nil, nil
	}
	if isNull(raw) {
		return nil, fmt.Errorf("live_lineage must not be null; absent is the only absent form")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("live_lineage must be an object")
	}
	if err := exactMembers(object, []string{"kind"}, nil); err != nil {
		return nil, err
	}
	kind, err := decodeString(object["kind"], "live_lineage.kind")
	if err != nil {
		return nil, err
	}
	switch kind {
	case "same_generation_exact_bytes":
		value := LiveLineageSameGenerationExactBytes
		return &value, nil
	case "same_generation_physical_bytes_changed":
		value := LiveLineageSameGenerationPhysicalBytesChanged
		return &value, nil
	case "advanced_generation":
		value := LiveLineageAdvancedGeneration
		return &value, nil
	}
	return nil, fmt.Errorf("live_lineage.kind must be same_generation_exact_bytes, same_generation_physical_bytes_changed, or advanced_generation")
}

func decodeAccessPolicy(raw json.RawMessage) (AccessPolicy, error) {
	value, err := decodeString(raw, "access_policy")
	if err != nil {
		return 0, err
	}
	switch value {
	case "absent":
		return AccessPolicyAbsent, nil
	case "creator_only":
		return AccessPolicyCreatorOnly, nil
	case "changed_or_unproven":
		return AccessPolicyChangedOrUnproven, nil
	case "unclassified":
		return AccessPolicyUnclassified, nil
	}
	return 0, fmt.Errorf("access_policy must be absent, creator_only, changed_or_unproven, or unclassified")
}

func decodePublicationCleanup(raw json.RawMessage) (CleanupArtifacts, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return CleanupArtifacts{}, fmt.Errorf("cleanup must be an object")
	}
	if err := exactMembers(object, nil, []string{"artifacts"}); err != nil {
		return CleanupArtifacts{}, err
	}
	artifactsRaw, ok := object["artifacts"]
	if !ok || isNull(artifactsRaw) {
		return NewCleanupArtifacts(), nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(artifactsRaw, &entries); err != nil {
		return CleanupArtifacts{}, fmt.Errorf("cleanup.artifacts must be an array")
	}
	cleanup := NewCleanupArtifacts()
	for _, entry := range entries {
		artifact, err := decodeCleanupArtifact(entry)
		if err != nil {
			return CleanupArtifacts{}, err
		}
		cleanup.Push(artifact)
	}
	return cleanup, nil
}

func decodeCoordinationCleanup(raw json.RawMessage) (CoordinationCleanup, error) {
	if isNull(raw) {
		return CoordinationCleanupNone, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return CoordinationCleanupNone, fmt.Errorf("coordination_cleanup must be an object")
	}
	if len(object) == 0 {
		return CoordinationCleanupNone, nil
	}
	if err := exactMembers(object, []string{"kind"}, nil); err != nil {
		return CoordinationCleanupNone, err
	}
	kind, err := decodeString(object["kind"], "coordination_cleanup.kind")
	if err != nil {
		return CoordinationCleanupNone, err
	}
	switch kind {
	case "cleanup_guard":
		return CoordinationCleanupCleanupGuard, nil
	case "retained_reader_close_required":
		return CoordinationCleanupRetainedReaderCloseRequired, nil
	case "retained_writer_close_required":
		return CoordinationCleanupRetainedWriterCloseRequired, nil
	}
	return CoordinationCleanupNone, fmt.Errorf("coordination_cleanup.kind is invalid")
}

func decodeHousekeeping(raw json.RawMessage) (Housekeeping, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return HousekeepingNone, fmt.Errorf("housekeeping must be an object")
	}
	if len(object) == 0 {
		return HousekeepingNone, nil
	}
	if err := exactMembers(object, []string{"state", "artifacts"}, nil); err != nil {
		return HousekeepingNone, err
	}
	state, err := decodeString(object["state"], "housekeeping.state")
	if err != nil {
		return HousekeepingNone, err
	}
	switch state {
	case "crash_reappearance_possible":
		return HousekeepingCrashReappearancePossible, nil
	case "visible":
		return HousekeepingVisible, nil
	}
	return HousekeepingNone, fmt.Errorf("housekeeping.state must be crash_reappearance_possible or visible")
}

func decodeHousekeepingArtifacts(raw json.RawMessage) ([]HousekeepingArtifact, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("visible_housekeeping must be an array")
	}
	result := make([]HousekeepingArtifact, 0, len(entries))
	for _, entry := range entries {
		artifact, err := decodeHousekeepingArtifact(entry)
		if err != nil {
			return nil, err
		}
		result = append(result, artifact)
	}
	return result, nil
}

func exactMembers(object map[string]json.RawMessage, required, optional []string) error {
	for key := range object {
		known := false
		for _, name := range required {
			if key == name {
				known = true
				break
			}
		}
		if !known {
			for _, name := range optional {
				if key == name {
					known = true
					break
				}
			}
		}
		if !known {
			return fmt.Errorf("unknown member %q", key)
		}
	}
	for _, name := range required {
		if _, ok := object[name]; !ok {
			return fmt.Errorf("missing member %q", name)
		}
	}
	return nil
}

func decodeString(raw json.RawMessage, field string) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string", field)
	}
	return value, nil
}

func decodeBool(raw json.RawMessage, field string) (bool, error) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("%s must be a boolean", field)
	}
	return value, nil
}

func decodeUint16(raw json.RawMessage, field string) (uint16, error) {
	var value uint64
	if err := json.Unmarshal(raw, &value); err != nil || value > 0xffff {
		return 0, fmt.Errorf("%s must be a u16 integer", field)
	}
	return uint16(value), nil
}

func decodeDecimalUint64(raw json.RawMessage, field string) (uint64, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, fmt.Errorf("%s must be a decimal string", field)
	}
	parsed, err := parseDecimalUint64(text)
	if err != nil {
		return 0, fmt.Errorf("%s must be a decimal string", field)
	}
	return parsed, nil
}

func parseDecimalUint64(text string) (uint64, error) {
	if text == "" {
		return 0, fmt.Errorf("empty")
	}
	var value uint64
	for i := 0; i < len(text); i++ {
		digit := text[i]
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("not decimal")
		}
		if value > (^uint64(0)-uint64(digit-'0'))/10 {
			return 0, fmt.Errorf("overflow")
		}
		value = value*10 + uint64(digit-'0')
	}
	return value, nil
}

func decodeHex16(raw json.RawMessage, field string) ([16]byte, error) {
	var result [16]byte
	text, err := decodeString(raw, field)
	if err != nil {
		return result, err
	}
	if len(text) != 32 {
		return result, fmt.Errorf("%s must be 32 lowercase hexadecimal characters", field)
	}
	bytes, err := parseHexBytes(text, 16, field)
	if err != nil {
		return result, err
	}
	copy(result[:], bytes)
	return result, nil
}

func decodeHex64(raw json.RawMessage, field string) ([64]byte, error) {
	var result [64]byte
	text, err := decodeString(raw, field)
	if err != nil {
		return result, err
	}
	if len(text) != 128 {
		return result, fmt.Errorf("%s must be 128 lowercase hexadecimal characters", field)
	}
	bytes, err := parseHexBytes(text, 64, field)
	if err != nil {
		return result, err
	}
	copy(result[:], bytes)
	return result, nil
}

func parseHexBytes(text string, length int, field string) ([]byte, error) {
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		hi, ok1 := hexValue(text[2*i])
		lo, ok2 := hexValue(text[2*i+1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("%s must be lowercase hexadecimal characters", field)
		}
		result[i] = hi<<4 | lo
	}
	return result, nil
}

func hexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

func decodeFileIdentity(raw json.RawMessage) (LocalFileIdentity, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return LocalFileIdentity{}, fmt.Errorf("identity must be an object")
	}
	if err := exactMembers(object, []string{"volume", "file"}, nil); err != nil {
		return LocalFileIdentity{}, err
	}
	volumeText, err := decodeString(object["volume"], "identity.volume")
	if err != nil {
		return LocalFileIdentity{}, err
	}
	fileText, err := decodeString(object["file"], "identity.file")
	if err != nil {
		return LocalFileIdentity{}, err
	}
	volume, err := parseDecimalUint64(volumeText)
	if err != nil {
		return LocalFileIdentity{}, fmt.Errorf("identity.volume must be a decimal string")
	}
	file, err := parseDecimalUint64(fileText)
	if err != nil {
		return LocalFileIdentity{}, fmt.Errorf("identity.file must be a decimal string")
	}
	// Kind 1 (POSIX): device + inode little-endian, zero trailing pad.
	return LocalFileIdentityFromDeviceInode(volume, file), nil
}

func decodeCreationSecurity(raw json.RawMessage) (CreationSecurity, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return CreationSecurity{}, fmt.Errorf("creation_security must be an object")
	}
	if err := exactMembers(object, []string{"kind", "commitment"}, nil); err != nil {
		return CreationSecurity{}, err
	}
	kind, err := decodeUint16(object["kind"], "creation_security.kind")
	if err != nil {
		return CreationSecurity{}, err
	}
	commitment, err := decodeHex32(object["commitment"], "creation_security.commitment")
	if err != nil {
		return CreationSecurity{}, err
	}
	return CreationSecurity{Kind: kind, Commitment: commitment}, nil
}

func decodeHex32(raw json.RawMessage, field string) ([32]byte, error) {
	var result [32]byte
	text, err := decodeString(raw, field)
	if err != nil {
		return result, err
	}
	if len(text) != 64 {
		return result, fmt.Errorf("%s must be 64 lowercase hexadecimal characters", field)
	}
	bytes, err := parseHexBytes(text, 32, field)
	if err != nil {
		return result, err
	}
	copy(result[:], bytes)
	return result, nil
}

func isNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}

// rawOr returns the named member, or nil when absent (absent is the
// only absent form; an explicit null is decoded and rejected by the
// member decoder).
func rawOr(object map[string]json.RawMessage, name string) json.RawMessage {
	if raw, ok := object[name]; ok {
		return raw
	}
	return nil
}

func decodeCleanupArtifact(raw json.RawMessage) (CleanupArtifact, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return CleanupArtifact{}, fmt.Errorf("cleanup artifact must be an object")
	}
	if err := exactMembers(object, []string{
		"kind", "directory_role", "directory_identity", "basename_encoding",
		"basename", "error",
	}, []string{"identity", "creation_security", "unpublished_tail"}); err != nil {
		return CleanupArtifact{}, err
	}
	kind, err := decodeArtifactKind(object["kind"])
	if err != nil {
		return CleanupArtifact{}, err
	}
	role, err := decodeDirectoryRole(object["directory_role"])
	if err != nil {
		return CleanupArtifact{}, err
	}
	directoryIdentity, err := decodeFileIdentity(object["directory_identity"])
	if err != nil {
		return CleanupArtifact{}, err
	}
	basenameEncoding, err := decodeUint16(object["basename_encoding"], "basename_encoding")
	if err != nil {
		return CleanupArtifact{}, err
	}
	basenameText, err := decodeString(object["basename"], "basename")
	if err != nil {
		return CleanupArtifact{}, err
	}
	basename, err := parseHexBytes(basenameText, len(basenameText)/2, "basename")
	if err != nil {
		return CleanupArtifact{}, err
	}
	var identity *LocalFileIdentity
	if rawIdentity, ok := object["identity"]; ok {
		value, err := decodeFileIdentity(rawIdentity)
		if err != nil {
			return CleanupArtifact{}, err
		}
		identity = &value
	}
	var creationSecurity *CreationSecurity
	if rawSecurity, ok := object["creation_security"]; ok {
		value, err := decodeCreationSecurity(rawSecurity)
		if err != nil {
			return CleanupArtifact{}, err
		}
		creationSecurity = &value
	}
	var unpublishedTail *UnpublishedTailFacts
	if rawTail, ok := object["unpublished_tail"]; ok {
		value, err := decodeUnpublishedTail(rawTail)
		if err != nil {
			return CleanupArtifact{}, err
		}
		unpublishedTail = &value
	}
	problem, err := decodePublicationProblem(object["error"])
	if err != nil {
		return CleanupArtifact{}, err
	}
	return CleanupArtifact{
		Kind:              kind,
		DirectoryRole:     role,
		DirectoryIdentity: directoryIdentity,
		BasenameEncoding:  basenameEncoding,
		Basename:          basename,
		Identity:          identity,
		CreationSecurity:  creationSecurity,
		UnpublishedTail:   unpublishedTail,
		Error:             problem,
	}, nil
}

func decodeHousekeepingArtifact(raw json.RawMessage) (HousekeepingArtifact, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return HousekeepingArtifact{}, fmt.Errorf("housekeeping artifact must be an object")
	}
	optional := []string{
		"source_identity", "inert_identity",
	}
	required := []string{
		"state", "directory_role", "directory_identity", "basename_encoding",
		"attempt_id", "ordinal", "envelope_basename", "envelope_identity",
		"source_basename", "inert_basename", "source_presence", "inert_presence",
		"kind", "creation_security", "selected_envelope_sequence",
	}
	if err := exactMembers(object, required, optional); err != nil {
		return HousekeepingArtifact{}, err
	}
	state, err := decodeHousekeepingState(object["state"])
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	role, err := decodeDirectoryRole(object["directory_role"])
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	directoryIdentity, err := decodeFileIdentity(object["directory_identity"])
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	basenameEncoding, err := decodeUint16(object["basename_encoding"], "basename_encoding")
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	attemptID, err := decodeHex16(object["attempt_id"], "attempt_id")
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	ordinal, err := decodeUint32(object["ordinal"], "ordinal")
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	envelopeBasename, err := decodeString(object["envelope_basename"], "envelope_basename")
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	envelopeIdentity, err := decodeFileIdentity(object["envelope_identity"])
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	sourceBasename, err := decodeString(object["source_basename"], "source_basename")
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	inertBasename, err := decodeString(object["inert_basename"], "inert_basename")
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	sourcePresence, err := decodeArtifactPresence(object["source_presence"])
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	inertPresence, err := decodeArtifactPresence(object["inert_presence"])
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	kind, err := decodeArtifactKind(object["kind"])
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	creationSecurity, err := decodeCreationSecurity(object["creation_security"])
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	sequence, err := decodeDecimalUint64(object["selected_envelope_sequence"], "selected_envelope_sequence")
	if err != nil {
		return HousekeepingArtifact{}, err
	}
	var sourceIdentity *LocalFileIdentity
	if raw, ok := object["source_identity"]; ok {
		value, err := decodeFileIdentity(raw)
		if err != nil {
			return HousekeepingArtifact{}, err
		}
		sourceIdentity = &value
	}
	var inertIdentity *LocalFileIdentity
	if raw, ok := object["inert_identity"]; ok {
		value, err := decodeFileIdentity(raw)
		if err != nil {
			return HousekeepingArtifact{}, err
		}
		inertIdentity = &value
	}
	return HousekeepingArtifact{
		State:                    state,
		DirectoryRole:            role,
		DirectoryIdentity:        directoryIdentity,
		BasenameEncoding:         basenameEncoding,
		AttemptID:                attemptID,
		Ordinal:                  ordinal,
		EnvelopeBasename:         []byte(envelopeBasename),
		EnvelopeIdentity:         envelopeIdentity,
		SourceBasename:           []byte(sourceBasename),
		InertBasename:            []byte(inertBasename),
		SourcePresence:           sourcePresence,
		SourceIdentity:           sourceIdentity,
		InertPresence:            inertPresence,
		InertIdentity:            inertIdentity,
		Kind:                     kind,
		CreationSecurity:         creationSecurity,
		SelectedEnvelopeSequence: sequence,
	}, nil
}

func decodeUnpublishedTail(raw json.RawMessage) (UnpublishedTailFacts, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return UnpublishedTailFacts{}, fmt.Errorf("unpublished_tail must be an object")
	}
	if err := exactMembers(object, []string{
		"expected_database_id", "committed_target_transaction_id",
		"committed_target_nonce", "committed_target_length", "observed_tail_end_exclusive",
	}, nil); err != nil {
		return UnpublishedTailFacts{}, err
	}
	databaseID, err := decodeHex16(object["expected_database_id"], "expected_database_id")
	if err != nil {
		return UnpublishedTailFacts{}, err
	}
	transactionID, err := decodeDecimalUint64(object["committed_target_transaction_id"], "committed_target_transaction_id")
	if err != nil {
		return UnpublishedTailFacts{}, err
	}
	nonce, err := decodeHex16(object["committed_target_nonce"], "committed_target_nonce")
	if err != nil {
		return UnpublishedTailFacts{}, err
	}
	length, err := decodeDecimalUint64(object["committed_target_length"], "committed_target_length")
	if err != nil {
		return UnpublishedTailFacts{}, err
	}
	tail, err := decodeDecimalUint64(object["observed_tail_end_exclusive"], "observed_tail_end_exclusive")
	if err != nil {
		return UnpublishedTailFacts{}, err
	}
	return UnpublishedTailFacts{
		ExpectedDatabaseID:           databaseID,
		CommittedTargetTransactionID: transactionID,
		CommittedTargetNonce:         nonce,
		CommittedTargetLength:        length,
		ObservedTailEndExclusive:     tail,
	}, nil
}

func decodePublicationProblem(raw json.RawMessage) (error, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("error must be an object")
	}
	code, err := decodeString(object["code"], "error.code")
	if err != nil {
		return nil, err
	}
	detail, err := decodeString(object["detail"], "error.detail")
	if err != nil {
		return nil, err
	}
	return fmt.Errorf("%s: %s", code, detail), nil
}

func decodeUint32(raw json.RawMessage, field string) (uint32, error) {
	var value uint64
	if err := json.Unmarshal(raw, &value); err != nil || value > 0xffffffff {
		return 0, fmt.Errorf("%s must be a u32 integer", field)
	}
	return uint32(value), nil
}

func decodeHousekeepingState(raw json.RawMessage) (HousekeepingState, error) {
	value, err := decodeString(raw, "state")
	if err != nil {
		return 0, err
	}
	switch value {
	case "move_pending":
		return HousekeepingMovePending, nil
	case "move_ambiguous":
		return HousekeepingMoveAmbiguous, nil
	case "inert":
		return HousekeepingInert, nil
	case "conflict":
		return HousekeepingConflict, nil
	}
	return 0, fmt.Errorf("state must be move_pending, move_ambiguous, inert, or conflict")
}

func decodeDirectoryRole(raw json.RawMessage) (DirectoryRole, error) {
	value, err := decodeString(raw, "directory_role")
	if err != nil {
		return 0, err
	}
	switch value {
	case "destination":
		return DirectoryRoleDestination, nil
	case "scratch_directory":
		return DirectoryRoleScratchDirectory, nil
	case "main_file":
		return DirectoryRoleMainFile, nil
	}
	return 0, fmt.Errorf("directory_role must be destination, scratch_directory, or main_file")
}

func decodeArtifactKind(raw json.RawMessage) (ArtifactKind, error) {
	value, err := decodeString(raw, "kind")
	if err != nil {
		return 0, err
	}
	switch value {
	case "private_output":
		return ArtifactPrivateOutput, nil
	case "private_reservation":
		return ArtifactPrivateReservation, nil
	case "owned_coordination":
		return ArtifactOwnedCoordination, nil
	case "authorized_scratch":
		return ArtifactAuthorizedScratch, nil
	case "owned_main":
		return ArtifactOwnedMain, nil
	case "unpublished_main_tail":
		return ArtifactUnpublishedMainTail, nil
	}
	return 0, fmt.Errorf("kind is invalid")
}

func decodeArtifactPresence(raw json.RawMessage) (ArtifactPresence, error) {
	value, err := decodeString(raw, "presence")
	if err != nil {
		return 0, err
	}
	switch value {
	case "absent":
		return ArtifactAbsent, nil
	case "present":
		return ArtifactPresent, nil
	case "unclassified":
		return ArtifactUnclassified, nil
	}
	return 0, fmt.Errorf("presence must be absent, present, or unclassified")
}
