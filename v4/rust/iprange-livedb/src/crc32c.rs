//! CRC32C (Castagnoli) — the per-page corruption checksum (D9).
//!
//! Parameters (D9): reflected polynomial `0x82F63B78` (normal `0x1EDC6F41`),
//! `init = 0xFFFF_FFFF`, `refin = refout = true`, `xorout = 0xFFFF_FFFF` — the
//! iSCSI/Intel CRC32C. Test vector: `crc32c("123456789") == 0xE306_9283`.
//!
//! Software, table-driven, `no_std`. A hardware-accelerated path (SSE4.2 / ARM CRC)
//! is a later, **measured** optimization (it must produce identical results); it is
//! deliberately absent here to keep the foundation portable and simple.

use crate::spec::{PAGE_SIZE, PH_CHECKSUM};

/// Reflected CRC32C polynomial (D9).
const POLY: u32 = 0x82F6_3B78;
const INIT: u32 = 0xFFFF_FFFF;
const XOROUT: u32 = 0xFFFF_FFFF;

/// The 256-entry lookup table, built at compile time (no runtime init, `no_std`-safe).
const TABLE: [u32; 256] = build_table();

const fn build_table() -> [u32; 256] {
    let mut table = [0u32; 256];
    let mut i = 0usize;
    while i < 256 {
        let mut crc = i as u32;
        let mut bit = 0;
        while bit < 8 {
            crc = if crc & 1 != 0 {
                (crc >> 1) ^ POLY
            } else {
                crc >> 1
            };
            bit += 1;
        }
        table[i] = crc;
        i += 1;
    }
    table
}

/// Fold `bytes` into a running CRC register (pre-xorout). Start from [`INIT`].
#[inline]
fn update(mut crc: u32, bytes: &[u8]) -> u32 {
    for &b in bytes {
        crc = (crc >> 8) ^ TABLE[((crc ^ b as u32) & 0xFF) as usize];
    }
    crc
}

/// CRC32C of `bytes` (full init + xorout).
#[inline]
pub fn crc32c(bytes: &[u8]) -> u32 {
    update(INIT, bytes) ^ XOROUT
}

/// The D9 page checksum value to store: CRC32C over all `PAGE_SIZE` bytes with the
/// 8-byte checksum field (`[8, 16)`) taken as **zero**, placed in the low 4 bytes of a
/// `u64` (the high 4 bytes are 0). `page` MUST be exactly `PAGE_SIZE` bytes.
#[inline]
pub fn page_checksum(page: &[u8]) -> u64 {
    debug_assert_eq!(page.len(), PAGE_SIZE);
    let crc = update(INIT, &page[..PH_CHECKSUM]); // [0, 8)
    let crc = update(crc, &[0u8; 8]); // checksum field as zero
    let crc = update(crc, &page[PH_CHECKSUM + 8..]); // [16, PAGE_SIZE)
    (crc ^ XOROUT) as u64 // high 32 bits are zero by construction
}

/// Verify a page against its stored checksum (D9), enforcing the high-32-bits-zero
/// rule: a reader MUST reject a non-zero high half. `page` MUST be `PAGE_SIZE` bytes.
#[inline]
pub fn verify_page(page: &[u8]) -> bool {
    let mut field = [0u8; 8];
    field.copy_from_slice(&page[PH_CHECKSUM..PH_CHECKSUM + 8]);
    let stored = u64::from_le_bytes(field);
    if stored >> 32 != 0 {
        return false; // high 4 bytes MUST be zero (D9)
    }
    page_checksum(page) == stored
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn crc32c_test_vector() {
        // D9 mandatory vector.
        assert_eq!(crc32c(b"123456789"), 0xE306_9283);
    }

    #[test]
    fn crc32c_known_vectors() {
        // Empty input -> 0 (init ^ xorout). And a couple of stable references.
        assert_eq!(crc32c(b""), 0x0000_0000);
        // Single zero byte.
        assert_eq!(crc32c(&[0u8]), 0x527D_5351);
    }

    #[test]
    fn page_checksum_ignores_the_checksum_field() {
        let mut page = [0u8; PAGE_SIZE];
        for (i, b) in page.iter_mut().enumerate() {
            *b = (i % 251) as u8;
        }
        let sum = page_checksum(&page);
        // Writing the computed sum into the field and recomputing must be stable, and
        // mutating ONLY the checksum field must not change the computed value.
        page[PH_CHECKSUM..PH_CHECKSUM + 8].copy_from_slice(&sum.to_le_bytes());
        assert_eq!(
            page_checksum(&page),
            sum,
            "checksum field excluded from the span"
        );
        assert!(verify_page(&page));
    }

    #[test]
    fn verify_rejects_corruption_and_nonzero_high_half() {
        let mut page = [7u8; PAGE_SIZE];
        let sum = page_checksum(&page);
        page[PH_CHECKSUM..PH_CHECKSUM + 8].copy_from_slice(&sum.to_le_bytes());
        assert!(verify_page(&page));

        // Flip a data byte -> reject.
        let mut bad = page;
        bad[100] ^= 0x01;
        assert!(!verify_page(&bad));

        // Set a high-half bit of the checksum field -> reject even though low 32 match.
        let mut hi = page;
        hi[PH_CHECKSUM + 4] = 0x01;
        assert!(!verify_page(&hi));
    }
}
