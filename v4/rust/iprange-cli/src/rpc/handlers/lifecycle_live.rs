//! Wire evidence decoding for the JSON-RPC resolution attempts (SOW-0028).
//!
//! `database.create.resolve`, `database.live_transition.resolve`, and
//! `commit.resolve` accept the complete factual result object the caller
//! preserved from the operation that made the attempt. These decoders
//! rebuild the public SDK result structs from that strict wire shape.
//! Every field the SDK consumes during resolution or identity validation
//! is decoded exactly; missing, extra, or mistyped members are invalid
//! params before any SDK work starts.
//!
//! Two wire members are intentionally approximate because the JSON-RPC
//! conversion that produced them dropped information: the `Housekeeping`
//! enum state is not serialized (only its artifact list is), so an empty
//! artifact list decodes to `None` and a non-empty list to `Visible`; and
//! cleanup/artifact containers are validated for shape but their nested
//! artifact values are not consumed by the SDK resolvers.

use std::path::Path;

use iprange_livedb::error::ErrorCode;
use iprange_livedb::publication::{
    ArtifactKind, ArtifactPresence, CoordinationCleanup, CreationSecurity, DirectoryRole,
    Housekeeping, HousekeepingArtifact, HousekeepingState,
};
use iprange_livedb::validation::LocalFileIdentity;
use iprange_livedb::{
    AddressFamily, CommitCleanupArtifact, CommitCleanupArtifacts, CommitDurability, CommitResult,
    CreateResult, CreationState, LiveCoordinationLocation, LiveResetPolicy,
    LiveTransitionOperation, LiveTransitionResult, LiveTransitionStatus, LocalBasename,
    StructureKind, ValueKind, ValueTag,
};
use serde_json::Value;

// ---------------------------------------------------------------------------
// Primitive wire decoders (result-schema encodings).
// ---------------------------------------------------------------------------

pub(crate) fn hex16(value: &Value, field: &str) -> Result<[u8; 16], String> {
    let text = value.as_str().ok_or_else(|| format!("{field} must be a string"))?;
    let bytes = decode_hex(text, 16).ok_or_else(|| {
        format!("{field} must be 32 lowercase hexadecimal characters")
    })?;
    bytes
        .try_into()
        .map_err(|_| format!("{field} must be 32 lowercase hexadecimal characters"))
}

pub(crate) fn hex32(value: &Value, field: &str) -> Result<[u8; 32], String> {
    let text = value.as_str().ok_or_else(|| format!("{field} must be a string"))?;
    let bytes = decode_hex(text, 32).ok_or_else(|| {
        format!("{field} must be 64 lowercase hexadecimal characters")
    })?;
    bytes
        .try_into()
        .map_err(|_| format!("{field} must be 64 lowercase hexadecimal characters"))
}

pub(crate) fn decode_hex(text: &str, length: usize) -> Option<Vec<u8>> {
    if text.len() != length * 2 || !text.bytes().all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase()) {
        return None;
    }
    let mut bytes = Vec::with_capacity(length);
    for pair in text.as_bytes().chunks_exact(2) {
        let high = (pair[0] as char).to_digit(16).unwrap_or(0);
        let low = (pair[1] as char).to_digit(16).unwrap_or(0);
        bytes.push(((high << 4) | low) as u8);
    }
    Some(bytes)
}

pub(crate) fn decimal_u64(value: &Value, field: &str) -> Result<u64, String> {
    let text = value.as_str().ok_or_else(|| format!("{field} must be a string"))?;
    if text == "0" {
        return Ok(0);
    }
    if text.is_empty() || !text.bytes().all(|b| b.is_ascii_digit()) || text.starts_with('0') {
        return Err(format!("{field} must be a canonical unsigned decimal string"));
    }
    text.parse().map_err(|_| format!("{field} must be a canonical unsigned decimal string"))
}

pub(crate) fn u32_integer(value: &Value, field: &str) -> Result<u32, String> {
    value
        .as_u64()
        .and_then(|parsed| u32::try_from(parsed).ok())
        .ok_or_else(|| format!("{field} must be a u32 integer"))
}

pub(crate) fn boolean(value: &Value, field: &str) -> Result<bool, String> {
    value.as_bool().ok_or_else(|| format!("{field} must be a boolean"))
}

pub(crate) fn string<'a>(value: &'a Value, field: &str) -> Result<&'a str, String> {
    value.as_str().ok_or_else(|| format!("{field} must be a string"))
}

