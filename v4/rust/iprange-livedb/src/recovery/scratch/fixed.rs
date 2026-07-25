use std::fs::File;

use crate::error::{Error, Result};
use crate::file_io;

use super::{ScratchSlot, HEADER_SIZE};

/// Fixed-size storage detached while its scratch attempt retains ownership.
pub(crate) struct ScratchFile {
    pub(super) slot: ScratchSlot,
    pub(super) file: File,
    pub(super) length: u64,
}

impl ScratchFile {
    pub(crate) fn read(&self, offset: u64, bytes: &mut [u8]) -> Result<()> {
        require_fixed_io(offset, bytes.len(), self.length)?;
        file_io::read_exact_at(&self.file, bytes, offset)
    }

    pub(crate) fn write(&self, offset: u64, bytes: &[u8]) -> Result<()> {
        require_fixed_io(offset, bytes.len(), self.length)?;
        file_io::write_exact_at(&self.file, bytes, offset)
    }
}

fn require_fixed_io(offset: u64, length: usize, retained: u64) -> Result<()> {
    let end = offset
        .checked_add(length as u64)
        .ok_or(Error::ArithmeticOverflow("detached recovery scratch I/O"))?;
    if offset < HEADER_SIZE || end > retained {
        return Err(Error::Corrupt(
            "detached scratch I/O exceeds its fixed region",
        ));
    }
    Ok(())
}
