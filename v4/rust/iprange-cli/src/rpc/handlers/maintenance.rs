//! Reclamation, publication resolution, residue cleanup, and offline
//! maintenance handlers (SOW-0028 family: maintenance / resolution).
//!
//! `database.reclaim` opens one clean live writer, reclaims complete
//! oldest retirement transactions, and reports the factual enumeration
//! result and close. Publication residue inspection/resolution/removal
//! and the four maintenance kinds are entirely explicit: no handler
//! synthesizes a removal identity, and a destructive step only ever
//! runs on evidence this process or the SDK authenticated.

use std::cell::RefCell;
use std::collections::HashMap;
use std::path::Path;

use iprange_livedb::error::Error;
use iprange_livedb::publication::{
    inspect_publication_residue, remove_publication_residue, resolve_publication,
    AbandonedArtifactRemoval, AbandonedPublicationTempEntry, AbandonedPublicationTempSink,
    AbandonedPublicationTempSinkControl, AbandonedReservationEntry, AbandonedReservationSink,
    AbandonedReservationSinkControl, AccessPolicy, CleanupArtifacts, CleanupState,
    HousekeepingPayloadIdentity, PrivateOutputAttempt, PublicationDigest, PublicationProblem,
    PublicationResidueCoordination, PublicationResidueHandle, PublicationResidueInspection,
    PublicationResidueMain, PublicationResidueMainContent, PublicationResidueRemoval,
    PublicationResolutionMode, PublicationTuple, WindowsHousekeepingCandidateKind,
    WindowsHousekeepingEntry, WindowsHousekeepingRemoval, WindowsHousekeepingSink,
    WindowsHousekeepingSinkControl,
};
use iprange_livedb::recovery::{
    list_abandoned_scratch, remove_abandoned_scratch, AbandonedScratchAuthentication,
    AbandonedScratchEntry, AbandonedScratchSink, AbandonedScratchSinkControl, ScratchOwnerKind,
};
use iprange_livedb::validation::LocalFileIdentity;
use iprange_livedb::{CommitDurability, LiveWriter, ReclaimResult};
use serde_json::{json, Map, Value};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;
use super::{convert, lifecycle, output, publication_evidence, publish, reader};
use super::lifecycle::durability_outcome;
use crate::io::export_writer::{ExportBudget, ExportFacts, ExportWriter};

// ---------------------------------------------------------------------------
// iprange.v1.database.reclaim
// ---------------------------------------------------------------------------

pub fn validate_database_reclaim(params: &Value) -> Result<(), String> {
    let object = exact_object(
        params,
        &["path", "max_transactions", "max_pages", "writer_budget"],
    )?;
    reader::validate_path(object["path"].as_str())?;
    reader::u64_string(object["max_transactions"].as_str())
        .map_err(|_| "max_transactions must be a canonical unsigned decimal string".to_owned())?;
    reader::u64_string(object["max_pages"].as_str())
        .map_err(|_| "max_pages must be a canonical unsigned decimal string".to_owned())?;
    lifecycle::validate_writer_budget(&object["writer_budget"])
}

pub fn database_reclaim(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let path = object["path"].as_str().expect("validator checked path");
    let max_transactions = reader::u64_string(object["max_transactions"].as_str())
        .map_err(HandlerError::invalid_params)?;
    let max_pages = reader::u64_string(object["max_pages"].as_str())
        .map_err(HandlerError::invalid_params)?;
    let budget = lifecycle::writer_budget(&object["writer_budget"])
        .map_err(HandlerError::invalid_params)?;
    require_existing_database(Path::new(path))?;

    let mut writer = match LiveWriter::open(path, budget, &state.token()) {
        Ok(writer) => writer,
        Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let reclaim = writer.reclaim(max_transactions, max_pages, &state.token());
    let close = match writer.close() {
        Ok(result) => lifecycle::close_result(&result)?,
        Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
    };

    let reclamation = match reclaim {
        Ok(ReclaimResult::NoChange) => Reclamation::NoChange,
        Ok(ReclaimResult::Commit {
            transaction_count,
            page_count,
            commit,
        }) => {
            if commit.durability != CommitDurability::Committed || commit.cause.is_some() {
                let cause = commit.cause.as_ref();
                let code = cause.map_or("io", |error| reader::sdk_code(error.code()));
                let message = cause.map_or_else(
                    || "reclamation commit did not complete".to_owned(),
                    ToString::to_string,
                );
                return Err(HandlerError {
                    code,
                    outcome: durability_outcome(commit.durability),
                    message,
                    details: Some(json!({
                        "reclamation": reclamation_value(&Reclamation::Commit {
                            transaction_count,
                            page_count,
                            commit,
                        })?,
                        "writer_close": close,
                    })),
                });
            }
            Reclamation::Commit {
                transaction_count,
                page_count,
                commit,
            }
        }
        Err(error) => {
            return Err(HandlerError {
                details: Some(json!({"writer_close": close})),
                ..lifecycle::sdk_error(&error, "not_started")
            });
        }
    };

    if close["outcome"].as_str() == Some("close_incomplete") {
        let outcome = match &reclamation {
            Reclamation::NoChange => "not_started",
            Reclamation::Commit { commit, .. } => durability_outcome(commit.durability),
        };
        return Err(HandlerError {
            code: "io",
            outcome,
            message: "live writer close is incomplete".into(),
            details: Some(json!({
                "reclamation": reclamation_value(&reclamation)?,
                "writer_close": close,
            })),
        });
    }
    bounded(json!({
        "method": "iprange.v1.database.reclaim",
        "reclamation": reclamation_value(&reclamation)?,
        "writer_close": close,
    }))
}

enum Reclamation {
    NoChange,
    Commit {
        transaction_count: u64,
        page_count: u64,
        commit: iprange_livedb::CommitResult,
    },
}

fn reclamation_value(value: &Reclamation) -> Result<Value, HandlerError> {
    Ok(match value {
        Reclamation::NoChange => json!({"kind": "no_change"}),
        Reclamation::Commit {
            transaction_count,
            page_count,
            commit,
        } => json!({
            "kind": "commit",
            "transaction_count": convert::decimal_u64(*transaction_count),
            "page_count": convert::decimal_u64(*page_count),
            "commit": lifecycle::commit_result(commit)?,
        }),
    })
}


// ---------------------------------------------------------------------------
// iprange.v1.publication.inspect / .resolve / .residue.remove
// ---------------------------------------------------------------------------

// Connection-scoped retained publication residue handles.
//
// `inspect_publication_residue` retains opened coordination descriptors
// inside an SDK `PublicationResidueHandle`, which cannot be serialized.
// Each inspection stores the handle under a random opaque token so the
// wire result and the later residue removal stay inside one connection.
// Handlers run on the single session worker thread, so a thread-local
// registry has exactly the connection lifetime of the process.
thread_local! {
    static RESIDUE_HANDLES: RefCell<HashMap<String, PublicationResidueHandle>> =
        RefCell::new(HashMap::new());
}

pub fn validate_publication_inspect(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["path"])?;
    reader::validate_path(object["path"].as_str())
}

pub fn publication_inspect(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let path = object["path"].as_str().expect("validator checked path");
    require_existing_path(path)?;
    let inspection = inspect_publication_residue(path, &state.token())
        .map_err(|problem| publication_error(problem, "read_only_failure"))?;
    let PublicationResidueInspection {
        directory_identity,
        coordination_identity,
        coordination,
        publication,
        handle,
    } = inspection;
    let mut converted = json!({
        "directory_identity": lifecycle::file_identity(&directory_identity)?,
        "coordination": residue_coordination_name(coordination),
    });
    if let Some(identity) = coordination_identity {
        converted["coordination_identity"] = lifecycle::file_identity(&identity)?;
    }
    if let Some(publication) = &publication {
        converted["publication"] = publish::publication_result(publication)?;
    }
    if let Some(handle) = handle {
        let token = reader::random_handle()?;
        RESIDUE_HANDLES.with(|slots| slots.borrow_mut().insert(token.clone(), handle));
        converted["handle"] = residue_handle_wire(&token);
    }
    bounded(json!({
        "method": "iprange.v1.publication.inspect",
        "inspection": converted,
    }))
}

pub fn validate_publication_resolve(params: &Value) -> Result<(), String> {
    let object = exact_object_opt(params, &["path", "resolution_mode"], &["publication_result"])?;
    reader::validate_path(object["path"].as_str())?;
    match object["resolution_mode"].as_str() {
        Some("complete") | Some("remove") => {}
        _ => return Err("resolution_mode must be complete or remove".into()),
    }
    if let Some(supplied) = object.get("publication_result") {
        publication_evidence::decode_publication_result(supplied)?;
    }
    Ok(())
}

pub fn publication_resolve(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let path = object["path"].as_str().expect("validator checked path");
    let mode = match object["resolution_mode"]
        .as_str()
        .expect("validator checked resolution_mode")
    {
        "complete" => PublicationResolutionMode::Complete,
        _ => PublicationResolutionMode::Remove,
    };
    let supplied = match object.get("publication_result") {
        Some(value) => Some(
            publication_evidence::decode_publication_result(value)
                .map_err(HandlerError::invalid_params)?,
        ),
        None => None,
    };
    let result = resolve_publication(path, supplied.as_ref(), mode, &state.token())
        .map_err(|problem| publication_error(problem, "not_started"))?;
    if let Some(cause) = result.cause.as_ref() {
        let outcome = match result.publication {
            iprange_livedb::publication::PublicationStatus::Published => "published",
            iprange_livedb::publication::PublicationStatus::OutcomeUnknown => "outcome_unknown",
            iprange_livedb::publication::PublicationStatus::NotPublished => "not_published",
        };
        return Err(HandlerError {
            code: reader::sdk_code(cause.code),
            outcome,
            message: format!("publication resolution failed: {}", cause.detail),
            details: Some(json!({
                "publication": publish::publication_result(&result)?,
            })),
        });
    }
    bounded(json!({
        "method": "iprange.v1.publication.resolve",
        "publication": publish::publication_result(&result)?,
    }))
}

pub fn validate_publication_residue_remove(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["handle"])?;
    residue_handle_token(&object["handle"]).map(|_| ())
}

