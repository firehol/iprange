//! Exact v4 retirement-tree page views.

use crate::contract::{u32_le, u64_le, PAGE_SIZE};
use crate::page::{self, PageHeader, PageHeaderError, PageType, PAGE_HEADER_SIZE};

const BRANCH_ENTRY_SIZE: usize = 16;
const LEAF_RECORD_SIZE: usize = 32;
const MAX_BATCH_PAGE_COUNT: u64 = u32::MAX as u64 + 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RetirementPageError {
    Header(PageHeaderError),
    WrongPageType(PageType),
    WrongAux(u32),
    BranchLevelZero,
    LeafLevelNonzero(u16),
    FixedGeometry,
    EmptyPage,
    IndexOutOfBounds,
    ChildOutOfBounds(u32),
    ReservedNonzero,
    KeysNotStrict,
    RetiredTransactionOutOfRange(u64),
    BatchPageCountOutOfRange(u64),
    BlobRootOutOfBounds(u32),
    BlobLengthOverflow,
    Checksum,
}

impl From<PageHeaderError> for RetirementPageError {
    fn from(value: PageHeaderError) -> Self {
        Self::Header(value)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RetirementBranchEntry {
    pub(crate) max_retired_by_txn: u64,
    pub(crate) child_pgno: u32,
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct RetirementBranch<'a> {
    page: &'a [u8; PAGE_SIZE],
    count: usize,
    level: u16,
    page_count: u64,
    selected_txn: u64,
}

impl<'a> RetirementBranch<'a> {
    pub(crate) fn open(
        page: &'a [u8; PAGE_SIZE],
        selected_txn: u64,
        page_count: u64,
    ) -> Result<Self, RetirementPageError> {
        let header = match PageHeader::decode(page, selected_txn) {
            Err(PageHeaderError::BranchLevelZero(PageType::RetirementBranch)) => {
                return Err(RetirementPageError::BranchLevelZero);
            }
            result => result?,
        };
        if header.page_type != PageType::RetirementBranch {
            return Err(RetirementPageError::WrongPageType(header.page_type));
        }
        if header.aux != 0 {
            return Err(RetirementPageError::WrongAux(header.aux));
        }
        let count = usize::from(header.item_count);
        if count == 0 {
            return Err(RetirementPageError::EmptyPage);
        }
        let lower = usize::from(PAGE_HEADER_SIZE)
            .checked_add(
                count
                    .checked_mul(BRANCH_ENTRY_SIZE)
                    .ok_or(RetirementPageError::FixedGeometry)?,
            )
            .ok_or(RetirementPageError::FixedGeometry)?;
        if lower != usize::from(header.lower) || usize::from(header.upper) != PAGE_SIZE {
            return Err(RetirementPageError::FixedGeometry);
        }
        if page[lower..].iter().any(|&byte| byte != 0) {
            return Err(RetirementPageError::ReservedNonzero);
        }
        let branch = Self {
            page,
            count,
            level: header.level,
            page_count,
            selected_txn,
        };
        let mut previous = None;
        for index in 0..count {
            let entry = branch.entry(index)?;
            if previous
                .map(|key| entry.max_retired_by_txn <= key)
                .unwrap_or(false)
            {
                return Err(RetirementPageError::KeysNotStrict);
            }
            previous = Some(entry.max_retired_by_txn);
        }
        Ok(branch)
    }

    #[inline]
    pub(crate) const fn len(self) -> usize {
        self.count
    }

    #[inline]
    pub(crate) const fn level(self) -> u16 {
        self.level
    }

    pub(crate) fn entry(self, index: usize) -> Result<RetirementBranchEntry, RetirementPageError> {
        if index >= self.count {
            return Err(RetirementPageError::IndexOutOfBounds);
        }
        let offset = usize::from(PAGE_HEADER_SIZE) + index * BRANCH_ENTRY_SIZE;
        if u32_le(self.page, offset + 12) != 0 {
            return Err(RetirementPageError::ReservedNonzero);
        }
        let max_retired_by_txn = u64_le(self.page, offset);
        if max_retired_by_txn <= 1 || max_retired_by_txn > self.selected_txn {
            return Err(RetirementPageError::RetiredTransactionOutOfRange(
                max_retired_by_txn,
            ));
        }
        let child_pgno = u32_le(self.page, offset + 8);
        if child_pgno < 2 || u64::from(child_pgno) >= self.page_count {
            return Err(RetirementPageError::ChildOutOfBounds(child_pgno));
        }
        Ok(RetirementBranchEntry {
            max_retired_by_txn,
            child_pgno,
        })
    }

    pub(crate) fn maximum_key(self) -> Result<u64, RetirementPageError> {
        Ok(self.entry(self.count - 1)?.max_retired_by_txn)
    }

    pub(crate) fn verify_crc(self) -> Result<(), RetirementPageError> {
        if page::verify_crc32c(self.page) {
            Ok(())
        } else {
            Err(RetirementPageError::Checksum)
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RetirementBatch {
    pub(crate) retired_by_txn: u64,
    pub(crate) page_count: u64,
    pub(crate) page_list_blob_root: u32,
}

impl RetirementBatch {
    pub(crate) fn blob_length(self) -> Result<u64, RetirementPageError> {
        self.page_count
            .checked_mul(4)
            .ok_or(RetirementPageError::BlobLengthOverflow)
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct RetirementLeaf<'a> {
    page: &'a [u8; PAGE_SIZE],
    count: usize,
    selected_txn: u64,
    page_count: u64,
}

impl<'a> RetirementLeaf<'a> {
    pub(crate) fn open(
        page: &'a [u8; PAGE_SIZE],
        selected_txn: u64,
        page_count: u64,
    ) -> Result<Self, RetirementPageError> {
        let header = match PageHeader::decode(page, selected_txn) {
            Err(PageHeaderError::NonBranchLevelNonzero {
                page_type: PageType::RetirementLeaf,
                level,
            }) => return Err(RetirementPageError::LeafLevelNonzero(level)),
            result => result?,
        };
        if header.page_type != PageType::RetirementLeaf {
            return Err(RetirementPageError::WrongPageType(header.page_type));
        }
        if header.aux != 0 {
            return Err(RetirementPageError::WrongAux(header.aux));
        }
        let count = usize::from(header.item_count);
        if count == 0 {
            return Err(RetirementPageError::EmptyPage);
        }
        let lower = usize::from(PAGE_HEADER_SIZE)
            .checked_add(
                count
                    .checked_mul(LEAF_RECORD_SIZE)
                    .ok_or(RetirementPageError::FixedGeometry)?,
            )
            .ok_or(RetirementPageError::FixedGeometry)?;
        if lower != usize::from(header.lower) || usize::from(header.upper) != PAGE_SIZE {
            return Err(RetirementPageError::FixedGeometry);
        }
        if page[lower..].iter().any(|&byte| byte != 0) {
            return Err(RetirementPageError::ReservedNonzero);
        }
        let leaf = Self {
            page,
            count,
            selected_txn,
            page_count,
        };
        let mut previous = None;
        for index in 0..count {
            let batch = leaf.batch(index)?;
            if previous
                .map(|key| batch.retired_by_txn <= key)
                .unwrap_or(false)
            {
                return Err(RetirementPageError::KeysNotStrict);
            }
            previous = Some(batch.retired_by_txn);
        }
        Ok(leaf)
    }

    #[inline]
    pub(crate) const fn len(self) -> usize {
        self.count
    }

    pub(crate) fn batch(self, index: usize) -> Result<RetirementBatch, RetirementPageError> {
        if index >= self.count {
            return Err(RetirementPageError::IndexOutOfBounds);
        }
        let offset = usize::from(PAGE_HEADER_SIZE) + index * LEAF_RECORD_SIZE;
        if u64_le(self.page, offset) != 0 || u32_le(self.page, offset + 28) != 0 {
            return Err(RetirementPageError::ReservedNonzero);
        }
        let retired_by_txn = u64_le(self.page, offset + 8);
        if retired_by_txn <= 1 || retired_by_txn > self.selected_txn {
            return Err(RetirementPageError::RetiredTransactionOutOfRange(
                retired_by_txn,
            ));
        }
        let page_count = u64_le(self.page, offset + 16);
        if !(1..=MAX_BATCH_PAGE_COUNT).contains(&page_count) {
            return Err(RetirementPageError::BatchPageCountOutOfRange(page_count));
        }
        let page_list_blob_root = u32_le(self.page, offset + 24);
        if page_list_blob_root < 2 || u64::from(page_list_blob_root) >= self.page_count {
            return Err(RetirementPageError::BlobRootOutOfBounds(
                page_list_blob_root,
            ));
        }
        let batch = RetirementBatch {
            retired_by_txn,
            page_count,
            page_list_blob_root,
        };
        let _ = batch.blob_length()?;
        Ok(batch)
    }

    pub(crate) fn maximum_key(self) -> Result<u64, RetirementPageError> {
        Ok(self.batch(self.count - 1)?.retired_by_txn)
    }

    pub(crate) fn verify_crc(self) -> Result<(), RetirementPageError> {
        if page::verify_crc32c(self.page) {
            Ok(())
        } else {
            Err(RetirementPageError::Checksum)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::page::{write_crc32c, PageHeader, PAGE_CRC_OFFSET};

    fn header(page_type: PageType, count: u16, level: u16, lower: u16) -> PageHeader {
        PageHeader {
            page_type,
            born_txn: 1,
            item_count: count,
            level,
            lower,
            upper: PAGE_SIZE as u16,
            aux: 0,
            page_crc32c: 0,
        }
    }

    fn branch(entries: &[(u64, u32)]) -> [u8; PAGE_SIZE] {
        let mut page = [0u8; PAGE_SIZE];
        header(
            PageType::RetirementBranch,
            entries.len() as u16,
            1,
            (usize::from(PAGE_HEADER_SIZE) + entries.len() * BRANCH_ENTRY_SIZE) as u16,
        )
        .encode_into(&mut page);
        for (index, &(key, child)) in entries.iter().enumerate() {
            let at = usize::from(PAGE_HEADER_SIZE) + index * BRANCH_ENTRY_SIZE;
            page[at..at + 8].copy_from_slice(&key.to_le_bytes());
            page[at + 8..at + 12].copy_from_slice(&child.to_le_bytes());
        }
        write_crc32c(&mut page);
        page
    }

    fn leaf(entries: &[(u64, u64, u32)]) -> [u8; PAGE_SIZE] {
        let mut page = [0u8; PAGE_SIZE];
        header(
            PageType::RetirementLeaf,
            entries.len() as u16,
            0,
            (usize::from(PAGE_HEADER_SIZE) + entries.len() * LEAF_RECORD_SIZE) as u16,
        )
        .encode_into(&mut page);
        for (index, &(txn, count, root)) in entries.iter().enumerate() {
            let at = usize::from(PAGE_HEADER_SIZE) + index * LEAF_RECORD_SIZE;
            page[at + 8..at + 16].copy_from_slice(&txn.to_le_bytes());
            page[at + 16..at + 24].copy_from_slice(&count.to_le_bytes());
            page[at + 24..at + 28].copy_from_slice(&root.to_le_bytes());
        }
        write_crc32c(&mut page);
        page
    }

    #[test]
    fn branch_uses_exact_layout_and_ordered_maximum_keys() {
        let mut page = branch(&[(2, 3), (5, 4)]);
        assert_eq!(u64_le(&page, 32), 2);
        assert_eq!(u32_le(&page, 40), 3);
        assert_eq!(u32_le(&page, 44), 0);
        let view = RetirementBranch::open(&page, 6, 5).unwrap();
        assert_eq!(view.len(), 2);
        assert_eq!(view.level(), 1);
        assert_eq!(
            view.entry(1).unwrap(),
            RetirementBranchEntry {
                max_retired_by_txn: 5,
                child_pgno: 4,
            }
        );
        assert_eq!(view.maximum_key().unwrap(), 5);
        view.verify_crc().unwrap();

        page[PAGE_CRC_OFFSET] ^= 1;
        let view = RetirementBranch::open(&page, 6, 5).unwrap();
        assert_eq!(view.maximum_key().unwrap(), 5);
        assert_eq!(view.verify_crc(), Err(RetirementPageError::Checksum));
    }

    #[test]
    fn branch_rejects_aux_geometry_reserved_children_and_bad_keys() {
        let mut page = branch(&[(2, 3)]);
        page[24..28].copy_from_slice(&1u32.to_le_bytes());
        assert!(matches!(
            RetirementBranch::open(&page, 6, 5),
            Err(RetirementPageError::WrongAux(1))
        ));

        page = branch(&[(2, 3)]);
        page[20..22].copy_from_slice(&47u16.to_le_bytes());
        assert!(matches!(
            RetirementBranch::open(&page, 6, 5),
            Err(RetirementPageError::FixedGeometry)
        ));

        page = branch(&[(2, 3)]);
        page[44] = 1;
        assert!(matches!(
            RetirementBranch::open(&page, 6, 5),
            Err(RetirementPageError::ReservedNonzero)
        ));

        page = branch(&[(2, 3)]);
        page[18..20].copy_from_slice(&0u16.to_le_bytes());
        assert!(matches!(
            RetirementBranch::open(&page, 6, 5),
            Err(RetirementPageError::BranchLevelZero)
        ));

        page = branch(&[(2, 5)]);
        assert!(matches!(
            RetirementBranch::open(&page, 6, 5),
            Err(RetirementPageError::ChildOutOfBounds(5))
        ));

        page = branch(&[(2, 3), (2, 4)]);
        assert!(matches!(
            RetirementBranch::open(&page, 6, 5),
            Err(RetirementPageError::KeysNotStrict)
        ));

        page = branch(&[(1, 3)]);
        assert!(matches!(
            RetirementBranch::open(&page, 6, 5),
            Err(RetirementPageError::RetiredTransactionOutOfRange(1))
        ));

        page = branch(&[(2, 3)]);
        page[48] = 1;
        assert!(matches!(
            RetirementBranch::open(&page, 6, 5),
            Err(RetirementPageError::ReservedNonzero)
        ));
    }

    #[test]
    fn leaf_uses_exact_layout_and_batch_constraints() {
        let mut page = leaf(&[(2, 7, 3), (5, u32::MAX as u64 + 1, 4)]);
        assert_eq!(u64_le(&page, 32), 0);
        assert_eq!(u64_le(&page, 40), 2);
        assert_eq!(u64_le(&page, 48), 7);
        assert_eq!(u32_le(&page, 56), 3);
        assert_eq!(u32_le(&page, 60), 0);
        let view = RetirementLeaf::open(&page, 6, 5).unwrap();
        assert_eq!(
            view.batch(0).unwrap(),
            RetirementBatch {
                retired_by_txn: 2,
                page_count: 7,
                page_list_blob_root: 3,
            }
        );
        assert_eq!(view.batch(1).unwrap().blob_length().unwrap(), 1u64 << 34);
        assert_eq!(view.maximum_key().unwrap(), 5);
        view.verify_crc().unwrap();

        page[PAGE_CRC_OFFSET] ^= 1;
        let view = RetirementLeaf::open(&page, 6, 5).unwrap();
        assert_eq!(view.maximum_key().unwrap(), 5);
        assert_eq!(view.verify_crc(), Err(RetirementPageError::Checksum));
    }

    #[test]
    fn leaf_rejects_empty_geometry_reserved_order_and_batch_fields() {
        let mut page = [0u8; PAGE_SIZE];
        header(PageType::RetirementLeaf, 0, 0, PAGE_HEADER_SIZE).encode_into(&mut page);
        assert!(matches!(
            RetirementLeaf::open(&page, 6, 5),
            Err(RetirementPageError::EmptyPage)
        ));

        page = leaf(&[(2, 1, 3)]);
        page[20..22].copy_from_slice(&63u16.to_le_bytes());
        assert!(matches!(
            RetirementLeaf::open(&page, 6, 5),
            Err(RetirementPageError::FixedGeometry)
        ));

        page = leaf(&[(2, 1, 3)]);
        page[32] = 1;
        assert!(matches!(
            RetirementLeaf::open(&page, 6, 5),
            Err(RetirementPageError::ReservedNonzero)
        ));

        page = leaf(&[(2, 1, 3), (2, 1, 4)]);
        assert!(matches!(
            RetirementLeaf::open(&page, 6, 5),
            Err(RetirementPageError::KeysNotStrict)
        ));

        page = leaf(&[(7, 1, 3)]);
        assert!(matches!(
            RetirementLeaf::open(&page, 6, 5),
            Err(RetirementPageError::RetiredTransactionOutOfRange(7))
        ));

        page = leaf(&[(2, 0, 3)]);
        assert!(matches!(
            RetirementLeaf::open(&page, 6, 5),
            Err(RetirementPageError::BatchPageCountOutOfRange(0))
        ));

        page = leaf(&[(2, u32::MAX as u64 + 2, 3)]);
        assert!(matches!(
            RetirementLeaf::open(&page, 6, 5),
            Err(RetirementPageError::BatchPageCountOutOfRange(_))
        ));

        page = leaf(&[(2, 1, 5)]);
        assert!(matches!(
            RetirementLeaf::open(&page, 6, 5),
            Err(RetirementPageError::BlobRootOutOfBounds(5))
        ));

        page = leaf(&[(2, 1, 3)]);
        page[64] = 1;
        assert!(matches!(
            RetirementLeaf::open(&page, 6, 5),
            Err(RetirementPageError::ReservedNonzero)
        ));

        page = leaf(&[(2, 1, 3)]);
        page[18..20].copy_from_slice(&1u16.to_le_bytes());
        assert!(matches!(
            RetirementLeaf::open(&page, 6, 5),
            Err(RetirementPageError::LeafLevelNonzero(1))
        ));
    }
}
