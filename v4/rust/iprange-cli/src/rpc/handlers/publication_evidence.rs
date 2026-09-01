//! Complete publication evidence in the v1 wire (SOW-0028 decision D3-A).
//!
//! This module is the single authority for the complete `PublicationResult`
//! wire object: `publication_result` encodes it and
//! `decode_publication_result` is its strict inverse for the
//! caller-preserved `publication_result` param of `publication.resolve`.
//! Every handler family (current.publish, snapshot, algebra.publish,
//! recover, inspect) emits through this one encoder, so enum vocabulary
//! (`destination_content` "desired|previous|absent|other|unclassified",
//! `later_canonical` "none|reservation_or_transition|ready_live_sidecar"),
//! coordination cleanup (`{"kind": ...}` / `{}`), and artifact basenames
//! (hex) are identical everywhere. `cause` is never present on the wire
//! (results carry it as an error, not a success field), so the decoder
//! always reconstructs it as `None`.

use iprange_livedb::publication::{
    AccessPolicy, CleanupArtifact, CleanupArtifacts, DestinationContent, LaterCanonical,
    LiveLineage, PreviousDestination, PublicationAttempt, PublicationPolicy, PublicationProblem,
    PublicationResult, PublicationStatus, UnpublishedTailFacts,
};
use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::{convert, lifecycle, lifecycle_live};

/// Mechanical conversion of one SDK publication attempt (the callers of
/// `publication.resolve` preserve this object verbatim).
pub(crate) fn publication_attempt(attempt: &PublicationAttempt) -> Result<Value, HandlerError> {
    let mut value = json!({
        "database_id": convert::hex_id(&attempt.database_id),
        "transaction_id": convert::decimal_u64(attempt.transaction_id),
        "commit_nonce": convert::hex_id(&attempt.commit_nonce),
        "publication_attempt_id": convert::hex_id(&attempt.publication_attempt_id),
        "directory_identity": lifecycle::file_identity(&attempt.directory_identity)?,
        "destination_basename_encoding": attempt.destination_basename_encoding,
        "destination_basename": lifecycle::encode_base64(&attempt.destination_basename),
        "output_identity": lifecycle::file_identity(&attempt.output_identity)?,
        "output_byte_length": convert::decimal_u64(attempt.output_byte_length),
        "output_sha512": hex128(&attempt.output_sha512),
        "publication_policy": publication_policy_name(attempt.publication_policy),
        "reservation_identity": lifecycle::file_identity(&attempt.reservation_identity)?,
        "creation_security": lifecycle::creation_security(&attempt.creation_security),
    });
    if let Some(previous) = &attempt.previous_destination {
        value["previous_destination"] = json!({
            "identity": lifecycle::file_identity(&previous.identity)?,
            "byte_length": convert::decimal_u64(previous.byte_length),
            "sha512": hex128(&previous.sha512),
        });
    }
    Ok(value)
}

pub(crate) fn publication_policy_name(value: PublicationPolicy) -> &'static str {
    match value {
        PublicationPolicy::FailIfExists => "fail_if_exists",
        PublicationPolicy::ReplaceExisting => "replace_existing",
        PublicationPolicy::ReplaceExistingNoRollback => "replace_existing_no_rollback",
    }
}

fn hex128(bytes: &[u8; 64]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

pub(crate) fn publication_status_name(value: PublicationStatus) -> &'static str {
    match value {
        PublicationStatus::NotPublished => "not_published",
        PublicationStatus::Published => "published",
        PublicationStatus::OutcomeUnknown => "outcome_unknown",
    }
}

pub(crate) fn destination_content_name(value: DestinationContent) -> &'static str {
    match value {
        DestinationContent::Desired => "desired",
        DestinationContent::Previous => "previous",
        DestinationContent::Absent => "absent",
        DestinationContent::Other => "other",
        DestinationContent::Unclassified => "unclassified",
    }
}

pub(crate) fn later_canonical_name(value: LaterCanonical) -> &'static str {
    match value {
        LaterCanonical::None => "none",
        LaterCanonical::ReservationOrTransition => "reservation_or_transition",
        LaterCanonical::ReadyLiveSidecar => "ready_live_sidecar",
    }
}

