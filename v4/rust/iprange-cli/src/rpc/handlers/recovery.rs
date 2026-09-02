//! Validation, recovery-candidate inspection, and recovery handlers
//! (SOW-0028 family: snapshot / validation / recovery).
//!
//! All three methods run the version-matched SDK fault worker
//! (`iprange-v4-worker` next to the `iprange` binary); none of them
//! scans a database in the CLI process. Findings and unknown envelopes
//! stream into the caller-selected JSONL file under the row/byte
//! budget; the export writer publishes atomically only on success.

use std::io;
use std::path::Path;

use iprange_livedb::error::Error;
use iprange_livedb::recovery::{
    inspect_recovery_candidates, recover_immutable, recover_live, recover_offline,
    OfflineQuiescenceCertification, RecoveryCandidate, RecoveryCandidateLabel,
    RecoveryInspectionMode, RecoveryLogicalCounts, RecoveryPageCounts,
    RecoveryPreparationFailure, RecoveryReport, RecoveryResult, RecoveryScratchAttempt,
    RecoverySink, RecoverySinkControl, RecoveryUnknownEnvelope,
};
use iprange_livedb::validation::{
    LocalFileIdentity, ValidationAddressFence, ValidationBudget, ValidationFailure,
    ValidationFinding, ValidationMode, ValidationObject, ValidationProgress, ValidationReason,
    ValidationResult, ValidationSink, ValidationSinkControl,
};
use iprange_livedb::{AddressFamily, Cardinality129, Ipv6Key, StructureKind, ValueKind};
use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;
use super::{convert, lifecycle, maintenance, publish, reader};
use crate::io::export_writer::ExportWriter;

// ---------------------------------------------------------------------------
// iprange.v1.validate
// ---------------------------------------------------------------------------

pub fn validate_validate(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["path", "mode", "validation_budget", "findings_output"])?;
    reader::validate_path(object["path"].as_str())?;
    let mode = object["mode"].as_object().ok_or("mode must be an object")?;
    match mode.get("kind").and_then(Value::as_str) {
        Some("immutable_current") => maintenance::exact_fields(mode, &["kind"])?,
        Some("live_current") => maintenance::exact_fields(mode, &["kind"])?,
        Some("offline_candidate") => {
            maintenance::exact_fields(mode, &["kind", "candidate"])?;
            mode["candidate"]
                .as_object()
                .ok_or("candidate must be an object")?;
            candidate_from_value(&mode["candidate"])?;
        }
        _ => {
            return Err(
                "mode.kind must be immutable_current, live_current, or offline_candidate".into(),
            )
        }
    }
    validate_validation_budget(&object["validation_budget"])?;
    maintenance::validate_output_descriptor(&object["findings_output"])
}

pub fn validate(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let path = object["path"].as_str().expect("validator checked path");
    require_existing_path(path)?;
    let mode_value = object["mode"].as_object().expect("validator checked mode");
    let mode = match mode_value["kind"]
        .as_str()
        .expect("validator checked mode.kind")
    {
        "immutable_current" => ValidationMode::ImmutableCurrent,
        "live_current" => ValidationMode::LiveCurrent,
        _ => ValidationMode::OfflineCandidate(
            candidate_from_value(&mode_value["candidate"])
                .map_err(HandlerError::invalid_params)?,
        ),
    };
    let budget = validation_budget(&object["validation_budget"])?;
    let (output_path, policy, result_budget) =
        maintenance::output_descriptor(&object["findings_output"])?;
    let mut writer = ExportWriter::create(Path::new(&output_path), policy, &result_budget)?;
    match iprange_livedb::validation::validate(
        path,
        mode,
        &budget,
        &state.token(),
        &mut FindingSink { writer: &mut writer },
    ) {
        Ok(result) => {
            let facts = writer.finish()?;
            bounded(json!({
                "method": "iprange.v1.validate",
                "result": validation_result_value(&result),
                "findings": maintenance::output_facts(facts)?,
            }))
        }
        Err(failure) => Err(validation_failure_error(&failure)),
    }
}

