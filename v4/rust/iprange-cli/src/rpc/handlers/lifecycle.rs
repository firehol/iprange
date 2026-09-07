//! Explicit database creation and metadata replacement handlers.

use std::path::Path;

use iprange_livedb::error::Error;
use iprange_livedb::publication::{
    ArtifactKind, ArtifactPresence, CleanupArtifact, CoordinationCleanup, DirectoryRole,
    Housekeeping, HousekeepingArtifact, HousekeepingState,
};
use iprange_livedb::validation::LocalFileIdentity;
use iprange_livedb::{
    create_live, resolve_create_live, AddressFamily, CloseOutcome, CommitDurability, CreateResult,
    CreationState, LiveTransitionResolutionMode, LiveWriter, StructureKind, TransactionBudget,
    ValueKind, ValueTag,
};
use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;
use super::convert;
use super::reader;

pub fn validate_database_create(params: &Value) -> Result<(), String> {
    let object = exact_object(
        params,
        &[
            "path",
            "family",
            "value_kind",
            "structure_kind",
            "value_tag",
            "reader_capacity",
        ],
    )?;
    reader::validate_path(object["path"].as_str())?;
    match object["family"].as_str() {
        Some("ipv4") | Some("ipv6") => {}
        _ => return Err("family must be ipv4 or ipv6".into()),
    }
    match object["value_kind"].as_str() {
        Some("direct") | Some("membership") | Some("structured") => {}
        _ => return Err("value_kind must be direct, membership, or structured".into()),
    }
    let structure_valid = matches!(
        (
            object["value_kind"].as_str(),
            object["structure_kind"].as_str()
        ),
        (Some("direct") | Some("membership"), Some("none"))
            | (Some("structured"), Some("network_enrichment_v1"))
    );
    if !structure_valid {
        return Err("structure_kind is incompatible with value_kind".into());
    }
    validate_value_tag(&object["value_tag"])?;
    u32_member(object, "reader_capacity")?;
    Ok(())
}

pub fn validate_database_metadata_replace(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["path", "metadata", "writer_budget"])?;
    reader::validate_path(object["path"].as_str())?;
    validate_metadata(&object["metadata"], false)?;
    validate_writer_budget(&object["writer_budget"])
}

pub fn database_create(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let path = required_str(object, "path")?;
    require_creatable_parent(Path::new(path))?;
    let address_family = match required_str(object, "family")? {
        "ipv4" => AddressFamily::Ipv4,
        _ => AddressFamily::Ipv6,
    };
    let value_kind = match required_str(object, "value_kind")? {
        "direct" => ValueKind::Direct,
        "membership" => ValueKind::Membership,
        _ => ValueKind::Structured,
    };
    let structure_kind = if value_kind == ValueKind::Structured {
        StructureKind::NetworkEnrichmentV1
    } else {
        StructureKind::None
    };
    let value_tag = value_tag(&object["value_tag"]).map_err(HandlerError::invalid_params)?;
    let reader_capacity = u32_value(object.get("reader_capacity").unwrap_or(&Value::Null))
        .map_err(HandlerError::invalid_params)?;

    let mut result = create_live(
        path,
        address_family,
        value_kind,
        structure_kind,
        value_tag,
        reader_capacity,
        &state.token(),
    )
    .map_err(|error| sdk_error(&error, "not_started"))?;
    if result.state != CreationState::Created {
        result = resolve_create_live(
            path,
            &result,
            LiveTransitionResolutionMode::Complete,
            &state.token(),
        )
        .map_err(|error| sdk_error(&error, "outcome_unknown"))?;
    }
    if result.state != CreationState::Created {
        let details = json!({"create_result": create_result(&result)?});
        let cause = result.cause.as_ref().map_or_else(
            || "live database creation did not complete".to_owned(),
            ToString::to_string,
        );
        let code = result
            .cause
            .as_ref()
            .map(|error| reader::sdk_code(error.code()))
            .unwrap_or("io");
        let outcome = match result.state {
            CreationState::OutcomeUnknown => "outcome_unknown",
            CreationState::NotCreated => "not_started",
            CreationState::Created => "outcome_unknown",
        };
        return Err(HandlerError {
            code,
            outcome,
            message: cause,
            details: Some(details),
        });
    }
    let mut converted = create_result(&result)?;
    converted["method"] = json!("iprange.v1.database.create");
    bounded(converted)
}

pub fn database_metadata_replace(
    state: &mut SessionState,
    params: Value,
) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let path = Path::new(required_str(object, "path")?);
    require_existing_database(path)?;
    let metadata = metadata_value(&object["metadata"])?;
    let budget = writer_budget(&object["writer_budget"]).map_err(HandlerError::invalid_params)?;

    let mut writer = match LiveWriter::open(path, budget, &state.token()) {
        Ok(writer) => writer,
        Err(error) => return Err(sdk_error(&error, "not_started")),
    };
    let staged = match &metadata {
        MetadataValue::Clear => writer.clear_metadata_json(&state.token()),
        MetadataValue::Replace(bytes) => writer.set_metadata_json(bytes, &state.token()),
        MetadataValue::Keep => Ok(false),
    };
    let stage_failed = staged.is_err();
    let should_commit = match &metadata {
        MetadataValue::Replace(_) => !stage_failed,
        MetadataValue::Clear => staged.as_ref().copied().unwrap_or(false),
        MetadataValue::Keep => false,
    };
    let commit = if should_commit {
        Some(writer.commit(&state.token()))
    } else {
        None
    };
    if let Err(failure) = staged {
        let close = close_writer(&mut writer)?;
        let error = sdk_error(&failure, "not_started");
        return Err(HandlerError {
            details: Some(json!({
                "logical_change": "unchanged",
                "writer_close": close,
                "failure": {"code": error.code, "message": error.message},
            })),
            ..error
        });
    }
    let close = close_writer(&mut writer)?;
    let logical_change = if commit.is_some() {
        "changed"
    } else {
        "unchanged"
    };
    let mut details = match &commit {
        Some(Ok(commit)) => json!({
            "logical_change": logical_change,
            "commit": commit_result(commit)?,
            "writer_close": close,
        }),
        Some(Err(error)) => {
            let mut value = json!({
                "logical_change": logical_change,
                "writer_close": close,
            });
            value["failure"] = json!({
                "code": reader::sdk_code(error.code()),
                "message": error.to_string(),
            });
            value
        }
        None => json!({
            "logical_change": logical_change,
            "writer_close": close,
        }),
    };
    if let Some(Ok(commit)) = &commit {
        if commit.durability != CommitDurability::Committed || commit.cause.is_some() {
            let cause = commit.cause.as_ref();
            let code = cause.map_or("io", |error| reader::sdk_code(error.code()));
            let message = cause.map_or_else(
                || "metadata commit did not complete".to_owned(),
                ToString::to_string,
            );
            return Err(HandlerError {
                code,
                outcome: durability_outcome(commit.durability),
                message,
                details: Some(details),
            });
        }
    } else if let Some(Err(error)) = &commit {
        let failure = sdk_error(error, "not_started");
        return Err(HandlerError {
            details: Some(details),
            ..failure
        });
    }
    let close_failure = matches!(close["outcome"].as_str(), Some("close_incomplete"));
    if close_failure {
        return Err(HandlerError {
            code: "io",
            outcome: if commit.is_some() {
                "committed"
            } else {
                "not_started"
            },
            message: "live writer close is incomplete".into(),
            details: Some(details),
        });
    }
    details["method"] = json!("iprange.v1.database.metadata.replace");
    bounded(details)
}

