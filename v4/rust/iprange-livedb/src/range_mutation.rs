//! Range semantics over the generic COW tree.

use crate::error::{Error, Result};
use crate::fixed_tree::{self, RetiredPages, RetiringStore, Store};
use crate::key::IpKey;
use crate::mapping::ByteSource;
use crate::range_tree::{self, RangeCodec, Record as Range};

mod assign;
mod coverage;
mod locator;

pub(crate) use assign::{
    assign, assign_private, assign_private_input, clear, retire_tree, transform,
};
pub(crate) use coverage::{finish_input_untracked, push_private_untracked, UnionInput};
#[cfg(test)]
use coverage::{finish_private, union_private, UnionState};
pub(crate) use locator::AssignmentInput;
use locator::{insert_private_input_gap, PrivateInputInsert, UnionAssignmentInput};

pub(crate) trait RangeStore: RetiringStore {
    fn range_record_added(&mut self, value: u32) -> Result<()>;
    fn range_record_removed(&mut self, value: u32) -> Result<()>;
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

    #[inline(always)]
    fn as_slice(&self) -> &[u8] {
        &self.bytes[..self.len]
    }
}

struct PrivateGap<K> {
    range: Range<K>,
}

impl<K: IpKey> fixed_tree::LocalGap<RangeCodec<K>> for PrivateGap<K> {
    type Reject = Range<K>;

    fn previous<B: ByteSource>(
        &mut self,
        exact: bool,
        cell: Option<B>,
    ) -> Result<fixed_tree::LocalPrevious<Self::Reject>> {
        if let Some(cell) = cell {
            let previous = range_tree::decode_cell::<K, _>(cell)?;
            if exact
                || previous.to >= self.range.from
                || (previous.value == self.range.value
                    && previous.to.checked_next() == Some(self.range.from))
            {
                return Ok(fixed_tree::LocalPrevious::Reject(previous));
            }
        }
        Ok(fixed_tree::LocalPrevious::Accept)
    }

    fn next<B: ByteSource>(
        &mut self,
        cell: Option<B>,
    ) -> Result<fixed_tree::LocalNext<Self::Reject>> {
        let Some(cell) = cell else {
            return Ok(fixed_tree::LocalNext::Accept);
        };
        let next = range_tree::decode_cell::<K, _>(cell)?;
        if next.from > self.range.to
            && (next.value != self.range.value || self.range.to.checked_next() != Some(next.from))
        {
            Ok(fixed_tree::LocalNext::Accept)
        } else {
            Ok(fixed_tree::LocalNext::Reject(next))
        }
    }
}

#[inline]
fn insert_private_gap<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: Range<K>,
) -> Result<fixed_tree::LocalInsert<Range<K>>> {
    let encoded = EncodedRange::new(range)?;
    let mut retired = RetiredPages::new();
    let mut gap = PrivateGap { range };
    let result = fixed_tree::insert_if_local_gap::<RangeCodec<K>, S, _>(
        store,
        root,
        encoded.as_slice(),
        &mut retired,
        &mut gap,
    )?;
    if matches!(result, fixed_tree::LocalInsert::Inserted(_)) {
        if !retired.as_slice().is_empty() {
            return Err(Error::Corrupt("private range insertion retired a page"));
        }
        account_private_insert(store, record_count, range.value)?;
    }
    Ok(result)
}

#[inline]
fn account_private_insert<S: RangeStore>(
    store: &mut S,
    record_count: &mut u64,
    value: u32,
) -> Result<()> {
    *record_count = record_count
        .checked_add(1)
        .ok_or_else(|| Error::arithmetic_overflow("range record count"))?;
    store.range_record_added(value)?;
    crate::work::range_emitted(1);
    Ok(())
}

fn insert_private_rejected<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: Range<K>,
    rejected: fixed_tree::LocalReject<Range<K>>,
) -> Result<Option<fixed_tree::PrivatePosition>> {
    let encoded = EncodedRange::new(range)?;
    let position = fixed_tree::insert_rejected_gap::<RangeCodec<K>, S, _>(
        store,
        root,
        encoded.as_slice(),
        rejected,
    )?;
    account_private_insert(store, record_count, range.value)?;
    Ok(position)
}

fn insert<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: Range<K>,
) -> Result<()> {
    let encoded = EncodedRange::new(range)?;
    let inserted =
        fixed_tree::insert_retiring::<RangeCodec<K>, S>(store, root, encoded.as_slice())?;
    if inserted {
        *record_count = record_count
            .checked_add(1)
            .ok_or_else(|| Error::arithmetic_overflow("range record count"))?;
        store.range_record_added(range.value)?;
        crate::work::range_emitted(1);
    }
    Ok(())
}

fn read_predecessor<K: IpKey, S: Store>(store: &S, root: u32, key: K) -> Result<Option<Range<K>>> {
    fixed_tree::predecessor::<RangeCodec<K>, S>(store, root, key)
}

fn read_at_or_after<K: IpKey, S: Store>(store: &S, root: u32, key: K) -> Result<Option<Range<K>>> {
    fixed_tree::at_or_after::<RangeCodec<K>, S>(store, root, key)
}

#[cfg(test)]
fn decode_cell<K: IpKey>(cell: &[u8]) -> Result<Range<K>> {
    range_tree::decode_cell(cell)
}

#[cfg(test)]
#[path = "range_mutation_tests.rs"]
mod tests;
