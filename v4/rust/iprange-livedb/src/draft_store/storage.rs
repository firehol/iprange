//! Mapped page storage and allocator integration.

use crate::contract::{u32_le, u64_le};
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiredPages, RetiringStore, Store};
use crate::free_bitmap::{self, BitmapStore};
use crate::mapping::{ByteSource, PageMut, PageView};
use crate::page_checksum;
use crate::page_header;

use super::DraftStore;

const DIRTY_END: u32 = 1;
const PRIVATE_MAGIC: u32 = 0x5046_5245;
const PRIVATE_MAGIC_OFFSET: usize = 0;
const PRIVATE_NEXT_OFFSET: usize = 4;
const PRIVATE_TXN_OFFSET: usize = 8;
const PRIVATE_HEADER_SIZE: usize = 32;

impl Store for DraftStore<'_> {
    type ReadPage<'a>
        = PageView<'a>
    where
        Self: 'a;
    type WritePage<'a>
        = PageMut<'a>
    where
        Self: 'a;

    fn target_txn(&self) -> u64 {
        self.draft.meta.txn_id
    }

    fn page_limit(&self) -> u64 {
        self.draft.meta.page_count
    }

    fn inspect_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>) -> Result<T>,
    {
        require_page(page_number, self.draft.meta.page_count)?;
        inspect(self.mapping.page(page_number, self.draft.meta.page_count)?)
    }

    fn allocate(&mut self) -> Result<u32> {
        if self.draft.private_head != 0 {
            return self.pop_private();
        }
        let mut root = self.draft.meta.free_bitmap_root;
        let mut retired = RetiredPages::new();
        let limit = self.committed_page_count;
        if let Some(page) = free_bitmap::take_lowest(self, &mut root, limit, &mut retired)? {
            self.draft.meta.free_bitmap_root = root;
            self.draft.allocator_retired.extend(retired.as_slice())?;
            self.claim_allocated(page)?;
            return Ok(page);
        }
        self.draft.meta.free_bitmap_root = root;
        self.allocate_tail()
    }

    fn update_page<'a, T, F>(&'a mut self, page_number: u32, update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>,
    {
        require_page(page_number, self.draft.meta.page_count)?;
        let mut page = self
            .mapping
            .page_mut(page_number, self.draft.meta.page_count)?;
        let tag = private_tag(page.view(), self.draft.meta.txn_id)?;
        let result = update(&mut page)?;
        page.put_u32(page_checksum::OFFSET, tag)?;
        Ok(result)
    }

    fn copy_page<'a, T, F>(&'a mut self, source: u32, destination: u32, copy: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>, &mut Self::WritePage<'a>) -> Result<T>,
    {
        require_page(source, self.draft.meta.page_count)?;
        require_page(destination, self.draft.meta.page_count)?;
        let (source, mut destination) =
            self.mapping
                .page_pair(source, destination, self.draft.meta.page_count)?;
        let tag = private_tag(destination.view(), self.draft.meta.txn_id)?;
        let result = copy(source, &mut destination)?;
        destination.put_u32(page_checksum::OFFSET, tag)?;
        crate::work::page_copied(1);
        Ok(result)
    }

    fn discard_private(&mut self, page_number: u32) -> Result<()> {
        if page_number == self.draft.private_head {
            return Err(Error::Corrupt("discarded private page is invalid"));
        }
        let txn = self.draft.meta.txn_id;
        require_page(page_number, self.draft.meta.page_count)?;
        let next = self.draft.private_head;
        let mut page = self
            .mapping
            .page_mut(page_number, self.draft.meta.page_count)?;
        let view = page.view();
        if page_header::born_txn(view) != txn {
            return Err(Error::Corrupt(
                "committed page cannot enter the private stack",
            ));
        }
        if u32_le(view, page_checksum::OFFSET) == 0 {
            return Err(Error::Corrupt(
                "private page is absent from the dirty chain",
            ));
        }
        page.put_u32(PRIVATE_MAGIC_OFFSET, PRIVATE_MAGIC)?;
        page.put_u32(PRIVATE_NEXT_OFFSET, next)?;
        page.put_u64(PRIVATE_TXN_OFFSET, txn)?;
        private_tag(page.view(), txn)?;
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

            let txn = self.draft.meta.txn_id;
            let limit = self.draft.meta.page_count;
            let (next, data_page) = self.inspect_page(page_number, |page| {
                let next = dirty_next(u32_le(page, page_checksum::OFFSET), page_number, limit)?;
                if page_header::has_magic(page) && page_header::born_txn(page) != txn {
                    return Err(Error::Corrupt("dirty page has the wrong transaction"));
                }
                Ok((next, page_header::has_magic(page)))
            })?;
            if data_page {
                let mut page = self.mapping.page_mut(page_number, limit)?;
                page_checksum::seal_mapped(&mut page)?;
            }
            page_number = next;
        }
        self.draft.dirty_head = 0;
        Ok(())
    }

    fn existing_dirty_tag(&self, page_number: u32) -> Result<Option<u32>> {
        require_page(page_number, self.draft.meta.page_count)?;
        let page = self.mapping.page(page_number, self.draft.meta.page_count)?;
        let owned_data = page_header::owned_by(page, self.draft.meta.txn_id);
        let private_stack = is_private_page(page, self.draft.meta.txn_id);
        let tag = u32_le(page, page_checksum::OFFSET);
        Ok(((owned_data || private_stack) && tag != 0).then_some(tag))
    }

    pub(super) fn claim_allocated(&mut self, page_number: u32) -> Result<()> {
        require_page(page_number, self.draft.meta.page_count)?;
        let existing = self.existing_dirty_tag(page_number)?;
        self.claim_page(page_number, existing)
    }

    pub(super) fn claim_new_tail(&mut self, page_number: u32) -> Result<()> {
        require_page(page_number, self.draft.meta.page_count)?;
        self.claim_page(page_number, None)
    }

    fn claim_page(&mut self, page_number: u32, existing: Option<u32>) -> Result<()> {
        let tag = existing.unwrap_or(if self.draft.dirty_head == 0 {
            DIRTY_END
        } else {
            self.draft.dirty_head
        });
        if existing.is_none() {
            self.charge_private()?;
        }
        let txn = self.draft.meta.txn_id;
        let mut page = self
            .mapping
            .page_mut(page_number, self.draft.meta.page_count)?;
        page.zero(0, PRIVATE_HEADER_SIZE)?;
        page.put_u32(PRIVATE_MAGIC_OFFSET, PRIVATE_MAGIC)?;
        page.put_u64(PRIVATE_TXN_OFFSET, txn)?;
        page.put_u32(page_checksum::OFFSET, tag)?;
        if existing.is_none() {
            self.draft.dirty_head = page_number;
            crate::work::page_created(1);
        }
        Ok(())
    }
}

