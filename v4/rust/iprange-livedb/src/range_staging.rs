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
    PrivatePagePool, PrivatePagePoolError, PrivatePagePoolState, PrivatePageReservationScope,
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

impl RangeTreePhysicalAssignment {
    pub(crate) const fn empty() -> Self {
        Self {
            pgno: 0,
            authorization: PrivatePageAuthorization::CommittedFree,
        }
    }
}

/// One exact private-pool slot selected for a staged range page. It remains
/// separate from the physical assignment because the terminal coordinator
/// binds its own pool slot later; this is only shadow-scope provenance.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RangeTreePayloadReservationSlot {
    pub(crate) slot: usize,
    pub(crate) binding_epoch: u64,
}

impl RangeTreePayloadReservationSlot {
    pub(crate) const fn empty() -> Self {
        Self {
            slot: usize::MAX,
            binding_epoch: 0,
        }
    }
}

/// Caller-owned scratch for one exact range-payload reservation. No backing
/// storage is allocated by this path, and the terminal prefix remains the
/// range input to the later coordinator journal merge.
pub(crate) struct RangeTreePayloadScratch<'a> {
    pub(crate) assignments: &'a mut [RangeTreePhysicalAssignment],
    pub(crate) slots: &'a mut [RangeTreePayloadReservationSlot],
    pub(crate) terminal_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
}

#[derive(Debug)]
pub(crate) enum RangeTreePayloadReservationError {
    AssignmentScratchTooSmall { required: usize, actual: usize },
    SlotScratchTooSmall { required: usize, actual: usize },
    TerminalScratchTooSmall { required: usize, actual: usize },
    AvailableSlots { required: usize, actual: usize },
    SlotUnavailable { slot: usize, pgno: u32 },
    PhysicalOrder { previous: u32, current: u32 },
    CheckpointSteps,
    Pool(PrivatePagePoolError),
    Staging(RangeTreeStagingError),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RangeTreeStagedResult {
    logical_root: u32,
    pub(crate) root_level: u16,
    pub(crate) record_count: u64,
    page_count: usize,
}

impl RangeTreeStagedResult {
    /// The root is a staging-local identifier until materialization assigns a
    /// physical file page.
    pub(crate) const fn logical_root(self) -> u32 {
        self.logical_root
    }

    pub(crate) const fn page_count(self) -> usize {
        self.page_count
    }
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
    fn logical_page_limit_for(
        pages: &[RangeTreeStagingPage],
    ) -> Result<u64, RangeTreeStagingError> {
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
        Ok(logical_page_limit)
    }