struct FindingSink<'a> {
    writer: &'a mut ExportWriter,
}

impl ValidationSink for FindingSink<'_> {
    fn finding(&mut self, finding: &ValidationFinding) -> iprange_livedb::error::Result<ValidationSinkControl> {
        let row = validation_finding_value(finding);
        self.writer.write_line(&row.to_string(), 0).map_err(export_error)?;
        Ok(ValidationSinkControl::Continue)
    }
}

fn validation_failure_error(failure: &ValidationFailure) -> HandlerError {
    let code = match &failure.cause {
        Error::BudgetExceeded(_) => "output_limit",
        Error::SinkFailed(inner) if matches!(&**inner, Error::BudgetExceeded(_)) => "output_limit",
        cause => reader::sdk_code(cause.code()),
    };
    HandlerError {
        code,
        outcome: "read_only_failure",
        message: format!("validation failed: {}", failure.cause),
        details: Some(json!({
            "progress": progress_value(&failure.progress),
            "cleanup": maintenance::cleanup_artifacts(&failure.cleanup),
            "coordination_cleanup": lifecycle::coordination_cleanup(failure.coordination_cleanup),
        })),
    }
}

// ---------------------------------------------------------------------------
// iprange.v1.recovery.inspect
// ---------------------------------------------------------------------------

pub fn validate_recovery_inspect(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["path", "mode", "validation_budget"])?;
    reader::validate_path(object["path"].as_str())?;
    match object["mode"].as_str() {
        Some("immutable") | Some("live") | Some("caller_certified_offline") => {}
        _ => {
            return Err(
                "mode must be immutable, live, or caller_certified_offline".into(),
            )
        }
    }
    validate_validation_budget(&object["validation_budget"])
}

pub fn recovery_inspect(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let path = object["path"].as_str().expect("validator checked path");
    require_existing_path(path)?;
    let mode = match object["mode"].as_str().expect("validator checked mode") {
        "immutable" => RecoveryInspectionMode::Immutable,
        "live" => RecoveryInspectionMode::Live,
        _ => RecoveryInspectionMode::Offline,
    };
    let budget = validation_budget(&object["validation_budget"])?;
    let inspection = inspect_recovery_candidates(path, mode, &budget, &state.token())
        .map_err(reader::read_error)?;
    let mut candidates = Vec::new();
    for candidate in inspection.candidates() {
        candidates.push(candidate_value(candidate)?);
    }
    bounded(json!({
        "method": "iprange.v1.recovery.inspect",
        "source_identity": lifecycle::file_identity(&inspection.source_identity)?,
        "progress": progress_value(&inspection.progress),
        "candidates": candidates,
    }))
}

// ---------------------------------------------------------------------------
// iprange.v1.recover
// ---------------------------------------------------------------------------

pub fn validate_recover(params: &Value) -> Result<(), String> {
    let object = exact_object(
        params,
        &[
            "source_path",
            "source_mode",
            "candidate",
            "destination",
            "recovery_budget",
            "report_output",
        ],
    )?;
    reader::validate_path(object["source_path"].as_str())?;
    match object["source_mode"].as_str() {
        Some("immutable") | Some("live") | Some("caller_certified_offline") => {}
        _ => {
            return Err(
                "source_mode must be immutable, live, or caller_certified_offline".into(),
            )
        }
    }
    object["candidate"]
        .as_object()
        .ok_or("candidate must be an object")?;
    candidate_from_value(&object["candidate"])?;
    reader::validate_path(object["destination"].as_str())?;
    validate_recovery_budget(&object["recovery_budget"])?;
    maintenance::validate_output_descriptor(&object["report_output"])
}

