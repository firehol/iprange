//! File-backed unpublished page ownership for one COW transaction.

#[path = "draft_store/metadata.rs"]
mod metadata_ops;

use std::fs::File;

use crate::contract::{u32_le, u64_le, MetaV4, MAX_PAGE_COUNT, PAGE_SHIFT, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;
use crate::fixed_tree::{RetiredPages, Store};
use crate::free_bitmap::{self, BitmapStore};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_mutation::{self, RangeStore};
use crate::retirement;
use crate::slotted_page::put_u32;

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
    allocator_retired: RetiredPages,
    private_pages: u64,
    growth_pages: u64,
    changed: bool,
    metadata_staged: bool,
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
            allocator_retired: RetiredPages::new(),
            private_pages: 0,
            growth_pages: 0,
            changed: false,
            metadata_staged: false,
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

    pub(crate) fn assign_v4(&mut self, from: Ipv4Key, to: Ipv4Key, value: u32) -> Result<bool> {
        let mut root = self.draft.meta.range_root;
        let mut count = self.draft.meta.range_record_count;
        let changed = range_mutation::assign(self, &mut root, &mut count, from, to, value)?;
        self.draft.meta.range_root = root;
        self.draft.meta.range_record_count = count;
        self.draft.changed |= changed;
        Ok(changed)
    }

    pub(crate) fn assign_v6(&mut self, from: Ipv6Key, to: Ipv6Key, value: u32) -> Result<bool> {
        let mut root = self.draft.meta.range_root;
        let mut count = self.draft.meta.range_record_count;
        let changed = range_mutation::assign(self, &mut root, &mut count, from, to, value)?;
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

    pub(crate) fn prepare(&mut self) -> Result<()> {
        if !self.draft.changed {
            return Ok(());
        }
        self.release_private_pages()?;
        self.finish_bitmap_shape()
    }

    pub(crate) fn select_reclamation(
        &self,
        oldest_reader: Option<u64>,
        max_transactions: u64,
        max_pages: u64,
    ) -> Result<Option<retirement::Reclamation>> {
        retirement::select_reclamation(
            self,
            self.draft.meta.retirement_root,
            self.draft.meta.txn_id - 1,
            oldest_reader,
            max_transactions,
            max_pages,
        )
    }

    pub(crate) fn apply_reclamation(&mut self, selection: retirement::Reclamation) -> Result<()> {
        let mut transactions = 0u64;
        let mut pages = 0u64;
        let mut previous_txn = 0u64;

        loop {
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
            self.reclaim_extent(extent)?;
        }
        if transactions != selection.transactions || pages != selection.pages {
            return Err(Error::Corrupt("reclamation selection changed"));
        }
        self.draft.changed = true;
        Ok(())
    }

    fn release_private_pages(&mut self) -> Result<()> {
        loop {
            self.replenish_reserve()?;
            self.drain_allocator_retired()?;
            self.drain_private_stack()?;
            if self.draft.allocator_retired.as_slice().is_empty() {
                return Ok(());
            }
        }
    }

    fn drain_private_stack(&mut self) -> Result<()> {
        while self.draft.private_head != 0 {
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

    fn finish_bitmap_shape(&mut self) -> Result<()> {
        loop {
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

    fn reclaim_extent(&mut self, extent: retirement::Extent) -> Result<()> {
        for page in extent.pages() {
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
        if u32_le(&page, 0) != PRIVATE_MAGIC {
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

impl Store for DraftStore<'_> {
    fn target_txn(&self) -> u64 {
        self.draft.meta.txn_id
    }

    fn page_limit(&self) -> u64 {
        self.draft.meta.page_count
    }

    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()> {
        file_io::read_page(self.file, page_number, self.draft.meta.page_count, page)
    }

    fn allocate(&mut self) -> Result<u32> {
        if self.draft.private_head != 0 {
            return self.pop_private();
        }
        let mut root = self.draft.meta.free_bitmap_root;
        let mut retired = RetiredPages::new();
        let limit = self.committed_page_count;
        if let Some(page) = free_bitmap::take_lowest(self, &mut root, limit, &mut retired)? {
            self.charge_private()?;
            self.draft.meta.free_bitmap_root = root;
            self.draft.allocator_retired.extend(retired.as_slice())?;
            return Ok(page);
        }
        self.draft.meta.free_bitmap_root = root;
        self.allocate_tail()
    }

    fn write(&mut self, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()> {
        if page_number < 2 || u64::from(page_number) >= self.draft.meta.page_count {
            return Err(Error::Corrupt("draft write is outside page bounds"));
        }
        let offset = u64::from(page_number)
            .checked_shl(u32::from(PAGE_SHIFT))
            .ok_or(Error::ArithmeticOverflow("draft page offset"))?;
        file_io::write_exact_at(self.file, page, offset)
    }

    fn discard_private(&mut self, page_number: u32) -> Result<()> {
        if page_number < 2
            || u64::from(page_number) >= self.draft.meta.page_count
            || page_number == self.draft.private_head
        {
            return Err(Error::Corrupt("discarded private page is invalid"));
        }
        let mut page = [0; PAGE_SIZE];
        self.read(page_number, &mut page)?;
        if u64_le(&page, 8) != self.draft.meta.txn_id {
            return Err(Error::Corrupt(
                "committed page cannot enter the private stack",
            ));
        }
        page.fill(0);
        put_u32(&mut page, 0, PRIVATE_MAGIC);
        put_u32(&mut page, 4, self.draft.private_head);
        self.write(page_number, &page)?;
        self.draft.private_head = page_number;
        Ok(())
    }
}

impl BitmapStore for DraftStore<'_> {
    fn allocate_bitmap_page(&mut self) -> Result<u32> {
        if self.draft.private_head != 0 {
            return self.pop_private();
        }
        if let Some(slot) = self
            .draft
            .meta
            .allocator_reserve
            .iter_mut()
            .find(|page| **page != 0)
        {
            let page = *slot;
            *slot = 0;
            self.charge_private()?;
            return Ok(page);
        }
        self.allocate_tail()
    }

    fn allocation_forbidden(&self, page_number: u32) -> bool {
        page_number < 2
            || self.draft.meta.allocator_reserve.contains(&page_number)
            || roots(&self.draft.meta).contains(&page_number)
    }
}

impl RangeStore for DraftStore<'_> {
    fn retire_pages(&mut self, pages: &[u32]) -> Result<()> {
        for &page in pages {
            self.retire_one(page)?;
        }
        self.drain_allocator_retired()
    }
}

fn roots(meta: &MetaV4) -> [u32; 10] {
    [
        meta.range_root,
        meta.catalog_name_root,
        meta.catalog_index_root,
        meta.feed_used_root,
        meta.membership_id_root,
        meta.membership_hash_root,
        meta.membership_used_root,
        meta.metadata_root,
        meta.free_bitmap_root,
        meta.retirement_root,
    ]
}

#[cfg(test)]
#[path = "draft_store_tests.rs"]
mod tests;