pub fn publication_residue_remove(
    state: &mut SessionState,
    params: Value,
) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let token = residue_handle_token(&object["handle"]).map_err(HandlerError::invalid_params)?;
    let handle = RESIDUE_HANDLES
        .with(|slots| slots.borrow_mut().remove(&token))
        .ok_or_else(|| {
            HandlerError::new(
                "handle_not_found",
                "not_started",
                "publication residue handle is unknown or already consumed",
            )
        })?;
    let removal = remove_publication_residue(handle, &state.token())
        .map_err(|problem| publication_error(problem, "not_started"))?;
    let PublicationResidueRemoval {
        directory_identity,
        coordination_identity,
        main,
        later_coordination,
        coordination_access_policy,
        cleanup,
        coordination_cleanup,
        housekeeping,
        visible_housekeeping,
        handle: retained,
        cause,
    } = removal;
    if let Some(cause) = cause {
        return Err(HandlerError {
            code: reader::sdk_code(cause.code),
            outcome: "not_started",
            message: format!("publication residue removal failed: {}", cause.detail),
            details: Some(residue_removal_facts(
                &directory_identity,
                &coordination_identity,
                main.as_ref(),
                later_coordination,
                coordination_access_policy,
                &cleanup,
                coordination_cleanup,
                &housekeeping,
                &visible_housekeeping,
            )?),
        });
    }
    let mut converted = residue_removal_facts(
        &directory_identity,
        &coordination_identity,
        main.as_ref(),
        later_coordination,
        coordination_access_policy,
        &cleanup,
        coordination_cleanup,
        &housekeeping,
        &visible_housekeeping,
    )?;
    if let Some(retained) = retained {
        let next = reader::random_handle()?;
        RESIDUE_HANDLES.with(|slots| slots.borrow_mut().insert(next.clone(), retained));
        converted["handle"] = residue_handle_wire(&next);
    }
    bounded(json!({
        "method": "iprange.v1.publication.residue.remove",
        "removal": converted,
    }))
}

#[allow(clippy::too_many_arguments)]
fn residue_removal_facts(
    directory_identity: &LocalFileIdentity,
    coordination_identity: &LocalFileIdentity,
    main: Option<&PublicationResidueMain>,
    later_coordination: PublicationResidueCoordination,
    coordination_access_policy: AccessPolicy,
    cleanup: &CleanupArtifacts,
    coordination_cleanup: iprange_livedb::publication::CoordinationCleanup,
    housekeeping: &iprange_livedb::publication::Housekeeping,
    visible_housekeeping: &[iprange_livedb::publication::HousekeepingArtifact],
) -> Result<Value, HandlerError> {
    let mut value = json!({
        "directory_identity": lifecycle::file_identity(directory_identity)?,
        "coordination_identity": lifecycle::file_identity(coordination_identity)?,
        "later_coordination": json!({"kind": residue_coordination_name(later_coordination)}),
        "coordination_access_policy": access_policy_name(coordination_access_policy),
        "cleanup": cleanup_artifacts(cleanup),
        "coordination_cleanup": lifecycle::coordination_cleanup(coordination_cleanup),
        "housekeeping": lifecycle::housekeeping(housekeeping.clone(), visible_housekeeping),
        "visible_housekeeping": Value::Array(
            visible_housekeeping.iter().map(lifecycle::housekeeping_artifact).collect(),
        ),
    });
    if let Some(main) = main {
        value["main"] = residue_main_facts(main)?;
    }
    Ok(value)
}

fn residue_main_facts(main: &PublicationResidueMain) -> Result<Value, HandlerError> {
    let mut value = json!({
        "identity": lifecycle::file_identity(&main.identity)?,
        "content": match main.content {
            PublicationResidueMainContent::V4 => "v4",
            PublicationResidueMainContent::Other => "other",
        },
        "digest": publication_digest_value(&main.digest),
        "access_policy": access_policy_name(main.access_policy),
    });
    if let Some(tuple) = &main.tuple {
        value["tuple"] = publication_tuple_value(tuple);
    }
    Ok(value)
}

fn residue_coordination_name(value: PublicationResidueCoordination) -> &'static str {
    match value {
        PublicationResidueCoordination::Absent => "absent",
        PublicationResidueCoordination::PublicationReservation => "publication_reservation",
        PublicationResidueCoordination::LiveSidecar => "live_sidecar",
        PublicationResidueCoordination::Unselectable => "unselectable",
    }
}

fn residue_handle_wire(token: &str) -> Value {
    json!({"kind": "publication_residue", "handle": token})
}

fn residue_handle_token(value: &Value) -> Result<String, String> {
    let object = value.as_object().ok_or("handle must be an object")?;
    exact_fields(object, &["kind", "handle"])?;
    if object["kind"].as_str() != Some("publication_residue") {
        return Err("handle.kind is invalid".into());
    }
    let token = object["handle"].as_str().ok_or("handle.handle must be a string")?;
    reader::validate_handle(Some(token))?;
    Ok(token.to_owned())
}

fn access_policy_name(value: AccessPolicy) -> &'static str {
    match value {
        AccessPolicy::Absent => "absent",
        AccessPolicy::CreatorOnly => "creator_only",
        AccessPolicy::ChangedOrUnproven => "changed_or_unproven",
        AccessPolicy::Unclassified => "unclassified",
    }
}

// ---------------------------------------------------------------------------
// iprange.v1.maintenance.list
// ---------------------------------------------------------------------------

pub fn validate_maintenance_list(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["directory", "kinds", "max_entries", "output"])?;
    reader::validate_path(object["directory"].as_str())?;
    let kinds = object["kinds"].as_array().ok_or("kinds must be an array")?;
    if kinds.is_empty() || kinds.len() > 4 {
        return Err("kinds must contain 1 through 4 values".into());
    }
    let mut seen: Vec<&str> = Vec::new();
    for kind in kinds {
        let kind = kind.as_str().ok_or("each kind must be a string")?;
        if !matches!(
            kind,
            "scratch" | "reservation" | "publication_temp" | "windows_housekeeping"
        ) {
            return Err(
                "kind must be scratch, reservation, publication_temp, or windows_housekeeping"
                    .into(),
            );
        }
        if seen.contains(&kind) {
            return Err("kinds must be unique".into());
        }
        seen.push(kind);
    }
    let max_entries = object["max_entries"].as_u64().ok_or("max_entries must be a u32 integer")?;
    if max_entries == 0 || max_entries > 65_536 {
        return Err("max_entries must be 1 through 65536".into());
    }
    validate_output_descriptor(&object["output"])
}

pub fn maintenance_list(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let directory = object["directory"].as_str().expect("validator checked path");
    require_existing_directory(directory)?;
    let kinds = object["kinds"].as_array().expect("validator checked kinds");
    let max_entries = object["max_entries"].as_u64().expect("validator checked max_entries") as usize;
    let (output_path, policy, budget) = output_descriptor(&object["output"])?;

    // Collect every requested kind in caller order, bounded by max_entries;
    // the rows are written only after all scans, so a refused or failed
    // list never publishes a partial destination.
    let mut collected: Vec<Vec<(Vec<u8>, Value)>> = Vec::new();
    let mut reports: Vec<Value> = Vec::new();
    let mut total: usize = 0;
    for kind in kinds {
        let kind = kind.as_str().expect("validator checked kind");
        if total >= max_entries {
            break;
        }
        let remaining = max_entries - total;
        let (entries, reported) = match kind {
            "scratch" => {
                let mut sink = ScratchCollector { directory, remaining, entries: Vec::new() };
                let reported = list_reported(
                    list_abandoned_scratch(directory, &state.token(), &mut sink),
                    sink.entries.len() as u64,
                )
                .map_err(reader::read_error)?;
                (sink.entries, reported)
            }
            "reservation" => {
                let mut sink = ReservationCollector { directory, remaining, entries: Vec::new() };
                let reported = list_reported(
                    iprange_livedb::publication::list_abandoned_reservation_artifacts(
                        directory, &state.token(), &mut sink,
                    ),
                    sink.entries.len() as u64,
                )
                .map_err(reader::read_error)?;
                (sink.entries, reported)
            }
            "publication_temp" => {
                let mut sink = PublicationTempCollector { directory, remaining, entries: Vec::new() };
                let reported = list_reported(
                    iprange_livedb::publication::list_abandoned_publication_temps(
                        directory, &state.token(), &mut sink,
                    ),
                    sink.entries.len() as u64,
                )
                .map_err(reader::read_error)?;
                (sink.entries, reported)
            }
            _ => {
                let mut sink = HousekeepingCollector { directory, remaining, entries: Vec::new() };
                let reported = list_reported(
                    iprange_livedb::publication::list_windows_housekeeping(
                        directory, &state.token(), &mut sink,
                    ),
                    sink.entries.len() as u64,
                )
                .map_err(reader::read_error)?;
                (sink.entries, reported)
            }
        };
        total = total.saturating_add(entries.len());
        reports.push(json!({"kind": kind, "entries": convert::decimal_u64(reported)}));
        collected.push(entries);
    }

    // Rows are ordered by kind (caller order) then canonical basename. The
    // SDK delivers entries in directory scan order, so each kind is sorted
    // by its canonical name key: for the fixed-format attempt-bound names
    // the canonical basename order is exactly the attempt-id byte order
    // (scratch appends the zero-padded ordinal, which orders identically to
    // its big-endian bytes); Windows housekeeping entries carry their exact
    // basename bytes.
    for entries in &mut collected {
        entries.sort_by(|left, right| left.0.cmp(&right.0));
    }
    let mut writer = ExportWriter::create(Path::new(&output_path), policy, &budget)?;
    for entries in &collected {
        for (_, value) in entries {
            writer.write_line(&value.to_string(), 0)?;
        }
    }
    let facts = writer.finish()?;
    bounded(json!({
        "method": "iprange.v1.maintenance.list",
        "output": output_facts(facts)?,
        "reports": reports,
    }))
}

