//! Exact v4 generic-blob page views.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_SIZE};
use crate::page::{self, PageHeader, PageHeaderError, PageType, PAGE_HEADER_SIZE};

const BRANCH_ENTRY_SIZE: usize = 16;
pub(crate) const BLOB_LEAF_DATA_OFFSET: usize = 48;
pub(crate) const BLOB_LEAF_CAPACITY: u16 = (PAGE_SIZE - BLOB_LEAF_DATA_OFFSET) as u16;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub(crate) enum BlobKind {
    MembershipBitmap = 1,
    RetirementPageList = 2,
}

impl BlobKind {
    pub(crate) const fn alignment(self) -> u64 {
        match self {
            Self::MembershipBitmap => 8,
            Self::RetirementPageList => 4,
        }
    }

    const fn from_wire(value: u32) -> Option<Self> {
        match value {
            1 => Some(Self::MembershipBitmap),
            2 => Some(Self::RetirementPageList),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum BlobPageError {
    Header(PageHeaderError),
    WrongPageType(PageType),
    WrongKind(u32),
    FixedGeometry,
    EmptyBranch,
    IndexOutOfBounds,
    ChildOutOfBounds(u32),
    ReservedNonzero,
    OffsetsNotStrict,
    LeafItemCount(u16),
    DataLength(u16),
    DataAlignment { length: u16, alignment: u64 },
    Checksum,
}

impl From<PageHeaderError> for BlobPageError {
    fn from(value: PageHeaderError) -> Self {
        Self::Header(value)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct BlobBranchEntry {
    pub(crate) logical_offset: u64,
    pub(crate) child_pgno: u32,
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct BlobBranch<'a> {
    page: &'a [u8; PAGE_SIZE],
    count: usize,
    level: u16,
    page_count: u64,
}

impl<'a> BlobBranch<'a> {
    pub(crate) fn open(
        page: &'a [u8; PAGE_SIZE],
        selected_txn: u64,
        kind: BlobKind,
        page_count: u64,
    ) -> Result<Self, BlobPageError> {
        let header = PageHeader::decode(page, selected_txn)?;
        if header.page_type != PageType::BlobBranch {
            return Err(BlobPageError::WrongPageType(header.page_type));
        }
        if BlobKind::from_wire(header.aux) != Some(kind) {
            return Err(BlobPageError::WrongKind(header.aux));
        }
        let count = usize::from(header.item_count);
        if count == 0 {
            return Err(BlobPageError::EmptyBranch);
        }
        let lower = usize::from(PAGE_HEADER_SIZE)
            .checked_add(
                count
                    .checked_mul(BRANCH_ENTRY_SIZE)
                    .ok_or(BlobPageError::FixedGeometry)?,
            )
            .ok_or(BlobPageError::FixedGeometry)?;
        if lower != usize::from(header.lower) || usize::from(header.upper) != PAGE_SIZE {
            return Err(BlobPageError::FixedGeometry);
        }
        Ok(Self {
            page,
            count,
            level: header.level,
            page_count,
        })
    }

    pub(crate) fn verify_local(self) -> Result<(), BlobPageError> {
        if !page::verify_crc32c(self.page) {
            return Err(BlobPageError::Checksum);
        }
        let lower = usize::from(PAGE_HEADER_SIZE) + self.count * BRANCH_ENTRY_SIZE;
        if self.page[lower..].iter().any(|&byte| byte != 0) {
            return Err(BlobPageError::ReservedNonzero);
        }
        let mut previous = None;
        for index in 0..self.count {
            let entry = self.entry(index)?;
            if previous
                .map(|offset| entry.logical_offset <= offset)
                .unwrap_or(false)
            {
                return Err(BlobPageError::OffsetsNotStrict);
            }
            previous = Some(entry.logical_offset);
        }
        Ok(())
    }

    #[inline]
    pub(crate) const fn len(self) -> usize {
        self.count
    }

    #[inline]
    pub(crate) const fn level(self) -> u16 {
        self.level
    }

    pub(crate) fn entry(self, index: usize) -> Result<BlobBranchEntry, BlobPageError> {
        if index >= self.count {
            return Err(BlobPageError::IndexOutOfBounds);
        }
        let offset = usize::from(PAGE_HEADER_SIZE) + index * BRANCH_ENTRY_SIZE;
        if u32_le(self.page, offset + 12) != 0 {
            return Err(BlobPageError::ReservedNonzero);
        }
        let child_pgno = u32_le(self.page, offset + 8);
        if child_pgno < 2 || u64::from(child_pgno) >= self.page_count {
            return Err(BlobPageError::ChildOutOfBounds(child_pgno));
        }
        Ok(BlobBranchEntry {
            logical_offset: u64_le(self.page, offset),
            child_pgno,
        })
    }

    pub(crate) fn predecessor_for(self, logical_offset: u64) -> Result<usize, BlobPageError> {
        let mut low = 0usize;
        let mut high = self.count;
        while low < high {
            let middle = low + (high - low) / 2;
            if self.entry(middle)?.logical_offset <= logical_offset {
                low = middle + 1;
            } else {
                high = middle;
            }
        }
        if low == 0 {
            return Err(BlobPageError::OffsetsNotStrict);
        }
        Ok(low - 1)
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct BlobLeaf<'a> {
    page: &'a [u8; PAGE_SIZE],
    logical_offset: u64,
    data_len: u16,
}

impl<'a> BlobLeaf<'a> {
    pub(crate) fn open(
        page: &'a [u8; PAGE_SIZE],
        selected_txn: u64,
        kind: BlobKind,
    ) -> Result<Self, BlobPageError> {
        let header = PageHeader::decode(page, selected_txn)?;
        if header.page_type != PageType::BlobLeaf {
            return Err(BlobPageError::WrongPageType(header.page_type));
        }
        if BlobKind::from_wire(header.aux) != Some(kind) {
            return Err(BlobPageError::WrongKind(header.aux));
        }
        if header.item_count != 1 {
            return Err(BlobPageError::LeafItemCount(header.item_count));
        }
        let data_len = u16_le(page, 40);
        if !(1..=BLOB_LEAF_CAPACITY).contains(&data_len) {
            return Err(BlobPageError::DataLength(data_len));
        }
        let alignment = kind.alignment();
        if u64::from(data_len) % alignment != 0 {
            return Err(BlobPageError::DataAlignment {
                length: data_len,
                alignment,
            });
        }
        let lower = BLOB_LEAF_DATA_OFFSET
            .checked_add(usize::from(data_len))
            .ok_or(BlobPageError::FixedGeometry)?;
        if lower != usize::from(header.lower) || usize::from(header.upper) != PAGE_SIZE {
            return Err(BlobPageError::FixedGeometry);
        }
        if page[42..BLOB_LEAF_DATA_OFFSET]
            .iter()
            .any(|&byte| byte != 0)
        {
            return Err(BlobPageError::ReservedNonzero);
        }
        Ok(Self {
            page,
            logical_offset: u64_le(page, 32),
            data_len,
        })
    }

    #[inline]
    pub(crate) const fn logical_offset(self) -> u64 {
        self.logical_offset
    }

    #[inline]
    pub(crate) const fn data_len(self) -> u16 {
        self.data_len
    }

    #[inline]
    pub(crate) fn data(self) -> &'a [u8] {
        &self.page[BLOB_LEAF_DATA_OFFSET..BLOB_LEAF_DATA_OFFSET + usize::from(self.data_len)]
    }

    pub(crate) fn verify_local(self) -> Result<(), BlobPageError> {
        if !page::verify_crc32c(self.page) {
            return Err(BlobPageError::Checksum);
        }
        let lower = BLOB_LEAF_DATA_OFFSET + usize::from(self.data_len);
        if self.page[lower..].iter().any(|&byte| byte != 0) {
            return Err(BlobPageError::ReservedNonzero);
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::page::{write_crc32c, PageHeader};

    fn header(
        page_type: PageType,
        kind: BlobKind,
        count: u16,
        level: u16,
        lower: u16,
    ) -> PageHeader {
        PageHeader {
            page_type,
            born_txn: 3,
            item_count: count,
            level,
            lower,
            upper: PAGE_SIZE as u16,
            aux: kind as u32,
            page_crc32c: 0,
        }
    }

    fn branch(kind: BlobKind, level: u16, entries: &[(u64, u32)]) -> [u8; PAGE_SIZE] {
        let mut page = [0u8; PAGE_SIZE];
        header(
            PageType::BlobBranch,
            kind,
            entries.len() as u16,
            level,
            (usize::from(PAGE_HEADER_SIZE) + entries.len() * BRANCH_ENTRY_SIZE) as u16,
        )
        .encode_into(&mut page);
        for (index, &(logical_offset, child_pgno)) in entries.iter().enumerate() {
            let at = usize::from(PAGE_HEADER_SIZE) + index * BRANCH_ENTRY_SIZE;
            page[at..at + 8].copy_from_slice(&logical_offset.to_le_bytes());
            page[at + 8..at + 12].copy_from_slice(&child_pgno.to_le_bytes());
        }
        write_crc32c(&mut page);
        page
    }

    fn leaf(kind: BlobKind, logical_offset: u64, data: &[u8]) -> [u8; PAGE_SIZE] {
        let mut page = [0u8; PAGE_SIZE];
        header(
            PageType::BlobLeaf,
            kind,
            1,
            0,
            (BLOB_LEAF_DATA_OFFSET + data.len()) as u16,
        )
        .encode_into(&mut page);
        page[32..40].copy_from_slice(&logical_offset.to_le_bytes());
        page[40..42].copy_from_slice(&(data.len() as u16).to_le_bytes());
        page[BLOB_LEAF_DATA_OFFSET..BLOB_LEAF_DATA_OFFSET + data.len()].copy_from_slice(data);
        write_crc32c(&mut page);
        page
    }

    #[test]
    fn branch_has_exact_layout_kind_and_strict_offsets() {
        let page = branch(
            BlobKind::MembershipBitmap,
            2,
            &[(0, 3), (4048, 4), (8096, 5)],
        );
        assert_eq!(u64_le(&page, 32), 0);
        assert_eq!(u32_le(&page, 40), 3);
        assert_eq!(u32_le(&page, 44), 0);
        assert_eq!(u64_le(&page, 48), 4048);

        let view = BlobBranch::open(&page, 3, BlobKind::MembershipBitmap, 6).unwrap();
        assert_eq!(view.len(), 3);
        assert_eq!(view.level(), 2);
        assert_eq!(
            view.entry(1).unwrap(),
            BlobBranchEntry {
                logical_offset: 4048,
                child_pgno: 4,
            }
        );
        assert_eq!(view.predecessor_for(0).unwrap(), 0);
        assert_eq!(view.predecessor_for(8095).unwrap(), 1);
        assert_eq!(view.predecessor_for(u64::MAX).unwrap(), 2);
        view.verify_local().unwrap();
    }

    #[test]
    fn branch_rejects_zero_count_geometry_reserved_children_and_offsets() {
        let mut page = [0u8; PAGE_SIZE];
        header(
            PageType::BlobBranch,
            BlobKind::RetirementPageList,
            0,
            1,
            PAGE_HEADER_SIZE,
        )
        .encode_into(&mut page);
        assert!(matches!(
            BlobBranch::open(&page, 3, BlobKind::RetirementPageList, 5),
            Err(BlobPageError::EmptyBranch)
        ));

        page = branch(BlobKind::RetirementPageList, 1, &[(0, 3)]);
        page[20..22].copy_from_slice(&47u16.to_le_bytes());
        assert!(matches!(
            BlobBranch::open(&page, 3, BlobKind::RetirementPageList, 5),
            Err(BlobPageError::FixedGeometry)
        ));

        page = branch(BlobKind::RetirementPageList, 1, &[(0, 3)]);
        page[44] = 1;
        write_crc32c(&mut page);
        let view = BlobBranch::open(&page, 3, BlobKind::RetirementPageList, 5).unwrap();
        assert_eq!(view.verify_local(), Err(BlobPageError::ReservedNonzero));

        page = branch(BlobKind::RetirementPageList, 1, &[(0, 5)]);
        let view = BlobBranch::open(&page, 3, BlobKind::RetirementPageList, 5).unwrap();
        assert_eq!(view.verify_local(), Err(BlobPageError::ChildOutOfBounds(5)));

        page = branch(BlobKind::RetirementPageList, 1, &[(0, 3), (0, 4)]);
        let view = BlobBranch::open(&page, 3, BlobKind::RetirementPageList, 5).unwrap();
        assert_eq!(view.verify_local(), Err(BlobPageError::OffsetsNotStrict));

        page = branch(BlobKind::RetirementPageList, 1, &[(0, 3)]);
        page[48] = 1;
        write_crc32c(&mut page);
        let view = BlobBranch::open(&page, 3, BlobKind::RetirementPageList, 5).unwrap();
        assert_eq!(view.verify_local(), Err(BlobPageError::ReservedNonzero));
    }

    #[test]
    fn page_kind_and_type_are_not_interchangeable() {
        let page = branch(BlobKind::MembershipBitmap, 1, &[(0, 3)]);
        assert!(matches!(
            BlobBranch::open(&page, 3, BlobKind::RetirementPageList, 4),
            Err(BlobPageError::WrongKind(value))
                if value == BlobKind::MembershipBitmap as u32
        ));

        let page = leaf(BlobKind::MembershipBitmap, 0, &[7; 8]);
        assert!(matches!(
            BlobBranch::open(&page, 3, BlobKind::MembershipBitmap, 4),
            Err(BlobPageError::WrongPageType(PageType::BlobLeaf))
        ));
    }

    #[test]
    fn leaf_has_exact_layout_and_explicit_crc_verification() {
        let mut page = leaf(BlobKind::MembershipBitmap, 4048, &[1, 2, 3, 4, 5, 6, 7, 8]);
        assert_eq!(u64_le(&page, 32), 4048);
        assert_eq!(u16_le(&page, 40), 8);
        assert_eq!(&page[42..48], &[0; 6]);
        assert_eq!(&page[48..56], &[1, 2, 3, 4, 5, 6, 7, 8]);

        let view = BlobLeaf::open(&page, 3, BlobKind::MembershipBitmap).unwrap();
        assert_eq!(view.logical_offset(), 4048);
        assert_eq!(view.data_len(), 8);
        assert_eq!(view.data(), &[1, 2, 3, 4, 5, 6, 7, 8]);
        view.verify_local().unwrap();

        page[crate::page::PAGE_CRC_OFFSET] ^= 1;
        let view = BlobLeaf::open(&page, 3, BlobKind::MembershipBitmap).unwrap();
        assert_eq!(view.data(), &[1, 2, 3, 4, 5, 6, 7, 8]);
        assert_eq!(view.verify_local(), Err(BlobPageError::Checksum));
    }

    #[test]
    fn leaf_rejects_count_length_geometry_and_reserved_bytes() {
        let mut page = leaf(BlobKind::RetirementPageList, 0, &[1, 2, 3, 4]);
        page[16..18].copy_from_slice(&0u16.to_le_bytes());
        assert!(matches!(
            BlobLeaf::open(&page, 3, BlobKind::RetirementPageList),
            Err(BlobPageError::LeafItemCount(0))
        ));

        page = leaf(BlobKind::RetirementPageList, 0, &[1, 2, 3, 4]);
        page[40..42].copy_from_slice(&0u16.to_le_bytes());
        assert!(matches!(
            BlobLeaf::open(&page, 3, BlobKind::RetirementPageList),
            Err(BlobPageError::DataLength(0))
        ));

        page = leaf(BlobKind::RetirementPageList, 0, &[1, 2]);
        assert!(matches!(
            BlobLeaf::open(&page, 3, BlobKind::RetirementPageList),
            Err(BlobPageError::DataAlignment {
                length: 2,
                alignment: 4,
            })
        ));

        page = leaf(BlobKind::RetirementPageList, 0, &[1, 2, 3, 4]);
        page[20..22].copy_from_slice(&53u16.to_le_bytes());
        assert!(matches!(
            BlobLeaf::open(&page, 3, BlobKind::RetirementPageList),
            Err(BlobPageError::FixedGeometry)
        ));

        page = leaf(BlobKind::RetirementPageList, 0, &[1, 2, 3, 4]);
        page[42] = 1;
        assert!(matches!(
            BlobLeaf::open(&page, 3, BlobKind::RetirementPageList),
            Err(BlobPageError::ReservedNonzero)
        ));

        page = leaf(BlobKind::RetirementPageList, 0, &[1, 2, 3, 4]);
        page[52] = 1;
        write_crc32c(&mut page);
        let view = BlobLeaf::open(&page, 3, BlobKind::RetirementPageList).unwrap();
        assert_eq!(view.verify_local(), Err(BlobPageError::ReservedNonzero));
    }
}
