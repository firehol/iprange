//! Format constants from `binary-format-v3.md` — the single source of on-disk truth.
//!
//! Every value here is normative and was verified against the spec's byte-layout
//! tables. Changing one is a format change.

/// File magic, compared **bytewise** (8 ASCII bytes, endianness-independent, §5).
pub const MAGIC: [u8; 8] = *b"IPRANGE3";

/// Major version. A reader MUST reject any other major (§5/§12).
pub const VERSION_MAJOR: u16 = 3;
/// Minor version emitted by this crate (the v3.0 contract).
pub const VERSION_MINOR: u16 = 0;

/// Fixed header size for v3.0 (§5). For `version_minor == 0` it MUST be exactly this.
pub const HEADER_SIZE: u16 = 72;
/// Fixed directory-entry size, all of v3.x (§6).
pub const DIR_ENTRY_SIZE: usize = 72;
/// Index sub-header size (§9).
pub const INDEX_SUBHEADER_SIZE: usize = 32;

/// IPv4 index record size: `start(4) + end(4) + value_id(4)` (§9).
pub const V4_RECORD_SIZE: u32 = 12;
/// IPv6 index record size: `start(16) + end(16) + value_id(4) + pad(4)` (§9).
pub const V6_RECORD_SIZE: u32 = 40;

/// `value_id` sentinel meaning "present, no value" (§9). Caps the values table at
/// `0xFFFF_FFFE` distinct entries so it never collides with a real id (§10).
pub const VALUE_ID_NONE: u32 = 0xFFFF_FFFF;

/// `MaxUint64` = `2^64 − 1` (§3), used by the overflow-safe checks.
pub const MAX_U64: u64 = u64::MAX;

/// Header `flags` bit 0: IP version (0 = IPv4, 1 = IPv6). Bits 1–15 reserved = 0.
pub const FLAG_IP_VERSION: u16 = 0b1;

/// Directory-entry `flags` bit 0: `must_understand`. Bits 1–31 reserved = 0.
pub const DIR_FLAG_MUST_UNDERSTAND: u32 = 0b1;

/// Header `license_flags` bit 0: `dont_redistribute` (advisory in unsigned v3, §7).
pub const LICENSE_FLAG_DONT_REDISTRIBUTE: u32 = 0b1;

/// SHA-256 of the empty input — the hash of any zero-length section (§6). MUST NOT
/// be written as all-zeros.
pub const SHA256_EMPTY: [u8; 32] = [
    0xe3, 0xb0, 0xc4, 0x42, 0x98, 0xfc, 0x1c, 0x14, 0x9a, 0xfb, 0xf4, 0xc8, 0x99, 0x6f, 0xb9, 0x24,
    0x27, 0xae, 0x41, 0xe4, 0x64, 0x9b, 0x93, 0x4c, 0xa4, 0x95, 0x99, 0x1b, 0x78, 0x52, 0xb8, 0x55,
];

/// Section kinds (§8). The values, not the names, are the on-disk contract.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
#[repr(u32)]
pub enum SectionKind {
    /// kind 1 — identity/operational strings (align 8).
    FeedMeta = 1,
    /// kind 2 — the interval map (align 16; the hot path).
    Index = 2,
    /// kind 3 — interned value table (align 8; present iff used).
    Values = 3,
    /// kind 5 — reserved signature slot, empty in v3 (align 8); placed last.
    Signature = 5,
}

impl SectionKind {
    /// The numeric kind id as stored in a directory entry.
    pub const fn id(self) -> u32 {
        self as u32
    }

    /// The canonical alignment for this kind (§8). A reader MUST reject a mismatch
    /// for a known kind.
    pub const fn align(self) -> u64 {
        match self {
            SectionKind::Index => 16,
            SectionKind::FeedMeta | SectionKind::Values | SectionKind::Signature => 8,
        }
    }

    /// The fixed `flags` value for this known kind (§6): `must_understand = 1` for
    /// the required core sections (1/2/3), `0` for the signature (5). This removes
    /// the only writer freedom in the directory-entry bytes.
    pub const fn flags(self) -> u32 {
        match self {
            SectionKind::FeedMeta | SectionKind::Index | SectionKind::Values => {
                DIR_FLAG_MUST_UNDERSTAND
            }
            SectionKind::Signature => 0,
        }
    }

