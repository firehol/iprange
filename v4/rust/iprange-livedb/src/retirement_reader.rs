//! Bounded selection and verified two-pass reading of exact v4 retirement batches.

use crate::blob_page::BlobKind;
use crate::blob_reader::{BlobPageCheck, BlobReadError, BlobTree};
use crate::contract::{MAX_PAGE_COUNT, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::page::{PageHeader, PageType};
use crate::page_source::{CommittedPageSource, PageSourceError, SlicePageSource};
use crate::retirement_page::{
    RetirementBatch, RetirementBranch, RetirementLeaf, RetirementPageError,
};

const PATH_CAPACITY: usize = MAX_TREE_LEVEL as usize + 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RetirementIdentity {
    pub(crate) database_id: [u8; 16],
    pub(crate) txn_id: u64,
    pub(crate) commit_nonce: [u8; 16],
    pub(crate) page_count: u64,
    pub(crate) root: u32,
    pub(crate) batch_count: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RetirementReadError {
    Source(PageSourceError),
    Page(RetirementPageError),
    Blob(BlobReadError),
    PageOutOfBounds(u32),
    PageOffsetOverflow,
    IdentityInvalid,
    CommittedPageCountOutOfRange(u64),
    RootCountMismatch,
    BatchCountOutOfRange,
    RootType(PageType),
    ChildType(PageType),
    ChildLevel { expected: u16, actual: u16 },
    ChildMaximumMismatch { expected: u64, actual: u64 },
    KeysNotStrict,
    BatchCountMismatch { declared: u64, actual: u64 },
    WorkLimitZero,
    WorkLimitTooSmall { required_pages: u64 },
    ArithmeticOverflow,
    VerificationBufferTooSmall { required_batches: u64 },
    SelectionChanged,
    ListedPageOutOfBounds(u32),
    BlobPageCountMismatch { declared: u64, actual: u64 },
    PathChanged(u32),
    CursorFailed,
}

impl From<RetirementPageError> for RetirementReadError {
    fn from(value: RetirementPageError) -> Self {
        Self::Page(value)
    }
}

impl From<PageSourceError> for RetirementReadError {
    fn from(value: PageSourceError) -> Self {
        Self::Source(value)
    }
}

impl From<BlobReadError> for RetirementReadError {
    fn from(value: BlobReadError) -> Self {
        Self::Blob(value)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum RetirementPageCheck {
    Ordinary,
    VerifyCrc,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RetirementSelection {
    identity: RetirementIdentity,
    pub(crate) batch_count: u64,
    pub(crate) page_count: u64,
    pub(crate) last_retired_by_txn: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RetirementSelectionResult {
    NoChange,
    Selected(RetirementSelection),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RetirementPassResult {
    pub(crate) batch_count: u64,
    pub(crate) page_count: u64,
}

#[derive(Debug)]
pub(crate) struct RetirementTree<S: CommittedPageSource> {
    pages: S,
    identity: RetirementIdentity,
}

impl<'a> RetirementTree<SlicePageSource<'a>> {
    pub(crate) fn new(
        bytes: &'a [u8],
        identity: RetirementIdentity,
    ) -> Result<Self, RetirementReadError> {
        if !(2..=MAX_PAGE_COUNT).contains(&identity.page_count) {
            return Err(RetirementReadError::CommittedPageCountOutOfRange(
                identity.page_count,
            ));
        }
        let committed_u64 = identity
            .page_count
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(RetirementReadError::PageOffsetOverflow)?;
        let committed =
            usize::try_from(committed_u64).map_err(|_| RetirementReadError::PageOffsetOverflow)?;
        if committed > bytes.len() {
            return Err(RetirementReadError::PageOutOfBounds(identity.root));
        }
        Self::from_source(
            SlicePageSource::new(&bytes[..committed], identity.page_count),
            identity,
        )
    }
}

impl<S: CommittedPageSource> RetirementTree<S> {
    pub(crate) fn from_source(
        pages: S,
        identity: RetirementIdentity,
    ) -> Result<Self, RetirementReadError> {
        if identity.database_id == [0; 16]
            || identity.commit_nonce == [0; 16]
            || identity.txn_id == 0
        {
            return Err(RetirementReadError::IdentityInvalid);
        }
        if !(2..=MAX_PAGE_COUNT).contains(&identity.page_count) {
            return Err(RetirementReadError::CommittedPageCountOutOfRange(
                identity.page_count,
            ));
        }
        if (identity.root == 0) != (identity.batch_count == 0) {
            return Err(RetirementReadError::RootCountMismatch);
        }
        if identity.batch_count > identity.txn_id - 1 {
            return Err(RetirementReadError::BatchCountOutOfRange);
        }
        if identity.root != 0
            && (identity.root < 2 || u64::from(identity.root) >= identity.page_count)
        {
            return Err(RetirementReadError::PageOutOfBounds(identity.root));
        }
        Ok(Self { pages, identity })
    }

    pub(crate) const fn identity(&self) -> RetirementIdentity {
        self.identity
    }

    pub(crate) fn select_oldest_eligible(
        &self,
        reader_threshold: u64,
        max_batches: u64,
        max_pages: u64,
    ) -> Result<RetirementSelectionResult, RetirementReadError> {
        if max_batches == 0 || max_pages == 0 {
            return Err(RetirementReadError::WorkLimitZero);
        }
        if self.identity.root == 0 {
            return Ok(RetirementSelectionResult::NoChange);
        }

        let mut cursor = self.cursor(RetirementPageCheck::Ordinary);
        let mut selected_batches = 0u64;
        let mut selected_pages = 0u64;
        let mut last_retired_by_txn = 0u64;
        loop {
            if selected_batches == max_batches {
                break;
            }
            let Some(batch) = cursor.next_batch()? else {
                break;
            };
            if batch.retired_by_txn > reader_threshold {
                break;
            }
            let remaining = max_pages - selected_pages;
            if batch.page_count > remaining {
                if selected_batches == 0 {
                    return Err(RetirementReadError::WorkLimitTooSmall {
                        required_pages: batch.page_count,
                    });
                }
                break;
            }
            selected_batches = selected_batches
                .checked_add(1)
                .ok_or(RetirementReadError::ArithmeticOverflow)?;
            selected_pages = selected_pages
                .checked_add(batch.page_count)
                .ok_or(RetirementReadError::ArithmeticOverflow)?;
            last_retired_by_txn = batch.retired_by_txn;
        }
        if selected_batches == 0 {
            return Ok(RetirementSelectionResult::NoChange);
        }
        Ok(RetirementSelectionResult::Selected(RetirementSelection {
            identity: self.identity,
            batch_count: selected_batches,
            page_count: selected_pages,
            last_retired_by_txn,
        }))
    }

    pub(crate) fn verify_selection<'scratch>(
        &self,
        selection: RetirementSelection,
        scratch: &'scratch mut [RetirementBatch],
    ) -> Result<VerifiedRetirementSelection<'scratch>, RetirementReadError> {
        if selection.identity != self.identity {
            return Err(RetirementReadError::SelectionChanged);
        }
        let required = usize::try_from(selection.batch_count).map_err(|_| {
            RetirementReadError::VerificationBufferTooSmall {
                required_batches: selection.batch_count,
            }
        })?;
        if scratch.len() < required {
            return Err(RetirementReadError::VerificationBufferTooSmall {
                required_batches: selection.batch_count,
            });
        }

        let mut cursor = self.cursor(RetirementPageCheck::VerifyCrc);
        let mut actual_pages = 0u64;
        let mut actual_last = 0u64;
        for slot in &mut scratch[..required] {
            let batch = cursor
                .next_batch()?
                .ok_or(RetirementReadError::SelectionChanged)?;
            self.verify_batch_blob(batch)?;
            *slot = batch;
            actual_pages = actual_pages
                .checked_add(batch.page_count)
                .ok_or(RetirementReadError::ArithmeticOverflow)?;
            actual_last = batch.retired_by_txn;
        }
        if actual_pages != selection.page_count || actual_last != selection.last_retired_by_txn {
            return Err(RetirementReadError::SelectionChanged);
        }
        Ok(VerifiedRetirementSelection {
            identity: self.identity,
            selection,
            batches: &scratch[..required],
        })
    }

    fn verify_batch_blob(&self, batch: RetirementBatch) -> Result<(), RetirementReadError> {
        let length = batch.blob_length()?;
        let blob = BlobTree::from_source(
            &self.pages,
            self.identity.txn_id,
            self.identity.page_count,
            batch.page_list_blob_root,
            BlobKind::RetirementPageList,
            length,
        )?;
        let mut reader = blob.retirement_pages(BlobPageCheck::VerifyCrc)?;
        let mut count = 0u64;
        while let Some(page) = reader.next_page()? {
            self.require_listed_page(page)?;
            count = count
                .checked_add(1)
                .ok_or(RetirementReadError::ArithmeticOverflow)?;
        }
        if count != batch.page_count {
            return Err(RetirementReadError::BlobPageCountMismatch {
                declared: batch.page_count,
                actual: count,
            });
        }
        Ok(())
    }

    fn require_listed_page(&self, page: u32) -> Result<(), RetirementReadError> {
        if page < 2 || u64::from(page) >= self.identity.page_count {
            Err(RetirementReadError::ListedPageOutOfBounds(page))
        } else {
            Ok(())
        }
    }

    fn cursor(&self, check: RetirementPageCheck) -> RetirementCursor<'_, S> {
        RetirementCursor {
            tree: self,
            check,
            path: [RetirementFrame {
                pgno: 0,
                index: 0,
                len: 0,
            }; PATH_CAPACITY],
            root_level: 0,
            leaf_index: 0,
            previous_key: None,
            yielded: 0,
            scratch: [0; PAGE_SIZE],
            state: RetirementCursorState::Unpositioned,
        }
    }

    fn root_level(&self) -> Result<Option<u16>, RetirementReadError> {
        if self.identity.root == 0 {
            return Ok(None);
        }
        let mut page = [0; PAGE_SIZE];
        self.pages.read_page(self.identity.root, &mut page)?;
        let header =
            PageHeader::decode(&page, self.identity.txn_id).map_err(RetirementPageError::from)?;
        match header.page_type {
            PageType::RetirementLeaf => Ok(Some(0)),
            PageType::RetirementBranch => Ok(Some(header.level)),
            other => Err(RetirementReadError::RootType(other)),
        }
    }

    fn branch<'page>(
        &self,
        page: &'page [u8; PAGE_SIZE],
        expected_level: u16,
        check: RetirementPageCheck,
    ) -> Result<RetirementBranch<'page>, RetirementReadError> {
        let branch = RetirementBranch::open(page, self.identity.txn_id, self.identity.page_count)?;
        if branch.level() != expected_level {
            return Err(RetirementReadError::ChildLevel {
                expected: expected_level,
                actual: branch.level(),
            });
        }
        if check == RetirementPageCheck::VerifyCrc {
            branch.verify_crc()?;
        }
        Ok(branch)
    }

    fn leaf<'page>(
        &self,
        page: &'page [u8; PAGE_SIZE],
        check: RetirementPageCheck,
    ) -> Result<RetirementLeaf<'page>, RetirementReadError> {
        let leaf = RetirementLeaf::open(page, self.identity.txn_id, self.identity.page_count)?;
        if check == RetirementPageCheck::VerifyCrc {
            leaf.verify_crc()?;
        }
        Ok(leaf)
    }
}

