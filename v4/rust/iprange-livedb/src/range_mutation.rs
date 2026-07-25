//! Sequential direct-range assignment over the generic COW tree.

use std::marker::PhantomData;

use crate::contract::{u32_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::{self, Codec, LeafBuf, RetiredPages, RetiringStore, Store};
use crate::key::IpKey;

const MAX_RANGE_CELL: usize = 36;

pub(crate) trait RangeStore: RetiringStore {
    fn range_record_added(&mut self, value: u32) -> Result<()>;
    fn range_record_removed(&mut self, value: u32) -> Result<()>;
}

struct RangeCodec<K>(PhantomData<K>);

impl<K: IpKey> Codec for RangeCodec<K> {
    type Key = K;

    const BRANCH_TYPE: u8 = 1;
    const LEAF_TYPE: u8 = 2;
    const AUX: u32 = K::FAMILY as u32;
    const KEY_SIZE: usize = K::WIDTH;
    const LEAF_SIZE: usize = K::WIDTH * 2 + 4;

    fn read_key(cell: &[u8], _level: u16) -> Result<Self::Key> {
        Ok(K::read_le(cell))
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        key.write_le(output);
    }

    fn validate_leaf(cell: &[u8]) -> Result<()> {
        if cell.len() != Self::LEAF_SIZE {
            return Err(Error::Corrupt("range leaf has the wrong record size"));
        }
        if K::read_le(cell) > K::read_le(&cell[K::WIDTH..]) {
            return Err(Error::Corrupt("range leaf has reversed endpoints"));
        }
        Ok(())
    }
}

#[derive(Clone, Copy)]
struct Range<K> {
    from: K,
    to: K,
    value: u32,
}

struct EncodedRange {
    bytes: [u8; MAX_RANGE_CELL],
    len: usize,
}

impl EncodedRange {
    fn new<K: IpKey>(range: Range<K>) -> Self {
        let len = K::WIDTH * 2 + 4;
        let mut encoded = Self {
            bytes: [0; MAX_RANGE_CELL],
            len,
        };
        range.from.write_le(&mut encoded.bytes);
        range
            .to
            .write_le(&mut encoded.bytes[K::WIDTH..K::WIDTH * 2]);
        encoded.bytes[K::WIDTH * 2..len].copy_from_slice(&range.value.to_le_bytes());
        encoded
    }

    fn as_slice(&self) -> &[u8] {
        &self.bytes[..self.len]
    }
}

pub(crate) fn assign<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    from: K,
    to: K,
    value: u32,
) -> Result<bool> {
    replace(store, root, record_count, from, to, Some(value))
}

pub(crate) fn clear<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    from: K,
    to: K,
) -> Result<bool> {
    replace(store, root, record_count, from, to, None)
}

pub(crate) fn retire_tree<K, S, F>(store: &mut S, root: u32, checkpoint: F) -> Result<()>
where
    K: IpKey,
    S: RangeStore,
    F: FnMut() -> Result<()>,
{
    fixed_tree::retire_tree::<RangeCodec<K>, S, F>(store, root, checkpoint)
}

pub(crate) fn transform<K, S, F>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    from: K,
    to: K,
    mut operation: F,
) -> Result<bool>
where
    K: IpKey,
    S: RangeStore,
    F: FnMut(&mut S, Option<u32>) -> Result<Option<u32>>,
{
    if from > to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    let mut cursor = from;
    let mut changed = false;
    loop {
        let segment = segment_at::<K, S>(store, *root, cursor, to)?;
        let value = operation(store, segment.value)?;
        if value != segment.value {
            replace(store, root, record_count, cursor, segment.to, value)?;
            changed = true;
        }
        if segment.to == to {
            return Ok(changed);
        }
        cursor = segment
            .to
            .checked_next()
            .ok_or(Error::ArithmeticOverflow("membership range cursor"))?;
    }
}

struct Segment<K> {
    to: K,
    value: Option<u32>,
}

fn segment_at<K: IpKey, S: Store>(store: &S, root: u32, from: K, to: K) -> Result<Segment<K>> {
    if let Some(range) = read_predecessor::<K, S>(store, root, from)? {
        if range.to >= from {
            return Ok(Segment {
                to: range.to.min(to),
                value: Some(range.value),
            });
        }
    }
    let end = match read_at_or_after::<K, S>(store, root, from)? {
        Some(next) if next.from <= to => next
            .from
            .checked_previous()
            .ok_or(Error::Corrupt("range gap does not advance"))?,
        _ => to,
    };
    Ok(Segment {
        to: end,
        value: None,
    })
}

fn replace<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    from: K,
    to: K,
    value: Option<u32>,
) -> Result<bool> {
    if from > to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    let predecessor = read_predecessor::<K, S>(store, *root, from)?;
    if value.is_some_and(|new_value| {
        predecessor.is_some_and(|old| old.to >= to && old.value == new_value)
    }) {
        return Ok(false);
    }
    let mut rewrite = trim_predecessor(store, root, record_count, predecessor, from, to)?;
    trim_following(store, root, record_count, from, to, &mut rewrite)?;
    write_replacement(store, root, record_count, from, to, value, rewrite)
}

