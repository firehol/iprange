#[cfg(target_os = "linux")]
use std::mem;
use std::mem::size_of;
#[cfg(target_os = "linux")]
use std::path::PathBuf;

use crate::contract::MetaV4;
use crate::error::{Error, Result};

#[cfg(target_os = "linux")]
use super::scratch::{residue_error, Scratch, ScratchFile, ScratchSlot, HEADER_SIZE};
use super::{RecoveryBudget, ScratchCleanup};

const EMPTY: u64 = 0;
const MIN_SLOTS: usize = 8;
const MAX_LOAD_NUMERATOR: usize = 3;
const MAX_LOAD_DENOMINATOR: usize = 4;

enum Slots {
    Heap(Vec<u64>),
    #[cfg(target_os = "linux")]
    File(FileSlots),
}

#[cfg(target_os = "linux")]
struct FileSlots {
    file: ScratchFile,
    slots: usize,
}

#[cfg(target_os = "linux")]
struct Fallback {
    directory: PathBuf,
    source: MetaV4,
    max_bytes: u64,
    max_files: u32,
    max_open_files: u32,
    wanted_slots: usize,
}

/// Sparse page set which migrates to authorized fixed-slot scratch when needed.
pub(crate) struct PageSet {
    slots: Slots,
    len: usize,
    #[cfg(target_os = "linux")]
    fallback: Option<Fallback>,
    #[cfg(target_os = "linux")]
    scratch: Option<Scratch>,
    #[cfg(target_os = "linux")]
    completed: Option<ScratchCleanup>,
}

pub(crate) struct PageSetFailure {
    pub(crate) cause: Error,
    pub(crate) cleanup: Option<ScratchCleanup>,
}

impl PageSet {
    pub(crate) fn new(max_heap_bytes: u64, expected_pages: u64) -> Result<Self> {
        Self::allocate(max_heap_bytes, expected_pages, None)
    }

    pub(crate) fn for_recovery(
        max_heap_bytes: u64,
        expected_pages: u64,
        source: MetaV4,
        budget: &RecoveryBudget,
    ) -> Result<Self> {
        #[cfg(target_os = "linux")]
        let fallback = budget.scratch_directory.as_ref().map(|directory| Fallback {
            directory: directory.clone(),
            source,
            max_bytes: budget.max_scratch_bytes,
            max_files: budget.max_scratch_files,
            max_open_files: budget.max_open_files,
            wanted_slots: wanted_slots(expected_pages),
        });
        #[cfg(not(target_os = "linux"))]
        let fallback = {
            let _ = (source, budget);
            None
        };
        Self::allocate(max_heap_bytes, expected_pages, fallback)
    }

    #[cfg(target_os = "linux")]
    fn allocate(
        max_heap_bytes: u64,
        expected_pages: u64,
        fallback: Option<Fallback>,
    ) -> Result<Self> {
        let affordable =
            usize::try_from(max_heap_bytes / size_of::<u64>() as u64).unwrap_or(usize::MAX);
        if affordable < MIN_SLOTS && fallback.is_none() {
            return Err(Error::BudgetExceeded("recovery page-ownership table"));
        }
        let wanted = wanted_slots(expected_pages);
        let slots = if wanted <= affordable {
            wanted
        } else {
            floor_power_of_two(affordable)
        };
        let values = heap_slots(slots)?;
        Ok(Self {
            slots: Slots::Heap(values),
            len: 0,
            fallback,
            scratch: None,
            completed: None,
        })
    }

    #[cfg(not(target_os = "linux"))]
    fn allocate(max_heap_bytes: u64, expected_pages: u64, _fallback: Option<()>) -> Result<Self> {
        let affordable =
            usize::try_from(max_heap_bytes / size_of::<u64>() as u64).unwrap_or(usize::MAX);
        if affordable < MIN_SLOTS {
            return Err(Error::BudgetExceeded("recovery page-ownership table"));
        }
        let slots = wanted_slots(expected_pages).min(floor_power_of_two(affordable));
        Ok(Self {
            slots: Slots::Heap(heap_slots(slots)?),
            len: 0,
        })
    }

    /// Returns `true` for a new page and `false` for a prior claim.
    pub(crate) fn insert(&mut self, page: u32) -> Result<bool> {
        if exceeds_load(self.len + 1, self.slot_count()) {
            #[cfg(target_os = "linux")]
            self.migrate()?;
            if exceeds_load(self.len + 1, self.slot_count()) {
                return Err(Error::BudgetExceeded("recovery page-ownership table"));
            }
        }
        let encoded = u64::from(page) + 1;
        let mask = self.slot_count() - 1;
        let mut index = hash(page) & mask;
        loop {
            match self.read(index)? {
                EMPTY => {
                    self.write(index, encoded)?;
                    self.len += 1;
                    return Ok(true);
                }
                value if value == encoded => return Ok(false),
                _ => index = (index + 1) & mask,
            }
        }
    }

    pub(crate) fn retained_bytes(&self) -> u64 {
        match &self.slots {
            Slots::Heap(slots) => slots.len() as u64 * size_of::<u64>() as u64,
            #[cfg(target_os = "linux")]
            Slots::File(_) => 0,
        }
    }

