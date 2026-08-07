//! Allocation-free named-feed catalog reads.

use std::fmt;

use crate::contract::{u16_le, u32_le, MetaV4, ValueKind, MAX_TREE_LEVEL};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName, MAX_FEED_NAME};
use crate::mapping::{ByteSource, Mapping};
use crate::process_identity::ProcessIdentity;
use crate::slotted_page::{self, Header};

mod mutation;

pub(crate) use mutation::{delete, insert, rename};

pub(crate) const NAME_BRANCH: u8 = 3;
pub(crate) const NAME_LEAF: u8 = 4;
pub(crate) const INDEX_BRANCH: u8 = 5;
pub(crate) const INDEX_LEAF: u8 = 6;
pub(crate) const NAME_RECORD_BASE: usize = 12;
pub(crate) const MAX_NAME_RECORD: usize = NAME_RECORD_BASE + MAX_FEED_NAME;

pub(crate) fn lookup(
    mapping: &Mapping,
    meta: &MetaV4,
    name: &FeedName,
) -> Result<Option<FeedEntry>> {
    if meta.catalog_name_root == 0 {
        return Ok(None);
    }
    let mut page_number = meta.catalog_name_root;
    let mut expected = None;
    for _ in 0..=MAX_TREE_LEVEL {
        let page = mapping.page(page_number, meta.page_count)?;
        let header = parse_name_header(page, meta.txn_id, expected)?;
        let (position, exact) = lower_bound_name(page, &header, name)?;
        if header.level == 0 {
            return lookup_leaf(page, &header, position, exact, meta.feed_index_limit);
        }
        let Some(position) = position.checked_sub(usize::from(!exact)) else {
            return Ok(None);
        };
        let record = decode_name_record(page, &header, position)?;
        page_number = require_child(record.value, meta.page_count)?;
        expected = Some(header.level - 1);
    }
    Err(Error::Corrupt("feed name tree exceeds its maximum height"))
}

/// Forward cursor over the numeric catalog tree.
pub struct FeedCursor<'a> {
    mapping: &'a Mapping,
    meta: MetaV4,
    path: [Frame; MAX_TREE_LEVEL as usize],
    depth: usize,
    leaf: Option<Leaf>,
    index: usize,
    emitted: u64,
    previous: Option<u32>,
    finished: bool,
    owner_identity: Option<ProcessIdentity>,
}

#[derive(Clone, Copy)]
struct Frame {
    page_number: u32,
    index: usize,
    item_count: usize,
    level: u16,
}

const EMPTY_FRAME: Frame = Frame {
    page_number: 0,
    index: 0,
    item_count: 0,
    level: 0,
};

#[derive(Clone, Copy)]
struct Leaf {
    page_number: u32,
    header: Header,
}

impl<'a> FeedCursor<'a> {
    pub(crate) fn new(mapping: &'a Mapping, meta: &MetaV4) -> Result<Self> {
        Self::with_owner(mapping, meta, None)
    }

    pub(crate) fn new_live(
        mapping: &'a Mapping,
        meta: &MetaV4,
        owner_identity: ProcessIdentity,
    ) -> Result<Self> {
        Self::with_owner(mapping, meta, Some(owner_identity))
    }

    fn with_owner(
        mapping: &'a Mapping,
        meta: &MetaV4,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        let mut cursor = Self {
            mapping,
            meta: *meta,
            path: [EMPTY_FRAME; MAX_TREE_LEVEL as usize],
            depth: 0,
            leaf: None,
            index: 0,
            emitted: 0,
            previous: None,
            finished: meta.catalog_index_root == 0,
            owner_identity,
        };
        if !cursor.finished {
            cursor.descend_left(meta.catalog_index_root, None)?;
        }
        Ok(cursor)
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
        if self.emitted != 0 {
            self.advance()?;
            if self.finished {
                return Ok(None);
            }
        }
        let leaf = self.leaf.expect("active feed cursor has a leaf");
        let page = self.mapping.page(leaf.page_number, self.meta.page_count)?;
        let entry = decode_leaf(page, &leaf.header, self.index, self.meta.feed_index_limit)?;
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
            .ok_or(Error::ArithmeticOverflow("feed cursor count"))?;
        if self.emitted > self.meta.active_feed_count {
            self.finished = true;
            return Err(Error::Corrupt("feed catalog exceeds its declared count"));
        }
        Ok(Some(entry))
    }

    fn advance(&mut self) -> Result<()> {
        let item_count = self
            .leaf
            .as_ref()
            .expect("active feed cursor has a leaf")
            .header
            .item_count;
        if self.index + 1 < item_count {
            self.index += 1;
            return Ok(());
        }
        while self.depth > 0 {
            let slot = self.depth - 1;
            let mut frame = self.path[slot];
            if frame.index + 1 >= frame.item_count {
                self.depth = slot;
                continue;
            }
            frame.index += 1;
            self.path[slot] = frame;
            self.depth = slot + 1;
            let page = self.mapping.page(frame.page_number, self.meta.page_count)?;
            let header = parse_index_header(page, self.meta.txn_id, Some(frame.level))?;
            if header.item_count != frame.item_count {
                return Err(Error::Corrupt("feed branch changed during traversal"));
            }
            let child = index_child(page, &header, frame.index, self.meta.page_count)?;
            return self.descend_left(child, Some(frame.level - 1));
        }
        self.finished = true;
        if self.emitted != self.meta.active_feed_count {
            return Err(Error::Corrupt("feed catalog count is incomplete"));
        }
        Ok(())
    }

