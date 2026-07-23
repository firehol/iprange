//! Bounds-safe, allocation-free ordinary traversal of the exact v4 range tree.

use core::marker::PhantomData;

use crate::bootstrap::{self, Bootstrap, BootstrapError, OpenMode};
use crate::cardinality::{Cardinality129, CardinalityOverflow};
use crate::contract::{AddressFamily, MetaV4, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::key::IpKey;
use crate::page::{PageHeader, PageType};
use crate::page_source::{PageSourceError, PinnedPageSource, PositionalRead};
use crate::range_page::{RangeBranch, RangePageError, RangeRecord};

const PATH_CAPACITY: usize = MAX_TREE_LEVEL as usize + 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RangeReadError {
    Bootstrap(BootstrapError),
    Source(PageSourceError),
    Page(RangePageError),
    WrongKeyFamily,
    RootType(PageType),
    ChildType(PageType),
    ChildLevel { expected: u16, actual: u16 },
    SummaryMismatch,
    RecordCountMismatch,
    CardinalityOverflow,
    CursorFailed,
}

impl From<BootstrapError> for RangeReadError {
    fn from(value: BootstrapError) -> Self {
        Self::Bootstrap(value)
    }
}

impl From<RangePageError> for RangeReadError {
    fn from(value: RangePageError) -> Self {
        Self::Page(value)
    }
}

impl From<PageSourceError> for RangeReadError {
    fn from(value: PageSourceError) -> Self {
        Self::Source(value)
    }
}

impl From<CardinalityOverflow> for RangeReadError {
    fn from(_: CardinalityOverflow) -> Self {
        Self::CardinalityOverflow
    }
}

#[derive(Debug)]
pub(crate) struct RangeTree<'a, K: IpKey, S: PositionalRead + ?Sized = [u8]> {
    pages: PinnedPageSource<'a, S>,
    _key: PhantomData<K>,
}

impl<'a, K: IpKey> RangeTree<'a, K, [u8]> {
    pub(crate) fn open_immutable(bytes: &'a [u8]) -> Result<Self, RangeReadError> {
        let bootstrap = bootstrap::open(bytes, OpenMode::ImmutableReader)?;
        Self::from_source(bytes, bootstrap)
    }
}

impl<'a, K: IpKey, S: PositionalRead + ?Sized> RangeTree<'a, K, S> {
    pub(crate) fn from_source(source: &'a S, bootstrap: Bootstrap) -> Result<Self, RangeReadError> {
        if bootstrap.meta.address_family != K::FAMILY {
            return Err(RangeReadError::WrongKeyFamily);
        }
        Ok(Self {
            pages: PinnedPageSource::new(source, bootstrap)?,
            _key: PhantomData,
        })
    }

    #[inline]
    pub(crate) const fn meta(&self) -> MetaV4 {
        self.pages.bootstrap().meta
    }

    pub(crate) fn cursor(&self) -> RangeCursor<'_, 'a, K, S> {
        RangeCursor {
            tree: self,
            path: [Frame { pgno: 0, index: 0 }; PATH_CAPACITY],
            root_level: 0,
            state: CursorState::Unpositioned,
            scratch: [0; PAGE_SIZE],
            scratch_pgno: None,
        }
    }

    pub(crate) fn lookup(&self, target: K) -> Result<Option<RangeRecord<K>>, RangeReadError> {
        self.pages.check_access()?;
        let mut cursor = self.cursor();
        if !cursor.seek_after_access(target)? {
            return Ok(None);
        }
        let record = cursor
            .current_inner()?
            .ok_or(RangeReadError::SummaryMismatch)?;
        if record.from <= target && target <= record.to {
            Ok(Some(record))
        } else {
            Ok(None)
        }
    }

    pub(crate) fn count_addresses(&self) -> Result<Cardinality129, RangeReadError> {
        let mut cursor = self.cursor();
        if !cursor.first()? {
            return Ok(Cardinality129::ZERO);
        }
        let mut total = Cardinality129::ZERO;
        loop {
            let record = cursor.current()?.ok_or(RangeReadError::SummaryMismatch)?;
            let from = record.from.to_u128();
            let to = record.to.to_u128();
            let count = if K::FAMILY == AddressFamily::Ipv6 && from == 0 && to == u128::MAX {
                Cardinality129::FULL_IPV6_SPACE
            } else {
                Cardinality129::from_u128(to - from + 1)
            };
            total = total.checked_add(count)?;
            if !cursor.next()? {
                break;
            }
        }
        Ok(total)
    }

    fn read_page(&self, pgno: u32, page: &mut [u8; PAGE_SIZE]) -> Result<(), RangeReadError> {
        self.pages.read_page(pgno, page).map_err(Into::into)
    }