    /// Map a raw id to a known kind, if any (used by readers).
    pub const fn from_id(id: u32) -> Option<SectionKind> {
        match id {
            1 => Some(SectionKind::FeedMeta),
            2 => Some(SectionKind::Index),
            3 => Some(SectionKind::Values),
            5 => Some(SectionKind::Signature),
            _ => None,
        }
    }
}

/// IP family of a file (header `flags` bit 0).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum IpVersion {
    /// IPv4: 4-byte keys, 12-byte records.
    V4,
    /// IPv6: 16-byte keys, 40-byte records.
    V6,
}

impl IpVersion {
    /// Key width in bytes (4 or 16), as stored in the index sub-header (§9).
    pub const fn key_width(self) -> u32 {
        match self {
            IpVersion::V4 => 4,
            IpVersion::V6 => 16,
        }
    }

    /// Record size in bytes (12 or 40).
    pub const fn record_size(self) -> u32 {
        match self {
            IpVersion::V4 => V4_RECORD_SIZE,
            IpVersion::V6 => V6_RECORD_SIZE,
        }
    }

    /// Header `flags` bit for this family.
    pub const fn flag_bit(self) -> u16 {
        match self {
            IpVersion::V4 => 0,
            IpVersion::V6 => FLAG_IP_VERSION,
        }
    }
}

/// The feed-meta field count for v3.0 (§7): `name, category, maintainer,
/// maintainer_url, source_url, license`.
pub const FEED_META_FIELD_COUNT: u32 = 6;

/// `align_up(x, a)` for a power-of-two `a`, overflow-checked (§3). Returns `None`
/// if `x + (a-1)` would overflow `u64` — an unrepresentable layout.
#[inline]
pub const fn align_up(x: u64, a: u64) -> Option<u64> {
    // a is a power of two; a-1 is the mask. Check x <= MAX - (a-1) before adding.
    let m = a - 1;
    if x > MAX_U64 - m {
        None
    } else {
        Some((x + m) & !m)
    }
}

/// The set of valid `align` values (§6): powers of two in `8..=4096`.
#[inline]
pub const fn is_valid_align(a: u64) -> bool {
    a >= 8 && a <= 4096 && a.is_power_of_two()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sha256_empty_matches_library() {
        use sha2::{Digest, Sha256};
        let got = Sha256::digest([]);
        assert_eq!(got.as_slice(), &SHA256_EMPTY, "SHA-256(\"\") constant is wrong");
    }

    #[test]
    fn kind_align_and_flags() {
        assert_eq!(SectionKind::Index.align(), 16);
        assert_eq!(SectionKind::FeedMeta.align(), 8);
        assert_eq!(SectionKind::Index.flags(), DIR_FLAG_MUST_UNDERSTAND);
        assert_eq!(SectionKind::Signature.flags(), 0);
        assert_eq!(SectionKind::from_id(2), Some(SectionKind::Index));
        assert_eq!(SectionKind::from_id(4), None); // reserved
    }

    #[test]
    fn align_up_matches_spec_formula() {
        assert_eq!(align_up(0, 16), Some(0));
        assert_eq!(align_up(1, 16), Some(16));
        assert_eq!(align_up(16, 16), Some(16));
        assert_eq!(align_up(17, 8), Some(24));
        // overflow guard: prev_end > MAX - (align-1) is unrepresentable.
        assert_eq!(align_up(MAX_U64, 16), None);
        // MAX-15 (0xFFFF..F0) is already 16-aligned and is the largest representable
        // input (== MAX-(align-1)), so it maps to itself rather than overflowing.
        assert_eq!(align_up(MAX_U64 - 15, 16), Some(MAX_U64 - 15));
    }

    #[test]
    fn valid_align_set() {
        for a in [8u64, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096] {
            assert!(is_valid_align(a));
        }
        for a in [1u64, 2, 4, 8192, 3, 0] {
            assert!(!is_valid_align(a));
        }
    }

    #[test]
    fn record_sizes() {
        assert_eq!(IpVersion::V4.record_size(), 12);
        assert_eq!(IpVersion::V6.record_size(), 40);
        assert_eq!(IpVersion::V4.key_width(), 4);
        assert_eq!(IpVersion::V6.key_width(), 16);
    }
}