fn private_tag(page: PageView<'_>, target_txn: u64) -> Result<u32> {
    let tag = u32_le(page, page_checksum::OFFSET);
    if (page_header::owned_by(page, target_txn) || is_private_page(page, target_txn)) && tag != 0 {
        Ok(tag)
    } else {
        Err(Error::Corrupt("draft update page is not private"))
    }
}

pub(super) fn private_stack_next<S: ByteSource>(page: S, target_txn: u64) -> Result<u32> {
    if !is_private_page(page, target_txn) {
        return Err(Error::Corrupt("private page stack link is invalid"));
    }
    Ok(u32_le(page, PRIVATE_NEXT_OFFSET))
}

fn is_private_page<S: ByteSource>(page: S, target_txn: u64) -> bool {
    u32_le(page, PRIVATE_MAGIC_OFFSET) == PRIVATE_MAGIC
        && u64_le(page, PRIVATE_TXN_OFFSET) == target_txn
}

fn require_page(page_number: u32, page_limit: u64) -> Result<()> {
    if page_number < 2 || u64::from(page_number) >= page_limit {
        Err(Error::Corrupt("page number is outside draft bounds"))
    } else {
        Ok(())
    }
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
            self.claim_allocated(page)?;
            return Ok(page);
        }
        self.allocate_tail()
    }

    fn allocation_forbidden(&self, page_number: u32) -> bool {
        page_number < 2
            || self.draft.meta.allocator_reserve.contains(&page_number)
            || self.draft.meta.roots().contains(&page_number)
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