pub fn recover(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let source_path = object["source_path"].as_str().expect("validator checked path");
    require_existing_path(source_path)?;
    let source_mode = object["source_mode"].as_str().expect("validator checked mode");
    let candidate = candidate_from_value(&object["candidate"]).map_err(HandlerError::invalid_params)?;
    let destination = object["destination"].as_str().expect("validator checked path");
    let budget = recovery_budget(&object["recovery_budget"])?;
    let (report_path, policy, result_budget) =
        maintenance::output_descriptor(&object["report_output"])?;
    let mut writer = ExportWriter::create(Path::new(&report_path), policy, &result_budget)?;
    let mut sink = EnvelopeSink { writer: &mut writer };
    let outcome = match source_mode {
        "immutable" => recover_immutable(
            source_path,
            candidate,
            destination,
            &budget,
            &mut sink,
            &state.token(),
        ),
        "live" => recover_live(
            source_path,
            candidate,
            destination,
            &budget,
            &mut sink,
            &state.token(),
        ),
        _ => recover_offline(
            source_path,
            candidate,
            destination,
            OfflineQuiescenceCertification::CallerCertified,
            &budget,
            &mut sink,
            &state.token(),
        ),
    };
    match outcome {
        Ok(result) => recover_success(result, writer),
        Err(failure) => Err(recovery_failure_error(&failure)),
    }
}

fn recover_success(
    result: RecoveryResult,
    writer: ExportWriter,
) -> Result<Value, HandlerError> {
    // The report file is published before the publication-cause check so a
    // damaged publication still reports the completed report facts.
    let facts = writer.finish()?;
    if let Some(cause) = result.publication.cause.as_ref() {
        let outcome = match result.publication.publication {
            iprange_livedb::publication::PublicationStatus::Published => "published",
            iprange_livedb::publication::PublicationStatus::OutcomeUnknown => "outcome_unknown",
            iprange_livedb::publication::PublicationStatus::NotPublished => "not_published",
        };
        return Err(HandlerError {
            code: reader::sdk_code(cause.code),
            outcome,
            message: format!("recovery publication failed: {}", cause.detail),
            details: Some(json!({
                "report": recovery_report_value(&result.report),
                "scratch": result.scratch.as_ref().map(recovery_scratch_value),
                "publication": publish::publication_result(&result.publication)?,
                "report_output": maintenance::output_facts(facts)?,
            })),
        });
    }
    let mut value = json!({
        "method": "iprange.v1.recover",
        "report": recovery_report_value(&result.report),
        "publication": publish::publication_result(&result.publication)?,
    });
    if let Some(scratch) = &result.scratch {
        value["scratch"] = recovery_scratch_value(scratch);
    }
    bounded(value)
}

struct EnvelopeSink<'a> {
    writer: &'a mut ExportWriter,
}

impl RecoverySink for EnvelopeSink<'_> {
    fn unknown(&mut self, envelope: &RecoveryUnknownEnvelope) -> iprange_livedb::error::Result<RecoverySinkControl> {
        let row = unknown_envelope_value(envelope);
        self.writer.write_line(&row.to_string(), 0).map_err(export_error)?;
        Ok(RecoverySinkControl::Continue)
    }
}

fn recovery_failure_error(failure: &RecoveryPreparationFailure) -> HandlerError {
    let outcome = if failure.output.is_some() || !failure.cleanup.is_empty() {
        "not_published"
    } else {
        "not_started"
    };
    HandlerError {
        code: reader::sdk_code(failure.cause.code),
        outcome,
        message: format!("recovery preparation failed: {}", failure.cause.detail),
        details: Some(json!({
            "report": recovery_report_value(&failure.report),
            "scratch": failure.scratch.as_ref().map(recovery_scratch_value),
            "output": failure.output.as_ref().map(maintenance::private_output_attempt_value),
            "cleanup": maintenance::cleanup_artifacts(&failure.cleanup),
            "coordination_cleanup": lifecycle::coordination_cleanup(failure.coordination_cleanup),
            "housekeeping": lifecycle::housekeeping(failure.housekeeping.clone(), &failure.visible_housekeeping),
            "visible_housekeeping": Value::Array(
                failure.visible_housekeeping.iter().map(lifecycle::housekeeping_artifact).collect(),
            ),
        })),
    }
}