struct Rewrite<K> {
    left: Option<Range<K>>,
    right: Option<Range<K>>,
    changed: bool,
}

fn trim_predecessor<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    predecessor: Option<Range<K>>,
    from: K,
    to: K,
) -> Result<Rewrite<K>> {
    let mut rewrite = Rewrite {
        left: None,
        right: None,
        changed: false,
    };
    let Some(old) = predecessor.filter(|old| old.to >= from) else {
        return Ok(rewrite);
    };
    remove::<K, S>(store, root, record_count, old)?;
    rewrite.changed = true;
    if old.from < from {
        rewrite.left = Some(Range {
            from: old.from,
            to: from.checked_previous().expect("from is above old.from"),
            value: old.value,
        });
    }
    if old.to > to {
        rewrite.right = Some(Range {
            from: to.checked_next().expect("to is below old.to"),
            to: old.to,
            value: old.value,
        });
    }
    Ok(rewrite)
}

fn trim_following<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    from: K,
    to: K,
    rewrite: &mut Rewrite<K>,
) -> Result<()> {
    loop {
        let Some(old) = read_at_or_after::<K, S>(store, *root, from)? else {
            return Ok(());
        };
        if old.from > to {
            return Ok(());
        }
        rewrite.changed = true;
        remove::<K, S>(store, root, record_count, old)?;
        if old.to > to {
            rewrite.right = Some(Range {
                from: to.checked_next().expect("to is below old.to"),
                to: old.to,
                value: old.value,
            });
            return Ok(());
        }
    }
}

fn write_replacement<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    from: K,
    to: K,
    value: Option<u32>,
    rewrite: Rewrite<K>,
) -> Result<bool> {
    if let Some(left) = rewrite.left {
        insert_coalesced::<K, S>(store, root, record_count, left)?;
    }
    if let Some(value) = value {
        insert_coalesced::<K, S>(store, root, record_count, Range { from, to, value })?;
    }
    if let Some(right) = rewrite.right {
        insert_coalesced::<K, S>(store, root, record_count, right)?;
    }
    Ok(rewrite.changed || value.is_some())
}

fn insert_coalesced<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    mut range: Range<K>,
) -> Result<()> {
    merge_previous(store, root, record_count, &mut range)?;
    merge_next(store, root, record_count, &mut range)?;
    insert::<K, S>(store, root, record_count, range)
}

fn merge_previous<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: &mut Range<K>,
) -> Result<()> {
    let Some(previous) = read_predecessor::<K, S>(store, *root, range.from)? else {
        return Ok(());
    };
    if previous.value == range.value && previous.to.checked_next() == Some(range.from) {
        remove::<K, S>(store, root, record_count, previous)?;
        range.from = previous.from;
    }
    Ok(())
}

fn merge_next<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: &mut Range<K>,
) -> Result<()> {
    let Some(next) = read_at_or_after::<K, S>(store, *root, range.from)? else {
        return Ok(());
    };
    if next.value == range.value && range.to.checked_next() == Some(next.from) {
        remove::<K, S>(store, root, record_count, next)?;
        range.to = next.to;
    }
    Ok(())
}

fn insert<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: Range<K>,
) -> Result<()> {
    let encoded = EncodedRange::new(range);
    let mut retired = RetiredPages::new();
    let inserted =
        fixed_tree::insert::<RangeCodec<K>, S>(store, root, encoded.as_slice(), &mut retired)?;
    store.retire_pages(retired.as_slice())?;
    if inserted {
        *record_count = record_count
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("range record count"))?;
        store.range_record_added(range.value)?;
    }
    Ok(())
}

fn remove<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: Range<K>,
) -> Result<()> {
    let mut retired = RetiredPages::new();
    let deleted = fixed_tree::delete::<RangeCodec<K>, S>(store, root, range.from, &mut retired)?;
    store.retire_pages(retired.as_slice())?;
    if !deleted {
        return Err(Error::Corrupt("range disappeared during mutation"));
    }
    *record_count = record_count
        .checked_sub(1)
        .ok_or(Error::ArithmeticOverflow("range record count"))?;
    store.range_record_removed(range.value)
}

fn read_predecessor<K: IpKey, S: Store>(store: &S, root: u32, key: K) -> Result<Option<Range<K>>> {
    fixed_tree::predecessor::<RangeCodec<K>, S>(store, root, key)?
        .map(decode::<K>)
        .transpose()
}

fn read_at_or_after<K: IpKey, S: Store>(store: &S, root: u32, key: K) -> Result<Option<Range<K>>> {
    fixed_tree::at_or_after::<RangeCodec<K>, S>(store, root, key)?
        .map(decode::<K>)
        .transpose()
}

fn decode<K: IpKey>(cell: LeafBuf) -> Result<Range<K>> {
    let cell = cell.as_slice();
    RangeCodec::<K>::validate_leaf(cell)?;
    Ok(Range {
        from: K::read_le(cell),
        to: K::read_le(&cell[K::WIDTH..]),
        value: u32_le(cell, K::WIDTH * 2),
    })
}

const _: () = assert!(MAX_RANGE_CELL <= PAGE_SIZE);

#[cfg(test)]
#[path = "range_mutation_tests.rs"]
mod tests;
