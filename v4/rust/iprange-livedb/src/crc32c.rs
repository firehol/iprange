//! CRC32C (Castagnoli) — the per-page corruption checksum (D9).
//!
//! Parameters (D9): reflected polynomial `0x82F63B78` (normal `0x1EDC6F41`),
//! `init = 0xFFFF_FFFF`, `refin = refout = true`, `xorout = 0xFFFF_FFFF` — the
//! iSCSI/Intel CRC32C. Test vector: `crc32c("123456789") == 0xE306_9283`.
//!
//! Hardware-accelerated on x86_64 (SSE4.2 `crc32` instruction) and aarch64 (ARM
//! CRC extension), with runtime CPU detection. Falls back to a portable
//! table-driven software path on unsupported CPUs or `no_std` builds. Both paths
//! produce identical results (cross-checked by `hw_matches_soft`).

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

// ---------------------------------------------------------------------------
// Software path (portable, no_std-safe, table-driven, 1 byte/iteration).
// ---------------------------------------------------------------------------

/// Fold `bytes` into a running CRC register (pre-xorout). Start from [`INIT`].
#[inline]
fn update_soft(mut crc: u32, bytes: &[u8]) -> u32 {
    for &b in bytes {
        crc = (crc >> 8) ^ TABLE[((crc ^ b as u32) & 0xFF) as usize];
    }
    crc
}

// ---------------------------------------------------------------------------
// x86_64 hardware path (SSE4.2, 8 bytes/iteration).
// ---------------------------------------------------------------------------

#[cfg(all(feature = "std", target_arch = "x86_64"))]
#[target_feature(enable = "sse4.2")]
unsafe fn update_sse42(mut crc: u32, bytes: &[u8]) -> u32 {
    use core::arch::x86_64::*;

    // Process 8 bytes at a time. x86 allows unaligned reads; on modern CPUs the
    // CRC instruction has no alignment requirement.
    let n8 = bytes.len() & !7;
    let mut crc64 = crc as u64;
    let mut i = 0;
    while i < n8 {
        let v = u64::from_le_bytes([
            bytes[i], bytes[i + 1], bytes[i + 2], bytes[i + 3],
            bytes[i + 4], bytes[i + 5], bytes[i + 6], bytes[i + 7],
        ]);
        crc64 = _mm_crc32_u64(crc64, v);
        i += 8;
    }
    crc = crc64 as u32;

    // Handle remaining 0-7 bytes: 4 → 2 → 1.
    if i + 4 <= bytes.len() {
        let v = u32::from_le_bytes([
            bytes[i], bytes[i + 1], bytes[i + 2], bytes[i + 3],
        ]);
        crc = _mm_crc32_u32(crc, v);
        i += 4;
    }
    if i + 2 <= bytes.len() {
        let v = u16::from_le_bytes([bytes[i], bytes[i + 1]]);
        crc = _mm_crc32_u16(crc, v);
        i += 2;
    }
    while i < bytes.len() {
        crc = _mm_crc32_u8(crc, bytes[i]);
        i += 1;
    }
    crc
}

// ---------------------------------------------------------------------------
// aarch64 hardware path (ARM CRC extension, 8 bytes/iteration).
// ---------------------------------------------------------------------------

#[cfg(all(feature = "std", target_arch = "aarch64"))]
#[target_feature(enable = "crc")]
unsafe fn update_aarch64(mut crc: u32, bytes: &[u8]) -> u32 {
    use core::arch::aarch64::*;

    let n8 = bytes.len() & !7;
    let mut i = 0;
    while i < n8 {
        let v = u64::from_le_bytes([
            bytes[i], bytes[i + 1], bytes[i + 2], bytes[i + 3],
            bytes[i + 4], bytes[i + 5], bytes[i + 6], bytes[i + 7],
        ]);
        crc = __crc32cd(crc, v);
        i += 8;
    }

    if i + 4 <= bytes.len() {
        let v = u32::from_le_bytes([
            bytes[i], bytes[i + 1], bytes[i + 2], bytes[i + 3],
        ]);
        crc = __crc32cw(crc, v);
        i += 4;
    }
    if i + 2 <= bytes.len() {
        let v = u16::from_le_bytes([bytes[i], bytes[i + 1]]) as u32;
        crc = __crc32ch(crc, v);
        i += 2;
    }
    while i < bytes.len() {
        crc = __crc32cb(crc, bytes[i]);
        i += 1;
    }
    crc
}

// ---------------------------------------------------------------------------
// Dispatch: hardware if available (std + CPU), software otherwise.
// ---------------------------------------------------------------------------

/// Fold `bytes` into a running CRC register (pre-xorout). Start from [`INIT`].
/// Dispatches to the hardware-accelerated path when the CPU supports it.
#[inline]
fn update(crc: u32, bytes: &[u8]) -> u32 {
    #[cfg(all(feature = "std", target_arch = "x86_64"))]
    {
        if is_x86_feature_detected!("sse4.2") {
            // SAFETY: guarded by runtime CPU detection.
            return unsafe { update_sse42(crc, bytes) };
        }
    }
    #[cfg(all(feature = "std", target_arch = "aarch64"))]
    {
        if std::arch::is_aarch64_feature_detected!("crc") {
            // SAFETY: guarded by runtime CPU detection.
            return unsafe { update_aarch64(crc, bytes) };
        }
    }
    update_soft(crc, bytes)
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

    /// The hardware and software paths MUST produce identical results. This runs
    /// both on a variety of lengths (including all tail-remainder sizes 0..7) and
    /// data patterns so a dispatch bug cannot hide behind a lucky length alignment.
    #[test]
    fn hw_matches_soft() {
        let patterns: &[&[u8]] = &[
            b"",
            b"\x00",
            b"123456789",
            &[0xFFu8; 1],
            &[0xFFu8; 7],
            &[0xFFu8; 8],
            &[0xFFu8; 9],
            &[0xFFu8; 15],
            &[0xFFu8; 16],
            &[0xFFu8; 17],
        ];
        for pat in patterns {
            for init in [INIT, 0u32, 0x1234_5678u32] {
                let soft = update_soft(init, pat);
                let hw = update(init, pat);
                assert_eq!(soft, hw, "len={} init={:#x}", pat.len(), init);
            }
        }

        // A full page (the production hot path).
        let mut page = [0u8; PAGE_SIZE];
        for (i, b) in page.iter_mut().enumerate() {
            *b = (i % 251) as u8;
        }
        assert_eq!(update_soft(INIT, &page), update(INIT, &page), "full page");

        // Run-length sweeps across all 0..8 tail sizes.
        let mut buf = vec![0u8; PAGE_SIZE + 64];
        for len in 0..=PAGE_SIZE + 64 {
            for (i, b) in buf[..len].iter_mut().enumerate() {
                *b = (i % 251) as u8;
            }
            assert_eq!(
                update_soft(INIT, &buf[..len]),
                update(INIT, &buf[..len]),
                "run-length len={len}"
            );
        }
    }
}
