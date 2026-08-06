//! File-backed unpublished page ownership for one COW transaction.

#[path = "draft_store/catalog.rs"]
mod catalog_ops;
#[path = "draft_store/membership.rs"]
mod membership_ops;
#[path = "draft_store/metadata.rs"]
mod metadata_ops;
#[path = "draft_store/page_cache.rs"]
mod page_cache;
#[path = "draft_store/retention.rs"]
mod retention;
#[path = "draft_store/storage.rs"]
mod storage;
#[path = "draft_store/workflow.rs"]
mod workflow_ops;

use std::cell::RefCell;
use std::fs::File;

use crate::contract::{u32_le, MetaV4, MAX_PAGE_COUNT, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiredPages, Store};
use crate::free_bitmap;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_mutation;
use crate::retirement;

const PRIVATE_MAGIC: u32 = 0x5046_5245;

#[derive(Clone, Copy)]
pub(crate) struct PageBudget {
    pub(crate) max_heap_bytes: u64,
    pub(crate) max_private_pages: u64,
    pub(crate) max_growth_pages: u64,
}

#[derive(Debug)]
pub(crate) struct Draft {
    base: MetaV4,
    pub(crate) meta: MetaV4,
    private_head: u32,
    dirty_head: u32,
    allocator_retired: RetiredPages,
    private_pages: u64,
    growth_pages: u64,
    changed: bool,
    metadata_staged: bool,
    range_tree_private: bool,
    membership_delta_root: u32,
    workflow: WorkflowState,
    operation_abandoned: bool,
    page_cache: RefCell<Option<page_cache::PageCache>>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum WorkflowState {
    None,
    Input,
    Prepared,
}

impl Draft {
    pub(crate) fn new(base: MetaV4, nonce: [u8; 16]) -> Result<Self> {
        let mut meta = base;
        meta.txn_id = meta
            .txn_id
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("transaction ID"))?;
        meta.commit_nonce = nonce;
        Ok(Self {
            base,
            meta,
            private_head: 0,
            dirty_head: 0,
            allocator_retired: RetiredPages::new(),
            private_pages: 0,
            growth_pages: 0,
            changed: false,
            metadata_staged: false,
            range_tree_private: false,
            membership_delta_root: 0,
            workflow: WorkflowState::None,
            operation_abandoned: false,
            page_cache: RefCell::new(None),
        })
    }

    pub(crate) fn changed(&self) -> bool {
        self.changed
    }

    pub(crate) fn metadata_staged(&self) -> bool {
        self.metadata_staged
    }

    pub(crate) fn metadata_meta(&self) -> MetaV4 {
        if self.metadata_staged {
            self.meta
        } else {
            self.base
        }
    }

    pub(crate) fn begin_range_workflow(&mut self) -> Result<()> {
        self.begin_workflow()?;
        self.meta.range_root = 0;
        self.meta.range_record_count = 0;
        self.range_tree_private = true;
        Ok(())
    }

    pub(crate) fn begin_membership_workflow(&mut self) -> Result<()> {
        self.begin_workflow()
    }

    fn begin_workflow(&mut self) -> Result<()> {
        if self.workflow != WorkflowState::None {
            return Err(Error::WrongState("another exact workflow is active"));
        }
        self.workflow = WorkflowState::Input;
        Ok(())
    }

    pub(crate) fn workflow_input_open(&self) -> bool {
        self.workflow == WorkflowState::Input
    }

    pub(crate) fn workflow_active(&self) -> bool {
        self.workflow != WorkflowState::None
    }

    pub(crate) fn operation_abandoned(&self) -> bool {
        self.operation_abandoned
    }

    pub(crate) fn abandon_operation(&mut self) {
        self.operation_abandoned = true;
    }

    fn finish_workflow(&mut self) {
        self.workflow = WorkflowState::Prepared;
        self.changed = true;
    }
}

pub(crate) struct DraftStore<'a> {
    file: &'a File,
    committed_page_count: u64,
    budget: PageBudget,
    draft: &'a mut Draft,
}