fn require_creatable_parent(path: &Path) -> Result<(), HandlerError> {
    if path.file_name().is_none() {
        return Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("database destination has no file name: {}", path.display()),
        ));
    }
    let parent = path
        .parent()
        .filter(|value| !value.as_os_str().is_empty())
        .unwrap_or_else(|| Path::new("."));
    match parent.metadata() {
        Ok(value) if value.is_dir() => Ok(()),
        Ok(_) => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("database parent is not a directory: {}", parent.display()),
        )),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("database parent does not exist: {}", parent.display()),
        )),
        Err(error) => Err(HandlerError::new(
            "io",
            "not_started",
            format!("inspect database parent {}: {error}", parent.display()),
        )),
    }
}

fn require_existing_database(path: &Path) -> Result<(), HandlerError> {
    match path.metadata() {
        Ok(value) if value.is_file() => Ok(()),
        Ok(_) => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("live database is not a regular file: {}", path.display()),
        )),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("live database does not exist: {}", path.display()),
        )),
        Err(error) => Err(HandlerError::new(
            "io",
            "not_started",
            format!("inspect live database {}: {error}", path.display()),
        )),
    }
}

pub(crate) enum MetadataValue {
    Keep,
    Clear,
    Replace(Vec<u8>),
}

pub(crate) fn metadata_value(value: &Value) -> Result<MetadataValue, HandlerError> {
    let object = value
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("metadata must be an object"))?;
    match object.get("mode").and_then(Value::as_str) {
        Some("keep") => Ok(MetadataValue::Keep),
        Some("clear") => Ok(MetadataValue::Clear),
        Some("replace_utf8") => Ok(MetadataValue::Replace(
            object["text"]
                .as_str()
                .ok_or_else(|| HandlerError::invalid_params("metadata.text must be a string"))?
                .as_bytes()
                .to_vec(),
        )),
        Some("replace_base64") => decode_base64(
            object["base64"]
                .as_str()
                .ok_or_else(|| HandlerError::invalid_params("metadata.base64 must be a string"))?,
        )
        .map(MetadataValue::Replace)
        .map_err(HandlerError::invalid_params),
        Some("replace_file") => {
            let path = object["path"]
                .as_str()
                .ok_or_else(|| HandlerError::invalid_params("metadata.path must be a string"))?;
            read_file_exact(path).map(MetadataValue::Replace)
        }
        _ => Err(HandlerError::invalid_params("metadata.mode is invalid")),
    }
}

/// Read a metadata source with a hard cap, so a file that grows
/// between the size check and the read cannot drive an unbounded heap
/// allocation in the RPC process. Files at or below the cap read
/// exactly; longer files are refused like the pre-check would. The
/// capacity reserves only the stat-observed length (bounded by the
/// cap), never the full cap, so small metadata files do not reserve
/// 20 MiB.
fn read_bounded(path: &str, observed_len: u64) -> std::io::Result<Vec<u8>> {
    use std::io::Read as _;
    let mut file = std::fs::File::open(path)?;
    let cap = usize::try_from(iprange_livedb::MAX_METADATA_UNCOMPRESSED).unwrap_or(usize::MAX);
    let observed = usize::try_from(observed_len).unwrap_or(cap).min(cap);
    let mut bytes = Vec::with_capacity(observed);
    let mut chunk = [0u8; 64 * 1024];
    loop {
        let read = file.read(&mut chunk)?;
        if read == 0 {
            return Ok(bytes);
        }
        if bytes.len().saturating_add(read) > cap {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                "metadata source exceeds 20 MiB",
            ));
        }
        bytes.extend_from_slice(&chunk[..read]);
    }
}

fn read_file_exact(path: &str) -> Result<Vec<u8>, HandlerError> {
    let observed_len = match std::fs::metadata(path) {
        Ok(value) if !value.is_file() => {
            return Err(HandlerError::new(
                "invalid_path",
                "not_started",
                format!("metadata source is not a regular file: {path}"),
            ));
        }
        Ok(value) if value.len() > iprange_livedb::MAX_METADATA_UNCOMPRESSED => {
            return Err(HandlerError::new(
                "invalid_argument",
                "not_started",
                "metadata exceeds 20 MiB",
            ));
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            return Err(HandlerError::new(
                "invalid_path",
                "not_started",
                format!("metadata source does not exist: {path}"),
            ));
        }
        Err(error) => {
            return Err(HandlerError::new(
                "io",
                "not_started",
                format!("inspect metadata source {path}: {error}"),
            ));
        }
        Ok(value) => value.len(),
    };
    let read = read_bounded(path, observed_len);
    read.map_err(|error| {
        if error.kind() == std::io::ErrorKind::NotFound {
            HandlerError::new(
                "invalid_path",
                "not_started",
                format!("metadata source does not exist: {path}"),
            )
        } else {
            HandlerError::new(
                "io",
                "not_started",
                format!("read metadata source {path}: {error}"),
            )
        }
    })
}

