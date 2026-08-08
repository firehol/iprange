//! Durable alternate-meta publication.

use crate::cancellation::CancellationToken;
use crate::error::{combine_errors, Error, Result};
use crate::live_lock::Mode;
use crate::publication::CoordinationCleanup;
use crate::writer_core::{CommitAttempt, PublishOutcome};

use super::{
    verify_pair, CommitCleanupArtifacts, CommitDurability, CommitResult, LiveWriter, State,
};

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

    fn commit_attempt(&mut self) -> Result<CommitAttempt> {
        self.require_healthy()?;
        self.require_operation_owned()?;
        if self.core.has_draft() && !self.core.draft_changed() {
            self.discard_draft()?;
            return Err(Error::NoPendingTransaction);
        }
        self.core.commit_attempt()
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
        attempt: CommitAttempt,
        cancellation: &CancellationToken,
    ) -> CommitResult {
        self.finish_commit_locked_with(attempt, cancellation)
    }

    fn finish_commit_locked_with(
        &mut self,
        attempt: CommitAttempt,
        cancellation: &CancellationToken,
    ) -> CommitResult {
        match self.commit_locked(cancellation) {
            PublishOutcome::BeforePublication(cause) => {
                let cause = self.abort_after(cause);
                self.failed_result(attempt, CommitDurability::NotCommitted, cause)
            }
            PublishOutcome::OutcomeUnknown(cause) => {
                self.state = State::OutcomeUnknown;
                self.failed_result(attempt, CommitDurability::OutcomeUnknown, cause)
            }
            PublishOutcome::Committed => CommitResult {
                attempted_database_id: attempt.database_id,
                directory_identity: self.directory_identity,
                main_identity: self.main_public_identity,
                attempted_transaction_id: attempt.transaction_id,
                attempted_commit_nonce: attempt.commit_nonce,
                durability: CommitDurability::Committed,
                cleanup: CommitCleanupArtifacts::clean(),
                coordination_cleanup: CoordinationCleanup::None,
                cause: None,
            },
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

    fn prepare_with(&mut self, cancellation: &CancellationToken) -> Result<()> {
        self.core.prepare(cancellation)
    }

    fn commit_locked(&mut self, cancellation: &CancellationToken) -> PublishOutcome {
        if let Err(error) = cancellation.check() {
            return PublishOutcome::BeforePublication(error);
        }
        match self.prepublication_checks(cancellation) {
            Ok(()) => {}
            Err(error) => return PublishOutcome::BeforePublication(error),
        }
        if let Err(error) = cancellation.check() {
            return PublishOutcome::BeforePublication(error);
        }
        self.core.publish(cancellation)
    }

    fn prepublication_checks(&self, cancellation: &CancellationToken) -> Result<()> {
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;
        self.core.require_unchanged_base()?;
        self.sidecar
            .scan_at_most_cancellable(self.core.base_info().transaction_id, cancellation)?;
        self.core.require_draft_length()?;
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)
    }

    fn failed_result(
        &self,
        attempt: CommitAttempt,
        durability: CommitDurability,
        cause: Error,
    ) -> CommitResult {
        let cleanup = if self.core.has_draft() {
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
            attempted_database_id: attempt.database_id,
            directory_identity: self.directory_identity,
            main_identity: self.main_public_identity,
            attempted_transaction_id: attempt.transaction_id,
            attempted_commit_nonce: attempt.commit_nonce,
            durability,
            cleanup,
            coordination_cleanup,
            cause: Some(cause),
        }
    }
}
