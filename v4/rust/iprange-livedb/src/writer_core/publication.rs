//! Physical preparation and alternate-meta publication.

use crate::bootstrap::{Bootstrap, MetaSelection};
use crate::cancellation::CancellationToken;
use crate::contract::PAGE_SIZE;
use crate::draft_store::DraftStore;
use crate::error::{Error, Result};

use super::WriterCore;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct CommitAttempt {
    pub(crate) database_id: [u8; 16],
    pub(crate) transaction_id: u64,
    pub(crate) commit_nonce: [u8; 16],
}

#[derive(Debug)]
pub(crate) enum PublishOutcome {
    BeforePublication(Error),
    OutcomeUnknown(Error),
    Committed,
}

impl WriterCore {
    pub(crate) fn commit_attempt(&self) -> Result<CommitAttempt> {
        if self.workflow_input_open() {
            return Err(Error::WrongState("workflow input is not finished"));
        }
        let draft = self
            .draft
            .as_ref()
            .filter(|draft| draft.changed())
            .ok_or(Error::NoPendingTransaction)?;
        Ok(CommitAttempt {
            database_id: draft.meta.database_id,
            transaction_id: draft.meta.txn_id,
            commit_nonce: draft.meta.commit_nonce,
        })
    }

    pub(crate) fn prepare(&mut self, cancellation: &CancellationToken) -> Result<()> {
        let draft = self.draft.as_mut().ok_or(Error::NoPendingTransaction)?;
        let mut store = DraftStore::new(
            &mut self.mapping,
            self.base.meta.page_count,
            self.budget,
            draft,
        );
        store.prepare_with_checkpoint(&mut || cancellation.check())
    }

    pub(crate) fn require_draft_length(&self) -> Result<()> {
        let expected = self
            .draft
            .as_ref()
            .ok_or(Error::NoPendingTransaction)?
            .meta
            .page_count
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(Error::ArithmeticOverflow("committed file length"))?;
        if self.mapping.file().metadata()?.len() < expected || self.mapping.len() < expected {
            return Err(Error::Corrupt("draft file length is inconsistent"));
        }
        Ok(())
    }

    pub(crate) fn publish(&mut self, cancellation: &CancellationToken) -> PublishOutcome {
        if let Err(error) = cancellation.check() {
            return PublishOutcome::BeforePublication(error);
        }
        crate::fault::crash("commit.before_private_sync");
        let committed_bytes = self
            .draft
            .as_ref()
            .expect("publication requires a draft")
            .meta
            .page_count
            * PAGE_SIZE as u64;
        let physical_bytes = match self.mapping.shrink_or_retain(committed_bytes) {
            Ok(physical_bytes) => physical_bytes,
            Err(error) => return PublishOutcome::BeforePublication(error),
        };
        let data_offset = (2 * PAGE_SIZE) as u64;
        if committed_bytes > data_offset {
            if let Err(error) = self
                .mapping
                .flush_range(data_offset, committed_bytes - data_offset)
            {
                return PublishOutcome::BeforePublication(error);
            }
        }
        if let Err(error) = self.mapping.sync_file() {
            return PublishOutcome::BeforePublication(error);
        }
        crate::fault::crash("commit.after_private_sync");
        if let Err(error) = cancellation.check() {
            return PublishOutcome::BeforePublication(error);
        }

        let meta = self
            .draft
            .as_ref()
            .expect("publication requires a draft")
            .meta;
        let target_page = 1 - self.base.selected_meta_page;
        let encoded = self
            .mapping
            .page_mut(u32::from(target_page), meta.page_count)
            .and_then(|page| meta.encode_mapped(page));
        if let Err(error) = encoded {
            return self.outcome_unknown(error);
        }
        crate::fault::crash("commit.after_meta_write");
        if let Err(error) = self
            .mapping
            .flush_page(u32::from(target_page), meta.page_count)
        {
            return self.outcome_unknown(error);
        }
        if let Err(error) = self.mapping.sync_file() {
            return self.outcome_unknown(error);
        }
        crate::fault::crash("commit.after_meta_sync");
        self.base = Bootstrap {
            meta,
            selection: MetaSelection::ProvenCurrent,
            selected_meta_page: target_page,
            committed_bytes,
            physical_bytes,
        };
        self.draft = None;
        self.unproved_tail_end = None;
        PublishOutcome::Committed
    }

    fn outcome_unknown(&mut self, error: Error) -> PublishOutcome {
        self.draft = None;
        self.unproved_tail_end = None;
        PublishOutcome::OutcomeUnknown(error)
    }
}
