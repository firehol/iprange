//! Opaque factual reports and fixed report projections.

use std::cell::UnsafeCell;
use std::mem::size_of;
use std::sync::atomic::AtomicBool;

use iprange_livedb::publication::{CleanupState, CoordinationCleanup};
use iprange_livedb::{
    AbortOutcome, AbortResult, CloseOutcome, CloseResult, CommitCleanupArtifact, CommitDurability,
    CommitResult, LogicalChange, ReclaimResult, WorkflowKind, WorkflowReport,
};

use crate::abi::{
    AbortReport, Cardinality129, CleanupArtifact, CloseReport, CommitReport, FinishInputReport,
    LocalBasename, LocalIdentity, ReclaimReport, ScanReport, STATUS_OK,
};
use crate::abi_extra::{
    CommitResolutionReport, CreateReport, HousekeepingArtifact, LiveResidueReport,
    LiveTransitionReport, PublicationReport, RecoveryCandidatesReport, RecoveryReport,
    ResidueReport, ValidationReport,
};
use crate::abi_sdk::{HistoryProjectionReport, HistoryWindowReport};
use crate::error::{
    call, call_with_output, required_output, BoundaryError, CallError, ErrorHandle,
};
use crate::handle::{Gate, Header, OpaqueHandle, REPORT_KIND};
use crate::registry;

const FINISH_INPUT_KIND: u32 = registry::REPORT_KIND_FINISH_INPUT;
const COMMIT_KIND: u32 = registry::REPORT_KIND_COMMIT;
const ABORT_KIND: u32 = registry::REPORT_KIND_ABORT;
const CLOSE_KIND: u32 = registry::REPORT_KIND_CLOSE;
const RECLAIM_KIND: u32 = registry::REPORT_KIND_RECLAIM;
const SCAN_KIND: u32 = registry::REPORT_KIND_SCAN;
const COMMIT_RESOLUTION_KIND: u32 = registry::REPORT_KIND_COMMIT_RESOLUTION;
const CREATE_KIND: u32 = registry::REPORT_KIND_CREATE;
const LIVE_TRANSITION_KIND: u32 = registry::REPORT_KIND_LIVE_TRANSITION;
const CREATE_RESOLUTION_KIND: u32 = registry::REPORT_KIND_CREATE_RESOLUTION;
const LIVE_TRANSITION_RESOLUTION_KIND: u32 = registry::REPORT_KIND_LIVE_TRANSITION_RESOLUTION;
const PUBLICATION_KIND: u32 = registry::REPORT_KIND_PUBLICATION;
const VALIDATION_KIND: u32 = registry::REPORT_KIND_VALIDATION;
const RECOVERY_CANDIDATES_KIND: u32 = registry::REPORT_KIND_RECOVERY_CANDIDATES;
const RECOVERY_KIND: u32 = registry::REPORT_KIND_RECOVERY;
const RESIDUE_KIND: u32 = registry::REPORT_KIND_RESIDUE;
const LIVE_RESIDUE_KIND: u32 = registry::REPORT_KIND_LIVE_RESIDUE;
const HISTORY_PROJECTION_KIND: u32 = registry::REPORT_KIND_HISTORY_PROJECTION;

/// Opaque owned variable operation report.
#[repr(C)]
#[derive(Debug)]
pub struct ReportHandle {
    header: Header,
    busy: AtomicBool,
    pub(crate) body: Body,
    pub(crate) cause: Option<Box<ErrorHandle>>,
    pub(crate) cleanup: Vec<CleanupArtifact>,
    pub(crate) housekeeping: Vec<HousekeepingArtifact>,
    pub(crate) candidates: Vec<iprange_livedb::recovery::RecoveryCandidate>,
    obligations: UnsafeCell<ReportObligations>,
    pub(crate) commit_original: Option<CommitResult>,
    pub(crate) create_original: Option<iprange_livedb::CreateResult>,
    pub(crate) transition_original: Option<iprange_livedb::LiveTransitionResult>,
    pub(crate) publication_original: Option<iprange_livedb::publication::PublicationResult>,
}

