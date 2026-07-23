//! Private sequencing for transaction-local fixed-point work units.

use crate::bitmap_cow::{
    FreeBitmapCoordinatorOutputError, FreeBitmapCowError, PreparedFreeBitmapCoordinatorRecord,
    SealedFreeBitmapCoordinatorRecord, SealedFreeBitmapCoordinatorScratch,
};
use crate::contract::{MAX_PAGE_COUNT, PAGE_SIZE};
use crate::page_source::{CommittedPageSource, PageSourceError};
use crate::private_page_pool::{
    PrivatePageCoordinatorFence, PrivatePageCoordinatorPriorReturn,
    PrivatePageCoordinatorTerminalPage, PrivatePageCoordinatorWork, PrivatePagePool,
    PrivatePagePreparedCoordinatorPriorReturns, PrivatePagePreparedCoordinatorTerminal,
    PrivatePagePreparedScopeReservation, PrivatePagePreparedScopeSlot,
    PrivatePagePreparedSparseReplay, PrivatePageReservationScope, PrivatePageSealedProvenance,
    PrivatePageSparseReplayIndex, PrivatePageSparseReplaySlot,
};
use crate::retirement_writer::{PreparedProducedTerminalExport, RetirementTreeEditResult};
use core::cell::{Cell, RefCell};
use core::sync::atomic::{AtomicUsize, Ordering};

static NEXT_FIXED_POINT_IDENTITY: AtomicUsize = AtomicUsize::new(1);
static NEXT_FIXED_POINT_WORKSPACE_IDENTITY: AtomicUsize = AtomicUsize::new(1);

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum FixedPointError {
    InvalidArgument,
    InvalidWorkUnit,
    IdentityExhausted,
    StalePredecessor,
    RootOutOfBounds(u32),
    PageCountRegression { previous: u64, current: u64 },
    SourceOrderRegression { previous: u32, current: u32 },
    SourceScratchTooSmall { required: usize, actual: usize },
    ScratchAlias,
    AdvertisedOwnedPage(u32),
    AbortRequired,
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum FixedPointPrivateOutputDrainError<E> {
    Sink(E),
    Record(FreeBitmapCowError),
    Workspace(FixedPointError),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum DraftPageProvenance {
    SelectedCommitted {
        pgno: u32,
    },
    Private {
        work_unit: u64,
        page: PrivatePageSealedProvenance,
    },
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct DraftPrivatePageLocation {
    pub(crate) provenance: DraftPageProvenance,
    pub(crate) nonce: u64,
    pub(crate) record_index: usize,
    pub(crate) binding_index: usize,
}

impl DraftPrivatePageLocation {
    pub(crate) const EMPTY: Self = Self {
        provenance: DraftPageProvenance::SelectedCommitted { pgno: 0 },
        nonce: 0,
        record_index: usize::MAX,
        binding_index: usize::MAX,
    };
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct DraftPrivatePageEntry {
    work_unit: u64,
    nonce: u64,
    record_index: usize,
    binding_index: usize,
    page: PrivatePageSealedProvenance,
}

#[derive(Debug)]
pub(crate) struct FixedPointDraftSource<'a, 'index, 'slots, S: CommittedPageSource + ?Sized> {
    committed: &'a S,
    pool: &'a PrivatePagePool<'slots>,
    entries: RefCell<&'index mut [Option<DraftPrivatePageEntry>]>,
    slot_to_entry: RefCell<&'index mut [usize]>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum FixedPointWorkspaceRecordState {
    Vacant,
    Prepared(u64),
    Live(u64),
}

pub(crate) struct FixedPointWorkspaceRecordSlot<'arena, 'cleanup> {
    state: Cell<FixedPointWorkspaceRecordState>,
    scratch: Cell<Option<SealedFreeBitmapCoordinatorScratch<'arena, 'cleanup>>>,
    record: Cell<Option<PreparedFreeBitmapCoordinatorRecord<'arena, 'cleanup>>>,
}

impl<'arena, 'cleanup> FixedPointWorkspaceRecordSlot<'arena, 'cleanup> {
    pub(crate) const fn new(scratch: SealedFreeBitmapCoordinatorScratch<'arena, 'cleanup>) -> Self {
        Self {
            state: Cell::new(FixedPointWorkspaceRecordState::Vacant),
            scratch: Cell::new(Some(scratch)),
            record: Cell::new(None),
        }
    }

    fn retained_scratch_bytes(&self) -> Result<u64, FixedPointError> {
        let Some(scratch) = self.scratch.replace(None) else {
            return Err(FixedPointError::InvalidArgument);
        };
        let result = [
            core::mem::size_of_val(scratch.arena_bindings),
            core::mem::size_of_val(scratch.replacements),
            core::mem::size_of_val(scratch.index_nodes),
            core::mem::size_of_val(scratch.returned),
            core::mem::size_of_val(scratch.cleanup_nodes),
            core::mem::size_of_val(scratch.cleanup_path),
            core::mem::size_of_val(scratch.cleanup_targets),
        ]
        .into_iter()
        .try_fold(0u64, |total, bytes| {
            total
                .checked_add(u64::try_from(bytes).map_err(|_| FixedPointError::IdentityExhausted)?)
                .ok_or(FixedPointError::IdentityExhausted)
        });
        self.scratch.set(Some(scratch));
        result
    }
}

pub(crate) struct FixedPointWorkspaceBacking<'backing, 'arena, 'cleanup> {
    records: &'backing [FixedPointWorkspaceRecordSlot<'arena, 'cleanup>],
    source_entries: &'backing [Cell<Option<DraftPrivatePageEntry>>],
    source_slot_to_entry: &'backing [Cell<usize>],
    slot_to_record: &'backing [Cell<usize>],
    len: Cell<usize>,
    last_work_unit: Cell<u64>,
    revision: Cell<u64>,
    digest: Cell<u64>,
}

impl<'backing, 'arena, 'cleanup> FixedPointWorkspaceBacking<'backing, 'arena, 'cleanup> {
    pub(crate) fn new(
        records: &'backing [FixedPointWorkspaceRecordSlot<'arena, 'cleanup>],
        source_entries: &'backing [Cell<Option<DraftPrivatePageEntry>>],
        source_slot_to_entry: &'backing [Cell<usize>],
        slot_to_record: &'backing [Cell<usize>],
        pool_slots: usize,
    ) -> Result<Self, FixedPointError> {
        if records.is_empty()
            || source_entries.len() < pool_slots
            || source_slot_to_entry.len() < pool_slots
            || slot_to_record.len() < pool_slots
            || records.iter().any(|slot| {
                slot.state.get() != FixedPointWorkspaceRecordState::Vacant
                    || match slot.scratch.replace(None) {
                        Some(scratch) => {
                            slot.scratch.set(Some(scratch));
                            false
                        }
                        None => true,
                    }
                    || slot.record.take().is_some_and(|record| {
                        slot.record.set(Some(record));
                        true
                    })
            })
        {
            return Err(FixedPointError::InvalidArgument);
        }
        for entry in source_entries {
            entry.set(None);
        }
        for mapped in source_slot_to_entry {
            mapped.set(usize::MAX);
        }
        for mapped in slot_to_record {
            mapped.set(usize::MAX);
        }
        Ok(Self {
            records,
            source_entries,
            source_slot_to_entry,
            slot_to_record,
            len: Cell::new(0),
            last_work_unit: Cell::new(0),
            revision: Cell::new(0),
            digest: Cell::new(0),
        })
    }

    pub(crate) fn len(&self) -> usize {
        self.len.get()
    }

    #[cfg(test)]
    pub(crate) fn record_slot_ready(&self, index: usize, page_count: usize) -> bool {
        let Some(slot) = self.records.get(index) else {
            return false;
        };
        if slot.state.get() != FixedPointWorkspaceRecordState::Vacant
            || slot.record.replace(None).is_some_and(|record| {
                slot.record.set(Some(record));
                true
            })
        {
            return false;
        }
        let Some(scratch) = slot.scratch.replace(None) else {
            return false;
        };
        let ready = scratch.is_canonical_for(page_count);
        slot.scratch.set(Some(scratch));
        ready
    }

    #[cfg(test)]
    pub(crate) fn record_state(&self, index: usize) -> Option<FixedPointWorkspaceRecordState> {
        self.records.get(index).map(|slot| slot.state.get())
    }
}

#[derive(Clone, Copy)]
pub(crate) struct FixedPointCellWrite<'backing, T: Copy> {
    destination: &'backing Cell<T>,
    value: T,
}

impl<'backing, T: Copy> FixedPointCellWrite<'backing, T> {
    pub(crate) const fn new(destination: &'backing Cell<T>, value: T) -> Self {
        Self { destination, value }
    }
}

#[derive(Clone, Copy)]
pub(crate) struct FixedPointCellJournalBacking<'backing, T: Copy> {
    entries: &'backing [Cell<FixedPointCellWrite<'backing, T>>],
    neutral: FixedPointCellWrite<'backing, T>,
}

impl<'backing, T: Copy> FixedPointCellJournalBacking<'backing, T> {
    pub(crate) const fn new(
        entries: &'backing [Cell<FixedPointCellWrite<'backing, T>>],
        neutral: FixedPointCellWrite<'backing, T>,
    ) -> Self {
        Self { entries, neutral }
    }
}

#[derive(Clone, Copy)]
pub(crate) struct FixedPointCoordinatorJournals<'backing> {
    source: FixedPointCellJournalBacking<'backing, Option<DraftPrivatePageEntry>>,
    map: FixedPointCellJournalBacking<'backing, usize>,
    tombstone: FixedPointCellJournalBacking<'backing, bool>,
}

impl<'backing> FixedPointCoordinatorJournals<'backing> {
    pub(crate) const fn new(
        source: FixedPointCellJournalBacking<'backing, Option<DraftPrivatePageEntry>>,
        map: FixedPointCellJournalBacking<'backing, usize>,
        tombstone: FixedPointCellJournalBacking<'backing, bool>,
    ) -> Self {
        Self {
            source,
            map,
            tombstone,
        }
    }
}

/// Fixed caller storage for prebound writes. Only the used prefix is reset, and
/// replay reads that prefix directly without allocation or dynamic lookup.
struct FixedPointCellJournal<'backing, T: Copy> {
    entries: &'backing [Cell<FixedPointCellWrite<'backing, T>>],
    neutral: FixedPointCellWrite<'backing, T>,
    len: usize,
}

impl<'backing, T: Copy> FixedPointCellJournal<'backing, T> {
    const fn from_backing(backing: FixedPointCellJournalBacking<'backing, T>) -> Self {
        Self {
            entries: backing.entries,
            neutral: backing.neutral,
            len: 0,
        }
    }

    fn capacity(&self) -> usize {
        self.entries.len()
    }

    fn clear(&mut self) {
        for entry in &self.entries[..self.len] {
            entry.set(self.neutral);
        }
        self.len = 0;
    }

    fn push(&mut self, write: FixedPointCellWrite<'backing, T>) -> Result<(), FixedPointError> {
        let required = self
            .len
            .checked_add(1)
            .ok_or(FixedPointError::IdentityExhausted)?;
        let actual = self.entries.len();
        if required > actual {
            return Err(FixedPointError::SourceScratchTooSmall { required, actual });
        }
        self.entries[self.len].set(write);
        self.len = required;
        Ok(())
    }

    fn iter(&self) -> impl Iterator<Item = FixedPointCellWrite<'backing, T>> + '_ {
        self.entries[..self.len].iter().map(Cell::get)
    }
}

pub(crate) struct FixedPointCoordinatorSession<
    'session,
    'backing,
    'arena,
    'cleanup,
    'pool,
    'slots,
    'committed,
    S: CommittedPageSource + ?Sized,
> {
    backing: &'session FixedPointWorkspaceBacking<'backing, 'arena, 'cleanup>,
    committed: &'committed S,
    pool: &'pool PrivatePagePool<'slots>,
    source_writes: FixedPointCellJournal<'backing, Option<DraftPrivatePageEntry>>,
    map_writes: FixedPointCellJournal<'backing, usize>,
    tombstone_writes: FixedPointCellJournal<'backing, bool>,
}

impl<
        'session,
        'backing,
        'arena,
        'cleanup,
        'pool,
        'slots,
        'committed,
        S: CommittedPageSource + ?Sized,
    >
    FixedPointCoordinatorSession<'session, 'backing, 'arena, 'cleanup, 'pool, 'slots, 'committed, S>
where
    'arena: 'backing,
{
    pub(crate) fn new(
        backing: &'session FixedPointWorkspaceBacking<'backing, 'arena, 'cleanup>,
        committed: &'committed S,
        pool: &'pool PrivatePagePool<'slots>,
        journals: FixedPointCoordinatorJournals<'backing>,
    ) -> Result<Self, FixedPointError> {
        committed
            .check_access()
            .map_err(|_| FixedPointError::StalePredecessor)?;
        let writes = pool
            .len()
            .checked_mul(2)
            .ok_or(FixedPointError::IdentityExhausted)?;
        if journals.source.entries.len() < pool.len() {
            return Err(FixedPointError::SourceScratchTooSmall {
                required: pool.len(),
                actual: journals.source.entries.len(),
            });
        }
        if journals.map.entries.len() < writes {
            return Err(FixedPointError::SourceScratchTooSmall {
                required: writes,
                actual: journals.map.entries.len(),
            });
        }
        if journals.tombstone.entries.len() < pool.len() {
            return Err(FixedPointError::SourceScratchTooSmall {
                required: pool.len(),
                actual: journals.tombstone.entries.len(),
            });
        }
        Ok(Self {
            backing,
            committed,
            pool,
            source_writes: FixedPointCellJournal::from_backing(journals.source),
            map_writes: FixedPointCellJournal::from_backing(journals.map),
            tombstone_writes: FixedPointCellJournal::from_backing(journals.tombstone),
        })
    }

    fn reset_journal(&mut self) {
        self.source_writes.clear();
        self.map_writes.clear();
        self.tombstone_writes.clear();
    }

    fn append_prior_return(
        &mut self,
        returned: &'backing Cell<bool>,
        source_entry: &'backing Cell<Option<DraftPrivatePageEntry>>,
        source_map: &'backing Cell<usize>,
        record_map: &'backing Cell<usize>,
    ) -> Result<(), FixedPointError> {
        self.tombstone_writes
            .push(FixedPointCellWrite::new(returned, true))?;
        self.source_writes
            .push(FixedPointCellWrite::new(source_entry, None))?;
        self.map_writes
            .push(FixedPointCellWrite::new(source_map, usize::MAX))?;
        self.map_writes
            .push(FixedPointCellWrite::new(record_map, usize::MAX))
    }

    fn append_new_binding(
        &mut self,
        source_entry: &'backing Cell<Option<DraftPrivatePageEntry>>,
        entry: DraftPrivatePageEntry,
        source_map: &'backing Cell<usize>,
        source_slot: usize,
        record_map: &'backing Cell<usize>,
        record_index: usize,
    ) -> Result<(), FixedPointError> {
        self.source_writes
            .push(FixedPointCellWrite::new(source_entry, Some(entry)))?;
        self.map_writes
            .push(FixedPointCellWrite::new(source_map, source_slot))?;
        self.map_writes
            .push(FixedPointCellWrite::new(record_map, record_index))
    }
}

impl<S: CommittedPageSource + ?Sized> Drop
    for FixedPointCoordinatorSession<'_, '_, '_, '_, '_, '_, '_, S>
{
    fn drop(&mut self) {
        self.reset_journal();
    }
}

/// One caller-backed aggregate partition for a single private writer
/// transaction. The partition is intentionally fixed before preparation so
/// Active only consumes prebound journal and overlay storage.
pub(crate) struct FixedPointCoordinatorWorkspace<'backing, 'arena, 'cleanup> {
    identity: usize,
    backing: FixedPointWorkspaceBacking<'backing, 'arena, 'cleanup>,
    journals: FixedPointCoordinatorJournals<'backing>,
    ordered_prior_locations: &'backing mut [DraftPrivatePageLocation],
    pool_returns: &'backing mut [PrivatePageCoordinatorPriorReturn],
    new_locations: &'backing mut [DraftPrivatePageLocation],
    replay_slots: &'backing mut [PrivatePageSparseReplaySlot],
    replay_index: &'backing mut [PrivatePageSparseReplayIndex],
    retained_bytes: u64,
}