    fn header(&self, page: &[u8; PAGE_SIZE]) -> Result<PageHeader, RangeReadError> {
        PageHeader::decode(page, self.meta().txn_id)
            .map_err(RangePageError::from)
            .map_err(RangeReadError::from)
    }

    fn branch<'page>(
        &self,
        page: &'page [u8; PAGE_SIZE],
        expected_level: u16,
    ) -> Result<RangeBranch<'page, K>, RangeReadError> {
        let branch = RangeBranch::open(
            page,
            self.meta().txn_id,
            self.meta().address_family,
            self.meta().page_count,
        )?;
        if branch.level != expected_level {
            return Err(RangeReadError::ChildLevel {
                expected: expected_level,
                actual: branch.level,
            });
        }
        Ok(branch)
    }

    fn leaf<'page>(
        &self,
        page: &'page [u8; PAGE_SIZE],
    ) -> Result<crate::range_page::RangeLeaf<'page, K>, RangeReadError> {
        Ok(crate::range_page::RangeLeaf::open(
            page,
            self.meta().txn_id,
            self.meta().address_family,
            self.meta().value_kind,
        )?)
    }
}

#[derive(Clone, Copy, Debug)]
struct Frame {
    pgno: u32,
    index: u16,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum CursorState {
    Unpositioned,
    Empty,
    At,
    BeforeFirst,
    AfterLast,
    Failed,
}

#[derive(Debug)]
pub(crate) struct RangeCursor<'tree, 'source, K: IpKey, S: PositionalRead + ?Sized = [u8]> {
    tree: &'tree RangeTree<'source, K, S>,
    path: [Frame; PATH_CAPACITY],
    root_level: u16,
    state: CursorState,
    scratch: [u8; PAGE_SIZE],
    scratch_pgno: Option<u32>,
}

impl<'tree, 'source, K: IpKey, S: PositionalRead + ?Sized> RangeCursor<'tree, 'source, K, S> {
    pub(crate) fn first(&mut self) -> Result<bool, RangeReadError> {
        self.check_access()?;
        self.atomic_move(Self::first_inner)
    }

    fn first_inner(&mut self) -> Result<bool, RangeReadError> {
        let Some((root, level)) = self.root()? else {
            self.state = CursorState::Empty;
            return Ok(false);
        };
        self.root_level = level;
        if self.descend_edge(0, root, level, Edge::First)? {
            self.state = CursorState::At;
            return Ok(true);
        }
        if self.tree.meta().range_record_count != 0 {
            return Err(RangeReadError::RecordCountMismatch);
        }
        self.state = CursorState::Empty;
        Ok(false)
    }

    pub(crate) fn last(&mut self) -> Result<bool, RangeReadError> {
        self.check_access()?;
        self.atomic_move(Self::last_inner)
    }

    fn last_inner(&mut self) -> Result<bool, RangeReadError> {
        let Some((root, level)) = self.root()? else {
            self.state = CursorState::Empty;
            return Ok(false);
        };
        self.root_level = level;
        if self.descend_edge(0, root, level, Edge::Last)? {
            self.state = CursorState::At;
            return Ok(true);
        }
        if self.tree.meta().range_record_count != 0 {
            return Err(RangeReadError::RecordCountMismatch);
        }
        self.state = CursorState::Empty;
        Ok(false)
    }

    /// Position at the record covering `target`, or its first successor.
    pub(crate) fn seek(&mut self, target: K) -> Result<bool, RangeReadError> {
        self.check_access()?;
        self.seek_after_access(target)
    }

    fn seek_after_access(&mut self, target: K) -> Result<bool, RangeReadError> {
        if self.state == CursorState::Failed {
            return Err(RangeReadError::CursorFailed);
        }
        match self.seek_inner(target) {
            Ok(positioned) => Ok(positioned),
            Err(error) => {
                self.state = CursorState::Failed;
                Err(error)
            }
        }
    }

    fn seek_inner(&mut self, target: K) -> Result<bool, RangeReadError> {
        let Some((root, level)) = self.root()? else {
            self.state = CursorState::Empty;
            return Ok(false);
        };
        self.root_level = level;
        if !self.descend_predecessor(0, root, level, target)? {
            return self.first_inner();
        }
        self.state = CursorState::At;
        let record = self
            .current_inner()?
            .ok_or(RangeReadError::SummaryMismatch)?;
        if record.to >= target {
            Ok(true)
        } else {
            self.next_inner()
        }
    }

    pub(crate) fn next(&mut self) -> Result<bool, RangeReadError> {
        self.check_access()?;
        self.atomic_move(Self::next_inner)
    }

