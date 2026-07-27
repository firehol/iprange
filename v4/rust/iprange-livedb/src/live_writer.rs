//! Single live writer and direct range transaction surface.

mod close;
mod commit;
mod create;
mod direct;
mod direct_workflow;
mod feed_lifecycle;
mod feed_workflow;
mod membership;
mod membership_import;
mod reclaim;
mod result;
mod workflow;

use std::fs::File;
use std::path::{Path, PathBuf};

use crate::bootstrap::{Bootstrap, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind, MAX_METADATA_UNCOMPRESSED};
use crate::database;
use crate::draft_store::{Draft, DraftStore, PageBudget};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, Identity, Sidecar, MAIN_LIFETIME_LOCK};
use crate::metadata;
use crate::random;
use crate::validation::LocalFileIdentity;

pub use create::{create_live, CreateResult, CreationState};
pub(crate) use create::{empty_meta, write_empty_main};
pub(crate) use direct::DirectState;
pub use direct::DirectTransaction;
pub(crate) use direct_workflow::ExactDirectState;
pub use direct_workflow::{DirectReplacement, RetentionRefresh};
pub(crate) use feed_workflow::ExactFeedState;
pub use feed_workflow::{CreateFeed, ReplaceFeed};
pub(crate) use membership::MembershipState;
pub use membership::{FeedRef, MembershipRef, MembershipTransaction, TransactionFeedCursor};
pub(crate) use membership_import::{finish_import_state, Source as MembershipImportStateSource};
pub use membership_import::{MembershipImport, MembershipImportSource};
pub use reclaim::ReclaimResult;
pub use result::{
    AbortOutcome, AbortResult, CloseOutcome, CloseResult, CommitCleanupArtifact,
    CommitCleanupArtifacts, LocalBasename,
};
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

    fn pages(self) -> PageBudget {
        PageBudget {
            max_heap_bytes: self.max_heap_bytes,
            max_private_pages: self.max_private_pages,
            max_growth_pages: self.max_file_growth_pages,
        }
    }
}

pub use result::{CommitDurability, CommitResult};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum State {
    Healthy,
    OutcomeUnknown,
    Unusable,
    Closed,
}

/// Exclusive writer for one live database.
#[derive(Debug)]
pub struct LiveWriter {
    pub(super) file: File,
    pub(super) main_path: PathBuf,
    pub(super) main_identity: Identity,
    pub(super) directory_identity: LocalFileIdentity,
    pub(super) main_public_identity: LocalFileIdentity,
    pub(super) main_basename: LocalBasename,
    pub(super) sidecar: Sidecar,
    pub(super) base: Bootstrap,
    pub(super) budget: TransactionBudget,
    pub(super) draft: Option<Draft>,
    state: State,
    owner_pid: u32,
}

struct OpenedMain {
    path: PathBuf,
    file: File,
    identity: Identity,
    directory_identity: LocalFileIdentity,
    public_identity: LocalFileIdentity,
    basename: LocalBasename,
    initial: Bootstrap,
}

impl LiveWriter {
    /// Open the only writer lease without validating either page graph.
    pub fn open(
        path: impl AsRef<Path>,
        budget: TransactionBudget,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        cancellation.check()?;
        let budget = budget.validate()?;
        let main = open_main(path.as_ref(), cancellation)?;
        let sidecar = Sidecar::open(&main.path, main.initial.meta.database_id)?;
        sidecar.lock_gate_cancellable(Mode::Exclusive, cancellation)?;
        let opened = open_locked(
            &main.file,
            &main.path,
            main.identity,
            &sidecar,
            cancellation,
        );
        let unlocked = sidecar.unlock_gate();
        let base = gate_result(opened, unlocked)?;

        Ok(Self {
            file: main.file,
            main_path: main.path,
            main_identity: main.identity,
            directory_identity: main.directory_identity,
            main_public_identity: main.public_identity,
            main_basename: main.basename,
            sidecar,
            base,
            budget,
            draft: None,
            state: State::Healthy,
            owner_pid: std::process::id(),
        })
    }

    /// Assign one inclusive IPv4 interval in exact call order.
    fn assign_direct_v4(&mut self, from: Ipv4Key, to: Ipv4Key, value: u32) -> Result<bool> {
        self.require_direct(AddressFamily::Ipv4, from <= to)?;
        self.mutate(|store| store.assign_v4(from, to, value))
    }

    /// Assign one inclusive IPv6 interval in exact call order.
    fn assign_direct_v6(&mut self, from: Ipv6Key, to: Ipv6Key, value: u32) -> Result<bool> {
        self.require_direct(AddressFamily::Ipv6, from <= to)?;
        self.mutate(|store| store.assign_v6(from, to, value))
    }