impl<'backing, 'arena, 'cleanup> FixedPointCoordinatorWorkspace<'backing, 'arena, 'cleanup> {
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn new(
        records: &'backing [FixedPointWorkspaceRecordSlot<'arena, 'cleanup>],
        source_entries: &'backing [Cell<Option<DraftPrivatePageEntry>>],
        source_slot_to_entry: &'backing [Cell<usize>],
        slot_to_record: &'backing [Cell<usize>],
        journals: FixedPointCoordinatorJournals<'backing>,
        ordered_prior_locations: &'backing mut [DraftPrivatePageLocation],
        pool_returns: &'backing mut [PrivatePageCoordinatorPriorReturn],
        new_locations: &'backing mut [DraftPrivatePageLocation],
        replay_slots: &'backing mut [PrivatePageSparseReplaySlot],
        replay_index: &'backing mut [PrivatePageSparseReplayIndex],
        pool_slots: usize,
    ) -> Result<Self, FixedPointError> {
        if replay_slots.is_empty()
            || replay_index.len() < pool_slots
            || ordered_prior_locations
                .iter()
                .any(|location| *location != DraftPrivatePageLocation::EMPTY)
            || pool_returns
                .iter()
                .any(|planned| *planned != PrivatePageCoordinatorPriorReturn::empty())
            || new_locations
                .iter()
                .any(|location| *location != DraftPrivatePageLocation::EMPTY)
            || replay_slots
                .iter()
                .any(|slot| *slot != PrivatePageSparseReplaySlot::empty())
            || replay_index
                .iter()
                .any(|entry| *entry != PrivatePageSparseReplayIndex::empty())
            || !Self::journals_are_neutral(journals)
            || !Self::ranges_are_disjoint([
                Self::slice_range(records),
                Self::slice_range(source_entries),
                Self::slice_range(source_slot_to_entry),
                Self::slice_range(slot_to_record),
                Self::slice_range(journals.source.entries),
                Self::slice_range(journals.map.entries),
                Self::slice_range(journals.tombstone.entries),
                Self::slice_range(ordered_prior_locations),
                Self::slice_range(pool_returns),
                Self::slice_range(new_locations),
                Self::slice_range(replay_slots),
                Self::slice_range(replay_index),
            ])
        {
            return Err(FixedPointError::InvalidArgument);
        }
        let retained_bytes = Self::calculate_retained_bytes(
            records,
            source_entries,
            source_slot_to_entry,
            slot_to_record,
            journals,
            ordered_prior_locations,
            pool_returns,
            new_locations,
            replay_slots,
            replay_index,
        )?;
        let identity = NEXT_FIXED_POINT_WORKSPACE_IDENTITY
            .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |value| {
                value.checked_add(1)
            })
            .map_err(|_| FixedPointError::IdentityExhausted)?;
        let backing = FixedPointWorkspaceBacking::new(
            records,
            source_entries,
            source_slot_to_entry,
            slot_to_record,
            pool_slots,
        )?;
        Ok(Self {
            identity,
            backing,
            journals,
            ordered_prior_locations,
            pool_returns,
            new_locations,
            replay_slots,
            replay_index,
            retained_bytes,
        })
    }

    pub(crate) const fn identity(&self) -> usize {
        self.identity
    }

    pub(crate) const fn retained_bytes(&self) -> u64 {
        self.retained_bytes
    }

    pub(crate) fn is_idle(&self) -> bool {
        self.backing.len() == 0 && self.backing.last_work_unit.get() == 0
    }

    #[allow(clippy::type_complexity, clippy::result_large_err)]
    pub(crate) fn prepare_aggregate<
        'workspace,
        'slot,
        'scope_slot,
        'preparation_scratch,
        'carried,
        'plan,
        'pool,
        'slots,
        'committed,
        S: CommittedPageSource + ?Sized,
        B,
    >(
        &'workspace mut self,
        produced: FixedPointPreparedProducedTerminalWork<
            'slot,
            'scope_slot,
            'preparation_scratch,
            'carried,
            'plan,
            B,
        >,
        coordinator: &FixedPointCoordinator,
        predecessor: &FixedPointPredecessor,
        pool: &'pool PrivatePagePool<'slots>,
        committed: &'committed S,
        requested_prior_returns: &[DraftPrivatePageLocation],
    ) -> Result<
        FixedPointPreparedAggregateWork<
            'slot,
            'preparation_scratch,
            'carried,
            'pool,
            'slots,
            'plan,
            'workspace,
            'workspace,
            'backing,
            'arena,
            'cleanup,
            'committed,
            'workspace,
            'workspace,
            'workspace,
            S,
            B,
        >,
        (
            FixedPointPreparedProducedTerminalWork<
                'slot,
                'scope_slot,
                'preparation_scratch,
                'carried,
                'plan,
                B,
            >,
            FixedPointError,
        ),
    >
    where
        'arena: 'backing,
    {
        let record_index = self.backing.len();
        let session = match FixedPointCoordinatorSession::new(
            &self.backing,
            committed,
            pool,
            self.journals,
        ) {
            Ok(session) => session,
            Err(error) => return Err((produced, error)),
        };
        produced.prepare_aggregate(
            coordinator,
            predecessor,
            pool,
            session,
            requested_prior_returns,
            &mut *self.ordered_prior_locations,
            &mut *self.pool_returns,
            record_index,
            &mut *self.new_locations,
            &mut *self.replay_slots,
            &mut *self.replay_index,
            self.identity,
        )
    }

    pub(crate) fn cancel(&mut self) -> Result<(), FixedPointError> {
        for slot in self.backing.records {
            if slot.state.get() == FixedPointWorkspaceRecordState::Vacant {
                continue;
            }
            let Some(record) = slot.record.replace(None) else {
                return Err(FixedPointError::InvalidWorkUnit);
            };
            let scratch = record.cancel_into_scratch();
            slot.scratch.set(Some(scratch));
            slot.state.set(FixedPointWorkspaceRecordState::Vacant);
        }
        for entry in self.backing.source_entries {
            entry.set(None);
        }
        for entry in self.backing.source_slot_to_entry {
            entry.set(usize::MAX);
        }
        for entry in self.backing.slot_to_record {
            entry.set(usize::MAX);
        }
        self.backing.len.set(0);
        self.backing.last_work_unit.set(0);
        self.backing.revision.set(0);
        self.backing.digest.set(0);
        self.ordered_prior_locations
            .fill(DraftPrivatePageLocation::EMPTY);
        self.pool_returns
            .fill(PrivatePageCoordinatorPriorReturn::empty());
        self.new_locations.fill(DraftPrivatePageLocation::EMPTY);
        self.replay_slots.fill(PrivatePageSparseReplaySlot::empty());
        self.replay_index
            .fill(PrivatePageSparseReplayIndex::empty());
        Self::reset_journal_cells(self.journals);
        Ok(())
    }

    /// Writes every retained private page before releasing the scope that owns
    /// it. A caller must only invoke this after input is finished; failures
    /// leave publication forbidden and let the transaction take its normal
    /// cancel-and-abort path.
    pub(crate) fn drain_private_pages<E>(
        &mut self,
        pool: &PrivatePagePool<'_>,
        write: &mut impl FnMut(u32, &[u8]) -> Result<(), E>,
    ) -> Result<usize, FixedPointPrivateOutputDrainError<E>> {
        let len = self.backing.len.get();
        if len == 0 || self.backing.last_work_unit.get() == 0 {
            return Err(FixedPointPrivateOutputDrainError::Workspace(
                FixedPointError::InvalidWorkUnit,
            ));
        }
        let mut written = 0usize;
        for index in 0..len {
            let Some(slot) = self.backing.records.get(index) else {
                return Err(FixedPointPrivateOutputDrainError::Workspace(
                    FixedPointError::StalePredecessor,
                ));
            };
            if !matches!(slot.state.get(), FixedPointWorkspaceRecordState::Live(_)) {
                return Err(FixedPointPrivateOutputDrainError::Workspace(
                    FixedPointError::StalePredecessor,
                ));
            }
            let scratch = slot.scratch.replace(None);
            if scratch.is_some() {
                slot.scratch.set(scratch);
                return Err(FixedPointPrivateOutputDrainError::Workspace(
                    FixedPointError::StalePredecessor,
                ));
            }
            let Some(record) = slot.record.replace(None) else {
                return Err(FixedPointPrivateOutputDrainError::Workspace(
                    FixedPointError::StalePredecessor,
                ));
            };
            let record_pages = match record.write_private_pages(pool, write) {
                Ok(count) => count,
                Err(FreeBitmapCoordinatorOutputError::Sink(error)) => {
                    slot.record.set(Some(record));
                    return Err(FixedPointPrivateOutputDrainError::Sink(error));
                }
                Err(FreeBitmapCoordinatorOutputError::Record(error)) => {
                    slot.record.set(Some(record));
                    return Err(FixedPointPrivateOutputDrainError::Record(error));
                }
            };
            let Some(next_written) = written.checked_add(record_pages) else {
                slot.record.set(Some(record));
                return Err(FixedPointPrivateOutputDrainError::Workspace(
                    FixedPointError::IdentityExhausted,
                ));
            };
            let sealed = record.materialize(pool);
            let scratch = match sealed.cleanup() {
                Ok(scratch) => scratch,
                Err((sealed, error)) => {
                    slot.scratch
                        .set(Some(sealed.cancel_inactive_into_scratch()));
                    slot.state.set(FixedPointWorkspaceRecordState::Vacant);
                    return Err(FixedPointPrivateOutputDrainError::Record(error));
                }
            };
            slot.scratch.set(Some(scratch));
            slot.state.set(FixedPointWorkspaceRecordState::Vacant);
            written = next_written;
        }
        self.cancel()
            .map_err(FixedPointPrivateOutputDrainError::Workspace)?;
        Ok(written)
    }

    fn validate_live_record_handoff(
        &self,
        pool: &PrivatePagePool<'_>,
        record_index: usize,
        scope: &PrivatePageReservationScope<'_>,
        work_unit: u64,
        root: u32,
        pending_page_count: u64,
    ) -> Result<(), FixedPointError> {
        let Some(slot) = self.backing.records.get(record_index) else {
            return Err(FixedPointError::StalePredecessor);
        };
        if record_index >= self.backing.len.get()
            || self.backing.last_work_unit.get() != work_unit
            || slot.state.get() != FixedPointWorkspaceRecordState::Live(work_unit)
        {
            return Err(FixedPointError::StalePredecessor);
        }
        let Some(record) = slot.record.replace(None) else {
            return Err(FixedPointError::StalePredecessor);
        };
        let result = record.validate_sealed_handoff(
            pool,
            scope,
            work_unit,
            record_index,
            root,
            pending_page_count,
        );
        slot.record.set(Some(record));
        result.map_err(|_| FixedPointError::StalePredecessor)
    }

    #[cfg(test)]
    pub(crate) fn record_state(&self, index: usize) -> Option<FixedPointWorkspaceRecordState> {
        self.backing.record_state(index)
    }

    #[cfg(test)]
    pub(crate) fn record_slot_ready(&self, index: usize, page_count: usize) -> bool {
        self.backing.record_slot_ready(index, page_count)
    }

    #[allow(clippy::too_many_arguments)]
    fn calculate_retained_bytes(
        records: &[FixedPointWorkspaceRecordSlot<'arena, 'cleanup>],
        source_entries: &[Cell<Option<DraftPrivatePageEntry>>],
        source_slot_to_entry: &[Cell<usize>],
        slot_to_record: &[Cell<usize>],
        journals: FixedPointCoordinatorJournals<'_>,
        ordered_prior_locations: &[DraftPrivatePageLocation],
        pool_returns: &[PrivatePageCoordinatorPriorReturn],
        new_locations: &[DraftPrivatePageLocation],
        replay_slots: &[PrivatePageSparseReplaySlot],
        replay_index: &[PrivatePageSparseReplayIndex],
    ) -> Result<u64, FixedPointError> {
        let mut total = u64::try_from(core::mem::size_of::<Self>())
            .map_err(|_| FixedPointError::IdentityExhausted)?;
        for bytes in [
            core::mem::size_of_val(records),
            core::mem::size_of_val(source_entries),
            core::mem::size_of_val(source_slot_to_entry),
            core::mem::size_of_val(slot_to_record),
            core::mem::size_of_val(journals.source.entries),
            core::mem::size_of_val(journals.map.entries),
            core::mem::size_of_val(journals.tombstone.entries),
            core::mem::size_of_val(ordered_prior_locations),
            core::mem::size_of_val(pool_returns),
            core::mem::size_of_val(new_locations),
            core::mem::size_of_val(replay_slots),
            core::mem::size_of_val(replay_index),
        ] {
            total = total
                .checked_add(u64::try_from(bytes).map_err(|_| FixedPointError::IdentityExhausted)?)
                .ok_or(FixedPointError::IdentityExhausted)?;
        }
        for record in records {
            total = total
                .checked_add(record.retained_scratch_bytes()?)
                .ok_or(FixedPointError::IdentityExhausted)?;
        }
        Ok(total)
    }

    fn journals_are_neutral(journals: FixedPointCoordinatorJournals<'_>) -> bool {
        fn entries_are_neutral<'journal, T: Copy + PartialEq>(
            entries: &[Cell<FixedPointCellWrite<'journal, T>>],
            neutral: FixedPointCellWrite<'journal, T>,
        ) -> bool {
            entries.iter().all(|entry| {
                let current = entry.get();
                core::ptr::eq(current.destination, neutral.destination)
                    && current.value == neutral.value
            })
        }

        entries_are_neutral(journals.source.entries, journals.source.neutral)
            && entries_are_neutral(journals.map.entries, journals.map.neutral)
            && entries_are_neutral(journals.tombstone.entries, journals.tombstone.neutral)
    }

    fn reset_journal_cells(journals: FixedPointCoordinatorJournals<'_>) {
        fn reset<'journal, T: Copy>(
            entries: &[Cell<FixedPointCellWrite<'journal, T>>],
            neutral: FixedPointCellWrite<'journal, T>,
        ) {
            for entry in entries {
                entry.set(neutral);
            }
        }

        reset(journals.source.entries, journals.source.neutral);
        reset(journals.map.entries, journals.map.neutral);
        reset(journals.tombstone.entries, journals.tombstone.neutral);
    }

    fn slice_range<T>(slice: &[T]) -> Option<(usize, usize)> {
        let start = slice.as_ptr() as usize;
        let bytes = core::mem::size_of_val(slice);
        start.checked_add(bytes).map(|end| (start, end))
    }

    fn ranges_are_disjoint<const N: usize>(ranges: [Option<(usize, usize)>; N]) -> bool {
        ranges.iter().enumerate().all(|(left_index, left)| {
            ranges[..left_index]
                .iter()
                .all(|right| match (left, right) {
                    (Some((left_start, left_end)), Some((right_start, right_end))) => {
                        left_start >= right_end
                            || right_start >= left_end
                            || left_start == left_end
                            || right_start == right_end
                    }
                    _ => true,
                })
        })
    }
}