    pub(crate) fn reset(&mut self) -> Result<()> {
        self.len = 0;
        match &mut self.slots {
            Slots::Heap(slots) => slots.fill(EMPTY),
            #[cfg(target_os = "linux")]
            Slots::File(_) => self.reset_file()?,
        }
        Ok(())
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn finish(
        self,
        result: Result<()>,
    ) -> std::result::Result<Option<ScratchCleanup>, PageSetFailure> {
        #[cfg(target_os = "linux")]
        {
            finish_cleanup(self.cleanup(), result)
        }
        #[cfg(not(target_os = "linux"))]
        {
            drop(self);
            result.map(|()| None).map_err(|cause| PageSetFailure {
                cause,
                cleanup: None,
            })
        }
    }

    #[cfg(target_os = "linux")]
    pub(crate) fn take_scratch(&mut self) -> Option<Scratch> {
        self.fallback = None;
        self.scratch.take()
    }

    #[cfg(target_os = "linux")]
    pub(crate) fn release(mut self, scratch: &mut Scratch) -> Option<ScratchSlot> {
        match mem::replace(&mut self.slots, Slots::Heap(Vec::new())) {
            Slots::Heap(_) => None,
            Slots::File(slots) => Some(scratch.attach(slots.file)),
        }
    }

    #[cfg(target_os = "linux")]
    pub(crate) fn cleanup(mut self) -> Option<ScratchCleanup> {
        if let Some(mut scratch) = self.scratch.take() {
            let _ = self.release(&mut scratch);
            Some(scratch.cleanup())
        } else {
            self.completed.take()
        }
    }

    fn slot_count(&self) -> usize {
        match &self.slots {
            Slots::Heap(slots) => slots.len(),
            #[cfg(target_os = "linux")]
            Slots::File(slots) => slots.slots,
        }
    }

    fn read(&self, index: usize) -> Result<u64> {
        match &self.slots {
            Slots::Heap(slots) => Ok(slots[index]),
            #[cfg(target_os = "linux")]
            Slots::File(slots) => slots.read(index),
        }
    }

    fn write(&mut self, index: usize, value: u64) -> Result<()> {
        match &mut self.slots {
            Slots::Heap(slots) => {
                slots[index] = value;
                Ok(())
            }
            #[cfg(target_os = "linux")]
            Slots::File(slots) => slots.write(index, value),
        }
    }

    #[cfg(target_os = "linux")]
    fn migrate(&mut self) -> Result<()> {
        if matches!(self.slots, Slots::File(_)) {
            return Ok(());
        }
        let fallback = self
            .fallback
            .take()
            .ok_or(Error::BudgetExceeded("recovery page-ownership table"))?;
        let slots = fallback.file_slots()?;
        if exceeds_load(self.len + 1, slots) {
            return Err(Error::BudgetExceeded("recovery page-ownership table"));
        }
        let (scratch, output) = self.create_file_slots(&fallback, slots)?;
        self.slots = Slots::File(output);
        self.scratch = Some(scratch);
        Ok(())
    }

    #[cfg(target_os = "linux")]
    fn create_file_slots(
        &mut self,
        fallback: &Fallback,
        slots: usize,
    ) -> Result<(Scratch, FileSlots)> {
        let length = table_length(slots)?;
        let mut scratch = Scratch::start(
            &fallback.directory,
            fallback.source,
            fallback.max_bytes,
            fallback.max_files,
            fallback.max_open_files,
        )?;
        let slot = match scratch.create() {
            Ok(slot) => slot,
            Err(cause) => return Err(self.finish_failed_attempt(scratch, cause)),
        };
        if let Err(cause) = scratch.resize(slot, length) {
            return Err(self.finish_failed_attempt(scratch, cause));
        }
        let file = match scratch.detach(slot) {
            Ok(file) => file,
            Err(cause) => return Err(self.finish_failed_attempt(scratch, cause)),
        };
        let mut output = FileSlots { file, slots };
        if let Err(cause) = self.copy_claims(&mut output) {
            scratch.attach(output.file);
            return Err(self.finish_failed_attempt(scratch, cause));
        }
        Ok((scratch, output))
    }

    #[cfg(target_os = "linux")]
    fn copy_claims(&self, output: &mut FileSlots) -> Result<()> {
        let Slots::Heap(values) = &self.slots else {
            return Ok(());
        };
        for &value in values.iter().filter(|&&value| value != EMPTY) {
            output.insert(value)?;
        }
        Ok(())
    }

    #[cfg(target_os = "linux")]
    fn finish_failed_attempt(&mut self, scratch: Scratch, cause: Error) -> Error {
        let cleanup = scratch.cleanup();
        let result = if cleanup.clean() {
            cause
        } else {
            Error::CleanupIncomplete {
                cause: Box::new(cause),
                cleanup: Box::new(residue_error(&cleanup)),
            }
        };
        self.completed = Some(cleanup);
        result
    }

    #[cfg(target_os = "linux")]
    fn reset_file(&mut self) -> Result<()> {
        let Slots::File(slots) = mem::replace(&mut self.slots, Slots::Heap(Vec::new())) else {
            unreachable!("file reset requires file slots")
        };
        let count = slots.slots;
        let scratch = self
            .scratch
            .as_mut()
            .expect("file page set retains its scratch attempt");
        let slot = scratch.attach(slots.file);
        scratch.reset(slot)?;
        scratch.resize(slot, table_length(count)?)?;
        let file = scratch.detach(slot)?;
        self.slots = Slots::File(FileSlots { file, slots: count });
        Ok(())
    }
}

#[cfg(target_os = "linux")]
#[allow(clippy::result_large_err)]
fn finish_cleanup(
    cleanup: Option<ScratchCleanup>,
    result: Result<()>,
) -> std::result::Result<Option<ScratchCleanup>, PageSetFailure> {
    let Some(cleanup) = cleanup else {
        return result.map(|()| None).map_err(|cause| PageSetFailure {
            cause,
            cleanup: None,
        });
    };
    if cleanup.clean() {
        return match result {
            Ok(()) => Ok(Some(cleanup)),
            Err(cause) => Err(PageSetFailure {
                cause,
                cleanup: Some(cleanup),
            }),
        };
    }
    let cause = Error::CleanupIncomplete {
        cause: Box::new(match result {
            Ok(()) => Error::Corrupt("recovery scratch cleanup is incomplete"),
            Err(cause) => cause,
        }),
        cleanup: Box::new(residue_error(&cleanup)),
    };
    Err(PageSetFailure {
        cause,
        cleanup: Some(cleanup),
    })
}

#[cfg(target_os = "linux")]
impl FileSlots {
    fn read(&self, index: usize) -> Result<u64> {
        let mut bytes = [0; 8];
        self.file.read(slot_offset(index)?, &mut bytes)?;
        Ok(u64::from_le_bytes(bytes))
    }