pub(crate) fn access_policy_name(value: AccessPolicy) -> &'static str {
    match value {
        AccessPolicy::CreatorOnly => "creator_only",
        AccessPolicy::ChangedOrUnproven => "changed_or_unproven",
        AccessPolicy::Unclassified => "unclassified",
        AccessPolicy::Absent => "absent",
    }
}

fn live_lineage_name(value: LiveLineage) -> &'static str {
    match value {
        LiveLineage::SameGenerationExactBytes => "same_generation_exact_bytes",
        LiveLineage::SameGenerationPhysicalBytesChanged => {
            "same_generation_physical_bytes_changed"
        }
        LiveLineage::AdvancedGeneration => "advanced_generation",
    }
}

fn publication_cleanup(value: &CleanupArtifacts) -> Value {
    if value.is_empty() {
        return json!({});
    }
    json!({
        "artifacts": value.iter().map(lifecycle::cleanup_artifact).collect::<Vec<_>>(),
    })
}

/// Complete mechanical `PublicationResult` conversion: the single
/// encoder for every publication-producing handler family.
pub(crate) fn publication_result(result: &PublicationResult) -> Result<Value, HandlerError> {
    let mut value = json!({
        "attempt": publication_attempt(&result.attempt)?,
        "main_namespace_may_have_been_attempted": result.main_namespace_may_have_been_attempted,
        "publication": publication_status_name(result.publication),
        "destination_content": destination_content_name(result.destination_content),
        "later_canonical": later_canonical_name(result.later_canonical),
        "main_access_policy": access_policy_name(result.main_access_policy),
        "coordination_access_policy": access_policy_name(result.coordination_access_policy),
        "cleanup": publication_cleanup(&result.cleanup),
        "coordination_cleanup": lifecycle::coordination_cleanup(result.coordination_cleanup),
        "housekeeping": lifecycle::housekeeping(
            result.housekeeping,
            &result.visible_housekeeping,
        ),
        "visible_housekeeping": Value::Array(
            result
                .visible_housekeeping
                .iter()
                .map(lifecycle::housekeeping_artifact)
                .collect(),
        ),
    });
    if let Some(lineage) = result.live_lineage {
        value["live_lineage"] = json!({"kind": live_lineage_name(lineage)});
    }
    if let Some(id) = result.later_attempt_or_sidecar_id {
        value["later_attempt_or_sidecar_id"] = json!(convert::hex_id(&id));
    }
    if let Some(transaction) = result.later_selected_transaction_id {
        value["later_selected_transaction_id"] = json!(convert::decimal_u64(transaction));
    }
    if let Some(nonce) = result.later_selected_commit_nonce {
        value["later_selected_commit_nonce"] = json!(convert::hex_id(&nonce));
    }
    Ok(value)
}

