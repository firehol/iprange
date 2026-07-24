//! Bounded packing of an already canonical range stream into exact v4 pages.
//!
//! This layer deliberately does not normalize input and does not allocate page
//! numbers.  A transaction-owned sink supplies already-authorized private
//! pages; this builder retains only the unfinished leaf and two unfinished
//! branch groups at each compact tree level.

use crate::contract::{ValueKind, MAX_PAGE_COUNT, PAGE_SIZE};
use crate::key::IpKey;
use crate::page::PAGE_HEADER_SIZE;
use crate::range_page::{
    branch_capacity, encode_branch, encode_leaf, leaf_capacity, RangeBranchEntry,
    RangePageWriteError, RangeRecord,
};

// IPv6 has the smaller branch fanout (50). Six compact branch levels can
// cover more than every addressable non-meta page in a u32 page space.
const COMPACT_BRANCH_LEVELS: usize = 6;
const MAX_LEAF_RECORDS: usize = (PAGE_SIZE - PAGE_HEADER_SIZE as usize) / 12;
const MAX_BRANCH_ENTRIES: usize = (PAGE_SIZE - PAGE_HEADER_SIZE as usize) / 32;

/// A transaction-owned destination for one complete private range page.
///
/// The sink chooses one unique page number in the target draft and must copy
/// or persist `page` before returning. The packer reuses the page buffer for
/// every following write and never owns allocation, cleanup, or rollback.
pub(crate) trait RangeTreePageSink {
    type Error;

