//! Exact unsigned Phase-1 v4 wire constants and meta-page codec.

use crate::crc32c;
use crate::error::Result;
use crate::mapping::{ByteSource, PageMut};
use crate::page_io::PageSink;

pub const PAGE_SIZE: usize = 4096;
pub const PAGE_SHIFT: u8 = 12;
pub const META_SIZE: u16 = 256;
pub const MAX_PAGE_COUNT: u64 = 1u64 << 32;
pub const MAX_TREE_LEVEL: u16 = 31;
pub const MAX_METADATA_UNCOMPRESSED: u64 = 20 * 1024 * 1024;
pub const META_MAGIC: [u8; 8] = *b"IPRANGE4";
pub const PAGE_MAGIC: [u8; 4] = *b"IP4P";

pub const META_CRC_OFFSET: usize = 252;

const META_MAGIC_OFFSET: usize = 0;
const META_SIZE_OFFSET: usize = 8;
const PAGE_SHIFT_OFFSET: usize = 10;
const ADDRESS_FAMILY_OFFSET: usize = 11;
const VALUE_KIND_OFFSET: usize = 12;
const STRUCTURE_KIND_OFFSET: usize = 13;
const VALUE_TAG_OFFSET: usize = 16;
const DATABASE_ID_OFFSET: usize = 32;
const TXN_ID_OFFSET: usize = 48;
const COMMIT_NONCE_OFFSET: usize = 56;
const PAGE_COUNT_OFFSET: usize = 72;
const RANGE_RECORD_COUNT_OFFSET: usize = 80;
const ACTIVE_FEED_COUNT_OFFSET: usize = 88;
const FEED_INDEX_LIMIT_OFFSET: usize = 96;
const MEMBERSHIP_ENTRY_COUNT_OFFSET: usize = 104;
const MEMBERSHIP_ID_LIMIT_OFFSET: usize = 112;
const METADATA_UNCOMPRESSED_LEN_OFFSET: usize = 120;
const METADATA_COMPRESSED_LEN_OFFSET: usize = 128;
const RETIRED_EXTENT_COUNT_OFFSET: usize = 136;
const RANGE_ROOT_OFFSET: usize = 144;
const CATALOG_NAME_ROOT_OFFSET: usize = 148;
const CATALOG_INDEX_ROOT_OFFSET: usize = 152;
const FEED_USED_ROOT_OFFSET: usize = 156;
const MEMBERSHIP_ID_ROOT_OFFSET: usize = 160;
const MEMBERSHIP_HASH_ROOT_OFFSET: usize = 164;
const MEMBERSHIP_USED_ROOT_OFFSET: usize = 168;
const METADATA_ROOT_OFFSET: usize = 172;
const FREE_BITMAP_ROOT_OFFSET: usize = 176;
const RETIREMENT_ROOT_OFFSET: usize = 180;
const ALLOCATOR_RESERVE_OFFSET: usize = 184;
const STRUCTURE_ENTRY_COUNT_OFFSET: usize = 200;
const STRUCTURE_ID_LIMIT_OFFSET: usize = 208;
const STRUCTURE_ID_ROOT_OFFSET: usize = 216;
const STRUCTURE_HASH_ROOT_OFFSET: usize = 220;
const STRUCTURE_USED_ROOT_OFFSET: usize = 224;
const RESERVED_HEADER_OFFSET: usize = 14;
const RESERVED_HEADER_LEN: usize = 2;
const RESERVED_BODY_OFFSET: usize = 228;

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
    Structured = 3,
}

/// Hardcoded structure selected by one structured database.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[repr(u8)]
pub enum StructureKind {
    None = 0,
    NetworkEnrichmentV1 = 1,
}

impl StructureKind {
    pub const fn from_wire(value: u8) -> Option<Self> {
        match value {
            0 => Some(Self::None),
            1 => Some(Self::NetworkEnrichmentV1),
            _ => None,
        }
    }
}

