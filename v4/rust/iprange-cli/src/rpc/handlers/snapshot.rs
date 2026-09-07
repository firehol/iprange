//! `iprange.v1.snapshot`: compact unsigned immutable snapshot.

use iprange_livedb::error::ErrorCode;
use iprange_livedb::publication::{
    CleanupArtifact, CleanupArtifacts, CoordinationCleanup, PublicationPolicy, PublicationResult,
    PublicationStatus,
};
use iprange_livedb::snapshot::{
    snapshot_to, SnapshotBudget, SnapshotPreparationFailure, SnapshotPublicationPolicy,
    SnapshotResult, SnapshotSourceMode,
};
use iprange_livedb::validation::LocalFileIdentity;
use serde_json::{json, Map, Value};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;
use super::lifecycle;
use super::publication_evidence;
use super::reader::{
    bounded_result, member_object, positive_u32, positive_u64_string, publication_policy, sdk_code,
    u64_string, validate_path,
};

pub fn validate_snapshot(params: &Value) -> Result<(), String> {
    let object = params.as_object().ok_or("params must be an object")?;
    for key in object.keys() {
        if !matches!(
            key.as_str(),
            "source" | "destination" | "publication_policy" | "snapshot_budget"
        ) {
            return Err(format!("unknown member {key:?}"));
        }
    }
    for field in [
        "source",
        "destination",
        "publication_policy",
        "snapshot_budget",
    ] {
        if !object.contains_key(field) {
            return Err(format!("missing member {field:?}"));
        }
    }
    let source = object["source"]
        .as_object()
        .ok_or("source must be an object")?;
    if source.len() != 2 || !source.contains_key("path") || !source.contains_key("mode") {
        return Err("source requires exactly path and mode".into());
    }
    validate_path(source["path"].as_str())?;
    match source["mode"].as_str() {
        Some("immutable") | Some("live") => {}
        _ => return Err("source.mode must be immutable or live".into()),
    }
    validate_path(object["destination"].as_str())?;
    publication_policy(object["publication_policy"].as_str())
        .map_err(|_| "publication_policy is invalid".to_string())?;
    let budget = object["snapshot_budget"]
        .as_object()
        .ok_or("snapshot_budget must be an object")?;
    if budget.len() != 3
        || !budget.contains_key("max_heap_bytes")
        || !budget.contains_key("max_output_pages")
        || !budget.contains_key("max_open_files")
    {
        return Err(
            "snapshot_budget requires exactly max_heap_bytes, max_output_pages, and max_open_files"
                .into(),
        );
    }
    positive_u64_string(budget["max_heap_bytes"].as_str())
        .map_err(|error| format!("snapshot_budget.max_heap_bytes: {error}"))?;
    positive_u64_string(budget["max_output_pages"].as_str())
        .map_err(|error| format!("snapshot_budget.max_output_pages: {error}"))?;
    positive_u32(&budget["max_open_files"])
        .map_err(|error| format!("snapshot_budget.max_open_files: {error}"))?;
    Ok(())
}

pub fn snapshot(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let source = member_object(object, "source").map_err(HandlerError::invalid_params)?;
    let source_path = source["path"]
        .as_str()
        .expect("validator checked source.path");
    let source_mode = match source["mode"]
        .as_str()
        .expect("validator checked source.mode")
    {
        "immutable" => SnapshotSourceMode::Immutable,
        _ => SnapshotSourceMode::Live,
    };
    let destination = object["destination"]
        .as_str()
        .expect("validator checked destination");
    let policy = publication_policy(object["publication_policy"].as_str())
        .map_err(|_| HandlerError::invalid_params("publication_policy is invalid"))?;
    let budget = decode_budget(object)?;

    match snapshot_to(
        source_path,
        source_mode,
        destination,
        snapshot_policy(policy),
        &budget,
        &state.token(),
    ) {
        Ok(result) => publication_success(&result),
        Err(failure) => Err(publication_failure(&failure)),
    }
}

/// `snapshot_to` reuses the publication policy unchanged; the SDK type
/// alias exists to keep the snapshot boundary self-describing.
fn snapshot_policy(policy: PublicationPolicy) -> SnapshotPublicationPolicy {
    policy
}