pub(crate) fn object<'a>(value: &'a Value, field: &str) -> Result<&'a serde_json::Map<String, Value>, String> {
    value.as_object().ok_or_else(|| format!("{field} must be an object"))
}

pub(crate) fn exact_members(
    object: &serde_json::Map<String, Value>,
    required: &[&str],
    optional: &[&str],
    field: &str,
) -> Result<(), String> {
    for key in object.keys() {
        if !required.contains(&key.as_str()) && !optional.contains(&key.as_str()) {
            return Err(format!("{field} has unknown member {key:?}"));
        }
    }
    for key in required {
        if !object.contains_key(*key) {
            return Err(format!("{field} is missing member {key:?}"));
        }
    }
    Ok(())
}

/// Decode the wire volume/file pair into a kind-1 SDK local identity.
pub(crate) fn decode_file_identity(value: &Value, field: &str) -> Result<LocalFileIdentity, String> {
    let identity = object(value, field)?;
    exact_members(identity, &["volume", "file"], &[], field)?;
    let volume = decimal_u64(&identity["volume"], &format!("{field}.volume"))?;
    let file = decimal_u64(&identity["file"], &format!("{field}.file"))?;
    let mut bytes = [0u8; 32];
    bytes[0..8].copy_from_slice(&volume.to_le_bytes());
    bytes[8..16].copy_from_slice(&file.to_le_bytes());
    Ok(LocalFileIdentity { kind: 1, bytes })
}

pub(crate) fn optional_file_identity(
    value: Option<&Value>,
    field: &str,
) -> Result<Option<LocalFileIdentity>, String> {
    match value {
        Some(identity) if !identity.is_null() => decode_file_identity(identity, field).map(Some),
        _ => Ok(None),
    }
}

/// Verify the wire basename against the destination path and return the
/// path-derived SDK basename the resolvers compare against.
pub(crate) fn decode_main_basename(value: &Value, path: &str) -> Result<LocalBasename, String> {
    let wire = string(value, "main_basename")?;
    let actual = Path::new(path)
        .file_name()
        .ok_or_else(|| "database path has no file name".to_string())?
        .to_string_lossy();
    if wire != actual {
        return Err("main_basename does not match the database path".into());
    }
    LocalBasename::from_path(Path::new(path)).map_err(|_| "database path has no file name".into())
}

fn decode_value_tag(value: &Value, field: &str) -> Result<ValueTag, String> {
    let tag = object(value, field)?;
    exact_members(tag, &["hex"], &[], field)?;
    let text = string(&tag["hex"], &format!("{field}.hex"))?;
    let bytes = decode_hex(text, text.len() / 2)
        .filter(|decoded| decoded.len() <= 15)
        .ok_or_else(|| format!("{field}.hex must be even lowercase hex encoding at most 15 bytes"))?;
    ValueTag::new(&bytes).ok_or_else(|| format!("{field} encodes an invalid value tag"))
}

fn address_family(value: &Value) -> Result<AddressFamily, String> {
    match string(value, "address_family")? {
        "ipv4" => Ok(AddressFamily::Ipv4),
        "ipv6" => Ok(AddressFamily::Ipv6),
        _ => Err("address_family must be ipv4 or ipv6".into()),
    }
}

fn value_kind(value: &Value) -> Result<ValueKind, String> {
    match string(value, "value_kind")? {
        "direct" => Ok(ValueKind::Direct),
        "membership" => Ok(ValueKind::Membership),
        "structured" => Ok(ValueKind::Structured),
        _ => Err("value_kind must be direct, membership, or structured".into()),
    }
}

fn structure_kind(value: &Value) -> Result<StructureKind, String> {
    match string(value, "structure_kind")? {
        "none" => Ok(StructureKind::None),
        "network_enrichment_v1" => Ok(StructureKind::NetworkEnrichmentV1),
        _ => Err("structure_kind must be none or network_enrichment_v1".into()),
    }
}

fn creation_state(value: &Value) -> Result<CreationState, String> {
    match string(value, "state")? {
        "not_created" => Ok(CreationState::NotCreated),
        "created" => Ok(CreationState::Created),
        "outcome_unknown" => Ok(CreationState::OutcomeUnknown),
        _ => Err("state must be not_created, created, or outcome_unknown".into()),
    }
}

