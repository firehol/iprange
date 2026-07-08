//! CRC32C (Castagnoli) — the per-page corruption checksum (D9).
//!
//! Parameters (D9): reflected polynomial `0x82F63B78`, `init = xorout = 0xFFFF_FFFF`.
//! Test vector: `crc32c("123456789") == 0xE306_9283`.
//!
//! Hardware-accelerated on x86_64 (SSE4.2) and aarch64 (ARM CRC). On x86_64 the
//! serial `_mm_crc32_u64` instruction has 3-cycle latency but 1-cycle throughput.
//! For large buffers (>= 4032 bytes) we split the input into three chunks and CRC
//! them simultaneously — the three independent dependency chains pipeline at
//! 1 instruction/cycle, achieving ~3× the serial throughput. The partial CRCs
//! are combined via precomputed shift tables (the algorithm from Intel's
//! "Fast CRC Computation for iSCSI Polynomial Using CRC32 Instruction" paper,
//! also used by Go's `hash/crc32`). Falls back to software on unsupported CPUs.

#![allow(unsafe_op_in_unsafe_fn)]

use crate::spec::{PAGE_SIZE, PH_CHECKSUM};


const POLY: u32 = 0x82F6_3B78;
const INIT: u32 = 0xFFFF_FFFF;
const XOROUT: u32 = 0xFFFF_FFFF;

const TABLE: [u32; 256] = build_table();

const fn build_table() -> [u32; 256] {
    let mut table = [0u32; 256];
    let mut i = 0usize;
    while i < 256 {
        let mut crc = i as u32;
        let mut bit = 0;
        while bit < 8 {
            crc = if crc & 1 != 0 { (crc >> 1) ^ POLY } else { crc >> 1 };
            bit += 1;
        }
        table[i] = crc;
        i += 1;
    }
    table
}

#[inline]
fn update_soft(mut crc: u32, bytes: &[u8]) -> u32 {
    for &b in bytes {
        crc = (crc >> 8) ^ TABLE[((crc ^ b as u32) & 0xFF) as usize];
    }
    crc
}

// ---------------------------------------------------------------------------
// x86_64 hardware path
// ---------------------------------------------------------------------------

#[cfg(all(feature = "std", target_arch = "x86_64"))]
mod x86 {
    use core::arch::x86_64::*;

    /// Serial CRC over a byte slice (8 bytes/instruction, 3-cycle latency chain).
    #[target_feature(enable = "sse4.2")]
    pub unsafe fn crc_serial(mut crc: u32, bytes: &[u8]) -> u32 {
        let n8 = bytes.len() & !7;
        let mut crc64 = crc as u64;
        let mut i = 0;
        while i < n8 {
            let v = (bytes.as_ptr().add(i) as *const u64).read_unaligned();
            crc64 = _mm_crc32_u64(crc64, v);
            i += 8;
        }
        crc = crc64 as u32;
        if i + 4 <= bytes.len() {
            let v = (bytes.as_ptr().add(i) as *const u32).read_unaligned();
            crc = _mm_crc32_u32(crc, v);
            i += 4;
        }
        if i + 2 <= bytes.len() {
            let v = (bytes.as_ptr().add(i) as *const u16).read_unaligned();
            crc = _mm_crc32_u16(crc, v);
            i += 2;
        }
        while i < bytes.len() {
            crc = _mm_crc32_u8(crc, *bytes.get_unchecked(i));
            i += 1;
        }
        crc
    }

    pub const K2: usize = 1344;

    /// Three independent CRC chains over chunks a/b/c, each K2 bytes.
    /// The CPU pipelines the 3 CRC32 instructions per iteration (independent
    /// dependency chains), achieving 1 instruction/cycle instead of 1/3.
    #[target_feature(enable = "sse4.2")]
    pub unsafe fn crc_triple(
        mut ca: u32, a: *const u8,
        mut cb: u32, b: *const u8,
        mut cc: u32, c: *const u8,
    ) -> (u32, u32, u32) {
        let mut ca64 = ca as u64;
        let mut cb64 = cb as u64;
        let mut cc64 = cc as u64;
        let mut i = 0isize;
        while i < K2 as isize {
            let va = (a.offset(i) as *const u64).read_unaligned();
            ca64 = _mm_crc32_u64(ca64, va);
            let vb = (b.offset(i) as *const u64).read_unaligned();
            cb64 = _mm_crc32_u64(cb64, vb);
            let vc = (c.offset(i) as *const u64).read_unaligned();
            cc64 = _mm_crc32_u64(cc64, vc);
            i += 8;
        }
        ca = ca64 as u32;
        cb = cb64 as u32;
        cc = cc64 as u32;
        (ca, cb, cc)
    }