impl<'a, 'index, 'slots, S: CommittedPageSource + ?Sized>
    FixedPointDraftSource<'a, 'index, 'slots, S>
{
    pub(crate) fn new(
        committed: &'a S,
        pool: &'a PrivatePagePool<'slots>,
        entries: &'index mut [Option<DraftPrivatePageEntry>],
        slot_to_entry: &'index mut [usize],
    ) -> Result<Self, FixedPointError> {
        if entries.len() < pool.len() || slot_to_entry.len() < pool.len() {
            return Err(FixedPointError::InvalidArgument);
        }
        entries.fill(None);
        slot_to_entry.fill(usize::MAX);
        Ok(Self {
            committed,
            pool,
            entries: RefCell::new(entries),
            slot_to_entry: RefCell::new(slot_to_entry),
        })
    }

    pub(crate) fn register_private_page(
        &self,
        work_unit: u64,
        nonce: u64,
        record_index: usize,
        binding_index: usize,
        page: PrivatePageSealedProvenance,
    ) -> Result<(), FixedPointError> {
        if work_unit == 0 || nonce == 0 {
            return Err(FixedPointError::InvalidWorkUnit);
        }
        let mut entries = self
            .entries
            .try_borrow_mut()
            .map_err(|_| FixedPointError::StalePredecessor)?;
        let mut slot_to_entry = self
            .slot_to_entry
            .try_borrow_mut()
            .map_err(|_| FixedPointError::StalePredecessor)?;
        let mapped = slot_to_entry
            .get_mut(page.slot)
            .ok_or(FixedPointError::InvalidArgument)?;
        if *mapped != usize::MAX {
            return Err(FixedPointError::AdvertisedOwnedPage(page.pgno));
        }
        let entry_index = page.slot;
        entries[entry_index] = Some(DraftPrivatePageEntry {
            work_unit,
            nonce,
            record_index,
            binding_index,
            page,
        });
        *mapped = entry_index;
        Ok(())
    }

    pub(crate) fn register_private_location(
        &self,
        location: DraftPrivatePageLocation,
    ) -> Result<(), FixedPointError> {
        let DraftPageProvenance::Private { work_unit, page } = location.provenance else {
            return Err(FixedPointError::InvalidArgument);
        };
        self.register_private_page(
            work_unit,
            location.nonce,
            location.record_index,
            location.binding_index,
            page,
        )
    }

    fn validate_private_location_registration(
        &self,
        location: DraftPrivatePageLocation,
    ) -> Result<(), FixedPointError> {
        let DraftPageProvenance::Private { work_unit, page } = location.provenance else {
            return Err(FixedPointError::InvalidArgument);
        };
        if work_unit == 0 || location.nonce == 0 || page.slot >= self.pool.len() {
            return Err(FixedPointError::InvalidWorkUnit);
        }
        let entries = self
            .entries
            .try_borrow()
            .map_err(|_| FixedPointError::StalePredecessor)?;
        let slot_to_entry = self
            .slot_to_entry
            .try_borrow()
            .map_err(|_| FixedPointError::StalePredecessor)?;
        if entries[page.slot].is_some() || slot_to_entry[page.slot] != usize::MAX {
            return Err(FixedPointError::AdvertisedOwnedPage(page.pgno));
        }
        Ok(())
    }

    fn detach_private_location(
        &self,
        location: DraftPrivatePageLocation,
        require_unbound: bool,
    ) -> Result<(), FixedPointError> {
        let DraftPageProvenance::Private { work_unit, page } = location.provenance else {
            return Err(FixedPointError::InvalidArgument);
        };
        if require_unbound
            && self
                .pool
                .find_bound_page(page.pgno)
                .map_err(|_| FixedPointError::StalePredecessor)?
                .is_some()
        {
            return Err(FixedPointError::AdvertisedOwnedPage(page.pgno));
        }
        let mut entries = self
            .entries
            .try_borrow_mut()
            .map_err(|_| FixedPointError::StalePredecessor)?;
        let mut slot_to_entry = self
            .slot_to_entry
            .try_borrow_mut()
            .map_err(|_| FixedPointError::StalePredecessor)?;
        let mapped = slot_to_entry
            .get_mut(page.slot)
            .ok_or(FixedPointError::InvalidArgument)?;
        let entry = entries
            .get_mut(*mapped)
            .ok_or(FixedPointError::StalePredecessor)?;
        let current = entry.as_ref().ok_or(FixedPointError::StalePredecessor)?;
        if current.work_unit != work_unit
            || current.nonce != location.nonce
            || current.record_index != location.record_index
            || current.binding_index != location.binding_index
            || current.page != page
        {
            return Err(FixedPointError::StalePredecessor);
        }
        *entry = None;
        *mapped = usize::MAX;
        Ok(())
    }

    pub(crate) fn private_provenance(
        &self,
        pgno: u32,
    ) -> Result<Option<DraftPageProvenance>, FixedPointError> {
        Ok(self
            .private_location(pgno)?
            .map(|location| location.provenance))
    }

    pub(crate) fn private_location(
        &self,
        pgno: u32,
    ) -> Result<Option<DraftPrivatePageLocation>, FixedPointError> {
        let Some(slot) = self
            .pool
            .find_bound_page(pgno)
            .map_err(|_| FixedPointError::StalePredecessor)?
        else {
            return Ok(None);
        };
        let slot_to_entry = self
            .slot_to_entry
            .try_borrow()
            .map_err(|_| FixedPointError::StalePredecessor)?;
        let entry_index = *slot_to_entry
            .get(slot)
            .ok_or(FixedPointError::InvalidArgument)?;
        let entries = self
            .entries
            .try_borrow()
            .map_err(|_| FixedPointError::StalePredecessor)?;
        let entry = entries
            .get(entry_index)
            .and_then(Option::as_ref)
            .ok_or(FixedPointError::StalePredecessor)?;
        if entry.page.slot != slot || entry.page.pgno != pgno {
            return Err(FixedPointError::StalePredecessor);
        }
        Ok(Some(DraftPrivatePageLocation {
            provenance: DraftPageProvenance::Private {
                work_unit: entry.work_unit,
                page: entry.page,
            },
            nonce: entry.nonce,
            record_index: entry.record_index,
            binding_index: entry.binding_index,
        }))
    }

    pub(crate) fn unregister_private_page(
        &self,
        location: DraftPrivatePageLocation,
    ) -> Result<(), FixedPointError> {
        self.detach_private_location(location, true)
    }

    fn validate_private_location_detach(
        &self,
        location: DraftPrivatePageLocation,
    ) -> Result<(), FixedPointError> {
        let DraftPageProvenance::Private { work_unit, page } = location.provenance else {
            return Err(FixedPointError::InvalidArgument);
        };
        let entries = self
            .entries
            .try_borrow()
            .map_err(|_| FixedPointError::StalePredecessor)?;
        let slot_to_entry = self
            .slot_to_entry
            .try_borrow()
            .map_err(|_| FixedPointError::StalePredecessor)?;
        let entry_index = *slot_to_entry
            .get(page.slot)
            .ok_or(FixedPointError::StalePredecessor)?;
        let expected = DraftPrivatePageEntry {
            work_unit,
            nonce: location.nonce,
            record_index: location.record_index,
            binding_index: location.binding_index,
            page,
        };
        if entry_index == usize::MAX
            || entries.get(entry_index).copied().flatten() != Some(expected)
        {
            return Err(FixedPointError::StalePredecessor);
        }
        Ok(())
    }

    fn detach_private_location_terminal_prepared(&self, location: DraftPrivatePageLocation) {
        let DraftPageProvenance::Private { page, .. } = location.provenance else {
            unreachable!("prepared prior return retains private provenance");
        };
        let mut entries = self
            .entries
            .try_borrow_mut()
            .expect("prepared prior return owns the draft-source mutation suffix");
        let mut slot_to_entry = self
            .slot_to_entry
            .try_borrow_mut()
            .expect("prepared prior return owns the slot-map mutation suffix");
        let entry_index = slot_to_entry[page.slot];
        entries[entry_index] = None;
        slot_to_entry[page.slot] = usize::MAX;
    }

    pub(crate) fn read_page_with_provenance(
        &self,
        pgno: u32,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<DraftPageProvenance, PageSourceError> {
        let Some(location) = self
            .private_location(pgno)
            .map_err(|_| PageSourceError::ForkedHandle)?
        else {
            self.committed.read_page(pgno, destination)?;
            return Ok(DraftPageProvenance::SelectedCommitted { pgno });
        };
        self.read_private_location(location, destination)?;
        Ok(location.provenance)
    }

    pub(crate) fn read_private_location(
        &self,
        location: DraftPrivatePageLocation,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<(), PageSourceError> {
        let DraftPageProvenance::Private { page, .. } = location.provenance else {
            return Err(PageSourceError::ForkedHandle);
        };
        if self
            .private_location(page.pgno)
            .map_err(|_| PageSourceError::ForkedHandle)?
            != Some(location)
        {
            return Err(PageSourceError::ForkedHandle);
        }
        self.pool
            .copy_sealed_page_by_provenance(&page, location.nonce, destination)
            .map_err(|_| PageSourceError::ForkedHandle)
    }
}

pub(crate) struct FixedPointSealedLedger<'records, 'scratch, 'a, 'slots> {
    records: &'records mut [Option<SealedFreeBitmapCoordinatorRecord<'scratch, 'a, 'a, 'slots>>],
    slot_to_record: &'records mut [usize],
    len: usize,
    last_work_unit: u64,
}

pub(crate) struct FixedPointPreparedPriorReturns<
    'ledger,
    'records,
    'record_scratch,
    'record_pool,
    'slots,
    'source,
    'committed,
    'source_index,
    'locations,
    'plan,
    S: CommittedPageSource + ?Sized,
> {
    ledger: &'ledger mut FixedPointSealedLedger<'records, 'record_scratch, 'record_pool, 'slots>,
    source: &'source mut FixedPointDraftSource<'committed, 'source_index, 'slots, S>,
    locations: &'locations mut [DraftPrivatePageLocation],
    pool_plan: PrivatePagePreparedCoordinatorPriorReturns<'plan>,
}

impl core::fmt::Debug for FixedPointSealedLedger<'_, '_, '_, '_> {
    fn fmt(&self, formatter: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        formatter
            .debug_struct("FixedPointSealedLedger")
            .field("capacity", &self.records.len())
            .field("len", &self.len)
            .finish_non_exhaustive()
    }
}

impl<'records, 'scratch, 'a, 'slots> FixedPointSealedLedger<'records, 'scratch, 'a, 'slots> {
    pub(crate) fn new(
        records: &'records mut [Option<
            SealedFreeBitmapCoordinatorRecord<'scratch, 'a, 'a, 'slots>,
        >],
        slot_to_record: &'records mut [usize],
        pool_slots: usize,
    ) -> Result<Self, FixedPointError> {
        if records.is_empty() || slot_to_record.len() < pool_slots {
            return Err(FixedPointError::InvalidArgument);
        }
        records.fill_with(|| None);
        slot_to_record.fill(usize::MAX);
        Ok(Self {
            records,
            slot_to_record,
            len: 0,
            last_work_unit: 0,
        })
    }

    pub(crate) const fn len(&self) -> usize {
        self.len
    }

    pub(crate) const fn next_record_index(&self) -> usize {
        self.len
    }

    pub(crate) fn record(
        &self,
        index: usize,
    ) -> Option<&SealedFreeBitmapCoordinatorRecord<'scratch, 'a, 'a, 'slots>> {
        self.records.get(index).and_then(Option::as_ref)
    }

    pub(crate) fn record_mut(
        &mut self,
        index: usize,
    ) -> Option<&mut SealedFreeBitmapCoordinatorRecord<'scratch, 'a, 'a, 'slots>> {
        self.records.get_mut(index).and_then(Option::as_mut)
    }

    pub(crate) fn into_records(
        self,
    ) -> &'records mut [Option<SealedFreeBitmapCoordinatorRecord<'scratch, 'a, 'a, 'slots>>] {
        self.records
    }

    #[allow(clippy::result_large_err)] // Failure returns the move-only sealed record.
    pub(crate) fn push<S: CommittedPageSource + ?Sized>(
        &mut self,
        record: SealedFreeBitmapCoordinatorRecord<'scratch, 'a, 'a, 'slots>,
        source: &FixedPointDraftSource<'_, '_, 'slots, S>,
    ) -> Result<
        (),
        (
            SealedFreeBitmapCoordinatorRecord<'scratch, 'a, 'a, 'slots>,
            FixedPointError,
        ),
    > {
        let index = self.len;
        if index == self.records.len() {
            return Err((
                record,
                FixedPointError::SourceScratchTooSmall {
                    required: index.saturating_add(1),
                    actual: self.records.len(),
                },
            ));
        }
        if record.record_index != index {
            return Err((record, FixedPointError::InvalidWorkUnit));
        }
        if record.work_unit == 0 || record.work_unit <= self.last_work_unit {
            return Err((record, FixedPointError::InvalidWorkUnit));
        }
        // Safe Rust cannot construct overlapping mutable record partitions.
        // The caller-provided record index is therefore the O(1) liveness
        // fence; rescanning every retained record would make repeated work
        // quadratic without strengthening the ownership proof.
        let mut preflight_error = None;
        if let Err(error) = record.visit_private_pages(|location| {
            if preflight_error.is_none() {
                preflight_error = source
                    .validate_private_location_registration(location)
                    .err();
            }
            Ok(())
        }) {
            return Err((record, error));
        }
        if let Some(error) = preflight_error {
            return Err((record, error));
        }
        if record.map_bound_slots(index, self.slot_to_record).is_err() {
            return Err((record, FixedPointError::StalePredecessor));
        }
        let mut registered = 0usize;
        let register_result = record.visit_private_pages(|location| {
            source.register_private_location(location)?;
            registered += 1;
            Ok(())
        });
        if let Err(error) = register_result {
            let mut remaining = registered;
            let _ = record.visit_private_pages(|location| {
                if remaining != 0 {
                    source.detach_private_location(location, false)?;
                    remaining -= 1;
                }
                Ok(())
            });
            let mut remaining = registered;
            let _ = record.visit_private_pages(|location| {
                if remaining != 0 {
                    let DraftPageProvenance::Private { page, .. } = location.provenance else {
                        return Err(FixedPointError::StalePredecessor);
                    };
                    if self.slot_to_record.get(page.slot).copied() == Some(index) {
                        self.slot_to_record[page.slot] = usize::MAX;
                    }
                    remaining -= 1;
                }
                Ok(())
            });
            return Err((record, error));
        }
        self.records[index] = Some(record);
        self.len += 1;
        self.last_work_unit = self.records[index]
            .as_ref()
            .expect("inserted record remains present")
            .work_unit;
        Ok(())
    }

    #[allow(clippy::type_complexity)]
    fn prepare_prior_returns<
        'ledger,
        'source,
        'committed,
        'source_index,
        'locations,
        'plan,
        S: CommittedPageSource + ?Sized,
    >(
        &'ledger mut self,
        source: &'source mut FixedPointDraftSource<'committed, 'source_index, 'slots, S>,
        prepared_scope: &PrivatePagePreparedScopeReservation<'_>,
        terminal: &PrivatePagePreparedCoordinatorTerminal<'_>,
        requested: &[DraftPrivatePageLocation],
        ordered_locations: &'locations mut [DraftPrivatePageLocation],
        pool_returns: &'plan mut [PrivatePageCoordinatorPriorReturn],
    ) -> Result<
        FixedPointPreparedPriorReturns<
            'ledger,
            'records,
            'scratch,
            'a,
            'slots,
            'source,
            'committed,
            'source_index,
            'locations,
            'plan,
            S,
        >,
        FreeBitmapCowError,
    > {
        if ordered_locations.len() < requested.len()
            || pool_returns.len() < requested.len()
            || ordered_locations
                .iter()
                .any(|location| *location != DraftPrivatePageLocation::EMPTY)
            || pool_returns
                .iter()
                .any(|planned| *planned != PrivatePageCoordinatorPriorReturn::empty())
        {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        let len = requested.len();
        ordered_locations[..len].copy_from_slice(requested);
        ordered_locations[..len].sort_unstable_by_key(|location| match location.provenance {
            DraftPageProvenance::Private { page, .. } => page.slot,
            DraftPageProvenance::SelectedCommitted { .. } => usize::MAX,
        });
        let validation = (|| {
            let mut previous_slot = None;
            for (index, location) in ordered_locations[..len].iter().copied().enumerate() {
                let DraftPageProvenance::Private { page, .. } = location.provenance else {
                    return Err(FreeBitmapCowError::StaleReservationPredecessor);
                };
                if previous_slot.is_some_and(|previous| page.slot <= previous)
                    || self.slot_to_record.get(page.slot).copied() != Some(location.record_index)
                {
                    return Err(FreeBitmapCowError::StaleReservationPredecessor);
                }
                let record = self
                    .records
                    .get(location.record_index)
                    .and_then(Option::as_ref)
                    .ok_or(FreeBitmapCowError::StaleReservationPredecessor)?;
                pool_returns[index] = record.validate_private_return(location)?;
                source
                    .validate_private_location_detach(location)
                    .map_err(|_| FreeBitmapCowError::StaleReservationPredecessor)?;
                previous_slot = Some(page.slot);
            }
            source.check_access().map_err(FreeBitmapCowError::Source)?;
            Ok::<(), FreeBitmapCowError>(())
        })();
        if let Err(error) = validation {
            ordered_locations.fill(DraftPrivatePageLocation::EMPTY);
            pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
            return Err(error);
        }
        let fence = match source.pool.preflight_coordinator_prior_returns(
            prepared_scope,
            terminal,
            &pool_returns[..len],
        ) {
            Ok(fence) => fence,
            Err(error) => {
                ordered_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                return Err(FreeBitmapCowError::PrivatePool(error));
            }
        };
        let pool_plan = source
            .pool
            .seal_coordinator_prior_returns_preflighted(fence, &mut pool_returns[..len]);
        Ok(FixedPointPreparedPriorReturns {
            ledger: self,
            source,
            locations: &mut ordered_locations[..len],
            pool_plan,
        })
    }

    #[cfg(test)]
    pub(crate) fn return_prior_private<S: CommittedPageSource + ?Sized>(
        &mut self,
        pgno: u32,
        source: &FixedPointDraftSource<'_, '_, 'slots, S>,
    ) -> Result<(), FreeBitmapCowError> {
        let location = source
            .private_location(pgno)
            .map_err(|_| FreeBitmapCowError::StaleReservationPredecessor)?
            .ok_or(FreeBitmapCowError::StaleReservationPredecessor)?;
        self.return_prior_private_location(location, source)
    }

    #[cfg(test)]
    pub(crate) fn return_prior_private_location<S: CommittedPageSource + ?Sized>(
        &mut self,
        location: DraftPrivatePageLocation,
        source: &FixedPointDraftSource<'_, '_, 'slots, S>,
    ) -> Result<(), FreeBitmapCowError> {
        let DraftPageProvenance::Private { page, .. } = location.provenance else {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        };
        if self.slot_to_record.get(page.slot).copied() != Some(location.record_index) {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        let record = self
            .records
            .get_mut(location.record_index)
            .and_then(Option::as_mut)
            .ok_or(FreeBitmapCowError::StaleReservationPredecessor)?;
        record.return_private_page(location)?;
        source
            .unregister_private_page(location)
            .map_err(|_| FreeBitmapCowError::StaleReservationPredecessor)?;
        self.slot_to_record[page.slot] = usize::MAX;
        Ok(())
    }
}

impl<S: CommittedPageSource + ?Sized> CommittedPageSource for FixedPointDraftSource<'_, '_, '_, S> {
    fn check_access(&self) -> Result<(), PageSourceError> {
        self.committed.check_access()
    }

    fn read_page(
        &self,
        pgno: u32,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<(), PageSourceError> {
        self.read_page_with_provenance(pgno, destination)
            .map(|_| ())
    }
}

impl<
        'ledger,
        'records,
        'record_scratch,
        'record_pool,
        'slots,
        'source,
        'committed,
        'source_index,
        'locations,
        'plan,
        S: CommittedPageSource + ?Sized,
    >
    FixedPointPreparedPriorReturns<
        'ledger,
        'records,
        'record_scratch,
        'record_pool,
        'slots,
        'source,
        'committed,
        'source_index,
        'locations,
        'plan,
        S,
    >
{
    fn cancel(self) {
        self.locations.fill(DraftPrivatePageLocation::EMPTY);
        self.pool_plan
            .into_returns()
            .fill(PrivatePageCoordinatorPriorReturn::empty());
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)] // Failure retains the complete move-only prepared journal.
    pub(crate) fn apply(
        self,
        work: &PrivatePageCoordinatorWork,
    ) -> Result<usize, (Self, FreeBitmapCowError)> {
        let Self {
            ledger,
            source,
            locations,
            pool_plan,
        } = self;
        let pool_plan = match source
            .pool
            .apply_coordinator_prior_returns_prepared(work, pool_plan)
        {
            Ok(plan) => plan,
            Err((pool_plan, error)) => {
                return Err((
                    Self {
                        ledger,
                        source,
                        locations,
                        pool_plan,
                    },
                    FreeBitmapCowError::PrivatePool(error),
                ));
            }
        };
        let count = locations.len();
        for location in locations.iter().copied() {
            let DraftPageProvenance::Private { page, .. } = location.provenance else {
                unreachable!("prepared prior return retains private provenance");
            };
            let record = ledger.records[location.record_index]
                .as_mut()
                .expect("prepared prior return retains the exact owning record");
            record.apply_private_return_terminal_prepared(location);
            source.detach_private_location_terminal_prepared(location);
            ledger.slot_to_record[page.slot] = usize::MAX;
        }
        locations.fill(DraftPrivatePageLocation::EMPTY);
        pool_plan
            .into_returns()
            .fill(PrivatePageCoordinatorPriorReturn::empty());
        Ok(count)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct FixedPointCarriedSource {
    pub(crate) identity: u64,
    pub(crate) ordinal: u64,
    pub(crate) last_pgno: u32,
    pub(crate) epoch: u64,
}

impl FixedPointCarriedSource {
    pub(crate) const EMPTY: Self = Self {
        identity: 0,
        ordinal: 0,
        last_pgno: 0,
        epoch: 0,
    };
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct FixedPointPreparedOutput {
    pub(crate) root: u32,
    pub(crate) pending_page_count: u64,
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) struct FixedPointPreparedWorkSlot {
    address: usize,
    coordinator_identity: usize,
    predecessor_generation: u64,
    predecessor_nonce: u64,
    work_identity: u64,
    pool_fence: Option<PrivatePageCoordinatorFence>,
    output: FixedPointPreparedOutput,
    carried: FixedPointCarriedSource,
    carried_address: usize,
    carried_len: usize,
    carried_seal: u64,
    scratch_address: usize,
    scratch_len: usize,
    scratch_seal: u64,
    seal: u64,
}

impl FixedPointPreparedWorkSlot {
    pub(crate) const fn empty() -> Self {
        Self {
            address: 0,
            coordinator_identity: 0,
            predecessor_generation: 0,
            predecessor_nonce: 0,
            work_identity: 0,
            pool_fence: None,
            output: FixedPointPreparedOutput {
                root: 0,
                pending_page_count: 0,
            },
            carried: FixedPointCarriedSource::EMPTY,
            carried_address: 0,
            carried_len: 0,
            carried_seal: 0,
            scratch_address: 0,
            scratch_len: 0,
            scratch_seal: 0,
            seal: 0,
        }
    }

    fn clear(&mut self) {
        *self = Self::empty();
    }
}

#[derive(Debug)]
pub(crate) struct FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried> {
    slot: &'slot mut FixedPointPreparedWorkSlot,
    scope: PrivatePagePreparedScopeReservation<'scope_slot>,
    scratch: &'scratch mut [u8],
    carried_pages: &'carried [u32],
}

struct FixedPointPreparedAggregateBase<'slot, 'scratch, 'carried> {
    slot: &'slot mut FixedPointPreparedWorkSlot,
    scratch: &'scratch mut [u8],
    carried_pages: &'carried [u32],
    predecessor_generation: u64,
    predecessor_nonce: u64,
    work_identity: u64,
    output: FixedPointPreparedOutput,
    carried: FixedPointCarriedSource,
    next_predecessor_generation: u64,
    next_predecessor_nonce: u64,
    next_global_epoch: u64,
    coordinator_epoch: u64,
    pool_fence: PrivatePageCoordinatorFence,
}

#[derive(Clone, Copy)]
struct FixedPointPreparedAggregateFacts {
    predecessor_generation: u64,
    predecessor_nonce: u64,
    work_identity: u64,
    output: FixedPointPreparedOutput,
    carried: FixedPointCarriedSource,
    next_predecessor_generation: u64,
    next_predecessor_nonce: u64,
    next_global_epoch: u64,
    coordinator_epoch: u64,
    pool_fence: PrivatePageCoordinatorFence,
}

#[derive(Debug)]
pub(crate) struct FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan> {
    prepared: FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>,
    terminal: PrivatePagePreparedCoordinatorTerminal<'plan>,
}

#[cfg(test)]
pub(crate) struct FixedPointPreparedRetirementTerminalWork<
    'slot,
    'scope_slot,
    'scratch,
    'carried,
    'plan,
> {
    terminal: FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
    result: RetirementTreeEditResult,
}

pub(crate) struct FixedPointPreparedProducedTerminalWork<
    'slot,
    'scope_slot,
    'scratch,
    'carried,
    'plan,
    B,
> {
    terminal: FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
    retirement: RetirementTreeEditResult,
    bitmap: B,
}

struct FixedPointPreparedWorkspaceRecord<'backing, 'arena, 'cleanup> {
    slot: &'backing FixedPointWorkspaceRecordSlot<'arena, 'cleanup>,
    generation: u64,
}

impl FixedPointPreparedWorkspaceRecord<'_, '_, '_> {
    fn publish(&self) {
        self.slot
            .state
            .set(FixedPointWorkspaceRecordState::Live(self.generation));
    }
}

impl Drop for FixedPointPreparedWorkspaceRecord<'_, '_, '_> {
    fn drop(&mut self) {
        if self.slot.state.get() != FixedPointWorkspaceRecordState::Prepared(self.generation) {
            return;
        }
        self.slot.state.set(FixedPointWorkspaceRecordState::Vacant);
        if let Some(record) = self.slot.record.replace(None) {
            self.slot.scratch.set(Some(record.cancel_into_scratch()));
        }
    }
}

struct FixedPointPreparedWorkspaceReplay<
    'session,
    'backing,
    'arena,
    'cleanup,
    'pool,
    'slots,
    'committed,
    'prior_locations,
    'new_locations,
    'returns,
    S: CommittedPageSource + ?Sized,
> {
    session: FixedPointCoordinatorSession<
        'session,
        'backing,
        'arena,
        'cleanup,
        'pool,
        'slots,
        'committed,
        S,
    >,
    prior_locations: &'prior_locations mut [DraftPrivatePageLocation],
    new_locations: &'new_locations mut [DraftPrivatePageLocation],
    returns: &'returns mut [PrivatePageCoordinatorPriorReturn],
    record_index: usize,
    next_len: usize,
    next_revision: u64,
    next_digest: u64,
    record: FixedPointPreparedWorkspaceRecord<'backing, 'arena, 'cleanup>,
}

impl<S: CommittedPageSource + ?Sized>
    FixedPointPreparedWorkspaceReplay<'_, '_, '_, '_, '_, '_, '_, '_, '_, '_, S>
{
    fn clear_scratch(&mut self) {
        self.prior_locations.fill(DraftPrivatePageLocation::EMPTY);
        self.new_locations.fill(DraftPrivatePageLocation::EMPTY);
        self.returns
            .fill(PrivatePageCoordinatorPriorReturn::empty());
    }

    fn replay(&mut self) {
        for write in self.session.tombstone_writes.iter() {
            write.destination.set(write.value);
        }
        for write in self.session.source_writes.iter() {
            write.destination.set(write.value);
        }
        for write in self.session.map_writes.iter() {
            write.destination.set(write.value);
        }
        self.session.backing.len.set(self.next_len);
        self.session
            .backing
            .last_work_unit
            .set(self.record.generation);
        self.session.backing.revision.set(self.next_revision);
        self.session.backing.digest.set(self.next_digest);
        self.clear_scratch();
        self.record.publish();
    }
}

impl<S: CommittedPageSource + ?Sized> Drop
    for FixedPointPreparedWorkspaceReplay<'_, '_, '_, '_, '_, '_, '_, '_, '_, '_, S>
{
    fn drop(&mut self) {
        self.clear_scratch();
        self.session.reset_journal();
    }
}

pub(crate) struct FixedPointPreparedAggregateWork<
    'slot,
    'scratch,
    'carried,
    'pool,
    'slots,
    'plan,
    'pool_replay,
    'session,
    'backing,
    'record_arena,
    'record_cleanup,
    'committed,
    'prior_locations,
    'new_locations,
    'returns,
    S: CommittedPageSource + ?Sized,
    B,
> {
    base: FixedPointPreparedAggregateBase<'slot, 'scratch, 'carried>,
    terminal: PrivatePagePreparedCoordinatorTerminal<'plan>,
    retirement: RetirementTreeEditResult,
    bitmap: B,
    workspace_identity: usize,
    pool_replay: PrivatePagePreparedSparseReplay<'pool, 'slots, 'pool_replay>,
    workspace_replay: FixedPointPreparedWorkspaceReplay<
        'session,
        'backing,
        'record_arena,
        'record_cleanup,
        'pool,
        'slots,
        'committed,
        'prior_locations,
        'new_locations,
        'returns,
        S,
    >,
}