fn live_transition_operation(value: &Value) -> Result<LiveTransitionOperation, String> {
    match string(value, "operation")? {
        "initialize" => Ok(LiveTransitionOperation::Initialize),
        "reset" => Ok(LiveTransitionOperation::Reset),
        _ => Err("operation must be initialize or reset".into()),
    }
}

fn live_transition_status(value: &Value) -> Result<LiveTransitionStatus, String> {
    match string(value, "status")? {
        "unchanged" => Ok(LiveTransitionStatus::Unchanged),
        "initialized" => Ok(LiveTransitionStatus::Initialized),
        "outcome_unknown" => Ok(LiveTransitionStatus::OutcomeUnknown),
        _ => Err("status must be unchanged, initialized, or outcome_unknown".into()),
    }
}

fn live_coordination_location(value: &Value) -> Result<LiveCoordinationLocation, String> {
    match string(value, "new_sidecar_location")? {
        "absent" => Ok(LiveCoordinationLocation::Absent),
        "canonical" => Ok(LiveCoordinationLocation::Canonical),
        "private" => Ok(LiveCoordinationLocation::Private),
        "unclassified" => Ok(LiveCoordinationLocation::Unclassified),
        _ => Err("new_sidecar_location must be absent, canonical, private, or unclassified".into()),
    }
}

fn commit_durability(value: &Value) -> Result<CommitDurability, String> {
    match string(value, "durability")? {
        "not_committed" => Ok(CommitDurability::NotCommitted),
        "committed" => Ok(CommitDurability::Committed),
        "outcome_unknown" => Ok(CommitDurability::OutcomeUnknown),
        _ => Err("durability must be not_committed, committed, or outcome_unknown".into()),
    }
}

pub(crate) fn coordination_cleanup(value: &Value, field: &str) -> Result<CoordinationCleanup, String> {
    let cleanup = object(value, field)?;
    match cleanup.get("kind").and_then(Value::as_str) {
        None => {
            if cleanup.is_empty() {
                Ok(CoordinationCleanup::None)
            } else {
                Err(format!("{field} must be {{}} or {{kind: ...}}"))
            }
        }
        Some("cleanup_guard") => Ok(CoordinationCleanup::CleanupGuard),
        Some("retained_reader_close_required") => {
            Ok(CoordinationCleanup::RetainedReaderCloseRequired)
        }
        Some("retained_writer_close_required") => {
            Ok(CoordinationCleanup::RetainedWriterCloseRequired)
        }
        _ => Err(format!("{field}.kind is invalid")),
    }
}

/// Reads the preserved `Housekeeping` state: absent `state` with no
/// artifacts is `None`; the emitted states round-trip exactly.
pub(crate) fn decode_housekeeping(value: &Value, field: &str) -> Result<Housekeeping, String> {
    let housekeeping = object(value, field)?;
    exact_members(housekeeping, &["artifacts"], &["state"], field)?;
    let artifacts = housekeeping["artifacts"]
        .as_array()
        .ok_or_else(|| format!("{field}.artifacts must be an array"))?;
    if artifacts.iter().any(|artifact| !artifact.is_object()) {
        return Err(format!("{field}.artifacts entries must be objects"));
    }
    match housekeeping.get("state").map(Value::as_str) {
        None => {
            if artifacts.is_empty() {
                Ok(Housekeeping::None)
            } else {
                Ok(Housekeeping::Visible)
            }
        }
        Some(Some("crash_reappearance_possible")) => Ok(Housekeeping::CrashReappearancePossible),
        Some(Some("visible")) => Ok(Housekeeping::Visible),
        _ => Err(format!("{field}.state is invalid")),
    }
}

pub(crate) fn decode_housekeeping_artifacts(value: &Value) -> Result<Box<[HousekeepingArtifact]>, String> {
    let artifacts = value
        .as_array()
        .ok_or("visible_housekeeping must be an array")?;
    artifacts
        .iter()
        .map(|artifact| decode_housekeeping_artifact(artifact))
        .collect::<Result<Vec<_>, _>>()
        .map(Vec::into_boxed_slice)
}