impl<'a> DraftStore<'a> {
    pub(crate) fn new(
        file: &'a File,
        committed_page_count: u64,
        budget: PageBudget,
        draft: &'a mut Draft,
    ) -> Self {
        Self {
            file,
            committed_page_count,
            budget,
            draft,
        }
    }

    pub(crate) fn new_cached(
        file: &'a File,
        committed_page_count: u64,
        budget: PageBudget,
        draft: &'a mut Draft,
    ) -> Self {
        draft.ensure_page_cache(budget.max_heap_bytes);
        Self::new(file, committed_page_count, budget, draft)
    }

    pub(crate) fn new_uncached(
        file: &'a File,
        committed_page_count: u64,
        budget: PageBudget,
        draft: &'a mut Draft,
    ) -> Result<Self> {
        let store = Self::new(file, committed_page_count, budget, draft);
        store.flush_page_cache()?;
        store.draft.discard_page_cache();
        Ok(store)
    }

    pub(crate) fn assign_v4(&mut self, from: Ipv4Key, to: Ipv4Key, value: u32) -> Result<bool> {
        let mut root = self.draft.meta.range_root;
        let mut count = self.draft.meta.range_record_count;
        let changed = if self.draft.range_tree_private {
            range_mutation::assign_private(self, &mut root, &mut count, from, to, value)?
        } else {
            range_mutation::assign(self, &mut root, &mut count, from, to, value)?
        };
        self.draft.meta.range_root = root;
        self.draft.meta.range_record_count = count;
        self.draft.changed |= changed;
        Ok(changed)
    }

    pub(crate) fn assign_v6(&mut self, from: Ipv6Key, to: Ipv6Key, value: u32) -> Result<bool> {
        let mut root = self.draft.meta.range_root;
        let mut count = self.draft.meta.range_record_count;
        let changed = if self.draft.range_tree_private {
            range_mutation::assign_private(self, &mut root, &mut count, from, to, value)?
        } else {
            range_mutation::assign(self, &mut root, &mut count, from, to, value)?
        };
        self.draft.meta.range_root = root;
        self.draft.meta.range_record_count = count;
        self.draft.changed |= changed;
        Ok(changed)
    }

    pub(crate) fn clear_v4(&mut self, from: Ipv4Key, to: Ipv4Key) -> Result<bool> {
        let mut root = self.draft.meta.range_root;
        let mut count = self.draft.meta.range_record_count;
        let changed = range_mutation::clear(self, &mut root, &mut count, from, to)?;
        self.draft.meta.range_root = root;
        self.draft.meta.range_record_count = count;
        self.draft.changed |= changed;
        Ok(changed)
    }

    pub(crate) fn clear_v6(&mut self, from: Ipv6Key, to: Ipv6Key) -> Result<bool> {
        let mut root = self.draft.meta.range_root;
        let mut count = self.draft.meta.range_record_count;
        let changed = range_mutation::clear(self, &mut root, &mut count, from, to)?;
        self.draft.meta.range_root = root;
        self.draft.meta.range_record_count = count;
        self.draft.changed |= changed;
        Ok(changed)
    }

    #[cfg(test)]
    pub(crate) fn prepare(&mut self) -> Result<()> {
        self.prepare_with_checkpoint(&mut || Ok(()))
    }

