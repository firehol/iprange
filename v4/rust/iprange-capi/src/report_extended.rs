//! Lifecycle, publication, validation, and recovery report projections.

use std::mem::size_of;

use iprange_livedb::publication;
use iprange_livedb::recovery;
use iprange_livedb::validation;
use iprange_livedb::{
    CommitResolution, CommitResolutionResult, CreateResult, CreationState, LiveResidueKind,
    LiveResidueResult, LiveResidueStatus, LiveTransitionOperation, LiveTransitionResult,
    LiveTransitionStatus, LocalFileRelation,
};

use crate::abi_extra::{
    CommitResolutionReport, CreateReport, HousekeepingArtifact, LiveResidueReport,
    LiveTransitionReport, PublicationReport, RecoveryCandidate, RecoveryCandidatesReport,
    RecoveryReport, ResidueReport, ValidationReport,
};
use crate::error::{call_with_output, required_output, BoundaryError, ErrorHandle};
use crate::facts;
use crate::obligation::{CleanupGuardHandle, ResidueHandle};
use crate::registry;
use crate::report::{Body, ReportHandle};

impl ReportHandle {
    pub(crate) fn create(mut result: CreateResult, resolution: bool) -> Self {
        let body = encode_create(&result);
        let cause = result.cause.take().map(|cause| Box::new(cause.into()));
        let housekeeping = result
            .visible_housekeeping
            .iter()
            .map(facts::housekeeping_artifact)
            .collect();
        let mut report = Self::new(if resolution {
            Body::CreateResolution(body)
        } else {
            Body::Create(body)
        });
        report.cause = cause;
        report.housekeeping = housekeeping;
        report.create_original = Some(result);
        report
    }

    pub(crate) fn live_transition(mut result: LiveTransitionResult, resolution: bool) -> Self {
        let body = encode_live_transition(&result);
        let cause = result.cause.take().map(|cause| Box::new(cause.into()));
        let housekeeping = result
            .visible_housekeeping
            .iter()
            .map(facts::housekeeping_artifact)
            .collect();
        let mut report = Self::new(if resolution {
            Body::LiveTransitionResolution(body)
        } else {
            Body::LiveTransition(body)
        });
        report.cause = cause;
        report.housekeeping = housekeeping;
        report.transition_original = Some(result);
        report
    }

    pub(crate) fn live_residue(mut result: LiveResidueResult) -> Self {
        let body = LiveResidueReport {
            abi_version: 1,
            struct_size: size_of::<LiveResidueReport>() as u32,
            status: live_residue_status(result.status),
            kind: result.kind.map_or(0, live_residue_kind),
            database_id: facts::optional_bytes16(result.database_id),
            sidecar_id: facts::optional_bytes16(result.sidecar_id),
            reader_capacity: facts::optional_u32(result.reader_capacity),
            main_identity: facts::optional_identity(result.main_identity),
            sidecar_identity: facts::optional_identity(result.sidecar_identity),
            residue_possible: u8::from(result.residue_possible),
            reserved: [0; 3],
            housekeeping: facts::housekeeping(result.housekeeping),
        };
        let cause = result.cause.take().map(|cause| Box::new(cause.into()));
        let housekeeping = result
            .visible_housekeeping
            .iter()
            .map(facts::housekeeping_artifact)
            .collect();
        let mut report = Self::new(Body::LiveResidue(body));
        report.cause = cause;
        report.housekeeping = housekeeping;
        report
    }