/// Engine-defined meaning of a direct database's immutable value tag.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[repr(u8)]
pub enum DirectSemantic {
    Generic = 1,
    FirstSeen = 2,
    LastSeen = 3,
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
            3 => Some(Self::Structured),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct ValueTag([u8; 16]);

impl ValueTag {
    pub const FIRST_SEEN: Self = Self(*b"first_seen\0\0\0\0\0\0");
    pub const LAST_SEEN: Self = Self(*b"last_seen\0\0\0\0\0\0\0");

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

    /// Classify an exact direct tag without changing its opaque wire identity.
    #[inline]
    pub fn direct_semantic(self) -> DirectSemantic {
        if self == Self::FIRST_SEEN {
            DirectSemantic::FirstSeen
        } else if self == Self::LAST_SEEN {
            DirectSemantic::LastSeen
        } else {
            DirectSemantic::Generic
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct MetaV4 {
    pub(crate) address_family: AddressFamily,
    pub(crate) value_kind: ValueKind,
    pub(crate) structure_kind_code: u8,
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
    pub(crate) structure_entry_count: u64,
    pub(crate) structure_id_limit: u64,
    pub(crate) structure_id_root: u32,
    pub(crate) structure_hash_root: u32,
    pub(crate) structure_used_root: u32,
}

impl MetaV4 {
    pub(crate) fn encode_mapped(&self, mut page: PageMut<'_>) -> Result<()> {
        self.encode_fields(&mut page)?;
        let crc = crc32c::crc32c_page_mut_with_zeroed(&page, META_CRC_OFFSET, 4)
            .expect("fixed meta CRC field");
        page.put_u32(META_CRC_OFFSET, crc)
    }

    fn encode_fields<D: PageSink + ?Sized>(&self, page: &mut D) -> Result<()> {
        page.fill(0);
        page.write(META_MAGIC_OFFSET, &META_MAGIC)?;
        page.put_u16(META_SIZE_OFFSET, META_SIZE)?;
        page.set_byte(PAGE_SHIFT_OFFSET, PAGE_SHIFT)?;
        page.set_byte(ADDRESS_FAMILY_OFFSET, self.address_family as u8)?;
        page.set_byte(VALUE_KIND_OFFSET, self.value_kind as u8)?;
        page.set_byte(STRUCTURE_KIND_OFFSET, self.structure_kind_code)?;
        page.write(VALUE_TAG_OFFSET, self.value_tag.as_wire())?;
        page.write(DATABASE_ID_OFFSET, &self.database_id)?;
        page.put_u64(TXN_ID_OFFSET, self.txn_id)?;
        page.write(COMMIT_NONCE_OFFSET, &self.commit_nonce)?;
        page.put_u64(PAGE_COUNT_OFFSET, self.page_count)?;
        page.put_u64(RANGE_RECORD_COUNT_OFFSET, self.range_record_count)?;
        page.put_u64(ACTIVE_FEED_COUNT_OFFSET, self.active_feed_count)?;
        page.put_u64(FEED_INDEX_LIMIT_OFFSET, self.feed_index_limit)?;
        page.put_u64(MEMBERSHIP_ENTRY_COUNT_OFFSET, self.membership_entry_count)?;
        page.put_u64(MEMBERSHIP_ID_LIMIT_OFFSET, self.membership_id_limit)?;
        page.put_u64(
            METADATA_UNCOMPRESSED_LEN_OFFSET,
            self.metadata_uncompressed_len,
        )?;
        page.put_u64(METADATA_COMPRESSED_LEN_OFFSET, self.metadata_compressed_len)?;
        page.put_u64(RETIRED_EXTENT_COUNT_OFFSET, self.retired_extent_count)?;
        page.put_u32(RANGE_ROOT_OFFSET, self.range_root)?;
        page.put_u32(CATALOG_NAME_ROOT_OFFSET, self.catalog_name_root)?;
        page.put_u32(CATALOG_INDEX_ROOT_OFFSET, self.catalog_index_root)?;
        page.put_u32(FEED_USED_ROOT_OFFSET, self.feed_used_root)?;
        page.put_u32(MEMBERSHIP_ID_ROOT_OFFSET, self.membership_id_root)?;
        page.put_u32(MEMBERSHIP_HASH_ROOT_OFFSET, self.membership_hash_root)?;
        page.put_u32(MEMBERSHIP_USED_ROOT_OFFSET, self.membership_used_root)?;
        page.put_u32(METADATA_ROOT_OFFSET, self.metadata_root)?;
        page.put_u32(FREE_BITMAP_ROOT_OFFSET, self.free_bitmap_root)?;
        page.put_u32(RETIREMENT_ROOT_OFFSET, self.retirement_root)?;
        for (index, page_number) in self.allocator_reserve.iter().enumerate() {
            page.put_u32(ALLOCATOR_RESERVE_OFFSET + index * 4, *page_number)?;
        }
        page.put_u64(STRUCTURE_ENTRY_COUNT_OFFSET, self.structure_entry_count)?;
        page.put_u64(STRUCTURE_ID_LIMIT_OFFSET, self.structure_id_limit)?;
        page.put_u32(STRUCTURE_ID_ROOT_OFFSET, self.structure_id_root)?;
        page.put_u32(STRUCTURE_HASH_ROOT_OFFSET, self.structure_hash_root)?;
        page.put_u32(STRUCTURE_USED_ROOT_OFFSET, self.structure_used_root)?;
        Ok(())
    }

    pub(crate) fn decode_unchecked<S: ByteSource>(page: S) -> Option<Self> {
        let tag = page.array(VALUE_TAG_OFFSET)?;
        let database_id = page.array(DATABASE_ID_OFFSET)?;
        let commit_nonce = page.array(COMMIT_NONCE_OFFSET)?;
        Some(Self {
            address_family: AddressFamily::from_wire(page.byte(ADDRESS_FAMILY_OFFSET)?)?,
            value_kind: ValueKind::from_wire(page.byte(VALUE_KIND_OFFSET)?)?,
            structure_kind_code: page.byte(STRUCTURE_KIND_OFFSET)?,
            value_tag: ValueTag::from_wire(tag)?,
            database_id,
            txn_id: u64_source(page, TXN_ID_OFFSET)?,
            commit_nonce,
            page_count: u64_source(page, PAGE_COUNT_OFFSET)?,
            range_record_count: u64_source(page, RANGE_RECORD_COUNT_OFFSET)?,
            active_feed_count: u64_source(page, ACTIVE_FEED_COUNT_OFFSET)?,
            feed_index_limit: u64_source(page, FEED_INDEX_LIMIT_OFFSET)?,
            membership_entry_count: u64_source(page, MEMBERSHIP_ENTRY_COUNT_OFFSET)?,
            membership_id_limit: u64_source(page, MEMBERSHIP_ID_LIMIT_OFFSET)?,
            metadata_uncompressed_len: u64_source(page, METADATA_UNCOMPRESSED_LEN_OFFSET)?,
            metadata_compressed_len: u64_source(page, METADATA_COMPRESSED_LEN_OFFSET)?,
            retired_extent_count: u64_source(page, RETIRED_EXTENT_COUNT_OFFSET)?,
            range_root: u32_source(page, RANGE_ROOT_OFFSET)?,
            catalog_name_root: u32_source(page, CATALOG_NAME_ROOT_OFFSET)?,
            catalog_index_root: u32_source(page, CATALOG_INDEX_ROOT_OFFSET)?,
            feed_used_root: u32_source(page, FEED_USED_ROOT_OFFSET)?,
            membership_id_root: u32_source(page, MEMBERSHIP_ID_ROOT_OFFSET)?,
            membership_hash_root: u32_source(page, MEMBERSHIP_HASH_ROOT_OFFSET)?,
            membership_used_root: u32_source(page, MEMBERSHIP_USED_ROOT_OFFSET)?,
            metadata_root: u32_source(page, METADATA_ROOT_OFFSET)?,
            free_bitmap_root: u32_source(page, FREE_BITMAP_ROOT_OFFSET)?,
            retirement_root: u32_source(page, RETIREMENT_ROOT_OFFSET)?,
            allocator_reserve: [
                u32_source(page, ALLOCATOR_RESERVE_OFFSET)?,
                u32_source(page, ALLOCATOR_RESERVE_OFFSET + 4)?,
                u32_source(page, ALLOCATOR_RESERVE_OFFSET + 8)?,
                u32_source(page, ALLOCATOR_RESERVE_OFFSET + 12)?,
            ],
            structure_entry_count: u64_source(page, STRUCTURE_ENTRY_COUNT_OFFSET)?,
            structure_id_limit: u64_source(page, STRUCTURE_ID_LIMIT_OFFSET)?,
            structure_id_root: u32_source(page, STRUCTURE_ID_ROOT_OFFSET)?,
            structure_hash_root: u32_source(page, STRUCTURE_HASH_ROOT_OFFSET)?,
            structure_used_root: u32_source(page, STRUCTURE_USED_ROOT_OFFSET)?,
        })
    }

    pub(crate) fn static_identity_eq(&self, other: &Self) -> bool {
        self.address_family == other.address_family
            && self.value_kind == other.value_kind
            && self.structure_kind_code == other.structure_kind_code
            && self.value_tag == other.value_tag
            && self.database_id == other.database_id
    }

    pub(crate) const fn roots(&self) -> [u32; 13] {
        [
            self.range_root,
            self.catalog_name_root,
            self.catalog_index_root,
            self.feed_used_root,
            self.membership_id_root,
            self.membership_hash_root,
            self.membership_used_root,
            self.metadata_root,
            self.free_bitmap_root,
            self.retirement_root,
            self.structure_id_root,
            self.structure_hash_root,
            self.structure_used_root,
        ]
    }

    pub(crate) const fn structure_kind(&self) -> Option<StructureKind> {
        StructureKind::from_wire(self.structure_kind_code)
    }
}

pub(crate) fn meta_magic_valid<S: ByteSource>(page: S) -> bool {
    page.len() == PAGE_SIZE && page.equals(META_MAGIC_OFFSET, &META_MAGIC)
}

pub(crate) fn meta_fixed_values_valid<S: ByteSource>(page: S) -> bool {
    u16_source(page, META_SIZE_OFFSET) == Some(META_SIZE)
        && page.byte(PAGE_SHIFT_OFFSET) == Some(PAGE_SHIFT)
        && page
            .byte(ADDRESS_FAMILY_OFFSET)
            .and_then(AddressFamily::from_wire)
            .is_some()
        && page
            .byte(VALUE_KIND_OFFSET)
            .and_then(ValueKind::from_wire)
            .is_some()
}

pub(crate) fn meta_reserved_zero<S: ByteSource>(page: S) -> bool {
    page.all_zero(RESERVED_HEADER_OFFSET, RESERVED_HEADER_LEN)
        && page.all_zero(RESERVED_BODY_OFFSET, META_CRC_OFFSET - RESERVED_BODY_OFFSET)
        && page.all_zero(META_SIZE as usize, PAGE_SIZE - META_SIZE as usize)
}

pub(crate) fn meta_checksum_valid<S: ByteSource>(page: S) -> bool {
    u32_source(page, META_CRC_OFFSET).is_some_and(|stored| {
        crc32c::crc32c_source_with_zeroed(page, META_CRC_OFFSET, 4) == Some(stored)
    })
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
#[path = "contract_tests.rs"]
mod tests;