// ---------------------------------------------------------------------------
// Mechanical conversions
// ---------------------------------------------------------------------------

fn validation_result_value(result: &ValidationResult) -> Value {
    let mut value = json!({
        "valid": result.valid,
        "file_identity": file_identity_ok(&result.file_identity),
        "progress": progress_value(&result.progress),
    });
    if let Some(generation) = &result.generation {
        // Page roots stay private (iprange-jsonrpc-v1.md: the API never
        // exposes roots or allocator state).
        value["generation"] = json!({
            "address_family": address_family(generation.address_family),
            "value_kind": value_kind(generation.value_kind),
            "structure_kind": structure_kind(generation.structure_kind),
            "value_tag": convert::value_tag(generation.value_tag.bytes()),
            "database_id": convert::hex_id(&generation.database_id),
            "transaction_id": convert::decimal_u64(generation.transaction_id),
            "commit_nonce": convert::hex_id(&generation.commit_nonce),
            "page_count": convert::decimal_u64(generation.page_count),
        });
    }
    value
}

fn progress_value(progress: &ValidationProgress) -> Value {
    json!({
        "checked_unique_pages": convert::decimal_u64(progress.checked_unique_pages),
        "finding_count": convert::decimal_u64(progress.finding_count),
        "untraversable_subgraphs": convert::decimal_u64(progress.untraversable_subgraphs),
        "bounded_possible_span_addresses": cardinality_decimal(progress.bounded_possible_span_addresses),
        "has_unbounded_unknown": progress.has_unbounded_unknown,
    })
}

fn candidate_value(candidate: &RecoveryCandidate) -> Result<Value, HandlerError> {
    Ok(json!({
        "label": candidate_label_name(candidate.label()),
        "meta_page": candidate.meta_page,
        "source_identity": lifecycle::file_identity(&candidate.source_identity())?,
        "database_id": convert::hex_id(&candidate.database_id()),
        "transaction_id": convert::decimal_u64(candidate.transaction_id()),
        "commit_nonce": convert::hex_id(&candidate.commit_nonce()),
    }))
}

fn candidate_from_value(value: &Value) -> Result<RecoveryCandidate, String> {
    let object = value.as_object().ok_or("candidate must be an object")?;
    maintenance::exact_fields(
        object,
        &[
            "label",
            "meta_page",
            "source_identity",
            "database_id",
            "transaction_id",
            "commit_nonce",
        ],
    )?;
    let label = match object["label"].as_str() {
        Some("newest") => RecoveryCandidateLabel::Newest,
        Some("previous") => RecoveryCandidateLabel::Previous,
        Some("unordered_meta_0") => RecoveryCandidateLabel::UnorderedMeta0,
        Some("unordered_meta_1") => RecoveryCandidateLabel::UnorderedMeta1,
        _ => return Err("candidate.label is invalid".into()),
    };
    let meta_page = object["meta_page"]
        .as_u64()
        .and_then(|value| u8::try_from(value).ok())
        .filter(|page| *page <= 1)
        .ok_or("candidate.meta_page must be 0 or 1")?;
    let source_identity = maintenance::identity_from_value(&object["source_identity"])?;
    let database_id = maintenance::hex16_from_value(&object["database_id"])?;
    let transaction_id = maintenance::decimal_u64_value(&object["transaction_id"])?;
    let commit_nonce = maintenance::hex16_from_value(&object["commit_nonce"])?;
    Ok(RecoveryCandidate {
        label,
        meta_page,
        source_identity,
        database_id,
        transaction_id,
        commit_nonce,
    })
}

