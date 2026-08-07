use crate::contract::PAGE_SIZE;
use crate::slotted_page::put_u32;

use super::*;

pub(crate) fn seal(page: &mut [u8; PAGE_SIZE]) -> Result<()> {
    put_u32(page, CRC_OFFSET, 0);
    let checksum = crc32c::crc32c_with_zeroed(page, CRC_OFFSET, 4)
        .ok_or(Error::Corrupt("page checksum field is invalid"))?;
    put_u32(page, CRC_OFFSET, checksum);
    work::sealed();
    Ok(())
}
