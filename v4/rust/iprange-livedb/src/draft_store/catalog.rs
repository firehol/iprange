//! Draft-owned feed catalog operations.

use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::feed_catalog;
use crate::fixed_tree::{RetiredPages, RetiringStore};
use crate::used_bitmap::{self, Kind};

use super::DraftStore;

impl DraftStore<'_> {
    pub(crate) fn lookup_feed(&self, name: &FeedName) -> Result<Option<FeedEntry>> {
        feed_catalog::lookup(self.file, &self.draft.meta, name)
    }

    pub(crate) fn ensure_feed(&mut self, name: FeedName) -> Result<(FeedEntry, bool)> {
        if let Some(entry) = self.lookup_feed(&name)? {
            return Ok((entry, false));
        }
        let index = self.allocate_feed_index()?;
        let entry = FeedEntry { name, index };
        let mut name_root = self.draft.meta.catalog_name_root;
        let mut index_root = self.draft.meta.catalog_index_root;
        feed_catalog::insert(self, &mut name_root, &mut index_root, entry)?;
        self.draft.meta.catalog_name_root = name_root;
        self.draft.meta.catalog_index_root = index_root;
        self.draft.meta.active_feed_count = self
            .draft
            .meta
            .active_feed_count
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("active feed count"))?;
        self.draft.changed = true;
        Ok((entry, true))
    }

    pub(crate) fn rename_feed(&mut self, old: FeedName, new: FeedName) -> Result<FeedEntry> {
        let entry = self.lookup_feed(&old)?.ok_or(Error::NameNotFound)?;
        if self.lookup_feed(&new)?.is_some() {
            return Err(Error::NameExists);
        }
        let mut name_root = self.draft.meta.catalog_name_root;
        let mut index_root = self.draft.meta.catalog_index_root;
        feed_catalog::rename(self, &mut name_root, &mut index_root, entry, new)?;
        self.draft.meta.catalog_name_root = name_root;
        self.draft.meta.catalog_index_root = index_root;
        self.draft.changed = true;
        Ok(FeedEntry {
            name: new,
            index: entry.index,
        })
    }

    pub(crate) fn rename_feed_ref(
        &mut self,
        expected: FeedEntry,
        new: FeedName,
    ) -> Result<FeedEntry> {
        if self.lookup_feed(&expected.name)? != Some(expected) {
            return Err(Error::StaleReference);
        }
        self.rename_feed(expected.name, new)
    }

    pub(crate) fn remove_feed(&mut self, expected: FeedEntry) -> Result<()> {
        if self.lookup_feed(&expected.name)? != Some(expected) {
            return Err(Error::StaleReference);
        }
        let mut name_root = self.draft.meta.catalog_name_root;
        let mut index_root = self.draft.meta.catalog_index_root;
        feed_catalog::delete(self, &mut name_root, &mut index_root, expected)?;
        self.draft.meta.catalog_name_root = name_root;
        self.draft.meta.catalog_index_root = index_root;

        let mut used_root = self.draft.meta.feed_used_root;
        let mut retired = RetiredPages::new();
        if !used_bitmap::clear(
            self,
            &mut used_root,
            self.draft.meta.feed_index_limit,
            Kind::Feed,
            expected.index,
            &mut retired,
        )? {
            return Err(Error::Corrupt("deleted feed used bit is missing"));
        }
        self.draft.meta.feed_used_root = used_root;
        self.retire_pages(retired.as_slice())?;
        self.draft.meta.active_feed_count = self
            .draft
            .meta
            .active_feed_count
            .checked_sub(1)
            .ok_or(Error::ArithmeticOverflow("active feed count"))?;
        self.draft.changed = true;
        Ok(())
    }

    fn allocate_feed_index(&mut self) -> Result<u32> {
        let mut root = self.draft.meta.feed_used_root;
        let limit = self.draft.meta.feed_index_limit;
        let mut retired = RetiredPages::new();
        let reused = used_bitmap::take_lowest(self, &mut root, limit, Kind::Feed, &mut retired)?;
        let index = match reused {
            Some(index) => index,
            None => {
                if limit == 1u64 << 32 {
                    return Err(Error::FeedIndexExhausted);
                }
                let index = limit as u32;
                let next_limit = limit + 1;
                used_bitmap::set(self, &mut root, next_limit, Kind::Feed, index, &mut retired)?;
                self.draft.meta.feed_index_limit = next_limit;
                index
            }
        };
        self.draft.meta.feed_used_root = root;
        self.retire_pages(retired.as_slice())?;
        Ok(index)
    }
}