fn decode_housekeeping_artifact(value: &Value) -> Result<HousekeepingArtifact, String> {
    let artifact = object(value, "visible_housekeeping entry")?;
    exact_members(
        artifact,
        &[
            "state",
            "directory_role",
            "directory_identity",
            "basename_encoding",
            "attempt_id",
            "ordinal",
            "envelope_basename",
            "envelope_identity",
            "source_basename",
            "inert_basename",
            "source_presence",
            "inert_presence",
            "kind",
            "creation_security",
            "selected_envelope_sequence",
        ],
        &["source_identity", "inert_identity"],
        "housekeeping artifact",
    )?;
    Ok(HousekeepingArtifact {
        state: housekeeping_state(&artifact["state"])?,
        directory_role: directory_role(&artifact["directory_role"])?,
        directory_identity: decode_file_identity(&artifact["directory_identity"], "directory_identity")?,
        basename_encoding: u16_encoding(&artifact["basename_encoding"])?,
        attempt_id: hex16(&artifact["attempt_id"], "attempt_id")?,
        ordinal: u32_integer(&artifact["ordinal"], "ordinal")?,
        envelope_basename: string(&artifact["envelope_basename"], "envelope_basename")
            .map(|text| text.as_bytes().to_vec().into_boxed_slice())?,
        envelope_identity: decode_file_identity(&artifact["envelope_identity"], "envelope_identity")?,
        source_basename: string(&artifact["source_basename"], "source_basename")
            .map(|text| text.as_bytes().to_vec().into_boxed_slice())?,
        inert_basename: string(&artifact["inert_basename"], "inert_basename")
            .map(|text| text.as_bytes().to_vec().into_boxed_slice())?,
        source_presence: artifact_presence(&artifact["source_presence"])?,
        source_identity: optional_file_identity(artifact.get("source_identity"), "source_identity")?,
        inert_presence: artifact_presence(&artifact["inert_presence"])?,
        inert_identity: optional_file_identity(artifact.get("inert_identity"), "inert_identity")?,
        kind: artifact_kind(&artifact["kind"])?,
        creation_security: decode_creation_security(&artifact["creation_security"])?,
        selected_envelope_sequence: decimal_u64(&artifact["selected_envelope_sequence"], "selected_envelope_sequence")?,
    })
}

pub(crate) fn u16_encoding(value: &Value) -> Result<u16, String> {
    value
        .as_u64()
        .and_then(|parsed| u16::try_from(parsed).ok())
        .ok_or_else(|| "basename_encoding must be a u16 integer".to_string())
}

fn housekeeping_state(value: &Value) -> Result<HousekeepingState, String> {
    match string(value, "state")? {
        "move_pending" => Ok(HousekeepingState::MovePending),
        "move_ambiguous" => Ok(HousekeepingState::MoveAmbiguous),
        "inert" => Ok(HousekeepingState::Inert),
        "conflict" => Ok(HousekeepingState::Conflict),
        _ => Err("housekeeping state is invalid".into()),
    }
}

pub(crate) fn directory_role(value: &Value) -> Result<DirectoryRole, String> {
    match string(value, "directory_role")? {
        "destination" => Ok(DirectoryRole::Destination),
        "scratch_directory" => Ok(DirectoryRole::ScratchDirectory),
        "main_file" => Ok(DirectoryRole::MainFile),
        _ => Err("directory_role is invalid".into()),
    }
}

fn artifact_presence(value: &Value) -> Result<ArtifactPresence, String> {
    match string(value, "presence")? {
        "absent" => Ok(ArtifactPresence::Absent),
        "present" => Ok(ArtifactPresence::Present),
        "unclassified" => Ok(ArtifactPresence::Unclassified),
        _ => Err("artifact presence is invalid".into()),
    }
}

pub(crate) fn artifact_kind(value: &Value) -> Result<ArtifactKind, String> {
    match string(value, "kind")? {
        "private_output" => Ok(ArtifactKind::PrivateOutput),
        "private_reservation" => Ok(ArtifactKind::PrivateReservation),
        "owned_coordination" => Ok(ArtifactKind::OwnedCoordination),
        "authorized_scratch" => Ok(ArtifactKind::AuthorizedScratch),
        "owned_main" => Ok(ArtifactKind::OwnedMain),
        "unpublished_main_tail" => Ok(ArtifactKind::UnpublishedMainTail),
        _ => Err("artifact kind is invalid".into()),
    }
}

pub(crate) fn decode_creation_security(value: &Value) -> Result<CreationSecurity, String> {
    let security = object(value, "creation_security")?;
    exact_members(security, &["kind", "commitment"], &[], "creation_security")?;
    Ok(CreationSecurity {
        kind: u16_encoding(&security["kind"])?,
        commitment: hex32(&security["commitment"], "commitment")?,
    })
}