/// Strict inverse of `publication_result` for a preserved wire result.
pub(crate) fn decode_publication_result(value: &Value) -> Result<PublicationResult, String> {
    let result = lifecycle_live::object(value, "publication_result")?;
    lifecycle_live::exact_members(
        result,
        &[
            "attempt",
            "main_namespace_may_have_been_attempted",
            "publication",
            "destination_content",
            "later_canonical",
            "main_access_policy",
            "coordination_access_policy",
            "cleanup",
            "coordination_cleanup",
            "housekeeping",
            "visible_housekeeping",
        ],
        &[
            "live_lineage",
            "later_attempt_or_sidecar_id",
            "later_selected_transaction_id",
            "later_selected_commit_nonce",
        ],
        "publication_result",
    )?;
    Ok(PublicationResult {
        attempt: decode_publication_attempt(&result["attempt"])?,
        main_namespace_may_have_been_attempted: lifecycle_live::boolean(
            &result["main_namespace_may_have_been_attempted"],
            "main_namespace_may_have_been_attempted",
        )?,
        publication: decode_publication_status(&result["publication"])?,
        destination_content: decode_destination_content(&result["destination_content"])?,
        later_canonical: decode_later_canonical(&result["later_canonical"])?,
        live_lineage: decode_live_lineage(result.get("live_lineage"))?,
        later_attempt_or_sidecar_id: match result.get("later_attempt_or_sidecar_id") {
            Some(value) if value.is_null() => {
                return Err(
                    "later_attempt_or_sidecar_id must not be null; absent is the only absent form"
                        .into(),
                );
            }
            Some(value) => Some(lifecycle_live::hex16(
                value,
                "later_attempt_or_sidecar_id",
            )?),
            None => None,
        },
        later_selected_transaction_id: match result.get("later_selected_transaction_id") {
            Some(value) if value.is_null() => {
                return Err(
                    "later_selected_transaction_id must not be null; absent is the only absent form"
                        .into(),
                );
            }
            Some(value) => Some(lifecycle_live::decimal_u64(
                value,
                "later_selected_transaction_id",
            )?),
            None => None,
        },
        later_selected_commit_nonce: match result.get("later_selected_commit_nonce") {
            Some(value) if value.is_null() => {
                return Err(
                    "later_selected_commit_nonce must not be null; absent is the only absent form"
                        .into(),
                );
            }
            Some(value) => Some(lifecycle_live::hex16(
                value,
                "later_selected_commit_nonce",
            )?),
            None => None,
        },
        main_access_policy: decode_access_policy(&result["main_access_policy"])?,
        coordination_access_policy: decode_access_policy(&result["coordination_access_policy"])?,
        cleanup: decode_publication_cleanup(&result["cleanup"])?,
        coordination_cleanup: lifecycle_live::coordination_cleanup(
            &result["coordination_cleanup"],
            "coordination_cleanup",
        )?,
        housekeeping: lifecycle_live::decode_housekeeping(&result["housekeeping"], "housekeeping")?,
        visible_housekeeping: lifecycle_live::decode_housekeeping_artifacts(
            &result["visible_housekeeping"],
        )?,
        cause: None,
    })
}

fn decode_publication_attempt(value: &Value) -> Result<PublicationAttempt, String> {
    let attempt = lifecycle_live::object(value, "attempt")?;
    lifecycle_live::exact_members(
        attempt,
        &[
            "database_id",
            "transaction_id",
            "commit_nonce",
            "publication_attempt_id",
            "directory_identity",
            "destination_basename_encoding",
            "destination_basename",
            "output_identity",
            "output_byte_length",
            "output_sha512",
            "publication_policy",
            "reservation_identity",
            "creation_security",
        ],
        &["previous_destination"],
        "attempt",
    )?;
    Ok(PublicationAttempt {
        database_id: lifecycle_live::hex16(&attempt["database_id"], "database_id")?,
        transaction_id: lifecycle_live::decimal_u64(
            &attempt["transaction_id"],
            "transaction_id",
        )?,
        commit_nonce: lifecycle_live::hex16(&attempt["commit_nonce"], "commit_nonce")?,
        publication_attempt_id: lifecycle_live::hex16(
            &attempt["publication_attempt_id"],
            "publication_attempt_id",
        )?,
        directory_identity: lifecycle_live::decode_file_identity(
            &attempt["directory_identity"],
            "directory_identity",
        )?,
        destination_basename_encoding: lifecycle_live::u16_encoding(
            &attempt["destination_basename_encoding"],
        )?,
        destination_basename: lifecycle::decode_base64(lifecycle_live::string(
            &attempt["destination_basename"],
            "destination_basename",
        )?)
        .map_err(|error| format!("destination_basename: {error}"))?
        .into_boxed_slice(),
        output_identity: lifecycle_live::decode_file_identity(
            &attempt["output_identity"],
            "output_identity",
        )?,
        output_byte_length: lifecycle_live::decimal_u64(
            &attempt["output_byte_length"],
            "output_byte_length",
        )?,
        output_sha512: hex64(&attempt["output_sha512"], "output_sha512")?,
        publication_policy: decode_publication_policy(&attempt["publication_policy"])?,
        previous_destination: decode_previous_destination(attempt.get("previous_destination"))?,
        reservation_identity: lifecycle_live::decode_file_identity(
            &attempt["reservation_identity"],
            "reservation_identity",
        )?,
        creation_security: lifecycle_live::decode_creation_security(
            &attempt["creation_security"],
        )?,
    })
}

fn hex64(value: &Value, field: &str) -> Result<[u8; 64], String> {
    let text = value
        .as_str()
        .ok_or_else(|| format!("{field} must be a string"))?;
    let bytes = lifecycle_live::decode_hex(text, 64)
        .ok_or_else(|| format!("{field} must be 128 lowercase hexadecimal characters"))?;
    bytes
        .try_into()
        .map_err(|_| format!("{field} must be 128 lowercase hexadecimal characters"))
}

