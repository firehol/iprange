//! Hardware-dispatched CRC-32C checksums used by v4 pages.

use crate::mapping::{ByteSource, PageMut};

const ZEROES: [u8; 64] = [0; 64];

pub(crate) fn crc32c_with_zeroed(bytes: &[u8], zero_at: usize, zero_len: usize) -> Option<u32> {
    let zero_end = zero_at.checked_add(zero_len)?;
    if zero_end > bytes.len() {
        return None;
    }

    let mut crc = ::crc32c::crc32c(&bytes[..zero_at]);
    let mut remaining = zero_len;
    while remaining >= ZEROES.len() {
        crc = ::crc32c::crc32c_append(crc, &ZEROES);
        remaining -= ZEROES.len();
    }
    if remaining != 0 {
        crc = ::crc32c::crc32c_append(crc, &ZEROES[..remaining]);
    }
    Some(::crc32c::crc32c_append(crc, &bytes[zero_end..]))
}

pub(crate) fn crc32c_source_with_zeroed<S: ByteSource>(
    bytes: S,
    zero_at: usize,
    zero_len: usize,
) -> Option<u32> {
    let zero_end = zero_at.checked_add(zero_len)?;
    if zero_end > bytes.len() {
        return None;
    }

    let mut crc = u32::MAX;
    for index in 0..bytes.len() {
        let byte = if (zero_at..zero_end).contains(&index) {
            0
        } else {
            bytes.byte(index)?
        };
        crc = TABLE[((crc ^ u32::from(byte)) & 0xff) as usize] ^ (crc >> 8);
    }
    Some(!crc)
}

pub(crate) fn crc32c_page_mut_with_zeroed(
    page: &PageMut<'_>,
    zero_at: usize,
    zero_len: usize,
) -> Option<u32> {
    // SAFETY: `PageMut` holds the exclusive mapping borrow for the complete
    // page, and this reference cannot escape the checksum call.
    let bytes = unsafe { std::slice::from_raw_parts(page.as_ptr(), crate::contract::PAGE_SIZE) };
    crc32c_with_zeroed(bytes, zero_at, zero_len)
}

const TABLE: [u32; 256] = crc_table();

const fn crc_table() -> [u32; 256] {
    const POLYNOMIAL: u32 = 0x82f6_3b78;

    let mut table: [u32; 256] = [0; 256];
    let mut index = 0;
    while index < 256 {
        let mut crc = index as u32;
        let mut bit = 0;
        while bit < 8 {
            crc = (crc >> 1) ^ (POLYNOMIAL & 0u32.wrapping_sub(crc & 1));
            bit += 1;
        }
        table[index] = crc;
        index += 1;
    }
    table
}

#[cfg(test)]
#[path = "checksum_tests.rs"]
mod tests;