fn candidate_label_name(value: RecoveryCandidateLabel) -> &'static str {
    match value {
        RecoveryCandidateLabel::Newest => "newest",
        RecoveryCandidateLabel::Previous => "previous",
        RecoveryCandidateLabel::UnorderedMeta0 => "unordered_meta_0",
        RecoveryCandidateLabel::UnorderedMeta1 => "unordered_meta_1",
    }
}

fn validation_finding_value(finding: &ValidationFinding) -> Value {
    let mut value = json!({
        "sequence": convert::decimal_u64(finding.sequence),
        "reason": reason_name(finding.reason),
        "object": object_name(finding.object),
    });
    if let Some(page) = finding.page_number {
        value["page_number"] = json!(page);
    }
    if let Some(bytes) = &finding.physical_bytes {
        value["physical_bytes"] = json!({
            "start": convert::decimal_u64(bytes.start),
            "end_exclusive": convert::decimal_u64(bytes.end_exclusive),
        });
    }
    if let Some(page) = finding.related_page_number {
        value["related_page_number"] = json!(page);
    }
    if let Some(fence) = &finding.address_fence {
        value["address_fence"] = address_fence_value(fence);
    }
    value
}

fn unknown_envelope_value(envelope: &RecoveryUnknownEnvelope) -> Value {
    let mut value = json!({
        "sequence": convert::decimal_u64(envelope.sequence),
        "reason": reason_name(envelope.reason),
        "object": object_name(envelope.object),
        "contributes_to_possible_span": envelope.contributes_to_possible_span,
        "has_unbounded_extent": envelope.has_unbounded_extent,
    });
    if let Some(page) = envelope.page_number {
        value["page_number"] = json!(page);
    }
    if let Some(bytes) = &envelope.physical_bytes {
        value["physical_bytes"] = json!({
            "start": convert::decimal_u64(bytes.start),
            "end_exclusive": convert::decimal_u64(bytes.end_exclusive),
        });
    }
    if let Some(fence) = &envelope.address_fence {
        value["address_fence"] = address_fence_value(fence);
    }
    value
}

fn address_fence_value(fence: &ValidationAddressFence) -> Value {
    match fence {
        ValidationAddressFence::Ipv4 { from, to } => json!({
            "kind": "ipv4",
            "from": std::net::Ipv4Addr::from(from.0).to_string(),
            "to": std::net::Ipv4Addr::from(to.0).to_string(),
        }),
        ValidationAddressFence::Ipv6 { from, to } => json!({
            "kind": "ipv6",
            "from": ipv6_text(*from),
            "to": ipv6_text(*to),
        }),
    }
}

fn ipv6_text(value: Ipv6Key) -> String {
    std::net::Ipv6Addr::from(value.to_u128().to_be_bytes()).to_string()
}

fn recovery_report_value(report: &RecoveryReport) -> Value {
    json!({
        "pages": page_counts(&report.pages),
        "ranges": logical_counts(&report.ranges),
        "catalog_entries": logical_counts(&report.catalog_entries),
        "membership_entries": logical_counts(&report.membership_entries),
        "structure_entries": logical_counts(&report.structure_entries),
        "metadata_chunks": logical_counts(&report.metadata_chunks),
        "retirement_records": logical_counts(&report.retirement_records),
        "verified_addresses": cardinality_decimal(report.verified_addresses),
        "rejected_addresses": cardinality_decimal(report.rejected_addresses),
        "bounded_possible_span_addresses": cardinality_decimal(report.bounded_possible_span_addresses),
        "has_unbounded_unknown": report.has_unbounded_unknown,
        "unknown_envelopes": convert::decimal_u64(report.unknown_envelopes),
    })
}

fn page_counts(value: &RecoveryPageCounts) -> Value {
    json!({
        "examined": convert::decimal_u64(value.examined),
        "accepted": convert::decimal_u64(value.accepted),
        "rejected": convert::decimal_u64(value.rejected),
        "io_unreadable": convert::decimal_u64(value.io_unreadable),
    })
}

