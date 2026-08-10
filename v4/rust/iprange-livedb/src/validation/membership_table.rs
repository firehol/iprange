use std::mem;

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};

#[derive(Clone, Copy)]
pub(crate) struct Slot {
    pub(crate) id: u32,
    pub(crate) range_count: u64,
    pub(crate) stored_refcount: u64,
    word_count: u32,
    digest: [u8; 32],
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

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum CountResult {
    Inserted,
    Existing,
    Full,
    Cancelled,
    Unavailable,
}

enum ProbeResult {
    Index(usize),
    Missing,
    Cancelled,
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

    pub(crate) fn count_range(&mut self, id: u32, cancellation: &CancellationToken) -> CountResult {
        let index = match self.probe(id, true, cancellation) {
            ProbeResult::Index(index) => index,
            ProbeResult::Missing => return CountResult::Full,
            ProbeResult::Cancelled => return CountResult::Cancelled,
        };
        let result = if self.slots[index].id == 0 {
            self.slots[index].id = id;
            CountResult::Inserted
        } else {
            CountResult::Existing
        };
        // A membership's count cannot exceed the total ranges addressable by
        // u32 pages and u16 page slots, so this cannot overflow u64.
        self.slots[index].range_count += 1;
        result
    }

    pub(crate) fn define(
        &mut self,
        id: u32,
        stored_refcount: u64,
        word_count: u32,
        digest: [u8; 32],
        cancellation: &CancellationToken,
    ) -> Result<InsertResult> {
        let Some(index) = self.find_or_empty(id, cancellation)? else {
            return Ok(InsertResult::Full);
        };
        if self.slots[index].id == 0 {
            self.slots[index].id = id;
        } else if self.slots[index].defined {
            return Ok(InsertResult::Existing);
        }
        let slot = &mut self.slots[index];
        slot.stored_refcount = stored_refcount;
        slot.word_count = word_count;
        slot.digest = digest;
        slot.defined = true;
        slot.reverse_seen = false;
        Ok(InsertResult::Inserted)
    }

    pub(crate) fn mark_reverse(
        &mut self,
        id: u32,
        word_count: u32,
        digest: [u8; 32],
        cancellation: &CancellationToken,
    ) -> Result<bool> {
        let Some(index) = self.find(id, cancellation)? else {
            return Ok(false);
        };
        let slot = &mut self.slots[index];
        if !slot.defined
            || slot.word_count != word_count
            || slot.digest != digest
            || slot.reverse_seen
        {
            return Ok(false);
        }
        slot.reverse_seen = true;
        Ok(true)
    }

    pub(crate) fn len(&self) -> usize {
        self.slots.len()
    }

    pub(crate) fn slot(&self, index: usize) -> Option<Slot> {
        let slot = *self.slots.get(index)?;
        if slot.id == 0 {
            return None;
        }
        Some(slot)
    }

    fn find(&self, id: u32, cancellation: &CancellationToken) -> Result<Option<usize>> {
        self.probe_result(id, false, cancellation)
    }

    fn find_or_empty(&self, id: u32, cancellation: &CancellationToken) -> Result<Option<usize>> {
        self.probe_result(id, true, cancellation)
    }

    fn probe_result(
        &self,
        id: u32,
        accept_empty: bool,
        cancellation: &CancellationToken,
    ) -> Result<Option<usize>> {
        match self.probe(id, accept_empty, cancellation) {
            ProbeResult::Index(index) => Ok(Some(index)),
            ProbeResult::Missing => Ok(None),
            ProbeResult::Cancelled => Err(Error::Cancelled),
        }
    }

    fn probe(&self, id: u32, accept_empty: bool, cancellation: &CancellationToken) -> ProbeResult {
        if self.slots.is_empty() || id == 0 {
            return ProbeResult::Missing;
        }
        let mut index = hash(id) & self.mask;
        for probe in 0..self.slots.len() {
            if probe != 0 && probe & 63 == 0 && cancellation.is_cancelled() {
                return ProbeResult::Cancelled;
            }
            let found = self.slots[index].id;
            if found == id || (accept_empty && found == 0) {
                return ProbeResult::Index(index);
            }
            if found == 0 {
                return ProbeResult::Missing;
            }
            index = (index + 1) & self.mask;
        }
        ProbeResult::Missing
    }
}

fn hash(id: u32) -> usize {
    (id.wrapping_mul(0x9e37_79b1)) as usize
}

#[cfg(test)]
mod tests {
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::sync::Arc;

    use super::*;

    #[test]
    fn long_probe_chain_has_bounded_cancellation_checkpoints() {
        let mut table = Table::new(64, u64::MAX).unwrap();
        let ready = CancellationToken::new();
        for index in 0..64 {
            assert_ne!(
                table.count_range(1 + index * 128, &ready),
                CountResult::Cancelled
            );
        }

        let polls = Arc::new(AtomicUsize::new(0));
        let observed = Arc::clone(&polls);
        let cancellation = CancellationToken::from_poll(Arc::new(move || {
            observed.fetch_add(1, Ordering::SeqCst);
            true
        }));
        assert!(matches!(
            table.count_range(1 + 64 * 128, &cancellation),
            CountResult::Cancelled
        ));
        assert_eq!(polls.load(Ordering::SeqCst), 1);
    }

    #[test]
    fn ordinary_probe_does_not_repeat_the_callers_checkpoint() {
        let mut table = Table::new(1, u64::MAX).unwrap();
        let polls = Arc::new(AtomicUsize::new(0));
        let observed = Arc::clone(&polls);
        let cancellation = CancellationToken::from_poll(Arc::new(move || {
            observed.fetch_add(1, Ordering::SeqCst);
            false
        }));

        assert!(matches!(
            table.count_range(1, &cancellation),
            CountResult::Inserted
        ));
        assert_eq!(polls.load(Ordering::SeqCst), 0);
    }
}
