//! Bounded reclamation draft preparation.

use crate::cancellation::CancellationToken;
use crate::draft_store::{Draft, DraftStore};
use crate::error::Result;
use crate::random;
use crate::retirement::Reclamation;

use super::{CommitAttempt, WriterCore};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PreparedReclamation {
    pub(crate) selection: Reclamation,
    pub(crate) attempt: CommitAttempt,
}

impl WriterCore {
    pub(crate) fn prepare_reclamation(
        &mut self,
        oldest_reader: Option<u64>,
        max_transactions: u64,
        max_pages: u64,
        cancellation: &CancellationToken,
    ) -> Result<Option<PreparedReclamation>> {
        let mut draft = Draft::new(self.base.meta, random::nonzero_128()?)?;
        let mut checkpoint = || cancellation.check();
        let selection = DraftStore::new(
            &mut self.mapping,
            self.base.meta.page_count,
            self.budget,
            &mut draft,
        )
        .select_reclamation(oldest_reader, max_transactions, max_pages, &mut checkpoint)?;
        let Some(selection) = selection else {
            return Ok(None);
        };

        let attempt = CommitAttempt {
            database_id: draft.meta.database_id,
            transaction_id: draft.meta.txn_id,
            commit_nonce: draft.meta.commit_nonce,
        };
        self.draft = Some(draft);
        DraftStore::new(
            &mut self.mapping,
            self.base.meta.page_count,
            self.budget,
            self.draft
                .as_mut()
                .expect("reclamation draft was installed"),
        )
        .apply_reclamation(selection, &mut checkpoint)?;
        self.prepare(cancellation)?;
        cancellation.check()?;
        Ok(Some(PreparedReclamation { selection, attempt }))
    }
}
