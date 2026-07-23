//! Exact v4 common non-meta page header codec.

use crate::contract::{u16_le, u32_le, u64_le, MAX_TREE_LEVEL, PAGE_MAGIC, PAGE_SIZE};
use crate::crc32c;

pub(crate) const PAGE_HEADER_SIZE: u16 = 32;
pub(crate) const PAGE_CRC_OFFSET: usize = 28;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[repr(u8)]
pub(crate) enum PageType {
    RangeBranch = 1,
    RangeLeaf = 2,
    CatalogNameBranch = 3,
    CatalogNameLeaf = 4,
    CatalogIndexBranch = 5,
    CatalogIndexLeaf = 6,
    MembershipIdBranch = 7,
    MembershipIdLeaf = 8,
    MembershipHashBranch = 9,
    MembershipHashLeaf = 10,
    BlobBranch = 11,
    BlobLeaf = 12,
    MetadataChunk = 13,
    BitmapBranch = 14,
    BitmapLeaf = 15,
    RetirementBranch = 16,
    RetirementLeaf = 17,
}

impl PageType {
    pub(crate) const fn from_wire(value: u8) -> Option<Self> {
        match value {
            1 => Some(Self::RangeBranch),
            2 => Some(Self::RangeLeaf),
            3 => Some(Self::CatalogNameBranch),
            4 => Some(Self::CatalogNameLeaf),
            5 => Some(Self::CatalogIndexBranch),
            6 => Some(Self::CatalogIndexLeaf),
            7 => Some(Self::MembershipIdBranch),
            8 => Some(Self::MembershipIdLeaf),
            9 => Some(Self::MembershipHashBranch),
            10 => Some(Self::MembershipHashLeaf),
            11 => Some(Self::BlobBranch),
            12 => Some(Self::BlobLeaf),
            13 => Some(Self::MetadataChunk),
            14 => Some(Self::BitmapBranch),
            15 => Some(Self::BitmapLeaf),
            16 => Some(Self::RetirementBranch),
            17 => Some(Self::RetirementLeaf),
            _ => None,
        }
    }

