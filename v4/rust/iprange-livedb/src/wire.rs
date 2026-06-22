//! Unaligned little-endian field access and the fixed page structures.
//!
//! Per D8 every multi-byte field is **little-endian, read/written by explicit byte
//! assembly** — never a packed-struct pointer cast over the mmap'd bytes (fields are
//! not guaranteed naturally aligned: meta `u64`s sit at offsets 58/66/74, `record_size`
//! at 38, branch keys follow a `u32` child pgno). This makes the layout identical on
//! every host and trivially mirrored in pure Go.
//!
//! This module is **pure (de)serialization** plus the page checksum finalize. It does
//! no validation: magic/version/geometry checks, meta selection, and the structural
//! walk are the reader's job (§5.1, §9), which reads untrusted bytes and rejects.

use crate::crc32c;
use crate::spec;

// --- unaligned little-endian primitives (D8) ---

#[inline]
pub(crate) fn u16_le(b: &[u8], at: usize) -> u16 {
    u16::from_le_bytes([b[at], b[at + 1]])
}
#[inline]
pub(crate) fn u32_le(b: &[u8], at: usize) -> u32 {
    u32::from_le_bytes([b[at], b[at + 1], b[at + 2], b[at + 3]])
}
#[inline]
pub(crate) fn u64_le(b: &[u8], at: usize) -> u64 {
    let mut x = [0u8; 8];
    x.copy_from_slice(&b[at..at + 8]);
    u64::from_le_bytes(x)
}
#[inline]
pub(crate) fn put_u16(b: &mut [u8], at: usize, v: u16) {
    b[at..at + 2].copy_from_slice(&v.to_le_bytes());
}
#[inline]
pub(crate) fn put_u32(b: &mut [u8], at: usize, v: u32) {
    b[at..at + 4].copy_from_slice(&v.to_le_bytes());
}
#[inline]
pub(crate) fn put_u64(b: &mut [u8], at: usize, v: u64) {
    b[at..at + 8].copy_from_slice(&v.to_le_bytes());
}

/// The common 16-byte page header (§5), present on every page. Pure bytes; the reader
/// validates (`page_type` known, `reserved == 0`, self-`pgno` match, checksum).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PageHeader {
    /// 1 = meta, 2 = branch, 3 = leaf.
    pub page_type: u8,
    /// MUST be 0 (reader rejects non-zero).
    pub reserved: u8,
    /// Records in a leaf / separators in a branch; 0 for a meta.
    pub entry_count: u16,
    /// This page's own number (reader verifies it matches the expected pgno).
    pub pgno: u32,
    /// D9 page checksum (whole page, this field zeroed).
    pub checksum: u64,
}

impl PageHeader {
    /// Parse the header from the first 16 bytes of a page. `page` MUST be >= 16 bytes.
    #[inline]
    pub fn decode(page: &[u8]) -> PageHeader {
        PageHeader {
            page_type: page[spec::PH_PAGE_TYPE],
            reserved: page[spec::PH_RESERVED],
            entry_count: u16_le(page, spec::PH_ENTRY_COUNT),
            pgno: u32_le(page, spec::PH_PGNO),
            checksum: u64_le(page, spec::PH_CHECKSUM),
        }
    }

    /// Write `page_type` / `reserved = 0` / `entry_count` / `pgno` into the header. The
    /// checksum is written separately by [`finalize_checksum`] after the body is
    /// filled (it covers the whole page).
    #[inline]
    pub fn write(page: &mut [u8], page_type: u8, entry_count: u16, pgno: u32) {
        page[spec::PH_PAGE_TYPE] = page_type;
        page[spec::PH_RESERVED] = 0;
        put_u16(page, spec::PH_ENTRY_COUNT, entry_count);
        put_u32(page, spec::PH_PGNO, pgno);
        // checksum field [8,16) is left zero until finalize_checksum.
    }
}

/// Compute the D9 checksum over the whole (already fully populated) page and write it
/// into the header checksum field. Call last, after every other byte is set.
#[inline]
pub fn finalize_checksum(page: &mut [u8]) {
    let sum = crc32c::page_checksum(page);
    put_u64(page, spec::PH_CHECKSUM, sum);
}