#[derive(Debug, Default)]
struct ReportObligations {
    cleanup_guard: Option<iprange_livedb::recovery::RecoverySourceCleanupGuard>,
    residue: Option<iprange_livedb::PublicationResidueHandle>,
}

// Only immutable facts are read without mutation; obligations are protected by
// the fail-fast gate.
unsafe impl Send for ReportHandle {}
unsafe impl Sync for ReportHandle {}

unsafe impl OpaqueHandle for ReportHandle {
    const KIND: u32 = REPORT_KIND;
}

#[derive(Debug)]
pub(crate) enum Body {
    Scan(ScanReport),
    FinishInput(FinishInputReport),
    Commit(CommitReport),
    CommitResolution(CommitResolutionReport),
    Abort(AbortReport),
    Close(CloseReport),
    Reclaim(ReclaimReport),
    Create(CreateReport),
    LiveTransition(LiveTransitionReport),
    CreateResolution(CreateReport),
    LiveTransitionResolution(LiveTransitionReport),
    Publication(PublicationReport),
    Validation(ValidationReport),
    RecoveryCandidates(RecoveryCandidatesReport),
    Recovery(RecoveryReport),
    Residue(ResidueReport),
    LiveResidue(LiveResidueReport),
    HistoryProjection {
        fixed: HistoryProjectionReport,
        windows: Box<[HistoryWindowReport]>,
    },
}

impl ReportHandle {
    pub(crate) fn new(body: Body) -> Self {
        Self {
            header: Header::new(REPORT_KIND),
            busy: AtomicBool::new(false),
            body,
            cause: None,
            cleanup: Vec::new(),
            housekeeping: Vec::new(),
            candidates: Vec::new(),
            obligations: UnsafeCell::new(ReportObligations::default()),
            commit_original: None,
            create_original: None,
            transition_original: None,
            publication_original: None,
        }
    }

    pub(crate) fn scan(record_count: u64, completed: bool) -> Self {
        Self::new(Body::Scan(ScanReport {
            abi_version: 1,
            struct_size: size_of::<ScanReport>() as u32,
            record_count,
            completed: u8::from(completed),
            reserved: [0; 7],
        }))
    }

    pub(crate) fn finish_input(report: WorkflowReport) -> Self {
        Self::new(Body::FinishInput(encode_finish(report)))
    }

    pub(crate) fn history_projection(report: iprange_livedb::HistoryProjectionReport) -> Self {
        let windows = report
            .windows
            .iter()
            .copied()
            .map(encode_history_window)
            .collect();
        let fixed = HistoryProjectionReport {
            abi_version: 1,
            struct_size: size_of::<HistoryProjectionReport>() as u32,
            logical_change: match report.logical_change {
                LogicalChange::Changed => registry::LOGICAL_CHANGE_CHANGED,
                LogicalChange::NoChange => registry::LOGICAL_CHANGE_NO_CHANGE,
            },
            reserved: 0,
            source_range_count: report.source_range_count,
            source_addresses: cardinality(report.source_addresses),
            created_feed_count: report.created_feed_count,
            before_interval_count: report.before_interval_count,
            after_interval_count: report.after_interval_count,
            before_addresses: cardinality(report.before_addresses),
            after_addresses: cardinality(report.after_addresses),
            unchanged_addresses: cardinality(report.unchanged_addresses),
            added_addresses: cardinality(report.added_addresses),
            removed_addresses: cardinality(report.removed_addresses),
            window_count: report.windows.len() as u64,
        };
        Self::new(Body::HistoryProjection { fixed, windows })
    }