fn close_writer(writer: &mut LiveWriter) -> Result<Value, HandlerError> {
    match writer.close() {
        Ok(result) => close_result(&result),
        Err(error) => Err(sdk_error(&error, "not_started")),
    }
}

pub(crate) fn create_result(result: &CreateResult) -> Result<Value, HandlerError> {
    let mut value = json!({
        "address_family": convert::address_family(result.address_family),
        "value_kind": convert::value_kind(result.value_kind),
        "structure_kind": convert::structure_kind(result.structure_kind),
        "value_tag": convert::value_tag(result.value_tag.bytes()),
        "database_id": convert::hex_id(&result.database_id),
        "commit_nonce": convert::hex_id(&result.commit_nonce),
        "sidecar_id": convert::hex_id(&result.sidecar_id),
        "main_basename": local_basename_text(&result.main_basename),
        "reader_capacity": result.reader_capacity,
        "state": creation_state(result.state),
        "residue_possible": result.residue_possible,
        "housekeeping": housekeeping(result.housekeeping, &[]),
        "visible_housekeeping": Value::Array(
            result.visible_housekeeping.iter().map(housekeeping_artifact).collect()
        ),
    });
    if let Some(identity) = result.directory_identity {
        value["directory_identity"] = file_identity(&identity)?;
    }
    if let Some(identity) = result.main_identity {
        value["main_identity"] = file_identity(&identity)?;
    }
    if let Some(identity) = result.sidecar_identity {
        value["sidecar_identity"] = file_identity(&identity)?;
    }
    Ok(value)
}

pub(crate) fn commit_result(result: &iprange_livedb::CommitResult) -> Result<Value, HandlerError> {
    Ok(json!({
        "attempted_database_id": convert::hex_id(&result.attempted_database_id),
        "directory_identity": file_identity(&result.directory_identity)?,
        "main_identity": file_identity(&result.main_identity)?,
        "attempted_transaction_id": convert::decimal_u64(result.attempted_transaction_id),
        "attempted_commit_nonce": convert::hex_id(&result.attempted_commit_nonce),
        "durability": commit_durability(result.durability),
        "cleanup": commit_cleanup(&result.cleanup),
        "coordination_cleanup": coordination_cleanup(result.coordination_cleanup),
    }))
}

pub(crate) fn close_result(result: &iprange_livedb::CloseResult) -> Result<Value, HandlerError> {
    let mut value = json!({
        "outcome": close_outcome(result.outcome),
        "cleanup": commit_cleanup(&result.cleanup),
        "coordination_cleanup": coordination_cleanup(result.coordination_cleanup),
    });
    if let Some(outcome) = result.abort_outcome {
        value["abort_outcome"] = json!(abort_outcome(outcome));
    }
    Ok(value)
}

fn creation_state(value: CreationState) -> &'static str {
    match value {
        CreationState::NotCreated => "not_created",
        CreationState::Created => "created",
        CreationState::OutcomeUnknown => "outcome_unknown",
    }
}

fn commit_durability(value: CommitDurability) -> &'static str {
    match value {
        CommitDurability::NotCommitted => "not_committed",
        CommitDurability::Committed => "committed",
        CommitDurability::OutcomeUnknown => "outcome_unknown",
    }
}

pub(crate) fn durability_outcome(value: CommitDurability) -> &'static str {
    commit_durability(value)
}

fn close_outcome(value: CloseOutcome) -> &'static str {
    match value {
        CloseOutcome::Closed => "closed",
        CloseOutcome::CloseIncomplete => "close_incomplete",
    }
}

fn abort_outcome(value: iprange_livedb::AbortOutcome) -> &'static str {
    match value {
        iprange_livedb::AbortOutcome::Aborted => "aborted",
        iprange_livedb::AbortOutcome::AbortIncomplete => "abort_incomplete",
    }
}

/// Render one artifact basename to its documented wire form
/// (iprange-jsonrpc-v1.md), honoring the platform encoding tag:
/// encoding 2 (Windows UTF-16LE units) maps every stored byte to the
/// same-numbered U+00xx character (the opaque per-byte form), and
/// encoding 1 keeps the bytes as the text's UTF-8 encoding.  ASCII
/// names therefore render unchanged under both encodings.
pub(crate) fn basename(bytes: &[u8], encoding: u16) -> String {
    match encoding {
        2 => bytes.iter().map(|&byte| char::from(byte)).collect(),
        _ => String::from_utf8_lossy(bytes).into_owned(),
    }
}

/// Render one SDK-local basename to its wire text, honoring the
/// platform encoding tag (external review finding).  Encoding 1 is
/// raw POSIX bytes (UTF-8 lossy), encoding 2 is UTF-16LE units (as
/// stored on Windows by ``LocalBasename::from_path``); the stored
/// bytes are never the wire text.  The create/transition results
/// must round-trip: a client passes the returned ``main_basename``
/// back unchanged and the resolver compares it against the clean
/// destination basename (spec iprange-jsonrpc-v1.md §database.*).
pub(crate) fn local_basename_text(value: &iprange_livedb::LocalBasename) -> String {
    match value.encoding() {
        2 => utf16le_text(value.as_bytes()),
        _ => String::from_utf8_lossy(value.as_bytes()).into_owned(),
    }
}

