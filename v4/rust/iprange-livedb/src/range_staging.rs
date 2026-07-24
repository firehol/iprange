//! Bounded logical range pages for pre-allocation transaction preparation.
//!
//! Commit preparation cannot choose final file page numbers: the allocator
//! selects those only under the live finalization lock.  This module keeps the
//! ordered range builder's output in fixed caller-owned slots under temporary
//! logical IDs, then materializes it once allocator-owned physical assignments
//! are available.

use crate::contract::{u32_le, ValueKind, MAX_PAGE_COUNT, PAGE_SIZE};
use crate::key::IpKey;
use crate::page::{self, write_crc32c, PageHeader, PageType, PAGE_HEADER_SIZE};
use crate::private_page_pool::{
    PrivatePageAuthorization, PrivatePageCoordinatorTerminalPage, PrivatePageOwner,
};
use crate::range_builder::{RangeTreeBuildResult, RangeTreePageSink};
use crate::range_page::{branch_entry_size, RangeBranch, RangeLeaf};

/// One fixed logical page slot. It is private operation workspace, never a
/// physical v4 page identity.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RangeTreeStagingPage {
    bytes: [u8; PAGE_SIZE],
}

impl RangeTreeStagingPage {
    pub(crate) const fn empty() -> Self {
        Self {
            bytes: [0; PAGE_SIZE],
        }
    }

    const fn is_empty(self) -> bool {
        let mut index = 0;
        while index < PAGE_SIZE {
            if self.bytes[index] != 0 {
                return false;
            }
            index += 1;
        }
        true
    }
}

