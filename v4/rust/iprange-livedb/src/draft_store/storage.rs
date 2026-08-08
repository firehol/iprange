//! Mapped page storage and allocator integration.

use crate::contract::{u32_le, u64_le, MetaV4, PAGE_MAGIC};
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiredPages, RetiringStore, Store};
use crate::free_bitmap::{self, BitmapStore};
use crate::mapping::{ByteSource, PageMut, PageView};
use crate::page_checksum;

use super::{DraftStore, PRIVATE_MAGIC};

const DIRTY_END: u32 = 1;

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
            self.charge_private()?;
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
        let tag = self.dirty_tag(page_number)?;
        let mut page = self
            .mapping
            .page_mut(page_number, self.draft.meta.page_count)?;
        let result = update(&mut page)?;
        require_private_output(page.view(), self.draft.meta.txn_id)?;
        page.put_u32(28, tag)?;
        Ok(result)
    }

    fn copy_page<'a, T, F>(&'a mut self, source: u32, destination: u32, copy: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>, &mut Self::WritePage<'a>) -> Result<T>,
    {
        require_page(source, self.draft.meta.page_count)?;
        require_page(destination, self.draft.meta.page_count)?;
        let tag = self.dirty_tag(destination)?;
        let (source, mut destination) =
            self.mapping
                .page_pair(source, destination, self.draft.meta.page_count)?;
        let result = copy(source, &mut destination)?;
        require_private_output(destination.view(), self.draft.meta.txn_id)?;
        destination.put_u32(28, tag)?;
        crate::work::page_copied(1);
        Ok(result)
    }

    fn discard_private(&mut self, page_number: u32) -> Result<()> {
        if page_number == self.draft.private_head {
            return Err(Error::Corrupt("discarded private page is invalid"));
        }
        let txn = self.draft.meta.txn_id;
        let dirty_tag = self.inspect_page(page_number, |page| {
            if u64_le(page, 8) != txn {
                return Err(Error::Corrupt(
                    "committed page cannot enter the private stack",
                ));
            }
            let tag = u32_le(page, 28);
            if tag == 0 {
                return Err(Error::Corrupt(
                    "private page is absent from the dirty chain",
                ));
            }
            Ok(tag)
        })?;
        let next = self.draft.private_head;
        self.update_page(page_number, |page| {
            page.fill(0);
            page.put_u32(0, PRIVATE_MAGIC)?;
            page.put_u32(4, next)?;
            page.put_u64(8, txn)?;
            page.put_u32(28, dirty_tag)
        })?;
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
                let next = dirty_next(u32_le(page, 28), page_number, limit)?;
                if page.equals(0, &PAGE_MAGIC) && u64_le(page, 8) != txn {
                    return Err(Error::Corrupt("dirty page has the wrong transaction"));
                }
                Ok((next, page.equals(0, &PAGE_MAGIC)))
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
        require_page(page_number, self.draft.meta.page_count)?;
        let page = self.mapping.page(page_number, self.draft.meta.page_count)?;
        let owned_data = page.equals(0, &PAGE_MAGIC) && u64_le(page, 8) == self.draft.meta.txn_id;
        let private_stack =
            u32_le(page, 0) == PRIVATE_MAGIC && u64_le(page, 8) == self.draft.meta.txn_id;
        let tag = u32_le(page, 28);
        Ok(((owned_data || private_stack) && tag != 0).then_some(tag))
    }

    pub(super) fn claim_allocated(&mut self, page_number: u32) -> Result<()> {
        require_page(page_number, self.draft.meta.page_count)?;
        let tag = if self.draft.dirty_head == 0 {
            DIRTY_END
        } else {
            self.draft.dirty_head
        };
        let txn = self.draft.meta.txn_id;
        let mut page = self
            .mapping
            .page_mut(page_number, self.draft.meta.page_count)?;
        page.zero(0, 32)?;
        page.put_u32(0, PRIVATE_MAGIC)?;
        page.put_u64(8, txn)?;
        page.put_u32(28, tag)?;
        self.draft.dirty_head = page_number;
        crate::work::page_created(1);
        Ok(())
    }
}

fn require_private_output(page: PageView<'_>, target_txn: u64) -> Result<()> {
    let data = page.equals(0, &PAGE_MAGIC) && u64_le(page, 8) == target_txn;
    let private = u32_le(page, 0) == PRIVATE_MAGIC && u64_le(page, 8) == target_txn;
    let reserve = page.all_zero(0, 28);
    if data || private || reserve {
        Ok(())
    } else {
        Err(Error::Corrupt("draft update has the wrong transaction"))
    }
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
            self.charge_private()?;
            self.claim_allocated(page)?;
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
