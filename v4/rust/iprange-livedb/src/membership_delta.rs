//! Operation-private aggregation of dictionary refcount changes.

use crate::contract::{u32_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::{self, Codec, CursorDirection, CursorItem, RetiredPages, Store};
use crate::mapping::ByteSource;
use crate::slotted_page::Header;

const BRANCH_TYPE: u8 = 250;
const LEAF_TYPE: u8 = 251;
const AUX: u32 = 0x4d44_454c;
const ID_OFFSET: usize = 0;
const CHANGE_OFFSET: usize = 4;
const RECORD_SIZE: usize = 12;
const PENDING_SLOTS: usize = 2;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Delta {
    pub(crate) id: u32,
    pub(crate) change: i64,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct Pending {
    slots: [Option<Delta>; PENDING_SLOTS],
}

impl Pending {
    pub(crate) const fn new() -> Self {
        Self {
            slots: [None; PENDING_SLOTS],
        }
    }

    pub(crate) fn is_empty(self) -> bool {
        self.slots.iter().all(Option::is_none)
    }
}

struct DeltaCodec;

pub(crate) struct Drain {
    cursor: fixed_tree::Cursor<DeltaCodec>,
}

struct DeltaItem;

impl Codec for DeltaCodec {
    type Key = u32;
    type Leaf = Delta;

    const BRANCH_TYPE: u8 = BRANCH_TYPE;
    const LEAF_TYPE: u8 = LEAF_TYPE;
    const AUX: u32 = AUX;
    const KEY_SIZE: usize = CHANGE_OFFSET;
    const LEAF_SIZE: usize = RECORD_SIZE;

    fn read_key<S: ByteSource>(cell: S, _level: u16) -> Result<Self::Key> {
        Ok(u32_le(cell, ID_OFFSET))
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        decode(cell)
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[ID_OFFSET..CHANGE_OFFSET].copy_from_slice(&key.to_le_bytes());
    }
}

#[inline(always)]
pub(crate) fn track_buffered<S: Store>(
    store: &mut S,
    root: &mut u32,
    pending: &mut Pending,
    id: u32,
    change: i64,
) -> Result<()> {
    if id == 0 {
        return Ok(());
    }
    for slot in &mut pending.slots {
        if let Some(mut current) = *slot {
            if current.id != id {
                continue;
            }
            current.change = current
                .change
                .checked_add(change)
                .ok_or_else(|| Error::arithmetic_overflow("dictionary refcount delta"))?;
            *slot = Some(current);
            return Ok(());
        }
    }
    if let Some(slot) = pending.slots.iter_mut().find(|slot| slot.is_none()) {
        *slot = Some(Delta { id, change });
        return Ok(());
    }

    let oldest =
        pending.slots[0].ok_or_else(|| Error::corrupt("refcount delta pending slot is empty"))?;
    track(store, root, oldest.id, oldest.change)?;
    pending.slots[0] = pending.slots[1];
    pending.slots[1] = Some(Delta { id, change });
    Ok(())
}

pub(crate) fn flush<S: Store>(store: &mut S, root: &mut u32, pending: &mut Pending) -> Result<()> {
    for slot in &mut pending.slots {
        let Some(delta) = *slot else {
            continue;
        };
        track(store, root, delta.id, delta.change)?;
        *slot = None;
    }
    Ok(())
}

fn track<S: Store>(store: &mut S, root: &mut u32, id: u32, change: i64) -> Result<()> {
    crate::work::membership_delta_spill(1);
    let current = find(store, *root, id)?;
    if let Some(current) = current {
        if change == 0 {
            return Ok(());
        }
        let change = current
            .change
            .checked_add(change)
            .ok_or_else(|| Error::arithmetic_overflow("dictionary refcount delta"))?;
        let mut retired = RetiredPages::new();
        fixed_tree::mutate_leaf_u64::<DeltaCodec, S, _>(
            store,
            root,
            id,
            CHANGE_OFFSET,
            &mut retired,
            |_| Ok(fixed_tree::LeafU64Mutation::Replace(change as u64)),
        )?;
        require_private(retired)
    } else {
        insert(store, root, Delta { id, change })
    }
}

fn find<S: Store>(store: &S, root: u32, id: u32) -> Result<Option<Delta>> {
    if root == 0 {
        return Ok(None);
    }
    let found = fixed_tree::predecessor::<DeltaCodec, S>(store, root, id)?;
    match found {
        Some(found) if found.id == id => Ok(Some(found)),
        _ => Ok(None),
    }
}

fn insert<S: Store>(store: &mut S, root: &mut u32, delta: Delta) -> Result<()> {
    let mut retired = RetiredPages::new();
    if !fixed_tree::insert::<DeltaCodec, S>(store, root, &delta.encode(), &mut retired)? {
        return Err(Error::Corrupt("refcount delta key already exists"));
    }
    require_private(retired)
}

fn require_private(retired: RetiredPages) -> Result<()> {
    if retired.as_slice().is_empty() {
        Ok(())
    } else {
        Err(Error::Corrupt(
            "refcount delta tree contains a committed page",
        ))
    }
}

impl Drain {
    pub(crate) fn new<S: Store>(store: &mut S, root: u32) -> Result<Self> {
        Ok(Self {
            cursor: fixed_tree::Cursor::new_consuming(store, root, CursorDirection::Forward)?,
        })
    }

    pub(crate) fn next<S: Store>(&mut self, store: &mut S) -> Result<Option<Delta>> {
        self.cursor.next_consuming(store, &mut DeltaItem)
    }
}

impl CursorItem<DeltaCodec> for DeltaItem {
    type Output = Delta;

    fn read<S: ByteSource>(
        &mut self,
        page: S,
        header: &Header,
        _page_number: u32,
        index: usize,
    ) -> Result<Self::Output> {
        DeltaCodec::read_leaf(DeltaCodec::leaf_cell(page, header, index)?)
    }
}

fn decode<S: ByteSource>(cell: S) -> Result<Delta> {
    if cell.len() != RECORD_SIZE {
        return Err(Error::Corrupt("refcount delta record is malformed"));
    }
    let bytes = cell
        .array(CHANGE_OFFSET)
        .ok_or_else(|| Error::corrupt("refcount delta record is malformed"))?;
    Ok(Delta {
        id: u32_le(cell, ID_OFFSET),
        change: i64::from_le_bytes(bytes),
    })
}

impl Delta {
    fn encode(self) -> [u8; RECORD_SIZE] {
        let mut record = [0; RECORD_SIZE];
        record[ID_OFFSET..CHANGE_OFFSET].copy_from_slice(&self.id.to_le_bytes());
        record[CHANGE_OFFSET..].copy_from_slice(&self.change.to_le_bytes());
        record
    }
}

const _: () = assert!(DeltaCodec::LEAF_SIZE <= PAGE_SIZE);

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn signed_delta_encoding_round_trips() {
        for change in [i64::MIN, -7, -1, 0, 1, 7, i64::MAX] {
            let delta = Delta { id: 42, change };
            assert_eq!(decode(&delta.encode()).unwrap(), delta);
        }
    }
}
