//! Authoritative healthy-file reads and mutations for one live writer.

mod close;
mod edit;
mod open;
mod publication;
mod reclaim;

use std::fs::File;

use crate::bootstrap::{Bootstrap, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind, ValueTag, PAGE_SIZE};
use crate::database;
use crate::draft_store::{Draft, DraftStore, PageBudget};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::feed_catalog::{self, FeedCursor};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::metadata;
use crate::process_identity::ProcessIdentity;
use crate::random;
use crate::workflow::{compare, Comparison};

pub(crate) use crate::draft_store::{
    FeedMerge, ImportCache, ImportWords, MembershipHandle, TranslatedMembership,
};
pub(crate) use edit::WriterEdit;
pub(crate) use publication::{CommitAttempt, PublishOutcome};

/// Logical generation facts needed by writer workflows.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct WriterInfo {
    pub(crate) address_family: AddressFamily,
    pub(crate) value_kind: ValueKind,
    pub(crate) value_tag: ValueTag,
    pub(crate) database_id: [u8; 16],
    pub(crate) transaction_id: u64,
    pub(crate) commit_nonce: [u8; 16],
    pub(crate) page_count: u64,
    pub(crate) range_record_count: u64,
    pub(crate) active_feed_count: u64,
}

/// Logical facts needed to describe an unpublished-tail cleanup obligation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct TailCleanupState {
    pub(crate) database_id: [u8; 16],
    pub(crate) transaction_id: u64,
    pub(crate) commit_nonce: [u8; 16],
    pub(crate) committed_length: u64,
    pub(crate) observed_tail_end_exclusive: Option<u64>,
}

impl From<MetaV4> for WriterInfo {
    fn from(meta: MetaV4) -> Self {
        Self {
            address_family: meta.address_family,
            value_kind: meta.value_kind,
            value_tag: meta.value_tag,
            database_id: meta.database_id,
            transaction_id: meta.txn_id,
            commit_nonce: meta.commit_nonce,
            page_count: meta.page_count,
            range_record_count: meta.range_record_count,
            active_feed_count: meta.active_feed_count,
        }
    }
}

/// Mapped committed generation plus at most one unpublished COW draft.
#[derive(Debug)]
pub(crate) struct WriterCore {
    mapping: Mapping,
    base: Bootstrap,
    budget: PageBudget,
    draft: Option<Draft>,
    unproved_tail_end: Option<u64>,
}

impl WriterCore {
    fn new(mapping: Mapping, base: Bootstrap, budget: PageBudget) -> Self {
        Self {
            mapping,
            base,
            budget,
            draft: None,
            unproved_tail_end: None,
        }
    }

    pub(crate) fn base_info(&self) -> WriterInfo {
        self.base.meta.into()
    }

    pub(crate) fn current_info(&self) -> WriterInfo {
        self.current_meta().into()
    }

    pub(crate) fn file(&self) -> &File {
        self.mapping.file()
    }

    pub(crate) fn unmap(&mut self) {
        self.mapping.unmap();
    }

    pub(crate) fn has_draft(&self) -> bool {
        self.draft.is_some()
    }

    pub(crate) fn draft_changed(&self) -> bool {
        self.draft.as_ref().is_some_and(Draft::changed)
    }

    pub(crate) fn workflow_input_open(&self) -> bool {
        self.draft.as_ref().is_some_and(Draft::workflow_input_open)
    }

    pub(crate) fn workflow_active(&self) -> bool {
        self.draft.as_ref().is_some_and(Draft::workflow_active)
    }

    pub(crate) fn metadata_staged(&self) -> bool {
        self.draft.as_ref().is_some_and(Draft::metadata_staged)
    }

    pub(crate) fn operation_abandoned(&self) -> bool {
        self.draft.as_ref().is_some_and(Draft::operation_abandoned)
    }

    pub(crate) fn operation_is(&self, nonce: [u8; 16]) -> bool {
        self.draft
            .as_ref()
            .is_some_and(|draft| draft.meta.commit_nonce == nonce)
    }

    pub(crate) fn abandon_operation(&mut self) {
        if let Some(draft) = self.draft.as_mut() {
            draft.abandon_operation();
        }
    }

    pub(crate) fn begin_transaction(&mut self) -> Result<[u8; 16]> {
        if self.draft.is_some() {
            return Err(Error::WrongState("a writer transaction is already pending"));
        }
        let nonce = random::nonzero_128()?;
        self.draft = Some(Draft::new(self.base.meta, nonce)?);
        Ok(nonce)
    }

    pub(crate) fn begin_range_workflow(&mut self) -> Result<()> {
        let nonce = self.begin_transaction()?;
        let draft = self.draft.as_mut().expect("transaction was just created");
        if let Err(error) = draft.begin_range_workflow() {
            self.draft = None;
            return Err(error);
        }
        debug_assert_eq!(draft.meta.commit_nonce, nonce);
        Ok(())
    }

    pub(crate) fn begin_membership_workflow(&mut self) -> Result<()> {
        self.begin_transaction()?;
        let draft = self.draft.as_mut().expect("transaction was just created");
        if let Err(error) = draft.begin_membership_workflow() {
            self.draft = None;
            return Err(error);
        }
        Ok(())
    }

