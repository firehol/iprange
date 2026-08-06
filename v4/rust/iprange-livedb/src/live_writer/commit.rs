//! Durable alternate-meta publication.

use crate::bootstrap::{Bootstrap, MetaSelection, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::PAGE_SIZE;
use crate::database;
use crate::draft_store::DraftStore;
use crate::error::{combine_errors, Error, Result};
use crate::live_lock::Mode;
use crate::publication::CoordinationCleanup;

use super::{
    verify_pair, CommitCleanupArtifacts, CommitDurability, CommitResult, LiveWriter, State,
};

enum Phase {
    BeforePublication(Error),
    OutcomeUnknown(Error),
    Committed(Bootstrap),
}

impl LiveWriter {
    /// Publish all pending changes through the alternate metadata page.
    pub fn commit(&mut self, cancellation: &CancellationToken) -> Result<CommitResult> {
        self.commit_with(cancellation)
    }

    pub(crate) fn commit_operation(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<CommitResult> {
        self.commit_with(cancellation)
    }

    fn commit_with(&mut self, cancellation: &CancellationToken) -> Result<CommitResult> {
        let attempt = self.commit_attempt()?;
        if let Err(cause) = self.prepare_and_lock(cancellation) {
            let cause = self.abort_after(cause);
            return Ok(self.failed_result(attempt, CommitDurability::NotCommitted, cause));
        }
        let mut result = self.finish_commit_locked_with(attempt, cancellation);
        self.apply_commit_unlock(&mut result, self.sidecar.unlock_gate());
        Ok(result)
    }

    fn commit_attempt(&mut self) -> Result<([u8; 16], u64, [u8; 16])> {
        self.require_healthy()?;
        self.require_operation_owned()?;
        if self
            .draft
            .as_ref()
            .is_some_and(|draft| draft.workflow_input_open())
        {
            return Err(Error::WrongState("workflow input is not finished"));
        }
        if self.draft.as_ref().is_some_and(|draft| !draft.changed()) {
            self.discard_draft()?;
            return Err(Error::NoPendingTransaction);
        }
        self.draft
            .as_ref()
            .filter(|draft| draft.changed())
            .map(|draft| {
                (
                    draft.meta.database_id,
                    draft.meta.txn_id,
                    draft.meta.commit_nonce,
                )
            })
            .ok_or(Error::NoPendingTransaction)
    }

    fn prepare_and_lock(&mut self, cancellation: &CancellationToken) -> Result<()> {
        cancellation.check()?;
        self.prepare_with(cancellation)?;
        cancellation.check()?;
        self.sidecar
            .lock_gate_cancellable(Mode::Exclusive, cancellation)
    }

    pub(super) fn finish_commit_locked(
        &mut self,
        attempt: ([u8; 16], u64, [u8; 16]),
        cancellation: &CancellationToken,
    ) -> CommitResult {
        self.finish_commit_locked_with(attempt, cancellation)
    }

    fn finish_commit_locked_with(
        &mut self,
        attempt: ([u8; 16], u64, [u8; 16]),
        cancellation: &CancellationToken,
    ) -> CommitResult {
        match self.commit_locked(cancellation) {
            Phase::BeforePublication(cause) => {
                let cause = self.abort_after(cause);
                self.failed_result(attempt, CommitDurability::NotCommitted, cause)
            }
            Phase::OutcomeUnknown(cause) => {
                self.draft = None;
                self.unproved_tail_end = None;
                self.state = State::OutcomeUnknown;
                self.failed_result(attempt, CommitDurability::OutcomeUnknown, cause)
            }
            Phase::Committed(base) => {
                self.base = base;
                self.draft = None;
                self.unproved_tail_end = None;
                CommitResult {
                    attempted_database_id: attempt.0,
                    directory_identity: self.directory_identity,
                    main_identity: self.main_public_identity,
                    attempted_transaction_id: attempt.1,
                    attempted_commit_nonce: attempt.2,
                    durability: CommitDurability::Committed,
                    cleanup: CommitCleanupArtifacts::clean(),
                    coordination_cleanup: CoordinationCleanup::None,
                    cause: None,
                }
            }
        }
    }

    pub(super) fn apply_commit_unlock(&mut self, result: &mut CommitResult, unlock: Result<()>) {
        if let Err(cause) = unlock {
            self.state = State::Unusable;
            result.cause = Some(match result.cause.take() {
                Some(primary) => combine_errors(primary, Err(cause)),
                None => cause,
            });
            result.coordination_cleanup = CoordinationCleanup::RetainedWriterCloseRequired;
        }
    }

    pub(super) fn prepare_with_cancellation(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.prepare_with(cancellation)
    }

    fn prepare_with(&mut self, cancellation: &CancellationToken) -> Result<()> {
        let draft = self.draft.as_mut().unwrap();
        let mut store = DraftStore::new(
            &mut self.mapping,
            self.base.meta.page_count,
            self.budget.pages(),
            draft,
        );
        let mut check = || cancellation.check();
        store.prepare_with_checkpoint(&mut check)
    }

    fn commit_locked(&mut self, cancellation: &CancellationToken) -> Phase {
        if let Err(error) = cancellation.check() {
            return Phase::BeforePublication(error);
        }
        match self.prepublication_checks(cancellation) {
            Ok(()) => {}
            Err(error) => return Phase::BeforePublication(error),
        }
        if let Err(error) = cancellation.check() {
            return Phase::BeforePublication(error);
        }
        crate::fault::crash("commit.before_private_sync");
        let committed_bytes = self.draft.as_ref().unwrap().meta.page_count * PAGE_SIZE as u64;
        if let Err(error) = self.mapping.resize(committed_bytes) {
            return Phase::BeforePublication(error);
        }
        let data_offset = (2 * PAGE_SIZE) as u64;
        if committed_bytes > data_offset {
            if let Err(error) = self
                .mapping
                .flush_range(data_offset, committed_bytes - data_offset)
            {
                return Phase::BeforePublication(error);
            }
        }
        if let Err(error) = self.mapping.sync_file() {
            return Phase::BeforePublication(error);
        }
        crate::fault::crash("commit.after_private_sync");
        if let Err(error) = cancellation.check() {
            return Phase::BeforePublication(error);
        }

        let meta = self.draft.as_ref().unwrap().meta;
        let target_page = 1 - self.base.selected_meta_page;
        let encoded = self
            .mapping
            .page_mut(u32::from(target_page), meta.page_count)
            .and_then(|page| meta.encode_mapped(page));
        if let Err(error) = encoded {
            return Phase::OutcomeUnknown(error);
        }
        crate::fault::crash("commit.after_meta_write");
        if let Err(error) = self
            .mapping
            .flush_page(u32::from(target_page), meta.page_count)
        {
            return Phase::OutcomeUnknown(error);
        }
        if let Err(error) = self.mapping.sync_file() {
            return Phase::OutcomeUnknown(error);
        }
        crate::fault::crash("commit.after_meta_sync");
        Phase::Committed(Bootstrap {
            meta,
            selection: MetaSelection::ProvenCurrent,
            selected_meta_page: target_page,
            committed_bytes,
            physical_bytes: committed_bytes,
        })
    }

    fn prepublication_checks(&self, cancellation: &CancellationToken) -> Result<()> {
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;
        self.require_unchanged_base()?;
        self.sidecar
            .scan_at_most_cancellable(self.base.meta.txn_id, cancellation)?;
        self.require_draft_length()?;
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)
    }

    pub(super) fn require_unchanged_base(&self) -> Result<()> {
        let physical_bytes = self.mapping.file().metadata()?.len();
        let selected =
            database::bootstrap_mapping(&self.mapping, physical_bytes, OpenMode::Writer)?;
        if selected.meta != self.base.meta {
            return Err(Error::WrongMode(
                "committed generation changed under the writer",
            ));
        }
        Ok(())
    }

    fn require_draft_length(&self) -> Result<()> {
        let expected = self
            .draft
            .as_ref()
            .unwrap()
            .meta
            .page_count
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(Error::ArithmeticOverflow("committed file length"))?;
        if self.mapping.file().metadata()?.len() < expected || self.mapping.len() < expected {
            return Err(Error::Corrupt("draft file length is inconsistent"));
        }
        Ok(())
    }

    fn failed_result(
        &self,
        attempt: ([u8; 16], u64, [u8; 16]),
        durability: CommitDurability,
        cause: Error,
    ) -> CommitResult {
        let cleanup = if self.draft.is_some() {
            self.unpublished_tail_cleanup(cause.code())
        } else {
            CommitCleanupArtifacts::clean()
        };
        let coordination_cleanup = if durability == CommitDurability::OutcomeUnknown
            || self.state != State::Healthy
            || !cleanup.is_empty()
        {
            CoordinationCleanup::RetainedWriterCloseRequired
        } else {
            CoordinationCleanup::None
        };
        CommitResult {
            attempted_database_id: attempt.0,
            directory_identity: self.directory_identity,
            main_identity: self.main_public_identity,
            attempted_transaction_id: attempt.1,
            attempted_commit_nonce: attempt.2,
            durability,
            cleanup,
            coordination_cleanup,
            cause: Some(cause),
        }
    }
}
