//! Sequential direct-range assignment over the generic COW tree.

use crate::error::{Error, Result};
use crate::fixed_tree::{self, LeafBuf, RetiredPages, RetiringStore, Store};
use crate::key::IpKey;
use crate::range_tree::{self, RangeCodec, Record as Range};

pub(crate) trait RangeStore: RetiringStore {
    fn range_record_added(&mut self, value: u32) -> Result<()>;
    fn range_record_removed(&mut self, value: u32) -> Result<()>;
}

struct Untracked<'a, S>(&'a mut S);

impl<S: RetiringStore> Store for Untracked<'_, S> {
    type ReadPage<'a>
        = S::ReadPage<'a>
    where
        Self: 'a;
    type WritePage<'a>
        = S::WritePage<'a>
    where
        Self: 'a;

    fn target_txn(&self) -> u64 {
        self.0.target_txn()
    }

    fn page_limit(&self) -> u64 {
        self.0.page_limit()
    }

    fn inspect_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>) -> Result<T>,
    {
        self.0.inspect_page(page_number, inspect)
    }

    fn allocate(&mut self) -> Result<u32> {
        self.0.allocate()
    }

    fn update_page<'a, T, F>(&'a mut self, page_number: u32, update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>,
    {
        self.0.update_page(page_number, update)
    }

    fn copy_page<'a, T, F>(&'a mut self, source: u32, destination: u32, copy: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>, &mut Self::WritePage<'a>) -> Result<T>,
    {
        self.0.copy_page(source, destination, copy)
    }

    fn discard_private(&mut self, page_number: u32) -> Result<()> {
        self.0.discard_private(page_number)
    }
}

impl<S: RetiringStore> RetiringStore for Untracked<'_, S> {
    fn retire_pages(&mut self, pages: &[u32]) -> Result<()> {
        self.0.retire_pages(pages)
    }
}

impl<S: RetiringStore> RangeStore for Untracked<'_, S> {
    fn range_record_added(&mut self, _value: u32) -> Result<()> {
        Ok(())
    }

    fn range_record_removed(&mut self, _value: u32) -> Result<()> {
        Ok(())
    }
}

struct EncodedRange {
    bytes: [u8; range_tree::MAX_RECORD_SIZE],
    len: usize,
}

impl EncodedRange {
    fn new<K: IpKey>(range: Range<K>) -> Result<Self> {
        let mut encoded = Self {
            bytes: [0; range_tree::MAX_RECORD_SIZE],
            len: 0,
        };
        encoded.len = range_tree::encode_record(range, &mut encoded.bytes)?;
        Ok(encoded)
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

pub(crate) fn assign_private<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    from: K,
    to: K,
    value: u32,
) -> Result<bool> {
    if from > to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    let range = Range { from, to, value };
    let encoded = EncodedRange::new(range)?;
    let mut retired = RetiredPages::new();
    match fixed_tree::insert_if_local_gap::<RangeCodec<K>, S, _>(
        store,
        root,
        encoded.as_slice(),
        &mut retired,
        |previous, next| local_gap_accepts::<K>(previous, next, range),
    )? {
        fixed_tree::LocalInsert::Inserted => {
            if !retired.as_slice().is_empty() {
                return Err(Error::Corrupt("private range insertion retired a page"));
            }
            *record_count = record_count
                .checked_add(1)
                .ok_or_else(|| Error::arithmetic_overflow("range record count"))?;
            store.range_record_added(value)?;
            crate::work::range_emitted(1);
            Ok(true)
        }
        fixed_tree::LocalInsert::General => {
            replace(store, root, record_count, from, to, Some(value))
        }
    }
}

pub(crate) fn assign_private_untracked<K: IpKey, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    from: K,
    to: K,
    value: u32,
) -> Result<bool> {
    assign_private(&mut Untracked(store), root, record_count, from, to, value)
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
            .ok_or_else(|| Error::arithmetic_overflow("membership range cursor"))?;
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
            .ok_or_else(|| Error::corrupt("range gap does not advance"))?,
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
    if rewrite.left.is_some() && rewrite.right.is_some() {
        crate::work::range_split(1);
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
        crate::work::range_coalesced(1);
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
        crate::work::range_coalesced(1);
    }
    Ok(())
}

fn insert<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: Range<K>,
) -> Result<()> {
    let encoded = EncodedRange::new(range)?;
    let mut retired = RetiredPages::new();
    let inserted =
        fixed_tree::insert::<RangeCodec<K>, S>(store, root, encoded.as_slice(), &mut retired)?;
    store.retire_pages(retired.as_slice())?;
    if inserted {
        *record_count = record_count
            .checked_add(1)
            .ok_or_else(|| Error::arithmetic_overflow("range record count"))?;
        store.range_record_added(range.value)?;
        crate::work::range_emitted(1);
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
        .ok_or_else(|| Error::arithmetic_overflow("range record count"))?;
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

fn local_gap_accepts<K: IpKey>(
    previous: Option<&[u8]>,
    next: Option<&[u8]>,
    range: Range<K>,
) -> Result<bool> {
    if let Some(previous) = previous {
        let previous = decode_cell::<K>(previous)?;
        if previous.to >= range.from
            || (previous.value == range.value && previous.to.checked_next() == Some(range.from))
        {
            return Ok(false);
        }
    }
    if let Some(next) = next {
        let next = decode_cell::<K>(next)?;
        if next.from <= range.to
            || (next.value == range.value && range.to.checked_next() == Some(next.from))
        {
            return Ok(false);
        }
    }
    Ok(true)
}

fn decode<K: IpKey>(cell: LeafBuf) -> Result<Range<K>> {
    decode_cell(cell.as_slice())
}

fn decode_cell<K: IpKey>(cell: &[u8]) -> Result<Range<K>> {
    range_tree::decode_cell(cell)
}

#[cfg(test)]
#[path = "range_mutation_tests.rs"]
mod tests;