    pub(crate) const fn is_branch(self) -> bool {
        matches!(
            self,
            Self::RangeBranch
                | Self::CatalogNameBranch
                | Self::CatalogIndexBranch
                | Self::MembershipIdBranch
                | Self::MembershipHashBranch
                | Self::BlobBranch
                | Self::BitmapBranch
                | Self::RetirementBranch
        )
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PageHeader {
    pub(crate) page_type: PageType,
    pub(crate) born_txn: u64,
    pub(crate) item_count: u16,
    pub(crate) level: u16,
    pub(crate) lower: u16,
    pub(crate) upper: u16,
    pub(crate) aux: u32,
    pub(crate) page_crc32c: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PageHeaderError {
    Magic,
    PageType(u8),
    Flags(u8),
    HeaderSize(u16),
    BornTransactionZero,
    BornTransactionFuture { born_txn: u64, selected_txn: u64 },
    LevelTooHigh(u16),
    BranchLevelZero(PageType),
    NonBranchLevelNonzero { page_type: PageType, level: u16 },
    Bounds { lower: u16, upper: u16 },
}

impl PageHeader {
    /// Decode and check the ordinary-path structural invariants.
    ///
    /// This deliberately does not verify the page CRC. Explicit validation and
    /// recovery call [`verify_crc32c`] separately.
    pub(crate) fn decode(
        page: &[u8; PAGE_SIZE],
        selected_txn: u64,
    ) -> Result<Self, PageHeaderError> {
        if page[0..4] != PAGE_MAGIC {
            return Err(PageHeaderError::Magic);
        }
        let page_type = PageType::from_wire(page[4]).ok_or(PageHeaderError::PageType(page[4]))?;
        if page[5] != 0 {
            return Err(PageHeaderError::Flags(page[5]));
        }
        let header_size = u16_le(page, 6);
        if header_size != PAGE_HEADER_SIZE {
            return Err(PageHeaderError::HeaderSize(header_size));
        }

        let born_txn = u64_le(page, 8);
        if born_txn == 0 {
            return Err(PageHeaderError::BornTransactionZero);
        }
        if born_txn > selected_txn {
            return Err(PageHeaderError::BornTransactionFuture {
                born_txn,
                selected_txn,
            });
        }

        let level = u16_le(page, 18);
        if level > MAX_TREE_LEVEL {
            return Err(PageHeaderError::LevelTooHigh(level));
        }
        if page_type.is_branch() {
            if level == 0 {
                return Err(PageHeaderError::BranchLevelZero(page_type));
            }
        } else if level != 0 {
            return Err(PageHeaderError::NonBranchLevelNonzero { page_type, level });
        }

        let lower = u16_le(page, 20);
        let upper = u16_le(page, 22);
        if lower < PAGE_HEADER_SIZE || lower > upper || usize::from(upper) > PAGE_SIZE {
            return Err(PageHeaderError::Bounds { lower, upper });
        }

        Ok(Self {
            page_type,
            born_txn,
            item_count: u16_le(page, 16),
            level,
            lower,
            upper,
            aux: u32_le(page, 24),
            page_crc32c: u32_le(page, PAGE_CRC_OFFSET),
        })
    }

    /// Write the exact common header. The caller owns all type-specific body
    /// bytes and seals the completed page with [`write_crc32c`].
    pub(crate) fn encode_into(&self, page: &mut [u8; PAGE_SIZE]) {
        page[0..4].copy_from_slice(&PAGE_MAGIC);
        page[4] = self.page_type as u8;
        page[5] = 0;
        put_u16(page, 6, PAGE_HEADER_SIZE);
        put_u64(page, 8, self.born_txn);
        put_u16(page, 16, self.item_count);
        put_u16(page, 18, self.level);
        put_u16(page, 20, self.lower);
        put_u16(page, 22, self.upper);
        put_u32(page, 24, self.aux);
        put_u32(page, PAGE_CRC_OFFSET, self.page_crc32c);
    }
}

#[inline]
pub(crate) fn verify_crc32c(page: &[u8; PAGE_SIZE]) -> bool {
    let stored = u32_le(page, PAGE_CRC_OFFSET);
    crc32c::crc32c_with_zeroed(page, PAGE_CRC_OFFSET, 4) == Some(stored)
}

#[inline]
pub(crate) fn write_crc32c(page: &mut [u8; PAGE_SIZE]) -> u32 {
    let checksum = crc32c::crc32c_with_zeroed(page, PAGE_CRC_OFFSET, 4).unwrap();
    put_u32(page, PAGE_CRC_OFFSET, checksum);
    checksum
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

    const ALL_PAGE_TYPES: [PageType; 17] = [
        PageType::RangeBranch,
        PageType::RangeLeaf,
        PageType::CatalogNameBranch,
        PageType::CatalogNameLeaf,
        PageType::CatalogIndexBranch,
        PageType::CatalogIndexLeaf,
        PageType::MembershipIdBranch,
        PageType::MembershipIdLeaf,
        PageType::MembershipHashBranch,
        PageType::MembershipHashLeaf,
        PageType::BlobBranch,
        PageType::BlobLeaf,
        PageType::MetadataChunk,
        PageType::BitmapBranch,
        PageType::BitmapLeaf,
        PageType::RetirementBranch,
        PageType::RetirementLeaf,
    ];

    fn header(page_type: PageType) -> PageHeader {
        PageHeader {
            page_type,
            born_txn: 7,
            item_count: 3,
            level: if page_type.is_branch() { 1 } else { 0 },
            lower: PAGE_HEADER_SIZE,
            upper: PAGE_SIZE as u16,
            aux: 0x0605_0403,
            page_crc32c: 0,
        }
    }

    fn page(page_type: PageType) -> [u8; PAGE_SIZE] {
        let mut page = [0u8; PAGE_SIZE];
        header(page_type).encode_into(&mut page);
        write_crc32c(&mut page);
        page
    }

    #[test]
    fn exact_header_layout_and_crc() {
        let mut page = [0u8; PAGE_SIZE];
        let expected = header(PageType::RangeBranch);
        expected.encode_into(&mut page);
        let checksum = write_crc32c(&mut page);

        assert_eq!(&page[0..4], b"IP4P");
        assert_eq!(page[4], 1);
        assert_eq!(page[5], 0);
        assert_eq!(u16_le(&page, 6), 32);
        assert_eq!(u64_le(&page, 8), 7);
        assert_eq!(u16_le(&page, 16), 3);
        assert_eq!(u16_le(&page, 18), 1);
        assert_eq!(u16_le(&page, 20), 32);
        assert_eq!(u16_le(&page, 22), 4096);
        assert_eq!(u32_le(&page, 24), 0x0605_0403);
        assert_eq!(u32_le(&page, 28), checksum);
        assert_eq!(
            checksum,
            crc32c::crc32c_with_zeroed(&page, PAGE_CRC_OFFSET, 4).unwrap()
        );
        assert!(verify_crc32c(&page));

        let decoded = PageHeader::decode(&page, 7).unwrap();
        assert_eq!(
            decoded,
            PageHeader {
                page_crc32c: checksum,
                ..expected
            }
        );
    }

    #[test]
    fn all_and_only_seventeen_page_types_decode() {
        for (wire, page_type) in (1u8..=17).zip(ALL_PAGE_TYPES) {
            assert_eq!(page_type as u8, wire);
            assert_eq!(PageType::from_wire(wire), Some(page_type));
            assert_eq!(
                PageHeader::decode(&page(page_type), 7).unwrap().page_type,
                page_type
            );
        }
        for wire in [0, 18, u8::MAX] {
            let mut page = page(PageType::RangeLeaf);
            page[4] = wire;
            assert_eq!(
                PageHeader::decode(&page, 7),
                Err(PageHeaderError::PageType(wire))
            );
        }
    }

    #[test]
    fn fixed_magic_flags_and_header_size_fail_closed() {
        let valid = page(PageType::RangeLeaf);

        let mut bad = valid;
        bad[0] ^= 1;
        assert_eq!(PageHeader::decode(&bad, 7), Err(PageHeaderError::Magic));

        let mut bad = valid;
        bad[5] = 1;
        assert_eq!(PageHeader::decode(&bad, 7), Err(PageHeaderError::Flags(1)));

        for size in [0, 16, 31, 33, u16::MAX] {
            let mut bad = valid;
            put_u16(&mut bad, 6, size);
            assert_eq!(
                PageHeader::decode(&bad, 7),
                Err(PageHeaderError::HeaderSize(size))
            );
        }
    }

    #[test]
    fn born_transaction_must_be_current_or_older_and_nonzero() {
        let mut page = page(PageType::RangeLeaf);
        put_u64(&mut page, 8, 0);
        assert_eq!(
            PageHeader::decode(&page, 7),
            Err(PageHeaderError::BornTransactionZero)
        );

        put_u64(&mut page, 8, 8);
        assert_eq!(
            PageHeader::decode(&page, 7),
            Err(PageHeaderError::BornTransactionFuture {
                born_txn: 8,
                selected_txn: 7,
            })
        );

        put_u64(&mut page, 8, 6);
        assert_eq!(PageHeader::decode(&page, 7).unwrap().born_txn, 6);
    }

    #[test]
    fn branch_and_nonbranch_levels_are_exact_and_bounded() {
        for page_type in ALL_PAGE_TYPES {
            let mut page = page(page_type);
            if page_type.is_branch() {
                put_u16(&mut page, 18, 0);
                assert_eq!(
                    PageHeader::decode(&page, 7),
                    Err(PageHeaderError::BranchLevelZero(page_type))
                );

                put_u16(&mut page, 18, MAX_TREE_LEVEL);
                assert_eq!(PageHeader::decode(&page, 7).unwrap().level, MAX_TREE_LEVEL);
            } else {
                put_u16(&mut page, 18, 1);
                assert_eq!(
                    PageHeader::decode(&page, 7),
                    Err(PageHeaderError::NonBranchLevelNonzero {
                        page_type,
                        level: 1,
                    })
                );
            }

            put_u16(&mut page, 18, MAX_TREE_LEVEL + 1);
            assert_eq!(
                PageHeader::decode(&page, 7),
                Err(PageHeaderError::LevelTooHigh(MAX_TREE_LEVEL + 1))
            );
        }
    }

    #[test]
    fn used_area_boundaries_stay_inside_the_page() {
        let valid = page(PageType::RangeLeaf);
        for (lower, upper) in [(31, 4096), (33, 32), (32, 4097), (u16::MAX, u16::MAX)] {
            let mut bad = valid;
            put_u16(&mut bad, 20, lower);
            put_u16(&mut bad, 22, upper);
            assert_eq!(
                PageHeader::decode(&bad, 7),
                Err(PageHeaderError::Bounds { lower, upper })
            );
        }

        for (lower, upper) in [(32, 32), (32, 4096), (4096, 4096)] {
            let mut legal = valid;
            put_u16(&mut legal, 20, lower);
            put_u16(&mut legal, 22, upper);
            let decoded = PageHeader::decode(&legal, 7).unwrap();
            assert_eq!((decoded.lower, decoded.upper), (lower, upper));
        }
    }

    #[test]
    fn ordinary_decode_skips_crc_but_explicit_verification_does_not() {
        let valid = page(PageType::RangeLeaf);
        assert!(verify_crc32c(&valid));

        let mut bad_body = valid;
        bad_body[PAGE_HEADER_SIZE as usize] ^= 1;
        assert!(PageHeader::decode(&bad_body, 7).is_ok());
        assert!(!verify_crc32c(&bad_body));

        let mut bad_field = valid;
        bad_field[PAGE_CRC_OFFSET] ^= 1;
        assert!(PageHeader::decode(&bad_field, 7).is_ok());
        assert!(!verify_crc32c(&bad_field));

        write_crc32c(&mut bad_body);
        assert!(verify_crc32c(&bad_body));
    }
}