    fn next_inner(&mut self) -> Result<bool, RangeReadError> {
        match self.state {
            CursorState::Unpositioned | CursorState::BeforeFirst => return self.first_inner(),
            CursorState::Empty | CursorState::AfterLast => return Ok(false),
            CursorState::At => {}
            CursorState::Failed => return Err(RangeReadError::CursorFailed),
        }
        let leaf_depth = usize::from(self.root_level);
        let leaf_frame = self.path[leaf_depth];
        self.load_page(leaf_frame.pgno)?;
        let leaf_len = self.tree.leaf(&self.scratch)?.len();
        if usize::from(leaf_frame.index) + 1 < leaf_len {
            self.path[leaf_depth].index += 1;
            return Ok(true);
        }

        let mut depth = leaf_depth;
        while depth != 0 {
            depth -= 1;
            let frame = self.path[depth];
            let level = self.root_level - depth as u16;
            self.load_page(frame.pgno)?;
            let branch = self.tree.branch(&self.scratch, level)?;
            if let Some(index) = branch.next_nonempty(usize::from(frame.index) + 1)? {
                let child = branch.entry(index)?.child_pgno;
                self.path[depth].index = index as u16;
                if !self.descend_edge(depth + 1, child, level - 1, Edge::First)? {
                    return Err(RangeReadError::SummaryMismatch);
                }
                return Ok(true);
            }
        }
        self.state = CursorState::AfterLast;
        Ok(false)
    }

    pub(crate) fn previous(&mut self) -> Result<bool, RangeReadError> {
        self.check_access()?;
        self.atomic_move(Self::previous_inner)
    }

    fn previous_inner(&mut self) -> Result<bool, RangeReadError> {
        match self.state {
            CursorState::Unpositioned | CursorState::AfterLast => return self.last_inner(),
            CursorState::Empty | CursorState::BeforeFirst => return Ok(false),
            CursorState::At => {}
            CursorState::Failed => return Err(RangeReadError::CursorFailed),
        }
        let leaf_depth = usize::from(self.root_level);
        if self.path[leaf_depth].index != 0 {
            self.path[leaf_depth].index -= 1;
            return Ok(true);
        }

        let mut depth = leaf_depth;
        while depth != 0 {
            depth -= 1;
            let frame = self.path[depth];
            let level = self.root_level - depth as u16;
            self.load_page(frame.pgno)?;
            let branch = self.tree.branch(&self.scratch, level)?;
            if let Some(index) = branch.previous_nonempty(usize::from(frame.index))? {
                let child = branch.entry(index)?.child_pgno;
                self.path[depth].index = index as u16;
                if !self.descend_edge(depth + 1, child, level - 1, Edge::Last)? {
                    return Err(RangeReadError::SummaryMismatch);
                }
                return Ok(true);
            }
        }
        self.state = CursorState::BeforeFirst;
        Ok(false)
    }

    pub(crate) fn current(&mut self) -> Result<Option<RangeRecord<K>>, RangeReadError> {
        self.check_access()?;
        self.current_inner()
    }

    fn current_inner(&mut self) -> Result<Option<RangeRecord<K>>, RangeReadError> {
        if self.state == CursorState::Failed {
            return Err(RangeReadError::CursorFailed);
        }
        if self.state != CursorState::At {
            return Ok(None);
        }
        let frame = self.path[usize::from(self.root_level)];
        let result = self
            .load_page(frame.pgno)
            .and_then(|()| self.tree.leaf(&self.scratch))
            .and_then(|leaf| leaf.record(usize::from(frame.index)).map_err(Into::into));
        match result {
            Ok(record) => Ok(Some(record)),
            Err(error) => {
                self.state = CursorState::Failed;
                Err(error)
            }
        }
    }

    fn root(&mut self) -> Result<Option<(u32, u16)>, RangeReadError> {
        let root = self.tree.meta().range_root;
        if root == 0 {
            return Ok(None);
        }
        self.load_page(root)?;
        let header = self.tree.header(&self.scratch)?;
        match header.page_type {
            PageType::RangeLeaf => Ok(Some((root, 0))),
            PageType::RangeBranch => Ok(Some((root, header.level))),
            other => Err(RangeReadError::RootType(other)),
        }
    }