fn decode_publication_policy(value: &Value) -> Result<PublicationPolicy, String> {
    match lifecycle_live::string(value, "publication_policy")? {
        "fail_if_exists" => Ok(PublicationPolicy::FailIfExists),
        "replace_existing" => Ok(PublicationPolicy::ReplaceExisting),
        "replace_existing_no_rollback" => Ok(PublicationPolicy::ReplaceExistingNoRollback),
        _ => Err(
            "publication_policy must be fail_if_exists, replace_existing, or replace_existing_no_rollback"
                .into(),
        ),
    }
}

fn decode_previous_destination(
    value: Option<&Value>,
) -> Result<Option<PreviousDestination>, String> {
    match value {
        Some(value) if value.is_null() => {
            return Err("previous_destination must not be null; absent is the only absent form".into());
        }
        Some(value) => {
            let previous = lifecycle_live::object(value, "previous_destination")?;
            lifecycle_live::exact_members(
                previous,
                &["identity", "byte_length", "sha512"],
                &[],
                "previous_destination",
            )?;
            Ok(Some(PreviousDestination {
                identity: lifecycle_live::decode_file_identity(
                    &previous["identity"],
                    "previous_destination.identity",
                )?,
                byte_length: lifecycle_live::decimal_u64(
                    &previous["byte_length"],
                    "previous_destination.byte_length",
                )?,
                sha512: hex64(&previous["sha512"], "previous_destination.sha512")?,
            }))
        }
        _ => Ok(None),
    }
}

fn decode_publication_status(value: &Value) -> Result<PublicationStatus, String> {
    match lifecycle_live::string(value, "publication")? {
        "not_published" => Ok(PublicationStatus::NotPublished),
        "published" => Ok(PublicationStatus::Published),
        "outcome_unknown" => Ok(PublicationStatus::OutcomeUnknown),
        _ => Err("publication must be not_published, published, or outcome_unknown".into()),
    }
}

fn decode_destination_content(value: &Value) -> Result<DestinationContent, String> {
    match lifecycle_live::string(value, "destination_content")? {
        "desired" => Ok(DestinationContent::Desired),
        "previous" => Ok(DestinationContent::Previous),
        "absent" => Ok(DestinationContent::Absent),
        "other" => Ok(DestinationContent::Other),
        "unclassified" => Ok(DestinationContent::Unclassified),
        _ => Err(
            "destination_content must be desired, previous, absent, other, or unclassified"
                .into(),
        ),
    }
}

fn decode_later_canonical(value: &Value) -> Result<LaterCanonical, String> {
    match lifecycle_live::string(value, "later_canonical")? {
        "none" => Ok(LaterCanonical::None),
        "reservation_or_transition" => Ok(LaterCanonical::ReservationOrTransition),
        "ready_live_sidecar" => Ok(LaterCanonical::ReadyLiveSidecar),
        _ => Err("later_canonical must be none, reservation_or_transition, or ready_live_sidecar".into()),
    }
}

fn decode_live_lineage(value: Option<&Value>) -> Result<Option<LiveLineage>, String> {
    match value {
        Some(value) if value.is_null() => {
            return Err("live_lineage must not be null; absent is the only absent form".into());
        }
        Some(value) => {
            let lineage = lifecycle_live::object(value, "live_lineage")?;
            lifecycle_live::exact_members(lineage, &["kind"], &[], "live_lineage")?;
            let kind = match lifecycle_live::string(&lineage["kind"], "live_lineage.kind")? {
                "same_generation_exact_bytes" => LiveLineage::SameGenerationExactBytes,
                "same_generation_physical_bytes_changed" => {
                    LiveLineage::SameGenerationPhysicalBytesChanged
                }
                "advanced_generation" => LiveLineage::AdvancedGeneration,
                _ => {
                    return Err(
                        "live_lineage.kind must be same_generation_exact_bytes, same_generation_physical_bytes_changed, or advanced_generation"
                            .into(),
                    )
                }
            };
            Ok(Some(kind))
        }
        _ => Ok(None),
    }
}