    pub(crate) fn commit_resolution(mut result: CommitResolutionResult) -> Self {
        let body = CommitResolutionReport {
            abi_version: 1,
            struct_size: size_of::<CommitResolutionReport>() as u32,
            attempted_database_id: result.attempted_database_id,
            attempted_transaction_id: result.attempted_transaction_id,
            attempted_commit_nonce: result.attempted_commit_nonce,
            actual_directory_identity: facts::identity(result.actual_directory_identity),
            actual_main_identity: facts::identity(result.actual_main_identity),
            local_file_relation: match result.local_file_relation {
                LocalFileRelation::SameLocalFile => registry::LOCAL_FILE_RELATION_SAME_LOCAL_FILE,
                LocalFileRelation::DifferentLocalFile => {
                    registry::LOCAL_FILE_RELATION_DIFFERENT_LOCAL_FILE
                }
            },
            resolution: match result.resolution {
                CommitResolution::Committed => registry::COMMIT_RESOLUTION_COMMITTED,
                CommitResolution::NotCommitted => registry::COMMIT_RESOLUTION_NOT_COMMITTED,
                CommitResolution::SupersededUnknown => {
                    registry::COMMIT_RESOLUTION_SUPERSEDED_UNKNOWN
                }
                CommitResolution::Unresolvable => registry::COMMIT_RESOLUTION_UNRESOLVABLE,
            },
            cleanup_state: facts::cleanup_state(result.cleanup_state()),
            coordination_cleanup: facts::coordination_cleanup(result.coordination_cleanup),
        };
        let cleanup = result
            .cleanup
            .iter()
            .map(super::report::encode_cleanup)
            .collect();
        let cause = result.cause.take().map(|cause| Box::new(cause.into()));
        let mut report = Self::new(Body::CommitResolution(body));
        report.cause = cause;
        report.cleanup = cleanup;
        report
    }

    pub(crate) fn publication(mut result: publication::PublicationResult) -> Self {
        let body = facts::publication(&result);
        let cleanup = result.cleanup.iter().map(facts::cleanup).collect();
        let housekeeping = result
            .visible_housekeeping
            .iter()
            .map(facts::housekeeping_artifact)
            .collect();
        let cause = result
            .cause
            .take()
            .map(ErrorHandle::from_publication_problem)
            .map(Box::new);
        let mut report = Self::new(Body::Publication(body));
        report.cause = cause;
        report.cleanup = cleanup;
        report.housekeeping = housekeeping;
        report.publication_original = Some(result);
        report
    }

    pub(crate) fn validation(result: validation::ValidationResult) -> Self {
        Self::new(Body::Validation(ValidationReport {
            abi_version: 1,
            struct_size: size_of::<ValidationReport>() as u32,
            valid: u8::from(result.valid),
            reserved: [0; 7],
            file_identity: facts::identity(result.file_identity),
            generation: facts::validation_generation(result.generation),
            progress: facts::validation_progress(&result.progress),
        }))
    }

    pub(crate) fn validation_failure(failure: &validation::ValidationFailure) -> Self {
        let mut report = Self::new(Body::Validation(ValidationReport {
            abi_version: 1,
            struct_size: size_of::<ValidationReport>() as u32,
            valid: 0,
            reserved: [0; 7],
            file_identity: Default::default(),
            generation: Default::default(),
            progress: facts::validation_progress(&failure.progress),
        }));
        report.cleanup = failure.cleanup.iter().map(facts::cleanup).collect();
        report
    }

    pub(crate) fn recovery_candidates(result: recovery::RecoveryCandidateInspection) -> Self {
        let candidates = result.candidates().copied().collect();
        let body = RecoveryCandidatesReport {
            abi_version: 1,
            struct_size: size_of::<RecoveryCandidatesReport>() as u32,
            source_identity: facts::identity(result.source_identity),
            progress: facts::validation_progress(&result.progress),
        };
        let mut report = Self::new(Body::RecoveryCandidates(body));
        report.candidates = candidates;
        report
    }

    pub(crate) fn recovery(mut result: recovery::RecoveryResult) -> Self {
        let body = encode_recovery(&result);
        let cleanup = result
            .publication
            .cleanup
            .iter()
            .map(facts::cleanup)
            .collect();
        let housekeeping = result
            .publication
            .visible_housekeeping
            .iter()
            .map(facts::housekeeping_artifact)
            .collect();
        let cause = result
            .publication
            .cause
            .take()
            .map(ErrorHandle::from_publication_problem)
            .map(Box::new);
        let mut report = Self::new(Body::Recovery(body));
        report.cause = cause;
        report.cleanup = cleanup;
        report.housekeeping = housekeeping;
        report.publication_original = Some(result.publication);
        report
    }

