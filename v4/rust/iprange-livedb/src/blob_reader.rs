//! Bounded caller-buffered traversal of exact v4 generic blob trees.

use crate::blob_page::{
    BlobBranch, BlobKind, BlobLeaf, BlobPageError, BLOB_LEAF_CAPACITY, BLOB_LEAF_DATA_OFFSET,
};
use crate::contract::{MAX_TREE_LEVEL, PAGE_SIZE};
use crate::page::{PageHeader, PageType};
use crate::page_source::{CommittedPageSource, PageSourceError, SlicePageSource};

const PATH_CAPACITY: usize = MAX_TREE_LEVEL as usize + 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum BlobPageCheck {
    Ordinary,
    VerifyCrc,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum BlobReadError {
    Source(PageSourceError),
    Page(BlobPageError),
    PageOutOfBounds(u32),
    PageOffsetOverflow,
    OwnerLengthZero,
    OwnerLengthAlignment { length: u64, alignment: u64 },
    OwnerLengthTooLarge,
    RootType(PageType),
    ChildType(PageType),
    ChildLevel { expected: u16, actual: u16 },
    OffsetMismatch { expected: u64, actual: u64 },
    LogicalOffsetOverflow,
    ArithmeticOverflow,
    RequestOutsideLength(u64),
    NonfinalLeafLength(u16),
    LengthExceeded { declared: u64, actual: u64 },
    LengthShort { declared: u64, actual: u64 },
    TrailingData,
    RetirementPageOrder { previous: u32, current: u32 },
    RetirementPageOutOfBounds(u32),
    WrongRetirementStreamKind(BlobKind),
    PathChanged(u32),
    CursorFailed,
}

impl From<BlobPageError> for BlobReadError {
    fn from(value: BlobPageError) -> Self {
        Self::Page(value)
    }
}

impl From<PageSourceError> for BlobReadError {
    fn from(value: PageSourceError) -> Self {
        Self::Source(value)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct BlobChunk<'a> {
    pub(crate) logical_offset: u64,
    pub(crate) data: &'a [u8],
}

#[derive(Debug)]
pub(crate) struct BlobTree<S: CommittedPageSource> {
    pages: S,
    selected_txn: u64,
    page_count: u64,
    root: u32,
    kind: BlobKind,
    length: u64,
}

impl<'a> BlobTree<SlicePageSource<'a>> {
    pub(crate) fn new(
        bytes: &'a [u8],
        selected_txn: u64,
        page_count: u64,
        root: u32,
        kind: BlobKind,
        length: u64,
    ) -> Result<Self, BlobReadError> {
        let committed_u64 = page_count
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(BlobReadError::PageOffsetOverflow)?;
        let committed =
            usize::try_from(committed_u64).map_err(|_| BlobReadError::PageOffsetOverflow)?;
        if committed > bytes.len() {
            return Err(BlobReadError::PageOutOfBounds(root));
        }
        Self::from_source(
            SlicePageSource::new(&bytes[..committed], page_count),
            selected_txn,
            page_count,
            root,
            kind,
            length,
        )
    }
}

impl<S: CommittedPageSource> BlobTree<S> {
    pub(crate) fn from_source(
        pages: S,
        selected_txn: u64,
        page_count: u64,
        root: u32,
        kind: BlobKind,
        length: u64,
    ) -> Result<Self, BlobReadError> {
        if length == 0 {
            return Err(BlobReadError::OwnerLengthZero);
        }
        let alignment = kind.alignment();
        if length % alignment != 0 {
            return Err(BlobReadError::OwnerLengthAlignment { length, alignment });
        }
        if root < 2 || u64::from(root) >= page_count {
            return Err(BlobReadError::PageOutOfBounds(root));
        }
        let maximum_length = page_count
            .checked_sub(2)
            .and_then(|pages| pages.checked_mul(u64::from(BLOB_LEAF_CAPACITY)))
            .ok_or(BlobReadError::OwnerLengthTooLarge)?;
        if length > maximum_length {
            return Err(BlobReadError::OwnerLengthTooLarge);
        }
        Ok(Self {
            pages,
            selected_txn,
            page_count,
            root,
            kind,
            length,
        })
    }

    pub(crate) fn stream(&self, check: BlobPageCheck) -> BlobReader<'_, S> {
        BlobReader {
            tree: self,
            check,
            path: [BlobFrame {
                pgno: 0,
                index: 0,
                len: 0,
            }; PATH_CAPACITY],
            root_level: 0,
            next_offset: 0,
            previous_retirement_page: None,
            scratch: [0; PAGE_SIZE],
            state: BlobReaderState::Unpositioned,
        }
    }

    pub(crate) fn retirement_pages(
        &self,
        check: BlobPageCheck,
    ) -> Result<BlobRetirementPageReader<'_, S>, BlobReadError> {
        if self.kind != BlobKind::RetirementPageList {
            return Err(BlobReadError::WrongRetirementStreamKind(self.kind));
        }
        Ok(BlobRetirementPageReader {
            reader: self.stream(check),
            byte_offset: 0,
            data_len: 0,
        })
    }

    pub(crate) fn chunk_at<'page>(
        &self,
        logical_offset: u64,
        check: BlobPageCheck,
        page: &'page mut [u8; PAGE_SIZE],
    ) -> Result<BlobChunk<'page>, BlobReadError> {
        self.pages.check_access()?;
        if logical_offset >= self.length {
            return Err(BlobReadError::RequestOutsideLength(logical_offset));
        }
        let mut pgno = self.root;
        let mut level = self.root_level()?;
        let mut expected_offset = 0u64;
        let mut has_successor = false;
        let mut root = true;

        loop {
            self.pages.read_page(pgno, page)?;
            let header =
                PageHeader::decode(page, self.selected_txn).map_err(BlobPageError::from)?;
            if level == 0 {
                if header.page_type != PageType::BlobLeaf {
                    return Err(if root {
                        BlobReadError::RootType(header.page_type)
                    } else {
                        BlobReadError::ChildType(header.page_type)
                    });
                }
                let leaf = self.leaf(page, check)?;
                if leaf.logical_offset() != expected_offset {
                    return Err(BlobReadError::OffsetMismatch {
                        expected: expected_offset,
                        actual: leaf.logical_offset(),
                    });
                }
                let end = leaf
                    .logical_offset()
                    .checked_add(u64::from(leaf.data_len()))
                    .ok_or(BlobReadError::LogicalOffsetOverflow)?;
                self.check_leaf_end(leaf, end, has_successor)?;
                if logical_offset < leaf.logical_offset() || logical_offset >= end {
                    return Err(BlobReadError::OffsetMismatch {
                        expected: logical_offset,
                        actual: leaf.logical_offset(),
                    });
                }
                let _ = self.check_retirement_chunk(leaf.data(), None)?;
                return Ok(BlobChunk {
                    logical_offset: leaf.logical_offset(),
                    data: leaf.data(),
                });
            }

            if header.page_type != PageType::BlobBranch {
                return Err(if root {
                    BlobReadError::RootType(header.page_type)
                } else {
                    BlobReadError::ChildType(header.page_type)
                });
            }
            root = false;
            let branch = self.branch(page, level, check)?;
            let first = branch.entry(0)?;
            if first.logical_offset != expected_offset {
                return Err(BlobReadError::OffsetMismatch {
                    expected: expected_offset,
                    actual: first.logical_offset,
                });
            }
            let index = branch.predecessor_for(logical_offset)?;
            has_successor |= index + 1 < branch.len();
            let entry = branch.entry(index)?;
            expected_offset = entry.logical_offset;
            pgno = entry.child_pgno;
            level -= 1;
        }
    }

    fn check_leaf_end(
        &self,
        leaf: BlobLeaf<'_>,
        end: u64,
        has_successor: bool,
    ) -> Result<(), BlobReadError> {
        if end > self.length {
            return Err(BlobReadError::LengthExceeded {
                declared: self.length,
                actual: end,
            });
        }
        if end < self.length {
            if leaf.data_len() != BLOB_LEAF_CAPACITY {
                return Err(BlobReadError::NonfinalLeafLength(leaf.data_len()));
            }
            if !has_successor {
                return Err(BlobReadError::LengthShort {
                    declared: self.length,
                    actual: end,
                });
            }
        } else if has_successor {
            return Err(BlobReadError::TrailingData);
        }
        Ok(())
    }

    fn check_retirement_chunk(
        &self,
        data: &[u8],
        mut previous: Option<u32>,
    ) -> Result<Option<u32>, BlobReadError> {
        if self.kind != BlobKind::RetirementPageList {
            return Ok(previous);
        }
        for bytes in data.chunks_exact(4) {
            let current = u32::from_le_bytes(bytes.try_into().unwrap());
            if current < 2 || u64::from(current) >= self.page_count {
                return Err(BlobReadError::RetirementPageOutOfBounds(current));
            }
            if let Some(prior) = previous {
                if current <= prior {
                    return Err(BlobReadError::RetirementPageOrder {
                        previous: prior,
                        current,
                    });
                }
            }
            previous = Some(current);
        }
        Ok(previous)
    }

    fn root_level(&self) -> Result<u16, BlobReadError> {
        let mut page = [0u8; PAGE_SIZE];
        self.pages.read_page(self.root, &mut page)?;
        let header = PageHeader::decode(&page, self.selected_txn).map_err(BlobPageError::from)?;
        match header.page_type {
            PageType::BlobLeaf => Ok(0),
            PageType::BlobBranch => Ok(header.level),
            other => Err(BlobReadError::RootType(other)),
        }
    }

    fn branch<'page>(
        &self,
        page: &'page [u8; PAGE_SIZE],
        expected_level: u16,
        check: BlobPageCheck,
    ) -> Result<BlobBranch<'page>, BlobReadError> {
        let branch = BlobBranch::open(page, self.selected_txn, self.kind, self.page_count)?;
        if branch.level() != expected_level {
            return Err(BlobReadError::ChildLevel {
                expected: expected_level,
                actual: branch.level(),
            });
        }
        if check == BlobPageCheck::VerifyCrc {
            branch.verify_local()?;
        }
        Ok(branch)
    }

    fn leaf<'page>(
        &self,
        page: &'page [u8; PAGE_SIZE],
        check: BlobPageCheck,
    ) -> Result<BlobLeaf<'page>, BlobReadError> {
        let leaf = BlobLeaf::open(page, self.selected_txn, self.kind)?;
        if check == BlobPageCheck::VerifyCrc {
            leaf.verify_local()?;
        }
        Ok(leaf)
    }
}