    pub(crate) fn commit(mut result: CommitResult) -> Self {
        let cleanup = result.cleanup.iter().map(encode_cleanup).collect();
        let body = encode_commit(&result);
        let cause = result.cause.take().map(|cause| Box::new(cause.into()));
        let mut report = Self::new(Body::Commit(body));
        report.cause = cause;
        report.cleanup = cleanup;
        report.commit_original = Some(result);
        report
    }

    pub(crate) fn abort(result: AbortResult) -> Self {
        let cleanup = result.cleanup.iter().map(encode_cleanup).collect();
        let cleanup_state = cleanup_state(result.cleanup_state());
        let cause = result.cause.map(|cause| Box::new(cause.into()));
        let mut report = Self::new(Body::Abort(AbortReport {
            abi_version: 1,
            struct_size: size_of::<AbortReport>() as u32,
            outcome: abort_outcome(result.outcome),
            cleanup_state,
            coordination_cleanup: coordination_cleanup(result.coordination_cleanup),
            reserved: 0,
        }));
        report.cause = cause;
        report.cleanup = cleanup;
        report
    }

    pub(crate) fn close(result: CloseResult) -> Self {
        let cleanup = result.cleanup.iter().map(encode_cleanup).collect();
        let cleanup_state = cleanup_state(result.cleanup_state());
        let cause = result.cause.map(|cause| Box::new(cause.into()));
        let mut report = Self::new(Body::Close(CloseReport {
            abi_version: 1,
            struct_size: size_of::<CloseReport>() as u32,
            outcome: match result.outcome {
                CloseOutcome::Closed => registry::CLOSE_OUTCOME_CLOSED,
                CloseOutcome::CloseIncomplete => registry::CLOSE_OUTCOME_INCOMPLETE,
            },
            abort_present: u8::from(result.abort_outcome.is_some()),
            reserved0: [0; 3],
            abort_outcome: result.abort_outcome.map_or(0, abort_outcome),
            cleanup_state,
            coordination_cleanup: coordination_cleanup(result.coordination_cleanup),
            reserved1: 0,
        }));
        report.cause = cause;
        report.cleanup = cleanup;
        report
    }

    pub(crate) fn reclaim(result: ReclaimResult) -> Self {
        match result {
            ReclaimResult::NoChange => Self::new(Body::Reclaim(ReclaimReport {
                abi_version: 1,
                struct_size: size_of::<ReclaimReport>() as u32,
                ..ReclaimReport::default()
            })),
            ReclaimResult::Commit {
                transaction_count,
                page_count,
                commit,
            } => {
                let cleanup = commit.cleanup.iter().map(encode_cleanup).collect();
                let commit_report = encode_commit(&commit);
                let cause = commit.cause.map(|cause| Box::new(cause.into()));
                let mut report = Self::new(Body::Reclaim(ReclaimReport {
                    abi_version: 1,
                    struct_size: size_of::<ReclaimReport>() as u32,
                    changed: 1,
                    reserved0: [0; 7],
                    transaction_count,
                    page_count,
                    commit: commit_report,
                }));
                report.cause = cause;
                report.cleanup = cleanup;
                report
            }
        }
    }

    fn kind(&self) -> u32 {
        match self.body {
            Body::Scan(_) => SCAN_KIND,
            Body::FinishInput(_) => FINISH_INPUT_KIND,
            Body::Commit(_) => COMMIT_KIND,
            Body::CommitResolution(_) => COMMIT_RESOLUTION_KIND,
            Body::Abort(_) => ABORT_KIND,
            Body::Close(_) => CLOSE_KIND,
            Body::Reclaim(_) => RECLAIM_KIND,
            Body::Create(_) => CREATE_KIND,
            Body::LiveTransition(_) => LIVE_TRANSITION_KIND,
            Body::CreateResolution(_) => CREATE_RESOLUTION_KIND,
            Body::LiveTransitionResolution(_) => LIVE_TRANSITION_RESOLUTION_KIND,
            Body::Publication(_) => PUBLICATION_KIND,
            Body::Validation(_) => VALIDATION_KIND,
            Body::RecoveryCandidates(_) => RECOVERY_CANDIDATES_KIND,
            Body::Recovery(_) => RECOVERY_KIND,
            Body::Residue(_) => RESIDUE_KIND,
            Body::LiveResidue(_) => LIVE_RESIDUE_KIND,
            Body::HistoryProjection { .. } => HISTORY_PROJECTION_KIND,
        }
    }