    pub(crate) fn recovery_preparation(failure: &recovery::RecoveryPreparationFailure) -> Self {
        let body = encode_recovery_body(
            &failure.report,
            failure.scratch.as_ref(),
            PublicationReport::default(),
        );
        let mut report = Self::new(Body::Recovery(body));
        report.cause = Some(Box::new(ErrorHandle::from_publication_problem(
            failure.cause.clone(),
        )));
        report.housekeeping = failure
            .visible_housekeeping
            .iter()
            .map(facts::housekeeping_artifact)
            .collect();
        report
    }

    pub(crate) fn snapshot_preparation(
        failure: &iprange_livedb::SnapshotPreparationFailure,
    ) -> Self {
        Self::publication_preparation(
            failure,
            registry::RESIDUE_OPERATION_SNAPSHOT_PREPARATION_FAILURE,
        )
    }

    pub(crate) fn publication_preparation(
        failure: &iprange_livedb::SnapshotPreparationFailure,
        operation: u32,
    ) -> Self {
        let mut report = Self::new(Body::Residue(ResidueReport {
            abi_version: 1,
            struct_size: size_of::<ResidueReport>() as u32,
            operation,
            cleanup_state: facts::cleanup_state(failure.cleanup_state()),
            coordination_cleanup: facts::coordination_cleanup(failure.coordination_cleanup),
            housekeeping: facts::housekeeping(failure.housekeeping),
            ..ResidueReport::default()
        }));
        report.cause = Some(Box::new(ErrorHandle::from_publication_problem(
            failure.cause.clone(),
        )));
        report.housekeeping = failure
            .visible_housekeeping
            .iter()
            .map(facts::housekeeping_artifact)
            .collect();
        report
    }

    pub(crate) fn residue_inspection(result: publication::PublicationResidueInspection) -> Self {
        let publication_fixed = result
            .publication
            .as_ref()
            .map_or_else(PublicationReport::default, facts::publication);
        let mut report = Self::new(Body::Residue(ResidueReport {
            abi_version: 1,
            struct_size: size_of::<ResidueReport>() as u32,
            operation: registry::RESIDUE_OPERATION_INSPECT_PUBLICATION,
            classification: residue_coordination(result.coordination),
            directory_identity: facts::identity(result.directory_identity),
            coordination_identity: facts::optional_identity(result.coordination_identity),
            main_identity: Default::default(),
            main_content: 0,
            later_coordination: 0,
            access_policy: 0,
            cleanup_state: registry::CLEANUP_STATE_CLEAN,
            coordination_cleanup: registry::COORDINATION_CLEANUP_NONE,
            housekeeping: registry::HOUSEKEEPING_NONE,
            source_present: 0,
            publication_present: u8::from(result.publication.is_some()),
            reserved: [0; 6],
            entry_count: 0,
            main_tuple_present: 0,
            reserved1: [0; 7],
            main_tuple: Default::default(),
            main_digest: Default::default(),
            publication: publication_fixed,
        }));
        report.publication_original = result.publication;
        report.set_residue(result.handle);
        report
    }

    pub(crate) fn residue_removal(mut result: publication::PublicationResidueRemoval) -> Self {
        let main = result.main.as_ref();
        let cleanup = result.cleanup.iter().map(facts::cleanup).collect();
        let housekeeping = result
            .visible_housekeeping
            .iter()
            .map(facts::housekeeping_artifact)
            .collect();
        let cause = result
            .cause
            .take()
            .map(ErrorHandle::from_publication_problem)
            .map(Box::new);
        let mut report = Self::new(Body::Residue(ResidueReport {
            abi_version: 1,
            struct_size: size_of::<ResidueReport>() as u32,
            operation: registry::RESIDUE_OPERATION_REMOVE_PUBLICATION,
            classification: 0,
            directory_identity: facts::identity(result.directory_identity),
            coordination_identity: facts::optional_identity(Some(result.coordination_identity)),
            main_identity: facts::optional_identity(main.map(|main| main.identity)),
            main_content: main.map_or(0, |main| match main.content {
                publication::PublicationResidueMainContent::V4 => registry::RESIDUE_MAIN_CONTENT_V4,
                publication::PublicationResidueMainContent::Other => {
                    registry::RESIDUE_MAIN_CONTENT_OTHER
                }
            }),
            later_coordination: residue_coordination(result.later_coordination),
            access_policy: facts::access_policy(result.coordination_access_policy),
            cleanup_state: facts::cleanup_state(result.cleanup_state()),
            coordination_cleanup: facts::coordination_cleanup(result.coordination_cleanup),
            housekeeping: facts::housekeeping(result.housekeeping),
            source_present: 0,
            publication_present: 0,
            reserved: [0; 6],
            entry_count: 0,
            main_tuple_present: u8::from(main.and_then(|main| main.tuple).is_some()),
            reserved1: [0; 7],
            main_tuple: main
                .and_then(|main| main.tuple)
                .map_or_else(Default::default, facts::tuple),
            main_digest: main.map_or_else(Default::default, |main| facts::digest(main.digest)),
            publication: Default::default(),
        }));
        report.cause = cause;
        report.cleanup = cleanup;
        report.housekeeping = housekeeping;
        report.set_residue(result.handle.take());
        report
    }