#[derive(Clone, Copy, Debug)]
struct BlobFrame {
    pgno: u32,
    index: u16,
    len: u16,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum BlobReaderState {
    Unpositioned,
    Ready,
    Yielded,
    Done,
    Failed,
}

#[derive(Debug)]
pub(crate) struct BlobReader<'tree, S: CommittedPageSource> {
    tree: &'tree BlobTree<S>,
    check: BlobPageCheck,
    path: [BlobFrame; PATH_CAPACITY],
    root_level: u16,
    next_offset: u64,
    previous_retirement_page: Option<u32>,
    scratch: [u8; PAGE_SIZE],
    state: BlobReaderState,
}

impl<S: CommittedPageSource> BlobReader<'_, S> {
    pub(crate) fn next_chunk(&mut self) -> Result<Option<BlobChunk<'_>>, BlobReadError> {
        if self.state == BlobReaderState::Failed {
            return Err(BlobReadError::CursorFailed);
        }
        if let Err(error) = self.tree.pages.check_access() {
            self.state = BlobReaderState::Failed;
            return Err(error.into());
        }
        match self.prepare_next_chunk() {
            Ok(Some((logical_offset, data_len))) => {
                self.state = BlobReaderState::Yielded;
                Ok(Some(BlobChunk {
                    logical_offset,
                    data: &self.scratch
                        [BLOB_LEAF_DATA_OFFSET..BLOB_LEAF_DATA_OFFSET + usize::from(data_len)],
                }))
            }
            Ok(None) => Ok(None),
            Err(error) => {
                self.state = BlobReaderState::Failed;
                Err(error)
            }
        }
    }

    fn prepare_next_chunk(&mut self) -> Result<Option<(u64, u16)>, BlobReadError> {
        match self.state {
            BlobReaderState::Unpositioned => {
                self.load_page(self.tree.root)?;
                let header = PageHeader::decode(&self.scratch, self.tree.selected_txn)
                    .map_err(BlobPageError::from)?;
                self.root_level = match header.page_type {
                    PageType::BlobLeaf => 0,
                    PageType::BlobBranch => header.level,
                    other => return Err(BlobReadError::RootType(other)),
                };
                self.descend_first(0, self.tree.root, self.root_level, 0)?;
            }
            BlobReaderState::Done => return Ok(None),
            BlobReaderState::Failed => return Err(BlobReadError::CursorFailed),
            BlobReaderState::Yielded => {
                if !self.advance()? {
                    return Ok(None);
                }
            }
            BlobReaderState::Ready => {}
        }

        let leaf = self.tree.leaf(&self.scratch, self.check)?;
        if leaf.logical_offset() != self.next_offset {
            return Err(BlobReadError::OffsetMismatch {
                expected: self.next_offset,
                actual: leaf.logical_offset(),
            });
        }
        let end = leaf
            .logical_offset()
            .checked_add(u64::from(leaf.data_len()))
            .ok_or(BlobReadError::LogicalOffsetOverflow)?;
        if end > self.tree.length {
            return Err(BlobReadError::LengthExceeded {
                declared: self.tree.length,
                actual: end,
            });
        }
        if end < self.tree.length && leaf.data_len() != BLOB_LEAF_CAPACITY {
            return Err(BlobReadError::NonfinalLeafLength(leaf.data_len()));
        }

        self.previous_retirement_page = self
            .tree
            .check_retirement_chunk(leaf.data(), self.previous_retirement_page)?;
        let has_successor = self.has_successor();
        if end == self.tree.length {
            if has_successor {
                return Err(BlobReadError::TrailingData);
            }
        } else if !has_successor {
            return Err(BlobReadError::LengthShort {
                declared: self.tree.length,
                actual: end,
            });
        }
        self.next_offset = end;
        Ok(Some((leaf.logical_offset(), leaf.data_len())))
    }

    fn descend_first(
        &mut self,
        mut depth: usize,
        mut pgno: u32,
        mut level: u16,
        mut expected_offset: u64,
    ) -> Result<(), BlobReadError> {
        loop {
            if depth >= PATH_CAPACITY {
                return Err(BlobReadError::ChildLevel {
                    expected: 0,
                    actual: level,
                });
            }
            if !(depth == 0 && pgno == self.tree.root) {
                self.load_page(pgno)?;
            }
            let header = PageHeader::decode(&self.scratch, self.tree.selected_txn)
                .map_err(BlobPageError::from)?;
            if level == 0 {
                if header.page_type != PageType::BlobLeaf {
                    return Err(BlobReadError::ChildType(header.page_type));
                }
                let leaf = self.tree.leaf(&self.scratch, self.check)?;
                if leaf.logical_offset() != expected_offset {
                    return Err(BlobReadError::OffsetMismatch {
                        expected: expected_offset,
                        actual: leaf.logical_offset(),
                    });
                }
                self.state = BlobReaderState::Ready;
                return Ok(());
            }

            if header.page_type != PageType::BlobBranch {
                return Err(BlobReadError::ChildType(header.page_type));
            }
            let branch = self.tree.branch(&self.scratch, level, self.check)?;
            let entry = branch.entry(0)?;
            if entry.logical_offset != expected_offset {
                return Err(BlobReadError::OffsetMismatch {
                    expected: expected_offset,
                    actual: entry.logical_offset,
                });
            }
            self.path[depth] = BlobFrame {
                pgno,
                index: 0,
                len: u16::try_from(branch.len()).map_err(|_| BlobReadError::ArithmeticOverflow)?,
            };
            pgno = entry.child_pgno;
            expected_offset = entry.logical_offset;
            level -= 1;
            depth += 1;
        }
    }

    fn advance(&mut self) -> Result<bool, BlobReadError> {
        let leaf_depth = usize::from(self.root_level);
        for depth in (0..leaf_depth).rev() {
            let frame = self.path[depth];
            let level = self.root_level - depth as u16;
            self.load_page(frame.pgno)?;
            let branch = self.tree.branch(&self.scratch, level, self.check)?;
            if branch.len() != usize::from(frame.len) {
                return Err(BlobReadError::PathChanged(frame.pgno));
            }
            let next_index = usize::from(frame.index) + 1;
            if next_index == branch.len() {
                continue;
            }
            self.path[depth].index = next_index as u16;
            let entry = branch.entry(next_index)?;
            self.descend_first(depth + 1, entry.child_pgno, level - 1, entry.logical_offset)?;
            return Ok(true);
        }
        self.state = BlobReaderState::Done;
        Ok(false)
    }

    fn has_successor(&self) -> bool {
        self.path[..usize::from(self.root_level)]
            .iter()
            .any(|frame| usize::from(frame.index) + 1 < usize::from(frame.len))
    }

    fn load_page(&mut self, pgno: u32) -> Result<(), BlobReadError> {
        self.tree.pages.read_page(pgno, &mut self.scratch)?;
        Ok(())
    }
}

