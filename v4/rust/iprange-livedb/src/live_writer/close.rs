//! Retryable, meta-aware live-writer close.

use crate::error::{combine_errors, Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;

use super::{verify_pair, AbortOutcome, CloseResult, CommitCleanupArtifacts, LiveWriter, State};

impl LiveWriter {
    /// Abort unpublished state, clear the writer lease, and release live locks.
    pub fn close(&mut self) -> Result<CloseResult> {
        self.require_owner()?;
        if self.state == State::Closed {
            return Ok(CloseResult::closed(None));
        }
        if matches!(
            self.state,
            State::ClosingWriter(_) | State::ClosingGate(_) | State::ClosingMain(_)
        ) {
            return Ok(self.finish_close());
        }

        let had_pending = self.core.has_draft();
        if let Err(cause) = self.sidecar.lock_gate(Mode::Exclusive) {
            return Ok(self.close_failure(had_pending, cause));
        }
        let operation = self.close_locked();
        match operation {
            Ok(()) => {
                self.core.unmap();
                self.state = State::ClosingWriter(had_pending);
                Ok(self.finish_close())
            }
            Err(cause) => {
                let cause = combine_errors(cause, self.sidecar.unlock_gate());
                Ok(self.close_failure(had_pending, cause))
            }
        }
    }

    fn close_locked(&mut self) -> Result<()> {
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;
        let plan = self.core.prepare_close()?;
        self.sidecar.scan_at_most(plan.transaction_id())?;
        self.core.finish_close(plan)?;
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)
    }

    fn finish_close(&mut self) -> CloseResult {
        let had_pending = match self.state {
            State::ClosingWriter(had_pending)
            | State::ClosingGate(had_pending)
            | State::ClosingMain(had_pending) => had_pending,
            _ => return self.close_failure(false, Error::WrongState("writer is not closing")),
        };
        if let State::ClosingWriter(_) = self.state {
            if let Err(cause) = self.sidecar.release_writer() {
                return self.closing_failure(had_pending, cause);
            }
            self.state = State::ClosingGate(had_pending);
        }
        if let State::ClosingGate(_) = self.state {
            if let Err(cause) = self.sidecar.unlock_gate() {
                return self.closing_failure(had_pending, cause);
            }
            self.state = State::ClosingMain(had_pending);
        }
        if let State::ClosingMain(_) = self.state {
            if let Err(cause) = live_lock::unlock_file(self.core.file(), MAIN_LIFETIME_LOCK) {
                return self.closing_failure(had_pending, cause);
            }
            self.state = State::Closed;
        }
        CloseResult::closed(had_pending.then_some(AbortOutcome::Aborted))
    }

    fn closing_failure(&self, had_pending: bool, cause: Error) -> CloseResult {
        CloseResult::incomplete(
            had_pending.then_some(AbortOutcome::Aborted),
            CommitCleanupArtifacts::clean(),
            cause,
        )
    }

    fn close_failure(&mut self, had_pending: bool, cause: Error) -> CloseResult {
        self.state = State::Unusable;
        let cleanup = if self.core.has_draft() {
            self.unpublished_tail_cleanup(cause.code())
        } else {
            CommitCleanupArtifacts::clean()
        };
        CloseResult::incomplete(
            had_pending.then_some(if self.core.has_draft() {
                AbortOutcome::AbortIncomplete
            } else {
                AbortOutcome::Aborted
            }),
            cleanup,
            cause,
        )
    }
}