    pub(crate) fn maintenance_list(
        operation: u32,
        directory_identity: validation::LocalFileIdentity,
        entry_count: u64,
    ) -> Self {
        Self::new(Body::Residue(ResidueReport {
            abi_version: 1,
            struct_size: size_of::<ResidueReport>() as u32,
            operation,
            directory_identity: facts::identity(directory_identity),
            cleanup_state: registry::CLEANUP_STATE_CLEAN,
            coordination_cleanup: registry::COORDINATION_CLEANUP_NONE,
            housekeeping: registry::HOUSEKEEPING_NONE,
            entry_count,
            ..ResidueReport::default()
        }))
    }

    pub(crate) fn abandoned_removal(
        operation: u32,
        result: publication::AbandonedArtifactRemoval,
    ) -> Self {
        let cause = result
            .cause
            .map(ErrorHandle::from_publication_problem)
            .map(Box::new);
        let housekeeping = result
            .visible_housekeeping
            .iter()
            .map(facts::housekeeping_artifact)
            .collect();
        let mut report = Self::new(Body::Residue(ResidueReport {
            abi_version: 1,
            struct_size: size_of::<ResidueReport>() as u32,
            operation,
            cleanup_state: facts::cleanup_state(result.cleanup_state),
            coordination_cleanup: registry::COORDINATION_CLEANUP_NONE,
            housekeeping: facts::housekeeping(result.housekeeping),
            source_present: u8::from(result.source_present),
            ..ResidueReport::default()
        }));
        report.cause = cause;
        report.housekeeping = housekeeping;
        report
    }