/// A list that stopped at `max_entries` (sink `Stop`) is the bounded
/// complete answer; every other list failure keeps its SDK error.
fn list_reported<T>(result: Result<T, Error>, delivered: u64) -> Result<u64, Error> {
    match result {
        Ok(_) | Err(Error::StoppedBySink) => Ok(delivered),
        Err(cause) => Err(cause),
    }
}

struct ScratchCollector<'a> {
    directory: &'a str,
    remaining: usize,
    entries: Vec<(Vec<u8>, Value)>,
}

impl AbandonedScratchSink for ScratchCollector<'_> {
    fn entry(
        &mut self,
        entry: &AbandonedScratchEntry,
    ) -> iprange_livedb::error::Result<AbandonedScratchSinkControl> {
        if self.entries.len() >= self.remaining {
            return Ok(AbandonedScratchSinkControl::Stop);
        }
        let value = scratch_entry_value(self.directory, entry)
            .map_err(|_| Error::Conflict("scratch list entry conversion failed"))?;
        let mut key = Vec::with_capacity(20);
        key.extend_from_slice(&entry.attempt_id);
        key.extend_from_slice(&entry.ordinal.to_be_bytes());
        self.entries.push((key, value));
        Ok(AbandonedScratchSinkControl::Continue)
    }
}

struct ReservationCollector<'a> {
    directory: &'a str,
    remaining: usize,
    entries: Vec<(Vec<u8>, Value)>,
}

impl AbandonedReservationSink for ReservationCollector<'_> {
    fn entry(
        &mut self,
        entry: &AbandonedReservationEntry,
    ) -> iprange_livedb::error::Result<AbandonedReservationSinkControl> {
        if self.entries.len() >= self.remaining {
            return Ok(AbandonedReservationSinkControl::Stop);
        }
        let value = reservation_entry_value(self.directory, entry)
            .map_err(|_| Error::Conflict("reservation list entry conversion failed"))?;
        let mut key = Vec::with_capacity(16);
        key.extend_from_slice(&entry.publication_attempt_id);
        self.entries.push((key, value));
        Ok(AbandonedReservationSinkControl::Continue)
    }
}

struct PublicationTempCollector<'a> {
    directory: &'a str,
    remaining: usize,
    entries: Vec<(Vec<u8>, Value)>,
}

impl AbandonedPublicationTempSink for PublicationTempCollector<'_> {
    fn entry(
        &mut self,
        entry: &AbandonedPublicationTempEntry,
    ) -> iprange_livedb::error::Result<AbandonedPublicationTempSinkControl> {
        if self.entries.len() >= self.remaining {
            return Ok(AbandonedPublicationTempSinkControl::Stop);
        }
        let value = publication_temp_entry_value(self.directory, entry)
            .map_err(|_| Error::Conflict("publication-temp list entry conversion failed"))?;
        let mut key = Vec::with_capacity(16);
        key.extend_from_slice(&entry.publication_attempt_id);
        self.entries.push((key, value));
        Ok(AbandonedPublicationTempSinkControl::Continue)
    }
}

struct HousekeepingCollector<'a> {
    directory: &'a str,
    remaining: usize,
    entries: Vec<(Vec<u8>, Value)>,
}

impl WindowsHousekeepingSink for HousekeepingCollector<'_> {
    fn entry(
        &mut self,
        entry: &WindowsHousekeepingEntry,
    ) -> iprange_livedb::error::Result<WindowsHousekeepingSinkControl> {
        if self.entries.len() >= self.remaining {
            return Ok(WindowsHousekeepingSinkControl::Stop);
        }
        let value = housekeeping_entry_value(self.directory, entry)
            .map_err(|_| Error::Conflict("windows housekeeping entry conversion failed"))?;
        self.entries.push((entry.basename.to_vec(), value));
        Ok(WindowsHousekeepingSinkControl::Continue)
    }
}

fn scratch_entry_value(directory: &str, entry: &AbandonedScratchEntry) -> Result<Value, HandlerError> {
    Ok(json!({
        "kind": "scratch",
        "directory": directory,
        "directory_identity": lifecycle::file_identity(&entry.directory_identity)?,
        "artifact_identity": lifecycle::file_identity(&entry.artifact_identity)?,
        "attempt_id": convert::hex_id(&entry.attempt_id),
        "ordinal": entry.ordinal,
        "authentication": scratch_authentication_value(entry.authentication),
    }))
}

fn scratch_authentication_value(value: AbandonedScratchAuthentication) -> Value {
    match value {
        AbandonedScratchAuthentication::Authenticated(owner) => json!({
            "kind": "authenticated",
            "owner": match owner {
                ScratchOwnerKind::Validation => "validation",
                ScratchOwnerKind::Recovery => "recovery",
            },
        }),
        AbandonedScratchAuthentication::Unauthenticated => json!({"kind": "unauthenticated"}),
    }
}

fn reservation_entry_value(
    directory: &str,
    entry: &AbandonedReservationEntry,
) -> Result<Value, HandlerError> {
    let mut value = json!({
        "kind": "reservation",
        "directory": directory,
        "directory_identity": lifecycle::file_identity(&entry.directory_identity)?,
        "artifact_identity": lifecycle::file_identity(&entry.artifact_identity)?,
        "publication_attempt_id": convert::hex_id(&entry.publication_attempt_id),
    });
    if let Some(evidence) = &entry.evidence {
        let mut evidence_value = json!({
            "policy": reservation_policy_name(evidence.policy),
            "phase": reservation_phase_name(evidence.phase),
            "output": {
                "identity": lifecycle::file_identity(&evidence.output.identity)?,
                "tuple": publication_tuple_value(&evidence.output.tuple),
                "digest": publication_digest_value(&evidence.output.digest),
            },
        });
        if let Some((identity, digest)) = &evidence.previous {
            evidence_value["previous"] = json!({
                "identity": lifecycle::file_identity(identity)?,
                "digest": publication_digest_value(digest),
            });
        }
        value["evidence"] = evidence_value;
    }
    Ok(value)
}

fn reservation_policy_name(
    value: iprange_livedb::publication::AbandonedReservationPolicy,
) -> &'static str {
    match value {
        iprange_livedb::publication::AbandonedReservationPolicy::FailIfExists => "fail_if_exists",
        iprange_livedb::publication::AbandonedReservationPolicy::ReplaceExisting => {
            "replace_existing"
        }
        iprange_livedb::publication::AbandonedReservationPolicy::ReplaceExistingNoRollback => {
            "replace_existing_no_rollback"
        }
    }
}

fn reservation_phase_name(
    value: iprange_livedb::publication::AbandonedReservationPhase,
) -> &'static str {
    match value {
        iprange_livedb::publication::AbandonedReservationPhase::Prepared => "prepared",
        iprange_livedb::publication::AbandonedReservationPhase::MainMayHaveBeenAttempted => {
            "main_may_have_been_attempted"
        }
    }
}

fn publication_temp_entry_value(
    directory: &str,
    entry: &AbandonedPublicationTempEntry,
) -> Result<Value, HandlerError> {
    let mut value = json!({
        "kind": "publication_temp",
        "directory": directory,
        "directory_identity": lifecycle::file_identity(&entry.directory_identity)?,
        "artifact_identity": lifecycle::file_identity(&entry.artifact_identity)?,
        "publication_attempt_id": convert::hex_id(&entry.publication_attempt_id),
    });
    if let Some(tuple) = &entry.tuple {
        value["tuple"] = publication_tuple_value(tuple);
    }
    if let Some(digest) = &entry.digest {
        value["digest"] = publication_digest_value(digest);
    }
    Ok(value)
}

fn housekeeping_entry_value(
    directory: &str,
    entry: &WindowsHousekeepingEntry,
) -> Result<Value, HandlerError> {
    let mut value = json!({
        "kind": "windows_housekeeping",
        "directory": directory,
        "directory_identity": lifecycle::file_identity(&entry.directory_identity)?,
        "candidate_kind": housekeeping_candidate_kind_name(entry.candidate_kind),
        "basename_encoding": entry.basename_encoding,
        "basename": output::base64_padded(&entry.basename),
    });
    if let Some(identity) = &entry.identity {
        value["identity"] = lifecycle::file_identity(identity)?;
    }
    if let Some(attempt_id) = &entry.attempt_id {
        value["attempt_id"] = json!(convert::hex_id(attempt_id));
    }
    if let Some(ordinal) = entry.ordinal {
        value["ordinal"] = json!(ordinal);
    }
    if let Some(artifact) = &entry.artifact {
        value["artifact"] = lifecycle::housekeeping_artifact(artifact);
    }
    if let Some(problem) = &entry.problem {
        value["problem"] = lifecycle::publication_problem(problem);
    }
    Ok(value)
}

fn housekeeping_candidate_kind_name(value: WindowsHousekeepingCandidateKind) -> &'static str {
    match value {
        WindowsHousekeepingCandidateKind::Envelope => "envelope",
        WindowsHousekeepingCandidateKind::InertPayload => "inert_payload",
    }
}

fn publication_tuple_value(tuple: &PublicationTuple) -> Value {
    json!({
        "database_id": convert::hex_id(&tuple.database_id),
        "transaction_id": convert::decimal_u64(tuple.transaction_id),
        "commit_nonce": convert::hex_id(&tuple.commit_nonce),
    })
}

fn publication_digest_value(digest: &PublicationDigest) -> Value {
    json!({
        "byte_length": convert::decimal_u64(digest.byte_length),
        "sha512": digest.sha512.iter().map(|byte| format!("{byte:02x}")).collect::<String>(),
    })
}

// ---------------------------------------------------------------------------
// iprange.v1.maintenance.remove
// ---------------------------------------------------------------------------

pub fn validate_maintenance_remove(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["entry"])?;
    object["entry"].as_object().ok_or_else(|| "entry must be an object".to_owned()).map(|_| ())
}