fn decode_access_policy(value: &Value) -> Result<AccessPolicy, String> {
    match lifecycle_live::string(value, "access policy")? {
        "absent" => Ok(AccessPolicy::Absent),
        "creator_only" => Ok(AccessPolicy::CreatorOnly),
        "changed_or_unproven" => Ok(AccessPolicy::ChangedOrUnproven),
        "unclassified" => Ok(AccessPolicy::Unclassified),
        _ => Err(
            "access policy must be absent, creator_only, changed_or_unproven, or unclassified"
                .into(),
        ),
    }
}

/// `{}` (no artifacts) and `{"artifacts": [...]}` both round-trip; the
/// ledger capacity is enforced by the SDK constructor.
fn decode_publication_cleanup(value: &Value) -> Result<CleanupArtifacts, String> {
    let cleanup = lifecycle_live::object(value, "cleanup")?;
    lifecycle_live::exact_members(cleanup, &[], &["artifacts"], "cleanup")?;
    let entries = match cleanup.get("artifacts") {
        None => Vec::new(),
        Some(array) => array
            .as_array()
            .ok_or_else(|| "cleanup.artifacts must be an array".to_string())?
            .iter()
            .map(decode_cleanup_artifact)
            .collect::<Result<Vec<_>, _>>()?,
    };
    CleanupArtifacts::from_entries(entries)
        .ok_or_else(|| "cleanup.artifacts exceeds the fixed ledger capacity".to_string())
}

pub(crate) fn decode_cleanup_artifact(value: &Value) -> Result<CleanupArtifact, String> {
    let artifact = lifecycle_live::object(value, "cleanup artifact")?;
    lifecycle_live::exact_members(
        artifact,
        &[
            "kind",
            "directory_role",
            "directory_identity",
            "basename_encoding",
            "error",
        ],
        &["identity", "creation_security", "unpublished_tail", "basename"],
        "cleanup artifact",
    )?;
    Ok(CleanupArtifact {
        kind: lifecycle_live::artifact_kind(&artifact["kind"])?,
        directory_role: lifecycle_live::directory_role(&artifact["directory_role"])?,
        directory_identity: lifecycle_live::decode_file_identity(
            &artifact["directory_identity"],
            "directory_identity",
        )?,
        basename_encoding: lifecycle_live::u16_encoding(&artifact["basename_encoding"])?,
        // The encoder carries the cleanup basename as lowercase hex; the
        // reconstructed ledger keeps the exact bytes.
        basename: match artifact.get("basename") {
            Some(value) => decode_hex_bytes(value, "basename")?,
            None => Box::new([]),
        },
        identity: lifecycle_live::optional_file_identity(artifact.get("identity"), "identity")?,
        creation_security: match artifact.get("creation_security") {
            Some(security) => Some(lifecycle_live::decode_creation_security(security)?),
            _ => None,
        },
        unpublished_tail: decode_unpublished_tail(artifact.get("unpublished_tail"))?,
        error: decode_publication_problem(&artifact["error"])?,
    })
}

fn decode_unpublished_tail(value: Option<&Value>) -> Result<Option<UnpublishedTailFacts>, String> {
    match value {
        Some(value) if value.is_null() => {
            return Err("unpublished_tail must not be null; absent is the only absent form".into());
        }
        Some(value) => {
            let tail = lifecycle_live::object(value, "unpublished_tail")?;
            lifecycle_live::exact_members(
                tail,
                &[
                    "expected_database_id",
                    "committed_target_transaction_id",
                    "committed_target_nonce",
                    "committed_target_length",
                    "observed_tail_end_exclusive",
                ],
                &[],
                "unpublished_tail",
            )?;
            Ok(Some(UnpublishedTailFacts {
                expected_database_id: lifecycle_live::hex16(
                    &tail["expected_database_id"],
                    "expected_database_id",
                )?,
                committed_target_transaction_id: lifecycle_live::decimal_u64(
                    &tail["committed_target_transaction_id"],
                    "committed_target_transaction_id",
                )?,
                committed_target_nonce: lifecycle_live::hex16(
                    &tail["committed_target_nonce"],
                    "committed_target_nonce",
                )?,
                committed_target_length: lifecycle_live::decimal_u64(
                    &tail["committed_target_length"],
                    "committed_target_length",
                )?,
                observed_tail_end_exclusive: lifecycle_live::decimal_u64(
                    &tail["observed_tail_end_exclusive"],
                    "observed_tail_end_exclusive",
                )?,
            }))
        }
        _ => Ok(None),
    }
}

