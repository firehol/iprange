//! Exact source-generation protection for one recovery operation.

mod basic;

use std::fs::File;
use std::path::{Path, PathBuf};

use crate::cancellation::CancellationToken;
use crate::contract::MetaV4;
use crate::database;
use crate::error::{Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, Identity, Sidecar, MAIN_LIFETIME_LOCK};
use crate::publication::PublicationProblem;
use crate::validation::source::public_identity;

use super::classify::GenerationOrder;
use super::inspection::read_classified;
use super::{RecoveryCandidate, RecoveryCandidateLabel};

#[derive(Clone, Copy)]
pub(crate) enum SourceMode {
    Immutable,
    Offline,
    Live,
}

#[derive(Debug)]
pub(crate) enum Source {
    Basic(BasicSource),
    Live(LiveSource),
}

#[derive(Debug)]
pub(crate) struct BasicSource {
    file: File,
    path: PathBuf,
    sidecar: Option<PathBuf>,
    identity: Identity,
    candidate: RecoveryCandidate,
    meta: MetaV4,
    lifetime_locked: bool,
}

#[derive(Debug)]
pub(crate) struct LiveSource {
    file: File,
    path: PathBuf,
    identity: Identity,
    sidecar: Sidecar,
    slot: u32,
    candidate: RecoveryCandidate,
    meta: MetaV4,
    gate_locked: bool,
    slot_active: bool,
    lifetime_locked: bool,
    owner_pid: u32,
}

pub(crate) struct SourceEnd {
    pub(crate) cause: Option<Error>,
    pub(crate) guard: Option<RecoverySourceCleanupGuard>,
}

pub(crate) struct SourceOpenFailure {
    pub(crate) cause: Error,
    pub(crate) guard: Option<RecoverySourceCleanupGuard>,
}

#[derive(Debug)]
pub struct RecoverySourceCleanupGuard {
    source: Option<Box<Source>>,
    last_problem: PublicationProblem,
}

impl RecoverySourceCleanupGuard {
    pub fn last_problem(&self) -> PublicationProblem {
        self.last_problem
    }

    pub fn cleanup_pending(&self) -> bool {
        self.source.is_some()
    }

    pub fn retry_cleanup(&mut self) -> std::result::Result<bool, PublicationProblem> {
        let Some(mut source) = self.source.take() else {
            return Ok(false);
        };
        match source.release() {
            Ok(()) => Ok(true),
            Err(cause) => {
                self.last_problem = problem(&cause);
                self.source = Some(source);
                Err(self.last_problem)
            }
        }
    }
}

impl Source {
    pub(crate) fn open(
        path: &Path,
        candidate: RecoveryCandidate,
        mode: SourceMode,
        cancellation: &CancellationToken,
    ) -> std::result::Result<Self, SourceOpenFailure> {
        cancellation.check().map_err(open_problem)?;
        let result = match mode {
            SourceMode::Immutable => {
                BasicSource::open(path, candidate, true, cancellation).map(Self::Basic)
            }
            SourceMode::Offline => {
                BasicSource::open(path, candidate, false, cancellation).map(Self::Basic)
            }
            SourceMode::Live => return LiveSource::open(path, candidate, cancellation),
        };
        result.map_err(open_problem)
    }

    pub(crate) fn file(&self) -> &File {
        match self {
            Self::Basic(source) => &source.file,
            Self::Live(source) => &source.file,
        }
    }

    pub(crate) fn meta(&self) -> MetaV4 {
        match self {
            Self::Basic(source) => source.meta,
            Self::Live(source) => source.meta,
        }
    }

    pub(crate) fn identity(&self) -> crate::validation::LocalFileIdentity {
        match self {
            Self::Basic(source) => public_identity(source.identity),
            Self::Live(source) => public_identity(source.identity),
        }
    }

    pub(crate) fn finish(mut self, used: MetaV4, cancellation: &CancellationToken) -> SourceEnd {
        let checked = self.final_check(used, cancellation);
        let released = self.release();
        terminal(self, checked, released)
    }

    pub(crate) fn abandon(mut self, cause: Error) -> SourceEnd {
        let released = self.release();
        terminal(self, Err(cause), released)
    }

    pub(crate) fn release_only(mut self) -> SourceEnd {
        let released = self.release();
        terminal(self, Ok(()), released)
    }

    fn final_check(&mut self, used: MetaV4, cancellation: &CancellationToken) -> Result<()> {
        match self {
            Self::Basic(source) => source.final_check(used, cancellation),
            Self::Live(source) => source.final_check(used, cancellation),
        }
    }

    fn release(&mut self) -> Result<()> {
        match self {
            Self::Basic(source) => source.release(),
            Self::Live(source) => source.release(),
        }
    }
}