/// One allocator-selected physical destination for the matching logical page
/// slot. Assignments must be strictly increasing, matching terminal-bind
/// order, and are intentionally separate from terminal-page bytes so every
/// failure can be checked before output mutation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RangeTreePhysicalAssignment {
    pub(crate) pgno: u32,
    pub(crate) authorization: PrivatePageAuthorization,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RangeTreeStagedResult {
    logical_root: u32,
    pub(crate) root_level: u16,
    pub(crate) record_count: u64,
    page_count: usize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RangeTreeMaterializedResult {
    pub(crate) root_pgno: u32,
    pub(crate) root_level: u16,
    pub(crate) record_count: u64,
    pub(crate) page_count: usize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RangeTreeStagingError {
    BornTransactionZero,
    LogicalPageCapacity { capacity: usize },
    WorkspaceDirty,
    Finished,
    CapacityExhausted { required: usize, actual: usize },
    InvalidEncodedPage,
    InvalidLogicalResult,
    StagedResultMismatch,
    FinalPageCount { page_count: u64 },
    AssignmentCount { required: usize, actual: usize },
    TerminalOutputCount { required: usize, actual: usize },
    TerminalOutputDirty,
    PhysicalPageOutOfBounds(u32),
    PhysicalPageOrder { previous: u32, current: u32 },
    InvalidStagedPage { index: usize },
    LogicalChildOutOfBounds { page: usize, child: u32 },
    LogicalChildOrder { page: usize, child: u32 },
}

/// Fixed-capacity logical sink for one ordered range-tree build.
#[derive(Debug)]
pub(crate) struct RangeTreeStaging<'storage, K: IpKey> {
    pages: &'storage mut [RangeTreeStagingPage],
    born_txn: u64,
    value_kind: ValueKind,
    logical_page_limit: u64,
    len: usize,
    finished: bool,
    _key: core::marker::PhantomData<K>,
}

impl<'storage, K: IpKey> RangeTreeStaging<'storage, K> {
    pub(crate) fn new(
        pages: &'storage mut [RangeTreeStagingPage],
        born_txn: u64,
        value_kind: ValueKind,
    ) -> Result<Self, RangeTreeStagingError> {
        if born_txn == 0 {
            return Err(RangeTreeStagingError::BornTransactionZero);
        }
        let capacity =
            u64::try_from(pages.len()).map_err(|_| RangeTreeStagingError::LogicalPageCapacity {
                capacity: pages.len(),
            })?;
        let Some(logical_page_limit) = capacity.checked_add(2) else {
            return Err(RangeTreeStagingError::LogicalPageCapacity {
                capacity: pages.len(),
            });
        };
        if logical_page_limit > MAX_PAGE_COUNT {
            return Err(RangeTreeStagingError::LogicalPageCapacity {
                capacity: pages.len(),
            });
        }
        if pages.iter().any(|page| !page.is_empty()) {
            return Err(RangeTreeStagingError::WorkspaceDirty);
        }
        Ok(Self {
            pages,
            born_txn,
            value_kind,
            logical_page_limit,
            len: 0,
            finished: false,
            _key: core::marker::PhantomData,
        })
    }

    /// The temporary page-count bound supplied to the existing builder. No
    /// value in this range is a final file page number.
    pub(crate) const fn logical_page_limit(&self) -> u64 {
        self.logical_page_limit
    }

    pub(crate) const fn len(&self) -> usize {
        self.len
    }

    pub(crate) fn finish(
        &mut self,
        result: RangeTreeBuildResult,
    ) -> Result<RangeTreeStagedResult, RangeTreeStagingError> {
        if self.finished {
            return Err(RangeTreeStagingError::Finished);
        }
        if (self.len == 0 && result.root_pgno != 0)
            || (self.len != 0
                && (result.root_pgno < 2
                    || usize::try_from(result.root_pgno - 2)
                        .map_or(true, |index| index >= self.len)))
        {
            return Err(RangeTreeStagingError::InvalidLogicalResult);
        }
        self.finished = true;
        Ok(RangeTreeStagedResult {
            logical_root: result.root_pgno,
            root_level: result.root_level,
            record_count: result.record_count,
            page_count: self.len,
        })
    }

    fn check_staged_result(
        &self,
        result: RangeTreeStagedResult,
    ) -> Result<(), RangeTreeStagingError> {
        if !self.finished
            || result.page_count != self.len
            || (self.len == 0 && result.logical_root != 0)
            || (self.len != 0
                && (result.logical_root < 2
                    || usize::try_from(result.logical_root - 2)
                        .map_or(true, |index| index >= self.len)))
        {
            return Err(RangeTreeStagingError::StagedResultMismatch);
        }
        Ok(())
    }

    fn validate_leaf(&self, page: &[u8; PAGE_SIZE]) -> Result<(), RangeTreeStagingError> {
        // This is operation-private output from the ordered builder, not an
        // input-file validation pass. Check only the geometry needed to hand
        // the page to the terminal coordinator.
        RangeLeaf::<K>::open(page, self.born_txn, K::FAMILY, self.value_kind)
            .map(|_| ())
            .map_err(|_| RangeTreeStagingError::InvalidEncodedPage)
    }

    fn validate_branch(
        &self,
        page_index: usize,
        page: &[u8; PAGE_SIZE],
    ) -> Result<(), RangeTreeStagingError> {
        let branch =
            RangeBranch::<K>::open(page, self.born_txn, K::FAMILY, self.logical_page_limit)
                .map_err(|_| RangeTreeStagingError::InvalidStagedPage { index: page_index })?;
        for index in 0..branch.len() {
            let offset = usize::from(PAGE_HEADER_SIZE) + index * branch_entry_size::<K>();
            let child_offset = offset + if K::WIDTH == 4 { 4 } else { 16 };
            let child = u32_le(page, child_offset);
            let child_index = usize::try_from(child - 2).map_err(|_| {
                RangeTreeStagingError::LogicalChildOutOfBounds {
                    page: page_index,
                    child,
                }
            })?;
            if child_index >= self.len {
                return Err(RangeTreeStagingError::LogicalChildOutOfBounds {
                    page: page_index,
                    child,
                });
            }
            if child_index >= page_index {
                return Err(RangeTreeStagingError::LogicalChildOrder {
                    page: page_index,
                    child,
                });
            }
        }
        Ok(())
    }

    fn validate_staging_page(&self, index: usize) -> Result<PageHeader, RangeTreeStagingError> {
        let page = self
            .pages
            .get(index)
            .ok_or(RangeTreeStagingError::InvalidStagedPage { index })?;
        let header = PageHeader::decode(&page.bytes, self.born_txn)
            .map_err(|_| RangeTreeStagingError::InvalidStagedPage { index })?;
        if !page::verify_crc32c(&page.bytes) || header.aux != K::FAMILY as u32 {
            return Err(RangeTreeStagingError::InvalidStagedPage { index });
        }
        match header.page_type {
            PageType::RangeLeaf => self
                .validate_leaf(&page.bytes)
                .map_err(|_| RangeTreeStagingError::InvalidStagedPage { index })?,
            PageType::RangeBranch => self.validate_branch(index, &page.bytes)?,
            _ => return Err(RangeTreeStagingError::InvalidStagedPage { index }),
        }
        Ok(header)
    }

    fn validate_materialization(
        &self,
        result: RangeTreeStagedResult,
        final_page_count: u64,
        assignments: &[RangeTreePhysicalAssignment],
        output: &[PrivatePageCoordinatorTerminalPage],
    ) -> Result<(), RangeTreeStagingError> {
        self.check_staged_result(result)?;
        if !(2..=MAX_PAGE_COUNT).contains(&final_page_count) {
            return Err(RangeTreeStagingError::FinalPageCount {
                page_count: final_page_count,
            });
        }
        if assignments.len() != self.len {
            return Err(RangeTreeStagingError::AssignmentCount {
                required: self.len,
                actual: assignments.len(),
            });
        }
        if output.len() != self.len {
            return Err(RangeTreeStagingError::TerminalOutputCount {
                required: self.len,
                actual: output.len(),
            });
        }
        if output
            .iter()
            .any(|page| *page != PrivatePageCoordinatorTerminalPage::empty())
        {
            return Err(RangeTreeStagingError::TerminalOutputDirty);
        }
        let mut previous = None;
        for (index, assignment) in assignments.iter().copied().enumerate() {
            if assignment.pgno < 2 || u64::from(assignment.pgno) >= final_page_count {
                return Err(RangeTreeStagingError::PhysicalPageOutOfBounds(
                    assignment.pgno,
                ));
            }
            if let Some(last) = previous {
                if assignment.pgno <= last {
                    return Err(RangeTreeStagingError::PhysicalPageOrder {
                        previous: last,
                        current: assignment.pgno,
                    });
                }
            }
            previous = Some(assignment.pgno);
            self.validate_staging_page(index)?;
        }
        Ok(())
    }

    fn patch_branch_children(
        bytes: &mut [u8; PAGE_SIZE],
        count: usize,
        assignments: &[RangeTreePhysicalAssignment],
    ) {
        for index in 0..count {
            let offset = usize::from(PAGE_HEADER_SIZE) + index * branch_entry_size::<K>();
            let child_offset = offset + if K::WIDTH == 4 { 4 } else { 16 };
            let logical = u32_le(bytes, child_offset);
            let child = assignments[(logical - 2) as usize].pgno;
            bytes[child_offset..child_offset + 4].copy_from_slice(&child.to_le_bytes());
        }
    }

    /// Converts one fully staged logical tree to allocator-authorized terminal
    /// pages. Every fallible check runs first; `output` remains all-empty on
    /// failure and can be reused by the caller's whole-draft abort path.
    pub(crate) fn materialize(
        &self,
        result: RangeTreeStagedResult,
        final_page_count: u64,
        assignments: &[RangeTreePhysicalAssignment],
        output: &mut [PrivatePageCoordinatorTerminalPage],
    ) -> Result<RangeTreeMaterializedResult, RangeTreeStagingError> {
        self.validate_materialization(result, final_page_count, assignments, output)?;
        for (index, destination) in output.iter_mut().enumerate() {
            let source = self.pages[index];
            let mut bytes = source.bytes;
            // `validate_materialization` has already decoded and checked this
            // immutable logical page. The wire discriminator is sufficient
            // here, which keeps the no-failure output pass free of a panic.
            if bytes[4] == PageType::RangeBranch as u8 {
                let item_count = usize::from(u16::from_le_bytes([bytes[16], bytes[17]]));
                Self::patch_branch_children(&mut bytes, item_count, assignments);
                write_crc32c(&mut bytes);
            }
            let mut terminal = PrivatePageCoordinatorTerminalPage::empty();
            terminal.pgno = assignments[index].pgno;
            terminal.authorization = assignments[index].authorization;
            terminal.owner = PrivatePageOwner::Range;
            terminal.owner_generation = self.born_txn;
            terminal.tag = K::FAMILY as u64;
            terminal.bytes = bytes;
            *destination = terminal;
        }
        let root_pgno = if result.logical_root == 0 {
            0
        } else {
            assignments[(result.logical_root - 2) as usize].pgno
        };
        Ok(RangeTreeMaterializedResult {
            root_pgno,
            root_level: result.root_level,
            record_count: result.record_count,
            page_count: self.len,
        })
    }
}

impl<K: IpKey> RangeTreePageSink for RangeTreeStaging<'_, K> {
    type Error = RangeTreeStagingError;

    fn write_range_page(&mut self, page: &[u8; PAGE_SIZE]) -> Result<u32, Self::Error> {
        if self.finished {
            return Err(RangeTreeStagingError::Finished);
        }
        if self.len == self.pages.len() {
            return Err(RangeTreeStagingError::CapacityExhausted {
                required: self.len.saturating_add(1),
                actual: self.pages.len(),
            });
        }
        let header = PageHeader::decode(page, self.born_txn)
            .map_err(|_| RangeTreeStagingError::InvalidEncodedPage)?;
        if !page::verify_crc32c(page)
            || header.aux != K::FAMILY as u32
            || !matches!(
                header.page_type,
                PageType::RangeLeaf | PageType::RangeBranch
            )
        {
            return Err(RangeTreeStagingError::InvalidEncodedPage);
        }
        let logical = u32::try_from(self.len)
            .ok()
            .and_then(|index| index.checked_add(2))
            .ok_or(RangeTreeStagingError::LogicalPageCapacity {
                capacity: self.pages.len(),
            })?;
        self.pages[self.len].bytes = *page;
        self.len += 1;
        Ok(logical)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::contract::{AddressFamily, ValueTag};
    use crate::key::{Ipv4Key, Ipv6Key};
    use crate::range_builder::RangeTreeBuildWorkspace;
    use crate::range_page::RangeRecord;
    use crate::range_reader::RangeTree;
    use crate::test_alloc::count_thread_allocations;
    use std::vec;

    fn assignment(pgno: u32) -> RangeTreePhysicalAssignment {
        RangeTreePhysicalAssignment {
            pgno,
            authorization: PrivatePageAuthorization::CommittedFree,
        }
    }

    fn record(value: u32) -> RangeRecord<Ipv4Key> {
        let address = value * 2;
        RangeRecord {
            from: Ipv4Key(address),
            to: Ipv4Key(address),
            value: 1,
        }
    }

    fn record_v6(value: u128) -> RangeRecord<Ipv6Key> {
        let address = value * 2;
        RangeRecord {
            from: Ipv6Key::from_u128(address),
            to: Ipv6Key::from_u128(address),
            value: 1,
        }
    }

    fn build_v4(
        staging: &mut RangeTreeStaging<'_, Ipv4Key>,
        workspace: &mut RangeTreeBuildWorkspace<Ipv4Key>,
        records: impl IntoIterator<Item = RangeRecord<Ipv4Key>>,
    ) -> RangeTreeStagedResult {
        let mut builder = workspace
            .begin(2, ValueKind::Direct, staging.logical_page_limit())
            .unwrap();
        for record in records {
            builder.push(staging, record).unwrap();
        }
        let result = builder.finish(staging).unwrap();
        staging.finish(result).unwrap()
    }

    fn build_v6(
        staging: &mut RangeTreeStaging<'_, Ipv6Key>,
        workspace: &mut RangeTreeBuildWorkspace<Ipv6Key>,
        records: impl IntoIterator<Item = RangeRecord<Ipv6Key>>,
    ) -> RangeTreeStagedResult {
        let mut builder = workspace
            .begin(2, ValueKind::Direct, staging.logical_page_limit())
            .unwrap();
        for record in records {
            builder.push(staging, record).unwrap();
        }
        let result = builder.finish(staging).unwrap();
        staging.finish(result).unwrap()
    }

    #[test]
    fn materializes_one_logical_leaf_without_exposing_a_temporary_root() {
        let mut pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::new();
        let staged = build_v4(&mut staging, &mut workspace, [record(1)]);
        assert_eq!(staged.logical_root, 2);
        let mut terminal = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        let result = staging
            .materialize(staged, 12, &[assignment(7)], &mut terminal)
            .unwrap();
        assert_eq!(result.root_pgno, 7);
        assert_eq!(result.page_count, 1);
        assert_eq!(terminal[0].owner, PrivatePageOwner::Range);
        assert_eq!(terminal[0].owner_generation, 2);
        assert_eq!(terminal[0].tag, AddressFamily::Ipv4 as u64);
        assert!(page::verify_crc32c(&terminal[0].bytes));
    }

    #[test]
    fn materializes_one_ipv6_logical_leaf_without_exposing_a_temporary_root() {
        let mut pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv6Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::new();
        let staged = build_v6(&mut staging, &mut workspace, [record_v6(1)]);
        let mut terminal = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        let result = staging
            .materialize(staged, 12, &[assignment(7)], &mut terminal)
            .unwrap();
        assert_eq!(result.root_pgno, 7);
        assert_eq!(terminal[0].owner, PrivatePageOwner::Range);
        assert_eq!(terminal[0].tag, AddressFamily::Ipv6 as u64);
        assert!(page::verify_crc32c(&terminal[0].bytes));
    }

    #[test]
    fn remaps_multilevel_children_and_reopens_only_after_materialization() {
        let capacity = crate::range_page::leaf_capacity::<Ipv4Key>() + 1;
        let mut pages = vec![RangeTreeStagingPage::empty(); 3];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::new();
        let staged = build_v4(
            &mut staging,
            &mut workspace,
            (0..capacity as u32).map(record),
        );
        assert_eq!(staged.page_count, 3);
        let assignments = [assignment(3), assignment(9), assignment(17)];
        let mut terminal: [PrivatePageCoordinatorTerminalPage; 3] =
            core::array::from_fn(|_| PrivatePageCoordinatorTerminalPage::empty());
        let result = staging
            .materialize(staged, 20, &assignments, &mut terminal)
            .unwrap();
        assert_eq!(result.root_pgno, 17);
        let branch =
            RangeBranch::<Ipv4Key>::open(&terminal[2].bytes, 2, AddressFamily::Ipv4, 20).unwrap();
        assert_eq!(branch.entry(0).unwrap().child_pgno, 3);
        assert_eq!(branch.entry(1).unwrap().child_pgno, 9);
        assert!(page::verify_crc32c(&terminal[2].bytes));

        let mut meta = empty_direct_meta(2);
        meta.address_family = AddressFamily::Ipv4;
        meta.value_kind = ValueKind::Direct;
        meta.value_tag = ValueTag::new(b"").unwrap();
        meta.page_count = 20;
        meta.range_root = result.root_pgno;
        meta.range_record_count = result.record_count;
        let mut image = vec![0u8; 20 * PAGE_SIZE];
        meta.encode_into((&mut image[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into((&mut image[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
        for page in terminal {
            let start = page.pgno as usize * PAGE_SIZE;
            image[start..start + PAGE_SIZE].copy_from_slice(&page.bytes);
        }
        let tree = RangeTree::<Ipv4Key>::open_immutable(&image).unwrap();
        assert_eq!(tree.lookup(Ipv4Key(0)).unwrap().unwrap().value, 1);
        assert_eq!(
            tree.lookup(Ipv4Key((capacity as u32 - 1) * 2))
                .unwrap()
                .unwrap()
                .value,
            1
        );
    }

    #[test]
    fn remaps_multilevel_ipv6_children_and_reopens_after_materialization() {
        let capacity = crate::range_page::leaf_capacity::<Ipv6Key>() + 1;
        let mut pages = vec![RangeTreeStagingPage::empty(); 3];
        let mut staging =
            RangeTreeStaging::<Ipv6Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::new();
        let staged = build_v6(
            &mut staging,
            &mut workspace,
            (0..capacity as u128).map(record_v6),
        );
        assert_eq!(staged.page_count, 3);
        let assignments = [assignment(4), assignment(10), assignment(19)];
        let mut terminal: [PrivatePageCoordinatorTerminalPage; 3] =
            core::array::from_fn(|_| PrivatePageCoordinatorTerminalPage::empty());
        let result = staging
            .materialize(staged, 20, &assignments, &mut terminal)
            .unwrap();
        assert_eq!(result.root_pgno, 19);
        let branch =
            RangeBranch::<Ipv6Key>::open(&terminal[2].bytes, 2, AddressFamily::Ipv6, 20).unwrap();
        assert_eq!(branch.entry(0).unwrap().child_pgno, 4);
        assert_eq!(branch.entry(1).unwrap().child_pgno, 10);
        assert!(page::verify_crc32c(&terminal[2].bytes));

        let mut meta = empty_direct_meta(2);
        meta.address_family = AddressFamily::Ipv6;
        meta.value_kind = ValueKind::Direct;
        meta.value_tag = ValueTag::new(b"").unwrap();
        meta.page_count = 20;
        meta.range_root = result.root_pgno;
        meta.range_record_count = result.record_count;
        let mut image = vec![0u8; 20 * PAGE_SIZE];
        meta.encode_into((&mut image[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into((&mut image[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
        for page in terminal {
            let start = page.pgno as usize * PAGE_SIZE;
            image[start..start + PAGE_SIZE].copy_from_slice(&page.bytes);
        }
        let tree = RangeTree::<Ipv6Key>::open_immutable(&image).unwrap();
        assert_eq!(
            tree.lookup(Ipv6Key::from_u128((capacity as u128 - 1) * 2))
                .unwrap()
                .unwrap()
                .value,
            1
        );
    }

    #[test]
    fn invalid_assignment_or_logical_child_leaves_terminal_output_untouched() {
        let mut pages = vec![RangeTreeStagingPage::empty(); 3];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::new();
        let staged = build_v4(
            &mut staging,
            &mut workspace,
            (0..=(crate::range_page::leaf_capacity::<Ipv4Key>() as u32)).map(record),
        );
        let before: [PrivatePageCoordinatorTerminalPage; 3] =
            core::array::from_fn(|_| PrivatePageCoordinatorTerminalPage::empty());
        let mut terminal = before.clone();
        assert_eq!(
            staging.materialize(
                staged,
                20,
                &[assignment(3), assignment(3), assignment(9)],
                &mut terminal
            ),
            Err(RangeTreeStagingError::PhysicalPageOrder {
                previous: 3,
                current: 3,
            })
        );
        assert_eq!(terminal, before);

        assert_eq!(
            staging.materialize(staged, 20, &[assignment(3), assignment(5)], &mut terminal),
            Err(RangeTreeStagingError::AssignmentCount {
                required: 3,
                actual: 2,
            })
        );
        assert_eq!(terminal, before);
        assert_eq!(
            staging.materialize(
                staged,
                1,
                &[assignment(3), assignment(5), assignment(9)],
                &mut terminal
            ),
            Err(RangeTreeStagingError::FinalPageCount { page_count: 1 })
        );
        assert_eq!(terminal, before);

        staging.pages[2].bytes
            [usize::from(PAGE_HEADER_SIZE) + 4..usize::from(PAGE_HEADER_SIZE) + 8]
            .copy_from_slice(&99u32.to_le_bytes());
        write_crc32c(&mut staging.pages[2].bytes);
        assert_eq!(
            staging.materialize(
                staged,
                20,
                &[assignment(3), assignment(5), assignment(9)],
                &mut terminal
            ),
            Err(RangeTreeStagingError::LogicalChildOutOfBounds { page: 2, child: 99 })
        );
        assert_eq!(terminal, before);

        staging.pages[0].bytes[0] ^= 1;
        assert_eq!(
            staging.materialize(
                staged,
                20,
                &[assignment(3), assignment(5), assignment(9)],
                &mut terminal
            ),
            Err(RangeTreeStagingError::InvalidStagedPage { index: 0 })
        );
        assert_eq!(terminal, before);
    }

    #[test]
    fn empty_trees_materialize_without_terminal_pages() {
        let mut ipv4_pages = [RangeTreeStagingPage::empty(); 1];
        let mut ipv4_staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut ipv4_pages, 2, ValueKind::Direct).unwrap();
        let mut ipv4_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let ipv4_result = ipv4_workspace
            .begin(2, ValueKind::Direct, ipv4_staging.logical_page_limit())
            .unwrap()
            .finish(&mut ipv4_staging)
            .unwrap();
        let ipv4_staged = ipv4_staging.finish(ipv4_result).unwrap();
        let mut ipv4_terminal = [];
        let ipv4_materialized = ipv4_staging
            .materialize(ipv4_staged, 2, &[], &mut ipv4_terminal)
            .unwrap();
        assert_eq!(ipv4_materialized.root_pgno, 0);
        assert_eq!(ipv4_materialized.page_count, 0);

        let mut pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv6Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::<Ipv6Key>::new();
        let mut builder = workspace
            .begin(2, ValueKind::Direct, staging.logical_page_limit())
            .unwrap();
        let result = builder.finish(&mut staging).unwrap();
        let staged = staging.finish(result).unwrap();
        let mut terminal = [];
        let materialized = staging.materialize(staged, 2, &[], &mut terminal).unwrap();
        assert_eq!(materialized.root_pgno, 0);
        assert_eq!(materialized.page_count, 0);
    }

    #[test]
    fn staging_and_materialization_allocate_nothing_after_fixed_setup() {
        let (_, allocations) = count_thread_allocations(|| {
            let mut pages = [RangeTreeStagingPage::empty(); 1];
            let mut staging =
                RangeTreeStaging::<Ipv4Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
            let mut workspace = RangeTreeBuildWorkspace::new();
            let staged = build_v4(&mut staging, &mut workspace, [record(1)]);
            let mut terminal = [PrivatePageCoordinatorTerminalPage::empty(); 1];
            staging
                .materialize(staged, 8, &[assignment(3)], &mut terminal)
                .unwrap()
        });
        assert_eq!(allocations, 0);
    }
}