fn decode_hex_bytes(value: &Value, field: &str) -> Result<Box<[u8]>, String> {
    let text = lifecycle_live::string(value, field)?;
    let valid = text.len() % 2 == 0
        && text
            .bytes()
            .all(|byte| byte.is_ascii_hexdigit() && !byte.is_ascii_uppercase());
    if !valid {
        return Err(format!("{field} must be even lowercase hex"));
    }
    let bytes = text
        .as_bytes()
        .chunks_exact(2)
        .map(|pair| {
            let high = (pair[0] as char).to_digit(16).expect("validated hex");
            let low = (pair[1] as char).to_digit(16).expect("validated hex");
            (high * 16 + low) as u8
        })
        .collect::<Vec<_>>();
    Ok(bytes.into_boxed_slice())
}

fn decode_publication_problem(value: &Value) -> Result<PublicationProblem, String> {
    let problem = lifecycle_live::object(value, "error")?;
    lifecycle_live::exact_members(problem, &["code", "detail"], &["os_code"], "error")?;
    let code = lifecycle_live::error_code_from_wire(lifecycle_live::string(
        &problem["code"],
        "error.code",
    )?)
    .ok_or_else(|| "error.code is not a canonical SDK error name".to_string())?;
    let os_code = match problem.get("os_code") {
        Some(value) if value.is_null() => {
            return Err("error.os_code must not be null; absent is the only absent form".into());
        }
        Some(value) => Some(
            value
                .as_i64()
                .and_then(|parsed| i32::try_from(parsed).ok())
                .ok_or_else(|| "error.os_code must be a signed 32-bit integer".to_string())?,
        ),
        None => None,
    };
    Ok(PublicationProblem::owned(
        code,
        os_code,
        lifecycle_live::string(&problem["detail"], "error.detail")?.to_owned(),
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn identity(volume: u64, file: u64) -> iprange_livedb::validation::LocalFileIdentity {
        let mut bytes = [0u8; 32];
        bytes[0..8].copy_from_slice(&volume.to_le_bytes());
        bytes[8..16].copy_from_slice(&file.to_le_bytes());
        iprange_livedb::validation::LocalFileIdentity { kind: 1, bytes }
    }

    fn sample_attempt() -> PublicationAttempt {
        PublicationAttempt {
            database_id: [1; 16],
            transaction_id: 7,
            commit_nonce: [2; 16],
            publication_attempt_id: [3; 16],
            directory_identity: identity(1, 42),
            destination_basename_encoding: 1,
            destination_basename: b"current.iprange".to_vec().into_boxed_slice(),
            output_identity: identity(1, 43),
            output_byte_length: 1234,
            output_sha512: [4; 64],
            publication_policy: PublicationPolicy::FailIfExists,
            previous_destination: None,
            reservation_identity: identity(1, 44),
            creation_security: iprange_livedb::publication::CreationSecurity {
                kind: 1,
                commitment: [5; 32],
            },
        }
    }

    #[test]
    fn attempt_round_trips_through_the_wire_decoder() {
        let attempt = sample_attempt();
        let wire = publication_attempt(&attempt).unwrap();
        assert_eq!(wire["database_id"], json!(convert::hex_id(&[1; 16])));
        assert_eq!(wire["transaction_id"], json!("7"));
        assert_eq!(wire["destination_basename"], json!("Y3VycmVudC5pcHJhbmdl"));
        assert_eq!(wire["publication_policy"], json!("fail_if_exists"));
        assert_eq!(wire["output_sha512"].as_str().unwrap().len(), 128);
        assert!(wire.get("previous_destination").is_none());
        let decoded = decode_publication_attempt(&wire).unwrap();
        assert_eq!(decoded, attempt);
    }

    #[test]
    fn attempt_with_previous_destination_round_trips() {
        let mut attempt = sample_attempt();
        attempt.publication_policy = PublicationPolicy::ReplaceExisting;
        attempt.previous_destination = Some(PreviousDestination {
            identity: identity(1, 41),
            byte_length: 99,
            sha512: [6; 64],
        });
        let wire = publication_attempt(&attempt).unwrap();
        assert_eq!(wire["publication_policy"], json!("replace_existing"));
        let previous = wire["previous_destination"].as_object().unwrap();
        assert_eq!(previous["byte_length"], json!("99"));
        let decoded = decode_publication_attempt(&wire).unwrap();
        assert_eq!(decoded, attempt);
    }

    #[test]
    fn decode_rejects_unknown_or_missing_members() {
        let attempt = sample_attempt();
        let wire = publication_attempt(&attempt).unwrap();
        let mut unknown = wire.clone();
        unknown["unexpected"] = json!(1);
        assert!(decode_publication_attempt(&unknown).is_err());
        let mut truncated = wire.clone();
        truncated["transaction_id"] = json!("01");
        assert!(decode_publication_attempt(&truncated).is_err());
        let mut wrong_case = wire.clone();
        wrong_case["database_id"] = json!("ABCDEF0123456789abcdef0123456789");
        assert!(decode_publication_attempt(&wrong_case).is_err());
    }

    #[test]
    fn full_result_decode_is_strict_about_members_and_absent_cause() {
        let attempt = sample_attempt();
        let mut result = json!({
            "attempt": publication_attempt(&attempt).unwrap(),
            "main_namespace_may_have_been_attempted": true,
            "publication": "published",
            "destination_content": "desired",
            "later_canonical": "none",
            "main_access_policy": "creator_only",
            "coordination_access_policy": "absent",
            "cleanup": {},
            "coordination_cleanup": {},
            "housekeeping": {"artifacts": []},
            "visible_housekeeping": [],
        });
        let decoded = decode_publication_result(&result).unwrap();
        assert_eq!(decoded.attempt, attempt);
        assert!(decoded.cause.is_none());
        assert!(matches!(decoded.publication, PublicationStatus::Published));

        let mut extra = result.clone();
        extra["cause"] = json!({"code": "io", "detail": "x"});
        assert!(decode_publication_result(&extra).is_err());

        result["later_attempt_or_sidecar_id"] = json!(convert::hex_id(&[9; 16]));
        result["later_selected_transaction_id"] = json!("11");
        result["later_selected_commit_nonce"] = json!(convert::hex_id(&[10; 16]));
        result["live_lineage"] = json!({"kind": "advanced_generation"});
        let decoded = decode_publication_result(&result).unwrap();
        assert_eq!(decoded.later_attempt_or_sidecar_id, Some([9; 16]));
        assert_eq!(decoded.later_selected_transaction_id, Some(11));
        assert_eq!(decoded.later_selected_commit_nonce, Some([10; 16]));
        assert!(matches!(
            decoded.live_lineage,
            Some(LiveLineage::AdvancedGeneration)
        ));
    }

    #[test]
    fn decode_rejects_null_optionals() {
        // Optional evidence members are absent, never null
        // (iprange-jsonrpc-v1.md, optional fields rule).
        let attempt = sample_attempt();
        let result = json!({
            "attempt": publication_attempt(&attempt).unwrap(),
            "main_namespace_may_have_been_attempted": true,
            "publication": "published",
            "destination_content": "desired",
            "later_canonical": "none",
            "main_access_policy": "creator_only",
            "coordination_access_policy": "absent",
            "cleanup": {},
            "coordination_cleanup": {},
            "housekeeping": {"artifacts": []},
            "visible_housekeeping": [],
        });
        let mut null_lineage = result.clone();
        null_lineage["live_lineage"] = serde_json::Value::Null;
        assert!(decode_publication_result(&null_lineage).is_err());

        let mut null_later = result.clone();
        null_later["later_attempt_or_sidecar_id"] = serde_json::Value::Null;
        assert!(decode_publication_result(&null_later).is_err());

        let mut null_transaction = result.clone();
        null_transaction["later_selected_transaction_id"] = serde_json::Value::Null;
        assert!(decode_publication_result(&null_transaction).is_err());

        let mut null_nonce = result.clone();
        null_nonce["later_selected_commit_nonce"] = serde_json::Value::Null;
        assert!(decode_publication_result(&null_nonce).is_err());
    }
}