    fn write_range_page(&mut self, page: &[u8; PAGE_SIZE]) -> Result<u32, Self::Error>;
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RangeTreeBuildStartError {
    BornTransactionZero,
    PageCount { page_count: u64 },
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum RangeTreeBuildError<E> {
    Finished,
    Failed,
    RangeReversed,
    MembershipValueZero,
    RangeOverlap,
    AdjacentEqualValue,
    RecordCountOverflow,
    Page(RangePageWriteError),
    Sink(E),
    SinkPageOutOfBounds(u32),
    TreeTooDeep,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RangeTreeBuildResult {
    pub(crate) root_pgno: u32,
    pub(crate) root_level: u16,
    pub(crate) record_count: u64,
}

#[derive(Clone, Copy, Debug)]
struct RangeTreeNode<K: IpKey> {
    pgno: u32,
    level: u16,
    record_count: u64,
    lower_fence: K,
    first_from: K,
    last_from: K,
    last_to: K,
}

impl<K: IpKey> RangeTreeNode<K> {
    fn branch_entry(self) -> RangeBranchEntry<K> {
        RangeBranchEntry {
            lower_fence: self.lower_fence,
            child_pgno: self.pgno,
            subtree_record_count: self.record_count,
            first_from: self.first_from,
            last_from: self.last_from,
            last_to: self.last_to,
        }
    }
}

#[derive(Clone, Copy, Debug)]
struct RangeTreeBuildLevel<K: IpKey> {
    current: [RangeBranchEntry<K>; MAX_BRANCH_ENTRIES],
    current_len: usize,
    pending: [RangeBranchEntry<K>; MAX_BRANCH_ENTRIES],
    pending_len: usize,
    emitted_count: u64,
}

impl<K: IpKey> RangeTreeBuildLevel<K> {
    fn empty() -> Self {
        let entry = RangeBranchEntry {
            lower_fence: K::MIN,
            child_pgno: 0,
            subtree_record_count: 0,
            first_from: K::MIN,
            last_from: K::MIN,
            last_to: K::MIN,
        };
        Self {
            current: [entry; MAX_BRANCH_ENTRIES],
            current_len: 0,
            pending: [entry; MAX_BRANCH_ENTRIES],
            pending_len: 0,
            emitted_count: 0,
        }
    }

    fn reset(&mut self) {
        self.current_len = 0;
        self.pending_len = 0;
        self.emitted_count = 0;
    }
}

/// Reusable fixed workspace for one ordered range-tree build.
///
/// Construction is the only point at which callers need to reserve its fixed
/// memory. Builds themselves perform no heap allocation and retain no input
/// proportional state.
#[derive(Debug)]
pub(crate) struct RangeTreeBuildWorkspace<K: IpKey> {
    leaf: [RangeRecord<K>; MAX_LEAF_RECORDS],
    leaf_len: usize,
    levels: [RangeTreeBuildLevel<K>; COMPACT_BRANCH_LEVELS],
    page: [u8; PAGE_SIZE],
}

impl<K: IpKey> RangeTreeBuildWorkspace<K> {
    pub(crate) fn new() -> Self {
        let record = RangeRecord {
            from: K::MIN,
            to: K::MIN,
            value: 0,
        };
        Self {
            leaf: [record; MAX_LEAF_RECORDS],
            leaf_len: 0,
            levels: [RangeTreeBuildLevel::empty(); COMPACT_BRANCH_LEVELS],
            page: [0; PAGE_SIZE],
        }
    }

    pub(crate) fn begin(
        &mut self,
        born_txn: u64,
        value_kind: ValueKind,
        page_count: u64,
    ) -> Result<RangeTreeBuilder<'_, K>, RangeTreeBuildStartError> {
        if born_txn == 0 {
            return Err(RangeTreeBuildStartError::BornTransactionZero);
        }
        if !(2..=MAX_PAGE_COUNT).contains(&page_count) {
            return Err(RangeTreeBuildStartError::PageCount { page_count });
        }
        self.leaf_len = 0;
        for level in &mut self.levels {
            level.reset();
        }
        Ok(RangeTreeBuilder {
            workspace: self,
            born_txn,
            value_kind,
            page_count,
            record_count: 0,
            leaf_count: 0,
            last_record: None,
            finished: false,
            failed: false,
        })
    }
}

/// One ordered, canonical range stream being packed into private v4 pages.
#[derive(Debug)]
pub(crate) struct RangeTreeBuilder<'workspace, K: IpKey> {
    workspace: &'workspace mut RangeTreeBuildWorkspace<K>,
    born_txn: u64,
    value_kind: ValueKind,
    page_count: u64,
    record_count: u64,
    leaf_count: u64,
    last_record: Option<RangeRecord<K>>,
    finished: bool,
    failed: bool,
}

impl<K: IpKey> RangeTreeBuilder<'_, K> {
    /// Adds one record to the ordered canonical stream.
    ///
    /// A rejected input or a sink failure poisons this builder because a
    /// surrounding transaction must discard the entire draft on failure.
    pub(crate) fn push<S: RangeTreePageSink>(
        &mut self,
        sink: &mut S,
        record: RangeRecord<K>,
    ) -> Result<(), RangeTreeBuildError<S::Error>> {
        if self.finished {
            return Err(RangeTreeBuildError::Finished);
        }
        if self.failed {
            return Err(RangeTreeBuildError::Failed);
        }
        let result = self.push_inner(sink, record);
        if result.is_err() {
            self.failed = true;
        }
        result
    }

    /// Seals the stream and returns the range-root summary for the next meta
    /// page. Empty input has root page zero and writes no range pages.
    pub(crate) fn finish<S: RangeTreePageSink>(
        &mut self,
        sink: &mut S,
    ) -> Result<RangeTreeBuildResult, RangeTreeBuildError<S::Error>> {
        if self.finished {
            return Err(RangeTreeBuildError::Finished);
        }
        if self.failed {
            return Err(RangeTreeBuildError::Failed);
        }
        let result = self.finish_inner(sink);
        if result.is_err() {
            self.failed = true;
        } else {
            self.finished = true;
        }
        result
    }

