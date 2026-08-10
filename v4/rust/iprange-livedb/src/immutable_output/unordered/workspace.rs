//! High-offset mapped workspace inside one private final output inode.

use crate::contract::{u32_le, u64_le};
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiringStore, Store};
use crate::mapping::{Mapping, PageMut, PageView};
use crate::page_header;
use crate::range_mutation::RangeStore;

const FREE_MAGIC: u32 = 0x5746_5245;
const FREE_NEXT: usize = 4;
const FREE_TXN: usize = 8;

pub(super) struct Workspace {
    mapping: Mapping,
    first: u64,
    limit: u64,
    next: u64,
    free: u32,
    txn: u64,
}

impl Workspace {
    pub(super) fn new(mapping: Mapping, first: u64, limit: u64, txn: u64) -> Result<Self> {
        if first > limit
            || limit.checked_mul(crate::contract::PAGE_SIZE as u64) != Some(mapping.len())
        {
            return Err(Error::InvalidArgument(
                "immutable construction workspace is invalid",
            ));
        }
        Ok(Self {
            mapping,
            first,
            limit,
            next: first,
            free: 0,
            txn,
        })
    }

    pub(super) const fn page_count(&self) -> u64 {
        self.next
    }

    fn require_allocated(&self, page: u32) -> Result<()> {
        if u64::from(page) < self.first || u64::from(page) >= self.next {
            Err(Error::Corrupt(
                "immutable workspace page is outside its allocation",
            ))
        } else {
            Ok(())
        }
    }

    fn pop_free(&mut self) -> Result<u32> {
        let page = self.free;
        self.require_allocated(page)?;
        let next = self.mapping.page(page, self.next)?;
        if u32_le(next, 0) != FREE_MAGIC || u64_le(next, FREE_TXN) != self.txn {
            return Err(Error::Corrupt("immutable workspace free link is invalid"));
        }
        let following = u32_le(next, FREE_NEXT);
        if following != 0
            && (following == page
                || u64::from(following) < self.first
                || u64::from(following) >= self.next)
        {
            return Err(Error::Corrupt("immutable workspace free link is invalid"));
        }
        self.free = following;
        Ok(page)
    }
}

impl Store for Workspace {
    type ReadPage<'a>
        = PageView<'a>
    where
        Self: 'a;
    type WritePage<'a>
        = PageMut<'a>
    where
        Self: 'a;

    fn target_txn(&self) -> u64 {
        self.txn
    }

    fn page_limit(&self) -> u64 {
        self.next
    }

    fn inspect_page<'a, T, F>(&'a self, page: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>) -> Result<T>,
    {
        self.require_allocated(page)?;
        inspect(self.mapping.page(page, self.next)?)
    }

    fn allocate(&mut self) -> Result<u32> {
        if self.free != 0 {
            return self.pop_free();
        }
        if self.next == self.limit {
            return Err(Error::BudgetExceeded(
                "immutable construction workspace pages",
            ));
        }
        let page = u32::try_from(self.next).map_err(|_| Error::PageSpaceExhausted)?;
        self.next += 1;
        crate::work::page_created(1);
        Ok(page)
    }

    fn update_page<'a, T, F>(&'a mut self, page: u32, update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>,
    {
        self.require_allocated(page)?;
        let mut page = self.mapping.page_mut(page, self.next)?;
        update(&mut page)
    }

    fn copy_page<'a, T, F>(&'a mut self, source: u32, destination: u32, copy: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>, &mut Self::WritePage<'a>) -> Result<T>,
    {
        self.require_allocated(source)?;
        self.require_allocated(destination)?;
        let (source, mut destination) = self.mapping.page_pair(source, destination, self.next)?;
        let result = copy(source, &mut destination)?;
        crate::work::page_copied(1);
        Ok(result)
    }

    fn discard_private(&mut self, page: u32) -> Result<()> {
        self.require_allocated(page)?;
        if page == self.free {
            return Err(Error::Corrupt(
                "immutable workspace page was discarded twice",
            ));
        }
        let mut target = self.mapping.page_mut(page, self.next)?;
        if !page_header::owned_by(target.view(), self.txn) {
            return Err(Error::Corrupt(
                "immutable workspace discarded a foreign page",
            ));
        }
        target.put_u32(0, FREE_MAGIC)?;
        target.put_u32(FREE_NEXT, self.free)?;
        target.put_u64(FREE_TXN, self.txn)?;
        self.free = page;
        Ok(())
    }
}

impl RetiringStore for Workspace {
    fn retire_pages(&mut self, pages: &[u32]) -> Result<()> {
        for &page in pages {
            self.discard_private(page)?;
        }
        Ok(())
    }
}

impl RangeStore for Workspace {
    fn range_record_added(&mut self, _value: u32) -> Result<()> {
        Ok(())
    }

    fn range_record_removed(&mut self, _value: u32) -> Result<()> {
        Ok(())
    }
}