fn logical_counts(value: &RecoveryLogicalCounts) -> Value {
    json!({
        "examined": convert::decimal_u64(value.examined),
        "accepted": convert::decimal_u64(value.accepted),
        "rejected": convert::decimal_u64(value.rejected),
    })
}

fn recovery_scratch_value(value: &RecoveryScratchAttempt) -> Value {
    json!({
        "attempt_id": convert::hex_id(&value.attempt_id),
        "directory_identity": file_identity_ok(&value.directory_identity),
        "creation_security": lifecycle::creation_security(&value.creation_security),
    })
}

fn cardinality_decimal(value: Cardinality129) -> String {
    // Two-limb base-2^64 division by ten; at most 2^129-1.
    let mut limbs = [value.lo(), value.hi(), u64::from(value.bit128())];
    if limbs.iter().all(|limb| *limb == 0) {
        return "0".to_owned();
    }
    let mut digits = Vec::with_capacity(40);
    while limbs.iter().any(|limb| *limb != 0) {
        let mut remainder: u128 = 0;
        for limb in limbs.iter_mut().rev() {
            let current = (remainder << 64) | u128::from(*limb);
            *limb = (current / 10) as u64;
            remainder = current % 10;
        }
        digits.push(b'0' + remainder as u8);
    }
    digits.reverse();
    // SAFETY-free: digits are always ASCII '0'..='9'.
    String::from_utf8(digits).expect("decimal digits are ASCII")
}

fn reason_name(value: ValidationReason) -> &'static str {
    match value {
        ValidationReason::MetaUnavailable => "meta_unavailable",
        ValidationReason::MetaInvalid => "meta_invalid",
        ValidationReason::MetaStaticMismatch => "meta_static_mismatch",
        ValidationReason::FileGeometryInvalid => "file_geometry_invalid",
        ValidationReason::RootCountInvalid => "root_count_invalid",
        ValidationReason::IoError => "io_error",
        ValidationReason::ArithmeticOverflow => "arithmetic_overflow",
        ValidationReason::PageOutOfBounds => "page_out_of_bounds",
        ValidationReason::PageHeaderInvalid => "page_header_invalid",
        ValidationReason::PageCrcMismatch => "page_crc_mismatch",
        ValidationReason::PageTypeMismatch => "page_type_mismatch",
        ValidationReason::PageBornTxnInvalid => "page_born_txn_invalid",
        ValidationReason::PageReservedNonzero => "page_reserved_nonzero",
        ValidationReason::TreeCycle => "tree_cycle",
        ValidationReason::PageAlias => "page_alias",
        ValidationReason::TreeLevelInvalid => "tree_level_invalid",
        ValidationReason::TreeOrderInvalid => "tree_order_invalid",
        ValidationReason::TreeFenceInvalid => "tree_fence_invalid",
        ValidationReason::RangeReversed => "range_reversed",
        ValidationReason::RangeOverlap => "range_overlap",
        ValidationReason::RangeNotCoalesced => "range_not_coalesced",
        ValidationReason::CatalogNameInvalid => "catalog_name_invalid",
        ValidationReason::CatalogBijectionInvalid => "catalog_bijection_invalid",
        ValidationReason::CatalogBitmapInvalid => "catalog_bitmap_invalid",
        ValidationReason::MembershipBitmapInvalid => "membership_bitmap_invalid",
        ValidationReason::MembershipHashInvalid => "membership_hash_invalid",
        ValidationReason::MembershipReverseIndexInvalid => "membership_reverse_index_invalid",
        ValidationReason::MembershipRefcountInvalid => "membership_refcount_invalid",
        ValidationReason::MembershipActiveFeedInvalid => "membership_active_feed_invalid",
        ValidationReason::BlobInvalid => "blob_invalid",
        ValidationReason::MetadataZlibInvalid => "metadata_zlib_invalid",
        ValidationReason::MetadataLengthInvalid => "metadata_length_invalid",
        ValidationReason::BitmapSummaryInvalid => "bitmap_summary_invalid",
        ValidationReason::AllocationPartitionInvalid => "allocation_partition_invalid",
        ValidationReason::RetirementOrderInvalid => "retirement_order_invalid",
        ValidationReason::RetirementListInvalid => "retirement_list_invalid",
        ValidationReason::CatalogInvalid => "catalog_invalid",
        ValidationReason::MembershipMissing => "membership_missing",
        ValidationReason::MembershipInvalid => "membership_invalid",
        ValidationReason::MetadataInvalid => "metadata_invalid",
        ValidationReason::StructurePayloadInvalid => "structure_payload_invalid",
        ValidationReason::StructureHashInvalid => "structure_hash_invalid",
        ValidationReason::StructureReverseIndexInvalid => "structure_reverse_index_invalid",
        ValidationReason::StructureRefcountInvalid => "structure_refcount_invalid",
        ValidationReason::StructureMembershipInvalid => "structure_membership_invalid",
        ValidationReason::StructureMissing => "structure_missing",
        ValidationReason::StructureInvalid => "structure_invalid",
    }
}

