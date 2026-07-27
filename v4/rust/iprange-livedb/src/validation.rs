//! Explicit bounded full-file validation.

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
mod tree;
mod types;

use std::path::Path;

use crate::bootstrap::{BootstrapError, MetaProblem, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::MetaV4;
use crate::database;
use crate::error::{Error, Result};

use source::{combine_cleanup, ImmutableSource, LiveBootstrapSource, LiveOpened, LiveSource};

pub use types::{
    LocalFileIdentity, PhysicalByteInterval, ValidatedGeneration, ValidationAddressFence,
    ValidationBudget, ValidationFailure, ValidationFinding, ValidationMode, ValidationObject,
    ValidationProgress, ValidationReason, ValidationResult, ValidationSink, ValidationSinkControl,
};

/// Validate one explicitly selected source mode without changing the source.
pub fn validate<S: ValidationSink>(
    path: impl AsRef<Path>,
    mode: ValidationMode,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<ValidationResult, ValidationFailure> {
    if let Err(cause) = budget.validate().and_then(|()| cancellation.check()) {
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
            validate_immutable(path.as_ref(), budget, cancellation, sink)
        }
        ValidationMode::LiveCurrent => {
            if budget.max_open_files < 2 {
                return Err(failure(
                    Error::BudgetExceeded("live validation open files"),
                    ValidationProgress::new(),
                ));
            }
            validate_live(path.as_ref(), budget, cancellation, sink)
        }
        ValidationMode::OfflineCandidate(candidate) => {
            validate_offline(path.as_ref(), &candidate, budget, cancellation, sink)
        }
    }
}

fn validate_offline<S: ValidationSink>(
    path: &Path,
    candidate: &crate::recovery::RecoveryCandidate,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<ValidationResult, ValidationFailure> {
    let source = crate::recovery::inspection::OfflineSource::open(path)
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
    let mut context = context::Context::new(&source.file, meta, budget, cancellation, sink)
        .map_err(|cause| failure(cause, ValidationProgress::new()))?;
    let scan = context
        .reserve_allocator_pages()
        .and_then(|()| validate_selected(&mut context));
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
    match LiveSource::open(path).map_err(|cause| failure(cause, ValidationProgress::new()))? {
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
    let meta = source.meta;
    let file_identity = source.public_identity();
    let mut context = match context::Context::new(&source.file, meta, budget, cancellation, sink) {
        Ok(context) => context,
        Err(cause) => {
            return match source.close() {
                Ok(()) => Err(failure(cause, ValidationProgress::new())),
                Err(cleanup) => Err(failure(
                    Error::CleanupIncomplete {
                        cause: Box::new(cause),
                        cleanup: Box::new(cleanup),
                    },
                    ValidationProgress::new(),
                )),
            };
        }
    };
    let scan = context
        .reserve_allocator_pages()
        .and_then(|()| validate_selected(&mut context));
    let progress = context.finish();
    let cleanup = source.close();
    if let Err(cause) = scan {
        return Err(failure(combine_cleanup(cause, cleanup), progress));
    }
    if let Err(cause) = cleanup {
        return Err(failure(cause, progress));
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
    let report = write_bootstrap_findings(problem, sink, &mut progress);
    if report.is_ok() {
        if let Err(cause) = progress.mark_untraversable(true) {
            return Err(failure(cause, progress));
        }
    }
    let cleanup = source.close();
    if let Err(cause) = report {
        return Err(failure(combine_cleanup(cause, cleanup), progress));
    }
    if let Err(cause) = cleanup {
        return Err(failure(cause, progress));
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
    let source =
        ImmutableSource::open(path).map_err(|cause| failure(cause, ValidationProgress::new()))?;
    let bootstrap = match database::bootstrap_file(&source.file, OpenMode::ImmutableReader) {
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
    let mut context =
        context::Context::new(&source.file, bootstrap.meta, budget, cancellation, sink)
            .map_err(|cause| failure(cause, ValidationProgress::new()))?;
    let scan = context
        .reserve_allocator_pages()
        .and_then(|()| validate_selected(&mut context));
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
        | BootstrapError::CurrentGenerationUnprovable => report_bootstrap_finding(
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
    let reason = if problem == MetaProblem::Magic {
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
    match sink.finding(&finding) {
        Ok(ValidationSinkControl::Continue) => Ok(()),
        Ok(ValidationSinkControl::Stop) => Err(Error::StoppedBySink),
        Err(cause) => Err(Error::SinkFailed(Box::new(cause))),
    }
}

fn failure(cause: Error, progress: ValidationProgress) -> ValidationFailure {
    ValidationFailure {
        cause,
        progress: Box::new(progress),
    }
}

fn validate_selected<S: ValidationSink>(context: &mut context::Context<'_, S>) -> Result<()> {
    range::validate(context)?;
    catalog::validate(context)?;
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
        value_tag: meta.value_tag,
        database_id: meta.database_id,
        transaction_id: meta.txn_id,
        commit_nonce: meta.commit_nonce,
        page_count: meta.page_count,
        roots: [
            meta.range_root,
            meta.catalog_name_root,
            meta.catalog_index_root,
            meta.feed_used_root,
            meta.membership_id_root,
            meta.membership_hash_root,
            meta.membership_used_root,
            meta.metadata_root,
            meta.free_bitmap_root,
            meta.retirement_root,
        ],
    }
}
