use std::fs::File;
use std::path::{Path, PathBuf};

use crate::bootstrap::classify_recovery_meta;
use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, PAGE_SIZE};
use crate::database_file;
use crate::error::{combine_errors, Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_namespace::Identity;
use crate::live_sidecar::{self, MAIN_LIFETIME_LOCK};
use crate::mapping::Mapping;
use crate::validation::source::{public_identity, ImmutableSource};
use crate::validation::ValidationBudget;

use super::classify::{ClassifiedMetas, GenerationOrder};
use super::{RecoveryCandidateInspection, RecoveryCandidateLabel};

/// Coordination contract used while inspecting retained recovery candidates.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum RecoveryInspectionMode {
    Immutable,
    Live,
    /// The caller certifies exclusive quiescence for the complete operation.
    Offline,
}

/// Inspect exact retained metadata candidates without scanning their page graphs.
pub fn inspect_recovery_candidates(
    path: impl AsRef<Path>,
    mode: RecoveryInspectionMode,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
) -> Result<RecoveryCandidateInspection> {
    if mode == RecoveryInspectionMode::Live {
        live_lock::require_live_supported()?;
    }
    require_budget(budget, mode)?;
    cancellation.check()?;
    crate::worker::inspect_recovery_candidates(path.as_ref(), mode, budget, cancellation)
}

pub(crate) fn inspect_recovery_candidates_local(
    path: &Path,
    mode: RecoveryInspectionMode,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
) -> Result<RecoveryCandidateInspection> {
    if mode == RecoveryInspectionMode::Live {
        live_lock::require_live_supported()?;
    }
    require_budget(budget, mode)?;
    cancellation.check()?;
    match mode {
        RecoveryInspectionMode::Immutable => inspect_immutable(path, cancellation),
        RecoveryInspectionMode::Live => inspect_live(path, cancellation),
        RecoveryInspectionMode::Offline => inspect_offline(path, cancellation),
    }
}

fn require_budget(budget: &ValidationBudget, mode: RecoveryInspectionMode) -> Result<()> {
    budget.validate()?;
    let required = match mode {
        RecoveryInspectionMode::Live => 2,
        RecoveryInspectionMode::Immutable | RecoveryInspectionMode::Offline => 1,
    };
    if budget.max_open_files < required {
        return Err(Error::BudgetExceeded(
            "recovery inspection open-file budget",
        ));
    }
    Ok(())
}

fn inspect_immutable(
    path: &Path,
    cancellation: &CancellationToken,
) -> Result<RecoveryCandidateInspection> {
    let source = ImmutableSource::open(path, cancellation)?;
    let classified = read_classified(&source.file, cancellation)?;
    require_immutable_available(&source, &classified)?;
    source.verify()?;
    inspection(source.public_identity(), &classified)
}

fn inspect_offline(
    path: &Path,
    cancellation: &CancellationToken,
) -> Result<RecoveryCandidateInspection> {
    let source = OfflineSource::open(path, cancellation)?;
    let classified = read_classified(&source.file, cancellation)?;
    require_offline_available(&source, &classified)?;
    source.verify()?;
    inspection(source.public_identity(), &classified)
}

fn inspect_live(
    path: &Path,
    cancellation: &CancellationToken,
) -> Result<RecoveryCandidateInspection> {
    let file = database_file::open_read_only(path)?;
    let identity = crate::live_namespace::identity(&file)?;
    live_lock::lock_file_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Shared, cancellation)?;
    crate::live_namespace::verify_path(path, identity)?;
    let initial = read_classified(&file, cancellation)?;
    let current = require_live_current(&initial)?;
    crate::live_cleanup::require_main_available(path, identity, current.database_id)?;
    let sidecar =
        live_sidecar::Sidecar::open(path, current.database_id).map_err(live_coordination_error)?;
    sidecar
        .lock_gate_cancellable(Mode::Exclusive, cancellation)
        .map_err(live_coordination_error)?;
    let result = inspect_live_locked(path, &file, identity, &sidecar, cancellation);
    release_live_gate(&sidecar, result)
}

fn release_live_gate(
    sidecar: &live_sidecar::Sidecar,
    result: Result<RecoveryCandidateInspection>,
) -> Result<RecoveryCandidateInspection> {
    let unlocked = sidecar.unlock_gate().map_err(live_coordination_error);
    match result {
        Ok(result) => {
            unlocked?;
            Ok(result)
        }
        Err(cause) => Err(combine_errors(cause, unlocked)),
    }
}

fn inspect_live_locked(
    path: &Path,
    file: &File,
    identity: Identity,
    sidecar: &live_sidecar::Sidecar,
    cancellation: &CancellationToken,
) -> Result<RecoveryCandidateInspection> {
    verify_live(path, identity, sidecar)?;
    cancellation.check()?;
    let classified = read_classified(file, cancellation)?;
    let current = require_live_current(&classified)?;
    if current.database_id != sidecar.header.database_id {
        return Err(live_coordination_error(Error::WrongMode(
            "reader table belongs to a different database",
        )));
    }
    sidecar
        .inspect_at_most_cancellable(current.txn_id, cancellation)
        .map_err(live_coordination_error)?;
    verify_live(path, identity, sidecar)?;
    live_inspection(public_identity(identity), &classified)
}

