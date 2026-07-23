//! Bounded ordinary search and destructive-free-page authorization.

use crate::bitmap_page::{BitmapBranch, BitmapKind, BitmapLeaf, BitmapPageError};
use crate::bootstrap::{self, Bootstrap, BootstrapError, OpenMode};
use crate::contract::{BITMAP_FANOUT, BITMAP_LEAF_BITS, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::page::{PageHeader, PageType};
use crate::page_source::{CommittedPageSource, PageSourceError, SlicePageSource};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum BitmapReadError {
    Bootstrap(BootstrapError),
    Source(PageSourceError),
    Page(BitmapPageError),
    PageOutOfBounds(u32),
    PageOffsetOverflow,
    RootType(PageType),
    RootLevel { expected: u16, actual: u16 },
    ChildLevel { expected: u16, actual: u16 },
    CoverageOverflow,
    SelectedChildMissing,
    SelectedCoverageOutsideLimit,
    SummaryMismatch,
}

impl From<BootstrapError> for BitmapReadError {
    fn from(value: BootstrapError) -> Self {
        Self::Bootstrap(value)
    }
}

impl From<BitmapPageError> for BitmapReadError {
    fn from(value: BitmapPageError) -> Self {
        Self::Page(value)
    }
}

impl From<PageSourceError> for BitmapReadError {
    fn from(value: PageSourceError) -> Self {
        Self::Source(value)
    }
}

#[derive(Debug)]
pub(crate) struct BitmapTree<S: CommittedPageSource> {
    pages: S,
    selected_txn: u64,
    page_count: u64,
    root: u32,
    kind: BitmapKind,
    limit: u64,
    first_eligible: u64,
}

impl<'a> BitmapTree<SlicePageSource<'a>> {
    pub(crate) fn open_immutable_free(bytes: &'a [u8]) -> Result<Self, BitmapReadError> {
        let bootstrap = bootstrap::open(bytes, OpenMode::ImmutableReader)?;
        Ok(Self::new(
            SlicePageSource::new(bytes, bootstrap.meta.page_count),
            bootstrap,
            bootstrap.meta.free_bitmap_root,
            BitmapKind::FreePages,
            bootstrap.meta.page_count,
            2,
        ))
    }
}

impl<S: CommittedPageSource> BitmapTree<S> {
    pub(crate) fn new(
        pages: S,
        bootstrap: Bootstrap,
        root: u32,
        kind: BitmapKind,
        limit: u64,
        first_eligible: u64,
    ) -> Self {
        Self {
            pages,
            selected_txn: bootstrap.meta.txn_id,
            page_count: bootstrap.meta.page_count,
            root,
            kind,
            limit,
            first_eligible,
        }
    }

    /// Find the lowest free page without verifying page CRCs.
    ///
    /// This is suitable for inspection only. A writer must use
    /// [`Self::lowest_free_verified`] before destructive reuse.
    pub(crate) fn lowest_free(&self) -> Result<Option<u64>, BitmapReadError> {
        debug_assert_eq!(self.kind, BitmapKind::FreePages);
        self.lowest_candidate(false)
    }

    /// Inspect the lowest free-page path with allocator-grade CRC and local
    /// checks. Writer integration must retain these verified pages in its
    /// transaction-owned COW state so each committed page is checked at most
    /// once before destructive reuse.
    pub(crate) fn lowest_free_verified(&self) -> Result<Option<u64>, BitmapReadError> {
        debug_assert_eq!(self.kind, BitmapKind::FreePages);
        self.lowest_candidate(true)
    }

    pub(crate) fn lowest_unused(&self) -> Result<Option<u64>, BitmapReadError> {
        debug_assert!(matches!(
            self.kind,
            BitmapKind::FeedUsed | BitmapKind::MembershipUsed
        ));
        self.lowest_candidate(false)
    }

    fn lowest_candidate(&self, verify_selected_path: bool) -> Result<Option<u64>, BitmapReadError> {
        self.pages.check_access()?;
        let start = self.first_eligible;
        if start >= self.limit {
            return Ok(None);
        }
        if self.root == 0 {
            return Ok(match self.kind {
                BitmapKind::FreePages => None,
                BitmapKind::FeedUsed | BitmapKind::MembershipUsed => Some(start),
            });
        }

        let expected_root_level = minimum_level(self.limit)?;
        let mut pgno = self.root;
        let mut level = expected_root_level;
        let mut base = 0u64;
        let mut selected_by_summary = false;
        let mut page = [0u8; PAGE_SIZE];
        let mut root = true;
        loop {
            self.pages.read_page(pgno, &mut page)?;
            let header =
                PageHeader::decode(&page, self.selected_txn).map_err(BitmapPageError::from)?;
            let actual_level = match header.page_type {
                PageType::BitmapLeaf => 0,
                PageType::BitmapBranch => header.level,
                other => return Err(BitmapReadError::RootType(other)),
            };
            if actual_level != level {
                return Err(if root {
                    BitmapReadError::RootLevel {
                        expected: level,
                        actual: actual_level,
                    }
                } else {
                    BitmapReadError::ChildLevel {
                        expected: level,
                        actual: actual_level,
                    }
                });
            }
            root = false;
            if level == 0 {
                let leaf = BitmapLeaf::open(&page, self.selected_txn, self.kind)?;
                if verify_selected_path {
                    leaf.verify_local(self.kind, base, self.limit)?;
                }
                let found = self.search_leaf(leaf, base, start)?;
                if found.is_none() && selected_by_summary {
                    return Err(BitmapReadError::SummaryMismatch);
                }
                return Ok(found);
            }

            let branch = BitmapBranch::open(&page, self.selected_txn, self.kind)?;
            let child_span = coverage(level - 1)?;
            if verify_selected_path {
                branch.verify_local(base, child_span, self.limit, self.page_count)?;
            }
            let first_child = if start <= base {
                0
            } else {
                usize::try_from((start - base) / child_span)
                    .map_err(|_| BitmapReadError::CoverageOverflow)?
            };
            let Some(index) = branch.next_summary(first_child) else {
                return if selected_by_summary {
                    Err(BitmapReadError::SummaryMismatch)
                } else {
                    Ok(None)
                };
            };
            let child_base = base
                .checked_add(
                    child_span
                        .checked_mul(index as u64)
                        .ok_or(BitmapReadError::CoverageOverflow)?,
                )
                .ok_or(BitmapReadError::CoverageOverflow)?;
            if child_base >= self.limit {
                return Err(BitmapReadError::SelectedCoverageOutsideLimit);
            }
            let child = branch.child(index);
            if child == 0 {
                if self.kind == BitmapKind::FreePages {
                    return Err(BitmapReadError::SelectedChildMissing);
                }
                return Ok(Some(core::cmp::max(start, child_base)));
            }
            pgno = child;
            level -= 1;
            base = child_base;
            selected_by_summary = true;
        }
    }

    fn search_leaf(
        &self,
        leaf: BitmapLeaf<'_>,
        base: u64,
        start: u64,
    ) -> Result<Option<u64>, BitmapReadError> {
        let first = core::cmp::max(start, base);
        if first >= self.limit {
            return Ok(None);
        }
        let local = first - base;
        let mut word_index =
            usize::try_from(local / 64).map_err(|_| BitmapReadError::CoverageOverflow)?;
        let first_bit = (local % 64) as u32;
        while word_index < crate::contract::BITMAP_LEAF_WORDS {
            let word_base = base
                .checked_add((word_index as u64) * 64)
                .ok_or(BitmapReadError::CoverageOverflow)?;
            if word_base >= self.limit {
                break;
            }
            let stored = leaf.word(word_index);
            let mut candidates = match self.kind {
                BitmapKind::FreePages => stored,
                BitmapKind::FeedUsed | BitmapKind::MembershipUsed => !stored,
            };
            if word_index == usize::try_from(local / 64).unwrap() {
                candidates &= u64::MAX << first_bit;
            }
            let remaining = self.limit - word_base;
            if remaining < 64 {
                candidates &= (1u64 << remaining) - 1;
            }
            if candidates != 0 {
                let bit = u64::from(candidates.trailing_zeros());
                return word_base
                    .checked_add(bit)
                    .map(Some)
                    .ok_or(BitmapReadError::CoverageOverflow);
            }
            word_index += 1;
        }
        Ok(None)
    }
}

fn minimum_level(limit: u64) -> Result<u16, BitmapReadError> {
    let mut level = 0u16;
    let mut covered = BITMAP_LEAF_BITS;
    while covered < limit {
        if level == MAX_TREE_LEVEL {
            return Err(BitmapReadError::CoverageOverflow);
        }
        covered = covered
            .checked_mul(BITMAP_FANOUT)
            .ok_or(BitmapReadError::CoverageOverflow)?;
        level += 1;
    }
    Ok(level)
}

fn coverage(level: u16) -> Result<u64, BitmapReadError> {
    let mut covered = BITMAP_LEAF_BITS;
    for _ in 0..level {
        covered = covered
            .checked_mul(BITMAP_FANOUT)
            .ok_or(BitmapReadError::CoverageOverflow)?;
    }
    Ok(covered)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bitmap_page::BitmapKind;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::contract::MetaV4;
    use crate::page::{write_crc32c, PageHeader};
    use crate::test_alloc::count_thread_allocations;
    use core::cell::Cell;
    use std::{vec, vec::Vec};

    struct FailingSource {
        access: Option<PageSourceError>,
        read: Option<PageSourceError>,
    }

    impl CommittedPageSource for FailingSource {
        fn check_access(&self) -> Result<(), PageSourceError> {
            self.access.map_or(Ok(()), Err)
        }

        fn read_page(&self, _: u32, _: &mut [u8; PAGE_SIZE]) -> Result<(), PageSourceError> {
            self.read.map_or(Ok(()), Err)
        }
    }

    struct TornSource<'a> {
        bytes: &'a [u8],
        torn: Cell<bool>,
        page_count: u64,
    }

    impl CommittedPageSource for TornSource<'_> {
        fn read_page(
            &self,
            pgno: u32,
            destination: &mut [u8; PAGE_SIZE],
        ) -> Result<(), PageSourceError> {
            SlicePageSource::new(self.bytes, self.page_count).read_page(pgno, destination)?;
            if pgno == 2 && !self.torn.replace(true) {
                destination[64..68].copy_from_slice(&u32::MAX.to_le_bytes());
            }
            Ok(())
        }
    }

    fn page_mut(bytes: &mut [u8], pgno: usize) -> &mut [u8; PAGE_SIZE] {
        (&mut bytes[pgno * PAGE_SIZE..(pgno + 1) * PAGE_SIZE])
            .try_into()
            .unwrap()
    }

    fn leaf(page: &mut [u8; PAGE_SIZE], kind: BitmapKind, words: &[(usize, u64)]) {
        let count = words.iter().filter(|(_, word)| *word != 0).count();
        PageHeader {
            page_type: PageType::BitmapLeaf,
            born_txn: 1,
            item_count: count as u16,
            level: 0,
            lower: 4032,
            upper: PAGE_SIZE as u16,
            aux: kind as u32,
            page_crc32c: 0,
        }
        .encode_into(page);
        for &(index, word) in words {
            page[32 + index * 8..40 + index * 8].copy_from_slice(&word.to_le_bytes());
        }
        write_crc32c(page);
    }

    fn branch(
        page: &mut [u8; PAGE_SIZE],
        kind: BitmapKind,
        level: u16,
        summary: &[usize],
        children: &[(usize, u32)],
    ) {
        PageHeader {
            page_type: PageType::BitmapBranch,
            born_txn: 1,
            item_count: children.iter().filter(|(_, child)| *child != 0).count() as u16,
            level,
            lower: 1088,
            upper: PAGE_SIZE as u16,
            aux: kind as u32,
            page_crc32c: 0,
        }
        .encode_into(page);
        for &index in summary {
            let at = 32 + (index / 64) * 8;
            let word = crate::contract::u64_le(page, at) | (1u64 << (index % 64));
            page[at..at + 8].copy_from_slice(&word.to_le_bytes());
        }
        for &(index, child) in children {
            page[64 + index * 4..68 + index * 4].copy_from_slice(&child.to_le_bytes());
        }
        write_crc32c(page);
    }

    fn image(mut meta: MetaV4, pages: usize, fill: impl FnOnce(&mut [u8])) -> Vec<u8> {
        meta.page_count = pages as u64;
        let mut bytes = vec![0u8; pages * PAGE_SIZE];
        meta.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into((&mut bytes[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
        fill(&mut bytes);
        bytes
    }

    fn logical_tree<'a>(
        bytes: &'a [u8],
        root: u32,
        kind: BitmapKind,
        limit: u64,
        first: u64,
    ) -> BitmapTree<SlicePageSource<'a>> {
        let bootstrap = bootstrap::open(bytes, OpenMode::ImmutableReader).unwrap();
        BitmapTree::new(
            SlicePageSource::new(bytes, bootstrap.meta.page_count),
            bootstrap,
            root,
            kind,
            limit,
            first,
        )
    }

    #[test]
    fn reachable_empty_free_leaf_traverses_as_no_free_page() {
        let mut meta = empty_direct_meta(1);
        meta.free_bitmap_root = 2;
        let bytes = image(meta, 3, |bytes| {
            leaf(page_mut(bytes, 2), BitmapKind::FreePages, &[]);
        });
        let tree = BitmapTree::open_immutable_free(&bytes).unwrap();

        assert_eq!(tree.lowest_free().unwrap(), None);
        assert_eq!(tree.lowest_free_verified().unwrap(), None);
    }

    #[test]
    fn free_leaf_finds_reusable_page_and_ignores_crc_only_when_unverified() {
        let mut meta = empty_direct_meta(1);
        meta.free_bitmap_root = 2;
        let mut bytes = image(meta, 431, |bytes| {
            leaf(
                page_mut(bytes, 2),
                BitmapKind::FreePages,
                &[(6, 1u64 << 45)],
            );
        });
        let tree = BitmapTree::open_immutable_free(&bytes).unwrap();
        assert_eq!(tree.lowest_free().unwrap(), Some(429));
        assert_eq!(tree.lowest_free_verified().unwrap(), Some(429));

        bytes[2 * PAGE_SIZE + 28] ^= 1;
        let tree = BitmapTree::open_immutable_free(&bytes).unwrap();
        assert_eq!(tree.lowest_free().unwrap(), Some(429));
        assert_eq!(
            tree.lowest_free_verified(),
            Err(BitmapReadError::Page(BitmapPageError::Checksum))
        );
    }

    #[test]
    fn verified_free_path_rejects_meta_bits_even_when_later_candidate_exists() {
        let mut meta = empty_direct_meta(1);
        meta.free_bitmap_root = 2;
        let bytes = image(meta, 3, |bytes| {
            leaf(page_mut(bytes, 2), BitmapKind::FreePages, &[(0, 0b110)]);
        });
        let tree = BitmapTree::open_immutable_free(&bytes).unwrap();
        assert_eq!(tree.lowest_free().unwrap(), Some(2));
        assert_eq!(
            tree.lowest_free_verified(),
            Err(BitmapReadError::Page(BitmapPageError::BitOutsideLimit))
        );
    }

    #[test]
    fn used_bitmap_absent_child_is_logically_zero_and_candidate() {
        let meta = empty_direct_meta(1);
        let bytes = image(meta, 4, |bytes| {
            branch(page_mut(bytes, 2), BitmapKind::FeedUsed, 1, &[0], &[(1, 3)]);
            leaf(page_mut(bytes, 3), BitmapKind::FeedUsed, &[(0, 1)]);
        });
        let tree = logical_tree(&bytes, 2, BitmapKind::FeedUsed, 32_001, 0);
        assert_eq!(tree.lowest_unused().unwrap(), Some(0));
    }

    #[test]
    fn membership_id_zero_is_never_an_allocation_candidate() {
        let meta = empty_direct_meta(1);
        let bytes = image(meta, 3, |bytes| {
            leaf(page_mut(bytes, 2), BitmapKind::MembershipUsed, &[(0, 0b10)]);
        });
        let tree = logical_tree(&bytes, 2, BitmapKind::MembershipUsed, 10, 1);
        assert_eq!(tree.lowest_unused().unwrap(), Some(2));
    }

    #[test]
    fn selected_free_summary_requires_a_child_and_exact_minimum_root_level() {
        let meta = empty_direct_meta(1);
        let bytes = image(meta, 4, |bytes| {
            branch(
                page_mut(bytes, 2),
                BitmapKind::FreePages,
                1,
                &[0],
                &[(1, 3)],
            );
            leaf(page_mut(bytes, 3), BitmapKind::FreePages, &[(0, 1u64 << 2)]);
        });
        let tree = logical_tree(&bytes, 2, BitmapKind::FreePages, 32_001, 2);
        assert_eq!(
            tree.lowest_free(),
            Err(BitmapReadError::SelectedChildMissing)
        );

        let mut bytes = bytes;
        leaf(
            page_mut(&mut bytes, 2),
            BitmapKind::FreePages,
            &[(0, 1u64 << 2)],
        );
        let tree = logical_tree(&bytes, 2, BitmapKind::FreePages, 32_001, 2);
        assert_eq!(
            tree.lowest_free(),
            Err(BitmapReadError::RootLevel {
                expected: 1,
                actual: 0
            })
        );
    }

    #[test]
    fn verified_multilevel_path_checks_every_pointer_on_selected_branch() {
        let meta = empty_direct_meta(1);
        let mut bytes = image(meta, 4, |bytes| {
            branch(
                page_mut(bytes, 2),
                BitmapKind::FreePages,
                1,
                &[0],
                &[(0, 3)],
            );
            leaf(page_mut(bytes, 3), BitmapKind::FreePages, &[(0, 1u64 << 2)]);
        });
        let tree = logical_tree(&bytes, 2, BitmapKind::FreePages, 32_001, 2);
        assert_eq!(tree.lowest_free_verified().unwrap(), Some(2));

        let root = page_mut(&mut bytes, 2);
        root[64 + 4..64 + 8].copy_from_slice(&1u32.to_le_bytes());
        root[16..18].copy_from_slice(&2u16.to_le_bytes());
        write_crc32c(root);
        let tree = logical_tree(&bytes, 2, BitmapKind::FreePages, 32_001, 2);
        assert_eq!(
            tree.lowest_free_verified(),
            Err(BitmapReadError::Page(
                BitmapPageError::ChildPageOutOfBounds(1)
            ))
        );
    }

    #[test]
    fn minimum_level_and_coverage_are_checked() {
        assert_eq!(minimum_level(0).unwrap(), 0);
        assert_eq!(minimum_level(32_000).unwrap(), 0);
        assert_eq!(minimum_level(32_001).unwrap(), 1);
        assert_eq!(minimum_level(1u64 << 32).unwrap(), 3);
        assert_eq!(coverage(0).unwrap(), 32_000);
        assert_eq!(coverage(1).unwrap(), 8_192_000);
    }

    #[test]
    fn positional_source_preserves_fork_io_and_torn_page_evidence() {
        let bytes = image(empty_direct_meta(1), 4, |bytes| {
            branch(
                page_mut(bytes, 2),
                BitmapKind::FreePages,
                1,
                &[0],
                &[(0, 3)],
            );
            leaf(page_mut(bytes, 3), BitmapKind::FreePages, &[(0, 1u64 << 2)]);
        });
        let bootstrap = bootstrap::open(&bytes, OpenMode::ImmutableReader).unwrap();

        let forked = FailingSource {
            access: Some(PageSourceError::ForkedHandle),
            read: None,
        };
        let tree = BitmapTree::new(&forked, bootstrap, 0, BitmapKind::FreePages, 4, 2);
        assert_eq!(
            tree.lowest_free(),
            Err(BitmapReadError::Source(PageSourceError::ForkedHandle))
        );

        let io = PageSourceError::Io(crate::page_source::PageIoEvidence {
            kind: crate::page_source::PageIoKind::PermissionDenied,
            raw_os_error: Some(13),
        });
        let failing = FailingSource {
            access: None,
            read: Some(io),
        };
        let tree = BitmapTree::new(&failing, bootstrap, 2, BitmapKind::FreePages, 32_001, 2);
        assert_eq!(tree.lowest_free(), Err(BitmapReadError::Source(io)));

        let torn = TornSource {
            bytes: &bytes,
            torn: Cell::new(false),
            page_count: 4,
        };
        let tree = BitmapTree::new(&torn, bootstrap, 2, BitmapKind::FreePages, 32_001, 2);
        assert_eq!(
            tree.lowest_free(),
            Err(BitmapReadError::Source(PageSourceError::PageOutOfBounds(
                u32::MAX
            )))
        );
    }

    #[test]
    fn warmed_positional_bitmap_search_allocates_nothing() {
        let bytes = image(empty_direct_meta(1), 3, |bytes| {
            leaf(page_mut(bytes, 2), BitmapKind::FreePages, &[(0, 1u64 << 2)]);
        });
        let tree = logical_tree(&bytes, 2, BitmapKind::FreePages, 3, 2);
        assert_eq!(tree.lowest_free().unwrap(), Some(2));
        let (result, allocations) = count_thread_allocations(|| {
            for _ in 0..128 {
                assert_eq!(tree.lowest_free().unwrap(), Some(2));
            }
        });
        assert_eq!(result, ());
        assert_eq!(allocations, 0);
    }
}
