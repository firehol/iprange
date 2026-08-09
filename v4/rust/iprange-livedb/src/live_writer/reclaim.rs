//! Bounded reader-safe page reclamation.

use crate::cancellation::CancellationToken;
use crate::error::{combine_errors, Error, Result};
use crate::live_lock::Mode;

use super::{verify_pair, CommitResult, LiveWriter, State};

/// Result of one clean-writer reclamation operation.
#[derive(Debug)]
// CommitResult keeps its bounded basename inline so commit finalization allocates nothing.
#[allow(clippy::large_enum_variant)]
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
    pub fn reclaim(
        &mut self,
        max_transactions: u64,
        max_pages: u64,
        cancellation: &CancellationToken,
    ) -> Result<ReclaimResult> {
        cancellation.check()?;
        self.require_reclaim(max_transactions, max_pages)?;
        self.sidecar
            .lock_gate_cancellable(Mode::Exclusive, cancellation)?;
        let operation = self.reclaim_locked(max_transactions, max_pages, cancellation);
        self.finish_reclaim(operation)
    }

    fn require_reclaim(&self, max_transactions: u64, max_pages: u64) -> Result<()> {
        self.require_healthy()?;
        if self.core.has_draft() {
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
        operation: Result<Option<(u64, u64, CommitResult)>>,
    ) -> Result<ReclaimResult> {
        match operation {
            Ok(None) => {
                if let Err(cause) = self.sidecar.unlock_gate() {
                    self.state = State::Unusable;
                    return Err(cause);
                }
                Ok(ReclaimResult::NoChange)
            }
            Ok(Some((transaction_count, page_count, mut commit))) => {
                let unlock = self.sidecar.unlock_gate();
                self.apply_commit_unlock(&mut commit, unlock);
                Ok(ReclaimResult::Commit {
                    transaction_count,
                    page_count,
                    commit,
                })
            }
            Err(cause) => Err(self.finish_reclaim_error(cause)),
        }
    }

    fn finish_reclaim_error(&mut self, cause: Error) -> Error {
        let cause = if self.core.has_draft() {
            self.abort_after(cause)
        } else {
            cause
        };
        match self.sidecar.unlock_gate() {
            Ok(()) => cause,
            Err(cleanup) => {
                self.state = State::Unusable;
                combine_errors(cause, Err(cleanup))
            }
        }
    }

    fn reclaim_locked(
        &mut self,
        max_transactions: u64,
        max_pages: u64,
        cancellation: &CancellationToken,
    ) -> Result<Option<(u64, u64, CommitResult)>> {
        cancellation.check()?;
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;
        self.core.require_unchanged_base()?;
        let oldest_reader = self
            .sidecar
            .oldest_reader_cancellable(self.core.base_info().transaction_id, cancellation)?;
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;

        let Some(prepared) = self.core.prepare_reclamation(
            oldest_reader,
            max_transactions,
            max_pages,
            cancellation,
        )?
        else {
            return Ok(None);
        };
        Ok(Some((
            prepared.transaction_count,
            prepared.page_count,
            self.finish_commit_locked(prepared.attempt, cancellation),
        )))
    }
}