fn verify_live(path: &Path, identity: Identity, sidecar: &live_sidecar::Sidecar) -> Result<()> {
    crate::live_namespace::verify_path(path, identity)
        .and_then(|()| sidecar.verify_path())
        .and_then(|()| sidecar.verify_header())
        .map_err(live_coordination_error)
}

fn require_live_current(classified: &ClassifiedMetas) -> Result<MetaV4> {
    if classified.order == GenerationOrder::Unproven {
        return Err(Error::LiveRecoveryCurrentGenerationUnprovable);
    }
    classified
        .current_recovery_meta()
        .ok_or(Error::LiveRecoveryCurrentGenerationUnreadable)
}

fn require_immutable_available(
    source: &ImmutableSource,
    classified: &ClassifiedMetas,
) -> Result<()> {
    for candidate in classified
        .candidates(source.public_identity())
        .into_iter()
        .flatten()
    {
        source.require_available(candidate.database_id)?;
    }
    Ok(())
}

fn require_offline_available(source: &OfflineSource, classified: &ClassifiedMetas) -> Result<()> {
    for candidate in classified
        .candidates(source.public_identity())
        .into_iter()
        .flatten()
    {
        source.require_available(candidate.database_id)?;
    }
    Ok(())
}

fn live_coordination_error(cause: Error) -> Error {
    match cause {
        Error::Cancelled => Error::Cancelled,
        Error::LiveRecoveryCoordinationUnavailable(_) => cause,
        cause => Error::LiveRecoveryCoordinationUnavailable(Box::new(cause)),
    }
}

fn inspection(
    identity: crate::validation::LocalFileIdentity,
    classified: &ClassifiedMetas,
) -> Result<RecoveryCandidateInspection> {
    Ok(RecoveryCandidateInspection::new(
        identity,
        classified.progress()?,
        classified.candidates(identity),
    ))
}

fn live_inspection(
    identity: crate::validation::LocalFileIdentity,
    classified: &ClassifiedMetas,
) -> Result<RecoveryCandidateInspection> {
    let newest = classified
        .candidates(identity)
        .into_iter()
        .flatten()
        .find(|candidate| candidate.label == RecoveryCandidateLabel::Newest)
        .ok_or(Error::LiveRecoveryCurrentGenerationUnreadable)?;
    Ok(RecoveryCandidateInspection::new(
        identity,
        classified.progress()?,
        [Some(newest), None],
    ))
}

pub(crate) fn read_classified(
    file: &File,
    cancellation: &CancellationToken,
) -> Result<ClassifiedMetas> {
    let physical_bytes = file.metadata()?.len();
    let mapped_bytes = physical_bytes.min((2 * PAGE_SIZE) as u64);
    let mapping = (mapped_bytes >= PAGE_SIZE as u64)
        .then(|| Mapping::read_only_view(file, mapped_bytes))
        .transpose()?;
    let mut states = [None, None];
    if let Some(mapping) = mapping.as_ref() {
        crate::worker::probe_source(mapping, || {
            classify_mapped(mapping, mapped_bytes, cancellation, &mut states)
        })?;
    }
    Ok(ClassifiedMetas::new(states))
}

fn classify_mapped(
    mapping: &Mapping,
    mapped_bytes: u64,
    cancellation: &CancellationToken,
    states: &mut [Option<crate::bootstrap::RecoveryMetaState>; 2],
) -> Result<()> {
    for (index, slot) in states.iter_mut().enumerate() {
        cancellation.check()?;
        let page_end = ((index + 1) * PAGE_SIZE) as u64;
        if page_end <= mapped_bytes {
            if crate::worker::source_page_unreadable(index as u32) {
                continue;
            }
            let page = mapping.page(index as u32, 2)?;
            *slot = Some(classify_recovery_meta(page));
        }
    }
    Ok(())
}

pub(crate) struct OfflineSource {
    pub(crate) file: File,
    path: PathBuf,
    identity: Identity,
}

impl OfflineSource {
    pub(crate) fn open(path: &Path, cancellation: &CancellationToken) -> Result<Self> {
        let file = crate::live_namespace::open_rw(path)?;
        let identity = crate::live_namespace::identity_any_link(&file)?;
        live_lock::lock_file_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Exclusive, cancellation)?;
        crate::live_namespace::verify_path_any_link(path, identity)?;
        Ok(Self {
            file,
            path: path.to_path_buf(),
            identity,
        })
    }

    pub(crate) fn verify(&self) -> Result<()> {
        crate::live_namespace::verify_path_any_link(&self.path, self.identity)
    }

    pub(crate) fn public_identity(&self) -> crate::validation::LocalFileIdentity {
        public_identity(self.identity)
    }

    pub(crate) fn require_available(&self, database_id: [u8; 16]) -> Result<()> {
        crate::live_cleanup::require_main_available(&self.path, self.identity, database_id)
    }
}

#[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
#[path = "inspection_tests.rs"]
mod tests;