#[derive(Debug)]
pub(crate) struct BlobRetirementPageReader<'tree, S: CommittedPageSource> {
    reader: BlobReader<'tree, S>,
    byte_offset: usize,
    data_len: usize,
}

impl<S: CommittedPageSource> BlobRetirementPageReader<'_, S> {
    /// Yield one copied page number. No view into the cursor's reusable page
    /// buffer escapes this call.
    pub(crate) fn next_page(&mut self) -> Result<Option<u32>, BlobReadError> {
        if self.reader.state == BlobReaderState::Failed {
            return Err(BlobReadError::CursorFailed);
        }
        if let Err(error) = self.reader.tree.pages.check_access() {
            self.reader.state = BlobReaderState::Failed;
            return Err(error.into());
        }
        if self.byte_offset == self.data_len {
            match self.reader.prepare_next_chunk() {
                Ok(Some((_, data_len))) => {
                    self.reader.state = BlobReaderState::Yielded;
                    self.byte_offset = 0;
                    self.data_len = usize::from(data_len);
                }
                Ok(None) => return Ok(None),
                Err(error) => {
                    self.reader.state = BlobReaderState::Failed;
                    return Err(error);
                }
            }
        }
        let start = BLOB_LEAF_DATA_OFFSET + self.byte_offset;
        let page = u32::from_le_bytes(
            self.reader.scratch[start..start + 4]
                .try_into()
                .expect("retirement blob alignment was validated"),
        );
        self.byte_offset += 4;
        Ok(Some(page))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::page::{write_crc32c, PageHeader, PAGE_CRC_OFFSET, PAGE_HEADER_SIZE};
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
                destination[40..44].copy_from_slice(&u32::MAX.to_le_bytes());
            }
            Ok(())
        }
    }

    fn page_mut(bytes: &mut [u8], pgno: usize) -> &mut [u8; PAGE_SIZE] {
        (&mut bytes[pgno * PAGE_SIZE..(pgno + 1) * PAGE_SIZE])
            .try_into()
            .unwrap()
    }

    fn put_leaf(
        page: &mut [u8; PAGE_SIZE],
        kind: BlobKind,
        logical_offset: u64,
        data_len: u16,
        value: u8,
    ) {
        PageHeader {
            page_type: PageType::BlobLeaf,
            born_txn: 1,
            item_count: 1,
            level: 0,
            lower: 48 + data_len,
            upper: PAGE_SIZE as u16,
            aux: kind as u32,
            page_crc32c: 0,
        }
        .encode_into(page);
        page[32..40].copy_from_slice(&logical_offset.to_le_bytes());
        page[40..42].copy_from_slice(&data_len.to_le_bytes());
        page[48..48 + usize::from(data_len)].fill(value);
        write_crc32c(page);
    }

    fn put_branch(page: &mut [u8; PAGE_SIZE], kind: BlobKind, level: u16, entries: &[(u64, u32)]) {
        PageHeader {
            page_type: PageType::BlobBranch,
            born_txn: 1,
            item_count: entries.len() as u16,
            level,
            lower: (usize::from(PAGE_HEADER_SIZE) + entries.len() * 16) as u16,
            upper: PAGE_SIZE as u16,
            aux: kind as u32,
            page_crc32c: 0,
        }
        .encode_into(page);
        for (index, &(logical_offset, child_pgno)) in entries.iter().enumerate() {
            let at = usize::from(PAGE_HEADER_SIZE) + index * 16;
            page[at..at + 8].copy_from_slice(&logical_offset.to_le_bytes());
            page[at + 8..at + 12].copy_from_slice(&child_pgno.to_le_bytes());
        }
        write_crc32c(page);
    }

    fn image(pages: usize, fill: impl FnOnce(&mut [u8])) -> Vec<u8> {
        let mut bytes = vec![0u8; pages * PAGE_SIZE];
        fill(&mut bytes);
        bytes
    }

    fn tree<'a>(
        bytes: &'a [u8],
        root: u32,
        kind: BlobKind,
        length: u64,
    ) -> Result<BlobTree<SlicePageSource<'a>>, BlobReadError> {
        BlobTree::new(
            bytes,
            1,
            (bytes.len() / PAGE_SIZE) as u64,
            root,
            kind,
            length,
        )
    }

    #[test]
    fn owner_contract_rejects_zero_unaligned_unaddressable_and_impossible_lengths() {
        let bytes = image(3, |_| {});
        assert!(matches!(
            tree(&bytes, 2, BlobKind::MembershipBitmap, 0),
            Err(BlobReadError::OwnerLengthZero)
        ));
        assert!(matches!(
            tree(&bytes, 2, BlobKind::MembershipBitmap, 4),
            Err(BlobReadError::OwnerLengthAlignment {
                length: 4,
                alignment: 8,
            })
        ));
        assert!(matches!(
            tree(&bytes, 0, BlobKind::RetirementPageList, 4),
            Err(BlobReadError::PageOutOfBounds(0))
        ));
        assert!(matches!(
            BlobTree::new(&bytes, 1, 4, 2, BlobKind::RetirementPageList, 4,),
            Err(BlobReadError::PageOutOfBounds(2))
        ));
        assert!(matches!(
            tree(&bytes, 2, BlobKind::MembershipBitmap, 4056),
            Err(BlobReadError::OwnerLengthTooLarge)
        ));
    }

    #[test]
    fn single_leaf_stream_lookup_and_crc_modes_are_exact() {
        let mut bytes = image(3, |bytes| {
            put_leaf(page_mut(bytes, 2), BlobKind::MembershipBitmap, 0, 8, 0x5a);
        });
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8).unwrap();
        let mut reader = blob.stream(BlobPageCheck::Ordinary);
        let mut page = [0; PAGE_SIZE];
        assert_eq!(
            reader.next_chunk().unwrap(),
            Some(BlobChunk {
                logical_offset: 0,
                data: &[0x5a; 8],
            })
        );
        assert_eq!(reader.next_chunk().unwrap(), None);
        assert_eq!(
            blob.chunk_at(0, BlobPageCheck::Ordinary, &mut page)
                .unwrap()
                .data,
            &[0x5a; 8]
        );
        assert_eq!(
            blob.chunk_at(7, BlobPageCheck::Ordinary, &mut page)
                .unwrap()
                .data,
            &[0x5a; 8]
        );
        assert_eq!(
            blob.chunk_at(8, BlobPageCheck::Ordinary, &mut page),
            Err(BlobReadError::RequestOutsideLength(8))
        );

        bytes[2 * PAGE_SIZE + PAGE_CRC_OFFSET] ^= 1;
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8).unwrap();
        assert_eq!(
            blob.stream(BlobPageCheck::Ordinary)
                .next_chunk()
                .unwrap()
                .unwrap()
                .data,
            &[0x5a; 8]
        );
        assert_eq!(
            blob.stream(BlobPageCheck::VerifyCrc).next_chunk(),
            Err(BlobReadError::Page(BlobPageError::Checksum))
        );
    }

    #[test]
    fn ordinary_lookup_does_not_scan_unselected_entries_or_reserved_tails() {
        let mut bytes = image(6, |bytes| {
            put_branch(
                page_mut(bytes, 2),
                BlobKind::MembershipBitmap,
                1,
                &[(0, 3), (4048, 4), (8096, 5)],
            );
            put_leaf(
                page_mut(bytes, 3),
                BlobKind::MembershipBitmap,
                0,
                BLOB_LEAF_CAPACITY,
                1,
            );
            put_leaf(
                page_mut(bytes, 4),
                BlobKind::MembershipBitmap,
                4048,
                BLOB_LEAF_CAPACITY,
                2,
            );
            put_leaf(page_mut(bytes, 5), BlobKind::MembershipBitmap, 8096, 8, 3);
        });

        let branch = page_mut(&mut bytes, 2);
        branch[72..76].copy_from_slice(&6u32.to_le_bytes());
        write_crc32c(branch);
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8104).unwrap();
        let mut page = [0; PAGE_SIZE];
        assert_eq!(
            blob.chunk_at(0, BlobPageCheck::Ordinary, &mut page)
                .unwrap()
                .logical_offset,
            0
        );
        assert_eq!(
            blob.chunk_at(0, BlobPageCheck::VerifyCrc, &mut page),
            Err(BlobReadError::Page(BlobPageError::ChildOutOfBounds(6)))
        );

        let branch = page_mut(&mut bytes, 2);
        branch[72..76].copy_from_slice(&5u32.to_le_bytes());
        branch[80] = 1;
        write_crc32c(branch);
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8104).unwrap();
        assert!(blob.chunk_at(0, BlobPageCheck::Ordinary, &mut page).is_ok());
        assert_eq!(
            blob.chunk_at(0, BlobPageCheck::VerifyCrc, &mut page),
            Err(BlobReadError::Page(BlobPageError::ReservedNonzero))
        );

        let mut bytes = image(3, |bytes| {
            put_leaf(page_mut(bytes, 2), BlobKind::MembershipBitmap, 0, 8, 1);
        });
        let leaf = page_mut(&mut bytes, 2);
        leaf[56] = 1;
        write_crc32c(leaf);
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8).unwrap();
        let mut page = [0; PAGE_SIZE];
        assert!(blob.chunk_at(0, BlobPageCheck::Ordinary, &mut page).is_ok());
        assert_eq!(
            blob.chunk_at(0, BlobPageCheck::VerifyCrc, &mut page),
            Err(BlobReadError::Page(BlobPageError::ReservedNonzero))
        );
    }

    #[test]
    fn branch_streams_full_nonfinal_and_short_final_leaf_without_copying() {
        let bytes = image(5, |bytes| {
            put_branch(
                page_mut(bytes, 2),
                BlobKind::MembershipBitmap,
                1,
                &[(0, 3), (4048, 4)],
            );
            put_leaf(
                page_mut(bytes, 3),
                BlobKind::MembershipBitmap,
                0,
                BLOB_LEAF_CAPACITY,
                1,
            );
            put_leaf(page_mut(bytes, 4), BlobKind::MembershipBitmap, 4048, 8, 2);
        });
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 4056).unwrap();
        let mut reader = blob.stream(BlobPageCheck::Ordinary);
        let first = reader.next_chunk().unwrap().unwrap();
        assert_eq!(first.logical_offset, 0);
        assert_eq!(first.data.len(), 4048);
        assert!(first.data.iter().all(|&byte| byte == 1));
        let final_chunk = reader.next_chunk().unwrap().unwrap();
        assert_eq!(final_chunk.logical_offset, 4048);
        assert_eq!(final_chunk.data, &[2; 8]);
        assert_eq!(reader.next_chunk().unwrap(), None);

        assert_eq!(
            blob.chunk_at(4049, BlobPageCheck::Ordinary, &mut [0; PAGE_SIZE])
                .unwrap()
                .logical_offset,
            4048
        );
    }

    #[test]
    fn retirement_stream_enforces_strict_u32_order_across_leaf_boundaries() {
        let bytes = image(1016, |bytes| {
            put_branch(
                page_mut(bytes, 2),
                BlobKind::RetirementPageList,
                1,
                &[(0, 3), (4048, 4)],
            );
            put_leaf(
                page_mut(bytes, 3),
                BlobKind::RetirementPageList,
                0,
                BLOB_LEAF_CAPACITY,
                0,
            );
            put_leaf(page_mut(bytes, 4), BlobKind::RetirementPageList, 4048, 8, 0);
            let first = page_mut(bytes, 3);
            for index in 0..1012usize {
                let value = index as u32 + 2;
                let at = 48 + index * 4;
                first[at..at + 4].copy_from_slice(&value.to_le_bytes());
            }
            write_crc32c(first);
            let final_page = page_mut(bytes, 4);
            final_page[48..52].copy_from_slice(&1014u32.to_le_bytes());
            final_page[52..56].copy_from_slice(&1015u32.to_le_bytes());
            write_crc32c(final_page);
        });
        let blob = tree(&bytes, 2, BlobKind::RetirementPageList, 4056).unwrap();
        let mut reader = blob.stream(BlobPageCheck::VerifyCrc);
        assert_eq!(reader.next_chunk().unwrap().unwrap().data.len(), 4048);
        assert_eq!(reader.next_chunk().unwrap().unwrap().data.len(), 8);
        assert_eq!(reader.next_chunk().unwrap(), None);

        let mut duplicate = bytes;
        let final_page = page_mut(&mut duplicate, 4);
        final_page[48..52].copy_from_slice(&1013u32.to_le_bytes());
        write_crc32c(final_page);
        let blob = tree(&duplicate, 2, BlobKind::RetirementPageList, 4056).unwrap();
        let mut reader = blob.stream(BlobPageCheck::VerifyCrc);
        assert!(reader.next_chunk().unwrap().is_some());
        assert_eq!(
            reader.next_chunk(),
            Err(BlobReadError::RetirementPageOrder {
                previous: 1013,
                current: 1013,
            })
        );
        assert_eq!(reader.next_chunk(), Err(BlobReadError::CursorFailed));
    }

    #[test]
    fn retirement_stream_rejects_reserved_and_uncommitted_page_numbers() {
        for (value, expected) in [
            (0, BlobReadError::RetirementPageOutOfBounds(0)),
            (1, BlobReadError::RetirementPageOutOfBounds(1)),
            (5, BlobReadError::RetirementPageOutOfBounds(5)),
        ] {
            let bytes = image(5, |bytes| {
                put_leaf(page_mut(bytes, 2), BlobKind::RetirementPageList, 0, 4, 0);
                let leaf = page_mut(bytes, 2);
                leaf[48..52].copy_from_slice(&u32::to_le_bytes(value));
                write_crc32c(leaf);
            });
            let blob = tree(&bytes, 2, BlobKind::RetirementPageList, 4).unwrap();
            let mut reader = blob.stream(BlobPageCheck::Ordinary);
            assert_eq!(reader.next_chunk(), Err(expected));
            assert_eq!(reader.next_chunk(), Err(BlobReadError::CursorFailed));
        }
    }

    fn two_leaf_image(second_offset: u64, first_len: u16, second_len: u16) -> Vec<u8> {
        image(5, |bytes| {
            put_branch(
                page_mut(bytes, 2),
                BlobKind::MembershipBitmap,
                1,
                &[(0, 3), (second_offset, 4)],
            );
            put_leaf(
                page_mut(bytes, 3),
                BlobKind::MembershipBitmap,
                0,
                first_len,
                1,
            );
            put_leaf(
                page_mut(bytes, 4),
                BlobKind::MembershipBitmap,
                second_offset,
                second_len,
                2,
            );
        })
    }

    #[test]
    fn stream_rejects_gap_overlap_short_nonfinal_missing_and_trailing_data() {
        for (name, second_offset) in [("gap", 4056), ("overlap", 4040)] {
            let bytes = two_leaf_image(second_offset, BLOB_LEAF_CAPACITY, 8);
            let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 4064).unwrap();
            let mut reader = blob.stream(BlobPageCheck::Ordinary);
            assert!(reader.next_chunk().unwrap().is_some(), "{name}");
            assert_eq!(
                reader.next_chunk(),
                Err(BlobReadError::OffsetMismatch {
                    expected: 4048,
                    actual: second_offset,
                }),
                "{name}"
            );
            assert_eq!(reader.next_chunk(), Err(BlobReadError::CursorFailed));
        }

        let bytes = two_leaf_image(4048, BLOB_LEAF_CAPACITY, 8);
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 4048).unwrap();
        assert_eq!(
            blob.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::TrailingData)
        );

        let bytes = image(4, |bytes| {
            put_leaf(
                page_mut(bytes, 2),
                BlobKind::MembershipBitmap,
                0,
                BLOB_LEAF_CAPACITY,
                1,
            );
        });
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 4056).unwrap();
        assert_eq!(
            blob.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::LengthShort {
                declared: 4056,
                actual: 4048,
            })
        );

        let bytes = two_leaf_image(8, 8, 8);
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 16).unwrap();
        assert_eq!(
            blob.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::NonfinalLeafLength(8))
        );

        let bytes = image(3, |bytes| {
            put_leaf(page_mut(bytes, 2), BlobKind::MembershipBitmap, 0, 16, 1);
        });
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8).unwrap();
        assert_eq!(
            blob.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::LengthExceeded {
                declared: 8,
                actual: 16,
            })
        );
    }

    #[test]
    fn root_child_offsets_types_and_levels_are_checked_before_descent() {
        let bytes = image(3, |bytes| {
            let page = page_mut(bytes, 2);
            PageHeader {
                page_type: PageType::MetadataChunk,
                born_txn: 1,
                item_count: 0,
                level: 0,
                lower: PAGE_HEADER_SIZE,
                upper: PAGE_SIZE as u16,
                aux: 0,
                page_crc32c: 0,
            }
            .encode_into(page);
            write_crc32c(page);
        });
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8).unwrap();
        assert_eq!(
            blob.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::RootType(PageType::MetadataChunk))
        );

        let bytes = image(4, |bytes| {
            put_branch(page_mut(bytes, 2), BlobKind::MembershipBitmap, 1, &[(8, 3)]);
            put_leaf(page_mut(bytes, 3), BlobKind::MembershipBitmap, 8, 8, 1);
        });
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8).unwrap();
        assert_eq!(
            blob.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::OffsetMismatch {
                expected: 0,
                actual: 8,
            })
        );

        let bytes = image(4, |bytes| {
            put_branch(page_mut(bytes, 2), BlobKind::MembershipBitmap, 1, &[(0, 3)]);
            put_leaf(page_mut(bytes, 3), BlobKind::MembershipBitmap, 8, 8, 1);
        });
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8).unwrap();
        assert_eq!(
            blob.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::OffsetMismatch {
                expected: 0,
                actual: 8,
            })
        );

        let bytes = image(5, |bytes| {
            put_branch(page_mut(bytes, 2), BlobKind::MembershipBitmap, 2, &[(0, 3)]);
            put_branch(page_mut(bytes, 3), BlobKind::MembershipBitmap, 2, &[(0, 4)]);
            put_leaf(page_mut(bytes, 4), BlobKind::MembershipBitmap, 0, 8, 1);
        });
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8).unwrap();
        assert_eq!(
            blob.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::ChildLevel {
                expected: 1,
                actual: 2,
            })
        );

        let bytes = image(4, |bytes| {
            put_branch(page_mut(bytes, 2), BlobKind::MembershipBitmap, 1, &[(0, 2)]);
        });
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8).unwrap();
        assert_eq!(
            blob.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::ChildType(PageType::BlobBranch))
        );
    }

    #[test]
    fn verified_mode_checks_branch_and_leaf_pages_but_ordinary_mode_does_not() {
        let mut bytes = two_leaf_image(4048, BLOB_LEAF_CAPACITY, 8);
        bytes[2 * PAGE_SIZE + PAGE_CRC_OFFSET] ^= 1;
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 4056).unwrap();
        assert!(blob
            .stream(BlobPageCheck::Ordinary)
            .next_chunk()
            .unwrap()
            .is_some());
        assert_eq!(
            blob.stream(BlobPageCheck::VerifyCrc).next_chunk(),
            Err(BlobReadError::Page(BlobPageError::Checksum))
        );

        let mut bytes = two_leaf_image(4048, BLOB_LEAF_CAPACITY, 8);
        bytes[3 * PAGE_SIZE + PAGE_CRC_OFFSET] ^= 1;
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 4056).unwrap();
        assert_eq!(
            blob.stream(BlobPageCheck::VerifyCrc).next_chunk(),
            Err(BlobReadError::Page(BlobPageError::Checksum))
        );
    }

    #[test]
    fn maximum_legal_depth_uses_exactly_the_fixed_stack_capacity() {
        let pages = usize::from(MAX_TREE_LEVEL) + 3;
        let bytes = image(pages, |bytes| {
            for level in (1..=MAX_TREE_LEVEL).rev() {
                let pgno = usize::from(MAX_TREE_LEVEL - level) + 2;
                put_branch(
                    page_mut(bytes, pgno),
                    BlobKind::MembershipBitmap,
                    level,
                    &[(0, pgno as u32 + 1)],
                );
            }
            put_leaf(
                page_mut(bytes, usize::from(MAX_TREE_LEVEL) + 2),
                BlobKind::MembershipBitmap,
                0,
                8,
                1,
            );
        });
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 8).unwrap();
        let mut reader = blob.stream(BlobPageCheck::VerifyCrc);
        assert_eq!(reader.next_chunk().unwrap().unwrap().data, &[1; 8]);
        assert_eq!(reader.next_chunk().unwrap(), None);
    }

    #[test]
    fn positional_source_preserves_fork_io_short_read_and_torn_page_evidence() {
        let bytes = image(4, |bytes| {
            put_branch(page_mut(bytes, 2), BlobKind::MembershipBitmap, 1, &[(0, 3)]);
            put_leaf(page_mut(bytes, 3), BlobKind::MembershipBitmap, 0, 8, 1);
        });

        let forked = BlobTree::from_source(
            FailingSource {
                access: Some(PageSourceError::ForkedHandle),
                read: None,
            },
            1,
            4,
            2,
            BlobKind::MembershipBitmap,
            8,
        )
        .unwrap();
        assert_eq!(
            forked.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::Source(PageSourceError::ForkedHandle))
        );

        let io = PageSourceError::Io(crate::page_source::PageIoEvidence {
            kind: crate::page_source::PageIoKind::PermissionDenied,
            raw_os_error: Some(13),
        });
        let failing = BlobTree::from_source(
            FailingSource {
                access: None,
                read: Some(io),
            },
            1,
            4,
            2,
            BlobKind::MembershipBitmap,
            8,
        )
        .unwrap();
        assert_eq!(
            failing.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::Source(io))
        );

        let short = BlobTree::from_source(
            SlicePageSource::new(&bytes[..2 * PAGE_SIZE + 17], 4),
            1,
            4,
            2,
            BlobKind::MembershipBitmap,
            8,
        )
        .unwrap();
        assert_eq!(
            short.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::Source(PageSourceError::ShortRead {
                offset: (2 * PAGE_SIZE) as u64,
                expected: PAGE_SIZE,
                actual: 17,
            }))
        );

        let torn = BlobTree::from_source(
            TornSource {
                bytes: &bytes,
                torn: Cell::new(false),
                page_count: 4,
            },
            1,
            4,
            2,
            BlobKind::MembershipBitmap,
            8,
        )
        .unwrap();
        assert_eq!(
            torn.stream(BlobPageCheck::Ordinary).next_chunk(),
            Err(BlobReadError::Page(BlobPageError::ChildOutOfBounds(
                u32::MAX
            )))
        );
    }

    #[test]
    fn cursor_reuses_one_page_buffer_and_warmed_streaming_allocates_nothing() {
        let bytes = two_leaf_image(4048, BLOB_LEAF_CAPACITY, 8);
        let blob = tree(&bytes, 2, BlobKind::MembershipBitmap, 4056).unwrap();
        let mut reader = blob.stream(BlobPageCheck::Ordinary);
        let first_buffer = reader.next_chunk().unwrap().unwrap().data.as_ptr();
        let second_buffer = reader.next_chunk().unwrap().unwrap().data.as_ptr();
        assert_eq!(first_buffer, second_buffer);
        assert_eq!(reader.next_chunk().unwrap(), None);

        let ((), allocations) = count_thread_allocations(|| {
            for _ in 0..128 {
                let mut reader = blob.stream(BlobPageCheck::Ordinary);
                while reader.next_chunk().unwrap().is_some() {}
            }
        });
        assert_eq!(allocations, 0);
    }

    #[test]
    fn retirement_page_stream_yields_copied_u32_values() {
        let mut bytes = image(8, |bytes| {
            put_leaf(page_mut(bytes, 2), BlobKind::RetirementPageList, 0, 12, 0);
        });
        page_mut(&mut bytes, 2)[48..60].copy_from_slice(&[5, 0, 0, 0, 6, 0, 0, 0, 7, 0, 0, 0]);
        write_crc32c(page_mut(&mut bytes, 2));
        let blob = tree(&bytes, 2, BlobKind::RetirementPageList, 12).unwrap();
        let mut pages = blob.retirement_pages(BlobPageCheck::VerifyCrc).unwrap();
        let first = pages.next_page().unwrap().unwrap();
        assert_eq!(first, 5);
        assert_eq!(pages.next_page().unwrap(), Some(6));
        assert_eq!(pages.next_page().unwrap(), Some(7));
        assert_eq!(pages.next_page().unwrap(), None);
        assert_eq!(first, 5);
    }
}
