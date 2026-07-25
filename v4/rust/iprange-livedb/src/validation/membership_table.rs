use std::mem;

use crate::error::{Error, Result};

#[derive(Clone, Copy)]
pub(crate) struct Slot {
    pub(crate) id: u32,
    pub(crate) range_count: u64,
    pub(crate) stored_refcount: u64,
    pub(crate) word_count: u32,
    pub(crate) digest: [u8; 32],
    pub(crate) defined: bool,
    pub(crate) reverse_seen: bool,
}

impl Slot {
    const EMPTY: Self = Self {
        id: 0,
        range_count: 0,
        stored_refcount: 0,
        word_count: 0,
        digest: [0; 32],
        defined: false,
        reverse_seen: false,
    };
}

pub(crate) enum InsertResult {
    Inserted,
    Existing,
    Full,
}

pub(crate) struct Table {
    slots: Vec<Slot>,
    mask: usize,
}

impl Table {
    pub(crate) fn new(entry_count: u64, available_bytes: u64) -> Result<Self> {
        if entry_count == 0 {
            return Ok(Self {
                slots: Vec::new(),
                mask: 0,
            });
        }
        let wanted = entry_count
            .checked_mul(2)
            .and_then(|value| value.checked_next_power_of_two())
            .ok_or(Error::ArithmeticOverflow(
                "validation membership table capacity",
            ))?;
        let capacity = usize::try_from(wanted)
            .map_err(|_| Error::BudgetExceeded("validation membership table"))?;
        let bytes =
            wanted
                .checked_mul(mem::size_of::<Slot>() as u64)
                .ok_or(Error::ArithmeticOverflow(
                    "validation membership table bytes",
                ))?;
        if bytes > available_bytes {
            return Err(Error::BudgetExceeded("validation membership table"));
        }
        let mut slots = Vec::new();
        slots
            .try_reserve_exact(capacity)
            .map_err(|_| Error::BudgetExceeded("validation membership table"))?;
        slots.resize(capacity, Slot::EMPTY);
        Ok(Self {
            slots,
            mask: capacity - 1,
        })
    }

    pub(crate) fn retained_bytes(&self) -> u64 {
        self.slots.len() as u64 * mem::size_of::<Slot>() as u64
    }

    pub(crate) fn count_range(&mut self, id: u32) -> Result<InsertResult> {
        let Some(index) = self.find_or_empty(id) else {
            return Ok(InsertResult::Full);
        };
        let result = if self.slots[index].id == 0 {
            self.slots[index].id = id;
            InsertResult::Inserted
        } else {
            InsertResult::Existing
        };
        self.slots[index].range_count = self.slots[index]
            .range_count
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("validation membership refcount"))?;
        Ok(result)
    }

    pub(crate) fn define(
        &mut self,
        id: u32,
        stored_refcount: u64,
        word_count: u32,
        digest: [u8; 32],
    ) -> InsertResult {
        let Some(index) = self.find_or_empty(id) else {
            return InsertResult::Full;
        };
        let slot = &mut self.slots[index];
        if slot.id == 0 {
            slot.id = id;
        } else if slot.defined {
            return InsertResult::Existing;
        }
        slot.stored_refcount = stored_refcount;
        slot.word_count = word_count;
        slot.digest = digest;
        slot.defined = true;
        InsertResult::Inserted
    }

    pub(crate) fn mark_reverse(&mut self, id: u32, word_count: u32, digest: [u8; 32]) -> bool {
        let Some(index) = self.find(id) else {
            return false;
        };
        let slot = &mut self.slots[index];
        if !slot.defined
            || slot.word_count != word_count
            || slot.digest != digest
            || slot.reverse_seen
        {
            return false;
        }
        slot.reverse_seen = true;
        true
    }

    pub(crate) fn len(&self) -> usize {
        self.slots.len()
    }

    pub(crate) fn slot(&self, index: usize) -> Option<Slot> {
        self.slots.get(index).copied().filter(|slot| slot.id != 0)
    }

    fn find(&self, id: u32) -> Option<usize> {
        if self.slots.is_empty() || id == 0 {
            return None;
        }
        let mut index = hash(id) & self.mask;
        for _ in 0..self.slots.len() {
            match self.slots[index].id {
                0 => return None,
                found if found == id => return Some(index),
                _ => index = (index + 1) & self.mask,
            }
        }
        None
    }

    fn find_or_empty(&self, id: u32) -> Option<usize> {
        if self.slots.is_empty() || id == 0 {
            return None;
        }
        let mut index = hash(id) & self.mask;
        for _ in 0..self.slots.len() {
            let found = self.slots[index].id;
            if found == 0 || found == id {
                return Some(index);
            }
            index = (index + 1) & self.mask;
        }
        None
    }
}

fn hash(id: u32) -> usize {
    (id.wrapping_mul(0x9e37_79b1)) as usize
}