pub(crate) struct FixedPointSealedAggregateWork<'record_arena, 'record_cleanup, 'slots> {
    active: FixedPointActiveWork<'slots>,
    retirement: RetirementTreeEditResult,
    record_index: usize,
    nonce: u64,
    _record: core::marker::PhantomData<
        PreparedFreeBitmapCoordinatorRecord<'record_arena, 'record_cleanup>,
    >,
}

/// The only successor emitted after a sealed aggregate has been accepted into
/// its transaction-owned canonical record. Terminal page bytes remain private
/// to that record and the pool; no producer output escapes this handoff.
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct FixedPointAggregateCompletion {
    predecessor: FixedPointPredecessor,
    retirement: RetirementTreeEditResult,
}

impl FixedPointAggregateCompletion {
    pub(crate) fn into_parts(self) -> (FixedPointPredecessor, RetirementTreeEditResult) {
        (self.predecessor, self.retirement)
    }
}

pub(crate) trait FixedPointPreparedAggregateAuthority: Sized {
    type Sealed;

    fn work_identity(&self) -> u64;
    fn work_generation(&self) -> u64;
    fn workspace_identity(&self) -> usize;
    fn preflight_authority(
        &self,
        coordinator: &FixedPointCoordinator,
        predecessor: &FixedPointPredecessor,
    ) -> Result<(), FixedPointError>;
    fn execute_authority(
        self,
        coordinator: &FixedPointCoordinator,
        predecessor: FixedPointPredecessor,
    ) -> Self::Sealed;
}

impl<
        'slot,
        'scratch,
        'carried,
        'pool,
        'slots,
        'plan,
        'pool_replay,
        'session,
        'backing,
        'record_arena,
        'record_cleanup,
        'committed,
        'prior_locations,
        'new_locations,
        'returns,
        S: CommittedPageSource + ?Sized,
        B,
    >
    FixedPointPreparedAggregateWork<
        'slot,
        'scratch,
        'carried,
        'pool,
        'slots,
        'plan,
        'pool_replay,
        'session,
        'backing,
        'record_arena,
        'record_cleanup,
        'committed,
        'prior_locations,
        'new_locations,
        'returns,
        S,
        B,
    >
where
    'record_arena: 'backing,
{
    pub(crate) fn preflight_execute(
        &self,
        coordinator: &FixedPointCoordinator,
        predecessor: &FixedPointPredecessor,
    ) -> Result<(), FixedPointError> {
        if predecessor.generation != self.base.predecessor_generation
            || predecessor.nonce != self.base.predecessor_nonce
            || coordinator.validate_predecessor(predecessor).is_err()
            || coordinator.global_epoch.get() != self.base.coordinator_epoch
            || coordinator.registered_work.get() != 0
            || self.pool_replay.live_fence() != self.base.pool_fence
        {
            return Err(FixedPointError::StalePredecessor);
        }
        Ok(())
    }

    pub(crate) fn execute(
        self,
        coordinator: &FixedPointCoordinator,
        predecessor: FixedPointPredecessor,
    ) -> FixedPointSealedAggregateWork<'record_arena, 'record_cleanup, 'slots> {
        let Self {
            base,
            terminal,
            retirement,
            bitmap,
            workspace_identity: _,
            pool_replay,
            mut workspace_replay,
        } = self;
        let nonce = terminal.nonce();
        // The producer authority is fully represented by the sealed terminal
        // journal now. Keeping it would retain a draft borrow after Active.
        drop(bitmap);
        let record_index = workspace_replay.record_index;
        coordinator.registered_work.set(base.work_identity);
        coordinator.predecessor_outstanding.set(false);
        coordinator
            .predecessor_generation
            .set(base.next_predecessor_generation);
        coordinator
            .predecessor_nonce
            .set(base.next_predecessor_nonce);
        coordinator.last_work_identity.set(base.work_identity);
        coordinator.global_epoch.set(base.next_global_epoch);
        base.slot.clear();
        base.scratch.fill(0);
        let (pool_work, scope, _pool) = pool_replay.replay();
        workspace_replay.replay();
        let active = FixedPointActiveWork {
            coordinator_identity: coordinator.identity,
            predecessor_generation: predecessor.generation,
            work_identity: base.work_identity,
            output: base.output,
            carried: base.carried,
            pool_work,
            scope,
        };
        FixedPointSealedAggregateWork {
            active,
            retirement,
            record_index,
            nonce,
            _record: core::marker::PhantomData,
        }
    }
}

impl<
        'slot,
        'scratch,
        'carried,
        'pool,
        'slots,
        'plan,
        'pool_replay,
        'session,
        'backing,
        'record_arena,
        'record_cleanup,
        'committed,
        'prior_locations,
        'new_locations,
        'returns,
        S: CommittedPageSource + ?Sized,
        B,
    > FixedPointPreparedAggregateAuthority
    for FixedPointPreparedAggregateWork<
        'slot,
        'scratch,
        'carried,
        'pool,
        'slots,
        'plan,
        'pool_replay,
        'session,
        'backing,
        'record_arena,
        'record_cleanup,
        'committed,
        'prior_locations,
        'new_locations,
        'returns,
        S,
        B,
    >
where
    'record_arena: 'backing,
{
    type Sealed = FixedPointSealedAggregateWork<'record_arena, 'record_cleanup, 'slots>;

    fn work_identity(&self) -> u64 {
        self.base.work_identity
    }

    fn work_generation(&self) -> u64 {
        self.pool_replay.work_generation()
    }

    fn workspace_identity(&self) -> usize {
        self.workspace_identity
    }

    fn preflight_authority(
        &self,
        coordinator: &FixedPointCoordinator,
        predecessor: &FixedPointPredecessor,
    ) -> Result<(), FixedPointError> {
        self.preflight_execute(coordinator, predecessor)
    }

    fn execute_authority(
        self,
        coordinator: &FixedPointCoordinator,
        predecessor: FixedPointPredecessor,
    ) -> Self::Sealed {
        self.execute(coordinator, predecessor)
    }
}

impl<'record_arena, 'record_cleanup, 'slots>
    FixedPointSealedAggregateWork<'record_arena, 'record_cleanup, 'slots>
{
    pub(crate) fn finish(
        self,
        coordinator: &FixedPointCoordinator,
        pool: &PrivatePagePool<'slots>,
        workspace: &FixedPointCoordinatorWorkspace<'_, 'record_arena, 'record_cleanup>,
    ) -> Result<FixedPointAggregateCompletion, FixedPointError> {
        let Self {
            active,
            retirement,
            record_index,
            nonce,
            _record: _,
        } = self;
        if (retirement.root == 0) != (retirement.batch_count == 0)
            || (retirement.root != 0
                && (retirement.root < 2
                    || u64::from(retirement.root) >= active.output.pending_page_count))
        {
            return Err(FixedPointError::StalePredecessor);
        }
        workspace.validate_live_record_handoff(
            pool,
            record_index,
            &active.scope,
            active.work_identity,
            active.output.root,
            active.output.pending_page_count,
        )?;
        let predecessor = coordinator.complete_sealed_work(pool, active, nonce)?;
        Ok(FixedPointAggregateCompletion {
            predecessor,
            retirement,
        })
    }

    #[cfg(test)]
    pub(crate) const fn record_index(&self) -> usize {
        self.record_index
    }

    #[cfg(test)]
    pub(crate) const fn retirement_result(&self) -> RetirementTreeEditResult {
        self.retirement
    }
}

impl<'slot, 'scope_slot, 'scratch, 'carried, 'plan, B>
    FixedPointPreparedProducedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan, B>
{
    pub(crate) const fn new(
        terminal: FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
        retirement: RetirementTreeEditResult,
        bitmap: B,
    ) -> Self {
        Self {
            terminal,
            retirement,
            bitmap,
        }
    }

    #[allow(
        clippy::too_many_arguments,
        clippy::type_complexity,
        clippy::result_large_err
    )]
    pub(crate) fn prepare_aggregate<
        'session,
        'backing,
        'pool,
        'slots,
        'pool_replay,
        'committed,
        'prior_locations,
        'new_locations,
        'returns,
        'arena,
        'cleanup,
        S: CommittedPageSource + ?Sized,
    >(
        self,
        coordinator: &FixedPointCoordinator,
        predecessor: &FixedPointPredecessor,
        pool: &'pool PrivatePagePool<'slots>,
        mut session: FixedPointCoordinatorSession<
            'session,
            'backing,
            'arena,
            'cleanup,
            'pool,
            'slots,
            'committed,
            S,
        >,
        requested_prior_returns: &[DraftPrivatePageLocation],
        ordered_prior_locations: &'prior_locations mut [DraftPrivatePageLocation],
        pool_returns: &'returns mut [PrivatePageCoordinatorPriorReturn],
        record_index: usize,
        new_locations: &'new_locations mut [DraftPrivatePageLocation],
        replay_slots: &'pool_replay mut [PrivatePageSparseReplaySlot],
        replay_index: &'pool_replay mut [PrivatePageSparseReplayIndex],
        workspace_identity: usize,
    ) -> Result<
        FixedPointPreparedAggregateWork<
            'slot,
            'scratch,
            'carried,
            'pool,
            'slots,
            'plan,
            'pool_replay,
            'session,
            'backing,
            'arena,
            'cleanup,
            'committed,
            'prior_locations,
            'new_locations,
            'returns,
            S,
            B,
        >,
        (Self, FixedPointError),
    >
    where
        'arena: 'backing,
    {
        if workspace_identity == 0 {
            return Err((self, FixedPointError::InvalidArgument));
        }
        session.reset_journal();
        let facts =
            match self
                .terminal
                .prepared
                .preflight_aggregate_base(coordinator, predecessor, pool)
            {
                Ok(facts) => facts,
                Err(error) => return Err((self, error)),
            };
        let backing = session.backing;
        let record_slot = match backing.records.get(record_index) {
            Some(slot)
                if core::ptr::eq(session.pool, pool)
                    && backing.len.get() == record_index
                    && slot.state.get() == FixedPointWorkspaceRecordState::Vacant
                    && facts.work_identity > backing.last_work_unit.get() =>
            {
                slot
            }
            _ => return Err((self, FixedPointError::InvalidWorkUnit)),
        };
        if let Some(record) = record_slot.record.replace(None) {
            record_slot.record.set(Some(record));
            return Err((self, FixedPointError::InvalidWorkUnit));
        }
        let terminal = &self.terminal.terminal;
        let terminal_page_count = terminal.pages().len();
        if new_locations.len() != terminal_page_count
            || new_locations
                .iter()
                .any(|location| *location != DraftPrivatePageLocation::EMPTY)
        {
            return Err((
                self,
                FixedPointError::SourceScratchTooSmall {
                    required: terminal_page_count,
                    actual: new_locations.len(),
                },
            ));
        }
        if ordered_prior_locations.len() < requested_prior_returns.len()
            || pool_returns.len() < requested_prior_returns.len()
            || ordered_prior_locations
                .iter()
                .any(|location| *location != DraftPrivatePageLocation::EMPTY)
            || pool_returns
                .iter()
                .any(|planned| *planned != PrivatePageCoordinatorPriorReturn::empty())
            || requested_prior_returns.len() + terminal_page_count
                > session.source_writes.capacity()
            || (requested_prior_returns.len() + terminal_page_count)
                .checked_mul(2)
                .map_or(true, |writes| writes > session.map_writes.capacity())
            || requested_prior_returns.len() > session.tombstone_writes.capacity()
        {
            return Err((
                self,
                FixedPointError::SourceScratchTooSmall {
                    required: requested_prior_returns.len() + terminal_page_count,
                    actual: session.source_writes.capacity(),
                },
            ));
        }
        let prior_len = requested_prior_returns.len();
        ordered_prior_locations[..prior_len].copy_from_slice(requested_prior_returns);
        ordered_prior_locations[..prior_len].sort_unstable_by_key(|location| {
            match location.provenance {
                DraftPageProvenance::Private { page, .. } => page.slot,
                DraftPageProvenance::SelectedCommitted { .. } => usize::MAX,
            }
        });
        let mut previous_slot = None;
        for (index, location) in ordered_prior_locations[..prior_len]
            .iter()
            .copied()
            .enumerate()
        {
            let DraftPageProvenance::Private { work_unit, page } = location.provenance else {
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                return Err((self, FixedPointError::StalePredecessor));
            };
            let Some(owner_slot) = backing.records.get(location.record_index) else {
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                return Err((self, FixedPointError::StalePredecessor));
            };
            if previous_slot.is_some_and(|previous| page.slot <= previous)
                || owner_slot.state.get() != FixedPointWorkspaceRecordState::Live(work_unit)
                || backing.slot_to_record.get(page.slot).map(Cell::get)
                    != Some(location.record_index)
                || backing.source_slot_to_entry.get(page.slot).map(Cell::get) != Some(page.slot)
            {
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                return Err((self, FixedPointError::StalePredecessor));
            }
            let expected = DraftPrivatePageEntry {
                work_unit,
                nonce: location.nonce,
                record_index: location.record_index,
                binding_index: location.binding_index,
                page,
            };
            if backing.source_entries.get(page.slot).map(Cell::get) != Some(Some(expected)) {
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                return Err((self, FixedPointError::StalePredecessor));
            }
            let Some(record) = owner_slot.record.replace(None) else {
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                return Err((self, FixedPointError::StalePredecessor));
            };
            let validated = record.validate_private_return(pool, location);
            let returned = record.returned_cell(location.binding_index);
            owner_slot.record.set(Some(record));
            let (Ok(planned), Some(returned)) = (validated, returned) else {
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                return Err((self, FixedPointError::StalePredecessor));
            };
            if returned.get() {
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                return Err((self, FixedPointError::StalePredecessor));
            }
            pool_returns[index] = planned;
            if let Err(error) = session.append_prior_return(
                returned,
                &backing.source_entries[page.slot],
                &backing.source_slot_to_entry[page.slot],
                &backing.slot_to_record[page.slot],
            ) {
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                session.reset_journal();
                return Err((self, error));
            }
            previous_slot = Some(page.slot);
        }
        if session.committed.check_access().is_err() {
            ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
            pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
            session.reset_journal();
            return Err((self, FixedPointError::StalePredecessor));
        }
        let prior_fence = match pool.preflight_coordinator_prior_returns(
            &self.terminal.prepared.scope,
            terminal,
            &pool_returns[..prior_len],
        ) {
            Ok(fence) => fence,
            Err(_) => {
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                session.reset_journal();
                return Err((self, FixedPointError::StalePredecessor));
            }
        };
        let prior_plan = pool.seal_coordinator_prior_returns_preflighted(
            prior_fence,
            &mut pool_returns[..prior_len],
        );
        let pool_replay = pool
            .prepare_sparse_coordinator_replay(
                &self.terminal.prepared.scope,
                terminal,
                &prior_plan,
                replay_slots,
                replay_index,
            )
            .map_err(|_| FixedPointError::StalePredecessor);
        let pool_replay = match pool_replay {
            Ok(replay) => replay,
            Err(error) => {
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                prior_plan
                    .into_returns()
                    .fill(PrivatePageCoordinatorPriorReturn::empty());
                session.reset_journal();
                return Err((self, error));
            }
        };
        for (binding_index, page) in terminal.pages().iter().enumerate() {
            let provenance = match pool_replay.future_sealed_page_provenance(page.pool_slot) {
                Ok(provenance) => provenance,
                Err(_) => {
                    new_locations.fill(DraftPrivatePageLocation::EMPTY);
                    pool_replay.cancel();
                    ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                    pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                    session.reset_journal();
                    return Err((self, FixedPointError::StalePredecessor));
                }
            };
            let Some(source_entry) = backing.source_entries.get(provenance.slot) else {
                new_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_replay.cancel();
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                session.reset_journal();
                return Err((self, FixedPointError::StalePredecessor));
            };
            let Some(source_map) = backing.source_slot_to_entry.get(provenance.slot) else {
                new_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_replay.cancel();
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                session.reset_journal();
                return Err((self, FixedPointError::StalePredecessor));
            };
            let Some(record_map) = backing.slot_to_record.get(provenance.slot) else {
                new_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_replay.cancel();
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                session.reset_journal();
                return Err((self, FixedPointError::StalePredecessor));
            };
            if source_entry.get().is_some()
                || source_map.get() != usize::MAX
                || record_map.get() != usize::MAX
            {
                new_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_replay.cancel();
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                session.reset_journal();
                return Err((self, FixedPointError::AdvertisedOwnedPage(provenance.pgno)));
            }
            let location = DraftPrivatePageLocation {
                provenance: DraftPageProvenance::Private {
                    work_unit: facts.work_identity,
                    page: provenance,
                },
                nonce: terminal.nonce(),
                record_index,
                binding_index,
            };
            new_locations[binding_index] = location;
            let entry = DraftPrivatePageEntry {
                work_unit: facts.work_identity,
                nonce: terminal.nonce(),
                record_index,
                binding_index,
                page: provenance,
            };
            if let Err(error) = session.append_new_binding(
                source_entry,
                entry,
                source_map,
                provenance.slot,
                record_map,
                record_index,
            ) {
                new_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_replay.cancel();
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                session.reset_journal();
                return Err((self, error));
            }
        }
        let Some(record_scratch) = record_slot.scratch.replace(None) else {
            new_locations.fill(DraftPrivatePageLocation::EMPTY);
            pool_replay.cancel();
            ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
            pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
            session.reset_journal();
            return Err((self, FixedPointError::StalePredecessor));
        };
        let record = PreparedFreeBitmapCoordinatorRecord::prepare_from_simulated_terminal(
            &pool_replay,
            terminal.nonce(),
            facts.work_identity,
            record_index,
            facts.output.root,
            facts.output.pending_page_count,
            terminal.pages(),
            record_scratch,
        )
        .map_err(|(scratch, _)| {
            record_slot.scratch.set(Some(scratch));
            FixedPointError::StalePredecessor
        });
        let record = match record {
            Ok(record) => record,
            Err(error) => {
                new_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_replay.cancel();
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                session.reset_journal();
                return Err((self, error));
            }
        };
        let next_len = match backing.len.get().checked_add(1) {
            Some(len) => len,
            None => {
                new_locations.fill(DraftPrivatePageLocation::EMPTY);
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                session.reset_journal();
                record_slot.scratch.set(Some(record.cancel_into_scratch()));
                pool_replay.cancel();
                return Err((self, FixedPointError::IdentityExhausted));
            }
        };
        let next_revision = match backing.revision.get().checked_add(1) {
            Some(revision) => revision,
            None => {
                new_locations.fill(DraftPrivatePageLocation::EMPTY);
                ordered_prior_locations.fill(DraftPrivatePageLocation::EMPTY);
                pool_returns.fill(PrivatePageCoordinatorPriorReturn::empty());
                session.reset_journal();
                record_slot.scratch.set(Some(record.cancel_into_scratch()));
                pool_replay.cancel();
                return Err((self, FixedPointError::IdentityExhausted));
            }
        };
        let mut next_digest = backing.digest.get() ^ facts.work_identity;
        for location in ordered_prior_locations[..prior_len]
            .iter()
            .chain(new_locations.iter())
        {
            if let DraftPageProvenance::Private { page, .. } = location.provenance {
                next_digest = next_digest
                    .rotate_left(7)
                    .wrapping_add(page.slot as u64)
                    .wrapping_add(page.binding_epoch);
            }
        }
        record_slot.record.set(Some(record));
        record_slot
            .state
            .set(FixedPointWorkspaceRecordState::Prepared(
                facts.work_identity,
            ));
        let record = FixedPointPreparedWorkspaceRecord {
            slot: record_slot,
            generation: facts.work_identity,
        };
        let Self {
            terminal: FixedPointPreparedTerminalWork { prepared, terminal },
            retirement,
            bitmap,
        } = self;
        let base = prepared.into_aggregate_base(facts);
        Ok(FixedPointPreparedAggregateWork {
            base,
            terminal,
            retirement,
            bitmap,
            workspace_identity,
            pool_replay,
            workspace_replay: FixedPointPreparedWorkspaceReplay {
                session,
                prior_locations: &mut ordered_prior_locations[..prior_len],
                new_locations,
                returns: &mut pool_returns[..prior_len],
                record_index,
                next_len,
                next_revision,
                next_digest,
                record,
            },
        })
    }
}

