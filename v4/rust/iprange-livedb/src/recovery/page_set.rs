use std::mem::size_of;

use crate::error::{Error, Result};

const EMPTY: u64 = 0;
const MIN_SLOTS: usize = 8;
const MAX_LOAD_NUMERATOR: usize = 3;
const MAX_LOAD_DENOMINATOR: usize = 4;

/// Fixed-capacity sparse page set. Memory follows the budget, not page numbers.
pub(crate) struct PageSet {
    slots: Vec<u64>,
    len: usize,
}

impl PageSet {
    pub(crate) fn new(max_heap_bytes: u64, expected_pages: u64) -> Result<Self> {
        let affordable =
            usize::try_from(max_heap_bytes / size_of::<u64>() as u64).unwrap_or(usize::MAX);
        if affordable < MIN_SLOTS {
            return Err(Error::BudgetExceeded("recovery page-ownership table"));
        }
        let wanted = expected_pages
            .saturating_mul(MAX_LOAD_DENOMINATOR as u64)
            .div_ceil(MAX_LOAD_NUMERATOR as u64)
            .max(MIN_SLOTS as u64);
        let wanted = usize::try_from(wanted).unwrap_or(usize::MAX);
        let wanted_slots = wanted.checked_next_power_of_two().unwrap_or(usize::MAX);
        let slots = if wanted_slots <= affordable {
            wanted_slots
        } else {
            floor_power_of_two(affordable)
        };
        if slots < MIN_SLOTS {
            return Err(Error::BudgetExceeded("recovery page-ownership table"));
        }
        let mut values = Vec::new();
        values
            .try_reserve_exact(slots)
            .map_err(|_| Error::BudgetExceeded("recovery page-ownership table"))?;
        values.resize(slots, EMPTY);
        Ok(Self {
            slots: values,
            len: 0,
        })
    }

    /// Returns `true` for a new page and `false` for a prior claim.
    pub(crate) fn insert(&mut self, page: u32) -> Result<bool> {
        if (self.len + 1) * MAX_LOAD_DENOMINATOR > self.slots.len() * MAX_LOAD_NUMERATOR {
            return Err(Error::BudgetExceeded("recovery page-ownership table"));
        }
        let encoded = u64::from(page) + 1;
        let mask = self.slots.len() - 1;
        let mut index = hash(page) & mask;
        loop {
            match self.slots[index] {
                EMPTY => {
                    self.slots[index] = encoded;
                    self.len += 1;
                    return Ok(true);
                }
                value if value == encoded => return Ok(false),
                _ => index = (index + 1) & mask,
            }
        }
    }

    pub(crate) fn retained_bytes(&self) -> u64 {
        self.slots.len() as u64 * size_of::<u64>() as u64
    }
}

fn floor_power_of_two(value: usize) -> usize {
    if value == 0 {
        0
    } else {
        1usize << (usize::BITS - 1 - value.leading_zeros())
    }
}

fn hash(page: u32) -> usize {
    let mut value = u64::from(page);
    value ^= value >> 30;
    value = value.wrapping_mul(0xbf58_476d_1ce4_e5b9);
    value ^= value >> 27;
    value = value.wrapping_mul(0x94d0_49bb_1331_11eb);
    (value ^ (value >> 31)) as usize
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sparse_maximum_page_does_not_size_the_table() {
        let mut set = PageSet::new(1024, u64::from(u32::MAX)).unwrap();
        assert!(set.insert(u32::MAX).unwrap());
        assert!(!set.insert(u32::MAX).unwrap());
        assert!(set.slots.len() <= 128);
    }

    #[test]
    fn full_table_fails_before_allocation_or_looping() {
        let mut set = PageSet::new(64, 100).unwrap();
        for page in 0..6 {
            assert!(set.insert(page).unwrap());
        }
        assert!(matches!(
            set.insert(7),
            Err(Error::BudgetExceeded("recovery page-ownership table"))
        ));
    }
}