    pub(crate) fn edit<T>(
        &mut self,
        operation: impl FnOnce(&mut WriterEdit<'_>) -> Result<T>,
    ) -> Result<T> {
        if self.draft.is_none() {
            self.begin_transaction()?;
        }
        let base = self.base;
        let draft = self.draft.as_mut().expect("edit has a draft");
        let store = DraftStore::new(&mut self.mapping, base.meta.page_count, self.budget, draft);
        operation(&mut WriterEdit::new(store, base))
    }

    pub(crate) fn lookup_base_feed(&self, name: &FeedName) -> Result<Option<FeedEntry>> {
        feed_catalog::lookup(&self.mapping, &self.base.meta, name)
    }

    pub(crate) fn lookup_current_feed(&self, name: &FeedName) -> Result<Option<FeedEntry>> {
        feed_catalog::lookup(&self.mapping, &self.current_meta(), name)
    }

    pub(crate) fn current_feed_cursor(&self, owner: ProcessIdentity) -> Result<FeedCursor<'_>> {
        FeedCursor::new(&self.mapping, &self.current_meta(), Some(owner))
    }

    pub(crate) fn membership_reference_matches(
        &mut self,
        membership: MembershipHandle,
    ) -> Result<bool> {
        let draft = self
            .draft
            .as_mut()
            .ok_or(Error::WrongState("membership transaction is not active"))?;
        DraftStore::new(
            &mut self.mapping,
            self.base.meta.page_count,
            self.budget,
            draft,
        )
        .membership_reference_matches(membership)
    }

    pub(crate) fn metadata_json_len(&self) -> Option<u64> {
        let meta = self.current_meta();
        (meta.metadata_root != 0).then_some(meta.metadata_uncompressed_len)
    }

    pub(crate) fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        metadata::read(&self.mapping, &self.current_meta(), output)
    }

    pub(crate) fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        metadata::read_vec(&self.mapping, &self.current_meta())
    }

    pub(crate) fn compare_maps(&self, cancellation: &CancellationToken) -> Result<Comparison> {
        let after = self.current_meta();
        match after.address_family {
            AddressFamily::Ipv4 => {
                compare::maps::<Ipv4Key>(&self.mapping, &self.base, &after, cancellation)
            }
            AddressFamily::Ipv6 => {
                compare::maps::<Ipv6Key>(&self.mapping, &self.base, &after, cancellation)
            }
        }
    }

    pub(crate) fn discard_unpublished(&mut self) -> Result<()> {
        self.require_unchanged_base()?;
        let physical_bytes = self.trim_unpublished_tail()?;
        self.verify_discard_result(physical_bytes)?;
        self.base.physical_bytes = physical_bytes;
        self.draft = None;
        self.unproved_tail_end = None;
        Ok(())
    }

    pub(crate) fn require_unchanged_base(&self) -> Result<()> {
        let physical_bytes = self.mapping.file().metadata()?.len();
        let selected =
            database::bootstrap_mapping(&self.mapping, physical_bytes, OpenMode::Writer)?;
        if selected.meta != self.base.meta {
            return Err(Error::WrongMode(
                "committed generation changed under the writer",
            ));
        }
        Ok(())
    }

    pub(crate) fn tail_cleanup_state(&self) -> TailCleanupState {
        let current_end = self
            .mapping
            .file()
            .metadata()
            .ok()
            .map(|metadata| metadata.len());
        let observed_tail_end_exclusive = [self.unproved_tail_end, current_end]
            .into_iter()
            .flatten()
            .filter(|&length| length > self.base.committed_bytes)
            .max();
        TailCleanupState {
            database_id: self.base.meta.database_id,
            transaction_id: self.base.meta.txn_id,
            commit_nonce: self.base.meta.commit_nonce,
            committed_length: self.base.committed_bytes,
            observed_tail_end_exclusive,
        }
    }

    fn trim_unpublished_tail(&mut self) -> Result<u64> {
        let length = self.mapping.file().metadata()?.len();
        if length < self.base.committed_bytes {
            return Err(Error::Corrupt(
                "main file is shorter than its committed generation",
            ));
        }
        if length > self.base.committed_bytes {
            self.unproved_tail_end = Some(length);
            let physical_bytes = self.mapping.shrink_or_retain(self.base.committed_bytes)?;
            self.mapping.sync_file()?;
            return Ok(physical_bytes);
        }
        if self.mapping.len() != self.base.committed_bytes {
            self.mapping.remap(self.base.committed_bytes)?;
        }
        Ok(length)
    }

    fn verify_discard_result(&self, physical_bytes: u64) -> Result<()> {
        self.require_unchanged_base()?;
        if physical_bytes < self.base.committed_bytes
            || physical_bytes % PAGE_SIZE as u64 != 0
            || self.mapping.file().metadata()?.len() != physical_bytes
            || self.mapping.len() != self.base.committed_bytes
        {
            return Err(Error::Corrupt(
                "unpublished tail cleanup left inconsistent geometry",
            ));
        }
        Ok(())
    }

    fn current_meta(&self) -> MetaV4 {
        self.draft
            .as_ref()
            .map(|draft| draft.meta)
            .unwrap_or(self.base.meta)
    }
}