    fn descend_edge(
        &mut self,
        mut depth: usize,
        mut pgno: u32,
        mut level: u16,
        edge: Edge,
    ) -> Result<bool, RangeReadError> {
        loop {
            if depth >= PATH_CAPACITY {
                return Err(RangeReadError::ChildLevel {
                    expected: 0,
                    actual: level,
                });
            }
            if level == 0 {
                self.load_page(pgno)?;
                let header = self.tree.header(&self.scratch)?;
                if header.page_type != PageType::RangeLeaf {
                    return Err(RangeReadError::ChildType(header.page_type));
                }
                let leaf = self.tree.leaf(&self.scratch)?;
                if leaf.is_empty() {
                    return Ok(false);
                }
                self.path[depth] = Frame {
                    pgno,
                    index: match edge {
                        Edge::First => 0,
                        Edge::Last => (leaf.len() - 1) as u16,
                    },
                };
                self.state = CursorState::At;
                return Ok(true);
            }

            self.load_page(pgno)?;
            let header = self.tree.header(&self.scratch)?;
            if header.page_type != PageType::RangeBranch {
                return Err(RangeReadError::ChildType(header.page_type));
            }
            let branch = self.tree.branch(&self.scratch, level)?;
            let index = match edge {
                Edge::First => branch.first_nonempty()?,
                Edge::Last => branch.previous_nonempty(branch.len())?,
            };
            let Some(index) = index else {
                return Ok(false);
            };
            self.path[depth] = Frame {
                pgno,
                index: index as u16,
            };
            pgno = branch.entry(index)?.child_pgno;
            level -= 1;
            depth += 1;
        }
    }

    fn descend_predecessor(
        &mut self,
        mut depth: usize,
        mut pgno: u32,
        mut level: u16,
        target: K,
    ) -> Result<bool, RangeReadError> {
        loop {
            if depth >= PATH_CAPACITY {
                return Err(RangeReadError::ChildLevel {
                    expected: 0,
                    actual: level,
                });
            }
            if level == 0 {
                self.load_page(pgno)?;
                let header = self.tree.header(&self.scratch)?;
                if header.page_type != PageType::RangeLeaf {
                    return Err(RangeReadError::ChildType(header.page_type));
                }
                let leaf = self.tree.leaf(&self.scratch)?;
                let mut low = 0usize;
                let mut high = leaf.len();
                while low < high {
                    let middle = low + (high - low) / 2;
                    if leaf.record(middle)?.from <= target {
                        low = middle + 1;
                    } else {
                        high = middle;
                    }
                }
                if low == 0 {
                    return self.fallback_predecessor(depth);
                }
                self.path[depth] = Frame {
                    pgno,
                    index: (low - 1) as u16,
                };
                return Ok(true);
            }

            self.load_page(pgno)?;
            let header = self.tree.header(&self.scratch)?;
            if header.page_type != PageType::RangeBranch {
                return Err(RangeReadError::ChildType(header.page_type));
            }
            let branch = self.tree.branch(&self.scratch, level)?;
            let Some(index) = branch.predecessor_for(target)? else {
                return self.fallback_predecessor(depth);
            };
            self.path[depth] = Frame {
                pgno,
                index: index as u16,
            };
            pgno = branch.entry(index)?.child_pgno;
            level -= 1;
            depth += 1;
        }
    }

    fn fallback_predecessor(&mut self, mut depth: usize) -> Result<bool, RangeReadError> {
        while depth != 0 {
            depth -= 1;
            let frame = self.path[depth];
            let level = self.root_level - depth as u16;
            self.load_page(frame.pgno)?;
            let branch = self.tree.branch(&self.scratch, level)?;
            if let Some(index) = branch.previous_nonempty(usize::from(frame.index))? {
                let child = branch.entry(index)?.child_pgno;
                self.path[depth].index = index as u16;
                if !self.descend_edge(depth + 1, child, level - 1, Edge::Last)? {
                    return Err(RangeReadError::SummaryMismatch);
                }
                return Ok(true);
            }
        }
        Ok(false)
    }

    fn atomic_move(
        &mut self,
        operation: fn(&mut Self) -> Result<bool, RangeReadError>,
    ) -> Result<bool, RangeReadError> {
        if self.state == CursorState::Failed {
            return Err(RangeReadError::CursorFailed);
        }
        match operation(self) {
            Ok(positioned) => Ok(positioned),
            Err(error) => {
                self.state = CursorState::Failed;
                Err(error)
            }
        }
    }

    fn load_page(&mut self, pgno: u32) -> Result<(), RangeReadError> {
        if self.scratch_pgno == Some(pgno) {
            return Ok(());
        }
        self.scratch_pgno = None;
        self.tree.read_page(pgno, &mut self.scratch)?;
        self.scratch_pgno = Some(pgno);
        Ok(())
    }

    #[inline]
    fn check_access(&self) -> Result<(), RangeReadError> {
        self.tree.pages.check_access().map_err(Into::into)
    }
}

#[derive(Clone, Copy)]
enum Edge {
    First,
    Last,
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::contract::{ValueKind, ValueTag};
    use crate::key::{Ipv4Key, Ipv6Key};
    use crate::page::{write_crc32c, PageHeader, PAGE_HEADER_SIZE};
    use crate::range_page::{branch_entry_size, record_size};
    use std::{
        cell::{Cell, RefCell},
        vec,
        vec::Vec,
    };

