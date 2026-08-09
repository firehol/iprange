//! Commit-time CRC sealing for non-meta main-file pages.

use crate::contract::u32_le;
use crate::crc32c;
use crate::error::{Error, Result};
use crate::mapping::{ByteSource, PageMut};
use crate::slotted_page::PageSink;

pub(crate) const OFFSET: usize = 28;
const LENGTH: usize = 4;

pub(crate) fn valid<S: ByteSource>(page: S) -> bool {
    crc32c::crc32c_source_with_zeroed(page, OFFSET, LENGTH) == Some(u32_le(page, OFFSET))
}

pub(crate) fn clear<D: PageSink + ?Sized>(page: &mut D) -> Result<()> {
    page.put_u32(OFFSET, 0)
}

pub(crate) fn seal_mapped(page: &mut PageMut<'_>) -> Result<()> {
    clear(page)?;
    let checksum = crc32c::crc32c_page_mut_with_zeroed(page, OFFSET, LENGTH)
        .ok_or(Error::Corrupt("page checksum field is invalid"))?;
    page.put_u32(OFFSET, checksum)?;
    crate::work::page_sealed(1);
    Ok(())
}

#[cfg(test)]
#[path = "page_checksum_test.rs"]
mod test_support;
#[cfg(test)]
pub(crate) use test_support::seal;
