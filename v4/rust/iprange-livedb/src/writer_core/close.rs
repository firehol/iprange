//! Mapped main-file cleanup used by retryable writer close.

use crate::bootstrap::{Bootstrap, OpenMode};
use crate::database_file;
use crate::error::{Error, Result};

use super::WriterCore;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ClosePlan {
    selected: Bootstrap,
}

impl ClosePlan {
    pub(crate) fn transaction_id(self) -> u64 {
        self.selected.meta.txn_id
    }
}

impl WriterCore {
    pub(crate) fn prepare_close(&self) -> Result<ClosePlan> {
        let physical_bytes = self.mapping.file().metadata()?.len();
        let selected =
            database_file::bootstrap_mapping(&self.mapping, physical_bytes, OpenMode::Writer)?;
        if selected.meta.database_id != self.base.meta.database_id {
            return Err(Error::WrongMode("live database identity changed"));
        }
        if self.draft.is_some() && selected.meta != self.base.meta {
            return Err(Error::WrongMode(
                "committed generation changed before abort cleanup",
            ));
        }
        Ok(ClosePlan { selected })
    }

    pub(crate) fn finish_close(&mut self, plan: ClosePlan) -> Result<()> {
        let physical_bytes = self.trim_to(plan.selected.committed_bytes)?;
        if self.draft.is_some() {
            self.verify_discard_result(physical_bytes)?;
        } else if self.mapping.file().metadata()?.len() != physical_bytes {
            return Err(Error::Corrupt(
                "writer close changed the retained physical length",
            ));
        }
        self.base.physical_bytes = physical_bytes;
        self.draft = None;
        self.unproved_tail_end = None;
        Ok(())
    }

    fn trim_to(&mut self, committed_bytes: u64) -> Result<u64> {
        let length = self.mapping.file().metadata()?.len();
        if length < committed_bytes {
            return Err(Error::Corrupt(
                "main file is shorter than its committed generation",
            ));
        }
        if length > committed_bytes {
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
}