    pub(crate) fn prepare_with_checkpoint<F>(&mut self, checkpoint: &mut F) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        if self.draft.workflow_input_open() {
            return Err(Error::WrongState("workflow input is not finished"));
        }
        if !self.draft.changed {
            return Ok(());
        }
        checkpoint()?;
        self.finish_membership_deltas_with_checkpoint(checkpoint)?;
        checkpoint()?;
        self.release_private_pages(checkpoint)?;
        self.finish_bitmap_shape(checkpoint)?;
        checkpoint()?;
        self.seal_private_pages(checkpoint)
    }

    pub(crate) fn select_reclamation<F>(
        &self,
        oldest_reader: Option<u64>,
        max_transactions: u64,
        max_pages: u64,
        checkpoint: &mut F,
    ) -> Result<Option<retirement::Reclamation>>
    where
        F: FnMut() -> Result<()>,
    {
        retirement::select_reclamation_with_checkpoint(
            self,
            self.draft.meta.retirement_root,
            self.draft.meta.txn_id - 1,
            oldest_reader,
            max_transactions,
            max_pages,
            checkpoint,
        )
    }

    pub(crate) fn apply_reclamation<F>(
        &mut self,
        selection: retirement::Reclamation,
        checkpoint: &mut F,
    ) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        let mut transactions = 0u64;
        let mut pages = 0u64;
        let mut previous_txn = 0u64;

        loop {
            checkpoint()?;
            let Some(extent) = retirement::first(self, self.draft.meta.retirement_root)? else {
                break;
            };
            if extent.transaction() > selection.through_txn {
                break;
            }
            if extent.transaction() != previous_txn {
                transactions += 1;
                previous_txn = extent.transaction();
            }
            pages = pages
                .checked_add(extent.page_count())
                .ok_or(Error::ArithmeticOverflow("reclaimed page count"))?;
            self.reclaim_extent(extent, checkpoint)?;
        }
        if transactions != selection.transactions || pages != selection.pages {
            return Err(Error::Corrupt("reclamation selection changed"));
        }
        self.draft.changed = true;
        Ok(())
    }

    fn release_private_pages<F>(&mut self, checkpoint: &mut F) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        loop {
            checkpoint()?;
            self.replenish_reserve()?;
            self.drain_allocator_retired()?;
            self.drain_private_stack(checkpoint)?;
            if self.draft.allocator_retired.as_slice().is_empty() {
                return Ok(());
            }
        }
    }

    fn drain_private_stack<F>(&mut self, checkpoint: &mut F) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        while self.draft.private_head != 0 {
            checkpoint()?;
            let page = self.pop_private()?;
            let mut root = self.draft.meta.free_bitmap_root;
            let mut retired = RetiredPages::new();
            free_bitmap::set_free(
                self,
                &mut root,
                self.draft.meta.page_count,
                page,
                &mut retired,
            )?;
            self.draft.meta.free_bitmap_root = root;
            self.draft.allocator_retired.extend(retired.as_slice())?;
            self.drain_allocator_retired()?;
        }
        Ok(())
    }

    fn finish_bitmap_shape<F>(&mut self, checkpoint: &mut F) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        loop {
            checkpoint()?;
            self.replenish_reserve()?;
            let mut root = self.draft.meta.free_bitmap_root;
            let limit = self.draft.meta.page_count;
            free_bitmap::ensure_level(self, &mut root, limit)?;
            self.draft.meta.free_bitmap_root = root;
            if self
                .draft
                .meta
                .allocator_reserve
                .iter()
                .all(|&page| page != 0)
            {
                return Ok(());
            }
        }
    }

    fn retire_one(&mut self, page_number: u32) -> Result<()> {
        let mut root = self.draft.meta.retirement_root;
        let mut count = self.draft.meta.retired_extent_count;
        let generated = retirement::add_page(
            self,
            &mut root,
            &mut count,
            self.draft.meta.txn_id,
            page_number,
        )?;
        self.draft.meta.retirement_root = root;
        self.draft.meta.retired_extent_count = count;

        for &page in generated.as_slice() {
            let mut root = self.draft.meta.retirement_root;
            let mut count = self.draft.meta.retired_extent_count;
            let nested =
                retirement::add_page(self, &mut root, &mut count, self.draft.meta.txn_id, page)?;
            self.draft.meta.retirement_root = root;
            self.draft.meta.retired_extent_count = count;
            if !nested.as_slice().is_empty() {
                return Err(Error::Corrupt("retirement COW path did not become private"));
            }
        }
        Ok(())
    }

    fn reclaim_extent<F>(&mut self, extent: retirement::Extent, checkpoint: &mut F) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        for page in extent.pages() {
            checkpoint()?;
            let page = u32::try_from(page)
                .map_err(|_| Error::Corrupt("reclaimed page exceeds page-number space"))?;
            self.free_one(page)?;
        }

        let mut root = self.draft.meta.retirement_root;
        let mut count = self.draft.meta.retired_extent_count;
        let generated = retirement::remove_extent(self, &mut root, &mut count, extent)?;
        self.draft.meta.retirement_root = root;
        self.draft.meta.retired_extent_count = count;
        for &page in generated.as_slice() {
            self.retire_one(page)?;
        }
        self.drain_allocator_retired()
    }

    fn free_one(&mut self, page_number: u32) -> Result<()> {
        let mut root = self.draft.meta.free_bitmap_root;
        let mut retired = RetiredPages::new();
        free_bitmap::set_free(
            self,
            &mut root,
            self.draft.meta.page_count,
            page_number,
            &mut retired,
        )?;
        self.draft.meta.free_bitmap_root = root;
        self.draft.allocator_retired.extend(retired.as_slice())?;
        self.drain_allocator_retired()
    }

    fn drain_allocator_retired(&mut self) -> Result<()> {
        while !self.draft.allocator_retired.as_slice().is_empty() {
            let pages = std::mem::replace(&mut self.draft.allocator_retired, RetiredPages::new());
            for &page in pages.as_slice() {
                self.retire_one(page)?;
            }
        }
        Ok(())
    }

    fn replenish_reserve(&mut self) -> Result<()> {
        for index in 0..self.draft.meta.allocator_reserve.len() {
            if self.draft.meta.allocator_reserve[index] != 0 {
                continue;
            }
            let page = if self.draft.private_head != 0 {
                self.pop_private()?
            } else {
                let page = self.allocate_tail()?;
                self.write(page, &[0; PAGE_SIZE])?;
                page
            };
            self.draft.meta.allocator_reserve[index] = page;
        }
        Ok(())
    }

    fn pop_private(&mut self) -> Result<u32> {
        let page_number = self.draft.private_head;
        if page_number == 0 {
            return Err(Error::Corrupt("private page stack is empty"));
        }
        let mut page = [0; PAGE_SIZE];
        self.read(page_number, &mut page)?;
        if u32_le(&page, 0) != PRIVATE_MAGIC
            || crate::contract::u64_le(&page, 8) != self.draft.meta.txn_id
        {
            return Err(Error::Corrupt("private page stack link is invalid"));
        }
        let next = u32_le(&page, 4);
        if next == page_number
            || (next != 0 && (next < 2 || u64::from(next) >= self.draft.meta.page_count))
        {
            return Err(Error::Corrupt(
                "private page stack points outside the draft",
            ));
        }
        self.draft.private_head = next;
        Ok(page_number)
    }

    fn charge_private(&mut self) -> Result<()> {
        if self.draft.private_pages >= self.budget.max_private_pages {
            return Err(Error::BudgetExceeded("private pages"));
        }
        self.draft.private_pages += 1;
        Ok(())
    }

    fn allocate_tail(&mut self) -> Result<u32> {
        if self.draft.meta.page_count < self.committed_page_count {
            return Err(Error::Corrupt("draft page count moved backwards"));
        }
        if self.draft.growth_pages >= self.budget.max_growth_pages {
            return Err(Error::BudgetExceeded("file growth pages"));
        }
        if self.draft.meta.page_count >= MAX_PAGE_COUNT {
            return Err(Error::PageSpaceExhausted);
        }
        self.charge_private()?;
        let page_number = self.draft.meta.page_count as u32;
        self.draft.meta.page_count += 1;
        self.draft.growth_pages += 1;
        Ok(page_number)
    }
}

impl Draft {
    fn ensure_page_cache(&mut self, heap_budget: u64) {
        if self.page_cache.get_mut().is_none() {
            *self.page_cache.get_mut() = Some(page_cache::PageCache::new(heap_budget));
        }
    }

    fn discard_page_cache(&mut self) {
        *self.page_cache.get_mut() = None;
    }
}

#[cfg(test)]
#[path = "draft_store_tests.rs"]
mod tests;