/// Decode raw UTF-16LE wire units (encoding 2) to text, lossy.
pub(crate) fn utf16le_text(bytes: &[u8]) -> String {
    let units: Vec<u16> = bytes
        .chunks_exact(2)
        .map(|pair| u16::from_le_bytes([pair[0], pair[1]]))
        .collect();
    String::from_utf16_lossy(&units)
}

pub(crate) fn file_identity(identity: &LocalFileIdentity) -> Result<Value, HandlerError> {
    let tail_zero = match identity.kind {
        1 => identity.bytes[16..].iter().all(|byte| *byte == 0),
        2 => identity.bytes[24..].iter().all(|byte| *byte == 0),
        _ => false,
    };
    if !tail_zero {
        return Err(HandlerError::new(
            "io",
            "not_started",
            format!("unsupported local file identity kind {}", identity.kind),
        ));
    }
    let device = u64::from_le_bytes(
        identity.bytes[0..8]
            .try_into()
            .map_err(|_| HandlerError::invalid_params("identity"))?,
    );
    let file = u64::from_le_bytes(
        identity.bytes[8..16]
            .try_into()
            .map_err(|_| HandlerError::invalid_params("identity"))?,
    );
    Ok(json!({
        "volume": convert::decimal_u64(device),
        "file": convert::decimal_u64(file),
    }))
}

pub(crate) fn housekeeping(state: Housekeeping, artifacts: &[HousekeepingArtifact]) -> Value {
    let listed = artifacts
        .iter()
        .map(housekeeping_artifact)
        .collect::<Vec<_>>();
    match state {
        Housekeeping::None => json!({"artifacts": []}),
        Housekeeping::CrashReappearancePossible => {
            json!({"state": "crash_reappearance_possible", "artifacts": listed})
        }
        Housekeeping::Visible => json!({"state": "visible", "artifacts": listed}),
    }
}

pub(crate) fn housekeeping_artifact(value: &HousekeepingArtifact) -> Value {
    let mut result = json!({
        "state": housekeeping_state(value.state),
        "directory_role": directory_role(value.directory_role),
        "directory_identity": file_identity_ok(&value.directory_identity),
        "basename_encoding": value.basename_encoding,
        "attempt_id": convert::hex_id(&value.attempt_id),
        "ordinal": value.ordinal,
        "envelope_basename": basename(&value.envelope_basename, value.basename_encoding),
        "envelope_identity": file_identity_ok(&value.envelope_identity),
        "source_basename": basename(&value.source_basename, value.basename_encoding),
        "inert_basename": basename(&value.inert_basename, value.basename_encoding),
        "source_presence": artifact_presence(value.source_presence),
        "inert_presence": artifact_presence(value.inert_presence),
        "kind": artifact_kind(value.kind),
        "creation_security": creation_security(&value.creation_security),
        "selected_envelope_sequence": convert::decimal_u64(value.selected_envelope_sequence),
    });
    // Optional SDK fields are absent, never null (wire rule): omit the
    // unknown identities instead of emitting null so the evidence can
    // round-trip through the strict decoders.
    if let Some(identity) = &value.source_identity {
        result["source_identity"] = file_identity_ok(identity);
    }
    if let Some(identity) = &value.inert_identity {
        result["inert_identity"] = file_identity_ok(identity);
    }
    result
}

fn housekeeping_state(value: HousekeepingState) -> &'static str {
    match value {
        HousekeepingState::MovePending => "move_pending",
        HousekeepingState::MoveAmbiguous => "move_ambiguous",
        HousekeepingState::Inert => "inert",
        HousekeepingState::Conflict => "conflict",
    }
}

fn directory_role(value: DirectoryRole) -> &'static str {
    match value {
        DirectoryRole::Destination => "destination",
        DirectoryRole::ScratchDirectory => "scratch_directory",
        DirectoryRole::MainFile => "main_file",
    }
}

fn artifact_presence(value: ArtifactPresence) -> &'static str {
    match value {
        ArtifactPresence::Absent => "absent",
        ArtifactPresence::Present => "present",
        ArtifactPresence::Unclassified => "unclassified",
    }
}

fn artifact_kind(value: ArtifactKind) -> &'static str {
    match value {
        ArtifactKind::PrivateOutput => "private_output",
        ArtifactKind::PrivateReservation => "private_reservation",
        ArtifactKind::OwnedCoordination => "owned_coordination",
        ArtifactKind::AuthorizedScratch => "authorized_scratch",
        ArtifactKind::OwnedMain => "owned_main",
        ArtifactKind::UnpublishedMainTail => "unpublished_main_tail",
    }
}

pub(crate) fn creation_security(value: &iprange_livedb::publication::CreationSecurity) -> Value {
    json!({
        "kind": value.kind,
        "commitment": value.commitment.iter().map(|byte| format!("{byte:02x}")).collect::<String>(),
    })
}

fn file_identity_ok(identity: &LocalFileIdentity) -> Value {
    file_identity(identity).unwrap_or_else(|error| json!({"error": error.message}))
}

pub(crate) fn commit_cleanup(value: &iprange_livedb::CommitCleanupArtifacts) -> Value {
    if value.is_empty() {
        return json!({});
    }
    json!({
        "artifacts": value.iter().map(commit_cleanup_artifact).collect::<Vec<_>>(),
    })
}

fn commit_cleanup_artifact(value: &iprange_livedb::CommitCleanupArtifact) -> Value {
    let mut result = json!({
        "directory_identity": file_identity_ok(&value.directory_identity),
        "main_basename": local_basename_text(&value.main_basename),
        "main_identity": file_identity_ok(&value.main_identity),
        "expected_database_id": convert::hex_id(&value.expected_database_id),
        "target_transaction_id": convert::decimal_u64(value.target_transaction_id),
        "target_commit_nonce": convert::hex_id(&value.target_commit_nonce),
        "committed_target_length": convert::decimal_u64(value.committed_target_length),
        "cleanup_error": reader::sdk_code(value.cleanup_error),
    });
    if let Some(tail) = value.observed_tail_end_exclusive {
        result["observed_tail_end_exclusive"] = json!(convert::decimal_u64(tail));
    }
    result
}

