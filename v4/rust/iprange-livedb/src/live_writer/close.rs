//! Retryable, meta-aware live-writer close.

use crate::bootstrap::{Bootstrap, OpenMode};
use crate::database;
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

        let had_pending = self.draft.is_some();
        if let Err(cause) = self.sidecar.lock_gate(Mode::Exclusive) {
            return Ok(self.close_failure(had_pending, cause));
        }
        let operation = self.close_locked();
        match operation {
            Ok(()) => {
                self.mapping.unmap();
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
        let selected = self.close_base()?;
        self.sidecar.scan_at_most(selected.meta.txn_id)?;
        let physical_bytes = self.trim_to(selected.committed_bytes)?;
        self.verify_close_cleanup(physical_bytes)?;
        self.base.physical_bytes = physical_bytes;
        self.draft = None;
        self.unproved_tail_end = None;
        Ok(())
    }

    fn close_base(&self) -> Result<Bootstrap> {
        verify_pair(&self.main_path, self.main_identity, &self.sidecar)?;
        let physical_bytes = self.mapping.file().metadata()?.len();
        let selected =
            database::bootstrap_mapping(&self.mapping, physical_bytes, OpenMode::Writer)?;
        if selected.meta.database_id != self.base.meta.database_id {
            return Err(Error::WrongMode("live database identity changed"));
        }
        if self.draft.is_some() && selected.meta != self.base.meta {
            return Err(Error::WrongMode(
                "committed generation changed before abort cleanup",
            ));
        }
        Ok(selected)
    }

    fn verify_close_cleanup(&self, physical_bytes: u64) -> Result<()> {
        if self.draft.is_some() {
            self.verify_discard_result(physical_bytes)
        } else {
            if self.mapping.file().metadata()?.len() != physical_bytes {
                return Err(Error::Corrupt(
                    "writer close changed the retained physical length",
                ));
            }
            verify_pair(&self.main_path, self.main_identity, &self.sidecar)
        }
    }

    fn trim_to(&mut self, committed_bytes: u64) -> Result<u64> {
        let length = self.mapping.file().metadata()?.len();
        if length < committed_bytes {
            return Err(Error::Corrupt(
                "main file is shorter than its committed generation",
            ));
        }
        let has_tail = length > committed_bytes;
        if has_tail {
            self.unproved_tail_end = Some(length);
            let physical_bytes = self.mapping.shrink_or_retain(committed_bytes)?;
            self.mapping.sync_file()?;
            return Ok(physical_bytes);
        }
        if self.mapping.len() != committed_bytes {
            self.mapping.remap(committed_bytes)?;
        }
        if self.draft.is_some() {
            self.mapping.sync_file()?;
        }
        Ok(length)
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
            if let Err(cause) = live_lock::unlock_file(self.mapping.file(), MAIN_LIFETIME_LOCK) {
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
        let cleanup = if self.draft.is_some() {
            self.unpublished_tail_cleanup(cause.code())
        } else {
            CommitCleanupArtifacts::clean()
        };
        CloseResult::incomplete(
            had_pending.then_some(if self.draft.is_some() {
                AbortOutcome::AbortIncomplete
            } else {
                AbortOutcome::Aborted
            }),
            cleanup,
            cause,
        )
    }
}