    struct TornSource {
        bytes: RefCell<Vec<u8>>,
        torn: Cell<bool>,
    }

    struct CountingSource<'a> {
        bytes: &'a [u8],
        reads: Cell<usize>,
    }

    struct RejectableSource<'a> {
        bytes: &'a [u8],
        reject: Cell<bool>,
        reads: Cell<usize>,
    }

    impl PositionalRead for CountingSource<'_> {
        fn read_exact_at(&self, offset: u64, output: &mut [u8]) -> Result<(), PageSourceError> {
            self.reads.set(self.reads.get() + 1);
            self.bytes.read_exact_at(offset, output)
        }
    }

    impl PositionalRead for RejectableSource<'_> {
        fn check_access(&self) -> Result<(), PageSourceError> {
            if self.reject.get() {
                Err(PageSourceError::ForkedHandle)
            } else {
                Ok(())
            }
        }

        fn read_exact_at(&self, offset: u64, output: &mut [u8]) -> Result<(), PageSourceError> {
            self.reads.set(self.reads.get() + 1);
            self.bytes.read_exact_at(offset, output)
        }
    }

    impl PositionalRead for TornSource {
        fn read_exact_at(&self, offset: u64, output: &mut [u8]) -> Result<(), PageSourceError> {
            let start = usize::try_from(offset).map_err(|_| PageSourceError::OffsetOverflow)?;
            let end = start
                .checked_add(output.len())
                .ok_or(PageSourceError::OffsetOverflow)?;
            let mut bytes = self.bytes.borrow_mut();
            if offset == 2 * PAGE_SIZE as u64 && !self.torn.replace(true) {
                let split = usize::from(PAGE_HEADER_SIZE) + Ipv4Key::WIDTH;
                output[..split].copy_from_slice(&bytes[start..start + split]);
                bytes[start + split..start + split + 4].copy_from_slice(&u32::MAX.to_le_bytes());
                output[split..].copy_from_slice(&bytes[start + split..end]);
            } else {
                output.copy_from_slice(bytes.get(start..end).ok_or(PageSourceError::Io(
                    crate::page_source::PageIoEvidence {
                        kind: crate::page_source::PageIoKind::UnexpectedEof,
                        raw_os_error: None,
                    },
                ))?);
            }
            Ok(())
        }
    }

    fn put_header(
        page: &mut [u8; PAGE_SIZE],
        page_type: PageType,
        count: usize,
        level: u16,
        lower: usize,
        family: AddressFamily,
    ) {
        PageHeader {
            page_type,
            born_txn: 1,
            item_count: count as u16,
            level,
            lower: lower as u16,
            upper: PAGE_SIZE as u16,
            aux: family as u32,
            page_crc32c: 0,
        }
        .encode_into(page);
    }

    fn put_leaf<K: IpKey>(page: &mut [u8; PAGE_SIZE], records: &[(K, K, u32)]) {
        let size = record_size::<K>();
        put_header(
            page,
            PageType::RangeLeaf,
            records.len(),
            0,
            usize::from(PAGE_HEADER_SIZE) + records.len() * size,
            K::FAMILY,
        );
        for (index, &(from, to, value)) in records.iter().enumerate() {
            let at = usize::from(PAGE_HEADER_SIZE) + index * size;
            from.write_le(&mut page[at..at + K::WIDTH]);
            to.write_le(&mut page[at + K::WIDTH..at + 2 * K::WIDTH]);
            page[at + 2 * K::WIDTH..at + size].copy_from_slice(&value.to_le_bytes());
        }
        write_crc32c(page);
    }

    fn put_v4_branch(page: &mut [u8; PAGE_SIZE], entries: &[(u32, u32, u64, u32, u32, u32)]) {
        put_v4_branch_level(page, 1, entries);
    }

    fn put_v4_branch_level(
        page: &mut [u8; PAGE_SIZE],
        level: u16,
        entries: &[(u32, u32, u64, u32, u32, u32)],
    ) {
        put_header(
            page,
            PageType::RangeBranch,
            entries.len(),
            level,
            usize::from(PAGE_HEADER_SIZE) + entries.len() * branch_entry_size::<Ipv4Key>(),
            AddressFamily::Ipv4,
        );
        for (index, &(fence, child, count, first, last, to)) in entries.iter().enumerate() {
            let at = usize::from(PAGE_HEADER_SIZE) + index * 32;
            page[at..at + 4].copy_from_slice(&fence.to_le_bytes());
            page[at + 4..at + 8].copy_from_slice(&child.to_le_bytes());
            page[at + 8..at + 16].copy_from_slice(&count.to_le_bytes());
            page[at + 16..at + 20].copy_from_slice(&first.to_le_bytes());
            page[at + 20..at + 24].copy_from_slice(&last.to_le_bytes());
            page[at + 24..at + 28].copy_from_slice(&to.to_le_bytes());
        }
        write_crc32c(page);
    }

    fn image<K: IpKey>(
        root: u32,
        count: u64,
        pages: usize,
        fill: impl FnOnce(&mut [u8]),
    ) -> Vec<u8> {
        let mut meta = empty_direct_meta(1);
        meta.address_family = K::FAMILY;
        meta.value_kind = ValueKind::Direct;
        meta.value_tag = ValueTag::new(b"").unwrap();
        meta.page_count = pages as u64;
        meta.range_root = root;
        meta.range_record_count = count;
        let mut bytes = vec![0u8; pages * PAGE_SIZE];
        meta.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into((&mut bytes[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
        fill(&mut bytes);
        bytes
    }

    fn page_mut(bytes: &mut [u8], pgno: usize) -> &mut [u8; PAGE_SIZE] {
        (&mut bytes[pgno * PAGE_SIZE..(pgno + 1) * PAGE_SIZE])
            .try_into()
            .unwrap()
    }

    #[test]
    fn single_leaf_lookup_seek_and_count() {
        let bytes = image::<Ipv4Key>(2, 2, 3, |bytes| {
            put_leaf(
                page_mut(bytes, 2),
                &[(Ipv4Key(10), Ipv4Key(20), 1), (Ipv4Key(30), Ipv4Key(39), 2)],
            );
        });
        let tree = RangeTree::<Ipv4Key>::open_immutable(&bytes).unwrap();
        assert_eq!(tree.lookup(Ipv4Key(15)).unwrap().unwrap().value, 1);
        assert_eq!(tree.lookup(Ipv4Key(25)).unwrap(), None);
        assert_eq!(
            tree.count_addresses().unwrap(),
            Cardinality129::from_u64(21)
        );
    }

    #[test]
    fn cursor_skips_legal_empty_leaf_without_scaling_by_empty_records() {
        let bytes = image::<Ipv4Key>(2, 2, 6, |bytes| {
            put_v4_branch(
                page_mut(bytes, 2),
                &[
                    (0, 3, 1, 10, 10, 20),
                    (100, 4, 0, 0, 0, 0),
                    (200, 5, 1, 210, 210, 220),
                ],
            );
            put_leaf(page_mut(bytes, 3), &[(Ipv4Key(10), Ipv4Key(20), 1)]);
            put_leaf::<Ipv4Key>(page_mut(bytes, 4), &[]);
            put_leaf(page_mut(bytes, 5), &[(Ipv4Key(210), Ipv4Key(220), 2)]);
        });
        let tree = RangeTree::<Ipv4Key>::open_immutable(&bytes).unwrap();
        let mut cursor = tree.cursor();
        assert!(cursor.first().unwrap());
        assert_eq!(cursor.current().unwrap().unwrap().from, Ipv4Key(10));
        assert!(cursor.next().unwrap());
        assert_eq!(cursor.current().unwrap().unwrap().from, Ipv4Key(210));
        assert!(!cursor.next().unwrap());
        assert!(cursor.previous().unwrap());
        assert_eq!(cursor.current().unwrap().unwrap().from, Ipv4Key(210));
        assert!(cursor.previous().unwrap());
        assert_eq!(cursor.current().unwrap().unwrap().from, Ipv4Key(10));
        assert!(cursor.seek(Ipv4Key(150)).unwrap());
        assert_eq!(cursor.current().unwrap().unwrap().from, Ipv4Key(210));
    }

    #[test]
    fn legal_all_empty_branch_represents_empty_map() {
        let bytes = image::<Ipv4Key>(2, 0, 5, |bytes| {
            put_v4_branch(
                page_mut(bytes, 2),
                &[(0, 3, 0, 0, 0, 0), (100, 4, 0, 0, 0, 0)],
            );
            put_leaf::<Ipv4Key>(page_mut(bytes, 3), &[]);
            put_leaf::<Ipv4Key>(page_mut(bytes, 4), &[]);
        });
        let tree = RangeTree::<Ipv4Key>::open_immutable(&bytes).unwrap();
        assert!(!tree.cursor().first().unwrap());
        assert_eq!(tree.count_addresses().unwrap(), Cardinality129::ZERO);
    }

    #[test]
    fn full_ipv6_space_does_not_wrap_or_panic() {
        let bytes = image::<Ipv6Key>(2, 1, 3, |bytes| {
            put_leaf(page_mut(bytes, 2), &[(Ipv6Key::MIN, Ipv6Key::MAX, 42)]);
        });
        let tree = RangeTree::<Ipv6Key>::open_immutable(&bytes).unwrap();
        assert_eq!(
            tree.count_addresses().unwrap(),
            Cardinality129::FULL_IPV6_SPACE
        );
        assert_eq!(tree.lookup(Ipv6Key::MAX).unwrap().unwrap().value, 42);
    }

    #[test]
    fn split_ipv6_cardinalities_carry_exactly_into_bit_128() {
        let bytes = image::<Ipv6Key>(2, 2, 3, |bytes| {
            put_leaf(
                page_mut(bytes, 2),
                &[
                    (
                        Ipv6Key::MIN,
                        Ipv6Key {
                            hi: u64::MAX,
                            lo: u64::MAX - 1,
                        },
                        1,
                    ),
                    (Ipv6Key::MAX, Ipv6Key::MAX, 2),
                ],
            );
        });
        let tree = RangeTree::<Ipv6Key>::open_immutable(&bytes).unwrap();
        assert_eq!(
            tree.count_addresses().unwrap(),
            Cardinality129::FULL_IPV6_SPACE
        );
    }

    #[test]
    fn ordinary_reads_do_not_validate_page_crc() {
        let mut bytes = image::<Ipv4Key>(2, 1, 3, |bytes| {
            put_leaf(page_mut(bytes, 2), &[(Ipv4Key(10), Ipv4Key(20), 1)]);
        });
        bytes[2 * PAGE_SIZE + 28] ^= 1;
        let tree = RangeTree::<Ipv4Key>::open_immutable(&bytes).unwrap();
        assert_eq!(tree.lookup(Ipv4Key(15)).unwrap().unwrap().value, 1);
    }

    #[test]
    fn torn_source_cannot_turn_a_child_number_into_an_out_of_bounds_read() {
        let bytes = image::<Ipv4Key>(2, 1, 4, |bytes| {
            put_v4_branch(page_mut(bytes, 2), &[(0, 3, 1, 10, 10, 20)]);
            put_leaf(page_mut(bytes, 3), &[(Ipv4Key(10), Ipv4Key(20), 1)]);
        });
        let bootstrap = bootstrap::open(&bytes, OpenMode::ImmutableReader).unwrap();
        let source = TornSource {
            bytes: RefCell::new(bytes),
            torn: Cell::new(false),
        };
        let tree = RangeTree::<Ipv4Key, _>::from_source(&source, bootstrap).unwrap();
        assert_eq!(
            tree.lookup(Ipv4Key(15)),
            Err(RangeReadError::Page(RangePageError::ChildOutOfBounds(
                u32::MAX
            )))
        );
    }

    #[test]
    fn direct_hit_lookup_reads_root_and_leaf_once() {
        let bytes = image::<Ipv4Key>(2, 1, 4, |bytes| {
            put_v4_branch(page_mut(bytes, 2), &[(0, 3, 1, 10, 10, 20)]);
            put_leaf(page_mut(bytes, 3), &[(Ipv4Key(10), Ipv4Key(20), 1)]);
        });
        let bootstrap = bootstrap::open(&bytes, OpenMode::ImmutableReader).unwrap();
        let source = CountingSource {
            bytes: &bytes,
            reads: Cell::new(0),
        };
        let tree = RangeTree::<Ipv4Key, _>::from_source(&source, bootstrap).unwrap();
        assert_eq!(tree.lookup(Ipv4Key(15)).unwrap().unwrap().value, 1);
        assert_eq!(source.reads.get(), 2);
    }

    #[test]
    fn gap_lookup_rereads_only_the_bounded_ancestor_path() {
        let bytes = image::<Ipv4Key>(2, 2, 5, |bytes| {
            put_v4_branch(
                page_mut(bytes, 2),
                &[(0, 3, 1, 10, 10, 20), (30, 4, 1, 30, 30, 40)],
            );
            put_leaf(page_mut(bytes, 3), &[(Ipv4Key(10), Ipv4Key(20), 1)]);
            put_leaf(page_mut(bytes, 4), &[(Ipv4Key(30), Ipv4Key(40), 2)]);
        });
        let bootstrap = bootstrap::open(&bytes, OpenMode::ImmutableReader).unwrap();
        let source = CountingSource {
            bytes: &bytes,
            reads: Cell::new(0),
        };
        let tree = RangeTree::<Ipv4Key, _>::from_source(&source, bootstrap).unwrap();
        assert_eq!(tree.lookup(Ipv4Key(25)).unwrap(), None);
        assert_eq!(source.reads.get(), 4);
    }

    #[test]
    fn cached_cursor_content_still_checks_source_access() {
        let bytes = image::<Ipv4Key>(2, 2, 3, |bytes| {
            put_leaf(
                page_mut(bytes, 2),
                &[(Ipv4Key(10), Ipv4Key(20), 1), (Ipv4Key(30), Ipv4Key(40), 2)],
            );
        });
        let bootstrap = bootstrap::open(&bytes, OpenMode::ImmutableReader).unwrap();
        let source = RejectableSource {
            bytes: &bytes,
            reject: Cell::new(false),
            reads: Cell::new(0),
        };
        let tree = RangeTree::<Ipv4Key, _>::from_source(&source, bootstrap).unwrap();
        let mut cursor = tree.cursor();
        assert!(cursor.first().unwrap());
        assert_eq!(cursor.current().unwrap().unwrap().value, 1);
        let reads = source.reads.get();

        source.reject.set(true);
        assert_eq!(
            cursor.current(),
            Err(RangeReadError::Source(PageSourceError::ForkedHandle))
        );
        assert_eq!(
            cursor.next(),
            Err(RangeReadError::Source(PageSourceError::ForkedHandle))
        );
        assert_eq!(source.reads.get(), reads);
    }

    #[test]
    fn child_level_mismatch_and_cycle_are_bounded_errors() {
        let bytes = image::<Ipv4Key>(2, 1, 4, |bytes| {
            put_v4_branch(page_mut(bytes, 2), &[(0, 3, 1, 10, 10, 20)]);
            put_v4_branch(page_mut(bytes, 3), &[(0, 2, 1, 10, 10, 20)]);
        });
        let tree = RangeTree::<Ipv4Key>::open_immutable(&bytes).unwrap();
        let mut cursor = tree.cursor();
        assert!(matches!(
            cursor.first(),
            Err(RangeReadError::ChildType(PageType::RangeBranch))
        ));
        assert_eq!(cursor.current(), Err(RangeReadError::CursorFailed));
        assert_eq!(cursor.next(), Err(RangeReadError::CursorFailed));
        assert_eq!(cursor.previous(), Err(RangeReadError::CursorFailed));
    }

    #[test]
    fn current_record_decode_error_is_terminal() {
        let bytes = image::<Ipv4Key>(2, 1, 3, |bytes| {
            put_leaf(page_mut(bytes, 2), &[(Ipv4Key(20), Ipv4Key(10), 1)]);
        });
        let tree = RangeTree::<Ipv4Key>::open_immutable(&bytes).unwrap();
        let mut cursor = tree.cursor();
        assert!(cursor.first().unwrap());
        assert!(matches!(
            cursor.current(),
            Err(RangeReadError::Page(RangePageError::RangeReversed))
        ));
        assert_eq!(cursor.current(), Err(RangeReadError::CursorFailed));
        assert_eq!(cursor.next(), Err(RangeReadError::CursorFailed));
        assert_eq!(cursor.previous(), Err(RangeReadError::CursorFailed));
    }

    #[test]
    fn predecessor_falls_back_to_nearest_earlier_ancestor_sibling() {
        let bytes = image::<Ipv4Key>(2, 2, 7, |bytes| {
            put_v4_branch_level(
                page_mut(bytes, 2),
                2,
                &[(0, 4, 1, 10, 10, 150), (50, 3, 1, 50, 50, 250)],
            );
            put_v4_branch(page_mut(bytes, 4), &[(0, 5, 1, 10, 10, 150)]);
            put_v4_branch(page_mut(bytes, 3), &[(50, 6, 1, 200, 200, 250)]);
            put_leaf(page_mut(bytes, 5), &[(Ipv4Key(10), Ipv4Key(150), 1)]);
            put_leaf(page_mut(bytes, 6), &[(Ipv4Key(200), Ipv4Key(250), 2)]);
        });
        let tree = RangeTree::<Ipv4Key>::open_immutable(&bytes).unwrap();
        assert_eq!(tree.lookup(Ipv4Key(100)).unwrap().unwrap().value, 1);
    }

    #[test]
    fn maximum_legal_level_uses_exactly_the_fixed_stack_capacity() {
        let bytes = image::<Ipv4Key>(2, 1, 34, |bytes| {
            for level in (1u16..=MAX_TREE_LEVEL).rev() {
                let pgno = usize::from(MAX_TREE_LEVEL - level) + 2;
                put_v4_branch_level(
                    page_mut(bytes, pgno),
                    level,
                    &[(0, pgno as u32 + 1, 1, 10, 10, 20)],
                );
            }
            put_leaf(page_mut(bytes, 33), &[(Ipv4Key(10), Ipv4Key(20), 1)]);
        });
        let tree = RangeTree::<Ipv4Key>::open_immutable(&bytes).unwrap();
        assert_eq!(tree.lookup(Ipv4Key(15)).unwrap().unwrap().value, 1);
    }
}