pub(crate) fn cleanup_artifact(value: &CleanupArtifact) -> Value {
    let mut result = json!({
        "kind": artifact_kind(value.kind),
        "directory_role": directory_role(value.directory_role),
        "directory_identity": file_identity_ok(&value.directory_identity),
        "basename_encoding": value.basename_encoding,
        "basename": convert::hex_bytes(&value.basename),
        "error": publication_problem(&value.error),
    });
    if let Some(identity) = &value.identity {
        result["identity"] = file_identity_ok(identity);
    }
    if let Some(security) = &value.creation_security {
        result["creation_security"] = creation_security(security);
    }
    if let Some(tail) = &value.unpublished_tail {
        result["unpublished_tail"] = json!({
            "expected_database_id": convert::hex_id(&tail.expected_database_id),
            "committed_target_transaction_id": convert::decimal_u64(tail.committed_target_transaction_id),
            "committed_target_nonce": convert::hex_id(&tail.committed_target_nonce),
            "committed_target_length": convert::decimal_u64(tail.committed_target_length),
            "observed_tail_end_exclusive": convert::decimal_u64(tail.observed_tail_end_exclusive),
        });
    }
    result
}

pub(crate) fn coordination_cleanup(value: CoordinationCleanup) -> Value {
    match value {
        CoordinationCleanup::None => json!({}),
        CoordinationCleanup::CleanupGuard => json!({"kind": "cleanup_guard"}),
        CoordinationCleanup::RetainedReaderCloseRequired => {
            json!({"kind": "retained_reader_close_required"})
        }
        CoordinationCleanup::RetainedWriterCloseRequired => {
            json!({"kind": "retained_writer_close_required"})
        }
    }
}

pub(crate) fn publication_problem(
    value: &iprange_livedb::publication::PublicationProblem,
) -> Value {
    let mut result = json!({
        "code": reader::sdk_code(value.code),
        "detail": value.detail,
    });
    if let Some(code) = value.os_code {
        result["os_code"] = json!(code);
    }
    result
}

pub(crate) fn sdk_error(error: &Error, outcome: &'static str) -> HandlerError {
    HandlerError::new(reader::sdk_code(error.code()), outcome, error.to_string())
}

fn bounded(result: Value) -> Result<Value, HandlerError> {
    reader::bounded_result(result)
}

fn exact_object<'a>(
    value: &'a Value,
    fields: &[&str],
) -> Result<&'a serde_json::Map<String, Value>, String> {
    let object = value.as_object().ok_or("value must be an object")?;
    for key in object.keys() {
        if !fields.contains(&key.as_str()) {
            return Err(format!("unknown member {key:?}"));
        }
    }
    for field in fields {
        if !object.contains_key(*field) {
            return Err(format!("missing member {field:?}"));
        }
    }
    Ok(object)
}

fn required_str<'a>(
    object: &'a serde_json::Map<String, Value>,
    name: &str,
) -> Result<&'a str, HandlerError> {
    object[name]
        .as_str()
        .ok_or_else(|| HandlerError::invalid_params(format!("{name} must be a string")))
}

fn u32_member(object: &serde_json::Map<String, Value>, name: &str) -> Result<(), String> {
    let value = object[name]
        .as_u64()
        .and_then(|value| u32::try_from(value).ok());
    if value.is_none() {
        return Err(format!("{name} must be a u32 integer"));
    }
    Ok(())
}

fn u32_value(value: &Value) -> Result<u32, String> {
    value
        .as_u64()
        .and_then(|value| u32::try_from(value).ok())
        .ok_or_else(|| "value must be a u32 integer".to_owned())
}

pub(crate) fn validate_value_tag(value: &Value) -> Result<(), String> {
    let object = value.as_object().ok_or("value_tag must be an object")?;
    if object.len() == 1 && object.contains_key("text") {
        let tag = object["text"]
            .as_str()
            .ok_or("value_tag.text must be a string")?;
        if tag.len() > 15 || tag.contains('\0') {
            return Err("value_tag.text must encode 0 through 15 bytes without NUL".into());
        }
        return Ok(());
    }
    if object.len() == 1 && object.contains_key("hex") {
        let tag = object["hex"]
            .as_str()
            .ok_or("value_tag.hex must be a string")?;
        let valid = tag.len() <= 30
            && tag.len() % 2 == 0
            && tag
                .bytes()
                .all(|byte| matches!(byte, b'0'..=b'9' | b'a'..=b'f'));
        let contains_nul = tag.as_bytes().chunks_exact(2).any(|pair| pair == b"00");
        if contains_nul {
            return Err("value_tag.hex must not encode a NUL byte".into());
        }
        return valid.then_some(()).ok_or_else(|| {
            "value_tag.hex must be even lowercase hex encoding at most 15 bytes".to_owned()
        });
    }
    Err("value_tag must contain exactly one of text or hex".into())
}

pub(crate) fn value_tag(value: &Value) -> Result<ValueTag, String> {
    let object = value.as_object().ok_or("value_tag must be an object")?;
    if let Some(text) = object.get("text").and_then(Value::as_str) {
        return ValueTag::new(text.as_bytes()).ok_or_else(|| "value_tag is invalid".to_owned());
    }
    if let Some(hex) = object.get("hex").and_then(Value::as_str) {
        let mut bytes = Vec::with_capacity(hex.len() / 2);
        let chars = hex.as_bytes();
        for pair in chars.chunks_exact(2) {
            let high = (pair[0] as char)
                .to_digit(16)
                .ok_or("invalid value_tag hex")?;
            let low = (pair[1] as char)
                .to_digit(16)
                .ok_or("invalid value_tag hex")?;
            bytes.push(((high << 4) | low) as u8);
        }
        return ValueTag::new(&bytes).ok_or_else(|| "value_tag is invalid".to_owned());
    }
    Err("value_tag must contain text or hex".into())
}