pub fn maintenance_remove(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let entry = object["entry"].as_object().expect("validator checked entry");
    let kind = entry
        .get("kind")
        .and_then(Value::as_str)
        .ok_or_else(|| HandlerError::invalid_params("entry.kind must be a string"))?;
    match kind {
        "scratch" => {
            let (directory, directory_identity, artifact_identity, attempt_id, ordinal) =
                scratch_remove_fields(entry).map_err(HandlerError::invalid_params)?;
            let removal = remove_abandoned_scratch(
                &directory,
                directory_identity,
                attempt_id,
                ordinal,
                artifact_identity,
                &state.token(),
            )
            .map_err(sdk_remove_error)?;
            removal_result(removal)
        }
        "reservation" => {
            let (directory, directory_identity, artifact_identity, attempt_id) =
                reservation_remove_fields(entry).map_err(HandlerError::invalid_params)?;
            let removal = iprange_livedb::publication::remove_abandoned_reservation_artifact(
                &directory,
                directory_identity,
                attempt_id,
                artifact_identity,
                &state.token(),
            )
            .map_err(sdk_remove_error)?;
            removal_result(removal)
        }
        "publication_temp" => {
            let (directory, directory_identity, artifact_identity, attempt_id, tuple, digest) =
                publication_temp_remove_fields(entry).map_err(HandlerError::invalid_params)?;
            let removal = iprange_livedb::publication::remove_abandoned_publication_temp(
                &directory,
                directory_identity,
                attempt_id,
                artifact_identity,
                tuple,
                digest,
                &state.token(),
            )
            .map_err(sdk_remove_error)?;
            removal_result(removal)
        }
        "windows_housekeeping" => {
            let (directory, directory_identity, attempt_id, ordinal, envelope_identity) =
                housekeeping_remove_fields(entry).map_err(HandlerError::invalid_params)?;
            let removal = iprange_livedb::publication::remove_windows_housekeeping(
                &directory,
                directory_identity,
                attempt_id,
                ordinal,
                envelope_identity,
                None::<HousekeepingPayloadIdentity>,
                &state.token(),
            )
            .map_err(sdk_remove_error)?;
            windows_removal_result(removal)
        }
        _ => Err(HandlerError::invalid_params("entry.kind is invalid")),
    }
}

fn scratch_remove_fields(
    entry: &Map<String, Value>,
) -> Result<(String, LocalFileIdentity, LocalFileIdentity, [u8; 16], u32), String> {
    exact_fields(
        entry,
        &[
            "kind",
            "directory",
            "directory_identity",
            "artifact_identity",
            "attempt_id",
            "ordinal",
            "authentication",
        ],
    )?;
    let directory = remove_directory(entry)?;
    let directory_identity = identity_from_value(&entry["directory_identity"])?;
    let artifact_identity = identity_from_value(&entry["artifact_identity"])?;
    let attempt_id = hex16_from_value(&entry["attempt_id"])?;
    let ordinal = u32_member(entry, "ordinal")?;
    validate_scratch_authentication(&entry["authentication"])?;
    Ok((directory, directory_identity, artifact_identity, attempt_id, ordinal))
}

fn validate_scratch_authentication(value: &Value) -> Result<(), String> {
    let object = value.as_object().ok_or("entry.authentication must be an object")?;
    match object.get("kind").and_then(Value::as_str) {
        Some("authenticated") => {
            exact_fields(object, &["kind", "owner"])?;
            match object["owner"].as_str() {
                Some("validation") | Some("recovery") => Ok(()),
                _ => Err("entry.authentication.owner is invalid".into()),
            }
        }
        Some("unauthenticated") => exact_fields(object, &["kind"]).map(|_| ()),
        _ => Err("entry.authentication.kind is invalid".into()),
    }
}

/// Enforces the round-trip member set of one `maintenance.remove` entry.
///
/// Every member must belong to the row form `maintenance.list` publishes
/// for the kind, and every member the emitter writes for every row of the
/// kind must be present. A member named in `optional` is one the emitter
/// omits when the scan could not read it, so its absence is a valid row
/// and its presence is validated as strictly as a required member. Absent
/// is the only absent form: a present null is a different value and is
/// refused by the member decoder.
///
/// The three identification members of a `windows_housekeeping` row stay
/// required although the emitter can omit them: they are the opaque
/// authenticated removal identity (attempt id, ordinal, and envelope
/// identity) that `maintenance.remove` hands to the SDK, and a row without
/// them is not a removable entry (spec `maintenance.list`: "Every removable
/// entry contains its opaque authenticated removal identity"). Accepting
/// such a row would force the removal to invent an identity from the
/// basename, which spec `maintenance.remove` forbids.
fn maintenance_entry_members(
    entry: &Map<String, Value>,
    required: &[&str],
    optional: &[&str],
) -> Result<(), String> {
    for key in entry.keys() {
        if !required.contains(&key.as_str()) && !optional.contains(&key.as_str()) {
            return Err(format!("unknown member {key:?}"));
        }
    }
    for field in required {
        if !entry.contains_key(*field) {
            return Err(format!("missing member {field:?}"));
        }
    }
    Ok(())
}

fn reservation_remove_fields(
    entry: &Map<String, Value>,
) -> Result<(String, LocalFileIdentity, LocalFileIdentity, [u8; 16]), String> {
    maintenance_entry_members(
        entry,
        &[
            "kind",
            "directory",
            "directory_identity",
            "artifact_identity",
            "publication_attempt_id",
        ],
        &["evidence"],
    )?;
    let directory = remove_directory(entry)?;
    let directory_identity = identity_from_value(&entry["directory_identity"])?;
    let artifact_identity = identity_from_value(&entry["artifact_identity"])?;
    let attempt_id = hex16_from_value(&entry["publication_attempt_id"])?;
    // The reservation removal is authorized by the directory, artifact, and
    // attempt identity alone; evidence never reaches the SDK, so a row the
    // scan could not authenticate is still removable.
    let Some(evidence_value) = entry.get("evidence") else {
        return Ok((directory, directory_identity, artifact_identity, attempt_id));
    };
    if evidence_value.is_null() {
        return Err("entry.evidence must not be null; absent is the only absent form".into());
    }
    let evidence = evidence_value
        .as_object()
        .ok_or("entry.evidence must be an object")?;
    // policy, phase, output are required; previous is the only
    // allowed optional member and any other key is refused.
    for field in ["policy", "phase", "output"] {
        if !evidence.contains_key(field) {
            return Err(format!("missing member {field:?}"));
        }
    }
    for key in evidence.keys() {
        if !["policy", "phase", "output", "previous"].contains(&key.as_str()) {
            return Err(format!("unknown member {key:?}"));
        }
    }
    for field in ["policy", "phase"] {
        if !evidence[field].is_string() {
            return Err(format!("entry.evidence.{field} must be a string"));
        }
    }
    let output = evidence["output"]
        .as_object()
        .ok_or("entry.evidence.output must be an object")?;
    exact_fields(output, &["identity", "tuple", "digest"])?;
    identity_from_value(&output["identity"])?;
    tuple_from_value(&output["tuple"])?;
    digest_from_value(&output["digest"])?;
    match evidence.get("previous") {
        None => {}
        Some(Value::Null) => {
            return Err(
                "entry.evidence.previous must not be null; absent is the only absent form".into(),
            )
        }
        Some(value) => {
            let previous = value
                .as_object()
                .ok_or("entry.evidence.previous must be an object")?;
            exact_fields(previous, &["identity", "digest"])?;
            identity_from_value(&previous["identity"])?;
            digest_from_value(&previous["digest"])?;
        }
    }
    Ok((directory, directory_identity, artifact_identity, attempt_id))
}

/// Validates and extracts the publication-temp entry fields. The tuple and
/// digest content evidence is optional exactly as `maintenance.list` emits
/// it, and validated to the same depth when the row carries it.
fn publication_temp_remove_fields(
    entry: &Map<String, Value>,
) -> Result<
    (
        String,
        LocalFileIdentity,
        LocalFileIdentity,
        [u8; 16],
        Option<PublicationTuple>,
        Option<PublicationDigest>,
    ),
    String,
> {
    maintenance_entry_members(
        entry,
        &[
            "kind",
            "directory",
            "directory_identity",
            "artifact_identity",
            "publication_attempt_id",
        ],
        &["tuple", "digest"],
    )?;
    let directory = remove_directory(entry)?;
    let directory_identity = identity_from_value(&entry["directory_identity"])?;
    let artifact_identity = identity_from_value(&entry["artifact_identity"])?;
    let attempt_id = hex16_from_value(&entry["publication_attempt_id"])?;
    // A private output whose content is not a readable v4 main carries no
    // evidence, and the SDK removes such partial content only with both
    // members absent; a present member is validated as strictly as a
    // required one, and the pair is never half present.
    let has_tuple = entry.contains_key("tuple");
    let has_digest = entry.contains_key("digest");
    if has_tuple != has_digest {
        return Err("entry.tuple and entry.digest must both be present or both absent".into());
    }
    let tuple = match entry.get("tuple") {
        Some(value) => tuple_from_value(value)?,
        None => None,
    };
    let digest = match entry.get("digest") {
        Some(value) => digest_from_value(value)?,
        None => None,
    };
    Ok((
        directory,
        directory_identity,
        artifact_identity,
        attempt_id,
        tuple,
        digest,
    ))
}

fn housekeeping_remove_fields(
    entry: &Map<String, Value>,
) -> Result<(String, LocalFileIdentity, [u8; 16], u32, LocalFileIdentity), String> {
    // identity, attempt_id, and ordinal carry the authenticated removal
    // identity of this kind, so they stay required; artifact and problem
    // are optional members the emitter omits when they do not apply
    // (maintenance_entry_members states the general rule).
    maintenance_entry_members(
        entry,
        &[
            "kind",
            "directory",
            "directory_identity",
            "candidate_kind",
            "basename_encoding",
            "basename",
            "identity",
            "attempt_id",
            "ordinal",
        ],
        &["artifact", "problem"],
    )?;
    let directory = remove_directory(entry)?;
    let directory_identity = identity_from_value(&entry["directory_identity"])?;
    match entry["candidate_kind"].as_str() {
        Some("envelope") | Some("inert_payload") => {}
        _ => return Err("entry.candidate_kind is invalid".into()),
    }
    if !entry["basename_encoding"].is_u64() {
        return Err("entry.basename_encoding must be an integer".into());
    }
    let basename = entry["basename"].as_str().ok_or("entry.basename must be a string")?;
    lifecycle::decode_base64(basename)?;
    let identity = identity_from_value(&entry["identity"])?;
    let attempt_id = hex16_from_value(&entry["attempt_id"])?;
    let ordinal = u32_member(entry, "ordinal")?;
    if let Some(artifact) = entry.get("artifact") {
        artifact.as_object().ok_or("entry.artifact must be an object")?;
    }
    if let Some(problem) = entry.get("problem") {
        problem.as_object().ok_or("entry.problem must be an object")?;
    }
    Ok((directory, directory_identity, attempt_id, ordinal, identity))
}

