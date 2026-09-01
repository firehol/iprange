//! Single live writer and direct range transaction surface.

mod close;
mod commit;
mod create;
mod direct;
mod direct_workflow;
mod feed_lifecycle;
mod feed_workflow;
mod history_projection;
mod membership;
mod membership_import;
mod reclaim;
mod result;
mod structured;
mod workflow;

use std::path::{Path, PathBuf};

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, ValueKind, MAX_METADATA_UNCOMPRESSED};
use crate::error::{finish_with_cleanup, Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_namespace::Identity;
use crate::live_sidecar::{Sidecar, MAIN_LIFETIME_LOCK};
use crate::process_identity::ProcessIdentity;
use crate::validation::LocalFileIdentity;
use crate::writer_core::{WriterCore, WriterEdit};

pub use create::{create_live, CreateResult, CreationState};
pub(crate) use direct::DirectState;
pub use direct::DirectTransaction;
pub(crate) use direct_workflow::ExactDirectState;
pub use direct_workflow::{DirectReplacement, FirstSeenRefresh, LastSeenRefresh};
pub(crate) use feed_workflow::ExactFeedState;
pub use feed_workflow::{CreateFeed, ReplaceFeed};
pub use history_projection::{
    FinishedHistoryProjection, HistoryProjectionSource, PreparedHistoryProjection,
};
pub(crate) use membership::MembershipState;
pub use membership::{FeedRef, MembershipRef, MembershipTransaction, TransactionFeedCursor};
pub(crate) use membership_import::{finish_import_state, Source as MembershipImportStateSource};
pub use membership_import::{MembershipImport, MembershipImportSource};
pub use reclaim::ReclaimResult;
pub use result::{
    AbortOutcome, AbortResult, CloseOutcome, CloseResult, CommitCleanupArtifact,
    CommitCleanupArtifacts, LocalBasename,
};
pub use structured::{StructureRef, StructuredTransaction};
pub(crate) use workflow::{FinishedState, PreparedState};
pub use workflow::{FinishedWorkflow, PreparedFeedChange, PreparedWorkflow};

/// Maximum resources retained by one writer transaction.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct TransactionBudget {
    pub max_heap_bytes: u64,
    pub max_private_pages: u64,
    pub max_file_growth_pages: u64,
    pub max_open_files: u32,
}

impl TransactionBudget {
    fn validate(self) -> Result<Self> {
        if self.max_open_files < 2 {
            return Err(Error::BudgetExceeded(
                "a live writer requires two open files",
            ));
        }
        Ok(self)
    }
}

pub use result::{CommitDurability, CommitResult};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum State {
    Healthy,
    OutcomeUnknown,
    Unusable,
    ClosingWriter(bool),
    ClosingGate(bool),
    ClosingMain(bool),
    Closed,
}

/// Exclusive writer for one live database.
#[derive(Debug)]
pub struct LiveWriter {
    pub(super) core: WriterCore,
    pub(super) main_path: PathBuf,
    pub(super) main_identity: Identity,
    pub(super) directory_identity: LocalFileIdentity,
    pub(super) main_public_identity: LocalFileIdentity,
    pub(super) main_basename: LocalBasename,
    pub(super) sidecar: Sidecar,
    state: State,
    owner_identity: ProcessIdentity,
}

struct OpenedMain {
    path: PathBuf,
    core: WriterCore,
    identity: Identity,
    directory_identity: LocalFileIdentity,
    public_identity: LocalFileIdentity,
    basename: LocalBasename,
}

impl LiveWriter {
    pub fn address_family(&self) -> AddressFamily {
        self.core.base_info().address_family
    }

    /// Open the only writer lease without validating either page graph.
    pub fn open(
        path: impl AsRef<Path>,
        budget: TransactionBudget,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        live_lock::require_live_supported()?;
        cancellation.check()?;
        let budget = budget.validate()?;
        let main = open_main(path.as_ref(), budget, cancellation)?;
        let sidecar = Sidecar::open(&main.path, main.core.base_info().database_id)?;
        sidecar.lock_gate_cancellable(Mode::Exclusive, cancellation)?;
        let mut core = main.core;
        let opened = open_locked(&mut core, &main.path, main.identity, &sidecar, cancellation);
        finish_with_cleanup(opened, sidecar.unlock_gate())?;

        Ok(Self {
            core,
            main_path: main.path,
            main_identity: main.identity,
            directory_identity: main.directory_identity,
            main_public_identity: main.public_identity,
            main_basename: main.basename,
            sidecar,
            state: State::Healthy,
            owner_identity: ProcessIdentity::capture(),
        })
    }

