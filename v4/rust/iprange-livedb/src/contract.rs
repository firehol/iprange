//! Exact unsigned Phase-1 v4 wire constants and meta-page codec.

use crate::crc32c;

pub const PAGE_SIZE: usize = 4096;
pub const PAGE_SHIFT: u8 = 12;
pub const META_SIZE: u16 = 256;
pub const MAX_PAGE_COUNT: u64 = 1u64 << 32;
pub const MAX_TREE_LEVEL: u16 = 31;
pub const MAX_METADATA_UNCOMPRESSED: u64 = 1_048_576;
pub const META_MAGIC: [u8; 8] = *b"IPRANGE4";
pub const PAGE_MAGIC: [u8; 4] = *b"IP4P";

pub const META_CRC_OFFSET: usize = 252;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[repr(u8)]
pub enum AddressFamily {
    Ipv4 = 4,
    Ipv6 = 6,
}

impl AddressFamily {
    pub const fn from_wire(value: u8) -> Option<Self> {
        match value {
            4 => Some(Self::Ipv4),
            6 => Some(Self::Ipv6),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[repr(u8)]
pub enum ValueKind {
    Direct = 1,
    Membership = 2,
}

impl ValueKind {
    pub const fn from_wire(value: u8) -> Option<Self> {
        match value {
            1 => Some(Self::Direct),
            2 => Some(Self::Membership),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct ValueTag([u8; 16]);

impl ValueTag {
    pub const RETENTION: Self = Self(*b"retention\0\0\0\0\0\0\0");

    pub fn new(bytes: &[u8]) -> Option<Self> {
        if bytes.len() > 15 || bytes.contains(&0) {
            return None;
        }
        let mut storage = [0u8; 16];
        storage[..bytes.len()].copy_from_slice(bytes);
        Some(Self(storage))
    }

    pub fn from_wire(storage: [u8; 16]) -> Option<Self> {
        let nul = storage.iter().position(|&byte| byte == 0)?;
        if storage[nul..].iter().any(|&byte| byte != 0) {
            return None;
        }
        Some(Self(storage))
    }

    #[inline]
    pub const fn as_wire(&self) -> &[u8; 16] {
        &self.0
    }

    #[inline]
    pub fn bytes(&self) -> &[u8] {
        let len = self.0.iter().position(|&byte| byte == 0).unwrap_or(15);
        &self.0[..len]
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct MetaV4 {
    pub(crate) address_family: AddressFamily,
    pub(crate) value_kind: ValueKind,
    pub(crate) value_tag: ValueTag,
    pub(crate) database_id: [u8; 16],
    pub(crate) txn_id: u64,
    pub(crate) commit_nonce: [u8; 16],
    pub(crate) page_count: u64,
    pub(crate) range_record_count: u64,
    pub(crate) active_feed_count: u64,
    pub(crate) feed_index_limit: u64,
    pub(crate) membership_entry_count: u64,
    pub(crate) membership_id_limit: u64,
    pub(crate) metadata_uncompressed_len: u64,
    pub(crate) metadata_compressed_len: u64,
    pub(crate) retired_extent_count: u64,
    pub(crate) range_root: u32,
    pub(crate) catalog_name_root: u32,
    pub(crate) catalog_index_root: u32,
    pub(crate) feed_used_root: u32,
    pub(crate) membership_id_root: u32,
    pub(crate) membership_hash_root: u32,
    pub(crate) membership_used_root: u32,
    pub(crate) metadata_root: u32,
    pub(crate) free_bitmap_root: u32,
    pub(crate) retirement_root: u32,
    pub(crate) allocator_reserve: [u32; 4],
}

impl MetaV4 {
    pub(crate) fn encode_into(&self, page: &mut [u8; PAGE_SIZE]) {
        page.fill(0);
        page[0..8].copy_from_slice(&META_MAGIC);
        put_u16(page, 8, META_SIZE);
        page[10] = PAGE_SHIFT;
        page[11] = self.address_family as u8;
        page[12] = self.value_kind as u8;
        page[16..32].copy_from_slice(self.value_tag.as_wire());
        page[32..48].copy_from_slice(&self.database_id);
        put_u64(page, 48, self.txn_id);
        page[56..72].copy_from_slice(&self.commit_nonce);
        put_u64(page, 72, self.page_count);
        put_u64(page, 80, self.range_record_count);
        put_u64(page, 88, self.active_feed_count);
        put_u64(page, 96, self.feed_index_limit);
        put_u64(page, 104, self.membership_entry_count);
        put_u64(page, 112, self.membership_id_limit);
        put_u64(page, 120, self.metadata_uncompressed_len);
        put_u64(page, 128, self.metadata_compressed_len);
        put_u64(page, 136, self.retired_extent_count);
        put_u32(page, 144, self.range_root);
        put_u32(page, 148, self.catalog_name_root);
        put_u32(page, 152, self.catalog_index_root);
        put_u32(page, 156, self.feed_used_root);
        put_u32(page, 160, self.membership_id_root);
        put_u32(page, 164, self.membership_hash_root);
        put_u32(page, 168, self.membership_used_root);
        put_u32(page, 172, self.metadata_root);
        put_u32(page, 176, self.free_bitmap_root);
        put_u32(page, 180, self.retirement_root);
        for (index, page_number) in self.allocator_reserve.iter().enumerate() {
            put_u32(page, 184 + index * 4, *page_number);
        }
        let crc = crc32c::crc32c_with_zeroed(page, META_CRC_OFFSET, 4).unwrap();
        put_u32(page, META_CRC_OFFSET, crc);
    }

    pub(crate) fn decode_unchecked(page: &[u8; PAGE_SIZE]) -> Option<Self> {
        let mut tag = [0u8; 16];
        tag.copy_from_slice(&page[16..32]);
        let mut database_id = [0u8; 16];
        database_id.copy_from_slice(&page[32..48]);
        let mut commit_nonce = [0u8; 16];
        commit_nonce.copy_from_slice(&page[56..72]);
        Some(Self {
            address_family: AddressFamily::from_wire(page[11])?,
            value_kind: ValueKind::from_wire(page[12])?,
            value_tag: ValueTag::from_wire(tag)?,
            database_id,
            txn_id: u64_le(page, 48),
            commit_nonce,
            page_count: u64_le(page, 72),
            range_record_count: u64_le(page, 80),
            active_feed_count: u64_le(page, 88),
            feed_index_limit: u64_le(page, 96),
            membership_entry_count: u64_le(page, 104),
            membership_id_limit: u64_le(page, 112),
            metadata_uncompressed_len: u64_le(page, 120),
            metadata_compressed_len: u64_le(page, 128),
            retired_extent_count: u64_le(page, 136),
            range_root: u32_le(page, 144),
            catalog_name_root: u32_le(page, 148),
            catalog_index_root: u32_le(page, 152),
            feed_used_root: u32_le(page, 156),
            membership_id_root: u32_le(page, 160),
            membership_hash_root: u32_le(page, 164),
            membership_used_root: u32_le(page, 168),
            metadata_root: u32_le(page, 172),
            free_bitmap_root: u32_le(page, 176),
            retirement_root: u32_le(page, 180),
            allocator_reserve: [
                u32_le(page, 184),
                u32_le(page, 188),
                u32_le(page, 192),
                u32_le(page, 196),
            ],
        })
    }

    pub(crate) fn static_identity_eq(&self, other: &Self) -> bool {
        self.address_family == other.address_family
            && self.value_kind == other.value_kind
            && self.value_tag == other.value_tag
            && self.database_id == other.database_id
    }
}

#[inline]
pub(crate) fn u16_le(bytes: &[u8], at: usize) -> u16 {
    u16::from_le_bytes([bytes[at], bytes[at + 1]])
}

#[inline]
pub(crate) fn u32_le(bytes: &[u8], at: usize) -> u32 {
    u32::from_le_bytes(bytes[at..at + 4].try_into().unwrap())
}

#[inline]
pub(crate) fn u64_le(bytes: &[u8], at: usize) -> u64 {
    u64::from_le_bytes(bytes[at..at + 8].try_into().unwrap())
}

#[inline]
fn put_u16(bytes: &mut [u8], at: usize, value: u16) {
    bytes[at..at + 2].copy_from_slice(&value.to_le_bytes());
}

#[inline]
fn put_u32(bytes: &mut [u8], at: usize, value: u32) {
    bytes[at..at + 4].copy_from_slice(&value.to_le_bytes());
}

#[inline]
fn put_u64(bytes: &mut [u8], at: usize, value: u64) {
    bytes[at..at + 8].copy_from_slice(&value.to_le_bytes());
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tag_is_canonical_and_retention_is_exact() {
        assert_eq!(ValueTag::RETENTION.bytes(), b"retention");
        assert!(ValueTag::new(b"123456789012345").is_some());
        assert!(ValueTag::new(b"1234567890123456").is_none());
        assert!(ValueTag::new(b"bad\0tag").is_none());
        assert!(ValueTag::from_wire(*b"bad\0x\0\0\0\0\0\0\0\0\0\0\0").is_none());
    }

    #[test]
    fn exact_meta_offsets_and_crc() {
        let meta = super::super::bootstrap::tests::empty_direct_meta(7);
        let mut page = [0u8; PAGE_SIZE];
        meta.encode_into(&mut page);
        assert_eq!(&page[0..8], b"IPRANGE4");
        assert_eq!(u16_le(&page, 8), 256);
        assert_eq!(page[10], 12);
        assert_eq!(page[11], 4);
        assert_eq!(page[12], 1);
        assert_eq!(u64_le(&page, 48), 7);
        assert_eq!(u64_le(&page, 72), 2);
        assert_eq!(
            u32_le(&page, META_CRC_OFFSET),
            crc32c::crc32c_with_zeroed(&page, META_CRC_OFFSET, 4).unwrap()
        );
        assert!(page[256..].iter().all(|&byte| byte == 0));
    }
}
