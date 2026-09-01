//! Explicit bounded full-file validation.

// A validation failure returns its factual progress and cleanup authority inline.
#![allow(clippy::result_large_err)]

mod bitmap;
mod blob;
mod catalog;
mod context;
mod membership;
mod membership_table;
mod metadata;
mod page;
mod range;
mod retirement;
pub(crate) mod source;
mod structure;
mod structure_table;
mod tree;
mod types;

use std::path::Path;

use crate::bootstrap::{BootstrapError, MetaProblem, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, PAGE_SIZE};
use crate::database_file;
use crate::error::{Error, Result};
use crate::mapping::Mapping;
use crate::publication::{CleanupArtifacts, CoordinationCleanup};
use crate::recovery::RecoverySourceCleanupGuard;

use source::{ImmutableSource, LiveBootstrapSource, LiveOpened, LiveSource};

pub use types::{
    LocalFileIdentity, PhysicalByteInterval, ValidatedGeneration, ValidationAddressFence,
    ValidationBudget, ValidationFailure, ValidationFinding, ValidationMode, ValidationObject,
    ValidationProgress, ValidationReason, ValidationResult, ValidationSink, ValidationSinkControl,
};

/// Validate one explicitly selected source mode without changing the source.
/// Factual availability facts for the version-matched fault worker
/// used by validation and recovery (spec `system.describe.fault_worker`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct WorkerAvailability {
    /// True when a candidate worker executable exists beside the binary.
    pub available: bool,
    /// Worker control-protocol version understood by this build.
    pub protocol: &'static str,
}

/// Reports fault-worker availability for this installation without
/// spawning the worker.
pub fn worker_availability() -> WorkerAvailability {
    crate::worker::availability()
}