fn remove_directory(entry: &Map<String, Value>) -> Result<String, String> {
    let directory = entry["directory"].as_str().ok_or("entry.directory must be a string")?;
    reader::validate_path(Some(directory))?;
    Ok(directory.to_owned())
}

fn tuple_from_value(value: &Value) -> Result<Option<PublicationTuple>, String> {
    if value.is_null() {
        return Err("tuple must not be null; absent is the only absent form".into());
    }
    let object = value.as_object().ok_or("tuple must be an object")?;
    exact_fields(object, &["database_id", "transaction_id", "commit_nonce"])?;
    let database_id = hex16_from_value(&object["database_id"])?;
    let transaction_id = decimal_u64_value(&object["transaction_id"])?;
    let commit_nonce = hex16_from_value(&object["commit_nonce"])?;
    Ok(Some(PublicationTuple {
        database_id,
        transaction_id,
        commit_nonce,
    }))
}

fn digest_from_value(value: &Value) -> Result<Option<PublicationDigest>, String> {
    if value.is_null() {
        return Err("digest must not be null; absent is the only absent form".into());
    }
    let object = value.as_object().ok_or("digest must be an object")?;
    exact_fields(object, &["byte_length", "sha512"])?;
    let byte_length = decimal_u64_value(&object["byte_length"])?;
    let sha512 = hex64_from_value(&object["sha512"])?;
    Ok(Some(PublicationDigest {
        byte_length,
        sha512,
    }))
}

fn removal_result(removal: AbandonedArtifactRemoval) -> Result<Value, HandlerError> {
    if let Some(cause) = &removal.cause {
        return Err(HandlerError {
            code: reader::sdk_code(cause.code),
            outcome: "not_started",
            message: format!("maintenance removal failed: {}", cause.detail),
            details: Some(removal_facts(&removal)),
        });
    }
    bounded(json!({
        "method": "iprange.v1.maintenance.remove",
        "removal": removal_facts(&removal),
    }))
}

fn removal_facts(removal: &AbandonedArtifactRemoval) -> Value {
    json!({
        "source_present": removal.source_present,
        "cleanup_state": cleanup_state_name(removal.cleanup_state),
        "housekeeping": lifecycle::housekeeping(removal.housekeeping.clone(), &removal.visible_housekeeping),
        "visible_housekeeping": Value::Array(
            removal.visible_housekeeping.iter().map(lifecycle::housekeeping_artifact).collect(),
        ),
    })
}

fn windows_removal_result(removal: WindowsHousekeepingRemoval) -> Result<Value, HandlerError> {
    let facts = json!({
        "housekeeping": lifecycle::housekeeping(removal.housekeeping.clone(), &removal.visible_housekeeping),
        "visible_housekeeping": Value::Array(
            removal.visible_housekeeping.iter().map(lifecycle::housekeeping_artifact).collect(),
        ),
    });
    if let Some(cause) = &removal.cause {
        return Err(HandlerError {
            code: reader::sdk_code(cause.code),
            outcome: "not_started",
            message: format!("maintenance removal failed: {}", cause.detail),
            details: Some(facts),
        });
    }
    bounded(json!({
        "method": "iprange.v1.maintenance.remove",
        "removal": facts,
    }))
}

fn sdk_remove_error(error: Error) -> HandlerError {
    lifecycle::sdk_error(&error, "not_started")
}

// ---------------------------------------------------------------------------
// Shared conversion helpers (also used by recovery.rs)
// ---------------------------------------------------------------------------

pub(super) fn cleanup_state_name(value: CleanupState) -> &'static str {
    match value {
        CleanupState::Clean => "clean",
        CleanupState::ResiduePossible => "residue_possible",
    }
}

pub(super) fn cleanup_artifacts(value: &CleanupArtifacts) -> Value {
    if value.is_empty() {
        return json!({});
    }
    json!({
        "artifacts": value.iter().map(lifecycle::cleanup_artifact).collect::<Vec<_>>(),
    })
}

pub(super) fn private_output_attempt_value(value: &PrivateOutputAttempt) -> Value {
    json!({
        "publication_attempt_id": convert::hex_id(&value.publication_attempt_id),
        "directory_identity": lifecycle::file_identity(&value.directory_identity)
            .unwrap_or_else(|error| json!({"error": error.message})),
        "basename_encoding": value.basename_encoding,
        "basename": lifecycle::basename(&value.basename, value.basename_encoding),
        "identity": value.identity.as_ref().map(|identity| {
            lifecycle::file_identity(identity)
                .unwrap_or_else(|error| json!({"error": error.message}))
        }),
        "creation_security": lifecycle::creation_security(&value.creation_security),
    })
}

pub(super) fn exact_fields(object: &Map<String, Value>, fields: &[&str]) -> Result<(), String> {
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
    Ok(())
}

pub(super) fn identity_from_value(value: &Value) -> Result<LocalFileIdentity, String> {
    let object = value.as_object().ok_or("identity must be an object")?;
    exact_fields(object, &["volume", "file"])?;
    let volume = decimal_u64_value(&object["volume"])?;
    let file = decimal_u64_value(&object["file"])?;
    let mut bytes = [0u8; 32];
    bytes[0..8].copy_from_slice(&volume.to_le_bytes());
    bytes[8..16].copy_from_slice(&file.to_le_bytes());
    let kind = if cfg!(windows) { 2 } else { 1 };
    Ok(LocalFileIdentity { kind, bytes })
}

pub(super) fn hex16_from_value(value: &Value) -> Result<[u8; 16], String> {
    let text = value.as_str().ok_or("value must be a string")?;
    decode_hex(text, 16).ok_or_else(|| "value must be 32 lowercase hexadecimal characters".to_owned())
}

pub(super) fn hex64_from_value(value: &Value) -> Result<[u8; 64], String> {
    let text = value.as_str().ok_or("value must be a string")?;
    decode_hex(text, 64).ok_or_else(|| "value must be 128 lowercase hexadecimal characters".to_owned())
}

fn decode_hex<const N: usize>(text: &str, length: usize) -> Option<[u8; N]> {
    if text.len() != length * 2 || !text.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return None;
    }
    let mut bytes = [0u8; N];
    for (index, pair) in text.as_bytes().chunks_exact(2).enumerate() {
        let high = (pair[0] as char).to_digit(16)?;
        let low = (pair[1] as char).to_digit(16)?;
        bytes[index] = ((high << 4) | low) as u8;
    }
    Some(bytes)
}

pub(super) fn decimal_u64_value(value: &Value) -> Result<u64, String> {
    reader::u64_string(value.as_str())
}

fn u32_member(object: &Map<String, Value>, name: &str) -> Result<u32, String> {
    object[name]
        .as_u64()
        .and_then(|value| u32::try_from(value).ok())
        .ok_or_else(|| format!("{name} must be a u32 integer"))
}

/// Strict JSONL-only tabular output descriptor shared by validate,
/// recover, and maintenance.list.
pub(super) fn validate_output_descriptor(value: &Value) -> Result<(), String> {
    let object = exact_object(
        value,
        &["path", "format", "publication_policy", "result_budget"],
    )?;
    reader::validate_path(object["path"].as_str())?;
    if object["format"].as_str() != Some("jsonl") {
        return Err("format must be jsonl".into());
    }
    reader::publication_policy(object["publication_policy"].as_str())
        .map_err(|_| "publication_policy is invalid".to_string())?;
    validate_result_budget(&object["result_budget"])
}

pub(super) fn output_descriptor(
    value: &Value,
) -> Result<
    (
        String,
        iprange_livedb::publication::PublicationPolicy,
        ExportBudget,
    ),
    HandlerError,
> {
    let object = value.as_object().expect("validator checked output");
    let path = object["path"].as_str().expect("validator checked path").to_owned();
    let policy = reader::publication_policy(object["publication_policy"].as_str())
        .map_err(|_| HandlerError::invalid_params("publication_policy is invalid"))?;
    let budget = result_budget(&object["result_budget"])?;
    Ok((path, policy, budget))
}

fn validate_result_budget(value: &Value) -> Result<(), String> {
    let budget = exact_object(value, &["max_rows", "max_output_bytes", "max_open_files"])?;
    reader::positive_u64_string(budget["max_rows"].as_str())
        .map_err(|error| format!("result_budget.max_rows: {error}"))?;
    reader::positive_u64_string(budget["max_output_bytes"].as_str())
        .map_err(|error| format!("result_budget.max_output_bytes: {error}"))?;
    reader::positive_u32(&budget["max_open_files"])
        .map(|_| ())
        .map_err(|error| format!("result_budget.max_open_files: {error}"))
}

fn result_budget(value: &Value) -> Result<ExportBudget, HandlerError> {
    let budget = value.as_object().expect("validator checked result_budget");
    Ok(ExportBudget {
        max_rows: reader::u64_string(budget["max_rows"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_output_bytes: reader::u64_string(budget["max_output_bytes"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_open_files: budget["max_open_files"]
            .as_u64()
            .and_then(|value| u32::try_from(value).ok())
            .ok_or_else(|| HandlerError::invalid_params("max_open_files must be u32"))?,
    })
}

pub(super) fn output_facts(facts: ExportFacts) -> Result<Value, HandlerError> {
    Ok(json!({
        "path": facts.path,
        "sha256": facts.sha256,
        "bytes": facts.bytes.to_string(),
        "rows": facts.rows.to_string(),
    }))
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

fn require_existing_directory(path: &str) -> Result<(), HandlerError> {
    match Path::new(path).metadata() {
        Ok(value) if value.is_dir() => Ok(()),
        Ok(_) => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("maintenance directory is not a directory: {path}"),
        )),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("maintenance directory does not exist: {path}"),
        )),
        Err(error) => Err(HandlerError::new(
            "io",
            "not_started",
            format!("inspect maintenance directory {path}: {error}"),
        )),
    }
}

fn publication_error(problem: PublicationProblem, outcome: &'static str) -> HandlerError {
    HandlerError::new(
        reader::sdk_code(problem.code),
        outcome,
        problem.detail.into_owned(),
    )
}

fn exact_object<'a>(value: &'a Value, fields: &[&str]) -> Result<&'a Map<String, Value>, String> {
    let object = value.as_object().ok_or("params must be an object")?;
    exact_fields(object, fields)?;
    Ok(object)
}