    /// Shift tables: CRC(byte_val << byte_pos, K2_zeros) for all 256 byte values,
    /// 4 byte positions. Used to combine the three parallel CRC chunks.
    pub struct ShiftTables {
        pub k2: [[u32; 256]; 4],
    }

    impl ShiftTables {
        pub fn compute() -> Self {
            let mut k2 = [[0u32; 256]; 4];
            let zeros = [0u8; K2];
            for (b, row) in k2.iter_mut().enumerate() {
                for i in 0..256u32 {
                    let val = i << (b * 8);
                    // SAFETY: called at init on a CPU known to have SSE4.2 (the
                    // dispatch layer checks before accessing the tables).
                    row[i as usize] = if is_x86_feature_detected!("sse4.2") {
                        unsafe { crc_serial(val, &zeros) }
                    } else {
                        super::update_soft(val, &zeros)
                    };
                }
            }
            ShiftTables { k2 }
        }

        #[inline]
        pub fn shift_k2(&self, crc: u32) -> u32 {
            self.k2[3][(crc >> 24) as usize]
                ^ self.k2[2][((crc >> 16) & 0xFF) as usize]
                ^ self.k2[1][((crc >> 8) & 0xFF) as usize]
                ^ self.k2[0][(crc & 0xFF) as usize]
        }
    }
}

#[cfg(all(feature = "std", target_arch = "x86_64"))]
use x86::*;

#[cfg(all(feature = "std", target_arch = "x86_64"))]
static TABLES: std::sync::OnceLock<ShiftTables> = std::sync::OnceLock::new();

#[cfg(all(feature = "std", target_arch = "x86_64"))]
fn get_tables() -> &'static ShiftTables {
    TABLES.get_or_init(ShiftTables::compute)
}

// ---------------------------------------------------------------------------
// aarch64 hardware path
// ---------------------------------------------------------------------------