#[cfg(test)]
impl<'slot, 'scope_slot, 'scratch, 'carried, 'plan>
    FixedPointPreparedRetirementTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>
{
    pub(crate) const fn new(
        terminal: FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
        result: RetirementTreeEditResult,
    ) -> Self {
        Self { terminal, result }
    }
}

#[cfg(test)]
pub(crate) struct FixedPointPreparedCanonicalWork<
    'slot,
    'scope_slot,
    'scratch,
    'carried,
    'plan,
    'arena,
    'cleanup,
> {
    terminal: FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
    record_index: usize,
    record_scratch: SealedFreeBitmapCoordinatorScratch<'arena, 'cleanup>,
}

#[cfg(test)]
impl<'slot, 'scope_slot, 'scratch, 'carried, 'plan>
    FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>
{
    #[allow(clippy::result_large_err)]
    pub(crate) fn with_record_scratch<'arena, 'cleanup>(
        self,
        record_index: usize,
        scratch: SealedFreeBitmapCoordinatorScratch<'arena, 'cleanup>,
    ) -> Result<
        FixedPointPreparedCanonicalWork<
            'slot,
            'scope_slot,
            'scratch,
            'carried,
            'plan,
            'arena,
            'cleanup,
        >,
        (
            Self,
            SealedFreeBitmapCoordinatorScratch<'arena, 'cleanup>,
            FixedPointError,
        ),
    > {
        let required = self.terminal.pages().len();
        if !scratch.is_canonical_for(required) {
            let actual = scratch
                .arena_bindings
                .len()
                .min(scratch.index_nodes.len())
                .min(scratch.cleanup_targets.len());
            return Err((
                self,
                scratch,
                FixedPointError::SourceScratchTooSmall { required, actual },
            ));
        }
        Ok(FixedPointPreparedCanonicalWork {
            terminal: self,
            record_index,
            record_scratch: scratch,
        })
    }
}

impl<'slot, 'scope_slot, 'scratch, 'carried>
    FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>
{
    pub(crate) fn work_identity(&self) -> u64 {
        self.slot.work_identity
    }

    fn preflight_aggregate_base(
        &self,
        coordinator: &FixedPointCoordinator,
        predecessor: &FixedPointPredecessor,
        pool: &PrivatePagePool<'_>,
    ) -> Result<FixedPointPreparedAggregateFacts, FixedPointError> {
        if coordinator.validate_predecessor(predecessor).is_err() {
            return Err(FixedPointError::StalePredecessor);
        }
        let slot = &*self.slot;
        let address = slot as *const FixedPointPreparedWorkSlot as usize;
        let valid = slot.address == address
            && slot.coordinator_identity == coordinator.identity
            && slot.predecessor_generation == predecessor.generation
            && slot.predecessor_nonce == predecessor.nonce
            && slot.work_identity > coordinator.last_work_identity.get()
            && slot.pool_fence == Some(pool.coordinator_fence())
            && slot.carried_address == self.carried_pages.as_ptr() as usize
            && slot.carried_len == self.carried_pages.len()
            && slot.carried_seal == FixedPointCoordinator::carried_hash(self.carried_pages)
            && slot.scratch_address == self.scratch.as_ptr() as usize
            && slot.scratch_len == self.scratch.len()
            && slot.scratch_seal == FixedPointCoordinator::scratch_hash(self.scratch)
            && slot.seal != 0
            && slot.seal == FixedPointCoordinator::prepared_seal(slot);
        if !valid {
            return Err(FixedPointError::StalePredecessor);
        }
        let Some(next_predecessor_generation) =
            coordinator.predecessor_generation.get().checked_add(1)
        else {
            return Err(FixedPointError::IdentityExhausted);
        };
        let Some(next_predecessor_nonce) = coordinator.predecessor_nonce.get().checked_add(1)
        else {
            return Err(FixedPointError::IdentityExhausted);
        };
        let Some(next_global_epoch) = coordinator.global_epoch.get().checked_add(1) else {
            return Err(FixedPointError::IdentityExhausted);
        };
        if coordinator.sequence.get().checked_add(1).is_none() {
            return Err(FixedPointError::IdentityExhausted);
        }
        Ok(FixedPointPreparedAggregateFacts {
            predecessor_generation: predecessor.generation,
            predecessor_nonce: predecessor.nonce,
            work_identity: slot.work_identity,
            output: slot.output,
            carried: slot.carried,
            next_predecessor_generation,
            next_predecessor_nonce,
            next_global_epoch,
            coordinator_epoch: coordinator.global_epoch.get(),
            pool_fence: pool.coordinator_fence(),
        })
    }

    fn into_aggregate_base(
        self,
        facts: FixedPointPreparedAggregateFacts,
    ) -> FixedPointPreparedAggregateBase<'slot, 'scratch, 'carried> {
        let Self {
            slot,
            scope: _scope,
            scratch,
            carried_pages,
        } = self;
        FixedPointPreparedAggregateBase {
            slot,
            scratch,
            carried_pages,
            predecessor_generation: facts.predecessor_generation,
            predecessor_nonce: facts.predecessor_nonce,
            work_identity: facts.work_identity,
            output: facts.output,
            carried: facts.carried,
            next_predecessor_generation: facts.next_predecessor_generation,
            next_predecessor_nonce: facts.next_predecessor_nonce,
            next_global_epoch: facts.next_global_epoch,
            coordinator_epoch: facts.coordinator_epoch,
            pool_fence: facts.pool_fence,
        }
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn with_produced_terminal_export<'plan, B>(
        self,
        pool: &PrivatePagePool<'_>,
        export: PreparedProducedTerminalExport<'plan, B>,
        nonce: u64,
    ) -> Result<
        FixedPointPreparedProducedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan, B>,
        (
            Self,
            PreparedProducedTerminalExport<'plan, B>,
            FixedPointError,
        ),
    > {
        let (retirement, bitmap, pages, rebind) = export.into_bind_parts();
        let appended = pages
            .iter()
            .filter(|page| {
                page.authorization == crate::private_page_pool::PrivatePageAuthorization::Appended
            })
            .count();
        let expected_pending_page_count = pool
            .pending_page_count()
            .checked_add(u64::try_from(appended).unwrap_or(u64::MAX));
        if expected_pending_page_count != Some(self.slot.output.pending_page_count)
            || (self.slot.output.root != 0
                && !pages.iter().any(|page| {
                    page.pgno == self.slot.output.root
                        && page.owner == crate::private_page_pool::PrivatePageOwner::Bitmap
                }))
        {
            return Err((
                self,
                PreparedProducedTerminalExport::from_bind_parts(retirement, bitmap, pages, rebind),
                FixedPointError::StalePredecessor,
            ));
        }
        let terminal = match pool.prepare_unbound_coordinator_terminal(&self.scope, pages, nonce) {
            Ok(terminal) => terminal,
            Err((pages, _)) => {
                return Err((
                    self,
                    PreparedProducedTerminalExport::from_bind_parts(
                        retirement, bitmap, pages, rebind,
                    ),
                    FixedPointError::StalePredecessor,
                ));
            }
        };
        Ok(FixedPointPreparedProducedTerminalWork::new(
            FixedPointPreparedTerminalWork {
                prepared: self,
                terminal,
            },
            retirement,
            bitmap,
        ))
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)]
    pub(crate) fn with_terminal_pages<'plan>(
        self,
        pool: &PrivatePagePool<'_>,
        pages: &'plan [PrivatePageCoordinatorTerminalPage],
        nonce: u64,
    ) -> Result<
        FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
        (Self, FixedPointError),
    > {
        let terminal = match pool.prepare_coordinator_terminal(&self.scope, pages, nonce) {
            Ok(terminal) => terminal,
            Err(_) => return Err((self, FixedPointError::StalePredecessor)),
        };
        if terminal.pending_page_count() != self.slot.output.pending_page_count
            || (self.slot.output.root != 0
                && !pages.iter().any(|page| {
                    page.pgno == self.slot.output.root
                        && page.owner == crate::private_page_pool::PrivatePageOwner::Bitmap
                }))
        {
            return Err((self, FixedPointError::StalePredecessor));
        }
        Ok(FixedPointPreparedTerminalWork {
            prepared: self,
            terminal,
        })
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)]
    pub(crate) fn with_unbound_terminal_pages<'plan>(
        self,
        pool: &PrivatePagePool<'_>,
        pages: &'plan mut [PrivatePageCoordinatorTerminalPage],
        nonce: u64,
    ) -> Result<
        FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
        (
            Self,
            &'plan mut [PrivatePageCoordinatorTerminalPage],
            FixedPointError,
        ),
    > {
        let appended = pages
            .iter()
            .filter(|page| {
                page.authorization == crate::private_page_pool::PrivatePageAuthorization::Appended
            })
            .count();
        let expected_pending_page_count = pool
            .pending_page_count()
            .checked_add(u64::try_from(appended).unwrap_or(u64::MAX));
        if expected_pending_page_count != Some(self.slot.output.pending_page_count)
            || (self.slot.output.root != 0
                && !pages.iter().any(|page| {
                    page.pgno == self.slot.output.root
                        && page.owner == crate::private_page_pool::PrivatePageOwner::Bitmap
                }))
        {
            return Err((self, pages, FixedPointError::StalePredecessor));
        }
        let terminal = match pool.prepare_unbound_coordinator_terminal(&self.scope, pages, nonce) {
            Ok(terminal) => terminal,
            Err((pages, _)) => {
                return Err((self, pages, FixedPointError::StalePredecessor));
            }
        };
        debug_assert_eq!(
            terminal.pending_page_count(),
            self.slot.output.pending_page_count
        );
        Ok(FixedPointPreparedTerminalWork {
            prepared: self,
            terminal,
        })
    }
    #[cfg(test)]
    fn test_relocate_slot<'destination>(
        self,
        destination: &'destination mut FixedPointPreparedWorkSlot,
    ) -> Result<
        FixedPointPreparedWork<'destination, 'scope_slot, 'scratch, 'carried>,
        FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>,
    > {
        if *destination != FixedPointPreparedWorkSlot::empty() {
            return Err(self);
        }
        *destination = core::mem::replace(self.slot, FixedPointPreparedWorkSlot::empty());
        Ok(FixedPointPreparedWork {
            slot: destination,
            scope: self.scope,
            scratch: self.scratch,
            carried_pages: self.carried_pages,
        })
    }

    #[cfg(test)]
    fn test_forge_work_identity(&mut self) {
        self.slot.work_identity = self.slot.work_identity.saturating_add(1);
    }

    #[cfg(test)]
    fn test_corrupt_scope_address(&mut self) {
        self.scope.test_corrupt_address();
    }

    #[cfg(test)]
    fn test_force_scratch_alias(&mut self) {
        self.slot.scratch_address = self.slot.address;
        self.slot.scratch_len = core::mem::size_of::<FixedPointPreparedWorkSlot>();
        self.slot.seal = FixedPointCoordinator::prepared_seal(self.slot);
    }

    #[cfg(test)]
    fn test_force_carried_alias(&mut self) {
        self.slot.carried_address = self.slot.address;
        self.slot.carried_len = core::mem::size_of::<FixedPointPreparedWorkSlot>()
            .div_ceil(core::mem::size_of::<u32>());
        self.slot.seal = FixedPointCoordinator::prepared_seal(self.slot);
    }

    #[cfg(test)]
    fn test_corrupt_carried_content_seal(&mut self) {
        self.slot.carried_seal ^= 1;
        self.slot.seal = FixedPointCoordinator::prepared_seal(self.slot);
    }
}

#[derive(Debug)]
pub(crate) struct FixedPointActiveWork<'pool> {
    coordinator_identity: usize,
    predecessor_generation: u64,
    work_identity: u64,
    output: FixedPointPreparedOutput,
    carried: FixedPointCarriedSource,
    pool_work: PrivatePageCoordinatorWork,
    scope: PrivatePageReservationScope<'pool>,
}

#[derive(Debug)]
pub(crate) struct FixedPointSealedActiveWork<'pool, 'plan> {
    active: FixedPointActiveWork<'pool>,
    nonce: u64,
    pages: &'plan [PrivatePageCoordinatorTerminalPage],
}

impl<'pool> FixedPointSealedActiveWork<'pool, '_> {
    pub(crate) fn into_active(self) -> FixedPointActiveWork<'pool> {
        self.active
    }
}

pub(crate) struct FixedPointSealedRetirementWork<'pool, 'plan> {
    pub(crate) terminal: FixedPointSealedActiveWork<'pool, 'plan>,
    pub(crate) result: RetirementTreeEditResult,
}

#[cfg(test)]
pub(crate) struct FixedPointActiveTerminalWork<'scope, 'plan> {
    active: FixedPointActiveWork<'scope>,
    terminal: PrivatePagePreparedCoordinatorTerminal<'plan>,
}

#[cfg(test)]
impl FixedPointActiveTerminalWork<'_, '_> {
    pub(crate) fn work_authority(&self) -> &PrivatePageCoordinatorWork {
        &self.active.pool_work
    }
}

#[cfg(test)]
pub(crate) struct FixedPointSealedCanonicalWork<'cleanup, 'pool, 'slots> {
    active: FixedPointActiveWork<'slots>,
    pub(crate) record: SealedFreeBitmapCoordinatorRecord<'cleanup, 'pool, 'pool, 'slots>,
}

#[cfg(test)]
#[derive(Debug)]
#[allow(clippy::large_enum_variant)] // Retry must return every move-only authority without heap use.
pub(crate) enum FixedPointTerminalExecutionFailure<'slot, 'scope_slot, 'scratch, 'carried, 'plan> {
    Retry {
        predecessor: FixedPointPredecessor,
        prepared: FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
        error: FixedPointError,
    },
    AbortRequired,
}

#[cfg(test)]
#[allow(clippy::large_enum_variant)] // Retry must return every move-only authority without heap use.
pub(crate) enum FixedPointCanonicalExecutionFailure<
    'slot,
    'scope_slot,
    'scratch,
    'carried,
    'plan,
    'arena,
    'cleanup,
> {
    Retry {
        predecessor: FixedPointPredecessor,
        prepared: FixedPointPreparedCanonicalWork<
            'slot,
            'scope_slot,
            'scratch,
            'carried,
            'plan,
            'arena,
            'cleanup,
        >,
        error: FixedPointError,
    },
    AbortRequired,
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) struct FixedPointPredecessor {
    coordinator_identity: usize,
    sequence: u64,
    generation: u64,
    nonce: u64,
    selected_txn: u64,
    root: u32,
    pending_page_count: u64,
    carried: FixedPointCarriedSource,
}

impl FixedPointPredecessor {
    pub(crate) const fn sequence(&self) -> u64 {
        self.sequence
    }

    pub(crate) const fn root(&self) -> u32 {
        self.root
    }

    pub(crate) const fn pending_page_count(&self) -> u64 {
        self.pending_page_count
    }

    #[cfg(test)]
    fn test_duplicate(&self) -> Self {
        Self {
            coordinator_identity: self.coordinator_identity,
            sequence: self.sequence,
            generation: self.generation,
            nonce: self.nonce,
            selected_txn: self.selected_txn,
            root: self.root,
            pending_page_count: self.pending_page_count,
            carried: self.carried,
        }
    }
}

#[derive(Debug)]
pub(crate) struct FixedPointCoordinator {
    identity: usize,
    selected_txn: u64,
    root: Cell<u32>,
    pending_page_count: Cell<u64>,
    sequence: Cell<u64>,
    predecessor_generation: Cell<u64>,
    predecessor_nonce: Cell<u64>,
    carried: Cell<FixedPointCarriedSource>,
    last_work_identity: Cell<u64>,
    session_generation: Cell<u64>,
    global_epoch: Cell<u64>,
    registered_work: Cell<u64>,
    predecessor_outstanding: Cell<bool>,
    abort_required: Cell<bool>,
}

