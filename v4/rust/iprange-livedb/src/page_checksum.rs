//! Commit-time CRC sealing for non-meta main-file pages.

use crate::contract::PAGE_SIZE;
use crate::crc32c;
use crate::error::{Error, Result};
use crate::slotted_page::put_u32;

const CRC_OFFSET: usize = 28;

pub(crate) fn seal(page: &mut [u8; PAGE_SIZE]) -> Result<()> {
    put_u32(page, CRC_OFFSET, 0);
    let checksum = crc32c::crc32c_with_zeroed(page, CRC_OFFSET, 4)
        .ok_or(Error::Corrupt("page checksum field is invalid"))?;
    put_u32(page, CRC_OFFSET, checksum);
    work::sealed();
    Ok(())
}

#[cfg(test)]
pub(crate) mod work {
    use std::cell::Cell;

    thread_local! {
        static SEALED: Cell<u64> = const { Cell::new(0) };
    }

    pub(super) fn sealed() {
        SEALED.set(SEALED.get() + 1);
    }

    pub(crate) fn reset() {
        SEALED.set(0);
    }

    pub(crate) fn count() -> u64 {
        SEALED.get()
    }
}

#[cfg(not(test))]
mod work {
    pub(super) fn sealed() {}
}
