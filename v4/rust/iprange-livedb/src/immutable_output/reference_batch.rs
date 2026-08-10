//! Fixed-memory aggregation of recurring output membership references.

use std::mem::size_of;

use crate::error::{Error, Result};
use crate::heap::Heap;
use crate::membership_delta::Delta;

const ENTRY_LIMIT: usize = 1024;

#[derive(Clone, Copy, Debug, Default)]
struct Slot {
    id: u32,
    count: i64,
}

#[derive(Default)]
pub(super) struct ReferenceBatch {
    slots: Vec<Slot>,
    entries: usize,
}

impl std::fmt::Debug for ReferenceBatch {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output
            .debug_struct("ReferenceBatch")
            .field("entries", &self.entries)
            .field("capacity", &(self.slots.len() / 2))
            .finish()
    }
}

pub(super) enum Add {
    Added,
    Full,
    Direct,
}

impl ReferenceBatch {
    pub(super) fn new(enabled: bool, heap: &mut Heap) -> Result<Self> {
        if !enabled {
            return Ok(Self::default());
        }
        let possible_entries = usize::try_from(heap.remaining())
            .unwrap_or(usize::MAX)
            .checked_div(2 * size_of::<Slot>())
            .unwrap_or(0)
            .min(ENTRY_LIMIT);
        let entries = floor_power_of_two(possible_entries);
        if entries == 0 {
            return Ok(Self::default());
        }
        let slots = heap.filled(
            entries * 2,
            Slot::default(),
            "immutable membership reference batch",
        )?;
        Ok(Self { slots, entries: 0 })
    }

    pub(super) fn add(&mut self, id: u32) -> Result<Add> {
        if id == 0 {
            return Err(Error::Corrupt("membership reference ID is zero"));
        }
        if self.slots.is_empty() {
            return Ok(Add::Direct);
        }
        let entry_limit = self.slots.len() / 2;
        let mut index = hash(id) & (self.slots.len() - 1);
        for _ in 0..self.slots.len() {
            let slot = &mut self.slots[index];
            if slot.id == id {
                slot.count = slot
                    .count
                    .checked_add(1)
                    .ok_or(Error::ArithmeticOverflow("membership reference count"))?;
                return Ok(Add::Added);
            }
            if slot.id == 0 {
                if self.entries == entry_limit {
                    return Ok(Add::Full);
                }
                *slot = Slot { id, count: 1 };
                self.entries += 1;
                return Ok(Add::Added);
            }
            index = (index + 1) & (self.slots.len() - 1);
        }
        Ok(Add::Full)
    }

    pub(super) fn take(&mut self, index: usize) -> Option<Delta> {
        let slot = std::mem::take(&mut self.slots[index]);
        (slot.id != 0).then_some(Delta {
            id: slot.id,
            change: slot.count,
        })
    }

    pub(super) fn finish_flush(&mut self) {
        self.entries = 0;
    }

    pub(super) fn len(&self) -> usize {
        self.slots.len()
    }

    pub(super) const fn is_empty(&self) -> bool {
        self.entries == 0
    }
}

fn hash(id: u32) -> usize {
    id.wrapping_mul(0x9e37_79b1) as usize
}

fn floor_power_of_two(value: usize) -> usize {
    if value == 0 {
        0
    } else {
        1usize << (usize::BITS - value.leading_zeros() - 1)
    }
}
