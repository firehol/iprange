//! Exact unsigned Phase-1 v4 wire constants and meta-page codec.

use crate::crc32c;
use crate::error::Result;
use crate::mapping::{ByteSource, PageMut};

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

/// One per-address membership operation.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum MembershipOperation {
    Replace,
    Union,
    Difference,
    Intersection,
    Xor,
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
    pub(crate) fn encode_mapped(&self, mut page: PageMut<'_>) -> Result<()> {
        page.fill(0);
        page.write(0, &META_MAGIC)?;
        page.put_u16(8, META_SIZE)?;
        page.set_byte(10, PAGE_SHIFT)?;
        page.set_byte(11, self.address_family as u8)?;
        page.set_byte(12, self.value_kind as u8)?;
        page.write(16, self.value_tag.as_wire())?;
        page.write(32, &self.database_id)?;
        page.put_u64(48, self.txn_id)?;
        page.write(56, &self.commit_nonce)?;
        page.put_u64(72, self.page_count)?;
        page.put_u64(80, self.range_record_count)?;
        page.put_u64(88, self.active_feed_count)?;
        page.put_u64(96, self.feed_index_limit)?;
        page.put_u64(104, self.membership_entry_count)?;
        page.put_u64(112, self.membership_id_limit)?;
        page.put_u64(120, self.metadata_uncompressed_len)?;
        page.put_u64(128, self.metadata_compressed_len)?;
        page.put_u64(136, self.retired_extent_count)?;
        page.put_u32(144, self.range_root)?;
        page.put_u32(148, self.catalog_name_root)?;
        page.put_u32(152, self.catalog_index_root)?;
        page.put_u32(156, self.feed_used_root)?;
        page.put_u32(160, self.membership_id_root)?;
        page.put_u32(164, self.membership_hash_root)?;
        page.put_u32(168, self.membership_used_root)?;
        page.put_u32(172, self.metadata_root)?;
        page.put_u32(176, self.free_bitmap_root)?;
        page.put_u32(180, self.retirement_root)?;
        for (index, page_number) in self.allocator_reserve.iter().enumerate() {
            page.put_u32(184 + index * 4, *page_number)?;
        }
        let crc = crc32c::crc32c_page_mut_with_zeroed(&page, META_CRC_OFFSET, 4)
            .expect("fixed meta CRC field");
        page.put_u32(META_CRC_OFFSET, crc)
    }

    pub(crate) fn decode_unchecked<S: ByteSource>(page: S) -> Option<Self> {
        let tag = page.array(16)?;
        let database_id = page.array(32)?;
        let commit_nonce = page.array(56)?;
        Some(Self {
            address_family: AddressFamily::from_wire(page.byte(11)?)?,
            value_kind: ValueKind::from_wire(page.byte(12)?)?,
            value_tag: ValueTag::from_wire(tag)?,
            database_id,
            txn_id: u64_source(page, 48)?,
            commit_nonce,
            page_count: u64_source(page, 72)?,
            range_record_count: u64_source(page, 80)?,
            active_feed_count: u64_source(page, 88)?,
            feed_index_limit: u64_source(page, 96)?,
            membership_entry_count: u64_source(page, 104)?,
            membership_id_limit: u64_source(page, 112)?,
            metadata_uncompressed_len: u64_source(page, 120)?,
            metadata_compressed_len: u64_source(page, 128)?,
            retired_extent_count: u64_source(page, 136)?,
            range_root: u32_source(page, 144)?,
            catalog_name_root: u32_source(page, 148)?,
            catalog_index_root: u32_source(page, 152)?,
            feed_used_root: u32_source(page, 156)?,
            membership_id_root: u32_source(page, 160)?,
            membership_hash_root: u32_source(page, 164)?,
            membership_used_root: u32_source(page, 168)?,
            metadata_root: u32_source(page, 172)?,
            free_bitmap_root: u32_source(page, 176)?,
            retirement_root: u32_source(page, 180)?,
            allocator_reserve: [
                u32_source(page, 184)?,
                u32_source(page, 188)?,
                u32_source(page, 192)?,
                u32_source(page, 196)?,
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

#[cfg(test)]
impl MetaV4 {
    pub(crate) fn encode_into(&self, page: &mut [u8; PAGE_SIZE]) {
        use crate::slotted_page::{put_u16, put_u32, put_u64};

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
}

#[inline]
pub(crate) fn u16_source<S: ByteSource>(bytes: S, at: usize) -> Option<u16> {
    Some(u16::from_le_bytes(bytes.array(at)?))
}

#[inline]
pub(crate) fn u32_source<S: ByteSource>(bytes: S, at: usize) -> Option<u32> {
    Some(u32::from_le_bytes(bytes.array(at)?))
}

#[inline]
pub(crate) fn u64_source<S: ByteSource>(bytes: S, at: usize) -> Option<u64> {
    Some(u64::from_le_bytes(bytes.array(at)?))
}

#[inline]
pub(crate) fn u16_le<S: ByteSource>(bytes: S, at: usize) -> u16 {
    u16_source(bytes, at).expect("validated u16 field")
}

#[inline]
pub(crate) fn u32_le<S: ByteSource>(bytes: S, at: usize) -> u32 {
    u32_source(bytes, at).expect("validated u32 field")
}

#[inline]
pub(crate) fn u64_le<S: ByteSource>(bytes: S, at: usize) -> u64 {
    u64_source(bytes, at).expect("validated u64 field")
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
