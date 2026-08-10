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

    if let Some(pointer) = bytes.contiguous_ptr() {
        // SAFETY: `ByteSource::contiguous_ptr` covers `bytes.len()` bytes, the
        // zeroed extent was checked above, and the checksum loop creates no
        // reference into mapped storage. Validation/recovery workers contain a
        // mapped-file fault without unwinding through this code.
        return Some(unsafe {
            crc32c_pointer_with_zeroed(pointer, bytes.len(), zero_at, zero_len)
        });
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

unsafe fn crc32c_pointer_with_zeroed(
    pointer: *const u8,
    len: usize,
    zero_at: usize,
    zero_len: usize,
) -> u32 {
    let zero_end = zero_at + zero_len;
    // SAFETY: The caller checked the complete source and zeroed extents.
    let mut crc = unsafe { crc32c_pointer_append(0, pointer, zero_at) };
    let mut remaining = zero_len;
    while remaining >= ZEROES.len() {
        // SAFETY: `ZEROES` is a retained static array.
        crc = unsafe { crc32c_pointer_append(crc, ZEROES.as_ptr(), ZEROES.len()) };
        remaining -= ZEROES.len();
    }
    if remaining != 0 {
        // SAFETY: `remaining < ZEROES.len()`.
        crc = unsafe { crc32c_pointer_append(crc, ZEROES.as_ptr(), remaining) };
    }
    // SAFETY: `zero_end <= len` was established by the caller.
    unsafe { crc32c_pointer_append(crc, pointer.add(zero_end), len - zero_end) }
}

unsafe fn crc32c_pointer_append(crc: u32, pointer: *const u8, len: usize) -> u32 {
    #[cfg(all(target_arch = "x86_64", target_endian = "little"))]
    if std::arch::is_x86_feature_detected!("sse4.2") {
        // SAFETY: Runtime detection established SSE4.2 support and the caller
        // guarantees the raw input extent.
        return unsafe { crc32c_pointer_x86_64(crc, pointer, len) };
    }

    #[cfg(all(target_arch = "aarch64", target_endian = "little"))]
    if std::arch::is_aarch64_feature_detected!("crc") {
        // SAFETY: Runtime detection established CRC support and the caller
        // guarantees the raw input extent.
        return unsafe { crc32c_pointer_aarch64(crc, pointer, len) };
    }

    // SAFETY: The caller guarantees the complete raw input extent.
    unsafe { crc32c_pointer_table(crc, pointer, len) }
}

#[cfg(all(target_arch = "x86_64", target_endian = "little"))]
#[target_feature(enable = "sse4.2")]
// Rust 1.74 declares these target-feature intrinsics unsafe; current Rust does
// not. Keep one zero-cost source form that compiles under both signatures.
#[allow(unused_unsafe)]
unsafe fn crc32c_pointer_x86_64(mut crc: u32, mut pointer: *const u8, mut len: usize) -> u32 {
    use std::arch::x86_64::{_mm_crc32_u64, _mm_crc32_u8};

    let mut state = u64::from(!crc);
    while len >= 8 {
        // SAFETY: The caller guarantees `len` readable bytes; unaligned mapped
        // records are allowed.
        let word = unsafe { std::ptr::read_unaligned(pointer.cast::<u64>()) };
        state = unsafe { _mm_crc32_u64(state, word) };
        // SAFETY: Eight readable bytes were consumed above.
        pointer = unsafe { pointer.add(8) };
        len -= 8;
    }
    crc = !(state as u32);
    if len == 0 {
        return crc;
    }
    state = u64::from(!crc);
    while len != 0 {
        // SAFETY: At least one readable byte remains.
        state = u64::from(unsafe { _mm_crc32_u8(state as u32, std::ptr::read(pointer)) });
        // SAFETY: One readable byte was consumed above.
        pointer = unsafe { pointer.add(1) };
        len -= 1;
    }
    !(state as u32)
}

#[cfg(all(target_arch = "aarch64", target_endian = "little"))]
#[target_feature(enable = "crc")]
// Rust 1.74 declares these target-feature intrinsics unsafe; current Rust does
// not. Keep one zero-cost source form that compiles under both signatures.
#[allow(unused_unsafe)]
unsafe fn crc32c_pointer_aarch64(mut crc: u32, mut pointer: *const u8, mut len: usize) -> u32 {
    use std::arch::aarch64::{__crc32cb, __crc32cd};

    let mut state = !crc;
    while len >= 8 {
        // SAFETY: The caller guarantees `len` readable bytes; unaligned mapped
        // records are allowed.
        let word = unsafe { std::ptr::read_unaligned(pointer.cast::<u64>()) };
        state = unsafe { __crc32cd(state, word) };
        // SAFETY: Eight readable bytes were consumed above.
        pointer = unsafe { pointer.add(8) };
        len -= 8;
    }
    crc = !state;
    if len == 0 {
        return crc;
    }
    state = !crc;
    while len != 0 {
        // SAFETY: At least one readable byte remains.
        state = unsafe { __crc32cb(state, std::ptr::read(pointer)) };
        // SAFETY: One readable byte was consumed above.
        pointer = unsafe { pointer.add(1) };
        len -= 1;
    }
    !state
}

unsafe fn crc32c_pointer_table(crc: u32, pointer: *const u8, len: usize) -> u32 {
    let mut state = !crc;
    for index in 0..len {
        // SAFETY: The caller guarantees `len` readable bytes.
        let byte = unsafe { std::ptr::read(pointer.add(index)) };
        state = TABLE[((state ^ u32::from(byte)) & 0xff) as usize] ^ (state >> 8);
    }
    !state
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
