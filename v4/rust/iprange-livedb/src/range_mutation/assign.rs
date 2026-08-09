//! Direct range assignment and transformation semantics.

use crate::error::{Error, Result};
use crate::fixed_tree::{self, RetiredPages, Store};
use crate::key::IpKey;
use crate::range_tree::{RangeCodec, Record as Range};

use super::{
    insert, insert_private_gap, read_at_or_after, read_predecessor, EncodedRange, RangeStore,
};

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
    match insert_private_gap(store, root, record_count, range)? {
        fixed_tree::LocalInsert::Inserted => Ok(true),
        fixed_tree::LocalInsert::General(rejected) => {
            assign_with_hint(store, root, record_count, range, rejected)
        }
    }
}

pub(super) fn assign_with_hint<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: Range<K>,
    hint: fixed_tree::LocalReject<Range<K>>,
) -> Result<bool> {
    replace_with_hint(
        store,
        root,
        record_count,
        Change {
            from: range.from,
            to: range.to,
            value: Some(range.value),
        },
        Some(hint),
    )
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
    replace_with_hint(store, root, record_count, Change { from, to, value }, None)
}

#[derive(Clone, Copy)]
struct Change<K> {
    from: K,
    to: K,
    value: Option<u32>,
}

fn replace_with_hint<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    change: Change<K>,
    hint: Option<fixed_tree::LocalReject<Range<K>>>,
) -> Result<bool> {
    let Change { from, to, value } = change;
    if from > to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    let (predecessor, hint) = match hint {
        Some(hint) => match hint.predecessor().copied() {
            Some(predecessor) => (Some(predecessor), Some(hint)),
            None => (read_predecessor::<K, S>(store, *root, from)?, None),
        },
        None => (read_predecessor::<K, S>(store, *root, from)?, None),
    };
    if value.is_some_and(|new_value| {
        predecessor.is_some_and(|old| old.to >= to && old.value == new_value)
    }) {
        return Ok(false);
    }
    if let Some(old) = predecessor.filter(|old| old.from < from && old.to > to) {
        return replace_strictly_inside(store, root, record_count, old, change, hint);
    }
    let mut rewrite = trim_predecessor(store, root, record_count, predecessor, from, to)?;
    trim_following(store, root, record_count, from, to, &mut rewrite)?;
    write_replacement(store, root, record_count, from, to, value, rewrite)
}

fn replace_strictly_inside<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    old: Range<K>,
    change: Change<K>,
    hint: Option<fixed_tree::LocalReject<Range<K>>>,
) -> Result<bool> {
    let Change { from, to, value } = change;
    let left = EncodedRange::new(Range {
        from: old.from,
        to: from.checked_previous().expect("from is above old.from"),
        value: old.value,
    })?;
    let right = EncodedRange::new(Range {
        from: to.checked_next().expect("to is below old.to"),
        to: old.to,
        value: old.value,
    })?;
    let middle = value
        .map(|value| EncodedRange::new(Range { from, to, value }))
        .transpose()?;
    let mut retired = RetiredPages::new();
    if let Some(middle) = middle.as_ref() {
        let cells = [left.as_slice(), middle.as_slice(), right.as_slice()];
        replace_strict_cells::<K, S>(store, root, old.from, &cells, hint, &mut retired)?;
    } else {
        let cells = [left.as_slice(), right.as_slice()];
        replace_strict_cells::<K, S>(store, root, old.from, &cells, hint, &mut retired)?;
    }
    store.retire_pages(retired.as_slice())?;
    *record_count = record_count
        .checked_add(if middle.is_some() { 2 } else { 1 })
        .ok_or_else(|| Error::arithmetic_overflow("range record count"))?;
    store.range_record_added(value.unwrap_or(old.value))?;
    if middle.is_some() {
        store.range_record_added(old.value)?;
    }
    crate::work::range_emitted(if middle.is_some() { 3 } else { 2 });
    crate::work::range_split(1);
    Ok(true)
}

fn replace_strict_cells<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    old_key: K,
    cells: &[&[u8]],
    hint: Option<fixed_tree::LocalReject<Range<K>>>,
    retired: &mut RetiredPages,
) -> Result<()> {
    if let Some(hint) = hint {
        fixed_tree::replace_local_predecessor_with::<RangeCodec<K>, S, _>(
            store, root, hint, old_key, cells,
        )
    } else {
        fixed_tree::replace_leaf_with::<RangeCodec<K>, S>(store, root, old_key, cells, retired)
    }
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

fn remove<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: Range<K>,
) -> Result<()> {
    fixed_tree::delete_retiring::<RangeCodec<K>, S>(store, root, range.from)?;
    *record_count = record_count
        .checked_sub(1)
        .ok_or_else(|| Error::arithmetic_overflow("range record count"))?;
    store.range_record_removed(range.value)
}
