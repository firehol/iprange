//! One bounded backing store for recovery catalog and membership tables.

use crate::error::{Error, Result};

use super::page_set::PageSet;
#[cfg(any(unix, windows))]
use super::scratch::{ScratchFile, ScratchSlot, HEADER_SIZE};
use super::RecoveryBudget;

pub(crate) const CATALOG_RECORD_SIZE: u64 = 264;
pub(crate) const CATALOG_NAME_SLOT_SIZE: u64 = 24;
pub(crate) const CATALOG_INDEX_SLOT_SIZE: u64 = 16;
pub(crate) const MEMBERSHIP_RECORD_SIZE: u64 = 56;
pub(crate) const STRUCTURE_RECORD_SIZE: u64 = 48;
pub(crate) const ID_SLOT_SIZE: u64 = 16;

#[derive(Clone, Copy)]
pub(crate) struct Counts {
    pub(crate) catalog: u64,
    pub(crate) memberships: u64,
    pub(crate) structures: u64,
}

#[derive(Clone, Copy)]
pub(crate) struct Region {
    start: u64,
    pub(crate) slots: u64,
    width: u64,
}

#[derive(Clone, Copy)]
pub(crate) struct Layout {
    pub(crate) catalog_records: Region,
    pub(crate) catalog_names: Region,
    pub(crate) catalog_indexes: Region,
    pub(crate) membership_records: Region,
    pub(crate) membership_ids: Region,
    pub(crate) structure_records: Region,
    pub(crate) structure_ids: Region,
    bytes: u64,
}

pub(crate) struct Tables {
    storage: Storage,
    layout: Layout,
}

enum Storage {
    Heap(Vec<u8>),
    #[cfg(any(unix, windows))]
    Scratch(ScratchFile),
}

impl Tables {
    pub(crate) fn allocate(
        counts: Counts,
        pages: &mut PageSet,
        budget: &RecoveryBudget,
        reserved_heap_bytes: u64,
    ) -> Result<Self> {
        let layout = Layout::new(counts)?;
        let available = budget
            .max_heap_bytes
            .checked_sub(pages.retained_bytes())
            .and_then(|bytes| bytes.checked_sub(reserved_heap_bytes))
            .unwrap_or(0);
        if let Some(bytes) = heap_bytes(layout.bytes, available) {
            return Ok(Self {
                storage: Storage::Heap(bytes),
                layout,
            });
        }
        #[cfg(any(unix, windows))]
        {
            let length = layout
                .bytes
                .checked_add(HEADER_SIZE)
                .ok_or(Error::ArithmeticOverflow("recovery table scratch"))?;
            let file = pages.create_scratch_file(length)?;
            Ok(Self {
                storage: Storage::Scratch(file),
                layout,
            })
        }
        #[cfg(not(any(unix, windows)))]
        {
            let _ = pages;
            Err(Error::BudgetExceeded("recovery tables"))
        }
    }

    pub(crate) fn layout(&self) -> Layout {
        self.layout
    }

    pub(crate) fn retained_bytes(&self) -> u64 {
        match &self.storage {
            Storage::Heap(bytes) => bytes.capacity() as u64,
            #[cfg(any(unix, windows))]
            Storage::Scratch(_) => 0,
        }
    }

    #[cfg(any(unix, windows))]
    pub(crate) fn scratch_region(&self) -> Option<(ScratchSlot, u64)> {
        match &self.storage {
            Storage::Heap(_) => None,
            Storage::Scratch(file) => Some((file.slot(), file.length())),
        }
    }

    pub(crate) fn read(&self, region: Region, index: u64, output: &mut [u8]) -> Result<()> {
        let offset = self.offset(region, index, output.len())?;
        match &self.storage {
            Storage::Heap(bytes) => {
                let start = usize::try_from(offset)
                    .map_err(|_| Error::ArithmeticOverflow("recovery table heap offset"))?;
                output.copy_from_slice(&bytes[start..start + output.len()]);
                Ok(())
            }
            #[cfg(any(unix, windows))]
            Storage::Scratch(file) => file.read(offset + HEADER_SIZE, output),
        }
    }

