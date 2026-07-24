//! Fixed-memory reservation and COW mutation of the free-page bitmap.
//!
//! Reservation is a read-only, bounded preflight. It verifies and retains the
//! exact committed bitmap path union, reserves the lowest candidates, and uses
//! appended pages only after verified candidate exhaustion. The resulting COW
//! draft also accepts caller-authorized, strictly ordered direct-free or proven
//! reclamation streams through a complete insertion preflight. It still does not
//! prove reclamation safety, classify retirement, acquire locks, or publish a
//! meta page.

use crate::bitmap_page::{BitmapBranch, BitmapKind, BitmapLeaf, BitmapPageError};
use crate::contract::{
    BITMAP_FANOUT, BITMAP_LEAF_BITS, BITMAP_LEAF_WORDS, MAX_PAGE_COUNT, PAGE_SIZE,
};
use crate::page::{self, PageHeader, PageType};
use crate::page_source::{CommittedPageSource, PageSourceError};
use crate::private_page_pool::{
    PrivatePageAuthority, PrivatePageAuthorization, PrivatePageCompositeBind,
    PrivatePageCompositeBindState, PrivatePageCoordinatorPriorReturn,
    PrivatePageCoordinatorTerminalPage, PrivatePageOwner, PrivatePagePool,
    PrivatePagePoolCommitment, PrivatePagePoolError, PrivatePagePoolSlot, PrivatePagePoolSnapshot,
    PrivatePagePoolState, PrivatePagePreparedSparseReplay, PrivatePageRef,
    PrivatePageReservationScope, PrivatePageReservationScopeSeed, PrivatePageReturn,
    PrivatePageScopedOperation, PrivatePageScopedOperationSlot, PrivatePageScopedSlotInfo,
};
#[cfg(test)]
use crate::retirement_reader::test_reclaim_guard;
use crate::retirement_reader::{RetirementReclaimGuard, RetirementReclamation};
use core::cell::Cell;
use core::sync::atomic::{AtomicU32, AtomicU64, Ordering};

mod selective_finalization;
pub(crate) use selective_finalization::{
    FreeBitmapCoordinatorOutputError, PreparedFreeBitmapCoordinatorRecord,
    PreparedFreeBitmapTerminalExport, SealedFreeBitmapCoordinatorRecord,
    SealedFreeBitmapCoordinatorScratch,
};
#[cfg(test)]
pub(crate) use selective_finalization::{
    FreeBitmapFinalizationCachedPage, FreeBitmapFinalizationScratch,
};

const FREE_PATH_CAPACITY: usize = 4;
const LEAF_LOWER: u16 = 4032;
const BRANCH_LOWER: u16 = 1088;
const SUMMARY_OFFSET: usize = 32;
const CHILDREN_OFFSET: usize = 64;
const BITMAP_SLOT_CANDIDATE: u64 = 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum BitmapPrivatePageState {
    Available,
    InUse { committed_origin: u32 },
    ReleasedFree,
    ReleasedTail,
    Foreign,
}
#[cfg(test)]
use BitmapPrivatePageState as PrivatePageState;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum InsertPageOrigin {
    None,
    Committed,
    Verified(usize),
    Private(usize),
    New,
}

/// Caller-owned retained page image used by one insertion preflight.
///
/// The complete changed path union is prepared here before the COW draft is
/// mutated. Callers may reuse the buffer after application or failure.
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct FreeBitmapInsertPage {
    bytes: [u8; PAGE_SIZE],
    base: u64,
    level: u16,
    source_pgno: u32,
    result_pgno: u32,
    origin: InsertPageOrigin,
    destination_slot: usize,
    changed: bool,
    source_left: usize,
    source_right: usize,
    source_height: u8,
}

impl FreeBitmapInsertPage {
    pub(crate) const fn empty() -> Self {
        Self {
            bytes: [0; PAGE_SIZE],
            base: 0,
            level: 0,
            source_pgno: 0,
            result_pgno: 0,
            origin: InsertPageOrigin::None,
            destination_slot: NO_INDEX,
            changed: false,
            source_left: NO_INDEX,
            source_right: NO_INDEX,
            source_height: 0,
        }
    }
}

const NO_INDEX: usize = usize::MAX;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum IndexedPage {
    Arena(usize),
    Verified(usize),
    PlannedCandidate(usize),
    Replacement,
}

/// Caller-owned node storage for the transaction's page-number AVL index.
/// Preparation initializes only the prefix needed by the transaction.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct BitmapCowIndexNode {
    pgno: u32,
    page: IndexedPage,
    left: usize,
    right: usize,
    height: u8,
    candidate_pgno: u32,
    candidate_index: usize,
    candidate_mapped: bool,
}

impl BitmapCowIndexNode {
    pub(crate) const fn empty() -> Self {
        Self {
            pgno: 0,
            page: IndexedPage::Replacement,
            left: NO_INDEX,
            right: NO_INDEX,
            height: 0,
            candidate_pgno: 0,
            candidate_index: NO_INDEX,
            candidate_mapped: false,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct BitmapCowArenaBinding {
    pool_slot: usize,
    pool_epoch: u64,
    page_number: u32,
    storage_node: usize,
    active_node: usize,
    bound: bool,
}

impl BitmapCowArenaBinding {
    pub(crate) const fn empty() -> Self {
        Self {
            pool_slot: NO_INDEX,
            pool_epoch: 0,
            page_number: 0,
            storage_node: NO_INDEX,
            active_node: NO_INDEX,
            bound: false,
        }
    }
}

/// One physical page number already reserved by the enclosing transaction.
/// The COW engine never obtains page numbers by itself.
pub(crate) type ReservedBitmapPage = PrivatePagePoolSlot;

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct VerifiedBitmapPage {
    pgno: u32,
    bytes: [u8; PAGE_SIZE],
    base: u64,
    level: u16,
    parent: usize,
    remaining: u32,
    survives: bool,
}

impl VerifiedBitmapPage {
    pub(crate) const fn empty() -> Self {
        Self {
            pgno: 0,
            bytes: [0; PAGE_SIZE],
            base: 0,
            level: 0,
            parent: NO_INDEX,
            remaining: 0,
            survives: false,
        }
    }
}

/// Caller-owned fixed-width transaction storage. Only the initialized prefixes
/// participate in duplicate and ownership checks.
#[derive(Debug)]
pub(crate) struct FreeBitmapCowLedger<'a> {
    arena: &'a mut [ReservedBitmapPage],
    replacements: &'a mut [u32],
    replacement_len: usize,
    candidates: &'a mut [u32],
    candidate_len: usize,
    index_nodes: &'a mut [BitmapCowIndexNode],
    available_slots: &'a mut [usize],
}

#[derive(Debug)]
pub(crate) struct SharedFreeBitmapCowLedger<'a> {
    replacements: &'a mut [u32],
    replacement_len: usize,
    candidates: &'a mut [u32],
    candidate_len: usize,
    index_nodes: &'a mut [BitmapCowIndexNode],
    available_slots: &'a mut [usize],
}

#[derive(Debug)]
pub(crate) struct ScopedFreeBitmapCowLedger<'a> {
    arena_bindings: &'a mut [BitmapCowArenaBinding],
    replacements: &'a mut [u32],
    replacement_len: usize,
    candidates: &'a mut [u32],
    candidate_len: usize,
    index_nodes: &'a mut [BitmapCowIndexNode],
    available_slots: &'a mut [usize],
    verified_pages: &'a mut [VerifiedBitmapPage],
    planned_candidate_len: usize,
    reservation_planned: bool,
    payload_page_budget: usize,
    planned_required_private_pages: usize,
}

impl<'a> ScopedFreeBitmapCowLedger<'a> {
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn new(
        arena_bindings: &'a mut [BitmapCowArenaBinding],
        replacements: &'a mut [u32],
        replacement_len: usize,
        candidates: &'a mut [u32],
        candidate_len: usize,
        index_nodes: &'a mut [BitmapCowIndexNode],
        available_slots: &'a mut [usize],
        verified_pages: &'a mut [VerifiedBitmapPage],
        planned_candidate_len: usize,
        reservation_planned: bool,
        payload_page_budget: usize,
        planned_required_private_pages: usize,
    ) -> Self {
        Self {
            arena_bindings,
            replacements,
            replacement_len,
            candidates,
            candidate_len,
            index_nodes,
            available_slots,
            verified_pages,
            planned_candidate_len,
            reservation_planned,
            payload_page_budget,
            planned_required_private_pages,
        }
    }
}

impl<'a> SharedFreeBitmapCowLedger<'a> {
    pub(crate) fn empty(
        replacements: &'a mut [u32],
        candidates: &'a mut [u32],
        index_nodes: &'a mut [BitmapCowIndexNode],
        available_slots: &'a mut [usize],
    ) -> Self {
        Self::with_prefixes(replacements, 0, candidates, 0, index_nodes, available_slots)
    }

    pub(crate) fn with_prefixes(
        replacements: &'a mut [u32],
        replacement_len: usize,
        candidates: &'a mut [u32],
        candidate_len: usize,
        index_nodes: &'a mut [BitmapCowIndexNode],
        available_slots: &'a mut [usize],
    ) -> Self {
        Self {
            replacements,
            replacement_len,
            candidates,
            candidate_len,
            index_nodes,
            available_slots,
        }
    }
}

impl<'a> FreeBitmapCowLedger<'a> {
    pub(crate) fn empty(
        arena: &'a mut [ReservedBitmapPage],
        replacements: &'a mut [u32],
        candidates: &'a mut [u32],
        index_nodes: &'a mut [BitmapCowIndexNode],
        available_slots: &'a mut [usize],
    ) -> Self {
        Self {
            arena,
            replacements,
            replacement_len: 0,
            candidates,
            candidate_len: 0,
            index_nodes,
            available_slots,
        }
    }

    #[cfg(test)]
    fn with_prefixes(
        arena: &'a mut [ReservedBitmapPage],
        replacements: &'a mut [u32],
        replacement_len: usize,
        candidates: &'a mut [u32],
        candidate_len: usize,
        index_nodes: &'a mut [BitmapCowIndexNode],
        available_slots: &'a mut [usize],
    ) -> Self {
        Self {
            arena,
            replacements,
            replacement_len,
            candidates,
            candidate_len,
            index_nodes,
            available_slots,
        }
    }
}

/// Move-only evidence that one page is reserved in this transaction's
/// candidate ledger and no longer appears in its draft free bitmap.
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct ReservedFreePage {
    pgno: u32,
}

impl ReservedFreePage {
    pub(crate) const fn page_number(&self) -> u32 {
        self.pgno
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum FreeBitmapCowError {
    PrivatePool(PrivatePagePoolError),
    SelectedTransactionZero,
    TransactionExhausted,
    PageCountOutOfRange(u64),
    RootPageOutOfBounds(u32),
    Source(PageSourceError),
    Page {
        pgno: u32,
        cause: BitmapPageError,
    },
    UnexpectedPageType {
        pgno: u32,
        page_type: PageType,
    },
    RootLevel {
        expected: u16,
        actual: u16,
    },
    ChildLevel {
        expected: u16,
        actual: u16,
    },
    CoverageOverflow,
    SelectedChildMissing,
    SelectedCoverageOutsideLimit,
    SummaryMismatch,
    CommittedSummaryMismatch(u32),
    RepeatedPathPage(u32),
    RepeatedCommittedPage(u32),
    VerifiedPageIdentityMismatch {
        pgno: u32,
        expected_base: u64,
        actual_base: u64,
        expected_level: u16,
        actual_level: u16,
    },
    LedgerPrefixOutOfBounds,
    LedgerPageOutOfBounds(u32),
    DuplicateArenaPage(u32),
    DuplicateReplacement(u32),
    DuplicateCandidate(u32),
    CandidateOrderRegression {
        previous: u32,
        current: u32,
    },
    LedgerPageConflict(u32),
    IndexCapacityOverflow,
    IndexCapacityTooSmall {
        required: usize,
        actual: usize,
    },
    AvailableSlotCapacityTooSmall {
        required: usize,
        actual: usize,
    },
    InvalidBitmapPoolState {
        pgno: u32,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
    },
    CandidateIsPathPage(u32),
    CandidateAlreadyReserved(u32),
    CandidateIsDraftReplacement(u32),
    CandidateIsArenaPage(u32),
    PlannedCandidateMismatch {
        expected: u32,
        actual: u32,
    },
    PlannedCandidatesRemain {
        remaining: usize,
    },
    ArenaPageConflict(u32),
    CandidateLedgerExhausted,
    ReplacementLedgerExhausted,
    PrivateArenaExhausted,
    InsertPageOutOfBounds(u32),
    InsertPageOrderRegression {
        previous: u32,
        current: u32,
    },
    InsertPageInUse(u32),
    InsertPageIsBitmapPath(u32),
    InsertScratchExhausted {
        required: usize,
        available: usize,
    },
    NonCanonicalRootDemotion,
    InsufficientResourceBudget {
        resource: ReservationResource,
        required: usize,
        available: usize,
    },
    PageSpaceExhausted,
    StaleReservationPredecessor,
    StaleInsertionPlan,
    MutationEpochExhausted,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ReservationResource {
    ArenaPages,
    CandidatePages,
    VerifiedPages,
    IndexNodes,
    ReplacementPages,
    AvailableSlots,
    ArenaBindings,
    PoolValidation,
    SourceNodes,
    ReclamationTicket,
    StagedArenaPages,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct FreeBitmapInsertResult {
    pub(crate) inserted: usize,
    pub(crate) already_free: usize,
    pub(crate) committed_replacements: usize,
    pub(crate) new_bitmap_pages: usize,
    pub(crate) recycled_private_pages: usize,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct UnusedReservationRelease {
    pub(crate) reinserted_candidates: usize,
    pub(crate) reinserted_appended: usize,
    pub(crate) truncated_appended: usize,
    pub(crate) pending_page_count: u64,
}

/// Complete preflight for one ordered insertion stream.
#[derive(Debug)]
struct PreparedFreeBitmapInsertion<'slots, 'plan> {
    pages: &'plan [u32],
    scratch: &'plan mut [FreeBitmapInsertPage],
    scratch_len: usize,
    root: u32,
    governing_page_count: u64,
    destination_count: usize,
    demoted_slots: [usize; FREE_PATH_CAPACITY - 1],
    demoted_len: usize,
    auto_release_pages: [u32; FREE_PATH_CAPACITY - 1],
    auto_release_len: usize,
    auto_reinserted_candidates: usize,
    auto_reinserted_appended: usize,
    inserted: usize,
    already_free: usize,
    committed_replacements: usize,
    new_bitmap_pages: usize,
    pool_snapshot: PrivatePagePoolSnapshot<'slots>,
}

/// Move-only insertion plan bound exclusively to the exact draft it observed.
#[derive(Debug)]
pub(crate) struct PlannedFreeBitmapInsertion<
    'cow,
    'arena,
    'slots,
    'scope,
    'plan,
    S: CommittedPageSource + ?Sized,
> {
    cow: &'cow mut FreeBitmapCow<'arena, 'slots, 'scope, S>,
    prepared: PreparedFreeBitmapInsertion<'slots, 'plan>,
}

impl<S: CommittedPageSource + ?Sized> PlannedFreeBitmapInsertion<'_, '_, '_, '_, '_, S> {
    pub(crate) const fn required_private_pages(&self) -> usize {
        self.prepared.destination_count
    }

    pub(crate) const fn changed_bitmap_pages(&self) -> usize {
        self.prepared.scratch_len
    }

    pub(crate) const fn result_root(&self) -> u32 {
        self.prepared.root
    }

    /// Recheck creator authority, then apply the fully preflighted mutation.
    pub(crate) fn apply(self) -> Result<FreeBitmapInsertResult, FreeBitmapCowError> {
        self.cow
            .committed
            .check_access()
            .map_err(FreeBitmapCowError::Source)?;
        self.cow.preflight_prepared_application(&self.prepared)?;
        Ok(self.cow.apply_prepared_insert(self.prepared))
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum FrameOrigin {
    Committed,
    Verified(usize),
    Private(usize),
}

#[derive(Clone, Copy, Debug)]
struct PathFrame {
    pgno: u32,
    origin: FrameOrigin,
    base: u64,
    level: u16,
    child_index: usize,
    child_count: u16,
}

const EMPTY_FRAME: PathFrame = PathFrame {
    pgno: 0,
    origin: FrameOrigin::Committed,
    base: 0,
    level: 0,
    child_index: 0,
    child_count: 0,
};

struct RemovalPlan {
    frames: [PathFrame; FREE_PATH_CAPACITY],
    snapshots: [[u8; PAGE_SIZE]; FREE_PATH_CAPACITY],
    survives: [bool; FREE_PATH_CAPACITY],
    clone_slots: [Option<usize>; FREE_PATH_CAPACITY],
    len: usize,
    candidate: u32,
    clone_count: usize,
}

/// Move-only evidence that the complete planned scope was validated immediately
/// before a callback-free removal batch.
#[derive(Debug)]
struct PreparedPlannedRemovalBatch {
    target: usize,
    scoped: bool,
}

#[derive(Clone, Copy, Debug)]
struct ReservationCursorFrame {
    verified: usize,
    next: usize,
}

const EMPTY_CURSOR_FRAME: ReservationCursorFrame = ReservationCursorFrame {
    verified: NO_INDEX,
    next: 0,
};

#[derive(Debug)]
pub(crate) struct FreeBitmapReservationBuffers<'a> {
    pub(crate) arena: &'a mut [ReservedBitmapPage],
    pub(crate) pool_validation: &'a mut [PrivatePageCompositeBind],
    pub(crate) arena_bindings: &'a mut [BitmapCowArenaBinding],
    pub(crate) candidates: &'a mut [u32],
    pub(crate) verified_pages: &'a mut [VerifiedBitmapPage],
    pub(crate) replacements: &'a mut [u32],
    pub(crate) index_nodes: &'a mut [BitmapCowIndexNode],
    pub(crate) available_slots: &'a mut [usize],
    pub(crate) source_nodes: &'a mut [FreeBitmapReservationSourceNode],
    pub(crate) reclamation: &'a FreeBitmapReclamationTicket,
    pub(crate) stage: FreeBitmapReservationStageBuffers<'a>,
}

#[derive(Debug)]
pub(crate) struct FreeBitmapReservationStageBuffers<'a> {
    pub(crate) arena: &'a mut [ReservedBitmapPage],
    pub(crate) arena_bindings: &'a mut [BitmapCowArenaBinding],
    pub(crate) candidates: &'a mut [u32],
    pub(crate) verified_pages: &'a mut [VerifiedBitmapPage],
    pub(crate) replacements: &'a mut [u32],
    pub(crate) index_nodes: &'a mut [BitmapCowIndexNode],
    pub(crate) available_slots: &'a mut [usize],
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum FreeBitmapReservationSourceKind {
    Committed,
    Reclaimed,
    Appended,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct FreeBitmapReservationSourceNode {
    pgno: u32,
    kind: FreeBitmapReservationSourceKind,
    required: usize,
}

impl FreeBitmapReservationSourceNode {
    pub(crate) const fn empty() -> Self {
        Self {
            pgno: 0,
            kind: FreeBitmapReservationSourceKind::Committed,
            required: 0,
        }
    }
}

#[derive(Debug)]
pub(crate) struct FreeBitmapReclamationTicket {
    nonce: Cell<u64>,
    selected_txn: Cell<u64>,
    committed_page_count: Cell<u64>,
    root: Cell<u32>,
    fingerprint: Cell<u64>,
    selection_id: Cell<u64>,
    page_count: Cell<usize>,
    pages_fingerprint: Cell<u64>,
    state: AtomicU32,
}

impl FreeBitmapReclamationTicket {
    pub(crate) const fn new() -> Self {
        Self {
            nonce: Cell::new(0),
            selected_txn: Cell::new(0),
            committed_page_count: Cell::new(0),
            root: Cell::new(0),
            fingerprint: Cell::new(0),
            selection_id: Cell::new(0),
            page_count: Cell::new(0),
            pages_fingerprint: Cell::new(0),
            state: AtomicU32::new(0),
        }
    }
}

static FREE_BITMAP_RECLAMATION_NONCE: AtomicU64 = AtomicU64::new(0);

fn mint_free_bitmap_reclamation_nonce(counter: &AtomicU64) -> Option<u64> {
    let mut current = counter.load(Ordering::Relaxed);
    loop {
        let next = current.checked_add(1)?;
        match counter.compare_exchange_weak(current, next, Ordering::AcqRel, Ordering::Relaxed) {
            Ok(_) => return Some(next),
            Err(actual) => current = actual,
        }
    }
}

/// Move-only verifier request bound to one exact initial predecessor.
#[derive(Debug)]
pub(crate) struct FreeBitmapReclamationRequest<'a> {
    nonce: u64,
    selected_txn: u64,
    committed_page_count: u64,
    root: u32,
    fingerprint: u64,
    ticket: &'a FreeBitmapReclamationTicket,
}

/// Move-only verifier proof. The binder consumes it before its final callback.
///
/// The retained reclaim guard keeps the exact live operation barrier held until
/// the resulting bound reservation is finalized or discarded.
#[derive(Debug)]
pub(crate) struct FreeBitmapReclamationProof<'ticket, 'pages, 'barrier> {
    nonce: u64,
    selection_id: u64,
    pages: &'pages [u32],
    fingerprint: u64,
    ticket: &'ticket FreeBitmapReclamationTicket,
    reclaim_guard: RetirementReclaimGuard<'barrier>,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct FreeBitmapReservationBinding {
    pub(crate) committed: usize,
    pub(crate) reclaimed: usize,
    pub(crate) appended: usize,
}

#[cfg(test)]
#[derive(Debug)]
pub(crate) struct PlannedFreeBitmapReservation<'a, S: CommittedPageSource + ?Sized> {
    committed: &'a S,
    selected_txn: u64,
    pending_txn: u64,
    committed_page_count: u64,
    pending_page_count: u64,
    root: u32,
    payload_pages: usize,
    candidate_len: usize,
    appended_len: usize,
    required_private_pages: usize,
    verified_len: usize,
    index_root: usize,
    index_len: usize,
    buffers: FreeBitmapReservationBuffers<'a>,
}

#[derive(Debug)]
pub(crate) struct FreeBitmapReservationCapacityBuffers<'a> {
    pool_validation: &'a mut [PrivatePageCompositeBind],
    arena_bindings: &'a mut [BitmapCowArenaBinding],
    candidates: &'a mut [u32],
    verified_pages: &'a mut [VerifiedBitmapPage],
    replacements: &'a mut [u32],
    index_nodes: &'a mut [BitmapCowIndexNode],
    available_slots: &'a mut [usize],
    source_nodes: &'a mut [FreeBitmapReservationSourceNode],
    reclamation: &'a FreeBitmapReclamationTicket,
    stage: FreeBitmapReservationStageBuffers<'a>,
}

/// Pool-independent capacity and committed-source authority. It owns no live
/// pool, scope, physical page number, or legacy all-bound arena.
#[derive(Debug)]
pub(crate) struct FreeBitmapReservationCapacityPlan<'a, S: CommittedPageSource + ?Sized> {
    committed: &'a S,
    selected_txn: u64,
    pending_txn: u64,
    committed_page_count: u64,
    governing_page_count: u64,
    root: u32,
    payload_pages: usize,
    private_pages: usize,
    candidate_len: usize,
    verified_len: usize,
    index_required: usize,
    capacity_fingerprint: u64,
    source_fingerprint: u64,
    buffers: FreeBitmapReservationCapacityBuffers<'a>,
}

/// A physical bitmap plan that cannot outlive the verified live-reader
/// authority which permitted its page selection.
#[derive(Debug)]
pub(crate) struct LockedFreeBitmapReservationPlan<
    'a,
    'selection,
    'barrier,
    'pages,
    S: CommittedPageSource + ?Sized,
> {
    plan: FreeBitmapReservationCapacityPlan<'a, S>,
    reclamation: RetirementReclamation<'selection, 'barrier, 'pages>,
}

#[derive(Debug)]
pub(crate) struct FreeBitmapReservationAttachment<
    'a,
    'slots,
    'scope,
    S: CommittedPageSource + ?Sized,
> {
    cow: FreeBitmapCow<'a, 'slots, 'scope, S>,
    scope: &'a PrivatePageReservationScope<'scope>,
    private_pages: usize,
    payload_pages: usize,
    source_nodes: &'a mut [FreeBitmapReservationSourceNode],
    pool_validation: &'a mut [PrivatePageCompositeBind],
    stage: FreeBitmapReservationStageBuffers<'a>,
    ticket: &'a FreeBitmapReclamationTicket,
    nonce: u64,
    capacity_fingerprint: u64,
    source_fingerprint: u64,
    pool_commitment: PrivatePagePoolCommitment,
}

#[derive(Debug)]
pub(crate) struct BoundFreeBitmapReservation<
    'a,
    'slots,
    'scope,
    'barrier,
    S: CommittedPageSource + ?Sized,
> {
    pub(crate) cow: FreeBitmapCow<'a, 'slots, 'scope, S>,
    pub(crate) binding: FreeBitmapReservationBinding,
    private_pages: usize,
    source_nodes: &'a mut [FreeBitmapReservationSourceNode],
    pool_validation: &'a mut [PrivatePageCompositeBind],
    stage: FreeBitmapReservationStageBuffers<'a>,
    reclaim_guard: RetirementReclaimGuard<'barrier>,
}

#[cfg(test)]
impl<'a, S: CommittedPageSource + ?Sized> PlannedFreeBitmapReservation<'a, S> {
    pub(crate) fn candidates(&self) -> &[u32] {
        &self.buffers.candidates[..self.candidate_len]
    }

    pub(crate) const fn appended_pages(&self) -> usize {
        self.appended_len
    }

    pub(crate) const fn verified_path_pages(&self) -> usize {
        self.verified_len
    }

    pub(crate) const fn pending_page_count(&self) -> u64 {
        self.pending_page_count
    }
}

fn reservation_slices_overlap<A, B>(left: &[A], right: &[B]) -> bool {
    if left.is_empty() || right.is_empty() {
        return false;
    }
    let left_start = left.as_ptr() as usize;
    let right_start = right.as_ptr() as usize;
    let Some(left_bytes) = core::mem::size_of::<A>().checked_mul(left.len()) else {
        return true;
    };
    let Some(right_bytes) = core::mem::size_of::<B>().checked_mul(right.len()) else {
        return true;
    };
    let Some(left_end) = left_start.checked_add(left_bytes) else {
        return true;
    };
    let Some(right_end) = right_start.checked_add(right_bytes) else {
        return true;
    };
    left_start < right_end && right_start < left_end
}

fn late_binding_pool_error(error: PrivatePagePoolError) -> FreeBitmapCowError {
    match error {
        PrivatePagePoolError::EpochExhausted => FreeBitmapCowError::MutationEpochExhausted,
        other => FreeBitmapCowError::PrivatePool(other),
    }
}

impl<'a, S: CommittedPageSource + ?Sized> FreeBitmapReservationCapacityPlan<'a, S> {
    fn validate(&self) -> Result<(), FreeBitmapCowError> {
        if self.selected_txn == 0
            || self.selected_txn == u64::MAX
            || !(2..=MAX_PAGE_COUNT).contains(&self.committed_page_count)
            || self.private_pages == 0
            || self.candidate_len > self.buffers.candidates.len()
            || self.candidate_len > self.buffers.source_nodes.len()
            || self.verified_len > self.buffers.verified_pages.len()
            || self.private_pages > self.buffers.pool_validation.len()
            || self.private_pages > self.buffers.arena_bindings.len()
            || self.private_pages > self.buffers.available_slots.len()
            || self.verified_len > self.buffers.replacements.len()
            || self.index_required > self.buffers.index_nodes.len()
        {
            return Err(FreeBitmapCowError::StaleInsertionPlan);
        }
        if reservation_capacity_fingerprint(
            &self.buffers.candidates[..self.candidate_len],
            &self.buffers.verified_pages[..self.verified_len],
        ) != self.capacity_fingerprint
            || reservation_source_fingerprint(&self.buffers.source_nodes[..self.candidate_len])
                != self.source_fingerprint
        {
            return Err(FreeBitmapCowError::StaleInsertionPlan);
        }
        for (index, node) in self.buffers.source_nodes[..self.candidate_len]
            .iter()
            .enumerate()
        {
            if !matches!(
                node.kind,
                FreeBitmapReservationSourceKind::Committed
                    | FreeBitmapReservationSourceKind::Appended
            ) || node.pgno != self.buffers.candidates[index]
                || (node.kind == FreeBitmapReservationSourceKind::Committed)
                    != (u64::from(node.pgno) < self.committed_page_count)
                || node.required < self.payload_pages
                || node.required > self.private_pages
            {
                return Err(FreeBitmapCowError::StaleInsertionPlan);
            }
        }
        let stage = &self.buffers.stage;
        if stage.arena.len() < self.private_pages
            || stage.arena_bindings.len() < self.private_pages
            || stage.candidates.len() < self.candidate_len
            || stage.verified_pages.len() < self.verified_len
            || stage.replacements.len() < self.verified_len
            || stage.index_nodes.len() < self.index_required
            || stage.available_slots.len() < self.private_pages
            || reservation_slices_overlap(
                &self.buffers.arena_bindings[..self.private_pages],
                &stage.arena_bindings[..self.private_pages],
            )
            || reservation_slices_overlap(
                &self.buffers.index_nodes[..self.index_required],
                &stage.index_nodes[..self.index_required],
            )
            || reservation_slices_overlap(
                &self.buffers.available_slots[..self.private_pages],
                &stage.available_slots[..self.private_pages],
            )
            || reservation_slices_overlap(
                &self.buffers.candidates[..self.candidate_len],
                &stage.candidates[..self.candidate_len],
            )
            || reservation_slices_overlap(
                &self.buffers.verified_pages[..self.verified_len],
                &stage.verified_pages[..self.verified_len],
            )
            || reservation_slices_overlap(
                &self.buffers.replacements[..self.verified_len],
                &stage.replacements[..self.verified_len],
            )
        {
            return Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::StagedArenaPages,
                required: self.private_pages,
                available: stage.arena.len(),
            });
        }
        Ok(())
    }

    pub(crate) const fn required_private_pages(&self) -> usize {
        self.private_pages
    }

    pub(crate) fn candidates(&self) -> &[u32] {
        &self.buffers.candidates[..self.candidate_len]
    }

    pub(crate) fn attach<'slots, 'scope>(
        self,
        pool: &'a PrivatePagePool<'slots>,
        scope: &'a PrivatePageReservationScope<'scope>,
    ) -> Result<
        (
            FreeBitmapReservationAttachment<'a, 'slots, 'scope, S>,
            FreeBitmapReclamationRequest<'a>,
        ),
        FreeBitmapCowError,
    > {
        self.attach_at_predecessor(pool, scope, true)
    }

    pub(crate) fn attach_current_draft<'slots, 'scope>(
        self,
        pool: &'a PrivatePagePool<'slots>,
        scope: &'a PrivatePageReservationScope<'scope>,
    ) -> Result<
        (
            FreeBitmapReservationAttachment<'a, 'slots, 'scope, S>,
            FreeBitmapReclamationRequest<'a>,
        ),
        FreeBitmapCowError,
    > {
        self.attach_at_predecessor(pool, scope, false)
    }

    fn attach_at_predecessor<'slots, 'scope>(
        self,
        pool: &'a PrivatePagePool<'slots>,
        scope: &'a PrivatePageReservationScope<'scope>,
        initial: bool,
    ) -> Result<
        (
            FreeBitmapReservationAttachment<'a, 'slots, 'scope, S>,
            FreeBitmapReclamationRequest<'a>,
        ),
        FreeBitmapCowError,
    > {
        self.validate()?;
        if initial
            && (self.pending_txn != self.selected_txn + 1
                || self.governing_page_count != self.committed_page_count)
        {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        if pool
            .backing_overlaps(&self.buffers.stage.arena[..self.private_pages])
            .map_err(FreeBitmapCowError::PrivatePool)?
        {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }
        if pool.pending_txn() != self.pending_txn
            || pool.committed_page_count() != self.committed_page_count
            || pool.pending_page_count() != self.governing_page_count
        {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        for &pgno in &self.buffers.candidates[..self.candidate_len] {
            if pool
                .find_bound_page(pgno)
                .map_err(FreeBitmapCowError::PrivatePool)?
                .is_some()
            {
                return Err(FreeBitmapCowError::StaleReservationPredecessor);
            }
        }
        if initial {
            for page in &self.buffers.verified_pages[..self.verified_len] {
                if pool
                    .find_bound_page(page.pgno)
                    .map_err(FreeBitmapCowError::PrivatePool)?
                    .is_some()
                {
                    return Err(FreeBitmapCowError::StaleReservationPredecessor);
                }
            }
        }
        let scope_status = pool
            .scope_status(scope)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        if scope_status.capacity != self.private_pages || scope_status.bound != 0 {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        pool.validate_vacant_scope_bind_order(scope)
            .map_err(|_| FreeBitmapCowError::StaleReservationPredecessor)?;
        let before = pool
            .exact_commitment(scope)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        let nonce = mint_free_bitmap_reclamation_nonce(&FREE_BITMAP_RECLAMATION_NONCE)
            .ok_or(FreeBitmapCowError::MutationEpochExhausted)?;
        let FreeBitmapReservationCapacityPlan {
            committed,
            selected_txn,
            pending_txn,
            committed_page_count,
            governing_page_count,
            root,
            payload_pages,
            private_pages,
            candidate_len,
            verified_len,
            index_required,
            capacity_fingerprint,
            source_fingerprint,
            buffers,
        } = self;
        let FreeBitmapReservationCapacityBuffers {
            pool_validation,
            arena_bindings,
            candidates,
            verified_pages,
            replacements,
            index_nodes,
            available_slots,
            source_nodes,
            reclamation,
            stage,
        } = buffers;
        let ledger = ScopedFreeBitmapCowLedger::new(
            &mut arena_bindings[..private_pages],
            &mut replacements[..verified_len],
            0,
            &mut candidates[..candidate_len],
            0,
            &mut index_nodes[..index_required],
            &mut available_slots[..private_pages],
            &mut verified_pages[..verified_len],
            candidate_len,
            true,
            payload_pages,
            private_pages,
        );
        let mut cow = FreeBitmapCow::from_scoped_pool_with_pending_txn(
            committed,
            selected_txn,
            pending_txn,
            governing_page_count,
            root,
            pool,
            scope,
            ledger,
        )?;
        cow.select_planned_candidate_prefix(0)?;
        cow.validate_scoped_bindings()?;
        pool.validate_exact_commitment(scope, &before)
            .map_err(|_| FreeBitmapCowError::StaleReservationPredecessor)?;
        let pool_commitment = pool
            .exact_commitment(scope)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        let fingerprint = reservation_hash_u64(
            reservation_hash_u64(capacity_fingerprint, source_fingerprint),
            nonce,
        );
        reclamation.nonce.set(nonce);
        reclamation.selected_txn.set(selected_txn);
        reclamation.committed_page_count.set(committed_page_count);
        reclamation.root.set(root);
        reclamation.fingerprint.set(fingerprint);
        reclamation.selection_id.set(0);
        reclamation.page_count.set(0);
        reclamation.pages_fingerprint.set(0);
        reclamation.state.store(1, Ordering::Release);
        let request = FreeBitmapReclamationRequest {
            nonce,
            selected_txn,
            committed_page_count,
            root,
            fingerprint,
            ticket: reclamation,
        };
        Ok((
            FreeBitmapReservationAttachment {
                cow,
                scope,
                private_pages,
                payload_pages,
                source_nodes,
                pool_validation,
                stage,
                ticket: reclamation,
                nonce,
                capacity_fingerprint,
                source_fingerprint,
                pool_commitment,
            },
            request,
        ))
    }
}

impl<'a, 'selection, 'barrier, 'pages, S: CommittedPageSource + ?Sized>
    LockedFreeBitmapReservationPlan<'a, 'selection, 'barrier, 'pages, S>
{
    pub(crate) const fn required_private_pages(&self) -> usize {
        self.plan.required_private_pages()
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn bind<'slots, 'scope>(
        self,
        pool: &'a PrivatePagePool<'slots>,
        scope: &'a PrivatePageReservationScope<'scope>,
    ) -> Result<BoundFreeBitmapReservation<'a, 'slots, 'scope, 'barrier, S>, FreeBitmapCowError>
    {
        let Self { plan, reclamation } = self;
        let (attachment, request) = plan.attach(pool, scope)?;
        let proof = complete_free_bitmap_reclamation(request, reclamation)?;
        attachment.bind(proof)
    }
}

/// Completes bitmap late binding from a verified retirement result. Normal
/// allocator code cannot supply an arbitrary page slice or detach the result
/// from the operation barrier that made it safe.
pub(crate) fn complete_free_bitmap_reclamation<'ticket, 'selection, 'barrier, 'pages>(
    request: FreeBitmapReclamationRequest<'ticket>,
    reclamation: RetirementReclamation<'selection, 'barrier, 'pages>,
) -> Result<FreeBitmapReclamationProof<'ticket, 'pages, 'barrier>, FreeBitmapCowError> {
    let (selection_id, pages, reclaim_guard) = reclamation.into_parts();
    complete_free_bitmap_reclamation_pages(request, selection_id, pages, reclaim_guard)
}

fn complete_free_bitmap_reclamation_pages<'ticket, 'pages, 'barrier>(
    request: FreeBitmapReclamationRequest<'ticket>,
    selection_id: u64,
    pages: &'pages [u32],
    reclaim_guard: RetirementReclaimGuard<'barrier>,
) -> Result<FreeBitmapReclamationProof<'ticket, 'pages, 'barrier>, FreeBitmapCowError> {
    let ticket = request.ticket;
    if request.nonce == 0
        || (pages.is_empty() != (selection_id == 0))
        || ticket.nonce.get() != request.nonce
        || ticket.selected_txn.get() != request.selected_txn
        || ticket.committed_page_count.get() != request.committed_page_count
        || ticket.root.get() != request.root
        || ticket.fingerprint.get() != request.fingerprint
        || ticket
            .state
            .compare_exchange(1, 2, Ordering::AcqRel, Ordering::Acquire)
            .is_err()
    {
        return Err(FreeBitmapCowError::StaleInsertionPlan);
    }
    let fingerprint = reservation_pages_fingerprint(pages);
    ticket.selection_id.set(selection_id);
    ticket.page_count.set(pages.len());
    ticket.pages_fingerprint.set(fingerprint);
    Ok(FreeBitmapReclamationProof {
        nonce: request.nonce,
        selection_id,
        pages,
        fingerprint,
        ticket,
        reclaim_guard,
    })
}

#[cfg(test)]
fn complete_free_bitmap_reclamation_for_test<'ticket, 'pages>(
    request: FreeBitmapReclamationRequest<'ticket>,
    selection_id: u64,
    pages: &'pages [u32],
) -> Result<FreeBitmapReclamationProof<'ticket, 'pages, 'static>, FreeBitmapCowError> {
    complete_free_bitmap_reclamation_pages(request, selection_id, pages, test_reclaim_guard())
}

struct CallbackFreeCommittedSource;

impl CommittedPageSource for CallbackFreeCommittedSource {
    fn read_page(&self, pgno: u32, _: &mut [u8; PAGE_SIZE]) -> Result<(), PageSourceError> {
        Err(PageSourceError::PageOutOfBounds(pgno))
    }
}

static CALLBACK_FREE_COMMITTED_SOURCE: CallbackFreeCommittedSource = CallbackFreeCommittedSource;

impl<'a, 'slots, 'scope, S: CommittedPageSource + ?Sized>
    FreeBitmapReservationAttachment<'a, 'slots, 'scope, S>
{
    pub(crate) fn bind<'ticket, 'pages, 'barrier>(
        mut self,
        proof: FreeBitmapReclamationProof<'ticket, 'pages, 'barrier>,
    ) -> Result<BoundFreeBitmapReservation<'a, 'slots, 'scope, 'barrier, S>, FreeBitmapCowError>
    {
        if !core::ptr::eq(proof.ticket, self.ticket)
            || proof.nonce != self.nonce
            || proof.nonce == 0
            || (proof.pages.is_empty() != (proof.selection_id == 0))
            || reservation_pages_fingerprint(proof.pages) != proof.fingerprint
            || self.ticket.nonce.get() != self.nonce
            || self.ticket.selected_txn.get() != self.cow.selected_txn
            || self.ticket.committed_page_count.get() != self.cow.committed_page_count
            || self.ticket.root.get() != self.cow.root
            || self.ticket.selection_id.get() != proof.selection_id
            || self.ticket.page_count.get() != proof.pages.len()
            || self.ticket.pages_fingerprint.get() != proof.fingerprint
            || self.ticket.fingerprint.get()
                != reservation_hash_u64(
                    reservation_hash_u64(self.capacity_fingerprint, self.source_fingerprint),
                    self.nonce,
                )
            || self.ticket.state.load(Ordering::Acquire) != 2
        {
            return Err(FreeBitmapCowError::StaleInsertionPlan);
        }

        let pool_commitment = self.pool_commitment;
        self.cow
            .pool()
            .validate_exact_commitment(self.scope, &pool_commitment)
            .map_err(|_| FreeBitmapCowError::StaleInsertionPlan)?;
        if self
            .ticket
            .state
            .compare_exchange(2, 3, Ordering::AcqRel, Ordering::Acquire)
            .is_err()
        {
            return Err(FreeBitmapCowError::StaleInsertionPlan);
        }
        let source_status = self.cow.committed.check_access();
        // The callback above is deliberately the last source call. Everything
        // below reauthenticates live state and uses verified/staged bytes only.
        self.cow
            .pool()
            .validate_exact_commitment(self.scope, &pool_commitment)
            .map_err(|_| FreeBitmapCowError::StaleInsertionPlan)?;
        if self.ticket.state.load(Ordering::Acquire) != 3
            || self.ticket.nonce.get() != self.nonce
            || self.ticket.selection_id.get() != proof.selection_id
            || self.ticket.page_count.get() != proof.pages.len()
            || self.ticket.pages_fingerprint.get() != proof.fingerprint
        {
            return Err(FreeBitmapCowError::StaleInsertionPlan);
        }
        source_status.map_err(FreeBitmapCowError::Source)?;
        self.cow.validate_scoped_bindings()?;
        let candidate_len = self.cow.planned_candidate_len;
        let verified_len = self.cow.verified_pages.len();
        if reservation_capacity_fingerprint(
            &self.cow.candidates[..candidate_len],
            self.cow.verified_pages,
        ) != self.capacity_fingerprint
            || reservation_source_fingerprint(&self.source_nodes[..candidate_len])
                != self.source_fingerprint
        {
            return Err(FreeBitmapCowError::StaleInsertionPlan);
        }
        for scratch in [
            &self.cow.candidates[..] as &[u32],
            &self.cow.replacements[..] as &[u32],
            &self.stage.candidates[..] as &[u32],
            &self.stage.replacements[..] as &[u32],
        ] {
            if reservation_slices_overlap(scratch, proof.pages) {
                return Err(FreeBitmapCowError::ArenaPageConflict(0));
            }
        }
        if reservation_slices_overlap(&self.pool_validation[..], proof.pages)
            || reservation_slices_overlap(&self.source_nodes[..], proof.pages)
            || reservation_slices_overlap(&self.cow.arena_bindings[..], proof.pages)
            || reservation_slices_overlap(&self.cow.index_nodes[..], proof.pages)
            || reservation_slices_overlap(&self.cow.available_slots[..], proof.pages)
            || reservation_slices_overlap(&self.cow.verified_pages[..], proof.pages)
            || reservation_slices_overlap(&self.stage.arena[..], proof.pages)
            || reservation_slices_overlap(&self.stage.arena_bindings[..], proof.pages)
            || reservation_slices_overlap(&self.stage.verified_pages[..], proof.pages)
            || reservation_slices_overlap(&self.stage.index_nodes[..], proof.pages)
            || reservation_slices_overlap(&self.stage.available_slots[..], proof.pages)
        {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }
        let needed_sources = candidate_len
            .checked_add(proof.pages.len())
            .ok_or(FreeBitmapCowError::IndexCapacityOverflow)?;
        if needed_sources > self.source_nodes.len() {
            return Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::SourceNodes,
                required: needed_sources,
                available: self.source_nodes.len(),
            });
        }
        let mut previous = None;
        for (index, &pgno) in proof.pages.iter().enumerate() {
            if pgno < 2 || u64::from(pgno) >= self.cow.committed_page_count {
                return Err(FreeBitmapCowError::LedgerPageOutOfBounds(pgno));
            }
            if let Some(prior) = previous {
                if pgno <= prior {
                    return Err(if pgno == prior {
                        FreeBitmapCowError::DuplicateCandidate(pgno)
                    } else {
                        FreeBitmapCowError::CandidateOrderRegression {
                            previous: prior,
                            current: pgno,
                        }
                    });
                }
            }
            if self.cow.candidates[..candidate_len]
                .binary_search(&pgno)
                .is_ok()
            {
                return Err(FreeBitmapCowError::LedgerPageConflict(pgno));
            }
            if self
                .cow
                .pool()
                .find_bound_page(pgno)
                .map_err(FreeBitmapCowError::PrivatePool)?
                .is_some()
            {
                return Err(FreeBitmapCowError::LedgerPageConflict(pgno));
            }
            self.source_nodes[candidate_len + index] = FreeBitmapReservationSourceNode {
                pgno,
                kind: FreeBitmapReservationSourceKind::Reclaimed,
                required: 0,
            };
            previous = Some(pgno);
        }

        let mut candidate_rank = 0usize;
        let mut committed_selected = 0usize;
        let mut reclaimed_rank = 0usize;
        let mut appended = 0usize;
        for index in 0..self.private_pages {
            let candidate = self.source_nodes[..candidate_len]
                .get(candidate_rank)
                .copied();
            let reclaimed = proof.pages.get(reclaimed_rank).copied();
            let (pgno, authorization) = match (candidate, reclaimed) {
                (Some(left), Some(right)) if left.pgno < right => {
                    candidate_rank += 1;
                    let authorization = match left.kind {
                        FreeBitmapReservationSourceKind::Committed => {
                            committed_selected += 1;
                            PrivatePageAuthorization::CommittedFree
                        }
                        FreeBitmapReservationSourceKind::Appended => {
                            appended += 1;
                            PrivatePageAuthorization::Appended
                        }
                        FreeBitmapReservationSourceKind::Reclaimed => {
                            return Err(FreeBitmapCowError::StaleInsertionPlan);
                        }
                    };
                    (left.pgno, authorization)
                }
                (_, Some(right)) => {
                    reclaimed_rank += 1;
                    (right, PrivatePageAuthorization::SafelyReclaimed)
                }
                (Some(left), None) => {
                    candidate_rank += 1;
                    let authorization = match left.kind {
                        FreeBitmapReservationSourceKind::Committed => {
                            committed_selected += 1;
                            PrivatePageAuthorization::CommittedFree
                        }
                        FreeBitmapReservationSourceKind::Appended => {
                            appended += 1;
                            PrivatePageAuthorization::Appended
                        }
                        FreeBitmapReservationSourceKind::Reclaimed => {
                            return Err(FreeBitmapCowError::StaleInsertionPlan);
                        }
                    };
                    (left.pgno, authorization)
                }
                (None, None) => {
                    let page = self
                        .cow
                        .pending_page_count
                        .checked_add(appended as u64)
                        .filter(|value| *value < MAX_PAGE_COUNT)
                        .ok_or(FreeBitmapCowError::PageSpaceExhausted)?;
                    appended += 1;
                    (
                        u32::try_from(page).map_err(|_| FreeBitmapCowError::PageSpaceExhausted)?,
                        PrivatePageAuthorization::Appended,
                    )
                }
            };
            self.pool_validation[index] = PrivatePageCompositeBind {
                pool_slot: self.cow.arena_bindings[index].pool_slot,
                pgno,
                authorization,
                state: PrivatePageCompositeBindState::Available,
            };
        }

        self.stage.candidates[..candidate_len]
            .copy_from_slice(&self.cow.candidates[..candidate_len]);
        self.stage.verified_pages[..verified_len].clone_from_slice(self.cow.verified_pages);
        self.stage.replacements[..verified_len].fill(0);
        let stage_pool = PrivatePagePool::new_vacant(
            &mut self.stage.arena[..self.private_pages],
            self.cow.committed_page_count,
            self.cow.pending_page_count,
            self.cow.pending_txn,
        )
        .map_err(FreeBitmapCowError::PrivatePool)?;
        let stage_scope = stage_pool
            .reserve_scope(self.private_pages)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        let stage_ledger = ScopedFreeBitmapCowLedger::new(
            &mut self.stage.arena_bindings[..self.private_pages],
            &mut self.stage.replacements[..verified_len],
            0,
            &mut self.stage.candidates[..candidate_len],
            0,
            &mut self.stage.index_nodes[..self.cow.index_nodes.len()],
            &mut self.stage.available_slots[..self.private_pages],
            &mut self.stage.verified_pages[..verified_len],
            candidate_len,
            true,
            self.payload_pages,
            self.private_pages,
        );
        let mut shadow = FreeBitmapCow::from_scoped_pool_with_pending_txn(
            &CALLBACK_FREE_COMMITTED_SOURCE,
            self.cow.selected_txn,
            self.cow.pending_txn,
            self.cow.pending_page_count,
            self.cow.root,
            &stage_pool,
            &stage_scope,
            stage_ledger,
        )?;
        shadow.select_planned_candidate_prefix(0)?;
        let checkpoint = stage_pool
            .begin_checkpoint()
            .map_err(FreeBitmapCowError::PrivatePool)?;
        for binding in &self.pool_validation[..self.private_pages] {
            if let Err(error) = stage_pool.bind_page(
                &checkpoint,
                &stage_scope,
                binding.pgno,
                binding.authorization,
            ) {
                return match stage_pool.rollback_checkpoint(checkpoint) {
                    Ok(()) => Err(FreeBitmapCowError::PrivatePool(error)),
                    Err((_checkpoint, rollback_error)) => {
                        Err(FreeBitmapCowError::PrivatePool(rollback_error))
                    }
                };
            }
        }
        stage_pool
            .commit_checkpoint(checkpoint)
            .map_err(|(_checkpoint, error)| FreeBitmapCowError::PrivatePool(error))?;
        shadow.synchronize_scoped_bindings_for_candidate_prefix(&stage_scope, candidate_rank)?;
        #[cfg(test)]
        let batch_index_probes_before = shadow.index_probe_count();
        #[cfg(test)]
        let batch_scope_validations_before = shadow.scoped_validation_pass_count();
        shadow.apply_planned_reservation_after_access()?;
        #[cfg(test)]
        let batch_index_probes = shadow
            .index_probe_count()
            .saturating_sub(batch_index_probes_before);
        #[cfg(test)]
        let batch_scope_validations = shadow
            .scoped_validation_pass_count()
            .saturating_sub(batch_scope_validations_before);
        if shadow.candidate_len != candidate_rank || shadow.available_len < self.payload_pages {
            return Err(FreeBitmapCowError::PrivateArenaExhausted);
        }
        shadow.validate_scoped_bindings()?;
        for (index, binding) in self.pool_validation[..self.private_pages]
            .iter_mut()
            .enumerate()
        {
            let stage_slot = shadow.arena_bindings[index].pool_slot;
            let info = stage_pool
                .scoped_slot_info(&stage_scope, stage_slot)
                .map_err(FreeBitmapCowError::PrivatePool)?
                .ok_or(FreeBitmapCowError::ArenaPageConflict(binding.pgno))?;
            binding.state = match info.state {
                PrivatePagePoolState::Available => PrivatePageCompositeBindState::Available,
                PrivatePagePoolState::InUse {
                    owner: PrivatePageOwner::Bitmap,
                    owner_generation,
                    tag,
                } if owner_generation == self.cow.pending_txn => {
                    PrivatePageCompositeBindState::Bitmap {
                        committed_origin: u32::try_from(tag)
                            .map_err(|_| FreeBitmapCowError::ArenaPageConflict(binding.pgno))?,
                        stage_slot,
                    }
                }
                _ => return Err(FreeBitmapCowError::ArenaPageConflict(binding.pgno)),
            };
        }
        let prepared = self
            .cow
            .pool()
            .prepare_composite_scope_bind(
                self.scope,
                pool_commitment,
                &stage_pool,
                &stage_scope,
                &self.pool_validation[..self.private_pages],
            )
            .map_err(late_binding_pool_error)?;

        self.cow
            .pool()
            .apply_prepared_composite_scope_bind(prepared, &stage_pool, &stage_scope)
            .map_err(late_binding_pool_error)?;

        // The live pool is now committed. The remaining copies and scalar
        // translations are bounds-proved above and contain no fallible step.
        for index in 0..self.private_pages {
            let real_slot = self.cow.arena_bindings[index].pool_slot;
            let old_epoch = self.cow.arena_bindings[index].pool_epoch;
            let mut binding = shadow.arena_bindings[index];
            binding.pool_slot = real_slot;
            binding.pool_epoch = old_epoch
                + match self.pool_validation[index].state {
                    PrivatePageCompositeBindState::Available => 1,
                    PrivatePageCompositeBindState::Bitmap { .. } => 2,
                };
            self.cow.arena_bindings[index] = binding;
        }
        self.cow.replacements.copy_from_slice(shadow.replacements);
        self.cow.index_nodes.copy_from_slice(shadow.index_nodes);
        for node in self.cow.index_nodes.iter_mut() {
            if let IndexedPage::Arena(stage_slot) = node.page {
                node.page = IndexedPage::Arena(self.cow.arena_bindings[stage_slot].pool_slot);
            }
        }
        self.cow.available_slots.fill(0);
        for index in 0..shadow.available_len {
            let stage_slot = shadow.available_slots[index];
            self.cow.available_slots[index] = self.cow.arena_bindings[stage_slot].pool_slot;
        }
        self.cow.replacement_len = shadow.replacement_len;
        self.cow.candidate_len = shadow.candidate_len;
        self.cow.index_root = shadow.index_root;
        self.cow.index_len = shadow.index_len;
        self.cow.available_len = shadow.available_len;
        self.cow.selected_candidate_len = shadow.selected_candidate_len;
        self.cow.candidate_selection_set = shadow.candidate_selection_set;
        self.cow.pending_page_count = shadow.pending_page_count;
        self.cow.root = shadow.root;
        #[cfg(test)]
        {
            self.cow.index_probes.set(batch_index_probes);
            self.cow
                .scoped_validation_passes
                .set(batch_scope_validations);
        }
        self.source_nodes[candidate_len..needed_sources]
            .fill(FreeBitmapReservationSourceNode::empty());
        self.ticket.nonce.set(0);
        let reclaimed_selected = reclaimed_rank;
        Ok(BoundFreeBitmapReservation {
            cow: self.cow,
            binding: FreeBitmapReservationBinding {
                committed: committed_selected,
                reclaimed: reclaimed_selected,
                appended,
            },
            private_pages: self.private_pages,
            source_nodes: self.source_nodes,
            pool_validation: self.pool_validation,
            stage: self.stage,
            reclaim_guard: proof.reclaim_guard,
        })
    }
}

impl<'a, 'slots, 'scope, S: CommittedPageSource + ?Sized> FreeBitmapCow<'a, 'slots, 'scope, S> {
    pub(crate) fn select_planned_candidate_prefix(
        &mut self,
        selected: usize,
    ) -> Result<(), FreeBitmapCowError> {
        if !self.reservation_planned
            || selected < self.candidate_len
            || selected > self.planned_candidate_len
        {
            return Err(FreeBitmapCowError::LedgerPrefixOutOfBounds);
        }
        self.selected_candidate_len = selected;
        self.candidate_selection_set = true;
        Ok(())
    }

    fn validate_planned_candidate_prefix(&self, selected: usize) -> Result<(), FreeBitmapCowError> {
        if !self.reservation_planned {
            return if selected == 0 {
                Ok(())
            } else {
                Err(FreeBitmapCowError::LedgerPrefixOutOfBounds)
            };
        }
        if selected < self.candidate_len
            || selected > self.planned_candidate_len
            || self.planned_candidate_len > self.candidates.len()
        {
            return Err(FreeBitmapCowError::LedgerPrefixOutOfBounds);
        }
        let mut previous = None;
        for (candidate_index, &pgno) in self.candidates[..self.planned_candidate_len]
            .iter()
            .enumerate()
        {
            if pgno < 2 || u64::from(pgno) >= self.committed_page_count {
                return Err(FreeBitmapCowError::LedgerPageOutOfBounds(pgno));
            }
            if let Some(prior) = previous {
                if pgno == prior {
                    return Err(FreeBitmapCowError::DuplicateCandidate(pgno));
                }
                if pgno < prior {
                    return Err(FreeBitmapCowError::CandidateOrderRegression {
                        previous: prior,
                        current: pgno,
                    });
                }
            }
            previous = Some(pgno);
            let node = page_index_find_node(self.index_nodes, self.index_root, pgno)
                .ok_or(FreeBitmapCowError::ArenaPageConflict(pgno))?;
            let expected_node = self
                .scope_capacity
                .checked_add(self.replacements.len())
                .and_then(|offset| offset.checked_add(candidate_index))
                .filter(|&expected| expected < self.index_nodes.len())
                .ok_or(FreeBitmapCowError::ArenaPageConflict(pgno))?;
            let mapped = self.index_nodes[node];
            if node != expected_node
                || !mapped.candidate_mapped
                || mapped.candidate_pgno != pgno
                || mapped.candidate_index != candidate_index
                || !match mapped.page {
                    IndexedPage::PlannedCandidate(index) => index == candidate_index,
                    IndexedPage::Arena(_) => true,
                    IndexedPage::Verified(_) | IndexedPage::Replacement => false,
                }
            {
                return Err(FreeBitmapCowError::ArenaPageConflict(pgno));
            }
        }
        Ok(())
    }

    fn selected_candidate_node(&self, pgno: u32, selected: usize) -> Option<usize> {
        let candidate_index = self.candidates[..selected].binary_search(&pgno).ok()?;
        let node = page_index_find_node(self.index_nodes, self.index_root, pgno)?;
        let mapped = self.index_nodes[node];
        (mapped.candidate_mapped
            && mapped.candidate_pgno == pgno
            && mapped.candidate_index == candidate_index)
            .then_some(node)
    }

    fn validate_scoped_bindings(&self) -> Result<(), FreeBitmapCowError> {
        #[cfg(test)]
        self.scoped_validation_passes
            .set(self.scoped_validation_passes.get().saturating_add(1));
        let scope = self
            .scoped()
            .ok_or(FreeBitmapCowError::ArenaPageConflict(0))?;
        if self.arena_bindings.len() < self.scope_capacity {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }
        self.validate_canonical_arena_bindings(scope)?;
        let status = self
            .pool()
            .scope_status(scope)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        if status.capacity != self.scope_capacity
            || self.pool().pending_txn() != self.pending_txn
            || self.pool().pending_page_count() != self.pending_page_count
        {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }
        let selected = self.selected_candidate_target();
        self.validate_planned_candidate_prefix(selected)?;
        let mut bound = 0usize;
        let mut selected_bound = 0usize;
        for binding in &self.arena_bindings[..self.scope_capacity] {
            let info = self
                .pool()
                .scoped_slot_info(scope, binding.pool_slot)
                .map_err(FreeBitmapCowError::PrivatePool)?
                .ok_or(FreeBitmapCowError::ArenaPageConflict(binding.page_number))?;
            if info.binding_epoch != binding.pool_epoch || info.bound != binding.bound {
                return Err(FreeBitmapCowError::ArenaPageConflict(info.pgno));
            }
            if !info.bound {
                if binding.page_number != 0
                    || binding.active_node != NO_INDEX
                    || self.index_nodes[binding.storage_node] != BitmapCowIndexNode::empty()
                {
                    return Err(FreeBitmapCowError::ArenaPageConflict(0));
                }
                continue;
            }
            bound += 1;
            if binding.page_number != info.pgno || binding.active_node >= self.index_nodes.len() {
                return Err(FreeBitmapCowError::ArenaPageConflict(info.pgno));
            }
            let node = page_index_find_node(self.index_nodes, self.index_root, info.pgno)
                .ok_or(FreeBitmapCowError::ArenaPageConflict(info.pgno))?;
            if node != binding.active_node
                || self.index_nodes[node].page != IndexedPage::Arena(binding.pool_slot)
            {
                return Err(FreeBitmapCowError::ArenaPageConflict(info.pgno));
            }
            let mapped = self.index_nodes[node];
            if mapped.candidate_mapped {
                let expected_node = self
                    .scope_capacity
                    .checked_add(self.replacements.len())
                    .and_then(|offset| offset.checked_add(mapped.candidate_index))
                    .filter(|&expected| expected < self.index_nodes.len())
                    .ok_or(FreeBitmapCowError::ArenaPageConflict(info.pgno))?;
                if mapped.candidate_index >= selected
                    || node != expected_node
                    || mapped.candidate_pgno != info.pgno
                    || self.candidates[mapped.candidate_index] != info.pgno
                    || info.authorization != Some(PrivatePageAuthorization::CommittedFree)
                    || self.index_nodes[binding.storage_node] != BitmapCowIndexNode::empty()
                {
                    return Err(FreeBitmapCowError::ArenaPageConflict(info.pgno));
                }
                selected_bound += 1;
            } else if node != binding.storage_node {
                return Err(FreeBitmapCowError::ArenaPageConflict(info.pgno));
            }
        }
        if bound != status.bound || selected_bound != selected {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }
        Ok(())
    }

    pub(crate) fn synchronize_scoped_bindings(
        &mut self,
        scope: &PrivatePageReservationScope<'scope>,
    ) -> Result<(), FreeBitmapCowError> {
        self.synchronize_scoped_bindings_for_candidate_prefix(
            scope,
            self.selected_candidate_target(),
        )
    }

    pub(crate) fn synchronize_scoped_bindings_for_candidate_prefix(
        &mut self,
        scope: &PrivatePageReservationScope<'scope>,
        selected: usize,
    ) -> Result<(), FreeBitmapCowError> {
        let stored_scope = self
            .scoped()
            .ok_or(FreeBitmapCowError::ArenaPageConflict(0))?;
        if !core::ptr::eq(scope, stored_scope) {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }
        self.validate_canonical_arena_bindings(scope)?;
        self.validate_planned_candidate_prefix(selected)?;
        let status = self
            .pool()
            .scope_status(scope)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        let pending_page_count = self.pool().pending_page_count();
        if status.capacity != self.scope_capacity
            || self.pool().pending_txn() != self.pending_txn
            || pending_page_count < self.committed_page_count
            || self.arena_bindings.len() < self.scope_capacity
        {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }

        let mut bound = 0usize;
        let mut available = 0usize;
        let mut selected_bound = 0usize;
        for binding in &self.arena_bindings[..self.scope_capacity] {
            if binding.pool_slot >= self.pool().len()
                || binding.storage_node >= self.index_nodes.len()
            {
                return Err(FreeBitmapCowError::ArenaPageConflict(binding.page_number));
            }
            let info = self
                .pool()
                .scoped_slot_info(scope, binding.pool_slot)
                .map_err(FreeBitmapCowError::PrivatePool)?
                .ok_or(FreeBitmapCowError::ArenaPageConflict(binding.page_number))?;
            if binding.bound {
                if binding.active_node >= self.index_nodes.len() {
                    return Err(FreeBitmapCowError::ArenaPageConflict(binding.page_number));
                }
                let node =
                    page_index_find_node(self.index_nodes, self.index_root, binding.page_number)
                        .ok_or(FreeBitmapCowError::ArenaPageConflict(binding.page_number))?;
                if node != binding.active_node
                    || self.index_nodes[node].page != IndexedPage::Arena(binding.pool_slot)
                {
                    return Err(FreeBitmapCowError::ArenaPageConflict(binding.page_number));
                }
                let mapped = self.index_nodes[node];
                if mapped.candidate_mapped {
                    let expected_node = self
                        .scope_capacity
                        .checked_add(self.replacements.len())
                        .and_then(|offset| offset.checked_add(mapped.candidate_index))
                        .filter(|&expected| expected < self.index_nodes.len())
                        .ok_or(FreeBitmapCowError::ArenaPageConflict(binding.page_number))?;
                    if mapped.candidate_index >= self.planned_candidate_len
                        || node != expected_node
                        || mapped.candidate_pgno != binding.page_number
                        || self.index_nodes[binding.storage_node] != BitmapCowIndexNode::empty()
                    {
                        return Err(FreeBitmapCowError::ArenaPageConflict(binding.page_number));
                    }
                } else if node != binding.storage_node {
                    return Err(FreeBitmapCowError::ArenaPageConflict(binding.page_number));
                }
            } else if binding.page_number != 0
                || binding.active_node != NO_INDEX
                || self.index_nodes[binding.storage_node] != BitmapCowIndexNode::empty()
            {
                return Err(FreeBitmapCowError::ArenaPageConflict(0));
            }
            if !info.bound {
                continue;
            }
            bound += 1;
            if info.pgno < 2 || u64::from(info.pgno) >= pending_page_count {
                return Err(FreeBitmapCowError::LedgerPageOutOfBounds(info.pgno));
            }
            if info.state == PrivatePagePoolState::Available {
                available += 1;
            }
            if let Some(candidate_node) = self.selected_candidate_node(info.pgno, selected) {
                if info.authorization != Some(PrivatePageAuthorization::CommittedFree) {
                    return Err(FreeBitmapCowError::ArenaPageConflict(info.pgno));
                }
                let node = page_index_find_node(self.index_nodes, self.index_root, info.pgno)
                    .ok_or(FreeBitmapCowError::LedgerPageConflict(info.pgno))?;
                if node != candidate_node
                    || (matches!(self.index_nodes[node].page, IndexedPage::Arena(_))
                        && (!binding.bound
                            || binding.active_node != node
                            || binding.page_number != info.pgno))
                {
                    return Err(FreeBitmapCowError::LedgerPageConflict(info.pgno));
                }
                selected_bound += 1;
                continue;
            }
            if self
                .selected_candidate_node(info.pgno, self.planned_candidate_len)
                .is_some()
            {
                return Err(FreeBitmapCowError::LedgerPageConflict(info.pgno));
            }
            if let Some(node) = page_index_find_node(self.index_nodes, self.index_root, info.pgno) {
                if !binding.bound || binding.page_number != info.pgno || binding.active_node != node
                {
                    return Err(FreeBitmapCowError::LedgerPageConflict(info.pgno));
                }
            }
        }
        if bound != status.bound
            || selected_bound != selected
            || available > self.available_slots.len()
        {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }

        for binding_index in 0..self.scope_capacity {
            let pool_slot = self.arena_bindings[binding_index].pool_slot;
            let info = self
                .pool()
                .scoped_slot_info(scope, pool_slot)
                .expect("scoped synchronization preflight retained the exact scope")
                .expect("scoped synchronization preflight retained the mapped slot");
            let binding = &mut self.arena_bindings[binding_index];
            if binding.bound && (!info.bound || binding.page_number != info.pgno) {
                let active_node = binding.active_node;
                let (root, removed) =
                    page_index_delete(self.index_nodes, self.index_root, binding.page_number);
                debug_assert_eq!(removed, active_node);
                self.index_root = root;
                if self.index_nodes[active_node].candidate_mapped {
                    restore_planned_candidate_node(
                        self.index_nodes,
                        &mut self.index_root,
                        active_node,
                    );
                } else {
                    clear_active_index_node(&mut self.index_nodes[active_node]);
                }
                binding.page_number = 0;
                binding.active_node = NO_INDEX;
                binding.bound = false;
            }
        }
        self.available_len = 0;
        for binding_index in 0..self.scope_capacity {
            let pool_slot = self.arena_bindings[binding_index].pool_slot;
            let info = self
                .pool()
                .scoped_slot_info(scope, pool_slot)
                .expect("scoped synchronization preflight retained the exact scope")
                .expect("scoped synchronization preflight retained the mapped slot");
            let binding = &mut self.arena_bindings[binding_index];
            if info.bound && !binding.bound {
                let active_node = if let Some(candidate_node) = selected_candidate_node_in(
                    self.index_nodes,
                    self.index_root,
                    self.candidates,
                    info.pgno,
                    selected,
                ) {
                    let previous = page_index_replace(
                        self.index_nodes,
                        self.index_root,
                        info.pgno,
                        IndexedPage::Arena(binding.pool_slot),
                    );
                    debug_assert!(matches!(previous, Some(IndexedPage::PlannedCandidate(_))));
                    candidate_node
                } else {
                    page_index_insert_existing_prechecked(
                        self.index_nodes,
                        &mut self.index_root,
                        binding.storage_node,
                        info.pgno,
                        IndexedPage::Arena(binding.pool_slot),
                    );
                    binding.storage_node
                };
                binding.page_number = info.pgno;
                binding.active_node = active_node;
                binding.bound = true;
            }
            binding.pool_epoch = info.binding_epoch;
        }
        for binding in self.arena_bindings[..self.scope_capacity].iter().rev() {
            let info = self
                .pool()
                .scoped_slot_info(scope, binding.pool_slot)
                .expect("scoped synchronization preflight retained the exact scope")
                .expect("scoped synchronization preflight retained the mapped slot");
            if info.bound && info.state == PrivatePagePoolState::Available {
                self.available_slots[self.available_len] = binding.pool_slot;
                self.available_len += 1;
            }
        }
        self.pending_page_count = pending_page_count;
        self.selected_candidate_len = selected;
        self.candidate_selection_set = true;
        Ok(())
    }
}

#[cfg(test)]
impl<'a, S: CommittedPageSource + ?Sized> PlannedFreeBitmapReservation<'a, S> {
    pub(crate) const fn reserved_pages(&self) -> usize {
        self.candidate_len + self.appended_len
    }

    pub(crate) fn into_cow(self) -> FreeBitmapCow<'a, 'a, 'a, S> {
        let reserved_len = self.reserved_pages();
        let pool = PrivatePagePool::new(
            &mut self.buffers.arena[..reserved_len],
            self.committed_page_count,
            self.pending_page_count,
            self.pending_txn,
        )
        .expect("reservation planning produced an exact private-page pool");
        FreeBitmapCow {
            committed: self.committed,
            selected_txn: self.selected_txn,
            pending_txn: self.pending_txn,
            committed_page_count: self.committed_page_count,
            pending_page_count: self.pending_page_count,
            root: self.root,
            pool: BitmapPoolBacking::Owned(pool),
            scope: None,
            scope_capacity: 0,
            arena_bindings: &mut [],
            replacements: self.buffers.replacements,
            replacement_len: 0,
            candidates: &mut self.buffers.candidates[..self.candidate_len],
            candidate_len: 0,
            index_nodes: self.buffers.index_nodes,
            index_root: self.index_root,
            index_len: self.index_len,
            available_slots: &mut self.buffers.available_slots[..reserved_len],
            available_len: reserved_len,
            verified_pages: &mut self.buffers.verified_pages[..self.verified_len],
            planned_candidate_len: self.candidate_len,
            selected_candidate_len: self.candidate_len,
            candidate_selection_set: false,
            reservation_planned: true,
            payload_page_budget: self.payload_pages,
            planned_required_private_pages: self.required_private_pages,
            #[cfg(test)]
            index_probes: core::cell::Cell::new(0),
            #[cfg(test)]
            scoped_validation_passes: core::cell::Cell::new(0),
            #[cfg(test)]
            apply_preflight_checks: core::cell::Cell::new(0),
            #[cfg(test)]
            shared_preparation_work: 0,
        }
    }
}

#[derive(Debug)]
pub(crate) struct FreeBitmapReservationPlanner<'a, S: CommittedPageSource + ?Sized> {
    committed: &'a S,
    selected_txn: u64,
    pending_txn: u64,
    committed_page_count: u64,
    governing_page_count: u64,
    root: u32,
    committed_root_level: u16,
    payload_pages: usize,
    candidate_len: usize,
    verified_len: usize,
    surviving_metadata: usize,
    peak_live_metadata: usize,
    peak_private_pages: usize,
    capacity_planning: bool,
    source_len: usize,
    index_root: usize,
    index_len: usize,
    cursor: [ReservationCursorFrame; FREE_PATH_CAPACITY],
    cursor_len: usize,
    cursor_started: bool,
    candidate_floor_exclusive: u32,
    buffers: FreeBitmapReservationBuffers<'a>,
}

impl<'a, S: CommittedPageSource + ?Sized> FreeBitmapReservationPlanner<'a, S> {
    pub(crate) fn new(
        committed: &'a S,
        selected_txn: u64,
        committed_page_count: u64,
        root: u32,
        payload_pages: usize,
        buffers: FreeBitmapReservationBuffers<'a>,
    ) -> Result<Self, FreeBitmapCowError> {
        let pending_txn = selected_txn
            .checked_add(1)
            .ok_or(FreeBitmapCowError::TransactionExhausted)?;
        Self::new_for_draft(
            committed,
            selected_txn,
            pending_txn,
            committed_page_count,
            committed_page_count,
            root,
            payload_pages,
            buffers,
        )
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn new_for_draft(
        committed: &'a S,
        selected_txn: u64,
        pending_txn: u64,
        committed_page_count: u64,
        governing_page_count: u64,
        root: u32,
        payload_pages: usize,
        buffers: FreeBitmapReservationBuffers<'a>,
    ) -> Result<Self, FreeBitmapCowError> {
        committed
            .check_access()
            .map_err(FreeBitmapCowError::Source)?;
        if selected_txn == 0 || pending_txn < selected_txn {
            return Err(FreeBitmapCowError::SelectedTransactionZero);
        }
        if !(2..=MAX_PAGE_COUNT).contains(&committed_page_count)
            || !(committed_page_count..=MAX_PAGE_COUNT).contains(&governing_page_count)
        {
            return Err(FreeBitmapCowError::PageCountOutOfRange(
                governing_page_count,
            ));
        }
        if root != 0 && (root < 2 || u64::from(root) >= governing_page_count) {
            return Err(FreeBitmapCowError::RootPageOutOfBounds(root));
        }
        let committed_root_level = minimum_level(governing_page_count)?;
        Ok(Self {
            committed,
            selected_txn,
            pending_txn,
            committed_page_count,
            governing_page_count,
            root,
            committed_root_level,
            payload_pages,
            candidate_len: 0,
            verified_len: 0,
            surviving_metadata: 0,
            peak_live_metadata: 0,
            peak_private_pages: payload_pages,
            capacity_planning: false,
            source_len: 0,
            index_root: NO_INDEX,
            index_len: 0,
            cursor: [EMPTY_CURSOR_FRAME; FREE_PATH_CAPACITY],
            cursor_len: 0,
            cursor_started: false,
            candidate_floor_exclusive: 1,
            buffers,
        })
    }

    pub(crate) fn after_carried_source(
        mut self,
        candidate_floor_exclusive: u32,
    ) -> Result<Self, FreeBitmapCowError> {
        if self.cursor_started || self.candidate_len != 0 {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        self.candidate_floor_exclusive = candidate_floor_exclusive;
        Ok(self)
    }

    #[cfg(test)]
    pub(crate) fn plan(
        mut self,
    ) -> Result<PlannedFreeBitmapReservation<'a, S>, FreeBitmapCowError> {
        self.committed
            .check_access()
            .map_err(FreeBitmapCowError::Source)?;
        loop {
            let required = self.required_private_pages()?;
            if self.candidate_len >= required {
                return self.finish(0);
            }
            let Some((candidate, path, path_len)) = self.next_candidate()? else {
                if self.surviving_metadata != 0 && self.candidate_floor_exclusive == 1 {
                    return Err(FreeBitmapCowError::SummaryMismatch);
                }
                let appended = self.appended_deficit()?;
                return self.finish(appended);
            };
            self.reserve_candidate(candidate, &path[..path_len])?;
        }
    }

    fn plan_capacity_impl(
        mut self,
    ) -> Result<FreeBitmapReservationCapacityPlan<'a, S>, FreeBitmapCowError> {
        self.capacity_planning = true;
        self.committed
            .check_access()
            .map_err(FreeBitmapCowError::Source)?;
        loop {
            let required = self.capacity_required_private_pages()?;
            if self.candidate_len >= required {
                return self.finish_capacity(0);
            }
            let Some((candidate, path, path_len)) = self.next_candidate()? else {
                if self.surviving_metadata != 0 && self.candidate_floor_exclusive == 1 {
                    return Err(FreeBitmapCowError::SummaryMismatch);
                }
                let appended = self.appended_deficit()?;
                return self.finish_capacity(appended);
            };
            self.reserve_candidate(candidate, &path[..path_len])?;
        }
    }

    /// Test-only direct capacity planning. Normal callers must bind the
    /// allocator to a live retirement-reclamation authority instead.
    #[cfg(test)]
    pub(crate) fn plan_capacity(
        self,
    ) -> Result<FreeBitmapReservationCapacityPlan<'a, S>, FreeBitmapCowError> {
        self.plan_capacity_impl()
    }

    /// Selects physical bitmap pages only while a verified retirement result
    /// retains the live operation-barrier authority. The returned plan keeps
    /// that authority until it binds the exact shadow scope.
    #[allow(clippy::result_large_err)]
    pub(crate) fn plan_under_reclamation<'selection, 'barrier, 'pages>(
        self,
        reclamation: RetirementReclamation<'selection, 'barrier, 'pages>,
    ) -> Result<
        LockedFreeBitmapReservationPlan<'a, 'selection, 'barrier, 'pages, S>,
        FreeBitmapCowError,
    > {
        Ok(LockedFreeBitmapReservationPlan {
            plan: self.plan_capacity_impl()?,
            reclamation,
        })
    }

    fn next_candidate(
        &mut self,
    ) -> Result<Option<(u32, [usize; FREE_PATH_CAPACITY], usize)>, FreeBitmapCowError> {
        if !self.cursor_started {
            self.cursor_started = true;
            if self.root == 0 {
                return Ok(None);
            }
            let root = self.load_verified(self.root, 0, self.committed_root_level, NO_INDEX)?;
            self.cursor[0] = ReservationCursorFrame {
                verified: root,
                next: if self.committed_root_level == 0 { 2 } else { 0 },
            };
            self.cursor_len = 1;
        }

        while self.cursor_len != 0 {
            let frame_index = self.cursor_len - 1;
            let verified_index = self.cursor[frame_index].verified;
            let level = self.buffers.verified_pages[verified_index].level;
            if level == 0 {
                let base = self.buffers.verified_pages[verified_index].base;
                let start = self.cursor[frame_index].next;
                let leaf = BitmapLeaf::open(
                    &self.buffers.verified_pages[verified_index].bytes,
                    self.selected_txn,
                    BitmapKind::FreePages,
                )
                .map_err(|cause| FreeBitmapCowError::Page {
                    pgno: self.buffers.verified_pages[verified_index].pgno,
                    cause,
                })?;
                let start_absolute = base
                    .checked_add(start as u64)
                    .ok_or(FreeBitmapCowError::CoverageOverflow)?
                    .max(u64::from(self.candidate_floor_exclusive) + 1);
                if let Some(candidate) =
                    search_free_leaf_from(leaf, base, self.governing_page_count, start_absolute)?
                {
                    let local = usize::try_from(candidate - base)
                        .map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
                    self.cursor[frame_index].next = local + 1;
                    let candidate = u32::try_from(candidate)
                        .map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
                    let mut path = [NO_INDEX; FREE_PATH_CAPACITY];
                    for (destination, frame) in
                        path.iter_mut().zip(self.cursor[..self.cursor_len].iter())
                    {
                        *destination = frame.verified;
                    }
                    return Ok(Some((candidate, path, self.cursor_len)));
                }
                self.cursor_len -= 1;
                continue;
            }

            let page = &self.buffers.verified_pages[verified_index];
            let branch = BitmapBranch::open(&page.bytes, self.selected_txn, BitmapKind::FreePages)
                .map_err(|cause| FreeBitmapCowError::Page {
                    pgno: page.pgno,
                    cause,
                })?;
            let start = self.cursor[frame_index].next;
            let Some(child_index) = branch.next_summary(start) else {
                self.cursor_len -= 1;
                continue;
            };
            self.cursor[frame_index].next = child_index + 1;
            let child = branch.child(child_index);
            if child == 0 {
                return Err(FreeBitmapCowError::SelectedChildMissing);
            }
            let child_span = coverage(level - 1)?;
            let child_base = page
                .base
                .checked_add(
                    child_span
                        .checked_mul(child_index as u64)
                        .ok_or(FreeBitmapCowError::CoverageOverflow)?,
                )
                .ok_or(FreeBitmapCowError::CoverageOverflow)?;
            if child_base >= self.governing_page_count {
                return Err(FreeBitmapCowError::SelectedCoverageOutsideLimit);
            }
            if self.cursor_len == FREE_PATH_CAPACITY {
                return Err(FreeBitmapCowError::CoverageOverflow);
            }
            let child_verified =
                self.load_verified(child, child_base, level - 1, verified_index)?;
            self.cursor[self.cursor_len] = ReservationCursorFrame {
                verified: child_verified,
                next: 0,
            };
            self.cursor_len += 1;
        }
        Ok(None)
    }

    fn load_verified(
        &mut self,
        pgno: u32,
        base: u64,
        expected_level: u16,
        parent: usize,
    ) -> Result<usize, FreeBitmapCowError> {
        if pgno < 2 || u64::from(pgno) >= self.governing_page_count {
            return Err(FreeBitmapCowError::RootPageOutOfBounds(pgno));
        }
        if page_index_find(self.buffers.index_nodes, self.index_root, pgno).is_some() {
            return Err(FreeBitmapCowError::RepeatedCommittedPage(pgno));
        }
        self.ensure_room(
            ReservationResource::VerifiedPages,
            self.verified_len + 1,
            self.buffers.verified_pages.len(),
        )?;
        self.ensure_room(
            ReservationResource::IndexNodes,
            self.index_len + 1,
            self.buffers.index_nodes.len(),
        )?;

        let mut bytes = [0u8; PAGE_SIZE];
        self.committed
            .read_page(pgno, &mut bytes)
            .map_err(FreeBitmapCowError::Source)?;
        let header = PageHeader::decode(&bytes, self.selected_txn).map_err(|cause| {
            FreeBitmapCowError::Page {
                pgno,
                cause: BitmapPageError::from(cause),
            }
        })?;
        let actual_level = match header.page_type {
            PageType::BitmapLeaf => 0,
            PageType::BitmapBranch => header.level,
            page_type => {
                return Err(FreeBitmapCowError::UnexpectedPageType { pgno, page_type });
            }
        };
        if actual_level != expected_level {
            return Err(if parent == NO_INDEX {
                FreeBitmapCowError::RootLevel {
                    expected: expected_level,
                    actual: actual_level,
                }
            } else {
                FreeBitmapCowError::ChildLevel {
                    expected: expected_level,
                    actual: actual_level,
                }
            });
        }
        let remaining = if expected_level == 0 {
            let leaf = BitmapLeaf::open(&bytes, self.selected_txn, BitmapKind::FreePages)
                .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })?;
            leaf.verify_local(BitmapKind::FreePages, base, self.governing_page_count)
                .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })?;
            (0..BITMAP_LEAF_WORDS)
                .map(|index| leaf.word(index).count_ones())
                .sum()
        } else {
            let branch = BitmapBranch::open(&bytes, self.selected_txn, BitmapKind::FreePages)
                .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })?;
            let child_span = coverage(expected_level - 1)?;
            branch
                .verify_local(
                    base,
                    child_span,
                    self.governing_page_count,
                    self.governing_page_count,
                )
                .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })?;
            for index in 0..BITMAP_FANOUT as usize {
                if branch.summary_bit(index) != (branch.child(index) != 0) {
                    return Err(FreeBitmapCowError::SummaryMismatch);
                }
            }
            u32::from(header.item_count)
        };
        if remaining == 0 && !(parent == NO_INDEX && expected_level == 0) {
            return Err(FreeBitmapCowError::SummaryMismatch);
        }

        let verified = self.verified_len;
        self.buffers.verified_pages[verified] = VerifiedBitmapPage {
            pgno,
            bytes,
            base,
            level: expected_level,
            parent,
            remaining,
            survives: remaining != 0,
        };
        page_index_insert_prechecked(
            self.buffers.index_nodes,
            &mut self.index_root,
            &mut self.index_len,
            pgno,
            IndexedPage::Verified(verified),
        );
        self.verified_len += 1;
        self.surviving_metadata += usize::from(remaining != 0);
        Ok(verified)
    }

    fn reserve_candidate(
        &mut self,
        candidate: u32,
        path: &[usize],
    ) -> Result<(), FreeBitmapCowError> {
        if let Some(&previous) = self.buffers.candidates[..self.candidate_len].last() {
            if candidate <= previous {
                return Err(if candidate == previous {
                    FreeBitmapCowError::DuplicateCandidate(candidate)
                } else {
                    FreeBitmapCowError::CandidateOrderRegression {
                        previous,
                        current: candidate,
                    }
                });
            }
        }
        self.ensure_room(
            ReservationResource::CandidatePages,
            self.candidate_len + 1,
            self.buffers.candidates.len(),
        )?;
        if !self.capacity_planning {
            self.ensure_room(
                ReservationResource::ArenaPages,
                self.candidate_len + 1,
                self.buffers.arena.len(),
            )?;
        }
        self.ensure_room(
            ReservationResource::IndexNodes,
            self.index_len + 1,
            self.buffers.index_nodes.len(),
        )?;
        if page_index_find(self.buffers.index_nodes, self.index_root, candidate).is_some() {
            return Err(FreeBitmapCowError::CandidateIsPathPage(candidate));
        }
        self.buffers.candidates[self.candidate_len] = candidate;
        page_index_insert_prechecked(
            self.buffers.index_nodes,
            &mut self.index_root,
            &mut self.index_len,
            candidate,
            IndexedPage::PlannedCandidate(self.candidate_len),
        );
        self.candidate_len += 1;

        let leaf = *path.last().ok_or(FreeBitmapCowError::SummaryMismatch)?;
        self.buffers.verified_pages[leaf].remaining = self.buffers.verified_pages[leaf]
            .remaining
            .checked_sub(1)
            .ok_or(FreeBitmapCowError::SummaryMismatch)?;
        if self.buffers.verified_pages[leaf].remaining == 0 {
            self.collapse_path(leaf)?;
        }
        self.peak_live_metadata = self.peak_live_metadata.max(self.surviving_metadata);
        let current = self
            .payload_pages
            .checked_add(self.surviving_metadata)
            .ok_or(FreeBitmapCowError::CoverageOverflow)?;
        self.peak_private_pages = self.peak_private_pages.max(current);
        if self.capacity_planning {
            self.ensure_room(
                ReservationResource::SourceNodes,
                self.source_len + 1,
                self.buffers.source_nodes.len(),
            )?;
            self.buffers.source_nodes[self.source_len] = FreeBitmapReservationSourceNode {
                pgno: candidate,
                kind: if u64::from(candidate) < self.committed_page_count {
                    FreeBitmapReservationSourceKind::Committed
                } else {
                    FreeBitmapReservationSourceKind::Appended
                },
                required: self.capacity_required_private_pages()?,
            };
            self.source_len += 1;
        }
        Ok(())
    }

    fn collapse_path(&mut self, mut page: usize) -> Result<(), FreeBitmapCowError> {
        loop {
            if !self.buffers.verified_pages[page].survives {
                return Err(FreeBitmapCowError::SummaryMismatch);
            }
            self.buffers.verified_pages[page].survives = false;
            self.surviving_metadata = self
                .surviving_metadata
                .checked_sub(1)
                .ok_or(FreeBitmapCowError::SummaryMismatch)?;
            let parent = self.buffers.verified_pages[page].parent;
            if parent == NO_INDEX {
                return Ok(());
            }
            self.buffers.verified_pages[parent].remaining = self.buffers.verified_pages[parent]
                .remaining
                .checked_sub(1)
                .ok_or(FreeBitmapCowError::SummaryMismatch)?;
            if self.buffers.verified_pages[parent].remaining != 0 {
                return Ok(());
            }
            page = parent;
        }
    }

    fn appended_deficit(&self) -> Result<usize, FreeBitmapCowError> {
        let required = if self.capacity_planning {
            self.capacity_required_private_pages()?
        } else {
            self.required_private_pages()?
        };
        let appended = required.saturating_sub(self.candidate_len);
        let appended_u64 =
            u64::try_from(appended).map_err(|_| FreeBitmapCowError::PageSpaceExhausted)?;
        self.governing_page_count
            .checked_add(appended_u64)
            .filter(|count| *count <= MAX_PAGE_COUNT)
            .ok_or(FreeBitmapCowError::PageSpaceExhausted)?;
        Ok(appended)
    }

    fn required_private_pages(&self) -> Result<usize, FreeBitmapCowError> {
        let final_requirement = self
            .payload_pages
            .checked_add(self.surviving_metadata)
            .ok_or(FreeBitmapCowError::CoverageOverflow)?;
        Ok(self.peak_live_metadata.max(final_requirement))
    }

    fn capacity_required_private_pages(&self) -> Result<usize, FreeBitmapCowError> {
        Ok(self.required_private_pages()?.max(self.peak_private_pages))
    }

    fn finish_capacity(
        self,
        appended_len: usize,
    ) -> Result<FreeBitmapReservationCapacityPlan<'a, S>, FreeBitmapCowError> {
        let private_pages = self
            .candidate_len
            .checked_add(appended_len)
            .ok_or(FreeBitmapCowError::CoverageOverflow)?;
        let required = self.capacity_required_private_pages()?;
        if private_pages < required {
            return Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::ArenaPages,
                required,
                available: private_pages,
            });
        }
        let index_required = private_pages
            .checked_add(self.verified_len)
            .and_then(|value| value.checked_add(self.candidate_len))
            .and_then(|value| value.checked_add(self.verified_len))
            .ok_or(FreeBitmapCowError::IndexCapacityOverflow)?;
        for (resource, required, available) in [
            (
                ReservationResource::PoolValidation,
                private_pages,
                self.buffers.pool_validation.len(),
            ),
            (
                ReservationResource::ArenaBindings,
                private_pages,
                self.buffers.arena_bindings.len(),
            ),
            (
                ReservationResource::AvailableSlots,
                private_pages,
                self.buffers.available_slots.len(),
            ),
            (
                ReservationResource::ReplacementPages,
                self.verified_len,
                self.buffers.replacements.len(),
            ),
            (
                ReservationResource::IndexNodes,
                index_required,
                self.buffers.index_nodes.len(),
            ),
            (
                ReservationResource::SourceNodes,
                self.candidate_len,
                self.buffers.source_nodes.len(),
            ),
            (
                ReservationResource::StagedArenaPages,
                private_pages,
                self.buffers.stage.arena.len(),
            ),
            (
                ReservationResource::ArenaBindings,
                private_pages,
                self.buffers.stage.arena_bindings.len(),
            ),
            (
                ReservationResource::CandidatePages,
                self.candidate_len,
                self.buffers.stage.candidates.len(),
            ),
            (
                ReservationResource::VerifiedPages,
                self.verified_len,
                self.buffers.stage.verified_pages.len(),
            ),
            (
                ReservationResource::ReplacementPages,
                self.verified_len,
                self.buffers.stage.replacements.len(),
            ),
            (
                ReservationResource::IndexNodes,
                index_required,
                self.buffers.stage.index_nodes.len(),
            ),
            (
                ReservationResource::AvailableSlots,
                private_pages,
                self.buffers.stage.available_slots.len(),
            ),
        ] {
            self.ensure_room(resource, required, available)?;
        }
        if private_pages == 0 {
            return Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::ArenaPages,
                required: 1,
                available: 0,
            });
        }
        let capacity_fingerprint = reservation_capacity_fingerprint(
            &self.buffers.candidates[..self.candidate_len],
            &self.buffers.verified_pages[..self.verified_len],
        );
        let source_fingerprint =
            reservation_source_fingerprint(&self.buffers.source_nodes[..self.source_len]);
        let FreeBitmapReservationBuffers {
            arena: _,
            pool_validation,
            arena_bindings,
            candidates,
            verified_pages,
            replacements,
            index_nodes,
            available_slots,
            source_nodes,
            reclamation,
            stage,
        } = self.buffers;
        Ok(FreeBitmapReservationCapacityPlan {
            committed: self.committed,
            selected_txn: self.selected_txn,
            pending_txn: self.pending_txn,
            committed_page_count: self.committed_page_count,
            governing_page_count: self.governing_page_count,
            root: self.root,
            payload_pages: self.payload_pages,
            private_pages,
            candidate_len: self.candidate_len,
            verified_len: self.verified_len,
            index_required,
            capacity_fingerprint,
            source_fingerprint,
            buffers: FreeBitmapReservationCapacityBuffers {
                pool_validation,
                arena_bindings,
                candidates,
                verified_pages,
                replacements,
                index_nodes,
                available_slots,
                source_nodes,
                reclamation,
                stage,
            },
        })
    }

    #[cfg(test)]
    fn finish(
        mut self,
        appended_len: usize,
    ) -> Result<PlannedFreeBitmapReservation<'a, S>, FreeBitmapCowError> {
        let reserved_len = self
            .candidate_len
            .checked_add(appended_len)
            .ok_or(FreeBitmapCowError::CoverageOverflow)?;
        let required_private_pages = self.required_private_pages()?;
        self.ensure_room(
            ReservationResource::ArenaPages,
            required_private_pages,
            reserved_len,
        )?;
        self.ensure_room(
            ReservationResource::ArenaPages,
            reserved_len,
            self.buffers.arena.len(),
        )?;
        self.ensure_room(
            ReservationResource::AvailableSlots,
            reserved_len,
            self.buffers.available_slots.len(),
        )?;
        self.ensure_room(
            ReservationResource::ReplacementPages,
            self.verified_len,
            self.buffers.replacements.len(),
        )?;
        self.ensure_room(
            ReservationResource::IndexNodes,
            self.index_len + appended_len,
            self.buffers.index_nodes.len(),
        )?;
        let appended_u64 =
            u64::try_from(appended_len).map_err(|_| FreeBitmapCowError::PageSpaceExhausted)?;
        let pending_page_count = self
            .committed_page_count
            .checked_add(appended_u64)
            .filter(|count| *count <= MAX_PAGE_COUNT)
            .ok_or(FreeBitmapCowError::PageSpaceExhausted)?;

        for slot in 0..self.candidate_len {
            let pgno = self.buffers.candidates[slot];
            self.buffers.arena[slot]
                .authorize_initial(pgno, PrivatePageAuthorization::CommittedFree);
            self.buffers.arena[slot]
                .set_adapter_label(PrivatePageOwner::Bitmap, BITMAP_SLOT_CANDIDATE);
            let previous = page_index_replace(
                self.buffers.index_nodes,
                self.index_root,
                pgno,
                IndexedPage::Arena(slot),
            );
            debug_assert_eq!(previous, Some(IndexedPage::PlannedCandidate(slot)));
        }
        for offset in 0..appended_len {
            let slot = self.candidate_len + offset;
            let pgno_u64 = self.committed_page_count + offset as u64;
            let pgno =
                u32::try_from(pgno_u64).map_err(|_| FreeBitmapCowError::PageSpaceExhausted)?;
            self.buffers.arena[slot].authorize_initial(pgno, PrivatePageAuthorization::Appended);
            page_index_insert_prechecked(
                self.buffers.index_nodes,
                &mut self.index_root,
                &mut self.index_len,
                pgno,
                IndexedPage::Arena(slot),
            );
        }
        for (available, slot) in self.buffers.available_slots[..reserved_len]
            .iter_mut()
            .zip((0..reserved_len).rev())
        {
            *available = slot;
        }

        Ok(PlannedFreeBitmapReservation {
            committed: self.committed,
            selected_txn: self.selected_txn,
            pending_txn: self.pending_txn,
            committed_page_count: self.committed_page_count,
            pending_page_count,
            root: self.root,
            payload_pages: self.payload_pages,
            candidate_len: self.candidate_len,
            appended_len,
            required_private_pages,
            verified_len: self.verified_len,
            index_root: self.index_root,
            index_len: self.index_len,
            buffers: self.buffers,
        })
    }

    fn ensure_room(
        &self,
        resource: ReservationResource,
        required: usize,
        available: usize,
    ) -> Result<(), FreeBitmapCowError> {
        if required > available {
            Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource,
                required,
                available,
            })
        } else {
            Ok(())
        }
    }
}

// The owned form is test-only construction state in production builds. Keeping
// it inline preserves the engine's allocation-free contract.
#[allow(clippy::large_enum_variant)]
enum BitmapPoolBacking<'borrow, 'slots> {
    Owned(PrivatePagePool<'slots>),
    Shared(&'borrow PrivatePagePool<'slots>),
}

impl core::fmt::Debug for BitmapPoolBacking<'_, '_> {
    fn fmt(&self, formatter: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match self {
            Self::Owned(pool) => formatter.debug_tuple("Owned").field(pool).finish(),
            Self::Shared(pool) => formatter.debug_tuple("Shared").field(pool).finish(),
        }
    }
}

#[derive(Debug)]
pub(crate) struct FreeBitmapCow<'a, 'slots, 'scope, S: CommittedPageSource + ?Sized> {
    committed: &'a S,
    selected_txn: u64,
    pending_txn: u64,
    committed_page_count: u64,
    pending_page_count: u64,
    root: u32,
    pool: BitmapPoolBacking<'a, 'slots>,
    scope: Option<&'a PrivatePageReservationScope<'scope>>,
    scope_capacity: usize,
    arena_bindings: &'a mut [BitmapCowArenaBinding],
    replacements: &'a mut [u32],
    replacement_len: usize,
    candidates: &'a mut [u32],
    candidate_len: usize,
    index_nodes: &'a mut [BitmapCowIndexNode],
    index_root: usize,
    index_len: usize,
    available_slots: &'a mut [usize],
    available_len: usize,
    verified_pages: &'a mut [VerifiedBitmapPage],
    planned_candidate_len: usize,
    selected_candidate_len: usize,
    candidate_selection_set: bool,
    reservation_planned: bool,
    payload_page_budget: usize,
    planned_required_private_pages: usize,
    #[cfg(test)]
    index_probes: core::cell::Cell<usize>,
    #[cfg(test)]
    scoped_validation_passes: core::cell::Cell<usize>,
    #[cfg(test)]
    apply_preflight_checks: core::cell::Cell<usize>,
    #[cfg(test)]
    shared_preparation_work: usize,
}

impl<'a, 'slots, 'scope, S: CommittedPageSource + ?Sized> FreeBitmapCow<'a, 'slots, 'scope, S> {
    fn pool(&self) -> &PrivatePagePool<'slots> {
        match &self.pool {
            BitmapPoolBacking::Owned(pool) => pool,
            BitmapPoolBacking::Shared(pool) => pool,
        }
    }

    fn persistent_shared_pool(&self) -> &'a PrivatePagePool<'slots> {
        match &self.pool {
            BitmapPoolBacking::Shared(pool) => pool,
            BitmapPoolBacking::Owned(_) => {
                // Bound reservations are constructed only by scoped attachment;
                // owned COWs cannot enter selective finalization.
                unreachable!("scoped finalization always uses a shared pool")
            }
        }
    }

    fn scoped(&self) -> Option<&PrivatePageReservationScope<'scope>> {
        self.scope
    }

    fn arena_binding_index(&self, slot: usize) -> Option<usize> {
        let scope = self.scoped()?;
        let info = self.pool().scoped_slot_info(scope, slot).ok()??;
        let ordinal = info.member_ordinal;
        (ordinal < self.scope_capacity
            && self.arena_bindings[ordinal].pool_slot == slot
            && self.arena_bindings[ordinal].storage_node == ordinal)
            .then_some(ordinal)
    }

    fn scoped_slot_info(
        &self,
        slot: usize,
    ) -> Result<PrivatePageScopedSlotInfo, FreeBitmapCowError> {
        let scope = self
            .scoped()
            .ok_or(FreeBitmapCowError::ArenaPageConflict(0))?;
        let binding_index = self
            .arena_binding_index(slot)
            .ok_or(FreeBitmapCowError::ArenaPageConflict(0))?;
        let binding = self.arena_bindings[binding_index];
        let info = self
            .pool()
            .scoped_slot_info(scope, slot)
            .map_err(FreeBitmapCowError::PrivatePool)?
            .ok_or(FreeBitmapCowError::ArenaPageConflict(binding.page_number))?;
        if !info.bound
            || !binding.bound
            || info.binding_epoch != binding.pool_epoch
            || info.pgno != binding.page_number
        {
            return Err(FreeBitmapCowError::ArenaPageConflict(info.pgno));
        }
        Ok(info)
    }

    fn refresh_arena_binding_epoch(
        &mut self,
        slot: usize,
        binding_epoch: u64,
    ) -> Result<(), FreeBitmapCowError> {
        let binding_index = self
            .arena_binding_index(slot)
            .ok_or(FreeBitmapCowError::ArenaPageConflict(0))?;
        self.arena_bindings[binding_index].pool_epoch = binding_epoch;
        Ok(())
    }

    fn selected_candidate_target(&self) -> usize {
        if self.candidate_selection_set {
            self.selected_candidate_len
        } else {
            self.planned_candidate_len
        }
    }

    fn validate_canonical_arena_bindings(
        &self,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<(), FreeBitmapCowError> {
        if self.arena_bindings.len() < self.scope_capacity {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }
        let mut mapped = 0usize;
        let mut layout_error = None;
        let layout = self
            .pool()
            .visit_exact_scope_layout(scope, |ordinal, slot, _info| {
                if layout_error.is_some() {
                    return;
                }
                if ordinal != mapped || mapped == self.scope_capacity {
                    layout_error = Some(FreeBitmapCowError::ArenaPageConflict(0));
                    return;
                }
                let binding = self.arena_bindings[mapped];
                if binding.pool_slot != slot || binding.storage_node != mapped {
                    layout_error = Some(FreeBitmapCowError::ArenaPageConflict(binding.page_number));
                    return;
                }
                let storage = self.index_nodes[mapped];
                if storage.candidate_mapped
                    || storage.candidate_pgno != 0
                    || storage.candidate_index != NO_INDEX
                {
                    layout_error = Some(FreeBitmapCowError::ArenaPageConflict(binding.page_number));
                    return;
                }
                mapped += 1;
            });
        layout.map_err(FreeBitmapCowError::PrivatePool)?;
        if let Some(error) = layout_error {
            return Err(error);
        }
        if mapped != self.scope_capacity {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }
        Ok(())
    }

    fn slot_page_number(&self, slot: usize) -> u32 {
        if self.scoped().is_some() {
            return self
                .scoped_slot_info(slot)
                .expect("bitmap index retains an exact scoped pool slot")
                .pgno;
        }
        self.pool()
            .page_number(slot)
            .expect("bitmap index retains an authorized pool slot")
    }

    fn slot_authorization(&self, slot: usize) -> PrivatePageAuthorization {
        if self.scoped().is_some() {
            return self
                .scoped_slot_info(slot)
                .expect("bitmap index retains an exact scoped pool slot")
                .authorization
                .expect("a bound scoped slot retains authorization");
        }
        self.pool()
            .authorization(slot)
            .expect("bitmap index retains an authorized pool slot")
    }

    fn slot_state(&self, slot: usize) -> BitmapPrivatePageState {
        if self.scoped().is_some() {
            let Ok(info) = self.scoped_slot_info(slot) else {
                return BitmapPrivatePageState::Foreign;
            };
            return classify_bitmap_pool_state(info.pgno, info.state, self.pending_txn)
                .unwrap_or(BitmapPrivatePageState::Foreign);
        }
        let Ok(pgno) = self.pool().page_number(slot) else {
            return BitmapPrivatePageState::Foreign;
        };
        let Ok(state) = self.pool().state(slot) else {
            return BitmapPrivatePageState::Foreign;
        };
        classify_bitmap_pool_state(pgno, state, self.pending_txn)
            .unwrap_or(BitmapPrivatePageState::Foreign)
    }

    fn authority(
        &self,
        slot: usize,
    ) -> Result<crate::private_page_pool::PrivatePageAuthority<'_>, FreeBitmapCowError> {
        if let Some(scope) = self.scoped() {
            return self
                .pool()
                .authority_in_scope(
                    scope,
                    self.slot_page_number(slot),
                    PrivatePageOwner::Bitmap,
                    self.pending_txn,
                )
                .map_err(FreeBitmapCowError::PrivatePool);
        }
        self.pool()
            .authority(
                self.slot_page_number(slot),
                PrivatePageOwner::Bitmap,
                self.pending_txn,
            )
            .map_err(FreeBitmapCowError::PrivatePool)
    }

    fn claim_slot(&self, slot: usize, committed_origin: u32) -> Result<(), FreeBitmapCowError> {
        self.pool()
            .claim(
                slot,
                PrivatePageOwner::Bitmap,
                self.pending_txn,
                u64::from(committed_origin),
            )
            .map(|_| ())
            .map_err(FreeBitmapCowError::PrivatePool)
    }

    fn write_slot(&self, slot: usize, bytes: &[u8; PAGE_SIZE]) -> Result<(), FreeBitmapCowError> {
        let authority = self.authority(slot)?;
        if let Some(scope) = self.scoped() {
            self.pool()
                .borrow_page_mut_in_scope(scope, &authority)
                .map_err(FreeBitmapCowError::PrivatePool)?
                .copy_from_slice(bytes);
        } else {
            self.pool()
                .borrow_page_mut(&authority)
                .map_err(FreeBitmapCowError::PrivatePool)?
                .copy_from_slice(bytes);
        }
        Ok(())
    }

    fn edit_slot(
        &self,
        slot: usize,
        edit: impl FnOnce(&mut [u8; PAGE_SIZE]),
    ) -> Result<(), FreeBitmapCowError> {
        let authority = self.authority(slot)?;
        let mut page = if let Some(scope) = self.scoped() {
            self.pool()
                .borrow_page_mut_in_scope(scope, &authority)
                .map_err(FreeBitmapCowError::PrivatePool)?
        } else {
            self.pool()
                .borrow_page_mut(&authority)
                .map_err(FreeBitmapCowError::PrivatePool)?
        };
        edit(&mut page);
        Ok(())
    }

    fn return_slot(
        &self,
        slot: usize,
        disposition: PrivatePageReturn,
    ) -> Result<(), (PrivatePageAuthority<'_>, FreeBitmapCowError)> {
        let authority = match self.authority(slot) {
            Ok(authority) => authority,
            Err(error) => {
                unreachable!(
                    "return callers preflight exact authority before consumption: {error:?}"
                )
            }
        };
        if let Some(scope) = self.scoped() {
            self.pool()
                .return_page_in_scope(scope, authority, disposition)
                .map_err(|(authority, error)| (authority, FreeBitmapCowError::PrivatePool(error)))
        } else {
            self.pool()
                .return_page(authority, disposition)
                .map_err(|(authority, error)| (authority, FreeBitmapCowError::PrivatePool(error)))
        }
    }

    fn is_planned_candidate(&self, pgno: u32) -> bool {
        let Some(node) = page_index_find_node(self.index_nodes, self.index_root, pgno) else {
            return false;
        };
        let indexed = self.index_nodes[node];
        if self.scoped().is_some() {
            indexed.candidate_mapped && indexed.candidate_pgno == pgno
        } else {
            let IndexedPage::Arena(slot) = indexed.page else {
                return false;
            };
            matches!(
                self.pool().adapter_label(slot),
                Ok(Some((PrivatePageOwner::Bitmap, BITMAP_SLOT_CANDIDATE)))
            )
        }
    }

    /// Prepare the caller-owned indexes and transaction ledger. Live writers
    /// construct this before taking the operation lock; `remove_lowest` then
    /// performs no allocation or full-ledger scan.
    pub(crate) fn new(
        committed: &'a S,
        selected_txn: u64,
        page_count: u64,
        root: u32,
        ledger: FreeBitmapCowLedger<'a>,
    ) -> Result<Self, FreeBitmapCowError>
    where
        'a: 'slots,
        'slots: 'a,
    {
        Self::new_with_page_counts(
            committed,
            selected_txn,
            page_count,
            page_count,
            root,
            ledger,
        )
    }

    fn new_with_page_counts(
        committed: &'a S,
        selected_txn: u64,
        committed_page_count: u64,
        pending_page_count: u64,
        root: u32,
        ledger: FreeBitmapCowLedger<'a>,
    ) -> Result<Self, FreeBitmapCowError>
    where
        'a: 'slots,
        'slots: 'a,
    {
        if selected_txn == 0 {
            return Err(FreeBitmapCowError::SelectedTransactionZero);
        }
        let pending_txn = selected_txn
            .checked_add(1)
            .ok_or(FreeBitmapCowError::TransactionExhausted)?;
        if !(2..=MAX_PAGE_COUNT).contains(&committed_page_count)
            || !(committed_page_count..=MAX_PAGE_COUNT).contains(&pending_page_count)
        {
            return Err(FreeBitmapCowError::PageCountOutOfRange(pending_page_count));
        }
        if root != 0 && (root < 2 || u64::from(root) >= pending_page_count) {
            return Err(FreeBitmapCowError::RootPageOutOfBounds(root));
        }
        if ledger.replacement_len > ledger.replacements.len()
            || ledger.candidate_len > ledger.candidates.len()
        {
            return Err(FreeBitmapCowError::LedgerPrefixOutOfBounds);
        }
        let prepared = prepare_ledger(pending_page_count, ledger)?;
        let pool = PrivatePagePool::new(
            prepared.arena,
            committed_page_count,
            pending_page_count,
            pending_txn,
        )
        .map_err(map_pool_construction_error)?;
        validate_bitmap_pool(&pool, pending_txn)?;

        Ok(Self {
            committed,
            selected_txn,
            pending_txn,
            committed_page_count,
            pending_page_count,
            root,
            pool: BitmapPoolBacking::Owned(pool),
            scope: None,
            scope_capacity: 0,
            arena_bindings: &mut [],
            replacements: prepared.replacements,
            replacement_len: prepared.replacement_len,
            candidates: prepared.candidates,
            candidate_len: prepared.candidate_len,
            index_nodes: prepared.index_nodes,
            index_root: prepared.index_root,
            index_len: prepared.index_len,
            available_slots: prepared.available_slots,
            available_len: prepared.available_len,
            verified_pages: &mut [],
            planned_candidate_len: 0,
            selected_candidate_len: 0,
            candidate_selection_set: false,
            reservation_planned: false,
            payload_page_budget: 0,
            planned_required_private_pages: 0,
            #[cfg(test)]
            index_probes: core::cell::Cell::new(0),
            #[cfg(test)]
            scoped_validation_passes: core::cell::Cell::new(0),
            #[cfg(test)]
            apply_preflight_checks: core::cell::Cell::new(0),
            #[cfg(test)]
            shared_preparation_work: 0,
        })
    }

    pub(crate) fn from_pool(
        committed: &'a S,
        selected_txn: u64,
        root: u32,
        pool: &'a PrivatePagePool<'slots>,
        ledger: SharedFreeBitmapCowLedger<'a>,
    ) -> Result<Self, FreeBitmapCowError> {
        if selected_txn == 0 {
            return Err(FreeBitmapCowError::SelectedTransactionZero);
        }
        let pending_txn = selected_txn
            .checked_add(1)
            .ok_or(FreeBitmapCowError::TransactionExhausted)?;
        if pending_txn != pool.pending_txn() {
            return Err(FreeBitmapCowError::PrivatePool(
                PrivatePagePoolError::PendingTransactionMismatch {
                    expected: pool.pending_txn(),
                    actual: pending_txn,
                },
            ));
        }
        let committed_page_count = pool.committed_page_count();
        let pending_page_count = pool.pending_page_count();
        if root != 0 && (root < 2 || u64::from(root) >= pending_page_count) {
            return Err(FreeBitmapCowError::RootPageOutOfBounds(root));
        }
        let prepared = prepare_shared_ledger(pool, pending_page_count, ledger)?;

        Ok(Self {
            committed,
            selected_txn,
            pending_txn,
            committed_page_count,
            pending_page_count,
            root,
            pool: BitmapPoolBacking::Shared(pool),
            scope: None,
            scope_capacity: 0,
            arena_bindings: &mut [],
            replacements: prepared.replacements,
            replacement_len: prepared.replacement_len,
            candidates: prepared.candidates,
            candidate_len: prepared.candidate_len,
            index_nodes: prepared.index_nodes,
            index_root: prepared.index_root,
            index_len: prepared.index_len,
            available_slots: prepared.available_slots,
            available_len: prepared.available_len,
            verified_pages: &mut [],
            planned_candidate_len: 0,
            selected_candidate_len: 0,
            candidate_selection_set: false,
            reservation_planned: false,
            payload_page_budget: 0,
            planned_required_private_pages: 0,
            #[cfg(test)]
            index_probes: core::cell::Cell::new(0),
            #[cfg(test)]
            scoped_validation_passes: core::cell::Cell::new(0),
            #[cfg(test)]
            apply_preflight_checks: core::cell::Cell::new(0),
            #[cfg(test)]
            shared_preparation_work: prepared.preparation_work,
        })
    }

    pub(crate) fn from_scoped_pool(
        committed: &'a S,
        selected_txn: u64,
        pending_page_count: u64,
        root: u32,
        pool: &'a PrivatePagePool<'slots>,
        scope: &'a PrivatePageReservationScope<'scope>,
        ledger: ScopedFreeBitmapCowLedger<'a>,
    ) -> Result<Self, FreeBitmapCowError> {
        let pending_txn = selected_txn
            .checked_add(1)
            .ok_or(FreeBitmapCowError::TransactionExhausted)?;
        Self::from_scoped_pool_with_pending_txn(
            committed,
            selected_txn,
            pending_txn,
            pending_page_count,
            root,
            pool,
            scope,
            ledger,
        )
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn from_scoped_pool_with_pending_txn(
        committed: &'a S,
        selected_txn: u64,
        pending_txn: u64,
        pending_page_count: u64,
        root: u32,
        pool: &'a PrivatePagePool<'slots>,
        scope: &'a PrivatePageReservationScope<'scope>,
        ledger: ScopedFreeBitmapCowLedger<'a>,
    ) -> Result<Self, FreeBitmapCowError> {
        if selected_txn == 0 {
            return Err(FreeBitmapCowError::SelectedTransactionZero);
        }
        if pending_txn < selected_txn {
            return Err(FreeBitmapCowError::TransactionExhausted);
        }
        if !(2..=MAX_PAGE_COUNT).contains(&pending_page_count) {
            return Err(FreeBitmapCowError::PageCountOutOfRange(pending_page_count));
        }
        if root != 0 && (root < 2 || u64::from(root) >= pending_page_count) {
            return Err(FreeBitmapCowError::RootPageOutOfBounds(root));
        }
        if ledger.replacement_len > ledger.replacements.len()
            || ledger.candidate_len > ledger.candidates.len()
            || ledger.planned_candidate_len > ledger.candidates.len()
            || (ledger.reservation_planned && ledger.candidate_len > ledger.planned_candidate_len)
        {
            return Err(FreeBitmapCowError::LedgerPrefixOutOfBounds);
        }
        if pending_txn != pool.pending_txn() {
            return Err(FreeBitmapCowError::PrivatePool(
                PrivatePagePoolError::PendingTransactionMismatch {
                    expected: pool.pending_txn(),
                    actual: pending_txn,
                },
            ));
        }
        if pending_page_count != pool.pending_page_count() {
            return Err(FreeBitmapCowError::PageCountOutOfRange(pending_page_count));
        }
        let scope_status = pool
            .scope_status(scope)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        let prepared = prepare_scoped_ledger(
            pool,
            scope,
            scope_status.capacity,
            pending_page_count,
            ledger,
        )?;
        Ok(Self {
            committed,
            selected_txn,
            pending_txn,
            committed_page_count: pool.committed_page_count(),
            pending_page_count,
            root,
            pool: BitmapPoolBacking::Shared(pool),
            scope: Some(scope),
            scope_capacity: scope_status.capacity,
            arena_bindings: prepared.arena_bindings,
            replacements: prepared.replacements,
            replacement_len: prepared.replacement_len,
            candidates: prepared.candidates,
            candidate_len: prepared.candidate_len,
            index_nodes: prepared.index_nodes,
            index_root: prepared.index_root,
            index_len: prepared.index_len,
            available_slots: prepared.available_slots,
            available_len: prepared.available_len,
            verified_pages: prepared.verified_pages,
            planned_candidate_len: prepared.planned_candidate_len,
            selected_candidate_len: prepared.planned_candidate_len,
            candidate_selection_set: false,
            reservation_planned: prepared.reservation_planned,
            payload_page_budget: prepared.payload_page_budget,
            planned_required_private_pages: prepared.planned_required_private_pages,
            #[cfg(test)]
            index_probes: core::cell::Cell::new(0),
            #[cfg(test)]
            scoped_validation_passes: core::cell::Cell::new(0),
            #[cfg(test)]
            apply_preflight_checks: core::cell::Cell::new(0),
            #[cfg(test)]
            shared_preparation_work: 0,
        })
    }

    pub(crate) const fn root(&self) -> u32 {
        self.root
    }

    pub(crate) fn replacements(&self) -> &[u32] {
        &self.replacements[..self.replacement_len]
    }

    pub(crate) fn candidates(&self) -> &[u32] {
        &self.candidates[..self.candidate_len]
    }

    pub(crate) fn private_page(&self, pgno: u32) -> Option<PrivatePageRef<'_, 'slots>> {
        let IndexedPage::Arena(slot) = self.indexed_page(pgno)? else {
            return None;
        };
        if !matches!(self.slot_state(slot), BitmapPrivatePageState::InUse { .. }) {
            return None;
        }
        let authority = self.authority(slot).ok()?;
        if let Some(scope) = self.scoped() {
            self.pool().borrow_page_in_scope(scope, &authority).ok()
        } else {
            self.pool().borrow_page(&authority).ok()
        }
    }

    pub(crate) fn available_private_pages(&self) -> usize {
        self.available_len
    }

    pub(crate) const fn committed_page_count(&self) -> u64 {
        self.committed_page_count
    }

    pub(crate) const fn pending_page_count(&self) -> u64 {
        self.pending_page_count
    }

    /// Preflight one strictly increasing caller-authorized release stream.
    ///
    /// The caller proves that every page is either direct-free or comes from a
    /// completely verified safe reclamation batch. This layer only constructs
    /// the free-bitmap COW paths and never infers that authorization itself.
    pub(crate) fn plan_insert_free<'cow, 'plan>(
        &'cow mut self,
        pages: &'plan [u32],
        scratch: &'plan mut [FreeBitmapInsertPage],
    ) -> Result<PlannedFreeBitmapInsertion<'cow, 'a, 'slots, 'scope, 'plan, S>, FreeBitmapCowError>
    {
        self.plan_insert_free_for_page_count(pages, self.pending_page_count, scratch)
    }

    fn plan_insert_free_for_page_count<'cow, 'plan>(
        &'cow mut self,
        pages: &'plan [u32],
        governing_page_count: u64,
        scratch: &'plan mut [FreeBitmapInsertPage],
    ) -> Result<PlannedFreeBitmapInsertion<'cow, 'a, 'slots, 'scope, 'plan, S>, FreeBitmapCowError>
    {
        self.committed
            .check_access()
            .map_err(FreeBitmapCowError::Source)?;
        let prepared = InsertPreflight::new(self, pages, governing_page_count, scratch)?.plan()?;
        Ok(PlannedFreeBitmapInsertion {
            cow: self,
            prepared,
        })
    }

    /// Apply a complete insertion preflight. The plan's exclusive draft borrow
    /// makes stale or cross-draft application unrepresentable.
    fn preflight_prepared_application(
        &self,
        plan: &PreparedFreeBitmapInsertion<'slots, '_>,
    ) -> Result<(), FreeBitmapCowError> {
        self.pool()
            .preflight_mutation(&plan.pool_snapshot, 0)
            .map_err(FreeBitmapCowError::PrivatePool)?;

        let mut epoch_steps = 0usize;
        #[cfg(test)]
        let mut structural_checks = 0usize;
        for (index, &slot) in plan.demoted_slots[..plan.demoted_len].iter().enumerate() {
            #[cfg(test)]
            {
                structural_checks += 1;
            }
            if plan.demoted_slots[..index].contains(&slot) {
                return Err(FreeBitmapCowError::ArenaPageConflict(
                    self.slot_page_number(slot),
                ));
            }
            self.pool()
                .validate_owner(slot, PrivatePageOwner::Bitmap, self.pending_txn)
                .map_err(FreeBitmapCowError::PrivatePool)?;
            epoch_steps = epoch_steps
                .checked_add(2)
                .ok_or(FreeBitmapCowError::CoverageOverflow)?;
        }

        for node in &plan.scratch[..plan.scratch_len] {
            #[cfg(test)]
            {
                structural_checks += 1;
            }
            if !node.changed {
                continue;
            }
            match node.origin {
                InsertPageOrigin::Private(slot) => {
                    self.pool()
                        .validate_owner(slot, PrivatePageOwner::Bitmap, self.pending_txn)
                        .map_err(FreeBitmapCowError::PrivatePool)?;
                    epoch_steps = epoch_steps
                        .checked_add(2)
                        .ok_or(FreeBitmapCowError::CoverageOverflow)?;
                }
                InsertPageOrigin::Committed
                | InsertPageOrigin::Verified(_)
                | InsertPageOrigin::New => {
                    let slot = node.destination_slot;
                    // Plan construction consumes each available/demoted cursor
                    // exactly once; the move-only scratch keeps that invariant.
                    if !plan.demoted_slots[..plan.demoted_len].contains(&slot) {
                        self.pool()
                            .validate_available(slot)
                            .map_err(FreeBitmapCowError::PrivatePool)?;
                    }
                    epoch_steps = epoch_steps
                        .checked_add(3)
                        .ok_or(FreeBitmapCowError::CoverageOverflow)?;
                }
                InsertPageOrigin::None => unreachable!(),
            }
        }

        for &pgno in plan.pages {
            #[cfg(test)]
            {
                structural_checks += 1;
            }
            if let Some(IndexedPage::Arena(slot)) = self.indexed_page(pgno) {
                let demoted = plan.demoted_slots[..plan.demoted_len].contains(&slot);
                let available = self
                    .pool()
                    .state(slot)
                    .map_err(FreeBitmapCowError::PrivatePool)?
                    == PrivatePagePoolState::Available;
                if demoted || available {
                    if !demoted {
                        self.pool()
                            .validate_available(slot)
                            .map_err(FreeBitmapCowError::PrivatePool)?;
                    }
                    epoch_steps = epoch_steps
                        .checked_add(2)
                        .ok_or(FreeBitmapCowError::CoverageOverflow)?;
                }
            }
        }
        for &pgno in &plan.auto_release_pages[..plan.auto_release_len] {
            #[cfg(test)]
            {
                structural_checks += 1;
            }
            if let Some(IndexedPage::Arena(slot)) = self.indexed_page(pgno) {
                if !plan.demoted_slots[..plan.demoted_len].contains(&slot) {
                    return Err(FreeBitmapCowError::PrivatePool(
                        PrivatePagePoolError::PageUnavailable(pgno),
                    ));
                }
                epoch_steps = epoch_steps
                    .checked_add(2)
                    .ok_or(FreeBitmapCowError::CoverageOverflow)?;
            }
        }

        #[cfg(test)]
        self.apply_preflight_checks.set(structural_checks);
        self.pool()
            .preflight_mutation(&plan.pool_snapshot, epoch_steps)
            .map_err(FreeBitmapCowError::PrivatePool)
    }

    fn apply_prepared_insert(
        &mut self,
        plan: PreparedFreeBitmapInsertion<'slots, '_>,
    ) -> FreeBitmapInsertResult {
        for &slot in &plan.demoted_slots[..plan.demoted_len] {
            self.return_slot(slot, PrivatePageReturn::Available)
                .expect("insertion preflight retained exact demoted-page ownership");
        }

        for node in &plan.scratch[..plan.scratch_len] {
            if !node.changed {
                continue;
            }
            match node.origin {
                InsertPageOrigin::Private(slot) => {
                    self.write_slot(slot, &node.bytes)
                        .expect("insertion preflight retained exact private-page ownership");
                }
                InsertPageOrigin::Committed | InsertPageOrigin::Verified(_) => {
                    let slot = node.destination_slot;
                    self.claim_slot(slot, node.source_pgno)
                        .expect("insertion preflight retained an available destination");
                    self.write_slot(slot, &node.bytes)
                        .expect("claimed bitmap destination remains writable");
                    self.replacements[self.replacement_len] = node.source_pgno;
                    self.replacement_len += 1;
                    match node.origin {
                        InsertPageOrigin::Committed => {
                            page_index_insert_prechecked(
                                self.index_nodes,
                                &mut self.index_root,
                                &mut self.index_len,
                                node.source_pgno,
                                IndexedPage::Replacement,
                            );
                        }
                        InsertPageOrigin::Verified(verified) => {
                            let previous = page_index_replace(
                                self.index_nodes,
                                self.index_root,
                                node.source_pgno,
                                IndexedPage::Replacement,
                            );
                            debug_assert_eq!(previous, Some(IndexedPage::Verified(verified)));
                        }
                        _ => unreachable!(),
                    }
                }
                InsertPageOrigin::New => {
                    let slot = node.destination_slot;
                    self.claim_slot(slot, 0)
                        .expect("insertion preflight retained an available destination");
                    self.write_slot(slot, &node.bytes)
                        .expect("claimed bitmap destination remains writable");
                }
                InsertPageOrigin::None => unreachable!(),
            }
        }

        for &pgno in plan.pages {
            if let Some(IndexedPage::Arena(slot)) = self.indexed_page(pgno) {
                if self.slot_state(slot) == BitmapPrivatePageState::Available {
                    let authority = self
                        .pool()
                        .claim(slot, PrivatePageOwner::Bitmap, self.pending_txn, 0)
                        .expect("release preflight retained an available exact page");
                    self.pool()
                        .return_page(authority, PrivatePageReturn::Free)
                        .expect("claimed release page remains exact");
                }
            }
        }
        for &pgno in &plan.auto_release_pages[..plan.auto_release_len] {
            if let Some(IndexedPage::Arena(slot)) = self.indexed_page(pgno) {
                debug_assert_eq!(self.slot_state(slot), BitmapPrivatePageState::Available);
                let authority = self
                    .pool()
                    .claim(slot, PrivatePageOwner::Bitmap, self.pending_txn, 0)
                    .expect("automatic release retained an available exact page");
                self.pool()
                    .return_page(authority, PrivatePageReturn::Free)
                    .expect("automatic release page remains exact");
            }
        }

        self.root = plan.root;
        self.pending_page_count = plan.governing_page_count;
        self.rebuild_available_slots();
        FreeBitmapInsertResult {
            inserted: plan.inserted,
            already_free: plan.already_free,
            committed_replacements: plan.committed_replacements,
            new_bitmap_pages: plan.new_bitmap_pages,
            recycled_private_pages: plan.demoted_len,
        }
    }

    pub(crate) fn insert_free(
        &mut self,
        pgno: u32,
        scratch: &mut [FreeBitmapInsertPage],
    ) -> Result<FreeBitmapInsertResult, FreeBitmapCowError> {
        let pages = [pgno];
        let plan = self.plan_insert_free(&pages, scratch)?;
        plan.apply()
    }

    /// Return every unused logical reservation and discard the contiguous
    /// unused appended suffix. The caller invokes this only after consuming all
    /// private slots needed by the transaction's other writers.
    pub(crate) fn release_unused_reservations(
        &mut self,
        release_pages: &mut [u32],
        scratch: &mut [FreeBitmapInsertPage],
    ) -> Result<UnusedReservationRelease, FreeBitmapCowError> {
        self.committed
            .check_access()
            .map_err(FreeBitmapCowError::Source)?;

        let old_page_count = self.pending_page_count;
        let mut new_page_count = old_page_count;
        while new_page_count > self.committed_page_count {
            let pgno = u32::try_from(new_page_count - 1)
                .map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
            let Some(IndexedPage::Arena(slot)) = self.indexed_page(pgno) else {
                break;
            };
            if self.slot_authorization(slot) != PrivatePageAuthorization::Appended
                || self.slot_state(slot) != BitmapPrivatePageState::Available
            {
                break;
            }
            new_page_count -= 1;
        }

        let mut release_len = 0usize;
        let mut candidate_count = 0usize;
        let mut appended_count = 0usize;
        for slot in 0..self.pool().len() {
            if self.slot_state(slot) != BitmapPrivatePageState::Available {
                continue;
            }
            let pgno = self.slot_page_number(slot);
            let release = match self.slot_authorization(slot) {
                PrivatePageAuthorization::CommittedFree if self.is_planned_candidate(pgno) => {
                    candidate_count += 1;
                    true
                }
                PrivatePageAuthorization::Appended if u64::from(pgno) < new_page_count => {
                    appended_count += 1;
                    true
                }
                PrivatePageAuthorization::CommittedFree
                | PrivatePageAuthorization::SafelyReclaimed
                | PrivatePageAuthorization::Appended => false,
            };
            if !release {
                continue;
            }
            if release_len == release_pages.len() {
                return Err(FreeBitmapCowError::InsufficientResourceBudget {
                    resource: ReservationResource::CandidatePages,
                    required: release_len + 1,
                    available: release_pages.len(),
                });
            }
            if release_len != 0 && pgno <= release_pages[release_len - 1] {
                return Err(FreeBitmapCowError::InsertPageOrderRegression {
                    previous: release_pages[release_len - 1],
                    current: pgno,
                });
            }
            release_pages[release_len] = pgno;
            release_len += 1;
        }

        let plan = self.plan_insert_free_for_page_count(
            &release_pages[..release_len],
            new_page_count,
            scratch,
        )?;
        candidate_count += plan.prepared.auto_reinserted_candidates;
        appended_count += plan.prepared.auto_reinserted_appended;
        let truncated_appended = usize::try_from(old_page_count - new_page_count)
            .map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
        plan.apply()?;
        for slot in 0..self.pool().len() {
            if self.slot_authorization(slot) == PrivatePageAuthorization::Appended
                && u64::from(self.slot_page_number(slot)) >= new_page_count
                && self.slot_state(slot) == BitmapPrivatePageState::Available
            {
                let authority = self
                    .pool()
                    .claim(slot, PrivatePageOwner::Bitmap, self.pending_txn, 0)
                    .expect("tail release retained an available exact page");
                self.pool()
                    .return_page(authority, PrivatePageReturn::Tail)
                    .expect("tail release page remains exact");
            }
        }
        self.rebuild_available_slots();
        Ok(UnusedReservationRelease {
            reinserted_candidates: candidate_count,
            reinserted_appended: appended_count,
            truncated_appended,
            pending_page_count: new_page_count,
        })
    }

    pub(crate) fn apply_planned_reservation(&mut self) -> Result<(), FreeBitmapCowError> {
        self.committed
            .check_access()
            .map_err(FreeBitmapCowError::Source)?;
        self.apply_planned_reservation_after_access()
    }

    fn apply_planned_reservation_after_access(&mut self) -> Result<(), FreeBitmapCowError> {
        let prepared = self.prepare_planned_removal_batch()?;
        self.apply_prepared_planned_removal_batch(prepared)
    }

    fn prepare_planned_removal_batch(
        &self,
    ) -> Result<PreparedPlannedRemovalBatch, FreeBitmapCowError> {
        self.preflight_planned_application()?;
        let scoped = self.scoped().is_some();
        if scoped {
            self.validate_scoped_bindings()?;
        }
        Ok(PreparedPlannedRemovalBatch {
            target: self.selected_candidate_target(),
            scoped,
        })
    }

    fn apply_prepared_planned_removal_batch(
        &mut self,
        prepared: PreparedPlannedRemovalBatch,
    ) -> Result<(), FreeBitmapCowError> {
        if prepared.target != self.selected_candidate_target()
            || prepared.scoped != self.scoped().is_some()
        {
            return Err(FreeBitmapCowError::StaleInsertionPlan);
        }
        while self.candidate_len < prepared.target {
            if self.remove_lowest_after_prevalidated_scope()?.is_none() {
                return Err(FreeBitmapCowError::PlannedCandidatesRemain {
                    remaining: prepared.target - self.candidate_len,
                });
            }
        }
        self.finish_planned_application()
    }

    fn preflight_planned_application(&self) -> Result<(), FreeBitmapCowError> {
        if !self.reservation_planned {
            return Err(FreeBitmapCowError::PlannedCandidatesRemain { remaining: 0 });
        }
        if self.candidate_len > self.selected_candidate_target() {
            return Err(FreeBitmapCowError::PlannedCandidatesRemain { remaining: 0 });
        }
        if self.pool().len() < self.planned_required_private_pages
            || self.available_slots.len() < self.planned_required_private_pages
        {
            return Err(FreeBitmapCowError::PrivateArenaExhausted);
        }
        if self.replacements.len() < self.verified_pages.len() {
            return Err(FreeBitmapCowError::ReplacementLedgerExhausted);
        }
        Ok(())
    }

    fn finish_planned_application(&self) -> Result<(), FreeBitmapCowError> {
        let target = self.selected_candidate_target();
        if self.candidate_len != target {
            return Err(FreeBitmapCowError::PlannedCandidatesRemain {
                remaining: target - self.candidate_len,
            });
        }
        if self.available_len < self.payload_page_budget {
            return Err(FreeBitmapCowError::PrivateArenaExhausted);
        }
        Ok(())
    }

    /// Remove and reserve the lowest free page represented by the current draft.
    /// Absence returns `None`; this layer never appends a replacement page.
    pub(crate) fn remove_lowest(&mut self) -> Result<Option<ReservedFreePage>, FreeBitmapCowError> {
        self.committed
            .check_access()
            .map_err(FreeBitmapCowError::Source)?;
        self.remove_lowest_after_access()
    }

    fn remove_lowest_after_access(
        &mut self,
    ) -> Result<Option<ReservedFreePage>, FreeBitmapCowError> {
        if self.reservation_planned && self.candidate_len == self.selected_candidate_target() {
            return Ok(None);
        }
        if self.scoped().is_some() {
            self.validate_scoped_bindings()?;
        }
        self.remove_lowest_after_prevalidated_scope()
    }

    /// The complete scoped ledger was validated by the caller and no callback
    /// can run before this path-local removal completes.
    fn remove_lowest_after_prevalidated_scope(
        &mut self,
    ) -> Result<Option<ReservedFreePage>, FreeBitmapCowError> {
        let Some(mut plan) = self.select_verified_path()? else {
            return Ok(None);
        };
        let mut operation_slots =
            [const { PrivatePageScopedOperationSlot::empty() }; FREE_PATH_CAPACITY];
        let operation = self.preflight(&mut plan, &mut operation_slots)?;
        let result = self.apply(plan, operation.as_ref());
        if let Some(operation) = operation {
            self.pool()
                .finish_operation_in_scope(operation)
                .expect("scoped removal preflight reserved exact terminal headroom");
        }
        Ok(result)
    }

    fn select_verified_path(&self) -> Result<Option<RemovalPlan>, FreeBitmapCowError> {
        if self.root == 0 {
            return Ok(None);
        }
        let minimum_candidate = if self.reservation_planned {
            *self
                .candidates
                .get(self.candidate_len)
                .ok_or(FreeBitmapCowError::PlannedCandidatesRemain { remaining: 0 })?
        } else {
            2
        };
        let expected_root_level = minimum_level(self.committed_page_count)?;
        let mut frames = [EMPTY_FRAME; FREE_PATH_CAPACITY];
        let mut snapshots = [[0u8; PAGE_SIZE]; FREE_PATH_CAPACITY];
        let mut len = 0usize;
        let mut pgno = self.root;
        let mut expected_level = expected_root_level;
        let mut base = 0u64;
        let mut selected_by_summary = false;

        loop {
            if len == FREE_PATH_CAPACITY {
                return Err(FreeBitmapCowError::CoverageOverflow);
            }
            if frames[..len].iter().any(|frame| frame.pgno == pgno) {
                return Err(FreeBitmapCowError::RepeatedPathPage(pgno));
            }
            let indexed = self.indexed_page(pgno);
            let page_count = if matches!(indexed, Some(IndexedPage::Arena(_))) {
                self.pending_page_count
            } else {
                self.committed_page_count
            };
            if pgno < 2 || u64::from(pgno) >= page_count {
                return Err(FreeBitmapCowError::RootPageOutOfBounds(pgno));
            }
            let (origin, page) = match indexed {
                Some(IndexedPage::Arena(slot)) => match self.slot_state(slot) {
                    BitmapPrivatePageState::InUse { committed_origin } => {
                        if let Some(scope) = self.scoped() {
                            let info = self.scoped_slot_info(slot)?;
                            self.pool()
                                .copy_owned_page_in_scope(
                                    scope,
                                    slot,
                                    info.binding_epoch,
                                    info.pgno,
                                    PrivatePageOwner::Bitmap,
                                    self.pending_txn,
                                    u64::from(committed_origin),
                                    &mut snapshots[len],
                                )
                                .map_err(FreeBitmapCowError::PrivatePool)?;
                        } else {
                            let authority = self.authority(slot)?;
                            let private = self
                                .pool()
                                .borrow_page(&authority)
                                .map_err(FreeBitmapCowError::PrivatePool)?;
                            snapshots[len].copy_from_slice(&private[..]);
                        }
                        (FrameOrigin::Private(slot), &snapshots[len])
                    }
                    BitmapPrivatePageState::Available => {
                        return Err(FreeBitmapCowError::ArenaPageConflict(pgno));
                    }
                    BitmapPrivatePageState::ReleasedFree | BitmapPrivatePageState::ReleasedTail => {
                        return Err(FreeBitmapCowError::ArenaPageConflict(pgno));
                    }
                    BitmapPrivatePageState::Foreign => {
                        return Err(FreeBitmapCowError::ArenaPageConflict(pgno));
                    }
                },
                Some(IndexedPage::Replacement) => {
                    return Err(FreeBitmapCowError::RepeatedCommittedPage(pgno));
                }
                Some(IndexedPage::Verified(slot)) => (
                    FrameOrigin::Verified(slot),
                    &self.verified_pages[slot].bytes,
                ),
                Some(IndexedPage::PlannedCandidate(_)) => {
                    return Err(FreeBitmapCowError::ArenaPageConflict(pgno));
                }
                None => {
                    self.committed
                        .read_page(pgno, &mut snapshots[len])
                        .map_err(FreeBitmapCowError::Source)?;
                    (FrameOrigin::Committed, &snapshots[len])
                }
            };
            let selected_txn = match origin {
                FrameOrigin::Committed | FrameOrigin::Verified(_) => self.selected_txn,
                FrameOrigin::Private(_) => self.pending_txn,
            };
            let header = PageHeader::decode(page, selected_txn).map_err(|cause| {
                FreeBitmapCowError::Page {
                    pgno,
                    cause: BitmapPageError::from(cause),
                }
            })?;
            let actual_level = match header.page_type {
                PageType::BitmapLeaf => 0,
                PageType::BitmapBranch => header.level,
                page_type => {
                    return Err(FreeBitmapCowError::UnexpectedPageType { pgno, page_type });
                }
            };
            if actual_level != expected_level {
                return Err(if len == 0 {
                    FreeBitmapCowError::RootLevel {
                        expected: expected_level,
                        actual: actual_level,
                    }
                } else {
                    FreeBitmapCowError::ChildLevel {
                        expected: expected_level,
                        actual: actual_level,
                    }
                });
            }

            if expected_level == 0 {
                let leaf = BitmapLeaf::open(page, selected_txn, BitmapKind::FreePages)
                    .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })?;
                if origin == FrameOrigin::Committed {
                    leaf.verify_local(BitmapKind::FreePages, base, self.committed_page_count)
                        .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })?;
                }
                let Some(candidate) = search_free_leaf_from(
                    leaf,
                    base,
                    self.committed_page_count,
                    u64::from(minimum_candidate),
                )?
                else {
                    return if selected_by_summary {
                        Err(FreeBitmapCowError::SummaryMismatch)
                    } else {
                        Ok(None)
                    };
                };
                let candidate =
                    u32::try_from(candidate).map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
                frames[len] = PathFrame {
                    pgno,
                    origin,
                    base,
                    level: 0,
                    child_index: 0,
                    child_count: 0,
                };
                len += 1;
                let mut plan = RemovalPlan {
                    frames,
                    snapshots,
                    survives: [false; FREE_PATH_CAPACITY],
                    clone_slots: [None; FREE_PATH_CAPACITY],
                    len,
                    candidate,
                    clone_count: 0,
                };
                plan.survives[len - 1] = leaf_survives_clear(leaf, base, u64::from(candidate));
                return Ok(Some(plan));
            }

            let branch = BitmapBranch::open(page, selected_txn, BitmapKind::FreePages)
                .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })?;
            let child_span = coverage(expected_level - 1)?;
            if origin == FrameOrigin::Committed {
                branch
                    .verify_local(
                        base,
                        child_span,
                        self.committed_page_count,
                        self.committed_page_count,
                    )
                    .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })?;
            }
            let first_child = if u64::from(minimum_candidate) <= base {
                0
            } else {
                usize::try_from((u64::from(minimum_candidate) - base) / child_span)
                    .map_err(|_| FreeBitmapCowError::CoverageOverflow)?
            };
            let Some(index) = branch.next_summary(first_child) else {
                return if selected_by_summary {
                    Err(FreeBitmapCowError::SummaryMismatch)
                } else {
                    Ok(None)
                };
            };
            let child_base = base
                .checked_add(
                    child_span
                        .checked_mul(index as u64)
                        .ok_or(FreeBitmapCowError::CoverageOverflow)?,
                )
                .ok_or(FreeBitmapCowError::CoverageOverflow)?;
            if child_base >= self.committed_page_count {
                return Err(FreeBitmapCowError::SelectedCoverageOutsideLimit);
            }
            let child = branch.child(index);
            if child == 0 {
                return Err(FreeBitmapCowError::SelectedChildMissing);
            }
            frames[len] = PathFrame {
                pgno,
                origin,
                base,
                level: expected_level,
                child_index: index,
                child_count: count_branch_children(branch),
            };
            len += 1;
            pgno = child;
            expected_level -= 1;
            base = child_base;
            selected_by_summary = true;
        }
    }

    fn preflight<'plan>(
        &self,
        plan: &mut RemovalPlan,
        operation_slots: &'plan mut [PrivatePageScopedOperationSlot],
    ) -> Result<Option<PrivatePageScopedOperation<'plan>>, FreeBitmapCowError> {
        if let Some(frame) = plan.frames[..plan.len]
            .iter()
            .find(|frame| frame.pgno == plan.candidate)
        {
            let planned_private_destination = match frame.origin {
                FrameOrigin::Private(slot) => {
                    self.reservation_planned
                        && self.is_planned_candidate(self.slot_page_number(slot))
                }
                FrameOrigin::Committed | FrameOrigin::Verified(_) => false,
            };
            if !planned_private_destination {
                return Err(FreeBitmapCowError::CandidateIsPathPage(plan.candidate));
            }
        }
        if let Some(&previous) = self.candidates().last() {
            if plan.candidate == previous {
                return Err(FreeBitmapCowError::CandidateAlreadyReserved(plan.candidate));
            }
            if plan.candidate < previous {
                return Err(FreeBitmapCowError::CandidateOrderRegression {
                    previous,
                    current: plan.candidate,
                });
            }
        }
        if self.reservation_planned {
            let expected = self.candidates[self.candidate_len];
            if plan.candidate != expected {
                return Err(FreeBitmapCowError::PlannedCandidateMismatch {
                    expected,
                    actual: plan.candidate,
                });
            }
        }
        match self.indexed_page(plan.candidate) {
            Some(IndexedPage::Replacement) => {
                return Err(FreeBitmapCowError::CandidateIsDraftReplacement(
                    plan.candidate,
                ));
            }
            Some(IndexedPage::Arena(slot)) => {
                if !self.reservation_planned
                    || !self.is_planned_candidate(self.slot_page_number(slot))
                {
                    return Err(FreeBitmapCowError::CandidateIsArenaPage(plan.candidate));
                }
            }
            Some(IndexedPage::Verified(_)) => {
                return Err(FreeBitmapCowError::CandidateIsPathPage(plan.candidate));
            }
            Some(IndexedPage::PlannedCandidate(_)) => {
                return Err(FreeBitmapCowError::ArenaPageConflict(plan.candidate));
            }
            None => {}
        }
        if self.candidate_len == self.candidates.len() {
            return Err(FreeBitmapCowError::CandidateLedgerExhausted);
        }

        for index in (0..plan.len - 1).rev() {
            let child_survives = plan.survives[index + 1];
            let frame = plan.frames[index];
            let remaining = frame
                .child_count
                .checked_sub(u16::from(!child_survives))
                .ok_or(FreeBitmapCowError::SummaryMismatch)?;
            plan.survives[index] = remaining != 0;
        }

        let committed_count = plan.frames[..plan.len]
            .iter()
            .filter(|frame| {
                matches!(
                    frame.origin,
                    FrameOrigin::Committed | FrameOrigin::Verified(_)
                )
            })
            .count();
        if self
            .replacement_len
            .checked_add(committed_count)
            .map_or(true, |needed| needed > self.replacements.len())
        {
            return Err(FreeBitmapCowError::ReplacementLedgerExhausted);
        }

        let clone_count = plan.frames[..plan.len]
            .iter()
            .zip(plan.survives[..plan.len].iter())
            .filter(|(frame, survives)| {
                matches!(
                    frame.origin,
                    FrameOrigin::Committed | FrameOrigin::Verified(_)
                ) && **survives
            })
            .count();
        if self.available_len < clone_count {
            return Err(FreeBitmapCowError::PrivateArenaExhausted);
        }
        plan.clone_count = clone_count;
        let mut next_slot = self.available_len;
        for index in 0..plan.len {
            if matches!(
                plan.frames[index].origin,
                FrameOrigin::Committed | FrameOrigin::Verified(_)
            ) && plan.survives[index]
            {
                next_slot -= 1;
                plan.clone_slots[index] = Some(self.available_slots[next_slot]);
            }
        }
        if let Some(scope) = self.scoped() {
            let mut epoch_steps = 0usize;
            let mut operation_slot_len = 0usize;
            for index in 0..plan.len {
                let frame = plan.frames[index];
                let (slot, binding_steps, steps) = match frame.origin {
                    FrameOrigin::Private(slot) => {
                        let info = self.scoped_slot_info(slot)?;
                        if !matches!(
                            classify_bitmap_pool_state(info.pgno, info.state, self.pending_txn)?,
                            BitmapPrivatePageState::InUse { .. }
                        ) {
                            return Err(FreeBitmapCowError::ArenaPageConflict(info.pgno));
                        }
                        (slot, usize::from(!plan.survives[index]), 1usize)
                    }
                    FrameOrigin::Committed | FrameOrigin::Verified(_) if plan.survives[index] => {
                        let slot =
                            plan.clone_slots[index].expect("surviving clone has a destination");
                        let info = self.scoped_slot_info(slot)?;
                        if info.state != PrivatePagePoolState::Available {
                            return Err(FreeBitmapCowError::ArenaPageConflict(info.pgno));
                        }
                        (slot, 1usize, 2usize)
                    }
                    FrameOrigin::Committed | FrameOrigin::Verified(_) => continue,
                };
                if operation_slot_len == operation_slots.len() {
                    return Err(FreeBitmapCowError::CoverageOverflow);
                }
                let info = self.scoped_slot_info(slot)?;
                operation_slots[operation_slot_len] =
                    PrivatePageScopedOperationSlot::new(slot, info.binding_epoch, binding_steps);
                operation_slot_len += 1;
                epoch_steps = epoch_steps
                    .checked_add(steps)
                    .ok_or(FreeBitmapCowError::CoverageOverflow)?;
            }
            operation_slots[..operation_slot_len]
                .sort_unstable_by_key(|planned| planned.slot_number());
            let operation = self
                .pool()
                .preflight_operation_in_scope(
                    scope,
                    epoch_steps,
                    &mut operation_slots[..operation_slot_len],
                )
                .map_err(FreeBitmapCowError::PrivatePool)?;
            return Ok(Some(operation));
        }

        let snapshot = self
            .pool()
            .mutation_snapshot()
            .map_err(FreeBitmapCowError::PrivatePool)?;
        let mut epoch_steps = 0usize;
        for index in 0..plan.len {
            let frame = plan.frames[index];
            match frame.origin {
                FrameOrigin::Private(slot) => {
                    self.pool()
                        .validate_owner(slot, PrivatePageOwner::Bitmap, self.pending_txn)
                        .map_err(FreeBitmapCowError::PrivatePool)?;
                    epoch_steps = epoch_steps
                        .checked_add(2)
                        .ok_or(FreeBitmapCowError::CoverageOverflow)?;
                }
                FrameOrigin::Committed | FrameOrigin::Verified(_) if plan.survives[index] => {
                    let slot = plan.clone_slots[index].expect("surviving clone has a destination");
                    self.pool()
                        .validate_available(slot)
                        .map_err(FreeBitmapCowError::PrivatePool)?;
                    epoch_steps = epoch_steps
                        .checked_add(3)
                        .ok_or(FreeBitmapCowError::CoverageOverflow)?;
                }
                FrameOrigin::Committed | FrameOrigin::Verified(_) => {}
            }
        }
        self.pool()
            .preflight_mutation(&snapshot, epoch_steps)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        Ok(None)
    }

    fn claim_and_write_for_scoped_operation(
        &mut self,
        operation: &PrivatePageScopedOperation<'_>,
        slot: usize,
        committed_origin: u32,
        bytes: &[u8; PAGE_SIZE],
    ) {
        let binding_epoch = self
            .pool()
            .claim_slot_for_operation_in_scope_prepared(
                operation,
                slot,
                PrivatePageOwner::Bitmap,
                self.pending_txn,
                u64::from(committed_origin),
            )
            .expect("scoped removal preflight retained an exact available destination");
        self.refresh_arena_binding_epoch(slot, binding_epoch)
            .expect("scoped removal retained its canonical binding map");
        self.pool()
            .write_slot_for_operation_in_scope_prepared(
                operation,
                slot,
                PrivatePageOwner::Bitmap,
                self.pending_txn,
                bytes,
            )
            .expect("claimed scoped removal destination remains writable");
    }

    fn write_for_scoped_operation(
        &self,
        operation: &PrivatePageScopedOperation<'_>,
        slot: usize,
        bytes: &[u8; PAGE_SIZE],
    ) {
        self.pool()
            .write_slot_for_operation_in_scope_prepared(
                operation,
                slot,
                PrivatePageOwner::Bitmap,
                self.pending_txn,
                bytes,
            )
            .expect("scoped removal preflight retained exact private ownership");
    }

    fn return_for_scoped_operation(
        &mut self,
        operation: &PrivatePageScopedOperation<'_>,
        slot: usize,
        disposition: PrivatePageReturn,
    ) {
        let binding_epoch = self
            .pool()
            .return_slot_for_operation_in_scope_prepared(
                operation,
                slot,
                PrivatePageOwner::Bitmap,
                self.pending_txn,
                disposition,
            )
            .expect("scoped removal preflight retained exact private ownership");
        self.refresh_arena_binding_epoch(slot, binding_epoch)
            .expect("scoped removal retained its canonical binding map");
    }

    fn apply(
        &mut self,
        plan: RemovalPlan,
        operation: Option<&PrivatePageScopedOperation<'_>>,
    ) -> Option<ReservedFreePage> {
        self.available_len -= plan.clone_count;
        let mut child_ref = 0u32;
        for index in (0..plan.len).rev() {
            let frame = plan.frames[index];
            if !plan.survives[index] {
                if let FrameOrigin::Private(slot) = frame.origin {
                    self.release_private(slot, operation);
                }
                child_ref = 0;
                continue;
            }
            match (frame.level, frame.origin) {
                (0, origin @ (FrameOrigin::Committed | FrameOrigin::Verified(_))) => {
                    let slot = plan.clone_slots[index].unwrap();
                    let pgno = self.slot_page_number(slot);
                    let source = match origin {
                        FrameOrigin::Committed => &plan.snapshots[index],
                        FrameOrigin::Verified(verified) => &self.verified_pages[verified].bytes,
                        FrameOrigin::Private(_) => unreachable!(),
                    };
                    let mut bytes = [0u8; PAGE_SIZE];
                    encode_leaf_clear(
                        &mut bytes,
                        source,
                        self.pending_txn,
                        frame.base,
                        plan.candidate,
                    );
                    if let Some(operation) = operation {
                        self.claim_and_write_for_scoped_operation(
                            operation, slot, frame.pgno, &bytes,
                        );
                    } else {
                        self.claim_slot(slot, frame.pgno)
                            .expect("removal preflight retained an available destination");
                        self.write_slot(slot, &bytes)
                            .expect("claimed removal destination remains writable");
                    }
                    child_ref = pgno;
                }
                (0, FrameOrigin::Private(slot)) => {
                    if let Some(operation) = operation {
                        let mut bytes = plan.snapshots[index];
                        mutate_leaf_clear(&mut bytes, self.pending_txn, frame.base, plan.candidate);
                        self.write_for_scoped_operation(operation, slot, &bytes);
                    } else {
                        self.edit_slot(slot, |page| {
                            mutate_leaf_clear(page, self.pending_txn, frame.base, plan.candidate);
                        })
                        .expect("removal preflight retained exact private-page ownership");
                    }
                    child_ref = frame.pgno;
                }
                (_, origin @ (FrameOrigin::Committed | FrameOrigin::Verified(_))) => {
                    let slot = plan.clone_slots[index].unwrap();
                    let pgno = self.slot_page_number(slot);
                    let source = match origin {
                        FrameOrigin::Committed => &plan.snapshots[index],
                        FrameOrigin::Verified(verified) => &self.verified_pages[verified].bytes,
                        FrameOrigin::Private(_) => unreachable!(),
                    };
                    let mut bytes = [0u8; PAGE_SIZE];
                    encode_branch_child(
                        &mut bytes,
                        source,
                        self.pending_txn,
                        frame.level,
                        frame.child_index,
                        child_ref,
                    );
                    if let Some(operation) = operation {
                        self.claim_and_write_for_scoped_operation(
                            operation, slot, frame.pgno, &bytes,
                        );
                    } else {
                        self.claim_slot(slot, frame.pgno)
                            .expect("removal preflight retained an available destination");
                        self.write_slot(slot, &bytes)
                            .expect("claimed removal destination remains writable");
                    }
                    child_ref = pgno;
                }
                (_, FrameOrigin::Private(slot)) => {
                    if let Some(operation) = operation {
                        let mut bytes = plan.snapshots[index];
                        mutate_branch_child(
                            &mut bytes,
                            self.pending_txn,
                            frame.level,
                            frame.child_index,
                            child_ref,
                        );
                        self.write_for_scoped_operation(operation, slot, &bytes);
                    } else {
                        self.edit_slot(slot, |page| {
                            mutate_branch_child(
                                page,
                                self.pending_txn,
                                frame.level,
                                frame.child_index,
                                child_ref,
                            );
                        })
                        .expect("removal preflight retained exact private-page ownership");
                    }
                    child_ref = frame.pgno;
                }
            }
        }

        for frame in &plan.frames[..plan.len] {
            if matches!(
                frame.origin,
                FrameOrigin::Committed | FrameOrigin::Verified(_)
            ) {
                self.replacements[self.replacement_len] = frame.pgno;
                self.replacement_len += 1;
                if frame.origin == FrameOrigin::Committed {
                    debug_assert!(
                        page_index_find(self.index_nodes, self.index_root, frame.pgno).is_none()
                    );
                    if self.scoped().is_some() {
                        page_index_insert_existing_prechecked(
                            self.index_nodes,
                            &mut self.index_root,
                            self.scope_capacity + self.replacement_len - 1,
                            frame.pgno,
                            IndexedPage::Replacement,
                        );
                    } else {
                        debug_assert!(self.index_len < self.index_nodes.len());
                        page_index_insert_prechecked(
                            self.index_nodes,
                            &mut self.index_root,
                            &mut self.index_len,
                            frame.pgno,
                            IndexedPage::Replacement,
                        );
                    }
                } else {
                    let previous = page_index_replace(
                        self.index_nodes,
                        self.index_root,
                        frame.pgno,
                        IndexedPage::Replacement,
                    );
                    debug_assert_eq!(
                        previous,
                        Some(IndexedPage::Verified(match frame.origin {
                            FrameOrigin::Verified(slot) => slot,
                            _ => unreachable!(),
                        }))
                    );
                }
            }
        }
        self.candidates[self.candidate_len] = plan.candidate;
        self.candidate_len += 1;
        self.root = child_ref;
        Some(ReservedFreePage {
            pgno: plan.candidate,
        })
    }

    fn release_private(&mut self, slot: usize, operation: Option<&PrivatePageScopedOperation<'_>>) {
        if let Some(operation) = operation {
            self.return_for_scoped_operation(operation, slot, PrivatePageReturn::Available);
        } else {
            self.return_slot(slot, PrivatePageReturn::Available)
                .expect("removal preflight retained exact private-page ownership");
        }
        self.available_slots[self.available_len] = slot;
        self.available_len += 1;
    }

    fn rebuild_available_slots(&mut self) {
        self.available_len = 0;
        for slot in (0..self.pool().len()).rev() {
            if self.slot_state(slot) == BitmapPrivatePageState::Available {
                self.available_slots[self.available_len] = slot;
                self.available_len += 1;
            }
        }
    }

    fn indexed_page(&self, pgno: u32) -> Option<IndexedPage> {
        #[cfg(test)]
        {
            let (page, probes) = page_index_find_counted(self.index_nodes, self.index_root, pgno);
            self.index_probes
                .set(self.index_probes.get().saturating_add(probes));
            page
        }
        #[cfg(not(test))]
        {
            page_index_find(self.index_nodes, self.index_root, pgno)
        }
    }

    #[cfg(test)]
    fn index_probe_count(&self) -> usize {
        self.index_probes.get()
    }

    #[cfg(test)]
    fn scoped_validation_pass_count(&self) -> usize {
        self.scoped_validation_passes.get()
    }

    #[cfg(test)]
    fn apply_preflight_check_count(&self) -> usize {
        self.apply_preflight_checks.get()
    }

    #[cfg(test)]
    const fn shared_preparation_work(&self) -> usize {
        self.shared_preparation_work
    }

    #[cfg(test)]
    fn test_page_at(&self, slot: usize) -> [u8; PAGE_SIZE] {
        self.pool().test_bytes(slot).unwrap()
    }

    #[cfg(test)]
    fn test_state_at(&self, slot: usize) -> BitmapPrivatePageState {
        self.slot_state(slot)
    }

    #[cfg(test)]
    fn test_authorization_at(&self, slot: usize) -> PrivatePageAuthorization {
        self.slot_authorization(slot)
    }

    #[cfg(test)]
    fn test_page_number_at(&self, slot: usize) -> u32 {
        self.slot_page_number(slot)
    }
}

struct InsertPreflight<'cow, 'arena, 'slots, 'scope, 'plan, S: CommittedPageSource + ?Sized> {
    cow: &'cow FreeBitmapCow<'arena, 'slots, 'scope, S>,
    pages: &'plan [u32],
    governing_page_count: u64,
    scratch: &'plan mut [FreeBitmapInsertPage],
    scratch_len: usize,
    source_index_root: usize,
    root: u32,
    root_level: u16,
    desired_level: u16,
    planned_root: usize,
    previous_path: [usize; FREE_PATH_CAPACITY],
    destination_count: usize,
    new_index_count: usize,
    available_cursor: usize,
    usable_available: usize,
    demoted_slots: [usize; FREE_PATH_CAPACITY - 1],
    demoted_len: usize,
    demoted_destination_slots: [usize; FREE_PATH_CAPACITY - 1],
    demoted_destination_len: usize,
    demoted_destination_cursor: usize,
    auto_release_pages: [u32; FREE_PATH_CAPACITY - 1],
    auto_release_len: usize,
    auto_reinserted_candidates: usize,
    auto_reinserted_appended: usize,
    inserted: usize,
    already_free: usize,
    committed_replacements: usize,
    new_bitmap_pages: usize,
}

impl<'cow, 'arena, 'slots, 'scope, 'plan, S: CommittedPageSource + ?Sized>
    InsertPreflight<'cow, 'arena, 'slots, 'scope, 'plan, S>
{
    fn new(
        cow: &'cow FreeBitmapCow<'arena, 'slots, 'scope, S>,
        pages: &'plan [u32],
        governing_page_count: u64,
        scratch: &'plan mut [FreeBitmapInsertPage],
    ) -> Result<Self, FreeBitmapCowError> {
        if !(cow.committed_page_count..=cow.pending_page_count).contains(&governing_page_count) {
            return Err(FreeBitmapCowError::PageCountOutOfRange(
                governing_page_count,
            ));
        }
        let mut previous = None;
        for &pgno in pages {
            if pgno < 2 || u64::from(pgno) >= governing_page_count {
                return Err(FreeBitmapCowError::InsertPageOutOfBounds(pgno));
            }
            if let Some(prior) = previous {
                if pgno <= prior {
                    return Err(FreeBitmapCowError::InsertPageOrderRegression {
                        previous: prior,
                        current: pgno,
                    });
                }
            }
            previous = Some(pgno);
            if let Some(IndexedPage::Arena(slot)) = cow.indexed_page(pgno) {
                if matches!(
                    cow.slot_state(slot),
                    BitmapPrivatePageState::InUse { .. } | BitmapPrivatePageState::Foreign
                ) {
                    return Err(FreeBitmapCowError::InsertPageInUse(pgno));
                }
            }
            if matches!(cow.indexed_page(pgno), Some(IndexedPage::Verified(_))) {
                return Err(FreeBitmapCowError::InsertPageIsBitmapPath(pgno));
            }
        }
        let desired_level = minimum_level(governing_page_count)?;
        Ok(Self {
            cow,
            pages,
            governing_page_count,
            scratch,
            scratch_len: 0,
            source_index_root: NO_INDEX,
            root: cow.root,
            root_level: desired_level,
            desired_level,
            planned_root: NO_INDEX,
            previous_path: [NO_INDEX; FREE_PATH_CAPACITY],
            destination_count: 0,
            new_index_count: 0,
            available_cursor: cow.available_len,
            usable_available: 0,
            demoted_slots: [NO_INDEX; FREE_PATH_CAPACITY - 1],
            demoted_len: 0,
            demoted_destination_slots: [NO_INDEX; FREE_PATH_CAPACITY - 1],
            demoted_destination_len: 0,
            demoted_destination_cursor: 0,
            auto_release_pages: [0; FREE_PATH_CAPACITY - 1],
            auto_release_len: 0,
            auto_reinserted_candidates: 0,
            auto_reinserted_appended: 0,
            inserted: 0,
            already_free: 0,
            committed_replacements: 0,
            new_bitmap_pages: 0,
        })
    }

    fn plan(mut self) -> Result<PreparedFreeBitmapInsertion<'slots, 'plan>, FreeBitmapCowError> {
        self.usable_available = self.count_usable_available();
        self.prepare_root()?;
        let mut requested = 0usize;
        let mut automatic = 0usize;
        while requested < self.pages.len() || automatic < self.auto_release_len {
            let pgno = if automatic == self.auto_release_len
                || (requested < self.pages.len()
                    && self.pages[requested] < self.auto_release_pages[automatic])
            {
                let pgno = self.pages[requested];
                requested += 1;
                pgno
            } else {
                let pgno = self.auto_release_pages[automatic];
                automatic += 1;
                pgno
            };
            self.plan_one(pgno)?;
        }
        let pool_snapshot = if let Some(scope) = self.cow.scoped() {
            self.cow.pool().mutation_snapshot_in_scope(scope)
        } else {
            self.cow.pool().mutation_snapshot()
        }
        .map_err(FreeBitmapCowError::PrivatePool)?;
        Ok(PreparedFreeBitmapInsertion {
            pages: self.pages,
            scratch: self.scratch,
            scratch_len: self.scratch_len,
            root: self.root,
            governing_page_count: self.governing_page_count,
            destination_count: self.destination_count,
            demoted_slots: self.demoted_slots,
            demoted_len: self.demoted_len,
            auto_release_pages: self.auto_release_pages,
            auto_release_len: self.auto_release_len,
            auto_reinserted_candidates: self.auto_reinserted_candidates,
            auto_reinserted_appended: self.auto_reinserted_appended,
            inserted: self.inserted,
            already_free: self.already_free,
            committed_replacements: self.committed_replacements,
            new_bitmap_pages: self.new_bitmap_pages,
            pool_snapshot,
        })
    }

    fn prepare_root(&mut self) -> Result<(), FreeBitmapCowError> {
        if self.root == 0 {
            self.root_level = self.desired_level;
            return Ok(());
        }

        let mut bytes = [0u8; PAGE_SIZE];
        let (mut origin, mut selected_txn) = self.copy_source(self.root, &mut bytes)?;
        let mut header =
            PageHeader::decode(&bytes, selected_txn).map_err(|cause| FreeBitmapCowError::Page {
                pgno: self.root,
                cause: BitmapPageError::from(cause),
            })?;
        let mut level = match header.page_type {
            PageType::BitmapLeaf => 0,
            PageType::BitmapBranch => header.level,
            page_type => {
                return Err(FreeBitmapCowError::UnexpectedPageType {
                    pgno: self.root,
                    page_type,
                });
            }
        };
        let committed_level = minimum_level(self.cow.committed_page_count)?;
        let pending_level = minimum_level(self.cow.pending_page_count)?;
        let valid_level = match origin {
            InsertPageOrigin::Committed | InsertPageOrigin::Verified(_) => level == committed_level,
            InsertPageOrigin::Private(_) => level >= committed_level && level <= pending_level,
            InsertPageOrigin::New | InsertPageOrigin::None => false,
        };
        if !valid_level {
            return Err(FreeBitmapCowError::RootLevel {
                expected: committed_level,
                actual: level,
            });
        }

        while level > self.desired_level {
            let InsertPageOrigin::Private(slot) = origin else {
                return Err(FreeBitmapCowError::NonCanonicalRootDemotion);
            };
            let branch = BitmapBranch::open(&bytes, selected_txn, BitmapKind::FreePages).map_err(
                |cause| FreeBitmapCowError::Page {
                    pgno: self.root,
                    cause,
                },
            )?;
            self.verify_branch(branch, self.root, origin, 0, level)?;
            if header.item_count != 1
                || branch.child(0) == 0
                || !branch.summary_bit(0)
                || (1..BITMAP_FANOUT as usize)
                    .any(|index| branch.child(index) != 0 || branch.summary_bit(index))
            {
                return Err(FreeBitmapCowError::NonCanonicalRootDemotion);
            }
            self.demoted_slots[self.demoted_len] = slot;
            self.demoted_len += 1;
            self.root = branch.child(0);
            level -= 1;
            let source = self.copy_source(self.root, &mut bytes)?;
            origin = source.0;
            selected_txn = source.1;
            header = PageHeader::decode(&bytes, selected_txn).map_err(|cause| {
                FreeBitmapCowError::Page {
                    pgno: self.root,
                    cause: BitmapPageError::from(cause),
                }
            })?;
            let actual = match header.page_type {
                PageType::BitmapLeaf => 0,
                PageType::BitmapBranch => header.level,
                page_type => {
                    return Err(FreeBitmapCowError::UnexpectedPageType {
                        pgno: self.root,
                        page_type,
                    });
                }
            };
            if actual != level {
                return Err(FreeBitmapCowError::ChildLevel {
                    expected: level,
                    actual,
                });
            }
        }
        self.root_level = level;
        self.classify_demoted_slots();
        self.usable_available += self.demoted_destination_len;

        let retained_root =
            self.append_retained_source(self.root, 0, level, bytes, origin, selected_txn)?;
        let retained_position = usize::from(self.desired_level - level);
        self.previous_path[retained_position] = retained_root;
        if level == self.desired_level {
            self.planned_root = retained_root;
        }

        if level < self.desired_level {
            let mut child = self.root;
            for promotion_level in level + 1..=self.desired_level {
                let node = self.append_new(0, promotion_level)?;
                self.ensure_changed(node)?;
                mutate_branch_child(
                    &mut self.scratch[node].bytes,
                    self.cow.pending_txn,
                    promotion_level,
                    0,
                    child,
                );
                child = self.scratch[node].result_pgno;
                let position = usize::from(self.desired_level - promotion_level);
                self.previous_path[position] = node;
                self.planned_root = node;
            }
            self.root = child;
            self.root_level = self.desired_level;
        }
        Ok(())
    }

    fn classify_demoted_slots(&mut self) {
        for &slot in &self.demoted_slots[..self.demoted_len] {
            let pgno = self.cow.slot_page_number(slot);
            let auto_release = match self.cow.slot_authorization(slot) {
                PrivatePageAuthorization::CommittedFree if self.cow.is_planned_candidate(pgno) => {
                    self.auto_reinserted_candidates += 1;
                    true
                }
                PrivatePageAuthorization::Appended
                    if u64::from(pgno) < self.governing_page_count =>
                {
                    self.auto_reinserted_appended += 1;
                    true
                }
                PrivatePageAuthorization::CommittedFree
                | PrivatePageAuthorization::SafelyReclaimed
                | PrivatePageAuthorization::Appended => false,
            };
            if auto_release {
                let mut at = self.auto_release_len;
                while at != 0 && self.auto_release_pages[at - 1] > pgno {
                    self.auto_release_pages[at] = self.auto_release_pages[at - 1];
                    at -= 1;
                }
                self.auto_release_pages[at] = pgno;
                self.auto_release_len += 1;
            } else if u64::from(pgno) < self.governing_page_count
                && !self.cow.is_planned_candidate(pgno)
            {
                self.demoted_destination_slots[self.demoted_destination_len] = slot;
                self.demoted_destination_len += 1;
            }
        }
    }

    fn is_release_page(&self, pgno: u32) -> bool {
        self.pages.binary_search(&pgno).is_ok()
            || self.auto_release_pages[..self.auto_release_len]
                .binary_search(&pgno)
                .is_ok()
    }

    fn plan_one(&mut self, pgno: u32) -> Result<(), FreeBitmapCowError> {
        let mut path = [NO_INDEX; FREE_PATH_CAPACITY];
        for position in 0..=usize::from(self.desired_level) {
            let level = self.desired_level - position as u16;
            let span = coverage(level)?;
            let base = (u64::from(pgno) / span) * span;
            let cached = self.previous_path[position];
            let node = if cached != NO_INDEX
                && self.scratch[cached].base == base
                && self.scratch[cached].level == level
            {
                cached
            } else if position == 0 {
                if self.planned_root != NO_INDEX {
                    self.planned_root
                } else if self.root == 0 {
                    self.append_new(0, level)?
                } else {
                    self.append_source(self.root, 0, level)?
                }
            } else {
                let parent = path[position - 1];
                let child_span = coverage(level)?;
                let child_index = usize::try_from((base - self.scratch[parent].base) / child_span)
                    .map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
                if child_index >= BITMAP_FANOUT as usize {
                    return Err(FreeBitmapCowError::CoverageOverflow);
                }
                let child = raw_branch_child(&self.scratch[parent].bytes, child_index);
                if child == 0 {
                    self.append_new(base, level)?
                } else {
                    self.append_source(child, base, level)?
                }
            };
            path[position] = node;
        }

        let leaf_index = usize::from(self.desired_level);
        let leaf = path[leaf_index];
        let local = u64::from(pgno) - self.scratch[leaf].base;
        let word_index =
            usize::try_from(local / 64).map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
        let mask = 1u64 << (local % 64);
        let word = raw_leaf_word(&self.scratch[leaf].bytes, word_index);
        if word & mask != 0 {
            self.already_free += 1;
            self.previous_path = path;
            return Ok(());
        }

        self.ensure_changed(leaf)?;
        mutate_leaf_set(
            &mut self.scratch[leaf].bytes,
            self.cow.pending_txn,
            self.scratch[leaf].base,
            pgno,
        );
        let mut child = self.scratch[leaf].result_pgno;
        for position in (0..leaf_index).rev() {
            let parent = path[position];
            let level = self.scratch[parent].level;
            let child_level = level - 1;
            let child_span = coverage(child_level)?;
            let child_index = usize::try_from(
                (self.scratch[path[position + 1]].base - self.scratch[parent].base) / child_span,
            )
            .map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
            self.ensure_changed(parent)?;
            mutate_branch_child(
                &mut self.scratch[parent].bytes,
                self.cow.pending_txn,
                level,
                child_index,
                child,
            );
            child = self.scratch[parent].result_pgno;
        }
        self.root = child;
        self.planned_root = path[0];
        self.previous_path = path;
        self.inserted += 1;
        Ok(())
    }

    fn append_new(&mut self, base: u64, level: u16) -> Result<usize, FreeBitmapCowError> {
        let index = self.reserve_scratch()?;
        self.scratch[index] = FreeBitmapInsertPage::empty();
        self.scratch[index].base = base;
        self.scratch[index].level = level;
        self.scratch[index].origin = InsertPageOrigin::New;
        Ok(index)
    }

    fn append_source(
        &mut self,
        pgno: u32,
        base: u64,
        expected_level: u16,
    ) -> Result<usize, FreeBitmapCowError> {
        let mut bytes = [0u8; PAGE_SIZE];
        let (origin, selected_txn) = self.copy_source(pgno, &mut bytes)?;
        self.append_retained_source(pgno, base, expected_level, bytes, origin, selected_txn)
    }

    fn append_retained_source(
        &mut self,
        pgno: u32,
        base: u64,
        expected_level: u16,
        bytes: [u8; PAGE_SIZE],
        origin: InsertPageOrigin,
        selected_txn: u64,
    ) -> Result<usize, FreeBitmapCowError> {
        let index = self.reserve_scratch()?;
        if self.is_release_page(pgno) {
            return Err(FreeBitmapCowError::InsertPageIsBitmapPath(pgno));
        }
        if insert_source_find(self.scratch, self.source_index_root, pgno).is_some() {
            return Err(FreeBitmapCowError::RepeatedCommittedPage(pgno));
        }
        self.verify_source(&bytes, pgno, origin, selected_txn, base, expected_level)?;
        self.scratch[index] = FreeBitmapInsertPage {
            bytes,
            base,
            level: expected_level,
            source_pgno: pgno,
            result_pgno: pgno,
            origin,
            destination_slot: NO_INDEX,
            changed: false,
            source_left: NO_INDEX,
            source_right: NO_INDEX,
            source_height: 1,
        };
        self.source_index_root =
            insert_source_insert_unique(self.scratch, self.source_index_root, index);
        Ok(index)
    }

    fn reserve_scratch(&mut self) -> Result<usize, FreeBitmapCowError> {
        if self.scratch_len == self.scratch.len() {
            return Err(FreeBitmapCowError::InsertScratchExhausted {
                required: self.scratch_len + 1,
                available: self.scratch.len(),
            });
        }
        let index = self.scratch_len;
        self.scratch_len += 1;
        Ok(index)
    }

    fn ensure_changed(&mut self, index: usize) -> Result<(), FreeBitmapCowError> {
        if self.scratch[index].changed {
            return Ok(());
        }
        let origin = self.scratch[index].origin;
        match origin {
            InsertPageOrigin::Private(_) => {}
            InsertPageOrigin::Committed | InsertPageOrigin::Verified(_) => {
                let required = self.cow.replacement_len + self.committed_replacements + 1;
                if required > self.cow.replacements.len() {
                    return Err(FreeBitmapCowError::InsufficientResourceBudget {
                        resource: ReservationResource::ReplacementPages,
                        required,
                        available: self.cow.replacements.len(),
                    });
                }
                if origin == InsertPageOrigin::Committed {
                    let required_index = self.cow.index_len + self.new_index_count + 1;
                    if required_index > self.cow.index_nodes.len() {
                        return Err(FreeBitmapCowError::InsufficientResourceBudget {
                            resource: ReservationResource::IndexNodes,
                            required: required_index,
                            available: self.cow.index_nodes.len(),
                        });
                    }
                    self.new_index_count += 1;
                }
                let slot = self.next_destination_slot()?;
                self.scratch[index].destination_slot = slot;
                self.scratch[index].result_pgno = self.cow.slot_page_number(slot);
                self.committed_replacements += 1;
            }
            InsertPageOrigin::New => {
                let slot = self.next_destination_slot()?;
                self.scratch[index].destination_slot = slot;
                self.scratch[index].result_pgno = self.cow.slot_page_number(slot);
                self.new_bitmap_pages += 1;
            }
            InsertPageOrigin::None => unreachable!(),
        }
        self.scratch[index].changed = true;
        Ok(())
    }

    fn next_destination_slot(&mut self) -> Result<usize, FreeBitmapCowError> {
        let required = self.destination_count + 1;
        if required > self.usable_available {
            return Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::ArenaPages,
                required,
                available: self.usable_available,
            });
        }
        let slot = if self.demoted_destination_cursor < self.demoted_destination_len {
            let slot = self.demoted_destination_slots[self.demoted_destination_cursor];
            self.demoted_destination_cursor += 1;
            slot
        } else {
            loop {
                self.available_cursor -= 1;
                let slot = self.cow.available_slots[self.available_cursor];
                let pgno = self.cow.slot_page_number(slot);
                if self.cow.slot_state(slot) == BitmapPrivatePageState::Available
                    && u64::from(pgno) < self.governing_page_count
                    && !self.is_release_page(pgno)
                {
                    break slot;
                }
            }
        };
        self.destination_count = required;
        Ok(slot)
    }

    fn count_usable_available(&self) -> usize {
        self.cow.available_slots[..self.cow.available_len]
            .iter()
            .filter(|&&slot| {
                self.cow.slot_state(slot) == BitmapPrivatePageState::Available
                    && u64::from(self.cow.slot_page_number(slot)) < self.governing_page_count
                    && self
                        .pages
                        .binary_search(&self.cow.slot_page_number(slot))
                        .is_err()
            })
            .count()
    }

    fn copy_source(
        &self,
        pgno: u32,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<(InsertPageOrigin, u64), FreeBitmapCowError> {
        if pgno < 2 || u64::from(pgno) >= self.cow.pending_page_count {
            return Err(FreeBitmapCowError::RootPageOutOfBounds(pgno));
        }
        match self.cow.indexed_page(pgno) {
            Some(IndexedPage::Arena(slot)) => match self.cow.slot_state(slot) {
                BitmapPrivatePageState::InUse { .. } => {
                    let authority = self.cow.authority(slot)?;
                    let private = if let Some(scope) = self.cow.scoped() {
                        self.cow
                            .pool()
                            .borrow_page_in_scope(scope, &authority)
                            .map_err(FreeBitmapCowError::PrivatePool)?
                    } else {
                        self.cow
                            .pool()
                            .borrow_page(&authority)
                            .map_err(FreeBitmapCowError::PrivatePool)?
                    };
                    destination.copy_from_slice(&private[..]);
                    Ok((InsertPageOrigin::Private(slot), self.cow.pending_txn))
                }
                BitmapPrivatePageState::Available
                | BitmapPrivatePageState::ReleasedFree
                | BitmapPrivatePageState::ReleasedTail => {
                    Err(FreeBitmapCowError::ArenaPageConflict(pgno))
                }
                BitmapPrivatePageState::Foreign => Err(FreeBitmapCowError::ArenaPageConflict(pgno)),
            },
            Some(IndexedPage::Verified(verified)) => {
                destination.copy_from_slice(&self.cow.verified_pages[verified].bytes);
                Ok((InsertPageOrigin::Verified(verified), self.cow.selected_txn))
            }
            Some(IndexedPage::Replacement) => Err(FreeBitmapCowError::RepeatedCommittedPage(pgno)),
            Some(IndexedPage::PlannedCandidate(_)) => {
                Err(FreeBitmapCowError::ArenaPageConflict(pgno))
            }
            None => {
                if u64::from(pgno) >= self.cow.committed_page_count {
                    return Err(FreeBitmapCowError::RootPageOutOfBounds(pgno));
                }
                self.cow
                    .committed
                    .read_page(pgno, destination)
                    .map_err(FreeBitmapCowError::Source)?;
                Ok((InsertPageOrigin::Committed, self.cow.selected_txn))
            }
        }
    }

    fn verify_source(
        &self,
        bytes: &[u8; PAGE_SIZE],
        pgno: u32,
        origin: InsertPageOrigin,
        selected_txn: u64,
        base: u64,
        expected_level: u16,
    ) -> Result<(), FreeBitmapCowError> {
        if let InsertPageOrigin::Verified(verified) = origin {
            let cached = &self.cow.verified_pages[verified];
            if cached.pgno != pgno || cached.base != base || cached.level != expected_level {
                return Err(FreeBitmapCowError::VerifiedPageIdentityMismatch {
                    pgno,
                    expected_base: base,
                    actual_base: cached.base,
                    expected_level,
                    actual_level: cached.level,
                });
            }
            return Ok(());
        }
        let header =
            PageHeader::decode(bytes, selected_txn).map_err(|cause| FreeBitmapCowError::Page {
                pgno,
                cause: BitmapPageError::from(cause),
            })?;
        let actual_level = match header.page_type {
            PageType::BitmapLeaf => 0,
            PageType::BitmapBranch => header.level,
            page_type => {
                return Err(FreeBitmapCowError::UnexpectedPageType { pgno, page_type });
            }
        };
        if actual_level != expected_level {
            return Err(FreeBitmapCowError::ChildLevel {
                expected: expected_level,
                actual: actual_level,
            });
        }
        if expected_level == 0 {
            BitmapLeaf::open(bytes, selected_txn, BitmapKind::FreePages)
                .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })?
                .verify_local(BitmapKind::FreePages, base, self.governing_page_count)
                .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })
        } else {
            let branch = BitmapBranch::open(bytes, selected_txn, BitmapKind::FreePages)
                .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })?;
            self.verify_branch(branch, pgno, origin, base, expected_level)
        }
    }

    fn verify_branch(
        &self,
        branch: BitmapBranch<'_>,
        pgno: u32,
        origin: InsertPageOrigin,
        base: u64,
        level: u16,
    ) -> Result<(), FreeBitmapCowError> {
        let child_span = coverage(level - 1)?;
        let physical_limit = if matches!(
            origin,
            InsertPageOrigin::Committed | InsertPageOrigin::Verified(_)
        ) {
            self.cow.committed_page_count
        } else {
            self.governing_page_count
        };
        branch
            .verify_local(base, child_span, self.governing_page_count, physical_limit)
            .map_err(|cause| FreeBitmapCowError::Page { pgno, cause })?;
        for index in 0..BITMAP_FANOUT as usize {
            if branch.summary_bit(index) != (branch.child(index) != 0) {
                return Err(FreeBitmapCowError::CommittedSummaryMismatch(pgno));
            }
        }
        Ok(())
    }
}

fn raw_leaf_word(page: &[u8; PAGE_SIZE], index: usize) -> u64 {
    let offset = SUMMARY_OFFSET + index * 8;
    u64::from_le_bytes(page[offset..offset + 8].try_into().unwrap())
}

fn reservation_hash_u64(mut hash: u64, value: u64) -> u64 {
    const PRIME: u64 = 1_099_511_628_211;
    for byte in value.to_le_bytes() {
        hash ^= u64::from(byte);
        hash = hash.wrapping_mul(PRIME);
    }
    hash
}

fn reservation_pages_fingerprint(pages: &[u32]) -> u64 {
    let mut hash = reservation_hash_u64(1_469_598_103_934_665_603, pages.len() as u64);
    for &pgno in pages {
        hash = reservation_hash_u64(hash, u64::from(pgno));
    }
    hash
}

fn reservation_capacity_fingerprint(
    candidates: &[u32],
    verified_pages: &[VerifiedBitmapPage],
) -> u64 {
    let mut hash = reservation_pages_fingerprint(candidates);
    hash = reservation_hash_u64(hash, verified_pages.len() as u64);
    for page in verified_pages {
        for value in [
            u64::from(page.pgno),
            page.base,
            u64::from(page.level),
            page.parent.wrapping_add(1) as u64,
            u64::from(page.remaining),
            page.survives as u64,
        ] {
            hash = reservation_hash_u64(hash, value);
        }
        for byte in page.bytes {
            hash ^= u64::from(byte);
            hash = hash.wrapping_mul(1_099_511_628_211);
        }
    }
    hash
}

fn reservation_source_fingerprint(nodes: &[FreeBitmapReservationSourceNode]) -> u64 {
    let mut hash = reservation_hash_u64(1_469_598_103_934_665_603, nodes.len() as u64);
    for node in nodes {
        hash = reservation_hash_u64(hash, u64::from(node.pgno));
        hash = reservation_hash_u64(hash, node.kind as u64);
        hash = reservation_hash_u64(hash, node.required as u64);
    }
    hash
}

fn raw_branch_child(page: &[u8; PAGE_SIZE], index: usize) -> u32 {
    let offset = CHILDREN_OFFSET + index * 4;
    u32::from_le_bytes(page[offset..offset + 4].try_into().unwrap())
}

fn mutate_leaf_set(page: &mut [u8; PAGE_SIZE], pending_txn: u64, base: u64, pgno: u32) {
    let local = u64::from(pgno).checked_sub(base).unwrap();
    let word_index = usize::try_from(local / 64).unwrap();
    let bit = (local % 64) as u32;
    let offset = SUMMARY_OFFSET + word_index * 8;
    let word = raw_leaf_word(page, word_index);
    page[offset..offset + 8].copy_from_slice(&(word | (1u64 << bit)).to_le_bytes());
    let item_count = (0..BITMAP_LEAF_WORDS)
        .filter(|&index| raw_leaf_word(page, index) != 0)
        .count();
    write_bitmap_header(
        page,
        PageType::BitmapLeaf,
        pending_txn,
        u16::try_from(item_count).unwrap(),
        0,
        LEAF_LOWER,
    );
}

fn insert_source_find(nodes: &[FreeBitmapInsertPage], mut root: usize, pgno: u32) -> Option<usize> {
    while root != NO_INDEX {
        if pgno < nodes[root].source_pgno {
            root = nodes[root].source_left;
        } else if pgno > nodes[root].source_pgno {
            root = nodes[root].source_right;
        } else {
            return Some(root);
        }
    }
    None
}

fn insert_source_insert_unique(
    nodes: &mut [FreeBitmapInsertPage],
    root: usize,
    new_index: usize,
) -> usize {
    if root == NO_INDEX {
        return new_index;
    }
    let pgno = nodes[new_index].source_pgno;
    if pgno < nodes[root].source_pgno {
        nodes[root].source_left =
            insert_source_insert_unique(nodes, nodes[root].source_left, new_index);
    } else {
        nodes[root].source_right =
            insert_source_insert_unique(nodes, nodes[root].source_right, new_index);
    }
    insert_source_update_height(nodes, root);
    let balance = insert_source_balance(nodes, root);
    if balance > 1 {
        let left = nodes[root].source_left;
        if pgno > nodes[left].source_pgno {
            nodes[root].source_left = insert_source_rotate_left(nodes, left);
        }
        return insert_source_rotate_right(nodes, root);
    }
    if balance < -1 {
        let right = nodes[root].source_right;
        if pgno < nodes[right].source_pgno {
            nodes[root].source_right = insert_source_rotate_right(nodes, right);
        }
        return insert_source_rotate_left(nodes, root);
    }
    root
}

fn insert_source_height(nodes: &[FreeBitmapInsertPage], index: usize) -> u8 {
    if index == NO_INDEX {
        0
    } else {
        nodes[index].source_height
    }
}

fn insert_source_update_height(nodes: &mut [FreeBitmapInsertPage], index: usize) {
    nodes[index].source_height = 1 + insert_source_height(nodes, nodes[index].source_left)
        .max(insert_source_height(nodes, nodes[index].source_right));
}

fn insert_source_balance(nodes: &[FreeBitmapInsertPage], index: usize) -> i16 {
    i16::from(insert_source_height(nodes, nodes[index].source_left))
        - i16::from(insert_source_height(nodes, nodes[index].source_right))
}

fn insert_source_rotate_left(nodes: &mut [FreeBitmapInsertPage], root: usize) -> usize {
    let pivot = nodes[root].source_right;
    let middle = nodes[pivot].source_left;
    nodes[pivot].source_left = root;
    nodes[root].source_right = middle;
    insert_source_update_height(nodes, root);
    insert_source_update_height(nodes, pivot);
    pivot
}

fn insert_source_rotate_right(nodes: &mut [FreeBitmapInsertPage], root: usize) -> usize {
    let pivot = nodes[root].source_left;
    let middle = nodes[pivot].source_right;
    nodes[pivot].source_right = root;
    nodes[root].source_left = middle;
    insert_source_update_height(nodes, root);
    insert_source_update_height(nodes, pivot);
    pivot
}

struct PreparedLedger<'a> {
    arena: &'a mut [ReservedBitmapPage],
    replacements: &'a mut [u32],
    replacement_len: usize,
    candidates: &'a mut [u32],
    candidate_len: usize,
    index_nodes: &'a mut [BitmapCowIndexNode],
    index_root: usize,
    index_len: usize,
    available_slots: &'a mut [usize],
    available_len: usize,
}

struct PreparedSharedLedger<'a> {
    replacements: &'a mut [u32],
    replacement_len: usize,
    candidates: &'a mut [u32],
    candidate_len: usize,
    index_nodes: &'a mut [BitmapCowIndexNode],
    index_root: usize,
    index_len: usize,
    available_slots: &'a mut [usize],
    available_len: usize,
    #[cfg(test)]
    preparation_work: usize,
}

struct PreparedScopedLedger<'a> {
    arena_bindings: &'a mut [BitmapCowArenaBinding],
    replacements: &'a mut [u32],
    replacement_len: usize,
    candidates: &'a mut [u32],
    candidate_len: usize,
    index_nodes: &'a mut [BitmapCowIndexNode],
    index_root: usize,
    index_len: usize,
    available_slots: &'a mut [usize],
    available_len: usize,
    verified_pages: &'a mut [VerifiedBitmapPage],
    planned_candidate_len: usize,
    reservation_planned: bool,
    payload_page_budget: usize,
    planned_required_private_pages: usize,
}

fn classify_bitmap_pool_state(
    pgno: u32,
    state: PrivatePagePoolState,
    pending_txn: u64,
) -> Result<BitmapPrivatePageState, FreeBitmapCowError> {
    match state {
        PrivatePagePoolState::Available => Ok(BitmapPrivatePageState::Available),
        PrivatePagePoolState::InUse {
            owner,
            owner_generation,
            tag,
        } if owner == PrivatePageOwner::Bitmap => {
            let committed_origin =
                u32::try_from(tag).map_err(|_| FreeBitmapCowError::InvalidBitmapPoolState {
                    pgno,
                    owner,
                    owner_generation,
                    tag,
                })?;
            if owner_generation != pending_txn {
                return Err(FreeBitmapCowError::InvalidBitmapPoolState {
                    pgno,
                    owner,
                    owner_generation,
                    tag,
                });
            }
            Ok(BitmapPrivatePageState::InUse { committed_origin })
        }
        PrivatePagePoolState::InUse { .. } => Ok(BitmapPrivatePageState::Foreign),
        PrivatePagePoolState::ReturnedFree => Ok(BitmapPrivatePageState::ReleasedFree),
        PrivatePagePoolState::ReturnedTail => Ok(BitmapPrivatePageState::ReleasedTail),
        PrivatePagePoolState::Vacant | PrivatePagePoolState::PendingReturn { .. } => {
            Ok(BitmapPrivatePageState::Foreign)
        }
    }
}

fn validate_bitmap_pool(
    pool: &PrivatePagePool<'_>,
    pending_txn: u64,
) -> Result<(), FreeBitmapCowError> {
    for slot in 0..pool.len() {
        let pgno = pool
            .page_number(slot)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        let state = pool.state(slot).map_err(FreeBitmapCowError::PrivatePool)?;
        classify_bitmap_pool_state(pgno, state, pending_txn)?;
    }
    Ok(())
}

fn map_pool_construction_error(error: PrivatePagePoolError) -> FreeBitmapCowError {
    match error {
        PrivatePagePoolError::PageCountOutOfRange { pending, .. } => {
            FreeBitmapCowError::PageCountOutOfRange(pending)
        }
        PrivatePagePoolError::PageOutOfBounds(pgno)
        | PrivatePagePoolError::AuthorizationMismatch { pgno, .. } => {
            FreeBitmapCowError::LedgerPageOutOfBounds(pgno)
        }
        PrivatePagePoolError::PagesNotStrict { current, .. } => {
            FreeBitmapCowError::DuplicateArenaPage(current)
        }
        other => FreeBitmapCowError::PrivatePool(other),
    }
}

fn prepare_ledger<'a>(
    page_count: u64,
    ledger: FreeBitmapCowLedger<'a>,
) -> Result<PreparedLedger<'a>, FreeBitmapCowError> {
    let required_index = ledger
        .arena
        .len()
        .checked_add(ledger.replacements.len())
        .ok_or(FreeBitmapCowError::IndexCapacityOverflow)?;
    if ledger.index_nodes.len() < required_index {
        return Err(FreeBitmapCowError::IndexCapacityTooSmall {
            required: required_index,
            actual: ledger.index_nodes.len(),
        });
    }
    if ledger.available_slots.len() < ledger.arena.len() {
        return Err(FreeBitmapCowError::AvailableSlotCapacityTooSmall {
            required: ledger.arena.len(),
            actual: ledger.available_slots.len(),
        });
    }

    let mut index_root = NO_INDEX;
    let mut index_len = 0usize;
    for (slot, page) in ledger.arena.iter().enumerate() {
        let pgno = page
            .initial_page_number()
            .ok_or(FreeBitmapCowError::LedgerPageOutOfBounds(0))?;
        if pgno < 2 || u64::from(pgno) >= page_count {
            return Err(FreeBitmapCowError::LedgerPageOutOfBounds(pgno));
        }
        if !page_index_insert(
            ledger.index_nodes,
            &mut index_root,
            &mut index_len,
            pgno,
            IndexedPage::Arena(slot),
        ) {
            return Err(FreeBitmapCowError::DuplicateArenaPage(pgno));
        }
    }
    for index in 0..ledger.replacement_len {
        let pgno = ledger.replacements[index];
        if pgno < 2 || u64::from(pgno) >= page_count {
            return Err(FreeBitmapCowError::LedgerPageOutOfBounds(pgno));
        }
        if let Some(existing) = page_index_find(ledger.index_nodes, index_root, pgno) {
            return Err(match existing {
                IndexedPage::Arena(_) => FreeBitmapCowError::LedgerPageConflict(pgno),
                IndexedPage::Replacement => FreeBitmapCowError::DuplicateReplacement(pgno),
                IndexedPage::Verified(_) | IndexedPage::PlannedCandidate(_) => {
                    FreeBitmapCowError::LedgerPageConflict(pgno)
                }
            });
        }
        debug_assert!(page_index_find(ledger.index_nodes, index_root, pgno).is_none());
        debug_assert!(index_len < ledger.index_nodes.len());
        page_index_insert_prechecked(
            ledger.index_nodes,
            &mut index_root,
            &mut index_len,
            pgno,
            IndexedPage::Replacement,
        );
    }
    let mut previous_candidate = None;
    for index in 0..ledger.candidate_len {
        let pgno = ledger.candidates[index];
        if pgno < 2 || u64::from(pgno) >= page_count {
            return Err(FreeBitmapCowError::LedgerPageOutOfBounds(pgno));
        }
        if let Some(previous) = previous_candidate {
            if pgno == previous {
                return Err(FreeBitmapCowError::DuplicateCandidate(pgno));
            }
            if pgno < previous {
                return Err(FreeBitmapCowError::CandidateOrderRegression {
                    previous,
                    current: pgno,
                });
            }
        }
        previous_candidate = Some(pgno);
        if page_index_find(ledger.index_nodes, index_root, pgno).is_some() {
            return Err(FreeBitmapCowError::LedgerPageConflict(pgno));
        }
    }

    let mut available_len = 0usize;
    for slot in (0..ledger.arena.len()).rev() {
        if ledger.arena[slot].initial_state() == PrivatePagePoolState::Available {
            ledger.available_slots[available_len] = slot;
            available_len += 1;
        }
    }

    Ok(PreparedLedger {
        arena: ledger.arena,
        replacements: ledger.replacements,
        replacement_len: ledger.replacement_len,
        candidates: ledger.candidates,
        candidate_len: ledger.candidate_len,
        index_nodes: ledger.index_nodes,
        index_root,
        index_len,
        available_slots: ledger.available_slots,
        available_len,
    })
}

fn prepare_shared_ledger<'a>(
    pool: &PrivatePagePool<'_>,
    page_count: u64,
    ledger: SharedFreeBitmapCowLedger<'a>,
) -> Result<PreparedSharedLedger<'a>, FreeBitmapCowError> {
    if ledger.replacement_len > ledger.replacements.len()
        || ledger.candidate_len > ledger.candidates.len()
    {
        return Err(FreeBitmapCowError::LedgerPrefixOutOfBounds);
    }
    let required_index = pool
        .len()
        .checked_add(ledger.replacement_len)
        .ok_or(FreeBitmapCowError::IndexCapacityOverflow)?;
    if ledger.index_nodes.len() < required_index {
        return Err(FreeBitmapCowError::IndexCapacityTooSmall {
            required: required_index,
            actual: ledger.index_nodes.len(),
        });
    }
    if ledger.available_slots.len() < pool.len() {
        return Err(FreeBitmapCowError::AvailableSlotCapacityTooSmall {
            required: pool.len(),
            actual: ledger.available_slots.len(),
        });
    }

    let snapshot = pool
        .mutation_snapshot()
        .map_err(FreeBitmapCowError::PrivatePool)?;
    #[cfg(test)]
    let mut preparation_work = 0usize;
    let mut index_root = NO_INDEX;
    let mut index_len = 0usize;
    let mut available_len = 0usize;
    for slot in 0..pool.len() {
        #[cfg(test)]
        {
            preparation_work = preparation_work.saturating_add(1);
        }
        let pgno = pool
            .page_number(slot)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        if pgno < 2 || u64::from(pgno) >= page_count {
            return Err(FreeBitmapCowError::LedgerPageOutOfBounds(pgno));
        }
        let state = pool.state(slot).map_err(FreeBitmapCowError::PrivatePool)?;
        if classify_bitmap_pool_state(pgno, state, pool.pending_txn())?
            == BitmapPrivatePageState::Available
        {
            ledger.available_slots[available_len] = slot;
            available_len += 1;
        }
        #[cfg(test)]
        let existing = {
            let (existing, probes) = page_index_find_counted(ledger.index_nodes, index_root, pgno);
            preparation_work = preparation_work.saturating_add(probes);
            existing
        };
        #[cfg(not(test))]
        let existing = page_index_find(ledger.index_nodes, index_root, pgno);
        if existing.is_some() {
            return Err(FreeBitmapCowError::DuplicateArenaPage(pgno));
        }
        #[cfg(test)]
        page_index_insert_prechecked_counted(
            ledger.index_nodes,
            &mut index_root,
            &mut index_len,
            pgno,
            IndexedPage::Arena(slot),
            &mut preparation_work,
        );
        #[cfg(not(test))]
        page_index_insert_prechecked(
            ledger.index_nodes,
            &mut index_root,
            &mut index_len,
            pgno,
            IndexedPage::Arena(slot),
        );
    }
    for index in 0..ledger.replacement_len {
        #[cfg(test)]
        {
            preparation_work = preparation_work.saturating_add(1);
        }
        let pgno = ledger.replacements[index];
        if pgno < 2 || u64::from(pgno) >= page_count {
            return Err(FreeBitmapCowError::LedgerPageOutOfBounds(pgno));
        }
        #[cfg(test)]
        let existing = {
            let (existing, probes) = page_index_find_counted(ledger.index_nodes, index_root, pgno);
            preparation_work = preparation_work.saturating_add(probes);
            existing
        };
        #[cfg(not(test))]
        let existing = page_index_find(ledger.index_nodes, index_root, pgno);
        if let Some(existing) = existing {
            return Err(match existing {
                IndexedPage::Replacement => FreeBitmapCowError::DuplicateReplacement(pgno),
                IndexedPage::Arena(_)
                | IndexedPage::Verified(_)
                | IndexedPage::PlannedCandidate(_) => FreeBitmapCowError::LedgerPageConflict(pgno),
            });
        }
        #[cfg(test)]
        page_index_insert_prechecked_counted(
            ledger.index_nodes,
            &mut index_root,
            &mut index_len,
            pgno,
            IndexedPage::Replacement,
            &mut preparation_work,
        );
        #[cfg(not(test))]
        page_index_insert_prechecked(
            ledger.index_nodes,
            &mut index_root,
            &mut index_len,
            pgno,
            IndexedPage::Replacement,
        );
    }
    let mut previous_candidate = None;
    for index in 0..ledger.candidate_len {
        #[cfg(test)]
        {
            preparation_work = preparation_work.saturating_add(1);
        }
        let pgno = ledger.candidates[index];
        if pgno < 2 || u64::from(pgno) >= page_count {
            return Err(FreeBitmapCowError::LedgerPageOutOfBounds(pgno));
        }
        if let Some(previous) = previous_candidate {
            if pgno == previous {
                return Err(FreeBitmapCowError::DuplicateCandidate(pgno));
            }
            if pgno < previous {
                return Err(FreeBitmapCowError::CandidateOrderRegression {
                    previous,
                    current: pgno,
                });
            }
        }
        previous_candidate = Some(pgno);
        #[cfg(test)]
        let existing = {
            let (existing, probes) = page_index_find_counted(ledger.index_nodes, index_root, pgno);
            preparation_work = preparation_work.saturating_add(probes);
            existing
        };
        #[cfg(not(test))]
        let existing = page_index_find(ledger.index_nodes, index_root, pgno);
        if existing.is_some() {
            return Err(FreeBitmapCowError::LedgerPageConflict(pgno));
        }
    }
    pool.preflight_mutation(&snapshot, 0)
        .map_err(FreeBitmapCowError::PrivatePool)?;
    ledger.available_slots[..available_len].reverse();

    Ok(PreparedSharedLedger {
        replacements: ledger.replacements,
        replacement_len: ledger.replacement_len,
        candidates: ledger.candidates,
        candidate_len: ledger.candidate_len,
        index_nodes: ledger.index_nodes,
        index_root,
        index_len,
        available_slots: ledger.available_slots,
        available_len,
        #[cfg(test)]
        preparation_work,
    })
}

fn prepare_scoped_ledger<'a>(
    pool: &PrivatePagePool<'_>,
    scope: &PrivatePageReservationScope<'_>,
    scope_capacity: usize,
    page_count: u64,
    ledger: ScopedFreeBitmapCowLedger<'a>,
) -> Result<PreparedScopedLedger<'a>, FreeBitmapCowError> {
    let ScopedFreeBitmapCowLedger {
        arena_bindings,
        replacements,
        replacement_len,
        candidates,
        candidate_len,
        index_nodes,
        available_slots,
        verified_pages,
        planned_candidate_len,
        reservation_planned,
        payload_page_budget,
        planned_required_private_pages,
    } = ledger;
    if arena_bindings.len() < scope_capacity {
        return Err(FreeBitmapCowError::AvailableSlotCapacityTooSmall {
            required: scope_capacity,
            actual: arena_bindings.len(),
        });
    }
    let planned_nodes = if reservation_planned {
        planned_candidate_len
    } else {
        0
    };
    let required_index = scope_capacity
        .checked_add(replacements.len())
        .and_then(|required| required.checked_add(planned_nodes))
        .and_then(|required| required.checked_add(verified_pages.len()))
        .ok_or(FreeBitmapCowError::IndexCapacityOverflow)?;
    if index_nodes.len() < required_index {
        return Err(FreeBitmapCowError::IndexCapacityTooSmall {
            required: required_index,
            actual: index_nodes.len(),
        });
    }
    if available_slots.len() < scope_capacity {
        return Err(FreeBitmapCowError::AvailableSlotCapacityTooSmall {
            required: scope_capacity,
            actual: available_slots.len(),
        });
    }

    index_nodes.fill(BitmapCowIndexNode::empty());
    available_slots.fill(0);
    arena_bindings.fill(BitmapCowArenaBinding::empty());
    let prepared = prepare_scoped_ledger_scratch(
        pool,
        scope,
        scope_capacity,
        page_count,
        arena_bindings,
        replacements,
        replacement_len,
        candidates,
        candidate_len,
        index_nodes,
        available_slots,
        verified_pages,
        planned_candidate_len,
        reservation_planned,
    );
    let (index_root, index_len, available_len) = match prepared {
        Ok(prepared) => prepared,
        Err(error) => {
            index_nodes.fill(BitmapCowIndexNode::empty());
            available_slots.fill(0);
            arena_bindings.fill(BitmapCowArenaBinding::empty());
            return Err(error);
        }
    };
    Ok(PreparedScopedLedger {
        arena_bindings,
        replacements,
        replacement_len,
        candidates,
        candidate_len,
        index_nodes,
        index_root,
        index_len,
        available_slots,
        available_len,
        verified_pages,
        planned_candidate_len,
        reservation_planned,
        payload_page_budget,
        planned_required_private_pages,
    })
}

#[allow(clippy::too_many_arguments)]
fn prepare_scoped_ledger_scratch(
    pool: &PrivatePagePool<'_>,
    scope: &PrivatePageReservationScope<'_>,
    scope_capacity: usize,
    page_count: u64,
    arena_bindings: &mut [BitmapCowArenaBinding],
    replacements: &[u32],
    replacement_len: usize,
    candidates: &[u32],
    candidate_len: usize,
    index_nodes: &mut [BitmapCowIndexNode],
    available_slots: &mut [usize],
    verified_pages: &[VerifiedBitmapPage],
    planned_candidate_len: usize,
    reservation_planned: bool,
) -> Result<(usize, usize, usize), FreeBitmapCowError> {
    let mut index_root = NO_INDEX;
    let mut mapped = 0usize;
    let mut layout_error = None;
    let layout = pool.visit_exact_scope_layout(scope, |ordinal, slot, info| {
        if layout_error.is_some() {
            return;
        }
        if ordinal != mapped || mapped == scope_capacity {
            layout_error = Some(FreeBitmapCowError::ArenaPageConflict(info.pgno));
            return;
        }
        arena_bindings[mapped] = BitmapCowArenaBinding {
            pool_slot: slot,
            pool_epoch: info.binding_epoch,
            page_number: 0,
            storage_node: mapped,
            active_node: NO_INDEX,
            bound: info.bound,
        };
        if info.bound {
            if info.pgno < 2 || u64::from(info.pgno) >= page_count {
                layout_error = Some(FreeBitmapCowError::LedgerPageOutOfBounds(info.pgno));
                return;
            }
            page_index_insert_existing_prechecked(
                index_nodes,
                &mut index_root,
                mapped,
                info.pgno,
                IndexedPage::Arena(slot),
            );
            arena_bindings[mapped].page_number = info.pgno;
            arena_bindings[mapped].active_node = mapped;
        }
        mapped += 1;
    });
    layout.map_err(FreeBitmapCowError::PrivatePool)?;
    if let Some(error) = layout_error {
        return Err(error);
    }
    if mapped != scope_capacity {
        return Err(FreeBitmapCowError::ArenaPageConflict(0));
    }

    for (index, &pgno) in replacements[..replacement_len].iter().enumerate() {
        if pgno < 2 || u64::from(pgno) >= page_count {
            return Err(FreeBitmapCowError::LedgerPageOutOfBounds(pgno));
        }
        if page_index_find(index_nodes, index_root, pgno).is_some() {
            return Err(FreeBitmapCowError::LedgerPageConflict(pgno));
        }
        page_index_insert_existing_prechecked(
            index_nodes,
            &mut index_root,
            scope_capacity + index,
            pgno,
            IndexedPage::Replacement,
        );
    }

    let planned_nodes = if reservation_planned {
        planned_candidate_len
    } else {
        0
    };
    let verified_offset = scope_capacity + replacements.len() + planned_nodes;
    for (index, verified) in verified_pages.iter().enumerate() {
        if verified.pgno < 2 || u64::from(verified.pgno) >= page_count {
            return Err(FreeBitmapCowError::LedgerPageOutOfBounds(verified.pgno));
        }
        if page_index_find(index_nodes, index_root, verified.pgno).is_some() {
            return Err(FreeBitmapCowError::LedgerPageConflict(verified.pgno));
        }
        page_index_insert_existing_prechecked(
            index_nodes,
            &mut index_root,
            verified_offset + index,
            verified.pgno,
            IndexedPage::Verified(index),
        );
    }

    let candidate_limit = if reservation_planned {
        planned_candidate_len
    } else {
        candidate_len
    };
    let mut previous = None;
    for (index, &pgno) in candidates[..candidate_limit].iter().enumerate() {
        if pgno < 2 || u64::from(pgno) >= pool.committed_page_count() {
            return Err(FreeBitmapCowError::LedgerPageOutOfBounds(pgno));
        }
        if let Some(prior) = previous {
            if pgno == prior {
                return Err(FreeBitmapCowError::DuplicateCandidate(pgno));
            }
            if pgno < prior {
                return Err(FreeBitmapCowError::CandidateOrderRegression {
                    previous: prior,
                    current: pgno,
                });
            }
        }
        previous = Some(pgno);
        if page_index_find(index_nodes, index_root, pgno).is_some() {
            return Err(FreeBitmapCowError::LedgerPageConflict(pgno));
        }
        if reservation_planned {
            page_index_insert_existing_prechecked(
                index_nodes,
                &mut index_root,
                scope_capacity + replacements.len() + index,
                pgno,
                IndexedPage::PlannedCandidate(index),
            );
        }
    }

    let mut available_len = 0usize;
    for binding in arena_bindings[..scope_capacity].iter().rev() {
        let info = pool
            .scoped_slot_info(scope, binding.pool_slot)
            .map_err(FreeBitmapCowError::PrivatePool)?
            .ok_or(FreeBitmapCowError::ArenaPageConflict(binding.page_number))?;
        if info.bound && info.state == PrivatePagePoolState::Available {
            available_slots[available_len] = binding.pool_slot;
            available_len += 1;
        }
    }
    let index_len = scope_capacity + replacements.len() + planned_nodes + verified_pages.len();
    Ok((index_root, index_len, available_len))
}

fn page_index_find(nodes: &[BitmapCowIndexNode], root: usize, pgno: u32) -> Option<IndexedPage> {
    page_index_find_node(nodes, root, pgno).map(|node| nodes[node].page)
}

fn page_index_find_node(nodes: &[BitmapCowIndexNode], mut root: usize, pgno: u32) -> Option<usize> {
    while root != NO_INDEX {
        let node = nodes[root];
        if pgno < node.pgno {
            root = node.left;
        } else if pgno > node.pgno {
            root = node.right;
        } else {
            return Some(root);
        }
    }
    None
}

fn selected_candidate_node_in(
    nodes: &[BitmapCowIndexNode],
    root: usize,
    candidates: &[u32],
    pgno: u32,
    selected: usize,
) -> Option<usize> {
    let candidate_index = candidates[..selected].binary_search(&pgno).ok()?;
    let node = page_index_find_node(nodes, root, pgno)?;
    let mapped = nodes[node];
    (mapped.candidate_mapped
        && mapped.candidate_pgno == pgno
        && mapped.candidate_index == candidate_index)
        .then_some(node)
}

#[cfg(test)]
fn page_index_find_counted(
    nodes: &[BitmapCowIndexNode],
    mut root: usize,
    pgno: u32,
) -> (Option<IndexedPage>, usize) {
    let mut probes = 0usize;
    while root != NO_INDEX {
        probes += 1;
        let node = nodes[root];
        if pgno < node.pgno {
            root = node.left;
        } else if pgno > node.pgno {
            root = node.right;
        } else {
            return (Some(node.page), probes);
        }
    }
    (None, probes)
}

fn page_index_replace(
    nodes: &mut [BitmapCowIndexNode],
    mut root: usize,
    pgno: u32,
    page: IndexedPage,
) -> Option<IndexedPage> {
    while root != NO_INDEX {
        if pgno < nodes[root].pgno {
            root = nodes[root].left;
        } else if pgno > nodes[root].pgno {
            root = nodes[root].right;
        } else {
            let previous = nodes[root].page;
            nodes[root].page = page;
            return Some(previous);
        }
    }
    None
}

fn page_index_insert(
    nodes: &mut [BitmapCowIndexNode],
    root: &mut usize,
    len: &mut usize,
    pgno: u32,
    page: IndexedPage,
) -> bool {
    if page_index_find(nodes, *root, pgno).is_some() || *len == nodes.len() {
        return false;
    }
    page_index_insert_prechecked(nodes, root, len, pgno, page);
    true
}

fn page_index_insert_prechecked(
    nodes: &mut [BitmapCowIndexNode],
    root: &mut usize,
    len: &mut usize,
    pgno: u32,
    page: IndexedPage,
) {
    let new_index = *len;
    initialize_page_index_node(&mut nodes[new_index], pgno, page);
    *len += 1;
    *root = page_index_insert_unique(nodes, *root, new_index);
}

fn page_index_insert_existing_prechecked(
    nodes: &mut [BitmapCowIndexNode],
    root: &mut usize,
    index: usize,
    pgno: u32,
    page: IndexedPage,
) {
    initialize_page_index_node(&mut nodes[index], pgno, page);
    *root = page_index_insert_unique(nodes, *root, index);
}

fn initialize_page_index_node(node: &mut BitmapCowIndexNode, pgno: u32, page: IndexedPage) {
    node.pgno = pgno;
    node.page = page;
    node.left = NO_INDEX;
    node.right = NO_INDEX;
    node.height = 1;
    if let IndexedPage::PlannedCandidate(candidate_index) = page {
        node.candidate_pgno = pgno;
        node.candidate_index = candidate_index;
        node.candidate_mapped = true;
    }
}

#[cfg(test)]
fn page_index_insert_prechecked_counted(
    nodes: &mut [BitmapCowIndexNode],
    root: &mut usize,
    len: &mut usize,
    pgno: u32,
    page: IndexedPage,
    work: &mut usize,
) {
    let new_index = *len;
    initialize_page_index_node(&mut nodes[new_index], pgno, page);
    *len += 1;
    *root = page_index_insert_unique_counted(nodes, *root, new_index, work);
}

fn page_index_insert_unique(
    nodes: &mut [BitmapCowIndexNode],
    root: usize,
    new_index: usize,
) -> usize {
    if root == NO_INDEX {
        return new_index;
    }

    let new_pgno = nodes[new_index].pgno;
    if new_pgno < nodes[root].pgno {
        nodes[root].left = page_index_insert_unique(nodes, nodes[root].left, new_index);
    } else {
        nodes[root].right = page_index_insert_unique(nodes, nodes[root].right, new_index);
    }
    page_index_update_height(nodes, root);

    let balance = page_index_balance(nodes, root);
    if balance > 1 {
        let left = nodes[root].left;
        if new_pgno > nodes[left].pgno {
            nodes[root].left = page_index_rotate_left(nodes, left);
        }
        return page_index_rotate_right(nodes, root);
    }
    if balance < -1 {
        let right = nodes[root].right;
        if new_pgno < nodes[right].pgno {
            nodes[root].right = page_index_rotate_right(nodes, right);
        }
        return page_index_rotate_left(nodes, root);
    }
    root
}

#[cfg(test)]
fn page_index_insert_unique_counted(
    nodes: &mut [BitmapCowIndexNode],
    root: usize,
    new_index: usize,
    work: &mut usize,
) -> usize {
    *work = work.saturating_add(1);
    if root == NO_INDEX {
        return new_index;
    }

    let new_pgno = nodes[new_index].pgno;
    if new_pgno < nodes[root].pgno {
        nodes[root].left =
            page_index_insert_unique_counted(nodes, nodes[root].left, new_index, work);
    } else {
        nodes[root].right =
            page_index_insert_unique_counted(nodes, nodes[root].right, new_index, work);
    }
    page_index_update_height(nodes, root);

    let balance = page_index_balance(nodes, root);
    if balance > 1 {
        let left = nodes[root].left;
        if new_pgno > nodes[left].pgno {
            nodes[root].left = page_index_rotate_left(nodes, left);
        }
        return page_index_rotate_right(nodes, root);
    }
    if balance < -1 {
        let right = nodes[root].right;
        if new_pgno < nodes[right].pgno {
            nodes[root].right = page_index_rotate_right(nodes, right);
        }
        return page_index_rotate_left(nodes, root);
    }
    root
}

fn page_index_height(nodes: &[BitmapCowIndexNode], index: usize) -> u8 {
    if index == NO_INDEX {
        0
    } else {
        nodes[index].height
    }
}

fn page_index_update_height(nodes: &mut [BitmapCowIndexNode], index: usize) {
    nodes[index].height = 1 + page_index_height(nodes, nodes[index].left)
        .max(page_index_height(nodes, nodes[index].right));
}

fn page_index_balance(nodes: &[BitmapCowIndexNode], index: usize) -> i16 {
    i16::from(page_index_height(nodes, nodes[index].left))
        - i16::from(page_index_height(nodes, nodes[index].right))
}

fn page_index_rotate_left(nodes: &mut [BitmapCowIndexNode], root: usize) -> usize {
    let pivot = nodes[root].right;
    let middle = nodes[pivot].left;
    nodes[pivot].left = root;
    nodes[root].right = middle;
    page_index_update_height(nodes, root);
    page_index_update_height(nodes, pivot);
    pivot
}

fn page_index_rotate_right(nodes: &mut [BitmapCowIndexNode], root: usize) -> usize {
    let pivot = nodes[root].left;
    let middle = nodes[pivot].right;
    nodes[pivot].right = root;
    nodes[root].left = middle;
    page_index_update_height(nodes, root);
    page_index_update_height(nodes, pivot);
    pivot
}

fn clear_active_index_node(node: &mut BitmapCowIndexNode) {
    node.pgno = 0;
    node.page = IndexedPage::Replacement;
    node.left = NO_INDEX;
    node.right = NO_INDEX;
    node.height = 0;
}

fn restore_planned_candidate_node(
    nodes: &mut [BitmapCowIndexNode],
    root: &mut usize,
    index: usize,
) {
    let pgno = nodes[index].candidate_pgno;
    let candidate_index = nodes[index].candidate_index;
    clear_active_index_node(&mut nodes[index]);
    page_index_insert_existing_prechecked(
        nodes,
        root,
        index,
        pgno,
        IndexedPage::PlannedCandidate(candidate_index),
    );
}

fn page_index_rebalance(nodes: &mut [BitmapCowIndexNode], root: usize) -> usize {
    page_index_update_height(nodes, root);
    let balance = page_index_balance(nodes, root);
    if balance > 1 {
        let left = nodes[root].left;
        if page_index_balance(nodes, left) < 0 {
            nodes[root].left = page_index_rotate_left(nodes, left);
        }
        return page_index_rotate_right(nodes, root);
    }
    if balance < -1 {
        let right = nodes[root].right;
        if page_index_balance(nodes, right) > 0 {
            nodes[root].right = page_index_rotate_right(nodes, right);
        }
        return page_index_rotate_left(nodes, root);
    }
    root
}

fn page_index_detach_minimum(nodes: &mut [BitmapCowIndexNode], root: usize) -> (usize, usize) {
    if nodes[root].left == NO_INDEX {
        return (nodes[root].right, root);
    }
    let (left, minimum) = page_index_detach_minimum(nodes, nodes[root].left);
    nodes[root].left = left;
    (page_index_rebalance(nodes, root), minimum)
}

fn page_index_delete(nodes: &mut [BitmapCowIndexNode], root: usize, pgno: u32) -> (usize, usize) {
    debug_assert_ne!(root, NO_INDEX);
    if pgno < nodes[root].pgno {
        let (left, removed) = page_index_delete(nodes, nodes[root].left, pgno);
        nodes[root].left = left;
        return (page_index_rebalance(nodes, root), removed);
    }
    if pgno > nodes[root].pgno {
        let (right, removed) = page_index_delete(nodes, nodes[root].right, pgno);
        nodes[root].right = right;
        return (page_index_rebalance(nodes, root), removed);
    }
    let left = nodes[root].left;
    let right = nodes[root].right;
    if left == NO_INDEX {
        return (right, root);
    }
    if right == NO_INDEX {
        return (left, root);
    }
    let (right, successor) = page_index_detach_minimum(nodes, right);
    nodes[successor].left = left;
    nodes[successor].right = right;
    (page_index_rebalance(nodes, successor), root)
}

fn minimum_level(limit: u64) -> Result<u16, FreeBitmapCowError> {
    let mut level = 0u16;
    let mut covered = BITMAP_LEAF_BITS;
    while covered < limit {
        covered = covered
            .checked_mul(BITMAP_FANOUT)
            .ok_or(FreeBitmapCowError::CoverageOverflow)?;
        level = level
            .checked_add(1)
            .ok_or(FreeBitmapCowError::CoverageOverflow)?;
        if usize::from(level) >= FREE_PATH_CAPACITY {
            return Err(FreeBitmapCowError::CoverageOverflow);
        }
    }
    Ok(level)
}

fn coverage(level: u16) -> Result<u64, FreeBitmapCowError> {
    let mut covered = BITMAP_LEAF_BITS;
    for _ in 0..level {
        covered = covered
            .checked_mul(BITMAP_FANOUT)
            .ok_or(FreeBitmapCowError::CoverageOverflow)?;
    }
    Ok(covered)
}

fn search_free_leaf(
    leaf: BitmapLeaf<'_>,
    base: u64,
    limit: u64,
) -> Result<Option<u64>, FreeBitmapCowError> {
    search_free_leaf_from(leaf, base, limit, 2)
}

fn search_free_leaf_from(
    leaf: BitmapLeaf<'_>,
    base: u64,
    limit: u64,
    start: u64,
) -> Result<Option<u64>, FreeBitmapCowError> {
    let first = core::cmp::max(base, start);
    if first >= limit {
        return Ok(None);
    }
    let local = first - base;
    let first_word =
        usize::try_from(local / 64).map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
    for word_index in first_word..BITMAP_LEAF_WORDS {
        let word_base = base
            .checked_add((word_index as u64) * 64)
            .ok_or(FreeBitmapCowError::CoverageOverflow)?;
        if word_base >= limit {
            break;
        }
        let mut candidates = leaf.word(word_index);
        if word_index == first_word {
            candidates &= u64::MAX << (local % 64);
        }
        let remaining = limit - word_base;
        if remaining < 64 {
            candidates &= (1u64 << remaining) - 1;
        }
        if candidates != 0 {
            return word_base
                .checked_add(u64::from(candidates.trailing_zeros()))
                .map(Some)
                .ok_or(FreeBitmapCowError::CoverageOverflow);
        }
    }
    Ok(None)
}

fn leaf_survives_clear(leaf: BitmapLeaf<'_>, base: u64, candidate: u64) -> bool {
    let local = candidate - base;
    let selected_word = usize::try_from(local / 64).unwrap();
    let selected_mask = 1u64 << (local % 64);
    (0..BITMAP_LEAF_WORDS).any(|index| {
        let word = leaf.word(index);
        if index == selected_word {
            word & !selected_mask != 0
        } else {
            word != 0
        }
    })
}

fn count_branch_children(branch: BitmapBranch<'_>) -> u16 {
    let count = (0..BITMAP_FANOUT as usize)
        .filter(|&index| branch.child(index) != 0)
        .count();
    u16::try_from(count).unwrap()
}

fn encode_leaf_clear(
    destination: &mut [u8; PAGE_SIZE],
    source: &[u8; PAGE_SIZE],
    pending_txn: u64,
    base: u64,
    candidate: u32,
) {
    destination.fill(0);
    destination[SUMMARY_OFFSET..usize::from(LEAF_LOWER)]
        .copy_from_slice(&source[SUMMARY_OFFSET..usize::from(LEAF_LOWER)]);
    mutate_leaf_clear(destination, pending_txn, base, candidate);
}

fn mutate_leaf_clear(page: &mut [u8; PAGE_SIZE], pending_txn: u64, base: u64, candidate: u32) {
    let local = u64::from(candidate).checked_sub(base).unwrap();
    let word_index = usize::try_from(local / 64).unwrap();
    let bit = (local % 64) as u32;
    let offset = SUMMARY_OFFSET + word_index * 8;
    let word = u64::from_le_bytes(page[offset..offset + 8].try_into().unwrap());
    page[offset..offset + 8].copy_from_slice(&(word & !(1u64 << bit)).to_le_bytes());
    let item_count = (0..BITMAP_LEAF_WORDS)
        .filter(|&index| {
            let at = SUMMARY_OFFSET + index * 8;
            u64::from_le_bytes(page[at..at + 8].try_into().unwrap()) != 0
        })
        .count();
    write_bitmap_header(
        page,
        PageType::BitmapLeaf,
        pending_txn,
        u16::try_from(item_count).unwrap(),
        0,
        LEAF_LOWER,
    );
}

fn encode_branch_child(
    destination: &mut [u8; PAGE_SIZE],
    source: &[u8; PAGE_SIZE],
    pending_txn: u64,
    level: u16,
    child_index: usize,
    child_pgno: u32,
) {
    destination.fill(0);
    destination[SUMMARY_OFFSET..usize::from(BRANCH_LOWER)]
        .copy_from_slice(&source[SUMMARY_OFFSET..usize::from(BRANCH_LOWER)]);
    mutate_branch_child(destination, pending_txn, level, child_index, child_pgno);
}

fn mutate_branch_child(
    page: &mut [u8; PAGE_SIZE],
    pending_txn: u64,
    level: u16,
    child_index: usize,
    child_pgno: u32,
) {
    let child_offset = CHILDREN_OFFSET + child_index * 4;
    page[child_offset..child_offset + 4].copy_from_slice(&child_pgno.to_le_bytes());
    let summary_offset = SUMMARY_OFFSET + (child_index / 64) * 8;
    let summary = u64::from_le_bytes(page[summary_offset..summary_offset + 8].try_into().unwrap());
    let mask = 1u64 << (child_index % 64);
    let summary = if child_pgno == 0 {
        summary & !mask
    } else {
        summary | mask
    };
    page[summary_offset..summary_offset + 8].copy_from_slice(&summary.to_le_bytes());
    let item_count = (0..BITMAP_FANOUT as usize)
        .filter(|&index| {
            let at = CHILDREN_OFFSET + index * 4;
            u32::from_le_bytes(page[at..at + 4].try_into().unwrap()) != 0
        })
        .count();
    write_bitmap_header(
        page,
        PageType::BitmapBranch,
        pending_txn,
        u16::try_from(item_count).unwrap(),
        level,
        BRANCH_LOWER,
    );
}

fn write_bitmap_header(
    page: &mut [u8; PAGE_SIZE],
    page_type: PageType,
    pending_txn: u64,
    item_count: u16,
    level: u16,
    lower: u16,
) {
    PageHeader {
        page_type,
        born_txn: pending_txn,
        item_count,
        level,
        lower,
        upper: PAGE_SIZE as u16,
        aux: BitmapKind::FreePages as u32,
        page_crc32c: 0,
    }
    .encode_into(page);
    page::write_crc32c(page);
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use crate::test_alloc::count_thread_allocations;
    use core::cell::Cell;
    use std::vec;
    use std::vec::Vec;

    #[derive(Debug)]
    struct SparsePage {
        pgno: u32,
        bytes: [u8; PAGE_SIZE],
        reads: Cell<usize>,
    }

    #[derive(Debug)]
    pub(crate) struct SparsePages<const N: usize> {
        pages: [SparsePage; N],
        reads: Cell<usize>,
    }

    #[derive(Debug)]
    struct FailingPages {
        access: Option<PageSourceError>,
        read: Option<PageSourceError>,
    }

    #[derive(Debug)]
    struct AccessControlledPages<const N: usize> {
        source: SparsePages<N>,
        access_error: Cell<Option<PageSourceError>>,
        fail_on_check: Cell<Option<usize>>,
        checks: Cell<usize>,
    }

    impl<const N: usize> SparsePages<N> {
        fn new(pages: [SparsePage; N]) -> Self {
            Self {
                pages,
                reads: Cell::new(0),
            }
        }

        fn page_reads(&self, pgno: u32) -> usize {
            self.pages
                .iter()
                .find(|page| page.pgno == pgno)
                .map_or(0, |page| page.reads.get())
        }
    }

    impl<const N: usize> CommittedPageSource for SparsePages<N> {
        fn read_page(
            &self,
            pgno: u32,
            destination: &mut [u8; PAGE_SIZE],
        ) -> Result<(), PageSourceError> {
            self.reads.set(self.reads.get() + 1);
            let Some(page) = self.pages.iter().find(|page| page.pgno == pgno) else {
                return Err(PageSourceError::PageOutOfBounds(pgno));
            };
            page.reads.set(page.reads.get() + 1);
            destination.copy_from_slice(&page.bytes);
            Ok(())
        }
    }

    impl<const N: usize> AccessControlledPages<N> {
        fn new(pages: [SparsePage; N]) -> Self {
            Self {
                source: SparsePages::new(pages),
                access_error: Cell::new(None),
                fail_on_check: Cell::new(None),
                checks: Cell::new(0),
            }
        }

        fn deny_access(&self, error: PageSourceError) {
            self.access_error.set(Some(error));
        }

        fn allow_access(&self) {
            self.access_error.set(None);
        }

        fn fail_on_check(&self, check: usize) {
            self.fail_on_check.set(Some(check));
        }
    }

    impl<const N: usize> CommittedPageSource for AccessControlledPages<N> {
        fn check_access(&self) -> Result<(), PageSourceError> {
            let check = self.checks.get() + 1;
            self.checks.set(check);
            if self.fail_on_check.get() == Some(check) {
                return Err(PageSourceError::ForkedHandle);
            }
            self.access_error.get().map_or(Ok(()), Err)
        }

        fn read_page(
            &self,
            pgno: u32,
            destination: &mut [u8; PAGE_SIZE],
        ) -> Result<(), PageSourceError> {
            self.source.read_page(pgno, destination)
        }
    }

    impl CommittedPageSource for FailingPages {
        fn check_access(&self) -> Result<(), PageSourceError> {
            self.access.map_or(Ok(()), Err)
        }

        fn read_page(&self, _: u32, _: &mut [u8; PAGE_SIZE]) -> Result<(), PageSourceError> {
            self.read.map_or(Ok(()), Err)
        }
    }

    #[derive(Debug)]
    struct ForeignMutatingPages<'pool, 'slots, 'scope, const N: usize> {
        pages: SparsePages<N>,
        pool: &'pool PrivatePagePool<'slots>,
        scope: &'pool PrivatePageReservationScope<'scope>,
        pgno: u32,
        armed: Cell<bool>,
        fail_when_armed: bool,
        checks: Cell<usize>,
        fire_on_check: Cell<Option<usize>>,
    }

    impl<const N: usize> CommittedPageSource for ForeignMutatingPages<'_, '_, '_, N> {
        fn check_access(&self) -> Result<(), PageSourceError> {
            let check = self.checks.get() + 1;
            self.checks.set(check);
            if self.armed.replace(false) || self.fire_on_check.get() == Some(check) {
                let checkpoint = self.pool.begin_checkpoint().unwrap();
                let _authority = self
                    .pool
                    .claim_page_in_scope(
                        &checkpoint,
                        self.scope,
                        self.pgno,
                        PrivatePageOwner::Retirement,
                        self.pool.pending_txn(),
                        0,
                    )
                    .unwrap();
                self.pool.commit_checkpoint(checkpoint).unwrap();
                if self.fail_when_armed {
                    return Err(PageSourceError::ForkedHandle);
                }
            }
            Ok(())
        }

        fn read_page(
            &self,
            pgno: u32,
            destination: &mut [u8; PAGE_SIZE],
        ) -> Result<(), PageSourceError> {
            self.pages.read_page(pgno, destination)
        }
    }

    fn leaf(pgno: u32, txn: u64, bits: &[u32]) -> SparsePage {
        let mut bytes = [0u8; PAGE_SIZE];
        for &bit in bits {
            let word = bit as usize / 64;
            let at = SUMMARY_OFFSET + word * 8;
            let value = u64::from_le_bytes(bytes[at..at + 8].try_into().unwrap());
            bytes[at..at + 8].copy_from_slice(&(value | (1u64 << (bit % 64))).to_le_bytes());
        }
        let count = (0..BITMAP_LEAF_WORDS)
            .filter(|&index| {
                let at = SUMMARY_OFFSET + index * 8;
                u64::from_le_bytes(bytes[at..at + 8].try_into().unwrap()) != 0
            })
            .count();
        write_bitmap_header(
            &mut bytes,
            PageType::BitmapLeaf,
            txn,
            u16::try_from(count).unwrap(),
            0,
            LEAF_LOWER,
        );
        SparsePage {
            pgno,
            bytes,
            reads: Cell::new(0),
        }
    }

    fn scoped_ledger<'a>(
        bindings: &'a mut [BitmapCowArenaBinding],
        replacements: &'a mut [u32],
        candidates: &'a mut [u32],
        index: &'a mut [BitmapCowIndexNode],
        available: &'a mut [usize],
    ) -> ScopedFreeBitmapCowLedger<'a> {
        ScopedFreeBitmapCowLedger::new(
            bindings,
            replacements,
            0,
            candidates,
            0,
            index,
            available,
            &mut [],
            0,
            false,
            0,
            0,
        )
    }

    #[derive(Debug, PartialEq, Eq)]
    struct ScopedCowState {
        root: u32,
        pending_page_count: u64,
        replacement_len: usize,
        candidate_len: usize,
        index_root: usize,
        index_len: usize,
        available_len: usize,
        selected_candidate_len: usize,
        candidate_selection_set: bool,
        index: Vec<BitmapCowIndexNode>,
        bindings: Vec<BitmapCowArenaBinding>,
        available: Vec<usize>,
        replacements: Vec<u32>,
        candidates: Vec<u32>,
    }

    fn snapshot_scoped_cow<S: CommittedPageSource + ?Sized>(
        cow: &FreeBitmapCow<'_, '_, '_, S>,
    ) -> ScopedCowState {
        ScopedCowState {
            root: cow.root,
            pending_page_count: cow.pending_page_count,
            replacement_len: cow.replacement_len,
            candidate_len: cow.candidate_len,
            index_root: cow.index_root,
            index_len: cow.index_len,
            available_len: cow.available_len,
            selected_candidate_len: cow.selected_candidate_len,
            candidate_selection_set: cow.candidate_selection_set,
            index: cow.index_nodes.to_vec(),
            bindings: cow.arena_bindings.to_vec(),
            available: cow.available_slots.to_vec(),
            replacements: cow.replacements.to_vec(),
            candidates: cow.candidates.to_vec(),
        }
    }

    fn restore_scoped_cow<S: CommittedPageSource + ?Sized>(
        cow: &mut FreeBitmapCow<'_, '_, '_, S>,
        state: &ScopedCowState,
    ) {
        cow.root = state.root;
        cow.pending_page_count = state.pending_page_count;
        cow.replacement_len = state.replacement_len;
        cow.candidate_len = state.candidate_len;
        cow.index_root = state.index_root;
        cow.index_len = state.index_len;
        cow.available_len = state.available_len;
        cow.selected_candidate_len = state.selected_candidate_len;
        cow.candidate_selection_set = state.candidate_selection_set;
        cow.index_nodes.copy_from_slice(&state.index);
        cow.arena_bindings.copy_from_slice(&state.bindings);
        cow.available_slots.copy_from_slice(&state.available);
        cow.replacements.copy_from_slice(&state.replacements);
        cow.candidates.copy_from_slice(&state.candidates);
    }

    #[test]
    fn scoped_cow_maps_exact_partial_scope_and_ignores_foreign_scope() {
        let source = SparsePages::new([]);
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 4];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let foreign = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let first = pool
            .bind_page(
                &checkpoint,
                &scope,
                9,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        let second = pool
            .bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.bind_page(
            &checkpoint,
            &foreign,
            8,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();

        let mut bindings = [BitmapCowArenaBinding::empty(); 3];
        let mut replacements = [];
        let mut candidates = [];
        let mut index = [BitmapCowIndexNode::empty(); 3];
        let mut available = [0usize; 3];
        let ledger = scoped_ledger(
            &mut bindings,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow =
            FreeBitmapCow::from_scoped_pool(&source, 1, 20, 0, &pool, &scope, ledger).unwrap();
        assert_eq!(cow.indexed_page(9), Some(IndexedPage::Arena(first)));
        assert_eq!(cow.indexed_page(7), Some(IndexedPage::Arena(second)));
        assert_eq!(cow.indexed_page(8), None);
        assert_eq!(cow.available_private_pages(), 2);
        assert_eq!(&cow.available_slots[..2], &[second, first]);
        cow.synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            .unwrap();
        assert_eq!(cow.pending_page_count(), 20);
    }

    #[test]
    fn scoped_cow_selects_none_partial_or_all_planned_candidate_prefix() {
        fn run(selected: usize, pages: [u32; 2]) {
            let source = SparsePages::new([]);
            let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let scope = pool.reserve_scope(2).unwrap();
            let mut bindings = [BitmapCowArenaBinding::empty(); 2];
            let mut replacements = [];
            let mut candidates = [5u32, 6];
            let mut index = [BitmapCowIndexNode::empty(); 4];
            let mut available = [0usize; 2];
            let ledger = ScopedFreeBitmapCowLedger::new(
                &mut bindings,
                &mut replacements,
                0,
                &mut candidates,
                0,
                &mut index,
                &mut available,
                &mut [],
                2,
                true,
                0,
                0,
            );
            let mut cow =
                FreeBitmapCow::from_scoped_pool(&source, 1, 20, 0, &pool, &scope, ledger).unwrap();
            cow.select_planned_candidate_prefix(selected).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            for pgno in pages {
                pool.bind_page(
                    &checkpoint,
                    &scope,
                    pgno,
                    PrivatePageAuthorization::CommittedFree,
                )
                .unwrap();
            }
            pool.commit_checkpoint(checkpoint).unwrap();
            cow.synchronize_scoped_bindings(&scope).unwrap();
            for candidate_index in 0..2 {
                let expected_arena = candidate_index < selected;
                assert_eq!(
                    matches!(
                        cow.indexed_page(cow.candidates[candidate_index]),
                        Some(IndexedPage::Arena(_))
                    ),
                    expected_arena
                );
                assert_eq!(
                    cow.indexed_page(cow.candidates[candidate_index]),
                    if expected_arena {
                        cow.indexed_page(cow.candidates[candidate_index])
                    } else {
                        Some(IndexedPage::PlannedCandidate(candidate_index))
                    }
                );
            }
            assert_eq!(cow.available_private_pages(), 2);
            cow.validate_scoped_bindings().unwrap();
        }

        run(0, [10, 11]);
        run(1, [5, 10]);
        run(2, [5, 6]);
    }

    #[test]
    fn scoped_selected_candidate_funds_clearing_its_own_free_bit() {
        let source = SparsePages::new([]);
        let page = leaf(2, 1, &[5, 6]);
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(1).unwrap();
        let mut bindings = [BitmapCowArenaBinding::empty()];
        let mut replacements = [0u32; 1];
        let mut candidates = [5u32];
        let mut index = [BitmapCowIndexNode::empty(); 4];
        let mut available = [0usize; 1];
        let mut verified = [VerifiedBitmapPage {
            pgno: 2,
            bytes: page.bytes,
            base: 0,
            level: 0,
            parent: NO_INDEX,
            remaining: 2,
            survives: true,
        }];
        let ledger = ScopedFreeBitmapCowLedger::new(
            &mut bindings,
            &mut replacements,
            0,
            &mut candidates,
            0,
            &mut index,
            &mut available,
            &mut verified,
            1,
            true,
            0,
            1,
        );
        let mut cow =
            FreeBitmapCow::from_scoped_pool(&source, 1, 20, 2, &pool, &scope, ledger).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let slot = pool
            .bind_page(
                &checkpoint,
                &scope,
                5,
                PrivatePageAuthorization::CommittedFree,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        cow.synchronize_scoped_bindings_for_candidate_prefix(&scope, 1)
            .unwrap();
        cow.apply_planned_reservation().unwrap();

        assert_eq!(cow.root(), 5);
        assert_eq!(cow.candidates(), &[5]);
        assert_eq!(cow.replacements(), &[2]);
        assert_eq!(cow.available_private_pages(), 0);
        let bytes = cow.test_page_at(slot);
        let leaf = BitmapLeaf::open(&bytes, 2, BitmapKind::FreePages).unwrap();
        assert_eq!(leaf.word(0) & (1 << 5), 0);
        assert_ne!(leaf.word(0) & (1 << 6), 0);
        cow.validate_scoped_bindings().unwrap();
    }

    #[test]
    fn scoped_cow_supports_repeated_direct_operations() {
        let source = SparsePages::new([]);
        let page = leaf(10, 2, &[5, 6]);
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            10,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        let authority = pool
            .claim_page_in_scope(&checkpoint, &scope, 10, PrivatePageOwner::Bitmap, 2, 0)
            .unwrap();
        pool.borrow_page_mut_in_scope(&scope, &authority)
            .unwrap()
            .copy_from_slice(&page.bytes);
        pool.commit_checkpoint(checkpoint).unwrap();

        let mut bindings = [BitmapCowArenaBinding::empty()];
        let mut replacements = [];
        let mut candidates = [0u32; 2];
        let mut index = [BitmapCowIndexNode::empty(); 1];
        let mut available = [0usize; 1];
        let ledger = scoped_ledger(
            &mut bindings,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow =
            FreeBitmapCow::from_scoped_pool(&source, 1, 20, 10, &pool, &scope, ledger).unwrap();
        let ((first, second, exhausted), allocations) = count_thread_allocations(|| {
            (
                cow.remove_lowest(),
                cow.remove_lowest(),
                cow.remove_lowest(),
            )
        });
        assert_eq!(allocations, 0);
        assert_eq!(first.unwrap().unwrap().page_number(), 5);
        assert_eq!(second.unwrap().unwrap().page_number(), 6);
        assert_eq!(exhausted.unwrap(), None);
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.candidates(), &[5, 6]);
        assert_eq!(cow.available_private_pages(), 1);
        cow.validate_scoped_bindings().unwrap();
    }

    #[test]
    fn scoped_growth_rollback_restores_planned_candidate_and_page_count() {
        let source = SparsePages::new([]);
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(2).unwrap();
        let mut bindings = [BitmapCowArenaBinding::empty(); 2];
        let mut replacements = [];
        let mut candidates = [5u32];
        let mut index = [BitmapCowIndexNode::empty(); 3];
        let mut available = [0usize; 2];
        let ledger = ScopedFreeBitmapCowLedger::new(
            &mut bindings,
            &mut replacements,
            0,
            &mut candidates,
            0,
            &mut index,
            &mut available,
            &mut [],
            1,
            true,
            0,
            0,
        );
        let mut cow =
            FreeBitmapCow::from_scoped_pool(&source, 1, 20, 0, &pool, &scope, ledger).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            5,
            PrivatePageAuthorization::CommittedFree,
        )
        .unwrap();
        pool.bind_page(&checkpoint, &scope, 20, PrivatePageAuthorization::Appended)
            .unwrap();
        cow.synchronize_scoped_bindings_for_candidate_prefix(&scope, 1)
            .unwrap();
        assert_eq!(cow.pending_page_count(), 21);
        pool.rollback_checkpoint(checkpoint).unwrap();
        assert!(cow.validate_scoped_bindings().is_err());
        cow.synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            .unwrap();
        assert_eq!(cow.pending_page_count(), 20);
        assert_eq!(cow.available_private_pages(), 0);
        assert_eq!(cow.indexed_page(5), Some(IndexedPage::PlannedCandidate(0)));
        cow.validate_scoped_bindings().unwrap();
    }

    #[test]
    fn scoped_sync_rejects_noncanonical_or_aliased_binding_metadata_atomically() {
        let source = SparsePages::new([]);
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(2).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            7,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            9,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let mut bindings = [BitmapCowArenaBinding::empty(); 2];
        let mut replacements = [];
        let mut candidates = [];
        let mut index = [BitmapCowIndexNode::empty(); 2];
        let mut available = [0usize; 2];
        let ledger = scoped_ledger(
            &mut bindings,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow =
            FreeBitmapCow::from_scoped_pool(&source, 1, 20, 0, &pool, &scope, ledger).unwrap();
        let original_bindings = cow.arena_bindings.to_vec();

        cow.arena_bindings[1].pool_slot = cow.arena_bindings[0].pool_slot;
        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);
        cow.arena_bindings.copy_from_slice(&original_bindings);

        cow.arena_bindings.swap(0, 1);
        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);
        cow.arena_bindings.copy_from_slice(&original_bindings);

        cow.arena_bindings[1].storage_node = cow.arena_bindings[0].storage_node;
        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);
        cow.arena_bindings.copy_from_slice(&original_bindings);

        cow.arena_bindings[1].active_node = cow.arena_bindings[0].active_node;
        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);
        cow.arena_bindings.copy_from_slice(&original_bindings);

        cow.synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            .unwrap();
        assert_eq!(cow.arena_bindings, original_bindings);
    }

    #[test]
    fn scoped_sync_rejects_moved_active_and_occupied_storage_nodes_atomically() {
        let source = SparsePages::new([]);
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 3];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let mut bindings = [BitmapCowArenaBinding::empty(); 3];
        let mut replacements = [];
        let mut candidates = [5u32];
        let mut index = [BitmapCowIndexNode::empty(); 5];
        let mut available = [0usize; 3];
        let ledger = ScopedFreeBitmapCowLedger::new(
            &mut bindings,
            &mut replacements,
            0,
            &mut candidates,
            0,
            &mut index,
            &mut available,
            &mut [],
            1,
            true,
            0,
            0,
        );
        let mut cow =
            FreeBitmapCow::from_scoped_pool(&source, 1, 20, 0, &pool, &scope, ledger).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            5,
            PrivatePageAuthorization::CommittedFree,
        )
        .unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            10,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            11,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        cow.synchronize_scoped_bindings_for_candidate_prefix(&scope, 1)
            .unwrap();
        let correct = snapshot_scoped_cow(&cow);
        let candidate_binding = cow
            .arena_bindings
            .iter()
            .position(|binding| binding.page_number == 5)
            .unwrap();
        let ordinary_binding = cow
            .arena_bindings
            .iter()
            .position(|binding| binding.page_number == 10)
            .unwrap();
        let spare_node = 4;

        let ordinary_node = cow.arena_bindings[ordinary_binding].active_node;
        let ordinary_slot = cow.arena_bindings[ordinary_binding].pool_slot;
        let (root, removed) = page_index_delete(cow.index_nodes, cow.index_root, 10);
        assert_eq!(removed, ordinary_node);
        cow.index_root = root;
        cow.index_nodes[ordinary_node] = BitmapCowIndexNode::empty();
        page_index_insert_existing_prechecked(
            cow.index_nodes,
            &mut cow.index_root,
            spare_node,
            10,
            IndexedPage::Arena(ordinary_slot),
        );
        cow.arena_bindings[ordinary_binding].active_node = spare_node;
        assert!(cow.validate_scoped_bindings().is_err());
        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&scope, 1)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);
        restore_scoped_cow(&mut cow, &correct);

        let candidate_node = cow.arena_bindings[candidate_binding].active_node;
        let candidate_slot = cow.arena_bindings[candidate_binding].pool_slot;
        let (root, removed) = page_index_delete(cow.index_nodes, cow.index_root, 5);
        assert_eq!(removed, candidate_node);
        cow.index_root = root;
        cow.index_nodes[candidate_node] = BitmapCowIndexNode::empty();
        page_index_insert_existing_prechecked(
            cow.index_nodes,
            &mut cow.index_root,
            spare_node,
            5,
            IndexedPage::Arena(candidate_slot),
        );
        cow.index_nodes[spare_node].candidate_pgno = 5;
        cow.index_nodes[spare_node].candidate_index = 0;
        cow.index_nodes[spare_node].candidate_mapped = true;
        cow.arena_bindings[candidate_binding].active_node = spare_node;
        assert!(cow.validate_scoped_bindings().is_err());
        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&scope, 1)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);
        restore_scoped_cow(&mut cow, &correct);

        let candidate_storage = cow.arena_bindings[candidate_binding].storage_node;
        initialize_page_index_node(
            &mut cow.index_nodes[candidate_storage],
            12,
            IndexedPage::Replacement,
        );
        assert!(cow.validate_scoped_bindings().is_err());
        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&scope, 1)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);
        restore_scoped_cow(&mut cow, &correct);

        let ordinary_storage = cow.arena_bindings[ordinary_binding].storage_node;
        cow.index_nodes[ordinary_storage].candidate_pgno = 5;
        cow.index_nodes[ordinary_storage].candidate_index = 0;
        cow.index_nodes[ordinary_storage].candidate_mapped = true;
        assert!(cow.validate_scoped_bindings().is_err());
        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&scope, 1)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);
        restore_scoped_cow(&mut cow, &correct);

        cow.synchronize_scoped_bindings_for_candidate_prefix(&scope, 1)
            .unwrap();
        cow.validate_scoped_bindings().unwrap();
    }

    #[test]
    fn scoped_sync_rejects_cross_scope_closed_scope_and_replacement_scope_atomically() {
        let source = SparsePages::new([]);
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(1).unwrap();
        let foreign = pool.reserve_scope(1).unwrap();
        let mut bindings = [BitmapCowArenaBinding::empty()];
        let mut replacements = [];
        let mut candidates = [];
        let mut index = [BitmapCowIndexNode::empty()];
        let mut available = [0usize; 1];
        let ledger = scoped_ledger(
            &mut bindings,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow =
            FreeBitmapCow::from_scoped_pool(&source, 1, 20, 0, &pool, &scope, ledger).unwrap();

        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&foreign, 0)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);

        pool.close_scope(&scope).unwrap();
        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);

        let replacement = pool.reserve_scope(1).unwrap();
        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&replacement, 0)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);
    }

    #[test]
    fn scoped_sync_rejects_unselected_candidate_wrong_authorization_and_stale_page_count() {
        fn planned_case(
            selected: usize,
            page: u32,
            authorization: PrivatePageAuthorization,
        ) -> FreeBitmapCowError {
            let source = SparsePages::new([]);
            let mut slots = [PrivatePagePoolSlot::empty()];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let scope = pool.reserve_scope(1).unwrap();
            let mut bindings = [BitmapCowArenaBinding::empty()];
            let mut replacements = [];
            let mut candidates = [5u32, 6];
            let mut index = [BitmapCowIndexNode::empty(); 3];
            let mut available = [0usize; 1];
            let ledger = ScopedFreeBitmapCowLedger::new(
                &mut bindings,
                &mut replacements,
                0,
                &mut candidates,
                0,
                &mut index,
                &mut available,
                &mut [],
                2,
                true,
                0,
                0,
            );
            let mut cow =
                FreeBitmapCow::from_scoped_pool(&source, 1, 20, 0, &pool, &scope, ledger).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.bind_page(&checkpoint, &scope, page, authorization)
                .unwrap();
            pool.commit_checkpoint(checkpoint).unwrap();
            let before = snapshot_scoped_cow(&cow);
            let error = cow
                .synchronize_scoped_bindings_for_candidate_prefix(&scope, selected)
                .unwrap_err();
            assert_eq!(snapshot_scoped_cow(&cow), before);
            error
        }

        assert_eq!(
            planned_case(1, 6, PrivatePageAuthorization::CommittedFree),
            FreeBitmapCowError::LedgerPageConflict(6)
        );
        assert_eq!(
            planned_case(1, 5, PrivatePageAuthorization::SafelyReclaimed),
            FreeBitmapCowError::ArenaPageConflict(5)
        );

        let source = SparsePages::new([]);
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(1).unwrap();
        let mut bindings = [BitmapCowArenaBinding::empty()];
        let mut replacements = [];
        let mut candidates = [0u32; 1];
        let mut index = [BitmapCowIndexNode::empty()];
        let mut available = [0usize; 1];
        let ledger = scoped_ledger(
            &mut bindings,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow =
            FreeBitmapCow::from_scoped_pool(&source, 1, 20, 0, &pool, &scope, ledger).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(&checkpoint, &scope, 20, PrivatePageAuthorization::Appended)
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let before = snapshot_scoped_cow(&cow);
        assert!(cow.remove_lowest().is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);
        cow.synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            .unwrap();
        assert_eq!(cow.pending_page_count(), 21);
    }

    #[test]
    fn scoped_candidate_identity_and_order_failures_are_atomic_and_retryable() {
        let source = SparsePages::new([]);
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut bindings = [BitmapCowArenaBinding {
            pool_slot: 7,
            pool_epoch: 7,
            page_number: 7,
            storage_node: 7,
            active_node: 7,
            bound: true,
        }];
        let mut replacements = [];
        let mut candidates = [6u32, 5];
        let mut index = [BitmapCowIndexNode::empty(); 3];
        let mut available = [7usize; 1];
        {
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let scope = pool.reserve_scope(1).unwrap();
            let ledger = ScopedFreeBitmapCowLedger::new(
                &mut bindings,
                &mut replacements,
                0,
                &mut candidates,
                0,
                &mut index,
                &mut available,
                &mut [],
                2,
                true,
                0,
                0,
            );
            let error = FreeBitmapCow::from_scoped_pool(&source, 1, 20, 0, &pool, &scope, ledger)
                .unwrap_err();
            assert_eq!(
                error,
                FreeBitmapCowError::CandidateOrderRegression {
                    previous: 6,
                    current: 5,
                }
            );
        }
        assert_eq!(bindings, [BitmapCowArenaBinding::empty()]);
        assert!(index
            .iter()
            .all(|node| *node == BitmapCowIndexNode::empty()));
        assert_eq!(available, [0]);

        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(1).unwrap();
        candidates.copy_from_slice(&[5, 6]);
        let ledger = ScopedFreeBitmapCowLedger::new(
            &mut bindings,
            &mut replacements,
            0,
            &mut candidates,
            0,
            &mut index,
            &mut available,
            &mut [],
            2,
            true,
            0,
            0,
        );
        let mut cow =
            FreeBitmapCow::from_scoped_pool(&source, 1, 20, 0, &pool, &scope, ledger).unwrap();
        let candidate_node = page_index_find_node(cow.index_nodes, cow.index_root, 5).unwrap();
        let original = cow.index_nodes[candidate_node];
        cow.index_nodes[candidate_node].candidate_index = 1;
        let before = snapshot_scoped_cow(&cow);
        assert!(cow
            .synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            .is_err());
        assert_eq!(snapshot_scoped_cow(&cow), before);
        cow.index_nodes[candidate_node] = original;
        cow.synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            .unwrap();
    }

    #[test]
    fn scoped_sync_is_zero_allocation_and_balanced_at_512_and_4096_slots() {
        fn verify(nodes: &[BitmapCowIndexNode], root: usize) -> (u8, usize) {
            if root == NO_INDEX {
                return (0, 0);
            }
            let (left_height, left_count) = verify(nodes, nodes[root].left);
            let (right_height, right_count) = verify(nodes, nodes[root].right);
            assert!(left_height.abs_diff(right_height) <= 1);
            let height = 1 + left_height.max(right_height);
            assert_eq!(nodes[root].height, height);
            (height, left_count + right_count + 1)
        }

        fn run(count: usize) -> u8 {
            let source = SparsePages::new([]);
            let mut slots = vec![PrivatePagePoolSlot::empty(); count];
            let page_count = u64::try_from(count + 2).unwrap();
            let pool = PrivatePagePool::new_vacant(&mut slots, page_count, page_count, 2).unwrap();
            let scope = pool.reserve_scope(count).unwrap();
            let mut bindings = vec![BitmapCowArenaBinding::empty(); count];
            let mut replacements = [];
            let mut candidates = [];
            let mut index = vec![BitmapCowIndexNode::empty(); count];
            let mut available = vec![0usize; count];
            let ledger = scoped_ledger(
                &mut bindings,
                &mut replacements,
                &mut candidates,
                &mut index,
                &mut available,
            );
            let mut cow =
                FreeBitmapCow::from_scoped_pool(&source, 1, page_count, 0, &pool, &scope, ledger)
                    .unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            for index in 0..count {
                let pgno = u32::try_from(2 + ((index * 4_051) & (count - 1))).unwrap();
                pool.bind_page(
                    &checkpoint,
                    &scope,
                    pgno,
                    PrivatePageAuthorization::SafelyReclaimed,
                )
                .unwrap();
            }
            pool.commit_checkpoint(checkpoint).unwrap();
            let (result, allocations) = count_thread_allocations(|| {
                cow.synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
            });
            assert_eq!(allocations, 0);
            result.unwrap();
            let ((), repeated_allocations) = count_thread_allocations(|| {
                for _ in 0..100 {
                    cow.synchronize_scoped_bindings_for_candidate_prefix(&scope, 0)
                        .unwrap();
                }
            });
            assert_eq!(repeated_allocations, 0);
            assert_eq!(cow.available_private_pages(), count);
            let (height, visited) = verify(cow.index_nodes, cow.index_root);
            assert_eq!(visited, count);
            let logarithmic_height_bound =
                2 * u8::try_from(usize::BITS - count.leading_zeros()).unwrap();
            assert!(height <= logarithmic_height_bound);
            height
        }

        let small = run(512);
        let large = run(4_096);
        assert!(large <= small + 6);
    }

    fn branch(pgno: u32, txn: u64, level: u16, child: u32) -> SparsePage {
        branch_at(pgno, txn, level, 0, child)
    }

    fn branch_at(pgno: u32, txn: u64, level: u16, child_index: usize, child: u32) -> SparsePage {
        branch_many(pgno, txn, level, &[(child_index, child)])
    }

    fn branch_many(pgno: u32, txn: u64, level: u16, children: &[(usize, u32)]) -> SparsePage {
        let mut bytes = [0u8; PAGE_SIZE];
        for &(child_index, child) in children {
            let summary_offset = SUMMARY_OFFSET + (child_index / 64) * 8;
            let summary = u64::from_le_bytes(
                bytes[summary_offset..summary_offset + 8]
                    .try_into()
                    .unwrap(),
            );
            bytes[summary_offset..summary_offset + 8]
                .copy_from_slice(&(summary | (1u64 << (child_index % 64))).to_le_bytes());
            let child_offset = CHILDREN_OFFSET + child_index * 4;
            bytes[child_offset..child_offset + 4].copy_from_slice(&child.to_le_bytes());
        }
        write_bitmap_header(
            &mut bytes,
            PageType::BitmapBranch,
            txn,
            u16::try_from(children.len()).unwrap(),
            level,
            BRANCH_LOWER,
        );
        SparsePage {
            pgno,
            bytes,
            reads: Cell::new(0),
        }
    }

    fn header(page: &[u8; PAGE_SIZE], txn: u64) -> PageHeader {
        PageHeader::decode(page, txn).unwrap()
    }

    struct PlannerStorage {
        arena: Vec<ReservedBitmapPage>,
        pool_validation: Vec<PrivatePageCompositeBind>,
        arena_bindings: Vec<BitmapCowArenaBinding>,
        candidates: Vec<u32>,
        verified: Vec<VerifiedBitmapPage>,
        replacements: Vec<u32>,
        index: Vec<BitmapCowIndexNode>,
        available: Vec<usize>,
        source_nodes: Vec<FreeBitmapReservationSourceNode>,
        reclamation: FreeBitmapReclamationTicket,
        stage_arena: Vec<ReservedBitmapPage>,
        stage_bindings: Vec<BitmapCowArenaBinding>,
        stage_candidates: Vec<u32>,
        stage_verified: Vec<VerifiedBitmapPage>,
        stage_replacements: Vec<u32>,
        stage_index: Vec<BitmapCowIndexNode>,
        stage_available: Vec<usize>,
    }

    impl PlannerStorage {
        fn new(arena: usize, candidates: usize, verified: usize, index: usize) -> Self {
            Self {
                arena: (0..arena).map(|_| ReservedBitmapPage::empty()).collect(),
                pool_validation: vec![PrivatePageCompositeBind::empty(); arena],
                arena_bindings: vec![BitmapCowArenaBinding::empty(); arena],
                candidates: vec![0; candidates],
                verified: (0..verified).map(|_| VerifiedBitmapPage::empty()).collect(),
                replacements: vec![0; verified],
                index: vec![BitmapCowIndexNode::empty(); index],
                available: vec![0; arena],
                source_nodes: vec![FreeBitmapReservationSourceNode::empty(); candidates + arena],
                reclamation: FreeBitmapReclamationTicket::new(),
                stage_arena: (0..arena).map(|_| ReservedBitmapPage::empty()).collect(),
                stage_bindings: vec![BitmapCowArenaBinding::empty(); arena],
                stage_candidates: vec![0; candidates],
                stage_verified: (0..verified).map(|_| VerifiedBitmapPage::empty()).collect(),
                stage_replacements: vec![0; verified],
                stage_index: vec![BitmapCowIndexNode::empty(); index],
                stage_available: vec![0; arena],
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
                reclamation: &self.reclamation,
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

    pub(crate) fn with_finalized_bitmap_export<R>(
        pages: &mut [PrivatePageCoordinatorTerminalPage],
        consume: impl FnOnce(PreparedFreeBitmapTerminalExport<'_, '_, '_, '_, '_, SparsePages<1>>) -> R,
    ) -> R {
        let source = SparsePages::new([leaf(11, 1, &[5])]);
        let mut storage = PlannerStorage::new(2, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 1, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(plan.required_private_pages()).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let mut bound = attachment.bind(proof).unwrap();
        bound.cow.apply_planned_reservation().unwrap();
        let required = bound.finalization_scratch_requirements().unwrap();
        let mut release = vec![0; required.release_pages];
        let mut insert: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cache = vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let finalized = bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release,
                insert_pages: &mut insert,
                cached_pages: &mut cache,
                index_stack: &mut stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
            .unwrap();
        let export = match finalized
            .output
            .prepare_terminal_export(finalized.successor, pages)
        {
            Ok(export) => export,
            Err((_output, _successor, _pages, error)) => panic!("{error:?}"),
        };
        consume(export)
    }

    struct CowStorage {
        arena: Vec<ReservedBitmapPage>,
        replacements: Vec<u32>,
        candidates: Vec<u32>,
        index: Vec<BitmapCowIndexNode>,
        available: Vec<usize>,
    }

    impl CowStorage {
        fn new(page_numbers: impl IntoIterator<Item = u32>, replacements: usize) -> Self {
            let arena: Vec<_> = page_numbers
                .into_iter()
                .map(ReservedBitmapPage::new)
                .collect();
            let arena_len = arena.len();
            Self {
                arena,
                replacements: vec![0; replacements],
                candidates: Vec::new(),
                index: vec![BitmapCowIndexNode::empty(); arena_len + replacements],
                available: vec![0; arena_len],
            }
        }

        fn cow<'a, S: CommittedPageSource + ?Sized>(
            &'a mut self,
            source: &'a S,
            page_count: u64,
            root: u32,
        ) -> FreeBitmapCow<'a, 'a, 'a, S> {
            FreeBitmapCow::new(
                source,
                1,
                page_count,
                root,
                FreeBitmapCowLedger::empty(
                    &mut self.arena,
                    &mut self.replacements,
                    &mut self.candidates,
                    &mut self.index,
                    &mut self.available,
                ),
            )
            .unwrap()
        }

        fn cow_with_page_counts<'a, S: CommittedPageSource + ?Sized>(
            &'a mut self,
            source: &'a S,
            committed_page_count: u64,
            pending_page_count: u64,
            root: u32,
        ) -> FreeBitmapCow<'a, 'a, 'a, S> {
            FreeBitmapCow::new_with_page_counts(
                source,
                1,
                committed_page_count,
                pending_page_count,
                root,
                FreeBitmapCowLedger::empty(
                    &mut self.arena,
                    &mut self.replacements,
                    &mut self.candidates,
                    &mut self.index,
                    &mut self.available,
                ),
            )
            .unwrap()
        }
    }

    fn private_free_bit<S: CommittedPageSource + ?Sized>(
        cow: &FreeBitmapCow<'_, '_, '_, S>,
        bit: u32,
    ) -> bool {
        let mut pgno = cow.root();
        let mut level = minimum_level(cow.pending_page_count()).unwrap();
        let mut base = 0u64;
        loop {
            let page = cow.private_page(pgno).unwrap();
            if level == 0 {
                let local = u64::from(bit) - base;
                return raw_leaf_word(&page, usize::try_from(local / 64).unwrap())
                    & (1u64 << (local % 64))
                    != 0;
            }
            let child_span = coverage(level - 1).unwrap();
            let child = usize::try_from((u64::from(bit) - base) / child_span).unwrap();
            pgno = raw_branch_child(&page, child);
            if pgno == 0 {
                return false;
            }
            base += child_span * child as u64;
            level -= 1;
        }
    }

    fn prove_root_boundary<const N: usize>(source: SparsePages<N>, boundary: u64, old_level: u16) {
        let mut storage = CowStorage::new([100, 101], 0);
        let mut cow = storage.cow(&source, boundary, 2);
        let mut scratch: Vec<_> = (0..FREE_PATH_CAPACITY)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();

        let exact = cow.plan_insert_free(&[5], &mut scratch).unwrap();
        assert_eq!(exact.required_private_pages(), 0);
        exact.apply().unwrap();
        assert_eq!(cow.root(), 2);

        cow.pending_page_count = boundary + 1;
        let promoted = cow.plan_insert_free(&[5], &mut scratch).unwrap();
        assert_eq!(promoted.required_private_pages(), 1);
        let promoted_root = promoted.result_root();
        promoted.apply().unwrap();
        assert_eq!(cow.root(), promoted_root);
        let root_page = cow.private_page(promoted_root).unwrap();
        let root = BitmapBranch::open(&root_page, 2, BitmapKind::FreePages).unwrap();
        assert_eq!(root.level(), old_level + 1);
        assert_eq!(root.child(0), 2);
        assert_eq!(PageHeader::decode(&root_page, 2).unwrap().item_count, 1);
        drop(root_page);

        let demoted = cow
            .plan_insert_free_for_page_count(&[], boundary, &mut scratch)
            .unwrap();
        let result = demoted.apply().unwrap();
        assert_eq!(result.recycled_private_pages, 1);
        assert_eq!(cow.root(), 2);
        assert_eq!(cow.pending_page_count(), boundary);
        assert_eq!(cow.available_private_pages(), 2);
    }

    #[test]
    fn access_is_checked_before_every_planner_and_planned_apply_entry() {
        let source = AccessControlledPages::new([leaf(2, 1, &[5])]);
        let mut plan_storage = PlannerStorage::new(1, 1, 1, 2);
        let planner =
            FreeBitmapReservationPlanner::new(&source, 1, 20, 2, 0, plan_storage.buffers())
                .unwrap();
        assert_eq!(source.checks.get(), 1);
        source.deny_access(PageSourceError::ForkedHandle);
        let result = planner.plan();
        assert!(matches!(
            result,
            Err(FreeBitmapCowError::Source(PageSourceError::ForkedHandle))
        ));
        assert_eq!(source.checks.get(), 2);
        assert_eq!(source.source.reads.get(), 0);
        assert_eq!(plan_storage.arena[0], ReservedBitmapPage::empty());
        assert_eq!(plan_storage.candidates, &[0]);
        assert_eq!(plan_storage.verified[0], VerifiedBitmapPage::empty());

        source.allow_access();
        let mut apply_storage = PlannerStorage::new(1, 0, 0, 1);
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 0, 1, apply_storage.buffers())
            .unwrap()
            .plan()
            .unwrap();
        assert_eq!(plan.candidates(), &[]);
        assert_eq!(plan.appended_pages(), 1);
        let mut cow = plan.into_cow();
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.available_private_pages(), 1);
        assert_eq!(cow.test_state_at(0), PrivatePageState::Available);
        assert_eq!(
            cow.test_authorization_at(0),
            PrivatePageAuthorization::Appended
        );

        source.deny_access(PageSourceError::ForkedHandle);
        let before_checks = source.checks.get();
        let result = cow.apply_planned_reservation();
        assert!(matches!(
            result,
            Err(FreeBitmapCowError::Source(PageSourceError::ForkedHandle))
        ));
        assert_eq!(source.checks.get(), before_checks + 1);
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.candidates(), &[]);
        assert_eq!(cow.available_private_pages(), 1);
        assert_eq!(cow.test_state_at(0), PrivatePageState::Available);

        source.allow_access();
        cow.apply_planned_reservation().unwrap();
        source.deny_access(PageSourceError::ForkedHandle);
        let before_checks = source.checks.get();
        assert!(matches!(
            cow.remove_lowest(),
            Err(FreeBitmapCowError::Source(PageSourceError::ForkedHandle))
        ));
        assert_eq!(source.checks.get(), before_checks + 1);
    }

    #[test]
    fn late_binding_attaches_verified_reclaim_authority_to_shared_pool() {
        let source = AccessControlledPages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        // Production capacity planning owns no legacy all-bound arena.
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        assert_eq!(plan.required_private_pages(), 3);
        assert_eq!(plan.candidates(), [5, 9]);
        assert_eq!(
            plan.buffers.source_nodes[..2]
                .iter()
                .map(|node| node.required)
                .collect::<Vec<_>>(),
            [3, 3]
        );

        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let foreign = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let foreign_slot = pool
            .bind_page(
                &checkpoint,
                &foreign,
                17,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let foreign_before = pool.scoped_slot_info(&foreign, foreign_slot).unwrap();
        let target = pool.reserve_scope(3).unwrap();

        let (attachment, request) = plan.attach(&pool, &target).unwrap();
        assert_eq!(
            pool.scoped_slot_info(&foreign, foreign_slot).unwrap(),
            foreign_before
        );
        let reclaimed = [3, 7];
        let reclaimed = crate::retirement_reader::test_reclaimed_pages(&reclaimed);
        let proof = complete_free_bitmap_reclamation(
            request,
            RetirementReclamation::Reclaimed(reclaimed.unwrap()),
        )
        .unwrap();
        let (bound, allocations) = count_thread_allocations(|| attachment.bind(proof));
        let bound = bound.unwrap();
        assert_eq!(allocations, 0);
        assert_eq!(source.checks.get(), 3);
        assert_eq!(source.source.page_reads(11), 1);
        assert_eq!(
            bound.binding,
            FreeBitmapReservationBinding {
                committed: 1,
                reclaimed: 2,
                appended: 0,
            }
        );
        assert_eq!(pool.pending_page_count(), 20);
        let pages: Vec<_> = bound.cow.arena_bindings[..3]
            .iter()
            .map(|binding| {
                pool.scoped_slot_info(&target, binding.pool_slot)
                    .unwrap()
                    .unwrap()
                    .pgno
            })
            .collect();
        assert_eq!(pages, [3, 5, 7]);
        assert_eq!(bound.cow.candidates(), [5]);
        assert_eq!(bound.cow.available_private_pages(), 2);
        assert_eq!(
            pool.scoped_slot_info(&foreign, foreign_slot).unwrap(),
            foreign_before
        );
    }

    #[test]
    fn reclamation_nonce_saturates_without_wrapping() {
        let counter = AtomicU64::new(u64::MAX - 1);
        assert_eq!(mint_free_bitmap_reclamation_nonce(&counter), Some(u64::MAX));
        assert_eq!(mint_free_bitmap_reclamation_nonce(&counter), None);
    }

    #[test]
    fn late_binding_final_callback_fences_foreign_pool_mutation_before_target_bind() {
        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let foreign = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &foreign,
            17,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let target = pool.reserve_scope(3).unwrap();
        let source = ForeignMutatingPages {
            pages: SparsePages::new([leaf(11, 1, &[5, 9])]),
            pool: &pool,
            scope: &foreign,
            pgno: 17,
            armed: Cell::new(false),
            fail_when_armed: false,
            checks: Cell::new(0),
            fire_on_check: Cell::new(None),
        };
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let (attachment, request) = plan.attach(&pool, &target).unwrap();
        let reclaimed = [3, 7];
        let proof = complete_free_bitmap_reclamation_for_test(request, 51, &reclaimed).unwrap();
        source.armed.set(true);
        assert!(matches!(
            attachment.bind(proof),
            Err(FreeBitmapCowError::StaleInsertionPlan)
        ));
        assert_eq!(pool.scope_status(&target).unwrap().bound, 0);
        assert!(matches!(
            pool.scoped_slot_info(&foreign, 0).unwrap().unwrap().state,
            PrivatePagePoolState::InUse {
                owner: PrivatePageOwner::Retirement,
                ..
            }
        ));
    }

    #[test]
    fn late_binding_appends_only_after_both_eligible_sources_are_exhausted() {
        let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let bound = attachment.bind(proof).unwrap();
        assert_eq!(
            bound.binding,
            FreeBitmapReservationBinding {
                committed: 2,
                reclaimed: 0,
                appended: 1,
            }
        );
        assert_eq!(pool.pending_page_count(), 21);
        let pages: Vec<_> = bound.cow.arena_bindings[..3]
            .iter()
            .map(|binding| {
                pool.scoped_slot_info(&scope, binding.pool_slot)
                    .unwrap()
                    .unwrap()
                    .pgno
            })
            .collect();
        assert_eq!(pages, [5, 9, 20]);
    }

    #[test]
    fn current_draft_planner_reads_prior_sealed_private_root_with_exact_provenance() {
        use crate::writer_fixed_point::{
            DraftPageProvenance, FixedPointDraftSource, FixedPointSealedLedger,
        };

        let committed = SparsePages::new([leaf(11, 1, &[5, 9, 13])]);
        let mut first_storage = PlannerStorage::new(2, 4, 4, 8);
        first_storage.arena.clear();
        let first_plan =
            FreeBitmapReservationPlanner::new(&committed, 1, 20, 11, 1, first_storage.buffers())
                .unwrap()
                .plan_capacity()
                .unwrap();
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 8];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let first_scope = pool
            .reserve_scope(first_plan.required_private_pages())
            .unwrap();
        let (first_attachment, first_request) = first_plan.attach(&pool, &first_scope).unwrap();
        let first_proof = complete_free_bitmap_reclamation_for_test(first_request, 0, &[]).unwrap();
        let first_bound = first_attachment.bind(first_proof).unwrap();
        let first_required = first_bound.finalization_scratch_requirements().unwrap();
        let mut first_release = vec![0; first_required.release_pages];
        let mut first_insert: Vec<_> = (0..first_required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut first_cache =
            vec![FreeBitmapFinalizationCachedPage::empty(); first_required.cached_pages];
        let mut first_stack = vec![usize::MAX; first_required.index_stack];
        let mut first_cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            first_required.cleanup_nodes
        ];
        let mut first_cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            first_required.cleanup_path
        ];
        let mut first_cleanup_targets = vec![usize::MAX; first_required.cleanup_targets];
        let first_final = first_bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut first_release,
                insert_pages: &mut first_insert,
                cached_pages: &mut first_cache,
                index_stack: &mut first_stack,
                cleanup_nodes: &mut first_cleanup_nodes,
                cleanup_path: &mut first_cleanup_path,
                cleanup_targets: &mut first_cleanup_targets,
            })
            .unwrap();
        assert_eq!(first_final.released.reinserted_candidates, 1);
        let first_record =
            match first_final
                .output
                .into_coordinator_record(first_final.successor, 101, 0)
            {
                Ok(record) => record,
                Err((_output, _successor, error)) => panic!("{error:?}"),
            };
        assert_ne!(first_record.root(), 0);
        let first_root = first_record.root();
        let first_tail = first_record.pending_page_count();

        let mut private_entries = [None; 8];
        let mut slot_to_entry = [usize::MAX; 8];
        let mut records = [None, None, None];
        let mut slot_to_record = [usize::MAX; 8];
        let draft_source =
            FixedPointDraftSource::new(&committed, &pool, &mut private_entries, &mut slot_to_entry)
                .unwrap();
        let mut ledger =
            FixedPointSealedLedger::new(&mut records, &mut slot_to_record, pool.len()).unwrap();
        ledger.push(first_record, &draft_source).unwrap();
        assert_eq!(ledger.len(), 1);
        let mut root_bytes = [0; PAGE_SIZE];
        assert!(matches!(
            draft_source
                .read_page_with_provenance(first_root, &mut root_bytes)
                .unwrap(),
            DraftPageProvenance::Private { work_unit: 101, .. }
        ));

        let mut second_storage = PlannerStorage::new(2, 4, 4, 8);
        second_storage.arena.clear();
        let second_plan = FreeBitmapReservationPlanner::new_for_draft(
            &draft_source,
            2,
            2,
            20,
            first_tail,
            first_root,
            1,
            second_storage.buffers(),
        )
        .unwrap()
        .after_carried_source(9)
        .unwrap()
        .plan_capacity()
        .unwrap();
        let second_scope = pool
            .reserve_scope(second_plan.required_private_pages())
            .unwrap();
        assert_eq!(second_plan.candidates(), &[13]);
        assert_eq!(pool.find_bound_page(9).unwrap(), None);
        let (second_attachment, second_request) = second_plan
            .attach_current_draft(&pool, &second_scope)
            .unwrap();
        let second_proof =
            complete_free_bitmap_reclamation_for_test(second_request, 0, &[]).unwrap();
        let second_bound = second_attachment.bind(second_proof).unwrap();
        let second_required = second_bound.finalization_scratch_requirements().unwrap();
        let mut second_release = vec![0; second_required.release_pages];
        let mut second_insert: Vec<_> = (0..second_required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut second_cache =
            vec![FreeBitmapFinalizationCachedPage::empty(); second_required.cached_pages];
        let mut second_stack = vec![usize::MAX; second_required.index_stack];
        let mut second_cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            second_required.cleanup_nodes
        ];
        let mut second_cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            second_required.cleanup_path
        ];
        let mut second_cleanup_targets = vec![usize::MAX; second_required.cleanup_targets];
        let second_final = second_bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut second_release,
                insert_pages: &mut second_insert,
                cached_pages: &mut second_cache,
                index_stack: &mut second_stack,
                cleanup_nodes: &mut second_cleanup_nodes,
                cleanup_path: &mut second_cleanup_path,
                cleanup_targets: &mut second_cleanup_targets,
            })
            .unwrap();
        let mut second_record =
            match second_final
                .output
                .into_coordinator_record(second_final.successor, 202, 1)
            {
                Ok(record) => record,
                Err((_output, _successor, error)) => panic!("{error:?}"),
            };
        assert!(second_record.replacements().contains(&first_root));
        second_record.work_unit = 101;
        let (mut second_record, error) = ledger
            .push(second_record, &draft_source)
            .expect_err("duplicate work units must be rejected before registration");
        assert_eq!(
            error,
            crate::writer_fixed_point::FixedPointError::InvalidWorkUnit
        );
        assert_eq!(ledger.len(), 1);
        second_record.work_unit = 100;
        let (mut second_record, error) = ledger
            .push(second_record, &draft_source)
            .expect_err("regressed work units must be rejected before registration");
        assert_eq!(
            error,
            crate::writer_fixed_point::FixedPointError::InvalidWorkUnit
        );
        assert_eq!(ledger.len(), 1);
        second_record.work_unit = 202;
        ledger.push(second_record, &draft_source).unwrap();
        let replaced = draft_source
            .private_location(first_root)
            .unwrap()
            .expect("the first root is still an exact private draft page");
        let DraftPageProvenance::Private { page, .. } = replaced.provenance else {
            unreachable!()
        };
        let before_wrong_owner = pool.test_mutation_snapshot();
        assert_eq!(
            ledger.record_mut(0).unwrap().return_private_page(
                crate::writer_fixed_point::DraftPrivatePageLocation {
                    provenance: DraftPageProvenance::Private {
                        work_unit: 999,
                        page,
                    },
                    ..replaced
                }
            ),
            Err(FreeBitmapCowError::StaleReservationPredecessor)
        );
        assert_eq!(pool.test_mutation_snapshot(), before_wrong_owner);
        assert_eq!(
            ledger
                .record(0)
                .unwrap()
                .private_provenance(first_root)
                .unwrap(),
            Some(replaced.provenance)
        );
        let (returned, return_allocations) =
            count_thread_allocations(|| ledger.return_prior_private(first_root, &draft_source));
        returned.unwrap();
        assert_eq!(return_allocations, 0);
        assert_eq!(pool.find_bound_page(first_root).unwrap(), None);
        assert_eq!(
            ledger
                .record(0)
                .unwrap()
                .private_provenance(first_root)
                .unwrap(),
            None
        );
        let second_root = ledger.record(1).unwrap().root();
        let second_tail = ledger.record(1).unwrap().pending_page_count();
        let mut third_storage = PlannerStorage::new(2, 4, 4, 8);
        third_storage.arena.clear();
        let third_plan = FreeBitmapReservationPlanner::new_for_draft(
            &draft_source,
            2,
            2,
            20,
            second_tail,
            second_root,
            1,
            third_storage.buffers(),
        )
        .unwrap()
        .after_carried_source(13)
        .unwrap()
        .plan_capacity()
        .unwrap();
        let third_scope = pool
            .reserve_scope(third_plan.required_private_pages())
            .unwrap();
        let (third_attachment, third_request) = third_plan
            .attach_current_draft(&pool, &third_scope)
            .unwrap();
        let third_proof = complete_free_bitmap_reclamation_for_test(third_request, 0, &[]).unwrap();
        let third_bound = third_attachment.bind(third_proof).unwrap();
        let third_required = third_bound.finalization_scratch_requirements().unwrap();
        let mut third_release = vec![0; third_required.release_pages];
        let mut third_insert: Vec<_> = (0..third_required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut third_cache =
            vec![FreeBitmapFinalizationCachedPage::empty(); third_required.cached_pages];
        let mut third_stack = vec![usize::MAX; third_required.index_stack];
        let mut third_cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            third_required.cleanup_nodes
        ];
        let mut third_cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            third_required.cleanup_path
        ];
        let mut third_cleanup_targets = vec![usize::MAX; third_required.cleanup_targets];
        let third_final = third_bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut third_release,
                insert_pages: &mut third_insert,
                cached_pages: &mut third_cache,
                index_stack: &mut third_stack,
                cleanup_nodes: &mut third_cleanup_nodes,
                cleanup_path: &mut third_cleanup_path,
                cleanup_targets: &mut third_cleanup_targets,
            })
            .unwrap();
        let third_record =
            match third_final
                .output
                .into_coordinator_record(third_final.successor, 303, 2)
            {
                Ok(record) => record,
                Err((_output, _successor, error)) => panic!("{error:?}"),
            };
        ledger.push(third_record, &draft_source).unwrap();
        assert_eq!(ledger.record(0).unwrap().work_unit, 101);
        assert_eq!(ledger.record(1).unwrap().work_unit, 202);
        assert_eq!(ledger.record(2).unwrap().work_unit, 303);
        let records = ledger.into_records();
        for record in records.iter_mut().rev().filter_map(Option::take) {
            record.cleanup().unwrap();
        }
        assert!(pool.reserve_scope(8).is_ok());
    }

    #[test]
    fn finalization_promotes_sole_selected_page_to_reachable_empty_leaf() {
        let source = SparsePages::new([leaf(11, 1, &[5])]);
        let mut storage = PlannerStorage::new(2, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 1, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(plan.required_private_pages()).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let mut bound = attachment.bind(proof).unwrap();
        assert_eq!(bound.cow.candidates(), &[5]);
        bound.cow.apply_planned_reservation().unwrap();
        assert_eq!(bound.cow.root(), 0);
        assert_eq!(bound.cow.selected_candidate_target(), 1);
        assert_eq!(bound.cow.available_private_pages(), 1);

        let required = bound.finalization_scratch_requirements().unwrap();
        let mut release = vec![0; required.release_pages];
        let mut insert: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cache = vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let result = bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release,
                insert_pages: &mut insert,
                cached_pages: &mut cache,
                index_stack: &mut stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
            .unwrap();
        assert_eq!(result.output.root(), 5);
        let mut bytes = [0; PAGE_SIZE];
        result.output.read_page(5, &mut bytes).unwrap();
        let leaf = BitmapLeaf::open(&bytes, 2, BitmapKind::FreePages).unwrap();
        assert_eq!(header(&bytes, 2).item_count, 0);
        assert!(search_free_leaf(leaf, 0, 20).unwrap().is_none());
        let first_record = match result
            .output
            .into_coordinator_record(result.successor, 101, 0)
        {
            Ok(record) => record,
            Err((_output, _successor, error)) => panic!("{error:?}"),
        };
        let first_root = first_record.root();
        let first_tail = first_record.pending_page_count();
        let mut private_entries = [const { None }; 2];
        let mut slot_to_entry = [usize::MAX; 2];
        let mut records = [None, None];
        let mut slot_to_record = [usize::MAX; 2];
        let draft = crate::writer_fixed_point::FixedPointDraftSource::new(
            &source,
            &pool,
            &mut private_entries,
            &mut slot_to_entry,
        )
        .unwrap();
        let mut ledger = crate::writer_fixed_point::FixedPointSealedLedger::new(
            &mut records,
            &mut slot_to_record,
            pool.len(),
        )
        .unwrap();
        ledger.push(first_record, &draft).unwrap();

        let mut second_storage = PlannerStorage::new(2, 4, 4, 8);
        second_storage.arena.clear();
        let second_plan = FreeBitmapReservationPlanner::new_for_draft(
            &draft,
            2,
            2,
            20,
            first_tail,
            first_root,
            1,
            second_storage.buffers(),
        )
        .unwrap()
        .plan_capacity()
        .unwrap();
        assert_eq!(second_plan.candidates(), &[]);
        assert_eq!(second_plan.required_private_pages(), 1);
        let second_scope = pool.reserve_scope(1).unwrap();
        let (second_attachment, second_request) = second_plan
            .attach_current_draft(&pool, &second_scope)
            .unwrap();
        let second_proof =
            complete_free_bitmap_reclamation_for_test(second_request, 0, &[]).unwrap();
        let mut second_bound = second_attachment.bind(second_proof).unwrap();
        second_bound.cow.apply_planned_reservation().unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let retirement = pool
            .claim_page_in_scope(
                &checkpoint,
                &second_scope,
                first_tail as u32,
                PrivatePageOwner::Retirement,
                2,
                1,
            )
            .unwrap();
        pool.borrow_page_mut_in_scope(&second_scope, &retirement)
            .unwrap()[33] = 0xa5;
        pool.commit_checkpoint(checkpoint).unwrap();
        second_bound
            .cow
            .synchronize_scoped_bindings(&second_scope)
            .unwrap();
        let second_required = second_bound.finalization_scratch_requirements().unwrap();
        let mut second_release = vec![0; second_required.release_pages];
        let mut second_insert: Vec<_> = (0..second_required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut second_cache =
            vec![FreeBitmapFinalizationCachedPage::empty(); second_required.cached_pages];
        let mut second_stack = vec![usize::MAX; second_required.index_stack];
        let mut second_cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            second_required.cleanup_nodes
        ];
        let mut second_cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            second_required.cleanup_path
        ];
        let mut second_cleanup_targets = vec![usize::MAX; second_required.cleanup_targets];
        let second_final = second_bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut second_release,
                insert_pages: &mut second_insert,
                cached_pages: &mut second_cache,
                index_stack: &mut second_stack,
                cleanup_nodes: &mut second_cleanup_nodes,
                cleanup_path: &mut second_cleanup_path,
                cleanup_targets: &mut second_cleanup_targets,
            })
            .unwrap();
        let second_record =
            match second_final
                .output
                .into_coordinator_record(second_final.successor, 202, 1)
            {
                Ok(record) => record,
                Err((_output, _successor, error)) => panic!("{error:?}"),
            };
        assert_eq!(second_record.root(), first_root);
        assert_eq!(second_record.pending_page_count(), first_tail + 1);
        ledger.push(second_record, &draft).unwrap();

        let mut third_storage = PlannerStorage::new(2, 4, 4, 8);
        third_storage.arena.clear();
        let third_plan = FreeBitmapReservationPlanner::new_for_draft(
            &draft,
            2,
            2,
            20,
            first_tail + 1,
            first_root,
            1,
            third_storage.buffers(),
        )
        .unwrap()
        .plan_capacity()
        .unwrap();
        assert_eq!(third_plan.candidates(), &[]);
        assert_eq!(third_plan.required_private_pages(), 1);

        for record in ledger
            .into_records()
            .iter_mut()
            .rev()
            .filter_map(Option::take)
        {
            record.cleanup().unwrap();
        }
    }

    #[test]
    fn terminal_export_is_produced_only_by_the_real_bitmap_finalizer() {
        let source = SparsePages::new([leaf(11, 1, &[5])]);
        let mut storage = PlannerStorage::new(2, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 1, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(plan.required_private_pages()).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let mut bound = attachment.bind(proof).unwrap();
        bound.cow.apply_planned_reservation().unwrap();
        let required = bound.finalization_scratch_requirements().unwrap();
        let mut release = vec![0; required.release_pages];
        let mut insert: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cache = vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let finalized = bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release,
                insert_pages: &mut insert,
                cached_pages: &mut cache,
                index_stack: &mut stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
            .unwrap();
        let mut pages = [PrivatePageCoordinatorTerminalPage::empty()];
        let (export, allocations) = count_thread_allocations(|| {
            finalized
                .output
                .prepare_terminal_export(finalized.successor, &mut pages)
        });
        assert_eq!(allocations, 0);
        let export = match export {
            Ok(export) => export,
            Err((_output, _successor, _pages, error)) => panic!("{error:?}"),
        };
        assert_eq!(export.root(), 5);
        assert_eq!(export.pages().len(), 1);
        assert_eq!(export.pages()[0].pool_slot, usize::MAX);
        assert_eq!(export.pages()[0].owner, PrivatePageOwner::Bitmap);
        assert_eq!(export.pages()[0].pgno, 5);
        assert!(page::verify_crc32c(&export.pages()[0].bytes));
    }

    #[test]
    fn finalization_retains_retirement_pages_in_the_same_work_unit_scope() {
        let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 1, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 3];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(plan.required_private_pages()).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let mut bound = attachment.bind(proof).unwrap();

        let retirement_pgno = bound.cow.candidates()[0];
        let checkpoint = pool.begin_checkpoint().unwrap();
        let retirement = pool
            .claim_page_in_scope(
                &checkpoint,
                &scope,
                retirement_pgno,
                PrivatePageOwner::Retirement,
                7,
                1,
            )
            .unwrap();
        pool.borrow_page_mut_in_scope(&scope, &retirement).unwrap()[77] = 0x5a;
        pool.commit_checkpoint_in_scope(checkpoint, &scope).unwrap();
        bound.cow.synchronize_scoped_bindings(&scope).unwrap();
        bound.cow.apply_planned_reservation().unwrap();

        let required = bound.finalization_scratch_requirements().unwrap();
        let mut release = vec![0; required.release_pages];
        let mut insert: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cache = vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let result = bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release,
                insert_pages: &mut insert,
                cached_pages: &mut cache,
                index_stack: &mut stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
            .unwrap();
        let record = match result
            .output
            .into_coordinator_record(result.successor, 101, 0)
        {
            Ok(record) => record,
            Err((_output, _successor, error)) => panic!("{error:?}"),
        };
        let mut source_entries = [const { None }; 3];
        let mut slot_to_entry = [usize::MAX; 3];
        let draft = crate::writer_fixed_point::FixedPointDraftSource::new(
            &source,
            &pool,
            &mut source_entries,
            &mut slot_to_entry,
        )
        .unwrap();
        let mut records = [None];
        let mut slot_to_record = [usize::MAX; 3];
        let mut ledger = crate::writer_fixed_point::FixedPointSealedLedger::new(
            &mut records,
            &mut slot_to_record,
            3,
        )
        .unwrap();
        ledger.push(record, &draft).unwrap();

        let mut prior_returns = [None; 1];
        let adapter =
            crate::retirement_writer::FixedPointRetirementSource::new(&draft, &mut prior_returns);
        {
            let mut bytes = [0; PAGE_SIZE];
            let residence = adapter
                .read_non_current_page(
                    retirement_pgno,
                    crate::retirement_writer::FixedPointRetirementPageKind::Tree,
                    &mut bytes,
                )
                .unwrap();
            let crate::retirement_writer::FixedPointRetirementResidence::PriorPrivate(location) =
                residence
            else {
                panic!("retirement page was misclassified as selected committed")
            };
            let crate::writer_fixed_point::DraftPageProvenance::Private { page, .. } =
                location.provenance
            else {
                unreachable!()
            };
            assert_eq!(page.owner, PrivatePageOwner::Retirement);
            assert_eq!(page.tag, 1);
            assert_eq!(bytes[77], 0x5a);
            assert!(matches!(
                adapter.read_non_current_page(
                    retirement_pgno,
                    crate::retirement_writer::FixedPointRetirementPageKind::Blob,
                    &mut bytes,
                ),
                Err(crate::retirement_writer::RetirementWriteError::PrivatePageOriginMismatch {
                    pgno,
                    ..
                }) if pgno == retirement_pgno
            ));

            let mut committed_bytes = [0; PAGE_SIZE];
            assert_eq!(
                adapter
                    .read_non_current_page(
                        11,
                        crate::retirement_writer::FixedPointRetirementPageKind::Tree,
                        &mut committed_bytes,
                    )
                    .unwrap(),
                crate::retirement_writer::FixedPointRetirementResidence::SelectedCommitted
            );
            crate::retirement_writer::RetirementMetadataSource::stage_prior_release(
                &adapter, 0, location,
            )
            .unwrap();
            crate::retirement_writer::RetirementMetadataSource::validate_prior_release_commit(
                &adapter, 0, 1,
            )
            .unwrap();
            crate::retirement_writer::RetirementMetadataSource::commit_prior_releases_prepared(
                &adapter, 0, 1,
            );
            assert_eq!(adapter.prior_returns().as_ref(), &[Some(location)]);
        };
        let (returned, allocations) =
            count_thread_allocations(|| adapter.return_staged_prior_pages(&mut ledger));
        assert_eq!(returned.unwrap(), 1);
        assert_eq!(allocations, 0);
        assert!(adapter.prior_returns().is_empty());
        assert_eq!(draft.private_location(retirement_pgno).unwrap(), None);
        ledger.into_records()[0].take().unwrap().cleanup().unwrap();
    }

    #[test]
    fn retirement_editor_rewrites_and_returns_a_prior_sealed_leaf() {
        use crate::page::PAGE_HEADER_SIZE;
        use crate::retirement_writer::{
            BlobBuildScratch, CommittedPageOrigin, CommittedPageReplacement,
            CommittedReplacementLedger, FixedPointRetirementSource, PageRoleIndex,
            PageRoleIndexSlot, PrivatePageArena, PrivateReleaseBuffer, RetirementBlobBuilder,
            RetirementMetadataSource, RetirementPathFrame, RetirementTreeEditor,
            RetirementTreeState,
        };
        use crate::writer_fixed_point::{FixedPointDraftSource, FixedPointSealedLedger};

        let source = SparsePages::new([leaf(11, 7, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 7, 20, 11, 1, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 5];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 8).unwrap();
        let first_scope = pool.reserve_scope(plan.required_private_pages()).unwrap();
        let (attachment, request) = plan.attach(&pool, &first_scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let mut bound = attachment.bind(proof).unwrap();
        let retirement_pgno = bound.cow.candidates()[0];
        let checkpoint = pool.begin_checkpoint().unwrap();
        let retirement = pool
            .claim_page_in_scope(
                &checkpoint,
                &first_scope,
                retirement_pgno,
                PrivatePageOwner::Retirement,
                8,
                1,
            )
            .unwrap();
        {
            let mut bytes = pool
                .borrow_page_mut_in_scope(&first_scope, &retirement)
                .unwrap();
            bytes.fill(0);
            PageHeader {
                page_type: PageType::RetirementLeaf,
                born_txn: 8,
                item_count: 1,
                level: 0,
                lower: PAGE_HEADER_SIZE + 32,
                upper: PAGE_SIZE as u16,
                aux: 0,
                page_crc32c: 0,
            }
            .encode_into(&mut bytes);
            let at = usize::from(PAGE_HEADER_SIZE);
            bytes[at + 8..at + 16].copy_from_slice(&3u64.to_le_bytes());
            bytes[at + 16..at + 24].copy_from_slice(&1u64.to_le_bytes());
            bytes[at + 24..at + 28].copy_from_slice(&12u32.to_le_bytes());
            page::write_crc32c(&mut bytes);
        }
        pool.commit_checkpoint_in_scope(checkpoint, &first_scope)
            .unwrap();
        bound.cow.synchronize_scoped_bindings(&first_scope).unwrap();
        bound.cow.apply_planned_reservation().unwrap();
        let required = bound.finalization_scratch_requirements().unwrap();
        let mut release = vec![0; required.release_pages];
        let mut insert: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cache = vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let finalized = bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release,
                insert_pages: &mut insert,
                cached_pages: &mut cache,
                index_stack: &mut stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
            .unwrap();
        let record = match finalized
            .output
            .into_coordinator_record(finalized.successor, 101, 0)
        {
            Ok(record) => record,
            Err((_output, _successor, error)) => panic!("{error:?}"),
        };
        let mut source_entries = [const { None }; 5];
        let mut slot_to_entry = [usize::MAX; 5];
        let draft =
            FixedPointDraftSource::new(&source, &pool, &mut source_entries, &mut slot_to_entry)
                .unwrap();
        let mut records = [None];
        let mut slot_to_record = [usize::MAX; 5];
        let mut ledger =
            FixedPointSealedLedger::new(&mut records, &mut slot_to_record, pool.len()).unwrap();
        ledger.push(record, &draft).unwrap();
        assert!(draft.private_location(retirement_pgno).unwrap().is_some());

        let second_scope = pool.reserve_scope(2).unwrap();
        let second_tail = u32::try_from(pool.pending_page_count()).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &second_scope,
            second_tail,
            PrivatePageAuthorization::Appended,
        )
        .unwrap();
        pool.bind_page(
            &checkpoint,
            &second_scope,
            second_tail + 1,
            PrivatePageAuthorization::Appended,
        )
        .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let mut arena = PrivatePageArena::from_scoped_pool(&pool, &second_scope, 8).unwrap();
        let mut blob_pages = [0u32; 1];
        let mut blob = RetirementBlobBuilder::build(
            &[14],
            &mut arena,
            &mut BlobBuildScratch::new(&mut blob_pages),
        )
        .unwrap();
        let mut prior_returns = [None; 1];
        let adapter = FixedPointRetirementSource::new(&draft, &mut prior_returns);
        let mut prior_bytes = [0; PAGE_SIZE];
        adapter
            .read_non_current_page(
                retirement_pgno,
                crate::retirement_writer::FixedPointRetirementPageKind::Tree,
                &mut prior_bytes,
            )
            .unwrap();
        let mut path = [RetirementPathFrame::new(); 2];
        let mut replacement_entries = [CommittedPageReplacement {
            pgno: 0,
            origin: CommittedPageOrigin::RetirementTree,
        }; 2];
        let mut replacements = CommittedReplacementLedger::new(&mut replacement_entries);
        let mut release_entries = [0u32; 2];
        let mut releases = PrivateReleaseBuffer::new(&mut release_entries);
        let mut role_entries = [PageRoleIndexSlot::new(); 16];
        let mut roles = PageRoleIndex::new(&mut role_entries);
        let plan = RetirementTreeEditor::plan_upsert_newest(
            &adapter,
            RetirementTreeState {
                selected_txn: 7,
                page_count: 20,
                root: retirement_pgno,
                batch_count: 1,
            },
            &mut blob,
            &mut path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .unwrap();
        let result = plan.apply().unwrap();
        assert_eq!(result.batch_count, 2);
        assert_eq!(result.prior_private_replacements, 1);
        assert_eq!(RetirementMetadataSource::prior_release_len(&adapter), 1);
        let (returned, allocations) =
            count_thread_allocations(|| adapter.return_staged_prior_pages(&mut ledger));
        assert_eq!(returned.unwrap(), 1);
        assert_eq!(allocations, 0);
        assert_eq!(draft.private_location(retirement_pgno).unwrap(), None);

        assert_eq!(pool.scoped_in_use(&second_scope).unwrap(), 2);
        ledger.into_records()[0].take().unwrap().cleanup().unwrap();
    }

    #[test]
    fn selective_finalization_seals_reads_replays_once_and_cleans_exact_scope() {
        let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let bound = attachment.bind(proof).unwrap();
        let required = bound.finalization_scratch_requirements().unwrap();
        let mut release_pages = vec![0; required.release_pages];
        let mut insert_pages: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cached_pages =
            vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut index_stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];

        let (result, finalization_allocations) = count_thread_allocations(|| {
            bound.finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release_pages,
                insert_pages: &mut insert_pages,
                cached_pages: &mut cached_pages,
                index_stack: &mut index_stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
        });
        let result = result.unwrap();
        assert_eq!(finalization_allocations, 0);
        assert_eq!(
            pool.scope_status(&scope),
            Err(PrivatePagePoolError::StaleScope)
        );
        assert!(result.output.root() >= 2);
        assert_eq!(
            result.output.pending_page_count(),
            pool.pending_page_count()
        );
        let mut bytes = [0; PAGE_SIZE];
        result
            .output
            .read_page(result.output.root(), &mut bytes)
            .unwrap();
        let duplicate = result.successor.test_duplicate();
        let predecessor = result.successor.consume().unwrap();
        assert!(matches!(
            duplicate.consume(),
            Err((_, FreeBitmapCowError::StaleReservationPredecessor))
        ));
        let (cleanup, cleanup_allocations) =
            count_thread_allocations(|| result.output.cleanup(predecessor));
        assert!(cleanup.is_ok());
        assert_eq!(cleanup_allocations, 0);
        assert_eq!(
            pool.scope_status(&scope),
            Err(PrivatePagePoolError::StaleScope)
        );
    }

    #[test]
    fn selective_finalization_rejects_presealed_scope_atomically_and_retries_same_authority() {
        let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let bound = attachment.bind(proof).unwrap();
        let required = bound.finalization_scratch_requirements().unwrap();
        pool.test_set_scope_sealed(&scope, true);
        let before = pool.test_mutation_snapshot();

        let mut release_pages = vec![0; required.release_pages];
        let mut insert_pages: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cached_pages =
            vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut index_stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let (bound, error) = match bound.finalize(FreeBitmapFinalizationScratch {
            release_pages: &mut release_pages,
            insert_pages: &mut insert_pages,
            cached_pages: &mut cached_pages,
            index_stack: &mut index_stack,
            cleanup_nodes: &mut cleanup_nodes,
            cleanup_path: &mut cleanup_path,
            cleanup_targets: &mut cleanup_targets,
        }) {
            Ok(_) => panic!("presealed scope unexpectedly finalized"),
            Err(failure) => failure,
        };
        assert!(matches!(error, FreeBitmapCowError::ArenaPageConflict(_)));
        assert_eq!(pool.test_mutation_snapshot(), before);

        pool.test_set_scope_sealed(&scope, false);
        let mut release_pages = vec![0; required.release_pages];
        let mut insert_pages: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cached_pages =
            vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut index_stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let result = bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release_pages,
                insert_pages: &mut insert_pages,
                cached_pages: &mut cached_pages,
                index_stack: &mut index_stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
            .unwrap();
        let predecessor = result.successor.consume().unwrap();
        assert!(result.output.cleanup(predecessor).is_ok());
    }

    #[test]
    fn selective_cleanup_failures_return_both_authorities_for_repair_and_retry() {
        let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let bound = attachment.bind(proof).unwrap();
        let required = bound.finalization_scratch_requirements().unwrap();
        let mut release_pages = vec![0; required.release_pages];
        let mut insert_pages: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cached_pages =
            vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut index_stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let result = bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release_pages,
                insert_pages: &mut insert_pages,
                cached_pages: &mut cached_pages,
                index_stack: &mut index_stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
            .unwrap();
        let predecessor = result.successor.consume().unwrap();
        let wrong = predecessor.test_wrong_nonce();
        let before_wrong = result.output.test_exact_commitment().unwrap();
        let (output, _wrong, error) = match result.output.cleanup(wrong) {
            Ok(()) => panic!("wrong predecessor unexpectedly cleaned the scope"),
            Err(failure) => failure,
        };
        assert_eq!(error, FreeBitmapCowError::StaleReservationPredecessor);
        assert_eq!(output.test_exact_commitment().unwrap(), before_wrong);
        let (slot, original) = output.test_corrupt_cleanup_bound_validation_marker();
        let before_marker = output.test_pool_mutation_snapshot();
        let (mut output, predecessor, error) = match output.cleanup(predecessor) {
            Ok(()) => panic!("corrupt bound validation marker unexpectedly cleaned the scope"),
            Err(failure) => failure,
        };
        assert!(matches!(error, FreeBitmapCowError::ArenaPageConflict(_)));
        assert_eq!(output.test_pool_mutation_snapshot(), before_marker);
        output.test_restore_cleanup_slot(slot, original);
        output.test_poison_cleanup_scratch();
        let before_poisoned = output.test_exact_commitment().unwrap();
        let (mut output, predecessor, error) = match output.cleanup(predecessor) {
            Ok(()) => panic!("poisoned cleanup scratch unexpectedly succeeded"),
            Err(failure) => failure,
        };
        assert_eq!(error, FreeBitmapCowError::ArenaPageConflict(0));
        assert_eq!(output.test_exact_commitment().unwrap(), before_poisoned);
        output.test_repair_cleanup_scratch();
        assert!(output.cleanup(predecessor).is_ok());
        assert_eq!(
            pool.scope_status(&scope),
            Err(PrivatePagePoolError::StaleScope)
        );
    }

    #[test]
    fn selective_successor_transient_failures_return_the_same_seed_for_retry() {
        #[derive(Clone, Copy, Debug)]
        enum Failure {
            ActiveCheckpoint,
            BorrowConflict,
        }

        for failure in [Failure::ActiveCheckpoint, Failure::BorrowConflict] {
            let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
            let mut storage = PlannerStorage::new(3, 4, 4, 8);
            storage.arena.clear();
            let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
                .unwrap()
                .plan_capacity()
                .unwrap();
            let mut slots = [
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
            ];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let scope = pool.reserve_scope(3).unwrap();
            let (attachment, request) = plan.attach(&pool, &scope).unwrap();
            let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
            let bound = attachment.bind(proof).unwrap();
            let required = bound.finalization_scratch_requirements().unwrap();
            let mut release_pages = vec![0; required.release_pages];
            let mut insert_pages: Vec<_> = (0..required.insert_pages)
                .map(|_| FreeBitmapInsertPage::empty())
                .collect();
            let mut cached_pages =
                vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
            let mut index_stack = vec![usize::MAX; required.index_stack];
            let mut cleanup_nodes = vec![
                crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
                required.cleanup_nodes
            ];
            let mut cleanup_path = vec![
                crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
                required.cleanup_path
            ];
            let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
            let result = bound
                .finalize(FreeBitmapFinalizationScratch {
                    release_pages: &mut release_pages,
                    insert_pages: &mut insert_pages,
                    cached_pages: &mut cached_pages,
                    index_stack: &mut index_stack,
                    cleanup_nodes: &mut cleanup_nodes,
                    cleanup_path: &mut cleanup_path,
                    cleanup_targets: &mut cleanup_targets,
                })
                .unwrap();
            let commitment = result.output.test_exact_commitment().unwrap();

            let seed = match failure {
                Failure::ActiveCheckpoint => {
                    let checkpoint = pool.begin_checkpoint().unwrap();
                    let active_commitment = result.output.test_exact_commitment().unwrap();
                    let before = pool.test_mutation_snapshot();
                    let (seed, error) = match result.successor.consume() {
                        Ok(_) => panic!("active checkpoint unexpectedly consumed successor"),
                        Err(failure) => failure,
                    };
                    assert_eq!(
                        error,
                        FreeBitmapCowError::PrivatePool(PrivatePagePoolError::CheckpointActive)
                    );
                    assert_eq!(pool.test_mutation_snapshot(), before);
                    assert_eq!(
                        result.output.test_exact_commitment().unwrap(),
                        active_commitment
                    );
                    pool.rollback_checkpoint(checkpoint).unwrap();
                    seed
                }
                Failure::BorrowConflict => {
                    let held = pool.test_hold_slots_borrow();
                    let (seed, error) = match result.successor.consume() {
                        Ok(_) => panic!("held pool borrow unexpectedly consumed successor"),
                        Err(failure) => failure,
                    };
                    assert_eq!(
                        error,
                        FreeBitmapCowError::PrivatePool(PrivatePagePoolError::BorrowConflict)
                    );
                    drop(held);
                    assert_eq!(result.output.test_exact_commitment().unwrap(), commitment);
                    seed
                }
            };

            let duplicate = seed.test_duplicate();
            let predecessor = seed.consume().unwrap();
            let (_duplicate, error) = match duplicate.consume() {
                Ok(_) => panic!("duplicate successor unexpectedly consumed twice"),
                Err(failure) => failure,
            };
            assert_eq!(error, FreeBitmapCowError::StaleReservationPredecessor);
            assert!(result.output.cleanup(predecessor).is_ok());
            assert_eq!(
                pool.scope_status(&scope),
                Err(PrivatePagePoolError::StaleScope),
                "{failure:?}"
            );
        }
    }

    #[test]
    fn selective_cleanup_rejects_corrupt_destination_boundary_and_retries_same_authorities() {
        #[derive(Clone, Copy, Debug)]
        enum Corruption {
            EmptyCountWithLinks,
            CountExceedsCloseCapacity,
            MissingHead,
            HeadOutOfBounds,
            TailOutOfBounds,
            HeadPrevious,
            TailNext,
            HeadNeighborBacklink,
            TailNeighborForwardLink,
        }

        for corruption in [
            Corruption::EmptyCountWithLinks,
            Corruption::CountExceedsCloseCapacity,
            Corruption::MissingHead,
            Corruption::HeadOutOfBounds,
            Corruption::TailOutOfBounds,
            Corruption::HeadPrevious,
            Corruption::TailNext,
            Corruption::HeadNeighborBacklink,
            Corruption::TailNeighborForwardLink,
        ] {
            let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
            let mut storage = PlannerStorage::new(3, 4, 4, 8);
            storage.arena.clear();
            let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
                .unwrap()
                .plan_capacity()
                .unwrap();
            let mut slots = [
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
            ];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let scope = pool.reserve_scope(3).unwrap();
            let (attachment, request) = plan.attach(&pool, &scope).unwrap();
            let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
            let bound = attachment.bind(proof).unwrap();
            let required = bound.finalization_scratch_requirements().unwrap();
            let mut release_pages = vec![0; required.release_pages];
            let mut insert_pages: Vec<_> = (0..required.insert_pages)
                .map(|_| FreeBitmapInsertPage::empty())
                .collect();
            let mut cached_pages =
                vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
            let mut index_stack = vec![usize::MAX; required.index_stack];
            let mut cleanup_nodes = vec![
                crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
                required.cleanup_nodes
            ];
            let mut cleanup_path = vec![
                crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
                required.cleanup_path
            ];
            let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
            let result = bound
                .finalize(FreeBitmapFinalizationScratch {
                    release_pages: &mut release_pages,
                    insert_pages: &mut insert_pages,
                    cached_pages: &mut cached_pages,
                    index_stack: &mut index_stack,
                    cleanup_nodes: &mut cleanup_nodes,
                    cleanup_path: &mut cleanup_path,
                    cleanup_targets: &mut cleanup_targets,
                })
                .unwrap();
            let predecessor = result.successor.consume().unwrap();

            match corruption {
                Corruption::EmptyCountWithLinks => pool.test_set_unscoped_vacant_count(0),
                Corruption::CountExceedsCloseCapacity => {
                    pool.test_set_unscoped_vacant_count(3);
                }
                Corruption::MissingHead => pool.test_set_unscoped_vacant_head(usize::MAX),
                Corruption::HeadOutOfBounds => pool.test_set_unscoped_vacant_head(5),
                Corruption::TailOutOfBounds => pool.test_set_unscoped_vacant_tail(5),
                Corruption::HeadPrevious => {
                    pool.test_set_unscoped_vacancy_links(3, 4, 4);
                }
                Corruption::TailNext => {
                    pool.test_set_unscoped_vacancy_links(4, 3, 3);
                }
                Corruption::HeadNeighborBacklink => {
                    pool.test_set_unscoped_vacancy_links(4, usize::MAX, usize::MAX);
                }
                Corruption::TailNeighborForwardLink => {
                    pool.test_set_unscoped_vacancy_links(3, usize::MAX, usize::MAX);
                }
            }
            let before = result.output.test_pool_mutation_snapshot();
            let (output, predecessor, error) = match result.output.cleanup(predecessor) {
                Ok(()) => panic!("{corruption:?} unexpectedly cleaned the scope"),
                Err(failure) => failure,
            };
            assert_eq!(
                error,
                FreeBitmapCowError::PrivatePool(PrivatePagePoolError::StaleScope),
                "{corruption:?}"
            );
            assert_eq!(
                output.test_pool_mutation_snapshot(),
                before,
                "{corruption:?}"
            );

            pool.test_set_unscoped_vacant_count(2);
            pool.test_set_unscoped_vacant_head(3);
            pool.test_set_unscoped_vacant_tail(4);
            pool.test_set_unscoped_vacancy_links(3, usize::MAX, 4);
            pool.test_set_unscoped_vacancy_links(4, 3, usize::MAX);
            assert!(output.cleanup(predecessor).is_ok(), "{corruption:?}");
            assert_eq!(
                pool.scope_status(&scope),
                Err(PrivatePagePoolError::StaleScope),
                "{corruption:?}"
            );
        }
    }

    #[test]
    fn selective_cleanup_rejects_every_noncanonical_vacant_payload_and_retries() {
        use crate::private_page_pool::PrivatePageVacantPayloadCorruption as Corruption;

        for corruption in [
            Corruption::PageNumber,
            Corruption::Authorization,
            Corruption::State,
            Corruption::AllocationGeneration,
            Corruption::CheckpointGeneration,
            Corruption::SavedState,
            Corruption::AdapterOwner,
            Corruption::AdapterTag,
            Corruption::Bytes,
            Corruption::IndexLeft,
            Corruption::IndexRight,
            Corruption::IndexHeight,
            Corruption::IndexAvailable,
            Corruption::IndexInUse,
            Corruption::IndexUnscopedAvailable,
            Corruption::ScopeLeft,
            Corruption::ScopeRight,
            Corruption::ScopeHeight,
            Corruption::ScopeAvailable,
            Corruption::ScopeInUse,
            Corruption::ValidationMarker,
            Corruption::SavedBinding,
            Corruption::SavedIndexGeneration,
            Corruption::SavedIndexNext,
            Corruption::SavedScopeGeneration,
        ] {
            let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
            let mut storage = PlannerStorage::new(3, 4, 4, 8);
            storage.arena.clear();
            let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
                .unwrap()
                .plan_capacity()
                .unwrap();
            let mut slots = [
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
            ];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let scope = pool.reserve_scope(3).unwrap();
            let (attachment, request) = plan.attach(&pool, &scope).unwrap();
            let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
            let bound = attachment.bind(proof).unwrap();
            let required = bound.finalization_scratch_requirements().unwrap();
            let mut release_pages = vec![0; required.release_pages];
            let mut insert_pages: Vec<_> = (0..required.insert_pages)
                .map(|_| FreeBitmapInsertPage::empty())
                .collect();
            let mut cached_pages =
                vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
            let mut index_stack = vec![usize::MAX; required.index_stack];
            let mut cleanup_nodes = vec![
                crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
                required.cleanup_nodes
            ];
            let mut cleanup_path = vec![
                crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
                required.cleanup_path
            ];
            let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
            let result = bound
                .finalize(FreeBitmapFinalizationScratch {
                    release_pages: &mut release_pages,
                    insert_pages: &mut insert_pages,
                    cached_pages: &mut cached_pages,
                    index_stack: &mut index_stack,
                    cleanup_nodes: &mut cleanup_nodes,
                    cleanup_path: &mut cleanup_path,
                    cleanup_targets: &mut cleanup_targets,
                })
                .unwrap();
            let predecessor = result.successor.consume().unwrap();
            let (slot, original) = result
                .output
                .test_corrupt_cleanup_vacant_payload(corruption);
            let before = result.output.test_pool_mutation_snapshot();
            let (output, predecessor, error) = match result.output.cleanup(predecessor) {
                Ok(()) => panic!("{corruption:?} unexpectedly cleaned the scope"),
                Err(failure) => failure,
            };
            assert!(
                matches!(error, FreeBitmapCowError::ArenaPageConflict(_)),
                "{corruption:?}: {error:?}"
            );
            assert_eq!(
                output.test_pool_mutation_snapshot(),
                before,
                "{corruption:?}"
            );
            output.test_restore_cleanup_slot(slot, original);
            assert!(output.cleanup(predecessor).is_ok(), "{corruption:?}");
            assert_eq!(
                pool.scope_status(&scope),
                Err(PrivatePagePoolError::StaleScope),
                "{corruption:?}"
            );
        }
    }

    #[test]
    fn selective_cleanup_never_traverses_an_unrelated_lowest_index_path_after_checkpoint() {
        let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [
            PrivatePagePoolSlot::authorized(12, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(13, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(14, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(15, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(16, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        for tag in 0..5 {
            let _authority = pool
                .claim_lowest(PrivatePageOwner::Retirement, 2, tag)
                .unwrap();
        }
        let scope = pool.reserve_scope(3).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let bound = attachment.bind(proof).unwrap();
        let required = bound.finalization_scratch_requirements().unwrap();
        let mut release_pages = vec![0; required.release_pages];
        let mut insert_pages: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cached_pages =
            vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut index_stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let result = bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release_pages,
                insert_pages: &mut insert_pages,
                cached_pages: &mut cached_pages,
                index_stack: &mut index_stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
            .unwrap();
        let predecessor = result.successor.consume().unwrap();
        pool.test_corrupt_unrelated_unscoped_search_child();
        assert!(result.output.cleanup(predecessor).is_ok());
        assert_eq!(
            pool.scope_status(&scope),
            Err(PrivatePagePoolError::StaleScope)
        );
    }

    #[test]
    fn selective_finalization_one_short_is_atomic_and_same_authority_retries() {
        let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let bound = attachment.bind(proof).unwrap();
        let required = bound.finalization_scratch_requirements().unwrap();
        assert!(required.release_pages > 0);
        let commitment = pool.exact_commitment(&scope).unwrap();
        let mut short_release = vec![0xfeed_beef; required.release_pages - 1];
        let before_release = short_release.clone();
        let mut insert_pages: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cached_pages =
            vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut index_stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];

        let (bound, error) = match bound.finalize(FreeBitmapFinalizationScratch {
            release_pages: &mut short_release,
            insert_pages: &mut insert_pages,
            cached_pages: &mut cached_pages,
            index_stack: &mut index_stack,
            cleanup_nodes: &mut cleanup_nodes,
            cleanup_path: &mut cleanup_path,
            cleanup_targets: &mut cleanup_targets,
        }) {
            Ok(_) => panic!("one-short finalization unexpectedly succeeded"),
            Err(failure) => failure,
        };
        assert_eq!(
            error,
            FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::CandidatePages,
                required: required.release_pages,
                available: required.release_pages - 1,
            }
        );
        assert_eq!(short_release, before_release);
        pool.validate_exact_commitment(&scope, &commitment).unwrap();

        let mut release_pages = vec![0; required.release_pages];
        let mut insert_pages: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cached_pages =
            vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut index_stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let result = bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release_pages,
                insert_pages: &mut insert_pages,
                cached_pages: &mut cached_pages,
                index_stack: &mut index_stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
            .unwrap();
        let predecessor = result.successor.consume().unwrap();
        assert!(result.output.cleanup(predecessor).is_ok());
    }

    #[test]
    fn selective_finalization_last_callback_failure_returns_same_authority() {
        let source = AccessControlledPages::new([leaf(11, 1, &[5, 9])]);

        let callback_count = {
            let mut storage = PlannerStorage::new(3, 4, 4, 8);
            storage.arena.clear();
            let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
                .unwrap()
                .plan_capacity()
                .unwrap();
            let mut slots = [
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
            ];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let scope = pool.reserve_scope(3).unwrap();
            let (attachment, request) = plan.attach(&pool, &scope).unwrap();
            let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
            let bound = attachment.bind(proof).unwrap();
            let required = bound.finalization_scratch_requirements().unwrap();
            let before = source.checks.get();
            let mut release_pages = vec![0; required.release_pages];
            let mut insert_pages: Vec<_> = (0..required.insert_pages)
                .map(|_| FreeBitmapInsertPage::empty())
                .collect();
            let mut cached_pages =
                vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
            let mut index_stack = vec![usize::MAX; required.index_stack];
            let mut cleanup_nodes = vec![
                crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
                required.cleanup_nodes
            ];
            let mut cleanup_path = vec![
                crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
                required.cleanup_path
            ];
            let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
            let result = bound
                .finalize(FreeBitmapFinalizationScratch {
                    release_pages: &mut release_pages,
                    insert_pages: &mut insert_pages,
                    cached_pages: &mut cached_pages,
                    index_stack: &mut index_stack,
                    cleanup_nodes: &mut cleanup_nodes,
                    cleanup_path: &mut cleanup_path,
                    cleanup_targets: &mut cleanup_targets,
                })
                .unwrap();
            let callbacks = source.checks.get() - before;
            let predecessor = result.successor.consume().unwrap();
            assert!(result.output.cleanup(predecessor).is_ok());
            callbacks
        };
        assert!(callback_count > 0);

        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let bound = attachment.bind(proof).unwrap();
        let commitment = pool.exact_commitment(&scope).unwrap();
        let required = bound.finalization_scratch_requirements().unwrap();
        let fail_at = source.checks.get() + callback_count;
        source.fail_on_check(fail_at);
        let mut release_pages = vec![0; required.release_pages];
        let mut insert_pages: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cached_pages =
            vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut index_stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let (bound, error) = match bound.finalize(FreeBitmapFinalizationScratch {
            release_pages: &mut release_pages,
            insert_pages: &mut insert_pages,
            cached_pages: &mut cached_pages,
            index_stack: &mut index_stack,
            cleanup_nodes: &mut cleanup_nodes,
            cleanup_path: &mut cleanup_path,
            cleanup_targets: &mut cleanup_targets,
        }) {
            Ok(_) => panic!("last-callback failure unexpectedly finalized"),
            Err(failure) => failure,
        };
        assert_eq!(
            error,
            FreeBitmapCowError::Source(PageSourceError::ForkedHandle)
        );
        assert_eq!(source.checks.get(), fail_at);
        pool.validate_exact_commitment(&scope, &commitment).unwrap();

        source.fail_on_check(usize::MAX);
        let mut release_pages = vec![0; required.release_pages];
        let mut insert_pages: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cached_pages =
            vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut index_stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let result = bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release_pages,
                insert_pages: &mut insert_pages,
                cached_pages: &mut cached_pages,
                index_stack: &mut index_stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
            .unwrap();
        let predecessor = result.successor.consume().unwrap();
        assert!(result.output.cleanup(predecessor).is_ok());
    }

    #[test]
    fn selective_finalization_last_callback_drift_precedes_source_error() {
        let callback_count = {
            let mut slots = [
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
            ];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let foreign = pool.reserve_scope(1).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.bind_page(
                &checkpoint,
                &foreign,
                17,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
            pool.commit_checkpoint(checkpoint).unwrap();
            let target = pool.reserve_scope(3).unwrap();
            let source = ForeignMutatingPages {
                pages: SparsePages::new([leaf(11, 1, &[5, 9])]),
                pool: &pool,
                scope: &foreign,
                pgno: 17,
                armed: Cell::new(false),
                fail_when_armed: false,
                checks: Cell::new(0),
                fire_on_check: Cell::new(None),
            };
            let mut storage = PlannerStorage::new(3, 4, 4, 8);
            storage.arena.clear();
            let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
                .unwrap()
                .plan_capacity()
                .unwrap();
            let (attachment, request) = plan.attach(&pool, &target).unwrap();
            let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
            let bound = attachment.bind(proof).unwrap();
            let required = bound.finalization_scratch_requirements().unwrap();
            let before = source.checks.get();
            let mut release_pages = vec![0; required.release_pages];
            let mut insert_pages: Vec<_> = (0..required.insert_pages)
                .map(|_| FreeBitmapInsertPage::empty())
                .collect();
            let mut cached_pages =
                vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
            let mut index_stack = vec![usize::MAX; required.index_stack];
            let mut cleanup_nodes = vec![
                crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
                required.cleanup_nodes
            ];
            let mut cleanup_path = vec![
                crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
                required.cleanup_path
            ];
            let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
            let result = bound
                .finalize(FreeBitmapFinalizationScratch {
                    release_pages: &mut release_pages,
                    insert_pages: &mut insert_pages,
                    cached_pages: &mut cached_pages,
                    index_stack: &mut index_stack,
                    cleanup_nodes: &mut cleanup_nodes,
                    cleanup_path: &mut cleanup_path,
                    cleanup_targets: &mut cleanup_targets,
                })
                .unwrap();
            let callbacks = source.checks.get() - before;
            let predecessor = result.successor.consume().unwrap();
            assert!(result.output.cleanup(predecessor).is_ok());
            callbacks
        };
        assert!(callback_count > 0);

        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let foreign = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let foreign_slot = pool
            .bind_page(
                &checkpoint,
                &foreign,
                17,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let target = pool.reserve_scope(3).unwrap();
        let source = ForeignMutatingPages {
            pages: SparsePages::new([leaf(11, 1, &[5, 9])]),
            pool: &pool,
            scope: &foreign,
            pgno: 17,
            armed: Cell::new(false),
            fail_when_armed: true,
            checks: Cell::new(0),
            fire_on_check: Cell::new(None),
        };
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let (attachment, request) = plan.attach(&pool, &target).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let bound = attachment.bind(proof).unwrap();
        let required = bound.finalization_scratch_requirements().unwrap();
        let target_before: Vec<_> = bound.cow.arena_bindings[..3]
            .iter()
            .map(|binding| {
                pool.scoped_slot_info(&target, binding.pool_slot)
                    .unwrap()
                    .unwrap()
            })
            .collect();
        let fail_at = source.checks.get() + callback_count;
        source.fire_on_check.set(Some(fail_at));
        let mut release_pages = vec![0; required.release_pages];
        let mut insert_pages: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cached_pages =
            vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut index_stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let (bound, error) = match bound.finalize(FreeBitmapFinalizationScratch {
            release_pages: &mut release_pages,
            insert_pages: &mut insert_pages,
            cached_pages: &mut cached_pages,
            index_stack: &mut index_stack,
            cleanup_nodes: &mut cleanup_nodes,
            cleanup_path: &mut cleanup_path,
            cleanup_targets: &mut cleanup_targets,
        }) {
            Ok(_) => panic!("drifting last callback unexpectedly finalized"),
            Err(failure) => failure,
        };
        assert_eq!(error, FreeBitmapCowError::StaleInsertionPlan);
        assert_eq!(source.checks.get(), fail_at);
        assert!(matches!(
            pool.scoped_slot_info(&foreign, foreign_slot)
                .unwrap()
                .unwrap()
                .state,
            PrivatePagePoolState::InUse {
                owner: PrivatePageOwner::Retirement,
                ..
            }
        ));
        let target_after: Vec<_> = bound.cow.arena_bindings[..3]
            .iter()
            .map(|binding| {
                pool.scoped_slot_info(&target, binding.pool_slot)
                    .unwrap()
                    .unwrap()
            })
            .collect();
        assert_eq!(target_after, target_before);

        source.fire_on_check.set(None);
        let mut release_pages = vec![0; required.release_pages];
        let mut insert_pages: Vec<_> = (0..required.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cached_pages =
            vec![FreeBitmapFinalizationCachedPage::empty(); required.cached_pages];
        let mut index_stack = vec![usize::MAX; required.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            required.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            required.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; required.cleanup_targets];
        let result = bound
            .finalize(FreeBitmapFinalizationScratch {
                release_pages: &mut release_pages,
                insert_pages: &mut insert_pages,
                cached_pages: &mut cached_pages,
                index_stack: &mut index_stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            })
            .unwrap();
        let predecessor = result.successor.consume().unwrap();
        assert!(result.output.cleanup(predecessor).is_ok());
    }

    #[test]
    fn late_binding_covers_zero_committed_reclaimed_only_and_append_only() {
        fn bind_pages<const N: usize>(
            source: &SparsePages<N>,
            root: u32,
            payload: usize,
            reclaimed: &[u32],
            selection_id: u64,
        ) -> (FreeBitmapReservationBinding, Vec<u32>) {
            let mut storage = PlannerStorage::new(4, 4, 4, 12);
            storage.arena.clear();
            let plan =
                FreeBitmapReservationPlanner::new(source, 1, 20, root, payload, storage.buffers())
                    .unwrap()
                    .plan_capacity()
                    .unwrap();
            let private_pages = plan.required_private_pages();
            let mut slots = vec![PrivatePagePoolSlot::empty(); private_pages];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let scope = pool.reserve_scope(private_pages).unwrap();
            let (attachment, request) = plan.attach(&pool, &scope).unwrap();
            let proof = complete_free_bitmap_reclamation_for_test(request, selection_id, reclaimed)
                .unwrap();
            let bound = attachment.bind(proof).unwrap();
            let pages = bound.cow.arena_bindings[..private_pages]
                .iter()
                .map(|binding| {
                    pool.scoped_slot_info(&scope, binding.pool_slot)
                        .unwrap()
                        .unwrap()
                        .pgno
                })
                .collect();
            (bound.binding, pages)
        }

        let source = SparsePages::new([leaf(11, 1, &[9, 10])]);
        assert_eq!(
            bind_pages(&source, 11, 2, &[3, 4, 7], 81),
            (
                FreeBitmapReservationBinding {
                    committed: 0,
                    reclaimed: 3,
                    appended: 0,
                },
                vec![3, 4, 7],
            )
        );
        let empty = SparsePages::new([]);
        assert_eq!(
            bind_pages(&empty, 0, 3, &[3, 7, 9], 82),
            (
                FreeBitmapReservationBinding {
                    committed: 0,
                    reclaimed: 3,
                    appended: 0,
                },
                vec![3, 7, 9],
            )
        );
        assert_eq!(
            bind_pages(&empty, 0, 3, &[], 0),
            (
                FreeBitmapReservationBinding {
                    committed: 0,
                    reclaimed: 0,
                    appended: 3,
                },
                vec![20, 21, 22],
            )
        );
    }

    #[test]
    fn late_binding_rejects_invalid_reclaimed_sets_before_pool_mutation() {
        let cases: &[&[u32]] = &[&[3, 3], &[7, 3], &[1], &[20], &[5], &[11]];
        for reclaimed in cases {
            let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
            let mut storage = PlannerStorage::new(3, 4, 4, 8);
            storage.arena.clear();
            let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
                .unwrap()
                .plan_capacity()
                .unwrap();
            let mut slots = [
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
                PrivatePagePoolSlot::empty(),
            ];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let scope = pool.reserve_scope(3).unwrap();
            let (attachment, request) = plan.attach(&pool, &scope).unwrap();
            let proof = complete_free_bitmap_reclamation_for_test(request, 91, reclaimed).unwrap();
            assert!(attachment.bind(proof).is_err(), "accepted {reclaimed:?}");
            assert_eq!(pool.scope_status(&scope).unwrap().bound, 0);
            assert_eq!(pool.pending_page_count(), 20);
        }
    }

    #[test]
    fn late_binding_source_failure_is_terminal_and_pool_atomic() {
        let source = AccessControlledPages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let reclaimed = [3, 7];
        let proof = complete_free_bitmap_reclamation_for_test(request, 101, &reclaimed).unwrap();
        source.deny_access(PageSourceError::ForkedHandle);
        assert!(matches!(
            attachment.bind(proof),
            Err(FreeBitmapCowError::Source(PageSourceError::ForkedHandle))
        ));
        assert_eq!(source.checks.get(), 3);
        assert_eq!(pool.scope_status(&scope).unwrap().bound, 0);
        assert_eq!(pool.pending_page_count(), 20);
    }

    #[test]
    fn late_binding_source_node_shortage_is_typed_and_precedes_pool_mutation() {
        let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        storage.source_nodes.truncate(2);
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(3).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let reclaimed = [3, 7];
        let proof = complete_free_bitmap_reclamation_for_test(request, 61, &reclaimed).unwrap();
        assert!(matches!(
            attachment.bind(proof),
            Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::SourceNodes,
                required: 4,
                available: 2,
            })
        ));
        assert_eq!(pool.scope_status(&scope).unwrap().bound, 0);
        assert_eq!(pool.pending_page_count(), 20);
    }

    #[test]
    fn late_binding_epoch_headroom_failures_are_typed_and_pool_atomic() {
        fn run(pool_epoch: Option<u64>, slot_epoch: Option<u64>) {
            let source = SparsePages::new([]);
            let mut storage = PlannerStorage::new(2, 1, 1, 8);
            storage.arena.clear();
            let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 0, 2, storage.buffers())
                .unwrap()
                .plan_capacity()
                .unwrap();
            let mut slots = [PrivatePagePoolSlot::empty(), PrivatePagePoolSlot::empty()];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let scope = pool.reserve_scope(2).unwrap();
            if let Some(epoch) = pool_epoch {
                pool.test_set_epoch(epoch);
            }
            if let Some(epoch) = slot_epoch {
                pool.test_set_binding_epoch(0, epoch);
            }
            let (attachment, request) = plan.attach(&pool, &scope).unwrap();
            let reclaimed = [3, 7];
            let proof =
                complete_free_bitmap_reclamation_for_test(request, 111, &reclaimed).unwrap();
            assert!(matches!(
                attachment.bind(proof),
                Err(FreeBitmapCowError::MutationEpochExhausted)
            ));
            assert_eq!(pool.scope_status(&scope).unwrap().bound, 0);
            assert_eq!(pool.pending_page_count(), 20);
        }

        run(Some(u64::MAX - 1), None);
        run(None, Some(u64::MAX));
    }

    #[test]
    fn late_binding_attach_rejects_noninitial_tail_without_touching_foreign_scope() {
        let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();
        let mut slots = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let foreign = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let foreign_slot = pool
            .bind_page(
                &checkpoint,
                &foreign,
                20,
                PrivatePageAuthorization::Appended,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let foreign_before = pool.scoped_slot_info(&foreign, foreign_slot).unwrap();
        let target = pool.reserve_scope(3).unwrap();
        assert!(matches!(
            plan.attach(&pool, &target),
            Err(FreeBitmapCowError::StaleReservationPredecessor)
        ));
        assert_eq!(pool.pending_page_count(), 21);
        assert_eq!(pool.scope_status(&target).unwrap().bound, 0);
        assert_eq!(
            pool.scoped_slot_info(&foreign, foreign_slot).unwrap(),
            foreign_before
        );
    }

    #[test]
    fn late_binding_scope_lifecycle_visits_only_the_exact_embedded_scope() {
        fn run(pool_slots: usize) -> (usize, [usize; 3], usize) {
            let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
            let mut storage = PlannerStorage::new(3, 4, 4, 8);
            storage.arena.clear();
            let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
                .unwrap()
                .plan_capacity()
                .unwrap();

            let mut slots = vec![PrivatePagePoolSlot::empty(); pool_slots];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let before = pool.test_scope_lifecycle_visits();
            let (target_slots, allocations) = count_thread_allocations(|| {
                let target = pool.reserve_scope(3).unwrap();
                let target_slots = {
                    let (attachment, request) = plan.attach(&pool, &target).unwrap();
                    let target_slots = core::array::from_fn(|index| {
                        attachment.cow.arena_bindings[index].pool_slot
                    });
                    assert_eq!(request.ticket.state.load(Ordering::Acquire), 1);
                    target_slots
                };
                pool.close_scope(&target).unwrap();
                target_slots
            });
            let visits = pool.test_scope_lifecycle_visits() - before;
            assert_eq!(pool.test_unscoped_vacant_count(), pool_slots);
            (visits, target_slots, allocations)
        }

        let small = run(512);
        let large = run(4_096);
        assert_eq!(small, (36, [0, 1, 2], 0));
        assert_eq!(large, small);
    }

    #[test]
    fn late_binding_uses_reservation_ordinals_after_nonmonotonic_scope_reuse() {
        let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
        let mut storage = PlannerStorage::new(3, 4, 4, 8);
        storage.arena.clear();
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
            .unwrap()
            .plan_capacity()
            .unwrap();

        let mut slots = vec![PrivatePagePoolSlot::empty(); 6];
        let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
        let first = pool.reserve_scope(2).unwrap();
        let foreign = pool.reserve_scope(2).unwrap();
        let third = pool.reserve_scope(2).unwrap();
        pool.close_scope(&third).unwrap();
        pool.close_scope(&first).unwrap();

        let foreign_before = core::array::from_fn::<_, 2, _>(|index| {
            pool.scoped_slot_info(&foreign, index + 2).unwrap()
        });
        let target = pool.reserve_scope(3).unwrap();
        let mut target_order = [usize::MAX; 3];
        pool.visit_exact_scope_layout(&target, |ordinal, slot, info| {
            assert_eq!(info.member_ordinal, ordinal);
            target_order[ordinal] = slot;
        })
        .unwrap();
        assert_eq!(target_order, [4, 5, 0]);

        let (attachment, request) = plan.attach(&pool, &target).unwrap();
        let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
        let bound = attachment.bind(proof).unwrap();
        assert_eq!(
            bound.binding,
            FreeBitmapReservationBinding {
                committed: 2,
                reclaimed: 0,
                appended: 1,
            }
        );
        assert_eq!(
            core::array::from_fn::<_, 3, _>(|index| { bound.cow.arena_bindings[index].pool_slot }),
            [4, 5, 0]
        );
        assert_eq!(
            core::array::from_fn::<_, 2, _>(|index| {
                pool.scoped_slot_info(&foreign, index + 2).unwrap()
            }),
            foreign_before
        );
    }

    #[test]
    fn late_binding_rejects_every_corrupt_scope_member_link_before_request_issuance() {
        #[derive(Clone, Copy, Debug)]
        enum Corruption {
            WrongHead,
            EarlyEnd,
            Duplicate,
            Cycle,
            Overlong,
            WrongScope,
            WrongAnchor,
        }

        fn run(corruption: Corruption) {
            let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
            let mut storage = PlannerStorage::new(3, 4, 4, 8);
            storage.arena.clear();
            let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
                .unwrap()
                .plan_capacity()
                .unwrap();
            let mut slots = vec![PrivatePagePoolSlot::empty(); 4];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let target = pool.reserve_scope(3).unwrap();
            let _foreign = pool.reserve_scope(1).unwrap();
            let (scope_id, anchor) = PrivatePagePool::test_scope_identity(&target);
            match corruption {
                Corruption::WrongHead => pool.test_set_scope_member_head(&target, 1),
                Corruption::EarlyEnd => pool.test_set_scope_member_next(0, usize::MAX),
                Corruption::Duplicate => pool.test_set_scope_member_next(0, 0),
                Corruption::Cycle => pool.test_set_scope_member_next(2, 0),
                Corruption::Overlong => pool.test_set_scope_member_next(2, 3),
                Corruption::WrongScope => {
                    pool.test_set_scope_member_identity(1, scope_id + 1, anchor);
                }
                Corruption::WrongAnchor => {
                    pool.test_set_scope_member_identity(1, scope_id, usize::MAX);
                }
            }
            assert!(matches!(
                plan.attach(&pool, &target),
                Err(FreeBitmapCowError::StaleReservationPredecessor)
            ));
            assert_eq!(storage.reclamation.state.load(Ordering::Acquire), 0);
        }

        for corruption in [
            Corruption::WrongHead,
            Corruption::EarlyEnd,
            Corruption::Duplicate,
            Corruption::Cycle,
            Corruption::Overlong,
            Corruption::WrongScope,
            Corruption::WrongAnchor,
        ] {
            run(corruption);
        }
    }

    #[test]
    fn late_binding_rejects_reordered_vacancy_but_accepts_canonical_rebind_order() {
        fn run(unbind_order: [u32; 3], accepted: bool) {
            let source = SparsePages::new([leaf(11, 1, &[5, 9])]);
            let mut storage = PlannerStorage::new(3, 4, 4, 8);
            storage.arena.clear();
            let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 11, 2, storage.buffers())
                .unwrap()
                .plan_capacity()
                .unwrap();
            let mut slots = vec![PrivatePagePoolSlot::empty(); 3];
            let pool = PrivatePagePool::new_vacant(&mut slots, 20, 20, 2).unwrap();
            let target = pool.reserve_scope(3).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            for pgno in [3, 7, 9] {
                pool.bind_page(
                    &checkpoint,
                    &target,
                    pgno,
                    PrivatePageAuthorization::SafelyReclaimed,
                )
                .unwrap();
            }
            for pgno in unbind_order {
                pool.unbind_page(&checkpoint, &target, pgno).unwrap();
            }
            pool.commit_checkpoint(checkpoint).unwrap();

            let attached = plan.attach(&pool, &target);
            if accepted {
                let (attachment, request) = attached.unwrap();
                assert_eq!(request.ticket.state.load(Ordering::Acquire), 1);
                let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
                assert!(attachment.bind(proof).is_ok());
            } else {
                assert!(matches!(
                    attached,
                    Err(FreeBitmapCowError::StaleReservationPredecessor)
                ));
                assert_eq!(storage.reclamation.state.load(Ordering::Acquire), 0);
            }
        }

        run([3, 7, 9], false);
        run([9, 7, 3], true);
    }

    #[test]
    fn late_binding_selection_is_zero_allocation_at_512_and_4096_sources() {
        fn run(count: usize) {
            let candidates: Vec<u32> = (0..count).map(|index| 10_000 + index as u32).collect();
            let source = SparsePages::new([leaf(2, 1, &candidates)]);
            let private_pages = count + 1;
            let index_required = private_pages + 1 + count + 1;
            let mut storage = PlannerStorage::new(private_pages, count + 1, 1, index_required);
            storage.arena.clear();
            let plan =
                FreeBitmapReservationPlanner::new(&source, 1, 30_000, 2, count, storage.buffers())
                    .unwrap()
                    .plan_capacity()
                    .unwrap();
            assert_eq!(plan.required_private_pages(), private_pages);
            let mut slots: Vec<_> = (0..private_pages)
                .map(|_| PrivatePagePoolSlot::empty())
                .collect();
            let pool = PrivatePagePool::new_vacant(&mut slots, 30_000, 30_000, 2).unwrap();
            let scope = pool.reserve_scope(private_pages).unwrap();
            let (attachment, request) = plan.attach(&pool, &scope).unwrap();
            let reclaimed: Vec<u32> = (0..count).map(|index| 3 + index as u32).collect();
            let proof = complete_free_bitmap_reclamation_for_test(request, 71, &reclaimed).unwrap();
            let (bound, allocations) = count_thread_allocations(|| attachment.bind(proof));
            let bound = bound.unwrap();
            assert_eq!(allocations, 0);
            assert_eq!(bound.binding.committed, 1);
            assert_eq!(bound.binding.reclaimed, count);
            assert_eq!(bound.binding.appended, 0);
            assert_eq!(bound.cow.available_private_pages(), count);
        }

        run(512);
        run(4096);
    }

    #[test]
    fn late_binding_all_committed_batch_is_single_validation_and_logarithmic() {
        fn run(count: usize) -> usize {
            let candidates: Vec<u32> = (0..count).map(|index| 10_000 + index as u32).collect();
            let source = SparsePages::new([leaf(2, 1, &candidates)]);
            let private_pages = count + 1;
            let index_required = private_pages + 1 + count + 1;
            let mut storage = PlannerStorage::new(private_pages, count + 1, 1, index_required);
            storage.arena.clear();
            let plan =
                FreeBitmapReservationPlanner::new(&source, 1, 30_000, 2, count, storage.buffers())
                    .unwrap()
                    .plan_capacity()
                    .unwrap();
            let mut slots = vec![PrivatePagePoolSlot::empty(); private_pages];
            let pool = PrivatePagePool::new_vacant(&mut slots, 30_000, 30_000, 2).unwrap();
            let scope = pool.reserve_scope(private_pages).unwrap();
            let (attachment, request) = plan.attach(&pool, &scope).unwrap();
            let proof = complete_free_bitmap_reclamation_for_test(request, 0, &[]).unwrap();
            let (bound, allocations) = count_thread_allocations(|| attachment.bind(proof));
            let bound = bound.unwrap();
            assert_eq!(allocations, 0);
            assert_eq!(bound.binding.committed, count);
            assert_eq!(bound.binding.reclaimed, 0);
            assert_eq!(bound.binding.appended, 1);
            assert_eq!(bound.cow.scoped_validation_pass_count(), 1);
            let probes = bound.cow.index_probe_count();
            let logarithmic_factor = usize::BITS as usize - count.leading_zeros() as usize + 1;
            assert!(
                probes <= 16 * count * logarithmic_factor,
                "{probes} probes exceeded the O(k log k) bound for {count} candidates"
            );
            probes
        }

        let small = run(512);
        let large = run(4_096);
        assert!(small > 0);
        assert!(large <= small * 12);
    }

    #[test]
    fn insertion_builds_empty_and_absent_paths_and_existing_bits_are_noops() {
        let source = AccessControlledPages::new([]);
        let mut storage = CowStorage::new(90_000..90_004, 0);
        let mut cow = storage.cow(&source, 100_000, 0);
        let pages = [5, 40_000];
        let mut scratch: Vec<_> = (0..3).map(|_| FreeBitmapInsertPage::empty()).collect();
        let plan = cow.plan_insert_free(&pages, &mut scratch).unwrap();
        assert_eq!(plan.required_private_pages(), 3);
        assert_eq!(plan.changed_bitmap_pages(), 3);
        assert_ne!(plan.result_root(), 0);
        let result = plan.apply().unwrap();
        assert_eq!(
            result,
            FreeBitmapInsertResult {
                inserted: 2,
                already_free: 0,
                committed_replacements: 0,
                new_bitmap_pages: 3,
                recycled_private_pages: 0,
            }
        );
        assert!(private_free_bit(&cow, 5));
        assert!(private_free_bit(&cow, 40_000));
        let root_page = cow.private_page(cow.root()).unwrap();
        let root = BitmapBranch::open(&root_page, 2, BitmapKind::FreePages).unwrap();
        assert_eq!(root.level(), 1);
        assert_eq!(PageHeader::decode(&root_page, 2).unwrap().item_count, 2);
        assert!(root.summary_bit(0));
        assert!(root.summary_bit(1));
        assert!(!root.summary_bit(2));
        drop(root_page);

        let root_before = cow.root();
        let replacements_before = cow.replacements().len();
        let plan = cow.plan_insert_free(&pages, &mut scratch).unwrap();
        assert_eq!(plan.required_private_pages(), 0);
        let result = plan.apply().unwrap();
        assert_eq!(result.inserted, 0);
        assert_eq!(result.already_free, 2);
        assert_eq!(cow.root(), root_before);
        assert_eq!(cow.replacements().len(), replacements_before);

        source.deny_access(PageSourceError::ForkedHandle);
        let checks = source.checks.get();
        assert!(matches!(
            cow.plan_insert_free(&[], &mut scratch),
            Err(FreeBitmapCowError::Source(PageSourceError::ForkedHandle))
        ));
        assert_eq!(source.checks.get(), checks + 1);
    }

    #[test]
    fn insertion_clones_committed_origins_once_and_rejects_malformed_paths_atomically() {
        let source = SparsePages::new([leaf(2, 1, &[5])]);
        let mut storage = CowStorage::new([10], 1);
        let mut cow = storage.cow(&source, 20, 2);
        let mut scratch = [FreeBitmapInsertPage::empty()];
        let plan = cow.plan_insert_free(&[6], &mut scratch).unwrap();
        assert_eq!(plan.required_private_pages(), 1);
        let result = plan.apply().unwrap();
        assert_eq!(result.committed_replacements, 1);
        assert_eq!(cow.replacements(), &[2]);
        assert_eq!(cow.root(), 10);
        assert!(private_free_bit(&cow, 5));
        assert!(private_free_bit(&cow, 6));
        let plan = cow.plan_insert_free(&[7], &mut scratch).unwrap();
        let result = plan.apply().unwrap();
        assert_eq!(result.committed_replacements, 0);
        assert_eq!(cow.replacements(), &[2]);
        assert!(private_free_bit(&cow, 7));

        let mut corrupt = leaf(2, 1, &[5]);
        corrupt.bytes[SUMMARY_OFFSET] ^= 0x80;
        let corrupt_source = SparsePages::new([corrupt]);
        let mut corrupt_storage = CowStorage::new([10], 1);
        let mut corrupt_cow = corrupt_storage.cow(&corrupt_source, 20, 2);
        let arena_before = corrupt_cow.test_page_at(0);
        let result = corrupt_cow.plan_insert_free(&[6], &mut scratch);
        assert!(matches!(
            result,
            Err(FreeBitmapCowError::Page {
                pgno: 2,
                cause: BitmapPageError::Checksum,
            })
        ));
        assert_eq!(corrupt_cow.root(), 2);
        assert!(corrupt_cow.replacements().is_empty());
        assert_eq!(corrupt_cow.test_page_at(0), arena_before);
        assert_eq!(corrupt_cow.test_state_at(0), PrivatePageState::Available);
    }

    #[test]
    fn insertion_stream_guards_and_exact_budget_failures_are_atomic() {
        let source = SparsePages::new([]);
        let mut storage = CowStorage::new([50], 0);
        let mut cow = storage.cow(&source, 40_000, 0);
        let mut scratch = [const { FreeBitmapInsertPage::empty() }; 2];
        let before = cow.test_page_at(0);
        assert!(matches!(
            cow.plan_insert_free(&[0], &mut scratch),
            Err(FreeBitmapCowError::InsertPageOutOfBounds(0))
        ));
        assert!(matches!(
            cow.plan_insert_free(&[1], &mut scratch),
            Err(FreeBitmapCowError::InsertPageOutOfBounds(1))
        ));
        assert!(matches!(
            cow.plan_insert_free(&[5, 5], &mut scratch),
            Err(FreeBitmapCowError::InsertPageOrderRegression {
                previous: 5,
                current: 5,
            })
        ));
        assert!(matches!(
            cow.plan_insert_free(&[6, 5], &mut scratch),
            Err(FreeBitmapCowError::InsertPageOrderRegression {
                previous: 6,
                current: 5,
            })
        ));
        assert!(matches!(
            cow.plan_insert_free(&[5], &mut scratch),
            Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::ArenaPages,
                required: 2,
                available: 1,
            })
        ));
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.test_page_at(0), before);
        assert_eq!(cow.test_state_at(0), PrivatePageState::Available);

        let mut enough_storage = CowStorage::new([50, 51], 0);
        let mut enough_cow = enough_storage.cow(&source, 40_000, 0);
        let mut short_scratch = [FreeBitmapInsertPage::empty()];
        assert!(matches!(
            enough_cow.plan_insert_free(&[5], &mut short_scratch),
            Err(FreeBitmapCowError::InsertScratchExhausted {
                required: 2,
                available: 1,
            })
        ));
        assert_eq!(enough_cow.root(), 0);
        assert!((0..enough_cow.pool().len())
            .all(|slot| enough_cow.test_state_at(slot) == PrivatePageState::Available));

        let committed = SparsePages::new([leaf(2, 1, &[5])]);
        let mut no_replacement = CowStorage::new([10], 0);
        let mut committed_cow = no_replacement.cow(&committed, 20, 2);
        let mut leaf_scratch = [FreeBitmapInsertPage::empty()];
        assert!(matches!(
            committed_cow.plan_insert_free(&[6], &mut leaf_scratch),
            Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::ReplacementPages,
                required: 1,
                available: 0,
            })
        ));
        assert_eq!(committed_cow.root(), 2);
        assert!(committed_cow.replacements().is_empty());
        assert_eq!(committed_cow.test_state_at(0), PrivatePageState::Available);
        assert!(matches!(
            committed_cow.plan_insert_free(&[2], &mut leaf_scratch),
            Err(FreeBitmapCowError::InsertPageIsBitmapPath(2))
        ));
    }

    #[test]
    fn insertion_apply_rechecks_access_even_for_a_cached_noop_plan() {
        let source = AccessControlledPages::new([leaf(2, 1, &[5])]);
        let mut storage = CowStorage::new([10], 1);
        let mut cow = storage.cow(&source, 20, 2);
        let mut scratch = [FreeBitmapInsertPage::empty()];
        let plan = cow.plan_insert_free(&[5], &mut scratch).unwrap();
        assert_eq!(plan.required_private_pages(), 0);
        source.deny_access(PageSourceError::ForkedHandle);
        let checks = source.checks.get();
        assert_eq!(
            plan.apply(),
            Err(FreeBitmapCowError::Source(PageSourceError::ForkedHandle))
        );
        assert_eq!(source.checks.get(), checks + 1);
        assert_eq!(cow.root(), 2);
        assert!(cow.replacements().is_empty());
        assert_eq!(cow.test_state_at(0), PrivatePageState::Available);
    }

    #[test]
    fn shared_constructor_binds_transaction_preserves_prefixes_and_is_failure_atomic() {
        let source = SparsePages::new([]);

        {
            let mut slots = [ReservedBitmapPage::authorized(
                10,
                PrivatePageAuthorization::CommittedFree,
            )];
            let pool = PrivatePagePool::new(&mut slots, 20, 20, 3).unwrap();
            let mut replacements = [3u32];
            let mut candidates = [4u32];
            let mut index = [BitmapCowIndexNode::empty(); 2];
            let index_before = index;
            let mut available = [usize::MAX];
            let error = FreeBitmapCow::from_pool(
                &source,
                1,
                0,
                &pool,
                SharedFreeBitmapCowLedger::with_prefixes(
                    &mut replacements,
                    1,
                    &mut candidates,
                    1,
                    &mut index,
                    &mut available,
                ),
            )
            .unwrap_err();
            assert_eq!(
                error,
                FreeBitmapCowError::PrivatePool(PrivatePagePoolError::PendingTransactionMismatch {
                    expected: 3,
                    actual: 2,
                })
            );
            assert_eq!(index, index_before);
            assert_eq!(available, [usize::MAX]);
        }

        {
            let mut slots = [ReservedBitmapPage::authorized(
                10,
                PrivatePageAuthorization::CommittedFree,
            )];
            let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
            let mut replacements = [3u32, 3];
            let mut candidates = [4u32];
            let mut index = [BitmapCowIndexNode::empty(); 3];
            let mut available = [usize::MAX];
            let error = FreeBitmapCow::from_pool(
                &source,
                1,
                0,
                &pool,
                SharedFreeBitmapCowLedger::with_prefixes(
                    &mut replacements,
                    2,
                    &mut candidates,
                    1,
                    &mut index,
                    &mut available,
                ),
            )
            .unwrap_err();
            assert_eq!(error, FreeBitmapCowError::DuplicateReplacement(3));
            assert_eq!(pool.state(0).unwrap(), PrivatePagePoolState::Available);
            assert_eq!(pool.test_bytes(0).unwrap(), [0; PAGE_SIZE]);
            assert_eq!(replacements, [3, 3]);
            assert_eq!(candidates, [4]);
            let cow = FreeBitmapCow::from_pool(
                &source,
                1,
                0,
                &pool,
                SharedFreeBitmapCowLedger::with_prefixes(
                    &mut replacements,
                    1,
                    &mut candidates,
                    1,
                    &mut index,
                    &mut available,
                ),
            )
            .unwrap();
            assert_eq!(cow.replacements(), &[3]);
            assert_eq!(cow.candidates(), &[4]);
            assert_eq!(cow.available_private_pages(), 1);
        }

        let mut slots = [ReservedBitmapPage::authorized(
            10,
            PrivatePageAuthorization::CommittedFree,
        )];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let mut replacements = [3u32, 0];
        let mut candidates = [4u32, 0];
        let mut index = [BitmapCowIndexNode::empty(); 3];
        let mut available = [usize::MAX];
        let mut cow = FreeBitmapCow::from_pool(
            &source,
            1,
            0,
            &pool,
            SharedFreeBitmapCowLedger::with_prefixes(
                &mut replacements,
                1,
                &mut candidates,
                1,
                &mut index,
                &mut available,
            ),
        )
        .unwrap();
        assert_eq!(cow.replacements(), &[3]);
        assert_eq!(cow.candidates(), &[4]);
        let mut scratch = [FreeBitmapInsertPage::empty()];
        cow.insert_free(5, &mut scratch).unwrap();
        assert_eq!(cow.replacements(), &[3]);
        assert_eq!(cow.candidates(), &[4]);
        assert!(private_free_bit(&cow, 5));
    }

    #[test]
    fn shared_constructor_accepts_foreign_retirement_pages_and_rejects_invalid_bitmap_tag() {
        let source = SparsePages::new([]);
        let mut foreign_replacements = [];
        let mut foreign_candidates = [];
        let mut foreign_index = [BitmapCowIndexNode::empty(); 2];
        let mut foreign_available = [usize::MAX; 2];
        let mut invalid_tag_replacements = [];
        let mut invalid_tag_candidates = [];
        let mut invalid_tag_index = [BitmapCowIndexNode::empty(); 2];
        let mut invalid_tag_available = [usize::MAX; 2];
        let mut wrong_generation_replacements = [];
        let mut wrong_generation_candidates = [];
        let mut wrong_generation_index = [BitmapCowIndexNode::empty(); 2];
        let mut wrong_generation_available = [usize::MAX; 2];
        let mut valid_replacements = [];
        let mut valid_candidates = [];
        let mut valid_index = [BitmapCowIndexNode::empty(); 2];
        let mut valid_available = [usize::MAX; 2];
        let mut slots = [
            ReservedBitmapPage::authorized(10, PrivatePageAuthorization::CommittedFree),
            ReservedBitmapPage::authorized(11, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let retirement_tree = pool.claim(0, PrivatePageOwner::Retirement, 7, 1).unwrap();
        let _retirement_blob = pool.claim(1, PrivatePageOwner::Retirement, 8, 2).unwrap();

        let invalid_bitmap = {
            let cow = FreeBitmapCow::from_pool(
                &source,
                1,
                0,
                &pool,
                SharedFreeBitmapCowLedger::empty(
                    &mut foreign_replacements,
                    &mut foreign_candidates,
                    &mut foreign_index,
                    &mut foreign_available,
                ),
            )
            .unwrap();
            assert_eq!(cow.test_state_at(0), PrivatePageState::Foreign);
            assert_eq!(cow.test_state_at(1), PrivatePageState::Foreign);
            assert_eq!(cow.available_private_pages(), 0);
            let invalid_bitmap = pool
                .transfer(
                    retirement_tree,
                    PrivatePageOwner::Bitmap,
                    pool.pending_txn(),
                    u64::MAX,
                )
                .unwrap();
            assert_eq!(cow.test_state_at(0), PrivatePageState::Foreign);
            invalid_bitmap
        };
        let before = pool.test_bytes(0).unwrap();
        assert_eq!(
            FreeBitmapCow::from_pool(
                &source,
                1,
                0,
                &pool,
                SharedFreeBitmapCowLedger::empty(
                    &mut invalid_tag_replacements,
                    &mut invalid_tag_candidates,
                    &mut invalid_tag_index,
                    &mut invalid_tag_available,
                ),
            )
            .unwrap_err(),
            FreeBitmapCowError::InvalidBitmapPoolState {
                pgno: 10,
                owner: PrivatePageOwner::Bitmap,
                owner_generation: 2,
                tag: u64::MAX,
            }
        );
        assert_eq!(pool.test_bytes(0).unwrap(), before);
        assert!(matches!(
            pool.state(0).unwrap(),
            PrivatePagePoolState::InUse {
                owner: PrivatePageOwner::Bitmap,
                owner_generation: 2,
                tag: u64::MAX,
            }
        ));

        let retirement_tree = pool
            .transfer(invalid_bitmap, PrivatePageOwner::Retirement, 7, 1)
            .unwrap();
        let wrong_generation = pool
            .transfer(retirement_tree, PrivatePageOwner::Bitmap, 3, 0)
            .unwrap();
        assert_eq!(
            FreeBitmapCow::from_pool(
                &source,
                1,
                0,
                &pool,
                SharedFreeBitmapCowLedger::empty(
                    &mut wrong_generation_replacements,
                    &mut wrong_generation_candidates,
                    &mut wrong_generation_index,
                    &mut wrong_generation_available,
                ),
            )
            .unwrap_err(),
            FreeBitmapCowError::InvalidBitmapPoolState {
                pgno: 10,
                owner: PrivatePageOwner::Bitmap,
                owner_generation: 3,
                tag: 0,
            }
        );

        let retirement_tree = pool
            .transfer(wrong_generation, PrivatePageOwner::Retirement, 7, 1)
            .unwrap();
        let _valid_bitmap = pool
            .transfer(retirement_tree, PrivatePageOwner::Bitmap, 2, 0)
            .unwrap();
        let cow = FreeBitmapCow::from_pool(
            &source,
            1,
            0,
            &pool,
            SharedFreeBitmapCowLedger::empty(
                &mut valid_replacements,
                &mut valid_candidates,
                &mut valid_index,
                &mut valid_available,
            ),
        )
        .unwrap();
        assert_eq!(
            cow.test_state_at(0),
            PrivatePageState::InUse {
                committed_origin: 0,
            }
        );
        assert_eq!(cow.test_state_at(1), PrivatePageState::Foreign);
    }

    #[test]
    fn shared_preparation_scales_with_caller_index_and_allocates_nothing() {
        fn run(count: usize) -> usize {
            let source = SparsePages::new([]);
            let mut replacements: Vec<_> = (0..count)
                .map(|offset| 10_000 + u32::try_from(offset).unwrap())
                .collect();
            let mut candidates: Vec<_> = (0..count)
                .map(|offset| 40_000 + u32::try_from(offset).unwrap())
                .collect();
            let mut index = vec![BitmapCowIndexNode::empty(); count * 2];
            let mut available = vec![usize::MAX; count];
            let mut slots: Vec<_> = (0..count)
                .map(|offset| {
                    ReservedBitmapPage::authorized(
                        100_000 + u32::try_from(offset).unwrap(),
                        PrivatePageAuthorization::CommittedFree,
                    )
                })
                .collect();
            let pool = PrivatePagePool::new(&mut slots, 200_000, 200_000, 2).unwrap();

            let ((work, height), allocations) = count_thread_allocations(|| {
                let cow = FreeBitmapCow::from_pool(
                    &source,
                    1,
                    0,
                    &pool,
                    SharedFreeBitmapCowLedger::with_prefixes(
                        &mut replacements,
                        count,
                        &mut candidates,
                        count,
                        &mut index,
                        &mut available,
                    ),
                )
                .unwrap();
                assert_eq!(cow.replacements().len(), count);
                assert_eq!(cow.candidates().len(), count);
                assert_eq!(cow.available_private_pages(), count);
                (
                    cow.shared_preparation_work(),
                    cow.index_nodes[cow.index_root].height,
                )
            });
            assert_eq!(allocations, 0);
            assert_eq!(replacements[0], 10_000);
            assert_eq!(replacements[count - 1], 10_000 + count as u32 - 1);
            assert_eq!(candidates[0], 40_000);
            assert_eq!(candidates[count - 1], 40_000 + count as u32 - 1);
            let logarithmic_height_bound =
                2 * u8::try_from((usize::BITS - (count * 2 + 1).leading_zeros()) as usize).unwrap();
            assert!(height <= logarithmic_height_bound);
            let logarithmic_work_bound =
                count * 3 * usize::try_from(usize::BITS - (count * 2).leading_zeros()).unwrap() * 6;
            assert!(work <= logarithmic_work_bound);
            work
        }

        let samples = [run(512), run(4_096), run(8_192)];
        assert!(samples[1] < samples[0] * 12);
        assert!(samples[2] < samples[1] * 3);
    }

    #[test]
    fn shared_plan_rejects_owner_drift_before_any_bitmap_mutation() {
        let source = SparsePages::new([]);
        let mut slots = [
            ReservedBitmapPage::authorized(90_000, PrivatePageAuthorization::CommittedFree),
            ReservedBitmapPage::authorized(90_001, PrivatePageAuthorization::CommittedFree),
            ReservedBitmapPage::authorized(90_002, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 100_000, 100_000, 2).unwrap();
        let mut replacements = [];
        let mut candidates = [];
        let mut index = [BitmapCowIndexNode::empty(); 3];
        let mut available = [usize::MAX; 3];
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
        let mut scratch = [const { FreeBitmapInsertPage::empty() }; 3];
        let plan = cow.plan_insert_free(&[5, 40_000], &mut scratch).unwrap();
        let _retirement_authority = pool.claim(2, PrivatePageOwner::Retirement, 7, 1).unwrap();

        assert!(matches!(
            plan.apply(),
            Err(FreeBitmapCowError::PrivatePool(
                PrivatePagePoolError::StaleSnapshot { .. }
            ))
        ));
        assert_eq!(cow.root(), 0);
        assert!(cow.replacements().is_empty());
        assert!(cow.candidates().is_empty());
        assert!(matches!(
            pool.state(2).unwrap(),
            PrivatePagePoolState::InUse {
                owner: PrivatePageOwner::Retirement,
                owner_generation: 7,
                ..
            }
        ));
        for slot in 0..2 {
            assert_eq!(pool.state(slot).unwrap(), PrivatePagePoolState::Available);
            assert_eq!(pool.test_bytes(slot).unwrap(), [0; PAGE_SIZE]);
        }
    }

    #[test]
    fn later_epoch_exhaustion_is_rejected_before_any_insertion_mutation() {
        let source = SparsePages::new([]);
        let mut slots = [
            ReservedBitmapPage::authorized(90_000, PrivatePageAuthorization::CommittedFree),
            ReservedBitmapPage::authorized(90_001, PrivatePageAuthorization::CommittedFree),
            ReservedBitmapPage::authorized(90_002, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 100_000, 100_000, 2).unwrap();
        let mut replacements = [];
        let mut candidates = [];
        let mut index = [BitmapCowIndexNode::empty(); 3];
        let mut available = [usize::MAX; 3];
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
        pool.test_set_epoch(u64::MAX - 5);
        let mut scratch = [const { FreeBitmapInsertPage::empty() }; 3];
        let plan = cow.plan_insert_free(&[5, 40_000], &mut scratch).unwrap();
        assert_eq!(plan.changed_bitmap_pages(), 3);
        assert_eq!(
            plan.apply(),
            Err(FreeBitmapCowError::PrivatePool(
                PrivatePagePoolError::EpochExhausted
            ))
        );
        assert_eq!(cow.root(), 0);
        assert!(cow.replacements().is_empty());
        assert!(cow.candidates().is_empty());
        for slot in 0..3 {
            assert_eq!(pool.state(slot).unwrap(), PrivatePagePoolState::Available);
            assert_eq!(pool.test_bytes(slot).unwrap(), [0; PAGE_SIZE]);
        }
    }

    #[test]
    fn insertion_rejects_a_self_consistent_bad_summary_with_exact_page_evidence() {
        let mut root = branch(2, 1, 1, 3);
        root.bytes[SUMMARY_OFFSET..SUMMARY_OFFSET + 8].copy_from_slice(&0u64.to_le_bytes());
        page::write_crc32c(&mut root.bytes);
        let source = SparsePages::new([root, leaf(3, 1, &[5])]);
        let mut storage = CowStorage::new([10, 11], 2);
        let mut cow = storage.cow(&source, 40_000, 2);
        let mut scratch = [const { FreeBitmapInsertPage::empty() }; 2];
        assert!(matches!(
            cow.plan_insert_free(&[6], &mut scratch),
            Err(FreeBitmapCowError::CommittedSummaryMismatch(2))
        ));
        assert_eq!(source.page_reads(2), 1);
        assert_eq!(source.page_reads(3), 0);
        assert_eq!(cow.root(), 2);
        assert!(cow.replacements().is_empty());
        assert!((0..cow.pool().len())
            .all(|slot| cow.test_state_at(slot) == PrivatePageState::Available));
    }

    #[test]
    fn governing_page_count_promotes_and_demotes_at_all_exact_boundaries() {
        prove_root_boundary(SparsePages::new([leaf(2, 1, &[5])]), 32_000, 0);
        prove_root_boundary(
            SparsePages::new([branch(2, 1, 1, 3), leaf(3, 1, &[5])]),
            8_192_000,
            1,
        );
        prove_root_boundary(
            SparsePages::new([branch(2, 1, 2, 3), branch(3, 1, 1, 4), leaf(4, 1, &[5])]),
            2_097_152_000,
            2,
        );
    }

    #[test]
    fn direct_free_and_proven_reclaim_streams_share_one_canonical_draft() {
        let source = SparsePages::new([]);
        let mut storage = CowStorage::new([50], 0);
        let mut cow = storage.cow(&source, 100, 0);
        let mut scratch = [FreeBitmapInsertPage::empty()];

        let direct_free = cow.plan_insert_free(&[5, 6], &mut scratch).unwrap();
        let direct_result = direct_free.apply().unwrap();
        assert_eq!(direct_result.inserted, 2);
        assert_eq!(direct_result.new_bitmap_pages, 1);

        let proven_reclaim = cow.plan_insert_free(&[7, 8], &mut scratch).unwrap();
        assert_eq!(proven_reclaim.required_private_pages(), 0);
        let reclaim_result = proven_reclaim.apply().unwrap();
        assert_eq!(reclaim_result.inserted, 2);
        assert_eq!(reclaim_result.new_bitmap_pages, 0);
        for bit in 5..=8 {
            assert!(private_free_bit(&cow, bit));
        }
        assert!(cow.replacements().is_empty());
    }

    #[test]
    fn unused_candidates_reinsert_and_only_the_available_appended_suffix_truncates() {
        let source = SparsePages::new([]);
        let mut storage = CowStorage::new([5, 50, 100, 101, 102], 0);
        storage.arena[0].set_adapter_label(PrivatePageOwner::Bitmap, BITMAP_SLOT_CANDIDATE);
        storage.arena[1].authorize_generic(50);
        storage.arena[2].authorize_initial(100, PrivatePageAuthorization::Appended);
        storage.arena[3].authorize_initial(101, PrivatePageAuthorization::Appended);
        storage.arena[3].preset_bitmap_page(2, 0, [0; PAGE_SIZE]);
        storage.arena[4].authorize_initial(102, PrivatePageAuthorization::Appended);
        let mut cow = storage.cow_with_page_counts(&source, 100, 103, 0);
        let mut release_pages = [0u32; 2];
        let mut scratch = [FreeBitmapInsertPage::empty()];

        let result = cow
            .release_unused_reservations(&mut release_pages, &mut scratch)
            .unwrap();
        assert_eq!(
            result,
            UnusedReservationRelease {
                reinserted_candidates: 1,
                reinserted_appended: 1,
                truncated_appended: 1,
                pending_page_count: 102,
            }
        );
        assert_eq!(release_pages, [5, 100]);
        assert!(private_free_bit(&cow, 5));
        assert!(private_free_bit(&cow, 100));
        assert_eq!(cow.test_state_at(0), PrivatePageState::ReleasedFree);
        assert_eq!(cow.test_state_at(2), PrivatePageState::ReleasedFree);
        assert_eq!(
            cow.test_state_at(3),
            PrivatePageState::InUse {
                committed_origin: 0
            }
        );
        assert_eq!(cow.test_state_at(4), PrivatePageState::ReleasedTail);
        assert_eq!(cow.available_private_pages(), 0);
        assert!(cow.replacements().is_empty());
    }

    #[test]
    fn private_root_demotion_recycles_without_retirement_or_replacement() {
        let source = SparsePages::new([]);
        let mut storage = CowStorage::new([10, 11], 0);
        storage.arena[0].preset_bitmap_page(2, 0, branch(10, 2, 1, 11).bytes);
        storage.arena[1].preset_bitmap_page(2, 0, leaf(11, 2, &[5]).bytes);
        let mut cow = storage.cow_with_page_counts(&source, 32_000, 40_000, 10);
        let mut scratch: Vec<_> = (0..2).map(|_| FreeBitmapInsertPage::empty()).collect();
        let plan = cow
            .plan_insert_free_for_page_count(&[], 32_000, &mut scratch)
            .unwrap();
        let result = plan.apply().unwrap();
        assert_eq!(result.recycled_private_pages, 1);
        assert_eq!(result.committed_replacements, 0);
        assert_eq!(cow.root(), 11);
        assert_eq!(cow.test_state_at(0), PrivatePageState::Available);
        assert_eq!(
            cow.test_state_at(1),
            PrivatePageState::InUse {
                committed_origin: 0
            }
        );
        assert_eq!(cow.available_private_pages(), 1);
        assert!(cow.replacements().is_empty());
    }

    #[test]
    fn finalization_reinserts_a_demoted_candidate_in_the_same_atomic_plan() {
        let source = SparsePages::new([]);
        let mut storage = CowStorage::new([10, 11, 32_000], 0);
        storage.arena[0].set_adapter_label(PrivatePageOwner::Bitmap, BITMAP_SLOT_CANDIDATE);
        storage.arena[0].preset_bitmap_page(2, 0, branch(10, 2, 1, 11).bytes);
        storage.arena[1].preset_bitmap_page(2, 0, leaf(11, 2, &[5]).bytes);
        storage.arena[2].authorize_initial(32_000, PrivatePageAuthorization::Appended);
        let mut cow = storage.cow_with_page_counts(&source, 32_000, 32_001, 10);
        let mut release_pages = [0u32; 1];
        let mut scratch = [FreeBitmapInsertPage::empty()];

        let (result, allocations) = count_thread_allocations(|| {
            cow.release_unused_reservations(&mut release_pages, &mut scratch)
        });
        assert_eq!(allocations, 0);
        assert_eq!(
            result,
            Ok(UnusedReservationRelease {
                reinserted_candidates: 1,
                reinserted_appended: 0,
                truncated_appended: 1,
                pending_page_count: 32_000,
            })
        );
        assert_eq!(cow.root(), 11);
        assert!(private_free_bit(&cow, 10));
        assert_eq!(cow.test_state_at(0), PrivatePageState::ReleasedFree);
        assert_eq!(cow.test_state_at(2), PrivatePageState::ReleasedTail);
        assert_eq!(cow.available_private_pages(), 0);
        assert!(cow.replacements().is_empty());
    }

    #[test]
    fn finalization_demoted_candidate_budget_failure_is_atomic() {
        let source = SparsePages::new([]);
        let mut storage = CowStorage::new([10, 11, 32_000], 0);
        storage.arena[0].set_adapter_label(PrivatePageOwner::Bitmap, BITMAP_SLOT_CANDIDATE);
        storage.arena[0].preset_bitmap_page(2, 0, branch(10, 2, 1, 11).bytes);
        storage.arena[1].preset_bitmap_page(2, 0, leaf(11, 2, &[5]).bytes);
        storage.arena[2].authorize_initial(32_000, PrivatePageAuthorization::Appended);
        let mut cow = storage.cow_with_page_counts(&source, 32_000, 32_001, 10);
        let before: Vec<_> = (0..cow.pool().len())
            .map(|slot| {
                (
                    cow.test_page_number_at(slot),
                    cow.test_state_at(slot),
                    cow.test_page_at(slot),
                )
            })
            .collect();
        let mut release_pages = [0u32; 1];
        let mut scratch = [];

        let result = cow.release_unused_reservations(&mut release_pages, &mut scratch);
        assert!(matches!(
            result,
            Err(FreeBitmapCowError::InsertScratchExhausted {
                required: 1,
                available: 0,
            })
        ));
        assert_eq!(cow.root(), 10);
        assert_eq!(cow.pending_page_count(), 32_001);
        assert!(cow.replacements().is_empty());
        for (slot, expected) in before.iter().enumerate() {
            assert_eq!(
                (
                    cow.test_page_number_at(slot),
                    cow.test_state_at(slot),
                    cow.test_page_at(slot),
                ),
                *expected
            );
        }
    }

    #[test]
    fn insertion_requires_the_committed_root_level_after_pending_growth() {
        let source = SparsePages::new([branch(2, 1, 1, 3), leaf(3, 1, &[5])]);
        let mut storage = CowStorage::new([10, 11], 2);
        let mut cow = storage.cow(&source, 32_000, 2);
        cow.pending_page_count = 32_001;
        let mut scratch = [const { FreeBitmapInsertPage::empty() }; 2];

        assert!(matches!(
            cow.plan_insert_free(&[5], &mut scratch),
            Err(FreeBitmapCowError::RootLevel {
                expected: 0,
                actual: 1,
            })
        ));
        assert_eq!(cow.root(), 2);
        assert!(cow.replacements().is_empty());
        assert!((0..cow.pool().len())
            .all(|slot| cow.test_state_at(slot) == PrivatePageState::Available));
    }

    #[test]
    fn insertion_reuses_verified_identity_without_reading_or_rechecking_crc() {
        let source = SparsePages::new([]);
        let mut verified = [VerifiedBitmapPage {
            pgno: 2,
            bytes: leaf(2, 1, &[5]).bytes,
            base: 0,
            level: 0,
            parent: NO_INDEX,
            remaining: 1,
            survives: true,
        }];
        verified[0].bytes[page::PAGE_CRC_OFFSET] ^= 0x80;
        let mut storage = CowStorage::new([10], 1);
        let mut cow = storage.cow(&source, 20, 2);
        cow.verified_pages = &mut verified;
        page_index_insert_prechecked(
            cow.index_nodes,
            &mut cow.index_root,
            &mut cow.index_len,
            2,
            IndexedPage::Verified(0),
        );
        let mut scratch = [FreeBitmapInsertPage::empty()];

        let plan = cow.plan_insert_free(&[6], &mut scratch).unwrap();
        assert_eq!(source.reads.get(), 0);
        let result = plan.apply().unwrap();
        assert_eq!(result.committed_replacements, 1);
        assert_eq!(source.reads.get(), 0);
        assert_eq!(cow.replacements(), &[2]);
        assert_eq!(cow.indexed_page(2), Some(IndexedPage::Replacement));
        assert!(private_free_bit(&cow, 6));
    }

    #[test]
    fn insertion_rejects_a_cached_verified_page_at_a_different_logical_base() {
        let source = SparsePages::new([]);
        let mut verified = [VerifiedBitmapPage {
            pgno: 2,
            bytes: leaf(2, 1, &[5]).bytes,
            base: 32_000,
            level: 0,
            parent: NO_INDEX,
            remaining: 1,
            survives: true,
        }];
        let mut storage = CowStorage::new([10], 1);
        let mut cow = storage.cow(&source, 20, 2);
        cow.verified_pages = &mut verified;
        page_index_insert_prechecked(
            cow.index_nodes,
            &mut cow.index_root,
            &mut cow.index_len,
            2,
            IndexedPage::Verified(0),
        );
        let mut scratch = [FreeBitmapInsertPage::empty()];

        assert!(matches!(
            cow.plan_insert_free(&[6], &mut scratch),
            Err(FreeBitmapCowError::VerifiedPageIdentityMismatch {
                pgno: 2,
                expected_base: 0,
                actual_base: 32_000,
                expected_level: 0,
                actual_level: 0,
            })
        ));
        assert_eq!(source.reads.get(), 0);
        assert_eq!(cow.root(), 2);
        assert!(cow.replacements().is_empty());
        assert_eq!(cow.test_state_at(0), PrivatePageState::Available);
    }

    #[test]
    fn insertion_scales_without_heap_allocation_and_accepts_u32_max() {
        const LEAVES: usize = 128;
        let source = SparsePages::new([]);
        let pages: Vec<u32> = (0..LEAVES)
            .map(|index| u32::try_from(2 + index as u64 * BITMAP_LEAF_BITS).unwrap())
            .collect();
        let mut storage = CowStorage::new(1_000..1_000 + LEAVES as u32 + 1, 0);
        let mut cow = storage.cow(&source, coverage(1).unwrap(), 0);
        let mut scratch: Vec<_> = (0..LEAVES + 1)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let ((required, result), allocations) = count_thread_allocations(|| {
            let plan = cow.plan_insert_free(&pages, &mut scratch).unwrap();
            let required = plan.required_private_pages();
            let result = plan.apply().unwrap();
            (required, result)
        });
        assert_eq!(allocations, 0);
        assert_eq!(required, LEAVES + 1);
        assert_eq!(result.inserted, LEAVES);
        assert_eq!(result.new_bitmap_pages, LEAVES + 1);
        assert!(private_free_bit(&cow, pages[0]));
        assert!(private_free_bit(&cow, *pages.last().unwrap()));

        let mut max_storage = CowStorage::new([100, 101, 102, 103], 0);
        let mut max_cow = max_storage.cow(&source, MAX_PAGE_COUNT, 0);
        let mut max_scratch: Vec<_> = (0..FREE_PATH_CAPACITY)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let max_result = max_cow.insert_free(u32::MAX, &mut max_scratch).unwrap();
        assert_eq!(max_result.new_bitmap_pages, FREE_PATH_CAPACITY);
        assert!(private_free_bit(&max_cow, u32::MAX));
    }

    #[test]
    fn insertion_apply_preflight_work_is_linear_from_512_to_8192_pages() {
        fn run(page_len: usize) -> (usize, usize) {
            let source = SparsePages::new([]);
            let pages: Vec<u32> = (0..page_len)
                .map(|index| u32::try_from(2 + index as u64 * BITMAP_LEAF_BITS).unwrap())
                .collect();
            let level_one_nodes = page_len.div_ceil(BITMAP_FANOUT as usize);
            let slot_len = page_len + level_one_nodes + 1;
            let first_slot = pages[page_len - 1].checked_add(1).unwrap();
            let pending_page_count = u64::from(first_slot) + slot_len as u64 + 1;
            let mut storage =
                CowStorage::new(first_slot..first_slot + u32::try_from(slot_len).unwrap(), 0);
            let mut cow = storage.cow(&source, pending_page_count, 0);
            let mut scratch: Vec<_> = (0..slot_len)
                .map(|_| FreeBitmapInsertPage::empty())
                .collect();
            let plan = cow.plan_insert_free(&pages, &mut scratch).unwrap();
            let changed = plan.changed_bitmap_pages();
            plan.apply().unwrap();
            let checks = cow.apply_preflight_check_count();
            assert_eq!(checks, page_len + changed);
            assert_eq!(changed, slot_len);
            (page_len + changed, checks)
        }

        let work = [run(512), run(4_096), run(8_192)];
        for (expected, actual) in work {
            assert_eq!(actual, expected);
        }
    }

    #[test]
    fn peak_live_metadata_is_reserved_before_any_authorization() {
        let page_count = coverage(2).unwrap() + 1;
        let pages = [
            branch(2, 1, 3, 3),
            branch(3, 1, 2, 4),
            branch(4, 1, 1, 5),
            leaf(5, 1, &[10, 11]),
        ];
        let source = SparsePages::new(pages);
        let mut storage = PlannerStorage::new(4, 2, 4, 8);
        let plan =
            FreeBitmapReservationPlanner::new(&source, 1, page_count, 2, 1, storage.buffers())
                .unwrap()
                .plan()
                .unwrap();
        assert_eq!(plan.candidates(), &[10, 11]);
        assert_eq!(plan.appended_pages(), 2);
        assert_eq!(plan.reserved_pages(), 4);
        assert_eq!(plan.verified_path_pages(), 4);

        let mut cow = plan.into_cow();
        let (result, allocations) = count_thread_allocations(|| cow.apply_planned_reservation());
        assert_eq!(allocations, 0);
        assert_eq!(result, Ok(()));
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.candidates(), &[10, 11]);
        assert_eq!(cow.available_private_pages(), 4);

        let minus_one_source = SparsePages::new([
            branch(2, 1, 3, 3),
            branch(3, 1, 2, 4),
            branch(4, 1, 1, 5),
            leaf(5, 1, &[10, 11]),
        ]);
        let mut minus_one = PlannerStorage::new(3, 2, 4, 8);
        let result = FreeBitmapReservationPlanner::new(
            &minus_one_source,
            1,
            page_count,
            2,
            1,
            minus_one.buffers(),
        )
        .unwrap()
        .plan();
        assert!(matches!(
            result,
            Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::ArenaPages,
                required: 4,
                available: 3,
            })
        ));
        assert!(minus_one
            .arena
            .iter()
            .all(|page| page == &ReservedBitmapPage::empty()));
    }

    #[test]
    fn later_prefix_peak_is_preflighted_and_apply_checks_access_only_once() {
        let second_base = coverage(2).unwrap();
        let page_count = second_base + 2;
        let pages = [
            branch_many(2, 1, 3, &[(0, 3), (1, 6)]),
            branch(3, 1, 2, 4),
            branch(4, 1, 1, 5),
            leaf(5, 1, &[10]),
            branch(6, 1, 2, 7),
            branch(7, 1, 1, 8),
            leaf(8, 1, &[0, 1]),
        ];
        let source = AccessControlledPages::new(pages);
        let mut storage = PlannerStorage::new(4, 3, 7, 11);
        let plan =
            FreeBitmapReservationPlanner::new(&source, 1, page_count, 2, 1, storage.buffers())
                .unwrap()
                .plan()
                .unwrap();
        assert_eq!(
            plan.candidates(),
            &[
                10,
                u32::try_from(second_base).unwrap(),
                u32::try_from(second_base + 1).unwrap()
            ]
        );
        assert_eq!(plan.appended_pages(), 1);
        assert_eq!(plan.reserved_pages(), 4);
        assert_eq!(plan.verified_path_pages(), 7);

        let mut cow = plan.into_cow();
        let before_checks = source.checks.get();
        source.fail_on_check(before_checks + 3);
        let (result, allocations) = count_thread_allocations(|| cow.apply_planned_reservation());
        assert_eq!(allocations, 0);
        assert_eq!(result, Ok(()));
        assert_eq!(source.checks.get(), before_checks + 1);
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.available_private_pages(), 4);

        let minus_one_source = SparsePages::new([
            branch_many(2, 1, 3, &[(0, 3), (1, 6)]),
            branch(3, 1, 2, 4),
            branch(4, 1, 1, 5),
            leaf(5, 1, &[10]),
            branch(6, 1, 2, 7),
            branch(7, 1, 1, 8),
            leaf(8, 1, &[0, 1]),
        ]);
        let mut minus_one = PlannerStorage::new(3, 3, 7, 11);
        let result = FreeBitmapReservationPlanner::new(
            &minus_one_source,
            1,
            page_count,
            2,
            1,
            minus_one.buffers(),
        )
        .unwrap()
        .plan();
        assert!(matches!(
            result,
            Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::ArenaPages,
                required: 4,
                available: 3,
            })
        ));
        assert!(minus_one
            .arena
            .iter()
            .all(|page| page == &ReservedBitmapPage::empty()));
    }

    #[test]
    fn planned_candidate_can_fund_the_cow_that_clears_its_bit() {
        let source = SparsePages::new([leaf(2, 1, &[5, 6, 7])]);
        let mut storage = PlannerStorage::new(2, 2, 1, 3);
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 2, 1, storage.buffers())
            .unwrap()
            .plan()
            .unwrap();
        assert_eq!(plan.candidates(), &[5, 6]);
        assert_eq!(plan.appended_pages(), 0);
        assert_eq!(plan.verified_path_pages(), 1);
        assert_eq!(source.page_reads(2), 1);

        let mut cow = plan.into_cow();
        assert_eq!(cow.indexed_page(5), Some(IndexedPage::Arena(0)));
        let (result, allocations) = count_thread_allocations(|| cow.apply_planned_reservation());
        assert_eq!(allocations, 0);
        assert_eq!(result, Ok(()));
        assert_eq!(source.page_reads(2), 1);
        assert_eq!(cow.root(), 5);
        assert_eq!(cow.candidates(), &[5, 6]);
        assert_eq!(cow.indexed_page(2), Some(IndexedPage::Replacement));
        assert_eq!(cow.available_private_pages(), 1);
        assert_eq!(
            cow.test_authorization_at(0),
            PrivatePageAuthorization::CommittedFree
        );
        assert!(cow.is_planned_candidate(5));
        assert_eq!(
            cow.test_state_at(0),
            PrivatePageState::InUse {
                committed_origin: 2
            }
        );
        assert_eq!(&*cow.private_page(5).unwrap(), &leaf(5, 2, &[7]).bytes);

        let multilevel = SparsePages::new([branch(2, 1, 1, 3), leaf(3, 1, &[5, 6, 7])]);
        let mut multilevel_storage = PlannerStorage::new(3, 3, 2, 5);
        let multilevel_plan = FreeBitmapReservationPlanner::new(
            &multilevel,
            1,
            32_001,
            2,
            1,
            multilevel_storage.buffers(),
        )
        .unwrap()
        .plan()
        .unwrap();
        assert_eq!(multilevel_plan.candidates(), &[5, 6, 7]);
        let mut multilevel_cow = multilevel_plan.into_cow();
        let (result, allocations) =
            count_thread_allocations(|| multilevel_cow.apply_planned_reservation());
        assert_eq!(allocations, 0);
        assert_eq!(result, Ok(()));
        assert_eq!(multilevel_cow.root(), 0);
        assert_eq!(multilevel_cow.available_private_pages(), 3);
        assert_eq!(multilevel.page_reads(2), 1);
        assert_eq!(multilevel.page_reads(3), 1);
    }

    #[test]
    fn appended_slots_are_used_only_after_verified_candidate_exhaustion() {
        let source = SparsePages::new([leaf(2, 1, &[5])]);
        let mut storage = PlannerStorage::new(3, 3, 1, 4);
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 20, 2, 3, storage.buffers())
            .unwrap()
            .plan()
            .unwrap();
        assert_eq!(plan.candidates(), &[5]);
        assert_eq!(plan.appended_pages(), 2);
        assert_eq!(plan.pending_page_count(), 22);
        assert_eq!(plan.reserved_pages(), 3);

        let mut cow = plan.into_cow();
        cow.apply_planned_reservation().unwrap();
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.committed_page_count(), 20);
        assert_eq!(cow.pending_page_count(), 22);
        assert_eq!(cow.available_private_pages(), 3);
        assert_eq!(cow.test_page_number_at(0), 5);
        assert_eq!(cow.test_page_number_at(1), 20);
        assert_eq!(cow.test_page_number_at(2), 21);
        assert_eq!(
            cow.test_authorization_at(1),
            PrivatePageAuthorization::Appended
        );
        assert_eq!(
            cow.test_authorization_at(2),
            PrivatePageAuthorization::Appended
        );

        let many = SparsePages::new([leaf(2, 1, &[5, 6, 7])]);
        let mut many_storage = PlannerStorage::new(2, 2, 1, 3);
        let candidate_only =
            FreeBitmapReservationPlanner::new(&many, 1, 20, 2, 1, many_storage.buffers())
                .unwrap()
                .plan()
                .unwrap();
        assert_eq!(candidate_only.candidates(), &[5, 6]);
        assert_eq!(candidate_only.appended_pages(), 0);
        assert_eq!(candidate_only.pending_page_count(), 20);
    }

    #[test]
    fn sparse_candidates_expand_the_exact_distinct_path_union() {
        let source = SparsePages::new([
            branch_many(2, 1, 1, &[(0, 100), (1, 101), (2, 102)]),
            leaf(100, 1, &[5]),
            leaf(101, 1, &[0]),
            leaf(102, 1, &[0]),
        ]);
        let mut storage = PlannerStorage::new(3, 3, 4, 7);
        let plan = FreeBitmapReservationPlanner::new(&source, 1, 64_001, 2, 2, storage.buffers())
            .unwrap()
            .plan()
            .unwrap();
        assert_eq!(plan.candidates(), &[5, 32_000, 64_000]);
        assert_eq!(plan.verified_path_pages(), 4);
        assert_eq!(plan.appended_pages(), 0);
        for pgno in [2, 100, 101, 102] {
            assert_eq!(source.page_reads(pgno), 1);
        }

        let mut cow = plan.into_cow();
        let (result, allocations) = count_thread_allocations(|| cow.apply_planned_reservation());
        assert_eq!(allocations, 0);
        assert_eq!(result, Ok(()));
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.replacements(), &[2, 100, 101, 102]);
        assert_eq!(cow.available_private_pages(), 3);
        for pgno in [2, 100, 101, 102] {
            assert_eq!(source.page_reads(pgno), 1);
        }
    }

    #[test]
    fn planner_budget_minus_one_and_conflicts_fail_before_arena_mutation() {
        let source = SparsePages::new([leaf(2, 1, &[5, 6])]);
        let mut too_small = PlannerStorage::new(1, 2, 1, 3);
        let (result, allocations) = count_thread_allocations(|| {
            FreeBitmapReservationPlanner::new(&source, 1, 20, 2, 1, too_small.buffers())
                .unwrap()
                .plan()
        });
        assert_eq!(allocations, 0);
        assert!(matches!(
            result,
            Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::ArenaPages,
                required: 2,
                available: 1,
            })
        ));

        let multilevel = SparsePages::new([branch(2, 1, 1, 3), leaf(3, 1, &[5, 6, 7])]);
        let mut path_minus_one = PlannerStorage::new(3, 3, 2, 5);
        path_minus_one.replacements.truncate(1);
        let result = FreeBitmapReservationPlanner::new(
            &multilevel,
            1,
            32_001,
            2,
            1,
            path_minus_one.buffers(),
        )
        .unwrap()
        .plan();
        assert!(matches!(
            result,
            Err(FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::ReplacementPages,
                required: 2,
                available: 1,
            })
        ));
        assert!(path_minus_one
            .arena
            .iter()
            .all(|page| page == &ReservedBitmapPage::empty()));

        let self_alias = SparsePages::new([leaf(5, 1, &[5, 6])]);
        let mut conflict = PlannerStorage::new(1, 1, 1, 2);
        let result =
            FreeBitmapReservationPlanner::new(&self_alias, 1, 20, 5, 1, conflict.buffers())
                .unwrap()
                .plan();
        assert!(matches!(
            result,
            Err(FreeBitmapCowError::CandidateIsPathPage(5))
        ));
        assert_eq!(conflict.arena[0], ReservedBitmapPage::empty());

        let duplicate_path = SparsePages::new([
            branch_many(2, 1, 1, &[(0, 100), (1, 100)]),
            leaf(100, 1, &[5]),
        ]);
        let mut duplicate = PlannerStorage::new(3, 3, 3, 6);
        let result = FreeBitmapReservationPlanner::new(
            &duplicate_path,
            1,
            64_000,
            2,
            2,
            duplicate.buffers(),
        )
        .unwrap()
        .plan();
        assert!(matches!(
            result,
            Err(FreeBitmapCowError::RepeatedCommittedPage(100))
        ));
        assert!(duplicate
            .arena
            .iter()
            .all(|page| page == &ReservedBitmapPage::empty()));

        for forbidden in [0, 1] {
            let invalid = SparsePages::new([leaf(2, 1, &[forbidden, 5])]);
            let mut invalid_storage = PlannerStorage::new(2, 2, 1, 3);
            let (result, allocations) = count_thread_allocations(|| {
                FreeBitmapReservationPlanner::new(&invalid, 1, 20, 2, 1, invalid_storage.buffers())
                    .unwrap()
                    .plan()
            });
            assert_eq!(allocations, 0);
            assert!(matches!(
                result,
                Err(FreeBitmapCowError::Page {
                    pgno: 2,
                    cause: BitmapPageError::BitOutsideLimit,
                })
            ));
            assert!(invalid_storage
                .arena
                .iter()
                .all(|page| page == &ReservedBitmapPage::empty()));
        }
    }

    #[test]
    fn planner_handles_u32_page_space_and_bitmap_level_boundaries() {
        let near_max = SparsePages::new([]);
        let mut last_page = PlannerStorage::new(1, 0, 0, 1);
        let plan = FreeBitmapReservationPlanner::new(
            &near_max,
            1,
            MAX_PAGE_COUNT - 1,
            0,
            1,
            last_page.buffers(),
        )
        .unwrap()
        .plan()
        .unwrap();
        assert_eq!(plan.pending_page_count(), MAX_PAGE_COUNT);
        assert_eq!(plan.appended_pages(), 1);
        let mut cow = plan.into_cow();
        cow.apply_planned_reservation().unwrap();
        assert_eq!(cow.test_page_number_at(0), u32::MAX);

        let mut exhausted = PlannerStorage::new(1, 0, 0, 1);
        let result = FreeBitmapReservationPlanner::new(
            &near_max,
            1,
            MAX_PAGE_COUNT,
            0,
            1,
            exhausted.buffers(),
        )
        .unwrap()
        .plan();
        assert!(matches!(
            result,
            Err(FreeBitmapCowError::PageSpaceExhausted)
        ));
        assert_eq!(exhausted.arena[0], ReservedBitmapPage::empty());

        let below = SparsePages::new([leaf(2, 1, &[31_998])]);
        let mut below_storage = PlannerStorage::new(1, 1, 1, 2);
        let below_plan =
            FreeBitmapReservationPlanner::new(&below, 1, 31_999, 2, 1, below_storage.buffers())
                .unwrap()
                .plan()
                .unwrap();
        assert_eq!(below_plan.candidates(), &[31_998]);
        assert_eq!(below_plan.verified_path_pages(), 1);

        let exact = SparsePages::new([leaf(2, 1, &[31_999])]);
        let mut exact_storage = PlannerStorage::new(1, 1, 1, 2);
        let exact_plan =
            FreeBitmapReservationPlanner::new(&exact, 1, 32_000, 2, 1, exact_storage.buffers())
                .unwrap()
                .plan()
                .unwrap();
        assert_eq!(exact_plan.candidates(), &[31_999]);
        assert_eq!(exact_plan.verified_path_pages(), 1);

        let above = SparsePages::new([branch_at(2, 1, 1, 1, 100), leaf(100, 1, &[0])]);
        let mut above_storage = PlannerStorage::new(1, 1, 2, 3);
        let above_plan =
            FreeBitmapReservationPlanner::new(&above, 1, 32_001, 2, 1, above_storage.buffers())
                .unwrap()
                .plan()
                .unwrap();
        assert_eq!(above_plan.candidates(), &[32_000]);
        assert_eq!(above_plan.verified_path_pages(), 2);
    }

    #[test]
    fn planner_and_planned_apply_scale_without_heap_allocation() {
        fn run(payload_pages: usize) -> usize {
            let candidate_count = payload_pages + 1;
            let bits: Vec<u32> = (3..u32::try_from(candidate_count + 4).unwrap()).collect();
            let source = SparsePages::new([leaf(2, 1, &bits)]);
            let mut storage =
                PlannerStorage::new(candidate_count, candidate_count, 1, candidate_count + 1);
            let (plan, plan_allocations) = count_thread_allocations(|| {
                FreeBitmapReservationPlanner::new(
                    &source,
                    1,
                    BITMAP_LEAF_BITS,
                    2,
                    payload_pages,
                    storage.buffers(),
                )
                .unwrap()
                .plan()
                .unwrap()
            });
            assert_eq!(plan_allocations, 0);
            assert_eq!(plan.candidates().len(), candidate_count);
            assert_eq!(source.page_reads(2), 1);
            let mut cow = plan.into_cow();
            let (result, apply_allocations) =
                count_thread_allocations(|| cow.apply_planned_reservation());
            assert_eq!(apply_allocations, 0);
            assert_eq!(result, Ok(()));
            assert_eq!(cow.available_private_pages(), payload_pages);
            assert_eq!(source.page_reads(2), 1);
            cow.index_probe_count()
        }

        let probes = [run(512), run(1_024), run(2_048)];
        for pair in probes.windows(2) {
            assert!(pair[1] < pair[0] * 3);
        }
    }

    #[test]
    fn canonical_cow_encoding_and_verified_once_private_path() {
        let source = SparsePages::new([branch(2, 1, 1, 3), leaf(3, 1, &[5, 6])]);
        let mut arena = [ReservedBitmapPage::new(10), ReservedBitmapPage::new(11)];
        let mut replacements = [0u32; 4];
        let mut candidates = [0u32; 4];
        let mut index = [BitmapCowIndexNode::empty(); 6];
        let mut available = [0usize; 2];
        let ledger = FreeBitmapCowLedger::empty(
            &mut arena,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow = FreeBitmapCow::new(&source, 1, 32_001, 2, ledger).unwrap();

        let reserved = cow.remove_lowest().unwrap().unwrap();
        assert_eq!(reserved.page_number(), 5);
        assert_eq!(cow.root(), 10);
        assert_eq!(cow.replacements(), &[2, 3]);
        assert_eq!(cow.candidates(), &[5]);
        let reads_after_first = source.reads.get();

        let root = cow.private_page(10).unwrap();
        let root_header = header(&root, 2);
        assert_eq!(root_header.page_type, PageType::BitmapBranch);
        assert_eq!(root_header.born_txn, 2);
        assert_eq!(root_header.item_count, 1);
        assert_eq!(root_header.level, 1);
        assert_eq!(root_header.aux, BitmapKind::FreePages as u32);
        assert_eq!(u64::from_le_bytes(root[32..40].try_into().unwrap()), 1);
        assert_eq!(u32::from_le_bytes(root[64..68].try_into().unwrap()), 11);
        assert!(root[usize::from(BRANCH_LOWER)..]
            .iter()
            .all(|&byte| byte == 0));
        assert!(page::verify_crc32c(&root));
        assert_eq!(&*root, &branch(10, 2, 1, 11).bytes);
        drop(root);

        let private_leaf = cow.private_page(11).unwrap();
        let leaf_header = header(&private_leaf, 2);
        assert_eq!(leaf_header.page_type, PageType::BitmapLeaf);
        assert_eq!(leaf_header.born_txn, 2);
        assert_eq!(leaf_header.item_count, 1);
        assert_eq!(
            u64::from_le_bytes(private_leaf[32..40].try_into().unwrap()),
            1 << 6
        );
        assert!(private_leaf[usize::from(LEAF_LOWER)..]
            .iter()
            .all(|&byte| byte == 0));
        assert!(page::verify_crc32c(&private_leaf));
        assert_eq!(&*private_leaf, &leaf(11, 2, &[6]).bytes);
        drop(private_leaf);

        assert_eq!(cow.remove_lowest().unwrap().unwrap().page_number(), 6);
        assert_eq!(source.reads.get(), reads_after_first);
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.replacements(), &[2, 3]);
        assert_eq!(cow.candidates(), &[5, 6]);
        assert_eq!(cow.available_private_pages(), 2);
    }

    #[test]
    fn surviving_sibling_switches_path_without_rereading_private_root() {
        let source = SparsePages::new([
            branch_many(2, 1, 1, &[(0, 3), (1, 4)]),
            leaf(3, 1, &[5]),
            leaf(4, 1, &[0]),
        ]);
        let mut arena = [ReservedBitmapPage::new(10)];
        let mut replacements = [0u32; 3];
        let mut candidates = [0u32; 2];
        let mut index = [BitmapCowIndexNode::empty(); 4];
        let mut available = [0usize; 1];
        let ledger = FreeBitmapCowLedger::empty(
            &mut arena,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow = FreeBitmapCow::new(&source, 1, 32_001, 2, ledger).unwrap();

        assert_eq!(cow.remove_lowest().unwrap().unwrap().page_number(), 5);
        assert_eq!(cow.root(), 10);
        assert_eq!(cow.replacements(), &[2, 3]);
        assert_eq!(cow.candidates(), &[5]);
        assert_eq!(source.page_reads(2), 1);
        assert_eq!(source.page_reads(3), 1);
        assert_eq!(source.page_reads(4), 0);
        assert_eq!(
            &*cow.private_page(10).unwrap(),
            &branch_at(10, 2, 1, 1, 4).bytes
        );

        assert_eq!(cow.remove_lowest().unwrap().unwrap().page_number(), 32_000);
        assert_eq!(source.page_reads(2), 1);
        assert_eq!(source.page_reads(3), 1);
        assert_eq!(source.page_reads(4), 1);
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.replacements(), &[2, 3, 4]);
        assert_eq!(cow.candidates(), &[5, 32_000]);
        assert_eq!(cow.available_private_pages(), 1);
    }

    #[test]
    fn nonzero_leaf_base_uses_leaf_local_bit_offsets() {
        let source = SparsePages::new([branch_at(2, 1, 1, 1, 3), leaf(3, 1, &[0, 1])]);
        let mut arena = [ReservedBitmapPage::new(10), ReservedBitmapPage::new(11)];
        let mut replacements = [0u32; 2];
        let mut candidates = [0u32; 1];
        let mut index = [BitmapCowIndexNode::empty(); 4];
        let mut available = [0usize; 2];
        let ledger = FreeBitmapCowLedger::empty(
            &mut arena,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow = FreeBitmapCow::new(&source, 1, 32_002, 2, ledger).unwrap();

        assert_eq!(cow.remove_lowest().unwrap().unwrap().page_number(), 32_000);
        let root = cow.private_page(10).unwrap();
        assert_eq!(u64::from_le_bytes(root[32..40].try_into().unwrap()), 2);
        assert_eq!(u32::from_le_bytes(root[68..72].try_into().unwrap()), 11);
        assert!(page::verify_crc32c(&root));
        assert_eq!(&*root, &branch_at(10, 2, 1, 1, 11).bytes);
        drop(root);
        let private_leaf = cow.private_page(11).unwrap();
        assert_eq!(
            u64::from_le_bytes(private_leaf[32..40].try_into().unwrap()),
            2
        );
        assert!(page::verify_crc32c(&private_leaf));
        assert_eq!(&*private_leaf, &leaf(11, 2, &[1]).bytes);
    }

    #[test]
    fn last_bit_collapses_committed_multilevel_path_without_private_pages() {
        let source = SparsePages::new([branch(2, 1, 1, 3), leaf(3, 1, &[5])]);
        let mut arena = [];
        let mut replacements = [0u32; 2];
        let mut candidates = [0u32; 1];
        let mut index = [BitmapCowIndexNode::empty(); 2];
        let mut available = [];
        let ledger = FreeBitmapCowLedger::empty(
            &mut arena,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow = FreeBitmapCow::new(&source, 1, 32_001, 2, ledger).unwrap();

        assert_eq!(cow.remove_lowest().unwrap().unwrap().page_number(), 5);
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.replacements(), &[2, 3]);
        assert_eq!(cow.candidates(), &[5]);
        assert_eq!(cow.available_private_pages(), 0);
    }

    #[test]
    fn committed_corruption_aborts_before_any_draft_mutation() {
        let mut corrupt = leaf(2, 1, &[5, 6]);
        corrupt.bytes[100] ^= 1;
        let source = SparsePages::new([corrupt]);
        let mut arena = [ReservedBitmapPage::new(10)];
        let pristine = ReservedBitmapPage::new(10);
        let mut replacements = [0u32; 1];
        let mut candidates = [0u32; 1];
        let mut index = [BitmapCowIndexNode::empty(); 2];
        let mut available = [0usize; 1];
        let ledger = FreeBitmapCowLedger::empty(
            &mut arena,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow = FreeBitmapCow::new(&source, 1, 20, 2, ledger).unwrap();

        let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
        assert_eq!(allocations, 0);
        assert_eq!(
            result,
            Err(FreeBitmapCowError::Page {
                pgno: 2,
                cause: BitmapPageError::Checksum,
            })
        );
        assert_eq!(cow.root(), 2);
        assert!(cow.replacements().is_empty());
        assert!(cow.candidates().is_empty());
        assert_eq!(cow.test_page_at(0), *pristine.initial_bytes());
        assert_eq!(cow.test_state_at(0), PrivatePageState::Available);
    }

    #[test]
    fn source_io_and_access_failures_are_not_erased_or_allocated() {
        fn run(source: &FailingPages, expected: PageSourceError) {
            let mut arena = [ReservedBitmapPage::new(10)];
            let mut replacements = [0u32; 1];
            let mut candidates = [0u32; 1];
            let mut index = [BitmapCowIndexNode::empty(); 2];
            let mut available = [0usize; 1];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena,
                &mut replacements,
                &mut candidates,
                &mut index,
                &mut available,
            );
            let mut cow = FreeBitmapCow::new(source, 1, 20, 2, ledger).unwrap();
            let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
            assert_eq!(result, Err(FreeBitmapCowError::Source(expected)));
            assert_eq!(allocations, 0);
            assert!(cow.replacements().is_empty());
            assert!(cow.candidates().is_empty());
        }

        run(
            &FailingPages {
                access: Some(PageSourceError::ForkedHandle),
                read: None,
            },
            PageSourceError::ForkedHandle,
        );
        let io = PageSourceError::Io(crate::page_source::PageIoEvidence {
            kind: crate::page_source::PageIoKind::PermissionDenied,
            raw_os_error: Some(13),
        });
        run(
            &FailingPages {
                access: None,
                read: Some(io),
            },
            io,
        );
    }

    #[test]
    fn capacity_failures_are_atomic() {
        fn run(
            arena_capacity: usize,
            replacement_capacity: usize,
            candidate_capacity: usize,
        ) -> FreeBitmapCowError {
            let source = SparsePages::new([leaf(2, 1, &[5, 6])]);
            let mut arena_storage = [ReservedBitmapPage::new(10)];
            let mut replacement_storage = [0u32; 1];
            let mut candidate_storage = [0u32; 1];
            let mut index_storage = [BitmapCowIndexNode::empty(); 2];
            let mut available_storage = [0usize; 1];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena_storage[..arena_capacity],
                &mut replacement_storage[..replacement_capacity],
                &mut candidate_storage[..candidate_capacity],
                &mut index_storage[..arena_capacity + replacement_capacity],
                &mut available_storage[..arena_capacity],
            );
            let mut cow = FreeBitmapCow::new(&source, 1, 20, 2, ledger).unwrap();
            let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
            assert_eq!(allocations, 0);
            let error = result.unwrap_err();
            assert_eq!(cow.root(), 2);
            assert!(cow.replacements().is_empty());
            assert!(cow.candidates().is_empty());
            if arena_capacity != 0 {
                assert_eq!(cow.test_page_number_at(0), 10);
                assert_eq!(cow.test_page_at(0), [0; PAGE_SIZE]);
                assert_eq!(cow.test_state_at(0), PrivatePageState::Available);
            }
            error
        }

        assert_eq!(run(0, 1, 1), FreeBitmapCowError::PrivateArenaExhausted);
        assert_eq!(run(1, 0, 1), FreeBitmapCowError::ReplacementLedgerExhausted);
        assert_eq!(run(1, 1, 0), FreeBitmapCowError::CandidateLedgerExhausted);
    }

    #[test]
    fn every_insufficient_multilevel_clone_and_replacement_capacity_is_atomic() {
        fn run(arena_capacity: usize, replacement_capacity: usize) -> FreeBitmapCowError {
            let source =
                SparsePages::new([branch(2, 1, 2, 3), branch(3, 1, 1, 4), leaf(4, 1, &[5, 6])]);
            let mut arena_storage = [
                ReservedBitmapPage::new(10),
                ReservedBitmapPage::new(11),
                ReservedBitmapPage::new(12),
            ];
            let mut replacement_storage = [0u32; 3];
            let mut candidate_storage = [0u32; 1];
            let mut index_storage = [BitmapCowIndexNode::empty(); 6];
            let mut available_storage = [0usize; 3];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena_storage[..arena_capacity],
                &mut replacement_storage[..replacement_capacity],
                &mut candidate_storage,
                &mut index_storage[..arena_capacity + replacement_capacity],
                &mut available_storage[..arena_capacity],
            );
            let mut cow =
                FreeBitmapCow::new(&source, 1, coverage(1).unwrap() + 1, 2, ledger).unwrap();

            let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
            assert_eq!(allocations, 0);
            let error = result.unwrap_err();
            assert_eq!(cow.root(), 2);
            assert!(cow.replacements().is_empty());
            assert!(cow.candidates().is_empty());
            assert_eq!(cow.available_private_pages(), arena_capacity);
            for index in 0..cow.pool().len() {
                assert_eq!(cow.test_page_number_at(index), 10 + index as u32);
                assert_eq!(cow.test_page_at(index), [0; PAGE_SIZE]);
                assert_eq!(cow.test_state_at(index), PrivatePageState::Available);
            }
            error
        }

        for arena_capacity in 0..3 {
            assert_eq!(
                run(arena_capacity, 3),
                FreeBitmapCowError::PrivateArenaExhausted
            );
        }
        for replacement_capacity in 0..3 {
            assert_eq!(
                run(3, replacement_capacity),
                FreeBitmapCowError::ReplacementLedgerExhausted
            );
        }
    }

    #[test]
    fn maximum_page_count_removes_u32_max_through_sparse_level_three_path() {
        let candidate = u64::from(u32::MAX);
        let level_two_span = coverage(2).unwrap();
        let level_one_span = coverage(1).unwrap();
        let leaf_span = coverage(0).unwrap();
        let root_index = usize::try_from(candidate / level_two_span).unwrap();
        let after_root = candidate % level_two_span;
        let level_two_index = usize::try_from(after_root / level_one_span).unwrap();
        let after_level_two = after_root % level_one_span;
        let level_one_index = usize::try_from(after_level_two / leaf_span).unwrap();
        let leaf_bit = u32::try_from(after_level_two % leaf_span).unwrap();
        assert_eq!(
            (root_index, level_two_index, level_one_index, leaf_bit),
            (2, 12, 73, 23_295)
        );

        let source = SparsePages::new([
            branch_at(2, 1, 3, root_index, 3),
            branch_at(3, 1, 2, level_two_index, 4),
            branch_at(4, 1, 1, level_one_index, 5),
            leaf(5, 1, &[leaf_bit]),
        ]);
        let mut arena = [];
        let mut replacements = [0u32; FREE_PATH_CAPACITY];
        let mut candidates = [0u32; 1];
        let mut index = [BitmapCowIndexNode::empty(); FREE_PATH_CAPACITY];
        let mut available = [];
        let ledger = FreeBitmapCowLedger::empty(
            &mut arena,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow = FreeBitmapCow::new(&source, 1, MAX_PAGE_COUNT, 2, ledger).unwrap();

        assert_eq!(
            cow.remove_lowest().unwrap().unwrap().page_number(),
            u32::MAX
        );
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.replacements(), &[2, 3, 4, 5]);
        assert_eq!(cow.candidates(), &[u32::MAX]);
        for pgno in 2..=5 {
            assert_eq!(source.page_reads(pgno), 1);
        }
    }

    #[test]
    fn multilevel_removal_allocates_nothing_dynamically() {
        let source =
            SparsePages::new([branch(2, 1, 2, 3), branch(3, 1, 1, 4), leaf(4, 1, &[5, 6])]);
        let mut arena = [
            ReservedBitmapPage::new(10),
            ReservedBitmapPage::new(11),
            ReservedBitmapPage::new(12),
        ];
        let mut replacements = [0u32; 3];
        let mut candidates = [0u32; 1];
        let mut index = [BitmapCowIndexNode::empty(); 6];
        let mut available = [0usize; 3];
        let ledger = FreeBitmapCowLedger::empty(
            &mut arena,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow = FreeBitmapCow::new(&source, 1, coverage(1).unwrap() + 1, 2, ledger).unwrap();

        let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());

        assert_eq!(result.unwrap().unwrap().page_number(), 5);
        assert_eq!(allocations, 0);
        assert_eq!(cow.root(), 10);
        assert_eq!(cow.replacements(), &[2, 3, 4]);
        assert_eq!(cow.candidates(), &[5]);
    }

    #[test]
    fn duplicate_and_self_candidates_fail_before_mutation() {
        let duplicate_source = SparsePages::new([leaf(2, 1, &[5, 6])]);
        let mut arena = [ReservedBitmapPage::new(10)];
        let mut replacements = [0u32; 1];
        let mut candidates = [5u32, 0];
        let mut index = [BitmapCowIndexNode::empty(); 2];
        let mut available = [0usize; 1];
        let ledger = FreeBitmapCowLedger::with_prefixes(
            &mut arena,
            &mut replacements,
            0,
            &mut candidates,
            1,
            &mut index,
            &mut available,
        );
        let mut duplicate = FreeBitmapCow::new(&duplicate_source, 1, 20, 2, ledger).unwrap();
        let (result, allocations) = count_thread_allocations(|| duplicate.remove_lowest());
        assert_eq!(allocations, 0);
        assert_eq!(result, Err(FreeBitmapCowError::CandidateAlreadyReserved(5)));
        assert_eq!(duplicate.root(), 2);
        assert!(duplicate.replacements().is_empty());
        assert_eq!(duplicate.candidates(), &[5]);

        let self_source = SparsePages::new([leaf(2, 1, &[2])]);
        let mut arena = [];
        let mut replacements = [0u32; 1];
        let mut candidates = [0u32; 1];
        let mut index = [BitmapCowIndexNode::empty(); 1];
        let mut available = [];
        let ledger = FreeBitmapCowLedger::empty(
            &mut arena,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut self_candidate = FreeBitmapCow::new(&self_source, 1, 20, 2, ledger).unwrap();
        let (result, allocations) = count_thread_allocations(|| self_candidate.remove_lowest());
        assert_eq!(allocations, 0);
        assert_eq!(result, Err(FreeBitmapCowError::CandidateIsPathPage(2)));
        assert_eq!(self_candidate.root(), 2);
        assert!(self_candidate.replacements().is_empty());
        assert!(self_candidate.candidates().is_empty());
    }

    #[test]
    fn lowest_bit_order_crosses_word_63_64_without_allocating() {
        let source = SparsePages::new([leaf(2, 1, &[63, 64, 65])]);
        let mut arena = [ReservedBitmapPage::new(10)];
        let mut replacements = [0u32; 1];
        let mut candidates = [0u32; 3];
        let mut index = [BitmapCowIndexNode::empty(); 2];
        let mut available = [0usize; 1];
        let ledger = FreeBitmapCowLedger::empty(
            &mut arena,
            &mut replacements,
            &mut candidates,
            &mut index,
            &mut available,
        );
        let mut cow = FreeBitmapCow::new(&source, 1, 100, 2, ledger).unwrap();

        for expected in [63, 64, 65] {
            let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
            assert_eq!(allocations, 0);
            assert_eq!(result.unwrap().unwrap().page_number(), expected);
        }
        assert_eq!(cow.root(), 0);
        assert_eq!(cow.candidates(), &[63, 64, 65]);
    }

    #[test]
    fn committed_free_bits_zero_and_one_are_rejected_without_mutation_or_allocation() {
        for bad_bit in [0, 1] {
            let source = SparsePages::new([leaf(2, 1, &[bad_bit, 5])]);
            let mut arena = [ReservedBitmapPage::new(10)];
            let mut replacements = [0u32; 1];
            let mut candidates = [0u32; 1];
            let mut index = [BitmapCowIndexNode::empty(); 2];
            let mut available = [0usize; 1];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena,
                &mut replacements,
                &mut candidates,
                &mut index,
                &mut available,
            );
            let mut cow = FreeBitmapCow::new(&source, 1, 20, 2, ledger).unwrap();

            let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
            assert_eq!(allocations, 0);
            assert_eq!(
                result,
                Err(FreeBitmapCowError::Page {
                    pgno: 2,
                    cause: BitmapPageError::BitOutsideLimit,
                })
            );
            assert_eq!(cow.root(), 2);
            assert!(cow.replacements().is_empty());
            assert!(cow.candidates().is_empty());
            assert_eq!(cow.available_private_pages(), 1);
        }
    }

    #[test]
    fn committed_corruption_matrix_is_atomic_and_allocation_free() {
        fn assert_error(page: SparsePage, page_count: u64, expected: FreeBitmapCowError) {
            let source = SparsePages::new([page]);
            let mut arena = [ReservedBitmapPage::new(10)];
            let pristine = ReservedBitmapPage::new(10);
            let mut replacements = [0u32; 1];
            let mut candidates = [0u32; 1];
            let mut index = [BitmapCowIndexNode::empty(); 2];
            let mut available = [0usize; 1];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena,
                &mut replacements,
                &mut candidates,
                &mut index,
                &mut available,
            );
            let mut cow = FreeBitmapCow::new(&source, 1, page_count, 2, ledger).unwrap();

            let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
            assert_eq!(allocations, 0);
            assert_eq!(result, Err(expected));
            assert_eq!(cow.root(), 2);
            assert!(cow.replacements().is_empty());
            assert!(cow.candidates().is_empty());
            assert_eq!(cow.test_page_at(0), *pristine.initial_bytes());
            assert_eq!(cow.test_state_at(0), PrivatePageState::Available);
        }

        let mut checksum = leaf(2, 1, &[5]);
        checksum.bytes[100] ^= 1;
        assert_error(
            checksum,
            20,
            FreeBitmapCowError::Page {
                pgno: 2,
                cause: BitmapPageError::Checksum,
            },
        );

        let mut reserved = leaf(2, 1, &[5]);
        reserved.bytes[usize::from(LEAF_LOWER)] = 1;
        page::write_crc32c(&mut reserved.bytes);
        assert_error(
            reserved,
            20,
            FreeBitmapCowError::Page {
                pgno: 2,
                cause: BitmapPageError::ReservedNonzero,
            },
        );

        let mut wrong_count = leaf(2, 1, &[5]);
        let mut wrong_count_header = PageHeader::decode(&wrong_count.bytes, 1).unwrap();
        wrong_count_header.item_count = 2;
        wrong_count_header.encode_into(&mut wrong_count.bytes);
        page::write_crc32c(&mut wrong_count.bytes);
        assert_error(
            wrong_count,
            20,
            FreeBitmapCowError::Page {
                pgno: 2,
                cause: BitmapPageError::ItemCountMismatch,
            },
        );

        let mut wrong_kind = leaf(2, 1, &[5]);
        let mut wrong_kind_header = PageHeader::decode(&wrong_kind.bytes, 1).unwrap();
        wrong_kind_header.aux = BitmapKind::FeedUsed as u32;
        wrong_kind_header.encode_into(&mut wrong_kind.bytes);
        page::write_crc32c(&mut wrong_kind.bytes);
        assert_error(
            wrong_kind,
            20,
            FreeBitmapCowError::Page {
                pgno: 2,
                cause: BitmapPageError::WrongKind(BitmapKind::FeedUsed as u32),
            },
        );

        assert_error(
            branch(2, 1, 1, 1),
            32_001,
            FreeBitmapCowError::Page {
                pgno: 2,
                cause: BitmapPageError::ChildPageOutOfBounds(1),
            },
        );
    }

    #[test]
    fn constructor_preparation_guards_are_allocation_free() {
        let source = SparsePages::new([]);

        fn assert_early_error(
            selected_txn: u64,
            page_count: u64,
            root: u32,
            replacement_len: usize,
            candidate_len: usize,
            expected: FreeBitmapCowError,
        ) {
            let source = SparsePages::new([]);
            let mut arena = [];
            let mut replacements = [0u32; 1];
            let mut candidates = [0u32; 1];
            let mut index = [BitmapCowIndexNode::empty(); 1];
            let mut available = [];
            let ledger = FreeBitmapCowLedger::with_prefixes(
                &mut arena,
                &mut replacements,
                replacement_len,
                &mut candidates,
                candidate_len,
                &mut index,
                &mut available,
            );
            let (result, allocations) = count_thread_allocations(|| {
                FreeBitmapCow::new(&source, selected_txn, page_count, root, ledger)
            });
            assert_eq!(allocations, 0);
            assert_eq!(result.unwrap_err(), expected);
        }

        assert_early_error(0, 20, 0, 0, 0, FreeBitmapCowError::SelectedTransactionZero);
        assert_early_error(
            u64::MAX,
            20,
            0,
            0,
            0,
            FreeBitmapCowError::TransactionExhausted,
        );
        assert_early_error(1, 1, 0, 0, 0, FreeBitmapCowError::PageCountOutOfRange(1));
        assert_early_error(
            1,
            MAX_PAGE_COUNT + 1,
            0,
            0,
            0,
            FreeBitmapCowError::PageCountOutOfRange(MAX_PAGE_COUNT + 1),
        );
        assert_early_error(1, 20, 1, 0, 0, FreeBitmapCowError::RootPageOutOfBounds(1));
        assert_early_error(1, 20, 20, 0, 0, FreeBitmapCowError::RootPageOutOfBounds(20));
        assert_early_error(1, 20, 0, 2, 0, FreeBitmapCowError::LedgerPrefixOutOfBounds);
        assert_early_error(1, 20, 0, 0, 2, FreeBitmapCowError::LedgerPrefixOutOfBounds);

        {
            let mut arena = [ReservedBitmapPage::new(10)];
            let mut replacements = [0u32; 1];
            let mut candidates = [0u32; 1];
            let mut index = [BitmapCowIndexNode::empty(); 2];
            let mut available = [0usize; 1];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena,
                &mut replacements,
                &mut candidates,
                &mut index,
                &mut available,
            );
            let (result, allocations) =
                count_thread_allocations(|| FreeBitmapCow::new(&source, 1, 20, 0, ledger));
            assert_eq!(allocations, 0);
            assert!(result.is_ok());
        }

        for invalid_pgno in [0, 1, 20] {
            let mut arena = [ReservedBitmapPage::new(invalid_pgno)];
            let mut replacements = [];
            let mut candidates = [];
            let mut index = [BitmapCowIndexNode::empty(); 1];
            let mut available = [0usize; 1];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena,
                &mut replacements,
                &mut candidates,
                &mut index,
                &mut available,
            );
            let (result, allocations) =
                count_thread_allocations(|| FreeBitmapCow::new(&source, 1, 20, 0, ledger));
            assert_eq!(allocations, 0);
            assert!(matches!(
                result,
                Err(FreeBitmapCowError::LedgerPageOutOfBounds(pgno)) if pgno == invalid_pgno
            ));
        }

        {
            let mut arena = [ReservedBitmapPage::new(10)];
            let mut replacements = [0u32; 1];
            let mut candidates = [0u32; 1];
            let mut index = [BitmapCowIndexNode::empty(); 1];
            let mut available = [0usize; 1];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena,
                &mut replacements,
                &mut candidates,
                &mut index,
                &mut available,
            );
            let (result, allocations) =
                count_thread_allocations(|| FreeBitmapCow::new(&source, 1, 20, 0, ledger));
            assert_eq!(allocations, 0);
            assert!(matches!(
                result,
                Err(FreeBitmapCowError::IndexCapacityTooSmall {
                    required: 2,
                    actual: 1
                })
            ));
        }

        {
            let mut arena = [ReservedBitmapPage::new(10)];
            let mut replacements = [];
            let mut candidates = [];
            let mut index = [BitmapCowIndexNode::empty(); 1];
            let mut available = [];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena,
                &mut replacements,
                &mut candidates,
                &mut index,
                &mut available,
            );
            let (result, allocations) =
                count_thread_allocations(|| FreeBitmapCow::new(&source, 1, 20, 0, ledger));
            assert_eq!(allocations, 0);
            assert!(matches!(
                result,
                Err(FreeBitmapCowError::AvailableSlotCapacityTooSmall {
                    required: 1,
                    actual: 0
                })
            ));
        }

        {
            let mut arena = [ReservedBitmapPage::new(10), ReservedBitmapPage::new(10)];
            let mut replacements = [];
            let mut candidates = [];
            let mut index = [BitmapCowIndexNode::empty(); 2];
            let mut available = [0usize; 2];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena,
                &mut replacements,
                &mut candidates,
                &mut index,
                &mut available,
            );
            let (result, allocations) =
                count_thread_allocations(|| FreeBitmapCow::new(&source, 1, 20, 0, ledger));
            assert_eq!(allocations, 0);
            assert!(matches!(
                result,
                Err(FreeBitmapCowError::DuplicateArenaPage(10))
            ));
        }

        {
            let mut arena = [];
            let mut replacements = [5u32, 5];
            let mut candidates = [];
            let mut index = [BitmapCowIndexNode::empty(); 2];
            let mut available = [];
            let ledger = FreeBitmapCowLedger::with_prefixes(
                &mut arena,
                &mut replacements,
                2,
                &mut candidates,
                0,
                &mut index,
                &mut available,
            );
            let (result, allocations) =
                count_thread_allocations(|| FreeBitmapCow::new(&source, 1, 20, 0, ledger));
            assert_eq!(allocations, 0);
            assert!(matches!(
                result,
                Err(FreeBitmapCowError::DuplicateReplacement(5))
            ));
        }

        for (candidates_prefix, expected) in [
            ([5u32, 5], FreeBitmapCowError::DuplicateCandidate(5)),
            (
                [6u32, 5],
                FreeBitmapCowError::CandidateOrderRegression {
                    previous: 6,
                    current: 5,
                },
            ),
        ] {
            let mut arena = [];
            let mut replacements = [];
            let mut candidates = candidates_prefix;
            let mut index = [];
            let mut available = [];
            let ledger = FreeBitmapCowLedger::with_prefixes(
                &mut arena,
                &mut replacements,
                0,
                &mut candidates,
                2,
                &mut index,
                &mut available,
            );
            let (result, allocations) =
                count_thread_allocations(|| FreeBitmapCow::new(&source, 1, 20, 0, ledger));
            assert_eq!(allocations, 0);
            assert_eq!(result.unwrap_err(), expected);
        }

        {
            let mut arena = [ReservedBitmapPage::new(10)];
            let mut replacements = [];
            let mut candidates = [10u32];
            let mut index = [BitmapCowIndexNode::empty(); 1];
            let mut available = [0usize; 1];
            let ledger = FreeBitmapCowLedger::with_prefixes(
                &mut arena,
                &mut replacements,
                0,
                &mut candidates,
                1,
                &mut index,
                &mut available,
            );
            let (result, allocations) =
                count_thread_allocations(|| FreeBitmapCow::new(&source, 1, 20, 0, ledger));
            assert_eq!(allocations, 0);
            assert!(matches!(
                result,
                Err(FreeBitmapCowError::LedgerPageConflict(10))
            ));
        }
    }

    #[test]
    fn indexed_conflict_errors_are_atomic_and_allocation_free() {
        fn assert_remove_error(
            arena_pgno: Option<u32>,
            replacement_prefix: Option<u32>,
            candidate_prefix: Option<u32>,
            free_bit: u32,
            expected: FreeBitmapCowError,
        ) {
            let source = SparsePages::new([leaf(2, 1, &[free_bit])]);
            let mut arena_storage = [ReservedBitmapPage::new(arena_pgno.unwrap_or(10))];
            let mut replacement_storage = [replacement_prefix.unwrap_or(0)];
            let mut candidate_storage = [candidate_prefix.unwrap_or(0), 0];
            let arena_len = usize::from(arena_pgno.is_some());
            let replacement_len = usize::from(replacement_prefix.is_some());
            let candidate_len = usize::from(candidate_prefix.is_some());
            let mut index = [BitmapCowIndexNode::empty(); 2];
            let mut available = [0usize; 1];
            let ledger = FreeBitmapCowLedger::with_prefixes(
                &mut arena_storage[..arena_len],
                &mut replacement_storage,
                replacement_len,
                &mut candidate_storage,
                candidate_len,
                &mut index,
                &mut available[..arena_len],
            );
            let mut cow = FreeBitmapCow::new(&source, 1, 20, 2, ledger).unwrap();

            let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
            assert_eq!(allocations, 0);
            assert_eq!(result, Err(expected));
            assert_eq!(cow.root(), 2);
            assert_eq!(cow.replacement_len, replacement_len);
            assert_eq!(cow.candidate_len, candidate_len);
        }

        assert_remove_error(
            Some(5),
            None,
            None,
            5,
            FreeBitmapCowError::CandidateIsArenaPage(5),
        );
        assert_remove_error(
            None,
            Some(5),
            None,
            5,
            FreeBitmapCowError::CandidateIsDraftReplacement(5),
        );
        assert_remove_error(
            None,
            None,
            Some(6),
            5,
            FreeBitmapCowError::CandidateOrderRegression {
                previous: 6,
                current: 5,
            },
        );
        assert_remove_error(
            Some(2),
            None,
            None,
            5,
            FreeBitmapCowError::ArenaPageConflict(2),
        );
        assert_remove_error(
            None,
            Some(2),
            None,
            5,
            FreeBitmapCowError::RepeatedCommittedPage(2),
        );
    }

    #[test]
    fn path_selection_error_classes_are_atomic_and_allocation_free() {
        fn assert_committed_error<const N: usize>(
            source: SparsePages<N>,
            page_count: u64,
            expected: FreeBitmapCowError,
        ) {
            let mut arena = [ReservedBitmapPage::new(19)];
            let mut replacements = [0u32; 1];
            let mut candidates = [0u32; 1];
            let mut index = [BitmapCowIndexNode::empty(); 2];
            let mut available = [0usize; 1];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena,
                &mut replacements,
                &mut candidates,
                &mut index,
                &mut available,
            );
            let mut cow = FreeBitmapCow::new(&source, 1, page_count, 2, ledger).unwrap();
            let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
            assert_eq!(allocations, 0);
            assert_eq!(result, Err(expected));
            assert_eq!(cow.root(), 2);
            assert!(cow.replacements().is_empty());
            assert!(cow.candidates().is_empty());
            assert_eq!(cow.available_private_pages(), 1);
        }

        assert_committed_error(
            SparsePages::new([]),
            20,
            FreeBitmapCowError::Source(PageSourceError::PageOutOfBounds(2)),
        );

        let mut wrong_type = leaf(2, 1, &[5]);
        let mut header = PageHeader::decode(&wrong_type.bytes, 1).unwrap();
        header.page_type = PageType::RangeLeaf;
        header.lower = 32;
        header.encode_into(&mut wrong_type.bytes);
        page::write_crc32c(&mut wrong_type.bytes);
        assert_committed_error(
            SparsePages::new([wrong_type]),
            20,
            FreeBitmapCowError::UnexpectedPageType {
                pgno: 2,
                page_type: PageType::RangeLeaf,
            },
        );

        assert_committed_error(
            SparsePages::new([branch(2, 1, 1, 3)]),
            20,
            FreeBitmapCowError::RootLevel {
                expected: 0,
                actual: 1,
            },
        );
        assert_committed_error(
            SparsePages::new([branch(2, 1, 1, 3), branch(3, 1, 1, 4)]),
            32_001,
            FreeBitmapCowError::ChildLevel {
                expected: 0,
                actual: 1,
            },
        );
        assert_committed_error(
            SparsePages::new([branch(2, 1, 1, 2)]),
            32_001,
            FreeBitmapCowError::RepeatedPathPage(2),
        );

        let (result, allocations) = count_thread_allocations(|| coverage(u16::MAX));
        assert_eq!(allocations, 0);
        assert_eq!(result, Err(FreeBitmapCowError::CoverageOverflow));

        {
            let mut private_root = branch_at(10, 2, 1, 2, 11);
            private_root.bytes[CHILDREN_OFFSET + 2 * 4..CHILDREN_OFFSET + 3 * 4]
                .copy_from_slice(&11u32.to_le_bytes());
            let mut arena = [ReservedBitmapPage::new(10)];
            arena[0].preset_bitmap_page(2, 2, private_root.bytes);
            let before = *arena[0].initial_bytes();
            let mut replacements = [2u32];
            let mut candidates = [0u32; 1];
            let mut index = [BitmapCowIndexNode::empty(); 2];
            let mut available = [0usize; 1];
            let ledger = FreeBitmapCowLedger::with_prefixes(
                &mut arena,
                &mut replacements,
                1,
                &mut candidates,
                0,
                &mut index,
                &mut available,
            );
            let source = SparsePages::new([]);
            let mut cow = FreeBitmapCow::new(&source, 1, 32_001, 10, ledger).unwrap();
            let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
            assert_eq!(allocations, 0);
            assert_eq!(
                result,
                Err(FreeBitmapCowError::SelectedCoverageOutsideLimit)
            );
            assert_eq!(cow.root(), 10);
            assert_eq!(cow.test_page_at(0), before);
        }

        {
            let mut private_root = branch(10, 2, 1, 11);
            private_root.bytes[CHILDREN_OFFSET..CHILDREN_OFFSET + 4].fill(0);
            let mut arena = [ReservedBitmapPage::new(10)];
            arena[0].preset_bitmap_page(2, 2, private_root.bytes);
            let before = *arena[0].initial_bytes();
            let mut replacements = [2u32];
            let mut candidates = [0u32; 1];
            let mut index = [BitmapCowIndexNode::empty(); 2];
            let mut available = [0usize; 1];
            let ledger = FreeBitmapCowLedger::with_prefixes(
                &mut arena,
                &mut replacements,
                1,
                &mut candidates,
                0,
                &mut index,
                &mut available,
            );
            let source = SparsePages::new([]);
            let mut cow = FreeBitmapCow::new(&source, 1, 32_001, 10, ledger).unwrap();
            let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
            assert_eq!(allocations, 0);
            assert_eq!(result, Err(FreeBitmapCowError::SelectedChildMissing));
            assert_eq!(cow.root(), 10);
            assert_eq!(cow.test_page_at(0), before);
        }

        {
            let mut arena = [ReservedBitmapPage::new(10), ReservedBitmapPage::new(11)];
            arena[0].preset_bitmap_page(2, 3, leaf(10, 2, &[1]).bytes);
            arena[1].preset_bitmap_page(2, 2, branch(11, 2, 1, 10).bytes);
            let before = [*arena[0].initial_bytes(), *arena[1].initial_bytes()];
            let mut replacements = [2u32, 3];
            let mut candidates = [0u32; 1];
            let mut index = [BitmapCowIndexNode::empty(); 4];
            let mut available = [0usize; 2];
            let ledger = FreeBitmapCowLedger::with_prefixes(
                &mut arena,
                &mut replacements,
                2,
                &mut candidates,
                0,
                &mut index,
                &mut available,
            );
            let source = SparsePages::new([]);
            let mut cow = FreeBitmapCow::new(&source, 1, 32_001, 11, ledger).unwrap();
            let (result, allocations) = count_thread_allocations(|| cow.remove_lowest());
            assert_eq!(allocations, 0);
            assert_eq!(result, Err(FreeBitmapCowError::SummaryMismatch));
            assert_eq!(cow.root(), 11);
            assert_eq!(cow.test_page_at(0), before[0]);
            assert_eq!(cow.test_page_at(1), before[1]);
        }
    }

    #[test]
    fn large_doubling_scaling_benchmark_is_n_log_n_and_allocation_free() {
        fn run(count: usize) -> (usize, u8) {
            let bits: Vec<u32> = (3..u32::try_from(count).unwrap() + 3).collect();
            let source = SparsePages::new([leaf(2, 1, &bits)]);
            let mut arena: Vec<ReservedBitmapPage> = (0..count)
                .map(|offset| ReservedBitmapPage::new(20_000 + offset as u32))
                .collect();
            let mut replacements = vec![0u32; 1];
            let mut candidates = vec![0u32; count];
            let mut index = vec![BitmapCowIndexNode::empty(); count + 1];
            let mut available = vec![0usize; count];
            let ledger = FreeBitmapCowLedger::empty(
                &mut arena,
                &mut replacements,
                &mut candidates,
                &mut index,
                &mut available,
            );
            let mut cow = FreeBitmapCow::new(&source, 1, BITMAP_LEAF_BITS, 2, ledger).unwrap();

            let ((), allocations) = count_thread_allocations(|| {
                for expected in 3..u32::try_from(count).unwrap() + 3 {
                    assert_eq!(
                        cow.remove_lowest().unwrap().unwrap().page_number(),
                        expected
                    );
                }
            });
            assert_eq!(allocations, 0);
            assert_eq!(cow.root(), 0);
            assert_eq!(cow.candidate_len, count);
            assert_eq!(cow.available_private_pages(), count);
            let height = cow.index_nodes[cow.index_root].height;
            let logarithmic_height_bound =
                2 * u8::try_from((usize::BITS - (count + 1).leading_zeros()) as usize).unwrap();
            assert!(height <= logarithmic_height_bound);
            (cow.index_probe_count(), height)
        }

        let samples = [run(512), run(1_024), run(2_048), run(4_096)];
        for pair in samples.windows(2) {
            let (smaller_probes, smaller_height) = pair[0];
            let (larger_probes, larger_height) = pair[1];
            assert!(larger_probes < smaller_probes * 3);
            assert!(larger_height <= smaller_height + 2);
        }
    }

    #[test]
    fn avl_index_stays_balanced_for_adversarial_insertion_orders() {
        fn verify(nodes: &[BitmapCowIndexNode], root: usize) -> Option<(u32, u32, u8, usize)> {
            if root == NO_INDEX {
                return None;
            }
            let left = verify(nodes, nodes[root].left);
            let right = verify(nodes, nodes[root].right);
            if let Some((_, left_max, _, _)) = left {
                assert!(left_max < nodes[root].pgno);
            }
            if let Some((right_min, _, _, _)) = right {
                assert!(nodes[root].pgno < right_min);
            }
            let left_height = left.map_or(0, |value| value.2);
            let right_height = right.map_or(0, |value| value.2);
            assert!(left_height.abs_diff(right_height) <= 1);
            let height = 1 + left_height.max(right_height);
            assert_eq!(nodes[root].height, height);
            Some((
                left.map_or(nodes[root].pgno, |value| value.0),
                right.map_or(nodes[root].pgno, |value| value.1),
                height,
                1 + left.map_or(0, |value| value.3) + right.map_or(0, |value| value.3),
            ))
        }

        const COUNT: usize = 4_096;
        let ascending: Vec<u32> = (2..u32::try_from(COUNT).unwrap() + 2).collect();
        let descending: Vec<u32> = ascending.iter().rev().copied().collect();
        let alternating: Vec<u32> = (0..COUNT)
            .map(|index| {
                let offset = if index % 2 == 0 {
                    index / 2
                } else {
                    COUNT - 1 - index / 2
                };
                u32::try_from(offset + 2).unwrap()
            })
            .collect();
        let permuted: Vec<u32> = (0..COUNT)
            .map(|index| u32::try_from(((index * 4_051) & (COUNT - 1)) + 2).unwrap())
            .collect();

        for order in [&ascending, &descending, &alternating, &permuted] {
            let mut nodes = vec![BitmapCowIndexNode::empty(); COUNT];
            let mut root = NO_INDEX;
            let mut len = 0usize;
            let ((), allocations) = count_thread_allocations(|| {
                for &pgno in order {
                    assert!(page_index_insert(
                        &mut nodes,
                        &mut root,
                        &mut len,
                        pgno,
                        IndexedPage::Replacement,
                    ));
                }
            });
            assert_eq!(allocations, 0);
            assert_eq!(len, COUNT);
            let (_, _, height, visited) = verify(&nodes, root).unwrap();
            assert_eq!(visited, COUNT);
            assert!(height <= 2 * u8::try_from(usize::BITS - COUNT.leading_zeros()).unwrap());
        }
    }
}