fn decode_budget(object: &serde_json::Map<String, Value>) -> Result<SnapshotBudget, HandlerError> {
    let budget = member_object(object, "snapshot_budget").map_err(HandlerError::invalid_params)?;
    Ok(SnapshotBudget::new(
        u64_string(budget["max_heap_bytes"].as_str()).map_err(HandlerError::invalid_params)?,
        u64_string(budget["max_output_pages"].as_str()).map_err(HandlerError::invalid_params)?,
        budget["max_open_files"]
            .as_u64()
            .and_then(|value| u32::try_from(value).ok())
            .ok_or_else(|| HandlerError::invalid_params("max_open_files must be u32"))?,
    ))
}

fn publication_success(result: &SnapshotResult) -> Result<Value, HandlerError> {
    if let Some(cause) = result.publication.cause.as_ref() {
        // An SDK cause is never a success field; it becomes the error
        // message while the complete publication facts stay available.
        let outcome = match result.publication.publication {
            PublicationStatus::Published => "published",
            PublicationStatus::OutcomeUnknown => "outcome_unknown",
            PublicationStatus::NotPublished => "not_published",
        };
        return Err(HandlerError {
            code: publication_code(cause.code),
            outcome,
            message: format!("snapshot publication failed: {}", cause.detail),
            details: Some(json!({"publication": publication_result(&result.publication)})),
        });
    }
    bounded_result(json!({
        "method": "iprange.v1.snapshot",
        "publication": publication_result(&result.publication),
    }))
}

fn publication_failure(failure: &SnapshotPreparationFailure) -> HandlerError {
    HandlerError {
        code: publication_code(failure.cause.code),
        // A preparation failure never completed a durable publication;
        // the discarded-attempt and residue facts stay in `details`.
        outcome: "not_started",
        message: format!("snapshot preparation failed: {}", failure.cause.detail),
        details: Some(preparation_details(failure)),
    }
}

fn preparation_details(failure: &SnapshotPreparationFailure) -> Value {
    json!({
        "cleanup_state": cleanup_state(failure.cleanup_state()),
        "cleanup": cleanup_artifacts(&failure.cleanup),
        "coordination_cleanup": coordination_cleanup(failure.coordination_cleanup),
        "housekeeping": lifecycle::housekeeping(
            failure.housekeeping.clone(),
            &failure.visible_housekeeping,
        ),
        "visible_housekeeping": lifecycle::visible_housekeeping(&failure.visible_housekeeping),
        "output": failure.output.as_ref().map(private_attempt),
    })
}

/// Mechanical `PublicationResult` conversion for the frozen wire
/// schema (v4/cli/schema/results.py). `attempt` carries the complete
/// SDK attempt object (publication_evidence::publication_attempt).
pub(crate) fn publication_result(result: &PublicationResult) -> Value {
    publication_evidence::publication_result(result)
        .unwrap_or_else(|error| json!({"error": error.message}))
}






/// Empty cleanup converts to the golden-corpus empty object; entries
/// are listed under `artifacts` with their complete public facts.
fn cleanup_artifacts(cleanup: &CleanupArtifacts) -> Value {
    if cleanup.is_empty() {
        return json!({});
    }
    let artifacts: Vec<Value> = cleanup.iter().map(cleanup_artifact).collect();
    json!({ "artifacts": artifacts })
}

fn cleanup_artifact(artifact: &CleanupArtifact) -> Value {
    let mut converted = Map::new();
    converted.insert("kind".into(), json!(artifact_kind(artifact.kind)));
    converted.insert(
        "directory_role".into(),
        json!(directory_role(artifact.directory_role)),
    );
    converted.insert(
        "directory_identity".into(),
        file_identity(&artifact.directory_identity),
    );
    converted.insert(
        "basename_encoding".into(),
        json!(artifact.basename_encoding),
    );
    converted.insert("basename".into(), json!(hex(&artifact.basename)));
    if let Some(identity) = artifact.identity.as_ref() {
        converted.insert("identity".into(), file_identity(identity));
    }
    if let Some(security) = artifact.creation_security.as_ref() {
        converted.insert(
            "creation_security".into(),
            json!({
                "kind": security.kind,
                "commitment": hex(&security.commitment),
            }),
        );
    }
    if let Some(tail) = artifact.unpublished_tail.as_ref() {
        converted.insert(
            "unpublished_tail".into(),
            json!({
                "expected_database_id": hex16(&tail.expected_database_id),
                "committed_target_transaction_id": tail.committed_target_transaction_id.to_string(),
                "committed_target_nonce": hex16(&tail.committed_target_nonce),
                "committed_target_length": tail.committed_target_length.to_string(),
                "observed_tail_end_exclusive": tail.observed_tail_end_exclusive.to_string(),
            }),
        );
    }
    converted.insert("error".into(), problem(&artifact.error));
    Value::Object(converted)
}

