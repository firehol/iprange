//! Durable alternate-meta publication.

use crate::bootstrap::{Bootstrap, MetaSelection, OpenMode};
use crate::contract::PAGE_SIZE;
use crate::database;
use crate::draft_store::DraftStore;
use crate::error::{Error, Result};
use crate::file_io;
use crate::live_lock::Mode;

use super::{verify_pair, CommitDurability, CommitResult, LiveWriter, State};

enum Phase {
    BeforePublication(Error),
    OutcomeUnknown(Error),
    Committed(Bootstrap),
}

impl LiveWriter {
    /// Publish all pending changes through the alternate metadata page.
    pub fn commit(&mut self) -> Result<CommitResult> {
        self.require_healthy()?;
        if self.draft.as_ref().is_some_and(|draft| !draft.changed()) {
            self.discard_draft()?;
            return Err(Error::NoPendingTransaction);
        }
        let attempt = self
            .draft
            .as_ref()
            .filter(|draft| draft.changed())
            .map(|draft| {
                (
                    draft.meta.database_id,
                    draft.meta.txn_id,
                    draft.meta.commit_nonce,
                )
            })
            .ok_or(Error::NoPendingTransaction)?;

        if let Err(cause) = self.prepare() {
            let cause = self.abort_after(cause);
            return Ok(result(attempt, CommitDurability::NotCommitted, cause));
        }
        if let Err(cause) = self.sidecar.lock_gate(Mode::Exclusive) {
            let cause = self.abort_after(cause);
            return Ok(result(attempt, CommitDurability::NotCommitted, cause));
        }

        let mut result = self.finish_commit_locked(attempt);
        self.apply_commit_unlock(&mut result, self.sidecar.unlock_gate());
        Ok(result)
    }

    pub(super) fn finish_commit_locked(
        &mut self,
        attempt: ([u8; 16], u64, [u8; 16]),
    ) -> CommitResult {
        match self.commit_locked() {
            Phase::BeforePublication(cause) => {
                let cause = self.abort_after(cause);
                result(attempt, CommitDurability::NotCommitted, cause)
            }
            Phase::OutcomeUnknown(cause) => {
                self.draft = None;
                self.state = State::OutcomeUnknown;
                result(attempt, CommitDurability::OutcomeUnknown, cause)
            }
            Phase::Committed(base) => {
                self.base = base;
                self.draft = None;
                CommitResult {
                    database_id: attempt.0,
                    transaction_id: attempt.1,
                    commit_nonce: attempt.2,
                    durability: CommitDurability::Committed,
                    cause: None,
                }
            }
        }
    }

    pub(super) fn apply_commit_unlock(&mut self, result: &mut CommitResult, unlock: Result<()>) {
        if let Err(cause) = unlock {
            self.state = State::Unusable;
            if result.cause.is_none() {
                result.cause = Some(cause);
            }
        }
    }

    pub(super) fn prepare(&mut self) -> Result<()> {
        let draft = self.draft.as_mut().unwrap();
        DraftStore::new(
            &self.file,
            self.base.meta.page_count,
            self.budget.pages(),
            draft,
        )
        .prepare()
    }

    fn commit_locked(&self) -> Phase {
        match self.prepublication_checks() {
            Ok(()) => {}
            Err(error) => return Phase::BeforePublication(error),
        }
        crate::fault::crash("commit.before_private_sync");
        if let Err(error) = self.file.sync_all() {
            return Phase::BeforePublication(error.into());
        }
        crate::fault::crash("commit.after_private_sync");

        let draft = self.draft.as_ref().unwrap();
        let target_page = 1 - self.base.selected_meta_page;
        let mut meta_page = [0; PAGE_SIZE];
        draft.meta.encode_into(&mut meta_page);
        if let Err(error) = file_io::write_exact_at(
            &self.file,
            &meta_page,
            u64::from(target_page) * PAGE_SIZE as u64,
        ) {
            return Phase::OutcomeUnknown(error);
        }
        crate::fault::crash("commit.after_meta_write");
        if let Err(error) = self.file.sync_all() {
            return Phase::OutcomeUnknown(error.into());
        }
        crate::fault::crash("commit.after_meta_sync");
        Phase::Committed(Bootstrap {
            meta: draft.meta,
            selection: MetaSelection::ProvenCurrent,
            selected_meta_page: target_page,
            committed_bytes: draft.meta.page_count * PAGE_SIZE as u64,
            physical_bytes: draft.meta.page_count * PAGE_SIZE as u64,
        })
    }

    fn prepublication_checks(&self) -> Result<()> {
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;
        self.require_unchanged_base()?;
        self.sidecar.scan_at_most(self.base.meta.txn_id)?;
        self.require_draft_length()?;
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)
    }

    pub(super) fn require_unchanged_base(&self) -> Result<()> {
        let selected = database::bootstrap_file(&self.file, OpenMode::Writer)?;
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
        if self.file.metadata()?.len() != expected {
            return Err(Error::Corrupt("draft file length is inconsistent"));
        }
        Ok(())
    }
}

fn result(
    attempt: ([u8; 16], u64, [u8; 16]),
    durability: CommitDurability,
    cause: Error,
) -> CommitResult {
    CommitResult {
        database_id: attempt.0,
        transaction_id: attempt.1,
        commit_nonce: attempt.2,
        durability,
        cause: Some(cause),
    }
}