    /// Remove values from one inclusive IPv4 interval.
    fn clear_direct_v4(&mut self, from: Ipv4Key, to: Ipv4Key) -> Result<bool> {
        self.require_direct(AddressFamily::Ipv4, from <= to)?;
        self.mutate(|store| store.clear_v4(from, to))
    }

    /// Remove values from one inclusive IPv6 interval.
    fn clear_direct_v6(&mut self, from: Ipv6Key, to: Ipv6Key) -> Result<bool> {
        self.require_direct(AddressFamily::Ipv6, from <= to)?;
        self.mutate(|store| store.clear_v6(from, to))
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
        if self.draft.is_some() {
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
            return Err(Error::InvalidArgument("metadata exceeds 1 MiB"));
        }
        self.mutate_uncached(|store| store.set_metadata(input))
    }

    /// Stage metadata absence, or report an already-absent no-op.
    pub fn clear_metadata_json(&mut self, cancellation: &CancellationToken) -> Result<bool> {
        self.require_healthy()?;
        if self.draft.is_some() {
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
        if self.current_meta().metadata_root == 0 {
            return Ok(false);
        }
        self.mutate_uncached(|store| store.clear_metadata())
    }

    fn check_metadata_cancellation(&mut self, cancellation: &CancellationToken) -> Result<()> {
        cancellation.check().map_err(|cause| {
            if self.draft.is_some() {
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
        let meta = self.current_meta();
        Ok((meta.metadata_root != 0).then_some(meta.metadata_uncompressed_len))
    }

    /// Fill caller storage from the committed or staged metadata.
    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.require_healthy()?;
        self.require_operation_owned()?;
        metadata::read(&self.file, &self.current_meta(), output)
    }

    /// Return the complete committed or staged bounded metadata value.
    pub fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        self.require_healthy()?;
        self.require_operation_owned()?;
        metadata::read_vec(&self.file, &self.current_meta())
    }

    /// Discard all unpublished changes.
    pub fn abort(&mut self) -> Result<AbortResult> {
        self.require_healthy()?;
        if self.draft.is_none() {
            return Err(Error::NoPendingTransaction);
        }
        match self.discard_draft() {
            Ok(()) => Ok(AbortResult::aborted()),
            Err(cause) => Ok(AbortResult::incomplete(
                self.unpublished_tail_artifact(cause.code()),
                cause,
            )),
        }
    }

    pub(crate) fn mutate<T>(
        &mut self,
        operation: impl FnOnce(&mut DraftStore<'_>) -> Result<T>,
    ) -> Result<T> {
        self.mutate_with_cache(true, operation)
    }

    fn mutate_uncached<T>(
        &mut self,
        operation: impl FnOnce(&mut DraftStore<'_>) -> Result<T>,
    ) -> Result<T> {
        self.mutate_with_cache(false, operation)
    }

    fn mutate_with_cache<T>(
        &mut self,
        cache_pages: bool,
        operation: impl FnOnce(&mut DraftStore<'_>) -> Result<T>,
    ) -> Result<T> {
        self.require_healthy()?;
        let started = self.draft.is_none();
        if started {
            self.draft = Some(Draft::new(self.base.meta, random::nonzero_128()?)?);
        }

        let result = {
            let draft = self.draft.as_mut().unwrap();
            let mut store = if cache_pages {
                DraftStore::new_cached(
                    &self.file,
                    self.base.meta.page_count,
                    self.budget.pages(),
                    draft,
                )
            } else {
                DraftStore::new_uncached(
                    &self.file,
                    self.base.meta.page_count,
                    self.budget.pages(),
                    draft,
                )
            };
            operation(&mut store)
        };
        match result {
            Ok(changed) => {
                if started && !self.draft.as_ref().unwrap().changed() {
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
        if self.draft.as_ref().is_some_and(Draft::workflow_active) {
            return Err(Error::WrongState(
                "an exact workflow owns the pending transaction",
            ));
        }
        if !ordered {
            return Err(Error::InvalidArgument("range start exceeds range end"));
        }
        if self.base.meta.value_kind != ValueKind::Direct {
            return Err(Error::WrongValueKind(
                "direct mutation requires a direct database",
            ));
        }
        if self.base.meta.address_family != family {
            return Err(Error::WrongAddressFamily(
                "direct mutation does not match the database family",
            ));
        }
        Ok(())
    }

    fn require_metadata_stage_available(&self) -> Result<()> {
        self.require_operation_owned()?;
        if self.draft.as_ref().is_some_and(Draft::workflow_input_open) {
            return Err(Error::WrongState("workflow input is not finished"));
        }
        if self.draft.as_ref().is_some_and(Draft::metadata_staged) {
            return Err(Error::WrongState(
                "this transaction already staged metadata",
            ));
        }
        Ok(())
    }

    fn require_operation_owned(&self) -> Result<()> {
        if self.draft.as_ref().is_some_and(Draft::operation_abandoned) {
            Err(Error::WrongState("operation handle was dropped"))
        } else {
            Ok(())
        }
    }

    fn current_meta(&self) -> MetaV4 {
        self.draft
            .as_ref()
            .map(Draft::metadata_meta)
            .unwrap_or(self.base.meta)
    }

    fn require_healthy(&self) -> Result<()> {
        self.require_owner()?;
        match self.state {
            State::Healthy => Ok(()),
            State::OutcomeUnknown => {
                Err(Error::WrongMode("writer has an unresolved commit outcome"))
            }
            State::Unusable => Err(Error::WrongMode("writer is unusable")),
            State::Closed => Err(Error::WrongState("writer is closed")),
        }
    }

    fn require_owner(&self) -> Result<()> {
        if self.owner_pid != std::process::id() {
            return Err(Error::ForkedHandle);
        }
        Ok(())
    }

    fn discard_draft(&mut self) -> Result<()> {
        match self.file.set_len(self.base.committed_bytes) {
            Ok(()) => {
                self.draft = None;
                Ok(())
            }
            Err(error) => {
                self.state = State::Unusable;
                Err(error.into())
            }
        }
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

    fn unpublished_tail_artifact(
        &self,
        cleanup_error: crate::error::ErrorCode,
    ) -> CommitCleanupArtifact {
        CommitCleanupArtifact {
            directory_identity: self.directory_identity,
            main_basename: self.main_basename,
            main_identity: self.main_public_identity,
            expected_database_id: self.base.meta.database_id,
            target_transaction_id: self.base.meta.txn_id,
            target_commit_nonce: self.base.meta.commit_nonce,
            committed_target_length: self.base.committed_bytes,
            observed_tail_end_exclusive: self.file.metadata().ok().map(|metadata| metadata.len()),
            cleanup_error,
        }
    }
}

fn open_locked(
    file: &File,
    main_path: &Path,
    main_identity: Identity,
    sidecar: &Sidecar,
    cancellation: &CancellationToken,
) -> Result<Bootstrap> {
    verify_pair(main_path, main_identity, sidecar)?;
    let base = select_base(file, sidecar, cancellation)?;
    cancellation.check()?;
    sidecar.claim_writer()?;
    cancellation.check()?;
    trim_tail(file, base)?;
    cancellation.check()?;
    verify_pair(main_path, main_identity, sidecar)?;
    Ok(Bootstrap {
        physical_bytes: base.committed_bytes,
        ..base
    })
}

fn open_main(path: &Path, cancellation: &CancellationToken) -> Result<OpenedMain> {
    let path = path.to_path_buf();
    let file = live_sidecar::open_rw(&path)?;
    let identity = live_sidecar::identity(&file)?;
    let directory_identity = live_sidecar::parent_identity(&path)?;
    let public_identity = live_sidecar::public_identity(identity);
    let basename = LocalBasename::from_path(&path)?;
    live_lock::lock_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Shared, cancellation)?;
    live_sidecar::verify_path(&path, identity)?;
    let initial = database::bootstrap_file(&file, OpenMode::Writer)?;
    crate::live_cleanup::require_main_available(&path, identity, initial.meta.database_id)?;
    Ok(OpenedMain {
        path,
        file,
        identity,
        directory_identity,
        public_identity,
        basename,
        initial,
    })
}

fn select_base(
    file: &File,
    sidecar: &Sidecar,
    cancellation: &CancellationToken,
) -> Result<Bootstrap> {
    let base = database::bootstrap_file(file, OpenMode::Writer)?;
    if base.meta.database_id != sidecar.header.database_id {
        return Err(Error::WrongMode(
            "reader table belongs to a different database",
        ));
    }
    sidecar.scan_at_most_cancellable(base.meta.txn_id, cancellation)?;
    Ok(base)
}

fn verify_pair(main_path: &Path, main_identity: Identity, sidecar: &Sidecar) -> Result<()> {
    live_sidecar::verify_path(main_path, main_identity)?;
    sidecar.verify_path()?;
    sidecar.verify_header()
}

fn trim_tail(file: &File, base: Bootstrap) -> Result<()> {
    if base.physical_bytes != base.committed_bytes {
        file.set_len(base.committed_bytes)?;
        file.sync_all()?;
    }
    Ok(())
}

fn gate_result<T>(operation: Result<T>, unlock: Result<()>) -> Result<T> {
    match (operation, unlock) {
        (Ok(value), Ok(())) => Ok(value),
        (Err(error), _) | (Ok(_), Err(error)) => Err(error),
    }
}
