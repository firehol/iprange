//! Bounded reader-safe page reclamation.

use crate::draft_store::{Draft, DraftStore};
use crate::error::{Error, Result};
use crate::live_lock::Mode;
use crate::retirement::Reclamation;

use super::{verify_pair, CommitResult, LiveWriter, State};

/// Result of one clean-writer reclamation operation.
#[derive(Debug)]
pub enum ReclaimResult {
    /// No complete retirement transaction was safe and within both limits.
    NoChange,
    /// One selected prefix reached the normal commit path.
    Commit {
        transaction_count: u64,
        page_count: u64,
        commit: CommitResult,
    },
}

impl LiveWriter {
    /// Reclaim complete oldest safe retirement transactions and auto-publish.
    pub fn reclaim(&mut self, max_transactions: u64, max_pages: u64) -> Result<ReclaimResult> {
        self.require_reclaim(max_transactions, max_pages)?;
        let draft = Draft::new(self.base.meta, crate::random::nonzero_128()?)?;
        self.sidecar.lock_gate(Mode::Exclusive)?;
        let operation = self.reclaim_locked(draft, max_transactions, max_pages);
        self.finish_reclaim(operation)
    }

    fn require_reclaim(&self, max_transactions: u64, max_pages: u64) -> Result<()> {
        self.require_healthy()?;
        if self.draft.is_some() {
            return Err(Error::WrongMode("reclamation requires a clean writer"));
        }
        if max_transactions == 0 || max_pages == 0 {
            return Err(Error::InvalidArgument(
                "reclamation work limits must be nonzero",
            ));
        }
        Ok(())
    }

    fn finish_reclaim(
        &mut self,
        operation: Result<Option<(Reclamation, CommitResult)>>,
    ) -> Result<ReclaimResult> {
        match operation {
            Ok(None) => {
                self.sidecar.unlock_gate()?;
                Ok(ReclaimResult::NoChange)
            }
            Ok(Some((selection, mut commit))) => {
                let unlock = self.sidecar.unlock_gate();
                self.apply_commit_unlock(&mut commit, unlock);
                Ok(ReclaimResult::Commit {
                    transaction_count: selection.transactions,
                    page_count: selection.pages,
                    commit,
                })
            }
            Err(cause) => Err(self.finish_reclaim_error(cause)),
        }
    }

    fn finish_reclaim_error(&mut self, cause: Error) -> Error {
        let cause = if self.draft.is_some() {
            self.abort_after(cause)
        } else {
            cause
        };
        if self.sidecar.unlock_gate().is_err() {
            self.state = State::Unusable;
        }
        cause
    }

    fn reclaim_locked(
        &mut self,
        mut draft: Draft,
        max_transactions: u64,
        max_pages: u64,
    ) -> Result<Option<(Reclamation, CommitResult)>> {
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;
        self.require_unchanged_base()?;
        let oldest_reader = self.sidecar.oldest_reader(self.base.meta.txn_id)?;
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;

        let selection = DraftStore::new(
            &self.file,
            self.base.meta.page_count,
            self.budget.pages(),
            &mut draft,
        )
        .select_reclamation(oldest_reader, max_transactions, max_pages)?;
        let Some(selection) = selection else {
            return Ok(None);
        };

        let attempt = (
            draft.meta.database_id,
            draft.meta.txn_id,
            draft.meta.commit_nonce,
        );
        self.draft = Some(draft);
        DraftStore::new(
            &self.file,
            self.base.meta.page_count,
            self.budget.pages(),
            self.draft.as_mut().unwrap(),
        )
        .apply_reclamation(selection)?;
        self.prepare()?;
        Ok(Some((selection, self.finish_commit_locked(attempt))))
    }
}