pub(crate) fn validate_metadata(value: &Value, allow_keep: bool) -> Result<(), String> {
    let object = value.as_object().ok_or("metadata must be an object")?;
    match object.get("mode").and_then(Value::as_str) {
        Some("keep") if allow_keep => exact_object(value, &["mode"]).map(|_| ()),
        Some("clear") => exact_object(value, &["mode"]).map(|_| ()),
        Some("replace_utf8") => {
            exact_object(value, &["mode", "text"])?;
            object["text"]
                .as_str()
                .ok_or_else(|| "metadata.text must be a string".to_owned())
                .map(|_| ())
        }
        Some("replace_base64") => {
            exact_object(value, &["mode", "base64"])?;
            let text = object["base64"]
                .as_str()
                .ok_or("metadata.base64 must be a string")?;
            decode_base64(text).map(|_| ())
        }
        Some("replace_file") => {
            exact_object(value, &["mode", "path"])?;
            reader::validate_path(object["path"].as_str())
        }
        _ => Err("metadata.mode is invalid for this method".into()),
    }
}

pub(crate) fn validate_writer_budget(value: &Value) -> Result<(), String> {
    let object = exact_object(
        value,
        &[
            "max_heap_bytes",
            "max_private_pages",
            "max_growth_pages",
            "max_open_files",
        ],
    )?;
    for field in ["max_heap_bytes", "max_private_pages", "max_growth_pages"] {
        reader::positive_u64_string(object[field].as_str())
            .map_err(|error| format!("writer_budget.{field}: {error}"))?;
    }
    reader::positive_u32(&object["max_open_files"])
        .map(|_| ())
        .map_err(|error| format!("writer_budget.max_open_files: {error}"))
}

pub(crate) fn writer_budget(value: &Value) -> Result<TransactionBudget, String> {
    let object = value.as_object().ok_or("writer_budget must be an object")?;
    Ok(TransactionBudget {
        max_heap_bytes: reader::u64_string(object["max_heap_bytes"].as_str())?,
        max_private_pages: reader::u64_string(object["max_private_pages"].as_str())?,
        max_file_growth_pages: reader::u64_string(object["max_growth_pages"].as_str())?,
        max_open_files: u32_value(&object["max_open_files"])?,
    })
}

/// RFC 4648 standard alphabet with required padding (wire inverse of
/// [`decode_base64`]).
pub(crate) fn encode_base64(bytes: &[u8]) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut output = String::with_capacity(bytes.len().div_ceil(3) * 4);
    for chunk in bytes.chunks(3) {
        let word = u32::from(chunk[0]) << 16
            | u32::from(*chunk.get(1).unwrap_or(&0)) << 8
            | u32::from(*chunk.get(2).unwrap_or(&0));
        output.push(ALPHABET[(word >> 18) as usize & 63] as char);
        output.push(ALPHABET[(word >> 12) as usize & 63] as char);
        output.push(if chunk.len() > 1 {
            ALPHABET[(word >> 6) as usize & 63] as char
        } else {
            '='
        });
        output.push(if chunk.len() > 2 {
            ALPHABET[word as usize & 63] as char
        } else {
            '='
        });
    }
    output
}

