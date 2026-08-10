//! Bounded exact cache for recurring selected-membership sequences.

use std::mem::size_of;

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::heap::Heap;

const MAX_ENTRIES: usize = 1024;
const MAX_POSITIONS: usize = 65_536;

#[derive(Clone, Copy, Default)]
struct Slot {
    hash: u64,
    value: u32,
    offset: u32,
    length_plus_one: u32,
}

pub(super) struct SequenceCache {
    slots: Vec<Slot>,
    positions: Vec<u32>,
    position_limit: usize,
    mask: usize,
    entries: usize,
    entry_limit: usize,
}

impl SequenceCache {
    pub(super) const fn empty() -> Self {
        Self {
            slots: Vec::new(),
            positions: Vec::new(),
            position_limit: 0,
            mask: 0,
            entries: 0,
            entry_limit: 0,
        }
    }

    pub(super) fn enable(&mut self, heap: &mut Heap, max_bytes: u64) -> Result<()> {
        if !self.slots.is_empty() {
            return Ok(());
        }
        let available = heap.remaining().min(max_bytes);
        let minimum = size_of::<Slot>()
            .checked_mul(2)
            .and_then(|bytes| bytes.checked_add(size_of::<u32>()))
            .ok_or(Error::BudgetExceeded("membership sequence cache"))?;
        let possible = usize::try_from(available)
            .unwrap_or(usize::MAX)
            .checked_div(minimum)
            .unwrap_or(0)
            .min(MAX_ENTRIES);
        let entry_limit = floor_power_of_two(possible);
        if entry_limit == 0 {
            return Ok(());
        }
        let slot_count = entry_limit
            .checked_mul(2)
            .ok_or(Error::BudgetExceeded("membership sequence cache"))?;
        let slot_bytes = slot_count
            .checked_mul(size_of::<Slot>())
            .ok_or(Error::BudgetExceeded("membership sequence cache"))?;
        let remaining = usize::try_from(available)
            .unwrap_or(usize::MAX)
            .saturating_sub(slot_bytes);
        let position_capacity = (remaining / size_of::<u32>()).min(MAX_POSITIONS);
        if position_capacity == 0 {
            return Ok(());
        }
        self.slots = heap.filled(slot_count, Slot::default(), "membership sequence cache")?;
        self.positions = heap.vector(position_capacity, "membership sequence cache")?;
        self.position_limit = position_capacity;
        self.mask = slot_count - 1;
        self.entry_limit = entry_limit;
        Ok(())
    }

    pub(super) fn keyed(&self, key: u32) -> Option<&[u32]> {
        if key == 0 || self.slots.is_empty() {
            return None;
        }
        let hash = hash_key(key);
        self.find(hash, |slot| slot.value == key)
            .map(|slot| self.sequence(slot))
    }

    pub(super) fn insert_keyed(&mut self, key: u32, positions: &[u32]) -> Result<()> {
        if key == 0 {
            return Err(Error::Corrupt("membership cache key is zero"));
        }
        self.insert(hash_key(key), key, positions)
    }

    pub(super) fn sequence_value(
        &self,
        positions: &[u32],
        cancellation: &CancellationToken,
    ) -> Result<Option<u32>> {
        if self.slots.is_empty() {
            return Ok(None);
        }
        let hash = hash_sequence(positions, cancellation)?;
        let mut index = hash as usize & self.mask;
        for _ in 0..self.slots.len() {
            let slot = self.slots[index];
            if slot.length_plus_one == 0 {
                return Ok(None);
            }
            if slot.hash == hash && sequences_equal(self.sequence(slot), positions, cancellation)? {
                return Ok(Some(slot.value));
            }
            index = (index + 1) & self.mask;
        }
        Ok(None)
    }

    pub(super) fn insert_sequence(
        &mut self,
        positions: &[u32],
        value: u32,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        if value == 0 {
            return Err(Error::Corrupt("membership cache value is zero"));
        }
        self.insert(hash_sequence(positions, cancellation)?, value, positions)
    }

    fn find(&self, hash: u64, matches: impl Fn(Slot) -> bool) -> Option<Slot> {
        let mut index = hash as usize & self.mask;
        for _ in 0..self.slots.len() {
            let slot = self.slots[index];
            if slot.length_plus_one == 0 {
                return None;
            }
            if slot.hash == hash && matches(slot) {
                return Some(slot);
            }
            index = (index + 1) & self.mask;
        }
        None
    }

    fn insert(&mut self, hash: u64, value: u32, positions: &[u32]) -> Result<()> {
        if self.slots.is_empty()
            || self.entries == self.entry_limit
            || positions.len() > self.position_limit - self.positions.len()
        {
            return Ok(());
        }
        let length = u32::try_from(positions.len())
            .map_err(|_| Error::BudgetExceeded("membership sequence cache"))?;
        let offset = u32::try_from(self.positions.len())
            .map_err(|_| Error::BudgetExceeded("membership sequence cache"))?;
        let mut index = hash as usize & self.mask;
        for _ in 0..self.slots.len() {
            if self.slots[index].length_plus_one == 0 {
                self.positions.extend_from_slice(positions);
                self.slots[index] = Slot {
                    hash,
                    value,
                    offset,
                    length_plus_one: length
                        .checked_add(1)
                        .ok_or(Error::BudgetExceeded("membership sequence cache"))?,
                };
                self.entries += 1;
                return Ok(());
            }
            index = (index + 1) & self.mask;
        }
        Ok(())
    }

    fn sequence(&self, slot: Slot) -> &[u32] {
        let start = slot.offset as usize;
        let length = (slot.length_plus_one - 1) as usize;
        &self.positions[start..start + length]
    }
}

fn floor_power_of_two(value: usize) -> usize {
    if value == 0 {
        0
    } else {
        1usize << (usize::BITS - value.leading_zeros() - 1)
    }
}

fn hash_key(key: u32) -> u64 {
    mix(u64::from(key))
}

fn hash_sequence(positions: &[u32], cancellation: &CancellationToken) -> Result<u64> {
    let mut hash = mix(positions.len() as u64);
    for (work, &position) in positions.iter().enumerate() {
        if work & 4095 == 4095 {
            cancellation.check()?;
        }
        hash = mix(hash ^ u64::from(position));
    }
    Ok(hash)
}

fn sequences_equal(left: &[u32], right: &[u32], cancellation: &CancellationToken) -> Result<bool> {
    if left.len() != right.len() {
        return Ok(false);
    }
    for (work, (&left, &right)) in left.iter().zip(right).enumerate() {
        if work & 4095 == 4095 {
            cancellation.check()?;
        }
        if left != right {
            return Ok(false);
        }
    }
    Ok(true)
}

fn mix(mut value: u64) -> u64 {
    value ^= value >> 30;
    value = value.wrapping_mul(0xbf58_476d_1ce4_e5b9);
    value ^= value >> 27;
    value = value.wrapping_mul(0x94d0_49bb_1331_11eb);
    value ^ (value >> 31)
}