#[derive(Clone, Copy, Debug)]
struct RetirementFrame {
    pgno: u32,
    index: u16,
    len: u16,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum RetirementCursorState {
    Unpositioned,
    At,
    Done,
    Failed,
}

#[derive(Debug)]
struct RetirementCursor<'tree, S: CommittedPageSource> {
    tree: &'tree RetirementTree<S>,
    check: RetirementPageCheck,
    path: [RetirementFrame; PATH_CAPACITY],
    root_level: u16,
    leaf_index: u16,
    previous_key: Option<u64>,
    yielded: u64,
    scratch: [u8; PAGE_SIZE],
    state: RetirementCursorState,
}

impl<S: CommittedPageSource> RetirementCursor<'_, S> {
    fn next_batch(&mut self) -> Result<Option<RetirementBatch>, RetirementReadError> {
        if self.state == RetirementCursorState::Failed {
            return Err(RetirementReadError::CursorFailed);
        }
        if let Err(error) = self.tree.pages.check_access() {
            self.state = RetirementCursorState::Failed;
            return Err(error.into());
        }
        match self.next_batch_inner() {
            Ok(batch) => Ok(batch),
            Err(error) => {
                self.state = RetirementCursorState::Failed;
                Err(error)
            }
        }
    }

    fn next_batch_inner(&mut self) -> Result<Option<RetirementBatch>, RetirementReadError> {
        match self.state {
            RetirementCursorState::Unpositioned => {
                let Some(level) = self.tree.root_level()? else {
                    self.state = RetirementCursorState::Done;
                    return Ok(None);
                };
                self.root_level = level;
                self.descend_first(0, self.tree.identity.root, level, None)?;
            }
            RetirementCursorState::Done => return Ok(None),
            RetirementCursorState::Failed => return Err(RetirementReadError::CursorFailed),
            RetirementCursorState::At => {}
        }

        let leaf = self.tree.leaf(&self.scratch, self.check)?;
        let batch = leaf.batch(usize::from(self.leaf_index))?;
        if self
            .previous_key
            .map(|previous| batch.retired_by_txn <= previous)
            .unwrap_or(false)
        {
            return Err(RetirementReadError::KeysNotStrict);
        }
        self.previous_key = Some(batch.retired_by_txn);
        self.yielded = self
            .yielded
            .checked_add(1)
            .ok_or(RetirementReadError::ArithmeticOverflow)?;
        self.advance()?;
        if self.state == RetirementCursorState::Done
            && self.yielded != self.tree.identity.batch_count
        {
            return Err(RetirementReadError::BatchCountMismatch {
                declared: self.tree.identity.batch_count,
                actual: self.yielded,
            });
        }
        Ok(Some(batch))
    }

    fn descend_first(
        &mut self,
        mut depth: usize,
        mut pgno: u32,
        mut level: u16,
        mut expected_maximum: Option<u64>,
    ) -> Result<(), RetirementReadError> {
        loop {
            if depth >= PATH_CAPACITY {
                return Err(RetirementReadError::ChildLevel {
                    expected: 0,
                    actual: level,
                });
            }
            self.load_page(pgno)?;
            let header = PageHeader::decode(&self.scratch, self.tree.identity.txn_id)
                .map_err(RetirementPageError::from)?;
            if level == 0 {
                if header.page_type != PageType::RetirementLeaf {
                    return Err(RetirementReadError::ChildType(header.page_type));
                }
                let leaf = self.tree.leaf(&self.scratch, self.check)?;
                let actual = leaf.maximum_key()?;
                if let Some(expected) = expected_maximum {
                    if actual != expected {
                        return Err(RetirementReadError::ChildMaximumMismatch { expected, actual });
                    }
                }
                self.leaf_index = 0;
                self.state = RetirementCursorState::At;
                return Ok(());
            }

            if header.page_type != PageType::RetirementBranch {
                return Err(RetirementReadError::ChildType(header.page_type));
            }
            let branch = self.tree.branch(&self.scratch, level, self.check)?;
            let actual = branch.maximum_key()?;
            if let Some(expected) = expected_maximum {
                if actual != expected {
                    return Err(RetirementReadError::ChildMaximumMismatch { expected, actual });
                }
            }
            let entry = branch.entry(0)?;
            self.path[depth] = RetirementFrame {
                pgno,
                index: 0,
                len: u16::try_from(branch.len())
                    .map_err(|_| RetirementReadError::ArithmeticOverflow)?,
            };
            pgno = entry.child_pgno;
            expected_maximum = Some(entry.max_retired_by_txn);
            level -= 1;
            depth += 1;
        }
    }

    fn advance(&mut self) -> Result<(), RetirementReadError> {
        let leaf = self.tree.leaf(&self.scratch, self.check)?;
        if usize::from(self.leaf_index) + 1 < leaf.len() {
            self.leaf_index += 1;
            return Ok(());
        }

        let leaf_depth = usize::from(self.root_level);
        for depth in (0..leaf_depth).rev() {
            let frame = self.path[depth];
            let level = self.root_level - depth as u16;
            self.load_page(frame.pgno)?;
            let branch = self.tree.branch(&self.scratch, level, self.check)?;
            if branch.len() != usize::from(frame.len) {
                return Err(RetirementReadError::PathChanged(frame.pgno));
            }
            let next_index = usize::from(frame.index) + 1;
            if next_index == branch.len() {
                continue;
            }
            self.path[depth].index = next_index as u16;
            let entry = branch.entry(next_index)?;
            self.descend_first(
                depth + 1,
                entry.child_pgno,
                level - 1,
                Some(entry.max_retired_by_txn),
            )?;
            return Ok(());
        }
        self.state = RetirementCursorState::Done;
        Ok(())
    }

    fn load_page(&mut self, pgno: u32) -> Result<(), RetirementReadError> {
        self.tree.pages.read_page(pgno, &mut self.scratch)?;
        Ok(())
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct VerifiedRetirementSelection<'scratch> {
    identity: RetirementIdentity,
    selection: RetirementSelection,
    batches: &'scratch [RetirementBatch],
}