pub(crate) fn decode_base64(value: &str) -> Result<Vec<u8>, String> {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    if value.len() % 4 != 0 {
        return Err("base64 length must be a multiple of four".into());
    }
    let bytes = value.as_bytes();
    let mut output = Vec::with_capacity(bytes.len() / 4 * 3);
    for (index, chunk) in bytes.chunks_exact(4).enumerate() {
        let last = index == bytes.len() / 4 - 1;
        let padding = chunk.iter().rev().take_while(|byte| **byte == b'=').count();
        if padding > 2 || (last && padding == 0 && bytes.is_empty()) {
            return Err("base64 padding is invalid".into());
        }
        if !last && padding != 0 {
            return Err("base64 padding is not at the end".into());
        }
        let mut word = 0u32;
        for (position, byte) in chunk.iter().enumerate() {
            let digit = if *byte == b'=' {
                if !last || position < 4 - padding {
                    return Err("base64 padding is invalid".into());
                }
                0
            } else {
                ALPHABET
                    .iter()
                    .position(|candidate| candidate == byte)
                    .ok_or("base64 uses the standard alphabet only")? as u32
            };
            word = (word << 6) | digit;
        }
        let significant = 3 - padding;
        let decoded = word.to_be_bytes();
        output.extend_from_slice(&decoded[1..1 + significant]);
        if padding > 0 {
            let bits = padding * 8;
            if word & ((1 << bits) - 1) != 0 {
                return Err("base64 has non-canonical trailing bits".into());
            }
        }
    }
    Ok(output)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn artifact_identity(volume: u64, file: u64) -> iprange_livedb::validation::LocalFileIdentity {
        let mut bytes = [0u8; 32];
        bytes[0..8].copy_from_slice(&volume.to_le_bytes());
        bytes[8..16].copy_from_slice(&file.to_le_bytes());
        iprange_livedb::validation::LocalFileIdentity { kind: 1, bytes }
    }

    #[test]
    fn artifact_encoders_omit_absent_optional_identities() {
        // Optional SDK fields are absent, never null: an artifact whose
        // identity observations are unknown must round-trip through the
        // strict decoders, so the emitters must omit the members.
        let problem = iprange_livedb::publication::PublicationProblem {
            code: iprange_livedb::ErrorCode::Io,
            detail: "x".into(),
            os_code: None,
        };
        let cleanup = super::cleanup_artifact(&iprange_livedb::publication::CleanupArtifact {
            kind: iprange_livedb::publication::ArtifactKind::PrivateOutput,
            directory_role: iprange_livedb::publication::DirectoryRole::Destination,
            directory_identity: artifact_identity(1, 2),
            basename_encoding: 1,
            basename: b"current.iprange".to_vec().into_boxed_slice(),
            identity: None,
            creation_security: None,
            unpublished_tail: None,
            error: problem,
        });
        assert!(
            cleanup.get("identity").is_none(),
            "absent identity must be omitted"
        );
        assert!(super::super::publication_evidence::decode_cleanup_artifact(&cleanup).is_ok());

        let housekeeping =
            super::housekeeping_artifact(&iprange_livedb::publication::HousekeepingArtifact {
                state: iprange_livedb::publication::HousekeepingState::Inert,
                directory_role: iprange_livedb::publication::DirectoryRole::MainFile,
                directory_identity: artifact_identity(1, 3),
                basename_encoding: 1,
                attempt_id: [7; 16],
                ordinal: 4,
                envelope_basename: b"e".to_vec().into_boxed_slice(),
                envelope_identity: artifact_identity(1, 4),
                source_basename: b"s".to_vec().into_boxed_slice(),
                inert_basename: b"i".to_vec().into_boxed_slice(),
                source_presence: iprange_livedb::publication::ArtifactPresence::Unclassified,
                source_identity: None,
                inert_presence: iprange_livedb::publication::ArtifactPresence::Unclassified,
                inert_identity: None,
                kind: iprange_livedb::publication::ArtifactKind::OwnedMain,
                creation_security: iprange_livedb::publication::CreationSecurity {
                    kind: 1,
                    commitment: [9; 32],
                },
                selected_envelope_sequence: 11,
            });
        assert!(housekeeping.get("source_identity").is_none());
        assert!(housekeeping.get("inert_identity").is_none());
        let artifacts = serde_json::json!([housekeeping]);
        assert!(super::super::lifecycle_live::decode_housekeeping_artifacts(&artifacts).is_ok());
    }

    #[test]
    fn metadata_forms_reject_unknown_members_and_keep_when_required() {
        assert!(validate_metadata(&json!({"mode":"keep"}), true).is_ok());
        assert!(validate_metadata(&json!({"mode":"keep"}), false).is_err());
        assert!(validate_metadata(&json!({"mode":"replace_utf8","text":"x"}), false).is_ok());
        assert!(
            validate_metadata(&json!({"mode":"replace_utf8","text":"x","extra":1}), false).is_err()
        );
        assert!(
            validate_metadata(&json!({"mode":"replace_base64","base64":"Zg=="}), false).is_ok()
        );
    }

    #[test]
    fn base64_decoder_accepts_only_canonical_standard_encoding() {
        assert_eq!(decode_base64("").unwrap(), Vec::<u8>::new());
        assert_eq!(decode_base64("Zg==").unwrap(), b"f".to_vec());
        assert_eq!(decode_base64("Zm8=").unwrap(), b"fo".to_vec());
        assert_eq!(decode_base64("Zm9v").unwrap(), b"foo".to_vec());
        assert_eq!(decode_base64("Zm9vYmFy").unwrap(), b"foobar".to_vec());
        assert!(decode_base64("Zg=").is_err());
        assert!(decode_base64("Zh==").is_err());
        assert!(decode_base64("Zg== ").is_err());
        assert!(decode_base64("Zg-=").is_err());
    }

    #[test]
    fn create_result_conversion_uses_optional_and_exact_fields() {
        let unique = format!(
            "iprange-create-result-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        );
        let main = std::env::temp_dir().join(unique);
        let result = create_live(
            &main,
            AddressFamily::Ipv4,
            ValueKind::Direct,
            StructureKind::None,
            ValueTag::new(b"tag").unwrap(),
            7,
            &iprange_livedb::CancellationToken::new(),
        )
        .unwrap();
        let value = create_result(&result).unwrap();
        assert_eq!(value["state"], "created");
        assert!(value["main_basename"]
            .as_str()
            .unwrap_or("")
            .starts_with("iprange-create-result-"));
        assert!(value.get("sidecar_identity").is_some());
        assert_eq!(value["housekeeping"], json!({"artifacts": []}));

        let mut sidecar = main.clone().into_os_string();
        sidecar.push(".readers");
        std::fs::remove_file(&main).unwrap();
        std::fs::remove_file(std::path::PathBuf::from(sidecar)).unwrap();
    }
}

#[cfg(test)]
mod handler_tests {
    use super::*;
    use serde_json::json;

    fn unique(label: &str) -> std::path::PathBuf {
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        std::env::temp_dir().join(format!(
            "iprange-lifecycle-{label}-{}-{unique}",
            std::process::id()
        ))
    }

    fn sidecar(main: &std::path::Path) -> std::path::PathBuf {
        let mut name = main.file_name().unwrap().to_os_string();
        name.push(".readers");
        main.with_file_name(name)
    }

    #[test]
    fn create_and_metadata_replacement_return_factual_results() {
        let main = unique("metadata");
        let mut state = SessionState::default();
        let created = database_create(
            &mut state,
            json!({
                "path": main.display().to_string(),
                "family": "ipv4",
                "value_kind": "direct",
                "structure_kind": "none",
                "value_tag": {"text": "tag"},
                "reader_capacity": 2
            }),
        )
        .unwrap();
        assert_eq!(created["state"], "created");

        let budget = json!({
            "max_heap_bytes": "2097152",
            "max_private_pages": "10000",
            "max_growth_pages": "10000",
            "max_open_files": 2
        });
        let absent_clear = database_metadata_replace(
            &mut state,
            json!({
                "path": main.display().to_string(),
                "metadata": {"mode":"clear"},
                "writer_budget": budget
            }),
        )
        .unwrap();
        assert_eq!(absent_clear["logical_change"], "unchanged");
        assert!(absent_clear.get("commit").is_none());

        let empty = database_metadata_replace(
            &mut state,
            json!({
                "path": main.display().to_string(),
                "metadata": {"mode":"replace_utf8","text":""},
                "writer_budget": budget
            }),
        )
        .unwrap();
        assert_eq!(empty["logical_change"], "changed");
        assert_eq!(empty["commit"]["durability"], "committed");

        let same = database_metadata_replace(
            &mut state,
            json!({
                "path": main.display().to_string(),
                "metadata": {"mode":"replace_utf8","text":""},
                "writer_budget": budget
            }),
        )
        .unwrap();
        assert_eq!(same["logical_change"], "changed");
        assert_eq!(same["commit"]["durability"], "committed");
        assert_eq!(same["writer_close"]["outcome"], "closed");

        std::fs::remove_file(&main).unwrap();
        std::fs::remove_file(sidecar(&main)).unwrap();
    }
}

#[cfg(test)]
mod path_error_tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn create_and_metadata_reject_missing_paths_before_sdk_work() {
        let mut state = SessionState::default();
        let create = database_create(
            &mut state,
            json!({
                "path": "/definitely/missing/iprange-parent/db.iprange",
                "family": "ipv4",
                "value_kind": "direct",
                "structure_kind": "none",
                "value_tag": {"text":"tag"},
                "reader_capacity": 1
            }),
        )
        .unwrap_err();
        assert_eq!(
            (create.code, create.outcome),
            ("invalid_path", "not_started")
        );

        let metadata = database_metadata_replace(
            &mut state,
            json!({
                "path": "/definitely/missing/db.iprange",
                "metadata": {"mode":"clear"},
                "writer_budget": {
                    "max_heap_bytes":"2097152",
                    "max_private_pages":"10000",
                    "max_growth_pages":"10000",
                    "max_open_files":2
                }
            }),
        )
        .unwrap_err();
        assert_eq!(
            (metadata.code, metadata.outcome),
            ("invalid_path", "not_started")
        );
    }
}