fn exact_object_opt<'a>(
    value: &'a Value,
    required: &[&str],
    optional: &[&str],
) -> Result<&'a Map<String, Value>, String> {
    let object = value.as_object().ok_or("params must be an object")?;
    for key in object.keys() {
        if !required.contains(&key.as_str()) && !optional.contains(&key.as_str()) {
            return Err(format!("unknown member {key:?}"));
        }
    }
    for field in required {
        if !object.contains_key(*field) {
            return Err(format!("missing member {field:?}"));
        }
    }
    Ok(object)
}

fn bounded(result: Value) -> Result<Value, HandlerError> {
    reader::bounded_result(result)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// One valid reservation remove evidence object; the optional
    /// previous member is included only when requested (the list
    /// emitter omits it when there is no previous block, so the
    /// remove wire check must accept its absence).
    fn reservation_evidence(previous: Option<Value>) -> Value {
        let mut evidence = json!({
            "policy": "replace_existing",
            "phase": "prepared",
            "output": {
                "identity": {"volume": "1", "file": "2"},
                "tuple": {
                    "database_id": "11111111111111111111111111111111",
                    "transaction_id": "1",
                    "commit_nonce": "22222222222222222222222222222222",
                },
                "digest": {
                    "byte_length": "64",
                    "sha512": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                },
            },
        });
        if let Some(previous) = previous {
            evidence["previous"] = previous;
        }
        evidence
    }

    /// One valid reservation previous block.
    fn reservation_previous() -> Value {
        json!({
            "identity": {"volume": "3", "file": "4"},
            "digest": {
                "byte_length": "32",
                "sha512": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            },
        })
    }

    /// One complete maintenance.remove reservation entry (the exact
    /// list row shape) around the given evidence value.
    fn reservation_remove_entry(evidence: Value) -> Map<String, Value> {
        json!({
            "kind": "reservation",
            "directory": "/tmp/probe",
            "directory_identity": {"volume": "1", "file": "2"},
            "artifact_identity": {"volume": "9", "file": "8"},
            "publication_attempt_id": "11111111111111111111111111111111",
            "evidence": evidence,
        })
        .as_object()
        .unwrap()
        .clone()
    }

    #[test]
    fn reservation_remove_accepts_evidence_without_previous() {
        let entry = reservation_remove_entry(reservation_evidence(None));
        let (directory, _, _, attempt) = reservation_remove_fields(&entry).unwrap();
        assert_eq!(directory, "/tmp/probe");
        assert_eq!(attempt, [0x11; 16]);
    }

    #[test]
    fn reservation_remove_accepts_evidence_with_previous() {
        let entry = reservation_remove_entry(reservation_evidence(Some(reservation_previous())));
        reservation_remove_fields(&entry).unwrap();
    }

    #[test]
    fn reservation_remove_refuses_null_previous() {
        let entry = reservation_remove_entry(reservation_evidence(Some(Value::Null)));
        let error = reservation_remove_fields(&entry).unwrap_err();
        assert!(
            error.contains("previous"),
            "null previous must be refused, got: {error}"
        );
    }

    #[test]
    fn reservation_remove_refuses_unknown_evidence_keys() {
        let mut evidence = reservation_evidence(Some(reservation_previous()));
        evidence["extra"] = json!("x");
        let entry = reservation_remove_entry(evidence);
        let error = reservation_remove_fields(&entry).unwrap_err();
        assert!(
            error.contains("unknown member"),
            "unknown evidence member must be refused, got: {error}"
        );
    }

    #[test]
    fn reservation_remove_refuses_missing_required_evidence_member() {
        let mut evidence = reservation_evidence(None);
        evidence.as_object_mut().unwrap().remove("output");
        let entry = reservation_remove_entry(evidence);
        let error = reservation_remove_fields(&entry).unwrap_err();
        assert!(
            error.contains("missing member"),
            "missing required evidence member must be refused, got: {error}"
        );
    }

    /// One complete maintenance.remove windows_housekeeping entry
    /// (the exact list row shape); artifact and problem are included
    /// only when requested, exactly like maintenance.list emits them.
    fn housekeeping_remove_entry(artifact: Option<Value>, problem: Option<Value>) -> Map<String, Value> {
        let mut entry = json!({
            "kind": "windows_housekeeping",
            "directory": "/tmp/probe",
            "directory_identity": {"volume": "1", "file": "2"},
            "candidate_kind": "envelope",
            "basename_encoding": 2,
            "basename": "LgBpAHAAcgBhAG4AZwBlAC0AZwBjAGEAdQB0AGgALQAxAC4AdABtAHAA",
            "identity": {"volume": "3", "file": "4"},
            "attempt_id": "11111111111111111111111111111111",
            "ordinal": 1,
        });
        if let Some(artifact) = artifact {
            entry["artifact"] = artifact;
        }
        if let Some(problem) = problem {
            entry["problem"] = problem;
        }
        entry.as_object().unwrap().clone()
    }

    #[test]
    fn housekeeping_remove_accepts_row_without_optional_members() {
        // A clean list row omits artifact and problem; the unchanged
        // row must round-trip into remove.
        let entry = housekeeping_remove_entry(None, None);
        let (directory, _, attempt, ordinal, _) = housekeeping_remove_fields(&entry).unwrap();
        assert_eq!(directory, "/tmp/probe");
        assert_eq!(attempt, [0x11; 16]);
        assert_eq!(ordinal, 1);
    }

    #[test]
    fn housekeeping_remove_accepts_row_with_optional_members() {
        let artifact = json!({"kind": "private_output"});
        let problem = json!({"code": "cleanup_conflict"});
        let entry = housekeeping_remove_entry(Some(artifact), Some(problem));
        housekeeping_remove_fields(&entry).unwrap();
    }

    #[test]
    fn housekeeping_remove_refuses_unknown_members() {
        let mut entry = housekeeping_remove_entry(None, None);
        entry.insert("extra".into(), json!("x"));
        let error = housekeeping_remove_fields(&entry).unwrap_err();
        assert!(
            error.contains("unknown member"),
            "unknown entry member must be refused, got: {error}"
        );
    }

    #[test]
    fn reservation_remove_handler_accepts_evidence_without_previous() {
        // The wire layer accepts the listed row unchanged; the only
        // refusal that can follow is an SDK-level outcome about the
        // probe directory, never an invalid_argument param schema
        // error (the probe directory does not exist).
        let entry = reservation_remove_entry(reservation_evidence(None));
        let result = maintenance_remove(&mut SessionState::default(), json!({"entry": entry}));
        match result {
            Ok(_) => {}
            Err(error) => assert_ne!(
                error.code, "invalid_argument",
                "evidence without previous must pass the handler wire check: {}",
                error.message
            ),
        }
    }

    // ------------------------------------------------------------------
    // maintenance.list -> maintenance.remove row round-trip
    // ------------------------------------------------------------------

    /// The platform creator-only security kind recorded in a scratch
    /// ownership header (Rust namespace CREATION_SECURITY_KIND arm).
    #[cfg(not(windows))]
    const SCRATCH_CREATION_SECURITY_KIND: u16 = 1;
    #[cfg(windows)]
    const SCRATCH_CREATION_SECURITY_KIND: u16 = 2;

    /// One unique scratch directory per probe; the caller removes it.
    fn probe_directory(label: &str) -> std::path::PathBuf {
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock after the unix epoch")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-maintenance-{label}-{}-{unique}",
            std::process::id()
        ));
        std::fs::create_dir_all(&path).expect("create the maintenance probe directory");
        path
    }

    /// The lowercase hex wire form of one attempt identity.
    fn attempt_hex(attempt: [u8; 16]) -> String {
        attempt.iter().map(|byte| format!("{byte:02x}")).collect()
    }

    /// One fixed attempt identity per kind, so each kind owns an exact
    /// private name that no other probe shares.
    fn maintenance_attempt(mark: u8) -> [u8; 16] {
        let mut attempt = [0u8; 16];
        for (index, byte) in attempt.iter_mut().enumerate() {
            *byte = ((index * 16) as u8) | mark;
        }
        attempt[15] = mark;
        attempt
    }

    /// One portable local identity of the current platform, the pair the
    /// wire encodes as {"volume","file"}.
    fn local_identity(volume: u64, file: u64) -> LocalFileIdentity {
        let mut bytes = [0u8; 32];
        bytes[0..8].copy_from_slice(&volume.to_le_bytes());
        bytes[8..16].copy_from_slice(&file.to_le_bytes());
        LocalFileIdentity {
            kind: if cfg!(windows) { 2 } else { 1 },
            bytes,
        }
    }

    /// CRC-32C (Castagnoli, reflected) of one buffer with one fixed range
    /// treated as zero: the scratch header checksum rule of
    /// binary-format-v4.md.
    fn scratch_checksum(bytes: &[u8], zero_at: usize, zero_len: usize) -> u32 {
        let mut mirror = bytes.to_vec();
        mirror[zero_at..zero_at + zero_len].fill(0);
        let mut crc = 0xffff_ffff_u32;
        for byte in mirror {
            crc ^= u32::from(byte);
            for _ in 0..8 {
                crc = if crc & 1 == 1 {
                    (crc >> 1) ^ 0x82f6_3b78
                } else {
                    crc >> 1
                };
            }
        }
        !crc
    }

    /// The exact 62-byte basename of one recovery-scratch artifact.
    fn scratch_basename(attempt: [u8; 16], ordinal: u32) -> String {
        format!(
            ".iprange-scratch-{}-{:08x}.tmp",
            attempt_hex(attempt),
            ordinal
        )
    }

    /// Writes one recovery-scratch artifact whose 128-byte ownership header
    /// authenticates: magic, fixed fields, meta facts, attempt, ordinal, the
    /// platform creator-only security kind, a non-zero creator commitment,
    /// and the header checksum. The POSIX removal arm does not compare the
    /// commitment against the live profile, so the artifact is a true
    /// abandoned-scratch entry; the Windows arm does, so this fixture — and
    /// the round-trip that uses it — is unix-only.
    #[cfg(unix)]
    fn authenticated_scratch_artifact(
        directory: &std::path::Path,
        attempt: [u8; 16],
        ordinal: u32,
    ) -> std::path::PathBuf {
        use std::os::unix::fs::PermissionsExt as _;
        let mut header = [0u8; 128];
        header[0..8].copy_from_slice(b"IPR4SCR1");
        header[8..10].copy_from_slice(&1u16.to_le_bytes()); // version
        header[10..12].copy_from_slice(&128u16.to_le_bytes()); // header size
        header[12..14].copy_from_slice(&2u16.to_le_bytes()); // owner kind: recovery
        header[16] = 1; // database id
        header[32..40].copy_from_slice(&7u64.to_le_bytes()); // transaction id
        header[40] = 2; // commit nonce
        header[56..72].copy_from_slice(&attempt);
        header[72..76].copy_from_slice(&ordinal.to_le_bytes());
        header[76..78].copy_from_slice(&SCRATCH_CREATION_SECURITY_KIND.to_le_bytes());
        header[80..112].copy_from_slice(&[0x5a; 32]); // non-zero commitment
        let checksum = scratch_checksum(&header, 124, 4);
        header[124..128].copy_from_slice(&checksum.to_le_bytes());
        let path = directory.join(scratch_basename(attempt, ordinal));
        std::fs::write(&path, header).expect("write the scratch artifact");
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))
            .expect("make the scratch artifact creator-only");
        path
    }

    /// Writes one exact-pattern private artifact of `prefix` whose content
    /// is neither a readable reservation record nor readable v4 geometry,
    /// which is what a killed publisher leaves behind. maintenance.list
    /// reports such an artifact without its optional evidence members.
    fn partial_private_artifact(
        directory: &std::path::Path,
        prefix: &str,
        attempt: [u8; 16],
    ) -> std::path::PathBuf {
        let path = directory.join(format!("{prefix}{}.tmp", attempt_hex(attempt)));
        std::fs::write(&path, b"partial").expect("write the private residue");
        path
    }

    /// Runs iprange.v1.maintenance.list for the given kinds and returns the
    /// exact JSONL row bytes it published, in list order.
    fn listed_rows(directory: &std::path::Path, kinds: &[&str]) -> Vec<String> {
        let rows_dir = probe_directory("rows");
        let rows_path = rows_dir.join("rows.jsonl");
        let mut state = SessionState::default();
        maintenance_list(
            &mut state,
            json!({
                "directory": directory.display().to_string(),
                "kinds": kinds,
                "max_entries": 64,
                "output": {
                    "path": rows_path.display().to_string(),
                    "format": "jsonl",
                    "publication_policy": "fail_if_exists",
                    "result_budget": {
                        "max_rows": "64",
                        "max_output_bytes": "65536",
                        "max_open_files": 3,
                    },
                },
            }),
        )
        .expect("maintenance.list serves the requested kinds");
        let published = std::fs::read_to_string(&rows_path).expect("read the published rows");
        std::fs::remove_dir_all(&rows_dir).ok();
        published.lines().map(str::to_owned).collect()
    }

    /// Submits one exact maintenance.list row as the entry of
    /// iprange.v1.maintenance.remove. The row is parsed from the published
    /// bytes and proven to re-serialize identically before the call, so the
    /// handler received what the list published and the test reconstructed
    /// no field.
    fn remove_row(row: &str) -> Result<Value, HandlerError> {
        let entry: Value = serde_json::from_str(row).expect("a published row is JSON");
        assert_eq!(
            entry.to_string(),
            row,
            "the removal entry must be the exact published row bytes"
        );
        let params = json!({ "entry": entry });
        validate_maintenance_remove(&params).expect("one unchanged entry object is valid params");
        maintenance_remove(&mut SessionState::default(), params)
    }

    /// Parses one published row.
    fn published_row(row: &str) -> Value {
        serde_json::from_str(row).expect("a published row is JSON")
    }

    /// Checks one successful removal terminal against the source-presence
    /// fact the caller expects: the first removal of a listed row reports
    /// the artifact it retired, a replay of the same row reports the durable
    /// absence.
    fn require_removal_facts(result: &Value, source_present: bool) {
        assert_eq!(result["method"], "iprange.v1.maintenance.remove");
        assert_eq!(
            result["removal"]["source_present"], source_present,
            "unexpected removal facts: {result}"
        );
        assert_eq!(result["removal"]["cleanup_state"], "clean");
    }

    /// Plants residue of one kind, lists it, and removes the exact bytes the
    /// list published; the artifact must be gone, and the same row must then
    /// report the durable absence.
    fn require_row_roundtrip(kind: &str, plant: &dyn Fn(&std::path::Path) -> std::path::PathBuf) {
        let directory = probe_directory(kind);
        let residue = plant(&directory);
        let rows = listed_rows(&directory, &[kind]);
        assert_eq!(
            rows.len(),
            1,
            "{kind}: list published {rows:?}, want the one planted artifact"
        );
        assert_eq!(published_row(&rows[0])["kind"], kind);
        let result = remove_row(&rows[0]).unwrap_or_else(|error| {
            panic!(
                "{kind}: the unchanged list row {} must be accepted by maintenance.remove: [{}] {}",
                rows[0], error.code, error.message
            )
        });
        require_removal_facts(&result, true);
        assert!(
            !residue.exists(),
            "{kind}: residue {} survived the removal",
            residue.display()
        );
        let result = remove_row(&rows[0]).unwrap_or_else(|error| {
            panic!(
                "{kind}: replaying the row must report the durable absence: [{}] {}",
                error.code, error.message
            )
        });
        require_removal_facts(&result, false);
        assert!(
            listed_rows(&directory, &[kind]).is_empty(),
            "{kind}: the list still reports the removed artifact"
        );
        std::fs::remove_dir_all(&directory).ok();
    }

    /// One row the list emitters produce, with the removal outcome its shape
    /// must produce. `authorized` marks a row that carries the opaque
    /// authenticated removal identity of its kind; only such a row is a
    /// removable entry (iprange-jsonrpc-v1.md, maintenance.list), so only
    /// such a row may reach the SDK.
    struct RowProbe {
        name: &'static str,
        row: Value,
        authorized: bool,
    }

    /// One row per (kind, scan outcome) pair maintenance.list can publish,
    /// produced by the list emitters themselves so a row shape cannot drift
    /// from what the method serves.
    fn emitted_rows(directory: &str) -> Vec<RowProbe> {
        let identity = local_identity(1, 2);
        let artifact = local_identity(3, 4);
        let attempt = maintenance_attempt(0x01);
        let ordinal = 3u32;
        let tuple = PublicationTuple {
            database_id: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16],
            transaction_id: 7,
            commit_nonce: [16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
        };
        let digest = PublicationDigest {
            byte_length: 8192,
            sha512: [0xab; 64],
        };
        let scratch = |authenticated: bool| AbandonedScratchEntry {
            directory_identity: identity,
            artifact_identity: artifact,
            attempt_id: attempt,
            ordinal,
            authentication: if authenticated {
                AbandonedScratchAuthentication::Authenticated(ScratchOwnerKind::Recovery)
            } else {
                AbandonedScratchAuthentication::Unauthenticated
            },
        };
        let evidence = iprange_livedb::publication::AbandonedReservationEvidence {
            policy: iprange_livedb::publication::AbandonedReservationPolicy::ReplaceExisting,
            phase: iprange_livedb::publication::AbandonedReservationPhase::Prepared,
            output: iprange_livedb::publication::PublicationOutputEvidence {
                identity,
                tuple,
                digest: digest.clone(),
            },
            previous: None,
        };
        let reservation =
            |evidence: Option<iprange_livedb::publication::AbandonedReservationEvidence>| {
                AbandonedReservationEntry {
                    directory_identity: identity,
                    artifact_identity: artifact,
                    publication_attempt_id: attempt,
                    evidence,
                }
            };
        let publication_temp = |with_evidence: bool| AbandonedPublicationTempEntry {
            directory_identity: identity,
            artifact_identity: artifact,
            publication_attempt_id: attempt,
            tuple: with_evidence.then_some(tuple),
            digest: with_evidence.then_some(digest.clone()),
        };
        let housekeeping = |authorized: bool| WindowsHousekeepingEntry {
            directory_identity: identity,
            candidate_kind: WindowsHousekeepingCandidateKind::Envelope,
            basename_encoding: 2,
            basename: vec![0x2e, 0x00, 0x74, 0x00].into_boxed_slice(),
            identity: authorized.then_some(artifact),
            attempt_id: authorized.then_some(attempt),
            ordinal: authorized.then_some(ordinal),
            artifact: None,
            problem: None,
        };
        let emit =
            |name: &'static str, value: Result<Value, HandlerError>, authorized: bool| RowProbe {
                name,
                row: value.expect("the list emitter converts its own entry"),
                authorized,
            };
        let mut rows = vec![
            emit(
                "scratch authenticated",
                scratch_entry_value(directory, &scratch(true)),
                true,
            ),
            emit(
                "scratch unauthenticated",
                scratch_entry_value(directory, &scratch(false)),
                true,
            ),
            emit(
                "reservation without evidence",
                reservation_entry_value(directory, &reservation(None)),
                true,
            ),
            emit(
                "reservation with evidence",
                reservation_entry_value(directory, &reservation(Some(evidence.clone()))),
                true,
            ),
            emit(
                "publication_temp without evidence",
                publication_temp_entry_value(directory, &publication_temp(false)),
                true,
            ),
            emit(
                "publication_temp with evidence",
                publication_temp_entry_value(directory, &publication_temp(true)),
                true,
            ),
            emit(
                "windows_housekeeping without the optional members",
                housekeeping_entry_value(directory, &housekeeping(true)),
                true,
            ),
            emit(
                "windows_housekeeping without a removal identity",
                housekeeping_entry_value(directory, &housekeeping(false)),
                false,
            ),
        ];
        // A classified candidate additionally carries the artifact and
        // problem members the emitter names; the removal checks only that
        // each of them is an object.
        let mut classified = housekeeping_entry_value(directory, &housekeeping(true))
            .expect("the list emitter converts its own entry");
        classified["artifact"] = json!({"kind": "private_output"});
        classified["problem"] = json!({"code": "cleanup_conflict"});
        rows.push(emit(
            "windows_housekeeping with every member",
            Ok(classified),
            true,
        ));
        rows
    }

    /// Overwrites one (possibly nested) member of an emitted row.
    fn set_member(row: &mut Value, path: &[&str], value: Value) {
        let (last, parents) = path.split_last().expect("a member path is not empty");
        let mut current = row.as_object_mut().expect("an emitted row is an object");
        for key in parents {
            current = current
                .get_mut(*key)
                .and_then(Value::as_object_mut)
                .unwrap_or_else(|| panic!("member {key:?} of the emitted row is not an object"));
        }
        current.insert((*last).to_owned(), value);
    }

    /// Removes one (possibly nested) member of an emitted row.
    fn drop_member(row: &mut Value, path: &[&str]) {
        let (last, parents) = path.split_last().expect("a member path is not empty");
        let mut current = row.as_object_mut().expect("an emitted row is an object");
        for key in parents {
            current = current
                .get_mut(*key)
                .and_then(Value::as_object_mut)
                .unwrap_or_else(|| panic!("member {key:?} of the emitted row is not an object"));
        }
        assert!(
            current.remove(*last).is_some(),
            "member {last:?} is not part of the emitted row"
        );
    }

    #[test]
    fn list_rows_round_trip_into_remove_for_every_removable_kind() {
        #[cfg(unix)]
        require_row_roundtrip("scratch", &|directory| {
            authenticated_scratch_artifact(directory, maintenance_attempt(0xa1), 7)
        });
        require_row_roundtrip("reservation", &|directory| {
            partial_private_artifact(
                directory,
                ".iprange-reservation-",
                maintenance_attempt(0xb2),
            )
        });
        require_row_roundtrip("publication_temp", &|directory| {
            partial_private_artifact(directory, ".iprange-publish-", maintenance_attempt(0xc3))
        });
    }

    #[test]
    fn one_list_covers_every_kind_and_every_row_stays_removable() {
        let directory = probe_directory("all-kinds");
        let mut planted: Vec<(&str, std::path::PathBuf)> = Vec::new();
        #[cfg(unix)]
        planted.push((
            "scratch",
            authenticated_scratch_artifact(&directory, maintenance_attempt(0xd1), 3),
        ));
        planted.push((
            "reservation",
            partial_private_artifact(
                &directory,
                ".iprange-reservation-",
                maintenance_attempt(0xe2),
            ),
        ));
        planted.push((
            "publication_temp",
            partial_private_artifact(&directory, ".iprange-publish-", maintenance_attempt(0xf3)),
        ));
        let kinds: Vec<&str> = planted.iter().map(|(kind, _)| *kind).collect();
        let rows = listed_rows(&directory, &kinds);
        assert_eq!(
            rows.len(),
            planted.len(),
            "list published {rows:?}, want one row per kind"
        );
        for (index, (kind, _)) in planted.iter().enumerate() {
            assert_eq!(published_row(&rows[index])["kind"], *kind);
        }
        // The unreadable reservation and the partial publication output are
        // reported without their optional evidence members.
        let index = planted
            .iter()
            .position(|(kind, _)| *kind == "reservation")
            .expect("the reservation kind is planted");
        assert!(
            published_row(&rows[index]).get("evidence").is_none(),
            "the reservation row of an unreadable record must omit evidence: {}",
            rows[index]
        );
        let index = planted
            .iter()
            .position(|(kind, _)| *kind == "publication_temp")
            .expect("the publication_temp kind is planted");
        let partial = published_row(&rows[index]);
        assert!(
            partial.get("tuple").is_none() && partial.get("digest").is_none(),
            "the publication_temp row of partial content must omit tuple and digest: {}",
            rows[index]
        );
        for (index, (kind, path)) in planted.iter().enumerate() {
            let result = remove_row(&rows[index]).unwrap_or_else(|error| {
                panic!(
                    "{kind}: the unchanged list row {} must be accepted by maintenance.remove: [{}] {}",
                    rows[index], error.code, error.message
                )
            });
            require_removal_facts(&result, true);
            assert!(
                !path.exists(),
                "{kind}: residue {} survived the removal",
                path.display()
            );
        }
        assert!(listed_rows(&directory, &kinds).is_empty());
        std::fs::remove_dir_all(&directory).ok();
    }

    #[test]
    fn remove_accepts_every_removable_row_the_list_can_publish() {
        let directory = probe_directory("emitted-rows");
        let directory_name = directory.display().to_string();
        for probe in emitted_rows(&directory_name) {
            let row = probe.row.to_string();
            let error = match remove_row(&row) {
                Ok(result) => {
                    assert!(
                        probe.authorized,
                        "{}: a row without its authenticated removal identity must be refused before any destructive step",
                        probe.name
                    );
                    require_removal_facts(&result, false);
                    continue;
                }
                Err(error) => error,
            };
            if !probe.authorized {
                assert_eq!(
                    error.code, "invalid_argument",
                    "{}: a row without its authenticated removal identity must be refused before any destructive step",
                    probe.name
                );
                continue;
            }
            assert_ne!(
                error.code, "invalid_argument",
                "{}: maintenance.remove refused a removable row maintenance.list emits: {row}: [{}] {}",
                probe.name, error.code, error.message
            );
            if row.contains("windows_housekeeping") {
                if cfg!(windows) {
                    continue;
                }
                assert_eq!(
                    error.code, "os_unsupported",
                    "{}: the housekeeping removal must reach the platform refusal",
                    probe.name
                );
                continue;
            }
            assert_eq!(
                error.code, "directory_identity_mismatch",
                "{}: the removal must reach the SDK identity proof",
                probe.name
            );
        }
        std::fs::remove_dir_all(&directory).ok();
    }

    #[test]
    fn remove_still_validates_every_present_member() {
        let directory = probe_directory("present-members");
        let directory_name = directory.display().to_string();
        let emitted: std::collections::HashMap<&str, Value> = emitted_rows(&directory_name)
            .into_iter()
            .map(|probe| (probe.name, probe.row))
            .collect();
        let template = |name: &str| emitted[name].clone();
        // One emitted row with members overwritten (`set`) and removed
        // (`drop`), so each probe differs from a published row in exactly
        // the one way the case names.
        let mutate = |name: &str, set: &[(&[&str], Value)], drop: &[&[&str]]| {
            let mut row = template(name);
            for item in set {
                set_member(&mut row, item.0, item.1.clone());
            }
            for path in drop {
                drop_member(&mut row, path);
            }
            row
        };
        let cases: Vec<(&str, Value)> = vec![
            (
                "publication_temp tuple without digest",
                mutate("publication_temp with evidence", &[], &[&["digest"]]),
            ),
            (
                "publication_temp digest without tuple",
                mutate("publication_temp with evidence", &[], &[&["tuple"]]),
            ),
            (
                "publication_temp null tuple",
                mutate(
                    "publication_temp without evidence",
                    &[(&["tuple"], Value::Null)],
                    &[],
                ),
            ),
            (
                "publication_temp malformed digest",
                mutate(
                    "publication_temp with evidence",
                    &[(&["digest", "sha512"], json!("not-a-digest"))],
                    &[],
                ),
            ),
            (
                "publication_temp unknown member",
                mutate(
                    "publication_temp without evidence",
                    &[(&["basename"], json!("synthesized"))],
                    &[],
                ),
            ),
            (
                "publication_temp without artifact identity",
                mutate(
                    "publication_temp without evidence",
                    &[],
                    &[&["artifact_identity"]],
                ),
            ),
            (
                "reservation null evidence",
                mutate(
                    "reservation without evidence",
                    &[(&["evidence"], Value::Null)],
                    &[],
                ),
            ),
            (
                "reservation evidence with unknown member",
                mutate(
                    "reservation with evidence",
                    &[(&["evidence", "forged"], json!(true))],
                    &[],
                ),
            ),
            (
                "reservation evidence without output",
                mutate("reservation with evidence", &[], &[&["evidence", "output"]]),
            ),
            (
                "reservation evidence with non-canonical byte_length",
                mutate(
                    "reservation with evidence",
                    &[(
                        &["evidence", "output", "digest", "byte_length"],
                        json!("08192"),
                    )],
                    &[],
                ),
            ),
            (
                "scratch without authentication",
                mutate("scratch authenticated", &[], &[&["authentication"]]),
            ),
            (
                "scratch with a forged owner",
                mutate(
                    "scratch authenticated",
                    &[(&["authentication", "owner"], json!("attacker"))],
                    &[],
                ),
            ),
        ];
        for (name, row) in cases {
            let row = row.to_string();
            match remove_row(&row) {
                Err(error) if error.code == "invalid_argument" => {}
                Err(error) => panic!(
                    "{name}: must be refused with invalid_argument, got [{}] {}",
                    error.code, error.message
                ),
                Ok(result) => {
                    panic!("{name}: must be refused before any destructive step, got {result}")
                }
            }
        }
        std::fs::remove_dir_all(&directory).ok();
    }

    #[test]
    fn scratch_header_checksum_matches_the_recorded_crc32c_vector() {
        // The residue fixture is only trustworthy if its checksum is the
        // recorded CRC-32C: the canonical check value of "123456789" is
        // 0xE3069283.
        assert_eq!(scratch_checksum(b"123456789", 9, 0), 0xe306_9283);
    }
}