fn decode_commit_cleanup(value: &Value) -> Result<CommitCleanupArtifacts, String> {
    let cleanup = object(value, "cleanup")?;
    // An empty cleanup is emitted as {}; a non-empty one carries
    // {"artifacts": [...]}.
    if cleanup.is_empty() {
        return Ok(CommitCleanupArtifacts::clean());
    }
    exact_members(cleanup, &["artifacts"], &[], "cleanup")?;
    let artifacts = cleanup["artifacts"]
        .as_array()
        .ok_or_else(|| "cleanup.artifacts must be an array".to_string())?;
    match artifacts.len() {
        0 => Ok(CommitCleanupArtifacts::clean()),
        1 => decode_commit_cleanup_artifact(&artifacts[0]).map(CommitCleanupArtifacts::tail),
        _ => Err("cleanup.artifacts must contain at most one entry".into()),
    }
}

fn decode_commit_cleanup_artifact(value: &Value) -> Result<CommitCleanupArtifact, String> {
    let artifact = object(value, "cleanup artifact")?;
    exact_members(
        artifact,
        &[
            "directory_identity",
            "main_basename",
            "main_identity",
            "expected_database_id",
            "target_transaction_id",
            "target_commit_nonce",
            "committed_target_length",
            "cleanup_error",
        ],
        &["observed_tail_end_exclusive"],
        "cleanup artifact",
    )?;
    let basename = string(&artifact["main_basename"], "main_basename")?;
    Ok(CommitCleanupArtifact {
        directory_identity: decode_file_identity(&artifact["directory_identity"], "directory_identity")?,
        main_basename: LocalBasename::from_path(Path::new(basename))
            .map_err(|_| "main_basename is not a valid basename".to_string())?,
        main_identity: decode_file_identity(&artifact["main_identity"], "main_identity")?,
        expected_database_id: hex16(&artifact["expected_database_id"], "expected_database_id")?,
        target_transaction_id: decimal_u64(&artifact["target_transaction_id"], "target_transaction_id")?,
        target_commit_nonce: hex16(&artifact["target_commit_nonce"], "target_commit_nonce")?,
        committed_target_length: decimal_u64(&artifact["committed_target_length"], "committed_target_length")?,
        observed_tail_end_exclusive: match artifact.get("observed_tail_end_exclusive") {
            Some(value) if !value.is_null() => Some(decimal_u64(value, "observed_tail_end_exclusive")?),
            _ => None,
        },
        cleanup_error: error_code_from_wire(string(&artifact["cleanup_error"], "cleanup_error")?)
            .ok_or_else(|| "cleanup_error is not a canonical SDK error name".to_string())?,
    })
}

/// Reverse of the stable `sdk_code` names in `handlers/reader.rs`.
macro_rules! error_code_table {
    ($($snake:literal => $variant:ident),* $(,)?) => {
        pub(crate) fn error_code_from_wire(value: &str) -> Option<ErrorCode> {
            Some(match value {
                $($snake => ErrorCode::$variant,)*
                _ => return None,
            })
        }
    };
}