    pub(crate) fn require(&self) -> Result<(), BoundaryError> {
        self.header.require(REPORT_KIND)
    }

    pub(crate) fn enter(&self) -> Result<Gate<'_>, BoundaryError> {
        self.require()?;
        Gate::enter(&self.busy)
    }

    pub(crate) fn set_residue(
        &mut self,
        residue: Option<iprange_livedb::PublicationResidueHandle>,
    ) {
        self.obligations.get_mut().residue = residue;
    }

    pub(crate) fn set_cleanup_guard(
        &mut self,
        guard: Option<iprange_livedb::recovery::RecoverySourceCleanupGuard>,
    ) {
        self.obligations.get_mut().cleanup_guard = guard;
    }

    pub(crate) fn take_cleanup_guard(
        &self,
    ) -> Option<iprange_livedb::recovery::RecoverySourceCleanupGuard> {
        // SAFETY: callers hold this report's fail-fast gate.
        unsafe { &mut *self.obligations.get() }.cleanup_guard.take()
    }

    pub(crate) fn take_residue(&self) -> Option<iprange_livedb::PublicationResidueHandle> {
        // SAFETY: callers hold this report's fail-fast gate.
        unsafe { &mut *self.obligations.get() }.residue.take()
    }

    fn has_obligations(&self) -> bool {
        // SAFETY: callers hold this report's fail-fast gate.
        let obligations = unsafe { &*self.obligations.get() };
        obligations.cleanup_guard.is_some() || obligations.residue.is_some()
    }

    pub(crate) fn commit_attempt(&self) -> Result<&CommitResult, BoundaryError> {
        self.require()?;
        self.commit_original
            .as_ref()
            .ok_or_else(|| BoundaryError::wrong_state("report is not a commit attempt"))
    }

    pub(crate) fn create_attempt(&self) -> Result<&iprange_livedb::CreateResult, BoundaryError> {
        self.require()?;
        self.create_original
            .as_ref()
            .ok_or_else(|| BoundaryError::wrong_state("report is not a creation attempt"))
    }

    pub(crate) fn transition_attempt(
        &self,
    ) -> Result<&iprange_livedb::LiveTransitionResult, BoundaryError> {
        self.require()?;
        self.transition_original
            .as_ref()
            .ok_or_else(|| BoundaryError::wrong_state("report is not a live transition attempt"))
    }

    pub(crate) fn publication_attempt(
        &self,
    ) -> Result<&iprange_livedb::publication::PublicationResult, BoundaryError> {
        self.require()?;
        self.publication_original
            .as_ref()
            .ok_or_else(|| BoundaryError::wrong_state("report has no publication attempt"))
    }

    pub(crate) fn candidate(
        &self,
        index: u64,
    ) -> Result<iprange_livedb::recovery::RecoveryCandidate, BoundaryError> {
        self.require()?;
        self.candidates
            .get(index as usize)
            .copied()
            .ok_or_else(|| BoundaryError::invalid_argument("candidate index is invalid"))
    }
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_kind(
    report: *const ReportHandle,
    kind: *mut u32,
) -> u32 {
    call_with_output(
        std::ptr::null_mut(),
        kind,
        "report kind output is null",
        || {
            // SAFETY: both pointers are validated before use.
            let report =
                unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
            let _guard = report.enter()?;
            let kind = unsafe { required_output(kind, "report kind output is null")? };
            *kind = report.kind();
            Ok::<_, BoundaryError>(())
        },
    )
}