    /// Compress and stage one exact opaque metadata replacement.
    ///
    /// The minimum heap budget is the zlib stored-block bound from the v4
    /// specification. With 512 KiB of additional budget, the writer first
    /// attempts normal DEFLATE compression and falls back to stored blocks.
    pub fn set_metadata_json(
        &mut self,
        input: &[u8],
        cancellation: &CancellationToken,
    ) -> Result<bool> {
        self.require_healthy()?;
        if self.core.has_draft() {
            return Err(Error::WrongState(
                "metadata-only mutation requires a clean writer",
            ));
        }
        cancellation.check()?;
        let result = self.stage_metadata_json(input);
        if result.is_ok() {
            self.check_metadata_cancellation(cancellation)?;
        }
        result
    }

    pub(super) fn stage_metadata_json(&mut self, input: &[u8]) -> Result<bool> {
        self.require_healthy()?;
        self.require_metadata_stage_available()?;
        if input.len() as u64 > MAX_METADATA_UNCOMPRESSED {
            return Err(Error::InvalidArgument("metadata exceeds 20 MiB"));
        }
        self.mutate(|edit| edit.set_metadata(input))
    }

    /// Stage metadata absence, or report an already-absent no-op.
    pub fn clear_metadata_json(&mut self, cancellation: &CancellationToken) -> Result<bool> {
        self.require_healthy()?;
        if self.core.has_draft() {
            return Err(Error::WrongState(
                "metadata-only mutation requires a clean writer",
            ));
        }
        cancellation.check()?;
        let result = self.stage_clear_metadata_json();
        if result.is_ok() {
            self.check_metadata_cancellation(cancellation)?;
        }
        result
    }

    pub(super) fn stage_clear_metadata_json(&mut self) -> Result<bool> {
        self.require_healthy()?;
        self.require_metadata_stage_available()?;
        if self.core.metadata_json_len().is_none() {
            return Ok(false);
        }
        self.mutate(|edit| edit.clear_metadata())
    }

    fn check_metadata_cancellation(&mut self, cancellation: &CancellationToken) -> Result<()> {
        cancellation.check().map_err(|cause| {
            if self.core.has_draft() {
                self.abort_after(cause)
            } else {
                cause
            }
        })
    }

    /// Exact decompressed length of the committed or staged metadata.
    pub fn metadata_json_len(&self) -> Result<Option<u64>> {
        self.require_healthy()?;
        self.require_operation_owned()?;
        Ok(self.core.metadata_json_len())
    }