fn object_name(value: ValidationObject) -> &'static str {
    match value {
        ValidationObject::FileGeometry => "file_geometry",
        ValidationObject::Meta => "meta",
        ValidationObject::RangeTree => "range_tree",
        ValidationObject::CatalogNameTree => "catalog_name_tree",
        ValidationObject::CatalogIndexTree => "catalog_index_tree",
        ValidationObject::MembershipDictionary => "membership_dictionary",
        ValidationObject::MembershipReverseIndex => "membership_reverse_index",
        ValidationObject::MembershipBlob => "membership_blob",
        ValidationObject::Metadata => "metadata",
        ValidationObject::FreeBitmap => "free_bitmap",
        ValidationObject::FeedUsedBitmap => "feed_used_bitmap",
        ValidationObject::MembershipUsedBitmap => "membership_used_bitmap",
        ValidationObject::RetirementTree => "retirement_tree",
        ValidationObject::RetirementBlob => "retirement_blob",
        ValidationObject::StructureDictionary => "structure_dictionary",
        ValidationObject::StructureReverseIndex => "structure_reverse_index",
        ValidationObject::StructureUsedBitmap => "structure_used_bitmap",
    }
}

fn file_identity_ok(identity: &LocalFileIdentity) -> Value {
    lifecycle::file_identity(identity).unwrap_or_else(|error| json!({"error": error.message}))
}

fn address_family(value: AddressFamily) -> &'static str {
    match value {
        AddressFamily::Ipv4 => "ipv4",
        AddressFamily::Ipv6 => "ipv6",
    }
}

fn value_kind(value: ValueKind) -> &'static str {
    match value {
        ValueKind::Direct => "direct",
        ValueKind::Membership => "membership",
        ValueKind::Structured => "structured",
    }
}

fn structure_kind(value: StructureKind) -> &'static str {
    match value {
        StructureKind::None => "none",
        StructureKind::NetworkEnrichmentV1 => "network_enrichment_v1",
    }
}

// ---------------------------------------------------------------------------
// Budgets and errors
// ---------------------------------------------------------------------------

fn export_error(error: HandlerError) -> Error {
    if error.code == "output_limit" {
        Error::BudgetExceeded("JSONL output budget")
    } else {
        Error::Io(io::Error::other(error.message))
    }
}

pub(super) fn validate_validation_budget(value: &Value) -> Result<(), String> {
    scratch_budget_fields(value, false)
}

pub(super) fn validate_recovery_budget(value: &Value) -> Result<(), String> {
    scratch_budget_fields(value, true)
}