fn artifact_kind(value: iprange_livedb::publication::ArtifactKind) -> &'static str {
    use iprange_livedb::publication::ArtifactKind as Kind;
    match value {
        Kind::PrivateOutput => "private_output",
        Kind::PrivateReservation => "private_reservation",
        Kind::OwnedCoordination => "owned_coordination",
        Kind::AuthorizedScratch => "authorized_scratch",
        Kind::OwnedMain => "owned_main",
        Kind::UnpublishedMainTail => "unpublished_main_tail",
    }
}

fn directory_role(value: iprange_livedb::publication::DirectoryRole) -> &'static str {
    use iprange_livedb::publication::DirectoryRole as Role;
    match value {
        Role::Destination => "destination",
        Role::ScratchDirectory => "scratch_directory",
        Role::MainFile => "main_file",
    }
}

/// `None` converts to the golden-corpus empty object; every retained
/// coordination obligation reports its factual state.
fn coordination_cleanup(value: CoordinationCleanup) -> Value {
    match value {
        CoordinationCleanup::None => json!({}),
        CoordinationCleanup::CleanupGuard => json!({"state": "cleanup_guard"}),
        CoordinationCleanup::RetainedReaderCloseRequired => {
            json!({"state": "retained_reader_close_required"})
        }
        CoordinationCleanup::RetainedWriterCloseRequired => {
            json!({"state": "retained_writer_close_required"})
        }
    }
}

fn cleanup_state(value: iprange_livedb::publication::CleanupState) -> &'static str {
    match value {
        iprange_livedb::publication::CleanupState::Clean => "clean",
        iprange_livedb::publication::CleanupState::ResiduePossible => "residue_possible",
    }
}

fn private_attempt(attempt: &iprange_livedb::publication::PrivateOutputAttempt) -> Value {
    let mut converted = Map::new();
    converted.insert(
        "publication_attempt_id".into(),
        json!(hex16(&attempt.publication_attempt_id)),
    );
    converted.insert(
        "directory_identity".into(),
        file_identity(&attempt.directory_identity),
    );
    converted.insert("basename_encoding".into(), json!(attempt.basename_encoding));
    converted.insert(
        "basename".into(),
        json!(lifecycle::basename(&attempt.basename, attempt.basename_encoding)),
    );
    if let Some(identity) = attempt.identity.as_ref() {
        converted.insert("identity".into(), file_identity(identity));
    }
    converted.insert(
        "creation_security".into(),
        json!({
            "kind": attempt.creation_security.kind,
            "commitment": hex(&attempt.creation_security.commitment),
        }),
    );
    Value::Object(converted)
}

fn problem(value: &iprange_livedb::publication::PublicationProblem) -> Value {
    json!({
        "code": publication_code(value.code),
        "os_code": value.os_code,
        "detail": value.detail,
    })
}

fn file_identity(identity: &LocalFileIdentity) -> Value {
    let volume = u64::from_le_bytes(identity.bytes[0..8].try_into().unwrap_or_default());
    let file = u64::from_le_bytes(identity.bytes[8..16].try_into().unwrap_or_default());
    json!({"volume": volume.to_string(), "file": file.to_string()})
}

