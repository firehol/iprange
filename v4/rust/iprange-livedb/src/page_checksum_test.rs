use crate::contract::PAGE_SIZE;
use crate::slotted_page::put_u32;

use super::*;

pub(crate) fn seal(page: &mut [u8; PAGE_SIZE]) -> Result<()> {
    put_u32(page, OFFSET, 0);
    let checksum = crc32c::crc32c_with_zeroed(page, OFFSET, LENGTH)
        .ok_or(Error::Corrupt("page checksum field is invalid"))?;
    put_u32(page, OFFSET, checksum);
    crate::work::page_sealed(1);
    Ok(())
}