    pub(crate) fn write(&mut self, region: Region, index: u64, input: &[u8]) -> Result<()> {
        let offset = self.offset(region, index, input.len())?;
        match &mut self.storage {
            Storage::Heap(bytes) => {
                let start = usize::try_from(offset)
                    .map_err(|_| Error::ArithmeticOverflow("recovery table heap offset"))?;
                bytes[start..start + input.len()].copy_from_slice(input);
                Ok(())
            }
            #[cfg(any(unix, windows))]
            Storage::Scratch(file) => file.write(offset + HEADER_SIZE, input),
        }
    }

    fn offset(&self, region: Region, index: u64, width: usize) -> Result<u64> {
        if width as u64 != region.width || index >= region.slots {
            return Err(Error::Corrupt(
                "recovery table access is outside its region",
            ));
        }
        let offset = index
            .checked_mul(region.width)
            .and_then(|bytes| region.start.checked_add(bytes))
            .ok_or(Error::ArithmeticOverflow("recovery table offset"))?;
        let Some(end) = offset.checked_add(region.width) else {
            return Err(Error::ArithmeticOverflow("recovery table offset"));
        };
        if end > self.layout.bytes {
            return Err(Error::Corrupt("recovery table region exceeds its backing"));
        }
        Ok(offset)
    }
}

impl Layout {
    fn new(counts: Counts) -> Result<Self> {
        let catalog_slots = hash_slots(counts.catalog)?;
        let membership_slots = hash_slots(counts.memberships)?;
        let structure_slots = hash_slots(counts.structures)?;
        let mut next = 0;
        let catalog_records = region(&mut next, counts.catalog, CATALOG_RECORD_SIZE)?;
        let catalog_names = region(&mut next, catalog_slots, CATALOG_NAME_SLOT_SIZE)?;
        let catalog_indexes = region(&mut next, catalog_slots, CATALOG_INDEX_SLOT_SIZE)?;
        let membership_records = region(&mut next, counts.memberships, MEMBERSHIP_RECORD_SIZE)?;
        let membership_ids = region(&mut next, membership_slots, ID_SLOT_SIZE)?;
        let structure_records = region(&mut next, counts.structures, STRUCTURE_RECORD_SIZE)?;
        let structure_ids = region(&mut next, structure_slots, ID_SLOT_SIZE)?;
        Ok(Self {
            catalog_records,
            catalog_names,
            catalog_indexes,
            membership_records,
            membership_ids,
            structure_records,
            structure_ids,
            bytes: next,
        })
    }
}

fn region(next: &mut u64, slots: u64, width: u64) -> Result<Region> {
    let start = *next;
    *next = slots
        .checked_mul(width)
        .and_then(|bytes| start.checked_add(bytes))
        .ok_or(Error::ArithmeticOverflow("recovery table layout"))?;
    Ok(Region {
        start,
        slots,
        width,
    })
}

fn hash_slots(records: u64) -> Result<u64> {
    if records == 0 {
        return Ok(0);
    }
    records
        .checked_mul(4)
        .and_then(|slots| slots.checked_add(2))
        .map(|slots| (slots / 3).max(8))
        .and_then(u64::checked_next_power_of_two)
        .ok_or(Error::ArithmeticOverflow("recovery table slots"))
}

pub(super) fn hash_u32(value: u32) -> u64 {
    let mut value = u64::from(value);
    value ^= value >> 30;
    value = value.wrapping_mul(0xbf58_476d_1ce4_e5b9);
    value ^= value >> 27;
    value = value.wrapping_mul(0x94d0_49bb_1331_11eb);
    value ^ (value >> 31)
}

fn heap_bytes(length: u64, available: u64) -> Option<Vec<u8>> {
    if length > available {
        return None;
    }
    let length = usize::try_from(length).ok()?;
    let mut bytes = Vec::new();
    bytes.try_reserve_exact(length).ok()?;
    if bytes.capacity() as u64 > available {
        return None;
    }
    bytes.resize(length, 0);
    Some(bytes)
}