pub fn validate<S: ValidationSink>(
    path: impl AsRef<Path>,
    mode: ValidationMode,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<ValidationResult, ValidationFailure> {
    if let Err(cause) = preflight(&mode, budget, cancellation) {
        return Err(failure(cause, ValidationProgress::new()));
    }
    crate::worker::validate(path.as_ref(), &mode, budget, cancellation, sink)
}

pub(crate) fn validate_local<S: ValidationSink>(
    path: &Path,
    mode: ValidationMode,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<ValidationResult, ValidationFailure> {
    if let Err(cause) = preflight(&mode, budget, cancellation) {
        return Err(failure(cause, ValidationProgress::new()));
    }
    match mode {
        ValidationMode::ImmutableCurrent => {
            if budget.max_open_files < 1 {
                return Err(failure(
                    Error::BudgetExceeded("immutable validation open files"),
                    ValidationProgress::new(),
                ));
            }
            validate_immutable(path, budget, cancellation, sink)
        }
        ValidationMode::LiveCurrent => {
            if budget.max_open_files < 2 {
                return Err(failure(
                    Error::BudgetExceeded("live validation open files"),
                    ValidationProgress::new(),
                ));
            }
            validate_live(path, budget, cancellation, sink)
        }
        ValidationMode::OfflineCandidate(candidate) => {
            validate_offline(path, &candidate, budget, cancellation, sink)
        }
    }
}

fn preflight(
    mode: &ValidationMode,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
) -> Result<()> {
    if matches!(mode, &ValidationMode::LiveCurrent) {
        crate::live_lock::require_live_supported()?;
    }
    budget.validate()?;
    cancellation.check()
}

fn validate_offline<S: ValidationSink>(
    path: &Path,
    candidate: &crate::recovery::RecoveryCandidate,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<ValidationResult, ValidationFailure> {
    let source = crate::recovery::inspection::OfflineSource::open(path, cancellation)
        .map_err(|cause| failure(cause, ValidationProgress::new()))?;
    let identity = source.public_identity();
    let classified = crate::recovery::inspection::read_classified(&source.file, cancellation)
        .map_err(|cause| failure(cause, ValidationProgress::new()))?;
    if identity != candidate.source_identity {
        return Err(failure(
            Error::RecoveryCandidateChanged,
            ValidationProgress::new(),
        ));
    }
    let meta = classified
        .selected_meta(candidate)
        .ok_or_else(|| failure(Error::RecoveryCandidateChanged, ValidationProgress::new()))?;
    source
        .require_available(meta.database_id)
        .map_err(|cause| failure(cause, ValidationProgress::new()))?;
    let mapping = validation_mapping(&source.file, meta)
        .map_err(|cause| failure(cause, ValidationProgress::new()))?;
    let mut context = context::Context::new(&mapping, meta, budget, cancellation, sink)
        .map_err(|cause| failure(cause, ValidationProgress::new()))?;
    let scan = crate::worker::probe_source(&mapping, || {
        context
            .reserve_allocator_pages()
            .and_then(|()| validate_selected(&mut context))
    });
    let verification = verify_offline_candidate(&source, candidate, cancellation);
    let progress = context.finish();
    if let Err(cause) = scan {
        return Err(failure(cause, progress));
    }
    if let Err(cause) = verification {
        return Err(failure(cause, progress));
    }
    Ok(ValidationResult {
        valid: progress.finding_count == 0,
        file_identity: identity,
        generation: Some(generation(meta)),
        progress,
    })
}

fn verify_offline_candidate(
    source: &crate::recovery::inspection::OfflineSource,
    candidate: &crate::recovery::RecoveryCandidate,
    cancellation: &CancellationToken,
) -> Result<()> {
    source.verify().map_err(candidate_identity_error)?;
    let classified = crate::recovery::inspection::read_classified(&source.file, cancellation)?;
    if classified.selected_meta(candidate).is_none() {
        return Err(Error::RecoveryCandidateChanged);
    }
    source.verify().map_err(candidate_identity_error)
}

fn candidate_identity_error(cause: Error) -> Error {
    match cause {
        Error::WrongMode(_) => Error::RecoveryCandidateChanged,
        Error::Io(error) if error.kind() == std::io::ErrorKind::NotFound => {
            Error::RecoveryCandidateChanged
        }
        cause => cause,
    }
}

fn validate_live<S: ValidationSink>(
    path: &Path,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<ValidationResult, ValidationFailure> {
    match LiveSource::open(path, cancellation).map_err(|failure| {
        failure_with_guard(failure.cause, ValidationProgress::new(), failure.guard)
    })? {
        LiveOpened::Selected(source) => validate_live_selected(source, budget, cancellation, sink),
        LiveOpened::Bootstrap(source, problem) => validate_live_bootstrap(source, problem, sink),
    }
}

fn validate_live_selected<S: ValidationSink>(
    source: LiveSource,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<ValidationResult, ValidationFailure> {
    let meta = *source.meta;
    let file_identity = source.public_identity();
    let mapping = match validation_mapping(&source.file, meta) {
        Ok(mapping) => mapping,
        Err(cause) => {
            let end = source.finish(Err(cause));
            return Err(failure_with_guard(
                end.cause.expect("failed validation retains its cause"),
                ValidationProgress::new(),
                end.guard,
            ));
        }
    };
    let mut context = match context::Context::new(&mapping, meta, budget, cancellation, sink) {
        Ok(context) => context,
        Err(cause) => {
            let end = source.finish(Err(cause));
            return Err(failure_with_guard(
                end.cause.expect("failed validation retains its cause"),
                ValidationProgress::new(),
                end.guard,
            ));
        }
    };
    let scan = crate::worker::probe_source(&mapping, || {
        context
            .reserve_allocator_pages()
            .and_then(|()| validate_selected(&mut context))
    });
    let progress = context.finish();
    let scan = scan.and_then(|()| crate::worker::checkpoint_validation_progress(&progress));
    let end = source.finish(scan);
    if let Some(cause) = end.cause {
        return Err(failure_with_guard(cause, progress, end.guard));
    }
    Ok(ValidationResult {
        valid: progress.finding_count == 0,
        file_identity,
        generation: Some(generation(meta)),
        progress,
    })
}

fn validate_live_bootstrap<S: ValidationSink>(
    source: LiveBootstrapSource,
    problem: BootstrapError,
    sink: &mut S,
) -> std::result::Result<ValidationResult, ValidationFailure> {
    let file_identity = source.public_identity();
    let mut progress = ValidationProgress::new();
    let report = write_bootstrap_findings(problem, sink, &mut progress)
        .and_then(|()| progress.mark_untraversable(true));
    let end = source.finish(report);
    if let Some(cause) = end.cause {
        return Err(failure_with_guard(cause, progress, end.guard));
    }
    Ok(ValidationResult {
        valid: false,
        file_identity,
        generation: None,
        progress,
    })
}

fn validate_immutable<S: ValidationSink>(
    path: &Path,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<ValidationResult, ValidationFailure> {
    let source = ImmutableSource::open(path, cancellation)
        .map_err(|cause| failure(cause, ValidationProgress::new()))?;
    let bootstrap =
        match database_file::bootstrap_file_faultable(&source.file, OpenMode::ImmutableReader) {
            Ok(bootstrap) => bootstrap,
            Err(Error::Format(problem)) => {
                require_bound_source_available(&source)
                    .map_err(|cause| failure(cause, ValidationProgress::new()))?;
                return bootstrap_report(&source, problem, sink);
            }
            Err(cause) => return Err(failure(cause, ValidationProgress::new())),
        };
    source
        .require_available(bootstrap.meta.database_id)
        .map_err(|cause| failure(cause, ValidationProgress::new()))?;
    let mapping = validation_mapping(&source.file, bootstrap.meta)
        .map_err(|cause| failure(cause, ValidationProgress::new()))?;
    let mut context = context::Context::new(&mapping, bootstrap.meta, budget, cancellation, sink)
        .map_err(|cause| failure(cause, ValidationProgress::new()))?;
    let scan = crate::worker::probe_source(&mapping, || {
        context
            .reserve_allocator_pages()
            .and_then(|()| validate_selected(&mut context))
    });
    let verification = source.verify();
    let progress = context.finish();
    if let Err(cause) = scan {
        return Err(failure(cause, progress));
    }
    if let Err(cause) = verification {
        return Err(failure(cause, progress));
    }
    Ok(ValidationResult {
        valid: progress.finding_count == 0,
        file_identity: source.public_identity(),
        generation: Some(generation(bootstrap.meta)),
        progress,
    })
}

fn validation_mapping(file: &std::fs::File, meta: MetaV4) -> Result<Mapping> {
    let len = meta
        .page_count
        .checked_mul(PAGE_SIZE as u64)
        .ok_or(Error::ArithmeticOverflow("validation mapping length"))?;
    let mut mapping = Mapping::read_only_view(file, len)?;
    mapping.set_unreadable_pages(&crate::worker::unreadable_source_pages())?;
    Ok(mapping)
}

fn require_bound_source_available(source: &ImmutableSource) -> Result<()> {
    match source::selected_or_bound_database_id(&source.file) {
        Ok(database_id) => source.require_available(database_id),
        Err(Error::Format(_) | Error::Corrupt(_)) => Ok(()),
        Err(cause) => Err(cause),
    }
}

fn bootstrap_report<S: ValidationSink>(
    source: &ImmutableSource,
    problem: BootstrapError,
    sink: &mut S,
) -> std::result::Result<ValidationResult, ValidationFailure> {
    let mut progress = ValidationProgress::new();
    let report = write_bootstrap_findings(problem, sink, &mut progress);
    if let Err(cause) = report {
        return Err(failure(cause, progress));
    }
    if let Err(cause) = progress.mark_untraversable(true) {
        return Err(failure(cause, progress));
    }
    if let Err(cause) = source.verify() {
        return Err(failure(cause, progress));
    }
    Ok(ValidationResult {
        valid: false,
        file_identity: source.public_identity(),
        generation: None,
        progress,
    })
}

fn write_bootstrap_findings<S: ValidationSink>(
    problem: BootstrapError,
    sink: &mut S,
    progress: &mut ValidationProgress,
) -> Result<()> {
    match problem {
        BootstrapError::NoBootstrapMeta { meta0, meta1 } => {
            report_meta_problem(sink, progress, 0, meta0)
                .and_then(|()| report_meta_problem(sink, progress, 1, meta1))
        }
        BootstrapError::StaticIdentityMismatch => report_bootstrap_finding(
            sink,
            progress,
            ValidationReason::MetaStaticMismatch,
            ValidationObject::Meta,
            None,
        ),
        BootstrapError::FileTooShort
        | BootstrapError::FileUnaligned
        | BootstrapError::HostAddressability
        | BootstrapError::ImmutableLengthMismatch => report_bootstrap_finding(
            sink,
            progress,
            ValidationReason::FileGeometryInvalid,
            ValidationObject::FileGeometry,
            None,
        ),
        BootstrapError::TransactionGap
        | BootstrapError::PhysicalParity
        | BootstrapError::EqualTransactionDisagreement
        | BootstrapError::CurrentGenerationUnprovable
        | BootstrapError::UnsupportedStructure(_) => report_bootstrap_finding(
            sink,
            progress,
            ValidationReason::MetaInvalid,
            ValidationObject::Meta,
            None,
        ),
    }
}

fn report_meta_problem<S: ValidationSink>(
    sink: &mut S,
    progress: &mut ValidationProgress,
    page_number: u32,
    problem: MetaProblem,
) -> Result<()> {
    let reason = if crate::worker::source_page_unreadable(page_number) {
        ValidationReason::IoError
    } else if problem == MetaProblem::Magic {
        ValidationReason::MetaUnavailable
    } else {
        ValidationReason::MetaInvalid
    };
    report_bootstrap_finding(
        sink,
        progress,
        reason,
        ValidationObject::Meta,
        Some(page_number),
    )
}

fn report_bootstrap_finding<S: ValidationSink>(
    sink: &mut S,
    progress: &mut ValidationProgress,
    reason: ValidationReason,
    object: ValidationObject,
    page_number: Option<u32>,
) -> Result<()> {
    progress.count_finding(reason)?;
    let finding = ValidationFinding {
        sequence: progress.finding_count,
        reason,
        object,
        page_number,
        physical_bytes: page_number.map(|page| PhysicalByteInterval {
            start: u64::from(page) * crate::contract::PAGE_SIZE as u64,
            end_exclusive: (u64::from(page) + 1) * crate::contract::PAGE_SIZE as u64,
        }),
        related_page_number: None,
        address_fence: None,
    };
    crate::worker::checkpoint_validation_progress(progress)?;
    match sink.finding(&finding) {
        Ok(ValidationSinkControl::Continue) => Ok(()),
        Ok(ValidationSinkControl::Stop) => Err(Error::StoppedBySink),
        Err(cause) => Err(Error::SinkFailed(Box::new(cause))),
    }
}

pub(crate) fn failure(cause: Error, progress: ValidationProgress) -> ValidationFailure {
    failure_with_guard(cause, progress, None)
}

fn failure_with_guard(
    cause: Error,
    progress: ValidationProgress,
    source_cleanup: Option<RecoverySourceCleanupGuard>,
) -> ValidationFailure {
    let coordination_cleanup = if source_cleanup.is_some() {
        CoordinationCleanup::CleanupGuard
    } else {
        CoordinationCleanup::None
    };
    ValidationFailure {
        cause,
        progress: Box::new(progress),
        cleanup: Box::new(CleanupArtifacts::new()),
        coordination_cleanup,
        source_cleanup,
    }
}

fn validate_selected<S: ValidationSink>(context: &mut context::Context<'_, S>) -> Result<()> {
    range::validate(context)?;
    catalog::validate(context)?;
    structure::validate(context)?;
    membership::validate(context)?;
    metadata::validate(context)?;
    let free_root = context.meta.free_bitmap_root;
    let page_count = context.meta.page_count;
    bitmap::validate(context, free_root, page_count, bitmap::Kind::Free)?;
    retirement::validate(context)?;
    context.validate_partition()
}

fn generation(meta: MetaV4) -> ValidatedGeneration {
    ValidatedGeneration {
        address_family: meta.address_family,
        value_kind: meta.value_kind,
        structure_kind: meta
            .structure_kind()
            .expect("bootstrap rejects unsupported structures"),
        value_tag: meta.value_tag,
        database_id: meta.database_id,
        transaction_id: meta.txn_id,
        commit_nonce: meta.commit_nonce,
        page_count: meta.page_count,
        roots: meta.roots(),
    }
}
