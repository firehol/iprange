//! Canonical persistent page identities for the exact v4 format.

use crate::contract::MetaV4;
use crate::error::Result;
use crate::fixed_tree::PageSource;
use crate::mapping::{Mapping, PageView};

#[derive(Clone, Copy)]
pub(crate) struct Generation<'a> {
    mapping: &'a Mapping,
    meta: MetaV4,
}

impl<'a> Generation<'a> {
    pub(crate) const fn new(mapping: &'a Mapping, meta: MetaV4) -> Self {
        Self { mapping, meta }
    }
}

impl PageSource for Generation<'_> {
    type Page<'a>
        = PageView<'a>
    where
        Self: 'a;

    fn selected_txn(&self) -> u64 {
        self.meta.txn_id
    }

    fn selected_page_limit(&self) -> u64 {
        self.meta.page_count
    }

    fn view_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::Page<'a>) -> Result<T>,
    {
        inspect(self.mapping.page(page_number, self.meta.page_count)?)
    }
}

pub(crate) mod page_type {
    pub(crate) const RANGE_BRANCH: u8 = 1;
    pub(crate) const RANGE_LEAF: u8 = 2;
    pub(crate) const FEED_NAME_BRANCH: u8 = 3;
    pub(crate) const FEED_NAME_LEAF: u8 = 4;
    pub(crate) const FEED_INDEX_BRANCH: u8 = 5;
    pub(crate) const FEED_INDEX_LEAF: u8 = 6;
    pub(crate) const MEMBERSHIP_ID_BRANCH: u8 = 7;
    pub(crate) const MEMBERSHIP_ID_LEAF: u8 = 8;
    pub(crate) const MEMBERSHIP_HASH_BRANCH: u8 = 9;
    pub(crate) const MEMBERSHIP_HASH_LEAF: u8 = 10;
    pub(crate) const MEMBERSHIP_BLOB_BRANCH: u8 = 11;
    pub(crate) const MEMBERSHIP_BLOB_LEAF: u8 = 12;
    pub(crate) const METADATA: u8 = 13;
    pub(crate) const USED_BITMAP_BRANCH: u8 = 14;
    pub(crate) const USED_BITMAP_LEAF: u8 = 15;
    pub(crate) const RETIREMENT_BRANCH: u8 = 16;
    pub(crate) const RETIREMENT_LEAF: u8 = 17;
    pub(crate) const STRUCTURE_ID_BRANCH: u8 = 18;
    pub(crate) const STRUCTURE_ID_LEAF: u8 = 19;
    pub(crate) const STRUCTURE_HASH_BRANCH: u8 = 20;
    pub(crate) const STRUCTURE_HASH_LEAF: u8 = 21;
}
