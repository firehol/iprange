//! File-backed page storage and allocator integration.

use crate::contract::{u64_le, MetaV4, PAGE_SHIFT, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;
use crate::fixed_tree::{RetiredPages, RetiringStore, Store};
use crate::free_bitmap::{self, BitmapStore};
use crate::slotted_page::put_u32;

use super::{DraftStore, PRIVATE_MAGIC};

impl Store for DraftStore<'_> {
    fn target_txn(&self) -> u64 {
        self.draft.meta.txn_id
    }

    fn page_limit(&self) -> u64 {
        self.draft.meta.page_count
    }

    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()> {
        if page_number < 2 || u64::from(page_number) >= self.draft.meta.page_count {
            return Err(Error::Corrupt("page number is outside committed bounds"));
        }
        if self
            .draft
            .page_cache
            .borrow()
            .as_ref()
            .is_some_and(|cache| cache.read(page_number, page))
        {
            return Ok(());
        }
        file_io::read_page(self.file, page_number, self.draft.meta.page_count, page)?;
        if let Some(cache) = self.draft.page_cache.borrow_mut().as_mut() {
            cache.store(page_number, page);
        }
        Ok(())
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
        file_io::write_exact_at(self.file, page, offset)?;
        if let Some(cache) = self.draft.page_cache.borrow_mut().as_mut() {
            cache.store(page_number, page);
        }
        Ok(())
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

impl RetiringStore for DraftStore<'_> {
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