impl LiveSource {
    fn open(
        path: &Path,
        candidate: RecoveryCandidate,
        cancellation: &CancellationToken,
    ) -> std::result::Result<Source, SourceOpenFailure> {
        if candidate.label != RecoveryCandidateLabel::Newest {
            return Err(open_problem(Error::InvalidArgument(
                "live recovery requires the newest candidate",
            )));
        }
        let file = database::open_read_only(path).map_err(open_problem)?;
        let identity = live_sidecar::identity(&file).map_err(open_problem)?;
        live_lock::lock_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Shared, cancellation)
            .map_err(open_problem)?;
        match open_live_locked(file, path, identity, candidate, cancellation) {
            Ok(source) => Ok(Source::Live(source)),
            Err(LiveOpenFailure::Unclaimed(file, cause)) => {
                let _ = live_lock::unlock(&file, MAIN_LIFETIME_LOCK);
                Err(open_problem(cause))
            }
            Err(LiveOpenFailure::Claimed(source, cause)) => {
                let end = Source::Live(*source).abandon(cause);
                Err(SourceOpenFailure {
                    cause: end.cause.expect("failed open retains its cause"),
                    guard: end.guard,
                })
            }
        }
    }

    fn final_check(&mut self, used: MetaV4, cancellation: &CancellationToken) -> Result<()> {
        self.require_owner()?;
        cancellation.check()?;
        if !self.gate_locked {
            live_lock::lock_cancellable(&self.sidecar.file, 0, Mode::Exclusive, cancellation)
                .map_err(live_coordination)?;
            self.gate_locked = true;
        }
        if self.meta != used || self.candidate.transaction_id != used.txn_id {
            return Err(Error::RecoveryCandidateChanged);
        }
        verify_live_paths(&self.path, self.identity, &self.sidecar)?;
        self.sidecar
            .verify_reader(self.slot, used.txn_id)
            .map_err(live_coordination)
    }

    fn release(&mut self) -> Result<()> {
        self.require_owner()?;
        self.release_slot()?;
        self.release_gate()?;
        self.release_lifetime()
    }

    fn release_slot(&mut self) -> Result<()> {
        if !self.slot_active {
            return Ok(());
        }
        self.ensure_gate()?;
        self.sidecar
            .release_reader(self.slot)
            .map_err(live_coordination)?;
        self.slot_active = false;
        Ok(())
    }

    fn ensure_gate(&mut self) -> Result<()> {
        if !self.gate_locked {
            self.sidecar
                .lock_gate(Mode::Exclusive)
                .map_err(live_coordination)?;
            self.gate_locked = true;
        }
        Ok(())
    }

    fn release_gate(&mut self) -> Result<()> {
        if self.gate_locked {
            self.sidecar.unlock_gate().map_err(live_coordination)?;
            self.gate_locked = false;
        }
        Ok(())
    }

    fn release_lifetime(&mut self) -> Result<()> {
        if self.lifetime_locked {
            live_lock::unlock(&self.file, MAIN_LIFETIME_LOCK)?;
            self.lifetime_locked = false;
        }
        Ok(())
    }

    fn require_owner(&self) -> Result<()> {
        if self.owner_pid != std::process::id() {
            Err(Error::ForkedHandle)
        } else {
            Ok(())
        }
    }
}

fn open_live_locked(
    file: File,
    path: &Path,
    identity: Identity,
    candidate: RecoveryCandidate,
    cancellation: &CancellationToken,
) -> std::result::Result<LiveSource, LiveOpenFailure> {
    let meta = match bind_live_meta(&file, path, identity, candidate, cancellation) {
        Ok(meta) => meta,
        Err(cause) => return Err(LiveOpenFailure::Unclaimed(file, cause)),
    };
    let sidecar = match Sidecar::open(path, meta.database_id) {
        Ok(sidecar) => sidecar,
        Err(cause) => return Err(LiveOpenFailure::Unclaimed(file, live_coordination(cause))),
    };
    if let Err(cause) = live_lock::lock_cancellable(&sidecar.file, 0, Mode::Exclusive, cancellation)
        .map_err(live_coordination)
    {
        return Err(LiveOpenFailure::Unclaimed(file, cause));
    }
    match claim_live(file, path, identity, sidecar, candidate, meta, cancellation) {
        Ok(source) => Ok(source),
        Err(ClaimFailure::Unclaimed(file, sidecar, cause)) => {
            let _ = sidecar.unlock_gate();
            Err(LiveOpenFailure::Unclaimed(file, cause))
        }
        Err(ClaimFailure::Claimed(mut source, cause)) => {
            if let Err(unlock) = source.sidecar.unlock_gate().map_err(live_coordination) {
                source.gate_locked = true;
                return Err(LiveOpenFailure::Claimed(source, unlock));
            }
            source.gate_locked = false;
            Err(LiveOpenFailure::Claimed(source, cause))
        }
    }
}

enum LiveOpenFailure {
    Unclaimed(File, Error),
    Claimed(Box<LiveSource>, Error),
}

enum ClaimFailure {
    Unclaimed(File, Sidecar, Error),
    Claimed(Box<LiveSource>, Error),
}

