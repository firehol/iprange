//! Commit-time CRC sealing for non-meta main-file pages.

use crate::crc32c;
use crate::error::{Error, Result};
use crate::mapping::PageMut;

const CRC_OFFSET: usize = 28;

pub(crate) fn seal_mapped(page: &mut PageMut<'_>) -> Result<()> {
    page.put_u32(CRC_OFFSET, 0)?;
    let checksum = crc32c::crc32c_page_mut_with_zeroed(page, CRC_OFFSET, 4)
        .ok_or(Error::Corrupt("page checksum field is invalid"))?;
    page.put_u32(CRC_OFFSET, checksum)?;
    work::sealed();
    Ok(())
}

#[cfg(test)]
#[path = "page_checksum_test.rs"]
mod test_support;
#[cfg(test)]
pub(crate) use test_support::seal;

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