    pub(crate) fn housekeeping_removal(result: publication::WindowsHousekeepingRemoval) -> Self {
        let cause = result
            .cause
            .map(ErrorHandle::from_publication_problem)
            .map(Box::new);
        let housekeeping = result
            .visible_housekeeping
            .iter()
            .map(facts::housekeeping_artifact)
            .collect();
        let mut report = Self::new(Body::Residue(ResidueReport {
            abi_version: 1,
            struct_size: size_of::<ResidueReport>() as u32,
            operation: registry::RESIDUE_OPERATION_REMOVE_HOUSEKEEPING_ARTIFACT,
            cleanup_state: registry::CLEANUP_STATE_CLEAN,
            coordination_cleanup: registry::COORDINATION_CLEANUP_NONE,
            housekeeping: facts::housekeeping(result.housekeeping),
            ..ResidueReport::default()
        }));
        report.cause = cause;
        report.housekeeping = housekeeping;
        report
    }
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_commit_resolution(
    report: *const ReportHandle,
    output: *mut CommitResolutionReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    crate::report::fixed_getter(report, output, error_output, |body| match body {
        Body::CommitResolution(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_create(
    report: *const ReportHandle,
    output: *mut CreateReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    crate::report::fixed_getter(report, output, error_output, |body| match body {
        Body::Create(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_live_transition(
    report: *const ReportHandle,
    output: *mut LiveTransitionReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    crate::report::fixed_getter(report, output, error_output, |body| match body {
        Body::LiveTransition(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_create_resolution(
    report: *const ReportHandle,
    output: *mut CreateReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    crate::report::fixed_getter(report, output, error_output, |body| match body {
        Body::CreateResolution(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_live_transition_resolution(
    report: *const ReportHandle,
    output: *mut LiveTransitionReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    crate::report::fixed_getter(report, output, error_output, |body| match body {
        Body::LiveTransitionResolution(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_live_residue(
    report: *const ReportHandle,
    output: *mut LiveResidueReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    crate::report::fixed_getter(report, output, error_output, |body| match body {
        Body::LiveResidue(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_publication(
    report: *const ReportHandle,
    output: *mut PublicationReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    crate::report::fixed_getter(report, output, error_output, |body| match body {
        Body::Publication(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_validation(
    report: *const ReportHandle,
    output: *mut ValidationReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    crate::report::fixed_getter(report, output, error_output, |body| match body {
        Body::Validation(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_recovery_candidates(
    report: *const ReportHandle,
    output: *mut RecoveryCandidatesReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    crate::report::fixed_getter(report, output, error_output, |body| match body {
        Body::RecoveryCandidates(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_recovery(
    report: *const ReportHandle,
    output: *mut RecoveryReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    crate::report::fixed_getter(report, output, error_output, |body| match body {
        Body::Recovery(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_get_residue(
    report: *const ReportHandle,
    output: *mut ResidueReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    crate::report::fixed_getter(report, output, error_output, |body| match body {
        Body::Residue(value) => Some(value),
        _ => None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_housekeeping_artifact_count(
    report: *const ReportHandle,
    count: *mut u64,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, count, "artifact count output is null", || {
        // SAFETY: pointers are validated before use.
        let report =
            unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
        let _guard = report.enter()?;
        let count = unsafe { required_output(count, "artifact count output is null")? };
        *count = report.housekeeping.len() as u64;
        Ok::<_, BoundaryError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_housekeeping_artifact_get(
    report: *const ReportHandle,
    index: u64,
    output: *mut HousekeepingArtifact,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "housekeeping output is null", || {
        // SAFETY: pointers are validated before use.
        let report =
            unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
        let _guard = report.enter()?;
        let output = unsafe { required_output(output, "housekeeping output is null")? };
        *output = Default::default();
        *output = *report.housekeeping.get(index as usize).ok_or_else(|| {
            BoundaryError::invalid_argument("housekeeping artifact index is invalid")
        })?;
        Ok::<_, BoundaryError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_recovery_candidate_count(
    report: *const ReportHandle,
    count: *mut u64,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        count,
        "candidate count output is null",
        || {
            // SAFETY: pointers are validated before use.
            let report =
                unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
            let _guard = report.enter()?;
            let count = unsafe { required_output(count, "candidate count output is null")? };
            *count = report.candidates.len() as u64;
            Ok::<_, BoundaryError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_recovery_candidate_get(
    report: *const ReportHandle,
    index: u64,
    output: *mut RecoveryCandidate,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "candidate output is null", || {
        // SAFETY: pointers are validated before use.
        let report =
            unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
        let _guard = report.enter()?;
        let output = unsafe { required_output(output, "candidate output is null")? };
        *output = Default::default();
        let candidate = report
            .candidates
            .get(index as usize)
            .ok_or_else(|| BoundaryError::invalid_argument("candidate index is invalid"))?;
        *output = encode_candidate(candidate);
        Ok::<_, BoundaryError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_take_cleanup_guard(
    report: *mut ReportHandle,
    output: *mut *mut CleanupGuardHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "cleanup guard output is null", || {
        // SAFETY: pointers are validated before use.
        let report =
            unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
        let output = unsafe { required_output(output, "cleanup guard output is null")? };
        *output = std::ptr::null_mut();
        let _gate = report.enter()?;
        let guard = report
            .take_cleanup_guard()
            .ok_or_else(|| BoundaryError::wrong_state("report has no cleanup guard"))?;
        *output = Box::into_raw(Box::new(CleanupGuardHandle::new(guard)));
        Ok::<_, BoundaryError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_report_take_residue(
    report: *mut ReportHandle,
    output: *mut *mut ResidueHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "residue output is null", || {
        // SAFETY: pointers are validated before use.
        let report =
            unsafe { crate::handle::required_handle_input(report, "report handle is null")? };
        let output = unsafe { required_output(output, "residue output is null")? };
        *output = std::ptr::null_mut();
        let _gate = report.enter()?;
        let residue = report
            .take_residue()
            .ok_or_else(|| BoundaryError::wrong_state("report has no residue handle"))?;
        *output = Box::into_raw(Box::new(ResidueHandle::new(residue)));
        Ok::<_, BoundaryError>(())
    })
}

fn encode_create(result: &CreateResult) -> CreateReport {
    CreateReport {
        abi_version: 1,
        struct_size: size_of::<CreateReport>() as u32,
        address_family: facts::address_family(result.address_family),
        value_kind: facts::value_kind(result.value_kind),
        value_tag: *result.value_tag.as_wire(),
        database_id: result.database_id,
        commit_nonce: result.commit_nonce,
        sidecar_id: result.sidecar_id,
        directory_identity: facts::optional_identity(result.directory_identity),
        main_basename: facts::basename(
            result.main_basename.encoding(),
            result.main_basename.as_bytes(),
        ),
        main_identity: facts::optional_identity(result.main_identity),
        sidecar_identity: facts::optional_identity(result.sidecar_identity),
        reader_capacity: result.reader_capacity,
        state: match result.state {
            CreationState::NotCreated => registry::CREATION_NOT_CREATED,
            CreationState::Created => registry::CREATION_CREATED,
            CreationState::OutcomeUnknown => registry::CREATION_OUTCOME_UNKNOWN,
        },
        residue_possible: u8::from(result.residue_possible),
        reserved: [0; 3],
        housekeeping: facts::housekeeping(result.housekeeping),
    }
}

fn encode_live_transition(result: &LiveTransitionResult) -> LiveTransitionReport {
    LiveTransitionReport {
        abi_version: 1,
        struct_size: size_of::<LiveTransitionReport>() as u32,
        operation: match result.operation {
            LiveTransitionOperation::Initialize => registry::LIVE_TRANSITION_OPERATION_INITIALIZE,
            LiveTransitionOperation::Reset => registry::LIVE_TRANSITION_OPERATION_RESET,
        },
        reset_policy: result.reset_policy.map_or(0, |value| match value {
            iprange_livedb::LiveResetPolicy::RollbackSafe => {
                registry::LIVE_RESET_POLICY_ROLLBACK_SAFE
            }
            iprange_livedb::LiveResetPolicy::DiscardPrevious => {
                registry::LIVE_RESET_POLICY_DISCARD_PREVIOUS
            }
        }),
        status: match result.status {
            LiveTransitionStatus::Unchanged => match result.operation {
                LiveTransitionOperation::Initialize => registry::LIVE_TRANSITION_LEFT_IMMUTABLE,
                LiveTransitionOperation::Reset => {
                    registry::LIVE_TRANSITION_OLD_COORDINATION_RETAINED
                }
            },
            LiveTransitionStatus::Initialized => registry::LIVE_TRANSITION_INITIALIZED,
            LiveTransitionStatus::OutcomeUnknown => registry::LIVE_TRANSITION_OUTCOME_UNKNOWN,
        },
        new_sidecar_location: match result.new_sidecar_location {
            iprange_livedb::LiveCoordinationLocation::Absent => {
                registry::LIVE_COORDINATION_LOCATION_ABSENT
            }
            iprange_livedb::LiveCoordinationLocation::Canonical => {
                registry::LIVE_COORDINATION_LOCATION_CANONICAL
            }
            iprange_livedb::LiveCoordinationLocation::Private => {
                registry::LIVE_COORDINATION_LOCATION_PRIVATE
            }
            iprange_livedb::LiveCoordinationLocation::Unclassified => {
                registry::LIVE_COORDINATION_LOCATION_UNCLASSIFIED
            }
        },
        database_id: result.database_id,
        transaction_id: result.transaction_id,
        commit_nonce: result.commit_nonce,
        directory_identity: facts::identity(result.directory_identity),
        main_identity: facts::identity(result.main_identity),
        main_basename: facts::basename(
            result.main_basename.encoding(),
            result.main_basename.as_bytes(),
        ),
        reader_capacity: result.reader_capacity,
        housekeeping: facts::housekeeping(result.housekeeping),
        sidecar_id: result.sidecar_id,
        previous_sidecar_identity: facts::optional_identity(result.previous_sidecar_identity),
        new_sidecar_identity: facts::optional_identity(result.new_sidecar_identity),
        residue_possible: u8::from(result.residue_possible),
        reserved: [0; 7],
    }
}

fn encode_recovery(result: &recovery::RecoveryResult) -> RecoveryReport {
    encode_recovery_body(
        &result.report,
        result.scratch.as_ref(),
        facts::publication(&result.publication),
    )
}

fn encode_recovery_body(
    report: &recovery::RecoveryReport,
    scratch: Option<&recovery::RecoveryScratchAttempt>,
    publication: PublicationReport,
) -> RecoveryReport {
    RecoveryReport {
        abi_version: 1,
        struct_size: size_of::<RecoveryReport>() as u32,
        facts: facts::recovery_facts(report),
        scratch_present: u8::from(scratch.is_some()),
        reserved: [0; 7],
        scratch_attempt_id: scratch.map_or([0; 16], |value| value.attempt_id),
        scratch_directory_identity: scratch.map_or_else(Default::default, |value| {
            facts::identity(value.directory_identity)
        }),
        scratch_creation_security: scratch.map_or_else(Default::default, |value| {
            facts::creation_security(Some(&value.creation_security))
        }),
        publication,
    }
}

fn encode_candidate(value: &recovery::RecoveryCandidate) -> RecoveryCandidate {
    RecoveryCandidate {
        abi_version: 1,
        struct_size: size_of::<RecoveryCandidate>() as u32,
        label: match value.label() {
            recovery::RecoveryCandidateLabel::Newest => registry::RECOVERY_CANDIDATE_NEWEST,
            recovery::RecoveryCandidateLabel::Previous => registry::RECOVERY_CANDIDATE_PREVIOUS,
            recovery::RecoveryCandidateLabel::UnorderedMeta0 => {
                registry::RECOVERY_CANDIDATE_UNORDERED_META0
            }
            recovery::RecoveryCandidateLabel::UnorderedMeta1 => {
                registry::RECOVERY_CANDIDATE_UNORDERED_META1
            }
        },
        reserved: 0,
        source_identity: facts::identity(value.source_identity()),
        database_id: value.database_id(),
        transaction_id: value.transaction_id(),
        commit_nonce: value.commit_nonce(),
    }
}

fn residue_coordination(value: publication::PublicationResidueCoordination) -> u32 {
    match value {
        publication::PublicationResidueCoordination::Absent => {
            registry::RESIDUE_COORDINATION_ABSENT
        }
        publication::PublicationResidueCoordination::PublicationReservation => {
            registry::RESIDUE_COORDINATION_PUBLICATION_RESERVATION
        }
        publication::PublicationResidueCoordination::LiveSidecar => {
            registry::RESIDUE_COORDINATION_LIVE_SIDECAR
        }
        publication::PublicationResidueCoordination::Unselectable => {
            registry::RESIDUE_COORDINATION_UNSELECTABLE
        }
    }
}

fn live_residue_status(value: LiveResidueStatus) -> u32 {
    match value {
        LiveResidueStatus::Absent => registry::LIVE_RESIDUE_STATUS_ABSENT,
        LiveResidueStatus::Ready => registry::LIVE_RESIDUE_STATUS_READY,
        LiveResidueStatus::Completed => registry::LIVE_RESIDUE_STATUS_COMPLETED,
        LiveResidueStatus::Removed => registry::LIVE_RESIDUE_STATUS_REMOVED,
        LiveResidueStatus::OutcomeUnknown => registry::LIVE_RESIDUE_STATUS_OUTCOME_UNKNOWN,
    }
}

fn live_residue_kind(value: LiveResidueKind) -> u32 {
    match value {
        LiveResidueKind::Canonical => registry::LIVE_RESIDUE_KIND_CANONICAL,
        LiveResidueKind::PrivateReset => registry::LIVE_RESIDUE_KIND_PRIVATE_RESET,
    }
}
