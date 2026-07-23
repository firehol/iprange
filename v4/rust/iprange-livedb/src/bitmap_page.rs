//! Exact v4 hierarchical-bitmap page views.

use crate::contract::{u32_le, u64_le, BITMAP_FANOUT, BITMAP_LEAF_WORDS, PAGE_SIZE};
use crate::page::{self, PageHeader, PageHeaderError, PageType};

const LEAF_LOWER: u16 = 4032;
const BRANCH_LOWER: u16 = 1088;
const SUMMARY_OFFSET: usize = 32;
const CHILDREN_OFFSET: usize = 64;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub(crate) enum BitmapKind {
    FreePages = 1,
    FeedUsed = 2,
    MembershipUsed = 3,
}

impl BitmapKind {
    const fn from_wire(value: u32) -> Option<Self> {
        match value {
            1 => Some(Self::FreePages),
            2 => Some(Self::FeedUsed),
            3 => Some(Self::MembershipUsed),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum BitmapPageError {
    Header(PageHeaderError),
    WrongPageType(PageType),
    WrongKind(u32),
    FixedGeometry,
    EmptyPage,
    TooManyItems(u16),
    ReservedNonzero,
    ItemCountMismatch,
    Checksum,
    BitOutsideLimit,
    ChildPageOutOfBounds(u32),
    ChildOutsideLimit,
}

impl From<PageHeaderError> for BitmapPageError {
    fn from(value: PageHeaderError) -> Self {
        Self::Header(value)
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct BitmapLeaf<'a> {
    page: &'a [u8; PAGE_SIZE],
    header: PageHeader,
}

impl<'a> BitmapLeaf<'a> {
    pub(crate) fn open(
        page: &'a [u8; PAGE_SIZE],
        selected_txn: u64,
        kind: BitmapKind,
    ) -> Result<Self, BitmapPageError> {
        let header = PageHeader::decode(page, selected_txn)?;
        if header.page_type != PageType::BitmapLeaf {
            return Err(BitmapPageError::WrongPageType(header.page_type));
        }
        if BitmapKind::from_wire(header.aux) != Some(kind) {
            return Err(BitmapPageError::WrongKind(header.aux));
        }
        if header.lower != LEAF_LOWER || usize::from(header.upper) != PAGE_SIZE {
            return Err(BitmapPageError::FixedGeometry);
        }
        if usize::from(header.item_count) > BITMAP_LEAF_WORDS {
            return Err(BitmapPageError::TooManyItems(header.item_count));
        }
        Ok(Self { page, header })
    }

    #[inline]
    pub(crate) fn word(self, index: usize) -> u64 {
        debug_assert!(index < BITMAP_LEAF_WORDS);
        u64_le(self.page, 32 + index * 8)
    }

    pub(crate) fn verify_local(
        self,
        kind: BitmapKind,
        base: u64,
        limit: u64,
    ) -> Result<(), BitmapPageError> {
        if !page::verify_crc32c(self.page) {
            return Err(BitmapPageError::Checksum);
        }
        if self.page[usize::from(LEAF_LOWER)..]
            .iter()
            .any(|&byte| byte != 0)
        {
            return Err(BitmapPageError::ReservedNonzero);
        }

        let mut actual_nonzero = 0usize;
        for index in 0..BITMAP_LEAF_WORDS {
            let word = self.word(index);
            actual_nonzero += usize::from(word != 0);
            if word == 0 {
                continue;
            }
            let word_base = base
                .checked_add((index as u64) * 64)
                .ok_or(BitmapPageError::BitOutsideLimit)?;
            for bit in 0..64u64 {
                if word & (1u64 << bit) == 0 {
                    continue;
                }
                let absolute = word_base
                    .checked_add(bit)
                    .ok_or(BitmapPageError::BitOutsideLimit)?;
                if absolute >= limit
                    || (kind == BitmapKind::FreePages && absolute < 2)
                    || (kind == BitmapKind::MembershipUsed && absolute == 0)
                {
                    return Err(BitmapPageError::BitOutsideLimit);
                }
            }
        }
        if actual_nonzero != usize::from(self.header.item_count) {
            return Err(BitmapPageError::ItemCountMismatch);
        }
        Ok(())
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct BitmapBranch<'a> {
    page: &'a [u8; PAGE_SIZE],
    header: PageHeader,
}

impl<'a> BitmapBranch<'a> {
    pub(crate) fn open(
        page: &'a [u8; PAGE_SIZE],
        selected_txn: u64,
        kind: BitmapKind,
    ) -> Result<Self, BitmapPageError> {
        let header = PageHeader::decode(page, selected_txn)?;
        if header.page_type != PageType::BitmapBranch {
            return Err(BitmapPageError::WrongPageType(header.page_type));
        }
        if BitmapKind::from_wire(header.aux) != Some(kind) {
            return Err(BitmapPageError::WrongKind(header.aux));
        }
        if header.lower != BRANCH_LOWER || usize::from(header.upper) != PAGE_SIZE {
            return Err(BitmapPageError::FixedGeometry);
        }
        if header.item_count == 0 {
            return Err(BitmapPageError::EmptyPage);
        }
        if u64::from(header.item_count) > BITMAP_FANOUT {
            return Err(BitmapPageError::TooManyItems(header.item_count));
        }
        Ok(Self { page, header })
    }

    #[inline]
    pub(crate) const fn level(self) -> u16 {
        self.header.level
    }

    #[inline]
    pub(crate) fn summary_word(self, index: usize) -> u64 {
        debug_assert!(index < 4);
        u64_le(self.page, SUMMARY_OFFSET + index * 8)
    }

    #[inline]
    pub(crate) fn summary_bit(self, index: usize) -> bool {
        debug_assert!(index < BITMAP_FANOUT as usize);
        self.summary_word(index / 64) & (1u64 << (index % 64)) != 0
    }

    #[inline]
    pub(crate) fn child(self, index: usize) -> u32 {
        debug_assert!(index < BITMAP_FANOUT as usize);
        u32_le(self.page, CHILDREN_OFFSET + index * 4)
    }

    pub(crate) fn next_summary(self, start: usize) -> Option<usize> {
        if start >= BITMAP_FANOUT as usize {
            return None;
        }
        let mut word_index = start / 64;
        let mut word = self.summary_word(word_index) & (u64::MAX << (start % 64));
        loop {
            if word != 0 {
                return Some(word_index * 64 + word.trailing_zeros() as usize);
            }
            word_index += 1;
            if word_index == 4 {
                return None;
            }
            word = self.summary_word(word_index);
        }
    }

    pub(crate) fn verify_local(
        self,
        base: u64,
        child_span: u64,
        limit: u64,
        page_count: u64,
    ) -> Result<(), BitmapPageError> {
        if !page::verify_crc32c(self.page) {
            return Err(BitmapPageError::Checksum);
        }
        if self.page[usize::from(BRANCH_LOWER)..]
            .iter()
            .any(|&byte| byte != 0)
        {
            return Err(BitmapPageError::ReservedNonzero);
        }

        let mut actual_nonzero = 0usize;
        for index in 0..BITMAP_FANOUT as usize {
            let child = self.child(index);
            actual_nonzero += usize::from(child != 0);
            if child != 0 && (child < 2 || u64::from(child) >= page_count) {
                return Err(BitmapPageError::ChildPageOutOfBounds(child));
            }
            let child_base = base
                .checked_add(
                    child_span
                        .checked_mul(index as u64)
                        .ok_or(BitmapPageError::ChildOutsideLimit)?,
                )
                .ok_or(BitmapPageError::ChildOutsideLimit)?;
            if child_base >= limit && (child != 0 || self.summary_bit(index)) {
                return Err(BitmapPageError::ChildOutsideLimit);
            }
        }
        if actual_nonzero != usize::from(self.header.item_count) {
            return Err(BitmapPageError::ItemCountMismatch);
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::page::{write_crc32c, PageHeader, PAGE_HEADER_SIZE};

    fn header(page_type: PageType, kind: BitmapKind, count: u16, level: u16) -> PageHeader {
        PageHeader {
            page_type,
            born_txn: 1,
            item_count: count,
            level,
            lower: if page_type == PageType::BitmapLeaf {
                LEAF_LOWER
            } else {
                BRANCH_LOWER
            },
            upper: PAGE_SIZE as u16,
            aux: kind as u32,
            page_crc32c: 0,
        }
    }

    #[test]
    fn leaf_has_exact_geometry_count_and_domain() {
        let mut page = [0u8; PAGE_SIZE];
        header(PageType::BitmapLeaf, BitmapKind::FreePages, 1, 0).encode_into(&mut page);
        page[32..40].copy_from_slice(&(1u64 << 2).to_le_bytes());
        write_crc32c(&mut page);
        let leaf = BitmapLeaf::open(&page, 1, BitmapKind::FreePages).unwrap();
        assert_eq!(leaf.word(0), 4);
        leaf.verify_local(BitmapKind::FreePages, 0, 3).unwrap();

        page[32..40].copy_from_slice(&3u64.to_le_bytes());
        write_crc32c(&mut page);
        assert_eq!(
            BitmapLeaf::open(&page, 1, BitmapKind::FreePages)
                .unwrap()
                .verify_local(BitmapKind::FreePages, 0, 3),
            Err(BitmapPageError::BitOutsideLimit)
        );
    }

    #[test]
    fn reachable_empty_leaf_is_legal() {
        let mut page = [0u8; PAGE_SIZE];
        header(PageType::BitmapLeaf, BitmapKind::FreePages, 0, 0).encode_into(&mut page);
        write_crc32c(&mut page);

        let leaf = BitmapLeaf::open(&page, 1, BitmapKind::FreePages).unwrap();
        leaf.verify_local(BitmapKind::FreePages, 0, 3).unwrap();
        assert!((0..BITMAP_LEAF_WORDS).all(|index| leaf.word(index) == 0));
    }

    #[test]
    fn branch_uses_lsb_first_summary_and_exact_child_count() {
        let mut page = [0u8; PAGE_SIZE];
        header(PageType::BitmapBranch, BitmapKind::FeedUsed, 1, 1).encode_into(&mut page);
        page[32..40].copy_from_slice(&((1u64 << 3) | (1u64 << 63)).to_le_bytes());
        page[64 + 3 * 4..64 + 4 * 4].copy_from_slice(&7u32.to_le_bytes());
        write_crc32c(&mut page);
        let branch = BitmapBranch::open(&page, 1, BitmapKind::FeedUsed).unwrap();
        assert_eq!(branch.next_summary(0), Some(3));
        assert_eq!(branch.next_summary(4), Some(63));
        assert_eq!(branch.child(3), 7);
        branch.verify_local(0, 32_000, 2_048_000, 8).unwrap();

        page[64 + 4 * 4..64 + 5 * 4].copy_from_slice(&8u32.to_le_bytes());
        write_crc32c(&mut page);
        assert_eq!(
            BitmapBranch::open(&page, 1, BitmapKind::FeedUsed)
                .unwrap()
                .verify_local(0, 32_000, 2_048_000, 9),
            Err(BitmapPageError::ItemCountMismatch)
        );
    }

    #[test]
    fn page_kind_type_and_layout_are_not_interchangeable() {
        let mut page = [0u8; PAGE_SIZE];
        header(PageType::BitmapLeaf, BitmapKind::FreePages, 1, 0).encode_into(&mut page);
        page[usize::from(PAGE_HEADER_SIZE)..usize::from(PAGE_HEADER_SIZE) + 8]
            .copy_from_slice(&1u64.to_le_bytes());
        assert!(matches!(
            BitmapLeaf::open(&page, 1, BitmapKind::FeedUsed),
            Err(BitmapPageError::WrongKind(value))
                if value == BitmapKind::FreePages as u32
        ));
    }
}
