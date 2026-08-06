//! File-backed page storage and allocator integration.

use crate::contract::{u32_le, u64_le, MetaV4, PAGE_MAGIC, PAGE_SHIFT, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;
use crate::fixed_tree::{RetiredPages, RetiringStore, Store};
use crate::free_bitmap::{self, BitmapStore};
use crate::page_checksum;
use crate::slotted_page::put_u32;

use super::{DraftStore, PRIVATE_MAGIC};

const DIRTY_END: u32 = 1;

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
        let file = self.file;
        if let Some(cache) = self.draft.page_cache.borrow_mut().as_mut() {
            cache.prepare(page_number, &mut |dirty_page, bytes| {
                persist_unsealed(file, dirty_page, bytes)
            })?;
        }
        file_io::read_page(self.file, page_number, self.draft.meta.page_count, page)?;
        if let Some(cache) = self.draft.page_cache.borrow_mut().as_mut() {
            cache.store_clean(page_number, page, &mut |dirty_page, bytes| {
                persist_unsealed(file, dirty_page, bytes)
            })?;
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
        let tag = if page[..4] == PAGE_MAGIC {
            if u64_le(page, 8) != self.draft.meta.txn_id {
                return Err(Error::Corrupt("draft write has the wrong transaction"));
            }
            self.dirty_tag(page_number)?
        } else {
            u32_le(page, 28)
        };
        self.persist_tagged(page_number, page, tag)
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
        let dirty_tag = u32_le(&page, 28);
        if dirty_tag == 0 {
            return Err(Error::Corrupt(
                "private page is absent from the dirty chain",
            ));
        }
        page.fill(0);
        put_u32(&mut page, 0, PRIVATE_MAGIC);
        put_u32(&mut page, 4, self.draft.private_head);
        crate::slotted_page::put_u64(&mut page, 8, self.draft.meta.txn_id);
        put_u32(&mut page, 28, dirty_tag);
        self.write(page_number, &page)?;
        self.draft.private_head = page_number;
        Ok(())
    }
}

impl DraftStore<'_> {
    pub(crate) fn seal_private_pages<F>(&mut self, checkpoint: &mut F) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        let mut page_number = self.draft.dirty_head;
        let mut remaining = self.draft.private_pages;
        while page_number != 0 {
            checkpoint()?;
            if remaining == 0 {
                return Err(Error::Corrupt("draft dirty-page chain is cyclic"));
            }
            remaining -= 1;

            let mut page = [0; PAGE_SIZE];
            self.read(page_number, &mut page)?;
            let next = dirty_next(u32_le(&page, 28), page_number, self.page_limit())?;
            if page[..4] == PAGE_MAGIC {
                if u64_le(&page, 8) != self.draft.meta.txn_id {
                    return Err(Error::Corrupt("dirty page has the wrong transaction"));
                }
                page_checksum::seal(&mut page)?;
                self.persist(page_number, &page)?;
            }
            page_number = next;
        }
        self.flush_page_cache()?;
        self.draft.dirty_head = 0;
        Ok(())
    }

    pub(crate) fn flush_page_cache(&self) -> Result<()> {
        let file = self.file;
        if let Some(cache) = self.draft.page_cache.borrow_mut().as_mut() {
            cache.flush(&mut |page_number, bytes| persist_unsealed(file, page_number, bytes))?;
        }
        Ok(())
    }

    fn dirty_tag(&mut self, page_number: u32) -> Result<u32> {
        if let Some(tag) = self.existing_dirty_tag(page_number)? {
            return Ok(tag);
        }
        let tag = if self.draft.dirty_head == 0 {
            DIRTY_END
        } else {
            self.draft.dirty_head
        };
        self.draft.dirty_head = page_number;
        Ok(tag)
    }

    fn existing_dirty_tag(&self, page_number: u32) -> Result<Option<u32>> {
        let header = self
            .draft
            .page_cache
            .borrow()
            .as_ref()
            .and_then(|cache| cache.header(page_number));
        let header = match header {
            Some(header) => Some(header),
            None => self.file_header(page_number)?,
        };
        let Some(header) = header else {
            return Ok(None);
        };
        let owned_data = header[..4] == PAGE_MAGIC && u64_le(&header, 8) == self.draft.meta.txn_id;
        let private_stack =
            u32_le(&header, 0) == PRIVATE_MAGIC && u64_le(&header, 8) == self.draft.meta.txn_id;
        let tag = u32_le(&header, 28);
        Ok(((owned_data || private_stack) && tag != 0).then_some(tag))
    }

    fn file_header(&self, page_number: u32) -> Result<Option<[u8; 32]>> {
        let offset = page_offset(page_number)?;
        if self.file.metadata()?.len() < offset + 32 {
            return Ok(None);
        }
        let mut header = [0; 32];
        file_io::read_exact_at(self.file, &mut header, offset)?;
        Ok(Some(header))
    }

    fn persist_tagged(&self, page_number: u32, page: &[u8; PAGE_SIZE], tag: u32) -> Result<()> {
        let file = self.file;
        if let Some(cache) = self.draft.page_cache.borrow_mut().as_mut() {
            if cache.store_dirty(page_number, page, tag, &mut |dirty_page, bytes| {
                persist_unsealed(file, dirty_page, bytes)
            })? {
                return Ok(());
            }
        }
        let mut bytes = *page;
        put_u32(&mut bytes, 28, tag);
        persist_unsealed(self.file, page_number, &bytes)
    }

    fn persist(&self, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()> {
        let offset = page_offset(page_number)?;
        let file = self.file;
        if let Some(cache) = self.draft.page_cache.borrow_mut().as_mut() {
            cache.store_clean(page_number, page, &mut |dirty_page, bytes| {
                persist_unsealed(file, dirty_page, bytes)
            })?;
        }
        file_io::write_exact_at(self.file, page, offset)
    }
}

fn page_offset(page_number: u32) -> Result<u64> {
    u64::from(page_number)
        .checked_shl(u32::from(PAGE_SHIFT))
        .ok_or(Error::ArithmeticOverflow("draft page offset"))
}

fn persist_unsealed(file: &std::fs::File, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()> {
    file_io::write_exact_at(file, page, page_offset(page_number)?)
}

fn dirty_next(tag: u32, page_number: u32, page_limit: u64) -> Result<u32> {
    if tag == DIRTY_END {
        return Ok(0);
    }
    if tag < 2 || tag == page_number || u64::from(tag) >= page_limit {
        return Err(Error::Corrupt("draft dirty-page link is invalid"));
    }
    Ok(tag)
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