    fn write(&self, index: usize, value: u64) -> Result<()> {
        self.file.write(slot_offset(index)?, &value.to_le_bytes())
    }

    fn insert(&mut self, encoded: u64) -> Result<()> {
        let page = u32::try_from(encoded - 1)
            .map_err(|_| Error::Corrupt("recovery page claim is invalid"))?;
        let mask = self.slots - 1;
        let mut index = hash(page) & mask;
        loop {
            if self.read(index)? == EMPTY {
                return self.write(index, encoded);
            }
            index = (index + 1) & mask;
        }
    }
}

#[cfg(target_os = "linux")]
impl Fallback {
    fn file_slots(&self) -> Result<usize> {
        let reserved = if self.max_files >= 2 { HEADER_SIZE } else { 0 };
        let table_bytes = self
            .max_bytes
            .checked_sub(reserved)
            .and_then(|value| value.checked_sub(HEADER_SIZE))
            .ok_or(Error::BudgetExceeded("recovery page-ownership scratch"))?;
        let affordable =
            usize::try_from(table_bytes / size_of::<u64>() as u64).unwrap_or(usize::MAX);
        let slots = self.wanted_slots.min(floor_power_of_two(affordable));
        if slots < MIN_SLOTS {
            return Err(Error::BudgetExceeded("recovery page-ownership scratch"));
        }
        Ok(slots)
    }
}

fn heap_slots(slots: usize) -> Result<Vec<u64>> {
    if slots < MIN_SLOTS {
        return Ok(Vec::new());
    }
    let mut values = Vec::new();
    values
        .try_reserve_exact(slots)
        .map_err(|_| Error::BudgetExceeded("recovery page-ownership table"))?;
    values.resize(slots, EMPTY);
    Ok(values)
}

fn wanted_slots(expected_pages: u64) -> usize {
    let wanted = expected_pages
        .saturating_mul(MAX_LOAD_DENOMINATOR as u64)
        .div_ceil(MAX_LOAD_NUMERATOR as u64)
        .max(MIN_SLOTS as u64);
    usize::try_from(wanted)
        .ok()
        .and_then(usize::checked_next_power_of_two)
        .unwrap_or(1usize << (usize::BITS - 1))
}

fn exceeds_load(len: usize, slots: usize) -> bool {
    slots == 0
        || len.saturating_mul(MAX_LOAD_DENOMINATOR) > slots.saturating_mul(MAX_LOAD_NUMERATOR)
}

#[cfg(target_os = "linux")]
fn table_length(slots: usize) -> Result<u64> {
    (slots as u64)
        .checked_mul(size_of::<u64>() as u64)
        .and_then(|bytes| bytes.checked_add(HEADER_SIZE))
        .ok_or(Error::ArithmeticOverflow(
            "recovery page-ownership scratch length",
        ))
}

#[cfg(target_os = "linux")]
fn slot_offset(index: usize) -> Result<u64> {
    (index as u64)
        .checked_mul(size_of::<u64>() as u64)
        .and_then(|bytes| bytes.checked_add(HEADER_SIZE))
        .ok_or(Error::ArithmeticOverflow(
            "recovery page-ownership scratch offset",
        ))
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
#[path = "page_set_tests.rs"]
mod tests;