/// The meta page (pgno 0 / 1) — static identity + committed dynamic state (§5.1).
///
/// `magic` and `version_major` are implied constants ([`spec::MAGIC`],
/// [`spec::VERSION_MAJOR`]): [`Meta::encode_into`] writes them; [`Meta::decode`] does
/// **not** check them (the reader's bootstrap reads magic/version first to classify the
/// candidate, §5.1). The dynamic fields change every commit.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Meta {
    /// This meta's page number (0 or 1).
    pub pgno: u32,
    // --- static identity (identical in both metas) ---
    /// `version_minor`.
    pub version_minor: u16,
    /// `meta_size` (90 for v4.0).
    pub meta_size: u16,
    /// `page_size` (4096 for all v4).
    pub page_size: u32,
    /// `checksum_algo` (1 = CRC32C).
    pub checksum_algo: u8,
    /// `flags` (bit0 ip_version; bits 1-7 reserved = 0).
    pub flags: u8,
    /// `key_width` (4 or 16).
    pub key_width: u8,
    /// `scope_width` (0..=255).
    pub scope_width: u8,
    /// `record_size` (== `2·key_width + scope_width`).
    pub record_size: u32,
    /// `created_unixtime` (static).
    pub created_unixtime: u64,
    // --- dynamic state (per commit) ---
    /// `root_pgno` (0 = empty tree).
    pub root_pgno: u32,
    /// `tree_height` (0 = empty; leaf level = 1).
    pub tree_height: u32,
    /// `total_pages` (logical page count).
    pub total_pages: u64,
    /// `record_count` (unverified hint).
    pub record_count: u64,
    /// `txn_id` (monotonic; higher valid = active).
    pub txn_id: u64,
    /// `updated_unixtime` (caller-supplied per commit).
    pub updated_unixtime: u64,
}

impl Meta {
    /// Serialize into a full `PAGE_SIZE` page buffer: zero-fill, write the page header
    /// (`page_type = 1`, `entry_count = 0`, `pgno`), magic, `version_major`, every
    /// field at its §5.1 offset, then finalize the checksum. After this the page is a
    /// valid, checksummed meta. `page` MUST be exactly `PAGE_SIZE` bytes.
    pub fn encode_into(&self, page: &mut [u8]) {
        debug_assert_eq!(page.len(), spec::PAGE_SIZE);
        page.fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_META, 0, self.pgno);
        page[spec::META_MAGIC..spec::META_MAGIC + 8].copy_from_slice(&spec::MAGIC);
        put_u16(page, spec::META_VERSION_MAJOR, spec::VERSION_MAJOR);
        put_u16(page, spec::META_VERSION_MINOR, self.version_minor);
        put_u16(page, spec::META_META_SIZE, self.meta_size);
        put_u32(page, spec::META_PAGE_SIZE, self.page_size);
        page[spec::META_CHECKSUM_ALGO] = self.checksum_algo;
        page[spec::META_FLAGS] = self.flags;
        page[spec::META_KEY_WIDTH] = self.key_width;
        page[spec::META_SCOPE_WIDTH] = self.scope_width;
        put_u32(page, spec::META_RECORD_SIZE, self.record_size);
        put_u64(page, spec::META_CREATED_UNIXTIME, self.created_unixtime);
        put_u32(page, spec::META_ROOT_PGNO, self.root_pgno);
        put_u32(page, spec::META_TREE_HEIGHT, self.tree_height);
        put_u64(page, spec::META_TOTAL_PAGES, self.total_pages);
        put_u64(page, spec::META_RECORD_COUNT, self.record_count);
        put_u64(page, spec::META_TXN_ID, self.txn_id);
        put_u64(page, spec::META_UPDATED_UNIXTIME, self.updated_unixtime);
        finalize_checksum(page);
    }

    /// Parse the variable meta fields from a page (no validation of magic/version/
    /// geometry — the reader's bootstrap does that, §5.1). `page` MUST be >= 90 bytes.
    pub fn decode(page: &[u8]) -> Meta {
        Meta {
            pgno: u32_le(page, spec::PH_PGNO),
            version_minor: u16_le(page, spec::META_VERSION_MINOR),
            meta_size: u16_le(page, spec::META_META_SIZE),
            page_size: u32_le(page, spec::META_PAGE_SIZE),
            checksum_algo: page[spec::META_CHECKSUM_ALGO],
            flags: page[spec::META_FLAGS],
            key_width: page[spec::META_KEY_WIDTH],
            scope_width: page[spec::META_SCOPE_WIDTH],
            record_size: u32_le(page, spec::META_RECORD_SIZE),
            created_unixtime: u64_le(page, spec::META_CREATED_UNIXTIME),
            root_pgno: u32_le(page, spec::META_ROOT_PGNO),
            tree_height: u32_le(page, spec::META_TREE_HEIGHT),
            total_pages: u64_le(page, spec::META_TOTAL_PAGES),
            record_count: u64_le(page, spec::META_RECORD_COUNT),
            txn_id: u64_le(page, spec::META_TXN_ID),
            updated_unixtime: u64_le(page, spec::META_UPDATED_UNIXTIME),
        }
    }
}

/// Read the file `magic` from a page (`[16, 24)`). The bootstrap uses this before
/// trusting any other field (§5.1).
#[inline]
pub fn read_magic(page: &[u8]) -> [u8; 8] {
    let mut m = [0u8; 8];
    m.copy_from_slice(&page[spec::META_MAGIC..spec::META_MAGIC + 8]);
    m
}