    pub(crate) fn new(
        pages: &'storage mut [RangeTreeStagingPage],
        born_txn: u64,
        value_kind: ValueKind,
    ) -> Result<Self, RangeTreeStagingError> {
        if born_txn == 0 {
            return Err(RangeTreeStagingError::BornTransactionZero);
        }
        let logical_page_limit = Self::logical_page_limit_for(pages)?;
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

    /// Reattaches an immutable view to logical pages sealed by this same
    /// private staging protocol. This does not validate a file or rewrite any
    /// bytes; materialization performs its existing output-geometry checks.
    pub(crate) fn reopen_sealed(
        pages: &'storage mut [RangeTreeStagingPage],
        born_txn: u64,
        value_kind: ValueKind,
        result: RangeTreeStagedResult,
    ) -> Result<Self, RangeTreeStagingError> {
        if born_txn == 0 {
            return Err(RangeTreeStagingError::BornTransactionZero);
        }
        let logical_page_limit = Self::logical_page_limit_for(pages)?;
        if result.page_count > pages.len()
            || pages[result.page_count..]
                .iter()
                .any(|page| !page.is_empty())
        {
            return Err(RangeTreeStagingError::StagedResultMismatch);
        }
        let staging = Self {
            pages,
            born_txn,
            value_kind,
            logical_page_limit,
            len: result.page_count,
            finished: true,
            _key: core::marker::PhantomData,
        };
        staging.check_staged_result(result)?;
        Ok(staging)
    }

    /// The temporary page-count bound supplied to the existing builder. No
    /// value in this range is a final file page number.
    pub(crate) const fn logical_page_limit(&self) -> u64 {
        self.logical_page_limit
    }

    pub(crate) const fn born_txn(&self) -> u64 {
        self.born_txn
    }

    pub(crate) const fn len(&self) -> usize {
        self.len
    }

    /// Erases unpublished logical output after the enclosing draft has been
    /// abandoned. The stale staging value stays finished so it cannot stage
    /// bytes under its old transaction generation.
    pub(crate) fn discard_after_abort(&mut self) {
        for page in &mut *self.pages {
            *page = RangeTreeStagingPage::empty();
        }
        self.len = 0;
        self.finished = true;
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

    /// Claims allocator-selected slots and installs a staged range tree in the
    /// exact shadow scope. All fallible checks complete before the checkpoint
    /// starts; its prepared suffix cannot fail and therefore cannot publish a
    /// partially claimed payload.
    #[allow(clippy::result_large_err)]
    pub(crate) fn reserve_payload_in_scope(
        &self,
        result: RangeTreeStagedResult,
        pool: &PrivatePagePool<'_>,
        scope: &PrivatePageReservationScope<'_>,
        available_slots: &[usize],
        available_len: usize,
        scratch: &mut RangeTreePayloadScratch<'_>,
    ) -> Result<RangeTreeMaterializedResult, RangeTreePayloadReservationError> {
        let required = result.page_count();
        if scratch.assignments.len() < required {
            return Err(
                RangeTreePayloadReservationError::AssignmentScratchTooSmall {
                    required,
                    actual: scratch.assignments.len(),
                },
            );
        }
        if scratch.slots.len() < required {
            return Err(RangeTreePayloadReservationError::SlotScratchTooSmall {
                required,
                actual: scratch.slots.len(),
            });
        }
        if scratch.terminal_pages.len() < required {
            return Err(RangeTreePayloadReservationError::TerminalScratchTooSmall {
                required,
                actual: scratch.terminal_pages.len(),
            });
        }
        if available_len > available_slots.len() || required > available_len {
            return Err(RangeTreePayloadReservationError::AvailableSlots {
                required,
                actual: available_len.min(available_slots.len()),
            });
        }

        let assignments = &mut scratch.assignments[..required];
        let slots = &mut scratch.slots[..required];
        let terminal_pages = &mut scratch.terminal_pages[..required];
        let clear_selection =
            |assignments: &mut [RangeTreePhysicalAssignment],
             slots: &mut [RangeTreePayloadReservationSlot]| {
                assignments.fill(RangeTreePhysicalAssignment::empty());
                slots.fill(RangeTreePayloadReservationSlot::empty());
            };
        let clear_terminal = |terminal_pages: &mut [PrivatePageCoordinatorTerminalPage]| {
            terminal_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
        };

        let mut previous = None;
        for index in 0..required {
            let slot = available_slots[available_len - 1 - index];
            let info = match pool.scoped_slot_info(scope, slot) {
                Ok(Some(info)) => info,
                Ok(None) => {
                    clear_selection(assignments, slots);
                    return Err(RangeTreePayloadReservationError::SlotUnavailable {
                        slot,
                        pgno: 0,
                    });
                }
                Err(error) => {
                    clear_selection(assignments, slots);
                    return Err(RangeTreePayloadReservationError::Pool(error));
                }
            };
            let Some(authorization) = info.authorization else {
                clear_selection(assignments, slots);
                return Err(RangeTreePayloadReservationError::SlotUnavailable {
                    slot,
                    pgno: info.pgno,
                });
            };
            if info.state != PrivatePagePoolState::Available {
                clear_selection(assignments, slots);
                return Err(RangeTreePayloadReservationError::SlotUnavailable {
                    slot,
                    pgno: info.pgno,
                });
            }
            if let Some(last) = previous {
                if info.pgno <= last {
                    clear_selection(assignments, slots);
                    return Err(RangeTreePayloadReservationError::PhysicalOrder {
                        previous: last,
                        current: info.pgno,
                    });
                }
            }
            previous = Some(info.pgno);
            assignments[index] = RangeTreePhysicalAssignment {
                pgno: info.pgno,
                authorization,
            };
            slots[index] = RangeTreePayloadReservationSlot {
                slot,
                binding_epoch: info.binding_epoch,
            };
        }

        // An empty logical tree must not consume a checkpoint generation or
        // touch the shadow scope. It has no payload to reserve, and callers
        // use the zero root as the complete result.
        if required == 0 {
            return self
                .materialize(
                    result,
                    pool.pending_page_count(),
                    assignments,
                    terminal_pages,
                )
                .map_err(RangeTreePayloadReservationError::Staging);
        }

        let checkpoint_steps = required
            .checked_add(2)
            .map(|steps| steps.max(2))
            .ok_or(RangeTreePayloadReservationError::CheckpointSteps)?;
        let checkpoint = match pool.preflight_checkpoint_steps(checkpoint_steps) {
            Ok(checkpoint) => checkpoint,
            Err(error) => {
                clear_selection(assignments, slots);
                return Err(RangeTreePayloadReservationError::Pool(error));
            }
        };
        let materialized = match self.materialize(
            result,
            pool.pending_page_count(),
            assignments,
            terminal_pages,
        ) {
            Ok(materialized) => materialized,
            Err(error) => {
                clear_selection(assignments, slots);
                return Err(RangeTreePayloadReservationError::Staging(error));
            }
        };
        if let Err(error) = pool.begin_checkpoint_prepared(&checkpoint) {
            clear_selection(assignments, slots);
            clear_terminal(terminal_pages);
            return Err(RangeTreePayloadReservationError::Pool(error));
        }
        for index in 0..required {
            let terminal = &terminal_pages[index];
            let selected = slots[index];
            pool.claim_slot_in_scope_for_checkpoint_prepared(
                &checkpoint,
                scope,
                selected.slot,
                selected.binding_epoch,
                PrivatePageOwner::Range,
                self.born_txn,
                terminal.tag,
                &terminal.bytes,
            );
        }
        pool.commit_checkpoint_in_scope_prepared(checkpoint, scope);
        Ok(materialized)
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
    use crate::private_page_pool::{PrivatePagePool, PrivatePagePoolSlot};
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
    fn sealed_reopen_rejects_hidden_trailing_logical_output() {
        let mut pages = [RangeTreeStagingPage::empty(); 2];
        let staged = {
            let mut staging =
                RangeTreeStaging::<Ipv4Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
            let mut workspace = RangeTreeBuildWorkspace::new();
            build_v4(&mut staging, &mut workspace, [record(1)])
        };
        pages[1].bytes[0] = 1;
        assert!(matches!(
            RangeTreeStaging::<Ipv4Key>::reopen_sealed(&mut pages, 2, ValueKind::Direct, staged,),
            Err(RangeTreeStagingError::StagedResultMismatch)
        ));
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
    fn payload_reservation_claims_lowest_slots_in_terminal_order() {
        let mut pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::new();
        let staged = build_v4(&mut staging, &mut workspace, [record(1)]);

        let mut storage = [const { PrivatePagePoolSlot::empty() }; 3];
        let pool = PrivatePagePool::new_vacant(&mut storage, 12, 12, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let low = pool
            .bind_page(
                &checkpoint,
                &scope,
                3,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        let middle = pool
            .bind_page(
                &checkpoint,
                &scope,
                6,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        let high = pool
            .bind_page(
                &checkpoint,
                &scope,
                9,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();

        let available = [high, middle, low];
        let mut assignments = [RangeTreePhysicalAssignment::empty(); 1];
        let mut slots = [RangeTreePayloadReservationSlot::empty(); 1];
        let mut terminal = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        let materialized = staging
            .reserve_payload_in_scope(
                staged,
                &pool,
                &scope,
                &available,
                available.len(),
                &mut RangeTreePayloadScratch {
                    assignments: &mut assignments,
                    slots: &mut slots,
                    terminal_pages: &mut terminal,
                },
            )
            .unwrap();

        assert_eq!(materialized.root_pgno, 3);
        assert_eq!(terminal[0].pgno, 3);
        assert_eq!(terminal[0].owner, PrivatePageOwner::Range);
        assert_eq!(slots[0].slot, low);
        assert!(matches!(
            pool.scoped_slot_info(&scope, low).unwrap().unwrap().state,
            PrivatePagePoolState::InUse {
                owner: PrivatePageOwner::Range,
                owner_generation: 2,
                tag: 4,
                ..
            }
        ));
        assert_eq!(
            pool.scoped_slot_info(&scope, middle)
                .unwrap()
                .unwrap()
                .state,
            PrivatePagePoolState::Available
        );
    }

    #[test]
    fn payload_reservation_rejects_short_available_list_without_claiming_a_slot() {
        let mut pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::new();
        let staged = build_v4(&mut staging, &mut workspace, [record(1)]);

        let mut storage = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = PrivatePagePool::new_vacant(&mut storage, 12, 12, 2).unwrap();
        let scope = pool.reserve_scope(2).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let low = pool
            .bind_page(
                &checkpoint,
                &scope,
                3,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        let high = pool
            .bind_page(
                &checkpoint,
                &scope,
                9,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();

        let available = [low, high];
        let mut assignments = [RangeTreePhysicalAssignment::empty(); 1];
        let mut slots = [RangeTreePayloadReservationSlot::empty(); 1];
        let before = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        let mut terminal = before.clone();
        assert!(matches!(
            staging.reserve_payload_in_scope(
                staged,
                &pool,
                &scope,
                &available,
                0,
                &mut RangeTreePayloadScratch {
                    assignments: &mut assignments,
                    slots: &mut slots,
                    terminal_pages: &mut terminal,
                },
            ),
            Err(RangeTreePayloadReservationError::AvailableSlots {
                required: 1,
                actual: 0,
            })
        ));
        assert_eq!(terminal, before);
        assert_eq!(
            pool.scoped_slot_info(&scope, low).unwrap().unwrap().state,
            PrivatePagePoolState::Available
        );
        assert_eq!(
            pool.scoped_slot_info(&scope, high).unwrap().unwrap().state,
            PrivatePagePoolState::Available
        );
    }

    #[test]
    fn payload_reservation_rejects_nonascending_allocator_order_without_mutation() {
        let mut pages = [RangeTreeStagingPage::empty(); 1];
        let staging = RangeTreeStaging::<Ipv4Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
        let staged = RangeTreeStagedResult {
            logical_root: 2,
            root_level: 0,
            record_count: 0,
            page_count: 2,
        };
        let mut storage = [const { PrivatePagePoolSlot::empty() }; 3];
        let pool = PrivatePagePool::new_vacant(&mut storage, 12, 12, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let low = pool
            .bind_page(
                &checkpoint,
                &scope,
                3,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        let middle = pool
            .bind_page(
                &checkpoint,
                &scope,
                6,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        let high = pool
            .bind_page(
                &checkpoint,
                &scope,
                9,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();

        let available = [high, low, middle];
        let mut assignments = [RangeTreePhysicalAssignment::empty(); 2];
        let mut slots = [RangeTreePayloadReservationSlot::empty(); 2];
        let before_terminal = [const { PrivatePageCoordinatorTerminalPage::empty() }; 2];
        let mut terminal = before_terminal.clone();
        let before_pool = pool.test_mutation_snapshot();
        assert!(matches!(
            staging.reserve_payload_in_scope(
                staged,
                &pool,
                &scope,
                &available,
                available.len(),
                &mut RangeTreePayloadScratch {
                    assignments: &mut assignments,
                    slots: &mut slots,
                    terminal_pages: &mut terminal,
                },
            ),
            Err(RangeTreePayloadReservationError::PhysicalOrder {
                previous: 6,
                current: 3,
            })
        ));
        assert_eq!(terminal, before_terminal);
        assert_eq!(pool.test_mutation_snapshot(), before_pool);
    }

    #[test]
    fn payload_reservation_rejects_dirty_terminal_scratch_without_mutation() {
        let mut pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::new();
        let staged = build_v4(&mut staging, &mut workspace, [record(1)]);
        let mut storage = [const { PrivatePagePoolSlot::empty() }; 3];
        let pool = PrivatePagePool::new_vacant(&mut storage, 12, 12, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let low = pool
            .bind_page(
                &checkpoint,
                &scope,
                3,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        let middle = pool
            .bind_page(
                &checkpoint,
                &scope,
                6,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        let high = pool
            .bind_page(
                &checkpoint,
                &scope,
                9,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();

        let available = [high, middle, low];
        let mut assignments = [RangeTreePhysicalAssignment::empty(); 1];
        let mut slots = [RangeTreePayloadReservationSlot::empty(); 1];
        let mut terminal = [PrivatePageCoordinatorTerminalPage::empty()];
        terminal[0].pgno = 99;
        let before_terminal = terminal.clone();
        let before_pool = pool.test_mutation_snapshot();
        assert!(matches!(
            staging.reserve_payload_in_scope(
                staged,
                &pool,
                &scope,
                &available,
                available.len(),
                &mut RangeTreePayloadScratch {
                    assignments: &mut assignments,
                    slots: &mut slots,
                    terminal_pages: &mut terminal,
                },
            ),
            Err(RangeTreePayloadReservationError::Staging(
                RangeTreeStagingError::TerminalOutputDirty
            ))
        ));
        assert_eq!(terminal, before_terminal);
        assert_eq!(pool.test_mutation_snapshot(), before_pool);
    }

    #[test]
    fn empty_payload_reservation_is_a_no_op() {
        let mut pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv6Key>::new(&mut pages, 2, ValueKind::Direct).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::<Ipv6Key>::new();
        let mut builder = workspace
            .begin(2, ValueKind::Direct, staging.logical_page_limit())
            .unwrap();
        let result = builder.finish(&mut staging).unwrap();
        let staged = staging.finish(result).unwrap();
        let mut storage = [const { PrivatePagePoolSlot::empty() }; 1];
        let pool = PrivatePagePool::new_vacant(&mut storage, 2, 2, 2).unwrap();
        let scope = pool.reserve_scope(1).unwrap();
        let before = pool.test_mutation_snapshot();
        let mut assignments = [];
        let mut slots = [];
        let mut terminal = [];
        let materialized = staging
            .reserve_payload_in_scope(
                staged,
                &pool,
                &scope,
                &[],
                0,
                &mut RangeTreePayloadScratch {
                    assignments: &mut assignments,
                    slots: &mut slots,
                    terminal_pages: &mut terminal,
                },
            )
            .unwrap();
        assert_eq!(materialized.root_pgno, 0);
        assert_eq!(materialized.page_count, 0);
        assert_eq!(pool.scope_status(&scope).unwrap().bound, 0);
        assert_eq!(pool.test_mutation_snapshot(), before);
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