    fn push_inner<S: RangeTreePageSink>(
        &mut self,
        sink: &mut S,
        record: RangeRecord<K>,
    ) -> Result<(), RangeTreeBuildError<S::Error>> {
        self.validate_record(record)?;
        self.record_count = self
            .record_count
            .checked_add(1)
            .ok_or(RangeTreeBuildError::RecordCountOverflow)?;
        self.workspace.leaf[self.workspace.leaf_len] = record;
        self.workspace.leaf_len += 1;
        self.last_record = Some(record);
        if self.workspace.leaf_len == leaf_capacity::<K>() {
            self.flush_leaf(sink)?;
        }
        Ok(())
    }

    fn validate_record<E>(&self, record: RangeRecord<K>) -> Result<(), RangeTreeBuildError<E>> {
        if record.from > record.to {
            return Err(RangeTreeBuildError::RangeReversed);
        }
        if self.value_kind == ValueKind::Membership && record.value == 0 {
            return Err(RangeTreeBuildError::MembershipValueZero);
        }
        let Some(previous) = self.last_record else {
            return Ok(());
        };
        if previous.to >= record.from {
            return Err(RangeTreeBuildError::RangeOverlap);
        }
        if previous.value == record.value && previous.to.checked_inc() == Some(record.from) {
            return Err(RangeTreeBuildError::AdjacentEqualValue);
        }
        Ok(())
    }

    fn flush_leaf<S: RangeTreePageSink>(
        &mut self,
        sink: &mut S,
    ) -> Result<(), RangeTreeBuildError<S::Error>> {
        let len = self.workspace.leaf_len;
        debug_assert!(len != 0);
        let (first_from, last_from, last_to) = {
            let records = &self.workspace.leaf[..len];
            encode_leaf(
                &mut self.workspace.page,
                self.born_txn,
                self.value_kind,
                records,
            )
            .map_err(RangeTreeBuildError::Page)?;
            (records[0].from, records[len - 1].from, records[len - 1].to)
        };
        let pgno = self.write_encoded_page(sink)?;
        let lower_fence = if self.leaf_count == 0 {
            K::MIN
        } else {
            first_from
        };
        self.leaf_count += 1;
        self.workspace.leaf_len = 0;
        self.push_node(
            sink,
            1,
            RangeTreeNode {
                pgno,
                level: 0,
                record_count: len as u64,
                lower_fence,
                first_from,
                last_from,
                last_to,
            },
        )
    }

    fn push_node<S: RangeTreePageSink>(
        &mut self,
        sink: &mut S,
        branch_level: usize,
        node: RangeTreeNode<K>,
    ) -> Result<(), RangeTreeBuildError<S::Error>> {
        if branch_level == 0 || branch_level > COMPACT_BRANCH_LEVELS {
            return Err(RangeTreeBuildError::TreeTooDeep);
        }
        debug_assert_eq!(node.level as usize + 1, branch_level);
        let capacity = branch_capacity::<K>();
        let state = &mut self.workspace.levels[branch_level - 1];
        debug_assert!(state.current_len < capacity);
        state.current[state.current_len] = node.branch_entry();
        state.current_len += 1;

        if state.current_len == capacity {
            if state.pending_len != 0 {
                self.flush_pending(sink, branch_level)?;
            }
            let state = &mut self.workspace.levels[branch_level - 1];
            state.pending[..capacity].copy_from_slice(&state.current[..capacity]);
            state.pending_len = capacity;
            state.current_len = 0;
        } else if state.pending_len != 0 && state.current_len == 2 {
            self.flush_pending(sink, branch_level)?;
        }
        Ok(())
    }

    fn flush_pending<S: RangeTreePageSink>(
        &mut self,
        sink: &mut S,
        branch_level: usize,
    ) -> Result<(), RangeTreeBuildError<S::Error>> {
        let (len, upper_fence) = {
            let state = &self.workspace.levels[branch_level - 1];
            debug_assert!(state.pending_len != 0);
            debug_assert!(state.current_len >= 2);
            (state.pending_len, state.current[0].lower_fence)
        };
        let node = self.emit_branch(sink, branch_level as u16, true, len, upper_fence)?;
        let state = &mut self.workspace.levels[branch_level - 1];
        state.pending_len = 0;
        state.emitted_count += 1;
        self.push_node(sink, branch_level + 1, node)
    }