#[derive(Debug)]
pub(crate) enum RetirementSecondPassError<E> {
    Read(RetirementReadError),
    Sink(E),
}

impl<'scratch> VerifiedRetirementSelection<'scratch> {
    pub(crate) fn second_pass<E, F, S>(
        &self,
        tree: &RetirementTree<S>,
        mut sink: F,
    ) -> Result<RetirementPassResult, RetirementSecondPassError<E>>
    where
        F: FnMut(RetirementBatch, u32) -> Result<(), E>,
        S: CommittedPageSource,
    {
        if tree.identity != self.identity || self.selection.identity != self.identity {
            return Err(RetirementSecondPassError::Read(
                RetirementReadError::SelectionChanged,
            ));
        }

        let mut preflight = tree.cursor(RetirementPageCheck::Ordinary);
        for expected in self.batches {
            let actual = preflight
                .next_batch()
                .map_err(RetirementSecondPassError::Read)?
                .ok_or(RetirementSecondPassError::Read(
                    RetirementReadError::SelectionChanged,
                ))?;
            if actual != *expected {
                return Err(RetirementSecondPassError::Read(
                    RetirementReadError::SelectionChanged,
                ));
            }
        }

        let mut cursor = tree.cursor(RetirementPageCheck::Ordinary);
        let mut pages = 0u64;
        for expected in self.batches {
            let batch = cursor
                .next_batch()
                .map_err(RetirementSecondPassError::Read)?
                .ok_or(RetirementSecondPassError::Read(
                    RetirementReadError::SelectionChanged,
                ))?;
            if batch != *expected {
                return Err(RetirementSecondPassError::Read(
                    RetirementReadError::SelectionChanged,
                ));
            }
            let length = batch
                .blob_length()
                .map_err(RetirementReadError::from)
                .map_err(RetirementSecondPassError::Read)?;
            let blob = BlobTree::from_source(
                &tree.pages,
                tree.identity.txn_id,
                tree.identity.page_count,
                batch.page_list_blob_root,
                BlobKind::RetirementPageList,
                length,
            )
            .map_err(RetirementReadError::from)
            .map_err(RetirementSecondPassError::Read)?;
            let mut reader = blob
                .retirement_pages(BlobPageCheck::Ordinary)
                .map_err(RetirementReadError::from)
                .map_err(RetirementSecondPassError::Read)?;
            while let Some(page) = reader
                .next_page()
                .map_err(RetirementReadError::from)
                .map_err(RetirementSecondPassError::Read)?
            {
                tree.require_listed_page(page)
                    .map_err(RetirementSecondPassError::Read)?;
                sink(batch, page).map_err(RetirementSecondPassError::Sink)?;
                pages = pages.checked_add(1).ok_or(RetirementSecondPassError::Read(
                    RetirementReadError::ArithmeticOverflow,
                ))?;
            }
        }
        if pages != self.selection.page_count {
            return Err(RetirementSecondPassError::Read(
                RetirementReadError::SelectionChanged,
            ));
        }
        Ok(RetirementPassResult {
            batch_count: self.selection.batch_count,
            page_count: pages,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::blob_page::BlobPageError;
    use crate::page::{
        write_crc32c, PageHeader, PageHeaderError, PAGE_CRC_OFFSET, PAGE_HEADER_SIZE,
    };
    use crate::test_alloc::count_thread_allocations;
    use core::cell::RefCell;
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
        page_count: u64,
    }

    impl CommittedPageSource for TornSource<'_> {
        fn read_page(
            &self,
            pgno: u32,
            destination: &mut [u8; PAGE_SIZE],
        ) -> Result<(), PageSourceError> {
            SlicePageSource::new(self.bytes, self.page_count).read_page(pgno, destination)?;
            if pgno == 2 {
                destination[56..60].copy_from_slice(&u32::MAX.to_le_bytes());
            }
            Ok(())
        }
    }

    struct MutableSource {
        bytes: RefCell<Vec<u8>>,
        page_count: u64,
    }

    impl CommittedPageSource for MutableSource {
        fn read_page(
            &self,
            pgno: u32,
            destination: &mut [u8; PAGE_SIZE],
        ) -> Result<(), PageSourceError> {
            let bytes = self.bytes.borrow();
            SlicePageSource::new(&bytes, self.page_count).read_page(pgno, destination)
        }
    }

    const EMPTY_BATCH: RetirementBatch = RetirementBatch {
        retired_by_txn: 0,
        page_count: 0,
        page_list_blob_root: 0,
    };

    fn identity(page_count: u64, root: u32, batch_count: u64) -> RetirementIdentity {
        RetirementIdentity {
            database_id: [1; 16],
            txn_id: 8,
            commit_nonce: [2; 16],
            page_count,
            root,
            batch_count,
        }
    }

    fn image(page_count: usize) -> Vec<u8> {
        vec![0; page_count * PAGE_SIZE]
    }

    fn page_mut(bytes: &mut [u8], pgno: u32) -> &mut [u8; PAGE_SIZE] {
        let start = pgno as usize * PAGE_SIZE;
        (&mut bytes[start..start + PAGE_SIZE]).try_into().unwrap()
    }

    fn retirement_leaf(page: &mut [u8; PAGE_SIZE], batches: &[RetirementBatch]) {
        *page = [0; PAGE_SIZE];
        PageHeader {
            page_type: PageType::RetirementLeaf,
            born_txn: 1,
            item_count: batches.len() as u16,
            level: 0,
            lower: (usize::from(PAGE_HEADER_SIZE) + batches.len() * 32) as u16,
            upper: PAGE_SIZE as u16,
            aux: 0,
            page_crc32c: 0,
        }
        .encode_into(page);
        for (index, batch) in batches.iter().enumerate() {
            let at = usize::from(PAGE_HEADER_SIZE) + index * 32;
            page[at + 8..at + 16].copy_from_slice(&batch.retired_by_txn.to_le_bytes());
            page[at + 16..at + 24].copy_from_slice(&batch.page_count.to_le_bytes());
            page[at + 24..at + 28].copy_from_slice(&batch.page_list_blob_root.to_le_bytes());
        }
        write_crc32c(page);
    }

    fn retirement_branch(page: &mut [u8; PAGE_SIZE], level: u16, entries: &[(u64, u32)]) {
        *page = [0; PAGE_SIZE];
        PageHeader {
            page_type: PageType::RetirementBranch,
            born_txn: 1,
            item_count: entries.len() as u16,
            level,
            lower: (usize::from(PAGE_HEADER_SIZE) + entries.len() * 16) as u16,
            upper: PAGE_SIZE as u16,
            aux: 0,
            page_crc32c: 0,
        }
        .encode_into(page);
        for (index, &(maximum, child)) in entries.iter().enumerate() {
            let at = usize::from(PAGE_HEADER_SIZE) + index * 16;
            page[at..at + 8].copy_from_slice(&maximum.to_le_bytes());
            page[at + 8..at + 12].copy_from_slice(&child.to_le_bytes());
        }
        write_crc32c(page);
    }

    fn retirement_blob(page: &mut [u8; PAGE_SIZE], pages: &[u32]) {
        *page = [0; PAGE_SIZE];
        let length = pages.len() * 4;
        PageHeader {
            page_type: PageType::BlobLeaf,
            born_txn: 1,
            item_count: 1,
            level: 0,
            lower: (48 + length) as u16,
            upper: PAGE_SIZE as u16,
            aux: BlobKind::RetirementPageList as u32,
            page_crc32c: 0,
        }
        .encode_into(page);
        page[40..42].copy_from_slice(&(length as u16).to_le_bytes());
        for (index, value) in pages.iter().enumerate() {
            let at = 48 + index * 4;
            page[at..at + 4].copy_from_slice(&value.to_le_bytes());
        }
        write_crc32c(page);
    }

    fn sample_image() -> Vec<u8> {
        let mut bytes = image(20);
        retirement_leaf(
            page_mut(&mut bytes, 2),
            &[
                RetirementBatch {
                    retired_by_txn: 2,
                    page_count: 2,
                    page_list_blob_root: 3,
                },
                RetirementBatch {
                    retired_by_txn: 4,
                    page_count: 1,
                    page_list_blob_root: 4,
                },
                RetirementBatch {
                    retired_by_txn: 6,
                    page_count: 3,
                    page_list_blob_root: 5,
                },
            ],
        );
        retirement_blob(page_mut(&mut bytes, 3), &[10, 11]);
        retirement_blob(page_mut(&mut bytes, 4), &[12]);
        retirement_blob(page_mut(&mut bytes, 5), &[13, 14, 15]);
        bytes
    }

    fn selected(result: RetirementSelectionResult) -> RetirementSelection {
        match result {
            RetirementSelectionResult::Selected(selection) => selection,
            RetirementSelectionResult::NoChange => panic!("expected a selection"),
        }
    }

    #[test]
    fn constructor_checks_identity_root_count_and_committed_bounds() {
        let bytes = image(3);
        let mut invalid = identity(3, 0, 0);
        invalid.database_id = [0; 16];
        assert_eq!(
            RetirementTree::new(&bytes, invalid).unwrap_err(),
            RetirementReadError::IdentityInvalid
        );
        assert_eq!(
            RetirementTree::new(&bytes, identity(3, 2, 0)).unwrap_err(),
            RetirementReadError::RootCountMismatch
        );
        assert_eq!(
            RetirementTree::new(&[], identity(0, 0, 0)).unwrap_err(),
            RetirementReadError::CommittedPageCountOutOfRange(0)
        );
        assert_eq!(
            RetirementTree::new(&[], identity(MAX_PAGE_COUNT + 1, 0, 0)).unwrap_err(),
            RetirementReadError::CommittedPageCountOutOfRange(MAX_PAGE_COUNT + 1)
        );
        assert_eq!(
            RetirementTree::new(&bytes, identity(3, 0, 1)).unwrap_err(),
            RetirementReadError::RootCountMismatch
        );
        assert!(matches!(
            RetirementTree::new(&bytes, identity(4, 2, 1)),
            Err(RetirementReadError::PageOutOfBounds(2))
        ));

        let empty = RetirementTree::new(&bytes[..2 * PAGE_SIZE], identity(2, 0, 0)).unwrap();
        assert_eq!(
            empty.select_oldest_eligible(8, 1, 1).unwrap(),
            RetirementSelectionResult::NoChange
        );
    }

    #[test]
    fn selection_returns_only_the_oldest_complete_eligible_prefix() {
        let bytes = sample_image();
        let tree = RetirementTree::new(&bytes, identity(20, 2, 3)).unwrap();

        assert_eq!(
            selected(tree.select_oldest_eligible(4, 10, 10).unwrap()),
            RetirementSelection {
                identity: identity(20, 2, 3),
                batch_count: 2,
                page_count: 3,
                last_retired_by_txn: 4,
            }
        );
        assert_eq!(
            selected(tree.select_oldest_eligible(3, 10, 10).unwrap()).batch_count,
            1
        );
        assert_eq!(
            tree.select_oldest_eligible(1, 10, 10).unwrap(),
            RetirementSelectionResult::NoChange
        );
        assert_eq!(
            tree.select_oldest_eligible(0, 10, 10).unwrap(),
            RetirementSelectionResult::NoChange
        );
        assert_eq!(
            selected(tree.select_oldest_eligible(8, 1, 10).unwrap()).page_count,
            2
        );
        assert_eq!(
            selected(tree.select_oldest_eligible(8, 10, 2).unwrap()).batch_count,
            1
        );
        assert_eq!(
            tree.select_oldest_eligible(8, 10, 1).unwrap_err(),
            RetirementReadError::WorkLimitTooSmall { required_pages: 2 }
        );
        assert_eq!(
            tree.select_oldest_eligible(8, 0, 1).unwrap_err(),
            RetirementReadError::WorkLimitZero
        );

        let mut bad_crc = bytes.clone();
        page_mut(&mut bad_crc, 2)[PAGE_CRC_OFFSET] ^= 1;
        let tree = RetirementTree::new(&bad_crc, identity(20, 2, 3)).unwrap();
        assert_eq!(
            selected(tree.select_oldest_eligible(3, 10, 10).unwrap()).batch_count,
            1
        );
    }

    #[test]
    fn verified_first_pass_and_ordinary_second_pass_yield_exact_pages() {
        let bytes = sample_image();
        let tree = RetirementTree::new(&bytes, identity(20, 2, 3)).unwrap();
        let selection = selected(tree.select_oldest_eligible(4, 10, 10).unwrap());
        let mut scratch = [EMPTY_BATCH; 2];
        let verified = tree.verify_selection(selection, &mut scratch).unwrap();
        let mut yielded = Vec::new();
        let result = verified
            .second_pass(&tree, |batch, page| {
                yielded.push((batch.retired_by_txn, page));
                Ok::<_, ()>(())
            })
            .unwrap();

        assert_eq!(yielded, [(2, 10), (2, 11), (4, 12)]);
        assert_eq!(
            result,
            RetirementPassResult {
                batch_count: 2,
                page_count: 3,
            }
        );
    }

    #[test]
    fn verification_checks_crc_blob_length_order_range_and_scratch() {
        let bytes = sample_image();
        let tree = RetirementTree::new(&bytes, identity(20, 2, 3)).unwrap();
        let selection = selected(tree.select_oldest_eligible(4, 10, 10).unwrap());
        assert_eq!(
            tree.verify_selection(selection, &mut [EMPTY_BATCH; 1])
                .unwrap_err(),
            RetirementReadError::VerificationBufferTooSmall {
                required_batches: 2,
            }
        );

        let mut bad = bytes.clone();
        page_mut(&mut bad, 2)[PAGE_CRC_OFFSET] ^= 1;
        let bad_tree = RetirementTree::new(&bad, identity(20, 2, 3)).unwrap();
        assert!(matches!(
            bad_tree.verify_selection(selection, &mut [EMPTY_BATCH; 2]),
            Err(RetirementReadError::Page(RetirementPageError::Checksum))
        ));

        bad = bytes.clone();
        page_mut(&mut bad, 3)[PAGE_CRC_OFFSET] ^= 1;
        let bad_tree = RetirementTree::new(&bad, identity(20, 2, 3)).unwrap();
        assert!(matches!(
            bad_tree.verify_selection(selection, &mut [EMPTY_BATCH; 2]),
            Err(RetirementReadError::Blob(BlobReadError::Page(
                BlobPageError::Checksum
            )))
        ));

        bad = bytes.clone();
        retirement_blob(page_mut(&mut bad, 3), &[10, 10]);
        let bad_tree = RetirementTree::new(&bad, identity(20, 2, 3)).unwrap();
        assert!(matches!(
            bad_tree.verify_selection(selection, &mut [EMPTY_BATCH; 2]),
            Err(RetirementReadError::Blob(
                BlobReadError::RetirementPageOrder {
                    previous: 10,
                    current: 10,
                }
            ))
        ));

        bad = bytes.clone();
        retirement_blob(page_mut(&mut bad, 3), &[1, 10]);
        let bad_tree = RetirementTree::new(&bad, identity(20, 2, 3)).unwrap();
        assert!(matches!(
            bad_tree.verify_selection(selection, &mut [EMPTY_BATCH; 2]),
            Err(RetirementReadError::Blob(
                BlobReadError::RetirementPageOutOfBounds(1)
            ))
        ));

        bad = bytes.clone();
        retirement_blob(page_mut(&mut bad, 3), &[10]);
        let bad_tree = RetirementTree::new(&bad, identity(20, 2, 3)).unwrap();
        assert!(matches!(
            bad_tree.verify_selection(selection, &mut [EMPTY_BATCH; 2]),
            Err(RetirementReadError::Blob(
                BlobReadError::NonfinalLeafLength(4)
            ))
        ));
    }

    #[test]
    fn second_pass_rechecks_full_identity_and_all_batch_roots_before_yielding() {
        let bytes = sample_image();
        let tree = RetirementTree::new(&bytes, identity(20, 2, 3)).unwrap();
        let selection = selected(tree.select_oldest_eligible(4, 10, 10).unwrap());
        let mut scratch = [EMPTY_BATCH; 2];
        let verified = tree.verify_selection(selection, &mut scratch).unwrap();

        let mut changed_identity = identity(20, 2, 3);
        changed_identity.commit_nonce = [3; 16];
        let changed_tree = RetirementTree::new(&bytes, changed_identity).unwrap();
        let mut calls = 0;
        assert!(matches!(
            verified.second_pass(&changed_tree, |_, _| {
                calls += 1;
                Ok::<_, ()>(())
            }),
            Err(RetirementSecondPassError::Read(
                RetirementReadError::SelectionChanged
            ))
        ));
        assert_eq!(calls, 0);

        let mut changed_bytes = bytes.clone();
        let second_root_at = usize::from(PAGE_HEADER_SIZE) + 32 + 24;
        page_mut(&mut changed_bytes, 2)[second_root_at..second_root_at + 4]
            .copy_from_slice(&6u32.to_le_bytes());
        let changed_tree = RetirementTree::new(&changed_bytes, identity(20, 2, 3)).unwrap();
        assert!(matches!(
            verified.second_pass(&changed_tree, |_, _| {
                calls += 1;
                Ok::<_, ()>(())
            }),
            Err(RetirementSecondPassError::Read(
                RetirementReadError::SelectionChanged
            ))
        ));
        assert_eq!(calls, 0);

        let mut sink_calls = 0;
        assert!(matches!(
            verified.second_pass(&tree, |_, _| {
                sink_calls += 1;
                Err::<(), _>(7u8)
            }),
            Err(RetirementSecondPassError::Sink(7))
        ));
        assert_eq!(sink_calls, 1);
    }

    #[test]
    fn branch_tree_checks_exact_child_maxima_counts_and_maximum_depth() {
        let mut bytes = image(20);
        retirement_branch(page_mut(&mut bytes, 2), 1, &[(4, 3), (6, 4)]);
        retirement_leaf(
            page_mut(&mut bytes, 3),
            &[
                RetirementBatch {
                    retired_by_txn: 2,
                    page_count: 1,
                    page_list_blob_root: 5,
                },
                RetirementBatch {
                    retired_by_txn: 4,
                    page_count: 1,
                    page_list_blob_root: 6,
                },
            ],
        );
        retirement_leaf(
            page_mut(&mut bytes, 4),
            &[RetirementBatch {
                retired_by_txn: 6,
                page_count: 1,
                page_list_blob_root: 7,
            }],
        );
        retirement_blob(page_mut(&mut bytes, 5), &[10]);
        retirement_blob(page_mut(&mut bytes, 6), &[11]);
        retirement_blob(page_mut(&mut bytes, 7), &[12]);
        let tree = RetirementTree::new(&bytes, identity(20, 2, 3)).unwrap();
        let selection = selected(tree.select_oldest_eligible(8, 10, 10).unwrap());
        let mut scratch = [EMPTY_BATCH; 3];
        tree.verify_selection(selection, &mut scratch).unwrap();

        retirement_branch(page_mut(&mut bytes, 2), 1, &[(3, 3), (6, 4)]);
        let bad_tree = RetirementTree::new(&bytes, identity(20, 2, 3)).unwrap();
        assert_eq!(
            bad_tree.select_oldest_eligible(8, 10, 10).unwrap_err(),
            RetirementReadError::ChildMaximumMismatch {
                expected: 3,
                actual: 4,
            }
        );

        retirement_branch(page_mut(&mut bytes, 2), MAX_TREE_LEVEL + 1, &[(6, 3)]);
        let bad_tree = RetirementTree::new(&bytes, identity(20, 2, 3)).unwrap();
        assert_eq!(
            bad_tree.select_oldest_eligible(8, 10, 10).unwrap_err(),
            RetirementReadError::Page(RetirementPageError::Header(PageHeaderError::LevelTooHigh(
                MAX_TREE_LEVEL + 1
            ),))
        );

        let mut deep = image(36);
        for level in (1..=MAX_TREE_LEVEL).rev() {
            let pgno = 2 + u32::from(MAX_TREE_LEVEL - level);
            retirement_branch(page_mut(&mut deep, pgno), level, &[(2, pgno + 1)]);
        }
        retirement_leaf(
            page_mut(&mut deep, 33),
            &[RetirementBatch {
                retired_by_txn: 2,
                page_count: 1,
                page_list_blob_root: 34,
            }],
        );
        retirement_blob(page_mut(&mut deep, 34), &[35]);
        let mut deep_identity = identity(36, 2, 1);
        deep_identity.txn_id = 3;
        let deep_tree = RetirementTree::new(&deep, deep_identity).unwrap();
        let selection = selected(deep_tree.select_oldest_eligible(3, 1, 1).unwrap());
        deep_tree
            .verify_selection(selection, &mut [EMPTY_BATCH; 1])
            .unwrap();
    }

    #[test]
    fn cursor_reports_declared_batch_count_mismatch_atomically() {
        let mut bytes = image(6);
        retirement_leaf(
            page_mut(&mut bytes, 2),
            &[RetirementBatch {
                retired_by_txn: 2,
                page_count: 1,
                page_list_blob_root: 3,
            }],
        );
        retirement_blob(page_mut(&mut bytes, 3), &[4]);
        let tree = RetirementTree::new(&bytes, identity(6, 2, 2)).unwrap();
        assert_eq!(
            tree.select_oldest_eligible(8, 10, 10).unwrap_err(),
            RetirementReadError::BatchCountMismatch {
                declared: 2,
                actual: 1,
            }
        );
    }

    #[test]
    fn positional_source_preserves_fork_io_short_read_and_torn_page_evidence() {
        let bytes = sample_image();
        let tree = RetirementTree::from_source(
            FailingSource {
                access: Some(PageSourceError::ForkedHandle),
                read: None,
            },
            identity(20, 2, 3),
        )
        .unwrap();
        assert_eq!(
            tree.select_oldest_eligible(8, 1, 2),
            Err(RetirementReadError::Source(PageSourceError::ForkedHandle))
        );

        let io = PageSourceError::Io(crate::page_source::PageIoEvidence {
            kind: crate::page_source::PageIoKind::PermissionDenied,
            raw_os_error: Some(13),
        });
        let tree = RetirementTree::from_source(
            FailingSource {
                access: None,
                read: Some(io),
            },
            identity(20, 2, 3),
        )
        .unwrap();
        assert_eq!(
            tree.select_oldest_eligible(8, 1, 2),
            Err(RetirementReadError::Source(io))
        );

        let tree = RetirementTree::from_source(
            SlicePageSource::new(&bytes[..2 * PAGE_SIZE + 17], 20),
            identity(20, 2, 3),
        )
        .unwrap();
        assert_eq!(
            tree.select_oldest_eligible(8, 1, 2),
            Err(RetirementReadError::Source(PageSourceError::ShortRead {
                offset: (2 * PAGE_SIZE) as u64,
                expected: PAGE_SIZE,
                actual: 17,
            }))
        );

        let tree = RetirementTree::from_source(
            TornSource {
                bytes: &bytes,
                page_count: 20,
            },
            identity(20, 2, 3),
        )
        .unwrap();
        assert_eq!(
            tree.select_oldest_eligible(8, 1, 2),
            Err(RetirementReadError::Page(
                RetirementPageError::BlobRootOutOfBounds(u32::MAX)
            ))
        );
    }

    #[test]
    fn second_pass_rechecks_same_source_before_yielding_and_warmed_paths_allocate_nothing() {
        let source = MutableSource {
            bytes: RefCell::new(sample_image()),
            page_count: 20,
        };
        let tree = RetirementTree::from_source(&source, identity(20, 2, 3)).unwrap();
        let selection = selected(tree.select_oldest_eligible(4, 10, 10).unwrap());
        let mut scratch = [EMPTY_BATCH; 2];
        let verified = tree.verify_selection(selection, &mut scratch).unwrap();

        let second_root_at = usize::from(PAGE_HEADER_SIZE) + 32 + 24;
        source.bytes.borrow_mut()
            [2 * PAGE_SIZE + second_root_at..2 * PAGE_SIZE + second_root_at + 4]
            .copy_from_slice(&6u32.to_le_bytes());
        let mut calls = 0;
        assert!(matches!(
            verified.second_pass(&tree, |_, _| {
                calls += 1;
                Ok::<_, ()>(())
            }),
            Err(RetirementSecondPassError::Read(
                RetirementReadError::SelectionChanged
            ))
        ));
        assert_eq!(calls, 0);

        let bytes = sample_image();
        let tree = RetirementTree::new(&bytes, identity(20, 2, 3)).unwrap();
        let ((), allocations) = count_thread_allocations(|| {
            for _ in 0..64 {
                let selection = selected(tree.select_oldest_eligible(4, 10, 10).unwrap());
                let mut scratch = [EMPTY_BATCH; 2];
                let verified = tree.verify_selection(selection, &mut scratch).unwrap();
                let mut count = 0u64;
                verified
                    .second_pass(&tree, |_, _| {
                        count += 1;
                        Ok::<_, ()>(())
                    })
                    .unwrap();
                assert_eq!(count, 3);
            }
        });
        assert_eq!(allocations, 0);
    }
}