    fn descend_left(&mut self, mut page_number: u32, mut expected: Option<u16>) -> Result<()> {
        loop {
            let page = self.mapping.page(page_number, self.meta.page_count)?;
            let header = parse_index_header(page, self.meta.txn_id, expected)?;
            if header.level == 0 {
                self.leaf = Some(Leaf {
                    page_number,
                    header,
                });
                self.index = 0;
                return Ok(());
            }
            self.push(page_number, &header)?;
            page_number = index_child(page, &header, 0, self.meta.page_count)?;
            expected = Some(header.level - 1);
        }
    }

    fn push(&mut self, page_number: u32, header: &Header) -> Result<()> {
        let frame = self
            .path
            .get_mut(self.depth)
            .ok_or(Error::Corrupt("feed index tree exceeds its maximum height"))?;
        *frame = Frame {
            page_number,
            index: 0,
            item_count: header.item_count,
            level: header.level,
        };
        self.depth += 1;
        Ok(())
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

struct NameRecord {
    name: FeedName,
    value: u32,
}

fn lower_bound_name<S: ByteSource>(
    page: S,
    header: &Header,
    target: &FeedName,
) -> Result<(usize, bool)> {
    let mut lower = 0;
    let mut upper = header.item_count;
    while lower < upper {
        let middle = lower + (upper - lower) / 2;
        if decode_name_record(page, header, middle)?.name < *target {
            lower = middle + 1;
        } else {
            upper = middle;
        }
    }
    let exact =
        lower < header.item_count && decode_name_record(page, header, lower)?.name == *target;
    Ok((lower, exact))
}

fn decode_leaf<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    feed_index_limit: u64,
) -> Result<FeedEntry> {
    let record = decode_name_record(page, header, index)?;
    if u64::from(record.value) >= feed_index_limit {
        return Err(Error::Corrupt("feed index is outside the declared limit"));
    }
    Ok(FeedEntry {
        name: record.name,
        index: record.value,
    })
}

fn lookup_leaf<S: ByteSource>(
    page: S,
    header: &Header,
    position: usize,
    exact: bool,
    feed_index_limit: u64,
) -> Result<Option<FeedEntry>> {
    if !exact {
        return Ok(None);
    }
    decode_leaf(page, header, position, feed_index_limit).map(Some)
}

fn decode_name_record<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<NameRecord> {
    let record = slotted_page::record(page, header, index, 13, MAX_NAME_RECORD)?;
    decode_record(record)
}

fn decode_record<S: ByteSource>(record: S) -> Result<NameRecord> {
    let entry = decode_entry(record)?;
    Ok(NameRecord {
        name: entry.name,
        value: entry.index,
    })
}

pub(crate) fn decode_entry<S: ByteSource>(record: S) -> Result<FeedEntry> {
    let name_len = usize::from(
        record
            .byte(8)
            .ok_or(Error::Corrupt("feed catalog record is malformed"))?,
    );
    if usize::from(u16_le(record, 0)) != NAME_RECORD_BASE + name_len
        || u16_le(record, 2) != 0
        || !record.all_zero(9, 3)
    {
        return Err(Error::Corrupt("feed catalog record is malformed"));
    }
    let name = FeedName::from_source(record, NAME_RECORD_BASE, name_len)
        .ok_or(Error::Corrupt("feed catalog name is invalid"))?;
    Ok(FeedEntry {
        name,
        index: u32_le(record, 4),
    })
}

fn index_child<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    page_count: u64,
) -> Result<u32> {
    let record = slotted_page::cell(page, header, index, 8)?;
    require_child(u32_le(record, 4), page_count)
}

fn require_child(child: u32, page_count: u64) -> Result<u32> {
    if child < 2 || u64::from(child) >= page_count {
        return Err(Error::Corrupt("feed catalog child is outside page bounds"));
    }
    Ok(child)
}

fn parse_name_header<S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected: Option<u16>,
) -> Result<Header> {
    let level = u16_le(page, 18);
    let page_type = if level == 0 { NAME_LEAF } else { NAME_BRANCH };
    slotted_page::parse(page, selected_txn, page_type, 0, expected)
}

fn parse_index_header<S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected: Option<u16>,
) -> Result<Header> {
    let level = u16_le(page, 18);
    let page_type = if level == 0 { INDEX_LEAF } else { INDEX_BRANCH };
    slotted_page::parse(page, selected_txn, page_type, 0, expected)
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