/// Shared validation/recovery budget wiring. Recovery adds
/// `max_output_pages`; scratch is fully disabled (both limits zero,
/// no directory) or fully enabled (nonzero limits plus directory),
/// exactly as iprange-jsonrpc-v1.md describes.
fn scratch_budget_fields(value: &Value, recovery: bool) -> Result<(), String> {
    let mut required = vec!["max_heap_bytes", "max_open_files", "max_scratch_bytes", "max_scratch_files"];
    if recovery {
        required.push("max_output_pages");
    }
    let object = reader::exact_object_opt(value, &required, &["scratch_directory"])?;
    reader::positive_u64_string(object["max_heap_bytes"].as_str())
        .map_err(|error| format!("max_heap_bytes: {error}"))?;
    reader::positive_u32(&object["max_open_files"])
        .map_err(|error| format!("max_open_files: {error}"))?;
    if recovery {
        reader::positive_u64_string(object["max_output_pages"].as_str())
            .map_err(|error| format!("max_output_pages: {error}"))?;
    }
    reader::u64_string(object["max_scratch_bytes"].as_str())
        .map_err(|error| format!("max_scratch_bytes: {error}"))?;
    let scratch_files = object["max_scratch_files"]
        .as_u64()
        .and_then(|value| u32::try_from(value).ok())
        .ok_or("max_scratch_files must be a u32 integer")?;
    if let Some(directory) = object.get("scratch_directory") {
        reader::validate_path(directory.as_str())?;
    }
    let disabled = object["max_scratch_bytes"] == "0" && scratch_files == 0;
    let enabled = object["max_scratch_bytes"] != "0"
        && scratch_files != 0
        && object.contains_key("scratch_directory");
    if (disabled && !object.contains_key("scratch_directory")) || enabled {
        return Ok(());
    }
    Err("scratch must be fully disabled or fully enabled".into())
}

pub(super) fn validation_budget(value: &Value) -> Result<ValidationBudget, HandlerError> {
    let object = value.as_object().expect("validator checked budget");
    Ok(ValidationBudget {
        max_heap_bytes: reader::u64_string(object["max_heap_bytes"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_open_files: u32_value(&object["max_open_files"])?,
        max_scratch_bytes: reader::u64_string(object["max_scratch_bytes"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_scratch_files: u32_value(&object["max_scratch_files"])?,
        scratch_directory: object
            .get("scratch_directory")
            .and_then(Value::as_str)
            .map(Path::new)
            .map(Path::to_path_buf),
    })
}

pub(super) fn recovery_budget(value: &Value) -> Result<iprange_livedb::recovery::RecoveryBudget, HandlerError> {
    let object = value.as_object().expect("validator checked budget");
    Ok(iprange_livedb::recovery::RecoveryBudget {
        max_heap_bytes: reader::u64_string(object["max_heap_bytes"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_output_pages: reader::u64_string(object["max_output_pages"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_open_files: u32_value(&object["max_open_files"])?,
        max_scratch_bytes: reader::u64_string(object["max_scratch_bytes"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_scratch_files: u32_value(&object["max_scratch_files"])?,
        scratch_directory: object
            .get("scratch_directory")
            .and_then(Value::as_str)
            .map(Path::new)
            .map(Path::to_path_buf),
    })
}

fn u32_value(value: &Value) -> Result<u32, HandlerError> {
    value
        .as_u64()
        .and_then(|value| u32::try_from(value).ok())
        .ok_or_else(|| HandlerError::invalid_params("value must be a u32 integer"))
}

fn require_existing_path(path: &str) -> Result<(), HandlerError> {
    match Path::new(path).try_exists() {
        Ok(true) => Ok(()),
        Ok(false) => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("path does not exist: {path}"),
        )),
        Err(error) => Err(HandlerError::new(
            "io",
            "not_started",
            format!("cannot inspect {path}: {error}"),
        )),
    }
}

fn exact_object<'a>(value: &'a Value, fields: &[&str]) -> Result<&'a serde_json::Map<String, Value>, String> {
    let object = value.as_object().ok_or("params must be an object")?;
    maintenance::exact_fields(object, fields)?;
    Ok(object)
}

fn bounded(result: Value) -> Result<Value, HandlerError> {
    reader::bounded_result(result)
}