error_code_table! {
    "invalid_argument" => InvalidArgument,
    "null_pointer" => NullPointer,
    "misaligned_pointer" => MisalignedPointer,
    "invalid_length" => InvalidLength,
    "invalid_enum" => InvalidEnum,
    "reserved_nonzero" => ReservedNonzero,
    "buffer_too_small" => BufferTooSmall,
    "handle_wrong_kind" => WrongHandleKind,
    "handle_closed" => HandleClosed,
    "handle_busy" => HandleBusy,
    "wrong_state" => WrongState,
    "wrong_address_family" => WrongAddressFamily,
    "wrong_value_kind" => WrongValueKind,
    "wrong_value_tag" => WrongValueTag,
    "range_reversed" => RangeReversed,
    "name_invalid" => NameInvalid,
    "name_exists" => NameExists,
    "name_not_found" => NameNotFound,
    "stale_reference" => StaleReference,
    "foreign_reference" => ForeignReference,
    "no_pending_transaction" => NoPendingTransaction,
    "transaction_aborted" => TransactionAborted,
    "abort_incomplete" => AbortIncomplete,
    "insufficient_resource_budget" => InsufficientResourceBudget,
    "page_space_exhausted" => PageSpaceExhausted,
    "work_limit_too_small" => WorkLimitTooSmall,
    "cancelled" => Cancelled,
    "source_failed" => SourceFailed,
    "sink_failed" => SinkFailed,
    "stopped_by_sink" => StoppedBySink,
    "io" => Io,
    "format_invalid" => FormatInvalid,
    "not_v4" => NotV4,
    "durability_unsupported" => DurabilityUnsupported,
    "publication_unsupported" => PublicationUnsupported,
    "access_policy_unsupported" => AccessPolicyUnsupported,
    "conflict" => Conflict,
    "unresolvable" => Unresolvable,
    "writer_busy" => WriterBusy,
    "directory_identity_mismatch" => DirectoryIdentityMismatch,
    "destination_name_mismatch" => DestinationNameMismatch,
    "cleanup_conflict" => CleanupConflict,
    "coordination_sequence_exhausted" => CoordinationSequenceExhausted,
    "live_coordination_unsupported" => LiveCoordinationUnsupported,
    "live_coordination_cleanup_required" => LiveCoordinationCleanupRequired,
    "live_coordination_malformed_requires_reset" => LiveCoordinationMalformedRequiresReset,
    "live_open_cleanup_required" => LiveOpenCleanupRequired,
    "live_recovery_coordination_unavailable" => LiveRecoveryCoordinationUnavailable,
    "live_recovery_current_generation_unprovable" => LiveRecoveryCurrentGenerationUnprovable,
    "live_recovery_current_generation_unreadable" => LiveRecoveryCurrentGenerationUnreadable,
    "recovery_candidate_changed" => RecoveryCandidateChanged,
    "recovery_preparation_failed" => RecoveryPreparationFailed,
    "snapshot_preparation_failed" => SnapshotPreparationFailed,
    "transition_superseded" => TransitionSuperseded,
    "current_generation_unprovable" => CurrentGenerationUnprovable,
    "forked_handle" => ForkedHandle,
    "panic" => Panic,
    "os_unsupported" => OsUnsupported,
    "transaction_id_exhausted" => TransactionIdExhausted,
    "arithmetic_overflow" => ArithmeticOverflow,
    "feed_index_exhausted" => FeedIndexExhausted,
    "membership_id_exhausted" => MembershipIdExhausted,
    "reader_capacity_exhausted" => ReaderCapacityExhausted,
    "cleanup_in_progress" => CleanupInProgress,
    "fault_worker_unavailable" => FaultWorkerUnavailable,
    "fault_worker_failed" => FaultWorkerFailed,
    "unsupported_structure" => UnsupportedStructure,
    "wrong_structure_kind" => WrongStructureKind,
    "structure_id_exhausted" => StructureIdExhausted,
}

// ---------------------------------------------------------------------------
// Complete evidence decoders for the three resolution methods.
// ---------------------------------------------------------------------------

/// Rebuild a `CommitResult` from the complete object returned by a commit.
pub(crate) fn commit_result_from_wire(value: &Value) -> Result<CommitResult, String> {
    let commit = object(value, "commit_result")?;
    exact_members(
        commit,
        &[
            "attempted_database_id",
            "directory_identity",
            "main_identity",
            "attempted_transaction_id",
            "attempted_commit_nonce",
            "durability",
            "cleanup",
            "coordination_cleanup",
        ],
        &[],
        "commit_result",
    )?;
    Ok(CommitResult {
        attempted_database_id: hex16(&commit["attempted_database_id"], "attempted_database_id")?,
        directory_identity: decode_file_identity(&commit["directory_identity"], "directory_identity")?,
        main_identity: decode_file_identity(&commit["main_identity"], "main_identity")?,
        attempted_transaction_id: decimal_u64(&commit["attempted_transaction_id"], "attempted_transaction_id")?,
        attempted_commit_nonce: hex16(&commit["attempted_commit_nonce"], "attempted_commit_nonce")?,
        durability: commit_durability(&commit["durability"])?,
        cleanup: decode_commit_cleanup(&commit["cleanup"])?,
        coordination_cleanup: coordination_cleanup(&commit["coordination_cleanup"], "coordination_cleanup")?,
        cause: None,
    })
}

