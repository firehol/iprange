//! Transaction-private construction and editing of retirement metadata.
//!
//! This layer never allocates physical pages. The caller authorizes every
//! private page up front and supplies bounded replacement and path storage.

#[cfg(test)]
use crate::bitmap_cow::FreeBitmapCowError;
use crate::bitmap_cow::{
    PreparedFreeBitmapRangeRetirementTerminalExport, PreparedFreeBitmapTerminalExport,
};
use crate::blob_page::{BlobBranch, BlobKind, BlobLeaf, BlobPageError, BLOB_LEAF_CAPACITY};
#[cfg(test)]
use crate::contract::MAX_PAGE_COUNT;
use crate::contract::{MAX_TREE_LEVEL, PAGE_SIZE};
use crate::crc32c::crc32c_with_zeroed;
use crate::page::{self, PageHeader, PageHeaderError, PageType, PAGE_HEADER_SIZE};
use crate::page_number_index::{PageNumberIndex, PageNumberIndexError, PageNumberIndexVisitError};
use crate::page_source::{CommittedPageSource, PageSourceError};
#[cfg(test)]
use crate::private_page_pool::PrivatePageRef;
use crate::private_page_pool::{
    merge_unbound_terminal_page_journals, PrivatePageAuthority, PrivatePageAuthorization,
    PrivatePageCoordinatorTerminalPage, PrivatePageOwner, PrivatePagePool,
    PrivatePagePoolCheckpoint, PrivatePagePoolCommitment, PrivatePagePoolError,
    PrivatePagePoolSlot, PrivatePagePoolSnapshot, PrivatePagePoolState,
    PrivatePageReservationScope, PrivatePageReturn,
};
use crate::range_staging::RangeTreeMaterializedResult;
use crate::retirement_page::{
    RetirementBatch, RetirementBranch, RetirementLeaf, RetirementPageError,
};
use crate::retirement_reader::{RetirementIdentity, RetirementReclamationAuthority};
#[cfg(test)]
use crate::writer_fixed_point::FixedPointPreparedRetirementTerminalWork;
#[cfg(test)]
use crate::writer_fixed_point::FixedPointSealedLedger;
use crate::writer_fixed_point::{
    DraftPageProvenance, DraftPrivatePageLocation, FixedPointDraftSource, FixedPointError,
    FixedPointPreparedProducedTerminalWork, FixedPointPreparedWork,
};
#[cfg(test)]
use crate::writer_fixed_point::{
    FixedPointCoordinatorJournals, FixedPointCoordinatorWorkspace, FixedPointMapJournalWrite,
    FixedPointSourceJournalWrite, FixedPointTombstoneJournalWrite, FixedPointWorkspaceRecordSlot,
};
use core::cell::{Cell, RefCell};

const BLOB_BRANCH_CAPACITY: usize = (PAGE_SIZE - PAGE_HEADER_SIZE as usize) / 16;
const RETIREMENT_LEAF_CAPACITY: usize = (PAGE_SIZE - PAGE_HEADER_SIZE as usize) / 32;
const RETIREMENT_BRANCH_CAPACITY: usize = (PAGE_SIZE - PAGE_HEADER_SIZE as usize) / 16;
const RETIREMENT_PATH_CAPACITY: usize = MAX_TREE_LEVEL as usize + 1;
const RETIREMENT_VALUES_PER_BLOB_LEAF: u64 = BLOB_LEAF_CAPACITY as u64 / 4;
const MAX_BATCH_PAGE_COUNT: u64 = u32::MAX as u64 + 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum RetirementPrivatePageState {
    InUse {
        origin: PrivatePageOrigin,
        generation: u64,
    },
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum PrivatePageOrigin {
    RetirementTree,
    RetirementBlob,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum FixedPointRetirementResidence {
    SelectedCommitted,
    PriorPrivate(DraftPrivatePageLocation),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum FixedPointRetirementPageKind {
    Tree,
    Blob,
}

/// Retirement-only provenance adapter for the transaction draft source.
///
/// Current-work-unit pages remain owned by `PrivatePageArena`; this adapter is
/// consulted only after that arena reports the page absent.
pub(crate) struct FixedPointRetirementSource<
    'draft,
    'source,
    'index,
    'slots,
    S: CommittedPageSource + ?Sized,
> {
    source: &'draft FixedPointDraftSource<'source, 'index, 'slots, S>,
    prior_returns: RefCell<&'draft mut [Option<DraftPrivatePageLocation>]>,
    prior_len: Cell<usize>,
    prior_cursor: Cell<usize>,
}

pub(crate) trait RetirementMetadataSource {
    fn check_retirement_access(&self) -> Result<(), PageSourceError>;

    fn prior_private_location(
        &self,
        _pgno: u32,
        _expected_kind: FixedPointRetirementPageKind,
    ) -> Result<Option<DraftPrivatePageLocation>, RetirementWriteError> {
        Ok(None)
    }

    fn read_non_current_retirement_page(
        &self,
        pgno: u32,
        expected_kind: FixedPointRetirementPageKind,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<FixedPointRetirementResidence, RetirementWriteError>;

    fn prior_release_len(&self) -> usize {
        0
    }

    fn stage_prior_release(
        &self,
        _index: usize,
        location: DraftPrivatePageLocation,
    ) -> Result<(), RetirementWriteError> {
        Err(RetirementWriteError::PrivatePageUnavailable(
            match location.provenance {
                DraftPageProvenance::Private { page, .. } => page.pgno,
                DraftPageProvenance::SelectedCommitted { pgno } => pgno,
            },
        ))
    }

    fn validate_prior_release_commit(
        &self,
        base: usize,
        count: usize,
    ) -> Result<(), RetirementWriteError> {
        if base == 0 && count == 0 {
            Ok(())
        } else {
            Err(RetirementWriteError::PrivatePageUnavailable(0))
        }
    }

    fn commit_prior_releases_prepared(&self, base: usize, count: usize) {
        debug_assert_eq!((base, count), (0, 0));
    }
}

impl<S: CommittedPageSource + ?Sized> RetirementMetadataSource for S {
    fn check_retirement_access(&self) -> Result<(), PageSourceError> {
        self.check_access()
    }

    fn read_non_current_retirement_page(
        &self,
        pgno: u32,
        _expected_kind: FixedPointRetirementPageKind,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<FixedPointRetirementResidence, RetirementWriteError> {
        self.read_page(pgno, destination)?;
        Ok(FixedPointRetirementResidence::SelectedCommitted)
    }
}

impl<'draft, 'source, 'index, 'slots, S: CommittedPageSource + ?Sized>
    FixedPointRetirementSource<'draft, 'source, 'index, 'slots, S>
{
    pub(crate) const fn new(
        source: &'draft FixedPointDraftSource<'source, 'index, 'slots, S>,
        prior_returns: &'draft mut [Option<DraftPrivatePageLocation>],
    ) -> Self {
        Self {
            source,
            prior_returns: RefCell::new(prior_returns),
            prior_len: Cell::new(0),
            prior_cursor: Cell::new(0),
        }
    }

    pub(crate) fn prior_returns(&self) -> core::cell::Ref<'_, [Option<DraftPrivatePageLocation>]> {
        let len = self.prior_len.get();
        core::cell::Ref::map(self.prior_returns.borrow(), |entries| &entries[..len])
    }

    pub(crate) fn read_non_current_page(
        &self,
        pgno: u32,
        expected_kind: FixedPointRetirementPageKind,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<FixedPointRetirementResidence, RetirementWriteError> {
        let location = self
            .source
            .private_location(pgno)
            .map_err(|_| RetirementWriteError::Source(PageSourceError::ForkedHandle))?;
        if let Some(location) = location {
            let DraftPageProvenance::Private { page, .. } = location.provenance else {
                return Err(RetirementWriteError::Source(PageSourceError::ForkedHandle));
            };
            let expected_tag = match expected_kind {
                FixedPointRetirementPageKind::Tree => 1,
                FixedPointRetirementPageKind::Blob => 2,
            };
            if page.owner != PrivatePageOwner::Retirement
                || page.owner_generation == 0
                || page.tag != expected_tag
            {
                return Err(RetirementWriteError::PrivatePageOriginMismatch {
                    pgno,
                    expected: PageRole::PrivateAuthorization,
                });
            }
            self.source.read_private_location(location, destination)?;
            return Ok(FixedPointRetirementResidence::PriorPrivate(location));
        }
        let provenance = self.source.read_page_with_provenance(pgno, destination)?;
        if provenance != (DraftPageProvenance::SelectedCommitted { pgno }) {
            return Err(RetirementWriteError::Source(PageSourceError::ForkedHandle));
        }
        Ok(FixedPointRetirementResidence::SelectedCommitted)
    }

    /// Returns exact prior-work pages staged by successful retirement edits.
    ///
    /// The cursor advances only after each exact ledger return succeeds, so a
    /// caller may retry after an error without returning an earlier page twice.
    #[cfg(test)]
    pub(crate) fn return_staged_prior_pages<'records, 'scratch, 'record>(
        &self,
        ledger: &mut FixedPointSealedLedger<'records, 'scratch, 'record, 'slots>,
    ) -> Result<usize, FreeBitmapCowError> {
        let mut returned = 0usize;
        let len = self.prior_len.get();
        let mut cursor = self.prior_cursor.get();
        while cursor < len {
            let location = self
                .prior_returns
                .try_borrow()
                .map_err(|_| FreeBitmapCowError::StaleReservationPredecessor)?[cursor]
                .ok_or(FreeBitmapCowError::StaleReservationPredecessor)?;
            ledger.return_prior_private_location(location, self.source)?;
            self.prior_returns
                .try_borrow_mut()
                .map_err(|_| FreeBitmapCowError::StaleReservationPredecessor)?[cursor] = None;
            cursor += 1;
            returned += 1;
            self.prior_cursor.set(cursor);
        }
        self.prior_cursor.set(0);
        self.prior_len.set(0);
        Ok(returned)
    }
}

impl<S: CommittedPageSource + ?Sized> RetirementMetadataSource
    for FixedPointRetirementSource<'_, '_, '_, '_, S>
{
    fn check_retirement_access(&self) -> Result<(), PageSourceError> {
        self.source.check_access()
    }

    fn read_non_current_retirement_page(
        &self,
        pgno: u32,
        expected_kind: FixedPointRetirementPageKind,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<FixedPointRetirementResidence, RetirementWriteError> {
        self.read_non_current_page(pgno, expected_kind, destination)
    }

    fn prior_private_location(
        &self,
        pgno: u32,
        expected_kind: FixedPointRetirementPageKind,
    ) -> Result<Option<DraftPrivatePageLocation>, RetirementWriteError> {
        let Some(location) = self
            .source
            .private_location(pgno)
            .map_err(|_| RetirementWriteError::Source(PageSourceError::ForkedHandle))?
        else {
            return Ok(None);
        };
        let DraftPageProvenance::Private { page, .. } = location.provenance else {
            return Err(RetirementWriteError::Source(PageSourceError::ForkedHandle));
        };
        let expected_tag = match expected_kind {
            FixedPointRetirementPageKind::Tree => 1,
            FixedPointRetirementPageKind::Blob => 2,
        };
        if page.owner != PrivatePageOwner::Retirement
            || page.owner_generation == 0
            || page.tag != expected_tag
        {
            return Err(RetirementWriteError::PrivatePageOriginMismatch {
                pgno,
                expected: PageRole::PrivateAuthorization,
            });
        }
        Ok(Some(location))
    }

    fn prior_release_len(&self) -> usize {
        self.prior_len.get()
    }

    fn stage_prior_release(
        &self,
        index: usize,
        location: DraftPrivatePageLocation,
    ) -> Result<(), RetirementWriteError> {
        let mut entries = self
            .prior_returns
            .try_borrow_mut()
            .map_err(|_| RetirementWriteError::Source(PageSourceError::ForkedHandle))?;
        let actual = entries.len();
        let destination = entries.get_mut(index).ok_or(
            RetirementWriteError::PriorPrivateReleaseBufferTooSmall {
                required: index.saturating_add(1),
                actual,
            },
        )?;
        *destination = Some(location);
        Ok(())
    }

    fn validate_prior_release_commit(
        &self,
        base: usize,
        count: usize,
    ) -> Result<(), RetirementWriteError> {
        if self.prior_len.get() != base {
            return Err(RetirementWriteError::Source(PageSourceError::ForkedHandle));
        }
        let end = base
            .checked_add(count)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        if end > self.prior_returns.borrow().len() {
            return Err(RetirementWriteError::PriorPrivateReleaseBufferTooSmall {
                required: end,
                actual: self.prior_returns.borrow().len(),
            });
        }
        Ok(())
    }

    fn commit_prior_releases_prepared(&self, base: usize, count: usize) {
        debug_assert_eq!(self.prior_len.get(), base);
        self.prior_len.set(base + count);
    }
}

pub(crate) type PrivatePageSlot = PrivatePagePoolSlot;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum CommittedPageOrigin {
    RetirementTree,
    RetirementBlob,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct CommittedPageReplacement {
    pub(crate) pgno: u32,
    pub(crate) origin: CommittedPageOrigin,
}

pub(crate) trait VisitedCommittedPageSink {
    fn visit_committed(
        &mut self,
        page: CommittedPageReplacement,
    ) -> Result<(), RetirementWriteError>;
}

#[derive(Debug)]
pub(crate) struct CommittedReplacementLedger<'a> {
    entries: &'a mut [CommittedPageReplacement],
    len: usize,
}

impl<'a> CommittedReplacementLedger<'a> {
    pub(crate) fn new(entries: &'a mut [CommittedPageReplacement]) -> Self {
        Self { entries, len: 0 }
    }

    pub(crate) fn with_prefix(
        entries: &'a mut [CommittedPageReplacement],
        len: usize,
    ) -> Result<Self, RetirementWriteError> {
        if len > entries.len() {
            return Err(RetirementWriteError::ReplacementLedgerTooSmall {
                required: len,
                actual: entries.len(),
            });
        }
        Ok(Self { entries, len })
    }

    pub(crate) fn entries(&self) -> &[CommittedPageReplacement] {
        &self.entries[..self.len]
    }

    fn checkpoint(&self) -> usize {
        self.len
    }

    fn rollback(&mut self, checkpoint: usize) {
        self.len = checkpoint;
    }

    fn require_additional(&self, additional: usize) -> Result<(), RetirementWriteError> {
        let required = self
            .len
            .checked_add(additional)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        if required > self.entries.len() {
            return Err(RetirementWriteError::ReplacementLedgerTooSmall {
                required,
                actual: self.entries.len(),
            });
        }
        Ok(())
    }
}

impl VisitedCommittedPageSink for CommittedReplacementLedger<'_> {
    fn visit_committed(
        &mut self,
        page: CommittedPageReplacement,
    ) -> Result<(), RetirementWriteError> {
        if self.len == self.entries.len() {
            return Err(RetirementWriteError::ReplacementLedgerTooSmall {
                required: self.len.saturating_add(1),
                actual: self.entries.len(),
            });
        }
        self.entries[self.len] = page;
        self.len += 1;
        Ok(())
    }
}

// The owned form exists only for tests. Indirection here would add a heap
// allocation to the arena construction path being tested.
#[allow(clippy::large_enum_variant)]
enum PrivatePagePoolBacking<'a> {
    #[cfg(test)]
    Owned(PrivatePagePool<'a>),
    Shared(&'a PrivatePagePool<'a>),
}

impl core::fmt::Debug for PrivatePagePoolBacking<'_> {
    fn fmt(&self, formatter: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match self {
            #[cfg(test)]
            Self::Owned(pool) => formatter.debug_tuple("Owned").field(pool).finish(),
            Self::Shared(pool) => formatter.debug_tuple("Shared").field(pool).finish(),
        }
    }
}

#[derive(Debug)]
pub(crate) struct PrivatePageArena<'a> {
    pool: PrivatePagePoolBacking<'a>,
    scope: Option<PrivatePageReservationScope<'a>>,
    committed_page_count: u64,
    pending_page_count: u64,
    born_txn: u64,
    generation: Cell<u64>,
    terminal_result: Cell<Option<RetirementTreeEditResult>>,
}

#[derive(Debug)]
struct ArenaCheckpoint<'pool> {
    generation: u64,
    pool: PrivatePagePoolCheckpoint<'pool>,
}

#[derive(Debug)]
enum RetirementPoolFence<'pool> {
    Unscoped(PrivatePagePoolSnapshot<'pool>),
    Scoped(PrivatePagePoolCommitment),
}

impl<'a> PrivatePageArena<'a> {
    #[cfg(test)]
    pub(crate) fn new(
        slots: &'a mut [PrivatePageSlot],
        committed_page_count: u64,
        pending_page_count: u64,
        born_txn: u64,
    ) -> Result<Self, RetirementWriteError> {
        if !(2..=MAX_PAGE_COUNT).contains(&committed_page_count)
            || pending_page_count < committed_page_count
            || pending_page_count > MAX_PAGE_COUNT
        {
            return Err(RetirementWriteError::PageCountOutOfRange {
                committed: committed_page_count,
                pending: pending_page_count,
            });
        }
        if born_txn <= 1 {
            return Err(RetirementWriteError::PendingTransactionOutOfRange(born_txn));
        }
        let pool = PrivatePagePool::new(slots, committed_page_count, pending_page_count, born_txn)
            .map_err(map_pool_construction_error)?;
        Ok(Self {
            pool: PrivatePagePoolBacking::Owned(pool),
            scope: None,
            committed_page_count,
            pending_page_count,
            born_txn,
            generation: Cell::new(0),
            terminal_result: Cell::new(None),
        })
    }

    pub(crate) fn from_pool(
        pool: &'a PrivatePagePool<'a>,
        born_txn: u64,
    ) -> Result<Self, RetirementWriteError> {
        if born_txn <= 1 {
            return Err(RetirementWriteError::PendingTransactionOutOfRange(born_txn));
        }
        if born_txn != pool.pending_txn() {
            return Err(RetirementWriteError::PrivatePool(
                PrivatePagePoolError::PendingTransactionMismatch {
                    expected: pool.pending_txn(),
                    actual: born_txn,
                },
            ));
        }
        if pool.has_active_scopes() {
            return Err(RetirementWriteError::PrivatePool(
                PrivatePagePoolError::StaleScope,
            ));
        }
        Ok(Self {
            pool: PrivatePagePoolBacking::Shared(pool),
            scope: None,
            committed_page_count: pool.committed_page_count(),
            pending_page_count: pool.pending_page_count(),
            born_txn,
            generation: Cell::new(0),
            terminal_result: Cell::new(None),
        })
    }

    pub(crate) fn from_scoped_pool(
        pool: &'a PrivatePagePool<'a>,
        scope: &PrivatePageReservationScope<'a>,
        born_txn: u64,
    ) -> Result<Self, RetirementWriteError> {
        if born_txn <= 1 {
            return Err(RetirementWriteError::PendingTransactionOutOfRange(born_txn));
        }
        if born_txn != pool.pending_txn() {
            return Err(RetirementWriteError::PrivatePool(
                PrivatePagePoolError::PendingTransactionMismatch {
                    expected: pool.pending_txn(),
                    actual: born_txn,
                },
            ));
        }
        pool.visit_exact_scope_layout(scope, |_, _, _| {})
            .map_err(map_pool_error)?;
        Ok(Self {
            pool: PrivatePagePoolBacking::Shared(pool),
            scope: Some(scope.share()),
            committed_page_count: pool.committed_page_count(),
            pending_page_count: pool.pending_page_count(),
            born_txn,
            generation: Cell::new(0),
            terminal_result: Cell::new(None),
        })
    }

    fn pool(&self) -> &PrivatePagePool<'a> {
        match &self.pool {
            #[cfg(test)]
            PrivatePagePoolBacking::Owned(pool) => pool,
            PrivatePagePoolBacking::Shared(pool) => pool,
        }
    }

    fn scope(&self) -> Option<&PrivatePageReservationScope<'a>> {
        self.scope.as_ref()
    }

    fn validate_authority(&self) -> Result<(), RetirementWriteError> {
        if self.pool().pending_txn() != self.born_txn
            || self.pool().committed_page_count() != self.committed_page_count
            || self.pool().pending_page_count() != self.pending_page_count
        {
            return Err(RetirementWriteError::StaleEditPlan(
                RetirementEditBinding::Arena,
            ));
        }
        match self.scope() {
            Some(scope) => self
                .pool()
                .visit_exact_scope_layout(scope, |_, _, _| {})
                .map_err(map_pool_error),
            None if self.pool().has_active_scopes() => Err(RetirementWriteError::PrivatePool(
                PrivatePagePoolError::StaleScope,
            )),
            None => Ok(()),
        }
    }

    pub(crate) const fn committed_page_count(&self) -> u64 {
        self.committed_page_count
    }

    pub(crate) const fn pending_page_count(&self) -> u64 {
        self.pending_page_count
    }

    pub(crate) const fn born_txn(&self) -> u64 {
        self.born_txn
    }

    pub(crate) fn in_use_count(&self) -> Result<usize, RetirementWriteError> {
        if let Some(scope) = self.scope() {
            // A lock-bound finalizer shares one shadow scope between bitmap and
            // retirement construction. Count only this arena's retirement
            // pages; a bitmap page must not make its terminal export stale.
            let mut retirement_pages = 0usize;
            self.pool()
                .visit_exact_scope_layout(scope, |_, _, info| {
                    if matches!(
                        info.state,
                        PrivatePagePoolState::InUse {
                            owner: PrivatePageOwner::Retirement,
                            ..
                        }
                    ) {
                        retirement_pages += 1;
                    }
                })
                .map_err(map_pool_error)?;
            return Ok(retirement_pages);
        }
        Ok((0..self.pool().len())
            .filter(|&slot| {
                matches!(
                    self.pool().state(slot),
                    Ok(PrivatePagePoolState::InUse {
                        owner: PrivatePageOwner::Retirement,
                        ..
                    })
                )
            })
            .count())
    }

    pub(crate) fn available(&self) -> Result<usize, RetirementWriteError> {
        match self.scope() {
            Some(scope) => self.pool().scoped_available(scope).map_err(map_pool_error),
            None => self.pool().available().map_err(map_pool_error),
        }
    }

    fn require_pages(&self, count: usize) -> Result<(), RetirementWriteError> {
        let in_use = self.in_use_count()?;
        let available = self.available()?;
        if count > available {
            return Err(RetirementWriteError::PrivatePageBudgetTooSmall {
                required: in_use.saturating_add(count),
                actual: in_use.saturating_add(available),
            });
        }
        Ok(())
    }

    fn capture_fence(&self) -> Result<RetirementPoolFence<'a>, RetirementWriteError> {
        self.validate_authority()?;
        match self.scope() {
            Some(scope) => self
                .pool()
                .exact_commitment(scope)
                .map(RetirementPoolFence::Scoped)
                .map_err(map_pool_error),
            None => self
                .pool()
                .mutation_snapshot()
                .map(RetirementPoolFence::Unscoped)
                .map_err(map_pool_error),
        }
    }

    fn validate_fence(&self, fence: &RetirementPoolFence<'_>) -> Result<(), RetirementWriteError> {
        match (self.scope(), fence) {
            (Some(scope), RetirementPoolFence::Scoped(commitment)) => self
                .pool()
                .validate_exact_commitment(scope, commitment)
                .map_err(map_pool_error),
            (None, RetirementPoolFence::Unscoped(snapshot)) => self
                .pool()
                .preflight_mutation(snapshot, 0)
                .map_err(map_pool_error),
            _ => Err(RetirementWriteError::StaleEditPlan(
                RetirementEditBinding::Arena,
            )),
        }
    }

    fn begin(&self) -> Result<ArenaCheckpoint<'a>, RetirementWriteError> {
        self.validate_authority()?;
        let pool = self.pool().preflight_checkpoint().map_err(map_pool_error)?;
        let generation = self
            .generation
            .get()
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        self.pool()
            .begin_checkpoint_prepared(&pool)
            .map_err(map_pool_error)?;
        Ok(ArenaCheckpoint { generation, pool })
    }

    fn begin_reserved(
        &self,
        epoch_steps: usize,
    ) -> Result<ArenaCheckpoint<'a>, RetirementWriteError> {
        self.validate_authority()?;
        let pool = self
            .pool()
            .preflight_checkpoint_steps(epoch_steps)
            .map_err(map_pool_error)?;
        let generation = self
            .generation
            .get()
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        self.pool()
            .begin_checkpoint_prepared(&pool)
            .map_err(map_pool_error)?;
        Ok(ArenaCheckpoint { generation, pool })
    }

    fn allocate(
        &self,
        checkpoint: &ArenaCheckpoint<'_>,
        origin: PrivatePageOrigin,
    ) -> Result<u32, RetirementWriteError> {
        let in_use = self.in_use_count()?;
        let available = self.available()?;
        let claimed = match self.scope() {
            Some(scope) => self.pool().claim_lowest_in_scope(
                &checkpoint.pool,
                scope,
                PrivatePageOwner::Retirement,
                checkpoint.generation,
                private_origin_tag(origin),
            ),
            None => self.pool().claim_lowest(
                PrivatePageOwner::Retirement,
                checkpoint.generation,
                private_origin_tag(origin),
            ),
        };
        claimed
            .map(|authority| authority.page_number())
            .map_err(|error| match error {
                PrivatePagePoolError::PageUnavailable(_) => {
                    RetirementWriteError::PrivatePageBudgetTooSmall {
                        required: in_use.saturating_add(1),
                        actual: in_use.saturating_add(available),
                    }
                }
                other => map_pool_error(other),
            })
    }

    fn rollback(&self, checkpoint: ArenaCheckpoint<'_>) {
        match self.scope() {
            Some(scope) => self
                .pool()
                .rollback_checkpoint_in_scope(checkpoint.pool, scope),
            None => self.pool().rollback_checkpoint(checkpoint.pool),
        }
        .expect("arena checkpoint remains exact");
    }

    fn commit(&self, checkpoint: ArenaCheckpoint<'_>, releases: &[u32]) {
        for &pgno in releases {
            let state = self
                .private_state(pgno)
                .expect("release lookup remains valid")
                .expect("release preflight retained an authorized private page");
            let RetirementPrivatePageState::InUse { generation, .. } = state;
            let authority = match self.scope() {
                Some(scope) => self.pool().authority_in_scope(
                    scope,
                    pgno,
                    PrivatePageOwner::Retirement,
                    generation,
                ),
                None => self
                    .pool()
                    .authority(pgno, PrivatePageOwner::Retirement, generation),
            }
            .expect("release authority remains exact");
            match self.scope() {
                Some(scope) => {
                    self.pool()
                        .return_page_in_scope(scope, authority, PrivatePageReturn::Available)
                }
                None => self
                    .pool()
                    .return_page(authority, PrivatePageReturn::Available),
            }
            .expect("release preflight remains exact");
        }
        match self.scope() {
            Some(scope) => self
                .pool()
                .commit_checkpoint_in_scope(checkpoint.pool, scope),
            None => self.pool().commit_checkpoint(checkpoint.pool),
        }
        .expect("arena checkpoint remains exact");
        self.generation.set(checkpoint.generation);
    }

    fn release_generation(&mut self, generation: u64, origin: PrivatePageOrigin) {
        if let Some(scope) = self.scope() {
            self.pool()
                .release_generation_in_scope(
                    scope,
                    PrivatePageOwner::Retirement,
                    generation,
                    private_origin_tag(origin),
                )
                .expect("scoped generation cleanup remains exact");
            return;
        }
        for slot in 0..self.pool().len() {
            let Ok(pgno) = self.pool().page_number(slot) else {
                continue;
            };
            if self
                .private_state(pgno)
                .expect("unscoped generation cleanup remains valid")
                == Some(RetirementPrivatePageState::InUse { origin, generation })
            {
                let authority = self
                    .pool()
                    .authority(pgno, PrivatePageOwner::Retirement, generation)
                    .expect("generation cleanup authority remains exact");
                self.pool()
                    .return_page(authority, PrivatePageReturn::Available)
                    .expect("generation cleanup remains exact");
            }
        }
    }

    fn private_state(
        &self,
        pgno: u32,
    ) -> Result<Option<RetirementPrivatePageState>, RetirementWriteError> {
        let slot = match self.scope() {
            Some(scope) => self.pool().find_in_scope(scope, pgno),
            None => self.pool().find(pgno),
        }
        .map_err(map_pool_error)?;
        let Some(slot) = slot else {
            return Ok(None);
        };
        let state = match self.scope() {
            Some(scope) => {
                self.pool()
                    .scoped_slot_info(scope, slot)
                    .map_err(map_pool_error)?
                    .ok_or(RetirementWriteError::PrivatePageUnavailable(pgno))?
                    .state
            }
            None => self.pool().state(slot).map_err(map_pool_error)?,
        };
        Ok(match state {
            PrivatePagePoolState::InUse {
                owner: PrivatePageOwner::Retirement,
                owner_generation,
                tag,
            } => Some(RetirementPrivatePageState::InUse {
                origin: match private_origin_from_tag(tag) {
                    Some(origin) => origin,
                    None => return Ok(None),
                },
                generation: owner_generation,
            }),
            _ => None,
        })
    }

    fn contains(&self, pgno: u32) -> Result<bool, RetirementWriteError> {
        match self.scope() {
            Some(scope) => self.pool().find_in_scope(scope, pgno),
            None => self.pool().find_bound_page(pgno),
        }
        .map(|slot| slot.is_some())
        .map_err(map_pool_error)
    }

    fn page_number_at(&self, slot: usize) -> Result<Option<u32>, RetirementWriteError> {
        if self.pool().state(slot).map_err(map_pool_error)? == PrivatePagePoolState::Vacant {
            return Ok(None);
        }
        self.pool()
            .page_number(slot)
            .map(Some)
            .map_err(map_pool_error)
    }

    fn child_logical_offset(&self, pgno: u32) -> Result<Option<u64>, RetirementWriteError> {
        let mut bytes = [0u8; PAGE_SIZE];
        Ok(self
            .read_page(pgno, &mut bytes)?
            .then(|| child_logical_offset(&bytes)))
    }

    fn read_page(
        &self,
        pgno: u32,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<bool, RetirementWriteError> {
        let Some(RetirementPrivatePageState::InUse { origin, generation }) =
            self.private_state(pgno)?
        else {
            return Ok(false);
        };
        match self.scope() {
            Some(scope) => {
                let Some(slot) = self
                    .pool()
                    .find_in_scope(scope, pgno)
                    .map_err(map_pool_error)?
                else {
                    return Ok(false);
                };
                let info = self
                    .pool()
                    .scoped_slot_info(scope, slot)
                    .map_err(map_pool_error)?
                    .ok_or(RetirementWriteError::PrivatePageUnavailable(pgno))?;
                self.pool()
                    .copy_owned_page_in_scope(
                        scope,
                        slot,
                        info.binding_epoch,
                        pgno,
                        PrivatePageOwner::Retirement,
                        generation,
                        private_origin_tag(origin),
                        destination,
                    )
                    .map(|()| true)
                    .map_err(map_pool_error)
            }
            None => {
                let authority = self
                    .pool()
                    .authority(pgno, PrivatePageOwner::Retirement, generation)
                    .map_err(map_pool_error)?;
                let page = self
                    .pool()
                    .borrow_page(&authority)
                    .map_err(map_pool_error)?;
                destination.copy_from_slice(&page[..]);
                Ok(true)
            }
        }
    }

    fn read_page_snapshot(
        &self,
        snapshot: &RetirementPoolFence<'_>,
        pgno: u32,
        origin: PrivatePageOrigin,
        generation: u64,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<(), RetirementWriteError> {
        match (self.scope(), snapshot) {
            (Some(scope), RetirementPoolFence::Scoped(commitment)) => {
                self.pool()
                    .validate_commitment_epoch(scope, commitment)
                    .map_err(map_pool_error)?;
                let slot = self
                    .pool()
                    .find_in_scope(scope, pgno)
                    .map_err(map_pool_error)?
                    .ok_or(RetirementWriteError::PrivatePageUnavailable(pgno))?;
                let info = self
                    .pool()
                    .scoped_slot_info(scope, slot)
                    .map_err(map_pool_error)?
                    .ok_or(RetirementWriteError::PrivatePageUnavailable(pgno))?;
                let result = self.pool().copy_owned_page_in_scope(
                    scope,
                    slot,
                    info.binding_epoch,
                    pgno,
                    PrivatePageOwner::Retirement,
                    generation,
                    private_origin_tag(origin),
                    destination,
                );
                #[cfg(test)]
                if result.is_ok() {
                    record_private_snapshot_read();
                }
                result.map_err(map_pool_error)
            }
            (None, RetirementPoolFence::Unscoped(snapshot)) => {
                let slot = self
                    .pool()
                    .find(pgno)
                    .map_err(map_pool_error)?
                    .ok_or(RetirementWriteError::PrivatePageUnavailable(pgno))?;
                self.pool()
                    .copy_owned_page(
                        snapshot,
                        slot,
                        pgno,
                        PrivatePageOwner::Retirement,
                        generation,
                        private_origin_tag(origin),
                        self.born_txn,
                        destination,
                    )
                    .map_err(map_pool_error)
            }
            _ => Err(RetirementWriteError::StaleEditPlan(
                RetirementEditBinding::Arena,
            )),
        }
    }

    fn transfer_bitmap_page_to_retirement(
        &self,
        pgno: u32,
        generation: u64,
        origin: PrivatePageOrigin,
    ) -> Result<(), (Option<PrivatePageAuthority<'_>>, RetirementWriteError)> {
        let authority = match self.scope() {
            Some(scope) => {
                self.pool()
                    .authority_in_scope(scope, pgno, PrivatePageOwner::Bitmap, self.born_txn)
            }
            None => self
                .pool()
                .authority(pgno, PrivatePageOwner::Bitmap, self.born_txn),
        }
        .map_err(|error| (None, map_pool_error(error)))?;
        match self.scope() {
            Some(scope) => self.pool().transfer_in_scope(
                scope,
                authority,
                PrivatePageOwner::Retirement,
                generation,
                private_origin_tag(origin),
            ),
            None => self.pool().transfer(
                authority,
                PrivatePageOwner::Retirement,
                generation,
                private_origin_tag(origin),
            ),
        }
        .map(|_| ())
        .map_err(|(authority, error)| (Some(authority), map_pool_error(error)))
    }

    fn transfer_retirement_page_to_bitmap(
        &self,
        pgno: u32,
        generation: u64,
        origin: PrivatePageOrigin,
    ) -> Result<(), (Option<PrivatePageAuthority<'_>>, RetirementWriteError)> {
        if self.private_state(pgno).map_err(|error| (None, error))?
            != Some(RetirementPrivatePageState::InUse { origin, generation })
        {
            return Err((None, RetirementWriteError::PrivatePageUnavailable(pgno)));
        }
        let authority = match self.scope() {
            Some(scope) => self.pool().authority_in_scope(
                scope,
                pgno,
                PrivatePageOwner::Retirement,
                generation,
            ),
            None => self
                .pool()
                .authority(pgno, PrivatePageOwner::Retirement, generation),
        }
        .map_err(|error| (None, map_pool_error(error)))?;
        match self.scope() {
            Some(scope) => self.pool().transfer_in_scope(
                scope,
                authority,
                PrivatePageOwner::Bitmap,
                self.born_txn,
                0,
            ),
            None => self
                .pool()
                .transfer(authority, PrivatePageOwner::Bitmap, self.born_txn, 0),
        }
        .map(|_| ())
        .map_err(|(authority, error)| (Some(authority), map_pool_error(error)))
    }

    fn write_page(&mut self, checkpoint: &ArenaCheckpoint<'_>, pgno: u32, bytes: &[u8; PAGE_SIZE]) {
        let RetirementPrivatePageState::InUse { generation, .. } = self
            .private_state(pgno)
            .expect("allocated page lookup remains valid")
            .expect("allocated page remains authorized");
        let authority = match self.scope() {
            Some(scope) => self.pool().authority_in_scope(
                scope,
                pgno,
                PrivatePageOwner::Retirement,
                generation,
            ),
            None => self
                .pool()
                .authority(pgno, PrivatePageOwner::Retirement, generation),
        }
        .expect("allocated page authority remains exact");
        self.pool()
            .write_page_for_checkpoint_prepared(&checkpoint.pool, &authority, bytes)
            .expect("new checkpoint page has exact reserved write headroom");
    }

    fn install_planned_page(
        &self,
        checkpoint: &ArenaCheckpoint<'_>,
        slot: usize,
        pgno: u32,
        binding_epoch: u64,
        generation: u64,
        bytes: &[u8; PAGE_SIZE],
    ) {
        if let Some(scope) = self.scope() {
            self.pool().claim_slot_in_scope_for_checkpoint_prepared(
                &checkpoint.pool,
                scope,
                slot,
                binding_epoch,
                PrivatePageOwner::Retirement,
                generation,
                private_origin_tag(PrivatePageOrigin::RetirementTree),
                bytes,
            );
            return;
        }
        let authority = match self.scope() {
            Some(_) => unreachable!(),
            None => self.pool().claim(
                slot,
                PrivatePageOwner::Retirement,
                generation,
                private_origin_tag(PrivatePageOrigin::RetirementTree),
            ),
        }
        .expect("planned destination remains exactly available");
        debug_assert_eq!(authority.page_number(), pgno);
        self.pool()
            .write_page_for_checkpoint_prepared(&checkpoint.pool, &authority, bytes)
            .expect("planned destination has exact reserved write headroom");
    }

    #[cfg(test)]
    fn test_install_page(
        &self,
        slot: usize,
        origin: PrivatePageOrigin,
        generation: u64,
        encode: impl FnOnce(&mut [u8; PAGE_SIZE]),
    ) {
        let authority = self
            .pool()
            .claim(
                slot,
                PrivatePageOwner::Retirement,
                generation,
                private_origin_tag(origin),
            )
            .unwrap();
        encode(&mut self.pool().borrow_page_mut(&authority).unwrap());
    }

    #[cfg(test)]
    fn test_edit_page(&self, slot: usize, encode: impl FnOnce(&mut [u8; PAGE_SIZE])) {
        let pgno = self.page_number_at(slot).unwrap().unwrap();
        let RetirementPrivatePageState::InUse { generation, .. } =
            self.private_state(pgno).unwrap().unwrap();
        let authority = self
            .pool()
            .authority(pgno, PrivatePageOwner::Retirement, generation)
            .unwrap();
        encode(&mut self.pool().borrow_page_mut(&authority).unwrap());
    }

    #[cfg(test)]
    fn test_page(&self, pgno: u32) -> Option<PrivatePageRef<'_, 'a>> {
        let RetirementPrivatePageState::InUse { generation, .. } =
            self.private_state(pgno).ok()??;
        let authority = self
            .pool()
            .authority(pgno, PrivatePageOwner::Retirement, generation)
            .ok()?;
        self.pool().borrow_page(&authority).ok()
    }

    #[cfg(test)]
    fn test_page_at(&self, slot: usize) -> [u8; PAGE_SIZE] {
        let pgno = self.page_number_at(slot).unwrap().unwrap();
        *self.test_page(pgno).unwrap()
    }

    #[cfg(test)]
    fn test_state_at(&self, slot: usize) -> Option<RetirementPrivatePageState> {
        self.private_state(self.page_number_at(slot).ok()??).ok()?
    }
}

fn private_origin_tag(origin: PrivatePageOrigin) -> u64 {
    match origin {
        PrivatePageOrigin::RetirementTree => 1,
        PrivatePageOrigin::RetirementBlob => 2,
    }
}

fn private_origin_from_tag(tag: u64) -> Option<PrivatePageOrigin> {
    match tag {
        1 => Some(PrivatePageOrigin::RetirementTree),
        2 => Some(PrivatePageOrigin::RetirementBlob),
        _ => None,
    }
}

fn map_pool_construction_error(error: PrivatePagePoolError) -> RetirementWriteError {
    match error {
        PrivatePagePoolError::PageCountOutOfRange { committed, pending } => {
            RetirementWriteError::PageCountOutOfRange { committed, pending }
        }
        PrivatePagePoolError::PageOutOfBounds(pgno) => {
            RetirementWriteError::PrivatePageOutOfBounds(pgno)
        }
        PrivatePagePoolError::AuthorizationMismatch {
            pgno,
            authorization,
        } => RetirementWriteError::PrivateAuthorizationMismatch {
            pgno,
            authorization,
        },
        PrivatePagePoolError::PagesNotStrict { previous, current } => {
            RetirementWriteError::PrivatePagesNotStrict { previous, current }
        }
        PrivatePagePoolError::PageUnavailable(pgno) => {
            RetirementWriteError::PrivateSlotAlreadyInUse(pgno)
        }
        other => map_pool_error(other),
    }
}

fn map_pool_error(error: PrivatePagePoolError) -> RetirementWriteError {
    RetirementWriteError::PrivatePool(error)
}

#[derive(Debug)]
pub(crate) struct BlobBuildScratch<'a> {
    pgnos: &'a mut [u32],
}

impl<'a> BlobBuildScratch<'a> {
    pub(crate) fn new(pgnos: &'a mut [u32]) -> Self {
        Self { pgnos }
    }
}

#[derive(Debug)]
pub(crate) struct PrivateReleaseBuffer<'a> {
    pgnos: &'a mut [u32],
    len: usize,
}

impl<'a> PrivateReleaseBuffer<'a> {
    pub(crate) fn new(pgnos: &'a mut [u32]) -> Self {
        Self { pgnos, len: 0 }
    }

    fn checkpoint(&self) -> usize {
        self.len
    }

    fn rollback(&mut self, checkpoint: usize) {
        self.len = checkpoint;
    }

    fn push(&mut self, pgno: u32) -> Result<(), RetirementWriteError> {
        if self.len == self.pgnos.len() {
            return Err(RetirementWriteError::PrivateReleaseBufferTooSmall {
                required: self.len.saturating_add(1),
                actual: self.pgnos.len(),
            });
        }
        self.pgnos[self.len] = pgno;
        self.len += 1;
        Ok(())
    }

    pub(crate) fn entries_from(&self, checkpoint: usize) -> &[u32] {
        &self.pgnos[checkpoint..self.len]
    }
}

struct RetirementEditStaging<'ledger, 'entries, 'release, 'release_entries, 'prior> {
    replacements: &'ledger mut CommittedReplacementLedger<'entries>,
    replacement_base: usize,
    replacement_len: usize,
    releases: &'release mut PrivateReleaseBuffer<'release_entries>,
    release_base: usize,
    release_len: usize,
    prior_source: &'prior dyn RetirementMetadataSource,
    prior_base: usize,
    prior_len: usize,
}

impl<'ledger, 'entries, 'release, 'release_entries, 'prior>
    RetirementEditStaging<'ledger, 'entries, 'release, 'release_entries, 'prior>
{
    fn new(
        replacements: &'ledger mut CommittedReplacementLedger<'entries>,
        releases: &'release mut PrivateReleaseBuffer<'release_entries>,
        prior_source: &'prior dyn RetirementMetadataSource,
    ) -> Self {
        Self {
            replacement_base: replacements.len,
            replacement_len: 0,
            release_base: releases.len,
            release_len: 0,
            prior_base: prior_source.prior_release_len(),
            prior_len: 0,
            replacements,
            releases,
            prior_source,
        }
    }

    fn stage_replacement(
        &mut self,
        replacement: CommittedPageReplacement,
    ) -> Result<(), RetirementWriteError> {
        let index = self
            .replacement_base
            .checked_add(self.replacement_len)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        if index == self.replacements.entries.len() {
            return Err(RetirementWriteError::ReplacementLedgerTooSmall {
                required: index.saturating_add(1),
                actual: self.replacements.entries.len(),
            });
        }
        self.replacements.entries[index] = replacement;
        self.replacement_len += 1;
        Ok(())
    }

    fn stage_release(&mut self, pgno: u32) -> Result<(), RetirementWriteError> {
        let index = self
            .release_base
            .checked_add(self.release_len)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        if index == self.releases.pgnos.len() {
            return Err(RetirementWriteError::PrivateReleaseBufferTooSmall {
                required: index.saturating_add(1),
                actual: self.releases.pgnos.len(),
            });
        }
        self.releases.pgnos[index] = pgno;
        self.release_len += 1;
        Ok(())
    }

    fn stage_prior_release(
        &mut self,
        location: DraftPrivatePageLocation,
    ) -> Result<(), RetirementWriteError> {
        let index = self
            .prior_base
            .checked_add(self.prior_len)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        self.prior_source.stage_prior_release(index, location)?;
        self.prior_len += 1;
        Ok(())
    }

    fn staged_releases(&self) -> &[u32] {
        &self.releases.pgnos[self.release_base..self.release_base + self.release_len]
    }

    fn finish(self) -> (usize, usize, usize, usize) {
        (
            self.replacement_len,
            self.release_len,
            self.prior_base,
            self.prior_len,
        )
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PageRole {
    PrivateAuthorization,
    ReferencedRetirementTree,
    ReferencedRetirementBlob,
    SelectedRetirementTree,
    SelectedRetirementBlob,
    ListedReclaimed,
    RequiredRetirementList,
    ReplacementRetirementTree,
    ReplacementRetirementBlob,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PageRoleIndexSlot {
    pgno: u32,
    roles: u16,
    reference_epoch: u8,
    selected_epoch: u8,
    prepared_slot: usize,
    prepared_binding_epoch: u64,
    prepared_owner_generation: u64,
    prepared_tag: u64,
    left: usize,
    right: usize,
    height: u8,
    occupied: bool,
}

impl PageRoleIndexSlot {
    pub(crate) const fn new() -> Self {
        Self {
            pgno: 0,
            roles: 0,
            reference_epoch: 0,
            selected_epoch: 0,
            prepared_slot: usize::MAX,
            prepared_binding_epoch: 0,
            prepared_owner_generation: 0,
            prepared_tag: 0,
            left: ROLE_NO_INDEX,
            right: ROLE_NO_INDEX,
            height: 0,
            occupied: false,
        }
    }
}

#[derive(Debug)]
pub(crate) struct PageRoleIndex<'a> {
    slots: &'a mut [PageRoleIndexSlot],
    root: usize,
    used: usize,
    reference_epoch: u8,
    replacements_must_be_listed: bool,
    #[cfg(test)]
    locate_work: Cell<usize>,
}

const ROLE_NO_INDEX: usize = usize::MAX;

const ROLE_PRIVATE: u16 = 1 << 0;
const ROLE_TREE: u16 = 1 << 1;
const ROLE_BLOB: u16 = 1 << 2;
const ROLE_LISTED: u16 = 1 << 3;
const ROLE_REPLACEMENT_TREE: u16 = 1 << 4;
const ROLE_REPLACEMENT_BLOB: u16 = 1 << 5;
const ROLE_REFERENCE_TREE: u16 = 1 << 6;
const ROLE_REFERENCE_BLOB: u16 = 1 << 7;
const ROLE_OLD_REQUIRED: u16 = 1 << 8;
const ROLE_PREFIX_REPLACEMENT: u16 = 1 << 9;
const ROLE_PRIVATE_RETIRED: u16 = 1 << 10;
const ROLE_PRIOR_PRIVATE_RETIRED: u16 = 1 << 11;
const ROLE_SAFE_RECLAIMED: u16 = 1 << 12;

impl<'a> PageRoleIndex<'a> {
    pub(crate) fn new(slots: &'a mut [PageRoleIndexSlot]) -> Self {
        Self {
            slots,
            root: ROLE_NO_INDEX,
            used: 0,
            reference_epoch: 1,
            replacements_must_be_listed: false,
            #[cfg(test)]
            locate_work: Cell::new(0),
        }
    }

    fn clear(&mut self) {
        self.slots.fill(PageRoleIndexSlot::new());
        self.root = ROLE_NO_INDEX;
        self.used = 0;
        self.reference_epoch = 1;
        self.replacements_must_be_listed = false;
        #[cfg(test)]
        self.locate_work.set(0);
    }

    fn prepare(
        &mut self,
        arena: &PrivatePageArena<'_>,
        replacements: &CommittedReplacementLedger<'_>,
    ) -> Result<(), RetirementWriteError> {
        self.clear();
        if let Some(scope) = arena.scope() {
            let mut problem = None;
            arena
                .pool()
                .visit_exact_scope_layout(scope, |_, _, info| {
                    if problem.is_some()
                        || !info.bound
                        || matches!(
                            info.state,
                            PrivatePagePoolState::InUse { owner, .. }
                                if owner != PrivatePageOwner::Retirement
                        )
                    {
                        return;
                    }
                    problem = self
                        .insert_exclusive(info.pgno, ROLE_PRIVATE, PageRole::PrivateAuthorization)
                        .err();
                })
                .map_err(map_pool_error)?;
            if let Some(problem) = problem {
                return Err(problem);
            }
        } else {
            for slot in 0..arena.pool().len() {
                let Some(pgno) = arena.page_number_at(slot)? else {
                    continue;
                };
                if matches!(
                    arena.pool().state(slot).map_err(map_pool_error)?,
                    PrivatePagePoolState::InUse { owner, .. }
                        if owner != PrivatePageOwner::Retirement
                ) {
                    continue;
                }
                self.insert_exclusive(pgno, ROLE_PRIVATE, PageRole::PrivateAuthorization)?;
            }
        }
        for replacement in replacements.entries() {
            let (role, name) = match replacement.origin {
                CommittedPageOrigin::RetirementTree => {
                    (ROLE_REPLACEMENT_TREE, PageRole::ReplacementRetirementTree)
                }
                CommittedPageOrigin::RetirementBlob => {
                    (ROLE_REPLACEMENT_BLOB, PageRole::ReplacementRetirementBlob)
                }
            };
            self.insert_exclusive(replacement.pgno, role | ROLE_PREFIX_REPLACEMENT, name)?;
        }
        Ok(())
    }

    fn select(
        &mut self,
        pgno: u32,
        role: PageRole,
        private: bool,
    ) -> Result<(), RetirementWriteError> {
        let bit = match role {
            PageRole::SelectedRetirementTree => ROLE_TREE,
            PageRole::SelectedRetirementBlob => ROLE_BLOB,
            _ => unreachable!("select accepts metadata roles only"),
        };
        let (index, found) = self.locate(pgno)?;
        if found {
            let existing = self.slots[index].roles;
            let reference = match role {
                PageRole::SelectedRetirementTree => ROLE_REFERENCE_TREE,
                PageRole::SelectedRetirementBlob => ROLE_REFERENCE_BLOB,
                _ => unreachable!("select accepts metadata roles only"),
            };
            let base = if private {
                ROLE_PRIVATE | (existing & ROLE_SAFE_RECLAIMED)
            } else {
                0
            };
            let allowed = existing == base || existing == base | reference;
            let same_selection = existing == base | bit || existing == base | reference | bit;
            if same_selection && self.slots[index].selected_epoch < self.reference_epoch {
                self.slots[index].selected_epoch = self.reference_epoch;
                if existing & reference != 0 {
                    self.slots[index].reference_epoch = self.reference_epoch;
                }
                return Ok(());
            }
            if !allowed {
                return Err(RetirementWriteError::PageRoleConflict {
                    pgno,
                    existing: role_from_bits(existing),
                    requested: role,
                });
            }
            self.slots[index].roles = existing | bit;
            self.slots[index].selected_epoch = self.reference_epoch;
            if existing & reference != 0 {
                self.slots[index].reference_epoch = self.reference_epoch;
            }
            return Ok(());
        }
        if private {
            return Err(RetirementWriteError::PrivatePageUnavailable(pgno));
        }
        self.insert_at(index, pgno, bit);
        self.slots[index].selected_epoch = self.reference_epoch;
        Ok(())
    }

    fn reference(
        &mut self,
        pgno: u32,
        role: PageRole,
        private: bool,
    ) -> Result<(), RetirementWriteError> {
        let bit = match role {
            PageRole::ReferencedRetirementTree => ROLE_REFERENCE_TREE,
            PageRole::ReferencedRetirementBlob => ROLE_REFERENCE_BLOB,
            _ => unreachable!("reference accepts metadata-reference roles only"),
        };
        let (index, found) = self.locate(pgno)?;
        if found {
            let existing = self.slots[index].roles;
            let base = if private {
                ROLE_PRIVATE | (existing & ROLE_SAFE_RECLAIMED)
            } else {
                0
            };
            if private && existing == base {
                self.slots[index].roles |= bit;
                self.slots[index].reference_epoch = self.reference_epoch;
                return Ok(());
            }
            let same_reference = existing == base | bit
                || existing
                    == base
                        | bit
                        | match role {
                            PageRole::ReferencedRetirementTree => ROLE_TREE,
                            PageRole::ReferencedRetirementBlob => ROLE_BLOB,
                            _ => unreachable!(),
                        };
            if same_reference && self.slots[index].reference_epoch < self.reference_epoch {
                self.slots[index].reference_epoch = self.reference_epoch;
                return Ok(());
            }
            return Err(RetirementWriteError::PageRoleConflict {
                pgno,
                existing: role_from_bits(existing),
                requested: role,
            });
        }
        if private {
            return Err(RetirementWriteError::PrivatePageUnavailable(pgno));
        }
        self.insert_at(index, pgno, bit);
        self.slots[index].reference_epoch = self.reference_epoch;
        Ok(())
    }

    fn advance_reference_epoch(&mut self) -> Result<(), RetirementWriteError> {
        self.reference_epoch = self
            .reference_epoch
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        Ok(())
    }

    fn require_new_replacements(&mut self) {
        self.replacements_must_be_listed = true;
        for slot in self.slots.iter_mut() {
            if slot.occupied && slot.roles & (ROLE_REPLACEMENT_TREE | ROLE_REPLACEMENT_BLOB) != 0 {
                slot.roles |= ROLE_OLD_REQUIRED;
            }
        }
    }

    fn require_in_new_list(&mut self, pgno: u32) -> Result<(), RetirementWriteError> {
        let (index, found) = self.locate(pgno)?;
        if found {
            let existing = self.slots[index].roles;
            if existing & ROLE_PREFIX_REPLACEMENT != 0
                && existing & ROLE_OLD_REQUIRED != 0
                && existing & (ROLE_REPLACEMENT_TREE | ROLE_REPLACEMENT_BLOB) != 0
            {
                return Ok(());
            }
            return Err(RetirementWriteError::PageRoleConflict {
                pgno,
                existing: role_from_bits(existing),
                requested: PageRole::ListedReclaimed,
            });
        }
        self.insert_at(index, pgno, ROLE_OLD_REQUIRED);
        Ok(())
    }

    fn listed(&mut self, pgno: u32, satisfy_required: bool) -> Result<(), RetirementWriteError> {
        let (index, found) = self.locate(pgno)?;
        if found {
            let existing = self.slots[index].roles;
            if !satisfy_required
                && (existing == ROLE_SAFE_RECLAIMED
                    || existing == (ROLE_PRIVATE | ROLE_SAFE_RECLAIMED))
            {
                return Ok(());
            }
            if satisfy_required && existing & ROLE_OLD_REQUIRED != 0 {
                self.slots[index].roles = (existing & !ROLE_OLD_REQUIRED) | ROLE_LISTED;
                return Ok(());
            }
            return Err(RetirementWriteError::PageRoleConflict {
                pgno,
                existing: role_from_bits(existing),
                requested: PageRole::ListedReclaimed,
            });
        }
        self.insert_at(index, pgno, ROLE_LISTED);
        Ok(())
    }

    fn authorize_reclaimed_pages(
        &mut self,
        arena: &PrivatePageArena<'_>,
        reclamation: &RetirementReclamationAuthority<'_>,
    ) -> Result<(), RetirementWriteError> {
        let scope = arena.scope().ok_or(RetirementWriteError::StaleEditPlan(
            RetirementEditBinding::Arena,
        ))?;
        for &pgno in reclamation.pages() {
            let pool_slot = arena
                .pool()
                .find_in_scope(scope, pgno)
                .map_err(map_pool_error)?
                .ok_or(RetirementWriteError::ReclaimedPageNotConsumed(pgno))?;
            let info = arena
                .pool()
                .scoped_slot_info(scope, pool_slot)
                .map_err(map_pool_error)?
                .ok_or(RetirementWriteError::ReclaimedPageNotConsumed(pgno))?;
            if !info.bound {
                return Err(RetirementWriteError::ReclaimedPageNotConsumed(pgno));
            }
            let (index, found) = self.locate(pgno)?;
            if found {
                if self.slots[index].roles == ROLE_PRIVATE {
                    self.slots[index].roles |= ROLE_SAFE_RECLAIMED;
                    continue;
                }
                return Err(RetirementWriteError::PageRoleConflict {
                    pgno,
                    existing: role_from_bits(self.slots[index].roles),
                    requested: PageRole::ListedReclaimed,
                });
            }
            self.insert_at(index, pgno, ROLE_SAFE_RECLAIMED);
        }
        Ok(())
    }

    /// A structural reclaim probe can run before bitmap binding, or after the
    /// exact reclaimed pages have been bound into its shadow scope. In the
    /// latter case, mark the bound pages as safe for the probe's role checks
    /// without consuming the reclamation authority or changing pool state.
    fn authorize_reclaimed_pages_when_bound(
        &mut self,
        arena: &PrivatePageArena<'_>,
        reclamation: &RetirementReclamationAuthority<'_>,
    ) -> Result<(), RetirementWriteError> {
        if arena.scope().is_none() {
            return Ok(());
        }
        let mut first_missing = None;
        let mut bound = 0usize;
        for &pgno in reclamation.pages() {
            if arena.contains(pgno)? {
                bound = bound
                    .checked_add(1)
                    .ok_or(RetirementWriteError::ArithmeticOverflow)?;
            } else {
                first_missing.get_or_insert(pgno);
            }
        }
        if bound == 0 {
            return Ok(());
        }
        if let Some(pgno) = first_missing {
            return Err(RetirementWriteError::ReclaimedPageNotConsumed(pgno));
        }
        self.authorize_reclaimed_pages(arena, reclamation)
    }

    fn first_unsatisfied_required(&self) -> Option<u32> {
        self.slots
            .iter()
            .filter(|slot| slot.occupied && slot.roles & ROLE_OLD_REQUIRED != 0)
            .map(|slot| slot.pgno)
            .min()
    }

    fn prepare_scoped_release(
        &mut self,
        arena: &PrivatePageArena<'_>,
        pgno: u32,
    ) -> Result<(), RetirementWriteError> {
        let scope = arena.scope().ok_or(RetirementWriteError::StaleEditPlan(
            RetirementEditBinding::Arena,
        ))?;
        let (role_slot, found) = self.locate(pgno)?;
        if !found || self.slots[role_slot].roles != (ROLE_PRIVATE | ROLE_PRIVATE_RETIRED) {
            return Err(RetirementWriteError::PrivatePageUnavailable(pgno));
        }
        let pool_slot = arena
            .pool()
            .find_in_scope(scope, pgno)
            .map_err(map_pool_error)?
            .ok_or(RetirementWriteError::PrivatePageUnavailable(pgno))?;
        let info = arena
            .pool()
            .scoped_slot_info(scope, pool_slot)
            .map_err(map_pool_error)?
            .ok_or(RetirementWriteError::PrivatePageUnavailable(pgno))?;
        let PrivatePagePoolState::InUse {
            owner: PrivatePageOwner::Retirement,
            owner_generation,
            tag,
        } = info.state
        else {
            return Err(RetirementWriteError::PrivatePageUnavailable(pgno));
        };
        if private_origin_from_tag(tag).is_none() {
            return Err(RetirementWriteError::PrivatePageUnavailable(pgno));
        }
        info.binding_epoch
            .checked_add(2)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        self.slots[role_slot].prepared_slot = pool_slot;
        self.slots[role_slot].prepared_binding_epoch = info.binding_epoch;
        self.slots[role_slot].prepared_owner_generation = owner_generation;
        self.slots[role_slot].prepared_tag = tag;
        Ok(())
    }

    fn scoped_release_prepared(&self, pgno: u32) -> (usize, u64, u64, u64) {
        let mut current = self.root;
        loop {
            let descriptor = &self.slots[current];
            if pgno < descriptor.pgno {
                current = descriptor.left;
            } else if pgno > descriptor.pgno {
                current = descriptor.right;
            } else {
                debug_assert_ne!(descriptor.prepared_slot, usize::MAX);
                return (
                    descriptor.prepared_slot,
                    descriptor.prepared_binding_epoch,
                    descriptor.prepared_owner_generation,
                    descriptor.prepared_tag,
                );
            }
        }
    }

    fn retire_committed(
        &mut self,
        pgno: u32,
        origin: CommittedPageOrigin,
    ) -> Result<(), RetirementWriteError> {
        let expected = match origin {
            CommittedPageOrigin::RetirementTree => ROLE_TREE,
            CommittedPageOrigin::RetirementBlob => ROLE_BLOB,
        };
        let replacement = match origin {
            CommittedPageOrigin::RetirementTree => ROLE_REPLACEMENT_TREE,
            CommittedPageOrigin::RetirementBlob => ROLE_REPLACEMENT_BLOB,
        };
        let (index, found) = self.locate(pgno)?;
        if !found || self.slots[index].roles & expected == 0 {
            return Err(RetirementWriteError::PageRoleConflict {
                pgno,
                existing: if found {
                    role_from_bits(self.slots[index].roles)
                } else {
                    PageRole::PrivateAuthorization
                },
                requested: match origin {
                    CommittedPageOrigin::RetirementTree => PageRole::ReplacementRetirementTree,
                    CommittedPageOrigin::RetirementBlob => PageRole::ReplacementRetirementBlob,
                },
            });
        }
        self.slots[index].roles = replacement
            | if self.replacements_must_be_listed {
                ROLE_OLD_REQUIRED
            } else {
                0
            };
        Ok(())
    }

    fn retire_private(
        &mut self,
        pgno: u32,
        origin: PrivatePageOrigin,
    ) -> Result<(), RetirementWriteError> {
        let expected = match origin {
            PrivatePageOrigin::RetirementTree => ROLE_TREE,
            PrivatePageOrigin::RetirementBlob => ROLE_BLOB,
        };
        let (index, found) = self.locate(pgno)?;
        if !found
            || self.slots[index].roles & ROLE_PRIVATE == 0
            || self.slots[index].roles & expected == 0
        {
            return Err(RetirementWriteError::PageRoleConflict {
                pgno,
                existing: if found {
                    role_from_bits(self.slots[index].roles)
                } else {
                    PageRole::PrivateAuthorization
                },
                requested: match origin {
                    PrivatePageOrigin::RetirementTree => PageRole::ReplacementRetirementTree,
                    PrivatePageOrigin::RetirementBlob => PageRole::ReplacementRetirementBlob,
                },
            });
        }
        self.slots[index].roles = ROLE_PRIVATE | ROLE_PRIVATE_RETIRED;
        Ok(())
    }

    fn retire_prior_private(
        &mut self,
        pgno: u32,
        origin: PrivatePageOrigin,
    ) -> Result<(), RetirementWriteError> {
        let expected = match origin {
            PrivatePageOrigin::RetirementTree => ROLE_TREE,
            PrivatePageOrigin::RetirementBlob => ROLE_BLOB,
        };
        let (index, found) = self.locate(pgno)?;
        if !found
            || self.slots[index].roles & ROLE_PRIVATE != 0
            || self.slots[index].roles & expected == 0
        {
            return Err(RetirementWriteError::PageRoleConflict {
                pgno,
                existing: if found {
                    role_from_bits(self.slots[index].roles)
                } else {
                    PageRole::PrivateAuthorization
                },
                requested: match origin {
                    PrivatePageOrigin::RetirementTree => PageRole::ReplacementRetirementTree,
                    PrivatePageOrigin::RetirementBlob => PageRole::ReplacementRetirementBlob,
                },
            });
        }
        self.slots[index].roles = ROLE_PRIOR_PRIVATE_RETIRED;
        Ok(())
    }

    fn insert_exclusive(
        &mut self,
        pgno: u32,
        bit: u16,
        requested: PageRole,
    ) -> Result<(), RetirementWriteError> {
        let (index, found) = self.locate(pgno)?;
        if found {
            return Err(RetirementWriteError::PageRoleConflict {
                pgno,
                existing: role_from_bits(self.slots[index].roles),
                requested,
            });
        }
        self.insert_at(index, pgno, bit);
        Ok(())
    }

    fn locate(&self, pgno: u32) -> Result<(usize, bool), RetirementWriteError> {
        let mut current = self.root;
        while current != ROLE_NO_INDEX {
            #[cfg(test)]
            self.locate_work.set(self.locate_work.get() + 1);
            if pgno < self.slots[current].pgno {
                current = self.slots[current].left;
            } else if pgno > self.slots[current].pgno {
                current = self.slots[current].right;
            } else {
                return Ok((current, true));
            }
        }
        if self.used == self.slots.len() {
            return Err(RetirementWriteError::PageRoleIndexTooSmall {
                required: self.used.saturating_add(1),
                actual: self.slots.len(),
            });
        }
        Ok((self.used, false))
    }

    fn insert_at(&mut self, index: usize, pgno: u32, roles: u16) {
        self.slots[index] = PageRoleIndexSlot {
            pgno,
            roles,
            reference_epoch: 0,
            selected_epoch: 0,
            prepared_slot: usize::MAX,
            prepared_binding_epoch: 0,
            prepared_owner_generation: 0,
            prepared_tag: 0,
            left: ROLE_NO_INDEX,
            right: ROLE_NO_INDEX,
            height: 1,
            occupied: true,
        };
        self.used += 1;
        self.root = role_index_insert_unique(self.slots, self.root, index);
    }

    #[cfg(test)]
    fn locate_work(&self) -> usize {
        self.locate_work.get()
    }
}

fn role_index_insert_unique(
    slots: &mut [PageRoleIndexSlot],
    root: usize,
    new_index: usize,
) -> usize {
    if root == ROLE_NO_INDEX {
        return new_index;
    }

    let pgno = slots[new_index].pgno;
    if pgno < slots[root].pgno {
        slots[root].left = role_index_insert_unique(slots, slots[root].left, new_index);
    } else {
        slots[root].right = role_index_insert_unique(slots, slots[root].right, new_index);
    }
    role_index_update_height(slots, root);

    let balance = role_index_balance(slots, root);
    if balance > 1 {
        let left = slots[root].left;
        if pgno > slots[left].pgno {
            slots[root].left = role_index_rotate_left(slots, left);
        }
        return role_index_rotate_right(slots, root);
    }
    if balance < -1 {
        let right = slots[root].right;
        if pgno < slots[right].pgno {
            slots[root].right = role_index_rotate_right(slots, right);
        }
        return role_index_rotate_left(slots, root);
    }
    root
}

fn role_index_height(slots: &[PageRoleIndexSlot], index: usize) -> u8 {
    if index == ROLE_NO_INDEX {
        0
    } else {
        slots[index].height
    }
}

fn role_index_update_height(slots: &mut [PageRoleIndexSlot], index: usize) {
    slots[index].height = 1 + role_index_height(slots, slots[index].left)
        .max(role_index_height(slots, slots[index].right));
}

fn role_index_balance(slots: &[PageRoleIndexSlot], index: usize) -> i16 {
    i16::from(role_index_height(slots, slots[index].left))
        - i16::from(role_index_height(slots, slots[index].right))
}

fn role_index_rotate_left(slots: &mut [PageRoleIndexSlot], root: usize) -> usize {
    let pivot = slots[root].right;
    let middle = slots[pivot].left;
    slots[pivot].left = root;
    slots[root].right = middle;
    role_index_update_height(slots, root);
    role_index_update_height(slots, pivot);
    pivot
}

fn role_index_rotate_right(slots: &mut [PageRoleIndexSlot], root: usize) -> usize {
    let pivot = slots[root].left;
    let middle = slots[pivot].right;
    slots[pivot].right = root;
    slots[root].left = middle;
    role_index_update_height(slots, root);
    role_index_update_height(slots, pivot);
    pivot
}

fn role_from_bits(bits: u16) -> PageRole {
    if bits & (ROLE_PRIVATE | ROLE_PRIOR_PRIVATE_RETIRED) != 0 {
        PageRole::PrivateAuthorization
    } else if bits & ROLE_REFERENCE_TREE != 0 {
        PageRole::ReferencedRetirementTree
    } else if bits & ROLE_REFERENCE_BLOB != 0 {
        PageRole::ReferencedRetirementBlob
    } else if bits & ROLE_TREE != 0 {
        PageRole::SelectedRetirementTree
    } else if bits & ROLE_BLOB != 0 {
        PageRole::SelectedRetirementBlob
    } else if bits & (ROLE_LISTED | ROLE_SAFE_RECLAIMED) != 0 {
        PageRole::ListedReclaimed
    } else if bits & ROLE_OLD_REQUIRED != 0 {
        PageRole::RequiredRetirementList
    } else if bits & ROLE_REPLACEMENT_TREE != 0 {
        PageRole::ReplacementRetirementTree
    } else {
        PageRole::ReplacementRetirementBlob
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RetirementEditBinding {
    Arena,
    Pool,
    ReplacementLedger,
    ReleaseLedger,
    Roles,
    BlobToken,
    DeleteScratch,
    UpsertScratch,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RetirementWriteError {
    Source(PageSourceError),
    Header(PageHeaderError),
    BlobPage(BlobPageError),
    RetirementPage(RetirementPageError),
    PrivatePool(PrivatePagePoolError),
    PageCountOutOfRange {
        committed: u64,
        pending: u64,
    },
    PendingTransactionOutOfRange(u64),
    SelectedTransactionOutOfRange(u64),
    TransactionOrder {
        selected: u64,
        pending: u64,
    },
    SelectedTransactionOverflow(u64),
    RootCountMismatch,
    RootOutOfBounds(u32),
    PrivatePageOutOfBounds(u32),
    PrivateAuthorizationMismatch {
        pgno: u32,
        authorization: PrivatePageAuthorization,
    },
    PrivatePagesNotStrict {
        previous: u32,
        current: u32,
    },
    PrivateSlotAlreadyInUse(u32),
    PrivatePageBudgetTooSmall {
        required: usize,
        actual: usize,
    },
    BlobBuildScratchTooSmall {
        required: usize,
        actual: usize,
    },
    PrivateReleaseBufferTooSmall {
        required: usize,
        actual: usize,
    },
    PriorPrivateReleaseBufferTooSmall {
        required: usize,
        actual: usize,
    },
    PageRoleIndexTooSmall {
        required: usize,
        actual: usize,
    },
    PageRoleConflict {
        pgno: u32,
        existing: PageRole,
        requested: PageRole,
    },
    PrivatePageUnavailable(u32),
    PrivatePageOriginMismatch {
        pgno: u32,
        expected: PageRole,
    },
    CommittedParentPrivateChild {
        parent: u32,
        child: u32,
    },
    ReplacementLedgerTooSmall {
        required: usize,
        actual: usize,
    },
    PathBufferTooSmall {
        required: usize,
        actual: usize,
    },
    EmptyRetirementStream,
    RetirementStreamTooLong(u64),
    RetirementStreamCountMismatch {
        expected: u64,
        actual: u64,
    },
    RetirementStreamOrder {
        previous: u32,
        current: u32,
    },
    RetirementTreeOrder {
        previous: u64,
        current: u64,
    },
    RetirementStreamPageOutOfBounds(u32),
    PageNumberIndex(PageNumberIndexError),
    ArithmeticOverflow,
    TreeDepthExceeded,
    RootType(PageType),
    ChildType(PageType),
    ChildLevel {
        expected: u16,
        actual: u16,
    },
    ChildMaximumMismatch {
        expected: u64,
        actual: u64,
    },
    BatchCountOutOfRange(u64),
    BatchCountMismatch {
        declared: u64,
        actual: u64,
    },
    DeleteCountOutOfRange {
        requested: u64,
        available: u64,
    },
    ReclamationStateMismatch,
    ReclamationPrefixMismatch {
        expected_batches: u64,
        actual_batches: u64,
        expected_last_retired_by_txn: u64,
        actual_last_retired_by_txn: u64,
    },
    ReclaimedPageNotConsumed(u32),
    BlobOffsetMismatch {
        expected: u64,
        actual: u64,
    },
    BlobLengthMismatch {
        expected: u64,
        actual: u64,
    },
    BlobPageCountMismatch {
        declared: u64,
        actual: u64,
    },
    RetirementListOmission(u32),
    PrivateBlobNonPrivateChild {
        parent: u32,
        child: u32,
        expected_generation: u64,
    },
    BlobResidenceMismatch(u32),
    BlobTokenTransactionMismatch {
        expected: u64,
        actual: u64,
    },
    BlobTokenGenerationMismatch(u64),
    CommittedReplacementIsPrivate(u32),
    StaleEditPlan(RetirementEditBinding),
}

impl From<PageSourceError> for RetirementWriteError {
    fn from(value: PageSourceError) -> Self {
        Self::Source(value)
    }
}

impl From<PageHeaderError> for RetirementWriteError {
    fn from(value: PageHeaderError) -> Self {
        Self::Header(value)
    }
}

impl From<BlobPageError> for RetirementWriteError {
    fn from(value: BlobPageError) -> Self {
        Self::BlobPage(value)
    }
}

impl From<RetirementPageError> for RetirementWriteError {
    fn from(value: RetirementPageError) -> Self {
        Self::RetirementPage(value)
    }
}

#[derive(Debug)]
pub(crate) struct RetirementBlobToken<'arena, 'slots> {
    arena: &'arena mut PrivatePageArena<'slots>,
    root: u32,
    page_count: u64,
    byte_length: u64,
    private_pages: usize,
    generation: u64,
    cleanup_generation: u64,
    born_txn: u64,
    stabilized: bool,
    epoch: u64,
}

impl RetirementBlobToken<'_, '_> {
    pub(crate) const fn root(&self) -> u32 {
        self.root
    }

    pub(crate) const fn page_count(&self) -> u64 {
        self.page_count
    }

    pub(crate) const fn byte_length(&self) -> u64 {
        self.byte_length
    }

    pub(crate) const fn private_pages(&self) -> usize {
        self.private_pages
    }

    pub(crate) fn discard(self) {}
}

impl Drop for RetirementBlobToken<'_, '_> {
    fn drop(&mut self) {
        if !self.stabilized {
            self.arena
                .release_generation(self.cleanup_generation, PrivatePageOrigin::RetirementBlob);
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct BlobGeometry {
    value_count: u64,
    leaf_count: usize,
    total_pages: usize,
    root_level: u16,
}

pub(crate) struct RetirementBlobBuilder;

impl RetirementBlobBuilder {
    /// Return the exact private-page count needed to encode a retirement list
    /// of `value_count` page numbers, without inspecting or allocating pages.
    pub(crate) fn required_private_pages(value_count: u64) -> Result<usize, RetirementWriteError> {
        Ok(Self::geometry_for_value_count(value_count)?.total_pages)
    }

    pub(crate) fn build<'arena, 'slots>(
        pages: &[u32],
        arena: &'arena mut PrivatePageArena<'slots>,
        scratch: &mut BlobBuildScratch<'_>,
    ) -> Result<RetirementBlobToken<'arena, 'slots>, RetirementWriteError> {
        let geometry = Self::preflight(pages, arena, scratch.pgnos.len())?;
        let epoch_steps = if arena.scope().is_some() {
            geometry
                .total_pages
                .checked_mul(3)
                .and_then(|steps| steps.checked_add(2))
        } else {
            geometry
                .total_pages
                .checked_mul(4)
                .and_then(|steps| steps.checked_add(1))
        }
        .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        let checkpoint = arena.begin_reserved(epoch_steps)?;
        match Self::apply(pages, arena, scratch, geometry, &checkpoint) {
            Ok((root, private_pages)) => {
                let generation = checkpoint.generation;
                arena.commit(checkpoint, &[]);
                let born_txn = arena.born_txn;
                Ok(RetirementBlobToken {
                    arena,
                    root,
                    page_count: geometry.value_count,
                    byte_length: geometry.value_count * 4,
                    private_pages,
                    generation,
                    cleanup_generation: generation,
                    born_txn,
                    stabilized: false,
                    epoch: 1,
                })
            }
            Err(error) => {
                arena.rollback(checkpoint);
                Err(error)
            }
        }
    }

    /// Stream a private ordered page-number index into a retirement blob.
    ///
    /// The index is walked once before reservation and once while encoding;
    /// neither pass materializes an input-sized page-number slice.
    #[allow(dead_code)] // Wired into the range-root fixed point in the next slice.
    pub(crate) fn build_from_index<'arena, 'slots>(
        index: &mut PageNumberIndex<'_, '_>,
        arena: &'arena mut PrivatePageArena<'slots>,
        scratch: &mut BlobBuildScratch<'_>,
    ) -> Result<RetirementBlobToken<'arena, 'slots>, RetirementWriteError> {
        let geometry = Self::preflight_index(index, arena, scratch.pgnos.len())?;
        let epoch_steps = if arena.scope().is_some() {
            geometry
                .total_pages
                .checked_mul(3)
                .and_then(|steps| steps.checked_add(2))
        } else {
            geometry
                .total_pages
                .checked_mul(4)
                .and_then(|steps| steps.checked_add(1))
        }
        .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        let checkpoint = arena.begin_reserved(epoch_steps)?;
        match Self::apply_index(index, arena, scratch, geometry, &checkpoint) {
            Ok((root, private_pages)) => {
                let generation = checkpoint.generation;
                arena.commit(checkpoint, &[]);
                let born_txn = arena.born_txn;
                Ok(RetirementBlobToken {
                    arena,
                    root,
                    page_count: geometry.value_count,
                    byte_length: geometry.value_count * 4,
                    private_pages,
                    generation,
                    cleanup_generation: generation,
                    born_txn,
                    stabilized: false,
                    epoch: 1,
                })
            }
            Err(error) => {
                arena.rollback(checkpoint);
                Err(error)
            }
        }
    }

    fn preflight(
        pages: &[u32],
        arena: &PrivatePageArena<'_>,
        scratch_len: usize,
    ) -> Result<BlobGeometry, RetirementWriteError> {
        let value_count =
            u64::try_from(pages.len()).map_err(|_| RetirementWriteError::ArithmeticOverflow)?;
        let mut previous = None;
        for &current in pages {
            if current < 2 || u64::from(current) >= arena.committed_page_count {
                return Err(RetirementWriteError::RetirementStreamPageOutOfBounds(
                    current,
                ));
            }
            if previous.map(|prior| current <= prior).unwrap_or(false) {
                return Err(RetirementWriteError::RetirementStreamOrder {
                    previous: previous.unwrap(),
                    current,
                });
            }
            previous = Some(current);
        }

        let geometry = Self::geometry_for_value_count(value_count)?;
        arena.require_pages(geometry.total_pages)?;
        if scratch_len < geometry.total_pages {
            return Err(RetirementWriteError::BlobBuildScratchTooSmall {
                required: geometry.total_pages,
                actual: scratch_len,
            });
        }
        Ok(geometry)
    }

    fn preflight_index(
        index: &mut PageNumberIndex<'_, '_>,
        arena: &PrivatePageArena<'_>,
        scratch_len: usize,
    ) -> Result<BlobGeometry, RetirementWriteError> {
        let geometry = Self::geometry_for_value_count(index.len())?;
        let mut previous = None;
        let mut visited = 0u64;
        match index.visit_ascending(|current| -> Result<(), RetirementWriteError> {
            if visited == geometry.value_count {
                return Err(RetirementWriteError::RetirementStreamCountMismatch {
                    expected: geometry.value_count,
                    actual: visited.saturating_add(1),
                });
            }
            if current < 2 || u64::from(current) >= arena.committed_page_count {
                return Err(RetirementWriteError::RetirementStreamPageOutOfBounds(
                    current,
                ));
            }
            if previous.map(|prior| current <= prior).unwrap_or(false) {
                return Err(RetirementWriteError::RetirementStreamOrder {
                    previous: previous.unwrap(),
                    current,
                });
            }
            previous = Some(current);
            visited = visited
                .checked_add(1)
                .ok_or(RetirementWriteError::ArithmeticOverflow)?;
            Ok(())
        }) {
            Ok(()) => {}
            Err(PageNumberIndexVisitError::Index(error)) => {
                return Err(RetirementWriteError::PageNumberIndex(error));
            }
            Err(PageNumberIndexVisitError::Visitor(error)) => return Err(error),
        }
        if visited != geometry.value_count {
            return Err(RetirementWriteError::RetirementStreamCountMismatch {
                expected: geometry.value_count,
                actual: visited,
            });
        }
        arena.require_pages(geometry.total_pages)?;
        if scratch_len < geometry.total_pages {
            return Err(RetirementWriteError::BlobBuildScratchTooSmall {
                required: geometry.total_pages,
                actual: scratch_len,
            });
        }
        Ok(geometry)
    }

    fn geometry_for_value_count(value_count: u64) -> Result<BlobGeometry, RetirementWriteError> {
        if value_count == 0 {
            return Err(RetirementWriteError::EmptyRetirementStream);
        }
        if value_count > MAX_BATCH_PAGE_COUNT {
            return Err(RetirementWriteError::RetirementStreamTooLong(value_count));
        }

        let leaf_count_u64 = value_count
            .checked_add(RETIREMENT_VALUES_PER_BLOB_LEAF - 1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?
            / RETIREMENT_VALUES_PER_BLOB_LEAF;
        let leaf_count = usize::try_from(leaf_count_u64)
            .map_err(|_| RetirementWriteError::ArithmeticOverflow)?;
        let mut nodes = leaf_count;
        let mut total_pages = leaf_count;
        let mut root_level = 0u16;
        while nodes > 1 {
            nodes = nodes
                .checked_add(BLOB_BRANCH_CAPACITY - 1)
                .ok_or(RetirementWriteError::ArithmeticOverflow)?
                / BLOB_BRANCH_CAPACITY;
            total_pages = total_pages
                .checked_add(nodes)
                .ok_or(RetirementWriteError::ArithmeticOverflow)?;
            root_level = root_level
                .checked_add(1)
                .ok_or(RetirementWriteError::TreeDepthExceeded)?;
            if root_level > MAX_TREE_LEVEL {
                return Err(RetirementWriteError::TreeDepthExceeded);
            }
        }
        Ok(BlobGeometry {
            value_count,
            leaf_count,
            total_pages,
            root_level,
        })
    }

    fn apply(
        pages: &[u32],
        arena: &mut PrivatePageArena<'_>,
        scratch: &mut BlobBuildScratch<'_>,
        geometry: BlobGeometry,
        checkpoint: &ArenaCheckpoint<'_>,
    ) -> Result<(u32, usize), RetirementWriteError> {
        for index in 0..geometry.total_pages {
            scratch.pgnos[index] = arena.allocate(checkpoint, PrivatePageOrigin::RetirementBlob)?;
        }
        let mut value_index = 0u64;
        for leaf_index in 0..geometry.leaf_count {
            let remaining = geometry.value_count - value_index;
            let values = remaining.min(RETIREMENT_VALUES_PER_BLOB_LEAF);
            let logical_offset = value_index * 4;
            let mut page = [0u8; PAGE_SIZE];
            encode_blob_leaf(
                &mut page,
                arena.born_txn,
                logical_offset,
                values,
                |offset| pages[(value_index + offset) as usize],
            );
            arena.write_page(checkpoint, scratch.pgnos[leaf_index], &page);
            value_index += values;
        }

        let mut input_start = 0usize;
        let mut input_count = geometry.leaf_count;
        let mut output_start = geometry.leaf_count;
        let mut level = 1u16;
        while input_count > 1 {
            let output_count = input_count.div_ceil(BLOB_BRANCH_CAPACITY);
            for output_index in 0..output_count {
                let child_start = input_start + output_index * BLOB_BRANCH_CAPACITY;
                let child_count =
                    (input_count - output_index * BLOB_BRANCH_CAPACITY).min(BLOB_BRANCH_CAPACITY);
                let destination_index = output_start + output_index;
                let mut page = [0u8; PAGE_SIZE];
                encode_blob_branch_refs(
                    &mut page,
                    arena.born_txn,
                    level,
                    &scratch.pgnos[child_start..child_start + child_count],
                    arena,
                );
                arena.write_page(checkpoint, scratch.pgnos[destination_index], &page);
            }
            input_start = output_start;
            input_count = output_count;
            output_start += output_count;
            level += 1;
        }
        debug_assert_eq!(level - 1, geometry.root_level);
        Ok((scratch.pgnos[input_start], geometry.total_pages))
    }

    fn apply_index(
        index: &mut PageNumberIndex<'_, '_>,
        arena: &mut PrivatePageArena<'_>,
        scratch: &mut BlobBuildScratch<'_>,
        geometry: BlobGeometry,
        checkpoint: &ArenaCheckpoint<'_>,
    ) -> Result<(u32, usize), RetirementWriteError> {
        for output_index in 0..geometry.total_pages {
            scratch.pgnos[output_index] =
                arena.allocate(checkpoint, PrivatePageOrigin::RetirementBlob)?;
        }

        let mut leaf = [0u8; PAGE_SIZE];
        let mut previous = None;
        let mut visited = 0u64;
        match index.visit_ascending(|current| -> Result<(), RetirementWriteError> {
            if visited == geometry.value_count {
                return Err(RetirementWriteError::RetirementStreamCountMismatch {
                    expected: geometry.value_count,
                    actual: visited.saturating_add(1),
                });
            }
            if current < 2 || u64::from(current) >= arena.committed_page_count {
                return Err(RetirementWriteError::RetirementStreamPageOutOfBounds(
                    current,
                ));
            }
            if previous.map(|prior| current <= prior).unwrap_or(false) {
                return Err(RetirementWriteError::RetirementStreamOrder {
                    previous: previous.unwrap(),
                    current,
                });
            }
            let leaf_index = usize::try_from(visited / RETIREMENT_VALUES_PER_BLOB_LEAF)
                .map_err(|_| RetirementWriteError::ArithmeticOverflow)?;
            let value_index = usize::try_from(visited % RETIREMENT_VALUES_PER_BLOB_LEAF)
                .map_err(|_| RetirementWriteError::ArithmeticOverflow)?;
            let leaf_start = u64::try_from(leaf_index)
                .map_err(|_| RetirementWriteError::ArithmeticOverflow)?
                * RETIREMENT_VALUES_PER_BLOB_LEAF;
            let leaf_values =
                (geometry.value_count - leaf_start).min(RETIREMENT_VALUES_PER_BLOB_LEAF);
            if value_index == 0 {
                init_blob_leaf(&mut leaf, arena.born_txn, visited * 4, leaf_values);
            }
            let at = 48 + value_index * 4;
            leaf[at..at + 4].copy_from_slice(&current.to_le_bytes());
            previous = Some(current);
            visited = visited
                .checked_add(1)
                .ok_or(RetirementWriteError::ArithmeticOverflow)?;
            if u64::try_from(value_index + 1)
                .map_err(|_| RetirementWriteError::ArithmeticOverflow)?
                == leaf_values
            {
                page::write_crc32c(&mut leaf);
                arena.write_page(checkpoint, scratch.pgnos[leaf_index], &leaf);
            }
            Ok(())
        }) {
            Ok(()) => {}
            Err(PageNumberIndexVisitError::Index(error)) => {
                return Err(RetirementWriteError::PageNumberIndex(error));
            }
            Err(PageNumberIndexVisitError::Visitor(error)) => return Err(error),
        }
        if visited != geometry.value_count {
            return Err(RetirementWriteError::RetirementStreamCountMismatch {
                expected: geometry.value_count,
                actual: visited,
            });
        }

        let mut input_start = 0usize;
        let mut input_count = geometry.leaf_count;
        let mut output_start = geometry.leaf_count;
        let mut level = 1u16;
        while input_count > 1 {
            let output_count = input_count.div_ceil(BLOB_BRANCH_CAPACITY);
            for output_index in 0..output_count {
                let child_start = input_start + output_index * BLOB_BRANCH_CAPACITY;
                let child_count =
                    (input_count - output_index * BLOB_BRANCH_CAPACITY).min(BLOB_BRANCH_CAPACITY);
                let destination_index = output_start + output_index;
                let mut page = [0u8; PAGE_SIZE];
                encode_blob_branch_refs(
                    &mut page,
                    arena.born_txn,
                    level,
                    &scratch.pgnos[child_start..child_start + child_count],
                    arena,
                );
                arena.write_page(checkpoint, scratch.pgnos[destination_index], &page);
            }
            input_start = output_start;
            input_count = output_count;
            output_start += output_count;
            level += 1;
        }
        debug_assert_eq!(level - 1, geometry.root_level);
        Ok((scratch.pgnos[input_start], geometry.total_pages))
    }
}

fn init_blob_leaf(page: &mut [u8; PAGE_SIZE], born_txn: u64, logical_offset: u64, values: u64) {
    page.fill(0);
    let data_len = u16::try_from(values * 4).unwrap();
    PageHeader {
        page_type: PageType::BlobLeaf,
        born_txn,
        item_count: 1,
        level: 0,
        lower: 48 + data_len,
        upper: PAGE_SIZE as u16,
        aux: BlobKind::RetirementPageList as u32,
        page_crc32c: 0,
    }
    .encode_into(page);
    page[32..40].copy_from_slice(&logical_offset.to_le_bytes());
    page[40..42].copy_from_slice(&data_len.to_le_bytes());
}

fn encode_blob_leaf(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    logical_offset: u64,
    values: u64,
    mut value_at: impl FnMut(u64) -> u32,
) {
    init_blob_leaf(page, born_txn, logical_offset, values);
    for index in 0..values {
        let at = 48 + index as usize * 4;
        page[at..at + 4].copy_from_slice(&value_at(index).to_le_bytes());
    }
    page::write_crc32c(page);
}

fn encode_blob_branch_refs(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    level: u16,
    children: &[u32],
    arena: &PrivatePageArena<'_>,
) {
    debug_assert!(!children.is_empty());
    debug_assert!(children.len() <= BLOB_BRANCH_CAPACITY);
    page.fill(0);
    PageHeader {
        page_type: PageType::BlobBranch,
        born_txn,
        item_count: children.len() as u16,
        level,
        lower: (PAGE_HEADER_SIZE as usize + children.len() * 16) as u16,
        upper: PAGE_SIZE as u16,
        aux: BlobKind::RetirementPageList as u32,
        page_crc32c: 0,
    }
    .encode_into(page);
    for (index, &child_pgno) in children.iter().enumerate() {
        let logical_offset = arena
            .child_logical_offset(child_pgno)
            .expect("blob child lookup remains valid")
            .expect("blob child remains in its authorized private slot");
        let at = PAGE_HEADER_SIZE as usize + index * 16;
        page[at..at + 8].copy_from_slice(&logical_offset.to_le_bytes());
        page[at + 8..at + 12].copy_from_slice(&child_pgno.to_le_bytes());
    }
    page::write_crc32c(page);
}

fn child_logical_offset(page: &[u8; PAGE_SIZE]) -> u64 {
    u64::from_le_bytes(page[32..40].try_into().unwrap())
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RetirementTreeState {
    pub(crate) selected_txn: u64,
    pub(crate) page_count: u64,
    pub(crate) root: u32,
    pub(crate) batch_count: u64,
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct RetirementPathFrame {
    pgno: u32,
    level: u16,
    decode_txn: u64,
    private: bool,
    prior_private: Option<DraftPrivatePageLocation>,
    page: [u8; PAGE_SIZE],
    keep_from: u16,
    destination_slot: usize,
    destination_binding_epoch: u64,
    scratch_epoch: u64,
}

impl RetirementPathFrame {
    pub(crate) const fn new() -> Self {
        Self {
            pgno: 0,
            level: 0,
            decode_txn: 0,
            private: false,
            prior_private: None,
            page: [0; PAGE_SIZE],
            keep_from: 0,
            destination_slot: usize::MAX,
            destination_binding_epoch: 0,
            scratch_epoch: 0,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RetirementTreeEditResult {
    pub(crate) root: u32,
    pub(crate) batch_count: u64,
    pub(crate) private_pages: usize,
    pub(crate) committed_replacements: usize,
    pub(crate) prior_private_replacements: usize,
}

/// Private evidence for the bitmap root carried through a produced terminal
/// journal. A range-only update can legitimately leave the selected bitmap
/// root unchanged, but no caller may substitute a different old root.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ProducedBitmapRootProvenance {
    Empty,
    Terminal(u32),
    SelectedUnchanged(u32),
}

impl ProducedBitmapRootProvenance {
    pub(crate) fn validates(self, root: u32, pages: &[PrivatePageCoordinatorTerminalPage]) -> bool {
        let has_bitmap_page = pages
            .iter()
            .any(|page| page.owner == PrivatePageOwner::Bitmap);
        match self {
            Self::Empty => root == 0 && !has_bitmap_page,
            Self::Terminal(expected) => {
                root == expected
                    && expected >= 2
                    && pages
                        .iter()
                        .any(|page| page.pgno == expected && page.owner == PrivatePageOwner::Bitmap)
            }
            Self::SelectedUnchanged(expected) => {
                root == expected && expected >= 2 && !has_bitmap_page
            }
        }
    }
}

pub(crate) struct PreparedRetirementTerminalExport<'pages> {
    result: RetirementTreeEditResult,
    pages: &'pages mut [PrivatePageCoordinatorTerminalPage],
}

pub(crate) struct PreparedCombinedRetirementTerminalExport<'pages> {
    result: RetirementTreeEditResult,
    pages: &'pages mut [PrivatePageCoordinatorTerminalPage],
}

pub(crate) struct PreparedProducedTerminalExport<'pages, B> {
    result: RetirementTreeEditResult,
    range: Option<RangeTreeMaterializedResult>,
    bitmap: B,
    bitmap_root_provenance: ProducedBitmapRootProvenance,
    pages: &'pages mut [PrivatePageCoordinatorTerminalPage],
}

pub(crate) struct PreparedProducedTerminalRebind {
    _private: (),
}

impl PreparedRetirementTerminalExport<'_> {
    pub(crate) const fn result(&self) -> RetirementTreeEditResult {
        self.result
    }

    pub(crate) fn pages(&self) -> &[PrivatePageCoordinatorTerminalPage] {
        self.pages
    }
}

impl<'pages> PreparedRetirementTerminalExport<'pages> {
    #[allow(clippy::result_large_err, clippy::type_complexity)]
    pub(crate) fn merge_with_bitmap_export<
        'combined,
        'bitmap_pages,
        'bitmap_scratch,
        'bitmap_pool,
        'bitmap_slots,
        'bitmap_scope,
        S: CommittedPageSource + ?Sized,
    >(
        self,
        bitmap: PreparedFreeBitmapTerminalExport<
            'bitmap_pages,
            'bitmap_scratch,
            'bitmap_pool,
            'bitmap_slots,
            'bitmap_scope,
            S,
        >,
        combined: &'combined mut [PrivatePageCoordinatorTerminalPage],
    ) -> Result<
        PreparedProducedTerminalExport<
            'combined,
            PreparedFreeBitmapTerminalExport<
                'bitmap_pages,
                'bitmap_scratch,
                'bitmap_pool,
                'bitmap_slots,
                'bitmap_scope,
                S,
            >,
        >,
        (
            Self,
            PreparedFreeBitmapTerminalExport<
                'bitmap_pages,
                'bitmap_scratch,
                'bitmap_pool,
                'bitmap_slots,
                'bitmap_scope,
                S,
            >,
            &'combined mut [PrivatePageCoordinatorTerminalPage],
            RetirementWriteError,
        ),
    > {
        let bitmap_root_provenance = match bitmap.produced_bitmap_root_provenance() {
            Some(provenance) => provenance,
            None => {
                return Err((
                    self,
                    bitmap,
                    combined,
                    RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena),
                ));
            }
        };
        let required = match bitmap.pages().len().checked_add(self.pages.len()) {
            Some(required) => required,
            None => {
                return Err((
                    self,
                    bitmap,
                    combined,
                    RetirementWriteError::ArithmeticOverflow,
                ));
            }
        };
        if combined.len() != required {
            return Err((
                self,
                bitmap,
                combined,
                RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena),
            ));
        }
        if merge_unbound_terminal_page_journals([bitmap.pages(), self.pages, &[]], combined)
            .is_err()
        {
            return Err((
                self,
                bitmap,
                combined,
                RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena),
            ));
        }
        Ok(PreparedProducedTerminalExport {
            result: self.result,
            range: None,
            bitmap,
            bitmap_root_provenance,
            pages: combined,
        })
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)]
    pub(crate) fn merge_with_bitmap_pages<'combined>(
        self,
        bitmap_pages: &[PrivatePageCoordinatorTerminalPage],
        combined: &'combined mut [PrivatePageCoordinatorTerminalPage],
    ) -> Result<
        PreparedCombinedRetirementTerminalExport<'combined>,
        (
            Self,
            &'combined mut [PrivatePageCoordinatorTerminalPage],
            RetirementWriteError,
        ),
    > {
        let required = match bitmap_pages.len().checked_add(self.pages.len()) {
            Some(required) => required,
            None => {
                return Err((self, combined, RetirementWriteError::ArithmeticOverflow));
            }
        };
        if combined.len() != required
            || bitmap_pages
                .iter()
                .any(|page| page.owner != PrivatePageOwner::Bitmap)
        {
            return Err((
                self,
                combined,
                RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena),
            ));
        }
        if merge_unbound_terminal_page_journals([bitmap_pages, self.pages, &[]], combined).is_err()
        {
            return Err((
                self,
                combined,
                RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena),
            ));
        }
        Ok(PreparedCombinedRetirementTerminalExport {
            result: self.result,
            pages: combined,
        })
    }
}

impl<
        'bitmap_pages,
        'range_pages,
        'retirement_pages,
        'scratch,
        'a,
        'slots,
        'scope,
        S: CommittedPageSource + ?Sized,
    >
    PreparedFreeBitmapRangeRetirementTerminalExport<
        'bitmap_pages,
        'range_pages,
        'retirement_pages,
        'scratch,
        'a,
        'slots,
        'scope,
        S,
    >
{
    /// Merges the only proof-bound three-owner terminal journals. The typed
    /// exporter remains attached to the produced work so the generic binder
    /// cannot replace its range or retirement authority.
    #[allow(clippy::result_large_err)]
    pub(crate) fn merge_terminal_journals<'combined>(
        self,
        combined: &'combined mut [PrivatePageCoordinatorTerminalPage],
    ) -> Result<
        PreparedProducedTerminalExport<'combined, Self>,
        (
            Self,
            &'combined mut [PrivatePageCoordinatorTerminalPage],
            RetirementWriteError,
        ),
    > {
        let bitmap_root_provenance = match self.produced_bitmap_root_provenance() {
            Some(provenance) => provenance,
            None => {
                return Err((
                    self,
                    combined,
                    RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena),
                ));
            }
        };
        let required = match self
            .range_pages()
            .len()
            .checked_add(self.bitmap_pages().len())
            .and_then(|count| count.checked_add(self.retirement_pages().len()))
        {
            Some(required) => required,
            None => return Err((self, combined, RetirementWriteError::ArithmeticOverflow)),
        };
        if combined.len() != required
            || self
                .range_pages()
                .iter()
                .any(|page| page.owner != PrivatePageOwner::Range)
            || self
                .bitmap_pages()
                .iter()
                .any(|page| page.owner != PrivatePageOwner::Bitmap)
            || self
                .retirement_pages()
                .iter()
                .any(|page| page.owner != PrivatePageOwner::Retirement)
        {
            return Err((
                self,
                combined,
                RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena),
            ));
        }
        if merge_unbound_terminal_page_journals(
            [
                self.range_pages(),
                self.bitmap_pages(),
                self.retirement_pages(),
            ],
            combined,
        )
        .is_err()
        {
            return Err((
                self,
                combined,
                RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena),
            ));
        }
        let result = self.retirement();
        let range = self.materialized();
        Ok(PreparedProducedTerminalExport {
            result,
            range: Some(range),
            bitmap: self,
            bitmap_root_provenance,
            pages: combined,
        })
    }
}

impl<'pages> PreparedCombinedRetirementTerminalExport<'pages> {
    #[cfg(test)]
    #[allow(clippy::type_complexity, clippy::result_large_err)]
    pub(crate) fn bind_to_prepared_work<'slot, 'scope_slot, 'scratch, 'carried>(
        self,
        prepared: FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>,
        pool: &PrivatePagePool<'_>,
        nonce: u64,
    ) -> Result<
        FixedPointPreparedRetirementTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'pages>,
        (
            FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>,
            Self,
            FixedPointError,
        ),
    > {
        let result = self.result;
        let pages = self.pages;
        match prepared.with_unbound_terminal_pages(pool, pages, nonce) {
            Ok(terminal) => Ok(FixedPointPreparedRetirementTerminalWork::new(
                terminal, result,
            )),
            Err((prepared, pages, error)) => Err((prepared, Self { result, pages }, error)),
        }
    }
}

impl<'pages, B> PreparedProducedTerminalExport<'pages, B> {
    pub(crate) fn pages(&self) -> &[PrivatePageCoordinatorTerminalPage] {
        self.pages
    }

    #[cfg(test)]
    pub(crate) const fn retirement_result(&self) -> RetirementTreeEditResult {
        self.result
    }

    pub(crate) fn bitmap(&self) -> &B {
        &self.bitmap
    }

    pub(crate) const fn bitmap_root_provenance(&self) -> ProducedBitmapRootProvenance {
        self.bitmap_root_provenance
    }

    #[cfg(test)]
    pub(crate) const fn range_target(&self) -> Option<RangeTreeMaterializedResult> {
        self.range
    }

    pub(crate) fn from_bind_parts(
        result: RetirementTreeEditResult,
        range: Option<RangeTreeMaterializedResult>,
        bitmap: B,
        bitmap_root_provenance: ProducedBitmapRootProvenance,
        pages: &'pages mut [PrivatePageCoordinatorTerminalPage],
        _rebind: PreparedProducedTerminalRebind,
    ) -> Self {
        Self {
            result,
            range,
            bitmap,
            bitmap_root_provenance,
            pages,
        }
    }

    pub(crate) fn into_bind_parts(
        self,
    ) -> (
        RetirementTreeEditResult,
        Option<RangeTreeMaterializedResult>,
        B,
        ProducedBitmapRootProvenance,
        &'pages mut [PrivatePageCoordinatorTerminalPage],
        PreparedProducedTerminalRebind,
    ) {
        (
            self.result,
            self.range,
            self.bitmap,
            self.bitmap_root_provenance,
            self.pages,
            PreparedProducedTerminalRebind { _private: () },
        )
    }

    #[allow(clippy::type_complexity, clippy::result_large_err)]
    pub(crate) fn bind_to_prepared_work<'slot, 'scope_slot, 'scratch, 'carried>(
        self,
        prepared: FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>,
        pool: &PrivatePagePool<'_>,
        nonce: u64,
    ) -> Result<
        FixedPointPreparedProducedTerminalWork<'slot, 'scope_slot, 'scratch, 'carried, 'pages, B>,
        (
            FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>,
            Self,
            FixedPointError,
        ),
    > {
        prepared.with_produced_terminal_export(pool, self, nonce)
    }
}

impl PrivatePageArena<'_> {
    pub(crate) fn prepare_terminal_export<'pages>(
        &self,
        result: RetirementTreeEditResult,
        pages: &'pages mut [PrivatePageCoordinatorTerminalPage],
    ) -> Result<PreparedRetirementTerminalExport<'pages>, RetirementWriteError> {
        if self.terminal_result.get() != Some(result)
            || self.in_use_count()? != pages.len()
            || self.scope().is_none()
        {
            return Err(RetirementWriteError::StaleEditPlan(
                RetirementEditBinding::Arena,
            ));
        }
        self.pool()
            .export_retirement_scope_terminal_pages(
                self.scope()
                    .expect("shadow export requires one exact scope"),
                pages,
            )
            .map_err(map_pool_error)?;
        if (result.root == 0) != pages.is_empty()
            || (result.root != 0
                && !pages.iter().any(|page| {
                    page.pgno == result.root
                        && page.owner == PrivatePageOwner::Retirement
                        && page.tag == 1
                }))
        {
            pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            return Err(RetirementWriteError::StaleEditPlan(
                RetirementEditBinding::Arena,
            ));
        }
        Ok(PreparedRetirementTerminalExport { result, pages })
    }
}

#[derive(Clone, Copy, Debug)]
struct ChildReference {
    maximum: u64,
    pgno: u32,
    level: u16,
}

#[derive(Clone, Copy, Debug)]
struct AppendPlan {
    path_len: usize,
    pages: usize,
    old_root: ChildReference,
    mode: UpsertMode,
}

#[derive(Clone, Copy, Debug)]
enum UpsertMode {
    Append,
    Replace(RetirementBatch),
}

#[derive(Clone, Copy, Debug)]
enum PageResidence {
    Committed,
    CurrentPrivate {
        generation: u64,
    },
    PriorPrivate {
        generation: u64,
        location: DraftPrivatePageLocation,
    },
}

#[derive(Clone, Copy, Debug)]
enum BlobResidenceExpectation {
    DeriveAtRoot,
    Committed,
    Private(u64),
}

#[derive(Clone, Copy, Debug)]
enum ListedPagePolicy {
    Register,
    MarkRequired,
    SatisfyRequired,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct LedgerBinding {
    entries: *const CommittedPageReplacement,
    capacity: usize,
    len: usize,
    fingerprint: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct ReleaseBinding {
    entries: *const u32,
    capacity: usize,
    len: usize,
    fingerprint: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct RoleBinding {
    entries: *const PageRoleIndexSlot,
    capacity: usize,
    root: usize,
    used: usize,
    reference_epoch: u8,
    replacements_must_be_listed: bool,
    fingerprint: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct BlobTokenBinding {
    root: u32,
    page_count: u64,
    byte_length: u64,
    private_pages: usize,
    generation: u64,
    cleanup_generation: u64,
    born_txn: u64,
    stabilized: bool,
    epoch: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct ScratchBinding {
    entries: *const RetirementPathFrame,
    capacity: usize,
    epoch: u64,
    len: usize,
    full_fingerprint: u64,
    fingerprints: [u32; RETIREMENT_PATH_CAPACITY],
}

#[derive(Clone, Copy, Debug)]
struct PlannedDestination {
    slot: usize,
    pgno: u32,
    binding_epoch: u64,
}

impl PlannedDestination {
    const NONE: Self = Self {
        slot: usize::MAX,
        pgno: 0,
        binding_epoch: 0,
    };
}

#[derive(Debug)]
struct DestinationCursor {
    next_slot: usize,
    selected: usize,
    #[cfg(test)]
    probes: usize,
}

impl DestinationCursor {
    const fn new() -> Self {
        Self {
            next_slot: 0,
            selected: 0,
            #[cfg(test)]
            probes: 0,
        }
    }

    fn take(
        &mut self,
        arena: &PrivatePageArena<'_>,
        snapshot: &RetirementPoolFence<'_>,
    ) -> Result<PlannedDestination, RetirementWriteError> {
        arena.validate_fence(snapshot)?;
        if let Some(scope) = arena.scope() {
            let slot = arena
                .pool()
                .available_slot_at_rank_in_scope(scope, self.selected)
                .map_err(map_pool_error)?;
            let info = arena
                .pool()
                .scoped_slot_info(scope, slot)
                .map_err(map_pool_error)?
                .ok_or(RetirementWriteError::PrivatePageUnavailable(0))?;
            if !info.bound || info.state != PrivatePagePoolState::Available {
                return Err(RetirementWriteError::PrivatePageUnavailable(info.pgno));
            }
            self.selected += 1;
            #[cfg(test)]
            {
                self.probes += 1;
            }
            return Ok(PlannedDestination {
                slot,
                pgno: info.pgno,
                binding_epoch: info.binding_epoch,
            });
        }
        while self.next_slot < arena.pool().len() {
            let slot = self.next_slot;
            self.next_slot += 1;
            #[cfg(test)]
            {
                self.probes += 1;
            }
            if arena.pool().state(slot).map_err(map_pool_error)? == PrivatePagePoolState::Available
            {
                self.selected += 1;
                return Ok(PlannedDestination {
                    slot,
                    pgno: arena.pool().page_number(slot).map_err(map_pool_error)?,
                    binding_epoch: 0,
                });
            }
        }
        Err(RetirementWriteError::PrivatePageBudgetTooSmall {
            required: arena
                .in_use_count()?
                .saturating_add(self.selected)
                .saturating_add(1),
            actual: arena.in_use_count()?.saturating_add(arena.available()?),
        })
    }
}

#[derive(Clone, Copy, Debug)]
struct VirtualTreeOverlay<'a> {
    frames: &'a [RetirementPathFrame],
    generation: u64,
}

impl VirtualTreeOverlay<'_> {
    fn find(&self, pgno: u32) -> Option<&RetirementPathFrame> {
        self.frames.iter().find(|frame| frame.pgno == pgno)
    }
}

impl ScratchBinding {
    const fn empty() -> Self {
        Self {
            entries: core::ptr::null(),
            capacity: 0,
            epoch: 0,
            len: 0,
            full_fingerprint: 0,
            fingerprints: [0; RETIREMENT_PATH_CAPACITY],
        }
    }
}

fn next_scratch_epoch(path: &[RetirementPathFrame]) -> Result<u64, RetirementWriteError> {
    match path.first() {
        Some(frame) => frame
            .scratch_epoch
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow),
        None => Ok(1),
    }
}

fn scratch_fingerprint(bytes: &[u8; PAGE_SIZE]) -> u32 {
    crc32c_with_zeroed(bytes, bytes.len(), 0).expect("empty checked range is valid")
}

const RETIREMENT_HASH_OFFSET: u64 = 1_469_598_103_934_665_603;
const RETIREMENT_HASH_PRIME: u64 = 1_099_511_628_211;

#[cfg(test)]
std::thread_local! {
    static RETIREMENT_CONTENT_SEAL_WORK: Cell<usize> = const { Cell::new(0) };
    static RETIREMENT_CALLBACK_GUARD_WORK: Cell<usize> = const { Cell::new(0) };
    static RETIREMENT_PRIVATE_SNAPSHOT_READS: Cell<usize> = const { Cell::new(0) };
}

#[cfg(test)]
fn record_content_seal_work(items: usize) {
    RETIREMENT_CONTENT_SEAL_WORK.with(|work| work.set(work.get().saturating_add(items)));
}

#[cfg(test)]
fn reset_content_seal_work() {
    RETIREMENT_CONTENT_SEAL_WORK.with(|work| work.set(0));
}

#[cfg(test)]
fn content_seal_work() -> usize {
    RETIREMENT_CONTENT_SEAL_WORK.with(Cell::get)
}

#[cfg(test)]
fn record_callback_guard_work() {
    RETIREMENT_CALLBACK_GUARD_WORK.with(|work| work.set(work.get().saturating_add(1)));
}

#[cfg(test)]
fn reset_callback_guard_work() {
    RETIREMENT_CALLBACK_GUARD_WORK.with(|work| work.set(0));
}

#[cfg(test)]
fn callback_guard_work() -> usize {
    RETIREMENT_CALLBACK_GUARD_WORK.with(Cell::get)
}

#[cfg(test)]
fn reset_private_snapshot_reads() {
    RETIREMENT_PRIVATE_SNAPSHOT_READS.with(|reads| reads.set(0));
}

#[cfg(test)]
fn record_private_snapshot_read() {
    RETIREMENT_PRIVATE_SNAPSHOT_READS.with(|reads| reads.set(reads.get().saturating_add(1)));
}

#[cfg(test)]
fn private_snapshot_reads() -> usize {
    RETIREMENT_PRIVATE_SNAPSHOT_READS.with(Cell::get)
}

fn retirement_hash_u64(mut hash: u64, value: u64) -> u64 {
    for byte in value.to_le_bytes() {
        hash ^= u64::from(byte);
        hash = hash.wrapping_mul(RETIREMENT_HASH_PRIME);
    }
    hash
}

fn retirement_hash_bytes(mut hash: u64, bytes: &[u8]) -> u64 {
    for &byte in bytes {
        hash ^= u64::from(byte);
        hash = hash.wrapping_mul(RETIREMENT_HASH_PRIME);
    }
    hash
}

fn path_fingerprint(path: &[RetirementPathFrame]) -> u64 {
    #[cfg(test)]
    record_content_seal_work(path.len());
    let mut hash = retirement_hash_u64(RETIREMENT_HASH_OFFSET, path.len() as u64);
    for frame in path {
        for value in [
            u64::from(frame.pgno),
            u64::from(frame.level),
            frame.decode_txn,
            u64::from(frame.private),
            u64::from(frame.keep_from),
            frame.destination_slot as u64,
            frame.destination_binding_epoch,
            frame.scratch_epoch,
        ] {
            hash = retirement_hash_u64(hash, value);
        }
        hash = retirement_hash_u64(hash, u64::from(frame.prior_private.is_some()));
        if let Some(location) = frame.prior_private {
            let DraftPageProvenance::Private { work_unit, page } = location.provenance else {
                unreachable!("prior-private frame carries exact private provenance")
            };
            for value in [
                work_unit,
                location.nonce,
                location.record_index as u64,
                location.binding_index as u64,
                page.scope_id,
                page.scope_anchor as u64,
                page.scope_generation,
                page.slot as u64,
                u64::from(page.pgno),
                page.binding_epoch,
                page.owner as u64,
                page.owner_generation,
                page.tag,
            ] {
                hash = retirement_hash_u64(hash, value);
            }
        }
        hash = retirement_hash_bytes(hash, &frame.page);
    }
    hash
}

fn bind_scratch(path: &[RetirementPathFrame], len: usize, epoch: u64) -> ScratchBinding {
    let mut binding = ScratchBinding {
        entries: path.as_ptr(),
        capacity: path.len(),
        epoch,
        len,
        full_fingerprint: path_fingerprint(path),
        fingerprints: [0; RETIREMENT_PATH_CAPACITY],
    };
    for (index, frame) in path[..len].iter().enumerate() {
        binding.fingerprints[index] = scratch_fingerprint(&frame.page);
    }
    binding
}

fn ledger_binding(ledger: &CommittedReplacementLedger<'_>) -> LedgerBinding {
    #[cfg(test)]
    record_content_seal_work(ledger.entries.len());
    let mut fingerprint = RETIREMENT_HASH_OFFSET;
    for entry in ledger.entries.iter() {
        fingerprint = retirement_hash_u64(fingerprint, u64::from(entry.pgno));
        fingerprint = retirement_hash_u64(fingerprint, entry.origin as u64);
    }
    LedgerBinding {
        entries: ledger.entries.as_ptr(),
        capacity: ledger.entries.len(),
        len: ledger.len,
        fingerprint,
    }
}

fn release_binding(releases: &PrivateReleaseBuffer<'_>) -> ReleaseBinding {
    #[cfg(test)]
    record_content_seal_work(releases.pgnos.len());
    let mut fingerprint = RETIREMENT_HASH_OFFSET;
    for &pgno in releases.pgnos.iter() {
        fingerprint = retirement_hash_u64(fingerprint, u64::from(pgno));
    }
    ReleaseBinding {
        entries: releases.pgnos.as_ptr(),
        capacity: releases.pgnos.len(),
        len: releases.len,
        fingerprint,
    }
}

fn role_binding(roles: &PageRoleIndex<'_>) -> RoleBinding {
    #[cfg(test)]
    record_content_seal_work(roles.slots.len());
    let mut fingerprint = RETIREMENT_HASH_OFFSET;
    for slot in roles.slots.iter() {
        for value in [
            u64::from(slot.pgno),
            u64::from(slot.roles),
            u64::from(slot.reference_epoch),
            u64::from(slot.selected_epoch),
            slot.prepared_slot as u64,
            slot.prepared_binding_epoch,
            slot.prepared_owner_generation,
            slot.prepared_tag,
            slot.left as u64,
            slot.right as u64,
            u64::from(slot.height),
            u64::from(slot.occupied),
        ] {
            fingerprint = retirement_hash_u64(fingerprint, value);
        }
    }
    RoleBinding {
        entries: roles.slots.as_ptr(),
        capacity: roles.slots.len(),
        root: roles.root,
        used: roles.used,
        reference_epoch: roles.reference_epoch,
        replacements_must_be_listed: roles.replacements_must_be_listed,
        fingerprint,
    }
}

fn arena_header_fingerprint(arena: &PrivatePageArena<'_>) -> u64 {
    let mut hash = RETIREMENT_HASH_OFFSET;
    for value in [
        arena.pool() as *const PrivatePagePool<'_> as usize as u64,
        u64::from(arena.scope().is_some()),
        arena.committed_page_count,
        arena.pending_page_count,
        arena.born_txn,
        arena.generation.get(),
    ] {
        hash = retirement_hash_u64(hash, value);
    }
    hash
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct CallbackBufferHeaders {
    replacement_entries: *const CommittedPageReplacement,
    replacement_capacity: usize,
    replacement_len: usize,
    release_entries: *const u32,
    release_capacity: usize,
    release_len: usize,
    role_entries: *const PageRoleIndexSlot,
    role_capacity: usize,
    role_root: usize,
    role_used: usize,
    role_reference_epoch: u8,
    replacements_must_be_listed: bool,
}

struct GuardedRetirementSource<
    'source,
    'arena,
    'slots,
    'entries,
    'release_entries,
    'role_entries,
    S: RetirementMetadataSource + ?Sized,
> {
    source: &'source S,
    arena: *const PrivatePageArena<'slots>,
    replacements: *const CommittedReplacementLedger<'entries>,
    releases: *const PrivateReleaseBuffer<'release_entries>,
    roles: *const PageRoleIndex<'role_entries>,
    token: Option<*const RetirementBlobToken<'arena, 'slots>>,
    drift: Cell<Option<RetirementEditBinding>>,
}

impl<
        'source,
        'arena,
        'slots,
        'entries,
        'release_entries,
        'role_entries,
        S: RetirementMetadataSource + ?Sized,
    >
    GuardedRetirementSource<'source, 'arena, 'slots, 'entries, 'release_entries, 'role_entries, S>
{
    #[allow(clippy::too_many_arguments)]
    fn new(
        source: &'source S,
        arena: &PrivatePageArena<'slots>,
        _delete_path: Option<&[RetirementPathFrame]>,
        _upsert_path: Option<&[RetirementPathFrame]>,
        replacements: &CommittedReplacementLedger<'entries>,
        releases: &PrivateReleaseBuffer<'release_entries>,
        roles: &PageRoleIndex<'role_entries>,
        token: Option<&RetirementBlobToken<'arena, 'slots>>,
    ) -> Self {
        Self {
            source,
            arena,
            replacements,
            releases,
            roles,
            token: token.map(|token| token as *const _),
            drift: Cell::new(None),
        }
    }

    fn record_drift(&self, binding: RetirementEditBinding) {
        if self.drift.get().is_none() {
            self.drift.set(Some(binding));
        }
    }

    fn buffer_headers(&self) -> CallbackBufferHeaders {
        // SAFETY: the outer planning call exclusively owns all three objects
        // for the guard lifetime. This reads only their fixed-size headers.
        let replacements = unsafe { &*self.replacements };
        let releases = unsafe { &*self.releases };
        let roles = unsafe { &*self.roles };
        CallbackBufferHeaders {
            replacement_entries: replacements.entries.as_ptr(),
            replacement_capacity: replacements.entries.len(),
            replacement_len: replacements.len,
            release_entries: releases.pgnos.as_ptr(),
            release_capacity: releases.pgnos.len(),
            release_len: releases.len,
            role_entries: roles.slots.as_ptr(),
            role_capacity: roles.slots.len(),
            role_root: roles.root,
            role_used: roles.used,
            role_reference_epoch: roles.reference_epoch,
            replacements_must_be_listed: roles.replacements_must_be_listed,
        }
    }

    fn invoke<T, E: From<PageSourceError>>(
        &self,
        callback: impl FnOnce() -> Result<T, E>,
    ) -> Result<T, E> {
        #[cfg(test)]
        record_callback_guard_work();
        // SAFETY: every raw pointer is derived from an argument held
        // exclusively by the outer planning call. No planner mutation occurs
        // while the source callback is executing. Rust's aliasing rules prevent
        // safe callbacks from changing buffer contents. The constant-time seals
        // below cover every interior-mutable header, while the final planning
        // callback receives one full bounded content validation.
        let arena = unsafe { &*self.arena };
        let before_pool_epoch = arena.pool().mutation_epoch();
        let before_arena = arena_header_fingerprint(arena);
        let before_arena_generation = arena.generation.get();
        let before_headers = self.buffer_headers();
        let before_token = self.token.map(|token| blob_binding(unsafe { &*token }));
        let result = callback();

        let binding = if before_arena != arena_header_fingerprint(arena) {
            Some(RetirementEditBinding::Arena)
        } else if before_pool_epoch != arena.pool().mutation_epoch() {
            Some(RetirementEditBinding::Pool)
        } else if before_headers != self.buffer_headers() {
            if before_headers.replacement_len != unsafe { &*self.replacements }.len {
                Some(RetirementEditBinding::ReplacementLedger)
            } else if before_headers.release_len != unsafe { &*self.releases }.len {
                Some(RetirementEditBinding::ReleaseLedger)
            } else {
                Some(RetirementEditBinding::Roles)
            }
        } else if before_token != self.token.map(|token| blob_binding(unsafe { &*token })) {
            Some(RetirementEditBinding::BlobToken)
        } else {
            None
        };
        if let Some(binding) = binding {
            match binding {
                RetirementEditBinding::Arena => arena.generation.set(before_arena_generation),
                RetirementEditBinding::ReplacementLedger => unsafe {
                    (*(self.replacements as *mut CommittedReplacementLedger<'entries>)).len =
                        before_headers.replacement_len;
                },
                RetirementEditBinding::ReleaseLedger => unsafe {
                    (*(self.releases as *mut PrivateReleaseBuffer<'release_entries>)).len =
                        before_headers.release_len;
                },
                RetirementEditBinding::Roles => unsafe {
                    let roles = &mut *(self.roles as *mut PageRoleIndex<'role_entries>);
                    roles.root = before_headers.role_root;
                    roles.used = before_headers.role_used;
                    roles.reference_epoch = before_headers.role_reference_epoch;
                    roles.replacements_must_be_listed = before_headers.replacements_must_be_listed;
                },
                RetirementEditBinding::BlobToken => {
                    if let (Some(token), Some(before)) = (self.token, before_token) {
                        unsafe {
                            let token = &mut *(token as *mut RetirementBlobToken<'arena, 'slots>);
                            token.root = before.root;
                            token.page_count = before.page_count;
                            token.byte_length = before.byte_length;
                            token.private_pages = before.private_pages;
                            token.generation = before.generation;
                            token.cleanup_generation = before.cleanup_generation;
                            token.born_txn = before.born_txn;
                            token.stabilized = before.stabilized;
                            token.epoch = before.epoch;
                        }
                    }
                }
                _ => {}
            }
            self.record_drift(binding);
            Err(PageSourceError::ForkedHandle.into())
        } else {
            result
        }
    }

    fn resolve<T>(
        &self,
        result: Result<T, RetirementWriteError>,
    ) -> Result<T, RetirementWriteError> {
        match self.drift.get() {
            Some(binding) => Err(RetirementWriteError::StaleEditPlan(binding)),
            None => result,
        }
    }
}

impl<
        'arena,
        'slots,
        'entries,
        'release_entries,
        'role_entries,
        S: RetirementMetadataSource + ?Sized,
    > RetirementMetadataSource
    for GuardedRetirementSource<'_, 'arena, 'slots, 'entries, 'release_entries, 'role_entries, S>
{
    fn check_retirement_access(&self) -> Result<(), PageSourceError> {
        self.invoke(|| self.source.check_retirement_access())
    }

    fn read_non_current_retirement_page(
        &self,
        pgno: u32,
        expected_kind: FixedPointRetirementPageKind,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<FixedPointRetirementResidence, RetirementWriteError> {
        self.invoke(|| {
            self.source
                .read_non_current_retirement_page(pgno, expected_kind, destination)
        })
    }

    fn prior_private_location(
        &self,
        pgno: u32,
        expected_kind: FixedPointRetirementPageKind,
    ) -> Result<Option<DraftPrivatePageLocation>, RetirementWriteError> {
        self.source.prior_private_location(pgno, expected_kind)
    }

    fn prior_release_len(&self) -> usize {
        self.source.prior_release_len()
    }

    fn stage_prior_release(
        &self,
        index: usize,
        location: DraftPrivatePageLocation,
    ) -> Result<(), RetirementWriteError> {
        self.source.stage_prior_release(index, location)
    }

    fn validate_prior_release_commit(
        &self,
        base: usize,
        count: usize,
    ) -> Result<(), RetirementWriteError> {
        self.source.validate_prior_release_commit(base, count)
    }

    fn commit_prior_releases_prepared(&self, base: usize, count: usize) {
        self.source.commit_prior_releases_prepared(base, count);
    }
}

fn blob_binding(token: &RetirementBlobToken<'_, '_>) -> BlobTokenBinding {
    BlobTokenBinding {
        root: token.root,
        page_count: token.page_count,
        byte_length: token.byte_length,
        private_pages: token.private_pages,
        generation: token.generation,
        cleanup_generation: token.cleanup_generation,
        born_txn: token.born_txn,
        stabilized: token.stabilized,
        epoch: token.epoch,
    }
}

#[derive(Debug)]
enum RetirementPlanArena<'plan, 'arena, 'slots> {
    Direct(&'plan mut PrivatePageArena<'slots>),
    Token(&'plan mut RetirementBlobToken<'arena, 'slots>),
}

impl<'slots> RetirementPlanArena<'_, '_, 'slots> {
    fn arena(&self) -> &PrivatePageArena<'slots> {
        match self {
            Self::Direct(arena) => arena,
            Self::Token(token) => token.arena,
        }
    }

    fn token(&self) -> Option<&RetirementBlobToken<'_, 'slots>> {
        match self {
            Self::Direct(_) => None,
            Self::Token(token) => Some(token),
        }
    }
}

#[derive(Debug)]
pub(crate) struct RetirementEditPlanInner<
    'plan,
    'arena,
    'slots,
    'entries,
    'release_entries,
    'role_entries,
    S: RetirementMetadataSource + ?Sized,
> {
    source: &'plan S,
    state: RetirementTreeState,
    arena: RetirementPlanArena<'plan, 'arena, 'slots>,
    delete_path: Option<&'plan mut [RetirementPathFrame]>,
    upsert_path: Option<&'plan mut [RetirementPathFrame]>,
    replacements: &'plan mut CommittedReplacementLedger<'entries>,
    releases: &'plan mut PrivateReleaseBuffer<'release_entries>,
    roles: &'plan mut PageRoleIndex<'role_entries>,
    pool_snapshot: RetirementPoolFence<'slots>,
    arena_generation: u64,
    replacement_binding: LedgerBinding,
    release_binding: ReleaseBinding,
    role_binding: RoleBinding,
    blob_binding: Option<BlobTokenBinding>,
    delete_scratch: ScratchBinding,
    upsert_scratch: ScratchBinding,
    staged_replacements: usize,
    staged_releases: usize,
    prior_release_base: usize,
    staged_prior_releases: usize,
    result: RetirementTreeEditResult,
    #[cfg(test)]
    destination_probes: usize,
}

#[derive(Debug)]
pub(crate) enum RetirementEditPlan<
    'plan,
    'arena,
    'slots,
    'entries,
    'release_entries,
    'role_entries,
    S: RetirementMetadataSource + ?Sized,
> {
    Upsert(
        RetirementEditPlanInner<
            'plan,
            'arena,
            'slots,
            'entries,
            'release_entries,
            'role_entries,
            S,
        >,
    ),
    Delete(
        RetirementEditPlanInner<
            'plan,
            'arena,
            'slots,
            'entries,
            'release_entries,
            'role_entries,
            S,
        >,
    ),
    Combined(
        RetirementEditPlanInner<
            'plan,
            'arena,
            'slots,
            'entries,
            'release_entries,
            'role_entries,
            S,
        >,
    ),
}

fn source_destination(
    frame: &RetirementPathFrame,
    arena: &PrivatePageArena<'_>,
    snapshot: &RetirementPoolFence<'_>,
    destinations: &mut DestinationCursor,
) -> Result<PlannedDestination, RetirementWriteError> {
    if frame.destination_slot != usize::MAX && frame.scratch_epoch != 0 {
        return Ok(PlannedDestination {
            slot: frame.destination_slot,
            pgno: frame.pgno,
            binding_epoch: frame.destination_binding_epoch,
        });
    }
    destinations.take(arena, snapshot)
}

fn finish_planned_frame(
    frame: &mut RetirementPathFrame,
    destination: PlannedDestination,
    level: u16,
    born_txn: u64,
    scratch_epoch: u64,
    bytes: [u8; PAGE_SIZE],
) {
    frame.pgno = destination.pgno;
    frame.level = level;
    frame.decode_txn = born_txn;
    frame.private = true;
    frame.prior_private = None;
    frame.page = bytes;
    frame.keep_from = 0;
    frame.destination_slot = destination.slot;
    frame.destination_binding_epoch = destination.binding_epoch;
    frame.scratch_epoch = scratch_epoch;
}

#[allow(clippy::too_many_arguments)]
fn prepare_upsert_pages(
    state: RetirementTreeState,
    batch: RetirementBatch,
    arena: &PrivatePageArena<'_>,
    snapshot: &RetirementPoolFence<'_>,
    path: &mut [RetirementPathFrame],
    plan: AppendPlan,
    destinations: &mut DestinationCursor,
    scratch_epoch: u64,
) -> Result<(RetirementTreeEditResult, ScratchBinding), RetirementWriteError> {
    if plan.pages > path.len() || plan.pages > RETIREMENT_PATH_CAPACITY {
        return Err(RetirementWriteError::PathBufferTooSmall {
            required: plan.pages,
            actual: path.len(),
        });
    }
    if state.root == 0 {
        let destination = destinations.take(arena, snapshot)?;
        let mut bytes = [0u8; PAGE_SIZE];
        encode_retirement_leaf_single(&mut bytes, arena.born_txn, batch);
        finish_planned_frame(
            &mut path[0],
            destination,
            0,
            arena.born_txn,
            scratch_epoch,
            bytes,
        );
        return Ok((
            RetirementTreeEditResult {
                root: destination.pgno,
                batch_count: 1,
                private_pages: 1,
                committed_replacements: 0,
                prior_private_replacements: 0,
            },
            bind_scratch(path, 1, scratch_epoch),
        ));
    }

    let leaf_depth = plan.path_len - 1;
    let leaf_destination = source_destination(&path[leaf_depth], arena, snapshot, destinations)?;
    let leaf = RetirementLeaf::open(
        &path[leaf_depth].page,
        path[leaf_depth].decode_txn,
        arena.pending_page_count,
    )?;
    let mut bytes = [0u8; PAGE_SIZE];
    let mut carry = match plan.mode {
        UpsertMode::Replace(_) => {
            encode_retirement_leaf_with_replace(&mut bytes, arena.born_txn, leaf, batch);
            false
        }
        UpsertMode::Append if leaf.len() < RETIREMENT_LEAF_CAPACITY => {
            encode_retirement_leaf_with_append(&mut bytes, arena.born_txn, leaf, batch);
            false
        }
        UpsertMode::Append => {
            encode_retirement_leaf_single(&mut bytes, arena.born_txn, batch);
            true
        }
    };
    finish_planned_frame(
        &mut path[leaf_depth],
        leaf_destination,
        0,
        arena.born_txn,
        scratch_epoch,
        bytes,
    );
    let mut current = ChildReference {
        maximum: batch.retired_by_txn,
        pgno: leaf_destination.pgno,
        level: 0,
    };

    for depth in (0..leaf_depth).rev() {
        let destination = source_destination(&path[depth], arena, snapshot, destinations)?;
        let branch = RetirementBranch::open(
            &path[depth].page,
            path[depth].decode_txn,
            arena.pending_page_count,
        )?;
        let level = branch.level();
        let mut bytes = [0u8; PAGE_SIZE];
        if carry && branch.len() == RETIREMENT_BRANCH_CAPACITY {
            encode_retirement_branch_single(&mut bytes, arena.born_txn, branch.level(), current);
        } else {
            encode_retirement_branch_right_edit(&mut bytes, arena.born_txn, branch, current, carry);
            carry = false;
        }
        finish_planned_frame(
            &mut path[depth],
            destination,
            level,
            arena.born_txn,
            scratch_epoch,
            bytes,
        );
        current = ChildReference {
            maximum: current.maximum,
            pgno: destination.pgno,
            level,
        };
    }
    if carry {
        let destination = destinations.take(arena, snapshot)?;
        let level = plan.old_root.level + 1;
        let mut bytes = [0u8; PAGE_SIZE];
        encode_retirement_branch_two(&mut bytes, arena.born_txn, level, plan.old_root, current);
        finish_planned_frame(
            &mut path[plan.path_len],
            destination,
            level,
            arena.born_txn,
            scratch_epoch,
            bytes,
        );
        current = ChildReference {
            maximum: current.maximum,
            pgno: destination.pgno,
            level,
        };
    }
    let batch_count = match plan.mode {
        UpsertMode::Append => state
            .batch_count
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?,
        UpsertMode::Replace(_) => state.batch_count,
    };
    Ok((
        RetirementTreeEditResult {
            root: current.pgno,
            batch_count,
            private_pages: plan.pages,
            committed_replacements: 0,
            prior_private_replacements: 0,
        },
        bind_scratch(path, plan.pages, scratch_epoch),
    ))
}

fn prepare_delete_pages(
    state: RetirementTreeState,
    arena: &PrivatePageArena<'_>,
    snapshot: &RetirementPoolFence<'_>,
    path: &mut [RetirementPathFrame],
    plan: DeletePlan,
    destinations: &mut DestinationCursor,
    scratch_epoch: u64,
) -> Result<(RetirementTreeEditResult, ScratchBinding), RetirementWriteError> {
    if plan.pages > path.len() || plan.pages > RETIREMENT_PATH_CAPACITY {
        return Err(RetirementWriteError::PathBufferTooSmall {
            required: plan.pages,
            actual: path.len(),
        });
    }
    let Some(boundary) = plan.boundary else {
        return Ok((
            RetirementTreeEditResult {
                root: 0,
                batch_count: 0,
                private_pages: 0,
                committed_replacements: 0,
                prior_private_replacements: 0,
            },
            bind_scratch(path, 0, scratch_epoch),
        ));
    };
    let mut reserved = [PlannedDestination::NONE; RETIREMENT_PATH_CAPACITY];
    for destination in &mut reserved[..plan.pages] {
        *destination = destinations.take(arena, snapshot)?;
    }
    let mut used = 0usize;
    let mut current = boundary.base;
    if boundary.partial_leaf {
        let depth = boundary.deepest_depth;
        let leaf = RetirementLeaf::open(
            &path[depth].page,
            path[depth].decode_txn,
            arena.pending_page_count,
        )?;
        let maximum = leaf.maximum_key()?;
        let mut bytes = [0u8; PAGE_SIZE];
        encode_retirement_leaf_suffix(
            &mut bytes,
            arena.born_txn,
            leaf,
            usize::from(path[depth].keep_from),
        );
        let destination = reserved[used];
        used += 1;
        finish_planned_frame(
            &mut path[depth],
            destination,
            0,
            arena.born_txn,
            scratch_epoch,
            bytes,
        );
        current = ChildReference {
            maximum,
            pgno: destination.pgno,
            level: 0,
        };
    }
    if let Some(root_depth) = boundary.new_root_depth {
        for depth in (root_depth..=boundary.deepest_depth).rev() {
            if path[depth].level == 0 {
                continue;
            }
            let branch = RetirementBranch::open(
                &path[depth].page,
                path[depth].decode_txn,
                arena.pending_page_count,
            )?;
            let maximum = branch.maximum_key()?;
            let level = branch.level();
            let mut bytes = [0u8; PAGE_SIZE];
            encode_retirement_branch_suffix(
                &mut bytes,
                arena.born_txn,
                branch,
                usize::from(path[depth].keep_from),
                current,
            );
            let destination = reserved[used];
            used += 1;
            finish_planned_frame(
                &mut path[depth],
                destination,
                level,
                arena.born_txn,
                scratch_epoch,
                bytes,
            );
            current = ChildReference {
                maximum,
                pgno: destination.pgno,
                level,
            };
        }
    }
    debug_assert_eq!(used, plan.pages);
    let mut output = 0usize;
    for depth in 0..=boundary.deepest_depth {
        if path[depth].scratch_epoch != scratch_epoch {
            continue;
        }
        if output != depth {
            path[output] = path[depth];
        }
        output += 1;
    }
    debug_assert_eq!(output, plan.pages);
    Ok((
        RetirementTreeEditResult {
            root: current.pgno,
            batch_count: state.batch_count,
            private_pages: plan.pages,
            committed_replacements: 0,
            prior_private_replacements: 0,
        },
        bind_scratch(path, plan.pages, scratch_epoch),
    ))
}

fn output_reused(pgno: u32, upsert: Option<&[RetirementPathFrame]>) -> bool {
    upsert
        .map(|frames| frames.iter().any(|frame| frame.pgno == pgno))
        .unwrap_or(false)
}

fn validate_scratch_binding(
    path: Option<&[RetirementPathFrame]>,
    binding: ScratchBinding,
    stale: RetirementEditBinding,
) -> Result<(), RetirementWriteError> {
    if binding.entries.is_null() {
        return if path.is_none() {
            Ok(())
        } else {
            Err(RetirementWriteError::StaleEditPlan(stale))
        };
    }
    let path = path.ok_or(RetirementWriteError::StaleEditPlan(stale))?;
    if binding.entries != path.as_ptr()
        || binding.capacity != path.len()
        || binding.full_fingerprint != path_fingerprint(path)
        || binding.len > path.len()
        || binding.len > RETIREMENT_PATH_CAPACITY
    {
        return Err(RetirementWriteError::StaleEditPlan(stale));
    }
    for (index, frame) in path[..binding.len].iter().enumerate() {
        if frame.scratch_epoch != binding.epoch
            || frame.destination_slot == usize::MAX
            || scratch_fingerprint(&frame.page) != binding.fingerprints[index]
        {
            return Err(RetirementWriteError::StaleEditPlan(stale));
        }
    }
    Ok(())
}

fn validate_plan_bindings<S: RetirementMetadataSource + ?Sized>(
    inner: &RetirementEditPlanInner<'_, '_, '_, '_, '_, '_, S>,
) -> Result<(), RetirementWriteError> {
    let arena = inner.arena.arena();
    arena
        .validate_fence(&inner.pool_snapshot)
        .map_err(|_| RetirementWriteError::StaleEditPlan(RetirementEditBinding::Pool))?;
    if arena.generation.get() != inner.arena_generation {
        return Err(RetirementWriteError::StaleEditPlan(
            RetirementEditBinding::Arena,
        ));
    }
    if ledger_binding(inner.replacements) != inner.replacement_binding {
        return Err(RetirementWriteError::StaleEditPlan(
            RetirementEditBinding::ReplacementLedger,
        ));
    }
    if release_binding(inner.releases) != inner.release_binding {
        return Err(RetirementWriteError::StaleEditPlan(
            RetirementEditBinding::ReleaseLedger,
        ));
    }
    if role_binding(inner.roles) != inner.role_binding {
        return Err(RetirementWriteError::StaleEditPlan(
            RetirementEditBinding::Roles,
        ));
    }
    if inner.arena.token().map(blob_binding) != inner.blob_binding {
        return Err(RetirementWriteError::StaleEditPlan(
            RetirementEditBinding::BlobToken,
        ));
    }
    validate_scratch_binding(
        inner.delete_path.as_deref(),
        inner.delete_scratch,
        RetirementEditBinding::DeleteScratch,
    )?;
    validate_scratch_binding(
        inner.upsert_path.as_deref(),
        inner.upsert_scratch,
        RetirementEditBinding::UpsertScratch,
    )?;
    let replacement_end = inner
        .replacement_binding
        .len
        .checked_add(inner.staged_replacements)
        .ok_or(RetirementWriteError::ArithmeticOverflow)?;
    if replacement_end > inner.replacement_binding.capacity {
        return Err(RetirementWriteError::ReplacementLedgerTooSmall {
            required: replacement_end,
            actual: inner.replacement_binding.capacity,
        });
    }
    let release_end = inner
        .release_binding
        .len
        .checked_add(inner.staged_releases)
        .ok_or(RetirementWriteError::ArithmeticOverflow)?;
    if release_end > inner.release_binding.capacity {
        return Err(RetirementWriteError::PrivateReleaseBufferTooSmall {
            required: release_end,
            actual: inner.release_binding.capacity,
        });
    }
    if inner.blob_binding.is_some() {
        inner
            .blob_binding
            .unwrap()
            .epoch
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn validate_callback_bindings(
    arena: &PrivatePageArena<'_>,
    pool: &RetirementPoolFence<'_>,
    replacements: &CommittedReplacementLedger<'_>,
    expected_replacements: LedgerBinding,
    releases: &PrivateReleaseBuffer<'_>,
    expected_releases: ReleaseBinding,
    roles: &PageRoleIndex<'_>,
    expected_roles: RoleBinding,
    token: Option<&RetirementBlobToken<'_, '_>>,
    expected_token: Option<BlobTokenBinding>,
    delete_path: Option<&[RetirementPathFrame]>,
    delete_scratch: ScratchBinding,
    upsert_path: Option<&[RetirementPathFrame]>,
    upsert_scratch: ScratchBinding,
) -> Result<(), RetirementWriteError> {
    arena
        .validate_fence(pool)
        .map_err(|_| RetirementWriteError::StaleEditPlan(RetirementEditBinding::Pool))?;
    if ledger_binding(replacements) != expected_replacements {
        return Err(RetirementWriteError::StaleEditPlan(
            RetirementEditBinding::ReplacementLedger,
        ));
    }
    if release_binding(releases) != expected_releases {
        return Err(RetirementWriteError::StaleEditPlan(
            RetirementEditBinding::ReleaseLedger,
        ));
    }
    if role_binding(roles) != expected_roles {
        return Err(RetirementWriteError::StaleEditPlan(
            RetirementEditBinding::Roles,
        ));
    }
    if token.map(blob_binding) != expected_token {
        return Err(RetirementWriteError::StaleEditPlan(
            RetirementEditBinding::BlobToken,
        ));
    }
    validate_scratch_binding(
        delete_path,
        delete_scratch,
        RetirementEditBinding::DeleteScratch,
    )?;
    validate_scratch_binding(
        upsert_path,
        upsert_scratch,
        RetirementEditBinding::UpsertScratch,
    )
}

fn restore_plan_header_after_callback<S: RetirementMetadataSource + ?Sized>(
    inner: &mut RetirementEditPlanInner<'_, '_, '_, '_, '_, '_, S>,
    binding: RetirementEditBinding,
) {
    match binding {
        RetirementEditBinding::Arena => inner.arena.arena().generation.set(inner.arena_generation),
        RetirementEditBinding::ReplacementLedger => {
            inner.replacements.len = inner.replacement_binding.len;
        }
        RetirementEditBinding::ReleaseLedger => {
            inner.releases.len = inner.release_binding.len;
        }
        RetirementEditBinding::Roles => {
            inner.roles.root = inner.role_binding.root;
            inner.roles.used = inner.role_binding.used;
            inner.roles.reference_epoch = inner.role_binding.reference_epoch;
            inner.roles.replacements_must_be_listed =
                inner.role_binding.replacements_must_be_listed;
        }
        RetirementEditBinding::BlobToken => {
            if let (RetirementPlanArena::Token(token), Some(binding)) =
                (&mut inner.arena, inner.blob_binding)
            {
                token.root = binding.root;
                token.page_count = binding.page_count;
                token.byte_length = binding.byte_length;
                token.private_pages = binding.private_pages;
                token.generation = binding.generation;
                token.cleanup_generation = binding.cleanup_generation;
                token.born_txn = binding.born_txn;
                token.stabilized = binding.stabilized;
                token.epoch = binding.epoch;
            }
        }
        _ => {}
    }
}

fn validate_destination(
    arena: &PrivatePageArena<'_>,
    frame: &RetirementPathFrame,
) -> Result<(), RetirementWriteError> {
    if let Some(scope) = arena.scope() {
        let info = arena
            .pool()
            .scoped_slot_info(scope, frame.destination_slot)
            .map_err(map_pool_error)?
            .ok_or(RetirementWriteError::PrivatePageUnavailable(frame.pgno))?;
        if !info.bound
            || info.pgno != frame.pgno
            || info.binding_epoch != frame.destination_binding_epoch
            || info.state != PrivatePagePoolState::Available
        {
            return Err(RetirementWriteError::PrivatePageUnavailable(frame.pgno));
        }
        info.binding_epoch
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        Ok(())
    } else {
        let actual = arena
            .pool()
            .page_number(frame.destination_slot)
            .map_err(map_pool_error)?;
        if actual != frame.pgno {
            return Err(RetirementWriteError::PrivatePageUnavailable(frame.pgno));
        }
        arena
            .pool()
            .validate_available(frame.destination_slot)
            .map_err(map_pool_error)
    }
}

fn validate_release(arena: &PrivatePageArena<'_>, pgno: u32) -> Result<(), RetirementWriteError> {
    let slot = match arena.scope() {
        Some(scope) => arena.pool().find_in_scope(scope, pgno),
        None => arena.pool().find(pgno),
    }
    .map_err(map_pool_error)?
    .ok_or(RetirementWriteError::PrivatePageUnavailable(pgno))?;
    let state = match arena.scope() {
        Some(scope) => {
            arena
                .pool()
                .scoped_slot_info(scope, slot)
                .map_err(map_pool_error)?
                .ok_or(RetirementWriteError::PrivatePageUnavailable(pgno))?
                .state
        }
        None => arena.pool().state(slot).map_err(map_pool_error)?,
    };
    match state {
        PrivatePagePoolState::InUse {
            owner: PrivatePageOwner::Retirement,
            owner_generation,
            tag,
        } if private_origin_from_tag(tag).is_some()
            && arena.private_state(pgno)?
                == Some(RetirementPrivatePageState::InUse {
                    origin: private_origin_from_tag(tag).unwrap(),
                    generation: owner_generation,
                }) =>
        {
            Ok(())
        }
        PrivatePagePoolState::InUse { owner: actual, .. }
        | PrivatePagePoolState::PendingReturn { owner: actual, .. }
            if actual != PrivatePageOwner::Retirement =>
        {
            Err(map_pool_error(PrivatePagePoolError::OwnerMismatch {
                pgno,
                expected: PrivatePageOwner::Retirement,
                actual,
            }))
        }
        _ => Err(RetirementWriteError::PrivatePageUnavailable(pgno)),
    }
}

impl<
        'plan,
        'arena,
        'slots,
        'entries,
        'release_entries,
        'role_entries,
        S: RetirementMetadataSource + ?Sized,
    > RetirementEditPlan<'plan, 'arena, 'slots, 'entries, 'release_entries, 'role_entries, S>
{
    pub(crate) fn apply(self) -> Result<RetirementTreeEditResult, RetirementWriteError> {
        self.apply_inner()
    }

    fn apply_inner(self) -> Result<RetirementTreeEditResult, RetirementWriteError> {
        let mut inner = match self {
            Self::Upsert(inner) | Self::Delete(inner) | Self::Combined(inner) => inner,
        };
        let source_access = inner.source.check_retirement_access();
        if let Err(error) = validate_plan_bindings(&inner) {
            if let RetirementWriteError::StaleEditPlan(binding) = error {
                restore_plan_header_after_callback(&mut inner, binding);
            }
            return Err(error);
        }
        inner
            .source
            .validate_prior_release_commit(inner.prior_release_base, inner.staged_prior_releases)?;
        source_access?;
        let arena = inner.arena.arena();
        let delete = inner
            .delete_path
            .as_deref()
            .map(|path| &path[..inner.delete_scratch.len]);
        let upsert = inner
            .upsert_path
            .as_deref()
            .map(|path| &path[..inner.upsert_scratch.len]);
        let unique_delete = delete
            .map(|frames| {
                frames
                    .iter()
                    .filter(|frame| !output_reused(frame.pgno, upsert))
                    .count()
            })
            .unwrap_or(0);
        let output_count = unique_delete
            .checked_add(upsert.map_or(0, <[RetirementPathFrame]>::len))
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        let output_epoch_steps = if arena.scope().is_some() {
            output_count
        } else {
            output_count
                .checked_mul(2)
                .ok_or(RetirementWriteError::ArithmeticOverflow)?
        };
        // Unscoped release refreshes authority before return; scoped prepared
        // release already carries exact authority. Commit finalizes both.
        let release_epoch_steps = inner
            .staged_releases
            .checked_mul(if arena.scope().is_some() { 2 } else { 3 })
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        let epoch_steps = output_epoch_steps
            .checked_add(release_epoch_steps)
            .and_then(|steps| steps.checked_add(2))
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        match (&inner.pool_snapshot, arena.scope()) {
            (RetirementPoolFence::Scoped(commitment), Some(scope)) => arena
                .pool()
                .preflight_mutation_in_scope(scope, commitment, epoch_steps)
                .map_err(map_pool_error)?,
            (RetirementPoolFence::Unscoped(snapshot), None) => arena
                .pool()
                .preflight_mutation(snapshot, epoch_steps)
                .map_err(map_pool_error)?,
            _ => {
                return Err(RetirementWriteError::StaleEditPlan(
                    RetirementEditBinding::Arena,
                ));
            }
        }
        if let Some(frames) = delete {
            for frame in frames {
                validate_destination(arena, frame)?;
            }
        }
        if let Some(frames) = upsert {
            for frame in frames {
                validate_destination(arena, frame)?;
            }
        }
        let release_start = inner.release_binding.len;
        let release_end = release_start + inner.staged_releases;
        for &pgno in &inner.releases.pgnos[release_start..release_end] {
            validate_release(arena, pgno)?;
            if arena.scope().is_some() {
                inner.roles.prepare_scoped_release(arena, pgno)?;
            }
        }
        if output_count == 0
            && inner.staged_releases == 0
            && inner.staged_replacements == 0
            && inner.staged_prior_releases == 0
        {
            arena.terminal_result.set(Some(inner.result));
            return Ok(inner.result);
        }

        let checkpoint = arena.begin_reserved(epoch_steps)?;
        if let Some(frames) = delete {
            for frame in frames {
                if !output_reused(frame.pgno, upsert) {
                    arena.install_planned_page(
                        &checkpoint,
                        frame.destination_slot,
                        frame.pgno,
                        frame.destination_binding_epoch,
                        checkpoint.generation,
                        &frame.page,
                    );
                }
            }
        }
        if let Some(frames) = upsert {
            for frame in frames {
                arena.install_planned_page(
                    &checkpoint,
                    frame.destination_slot,
                    frame.pgno,
                    frame.destination_binding_epoch,
                    checkpoint.generation,
                    &frame.page,
                );
            }
        }
        if let Some(scope) = arena.scope() {
            for &pgno in &inner.releases.pgnos[release_start..release_end] {
                let (slot, binding_epoch, owner_generation, tag) =
                    inner.roles.scoped_release_prepared(pgno);
                arena.pool().return_slot_in_scope_for_checkpoint_prepared(
                    &checkpoint.pool,
                    scope,
                    slot,
                    binding_epoch,
                    PrivatePageOwner::Retirement,
                    owner_generation,
                    tag,
                    PrivatePageReturn::Available,
                );
            }
            arena
                .pool()
                .commit_checkpoint_in_scope_prepared(checkpoint.pool, scope);
            arena.generation.set(checkpoint.generation);
        } else {
            arena.commit(
                checkpoint,
                &inner.releases.pgnos[release_start..release_end],
            );
        }
        inner.replacements.len = inner.replacement_binding.len + inner.staged_replacements;
        inner.releases.len = inner.release_binding.len;
        inner
            .source
            .commit_prior_releases_prepared(inner.prior_release_base, inner.staged_prior_releases);
        arena.terminal_result.set(Some(inner.result));
        if let RetirementPlanArena::Token(token) = &mut inner.arena {
            token.stabilized = true;
            token.epoch += 1;
        }
        Ok(inner.result)
    }
}

impl RetirementTreeEditor {
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn plan_upsert_newest<
        'plan,
        'arena,
        'slots,
        'entries,
        'release_entries,
        'role_entries,
        S: RetirementMetadataSource + ?Sized,
    >(
        source: &'plan S,
        state: RetirementTreeState,
        blob: &'plan mut RetirementBlobToken<'arena, 'slots>,
        path: &'plan mut [RetirementPathFrame],
        replacements: &'plan mut CommittedReplacementLedger<'entries>,
        releases: &'plan mut PrivateReleaseBuffer<'release_entries>,
        roles: &'plan mut PageRoleIndex<'role_entries>,
    ) -> Result<
        RetirementEditPlan<'plan, 'arena, 'slots, 'entries, 'release_entries, 'role_entries, S>,
        RetirementWriteError,
    > {
        let batch = RetirementBatch {
            retired_by_txn: blob.born_txn,
            page_count: blob.page_count,
            page_list_blob_root: blob.root,
        };
        let arena = &*blob.arena;
        let guarded = GuardedRetirementSource::new(
            source,
            arena,
            None,
            Some(path),
            replacements,
            releases,
            roles,
            Some(blob),
        );
        guarded.resolve(
            guarded
                .check_retirement_access()
                .map_err(RetirementWriteError::Source),
        )?;
        let scratch_epoch = next_scratch_epoch(path)?;
        validate_edit_inputs(state, arena, replacements)?;
        if blob.stabilized || blob.born_txn != arena.born_txn {
            return Err(RetirementWriteError::BlobTokenTransactionMismatch {
                expected: arena.born_txn,
                actual: blob.born_txn,
            });
        }
        roles.prepare(arena, replacements)?;
        roles.require_new_replacements();
        let pool_snapshot = arena.capture_fence()?;
        let arena_generation = arena.generation.get();
        arena_generation
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        let mut staging = RetirementEditStaging::new(replacements, releases, &guarded);
        let append = guarded.resolve(preflight_upsert(
            &guarded,
            state,
            batch,
            arena,
            &pool_snapshot,
            None,
            path,
            &mut staging,
            roles,
        ))?;
        if let UpsertMode::Replace(old) = append.mode {
            guarded.resolve(scan_batch_blob(
                &guarded,
                state,
                arena,
                &pool_snapshot,
                old,
                None,
                ListedPagePolicy::MarkRequired,
                true,
                &mut staging,
                roles,
            ))?;
        }
        guarded.resolve(scan_batch_blob(
            &guarded,
            state,
            arena,
            &pool_snapshot,
            batch,
            Some(blob.generation),
            ListedPagePolicy::SatisfyRequired,
            false,
            &mut staging,
            roles,
        ))?;
        if let Some(pgno) = roles.first_unsatisfied_required() {
            return Err(RetirementWriteError::RetirementListOmission(pgno));
        }
        let mut destinations = DestinationCursor::new();
        let (mut result, upsert_scratch) = prepare_upsert_pages(
            state,
            batch,
            arena,
            &pool_snapshot,
            path,
            append,
            &mut destinations,
            scratch_epoch,
        )?;
        let (staged_replacements, staged_releases, prior_release_base, staged_prior_releases) =
            staging.finish();
        result.committed_replacements = staged_replacements;
        result.prior_private_replacements = staged_prior_releases;
        let replacement_binding = ledger_binding(replacements);
        let release_binding = release_binding(releases);
        let role_binding = role_binding(roles);
        let blob_binding = Some(blob_binding(blob));
        guarded.resolve(
            guarded
                .check_retirement_access()
                .map_err(RetirementWriteError::Source),
        )?;
        validate_callback_bindings(
            arena,
            &pool_snapshot,
            replacements,
            replacement_binding,
            releases,
            release_binding,
            roles,
            role_binding,
            Some(blob),
            blob_binding,
            None,
            ScratchBinding::empty(),
            Some(path),
            upsert_scratch,
        )?;
        Ok(RetirementEditPlan::Upsert(RetirementEditPlanInner {
            source,
            state,
            arena: RetirementPlanArena::Token(blob),
            delete_path: None,
            upsert_path: Some(path),
            replacements,
            releases,
            roles,
            pool_snapshot,
            arena_generation,
            replacement_binding,
            release_binding,
            role_binding,
            blob_binding,
            delete_scratch: ScratchBinding::empty(),
            upsert_scratch,
            staged_replacements,
            staged_releases,
            prior_release_base,
            staged_prior_releases,
            result,
            #[cfg(test)]
            destination_probes: destinations.probes,
        }))
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn plan_delete_oldest_prefix<
        'plan,
        'slots,
        'entries,
        'release_entries,
        'role_entries,
        S: RetirementMetadataSource + ?Sized,
    >(
        source: &'plan S,
        state: RetirementTreeState,
        delete_count: u64,
        arena: &'plan mut PrivatePageArena<'slots>,
        path: &'plan mut [RetirementPathFrame],
        replacements: &'plan mut CommittedReplacementLedger<'entries>,
        releases: &'plan mut PrivateReleaseBuffer<'release_entries>,
        roles: &'plan mut PageRoleIndex<'role_entries>,
    ) -> Result<
        RetirementEditPlan<'plan, 'plan, 'slots, 'entries, 'release_entries, 'role_entries, S>,
        RetirementWriteError,
    > {
        let guarded = GuardedRetirementSource::new(
            source,
            arena,
            Some(path),
            None,
            replacements,
            releases,
            roles,
            None,
        );
        guarded.resolve(
            guarded
                .check_retirement_access()
                .map_err(RetirementWriteError::Source),
        )?;
        validate_edit_inputs(state, arena, replacements)?;
        if delete_count > state.batch_count {
            return Err(RetirementWriteError::DeleteCountOutOfRange {
                requested: delete_count,
                available: state.batch_count,
            });
        }
        roles.prepare(arena, replacements)?;
        let pool_snapshot = arena.capture_fence()?;
        let arena_generation = arena.generation.get();
        arena_generation
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        let scratch_epoch = next_scratch_epoch(path)?;
        let mut staging = RetirementEditStaging::new(replacements, releases, &guarded);
        let mut destinations = DestinationCursor::new();
        let (mut result, delete_scratch) = if delete_count == 0 {
            (
                RetirementTreeEditResult {
                    root: state.root,
                    batch_count: state.batch_count,
                    private_pages: 0,
                    committed_replacements: 0,
                    prior_private_replacements: 0,
                },
                bind_scratch(path, 0, scratch_epoch),
            )
        } else {
            let mut scanner = DeleteScanner {
                source: &guarded,
                state,
                arena: &*arena,
                pool_snapshot: &pool_snapshot,
                path,
                staging: &mut staging,
                roles,
                remaining: delete_count,
                deleted: 0,
                previous_retired_txn: None,
                last_deleted_txn: None,
            };
            let outcome = guarded.resolve(scanner.scan_node(state.root, None, None, 0))?;
            if scanner.remaining != 0 || scanner.deleted != delete_count {
                return Err(RetirementWriteError::BatchCountMismatch {
                    declared: delete_count,
                    actual: scanner.deleted,
                });
            }
            let delete = scanner.finish_plan(outcome, delete_count)?;
            let (mut result, binding) = prepare_delete_pages(
                state,
                scanner.arena,
                &pool_snapshot,
                scanner.path,
                delete,
                &mut destinations,
                scratch_epoch,
            )?;
            result.batch_count = state.batch_count - delete_count;
            (result, binding)
        };
        let (staged_replacements, staged_releases, prior_release_base, staged_prior_releases) =
            staging.finish();
        result.committed_replacements = staged_replacements;
        result.prior_private_replacements = staged_prior_releases;
        let replacement_binding = ledger_binding(replacements);
        let release_binding = release_binding(releases);
        let role_binding = role_binding(roles);
        guarded.resolve(
            guarded
                .check_retirement_access()
                .map_err(RetirementWriteError::Source),
        )?;
        validate_callback_bindings(
            arena,
            &pool_snapshot,
            replacements,
            replacement_binding,
            releases,
            release_binding,
            roles,
            role_binding,
            None,
            None,
            Some(path),
            delete_scratch,
            None,
            ScratchBinding::empty(),
        )?;
        Ok(RetirementEditPlan::Delete(RetirementEditPlanInner {
            source,
            state,
            arena: RetirementPlanArena::Direct(arena),
            delete_path: Some(path),
            upsert_path: None,
            replacements,
            releases,
            roles,
            pool_snapshot,
            arena_generation,
            replacement_binding,
            release_binding,
            role_binding,
            blob_binding: None,
            delete_scratch,
            upsert_scratch: ScratchBinding::empty(),
            staged_replacements,
            staged_releases,
            prior_release_base,
            staged_prior_releases,
            result,
            #[cfg(test)]
            destination_probes: destinations.probes,
        }))
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn plan_delete_oldest_and_upsert_newest<
        'plan,
        'arena,
        'slots,
        'entries,
        'release_entries,
        'role_entries,
        S: RetirementMetadataSource + ?Sized,
    >(
        source: &'plan S,
        state: RetirementTreeState,
        delete_count: u64,
        blob: &'plan mut RetirementBlobToken<'arena, 'slots>,
        delete_path: &'plan mut [RetirementPathFrame],
        upsert_path: &'plan mut [RetirementPathFrame],
        replacements: &'plan mut CommittedReplacementLedger<'entries>,
        releases: &'plan mut PrivateReleaseBuffer<'release_entries>,
        roles: &'plan mut PageRoleIndex<'role_entries>,
    ) -> Result<
        RetirementEditPlan<'plan, 'arena, 'slots, 'entries, 'release_entries, 'role_entries, S>,
        RetirementWriteError,
    > {
        Self::plan_delete_oldest_and_upsert_newest_with_reclamation(
            source,
            state,
            delete_count,
            blob,
            delete_path,
            upsert_path,
            replacements,
            releases,
            roles,
            None,
        )
    }

    #[allow(clippy::too_many_arguments)]
    fn plan_delete_oldest_and_upsert_newest_with_reclamation<
        'plan,
        'arena,
        'slots,
        'entries,
        'release_entries,
        'role_entries,
        S: RetirementMetadataSource + ?Sized,
    >(
        source: &'plan S,
        state: RetirementTreeState,
        delete_count: u64,
        blob: &'plan mut RetirementBlobToken<'arena, 'slots>,
        delete_path: &'plan mut [RetirementPathFrame],
        upsert_path: &'plan mut [RetirementPathFrame],
        replacements: &'plan mut CommittedReplacementLedger<'entries>,
        releases: &'plan mut PrivateReleaseBuffer<'release_entries>,
        roles: &'plan mut PageRoleIndex<'role_entries>,
        reclamation: Option<(&RetirementReclamationAuthority<'_>, RetirementIdentity)>,
    ) -> Result<
        RetirementEditPlan<'plan, 'arena, 'slots, 'entries, 'release_entries, 'role_entries, S>,
        RetirementWriteError,
    > {
        let batch = RetirementBatch {
            retired_by_txn: blob.born_txn,
            page_count: blob.page_count,
            page_list_blob_root: blob.root,
        };
        let arena = &*blob.arena;
        let guarded = GuardedRetirementSource::new(
            source,
            arena,
            Some(delete_path),
            Some(upsert_path),
            replacements,
            releases,
            roles,
            Some(blob),
        );
        guarded.resolve(
            guarded
                .check_retirement_access()
                .map_err(RetirementWriteError::Source),
        )?;
        let delete_epoch = next_scratch_epoch(delete_path)?;
        let upsert_epoch = next_scratch_epoch(upsert_path)?;
        validate_edit_inputs(state, arena, replacements)?;
        if blob.stabilized || blob.born_txn != arena.born_txn {
            return Err(RetirementWriteError::BlobTokenTransactionMismatch {
                expected: arena.born_txn,
                actual: blob.born_txn,
            });
        }
        if delete_count > state.batch_count {
            return Err(RetirementWriteError::DeleteCountOutOfRange {
                requested: delete_count,
                available: state.batch_count,
            });
        }
        if let Some((reclamation, selected_identity)) = reclamation {
            validate_reclamation_authority(state, selected_identity, delete_count, reclamation)?;
        }
        roles.prepare(arena, replacements)?;
        if let Some((reclamation, _)) = reclamation {
            roles.authorize_reclaimed_pages(arena, reclamation)?;
        }
        roles.require_new_replacements();
        let pool_snapshot = arena.capture_fence()?;
        let arena_generation = arena.generation.get();
        let planned_generation = arena_generation
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        let mut staging = RetirementEditStaging::new(replacements, releases, &guarded);
        let mut destinations = DestinationCursor::new();
        let (intermediate, delete_scratch) = if delete_count == 0 {
            (
                RetirementTreeEditResult {
                    root: state.root,
                    batch_count: state.batch_count,
                    private_pages: 0,
                    committed_replacements: 0,
                    prior_private_replacements: 0,
                },
                bind_scratch(delete_path, 0, delete_epoch),
            )
        } else {
            let mut scanner = DeleteScanner {
                source: &guarded,
                state,
                arena,
                pool_snapshot: &pool_snapshot,
                path: delete_path,
                staging: &mut staging,
                roles,
                remaining: delete_count,
                deleted: 0,
                previous_retired_txn: None,
                last_deleted_txn: None,
            };
            let outcome = guarded.resolve(scanner.scan_node(state.root, None, None, 0))?;
            if scanner.remaining != 0 || scanner.deleted != delete_count {
                return Err(RetirementWriteError::BatchCountMismatch {
                    declared: delete_count,
                    actual: scanner.deleted,
                });
            }
            if let Some((reclamation, _)) = reclamation {
                scanner.validate_reclaimed_prefix(reclamation)?;
            }
            let delete = scanner.finish_plan(outcome, delete_count)?;
            let (mut result, binding) = prepare_delete_pages(
                state,
                arena,
                &pool_snapshot,
                delete_path,
                delete,
                &mut destinations,
                delete_epoch,
            )?;
            result.batch_count = state.batch_count - delete_count;
            (result, binding)
        };
        let intermediate_state = RetirementTreeState {
            root: intermediate.root,
            batch_count: intermediate.batch_count,
            ..state
        };
        if delete_count != 0 {
            roles.advance_reference_epoch()?;
        }
        let overlay = VirtualTreeOverlay {
            frames: &delete_path[..delete_scratch.len],
            generation: planned_generation,
        };
        let append = guarded.resolve(preflight_upsert(
            &guarded,
            intermediate_state,
            batch,
            arena,
            &pool_snapshot,
            Some(&overlay),
            upsert_path,
            &mut staging,
            roles,
        ))?;
        if let UpsertMode::Replace(old) = append.mode {
            guarded.resolve(scan_batch_blob(
                &guarded,
                intermediate_state,
                arena,
                &pool_snapshot,
                old,
                None,
                ListedPagePolicy::MarkRequired,
                true,
                &mut staging,
                roles,
            ))?;
        }
        guarded.resolve(scan_batch_blob(
            &guarded,
            intermediate_state,
            arena,
            &pool_snapshot,
            batch,
            Some(blob.generation),
            ListedPagePolicy::SatisfyRequired,
            false,
            &mut staging,
            roles,
        ))?;
        if let Some(pgno) = roles.first_unsatisfied_required() {
            return Err(RetirementWriteError::RetirementListOmission(pgno));
        }
        let (mut result, upsert_scratch) = prepare_upsert_pages(
            intermediate_state,
            batch,
            arena,
            &pool_snapshot,
            upsert_path,
            append,
            &mut destinations,
            upsert_epoch,
        )?;
        let reused = delete_path[..delete_scratch.len]
            .iter()
            .filter(|frame| {
                upsert_path[..upsert_scratch.len]
                    .iter()
                    .any(|upsert| upsert.pgno == frame.pgno)
            })
            .count();
        result.private_pages = intermediate
            .private_pages
            .checked_add(result.private_pages)
            .and_then(|pages| pages.checked_sub(reused))
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        let (staged_replacements, staged_releases, prior_release_base, staged_prior_releases) =
            staging.finish();
        result.committed_replacements = staged_replacements;
        result.prior_private_replacements = staged_prior_releases;
        let replacement_binding = ledger_binding(replacements);
        let release_binding = release_binding(releases);
        let role_binding = role_binding(roles);
        let blob_binding = Some(blob_binding(blob));
        guarded.resolve(
            guarded
                .check_retirement_access()
                .map_err(RetirementWriteError::Source),
        )?;
        validate_callback_bindings(
            arena,
            &pool_snapshot,
            replacements,
            replacement_binding,
            releases,
            release_binding,
            roles,
            role_binding,
            Some(blob),
            blob_binding,
            Some(delete_path),
            delete_scratch,
            Some(upsert_path),
            upsert_scratch,
        )?;
        Ok(RetirementEditPlan::Combined(RetirementEditPlanInner {
            source,
            state,
            arena: RetirementPlanArena::Token(blob),
            delete_path: Some(delete_path),
            upsert_path: Some(upsert_path),
            replacements,
            releases,
            roles,
            pool_snapshot,
            arena_generation,
            replacement_binding,
            release_binding,
            role_binding,
            blob_binding,
            delete_scratch,
            upsert_scratch,
            staged_replacements,
            staged_releases,
            prior_release_base,
            staged_prior_releases,
            result,
            #[cfg(test)]
            destination_probes: destinations.probes,
        }))
    }
}

/// Committed retirement metadata that a clean reclamation must carry into its
/// next protected batch before it can replace the selected prefix.
///
/// The caller owns the returned entries through the supplied replacement
/// ledger. `tree_private_page_budget` is a safe upper bound for the delete and
/// append tree pages; a later edit may reuse one of those pages and use less.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RetirementReclamationReplacementProbe {
    pub(crate) replacement_count: usize,
    pub(crate) tree_private_page_budget: usize,
}

/// Committed retirement-tree pages that an ordinary append will replace before
/// the new batch is built.
///
/// Unlike [`RetirementReclamationReplacementProbe`], this never deletes an
/// existing batch. The caller owns the returned entries through the supplied
/// replacement ledger. `tree_private_page_budget` is the exact number of
/// private tree pages required by this append path.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RetirementAppendReplacementProbe {
    pub(crate) replacement_count: usize,
    pub(crate) tree_private_page_budget: usize,
}

pub(crate) struct RetirementTreeEditor;

impl RetirementTreeEditor {
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn upsert_newest<S: RetirementMetadataSource + ?Sized>(
        source: &S,
        state: RetirementTreeState,
        mut blob: RetirementBlobToken<'_, '_>,
        path: &mut [RetirementPathFrame],
        replacements: &mut CommittedReplacementLedger<'_>,
        releases: &mut PrivateReleaseBuffer<'_>,
        roles: &mut PageRoleIndex<'_>,
    ) -> Result<RetirementTreeEditResult, RetirementWriteError> {
        Self::plan_upsert_newest(
            source,
            state,
            &mut blob,
            path,
            replacements,
            releases,
            roles,
        )?
        .apply()
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn delete_oldest_prefix<S: RetirementMetadataSource + ?Sized>(
        source: &S,
        state: RetirementTreeState,
        delete_count: u64,
        arena: &mut PrivatePageArena<'_>,
        path: &mut [RetirementPathFrame],
        replacements: &mut CommittedReplacementLedger<'_>,
        releases: &mut PrivateReleaseBuffer<'_>,
        roles: &mut PageRoleIndex<'_>,
    ) -> Result<RetirementTreeEditResult, RetirementWriteError> {
        Self::plan_delete_oldest_prefix(
            source,
            state,
            delete_count,
            arena,
            path,
            replacements,
            releases,
            roles,
        )?
        .apply()
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn delete_oldest_and_upsert_newest<S: RetirementMetadataSource + ?Sized>(
        source: &S,
        state: RetirementTreeState,
        delete_count: u64,
        mut blob: RetirementBlobToken<'_, '_>,
        delete_path: &mut [RetirementPathFrame],
        upsert_path: &mut [RetirementPathFrame],
        replacements: &mut CommittedReplacementLedger<'_>,
        releases: &mut PrivateReleaseBuffer<'_>,
        roles: &mut PageRoleIndex<'_>,
    ) -> Result<RetirementTreeEditResult, RetirementWriteError> {
        Self::plan_delete_oldest_and_upsert_newest(
            source,
            state,
            delete_count,
            &mut blob,
            delete_path,
            upsert_path,
            replacements,
            releases,
            roles,
        )?
        .apply()
    }

    /// Reuses only pages proved safe by the exact selected source and prefix.
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn delete_reclaimed_oldest_and_upsert_newest<
        S: RetirementMetadataSource + ?Sized,
    >(
        source: &S,
        state: RetirementTreeState,
        selected_identity: RetirementIdentity,
        reclamation: &RetirementReclamationAuthority<'_>,
        mut blob: RetirementBlobToken<'_, '_>,
        delete_path: &mut [RetirementPathFrame],
        upsert_path: &mut [RetirementPathFrame],
        replacements: &mut CommittedReplacementLedger<'_>,
        releases: &mut PrivateReleaseBuffer<'_>,
        roles: &mut PageRoleIndex<'_>,
    ) -> Result<RetirementTreeEditResult, RetirementWriteError> {
        Self::plan_delete_oldest_and_upsert_newest_with_reclamation(
            source,
            state,
            reclamation.batch_count(),
            &mut blob,
            delete_path,
            upsert_path,
            replacements,
            releases,
            roles,
            Some((reclamation, selected_identity)),
        )?
        .apply()
    }

    /// Validates the exact selected append path and discovers the committed
    /// retirement-tree pages it will replace, without building a blob or
    /// mutating the supplied arena.
    ///
    /// The placeholder batch contains only the successor transaction key;
    /// append geometry depends on that key and the selected tree, not on the
    /// later protected-page list or blob root. The caller must use fresh
    /// scratch for the real append edit.
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn probe_append_newest<S: RetirementMetadataSource + ?Sized>(
        source: &S,
        state: RetirementTreeState,
        arena: &mut PrivatePageArena<'_>,
        upsert_path: &mut [RetirementPathFrame],
        replacements: &mut CommittedReplacementLedger<'_>,
        releases: &mut PrivateReleaseBuffer<'_>,
        roles: &mut PageRoleIndex<'_>,
    ) -> Result<RetirementAppendReplacementProbe, RetirementWriteError> {
        let guarded = GuardedRetirementSource::new(
            source,
            arena,
            None,
            Some(upsert_path),
            replacements,
            releases,
            roles,
            None,
        );
        guarded.resolve(
            guarded
                .check_retirement_access()
                .map_err(RetirementWriteError::Source),
        )?;
        let _upsert_epoch = next_scratch_epoch(upsert_path)?;
        validate_edit_inputs(state, arena, replacements)?;
        roles.prepare(arena, replacements)?;
        roles.require_new_replacements();
        let pool_snapshot = arena.capture_fence()?;
        arena
            .generation
            .get()
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        let replacement_base = replacements.len;
        let mut staging = RetirementEditStaging::new(replacements, releases, &guarded);
        let append = guarded.resolve(preflight_upsert(
            &guarded,
            state,
            RetirementBatch {
                retired_by_txn: arena.born_txn,
                page_count: 1,
                page_list_blob_root: 2,
            },
            arena,
            &pool_snapshot,
            None,
            upsert_path,
            &mut staging,
            roles,
        ))?;
        let (staged_replacements, _staged_releases, _prior_release_base, _staged_prior_releases) =
            staging.finish();
        guarded.resolve(
            guarded
                .check_retirement_access()
                .map_err(RetirementWriteError::Source),
        )?;
        arena.validate_fence(&pool_snapshot)?;
        replacements.len = replacement_base
            .checked_add(staged_replacements)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        Ok(RetirementAppendReplacementProbe {
            replacement_count: staged_replacements,
            tree_private_page_budget: append.pages,
        })
    }

    /// Discover the exact committed retirement tree/blob pages that a clean
    /// reclamation will replace, without building the new page-list blob or
    /// mutating the supplied arena.
    ///
    /// The caller supplies dedicated probe scratch. On success the appended
    /// ledger entries must be combined with the allocator replacements, sorted
    /// into the next batch's page-list blob, and then supplied to the real
    /// `delete_reclaimed_oldest_and_upsert_newest` edit through fresh scratch.
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn probe_reclaimed_oldest_and_append_newest<S: RetirementMetadataSource + ?Sized>(
        source: &S,
        state: RetirementTreeState,
        selected_identity: RetirementIdentity,
        reclamation: &RetirementReclamationAuthority<'_>,
        arena: &mut PrivatePageArena<'_>,
        delete_path: &mut [RetirementPathFrame],
        upsert_path: &mut [RetirementPathFrame],
        replacements: &mut CommittedReplacementLedger<'_>,
        releases: &mut PrivateReleaseBuffer<'_>,
        roles: &mut PageRoleIndex<'_>,
    ) -> Result<RetirementReclamationReplacementProbe, RetirementWriteError> {
        let guarded = GuardedRetirementSource::new(
            source,
            arena,
            Some(delete_path),
            Some(upsert_path),
            replacements,
            releases,
            roles,
            None,
        );
        guarded.resolve(
            guarded
                .check_retirement_access()
                .map_err(RetirementWriteError::Source),
        )?;
        let delete_epoch = next_scratch_epoch(delete_path)?;
        validate_edit_inputs(state, arena, replacements)?;
        let delete_count = reclamation.batch_count();
        validate_reclamation_authority(state, selected_identity, delete_count, reclamation)?;
        roles.prepare(arena, replacements)?;
        roles.authorize_reclaimed_pages_when_bound(arena, reclamation)?;
        roles.require_new_replacements();
        let pool_snapshot = arena.capture_fence()?;
        let planned_generation = arena
            .generation
            .get()
            .checked_add(1)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        let replacement_base = replacements.len;
        let mut staging = RetirementEditStaging::new(replacements, releases, &guarded);
        let mut destinations = DestinationCursor::new();
        let mut scanner = DeleteScanner {
            source: &guarded,
            state,
            arena: &*arena,
            pool_snapshot: &pool_snapshot,
            path: delete_path,
            staging: &mut staging,
            roles,
            remaining: delete_count,
            deleted: 0,
            previous_retired_txn: None,
            last_deleted_txn: None,
        };
        let outcome = guarded.resolve(scanner.scan_node(state.root, None, None, 0))?;
        if scanner.remaining != 0 || scanner.deleted != delete_count {
            return Err(RetirementWriteError::BatchCountMismatch {
                declared: delete_count,
                actual: scanner.deleted,
            });
        }
        scanner.validate_reclaimed_prefix(reclamation)?;
        let delete = scanner.finish_plan(outcome, delete_count)?;
        let (mut intermediate, delete_scratch) = prepare_delete_pages(
            state,
            arena,
            &pool_snapshot,
            delete_path,
            delete,
            &mut destinations,
            delete_epoch,
        )?;
        intermediate.batch_count = state
            .batch_count
            .checked_sub(delete_count)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        roles.advance_reference_epoch()?;
        let intermediate_state = RetirementTreeState {
            root: intermediate.root,
            batch_count: intermediate.batch_count,
            ..state
        };
        let overlay = VirtualTreeOverlay {
            frames: &delete_path[..delete_scratch.len],
            generation: planned_generation,
        };
        // Structural append planning reads only the transaction key. The real
        // blob is built after this probe has produced the required page list.
        let append = guarded.resolve(preflight_upsert(
            &guarded,
            intermediate_state,
            RetirementBatch {
                retired_by_txn: arena.born_txn,
                page_count: 1,
                page_list_blob_root: 2,
            },
            arena,
            &pool_snapshot,
            Some(&overlay),
            upsert_path,
            &mut staging,
            roles,
        ))?;
        let (staged_replacements, _staged_releases, _prior_release_base, _staged_prior_releases) =
            staging.finish();
        guarded.resolve(
            guarded
                .check_retirement_access()
                .map_err(RetirementWriteError::Source),
        )?;
        arena.validate_fence(&pool_snapshot)?;
        replacements.len = replacement_base
            .checked_add(staged_replacements)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        let tree_private_page_budget = intermediate
            .private_pages
            .checked_add(append.pages)
            .ok_or(RetirementWriteError::ArithmeticOverflow)?;
        Ok(RetirementReclamationReplacementProbe {
            replacement_count: staged_replacements,
            tree_private_page_budget,
        })
    }
}

#[allow(clippy::too_many_arguments)]
fn preflight_upsert<S: RetirementMetadataSource + ?Sized>(
    source: &S,
    state: RetirementTreeState,
    batch: RetirementBatch,
    arena: &PrivatePageArena<'_>,
    pool_snapshot: &RetirementPoolFence<'_>,
    overlay: Option<&VirtualTreeOverlay<'_>>,
    path: &mut [RetirementPathFrame],
    staging: &mut RetirementEditStaging<'_, '_, '_, '_, '_>,
    roles: &mut PageRoleIndex<'_>,
) -> Result<AppendPlan, RetirementWriteError> {
    source.check_retirement_access()?;
    if state.root == 0 {
        return Ok(AppendPlan {
            path_len: 0,
            pages: 1,
            old_root: ChildReference {
                maximum: 0,
                pgno: 0,
                level: 0,
            },
            mode: UpsertMode::Append,
        });
    }

    let mut pgno = state.root;
    let mut depth = 0usize;
    let mut expected_level = None;
    let mut expected_maximum = None;
    let mut lower_bound = None;
    loop {
        if depth >= path.len() || depth >= RETIREMENT_PATH_CAPACITY {
            return Err(RetirementWriteError::PathBufferTooSmall {
                required: depth + 1,
                actual: path.len(),
            });
        }
        read_tree_frame(
            source,
            state,
            arena,
            pool_snapshot,
            overlay,
            pgno,
            &mut path[depth],
            roles,
        )?;
        let frame = &path[depth];
        if let Some(level) = expected_level {
            if frame.level != level {
                return Err(RetirementWriteError::ChildLevel {
                    expected: level,
                    actual: frame.level,
                });
            }
        }
        match PageHeader::decode(&frame.page, frame.decode_txn)?.page_type {
            PageType::RetirementLeaf => {
                let leaf =
                    RetirementLeaf::open(&frame.page, frame.decode_txn, arena.pending_page_count)?;
                leaf.verify_crc()?;
                validate_retirement_leaf_blob_roots(
                    source,
                    frame,
                    leaf,
                    state,
                    arena,
                    pool_snapshot,
                    roles,
                )?;
                let maximum = leaf.maximum_key()?;
                require_maximum(expected_maximum, maximum)?;
                if let Some(lower) = lower_bound {
                    let first = leaf.batch(0)?.retired_by_txn;
                    if first <= lower {
                        return Err(RetirementWriteError::RetirementTreeOrder {
                            previous: lower,
                            current: first,
                        });
                    }
                }
                depth += 1;
                break;
            }
            PageType::RetirementBranch => {
                let branch = RetirementBranch::open(
                    &frame.page,
                    frame.decode_txn,
                    arena.pending_page_count,
                )?;
                branch.verify_crc()?;
                validate_retirement_branch_children(
                    source,
                    frame,
                    branch,
                    state,
                    arena,
                    pool_snapshot,
                    overlay,
                    roles,
                )?;
                let maximum = branch.maximum_key()?;
                require_maximum(expected_maximum, maximum)?;
                let last = branch.len() - 1;
                if last > 0 {
                    let sibling_maximum = branch.entry(last - 1)?.max_retired_by_txn;
                    lower_bound = Some(
                        lower_bound
                            .map(|lower| lower.max(sibling_maximum))
                            .unwrap_or(sibling_maximum),
                    );
                }
                let entry = branch.entry(last)?;
                pgno = entry.child_pgno;
                expected_level = Some(branch.level() - 1);
                expected_maximum = Some(entry.max_retired_by_txn);
                depth += 1;
            }
            other => {
                return Err(if depth == 0 {
                    RetirementWriteError::RootType(other)
                } else {
                    RetirementWriteError::ChildType(other)
                });
            }
        }
    }

    let leaf_frame = &path[depth - 1];
    let leaf = RetirementLeaf::open(
        &leaf_frame.page,
        leaf_frame.decode_txn,
        arena.pending_page_count,
    )?;
    let maximum = leaf.maximum_key()?;
    if maximum > batch.retired_by_txn {
        return Err(RetirementWriteError::TransactionOrder {
            selected: maximum,
            pending: batch.retired_by_txn,
        });
    }
    let mode = if maximum == batch.retired_by_txn {
        UpsertMode::Replace(leaf.batch(leaf.len() - 1)?)
    } else {
        UpsertMode::Append
    };

    let old_root = ChildReference {
        maximum: match PageHeader::decode(&path[0].page, path[0].decode_txn)?.page_type {
            PageType::RetirementLeaf => {
                RetirementLeaf::open(&path[0].page, path[0].decode_txn, arena.pending_page_count)?
                    .maximum_key()?
            }
            PageType::RetirementBranch => {
                RetirementBranch::open(&path[0].page, path[0].decode_txn, arena.pending_page_count)?
                    .maximum_key()?
            }
            other => return Err(RetirementWriteError::RootType(other)),
        },
        pgno: state.root,
        level: path[0].level,
    };

    let mut pages = 1usize;
    match mode {
        UpsertMode::Replace(_) => {
            for frame in &path[..depth] {
                retire_tree_frame(frame, staging, roles)?;
            }
            pages = depth;
        }
        UpsertMode::Append => {
            let mut carry = leaf.len() == RETIREMENT_LEAF_CAPACITY;
            if !carry {
                retire_tree_frame(leaf_frame, staging, roles)?;
            }
            for frame in path[..depth - 1].iter().rev() {
                let branch = RetirementBranch::open(
                    &frame.page,
                    frame.decode_txn,
                    arena.pending_page_count,
                )?;
                pages = pages
                    .checked_add(1)
                    .ok_or(RetirementWriteError::ArithmeticOverflow)?;
                if carry && branch.len() == RETIREMENT_BRANCH_CAPACITY {
                    carry = true;
                } else {
                    carry = false;
                    retire_tree_frame(frame, staging, roles)?;
                }
            }
            if carry {
                if old_root.level == MAX_TREE_LEVEL {
                    return Err(RetirementWriteError::TreeDepthExceeded);
                }
                pages = pages
                    .checked_add(1)
                    .ok_or(RetirementWriteError::ArithmeticOverflow)?;
            }
        }
    }
    Ok(AppendPlan {
        path_len: depth,
        pages,
        old_root,
        mode,
    })
}

#[derive(Clone, Copy, Debug)]
enum DeleteNodeOutcome {
    FullyRemoved { maximum: u64 },
    Boundary(DeleteBoundary),
}

#[derive(Clone, Copy, Debug)]
struct DeleteBoundary {
    base: ChildReference,
    deepest_depth: usize,
    partial_leaf: bool,
}

#[derive(Clone, Copy, Debug)]
struct FinalDeleteBoundary {
    base: ChildReference,
    deepest_depth: usize,
    partial_leaf: bool,
    new_root_depth: Option<usize>,
}

#[derive(Clone, Copy, Debug)]
struct DeletePlan {
    boundary: Option<FinalDeleteBoundary>,
    pages: usize,
}

struct DeleteScanner<
    'a,
    'pages,
    'arena,
    'path,
    'staging,
    'ledger,
    'entries,
    'release,
    'release_entries,
    'prior,
    'roles,
    'role_entries,
    S: RetirementMetadataSource + ?Sized,
> {
    source: &'a S,
    state: RetirementTreeState,
    arena: &'arena PrivatePageArena<'pages>,
    pool_snapshot: &'arena RetirementPoolFence<'pages>,
    path: &'path mut [RetirementPathFrame],
    staging:
        &'staging mut RetirementEditStaging<'ledger, 'entries, 'release, 'release_entries, 'prior>,
    roles: &'roles mut PageRoleIndex<'role_entries>,
    remaining: u64,
    deleted: u64,
    previous_retired_txn: Option<u64>,
    last_deleted_txn: Option<u64>,
}

impl<S: RetirementMetadataSource + ?Sized>
    DeleteScanner<'_, '_, '_, '_, '_, '_, '_, '_, '_, '_, '_, '_, S>
{
    fn scan_node(
        &mut self,
        pgno: u32,
        expected_level: Option<u16>,
        expected_maximum: Option<u64>,
        depth: usize,
    ) -> Result<DeleteNodeOutcome, RetirementWriteError> {
        if depth >= RETIREMENT_PATH_CAPACITY || depth >= self.path.len() {
            return Err(RetirementWriteError::PathBufferTooSmall {
                required: depth + 1,
                actual: self.path.len(),
            });
        }
        let mut frame = RetirementPathFrame::new();
        read_tree_frame(
            self.source,
            self.state,
            self.arena,
            self.pool_snapshot,
            None,
            pgno,
            &mut frame,
            self.roles,
        )?;
        let header = PageHeader::decode(&frame.page, frame.decode_txn)?;
        if let Some(level) = expected_level {
            if header.level != level {
                return Err(RetirementWriteError::ChildLevel {
                    expected: level,
                    actual: header.level,
                });
            }
        }
        match header.page_type {
            PageType::RetirementLeaf => {
                let leaf = RetirementLeaf::open(
                    &frame.page,
                    frame.decode_txn,
                    self.arena.pending_page_count,
                )?;
                leaf.verify_crc()?;
                validate_retirement_leaf_blob_roots(
                    self.source,
                    &frame,
                    leaf,
                    self.state,
                    self.arena,
                    self.pool_snapshot,
                    self.roles,
                )?;
                let maximum = leaf.maximum_key()?;
                require_maximum(expected_maximum, maximum)?;
                retire_tree_frame(&frame, self.staging, self.roles)?;
                let delete_here = self.remaining.min(leaf.len() as u64);
                let inspect = (delete_here + u64::from(delete_here < leaf.len() as u64)) as usize;
                for index in 0..inspect {
                    let current = leaf.batch(index)?.retired_by_txn;
                    if self
                        .previous_retired_txn
                        .map(|previous| current <= previous)
                        .unwrap_or(false)
                    {
                        return Err(RetirementWriteError::RetirementTreeOrder {
                            previous: self.previous_retired_txn.unwrap(),
                            current,
                        });
                    }
                    self.previous_retired_txn = Some(current);
                    if index < delete_here as usize {
                        self.last_deleted_txn = Some(current);
                    }
                }
                for index in 0..delete_here as usize {
                    scan_batch_blob(
                        self.source,
                        self.state,
                        self.arena,
                        self.pool_snapshot,
                        leaf.batch(index)?,
                        None,
                        ListedPagePolicy::Register,
                        true,
                        self.staging,
                        self.roles,
                    )?;
                }
                self.remaining -= delete_here;
                self.deleted = self
                    .deleted
                    .checked_add(delete_here)
                    .ok_or(RetirementWriteError::ArithmeticOverflow)?;
                if delete_here == leaf.len() as u64 {
                    Ok(DeleteNodeOutcome::FullyRemoved { maximum })
                } else {
                    frame.keep_from = delete_here as u16;
                    self.path[depth] = frame;
                    Ok(DeleteNodeOutcome::Boundary(DeleteBoundary {
                        base: ChildReference {
                            maximum,
                            pgno: 0,
                            level: 0,
                        },
                        deepest_depth: depth,
                        partial_leaf: true,
                    }))
                }
            }
            PageType::RetirementBranch => {
                let branch = RetirementBranch::open(
                    &frame.page,
                    frame.decode_txn,
                    self.arena.pending_page_count,
                )?;
                branch.verify_crc()?;
                validate_retirement_branch_children(
                    self.source,
                    &frame,
                    branch,
                    self.state,
                    self.arena,
                    self.pool_snapshot,
                    None,
                    self.roles,
                )?;
                let maximum = branch.maximum_key()?;
                require_maximum(expected_maximum, maximum)?;
                retire_tree_frame(&frame, self.staging, self.roles)?;
                for index in 0..branch.len() {
                    let entry = branch.entry(index)?;
                    if self.remaining == 0 {
                        frame.keep_from = index as u16;
                        self.path[depth] = frame;
                        return Ok(DeleteNodeOutcome::Boundary(DeleteBoundary {
                            base: ChildReference {
                                maximum: entry.max_retired_by_txn,
                                pgno: entry.child_pgno,
                                level: branch.level() - 1,
                            },
                            deepest_depth: depth,
                            partial_leaf: false,
                        }));
                    }
                    match self.scan_node(
                        entry.child_pgno,
                        Some(branch.level() - 1),
                        Some(entry.max_retired_by_txn),
                        depth + 1,
                    )? {
                        DeleteNodeOutcome::FullyRemoved {
                            maximum: child_maximum,
                        } => {
                            require_maximum(Some(entry.max_retired_by_txn), child_maximum)?;
                        }
                        DeleteNodeOutcome::Boundary(boundary) => {
                            frame.keep_from = index as u16;
                            self.path[depth] = frame;
                            return Ok(DeleteNodeOutcome::Boundary(boundary));
                        }
                    }
                }
                Ok(DeleteNodeOutcome::FullyRemoved { maximum })
            }
            other => Err(if depth == 0 {
                RetirementWriteError::RootType(other)
            } else {
                RetirementWriteError::ChildType(other)
            }),
        }
    }

    fn validate_reclaimed_prefix(
        &self,
        reclamation: &RetirementReclamationAuthority<'_>,
    ) -> Result<(), RetirementWriteError> {
        let actual_last_retired_by_txn = self.last_deleted_txn.unwrap_or(0);
        if self.deleted != reclamation.batch_count()
            || actual_last_retired_by_txn != reclamation.last_retired_by_txn()
        {
            return Err(RetirementWriteError::ReclamationPrefixMismatch {
                expected_batches: reclamation.batch_count(),
                actual_batches: self.deleted,
                expected_last_retired_by_txn: reclamation.last_retired_by_txn(),
                actual_last_retired_by_txn,
            });
        }
        Ok(())
    }

    fn finish_plan(
        &mut self,
        outcome: DeleteNodeOutcome,
        delete_count: u64,
    ) -> Result<DeletePlan, RetirementWriteError> {
        match outcome {
            DeleteNodeOutcome::FullyRemoved { .. } => {
                if delete_count != self.state.batch_count {
                    return Err(RetirementWriteError::BatchCountMismatch {
                        declared: self.state.batch_count,
                        actual: delete_count,
                    });
                }
                Ok(DeletePlan {
                    boundary: None,
                    pages: 0,
                })
            }
            DeleteNodeOutcome::Boundary(mut boundary) => {
                if delete_count == self.state.batch_count {
                    return Err(RetirementWriteError::BatchCountMismatch {
                        declared: self.state.batch_count,
                        actual: delete_count + 1,
                    });
                }
                let mut new_root_depth = None;
                for depth in 0..=boundary.deepest_depth {
                    let frame = &self.path[depth];
                    if frame.level == 0 {
                        continue;
                    }
                    let branch = RetirementBranch::open(
                        &frame.page,
                        frame.decode_txn,
                        self.arena.pending_page_count,
                    )?;
                    let retained = branch.len() - usize::from(frame.keep_from);
                    if retained >= 2 {
                        new_root_depth = Some(depth);
                        break;
                    }
                }

                if new_root_depth.is_none() && !boundary.partial_leaf {
                    boundary.base = self.collapse_promoted_root(boundary.base)?;
                }
                let mut pages = usize::from(boundary.partial_leaf);
                if let Some(root_depth) = new_root_depth {
                    for depth in root_depth..=boundary.deepest_depth {
                        if self.path[depth].level > 0 {
                            pages = pages
                                .checked_add(1)
                                .ok_or(RetirementWriteError::ArithmeticOverflow)?;
                        }
                    }
                }
                Ok(DeletePlan {
                    boundary: Some(FinalDeleteBoundary {
                        base: boundary.base,
                        deepest_depth: boundary.deepest_depth,
                        partial_leaf: boundary.partial_leaf,
                        new_root_depth,
                    }),
                    pages,
                })
            }
        }
    }

    fn collapse_promoted_root(
        &mut self,
        mut child: ChildReference,
    ) -> Result<ChildReference, RetirementWriteError> {
        while child.level > 0 {
            let mut frame = RetirementPathFrame::new();
            read_tree_frame(
                self.source,
                self.state,
                self.arena,
                self.pool_snapshot,
                None,
                child.pgno,
                &mut frame,
                self.roles,
            )?;
            let branch = RetirementBranch::open(
                &frame.page,
                frame.decode_txn,
                self.arena.pending_page_count,
            )?;
            branch.verify_crc()?;
            validate_retirement_branch_children(
                self.source,
                &frame,
                branch,
                self.state,
                self.arena,
                self.pool_snapshot,
                None,
                self.roles,
            )?;
            if branch.level() != child.level {
                return Err(RetirementWriteError::ChildLevel {
                    expected: child.level,
                    actual: branch.level(),
                });
            }
            require_maximum(Some(child.maximum), branch.maximum_key()?)?;
            if branch.len() != 1 {
                break;
            }
            retire_tree_frame(&frame, self.staging, self.roles)?;
            let entry = branch.entry(0)?;
            child = ChildReference {
                maximum: entry.max_retired_by_txn,
                pgno: entry.child_pgno,
                level: branch.level() - 1,
            };
        }
        Ok(child)
    }
}

fn require_maximum(expected: Option<u64>, actual: u64) -> Result<(), RetirementWriteError> {
    if let Some(expected) = expected {
        if actual != expected {
            return Err(RetirementWriteError::ChildMaximumMismatch { expected, actual });
        }
    }
    Ok(())
}

fn validate_edit_inputs(
    state: RetirementTreeState,
    arena: &PrivatePageArena<'_>,
    replacements: &CommittedReplacementLedger<'_>,
) -> Result<(), RetirementWriteError> {
    if state.selected_txn == 0 {
        return Err(RetirementWriteError::SelectedTransactionOutOfRange(
            state.selected_txn,
        ));
    }
    if state.page_count != arena.committed_page_count {
        return Err(RetirementWriteError::PageCountOutOfRange {
            committed: state.page_count,
            pending: arena.pending_page_count,
        });
    }
    if (state.root == 0) != (state.batch_count == 0) {
        return Err(RetirementWriteError::RootCountMismatch);
    }
    let expected_pending = state.selected_txn.checked_add(1).ok_or(
        RetirementWriteError::SelectedTransactionOverflow(state.selected_txn),
    )?;
    if arena.born_txn != expected_pending {
        return Err(RetirementWriteError::TransactionOrder {
            selected: state.selected_txn,
            pending: arena.born_txn,
        });
    }
    if state.batch_count > arena.born_txn.saturating_sub(1) {
        return Err(RetirementWriteError::BatchCountOutOfRange(
            state.batch_count,
        ));
    }
    if state.root != 0 && (state.root < 2 || u64::from(state.root) >= arena.pending_page_count) {
        return Err(RetirementWriteError::RootOutOfBounds(state.root));
    }
    for replacement in replacements.entries() {
        if replacement.pgno < 2 || u64::from(replacement.pgno) >= state.page_count {
            return Err(RetirementWriteError::RootOutOfBounds(replacement.pgno));
        }
        if arena.contains(replacement.pgno)? {
            return Err(RetirementWriteError::CommittedReplacementIsPrivate(
                replacement.pgno,
            ));
        }
    }
    Ok(())
}

fn validate_reclamation_authority(
    state: RetirementTreeState,
    selected_identity: RetirementIdentity,
    delete_count: u64,
    reclamation: &RetirementReclamationAuthority<'_>,
) -> Result<(), RetirementWriteError> {
    if selected_identity.txn_id != state.selected_txn
        || selected_identity.page_count != state.page_count
        || selected_identity.root != state.root
        || selected_identity.batch_count != state.batch_count
        || reclamation.identity() != Some(selected_identity)
    {
        return Err(RetirementWriteError::ReclamationStateMismatch);
    }
    if delete_count == 0
        || delete_count != reclamation.batch_count()
        || reclamation.last_retired_by_txn() == 0
        || reclamation.pages().is_empty()
    {
        return Err(RetirementWriteError::ReclamationStateMismatch);
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn read_tree_frame<S: RetirementMetadataSource + ?Sized>(
    source: &S,
    state: RetirementTreeState,
    arena: &PrivatePageArena<'_>,
    pool_snapshot: &RetirementPoolFence<'_>,
    overlay: Option<&VirtualTreeOverlay<'_>>,
    pgno: u32,
    frame: &mut RetirementPathFrame,
    roles: &mut PageRoleIndex<'_>,
) -> Result<(), RetirementWriteError> {
    if let Some(planned) = overlay.and_then(|overlay| overlay.find(pgno)) {
        debug_assert_eq!(overlay.unwrap().generation, arena.generation.get() + 1);
        frame.pgno = pgno;
        frame.private = true;
        frame.prior_private = None;
        frame.decode_txn = arena.born_txn;
        frame.level = planned.level;
        frame.page.copy_from_slice(&planned.page);
        frame.keep_from = 0;
        frame.destination_slot = planned.destination_slot;
        frame.destination_binding_epoch = planned.destination_binding_epoch;
        frame.scratch_epoch = planned.scratch_epoch;
        roles.select(pgno, PageRole::SelectedRetirementTree, true)?;
        return Ok(());
    }
    let residence = read_metadata_page(
        source,
        state,
        arena,
        pool_snapshot,
        pgno,
        PrivatePageOrigin::RetirementTree,
        PageRole::SelectedRetirementTree,
        &mut frame.page,
        roles,
    )?;
    frame.pgno = pgno;
    frame.private = !matches!(residence, PageResidence::Committed);
    frame.prior_private = match residence {
        PageResidence::PriorPrivate { location, .. } => Some(location),
        _ => None,
    };
    frame.decode_txn = match residence {
        PageResidence::Committed => state.selected_txn,
        PageResidence::CurrentPrivate { .. } => arena.born_txn,
        PageResidence::PriorPrivate { generation, .. } => generation,
    };
    frame.level = PageHeader::decode(&frame.page, frame.decode_txn)?.level;
    frame.keep_from = 0;
    frame.destination_slot = usize::MAX;
    frame.destination_binding_epoch = 0;
    frame.scratch_epoch = 0;
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn require_parent_child<S: RetirementMetadataSource + ?Sized>(
    source: &S,
    parent: u32,
    parent_private: bool,
    child: u32,
    state: RetirementTreeState,
    arena: &PrivatePageArena<'_>,
    overlay: Option<&VirtualTreeOverlay<'_>>,
    expected_origin: PrivatePageOrigin,
    expected_role: PageRole,
) -> Result<bool, RetirementWriteError> {
    if expected_origin == PrivatePageOrigin::RetirementTree
        && overlay.and_then(|overlay| overlay.find(child)).is_some()
    {
        return Ok(true);
    }
    let expected_kind = match expected_origin {
        PrivatePageOrigin::RetirementTree => FixedPointRetirementPageKind::Tree,
        PrivatePageOrigin::RetirementBlob => FixedPointRetirementPageKind::Blob,
    };
    let prior = source.prior_private_location(child, expected_kind)?;
    if !parent_private {
        if u64::from(child) >= state.page_count || arena.contains(child)? || prior.is_some() {
            return Err(RetirementWriteError::CommittedParentPrivateChild { parent, child });
        }
        return Ok(false);
    }
    match arena.private_state(child)? {
        Some(RetirementPrivatePageState::InUse { origin, .. }) if origin == expected_origin => {
            Ok(true)
        }
        Some(RetirementPrivatePageState::InUse { .. }) => {
            Err(RetirementWriteError::PrivatePageOriginMismatch {
                pgno: child,
                expected: expected_role,
            })
        }
        None if prior.is_some() => Ok(false),
        None if arena.contains(child)? => Err(RetirementWriteError::PrivatePageUnavailable(child)),
        None if u64::from(child) < state.page_count => Ok(false),
        None => Err(RetirementWriteError::PrivatePageUnavailable(child)),
    }
}

#[allow(clippy::too_many_arguments)]
fn validate_retirement_branch_children<S: RetirementMetadataSource + ?Sized>(
    source: &S,
    frame: &RetirementPathFrame,
    branch: RetirementBranch<'_>,
    state: RetirementTreeState,
    arena: &PrivatePageArena<'_>,
    _pool_snapshot: &RetirementPoolFence<'_>,
    overlay: Option<&VirtualTreeOverlay<'_>>,
    roles: &mut PageRoleIndex<'_>,
) -> Result<(), RetirementWriteError> {
    for index in 0..branch.len() {
        let child = branch.entry(index)?.child_pgno;
        let private = require_parent_child(
            source,
            frame.pgno,
            frame.private,
            child,
            state,
            arena,
            overlay,
            PrivatePageOrigin::RetirementTree,
            PageRole::SelectedRetirementTree,
        )?;
        roles.reference(child, PageRole::ReferencedRetirementTree, private)?;
    }
    Ok(())
}

fn validate_retirement_leaf_blob_roots<S: RetirementMetadataSource + ?Sized>(
    source: &S,
    frame: &RetirementPathFrame,
    leaf: RetirementLeaf<'_>,
    state: RetirementTreeState,
    arena: &PrivatePageArena<'_>,
    _pool_snapshot: &RetirementPoolFence<'_>,
    roles: &mut PageRoleIndex<'_>,
) -> Result<(), RetirementWriteError> {
    for index in 0..leaf.len() {
        let child = leaf.batch(index)?.page_list_blob_root;
        let private = require_parent_child(
            source,
            frame.pgno,
            frame.private,
            child,
            state,
            arena,
            None,
            PrivatePageOrigin::RetirementBlob,
            PageRole::SelectedRetirementBlob,
        )?;
        roles.reference(child, PageRole::ReferencedRetirementBlob, private)?;
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn read_metadata_page<S: RetirementMetadataSource + ?Sized>(
    source: &S,
    state: RetirementTreeState,
    arena: &PrivatePageArena<'_>,
    pool_snapshot: &RetirementPoolFence<'_>,
    pgno: u32,
    expected_origin: PrivatePageOrigin,
    selected_role: PageRole,
    bytes: &mut [u8; PAGE_SIZE],
    roles: &mut PageRoleIndex<'_>,
) -> Result<PageResidence, RetirementWriteError> {
    if arena.contains(pgno)? {
        match arena.private_state(pgno)? {
            None => Err(RetirementWriteError::PrivatePageUnavailable(pgno)),
            Some(RetirementPrivatePageState::InUse { origin, generation })
                if origin == expected_origin =>
            {
                arena.read_page_snapshot(pool_snapshot, pgno, origin, generation, bytes)?;
                roles.select(pgno, selected_role, true)?;
                Ok(PageResidence::CurrentPrivate { generation })
            }
            Some(RetirementPrivatePageState::InUse { .. }) => {
                Err(RetirementWriteError::PrivatePageOriginMismatch {
                    pgno,
                    expected: selected_role,
                })
            }
        }
    } else {
        if pgno < 2 || u64::from(pgno) >= arena.pending_page_count {
            return Err(RetirementWriteError::RootOutOfBounds(pgno));
        }
        let expected_kind = match expected_origin {
            PrivatePageOrigin::RetirementTree => FixedPointRetirementPageKind::Tree,
            PrivatePageOrigin::RetirementBlob => FixedPointRetirementPageKind::Blob,
        };
        match source.read_non_current_retirement_page(pgno, expected_kind, bytes)? {
            FixedPointRetirementResidence::SelectedCommitted => {
                if u64::from(pgno) >= state.page_count {
                    return Err(RetirementWriteError::RootOutOfBounds(pgno));
                }
                roles.select(pgno, selected_role, false)?;
                Ok(PageResidence::Committed)
            }
            FixedPointRetirementResidence::PriorPrivate(location) => {
                let DraftPageProvenance::Private { page, .. } = location.provenance else {
                    return Err(RetirementWriteError::Source(PageSourceError::ForkedHandle));
                };
                roles.select(pgno, selected_role, false)?;
                Ok(PageResidence::PriorPrivate {
                    generation: page.owner_generation,
                    location,
                })
            }
        }
    }
}

fn retire_tree_frame(
    frame: &RetirementPathFrame,
    staging: &mut RetirementEditStaging<'_, '_, '_, '_, '_>,
    roles: &mut PageRoleIndex<'_>,
) -> Result<(), RetirementWriteError> {
    if frame.destination_slot != usize::MAX && frame.scratch_epoch != 0 {
        return Ok(());
    }
    if let Some(location) = frame.prior_private {
        staging.stage_prior_release(location)?;
        roles.retire_prior_private(frame.pgno, PrivatePageOrigin::RetirementTree)
    } else if frame.private {
        staging.stage_release(frame.pgno)?;
        roles.retire_private(frame.pgno, PrivatePageOrigin::RetirementTree)
    } else {
        let replacement = CommittedPageReplacement {
            pgno: frame.pgno,
            origin: CommittedPageOrigin::RetirementTree,
        };
        staging.stage_replacement(replacement)?;
        roles.retire_committed(replacement.pgno, replacement.origin)
    }
}

fn retire_blob_page(
    pgno: u32,
    residence: PageResidence,
    staging: &mut RetirementEditStaging<'_, '_, '_, '_, '_>,
    roles: &mut PageRoleIndex<'_>,
) -> Result<(), RetirementWriteError> {
    match residence {
        PageResidence::CurrentPrivate { .. } => {
            staging.stage_release(pgno)?;
            roles.retire_private(pgno, PrivatePageOrigin::RetirementBlob)
        }
        PageResidence::PriorPrivate { location, .. } => {
            staging.stage_prior_release(location)?;
            roles.retire_prior_private(pgno, PrivatePageOrigin::RetirementBlob)
        }
        PageResidence::Committed => {
            let replacement = CommittedPageReplacement {
                pgno,
                origin: CommittedPageOrigin::RetirementBlob,
            };
            staging.stage_replacement(replacement)?;
            roles.retire_committed(replacement.pgno, replacement.origin)
        }
    }
}

fn require_blob_child<S: RetirementMetadataSource + ?Sized>(
    source: &S,
    parent: u32,
    child: u32,
    expected_residence: BlobResidenceExpectation,
    state: RetirementTreeState,
    arena: &PrivatePageArena<'_>,
) -> Result<bool, RetirementWriteError> {
    match expected_residence {
        BlobResidenceExpectation::DeriveAtRoot => {
            unreachable!("blob residence is derived before children are inspected")
        }
        BlobResidenceExpectation::Committed => require_parent_child(
            source,
            parent,
            false,
            child,
            state,
            arena,
            None,
            PrivatePageOrigin::RetirementBlob,
            PageRole::SelectedRetirementBlob,
        ),
        BlobResidenceExpectation::Private(expected_generation) => {
            let prior = source.prior_private_location(child, FixedPointRetirementPageKind::Blob)?;
            match arena.private_state(child)? {
                Some(RetirementPrivatePageState::InUse {
                    origin: PrivatePageOrigin::RetirementBlob,
                    generation,
                }) if generation == expected_generation => Ok(true),
                Some(RetirementPrivatePageState::InUse {
                    origin: PrivatePageOrigin::RetirementBlob,
                    ..
                }) => Err(RetirementWriteError::BlobTokenGenerationMismatch(
                    expected_generation,
                )),
                Some(RetirementPrivatePageState::InUse { .. }) => {
                    Err(RetirementWriteError::PrivatePageOriginMismatch {
                        pgno: child,
                        expected: PageRole::SelectedRetirementBlob,
                    })
                }
                None if prior.is_some_and(|location| {
                    matches!(
                        location.provenance,
                        DraftPageProvenance::Private { page, .. }
                            if page.owner_generation == expected_generation
                    )
                }) =>
                {
                    Ok(false)
                }
                None if prior.is_some() => Err(RetirementWriteError::BlobTokenGenerationMismatch(
                    expected_generation,
                )),
                None if arena.contains(child)? => {
                    Err(RetirementWriteError::PrivatePageUnavailable(child))
                }
                None if u64::from(child) < state.page_count => {
                    Err(RetirementWriteError::PrivateBlobNonPrivateChild {
                        parent,
                        child,
                        expected_generation,
                    })
                }
                None => Err(RetirementWriteError::PrivatePageUnavailable(child)),
            }
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn scan_batch_blob<S: RetirementMetadataSource + ?Sized>(
    source: &S,
    state: RetirementTreeState,
    arena: &PrivatePageArena<'_>,
    pool_snapshot: &RetirementPoolFence<'_>,
    batch: RetirementBatch,
    expected_generation: Option<u64>,
    listed_policy: ListedPagePolicy,
    retire: bool,
    staging: &mut RetirementEditStaging<'_, '_, '_, '_, '_>,
    roles: &mut PageRoleIndex<'_>,
) -> Result<(), RetirementWriteError> {
    let length = batch.blob_length()?;
    let residence = expected_generation.map_or(
        BlobResidenceExpectation::DeriveAtRoot,
        BlobResidenceExpectation::Private,
    );
    let mut previous = None;
    let mut values = 0u64;
    scan_blob_node(
        source,
        state,
        arena,
        pool_snapshot,
        batch.page_list_blob_root,
        None,
        0,
        length,
        length,
        residence,
        listed_policy,
        retire,
        staging,
        roles,
        &mut previous,
        &mut values,
        0,
    )?;
    if values != batch.page_count {
        return Err(RetirementWriteError::BlobPageCountMismatch {
            declared: batch.page_count,
            actual: values,
        });
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn scan_blob_node<S: RetirementMetadataSource + ?Sized>(
    source: &S,
    state: RetirementTreeState,
    arena: &PrivatePageArena<'_>,
    pool_snapshot: &RetirementPoolFence<'_>,
    pgno: u32,
    expected_level: Option<u16>,
    expected_start: u64,
    expected_end: u64,
    owner_length: u64,
    expected_residence: BlobResidenceExpectation,
    listed_policy: ListedPagePolicy,
    retire: bool,
    staging: &mut RetirementEditStaging<'_, '_, '_, '_, '_>,
    roles: &mut PageRoleIndex<'_>,
    previous: &mut Option<u32>,
    values: &mut u64,
    depth: usize,
) -> Result<(), RetirementWriteError> {
    if depth > MAX_TREE_LEVEL as usize {
        return Err(RetirementWriteError::TreeDepthExceeded);
    }
    let mut bytes = [0u8; PAGE_SIZE];
    let residence = read_metadata_page(
        source,
        state,
        arena,
        pool_snapshot,
        pgno,
        PrivatePageOrigin::RetirementBlob,
        PageRole::SelectedRetirementBlob,
        &mut bytes,
        roles,
    )?;
    let expected_residence = match expected_residence {
        BlobResidenceExpectation::DeriveAtRoot => match residence {
            PageResidence::Committed => BlobResidenceExpectation::Committed,
            PageResidence::CurrentPrivate { generation }
            | PageResidence::PriorPrivate { generation, .. } => {
                BlobResidenceExpectation::Private(generation)
            }
        },
        BlobResidenceExpectation::Committed => {
            if !matches!(residence, PageResidence::Committed) {
                return Err(RetirementWriteError::BlobResidenceMismatch(pgno));
            }
            BlobResidenceExpectation::Committed
        }
        BlobResidenceExpectation::Private(generation) => {
            if !matches!(
                residence,
                PageResidence::CurrentPrivate { generation: actual }
                    | PageResidence::PriorPrivate {
                        generation: actual,
                        ..
                    } if actual == generation
            ) {
                return Err(RetirementWriteError::BlobTokenGenerationMismatch(
                    generation,
                ));
            }
            BlobResidenceExpectation::Private(generation)
        }
    };
    let decode_txn = match residence {
        PageResidence::Committed => state.selected_txn,
        PageResidence::CurrentPrivate { .. } => arena.born_txn,
        PageResidence::PriorPrivate { generation, .. } => generation,
    };
    let header = PageHeader::decode(&bytes, decode_txn)?;
    if let Some(level) = expected_level {
        if header.level != level {
            return Err(RetirementWriteError::ChildLevel {
                expected: level,
                actual: header.level,
            });
        }
    }
    match header.page_type {
        PageType::BlobLeaf => {
            if expected_level.unwrap_or(0) != 0 {
                return Err(RetirementWriteError::ChildType(PageType::BlobLeaf));
            }
            let leaf = BlobLeaf::open(&bytes, decode_txn, BlobKind::RetirementPageList)?;
            leaf.verify_local()?;
            if leaf.logical_offset() != expected_start {
                return Err(RetirementWriteError::BlobOffsetMismatch {
                    expected: expected_start,
                    actual: leaf.logical_offset(),
                });
            }
            let actual_end = expected_start
                .checked_add(u64::from(leaf.data_len()))
                .ok_or(RetirementWriteError::ArithmeticOverflow)?;
            if actual_end != expected_end
                || (actual_end < owner_length && leaf.data_len() != BLOB_LEAF_CAPACITY)
            {
                return Err(RetirementWriteError::BlobLengthMismatch {
                    expected: expected_end,
                    actual: actual_end,
                });
            }
            if retire {
                retire_blob_page(pgno, residence, staging, roles)?;
            }
            for chunk in leaf.data().chunks_exact(4) {
                let current = u32::from_le_bytes(chunk.try_into().unwrap());
                if current < 2 || u64::from(current) >= state.page_count {
                    return Err(RetirementWriteError::RetirementStreamPageOutOfBounds(
                        current,
                    ));
                }
                if previous.map(|prior| current <= prior).unwrap_or(false) {
                    return Err(RetirementWriteError::RetirementStreamOrder {
                        previous: previous.unwrap(),
                        current,
                    });
                }
                match listed_policy {
                    ListedPagePolicy::Register => roles.listed(current, false)?,
                    ListedPagePolicy::MarkRequired => roles.require_in_new_list(current)?,
                    ListedPagePolicy::SatisfyRequired => roles.listed(current, true)?,
                }
                *previous = Some(current);
                *values = values
                    .checked_add(1)
                    .ok_or(RetirementWriteError::ArithmeticOverflow)?;
            }
            Ok(())
        }
        PageType::BlobBranch => {
            let branch = BlobBranch::open(
                &bytes,
                decode_txn,
                BlobKind::RetirementPageList,
                arena.pending_page_count,
            )?;
            branch.verify_local()?;
            for index in 0..branch.len() {
                let child = branch.entry(index)?.child_pgno;
                let private =
                    require_blob_child(source, pgno, child, expected_residence, state, arena)?;
                roles.reference(child, PageRole::ReferencedRetirementBlob, private)?;
            }
            let first = branch.entry(0)?;
            if first.logical_offset != expected_start {
                return Err(RetirementWriteError::BlobOffsetMismatch {
                    expected: expected_start,
                    actual: first.logical_offset,
                });
            }
            if retire {
                retire_blob_page(pgno, residence, staging, roles)?;
            }
            for index in 0..branch.len() {
                let entry = branch.entry(index)?;
                let child_end = if index + 1 < branch.len() {
                    branch.entry(index + 1)?.logical_offset
                } else {
                    expected_end
                };
                if child_end <= entry.logical_offset || child_end > expected_end {
                    return Err(RetirementWriteError::BlobLengthMismatch {
                        expected: expected_end,
                        actual: child_end,
                    });
                }
                scan_blob_node(
                    source,
                    state,
                    arena,
                    pool_snapshot,
                    entry.child_pgno,
                    Some(branch.level() - 1),
                    entry.logical_offset,
                    child_end,
                    owner_length,
                    expected_residence,
                    listed_policy,
                    retire,
                    staging,
                    roles,
                    previous,
                    values,
                    depth + 1,
                )?;
            }
            Ok(())
        }
        other => Err(RetirementWriteError::ChildType(other)),
    }
}

fn encode_retirement_leaf_single(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    batch: RetirementBatch,
) {
    encode_retirement_leaf(page, born_txn, 1, |_, destination| {
        encode_retirement_batch(destination, batch);
    });
}

fn encode_retirement_leaf_with_append(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    old: RetirementLeaf<'_>,
    batch: RetirementBatch,
) {
    let old_len = old.len();
    encode_retirement_leaf(page, born_txn, old_len + 1, |index, destination| {
        encode_retirement_batch(
            destination,
            if index < old_len {
                old.batch(index).unwrap()
            } else {
                batch
            },
        );
    });
}

fn encode_retirement_leaf_with_replace(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    old: RetirementLeaf<'_>,
    batch: RetirementBatch,
) {
    let old_len = old.len();
    encode_retirement_leaf(page, born_txn, old_len, |index, destination| {
        encode_retirement_batch(
            destination,
            if index + 1 == old_len {
                batch
            } else {
                old.batch(index).unwrap()
            },
        );
    });
}

fn encode_retirement_leaf_suffix(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    old: RetirementLeaf<'_>,
    keep_from: usize,
) {
    let count = old.len() - keep_from;
    encode_retirement_leaf(page, born_txn, count, |index, destination| {
        encode_retirement_batch(destination, old.batch(keep_from + index).unwrap());
    });
}

fn encode_retirement_leaf(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    count: usize,
    mut encode: impl FnMut(usize, &mut [u8]),
) {
    page.fill(0);
    PageHeader {
        page_type: PageType::RetirementLeaf,
        born_txn,
        item_count: count as u16,
        level: 0,
        lower: (PAGE_HEADER_SIZE as usize + count * 32) as u16,
        upper: PAGE_SIZE as u16,
        aux: 0,
        page_crc32c: 0,
    }
    .encode_into(page);
    for index in 0..count {
        let at = PAGE_HEADER_SIZE as usize + index * 32;
        encode(index, &mut page[at..at + 32]);
    }
    page::write_crc32c(page);
}

fn encode_retirement_batch(destination: &mut [u8], batch: RetirementBatch) {
    destination.fill(0);
    destination[8..16].copy_from_slice(&batch.retired_by_txn.to_le_bytes());
    destination[16..24].copy_from_slice(&batch.page_count.to_le_bytes());
    destination[24..28].copy_from_slice(&batch.page_list_blob_root.to_le_bytes());
}

fn encode_retirement_branch_single(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    level: u16,
    child: ChildReference,
) {
    encode_retirement_branch(page, born_txn, level, 1, |_, destination| {
        encode_retirement_branch_entry(destination, child)
    });
}

fn encode_retirement_branch_two(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    level: u16,
    left: ChildReference,
    right: ChildReference,
) {
    encode_retirement_branch(page, born_txn, level, 2, |index, destination| {
        encode_retirement_branch_entry(destination, if index == 0 { left } else { right });
    });
}

fn encode_retirement_branch_right_edit(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    old: RetirementBranch<'_>,
    child: ChildReference,
    append: bool,
) {
    let old_len = old.len();
    let count = old_len + usize::from(append);
    encode_retirement_branch(page, born_txn, old.level(), count, |index, destination| {
        let reference = if (append && index == old_len) || (!append && index + 1 == old_len) {
            child
        } else {
            let entry = old.entry(index).unwrap();
            ChildReference {
                maximum: entry.max_retired_by_txn,
                pgno: entry.child_pgno,
                level: old.level() - 1,
            }
        };
        encode_retirement_branch_entry(destination, reference);
    });
}

fn encode_retirement_branch_suffix(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    old: RetirementBranch<'_>,
    keep_from: usize,
    first_child: ChildReference,
) {
    let count = old.len() - keep_from;
    encode_retirement_branch(page, born_txn, old.level(), count, |index, destination| {
        let child = if index == 0 {
            first_child
        } else {
            let entry = old.entry(keep_from + index).unwrap();
            ChildReference {
                maximum: entry.max_retired_by_txn,
                pgno: entry.child_pgno,
                level: old.level() - 1,
            }
        };
        encode_retirement_branch_entry(destination, child);
    });
}

fn encode_retirement_branch(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    level: u16,
    count: usize,
    mut encode: impl FnMut(usize, &mut [u8]),
) {
    page.fill(0);
    PageHeader {
        page_type: PageType::RetirementBranch,
        born_txn,
        item_count: count as u16,
        level,
        lower: (PAGE_HEADER_SIZE as usize + count * 16) as u16,
        upper: PAGE_SIZE as u16,
        aux: 0,
        page_crc32c: 0,
    }
    .encode_into(page);
    for index in 0..count {
        let at = PAGE_HEADER_SIZE as usize + index * 16;
        encode(index, &mut page[at..at + 16]);
    }
    page::write_crc32c(page);
}

fn encode_retirement_branch_entry(destination: &mut [u8], child: ChildReference) {
    destination.fill(0);
    destination[..8].copy_from_slice(&child.maximum.to_le_bytes());
    destination[8..12].copy_from_slice(&child.pgno.to_le_bytes());
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bitmap_cow::{
        BitmapCowArenaBinding, BitmapCowIndexNode, FreeBitmapCow, FreeBitmapInsertPage,
        ScopedFreeBitmapCowLedger, SealedFreeBitmapCoordinatorScratch, SharedFreeBitmapCowLedger,
    };
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::bitmap_cow::{
        FreeBitmapFinalizationCachedPage, FreeBitmapFinalizationScratch,
        FreeBitmapReclamationTicket, FreeBitmapReservationBuffers, FreeBitmapReservationPlanner,
        FreeBitmapReservationSourceNode, FreeBitmapReservationStageBuffers, ReservedBitmapPage,
        VerifiedBitmapPage,
    };
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::bootstrap::OpenMode;
    use crate::contract::{AddressFamily, MetaV4, ValueKind, ValueTag};
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::key::Ipv4Key;
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::name_binding::{basename_commitment, BasenameEncoding};
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::os::linux::live_reader::LinuxLiveReader;
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::os::linux::live_writer::LinuxLiveWriterBarrierCause;
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::os::linux::live_writer::{
        LinuxLiveWriter, LinuxLiveWriterNormalRangeWorkspace,
        LinuxLiveWriterNormalRangeWorkspaceCapacity, LinuxLiveWriterPageSinkError,
        LinuxLiveWriterReclaimError, LinuxLiveWriterReclaimFailure, LinuxLiveWriterReclaimLimits,
        LinuxLiveWriterReclaimOutcome, LinuxLiveWriterReclaimWorkspace,
        LinuxLiveWriterReclaimWorkspaceCapacity,
    };
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::os::linux::{linux_process_domain_token, LinuxOsError, LockMode, RetainedDirectory};
    use crate::page_number_index::{
        PageNumberIndex, PageNumberIndexPage, PageNumberIndexWorkspace,
    };
    use crate::page_source::SlicePageSource;
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::private_page_pool::PrivatePageCompositeBind;
    use crate::private_page_pool::{
        PrivatePageAuthorization, PrivatePageCoordinatorPriorReturn, PrivatePageOwner,
        PrivatePagePoolSlot, PrivatePagePreparedScopeSlot, PrivatePageSelectiveOverlayNode,
        PrivatePageSelectivePathEntry, PrivatePageSparseReplayIndex, PrivatePageSparseReplaySlot,
    };
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::range_ownership_walk::RangeTreeOwnershipScratch;
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::range_page::{encode_leaf, RangeRecord};
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::range_root_proof::{
        finalize_range_root_replacement_terminal, prepare_range_root_replacement_proof,
        RangeRootReplacementProofScratch, RangeRootReplacementTerminalScratch,
        RangeRootRetirementStageScratch,
    };
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::range_staging::{
        RangeTreePayloadReservationSlot, RangeTreePayloadScratch, RangeTreePhysicalAssignment,
    };
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::reclamation_finalizer::{
        finalize_selected_reclamation_terminal_export, plan_locked_reclamation_bitmap_reservation,
        prepare_locked_reclamation_bitmap_reservation,
        preview_selected_reclamation_protected_pages, stage_selected_reclamation_retirement,
        LockedReclamationBitmapPlanOutcome, LockedReclamationFinalizerLimits,
        LockedReclamationFinalizerScratch, ReclamationProtectedPagesScratch,
        SelectedReclamationRetirementScratch, SelectedReclamationTerminalCompositionError,
        SelectedReclamationTerminalCompositionFailure, SelectedReclamationTerminalScratch,
    };
    use crate::retirement_reader::{test_reclaimed_pages, RetirementReclamation};
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::retirement_reader::{RetirementIdentity, RetirementSelectionResult, RetirementTree};
    #[cfg(all(feature = "os", target_os = "linux"))]
    use crate::sidecar::{
        LocalIdentityKind, ProcessDomainKind, SidecarHeader, SidecarOrigin, SidecarState, SLOT_SIZE,
    };
    use crate::test_alloc::count_thread_allocations;
    use crate::writer_fixed_point::{
        FixedPointCoordinator, FixedPointError, FixedPointPreparedOutput,
        FixedPointPreparedWorkSlot, FixedPointPrivateOutputDrainError,
    };
    use crate::writer_transaction_contract::PrivateWriterResourceBudget;
    use crate::writer_transaction_core::{
        PrivateWriterTransactionCore, PrivateWriterTransactionError, PrivateWriterTransactionState,
    };
    use core::cell::Cell;
    #[cfg(all(feature = "os", target_os = "linux"))]
    use core::cell::RefCell;
    #[cfg(all(feature = "os", target_os = "linux"))]
    use std::fs::File;
    #[cfg(all(feature = "os", target_os = "linux"))]
    use std::os::unix::ffi::OsStrExt;
    #[cfg(all(feature = "os", target_os = "linux"))]
    use std::path::PathBuf;
    #[cfg(all(feature = "os", target_os = "linux"))]
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::{boxed::Box, vec, vec::Vec};

    const EMPTY_REPLACEMENT: CommittedPageReplacement = CommittedPageReplacement {
        pgno: 0,
        origin: CommittedPageOrigin::RetirementTree,
    };

    fn appended_slots(first: u32, count: usize) -> Vec<PrivatePageSlot> {
        (0..count)
            .map(|index| {
                PrivatePageSlot::authorized(
                    first + index as u32,
                    PrivatePageAuthorization::Appended,
                )
            })
            .collect()
    }

    #[derive(Debug, PartialEq, Eq)]
    struct AggregateCleanupError;

    #[cfg(all(feature = "os", target_os = "linux"))]
    impl From<LinuxLiveWriterPageSinkError> for AggregateCleanupError {
        fn from(_: LinuxLiveWriterPageSinkError) -> Self {
            Self
        }
    }

    enum CompletedPrivateOutputPath {
        Sink {
            fail_on_sink_call: Option<usize>,
        },
        #[cfg(all(feature = "os", target_os = "linux"))]
        LinuxSuccess,
        #[cfg(all(feature = "os", target_os = "linux"))]
        LinuxFinalizerFailure,
        #[cfg(all(feature = "os", target_os = "linux"))]
        LinuxSelectedMismatch,
        #[cfg(all(feature = "os", target_os = "linux"))]
        LinuxPhaseTwoNotCommitted,
        #[cfg(all(feature = "os", target_os = "linux"))]
        LinuxPhaseFourUnknown,
        #[cfg(all(feature = "os", target_os = "linux"))]
        LinuxPhaseFiveCommitted,
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    static NEXT_LIVE_DATABASE: AtomicU64 = AtomicU64::new(1);

    #[cfg(all(feature = "os", target_os = "linux"))]
    struct LiveTestDatabase {
        directory: PathBuf,
        main: PathBuf,
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    impl Drop for LiveTestDatabase {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.directory);
        }
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    fn live_test_database(meta: MetaV4, physical_pages: usize) -> LiveTestDatabase {
        assert!(physical_pages >= usize::try_from(meta.page_count).unwrap());
        let ordinal = NEXT_LIVE_DATABASE.fetch_add(1, Ordering::Relaxed);
        let directory = std::env::temp_dir().join(format!(
            "iprange-v4-retirement-writer-{}-{ordinal}",
            std::process::id()
        ));
        std::fs::create_dir(&directory).unwrap();
        let main = directory.join("main.iprdb");
        let sidecar = directory.join("main.iprdb.readers");
        let mut bytes = vec![0u8; physical_pages * PAGE_SIZE];
        meta.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into((&mut bytes[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
        std::fs::write(&main, bytes).unwrap();

        let (parent, main_component) = RetainedDirectory::open_parent(&main).unwrap();
        let sidecar_component = parent.sidecar_component(&main_component).unwrap();
        let created = File::create(&sidecar).unwrap();
        created
            .set_len(2 * PAGE_SIZE as u64 + 2 * u64::from(SLOT_SIZE))
            .unwrap();
        drop(created);
        let retained_main = parent.open_regular(&main_component, true).unwrap();
        let retained_sidecar = parent.open_regular(&sidecar_component, true).unwrap();
        let header = SidecarHeader {
            identity_kind: LocalIdentityKind::Posix,
            capacity: 1,
            state: SidecarState::Ready,
            database_id: meta.database_id,
            main_identity: retained_main.identity().encode(),
            sidecar_identity: retained_sidecar.identity().encode(),
            sidecar_id: [7; 16],
            origin: SidecarOrigin::CreateLive,
            attempted_txn_id: meta.txn_id,
            attempted_commit_nonce: meta.commit_nonce,
            attempted_main_bytes: meta.page_count * PAGE_SIZE as u64,
            attempted_main_sha512: [8; 64],
            process_domain_kind: ProcessDomainKind::LinuxPidNamespace,
            process_domain_token: linux_process_domain_token().unwrap(),
            basename_encoding: BasenameEncoding::PosixBytes as u16,
            basename_len: main_component.as_bytes().len() as u32,
            basename_commitment: basename_commitment(
                BasenameEncoding::PosixBytes,
                main_component.as_bytes(),
            )
            .unwrap(),
            creation_security_kind: 1,
            creation_security_commitment: [9; 32],
            header_seq: 1,
        };
        let mut block = [0u8; PAGE_SIZE];
        header.encode_into(&mut block);
        retained_sidecar.write_all_at(&block, 0).unwrap();
        retained_sidecar
            .write_all_at(&block, PAGE_SIZE as u64)
            .unwrap();
        drop(retained_sidecar);
        drop(retained_main);
        drop(parent);

        LiveTestDatabase { directory, main }
    }

    /// Fixed caller-owned storage for one ordinary range replacement while the
    /// Linux operation barrier is held. The production workspace will own the
    /// same categories; this fixture keeps the boundary allocation-free.
    #[cfg(all(feature = "os", target_os = "linux"))]
    const RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY: usize = 8;
    #[cfg(all(feature = "os", target_os = "linux"))]
    const RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY: usize = 24;

    #[cfg(all(feature = "os", target_os = "linux"))]
    struct RangeRootLiveBitmapStorage {
        arena: [ReservedBitmapPage; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
        pool_validation: [PrivatePageCompositeBind; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
        arena_bindings: [BitmapCowArenaBinding; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
        candidates: [u32; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
        verified: [VerifiedBitmapPage; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
        replacements: [u32; RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY],
        index: [BitmapCowIndexNode; RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY * 3],
        available: [usize; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
        source_nodes: [FreeBitmapReservationSourceNode; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY * 2],
        ticket: FreeBitmapReclamationTicket,
        stage_arena: [ReservedBitmapPage; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
        stage_bindings: [BitmapCowArenaBinding; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
        stage_candidates: [u32; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
        stage_verified: [VerifiedBitmapPage; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
        stage_replacements: [u32; RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY],
        stage_index: [BitmapCowIndexNode; RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY * 3],
        stage_available: [usize; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    impl RangeRootLiveBitmapStorage {
        fn new() -> Self {
            Self {
                arena: [const { ReservedBitmapPage::empty() };
                    RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
                pool_validation: [PrivatePageCompositeBind::empty();
                    RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
                arena_bindings: [BitmapCowArenaBinding::empty();
                    RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
                candidates: [0; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
                verified: [const { VerifiedBitmapPage::empty() };
                    RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
                replacements: [0; RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY],
                index: [BitmapCowIndexNode::empty();
                    RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY * 3],
                available: [0; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
                source_nodes: [FreeBitmapReservationSourceNode::empty();
                    RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY * 2],
                ticket: FreeBitmapReclamationTicket::new(),
                stage_arena: [const { ReservedBitmapPage::empty() };
                    RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
                stage_bindings: [BitmapCowArenaBinding::empty();
                    RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
                stage_candidates: [0; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
                stage_verified: [const { VerifiedBitmapPage::empty() };
                    RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
                stage_replacements: [0; RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY],
                stage_index: [BitmapCowIndexNode::empty();
                    RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY * 3],
                stage_available: [0; RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY],
            }
        }

        fn buffers(&mut self) -> FreeBitmapReservationBuffers<'_> {
            FreeBitmapReservationBuffers {
                arena: &mut self.arena,
                pool_validation: &mut self.pool_validation,
                arena_bindings: &mut self.arena_bindings,
                candidates: &mut self.candidates,
                verified_pages: &mut self.verified,
                replacements: &mut self.replacements,
                index_nodes: &mut self.index,
                available_slots: &mut self.available,
                source_nodes: &mut self.source_nodes,
                reclamation: &self.ticket,
                stage: FreeBitmapReservationStageBuffers {
                    arena: &mut self.stage_arena,
                    arena_bindings: &mut self.stage_bindings,
                    candidates: &mut self.stage_candidates,
                    verified_pages: &mut self.stage_verified,
                    replacements: &mut self.stage_replacements,
                    index_nodes: &mut self.stage_index,
                    available_slots: &mut self.stage_available,
                },
            }
        }
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    fn free_bitmap_leaf(txn: u64, bits: &[u32]) -> [u8; PAGE_SIZE] {
        let mut page_bytes = [0u8; PAGE_SIZE];
        for &bit in bits {
            let word = usize::try_from(bit / 64).unwrap();
            let offset = 32 + word * 8;
            let current = u64::from_le_bytes(
                page_bytes[offset..offset + 8]
                    .try_into()
                    .expect("bitmap leaf word fits"),
            );
            page_bytes[offset..offset + 8]
                .copy_from_slice(&(current | (1u64 << (bit % 64))).to_le_bytes());
        }
        let item_count = (0..(4032 - 32) / 8)
            .filter(|&word| {
                let offset = 32 + word * 8;
                u64::from_le_bytes(
                    page_bytes[offset..offset + 8]
                        .try_into()
                        .expect("bitmap leaf word fits"),
                ) != 0
            })
            .count();
        PageHeader {
            page_type: PageType::BitmapLeaf,
            born_txn: txn,
            item_count: u16::try_from(item_count).unwrap(),
            level: 0,
            lower: 4032,
            upper: PAGE_SIZE as u16,
            aux: 1,
            page_crc32c: 0,
        }
        .encode_into(&mut page_bytes);
        page::write_crc32c(&mut page_bytes);
        page_bytes
    }

    #[test]
    fn produced_terminal_cancel_releases_its_unactivated_scope() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 10, 10, 8).unwrap();
        let coordinator = FixedPointCoordinator::new(7, 0, 10).unwrap();
        coordinator.attach_pool(&pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let before = pool.coordinator_fence();
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
                || {
                    Ok(FixedPointPreparedOutput {
                        root: 5,
                        pending_page_count: 10,
                    })
                },
            )
            .unwrap();
        let mut pages = [PrivatePageCoordinatorTerminalPage::empty()];
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
            upper: PAGE_SIZE as u16,
            aux: 1,
            page_crc32c: 0,
        }
        .encode_into(&mut pages[0].bytes);
        page::write_crc32c(&mut pages[0].bytes);
        let export = PreparedProducedTerminalExport {
            result: RetirementTreeEditResult {
                root: 0,
                batch_count: 0,
                private_pages: 0,
                committed_replacements: 0,
                prior_private_replacements: 0,
            },
            range: None,
            bitmap: (),
            bitmap_root_provenance: ProducedBitmapRootProvenance::Terminal(5),
            pages: &mut pages,
        };
        let produced = match prepared.with_produced_terminal_export(&pool, export, 91) {
            Ok(produced) => produced,
            Err(_) => panic!("produced terminal must bind"),
        };
        let (cancelled, allocations) = count_thread_allocations(|| produced.cancel(&pool));
        assert_eq!(allocations, 0);
        assert!(cancelled.is_ok());
        assert_eq!(pages, [PrivatePageCoordinatorTerminalPage::empty()]);
        assert_eq!(work_slot, FixedPointPreparedWorkSlot::empty());
        assert_eq!(scope_slot, PrivatePagePreparedScopeSlot::empty());
        assert_eq!(pool.coordinator_fence(), before);
        coordinator.finish(predecessor).unwrap();
    }

    #[test]
    fn produced_terminal_binder_accepts_only_the_exact_unchanged_bitmap_root() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 10, 10, 8).unwrap();
        let coordinator = FixedPointCoordinator::new(7, 0, 10).unwrap();
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
                || {
                    Ok(FixedPointPreparedOutput {
                        root: 5,
                        pending_page_count: 10,
                    })
                },
            )
            .unwrap();
        let mut pages = [PrivatePageCoordinatorTerminalPage::empty()];
        pages[0].pgno = 6;
        pages[0].authorization = PrivatePageAuthorization::SafelyReclaimed;
        pages[0].owner = PrivatePageOwner::Range;
        pages[0].owner_generation = 8;
        pages[0].tag = 4;
        PageHeader {
            page_type: PageType::RangeLeaf,
            born_txn: 8,
            item_count: 0,
            level: 0,
            lower: 4032,
            upper: PAGE_SIZE as u16,
            aux: 4,
            page_crc32c: 0,
        }
        .encode_into(&mut pages[0].bytes);
        page::write_crc32c(&mut pages[0].bytes);
        let export = PreparedProducedTerminalExport {
            result: RetirementTreeEditResult {
                root: 0,
                batch_count: 0,
                private_pages: 0,
                committed_replacements: 0,
                prior_private_replacements: 0,
            },
            range: None,
            bitmap: (),
            bitmap_root_provenance: ProducedBitmapRootProvenance::SelectedUnchanged(5),
            pages: &mut pages,
        };
        let produced = match prepared.with_produced_terminal_export(&pool, export, 91) {
            Ok(produced) => produced,
            Err((_prepared, _export, error)) => {
                panic!("exact unchanged bitmap root must bind: {error:?}")
            }
        };
        produced.cancel(&pool).unwrap();
        assert_eq!(pages, [PrivatePageCoordinatorTerminalPage::empty()]);
        assert_eq!(work_slot, FixedPointPreparedWorkSlot::empty());
        assert_eq!(scope_slot, PrivatePagePreparedScopeSlot::empty());
        coordinator.finish(predecessor).unwrap();
    }

    #[test]
    fn produced_terminal_binder_rejects_a_substituted_unchanged_bitmap_root() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 10, 10, 8).unwrap();
        let coordinator = FixedPointCoordinator::new(7, 0, 10).unwrap();
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
                || {
                    Ok(FixedPointPreparedOutput {
                        root: 5,
                        pending_page_count: 10,
                    })
                },
            )
            .unwrap();
        let mut pages = [PrivatePageCoordinatorTerminalPage::empty()];
        pages[0].pgno = 6;
        pages[0].authorization = PrivatePageAuthorization::SafelyReclaimed;
        pages[0].owner = PrivatePageOwner::Range;
        pages[0].owner_generation = 8;
        pages[0].tag = 4;
        PageHeader {
            page_type: PageType::RangeLeaf,
            born_txn: 8,
            item_count: 0,
            level: 0,
            lower: 4032,
            upper: PAGE_SIZE as u16,
            aux: 4,
            page_crc32c: 0,
        }
        .encode_into(&mut pages[0].bytes);
        page::write_crc32c(&mut pages[0].bytes);
        let expected_page = pages[0].clone();
        let export = PreparedProducedTerminalExport {
            result: RetirementTreeEditResult {
                root: 0,
                batch_count: 0,
                private_pages: 0,
                committed_replacements: 0,
                prior_private_replacements: 0,
            },
            range: None,
            bitmap: (),
            bitmap_root_provenance: ProducedBitmapRootProvenance::SelectedUnchanged(6),
            pages: &mut pages,
        };
        let (prepared, export, error) =
            match prepared.with_produced_terminal_export(&pool, export, 91) {
                Ok(_) => panic!("substituted bitmap root must fail"),
                Err(parts) => parts,
            };
        assert_eq!(error, FixedPointError::StalePredecessor);
        assert_eq!(export.pages[0], expected_page);
        prepared.cancel(&pool).unwrap();
        assert_eq!(pages, [expected_page]);
        coordinator.finish(predecessor).unwrap();
    }

    #[test]
    fn produced_terminal_binder_rejects_malformed_range_target_before_scope_bind() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant_transaction(&mut slots, 10, 10, 8).unwrap();
        let coordinator = FixedPointCoordinator::new(7, 5, 10).unwrap();
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
                || {
                    Ok(FixedPointPreparedOutput {
                        root: 5,
                        pending_page_count: 10,
                    })
                },
            )
            .unwrap();
        let mut pages = [PrivatePageCoordinatorTerminalPage::empty()];
        pages[0].pgno = 6;
        pages[0].authorization = PrivatePageAuthorization::SafelyReclaimed;
        pages[0].owner = PrivatePageOwner::Range;
        pages[0].owner_generation = 8;
        pages[0].tag = 4;
        PageHeader {
            page_type: PageType::RangeLeaf,
            born_txn: 8,
            item_count: 0,
            level: 0,
            lower: 4032,
            upper: PAGE_SIZE as u16,
            aux: 4,
            page_crc32c: 0,
        }
        .encode_into(&mut pages[0].bytes);
        page::write_crc32c(&mut pages[0].bytes);
        let expected_page = pages[0].clone();
        let before_bind = pool.test_mutation_snapshot();
        let export = PreparedProducedTerminalExport {
            result: RetirementTreeEditResult {
                root: 0,
                batch_count: 0,
                private_pages: 0,
                committed_replacements: 0,
                prior_private_replacements: 0,
            },
            range: Some(RangeTreeMaterializedResult {
                root_pgno: 6,
                root_level: 0,
                record_count: 0,
                page_count: 2,
            }),
            bitmap: (),
            bitmap_root_provenance: ProducedBitmapRootProvenance::SelectedUnchanged(5),
            pages: &mut pages,
        };
        let (prepared, export, error) =
            match prepared.with_produced_terminal_export(&pool, export, 91) {
                Ok(_) => panic!("malformed range target must fail"),
                Err(parts) => parts,
            };
        assert_eq!(error, FixedPointError::StalePredecessor);
        assert_eq!(pool.test_mutation_snapshot(), before_bind);
        assert_eq!(export.pages[0], expected_page);
        prepared.cancel(&pool).unwrap();
        assert_eq!(pages, [expected_page]);
        coordinator.finish(predecessor).unwrap();
    }

    #[test]
    fn aggregate_cancel_releases_prepared_scope_terminal_and_workspace() {
        let selected = MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [1; 16],
            txn_id: 1,
            commit_nonce: [2; 16],
            page_count: 100,
            range_record_count: 0,
            active_feed_count: 0,
            feed_index_limit: 0,
            membership_entry_count: 0,
            membership_id_limit: 0,
            metadata_uncompressed_len: 0,
            metadata_compressed_len: 0,
            retirement_batch_count: 0,
            range_root: 0,
            catalog_name_root: 0,
            catalog_index_root: 0,
            feed_used_root: 0,
            membership_id_root: 0,
            membership_hash_root: 0,
            membership_used_root: 0,
            metadata_root: 0,
            free_bitmap_root: 0,
            retirement_root: 0,
        };
        let mut record_bindings = [BitmapCowArenaBinding::empty(); 1];
        let mut record_replacements = [];
        let mut record_index_nodes = [BitmapCowIndexNode::empty(); 1];
        let record_returned = [const { Cell::new(false) }; 1];
        let mut cleanup_nodes = [PrivatePageSelectiveOverlayNode::empty(); 8];
        let mut cleanup_path = [PrivatePageSelectivePathEntry::empty(); 8];
        let mut cleanup_targets = [usize::MAX; 1];
        let workspace_records = [FixedPointWorkspaceRecordSlot::new(
            SealedFreeBitmapCoordinatorScratch {
                arena_bindings: &mut record_bindings,
                replacements: &mut record_replacements,
                index_nodes: &mut record_index_nodes,
                returned: &record_returned,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            },
        )];
        let workspace_entries = [const { Cell::new(None) }; 1];
        let workspace_source_map = [const { Cell::new(usize::MAX) }; 1];
        let workspace_record_map = [const { Cell::new(usize::MAX) }; 1];
        let source_journal = [const { Cell::new(FixedPointSourceJournalWrite::EMPTY) }];
        let map_journal = [const { Cell::new(FixedPointMapJournalWrite::EMPTY) }; 2];
        let tombstone_journal = [const { Cell::new(FixedPointTombstoneJournalWrite::EMPTY) }];
        let journals =
            FixedPointCoordinatorJournals::new(&source_journal, &map_journal, &tombstone_journal);
        let mut ordered_prior_locations = [];
        let mut pool_returns = [];
        let mut new_locations = [DraftPrivatePageLocation::EMPTY; 1];
        let mut replay_slots = [const { PrivatePageSparseReplaySlot::empty() }; 4];
        let mut replay_index = [PrivatePageSparseReplayIndex::empty(); 1];
        let mut workspace = FixedPointCoordinatorWorkspace::new(
            &workspace_records,
            &workspace_entries,
            &workspace_source_map,
            &workspace_record_map,
            journals,
            &mut ordered_prior_locations,
            &mut pool_returns,
            &mut new_locations,
            &mut replay_slots,
            &mut replay_index,
            1,
        )
        .unwrap();
        let mut live_slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), AggregateCleanupError>::new(
            selected,
            PrivateWriterResourceBudget::new(workspace.retained_bytes(), 1, 1, 2),
            &mut live_slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        core.reserve_fixed_point_workspace(&handle, &workspace)
            .unwrap();
        let live_pool = core.draft(&handle).unwrap();
        let coordinator = core.fixed_point(&handle).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut preparation_scratch = [];
        let prepared = coordinator
            .prepare_work(
                &predecessor,
                live_pool,
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut preparation_scratch,
                || {
                    Ok(FixedPointPreparedOutput {
                        root: 5,
                        pending_page_count: 100,
                    })
                },
            )
            .unwrap();
        let mut pages = [PrivatePageCoordinatorTerminalPage::empty()];
        pages[0].pgno = 5;
        pages[0].authorization = PrivatePageAuthorization::SafelyReclaimed;
        pages[0].owner = PrivatePageOwner::Bitmap;
        pages[0].owner_generation = 2;
        PageHeader {
            page_type: PageType::BitmapLeaf,
            born_txn: 2,
            item_count: 0,
            level: 0,
            lower: 4032,
            upper: PAGE_SIZE as u16,
            aux: 1,
            page_crc32c: 0,
        }
        .encode_into(&mut pages[0].bytes);
        page::write_crc32c(&mut pages[0].bytes);
        let produced = match prepared.with_produced_terminal_export(
            live_pool,
            PreparedProducedTerminalExport {
                result: RetirementTreeEditResult {
                    root: 0,
                    batch_count: 0,
                    private_pages: 0,
                    committed_replacements: 0,
                    prior_private_replacements: 0,
                },
                range: None,
                bitmap: (),
                bitmap_root_provenance: ProducedBitmapRootProvenance::Terminal(5),
                pages: &mut pages,
            },
            91,
        ) {
            Ok(produced) => produced,
            Err((_, _, error)) => panic!("produced terminal must bind: {error:?}"),
        };
        let committed_bytes = vec![0; 100 * PAGE_SIZE];
        let committed = SlicePageSource::new(&committed_bytes, 100);
        let before = live_pool.test_mutation_snapshot();
        let aggregate = match workspace.prepare_aggregate(
            produced,
            coordinator,
            &predecessor,
            live_pool,
            &committed,
            &[],
        ) {
            Ok(aggregate) => aggregate,
            Err(_) => panic!("aggregate preparation must succeed"),
        };
        let (cancelled, allocations) = count_thread_allocations(|| aggregate.cancel(live_pool));
        assert_eq!(allocations, 0);
        assert!(cancelled.is_ok());
        assert_eq!(pages, [PrivatePageCoordinatorTerminalPage::empty()]);
        assert_eq!(work_slot, FixedPointPreparedWorkSlot::empty());
        assert_eq!(scope_slot, PrivatePagePreparedScopeSlot::empty());
        assert_eq!(live_pool.test_mutation_snapshot(), before);
        assert!(workspace.is_idle());
        assert!(workspace.record_slot_ready(0, 1));
        core.cancel_fixed_point_workspace(&handle, &mut workspace)
            .unwrap();
        assert_eq!(core.abort().unwrap(), 1);
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    enum LinuxFinalizerReclamationCase {
        NoChange,
        SelectedBatch,
    }

    fn drain_completed_fixed_point_private_output<'pages, B>(
        produced: PreparedProducedTerminalExport<'pages, B>,
        bitmap_root: u32,
        appended: u64,
        expected_retirement: RetirementTreeEditResult,
        path: CompletedPrivateOutputPath,
    ) {
        drain_completed_fixed_point_private_output_with_selected_range(
            produced,
            bitmap_root,
            appended,
            expected_retirement,
            None,
            path,
        );
    }

    fn drain_completed_fixed_point_private_output_with_selected_range<'pages, B>(
        produced: PreparedProducedTerminalExport<'pages, B>,
        bitmap_root: u32,
        appended: u64,
        expected_retirement: RetirementTreeEditResult,
        selected_range: Option<RangeTreeMaterializedResult>,
        path: CompletedPrivateOutputPath,
    ) {
        assert_eq!(produced.pages.len(), 3);
        let expected_range = produced.range;
        let mut expected_pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        for (destination, source) in expected_pages.iter_mut().zip(produced.pages.iter()) {
            destination.clone_from(source);
        }
        let appended_pages = expected_pages
            .iter()
            .filter(|page| page.authorization == PrivatePageAuthorization::Appended)
            .count() as u64;
        assert_eq!(appended_pages, appended);

        let mut selected = MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [1; 16],
            txn_id: 1,
            commit_nonce: [2; 16],
            page_count: 100,
            range_record_count: 0,
            active_feed_count: 0,
            feed_index_limit: 0,
            membership_entry_count: 0,
            membership_id_limit: 0,
            metadata_uncompressed_len: 0,
            metadata_compressed_len: 0,
            retirement_batch_count: 0,
            range_root: 0,
            catalog_name_root: 0,
            catalog_index_root: 0,
            feed_used_root: 0,
            membership_id_root: 0,
            membership_hash_root: 0,
            membership_used_root: 0,
            metadata_root: 0,
            free_bitmap_root: 0,
            retirement_root: 0,
        };
        if let Some(range) = selected_range {
            selected.range_root = range.root_pgno;
            selected.range_record_count = range.record_count;
        }
        let mut record_bindings = [BitmapCowArenaBinding::empty(); 3];
        let mut record_replacements = [];
        let mut record_index_nodes = [BitmapCowIndexNode::empty(); 3];
        let record_returned = [const { Cell::new(false) }; 3];
        let mut cleanup_nodes = [PrivatePageSelectiveOverlayNode::empty(); 16];
        let mut cleanup_path = [PrivatePageSelectivePathEntry::empty(); 16];
        let mut cleanup_targets = [usize::MAX; 3];
        let workspace_records = [FixedPointWorkspaceRecordSlot::new(
            SealedFreeBitmapCoordinatorScratch {
                arena_bindings: &mut record_bindings,
                replacements: &mut record_replacements,
                index_nodes: &mut record_index_nodes,
                returned: &record_returned,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            },
        )];
        let workspace_entries = [const { Cell::new(None) }; 3];
        let workspace_source_map = [const { Cell::new(usize::MAX) }; 3];
        let workspace_record_map = [const { Cell::new(usize::MAX) }; 3];
        let source_journal = [const { Cell::new(FixedPointSourceJournalWrite::EMPTY) }; 4];
        let map_journal = [const { Cell::new(FixedPointMapJournalWrite::EMPTY) }; 8];
        let tombstone_journal = [const { Cell::new(FixedPointTombstoneJournalWrite::EMPTY) }; 3];
        let journals =
            FixedPointCoordinatorJournals::new(&source_journal, &map_journal, &tombstone_journal);
        let mut ordered_prior_locations = [DraftPrivatePageLocation::EMPTY; 1];
        let mut pool_returns = [PrivatePageCoordinatorPriorReturn::empty(); 1];
        let mut new_locations = [DraftPrivatePageLocation::EMPTY; 3];
        let mut replay_slots = [const { PrivatePageSparseReplaySlot::empty() }; 16];
        let mut replay_index = [const { PrivatePageSparseReplayIndex::empty() }; 3];
        let mut workspace = FixedPointCoordinatorWorkspace::new(
            &workspace_records,
            &workspace_entries,
            &workspace_source_map,
            &workspace_record_map,
            journals,
            &mut ordered_prior_locations,
            &mut pool_returns,
            &mut new_locations,
            &mut replay_slots,
            &mut replay_index,
            3,
        )
        .unwrap();
        let workspace_bytes = workspace.retained_bytes();

        let mut insufficient_slots = [const { PrivatePagePoolSlot::empty() }; 3];
        let mut insufficient_cleanup = [];
        let mut insufficient = PrivateWriterTransactionCore::<(), (), AggregateCleanupError>::new(
            selected,
            PrivateWriterResourceBudget::new(workspace_bytes - 1, 3, 3, 2),
            &mut insufficient_slots,
            &mut insufficient_cleanup,
        )
        .unwrap();
        let insufficient_handle = insufficient.begin([3; 16]).unwrap();
        assert!(matches!(
            insufficient.reserve_fixed_point_workspace(&insufficient_handle, &workspace),
            Err(PrivateWriterTransactionError::InsufficientBudget { required, actual })
                if required == workspace_bytes && actual == workspace_bytes - 1
        ));
        assert!(workspace.is_idle());
        assert_eq!(insufficient.abort().unwrap(), 3);

        let mut live_slots = [const { PrivatePagePoolSlot::empty() }; 3];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), AggregateCleanupError>::new(
            selected,
            PrivateWriterResourceBudget::new(workspace_bytes, 3, 3, 2),
            &mut live_slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        core.reserve_fixed_point_workspace(&handle, &workspace)
            .unwrap();
        assert!(matches!(
            core.abort(),
            Err(PrivateWriterTransactionError::AbortIncompleteResource)
        ));
        let live_pool = core.draft(&handle).unwrap();
        let coordinator = core.fixed_point(&handle).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut preparation_scratch = [];
        let prepared = coordinator
            .prepare_work(
                &predecessor,
                live_pool,
                1,
                3,
                &mut work_slot,
                &mut scope_slot,
                &mut preparation_scratch,
                || {
                    Ok(FixedPointPreparedOutput {
                        root: bitmap_root,
                        pending_page_count: 100 + appended,
                    })
                },
            )
            .unwrap();
        let produced = match produced.bind_to_prepared_work(prepared, live_pool, 77) {
            Ok(produced) => produced,
            Err((_, _, error)) => panic!("typed producer export must bind: {error:?}"),
        };
        let committed_bytes = vec![0; 100 * PAGE_SIZE];
        let committed = SlicePageSource::new(&committed_bytes, 100);
        let invalid_prior_returns = [DraftPrivatePageLocation::EMPTY];
        let (produced, error) = match workspace.prepare_aggregate(
            produced,
            coordinator,
            &predecessor,
            live_pool,
            &committed,
            &invalid_prior_returns,
        ) {
            Err(failure) => failure,
            Ok(_) => panic!("invalid prior provenance must fail before consume"),
        };
        assert_eq!(error, FixedPointError::StalePredecessor);
        assert!(workspace.is_idle());
        assert!(workspace.record_slot_ready(0, 3));
        let aggregate = match workspace.prepare_aggregate(
            produced,
            coordinator,
            &predecessor,
            live_pool,
            &committed,
            &[],
        ) {
            Ok(aggregate) => aggregate,
            Err(_) => panic!("restored aggregate preparation must succeed"),
        };
        let (sealed, allocations) = count_thread_allocations(|| {
            match core.execute_fixed_point_aggregate(&handle, predecessor, aggregate) {
                Ok(sealed) => sealed,
                Err(_) => panic!("production core must execute the prepared aggregate"),
            }
        });
        assert_eq!(allocations, 0);
        assert_eq!(sealed.retirement_result(), expected_retirement);
        assert_eq!(sealed.record_index(), 0);
        assert_eq!(
            workspace.record_state(0),
            Some(crate::writer_fixed_point::FixedPointWorkspaceRecordState::Live(1))
        );
        for page in expected_pages.iter() {
            assert!(live_pool.find_bound_page(page.pgno).unwrap().is_some());
        }
        let (completion, completion_allocations) = count_thread_allocations(|| {
            core.complete_fixed_point_aggregate(&handle, &workspace, sealed)
        });
        let successor = match completion {
            Ok(successor) => successor,
            Err(error) => panic!("sealed aggregate handoff must complete: {error:?}"),
        };
        assert_eq!(completion_allocations, 0);
        assert_eq!(successor.root(), bitmap_root);
        assert_eq!(successor.pending_page_count(), 100 + appended);
        let target = core.target().unwrap();
        assert_eq!(target.free_bitmap_root, bitmap_root);
        assert_eq!(target.page_count, 100 + appended);
        assert_eq!(target.retirement_root, expected_retirement.root);
        assert_eq!(
            target.retirement_batch_count,
            expected_retirement.batch_count
        );
        match expected_range {
            Some(range) => {
                assert_eq!(target.range_root, range.root_pgno);
                assert_eq!(target.range_record_count, range.record_count);
            }
            None => {
                assert_eq!(target.range_root, selected.range_root);
                assert_eq!(target.range_record_count, selected.range_record_count);
            }
        }
        assert!(matches!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::StalePredecessor
            ))
        ));
        let (_, finish_allocations) = count_thread_allocations(|| {
            core.finish_fixed_point_input(&handle, &workspace, successor)
                .unwrap();
        });
        assert_eq!(finish_allocations, 0);
        assert!(core.fixed_point(&handle).unwrap().is_quiescent());
        let live_pool = core.draft(&handle).unwrap();
        assert!(live_pool.has_active_scopes());
        assert_eq!(
            live_pool.coordinator_commit_fence(),
            Err(crate::private_page_pool::PrivatePagePoolError::ScopeNotEmpty(1))
        );
        assert!(matches!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::Pool(
                crate::private_page_pool::PrivatePagePoolError::ScopeNotEmpty(1)
            ))
        ));
        match path {
            CompletedPrivateOutputPath::Sink { fail_on_sink_call } => {
                let (preparation, preparation_allocations) = count_thread_allocations(|| {
                    core.prepare_fixed_point_private_output(&handle, &workspace)
                });
                assert_eq!(preparation_allocations, 0);
                let preparation = preparation.unwrap();
                assert_eq!(preparation.target(), target);
                assert!(matches!(
                    core.draft(&handle),
                    Err(PrivateWriterTransactionError::FixedPoint(
                        FixedPointError::InvalidWorkUnit
                    ))
                ));
                assert!(matches!(
                    core.fixed_point(&handle),
                    Err(PrivateWriterTransactionError::FixedPoint(
                        FixedPointError::InvalidWorkUnit
                    ))
                ));

                let mut output_pages = [0u32; 3];
                let mut output_len = 0usize;
                let (drained, drain_allocations) = count_thread_allocations(|| {
                    let mut sink = |pgno: u32, bytes: &[u8]| {
                        let Some(expected) = expected_pages.iter().find(|page| page.pgno == pgno)
                        else {
                            panic!("private output must be one of the sealed terminal pages");
                        };
                        assert_eq!(bytes, &expected.bytes[..]);
                        output_pages[output_len] = pgno;
                        output_len += 1;
                        if fail_on_sink_call == Some(output_len) {
                            Err(AggregateCleanupError)
                        } else {
                            Ok(())
                        }
                    };
                    core.drain_fixed_point_private_pages(
                        &handle,
                        &preparation,
                        &mut workspace,
                        &mut sink,
                    )
                });
                assert_eq!(drain_allocations, 0);
                match fail_on_sink_call {
                    Some(failing_call) => {
                        assert_eq!(output_len, failing_call);
                        let error = drained.unwrap_err();
                        assert_eq!(error.code(), crate::error::ErrorCode::SinkFailed);
                        assert!(matches!(
                            error,
                            PrivateWriterTransactionError::FixedPointOutput(
                                FixedPointPrivateOutputDrainError::Sink(AggregateCleanupError)
                            )
                        ));
                        assert_eq!(core.state(), PrivateWriterTransactionState::AbortRequired);
                        assert_eq!(
                            workspace.record_state(0),
                            Some(
                                crate::writer_fixed_point::FixedPointWorkspaceRecordState::Live(1)
                            )
                        );
                        assert!(matches!(
                            core.preflight_commit(&handle),
                            Err(PrivateWriterTransactionError::AbortRequired(None))
                        ));
                        core.cancel_fixed_point_workspace(&handle, &mut workspace)
                            .unwrap();
                        assert!(workspace.is_idle());
                        assert!(matches!(
                            core.finish_fixed_point_private_output(
                                &handle,
                                preparation,
                                &mut workspace,
                            ),
                            Err(PrivateWriterTransactionError::StaleHandle)
                        ));
                        assert_eq!(core.abort().unwrap(), 3);
                    }
                    None => {
                        assert_eq!(drained.unwrap(), expected_pages.len());
                        assert_eq!(output_len, expected_pages.len());
                        for expected in expected_pages.iter() {
                            assert!(output_pages[..output_len].contains(&expected.pgno));
                        }
                        let (publication, publication_allocations) =
                            count_thread_allocations(|| {
                                core.finish_fixed_point_private_output(
                                    &handle,
                                    preparation,
                                    &mut workspace,
                                )
                            });
                        assert_eq!(publication_allocations, 0);
                        assert_eq!(publication.unwrap().target(), target);
                        assert!(workspace.is_idle());
                        core.preflight_commit(&handle).unwrap();
                        assert_eq!(core.abort().unwrap(), 3);
                    }
                }
            }
            #[cfg(all(feature = "os", target_os = "linux"))]
            CompletedPrivateOutputPath::LinuxSuccess => {
                let database = live_test_database(selected, selected.page_count as usize);
                let writer = LinuxLiveWriter::open(&database.main).unwrap();
                let (parent, main_component) =
                    RetainedDirectory::open_parent(&database.main).unwrap();
                let sidecar_component = parent.sidecar_component(&main_component).unwrap();
                let mut contender = parent.open_regular(&sidecar_component, true).unwrap();
                let (publication, publication_allocations) = count_thread_allocations(|| {
                    writer.finalize_and_publish_fixed_point_private_output(
                        &mut core,
                        &handle,
                        &mut workspace,
                        |context, _, _, _| {
                            let (source_selected, pages, fence) = context.into_parts();
                            assert_eq!(source_selected.meta, selected);
                            let mut page = [0u8; PAGE_SIZE];
                            pages.read_page(2, &mut page).unwrap();
                            assert_eq!(page, [0; PAGE_SIZE]);
                            assert!(matches!(
                                contender.acquire_lock(LockMode::Exclusive, true),
                                Err(LinuxOsError::LockBusy)
                            ));
                            let meta = source_selected.meta;
                            let tree = RetirementTree::from_source(
                                pages,
                                RetirementIdentity {
                                    database_id: meta.database_id,
                                    txn_id: meta.txn_id,
                                    commit_nonce: meta.commit_nonce,
                                    page_count: meta.page_count,
                                    root: meta.retirement_root,
                                    batch_count: meta.retirement_batch_count,
                                },
                            )
                            .unwrap();
                            assert!(matches!(
                                tree.select_oldest_eligible(fence, 1, 1).unwrap(),
                                RetirementSelectionResult::NoChange(_)
                            ));
                            Ok(())
                        },
                    )
                });
                assert_eq!(publication_allocations, 0);
                assert_eq!(publication.unwrap().meta, target);
                assert!(workspace.is_idle());
                assert_eq!(core.state(), PrivateWriterTransactionState::Clean);
                assert_eq!(core.selected(), target);
                assert_eq!(core.target(), None);
                assert!(matches!(
                    core.preflight_commit(&handle),
                    Err(PrivateWriterTransactionError::StaleHandle)
                ));
                writer.close().unwrap();
                contender.acquire_lock(LockMode::Exclusive, true).unwrap();
                contender.release_lock().unwrap();

                let committed = std::fs::read(&database.main).unwrap();
                assert_eq!(
                    crate::bootstrap::open(&committed, OpenMode::Writer)
                        .unwrap()
                        .meta,
                    target
                );
                for expected in expected_pages.iter() {
                    let offset = usize::try_from(expected.pgno).unwrap() * PAGE_SIZE;
                    assert_eq!(
                        &committed[offset..offset + PAGE_SIZE],
                        &expected.bytes[..],
                        "bridge must publish each exact fixed-point terminal page"
                    );
                }
            }
            #[cfg(all(feature = "os", target_os = "linux"))]
            CompletedPrivateOutputPath::LinuxFinalizerFailure => {
                let database = live_test_database(selected, selected.page_count as usize);
                let writer = LinuxLiveWriter::open(&database.main).unwrap();
                let (publication, publication_allocations) = count_thread_allocations(|| {
                    writer.finalize_and_publish_fixed_point_private_output(
                        &mut core,
                        &handle,
                        &mut workspace,
                        |_, _, _, _| Err(PrivateWriterTransactionError::SelectedGenerationMismatch),
                    )
                });
                assert_eq!(publication_allocations, 0);
                assert!(matches!(
                    publication,
                    Err(
                        crate::os::linux::live_writer::LinuxLiveWriterCoreCommitError::Core(
                            PrivateWriterTransactionError::SelectedGenerationMismatch
                        )
                    )
                ));
                assert!(!workspace.is_idle());
                assert_eq!(core.state(), PrivateWriterTransactionState::Pending);
                assert_eq!(core.selected(), selected);
                assert_eq!(core.target(), Some(target));
                writer.close().unwrap();
                assert_eq!(
                    crate::bootstrap::open(
                        &std::fs::read(&database.main).unwrap(),
                        OpenMode::Writer
                    )
                    .unwrap()
                    .meta,
                    selected
                );
                core.cancel_fixed_point_workspace(&handle, &mut workspace)
                    .unwrap();
                assert!(workspace.is_idle());
                assert_eq!(core.abort().unwrap(), 3);
            }
            #[cfg(all(feature = "os", target_os = "linux"))]
            CompletedPrivateOutputPath::LinuxPhaseTwoNotCommitted => {
                let database = live_test_database(selected, selected.page_count as usize);
                let writer = LinuxLiveWriter::open(&database.main).unwrap();
                let (publication, publication_allocations) = count_thread_allocations(|| {
                    writer.publish_fixed_point_private_output_with(
                        &mut core,
                        &handle,
                        &mut workspace,
                        |_| Err(std::io::Error::from_raw_os_error(libc::EIO)),
                        |main, target| {
                            let mut page = [0u8; PAGE_SIZE];
                            target.encode_into(&mut page);
                            main.write_all_at(&page, (target.txn_id & 1) * PAGE_SIZE as u64)
                        },
                        |files, owned| files.update_writer_lease_after_meta(owned),
                    )
                });
                assert_eq!(publication_allocations, 0);
                assert!(matches!(
                    publication,
                    Err(crate::os::linux::live_writer::LinuxLiveWriterCoreCommitError::Publication(
                        crate::os::linux::live_writer::LinuxLiveWriterPublicationError::NotCommitted(_)
                    ))
                ));
                assert!(workspace.is_idle());
                assert_eq!(core.state(), PrivateWriterTransactionState::Pending);
                assert_eq!(core.selected(), selected);
                assert_eq!(core.target(), Some(target));
                assert_eq!(core.abort().unwrap(), 3);
                writer.close().unwrap();
                assert_eq!(
                    crate::bootstrap::open(
                        &std::fs::read(&database.main).unwrap(),
                        OpenMode::Writer
                    )
                    .unwrap()
                    .meta,
                    selected
                );
            }
            #[cfg(all(feature = "os", target_os = "linux"))]
            CompletedPrivateOutputPath::LinuxPhaseFourUnknown => {
                let database = live_test_database(selected, selected.page_count as usize);
                let writer = LinuxLiveWriter::open(&database.main).unwrap();
                let synchronizations = Cell::new(0u8);
                let (publication, publication_allocations) = count_thread_allocations(|| {
                    writer.publish_fixed_point_private_output_with(
                        &mut core,
                        &handle,
                        &mut workspace,
                        |file| {
                            let next = synchronizations.get() + 1;
                            synchronizations.set(next);
                            if next == 2 {
                                Err(std::io::Error::from_raw_os_error(libc::EIO))
                            } else {
                                file.sync_all()
                            }
                        },
                        |main, target| {
                            let mut page = [0u8; PAGE_SIZE];
                            target.encode_into(&mut page);
                            main.write_all_at(&page, (target.txn_id & 1) * PAGE_SIZE as u64)
                        },
                        |files, owned| files.update_writer_lease_after_meta(owned),
                    )
                });
                assert_eq!(publication_allocations, 0);
                assert_eq!(synchronizations.get(), 2);
                assert!(matches!(
                    publication,
                    Err(crate::os::linux::live_writer::LinuxLiveWriterCoreCommitError::Publication(
                        crate::os::linux::live_writer::LinuxLiveWriterPublicationError::OutcomeUnknown(_)
                    ))
                ));
                assert_eq!(core.state(), PrivateWriterTransactionState::OutcomeUnknown);
                assert_eq!(core.selected(), selected);
                assert_eq!(core.target(), Some(target));
                assert_eq!(
                    core.abort(),
                    Err(PrivateWriterTransactionError::OutcomeUnknown)
                );
                writer.close().unwrap();

                let committed = std::fs::read(&database.main).unwrap();
                assert_eq!(
                    crate::bootstrap::open(&committed, OpenMode::Writer)
                        .unwrap()
                        .meta,
                    target
                );
            }
            #[cfg(all(feature = "os", target_os = "linux"))]
            CompletedPrivateOutputPath::LinuxPhaseFiveCommitted => {
                let database = live_test_database(selected, selected.page_count as usize);
                let writer = LinuxLiveWriter::open(&database.main).unwrap();
                let (publication, publication_allocations) = count_thread_allocations(|| {
                    writer.publish_fixed_point_private_output_with(
                        &mut core,
                        &handle,
                        &mut workspace,
                        |file| file.sync_all(),
                        |main, target| {
                            let mut page = [0u8; PAGE_SIZE];
                            target.encode_into(&mut page);
                            main.write_all_at(&page, (target.txn_id & 1) * PAGE_SIZE as u64)
                        },
                        |_, _| Err(crate::os::linux::LinuxWriterLeaseError::GenerationChanged),
                    )
                });
                assert_eq!(publication_allocations, 0);
                assert!(matches!(
                    publication,
                    Err(crate::os::linux::live_writer::LinuxLiveWriterCoreCommitError::Publication(
                        crate::os::linux::live_writer::LinuxLiveWriterPublicationError::Committed(_)
                    ))
                ));
                assert!(workspace.is_idle());
                assert_eq!(core.state(), PrivateWriterTransactionState::Clean);
                assert_eq!(core.selected(), target);
                assert_eq!(core.target(), None);
                assert!(matches!(
                    core.abort(),
                    Err(PrivateWriterTransactionError::NoPendingTransaction)
                ));
                writer.close().unwrap();
                assert_eq!(
                    crate::bootstrap::open(
                        &std::fs::read(&database.main).unwrap(),
                        OpenMode::Writer
                    )
                    .unwrap()
                    .meta,
                    target
                );
            }
            #[cfg(all(feature = "os", target_os = "linux"))]
            CompletedPrivateOutputPath::LinuxSelectedMismatch => {
                let mut writer_selected = selected;
                writer_selected.commit_nonce = [9; 16];
                let database =
                    live_test_database(writer_selected, writer_selected.page_count as usize);
                let writer = LinuxLiveWriter::open(&database.main).unwrap();
                let (publication, publication_allocations) = count_thread_allocations(|| {
                    writer.publish_fixed_point_private_output(&mut core, &handle, &mut workspace)
                });
                assert_eq!(publication_allocations, 0);
                assert!(matches!(
                    publication,
                    Err(
                        crate::os::linux::live_writer::LinuxLiveWriterCoreCommitError::Core(
                            PrivateWriterTransactionError::SelectedGenerationMismatch
                        )
                    )
                ));
                assert_eq!(core.state(), PrivateWriterTransactionState::Pending);
                assert_eq!(core.selected(), selected);
                assert_eq!(core.target(), Some(target));
                writer.close().unwrap();
                core.cancel_fixed_point_workspace(&handle, &mut workspace)
                    .unwrap();
                assert!(workspace.is_idle());
                assert_eq!(core.abort().unwrap(), 3);
            }
        }
    }

    #[test]
    fn produced_range_target_updates_one_private_meta_replacement() {
        let mut pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        pages[0].pgno = 5;
        pages[0].authorization = PrivatePageAuthorization::SafelyReclaimed;
        pages[0].owner = PrivatePageOwner::Range;
        pages[0].owner_generation = 2;
        pages[0].tag = 4;
        PageHeader {
            page_type: PageType::RangeLeaf,
            born_txn: 2,
            item_count: 0,
            level: 0,
            lower: 4032,
            upper: PAGE_SIZE as u16,
            aux: 4,
            page_crc32c: 0,
        }
        .encode_into(&mut pages[0].bytes);
        page::write_crc32c(&mut pages[0].bytes);

        pages[1].pgno = 6;
        pages[1].authorization = PrivatePageAuthorization::SafelyReclaimed;
        pages[1].owner = PrivatePageOwner::Bitmap;
        pages[1].owner_generation = 2;
        PageHeader {
            page_type: PageType::BitmapLeaf,
            born_txn: 2,
            item_count: 0,
            level: 0,
            lower: 4032,
            upper: PAGE_SIZE as u16,
            aux: 1,
            page_crc32c: 0,
        }
        .encode_into(&mut pages[1].bytes);
        page::write_crc32c(&mut pages[1].bytes);

        pages[2].pgno = 7;
        pages[2].authorization = PrivatePageAuthorization::SafelyReclaimed;
        pages[2].owner = PrivatePageOwner::Retirement;
        pages[2].owner_generation = 2;
        pages[2].tag = 1;
        encode_retirement_leaf_single(
            &mut pages[2].bytes,
            2,
            RetirementBatch {
                retired_by_txn: 1,
                page_count: 0,
                page_list_blob_root: 0,
            },
        );

        let retirement = RetirementTreeEditResult {
            root: 7,
            batch_count: 1,
            private_pages: 1,
            committed_replacements: 0,
            prior_private_replacements: 0,
        };
        drain_completed_fixed_point_private_output(
            PreparedProducedTerminalExport {
                result: retirement,
                range: Some(RangeTreeMaterializedResult {
                    root_pgno: 5,
                    root_level: 0,
                    record_count: 0,
                    page_count: 1,
                }),
                bitmap: (),
                bitmap_root_provenance: ProducedBitmapRootProvenance::Terminal(6),
                pages: &mut pages,
            },
            6,
            0,
            retirement,
            CompletedPrivateOutputPath::Sink {
                fail_on_sink_call: None,
            },
        );
    }

    #[test]
    fn produced_terminal_without_range_preserves_selected_range_target() {
        let mut pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        pages[0].pgno = 5;
        pages[0].authorization = PrivatePageAuthorization::SafelyReclaimed;
        pages[0].owner = PrivatePageOwner::Bitmap;
        pages[0].owner_generation = 2;
        PageHeader {
            page_type: PageType::BitmapLeaf,
            born_txn: 2,
            item_count: 0,
            level: 0,
            lower: 4032,
            upper: PAGE_SIZE as u16,
            aux: 1,
            page_crc32c: 0,
        }
        .encode_into(&mut pages[0].bytes);
        page::write_crc32c(&mut pages[0].bytes);

        for (page, (pgno, tag)) in pages[1..].iter_mut().zip([(6, 1), (7, 1)]) {
            page.pgno = pgno;
            page.authorization = PrivatePageAuthorization::SafelyReclaimed;
            page.owner = PrivatePageOwner::Retirement;
            page.owner_generation = 2;
            page.tag = tag;
            encode_retirement_leaf_single(
                &mut page.bytes,
                2,
                RetirementBatch {
                    retired_by_txn: 1,
                    page_count: 0,
                    page_list_blob_root: 0,
                },
            );
        }

        let retirement = RetirementTreeEditResult {
            root: 6,
            batch_count: 1,
            private_pages: 2,
            committed_replacements: 0,
            prior_private_replacements: 0,
        };
        drain_completed_fixed_point_private_output_with_selected_range(
            PreparedProducedTerminalExport {
                result: retirement,
                range: None,
                bitmap: (),
                bitmap_root_provenance: ProducedBitmapRootProvenance::Terminal(5),
                pages: &mut pages,
            },
            5,
            0,
            retirement,
            Some(RangeTreeMaterializedResult {
                root_pgno: 8,
                root_level: 0,
                record_count: 3,
                page_count: 1,
            }),
            CompletedPrivateOutputPath::Sink {
                fail_on_sink_call: None,
            },
        );
    }

    #[test]
    fn scoped_arena_preserves_scope_isolation_through_linux_publication() {
        let mut storage = vec![PrivatePageSlot::empty(); 8];
        let pool = PrivatePagePool::new_vacant(&mut storage, 100, 100, 2).unwrap();
        let target = pool.reserve_scope(4).unwrap();
        let foreign = pool.reserve_scope(4).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        for pgno in [20, 22, 24, 26] {
            pool.bind_page(
                &checkpoint,
                &target,
                pgno,
                PrivatePageAuthorization::CommittedFree,
            )
            .unwrap();
        }
        for pgno in [3, 5, 7, 9] {
            pool.bind_page(
                &checkpoint,
                &foreign,
                pgno,
                PrivatePageAuthorization::CommittedFree,
            )
            .unwrap();
        }
        pool.commit_checkpoint(checkpoint).unwrap();
        assert_eq!(
            PrivatePageArena::from_pool(&pool, 2).unwrap_err(),
            RetirementWriteError::PrivatePool(PrivatePagePoolError::StaleScope)
        );

        let mut arena = PrivatePageArena::from_scoped_pool(&pool, &target, 2).unwrap();
        let mut blob_pages = [0u32; 2];
        let mut blob_scratch = BlobBuildScratch::new(&mut blob_pages);
        let blob = RetirementBlobBuilder::build(&[50], &mut arena, &mut blob_scratch).unwrap();
        assert_eq!(blob.root(), 20);
        let source = SlicePageSource::new(&[], 100);
        let mut path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut replacement_entries = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_entries);
        let mut release_entries = [0u32; 4];
        let mut releases = PrivateReleaseBuffer::new(&mut release_entries);
        let mut role_entries = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_entries);
        let result = RetirementTreeEditor::upsert_newest(
            &source,
            RetirementTreeState {
                selected_txn: 1,
                page_count: 100,
                root: 0,
                batch_count: 0,
            },
            blob,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(result.root, 22);
        assert_eq!(pool.scoped_in_use(&target).unwrap(), 2);
        assert_eq!(pool.scoped_available(&foreign).unwrap(), 4);
        for pgno in [3, 5, 7, 9] {
            let slot = pool.find_in_scope(&foreign, pgno).unwrap().unwrap();
            assert_eq!(
                pool.scoped_slot_info(&foreign, slot)
                    .unwrap()
                    .unwrap()
                    .state,
                PrivatePagePoolState::Available
            );
        }

        let mut second_blob_pages = [0u32; 2];
        let second_blob = RetirementBlobBuilder::build(
            &[51],
            &mut arena,
            &mut BlobBuildScratch::new(&mut second_blob_pages),
        )
        .unwrap();
        assert_eq!(second_blob.root(), 24);
        let mut delete_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut upsert_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let combined = RetirementTreeEditor::delete_oldest_and_upsert_newest(
            &source,
            RetirementTreeState {
                selected_txn: 1,
                page_count: 100,
                root: result.root,
                batch_count: result.batch_count,
            },
            1,
            second_blob,
            &mut delete_path,
            &mut upsert_path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(combined.root, 26);
        assert_eq!(combined.batch_count, 1);
        assert_eq!(pool.scoped_in_use(&target).unwrap(), 2);
        for pgno in [20, 22] {
            let slot = pool.find_in_scope(&target, pgno).unwrap().unwrap();
            assert_eq!(
                pool.scoped_slot_info(&target, slot).unwrap().unwrap().state,
                PrivatePagePoolState::Available
            );
        }
        let mut wrong_pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        let mut wrong_result = combined;
        wrong_result.batch_count += 1;
        match arena.prepare_terminal_export(wrong_result, &mut wrong_pages) {
            Err(error) => assert_eq!(
                error,
                RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena)
            ),
            Ok(_) => panic!("substituted retirement result must be rejected"),
        }
        assert!(wrong_pages
            .iter()
            .all(|page| *page == PrivatePageCoordinatorTerminalPage::empty()));

        let mut terminal_pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        let (export, allocations) = count_thread_allocations(|| {
            arena.prepare_terminal_export(combined, &mut terminal_pages)
        });
        assert_eq!(allocations, 0);
        let export = export.unwrap();
        assert_eq!(export.result(), combined);
        assert_eq!(
            export
                .pages()
                .iter()
                .map(|page| page.pgno)
                .collect::<Vec<_>>(),
            [24, 26]
        );
        assert_eq!(export.pages()[0].pool_slot, usize::MAX);
        assert_eq!(export.pages()[1].pool_slot, usize::MAX);
        assert_eq!(export.pages()[0].tag, 2);
        assert_eq!(export.pages()[1].tag, 1);
        assert!(export.pages().iter().all(|page| {
            page.owner == PrivatePageOwner::Retirement
                && page.owner_generation == 2
                && page::verify_crc32c(&page.bytes)
        }));

        let mut typed_retirement_pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        let typed_retirement = arena
            .prepare_terminal_export(combined, &mut typed_retirement_pages)
            .unwrap();
        let mut produced_bitmap_pages = [PrivatePageCoordinatorTerminalPage::empty()];
        crate::bitmap_cow::tests::with_finalized_bitmap_export(
            &mut produced_bitmap_pages,
            |bitmap_export| {
                let bitmap_root = bitmap_export.root();
                let mut produced_pages = [
                    PrivatePageCoordinatorTerminalPage::empty(),
                    PrivatePageCoordinatorTerminalPage::empty(),
                    PrivatePageCoordinatorTerminalPage::empty(),
                ];
                let produced = match typed_retirement
                    .merge_with_bitmap_export(bitmap_export, &mut produced_pages)
                {
                    Ok(produced) => produced,
                    Err(_) => panic!("typed producer exports must merge"),
                };
                let appended = produced
                    .pages
                    .iter()
                    .filter(|page| page.authorization == PrivatePageAuthorization::Appended)
                    .count() as u64;

                #[cfg(all(feature = "os", target_os = "linux"))]
                {
                    let selected = MetaV4 {
                        address_family: AddressFamily::Ipv4,
                        value_kind: ValueKind::Direct,
                        value_tag: ValueTag::RETENTION,
                        database_id: [1; 16],
                        txn_id: 1,
                        commit_nonce: [2; 16],
                        page_count: 100,
                        range_record_count: 0,
                        active_feed_count: 0,
                        feed_index_limit: 0,
                        membership_entry_count: 0,
                        membership_id_limit: 0,
                        metadata_uncompressed_len: 0,
                        metadata_compressed_len: 0,
                        retirement_batch_count: 0,
                        range_root: 0,
                        catalog_name_root: 0,
                        catalog_index_root: 0,
                        feed_used_root: 0,
                        membership_id_root: 0,
                        membership_hash_root: 0,
                        membership_used_root: 0,
                        metadata_root: 0,
                        free_bitmap_root: 0,
                        retirement_root: 0,
                    };
                    let mut record_bindings = [BitmapCowArenaBinding::empty(); 3];
                    let mut record_replacements = [];
                    let mut record_index_nodes = [BitmapCowIndexNode::empty(); 3];
                    let record_returned = [const { Cell::new(false) }; 3];
                    let mut cleanup_nodes = [PrivatePageSelectiveOverlayNode::empty(); 16];
                    let mut cleanup_path = [PrivatePageSelectivePathEntry::empty(); 16];
                    let mut cleanup_targets = [usize::MAX; 3];
                    let workspace_records = [FixedPointWorkspaceRecordSlot::new(
                        SealedFreeBitmapCoordinatorScratch {
                            arena_bindings: &mut record_bindings,
                            replacements: &mut record_replacements,
                            index_nodes: &mut record_index_nodes,
                            returned: &record_returned,
                            cleanup_nodes: &mut cleanup_nodes,
                            cleanup_path: &mut cleanup_path,
                            cleanup_targets: &mut cleanup_targets,
                        },
                    )];
                    let workspace_entries = [const { Cell::new(None) }; 3];
                    let workspace_source_map = [const { Cell::new(usize::MAX) }; 3];
                    let workspace_record_map = [const { Cell::new(usize::MAX) }; 3];
                    let source_journal =
                        [const { Cell::new(FixedPointSourceJournalWrite::EMPTY) }; 4];
                    let map_journal = [const { Cell::new(FixedPointMapJournalWrite::EMPTY) }; 8];
                    let tombstone_journal =
                        [const { Cell::new(FixedPointTombstoneJournalWrite::EMPTY) }; 3];
                    let journals = FixedPointCoordinatorJournals::new(
                        &source_journal,
                        &map_journal,
                        &tombstone_journal,
                    );
                    let mut ordered_prior_locations = [DraftPrivatePageLocation::EMPTY; 1];
                    let mut pool_returns = [PrivatePageCoordinatorPriorReturn::empty(); 1];
                    let mut new_locations = [DraftPrivatePageLocation::EMPTY; 3];
                    let mut replay_slots = [const { PrivatePageSparseReplaySlot::empty() }; 16];
                    let mut replay_index = [const { PrivatePageSparseReplayIndex::empty() }; 3];
                    let mut workspace = FixedPointCoordinatorWorkspace::new(
                        &workspace_records,
                        &workspace_entries,
                        &workspace_source_map,
                        &workspace_record_map,
                        journals,
                        &mut ordered_prior_locations,
                        &mut pool_returns,
                        &mut new_locations,
                        &mut replay_slots,
                        &mut replay_index,
                        3,
                    )
                    .unwrap();
                    let workspace_bytes = workspace.retained_bytes();
                    let mut insufficient_slots = [const { PrivatePagePoolSlot::empty() }; 3];
                    let mut insufficient_cleanup = [];
                    let mut insufficient =
                        PrivateWriterTransactionCore::<(), (), AggregateCleanupError>::new(
                            selected,
                            PrivateWriterResourceBudget::new(workspace_bytes - 1, 3, 3, 2),
                            &mut insufficient_slots,
                            &mut insufficient_cleanup,
                        )
                        .unwrap();
                    let insufficient_handle = insufficient.begin([3; 16]).unwrap();
                    assert!(matches!(
                        insufficient.reserve_fixed_point_workspace(&insufficient_handle, &workspace),
                        Err(crate::writer_transaction_core::PrivateWriterTransactionError::InsufficientBudget {
                            required,
                            actual,
                        }) if required == workspace_bytes && actual == workspace_bytes - 1
                    ));
                    assert!(workspace.is_idle());
                    assert_eq!(insufficient.abort().unwrap(), 3);
                    let mut live_slots = [const { PrivatePagePoolSlot::empty() }; 3];
                    let mut cleanup = [];
                    let mut core =
                        PrivateWriterTransactionCore::<(), (), AggregateCleanupError>::new(
                            selected,
                            PrivateWriterResourceBudget::new(workspace_bytes, 3, 3, 2),
                            &mut live_slots,
                            &mut cleanup,
                        )
                        .unwrap();
                    let handle = core.begin([3; 16]).unwrap();
                    core.reserve_fixed_point_workspace(&handle, &workspace)
                        .unwrap();
                    assert!(matches!(
                    core.abort(),
                    Err(crate::writer_transaction_core::PrivateWriterTransactionError::AbortIncompleteResource)
                ));
                    let live_pool = core.draft(&handle).unwrap();
                    let coordinator = core.fixed_point(&handle).unwrap();
                    let predecessor = coordinator.predecessor().unwrap();
                    let mut work_slot = FixedPointPreparedWorkSlot::empty();
                    let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
                    let mut preparation_scratch = [];
                    let prepared = coordinator
                        .prepare_work(
                            &predecessor,
                            live_pool,
                            1,
                            3,
                            &mut work_slot,
                            &mut scope_slot,
                            &mut preparation_scratch,
                            || {
                                Ok(FixedPointPreparedOutput {
                                    root: bitmap_root,
                                    pending_page_count: 100 + appended,
                                })
                            },
                        )
                        .unwrap();
                    let produced = match produced.bind_to_prepared_work(prepared, live_pool, 77) {
                        Ok(produced) => produced,
                        Err(_) => panic!("typed producer export must bind"),
                    };
                    let committed_bytes = vec![0; 100 * PAGE_SIZE];
                    let committed = SlicePageSource::new(&committed_bytes, 100);
                    let invalid_prior_returns = [DraftPrivatePageLocation::EMPTY];
                    let (produced, error) = match workspace.prepare_aggregate(
                        produced,
                        coordinator,
                        &predecessor,
                        live_pool,
                        &committed,
                        &invalid_prior_returns,
                    ) {
                        Err(failure) => failure,
                        Ok(_) => panic!("invalid prior provenance must fail before consume"),
                    };
                    assert_eq!(error, FixedPointError::StalePredecessor);
                    assert!(workspace.is_idle());
                    assert!(workspace.record_slot_ready(0, 3));

                    assert!(!core.fixed_point(&handle).unwrap().is_quiescent());
                    assert!(workspace.is_idle());
                    let database = live_test_database(selected, selected.page_count as usize);
                    let writer = LinuxLiveWriter::open(&database.main).unwrap();
                    let finalizer_ran = Cell::new(false);
                    let (publication, publication_allocations) = count_thread_allocations(|| {
                        writer.finalize_and_publish_fixed_point_private_output(
                            &mut core,
                            &handle,
                            &mut workspace,
                            |context, core, handle, workspace| {
                                finalizer_ran.set(true);
                                let (source_selected, pages, _fence) = context.into_parts();
                                assert_eq!(source_selected.meta, selected);
                                let live_pool = core.draft(handle)?;
                                let coordinator = core.fixed_point(handle)?;
                                let aggregate = workspace
                                    .prepare_aggregate(
                                        produced,
                                        coordinator,
                                        &predecessor,
                                        live_pool,
                                        &pages,
                                        &[],
                                    )
                                    .map_err(|(_, error)| {
                                        PrivateWriterTransactionError::FixedPoint(error)
                                    })?;
                                let sealed = core
                                    .execute_fixed_point_aggregate(handle, predecessor, aggregate)
                                    .map_err(|(_, _, error)| {
                                        PrivateWriterTransactionError::FixedPoint(error)
                                    })?;
                                assert_eq!(sealed.retirement_result(), combined);
                                assert_eq!(sealed.record_index(), 0);
                                assert_eq!(
                                    workspace.record_state(0),
                                    Some(
                                        crate::writer_fixed_point::FixedPointWorkspaceRecordState::Live(1)
                                    )
                                );
                                assert!(live_pool.find_bound_page(bitmap_root).unwrap().is_some());
                                let successor = core
                                    .complete_fixed_point_aggregate(handle, workspace, sealed)?;
                                assert_eq!(successor.root(), bitmap_root);
                                assert_eq!(successor.pending_page_count(), 100 + appended);
                                let target = core.target().unwrap();
                                assert_eq!(target.free_bitmap_root, bitmap_root);
                                assert_eq!(target.page_count, 100 + appended);
                                assert_eq!(target.retirement_root, combined.root);
                                assert_eq!(target.retirement_batch_count, combined.batch_count);
                                assert!(matches!(
                                    core.preflight_commit(handle),
                                    Err(PrivateWriterTransactionError::FixedPoint(
                                        FixedPointError::StalePredecessor
                                    ))
                                ));
                                core.finish_fixed_point_input(handle, workspace, successor)
                                    .map_err(|(_, error)| error)?;
                                assert!(core.fixed_point(handle).unwrap().is_quiescent());
                                let live_pool = core.draft(handle).unwrap();
                                assert!(live_pool.has_active_scopes());
                                assert_eq!(
                                    live_pool.coordinator_commit_fence(),
                                    Err(crate::private_page_pool::PrivatePagePoolError::ScopeNotEmpty(1))
                                );
                                assert!(matches!(
                                    core.preflight_commit(handle),
                                    Err(PrivateWriterTransactionError::Pool(
                                        crate::private_page_pool::PrivatePagePoolError::ScopeNotEmpty(1)
                                    ))
                                ));
                                Ok(())
                            },
                        )
                    });
                    assert_eq!(publication_allocations, 0);
                    assert!(finalizer_ran.get());
                    let target = publication.unwrap().meta;
                    assert!(workspace.is_idle());
                    assert_eq!(core.state(), PrivateWriterTransactionState::Clean);
                    assert_eq!(core.selected(), target);
                    assert_eq!(core.target(), None);
                    assert!(matches!(
                    core.preflight_commit(&handle),
                    Err(crate::writer_transaction_core::PrivateWriterTransactionError::StaleHandle)
                ));
                    writer.close().unwrap();

                    let committed = std::fs::read(&database.main).unwrap();
                    assert_eq!(
                        crate::bootstrap::open(&committed, OpenMode::Writer)
                            .unwrap()
                            .meta,
                        target
                    );
                    for expected in produced_pages.iter() {
                        let offset = usize::try_from(expected.pgno).unwrap() * PAGE_SIZE;
                        assert_eq!(
                            &committed[offset..offset + PAGE_SIZE],
                            &expected.bytes[..],
                            "bridge must publish each exact fixed-point terminal page"
                        );
                    }
                }
                #[cfg(not(all(feature = "os", target_os = "linux")))]
                {
                    drain_completed_fixed_point_private_output(
                        produced,
                        bitmap_root,
                        appended,
                        combined,
                        CompletedPrivateOutputPath::Sink {
                            fail_on_sink_call: None,
                        },
                    );
                }
            },
        );

        let mut failed_retirement_pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        let failed_retirement = arena
            .prepare_terminal_export(combined, &mut failed_retirement_pages)
            .unwrap();
        let mut failed_bitmap_pages = [PrivatePageCoordinatorTerminalPage::empty()];
        crate::bitmap_cow::tests::with_finalized_bitmap_export(
            &mut failed_bitmap_pages,
            |bitmap_export| {
                let bitmap_root = bitmap_export.root();
                let mut produced_pages = [
                    PrivatePageCoordinatorTerminalPage::empty(),
                    PrivatePageCoordinatorTerminalPage::empty(),
                    PrivatePageCoordinatorTerminalPage::empty(),
                ];
                let produced = match failed_retirement
                    .merge_with_bitmap_export(bitmap_export, &mut produced_pages)
                {
                    Ok(produced) => produced,
                    Err(_) => panic!("typed producer exports must merge"),
                };
                let appended = produced
                    .pages
                    .iter()
                    .filter(|page| page.authorization == PrivatePageAuthorization::Appended)
                    .count() as u64;
                drain_completed_fixed_point_private_output(
                    produced,
                    bitmap_root,
                    appended,
                    combined,
                    CompletedPrivateOutputPath::Sink {
                        fail_on_sink_call: Some(2),
                    },
                );
            },
        );

        #[cfg(all(feature = "os", target_os = "linux"))]
        {
            let mut phase_two_retirement_pages = [
                PrivatePageCoordinatorTerminalPage::empty(),
                PrivatePageCoordinatorTerminalPage::empty(),
            ];
            let phase_two_retirement = arena
                .prepare_terminal_export(combined, &mut phase_two_retirement_pages)
                .unwrap();
            let mut phase_two_bitmap_pages = [PrivatePageCoordinatorTerminalPage::empty()];
            crate::bitmap_cow::tests::with_finalized_bitmap_export(
                &mut phase_two_bitmap_pages,
                |bitmap_export| {
                    let bitmap_root = bitmap_export.root();
                    let mut produced_pages = [
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                    ];
                    let produced = match phase_two_retirement
                        .merge_with_bitmap_export(bitmap_export, &mut produced_pages)
                    {
                        Ok(produced) => produced,
                        Err(_) => panic!("typed producer exports must merge"),
                    };
                    let appended = produced
                        .pages
                        .iter()
                        .filter(|page| page.authorization == PrivatePageAuthorization::Appended)
                        .count() as u64;
                    drain_completed_fixed_point_private_output(
                        produced,
                        bitmap_root,
                        appended,
                        combined,
                        CompletedPrivateOutputPath::LinuxPhaseTwoNotCommitted,
                    );
                },
            );
        }

        #[cfg(all(feature = "os", target_os = "linux"))]
        {
            let mut unknown_retirement_pages = [
                PrivatePageCoordinatorTerminalPage::empty(),
                PrivatePageCoordinatorTerminalPage::empty(),
            ];
            let unknown_retirement = arena
                .prepare_terminal_export(combined, &mut unknown_retirement_pages)
                .unwrap();
            let mut unknown_bitmap_pages = [PrivatePageCoordinatorTerminalPage::empty()];
            crate::bitmap_cow::tests::with_finalized_bitmap_export(
                &mut unknown_bitmap_pages,
                |bitmap_export| {
                    let bitmap_root = bitmap_export.root();
                    let mut produced_pages = [
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                    ];
                    let produced = match unknown_retirement
                        .merge_with_bitmap_export(bitmap_export, &mut produced_pages)
                    {
                        Ok(produced) => produced,
                        Err(_) => panic!("typed producer exports must merge"),
                    };
                    let appended = produced
                        .pages
                        .iter()
                        .filter(|page| page.authorization == PrivatePageAuthorization::Appended)
                        .count() as u64;
                    drain_completed_fixed_point_private_output(
                        produced,
                        bitmap_root,
                        appended,
                        combined,
                        CompletedPrivateOutputPath::LinuxPhaseFourUnknown,
                    );
                },
            );
        }

        #[cfg(all(feature = "os", target_os = "linux"))]
        {
            let mut phase_five_retirement_pages = [
                PrivatePageCoordinatorTerminalPage::empty(),
                PrivatePageCoordinatorTerminalPage::empty(),
            ];
            let phase_five_retirement = arena
                .prepare_terminal_export(combined, &mut phase_five_retirement_pages)
                .unwrap();
            let mut phase_five_bitmap_pages = [PrivatePageCoordinatorTerminalPage::empty()];
            crate::bitmap_cow::tests::with_finalized_bitmap_export(
                &mut phase_five_bitmap_pages,
                |bitmap_export| {
                    let bitmap_root = bitmap_export.root();
                    let mut produced_pages = [
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                    ];
                    let produced = match phase_five_retirement
                        .merge_with_bitmap_export(bitmap_export, &mut produced_pages)
                    {
                        Ok(produced) => produced,
                        Err(_) => panic!("typed producer exports must merge"),
                    };
                    let appended = produced
                        .pages
                        .iter()
                        .filter(|page| page.authorization == PrivatePageAuthorization::Appended)
                        .count() as u64;
                    drain_completed_fixed_point_private_output(
                        produced,
                        bitmap_root,
                        appended,
                        combined,
                        CompletedPrivateOutputPath::LinuxPhaseFiveCommitted,
                    );
                },
            );
        }

        #[cfg(all(feature = "os", target_os = "linux"))]
        {
            let mut mismatch_retirement_pages = [
                PrivatePageCoordinatorTerminalPage::empty(),
                PrivatePageCoordinatorTerminalPage::empty(),
            ];
            let mismatch_retirement = arena
                .prepare_terminal_export(combined, &mut mismatch_retirement_pages)
                .unwrap();
            let mut mismatch_bitmap_pages = [PrivatePageCoordinatorTerminalPage::empty()];
            crate::bitmap_cow::tests::with_finalized_bitmap_export(
                &mut mismatch_bitmap_pages,
                |bitmap_export| {
                    let bitmap_root = bitmap_export.root();
                    let mut produced_pages = [
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                    ];
                    let produced = match mismatch_retirement
                        .merge_with_bitmap_export(bitmap_export, &mut produced_pages)
                    {
                        Ok(produced) => produced,
                        Err(_) => panic!("typed producer exports must merge"),
                    };
                    let appended = produced
                        .pages
                        .iter()
                        .filter(|page| page.authorization == PrivatePageAuthorization::Appended)
                        .count() as u64;
                    drain_completed_fixed_point_private_output(
                        produced,
                        bitmap_root,
                        appended,
                        combined,
                        CompletedPrivateOutputPath::LinuxSelectedMismatch,
                    );
                },
            );
        }

        #[cfg(all(feature = "os", target_os = "linux"))]
        {
            let mut success_retirement_pages = [
                PrivatePageCoordinatorTerminalPage::empty(),
                PrivatePageCoordinatorTerminalPage::empty(),
            ];
            let success_retirement = arena
                .prepare_terminal_export(combined, &mut success_retirement_pages)
                .unwrap();
            let mut success_bitmap_pages = [PrivatePageCoordinatorTerminalPage::empty()];
            crate::bitmap_cow::tests::with_finalized_bitmap_export(
                &mut success_bitmap_pages,
                |bitmap_export| {
                    let bitmap_root = bitmap_export.root();
                    let mut produced_pages = [
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                    ];
                    let produced = match success_retirement
                        .merge_with_bitmap_export(bitmap_export, &mut produced_pages)
                    {
                        Ok(produced) => produced,
                        Err(_) => panic!("typed producer exports must merge"),
                    };
                    let appended = produced
                        .pages
                        .iter()
                        .filter(|page| page.authorization == PrivatePageAuthorization::Appended)
                        .count() as u64;
                    drain_completed_fixed_point_private_output(
                        produced,
                        bitmap_root,
                        appended,
                        combined,
                        CompletedPrivateOutputPath::LinuxSuccess,
                    );
                },
            );
        }

        #[cfg(all(feature = "os", target_os = "linux"))]
        {
            let mut finalizer_failure_retirement_pages = [
                PrivatePageCoordinatorTerminalPage::empty(),
                PrivatePageCoordinatorTerminalPage::empty(),
            ];
            let finalizer_failure_retirement = arena
                .prepare_terminal_export(combined, &mut finalizer_failure_retirement_pages)
                .unwrap();
            let mut finalizer_failure_bitmap_pages = [PrivatePageCoordinatorTerminalPage::empty()];
            crate::bitmap_cow::tests::with_finalized_bitmap_export(
                &mut finalizer_failure_bitmap_pages,
                |bitmap_export| {
                    let bitmap_root = bitmap_export.root();
                    let mut produced_pages = [
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                        PrivatePageCoordinatorTerminalPage::empty(),
                    ];
                    let produced = match finalizer_failure_retirement
                        .merge_with_bitmap_export(bitmap_export, &mut produced_pages)
                    {
                        Ok(produced) => produced,
                        Err(_) => panic!("typed producer exports must merge"),
                    };
                    let appended = produced
                        .pages
                        .iter()
                        .filter(|page| page.authorization == PrivatePageAuthorization::Appended)
                        .count() as u64;
                    drain_completed_fixed_point_private_output(
                        produced,
                        bitmap_root,
                        appended,
                        combined,
                        CompletedPrivateOutputPath::LinuxFinalizerFailure,
                    );
                },
            );
        }

        let mut bitmap_pages = [PrivatePageCoordinatorTerminalPage::empty()];
        bitmap_pages[0].pgno = 24;
        bitmap_pages[0].authorization = PrivatePageAuthorization::CommittedFree;
        bitmap_pages[0].owner = PrivatePageOwner::Bitmap;
        bitmap_pages[0].owner_generation = 2;
        PageHeader {
            page_type: PageType::BitmapLeaf,
            born_txn: 2,
            item_count: 0,
            level: 0,
            lower: 4032,
            upper: 4096,
            aux: 1,
            page_crc32c: 0,
        }
        .encode_into(&mut bitmap_pages[0].bytes);
        page::write_crc32c(&mut bitmap_pages[0].bytes);
        let mut combined_pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        let export = match export.merge_with_bitmap_pages(&bitmap_pages, &mut combined_pages) {
            Err((export, returned, error)) => {
                assert_eq!(
                    error,
                    RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena)
                );
                assert!(returned
                    .iter()
                    .all(|page| *page == PrivatePageCoordinatorTerminalPage::empty()));
                export
            }
            Ok(_) => panic!("cross-owner duplicate page numbers must be rejected"),
        };
        bitmap_pages[0].pgno = 28;
        let (merged, merge_allocations) = count_thread_allocations(|| {
            export.merge_with_bitmap_pages(&bitmap_pages, &mut combined_pages)
        });
        assert_eq!(merge_allocations, 0);
        let export = match merged {
            Ok(export) => export,
            Err(_) => panic!("bitmap and retirement terminal journals must merge"),
        };
        assert_eq!(
            export
                .pages
                .iter()
                .map(|page| (page.pgno, page.owner))
                .collect::<Vec<_>>(),
            [
                (24, PrivatePageOwner::Retirement),
                (26, PrivatePageOwner::Retirement),
                (28, PrivatePageOwner::Bitmap),
            ]
        );

        let mut live_slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let live_pool =
            PrivatePagePool::new_vacant_transaction(&mut live_slots, 100, 100, 2).unwrap();
        let coordinator = FixedPointCoordinator::new(1, 0, 100).unwrap();
        coordinator.attach_pool(&live_pool).unwrap();
        let predecessor = coordinator.predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let prepared = coordinator
            .prepare_work(
                &predecessor,
                &live_pool,
                1,
                3,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || {
                    Ok(FixedPointPreparedOutput {
                        root: 28,
                        pending_page_count: 100,
                    })
                },
            )
            .unwrap();
        let prepared = match export.bind_to_prepared_work(prepared, &live_pool, 77) {
            Ok(prepared) => prepared,
            Err(_) => panic!("sealed shadow export must bind to the exact live work"),
        };
        let (sealed, allocations) = count_thread_allocations(|| {
            coordinator.execute_retirement_terminal_prepared(predecessor, &live_pool, prepared)
        });
        assert_eq!(allocations, 0);
        let sealed = match sealed {
            Ok(sealed) => sealed,
            Err(_) => panic!("prepared shadow retirement journal must replay"),
        };
        assert_eq!(sealed.result, combined);
        for pgno in [24, 26, 28] {
            assert!(live_pool.find_bound_page(pgno).unwrap().is_some());
        }
        coordinator
            .abort_active_work(&live_pool, sealed.terminal.into_active())
            .unwrap();
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    fn compose_reclamation_protected_pages(
        bitmap: &[u32],
        retirement: &[CommittedPageReplacement],
        output: &mut [u32],
    ) -> usize {
        let raw_len = bitmap
            .len()
            .checked_add(retirement.len())
            .expect("fixture protected-page count fits usize");
        assert!(raw_len <= output.len());
        output[..bitmap.len()].copy_from_slice(bitmap);
        for (destination, replacement) in output[bitmap.len()..raw_len].iter_mut().zip(retirement) {
            *destination = replacement.pgno;
        }
        output[..raw_len].sort_unstable();
        let mut unique = 0usize;
        for index in 0..raw_len {
            let pgno = output[index];
            assert!(pgno >= 2);
            if unique == 0 || output[unique - 1] != pgno {
                output[unique] = pgno;
                unique += 1;
            }
        }
        unique
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    fn run_linux_finalizer_reclamation_case(case: LinuxFinalizerReclamationCase) {
        let (selected_txn, retirement_root, retirement_batch_count) = match case {
            LinuxFinalizerReclamationCase::NoChange => (1, 0, 0),
            LinuxFinalizerReclamationCase::SelectedBatch => (2, 12, 1),
        };
        let selected = MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [1; 16],
            txn_id: selected_txn,
            commit_nonce: [2; 16],
            page_count: 100,
            range_record_count: 0,
            active_feed_count: 0,
            feed_index_limit: 0,
            membership_entry_count: 0,
            membership_id_limit: 0,
            metadata_uncompressed_len: 0,
            metadata_compressed_len: 0,
            retirement_batch_count,
            range_root: 0,
            catalog_name_root: 0,
            catalog_index_root: 0,
            feed_used_root: 0,
            membership_id_root: 0,
            membership_hash_root: 0,
            membership_used_root: 0,
            metadata_root: 0,
            free_bitmap_root: 11,
            retirement_root,
        };
        let database = live_test_database(selected, selected.page_count as usize);
        let mut initial_bytes = std::fs::read(&database.main).unwrap();
        let free_bits = match case {
            LinuxFinalizerReclamationCase::NoChange => &[20, 22, 24, 26][..],
            LinuxFinalizerReclamationCase::SelectedBatch => &[20, 24][..],
        };
        let free_leaf = free_bitmap_leaf(selected.txn_id, free_bits);
        let leaf_offset = usize::try_from(selected.free_bitmap_root).unwrap() * PAGE_SIZE;
        initial_bytes[leaf_offset..leaf_offset + PAGE_SIZE].copy_from_slice(&free_leaf);
        if case == LinuxFinalizerReclamationCase::SelectedBatch {
            put_retirement_leaf(
                page_mut(&mut initial_bytes, selected.retirement_root),
                selected.txn_id,
                &[batch(selected.txn_id, 13, 2)],
            );
            put_blob_leaf(page_mut(&mut initial_bytes, 13), selected.txn_id, &[21, 23]);
        }
        std::fs::write(&database.main, initial_bytes).unwrap();
        let mut protected_reader = match case {
            LinuxFinalizerReclamationCase::NoChange => None,
            LinuxFinalizerReclamationCase::SelectedBatch => {
                Some(LinuxLiveReader::<Ipv4Key>::open(&database.main).unwrap())
            }
        };

        let mut record_bindings = [BitmapCowArenaBinding::empty(); 3];
        let mut record_replacements = [];
        let mut record_index_nodes = [BitmapCowIndexNode::empty(); 3];
        let record_returned = [const { Cell::new(false) }; 3];
        let mut cleanup_nodes = [PrivatePageSelectiveOverlayNode::empty(); 16];
        let mut cleanup_path = [PrivatePageSelectivePathEntry::empty(); 16];
        let mut cleanup_targets = [usize::MAX; 3];
        let workspace_records = [FixedPointWorkspaceRecordSlot::new(
            SealedFreeBitmapCoordinatorScratch {
                arena_bindings: &mut record_bindings,
                replacements: &mut record_replacements,
                index_nodes: &mut record_index_nodes,
                returned: &record_returned,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            },
        )];
        let workspace_entries = [const { Cell::new(None) }; 3];
        let workspace_source_map = [const { Cell::new(usize::MAX) }; 3];
        let workspace_record_map = [const { Cell::new(usize::MAX) }; 3];
        let source_journal = [const { Cell::new(FixedPointSourceJournalWrite::EMPTY) }; 4];
        let map_journal = [const { Cell::new(FixedPointMapJournalWrite::EMPTY) }; 8];
        let tombstone_journal = [const { Cell::new(FixedPointTombstoneJournalWrite::EMPTY) }; 3];
        let journals =
            FixedPointCoordinatorJournals::new(&source_journal, &map_journal, &tombstone_journal);
        let mut ordered_prior_locations = [DraftPrivatePageLocation::EMPTY; 1];
        let mut pool_returns = [PrivatePageCoordinatorPriorReturn::empty(); 1];
        let mut new_locations = [DraftPrivatePageLocation::EMPTY; 3];
        let mut replay_slots = [const { PrivatePageSparseReplaySlot::empty() }; 16];
        let mut replay_index = [const { PrivatePageSparseReplayIndex::empty() }; 3];
        let mut workspace = FixedPointCoordinatorWorkspace::new(
            &workspace_records,
            &workspace_entries,
            &workspace_source_map,
            &workspace_record_map,
            journals,
            &mut ordered_prior_locations,
            &mut pool_returns,
            &mut new_locations,
            &mut replay_slots,
            &mut replay_index,
            3,
        )
        .unwrap();
        let workspace_bytes = workspace.retained_bytes();
        let mut live_slots = [const { PrivatePagePoolSlot::empty() }; 3];
        let mut core = PrivateWriterTransactionCore::<(), (), AggregateCleanupError>::new(
            selected,
            PrivateWriterResourceBudget::new(workspace_bytes, 3, 3, 2),
            &mut live_slots,
            &mut [],
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        core.reserve_fixed_point_workspace(&handle, &workspace)
            .unwrap();
        let predecessor = core.fixed_point(&handle).unwrap().predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut preparation_scratch = [];
        let reserved = core
            .fixed_point(&handle)
            .unwrap()
            .prepare_reserved_work(
                &predecessor,
                core.draft(&handle).unwrap(),
                1,
                3,
                &mut work_slot,
                &mut scope_slot,
                &mut preparation_scratch,
            )
            .unwrap();
        assert!(!core.fixed_point(&handle).unwrap().is_quiescent());
        assert!(workspace.is_idle());

        // Every buffer below exists before the Linux operation barrier is
        // acquired. The finalizer can only use these bounded arrays.
        let mut planner_arena = [const { PrivatePagePoolSlot::empty() }; 4];
        let mut planner_pool_validation = [PrivatePageCompositeBind::empty(); 4];
        let mut planner_bindings = [BitmapCowArenaBinding::empty(); 4];
        let mut planner_candidates = [0u32; 4];
        let mut planner_verified = [const { VerifiedBitmapPage::empty() }; 4];
        let mut planner_replacements = [0u32; 16];
        let mut planner_index = [BitmapCowIndexNode::empty(); 32];
        let mut planner_available = [0usize; 4];
        let mut planner_source_nodes = [const { FreeBitmapReservationSourceNode::empty() }; 8];
        let reclamation_ticket = FreeBitmapReclamationTicket::new();
        let mut stage_arena = [const { PrivatePagePoolSlot::empty() }; 4];
        let mut stage_bindings = [BitmapCowArenaBinding::empty(); 4];
        let mut stage_candidates = [0u32; 4];
        let mut stage_verified = [const { VerifiedBitmapPage::empty() }; 4];
        let mut stage_replacements = [0u32; 16];
        let mut stage_index = [BitmapCowIndexNode::empty(); 32];
        let mut stage_available = [0usize; 4];
        let mut shadow_slots = [const { PrivatePagePoolSlot::empty() }; 4];
        let shadow_pool = PrivatePagePool::new_vacant(
            &mut shadow_slots[..3],
            selected.page_count,
            selected.page_count,
            selected.txn_id + 1,
        )
        .unwrap();
        let shadow_scope = shadow_pool.reserve_scope(3).unwrap();
        let mut verified_reclamation_batches = [RetirementBatch {
            retired_by_txn: 0,
            page_count: 0,
            page_list_blob_root: 0,
        }];
        let mut verified_reclaimed_pages = [0u32; 2];
        let mut blob_pages = [0u32; 1];
        let mut preview_blob_pages = [0u32; 1];
        let mut delete_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut upsert_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut preview_delete_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut preview_upsert_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut probe_delete_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut probe_upsert_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut probe_replacement_entries = [EMPTY_REPLACEMENT; 4];
        let mut probe_release_pages = [0u32; 4];
        let mut probe_roles = [PageRoleIndexSlot::new(); 16];
        let mut protected_replacement_pages = [0u32; 4];
        let mut next_protected_replacement_pages = [0u32; 4];
        let mut actual_protected_replacement_pages = [0u32; 4];
        let mut preview_bitmap_replacements = [0u32; 16];
        let mut preview_replacement_entries = [EMPTY_REPLACEMENT; 4];
        let mut preview_release_pages = [0u32; 4];
        let mut preview_roles = [PageRoleIndexSlot::new(); 16];
        let mut retirement_replacements = [EMPTY_REPLACEMENT; 4];
        let mut retirement_releases = [0u32; 4];
        let mut retirement_roles = [PageRoleIndexSlot::new(); 16];
        let mut final_release_pages = [0u32; 4];
        let mut final_insert_pages = [const { FreeBitmapInsertPage::empty() }; 32];
        let mut final_cached_pages = [const { FreeBitmapFinalizationCachedPage::empty() }; 12];
        let mut final_index_stack = [usize::MAX; 32];
        let mut final_cleanup_nodes = [PrivatePageSelectiveOverlayNode::empty(); 32];
        let mut final_cleanup_path = [PrivatePageSelectivePathEntry::empty(); 32];
        let mut final_cleanup_targets = [usize::MAX; 4];
        let mut helper_protected_snapshot = [0u32; 4];
        let mut retirement_terminal_pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        let mut bitmap_terminal_pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        let mut produced_terminal_pages = [
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ];
        let expected_terminal_pages = RefCell::new([
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
            PrivatePageCoordinatorTerminalPage::empty(),
        ]);
        let final_bitmap_root = Cell::new(0u32);
        let final_retirement_root = Cell::new(0u32);
        let finalizer_ran = Cell::new(false);

        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let (parent, main_component) = RetainedDirectory::open_parent(&database.main).unwrap();
        let sidecar_component = parent.sidecar_component(&main_component).unwrap();
        let mut contender = parent.open_regular(&sidecar_component, true).unwrap();
        let mut reserved = Some(reserved);
        let (publication, publication_allocations) = count_thread_allocations(|| {
            writer.finalize_and_publish_fixed_point_private_output(
                &mut core,
                &handle,
                &mut workspace,
                |context, core, handle, workspace| {
                    finalizer_ran.set(true);
                    let (source_selected, pages, reclaim_fence) = context.into_parts();
                    assert_eq!(source_selected.meta, selected);
                    assert!(matches!(
                        contender.acquire_lock(LockMode::Exclusive, true),
                        Err(LinuxOsError::LockBusy)
                    ));

                    let meta = source_selected.meta;
                    let retirement_identity = RetirementIdentity {
                        database_id: meta.database_id,
                        txn_id: meta.txn_id,
                        commit_nonce: meta.commit_nonce,
                        page_count: meta.page_count,
                        root: meta.retirement_root,
                        batch_count: meta.retirement_batch_count,
                    };
                    let (max_batches, max_pages) = match case {
                        LinuxFinalizerReclamationCase::NoChange => (1, 8),
                        LinuxFinalizerReclamationCase::SelectedBatch => (1, 2),
                    };
                    let scratch = LockedReclamationFinalizerScratch {
                        bitmap: FreeBitmapReservationBuffers {
                            arena: &mut planner_arena,
                            pool_validation: &mut planner_pool_validation,
                            arena_bindings: &mut planner_bindings,
                            candidates: &mut planner_candidates,
                            verified_pages: &mut planner_verified,
                            replacements: &mut planner_replacements,
                            index_nodes: &mut planner_index,
                            available_slots: &mut planner_available,
                            source_nodes: &mut planner_source_nodes,
                            reclamation: &reclamation_ticket,
                            stage: FreeBitmapReservationStageBuffers {
                                arena: &mut stage_arena,
                                arena_bindings: &mut stage_bindings,
                                candidates: &mut stage_candidates,
                                verified_pages: &mut stage_verified,
                                replacements: &mut stage_replacements,
                                index_nodes: &mut stage_index,
                                available_slots: &mut stage_available,
                            },
                        },
                        verified_batches: &mut verified_reclamation_batches,
                        verified_pages: &mut verified_reclaimed_pages,
                    };
                    let limits = LockedReclamationFinalizerLimits {
                        max_batches,
                        max_pages,
                        bitmap_payload_pages: 2,
                    };
                    let mut reservation = match case {
                        LinuxFinalizerReclamationCase::NoChange => {
                            prepare_locked_reclamation_bitmap_reservation(
                                selected,
                                &pages,
                                reclaim_fence,
                                limits,
                                scratch,
                                &shadow_pool,
                                &shadow_scope,
                            )
                            .unwrap()
                        }
                        LinuxFinalizerReclamationCase::SelectedBatch => {
                            let plan = match plan_locked_reclamation_bitmap_reservation(
                                selected,
                                &pages,
                                reclaim_fence,
                                limits,
                                scratch,
                            )
                            .unwrap()
                            {
                                LockedReclamationBitmapPlanOutcome::Selected(plan) => plan,
                                LockedReclamationBitmapPlanOutcome::NoChange => {
                                    panic!("selected retirement batch must produce a bitmap plan")
                                }
                            };
                            assert_eq!(
                                plan.required_private_pages(),
                                shadow_pool.scope_status(&shadow_scope).unwrap().capacity,
                                "the selected plan must size the shadow scope exactly before binding"
                            );
                            plan.bind(&shadow_pool, &shadow_scope).unwrap()
                        }
                    };
                    match case {
                        LinuxFinalizerReclamationCase::NoChange => {
                            assert_eq!(
                                reservation.pass,
                                crate::retirement_reader::RetirementPassResult {
                                    batch_count: 0,
                                    page_count: 0,
                                }
                            );
                        }
                        LinuxFinalizerReclamationCase::SelectedBatch => {
                            assert_eq!(
                                reservation.pass,
                                crate::retirement_reader::RetirementPassResult {
                                    batch_count: 1,
                                    page_count: 2,
                                }
                            );
                        }
                    }
                    assert_eq!(
                        reservation.bound.binding.reclaimed,
                        usize::from(case == LinuxFinalizerReclamationCase::SelectedBatch) * 2
                    );
                    assert_eq!(reservation.bound.cow.available_private_pages(), 2);
                    let retirement_state = RetirementTreeState {
                        selected_txn: selected.txn_id,
                        page_count: selected.page_count,
                        root: selected.retirement_root,
                        batch_count: selected.retirement_batch_count,
                    };

                    let mut selected_protected = None;
                    let protected_replacements = match case {
                        LinuxFinalizerReclamationCase::NoChange => &[50][..],
                        LinuxFinalizerReclamationCase::SelectedBatch => {
                            let helper_protected =
                                preview_selected_reclamation_protected_pages(
                                    &mut reservation.bound,
                                    ReclamationProtectedPagesScratch {
                                        probe_delete_path: &mut probe_delete_path,
                                        probe_upsert_path: &mut probe_upsert_path,
                                        probe_replacements: &mut probe_replacement_entries,
                                        probe_releases: &mut probe_release_pages,
                                        probe_roles: &mut probe_roles,
                                        protected_pages: &mut helper_protected_snapshot,
                                        next_protected_pages: &mut next_protected_replacement_pages,
                                        preview_bitmap_replacements: &mut preview_bitmap_replacements,
                                        preview_blob_pages: &mut preview_blob_pages,
                                        preview_delete_path: &mut preview_delete_path,
                                        preview_upsert_path: &mut preview_upsert_path,
                                        preview_replacements: &mut preview_replacement_entries,
                                        preview_releases: &mut preview_release_pages,
                                        preview_roles: &mut preview_roles,
                                        final_release_pages: &mut final_release_pages,
                                        final_insert_pages: &mut final_insert_pages,
                                        final_cached_pages: &mut final_cached_pages,
                                        final_index_stack: &mut final_index_stack,
                                        final_cleanup_nodes: &mut final_cleanup_nodes,
                                        final_cleanup_path: &mut final_cleanup_path,
                                        final_cleanup_targets: &mut final_cleanup_targets,
                                    },
                                )
                                .unwrap();
                            assert_eq!(helper_protected.blob_private_pages(), blob_pages.len());
                            assert_eq!(
                                helper_protected.retirement_private_page_budget(),
                                reservation.bound.cow.available_private_pages()
                            );
                            assert_eq!(
                                helper_protected.retirement_private_page_budget(),
                                helper_protected
                                    .blob_private_pages()
                                    .checked_add(helper_protected.tree_private_page_budget())
                                    .unwrap()
                            );
                            selected_protected = Some(helper_protected);
                            let mut probe_arena = PrivatePageArena::from_scoped_pool(
                                &shadow_pool,
                                &shadow_scope,
                                selected.txn_id + 1,
                            )
                            .unwrap();
                            let mut probe_replacements =
                                CommittedReplacementLedger::new(&mut probe_replacement_entries);
                            let mut probe_releases =
                                PrivateReleaseBuffer::new(&mut probe_release_pages);
                            let mut probe_roles = PageRoleIndex::new(&mut probe_roles);
                            let probe = RetirementTreeEditor::probe_reclaimed_oldest_and_append_newest(
                                &pages,
                                RetirementTreeState {
                                    selected_txn: selected.txn_id,
                                    page_count: selected.page_count,
                                    root: selected.retirement_root,
                                    batch_count: selected.retirement_batch_count,
                                },
                                retirement_identity,
                                reservation.bound.reclamation_authority(),
                                &mut probe_arena,
                                &mut probe_delete_path,
                                &mut probe_upsert_path,
                                &mut probe_replacements,
                                &mut probe_releases,
                                &mut probe_roles,
                            )
                            .unwrap();
                            assert_eq!(probe.replacement_count, probe_replacements.entries().len());
                            assert!(probe_releases.entries_from(0).is_empty());
                            assert_eq!(probe_arena.in_use_count().unwrap(), 0);

                            let mut protected_len = compose_reclamation_protected_pages(
                                reservation.bound.cow.replacements(),
                                probe_replacements.entries(),
                                &mut protected_replacement_pages,
                            );
                            assert_eq!(
                                RetirementBlobBuilder::required_private_pages(
                                    u64::try_from(protected_len).unwrap()
                                )
                                .unwrap(),
                                blob_pages.len()
                            );
                            let preview_requirements =
                                reservation.bound.finalization_scratch_requirements().unwrap();
                            assert!(preview_requirements.release_pages <= final_release_pages.len());
                            assert!(preview_requirements.insert_pages <= final_insert_pages.len());
                            assert!(preview_requirements.cached_pages <= final_cached_pages.len());
                            assert!(preview_requirements.index_stack <= final_index_stack.len());
                            assert!(preview_requirements.cleanup_nodes <= final_cleanup_nodes.len());
                            assert!(preview_requirements.cleanup_path <= final_cleanup_path.len());
                            assert!(
                                preview_requirements.cleanup_targets <= final_cleanup_targets.len()
                            );

                            let mut converged = false;
                            for _ in 0..=protected_replacement_pages.len() {
                                let candidate = &protected_replacement_pages[..protected_len];
                                let preview_replacement_len = Cell::new(0usize);
                                let preview_len = reservation
                                    .bound
                                    .preview_terminal_replacements_with_stage(
                                        FreeBitmapFinalizationScratch {
                                            release_pages: &mut final_release_pages
                                                [..preview_requirements.release_pages],
                                            insert_pages: &mut final_insert_pages
                                                [..preview_requirements.insert_pages],
                                            cached_pages: &mut final_cached_pages
                                                [..preview_requirements.cached_pages],
                                            index_stack: &mut final_index_stack
                                                [..preview_requirements.index_stack],
                                            cleanup_nodes: &mut final_cleanup_nodes
                                                [..preview_requirements.cleanup_nodes],
                                            cleanup_path: &mut final_cleanup_path
                                                [..preview_requirements.cleanup_path],
                                            cleanup_targets: &mut final_cleanup_targets
                                                [..preview_requirements.cleanup_targets],
                                        },
                                        &mut preview_bitmap_replacements,
                                        |reclamation, stage_pool, stage_scope| {
                                            let mut arena = PrivatePageArena::from_scoped_pool(
                                                stage_pool,
                                                stage_scope,
                                                selected.txn_id + 1,
                                            )?;
                                            assert_eq!(
                                                RetirementBlobBuilder::required_private_pages(
                                                    u64::try_from(candidate.len()).unwrap()
                                                )?,
                                                preview_blob_pages.len()
                                            );
                                            let blob = RetirementBlobBuilder::build(
                                                candidate,
                                                &mut arena,
                                                &mut BlobBuildScratch::new(
                                                    &mut preview_blob_pages,
                                                ),
                                            )?;
                                            let mut replacements = CommittedReplacementLedger::new(
                                                &mut preview_replacement_entries,
                                            );
                                            let mut releases = PrivateReleaseBuffer::new(
                                                &mut preview_release_pages,
                                            );
                                            let mut roles = PageRoleIndex::new(&mut preview_roles);
                                            let result = RetirementTreeEditor::delete_reclaimed_oldest_and_upsert_newest(
                                                &pages,
                                                retirement_state,
                                                retirement_identity,
                                                reclamation,
                                                blob,
                                                &mut preview_delete_path,
                                                &mut preview_upsert_path,
                                                &mut replacements,
                                                &mut releases,
                                                &mut roles,
                                            )?;
                                            preview_replacement_len
                                                .set(replacements.entries().len());
                                            let mut replacement_fingerprint = 0u64;
                                            for replacement in replacements.entries() {
                                                replacement_fingerprint = replacement_fingerprint
                                                    .wrapping_mul(0x100_0000_01b3)
                                                    ^ u64::from(replacement.pgno)
                                                    ^ u64::from(matches!(
                                                        replacement.origin,
                                                        CommittedPageOrigin::RetirementBlob
                                                    ));
                                            }
                                            Ok::<_, RetirementWriteError>((
                                                result,
                                                replacement_fingerprint,
                                                arena.in_use_count()?,
                                            ))
                                        },
                                    )
                                    .unwrap();
                                assert_eq!(
                                    preview_replacement_len.get(),
                                    probe_replacements.entries().len()
                                );
                                assert_eq!(
                                    &preview_replacement_entries[..probe.replacement_count],
                                    probe_replacements.entries()
                                );
                                let next_len = compose_reclamation_protected_pages(
                                    &preview_bitmap_replacements[..preview_len],
                                    probe_replacements.entries(),
                                    &mut next_protected_replacement_pages,
                                );
                                if next_protected_replacement_pages[..next_len] == candidate[..] {
                                    converged = true;
                                    break;
                                }
                                protected_replacement_pages[..next_len]
                                    .copy_from_slice(&next_protected_replacement_pages[..next_len]);
                                protected_len = next_len;
                            }
                            assert!(converged, "reclamation protected-page fixed point diverged");
                            &protected_replacement_pages[..protected_len]
                        }
                    };
                    if case == LinuxFinalizerReclamationCase::SelectedBatch {
                        assert_eq!(
                            selected_protected
                                .expect("selected reclamation must produce protected pages")
                                .pages(),
                            protected_replacements,
                            "private protected-list finalization must match the established staged fixed point"
                        );
                    }
                    if case == LinuxFinalizerReclamationCase::SelectedBatch {
                        assert_eq!(
                            validate_reclamation_authority(
                                retirement_state,
                                RetirementIdentity {
                                    commit_nonce: [9; 16],
                                    ..retirement_identity
                                },
                                reservation.bound.reclamation_authority().batch_count(),
                                reservation.bound.reclamation_authority(),
                            ),
                            Err(RetirementWriteError::ReclamationStateMismatch)
                        );
                        let scope_before = shadow_pool.scope_status(&shadow_scope).unwrap();
                        let mut short_blob_pages = [];
                        let error = match stage_selected_reclamation_retirement(
                            &mut reservation.bound,
                            &shadow_pool,
                            &shadow_scope,
                            selected_protected
                                .expect("selected reclamation must produce protected pages"),
                            SelectedReclamationRetirementScratch {
                                blob_pages: &mut short_blob_pages,
                                delete_path: &mut delete_path,
                                upsert_path: &mut upsert_path,
                                replacements: &mut retirement_replacements,
                                releases: &mut retirement_releases,
                                roles: &mut retirement_roles,
                                terminal_pages: &mut retirement_terminal_pages,
                            },
                        ) {
                            Ok(_) => panic!("short blob scratch must fail before mutation"),
                            Err(error) => error,
                        };
                        assert!(matches!(
                            error,
                            crate::reclamation_finalizer::SelectedReclamationRetirementStageError::BlobScratchTooSmall {
                                required: 1,
                                actual: 0,
                            }
                        ));
                        assert_eq!(shadow_pool.scope_status(&shadow_scope).unwrap(), scope_before);

                        let mut short_terminal_pages = [PrivatePageCoordinatorTerminalPage::empty()];
                        let error = match stage_selected_reclamation_retirement(
                            &mut reservation.bound,
                            &shadow_pool,
                            &shadow_scope,
                            selected_protected
                                .expect("selected reclamation must produce protected pages"),
                            SelectedReclamationRetirementScratch {
                                blob_pages: &mut blob_pages,
                                delete_path: &mut delete_path,
                                upsert_path: &mut upsert_path,
                                replacements: &mut retirement_replacements,
                                releases: &mut retirement_releases,
                                roles: &mut retirement_roles,
                                terminal_pages: &mut short_terminal_pages,
                            },
                        ) {
                            Ok(_) => panic!("short terminal journal must fail before mutation"),
                            Err(error) => error,
                        };
                        assert!(matches!(
                            error,
                            crate::reclamation_finalizer::SelectedReclamationRetirementStageError::TerminalPagesTooSmall {
                                required: 2,
                                actual: 1,
                            }
                        ));
                        assert_eq!(shadow_pool.scope_status(&shadow_scope).unwrap(), scope_before);

                        let replacement_scope = shadow_scope.seed().materialize(&shadow_pool);
                        let error = match stage_selected_reclamation_retirement(
                            &mut reservation.bound,
                            &shadow_pool,
                            &replacement_scope,
                            selected_protected
                                .expect("selected reclamation must produce protected pages"),
                            SelectedReclamationRetirementScratch {
                                blob_pages: &mut blob_pages,
                                delete_path: &mut delete_path,
                                upsert_path: &mut upsert_path,
                                replacements: &mut retirement_replacements,
                                releases: &mut retirement_releases,
                                roles: &mut retirement_roles,
                                terminal_pages: &mut retirement_terminal_pages,
                            },
                        ) {
                            Ok(_) => panic!("replacement scope must fail before mutation"),
                            Err(error) => error,
                        };
                        assert!(matches!(
                            error,
                            crate::reclamation_finalizer::SelectedReclamationRetirementStageError::PreMutationBitmap(
                                crate::bitmap_cow::FreeBitmapCowError::ArenaPageConflict(0)
                            )
                        ));
                        assert_eq!(shadow_pool.scope_status(&shadow_scope).unwrap(), scope_before);
                    }
                    let (retirement, bitmap_root, pending_page_count, produced) = match case {
                        LinuxFinalizerReclamationCase::NoChange => {
                            let mut bound = reservation.bound;
                            let mut arena = PrivatePageArena::from_scoped_pool(
                                &shadow_pool,
                                &shadow_scope,
                                selected.txn_id + 1,
                            )
                            .unwrap();
                            let blob = RetirementBlobBuilder::build(
                                protected_replacements,
                                &mut arena,
                                &mut BlobBuildScratch::new(&mut blob_pages),
                            )
                            .unwrap();
                            let mut replacements =
                                CommittedReplacementLedger::new(&mut retirement_replacements);
                            let mut releases = PrivateReleaseBuffer::new(&mut retirement_releases);
                            let mut roles = PageRoleIndex::new(&mut retirement_roles);
                            let retirement = RetirementTreeEditor::upsert_newest(
                                &pages,
                                retirement_state,
                                blob,
                                &mut upsert_path,
                                &mut replacements,
                                &mut releases,
                                &mut roles,
                            )
                            .unwrap();
                            let retirement_export = match arena
                                .prepare_terminal_export(retirement, &mut retirement_terminal_pages)
                            {
                                Ok(export) => export,
                                Err(error) => panic!(
                                    "retirement terminal export failed: {error:?}; result={retirement:?}; in_use={:?}",
                                    arena.in_use_count()
                                ),
                            };
                            bound.synchronize_reclamation_scope(&shadow_scope).unwrap();
                            let requirements = bound.finalization_scratch_requirements().unwrap();
                            assert!(requirements.release_pages <= final_release_pages.len());
                            assert!(requirements.insert_pages <= final_insert_pages.len());
                            assert!(requirements.cached_pages <= final_cached_pages.len());
                            assert!(requirements.index_stack <= final_index_stack.len());
                            assert!(requirements.cleanup_nodes <= final_cleanup_nodes.len());
                            assert!(requirements.cleanup_path <= final_cleanup_path.len());
                            assert!(requirements.cleanup_targets <= final_cleanup_targets.len());
                            let finalized = match bound.finalize(FreeBitmapFinalizationScratch {
                                release_pages: &mut final_release_pages[..requirements.release_pages],
                                insert_pages: &mut final_insert_pages[..requirements.insert_pages],
                                cached_pages: &mut final_cached_pages[..requirements.cached_pages],
                                index_stack: &mut final_index_stack[..requirements.index_stack],
                                cleanup_nodes: &mut final_cleanup_nodes[..requirements.cleanup_nodes],
                                cleanup_path: &mut final_cleanup_path[..requirements.cleanup_path],
                                cleanup_targets: &mut final_cleanup_targets[..requirements.cleanup_targets],
                            }) {
                                Ok(finalized) => finalized,
                                Err((_bound, error)) => {
                                    panic!("bitmap finalization failed: {error:?}")
                                }
                            };
                            let bitmap_root = finalized.output.root();
                            let pending_page_count = finalized.output.pending_page_count();
                            let bitmap_terminal_page_count =
                                finalized.output.bitmap_terminal_page_count();
                            assert_eq!(bitmap_terminal_page_count, 1);
                            let bitmap_export = match finalized
                                .output
                                .prepare_terminal_export(
                                    finalized.successor,
                                    &mut bitmap_terminal_pages[..bitmap_terminal_page_count],
                                )
                            {
                                Ok(export) => export,
                                Err((_output, _successor, _pages, error)) => panic!("{error:?}"),
                            };
                            let produced = match retirement_export
                                .merge_with_bitmap_export(bitmap_export, &mut produced_terminal_pages)
                            {
                                Ok(export) => export,
                                Err((_retirement, _bitmap, _pages, error)) => panic!("{error:?}"),
                            };
                            assert_eq!(produced.pages.len(), 3);
                            expected_terminal_pages
                                .borrow_mut()
                                .clone_from_slice(produced.pages);
                            let live_pool = core.draft(handle)?;
                            let produced = reserved
                                .take()
                                .expect("one reserved coordinator scope")
                                .with_finalized_produced_terminal_export(
                                    live_pool,
                                    FixedPointPreparedOutput {
                                        root: bitmap_root,
                                        pending_page_count,
                                    },
                                    produced,
                                    77,
                                )
                                .map_err(|(_reserved, _produced, error)| {
                                    PrivateWriterTransactionError::FixedPoint(error)
                                })?;
                            (retirement, bitmap_root, pending_page_count, produced)
                        }
                        LinuxFinalizerReclamationCase::SelectedBatch => {
                            let requirements = reservation
                                .bound
                                .finalization_scratch_requirements()
                                .unwrap();
                            let scope_before = shadow_pool.scope_status(&shadow_scope).unwrap();
                            let mut short_combined_pages = [
                                PrivatePageCoordinatorTerminalPage::empty(),
                                PrivatePageCoordinatorTerminalPage::empty(),
                            ];
                            let retry_reservation = match finalize_selected_reclamation_terminal_export(
                                reservation,
                                &shadow_pool,
                                &shadow_scope,
                                selected_protected
                                    .expect("selected reclamation must produce protected pages"),
                                SelectedReclamationTerminalScratch {
                                    retirement: SelectedReclamationRetirementScratch {
                                        blob_pages: &mut blob_pages,
                                        delete_path: &mut delete_path,
                                        upsert_path: &mut upsert_path,
                                        replacements: &mut retirement_replacements,
                                        releases: &mut retirement_releases,
                                        roles: &mut retirement_roles,
                                        terminal_pages: &mut retirement_terminal_pages,
                                    },
                                    bitmap_finalization: FreeBitmapFinalizationScratch {
                                        release_pages: &mut final_release_pages
                                            [..requirements.release_pages],
                                        insert_pages: &mut final_insert_pages
                                            [..requirements.insert_pages],
                                        cached_pages: &mut final_cached_pages
                                            [..requirements.cached_pages],
                                        index_stack: &mut final_index_stack
                                            [..requirements.index_stack],
                                        cleanup_nodes: &mut final_cleanup_nodes
                                            [..requirements.cleanup_nodes],
                                        cleanup_path: &mut final_cleanup_path
                                            [..requirements.cleanup_path],
                                        cleanup_targets: &mut final_cleanup_targets
                                            [..requirements.cleanup_targets],
                                    },
                                    bitmap_terminal_pages: &mut bitmap_terminal_pages,
                                    combined_terminal_pages: &mut short_combined_pages,
                                },
                            ) {
                                Ok(_) => panic!("short combined journal must fail before mutation"),
                                Err(SelectedReclamationTerminalCompositionFailure::Retry {
                                    reservation,
                                    error,
                                }) => {
                                    assert!(matches!(
                                        error,
                                        SelectedReclamationTerminalCompositionError::CombinedTerminalPagesTooSmall {
                                            required: 3,
                                            actual: 2,
                                        }
                                    ));
                                    reservation
                                }
                                Err(SelectedReclamationTerminalCompositionFailure::Discard {
                                    error,
                                }) => panic!("short combined journal must be retryable: {error:?}"),
                            };
                            assert_eq!(shadow_pool.scope_status(&shadow_scope).unwrap(), scope_before);

                            let scope_before = shadow_pool.scope_status(&shadow_scope).unwrap();
                            let mut short_bitmap_pages = [
                                PrivatePageCoordinatorTerminalPage::empty(),
                                PrivatePageCoordinatorTerminalPage::empty(),
                            ];
                            let retry_reservation = match finalize_selected_reclamation_terminal_export(
                                retry_reservation,
                                &shadow_pool,
                                &shadow_scope,
                                selected_protected
                                    .expect("selected reclamation must produce protected pages"),
                                SelectedReclamationTerminalScratch {
                                    retirement: SelectedReclamationRetirementScratch {
                                        blob_pages: &mut blob_pages,
                                        delete_path: &mut delete_path,
                                        upsert_path: &mut upsert_path,
                                        replacements: &mut retirement_replacements,
                                        releases: &mut retirement_releases,
                                        roles: &mut retirement_roles,
                                        terminal_pages: &mut retirement_terminal_pages,
                                    },
                                    bitmap_finalization: FreeBitmapFinalizationScratch {
                                        release_pages: &mut final_release_pages
                                            [..requirements.release_pages],
                                        insert_pages: &mut final_insert_pages
                                            [..requirements.insert_pages],
                                        cached_pages: &mut final_cached_pages
                                            [..requirements.cached_pages],
                                        index_stack: &mut final_index_stack
                                            [..requirements.index_stack],
                                        cleanup_nodes: &mut final_cleanup_nodes
                                            [..requirements.cleanup_nodes],
                                        cleanup_path: &mut final_cleanup_path
                                            [..requirements.cleanup_path],
                                        cleanup_targets: &mut final_cleanup_targets
                                            [..requirements.cleanup_targets],
                                    },
                                    bitmap_terminal_pages: &mut short_bitmap_pages,
                                    combined_terminal_pages: &mut produced_terminal_pages,
                                },
                            ) {
                                Ok(_) => panic!("short bitmap journal must fail before mutation"),
                                Err(SelectedReclamationTerminalCompositionFailure::Retry {
                                    reservation,
                                    error,
                                }) => {
                                    assert!(matches!(
                                        error,
                                        SelectedReclamationTerminalCompositionError::BitmapTerminalPagesTooSmall {
                                            required: 3,
                                            actual: 2,
                                        }
                                    ));
                                    reservation
                                }
                                Err(SelectedReclamationTerminalCompositionFailure::Discard {
                                    error,
                                }) => panic!("short bitmap journal must be retryable: {error:?}"),
                            };
                            assert_eq!(shadow_pool.scope_status(&shadow_scope).unwrap(), scope_before);

                            let prepared = match finalize_selected_reclamation_terminal_export(
                                retry_reservation,
                                &shadow_pool,
                                &shadow_scope,
                                selected_protected
                                    .expect("selected reclamation must produce protected pages"),
                                SelectedReclamationTerminalScratch {
                                    retirement: SelectedReclamationRetirementScratch {
                                        blob_pages: &mut blob_pages,
                                        delete_path: &mut delete_path,
                                        upsert_path: &mut upsert_path,
                                        replacements: &mut retirement_replacements,
                                        releases: &mut retirement_releases,
                                        roles: &mut retirement_roles,
                                        terminal_pages: &mut retirement_terminal_pages,
                                    },
                                    bitmap_finalization: FreeBitmapFinalizationScratch {
                                        release_pages: &mut final_release_pages
                                            [..requirements.release_pages],
                                        insert_pages: &mut final_insert_pages
                                            [..requirements.insert_pages],
                                        cached_pages: &mut final_cached_pages
                                            [..requirements.cached_pages],
                                        index_stack: &mut final_index_stack
                                            [..requirements.index_stack],
                                        cleanup_nodes: &mut final_cleanup_nodes
                                            [..requirements.cleanup_nodes],
                                        cleanup_path: &mut final_cleanup_path
                                            [..requirements.cleanup_path],
                                        cleanup_targets: &mut final_cleanup_targets
                                            [..requirements.cleanup_targets],
                                    },
                                    bitmap_terminal_pages: &mut bitmap_terminal_pages,
                                    combined_terminal_pages: &mut produced_terminal_pages,
                                },
                            ) {
                                Ok(prepared) => prepared,
                                Err(error) => panic!("selected terminal composition failed: {error:?}"),
                            };
                            assert_eq!(
                                prepared.pass(),
                                crate::retirement_reader::RetirementPassResult {
                                    batch_count: 1,
                                    page_count: 2,
                                }
                            );
                            let retirement = prepared.retirement_result();
                            let actual_len = compose_reclamation_protected_pages(
                                prepared.bitmap_replacements(),
                                &retirement_replacements[..retirement.committed_replacements],
                                &mut actual_protected_replacement_pages,
                            );
                            assert_eq!(
                                &actual_protected_replacement_pages[..actual_len],
                                protected_replacements,
                                "the staged fixed point must equal the actual terminal replacements"
                            );
                            let bitmap_root = prepared.bitmap_root();
                            let pending_page_count = prepared.pending_page_count();
                            assert_eq!(prepared.terminal_pages().len(), 3);
                            expected_terminal_pages
                                .borrow_mut()
                                .clone_from_slice(prepared.terminal_pages());
                            let live_pool = core.draft(handle)?;
                            let (reserved_work, prepared) = match prepared.bind_to_reserved_work(
                                reserved.take().expect("one reserved coordinator scope"),
                                live_pool,
                                0,
                            ) {
                                Ok(_) => panic!("zero nonce must reject coordinator binding"),
                                Err((
                                    reserved_work,
                                    prepared,
                                    FixedPointError::StalePredecessor,
                                )) => (reserved_work, prepared),
                                Err((_reserved_work, _prepared, error)) => {
                                    panic!("zero nonce returned wrong bind error: {error:?}")
                                }
                            };
                            {
                                let expected = expected_terminal_pages.borrow();
                                assert_eq!(prepared.terminal_pages(), &expected[..]);
                            }
                            let produced = prepared
                                .bind_to_reserved_work(reserved_work, live_pool, 77)
                                .map_err(|(_reserved, _prepared, error)| {
                                    PrivateWriterTransactionError::FixedPoint(error)
                                })?;
                            (retirement, bitmap_root, pending_page_count, produced)
                        }
                    };
                    final_bitmap_root.set(bitmap_root);
                    final_retirement_root.set(retirement.root);

                    let live_pool = core.draft(handle)?;
                    let coordinator = core.fixed_point(handle)?;
                    let aggregate = workspace
                        .prepare_aggregate(
                            produced,
                            coordinator,
                            &predecessor,
                            live_pool,
                            &pages,
                            &[],
                        )
                        .map_err(|(_produced, error)| {
                            PrivateWriterTransactionError::FixedPoint(error)
                        })?;
                    let sealed = core
                        .execute_fixed_point_aggregate(handle, predecessor, aggregate)
                        .map_err(|(_predecessor, _aggregate, error)| {
                            PrivateWriterTransactionError::FixedPoint(error)
                        })?;
                    assert_eq!(sealed.retirement_result(), retirement);
                    let successor = core.complete_fixed_point_aggregate(handle, workspace, sealed)?;
                    assert_eq!(successor.root(), bitmap_root);
                    assert_eq!(successor.pending_page_count(), pending_page_count);
                    let target = core.target().expect("aggregate updates the target metadata");
                    assert_eq!(target.free_bitmap_root, bitmap_root);
                    assert_eq!(target.retirement_root, retirement.root);
                    assert_eq!(target.retirement_batch_count, retirement.batch_count);
                    core.finish_fixed_point_input(handle, workspace, successor)
                        .map_err(|(_successor, error)| error)?;
                    assert!(core.fixed_point(handle)?.is_quiescent());
                    Ok(())
                },
            )
        });
        assert_eq!(publication_allocations, 0);
        assert_eq!(
            bitmap_terminal_pages[1],
            PrivatePageCoordinatorTerminalPage::empty()
        );
        assert_eq!(
            bitmap_terminal_pages[2],
            PrivatePageCoordinatorTerminalPage::empty()
        );
        assert!(finalizer_ran.get());
        let target = publication.unwrap().meta;
        assert_eq!(target.free_bitmap_root, final_bitmap_root.get());
        assert_eq!(target.retirement_root, final_retirement_root.get());
        assert_eq!(target.page_count, selected.page_count);
        assert!(workspace.is_idle());
        assert_eq!(core.state(), PrivateWriterTransactionState::Clean);
        assert_eq!(core.selected(), target);
        assert_eq!(core.target(), None);
        writer.close().unwrap();
        if let Some(reader) = protected_reader.as_mut() {
            reader.close().unwrap();
        }
        contender.acquire_lock(LockMode::Exclusive, true).unwrap();
        contender.release_lock().unwrap();

        let committed = std::fs::read(&database.main).unwrap();
        assert_eq!(
            crate::bootstrap::open(&committed, OpenMode::Writer)
                .unwrap()
                .meta,
            target
        );
        let expected = expected_terminal_pages.borrow();
        assert_eq!(
            expected
                .iter()
                .map(|page| (page.pgno, page.owner, page.tag))
                .collect::<Vec<_>>(),
            match case {
                LinuxFinalizerReclamationCase::NoChange => [
                    (20, PrivatePageOwner::Bitmap, 11),
                    (22, PrivatePageOwner::Retirement, 2),
                    (24, PrivatePageOwner::Retirement, 1),
                ],
                LinuxFinalizerReclamationCase::SelectedBatch => [
                    (20, PrivatePageOwner::Bitmap, 11),
                    (21, PrivatePageOwner::Retirement, 2),
                    (23, PrivatePageOwner::Retirement, 1),
                ],
            }
        );
        for page in expected.iter() {
            let offset = usize::try_from(page.pgno).unwrap() * PAGE_SIZE;
            assert_eq!(
                &committed[offset..offset + PAGE_SIZE],
                &page.bytes,
                "the lock-bound terminal page must be the byte sequence published"
            );
        }
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    #[test]
    fn linux_finalizer_selects_bitmap_pages_under_the_held_operation_lock() {
        run_linux_finalizer_reclamation_case(LinuxFinalizerReclamationCase::NoChange);
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    #[test]
    fn linux_finalizer_reclaims_selected_retirement_batch_under_the_held_operation_lock() {
        run_linux_finalizer_reclamation_case(LinuxFinalizerReclamationCase::SelectedBatch);
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    enum LinuxOrdinaryRangeReplacementCase {
        Publish,
        RejectLateCoreBinding,
        RejectShortCoordinatorJournal,
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    fn run_linux_ordinary_range_replacement_case(case: LinuxOrdinaryRangeReplacementCase) {
        const PAGE_COUNT: u64 = 64;
        const MAX_PRIVATE_PAGES: usize = RANGE_ROOT_LIVE_PRIVATE_PAGE_CAPACITY;
        let coordinator_new_location_capacity = match case {
            LinuxOrdinaryRangeReplacementCase::RejectShortCoordinatorJournal => 3,
            LinuxOrdinaryRangeReplacementCase::Publish
            | LinuxOrdinaryRangeReplacementCase::RejectLateCoreBinding => MAX_PRIVATE_PAGES,
        };

        let selected = MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [1; 16],
            txn_id: 3,
            commit_nonce: [2; 16],
            page_count: PAGE_COUNT,
            range_record_count: 1,
            active_feed_count: 0,
            feed_index_limit: 0,
            membership_entry_count: 0,
            membership_id_limit: 0,
            metadata_uncompressed_len: 0,
            metadata_compressed_len: 0,
            retirement_batch_count: 1,
            range_root: 8,
            catalog_name_root: 0,
            catalog_index_root: 0,
            feed_used_root: 0,
            membership_id_root: 0,
            membership_hash_root: 0,
            membership_used_root: 0,
            metadata_root: 0,
            free_bitmap_root: 2,
            retirement_root: 5,
        };
        let database = live_test_database(selected, PAGE_COUNT as usize);
        let mut initial = std::fs::read(&database.main).unwrap();
        let mut free_pages = [0_u32; 57];
        let mut free_len = 0;
        for pgno in 2..u32::try_from(PAGE_COUNT).unwrap() {
            if !matches!(pgno, 2 | 5 | 6 | 8 | 10) {
                free_pages[free_len] = pgno;
                free_len += 1;
            }
        }
        assert_eq!(free_len, free_pages.len());
        *page_mut(&mut initial, selected.free_bitmap_root) =
            free_bitmap_leaf(selected.txn_id, &free_pages);
        encode_leaf::<Ipv4Key>(
            page_mut(&mut initial, selected.range_root),
            selected.txn_id,
            ValueKind::Direct,
            &[RangeRecord {
                from: Ipv4Key(10),
                to: Ipv4Key(20),
                value: 1,
            }],
        )
        .unwrap();
        put_retirement_leaf(
            page_mut(&mut initial, selected.retirement_root),
            selected.txn_id,
            &[batch(2, 6, 1)],
        );
        put_blob_leaf(page_mut(&mut initial, 6), selected.txn_id, &[10]);
        std::fs::write(&database.main, &initial).unwrap();

        // Logical preparation happens before the writer takes its operation
        // barrier. The finalizer below only assigns real pages and publishes
        // this already-normalized replacement.
        let mut normal_range = LinuxLiveWriterNormalRangeWorkspace::<Ipv4Key>::new(
            LinuxLiveWriterNormalRangeWorkspaceCapacity {
                normalizer_pages: 1,
                staged_range_pages: 1,
                max_assignments: 1,
                max_work: 10_000,
                max_mutations: 10_000,
            },
        )
        .unwrap();
        normal_range
            .prepare(selected, |engine| {
                engine.assign(Ipv4Key(30), Ipv4Key(40), 7)
            })
            .unwrap();
        let (prepared_selected, staged, staging) = normal_range.reopen_prepared_staging().unwrap();
        assert_eq!(prepared_selected, selected);

        let mut record_bindings = [BitmapCowArenaBinding::empty(); MAX_PRIVATE_PAGES];
        let mut record_replacements = [0_u32; RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY];
        let mut record_index_nodes =
            [BitmapCowIndexNode::empty(); RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY * 3];
        let record_returned = [const { Cell::new(false) }; MAX_PRIVATE_PAGES];
        let mut record_cleanup_nodes =
            [PrivatePageSelectiveOverlayNode::empty(); MAX_PRIVATE_PAGES * 4];
        let mut record_cleanup_path =
            [PrivatePageSelectivePathEntry::empty(); MAX_PRIVATE_PAGES * 4];
        let mut record_cleanup_targets = [usize::MAX; MAX_PRIVATE_PAGES];
        let workspace_records = [FixedPointWorkspaceRecordSlot::new(
            SealedFreeBitmapCoordinatorScratch {
                arena_bindings: &mut record_bindings,
                replacements: &mut record_replacements,
                index_nodes: &mut record_index_nodes,
                returned: &record_returned,
                cleanup_nodes: &mut record_cleanup_nodes,
                cleanup_path: &mut record_cleanup_path,
                cleanup_targets: &mut record_cleanup_targets,
            },
        )];
        let workspace_entries = [const { Cell::new(None) }; MAX_PRIVATE_PAGES];
        let workspace_source_map = [const { Cell::new(usize::MAX) }; MAX_PRIVATE_PAGES];
        let workspace_record_map = [const { Cell::new(usize::MAX) }; MAX_PRIVATE_PAGES];
        let source_journal =
            [const { Cell::new(FixedPointSourceJournalWrite::EMPTY) }; MAX_PRIVATE_PAGES];
        let map_journal =
            [const { Cell::new(FixedPointMapJournalWrite::EMPTY) }; MAX_PRIVATE_PAGES * 2];
        let tombstone_journal =
            [const { Cell::new(FixedPointTombstoneJournalWrite::EMPTY) }; MAX_PRIVATE_PAGES];
        let journals =
            FixedPointCoordinatorJournals::new(&source_journal, &map_journal, &tombstone_journal);
        let mut ordered_prior_locations = [DraftPrivatePageLocation::EMPTY; MAX_PRIVATE_PAGES];
        let mut pool_returns = [PrivatePageCoordinatorPriorReturn::empty(); MAX_PRIVATE_PAGES];
        // The workspace reserves its bounded maximum before the lock, while
        // the coordinator binds only the exact terminal prefix after the
        // finalizer knows the produced page count.
        let mut new_locations = [DraftPrivatePageLocation::EMPTY; MAX_PRIVATE_PAGES];
        let mut replay_slots =
            [const { PrivatePageSparseReplaySlot::empty() }; MAX_PRIVATE_PAGES * 4];
        let mut replay_index = [const { PrivatePageSparseReplayIndex::empty() }; MAX_PRIVATE_PAGES];
        let mut workspace = FixedPointCoordinatorWorkspace::new(
            &workspace_records,
            &workspace_entries,
            &workspace_source_map,
            &workspace_record_map,
            journals,
            &mut ordered_prior_locations,
            &mut pool_returns,
            &mut new_locations[..coordinator_new_location_capacity],
            &mut replay_slots,
            &mut replay_index,
            MAX_PRIVATE_PAGES,
        )
        .unwrap();
        let workspace_bytes = workspace.retained_bytes();
        let mut live_slots = [const { PrivatePagePoolSlot::empty() }; MAX_PRIVATE_PAGES];
        let mut core = PrivateWriterTransactionCore::<(), (), AggregateCleanupError>::new(
            selected,
            PrivateWriterResourceBudget::new(
                workspace_bytes,
                MAX_PRIVATE_PAGES as u64,
                MAX_PRIVATE_PAGES as u64,
                2,
            ),
            &mut live_slots,
            &mut [],
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        core.reserve_fixed_point_workspace(&handle, &workspace)
            .unwrap();
        let predecessor = core.fixed_point(&handle).unwrap().predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut preparation_scratch = [];
        let reserved = core
            .fixed_point(&handle)
            .unwrap()
            .prepare_reserved_work(
                &predecessor,
                core.draft(&handle).unwrap(),
                1,
                MAX_PRIVATE_PAGES,
                &mut work_slot,
                &mut scope_slot,
                &mut preparation_scratch,
            )
            .unwrap();

        // Every mutable buffer used after the barrier starts is fixed before
        // it starts. The cap is deliberately conservative for this proof.
        let mut planner_storage = RangeRootLiveBitmapStorage::new();
        let mut shadow_slots = [const { PrivatePagePoolSlot::empty() }; MAX_PRIVATE_PAGES];
        let mut assignments = [RangeTreePhysicalAssignment::empty(); 1];
        let mut payload_slots = [RangeTreePayloadReservationSlot::empty(); 1];
        let mut range_input_pages = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        let mut seed_pages = [PageNumberIndexPage::empty(); 4];
        let mut first_pages = [PageNumberIndexPage::empty(); 4];
        let mut second_pages = [PageNumberIndexPage::empty(); 4];
        let mut initial_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut initial_replacements = [EMPTY_REPLACEMENT; MAX_PRIVATE_PAGES];
        let mut initial_releases = [0_u32; MAX_PRIVATE_PAGES];
        let mut initial_roles = [PageRoleIndexSlot::new(); MAX_PRIVATE_PAGES * 2];
        let mut preview_bitmap_replacements = [0_u32; RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY];
        let mut preview_blob_pages = [0_u32; MAX_PRIVATE_PAGES];
        let mut preview_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut preview_replacements = [EMPTY_REPLACEMENT; MAX_PRIVATE_PAGES];
        let mut preview_releases = [0_u32; MAX_PRIVATE_PAGES];
        let mut preview_roles = [PageRoleIndexSlot::new(); MAX_PRIVATE_PAGES * 2];
        let mut terminal_blob_pages = [0_u32; MAX_PRIVATE_PAGES];
        let mut terminal_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut terminal_replacements = [EMPTY_REPLACEMENT; MAX_PRIVATE_PAGES];
        let mut terminal_releases = [0_u32; MAX_PRIVATE_PAGES];
        let mut terminal_roles = [PageRoleIndexSlot::new(); MAX_PRIVATE_PAGES * 2];
        let mut final_release_pages = [0_u32; MAX_PRIVATE_PAGES];
        let mut final_insert_pages =
            [const { FreeBitmapInsertPage::empty() }; MAX_PRIVATE_PAGES * 4 + 4];
        let mut final_cached_pages =
            [const { FreeBitmapFinalizationCachedPage::empty() }; MAX_PRIVATE_PAGES * 4];
        let mut final_index_stack = [usize::MAX; RANGE_ROOT_LIVE_BITMAP_REPLACEMENT_CAPACITY * 3];
        let mut final_cleanup_nodes =
            [PrivatePageSelectiveOverlayNode::empty(); MAX_PRIVATE_PAGES * 4];
        let mut final_cleanup_path =
            [PrivatePageSelectivePathEntry::empty(); MAX_PRIVATE_PAGES * 4];
        let mut final_cleanup_targets = [usize::MAX; MAX_PRIVATE_PAGES];
        let mut bitmap_terminal_pages =
            [const { PrivatePageCoordinatorTerminalPage::empty() }; MAX_PRIVATE_PAGES];
        let mut range_terminal_pages =
            [const { PrivatePageCoordinatorTerminalPage::empty() }; MAX_PRIVATE_PAGES];
        let mut retirement_terminal_pages =
            [const { PrivatePageCoordinatorTerminalPage::empty() }; MAX_PRIVATE_PAGES];
        let mut combined_terminal_pages =
            [const { PrivatePageCoordinatorTerminalPage::empty() }; MAX_PRIVATE_PAGES];
        let expected_terminal_pages = RefCell::new(
            [const { PrivatePageCoordinatorTerminalPage::empty() }; MAX_PRIVATE_PAGES],
        );
        let expected_terminal_len = Cell::new(0_usize);
        let final_bitmap_root = Cell::new(0_u32);
        let final_range_root = Cell::new(0_u32);
        let final_range_records = Cell::new(0_u64);
        let final_retirement_root = Cell::new(0_u32);
        let final_retirement_batches = Cell::new(0_u64);
        let finalizer_ran = Cell::new(false);

        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let (parent, main_component) = RetainedDirectory::open_parent(&database.main).unwrap();
        let sidecar_component = parent.sidecar_component(&main_component).unwrap();
        let mut contender = parent.open_regular(&sidecar_component, true).unwrap();
        let mut reserved = Some(reserved);
        let (publication, allocations) = count_thread_allocations(|| {
            writer.finalize_and_publish_fixed_point_private_output(
                &mut core,
                &handle,
                &mut workspace,
                |context, core, handle, workspace| {
                    finalizer_ran.set(true);
                    let (source_selected, pages, reclaim_fence) = context.into_parts();
                    assert_eq!(source_selected.meta, selected);
                    assert!(matches!(
                        contender.acquire_lock(LockMode::Exclusive, true),
                        Err(LinuxOsError::LockBusy)
                    ));

                    let plan = FreeBitmapReservationPlanner::new(
                        &pages,
                        selected.txn_id,
                        selected.page_count,
                        selected.free_bitmap_root,
                        3,
                        planner_storage.buffers(),
                    )
                    .unwrap()
                    .plan_under_reclamation(reclaim_fence.into_no_reclamation())
                    .unwrap();
                    let required = plan.required_private_pages();
                    assert!(required <= MAX_PRIVATE_PAGES);
                    let shadow_pool = PrivatePagePool::new_vacant(
                        &mut shadow_slots,
                        selected.page_count,
                        selected.page_count,
                        selected.txn_id + 1,
                    )
                    .unwrap();
                    let shadow_scope = shadow_pool.reserve_scope(required).unwrap();
                    let mut bound = plan.bind(&shadow_pool, &shadow_scope).unwrap();
                    let materialized = bound
                        .stage_range_payload(
                            &shadow_scope,
                            &staging,
                            staged,
                            &mut RangeTreePayloadScratch {
                                assignments: &mut assignments,
                                slots: &mut payload_slots,
                                terminal_pages: &mut range_input_pages,
                            },
                        )
                        .unwrap();
                    let requirements = bound.finalization_scratch_requirements().unwrap();
                    assert!(requirements.release_pages <= final_release_pages.len());
                    assert!(requirements.insert_pages <= final_insert_pages.len());
                    assert!(requirements.cached_pages <= final_cached_pages.len());
                    assert!(requirements.index_stack <= final_index_stack.len());
                    assert!(requirements.cleanup_nodes <= final_cleanup_nodes.len());
                    assert!(requirements.cleanup_path <= final_cleanup_path.len());
                    assert!(requirements.cleanup_targets <= final_cleanup_targets.len());

                    let mut seed_workspace = PageNumberIndexWorkspace::new(&mut seed_pages);
                    let mut first_workspace = PageNumberIndexWorkspace::new(&mut first_pages);
                    let mut second_workspace = PageNumberIndexWorkspace::new(&mut second_pages);
                    let mut seed = PageNumberIndex::new(&mut seed_workspace).unwrap();
                    let mut first = PageNumberIndex::new(&mut first_workspace).unwrap();
                    let mut second = PageNumberIndex::new(&mut second_workspace).unwrap();
                    let mut ownership_scratch = RangeTreeOwnershipScratch::new();
                    let proof = prepare_range_root_replacement_proof::<Ipv4Key, _>(
                        &mut bound,
                        &shadow_pool,
                        &shadow_scope,
                        selected,
                        materialized,
                        &range_input_pages,
                        &mut seed,
                        &mut first,
                        &mut second,
                        &mut ownership_scratch,
                        4,
                        3,
                        RangeRootReplacementProofScratch {
                            initial_upsert_path: &mut initial_path,
                            initial_replacements: &mut initial_replacements,
                            initial_releases: &mut initial_releases,
                            initial_roles: &mut initial_roles,
                            preview_bitmap_replacements: &mut preview_bitmap_replacements,
                            preview_blob_pages: &mut preview_blob_pages,
                            preview_upsert_path: &mut preview_path,
                            preview_replacements: &mut preview_replacements,
                            preview_releases: &mut preview_releases,
                            preview_roles: &mut preview_roles,
                            final_release_pages: &mut final_release_pages
                                [..requirements.release_pages],
                            final_insert_pages: &mut final_insert_pages
                                [..requirements.insert_pages],
                            final_cached_pages: &mut final_cached_pages
                                [..requirements.cached_pages],
                            final_index_stack: &mut final_index_stack[..requirements.index_stack],
                            final_cleanup_nodes: &mut final_cleanup_nodes
                                [..requirements.cleanup_nodes],
                            final_cleanup_path: &mut final_cleanup_path
                                [..requirements.cleanup_path],
                            final_cleanup_targets: &mut final_cleanup_targets
                                [..requirements.cleanup_targets],
                        },
                    )
                    .unwrap();
                    let produced = finalize_range_root_replacement_terminal(
                        bound,
                        &shadow_pool,
                        &shadow_scope,
                        proof,
                        RangeRootReplacementTerminalScratch {
                            retirement: RangeRootRetirementStageScratch {
                                blob_pages: &mut terminal_blob_pages,
                                upsert_path: &mut terminal_path,
                                replacements: &mut terminal_replacements,
                                releases: &mut terminal_releases,
                                roles: &mut terminal_roles,
                            },
                            bitmap_finalization: FreeBitmapFinalizationScratch {
                                release_pages: &mut final_release_pages
                                    [..requirements.release_pages],
                                insert_pages: &mut final_insert_pages[..requirements.insert_pages],
                                cached_pages: &mut final_cached_pages[..requirements.cached_pages],
                                index_stack: &mut final_index_stack[..requirements.index_stack],
                                cleanup_nodes: &mut final_cleanup_nodes
                                    [..requirements.cleanup_nodes],
                                cleanup_path: &mut final_cleanup_path[..requirements.cleanup_path],
                                cleanup_targets: &mut final_cleanup_targets
                                    [..requirements.cleanup_targets],
                            },
                            bitmap_pages: &mut bitmap_terminal_pages,
                            range_pages: &mut range_terminal_pages,
                            retirement_pages: &mut retirement_terminal_pages,
                            combined_pages: &mut combined_terminal_pages,
                        },
                    )
                    .unwrap();
                    let bitmap_root = produced.bitmap().root();
                    let pending_page_count = produced.bitmap().pending_page_count();
                    if case == LinuxOrdinaryRangeReplacementCase::RejectLateCoreBinding {
                        let live_pool = core.draft(handle)?;
                        let (reserved_work, produced) = match reserved
                            .take()
                            .expect("one reserved coordinator scope")
                            .with_finalized_produced_terminal_export(
                                live_pool,
                                FixedPointPreparedOutput {
                                    root: bitmap_root,
                                    pending_page_count,
                                },
                                produced,
                                0,
                            ) {
                            Ok(_) => panic!("zero nonce must reject the terminal bind"),
                            Err((reserved_work, produced, FixedPointError::StalePredecessor)) => {
                                (reserved_work, produced)
                            }
                            Err((_reserved_work, _produced, error)) => {
                                panic!("zero nonce returned the wrong error: {error:?}")
                            }
                        };
                        shadow_pool.require_abort();
                        let (_retirement, _range, bitmap, _provenance, pages, _rebind) =
                            produced.into_bind_parts();
                        bitmap.discard_after_abort();
                        pages.fill(PrivatePageCoordinatorTerminalPage::empty());
                        assert!(bitmap_terminal_pages
                            .iter()
                            .all(|page| *page == PrivatePageCoordinatorTerminalPage::empty()));
                        assert!(range_terminal_pages
                            .iter()
                            .all(|page| *page == PrivatePageCoordinatorTerminalPage::empty()));
                        assert!(retirement_terminal_pages
                            .iter()
                            .all(|page| *page == PrivatePageCoordinatorTerminalPage::empty()));
                        assert!(combined_terminal_pages
                            .iter()
                            .all(|page| *page == PrivatePageCoordinatorTerminalPage::empty()));
                        assert!(seed.is_empty_and_clean());
                        assert!(first.is_empty_and_clean());
                        assert!(second.is_empty_and_clean());
                        reserved_work
                            .cancel(live_pool)
                            .map_err(PrivateWriterTransactionError::FixedPoint)?;
                        core.require_abort(handle)?;
                        return Err(PrivateWriterTransactionError::FixedPoint(
                            FixedPointError::StalePredecessor,
                        ));
                    }
                    let range = produced.range_target().unwrap();
                    let retirement = produced.retirement_result();
                    let terminal_pages = produced.pages();
                    assert!(terminal_pages.len() <= MAX_PRIVATE_PAGES);
                    if case == LinuxOrdinaryRangeReplacementCase::Publish {
                        expected_terminal_pages.borrow_mut()[..terminal_pages.len()]
                            .clone_from_slice(terminal_pages);
                        expected_terminal_len.set(terminal_pages.len());
                    }
                    assert!(terminal_pages
                        .iter()
                        .any(|page| page.owner == PrivatePageOwner::Range));
                    assert!(terminal_pages
                        .iter()
                        .any(|page| page.owner == PrivatePageOwner::Retirement));
                    assert!(terminal_pages
                        .iter()
                        .any(|page| page.owner == PrivatePageOwner::Bitmap));
                    final_bitmap_root.set(bitmap_root);
                    final_range_root.set(range.root_pgno);
                    final_range_records.set(range.record_count);
                    final_retirement_root.set(retirement.root);
                    final_retirement_batches.set(retirement.batch_count);

                    let live_pool = core.draft(handle)?;
                    let produced = reserved
                        .take()
                        .expect("one reserved coordinator scope")
                        .with_finalized_produced_terminal_export(
                            live_pool,
                            FixedPointPreparedOutput {
                                root: bitmap_root,
                                pending_page_count,
                            },
                            produced,
                            77,
                        )
                        .map_err(|(_reserved, _produced, error)| {
                            PrivateWriterTransactionError::FixedPoint(error)
                        })?;
                    let coordinator = core.fixed_point(handle)?;
                    let aggregate = match workspace.prepare_aggregate(
                        produced,
                        coordinator,
                        &predecessor,
                        live_pool,
                        &pages,
                        &[],
                    ) {
                        Ok(aggregate) => aggregate,
                        Err((produced, error)) => {
                            if case
                                != LinuxOrdinaryRangeReplacementCase::RejectShortCoordinatorJournal
                            {
                                return Err(PrivateWriterTransactionError::FixedPoint(error));
                            }
                            assert_eq!(
                                error,
                                FixedPointError::SourceScratchTooSmall {
                                    required: 4,
                                    actual: coordinator_new_location_capacity,
                                }
                            );
                            shadow_pool.require_abort();
                            produced
                                .cancel(live_pool)
                                .map_err(PrivateWriterTransactionError::FixedPoint)?;
                            bitmap_terminal_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
                            range_terminal_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
                            retirement_terminal_pages
                                .fill(PrivatePageCoordinatorTerminalPage::empty());
                            combined_terminal_pages
                                .fill(PrivatePageCoordinatorTerminalPage::empty());
                            assert!(bitmap_terminal_pages
                                .iter()
                                .all(|page| *page == PrivatePageCoordinatorTerminalPage::empty()));
                            assert!(range_terminal_pages
                                .iter()
                                .all(|page| *page == PrivatePageCoordinatorTerminalPage::empty()));
                            assert!(retirement_terminal_pages
                                .iter()
                                .all(|page| *page == PrivatePageCoordinatorTerminalPage::empty()));
                            assert!(combined_terminal_pages
                                .iter()
                                .all(|page| *page == PrivatePageCoordinatorTerminalPage::empty()));
                            assert!(seed.is_empty_and_clean());
                            assert!(first.is_empty_and_clean());
                            assert!(second.is_empty_and_clean());
                            core.require_abort(handle)?;
                            return Err(PrivateWriterTransactionError::FixedPoint(error));
                        }
                    };
                    let sealed = core
                        .execute_fixed_point_aggregate(handle, predecessor, aggregate)
                        .map_err(|(_predecessor, _aggregate, error)| {
                            PrivateWriterTransactionError::FixedPoint(error)
                        })?;
                    assert_eq!(sealed.retirement_result(), retirement);
                    let successor =
                        core.complete_fixed_point_aggregate(handle, workspace, sealed)?;
                    assert_eq!(successor.root(), bitmap_root);
                    assert_eq!(successor.pending_page_count(), pending_page_count);
                    let target = core.target().expect("aggregate must set target metadata");
                    assert_eq!(target.free_bitmap_root, bitmap_root);
                    assert_eq!(target.range_root, range.root_pgno);
                    assert_eq!(target.range_record_count, range.record_count);
                    assert_eq!(target.retirement_root, retirement.root);
                    assert_eq!(target.retirement_batch_count, retirement.batch_count);
                    core.finish_fixed_point_input(handle, workspace, successor)
                        .map_err(|(_successor, error)| error)?;
                    assert!(core.fixed_point(handle)?.is_quiescent());
                    Ok(())
                },
            )
        });
        assert_eq!(allocations, 0);
        assert!(finalizer_ran.get());
        match case {
            LinuxOrdinaryRangeReplacementCase::Publish => {
                let target = publication.unwrap().meta;
                assert_eq!(target.free_bitmap_root, final_bitmap_root.get());
                assert_eq!(target.range_root, final_range_root.get());
                assert_eq!(target.range_record_count, final_range_records.get());
                assert_eq!(target.retirement_root, final_retirement_root.get());
                assert_eq!(
                    target.retirement_batch_count,
                    final_retirement_batches.get()
                );
                assert_ne!(target.range_root, selected.range_root);
                assert_eq!(target.range_record_count, 1);
                assert_eq!(target.txn_id, selected.txn_id + 1);
                assert!(target.retirement_batch_count > selected.retirement_batch_count);
                assert!(workspace.is_idle());
                assert_eq!(workspace.new_location_capacity(), MAX_PRIVATE_PAGES);
                assert!(workspace.new_locations_are_clean());
                assert_eq!(core.state(), PrivateWriterTransactionState::Clean);
                assert_eq!(core.selected(), target);
                assert_eq!(core.target(), None);
                writer.close().unwrap();
                contender.acquire_lock(LockMode::Exclusive, true).unwrap();
                contender.release_lock().unwrap();

                let committed = std::fs::read(&database.main).unwrap();
                assert_eq!(
                    crate::bootstrap::open(&committed, OpenMode::Writer)
                        .unwrap()
                        .meta,
                    target
                );
                let expected = expected_terminal_pages.borrow();
                for page in &expected[..expected_terminal_len.get()] {
                    let offset = usize::try_from(page.pgno).unwrap() * PAGE_SIZE;
                    assert_eq!(
                        &committed[offset..offset + PAGE_SIZE],
                        &page.bytes,
                        "the ordinary replacement must publish the exact lock-bound terminal page"
                    );
                }
            }
            LinuxOrdinaryRangeReplacementCase::RejectLateCoreBinding
            | LinuxOrdinaryRangeReplacementCase::RejectShortCoordinatorJournal => {
                match case {
                    LinuxOrdinaryRangeReplacementCase::RejectLateCoreBinding => assert!(matches!(
                        publication,
                        Err(
                            crate::os::linux::live_writer::LinuxLiveWriterCoreCommitError::Core(
                                PrivateWriterTransactionError::FixedPoint(
                                    FixedPointError::StalePredecessor
                                )
                            )
                        )
                    )),
                    LinuxOrdinaryRangeReplacementCase::RejectShortCoordinatorJournal => {
                        assert!(matches!(
                            publication,
                            Err(
                                crate::os::linux::live_writer::LinuxLiveWriterCoreCommitError::Core(
                                    PrivateWriterTransactionError::FixedPoint(
                                        FixedPointError::SourceScratchTooSmall {
                                            required: 4,
                                            actual: 3,
                                        }
                                    )
                                )
                            )
                        ));
                    }
                    LinuxOrdinaryRangeReplacementCase::Publish => unreachable!(),
                }
                assert_eq!(expected_terminal_len.get(), 0);
                assert_eq!(core.state(), PrivateWriterTransactionState::AbortRequired);
                assert_eq!(core.selected(), selected);
                let private_target = core
                    .target()
                    .expect("pending draft retains its base target");
                assert_eq!(private_target.range_root, selected.range_root);
                assert_eq!(
                    private_target.range_record_count,
                    selected.range_record_count
                );
                assert_eq!(private_target.free_bitmap_root, selected.free_bitmap_root);
                assert_eq!(private_target.retirement_root, selected.retirement_root);
                assert_eq!(
                    private_target.retirement_batch_count,
                    selected.retirement_batch_count
                );
                writer.close().unwrap();
                contender.acquire_lock(LockMode::Exclusive, true).unwrap();
                contender.release_lock().unwrap();
                assert_eq!(
                    std::fs::read(&database.main).unwrap(),
                    initial,
                    "a late terminal/core failure must not publish data or metadata"
                );

                core.cancel_fixed_point_workspace(&handle, &mut workspace)
                    .unwrap();
                assert!(workspace.is_idle());
                assert_eq!(
                    workspace.new_location_capacity(),
                    coordinator_new_location_capacity
                );
                assert!(workspace.new_locations_are_clean());
                let abort_visits = core.abort().unwrap();
                assert!(abort_visits > 0);
                assert_eq!(core.state(), PrivateWriterTransactionState::Clean);
                assert_eq!(core.target(), None);
                let fresh = core.begin([4; 16]).unwrap();
                assert_eq!(core.state(), PrivateWriterTransactionState::Pending);
                assert!(!core.draft(&fresh).unwrap().requires_abort());
                assert!(core.abort().unwrap() > 0);
            }
        }
        match case {
            LinuxOrdinaryRangeReplacementCase::Publish => normal_range.finish_after_publication(),
            LinuxOrdinaryRangeReplacementCase::RejectLateCoreBinding
            | LinuxOrdinaryRangeReplacementCase::RejectShortCoordinatorJournal => {
                normal_range.discard_after_abort();
            }
        }
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    #[test]
    fn linux_finalizer_publishes_proof_bound_ordinary_range_replacement() {
        run_linux_ordinary_range_replacement_case(LinuxOrdinaryRangeReplacementCase::Publish);
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    #[test]
    fn linux_finalizer_late_ordinary_range_failure_requires_whole_draft_abort() {
        run_linux_ordinary_range_replacement_case(
            LinuxOrdinaryRangeReplacementCase::RejectLateCoreBinding,
        );
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    #[test]
    fn linux_finalizer_rejects_undersized_ordinary_range_coordinator_journal() {
        run_linux_ordinary_range_replacement_case(
            LinuxOrdinaryRangeReplacementCase::RejectShortCoordinatorJournal,
        );
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    fn run_linux_live_reclaim_case(
        case: LinuxFinalizerReclamationCase,
        cancel_at_probe: Option<usize>,
    ) {
        let (selected_txn, retirement_root, retirement_batch_count) = match case {
            LinuxFinalizerReclamationCase::NoChange => (1, 0, 0),
            LinuxFinalizerReclamationCase::SelectedBatch => (2, 12, 1),
        };
        let selected = MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [1; 16],
            txn_id: selected_txn,
            commit_nonce: [2; 16],
            page_count: 100,
            range_record_count: 0,
            active_feed_count: 0,
            feed_index_limit: 0,
            membership_entry_count: 0,
            membership_id_limit: 0,
            metadata_uncompressed_len: 0,
            metadata_compressed_len: 0,
            retirement_batch_count,
            range_root: 0,
            catalog_name_root: 0,
            catalog_index_root: 0,
            feed_used_root: 0,
            membership_id_root: 0,
            membership_hash_root: 0,
            membership_used_root: 0,
            metadata_root: 0,
            free_bitmap_root: 11,
            retirement_root,
        };
        let database = live_test_database(selected, selected.page_count as usize);
        let mut initial_bytes = std::fs::read(&database.main).unwrap();
        let free_bits = match case {
            LinuxFinalizerReclamationCase::NoChange => &[20, 22, 24, 26][..],
            LinuxFinalizerReclamationCase::SelectedBatch => &[20, 24][..],
        };
        let free_leaf = free_bitmap_leaf(selected.txn_id, free_bits);
        let leaf_offset = usize::try_from(selected.free_bitmap_root).unwrap() * PAGE_SIZE;
        initial_bytes[leaf_offset..leaf_offset + PAGE_SIZE].copy_from_slice(&free_leaf);
        if case == LinuxFinalizerReclamationCase::SelectedBatch {
            put_retirement_leaf(
                page_mut(&mut initial_bytes, selected.retirement_root),
                selected.txn_id,
                &[batch(selected.txn_id, 13, 2)],
            );
            put_blob_leaf(page_mut(&mut initial_bytes, 13), selected.txn_id, &[21, 23]);
        }
        std::fs::write(&database.main, &initial_bytes).unwrap();
        let mut protected_reader = match case {
            LinuxFinalizerReclamationCase::NoChange => None,
            LinuxFinalizerReclamationCase::SelectedBatch => {
                Some(LinuxLiveReader::<Ipv4Key>::open(&database.main).unwrap())
            }
        };

        let mut workspace =
            LinuxLiveWriterReclaimWorkspace::new(LinuxLiveWriterReclaimWorkspaceCapacity {
                max_live_pages: 3,
                max_shadow_pages: 4,
                max_reclamation_batches: 1,
                max_reclaimed_pages: 2,
                max_bitmap_payload_pages: 2,
                scratch_slots: 4,
            })
            .unwrap();

        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let cancellation_checks = Cell::new(0usize);
        let (result, allocations) = count_thread_allocations(|| {
            writer.reclaim_with_workspace(
                &mut workspace,
                LinuxLiveWriterReclaimLimits {
                    max_batches: 1,
                    max_pages: 2,
                    bitmap_payload_pages: 2,
                },
                || {
                    let check = cancellation_checks
                        .get()
                        .checked_add(1)
                        .expect("test cancellation probe count fits usize");
                    cancellation_checks.set(check);
                    cancel_at_probe.is_some_and(|target| check >= target)
                },
            )
        });
        assert_eq!(
            allocations, 0,
            "Reclaim must use only its SDK-owned workspace"
        );

        match (case, cancel_at_probe, result) {
            (
                _,
                Some(1),
                Err(LinuxLiveWriterReclaimError::Barrier(LinuxLiveWriterBarrierCause::Cancelled)),
            ) => {
                assert_eq!(
                    std::fs::read(&database.main).unwrap(),
                    initial_bytes,
                    "cancellation before the operation barrier must leave no draft"
                );
            }
            (
                LinuxFinalizerReclamationCase::SelectedBatch,
                Some(14),
                Err(LinuxLiveWriterReclaimError::Failed {
                    cause:
                        LinuxLiveWriterReclaimFailure::Cancelled {
                            cleanup_complete: true,
                        },
                    release: None,
                }),
            ) => {
                assert_eq!(
                    std::fs::read(&database.main).unwrap(),
                    initial_bytes,
                    "cancellation after the private draft begins must abort it completely"
                );
            }
            (
                LinuxFinalizerReclamationCase::NoChange,
                None,
                Ok(LinuxLiveWriterReclaimOutcome::NoChange),
            ) => {
                assert_eq!(
                    std::fs::read(&database.main).unwrap(),
                    initial_bytes,
                    "NoChange must not start a draft or alter the committed main file"
                );
            }
            (
                LinuxFinalizerReclamationCase::SelectedBatch,
                None,
                Ok(LinuxLiveWriterReclaimOutcome::Reclaimed(target)),
            ) => {
                assert_eq!(target.meta.txn_id, selected.txn_id + 1);
                assert_ne!(target.meta.commit_nonce, selected.commit_nonce);
                assert_eq!(target.meta.page_count, selected.page_count);
                assert_eq!(target.meta.free_bitmap_root, 20);
                assert_eq!(target.meta.retirement_root, 23);
                assert_eq!(target.meta.retirement_batch_count, 1);
                let committed = std::fs::read(&database.main).unwrap();
                assert_ne!(committed, initial_bytes);
                assert_eq!(
                    crate::bootstrap::open(&committed, OpenMode::Writer)
                        .unwrap()
                        .meta,
                    target.meta
                );
            }
            (expected, cancellation, actual) => {
                panic!(
                    "unexpected Reclaim result for {expected:?}, cancellation={cancellation:?}: {actual:?}"
                )
            }
        }
        if let Some(expected) = cancel_at_probe {
            assert_eq!(
                cancellation_checks.get(),
                expected,
                "this fixture's probe ordinal must continue to target the intended cancellation checkpoint"
            );
        }
        if cancel_at_probe == Some(14) {
            let (retry, retry_allocations) = count_thread_allocations(|| {
                writer.reclaim_with_workspace(
                    &mut workspace,
                    LinuxLiveWriterReclaimLimits {
                        max_batches: 1,
                        max_pages: 2,
                        bitmap_payload_pages: 2,
                    },
                    || false,
                )
            });
            assert_eq!(
                retry_allocations, 0,
                "a reset and retry must stay within the retained workspace"
            );
            assert!(
                matches!(retry, Ok(LinuxLiveWriterReclaimOutcome::Reclaimed(_))),
                "whole-draft abort must leave the opaque workspace reusable: {retry:?}"
            );
        }

        writer.close().unwrap();
        if let Some(reader) = protected_reader.as_mut() {
            reader.close().unwrap();
        }
        let (parent, main_component) = RetainedDirectory::open_parent(&database.main).unwrap();
        let sidecar_component = parent.sidecar_component(&main_component).unwrap();
        let mut contender = parent.open_regular(&sidecar_component, true).unwrap();
        contender.acquire_lock(LockMode::Exclusive, true).unwrap();
        contender.release_lock().unwrap();
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    #[test]
    fn linux_live_reclaim_no_change_leaves_the_main_file_untouched() {
        run_linux_live_reclaim_case(LinuxFinalizerReclamationCase::NoChange, None);
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    #[test]
    fn linux_live_reclaim_selected_batch_publishes_one_new_generation() {
        run_linux_live_reclaim_case(LinuxFinalizerReclamationCase::SelectedBatch, None);
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    #[test]
    fn linux_live_reclaim_cancellation_before_lock_leaves_no_draft() {
        run_linux_live_reclaim_case(LinuxFinalizerReclamationCase::SelectedBatch, Some(1));
    }

    #[cfg(all(feature = "os", target_os = "linux"))]
    #[test]
    fn linux_live_reclaim_cancellation_after_draft_start_aborts_it() {
        // With this fixed one-reader sidecar, the first 13 probes cover lock
        // acquisition, the three reader-table passes, and selected terminal
        // finalization. Probe 14 is immediately after `core.begin`.
        run_linux_live_reclaim_case(LinuxFinalizerReclamationCase::SelectedBatch, Some(14));
    }

    #[test]
    fn scoped_plan_rejects_foreign_drift_without_rolling_it_back() {
        let mut storage = vec![PrivatePageSlot::empty(); 6];
        let pool = PrivatePagePool::new_vacant(&mut storage, 100, 100, 2).unwrap();
        let target = pool.reserve_scope(3).unwrap();
        let foreign = pool.reserve_scope(3).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        for pgno in [20, 22, 24] {
            pool.bind_page(
                &checkpoint,
                &target,
                pgno,
                PrivatePageAuthorization::CommittedFree,
            )
            .unwrap();
        }
        for pgno in [3, 5, 7] {
            pool.bind_page(
                &checkpoint,
                &foreign,
                pgno,
                PrivatePageAuthorization::CommittedFree,
            )
            .unwrap();
        }
        pool.commit_checkpoint(checkpoint).unwrap();

        let mut arena = PrivatePageArena::from_scoped_pool(&pool, &target, 2).unwrap();
        let mut blob_pages = [0u32; 2];
        let mut blob = RetirementBlobBuilder::build(
            &[50],
            &mut arena,
            &mut BlobBuildScratch::new(&mut blob_pages),
        )
        .unwrap();
        let source = SlicePageSource::new(&[], 100);
        let mut path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut replacement_entries = [EMPTY_REPLACEMENT; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_entries);
        let mut release_entries = [0u32; 2];
        let mut releases = PrivateReleaseBuffer::new(&mut release_entries);
        let mut role_entries = [PageRoleIndexSlot::new(); 12];
        let mut roles = PageRoleIndex::new(&mut role_entries);
        let plan = RetirementTreeEditor::plan_upsert_newest(
            &source,
            RetirementTreeState {
                selected_txn: 1,
                page_count: 100,
                root: 0,
                batch_count: 0,
            },
            &mut blob,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();

        let foreign_checkpoint = pool.begin_checkpoint().unwrap();
        let foreign_page = pool
            .claim_lowest_in_scope(
                &foreign_checkpoint,
                &foreign,
                PrivatePageOwner::Bitmap,
                2,
                0,
            )
            .unwrap();
        assert_eq!(foreign_page.page_number(), 3);
        pool.commit_checkpoint_in_scope(foreign_checkpoint, &foreign)
            .unwrap();

        assert_eq!(
            plan.apply().unwrap_err(),
            RetirementWriteError::StaleEditPlan(RetirementEditBinding::Pool)
        );
        drop(blob);
        let foreign_slot = pool.find_in_scope(&foreign, 3).unwrap().unwrap();
        assert!(matches!(
            pool.scoped_slot_info(&foreign, foreign_slot)
                .unwrap()
                .unwrap()
                .state,
            PrivatePagePoolState::InUse {
                owner: PrivatePageOwner::Bitmap,
                ..
            }
        ));
        assert_eq!(pool.scoped_in_use(&target).unwrap(), 0);
    }

    #[test]
    fn scoped_final_callbacks_reject_every_mutable_binding_and_remain_retriable() {
        #[derive(Clone, Copy)]
        enum Drift {
            Arena,
            Pool,
            ReplacementLedger,
            ReleaseLedger,
            Roles,
            BlobToken,
            UpsertScratch,
        }

        for mutate_on in [3usize, 4] {
            for drift in [
                Drift::Arena,
                Drift::Pool,
                Drift::ReplacementLedger,
                Drift::ReleaseLedger,
                Drift::Roles,
                Drift::BlobToken,
                Drift::UpsertScratch,
            ] {
                let mut storage = vec![PrivatePageSlot::empty(); 4];
                let pool = PrivatePagePool::new_vacant(&mut storage, 100, 100, 2).unwrap();
                let target = pool.reserve_scope(3).unwrap();
                let foreign = pool.reserve_scope(1).unwrap();
                let checkpoint = pool.begin_checkpoint().unwrap();
                for pgno in [20, 22, 24] {
                    pool.bind_page(
                        &checkpoint,
                        &target,
                        pgno,
                        PrivatePageAuthorization::CommittedFree,
                    )
                    .unwrap();
                }
                pool.bind_page(
                    &checkpoint,
                    &foreign,
                    3,
                    PrivatePageAuthorization::CommittedFree,
                )
                .unwrap();
                pool.commit_checkpoint(checkpoint).unwrap();

                let mut arena = PrivatePageArena::from_scoped_pool(&pool, &target, 2).unwrap();
                let mut blob_pages = [0u32; 1];
                let mut token = RetirementBlobBuilder::build(
                    &[50],
                    &mut arena,
                    &mut BlobBuildScratch::new(&mut blob_pages),
                )
                .unwrap();
                let generation = token.arena.generation.get();
                let mut path = [RetirementPathFrame::new(); 2];
                let mut replacement_entries = [EMPTY_REPLACEMENT; 2];
                let mut replacements = CommittedReplacementLedger::new(&mut replacement_entries);
                let mut release_entries = [0u32; 2];
                let mut releases = PrivateReleaseBuffer::new(&mut release_entries);
                let mut role_entries = [PageRoleIndexSlot::new(); 16];
                let mut roles = PageRoleIndex::new(&mut role_entries);

                let action: Box<dyn Fn() + '_> = match drift {
                    Drift::Arena => {
                        let generation = &token.arena.generation as *const Cell<u64>;
                        Box::new(move || {
                            // SAFETY: this deliberately simulates a reentrant callback
                            // violating the planner's exclusive arena borrow.
                            let generation = unsafe { &*generation };
                            generation.set(generation.get() + 1);
                        })
                    }
                    Drift::Pool => Box::new(|| {
                        let checkpoint = pool.begin_checkpoint().unwrap();
                        pool.claim_lowest_in_scope(
                            &checkpoint,
                            &foreign,
                            PrivatePageOwner::Bitmap,
                            2,
                            0,
                        )
                        .unwrap();
                        pool.commit_checkpoint_in_scope(checkpoint, &foreign)
                            .unwrap();
                    }),
                    Drift::ReplacementLedger => {
                        let len = &mut replacements.len as *mut usize;
                        Box::new(move || {
                            // SAFETY: intentional reentrant mutation for the guard test.
                            unsafe { *len = 1 };
                        })
                    }
                    Drift::ReleaseLedger => {
                        let len = &mut releases.len as *mut usize;
                        Box::new(move || {
                            // SAFETY: intentional reentrant mutation for the guard test.
                            unsafe { *len = 1 };
                        })
                    }
                    Drift::Roles => {
                        let epoch = &mut roles.reference_epoch as *mut u8;
                        Box::new(move || {
                            // SAFETY: intentional reentrant mutation for the guard test.
                            unsafe { *epoch = (*epoch).wrapping_add(1) };
                        })
                    }
                    Drift::BlobToken => {
                        let root = &mut token.root as *mut u32;
                        Box::new(move || {
                            // SAFETY: intentional reentrant mutation for the guard test.
                            unsafe { *root = 99 };
                        })
                    }
                    Drift::UpsertScratch => {
                        let byte = path[1].page.as_mut_ptr();
                        Box::new(move || {
                            // SAFETY: the pointer remains within the live path frame.
                            unsafe { *byte = 0x5a };
                        })
                    }
                };
                let source = MutateOnCheck {
                    inner: SlicePageSource::new(&[], 100),
                    checks: Cell::new(0),
                    mutate_on,
                    action: &*action,
                };
                let error = match RetirementTreeEditor::plan_upsert_newest(
                    &source,
                    state(1, 100, 0, 0),
                    &mut token,
                    &mut path,
                    &mut replacements,
                    &mut releases,
                    &mut roles,
                ) {
                    Ok(plan) => plan.apply().unwrap_err(),
                    Err(error) => error,
                };
                let expected = match drift {
                    Drift::Arena => RetirementEditBinding::Arena,
                    Drift::Pool => RetirementEditBinding::Pool,
                    Drift::ReplacementLedger => RetirementEditBinding::ReplacementLedger,
                    Drift::ReleaseLedger => RetirementEditBinding::ReleaseLedger,
                    Drift::Roles => RetirementEditBinding::Roles,
                    Drift::BlobToken => RetirementEditBinding::BlobToken,
                    Drift::UpsertScratch => RetirementEditBinding::UpsertScratch,
                };
                assert_eq!(error, RetirementWriteError::StaleEditPlan(expected));
                assert_eq!(source.checks.get(), mutate_on);
                drop(action);
                assert_eq!(token.arena.generation.get(), generation);

                let result = RetirementTreeEditor::upsert_newest(
                    &SlicePageSource::new(&[], 100),
                    state(1, 100, 0, 0),
                    token,
                    &mut path,
                    &mut replacements,
                    &mut releases,
                    &mut roles,
                )
                .unwrap();
                assert_eq!(result.root, 22);
                assert_eq!(pool.scoped_in_use(&target).unwrap(), 2);
                let foreign_slot = pool.find_in_scope(&foreign, 3).unwrap().unwrap();
                let foreign_state = pool
                    .scoped_slot_info(&foreign, foreign_slot)
                    .unwrap()
                    .unwrap()
                    .state;
                if matches!(drift, Drift::Pool) {
                    assert!(matches!(
                        foreign_state,
                        PrivatePagePoolState::InUse {
                            owner: PrivatePageOwner::Bitmap,
                            ..
                        }
                    ));
                } else {
                    assert_eq!(foreign_state, PrivatePagePoolState::Available);
                }
            }
        }
    }

    #[test]
    fn detected_callback_drift_takes_precedence_over_the_callback_error() {
        for mutate_on in [3usize, 4] {
            let mut storage = vec![PrivatePageSlot::empty(); 3];
            let pool = PrivatePagePool::new_vacant(&mut storage, 100, 100, 2).unwrap();
            let scope = pool.reserve_scope(3).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            for pgno in [20, 22, 24] {
                pool.bind_page(
                    &checkpoint,
                    &scope,
                    pgno,
                    PrivatePageAuthorization::CommittedFree,
                )
                .unwrap();
            }
            pool.commit_checkpoint(checkpoint).unwrap();

            let mut arena = PrivatePageArena::from_scoped_pool(&pool, &scope, 2).unwrap();
            let mut blob_pages = [0u32; 1];
            let mut token = RetirementBlobBuilder::build(
                &[50],
                &mut arena,
                &mut BlobBuildScratch::new(&mut blob_pages),
            )
            .unwrap();
            let generation_before = token.arena.generation.get();
            let generation = &token.arena.generation as *const Cell<u64>;
            let action = move || {
                // SAFETY: deliberate reentrant mutation for precedence testing.
                let generation = unsafe { &*generation };
                generation.set(generation.get() + 1);
            };
            let source = MutateAndFailOnCheck {
                inner: SlicePageSource::new(&[], 100),
                checks: Cell::new(0),
                mutate_on,
                action: &action,
            };
            let mut path = [RetirementPathFrame::new(); 2];
            let mut replacement_entries = [EMPTY_REPLACEMENT; 2];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_entries);
            let mut release_entries = [0u32; 2];
            let mut releases = PrivateReleaseBuffer::new(&mut release_entries);
            let mut role_entries = [PageRoleIndexSlot::new(); 16];
            let mut roles = PageRoleIndex::new(&mut role_entries);

            let error = match RetirementTreeEditor::plan_upsert_newest(
                &source,
                state(1, 100, 0, 0),
                &mut token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            ) {
                Ok(plan) => plan.apply().unwrap_err(),
                Err(error) => error,
            };
            assert_eq!(
                error,
                RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena)
            );
            assert_eq!(source.checks.get(), mutate_on);
            assert_eq!(token.arena.generation.get(), generation_before);
        }
    }

    #[test]
    fn safe_private_page_mutate_restore_callback_is_pool_drift_and_retryable() {
        for fail_on_mutate in [false, true] {
            let mut storage = vec![PrivatePageSlot::empty(); 3];
            let pool = PrivatePagePool::new_vacant(&mut storage, 100, 100, 2).unwrap();
            let scope = pool.reserve_scope(3).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            for pgno in [20, 22, 24] {
                pool.bind_page(
                    &checkpoint,
                    &scope,
                    pgno,
                    PrivatePageAuthorization::CommittedFree,
                )
                .unwrap();
            }
            pool.commit_checkpoint(checkpoint).unwrap();

            let mut arena = PrivatePageArena::from_scoped_pool(&pool, &scope, 2).unwrap();
            let mut blob_pages = [0u32; 1];
            let mut token = RetirementBlobBuilder::build(
                &[50],
                &mut arena,
                &mut BlobBuildScratch::new(&mut blob_pages),
            )
            .unwrap();
            let authority = pool
                .authority_in_scope(
                    &scope,
                    token.root,
                    PrivatePageOwner::Retirement,
                    token.generation,
                )
                .unwrap();
            let slot = pool.find_in_scope(&scope, token.root).unwrap().unwrap();
            let original = pool.test_bytes(slot).unwrap();
            let binding_epoch = pool
                .scoped_slot_info(&scope, slot)
                .unwrap()
                .unwrap()
                .binding_epoch;
            let start_epoch = pool.mutation_epoch();
            let born_txn = token.born_txn;
            let mutate = || {
                let mut page = pool.borrow_page_mut_in_scope(&scope, &authority).unwrap();
                encode_blob_leaf(&mut page, born_txn, 0, 1, |_| 51);
            };
            let restore = || {
                pool.borrow_page_mut_in_scope(&scope, &authority)
                    .unwrap()
                    .copy_from_slice(&original);
            };
            let source = MutateRestoreOnCheck {
                inner: SlicePageSource::new(&[], 100),
                checks: Cell::new(0),
                mutate_on: 2,
                restore_on: 3,
                fail_on_mutate,
                mutate: &mutate,
                restore: &restore,
            };
            let mut path = [RetirementPathFrame::new(); 2];
            let mut replacement_entries = [EMPTY_REPLACEMENT; 2];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_entries);
            let mut release_entries = [0u32; 2];
            let mut releases = PrivateReleaseBuffer::new(&mut release_entries);
            let mut role_entries = [PageRoleIndexSlot::new(); 16];
            let mut roles = PageRoleIndex::new(&mut role_entries);

            let error = match RetirementTreeEditor::plan_upsert_newest(
                &source,
                state(1, 100, 0, 0),
                &mut token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            ) {
                Ok(plan) => plan.apply().unwrap_err(),
                Err(error) => error,
            };
            assert_eq!(
                error,
                RetirementWriteError::StaleEditPlan(RetirementEditBinding::Pool)
            );
            assert_eq!(source.checks.get(), 2);
            assert_ne!(pool.test_bytes(slot).unwrap(), original);
            assert_eq!(pool.mutation_epoch(), start_epoch + 1);
            assert_eq!(
                pool.scoped_slot_info(&scope, slot)
                    .unwrap()
                    .unwrap()
                    .binding_epoch,
                binding_epoch
            );
            assert_eq!(pool.scoped_in_use(&scope).unwrap(), 1);
            assert!(!token.stabilized);

            restore();
            assert_eq!(pool.test_bytes(slot).unwrap(), original);
            assert_eq!(pool.mutation_epoch(), start_epoch + 2);
            assert_eq!(
                pool.scoped_slot_info(&scope, slot)
                    .unwrap()
                    .unwrap()
                    .binding_epoch,
                binding_epoch
            );

            let result = RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&[], 100),
                state(1, 100, 0, 0),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap();
            assert_eq!(result.root, 22);
            assert_eq!(pool.scoped_in_use(&scope).unwrap(), 2);
        }
    }

    #[test]
    fn scoped_apply_epoch_headroom_accepts_exact_boundary_and_rejects_one_step_short() {
        for (start_epoch, succeeds) in [(u64::MAX - 3, true), (u64::MAX - 2, false)] {
            let mut storage = vec![PrivatePageSlot::empty(); 2];
            let pool = PrivatePagePool::new_vacant(&mut storage, 100, 100, 2).unwrap();
            let scope = pool.reserve_scope(2).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            for pgno in [20, 22] {
                pool.bind_page(
                    &checkpoint,
                    &scope,
                    pgno,
                    PrivatePageAuthorization::CommittedFree,
                )
                .unwrap();
            }
            pool.commit_checkpoint(checkpoint).unwrap();

            let mut arena = PrivatePageArena::from_scoped_pool(&pool, &scope, 2).unwrap();
            let mut blob_pages = [0u32; 1];
            let mut token = RetirementBlobBuilder::build(
                &[50],
                &mut arena,
                &mut BlobBuildScratch::new(&mut blob_pages),
            )
            .unwrap();
            let generation = token.arena.generation.get();
            let mut path = [RetirementPathFrame::new(); 2];
            let mut replacement_entries = [EMPTY_REPLACEMENT; 2];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_entries);
            let mut release_entries = [0u32; 2];
            let mut releases = PrivateReleaseBuffer::new(&mut release_entries);
            let mut role_entries = [PageRoleIndexSlot::new(); 16];
            let mut roles = PageRoleIndex::new(&mut role_entries);
            pool.test_set_epoch(start_epoch);
            let source = SlicePageSource::new(&[], 100);

            let plan = RetirementTreeEditor::plan_upsert_newest(
                &source,
                state(1, 100, 0, 0),
                &mut token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap();
            if succeeds {
                let result = plan.apply().unwrap();
                assert_eq!(result.root, 22);
                assert_eq!(pool.mutation_epoch(), u64::MAX);
                assert_eq!(pool.scoped_in_use(&scope).unwrap(), 2);
            } else {
                assert_eq!(
                    plan.apply().unwrap_err(),
                    RetirementWriteError::PrivatePool(PrivatePagePoolError::EpochExhausted)
                );
                assert_eq!(pool.mutation_epoch(), start_epoch);
                assert_eq!(pool.scoped_in_use(&scope).unwrap(), 1);
                assert_eq!(token.arena.generation.get(), generation);
                assert!(!token.stabilized);
                pool.test_set_epoch(100);
            }
        }
    }

    #[test]
    fn unscoped_apply_epoch_headroom_includes_mutable_guard_acquisition() {
        for (start_epoch, succeeds) in [(u64::MAX - 4, true), (u64::MAX - 3, false)] {
            let mut storage = appended_slots(20, 2);
            let mut arena = PrivatePageArena::new(&mut storage, 20, 22, 2).unwrap();
            let mut blob_pages = [0u32; 1];
            let mut token = RetirementBlobBuilder::build(
                &[7],
                &mut arena,
                &mut BlobBuildScratch::new(&mut blob_pages),
            )
            .unwrap();
            let generation = token.arena.generation.get();
            let mut path = [RetirementPathFrame::new(); 2];
            let mut replacement_entries = [EMPTY_REPLACEMENT; 2];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_entries);
            let mut release_entries = [0u32; 2];
            let mut releases = PrivateReleaseBuffer::new(&mut release_entries);
            let mut role_entries = [PageRoleIndexSlot::new(); 16];
            let mut roles = PageRoleIndex::new(&mut role_entries);
            token.arena.pool().test_set_epoch(start_epoch);
            let source = SlicePageSource::new(&[], 20);

            let plan = RetirementTreeEditor::plan_upsert_newest(
                &source,
                state(1, 20, 0, 0),
                &mut token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap();
            if succeeds {
                let result = plan.apply().unwrap();
                assert_eq!(result.root, 21);
                assert_eq!(token.arena.pool().mutation_epoch(), u64::MAX);
                assert_eq!(token.arena.in_use_count().unwrap(), 2);
            } else {
                assert_eq!(
                    plan.apply().unwrap_err(),
                    RetirementWriteError::PrivatePool(PrivatePagePoolError::EpochExhausted)
                );
                assert_eq!(token.arena.pool().mutation_epoch(), start_epoch);
                assert_eq!(token.arena.in_use_count().unwrap(), 1);
                assert_eq!(token.arena.generation.get(), generation);
                assert!(!token.stabilized);
                token.arena.pool().test_set_epoch(100);
            }
        }
    }

    #[test]
    fn unscoped_release_headroom_is_exact_and_failure_is_pre_checkpoint_atomic() {
        for (start_epoch, succeeds) in [(u64::MAX - 8, true), (u64::MAX - 7, false)] {
            let mut storage = appended_slots(20, 2);
            let mut arena = PrivatePageArena::new(&mut storage, 20, 22, 2).unwrap();
            let mut blob_pages = [0u32; 1];
            let token = RetirementBlobBuilder::build(
                &[7],
                &mut arena,
                &mut BlobBuildScratch::new(&mut blob_pages),
            )
            .unwrap();
            let mut path = [RetirementPathFrame::new(); 2];
            let mut replacement_entries = [EMPTY_REPLACEMENT; 2];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_entries);
            let mut release_entries = [0u32; 2];
            let mut releases = PrivateReleaseBuffer::new(&mut release_entries);
            let mut role_entries = [PageRoleIndexSlot::new(); 16];
            let mut roles = PageRoleIndex::new(&mut role_entries);
            let first = RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&[], 20),
                state(1, 20, 0, 0),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap();
            assert_eq!(arena.in_use_count().unwrap(), 2);
            let generation = arena.generation.get();
            let before = [
                arena.pool().test_bytes(0).unwrap(),
                arena.pool().test_bytes(1).unwrap(),
            ];
            arena.pool().test_set_epoch(start_epoch);
            let source = SlicePageSource::new(&[], 20);

            let plan = RetirementTreeEditor::plan_delete_oldest_prefix(
                &source,
                state(1, 20, first.root, 1),
                1,
                &mut arena,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap();
            let RetirementEditPlan::Delete(inner) = &plan else {
                unreachable!()
            };
            assert_eq!(inner.staged_releases, 2);
            assert_eq!(inner.delete_scratch.len, 0);

            if succeeds {
                let result = plan.apply().unwrap();
                assert_eq!(result.root, 0);
                assert_eq!(result.batch_count, 0);
                assert_eq!(arena.pool().mutation_epoch(), u64::MAX);
                assert_eq!(arena.in_use_count().unwrap(), 0);
            } else {
                assert_eq!(
                    plan.apply().unwrap_err(),
                    RetirementWriteError::PrivatePool(PrivatePagePoolError::EpochExhausted)
                );
                assert_eq!(arena.pool().mutation_epoch(), start_epoch);
                assert_eq!(arena.generation.get(), generation);
                assert_eq!(arena.in_use_count().unwrap(), 2);
                assert_eq!(
                    [
                        arena.pool().test_bytes(0).unwrap(),
                        arena.pool().test_bytes(1).unwrap(),
                    ],
                    before
                );
                assert_eq!(replacements.entries().len(), 0);
                assert_eq!(releases.len, 0);
                arena.pool().test_set_epoch(100);
            }
        }
    }

    #[test]
    fn blob_build_epoch_headroom_is_exact_and_failure_is_pre_checkpoint_atomic() {
        for (start_epoch, succeeds) in [(u64::MAX - 5, true), (u64::MAX - 4, false)] {
            let mut storage = appended_slots(20, 1);
            let mut arena = PrivatePageArena::new(&mut storage, 20, 21, 2).unwrap();
            let generation = arena.generation.get();
            let before = arena.pool().test_bytes(0).unwrap();
            arena.pool().test_set_epoch(start_epoch);
            let mut blob_pages = [0u32; 1];

            let result = RetirementBlobBuilder::build(
                &[7],
                &mut arena,
                &mut BlobBuildScratch::new(&mut blob_pages),
            );
            if succeeds {
                let mut token = result.unwrap();
                assert_eq!(token.root, 20);
                assert_eq!(token.arena.pool().mutation_epoch(), u64::MAX);
                token.stabilized = true;
            } else {
                assert_eq!(
                    result.unwrap_err(),
                    RetirementWriteError::PrivatePool(PrivatePagePoolError::EpochExhausted)
                );
                assert_eq!(arena.pool().mutation_epoch(), start_epoch);
                assert_eq!(arena.generation.get(), generation);
                assert_eq!(arena.in_use_count().unwrap(), 0);
                assert_eq!(arena.pool().test_bytes(0).unwrap(), before);
                assert_eq!(blob_pages, [0]);
                arena.pool().test_set_epoch(100);
            }
        }
    }

    #[test]
    fn bitmap_page_round_trips_through_retirement_in_one_transaction_bound_pool() {
        let mut slots = [PrivatePageSlot::authorized(
            10,
            PrivatePageAuthorization::CommittedFree,
        )];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        assert_eq!(
            PrivatePageArena::from_pool(&pool, 4).unwrap_err(),
            RetirementWriteError::PrivatePool(PrivatePagePoolError::PendingTransactionMismatch {
                expected: 2,
                actual: 4,
            })
        );
        let source = SlicePageSource::new(&[], 20);
        let mut replacements = [];
        let mut candidates = [0u32; 1];
        let mut index = [BitmapCowIndexNode::empty(); 1];
        let mut available = [0usize; 1];
        let mut scratch = [FreeBitmapInsertPage::empty()];
        {
            let mut cow = FreeBitmapCow::from_pool(
                &source,
                1,
                0,
                &pool,
                SharedFreeBitmapCowLedger::empty(
                    &mut replacements,
                    &mut candidates,
                    &mut index,
                    &mut available,
                ),
            )
            .unwrap();
            cow.insert_free(5, &mut scratch).unwrap();
            assert_eq!(cow.root(), 10);
        }

        let bitmap = pool.authority(10, PrivatePageOwner::Bitmap, 2).unwrap();
        let bitmap_page = pool.borrow_page(&bitmap).unwrap();
        let physical_address = bitmap_page.as_ptr();
        let original_byte = bitmap_page[100];
        drop(bitmap_page);
        let retirement = pool
            .transfer(
                bitmap,
                PrivatePageOwner::Retirement,
                7,
                private_origin_tag(PrivatePageOrigin::RetirementTree),
            )
            .unwrap();
        let mut retirement_page = pool.borrow_page_mut(&retirement).unwrap();
        assert_eq!(retirement_page.as_ptr(), physical_address);
        retirement_page[100] = 0x5a;
        drop(retirement_page);

        let arena = PrivatePageArena::from_pool(&pool, 2).unwrap();
        assert_eq!(
            arena.private_state(10).unwrap(),
            Some(RetirementPrivatePageState::InUse {
                origin: PrivatePageOrigin::RetirementTree,
                generation: 7,
            })
        );
        let mut bytes = [0u8; PAGE_SIZE];
        assert!(arena.read_page(10, &mut bytes).unwrap());
        assert_eq!(bytes[100], 0x5a);

        let retirement = pool.authority(10, PrivatePageOwner::Retirement, 7).unwrap();
        pool.borrow_page_mut(&retirement).unwrap()[100] = original_byte;
        let retirement = pool.authority(10, PrivatePageOwner::Retirement, 7).unwrap();
        {
            let bitmap = pool
                .transfer(retirement, PrivatePageOwner::Bitmap, 2, 0)
                .unwrap();
            assert_eq!(
                pool.borrow_page(&bitmap).unwrap().as_ptr(),
                physical_address
            );
        }

        let mut second_replacements = [];
        let mut second_candidates = [0u32; 1];
        let mut second_index = [BitmapCowIndexNode::empty(); 1];
        let mut second_available = [0usize; 1];
        let second_cow = FreeBitmapCow::from_pool(
            &source,
            1,
            10,
            &pool,
            SharedFreeBitmapCowLedger::empty(
                &mut second_replacements,
                &mut second_candidates,
                &mut second_index,
                &mut second_available,
            ),
        )
        .unwrap();
        assert_eq!(
            second_cow.private_page(10).unwrap().as_ptr(),
            physical_address
        );
    }

    #[test]
    fn scoped_bitmap_page_round_trips_through_retirement_without_crossing_scopes() {
        let mut storage = vec![PrivatePageSlot::empty(); 3];
        let pool = PrivatePagePool::new_vacant(&mut storage, 20, 20, 2).unwrap();
        let target = pool.reserve_scope(2).unwrap();
        let foreign = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        for pgno in [10, 11] {
            pool.bind_page(
                &checkpoint,
                &target,
                pgno,
                PrivatePageAuthorization::CommittedFree,
            )
            .unwrap();
        }
        pool.bind_page(
            &checkpoint,
            &foreign,
            9,
            PrivatePageAuthorization::CommittedFree,
        )
        .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();

        let checkpoint = pool.begin_checkpoint().unwrap();
        let bitmap = pool
            .claim_lowest_in_scope(&checkpoint, &target, PrivatePageOwner::Bitmap, 2, 0)
            .unwrap();
        assert_eq!(bitmap.page_number(), 10);
        let mut page = pool.borrow_page_mut_in_scope(&target, &bitmap).unwrap();
        page[111] = 0x5a;
        let physical_address = page.as_ptr();
        drop(page);
        pool.commit_checkpoint_in_scope(checkpoint, &target)
            .unwrap();

        let source = SlicePageSource::new(&[], 20);
        let mut arena_bindings = [BitmapCowArenaBinding::empty(); 2];
        let mut replacements = [];
        let mut candidates = [];
        let mut index = [BitmapCowIndexNode::empty(); 2];
        let mut available = [0usize; 2];
        let mut verified = [];
        let cow = FreeBitmapCow::from_scoped_pool(
            &source,
            1,
            20,
            10,
            &pool,
            &target,
            ScopedFreeBitmapCowLedger::new(
                &mut arena_bindings,
                &mut replacements,
                0,
                &mut candidates,
                0,
                &mut index,
                &mut available,
                &mut verified,
                0,
                false,
                0,
                0,
            ),
        )
        .unwrap();
        assert_eq!(cow.private_page(10).unwrap().as_ptr(), physical_address);

        let arena = PrivatePageArena::from_scoped_pool(&pool, &target, 2).unwrap();
        arena
            .transfer_bitmap_page_to_retirement(10, 7, PrivatePageOrigin::RetirementTree)
            .unwrap();
        assert!(cow.private_page(10).is_none());
        let mut bytes = [0u8; PAGE_SIZE];
        assert!(arena.read_page(10, &mut bytes).unwrap());
        assert_eq!(bytes[111], 0x5a);

        arena
            .transfer_retirement_page_to_bitmap(10, 7, PrivatePageOrigin::RetirementTree)
            .unwrap();
        let bitmap = cow.private_page(10).unwrap();
        assert_eq!(bitmap.as_ptr(), physical_address);
        assert_eq!(bitmap[111], 0x5a);
        let slot = pool.find_in_scope(&target, 10).unwrap().unwrap();
        assert!(matches!(
            pool.scoped_slot_info(&target, slot).unwrap().unwrap().state,
            PrivatePagePoolState::InUse {
                owner: PrivatePageOwner::Bitmap,
                owner_generation: 2,
                tag: 0,
            }
        ));
        assert_eq!(pool.scoped_available(&foreign).unwrap(), 1);
    }

    fn image(page_count: usize) -> Vec<u8> {
        vec![0; page_count * PAGE_SIZE]
    }

    fn page_mut(bytes: &mut [u8], pgno: u32) -> &mut [u8; PAGE_SIZE] {
        let start = pgno as usize * PAGE_SIZE;
        (&mut bytes[start..start + PAGE_SIZE]).try_into().unwrap()
    }

    fn state(selected_txn: u64, page_count: u64, root: u32, batches: u64) -> RetirementTreeState {
        RetirementTreeState {
            selected_txn,
            page_count,
            root,
            batch_count: batches,
        }
    }

    fn batch(txn: u64, blob_root: u32, listed: usize) -> RetirementBatch {
        RetirementBatch {
            retired_by_txn: txn,
            page_count: listed as u64,
            page_list_blob_root: blob_root,
        }
    }

    fn put_blob_leaf_at(
        page: &mut [u8; PAGE_SIZE],
        born_txn: u64,
        logical_offset: u64,
        pages: &[u32],
    ) {
        encode_blob_leaf(
            page,
            born_txn,
            logical_offset,
            pages.len() as u64,
            |index| pages[index as usize],
        );
    }

    fn put_blob_leaf(page: &mut [u8; PAGE_SIZE], born_txn: u64, pages: &[u32]) {
        put_blob_leaf_at(page, born_txn, 0, pages);
    }

    fn put_blob_branch(page: &mut [u8; PAGE_SIZE], born_txn: u64, children: &[(u64, u32)]) {
        page.fill(0);
        PageHeader {
            page_type: PageType::BlobBranch,
            born_txn,
            item_count: children.len() as u16,
            level: 1,
            lower: (PAGE_HEADER_SIZE as usize + children.len() * 16) as u16,
            upper: PAGE_SIZE as u16,
            aux: BlobKind::RetirementPageList as u32,
            page_crc32c: 0,
        }
        .encode_into(page);
        for (index, &(offset, child)) in children.iter().enumerate() {
            let at = PAGE_HEADER_SIZE as usize + index * 16;
            page[at..at + 8].copy_from_slice(&offset.to_le_bytes());
            page[at + 8..at + 12].copy_from_slice(&child.to_le_bytes());
        }
        page::write_crc32c(page);
    }

    fn put_retirement_leaf(page: &mut [u8; PAGE_SIZE], born_txn: u64, batches: &[RetirementBatch]) {
        encode_retirement_leaf(page, born_txn, batches.len(), |index, destination| {
            encode_retirement_batch(destination, batches[index]);
        });
    }

    fn put_retirement_branch(
        page: &mut [u8; PAGE_SIZE],
        born_txn: u64,
        level: u16,
        entries: &[(u64, u32)],
    ) {
        encode_retirement_branch(
            page,
            born_txn,
            level,
            entries.len(),
            |index, destination| {
                encode_retirement_branch_entry(
                    destination,
                    ChildReference {
                        maximum: entries[index].0,
                        pgno: entries[index].1,
                        level: level - 1,
                    },
                );
            },
        );
    }

    fn deletion_image() -> Vec<u8> {
        let mut bytes = image(100);
        put_retirement_branch(page_mut(&mut bytes, 2), 1, 1, &[(3, 3), (5, 4)]);
        put_retirement_leaf(
            page_mut(&mut bytes, 3),
            1,
            &[batch(2, 5, 1), batch(3, 6, 1)],
        );
        put_retirement_leaf(
            page_mut(&mut bytes, 4),
            1,
            &[batch(4, 7, 1), batch(5, 8, 1)],
        );
        put_blob_leaf(page_mut(&mut bytes, 5), 1, &[50]);
        put_blob_leaf(page_mut(&mut bytes, 6), 1, &[51]);
        put_blob_leaf(page_mut(&mut bytes, 7), 1, &[52]);
        put_blob_leaf(page_mut(&mut bytes, 8), 1, &[53]);
        bytes
    }

    #[test]
    fn immutable_blob_builder_has_exact_geometry_and_automatic_rollback() {
        for (count, expected_pages, expected_level) in [(1_012usize, 1usize, 0u16), (1_013, 3, 1)] {
            let committed = count as u64 + 20;
            let input: Vec<u32> = (2..2 + count as u32).collect();
            let mut slots = appended_slots(committed as u32, expected_pages);
            let mut arena =
                PrivatePageArena::new(&mut slots, committed, committed + expected_pages as u64, 9)
                    .unwrap();
            let mut order = vec![0; expected_pages];
            let mut scratch = BlobBuildScratch::new(&mut order);
            let ((), allocations) = count_thread_allocations(|| {
                let token = RetirementBlobBuilder::build(&input, &mut arena, &mut scratch).unwrap();
                assert_eq!(token.page_count(), count as u64);
                assert_eq!(token.byte_length(), count as u64 * 4);
                assert_eq!(token.private_pages(), expected_pages);
                let root = token.arena.test_page(token.root()).unwrap();
                assert_eq!(PageHeader::decode(&root, 9).unwrap().level, expected_level);
                assert!(page::verify_crc32c(&root));
                drop(root);
                token.discard();
            });
            assert_eq!(allocations, 0);
            assert_eq!(arena.in_use_count().unwrap(), 0);
        }
    }

    #[test]
    fn retirement_blob_builder_streams_page_number_index() {
        let count = usize::try_from(RETIREMENT_VALUES_PER_BLOB_LEAF).unwrap() + 1;
        let values: Vec<u32> = (2..2 + u32::try_from(count).unwrap()).collect();
        let mut index_pages = [PageNumberIndexPage::empty(); 1];
        let mut workspace = PageNumberIndexWorkspace::new(&mut index_pages);
        let mut index = PageNumberIndex::new(&mut workspace).unwrap();
        for &value in values.iter().rev() {
            assert!(index.insert(value).unwrap());
        }

        let committed = 5_000u64;
        let mut slots = appended_slots(committed as u32, 3);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 9).unwrap();
        let mut order = [0u32; 3];
        let ((), allocations) = count_thread_allocations(|| {
            let token = RetirementBlobBuilder::build_from_index(
                &mut index,
                &mut arena,
                &mut BlobBuildScratch::new(&mut order),
            )
            .unwrap();
            assert_eq!(token.page_count(), count as u64);
            assert_eq!(token.byte_length(), count as u64 * 4);
            assert_eq!(token.private_pages(), 3);

            let root = token.arena.test_page(token.root()).unwrap();
            let branch =
                BlobBranch::open(&root, 9, BlobKind::RetirementPageList, committed + 3).unwrap();
            assert_eq!(branch.level(), 1);
            assert_eq!(branch.len(), 2);
            branch.verify_local().unwrap();
            let first = branch.entry(0).unwrap();
            let second = branch.entry(1).unwrap();
            assert_eq!(first.logical_offset, 0);
            assert_eq!(second.logical_offset, RETIREMENT_VALUES_PER_BLOB_LEAF * 4);
            drop(root);

            let first_page = token.arena.test_page(first.child_pgno).unwrap();
            let first_leaf = BlobLeaf::open(&first_page, 9, BlobKind::RetirementPageList).unwrap();
            first_leaf.verify_local().unwrap();
            assert_eq!(first_leaf.logical_offset(), 0);
            assert_eq!(
                first_leaf.data_len(),
                u16::try_from(RETIREMENT_VALUES_PER_BLOB_LEAF * 4).unwrap()
            );
            for (position, expected) in values[..count - 1].iter().enumerate() {
                let at = position * 4;
                assert_eq!(
                    u32::from_le_bytes(first_leaf.data()[at..at + 4].try_into().unwrap()),
                    *expected
                );
            }
            drop(first_page);

            let second_page = token.arena.test_page(second.child_pgno).unwrap();
            let second_leaf =
                BlobLeaf::open(&second_page, 9, BlobKind::RetirementPageList).unwrap();
            second_leaf.verify_local().unwrap();
            assert_eq!(
                second_leaf.logical_offset(),
                RETIREMENT_VALUES_PER_BLOB_LEAF * 4
            );
            assert_eq!(second_leaf.data_len(), 4);
            assert_eq!(
                u32::from_le_bytes(second_leaf.data()[..4].try_into().unwrap()),
                values[count - 1]
            );
            drop(second_page);
            token.discard();
        });
        assert_eq!(allocations, 0);
        assert_eq!(arena.in_use_count().unwrap(), 0);
    }

    #[test]
    fn retirement_blob_index_preflight_is_atomic() {
        for (values, scratch_len, expected) in [
            (
                vec![1],
                1,
                RetirementWriteError::RetirementStreamPageOutOfBounds(1),
            ),
            (
                (2..2 + u32::try_from(RETIREMENT_VALUES_PER_BLOB_LEAF + 1).unwrap()).collect(),
                2,
                RetirementWriteError::BlobBuildScratchTooSmall {
                    required: 3,
                    actual: 2,
                },
            ),
        ] {
            let mut index_pages = [PageNumberIndexPage::empty(); 1];
            let mut workspace = PageNumberIndexWorkspace::new(&mut index_pages);
            let mut index = PageNumberIndex::new(&mut workspace).unwrap();
            for value in values.iter().rev().copied() {
                assert!(index.insert(value).unwrap());
            }
            let committed = 5_000u64;
            let mut slots = appended_slots(committed as u32, 3);
            let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 9).unwrap();
            let mut order = [99u32; 3];
            let before = order;
            let error = RetirementBlobBuilder::build_from_index(
                &mut index,
                &mut arena,
                &mut BlobBuildScratch::new(&mut order[..scratch_len]),
            )
            .unwrap_err();
            assert_eq!(error, expected);
            assert_eq!(order, before);
            assert_eq!(arena.in_use_count().unwrap(), 0);
        }
    }

    #[test]
    fn retirement_blob_geometry_is_available_without_a_page_list() {
        assert_eq!(
            RetirementBlobBuilder::required_private_pages(0),
            Err(RetirementWriteError::EmptyRetirementStream)
        );
        assert_eq!(
            RetirementBlobBuilder::required_private_pages(MAX_BATCH_PAGE_COUNT + 1),
            Err(RetirementWriteError::RetirementStreamTooLong(
                MAX_BATCH_PAGE_COUNT + 1
            ))
        );

        let leaf_values = RETIREMENT_VALUES_PER_BLOB_LEAF;
        assert_eq!(RetirementBlobBuilder::required_private_pages(1).unwrap(), 1);
        assert_eq!(
            RetirementBlobBuilder::required_private_pages(leaf_values).unwrap(),
            1
        );
        assert_eq!(
            RetirementBlobBuilder::required_private_pages(leaf_values + 1).unwrap(),
            3
        );

        let branch_values = leaf_values * BLOB_BRANCH_CAPACITY as u64;
        assert_eq!(
            RetirementBlobBuilder::required_private_pages(branch_values).unwrap(),
            BLOB_BRANCH_CAPACITY + 1
        );
        assert_eq!(
            RetirementBlobBuilder::required_private_pages(branch_values + 1).unwrap(),
            BLOB_BRANCH_CAPACITY + 4
        );
    }

    #[test]
    fn blob_preflight_budgets_order_and_bounds_are_atomic() {
        let committed = 2_000u64;
        let input: Vec<u32> = (2..1_015).collect();
        let mut slots = appended_slots(committed as u32, 3);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 9).unwrap();
        let mut short = [0u32; 2];
        assert_eq!(
            RetirementBlobBuilder::build(
                &input,
                &mut arena,
                &mut BlobBuildScratch::new(&mut short),
            )
            .unwrap_err(),
            RetirementWriteError::BlobBuildScratchTooSmall {
                required: 3,
                actual: 2,
            }
        );
        assert_eq!(arena.in_use_count().unwrap(), 0);

        for (input, expected) in [
            (
                vec![2, 4, 3],
                RetirementWriteError::RetirementStreamOrder {
                    previous: 4,
                    current: 3,
                },
            ),
            (
                vec![2, 2],
                RetirementWriteError::RetirementStreamOrder {
                    previous: 2,
                    current: 2,
                },
            ),
            (
                vec![1, 2],
                RetirementWriteError::RetirementStreamPageOutOfBounds(1),
            ),
        ] {
            let mut order = [0u32; 3];
            assert_eq!(
                RetirementBlobBuilder::build(
                    &input,
                    &mut arena,
                    &mut BlobBuildScratch::new(&mut order),
                )
                .unwrap_err(),
                expected
            );
            assert_eq!(arena.in_use_count().unwrap(), 0);
        }
    }

    #[test]
    fn empty_upsert_builds_canonical_private_tree() {
        let committed = 20u64;
        let mut slots = appended_slots(committed as u32, 4);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 4, 2).unwrap();
        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[7], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let blob_root = token.root();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 4];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let result = RetirementTreeEditor::upsert_newest(
            &SlicePageSource::new(&[], committed),
            state(1, committed, 0, 0),
            token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(result.batch_count, 1);
        assert_eq!(arena.in_use_count().unwrap(), 2);
        assert!(replacements.entries().is_empty());
        let root_page = arena.test_page(result.root).unwrap();
        let leaf = RetirementLeaf::open(&root_page, 2, committed + 4).unwrap();
        assert_eq!(leaf.batch(0).unwrap().page_list_blob_root, blob_root);
    }

    struct AccessFailSource(PageSourceError);

    impl CommittedPageSource for AccessFailSource {
        fn check_access(&self) -> Result<(), PageSourceError> {
            Err(self.0)
        }

        fn read_page(&self, _: u32, _: &mut [u8; PAGE_SIZE]) -> Result<(), PageSourceError> {
            unreachable!("access failure must win before reads")
        }
    }

    struct FailOnCheck<'a> {
        inner: SlicePageSource<'a>,
        checks: Cell<usize>,
        fail_on: usize,
        error: PageSourceError,
    }

    impl CommittedPageSource for FailOnCheck<'_> {
        fn check_access(&self) -> Result<(), PageSourceError> {
            let check = self.checks.get() + 1;
            self.checks.set(check);
            if check == self.fail_on {
                Err(self.error)
            } else {
                Ok(())
            }
        }

        fn read_page(
            &self,
            pgno: u32,
            destination: &mut [u8; PAGE_SIZE],
        ) -> Result<(), PageSourceError> {
            self.inner.read_page(pgno, destination)
        }
    }

    struct MutateOnCheck<'a, 'action> {
        inner: SlicePageSource<'a>,
        checks: Cell<usize>,
        mutate_on: usize,
        action: &'action dyn Fn(),
    }

    impl CommittedPageSource for MutateOnCheck<'_, '_> {
        fn check_access(&self) -> Result<(), PageSourceError> {
            let check = self.checks.get() + 1;
            self.checks.set(check);
            if check == self.mutate_on {
                (self.action)();
            }
            Ok(())
        }

        fn read_page(
            &self,
            pgno: u32,
            destination: &mut [u8; PAGE_SIZE],
        ) -> Result<(), PageSourceError> {
            self.inner.read_page(pgno, destination)
        }
    }

    struct MutateRestoreOnCheck<'a, 'action> {
        inner: SlicePageSource<'a>,
        checks: Cell<usize>,
        mutate_on: usize,
        restore_on: usize,
        fail_on_mutate: bool,
        mutate: &'action dyn Fn(),
        restore: &'action dyn Fn(),
    }

    impl CommittedPageSource for MutateRestoreOnCheck<'_, '_> {
        fn check_access(&self) -> Result<(), PageSourceError> {
            let check = self.checks.get() + 1;
            self.checks.set(check);
            if check == self.mutate_on {
                (self.mutate)();
                if self.fail_on_mutate {
                    return Err(PageSourceError::ForkedHandle);
                }
            } else if check == self.restore_on {
                (self.restore)();
            }
            Ok(())
        }

        fn read_page(
            &self,
            pgno: u32,
            destination: &mut [u8; PAGE_SIZE],
        ) -> Result<(), PageSourceError> {
            self.inner.read_page(pgno, destination)
        }
    }

    struct MutateAndFailOnCheck<'a, 'action> {
        inner: SlicePageSource<'a>,
        checks: Cell<usize>,
        mutate_on: usize,
        action: &'action dyn Fn(),
    }

    impl CommittedPageSource for MutateAndFailOnCheck<'_, '_> {
        fn check_access(&self) -> Result<(), PageSourceError> {
            let check = self.checks.get() + 1;
            self.checks.set(check);
            if check == self.mutate_on {
                (self.action)();
                return Err(PageSourceError::ForkedHandle);
            }
            Ok(())
        }

        fn read_page(
            &self,
            pgno: u32,
            destination: &mut [u8; PAGE_SIZE],
        ) -> Result<(), PageSourceError> {
            self.inner.read_page(pgno, destination)
        }
    }

    #[test]
    fn source_access_precedes_empty_invalid_noop_cached_and_apply_paths() {
        let error = PageSourceError::ForkedHandle;
        let mut slots = appended_slots(20, 3);
        let mut arena = PrivatePageArena::new(&mut slots, 20, 23, 2).unwrap();
        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[7], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 3];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &AccessFailSource(error),
                state(0, 20, 99, 0),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::Source(error)
        );
        assert_eq!(arena.in_use_count().unwrap(), 0);

        assert_eq!(
            RetirementTreeEditor::delete_oldest_prefix(
                &AccessFailSource(error),
                state(0, 20, 99, 0),
                0,
                &mut arena,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::Source(error)
        );

        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[8], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let source = FailOnCheck {
            inner: SlicePageSource::new(&[], 20),
            checks: Cell::new(0),
            fail_on: 3,
            error,
        };
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &source,
                state(1, 20, 0, 0),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::Source(error)
        );
        assert_eq!(source.checks.get(), 3);
        assert_eq!(arena.in_use_count().unwrap(), 0);
    }

    #[test]
    fn forked_access_wins_for_valid_empty_noop_private_cache_and_delete_apply() {
        let error = PageSourceError::ForkedHandle;
        let committed = 30u64;
        let mut slots = appended_slots(30, 6);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 6, 2).unwrap();
        let mut path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 16];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 6];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 48];
        let mut roles = PageRoleIndex::new(&mut role_storage);

        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[7], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &AccessFailSource(error),
                state(1, committed, 0, 0),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::Source(error)
        );
        assert_eq!(arena.in_use_count().unwrap(), 0);

        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[7], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let first = RetirementTreeEditor::upsert_newest(
            &SlicePageSource::new(&[], committed),
            state(1, committed, 0, 0),
            token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(arena.in_use_count().unwrap(), 2);
        assert_eq!(
            RetirementTreeEditor::delete_oldest_prefix(
                &AccessFailSource(error),
                state(1, committed, first.root, 1),
                0,
                &mut arena,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::Source(error)
        );
        assert_eq!(arena.in_use_count().unwrap(), 2);

        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[8], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &AccessFailSource(error),
                state(1, committed, first.root, 1),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::Source(error)
        );
        assert_eq!(arena.in_use_count().unwrap(), 2);

        let bytes = deletion_image();
        let mut committed_slots = appended_slots(100, 3);
        let mut committed_arena = PrivatePageArena::new(&mut committed_slots, 100, 103, 6).unwrap();
        let source = FailOnCheck {
            inner: SlicePageSource::new(&bytes, 100),
            checks: Cell::new(0),
            fail_on: 3,
            error,
        };
        assert_eq!(
            RetirementTreeEditor::delete_oldest_prefix(
                &source,
                state(5, 100, 2, 4),
                1,
                &mut committed_arena,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::Source(error)
        );
        assert_eq!(source.checks.get(), 3);
        assert_eq!(committed_arena.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());
    }

    #[test]
    fn pending_transaction_must_be_exactly_selected_plus_one_before_mutation() {
        let mut no_slots = [];
        let mut skipped = PrivatePageArena::new(&mut no_slots, 20, 20, 7).unwrap();
        let mut path = [RetirementPathFrame::new(); 1];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 1];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut no_release = [];
        let mut releases = PrivateReleaseBuffer::new(&mut no_release);
        let mut role_storage = [PageRoleIndexSlot::new(); 2];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert_eq!(
            RetirementTreeEditor::delete_oldest_prefix(
                &SlicePageSource::new(&[], 20),
                state(5, 20, 0, 0),
                0,
                &mut skipped,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::TransactionOrder {
                selected: 5,
                pending: 7,
            }
        );
        assert_eq!(skipped.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());
        assert_eq!(
            RetirementTreeEditor::delete_oldest_prefix(
                &AccessFailSource(PageSourceError::ForkedHandle),
                state(5, 20, 0, 0),
                0,
                &mut skipped,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::Source(PageSourceError::ForkedHandle)
        );

        let mut overflow = PrivatePageArena::new(&mut no_slots, 20, 20, u64::MAX).unwrap();
        assert_eq!(
            RetirementTreeEditor::delete_oldest_prefix(
                &SlicePageSource::new(&[], 20),
                state(u64::MAX, 20, 0, 0),
                0,
                &mut overflow,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::SelectedTransactionOverflow(u64::MAX)
        );
        assert_eq!(overflow.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());
    }

    #[test]
    fn newest_t_replacement_recycles_private_tree_and_blob_pages() {
        let committed = 50u64;
        let mut slots = appended_slots(committed as u32, 6);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 6, 2).unwrap();
        let mut current = state(1, committed, 0, 0);
        let mut path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 4];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 32];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let mut roots = [0u32; 3];
        for (round, listed) in [&[2u32][..], &[2, 3][..], &[2, 3, 4][..]]
            .into_iter()
            .enumerate()
        {
            let mut order = [0u32; 1];
            let token = RetirementBlobBuilder::build(
                listed,
                &mut arena,
                &mut BlobBuildScratch::new(&mut order),
            )
            .unwrap();
            let result = RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&[], committed),
                current,
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap();
            roots[round] = result.root;
            current.root = result.root;
            current.batch_count = result.batch_count;
            assert_eq!(result.batch_count, 1);
            assert_eq!(arena.in_use_count().unwrap(), 2);
            assert!(replacements.entries().is_empty());
        }
        assert_eq!(
            roots[0], roots[2],
            "released physical pages must be reusable"
        );
    }

    #[test]
    fn combined_delete_and_upsert_has_one_atomic_fixed_point_and_no_stranding() {
        let committed = 50u64;
        let mut slots = appended_slots(committed as u32, 6);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 6, 2).unwrap();
        let mut current = state(1, committed, 0, 0);
        let mut delete_path = [RetirementPathFrame::new(); 3];
        let mut upsert_path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 6];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 32];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        for (round, listed) in [2u32, 3, 4].into_iter().enumerate() {
            let mut order = [0u32; 1];
            let token = RetirementBlobBuilder::build(
                &[listed],
                &mut arena,
                &mut BlobBuildScratch::new(&mut order),
            )
            .unwrap();
            let delete = u64::from(round != 0);
            let result = RetirementTreeEditor::delete_oldest_and_upsert_newest(
                &SlicePageSource::new(&[], committed),
                current,
                delete,
                token,
                &mut delete_path,
                &mut upsert_path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap();
            current.root = result.root;
            current.batch_count = result.batch_count;
            assert_eq!(current.batch_count, 1);
            assert_eq!(arena.in_use_count().unwrap(), 2);
            assert!(replacements.entries().is_empty());
        }
    }

    #[test]
    fn append_replace_delete_and_combined_apply_paths_allocate_nothing() {
        let committed = 50u64;
        let mut slots = appended_slots(50, 6);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 6, 2).unwrap();
        let mut path = [RetirementPathFrame::new(); 3];
        let mut delete_path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 16];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 6];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 48];
        let mut roles = PageRoleIndex::new(&mut role_storage);

        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[2], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let (append, allocations) = count_thread_allocations(|| {
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&[], committed),
                state(1, committed, 0, 0),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
        });
        assert_eq!(allocations, 0);
        let append = append.unwrap();

        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 3],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let (replace, allocations) = count_thread_allocations(|| {
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&[], committed),
                state(1, committed, append.root, 1),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
        });
        assert_eq!(allocations, 0);
        let replace = replace.unwrap();

        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[4], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let (combined, allocations) = count_thread_allocations(|| {
            RetirementTreeEditor::delete_oldest_and_upsert_newest(
                &SlicePageSource::new(&[], committed),
                state(1, committed, replace.root, 1),
                1,
                token,
                &mut delete_path,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
        });
        assert_eq!(allocations, 0);
        assert_eq!(combined.unwrap().batch_count, 1);

        let bytes = deletion_image();
        let mut delete_slots = appended_slots(100, 3);
        let mut delete_arena = PrivatePageArena::new(&mut delete_slots, 100, 103, 6).unwrap();
        let mut delete_replacement_storage = [EMPTY_REPLACEMENT; 8];
        let mut delete_replacements =
            CommittedReplacementLedger::new(&mut delete_replacement_storage);
        let mut delete_release_storage = [0u32; 3];
        let mut delete_releases = PrivateReleaseBuffer::new(&mut delete_release_storage);
        let mut delete_role_storage = [PageRoleIndexSlot::new(); 32];
        let mut delete_roles = PageRoleIndex::new(&mut delete_role_storage);
        let (deleted, allocations) = count_thread_allocations(|| {
            RetirementTreeEditor::delete_oldest_prefix(
                &SlicePageSource::new(&bytes, 100),
                state(5, 100, 2, 4),
                1,
                &mut delete_arena,
                &mut delete_path,
                &mut delete_replacements,
                &mut delete_releases,
                &mut delete_roles,
            )
        });
        assert_eq!(allocations, 0);
        assert_eq!(deleted.unwrap().batch_count, 3);
    }

    #[test]
    fn committed_prefix_delete_is_local_cow_with_origin_ledger() {
        let bytes = deletion_image();
        let mut slots = appended_slots(100, 3);
        let mut arena = PrivatePageArena::new(&mut slots, 100, 103, 6).unwrap();
        let mut path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 8];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 3];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 32];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let result = RetirementTreeEditor::delete_oldest_prefix(
            &SlicePageSource::new(&bytes, 100),
            state(5, 100, 2, 4),
            1,
            &mut arena,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(result.batch_count, 3);
        assert_eq!(result.private_pages, 2);
        assert_eq!(result.committed_replacements, 3);
        assert_eq!(arena.in_use_count().unwrap(), 2);
        assert_eq!(
            replacements.entries(),
            &[
                CommittedPageReplacement {
                    pgno: 2,
                    origin: CommittedPageOrigin::RetirementTree,
                },
                CommittedPageReplacement {
                    pgno: 3,
                    origin: CommittedPageOrigin::RetirementTree,
                },
                CommittedPageReplacement {
                    pgno: 5,
                    origin: CommittedPageOrigin::RetirementBlob,
                },
            ]
        );
    }

    #[test]
    fn newest_upsert_proves_only_the_selected_right_edge() {
        let committed = 30u64;
        let mut bytes = image(committed as usize);
        put_retirement_branch(page_mut(&mut bytes, 2), 1, 1, &[(2, 3), (3, 4)]);
        page_mut(&mut bytes, 3).fill(0);
        put_retirement_leaf(page_mut(&mut bytes, 4), 1, &[batch(3, 5, 1)]);

        let mut slots = appended_slots(30, 4);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 4, 4).unwrap();
        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 4, 10],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 4];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 32];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let result = RetirementTreeEditor::upsert_newest(
            &SlicePageSource::new(&bytes, committed),
            state(3, committed, 2, 2),
            token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(result.batch_count, 3);
        assert_eq!(result.committed_replacements, 2);
        assert!(replacements.entries().iter().all(|entry| entry.pgno != 3));
    }

    #[test]
    fn replacement_fixed_point_requires_every_committed_tree_path_page() {
        let committed = 30u64;
        let mut bytes = image(committed as usize);
        put_retirement_branch(page_mut(&mut bytes, 2), 1, 1, &[(2, 3), (3, 4)]);
        page_mut(&mut bytes, 3).fill(0);
        put_retirement_leaf(page_mut(&mut bytes, 4), 1, &[batch(3, 5, 1)]);

        for (listed, omitted) in [(&[2u32, 10][..], 4), (&[4, 10][..], 2)] {
            let mut slots = appended_slots(30, 3);
            let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 4).unwrap();
            let mut order = [0u32; 1];
            let token = RetirementBlobBuilder::build(
                listed,
                &mut arena,
                &mut BlobBuildScratch::new(&mut order),
            )
            .unwrap();
            let mut path = [RetirementPathFrame::new(); 3];
            let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
            let mut release_storage = [0u32; 3];
            let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
            let mut role_storage = [PageRoleIndexSlot::new(); 32];
            let mut roles = PageRoleIndex::new(&mut role_storage);
            assert_eq!(
                RetirementTreeEditor::upsert_newest(
                    &SlicePageSource::new(&bytes, committed),
                    state(3, committed, 2, 2),
                    token,
                    &mut path,
                    &mut replacements,
                    &mut releases,
                    &mut roles,
                )
                .unwrap_err(),
                RetirementWriteError::RetirementListOmission(omitted)
            );
            assert_eq!(arena.in_use_count().unwrap(), 0);
            assert!(replacements.entries().is_empty());
        }
    }

    #[test]
    fn append_probe_empty_tree_needs_one_tree_page_without_mutation() {
        let committed = 10u64;
        let source = SlicePageSource::new(&[], committed);
        let mut slots: [PrivatePageSlot; 0] = [];
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed, 3).unwrap();
        let mut path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut replacement_storage = [];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 4];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let generation = arena.generation.get();

        let probe = RetirementTreeEditor::probe_append_newest(
            &source,
            state(2, committed, 0, 0),
            &mut arena,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();

        assert_eq!(
            probe,
            RetirementAppendReplacementProbe {
                replacement_count: 0,
                tree_private_page_budget: 1,
            }
        );
        assert_eq!(arena.generation.get(), generation);
        assert_eq!(arena.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());
        assert!(releases.entries_from(0).is_empty());
    }

    #[test]
    fn append_probe_matches_real_append_without_allocating() {
        let committed = 10u64;
        let mut bytes = image(committed as usize);
        put_retirement_leaf(page_mut(&mut bytes, 2), 2, &[batch(2, 3, 1)]);
        put_blob_leaf(page_mut(&mut bytes, 3), 2, &[5]);
        let source = SlicePageSource::new(&bytes, committed);
        let mut probe_slots: [PrivatePageSlot; 0] = [];
        let mut probe_arena =
            PrivatePageArena::new(&mut probe_slots, committed, committed, 3).unwrap();
        let mut probe_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut probe_replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut probe_replacements =
            CommittedReplacementLedger::new(&mut probe_replacement_storage);
        let mut probe_release_storage = [];
        let mut probe_releases = PrivateReleaseBuffer::new(&mut probe_release_storage);
        let mut probe_role_storage = [PageRoleIndexSlot::new(); 16];
        let mut probe_roles = PageRoleIndex::new(&mut probe_role_storage);
        let generation = probe_arena.generation.get();

        let (probe, allocations) = count_thread_allocations(|| {
            RetirementTreeEditor::probe_append_newest(
                &source,
                state(2, committed, 2, 1),
                &mut probe_arena,
                &mut probe_path,
                &mut probe_replacements,
                &mut probe_releases,
                &mut probe_roles,
            )
        });
        assert_eq!(allocations, 0);
        let probe = probe.unwrap();
        assert_eq!(
            probe,
            RetirementAppendReplacementProbe {
                replacement_count: 1,
                tree_private_page_budget: 1,
            }
        );
        assert_eq!(probe_arena.generation.get(), generation);
        assert_eq!(probe_arena.in_use_count().unwrap(), 0);
        assert!(probe_releases.entries_from(0).is_empty());
        assert_eq!(
            probe_replacements.entries(),
            &[CommittedPageReplacement {
                pgno: 2,
                origin: CommittedPageOrigin::RetirementTree,
            }]
        );

        let mut actual_slots = appended_slots(committed as u32, 2);
        let mut actual_arena =
            PrivatePageArena::new(&mut actual_slots, committed, committed + 2, 3).unwrap();
        let mut blob_pages = [0u32; 1];
        let blob = RetirementBlobBuilder::build(
            &[2],
            &mut actual_arena,
            &mut BlobBuildScratch::new(&mut blob_pages),
        )
        .unwrap();
        let mut actual_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut actual_replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut actual_replacements =
            CommittedReplacementLedger::new(&mut actual_replacement_storage);
        let mut actual_release_storage = [];
        let mut actual_releases = PrivateReleaseBuffer::new(&mut actual_release_storage);
        let mut actual_role_storage = [PageRoleIndexSlot::new(); 16];
        let mut actual_roles = PageRoleIndex::new(&mut actual_role_storage);
        let result = RetirementTreeEditor::upsert_newest(
            &source,
            state(2, committed, 2, 1),
            blob,
            &mut actual_path,
            &mut actual_replacements,
            &mut actual_releases,
            &mut actual_roles,
        )
        .unwrap();
        assert_eq!(result.private_pages, probe.tree_private_page_budget);
        assert_eq!(actual_replacements.entries(), probe_replacements.entries());
    }

    #[test]
    fn append_probe_rejects_malformed_tree_before_arena_mutation() {
        let committed = 10u64;
        let mut bytes = image(committed as usize);
        put_retirement_leaf(page_mut(&mut bytes, 2), 2, &[batch(2, 3, 1)]);
        put_blob_leaf(page_mut(&mut bytes, 3), 2, &[5]);
        page_mut(&mut bytes, 2)[PAGE_HEADER_SIZE as usize] ^= 1;
        let source = SlicePageSource::new(&bytes, committed);
        let mut slots: [PrivatePageSlot; 0] = [];
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed, 3).unwrap();
        let mut path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let generation = arena.generation.get();

        assert!(RetirementTreeEditor::probe_append_newest(
            &source,
            state(2, committed, 2, 1),
            &mut arena,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .is_err());
        assert_eq!(arena.generation.get(), generation);
        assert_eq!(arena.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());
        assert!(releases.entries_from(0).is_empty());
    }

    #[test]
    fn reclamation_probe_discovers_all_replacement_metadata_before_blob_build() {
        let committed = 10u64;
        let mut bytes = image(committed as usize);
        put_retirement_leaf(page_mut(&mut bytes, 2), 2, &[batch(2, 3, 1)]);
        put_blob_leaf(page_mut(&mut bytes, 3), 2, &[5]);
        let source = SlicePageSource::new(&bytes, committed);
        let mut probe_slots: [PrivatePageSlot; 0] = [];
        let mut probe_arena =
            PrivatePageArena::new(&mut probe_slots, committed, committed, 3).unwrap();
        let reclaimed_pages = [5u32];
        let reclaimed = test_reclaimed_pages(&reclaimed_pages).unwrap();
        let (reclamation, _guard) = RetirementReclamation::Reclaimed(reclaimed).into_parts();
        let identity = reclamation.identity().unwrap();
        let mut delete_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut upsert_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let generation = probe_arena.generation.get();
        let (probe, allocations) = count_thread_allocations(|| {
            RetirementTreeEditor::probe_reclaimed_oldest_and_append_newest(
                &source,
                state(2, committed, 2, 1),
                identity,
                &reclamation,
                &mut probe_arena,
                &mut delete_path,
                &mut upsert_path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
        });
        assert_eq!(allocations, 0);
        let probe = probe.unwrap();
        assert_eq!(
            probe,
            RetirementReclamationReplacementProbe {
                replacement_count: 2,
                tree_private_page_budget: 1,
            }
        );
        assert_eq!(probe_arena.generation.get(), generation);
        assert_eq!(probe_arena.in_use_count().unwrap(), 0);
        assert!(releases.entries_from(0).is_empty());
        assert_eq!(
            replacements.entries(),
            &[
                CommittedPageReplacement {
                    pgno: 2,
                    origin: CommittedPageOrigin::RetirementTree,
                },
                CommittedPageReplacement {
                    pgno: 3,
                    origin: CommittedPageOrigin::RetirementBlob,
                },
            ]
        );

        let mut actual_slots = appended_slots(committed as u32, 3);
        let mut actual_arena =
            PrivatePageArena::new(&mut actual_slots, committed, committed + 3, 3).unwrap();
        let mut blob_pages = [0u32; 1];
        let blob = RetirementBlobBuilder::build(
            &[2, 3],
            &mut actual_arena,
            &mut BlobBuildScratch::new(&mut blob_pages),
        )
        .unwrap();
        let mut actual_delete_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut actual_upsert_path = [RetirementPathFrame::new(); RETIREMENT_PATH_CAPACITY];
        let mut actual_replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut actual_replacements =
            CommittedReplacementLedger::new(&mut actual_replacement_storage);
        let mut actual_release_storage = [0u32; 3];
        let mut actual_releases = PrivateReleaseBuffer::new(&mut actual_release_storage);
        let mut actual_role_storage = [PageRoleIndexSlot::new(); 16];
        let mut actual_roles = PageRoleIndex::new(&mut actual_role_storage);
        let result = RetirementTreeEditor::delete_oldest_and_upsert_newest(
            &source,
            state(2, committed, 2, 1),
            1,
            blob,
            &mut actual_delete_path,
            &mut actual_upsert_path,
            &mut actual_replacements,
            &mut actual_releases,
            &mut actual_roles,
        )
        .unwrap();
        assert_eq!(result.private_pages, probe.tree_private_page_budget);
        assert_eq!(actual_replacements.entries(), replacements.entries());
    }

    #[test]
    fn reclamation_probe_rejects_a_partially_bound_reclaimed_set() {
        let mut slots = [PrivatePageSlot::empty()];
        let pool = PrivatePagePool::new_vacant(&mut slots, 10, 10, 3).unwrap();
        let scope = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            5,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let arena = PrivatePageArena::from_scoped_pool(&pool, &scope, 3).unwrap();
        let reclaimed_pages = [5u32, 6];
        let reclaimed = test_reclaimed_pages(&reclaimed_pages).unwrap();
        let (reclamation, _guard) = RetirementReclamation::Reclaimed(reclaimed).into_parts();
        let mut replacement_entries = [];
        let replacements = CommittedReplacementLedger::new(&mut replacement_entries);
        let mut role_entries = [PageRoleIndexSlot::new(); 4];
        let mut roles = PageRoleIndex::new(&mut role_entries);

        roles.prepare(&arena, &replacements).unwrap();
        assert_eq!(
            roles
                .authorize_reclaimed_pages_when_bound(&arena, &reclamation)
                .unwrap_err(),
            RetirementWriteError::ReclaimedPageNotConsumed(6)
        );
    }

    #[test]
    fn replacement_prefix_provenance_converges_without_accepting_current_blob_aliases() {
        let committed = 30u64;
        let mut bytes = image(committed as usize);
        put_retirement_branch(page_mut(&mut bytes, 2), 1, 1, &[(2, 3), (3, 4)]);
        put_retirement_leaf(page_mut(&mut bytes, 4), 1, &[batch(3, 5, 1)]);
        let mut slots = appended_slots(30, 6);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 6, 4).unwrap();
        let mut path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 8];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 6];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 48];
        let mut roles = PageRoleIndex::new(&mut role_storage);

        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 4, 10],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let first = RetirementTreeEditor::upsert_newest(
            &SlicePageSource::new(&bytes, committed),
            state(3, committed, 2, 2),
            token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(replacements.entries().len(), 2);

        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 4, 10, 11],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let second = RetirementTreeEditor::upsert_newest(
            &SlicePageSource::new(&bytes, committed),
            state(3, committed, first.root, first.batch_count),
            token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(second.batch_count, first.batch_count);
        assert_eq!(second.committed_replacements, 0);
        assert_eq!(replacements.entries().len(), 2);
        assert_eq!(arena.in_use_count().unwrap(), 3);

        let mut alias_bytes = image(20);
        put_blob_leaf(page_mut(&mut alias_bytes, 3), 1, &[3]);
        let mut alias_slots = appended_slots(20, 3);
        let mut alias_arena = PrivatePageArena::new(&mut alias_slots, 20, 23, 4).unwrap();
        alias_arena.generation.set(5);
        alias_arena.test_install_page(0, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_leaf(page, 4, &[batch(4, 3, 1)]);
        });
        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[3, 10],
            &mut alias_arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut alias_replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut alias_replacements =
            CommittedReplacementLedger::new(&mut alias_replacement_storage);
        assert!(matches!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&alias_bytes, 20),
                state(3, 20, 20, 1),
                token,
                &mut path,
                &mut alias_replacements,
                &mut releases,
                &mut roles,
            ),
            Err(RetirementWriteError::PageRoleConflict { pgno: 3, .. })
        ));
        assert_eq!(alias_arena.in_use_count().unwrap(), 1);
        assert!(alias_replacements.entries().is_empty());
    }

    #[test]
    fn global_retirement_order_is_carried_across_leaves() {
        let mut bytes = image(40);
        put_retirement_branch(page_mut(&mut bytes, 2), 1, 1, &[(5, 3), (6, 4)]);
        put_retirement_leaf(
            page_mut(&mut bytes, 3),
            1,
            &[batch(2, 10, 1), batch(5, 11, 1)],
        );
        put_retirement_leaf(
            page_mut(&mut bytes, 4),
            1,
            &[batch(4, 12, 1), batch(6, 13, 1)],
        );
        for (pgno, listed) in [(10, 20), (11, 21), (12, 22), (13, 23)] {
            put_blob_leaf(page_mut(&mut bytes, pgno), 1, &[listed]);
        }
        let mut slots = appended_slots(40, 4);
        let mut arena = PrivatePageArena::new(&mut slots, 40, 44, 7).unwrap();
        let mut path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 16];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 4];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 64];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert_eq!(
            RetirementTreeEditor::delete_oldest_prefix(
                &SlicePageSource::new(&bytes, 40),
                state(6, 40, 2, 4),
                3,
                &mut arena,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::RetirementTreeOrder {
                previous: 5,
                current: 4,
            }
        );
        assert_eq!(arena.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());
    }

    #[test]
    fn cross_blob_leaf_overlap_is_rejected() {
        let page_count = 5_000u64;
        let mut bytes = image(page_count as usize);
        put_retirement_leaf(page_mut(&mut bytes, 2), 1, &[batch(2, 3, 1_013)]);
        put_blob_branch(page_mut(&mut bytes, 3), 1, &[(0, 4), (4_048, 5)]);
        let first: Vec<u32> = (1_000..2_012).collect();
        put_blob_leaf_at(page_mut(&mut bytes, 4), 1, 0, &first);
        put_blob_leaf_at(page_mut(&mut bytes, 5), 1, 4_048, &[1_500]);
        let mut slots = appended_slots(page_count as u32, 1);
        let mut arena = PrivatePageArena::new(&mut slots, page_count, page_count + 1, 3).unwrap();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 8];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 1];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = vec![PageRoleIndexSlot::new(); 1_100];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert_eq!(
            RetirementTreeEditor::delete_oldest_prefix(
                &SlicePageSource::new(&bytes, page_count),
                state(2, page_count, 2, 1),
                1,
                &mut arena,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::RetirementStreamOrder {
                previous: 2_011,
                current: 1_500,
            }
        );
        assert!(replacements.entries().is_empty());
    }

    #[test]
    fn role_index_old_hash_collisions_stay_logarithmic_and_allocation_free() {
        fn verify(slots: &[PageRoleIndexSlot], root: usize) -> Option<(u32, u32, u8, usize)> {
            if root == ROLE_NO_INDEX {
                return None;
            }
            let left = verify(slots, slots[root].left);
            let right = verify(slots, slots[root].right);
            if let Some((_, left_max, _, _)) = left {
                assert!(left_max < slots[root].pgno);
            }
            if let Some((right_min, _, _, _)) = right {
                assert!(slots[root].pgno < right_min);
            }
            let left_height = left.map_or(0, |value| value.2);
            let right_height = right.map_or(0, |value| value.2);
            assert!(left_height.abs_diff(right_height) <= 1);
            let height = 1 + left_height.max(right_height);
            assert_eq!(slots[root].height, height);
            Some((
                left.map_or(slots[root].pgno, |value| value.0),
                right.map_or(slots[root].pgno, |value| value.1),
                height,
                1 + left.map_or(0, |value| value.3) + right.map_or(0, |value| value.3),
            ))
        }

        fn run(count: usize) -> (usize, u8) {
            let old_table_len = u32::try_from(count).unwrap();
            let mut storage = vec![PageRoleIndexSlot::new(); count];
            let mut roles = PageRoleIndex::new(&mut storage);
            let ((), allocations) = count_thread_allocations(|| {
                for offset in 0..old_table_len {
                    let pgno = 2 + offset * old_table_len;
                    roles
                        .insert_exclusive(pgno, ROLE_PRIVATE, PageRole::PrivateAuthorization)
                        .unwrap();
                }
            });
            assert_eq!(allocations, 0);
            assert_eq!(roles.used, count);
            let (_, _, height, nodes) = verify(roles.slots, roles.root).unwrap();
            assert_eq!(nodes, count);
            let logarithmic_height_bound =
                2 * u8::try_from(usize::BITS - (count + 1).leading_zeros()).unwrap();
            assert!(height <= logarithmic_height_bound);
            let work = roles.locate_work();
            assert!(work <= count * usize::from(logarithmic_height_bound));
            (work, height)
        }

        let samples = [run(512), run(4_096), run(8_192)];
        assert!(samples[1].0 < samples[0].0 * 12);
        assert!(samples[2].0 < samples[1].0 * 3);
        assert!(samples[1].1 <= samples[0].1 + 6);
        assert!(samples[2].1 <= samples[1].1 + 2);
    }

    fn delete_alias_error(
        bytes: &[u8],
        page_count: u64,
        selected_txn: u64,
        batches: u64,
        delete_count: u64,
    ) -> RetirementWriteError {
        let mut no_slots = [];
        let mut arena =
            PrivatePageArena::new(&mut no_slots, page_count, page_count, selected_txn + 1).unwrap();
        let mut path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 16];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 64];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        RetirementTreeEditor::delete_oldest_prefix(
            &SlicePageSource::new(bytes, page_count),
            state(selected_txn, page_count, 2, batches),
            delete_count,
            &mut arena,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap_err()
    }

    #[test]
    fn role_index_rejects_tree_blob_shared_blob_repeated_tree_and_list_aliases() {
        let mut tree_blob = image(20);
        put_retirement_leaf(page_mut(&mut tree_blob, 2), 1, &[batch(2, 2, 1)]);
        assert!(matches!(
            delete_alias_error(&tree_blob, 20, 2, 1, 1),
            RetirementWriteError::PageRoleConflict { pgno: 2, .. }
        ));

        let mut shared_blob = image(20);
        put_retirement_leaf(
            page_mut(&mut shared_blob, 2),
            1,
            &[batch(2, 4, 1), batch(3, 4, 1)],
        );
        put_blob_leaf(page_mut(&mut shared_blob, 4), 1, &[10]);
        assert!(matches!(
            delete_alias_error(&shared_blob, 20, 3, 2, 2),
            RetirementWriteError::PageRoleConflict { pgno: 4, .. }
        ));

        let mut repeated_tree = image(20);
        put_retirement_branch(page_mut(&mut repeated_tree, 2), 1, 1, &[(2, 3), (3, 3)]);
        put_retirement_leaf(page_mut(&mut repeated_tree, 3), 1, &[batch(2, 4, 1)]);
        put_blob_leaf(page_mut(&mut repeated_tree, 4), 1, &[10]);
        assert!(matches!(
            delete_alias_error(&repeated_tree, 20, 3, 2, 2),
            RetirementWriteError::PageRoleConflict { pgno: 3, .. }
        ));

        for listed in [2u32, 3] {
            let mut bytes = image(20);
            put_retirement_leaf(page_mut(&mut bytes, 2), 1, &[batch(2, 3, 1)]);
            put_blob_leaf(page_mut(&mut bytes, 3), 1, &[listed]);
            assert!(matches!(
                delete_alias_error(&bytes, 20, 2, 1, 1),
                RetirementWriteError::PageRoleConflict { pgno, .. } if pgno == listed
            ));
        }

        let mut repeated_list = image(20);
        put_retirement_leaf(
            page_mut(&mut repeated_list, 2),
            1,
            &[batch(2, 3, 1), batch(3, 4, 1)],
        );
        put_blob_leaf(page_mut(&mut repeated_list, 3), 1, &[10]);
        put_blob_leaf(page_mut(&mut repeated_list, 4), 1, &[10]);
        assert!(matches!(
            delete_alias_error(&repeated_list, 20, 3, 2, 2),
            RetirementWriteError::PageRoleConflict { pgno: 10, .. }
        ));
    }

    #[test]
    fn role_index_rejects_private_list_and_replacement_private_aliases() {
        let mut slots = vec![
            PrivatePageSlot::authorized(7, PrivatePageAuthorization::CommittedFree),
            PrivatePageSlot::authorized(20, PrivatePageAuthorization::Appended),
            PrivatePageSlot::authorized(21, PrivatePageAuthorization::Appended),
        ];
        let mut arena = PrivatePageArena::new(&mut slots, 20, 22, 2).unwrap();
        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[7], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 3];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert!(matches!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&[], 20),
                state(1, 20, 0, 0),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            ),
            Err(RetirementWriteError::PageRoleConflict { pgno: 7, .. })
        ));
        assert_eq!(arena.in_use_count().unwrap(), 0);

        let mut slots = [PrivatePageSlot::authorized(
            3,
            PrivatePageAuthorization::CommittedFree,
        )];
        let mut arena = PrivatePageArena::new(&mut slots, 20, 20, 2).unwrap();
        let mut replacement_storage = [CommittedPageReplacement {
            pgno: 3,
            origin: CommittedPageOrigin::RetirementTree,
        }];
        let mut replacements =
            CommittedReplacementLedger::with_prefix(&mut replacement_storage, 1).unwrap();
        let mut no_release = [];
        let mut releases = PrivateReleaseBuffer::new(&mut no_release);
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert!(matches!(
            RetirementTreeEditor::delete_oldest_prefix(
                &SlicePageSource::new(&[], 20),
                state(1, 20, 0, 0),
                0,
                &mut arena,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            ),
            Err(RetirementWriteError::CommittedReplacementIsPrivate(3))
        ));
    }

    #[test]
    fn committed_parents_cannot_splice_private_tree_or_blob_children() {
        let mut tree_bytes = image(20);
        put_retirement_branch(page_mut(&mut tree_bytes, 2), 1, 1, &[(3, 20)]);
        let mut tree_slots = appended_slots(20, 4);
        let mut tree_arena = PrivatePageArena::new(&mut tree_slots, 20, 24, 4).unwrap();
        tree_arena.test_install_page(0, PrivatePageOrigin::RetirementTree, 1, |page| {
            put_retirement_leaf(page, 4, &[batch(3, 5, 1)]);
        });
        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[10],
            &mut tree_arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 8];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 4];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 32];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&tree_bytes, 20),
                state(3, 20, 2, 1),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::CommittedParentPrivateChild {
                parent: 2,
                child: 20,
            }
        );
        assert_eq!(tree_arena.in_use_count().unwrap(), 1);
        assert!(replacements.entries().is_empty());

        let mut leaf_bytes = image(20);
        put_retirement_leaf(page_mut(&mut leaf_bytes, 2), 1, &[batch(2, 20, 1)]);
        let mut leaf_slots = appended_slots(20, 1);
        let mut leaf_arena = PrivatePageArena::new(&mut leaf_slots, 20, 21, 3).unwrap();
        leaf_arena.test_install_page(0, PrivatePageOrigin::RetirementBlob, 1, |page| {
            put_blob_leaf(page, 3, &[10]);
        });
        let mut no_release = [];
        let mut no_releases = PrivateReleaseBuffer::new(&mut no_release);
        assert_eq!(
            RetirementTreeEditor::delete_oldest_prefix(
                &SlicePageSource::new(&leaf_bytes, 20),
                state(2, 20, 2, 1),
                1,
                &mut leaf_arena,
                &mut path,
                &mut replacements,
                &mut no_releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::CommittedParentPrivateChild {
                parent: 2,
                child: 20,
            }
        );
        assert_eq!(leaf_arena.in_use_count().unwrap(), 1);
        assert!(replacements.entries().is_empty());

        let mut blob_bytes = image(20);
        put_retirement_leaf(page_mut(&mut blob_bytes, 2), 1, &[batch(2, 3, 1)]);
        put_blob_branch(page_mut(&mut blob_bytes, 3), 1, &[(0, 20)]);
        let mut blob_slots = appended_slots(20, 1);
        let mut blob_arena = PrivatePageArena::new(&mut blob_slots, 20, 21, 3).unwrap();
        blob_arena.test_install_page(0, PrivatePageOrigin::RetirementBlob, 1, |page| {
            put_blob_leaf(page, 3, &[10]);
        });
        assert_eq!(
            RetirementTreeEditor::delete_oldest_prefix(
                &SlicePageSource::new(&blob_bytes, 20),
                state(2, 20, 2, 1),
                1,
                &mut blob_arena,
                &mut path,
                &mut replacements,
                &mut no_releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::CommittedParentPrivateChild {
                parent: 3,
                child: 20,
            }
        );
        assert_eq!(blob_arena.in_use_count().unwrap(), 1);
        assert!(replacements.entries().is_empty());
    }

    #[test]
    fn private_cow_parent_accepts_committed_and_private_tree_children() {
        let committed = 20u64;
        let mut bytes = image(committed as usize);
        put_retirement_leaf(page_mut(&mut bytes, 3), 1, &[batch(2, 4, 1)]);
        put_blob_leaf(page_mut(&mut bytes, 4), 1, &[10]);
        put_blob_leaf(page_mut(&mut bytes, 5), 1, &[11]);

        let mut slots = appended_slots(20, 6);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 6, 4).unwrap();
        arena.generation.set(5);
        arena.test_install_page(0, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_branch(page, 4, 1, &[(2, 3), (3, 21)]);
        });
        arena.test_install_page(1, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_leaf(page, 4, &[batch(3, 5, 1)]);
        });

        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[12], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let mut path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 4];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 32];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let result = RetirementTreeEditor::upsert_newest(
            &SlicePageSource::new(&bytes, committed),
            state(3, committed, 20, 2),
            token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(result.batch_count, 3);
        assert!(replacements.entries().is_empty());
        assert_eq!(arena.in_use_count().unwrap(), 3);
    }

    #[test]
    fn old_private_blob_tree_must_have_one_private_generation() {
        let committed = 20u64;
        let mut committed_child = image(committed as usize);
        put_blob_leaf(page_mut(&mut committed_child, 4), 1, &[2]);

        let mut slots = appended_slots(20, 6);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 6, 4).unwrap();
        arena.generation.set(5);
        arena.test_install_page(0, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_leaf(page, 4, &[batch(4, 21, 1)]);
        });
        arena.test_install_page(1, PrivatePageOrigin::RetirementBlob, 5, |page| {
            put_blob_branch(page, 4, &[(0, 4)]);
        });

        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 3, 4],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 6];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 32];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&committed_child, committed),
                state(3, committed, 20, 1),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::PrivateBlobNonPrivateChild {
                parent: 21,
                child: 4,
                expected_generation: 5,
            }
        );
        assert_eq!(arena.in_use_count().unwrap(), 2);
        assert!(replacements.entries().is_empty());

        arena.test_install_page(2, PrivatePageOrigin::RetirementBlob, 6, |page| {
            put_blob_leaf(page, 4, &[2]);
        });
        arena.test_edit_page(1, |page| put_blob_branch(page, 4, &[(0, 22)]));
        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 3],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&committed_child, committed),
                state(3, committed, 20, 1),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::BlobTokenGenerationMismatch(5)
        );
        assert_eq!(arena.in_use_count().unwrap(), 3);
        assert!(replacements.entries().is_empty());
    }

    #[test]
    fn homogeneous_private_and_committed_old_blob_trees_are_accepted() {
        let committed = 20u64;
        let mut slots = appended_slots(20, 6);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 6, 4).unwrap();
        arena.generation.set(5);
        arena.test_install_page(0, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_leaf(page, 4, &[batch(4, 21, 1)]);
        });
        arena.test_install_page(1, PrivatePageOrigin::RetirementBlob, 5, |page| {
            put_blob_branch(page, 4, &[(0, 22)]);
        });
        arena.test_install_page(2, PrivatePageOrigin::RetirementBlob, 5, |page| {
            put_blob_leaf(page, 4, &[2]);
        });

        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 3],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 6];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 32];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let result = RetirementTreeEditor::upsert_newest(
            &SlicePageSource::new(&[], committed),
            state(3, committed, 20, 1),
            token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(result.batch_count, 1);
        assert_eq!(arena.in_use_count().unwrap(), 2);
        assert!(replacements.entries().is_empty());

        let mut bytes = image(committed as usize);
        put_blob_branch(page_mut(&mut bytes, 3), 1, &[(0, 4)]);
        put_blob_leaf(page_mut(&mut bytes, 4), 1, &[2]);
        let mut slots = appended_slots(20, 4);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 4, 4).unwrap();
        arena.generation.set(5);
        arena.test_install_page(0, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_leaf(page, 4, &[batch(4, 3, 1)]);
        });
        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 3],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&bytes, committed),
                state(3, committed, 20, 1),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::RetirementListOmission(4)
        );
        assert_eq!(arena.in_use_count().unwrap(), 1);
        assert!(replacements.entries().is_empty());

        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 3, 4],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let result = RetirementTreeEditor::upsert_newest(
            &SlicePageSource::new(&bytes, committed),
            state(3, committed, 20, 1),
            token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(result.batch_count, 1);
        assert_eq!(arena.in_use_count().unwrap(), 2);
        assert_eq!(
            replacements.entries(),
            &[
                CommittedPageReplacement {
                    pgno: 3,
                    origin: CommittedPageOrigin::RetirementBlob,
                },
                CommittedPageReplacement {
                    pgno: 4,
                    origin: CommittedPageOrigin::RetirementBlob,
                },
            ]
        );
    }

    #[test]
    fn direct_tree_and_blob_references_cannot_be_listed_by_newest_batch() {
        let committed = 30u64;
        let mut bytes = image(committed as usize);
        put_retirement_branch(page_mut(&mut bytes, 2), 1, 1, &[(2, 3), (3, 4)]);
        put_retirement_leaf(page_mut(&mut bytes, 4), 1, &[batch(3, 5, 1)]);
        put_blob_leaf(page_mut(&mut bytes, 5), 1, &[10]);

        for listed in [3u32, 5] {
            let mut slots = appended_slots(30, 3);
            let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 4).unwrap();
            let mut order = [0u32; 1];
            let token = RetirementBlobBuilder::build(
                &[listed],
                &mut arena,
                &mut BlobBuildScratch::new(&mut order),
            )
            .unwrap();
            let mut path = [RetirementPathFrame::new(); 3];
            let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
            let mut release_storage = [0u32; 3];
            let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
            let mut role_storage = [PageRoleIndexSlot::new(); 32];
            let mut roles = PageRoleIndex::new(&mut role_storage);
            assert!(matches!(
                RetirementTreeEditor::upsert_newest(
                    &SlicePageSource::new(&bytes, committed),
                    state(3, committed, 2, 2),
                    token,
                    &mut path,
                    &mut replacements,
                    &mut releases,
                    &mut roles,
                ),
                Err(RetirementWriteError::PageRoleConflict { pgno, .. }) if pgno == listed
            ));
            assert_eq!(arena.in_use_count().unwrap(), 0);
            assert!(replacements.entries().is_empty());
        }

        let mut leaf_bytes = image(committed as usize);
        put_retirement_leaf(
            page_mut(&mut leaf_bytes, 2),
            1,
            &[batch(2, 3, 1), batch(3, 4, 1)],
        );
        let mut slots = appended_slots(30, 2);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 2, 4).unwrap();
        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[3], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 2];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert!(matches!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&leaf_bytes, committed),
                state(3, committed, 2, 2),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            ),
            Err(RetirementWriteError::PageRoleConflict { pgno: 3, .. })
        ));
        assert_eq!(arena.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());
    }

    #[test]
    fn direct_reference_role_budget_is_exact_and_atomic() {
        let committed = 30u64;
        let mut bytes = image(committed as usize);
        put_retirement_branch(page_mut(&mut bytes, 2), 1, 1, &[(2, 3), (3, 4)]);
        put_retirement_leaf(page_mut(&mut bytes, 4), 1, &[batch(3, 5, 1)]);
        put_blob_leaf(page_mut(&mut bytes, 5), 1, &[10]);
        let mut slots = appended_slots(30, 3);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 4).unwrap();
        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 11],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 3];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 7];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&bytes, committed),
                state(3, committed, 2, 2),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::PageRoleIndexTooSmall {
                required: 8,
                actual: 7,
            }
        );
        assert_eq!(arena.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());
    }

    #[test]
    fn combined_replacement_omission_rolls_back_intermediate_and_token_generations() {
        let committed = 20u64;
        let mut bytes = image(committed as usize);
        put_blob_leaf(page_mut(&mut bytes, 3), 1, &[10]);
        let mut slots = appended_slots(20, 7);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 7, 4).unwrap();
        arena.generation.set(5);
        arena.test_install_page(0, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_leaf(page, 4, &[batch(2, 3, 1), batch(4, 21, 3)]);
        });
        arena.test_install_page(1, PrivatePageOrigin::RetirementBlob, 5, |page| {
            put_blob_leaf(page, 4, &[2, 4, 6]);
        });
        let original_tree = arena.test_page_at(0);
        let original_blob = arena.test_page_at(1);

        let mut delete_path = [RetirementPathFrame::new(); 2];
        let mut upsert_path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [
            CommittedPageReplacement {
                pgno: 15,
                origin: CommittedPageOrigin::RetirementTree,
            },
            EMPTY_REPLACEMENT,
            EMPTY_REPLACEMENT,
        ];
        let mut replacements =
            CommittedReplacementLedger::with_prefix(&mut replacement_storage, 1).unwrap();
        let mut release_storage = [19u32, 0, 0, 0, 0, 0, 0];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        releases.len = 1;
        let mut role_storage = [PageRoleIndexSlot::new(); 48];
        let mut roles = PageRoleIndex::new(&mut role_storage);

        for (replacement, omitted) in [
            (&[3u32, 4, 6, 15][..], 2),
            (&[2, 3, 6, 15][..], 4),
            (&[2, 3, 4, 15][..], 6),
            (&[2, 4, 6, 15][..], 3),
            (&[2, 3, 4, 6][..], 15),
        ] {
            let mut order = [0u32; 1];
            let token = RetirementBlobBuilder::build(
                replacement,
                &mut arena,
                &mut BlobBuildScratch::new(&mut order),
            )
            .unwrap();
            assert_eq!(
                RetirementTreeEditor::delete_oldest_and_upsert_newest(
                    &SlicePageSource::new(&bytes, committed),
                    state(3, committed, 20, 2),
                    1,
                    token,
                    &mut delete_path,
                    &mut upsert_path,
                    &mut replacements,
                    &mut releases,
                    &mut roles,
                )
                .unwrap_err(),
                RetirementWriteError::RetirementListOmission(omitted)
            );
            assert_eq!(arena.in_use_count().unwrap(), 2);
            assert_eq!(
                arena.test_state_at(0),
                Some(RetirementPrivatePageState::InUse {
                    origin: PrivatePageOrigin::RetirementTree,
                    generation: 5,
                })
            );
            assert_eq!(
                arena.test_state_at(1),
                Some(RetirementPrivatePageState::InUse {
                    origin: PrivatePageOrigin::RetirementBlob,
                    generation: 5,
                })
            );
            assert_eq!(arena.test_page_at(0), original_tree);
            assert_eq!(arena.test_page_at(1), original_blob);
            assert_eq!(replacements.entries().len(), 1);
            assert_eq!(replacements.entries()[0].pgno, 15);
            assert_eq!(releases.len, 1);
            assert_eq!(releases.pgnos[0], 19);
        }
    }

    #[test]
    fn combined_intermediate_phase_rejects_duplicate_blob_roots() {
        let committed = 30u64;
        let mut bytes = image(committed as usize);
        put_retirement_branch(page_mut(&mut bytes, 2), 1, 1, &[(2, 3), (4, 4)]);
        put_retirement_leaf(page_mut(&mut bytes, 3), 1, &[batch(2, 5, 1)]);
        put_retirement_leaf(
            page_mut(&mut bytes, 4),
            1,
            &[batch(3, 6, 1), batch(4, 6, 1)],
        );
        put_blob_leaf(page_mut(&mut bytes, 5), 1, &[10]);
        put_blob_leaf(page_mut(&mut bytes, 6), 1, &[11]);

        let mut slots = appended_slots(30, 3);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 5).unwrap();
        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 3, 5, 10, 12],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut delete_path = [RetirementPathFrame::new(); 3];
        let mut upsert_path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 8];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 3];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 48];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert!(matches!(
            RetirementTreeEditor::delete_oldest_and_upsert_newest(
                &SlicePageSource::new(&bytes, committed),
                state(4, committed, 2, 3),
                1,
                token,
                &mut delete_path,
                &mut upsert_path,
                &mut replacements,
                &mut releases,
                &mut roles,
            ),
            Err(RetirementWriteError::PageRoleConflict { pgno: 6, .. })
        ));
        assert_eq!(arena.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());
    }

    #[test]
    fn combined_cross_subtree_private_blob_alias_cannot_be_recycled() {
        let committed = 20u64;
        let mut slots = appended_slots(20, 8);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 8, 5).unwrap();
        arena.generation.set(5);
        arena.test_install_page(0, PrivatePageOrigin::RetirementBlob, 5, |page| {
            put_blob_leaf(page, 5, &[10]);
        });
        arena.test_install_page(1, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_branch(page, 5, 1, &[(2, 22), (4, 23)]);
        });
        arena.test_install_page(2, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_leaf(page, 5, &[batch(2, 20, 1)]);
        });
        arena.test_install_page(3, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_leaf(page, 5, &[batch(4, 20, 1)]);
        });
        let original_pages = [
            arena.test_page_at(0),
            arena.test_page_at(1),
            arena.test_page_at(2),
            arena.test_page_at(3),
        ];

        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[11], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let mut delete_path = [RetirementPathFrame::new(); 3];
        let mut upsert_path = [RetirementPathFrame::new(); 3];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 8];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 48];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert!(matches!(
            RetirementTreeEditor::delete_oldest_and_upsert_newest(
                &SlicePageSource::new(&[], committed),
                state(4, committed, 21, 2),
                1,
                token,
                &mut delete_path,
                &mut upsert_path,
                &mut replacements,
                &mut releases,
                &mut roles,
            ),
            Err(RetirementWriteError::PageRoleConflict { pgno: 20, .. })
        ));
        assert_eq!(arena.in_use_count().unwrap(), 4);
        for (slot, expected) in original_pages.into_iter().enumerate() {
            assert_eq!(arena.test_page_at(slot), expected);
            assert!(matches!(
                arena.test_state_at(slot),
                Some(RetirementPrivatePageState::InUse { generation: 5, .. })
            ));
        }
        assert!(replacements.entries().is_empty());
        assert_eq!(releases.len, 0);
    }

    #[test]
    fn combined_level_two_promotion_reselects_retained_multichild_root() {
        let committed = 30u64;
        let mut bytes = image(committed as usize);
        put_retirement_branch(page_mut(&mut bytes, 2), 1, 2, &[(2, 3), (4, 5)]);
        put_retirement_branch(page_mut(&mut bytes, 3), 1, 1, &[(2, 4)]);
        put_retirement_leaf(page_mut(&mut bytes, 4), 1, &[batch(2, 10, 1)]);
        put_retirement_branch(page_mut(&mut bytes, 5), 1, 1, &[(3, 6), (4, 7)]);
        put_retirement_leaf(page_mut(&mut bytes, 6), 1, &[batch(3, 11, 1)]);
        put_retirement_leaf(page_mut(&mut bytes, 7), 1, &[batch(4, 12, 1)]);
        put_blob_leaf(page_mut(&mut bytes, 10), 1, &[14]);

        let mut slots = appended_slots(30, 3);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 5).unwrap();
        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 3, 4, 5, 7, 10, 13],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut delete_path = [RetirementPathFrame::new(); 4];
        let mut upsert_path = [RetirementPathFrame::new(); 4];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 8];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 3];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 64];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let result = RetirementTreeEditor::delete_oldest_and_upsert_newest(
            &SlicePageSource::new(&bytes, committed),
            state(4, committed, 2, 3),
            1,
            token,
            &mut delete_path,
            &mut upsert_path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(result.batch_count, 3);
        assert_eq!(arena.in_use_count().unwrap(), 3);
        assert_eq!(result.committed_replacements, 6);
        assert_eq!(replacements.entries().len(), 6);
    }

    #[test]
    fn combined_reselected_promoted_root_rejects_deeper_backreference() {
        let committed = 30u64;
        let mut bytes = image(committed as usize);
        put_retirement_branch(page_mut(&mut bytes, 2), 1, 3, &[(2, 3), (4, 5)]);
        put_retirement_branch(page_mut(&mut bytes, 3), 1, 2, &[(2, 4)]);
        put_retirement_branch(page_mut(&mut bytes, 4), 1, 1, &[(2, 6)]);
        put_retirement_leaf(page_mut(&mut bytes, 6), 1, &[batch(2, 10, 1)]);
        put_retirement_branch(page_mut(&mut bytes, 5), 1, 2, &[(3, 7), (4, 8)]);
        put_retirement_branch(page_mut(&mut bytes, 8), 1, 1, &[(3, 5), (4, 9)]);
        put_retirement_leaf(page_mut(&mut bytes, 9), 1, &[batch(4, 12, 1)]);
        put_blob_leaf(page_mut(&mut bytes, 10), 1, &[14]);

        let mut slots = appended_slots(30, 4);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 4, 5).unwrap();
        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[13], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let mut delete_path = [RetirementPathFrame::new(); 5];
        let mut upsert_path = [RetirementPathFrame::new(); 5];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 12];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 4];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 64];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert!(matches!(
            RetirementTreeEditor::delete_oldest_and_upsert_newest(
                &SlicePageSource::new(&bytes, committed),
                state(4, committed, 2, 3),
                1,
                token,
                &mut delete_path,
                &mut upsert_path,
                &mut replacements,
                &mut releases,
                &mut roles,
            ),
            Err(RetirementWriteError::PageRoleConflict { pgno: 5, .. })
        ));
        assert_eq!(arena.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());
        assert_eq!(releases.len, 0);
    }

    #[test]
    fn combined_reference_epoch_budget_failure_is_exact_and_atomic() {
        let committed = 20u64;
        let mut bytes = image(committed as usize);
        put_blob_leaf(page_mut(&mut bytes, 3), 1, &[10]);
        let mut slots = appended_slots(20, 6);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 6, 4).unwrap();
        arena.generation.set(5);
        arena.test_install_page(0, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_leaf(page, 4, &[batch(2, 3, 1), batch(4, 21, 1)]);
        });
        arena.test_install_page(1, PrivatePageOrigin::RetirementBlob, 5, |page| {
            put_blob_leaf(page, 4, &[2]);
        });
        let original_tree = arena.test_page_at(0);
        let original_blob = arena.test_page_at(1);
        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 3, 8],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut delete_path = [RetirementPathFrame::new(); 2];
        let mut upsert_path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 4];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 6];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 9];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert_eq!(
            RetirementTreeEditor::delete_oldest_and_upsert_newest(
                &SlicePageSource::new(&bytes, committed),
                state(3, committed, 20, 2),
                1,
                token,
                &mut delete_path,
                &mut upsert_path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::PageRoleIndexTooSmall {
                required: 10,
                actual: 9,
            }
        );
        assert_eq!(arena.in_use_count().unwrap(), 2);
        assert_eq!(arena.test_page_at(0), original_tree);
        assert_eq!(arena.test_page_at(1), original_blob);
        assert!(replacements.entries().is_empty());
        assert_eq!(releases.len, 0);
    }

    #[test]
    fn exact_budget_failures_are_atomic_for_arena_ledger_release_and_roles() {
        let mut bytes = image(20);
        put_retirement_leaf(page_mut(&mut bytes, 2), 1, &[batch(2, 3, 1)]);
        put_blob_leaf(page_mut(&mut bytes, 3), 1, &[10]);

        let mut slots = appended_slots(20, 1);
        let mut arena = PrivatePageArena::new(&mut slots, 20, 21, 3).unwrap();
        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 11],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut no_release = [];
        let mut releases = PrivateReleaseBuffer::new(&mut no_release);
        let mut role_storage = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&bytes, 20),
                state(2, 20, 2, 1),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::PrivatePageBudgetTooSmall {
                required: 2,
                actual: 1,
            }
        );
        assert_eq!(arena.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());

        let mut slots = appended_slots(20, 2);
        let mut arena = PrivatePageArena::new(&mut slots, 20, 22, 3).unwrap();
        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[11], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let mut no_replacement_storage = [];
        let mut no_replacements = CommittedReplacementLedger::new(&mut no_replacement_storage);
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&bytes, 20),
                state(2, 20, 2, 1),
                token,
                &mut path,
                &mut no_replacements,
                &mut releases,
                &mut PageRoleIndex::new(&mut role_storage),
            )
            .unwrap_err(),
            RetirementWriteError::ReplacementLedgerTooSmall {
                required: 1,
                actual: 0,
            }
        );
        assert_eq!(arena.in_use_count().unwrap(), 0);
        assert!(no_replacements.entries().is_empty());

        let mut slots = appended_slots(20, 3);
        let mut arena = PrivatePageArena::new(&mut slots, 20, 23, 3).unwrap();
        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[11], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let mut tiny_roles = [PageRoleIndexSlot::new(); 2];
        let mut roles = PageRoleIndex::new(&mut tiny_roles);
        assert!(matches!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&bytes, 20),
                state(2, 20, 2, 1),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            ),
            Err(RetirementWriteError::PageRoleIndexTooSmall { .. })
        ));
        assert_eq!(arena.in_use_count().unwrap(), 0);
        assert!(replacements.entries().is_empty());
    }

    #[test]
    fn release_budget_failure_preserves_old_private_generation() {
        let committed = 30u64;
        let mut slots = appended_slots(30, 6);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 6, 2).unwrap();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 2];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 32];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let mut order = [0u32; 1];
        let token =
            RetirementBlobBuilder::build(&[2], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let first = RetirementTreeEditor::upsert_newest(
            &SlicePageSource::new(&[], committed),
            state(1, committed, 0, 0),
            token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        assert_eq!(arena.in_use_count().unwrap(), 2);

        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[2, 3],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let mut no_release = [];
        let mut no_releases = PrivateReleaseBuffer::new(&mut no_release);
        assert!(matches!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&[], committed),
                state(1, committed, first.root, 1),
                token,
                &mut path,
                &mut replacements,
                &mut no_releases,
                &mut roles,
            ),
            Err(RetirementWriteError::PrivateReleaseBufferTooSmall { .. })
        ));
        assert_eq!(arena.in_use_count().unwrap(), 2);
        assert!(replacements.entries().is_empty());
    }

    #[test]
    fn opaque_token_runtime_binding_mismatches_roll_back_their_blobs() {
        let mut slots = appended_slots(20, 3);
        let mut arena = PrivatePageArena::new(&mut slots, 20, 23, 2).unwrap();
        let mut order = [0u32; 1];
        let mut token =
            RetirementBlobBuilder::build(&[7], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        token.born_txn = 99;
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 1];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 2];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&[], 20),
                state(1, 20, 0, 0),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::BlobTokenTransactionMismatch {
                expected: 2,
                actual: 99,
            }
        );
        assert_eq!(arena.in_use_count().unwrap(), 0);

        let mut order = [0u32; 1];
        let mut token =
            RetirementBlobBuilder::build(&[8], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        token.generation = 99;
        assert_eq!(
            RetirementTreeEditor::upsert_newest(
                &SlicePageSource::new(&[], 20),
                state(1, 20, 0, 0),
                token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap_err(),
            RetirementWriteError::BlobTokenGenerationMismatch(99)
        );
        assert_eq!(arena.in_use_count().unwrap(), 0);
    }

    #[test]
    fn one_shot_upsert_plan_is_atomic_and_both_phases_allocate_nothing() {
        let committed = 20u64;
        let mut slots = appended_slots(20, 3);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 2).unwrap();
        let mut order = [0u32; 1];
        let mut token =
            RetirementBlobBuilder::build(&[7], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let generation = token.arena.generation.get();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 2];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let source = SlicePageSource::new(&[], committed);

        let (plan, allocations) = count_thread_allocations(|| {
            RetirementTreeEditor::plan_upsert_newest(
                &source,
                state(1, committed, 0, 0),
                &mut token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
        });
        assert_eq!(allocations, 0);
        let plan = plan.unwrap();
        let RetirementEditPlan::Upsert(inner) = &plan else {
            unreachable!()
        };
        assert_eq!(inner.arena.arena().generation.get(), generation);
        assert_eq!(inner.arena.arena().in_use_count().unwrap(), 1);
        assert_eq!(inner.replacements.len, 0);
        assert_eq!(inner.releases.len, 0);
        assert!(!inner.arena.token().unwrap().stabilized);
        let output = &inner.upsert_path.as_deref().unwrap()[0];
        assert_eq!(
            inner
                .arena
                .arena()
                .pool()
                .state(output.destination_slot)
                .unwrap(),
            PrivatePagePoolState::Available
        );

        let (result, allocations) = count_thread_allocations(|| plan.apply());
        assert_eq!(allocations, 0);
        let result = result.unwrap();
        assert_eq!(result.batch_count, 1);
        assert_eq!(token.arena.in_use_count().unwrap(), 2);
        assert!(token.stabilized);
        assert!(replacements.entries().is_empty());
        assert_eq!(releases.len, 0);
    }

    struct ToggleAccessSource<'a> {
        inner: SlicePageSource<'a>,
        fail: Cell<bool>,
    }

    impl CommittedPageSource for ToggleAccessSource<'_> {
        fn check_access(&self) -> Result<(), PageSourceError> {
            if self.fail.get() {
                Err(PageSourceError::ForkedHandle)
            } else {
                Ok(())
            }
        }

        fn read_page(
            &self,
            pgno: u32,
            destination: &mut [u8; PAGE_SIZE],
        ) -> Result<(), PageSourceError> {
            self.inner.read_page(pgno, destination)
        }
    }

    #[test]
    fn one_shot_plan_rejects_stale_source_pool_arena_ledgers_blob_scratch_and_headroom() {
        #[derive(Clone, Copy)]
        enum Drift {
            Pool,
            Arena,
            ReplacementLedger,
            ReleaseLedger,
            Blob,
            Scratch,
            Headroom,
        }

        for drift in [
            Drift::Pool,
            Drift::Arena,
            Drift::ReplacementLedger,
            Drift::ReleaseLedger,
            Drift::Blob,
            Drift::Scratch,
            Drift::Headroom,
        ] {
            let committed = 20u64;
            let mut slots = appended_slots(20, 3);
            let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 2).unwrap();
            let mut order = [0u32; 1];
            let mut token = RetirementBlobBuilder::build(
                &[7],
                &mut arena,
                &mut BlobBuildScratch::new(&mut order),
            )
            .unwrap();
            let mut path = [RetirementPathFrame::new(); 2];
            let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
            let mut release_storage = [0u32; 2];
            let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
            let mut role_storage = [PageRoleIndexSlot::new(); 16];
            let mut roles = PageRoleIndex::new(&mut role_storage);
            let source = SlicePageSource::new(&[], committed);
            let mut plan = RetirementTreeEditor::plan_upsert_newest(
                &source,
                state(1, committed, 0, 0),
                &mut token,
                &mut path,
                &mut replacements,
                &mut releases,
                &mut roles,
            )
            .unwrap();
            let RetirementEditPlan::Upsert(inner) = &mut plan else {
                unreachable!()
            };
            match drift {
                Drift::Pool => {
                    let token = inner.arena.token().unwrap();
                    let _ = inner
                        .arena
                        .arena()
                        .pool()
                        .authority(token.root, PrivatePageOwner::Retirement, token.generation)
                        .unwrap();
                }
                Drift::Arena => inner
                    .arena
                    .arena()
                    .generation
                    .set(inner.arena_generation + 1),
                Drift::ReplacementLedger => inner.replacements.len += 1,
                Drift::ReleaseLedger => inner.releases.len += 1,
                Drift::Blob => match &mut inner.arena {
                    RetirementPlanArena::Token(token) => token.epoch += 1,
                    RetirementPlanArena::Direct(_) => unreachable!(),
                },
                Drift::Scratch => inner.upsert_path.as_deref_mut().unwrap()[0].page[64] ^= 1,
                Drift::Headroom => inner.staged_replacements = usize::MAX,
            }
            let error = plan.apply().unwrap_err();
            match drift {
                Drift::Pool => assert!(matches!(
                    error,
                    RetirementWriteError::StaleEditPlan(RetirementEditBinding::Pool)
                )),
                Drift::Arena => assert_eq!(
                    error,
                    RetirementWriteError::StaleEditPlan(RetirementEditBinding::Arena)
                ),
                Drift::ReplacementLedger => assert_eq!(
                    error,
                    RetirementWriteError::StaleEditPlan(RetirementEditBinding::ReplacementLedger)
                ),
                Drift::ReleaseLedger => assert_eq!(
                    error,
                    RetirementWriteError::StaleEditPlan(RetirementEditBinding::ReleaseLedger)
                ),
                Drift::Blob => assert_eq!(
                    error,
                    RetirementWriteError::StaleEditPlan(RetirementEditBinding::BlobToken)
                ),
                Drift::Scratch => assert_eq!(
                    error,
                    RetirementWriteError::StaleEditPlan(RetirementEditBinding::UpsertScratch)
                ),
                Drift::Headroom => assert_eq!(
                    error,
                    RetirementWriteError::ReplacementLedgerTooSmall {
                        required: usize::MAX,
                        actual: 2,
                    }
                ),
            }
            assert_eq!(token.arena.in_use_count().unwrap(), 1);
            assert!(!token.stabilized);
        }

        let committed = 20u64;
        let mut slots = appended_slots(20, 3);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 2).unwrap();
        let mut order = [0u32; 1];
        let mut token =
            RetirementBlobBuilder::build(&[7], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 2];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let source = ToggleAccessSource {
            inner: SlicePageSource::new(&[], committed),
            fail: Cell::new(false),
        };
        let plan = RetirementTreeEditor::plan_upsert_newest(
            &source,
            state(1, committed, 0, 0),
            &mut token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        source.fail.set(true);
        assert_eq!(
            plan.apply().unwrap_err(),
            RetirementWriteError::Source(PageSourceError::ForkedHandle)
        );
        assert_eq!(token.arena.in_use_count().unwrap(), 1);
        assert!(!token.stabilized);
    }

    #[test]
    fn one_shot_apply_rejects_late_destination_and_release_owner_drift_before_checkpoint() {
        let committed = 20u64;
        let mut slots = appended_slots(20, 3);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 3, 2).unwrap();
        let mut order = [0u32; 1];
        let mut token =
            RetirementBlobBuilder::build(&[7], &mut arena, &mut BlobBuildScratch::new(&mut order))
                .unwrap();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 3];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let source = SlicePageSource::new(&[], committed);
        let mut plan = RetirementTreeEditor::plan_upsert_newest(
            &source,
            state(1, committed, 0, 0),
            &mut token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        let RetirementEditPlan::Upsert(inner) = &mut plan else {
            unreachable!()
        };
        let output = inner.upsert_path.as_deref().unwrap()[0];
        let _ = inner
            .arena
            .arena()
            .pool()
            .claim(output.destination_slot, PrivatePageOwner::Bitmap, 99, 0)
            .unwrap();
        inner.pool_snapshot = inner.arena.arena().capture_fence().unwrap();
        assert!(matches!(
            plan.apply().unwrap_err(),
            RetirementWriteError::PrivatePool(PrivatePagePoolError::PageUnavailable(_))
        ));
        assert_eq!(token.arena.generation.get(), 1);
        assert_eq!(token.arena.in_use_count().unwrap(), 1);
        assert!(!token.stabilized);

        let mut second_slots = appended_slots(30, 4);
        let mut second_arena = PrivatePageArena::new(&mut second_slots, 30, 34, 2).unwrap();
        let mut order = [0u32; 1];
        let token = RetirementBlobBuilder::build(
            &[8],
            &mut second_arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let first = RetirementTreeEditor::upsert_newest(
            &SlicePageSource::new(&[], 30),
            state(1, 30, 0, 0),
            token,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        let generation = second_arena.generation.get();
        let second_source = SlicePageSource::new(&[], 30);
        let mut delete_plan = RetirementTreeEditor::plan_delete_oldest_prefix(
            &second_source,
            state(1, 30, first.root, 1),
            1,
            &mut second_arena,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        let RetirementEditPlan::Delete(inner) = &mut delete_plan else {
            unreachable!()
        };
        let pgno = inner.releases.pgnos[inner.release_binding.len];
        let RetirementPrivatePageState::InUse {
            generation: owner_generation,
            ..
        } = inner.arena.arena().private_state(pgno).unwrap().unwrap();
        let authority = inner
            .arena
            .arena()
            .pool()
            .authority(pgno, PrivatePageOwner::Retirement, owner_generation)
            .unwrap();
        let _ = inner
            .arena
            .arena()
            .pool()
            .transfer(authority, PrivatePageOwner::Bitmap, 99, 0)
            .unwrap();
        inner.pool_snapshot = inner.arena.arena().capture_fence().unwrap();
        assert!(matches!(
            delete_plan.apply().unwrap_err(),
            RetirementWriteError::PrivatePool(PrivatePagePoolError::OwnerMismatch { .. })
        ));
        assert_eq!(second_arena.generation.get(), generation);
    }

    #[test]
    fn combined_plan_uses_virtual_delete_overlay_without_intermediate_mutation() {
        let committed = 20u64;
        let mut bytes = image(committed as usize);
        put_blob_leaf(page_mut(&mut bytes, 3), 1, &[10]);
        let source = SlicePageSource::new(&bytes, committed);
        let mut slots = appended_slots(20, 8);
        let mut arena = PrivatePageArena::new(&mut slots, committed, committed + 8, 4).unwrap();
        arena.generation.set(5);
        arena.test_install_page(0, PrivatePageOrigin::RetirementTree, 5, |page| {
            put_retirement_leaf(page, 4, &[batch(2, 3, 1), batch(4, 21, 1)]);
        });
        arena.test_install_page(1, PrivatePageOrigin::RetirementBlob, 5, |page| {
            put_blob_leaf(page, 4, &[2]);
        });
        let original_tree = arena.test_page_at(0);
        let original_blob = arena.test_page_at(1);
        let mut order = [0u32; 1];
        let mut token = RetirementBlobBuilder::build(
            &[2, 3, 8],
            &mut arena,
            &mut BlobBuildScratch::new(&mut order),
        )
        .unwrap();
        let generation = token.arena.generation.get();
        let in_use = token.arena.in_use_count().unwrap();
        let mut delete_path = [RetirementPathFrame::new(); 2];
        let mut upsert_path = [RetirementPathFrame::new(); 2];
        let mut replacement_storage = [EMPTY_REPLACEMENT; 6];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
        let mut release_storage = [0u32; 8];
        let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
        let mut role_storage = [PageRoleIndexSlot::new(); 48];
        let mut roles = PageRoleIndex::new(&mut role_storage);
        let plan = RetirementTreeEditor::plan_delete_oldest_and_upsert_newest(
            &source,
            state(3, committed, 20, 2),
            1,
            &mut token,
            &mut delete_path,
            &mut upsert_path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        let RetirementEditPlan::Combined(inner) = &plan else {
            unreachable!()
        };
        assert_eq!(inner.arena.arena().generation.get(), generation);
        assert_eq!(inner.arena.arena().in_use_count().unwrap(), in_use);
        let mut actual_tree = [0u8; PAGE_SIZE];
        inner
            .arena
            .arena()
            .read_page_snapshot(
                &inner.pool_snapshot,
                20,
                PrivatePageOrigin::RetirementTree,
                5,
                &mut actual_tree,
            )
            .unwrap();
        let mut actual_blob = [0u8; PAGE_SIZE];
        inner
            .arena
            .arena()
            .read_page_snapshot(
                &inner.pool_snapshot,
                21,
                PrivatePageOrigin::RetirementBlob,
                5,
                &mut actual_blob,
            )
            .unwrap();
        assert_eq!(actual_tree, original_tree);
        assert_eq!(actual_blob, original_blob);
        assert_eq!(inner.replacements.len, 0);
        assert_eq!(inner.releases.len, 0);
        assert_eq!(inner.delete_scratch.len, 1);
        assert_eq!(inner.upsert_scratch.len, 1);
        assert_eq!(
            inner.delete_path.as_deref().unwrap()[0].pgno,
            inner.upsert_path.as_deref().unwrap()[0].pgno
        );
        assert_eq!(inner.result.private_pages, 1);

        let result = plan.apply().unwrap();
        assert_eq!(result.batch_count, 1);
        assert_eq!(result.private_pages, 1);
        assert_eq!(token.arena.in_use_count().unwrap(), 2);
        assert!(token.stabilized);
        assert_eq!(releases.len, 0);
    }

    #[test]
    fn destination_planning_is_linear_and_allocation_free_for_large_sparse_pools() {
        for occupied in [512usize, 4096] {
            let committed = 10_000u64;
            let mut slots = appended_slots(committed as u32, occupied + 3);
            let mut arena =
                PrivatePageArena::new(&mut slots, committed, committed + occupied as u64 + 3, 2)
                    .unwrap();
            for slot in 0..occupied {
                arena.test_install_page(slot, PrivatePageOrigin::RetirementBlob, 99, |page| {
                    page[0] = 1;
                });
            }
            let mut order = [0u32; 1];
            let mut token = RetirementBlobBuilder::build(
                &[7],
                &mut arena,
                &mut BlobBuildScratch::new(&mut order),
            )
            .unwrap();
            let mut path = [RetirementPathFrame::new(); 2];
            let mut replacement_storage = [EMPTY_REPLACEMENT; 2];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
            let mut release_storage = [0u32; 2];
            let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
            let mut role_storage = vec![PageRoleIndexSlot::new(); occupied + 16];
            let mut roles = PageRoleIndex::new(&mut role_storage);
            let source = SlicePageSource::new(&[], committed);
            let (plan, allocations) = count_thread_allocations(|| {
                RetirementTreeEditor::plan_upsert_newest(
                    &source,
                    state(1, committed, 0, 0),
                    &mut token,
                    &mut path,
                    &mut replacements,
                    &mut releases,
                    &mut roles,
                )
            });
            assert_eq!(allocations, 0);
            let plan = plan.unwrap();
            let RetirementEditPlan::Upsert(inner) = &plan else {
                unreachable!()
            };
            assert_eq!(inner.destination_probes, occupied + 2);
        }
    }

    #[test]
    fn scoped_operation_work_is_independent_of_active_foreign_scope_size() {
        let mut measurements = Vec::new();
        for foreign_capacity in [512usize, 4_096] {
            let mut storage = vec![PrivatePageSlot::empty(); foreign_capacity + 3];
            let pool = PrivatePagePool::new_vacant(&mut storage, 10_000, 10_000, 2).unwrap();
            let target = pool.reserve_scope(3).unwrap();
            let foreign = pool.reserve_scope(foreign_capacity).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            for pgno in [8_000, 8_002, 8_004] {
                pool.bind_page(
                    &checkpoint,
                    &target,
                    pgno,
                    PrivatePageAuthorization::CommittedFree,
                )
                .unwrap();
            }
            for pgno in 3..3 + u32::try_from(foreign_capacity).unwrap() {
                pool.bind_page(
                    &checkpoint,
                    &foreign,
                    pgno,
                    PrivatePageAuthorization::CommittedFree,
                )
                .unwrap();
            }
            pool.commit_checkpoint(checkpoint).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.claim_lowest_in_scope(&checkpoint, &foreign, PrivatePageOwner::Bitmap, 2, 0)
                .unwrap();
            pool.commit_checkpoint_in_scope(checkpoint, &foreign)
                .unwrap();

            let mut arena = PrivatePageArena::from_scoped_pool(&pool, &target, 2).unwrap();
            let mut blob_pages = [0u32; 1];
            let token = RetirementBlobBuilder::build(
                &[50],
                &mut arena,
                &mut BlobBuildScratch::new(&mut blob_pages),
            )
            .unwrap();
            let source = SlicePageSource::new(&[], 10_000);
            let mut path = [RetirementPathFrame::new(); 2];
            let mut replacement_entries = [EMPTY_REPLACEMENT; 2];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_entries);
            let mut release_entries = [0u32; 2];
            let mut releases = PrivateReleaseBuffer::new(&mut release_entries);
            let mut role_entries = [PageRoleIndexSlot::new(); 16];
            let mut roles = PageRoleIndex::new(&mut role_entries);

            let before_visits = pool.test_scope_layout_visits();
            pool.test_reset_scope_lookup_probes();
            let (result, allocations) = count_thread_allocations(|| {
                RetirementTreeEditor::upsert_newest(
                    &source,
                    state(1, 10_000, 0, 0),
                    token,
                    &mut path,
                    &mut replacements,
                    &mut releases,
                    &mut roles,
                )
            });
            assert_eq!(allocations, 0);
            assert_eq!(result.unwrap().root, 8_002);
            assert_eq!(pool.scoped_in_use(&target).unwrap(), 2);
            assert_eq!(pool.scoped_in_use(&foreign).unwrap(), 1);
            assert_eq!(
                pool.scoped_available(&foreign).unwrap(),
                foreign_capacity - 1
            );
            measurements.push((
                pool.test_scope_layout_visits() - before_visits,
                pool.test_scope_lookup_probes(),
            ));
        }

        assert_eq!(measurements[0], measurements[1]);
        assert!(measurements[0].1 > 0);
    }

    #[test]
    fn scoped_private_blob_planning_scales_linearly_and_allocates_nothing() {
        fn blob_page_count(values: usize) -> usize {
            let mut nodes = values.div_ceil(RETIREMENT_VALUES_PER_BLOB_LEAF as usize);
            let mut total = nodes;
            while nodes > 1 {
                nodes = nodes.div_ceil(BLOB_BRANCH_CAPACITY);
                total += nodes;
            }
            total
        }

        let mut measurements = Vec::new();
        for listed in [512usize, 4_096] {
            let page_count = 10_000u64;
            let private_blob_pages = blob_page_count(listed);
            let capacity = listed;
            let mut storage = vec![PrivatePageSlot::empty(); capacity];
            let pool =
                PrivatePagePool::new_vacant(&mut storage, page_count, page_count, 2).unwrap();
            let scope = pool.reserve_scope(capacity).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            for pgno in 8_000..8_000 + u32::try_from(private_blob_pages + 1).unwrap() {
                pool.bind_page(
                    &checkpoint,
                    &scope,
                    pgno,
                    PrivatePageAuthorization::CommittedFree,
                )
                .unwrap();
            }
            pool.commit_checkpoint(checkpoint).unwrap();

            let values: Vec<u32> = (100..100 + u32::try_from(listed).unwrap()).collect();
            let mut arena = PrivatePageArena::from_scoped_pool(&pool, &scope, 2).unwrap();
            let mut blob_pages = vec![0u32; private_blob_pages];
            let mut token = RetirementBlobBuilder::build(
                &values,
                &mut arena,
                &mut BlobBuildScratch::new(&mut blob_pages),
            )
            .unwrap();
            assert_eq!(token.private_pages(), private_blob_pages);
            let source = SlicePageSource::new(&[], page_count);
            let mut path = [RetirementPathFrame::new(); 2];
            let mut replacement_entries = [EMPTY_REPLACEMENT; 2];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_entries);
            let mut release_entries = [0u32; 2];
            let mut releases = PrivateReleaseBuffer::new(&mut release_entries);
            let mut role_entries = vec![PageRoleIndexSlot::new(); listed + private_blob_pages + 16];
            let mut roles = PageRoleIndex::new(&mut role_entries);

            pool.test_reset_commitment_work();
            reset_private_snapshot_reads();
            let (plan, plan_allocations) = count_thread_allocations(|| {
                RetirementTreeEditor::plan_upsert_newest(
                    &source,
                    state(1, page_count, 0, 0),
                    &mut token,
                    &mut path,
                    &mut replacements,
                    &mut releases,
                    &mut roles,
                )
            });
            assert_eq!(plan_allocations, 0);
            let plan = plan.unwrap();
            let RetirementEditPlan::Upsert(inner) = &plan else {
                unreachable!()
            };
            assert_eq!(
                inner.result.root,
                8_000 + u32::try_from(private_blob_pages).unwrap()
            );
            assert_eq!(inner.result.batch_count, 1);
            measurements.push((
                private_snapshot_reads(),
                pool.test_commitment_work(),
                plan_allocations,
            ));

            let (result, apply_allocations) = count_thread_allocations(|| plan.apply());
            assert_eq!(apply_allocations, 0);
            assert_eq!(result.unwrap().batch_count, 1);
            assert_eq!(pool.scoped_in_use(&scope).unwrap(), private_blob_pages + 1);
            assert!(token.stabilized);
            assert!(replacements.entries().is_empty());
            assert_eq!(releases.len, 0);
        }

        assert_eq!(measurements, [(1, 3 * 512, 0), (6, 3 * 4_096, 0)]);
    }

    #[test]
    fn committed_blob_callback_sealing_has_linear_total_work() {
        struct CountingSource<'a> {
            inner: SlicePageSource<'a>,
            checks: Cell<usize>,
            reads: Cell<usize>,
        }

        impl CommittedPageSource for CountingSource<'_> {
            fn check_access(&self) -> Result<(), PageSourceError> {
                self.checks.set(self.checks.get() + 1);
                self.inner.check_access()
            }

            fn read_page(
                &self,
                pgno: u32,
                destination: &mut [u8; PAGE_SIZE],
            ) -> Result<(), PageSourceError> {
                self.reads.set(self.reads.get() + 1);
                self.inner.read_page(pgno, destination)
            }
        }

        let mut measurements = Vec::new();
        for listed in [512usize, 4_096] {
            let page_count = listed as u64 + 100;
            let mut bytes = image(page_count as usize);
            let values: Vec<u32> = (20..20 + listed as u32).collect();
            let leaf_count = listed.div_ceil(RETIREMENT_VALUES_PER_BLOB_LEAF as usize);
            let blob_root = 3;
            put_retirement_leaf(page_mut(&mut bytes, 2), 1, &[batch(2, blob_root, listed)]);
            if leaf_count == 1 {
                put_blob_leaf(page_mut(&mut bytes, blob_root), 1, &values);
            } else {
                let children: Vec<(u64, u32)> = (0..leaf_count)
                    .map(|index| {
                        (
                            (index * usize::from(BLOB_LEAF_CAPACITY)) as u64,
                            4 + u32::try_from(index).unwrap(),
                        )
                    })
                    .collect();
                put_blob_branch(page_mut(&mut bytes, blob_root), 1, &children);
                for (index, chunk) in values
                    .chunks(RETIREMENT_VALUES_PER_BLOB_LEAF as usize)
                    .enumerate()
                {
                    put_blob_leaf_at(
                        page_mut(&mut bytes, 4 + u32::try_from(index).unwrap()),
                        1,
                        (index * usize::from(BLOB_LEAF_CAPACITY)) as u64,
                        chunk,
                    );
                }
            }

            let source = CountingSource {
                inner: SlicePageSource::new(&bytes, page_count),
                checks: Cell::new(0),
                reads: Cell::new(0),
            };
            let mut slots = appended_slots(page_count as u32, 1);
            let mut arena =
                PrivatePageArena::new(&mut slots, page_count, page_count + 1, 3).unwrap();
            let mut path = [RetirementPathFrame::new(); 2];
            let mut replacement_storage = vec![EMPTY_REPLACEMENT; leaf_count.saturating_add(3)];
            let mut replacements = CommittedReplacementLedger::new(&mut replacement_storage);
            let mut release_storage = [0u32; 1];
            let mut releases = PrivateReleaseBuffer::new(&mut release_storage);
            let mut role_storage = vec![PageRoleIndexSlot::new(); listed + 32];
            let mut roles = PageRoleIndex::new(&mut role_storage);
            let bounded_capacity = listed + 32 + leaf_count.saturating_add(3) + 1 + path.len();

            reset_content_seal_work();
            reset_callback_guard_work();
            let (plan, allocations) = count_thread_allocations(|| {
                RetirementTreeEditor::plan_delete_oldest_prefix(
                    &source,
                    state(2, page_count, 2, 1),
                    1,
                    &mut arena,
                    &mut path,
                    &mut replacements,
                    &mut releases,
                    &mut roles,
                )
            });
            assert_eq!(allocations, 0);
            let plan = plan.unwrap();
            let RetirementEditPlan::Delete(inner) = &plan else {
                unreachable!()
            };
            assert_eq!(inner.result.root, 0);
            let work = content_seal_work();
            assert!(work <= bounded_capacity * 3);
            let callbacks = source.checks.get() + source.reads.get();
            assert_eq!(callback_guard_work(), callbacks);
            measurements.push((source.reads.get(), work, callback_guard_work()));
        }

        assert_eq!(measurements, [(2, 1_102, 4), (7, 8_278, 9)]);
    }
}
