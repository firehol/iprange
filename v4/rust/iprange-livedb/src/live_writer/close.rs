//! Retryable, meta-aware live-writer close.

use crate::bootstrap::OpenMode;
use crate::database;
use crate::error::{Error, Result};
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

        let had_pending = self.draft.is_some();
        if let Err(cause) = self.sidecar.lock_gate(Mode::Exclusive) {
            return Ok(self.close_failure(had_pending, cause));
        }
        let operation = self.close_locked();
        match operation {
            Ok(()) => self.finish_close(had_pending),
            Err(cause) => {
                let _ = self.sidecar.unlock_gate();
                Ok(self.close_failure(had_pending, cause))
            }
        }
    }

    fn close_locked(&mut self) -> Result<()> {
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;
        let selected = database::bootstrap_file(&self.file, OpenMode::Writer)?;
        if selected.meta.database_id != self.base.meta.database_id {
            return Err(Error::WrongMode("live database identity changed"));
        }
        self.sidecar.scan_at_most(selected.meta.txn_id)?;
        self.trim_to(selected.committed_bytes)?;
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;
        self.sidecar.release_writer()
    }

    fn trim_to(&mut self, committed_bytes: u64) -> Result<()> {
        if self.file.metadata()?.len() != committed_bytes {
            self.file.set_len(committed_bytes)?;
            self.file.sync_all()?;
        }
        self.draft = None;
        Ok(())
    }

    fn finish_close(&mut self, had_pending: bool) -> Result<CloseResult> {
        if let Err(cause) = self.sidecar.unlock_gate() {
            return Ok(self.close_failure(had_pending, cause));
        }
        if let Err(cause) = live_lock::unlock(&self.file, MAIN_LIFETIME_LOCK) {
            return Ok(self.close_failure(had_pending, cause));
        }
        self.state = State::Closed;
        Ok(CloseResult::closed(
            had_pending.then_some(AbortOutcome::Aborted),
        ))
    }

    fn close_failure(&mut self, had_pending: bool, cause: Error) -> CloseResult {
        self.state = State::Unusable;
        let cleanup = if self.draft.is_some() {
            CommitCleanupArtifacts::tail(self.unpublished_tail_artifact(cause.code()))
        } else {
            CommitCleanupArtifacts::clean()
        };
        CloseResult::incomplete(
            had_pending.then_some(AbortOutcome::AbortIncomplete),
            cleanup,
            cause,
        )
    }
}