fn claim_live(
    file: File,
    path: &Path,
    identity: Identity,
    sidecar: Sidecar,
    candidate: RecoveryCandidate,
    initial: MetaV4,
    cancellation: &CancellationToken,
) -> std::result::Result<LiveSource, ClaimFailure> {
    if let Err(cause) = prepare_live_claim(
        &file,
        path,
        identity,
        &sidecar,
        candidate,
        initial,
        cancellation,
    ) {
        return Err(ClaimFailure::Unclaimed(file, sidecar, cause));
    }
    let slot = match sidecar
        .claim_reader(initial.txn_id)
        .map_err(live_coordination)
    {
        Ok(slot) => slot,
        Err(cause) => return Err(ClaimFailure::Unclaimed(file, sidecar, cause)),
    };
    let mut source = LiveSource {
        file,
        path: path.to_path_buf(),
        identity,
        sidecar,
        slot,
        candidate,
        meta: initial,
        gate_locked: true,
        slot_active: true,
        lifetime_locked: true,
        owner_pid: std::process::id(),
    };
    if let Err(cause) = verify_live_claim(&source) {
        return Err(ClaimFailure::Claimed(Box::new(source), cause));
    }
    if let Err(cause) = source.sidecar.unlock_gate().map_err(live_coordination) {
        return Err(ClaimFailure::Claimed(Box::new(source), cause));
    }
    source.gate_locked = false;
    Ok(source)
}

fn prepare_live_claim(
    file: &File,
    path: &Path,
    identity: Identity,
    sidecar: &Sidecar,
    candidate: RecoveryCandidate,
    initial: MetaV4,
    cancellation: &CancellationToken,
) -> Result<()> {
    verify_live_paths(path, identity, sidecar)?;
    cancellation.check()?;
    let meta = bind_live_meta(file, path, identity, candidate, cancellation)?;
    if meta != initial || meta.database_id != sidecar.header.database_id {
        return Err(Error::RecoveryCandidateChanged);
    }
    sidecar.scan_at_most(meta.txn_id).map_err(live_coordination)
}

fn verify_live_claim(source: &LiveSource) -> Result<()> {
    verify_live_paths(&source.path, source.identity, &source.sidecar)?;
    source
        .sidecar
        .verify_reader(source.slot, source.meta.txn_id)
        .map_err(live_coordination)
}

fn bind_live_meta(
    file: &File,
    path: &Path,
    identity: Identity,
    candidate: RecoveryCandidate,
    cancellation: &CancellationToken,
) -> Result<MetaV4> {
    live_sidecar::verify_path(path, identity).map_err(candidate_changed)?;
    let classified = read_classified(file, cancellation)?;
    if classified.order == GenerationOrder::Unproven {
        return Err(Error::RecoveryCandidateChanged);
    }
    let meta = classified
        .selected_meta(&candidate)
        .ok_or(Error::RecoveryCandidateChanged)?;
    if candidate.label != RecoveryCandidateLabel::Newest {
        return Err(Error::RecoveryCandidateChanged);
    }
    live_sidecar::verify_path(path, identity).map_err(candidate_changed)?;
    Ok(meta)
}

fn verify_live_paths(path: &Path, identity: Identity, sidecar: &Sidecar) -> Result<()> {
    live_sidecar::verify_path(path, identity)
        .and_then(|()| sidecar.verify_path())
        .and_then(|()| sidecar.verify_header())
        .map_err(live_coordination)
}

fn terminal(source: Source, checked: Result<()>, released: Result<()>) -> SourceEnd {
    match released {
        Ok(()) => SourceEnd {
            cause: checked.err(),
            guard: None,
        },
        Err(cleanup) => SourceEnd {
            cause: checked.err().or_else(|| Some(cleanup_for_cause(&cleanup))),
            guard: Some(RecoverySourceCleanupGuard {
                source: Some(Box::new(source)),
                last_problem: problem(&cleanup),
            }),
        },
    }
}

fn open_problem(cause: Error) -> SourceOpenFailure {
    SourceOpenFailure { cause, guard: None }
}

fn cleanup_for_cause(error: &Error) -> Error {
    match error {
        Error::ForkedHandle => Error::ForkedHandle,
        _ => Error::CleanupConflict("source recovery protection was not released"),
    }
}

fn candidate_changed(_cause: Error) -> Error {
    Error::RecoveryCandidateChanged
}

fn live_coordination(cause: Error) -> Error {
    match cause {
        Error::Cancelled => Error::Cancelled,
        Error::ForkedHandle => Error::ForkedHandle,
        Error::LiveRecoveryCoordinationUnavailable(_) => cause,
        cause => Error::LiveRecoveryCoordinationUnavailable(Box::new(cause)),
    }
}

pub(crate) fn problem(error: &Error) -> PublicationProblem {
    let os_code = match error {
        Error::Io(source) => source.raw_os_error(),
        _ => None,
    };
    PublicationProblem::new(error.code(), os_code, "recovery source operation failed")
}