impl FixedPointCoordinator {
    pub(crate) fn new(
        selected_txn: u64,
        root: u32,
        pending_page_count: u64,
    ) -> Result<Self, FixedPointError> {
        if selected_txn == 0
            || !(2..=MAX_PAGE_COUNT).contains(&pending_page_count)
            || (root != 0 && (root < 2 || u64::from(root) >= pending_page_count))
        {
            return Err(FixedPointError::InvalidArgument);
        }
        let identity = NEXT_FIXED_POINT_IDENTITY
            .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |value| {
                value.checked_add(1)
            })
            .map_err(|_| FixedPointError::IdentityExhausted)?;
        Ok(Self {
            identity,
            selected_txn,
            root: Cell::new(root),
            pending_page_count: Cell::new(pending_page_count),
            sequence: Cell::new(0),
            predecessor_generation: Cell::new(1),
            predecessor_nonce: Cell::new(1),
            carried: Cell::new(FixedPointCarriedSource::EMPTY),
            last_work_identity: Cell::new(0),
            session_generation: Cell::new(0),
            global_epoch: Cell::new(1),
            registered_work: Cell::new(0),
            predecessor_outstanding: Cell::new(false),
            abort_required: Cell::new(false),
        })
    }

    pub(crate) fn requires_abort(&self) -> bool {
        self.abort_required.get()
    }

    pub(crate) fn registered_work_failed(&self) -> bool {
        if self.registered_work.get() == 0 {
            return false;
        }
        self.abort_required.set(true);
        true
    }

    pub(crate) fn registered_work(&self) -> u64 {
        self.registered_work.get()
    }

    #[cfg(test)]
    fn test_set_sequence(&self, sequence: u64) {
        self.sequence.set(sequence);
    }

    pub(crate) fn attach_pool(&self, pool: &PrivatePagePool<'_>) -> Result<(), FixedPointError> {
        if self.session_generation.get() != 0 {
            return Err(FixedPointError::InvalidArgument);
        }
        let expected_pending_txn = self
            .selected_txn
            .checked_add(1)
            .ok_or(FixedPointError::IdentityExhausted)?;
        if pool.pending_txn() != expected_pending_txn
            || pool.committed_page_count() != self.pending_page_count.get()
            || pool.pending_page_count() != self.pending_page_count.get()
        {
            return Err(FixedPointError::StalePredecessor);
        }
        let session_identity =
            u64::try_from(self.identity).map_err(|_| FixedPointError::IdentityExhausted)?;
        let generation = pool
            .register_coordinator_session(session_identity)
            .map_err(|_| FixedPointError::StalePredecessor)?;
        self.session_generation.set(generation);
        Ok(())
    }

    pub(crate) fn is_quiescent(&self) -> bool {
        !self.predecessor_outstanding.get()
            && self.registered_work.get() == 0
            && !self.abort_required.get()
    }

    pub(crate) fn commit_fence(
        &self,
        pool: &PrivatePagePool<'_>,
        expected_root: u32,
        expected_pending_page_count: u64,
    ) -> Result<(), FixedPointError> {
        let session_identity =
            u64::try_from(self.identity).map_err(|_| FixedPointError::IdentityExhausted)?;
        let (_, pool_work_generation, _) = pool.coordinator_registered_work();
        if pool
            .validate_coordinator_session(session_identity, self.session_generation.get())
            .is_err()
            || pool_work_generation.checked_add(1) != Some(self.global_epoch.get())
            || !self.is_quiescent()
            || self.root.get() != expected_root
            || self.pending_page_count.get() != expected_pending_page_count
            || !(2..=MAX_PAGE_COUNT).contains(&self.pending_page_count.get())
            || (self.root.get() != 0
                && (self.root.get() < 2
                    || u64::from(self.root.get()) >= self.pending_page_count.get()))
        {
            return Err(FixedPointError::StalePredecessor);
        }
        Ok(())
    }

    pub(crate) fn predecessor(&self) -> Result<FixedPointPredecessor, FixedPointError> {
        if self.abort_required.get() {
            return Err(FixedPointError::AbortRequired);
        }
        if self.predecessor_outstanding.get() {
            return Err(FixedPointError::StalePredecessor);
        }
        let sequence = self
            .sequence
            .get()
            .checked_add(1)
            .ok_or(FixedPointError::IdentityExhausted)?;
        self.predecessor_outstanding.set(true);
        self.sequence.set(sequence);
        Ok(FixedPointPredecessor {
            coordinator_identity: self.identity,
            sequence,
            generation: self.predecessor_generation.get(),
            nonce: self.predecessor_nonce.get(),
            selected_txn: self.selected_txn,
            root: self.root.get(),
            pending_page_count: self.pending_page_count.get(),
            carried: self.carried.get(),
        })
    }

    fn validate_predecessor(
        &self,
        predecessor: &FixedPointPredecessor,
    ) -> Result<(), FixedPointError> {
        if self.abort_required.get() {
            return Err(FixedPointError::AbortRequired);
        }
        if !self.predecessor_outstanding.get()
            || predecessor.coordinator_identity != self.identity
            || predecessor.selected_txn != self.selected_txn
            || predecessor.sequence != self.sequence.get()
            || predecessor.generation != self.predecessor_generation.get()
            || predecessor.nonce != self.predecessor_nonce.get()
            || predecessor.root != self.root.get()
            || predecessor.pending_page_count != self.pending_page_count.get()
            || predecessor.carried != self.carried.get()
        {
            return Err(FixedPointError::StalePredecessor);
        }
        Ok(())
    }

    fn scratch_hash(bytes: &[u8]) -> u64 {
        let mut hash = 1_469_598_103_934_665_603u64;
        for &byte in bytes {
            hash ^= u64::from(byte);
            hash = hash.wrapping_mul(1_099_511_628_211);
        }
        hash
    }

    fn carried_hash(pages: &[u32]) -> u64 {
        let mut hash = 1_469_598_103_934_665_603u64;
        for &page in pages {
            hash ^= u64::from(page);
            hash = hash.wrapping_mul(1_099_511_628_211);
        }
        hash
    }

    fn ranges_overlap(left: (usize, usize), right: (usize, usize)) -> bool {
        left.0 < left.1 && right.0 < right.1 && left.0 < right.1 && right.0 < left.1
    }

    fn byte_range<T>(pointer: *const T, length: usize) -> Option<(usize, usize)> {
        let start = pointer as usize;
        let bytes = length.checked_mul(core::mem::size_of::<T>())?;
        Some((start, start.checked_add(bytes)?))
    }

    fn prepared_seal(slot: &FixedPointPreparedWorkSlot) -> u64 {
        let mut hash = 1_469_598_103_934_665_603u64;
        for value in [
            slot.address as u64,
            slot.coordinator_identity as u64,
            slot.predecessor_generation,
            slot.predecessor_nonce,
            slot.work_identity,
            slot.pool_fence.map_or(0, PrivatePageCoordinatorFence::seal),
            u64::from(slot.output.root),
            slot.output.pending_page_count,
            slot.carried.identity,
            slot.carried.ordinal,
            u64::from(slot.carried.last_pgno),
            slot.carried.epoch,
            slot.carried_address as u64,
            slot.carried_len as u64,
            slot.carried_seal,
            slot.scratch_address as u64,
            slot.scratch_len as u64,
            slot.scratch_seal,
        ] {
            hash ^= value;
            hash = hash.wrapping_mul(1_099_511_628_211);
        }
        hash
    }

    fn derive_carried(
        previous: FixedPointCarriedSource,
        identity: u64,
        epoch: u64,
        pages: &[u32],
    ) -> Result<FixedPointCarriedSource, FixedPointError> {
        if pages.is_empty() {
            return if identity == 0 && epoch == 0 {
                Ok(previous)
            } else {
                Err(FixedPointError::InvalidArgument)
            };
        }
        if identity == 0
            || epoch == 0
            || (previous.identity != 0
                && (identity != previous.identity || epoch != previous.epoch))
        {
            return Err(FixedPointError::InvalidArgument);
        }
        let mut last = previous.last_pgno;
        for &page in pages {
            if page < 2 || ((previous.ordinal != 0 || last != 0) && page <= last) {
                return Err(FixedPointError::SourceOrderRegression {
                    previous: last,
                    current: page,
                });
            }
            last = page;
        }
        let count = u64::try_from(pages.len()).map_err(|_| FixedPointError::IdentityExhausted)?;
        let ordinal = previous
            .ordinal
            .checked_add(count)
            .ok_or(FixedPointError::IdentityExhausted)?;
        Ok(FixedPointCarriedSource {
            identity,
            ordinal,
            last_pgno: last,
            epoch,
        })
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn prepare_work<'slot, 'scope_slot, 'scratch, F>(
        &self,
        predecessor: &FixedPointPredecessor,
        pool: &PrivatePagePool<'_>,
        work_identity: u64,
        scope_count: usize,
        slot: &'slot mut FixedPointPreparedWorkSlot,
        scope_slot: &'scope_slot mut PrivatePagePreparedScopeSlot,
        scratch: &'scratch mut [u8],
        final_callback: F,
    ) -> Result<FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'static>, FixedPointError>
    where
        F: FnOnce() -> Result<FixedPointPreparedOutput, FixedPointError>,
    {
        self.prepare_work_with_carried(
            predecessor,
            pool,
            work_identity,
            scope_count,
            slot,
            scope_slot,
            scratch,
            0,
            0,
            &[],
            final_callback,
        )
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn prepare_work_with_carried<'slot, 'scope_slot, 'scratch, 'carried, F>(
        &self,
        predecessor: &FixedPointPredecessor,
        pool: &PrivatePagePool<'_>,
        work_identity: u64,
        scope_count: usize,
        slot: &'slot mut FixedPointPreparedWorkSlot,
        scope_slot: &'scope_slot mut PrivatePagePreparedScopeSlot,
        scratch: &'scratch mut [u8],
        carried_identity: u64,
        carried_epoch: u64,
        carried_pages: &'carried [u32],
        final_callback: F,
    ) -> Result<FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>, FixedPointError>
    where
        F: FnOnce() -> Result<FixedPointPreparedOutput, FixedPointError>,
    {
        self.validate_predecessor(predecessor)?;
        if self.session_generation.get() == 0
            || work_identity == 0
            || work_identity <= self.last_work_identity.get()
            || *slot != FixedPointPreparedWorkSlot::empty()
            || scratch.iter().any(|&byte| byte != 0)
            || self.sequence.get().checked_add(1).is_none()
            || self.predecessor_generation.get().checked_add(1).is_none()
            || self.predecessor_nonce.get().checked_add(1).is_none()
            || self.global_epoch.get().checked_add(1).is_none()
        {
            return Err(FixedPointError::InvalidWorkUnit);
        }
        let slot_range = Self::byte_range(slot as *const FixedPointPreparedWorkSlot, 1)
            .ok_or(FixedPointError::InvalidWorkUnit)?;
        let scope_slot_range =
            Self::byte_range(scope_slot as *const PrivatePagePreparedScopeSlot, 1)
                .ok_or(FixedPointError::InvalidWorkUnit)?;
        let scratch_range = Self::byte_range(scratch.as_ptr(), scratch.len())
            .ok_or(FixedPointError::InvalidWorkUnit)?;
        let carried_range = Self::byte_range(carried_pages.as_ptr(), carried_pages.len())
            .ok_or(FixedPointError::InvalidWorkUnit)?;
        let carried_address = carried_pages.as_ptr() as usize;
        let carried_len = carried_pages.len();
        let carried_seal = Self::carried_hash(carried_pages);
        if Self::ranges_overlap(slot_range, scratch_range)
            || Self::ranges_overlap(scope_slot_range, scratch_range)
            || Self::ranges_overlap(slot_range, scope_slot_range)
            || Self::ranges_overlap(carried_range, slot_range)
            || Self::ranges_overlap(carried_range, scope_slot_range)
            || Self::ranges_overlap(carried_range, scratch_range)
        {
            return Err(FixedPointError::ScratchAlias);
        }
        let carried = Self::derive_carried(
            predecessor.carried,
            carried_identity,
            carried_epoch,
            carried_pages,
        )?;
        let pool_fence = pool.coordinator_fence();
        let session_identity =
            u64::try_from(self.identity).map_err(|_| FixedPointError::IdentityExhausted)?;
        let scope = pool
            .prepare_coordinator_scope(
                session_identity,
                self.session_generation.get(),
                work_identity,
                scope_count,
                scope_slot,
            )
            .map_err(|_| FixedPointError::StalePredecessor)?;
        let callback_isolation = match pool.isolate_callback_backing() {
            Ok(isolation) => isolation,
            Err(_) => {
                let _ = pool.cancel_prepared_coordinator_scope(scope);
                return Err(FixedPointError::StalePredecessor);
            }
        };
        let callback_result = final_callback();
        drop(callback_isolation);
        if pool.coordinator_fence() != pool_fence
            || self.validate_predecessor(predecessor).is_err()
            || scratch.iter().any(|&byte| byte != 0)
            || carried_pages.as_ptr() as usize != carried_address
            || carried_pages.len() != carried_len
            || Self::carried_hash(carried_pages) != carried_seal
        {
            let _ = pool.cancel_prepared_coordinator_scope(scope);
            return Err(FixedPointError::StalePredecessor);
        }
        let output = match callback_result {
            Ok(output) => output,
            Err(error) => {
                let _ = pool.cancel_prepared_coordinator_scope(scope);
                return Err(error);
            }
        };
        if output.pending_page_count < predecessor.pending_page_count
            || output.pending_page_count > MAX_PAGE_COUNT
        {
            let _ = pool.cancel_prepared_coordinator_scope(scope);
            return Err(FixedPointError::PageCountRegression {
                previous: predecessor.pending_page_count,
                current: output.pending_page_count,
            });
        }
        if output.root != 0
            && (output.root < 2 || u64::from(output.root) >= output.pending_page_count)
        {
            let _ = pool.cancel_prepared_coordinator_scope(scope);
            return Err(FixedPointError::RootOutOfBounds(output.root));
        }
        let address = slot as *const FixedPointPreparedWorkSlot as usize;
        *slot = FixedPointPreparedWorkSlot {
            address,
            coordinator_identity: self.identity,
            predecessor_generation: predecessor.generation,
            predecessor_nonce: predecessor.nonce,
            work_identity,
            pool_fence: Some(pool_fence),
            output,
            carried,
            carried_address,
            carried_len,
            carried_seal,
            scratch_address: scratch.as_ptr() as usize,
            scratch_len: scratch.len(),
            scratch_seal: Self::scratch_hash(scratch),
            seal: 0,
        };
        slot.seal = Self::prepared_seal(slot);
        Ok(FixedPointPreparedWork {
            slot,
            scope,
            scratch,
            carried_pages,
        })
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)]
    pub(crate) fn execute_prepared<'slots, 'slot, 'scope_slot, 'scratch, 'carried>(
        &self,
        predecessor: FixedPointPredecessor,
        pool: &PrivatePagePool<'slots>,
        prepared: FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>,
    ) -> Result<
        FixedPointActiveWork<'slots>,
        (
            FixedPointPredecessor,
            FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>,
            FixedPointError,
        ),
    > {
        if let Err(error) = self.validate_predecessor(&predecessor) {
            return Err((predecessor, prepared, error));
        }
        let slot = &*prepared.slot;
        let address = slot as *const FixedPointPreparedWorkSlot as usize;
        let Some(slot_range) = Self::byte_range(slot as *const FixedPointPreparedWorkSlot, 1)
        else {
            return Err((predecessor, prepared, FixedPointError::StalePredecessor));
        };
        let Some(scope_slot_range) = prepared.scope.slot_range() else {
            return Err((predecessor, prepared, FixedPointError::StalePredecessor));
        };
        let Some(stored_scratch_range) =
            Self::byte_range(slot.scratch_address as *const u8, slot.scratch_len)
        else {
            return Err((predecessor, prepared, FixedPointError::StalePredecessor));
        };
        let Some(stored_carried_range) =
            Self::byte_range(slot.carried_address as *const u32, slot.carried_len)
        else {
            return Err((predecessor, prepared, FixedPointError::StalePredecessor));
        };
        if Self::ranges_overlap(slot_range, stored_scratch_range)
            || Self::ranges_overlap(scope_slot_range, stored_scratch_range)
            || Self::ranges_overlap(slot_range, scope_slot_range)
            || Self::ranges_overlap(stored_carried_range, slot_range)
            || Self::ranges_overlap(stored_carried_range, scope_slot_range)
            || Self::ranges_overlap(stored_carried_range, stored_scratch_range)
        {
            return Err((predecessor, prepared, FixedPointError::ScratchAlias));
        }
        if slot.address != address
            || slot.coordinator_identity != self.identity
            || slot.predecessor_generation != predecessor.generation
            || slot.predecessor_nonce != predecessor.nonce
            || slot.work_identity <= self.last_work_identity.get()
            || slot.pool_fence != Some(pool.coordinator_fence())
            || slot.carried_address != prepared.carried_pages.as_ptr() as usize
            || slot.carried_len != prepared.carried_pages.len()
            || slot.carried_seal != Self::carried_hash(prepared.carried_pages)
            || slot.scratch_address != prepared.scratch.as_ptr() as usize
            || slot.scratch_len != prepared.scratch.len()
            || slot.scratch_seal != Self::scratch_hash(prepared.scratch)
            || slot.seal == 0
            || slot.seal != Self::prepared_seal(slot)
        {
            return Err((predecessor, prepared, FixedPointError::StalePredecessor));
        }
        let output = slot.output;
        let carried = slot.carried;
        let work_identity = slot.work_identity;
        let Some(next_predecessor_generation) = self.predecessor_generation.get().checked_add(1)
        else {
            return Err((predecessor, prepared, FixedPointError::IdentityExhausted));
        };
        let Some(next_predecessor_nonce) = self.predecessor_nonce.get().checked_add(1) else {
            return Err((predecessor, prepared, FixedPointError::IdentityExhausted));
        };
        let Some(next_global_epoch) = self.global_epoch.get().checked_add(1) else {
            return Err((predecessor, prepared, FixedPointError::IdentityExhausted));
        };
        if self.sequence.get().checked_add(1).is_none() {
            return Err((predecessor, prepared, FixedPointError::IdentityExhausted));
        }
        self.registered_work.set(work_identity);
        let (pool_work, scope) = match pool.activate_prepared_coordinator_scope(prepared.scope) {
            Ok(authority) => authority,
            Err((scope, _error)) => {
                if pool.coordinator_work_phase()
                    == crate::private_page_pool::PrivatePageCoordinatorWorkPhase::None
                    && !pool.requires_abort()
                {
                    self.registered_work.set(0);
                } else {
                    self.abort_required.set(true);
                }
                let prepared = FixedPointPreparedWork {
                    slot: prepared.slot,
                    scope,
                    scratch: prepared.scratch,
                    carried_pages: prepared.carried_pages,
                };
                let error = if self.abort_required.get() {
                    FixedPointError::AbortRequired
                } else {
                    FixedPointError::StalePredecessor
                };
                return Err((predecessor, prepared, error));
            }
        };
        prepared.slot.clear();
        prepared.scratch.fill(0);
        self.predecessor_outstanding.set(false);
        self.predecessor_generation.set(next_predecessor_generation);
        self.predecessor_nonce.set(next_predecessor_nonce);
        self.last_work_identity.set(work_identity);
        self.global_epoch.set(next_global_epoch);
        Ok(FixedPointActiveWork {
            coordinator_identity: self.identity,
            predecessor_generation: predecessor.generation,
            work_identity,
            output,
            carried,
            pool_work,
            scope,
        })
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)] // Retry returns all move-only authorities without allocation.
    pub(crate) fn execute_terminal_prepared<
        'slots,
        'slot,
        'scope_slot,
        'scratch,
        'carried,
        'plan,
    >(
        &self,
        predecessor: FixedPointPredecessor,
        pool: &PrivatePagePool<'slots>,
        prepared: FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
    ) -> Result<
        FixedPointSealedActiveWork<'slots, 'plan>,
        FixedPointTerminalExecutionFailure<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
    > {
        let active = self.activate_terminal_prepared(predecessor, pool, prepared)?;
        match self.seal_active_terminal(pool, active) {
            Ok(sealed) => Ok(sealed),
            Err(_) => {
                self.abort_required.set(true);
                Err(FixedPointTerminalExecutionFailure::AbortRequired)
            }
        }
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)]
    pub(crate) fn execute_retirement_terminal_prepared<
        'slots,
        'slot,
        'scope_slot,
        'scratch,
        'carried,
        'plan,
    >(
        &self,
        predecessor: FixedPointPredecessor,
        pool: &PrivatePagePool<'slots>,
        prepared: FixedPointPreparedRetirementTerminalWork<
            'slot,
            'scope_slot,
            'scratch,
            'carried,
            'plan,
        >,
    ) -> Result<
        FixedPointSealedRetirementWork<'slots, 'plan>,
        (
            FixedPointTerminalExecutionFailure<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
            RetirementTreeEditResult,
        ),
    > {
        let FixedPointPreparedRetirementTerminalWork { terminal, result } = prepared;
        match self.execute_terminal_prepared(predecessor, pool, terminal) {
            Ok(terminal) => Ok(FixedPointSealedRetirementWork { terminal, result }),
            Err(error) => Err((error, result)),
        }
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)] // Retry returns all move-only authorities without allocation.
    pub(crate) fn activate_terminal_prepared<
        'slots,
        'slot,
        'scope_slot,
        'scratch,
        'carried,
        'plan,
    >(
        &self,
        predecessor: FixedPointPredecessor,
        pool: &PrivatePagePool<'slots>,
        prepared: FixedPointPreparedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
    ) -> Result<
        FixedPointActiveTerminalWork<'slots, 'plan>,
        FixedPointTerminalExecutionFailure<'slot, 'scope_slot, 'scratch, 'carried, 'plan>,
    > {
        let FixedPointPreparedTerminalWork {
            prepared: base,
            terminal,
        } = prepared;
        let active = match self.execute_prepared(predecessor, pool, base) {
            Ok(active) => active,
            Err((predecessor, base, error)) => {
                return Err(FixedPointTerminalExecutionFailure::Retry {
                    predecessor,
                    prepared: FixedPointPreparedTerminalWork {
                        prepared: base,
                        terminal,
                    },
                    error,
                });
            }
        };
        Ok(FixedPointActiveTerminalWork { active, terminal })
    }

    #[cfg(test)]
    fn seal_active_terminal<'slots, 'plan>(
        &self,
        pool: &PrivatePagePool<'slots>,
        active: FixedPointActiveTerminalWork<'slots, 'plan>,
    ) -> Result<FixedPointSealedActiveWork<'slots, 'plan>, FixedPointError> {
        let nonce = active.terminal.nonce();
        let pages = active.terminal.pages();
        let FixedPointActiveWork {
            coordinator_identity,
            predecessor_generation,
            work_identity,
            output,
            carried,
            pool_work,
            scope,
        } = active.active;
        let scope =
            match pool.apply_coordinator_terminal_prepared(&pool_work, scope, active.terminal) {
                Ok(scope) => scope,
                Err((_scope, _terminal, _error)) => {
                    return Err(FixedPointError::AbortRequired);
                }
            };
        Ok(FixedPointSealedActiveWork {
            active: FixedPointActiveWork {
                coordinator_identity,
                predecessor_generation,
                work_identity,
                output,
                carried,
                pool_work,
                scope,
            },
            nonce,
            pages,
        })
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)] // Retry returns all move-only authorities without allocation.
    pub(crate) fn execute_canonical_prepared<
        'pool,
        'slots,
        'slot,
        'scope_slot,
        'scratch,
        'carried,
        'plan,
        'cleanup,
    >(
        &self,
        predecessor: FixedPointPredecessor,
        pool: &'pool PrivatePagePool<'slots>,
        prepared: FixedPointPreparedCanonicalWork<
            'slot,
            'scope_slot,
            'scratch,
            'carried,
            'plan,
            'pool,
            'cleanup,
        >,
    ) -> Result<
        FixedPointSealedCanonicalWork<'cleanup, 'pool, 'slots>,
        FixedPointCanonicalExecutionFailure<
            'slot,
            'scope_slot,
            'scratch,
            'carried,
            'plan,
            'pool,
            'cleanup,
        >,
    > {
        let FixedPointPreparedCanonicalWork {
            terminal,
            record_index,
            record_scratch,
        } = prepared;
        let sealed = match self.execute_terminal_prepared(predecessor, pool, terminal) {
            Ok(sealed) => sealed,
            Err(FixedPointTerminalExecutionFailure::Retry {
                predecessor,
                prepared,
                error,
            }) => {
                return Err(FixedPointCanonicalExecutionFailure::Retry {
                    predecessor,
                    prepared: FixedPointPreparedCanonicalWork {
                        terminal: prepared,
                        record_index,
                        record_scratch,
                    },
                    error,
                });
            }
            Err(FixedPointTerminalExecutionFailure::AbortRequired) => {
                return Err(FixedPointCanonicalExecutionFailure::AbortRequired);
            }
        };
        let record = match SealedFreeBitmapCoordinatorRecord::from_coordinator_terminal(
            pool,
            sealed.active.scope.share(),
            sealed.nonce,
            sealed.active.work_identity,
            record_index,
            sealed.active.output.root,
            sealed.active.output.pending_page_count,
            sealed.pages,
            record_scratch,
        ) {
            Ok(record) => record,
            Err(_) => {
                self.abort_required.set(true);
                return Err(FixedPointCanonicalExecutionFailure::AbortRequired);
            }
        };
        Ok(FixedPointSealedCanonicalWork {
            active: sealed.active,
            record,
        })
    }

    /// Consumes one already-replayed aggregate work unit after its canonical
    /// record has been validated. A sealed-scope failure is not retryable: the
    /// private draft has already changed and must be discarded as a whole.
    pub(crate) fn complete_sealed_work<'pool>(
        &self,
        pool: &PrivatePagePool<'_>,
        active: FixedPointActiveWork<'pool>,
        nonce: u64,
    ) -> Result<FixedPointPredecessor, FixedPointError> {
        if self.abort_required.get()
            || active.coordinator_identity != self.identity
            || active.predecessor_generation.checked_add(1)
                != Some(self.predecessor_generation.get())
            || active.work_identity == 0
            || active.work_identity != self.registered_work.get()
            || nonce == 0
        {
            self.abort_required.set(true);
            return Err(FixedPointError::AbortRequired);
        }
        if pool
            .accept_sealed_coordinator_scope(&active.pool_work, &active.scope, nonce)
            .is_err()
        {
            self.abort_required.set(true);
            return Err(FixedPointError::AbortRequired);
        }
        let FixedPointActiveWork {
            output,
            carried,
            pool_work,
            scope: _,
            ..
        } = active;
        if pool.finish_coordinator_work(pool_work).is_err() {
            self.abort_required.set(true);
            return Err(FixedPointError::AbortRequired);
        }
        self.root.set(output.root);
        self.pending_page_count.set(output.pending_page_count);
        self.carried.set(carried);
        self.registered_work.set(0);
        match self.predecessor() {
            Ok(successor) => Ok(successor),
            Err(_) => {
                self.abort_required.set(true);
                Err(FixedPointError::AbortRequired)
            }
        }
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)]
    pub(crate) fn complete_work<'pool>(
        &self,
        pool: &PrivatePagePool<'_>,
        active: FixedPointActiveWork<'pool>,
    ) -> Result<FixedPointPredecessor, FixedPointError> {
        if self.abort_required.get()
            || active.coordinator_identity != self.identity
            || active.predecessor_generation.checked_add(1)
                != Some(self.predecessor_generation.get())
            || active.work_identity == 0
            || active.work_identity != self.registered_work.get()
        {
            return Err(FixedPointError::StalePredecessor);
        }
        if pool
            .accept_coordinator_scope(&active.pool_work, &active.scope)
            .is_err()
        {
            self.abort_required.set(true);
            return Err(FixedPointError::AbortRequired);
        }
        let scope_id = active.scope.coordinator_scope_id();
        if pool.close_scope(&active.scope).is_err()
            || pool
                .coordinator_scope_closed(&active.pool_work, scope_id)
                .is_err()
        {
            self.abort_required.set(true);
            return Err(FixedPointError::AbortRequired);
        }
        let FixedPointActiveWork {
            output,
            carried,
            pool_work,
            scope: _,
            ..
        } = active;
        if pool.finish_coordinator_work(pool_work).is_err() {
            self.abort_required.set(true);
            return Err(FixedPointError::AbortRequired);
        }
        self.root.set(output.root);
        self.pending_page_count.set(output.pending_page_count);
        self.carried.set(carried);
        self.registered_work.set(0);
        match self.predecessor() {
            Ok(successor) => Ok(successor),
            Err(error) => {
                self.abort_required.set(true);
                Err(error)
            }
        }
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn abort_active_work<'pool>(
        &self,
        pool: &PrivatePagePool<'_>,
        active: FixedPointActiveWork<'pool>,
    ) -> Result<(), FixedPointError> {
        if active.coordinator_identity != self.identity
            || active.work_identity != self.registered_work.get()
        {
            return Err(FixedPointError::StalePredecessor);
        }
        let FixedPointActiveWork {
            pool_work, scope, ..
        } = active;
        match pool.abort_coordinator_work(pool_work, scope) {
            Ok(()) => {
                self.abort_required.set(true);
                self.registered_work.set(0);
                Ok(())
            }
            Err((_pool_work, _scope, _)) => Err(FixedPointError::StalePredecessor),
        }
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn finish(
        &self,
        predecessor: FixedPointPredecessor,
    ) -> Result<(), (FixedPointPredecessor, FixedPointError)> {
        if let Err(error) = self.validate_predecessor(&predecessor) {
            return Err((predecessor, error));
        }
        self.predecessor_outstanding.set(false);
        Ok(())
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn reject_advertised_owned(
        &self,
        predecessor: FixedPointPredecessor,
        pgno: u32,
    ) -> Result<FixedPointPredecessor, (FixedPointPredecessor, FixedPointError)> {
        if let Err(error) = self.validate_predecessor(&predecessor) {
            return Err((predecessor, error));
        }
        self.abort_required.set(true);
        Err((predecessor, FixedPointError::AdvertisedOwnedPage(pgno)))
    }

    #[cfg(test)]
    fn test_new(selected_txn: u64, root: u32, pending_page_count: u64) -> Self {
        Self::new(selected_txn, root, pending_page_count).unwrap()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bitmap_cow::{BitmapCowArenaBinding, BitmapCowIndexNode};
    use crate::page::{self, PageHeader, PageType};
    use crate::private_page_pool::{
        PrivatePageAuthorization, PrivatePageOwner, PrivatePagePoolError, PrivatePagePoolSlot,
        PrivatePageSelectiveOverlayNode, PrivatePageSelectivePathEntry,
        PrivatePageSparseReplayIndex, PrivatePageSparseReplaySlot,
    };
    use crate::test_alloc::count_thread_allocations;
    use std::vec;

    fn prepared_output(root: u32, pending_page_count: u64) -> FixedPointPreparedOutput {
        FixedPointPreparedOutput {
            root,
            pending_page_count,
        }
    }

    fn bitmap_terminal_page(pool_slot: usize, pgno: u32) -> PrivatePageCoordinatorTerminalPage {
        let mut terminal = PrivatePageCoordinatorTerminalPage::empty();
        terminal.pool_slot = pool_slot;
        terminal.pgno = pgno;
        terminal.authorization = PrivatePageAuthorization::SafelyReclaimed;
        terminal.owner = PrivatePageOwner::Bitmap;
        terminal.owner_generation = 8;
        PageHeader {
            page_type: PageType::BitmapLeaf,
            born_txn: 8,
            item_count: 0,
            level: 0,
            lower: 4032,
            upper: 4096,
            aux: 1,
            page_crc32c: 0,
        }
        .encode_into(&mut terminal.bytes);
        page::write_crc32c(&mut terminal.bytes);
        terminal
    }

    #[test]
    fn terminal_page_journal_applies_only_after_work_registration() {
        let mut slots = [PrivatePagePoolSlot::empty(), PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 10, 10, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 10);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let prepared = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                2,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || Ok(prepared_output(5, 10)),
            )
            .unwrap();
        let mut pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        for (index, page) in pages.iter_mut().enumerate() {
            page.pgno = u32::try_from(index + 5).unwrap();
            page.authorization = PrivatePageAuthorization::SafelyReclaimed;
            page.owner = PrivatePageOwner::Bitmap;
            page.owner_generation = 8;
            PageHeader {
                page_type: PageType::BitmapLeaf,
                born_txn: 8,
                item_count: 0,
                level: 0,
                lower: 4032,
                upper: 4096,
                aux: 1,
                page_crc32c: 0,
            }
            .encode_into(&mut page.bytes);
            page::write_crc32c(&mut page.bytes);
        }
        let prepared = prepared
            .with_unbound_terminal_pages(&pool, &mut pages, 91)
            .unwrap();
        let sealed = coordinator
            .execute_terminal_prepared(predecessor, &pool, prepared)
            .unwrap();

        assert_eq!(
            pool.coordinator_work_phase(),
            crate::private_page_pool::PrivatePageCoordinatorWorkPhase::Sealed
        );
        assert_eq!(sealed.nonce, 91);
        assert_eq!(sealed.pages[0].pool_slot, 0);
        assert_eq!(sealed.pages[1].pool_slot, 1);
        for page in sealed.pages {
            let slot = pool.find_bound_page(page.pgno).unwrap().unwrap();
            assert_eq!(pool.test_bytes(slot).unwrap(), page.bytes);
        }
        coordinator.abort_active_work(&pool, sealed.active).unwrap();
    }

    #[test]
    fn sparse_terminal_replay_is_preborrowed_bounded_and_allocation_free_at_4096_slots() {
        let mut slots = vec![PrivatePagePoolSlot::empty(); 4096];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 10, 10, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 10);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let prepared = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                2,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || Ok(prepared_output(5, 10)),
            )
            .unwrap();
        let mut pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        pages[0] = bitmap_terminal_page(usize::MAX, 5);
        pages[1] = bitmap_terminal_page(usize::MAX, 6);
        let terminal = prepared
            .with_unbound_terminal_pages(&pool, &mut pages, 91)
            .unwrap();
        let FixedPointPreparedTerminalWork { prepared, terminal } = terminal;
        let mut replay_slots = vec![PrivatePageSparseReplaySlot::empty(); 32];
        let mut replay_index = vec![PrivatePageSparseReplayIndex::empty(); 4096];
        let mut undersized_index = vec![PrivatePageSparseReplayIndex::empty(); 4095];
        let mut rejected_returns = [];
        let fence = pool
            .preflight_coordinator_prior_returns(&prepared.scope, &terminal, &rejected_returns)
            .unwrap();
        let rejected_prior =
            pool.seal_coordinator_prior_returns_preflighted(fence, &mut rejected_returns);
        let before = pool.test_mutation_snapshot();
        let error = match pool.prepare_sparse_coordinator_replay(
            &prepared.scope,
            &terminal,
            &rejected_prior,
            &mut replay_slots,
            &mut undersized_index,
        ) {
            Ok(_) => panic!("undersized sparse index must reject before preparation"),
            Err(error) => error,
        };
        assert_eq!(
            error,
            PrivatePagePoolError::ReservationBudget {
                required: 4096,
                actual: 4095,
            }
        );
        assert_eq!(pool.test_mutation_snapshot(), before);
        assert!(replay_slots
            .iter()
            .all(|slot| *slot == PrivatePageSparseReplaySlot::empty()));
        assert!(undersized_index
            .iter()
            .all(|entry| *entry == PrivatePageSparseReplayIndex::empty()));
        for _ in 0..32 {
            let mut canceled_returns = [];
            let fence = pool
                .preflight_coordinator_prior_returns(&prepared.scope, &terminal, &canceled_returns)
                .unwrap();
            let prior =
                pool.seal_coordinator_prior_returns_preflighted(fence, &mut canceled_returns);
            let before = pool.test_mutation_snapshot();
            let (replay, allocations) = count_thread_allocations(|| {
                pool.prepare_sparse_coordinator_replay(
                    &prepared.scope,
                    &terminal,
                    &prior,
                    &mut replay_slots,
                    &mut replay_index,
                )
            });
            assert_eq!(allocations, 0);
            let replay = replay.unwrap();
            assert!(replay.touched_slots() <= 8);
            assert!(replay.index_visits() <= 128);
            drop(replay);
            assert!(replay_slots
                .iter()
                .all(|slot| *slot == PrivatePageSparseReplaySlot::empty()));
            assert!(replay_index
                .iter()
                .all(|entry| *entry == PrivatePageSparseReplayIndex::empty()));
            assert_eq!(
                pool.test_mutation_snapshot(),
                before,
                "preparation and cancellation must not alter live pool state"
            );
        }
        let mut returns = [];
        let fence = pool
            .preflight_coordinator_prior_returns(&prepared.scope, &terminal, &returns)
            .unwrap();
        let prior = pool.seal_coordinator_prior_returns_preflighted(fence, &mut returns);
        let (replay, allocations) = count_thread_allocations(|| {
            pool.prepare_sparse_coordinator_replay(
                &prepared.scope,
                &terminal,
                &prior,
                &mut replay_slots,
                &mut replay_index,
            )
        });
        assert_eq!(allocations, 0);
        let replay = replay.unwrap();
        assert!(
            replay.touched_slots() <= 8,
            "two terminal pages must not copy or scan the 4096-slot pool"
        );
        assert!(
            replay.index_visits() <= 128,
            "direct sparse indexing must remain bounded by simulated path visits"
        );
        let (_, scope, _) = replay.replay();
        assert!(replay_slots
            .iter()
            .all(|slot| *slot == PrivatePageSparseReplaySlot::empty()));
        assert!(replay_index
            .iter()
            .all(|entry| *entry == PrivatePageSparseReplayIndex::empty()));
        assert_eq!(pool.scoped_in_use(&scope).unwrap(), 2);
        assert_eq!(
            pool.coordinator_work_phase(),
            crate::private_page_pool::PrivatePageCoordinatorWorkPhase::Sealed
        );
    }

    #[test]
    fn aggregate_active_suffix_contains_no_fallible_or_panicking_sites() {
        let source = include_str!("writer_fixed_point.rs");
        let start = source
            .find("    pub(crate) fn execute(\n        self,\n        coordinator:")
            .unwrap();
        let suffix = &source[start..];
        let end = suffix
            .find("\n}\n\nimpl<")
            .expect("aggregate implementation has a stable boundary");
        let active = &suffix[..end];
        for forbidden in [
            "Result<",
            "try_borrow",
            "get_mut",
            ".expect(",
            ".unwrap(",
            "panic!(",
            "unreachable!(",
            "debug_assert!(",
            "callback",
        ] {
            assert!(
                !active.contains(forbidden),
                "Active aggregate suffix contains forbidden site {forbidden}"
            );
        }
        let replay_start = source
            .find("    fn replay(&mut self) {\n        for write in self.session.tombstone_writes.iter()")
            .unwrap();
        let replay_suffix = &source[replay_start..];
        let replay_end = replay_suffix
            .find("\n    }\n}\n\nimpl<")
            .expect("workspace replay has a stable boundary");
        let replay = &replay_suffix[..replay_end];
        for forbidden in [
            "Result<",
            "try_borrow",
            "get_mut",
            ".expect(",
            ".unwrap(",
            "panic!(",
            "unreachable!(",
            "debug_assert!(",
            "callback",
        ] {
            assert!(
                !replay.contains(forbidden),
                "Active workspace replay contains forbidden site {forbidden}"
            );
        }
    }

    #[test]
    fn unbound_terminal_preflight_failure_preserves_caller_journal() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 10, 10, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 10);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let prepared = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || Ok(prepared_output(5, 10)),
            )
            .unwrap();
        let mut pages = [bitmap_terminal_page(usize::MAX, 5)];
        pages[0].bytes[100] ^= 1;
        let before = pages.clone();
        let (prepared, returned_pages, error) = prepared
            .with_unbound_terminal_pages(&pool, &mut pages, 91)
            .expect_err("corrupt terminal page must fail before journal binding");
        assert_eq!(error, FixedPointError::StalePredecessor);
        assert_eq!(returned_pages, before);
        pool.cancel_prepared_coordinator_scope(prepared.scope)
            .unwrap();
        coordinator.finish(predecessor).unwrap();
    }

    #[test]
    fn canonical_terminal_result_cannot_substitute_output_or_record_storage() {
        let mut slots = [PrivatePagePoolSlot::empty(), PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 10, 10, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 10);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut preparation_scratch = [];
        let prepared = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                2,
                &mut work_slot,
                &mut scope_slot,
                &mut preparation_scratch,
                || Ok(prepared_output(5, 10)),
            )
            .unwrap();
        let mut pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        for (index, terminal) in pages.iter_mut().enumerate() {
            terminal.pool_slot = index;
            terminal.pgno = u32::try_from(index + 5).unwrap();
            terminal.authorization = PrivatePageAuthorization::SafelyReclaimed;
            terminal.owner = PrivatePageOwner::Bitmap;
            terminal.owner_generation = 8;
            PageHeader {
                page_type: PageType::BitmapLeaf,
                born_txn: 8,
                item_count: 0,
                level: 0,
                lower: 4032,
                upper: 4096,
                aux: 1,
                page_crc32c: 0,
            }
            .encode_into(&mut terminal.bytes);
            page::write_crc32c(&mut terminal.bytes);
        }
        let terminal = prepared.with_terminal_pages(&pool, &pages, 91).unwrap();
        let mut bindings = [BitmapCowArenaBinding::empty(); 2];
        let mut replacements = [];
        let mut index = [BitmapCowIndexNode::empty(); 2];
        let returned = [const { Cell::new(false) }; 2];
        let mut cleanup_nodes: [PrivatePageSelectiveOverlayNode; 0] = [];
        let mut cleanup_path: [PrivatePageSelectivePathEntry; 0] = [];
        let mut cleanup_targets = [usize::MAX; 2];
        let canonical = match terminal.with_record_scratch(
            0,
            SealedFreeBitmapCoordinatorScratch {
                arena_bindings: &mut bindings,
                replacements: &mut replacements,
                index_nodes: &mut index,
                returned: &returned,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            },
        ) {
            Ok(canonical) => canonical,
            Err(_) => panic!("canonical record scratch must preflight"),
        };
        let (sealed, allocations) = count_thread_allocations(|| {
            coordinator.execute_canonical_prepared(predecessor, &pool, canonical)
        });
        assert_eq!(allocations, 0);
        let sealed = match sealed {
            Ok(sealed) => sealed,
            Err(_) => panic!("canonical work must seal"),
        };

        assert_eq!(sealed.record.root(), 5);
        assert_eq!(sealed.record.pending_page_count(), 10);
        assert!(sealed.record.private_provenance(5).unwrap().is_some());
        let mut bytes = [0; PAGE_SIZE];
        assert!(sealed.record.read_private(5, &mut bytes).unwrap());
        assert_eq!(bytes, pages[0].bytes);
        coordinator.abort_active_work(&pool, sealed.active).unwrap();
    }

    #[test]
    fn active_terminal_authority_is_required_for_prior_and_retirement_mutation() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 10, 10, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 10);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let prepared = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || Ok(prepared_output(5, 10)),
            )
            .unwrap();
        let mut pages = [PrivatePageCoordinatorTerminalPage::empty()];
        pages[0].pool_slot = 0;
        pages[0].pgno = 5;
        pages[0].authorization = PrivatePageAuthorization::SafelyReclaimed;
        pages[0].owner = PrivatePageOwner::Bitmap;
        pages[0].owner_generation = 8;
        PageHeader {
            page_type: PageType::BitmapLeaf,
            born_txn: 8,
            item_count: 0,
            level: 0,
            lower: 4032,
            upper: 4096,
            aux: 1,
            page_crc32c: 0,
        }
        .encode_into(&mut pages[0].bytes);
        page::write_crc32c(&mut pages[0].bytes);
        let prepared = prepared.with_terminal_pages(&pool, &pages, 91).unwrap();
        let active = coordinator
            .activate_terminal_prepared(predecessor, &pool, prepared)
            .unwrap();
        let commitment = pool.exact_commitment(&active.active.scope).unwrap();

        assert_eq!(
            pool.preflight_checkpoint_steps(2).unwrap_err(),
            PrivatePagePoolError::CoordinatorRequired
        );
        assert_eq!(
            pool.preflight_mutation_in_scope(&active.active.scope, &commitment, 2)
                .unwrap_err(),
            PrivatePagePoolError::CoordinatorRequired
        );
        pool.preflight_coordinator_checkpoint_steps(active.work_authority(), 2)
            .unwrap();
        pool.preflight_coordinator_mutation_in_scope(
            active.work_authority(),
            &active.active.scope,
            &commitment,
            2,
        )
        .unwrap();
        coordinator.abort_active_work(&pool, active.active).unwrap();
    }

    #[test]
    fn prior_private_return_is_prepared_before_consume_and_replayed_allocation_free() {
        struct EmptyCommitted;

        impl CommittedPageSource for EmptyCommitted {
            fn check_access(&self) -> Result<(), PageSourceError> {
                Ok(())
            }

            fn read_page(
                &self,
                pgno: u32,
                _destination: &mut [u8; PAGE_SIZE],
            ) -> Result<(), PageSourceError> {
                Err(PageSourceError::PageOutOfBounds(pgno))
            }
        }

        let mut slots = [const { PrivatePagePoolSlot::empty() }; 4];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 10, 10, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 10);
        coordinator.attach_pool(&pool).unwrap();

        let first_predecessor = coordinator.predecessor().unwrap();
        let mut first_work_slot = FixedPointPreparedWorkSlot::empty();
        let mut first_scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut first_preparation_scratch = [];
        let first_prepared = coordinator
            .prepare_work(
                &first_predecessor,
                &pool,
                1,
                1,
                &mut first_work_slot,
                &mut first_scope_slot,
                &mut first_preparation_scratch,
                || Ok(prepared_output(5, 10)),
            )
            .unwrap();
        let first_pages = [bitmap_terminal_page(0, 5)];
        let first_terminal = first_prepared
            .with_terminal_pages(&pool, &first_pages, 91)
            .unwrap();
        let mut first_bindings = [BitmapCowArenaBinding::empty(); 1];
        let mut first_replacements = [];
        let mut first_index = [BitmapCowIndexNode::empty(); 1];
        let first_returned = [const { Cell::new(false) }; 1];
        let mut first_cleanup_nodes: [PrivatePageSelectiveOverlayNode; 0] = [];
        let mut first_cleanup_path: [PrivatePageSelectivePathEntry; 0] = [];
        let mut first_cleanup_targets = [usize::MAX; 1];
        let first_canonical = match first_terminal.with_record_scratch(
            0,
            SealedFreeBitmapCoordinatorScratch {
                arena_bindings: &mut first_bindings,
                replacements: &mut first_replacements,
                index_nodes: &mut first_index,
                returned: &first_returned,
                cleanup_nodes: &mut first_cleanup_nodes,
                cleanup_path: &mut first_cleanup_path,
                cleanup_targets: &mut first_cleanup_targets,
            },
        ) {
            Ok(prepared) => prepared,
            Err(_) => panic!("first canonical scratch must preflight"),
        };
        let first_sealed =
            match coordinator.execute_canonical_prepared(first_predecessor, &pool, first_canonical)
            {
                Ok(sealed) => sealed,
                Err(_) => panic!("first canonical work must seal"),
            };
        let FixedPointSealedCanonicalWork {
            active: first_active,
            record: first_record,
        } = first_sealed;
        pool.accept_sealed_coordinator_scope(&first_active.pool_work, &first_active.scope, 91)
            .unwrap();
        let FixedPointActiveWork {
            output: first_output,
            carried: first_carried,
            pool_work: first_pool_work,
            ..
        } = first_active;
        pool.finish_coordinator_work(first_pool_work).unwrap();
        coordinator.root.set(first_output.root);
        coordinator
            .pending_page_count
            .set(first_output.pending_page_count);
        coordinator.carried.set(first_carried);
        coordinator.registered_work.set(0);

        let committed = EmptyCommitted;
        let mut source_entries = [None; 4];
        let mut source_slot_map = [usize::MAX; 4];
        let mut source = FixedPointDraftSource::new(
            &committed,
            &pool,
            &mut source_entries,
            &mut source_slot_map,
        )
        .unwrap();
        let mut records = [None, None];
        let mut ledger_slot_map = [usize::MAX; 4];
        let mut ledger =
            FixedPointSealedLedger::new(&mut records, &mut ledger_slot_map, pool.len()).unwrap();
        ledger.push(first_record, &source).unwrap();
        let prior_location = source.private_location(5).unwrap().unwrap();

        let second_predecessor = coordinator.predecessor().unwrap();
        let mut second_work_slot = FixedPointPreparedWorkSlot::empty();
        let mut second_scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut second_preparation_scratch = [];
        let second_prepared = coordinator
            .prepare_work(
                &second_predecessor,
                &pool,
                2,
                1,
                &mut second_work_slot,
                &mut second_scope_slot,
                &mut second_preparation_scratch,
                || Ok(prepared_output(6, 10)),
            )
            .unwrap();
        let second_pages = [bitmap_terminal_page(1, 6)];
        let second_terminal = second_prepared
            .with_terminal_pages(&pool, &second_pages, 92)
            .unwrap();
        let mut ordered_locations = [DraftPrivatePageLocation::EMPTY; 1];
        let mut pool_returns = [PrivatePageCoordinatorPriorReturn::empty(); 1];
        let prepared_returns = ledger
            .prepare_prior_returns(
                &mut source,
                &second_terminal.prepared.scope,
                &second_terminal.terminal,
                &[prior_location],
                &mut ordered_locations,
                &mut pool_returns,
            )
            .unwrap();

        assert_eq!(
            pool.coordinator_work_phase(),
            crate::private_page_pool::PrivatePageCoordinatorWorkPhase::None
        );
        assert!(pool.find_bound_page(5).unwrap().is_some());
        let second_active = coordinator
            .activate_terminal_prepared(second_predecessor, &pool, second_terminal)
            .unwrap();
        let second_sealed = coordinator
            .seal_active_terminal(&pool, second_active)
            .unwrap();
        let work = &second_sealed.active.pool_work;
        let (returned, allocations) = count_thread_allocations(|| prepared_returns.apply(work));
        assert_eq!(allocations, 0);
        match returned {
            Ok(count) => assert_eq!(count, 1),
            Err(_) => panic!("prepared return must replay"),
        }
        assert_eq!(ordered_locations, [DraftPrivatePageLocation::EMPTY]);
        assert_eq!(pool_returns, [PrivatePageCoordinatorPriorReturn::empty()]);
        assert_eq!(pool.find_bound_page(5).unwrap(), None);
        assert!(pool.find_bound_page(6).unwrap().is_some());
        assert_eq!(
            ledger.record(0).unwrap().private_provenance(5).unwrap(),
            None
        );
        assert!(source.private_location(5).unwrap().is_none());
        coordinator
            .abort_active_work(&pool, second_sealed.active)
            .unwrap();
    }

    #[test]
    fn coordinator_pool_rejects_direct_scope_reservation() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();

        assert_eq!(
            pool.reserve_scope(1).unwrap_err(),
            PrivatePagePoolError::CoordinatorRequired
        );
        assert_eq!(
            pool.begin_checkpoint().unwrap_err(),
            PrivatePagePoolError::CoordinatorRequired
        );
        assert_eq!(
            pool.preflight_checkpoint_steps(2).unwrap_err(),
            PrivatePagePoolError::CoordinatorRequired
        );
        assert_eq!(
            pool.authorize(0, 2, PrivatePageAuthorization::Appended),
            Err(PrivatePagePoolError::CoordinatorRequired)
        );
        assert_eq!(
            pool.claim_lowest(PrivatePageOwner::Bitmap, 1, 1)
                .unwrap_err(),
            PrivatePagePoolError::CoordinatorRequired
        );
    }

    #[test]
    fn coordinator_attachment_blocks_a_previously_prepared_raw_checkpoint() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let checkpoint = pool.preflight_checkpoint_steps(2).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();

        assert_eq!(
            pool.begin_checkpoint_prepared(&checkpoint),
            Err(PrivatePagePoolError::CoordinatorRequired)
        );
    }

    #[test]
    fn successor_sequence_headroom_is_checked_before_predecessor_consumption() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        coordinator.test_set_sequence(u64::MAX - 1);
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];

        assert_eq!(
            coordinator
                .prepare_work(
                    &predecessor,
                    &pool,
                    1,
                    1,
                    &mut work_slot,
                    &mut scope_slot,
                    &mut scratch,
                    || Ok(prepared_output(0, 2)),
                )
                .unwrap_err(),
            FixedPointError::InvalidWorkUnit
        );
        assert!(coordinator.validate_predecessor(&predecessor).is_ok());
    }

    #[test]
    fn corrupt_vacancy_state_is_rejected_before_final_callback() {
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        pool.test_set_unscoped_vacant_count(1);
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let callback_called = Cell::new(false);

        assert_eq!(
            coordinator
                .prepare_work(
                    &predecessor,
                    &pool,
                    1,
                    1,
                    &mut work_slot,
                    &mut scope_slot,
                    &mut scratch,
                    || {
                        callback_called.set(true);
                        Ok(prepared_output(0, 2))
                    },
                )
                .unwrap_err(),
            FixedPointError::StalePredecessor
        );
        assert!(!callback_called.get());
        assert!(coordinator.validate_predecessor(&predecessor).is_ok());
    }

    #[test]
    fn exhausted_predecessor_sequence_does_not_leave_phantom_authority() {
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.test_set_sequence(u64::MAX);

        assert_eq!(
            coordinator.predecessor(),
            Err(FixedPointError::IdentityExhausted)
        );
        assert!(coordinator.is_quiescent());
    }

    #[test]
    fn coordinator_cannot_attach_after_scope_reservation() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let scope = pool.reserve_scope(1).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        assert_eq!(
            coordinator.attach_pool(&pool),
            Err(FixedPointError::StalePredecessor)
        );
        pool.close_scope(&scope).unwrap();

        let mut mutated_slots = [PrivatePagePoolSlot::empty()];
        let mutated_pool =
            PrivatePagePool::new_vacant_transaction(&mut mutated_slots, 2, 3, 8).unwrap();
        mutated_pool
            .authorize(0, 2, PrivatePageAuthorization::Appended)
            .unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        assert_eq!(
            coordinator.attach_pool(&mutated_pool),
            Err(FixedPointError::StalePredecessor)
        );

        let mut mismatched_slots = [PrivatePagePoolSlot::empty()];
        let mismatched_pool =
            PrivatePagePool::new_vacant_transaction(&mut mismatched_slots, 2, 3, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        assert_eq!(
            coordinator.attach_pool(&mismatched_pool),
            Err(FixedPointError::StalePredecessor)
        );
        let scope = mismatched_pool.reserve_scope(1).unwrap();
        mismatched_pool.close_scope(&scope).unwrap();
    }

    #[test]
    fn two_prepared_plans_borrow_one_predecessor_but_only_one_consumes_it() {
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let replay = predecessor.test_duplicate();
        let mut work_slot_a = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot_a = PrivatePagePreparedScopeSlot::empty();
        let mut scratch_a = [];
        let plan_a = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot_a,
                &mut scope_slot_a,
                &mut scratch_a,
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();
        let mut work_slot_b = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot_b = PrivatePagePreparedScopeSlot::empty();
        let mut scratch_b = [];
        let plan_b = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot_b,
                &mut scope_slot_b,
                &mut scratch_b,
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();

        let active = coordinator
            .execute_prepared(predecessor, &pool, plan_a)
            .unwrap();
        assert_eq!(
            pool.coordinator_commit_fence(),
            Err(PrivatePagePoolError::CoordinatorWorkActive)
        );
        let FixedPointActiveWork {
            coordinator_identity,
            predecessor_generation,
            work_identity,
            output,
            carried,
            pool_work,
            scope,
        } = active;
        let (pool_work, error) = pool.finish_coordinator_work(pool_work).unwrap_err();
        assert_eq!(error, PrivatePagePoolError::UnacceptedCoordinatorScope);
        let active = FixedPointActiveWork {
            coordinator_identity,
            predecessor_generation,
            work_identity,
            output,
            carried,
            pool_work,
            scope,
        };
        assert_eq!(
            coordinator
                .execute_prepared(replay, &pool, plan_b)
                .unwrap_err()
                .2,
            FixedPointError::StalePredecessor
        );
        let successor = coordinator.complete_work(&pool, active).unwrap();
        assert_eq!(successor.sequence(), 2);
        assert!(pool.coordinator_commit_fence().is_ok());
    }

    #[test]
    fn copied_prepared_slot_is_rejected_before_predecessor_consumption() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut copied_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let prepared = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();
        let copied =
            FixedPointPreparedWork::test_relocate_slot(prepared, &mut copied_slot).unwrap();
        let (predecessor, _copied, error) = coordinator
            .execute_prepared(predecessor, &pool, copied)
            .unwrap_err();
        assert_eq!(error, FixedPointError::StalePredecessor);

        let mut retry_slot = FixedPointPreparedWorkSlot::empty();
        let mut retry_scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut retry_scratch = [];
        let retry = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut retry_slot,
                &mut retry_scope_slot,
                &mut retry_scratch,
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();
        let active = coordinator
            .execute_prepared(predecessor, &pool, retry)
            .unwrap();
        coordinator.complete_work(&pool, active).unwrap();
    }

    #[test]
    fn corrupted_prepared_scope_address_is_rejected_before_predecessor_consumption() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let mut prepared = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();
        prepared.test_corrupt_scope_address();

        let (predecessor, _prepared, error) = coordinator
            .execute_prepared(predecessor, &pool, prepared)
            .unwrap_err();
        assert_eq!(error, FixedPointError::StalePredecessor);
        assert!(coordinator.validate_predecessor(&predecessor).is_ok());
    }

    #[test]
    fn forged_prepared_slot_is_rejected_before_predecessor_consumption() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let mut prepared = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();
        prepared.test_forge_work_identity();
        let (predecessor, _prepared, error) = coordinator
            .execute_prepared(predecessor, &pool, prepared)
            .unwrap_err();
        assert_eq!(error, FixedPointError::StalePredecessor);
        assert!(coordinator.validate_predecessor(&predecessor).is_ok());
    }

    #[test]
    fn prepared_scratch_overlap_is_rejected_before_predecessor_consumption() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let mut prepared = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();
        prepared.test_force_scratch_alias();
        let (predecessor, _prepared, error) = coordinator
            .execute_prepared(predecessor, &pool, prepared)
            .unwrap_err();
        assert_eq!(error, FixedPointError::ScratchAlias);
        assert!(coordinator.validate_predecessor(&predecessor).is_ok());
    }

    #[test]
    fn prepared_carried_overlap_is_rejected_before_predecessor_consumption() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let pages = [5];
        let mut prepared = coordinator
            .prepare_work_with_carried(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                41,
                3,
                &pages,
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();
        prepared.test_force_carried_alias();
        let (predecessor, _prepared, error) = coordinator
            .execute_prepared(predecessor, &pool, prepared)
            .unwrap_err();
        assert_eq!(error, FixedPointError::ScratchAlias);
        assert!(coordinator.validate_predecessor(&predecessor).is_ok());
    }

    #[test]
    fn prepared_carried_content_seal_is_revalidated_before_consumption() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let pages = [5, 9];
        let mut prepared = coordinator
            .prepare_work_with_carried(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                41,
                3,
                &pages,
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();
        prepared.test_corrupt_carried_content_seal();
        let (predecessor, _prepared, error) = coordinator
            .execute_prepared(predecessor, &pool, prepared)
            .unwrap_err();
        assert_eq!(error, FixedPointError::StalePredecessor);
        assert!(coordinator.validate_predecessor(&predecessor).is_ok());
    }

    #[test]
    fn final_callback_failure_preserves_predecessor_and_pool_reentry_is_rejected() {
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();

        let mut failed_work_slot = FixedPointPreparedWorkSlot::empty();
        let mut failed_scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut failed_scratch = [];
        assert_eq!(
            coordinator
                .prepare_work(
                    &predecessor,
                    &pool,
                    1,
                    1,
                    &mut failed_work_slot,
                    &mut failed_scope_slot,
                    &mut failed_scratch,
                    || Err(FixedPointError::InvalidArgument),
                )
                .unwrap_err(),
            FixedPointError::InvalidArgument
        );
        assert_eq!(failed_work_slot, FixedPointPreparedWorkSlot::empty());
        assert_eq!(failed_scope_slot, PrivatePagePreparedScopeSlot::empty());
        assert!(coordinator.validate_predecessor(&predecessor).is_ok());

        let mut mutated_work_slot = FixedPointPreparedWorkSlot::empty();
        let mut mutated_scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut mutated_scratch = [];
        let prepared = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut mutated_work_slot,
                &mut mutated_scope_slot,
                &mut mutated_scratch,
                || {
                    assert_eq!(
                        pool.test_reserve_scope_direct(1).unwrap_err(),
                        PrivatePagePoolError::BorrowConflict
                    );
                    Ok(prepared_output(0, 2))
                },
            )
            .unwrap();
        let active = coordinator
            .execute_prepared(predecessor, &pool, prepared)
            .unwrap();
        let successor = coordinator.complete_work(&pool, active).unwrap();
        coordinator.finish(successor).unwrap();
    }

    #[test]
    fn prepared_execute_and_complete_are_allocation_free() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];

        let (successor, allocations) = count_thread_allocations(|| {
            let prepared = coordinator
                .prepare_work(
                    &predecessor,
                    &pool,
                    1,
                    1,
                    &mut work_slot,
                    &mut scope_slot,
                    &mut scratch,
                    || Ok(prepared_output(0, 2)),
                )
                .unwrap();
            let active = coordinator
                .execute_prepared(predecessor, &pool, prepared)
                .unwrap();
            coordinator.complete_work(&pool, active).unwrap()
        });
        assert_eq!(allocations, 0);
        coordinator.finish(successor).unwrap();
    }

    #[test]
    fn large_scope_preparation_is_allocation_free() {
        let mut slots = vec![PrivatePagePoolSlot::empty(); 4096];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];

        let ((), allocations) = count_thread_allocations(|| {
            let _prepared = coordinator
                .prepare_work(
                    &predecessor,
                    &pool,
                    1,
                    4096,
                    &mut work_slot,
                    &mut scope_slot,
                    &mut scratch,
                    || Ok(prepared_output(0, 2)),
                )
                .unwrap();
        });
        assert_eq!(allocations, 0);
        coordinator.finish(predecessor).unwrap();
    }

    #[test]
    fn carried_source_value_regression_is_retryable_and_does_not_consume_predecessor() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut first_work_slot = FixedPointPreparedWorkSlot::empty();
        let mut first_scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut first_scratch = [];
        let first = coordinator
            .prepare_work_with_carried(
                &predecessor,
                &pool,
                1,
                1,
                &mut first_work_slot,
                &mut first_scope_slot,
                &mut first_scratch,
                41,
                3,
                &[5, 9],
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();
        let active = coordinator
            .execute_prepared(predecessor, &pool, first)
            .unwrap();
        let successor = coordinator.complete_work(&pool, active).unwrap();

        let mut bad_work_slot = FixedPointPreparedWorkSlot::empty();
        let mut bad_scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut bad_scratch = [];
        let error = coordinator
            .prepare_work_with_carried(
                &successor,
                &pool,
                2,
                1,
                &mut bad_work_slot,
                &mut bad_scope_slot,
                &mut bad_scratch,
                41,
                3,
                &[8],
                || Ok(prepared_output(0, 2)),
            )
            .unwrap_err();
        assert_eq!(
            error,
            FixedPointError::SourceOrderRegression {
                previous: 9,
                current: 8,
            }
        );
        assert!(coordinator.validate_predecessor(&successor).is_ok());
    }

    #[test]
    fn aborting_active_work_invalidates_sibling_plan_and_requires_pool_abort() {
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 2, 2, 8).unwrap();
        let coordinator = FixedPointCoordinator::test_new(7, 0, 2);
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let replay = predecessor.test_duplicate();
        let mut work_slot_a = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot_a = PrivatePagePreparedScopeSlot::empty();
        let mut scratch_a = [];
        let plan_a = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot_a,
                &mut scope_slot_a,
                &mut scratch_a,
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();
        let mut work_slot_b = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot_b = PrivatePagePreparedScopeSlot::empty();
        let mut scratch_b = [];
        let plan_b = coordinator
            .prepare_work(
                &predecessor,
                &pool,
                1,
                1,
                &mut work_slot_b,
                &mut scope_slot_b,
                &mut scratch_b,
                || Ok(prepared_output(0, 2)),
            )
            .unwrap();
        let active = coordinator
            .execute_prepared(predecessor, &pool, plan_a)
            .unwrap();
        coordinator.abort_active_work(&pool, active).unwrap();
        assert!(coordinator.requires_abort());
        assert!(pool.requires_abort());
        assert_eq!(
            coordinator
                .execute_prepared(replay, &pool, plan_b)
                .unwrap_err()
                .2,
            FixedPointError::AbortRequired
        );
    }
}