/// Read `version_major` from a page (`[24, 26)`), used by bootstrap classification.
#[inline]
pub fn read_version_major(page: &[u8]) -> u16 {
    u16_le(page, spec::META_VERSION_MAJOR)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample_meta() -> Meta {
        Meta {
            pgno: 1,
            version_minor: 0,
            meta_size: spec::META_SIZE,
            page_size: spec::PAGE_SIZE as u32,
            checksum_algo: spec::CHECKSUM_ALGO_CRC32C,
            flags: spec::FLAG_IP_VERSION, // IPv6
            key_width: 16,
            scope_width: 4,
            record_size: spec::record_size(16, 4),
            created_unixtime: 0x1122_3344_5566_7788,
            root_pgno: 0x0A0B_0C0D,
            tree_height: 0x1112_1314,
            total_pages: 0x2122_2324_2526_2728,
            record_count: 0x3132_3334_3536_3738,
            txn_id: 0x4142_4344_4546_4748,
            updated_unixtime: 0x5152_5354_5556_5758,
        }
    }

    #[test]
    fn meta_round_trip_and_checksum() {
        let m = sample_meta();
        let mut page = [0u8; spec::PAGE_SIZE];
        m.encode_into(&mut page);
        assert!(crc32c::verify_page(&page), "encoded meta is self-consistent");
        assert_eq!(Meta::decode(&page), m, "round-trip");

        let h = PageHeader::decode(&page);
        assert_eq!(h.page_type, spec::PAGE_TYPE_META);
        assert_eq!(h.reserved, 0);
        assert_eq!(h.entry_count, 0, "meta entry_count is 0");
        assert_eq!(h.pgno, 1);
        assert_eq!(read_magic(&page), spec::MAGIC);
        assert_eq!(read_version_major(&page), spec::VERSION_MAJOR);
    }

    /// The correctness anchor: every field appears at its exact §5.1 byte offset, in
    /// little-endian. Distinct per-field sentinels make a wrong offset fail loudly.
    #[test]
    fn meta_field_byte_offsets_match_spec() {
        let m = sample_meta();
        let mut p = [0u8; spec::PAGE_SIZE];
        m.encode_into(&mut p);

        // common header
        assert_eq!(p[spec::PH_PAGE_TYPE], spec::PAGE_TYPE_META);
        assert_eq!(p[spec::PH_RESERVED], 0);
        assert_eq!(u16_le(&p, spec::PH_ENTRY_COUNT), 0);
        assert_eq!(u32_le(&p, spec::PH_PGNO), 1);
        // static identity
        assert_eq!(&p[spec::META_MAGIC..spec::META_MAGIC + 8], b"IPRANGE4");
        assert_eq!(u16_le(&p, spec::META_VERSION_MAJOR), 4);
        assert_eq!(u16_le(&p, spec::META_VERSION_MINOR), 0);
        assert_eq!(u16_le(&p, spec::META_META_SIZE), 90);
        assert_eq!(u32_le(&p, spec::META_PAGE_SIZE), 4096);
        assert_eq!(p[spec::META_CHECKSUM_ALGO], 1);
        assert_eq!(p[spec::META_FLAGS], spec::FLAG_IP_VERSION);
        assert_eq!(p[spec::META_KEY_WIDTH], 16);
        assert_eq!(p[spec::META_SCOPE_WIDTH], 4);
        assert_eq!(u32_le(&p, spec::META_RECORD_SIZE), 36);
        assert_eq!(u64_le(&p, spec::META_CREATED_UNIXTIME), 0x1122_3344_5566_7788);
        // dynamic state
        assert_eq!(u32_le(&p, spec::META_ROOT_PGNO), 0x0A0B_0C0D);
        assert_eq!(u32_le(&p, spec::META_TREE_HEIGHT), 0x1112_1314);
        assert_eq!(u64_le(&p, spec::META_TOTAL_PAGES), 0x2122_2324_2526_2728);
        assert_eq!(u64_le(&p, spec::META_RECORD_COUNT), 0x3132_3334_3536_3738);
        assert_eq!(u64_le(&p, spec::META_TXN_ID), 0x4142_4344_4546_4748);
        assert_eq!(u64_le(&p, spec::META_UPDATED_UNIXTIME), 0x5152_5354_5556_5758);

        // exact little-endian byte check for one multi-byte field (created_unixtime).
        assert_eq!(
            &p[spec::META_CREATED_UNIXTIME..spec::META_CREATED_UNIXTIME + 8],
            &[0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11]
        );
        // the region [meta_size, page_size) is reserved zero.
        assert!(p[spec::META_SIZE as usize..].iter().all(|&x| x == 0));
    }

    #[test]
    fn page_header_write_round_trip() {
        let mut p = [0u8; spec::PAGE_SIZE];
        PageHeader::write(&mut p, spec::PAGE_TYPE_LEAF, 7, 42);
        finalize_checksum(&mut p);
        let h = PageHeader::decode(&p);
        assert_eq!(h.page_type, spec::PAGE_TYPE_LEAF);
        assert_eq!(h.reserved, 0);
        assert_eq!(h.entry_count, 7);
        assert_eq!(h.pgno, 42);
        assert!(crc32c::verify_page(&p));
    }
}