#[cfg(all(feature = "std", target_arch = "aarch64"))]
#[target_feature(enable = "crc")]
unsafe fn crc_aarch64(mut crc: u32, bytes: &[u8]) -> u32 {
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
        let v = u32::from_le_bytes([bytes[i], bytes[i + 1], bytes[i + 2], bytes[i + 3]]);
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
// Dispatch
// ---------------------------------------------------------------------------

#[inline]
fn update(crc: u32, bytes: &[u8]) -> u32 {
    #[cfg(all(feature = "std", target_arch = "x86_64"))]
    {
        if is_x86_feature_detected!("sse4.2") {
            // SAFETY: guarded by runtime CPU detection.
            return unsafe { update_x86(crc, bytes) };
        }
    }
    #[cfg(all(feature = "std", target_arch = "aarch64"))]
    {
        if std::arch::is_aarch64_feature_detected!("crc") {
            return unsafe { crc_aarch64(crc, bytes) };
        }
    }
    update_soft(crc, bytes)
}

/// x86_64 path: triple-parallel for large buffers (>= 3*K2 = 4032), serial otherwise.
#[cfg(all(feature = "std", target_arch = "x86_64"))]
unsafe fn update_x86(crc: u32, bytes: &[u8]) -> u32 {
    let len = bytes.len();
    if len >= K2 * 3 {
        let tables = get_tables();

        // Align to 8 bytes.
        let (crc, aligned) = if (bytes.as_ptr() as usize) & 7 != 0 {
            let delta = 8 - ((bytes.as_ptr() as usize) & 7);
            let crc = crc_serial(crc, &bytes[..delta]);
            (crc, &bytes[delta..])
        } else {
            (crc, bytes)
        };

        let ptr = aligned.as_ptr();
        let remaining = aligned.len();
        let mut crc = crc;
        let mut off = 0usize;

        // Process 3*K2 at a time.
        while off + K2 * 3 <= remaining {
            let a = ptr.add(off);
            let b = ptr.add(off + K2);
            let c = ptr.add(off + K2 * 2);
            let (ca, cb, cc) = crc_triple(crc, a, 0, b, 0, c);
            let crc_ab = tables.shift_k2(ca) ^ cb;
            crc = tables.shift_k2(crc_ab) ^ cc;
            off += K2 * 3;
        }

        // Remaining bytes serially.
        if off < remaining {
            crc = crc_serial(crc, &aligned[off..]);
        }
        return crc;
    }
    crc_serial(crc, bytes)
}

#[inline]
pub fn crc32c(bytes: &[u8]) -> u32 {
    update(INIT, bytes) ^ XOROUT
}

#[inline]
pub fn page_checksum(page: &[u8]) -> u64 {
    debug_assert_eq!(page.len(), PAGE_SIZE);
    let crc = update(INIT, &page[..PH_CHECKSUM]);
    let crc = update(crc, &[0u8; 8]);
    let crc = update(crc, &page[PH_CHECKSUM + 8..]);
    (crc ^ XOROUT) as u64
}

#[inline]
pub fn verify_page(page: &[u8]) -> bool {
    let mut field = [0u8; 8];
    field.copy_from_slice(&page[PH_CHECKSUM..PH_CHECKSUM + 8]);
    let stored = u64::from_le_bytes(field);
    if stored >> 32 != 0 {
        return false;
    }
    page_checksum(page) == stored
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn crc32c_test_vector() {
        assert_eq!(crc32c(b"123456789"), 0xE306_9283);
    }

    #[test]
    fn crc32c_known_vectors() {
        assert_eq!(crc32c(b""), 0x0000_0000);
        assert_eq!(crc32c(&[0u8]), 0x527D_5351);
    }

    #[test]
    fn page_checksum_ignores_the_checksum_field() {
        let mut page = [0u8; PAGE_SIZE];
        for (i, b) in page.iter_mut().enumerate() {
            *b = (i % 251) as u8;
        }
        let sum = page_checksum(&page);
        page[PH_CHECKSUM..PH_CHECKSUM + 8].copy_from_slice(&sum.to_le_bytes());
        assert_eq!(page_checksum(&page), sum);
        assert!(verify_page(&page));
    }

    #[test]
    fn verify_rejects_corruption_and_nonzero_high_half() {
        let mut page = [7u8; PAGE_SIZE];
        let sum = page_checksum(&page);
        page[PH_CHECKSUM..PH_CHECKSUM + 8].copy_from_slice(&sum.to_le_bytes());
        assert!(verify_page(&page));
        let mut bad = page;
        bad[100] ^= 0x01;
        assert!(!verify_page(&bad));
        let mut hi = page;
        hi[PH_CHECKSUM + 4] = 0x01;
        assert!(!verify_page(&hi));
    }

    #[test]
    fn hw_matches_soft() {
        let patterns: &[&[u8]] = &[
            b"", b"\x00", b"123456789",
            &[0xFFu8; 1], &[0xFFu8; 7], &[0xFFu8; 8], &[0xFFu8; 9],
            &[0xFFu8; 15], &[0xFFu8; 16], &[0xFFu8; 17],
        ];
        for pat in patterns {
            for init in [INIT, 0u32, 0x1234_5678u32] {
                let soft = update_soft(init, pat);
                let hw = update(init, pat);
                assert_eq!(soft, hw, "len={} init={:#x}", pat.len(), init);
            }
        }
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

    #[test]
    fn triple_matches_serial() {
        let mut buf = vec![0u8; 8192];
        for (i, b) in buf.iter_mut().enumerate() {
            *b = (i % 251) as u8;
        }
        for len in [4032usize, 4096, 4097, 5000, 8064, 8192] {
            assert_eq!(
                update_soft(INIT, &buf[..len]),
                update(INIT, &buf[..len]),
                "triple-parallel len={len}"
            );
        }
    }
}
