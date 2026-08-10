//! Allocation-free named-feed catalog reads.

use std::fmt;

use crate::contract::{MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::fixed_tree::{self, CursorDirection, LeafQuery};
use crate::format::Generation;
use crate::mapping::{ByteSource, Mapping};
use crate::process_identity::ProcessIdentity;
use crate::slotted_page::Header;

mod codec;
mod mutation;

#[cfg(test)]
pub(crate) use codec::NAME_RECORD_BASE;
pub(crate) use codec::{
    decode_entry, decode_index_branch, INDEX_BRANCH, INDEX_BRANCH_SIZE, INDEX_LEAF,
    MAX_NAME_RECORD, MIN_NAME_RECORD, NAME_BRANCH, NAME_LEAF,
};
use codec::{IndexCodec, NameCodec};
pub(crate) use mutation::{delete, insert, rename};

pub(crate) fn lookup(
    mapping: &Mapping,
    meta: &MetaV4,
    name: &FeedName,
) -> Result<Option<FeedEntry>> {
    crate::work::catalog_lookup(1);
    fixed_tree::query::<NameCodec, _, _>(
        &Generation::new(mapping, *meta),
        meta.catalog_name_root,
        *name,
        &mut FeedLookup {
            feed_index_limit: meta.feed_index_limit,
        },
    )
}

pub(crate) fn lookup_index(
    mapping: &Mapping,
    meta: &MetaV4,
    index: u32,
) -> Result<Option<FeedEntry>> {
    crate::work::catalog_lookup(1);
    fixed_tree::query::<IndexCodec, _, _>(
        &Generation::new(mapping, *meta),
        meta.catalog_index_root,
        index,
        &mut IndexLookup {
            feed_index_limit: meta.feed_index_limit,
        },
    )
}

struct FeedLookup {
    feed_index_limit: u64,
}

struct IndexLookup {
    feed_index_limit: u64,
}

impl LeafQuery<NameCodec> for FeedLookup {
    type Output = FeedEntry;

    fn inspect<S: ByteSource>(
        &mut self,
        page: S,
        header: &Header,
        _page_number: u32,
        position: usize,
        exact: bool,
    ) -> Result<Option<Self::Output>> {
        if !exact {
            return Ok(None);
        }
        decode_leaf(page, header, position, self.feed_index_limit).map(Some)
    }
}

impl LeafQuery<IndexCodec> for IndexLookup {
    type Output = FeedEntry;

    fn inspect<S: ByteSource>(
        &mut self,
        _page: S,
        _header: &Header,
        _page_number: u32,
        _position: usize,
        exact: bool,
    ) -> Result<Option<Self::Output>> {
        if !exact {
            return Ok(None);
        }
        decode_leaf(_page, _header, _position, self.feed_index_limit).map(Some)
    }
}

/// Forward cursor over the numeric catalog tree.
pub struct FeedCursor<'a> {
    mapping: &'a Mapping,
    meta: MetaV4,
    inner: fixed_tree::Cursor<IndexCodec>,
    emitted: u64,
    previous: Option<u32>,
    finished: bool,
    owner_identity: Option<ProcessIdentity>,
}

impl<'a> FeedCursor<'a> {
    pub(crate) fn new(
        mapping: &'a Mapping,
        meta: &MetaV4,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        let source = Generation::new(mapping, *meta);
        Ok(Self {
            mapping,
            meta: *meta,
            inner: fixed_tree::Cursor::new(
                &source,
                meta.catalog_index_root,
                CursorDirection::Forward,
            )?,
            emitted: 0,
            previous: None,
            finished: meta.catalog_index_root == 0,
            owner_identity,
        })
    }

    /// Return the next catalog entry in ascending feed-index order.
    pub fn next_feed(&mut self) -> Result<Option<FeedEntry>> {
        self.require_owner()?;
        let result = self.next_inner();
        if result.is_err() {
            self.finished = true;
        }
        result
    }

    fn next_inner(&mut self) -> Result<Option<FeedEntry>> {
        if self.finished {
            return Ok(None);
        }
        let Some(entry) = self.inner.next_leaf_mapped(self.mapping)? else {
            self.finished = true;
            if self.emitted != self.meta.active_feed_count {
                return Err(Error::Corrupt("feed catalog count is incomplete"));
            }
            return Ok(None);
        };
        if u64::from(entry.index) >= self.meta.feed_index_limit {
            return Err(Error::Corrupt("feed index is outside the declared limit"));
        }
        if self
            .previous
            .is_some_and(|previous| previous >= entry.index)
        {
            self.finished = true;
            return Err(Error::Corrupt("feed indexes are not strictly increasing"));
        }
        self.previous = Some(entry.index);
        self.emitted = self
            .emitted
            .checked_add(1)
            .ok_or_else(|| Error::arithmetic_overflow("feed cursor count"))?;
        if self.emitted > self.meta.active_feed_count {
            self.finished = true;
            return Err(Error::Corrupt("feed catalog exceeds its declared count"));
        }
        Ok(Some(entry))
    }

    fn require_owner(&self) -> Result<()> {
        if self.owner_identity.is_some_and(|owner| !owner.is_current()) {
            return Err(Error::ForkedHandle);
        }
        Ok(())
    }
}

impl fmt::Debug for FeedCursor<'_> {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("FeedCursor")
            .field("emitted", &self.emitted)
            .field("finished", &self.finished)
            .finish_non_exhaustive()
    }
}

fn decode_leaf<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    feed_index_limit: u64,
) -> Result<FeedEntry> {
    let entry = codec::page_entry(page, header, index)?;
    if u64::from(entry.index) >= feed_index_limit {
        return Err(Error::Corrupt("feed index is outside the declared limit"));
    }
    Ok(entry)
}

pub(crate) fn require_membership(meta: &MetaV4) -> Result<()> {
    if meta.value_kind != ValueKind::Membership {
        return Err(Error::WrongValueKind(
            "feed access requires a membership database",
        ));
    }
    Ok(())
}

#[cfg(test)]
#[path = "feed_catalog_tests.rs"]
mod tests;