    fn emit_branch<S: RangeTreePageSink>(
        &mut self,
        sink: &mut S,
        level: u16,
        pending: bool,
        len: usize,
        upper_fence: K,
    ) -> Result<RangeTreeNode<K>, RangeTreeBuildError<S::Error>> {
        let (lower_fence, record_count, first_from, last_from, last_to) = {
            let entries = if pending {
                &self.workspace.levels[level as usize - 1].pending[..len]
            } else {
                &self.workspace.levels[level as usize - 1].current[..len]
            };
            let first = entries[0];
            let last = entries[len - 1];
            let mut record_count = 0u64;
            for entry in entries {
                record_count = record_count
                    .checked_add(entry.subtree_record_count)
                    .ok_or(RangeTreeBuildError::RecordCountOverflow)?;
            }
            encode_branch(
                &mut self.workspace.page,
                self.born_txn,
                level,
                self.page_count,
                first.lower_fence,
                Some(upper_fence),
                entries,
            )
            .map_err(RangeTreeBuildError::Page)?;
            (
                first.lower_fence,
                record_count,
                first.first_from,
                last.last_from,
                last.last_to,
            )
        };
        let pgno = self.write_encoded_page(sink)?;
        Ok(RangeTreeNode {
            pgno,
            level,
            record_count,
            lower_fence,
            first_from,
            last_from,
            last_to,
        })
    }

    fn emit_rightmost_branch<S: RangeTreePageSink>(
        &mut self,
        sink: &mut S,
        level: u16,
        pending: bool,
        len: usize,
    ) -> Result<RangeTreeNode<K>, RangeTreeBuildError<S::Error>> {
        let (lower_fence, record_count, first_from, last_from, last_to) = {
            let entries = if pending {
                &self.workspace.levels[level as usize - 1].pending[..len]
            } else {
                &self.workspace.levels[level as usize - 1].current[..len]
            };
            let first = entries[0];
            let last = entries[len - 1];
            let mut record_count = 0u64;
            for entry in entries {
                record_count = record_count
                    .checked_add(entry.subtree_record_count)
                    .ok_or(RangeTreeBuildError::RecordCountOverflow)?;
            }
            encode_branch(
                &mut self.workspace.page,
                self.born_txn,
                level,
                self.page_count,
                first.lower_fence,
                None,
                entries,
            )
            .map_err(RangeTreeBuildError::Page)?;
            (
                first.lower_fence,
                record_count,
                first.first_from,
                last.last_from,
                last.last_to,
            )
        };
        let pgno = self.write_encoded_page(sink)?;
        Ok(RangeTreeNode {
            pgno,
            level,
            record_count,
            lower_fence,
            first_from,
            last_from,
            last_to,
        })
    }

    fn write_encoded_page<S: RangeTreePageSink>(
        &mut self,
        sink: &mut S,
    ) -> Result<u32, RangeTreeBuildError<S::Error>> {
        let pgno = sink
            .write_range_page(&self.workspace.page)
            .map_err(RangeTreeBuildError::Sink)?;
        if pgno < 2 || u64::from(pgno) >= self.page_count {
            return Err(RangeTreeBuildError::SinkPageOutOfBounds(pgno));
        }
        Ok(pgno)
    }

    fn finish_inner<S: RangeTreePageSink>(
        &mut self,
        sink: &mut S,
    ) -> Result<RangeTreeBuildResult, RangeTreeBuildError<S::Error>> {
        if self.workspace.leaf_len != 0 {
            self.flush_leaf(sink)?;
        }
        if self.record_count == 0 {
            return Ok(RangeTreeBuildResult {
                root_pgno: 0,
                root_level: 0,
                record_count: 0,
            });
        }

        for branch_level in 1..=COMPACT_BRANCH_LEVELS {
            if let Some(root) = self.finish_level(sink, branch_level)? {
                debug_assert_eq!(root.record_count, self.record_count);
                return Ok(RangeTreeBuildResult {
                    root_pgno: root.pgno,
                    root_level: root.level,
                    record_count: self.record_count,
                });
            }
        }
        Err(RangeTreeBuildError::TreeTooDeep)
    }

