//! Exact source-generation protection for one recovery operation.

mod basic;
mod live;

use std::fs::File;
use std::path::{Path, PathBuf};

use crate::cancellation::CancellationToken;
use crate::contract::MetaV4;
use crate::database;
use crate::error::{combine_errors, Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, Identity, Sidecar, MAIN_LIFETIME_LOCK};
use crate::mapping::Mapping;
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

#[derive(Clone, Copy)]
pub(crate) enum CurrentSourceMode {
    Immutable,
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
    mapping: Mapping,
    path: PathBuf,
    sidecar: Option<PathBuf>,
    identity: Identity,
    selection: BasicSelection,
    meta: MetaV4,
    lifetime_locked: bool,
}

#[derive(Clone, Copy, Debug)]
enum BasicSelection {
    Candidate(RecoveryCandidate),
    Current,
}

#[derive(Debug)]
pub(crate) struct LiveSource {
    file: File,
    mapping: Mapping,
    path: PathBuf,
    identity: Identity,
    sidecar: Sidecar,
    slot: u32,
    candidate: Option<RecoveryCandidate>,
    meta: MetaV4,
    gate_locked: bool,
    registration: RegistrationState,
    lifetime_locked: bool,
    owner_pid: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum RegistrationState {
    Active,
    Clearing,
    Cleared,
    Released,
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
    source: Option<GuardSource>,
    last_problem: PublicationProblem,
}

#[derive(Debug)]
enum GuardSource {
    Recovery(Box<Source>),
    Validation(Box<crate::validation::source::ValidationCleanupSource>),
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
                self.last_problem = source.problem(&cause);
                self.source = Some(source);
                Err(self.last_problem)
            }
        }
    }

    pub(crate) fn for_validation(
        source: crate::validation::source::ValidationCleanupSource,
        cause: &Error,
    ) -> Self {
        Self {
            source: Some(GuardSource::Validation(Box::new(source))),
            last_problem: validation_problem(cause),
        }
    }
}

impl GuardSource {
    fn release(&mut self) -> Result<()> {
        match self {
            Self::Recovery(source) => source.release(),
            Self::Validation(source) => source.release(),
        }
    }

    fn problem(&self, error: &Error) -> PublicationProblem {
        match self {
            Self::Recovery(_) => problem(error),
            Self::Validation(_) => validation_problem(error),
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

    pub(crate) fn open_current(
        path: &Path,
        mode: CurrentSourceMode,
        cancellation: &CancellationToken,
    ) -> std::result::Result<Self, SourceOpenFailure> {
        cancellation.check().map_err(open_problem)?;
        match mode {
            CurrentSourceMode::Immutable => BasicSource::open_current(path, cancellation)
                .map(Self::Basic)
                .map_err(open_problem),
            CurrentSourceMode::Live => LiveSource::open_current(path, cancellation),
        }
    }

    pub(crate) fn mapping(&self) -> &Mapping {
        match self {
            Self::Basic(source) => &source.mapping,
            Self::Live(source) => &source.mapping,
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

fn terminal(source: Source, checked: Result<()>, released: Result<()>) -> SourceEnd {
    match released {
        Ok(()) => SourceEnd {
            cause: checked.err(),
            guard: None,
        },
        Err(cleanup) => SourceEnd {
            cause: checked.err().or_else(|| Some(cleanup_for_cause(&cleanup))),
            guard: Some(RecoverySourceCleanupGuard {
                source: Some(GuardSource::Recovery(Box::new(source))),
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

fn validation_problem(error: &Error) -> PublicationProblem {
    let os_code = match error {
        Error::Io(source) => source.raw_os_error(),
        _ => None,
    };
    PublicationProblem::new(error.code(), os_code, "validation source cleanup failed")
}