    /// Fill caller storage from the committed or staged metadata.
    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.require_healthy()?;
        self.require_operation_owned()?;
        self.core.read_metadata_json(output)
    }

    /// Return the complete committed or staged bounded metadata value.
    pub fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        self.require_healthy()?;
        self.require_operation_owned()?;
        self.core.metadata_json()
    }

    /// Discard all unpublished changes.
    pub fn abort(&mut self) -> Result<AbortResult> {
        self.require_healthy()?;
        if !self.core.has_draft() {
            return Err(Error::NoPendingTransaction);
        }
        match self.discard_draft() {
            Ok(()) => Ok(AbortResult::aborted()),
            Err(cause) => Ok(AbortResult::incomplete(
                self.unpublished_tail_cleanup(cause.code()),
                cause,
            )),
        }
    }

    #[inline]
    pub(crate) fn mutate<T>(
        &mut self,
        operation: impl FnOnce(&mut WriterEdit<'_>) -> Result<T>,
    ) -> Result<T> {
        self.require_healthy()?;
        let started = !self.core.has_draft();
        let result = self.core.edit(operation);
        match result {
            Ok(changed) => {
                if started && !self.core.draft_changed() {
                    self.discard_draft()?;
                }
                Ok(changed)
            }
            Err(cause) => Err(self.abort_after(cause)),
        }
    }

    fn require_direct(&self, family: AddressFamily, ordered: bool) -> Result<()> {
        self.require_healthy()?;
        self.require_metadata_stage_available()?;
        if self.core.workflow_active() {
            return Err(Error::WrongState(
                "an exact workflow owns the pending transaction",
            ));
        }
        if !ordered {
            return Err(Error::InvalidArgument("range start exceeds range end"));
        }
        if self.core.base_info().value_kind != ValueKind::Direct {
            return Err(Error::WrongValueKind(
                "direct mutation requires a direct database",
            ));
        }
        if self.core.base_info().address_family != family {
            return Err(Error::WrongAddressFamily(
                "direct mutation does not match the database family",
            ));
        }
        Ok(())
    }

    fn require_metadata_stage_available(&self) -> Result<()> {
        self.require_operation_owned()?;
        if self.core.workflow_input_open() {
            return Err(Error::WrongState("workflow input is not finished"));
        }
        if self.core.metadata_staged() {
            return Err(Error::WrongState(
                "this transaction already staged metadata",
            ));
        }
        Ok(())
    }

    fn require_operation_owned(&self) -> Result<()> {
        if self.core.operation_abandoned() {
            Err(Error::WrongState("operation handle was dropped"))
        } else {
            Ok(())
        }
    }

    fn require_healthy(&self) -> Result<()> {
        self.require_owner()?;
        match self.state {
            State::Healthy => Ok(()),
            State::OutcomeUnknown => {
                Err(Error::WrongMode("writer has an unresolved commit outcome"))
            }
            State::Unusable => Err(Error::WrongMode("writer is unusable")),
            State::ClosingWriter(_) | State::ClosingGate(_) | State::ClosingMain(_) => {
                Err(Error::WrongState("writer is closing"))
            }
            State::Closed => Err(Error::WrongState("writer is closed")),
        }
    }

    fn require_owner(&self) -> Result<()> {
        if !self.owner_identity.is_current() {
            return Err(Error::ForkedHandle);
        }
        Ok(())
    }

    fn discard_draft(&mut self) -> Result<()> {
        match self.discard_draft_inner() {
            Ok(()) => Ok(()),
            Err(error) => {
                self.state = State::Unusable;
                Err(error)
            }
        }
    }

    fn discard_draft_inner(&mut self) -> Result<()> {
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;
        self.core.discard_unpublished()?;
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;
        Ok(())
    }

    pub(crate) fn abort_after(&mut self, cause: Error) -> Error {
        let fatal = matches!(cause, Error::Io(_) | Error::Format(_) | Error::Corrupt(_));
        let result = self.abort_after_source(cause);
        if fatal {
            self.state = State::Unusable;
        }
        result
    }

    fn abort_after_source(&mut self, cause: Error) -> Error {
        if let Err(cleanup) = self.discard_draft() {
            self.state = State::Unusable;
            return Error::TransactionAborted(Box::new(Error::CleanupIncomplete {
                cause: Box::new(cause),
                cleanup: Box::new(cleanup),
            }));
        }
        Error::TransactionAborted(Box::new(cause))
    }

    fn unpublished_tail_cleanup(
        &self,
        cleanup_error: crate::error::ErrorCode,
    ) -> CommitCleanupArtifacts {
        let tail = self.core.tail_cleanup_state();
        let observed_tail_end_exclusive = tail.observed_tail_end_exclusive;
        let Some(observed_tail_end_exclusive) = observed_tail_end_exclusive else {
            return CommitCleanupArtifacts::clean();
        };
        CommitCleanupArtifacts::tail(CommitCleanupArtifact {
            directory_identity: self.directory_identity,
            main_basename: self.main_basename,
            main_identity: self.main_public_identity,
            expected_database_id: tail.database_id,
            target_transaction_id: tail.transaction_id,
            target_commit_nonce: tail.commit_nonce,
            committed_target_length: tail.committed_length,
            observed_tail_end_exclusive: Some(observed_tail_end_exclusive),
            cleanup_error,
        })
    }
}

fn open_locked(
    core: &mut WriterCore,
    main_path: &Path,
    main_identity: Identity,
    sidecar: &Sidecar,
    cancellation: &CancellationToken,
) -> Result<()> {
    verify_pair(main_path, main_identity, sidecar)?;
    let selected = core.select_committed()?;
    if selected.database_id != sidecar.header.database_id {
        return Err(Error::WrongMode(
            "reader table belongs to a different database",
        ));
    }
    sidecar.scan_at_most_cancellable(selected.transaction_id, cancellation)?;
    cancellation.check()?;
    sidecar.claim_writer()?;
    cancellation.check()?;
    core.trim_committed_tail()?;
    cancellation.check()?;
    verify_pair(main_path, main_identity, sidecar)?;
    Ok(())
}

fn open_main(
    path: &Path,
    budget: TransactionBudget,
    cancellation: &CancellationToken,
) -> Result<OpenedMain> {
    let path = path.to_path_buf();
    let file = crate::live_namespace::open_rw(&path)?;
    let identity = crate::live_namespace::identity(&file)?;
    let directory_identity = crate::live_namespace::parent_identity(&path)?;
    let public_identity = crate::live_namespace::public_identity(identity);
    let basename = LocalBasename::from_path(&path)?;
    live_lock::lock_file_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Shared, cancellation)?;
    crate::live_namespace::verify_path(&path, identity)?;
    let core = WriterCore::map_writer(
        file,
        budget.max_heap_bytes,
        budget.max_private_pages,
        budget.max_file_growth_pages,
    )?;
    crate::live_cleanup::require_main_available(&path, identity, core.base_info().database_id)?;
    Ok(OpenedMain {
        path,
        core,
        identity,
        directory_identity,
        public_identity,
        basename,
    })
}

fn verify_pair(main_path: &Path, main_identity: Identity, sidecar: &Sidecar) -> Result<()> {
    crate::live_namespace::verify_path(main_path, main_identity)?;
    sidecar.verify_path()?;
    sidecar.verify_header()
}
