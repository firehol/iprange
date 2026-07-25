use std::sync::atomic::Ordering;
use std::sync::Arc;

use crate::error::{Error, Result};
use crate::file_io;

use super::{ScratchSlot, SharedFile, HEADER_SIZE};

/// Shared fixed-size access to one scratch file retained by its attempt.
#[derive(Clone)]
pub(crate) struct ScratchFile {
    pub(super) slot: ScratchSlot,
    pub(super) shared: Arc<SharedFile>,
}

impl ScratchFile {
    pub(crate) fn slot(&self) -> ScratchSlot {
        self.slot
    }

    pub(crate) fn length(&self) -> u64 {
        self.shared.length.load(Ordering::Relaxed)
    }

    pub(crate) fn read(&self, offset: u64, bytes: &mut [u8]) -> Result<()> {
        require_fixed_io(offset, bytes.len(), self.length())?;
        file_io::read_exact_at(&self.shared.file, bytes, offset)
    }

    pub(crate) fn write(&self, offset: u64, bytes: &[u8]) -> Result<()> {
        require_fixed_io(offset, bytes.len(), self.length())?;
        file_io::write_exact_at(&self.shared.file, bytes, offset)
    }
}

fn require_fixed_io(offset: u64, length: usize, retained: u64) -> Result<()> {
    let end = offset
        .checked_add(length as u64)
        .ok_or(Error::ArithmeticOverflow("fixed recovery scratch I/O"))?;
    if offset < HEADER_SIZE || end > retained {
        return Err(Error::Corrupt("scratch I/O exceeds its fixed region"));
    }
    Ok(())
}