#[cfg(test)]
mod positive_budget_tests {
    use super::*;

    #[test]
    fn writer_budget_rejects_zero_but_accepts_positive_limits() {
        let valid = json!({
            "max_heap_bytes": "1",
            "max_private_pages": "1",
            "max_growth_pages": "1",
            "max_open_files": 1
        });
        assert_eq!(validate_writer_budget(&valid), Ok(()));
        for field in ["max_heap_bytes", "max_private_pages", "max_growth_pages"] {
            let mut zero = valid.clone();
            zero[field] = json!("0");
            assert!(validate_writer_budget(&zero).is_err());
        }
        let mut zero_files = valid.clone();
        zero_files["max_open_files"] = json!(0);
        assert!(validate_writer_budget(&zero_files).is_err());
    }
}

#[cfg(test)]
mod local_basename_tests {
    use super::*;

    /// UTF-16LE wire bytes of one name (what LocalBasename stores in
    /// its encoding-2 Windows branch).
    fn utf16le_wire(text: &str) -> Vec<u8> {
        let mut bytes = Vec::new();
        for unit in text.encode_utf16() {
            bytes.extend_from_slice(&unit.to_le_bytes());
        }
        bytes
    }

    #[test]
    fn utf16le_encoding_renders_to_clean_text() {
        // External review finding: on Windows LocalBasename stores
        // UTF-16LE units (encoding 2); the wire rendering must decode
        // them to text (round-trippable through the resolvers) instead
        // of emitting the NUL-interleaved raw units.
        let bytes = utf16le_wire("live.iprange");
        assert_eq!(utf16le_text(&bytes), "live.iprange");
        // Precondition: the raw UTF-16LE units carry the (ASCII) code
        // point in the low byte and NUL in the high byte, so every
        // odd-index byte must be zero and every even-index byte
        // non-zero.  This pins that the wire was really the
        // NUL-interleaved unit stream the decoder must clean up.
        for (index, byte) in bytes.iter().enumerate() {
            if index % 2 == 1 {
                assert_eq!(*byte, 0, "precondition: high byte {index} is not NUL");
            } else {
                assert_ne!(*byte, 0, "precondition: low byte {index} is NUL");
            }
        }
    }

    #[cfg(not(windows))]
    #[test]
    fn posix_encoding_renders_raw_bytes_lossy() {
        let name = std::env::temp_dir().join("live.iprange");
        let local = iprange_livedb::LocalBasename::from_path(&name).unwrap();
        assert_eq!(local.encoding(), 1);
        assert_eq!(local_basename_text(&local), "live.iprange");
    }

    #[cfg(windows)]
    #[test]
    fn windows_encoding_renders_utf16le_text() {
        let name = std::env::temp_dir().join("live.iprange");
        let local = iprange_livedb::LocalBasename::from_path(&name).unwrap();
        assert_eq!(local.encoding(), 2);
        assert_eq!(local_basename_text(&local), "live.iprange");
    }

    #[test]
    fn artifact_basename_wire_preserves_every_byte() {
        // The opaque per-byte wire form (iprange-jsonrpc-v1.md):
        // UTF-16LE units for "é" are E9 00; rendering must keep both
        // bytes as the U+00E9 U+0000 characters, never collapse them
        // through UTF-8 lossy, and decoding returns the exact units.
        let units = [0xe9, 0x00];
        let rendered = basename(&units, 2);
        assert_eq!(rendered, "\u{e9}\u{0}");
        for (byte, ch) in units.iter().zip(rendered.chars()) {
            assert_eq!(*byte as u32, ch as u32);
        }
        assert_eq!(String::from_utf8_lossy(&units), "\u{fffd}\u{0}");
    }

    #[test]
    fn posix_artifact_basename_wire_is_raw_utf8_text() {
        // Encoding 1 keeps the bytes as the text's UTF-8 encoding: an
        // ASCII name renders unchanged and multi-byte UTF-8 text stays
        // as the same text (never the per-byte projection, which the
        // encoding-1 decoder treats as UTF-8 bytes again).
        let ascii = b"live.iprange";
        assert_eq!(basename(ascii, 1), "live.iprange");
        assert_eq!(basename("größe.iprange".as_bytes(), 1), "größe.iprange");
    }

    #[test]
    fn incomplete_utf16le_tail_is_lossy_not_panicking() {
        // An odd trailing byte (e.g. a truncated basename) decodes
        // lossily instead of panicking or truncating the result.
        let mut bytes = utf16le_wire("live");
        bytes.push(0x61);
        let text = utf16le_text(&bytes);
        assert!(text.starts_with("live"));
    }
}