/// Rebuild a `CreateResult` from the complete object returned by `database.create`.
pub(crate) fn create_result_from_wire(value: &Value, path: &str) -> Result<CreateResult, String> {
    let create = object(value, "create_result")?;
    exact_members(
        create,
        &[
            "address_family",
            "value_kind",
            "structure_kind",
            "value_tag",
            "database_id",
            "commit_nonce",
            "sidecar_id",
            "main_basename",
            "reader_capacity",
            "state",
            "residue_possible",
            "housekeeping",
            "visible_housekeeping",
        ],
        &["directory_identity", "main_identity", "sidecar_identity"],
        "create_result",
    )?;
    Ok(CreateResult {
        address_family: address_family(&create["address_family"])?,
        value_kind: value_kind(&create["value_kind"])?,
        structure_kind: structure_kind(&create["structure_kind"])?,
        value_tag: decode_value_tag(&create["value_tag"], "value_tag")?,
        database_id: hex16(&create["database_id"], "database_id")?,
        commit_nonce: hex16(&create["commit_nonce"], "commit_nonce")?,
        sidecar_id: hex16(&create["sidecar_id"], "sidecar_id")?,
        directory_identity: optional_file_identity(create.get("directory_identity"), "directory_identity")?,
        main_basename: decode_main_basename(&create["main_basename"], path)?,
        main_identity: optional_file_identity(create.get("main_identity"), "main_identity")?,
        sidecar_identity: optional_file_identity(create.get("sidecar_identity"), "sidecar_identity")?,
        reader_capacity: u32_integer(&create["reader_capacity"], "reader_capacity")?,
        state: creation_state(&create["state"])?,
        residue_possible: boolean(&create["residue_possible"], "residue_possible")?,
        housekeeping: decode_housekeeping(&create["housekeeping"], "housekeeping")?,
        visible_housekeeping: decode_housekeeping_artifacts(&create["visible_housekeeping"])?,
        cause: None,
    })
}

/// Rebuild a `LiveTransitionResult` from the complete object returned by
/// `database.initialize_live` or `database.reset_live`.
pub(crate) fn live_transition_result_from_wire(
    value: &Value,
    path: &str,
) -> Result<LiveTransitionResult, String> {
    let transition = object(value, "live_transition_result")?;
    exact_members(
        transition,
        &[
            "operation",
            "status",
            "database_id",
            "transaction_id",
            "commit_nonce",
            "directory_identity",
            "main_identity",
            "main_basename",
            "reader_capacity",
            "sidecar_id",
            "new_sidecar_location",
            "residue_possible",
            "housekeeping",
            "visible_housekeeping",
        ],
        &[
            "reset_policy",
            "previous_sidecar_identity",
            "new_sidecar_identity",
        ],
        "live_transition_result",
    )?;
    let reset_policy = match transition.get("reset_policy") {
        Some(value) if !value.is_null() => Some(match string(value, "reset_policy")? {
            "rollback_safe" => LiveResetPolicy::RollbackSafe,
            "discard_previous" => LiveResetPolicy::DiscardPrevious,
            _ => return Err("reset_policy must be rollback_safe or discard_previous".into()),
        }),
        _ => None,
    };
    Ok(LiveTransitionResult {
        operation: live_transition_operation(&transition["operation"])?,
        reset_policy,
        status: live_transition_status(&transition["status"])?,
        database_id: hex16(&transition["database_id"], "database_id")?,
        transaction_id: decimal_u64(&transition["transaction_id"], "transaction_id")?,
        commit_nonce: hex16(&transition["commit_nonce"], "commit_nonce")?,
        directory_identity: decode_file_identity(&transition["directory_identity"], "directory_identity")?,
        main_identity: decode_file_identity(&transition["main_identity"], "main_identity")?,
        main_basename: decode_main_basename(&transition["main_basename"], path)?,
        reader_capacity: u32_integer(&transition["reader_capacity"], "reader_capacity")?,
        sidecar_id: hex16(&transition["sidecar_id"], "sidecar_id")?,
        previous_sidecar_identity: optional_file_identity(
            transition.get("previous_sidecar_identity"),
            "previous_sidecar_identity",
        )?,
        new_sidecar_identity: optional_file_identity(
            transition.get("new_sidecar_identity"),
            "new_sidecar_identity",
        )?,
        new_sidecar_location: live_coordination_location(&transition["new_sidecar_location"])?,
        residue_possible: boolean(&transition["residue_possible"], "residue_possible")?,
        housekeeping: decode_housekeeping(&transition["housekeeping"], "housekeeping")?,
        visible_housekeeping: decode_housekeeping_artifacts(&transition["visible_housekeeping"])?,
        cause: None,
    })
}