    /// Finishes one branch level. A returned node is the sole root; otherwise
    /// every final node was forwarded to the following level.
    fn finish_level<S: RangeTreePageSink>(
        &mut self,
        sink: &mut S,
        branch_level: usize,
    ) -> Result<Option<RangeTreeNode<K>>, RangeTreeBuildError<S::Error>> {
        let (pending_len, current_len, emitted_count) = {
            let state = &self.workspace.levels[branch_level - 1];
            (state.pending_len, state.current_len, state.emitted_count)
        };
        if pending_len == 0 && current_len == 0 {
            return Ok(None);
        }
        if pending_len == 0 && current_len == 1 {
            if emitted_count != 0 {
                return Err(RangeTreeBuildError::TreeTooDeep);
            }
            let entry = self.workspace.levels[branch_level - 1].current[0];
            return Ok(Some(RangeTreeNode {
                pgno: entry.child_pgno,
                level: branch_level as u16 - 1,
                record_count: entry.subtree_record_count,
                lower_fence: entry.lower_fence,
                first_from: entry.first_from,
                last_from: entry.last_from,
                last_to: entry.last_to,
            }));
        }

        if pending_len != 0 && current_len == 1 {
            let state = &mut self.workspace.levels[branch_level - 1];
            debug_assert!(pending_len >= 2);
            state.current[1] = state.current[0];
            state.current[0] = state.pending[pending_len - 1];
            state.current_len = 2;
            state.pending_len -= 1;
            let left = self.emit_branch(
                sink,
                branch_level as u16,
                true,
                pending_len - 1,
                self.workspace.levels[branch_level - 1].current[0].lower_fence,
            )?;
            let right = self.emit_rightmost_branch(sink, branch_level as u16, false, 2)?;
            self.push_node(sink, branch_level + 1, left)?;
            self.push_node(sink, branch_level + 1, right)?;
            return Ok(None);
        }

        if pending_len != 0 && current_len >= 2 {
            let upper = self.workspace.levels[branch_level - 1].current[0].lower_fence;
            let left = self.emit_branch(sink, branch_level as u16, true, pending_len, upper)?;
            let right =
                self.emit_rightmost_branch(sink, branch_level as u16, false, current_len)?;
            self.push_node(sink, branch_level + 1, left)?;
            self.push_node(sink, branch_level + 1, right)?;
            return Ok(None);
        }

        let node = if pending_len != 0 {
            self.emit_rightmost_branch(sink, branch_level as u16, true, pending_len)?
        } else {
            self.emit_rightmost_branch(sink, branch_level as u16, false, current_len)?
        };
        if emitted_count == 0 {
            Ok(Some(node))
        } else {
            self.push_node(sink, branch_level + 1, node)?;
            Ok(None)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::contract::AddressFamily;
    use crate::key::{Ipv4Key, Ipv6Key};
    use crate::page::{PageHeader, PageType};
    use crate::range_reader::RangeTree;
    use crate::test_alloc::count_thread_allocations;
    use std::vec;
    use std::vec::Vec;

    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    enum TestSinkError {
        Rejected,
    }

    #[derive(Debug)]
    struct TestSink {
        next_pgno: u32,
        pages: Vec<(u32, [u8; PAGE_SIZE])>,
        fail: bool,
        forced_pgno: Option<u32>,
    }

    impl TestSink {
        fn new() -> Self {
            Self {
                next_pgno: 2,
                pages: Vec::new(),
                fail: false,
                forced_pgno: None,
            }
        }
    }

    impl RangeTreePageSink for TestSink {
        type Error = TestSinkError;

        fn write_range_page(&mut self, page: &[u8; PAGE_SIZE]) -> Result<u32, Self::Error> {
            if self.fail {
                return Err(TestSinkError::Rejected);
            }
            let pgno = self.forced_pgno.unwrap_or(self.next_pgno);
            self.next_pgno = self.next_pgno.checked_add(1).unwrap();
            self.pages.push((pgno, *page));
            Ok(pgno)
        }
    }

    #[derive(Debug)]
    struct FixedSink<const N: usize> {
        next_pgno: u32,
        pages: [[u8; PAGE_SIZE]; N],
        len: usize,
    }

    impl<const N: usize> FixedSink<N> {
        fn new() -> Self {
            Self {
                next_pgno: 2,
                pages: [[0; PAGE_SIZE]; N],
                len: 0,
            }
        }

        fn reset(&mut self) {
            self.next_pgno = 2;
            self.len = 0;
        }
    }

    impl<const N: usize> RangeTreePageSink for FixedSink<N> {
        type Error = ();

        fn write_range_page(&mut self, page: &[u8; PAGE_SIZE]) -> Result<u32, Self::Error> {
            if self.len == N {
                return Err(());
            }
            self.pages[self.len] = *page;
            self.len += 1;
            let pgno = self.next_pgno;
            self.next_pgno += 1;
            Ok(pgno)
        }
    }

    fn tree_image<K: IpKey>(
        sink: &TestSink,
        result: RangeTreeBuildResult,
        page_count: usize,
    ) -> Vec<u8> {
        let mut meta = empty_direct_meta(1);
        meta.address_family = K::FAMILY;
        meta.value_kind = ValueKind::Direct;
        meta.page_count = page_count as u64;
        meta.range_root = result.root_pgno;
        meta.range_record_count = result.record_count;
        let mut bytes = vec![0; page_count * PAGE_SIZE];
        meta.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into((&mut bytes[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
        for &(pgno, ref page) in &sink.pages {
            let start = pgno as usize * PAGE_SIZE;
            bytes[start..start + PAGE_SIZE].copy_from_slice(page);
        }
        bytes
    }

    fn record_v4(value: u32) -> RangeRecord<Ipv4Key> {
        let address = value * 2;
        RangeRecord {
            from: Ipv4Key(address),
            to: Ipv4Key(address),
            value: 1,
        }
    }

    #[test]
    fn empty_input_writes_no_pages() {
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        let result = workspace
            .begin(1, ValueKind::Direct, 3)
            .unwrap()
            .finish(&mut sink)
            .unwrap();
        assert_eq!(
            result,
            RangeTreeBuildResult {
                root_pgno: 0,
                root_level: 0,
                record_count: 0,
            }
        );
        assert!(sink.pages.is_empty());
    }

    #[test]
    fn one_leaf_is_the_root_and_reopens() {
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        let mut builder = workspace.begin(1, ValueKind::Direct, 8).unwrap();
        builder.push(&mut sink, record_v4(5)).unwrap();
        builder.push(&mut sink, record_v4(10)).unwrap();
        let result = builder.finish(&mut sink).unwrap();
        assert_eq!(result.root_pgno, 2);
        assert_eq!(result.root_level, 0);
        assert_eq!(result.record_count, 2);
        assert_eq!(sink.pages.len(), 1);

        let bytes = tree_image::<Ipv4Key>(&sink, result, 8);
        let tree = RangeTree::<Ipv4Key>::open_immutable(&bytes).unwrap();
        assert_eq!(tree.lookup(Ipv4Key(10)).unwrap(), Some(record_v4(5)));
        assert_eq!(tree.lookup(Ipv4Key(20)).unwrap(), Some(record_v4(10)));
        assert_eq!(tree.lookup(Ipv4Key(11)).unwrap(), None);
    }

    #[test]
    fn two_leaves_form_a_root_branch_and_reopen() {
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        let mut builder = workspace.begin(1, ValueKind::Direct, 8).unwrap();
        for value in 0..(leaf_capacity::<Ipv4Key>() + 1) as u32 {
            builder.push(&mut sink, record_v4(value)).unwrap();
        }
        let result = builder.finish(&mut sink).unwrap();
        assert_eq!(result.root_pgno, 4);
        assert_eq!(result.root_level, 1);
        assert_eq!(result.record_count, (leaf_capacity::<Ipv4Key>() + 1) as u64);
        assert_eq!(sink.pages.len(), 3);

        let bytes = tree_image::<Ipv4Key>(&sink, result, 8);
        let tree = RangeTree::<Ipv4Key>::open_immutable(&bytes).unwrap();
        assert_eq!(tree.lookup(Ipv4Key(0)).unwrap(), Some(record_v4(0)));
        assert_eq!(
            tree.lookup(Ipv4Key((leaf_capacity::<Ipv4Key>() * 2) as u32))
                .unwrap(),
            Some(record_v4(leaf_capacity::<Ipv4Key>() as u32))
        );
    }

    #[test]
    fn ipv6_leaf_split_uses_the_same_bounded_packer() {
        let mut workspace = RangeTreeBuildWorkspace::<Ipv6Key>::new();
        let mut sink = TestSink::new();
        let mut builder = workspace.begin(1, ValueKind::Direct, 8).unwrap();
        for value in 0..(leaf_capacity::<Ipv6Key>() + 1) as u64 {
            let address = value * 2;
            builder
                .push(
                    &mut sink,
                    RangeRecord {
                        from: Ipv6Key { hi: 0, lo: address },
                        to: Ipv6Key { hi: 0, lo: address },
                        value: 7,
                    },
                )
                .unwrap();
        }
        let result = builder.finish(&mut sink).unwrap();
        assert_eq!(result.root_level, 1);

        let bytes = tree_image::<Ipv6Key>(&sink, result, 8);
        let tree = RangeTree::<Ipv6Key>::open_immutable(&bytes).unwrap();
        assert_eq!(
            tree.lookup(Ipv6Key { hi: 0, lo: 2 })
                .unwrap()
                .unwrap()
                .value,
            7
        );
    }

    #[test]
    fn final_singleton_branch_is_rebalanced_before_becoming_nonroot() {
        let record_count = branch_capacity::<Ipv4Key>() * leaf_capacity::<Ipv4Key>() + 1;
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        let mut builder = workspace.begin(1, ValueKind::Direct, 256).unwrap();
        for value in 0..record_count as u32 {
            builder.push(&mut sink, record_v4(value)).unwrap();
        }
        let result = builder.finish(&mut sink).unwrap();
        assert_eq!(result.root_level, 2);
        assert_eq!(result.record_count, record_count as u64);

        let mut level_one_counts = Vec::new();
        let mut level_two_counts = Vec::new();
        for (_, page) in &sink.pages {
            let header = PageHeader::decode(page, 1).unwrap();
            if header.page_type != PageType::RangeBranch {
                continue;
            }
            assert!(header.item_count >= 2, "non-empty branch has one child");
            match header.level {
                1 => level_one_counts.push(header.item_count),
                2 => level_two_counts.push(header.item_count),
                level => panic!("unexpected branch level {level}"),
            }
        }
        level_one_counts.sort_unstable();
        assert_eq!(
            level_one_counts,
            vec![2, (branch_capacity::<Ipv4Key>() - 1) as u16]
        );
        assert_eq!(level_two_counts, vec![2]);

        let bytes = tree_image::<Ipv4Key>(&sink, result, 256);
        let tree = RangeTree::<Ipv4Key>::open_immutable(&bytes).unwrap();
        assert_eq!(tree.lookup(Ipv4Key(0)).unwrap(), Some(record_v4(0)));
        let last = record_count as u32 - 1;
        assert_eq!(
            tree.lookup(Ipv4Key(last * 2)).unwrap(),
            Some(record_v4(last))
        );
    }

    #[test]
    fn canonicality_is_checked_across_leaf_boundaries_and_poisoned_after_failure() {
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        let mut builder = workspace.begin(1, ValueKind::Direct, 8).unwrap();
        for value in 0..leaf_capacity::<Ipv4Key>() as u32 {
            builder.push(&mut sink, record_v4(value)).unwrap();
        }
        assert_eq!(
            builder.push(&mut sink, record_v4(0)),
            Err(RangeTreeBuildError::RangeOverlap)
        );
        assert_eq!(builder.finish(&mut sink), Err(RangeTreeBuildError::Failed));
    }

    #[test]
    fn adjacent_equal_records_and_zero_membership_are_rejected() {
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        let mut builder = workspace.begin(1, ValueKind::Direct, 8).unwrap();
        builder
            .push(
                &mut sink,
                RangeRecord {
                    from: Ipv4Key(10),
                    to: Ipv4Key(10),
                    value: 4,
                },
            )
            .unwrap();
        assert_eq!(
            builder.push(
                &mut sink,
                RangeRecord {
                    from: Ipv4Key(11),
                    to: Ipv4Key(11),
                    value: 4,
                },
            ),
            Err(RangeTreeBuildError::AdjacentEqualValue)
        );

        let mut membership_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut membership_builder = membership_workspace
            .begin(1, ValueKind::Membership, 8)
            .unwrap();
        assert_eq!(
            membership_builder.push(
                &mut sink,
                RangeRecord {
                    from: Ipv4Key(10),
                    to: Ipv4Key(10),
                    value: 0,
                },
            ),
            Err(RangeTreeBuildError::MembershipValueZero)
        );
    }

    #[test]
    fn sink_failures_and_invalid_returned_pages_abort_the_build() {
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        sink.fail = true;
        let mut builder = workspace.begin(1, ValueKind::Direct, 8).unwrap();
        builder.push(&mut sink, record_v4(1)).unwrap();
        assert_eq!(
            builder.finish(&mut sink),
            Err(RangeTreeBuildError::Sink(TestSinkError::Rejected))
        );
        assert_eq!(
            builder.push(&mut sink, record_v4(2)),
            Err(RangeTreeBuildError::Failed)
        );

        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut bad_sink = TestSink::new();
        bad_sink.forced_pgno = Some(8);
        let mut builder = workspace.begin(1, ValueKind::Direct, 8).unwrap();
        builder.push(&mut bad_sink, record_v4(1)).unwrap();
        assert_eq!(
            builder.finish(&mut bad_sink),
            Err(RangeTreeBuildError::SinkPageOutOfBounds(8))
        );
    }

    #[test]
    fn hot_path_performs_no_heap_allocations_after_workspace_construction() {
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = FixedSink::<4>::new();
        let (result, allocations) = count_thread_allocations(|| {
            sink.reset();
            let mut builder = workspace.begin(1, ValueKind::Direct, 4).unwrap();
            builder.push(&mut sink, record_v4(1)).unwrap();
            builder.push(&mut sink, record_v4(3)).unwrap();
            builder.finish(&mut sink).unwrap()
        });
        assert_eq!(allocations, 0);
        assert_eq!(result.root_pgno, 2);
        assert_eq!(sink.len, 1);
    }

    #[test]
    fn start_rejects_impossible_transaction_or_target_page_count() {
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        assert_eq!(
            workspace.begin(0, ValueKind::Direct, 2).unwrap_err(),
            RangeTreeBuildStartError::BornTransactionZero
        );
        assert_eq!(
            workspace.begin(1, ValueKind::Direct, 1).unwrap_err(),
            RangeTreeBuildStartError::PageCount { page_count: 1 }
        );
        assert_eq!(
            workspace
                .begin(1, ValueKind::Direct, MAX_PAGE_COUNT + 1)
                .unwrap_err(),
            RangeTreeBuildStartError::PageCount {
                page_count: MAX_PAGE_COUNT + 1
            }
        );
        assert_eq!(Ipv4Key::FAMILY, AddressFamily::Ipv4);
    }
}
