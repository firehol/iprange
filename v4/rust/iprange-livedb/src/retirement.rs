//! Ordered retirement extents for pages removed from committed roots.

use crate::contract::{u32_le, u64_le};
use crate::error::{Error, Result};
use crate::fixed_tree::{self, Codec, RetiredPages, Store};
use crate::format::page_type;
use crate::mapping::ByteSource;

pub(crate) const BRANCH_TYPE: u8 = page_type::RETIREMENT_BRANCH;
pub(crate) const LEAF_TYPE: u8 = page_type::RETIREMENT_LEAF;
pub(crate) const AUX: u32 = 0;
pub(crate) const KEY_SIZE: usize = 12;
pub(crate) const CELL_SIZE: usize = 16;

const TXN_OFFSET: usize = 0;
const FIRST_OFFSET: usize = 8;
const COUNT_OFFSET: usize = 12;

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub(crate) struct Key {
    txn: u64,
    first: u32,
}

impl Key {
    pub(crate) fn first_page(self) -> u32 {
        self.first
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Extent {
    key: Key,
    count: u32,
}

impl Extent {
    pub(crate) fn key(self) -> Key {
        self.key
    }

    pub(crate) fn transaction(self) -> u64 {
        self.key.txn
    }

    pub(crate) fn pages(self) -> std::ops::Range<u64> {
        u64::from(self.key.first)..u64::from(self.key.first) + u64::from(self.count)
    }

    pub(crate) fn page_count(self) -> u64 {
        u64::from(self.count)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Reclamation {
    pub(crate) transactions: u64,
    pub(crate) pages: u64,
    pub(crate) through_txn: u64,
}

enum Neighbors {
    Neither,
    Previous(Extent),
    Next(Extent),
    Both(Extent, Extent),
}

struct Group {
    txn: u64,
    pages: u64,
    next: Option<Extent>,
}

struct RetirementCodec;

impl Codec for RetirementCodec {
    type Key = Key;
    type Leaf = Extent;

    const BRANCH_TYPE: u8 = BRANCH_TYPE;
    const LEAF_TYPE: u8 = LEAF_TYPE;
    const AUX: u32 = AUX;
    const KEY_SIZE: usize = KEY_SIZE;
    const LEAF_SIZE: usize = CELL_SIZE;

    fn read_key<S: ByteSource>(cell: S, _level: u16) -> Result<Self::Key> {
        decode_key(cell)
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        let extent = decode_slice(cell)?;
        if extent.key.txn <= 1 || extent.key.first < 2 || extent.count == 0 {
            return Err(Error::Corrupt("retirement extent has invalid fields"));
        }
        let end = u64::from(extent.key.first) + u64::from(extent.count);
        if end > (1u64 << 32) {
            return Err(Error::Corrupt("retirement extent endpoint overflow"));
        }
        Ok(extent)
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        encode_key(key, output);
    }
}

struct Encoded([u8; CELL_SIZE]);

impl Encoded {
    fn new(extent: Extent) -> Self {
        let mut bytes = [0; CELL_SIZE];
        encode_key(extent.key, &mut bytes);
        bytes[COUNT_OFFSET..].copy_from_slice(&extent.count.to_le_bytes());
        Self(bytes)
    }
}

fn encode_key(key: Key, output: &mut [u8]) {
    output[TXN_OFFSET..FIRST_OFFSET].copy_from_slice(&key.txn.to_le_bytes());
    output[FIRST_OFFSET..COUNT_OFFSET].copy_from_slice(&key.first.to_le_bytes());
}

pub(crate) fn add_page<S: Store>(
    store: &mut S,
    root: &mut u32,
    extent_count: &mut u64,
    txn: u64,
    page_number: u32,
) -> Result<RetiredPages> {
    if txn <= 1 {
        return Err(Error::InvalidArgument(
            "retirement transaction must be above creation",
        ));
    }
    if page_number < 2 {
        return Err(Error::Corrupt("a meta page cannot be retired"));
    }
    let key = Key {
        txn,
        first: page_number,
    };
    let previous = predecessor(store, *root, key)?;
    let next = at_or_after(store, *root, key)?;
    let neighbors = classify_neighbors(key, previous, next)?;
    apply_page(store, root, extent_count, key, neighbors)
}

fn classify_neighbors(
    key: Key,
    previous: Option<Extent>,
    next: Option<Extent>,
) -> Result<Neighbors> {
    if next.is_some_and(|extent| extent.key == key)
        || previous.is_some_and(|extent| {
            extent.key.txn == key.txn
                && u64::from(key.first) < u64::from(extent.key.first) + u64::from(extent.count)
        })
    {
        return Err(Error::Corrupt("page is already retired"));
    }

    let joins_previous = previous.is_some_and(|extent| {
        extent.key.txn == key.txn
            && u64::from(extent.key.first) + u64::from(extent.count) == u64::from(key.first)
    });
    let joins_next = next.is_some_and(|extent| {
        extent.key.txn == key.txn && key.first.checked_add(1) == Some(extent.key.first)
    });
    Ok(match (previous, next, joins_previous, joins_next) {
        (_, _, false, false) => Neighbors::Neither,
        (Some(previous), _, true, false) => Neighbors::Previous(previous),
        (_, Some(next), false, true) => Neighbors::Next(next),
        (Some(previous), Some(next), true, true) => Neighbors::Both(previous, next),
        _ => return Err(Error::Corrupt("retirement neighbor classification failed")),
    })
}

fn apply_page<S: Store>(
    store: &mut S,
    root: &mut u32,
    extent_count: &mut u64,
    key: Key,
    neighbors: Neighbors,
) -> Result<RetiredPages> {
    let mut retired = RetiredPages::new();
    match neighbors {
        Neighbors::Neither => insert_single(store, root, extent_count, key, &mut retired)?,
        Neighbors::Previous(previous) => {
            extend_previous(store, root, extent_count, previous, &mut retired)?
        }
        Neighbors::Next(next) => extend_next(store, root, extent_count, key, next, &mut retired)?,
        Neighbors::Both(previous, next) => {
            bridge(store, root, extent_count, previous, next, &mut retired)?
        }
    }
    Ok(retired)
}

fn insert_single<S: Store>(
    store: &mut S,
    root: &mut u32,
    count: &mut u64,
    key: Key,
    retired: &mut RetiredPages,
) -> Result<()> {
    insert(store, root, count, Extent { key, count: 1 }, retired)
}

fn extend_previous<S: Store>(
    store: &mut S,
    root: &mut u32,
    count: &mut u64,
    previous: Extent,
    retired: &mut RetiredPages,
) -> Result<()> {
    let extent = Extent {
        key: previous.key,
        count: grow(previous.count, 1)?,
    };
    insert(store, root, count, extent, retired)
}

fn extend_next<S: Store>(
    store: &mut S,
    root: &mut u32,
    count: &mut u64,
    key: Key,
    next: Extent,
    retired: &mut RetiredPages,
) -> Result<()> {
    remove(store, root, count, next.key, retired)?;
    let extent = Extent {
        key,
        count: grow(next.count, 1)?,
    };
    insert(store, root, count, extent, retired)
}

fn bridge<S: Store>(
    store: &mut S,
    root: &mut u32,
    count: &mut u64,
    previous: Extent,
    next: Extent,
    retired: &mut RetiredPages,
) -> Result<()> {
    remove(store, root, count, previous.key, retired)?;
    remove(store, root, count, next.key, retired)?;
    let extent = Extent {
        key: previous.key,
        count: grow(grow(previous.count, 1)?, next.count)?,
    };
    insert(store, root, count, extent, retired)
}

fn grow(count: u32, by: u32) -> Result<u32> {
    count
        .checked_add(by)
        .ok_or(Error::ArithmeticOverflow("retirement extent length"))
}

#[cfg(test)]
pub(crate) fn select_reclamation<S: Store>(
    store: &S,
    root: u32,
    selected_txn: u64,
    oldest_reader: Option<u64>,
    max_transactions: u64,
    max_pages: u64,
) -> Result<Option<Reclamation>> {
    select_reclamation_with_checkpoint(
        store,
        root,
        selected_txn,
        oldest_reader,
        max_transactions,
        max_pages,
        &mut || Ok(()),
    )
}

pub(crate) fn select_reclamation_with_checkpoint<S, F>(
    store: &S,
    root: u32,
    selected_txn: u64,
    oldest_reader: Option<u64>,
    max_transactions: u64,
    max_pages: u64,
    checkpoint: &mut F,
) -> Result<Option<Reclamation>>
where
    S: Store,
    F: FnMut() -> Result<()>,
{
    require_work_limits(max_transactions, max_pages)?;
    let mut next = first(store, root)?;
    let mut selected = Reclamation {
        transactions: 0,
        pages: 0,
        through_txn: 0,
    };

    while selected.transactions < max_transactions {
        checkpoint()?;
        let Some(extent) = next else {
            break;
        };
        let Some(group) = safe_group(store, root, extent, selected_txn, oldest_reader, checkpoint)?
        else {
            break;
        };
        require_first_group_fit(selected, group.pages, max_pages)?;
        if !append_group(&mut selected, group.txn, group.pages, max_pages)? {
            break;
        }
        next = group.next;
    }

    Ok((selected.transactions != 0).then_some(selected))
}

fn safe_group<S, F>(
    store: &S,
    root: u32,
    extent: Extent,
    selected_txn: u64,
    oldest_reader: Option<u64>,
    checkpoint: &mut F,
) -> Result<Option<Group>>
where
    S: Store,
    F: FnMut() -> Result<()>,
{
    validate_selected(store, extent, selected_txn)?;
    if !reader_safe(oldest_reader, extent.key.txn) {
        return Ok(None);
    }
    let (pages, next) = scan_group(store, root, extent, selected_txn, checkpoint)?;
    Ok(Some(Group {
        txn: extent.key.txn,
        pages,
        next,
    }))
}

fn require_first_group_fit(selected: Reclamation, group_pages: u64, max_pages: u64) -> Result<()> {
    if selected.transactions == 0 && group_pages > max_pages {
        return Err(Error::WorkLimitTooSmall {
            required_pages: group_pages,
        });
    }
    Ok(())
}

fn require_work_limits(max_transactions: u64, max_pages: u64) -> Result<()> {
    if max_transactions == 0 || max_pages == 0 {
        return Err(Error::InvalidArgument(
            "reclamation work limits must be nonzero",
        ));
    }
    Ok(())
}

fn reader_safe(oldest_reader: Option<u64>, retired_by_txn: u64) -> bool {
    match oldest_reader {
        Some(reader) => reader >= retired_by_txn,
        None => true,
    }
}

fn append_group(
    selected: &mut Reclamation,
    txn: u64,
    group_pages: u64,
    max_pages: u64,
) -> Result<bool> {
    let total = selected
        .pages
        .checked_add(group_pages)
        .ok_or(Error::ArithmeticOverflow("reclaimed page count"))?;
    if total > max_pages {
        return Ok(false);
    }
    selected.transactions += 1;
    selected.pages = total;
    selected.through_txn = txn;
    Ok(true)
}

pub(crate) fn first<S: Store>(store: &S, root: u32) -> Result<Option<Extent>> {
    at_or_after(store, root, Key { txn: 0, first: 0 })
}

pub(crate) fn remove_extent<S: Store>(
    store: &mut S,
    root: &mut u32,
    extent_count: &mut u64,
    extent: Extent,
) -> Result<RetiredPages> {
    let mut retired = RetiredPages::new();
    remove(store, root, extent_count, extent.key, &mut retired)?;
    Ok(retired)
}

fn scan_group<S, F>(
    store: &S,
    root: u32,
    first_extent: Extent,
    selected_txn: u64,
    checkpoint: &mut F,
) -> Result<(u64, Option<Extent>)>
where
    S: Store,
    F: FnMut() -> Result<()>,
{
    let txn = first_extent.key.txn;
    let mut extent = first_extent;
    let mut pages = 0u64;
    loop {
        checkpoint()?;
        validate_selected(store, extent, selected_txn)?;
        pages = pages
            .checked_add(u64::from(extent.count))
            .ok_or(Error::ArithmeticOverflow("reclaimed page count"))?;
        let next = after(store, root, extent)?;
        let Some(candidate) = next else {
            return Ok((pages, None));
        };
        if candidate.key.txn != txn {
            return Ok((pages, Some(candidate)));
        }
        let end = u64::from(extent.key.first) + u64::from(extent.count);
        if u64::from(candidate.key.first) <= end {
            return Err(Error::Corrupt(
                "retirement extents overlap or are not coalesced",
            ));
        }
        extent = candidate;
    }
}

fn validate_selected<S: Store>(store: &S, extent: Extent, selected_txn: u64) -> Result<()> {
    if extent.key.txn > selected_txn || extent.pages().end > store.page_limit() {
        return Err(Error::Corrupt(
            "retirement extent exceeds the selected generation",
        ));
    }
    Ok(())
}

fn after<S: Store>(store: &S, root: u32, extent: Extent) -> Result<Option<Extent>> {
    let key = if let Some(first) = extent.key.first.checked_add(1) {
        Key {
            txn: extent.key.txn,
            first,
        }
    } else if let Some(txn) = extent.key.txn.checked_add(1) {
        Key { txn, first: 0 }
    } else {
        return Ok(None);
    };
    at_or_after(store, root, key)
}

fn insert<S: Store>(
    store: &mut S,
    root: &mut u32,
    extent_count: &mut u64,
    extent: Extent,
    retired: &mut RetiredPages,
) -> Result<()> {
    let inserted =
        fixed_tree::insert::<RetirementCodec, S>(store, root, &Encoded::new(extent).0, retired)?;
    if inserted {
        *extent_count = extent_count
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("retirement extent count"))?;
    }
    Ok(())
}

fn remove<S: Store>(
    store: &mut S,
    root: &mut u32,
    extent_count: &mut u64,
    key: Key,
    retired: &mut RetiredPages,
) -> Result<()> {
    let mut removed = RetiredPages::new();
    fixed_tree::delete_existing::<RetirementCodec, S>(store, root, key, &mut removed)?;
    retired.extend(removed.as_slice())?;
    *extent_count = extent_count
        .checked_sub(1)
        .ok_or(Error::ArithmeticOverflow("retirement extent count"))?;
    Ok(())
}

fn predecessor<S: Store>(store: &S, root: u32, key: Key) -> Result<Option<Extent>> {
    fixed_tree::predecessor::<RetirementCodec, S>(store, root, key)
}

fn at_or_after<S: Store>(store: &S, root: u32, key: Key) -> Result<Option<Extent>> {
    fixed_tree::at_or_after::<RetirementCodec, S>(store, root, key)
}

fn decode_slice<S: ByteSource>(cell: S) -> Result<Extent> {
    decode_raw(cell).ok_or(Error::Corrupt("retirement leaf has the wrong record size"))
}

pub(crate) fn decode_key<S: ByteSource>(cell: S) -> Result<Key> {
    decode_key_raw(cell).ok_or(Error::Corrupt("retirement key is truncated"))
}

pub(crate) fn decode_branch_child<S: ByteSource>(cell: S) -> Option<u32> {
    (cell.len() == CELL_SIZE).then(|| u32_le(cell, KEY_SIZE))
}

pub(crate) fn decode_raw<S: ByteSource>(cell: S) -> Option<Extent> {
    if cell.len() != CELL_SIZE {
        return None;
    }
    Some(Extent {
        key: decode_key_raw(cell)?,
        count: u32_le(cell, COUNT_OFFSET),
    })
}

fn decode_key_raw<S: ByteSource>(cell: S) -> Option<Key> {
    (cell.len() >= KEY_SIZE).then(|| Key {
        txn: u64_le(cell, TXN_OFFSET),
        first: u32_le(cell, FIRST_OFFSET),
    })
}

#[cfg(test)]
#[path = "retirement_tests.rs"]
mod tests;