pub(crate) fn fixed_getter<T: Copy + Default>(
    report: *const ReportHandle,
    output: *mut T,
    error_output: *mut *mut ErrorHandle,
    select: impl FnOnce(&Body) -> Option<&T>,
) -> u32 {
    call_with_output(error_output, output, "report output is null", || {
        // SAFETY: the output pointer is validated before any input work.
        let output = unsafe { required_output(output, "report output is null")? };
        *output = T::default();
        // SAFETY: the opaque pointer is kind-checked before typed use.
        let report =
            unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
        let _guard = report.enter()?;
        let value = select(&report.body)
            .ok_or_else(|| BoundaryError::wrong_state("report kind does not match getter"))?;
        *output = *value;
        Ok::<_, BoundaryError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_scan(
    report: *const ReportHandle,
    output: *mut ScanReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    fixed_getter(report, output, error_output, |body| match body {
        Body::Scan(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_finish_input(
    report: *const ReportHandle,
    output: *mut FinishInputReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    fixed_getter(report, output, error_output, |body| match body {
        Body::FinishInput(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_history_projection(
    report: *const ReportHandle,
    output: *mut HistoryProjectionReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    fixed_getter(report, output, error_output, |body| match body {
        Body::HistoryProjection { fixed, .. } => Some(fixed),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_history_window(
    report: *const ReportHandle,
    index: u64,
    output: *mut HistoryWindowReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        output,
        "history window output is null",
        || {
            // SAFETY: both pointers are validated before use.
            let output = unsafe { required_output(output, "history window output is null")? };
            *output = HistoryWindowReport::default();
            let report =
                unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
            let _guard = report.enter()?;
            let Body::HistoryProjection { windows, .. } = &report.body else {
                return Err(BoundaryError::wrong_state(
                    "report is not a history projection",
                ));
            };
            *output = *windows.get(index as usize).ok_or_else(|| {
                BoundaryError::invalid_argument("history window index is invalid")
            })?;
            Ok::<_, BoundaryError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_commit(
    report: *const ReportHandle,
    output: *mut CommitReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    fixed_getter(report, output, error_output, |body| match body {
        Body::Commit(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_abort(
    report: *const ReportHandle,
    output: *mut AbortReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    fixed_getter(report, output, error_output, |body| match body {
        Body::Abort(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_close(
    report: *const ReportHandle,
    output: *mut CloseReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    fixed_getter(report, output, error_output, |body| match body {
        Body::Close(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_reclaim(
    report: *const ReportHandle,
    output: *mut ReclaimReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    fixed_getter(report, output, error_output, |body| match body {
        Body::Reclaim(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_cleanup_artifact_count(
    report: *const ReportHandle,
    count: *mut u64,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, count, "artifact count output is null", || {
        // SAFETY: both pointers are validated before use.
        let report =
            unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
        let _guard = report.enter()?;
        let count = unsafe { required_output(count, "artifact count output is null")? };
        *count = report.cleanup.len() as u64;
        Ok::<_, BoundaryError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_cleanup_artifact_get(
    report: *const ReportHandle,
    index: u64,
    output: *mut CleanupArtifact,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        output,
        "cleanup artifact output is null",
        || {
            // SAFETY: both pointers are validated before use.
            let report =
                unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
            let _guard = report.enter()?;
            let output = unsafe { required_output(output, "cleanup artifact output is null")? };
            *output = *report.cleanup.get(index as usize).ok_or_else(|| {
                BoundaryError::invalid_argument("cleanup artifact index is invalid")
            })?;
            Ok::<_, BoundaryError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_cause(
    report: *const ReportHandle,
    cause: *mut *const ErrorHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, cause, "report cause output is null", || {
        // SAFETY: both pointers are validated before use.
        let report =
            unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
        let _guard = report.enter()?;
        let cause = unsafe { required_output(cause, "report cause output is null")? };
        *cause = report
            .cause
            .as_deref()
            .map_or(std::ptr::null(), |cause| cause);
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_destroy(
    report: *mut ReportHandle,
    _error_output: *mut *mut ErrorHandle,
) -> u32 {
    if report.is_null() {
        return STATUS_OK;
    }
    call(_error_output, || {
        // SAFETY: the handle is validated before ownership is consumed.
        let value =
            unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
        let guard = value.enter()?;
        if value.has_obligations() {
            return Err(BoundaryError::handle_busy(
                "report still owns an untaken cleanup or residue handle",
            ));
        }
        drop(guard);
        // SAFETY: ownership was created by this ABI and is consumed exactly once.
        unsafe { drop(Box::from_raw(report)) };
        Ok::<_, BoundaryError>(())
    })
}

fn encode_finish(report: WorkflowReport) -> FinishInputReport {
    FinishInputReport {
        abi_version: 1,
        struct_size: size_of::<FinishInputReport>() as u32,
        workflow: match report.workflow {
            WorkflowKind::CreateFeed => registry::WORKFLOW_CREATE_FEED,
            WorkflowKind::ReplaceFeed => registry::WORKFLOW_REPLACE_FEED,
            WorkflowKind::DirectReplacement => registry::WORKFLOW_DIRECT_REPLACEMENT,
            WorkflowKind::FirstSeenRefresh => registry::WORKFLOW_FIRST_SEEN_REFRESH,
            WorkflowKind::LastSeenRefresh => registry::WORKFLOW_LAST_SEEN_REFRESH,
            WorkflowKind::MembershipImport => registry::WORKFLOW_MEMBERSHIP_IMPORT,
        },
        logical_change: match report.logical_change {
            LogicalChange::Changed => registry::LOGICAL_CHANGE_CHANGED,
            LogicalChange::NoChange => registry::LOGICAL_CHANGE_NO_CHANGE,
        },
        input_record_count: report.input_record_count,
        input_normalized_interval_count: report.input_normalized_interval_count,
        before_range_record_count: report.before_range_record_count,
        after_range_record_count: report.after_range_record_count,
        input_addresses: cardinality(report.input_addresses),
        before_addresses: cardinality(report.before_addresses),
        after_addresses: cardinality(report.after_addresses),
        unchanged_value_addresses: cardinality(report.unchanged_value_addresses),
        changed_value_addresses: cardinality(report.changed_value_addresses),
        added_addresses: cardinality(report.added_addresses),
        removed_addresses: cardinality(report.removed_addresses),
        source_feed_count: report.source_feed_count,
        matched_feed_count: report.matched_feed_count,
        created_feed_count: report.created_feed_count,
        source_distinct_membership_count: report.source_distinct_membership_count,
        translated_membership_count: report.translated_membership_count,
    }
}

fn encode_commit(result: &CommitResult) -> CommitReport {
    CommitReport {
        abi_version: 1,
        struct_size: size_of::<CommitReport>() as u32,
        attempted_database_id: result.attempted_database_id,
        directory_identity: identity(result.directory_identity),
        main_identity: identity(result.main_identity),
        attempted_transaction_id: result.attempted_transaction_id,
        attempted_commit_nonce: result.attempted_commit_nonce,
        durability: match result.durability {
            CommitDurability::NotCommitted => registry::COMMIT_DURABILITY_NOT_COMMITTED,
            CommitDurability::Committed => registry::COMMIT_DURABILITY_COMMITTED,
            CommitDurability::OutcomeUnknown => registry::COMMIT_DURABILITY_OUTCOME_UNKNOWN,
        },
        cleanup_state: cleanup_state(result.cleanup_state()),
        coordination_cleanup: coordination_cleanup(result.coordination_cleanup),
        reserved: 0,
    }
}

pub(crate) fn encode_cleanup(input: &CommitCleanupArtifact) -> CleanupArtifact {
    let mut basename = LocalBasename {
        encoding: u32::from(input.main_basename.encoding()),
        length: input.main_basename.as_bytes().len() as u32,
        ..LocalBasename::default()
    };
    basename.bytes[..input.main_basename.as_bytes().len()]
        .copy_from_slice(input.main_basename.as_bytes());
    CleanupArtifact {
        abi_version: 1,
        struct_size: size_of::<CleanupArtifact>() as u32,
        kind: registry::ARTIFACT_KIND_UNPUBLISHED_MAIN_TAIL,
        directory_role: registry::DIRECTORY_ROLE_MAIN_FILE,
        directory_identity: identity(input.directory_identity),
        basename,
        artifact_identity_present: 1,
        creation_security_present: 0,
        reserved0: [0; 6],
        artifact_identity: identity(input.main_identity),
        creation_security_kind: 0,
        reserved1: 0,
        creation_security_commitment: [0; 32],
        unpublished_tail_present: 1,
        reserved2: [0; 7],
        expected_database_id: input.expected_database_id,
        transaction_id: input.target_transaction_id,
        commit_nonce: input.target_commit_nonce,
        expected_length: input.committed_target_length,
        observed_end_exclusive: input.observed_tail_end_exclusive.unwrap_or(0),
        error_code: input.cleanup_error as u32,
        error_os_code_present: 0,
        reserved3: [0; 3],
        error_os_code: 0,
        reserved4: 0,
    }
}

pub(crate) fn cardinality(value: iprange_livedb::Cardinality129) -> Cardinality129 {
    Cardinality129 {
        bit128: value.bit128(),
        reserved: [0; 7],
        hi: value.hi(),
        lo: value.lo(),
    }
}

fn encode_history_window(report: iprange_livedb::HistoryWindowReport) -> HistoryWindowReport {
    HistoryWindowReport {
        feed_name: crate::query::encode_name(report.feed_name),
        cutoff: report.cutoff,
        created: u8::from(report.created),
        reserved: [0; 3],
        before_interval_count: report.before_interval_count,
        after_interval_count: report.after_interval_count,
        before_addresses: cardinality(report.before_addresses),
        after_addresses: cardinality(report.after_addresses),
        unchanged_addresses: cardinality(report.unchanged_addresses),
        added_addresses: cardinality(report.added_addresses),
        removed_addresses: cardinality(report.removed_addresses),
    }
}

fn identity(value: iprange_livedb::validation::LocalFileIdentity) -> LocalIdentity {
    LocalIdentity {
        kind: u32::from(value.kind),
        reserved: 0,
        bytes: value.bytes,
    }
}

fn cleanup_state(value: CleanupState) -> u32 {
    match value {
        CleanupState::Clean => registry::CLEANUP_STATE_CLEAN,
        CleanupState::ResiduePossible => registry::CLEANUP_STATE_RESIDUE_POSSIBLE,
    }
}

fn coordination_cleanup(value: CoordinationCleanup) -> u32 {
    match value {
        CoordinationCleanup::None => registry::COORDINATION_CLEANUP_NONE,
        CoordinationCleanup::CleanupGuard => registry::COORDINATION_CLEANUP_GUARD,
        CoordinationCleanup::RetainedReaderCloseRequired => {
            registry::COORDINATION_CLEANUP_RETAINED_READER_CLOSE_REQUIRED
        }
        CoordinationCleanup::RetainedWriterCloseRequired => {
            registry::COORDINATION_CLEANUP_RETAINED_WRITER_CLOSE_REQUIRED
        }
    }
}

fn abort_outcome(value: AbortOutcome) -> u32 {
    match value {
        AbortOutcome::Aborted => registry::ABORT_OUTCOME_ABORTED,
        AbortOutcome::AbortIncomplete => registry::ABORT_OUTCOME_ABORT_INCOMPLETE,
    }
}