fn hex16(bytes: &[u8; 16]) -> String {
    hex(bytes)
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

/// Canonical SDK error names for the publication failure classes the
/// reader mapping does not list; every other code keeps the shared
/// reader mapping so adapter codes stay consistent.
fn publication_code(code: ErrorCode) -> &'static str {
    match code {
        ErrorCode::PublicationUnsupported => "publication_unsupported",
        ErrorCode::OsUnsupported => "os_unsupported",
        ErrorCode::DurabilityUnsupported => "durability_unsupported",
        ErrorCode::AccessPolicyUnsupported => "access_policy_unsupported",
        ErrorCode::SnapshotPreparationFailed => "snapshot_preparation_failed",
        ErrorCode::LiveCoordinationUnsupported => "live_coordination_unsupported",
        code => sdk_code(code),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use super::super::lifecycle;
    use iprange_livedb::publication::LiveLineage;
    use iprange_livedb::SnapshotOutcome;
    use iprange_livedb::{
        create_live, AddressFamily, CancellationToken, Ipv4Key, LiveWriter, SnapshotBudget,
        StructureKind, TransactionBudget, ValueKind,
    };
    use std::path::Path;
    use std::path::PathBuf;

    struct TempDirectory(PathBuf);

    impl TempDirectory {
        fn new(label: &str) -> Self {
            let unique = format!(
                "iprange-snapshot-{label}-{}-{}",
                std::process::id(),
                std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .unwrap()
                    .as_nanos()
            );
            let path = std::env::temp_dir().join(unique);
            std::fs::create_dir_all(&path).unwrap();
            Self(path)
        }

        fn path(&self, name: &str) -> PathBuf {
            self.0.join(name)
        }
    }

    impl Drop for TempDirectory {
        fn drop(&mut self) {
            let _ = std::fs::remove_file(self.path("live.iprange"));
            let _ = std::fs::remove_file(self.path("live.iprange.readers"));
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }

    /// Build one real immutable v4 file through the public SDK writer
    /// and snapshot workflow (no fixture generation in the binary).
    fn immutable_fixture(directory: &TempDirectory) -> PathBuf {
        let token = CancellationToken::new();
        let live = directory.path("live.iprange");
        create_live(
            &live,
            AddressFamily::Ipv4,
            ValueKind::Direct,
            StructureKind::None,
            iprange_livedb::ValueTag::new(b"direct").unwrap(),
            8,
            &token,
        )
        .unwrap();
        let budget = TransactionBudget {
            max_heap_bytes: 16 * 1024 * 1024,
            max_private_pages: 20_000,
            max_file_growth_pages: 20_000,
            max_open_files: 2,
        };
        let mut writer = LiveWriter::open(&live, budget, &token).unwrap();
        let mut transaction = writer.begin_direct_transaction(&token).unwrap();
        transaction
            .assign_v4(Ipv4Key(0xC000_0200), Ipv4Key(0xC000_020F), 10)
            .unwrap();
        transaction.commit().unwrap();
        writer.close().map(|_| ()).unwrap();
        let snapshot_budget = SnapshotBudget::new(32 * 1024 * 1024, 20_000, 3);
        let output = directory.path("live-snapshot.iprange");
        snapshot_to(
            &live,
            SnapshotSourceMode::Live,
            &output,
            SnapshotPublicationPolicy::FailIfExists,
            &snapshot_budget,
            &token,
        )
        .map_err(|failure| failure.cause.detail.to_string())
        .unwrap();
        let _ = std::fs::remove_file(&live);
        let _ = std::fs::remove_file(directory.path("live.iprange.readers"));
        output
    }

    fn snapshot_result(source: &Path, destination: &Path) -> SnapshotOutcome {
        snapshot_to(
            source,
            SnapshotSourceMode::Immutable,
            destination,
            SnapshotPublicationPolicy::FailIfExists,
            &SnapshotBudget::new(32 * 1024 * 1024, 20_000, 2),
            &CancellationToken::new(),
        )
    }

    #[test]
    fn immutable_publication_converts_to_the_wire_shape() {
        let directory = TempDirectory::new("success");
        let source = immutable_fixture(&directory);
        let destination = directory.path("snapshot.iprange");
        let result = snapshot_result(&source, &destination)
            .map_err(|failure| failure.cause.detail.to_string())
            .unwrap();
        let converted = publication_result(&result.publication);
        assert_eq!(converted["main_namespace_may_have_been_attempted"], json!(true));
        assert_eq!(converted["publication"], json!("published"));
        assert_eq!(converted["destination_content"], json!("desired"));
        assert_eq!(converted["later_canonical"], json!("none"));
        assert_eq!(converted["main_access_policy"], json!("creator_only"));
        assert_eq!(converted["coordination_access_policy"], json!("absent"));
        assert_eq!(converted["cleanup"], json!({}));
        assert_eq!(converted["coordination_cleanup"], json!({}));
        // Windows GC retirement reports that the removed coordination
        // artifacts could reappear after a crash (the removal is not
        // power-loss durable); both SDKs implement the same state.
        assert_eq!(converted["housekeeping"]["artifacts"], json!([]));
        #[cfg(unix)]
        assert_eq!(converted["housekeeping"], json!({"artifacts": []}));
        #[cfg(windows)]
        assert_eq!(
            converted["housekeeping"],
            json!({"state": "crash_reappearance_possible", "artifacts": []})
        );
        assert_eq!(converted["visible_housekeeping"], json!([]));
        let attempt = converted["attempt"]
            .as_object()
            .expect("attempt is the complete SDK attempt object");
        assert_eq!(attempt.len(), 13);
        assert!(attempt.get("previous_destination").is_none());
        assert_eq!(
            attempt["database_id"],
            json!(hex16(&result.publication.attempt.database_id))
        );
        assert_eq!(
            attempt["transaction_id"],
            json!(result.publication.attempt.transaction_id.to_string())
        );
        assert_eq!(
            attempt["output_sha512"].as_str().unwrap().len(),
            128
        );
        assert_eq!(
            attempt["publication_policy"],
            json!("fail_if_exists")
        );
        assert_eq!(
            attempt["destination_basename"],
            json!(lifecycle::encode_base64(&result.publication.attempt.destination_basename))
        );
    }

    #[test]
    fn optional_publication_facts_stay_absent_until_present() {
        let directory = TempDirectory::new("optional");
        let source = immutable_fixture(&directory);
        let destination = directory.path("snapshot.iprange");
        let mut result = snapshot_result(&source, &destination)
            .map_err(|failure| failure.cause.detail.to_string())
            .unwrap();
        result.publication.live_lineage = Some(LiveLineage::AdvancedGeneration);
        result.publication.later_attempt_or_sidecar_id = Some([7; 16]);
        result.publication.later_selected_transaction_id = Some(9);
        result.publication.later_selected_commit_nonce = Some([8; 16]);
        let converted = publication_result(&result.publication);
        assert_eq!(
            converted["live_lineage"],
            json!({"kind": "advanced_generation"})
        );
        assert_eq!(
            converted["later_attempt_or_sidecar_id"],
            json!(hex16(&[7; 16]))
        );
        assert_eq!(converted["later_selected_transaction_id"], json!("9"));
        assert_eq!(
            converted["later_selected_commit_nonce"],
            json!(hex16(&[8; 16]))
        );
    }

    #[test]
    fn occupied_destination_maps_to_a_product_error_with_details() {
        let directory = TempDirectory::new("failure");
        let source = immutable_fixture(&directory);
        let destination = directory.path("snapshot.iprange");
        snapshot_result(&source, &destination)
            .map_err(|failure| failure.cause.detail.to_string())
            .unwrap();
        let failure = match snapshot_result(&source, &destination) {
            Ok(_) => panic!("second publication must fail"),
            Err(failure) => failure,
        };
        let error = publication_failure(&failure);
        assert_eq!(error.code, "name_exists");
        assert_eq!(error.outcome, "not_started");
        let details = error.details.unwrap();
        assert_eq!(details["cleanup_state"], json!("clean"));
        assert!(details["cleanup"].is_object());
        assert!(details["housekeeping"].is_object());
        assert_eq!(details["visible_housekeeping"], json!([]));
    }

    #[test]
    fn snapshot_params_validation_is_strict() {
        let valid = json!({
            "source": {"path": "/tmp/db.iprange", "mode": "immutable"},
            "destination": "/tmp/snap.iprange",
            "publication_policy": "replace_existing",
            "snapshot_budget": {
                "max_heap_bytes": "16777216",
                "max_output_pages": "512",
                "max_open_files": 32
            }
        });
        assert_eq!(validate_snapshot(&valid), Ok(()));
        let mut unknown = valid.clone();
        unknown["unexpected"] = json!(1);
        assert!(validate_snapshot(&unknown).is_err());
        let mut leading_zero = valid.clone();
        leading_zero["snapshot_budget"]["max_output_pages"] = json!("0512");
        assert!(validate_snapshot(&leading_zero).is_err());
        let mut dash = valid.clone();
        dash["destination"] = json!("-");
        assert!(validate_snapshot(&dash).is_err());
    }
}

#[cfg(test)]
mod attempt_wire_tests {
    use super::*;
    use iprange_livedb::publication::PrivateOutputAttempt;
    use iprange_livedb::validation::LocalFileIdentity;

    // The snapshot preparation-failure ``output`` member must render
    // the attempt basename with the same encoding-aware wire form as
    // every other private-output surface (maintenance, publish,
    // algebra, recovery): encoding 2 is the opaque per-byte wire form
    // of the stored UTF-16LE units, not a hex digest (wave-15
    // round-3 parity finding; the pre-fix renderer emitted hex).
    #[test]
    fn private_attempt_basename_uses_the_encoding_aware_wire_form() {
        let attempt = PrivateOutputAttempt {
            publication_attempt_id: [7; 16],
            directory_identity: LocalFileIdentity {
                kind: 2,
                bytes: [0; 32],
            },
            basename_encoding: 2,
            basename: vec![0xe9, 0x00].into_boxed_slice(),
            identity: None,
            creation_security: iprange_livedb::publication::CreationSecurity {
                kind: 2,
                commitment: [0; 32],
            },
        };
        let wire = private_attempt(&attempt);
        assert_eq!(wire["basename_encoding"], json!(2));
        // The per-byte form of the UTF-16LE units E9 00 is the two
        // characters U+00E9 U+0000, never a hex string and never
        // U+FFFD.
        assert_eq!(wire["basename"], json!("\u{e9}\u{0}"));
        // Identical facts must produce the identical wire on the
        // maintenance surface (the shared rendering authority).
        let maintenance_wire = super::super::maintenance::private_output_attempt_value(&attempt);
        assert_eq!(
            wire["basename"],
            maintenance_wire["basename"]
        );
        assert_eq!(
            wire["basename_encoding"],
            maintenance_wire["basename_encoding"]
        );
    }

    // Encoding 1 (posix raw bytes) renders as the raw text, exactly
    // like the maintenance surface.
    #[test]
    fn private_attempt_encoding_one_keeps_the_raw_text() {
        let attempt = PrivateOutputAttempt {
            publication_attempt_id: [8; 16],
            directory_identity: LocalFileIdentity {
                kind: 1,
                bytes: [0; 32],
            },
            basename_encoding: 1,
            basename: b"live.iprange".to_vec().into_boxed_slice(),
            identity: None,
            creation_security: iprange_livedb::publication::CreationSecurity {
                kind: 1,
                commitment: [0; 32],
            },
        };
        let wire = private_attempt(&attempt);
        assert_eq!(wire["basename"], json!("live.iprange"));
    }
}

#[cfg(test)]
mod positive_budget_tests {
    use super::*;

    #[test]
    fn snapshot_budget_rejects_zero_but_accepts_positive_limits() {
        let valid = json!({
            "source": {"path": "/tmp/db.iprange", "mode": "live"},
            "destination": "/tmp/snap.iprange",
            "publication_policy": "replace_existing",
            "snapshot_budget": {
                "max_heap_bytes": "1",
                "max_output_pages": "1",
                "max_open_files": 1
            }
        });
        assert_eq!(validate_snapshot(&valid), Ok(()));
        for field in ["max_heap_bytes", "max_output_pages"] {
            let mut zero = valid.clone();
            zero["snapshot_budget"][field] = json!("0");
            assert!(validate_snapshot(&zero).is_err());
        }
        let mut zero_files = valid.clone();
        zero_files["snapshot_budget"]["max_open_files"] = json!(0);
        assert!(validate_snapshot(&zero_files).is_err());
    }
}
