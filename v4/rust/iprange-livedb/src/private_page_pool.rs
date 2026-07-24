//! Transaction-owned physical pages shared by private COW engines.
//!
//! The pool owns page bytes and physical authorization. Engines retain only
//! semantic metadata and obtain short, checked borrows through move-only page
//! authority. No engine may manufacture, copy, or independently own a private
//! 4 KiB page.

use crate::contract::{MAX_PAGE_COUNT, PAGE_SIZE};
use crate::page::{self, PageHeader, PageType};
use core::cell::{Cell, Ref, RefCell, RefMut};
use core::marker::PhantomData;
use core::sync::atomic::{AtomicUsize, Ordering};

mod selective_finalization;
pub(crate) use selective_finalization::{
    private_page_selective_scratch_requirements, PrivatePageSelectiveError,
    PrivatePageSelectiveOverlayNode, PrivatePageSelectivePathEntry, PrivatePageSelectiveScratch,
};

static NEXT_POOL_IDENTITY: AtomicUsize = AtomicUsize::new(1);
const NO_SLOT: usize = usize::MAX;

macro_rules! retain_token_on_error {
    ($token:ident, $result:expr) => {
        match $result {
            Ok(value) => value,
            Err(error) => return Err(($token, error)),
        }
    };
}

#[cfg(test)]
std::thread_local! {
    static PRIVATE_PAGE_COMMITMENT_WORK: Cell<usize> = const { Cell::new(0) };
}

const fn checked_next_pool_identity(identity: usize) -> Option<usize> {
    identity.checked_add(1)
}

const fn checked_next_pool_identity_pair(identity: usize) -> Option<usize> {
    if identity == usize::MAX {
        None
    } else if identity == usize::MAX - 1 {
        Some(usize::MAX)
    } else {
        identity.checked_add(2)
    }
}

fn reserve_pool_identity_pair(counter: &AtomicUsize) -> Option<(usize, usize)> {
    let active = counter
        .fetch_update(
            Ordering::Relaxed,
            Ordering::Relaxed,
            checked_next_pool_identity_pair,
        )
        .ok()?;
    Some((active, active + 1))
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivatePageAuthorization {
    CommittedFree,
    SafelyReclaimed,
    Appended,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivatePageOwner {
    Bitmap,
    Range,
    Normalization,
    Retirement,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivatePageReturn {
    Available,
    Free,
    Tail,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivatePagePoolError {
    PageCountOutOfRange {
        committed: u64,
        pending: u64,
    },
    PendingTransactionOutOfRange(u64),
    PendingTransactionMismatch {
        expected: u64,
        actual: u64,
    },
    AuthorizedAfterVacant(usize),
    PageOutOfBounds(u32),
    AuthorizationMismatch {
        pgno: u32,
        authorization: PrivatePageAuthorization,
    },
    PagesNotStrict {
        previous: u32,
        current: u32,
    },
    SlotOutOfBounds(usize),
    SlotNotVacant(usize),
    SlotVacant(usize),
    PageNotFound(u32),
    PageUnavailable(u32),
    OwnerMismatch {
        pgno: u32,
        expected: PrivatePageOwner,
        actual: PrivatePageOwner,
    },
    PoolMismatch,
    StaleSnapshot {
        expected: u64,
        actual: u64,
    },
    StaleAuthority,
    BorrowConflict,
    CheckpointActive,
    CheckpointMissing,
    CheckpointMismatch,
    OperationActive,
    OperationMissing,
    AbortRequired,
    RollbackUnsafeWrite(u32),
    GenerationExhausted,
    EpochExhausted,
    PoolIdentityExhausted,
    ReservationBudget {
        required: usize,
        actual: usize,
    },
    StaleScope,
    ScopeMismatch(u32),
    ScopeNotEmpty(usize),
    ScopeIdentityExhausted,
    ActiveScopeUnderflow,
    CoordinatorRequired,
    CoordinatorMismatch,
    CoordinatorWorkActive,
    CoordinatorWorkMissing,
    UnacceptedCoordinatorScope,
    InvalidState(usize),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivatePageCoordinatorWorkPhase {
    None,
    Active,
    Sealed,
}

/// Move-only authority for one coordinator-registered work unit.
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageCoordinatorWork {
    pool_identity: usize,
    session_identity: u64,
    session_generation: u64,
    work_identity: u64,
    work_generation: u64,
}

#[derive(Clone, Copy)]
struct PrivatePageCoordinatorWorkSeed {
    pool_identity: usize,
    session_identity: u64,
    session_generation: u64,
    work_identity: u64,
    work_generation: u64,
}

impl PrivatePageCoordinatorWorkSeed {
    const fn materialize(self) -> PrivatePageCoordinatorWork {
        PrivatePageCoordinatorWork {
            pool_identity: self.pool_identity,
            session_identity: self.session_identity,
            session_generation: self.session_generation,
            work_identity: self.work_identity,
            work_generation: self.work_generation,
        }
    }
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) struct PrivatePagePreparedScopeSlot {
    address: usize,
    pool_identity: usize,
    session_identity: u64,
    session_generation: u64,
    work_identity: u64,
    count: usize,
    pool_epoch: u64,
    vacant_head: usize,
    vacant_count: usize,
    seal: u64,
}

impl PrivatePagePreparedScopeSlot {
    pub(crate) const fn empty() -> Self {
        Self {
            address: 0,
            pool_identity: 0,
            session_identity: 0,
            session_generation: 0,
            work_identity: 0,
            count: 0,
            pool_epoch: 0,
            vacant_head: NO_SLOT,
            vacant_count: 0,
            seal: 0,
        }
    }

    fn clear(&mut self) {
        *self = Self::empty();
    }
}

/// Move-only borrowed proof that a scope reservation is callback-free.
#[derive(Debug)]
pub(crate) struct PrivatePagePreparedScopeReservation<'slot> {
    slot: &'slot mut PrivatePagePreparedScopeSlot,
}

impl PrivatePagePreparedScopeReservation<'_> {
    pub(crate) fn slot_range(&self) -> Option<(usize, usize)> {
        let start = &*self.slot as *const PrivatePagePreparedScopeSlot as usize;
        Some((
            start,
            start.checked_add(core::mem::size_of_val(&*self.slot))?,
        ))
    }

    #[cfg(test)]
    pub(crate) fn test_corrupt_address(&mut self) {
        self.slot.address = self.slot.address.wrapping_add(1);
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageCoordinatorFence {
    pool_identity: usize,
    epoch: u64,
    pending_page_count: u64,
    session_identity: u64,
    session_generation: u64,
    work_identity: u64,
    work_generation: u64,
    work_phase: PrivatePageCoordinatorWorkPhase,
    active_checkpoint: u64,
    active_operation: u64,
    active_scopes: usize,
    unaccepted_scopes: usize,
    abort_required: bool,
}

pub(crate) struct PrivatePageCallbackIsolation<'borrow, 'slots> {
    _slots: RefMut<'borrow, &'slots mut [PrivatePagePoolSlot]>,
}

/// Move-only authority for the one post-input cleanup path that may mutate an
/// accepted sealed coordinator scope. Ordinary scoped mutation stays closed
/// once coordinator work has finished.
pub(crate) struct PrivatePageSealedCoordinatorCleanup<'pool, 'slots> {
    pool: &'pool PrivatePagePool<'slots>,
    scope_id: u64,
    nonce: u64,
    anchor: usize,
    active: bool,
}

impl<'pool, 'slots> PrivatePageSealedCoordinatorCleanup<'pool, 'slots> {
    pub(crate) fn finish(mut self) -> Result<(), PrivatePagePoolError> {
        if !self.active
            || self.pool.sealed_coordinator_cleanup_scope_id.get() != self.scope_id
            || self.pool.sealed_coordinator_cleanup_nonce.get() != self.nonce
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        let slots = self
            .pool
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = slots
            .get(self.anchor)
            .ok_or(PrivatePagePoolError::StaleScope)?;
        if anchor.scope_id != 0 || anchor.scope_anchor {
            return Err(PrivatePagePoolError::ScopeNotEmpty(
                self.pool.active_scopes.get(),
            ));
        }
        drop(slots);
        let cleanup = self
            .pool
            .coordinator_cleanup_pending
            .get()
            .checked_sub(1)
            .ok_or(PrivatePagePoolError::CoordinatorMismatch)?;
        self.pool.coordinator_cleanup_pending.set(cleanup);
        self.pool.sealed_coordinator_cleanup_scope_id.set(0);
        self.pool.sealed_coordinator_cleanup_nonce.set(0);
        self.active = false;
        Ok(())
    }
}

impl Drop for PrivatePageSealedCoordinatorCleanup<'_, '_> {
    fn drop(&mut self) {
        if self.active
            && self.pool.sealed_coordinator_cleanup_scope_id.get() == self.scope_id
            && self.pool.sealed_coordinator_cleanup_nonce.get() == self.nonce
        {
            self.pool.sealed_coordinator_cleanup_scope_id.set(0);
            self.pool.sealed_coordinator_cleanup_nonce.set(0);
        }
    }
}

impl PrivatePageCoordinatorFence {
    pub(crate) fn seal(self) -> u64 {
        let mut seal = 1_469_598_103_934_665_603u64;
        for value in [
            self.pool_identity as u64,
            self.epoch,
            self.pending_page_count,
            self.session_identity,
            self.session_generation,
            self.work_identity,
            self.work_generation,
            self.work_phase as u64,
            self.active_checkpoint,
            self.active_operation,
            self.active_scopes as u64,
            self.unaccepted_scopes as u64,
            self.abort_required as u64,
        ] {
            seal ^= value;
            seal = seal.wrapping_mul(1_099_511_628_211);
        }
        seal
    }
}

#[cfg(test)]
#[derive(Clone, Copy, Debug)]
pub(crate) enum PrivatePageVacantPayloadCorruption {
    PageNumber,
    Authorization,
    State,
    AllocationGeneration,
    CheckpointGeneration,
    SavedState,
    AdapterOwner,
    AdapterTag,
    Bytes,
    IndexLeft,
    IndexRight,
    IndexHeight,
    IndexAvailable,
    IndexInUse,
    IndexUnscopedAvailable,
    ScopeLeft,
    ScopeRight,
    ScopeHeight,
    ScopeAvailable,
    ScopeInUse,
    ValidationMarker,
    SavedBinding,
    SavedIndexGeneration,
    SavedIndexNext,
    SavedScopeGeneration,
}

#[cfg(test)]
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct PrivatePagePoolTestSnapshot {
    slots: std::vec::Vec<PrivatePagePoolSlot>,
    index_root: usize,
    authorized_len: usize,
    available_count: usize,
    lowest_available: usize,
    pending_page_count: u64,
    identity: usize,
    identity_epoch: usize,
    invalidation_identity: usize,
    abort_epoch_reserve: u64,
    generation: u64,
    epoch: u64,
    active_checkpoint: u64,
    operation_sequence: u64,
    active_operation_id: u64,
    operation_start_epoch: u64,
    abort_required: bool,
    checkpoint_cleanup_slots: usize,
    checkpoint_index_head: usize,
    checkpoint_index_count: usize,
    coordinator_session_identity: u64,
    coordinator_session_generation: u64,
    coordinator_work_identity: u64,
    coordinator_work_generation: u64,
    coordinator_work_phase: PrivatePageCoordinatorWorkPhase,
    coordinator_work_start_epoch: u64,
    coordinator_mutation_started: bool,
    coordinator_scope_id: u64,
    coordinator_unaccepted_scopes: usize,
    coordinator_cleanup_pending: usize,
    sealed_coordinator_cleanup_scope_id: u64,
    sealed_coordinator_cleanup_nonce: u64,
    scope_sequence: u64,
    active_scopes: usize,
    unscoped_vacant_count: usize,
    unscoped_vacant_head: usize,
    unscoped_vacant_tail: usize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum PrivatePageState {
    Vacant,
    Available,
    InUse {
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
        authority_epoch: u64,
    },
    PendingReturn {
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
        authority_epoch: u64,
        disposition: PrivatePageReturn,
    },
    ReturnedFree,
    ReturnedTail,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum SavedState {
    None,
    State(PrivatePageState),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum SavedBinding {
    None,
    Binding {
        pgno: u32,
        authorization: Option<PrivatePageAuthorization>,
        scope_id: u64,
        scope_anchor: bool,
        scope_anchor_index: usize,
        scope_vacant_next: usize,
        allocation_generation: u64,
        adapter_owner: Option<PrivatePageOwner>,
        adapter_tag: u64,
    },
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePagePoolSlot {
    pgno: u32,
    authorization: Option<PrivatePageAuthorization>,
    state: PrivatePageState,
    allocation_generation: u64,
    checkpoint_generation: u64,
    saved_state: SavedState,
    adapter_owner: Option<PrivatePageOwner>,
    adapter_tag: u64,
    bytes: [u8; PAGE_SIZE],
    binding_epoch: u64,
    scope_id: u64,
    scope_anchor: bool,
    scope_anchor_index: usize,
    scope_member_next: usize,
    scope_member_head: usize,
    scope_member_ordinal: usize,
    scope_validation_marker: usize,
    scope_vacant_next: usize,
    scope_root: usize,
    scope_vacant_head: usize,
    scope_capacity: usize,
    scope_bound: usize,
    scope_generation: u64,
    scope_sealed: bool,
    scope_successor: u64,
    successor_consumed: bool,
    index_left: usize,
    index_right: usize,
    index_height: u8,
    index_available: usize,
    index_in_use: usize,
    index_unscoped_available: usize,
    scope_left: usize,
    scope_right: usize,
    scope_height: u8,
    scope_available: usize,
    scope_in_use: usize,
    scope_count: usize,
    scope_revision: u64,
    scope_digest: u64,
    scope_vacant_count: usize,
    scope_vacant_revision: u64,
    scope_vacant_digest: u64,
    unscoped_vacant_prev: usize,
    unscoped_vacant_next: usize,
    saved_binding: SavedBinding,
    saved_index_generation: u64,
    saved_index_next: usize,
    saved_index_left: usize,
    saved_index_right: usize,
    saved_index_height: u8,
    saved_index_available: usize,
    saved_index_in_use: usize,
    saved_index_unscoped_available: usize,
    saved_scope_left: usize,
    saved_scope_right: usize,
    saved_scope_height: u8,
    saved_scope_available: usize,
    saved_scope_in_use: usize,
    saved_scope_count: usize,
    saved_scope_revision: u64,
    saved_scope_digest: u64,
    saved_scope_vacant_count: usize,
    saved_scope_vacant_revision: u64,
    saved_scope_vacant_digest: u64,
    saved_scope_generation: u64,
    saved_scope_root: usize,
    saved_scope_vacant_head: usize,
    saved_scope_bound: usize,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageSparseReplaySlot {
    slot: usize,
    after: PrivatePagePoolSlot,
    occupied: bool,
}

impl PrivatePageSparseReplaySlot {
    pub(crate) const fn empty() -> Self {
        Self {
            slot: NO_SLOT,
            after: PrivatePagePoolSlot::empty(),
            occupied: false,
        }
    }
}

/// Caller-owned direct map from a pool slot to its sparse overlay after-image.
/// It is intentionally outside `PrivatePagePoolSlot`: preparation must not add
/// per-slot runtime state or mutate live pool slots before replay.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageSparseReplayIndex {
    generation: u64,
    entry: usize,
}

impl PrivatePageSparseReplayIndex {
    pub(crate) const fn empty() -> Self {
        Self {
            generation: 0,
            entry: NO_SLOT,
        }
    }
}

#[derive(Clone, Copy, Debug)]
struct PrivatePageSparseReplayState {
    authorized_len: usize,
    available_count: usize,
    lowest_available: usize,
    pending_page_count: u64,
    epoch: u64,
    coordinator_work_identity: u64,
    coordinator_work_generation: u64,
    coordinator_work_phase: PrivatePageCoordinatorWorkPhase,
    coordinator_work_start_epoch: u64,
    coordinator_mutation_started: bool,
    coordinator_scope_id: u64,
    coordinator_unaccepted_scopes: usize,
    scope_sequence: u64,
    active_scopes: usize,
    unscoped_vacant_count: usize,
    unscoped_vacant_head: usize,
    unscoped_vacant_tail: usize,
    index_root: usize,
}

pub(crate) struct PrivatePagePreparedSparseReplay<'pool, 'slots, 'scratch> {
    pool: &'pool PrivatePagePool<'slots>,
    live: RefMut<'pool, &'slots mut [PrivatePagePoolSlot]>,
    slots: &'scratch mut [PrivatePageSparseReplaySlot],
    index: &'scratch mut [PrivatePageSparseReplayIndex],
    len: usize,
    index_visits: usize,
    state: PrivatePageSparseReplayState,
    work: PrivatePageCoordinatorWorkSeed,
    scope: PrivatePageReservationScopeSeed,
}

struct PrivatePageSparseOverlay<'live, 'scratch> {
    live: &'live mut [PrivatePagePoolSlot],
    slots: &'scratch mut [PrivatePageSparseReplaySlot],
    index: &'scratch mut [PrivatePageSparseReplayIndex],
    generation: u64,
    index_visits: Cell<usize>,
    len: usize,
    retained: bool,
}

fn private_page_scope_payload_revision(slot: &PrivatePagePoolSlot) -> u64 {
    let authority_epoch = match slot.state {
        PrivatePageState::InUse {
            authority_epoch, ..
        }
        | PrivatePageState::PendingReturn {
            authority_epoch, ..
        } => authority_epoch,
        _ => 0,
    };
    // `binding_epoch` only fences stale page authorities. A checkpoint
    // rollback deliberately advances it while restoring the represented page,
    // so it cannot participate in a rollback-stable scope aggregate.
    slot.allocation_generation
        .rotate_left(11)
        .wrapping_add(authority_epoch.rotate_left(23))
}

fn private_page_scope_payload_digest(index: usize, slot: &PrivatePagePoolSlot) -> u64 {
    let (state, owner, owner_generation, tag, authority_epoch, disposition) = match slot.state {
        PrivatePageState::Vacant => (0, 0, 0, 0, 0, 0),
        PrivatePageState::Available => (1, 0, 0, 0, 0, 0),
        PrivatePageState::InUse {
            owner,
            owner_generation,
            tag,
            authority_epoch,
        } => (
            2,
            owner as u64 + 1,
            owner_generation,
            tag,
            authority_epoch,
            0,
        ),
        PrivatePageState::PendingReturn {
            owner,
            owner_generation,
            tag,
            authority_epoch,
            disposition,
        } => (
            3,
            owner as u64 + 1,
            owner_generation,
            tag,
            authority_epoch,
            disposition as u64 + 1,
        ),
        PrivatePageState::ReturnedFree => (4, 0, 0, 0, 0, 0),
        PrivatePageState::ReturnedTail => (5, 0, 0, 0, 0, 0),
    };
    let stored_crc = u32::from_le_bytes([
        slot.bytes[page::PAGE_CRC_OFFSET],
        slot.bytes[page::PAGE_CRC_OFFSET + 1],
        slot.bytes[page::PAGE_CRC_OFFSET + 2],
        slot.bytes[page::PAGE_CRC_OFFSET + 3],
    ]);
    let mut hash = 1_469_598_103_934_665_603u64;
    for value in [
        index as u64,
        u64::from(slot.pgno),
        slot.authorization.map_or(0, |value| value as u64 + 1),
        state,
        owner,
        owner_generation,
        tag,
        authority_epoch,
        disposition,
        slot.allocation_generation,
        slot.scope_id,
        slot.scope_anchor_index as u64,
        slot.scope_member_ordinal as u64,
        u64::from(stored_crc),
    ] {
        hash = pool_hash_u64(hash, value);
    }
    hash
}

impl<'live, 'scratch> PrivatePageSparseOverlay<'live, 'scratch> {
    fn new(
        live: &'live mut [PrivatePagePoolSlot],
        slots: &'scratch mut [PrivatePageSparseReplaySlot],
        index: &'scratch mut [PrivatePageSparseReplayIndex],
        generation: u64,
    ) -> Result<Self, PrivatePagePoolError> {
        if slots.is_empty() || generation == 0 {
            return Err(PrivatePagePoolError::ReservationBudget {
                required: 1,
                actual: slots.len(),
            });
        }
        if index.len() < live.len() {
            return Err(PrivatePagePoolError::ReservationBudget {
                required: live.len(),
                actual: index.len(),
            });
        }
        Ok(Self {
            live,
            slots,
            index,
            generation,
            index_visits: Cell::new(0),
            len: 0,
            retained: false,
        })
    }

    fn mapped_entry(&self, slot: usize) -> Option<usize> {
        self.index_visits
            .set(self.index_visits.get().saturating_add(1));
        let mapped = self.index[slot];
        (mapped.generation == self.generation).then_some(mapped.entry)
    }

    fn get(&self, slot: usize) -> &PrivatePagePoolSlot {
        match self.mapped_entry(slot) {
            Some(entry) => &self.slots[entry].after,
            None => &self.live[slot],
        }
    }

    fn get_mut(&mut self, slot: usize) -> Result<&mut PrivatePagePoolSlot, PrivatePagePoolError> {
        let entry = match self.mapped_entry(slot) {
            Some(entry) => entry,
            None => {
                if self.len == self.slots.len() {
                    return Err(PrivatePagePoolError::ReservationBudget {
                        required: self.len.saturating_add(1),
                        actual: self.slots.len(),
                    });
                }
                let entry = self.len;
                self.len += 1;
                self.slots[entry] = PrivatePageSparseReplaySlot {
                    slot,
                    after: self.live[slot].clone(),
                    occupied: true,
                };
                self.index[slot] = PrivatePageSparseReplayIndex {
                    generation: self.generation,
                    entry,
                };
                entry
            }
        };
        Ok(&mut self.slots[entry].after)
    }

    fn finish(mut self) -> (usize, usize) {
        self.retained = true;
        (self.len, self.index_visits.get())
    }

    fn index_height(&self, slot: usize) -> u8 {
        if slot == NO_SLOT {
            0
        } else {
            self.get(slot).index_height
        }
    }

    fn scope_height(&self, slot: usize) -> u8 {
        if slot == NO_SLOT {
            0
        } else {
            self.get(slot).scope_height
        }
    }

    fn refresh_index(&mut self, slot: usize) -> Result<(), PrivatePagePoolError> {
        let (left, right, state, scope_id) = {
            let node = self.get(slot);
            (node.index_left, node.index_right, node.state, node.scope_id)
        };
        let mut available = usize::from(state == PrivatePageState::Available);
        let mut in_use = usize::from(matches!(state, PrivatePageState::InUse { .. }));
        let mut unscoped = usize::from(scope_id == 0 && state == PrivatePageState::Available);
        if left != NO_SLOT {
            let child = self.get(left);
            available += child.index_available;
            in_use += child.index_in_use;
            unscoped += child.index_unscoped_available;
        }
        if right != NO_SLOT {
            let child = self.get(right);
            available += child.index_available;
            in_use += child.index_in_use;
            unscoped += child.index_unscoped_available;
        }
        let height = 1 + self.index_height(left).max(self.index_height(right));
        let node = self.get_mut(slot)?;
        node.index_height = height;
        node.index_available = available;
        node.index_in_use = in_use;
        node.index_unscoped_available = unscoped;
        Ok(())
    }

    fn rotate_index_right(&mut self, root: usize) -> Result<usize, PrivatePagePoolError> {
        let left = self.get(root).index_left;
        let middle = self.get(left).index_right;
        self.get_mut(root)?.index_left = middle;
        self.get_mut(left)?.index_right = root;
        self.refresh_index(root)?;
        self.refresh_index(left)?;
        Ok(left)
    }

    fn rotate_index_left(&mut self, root: usize) -> Result<usize, PrivatePagePoolError> {
        let right = self.get(root).index_right;
        let middle = self.get(right).index_left;
        self.get_mut(root)?.index_right = middle;
        self.get_mut(right)?.index_left = root;
        self.refresh_index(root)?;
        self.refresh_index(right)?;
        Ok(right)
    }

    fn rebalance_index(&mut self, root: usize) -> Result<usize, PrivatePagePoolError> {
        self.refresh_index(root)?;
        let left = self.get(root).index_left;
        let right = self.get(root).index_right;
        let balance = i16::from(self.index_height(left)) - i16::from(self.index_height(right));
        if balance > 1 {
            if self.index_height(self.get(left).index_right)
                > self.index_height(self.get(left).index_left)
            {
                let rotated = self.rotate_index_left(left)?;
                self.get_mut(root)?.index_left = rotated;
            }
            return self.rotate_index_right(root);
        }
        if balance < -1 {
            if self.index_height(self.get(right).index_left)
                > self.index_height(self.get(right).index_right)
            {
                let rotated = self.rotate_index_right(right)?;
                self.get_mut(root)?.index_right = rotated;
            }
            return self.rotate_index_left(root);
        }
        Ok(root)
    }

    fn insert_index(
        &mut self,
        root: usize,
        inserted: usize,
    ) -> Result<usize, PrivatePagePoolError> {
        if root == NO_SLOT {
            return Ok(inserted);
        }
        if self.get(inserted).pgno < self.get(root).pgno {
            let child = self.insert_index(self.get(root).index_left, inserted)?;
            self.get_mut(root)?.index_left = child;
        } else {
            let child = self.insert_index(self.get(root).index_right, inserted)?;
            self.get_mut(root)?.index_right = child;
        }
        self.rebalance_index(root)
    }

    fn detach_index_minimum(
        &mut self,
        root: usize,
    ) -> Result<(usize, usize), PrivatePagePoolError> {
        let left = self.get(root).index_left;
        if left == NO_SLOT {
            return Ok((self.get(root).index_right, root));
        }
        let (left, minimum) = self.detach_index_minimum(left)?;
        self.get_mut(root)?.index_left = left;
        Ok((self.rebalance_index(root)?, minimum))
    }

    fn delete_index(
        &mut self,
        root: usize,
        pgno: u32,
    ) -> Result<(usize, usize), PrivatePagePoolError> {
        if root == NO_SLOT {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        if pgno < self.get(root).pgno {
            let (left, removed) = self.delete_index(self.get(root).index_left, pgno)?;
            self.get_mut(root)?.index_left = left;
            return Ok((self.rebalance_index(root)?, removed));
        }
        if pgno > self.get(root).pgno {
            let (right, removed) = self.delete_index(self.get(root).index_right, pgno)?;
            self.get_mut(root)?.index_right = right;
            return Ok((self.rebalance_index(root)?, removed));
        }
        let left = self.get(root).index_left;
        let right = self.get(root).index_right;
        if left == NO_SLOT {
            return Ok((right, root));
        }
        if right == NO_SLOT {
            return Ok((left, root));
        }
        let (right, successor) = self.detach_index_minimum(right)?;
        {
            let node = self.get_mut(successor)?;
            node.index_left = left;
            node.index_right = right;
        }
        Ok((self.rebalance_index(successor)?, root))
    }

    fn refresh_scope(&mut self, slot: usize) -> Result<(), PrivatePagePoolError> {
        let (left, right, state) = {
            let node = self.get(slot);
            (node.scope_left, node.scope_right, node.state)
        };
        let mut available = usize::from(state == PrivatePageState::Available);
        let mut in_use = usize::from(matches!(state, PrivatePageState::InUse { .. }));
        let mut count = 1usize;
        let mut revision = private_page_scope_payload_revision(self.get(slot));
        let mut digest = private_page_scope_payload_digest(slot, self.get(slot));
        if left != NO_SLOT {
            available += self.get(left).scope_available;
            in_use += self.get(left).scope_in_use;
            count += self.get(left).scope_count;
            revision = revision.wrapping_add(self.get(left).scope_revision);
            digest ^= self.get(left).scope_digest.rotate_left(7);
        }
        if right != NO_SLOT {
            available += self.get(right).scope_available;
            in_use += self.get(right).scope_in_use;
            count += self.get(right).scope_count;
            revision = revision.wrapping_add(self.get(right).scope_revision);
            digest ^= self.get(right).scope_digest.rotate_left(37);
        }
        let height = 1 + self.scope_height(left).max(self.scope_height(right));
        let node = self.get_mut(slot)?;
        node.scope_height = height;
        node.scope_available = available;
        node.scope_in_use = in_use;
        node.scope_count = count;
        node.scope_revision = revision;
        node.scope_digest = digest;
        Ok(())
    }

    fn rotate_scope_right(&mut self, root: usize) -> Result<usize, PrivatePagePoolError> {
        let left = self.get(root).scope_left;
        let middle = self.get(left).scope_right;
        self.get_mut(root)?.scope_left = middle;
        self.get_mut(left)?.scope_right = root;
        self.refresh_scope(root)?;
        self.refresh_scope(left)?;
        Ok(left)
    }

    fn rotate_scope_left(&mut self, root: usize) -> Result<usize, PrivatePagePoolError> {
        let right = self.get(root).scope_right;
        let middle = self.get(right).scope_left;
        self.get_mut(root)?.scope_right = middle;
        self.get_mut(right)?.scope_left = root;
        self.refresh_scope(root)?;
        self.refresh_scope(right)?;
        Ok(right)
    }

    fn rebalance_scope(&mut self, root: usize) -> Result<usize, PrivatePagePoolError> {
        self.refresh_scope(root)?;
        let left = self.get(root).scope_left;
        let right = self.get(root).scope_right;
        let balance = i16::from(self.scope_height(left)) - i16::from(self.scope_height(right));
        if balance > 1 {
            if self.scope_height(self.get(left).scope_right)
                > self.scope_height(self.get(left).scope_left)
            {
                let rotated = self.rotate_scope_left(left)?;
                self.get_mut(root)?.scope_left = rotated;
            }
            return self.rotate_scope_right(root);
        }
        if balance < -1 {
            if self.scope_height(self.get(right).scope_left)
                > self.scope_height(self.get(right).scope_right)
            {
                let rotated = self.rotate_scope_right(right)?;
                self.get_mut(root)?.scope_right = rotated;
            }
            return self.rotate_scope_left(root);
        }
        Ok(root)
    }

    fn insert_scope(
        &mut self,
        root: usize,
        inserted: usize,
    ) -> Result<usize, PrivatePagePoolError> {
        if root == NO_SLOT {
            return Ok(inserted);
        }
        if self.get(inserted).pgno < self.get(root).pgno {
            let child = self.insert_scope(self.get(root).scope_left, inserted)?;
            self.get_mut(root)?.scope_left = child;
        } else {
            let child = self.insert_scope(self.get(root).scope_right, inserted)?;
            self.get_mut(root)?.scope_right = child;
        }
        self.rebalance_scope(root)
    }

    fn detach_scope_minimum(
        &mut self,
        root: usize,
    ) -> Result<(usize, usize), PrivatePagePoolError> {
        let left = self.get(root).scope_left;
        if left == NO_SLOT {
            return Ok((self.get(root).scope_right, root));
        }
        let (left, minimum) = self.detach_scope_minimum(left)?;
        self.get_mut(root)?.scope_left = left;
        Ok((self.rebalance_scope(root)?, minimum))
    }

    fn delete_scope(
        &mut self,
        root: usize,
        pgno: u32,
    ) -> Result<(usize, usize), PrivatePagePoolError> {
        if root == NO_SLOT {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        if pgno < self.get(root).pgno {
            let (left, removed) = self.delete_scope(self.get(root).scope_left, pgno)?;
            self.get_mut(root)?.scope_left = left;
            return Ok((self.rebalance_scope(root)?, removed));
        }
        if pgno > self.get(root).pgno {
            let (right, removed) = self.delete_scope(self.get(root).scope_right, pgno)?;
            self.get_mut(root)?.scope_right = right;
            return Ok((self.rebalance_scope(root)?, removed));
        }
        let left = self.get(root).scope_left;
        let right = self.get(root).scope_right;
        if left == NO_SLOT {
            return Ok((right, root));
        }
        if right == NO_SLOT {
            return Ok((left, root));
        }
        let (right, successor) = self.detach_scope_minimum(right)?;
        {
            let node = self.get_mut(successor)?;
            node.scope_left = left;
            node.scope_right = right;
        }
        Ok((self.rebalance_scope(successor)?, root))
    }
}

impl Drop for PrivatePageSparseOverlay<'_, '_> {
    fn drop(&mut self) {
        if self.retained {
            return;
        }
        for entry in &mut self.slots[..self.len] {
            self.index[entry.slot] = PrivatePageSparseReplayIndex::empty();
            *entry = PrivatePageSparseReplaySlot::empty();
        }
        self.len = 0;
    }
}

impl<'pool, 'slots, 'scratch> PrivatePagePreparedSparseReplay<'pool, 'slots, 'scratch> {
    pub(crate) const fn work_generation(&self) -> u64 {
        self.work.work_generation
    }

    pub(crate) const fn scope_seed(&self) -> PrivatePageReservationScopeSeed {
        self.scope
    }

    pub(crate) fn live_fence(&self) -> PrivatePageCoordinatorFence {
        self.pool.coordinator_fence()
    }

    /// Ensures the prepared scope still matches this replay before the
    /// mechanically infallible replay suffix consumes it.
    pub(crate) fn preflight_prepared_scope(
        &self,
        prepared_scope: &PrivatePagePreparedScopeReservation<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        self.pool
            .preflight_prepared_coordinator_scope(prepared_scope)?;
        let slot = &*prepared_scope.slot;
        if slot.session_identity != self.work.session_identity
            || slot.session_generation != self.work.session_generation
            || slot.work_identity != self.work.work_identity
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        Ok(())
    }

    #[cfg(test)]
    pub(crate) const fn touched_slots(&self) -> usize {
        self.len
    }

    #[cfg(test)]
    pub(crate) const fn index_visits(&self) -> usize {
        self.index_visits
    }

    pub(crate) fn simulated_slot(&self, slot: usize) -> &PrivatePagePoolSlot {
        let mapping = self.index[slot];
        if mapping.generation == self.work_generation() {
            return &self.slots[mapping.entry].after;
        }
        &self.live[slot]
    }

    pub(crate) fn future_sealed_page_provenance(
        &self,
        slot: usize,
    ) -> Result<PrivatePageSealedProvenance, PrivatePagePoolError> {
        let page = self.simulated_slot(slot);
        let PrivatePageState::InUse {
            owner,
            owner_generation,
            tag,
            ..
        } = page.state
        else {
            return Err(PrivatePagePoolError::InvalidState(slot));
        };
        Ok(PrivatePageSealedProvenance {
            scope_id: self.scope.id,
            scope_anchor: self.scope.anchor,
            scope_generation: self.scope.generation,
            slot,
            pgno: page.pgno,
            binding_epoch: page.binding_epoch,
            owner,
            owner_generation,
            tag,
        })
    }

    pub(crate) fn future_commitment(
        &self,
    ) -> Result<PrivatePagePoolCommitment, PrivatePagePoolError> {
        let mut hash = 1_469_598_103_934_665_603u64;
        for value in [
            self.pool.slot_count as u64,
            self.state.authorized_len as u64,
            self.state.available_count as u64,
            self.state.lowest_available as u64,
            self.pool.committed_page_count,
            self.state.pending_page_count,
            self.pool.pending_txn,
            self.pool.identity as u64,
            self.pool.identity_epoch as u64,
            self.pool.generation.get(),
            self.state.epoch,
            self.pool.active_checkpoint.get(),
            self.pool.checkpoint_cleanup_slots.get() as u64,
            self.pool.checkpoint_index_head.get() as u64,
            self.pool.checkpoint_index_count.get() as u64,
            self.state.scope_sequence,
            self.state.active_scopes as u64,
            self.state.unscoped_vacant_count as u64,
            self.state.unscoped_vacant_head as u64,
            self.state.unscoped_vacant_tail as u64,
            self.state.index_root as u64,
        ] {
            hash = pool_hash_u64(hash, value);
        }
        let scope = self.simulated_slot(self.scope.anchor);
        for value in [
            self.scope.id,
            self.scope.anchor as u64,
            scope.scope_member_head as u64,
            scope.scope_root as u64,
            scope.scope_vacant_head as u64,
            scope.scope_capacity as u64,
            scope.scope_bound as u64,
        ] {
            hash = pool_hash_u64(hash, value);
        }
        let (bound, tree_revision, tree_digest) = if scope.scope_root == NO_SLOT {
            (0, 0, 0)
        } else {
            let root = self.simulated_slot(scope.scope_root);
            (root.scope_count, root.scope_revision, root.scope_digest)
        };
        if bound != scope.scope_bound
            || scope.scope_vacant_count != scope.scope_capacity - scope.scope_bound
        {
            return Err(PrivatePagePoolError::StaleScope);
        }
        for value in [
            bound as u64,
            tree_revision,
            tree_digest,
            scope.scope_vacant_count as u64,
            scope.scope_vacant_revision,
            scope.scope_vacant_digest,
        ] {
            hash = pool_hash_u64(hash, value);
        }
        Ok(PrivatePagePoolCommitment {
            identity: self.pool.identity,
            identity_epoch: self.pool.identity_epoch,
            generation: self.pool.generation.get(),
            epoch: self.state.epoch,
            operation_sequence: self.pool.operation_sequence.get(),
            active_operation_id: self.pool.active_operation_id.get(),
            operation_start_epoch: self.pool.operation_start_epoch.get(),
            abort_required: self.pool.abort_required.get(),
            pending_page_count: self.state.pending_page_count,
            scope_id: self.scope.id,
            scope_anchor: self.scope.anchor,
            fingerprint: hash,
        })
    }

    pub(crate) fn cancel(mut self) {
        self.clear_sparse_index();
        self.len = 0;
    }

    /// Consumes a preflighted reservation, then applies the prepared sparse
    /// replay. Every target and after image is fixed during preparation and
    /// the live backing borrow is already owned by this token.
    pub(crate) fn replay_preflighted(
        self,
        prepared_scope: PrivatePagePreparedScopeReservation<'_>,
    ) -> (
        PrivatePageCoordinatorWork,
        PrivatePageReservationScope<'slots>,
        &'pool PrivatePagePool<'slots>,
    ) {
        prepared_scope.slot.clear();
        self.replay()
    }

    /// Mechanically infallible post-consume suffix.
    fn replay(
        mut self,
    ) -> (
        PrivatePageCoordinatorWork,
        PrivatePageReservationScope<'slots>,
        &'pool PrivatePagePool<'slots>,
    ) {
        let pool = self.pool;
        let state = self.state;
        let work = self.work.materialize();
        let scope = self.scope;
        pool.coordinator_work_identity
            .set(state.coordinator_work_identity);
        pool.coordinator_work_generation
            .set(state.coordinator_work_generation);
        pool.coordinator_work_phase
            .set(PrivatePageCoordinatorWorkPhase::Active);
        pool.coordinator_work_start_epoch
            .set(state.coordinator_work_start_epoch);
        pool.coordinator_mutation_started.set(true);
        for entry in &self.slots[..self.len] {
            self.live[entry.slot].clone_from(&entry.after);
        }
        pool.authorized_len.set(state.authorized_len);
        pool.available_count.set(state.available_count);
        pool.lowest_available.set(state.lowest_available);
        pool.pending_page_count.set(state.pending_page_count);
        pool.epoch.set(state.epoch);
        pool.coordinator_work_phase
            .set(state.coordinator_work_phase);
        pool.coordinator_work_start_epoch
            .set(state.coordinator_work_start_epoch);
        pool.coordinator_mutation_started
            .set(state.coordinator_mutation_started);
        pool.coordinator_scope_id.set(state.coordinator_scope_id);
        pool.coordinator_unaccepted_scopes
            .set(state.coordinator_unaccepted_scopes);
        pool.scope_sequence.set(state.scope_sequence);
        pool.active_scopes.set(state.active_scopes);
        pool.unscoped_vacant_count.set(state.unscoped_vacant_count);
        pool.unscoped_vacant_head.set(state.unscoped_vacant_head);
        pool.unscoped_vacant_tail.set(state.unscoped_vacant_tail);
        pool.index_root.set(state.index_root);
        self.clear_sparse_index();
        self.len = 0;
        (work, scope.materialize(pool), pool)
    }

    fn clear_sparse_index(&mut self) {
        for entry in &mut self.slots[..self.len] {
            self.index[entry.slot] = PrivatePageSparseReplayIndex::empty();
            *entry = PrivatePageSparseReplaySlot::empty();
        }
    }
}

impl Drop for PrivatePagePreparedSparseReplay<'_, '_, '_> {
    fn drop(&mut self) {
        self.clear_sparse_index();
        self.len = 0;
    }
}

impl PrivatePagePoolSlot {
    pub(crate) const fn empty() -> Self {
        Self {
            pgno: 0,
            authorization: None,
            state: PrivatePageState::Vacant,
            allocation_generation: 0,
            checkpoint_generation: 0,
            saved_state: SavedState::None,
            adapter_owner: None,
            adapter_tag: 0,
            bytes: [0; PAGE_SIZE],
            binding_epoch: 1,
            scope_id: 0,
            scope_anchor: false,
            scope_anchor_index: NO_SLOT,
            scope_member_next: NO_SLOT,
            scope_member_head: NO_SLOT,
            scope_member_ordinal: NO_SLOT,
            scope_validation_marker: 0,
            scope_vacant_next: NO_SLOT,
            scope_root: NO_SLOT,
            scope_vacant_head: NO_SLOT,
            scope_capacity: 0,
            scope_bound: 0,
            scope_generation: 0,
            scope_sealed: false,
            scope_successor: 0,
            successor_consumed: false,
            index_left: NO_SLOT,
            index_right: NO_SLOT,
            index_height: 0,
            index_available: 0,
            index_in_use: 0,
            index_unscoped_available: 0,
            scope_left: NO_SLOT,
            scope_right: NO_SLOT,
            scope_height: 0,
            scope_available: 0,
            scope_in_use: 0,
            scope_count: 0,
            scope_revision: 0,
            scope_digest: 0,
            scope_vacant_count: 0,
            scope_vacant_revision: 0,
            scope_vacant_digest: 0,
            unscoped_vacant_prev: NO_SLOT,
            unscoped_vacant_next: NO_SLOT,
            saved_binding: SavedBinding::None,
            saved_index_generation: 0,
            saved_index_next: NO_SLOT,
            saved_index_left: NO_SLOT,
            saved_index_right: NO_SLOT,
            saved_index_height: 0,
            saved_index_available: 0,
            saved_index_in_use: 0,
            saved_index_unscoped_available: 0,
            saved_scope_left: NO_SLOT,
            saved_scope_right: NO_SLOT,
            saved_scope_height: 0,
            saved_scope_available: 0,
            saved_scope_in_use: 0,
            saved_scope_count: 0,
            saved_scope_revision: 0,
            saved_scope_digest: 0,
            saved_scope_vacant_count: 0,
            saved_scope_vacant_revision: 0,
            saved_scope_vacant_digest: 0,
            saved_scope_generation: 0,
            saved_scope_root: NO_SLOT,
            saved_scope_vacant_head: NO_SLOT,
            saved_scope_bound: 0,
        }
    }

    pub(crate) const fn authorized(pgno: u32, authorization: PrivatePageAuthorization) -> Self {
        Self {
            pgno,
            authorization: Some(authorization),
            state: PrivatePageState::Available,
            allocation_generation: 0,
            checkpoint_generation: 0,
            saved_state: SavedState::None,
            adapter_owner: None,
            adapter_tag: 0,
            bytes: [0; PAGE_SIZE],
            binding_epoch: 1,
            scope_id: 0,
            scope_anchor: false,
            scope_anchor_index: NO_SLOT,
            scope_member_next: NO_SLOT,
            scope_member_head: NO_SLOT,
            scope_member_ordinal: NO_SLOT,
            scope_validation_marker: 0,
            scope_vacant_next: NO_SLOT,
            scope_root: NO_SLOT,
            scope_vacant_head: NO_SLOT,
            scope_capacity: 0,
            scope_bound: 0,
            scope_generation: 0,
            scope_sealed: false,
            scope_successor: 0,
            successor_consumed: false,
            index_left: NO_SLOT,
            index_right: NO_SLOT,
            index_height: 1,
            index_available: 1,
            index_in_use: 0,
            index_unscoped_available: 1,
            scope_left: NO_SLOT,
            scope_right: NO_SLOT,
            scope_height: 0,
            scope_available: 0,
            scope_in_use: 0,
            scope_count: 0,
            scope_revision: 0,
            scope_digest: 0,
            scope_vacant_count: 0,
            scope_vacant_revision: 0,
            scope_vacant_digest: 0,
            unscoped_vacant_prev: NO_SLOT,
            unscoped_vacant_next: NO_SLOT,
            saved_binding: SavedBinding::None,
            saved_index_generation: 0,
            saved_index_next: NO_SLOT,
            saved_index_left: NO_SLOT,
            saved_index_right: NO_SLOT,
            saved_index_height: 0,
            saved_index_available: 0,
            saved_index_in_use: 0,
            saved_index_unscoped_available: 0,
            saved_scope_left: NO_SLOT,
            saved_scope_right: NO_SLOT,
            saved_scope_height: 0,
            saved_scope_available: 0,
            saved_scope_in_use: 0,
            saved_scope_count: 0,
            saved_scope_revision: 0,
            saved_scope_digest: 0,
            saved_scope_vacant_count: 0,
            saved_scope_vacant_revision: 0,
            saved_scope_vacant_digest: 0,
            saved_scope_generation: 0,
            saved_scope_root: NO_SLOT,
            saved_scope_vacant_head: NO_SLOT,
            saved_scope_bound: 0,
        }
    }

    #[cfg(test)]
    pub(crate) const fn new(pgno: u32) -> Self {
        let mut slot = Self::authorized(pgno, PrivatePageAuthorization::CommittedFree);
        slot.adapter_owner = Some(PrivatePageOwner::Bitmap);
        slot.adapter_tag = 2;
        slot
    }

    pub(crate) fn authorize_initial(&mut self, pgno: u32, authorization: PrivatePageAuthorization) {
        *self = Self::authorized(pgno, authorization);
    }

    #[cfg(test)]
    pub(crate) fn authorize_generic(&mut self, pgno: u32) {
        self.authorize_initial(pgno, PrivatePageAuthorization::CommittedFree);
        self.adapter_owner = Some(PrivatePageOwner::Bitmap);
        self.adapter_tag = 2;
    }

    pub(crate) fn set_adapter_label(&mut self, owner: PrivatePageOwner, tag: u64) {
        self.adapter_owner = Some(owner);
        self.adapter_tag = tag;
    }

    pub(crate) const fn initial_page_number(&self) -> Option<u32> {
        if self.authorization.is_some() {
            Some(self.pgno)
        } else {
            None
        }
    }

    pub(crate) const fn initial_authorization(&self) -> Option<PrivatePageAuthorization> {
        self.authorization
    }

    pub(crate) const fn initial_state(&self) -> PrivatePagePoolState {
        match self.state {
            PrivatePageState::Vacant => PrivatePagePoolState::Vacant,
            PrivatePageState::Available => PrivatePagePoolState::Available,
            PrivatePageState::InUse {
                owner,
                owner_generation,
                tag,
                ..
            } => PrivatePagePoolState::InUse {
                owner,
                owner_generation,
                tag,
            },
            PrivatePageState::PendingReturn {
                owner,
                owner_generation,
                tag,
                disposition,
                ..
            } => PrivatePagePoolState::PendingReturn {
                owner,
                owner_generation,
                tag,
                disposition,
            },
            PrivatePageState::ReturnedFree => PrivatePagePoolState::ReturnedFree,
            PrivatePageState::ReturnedTail => PrivatePagePoolState::ReturnedTail,
        }
    }

    #[cfg(test)]
    pub(crate) fn preset_bitmap_page(
        &mut self,
        owner_generation: u64,
        tag: u64,
        bytes: [u8; PAGE_SIZE],
    ) {
        self.state = PrivatePageState::InUse {
            owner: PrivatePageOwner::Bitmap,
            owner_generation,
            tag,
            authority_epoch: 1,
        };
        self.allocation_generation = 1;
        self.bytes = bytes;
    }

    #[cfg(test)]
    pub(crate) const fn initial_bytes(&self) -> &[u8; PAGE_SIZE] {
        &self.bytes
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivatePagePoolState {
    Vacant,
    Available,
    InUse {
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
    },
    PendingReturn {
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
        disposition: PrivatePageReturn,
    },
    ReturnedFree,
    ReturnedTail,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageReservationScopeStatus {
    pub(crate) capacity: usize,
    pub(crate) bound: usize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageScopedSlotInfo {
    pub(crate) bound: bool,
    pub(crate) member_ordinal: usize,
    pub(crate) pgno: u32,
    pub(crate) authorization: Option<PrivatePageAuthorization>,
    pub(crate) state: PrivatePagePoolState,
    pub(crate) binding_epoch: u64,
}

/// Exact immutable identity of one transaction-private page retained by a
/// sealed work-unit scope.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageSealedProvenance {
    pub(crate) scope_id: u64,
    pub(crate) scope_anchor: usize,
    pub(crate) scope_generation: u64,
    pub(crate) slot: usize,
    pub(crate) pgno: u32,
    pub(crate) binding_epoch: u64,
    pub(crate) owner: PrivatePageOwner,
    pub(crate) owner_generation: u64,
    pub(crate) tag: u64,
}

/// Opaque commitment to the complete pool state at one exact reservation
/// boundary. Late physical binding uses this to reject both ordinary pool
/// mutations and byte-only drift through an outstanding page guard.
#[cfg_attr(test, derive(Clone))]
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct PrivatePagePoolCommitment {
    identity: usize,
    identity_epoch: usize,
    generation: u64,
    epoch: u64,
    operation_sequence: u64,
    active_operation_id: u64,
    operation_start_epoch: u64,
    abort_required: bool,
    pending_page_count: u64,
    scope_id: u64,
    scope_anchor: usize,
    fingerprint: u64,
}

/// Final state for one page in a prepared composite scope bind.
#[derive(Clone, Copy, Debug)]
pub(crate) enum PrivatePageCompositeBindState {
    Available,
    Bitmap {
        committed_origin: u32,
        stage_slot: usize,
    },
}

/// One exact physical page selected after reclamation verification.
#[derive(Clone, Copy, Debug)]
pub(crate) struct PrivatePageCompositeBind {
    pub(crate) pool_slot: usize,
    pub(crate) pgno: u32,
    pub(crate) authorization: PrivatePageAuthorization,
    pub(crate) state: PrivatePageCompositeBindState,
}

/// One terminal private page proved before a coordinator work unit becomes
/// active. The immutable view of these entries is the complete live apply
/// journal for bitmap and retirement pages.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageCoordinatorTerminalPage {
    pub(crate) pool_slot: usize,
    pub(crate) pgno: u32,
    pub(crate) authorization: PrivatePageAuthorization,
    pub(crate) owner: PrivatePageOwner,
    pub(crate) owner_generation: u64,
    pub(crate) tag: u64,
    pub(crate) bytes: [u8; PAGE_SIZE],
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageCoordinatorPriorReturn {
    pub(crate) page: PrivatePageSealedProvenance,
    pub(crate) nonce: u64,
}

impl PrivatePageCoordinatorPriorReturn {
    pub(crate) const fn empty() -> Self {
        Self {
            page: PrivatePageSealedProvenance {
                scope_id: 0,
                scope_anchor: 0,
                scope_generation: 0,
                slot: 0,
                pgno: 0,
                binding_epoch: 0,
                owner: PrivatePageOwner::Bitmap,
                owner_generation: 0,
                tag: 0,
            },
            nonce: 0,
        }
    }
}

pub(crate) struct PrivatePagePreparedCoordinatorPriorReturns<'plan> {
    pool_identity: usize,
    pool_identity_epoch: usize,
    session_identity: u64,
    session_generation: u64,
    work_identity: u64,
    start_epoch: u64,
    final_epoch: u64,
    returns_address: usize,
    returns_len: usize,
    returns_fingerprint: u64,
    returns: &'plan mut [PrivatePageCoordinatorPriorReturn],
}

pub(crate) struct PrivatePageCoordinatorPriorReturnsFence {
    pool_identity: usize,
    pool_identity_epoch: usize,
    session_identity: u64,
    session_generation: u64,
    work_identity: u64,
    start_epoch: u64,
    final_epoch: u64,
    returns_len: usize,
    returns_fingerprint: u64,
}

impl<'plan> PrivatePagePreparedCoordinatorPriorReturns<'plan> {
    pub(crate) fn returns(&self) -> &[PrivatePageCoordinatorPriorReturn] {
        self.returns
    }

    pub(crate) fn into_returns(self) -> &'plan mut [PrivatePageCoordinatorPriorReturn] {
        self.returns
    }
}

impl PrivatePageCoordinatorTerminalPage {
    pub(crate) const fn empty() -> Self {
        Self {
            pool_slot: NO_SLOT,
            pgno: 0,
            authorization: PrivatePageAuthorization::CommittedFree,
            owner: PrivatePageOwner::Bitmap,
            owner_generation: 0,
            tag: 0,
            bytes: [0; PAGE_SIZE],
        }
    }
}

/// Move-only proof for the callback-free terminal scope apply.
#[derive(Debug)]
pub(crate) struct PrivatePagePreparedCoordinatorTerminal<'plan> {
    pool_identity: usize,
    pool_identity_epoch: usize,
    session_identity: u64,
    session_generation: u64,
    work_identity: u64,
    prepared_scope_address: usize,
    prepared_scope_seal: u64,
    start_epoch: u64,
    final_epoch: u64,
    final_pending_page_count: u64,
    nonce: u64,
    pages_address: usize,
    pages_len: usize,
    pages_fingerprint: u64,
    pages: &'plan mut [PrivatePageCoordinatorTerminalPage],
}

impl<'plan> PrivatePagePreparedCoordinatorTerminal<'plan> {
    pub(crate) const fn pending_page_count(&self) -> u64 {
        self.final_pending_page_count
    }

    pub(crate) const fn nonce(&self) -> u64 {
        self.nonce
    }

    pub(crate) fn pages(&self) -> &[PrivatePageCoordinatorTerminalPage] {
        self.pages
    }

    #[cfg(test)]
    pub(crate) fn into_pages(self) -> &'plan [PrivatePageCoordinatorTerminalPage] {
        self.pages
    }

    /// Cancels a terminal journal before its prepared scope becomes active.
    /// No live pool slot has been changed at this point, so resetting the
    /// caller backing makes it safe for the enclosing whole-draft abort path
    /// to reuse or release it without retaining stale assigned slot numbers.
    pub(crate) fn discard(self) -> &'plan mut [PrivatePageCoordinatorTerminalPage] {
        let Self { pages, .. } = self;
        pages.fill(PrivatePageCoordinatorTerminalPage::empty());
        pages
    }
}

impl PrivatePageCompositeBind {
    pub(crate) const fn empty() -> Self {
        Self {
            pool_slot: NO_SLOT,
            pgno: 0,
            authorization: PrivatePageAuthorization::CommittedFree,
            state: PrivatePageCompositeBindState::Available,
        }
    }
}

/// Move-only proof that every fallible check for a composite scope bind has
/// completed. Applying it may fail only while acquiring the single live pool
/// borrow, before the first mutation.
#[derive(Debug)]
pub(crate) struct PreparedPrivatePageCompositeBind<'plan> {
    pool_identity: usize,
    pool_identity_epoch: usize,
    scope_id: u64,
    scope_anchor: usize,
    scope_generation: u64,
    start: PrivatePagePoolCommitment,
    final_epoch: u64,
    final_pending_page_count: u64,
    bindings: &'plan [PrivatePageCompositeBind],
    stage_commitment: PrivatePagePoolCommitment,
}

impl From<PrivatePageState> for PrivatePagePoolState {
    fn from(value: PrivatePageState) -> Self {
        match value {
            PrivatePageState::Vacant => Self::Vacant,
            PrivatePageState::Available => Self::Available,
            PrivatePageState::InUse {
                owner,
                owner_generation,
                tag,
                ..
            } => Self::InUse {
                owner,
                owner_generation,
                tag,
            },
            PrivatePageState::PendingReturn {
                owner,
                owner_generation,
                tag,
                disposition,
                ..
            } => Self::PendingReturn {
                owner,
                owner_generation,
                tag,
                disposition,
            },
            PrivatePageState::ReturnedFree => Self::ReturnedFree,
            PrivatePageState::ReturnedTail => Self::ReturnedTail,
        }
    }
}

#[derive(Debug)]
pub(crate) struct PrivatePageAuthority<'pool> {
    pool_identity: usize,
    pool_epoch: usize,
    slot: usize,
    pgno: u32,
    owner: PrivatePageOwner,
    owner_generation: u64,
    authority_epoch: u64,
    binding_epoch: u64,
    scope_id: u64,
    _pool: PhantomData<&'pool PrivatePagePool<'pool>>,
}

impl PrivatePageAuthority<'_> {
    pub(crate) const fn page_number(&self) -> u32 {
        self.pgno
    }

    pub(crate) const fn owner(&self) -> PrivatePageOwner {
        self.owner
    }

    pub(crate) const fn owner_generation(&self) -> u64 {
        self.owner_generation
    }
}

#[derive(Debug)]
pub(crate) struct PrivatePagePoolCheckpoint<'pool> {
    pool_identity: usize,
    pool_epoch: usize,
    generation: u64,
    index_root: usize,
    authorized_len: usize,
    available_count: usize,
    lowest_available: usize,
    pending_page_count: u64,
    start_epoch: u64,
    reserved_end_epoch: u64,
    _slots: PhantomData<&'pool mut [PrivatePagePoolSlot]>,
}

#[derive(Debug)]
pub(crate) struct PrivatePageReservationScope<'pool> {
    pool_identity: usize,
    pool_epoch: usize,
    id: u64,
    pending_txn: u64,
    anchor: usize,
    generation: u64,
    _pool: PhantomData<&'pool PrivatePagePool<'pool>>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageReservationScopeSeed {
    pool_identity: usize,
    pool_epoch: usize,
    id: u64,
    pending_txn: u64,
    anchor: usize,
    generation: u64,
}

impl PrivatePageReservationScope<'_> {
    pub(crate) const fn coordinator_scope_id(&self) -> u64 {
        self.id
    }

    pub(crate) const fn seed(&self) -> PrivatePageReservationScopeSeed {
        PrivatePageReservationScopeSeed {
            pool_identity: self.pool_identity,
            pool_epoch: self.pool_epoch,
            id: self.id,
            pending_txn: self.pending_txn,
            anchor: self.anchor,
            generation: self.generation,
        }
    }
}

impl PrivatePageReservationScopeSeed {
    pub(crate) fn materialize<'slots>(
        self,
        pool: &PrivatePagePool<'slots>,
    ) -> PrivatePageReservationScope<'slots> {
        debug_assert_eq!(self.pool_identity, pool.identity);
        debug_assert_eq!(self.pool_epoch, pool.identity_epoch);
        debug_assert_eq!(self.pending_txn, pool.pending_txn);
        PrivatePageReservationScope {
            pool_identity: self.pool_identity,
            pool_epoch: self.pool_epoch,
            id: self.id,
            pending_txn: self.pending_txn,
            anchor: self.anchor,
            generation: self.generation,
            _pool: PhantomData,
        }
    }
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) struct PrivatePagePoolSnapshot<'pool> {
    pool_identity: usize,
    epoch: u64,
    operation_sequence: u64,
    active_operation_id: u64,
    operation_start_epoch: u64,
    abort_required: bool,
    _slots: PhantomData<&'pool mut [PrivatePagePoolSlot]>,
}

#[derive(Debug)]
pub(crate) struct PrivatePageScopedOperationSlot {
    slot: usize,
    binding_epoch: u64,
    binding_steps: usize,
    used_binding_steps: Cell<usize>,
}

impl PrivatePageScopedOperationSlot {
    pub(crate) const fn empty() -> Self {
        Self {
            slot: NO_SLOT,
            binding_epoch: 0,
            binding_steps: 0,
            used_binding_steps: Cell::new(0),
        }
    }

    pub(crate) const fn new(slot: usize, binding_epoch: u64, binding_steps: usize) -> Self {
        Self {
            slot,
            binding_epoch,
            binding_steps,
            used_binding_steps: Cell::new(0),
        }
    }

    pub(crate) const fn slot_number(&self) -> usize {
        self.slot
    }
}

#[derive(Debug)]
pub(crate) struct PrivatePageScopedOperation<'plan> {
    pool_identity: usize,
    pool_epoch: usize,
    id: u64,
    pending_txn: u64,
    generation: u64,
    scope_id: u64,
    scope_anchor: usize,
    start_epoch: u64,
    mutation_steps: usize,
    used_mutation_steps: Cell<usize>,
    slots: &'plan [PrivatePageScopedOperationSlot],
}

impl PrivatePagePoolCheckpoint<'_> {
    pub(crate) const fn generation(&self) -> u64 {
        self.generation
    }
}

impl<'pool> PrivatePageReservationScope<'pool> {
    pub(crate) const fn share(&self) -> Self {
        Self {
            pool_identity: self.pool_identity,
            pool_epoch: self.pool_epoch,
            id: self.id,
            pending_txn: self.pending_txn,
            anchor: self.anchor,
            generation: self.generation,
            _pool: PhantomData,
        }
    }
}

pub(crate) struct PrivatePageRef<'borrow, 'slots> {
    slots: Ref<'borrow, &'slots mut [PrivatePagePoolSlot]>,
    slot: usize,
}

impl core::fmt::Debug for PrivatePageRef<'_, '_> {
    fn fmt(&self, formatter: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        formatter
            .debug_struct("PrivatePageRef")
            .field("slot", &self.slot)
            .finish_non_exhaustive()
    }
}

impl core::ops::Deref for PrivatePageRef<'_, '_> {
    type Target = [u8; PAGE_SIZE];

    fn deref(&self) -> &Self::Target {
        &self.slots[self.slot].bytes
    }
}

pub(crate) struct PrivatePageRefMut<'borrow, 'slots> {
    slots: RefMut<'borrow, &'slots mut [PrivatePagePoolSlot]>,
    slot: usize,
}

impl core::fmt::Debug for PrivatePageRefMut<'_, '_> {
    fn fmt(&self, formatter: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        formatter
            .debug_struct("PrivatePageRefMut")
            .field("slot", &self.slot)
            .finish_non_exhaustive()
    }
}

impl core::ops::Deref for PrivatePageRefMut<'_, '_> {
    type Target = [u8; PAGE_SIZE];

    fn deref(&self) -> &Self::Target {
        &self.slots[self.slot].bytes
    }
}

impl core::ops::DerefMut for PrivatePageRefMut<'_, '_> {
    fn deref_mut(&mut self) -> &mut Self::Target {
        &mut self.slots[self.slot].bytes
    }
}

pub(crate) struct PrivatePagePool<'slots> {
    slots: RefCell<&'slots mut [PrivatePagePoolSlot]>,
    slot_count: usize,
    authorized_len: Cell<usize>,
    available_count: Cell<usize>,
    lowest_available: Cell<usize>,
    committed_page_count: u64,
    pending_page_count: Cell<u64>,
    pending_txn: u64,
    identity: usize,
    identity_epoch: usize,
    invalidation_identity: usize,
    abort_epoch_reserve: u64,
    generation: Cell<u64>,
    epoch: Cell<u64>,
    active_checkpoint: Cell<u64>,
    checkpoint_cleanup_slots: Cell<usize>,
    checkpoint_index_head: Cell<usize>,
    checkpoint_index_count: Cell<usize>,
    operation_sequence: Cell<u64>,
    active_operation_id: Cell<u64>,
    operation_start_epoch: Cell<u64>,
    abort_required: Cell<bool>,
    coordinator_session_identity: Cell<u64>,
    coordinator_session_generation: Cell<u64>,
    coordinator_work_identity: Cell<u64>,
    coordinator_work_generation: Cell<u64>,
    coordinator_work_phase: Cell<PrivatePageCoordinatorWorkPhase>,
    coordinator_work_start_epoch: Cell<u64>,
    coordinator_mutation_started: Cell<bool>,
    coordinator_scope_id: Cell<u64>,
    coordinator_unaccepted_scopes: Cell<usize>,
    coordinator_cleanup_pending: Cell<usize>,
    sealed_coordinator_cleanup_scope_id: Cell<u64>,
    sealed_coordinator_cleanup_nonce: Cell<u64>,
    scope_sequence: Cell<u64>,
    active_scopes: Cell<usize>,
    unscoped_vacant_count: Cell<usize>,
    unscoped_vacant_head: Cell<usize>,
    unscoped_vacant_tail: Cell<usize>,
    index_root: Cell<usize>,
    #[cfg(test)]
    claim_probe_count: Cell<usize>,
    #[cfg(test)]
    scope_lookup_probes: Cell<usize>,
    #[cfg(test)]
    terminal_rebuild_visits: Cell<usize>,
    #[cfg(test)]
    scope_layout_visits: Cell<usize>,
    #[cfg(test)]
    scope_lifecycle_visits: Cell<usize>,
    #[cfg(test)]
    scoped_operation_duplicate_probes: Cell<usize>,
}

impl core::fmt::Debug for PrivatePagePool<'_> {
    fn fmt(&self, formatter: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        formatter
            .debug_struct("PrivatePagePool")
            .field("committed_page_count", &self.committed_page_count)
            .field("pending_page_count", &self.pending_page_count.get())
            .field("pending_txn", &self.pending_txn)
            .field("identity", &self.identity)
            .field("invalidation_identity", &self.invalidation_identity)
            .field("abort_epoch_reserve", &self.abort_epoch_reserve)
            .field("generation", &self.generation.get())
            .field("epoch", &self.epoch.get())
            .field("active_checkpoint", &self.active_checkpoint.get())
            .field("active_operation_id", &self.active_operation_id.get())
            .field("abort_required", &self.abort_required.get())
            .field(
                "coordinator_session_identity",
                &self.coordinator_session_identity.get(),
            )
            .field(
                "coordinator_work_identity",
                &self.coordinator_work_identity.get(),
            )
            .field("coordinator_work_phase", &self.coordinator_work_phase.get())
            .field("active_scopes", &self.active_scopes.get())
            .finish_non_exhaustive()
    }
}

impl<'slots> PrivatePagePool<'slots> {
    pub(crate) fn new(
        slots: &'slots mut [PrivatePagePoolSlot],
        committed_page_count: u64,
        pending_page_count: u64,
        pending_txn: u64,
    ) -> Result<Self, PrivatePagePoolError> {
        if !(2..=MAX_PAGE_COUNT).contains(&committed_page_count)
            || pending_page_count < committed_page_count
            || pending_page_count > MAX_PAGE_COUNT
        {
            return Err(PrivatePagePoolError::PageCountOutOfRange {
                committed: committed_page_count,
                pending: pending_page_count,
            });
        }
        if pending_txn <= 1 {
            return Err(PrivatePagePoolError::PendingTransactionOutOfRange(
                pending_txn,
            ));
        }
        let mut previous = None;
        let mut vacant = false;
        let mut authorized_len = 0;
        let mut available_count = 0;
        for (index, slot) in slots.iter().enumerate() {
            let Some(authorization) = slot.authorization else {
                vacant = true;
                if slot.state != PrivatePageState::Vacant {
                    return Err(PrivatePagePoolError::SlotNotVacant(index));
                }
                continue;
            };
            if vacant {
                return Err(PrivatePagePoolError::AuthorizedAfterVacant(index));
            }
            validate_authorization(
                slot.pgno,
                authorization,
                committed_page_count,
                pending_page_count,
            )?;
            if let Some(prior) = previous {
                if slot.pgno <= prior {
                    return Err(PrivatePagePoolError::PagesNotStrict {
                        previous: prior,
                        current: slot.pgno,
                    });
                }
            }
            match slot.state {
                PrivatePageState::Available if slot.allocation_generation == 0 => {
                    available_count += 1;
                }
                PrivatePageState::InUse {
                    authority_epoch, ..
                } if authority_epoch != 0 && slot.allocation_generation != 0 => {}
                _ => return Err(PrivatePagePoolError::PageUnavailable(slot.pgno)),
            }
            if slot.checkpoint_generation != 0 || slot.saved_state != SavedState::None {
                return Err(PrivatePagePoolError::PageUnavailable(slot.pgno));
            }
            authorized_len += 1;
            previous = Some(slot.pgno);
        }
        let identity = NEXT_POOL_IDENTITY
            .fetch_update(
                Ordering::Relaxed,
                Ordering::Relaxed,
                checked_next_pool_identity,
            )
            .map_err(|_| PrivatePagePoolError::PoolIdentityExhausted)?;
        let slot_count = slots.len();
        for slot in slots.iter_mut() {
            Self::reset_dynamic_metadata(slot, slot.authorization.is_some());
        }
        let (unscoped_vacant_head, unscoped_vacant_tail, unscoped_vacant_count) =
            Self::initialize_unscoped_vacancy_index(slots);
        let mut index_root = NO_SLOT;
        for index in 0..authorized_len {
            index_root = Self::index_insert_plain(slots, index_root, index);
        }
        let lowest_available =
            Self::lowest_unscoped_available_slot(slots, index_root).unwrap_or(slot_count);
        Ok(Self {
            slots: RefCell::new(slots),
            slot_count,
            authorized_len: Cell::new(authorized_len),
            available_count: Cell::new(available_count),
            lowest_available: Cell::new(lowest_available),
            committed_page_count,
            pending_page_count: Cell::new(pending_page_count),
            pending_txn,
            identity,
            identity_epoch: identity,
            invalidation_identity: identity,
            abort_epoch_reserve: 0,
            generation: Cell::new(1),
            epoch: Cell::new(1),
            active_checkpoint: Cell::new(0),
            checkpoint_cleanup_slots: Cell::new(0),
            checkpoint_index_head: Cell::new(NO_SLOT),
            checkpoint_index_count: Cell::new(0),
            operation_sequence: Cell::new(0),
            active_operation_id: Cell::new(0),
            operation_start_epoch: Cell::new(0),
            abort_required: Cell::new(false),
            coordinator_session_identity: Cell::new(0),
            coordinator_session_generation: Cell::new(0),
            coordinator_work_identity: Cell::new(0),
            coordinator_work_generation: Cell::new(0),
            coordinator_work_phase: Cell::new(PrivatePageCoordinatorWorkPhase::None),
            coordinator_work_start_epoch: Cell::new(0),
            coordinator_mutation_started: Cell::new(false),
            coordinator_scope_id: Cell::new(0),
            coordinator_unaccepted_scopes: Cell::new(0),
            coordinator_cleanup_pending: Cell::new(0),
            sealed_coordinator_cleanup_scope_id: Cell::new(0),
            sealed_coordinator_cleanup_nonce: Cell::new(0),
            scope_sequence: Cell::new(0),
            active_scopes: Cell::new(0),
            unscoped_vacant_count: Cell::new(unscoped_vacant_count),
            unscoped_vacant_head: Cell::new(unscoped_vacant_head),
            unscoped_vacant_tail: Cell::new(unscoped_vacant_tail),
            index_root: Cell::new(index_root),
            #[cfg(test)]
            claim_probe_count: Cell::new(0),
            #[cfg(test)]
            scope_lookup_probes: Cell::new(0),
            #[cfg(test)]
            terminal_rebuild_visits: Cell::new(0),
            #[cfg(test)]
            scope_layout_visits: Cell::new(0),
            #[cfg(test)]
            scope_lifecycle_visits: Cell::new(0),
            #[cfg(test)]
            scoped_operation_duplicate_probes: Cell::new(0),
        })
    }

    pub(crate) fn new_vacant(
        slots: &'slots mut [PrivatePagePoolSlot],
        committed_page_count: u64,
        pending_page_count: u64,
        pending_txn: u64,
    ) -> Result<Self, PrivatePagePoolError> {
        validate_pool_bounds(committed_page_count, pending_page_count, pending_txn)?;
        let identity = NEXT_POOL_IDENTITY
            .fetch_update(
                Ordering::Relaxed,
                Ordering::Relaxed,
                checked_next_pool_identity,
            )
            .map_err(|_| PrivatePagePoolError::PoolIdentityExhausted)?;
        for slot in slots.iter_mut() {
            *slot = PrivatePagePoolSlot::empty();
        }
        let slot_count = slots.len();
        let (unscoped_vacant_head, unscoped_vacant_tail, unscoped_vacant_count) =
            Self::initialize_unscoped_vacancy_index(slots);
        Ok(Self {
            slots: RefCell::new(slots),
            slot_count,
            authorized_len: Cell::new(0),
            available_count: Cell::new(0),
            lowest_available: Cell::new(slot_count),
            committed_page_count,
            pending_page_count: Cell::new(pending_page_count),
            pending_txn,
            identity,
            identity_epoch: identity,
            invalidation_identity: identity,
            abort_epoch_reserve: 0,
            generation: Cell::new(1),
            epoch: Cell::new(1),
            active_checkpoint: Cell::new(0),
            checkpoint_cleanup_slots: Cell::new(0),
            checkpoint_index_head: Cell::new(NO_SLOT),
            checkpoint_index_count: Cell::new(0),
            operation_sequence: Cell::new(0),
            active_operation_id: Cell::new(0),
            operation_start_epoch: Cell::new(0),
            abort_required: Cell::new(false),
            coordinator_session_identity: Cell::new(0),
            coordinator_session_generation: Cell::new(0),
            coordinator_work_identity: Cell::new(0),
            coordinator_work_generation: Cell::new(0),
            coordinator_work_phase: Cell::new(PrivatePageCoordinatorWorkPhase::None),
            coordinator_work_start_epoch: Cell::new(0),
            coordinator_mutation_started: Cell::new(false),
            coordinator_scope_id: Cell::new(0),
            coordinator_unaccepted_scopes: Cell::new(0),
            coordinator_cleanup_pending: Cell::new(0),
            sealed_coordinator_cleanup_scope_id: Cell::new(0),
            sealed_coordinator_cleanup_nonce: Cell::new(0),
            scope_sequence: Cell::new(0),
            active_scopes: Cell::new(0),
            unscoped_vacant_count: Cell::new(unscoped_vacant_count),
            unscoped_vacant_head: Cell::new(unscoped_vacant_head),
            unscoped_vacant_tail: Cell::new(unscoped_vacant_tail),
            index_root: Cell::new(NO_SLOT),
            #[cfg(test)]
            claim_probe_count: Cell::new(0),
            #[cfg(test)]
            scope_lookup_probes: Cell::new(0),
            #[cfg(test)]
            terminal_rebuild_visits: Cell::new(0),
            #[cfg(test)]
            scope_layout_visits: Cell::new(0),
            #[cfg(test)]
            scope_lifecycle_visits: Cell::new(0),
            #[cfg(test)]
            scoped_operation_duplicate_probes: Cell::new(0),
        })
    }

    /// Build the vacant pool owned by one unpublished transaction.
    ///
    /// The active identity and the identity used to invalidate all draft
    /// capabilities are reserved atomically before the first slot is changed.
    /// Every failure returns the original caller storage.
    #[allow(clippy::result_large_err)]
    pub(crate) fn new_vacant_transaction(
        slots: &'slots mut [PrivatePagePoolSlot],
        committed_page_count: u64,
        pending_page_count: u64,
        pending_txn: u64,
    ) -> Result<Self, (&'slots mut [PrivatePagePoolSlot], PrivatePagePoolError)> {
        if let Err(error) =
            validate_pool_bounds(committed_page_count, pending_page_count, pending_txn)
        {
            return Err((slots, error));
        }
        let abort_epoch_reserve = match u64::try_from(slots.len()) {
            Ok(value) => value,
            Err(_) => return Err((slots, PrivatePagePoolError::EpochExhausted)),
        };
        if 1u64.checked_add(abort_epoch_reserve).is_none() {
            return Err((slots, PrivatePagePoolError::EpochExhausted));
        }
        let (identity, invalidation_identity) =
            match reserve_pool_identity_pair(&NEXT_POOL_IDENTITY) {
                Some(pair) => pair,
                None => return Err((slots, PrivatePagePoolError::PoolIdentityExhausted)),
            };

        for slot in slots.iter_mut() {
            *slot = PrivatePagePoolSlot::empty();
        }
        let slot_count = slots.len();
        let (unscoped_vacant_head, unscoped_vacant_tail, unscoped_vacant_count) =
            Self::initialize_unscoped_vacancy_index(slots);
        Ok(Self {
            slots: RefCell::new(slots),
            slot_count,
            authorized_len: Cell::new(0),
            available_count: Cell::new(0),
            lowest_available: Cell::new(slot_count),
            committed_page_count,
            pending_page_count: Cell::new(pending_page_count),
            pending_txn,
            identity,
            identity_epoch: identity,
            invalidation_identity,
            abort_epoch_reserve,
            generation: Cell::new(1),
            epoch: Cell::new(1),
            active_checkpoint: Cell::new(0),
            checkpoint_cleanup_slots: Cell::new(0),
            checkpoint_index_head: Cell::new(NO_SLOT),
            checkpoint_index_count: Cell::new(0),
            operation_sequence: Cell::new(0),
            active_operation_id: Cell::new(0),
            operation_start_epoch: Cell::new(0),
            abort_required: Cell::new(false),
            coordinator_session_identity: Cell::new(0),
            coordinator_session_generation: Cell::new(0),
            coordinator_work_identity: Cell::new(0),
            coordinator_work_generation: Cell::new(0),
            coordinator_work_phase: Cell::new(PrivatePageCoordinatorWorkPhase::None),
            coordinator_work_start_epoch: Cell::new(0),
            coordinator_mutation_started: Cell::new(false),
            coordinator_scope_id: Cell::new(0),
            coordinator_unaccepted_scopes: Cell::new(0),
            coordinator_cleanup_pending: Cell::new(0),
            sealed_coordinator_cleanup_scope_id: Cell::new(0),
            sealed_coordinator_cleanup_nonce: Cell::new(0),
            scope_sequence: Cell::new(0),
            active_scopes: Cell::new(0),
            unscoped_vacant_count: Cell::new(unscoped_vacant_count),
            unscoped_vacant_head: Cell::new(unscoped_vacant_head),
            unscoped_vacant_tail: Cell::new(unscoped_vacant_tail),
            index_root: Cell::new(NO_SLOT),
            #[cfg(test)]
            claim_probe_count: Cell::new(0),
            #[cfg(test)]
            scope_lookup_probes: Cell::new(0),
            #[cfg(test)]
            terminal_rebuild_visits: Cell::new(0),
            #[cfg(test)]
            scope_layout_visits: Cell::new(0),
            #[cfg(test)]
            scope_lifecycle_visits: Cell::new(0),
            #[cfg(test)]
            scoped_operation_duplicate_probes: Cell::new(0),
        })
    }

    fn reset_dynamic_metadata(slot: &mut PrivatePagePoolSlot, bound: bool) {
        slot.binding_epoch = 1;
        slot.scope_id = 0;
        slot.scope_anchor = false;
        slot.scope_anchor_index = NO_SLOT;
        slot.scope_member_next = NO_SLOT;
        slot.scope_member_head = NO_SLOT;
        slot.scope_member_ordinal = NO_SLOT;
        slot.scope_validation_marker = 0;
        slot.scope_vacant_next = NO_SLOT;
        slot.scope_root = NO_SLOT;
        slot.scope_vacant_head = NO_SLOT;
        slot.scope_capacity = 0;
        slot.scope_bound = 0;
        slot.scope_generation = 0;
        slot.scope_sealed = false;
        slot.scope_successor = 0;
        slot.successor_consumed = false;
        slot.index_left = NO_SLOT;
        slot.index_right = NO_SLOT;
        slot.index_height = u8::from(bound);
        slot.index_available = usize::from(bound && slot.state == PrivatePageState::Available);
        slot.index_in_use =
            usize::from(bound && matches!(slot.state, PrivatePageState::InUse { .. }));
        slot.index_unscoped_available = slot.index_available;
        slot.scope_left = NO_SLOT;
        slot.scope_right = NO_SLOT;
        slot.scope_height = 0;
        slot.scope_available = 0;
        slot.scope_in_use = 0;
        slot.scope_count = 0;
        slot.scope_revision = 0;
        slot.scope_digest = 0;
        slot.scope_vacant_count = 0;
        slot.scope_vacant_revision = 0;
        slot.scope_vacant_digest = 0;
        slot.unscoped_vacant_prev = NO_SLOT;
        slot.unscoped_vacant_next = NO_SLOT;
        slot.saved_binding = SavedBinding::None;
        slot.saved_index_generation = 0;
        slot.saved_index_next = NO_SLOT;
        slot.saved_index_left = NO_SLOT;
        slot.saved_index_right = NO_SLOT;
        slot.saved_index_height = 0;
        slot.saved_index_available = 0;
        slot.saved_index_in_use = 0;
        slot.saved_index_unscoped_available = 0;
        slot.saved_scope_left = NO_SLOT;
        slot.saved_scope_right = NO_SLOT;
        slot.saved_scope_height = 0;
        slot.saved_scope_available = 0;
        slot.saved_scope_in_use = 0;
        slot.saved_scope_count = 0;
        slot.saved_scope_revision = 0;
        slot.saved_scope_digest = 0;
        slot.saved_scope_vacant_count = 0;
        slot.saved_scope_vacant_revision = 0;
        slot.saved_scope_vacant_digest = 0;
        slot.saved_scope_generation = 0;
        slot.saved_scope_root = NO_SLOT;
        slot.saved_scope_vacant_head = NO_SLOT;
        slot.saved_scope_bound = 0;
    }

    fn initialize_unscoped_vacancy_index(
        slots: &mut [PrivatePagePoolSlot],
    ) -> (usize, usize, usize) {
        let mut head = NO_SLOT;
        let mut tail = NO_SLOT;
        let mut count = 0usize;
        for index in 0..slots.len() {
            if slots[index].authorization.is_some()
                || slots[index].state != PrivatePageState::Vacant
                || slots[index].scope_id != 0
            {
                continue;
            }
            slots[index].unscoped_vacant_prev = tail;
            slots[index].unscoped_vacant_next = NO_SLOT;
            if tail == NO_SLOT {
                head = index;
            } else {
                slots[tail].unscoped_vacant_next = index;
            }
            tail = index;
            count += 1;
        }
        (head, tail, count)
    }

    fn record_scope_lifecycle_visits(&self, visits: usize) {
        #[cfg(test)]
        self.scope_lifecycle_visits
            .set(self.scope_lifecycle_visits.get().saturating_add(visits));
        #[cfg(not(test))]
        let _ = visits;
    }

    fn is_canonical_vacant_payload(slot: &PrivatePagePoolSlot) -> bool {
        slot.pgno == 0
            && slot.authorization.is_none()
            && slot.state == PrivatePageState::Vacant
            && slot.allocation_generation == 0
            && slot.checkpoint_generation == 0
            && slot.saved_state == SavedState::None
            && slot.adapter_owner.is_none()
            && slot.adapter_tag == 0
            && slot.bytes == [0; PAGE_SIZE]
            && slot.index_left == NO_SLOT
            && slot.index_right == NO_SLOT
            && slot.index_height == 0
            && slot.index_available == 0
            && slot.index_in_use == 0
            && slot.index_unscoped_available == 0
            && slot.scope_left == NO_SLOT
            && slot.scope_right == NO_SLOT
            && slot.scope_height == 0
            && slot.scope_available == 0
            && slot.scope_in_use == 0
            && slot.scope_validation_marker == 0
            && slot.saved_binding == SavedBinding::None
            && slot.saved_index_generation == 0
            && slot.saved_index_next == NO_SLOT
            && slot.saved_scope_generation == 0
    }

    fn is_canonical_unscoped_vacancy(slot: &PrivatePagePoolSlot) -> bool {
        Self::is_canonical_vacant_payload(slot)
            && slot.scope_id == 0
            && !slot.scope_anchor
            && slot.scope_anchor_index == NO_SLOT
            && slot.scope_member_next == NO_SLOT
            && slot.scope_member_head == NO_SLOT
            && slot.scope_member_ordinal == NO_SLOT
            && slot.scope_vacant_next == NO_SLOT
            && slot.scope_root == NO_SLOT
            && slot.scope_vacant_head == NO_SLOT
            && slot.scope_capacity == 0
            && slot.scope_bound == 0
    }

    fn is_runtime_scoped_vacant_payload(slot: &PrivatePagePoolSlot, checkpoint: u64) -> bool {
        let checkpoint_metadata = if slot.checkpoint_generation == 0 {
            slot.saved_state == SavedState::None && slot.saved_binding == SavedBinding::None
        } else {
            slot.checkpoint_generation == checkpoint
                && matches!(slot.saved_state, SavedState::State(_))
                && matches!(slot.saved_binding, SavedBinding::Binding { .. })
        };
        slot.pgno == 0
            && slot.authorization.is_none()
            && slot.state == PrivatePageState::Vacant
            && slot.allocation_generation == 0
            && checkpoint_metadata
            && slot.adapter_owner.is_none()
            && slot.adapter_tag == 0
            && slot.bytes == [0; PAGE_SIZE]
            && slot.index_left == NO_SLOT
            && slot.index_right == NO_SLOT
            && slot.index_height == 0
            && slot.index_available == 0
            && slot.index_in_use == 0
            && slot.index_unscoped_available == 0
            && slot.scope_left == NO_SLOT
            && slot.scope_right == NO_SLOT
            && slot.scope_height == 0
            && slot.scope_available == 0
            && slot.scope_in_use == 0
            && slot.unscoped_vacant_prev == NO_SLOT
            && slot.unscoped_vacant_next == NO_SLOT
            && slot.scope_validation_marker == 0
            && ((slot.saved_index_generation == 0 && slot.saved_index_next == NO_SLOT)
                || slot.saved_index_generation == checkpoint)
            && (slot.saved_scope_generation == 0 || slot.saved_scope_generation == checkpoint)
    }

    fn validate_scoped_vacancy_slot(
        &self,
        slots: &[PrivatePagePoolSlot],
        scope: &PrivatePageReservationScope<'_>,
        anchor: usize,
        index: usize,
        checkpoint: u64,
    ) -> Result<usize, PrivatePagePoolError> {
        let page = slots.get(index).ok_or(PrivatePagePoolError::StaleScope)?;
        let capacity = slots[anchor].scope_capacity;
        if !Self::is_runtime_scoped_vacant_payload(page, checkpoint)
            || page.scope_id != scope.id
            || page.scope_anchor_index != anchor
            || page.scope_member_ordinal >= capacity
            || if index == anchor {
                !page.scope_anchor
                    || page.scope_member_head != anchor
                    || page.scope_member_ordinal != 0
                    || page.scope_capacity != capacity
                    || page.scope_bound != slots[anchor].scope_bound
                    || page.scope_vacant_head != slots[anchor].scope_vacant_head
            } else {
                page.scope_anchor
                    || page.scope_member_head != NO_SLOT
                    || page.scope_member_ordinal == 0
                    || page.scope_root != NO_SLOT
                    || page.scope_vacant_head != NO_SLOT
                    || page.scope_capacity != 0
                    || page.scope_bound != 0
            }
        {
            return Err(PrivatePagePoolError::InvalidState(index));
        }
        Ok(page.scope_vacant_next)
    }

    fn validate_scope_vacancy_head_window(
        &self,
        slots: &[PrivatePagePoolSlot],
        scope: &PrivatePageReservationScope<'_>,
        anchor: usize,
        vacant_count: usize,
    ) -> Result<usize, PrivatePagePoolError> {
        let head = slots[anchor].scope_vacant_head;
        if vacant_count == 0 {
            return if head == NO_SLOT {
                Ok(NO_SLOT)
            } else {
                Err(PrivatePagePoolError::StaleScope)
            };
        }
        if head == NO_SLOT {
            return Err(PrivatePagePoolError::StaleScope);
        }
        let checkpoint = self.active_checkpoint.get();
        let next = self.validate_scoped_vacancy_slot(slots, scope, anchor, head, checkpoint)?;
        if vacant_count == 1 {
            return if next == NO_SLOT {
                Ok(head)
            } else {
                Err(PrivatePagePoolError::StaleScope)
            };
        }
        if next == NO_SLOT || next == head {
            return Err(PrivatePagePoolError::StaleScope);
        }
        let after = self.validate_scoped_vacancy_slot(slots, scope, anchor, next, checkpoint)?;
        if vacant_count == 2 {
            return if after == NO_SLOT {
                Ok(head)
            } else {
                Err(PrivatePagePoolError::StaleScope)
            };
        }
        if after == NO_SLOT || after == head || after == next {
            return Err(PrivatePagePoolError::StaleScope);
        }
        self.validate_scoped_vacancy_slot(slots, scope, anchor, after, checkpoint)?;
        Ok(head)
    }

    fn validate_unscoped_vacancy_prefix(
        &self,
        slots: &[PrivatePagePoolSlot],
        count: usize,
    ) -> Result<usize, PrivatePagePoolError> {
        let available = self.unscoped_vacant_count.get();
        let head = self.unscoped_vacant_head.get();
        let tail = self.unscoped_vacant_tail.get();
        if available == 0 {
            return if head == NO_SLOT && tail == NO_SLOT && count == 0 {
                Ok(NO_SLOT)
            } else {
                Err(PrivatePagePoolError::StaleScope)
            };
        }
        if head == NO_SLOT || tail == NO_SLOT || head >= slots.len() || tail >= slots.len() {
            return Err(PrivatePagePoolError::StaleScope);
        }
        let mut current = head;
        let mut previous = NO_SLOT;
        for ordinal in 0..count {
            self.record_scope_lifecycle_visits(1);
            if current == NO_SLOT || current >= slots.len() {
                return Err(PrivatePagePoolError::StaleScope);
            }
            let slot = &slots[current];
            if !Self::is_canonical_unscoped_vacancy(slot) || slot.unscoped_vacant_prev != previous {
                return Err(PrivatePagePoolError::InvalidState(current));
            }
            let next = slot.unscoped_vacant_next;
            if next != NO_SLOT
                && (next >= slots.len() || slots[next].unscoped_vacant_prev != current)
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            previous = current;
            current = next;
            if ordinal + 1 < count && current == NO_SLOT {
                return Err(PrivatePagePoolError::StaleScope);
            }
        }
        if count == available {
            if current != NO_SLOT || previous != tail {
                return Err(PrivatePagePoolError::StaleScope);
            }
        } else {
            if current == NO_SLOT {
                return Err(PrivatePagePoolError::StaleScope);
            }
            self.validate_unscoped_vacancy_member(slots, current)?;
        }
        Ok(head)
    }

    fn validate_unscoped_vacancy_boundary(
        &self,
        slots: &[PrivatePagePoolSlot],
    ) -> Result<(), PrivatePagePoolError> {
        let count = self.unscoped_vacant_count.get();
        let head = self.unscoped_vacant_head.get();
        let tail = self.unscoped_vacant_tail.get();
        if count == 0 {
            return if head == NO_SLOT && tail == NO_SLOT {
                Ok(())
            } else {
                Err(PrivatePagePoolError::StaleScope)
            };
        }
        if count > slots.len()
            || head == NO_SLOT
            || tail == NO_SLOT
            || head >= slots.len()
            || tail >= slots.len()
        {
            return Err(PrivatePagePoolError::StaleScope);
        }
        let head_slot = &slots[head];
        let tail_slot = &slots[tail];
        for (index, slot) in [(head, head_slot), (tail, tail_slot)] {
            if !Self::is_canonical_unscoped_vacancy(slot) {
                return Err(PrivatePagePoolError::InvalidState(index));
            }
        }
        if head_slot.unscoped_vacant_prev != NO_SLOT || tail_slot.unscoped_vacant_next != NO_SLOT {
            return Err(PrivatePagePoolError::StaleScope);
        }
        if count == 1 {
            return if head == tail
                && head_slot.unscoped_vacant_next == NO_SLOT
                && tail_slot.unscoped_vacant_prev == NO_SLOT
            {
                Ok(())
            } else {
                Err(PrivatePagePoolError::StaleScope)
            };
        }
        let head_next = head_slot.unscoped_vacant_next;
        let tail_previous = tail_slot.unscoped_vacant_prev;
        if head == tail
            || head_next == NO_SLOT
            || head_next >= slots.len()
            || slots[head_next].unscoped_vacant_prev != head
            || tail_previous == NO_SLOT
            || tail_previous >= slots.len()
            || slots[tail_previous].unscoped_vacant_next != tail
        {
            return Err(PrivatePagePoolError::StaleScope);
        }
        Ok(())
    }

    fn validate_unscoped_vacancy_member(
        &self,
        slots: &[PrivatePagePoolSlot],
        index: usize,
    ) -> Result<(), PrivatePagePoolError> {
        if index >= slots.len() || self.unscoped_vacant_count.get() == 0 {
            return Err(PrivatePagePoolError::StaleScope);
        }
        let slot = &slots[index];
        if !Self::is_canonical_unscoped_vacancy(slot) {
            return Err(PrivatePagePoolError::InvalidState(index));
        }
        let previous = slot.unscoped_vacant_prev;
        let next = slot.unscoped_vacant_next;
        if (previous == NO_SLOT && self.unscoped_vacant_head.get() != index)
            || (previous != NO_SLOT
                && (previous >= slots.len() || slots[previous].unscoped_vacant_next != index))
            || (next == NO_SLOT && self.unscoped_vacant_tail.get() != index)
            || (next != NO_SLOT
                && (next >= slots.len() || slots[next].unscoped_vacant_prev != index))
        {
            return Err(PrivatePagePoolError::StaleScope);
        }
        if previous != NO_SLOT {
            let previous_slot = &slots[previous];
            if !Self::is_canonical_unscoped_vacancy(previous_slot) {
                return Err(PrivatePagePoolError::InvalidState(previous));
            }
            let before_previous = previous_slot.unscoped_vacant_prev;
            if (before_previous == NO_SLOT && self.unscoped_vacant_head.get() != previous)
                || (before_previous != NO_SLOT
                    && (before_previous >= slots.len()
                        || slots[before_previous].unscoped_vacant_next != previous))
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
        }
        if next != NO_SLOT {
            let next_slot = &slots[next];
            if !Self::is_canonical_unscoped_vacancy(next_slot) {
                return Err(PrivatePagePoolError::InvalidState(next));
            }
            let after_next = next_slot.unscoped_vacant_next;
            if (after_next == NO_SLOT && self.unscoped_vacant_tail.get() != next)
                || (after_next != NO_SLOT
                    && (after_next >= slots.len()
                        || slots[after_next].unscoped_vacant_prev != next))
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
        }
        Ok(())
    }

    fn remove_unscoped_vacancy_prepared(&self, slots: &mut [PrivatePagePoolSlot], index: usize) {
        let previous = slots[index].unscoped_vacant_prev;
        let next = slots[index].unscoped_vacant_next;
        if previous == NO_SLOT {
            debug_assert_eq!(self.unscoped_vacant_head.get(), index);
            self.unscoped_vacant_head.set(next);
        } else {
            slots[previous].unscoped_vacant_next = next;
        }
        if next == NO_SLOT {
            debug_assert_eq!(self.unscoped_vacant_tail.get(), index);
            self.unscoped_vacant_tail.set(previous);
        } else {
            slots[next].unscoped_vacant_prev = previous;
        }
        slots[index].unscoped_vacant_prev = NO_SLOT;
        slots[index].unscoped_vacant_next = NO_SLOT;
        self.unscoped_vacant_count
            .set(self.unscoped_vacant_count.get() - 1);
        self.record_scope_lifecycle_visits(1);
    }

    fn append_unscoped_vacancy_prepared(&self, slots: &mut [PrivatePagePoolSlot], index: usize) {
        let tail = self.unscoped_vacant_tail.get();
        slots[index].unscoped_vacant_prev = tail;
        slots[index].unscoped_vacant_next = NO_SLOT;
        if tail == NO_SLOT {
            debug_assert_eq!(self.unscoped_vacant_head.get(), NO_SLOT);
            self.unscoped_vacant_head.set(index);
        } else {
            slots[tail].unscoped_vacant_next = index;
        }
        self.unscoped_vacant_tail.set(index);
        self.unscoped_vacant_count
            .set(self.unscoped_vacant_count.get() + 1);
        self.record_scope_lifecycle_visits(1);
    }

    fn index_height(slots: &[PrivatePagePoolSlot], index: usize) -> u8 {
        if index == NO_SLOT {
            0
        } else {
            slots[index].index_height
        }
    }

    fn refresh_index_node(slots: &mut [PrivatePagePoolSlot], index: usize) {
        let left = slots[index].index_left;
        let right = slots[index].index_right;
        let mut available = usize::from(slots[index].state == PrivatePageState::Available);
        let mut in_use = usize::from(matches!(slots[index].state, PrivatePageState::InUse { .. }));
        let mut unscoped_available = usize::from(
            slots[index].scope_id == 0 && slots[index].state == PrivatePageState::Available,
        );
        if left != NO_SLOT {
            available += slots[left].index_available;
            in_use += slots[left].index_in_use;
            unscoped_available += slots[left].index_unscoped_available;
        }
        if right != NO_SLOT {
            available += slots[right].index_available;
            in_use += slots[right].index_in_use;
            unscoped_available += slots[right].index_unscoped_available;
        }
        slots[index].index_height =
            1 + Self::index_height(slots, left).max(Self::index_height(slots, right));
        slots[index].index_available = available;
        slots[index].index_in_use = in_use;
        slots[index].index_unscoped_available = unscoped_available;
    }

    fn rotate_index_right(slots: &mut [PrivatePagePoolSlot], root: usize) -> usize {
        let left = slots[root].index_left;
        slots[root].index_left = slots[left].index_right;
        slots[left].index_right = root;
        Self::refresh_index_node(slots, root);
        Self::refresh_index_node(slots, left);
        left
    }

    fn rotate_index_left(slots: &mut [PrivatePagePoolSlot], root: usize) -> usize {
        let right = slots[root].index_right;
        slots[root].index_right = slots[right].index_left;
        slots[right].index_left = root;
        Self::refresh_index_node(slots, root);
        Self::refresh_index_node(slots, right);
        right
    }

    fn rebalance_index(slots: &mut [PrivatePagePoolSlot], root: usize) -> usize {
        Self::refresh_index_node(slots, root);
        let left = slots[root].index_left;
        let right = slots[root].index_right;
        let balance = i16::from(Self::index_height(slots, left))
            - i16::from(Self::index_height(slots, right));
        if balance > 1 {
            if Self::index_height(slots, slots[left].index_right)
                > Self::index_height(slots, slots[left].index_left)
            {
                slots[root].index_left = Self::rotate_index_left(slots, left);
            }
            return Self::rotate_index_right(slots, root);
        }
        if balance < -1 {
            if Self::index_height(slots, slots[right].index_left)
                > Self::index_height(slots, slots[right].index_right)
            {
                slots[root].index_right = Self::rotate_index_right(slots, right);
            }
            return Self::rotate_index_left(slots, root);
        }
        root
    }

    fn index_insert_plain(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        inserted: usize,
    ) -> usize {
        if root == NO_SLOT {
            return inserted;
        }
        if slots[inserted].pgno < slots[root].pgno {
            slots[root].index_left =
                Self::index_insert_plain(slots, slots[root].index_left, inserted);
        } else {
            slots[root].index_right =
                Self::index_insert_plain(slots, slots[root].index_right, inserted);
        }
        Self::rebalance_index(slots, root)
    }

    fn remember_index(slots: &mut [PrivatePagePoolSlot], index: usize, generation: u64) {
        if index == NO_SLOT || slots[index].saved_index_generation == generation {
            return;
        }
        let slot = &mut slots[index];
        slot.saved_index_generation = generation;
        slot.saved_index_left = slot.index_left;
        slot.saved_index_right = slot.index_right;
        slot.saved_index_height = slot.index_height;
        slot.saved_index_available = slot.index_available;
        slot.saved_index_in_use = slot.index_in_use;
        slot.saved_index_unscoped_available = slot.index_unscoped_available;
        slot.saved_scope_left = slot.scope_left;
        slot.saved_scope_right = slot.scope_right;
        slot.saved_scope_height = slot.scope_height;
        slot.saved_scope_available = slot.scope_available;
        slot.saved_scope_in_use = slot.scope_in_use;
        slot.saved_scope_count = slot.scope_count;
        slot.saved_scope_revision = slot.scope_revision;
        slot.saved_scope_digest = slot.scope_digest;
        slot.saved_scope_vacant_count = slot.scope_vacant_count;
        slot.saved_scope_vacant_revision = slot.scope_vacant_revision;
        slot.saved_scope_vacant_digest = slot.scope_vacant_digest;
    }

    fn remember_index_in_journal(
        &self,
        slots: &mut [PrivatePagePoolSlot],
        index: usize,
        generation: u64,
    ) {
        if index == NO_SLOT || slots[index].saved_index_generation == generation {
            return;
        }
        Self::remember_index(slots, index, generation);
        slots[index].saved_index_next = self.checkpoint_index_head.get();
        self.checkpoint_index_head.set(index);
        self.checkpoint_index_count
            .set(self.checkpoint_index_count.get() + 1);
    }

    fn rotate_index_right_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        generation: u64,
    ) -> usize {
        let left = slots[root].index_left;
        Self::remember_index(slots, root, generation);
        Self::remember_index(slots, left, generation);
        Self::rotate_index_right(slots, root)
    }

    fn rotate_index_left_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        generation: u64,
    ) -> usize {
        let right = slots[root].index_right;
        Self::remember_index(slots, root, generation);
        Self::remember_index(slots, right, generation);
        Self::rotate_index_left(slots, root)
    }

    fn rebalance_index_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        generation: u64,
    ) -> usize {
        Self::remember_index(slots, root, generation);
        Self::refresh_index_node(slots, root);
        let left = slots[root].index_left;
        let right = slots[root].index_right;
        let balance = i16::from(Self::index_height(slots, left))
            - i16::from(Self::index_height(slots, right));
        if balance > 1 {
            if Self::index_height(slots, slots[left].index_right)
                > Self::index_height(slots, slots[left].index_left)
            {
                slots[root].index_left = Self::rotate_index_left_prepared(slots, left, generation);
            }
            return Self::rotate_index_right_prepared(slots, root, generation);
        }
        if balance < -1 {
            if Self::index_height(slots, slots[right].index_left)
                > Self::index_height(slots, slots[right].index_right)
            {
                slots[root].index_right =
                    Self::rotate_index_right_prepared(slots, right, generation);
            }
            return Self::rotate_index_left_prepared(slots, root, generation);
        }
        root
    }

    fn index_insert_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        inserted: usize,
        generation: u64,
    ) -> usize {
        if root == NO_SLOT {
            Self::remember_index(slots, inserted, generation);
            return inserted;
        }
        Self::remember_index(slots, root, generation);
        if slots[inserted].pgno < slots[root].pgno {
            slots[root].index_left =
                Self::index_insert_prepared(slots, slots[root].index_left, inserted, generation);
        } else {
            slots[root].index_right =
                Self::index_insert_prepared(slots, slots[root].index_right, inserted, generation);
        }
        Self::rebalance_index_prepared(slots, root, generation)
    }

    fn detach_index_minimum_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        generation: u64,
    ) -> (usize, usize) {
        Self::remember_index(slots, root, generation);
        if slots[root].index_left == NO_SLOT {
            return (slots[root].index_right, root);
        }
        let (left, minimum) =
            Self::detach_index_minimum_prepared(slots, slots[root].index_left, generation);
        slots[root].index_left = left;
        (
            Self::rebalance_index_prepared(slots, root, generation),
            minimum,
        )
    }

    fn index_delete_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        pgno: u32,
        generation: u64,
    ) -> (usize, usize) {
        Self::remember_index(slots, root, generation);
        if pgno < slots[root].pgno {
            let (left, removed) =
                Self::index_delete_prepared(slots, slots[root].index_left, pgno, generation);
            slots[root].index_left = left;
            return (
                Self::rebalance_index_prepared(slots, root, generation),
                removed,
            );
        }
        if pgno > slots[root].pgno {
            let (right, removed) =
                Self::index_delete_prepared(slots, slots[root].index_right, pgno, generation);
            slots[root].index_right = right;
            return (
                Self::rebalance_index_prepared(slots, root, generation),
                removed,
            );
        }
        let left = slots[root].index_left;
        let right = slots[root].index_right;
        if left == NO_SLOT {
            return (right, root);
        }
        if right == NO_SLOT {
            return (left, root);
        }
        let (right, successor) = Self::detach_index_minimum_prepared(slots, right, generation);
        Self::remember_index(slots, successor, generation);
        slots[successor].index_left = left;
        slots[successor].index_right = right;
        (
            Self::rebalance_index_prepared(slots, successor, generation),
            root,
        )
    }

    fn scope_height(slots: &[PrivatePagePoolSlot], index: usize) -> u8 {
        if index == NO_SLOT {
            0
        } else {
            slots[index].scope_height
        }
    }

    fn refresh_scope_node(slots: &mut [PrivatePagePoolSlot], index: usize) {
        let left = slots[index].scope_left;
        let right = slots[index].scope_right;
        let mut available = usize::from(slots[index].state == PrivatePageState::Available);
        let mut in_use = usize::from(matches!(slots[index].state, PrivatePageState::InUse { .. }));
        let mut count = 1usize;
        let mut revision = private_page_scope_payload_revision(&slots[index]);
        let mut digest = private_page_scope_payload_digest(index, &slots[index]);
        if left != NO_SLOT {
            available += slots[left].scope_available;
            in_use += slots[left].scope_in_use;
            count += slots[left].scope_count;
            revision = revision.wrapping_add(slots[left].scope_revision);
            digest ^= slots[left].scope_digest.rotate_left(7);
        }
        if right != NO_SLOT {
            available += slots[right].scope_available;
            in_use += slots[right].scope_in_use;
            count += slots[right].scope_count;
            revision = revision.wrapping_add(slots[right].scope_revision);
            digest ^= slots[right].scope_digest.rotate_left(37);
        }
        slots[index].scope_height =
            1 + Self::scope_height(slots, left).max(Self::scope_height(slots, right));
        slots[index].scope_available = available;
        slots[index].scope_in_use = in_use;
        slots[index].scope_count = count;
        slots[index].scope_revision = revision;
        slots[index].scope_digest = digest;
    }

    fn rotate_scope_right_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        generation: u64,
    ) -> usize {
        let left = slots[root].scope_left;
        Self::remember_index(slots, root, generation);
        Self::remember_index(slots, left, generation);
        slots[root].scope_left = slots[left].scope_right;
        slots[left].scope_right = root;
        Self::refresh_scope_node(slots, root);
        Self::refresh_scope_node(slots, left);
        left
    }

    fn rotate_scope_left_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        generation: u64,
    ) -> usize {
        let right = slots[root].scope_right;
        Self::remember_index(slots, root, generation);
        Self::remember_index(slots, right, generation);
        slots[root].scope_right = slots[right].scope_left;
        slots[right].scope_left = root;
        Self::refresh_scope_node(slots, root);
        Self::refresh_scope_node(slots, right);
        right
    }

    fn rebalance_scope_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        generation: u64,
    ) -> usize {
        Self::remember_index(slots, root, generation);
        Self::refresh_scope_node(slots, root);
        let left = slots[root].scope_left;
        let right = slots[root].scope_right;
        let balance = i16::from(Self::scope_height(slots, left))
            - i16::from(Self::scope_height(slots, right));
        if balance > 1 {
            if Self::scope_height(slots, slots[left].scope_right)
                > Self::scope_height(slots, slots[left].scope_left)
            {
                slots[root].scope_left = Self::rotate_scope_left_prepared(slots, left, generation);
            }
            return Self::rotate_scope_right_prepared(slots, root, generation);
        }
        if balance < -1 {
            if Self::scope_height(slots, slots[right].scope_left)
                > Self::scope_height(slots, slots[right].scope_right)
            {
                slots[root].scope_right =
                    Self::rotate_scope_right_prepared(slots, right, generation);
            }
            return Self::rotate_scope_left_prepared(slots, root, generation);
        }
        root
    }

    fn scope_insert_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        inserted: usize,
        generation: u64,
    ) -> usize {
        if root == NO_SLOT {
            Self::remember_index(slots, inserted, generation);
            return inserted;
        }
        Self::remember_index(slots, root, generation);
        if slots[inserted].pgno < slots[root].pgno {
            slots[root].scope_left =
                Self::scope_insert_prepared(slots, slots[root].scope_left, inserted, generation);
        } else {
            slots[root].scope_right =
                Self::scope_insert_prepared(slots, slots[root].scope_right, inserted, generation);
        }
        Self::rebalance_scope_prepared(slots, root, generation)
    }

    fn detach_scope_minimum_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        generation: u64,
    ) -> (usize, usize) {
        Self::remember_index(slots, root, generation);
        if slots[root].scope_left == NO_SLOT {
            return (slots[root].scope_right, root);
        }
        let (left, minimum) =
            Self::detach_scope_minimum_prepared(slots, slots[root].scope_left, generation);
        slots[root].scope_left = left;
        (
            Self::rebalance_scope_prepared(slots, root, generation),
            minimum,
        )
    }

    fn scope_delete_prepared(
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        pgno: u32,
        generation: u64,
    ) -> (usize, usize) {
        Self::remember_index(slots, root, generation);
        if pgno < slots[root].pgno {
            let (left, removed) =
                Self::scope_delete_prepared(slots, slots[root].scope_left, pgno, generation);
            slots[root].scope_left = left;
            return (
                Self::rebalance_scope_prepared(slots, root, generation),
                removed,
            );
        }
        if pgno > slots[root].pgno {
            let (right, removed) =
                Self::scope_delete_prepared(slots, slots[root].scope_right, pgno, generation);
            slots[root].scope_right = right;
            return (
                Self::rebalance_scope_prepared(slots, root, generation),
                removed,
            );
        }
        let left = slots[root].scope_left;
        let right = slots[root].scope_right;
        if left == NO_SLOT {
            return (right, root);
        }
        if right == NO_SLOT {
            return (left, root);
        }
        let (right, successor) = Self::detach_scope_minimum_prepared(slots, right, generation);
        Self::remember_index(slots, successor, generation);
        slots[successor].scope_left = left;
        slots[successor].scope_right = right;
        (
            Self::rebalance_scope_prepared(slots, successor, generation),
            root,
        )
    }

    fn refresh_index_page(slots: &mut [PrivatePagePoolSlot], root: usize, pgno: u32) {
        if root == NO_SLOT {
            return;
        }
        if pgno < slots[root].pgno {
            Self::refresh_index_page(slots, slots[root].index_left, pgno);
        } else if pgno > slots[root].pgno {
            Self::refresh_index_page(slots, slots[root].index_right, pgno);
        }
        Self::refresh_index_node(slots, root);
    }

    fn refresh_scope_page(slots: &mut [PrivatePagePoolSlot], root: usize, pgno: u32) {
        if root == NO_SLOT {
            return;
        }
        if pgno < slots[root].pgno {
            Self::refresh_scope_page(slots, slots[root].scope_left, pgno);
        } else if pgno > slots[root].pgno {
            Self::refresh_scope_page(slots, slots[root].scope_right, pgno);
        }
        Self::refresh_scope_node(slots, root);
    }

    fn refresh_index_page_checkpointed(
        &self,
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        pgno: u32,
        generation: u64,
    ) {
        if root == NO_SLOT {
            return;
        }
        self.remember_index_in_journal(slots, root, generation);
        if pgno < slots[root].pgno {
            self.refresh_index_page_checkpointed(slots, slots[root].index_left, pgno, generation);
        } else if pgno > slots[root].pgno {
            self.refresh_index_page_checkpointed(slots, slots[root].index_right, pgno, generation);
        }
        Self::refresh_index_node(slots, root);
    }

    fn refresh_scope_page_checkpointed(
        &self,
        slots: &mut [PrivatePagePoolSlot],
        root: usize,
        pgno: u32,
        generation: u64,
    ) {
        if root == NO_SLOT {
            return;
        }
        self.remember_index_in_journal(slots, root, generation);
        if pgno < slots[root].pgno {
            self.refresh_scope_page_checkpointed(slots, slots[root].scope_left, pgno, generation);
        } else if pgno > slots[root].pgno {
            self.refresh_scope_page_checkpointed(slots, slots[root].scope_right, pgno, generation);
        }
        Self::refresh_scope_node(slots, root);
    }

    fn refresh_slot_counts(&self, slots: &mut [PrivatePagePoolSlot], slot: usize) {
        let pgno = slots[slot].pgno;
        let anchor = slots[slot].scope_anchor_index;
        let checkpoint = self.active_checkpoint.get();
        if checkpoint == 0 {
            Self::refresh_index_page(slots, self.index_root.get(), pgno);
            if anchor != NO_SLOT {
                Self::refresh_scope_page(slots, slots[anchor].scope_root, pgno);
            }
        } else {
            self.refresh_index_page_checkpointed(slots, self.index_root.get(), pgno, checkpoint);
            if anchor != NO_SLOT {
                self.refresh_scope_page_checkpointed(
                    slots,
                    slots[anchor].scope_root,
                    pgno,
                    checkpoint,
                );
            }
        }
        self.sync_aggregate_views(slots);
    }

    fn rebuild_index_counts(slots: &mut [PrivatePagePoolSlot], root: usize) -> usize {
        if root == NO_SLOT {
            return 0;
        }
        let left = slots[root].index_left;
        let right = slots[root].index_right;
        let visits =
            Self::rebuild_index_counts(slots, left) + Self::rebuild_index_counts(slots, right) + 1;
        Self::refresh_index_node(slots, root);
        visits
    }

    fn rebuild_scope_counts(slots: &mut [PrivatePagePoolSlot], root: usize) -> usize {
        if root == NO_SLOT {
            return 0;
        }
        let left = slots[root].scope_left;
        let right = slots[root].scope_right;
        let visits =
            Self::rebuild_scope_counts(slots, left) + Self::rebuild_scope_counts(slots, right) + 1;
        Self::refresh_scope_node(slots, root);
        visits
    }

    fn rebuild_all_index_counts(&self, slots: &mut [PrivatePagePoolSlot]) {
        #[cfg(test)]
        let mut visits = slots.len();
        #[cfg(test)]
        {
            visits += Self::rebuild_index_counts(slots, self.index_root.get());
        }
        #[cfg(not(test))]
        Self::rebuild_index_counts(slots, self.index_root.get());
        for index in 0..slots.len() {
            if slots[index].scope_anchor {
                #[cfg(test)]
                {
                    visits += Self::rebuild_scope_counts(slots, slots[index].scope_root);
                }
                #[cfg(not(test))]
                Self::rebuild_scope_counts(slots, slots[index].scope_root);
            }
        }
        self.sync_aggregate_views(slots);
        #[cfg(test)]
        self.terminal_rebuild_visits.set(visits);
    }

    fn sync_aggregate_views(&self, slots: &[PrivatePagePoolSlot]) {
        let root = self.index_root.get();
        let available = if root == NO_SLOT {
            0
        } else {
            slots[root].index_available
        };
        self.available_count.set(available);
        self.lowest_available
            .set(Self::lowest_unscoped_available_slot(slots, root).unwrap_or(self.slot_count));
    }

    fn lowest_unscoped_available_slot(
        slots: &[PrivatePagePoolSlot],
        mut root: usize,
    ) -> Option<usize> {
        while root != NO_SLOT {
            let left = slots[root].index_left;
            if left != NO_SLOT && slots[left].index_unscoped_available != 0 {
                root = left;
                continue;
            }
            if slots[root].scope_id == 0 && slots[root].state == PrivatePageState::Available {
                return Some(root);
            }
            root = slots[root].index_right;
        }
        None
    }

    fn find_index(slots: &[PrivatePagePoolSlot], mut root: usize, pgno: u32) -> Option<usize> {
        while root != NO_SLOT {
            if pgno < slots[root].pgno {
                root = slots[root].index_left;
            } else if pgno > slots[root].pgno {
                root = slots[root].index_right;
            } else {
                return Some(root);
            }
        }
        None
    }

    fn find_scope_index(
        &self,
        slots: &[PrivatePagePoolSlot],
        scope: &PrivatePageReservationScope<'_>,
        mut root: usize,
        pgno: u32,
    ) -> Result<Option<usize>, PrivatePagePoolError> {
        let capacity = slots
            .get(scope.anchor)
            .ok_or(PrivatePagePoolError::StaleScope)?
            .scope_capacity;
        let mut probes = 0usize;
        while root != NO_SLOT {
            if probes == capacity {
                return Err(PrivatePagePoolError::StaleScope);
            }
            probes += 1;
            let page = slots.get(root).ok_or(PrivatePagePoolError::StaleScope)?;
            #[cfg(test)]
            self.scope_lookup_probes
                .set(self.scope_lookup_probes.get().saturating_add(1));
            if page.authorization.is_none()
                || page.scope_id != scope.id
                || page.scope_anchor_index != scope.anchor
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            if pgno < page.pgno {
                root = page.scope_left;
            } else if pgno > page.pgno {
                root = page.scope_right;
            } else {
                return Ok(Some(root));
            }
        }
        Ok(None)
    }

    pub(crate) const fn committed_page_count(&self) -> u64 {
        self.committed_page_count
    }

    pub(crate) fn pending_page_count(&self) -> u64 {
        self.pending_page_count.get()
    }

    pub(crate) const fn pending_txn(&self) -> u64 {
        self.pending_txn
    }

    pub(crate) fn validate_checkpoint_handle(
        &self,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        self.validate_checkpoint(checkpoint)
    }

    pub(crate) fn has_active_scopes(&self) -> bool {
        self.active_scopes.get() != 0
    }

    pub(crate) fn has_active_checkpoint(&self) -> bool {
        self.active_checkpoint.get() != 0
    }

    pub(crate) fn has_active_operation(&self) -> bool {
        self.active_operation_id.get() != 0
    }

    pub(crate) fn requires_abort(&self) -> bool {
        self.abort_required.get()
    }

    pub(crate) fn register_coordinator_session(
        &self,
        session_identity: u64,
    ) -> Result<u64, PrivatePagePoolError> {
        if session_identity == 0
            || self.coordinator_session_identity.get() != 0
            || self.coordinator_session_generation.get() != 0
            || self.coordinator_work_identity.get() != 0
            || self.coordinator_work_generation.get() != 0
            || self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::None
            || self.sealed_coordinator_cleanup_scope_id.get() != 0
            || self.sealed_coordinator_cleanup_nonce.get() != 0
            || self.epoch.get() != 1
            || self.generation.get() != 1
            || self.operation_sequence.get() != 0
            || self.scope_sequence.get() != 0
            || self.active_checkpoint.get() != 0
            || self.active_operation_id.get() != 0
            || self.active_scopes.get() != 0
            || self.abort_required.get()
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        let next_epoch = self.next_epoch()?;
        self.coordinator_session_identity.set(session_identity);
        self.coordinator_session_generation.set(1);
        self.epoch.set(next_epoch);
        Ok(1)
    }

    pub(crate) fn coordinator_fence(&self) -> PrivatePageCoordinatorFence {
        PrivatePageCoordinatorFence {
            pool_identity: self.identity,
            epoch: self.epoch.get(),
            pending_page_count: self.pending_page_count.get(),
            session_identity: self.coordinator_session_identity.get(),
            session_generation: self.coordinator_session_generation.get(),
            work_identity: self.coordinator_work_identity.get(),
            work_generation: self.coordinator_work_generation.get(),
            work_phase: self.coordinator_work_phase.get(),
            active_checkpoint: self.active_checkpoint.get(),
            active_operation: self.active_operation_id.get(),
            active_scopes: self.active_scopes.get(),
            unaccepted_scopes: self.coordinator_unaccepted_scopes.get(),
            abort_required: self.abort_required.get(),
        }
    }

    pub(crate) fn isolate_callback_backing(
        &self,
    ) -> Result<PrivatePageCallbackIsolation<'_, 'slots>, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        Ok(PrivatePageCallbackIsolation { _slots: slots })
    }

    pub(crate) fn validate_coordinator_session(
        &self,
        session_identity: u64,
        session_generation: u64,
    ) -> Result<(), PrivatePagePoolError> {
        if session_identity == 0
            || session_identity != self.coordinator_session_identity.get()
            || session_generation == 0
            || session_generation != self.coordinator_session_generation.get()
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        Ok(())
    }

    #[allow(clippy::too_many_arguments)]
    fn coordinator_scope_seal(
        &self,
        address: usize,
        session_identity: u64,
        session_generation: u64,
        work_identity: u64,
        count: usize,
        pool_epoch: u64,
        vacant_head: usize,
        vacant_count: usize,
    ) -> u64 {
        let mut seal = 1_469_598_103_934_665_603u64;
        for value in [
            address as u64,
            self.identity as u64,
            session_identity,
            session_generation,
            work_identity,
            count as u64,
            pool_epoch,
            vacant_head as u64,
            vacant_count as u64,
        ] {
            seal ^= value;
            seal = seal.wrapping_mul(1_099_511_628_211);
        }
        seal
    }

    pub(crate) fn prepare_coordinator_scope<'slot>(
        &self,
        session_identity: u64,
        session_generation: u64,
        work_identity: u64,
        count: usize,
        slot: &'slot mut PrivatePagePreparedScopeSlot,
    ) -> Result<PrivatePagePreparedScopeReservation<'slot>, PrivatePagePoolError> {
        if *slot != PrivatePagePreparedScopeSlot::empty() {
            return Err(PrivatePagePoolError::InvalidState(NO_SLOT));
        }
        if session_identity == 0
            || session_identity != self.coordinator_session_identity.get()
            || session_generation != self.coordinator_session_generation.get()
            || work_identity == 0
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        if self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::None {
            return Err(PrivatePagePoolError::CoordinatorWorkActive);
        }
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        self.preflight_scope_reservation(count)?;
        let address = slot as *const PrivatePagePreparedScopeSlot as usize;
        let pool_epoch = self.epoch.get();
        let vacant_head = self.unscoped_vacant_head.get();
        let vacant_count = self.unscoped_vacant_count.get();
        let seal = self.coordinator_scope_seal(
            address,
            session_identity,
            session_generation,
            work_identity,
            count,
            pool_epoch,
            vacant_head,
            vacant_count,
        );
        *slot = PrivatePagePreparedScopeSlot {
            address,
            pool_identity: self.identity,
            session_identity,
            session_generation,
            work_identity,
            count,
            pool_epoch,
            vacant_head,
            vacant_count,
            seal,
        };
        Ok(PrivatePagePreparedScopeReservation { slot })
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn activate_prepared_coordinator_scope<'slot>(
        &self,
        prepared: PrivatePagePreparedScopeReservation<'slot>,
    ) -> Result<
        (
            PrivatePageCoordinatorWork,
            PrivatePageReservationScope<'slots>,
        ),
        (
            PrivatePagePreparedScopeReservation<'slot>,
            PrivatePagePoolError,
        ),
    > {
        let slot = &*prepared.slot;
        let address = slot as *const PrivatePagePreparedScopeSlot as usize;
        let expected_seal = self.coordinator_scope_seal(
            address,
            slot.session_identity,
            slot.session_generation,
            slot.work_identity,
            slot.count,
            slot.pool_epoch,
            slot.vacant_head,
            slot.vacant_count,
        );
        if slot.address != address
            || slot.pool_identity != self.identity
            || slot.seal == 0
            || slot.seal != expected_seal
            || slot.session_identity != self.coordinator_session_identity.get()
            || slot.session_generation != self.coordinator_session_generation.get()
            || slot.pool_epoch != self.epoch.get()
            || slot.vacant_head != self.unscoped_vacant_head.get()
            || slot.vacant_count != self.unscoped_vacant_count.get()
        {
            return Err((prepared, PrivatePagePoolError::CoordinatorMismatch));
        }
        if self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::None {
            return Err((prepared, PrivatePagePoolError::CoordinatorWorkActive));
        }
        let session_identity = slot.session_identity;
        let session_generation = slot.session_generation;
        let work_identity = slot.work_identity;
        let count = slot.count;
        let Some(generation) = self.coordinator_work_generation.get().checked_add(1) else {
            return Err((prepared, PrivatePagePoolError::GenerationExhausted));
        };
        self.coordinator_work_identity.set(work_identity);
        self.coordinator_work_generation.set(generation);
        self.coordinator_work_phase
            .set(PrivatePageCoordinatorWorkPhase::Active);
        self.coordinator_work_start_epoch.set(self.epoch.get());
        self.coordinator_mutation_started.set(false);
        let scope = match self.reserve_scope_inner(count) {
            Ok(scope) => scope,
            Err(error) => {
                self.abort_required.set(true);
                return Err((prepared, error));
            }
        };
        self.coordinator_mutation_started.set(true);
        self.coordinator_unaccepted_scopes.set(1);
        self.coordinator_scope_id.set(scope.id);
        let work = PrivatePageCoordinatorWork {
            pool_identity: self.identity,
            session_identity,
            session_generation,
            work_identity,
            work_generation: generation,
        };
        prepared.slot.clear();
        Ok((work, scope))
    }

    pub(crate) fn cancel_prepared_coordinator_scope(
        &self,
        prepared: PrivatePagePreparedScopeReservation<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        let address = &*prepared.slot as *const PrivatePagePreparedScopeSlot as usize;
        if prepared.slot.address != address || prepared.slot.pool_identity != self.identity {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        prepared.slot.clear();
        Ok(())
    }

    /// Validates the reservation that a sparse replay is about to consume.
    ///
    /// Sparse replay keeps the live pool unchanged until its final infallible
    /// suffix. The prepared-slot proof must therefore still describe that same
    /// untouched pool immediately before that suffix starts.
    pub(crate) fn preflight_prepared_coordinator_scope(
        &self,
        prepared: &PrivatePagePreparedScopeReservation<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        let slot = &*prepared.slot;
        let address = slot as *const PrivatePagePreparedScopeSlot as usize;
        let expected_seal = self.coordinator_scope_seal(
            address,
            slot.session_identity,
            slot.session_generation,
            slot.work_identity,
            slot.count,
            slot.pool_epoch,
            slot.vacant_head,
            slot.vacant_count,
        );
        if slot.address != address
            || slot.pool_identity != self.identity
            || slot.seal == 0
            || slot.seal != expected_seal
            || slot.session_identity != self.coordinator_session_identity.get()
            || slot.session_generation != self.coordinator_session_generation.get()
            || slot.pool_epoch != self.epoch.get()
            || slot.vacant_head != self.unscoped_vacant_head.get()
            || slot.vacant_count != self.unscoped_vacant_count.get()
            || self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::None
            || self.active_checkpoint.get() != 0
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        Ok(())
    }

    fn coordinator_terminal_pages_fingerprint(pages: &[PrivatePageCoordinatorTerminalPage]) -> u64 {
        let mut hash = 1_469_598_103_934_665_603u64;
        for page in pages {
            for value in [
                page.pool_slot as u64,
                u64::from(page.pgno),
                page.authorization as u64,
                page.owner as u64,
                page.owner_generation,
                page.tag,
            ] {
                hash ^= value;
                hash = hash.wrapping_mul(1_099_511_628_211);
            }
            for &byte in &page.bytes {
                hash ^= u64::from(byte);
                hash = hash.wrapping_mul(1_099_511_628_211);
            }
        }
        hash
    }

    fn coordinator_prior_returns_fingerprint(returns: &[PrivatePageCoordinatorPriorReturn]) -> u64 {
        let mut hash = 1_469_598_103_934_665_603u64;
        for planned in returns.iter() {
            let page = planned.page;
            for value in [
                page.scope_id,
                page.scope_anchor as u64,
                page.scope_generation,
                page.slot as u64,
                u64::from(page.pgno),
                page.binding_epoch,
                page.owner as u64,
                page.owner_generation,
                page.tag,
                planned.nonce,
            ] {
                hash ^= value;
                hash = hash.wrapping_mul(1_099_511_628_211);
            }
        }
        hash
    }

    pub(crate) fn prepare_coordinator_terminal<'plan>(
        &self,
        prepared_scope: &PrivatePagePreparedScopeReservation<'_>,
        pages: &'plan mut [PrivatePageCoordinatorTerminalPage],
        nonce: u64,
    ) -> Result<PrivatePagePreparedCoordinatorTerminal<'plan>, PrivatePagePoolError> {
        let (address, final_epoch, pending) =
            self.preflight_coordinator_terminal(prepared_scope, pages, nonce, true)?;
        let scope_slot = &*prepared_scope.slot;
        Ok(PrivatePagePreparedCoordinatorTerminal {
            pool_identity: self.identity,
            pool_identity_epoch: self.identity_epoch,
            session_identity: scope_slot.session_identity,
            session_generation: scope_slot.session_generation,
            work_identity: scope_slot.work_identity,
            prepared_scope_address: address,
            prepared_scope_seal: scope_slot.seal,
            start_epoch: scope_slot.pool_epoch,
            final_epoch,
            final_pending_page_count: pending,
            nonce,
            pages_address: pages.as_ptr() as usize,
            pages_len: pages.len(),
            pages_fingerprint: Self::coordinator_terminal_pages_fingerprint(pages),
            pages,
        })
    }

    fn preflight_coordinator_terminal(
        &self,
        prepared_scope: &PrivatePagePreparedScopeReservation<'_>,
        pages: &[PrivatePageCoordinatorTerminalPage],
        nonce: u64,
        slots_bound: bool,
    ) -> Result<(usize, u64, u64), PrivatePagePoolError> {
        let scope_slot = &*prepared_scope.slot;
        let address = scope_slot as *const PrivatePagePreparedScopeSlot as usize;
        let expected_scope_seal = self.coordinator_scope_seal(
            address,
            scope_slot.session_identity,
            scope_slot.session_generation,
            scope_slot.work_identity,
            scope_slot.count,
            scope_slot.pool_epoch,
            scope_slot.vacant_head,
            scope_slot.vacant_count,
        );
        if nonce == 0
            || pages.is_empty()
            || pages.len() > scope_slot.count
            || scope_slot.address != address
            || scope_slot.pool_identity != self.identity
            || scope_slot.seal == 0
            || scope_slot.seal != expected_scope_seal
            || scope_slot.pool_epoch != self.epoch.get()
            || scope_slot.vacant_head != self.unscoped_vacant_head.get()
            || scope_slot.vacant_count != self.unscoped_vacant_count.get()
            || self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::None
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_unscoped_vacancy_boundary(&slots)?;
        let mut vacant = scope_slot.vacant_head;
        let mut previous = None;
        let mut pending = self.pending_page_count.get();
        for page in pages {
            if vacant == NO_SLOT
                || vacant >= slots.len()
                || (slots_bound && page.pool_slot != vacant)
                || (!slots_bound && page.pool_slot != NO_SLOT)
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            if let Some(previous) = previous {
                if page.pgno <= previous {
                    return Err(PrivatePagePoolError::PagesNotStrict {
                        previous,
                        current: page.pgno,
                    });
                }
            }
            if Self::find_index(&slots, self.index_root.get(), page.pgno).is_some() {
                return Err(PrivatePagePoolError::PagesNotStrict {
                    previous: page.pgno,
                    current: page.pgno,
                });
            }
            match page.authorization {
                PrivatePageAuthorization::CommittedFree
                | PrivatePageAuthorization::SafelyReclaimed => {
                    if page.pgno < 2 || u64::from(page.pgno) >= self.committed_page_count {
                        return Err(PrivatePagePoolError::AuthorizationMismatch {
                            pgno: page.pgno,
                            authorization: page.authorization,
                        });
                    }
                }
                PrivatePageAuthorization::Appended => {
                    if u64::from(page.pgno) != pending || pending == MAX_PAGE_COUNT {
                        return Err(PrivatePagePoolError::AuthorizationMismatch {
                            pgno: page.pgno,
                            authorization: page.authorization,
                        });
                    }
                    pending += 1;
                }
            }
            if page.owner_generation != self.pending_txn
                || !valid_terminal_owner_tag(page.owner, page.tag)
            {
                return Err(PrivatePagePoolError::InvalidState(vacant));
            }
            let header = PageHeader::decode(&page.bytes, self.pending_txn)
                .map_err(|_| PrivatePagePoolError::InvalidState(vacant))?;
            let expected_type = terminal_page_matches_owner(page.owner, page.tag, header);
            if !expected_type
                || header.born_txn != page.owner_generation
                || !page::verify_crc32c(&page.bytes)
            {
                return Err(PrivatePagePoolError::InvalidState(vacant));
            }
            slots[vacant]
                .binding_epoch
                .checked_add(3)
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
            vacant = slots[vacant].unscoped_vacant_next;
            previous = Some(page.pgno);
        }
        // The prepared scope is a bounded capacity reservation.  Activating it
        // moves every reserved slot, while terminal binding consumes only the
        // actual nonempty prefix selected under the live lock.
        let reserve_steps =
            u64::try_from(scope_slot.count).map_err(|_| PrivatePagePoolError::EpochExhausted)?;
        let bind_steps =
            u64::try_from(pages.len()).map_err(|_| PrivatePagePoolError::EpochExhausted)?;
        let apply_steps = bind_steps
            .checked_mul(3)
            .and_then(|steps| steps.checked_add(1))
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        let final_epoch = self.preflight_epoch_steps(
            reserve_steps
                .checked_add(apply_steps)
                .ok_or(PrivatePagePoolError::EpochExhausted)?,
        )?;
        self.authorized_len
            .get()
            .checked_add(pages.len())
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        Ok((address, final_epoch, pending))
    }

    pub(crate) fn prepare_unbound_coordinator_terminal<'plan>(
        &self,
        prepared_scope: &PrivatePagePreparedScopeReservation<'_>,
        pages: &'plan mut [PrivatePageCoordinatorTerminalPage],
        nonce: u64,
    ) -> Result<
        PrivatePagePreparedCoordinatorTerminal<'plan>,
        (
            &'plan mut [PrivatePageCoordinatorTerminalPage],
            PrivatePagePoolError,
        ),
    > {
        if pages.iter().any(|page| page.pool_slot != NO_SLOT) {
            return Err((pages, PrivatePagePoolError::CoordinatorMismatch));
        }
        let (address, final_epoch, pending) =
            match self.preflight_coordinator_terminal(prepared_scope, pages, nonce, false) {
                Ok(preflight) => preflight,
                Err(error) => return Err((pages, error)),
            };
        let scope_slot = &*prepared_scope.slot;
        {
            let slots = self
                .slots
                .try_borrow()
                .expect("terminal preflight proved pool borrowability");
            let mut vacant = scope_slot.vacant_head;
            for page in pages.iter_mut() {
                page.pool_slot = vacant;
                vacant = slots[vacant].unscoped_vacant_next;
            }
        }
        Ok(PrivatePagePreparedCoordinatorTerminal {
            pool_identity: self.identity,
            pool_identity_epoch: self.identity_epoch,
            session_identity: scope_slot.session_identity,
            session_generation: scope_slot.session_generation,
            work_identity: scope_slot.work_identity,
            prepared_scope_address: address,
            prepared_scope_seal: scope_slot.seal,
            start_epoch: scope_slot.pool_epoch,
            final_epoch,
            final_pending_page_count: pending,
            nonce,
            pages_address: pages.as_ptr() as usize,
            pages_len: pages.len(),
            pages_fingerprint: Self::coordinator_terminal_pages_fingerprint(pages),
            pages,
        })
    }

    pub(crate) fn preflight_coordinator_prior_returns(
        &self,
        prepared_scope: &PrivatePagePreparedScopeReservation<'_>,
        terminal: &PrivatePagePreparedCoordinatorTerminal<'_>,
        returns: &[PrivatePageCoordinatorPriorReturn],
    ) -> Result<PrivatePageCoordinatorPriorReturnsFence, PrivatePagePoolError> {
        let scope_slot = &*prepared_scope.slot;
        if terminal.pool_identity != self.identity
            || terminal.pool_identity_epoch != self.identity_epoch
            || terminal.session_identity != scope_slot.session_identity
            || terminal.session_generation != scope_slot.session_generation
            || terminal.work_identity != scope_slot.work_identity
            || terminal.prepared_scope_address
                != scope_slot as *const PrivatePagePreparedScopeSlot as usize
            || terminal.prepared_scope_seal != scope_slot.seal
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let mut previous_slot = None;
        for planned in returns.iter() {
            let page = planned.page;
            if planned.nonce == 0
                || page.slot >= slots.len()
                || previous_slot.is_some_and(|previous| page.slot <= previous)
            {
                return Err(PrivatePagePoolError::StaleAuthority);
            }
            let slot = &slots[page.slot];
            if slot.scope_id != page.scope_id
                || slot.scope_anchor_index != page.scope_anchor
                || slots[page.scope_anchor].scope_generation != page.scope_generation
                || !slots[page.scope_anchor].scope_sealed
                || slots[page.scope_anchor].scope_successor != planned.nonce
                || !slots[page.scope_anchor].successor_consumed
                || slot.pgno != page.pgno
                || slot.binding_epoch != page.binding_epoch
                || slot.authorization.is_none()
                || !matches!(
                    slot.state,
                    PrivatePageState::InUse {
                        owner,
                        owner_generation,
                        tag,
                        ..
                    } if owner == page.owner
                        && owner_generation == page.owner_generation
                        && tag == page.tag
                )
            {
                return Err(PrivatePagePoolError::StaleAuthority);
            }
            slot.binding_epoch
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
            previous_slot = Some(page.slot);
        }
        let steps =
            u64::try_from(returns.len()).map_err(|_| PrivatePagePoolError::EpochExhausted)?;
        let final_epoch = terminal
            .final_epoch
            .checked_add(steps)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        self.preflight_epoch_steps(
            final_epoch
                .checked_sub(self.epoch.get())
                .ok_or(PrivatePagePoolError::EpochExhausted)?,
        )?;
        Ok(PrivatePageCoordinatorPriorReturnsFence {
            pool_identity: self.identity,
            pool_identity_epoch: self.identity_epoch,
            session_identity: scope_slot.session_identity,
            session_generation: scope_slot.session_generation,
            work_identity: scope_slot.work_identity,
            start_epoch: terminal.final_epoch,
            final_epoch,
            returns_len: returns.len(),
            returns_fingerprint: Self::coordinator_prior_returns_fingerprint(returns),
        })
    }

    pub(crate) fn seal_coordinator_prior_returns_preflighted<'plan>(
        &self,
        fence: PrivatePageCoordinatorPriorReturnsFence,
        returns: &'plan mut [PrivatePageCoordinatorPriorReturn],
    ) -> PrivatePagePreparedCoordinatorPriorReturns<'plan> {
        debug_assert_eq!(fence.pool_identity, self.identity);
        debug_assert_eq!(fence.returns_len, returns.len());
        debug_assert_eq!(
            fence.returns_fingerprint,
            Self::coordinator_prior_returns_fingerprint(returns)
        );
        PrivatePagePreparedCoordinatorPriorReturns {
            pool_identity: fence.pool_identity,
            pool_identity_epoch: fence.pool_identity_epoch,
            session_identity: fence.session_identity,
            session_generation: fence.session_generation,
            work_identity: fence.work_identity,
            start_epoch: fence.start_epoch,
            final_epoch: fence.final_epoch,
            returns_address: returns.as_ptr() as usize,
            returns_len: fence.returns_len,
            returns_fingerprint: fence.returns_fingerprint,
            returns,
        }
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn prepare_sparse_coordinator_replay<
        'pool,
        'scope_slot,
        'pages,
        'returns,
        'scratch,
    >(
        &'pool self,
        prepared_scope: &PrivatePagePreparedScopeReservation<'scope_slot>,
        terminal: &PrivatePagePreparedCoordinatorTerminal<'pages>,
        prior: &PrivatePagePreparedCoordinatorPriorReturns<'returns>,
        replay_slots: &'scratch mut [PrivatePageSparseReplaySlot],
        replay_index: &'scratch mut [PrivatePageSparseReplayIndex],
    ) -> Result<PrivatePagePreparedSparseReplay<'pool, 'slots, 'scratch>, PrivatePagePoolError>
    {
        let scope_slot = &*prepared_scope.slot;
        let scope_address = scope_slot as *const PrivatePagePreparedScopeSlot as usize;
        if scope_slot.address != scope_address
            || scope_slot.pool_identity != self.identity
            || scope_slot.seal
                != self.coordinator_scope_seal(
                    scope_address,
                    scope_slot.session_identity,
                    scope_slot.session_generation,
                    scope_slot.work_identity,
                    scope_slot.count,
                    scope_slot.pool_epoch,
                    scope_slot.vacant_head,
                    scope_slot.vacant_count,
                )
            || terminal.pool_identity != self.identity
            || terminal.pool_identity_epoch != self.identity_epoch
            || terminal.prepared_scope_address != scope_address
            || terminal.prepared_scope_seal != scope_slot.seal
            || terminal.pages_address != terminal.pages().as_ptr() as usize
            || terminal.pages_len != terminal.pages().len()
            || terminal.pages_fingerprint
                != Self::coordinator_terminal_pages_fingerprint(terminal.pages())
            || prior.pool_identity != self.identity
            || prior.pool_identity_epoch != self.identity_epoch
            || prior.session_identity != scope_slot.session_identity
            || prior.session_generation != scope_slot.session_generation
            || prior.work_identity != scope_slot.work_identity
            || prior.returns_address != prior.returns.as_ptr() as usize
            || prior.returns_len != prior.returns.len()
            || prior.returns_fingerprint
                != Self::coordinator_prior_returns_fingerprint(prior.returns)
            || self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::None
            || self.epoch.get() != scope_slot.pool_epoch
            || self.unscoped_vacant_head.get() != scope_slot.vacant_head
            || self.unscoped_vacant_count.get() != scope_slot.vacant_count
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        let work_generation = self
            .coordinator_work_generation
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::GenerationExhausted)?;
        let scope_id = self
            .scope_sequence
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::ScopeIdentityExhausted)?;
        let active_scopes = self
            .active_scopes
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::ScopeIdentityExhausted)?;
        let mut live = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_unscoped_vacancy_boundary(&live)?;
        let anchor = self.validate_unscoped_vacancy_prefix(&live, scope_slot.count)?;
        let mut overlay =
            PrivatePageSparseOverlay::new(&mut live, replay_slots, replay_index, work_generation)?;
        let mut epoch = self.epoch.get();
        let mut unscoped_head = self.unscoped_vacant_head.get();
        let mut unscoped_tail = self.unscoped_vacant_tail.get();
        let mut unscoped_count = self.unscoped_vacant_count.get();
        let mut head = NO_SLOT;
        let mut previous = NO_SLOT;
        let mut scope_vacant_count = 0usize;
        let mut scope_vacant_revision = 0u64;
        let mut scope_vacant_digest = 0u64;
        for ordinal in 0..scope_slot.count {
            let index = unscoped_head;
            if index == NO_SLOT {
                return Err(PrivatePagePoolError::StaleScope);
            }
            let next = overlay.get(index).unscoped_vacant_next;
            unscoped_head = next;
            if next == NO_SLOT {
                unscoped_tail = NO_SLOT;
            } else {
                overlay.get_mut(next)?.unscoped_vacant_prev = NO_SLOT;
            }
            unscoped_count -= 1;
            if head == NO_SLOT {
                head = index;
            } else {
                overlay.get_mut(previous)?.scope_vacant_next = index;
                overlay.get_mut(previous)?.scope_member_next = index;
            }
            let slot = overlay.get_mut(index)?;
            slot.unscoped_vacant_prev = NO_SLOT;
            slot.unscoped_vacant_next = NO_SLOT;
            slot.scope_id = scope_id;
            slot.scope_anchor_index = anchor;
            slot.scope_member_next = NO_SLOT;
            slot.scope_member_head = NO_SLOT;
            slot.scope_member_ordinal = ordinal;
            slot.scope_vacant_next = NO_SLOT;
            slot.binding_epoch += 1;
            scope_vacant_count += 1;
            scope_vacant_revision =
                scope_vacant_revision.wrapping_add(private_page_scope_payload_revision(slot));
            scope_vacant_digest ^= private_page_scope_payload_digest(index, slot);
            epoch += 1;
            previous = index;
        }
        {
            let anchor_slot = overlay.get_mut(anchor)?;
            anchor_slot.scope_anchor = true;
            anchor_slot.scope_member_head = head;
            anchor_slot.scope_root = NO_SLOT;
            anchor_slot.scope_vacant_head = head;
            anchor_slot.scope_capacity = scope_slot.count;
            anchor_slot.scope_bound = 0;
            anchor_slot.scope_generation = 1;
            anchor_slot.scope_sealed = false;
            anchor_slot.scope_successor = 0;
            anchor_slot.successor_consumed = false;
            anchor_slot.scope_vacant_count = scope_vacant_count;
            anchor_slot.scope_vacant_revision = scope_vacant_revision;
            anchor_slot.scope_vacant_digest = scope_vacant_digest;
        }
        let mut index_root = self.index_root.get();
        let mut pending_page_count = self.pending_page_count.get();
        let mut authorized_len = self.authorized_len.get();
        let mut vacant = head;
        for page in terminal.pages() {
            if vacant != page.pool_slot {
                return Err(PrivatePagePoolError::StaleScope);
            }
            let next_vacant = overlay.get(vacant).scope_vacant_next;
            let removed_vacant_revision = private_page_scope_payload_revision(overlay.get(vacant));
            let removed_vacant_digest =
                private_page_scope_payload_digest(vacant, overlay.get(vacant));
            {
                let anchor_slot = overlay.get_mut(anchor)?;
                anchor_slot.scope_vacant_head = next_vacant;
                anchor_slot.scope_bound += 1;
                anchor_slot.scope_vacant_count -= 1;
                anchor_slot.scope_vacant_revision = anchor_slot
                    .scope_vacant_revision
                    .wrapping_sub(removed_vacant_revision);
                anchor_slot.scope_vacant_digest ^= removed_vacant_digest;
            }
            let slot = overlay.get_mut(vacant)?;
            slot.pgno = page.pgno;
            slot.authorization = Some(page.authorization);
            slot.state = PrivatePageState::InUse {
                owner: page.owner,
                owner_generation: page.owner_generation,
                tag: page.tag,
                authority_epoch: epoch + 2,
            };
            slot.allocation_generation = self.generation.get();
            slot.adapter_owner = None;
            slot.adapter_tag = 0;
            slot.bytes = page.bytes;
            slot.scope_vacant_next = NO_SLOT;
            slot.index_left = NO_SLOT;
            slot.index_right = NO_SLOT;
            slot.index_height = 1;
            slot.index_available = 0;
            slot.index_in_use = 1;
            slot.index_unscoped_available = 0;
            slot.scope_left = NO_SLOT;
            slot.scope_right = NO_SLOT;
            slot.scope_height = 1;
            slot.scope_available = 0;
            slot.scope_in_use = 1;
            slot.binding_epoch += 2;
            slot.scope_count = 1;
            slot.scope_revision = private_page_scope_payload_revision(slot);
            slot.scope_digest = private_page_scope_payload_digest(vacant, slot);
            index_root = overlay.insert_index(index_root, vacant)?;
            let scope_root = overlay.get(anchor).scope_root;
            let scope_root = overlay.insert_scope(scope_root, vacant)?;
            overlay.get_mut(anchor)?.scope_root = scope_root;
            if page.authorization == PrivatePageAuthorization::Appended {
                pending_page_count += 1;
            }
            authorized_len += 1;
            epoch += 3;
            vacant = next_vacant;
        }
        {
            let anchor_slot = overlay.get_mut(anchor)?;
            anchor_slot.scope_generation += 1;
            anchor_slot.scope_sealed = true;
            anchor_slot.scope_successor = terminal.nonce;
            anchor_slot.successor_consumed = true;
        }
        epoch += 1;
        if epoch != terminal.final_epoch || pending_page_count != terminal.final_pending_page_count
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        for planned in prior.returns.iter() {
            let page = planned.page;
            let (root, removed) = overlay.delete_index(index_root, page.pgno)?;
            if removed != page.slot {
                return Err(PrivatePagePoolError::StaleAuthority);
            }
            index_root = root;
            let scope_root = overlay.get(page.scope_anchor).scope_root;
            let (scope_root, removed) = overlay.delete_scope(scope_root, page.pgno)?;
            if removed != page.slot {
                return Err(PrivatePagePoolError::StaleAuthority);
            }
            {
                let owning = overlay.get_mut(page.scope_anchor)?;
                owning.scope_root = scope_root;
                owning.scope_bound -= 1;
            }
            let vacant_head = overlay.get(page.scope_anchor).scope_vacant_head;
            let slot = overlay.get_mut(page.slot)?;
            slot.pgno = 0;
            slot.authorization = None;
            slot.state = PrivatePageState::Vacant;
            slot.allocation_generation = 0;
            slot.adapter_owner = None;
            slot.adapter_tag = 0;
            slot.bytes.fill(0);
            slot.scope_vacant_next = vacant_head;
            slot.index_left = NO_SLOT;
            slot.index_right = NO_SLOT;
            slot.index_height = 0;
            slot.index_available = 0;
            slot.index_in_use = 0;
            slot.index_unscoped_available = 0;
            slot.scope_left = NO_SLOT;
            slot.scope_right = NO_SLOT;
            slot.scope_height = 0;
            slot.scope_available = 0;
            slot.scope_in_use = 0;
            slot.scope_count = 0;
            slot.scope_revision = 0;
            slot.scope_digest = 0;
            slot.binding_epoch += 1;
            let returned_revision = private_page_scope_payload_revision(slot);
            let returned_digest = private_page_scope_payload_digest(page.slot, slot);
            let owning = overlay.get_mut(page.scope_anchor)?;
            owning.scope_vacant_head = page.slot;
            owning.scope_vacant_count += 1;
            owning.scope_vacant_revision =
                owning.scope_vacant_revision.wrapping_add(returned_revision);
            owning.scope_vacant_digest ^= returned_digest;
            authorized_len -= 1;
            epoch += 1;
        }
        if epoch != prior.final_epoch {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        let available_count = if index_root == NO_SLOT {
            0
        } else {
            overlay.get(index_root).index_available
        };
        let mut lowest = index_root;
        let lowest_available = loop {
            if lowest == NO_SLOT {
                break self.slot_count;
            }
            let node = overlay.get(lowest);
            if node.index_left != NO_SLOT
                && overlay.get(node.index_left).index_unscoped_available != 0
            {
                lowest = node.index_left;
            } else if node.scope_id == 0 && node.state == PrivatePageState::Available {
                break lowest;
            } else {
                lowest = node.index_right;
            }
        };
        let (len, index_visits) = overlay.finish();
        let work = PrivatePageCoordinatorWorkSeed {
            pool_identity: self.identity,
            session_identity: scope_slot.session_identity,
            session_generation: scope_slot.session_generation,
            work_identity: scope_slot.work_identity,
            work_generation,
        };
        let scope = PrivatePageReservationScopeSeed {
            pool_identity: self.identity,
            pool_epoch: self.identity_epoch,
            id: scope_id,
            pending_txn: self.pending_txn,
            anchor,
            generation: 2,
        };
        let state = PrivatePageSparseReplayState {
            authorized_len,
            available_count,
            lowest_available,
            pending_page_count,
            epoch,
            coordinator_work_identity: scope_slot.work_identity,
            coordinator_work_generation: work_generation,
            coordinator_work_phase: PrivatePageCoordinatorWorkPhase::Sealed,
            coordinator_work_start_epoch: self.epoch.get(),
            coordinator_mutation_started: true,
            coordinator_scope_id: scope_id,
            coordinator_unaccepted_scopes: 1,
            scope_sequence: scope_id,
            active_scopes,
            unscoped_vacant_count: unscoped_count,
            unscoped_vacant_head: unscoped_head,
            unscoped_vacant_tail: unscoped_tail,
            index_root,
        };
        Ok(PrivatePagePreparedSparseReplay {
            pool: self,
            live,
            slots: replay_slots,
            index: replay_index,
            len,
            index_visits,
            state,
            work,
            scope,
        })
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)]
    pub(crate) fn apply_coordinator_terminal_prepared<'plan>(
        &self,
        work: &PrivatePageCoordinatorWork,
        mut scope: PrivatePageReservationScope<'slots>,
        prepared: PrivatePagePreparedCoordinatorTerminal<'plan>,
    ) -> Result<
        (
            PrivatePageReservationScope<'slots>,
            &'plan [PrivatePageCoordinatorTerminalPage],
        ),
        (
            PrivatePageReservationScope<'slots>,
            PrivatePagePreparedCoordinatorTerminal<'plan>,
            PrivatePagePoolError,
        ),
    > {
        if self.validate_coordinator_work(work).is_err()
            || self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::Active
            || prepared.pool_identity != self.identity
            || prepared.pool_identity_epoch != self.identity_epoch
            || prepared.session_identity != work.session_identity
            || prepared.session_generation != work.session_generation
            || prepared.work_identity != work.work_identity
            || prepared.pages_address != prepared.pages().as_ptr() as usize
            || prepared.pages_len != prepared.pages().len()
            || prepared.pages_fingerprint
                != Self::coordinator_terminal_pages_fingerprint(prepared.pages())
            || self.coordinator_scope_id.get() != scope.id
            || self.coordinator_unaccepted_scopes.get() != 1
        {
            return Err((scope, prepared, PrivatePagePoolError::CoordinatorMismatch));
        }
        let mut slots = match self.slots.try_borrow_mut() {
            Ok(slots) => slots,
            Err(_) => {
                return Err((scope, prepared, PrivatePagePoolError::BorrowConflict));
            }
        };
        let anchor = match self.validate_scope(&slots, &scope) {
            Ok(anchor) => anchor,
            Err(error) => return Err((scope, prepared, error)),
        };
        let scope_steps = match u64::try_from(slots[anchor].scope_capacity) {
            Ok(steps) => steps,
            Err(_) => return Err((scope, prepared, PrivatePagePoolError::EpochExhausted)),
        };
        let expected_epoch = match prepared.start_epoch.checked_add(scope_steps) {
            Some(epoch) => epoch,
            None => return Err((scope, prepared, PrivatePagePoolError::EpochExhausted)),
        };
        if self.epoch.get() != expected_epoch
            || prepared.pages().is_empty()
            || slots[anchor].scope_bound != 0
            || slots[anchor].scope_capacity < prepared.pages().len()
            || slots[anchor].scope_vacant_head != prepared.pages()[0].pool_slot
        {
            return Err((scope, prepared, PrivatePagePoolError::StaleScope));
        }
        let mut vacant = slots[anchor].scope_vacant_head;
        for page in prepared.pages() {
            if vacant != page.pool_slot
                || slots[vacant].authorization.is_some()
                || slots[vacant].state != PrivatePageState::Vacant
            {
                return Err((scope, prepared, PrivatePagePoolError::StaleScope));
            }
            vacant = slots[vacant].scope_vacant_next;
        }
        let suffix_count = slots[anchor].scope_capacity - prepared.pages().len();
        let mut suffix_revision = 0u64;
        let mut suffix_digest = 0u64;
        for _ in 0..suffix_count {
            if vacant == NO_SLOT
                || vacant >= slots.len()
                || slots[vacant].authorization.is_some()
                || slots[vacant].state != PrivatePageState::Vacant
            {
                return Err((scope, prepared, PrivatePagePoolError::StaleScope));
            }
            suffix_revision =
                suffix_revision.wrapping_add(private_page_scope_payload_revision(&slots[vacant]));
            suffix_digest ^= private_page_scope_payload_digest(vacant, &slots[vacant]);
            vacant = slots[vacant].scope_vacant_next;
        }
        if vacant != NO_SLOT {
            return Err((scope, prepared, PrivatePagePoolError::StaleScope));
        }

        // All fallible checks end above. The exact reservation proof and page
        // journal make the suffix deterministic.
        for page in prepared.pages() {
            let index = page.pool_slot;
            let next_vacant = slots[index].scope_vacant_next;
            slots[anchor].scope_vacant_head = next_vacant;
            slots[anchor].scope_bound += 1;
            let slot = &mut slots[index];
            slot.pgno = page.pgno;
            slot.authorization = Some(page.authorization);
            slot.state = PrivatePageState::InUse {
                owner: page.owner,
                owner_generation: page.owner_generation,
                tag: page.tag,
                authority_epoch: self.epoch.get() + 2,
            };
            slot.allocation_generation = self.generation.get();
            slot.adapter_owner = None;
            slot.adapter_tag = 0;
            slot.bytes = page.bytes;
            slot.scope_vacant_next = NO_SLOT;
            slot.index_left = NO_SLOT;
            slot.index_right = NO_SLOT;
            slot.index_height = 1;
            slot.index_available = 0;
            slot.index_in_use = 1;
            slot.index_unscoped_available = 0;
            slot.scope_left = NO_SLOT;
            slot.scope_right = NO_SLOT;
            slot.scope_height = 1;
            slot.scope_available = 0;
            slot.scope_in_use = 1;
            slot.binding_epoch += 2;
            slot.scope_count = 1;
            slot.scope_revision = private_page_scope_payload_revision(slot);
            slot.scope_digest = private_page_scope_payload_digest(index, slot);
            let root = Self::index_insert_plain(&mut slots, self.index_root.get(), index);
            self.index_root.set(root);
            let old_scope_root = slots[anchor].scope_root;
            let scope_root = Self::scope_insert_prepared(&mut slots, old_scope_root, index, 0);
            slots[anchor].scope_root = scope_root;
            if page.authorization == PrivatePageAuthorization::Appended {
                self.pending_page_count
                    .set(self.pending_page_count.get() + 1);
            }
            self.authorized_len.set(self.authorized_len.get() + 1);
            self.epoch.set(self.epoch.get() + 3);
        }
        slots[anchor].scope_vacant_count = suffix_count;
        slots[anchor].scope_vacant_revision = suffix_revision;
        slots[anchor].scope_vacant_digest = suffix_digest;
        slots[anchor].scope_generation += 1;
        slots[anchor].scope_sealed = true;
        slots[anchor].scope_successor = prepared.nonce;
        slots[anchor].successor_consumed = true;
        scope.generation = slots[anchor].scope_generation;
        self.epoch.set(self.epoch.get() + 1);
        self.sync_aggregate_views(&slots);
        debug_assert_eq!(self.epoch.get(), prepared.final_epoch);
        debug_assert_eq!(
            self.pending_page_count.get(),
            prepared.final_pending_page_count
        );
        self.coordinator_work_phase
            .set(PrivatePageCoordinatorWorkPhase::Sealed);
        drop(slots);
        Ok((scope, prepared.into_pages()))
    }

    #[cfg(test)]
    pub(crate) fn apply_coordinator_prior_returns_prepared<'plan>(
        &self,
        work: &PrivatePageCoordinatorWork,
        prepared: PrivatePagePreparedCoordinatorPriorReturns<'plan>,
    ) -> Result<
        PrivatePagePreparedCoordinatorPriorReturns<'plan>,
        (
            PrivatePagePreparedCoordinatorPriorReturns<'plan>,
            PrivatePagePoolError,
        ),
    > {
        if self.validate_coordinator_work(work).is_err()
            || self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::Sealed
            || prepared.pool_identity != self.identity
            || prepared.pool_identity_epoch != self.identity_epoch
            || prepared.session_identity != work.session_identity
            || prepared.session_generation != work.session_generation
            || prepared.work_identity != work.work_identity
            || prepared.returns_address != prepared.returns.as_ptr() as usize
            || prepared.returns_len != prepared.returns.len()
            || prepared.returns_fingerprint
                != Self::coordinator_prior_returns_fingerprint(prepared.returns)
            || self.epoch.get() != prepared.start_epoch
        {
            return Err((prepared, PrivatePagePoolError::CoordinatorMismatch));
        }
        let mut slots = match self.slots.try_borrow_mut() {
            Ok(slots) => slots,
            Err(_) => return Err((prepared, PrivatePagePoolError::BorrowConflict)),
        };
        for planned in prepared.returns.iter() {
            let page = planned.page;
            let slot = &slots[page.slot];
            if slot.scope_id != page.scope_id
                || slot.scope_anchor_index != page.scope_anchor
                || slots[page.scope_anchor].scope_generation != page.scope_generation
                || !slots[page.scope_anchor].scope_sealed
                || slots[page.scope_anchor].scope_successor != planned.nonce
                || !slots[page.scope_anchor].successor_consumed
                || slot.pgno != page.pgno
                || slot.binding_epoch != page.binding_epoch
                || slot.authorization.is_none()
                || !matches!(
                    slot.state,
                    PrivatePageState::InUse {
                        owner,
                        owner_generation,
                        tag,
                        ..
                    } if owner == page.owner
                        && owner_generation == page.owner_generation
                        && tag == page.tag
                )
            {
                return Err((prepared, PrivatePagePoolError::StaleAuthority));
            }
        }

        // Every fallible check ends above. The sealed journal names exact slots
        // and exact owning scopes, so replay performs no lookup or discovery.
        for planned in prepared.returns.iter() {
            let page = planned.page;
            let (root, removed) =
                Self::index_delete_prepared(&mut slots, self.index_root.get(), page.pgno, 0);
            debug_assert_eq!(removed, page.slot);
            self.index_root.set(root);
            let old_scope_root = slots[page.scope_anchor].scope_root;
            let (scope_root, scope_removed) =
                Self::scope_delete_prepared(&mut slots, old_scope_root, page.pgno, 0);
            debug_assert_eq!(scope_removed, page.slot);
            slots[page.scope_anchor].scope_root = scope_root;
            slots[page.scope_anchor].scope_bound -= 1;
            let vacant_head = slots[page.scope_anchor].scope_vacant_head;
            let slot = &mut slots[page.slot];
            slot.pgno = 0;
            slot.authorization = None;
            slot.state = PrivatePageState::Vacant;
            slot.allocation_generation = 0;
            slot.adapter_owner = None;
            slot.adapter_tag = 0;
            slot.bytes.fill(0);
            slot.scope_vacant_next = vacant_head;
            slot.index_left = NO_SLOT;
            slot.index_right = NO_SLOT;
            slot.index_height = 0;
            slot.index_available = 0;
            slot.index_in_use = 0;
            slot.index_unscoped_available = 0;
            slot.scope_left = NO_SLOT;
            slot.scope_right = NO_SLOT;
            slot.scope_height = 0;
            slot.scope_available = 0;
            slot.scope_in_use = 0;
            slot.scope_count = 0;
            slot.scope_revision = 0;
            slot.scope_digest = 0;
            slot.binding_epoch += 1;
            let returned_revision = private_page_scope_payload_revision(slot);
            let returned_digest = private_page_scope_payload_digest(page.slot, slot);
            let owning = &mut slots[page.scope_anchor];
            owning.scope_vacant_head = page.slot;
            owning.scope_vacant_count += 1;
            owning.scope_vacant_revision =
                owning.scope_vacant_revision.wrapping_add(returned_revision);
            owning.scope_vacant_digest ^= returned_digest;
            self.authorized_len.set(self.authorized_len.get() - 1);
            self.epoch.set(self.epoch.get() + 1);
        }
        self.sync_aggregate_views(&slots);
        debug_assert_eq!(self.epoch.get(), prepared.final_epoch);
        Ok(prepared)
    }

    fn validate_coordinator_work(
        &self,
        work: &PrivatePageCoordinatorWork,
    ) -> Result<(), PrivatePagePoolError> {
        if work.pool_identity != self.identity
            || work.session_identity != self.coordinator_session_identity.get()
            || work.session_generation != self.coordinator_session_generation.get()
            || work.work_identity != self.coordinator_work_identity.get()
            || work.work_generation != self.coordinator_work_generation.get()
            || self.coordinator_work_phase.get() == PrivatePageCoordinatorWorkPhase::None
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        Ok(())
    }

    pub(crate) fn accept_coordinator_scope(
        &self,
        work: &PrivatePageCoordinatorWork,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        self.validate_coordinator_work(work)?;
        self.visit_exact_scope_layout(scope, |_, _, _| {})?;
        if self.coordinator_unaccepted_scopes.get() != 1
            || self.coordinator_scope_id.get() != scope.id
        {
            return Err(PrivatePagePoolError::UnacceptedCoordinatorScope);
        }
        self.coordinator_unaccepted_scopes.set(0);
        Ok(())
    }

    pub(crate) fn accept_sealed_coordinator_scope(
        &self,
        work: &PrivatePageCoordinatorWork,
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
    ) -> Result<(), PrivatePagePoolError> {
        self.validate_coordinator_work(work)?;
        if self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::Sealed
            || self.coordinator_unaccepted_scopes.get() != 1
            || self.coordinator_scope_id.get() != scope.id
        {
            return Err(PrivatePagePoolError::UnacceptedCoordinatorScope);
        }
        let status = self.validate_sealed_scope(scope, nonce)?;
        if !status.successor_consumed {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        let cleanup = self
            .coordinator_cleanup_pending
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        self.coordinator_unaccepted_scopes.set(0);
        self.coordinator_scope_id.set(0);
        self.coordinator_cleanup_pending.set(cleanup);
        Ok(())
    }

    /// Opens the only mutation window allowed for a sealed coordinator scope
    /// after its work registration has finished. The guard keeps ordinary
    /// scope APIs closed to every other retained record.
    pub(crate) fn begin_sealed_coordinator_cleanup(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
    ) -> Result<Option<PrivatePageSealedCoordinatorCleanup<'_, 'slots>>, PrivatePagePoolError> {
        if self.coordinator_session_identity.get() == 0 {
            return Ok(None);
        }
        if self.abort_required.get() {
            return Err(PrivatePagePoolError::AbortRequired);
        }
        if self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::None
            || self.coordinator_unaccepted_scopes.get() != 0
            || self.coordinator_scope_id.get() != 0
            || self.coordinator_cleanup_pending.get() == 0
            || self.sealed_coordinator_cleanup_scope_id.get() != 0
            || self.sealed_coordinator_cleanup_nonce.get() != 0
            || self.active_checkpoint.get() != 0
            || self.active_operation_id.get() != 0
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        let status = self.validate_sealed_scope(scope, nonce)?;
        if !status.successor_consumed {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        self.sealed_coordinator_cleanup_scope_id.set(scope.id);
        self.sealed_coordinator_cleanup_nonce.set(nonce);
        Ok(Some(PrivatePageSealedCoordinatorCleanup {
            pool: self,
            scope_id: scope.id,
            nonce,
            anchor: scope.anchor,
            active: true,
        }))
    }

    pub(crate) fn finish_coordinator_work(
        &self,
        work: PrivatePageCoordinatorWork,
    ) -> Result<(), (PrivatePageCoordinatorWork, PrivatePagePoolError)> {
        if let Err(error) = self.validate_coordinator_work(&work) {
            return Err((work, error));
        }
        if self.coordinator_unaccepted_scopes.get() != 0 {
            return Err((work, PrivatePagePoolError::UnacceptedCoordinatorScope));
        }
        if self.coordinator_scope_id.get() != 0 {
            return Err((
                work,
                PrivatePagePoolError::ScopeNotEmpty(self.active_scopes.get()),
            ));
        }
        self.coordinator_work_phase
            .set(PrivatePageCoordinatorWorkPhase::None);
        self.coordinator_work_identity.set(0);
        self.coordinator_work_start_epoch.set(0);
        self.coordinator_mutation_started.set(false);
        Ok(())
    }

    pub(crate) fn abort_coordinator_work<'pool>(
        &self,
        work: PrivatePageCoordinatorWork,
        scope: PrivatePageReservationScope<'pool>,
    ) -> Result<
        (),
        (
            PrivatePageCoordinatorWork,
            PrivatePageReservationScope<'pool>,
            PrivatePagePoolError,
        ),
    > {
        if let Err(error) = self.validate_coordinator_work(&work) {
            return Err((work, scope, error));
        }
        if scope.coordinator_scope_id() == 0
            || self.coordinator_scope_id.get() != scope.coordinator_scope_id()
            || self.coordinator_unaccepted_scopes.get() != 1
            || self.active_scopes.get() == 0
        {
            return Err((work, scope, PrivatePagePoolError::CoordinatorMismatch));
        }
        self.coordinator_cleanup_pending.set(1);
        self.abort_required.set(true);
        Ok(())
    }

    pub(crate) fn coordinator_scope_closed(
        &self,
        work: &PrivatePageCoordinatorWork,
        scope_id: u64,
    ) -> Result<(), PrivatePagePoolError> {
        self.validate_coordinator_work(work)?;
        if self.coordinator_scope_id.get() != scope_id || self.active_scopes.get() != 0 {
            return Err(PrivatePagePoolError::ScopeNotEmpty(
                self.active_scopes.get(),
            ));
        }
        self.coordinator_scope_id.set(0);
        Ok(())
    }

    pub(crate) fn coordinator_work_phase(&self) -> PrivatePageCoordinatorWorkPhase {
        self.coordinator_work_phase.get()
    }

    pub(crate) fn next_coordinator_work_generation(&self) -> Option<u64> {
        self.coordinator_work_generation.get().checked_add(1)
    }

    pub(crate) fn coordinator_registered_work(
        &self,
    ) -> (u64, u64, PrivatePageCoordinatorWorkPhase) {
        (
            self.coordinator_work_identity.get(),
            self.coordinator_work_generation.get(),
            self.coordinator_work_phase.get(),
        )
    }

    pub(crate) fn coordinator_commit_fence(&self) -> Result<(), PrivatePagePoolError> {
        if self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::None {
            return Err(PrivatePagePoolError::CoordinatorWorkActive);
        }
        if self.sealed_coordinator_cleanup_scope_id.get() != 0
            || self.sealed_coordinator_cleanup_nonce.get() != 0
        {
            return Err(PrivatePagePoolError::CoordinatorWorkActive);
        }
        if self.coordinator_unaccepted_scopes.get() != 0 {
            return Err(PrivatePagePoolError::UnacceptedCoordinatorScope);
        }
        if self.active_scopes.get() != 0 {
            return Err(PrivatePagePoolError::ScopeNotEmpty(
                self.active_scopes.get(),
            ));
        }
        if self.coordinator_cleanup_pending.get() != 0 {
            return Err(PrivatePagePoolError::AbortRequired);
        }
        Ok(())
    }

    pub(crate) fn coordinator_work_failed(&self) -> bool {
        if self.coordinator_work_phase.get() == PrivatePageCoordinatorWorkPhase::None {
            return false;
        }
        self.coordinator_cleanup_pending.set(1);
        self.abort_required.set(true);
        true
    }

    pub(crate) fn reserve_scope(
        &self,
        count: usize,
    ) -> Result<PrivatePageReservationScope<'slots>, PrivatePagePoolError> {
        if self.coordinator_session_identity.get() != 0 {
            return Err(PrivatePagePoolError::CoordinatorRequired);
        }
        self.reserve_scope_inner(count)
    }

    fn preflight_scope_reservation(&self, count: usize) -> Result<(), PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        if self.checkpoint_cleanup_slots.get() != 0
            || self.checkpoint_index_head.get() != NO_SLOT
            || self.checkpoint_index_count.get() != 0
        {
            return Err(PrivatePagePoolError::CheckpointMismatch);
        }
        if count == 0 || count > self.slot_count {
            return Err(PrivatePagePoolError::ReservationBudget {
                required: count,
                actual: self.slot_count,
            });
        }
        self.scope_sequence
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::ScopeIdentityExhausted)?;
        self.active_scopes
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::ScopeIdentityExhausted)?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_unscoped_vacancy_boundary(&slots)?;
        let available = self.unscoped_vacant_count.get();
        if count > available {
            return Err(PrivatePagePoolError::ReservationBudget {
                required: count,
                actual: available,
            });
        }
        let anchor = self.validate_unscoped_vacancy_prefix(&slots, count)?;
        let mut current = anchor;
        for _ in 0..count {
            slots[current]
                .binding_epoch
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
            current = slots[current].unscoped_vacant_next;
        }
        self.preflight_epoch_steps(
            u64::try_from(count).map_err(|_| PrivatePagePoolError::EpochExhausted)?,
        )?;
        Ok(())
    }

    #[cfg(test)]
    pub(crate) fn test_reserve_scope_direct(
        &self,
        count: usize,
    ) -> Result<PrivatePageReservationScope<'slots>, PrivatePagePoolError> {
        self.reserve_scope_inner(count)
    }

    fn reserve_scope_inner(
        &self,
        count: usize,
    ) -> Result<PrivatePageReservationScope<'slots>, PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        if self.checkpoint_cleanup_slots.get() != 0
            || self.checkpoint_index_head.get() != NO_SLOT
            || self.checkpoint_index_count.get() != 0
        {
            return Err(PrivatePagePoolError::CheckpointMismatch);
        }
        if count == 0 || count > self.slot_count {
            return Err(PrivatePagePoolError::ReservationBudget {
                required: count,
                actual: self.slot_count,
            });
        }
        let id = self
            .scope_sequence
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::ScopeIdentityExhausted)?;
        let active_scopes = self
            .active_scopes
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::ScopeIdentityExhausted)?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_unscoped_vacancy_boundary(&slots)?;
        let available = self.unscoped_vacant_count.get();
        if count > available {
            return Err(PrivatePagePoolError::ReservationBudget {
                required: count,
                actual: available,
            });
        }
        let anchor = self.validate_unscoped_vacancy_prefix(&slots, count)?;
        let mut current = anchor;
        for _ in 0..count {
            self.record_scope_lifecycle_visits(1);
            slots[current]
                .binding_epoch
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
            current = slots[current].unscoped_vacant_next;
        }
        self.preflight_epoch_steps(
            u64::try_from(count).map_err(|_| PrivatePagePoolError::EpochExhausted)?,
        )?;

        let mut head = NO_SLOT;
        let mut previous = NO_SLOT;
        for assigned in 0..count {
            let index = self.unscoped_vacant_head.get();
            debug_assert_ne!(index, NO_SLOT);
            self.remove_unscoped_vacancy_prepared(&mut slots, index);
            if head == NO_SLOT {
                head = index;
            } else {
                slots[previous].scope_vacant_next = index;
                slots[previous].scope_member_next = index;
            }
            let slot = &mut slots[index];
            slot.scope_id = id;
            slot.scope_anchor_index = anchor;
            slot.scope_member_next = NO_SLOT;
            slot.scope_member_head = NO_SLOT;
            slot.scope_member_ordinal = assigned;
            slot.scope_vacant_next = NO_SLOT;
            slot.binding_epoch += 1;
            self.advance_epoch_prepared();
            previous = index;
        }
        let anchor_slot = &mut slots[anchor];
        anchor_slot.scope_anchor = true;
        anchor_slot.scope_member_head = head;
        anchor_slot.scope_root = NO_SLOT;
        anchor_slot.scope_vacant_head = head;
        anchor_slot.scope_capacity = count;
        anchor_slot.scope_bound = 0;
        anchor_slot.scope_generation = 1;
        anchor_slot.scope_sealed = false;
        anchor_slot.scope_successor = 0;
        anchor_slot.successor_consumed = false;
        self.scope_sequence.set(id);
        self.active_scopes.set(active_scopes);
        Ok(PrivatePageReservationScope {
            pool_identity: self.identity,
            pool_epoch: self.identity_epoch,
            id,
            pending_txn: self.pending_txn,
            anchor,
            generation: 1,
            _pool: PhantomData,
        })
    }

    pub(crate) fn scoped_available(
        &self,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<usize, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let root = slots[anchor].scope_root;
        Ok(if root == NO_SLOT {
            0
        } else {
            slots[root].scope_available
        })
    }

    pub(crate) fn scoped_in_use(
        &self,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<usize, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let root = slots[anchor].scope_root;
        Ok(if root == NO_SLOT {
            0
        } else {
            slots[root].scope_in_use
        })
    }

    pub(crate) fn available_slot_at_rank_in_scope(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        mut rank: usize,
    ) -> Result<usize, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let mut root = slots[anchor].scope_root;
        while root != NO_SLOT {
            let page = &slots[root];
            if page.scope_id != scope.id || page.scope_anchor_index != scope.anchor {
                return Err(PrivatePagePoolError::StaleScope);
            }
            let left_available = if page.scope_left == NO_SLOT {
                0
            } else {
                slots[page.scope_left].scope_available
            };
            if rank < left_available {
                root = page.scope_left;
                continue;
            }
            rank -= left_available;
            if page.state == PrivatePageState::Available {
                if rank == 0 {
                    return Ok(root);
                }
                rank -= 1;
            }
            root = page.scope_right;
        }
        Err(PrivatePagePoolError::ReservationBudget {
            required: rank.saturating_add(1),
            actual: 0,
        })
    }

    pub(crate) fn scope_status(
        &self,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<PrivatePageReservationScopeStatus, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        Ok(PrivatePageReservationScopeStatus {
            capacity: slots[anchor].scope_capacity,
            bound: slots[anchor].scope_bound,
        })
    }

    fn scoped_info(page: &PrivatePagePoolSlot) -> PrivatePageScopedSlotInfo {
        PrivatePageScopedSlotInfo {
            bound: page.authorization.is_some(),
            member_ordinal: page.scope_member_ordinal,
            pgno: page.pgno,
            authorization: page.authorization,
            state: page.state.into(),
            binding_epoch: page.binding_epoch,
        }
    }

    /// Enumerate exactly one scope without inspecting foreign slots. Bound
    /// and vacant members share one immutable reservation-order chain, so the
    /// canonical layout remains stable across bind/unbind transitions.
    pub(crate) fn visit_exact_scope_layout(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        mut visitor: impl FnMut(usize, usize, PrivatePageScopedSlotInfo),
    ) -> Result<(), PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let capacity = slots[anchor].scope_capacity;
        let head = slots[anchor].scope_member_head;
        let mut member = head;
        for ordinal in 0..capacity {
            if member == NO_SLOT || member >= slots.len() {
                return Err(PrivatePagePoolError::StaleScope);
            }
            let page = &slots[member];
            if page.scope_id != scope.id
                || page.scope_anchor_index != anchor
                || page.scope_anchor != (member == anchor)
                || page.scope_member_head != if member == anchor { head } else { NO_SLOT }
                || page.scope_member_ordinal != ordinal
                || (page.authorization.is_none()) != (page.state == PrivatePageState::Vacant)
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            visitor(ordinal, member, Self::scoped_info(page));
            member = page.scope_member_next;
        }
        if member != NO_SLOT {
            return Err(PrivatePagePoolError::StaleScope);
        }
        #[cfg(test)]
        self.scope_layout_visits
            .set(self.scope_layout_visits.get().saturating_add(capacity));
        Ok(())
    }

    /// Copy the complete retirement-owned state of one standalone shadow scope
    /// into an unbound terminal journal. The live coordinator assigns its own
    /// exact vacant slots later; shadow slot numbers never cross pools.
    pub(crate) fn export_retirement_scope_terminal_pages(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        destination: &mut [PrivatePageCoordinatorTerminalPage],
    ) -> Result<(), PrivatePagePoolError> {
        if self.coordinator_session_identity.get() != 0
            || self.active_checkpoint.get() != 0
            || self.active_operation_id.get() != 0
            || destination
                .iter()
                .any(|page| *page != PrivatePageCoordinatorTerminalPage::empty())
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let capacity = slots[anchor].scope_capacity;
        let mut member = slots[anchor].scope_member_head;
        let mut written = 0usize;
        for ordinal in 0..capacity {
            if member == NO_SLOT || member >= slots.len() {
                return Err(PrivatePagePoolError::StaleScope);
            }
            let slot = &slots[member];
            if slot.scope_id != scope.id
                || slot.scope_anchor_index != anchor
                || slot.scope_member_ordinal != ordinal
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            if let PrivatePageState::InUse {
                owner: PrivatePageOwner::Retirement,
                tag,
                ..
            } = slot.state
            {
                let destination_len = destination.len();
                let output = destination.get_mut(written).ok_or(
                    PrivatePagePoolError::ReservationBudget {
                        required: written.saturating_add(1),
                        actual: destination_len,
                    },
                )?;
                let authorization = slot
                    .authorization
                    .ok_or(PrivatePagePoolError::InvalidState(member))?;
                if !matches!(tag, 1 | 2)
                    || !page::verify_crc32c(&slot.bytes)
                    || PageHeader::decode(&slot.bytes, self.pending_txn).is_err()
                {
                    return Err(PrivatePagePoolError::InvalidState(member));
                }
                *output = PrivatePageCoordinatorTerminalPage {
                    pool_slot: NO_SLOT,
                    pgno: slot.pgno,
                    authorization,
                    owner: PrivatePageOwner::Retirement,
                    owner_generation: self.pending_txn,
                    tag,
                    bytes: slot.bytes,
                };
                written += 1;
            }
            member = slot.scope_member_next;
        }
        if member != NO_SLOT || written != destination.len() {
            return Err(PrivatePagePoolError::ReservationBudget {
                required: written,
                actual: destination.len(),
            });
        }
        destination.sort_unstable_by_key(|page| page.pgno);
        for pair in destination.windows(2) {
            if pair[0].pgno >= pair[1].pgno {
                return Err(PrivatePagePoolError::PagesNotStrict {
                    previous: pair[0].pgno,
                    current: pair[1].pgno,
                });
            }
        }
        Ok(())
    }

    /// Copy the complete bitmap-owned state of one standalone shadow scope
    /// into an unbound terminal journal. This is deliberately separate from
    /// the retirement exporter so a typed bitmap-finalizer result is the only
    /// production authority that can request it.
    pub(crate) fn export_bitmap_scope_terminal_pages(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        destination: &mut [PrivatePageCoordinatorTerminalPage],
    ) -> Result<(), PrivatePagePoolError> {
        if self.coordinator_session_identity.get() != 0
            || self.active_checkpoint.get() != 0
            || self.active_operation_id.get() != 0
            || destination
                .iter()
                .any(|page| *page != PrivatePageCoordinatorTerminalPage::empty())
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let capacity = slots[anchor].scope_capacity;
        let mut member = slots[anchor].scope_member_head;
        let mut written = 0usize;
        for ordinal in 0..capacity {
            if member == NO_SLOT || member >= slots.len() {
                return Err(PrivatePagePoolError::StaleScope);
            }
            let slot = &slots[member];
            if slot.scope_id != scope.id
                || slot.scope_anchor_index != anchor
                || slot.scope_member_ordinal != ordinal
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            if let PrivatePageState::InUse {
                owner: PrivatePageOwner::Bitmap,
                tag,
                ..
            } = slot.state
            {
                let destination_len = destination.len();
                let output = destination.get_mut(written).ok_or(
                    PrivatePagePoolError::ReservationBudget {
                        required: written.saturating_add(1),
                        actual: destination_len,
                    },
                )?;
                let authorization = slot
                    .authorization
                    .ok_or(PrivatePagePoolError::InvalidState(member))?;
                let header = PageHeader::decode(&slot.bytes, self.pending_txn)
                    .map_err(|_| PrivatePagePoolError::InvalidState(member))?;
                if header.aux != 1
                    || !matches!(
                        header.page_type,
                        PageType::BitmapBranch | PageType::BitmapLeaf
                    )
                    || !page::verify_crc32c(&slot.bytes)
                {
                    return Err(PrivatePagePoolError::InvalidState(member));
                }
                *output = PrivatePageCoordinatorTerminalPage {
                    pool_slot: NO_SLOT,
                    pgno: slot.pgno,
                    authorization,
                    owner: PrivatePageOwner::Bitmap,
                    owner_generation: self.pending_txn,
                    tag,
                    bytes: slot.bytes,
                };
                written += 1;
            }
            member = slot.scope_member_next;
        }
        if member != NO_SLOT || written != destination.len() {
            return Err(PrivatePagePoolError::ReservationBudget {
                required: written,
                actual: destination.len(),
            });
        }
        destination.sort_unstable_by_key(|page| page.pgno);
        for pair in destination.windows(2) {
            if pair[0].pgno >= pair[1].pgno {
                return Err(PrivatePagePoolError::PagesNotStrict {
                    previous: pair[0].pgno,
                    current: pair[1].pgno,
                });
            }
        }
        Ok(())
    }

    /// Composite binding consumes the vacant chain head-first. A scope that was
    /// rebound into another vacant order must be rejected before a one-shot
    /// reclamation request is issued.
    pub(crate) fn validate_vacant_scope_bind_order(
        &self,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let capacity = slots[anchor].scope_capacity;
        if slots[anchor].scope_bound != 0 || slots[anchor].scope_root != NO_SLOT {
            return Err(PrivatePagePoolError::ScopeNotEmpty(
                slots[anchor].scope_bound,
            ));
        }
        let head = slots[anchor].scope_member_head;
        let mut member = head;
        let mut vacant = slots[anchor].scope_vacant_head;
        for ordinal in 0..capacity {
            if member == NO_SLOT
                || member >= slots.len()
                || vacant == NO_SLOT
                || vacant >= slots.len()
                || member != vacant
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            let page = &slots[member];
            if page.scope_id != scope.id
                || page.scope_anchor_index != anchor
                || page.scope_anchor != (member == anchor)
                || page.scope_member_ordinal != ordinal
                || !Self::is_canonical_vacant_payload(page)
                || page.unscoped_vacant_prev != NO_SLOT
                || page.unscoped_vacant_next != NO_SLOT
                || if member == anchor {
                    page.scope_member_head != head
                        || page.scope_root != NO_SLOT
                        || page.scope_vacant_head != head
                        || page.scope_capacity != capacity
                        || page.scope_bound != 0
                } else {
                    page.scope_member_head != NO_SLOT
                        || page.scope_root != NO_SLOT
                        || page.scope_vacant_head != NO_SLOT
                        || page.scope_capacity != 0
                        || page.scope_bound != 0
                }
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            member = page.scope_member_next;
            vacant = page.scope_vacant_next;
        }
        if member != NO_SLOT || vacant != NO_SLOT {
            return Err(PrivatePagePoolError::StaleScope);
        }
        #[cfg(test)]
        self.scope_layout_visits
            .set(self.scope_layout_visits.get().saturating_add(capacity));
        Ok(())
    }

    pub(crate) fn scoped_slot_info(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        slot: usize,
    ) -> Result<Option<PrivatePageScopedSlotInfo>, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != scope.id || page.scope_anchor_index != scope.anchor {
            return Ok(None);
        }
        Ok(Some(Self::scoped_info(page)))
    }

    pub(crate) fn find_in_scope(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        pgno: u32,
    ) -> Result<Option<usize>, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        self.find_scope_index(&slots, scope, slots[anchor].scope_root, pgno)
    }

    /// Read-only global collision lookup. Unlike the legacy `find`, this is
    /// valid while exact scopes exist and never grants ownership authority.
    pub(crate) fn find_bound_page(&self, pgno: u32) -> Result<Option<usize>, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        Ok(Self::find_index(&slots, self.index_root.get(), pgno))
    }

    pub(crate) fn backing_overlaps(
        &self,
        candidate: &[PrivatePagePoolSlot],
    ) -> Result<bool, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        if slots.is_empty() || candidate.is_empty() {
            return Ok(false);
        }
        let left = slots.as_ptr() as usize;
        let right = candidate.as_ptr() as usize;
        let left_end = left.checked_add(core::mem::size_of_val(&**slots)).ok_or(
            PrivatePagePoolError::ReservationBudget {
                required: usize::MAX,
                actual: slots.len(),
            },
        )?;
        let right_end = right.checked_add(core::mem::size_of_val(candidate)).ok_or(
            PrivatePagePoolError::ReservationBudget {
                required: usize::MAX,
                actual: candidate.len(),
            },
        )?;
        Ok(left < right_end && right < left_end)
    }

    pub(crate) fn exact_commitment(
        &self,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<PrivatePagePoolCommitment, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.commitment_with_slots(&slots, scope)
    }

    /// Constant-work coordinator commitment. Unlike `exact_commitment`, this
    /// uses the maintained scope-tree and vacancy aggregates so a retained
    /// record does not rescan every page in an earlier sealed scope.
    pub(crate) fn coordinator_scope_commitment(
        &self,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<PrivatePagePoolCommitment, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let scope_slot = &slots[anchor];
        let mut hash = 1_469_598_103_934_665_603u64;
        for value in [
            self.slot_count as u64,
            self.authorized_len.get() as u64,
            self.available_count.get() as u64,
            self.lowest_available.get() as u64,
            self.committed_page_count,
            self.pending_page_count.get(),
            self.pending_txn,
            self.identity as u64,
            self.identity_epoch as u64,
            self.generation.get(),
            self.epoch.get(),
            self.active_checkpoint.get(),
            self.checkpoint_cleanup_slots.get() as u64,
            self.checkpoint_index_head.get() as u64,
            self.checkpoint_index_count.get() as u64,
            self.scope_sequence.get(),
            self.active_scopes.get() as u64,
            self.unscoped_vacant_count.get() as u64,
            self.unscoped_vacant_head.get() as u64,
            self.unscoped_vacant_tail.get() as u64,
            self.index_root.get() as u64,
        ] {
            hash = pool_hash_u64(hash, value);
        }
        for value in [
            scope.id,
            anchor as u64,
            scope_slot.scope_member_head as u64,
            scope_slot.scope_root as u64,
            scope_slot.scope_vacant_head as u64,
            scope_slot.scope_capacity as u64,
            scope_slot.scope_bound as u64,
        ] {
            hash = pool_hash_u64(hash, value);
        }
        let (bound, tree_revision, tree_digest) = if scope_slot.scope_root == NO_SLOT {
            (0, 0, 0)
        } else {
            let root = slots
                .get(scope_slot.scope_root)
                .ok_or(PrivatePagePoolError::StaleScope)?;
            if root.scope_id != scope.id || root.scope_anchor_index != anchor {
                return Err(PrivatePagePoolError::StaleScope);
            }
            (root.scope_count, root.scope_revision, root.scope_digest)
        };
        if bound != scope_slot.scope_bound
            || scope_slot.scope_vacant_count
                != scope_slot
                    .scope_capacity
                    .checked_sub(scope_slot.scope_bound)
                    .ok_or(PrivatePagePoolError::StaleScope)?
        {
            return Err(PrivatePagePoolError::StaleScope);
        }
        for value in [
            bound as u64,
            tree_revision,
            tree_digest,
            scope_slot.scope_vacant_count as u64,
            scope_slot.scope_vacant_revision,
            scope_slot.scope_vacant_digest,
        ] {
            hash = pool_hash_u64(hash, value);
        }
        Ok(PrivatePagePoolCommitment {
            identity: self.identity,
            identity_epoch: self.identity_epoch,
            generation: self.generation.get(),
            epoch: self.epoch.get(),
            operation_sequence: self.operation_sequence.get(),
            active_operation_id: self.active_operation_id.get(),
            operation_start_epoch: self.operation_start_epoch.get(),
            abort_required: self.abort_required.get(),
            pending_page_count: self.pending_page_count.get(),
            scope_id: scope.id,
            scope_anchor: anchor,
            fingerprint: hash,
        })
    }

    pub(crate) fn validate_coordinator_scope_commitment(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        expected: &PrivatePagePoolCommitment,
    ) -> Result<(), PrivatePagePoolError> {
        if &self.coordinator_scope_commitment(scope)? != expected {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        Ok(())
    }

    pub(crate) fn exact_commitment_terminal_prepared(
        &self,
        scope: &PrivatePageReservationScope<'_>,
    ) -> PrivatePagePoolCommitment {
        let slots = self
            .slots
            .try_borrow()
            .expect("prepared terminal commitment owns the pool suffix");
        self.commitment_with_slots(&slots, scope)
            .expect("prepared sealed scope remains exact")
    }

    pub(crate) fn validate_exact_commitment(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        expected: &PrivatePagePoolCommitment,
    ) -> Result<(), PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        if &self.commitment_with_slots(&slots, scope)? != expected {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        Ok(())
    }

    /// Constant-time validation between two complete commitment seals.
    ///
    /// Every legal pool mutation advances `epoch`. The synchronous caller must
    /// hold an exact commitment captured before the read sequence and perform a
    /// complete validation after its final callback. Individual page reads then
    /// validate the scope identity plus exact page authority without repeatedly
    /// hashing the complete scope and every 4 KiB payload.
    pub(crate) fn validate_commitment_epoch(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        expected: &PrivatePagePoolCommitment,
    ) -> Result<(), PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        if expected.identity != self.identity
            || expected.identity_epoch != self.identity_epoch
            || expected.generation != self.generation.get()
            || expected.epoch != self.epoch.get()
            || expected.operation_sequence != self.operation_sequence.get()
            || expected.active_operation_id != self.active_operation_id.get()
            || expected.operation_start_epoch != self.operation_start_epoch.get()
            || expected.abort_required != self.abort_required.get()
            || expected.pending_page_count != self.pending_page_count.get()
            || expected.scope_id != scope.id
            || expected.scope_anchor != anchor
        {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        Ok(())
    }

    fn commitment_with_slots(
        &self,
        slots: &[PrivatePagePoolSlot],
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<PrivatePagePoolCommitment, PrivatePagePoolError> {
        self.commitment_with_slots_at_epoch(slots, scope, self.epoch.get())
    }

    fn commitment_with_slots_at_epoch(
        &self,
        slots: &[PrivatePagePoolSlot],
        scope: &PrivatePageReservationScope<'_>,
        epoch: u64,
    ) -> Result<PrivatePagePoolCommitment, PrivatePagePoolError> {
        let anchor = self.validate_scope(slots, scope)?;
        self.commitment_with_validated_anchor_at_epoch(slots, scope, anchor, epoch)
    }

    pub(super) fn commitment_with_validated_anchor_at_epoch(
        &self,
        slots: &[PrivatePagePoolSlot],
        scope: &PrivatePageReservationScope<'_>,
        anchor: usize,
        epoch: u64,
    ) -> Result<PrivatePagePoolCommitment, PrivatePagePoolError> {
        let fingerprint =
            private_page_scope_fingerprint_at_epoch(self, slots, anchor, scope.id, epoch)
                .ok_or(PrivatePagePoolError::StaleScope)?;
        Ok(PrivatePagePoolCommitment {
            identity: self.identity,
            identity_epoch: self.identity_epoch,
            generation: self.generation.get(),
            epoch,
            operation_sequence: self.operation_sequence.get(),
            active_operation_id: self.active_operation_id.get(),
            operation_start_epoch: self.operation_start_epoch.get(),
            abort_required: self.abort_required.get(),
            pending_page_count: self.pending_page_count.get(),
            scope_id: scope.id,
            scope_anchor: anchor,
            fingerprint,
        })
    }

    pub(crate) fn prepare_composite_scope_bind<'plan>(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        expected: PrivatePagePoolCommitment,
        stage_pool: &PrivatePagePool<'_>,
        stage_scope: &PrivatePageReservationScope<'_>,
        bindings: &'plan [PrivatePageCompositeBind],
    ) -> Result<PreparedPrivatePageCompositeBind<'plan>, PrivatePagePoolError> {
        self.require_mutation_idle()?;
        stage_pool.require_mutation_idle()?;
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        if stage_pool.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        if self.commitment_with_slots(&slots, scope)? != expected {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        let stage_slots = stage_pool
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        stage_pool.validate_scope(&stage_slots, stage_scope)?;
        let stage_commitment = stage_pool.commitment_with_slots(&stage_slots, stage_scope)?;
        let anchor = self.validate_scope(&slots, scope)?;
        if slots[anchor].scope_bound != 0
            || slots[anchor].scope_root != NO_SLOT
            || bindings.len() != slots[anchor].scope_capacity
        {
            return Err(PrivatePagePoolError::ReservationBudget {
                required: bindings.len(),
                actual: slots[anchor].scope_capacity,
            });
        }

        let mut vacant = slots[anchor].scope_vacant_head;
        let mut member = slots[anchor].scope_member_head;
        let mut previous = None;
        let mut pending = self.pending_page_count.get();
        let mut epoch_steps = 0u64;
        for (ordinal, binding) in bindings.iter().enumerate() {
            if vacant == NO_SLOT
                || member != vacant
                || binding.pool_slot != vacant
                || vacant >= slots.len()
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            let next_vacant = self.validate_scoped_vacancy_slot(
                &slots,
                scope,
                anchor,
                vacant,
                self.active_checkpoint.get(),
            )?;
            let slot = &slots[vacant];
            if slot.scope_member_ordinal != ordinal {
                return Err(PrivatePagePoolError::StaleScope);
            }
            if let Some(prior) = previous {
                if binding.pgno <= prior {
                    return Err(PrivatePagePoolError::PagesNotStrict {
                        previous: prior,
                        current: binding.pgno,
                    });
                }
            }
            if Self::find_index(&slots, self.index_root.get(), binding.pgno).is_some() {
                return Err(PrivatePagePoolError::PagesNotStrict {
                    previous: binding.pgno,
                    current: binding.pgno,
                });
            }
            match binding.authorization {
                PrivatePageAuthorization::CommittedFree
                | PrivatePageAuthorization::SafelyReclaimed => {
                    if binding.pgno < 2 || u64::from(binding.pgno) >= self.committed_page_count {
                        return Err(PrivatePagePoolError::AuthorizationMismatch {
                            pgno: binding.pgno,
                            authorization: binding.authorization,
                        });
                    }
                }
                PrivatePageAuthorization::Appended => {
                    if u64::from(binding.pgno) != pending || pending == MAX_PAGE_COUNT {
                        return Err(PrivatePagePoolError::AuthorizationMismatch {
                            pgno: binding.pgno,
                            authorization: binding.authorization,
                        });
                    }
                    pending += 1;
                }
            }
            let binding_steps = match binding.state {
                PrivatePageCompositeBindState::Available => 1u64,
                PrivatePageCompositeBindState::Bitmap {
                    committed_origin,
                    stage_slot,
                } => {
                    let staged = stage_slots
                        .get(stage_slot)
                        .ok_or(PrivatePagePoolError::SlotOutOfBounds(stage_slot))?;
                    if staged.scope_id != stage_scope.id
                        || staged.scope_anchor_index != stage_scope.anchor
                        || staged.pgno != binding.pgno
                        || staged.authorization != Some(binding.authorization)
                        || !matches!(
                            staged.state,
                            PrivatePageState::InUse {
                                owner: PrivatePageOwner::Bitmap,
                                owner_generation,
                                tag,
                                ..
                            } if owner_generation == self.pending_txn
                                && tag == u64::from(committed_origin)
                        )
                    {
                        return Err(PrivatePagePoolError::InvalidState(stage_slot));
                    }
                    2u64
                }
            };
            slot.binding_epoch
                .checked_add(binding_steps)
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
            epoch_steps = epoch_steps
                .checked_add(match binding.state {
                    PrivatePageCompositeBindState::Available => 1,
                    PrivatePageCompositeBindState::Bitmap { .. } => 3,
                })
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
            previous = Some(binding.pgno);
            member = slot.scope_member_next;
            vacant = next_vacant;
        }
        if vacant != NO_SLOT || member != NO_SLOT {
            return Err(PrivatePagePoolError::StaleScope);
        }
        self.authorized_len
            .get()
            .checked_add(bindings.len())
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        let final_epoch = self.preflight_epoch_steps(epoch_steps)?;
        Ok(PreparedPrivatePageCompositeBind {
            pool_identity: self.identity,
            pool_identity_epoch: self.identity_epoch,
            scope_id: scope.id,
            scope_anchor: scope.anchor,
            scope_generation: scope.generation,
            start: expected,
            final_epoch,
            final_pending_page_count: pending,
            bindings,
            stage_commitment,
        })
    }

    /// Apply a completely checked bind under one mutable borrow. Every return
    /// below precedes the first write; the mutation suffix is deterministic.
    pub(crate) fn apply_prepared_composite_scope_bind(
        &self,
        prepared: PreparedPrivatePageCompositeBind<'_>,
        stage_pool: &PrivatePagePool<'_>,
        stage_scope: &PrivatePageReservationScope<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        self.require_mutation_idle()?;
        stage_pool.require_mutation_idle()?;
        if stage_pool.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        if prepared.pool_identity != self.identity
            || prepared.pool_identity_epoch != self.identity_epoch
            || self.active_checkpoint.get() != 0
        {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        let stage_slots = stage_pool
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        stage_pool.validate_scope(&stage_slots, stage_scope)?;
        if stage_pool.commitment_with_slots(&stage_slots, stage_scope)? != prepared.stage_commitment
        {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        let scope = PrivatePageReservationScope {
            pool_identity: prepared.pool_identity,
            pool_epoch: prepared.pool_identity_epoch,
            id: prepared.scope_id,
            pending_txn: self.pending_txn,
            anchor: prepared.scope_anchor,
            generation: prepared.scope_generation,
            _pool: PhantomData,
        };
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        if self.commitment_with_slots(&slots, &scope)? != prepared.start {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        let anchor = self.validate_scope(&slots, &scope)?;
        if slots[anchor].scope_bound != 0 || slots[anchor].scope_capacity != prepared.bindings.len()
        {
            return Err(PrivatePagePoolError::StaleScope);
        }

        let mut epoch = self.epoch.get();
        let mut pending = self.pending_page_count.get();
        let mut scope_root = NO_SLOT;
        let mut index_root = self.index_root.get();
        for binding in prepared.bindings {
            let index = binding.pool_slot;
            let next_vacant = slots[index].scope_vacant_next;
            epoch += 1;
            let slot = &mut slots[index];
            slot.pgno = binding.pgno;
            slot.authorization = Some(binding.authorization);
            slot.state = PrivatePageState::Available;
            slot.allocation_generation = 0;
            slot.adapter_owner = None;
            slot.adapter_tag = 0;
            slot.bytes.fill(0);
            slot.scope_vacant_next = NO_SLOT;
            slot.index_left = NO_SLOT;
            slot.index_right = NO_SLOT;
            slot.index_height = 1;
            slot.index_available = 1;
            slot.index_in_use = 0;
            slot.index_unscoped_available = 0;
            slot.scope_left = NO_SLOT;
            slot.scope_right = NO_SLOT;
            slot.scope_height = 1;
            slot.scope_available = 1;
            slot.scope_in_use = 0;
            slot.binding_epoch += 1;
            if let PrivatePageCompositeBindState::Bitmap {
                committed_origin,
                stage_slot,
            } = binding.state
            {
                epoch += 1;
                slot.state = PrivatePageState::InUse {
                    owner: PrivatePageOwner::Bitmap,
                    owner_generation: self.pending_txn,
                    tag: u64::from(committed_origin),
                    authority_epoch: epoch,
                };
                slot.allocation_generation = self.generation.get();
                slot.binding_epoch += 1;
                slot.index_available = 0;
                slot.index_in_use = 1;
                slot.scope_available = 0;
                slot.scope_in_use = 1;
                epoch += 1;
                slot.bytes.copy_from_slice(&stage_slots[stage_slot].bytes);
            }
            if binding.authorization == PrivatePageAuthorization::Appended {
                pending += 1;
            }
            index_root = Self::index_insert_plain(&mut slots, index_root, index);
            scope_root = Self::scope_insert_prepared(&mut slots, scope_root, index, 0);
            slots[anchor].scope_vacant_head = next_vacant;
            slots[anchor].scope_bound += 1;
        }
        debug_assert_eq!(epoch, prepared.final_epoch);
        debug_assert_eq!(pending, prepared.final_pending_page_count);
        slots[anchor].scope_root = scope_root;
        self.index_root.set(index_root);
        self.authorized_len
            .set(self.authorized_len.get() + prepared.bindings.len());
        self.pending_page_count
            .set(prepared.final_pending_page_count);
        self.epoch.set(prepared.final_epoch);
        self.sync_aggregate_views(&slots);
        Ok(())
    }

    pub(crate) fn state_in_scope(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        slot: usize,
    ) -> Result<PrivatePagePoolState, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != scope.id || page.scope_anchor_index != scope.anchor {
            return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
        }
        Ok(page.state.into())
    }

    pub(crate) fn page_number_in_scope(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        slot: usize,
    ) -> Result<u32, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != scope.id || page.scope_anchor_index != scope.anchor {
            return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
        }
        page.authorization
            .ok_or(PrivatePagePoolError::SlotVacant(slot))?;
        Ok(page.pgno)
    }

    pub(crate) fn close_scope(
        &self,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        if slots[anchor].scope_bound != 0 || slots[anchor].scope_root != NO_SLOT {
            return Err(PrivatePagePoolError::ScopeNotEmpty(
                slots[anchor].scope_bound,
            ));
        }
        let capacity = slots[anchor].scope_capacity;
        let active_scopes = self
            .active_scopes
            .get()
            .checked_sub(1)
            .ok_or(PrivatePagePoolError::ActiveScopeUnderflow)?;
        self.validate_unscoped_vacancy_boundary(&slots)?;
        self.unscoped_vacant_count
            .get()
            .checked_add(capacity)
            .filter(|&count| count <= self.slot_count)
            .ok_or(PrivatePagePoolError::StaleScope)?;
        let head = slots[anchor].scope_member_head;
        let vacant_head = slots[anchor].scope_vacant_head;
        let mut member = head;
        for ordinal in 0..capacity {
            self.record_scope_lifecycle_visits(1);
            if member == NO_SLOT || member >= slots.len() {
                return Err(PrivatePagePoolError::StaleScope);
            }
            let slot = &slots[member];
            if !Self::is_canonical_vacant_payload(slot) {
                return Err(PrivatePagePoolError::ScopeNotEmpty(ordinal));
            }
            if slot.scope_id != scope.id
                || slot.scope_anchor_index != anchor
                || slot.scope_anchor != (member == anchor)
                || slot.scope_member_ordinal != ordinal
                || slot.unscoped_vacant_prev != NO_SLOT
                || slot.unscoped_vacant_next != NO_SLOT
                || if member == anchor {
                    slot.scope_member_head != head
                        || slot.scope_root != NO_SLOT
                        || slot.scope_vacant_head != vacant_head
                        || slot.scope_capacity != capacity
                        || slot.scope_bound != 0
                } else {
                    slot.scope_member_head != NO_SLOT
                        || slot.scope_root != NO_SLOT
                        || slot.scope_vacant_head != NO_SLOT
                        || slot.scope_capacity != 0
                        || slot.scope_bound != 0
                }
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            slot.binding_epoch
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
            member = slot.scope_member_next;
        }
        if member != NO_SLOT {
            return Err(PrivatePagePoolError::StaleScope);
        }
        let mut vacant = vacant_head;
        for _ in 0..capacity {
            self.record_scope_lifecycle_visits(1);
            if vacant == NO_SLOT || vacant >= slots.len() {
                return Err(PrivatePagePoolError::StaleScope);
            }
            let slot = &slots[vacant];
            if !Self::is_canonical_vacant_payload(slot)
                || slot.scope_id != scope.id
                || slot.scope_anchor_index != anchor
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            vacant = slot.scope_vacant_next;
        }
        if vacant != NO_SLOT {
            return Err(PrivatePagePoolError::StaleScope);
        }
        member = head;
        for ordinal in 0..capacity {
            self.record_scope_lifecycle_visits(1);
            slots[member].scope_validation_marker = ordinal + 1;
            member = slots[member].scope_member_next;
        }
        let mut exact_permutation = true;
        vacant = vacant_head;
        for _ in 0..capacity {
            self.record_scope_lifecycle_visits(1);
            let next = slots[vacant].scope_vacant_next;
            if slots[vacant].scope_validation_marker == 0 {
                exact_permutation = false;
            } else {
                slots[vacant].scope_validation_marker = 0;
            }
            vacant = next;
        }
        member = head;
        for _ in 0..capacity {
            self.record_scope_lifecycle_visits(1);
            if slots[member].scope_validation_marker != 0 {
                exact_permutation = false;
                slots[member].scope_validation_marker = 0;
            }
            member = slots[member].scope_member_next;
        }
        if !exact_permutation {
            return Err(PrivatePagePoolError::StaleScope);
        }
        self.preflight_epoch_steps(
            u64::try_from(capacity).map_err(|_| PrivatePagePoolError::EpochExhausted)?,
        )?;
        member = head;
        for _ in 0..capacity {
            let next = slots[member].scope_member_next;
            let slot = &mut slots[member];
            slot.scope_id = 0;
            slot.scope_anchor = false;
            slot.scope_anchor_index = NO_SLOT;
            slot.scope_member_next = NO_SLOT;
            slot.scope_member_head = NO_SLOT;
            slot.scope_member_ordinal = NO_SLOT;
            slot.scope_validation_marker = 0;
            slot.scope_vacant_next = NO_SLOT;
            slot.scope_root = NO_SLOT;
            slot.scope_vacant_head = NO_SLOT;
            slot.scope_capacity = 0;
            slot.scope_bound = 0;
            slot.scope_generation = 0;
            slot.scope_sealed = false;
            slot.scope_successor = 0;
            slot.successor_consumed = false;
            slot.binding_epoch += 1;
            self.append_unscoped_vacancy_prepared(&mut slots, member);
            self.advance_epoch_prepared();
            member = next;
        }
        self.active_scopes.set(active_scopes);
        Ok(())
    }

    pub(crate) fn bind_page(
        &self,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
        scope: &PrivatePageReservationScope<'_>,
        pgno: u32,
        authorization: PrivatePageAuthorization,
    ) -> Result<usize, PrivatePagePoolError> {
        self.validate_checkpoint(checkpoint)?;
        let extends_tail = self.validate_dynamic_authorization(pgno, authorization)?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        if let Some(previous) = Self::find_index(&slots, self.index_root.get(), pgno) {
            return Err(PrivatePagePoolError::PagesNotStrict {
                previous: slots[previous].pgno,
                current: pgno,
            });
        }
        let vacant_count = slots[anchor]
            .scope_capacity
            .checked_sub(slots[anchor].scope_bound)
            .ok_or(PrivatePagePoolError::StaleScope)?;
        let index = self.validate_scope_vacancy_head_window(&slots, scope, anchor, vacant_count)?;
        if index == NO_SLOT {
            return Err(PrivatePagePoolError::ReservationBudget {
                required: slots[anchor].scope_bound.saturating_add(1),
                actual: slots[anchor].scope_capacity,
            });
        }
        slots[anchor]
            .scope_bound
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        self.authorized_len
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        let next_epoch = self.preflight_checkpoint_slot(&slots[index], true)?;

        self.save_for_checkpoint(&mut slots[index]);
        Self::remember_index(&mut slots, index, checkpoint.generation);
        Self::remember_scope_header(&mut slots, anchor, checkpoint.generation);
        let next_vacant = slots[index].scope_vacant_next;
        slots[anchor].scope_vacant_head = next_vacant;
        slots[anchor].scope_bound += 1;
        let slot = &mut slots[index];
        slot.pgno = pgno;
        slot.authorization = Some(authorization);
        slot.state = PrivatePageState::Available;
        slot.allocation_generation = 0;
        slot.adapter_owner = None;
        slot.adapter_tag = 0;
        slot.bytes.fill(0);
        slot.scope_vacant_next = NO_SLOT;
        slot.index_left = NO_SLOT;
        slot.index_right = NO_SLOT;
        slot.index_height = 1;
        slot.index_available = 1;
        slot.index_in_use = 0;
        slot.index_unscoped_available = 0;
        slot.scope_left = NO_SLOT;
        slot.scope_right = NO_SLOT;
        slot.scope_height = 1;
        slot.scope_available = 1;
        slot.scope_in_use = 0;
        slot.binding_epoch += 1;
        let root = Self::index_insert_prepared(
            &mut slots,
            self.index_root.get(),
            index,
            checkpoint.generation,
        );
        self.index_root.set(root);
        let old_scope_root = slots[anchor].scope_root;
        let scope_root =
            Self::scope_insert_prepared(&mut slots, old_scope_root, index, checkpoint.generation);
        slots[anchor].scope_root = scope_root;
        if extends_tail {
            self.pending_page_count
                .set(self.pending_page_count.get() + 1);
        }
        self.authorized_len.set(self.authorized_len.get() + 1);
        self.epoch.set(next_epoch);
        self.sync_aggregate_views(&slots);
        Ok(index)
    }

    pub(crate) fn unbind_page(
        &self,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
        scope: &PrivatePageReservationScope<'_>,
        pgno: u32,
    ) -> Result<(), PrivatePagePoolError> {
        self.validate_checkpoint(checkpoint)?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let index = self
            .find_scope_index(&slots, scope, slots[anchor].scope_root, pgno)?
            .ok_or(PrivatePagePoolError::PageNotFound(pgno))?;
        let slot = &slots[index];
        if slot.authorization.is_none()
            || !matches!(
                slot.state,
                PrivatePageState::Available
                    | PrivatePageState::ReturnedFree
                    | PrivatePageState::ReturnedTail
            )
            || slot.allocation_generation != 0
            || slots[anchor].scope_bound == 0
        {
            return Err(PrivatePagePoolError::InvalidState(index));
        }
        let vacant_count = slots[anchor]
            .scope_capacity
            .checked_sub(slots[anchor].scope_bound)
            .ok_or(PrivatePagePoolError::StaleScope)?;
        self.validate_scope_vacancy_head_window(&slots, scope, anchor, vacant_count)?;
        let shrinks_tail = slot.authorization == Some(PrivatePageAuthorization::Appended)
            && u64::from(pgno) + 1 == self.pending_page_count.get();
        let next_epoch = self.preflight_checkpoint_slot(slot, true)?;

        self.save_for_checkpoint(&mut slots[index]);
        Self::remember_index(&mut slots, index, checkpoint.generation);
        Self::remember_scope_header(&mut slots, anchor, checkpoint.generation);
        let (root, removed) = Self::index_delete_prepared(
            &mut slots,
            self.index_root.get(),
            pgno,
            checkpoint.generation,
        );
        debug_assert_eq!(removed, index);
        self.index_root.set(root);
        let old_scope_root = slots[anchor].scope_root;
        let (scope_root, scope_removed) =
            Self::scope_delete_prepared(&mut slots, old_scope_root, pgno, checkpoint.generation);
        debug_assert_eq!(scope_removed, index);
        slots[anchor].scope_root = scope_root;
        slots[anchor].scope_bound -= 1;
        let vacant_head = slots[anchor].scope_vacant_head;
        let slot = &mut slots[index];
        slot.pgno = 0;
        slot.authorization = None;
        slot.state = PrivatePageState::Vacant;
        slot.allocation_generation = 0;
        slot.adapter_owner = None;
        slot.adapter_tag = 0;
        slot.bytes.fill(0);
        slot.scope_vacant_next = vacant_head;
        slot.index_left = NO_SLOT;
        slot.index_right = NO_SLOT;
        slot.index_height = 0;
        slot.index_available = 0;
        slot.index_in_use = 0;
        slot.index_unscoped_available = 0;
        slot.scope_left = NO_SLOT;
        slot.scope_right = NO_SLOT;
        slot.scope_height = 0;
        slot.scope_available = 0;
        slot.scope_in_use = 0;
        slot.binding_epoch += 1;
        slots[anchor].scope_vacant_head = index;
        if shrinks_tail {
            self.pending_page_count
                .set(self.pending_page_count.get() - 1);
        }
        self.authorized_len.set(self.authorized_len.get() - 1);
        self.epoch.set(next_epoch);
        self.sync_aggregate_views(&slots);
        Ok(())
    }

    pub(crate) fn claim_lowest_in_scope(
        &self,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
        scope: &PrivatePageReservationScope<'_>,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
    ) -> Result<PrivatePageAuthority<'_>, PrivatePagePoolError> {
        self.validate_checkpoint(checkpoint)?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let index = Self::lowest_available_in_scope(&slots, slots[anchor].scope_root).ok_or(
            PrivatePagePoolError::ReservationBudget {
                required: slots[anchor].scope_bound.saturating_add(1),
                actual: slots[anchor].scope_capacity,
            },
        )?;
        self.claim_scoped_slot(&mut slots, scope, index, owner, owner_generation, tag)
    }

    pub(crate) fn claim_page_in_scope(
        &self,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
        scope: &PrivatePageReservationScope<'_>,
        pgno: u32,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
    ) -> Result<PrivatePageAuthority<'_>, PrivatePagePoolError> {
        self.validate_checkpoint(checkpoint)?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let index = self
            .find_scope_index(&slots, scope, slots[anchor].scope_root, pgno)?
            .ok_or(PrivatePagePoolError::PageNotFound(pgno))?;
        self.claim_scoped_slot(&mut slots, scope, index, owner, owner_generation, tag)
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn claim_slot_in_scope_for_checkpoint_prepared(
        &self,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
        scope: &PrivatePageReservationScope<'_>,
        slot: usize,
        expected_binding_epoch: u64,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
        bytes: &[u8; PAGE_SIZE],
    ) {
        debug_assert!(self.validate_checkpoint(checkpoint).is_ok());
        let mut slots = self
            .slots
            .try_borrow_mut()
            .expect("prepared scoped checkpoint owns the pool mutation suffix");
        debug_assert!(self.validate_scope(&slots, scope).is_ok());
        debug_assert_eq!(slots[slot].scope_id, scope.id);
        debug_assert_eq!(slots[slot].scope_anchor_index, scope.anchor);
        debug_assert_eq!(slots[slot].binding_epoch, expected_binding_epoch);
        debug_assert_eq!(slots[slot].state, PrivatePageState::Available);
        debug_assert!(self.epoch.get() < checkpoint.reserved_end_epoch);
        self.save_for_checkpoint(&mut slots[slot]);
        slots[slot].state = PrivatePageState::InUse {
            owner,
            owner_generation,
            tag,
            authority_epoch: self.epoch.get() + 1,
        };
        slots[slot].allocation_generation = checkpoint.generation;
        slots[slot].bytes.copy_from_slice(bytes);
        self.advance_epoch_prepared();
        self.refresh_slot_counts(&mut slots, slot);
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn return_slot_in_scope_for_checkpoint_prepared(
        &self,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
        scope: &PrivatePageReservationScope<'_>,
        slot: usize,
        expected_binding_epoch: u64,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
        disposition: PrivatePageReturn,
    ) {
        debug_assert!(self.validate_checkpoint(checkpoint).is_ok());
        let mut slots = self
            .slots
            .try_borrow_mut()
            .expect("prepared scoped checkpoint owns the pool mutation suffix");
        debug_assert!(self.validate_scope(&slots, scope).is_ok());
        debug_assert_eq!(slots[slot].scope_id, scope.id);
        debug_assert_eq!(slots[slot].scope_anchor_index, scope.anchor);
        debug_assert_eq!(slots[slot].binding_epoch, expected_binding_epoch);
        debug_assert!(matches!(
            slots[slot].state,
            PrivatePageState::InUse {
                owner: actual_owner,
                owner_generation: actual_generation,
                tag: actual_tag,
                ..
            } if actual_owner == owner
                && actual_generation == owner_generation
                && actual_tag == tag
        ));
        debug_assert!(self.epoch.get() < checkpoint.reserved_end_epoch);
        self.save_for_checkpoint(&mut slots[slot]);
        slots[slot].state = PrivatePageState::PendingReturn {
            owner,
            owner_generation,
            tag,
            authority_epoch: self.epoch.get() + 1,
            disposition,
        };
        self.advance_epoch_prepared();
        self.refresh_slot_counts(&mut slots, slot);
    }

    fn claim_scoped_slot<'pool>(
        &'pool self,
        slots: &mut [PrivatePagePoolSlot],
        scope: &PrivatePageReservationScope<'_>,
        index: usize,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
    ) -> Result<PrivatePageAuthority<'pool>, PrivatePagePoolError> {
        if slots[index].state != PrivatePageState::Available {
            return Err(PrivatePagePoolError::PageUnavailable(slots[index].pgno));
        }
        if slots[index].scope_id != scope.id || slots[index].scope_anchor_index != scope.anchor {
            return Err(PrivatePagePoolError::ScopeMismatch(slots[index].pgno));
        }
        let next_epoch = self.preflight_checkpoint_slot(&slots[index], false)?;
        self.save_for_checkpoint(&mut slots[index]);
        slots[index].state = PrivatePageState::InUse {
            owner,
            owner_generation,
            tag,
            authority_epoch: next_epoch,
        };
        slots[index].allocation_generation = self.current_allocation_generation();
        slots[index].bytes.fill(0);
        self.epoch.set(next_epoch);
        self.refresh_slot_counts(slots, index);
        Ok(self.make_authority(index, &slots[index]))
    }

    fn lowest_available_in_scope(slots: &[PrivatePagePoolSlot], mut root: usize) -> Option<usize> {
        while root != NO_SLOT {
            let left = slots[root].scope_left;
            if left != NO_SLOT && slots[left].scope_available != 0 {
                root = left;
                continue;
            }
            if slots[root].state == PrivatePageState::Available {
                return Some(root);
            }
            root = slots[root].scope_right;
        }
        None
    }

    fn validate_scope(
        &self,
        slots: &[PrivatePagePoolSlot],
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<usize, PrivatePagePoolError> {
        if scope.pool_identity != self.identity
            || scope.pool_epoch != self.identity_epoch
            || scope.pending_txn != self.pending_txn
            || scope.anchor >= slots.len()
        {
            return Err(PrivatePagePoolError::PoolMismatch);
        }
        if self.coordinator_session_identity.get() != 0 {
            #[cfg(not(test))]
            if self.coordinator_work_phase.get() == PrivatePageCoordinatorWorkPhase::None {
                if self.sealed_coordinator_cleanup_scope_id.get() != scope.id
                    || self.sealed_coordinator_cleanup_nonce.get() == 0
                {
                    return Err(PrivatePagePoolError::CoordinatorMismatch);
                }
            } else if self.coordinator_scope_id.get() != scope.id {
                return Err(PrivatePagePoolError::CoordinatorMismatch);
            }
            #[cfg(test)]
            if self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::None
                && self.coordinator_scope_id.get() != scope.id
            {
                return Err(PrivatePagePoolError::CoordinatorMismatch);
            }
        }
        let anchor = &slots[scope.anchor];
        if scope.id == 0
            || anchor.scope_id != scope.id
            || !anchor.scope_anchor
            || anchor.scope_anchor_index != scope.anchor
            || anchor.scope_generation != scope.generation
        {
            return Err(PrivatePagePoolError::StaleScope);
        }
        Ok(scope.anchor)
    }

    fn validate_dynamic_authorization(
        &self,
        pgno: u32,
        authorization: PrivatePageAuthorization,
    ) -> Result<bool, PrivatePagePoolError> {
        if pgno < 2 {
            return Err(PrivatePagePoolError::PageOutOfBounds(pgno));
        }
        match authorization {
            PrivatePageAuthorization::CommittedFree | PrivatePageAuthorization::SafelyReclaimed => {
                if u64::from(pgno) >= self.committed_page_count {
                    return Err(PrivatePagePoolError::AuthorizationMismatch {
                        pgno,
                        authorization,
                    });
                }
                Ok(false)
            }
            PrivatePageAuthorization::Appended => {
                let page = u64::from(pgno);
                let pending = self.pending_page_count.get();
                if page < self.committed_page_count || page > pending {
                    return Err(PrivatePagePoolError::AuthorizationMismatch {
                        pgno,
                        authorization,
                    });
                }
                if page == pending {
                    if pending == MAX_PAGE_COUNT {
                        return Err(PrivatePagePoolError::PageOutOfBounds(pgno));
                    }
                    return Ok(true);
                }
                Ok(false)
            }
        }
    }

    fn preflight_checkpoint_slot(
        &self,
        slot: &PrivatePagePoolSlot,
        advances_binding: bool,
    ) -> Result<u64, PrivatePagePoolError> {
        let checkpoint = self.active_checkpoint.get();
        if checkpoint == 0 {
            return Err(PrivatePagePoolError::CheckpointMissing);
        }
        let new_cleanup = usize::from(slot.checkpoint_generation == 0);
        let cleanup = self
            .checkpoint_cleanup_slots
            .get()
            .checked_add(new_cleanup)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        let cleanup = u64::try_from(cleanup).map_err(|_| PrivatePagePoolError::EpochExhausted)?;
        self.preflight_epoch_steps(
            cleanup
                .checked_add(2)
                .ok_or(PrivatePagePoolError::EpochExhausted)?,
        )?;
        let next = self
            .epoch
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        let binding_steps = if advances_binding { 2 } else { 1 };
        slot.binding_epoch
            .checked_add(binding_steps)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        Ok(next)
    }

    fn remember_scope_header(slots: &mut [PrivatePagePoolSlot], anchor: usize, generation: u64) {
        if slots[anchor].saved_scope_generation == generation {
            return;
        }
        let slot = &mut slots[anchor];
        slot.saved_scope_generation = generation;
        slot.saved_scope_root = slot.scope_root;
        slot.saved_scope_vacant_head = slot.scope_vacant_head;
        slot.saved_scope_bound = slot.scope_bound;
    }

    fn advance_epoch_prepared(&self) {
        self.epoch.set(self.epoch.get() + 1);
    }

    fn reject_unscoped_legacy_access(&self, pgno: u32) -> Result<(), PrivatePagePoolError> {
        if self.coordinator_session_identity.get() != 0 {
            Err(PrivatePagePoolError::CoordinatorRequired)
        } else if self.active_scopes.get() != 0 {
            Err(PrivatePagePoolError::ScopeMismatch(pgno))
        } else {
            Ok(())
        }
    }

    pub(crate) fn mutation_snapshot(
        &self,
    ) -> Result<PrivatePagePoolSnapshot<'slots>, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(0)?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        drop(slots);
        Ok(PrivatePagePoolSnapshot {
            pool_identity: self.identity,
            epoch: self.epoch.get(),
            operation_sequence: self.operation_sequence.get(),
            active_operation_id: self.active_operation_id.get(),
            operation_start_epoch: self.operation_start_epoch.get(),
            abort_required: self.abort_required.get(),
            _slots: PhantomData,
        })
    }

    /// Constant-time mutation seal for callback reentrancy guards. Every legal
    /// pool mutation advances this epoch; exact scoped layout validation remains
    /// a separate, bounded final-plan check.
    pub(crate) fn mutation_epoch(&self) -> u64 {
        self.epoch.get()
    }

    pub(crate) fn preflight_mutation(
        &self,
        snapshot: &PrivatePagePoolSnapshot<'_>,
        epoch_steps: usize,
    ) -> Result<(), PrivatePagePoolError> {
        if snapshot.pool_identity != self.identity
            || snapshot.operation_sequence != self.operation_sequence.get()
            || snapshot.active_operation_id != self.active_operation_id.get()
            || snapshot.operation_start_epoch != self.operation_start_epoch.get()
            || snapshot.abort_required != self.abort_required.get()
        {
            return Err(PrivatePagePoolError::PoolMismatch);
        }
        self.reject_unscoped_legacy_access(0)?;
        let actual = self.epoch.get();
        if snapshot.epoch != actual {
            return Err(PrivatePagePoolError::StaleSnapshot {
                expected: snapshot.epoch,
                actual,
            });
        }
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        let steps = u64::try_from(epoch_steps).map_err(|_| PrivatePagePoolError::EpochExhausted)?;
        self.preflight_epoch_steps(steps)?;
        let slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        drop(slots);
        Ok(())
    }

    pub(crate) fn preflight_mutation_in_scope(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        commitment: &PrivatePagePoolCommitment,
        epoch_steps: usize,
    ) -> Result<(), PrivatePagePoolError> {
        if self.coordinator_session_identity.get() != 0 {
            return Err(PrivatePagePoolError::CoordinatorRequired);
        }
        self.preflight_mutation_in_scope_inner(scope, commitment, epoch_steps)
    }

    pub(crate) fn preflight_coordinator_mutation_in_scope(
        &self,
        work: &PrivatePageCoordinatorWork,
        scope: &PrivatePageReservationScope<'_>,
        commitment: &PrivatePagePoolCommitment,
        epoch_steps: usize,
    ) -> Result<(), PrivatePagePoolError> {
        self.validate_coordinator_work(work)?;
        if self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::Active
            || self.coordinator_scope_id.get() != scope.id
        {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        self.preflight_mutation_in_scope_inner(scope, commitment, epoch_steps)
    }

    fn preflight_mutation_in_scope_inner(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        commitment: &PrivatePagePoolCommitment,
        epoch_steps: usize,
    ) -> Result<(), PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        let steps = u64::try_from(epoch_steps).map_err(|_| PrivatePagePoolError::EpochExhausted)?;
        self.preflight_epoch_steps(steps)?;
        let slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        if &self.commitment_with_slots(&slots, scope)? != commitment {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        Ok(())
    }

    pub(crate) fn preflight_operation_in_scope<'plan>(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        mutation_steps: usize,
        operation_slots: &'plan mut [PrivatePageScopedOperationSlot],
    ) -> Result<PrivatePageScopedOperation<'plan>, PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        let steps =
            u64::try_from(mutation_steps).map_err(|_| PrivatePagePoolError::EpochExhausted)?;
        self.preflight_epoch_steps(
            steps
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)?,
        )?;
        let id = self
            .operation_sequence
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::GenerationExhausted)?;
        let generation = self
            .generation
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::GenerationExhausted)?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        #[cfg(test)]
        self.scoped_operation_duplicate_probes.set(0);
        let mut previous_slot = None;
        for planned in operation_slots.iter() {
            if let Some(previous) = previous_slot {
                #[cfg(test)]
                self.scoped_operation_duplicate_probes
                    .set(self.scoped_operation_duplicate_probes.get() + 1);
                if planned.slot <= previous {
                    return Err(PrivatePagePoolError::InvalidState(planned.slot));
                }
            }
            previous_slot = Some(planned.slot);
            let page = slots
                .get(planned.slot)
                .ok_or(PrivatePagePoolError::SlotOutOfBounds(planned.slot))?;
            if page.authorization.is_none()
                || page.scope_id != scope.id
                || page.scope_anchor_index != scope.anchor
            {
                return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
            }
            if page.binding_epoch != planned.binding_epoch {
                return Err(PrivatePagePoolError::StaleAuthority);
            }
            let binding_steps = u64::try_from(planned.binding_steps)
                .map_err(|_| PrivatePagePoolError::EpochExhausted)?;
            page.binding_epoch
                .checked_add(binding_steps)
                .and_then(|epoch| epoch.checked_add(1))
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
        }
        for planned in operation_slots.iter() {
            planned.used_binding_steps.set(0);
        }
        let operation = PrivatePageScopedOperation {
            pool_identity: self.identity,
            pool_epoch: self.identity_epoch,
            id,
            pending_txn: self.pending_txn,
            generation,
            scope_id: scope.id,
            scope_anchor: scope.anchor,
            start_epoch: self.epoch.get(),
            mutation_steps,
            used_mutation_steps: Cell::new(0),
            slots: operation_slots,
        };
        drop(slots);
        self.operation_sequence.set(id);
        self.active_operation_id.set(id);
        self.operation_start_epoch.set(operation.start_epoch);
        Ok(operation)
    }

    fn poison_active_operation_if_mutated(&self) {
        if self.active_operation_id.get() != 0
            && self.epoch.get() != self.operation_start_epoch.get()
        {
            self.abort_required.set(true);
        }
    }

    fn operation_error(&self, error: PrivatePagePoolError) -> PrivatePagePoolError {
        self.poison_active_operation_if_mutated();
        error
    }

    fn validate_operation_identity(
        &self,
        operation: &PrivatePageScopedOperation<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        if operation.pool_identity != self.identity
            || operation.pool_epoch != self.identity_epoch
            || operation.pending_txn != self.pending_txn
        {
            self.poison_active_operation_if_mutated();
            return Err(PrivatePagePoolError::PoolMismatch);
        }
        if self.active_operation_id.get() == 0 {
            return Err(PrivatePagePoolError::OperationMissing);
        }
        if operation.id == 0
            || operation.id != self.active_operation_id.get()
            || operation.generation
                != self
                    .generation
                    .get()
                    .checked_add(1)
                    .ok_or(PrivatePagePoolError::GenerationExhausted)?
            || operation.start_epoch != self.operation_start_epoch.get()
        {
            self.poison_active_operation_if_mutated();
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        Ok(())
    }

    fn validate_scoped_operation(
        &self,
        slots: &[PrivatePagePoolSlot],
        operation: &PrivatePageScopedOperation<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        self.validate_operation_identity(operation)?;
        if self.abort_required.get() {
            return Err(PrivatePagePoolError::AbortRequired);
        }
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        if operation.scope_anchor >= slots.len() {
            self.poison_active_operation_if_mutated();
            return Err(PrivatePagePoolError::StaleScope);
        }
        let anchor = &slots[operation.scope_anchor];
        if operation.scope_id == 0
            || !anchor.scope_anchor
            || anchor.scope_id != operation.scope_id
            || anchor.scope_anchor_index != operation.scope_anchor
        {
            self.poison_active_operation_if_mutated();
            return Err(PrivatePagePoolError::StaleScope);
        }
        let used = operation.used_mutation_steps.get();
        if used > operation.mutation_steps {
            self.poison_active_operation_if_mutated();
            return Err(PrivatePagePoolError::InvalidState(operation.scope_anchor));
        }
        let expected = operation
            .start_epoch
            .checked_add(
                u64::try_from(used)
                    .map_err(|_| self.operation_error(PrivatePagePoolError::EpochExhausted))?,
            )
            .ok_or(PrivatePagePoolError::EpochExhausted)
            .map_err(|error| self.operation_error(error))?;
        let actual = self.epoch.get();
        if actual != expected {
            self.poison_active_operation_if_mutated();
            return Err(PrivatePagePoolError::StaleSnapshot { expected, actual });
        }
        Ok(())
    }

    fn scoped_operation_slot<'operation>(
        &self,
        slots: &[PrivatePagePoolSlot],
        operation: &'operation PrivatePageScopedOperation<'_>,
        slot: usize,
    ) -> Result<&'operation PrivatePageScopedOperationSlot, PrivatePagePoolError> {
        self.validate_scoped_operation(slots, operation)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))
            .map_err(|error| self.operation_error(error))?;
        if page.authorization.is_none()
            || page.scope_id != operation.scope_id
            || page.scope_anchor_index != operation.scope_anchor
        {
            return Err(self.operation_error(PrivatePagePoolError::ScopeMismatch(page.pgno)));
        }
        let planned = operation
            .slots
            .iter()
            .find(|planned| planned.slot == slot)
            .ok_or(PrivatePagePoolError::ScopeMismatch(page.pgno))
            .map_err(|error| self.operation_error(error))?;
        let expected = planned
            .binding_epoch
            .checked_add(
                u64::try_from(planned.used_binding_steps.get())
                    .map_err(|_| self.operation_error(PrivatePagePoolError::EpochExhausted))?,
            )
            .ok_or(PrivatePagePoolError::EpochExhausted)
            .map_err(|error| self.operation_error(error))?;
        if page.binding_epoch != expected {
            return Err(self.operation_error(PrivatePagePoolError::StaleAuthority));
        }
        Ok(planned)
    }

    fn consume_scoped_operation_step(
        &self,
        operation: &PrivatePageScopedOperation<'_>,
    ) -> Result<u64, PrivatePagePoolError> {
        let used = operation
            .used_mutation_steps
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)
            .map_err(|error| self.operation_error(error))?;
        if used > operation.mutation_steps {
            return Err(
                self.operation_error(PrivatePagePoolError::InvalidState(operation.scope_anchor))
            );
        }
        let next_epoch = self
            .preflight_epoch_steps_raw(1)
            .map_err(|error| self.operation_error(error))?;
        operation.used_mutation_steps.set(used);
        self.epoch.set(next_epoch);
        Ok(next_epoch)
    }

    fn consume_scoped_binding_step(
        planned: &PrivatePageScopedOperationSlot,
    ) -> Result<(), PrivatePagePoolError> {
        let used = planned
            .used_binding_steps
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        if used > planned.binding_steps {
            return Err(PrivatePagePoolError::InvalidState(planned.slot));
        }
        planned.used_binding_steps.set(used);
        Ok(())
    }

    pub(crate) fn claim_slot_for_operation_in_scope_prepared(
        &self,
        operation: &PrivatePageScopedOperation<'_>,
        slot: usize,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
    ) -> Result<u64, PrivatePagePoolError> {
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| self.operation_error(PrivatePagePoolError::BorrowConflict))?;
        let planned = self.scoped_operation_slot(&slots, operation, slot)?;
        if slots[slot].state != PrivatePageState::Available {
            return Err(
                self.operation_error(PrivatePagePoolError::PageUnavailable(slots[slot].pgno))
            );
        }
        if planned.used_binding_steps.get() == planned.binding_steps {
            return Err(self.operation_error(PrivatePagePoolError::InvalidState(slot)));
        }
        let next_binding_epoch = slots[slot]
            .binding_epoch
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)
            .map_err(|error| self.operation_error(error))?;
        let next_epoch = self.consume_scoped_operation_step(operation)?;
        Self::consume_scoped_binding_step(planned).map_err(|error| self.operation_error(error))?;
        slots[slot].state = PrivatePageState::InUse {
            owner,
            owner_generation,
            tag,
            authority_epoch: next_epoch,
        };
        slots[slot].allocation_generation = operation.generation;
        slots[slot].bytes.fill(0);
        slots[slot].binding_epoch = next_binding_epoch;
        self.refresh_slot_counts(&mut slots, slot);
        Ok(next_binding_epoch)
    }

    pub(crate) fn write_slot_for_operation_in_scope_prepared(
        &self,
        operation: &PrivatePageScopedOperation<'_>,
        slot: usize,
        owner: PrivatePageOwner,
        owner_generation: u64,
        bytes: &[u8; PAGE_SIZE],
    ) -> Result<(), PrivatePagePoolError> {
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| self.operation_error(PrivatePagePoolError::BorrowConflict))?;
        self.scoped_operation_slot(&slots, operation, slot)?;
        match slots[slot].state {
            PrivatePageState::InUse {
                owner: actual_owner,
                owner_generation: actual_generation,
                ..
            } if actual_owner == owner && actual_generation == owner_generation => {}
            PrivatePageState::InUse {
                owner: actual_owner,
                ..
            }
            | PrivatePageState::PendingReturn {
                owner: actual_owner,
                ..
            } if actual_owner != owner => {
                return Err(self.operation_error(PrivatePagePoolError::OwnerMismatch {
                    pgno: slots[slot].pgno,
                    expected: owner,
                    actual: actual_owner,
                }));
            }
            _ => return Err(self.operation_error(PrivatePagePoolError::StaleAuthority)),
        }
        self.consume_scoped_operation_step(operation)?;
        slots[slot].bytes.copy_from_slice(bytes);
        Ok(())
    }

    pub(crate) fn return_slot_for_operation_in_scope_prepared(
        &self,
        operation: &PrivatePageScopedOperation<'_>,
        slot: usize,
        owner: PrivatePageOwner,
        owner_generation: u64,
        disposition: PrivatePageReturn,
    ) -> Result<u64, PrivatePagePoolError> {
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| self.operation_error(PrivatePagePoolError::BorrowConflict))?;
        let planned = self.scoped_operation_slot(&slots, operation, slot)?;
        match slots[slot].state {
            PrivatePageState::InUse {
                owner: actual_owner,
                owner_generation: actual_generation,
                ..
            } if actual_owner == owner && actual_generation == owner_generation => {}
            PrivatePageState::InUse {
                owner: actual_owner,
                ..
            }
            | PrivatePageState::PendingReturn {
                owner: actual_owner,
                ..
            } if actual_owner != owner => {
                return Err(self.operation_error(PrivatePagePoolError::OwnerMismatch {
                    pgno: slots[slot].pgno,
                    expected: owner,
                    actual: actual_owner,
                }));
            }
            _ => return Err(self.operation_error(PrivatePagePoolError::StaleAuthority)),
        }
        let authorization = slots[slot]
            .authorization
            .ok_or(PrivatePagePoolError::SlotVacant(slot))
            .map_err(|error| self.operation_error(error))?;
        if disposition == PrivatePageReturn::Tail
            && authorization != PrivatePageAuthorization::Appended
        {
            return Err(
                self.operation_error(PrivatePagePoolError::AuthorizationMismatch {
                    pgno: slots[slot].pgno,
                    authorization,
                }),
            );
        }
        if planned.used_binding_steps.get() == planned.binding_steps {
            return Err(self.operation_error(PrivatePagePoolError::InvalidState(slot)));
        }
        let next_binding_epoch = slots[slot]
            .binding_epoch
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)
            .map_err(|error| self.operation_error(error))?;
        self.consume_scoped_operation_step(operation)?;
        Self::consume_scoped_binding_step(planned).map_err(|error| self.operation_error(error))?;
        apply_return(&mut slots[slot], disposition);
        slots[slot].binding_epoch = next_binding_epoch;
        self.refresh_slot_counts(&mut slots, slot);
        Ok(next_binding_epoch)
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn finish_operation_in_scope<'plan>(
        &self,
        operation: PrivatePageScopedOperation<'plan>,
    ) -> Result<(), (PrivatePageScopedOperation<'plan>, PrivatePagePoolError)> {
        let slots = match self.slots.try_borrow() {
            Ok(slots) => slots,
            Err(_) => {
                self.poison_active_operation_if_mutated();
                return Err((operation, PrivatePagePoolError::BorrowConflict));
            }
        };
        if let Err(error) = self.validate_scoped_operation(&slots, &operation) {
            return Err((operation, error));
        }
        if operation.used_mutation_steps.get() != operation.mutation_steps
            || operation
                .slots
                .iter()
                .any(|planned| planned.used_binding_steps.get() != planned.binding_steps)
        {
            self.poison_active_operation_if_mutated();
            let anchor = operation.scope_anchor;
            return Err((operation, PrivatePagePoolError::InvalidState(anchor)));
        }
        let next_epoch = match self.preflight_epoch_steps_raw(1) {
            Ok(epoch) => epoch,
            Err(error) => {
                self.poison_active_operation_if_mutated();
                return Err((operation, error));
            }
        };
        drop(slots);
        self.epoch.set(next_epoch);
        self.generation.set(operation.generation);
        self.active_operation_id.set(0);
        self.operation_start_epoch.set(0);
        Ok(())
    }

    pub(crate) fn failed_operation_may_have_mutated(
        operation: &PrivatePageScopedOperation<'_>,
    ) -> bool {
        operation.used_mutation_steps.get() != 0
            || operation
                .slots
                .iter()
                .any(|slot| slot.used_binding_steps.get() != 0)
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn abandon_unmutated_operation<'plan>(
        &self,
        operation: PrivatePageScopedOperation<'plan>,
    ) -> Result<(), (PrivatePageScopedOperation<'plan>, PrivatePagePoolError)> {
        if self.epoch.get() != self.operation_start_epoch.get() {
            self.abort_required.set(true);
            return Err((operation, PrivatePagePoolError::AbortRequired));
        }
        let slots = match self.slots.try_borrow() {
            Ok(slots) => slots,
            Err(_) => return Err((operation, PrivatePagePoolError::BorrowConflict)),
        };
        if let Err(error) = self.validate_scoped_operation(&slots, &operation) {
            return Err((operation, error));
        }
        if Self::failed_operation_may_have_mutated(&operation) {
            self.abort_required.set(true);
            return Err((operation, PrivatePagePoolError::AbortRequired));
        }
        drop(slots);
        self.active_operation_id.set(0);
        self.operation_start_epoch.set(0);
        Ok(())
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn copy_owned_page(
        &self,
        snapshot: &PrivatePagePoolSnapshot<'_>,
        slot: usize,
        pgno: u32,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
        pending_txn: u64,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<(), PrivatePagePoolError> {
        if pending_txn != self.pending_txn {
            return Err(PrivatePagePoolError::PendingTransactionMismatch {
                expected: self.pending_txn,
                actual: pending_txn,
            });
        }
        self.preflight_mutation(snapshot, 0)?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(pgno));
        }
        if page.authorization.is_none() || page.pgno != pgno {
            return Err(PrivatePagePoolError::PageNotFound(pgno));
        }
        match page.state {
            PrivatePageState::InUse {
                owner: actual_owner,
                owner_generation: actual_generation,
                tag: actual_tag,
                ..
            } if actual_owner == owner
                && actual_generation == owner_generation
                && actual_tag == tag => {}
            PrivatePageState::InUse {
                owner: actual_owner,
                ..
            }
            | PrivatePageState::PendingReturn {
                owner: actual_owner,
                ..
            } if actual_owner != owner => {
                return Err(PrivatePagePoolError::OwnerMismatch {
                    pgno,
                    expected: owner,
                    actual: actual_owner,
                });
            }
            PrivatePageState::InUse { .. } | PrivatePageState::PendingReturn { .. } => {
                return Err(PrivatePagePoolError::StaleAuthority);
            }
            _ => return Err(PrivatePagePoolError::PageUnavailable(pgno)),
        }
        destination.copy_from_slice(&page.bytes);
        Ok(())
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn copy_owned_page_in_scope(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        slot: usize,
        binding_epoch: u64,
        pgno: u32,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<(), PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != scope.id || page.scope_anchor_index != scope.anchor {
            return Err(PrivatePagePoolError::ScopeMismatch(pgno));
        }
        if page.authorization.is_none() || page.pgno != pgno || page.binding_epoch != binding_epoch
        {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        match page.state {
            PrivatePageState::InUse {
                owner: actual_owner,
                owner_generation: actual_generation,
                tag: actual_tag,
                ..
            } if actual_owner == owner
                && actual_generation == owner_generation
                && actual_tag == tag => {}
            PrivatePageState::InUse {
                owner: actual_owner,
                ..
            }
            | PrivatePageState::PendingReturn {
                owner: actual_owner,
                ..
            } if actual_owner != owner => {
                return Err(PrivatePagePoolError::OwnerMismatch {
                    pgno,
                    expected: owner,
                    actual: actual_owner,
                });
            }
            PrivatePageState::InUse { .. } | PrivatePageState::PendingReturn { .. } => {
                return Err(PrivatePagePoolError::StaleAuthority);
            }
            _ => return Err(PrivatePagePoolError::PageUnavailable(pgno)),
        }
        destination.copy_from_slice(&page.bytes);
        Ok(())
    }

    pub(crate) fn len(&self) -> usize {
        self.slot_count
    }

    pub(crate) fn state(&self, slot: usize) -> Result<PrivatePagePoolState, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(0)?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
        }
        Ok(page.state.into())
    }

    pub(crate) fn validate_available(&self, slot: usize) -> Result<(), PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(0)?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
        }
        if page.state == PrivatePageState::Available {
            Ok(())
        } else {
            Err(PrivatePagePoolError::PageUnavailable(page.pgno))
        }
    }

    pub(crate) fn validate_owner(
        &self,
        slot: usize,
        owner: PrivatePageOwner,
        owner_generation: u64,
    ) -> Result<(), PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(0)?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
        }
        match page.state {
            PrivatePageState::InUse {
                owner: actual,
                owner_generation: actual_generation,
                ..
            } if actual == owner && actual_generation == owner_generation => Ok(()),
            PrivatePageState::InUse { owner: actual, .. }
            | PrivatePageState::PendingReturn { owner: actual, .. }
                if actual != owner =>
            {
                Err(PrivatePagePoolError::OwnerMismatch {
                    pgno: page.pgno,
                    expected: owner,
                    actual,
                })
            }
            _ => Err(PrivatePagePoolError::StaleAuthority),
        }
    }

    pub(crate) fn page_number(&self, slot: usize) -> Result<u32, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(0)?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let slot = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if slot.scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(slot.pgno));
        }
        slot.authorization
            .ok_or(PrivatePagePoolError::SlotVacant(slot.pgno as usize))?;
        Ok(slot.pgno)
    }

    pub(crate) fn authorization(
        &self,
        slot: usize,
    ) -> Result<PrivatePageAuthorization, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(0)?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let slot = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if slot.scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(slot.pgno));
        }
        slot.authorization
            .ok_or(PrivatePagePoolError::SlotVacant(slot.pgno as usize))
    }

    pub(crate) fn find(&self, pgno: u32) -> Result<Option<usize>, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(pgno)?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let Some(index) = Self::find_index(&slots, self.index_root.get(), pgno) else {
            return Ok(None);
        };
        if slots[index].scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(pgno));
        }
        Ok(Some(index))
    }

    pub(crate) fn authorize(
        &self,
        slot_index: usize,
        pgno: u32,
        authorization: PrivatePageAuthorization,
    ) -> Result<(), PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(pgno)?;
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        validate_authorization(
            pgno,
            authorization,
            self.committed_page_count,
            self.pending_page_count.get(),
        )?;
        let next_epoch = self.next_epoch()?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        if slot_index >= self.slot_count {
            return Err(PrivatePagePoolError::SlotOutOfBounds(slot_index));
        }
        if slot_index != self.authorized_len.get() {
            return Err(PrivatePagePoolError::AuthorizedAfterVacant(slot_index));
        }
        if Self::find_index(&slots, self.index_root.get(), pgno).is_some() {
            return Err(PrivatePagePoolError::PagesNotStrict {
                previous: pgno,
                current: pgno,
            });
        }
        if slots[slot_index].state != PrivatePageState::Vacant
            || slots[slot_index].authorization.is_some()
        {
            return Err(PrivatePagePoolError::SlotNotVacant(slot_index));
        }
        self.validate_unscoped_vacancy_boundary(&slots)?;
        self.validate_unscoped_vacancy_member(&slots, slot_index)?;
        if slot_index > 0 {
            let previous = &slots[slot_index - 1];
            if previous.authorization.is_none() {
                return Err(PrivatePagePoolError::AuthorizedAfterVacant(slot_index));
            }
            if pgno <= previous.pgno {
                return Err(PrivatePagePoolError::PagesNotStrict {
                    previous: previous.pgno,
                    current: pgno,
                });
            }
        }
        if slot_index + 1 < slots.len() {
            if let Some(next_authorization) = slots[slot_index + 1].authorization {
                let _ = next_authorization;
                if pgno >= slots[slot_index + 1].pgno {
                    return Err(PrivatePagePoolError::PagesNotStrict {
                        previous: pgno,
                        current: slots[slot_index + 1].pgno,
                    });
                }
            }
        }
        self.remove_unscoped_vacancy_prepared(&mut slots, slot_index);
        let page = &mut slots[slot_index];
        page.pgno = pgno;
        page.authorization = Some(authorization);
        page.state = PrivatePageState::Available;
        page.allocation_generation = 0;
        page.checkpoint_generation = 0;
        page.saved_state = SavedState::None;
        page.adapter_owner = None;
        page.adapter_tag = 0;
        page.bytes.fill(0);
        Self::reset_dynamic_metadata(page, true);
        page.binding_epoch = page
            .binding_epoch
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        let root = Self::index_insert_plain(&mut slots, self.index_root.get(), slot_index);
        self.index_root.set(root);
        self.authorized_len.set(slot_index + 1);
        self.sync_aggregate_views(&slots);
        self.epoch.set(next_epoch);
        Ok(())
    }

    pub(crate) fn available(&self) -> Result<usize, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(0)?;
        Ok(self.available_count.get())
    }

    pub(crate) fn adapter_label(
        &self,
        slot: usize,
    ) -> Result<Option<(PrivatePageOwner, u64)>, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(0)?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
        }
        Ok(page.adapter_owner.map(|owner| (owner, page.adapter_tag)))
    }

    pub(crate) fn claim_lowest(
        &self,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
    ) -> Result<PrivatePageAuthority<'_>, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(0)?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let slot = Self::lowest_unscoped_available_slot(&slots, self.index_root.get())
            .ok_or(PrivatePagePoolError::PageUnavailable(0))?;
        #[cfg(test)]
        self.claim_probe_count
            .set(self.claim_probe_count.get().saturating_add(1));
        let next_epoch = if self.active_checkpoint.get() == 0 {
            self.next_epoch()?
        } else {
            self.preflight_checkpoint_slot(&slots[slot], false)?
        };
        self.save_for_checkpoint(&mut slots[slot]);
        slots[slot].state = PrivatePageState::InUse {
            owner,
            owner_generation,
            tag,
            authority_epoch: next_epoch,
        };
        slots[slot].allocation_generation = self.current_allocation_generation();
        slots[slot].bytes.fill(0);
        self.epoch.set(next_epoch);
        self.refresh_slot_counts(&mut slots, slot);
        Ok(self.make_authority(slot, &slots[slot]))
    }

    pub(crate) fn claim(
        &self,
        slot: usize,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
    ) -> Result<PrivatePageAuthority<'_>, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(0)?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
        }
        if page.state != PrivatePageState::Available {
            return Err(PrivatePagePoolError::PageUnavailable(page.pgno));
        }
        let next_epoch = if self.active_checkpoint.get() == 0 {
            self.next_epoch()?
        } else {
            self.preflight_checkpoint_slot(page, false)?
        };
        self.save_for_checkpoint(&mut slots[slot]);
        slots[slot].state = PrivatePageState::InUse {
            owner,
            owner_generation,
            tag,
            authority_epoch: next_epoch,
        };
        slots[slot].allocation_generation = self.current_allocation_generation();
        slots[slot].bytes.fill(0);
        self.epoch.set(next_epoch);
        self.refresh_slot_counts(&mut slots, slot);
        Ok(self.make_authority(slot, &slots[slot]))
    }

    pub(crate) fn authority(
        &self,
        pgno: u32,
        owner: PrivatePageOwner,
        owner_generation: u64,
    ) -> Result<PrivatePageAuthority<'_>, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(pgno)?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let slot = Self::find_index(&slots, self.index_root.get(), pgno)
            .ok_or(PrivatePagePoolError::PageNotFound(pgno))?;
        if slots[slot].scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(pgno));
        }
        let next_epoch = if self.active_checkpoint.get() == 0 {
            self.next_epoch()?
        } else {
            self.preflight_checkpoint_slot(&slots[slot], false)?
        };
        match slots[slot].state {
            PrivatePageState::InUse {
                owner: actual,
                owner_generation: actual_generation,
                tag,
                ..
            } if actual == owner && actual_generation == owner_generation => {
                self.save_for_checkpoint(&mut slots[slot]);
                slots[slot].state = PrivatePageState::InUse {
                    owner,
                    owner_generation,
                    tag,
                    authority_epoch: next_epoch,
                };
            }
            PrivatePageState::InUse { owner: actual, .. }
            | PrivatePageState::PendingReturn { owner: actual, .. } => {
                return Err(PrivatePagePoolError::OwnerMismatch {
                    pgno,
                    expected: owner,
                    actual,
                });
            }
            _ => return Err(PrivatePagePoolError::PageUnavailable(pgno)),
        }
        self.epoch.set(next_epoch);
        Ok(self.make_authority(slot, &slots[slot]))
    }

    pub(crate) fn borrow_page<'borrow>(
        &'borrow self,
        authority: &PrivatePageAuthority<'_>,
    ) -> Result<PrivatePageRef<'borrow, 'slots>, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(authority.pgno)?;
        if authority.scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(authority.pgno));
        }
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_authority(&slots, authority)?;
        Ok(PrivatePageRef {
            slots,
            slot: authority.slot,
        })
    }

    pub(crate) fn borrow_page_mut<'borrow>(
        &'borrow self,
        authority: &PrivatePageAuthority<'_>,
    ) -> Result<PrivatePageRefMut<'borrow, 'slots>, PrivatePagePoolError> {
        self.reject_unscoped_legacy_access(authority.pgno)?;
        if authority.scope_id != 0 {
            return Err(PrivatePagePoolError::ScopeMismatch(authority.pgno));
        }
        let slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_authority(&slots, authority)?;
        let checkpoint = self.active_checkpoint.get();
        if checkpoint != 0 && slots[authority.slot].allocation_generation != checkpoint {
            return Err(PrivatePagePoolError::RollbackUnsafeWrite(authority.pgno));
        }
        let next_epoch = if checkpoint == 0 {
            self.next_epoch()?
        } else {
            self.preflight_checkpoint_slot(&slots[authority.slot], false)?
        };
        self.epoch.set(next_epoch);
        Ok(PrivatePageRefMut {
            slots,
            slot: authority.slot,
        })
    }

    pub(crate) fn write_page_for_checkpoint_prepared(
        &self,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
        authority: &PrivatePageAuthority<'_>,
        bytes: &[u8; PAGE_SIZE],
    ) -> Result<(), PrivatePagePoolError> {
        self.validate_checkpoint(checkpoint)?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_authority(&slots, authority)?;
        if slots[authority.slot].allocation_generation != checkpoint.generation {
            return Err(PrivatePagePoolError::RollbackUnsafeWrite(authority.pgno));
        }
        let next_epoch = self.next_epoch()?;
        if next_epoch > checkpoint.reserved_end_epoch {
            return Err(PrivatePagePoolError::EpochExhausted);
        }
        slots[authority.slot].bytes.copy_from_slice(bytes);
        self.epoch.set(next_epoch);
        Ok(())
    }

    pub(crate) fn authority_in_scope<'pool>(
        &'pool self,
        scope: &PrivatePageReservationScope<'_>,
        pgno: u32,
        owner: PrivatePageOwner,
        owner_generation: u64,
    ) -> Result<PrivatePageAuthority<'pool>, PrivatePagePoolError> {
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let slot = self
            .find_scope_index(&slots, scope, slots[anchor].scope_root, pgno)?
            .ok_or(PrivatePagePoolError::PageNotFound(pgno))?;
        let next_epoch = if self.active_checkpoint.get() == 0 {
            self.next_epoch()?
        } else {
            self.preflight_checkpoint_slot(&slots[slot], false)?
        };
        match slots[slot].state {
            PrivatePageState::InUse {
                owner: actual,
                owner_generation: actual_generation,
                tag,
                ..
            } if actual == owner && actual_generation == owner_generation => {
                self.save_for_checkpoint(&mut slots[slot]);
                slots[slot].state = PrivatePageState::InUse {
                    owner,
                    owner_generation,
                    tag,
                    authority_epoch: next_epoch,
                };
            }
            PrivatePageState::InUse { owner: actual, .. }
            | PrivatePageState::PendingReturn { owner: actual, .. } => {
                return Err(PrivatePagePoolError::OwnerMismatch {
                    pgno,
                    expected: owner,
                    actual,
                });
            }
            _ => return Err(PrivatePagePoolError::PageUnavailable(pgno)),
        }
        self.epoch.set(next_epoch);
        self.refresh_slot_counts(&mut slots, slot);
        Ok(self.make_authority(slot, &slots[slot]))
    }

    pub(crate) fn borrow_page_in_scope<'borrow>(
        &'borrow self,
        scope: &PrivatePageReservationScope<'_>,
        authority: &PrivatePageAuthority<'_>,
    ) -> Result<PrivatePageRef<'borrow, 'slots>, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        if authority.scope_id != scope.id {
            return Err(PrivatePagePoolError::ScopeMismatch(authority.pgno));
        }
        self.validate_authority(&slots, authority)?;
        Ok(PrivatePageRef {
            slots,
            slot: authority.slot,
        })
    }

    pub(crate) fn borrow_page_mut_in_scope<'borrow>(
        &'borrow self,
        scope: &PrivatePageReservationScope<'_>,
        authority: &PrivatePageAuthority<'_>,
    ) -> Result<PrivatePageRefMut<'borrow, 'slots>, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        if authority.scope_id != scope.id {
            return Err(PrivatePagePoolError::ScopeMismatch(authority.pgno));
        }
        self.validate_authority(&slots, authority)?;
        let checkpoint = self.active_checkpoint.get();
        if checkpoint != 0 && slots[authority.slot].allocation_generation != checkpoint {
            return Err(PrivatePagePoolError::RollbackUnsafeWrite(authority.pgno));
        }
        let next_epoch = if checkpoint == 0 {
            self.next_epoch()?
        } else {
            self.preflight_checkpoint_slot(&slots[authority.slot], false)?
        };
        self.epoch.set(next_epoch);
        Ok(PrivatePageRefMut {
            slots,
            slot: authority.slot,
        })
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn transfer_in_scope<'pool, 'authority>(
        &'pool self,
        scope: &PrivatePageReservationScope<'_>,
        authority: PrivatePageAuthority<'authority>,
        new_owner: PrivatePageOwner,
        new_owner_generation: u64,
        new_tag: u64,
    ) -> Result<PrivatePageAuthority<'pool>, (PrivatePageAuthority<'authority>, PrivatePagePoolError)>
    {
        let mut slots = retain_token_on_error!(
            authority,
            self.slots
                .try_borrow_mut()
                .map_err(|_| PrivatePagePoolError::BorrowConflict)
        );
        retain_token_on_error!(authority, self.validate_scope(&slots, scope));
        if authority.scope_id != scope.id {
            let pgno = authority.pgno;
            return Err((authority, PrivatePagePoolError::ScopeMismatch(pgno)));
        }
        retain_token_on_error!(authority, self.validate_authority(&slots, &authority));
        let next_epoch = if self.active_checkpoint.get() == 0 {
            retain_token_on_error!(authority, self.next_epoch())
        } else {
            retain_token_on_error!(
                authority,
                self.preflight_checkpoint_slot(&slots[authority.slot], false)
            )
        };
        self.save_for_checkpoint(&mut slots[authority.slot]);
        slots[authority.slot].state = PrivatePageState::InUse {
            owner: new_owner,
            owner_generation: new_owner_generation,
            tag: new_tag,
            authority_epoch: next_epoch,
        };
        self.epoch.set(next_epoch);
        self.refresh_slot_counts(&mut slots, authority.slot);
        Ok(self.make_authority(authority.slot, &slots[authority.slot]))
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn return_page_in_scope<'authority>(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        authority: PrivatePageAuthority<'authority>,
        disposition: PrivatePageReturn,
    ) -> Result<(), (PrivatePageAuthority<'authority>, PrivatePagePoolError)> {
        let mut slots = retain_token_on_error!(
            authority,
            self.slots
                .try_borrow_mut()
                .map_err(|_| PrivatePagePoolError::BorrowConflict)
        );
        retain_token_on_error!(authority, self.validate_scope(&slots, scope));
        if authority.scope_id != scope.id {
            let pgno = authority.pgno;
            return Err((authority, PrivatePagePoolError::ScopeMismatch(pgno)));
        }
        retain_token_on_error!(authority, self.validate_authority(&slots, &authority));
        let authorization = retain_token_on_error!(
            authority,
            slots[authority.slot]
                .authorization
                .ok_or(PrivatePagePoolError::SlotVacant(authority.slot))
        );
        if disposition == PrivatePageReturn::Tail
            && authorization != PrivatePageAuthorization::Appended
        {
            let pgno = authority.pgno;
            return Err((
                authority,
                PrivatePagePoolError::AuthorizationMismatch {
                    pgno,
                    authorization,
                },
            ));
        }
        let next_epoch = if self.active_checkpoint.get() == 0 {
            retain_token_on_error!(authority, self.next_epoch())
        } else {
            retain_token_on_error!(
                authority,
                self.preflight_checkpoint_slot(&slots[authority.slot], false)
            )
        };
        self.save_for_checkpoint(&mut slots[authority.slot]);
        if self.active_checkpoint.get() == 0 {
            apply_return(&mut slots[authority.slot], disposition);
        } else {
            slots[authority.slot].state = PrivatePageState::PendingReturn {
                owner: authority.owner,
                owner_generation: authority.owner_generation,
                tag: state_tag(slots[authority.slot].state),
                authority_epoch: next_epoch,
                disposition,
            };
        }
        self.epoch.set(next_epoch);
        self.refresh_slot_counts(&mut slots, authority.slot);
        Ok(())
    }

    /// Release only matching pages from one exact scope. Every member and all
    /// epoch headroom are validated before the first mutation, so token-drop
    /// cleanup cannot partially release a generation.
    pub(crate) fn release_generation_in_scope(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
    ) -> Result<(), PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let capacity = slots[anchor].scope_capacity;
        let mut member = slots[anchor].scope_member_head;
        let mut matches = 0usize;
        for ordinal in 0..capacity {
            let page = slots.get(member).ok_or(PrivatePagePoolError::StaleScope)?;
            if page.scope_id != scope.id
                || page.scope_anchor_index != anchor
                || page.scope_member_ordinal != ordinal
                || page.scope_anchor != (member == anchor)
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            if matches!(
                page.state,
                PrivatePageState::InUse {
                    owner: actual_owner,
                    owner_generation: actual_generation,
                    tag: actual_tag,
                    ..
                } if actual_owner == owner
                    && actual_generation == owner_generation
                    && actual_tag == tag
            ) {
                page.binding_epoch
                    .checked_add(1)
                    .ok_or(PrivatePagePoolError::EpochExhausted)?;
                matches += 1;
            }
            member = page.scope_member_next;
        }
        if member != NO_SLOT {
            return Err(PrivatePagePoolError::StaleScope);
        }
        self.preflight_epoch_steps(
            u64::try_from(matches).map_err(|_| PrivatePagePoolError::EpochExhausted)?,
        )?;

        member = slots[anchor].scope_member_head;
        for _ in 0..capacity {
            let next = slots[member].scope_member_next;
            if matches!(
                slots[member].state,
                PrivatePageState::InUse {
                    owner: actual_owner,
                    owner_generation: actual_generation,
                    tag: actual_tag,
                    ..
                } if actual_owner == owner
                    && actual_generation == owner_generation
                    && actual_tag == tag
            ) {
                apply_return(&mut slots[member], PrivatePageReturn::Available);
                slots[member].binding_epoch += 1;
                self.advance_epoch_prepared();
                self.refresh_slot_counts(&mut slots, member);
            }
            member = next;
        }
        Ok(())
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn transfer<'pool, 'authority>(
        &'pool self,
        authority: PrivatePageAuthority<'authority>,
        new_owner: PrivatePageOwner,
        new_owner_generation: u64,
        new_tag: u64,
    ) -> Result<PrivatePageAuthority<'pool>, (PrivatePageAuthority<'authority>, PrivatePagePoolError)>
    {
        retain_token_on_error!(
            authority,
            self.reject_unscoped_legacy_access(authority.pgno)
        );
        if authority.scope_id != 0 {
            let pgno = authority.pgno;
            return Err((authority, PrivatePagePoolError::ScopeMismatch(pgno)));
        }
        let mut slots = retain_token_on_error!(
            authority,
            self.slots
                .try_borrow_mut()
                .map_err(|_| PrivatePagePoolError::BorrowConflict)
        );
        retain_token_on_error!(authority, self.validate_authority(&slots, &authority));
        let next_epoch = if self.active_checkpoint.get() == 0 {
            retain_token_on_error!(authority, self.next_epoch())
        } else {
            retain_token_on_error!(
                authority,
                self.preflight_checkpoint_slot(&slots[authority.slot], false)
            )
        };
        self.save_for_checkpoint(&mut slots[authority.slot]);
        slots[authority.slot].state = PrivatePageState::InUse {
            owner: new_owner,
            owner_generation: new_owner_generation,
            tag: new_tag,
            authority_epoch: next_epoch,
        };
        self.epoch.set(next_epoch);
        Ok(self.make_authority(authority.slot, &slots[authority.slot]))
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn return_page<'authority>(
        &self,
        authority: PrivatePageAuthority<'authority>,
        disposition: PrivatePageReturn,
    ) -> Result<(), (PrivatePageAuthority<'authority>, PrivatePagePoolError)> {
        retain_token_on_error!(
            authority,
            self.reject_unscoped_legacy_access(authority.pgno)
        );
        if authority.scope_id != 0 {
            let pgno = authority.pgno;
            return Err((authority, PrivatePagePoolError::ScopeMismatch(pgno)));
        }
        let mut slots = retain_token_on_error!(
            authority,
            self.slots
                .try_borrow_mut()
                .map_err(|_| PrivatePagePoolError::BorrowConflict)
        );
        retain_token_on_error!(authority, self.validate_authority(&slots, &authority));
        let next_epoch = if self.active_checkpoint.get() == 0 {
            retain_token_on_error!(authority, self.next_epoch())
        } else {
            retain_token_on_error!(
                authority,
                self.preflight_checkpoint_slot(&slots[authority.slot], false)
            )
        };
        let authorization = retain_token_on_error!(
            authority,
            slots[authority.slot]
                .authorization
                .ok_or(PrivatePagePoolError::SlotVacant(authority.slot))
        );
        if disposition == PrivatePageReturn::Tail
            && authorization != PrivatePageAuthorization::Appended
        {
            let pgno = authority.pgno;
            return Err((
                authority,
                PrivatePagePoolError::AuthorizationMismatch {
                    pgno,
                    authorization,
                },
            ));
        }
        self.save_for_checkpoint(&mut slots[authority.slot]);
        let active = self.active_checkpoint.get();
        if active == 0 {
            apply_return(&mut slots[authority.slot], disposition);
        } else {
            slots[authority.slot].state = PrivatePageState::PendingReturn {
                owner: authority.owner,
                owner_generation: authority.owner_generation,
                tag: state_tag(slots[authority.slot].state),
                authority_epoch: next_epoch,
                disposition,
            };
        }
        self.epoch.set(next_epoch);
        self.refresh_slot_counts(&mut slots, authority.slot);
        Ok(())
    }

    pub(crate) fn begin_checkpoint(
        &self,
    ) -> Result<PrivatePagePoolCheckpoint<'slots>, PrivatePagePoolError> {
        let checkpoint = self.preflight_checkpoint()?;
        self.begin_checkpoint_prepared(&checkpoint)?;
        Ok(checkpoint)
    }

    #[cfg(test)]
    pub(crate) fn test_begin_checkpoint_direct(
        &self,
    ) -> Result<PrivatePagePoolCheckpoint<'slots>, PrivatePagePoolError> {
        let checkpoint = self.preflight_checkpoint_steps_inner(2)?;
        self.begin_checkpoint_prepared_inner(&checkpoint)?;
        Ok(checkpoint)
    }

    pub(crate) fn preflight_checkpoint(
        &self,
    ) -> Result<PrivatePagePoolCheckpoint<'slots>, PrivatePagePoolError> {
        if self.coordinator_session_identity.get() != 0
            && self.sealed_coordinator_cleanup_scope_id.get() == 0
        {
            return Err(PrivatePagePoolError::CoordinatorRequired);
        }
        self.preflight_checkpoint_steps(2)
    }

    pub(crate) fn preflight_checkpoint_steps(
        &self,
        epoch_steps: usize,
    ) -> Result<PrivatePagePoolCheckpoint<'slots>, PrivatePagePoolError> {
        if self.coordinator_session_identity.get() != 0
            && self.sealed_coordinator_cleanup_scope_id.get() == 0
        {
            return Err(PrivatePagePoolError::CoordinatorRequired);
        }
        self.preflight_checkpoint_steps_inner(epoch_steps)
    }

    pub(crate) fn preflight_coordinator_checkpoint_steps(
        &self,
        work: &PrivatePageCoordinatorWork,
        epoch_steps: usize,
    ) -> Result<PrivatePagePoolCheckpoint<'slots>, PrivatePagePoolError> {
        self.validate_coordinator_work(work)?;
        if self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::Active {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        self.preflight_checkpoint_steps_inner(epoch_steps)
    }

    fn preflight_checkpoint_steps_inner(
        &self,
        epoch_steps: usize,
    ) -> Result<PrivatePagePoolCheckpoint<'slots>, PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        if epoch_steps < 2 {
            return Err(PrivatePagePoolError::EpochExhausted);
        }
        let generation = self
            .generation
            .get()
            .checked_add(1)
            .ok_or(PrivatePagePoolError::GenerationExhausted)?;
        let reserved_end_epoch = self.preflight_epoch_steps(
            u64::try_from(epoch_steps).map_err(|_| PrivatePagePoolError::EpochExhausted)?,
        )?;
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        drop(slots);
        Ok(PrivatePagePoolCheckpoint {
            pool_identity: self.identity,
            pool_epoch: self.identity_epoch,
            generation,
            index_root: self.index_root.get(),
            authorized_len: self.authorized_len.get(),
            available_count: self.available_count.get(),
            lowest_available: self.lowest_available.get(),
            pending_page_count: self.pending_page_count.get(),
            start_epoch: self.epoch.get(),
            reserved_end_epoch,
            _slots: PhantomData,
        })
    }

    pub(crate) fn begin_checkpoint_prepared(
        &self,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        if self.coordinator_session_identity.get() != 0
            && self.sealed_coordinator_cleanup_scope_id.get() == 0
        {
            return Err(PrivatePagePoolError::CoordinatorRequired);
        }
        self.begin_checkpoint_prepared_inner(checkpoint)
    }

    pub(crate) fn begin_coordinator_checkpoint_prepared(
        &self,
        work: &PrivatePageCoordinatorWork,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        self.validate_coordinator_work(work)?;
        if self.coordinator_work_phase.get() != PrivatePageCoordinatorWorkPhase::Active {
            return Err(PrivatePagePoolError::CoordinatorMismatch);
        }
        self.begin_checkpoint_prepared_inner(checkpoint)
    }

    fn begin_checkpoint_prepared_inner(
        &self,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        self.require_mutation_idle()?;
        if checkpoint.pool_identity != self.identity || checkpoint.pool_epoch != self.identity_epoch
        {
            return Err(PrivatePagePoolError::PoolMismatch);
        }
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        if checkpoint.start_epoch != self.epoch.get()
            || checkpoint.generation
                != self
                    .generation
                    .get()
                    .checked_add(1)
                    .ok_or(PrivatePagePoolError::GenerationExhausted)?
        {
            return Err(PrivatePagePoolError::CheckpointMismatch);
        }
        self.generation.set(checkpoint.generation);
        self.active_checkpoint.set(checkpoint.generation);
        self.checkpoint_cleanup_slots.set(0);
        self.checkpoint_index_head.set(NO_SLOT);
        self.checkpoint_index_count.set(0);
        self.advance_epoch_prepared();
        Ok(())
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn commit_checkpoint<'checkpoint>(
        &self,
        checkpoint: PrivatePagePoolCheckpoint<'checkpoint>,
    ) -> Result<(), (PrivatePagePoolCheckpoint<'checkpoint>, PrivatePagePoolError)> {
        retain_token_on_error!(checkpoint, self.validate_checkpoint(&checkpoint));
        let mut slots = retain_token_on_error!(
            checkpoint,
            self.slots
                .try_borrow_mut()
                .map_err(|_| PrivatePagePoolError::BorrowConflict)
        );
        let mut touched = 0usize;
        for (index, slot) in slots.iter().enumerate() {
            if slot.checkpoint_generation != 0
                && slot.checkpoint_generation != checkpoint.generation
            {
                return Err((checkpoint, PrivatePagePoolError::CheckpointMismatch));
            }
            if slot.saved_index_generation != 0
                && slot.saved_index_generation != checkpoint.generation
            {
                return Err((checkpoint, PrivatePagePoolError::CheckpointMismatch));
            }
            if slot.saved_scope_generation != 0
                && slot.saved_scope_generation != checkpoint.generation
            {
                return Err((checkpoint, PrivatePagePoolError::CheckpointMismatch));
            }
            if slot.checkpoint_generation == checkpoint.generation
                && (slot.saved_state == SavedState::None
                    || slot.saved_binding == SavedBinding::None)
            {
                return Err((checkpoint, PrivatePagePoolError::InvalidState(index)));
            }
            if slot.checkpoint_generation == checkpoint.generation {
                touched = retain_token_on_error!(
                    checkpoint,
                    touched
                        .checked_add(1)
                        .ok_or(PrivatePagePoolError::EpochExhausted)
                );
            }
        }
        if touched != self.checkpoint_cleanup_slots.get() {
            return Err((checkpoint, PrivatePagePoolError::CheckpointMismatch));
        }
        retain_token_on_error!(
            checkpoint,
            self.validate_checkpoint_index_journal(&slots, checkpoint.generation)
        );
        retain_token_on_error!(
            checkpoint,
            self.validate_checkpoint_rebuild_paths(&slots, &checkpoint, false)
        );
        let mut finalized = 0usize;
        for (index, slot) in slots.iter().enumerate() {
            if slot.checkpoint_generation == checkpoint.generation
                && matches!(slot.state, PrivatePageState::PendingReturn { .. })
            {
                retain_token_on_error!(checkpoint, Self::preflight_pending_return(slot, index));
                finalized = retain_token_on_error!(
                    checkpoint,
                    finalized
                        .checked_add(1)
                        .ok_or(PrivatePagePoolError::EpochExhausted)
                );
            }
        }
        let terminal_steps = retain_token_on_error!(
            checkpoint,
            finalized
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)
        );
        retain_token_on_error!(
            checkpoint,
            u64::try_from(terminal_steps)
                .map_err(|_| PrivatePagePoolError::EpochExhausted)
                .and_then(|steps| self.preflight_epoch_steps(steps))
        );
        let mut applied = 0usize;
        for index in 0..slots.len() {
            let slot = &mut slots[index];
            if slot.checkpoint_generation != checkpoint.generation {
                if slot.saved_index_generation == checkpoint.generation {
                    Self::clear_saved_index_metadata(slot);
                }
                if slot.saved_scope_generation == checkpoint.generation {
                    Self::clear_saved_scope_header_metadata(slot);
                }
                continue;
            }
            if let PrivatePageState::PendingReturn { disposition, .. } = slot.state {
                apply_return(slot, disposition);
                slot.binding_epoch += 1;
                self.advance_epoch_prepared();
                applied += 1;
            }
            Self::clear_checkpoint_metadata(slot, checkpoint.generation);
        }
        self.rebuild_all_index_counts(&mut slots);
        debug_assert_eq!(applied, finalized);
        self.active_checkpoint.set(0);
        self.checkpoint_cleanup_slots.set(0);
        self.checkpoint_index_head.set(NO_SLOT);
        self.checkpoint_index_count.set(0);
        self.advance_epoch_prepared();
        Ok(())
    }

    fn validate_checkpoint_index_journal(
        &self,
        slots: &[PrivatePagePoolSlot],
        generation: u64,
    ) -> Result<(), PrivatePagePoolError> {
        let expected = self.checkpoint_index_count.get();
        let mut journal = self.checkpoint_index_head.get();
        let mut actual = 0usize;
        while journal != NO_SLOT {
            if actual >= expected {
                return Err(PrivatePagePoolError::CheckpointMismatch);
            }
            let page = slots
                .get(journal)
                .ok_or(PrivatePagePoolError::CheckpointMismatch)?;
            if page.saved_index_generation != generation {
                return Err(PrivatePagePoolError::CheckpointMismatch);
            }
            journal = page.saved_index_next;
            actual += 1;
        }
        if actual != expected {
            return Err(PrivatePagePoolError::CheckpointMismatch);
        }
        Ok(())
    }

    fn projected_binding(
        slot: &PrivatePagePoolSlot,
        generation: u64,
        rollback: bool,
    ) -> (u32, Option<PrivatePageAuthorization>, u64, bool, usize) {
        if rollback && slot.checkpoint_generation == generation {
            if let SavedBinding::Binding {
                pgno,
                authorization,
                scope_id,
                scope_anchor,
                scope_anchor_index,
                ..
            } = slot.saved_binding
            {
                return (
                    pgno,
                    authorization,
                    scope_id,
                    scope_anchor,
                    scope_anchor_index,
                );
            }
        }
        (
            slot.pgno,
            slot.authorization,
            slot.scope_id,
            slot.scope_anchor,
            slot.scope_anchor_index,
        )
    }

    fn projected_index_links(
        slot: &PrivatePagePoolSlot,
        generation: u64,
        rollback: bool,
    ) -> (usize, usize, u8) {
        if rollback && slot.saved_index_generation == generation {
            (
                slot.saved_index_left,
                slot.saved_index_right,
                slot.saved_index_height,
            )
        } else {
            (slot.index_left, slot.index_right, slot.index_height)
        }
    }

    fn projected_scope_links(
        slot: &PrivatePagePoolSlot,
        generation: u64,
        rollback: bool,
    ) -> (usize, usize, u8) {
        if rollback && slot.saved_index_generation == generation {
            (
                slot.saved_scope_left,
                slot.saved_scope_right,
                slot.saved_scope_height,
            )
        } else {
            (slot.scope_left, slot.scope_right, slot.scope_height)
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn validate_projected_tree(
        slots: &[PrivatePagePoolSlot],
        root: usize,
        generation: u64,
        rollback: bool,
        scope: Option<(u64, usize)>,
        lower: Option<u32>,
        upper: Option<u32>,
    ) -> Result<(usize, u8), PrivatePagePoolError> {
        if root == NO_SLOT {
            return Ok((0, 0));
        }
        let page = slots
            .get(root)
            .ok_or(PrivatePagePoolError::CheckpointMismatch)?;
        let (pgno, authorization, scope_id, scope_anchor, scope_anchor_index) =
            Self::projected_binding(page, generation, rollback);
        if authorization.is_none()
            || lower.is_some_and(|bound| pgno <= bound)
            || upper.is_some_and(|bound| pgno >= bound)
        {
            return Err(PrivatePagePoolError::CheckpointMismatch);
        }
        if let Some((expected_scope, expected_anchor)) = scope {
            if scope_id != expected_scope
                || scope_anchor_index != expected_anchor
                || scope_anchor != (root == expected_anchor)
            {
                return Err(PrivatePagePoolError::CheckpointMismatch);
            }
        }
        let (left, right, stored_height) = if scope.is_some() {
            Self::projected_scope_links(page, generation, rollback)
        } else {
            Self::projected_index_links(page, generation, rollback)
        };
        if stored_height == 0 {
            return Err(PrivatePagePoolError::CheckpointMismatch);
        }
        for child in [left, right] {
            if child == NO_SLOT {
                continue;
            }
            let child = slots
                .get(child)
                .ok_or(PrivatePagePoolError::CheckpointMismatch)?;
            let (_, _, child_height) = if scope.is_some() {
                Self::projected_scope_links(child, generation, rollback)
            } else {
                Self::projected_index_links(child, generation, rollback)
            };
            if child_height == 0 || child_height >= stored_height {
                return Err(PrivatePagePoolError::CheckpointMismatch);
            }
        }
        let (left_count, left_height) = Self::validate_projected_tree(
            slots,
            left,
            generation,
            rollback,
            scope,
            lower,
            Some(pgno),
        )?;
        let (right_count, right_height) = Self::validate_projected_tree(
            slots,
            right,
            generation,
            rollback,
            scope,
            Some(pgno),
            upper,
        )?;
        let height = left_height
            .max(right_height)
            .checked_add(1)
            .ok_or(PrivatePagePoolError::CheckpointMismatch)?;
        if height != stored_height {
            return Err(PrivatePagePoolError::CheckpointMismatch);
        }
        let count = left_count
            .checked_add(right_count)
            .and_then(|value| value.checked_add(1))
            .ok_or(PrivatePagePoolError::CheckpointMismatch)?;
        Ok((count, height))
    }

    fn validate_checkpoint_rebuild_paths(
        &self,
        slots: &[PrivatePagePoolSlot],
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
        rollback: bool,
    ) -> Result<(), PrivatePagePoolError> {
        let root = if rollback {
            checkpoint.index_root
        } else {
            self.index_root.get()
        };
        let expected = if rollback {
            checkpoint.authorized_len
        } else {
            self.authorized_len.get()
        };
        let (actual, _) = Self::validate_projected_tree(
            slots,
            root,
            checkpoint.generation,
            rollback,
            None,
            None,
            None,
        )?;
        if actual != expected {
            return Err(PrivatePagePoolError::CheckpointMismatch);
        }
        for (anchor, page) in slots.iter().enumerate() {
            let (_, _, scope_id, scope_anchor, scope_anchor_index) =
                Self::projected_binding(page, checkpoint.generation, rollback);
            if !scope_anchor {
                continue;
            }
            if scope_id == 0 || scope_anchor_index != anchor {
                return Err(PrivatePagePoolError::CheckpointMismatch);
            }
            let (scope_root, scope_bound) =
                if rollback && page.saved_scope_generation == checkpoint.generation {
                    (page.saved_scope_root, page.saved_scope_bound)
                } else {
                    (page.scope_root, page.scope_bound)
                };
            let (actual, _) = Self::validate_projected_tree(
                slots,
                scope_root,
                checkpoint.generation,
                rollback,
                Some((scope_id, anchor)),
                None,
                None,
            )?;
            if actual != scope_bound {
                return Err(PrivatePagePoolError::CheckpointMismatch);
            }
        }
        Ok(())
    }

    fn preflight_pending_return(
        slot: &PrivatePagePoolSlot,
        index: usize,
    ) -> Result<(), PrivatePagePoolError> {
        let PrivatePageState::PendingReturn { disposition, .. } = slot.state else {
            return Ok(());
        };
        let authorization = slot
            .authorization
            .ok_or(PrivatePagePoolError::SlotVacant(index))?;
        if disposition == PrivatePageReturn::Tail
            && authorization != PrivatePageAuthorization::Appended
        {
            return Err(PrivatePagePoolError::AuthorizationMismatch {
                pgno: slot.pgno,
                authorization,
            });
        }
        slot.binding_epoch
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        Ok(())
    }

    fn validate_checkpoint_in_scope(
        &self,
        slots: &[PrivatePagePoolSlot],
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<(usize, usize), PrivatePagePoolError> {
        let anchor = self.validate_scope(slots, scope)?;
        let capacity = slots[anchor].scope_capacity;
        let mut member = slots[anchor].scope_member_head;
        let mut touched = 0usize;
        for ordinal in 0..capacity {
            let page = slots.get(member).ok_or(PrivatePagePoolError::StaleScope)?;
            if page.scope_id != scope.id
                || page.scope_anchor_index != anchor
                || page.scope_member_ordinal != ordinal
                || page.scope_anchor != (member == anchor)
            {
                return Err(PrivatePagePoolError::StaleScope);
            }
            match (
                page.checkpoint_generation,
                page.saved_state,
                page.saved_binding,
            ) {
                (0, SavedState::None, SavedBinding::None) => {}
                (generation, SavedState::State(_), SavedBinding::Binding { .. })
                    if generation == checkpoint.generation =>
                {
                    touched += 1;
                }
                _ => return Err(PrivatePagePoolError::CheckpointMismatch),
            }
            if page.saved_scope_generation != 0 {
                return Err(PrivatePagePoolError::CheckpointMismatch);
            }
            member = page.scope_member_next;
        }
        if member != NO_SLOT || touched != self.checkpoint_cleanup_slots.get() {
            return Err(PrivatePagePoolError::CheckpointMismatch);
        }

        self.validate_checkpoint_index_journal(slots, checkpoint.generation)?;
        Ok((capacity, touched))
    }

    fn clear_checkpoint_index_journal_prepared(
        &self,
        slots: &mut [PrivatePagePoolSlot],
        generation: u64,
    ) {
        let mut index = self.checkpoint_index_head.get();
        while index != NO_SLOT {
            let next = slots[index].saved_index_next;
            debug_assert_eq!(slots[index].saved_index_generation, generation);
            Self::clear_saved_index_metadata(&mut slots[index]);
            index = next;
        }
        self.checkpoint_index_head.set(NO_SLOT);
        self.checkpoint_index_count.set(0);
    }

    fn restore_checkpoint_index_journal_prepared(
        &self,
        slots: &mut [PrivatePagePoolSlot],
        generation: u64,
    ) {
        let mut index = self.checkpoint_index_head.get();
        while index != NO_SLOT {
            let next = slots[index].saved_index_next;
            debug_assert_eq!(slots[index].saved_index_generation, generation);
            slots[index].index_left = slots[index].saved_index_left;
            slots[index].index_right = slots[index].saved_index_right;
            slots[index].index_height = slots[index].saved_index_height;
            slots[index].index_available = slots[index].saved_index_available;
            slots[index].index_in_use = slots[index].saved_index_in_use;
            slots[index].index_unscoped_available = slots[index].saved_index_unscoped_available;
            slots[index].scope_left = slots[index].saved_scope_left;
            slots[index].scope_right = slots[index].saved_scope_right;
            slots[index].scope_height = slots[index].saved_scope_height;
            slots[index].scope_available = slots[index].saved_scope_available;
            slots[index].scope_in_use = slots[index].saved_scope_in_use;
            slots[index].scope_count = slots[index].saved_scope_count;
            slots[index].scope_revision = slots[index].saved_scope_revision;
            slots[index].scope_digest = slots[index].saved_scope_digest;
            slots[index].scope_vacant_count = slots[index].saved_scope_vacant_count;
            slots[index].scope_vacant_revision = slots[index].saved_scope_vacant_revision;
            slots[index].scope_vacant_digest = slots[index].saved_scope_vacant_digest;
            Self::clear_saved_index_metadata(&mut slots[index]);
            index = next;
        }
        self.checkpoint_index_head.set(NO_SLOT);
        self.checkpoint_index_count.set(0);
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn commit_checkpoint_in_scope<'checkpoint>(
        &self,
        checkpoint: PrivatePagePoolCheckpoint<'checkpoint>,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<(), (PrivatePagePoolCheckpoint<'checkpoint>, PrivatePagePoolError)> {
        retain_token_on_error!(checkpoint, self.validate_checkpoint(&checkpoint));
        let mut slots = retain_token_on_error!(
            checkpoint,
            self.slots
                .try_borrow_mut()
                .map_err(|_| PrivatePagePoolError::BorrowConflict)
        );
        let (capacity, _) = retain_token_on_error!(
            checkpoint,
            self.validate_checkpoint_in_scope(&slots, &checkpoint, scope)
        );
        let anchor = scope.anchor;
        let mut member = slots[anchor].scope_member_head;
        let mut finalized = 0usize;
        for _ in 0..capacity {
            if slots[member].checkpoint_generation == checkpoint.generation
                && matches!(slots[member].state, PrivatePageState::PendingReturn { .. })
            {
                retain_token_on_error!(
                    checkpoint,
                    Self::preflight_pending_return(&slots[member], member)
                );
                finalized = retain_token_on_error!(
                    checkpoint,
                    finalized
                        .checked_add(1)
                        .ok_or(PrivatePagePoolError::EpochExhausted)
                );
            }
            member = slots[member].scope_member_next;
        }
        let terminal_steps = retain_token_on_error!(
            checkpoint,
            finalized
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)
        );
        retain_token_on_error!(
            checkpoint,
            u64::try_from(terminal_steps)
                .map_err(|_| PrivatePagePoolError::EpochExhausted)
                .and_then(|steps| self.preflight_epoch_steps(steps))
        );

        member = slots[anchor].scope_member_head;
        for _ in 0..capacity {
            let next = slots[member].scope_member_next;
            if slots[member].checkpoint_generation == checkpoint.generation {
                if let PrivatePageState::PendingReturn { disposition, .. } = slots[member].state {
                    apply_return(&mut slots[member], disposition);
                    slots[member].binding_epoch += 1;
                    self.advance_epoch_prepared();
                    self.refresh_slot_counts(&mut slots, member);
                }
                slots[member].checkpoint_generation = 0;
                slots[member].saved_state = SavedState::None;
                slots[member].saved_binding = SavedBinding::None;
            }
            member = next;
        }
        self.clear_checkpoint_index_journal_prepared(&mut slots, checkpoint.generation);
        self.active_checkpoint.set(0);
        self.checkpoint_cleanup_slots.set(0);
        self.advance_epoch_prepared();
        Ok(())
    }

    pub(crate) fn commit_checkpoint_in_scope_prepared(
        &self,
        checkpoint: PrivatePagePoolCheckpoint<'_>,
        scope: &PrivatePageReservationScope<'_>,
    ) {
        let mut slots = self
            .slots
            .try_borrow_mut()
            .expect("prepared scoped checkpoint owns the pool mutation suffix");
        let anchor = scope.anchor;
        let capacity = slots[anchor].scope_capacity;
        let mut member = slots[anchor].scope_member_head;
        for _ in 0..capacity {
            let next = slots[member].scope_member_next;
            if slots[member].checkpoint_generation == checkpoint.generation {
                if let PrivatePageState::PendingReturn { disposition, .. } = slots[member].state {
                    debug_assert!(self.epoch.get() < checkpoint.reserved_end_epoch);
                    apply_return(&mut slots[member], disposition);
                    slots[member].binding_epoch += 1;
                    self.advance_epoch_prepared();
                    self.refresh_slot_counts(&mut slots, member);
                }
                slots[member].checkpoint_generation = 0;
                slots[member].saved_state = SavedState::None;
                slots[member].saved_binding = SavedBinding::None;
            }
            member = next;
        }
        if slots[anchor].saved_scope_generation == checkpoint.generation {
            Self::clear_saved_scope_header_metadata(&mut slots[anchor]);
        }
        self.clear_checkpoint_index_journal_prepared(&mut slots, checkpoint.generation);
        self.active_checkpoint.set(0);
        self.checkpoint_cleanup_slots.set(0);
        debug_assert!(self.epoch.get() < checkpoint.reserved_end_epoch);
        self.advance_epoch_prepared();
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn rollback_checkpoint_in_scope<'checkpoint>(
        &self,
        checkpoint: PrivatePagePoolCheckpoint<'checkpoint>,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<(), (PrivatePagePoolCheckpoint<'checkpoint>, PrivatePagePoolError)> {
        retain_token_on_error!(checkpoint, self.validate_checkpoint(&checkpoint));
        let mut slots = retain_token_on_error!(
            checkpoint,
            self.slots
                .try_borrow_mut()
                .map_err(|_| PrivatePagePoolError::BorrowConflict)
        );
        let (capacity, touched) = retain_token_on_error!(
            checkpoint,
            self.validate_checkpoint_in_scope(&slots, &checkpoint, scope)
        );
        let terminal_steps = retain_token_on_error!(
            checkpoint,
            touched
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)
        );
        retain_token_on_error!(
            checkpoint,
            u64::try_from(terminal_steps)
                .map_err(|_| PrivatePagePoolError::EpochExhausted)
                .and_then(|steps| self.preflight_epoch_steps(steps))
        );
        let anchor = scope.anchor;
        let mut member = slots[anchor].scope_member_head;
        for _ in 0..capacity {
            let next = slots[member].scope_member_next;
            if slots[member].checkpoint_generation == checkpoint.generation {
                let SavedState::State(saved) = slots[member].saved_state else {
                    unreachable!("scoped rollback preflight validated saved state")
                };
                let SavedBinding::Binding {
                    pgno,
                    authorization,
                    scope_id,
                    scope_anchor,
                    scope_anchor_index,
                    scope_vacant_next,
                    allocation_generation,
                    adapter_owner,
                    adapter_tag,
                } = slots[member].saved_binding
                else {
                    unreachable!("scoped rollback preflight validated saved binding")
                };
                if !matches!(saved, PrivatePageState::InUse { .. }) {
                    slots[member].bytes.fill(0);
                }
                slots[member].pgno = pgno;
                slots[member].authorization = authorization;
                slots[member].scope_id = scope_id;
                slots[member].scope_anchor = scope_anchor;
                slots[member].scope_anchor_index = scope_anchor_index;
                slots[member].scope_vacant_next = scope_vacant_next;
                slots[member].allocation_generation = allocation_generation;
                slots[member].adapter_owner = adapter_owner;
                slots[member].adapter_tag = adapter_tag;
                slots[member].state = saved;
                slots[member].binding_epoch += 1;
                slots[member].checkpoint_generation = 0;
                slots[member].saved_state = SavedState::None;
                slots[member].saved_binding = SavedBinding::None;
                self.advance_epoch_prepared();
            }
            member = next;
        }
        self.restore_checkpoint_index_journal_prepared(&mut slots, checkpoint.generation);
        self.index_root.set(checkpoint.index_root);
        self.authorized_len.set(checkpoint.authorized_len);
        self.available_count.set(checkpoint.available_count);
        self.lowest_available.set(checkpoint.lowest_available);
        self.pending_page_count.set(checkpoint.pending_page_count);
        self.active_checkpoint.set(0);
        self.checkpoint_cleanup_slots.set(0);
        self.advance_epoch_prepared();
        Ok(())
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn rollback_checkpoint<'checkpoint>(
        &self,
        checkpoint: PrivatePagePoolCheckpoint<'checkpoint>,
    ) -> Result<(), (PrivatePagePoolCheckpoint<'checkpoint>, PrivatePagePoolError)> {
        retain_token_on_error!(checkpoint, self.validate_checkpoint(&checkpoint));
        let mut slots = retain_token_on_error!(
            checkpoint,
            self.slots
                .try_borrow_mut()
                .map_err(|_| PrivatePagePoolError::BorrowConflict)
        );
        let mut touched = 0usize;
        for (index, slot) in slots.iter().enumerate() {
            match (
                slot.checkpoint_generation,
                slot.saved_state,
                slot.saved_binding,
            ) {
                (0, SavedState::None, SavedBinding::None) => {}
                (generation, SavedState::State(_), SavedBinding::Binding { .. })
                    if generation == checkpoint.generation =>
                {
                    retain_token_on_error!(
                        checkpoint,
                        slot.binding_epoch
                            .checked_add(1)
                            .ok_or(PrivatePagePoolError::EpochExhausted)
                    );
                    touched = retain_token_on_error!(
                        checkpoint,
                        touched
                            .checked_add(1)
                            .ok_or(PrivatePagePoolError::EpochExhausted)
                    );
                }
                _ => {
                    let _ = index;
                    return Err((checkpoint, PrivatePagePoolError::CheckpointMismatch));
                }
            }
            if slot.saved_index_generation != 0
                && slot.saved_index_generation != checkpoint.generation
            {
                return Err((checkpoint, PrivatePagePoolError::CheckpointMismatch));
            }
            if slot.saved_scope_generation != 0
                && slot.saved_scope_generation != checkpoint.generation
            {
                return Err((checkpoint, PrivatePagePoolError::CheckpointMismatch));
            }
        }
        if touched != self.checkpoint_cleanup_slots.get() {
            return Err((checkpoint, PrivatePagePoolError::CheckpointMismatch));
        }
        retain_token_on_error!(
            checkpoint,
            self.validate_checkpoint_index_journal(&slots, checkpoint.generation)
        );
        retain_token_on_error!(
            checkpoint,
            self.validate_checkpoint_rebuild_paths(&slots, &checkpoint, true)
        );
        let terminal_steps = retain_token_on_error!(
            checkpoint,
            touched
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)
        );
        retain_token_on_error!(
            checkpoint,
            u64::try_from(terminal_steps)
                .map_err(|_| PrivatePagePoolError::EpochExhausted)
                .and_then(|steps| self.preflight_epoch_steps(steps))
        );
        for slot in slots.iter_mut() {
            if slot.checkpoint_generation != checkpoint.generation {
                continue;
            }
            let SavedState::State(saved) = slot.saved_state else {
                unreachable!("rollback preflight validated every checkpoint slot");
            };
            let SavedBinding::Binding {
                pgno,
                authorization,
                scope_id,
                scope_anchor,
                scope_anchor_index,
                scope_vacant_next,
                allocation_generation,
                adapter_owner,
                adapter_tag,
            } = slot.saved_binding
            else {
                unreachable!("rollback preflight validated every saved binding")
            };
            if !matches!(saved, PrivatePageState::InUse { .. }) {
                slot.bytes.fill(0);
            }
            slot.pgno = pgno;
            slot.authorization = authorization;
            slot.scope_id = scope_id;
            slot.scope_anchor = scope_anchor;
            slot.scope_anchor_index = scope_anchor_index;
            slot.scope_vacant_next = scope_vacant_next;
            slot.allocation_generation = allocation_generation;
            slot.adapter_owner = adapter_owner;
            slot.adapter_tag = adapter_tag;
            slot.state = saved;
            slot.binding_epoch += 1;
            slot.checkpoint_generation = 0;
            slot.saved_state = SavedState::None;
            slot.saved_binding = SavedBinding::None;
            self.advance_epoch_prepared();
        }
        for slot in slots.iter_mut() {
            if slot.saved_index_generation == checkpoint.generation {
                slot.index_left = slot.saved_index_left;
                slot.index_right = slot.saved_index_right;
                slot.index_height = slot.saved_index_height;
                slot.index_available = slot.saved_index_available;
                slot.index_in_use = slot.saved_index_in_use;
                slot.index_unscoped_available = slot.saved_index_unscoped_available;
                slot.scope_left = slot.saved_scope_left;
                slot.scope_right = slot.saved_scope_right;
                slot.scope_height = slot.saved_scope_height;
                slot.scope_available = slot.saved_scope_available;
                slot.scope_in_use = slot.saved_scope_in_use;
                slot.scope_count = slot.saved_scope_count;
                slot.scope_revision = slot.saved_scope_revision;
                slot.scope_digest = slot.saved_scope_digest;
                slot.scope_vacant_count = slot.saved_scope_vacant_count;
                slot.scope_vacant_revision = slot.saved_scope_vacant_revision;
                slot.scope_vacant_digest = slot.saved_scope_vacant_digest;
                Self::clear_saved_index_metadata(slot);
            }
            if slot.saved_scope_generation == checkpoint.generation {
                slot.scope_root = slot.saved_scope_root;
                slot.scope_vacant_head = slot.saved_scope_vacant_head;
                slot.scope_bound = slot.saved_scope_bound;
                Self::clear_saved_scope_header_metadata(slot);
            }
        }
        self.index_root.set(checkpoint.index_root);
        self.authorized_len.set(checkpoint.authorized_len);
        self.available_count.set(checkpoint.available_count);
        self.lowest_available.set(checkpoint.lowest_available);
        self.pending_page_count.set(checkpoint.pending_page_count);
        self.rebuild_all_index_counts(&mut slots);
        self.active_checkpoint.set(0);
        self.checkpoint_cleanup_slots.set(0);
        self.checkpoint_index_head.set(NO_SLOT);
        self.checkpoint_index_count.set(0);
        self.advance_epoch_prepared();
        Ok(())
    }

    fn require_mutation_idle(&self) -> Result<(), PrivatePagePoolError> {
        if self.abort_required.get() {
            return Err(PrivatePagePoolError::AbortRequired);
        }
        if self.active_operation_id.get() != 0 {
            return Err(PrivatePagePoolError::OperationActive);
        }
        Ok(())
    }

    fn preflight_epoch_steps_raw(&self, steps: u64) -> Result<u64, PrivatePagePoolError> {
        let end = self
            .epoch
            .get()
            .checked_add(steps)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        end.checked_add(self.abort_epoch_reserve)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        Ok(end)
    }

    fn preflight_epoch_steps(&self, steps: u64) -> Result<u64, PrivatePagePoolError> {
        self.require_mutation_idle()?;
        self.preflight_epoch_steps_raw(steps)
    }

    fn next_epoch(&self) -> Result<u64, PrivatePagePoolError> {
        self.preflight_epoch_steps(1)
    }

    /// Consume and scrub one transaction-owned draft.
    ///
    /// All fallible checks, including borrowability and the complete slot walk,
    /// finish before the first write. A failure therefore returns the unchanged
    /// pool with every outstanding capability still referring to that pool.
    #[allow(clippy::result_large_err)]
    pub(crate) fn discard_transaction_draft(
        mut self,
    ) -> Result<(&'slots mut [PrivatePagePoolSlot], usize), (Self, PrivatePagePoolError)> {
        if let Err(error) = self.preflight_transaction_discard() {
            return Err((self, error));
        }
        self.epoch.set(self.epoch.get() + self.abort_epoch_reserve);
        self.identity = self.invalidation_identity;
        self.identity_epoch = self.invalidation_identity;
        self.abort_epoch_reserve = 0;
        let slots = self.slots.into_inner();
        for slot in slots.iter_mut() {
            *slot = PrivatePagePoolSlot::empty();
        }
        Ok((slots, self.slot_count))
    }

    fn preflight_transaction_discard(&self) -> Result<(), PrivatePagePoolError> {
        let expected_invalidation = match self.identity.checked_add(1) {
            Some(identity) => identity,
            None => return Err(PrivatePagePoolError::PoolIdentityExhausted),
        };
        let expected_reserve = match u64::try_from(self.slot_count) {
            Ok(reserve) => reserve,
            Err(_) => return Err(PrivatePagePoolError::EpochExhausted),
        };
        if self.invalidation_identity != expected_invalidation
            || self.abort_epoch_reserve != expected_reserve
        {
            return Err(PrivatePagePoolError::InvalidState(NO_SLOT));
        }
        if self
            .epoch
            .get()
            .checked_add(self.abort_epoch_reserve)
            .is_none()
        {
            return Err(PrivatePagePoolError::EpochExhausted);
        }

        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let mut visited = 0usize;
        for _ in slots.iter() {
            visited = visited
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
        }
        if visited != self.slot_count {
            return Err(PrivatePagePoolError::InvalidState(visited));
        }
        Ok(())
    }

    #[cfg(test)]
    fn claim_probe_count(&self) -> usize {
        self.claim_probe_count.get()
    }

    #[cfg(test)]
    pub(crate) fn test_reset_scope_lookup_probes(&self) {
        self.scope_lookup_probes.set(0);
    }

    #[cfg(test)]
    pub(crate) fn test_scope_lookup_probes(&self) -> usize {
        self.scope_lookup_probes.get()
    }

    #[cfg(test)]
    pub(crate) fn test_scope_layout_visits(&self) -> usize {
        self.scope_layout_visits.get()
    }

    #[cfg(test)]
    pub(crate) fn test_reset_commitment_work(&self) {
        PRIVATE_PAGE_COMMITMENT_WORK.with(|work| work.set(0));
    }

    #[cfg(test)]
    pub(crate) fn test_commitment_work(&self) -> usize {
        PRIVATE_PAGE_COMMITMENT_WORK.with(Cell::get)
    }

    #[cfg(test)]
    pub(crate) fn test_scope_lifecycle_visits(&self) -> usize {
        self.scope_layout_visits
            .get()
            .saturating_add(self.scope_lifecycle_visits.get())
    }

    #[cfg(test)]
    pub(crate) fn test_unscoped_vacant_count(&self) -> usize {
        self.unscoped_vacant_count.get()
    }

    #[cfg(test)]
    pub(crate) fn test_set_unscoped_vacant_count(&self, count: usize) {
        self.unscoped_vacant_count.set(count);
    }

    #[cfg(test)]
    pub(crate) fn test_set_unscoped_vacant_head(&self, head: usize) {
        self.unscoped_vacant_head.set(head);
    }

    #[cfg(test)]
    pub(crate) fn test_set_unscoped_vacant_tail(&self, tail: usize) {
        self.unscoped_vacant_tail.set(tail);
    }

    #[cfg(test)]
    pub(crate) fn test_set_unscoped_vacancy_links(
        &self,
        slot: usize,
        previous: usize,
        next: usize,
    ) {
        let mut slots = self.slots.borrow_mut();
        slots[slot].unscoped_vacant_prev = previous;
        slots[slot].unscoped_vacant_next = next;
    }

    #[cfg(test)]
    pub(crate) fn test_corrupt_scoped_vacant_payload(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        corruption: PrivatePageVacantPayloadCorruption,
    ) -> (usize, PrivatePagePoolSlot) {
        let mut slots = self.slots.borrow_mut();
        let anchor = self.validate_scope(&slots, scope).unwrap();
        let capacity = slots[anchor].scope_capacity;
        let mut member = slots[anchor].scope_member_head;
        for _ in 0..capacity {
            if slots[member].authorization.is_none() {
                let original = slots[member].clone();
                let slot = &mut slots[member];
                match corruption {
                    PrivatePageVacantPayloadCorruption::PageNumber => slot.pgno = 7,
                    PrivatePageVacantPayloadCorruption::Authorization => {
                        slot.authorization = Some(PrivatePageAuthorization::CommittedFree);
                    }
                    PrivatePageVacantPayloadCorruption::State => {
                        slot.state = PrivatePageState::Available;
                    }
                    PrivatePageVacantPayloadCorruption::AllocationGeneration => {
                        slot.allocation_generation = 1;
                    }
                    PrivatePageVacantPayloadCorruption::CheckpointGeneration => {
                        slot.checkpoint_generation = 1;
                    }
                    PrivatePageVacantPayloadCorruption::SavedState => {
                        slot.saved_state = SavedState::State(PrivatePageState::Vacant);
                    }
                    PrivatePageVacantPayloadCorruption::AdapterOwner => {
                        slot.adapter_owner = Some(PrivatePageOwner::Bitmap);
                    }
                    PrivatePageVacantPayloadCorruption::AdapterTag => slot.adapter_tag = 1,
                    PrivatePageVacantPayloadCorruption::Bytes => slot.bytes[0] = 1,
                    PrivatePageVacantPayloadCorruption::IndexLeft => slot.index_left = member,
                    PrivatePageVacantPayloadCorruption::IndexRight => slot.index_right = member,
                    PrivatePageVacantPayloadCorruption::IndexHeight => slot.index_height = 1,
                    PrivatePageVacantPayloadCorruption::IndexAvailable => {
                        slot.index_available = 1;
                    }
                    PrivatePageVacantPayloadCorruption::IndexInUse => slot.index_in_use = 1,
                    PrivatePageVacantPayloadCorruption::IndexUnscopedAvailable => {
                        slot.index_unscoped_available = 1;
                    }
                    PrivatePageVacantPayloadCorruption::ScopeLeft => slot.scope_left = member,
                    PrivatePageVacantPayloadCorruption::ScopeRight => slot.scope_right = member,
                    PrivatePageVacantPayloadCorruption::ScopeHeight => slot.scope_height = 1,
                    PrivatePageVacantPayloadCorruption::ScopeAvailable => {
                        slot.scope_available = 1;
                    }
                    PrivatePageVacantPayloadCorruption::ScopeInUse => slot.scope_in_use = 1,
                    PrivatePageVacantPayloadCorruption::ValidationMarker => {
                        slot.scope_validation_marker = 1;
                    }
                    PrivatePageVacantPayloadCorruption::SavedBinding => {
                        slot.saved_binding = SavedBinding::Binding {
                            pgno: 0,
                            authorization: None,
                            scope_id: scope.id,
                            scope_anchor: member == anchor,
                            scope_anchor_index: anchor,
                            scope_vacant_next: NO_SLOT,
                            allocation_generation: 0,
                            adapter_owner: None,
                            adapter_tag: 0,
                        };
                    }
                    PrivatePageVacantPayloadCorruption::SavedIndexGeneration => {
                        slot.saved_index_generation = 1;
                    }
                    PrivatePageVacantPayloadCorruption::SavedIndexNext => {
                        slot.saved_index_next = member;
                    }
                    PrivatePageVacantPayloadCorruption::SavedScopeGeneration => {
                        slot.saved_scope_generation = 1;
                    }
                }
                return (member, original);
            }
            member = slots[member].scope_member_next;
        }
        panic!("test requires an already-unbound scoped member");
    }

    #[cfg(test)]
    pub(crate) fn test_corrupt_bound_validation_marker(
        &self,
        scope: &PrivatePageReservationScope<'_>,
    ) -> (usize, PrivatePagePoolSlot) {
        let mut slots = self.slots.borrow_mut();
        let anchor = self.validate_scope(&slots, scope).unwrap();
        let capacity = slots[anchor].scope_capacity;
        let mut member = slots[anchor].scope_member_head;
        for _ in 0..capacity {
            if slots[member].authorization.is_some() {
                let original = slots[member].clone();
                slots[member].scope_validation_marker = 1;
                return (member, original);
            }
            member = slots[member].scope_member_next;
        }
        panic!("test requires a bound scoped member");
    }

    #[cfg(test)]
    pub(crate) fn test_restore_slot(&self, slot: usize, original: PrivatePagePoolSlot) {
        self.slots.borrow_mut()[slot] = original;
    }

    #[cfg(test)]
    pub(crate) fn test_mutation_snapshot(&self) -> PrivatePagePoolTestSnapshot {
        PrivatePagePoolTestSnapshot {
            slots: self.slots.borrow().to_vec(),
            index_root: self.index_root.get(),
            authorized_len: self.authorized_len.get(),
            available_count: self.available_count.get(),
            lowest_available: self.lowest_available.get(),
            pending_page_count: self.pending_page_count.get(),
            identity: self.identity,
            identity_epoch: self.identity_epoch,
            invalidation_identity: self.invalidation_identity,
            abort_epoch_reserve: self.abort_epoch_reserve,
            generation: self.generation.get(),
            epoch: self.epoch.get(),
            active_checkpoint: self.active_checkpoint.get(),
            operation_sequence: self.operation_sequence.get(),
            active_operation_id: self.active_operation_id.get(),
            operation_start_epoch: self.operation_start_epoch.get(),
            abort_required: self.abort_required.get(),
            checkpoint_cleanup_slots: self.checkpoint_cleanup_slots.get(),
            checkpoint_index_head: self.checkpoint_index_head.get(),
            checkpoint_index_count: self.checkpoint_index_count.get(),
            coordinator_session_identity: self.coordinator_session_identity.get(),
            coordinator_session_generation: self.coordinator_session_generation.get(),
            coordinator_work_identity: self.coordinator_work_identity.get(),
            coordinator_work_generation: self.coordinator_work_generation.get(),
            coordinator_work_phase: self.coordinator_work_phase.get(),
            coordinator_work_start_epoch: self.coordinator_work_start_epoch.get(),
            coordinator_mutation_started: self.coordinator_mutation_started.get(),
            coordinator_scope_id: self.coordinator_scope_id.get(),
            coordinator_unaccepted_scopes: self.coordinator_unaccepted_scopes.get(),
            coordinator_cleanup_pending: self.coordinator_cleanup_pending.get(),
            sealed_coordinator_cleanup_scope_id: self.sealed_coordinator_cleanup_scope_id.get(),
            sealed_coordinator_cleanup_nonce: self.sealed_coordinator_cleanup_nonce.get(),
            scope_sequence: self.scope_sequence.get(),
            active_scopes: self.active_scopes.get(),
            unscoped_vacant_count: self.unscoped_vacant_count.get(),
            unscoped_vacant_head: self.unscoped_vacant_head.get(),
            unscoped_vacant_tail: self.unscoped_vacant_tail.get(),
        }
    }

    #[cfg(test)]
    pub(crate) fn test_corrupt_unrelated_unscoped_search_child(&self) {
        let mut slots = self.slots.borrow_mut();
        let mut rightmost = self.index_root.get();
        while slots[rightmost].index_right != NO_SLOT {
            rightmost = slots[rightmost].index_right;
        }
        assert_eq!(slots[rightmost].scope_id, 0);
        slots[rightmost].index_right = slots.len();
    }

    #[cfg(test)]
    pub(crate) fn test_hold_slots_borrow(&self) -> Ref<'_, &'slots mut [PrivatePagePoolSlot]> {
        self.slots.borrow()
    }

    #[cfg(test)]
    pub(crate) fn test_corrupt_operation_identity(operation: &mut PrivatePageScopedOperation<'_>) {
        operation.pool_identity = operation.pool_identity.wrapping_add(1);
    }

    #[cfg(test)]
    pub(crate) fn test_duplicate_operation<'plan>(
        operation: &PrivatePageScopedOperation<'plan>,
    ) -> PrivatePageScopedOperation<'plan> {
        PrivatePageScopedOperation {
            pool_identity: operation.pool_identity,
            pool_epoch: operation.pool_epoch,
            id: operation.id,
            pending_txn: operation.pending_txn,
            generation: operation.generation,
            scope_id: operation.scope_id,
            scope_anchor: operation.scope_anchor,
            start_epoch: operation.start_epoch,
            mutation_steps: operation.mutation_steps,
            used_mutation_steps: Cell::new(operation.used_mutation_steps.get()),
            slots: operation.slots,
        }
    }

    #[cfg(test)]
    fn test_hold_slots_borrow_mut(&self) -> RefMut<'_, &'slots mut [PrivatePagePoolSlot]> {
        self.slots.borrow_mut()
    }

    #[cfg(test)]
    pub(crate) fn test_set_scope_member_head(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        head: usize,
    ) {
        self.slots.borrow_mut()[scope.anchor].scope_member_head = head;
    }

    #[cfg(test)]
    pub(crate) fn test_set_scope_sealed(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        sealed: bool,
    ) {
        self.slots.borrow_mut()[scope.anchor].scope_sealed = sealed;
    }

    #[cfg(test)]
    pub(crate) fn test_set_scope_member_next(&self, slot: usize, next: usize) {
        self.slots.borrow_mut()[slot].scope_member_next = next;
    }

    #[cfg(test)]
    pub(crate) fn test_set_scope_member_identity(&self, slot: usize, scope_id: u64, anchor: usize) {
        let mut slots = self.slots.borrow_mut();
        slots[slot].scope_id = scope_id;
        slots[slot].scope_anchor_index = anchor;
    }

    #[cfg(test)]
    pub(crate) const fn test_scope_identity(
        scope: &PrivatePageReservationScope<'_>,
    ) -> (u64, usize) {
        (scope.id, scope.anchor)
    }

    #[cfg(test)]
    pub(crate) fn test_bytes(&self, slot: usize) -> Result<[u8; PAGE_SIZE], PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        Ok(slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?
            .bytes)
    }

    #[cfg(test)]
    pub(crate) fn test_set_epoch(&self, epoch: u64) {
        self.epoch.set(epoch);
    }

    #[cfg(test)]
    pub(crate) fn test_set_binding_epoch(&self, slot: usize, epoch: u64) {
        self.slots.borrow_mut()[slot].binding_epoch = epoch;
    }

    #[cfg(test)]
    fn test_clear_saved_state(&self, slot: usize) {
        self.slots.borrow_mut()[slot].saved_state = SavedState::None;
    }

    fn current_allocation_generation(&self) -> u64 {
        let checkpoint = self.active_checkpoint.get();
        if checkpoint == 0 {
            self.generation.get()
        } else {
            checkpoint
        }
    }

    fn save_for_checkpoint(&self, slot: &mut PrivatePagePoolSlot) {
        let checkpoint = self.active_checkpoint.get();
        if checkpoint != 0 && slot.checkpoint_generation == 0 {
            slot.checkpoint_generation = checkpoint;
            slot.saved_state = SavedState::State(slot.state);
            slot.saved_binding = SavedBinding::Binding {
                pgno: slot.pgno,
                authorization: slot.authorization,
                scope_id: slot.scope_id,
                scope_anchor: slot.scope_anchor,
                scope_anchor_index: slot.scope_anchor_index,
                scope_vacant_next: slot.scope_vacant_next,
                allocation_generation: slot.allocation_generation,
                adapter_owner: slot.adapter_owner,
                adapter_tag: slot.adapter_tag,
            };
            self.checkpoint_cleanup_slots
                .set(self.checkpoint_cleanup_slots.get() + 1);
        }
    }

    fn clear_checkpoint_metadata(slot: &mut PrivatePagePoolSlot, generation: u64) {
        if slot.checkpoint_generation == generation {
            slot.checkpoint_generation = 0;
            slot.saved_state = SavedState::None;
            slot.saved_binding = SavedBinding::None;
        }
        if slot.saved_index_generation == generation {
            Self::clear_saved_index_metadata(slot);
        }
        if slot.saved_scope_generation == generation {
            Self::clear_saved_scope_header_metadata(slot);
        }
    }

    fn clear_saved_index_metadata(slot: &mut PrivatePagePoolSlot) {
        slot.saved_index_generation = 0;
        slot.saved_index_next = NO_SLOT;
        slot.saved_index_left = NO_SLOT;
        slot.saved_index_right = NO_SLOT;
        slot.saved_index_height = 0;
        slot.saved_index_available = 0;
        slot.saved_index_in_use = 0;
        slot.saved_index_unscoped_available = 0;
        slot.saved_scope_left = NO_SLOT;
        slot.saved_scope_right = NO_SLOT;
        slot.saved_scope_height = 0;
        slot.saved_scope_available = 0;
        slot.saved_scope_in_use = 0;
        slot.saved_scope_count = 0;
        slot.saved_scope_revision = 0;
        slot.saved_scope_digest = 0;
        slot.saved_scope_vacant_count = 0;
        slot.saved_scope_vacant_revision = 0;
        slot.saved_scope_vacant_digest = 0;
    }

    fn clear_saved_scope_header_metadata(slot: &mut PrivatePagePoolSlot) {
        slot.saved_scope_generation = 0;
        slot.saved_scope_root = NO_SLOT;
        slot.saved_scope_vacant_head = NO_SLOT;
        slot.saved_scope_bound = 0;
    }

    fn make_authority<'pool>(
        &'pool self,
        slot: usize,
        page: &PrivatePagePoolSlot,
    ) -> PrivatePageAuthority<'pool> {
        let PrivatePageState::InUse {
            owner,
            owner_generation,
            authority_epoch,
            ..
        } = page.state
        else {
            unreachable!("authority is issued only for an in-use page")
        };
        PrivatePageAuthority {
            pool_identity: self.identity,
            pool_epoch: self.identity_epoch,
            slot,
            pgno: page.pgno,
            owner,
            owner_generation,
            authority_epoch,
            binding_epoch: page.binding_epoch,
            scope_id: page.scope_id,
            _pool: PhantomData,
        }
    }

    fn validate_authority(
        &self,
        slots: &[PrivatePagePoolSlot],
        authority: &PrivatePageAuthority<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        if authority.pool_identity != self.identity || authority.pool_epoch != self.identity_epoch {
            return Err(PrivatePagePoolError::PoolMismatch);
        }
        let slot = slots
            .get(authority.slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(authority.slot))?;
        if slot.pgno != authority.pgno
            || slot.binding_epoch != authority.binding_epoch
            || slot.scope_id != authority.scope_id
        {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        match slot.state {
            PrivatePageState::InUse {
                owner,
                owner_generation,
                authority_epoch,
                ..
            } if owner == authority.owner
                && owner_generation == authority.owner_generation
                && authority_epoch == authority.authority_epoch =>
            {
                Ok(())
            }
            PrivatePageState::InUse { owner: actual, .. }
            | PrivatePageState::PendingReturn { owner: actual, .. }
                if actual != authority.owner =>
            {
                Err(PrivatePagePoolError::OwnerMismatch {
                    pgno: authority.pgno,
                    expected: authority.owner,
                    actual,
                })
            }
            _ => Err(PrivatePagePoolError::StaleAuthority),
        }
    }

    fn validate_checkpoint(
        &self,
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
    ) -> Result<(), PrivatePagePoolError> {
        if checkpoint.pool_identity != self.identity || checkpoint.pool_epoch != self.identity_epoch
        {
            return Err(PrivatePagePoolError::PoolMismatch);
        }
        let active = self.active_checkpoint.get();
        if active == 0 {
            return Err(PrivatePagePoolError::CheckpointMissing);
        }
        if active != checkpoint.generation {
            return Err(PrivatePagePoolError::CheckpointMismatch);
        }
        Ok(())
    }
}

fn pool_hash_u64(mut hash: u64, value: u64) -> u64 {
    const PRIME: u64 = 1_099_511_628_211;
    for byte in value.to_le_bytes() {
        hash ^= u64::from(byte);
        hash = hash.wrapping_mul(PRIME);
    }
    hash
}

fn hash_private_page_pool_slot(mut hash: u64, index: usize, slot: &PrivatePagePoolSlot) -> u64 {
    hash = pool_hash_u64(hash, index as u64);
    let (state, owner, owner_generation, tag, authority_epoch, disposition) = match slot.state {
        PrivatePageState::Vacant => (0, 0, 0, 0, 0, 0),
        PrivatePageState::Available => (1, 0, 0, 0, 0, 0),
        PrivatePageState::InUse {
            owner,
            owner_generation,
            tag,
            authority_epoch,
        } => (
            2,
            owner as u64 + 1,
            owner_generation,
            tag,
            authority_epoch,
            0,
        ),
        PrivatePageState::PendingReturn {
            owner,
            owner_generation,
            tag,
            authority_epoch,
            disposition,
        } => (
            3,
            owner as u64 + 1,
            owner_generation,
            tag,
            authority_epoch,
            disposition as u64 + 1,
        ),
        PrivatePageState::ReturnedFree => (4, 0, 0, 0, 0, 0),
        PrivatePageState::ReturnedTail => (5, 0, 0, 0, 0, 0),
    };
    let authorization = slot.authorization.map_or(0, |value| value as u64 + 1);
    let adapter_owner = slot.adapter_owner.map_or(0, |value| value as u64 + 1);
    for value in [
        u64::from(slot.pgno),
        authorization,
        state,
        owner,
        owner_generation,
        tag,
        authority_epoch,
        disposition,
        slot.allocation_generation,
        slot.checkpoint_generation,
        adapter_owner,
        slot.adapter_tag,
        slot.binding_epoch,
        slot.scope_id,
        slot.scope_anchor as u64,
        slot.scope_anchor_index as u64,
        slot.scope_member_next as u64,
        slot.scope_member_head as u64,
        slot.scope_member_ordinal as u64,
        slot.scope_validation_marker as u64,
        slot.scope_vacant_next as u64,
        slot.scope_root as u64,
        slot.scope_vacant_head as u64,
        slot.scope_capacity as u64,
        slot.scope_bound as u64,
        slot.index_left as u64,
        slot.index_right as u64,
        u64::from(slot.index_height),
        slot.index_available as u64,
        slot.index_in_use as u64,
        slot.index_unscoped_available as u64,
        slot.scope_left as u64,
        slot.scope_right as u64,
        u64::from(slot.scope_height),
        slot.scope_available as u64,
        slot.scope_in_use as u64,
        slot.unscoped_vacant_prev as u64,
        slot.unscoped_vacant_next as u64,
        slot.saved_index_generation,
        slot.saved_index_next as u64,
        slot.saved_scope_generation,
    ] {
        hash = pool_hash_u64(hash, value);
    }
    for byte in slot.bytes {
        hash ^= u64::from(byte);
        hash = hash.wrapping_mul(1_099_511_628_211);
    }
    hash
}

fn hash_private_page_scope_tree(
    slots: &[PrivatePagePoolSlot],
    root: usize,
    scope_id: u64,
    anchor: usize,
    remaining: usize,
    mut hash: u64,
) -> Option<(u64, usize)> {
    if root == NO_SLOT {
        return Some((hash, 0));
    }
    if remaining == 0 || root >= slots.len() {
        return None;
    }
    let slot = &slots[root];
    if slot.scope_id != scope_id || slot.scope_anchor_index != anchor {
        return None;
    }
    let (next, left_count) = hash_private_page_scope_tree(
        slots,
        slot.scope_left,
        scope_id,
        anchor,
        remaining - 1,
        hash,
    )?;
    hash = hash_private_page_pool_slot(next, root, slot);
    let available = remaining.checked_sub(left_count + 1)?;
    let (hash, right_count) =
        hash_private_page_scope_tree(slots, slot.scope_right, scope_id, anchor, available, hash)?;
    Some((hash, left_count + 1 + right_count))
}

fn private_page_scope_fingerprint(
    pool: &PrivatePagePool<'_>,
    slots: &[PrivatePagePoolSlot],
    anchor: usize,
    scope_id: u64,
) -> Option<u64> {
    private_page_scope_fingerprint_at_epoch(pool, slots, anchor, scope_id, pool.epoch.get())
}

fn private_page_scope_fingerprint_at_epoch(
    pool: &PrivatePagePool<'_>,
    slots: &[PrivatePagePoolSlot],
    anchor: usize,
    scope_id: u64,
    epoch: u64,
) -> Option<u64> {
    let mut hash = 1_469_598_103_934_665_603u64;
    for value in [
        pool.slot_count as u64,
        pool.authorized_len.get() as u64,
        pool.available_count.get() as u64,
        pool.lowest_available.get() as u64,
        pool.committed_page_count,
        pool.pending_page_count.get(),
        pool.pending_txn,
        pool.identity as u64,
        pool.identity_epoch as u64,
        pool.generation.get(),
        epoch,
        pool.active_checkpoint.get(),
        pool.checkpoint_cleanup_slots.get() as u64,
        pool.checkpoint_index_head.get() as u64,
        pool.checkpoint_index_count.get() as u64,
        pool.scope_sequence.get(),
        pool.active_scopes.get() as u64,
        pool.unscoped_vacant_count.get() as u64,
        pool.unscoped_vacant_head.get() as u64,
        pool.unscoped_vacant_tail.get() as u64,
        pool.index_root.get() as u64,
    ] {
        hash = pool_hash_u64(hash, value);
    }
    let scope = slots.get(anchor)?;
    #[cfg(test)]
    PRIVATE_PAGE_COMMITMENT_WORK.with(|work| {
        work.set(work.get().saturating_add(scope.scope_capacity));
    });
    for value in [
        scope_id,
        anchor as u64,
        scope.scope_member_head as u64,
        scope.scope_root as u64,
        scope.scope_vacant_head as u64,
        scope.scope_capacity as u64,
        scope.scope_bound as u64,
    ] {
        hash = pool_hash_u64(hash, value);
    }
    let (mut hash, bound) = hash_private_page_scope_tree(
        slots,
        scope.scope_root,
        scope_id,
        anchor,
        scope.scope_capacity,
        hash,
    )?;
    if bound != scope.scope_bound {
        return None;
    }
    let mut vacant = scope.scope_vacant_head;
    let mut vacant_count = 0usize;
    while vacant != NO_SLOT {
        if vacant_count >= scope.scope_capacity || vacant >= slots.len() {
            return None;
        }
        let slot = &slots[vacant];
        if slot.scope_id != scope_id
            || slot.scope_anchor_index != anchor
            || slot.authorization.is_some()
        {
            return None;
        }
        hash = hash_private_page_pool_slot(hash, vacant, slot);
        vacant = slot.scope_vacant_next;
        vacant_count += 1;
    }
    (bound + vacant_count == scope.scope_capacity).then_some(hash)
}

fn validate_pool_bounds(
    committed_page_count: u64,
    pending_page_count: u64,
    pending_txn: u64,
) -> Result<(), PrivatePagePoolError> {
    if !(2..=MAX_PAGE_COUNT).contains(&committed_page_count)
        || pending_page_count < committed_page_count
        || pending_page_count > MAX_PAGE_COUNT
    {
        return Err(PrivatePagePoolError::PageCountOutOfRange {
            committed: committed_page_count,
            pending: pending_page_count,
        });
    }
    if pending_txn <= 1 {
        return Err(PrivatePagePoolError::PendingTransactionOutOfRange(
            pending_txn,
        ));
    }
    Ok(())
}

fn validate_authorization(
    pgno: u32,
    authorization: PrivatePageAuthorization,
    committed_page_count: u64,
    pending_page_count: u64,
) -> Result<(), PrivatePagePoolError> {
    if pgno < 2 || u64::from(pgno) >= pending_page_count {
        return Err(PrivatePagePoolError::PageOutOfBounds(pgno));
    }
    let committed = u64::from(pgno) < committed_page_count;
    match authorization {
        PrivatePageAuthorization::CommittedFree | PrivatePageAuthorization::SafelyReclaimed
            if committed =>
        {
            Ok(())
        }
        PrivatePageAuthorization::Appended if !committed => Ok(()),
        _ => Err(PrivatePagePoolError::AuthorizationMismatch {
            pgno,
            authorization,
        }),
    }
}

fn state_tag(state: PrivatePageState) -> u64 {
    match state {
        PrivatePageState::InUse { tag, .. } | PrivatePageState::PendingReturn { tag, .. } => tag,
        _ => 0,
    }
}

fn valid_terminal_owner_tag(owner: PrivatePageOwner, tag: u64) -> bool {
    match owner {
        PrivatePageOwner::Bitmap => true,
        PrivatePageOwner::Range => matches!(tag, 4 | 6),
        // Normalization pages are transaction-private working storage. A
        // terminal generation must return every one before publication.
        PrivatePageOwner::Normalization => false,
        PrivatePageOwner::Retirement => matches!(tag, 1 | 2),
    }
}

fn terminal_page_matches_owner(owner: PrivatePageOwner, tag: u64, header: PageHeader) -> bool {
    match (owner, tag) {
        (PrivatePageOwner::Bitmap, _) => {
            header.aux == 1
                && matches!(
                    header.page_type,
                    PageType::BitmapBranch | PageType::BitmapLeaf
                )
        }
        (PrivatePageOwner::Range, family) => {
            header.aux == family as u32
                && matches!(
                    header.page_type,
                    PageType::RangeBranch | PageType::RangeLeaf
                )
        }
        (PrivatePageOwner::Normalization, _) => false,
        (PrivatePageOwner::Retirement, 1) => matches!(
            header.page_type,
            PageType::RetirementBranch | PageType::RetirementLeaf
        ),
        (PrivatePageOwner::Retirement, 2) => {
            matches!(header.page_type, PageType::BlobBranch | PageType::BlobLeaf)
        }
        _ => false,
    }
}

fn apply_return(slot: &mut PrivatePagePoolSlot, disposition: PrivatePageReturn) {
    slot.bytes.fill(0);
    slot.allocation_generation = 0;
    slot.state = match disposition {
        PrivatePageReturn::Available => PrivatePageState::Available,
        PrivatePageReturn::Free => PrivatePageState::ReturnedFree,
        PrivatePageReturn::Tail => PrivatePageState::ReturnedTail,
    };
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::page::PAGE_HEADER_SIZE;
    use crate::test_alloc::count_thread_allocations;

    fn slots() -> [PrivatePagePoolSlot; 4] {
        [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::SafelyReclaimed),
            PrivatePagePoolSlot::authorized(20, PrivatePageAuthorization::Appended),
            PrivatePagePoolSlot::empty(),
        ]
    }

    #[test]
    fn terminal_range_owner_requires_a_matching_family_and_range_type() {
        let v4_leaf = PageHeader {
            page_type: PageType::RangeLeaf,
            born_txn: 2,
            item_count: 1,
            level: 0,
            lower: PAGE_HEADER_SIZE,
            upper: PAGE_SIZE as u16,
            aux: 4,
            page_crc32c: 0,
        };
        assert!(valid_terminal_owner_tag(PrivatePageOwner::Range, 4));
        assert!(valid_terminal_owner_tag(PrivatePageOwner::Range, 6));
        assert!(!valid_terminal_owner_tag(PrivatePageOwner::Range, 0));
        assert!(!valid_terminal_owner_tag(PrivatePageOwner::Range, 5));
        assert!(!valid_terminal_owner_tag(
            PrivatePageOwner::Normalization,
            4
        ));
        assert!(terminal_page_matches_owner(
            PrivatePageOwner::Range,
            4,
            v4_leaf
        ));
        assert!(!terminal_page_matches_owner(
            PrivatePageOwner::Range,
            6,
            v4_leaf
        ));
        assert!(!terminal_page_matches_owner(
            PrivatePageOwner::Bitmap,
            0,
            v4_leaf
        ));
        assert!(!terminal_page_matches_owner(
            PrivatePageOwner::Normalization,
            4,
            v4_leaf
        ));
    }

    fn vacant_pool(
        storage: &mut [PrivatePagePoolSlot],
        committed: u64,
        pending: u64,
    ) -> PrivatePagePool<'_> {
        PrivatePagePool::new_vacant(storage, committed, pending, 2).unwrap()
    }

    #[derive(Debug, PartialEq, Eq)]
    struct PoolMutationSnapshot {
        slots: std::vec::Vec<PrivatePagePoolSlot>,
        index_root: usize,
        authorized_len: usize,
        available_count: usize,
        lowest_available: usize,
        pending_page_count: u64,
        identity: usize,
        identity_epoch: usize,
        invalidation_identity: usize,
        abort_epoch_reserve: u64,
        generation: u64,
        epoch: u64,
        active_checkpoint: u64,
        operation_sequence: u64,
        active_operation_id: u64,
        operation_start_epoch: u64,
        abort_required: bool,
        checkpoint_cleanup_slots: usize,
        checkpoint_index_head: usize,
        checkpoint_index_count: usize,
        scope_sequence: u64,
        active_scopes: usize,
        unscoped_vacant_count: usize,
        unscoped_vacant_head: usize,
        unscoped_vacant_tail: usize,
    }

    fn pool_mutation_snapshot(pool: &PrivatePagePool<'_>) -> PoolMutationSnapshot {
        PoolMutationSnapshot {
            slots: pool.slots.borrow().to_vec(),
            index_root: pool.index_root.get(),
            authorized_len: pool.authorized_len.get(),
            available_count: pool.available_count.get(),
            lowest_available: pool.lowest_available.get(),
            pending_page_count: pool.pending_page_count.get(),
            identity: pool.identity,
            identity_epoch: pool.identity_epoch,
            invalidation_identity: pool.invalidation_identity,
            abort_epoch_reserve: pool.abort_epoch_reserve,
            generation: pool.generation.get(),
            epoch: pool.epoch.get(),
            active_checkpoint: pool.active_checkpoint.get(),
            operation_sequence: pool.operation_sequence.get(),
            active_operation_id: pool.active_operation_id.get(),
            operation_start_epoch: pool.operation_start_epoch.get(),
            abort_required: pool.abort_required.get(),
            checkpoint_cleanup_slots: pool.checkpoint_cleanup_slots.get(),
            checkpoint_index_head: pool.checkpoint_index_head.get(),
            checkpoint_index_count: pool.checkpoint_index_count.get(),
            scope_sequence: pool.scope_sequence.get(),
            active_scopes: pool.active_scopes.get(),
            unscoped_vacant_count: pool.unscoped_vacant_count.get(),
            unscoped_vacant_head: pool.unscoped_vacant_head.get(),
            unscoped_vacant_tail: pool.unscoped_vacant_tail.get(),
        }
    }

    fn unscoped_vacancy_order(pool: &PrivatePagePool<'_>) -> std::vec::Vec<usize> {
        let slots = pool.slots.borrow();
        let count = pool.unscoped_vacant_count.get();
        let mut order = std::vec::Vec::with_capacity(count);
        let mut previous = NO_SLOT;
        let mut current = pool.unscoped_vacant_head.get();
        for _ in 0..count {
            assert_ne!(current, NO_SLOT);
            assert!(current < slots.len());
            let slot = &slots[current];
            assert_eq!(slot.unscoped_vacant_prev, previous);
            order.push(current);
            previous = current;
            current = slot.unscoped_vacant_next;
        }
        assert_eq!(current, NO_SLOT);
        assert_eq!(
            previous,
            if count == 0 {
                NO_SLOT
            } else {
                pool.unscoped_vacant_tail.get()
            }
        );
        order
    }

    fn exact_scope_order<const N: usize>(
        pool: &PrivatePagePool<'_>,
        scope: &PrivatePageReservationScope<'_>,
    ) -> [usize; N] {
        let mut order = [NO_SLOT; N];
        let mut visits = 0usize;
        pool.visit_exact_scope_layout(scope, |ordinal, slot, info| {
            assert_eq!(info.member_ordinal, ordinal);
            order[ordinal] = slot;
            visits += 1;
        })
        .unwrap();
        assert_eq!(visits, N);
        order
    }

    #[derive(Clone, Copy, Debug, Default)]
    struct AvlProof {
        minimum: u32,
        maximum: u32,
        height: usize,
        count: usize,
        available: usize,
        in_use: usize,
    }

    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    struct AvlNodeSnapshot {
        index_left: usize,
        index_right: usize,
        index_height: u8,
        index_available: usize,
        index_in_use: usize,
        index_unscoped_available: usize,
        scope_left: usize,
        scope_right: usize,
        scope_height: u8,
        scope_available: usize,
        scope_in_use: usize,
    }

    fn avl_node_snapshots(pool: &PrivatePagePool<'_>) -> std::vec::Vec<AvlNodeSnapshot> {
        pool.slots
            .borrow()
            .iter()
            .map(|slot| AvlNodeSnapshot {
                index_left: slot.index_left,
                index_right: slot.index_right,
                index_height: slot.index_height,
                index_available: slot.index_available,
                index_in_use: slot.index_in_use,
                index_unscoped_available: slot.index_unscoped_available,
                scope_left: slot.scope_left,
                scope_right: slot.scope_right,
                scope_height: slot.scope_height,
                scope_available: slot.scope_available,
                scope_in_use: slot.scope_in_use,
            })
            .collect()
    }

    fn deepest_avl_slot(
        slots: &[PrivatePagePoolSlot],
        root: usize,
        scoped: bool,
    ) -> (usize, usize) {
        if root == NO_SLOT {
            return (NO_SLOT, 0);
        }
        let (left, right) = if scoped {
            (slots[root].scope_left, slots[root].scope_right)
        } else {
            (slots[root].index_left, slots[root].index_right)
        };
        let (left_slot, left_depth) = deepest_avl_slot(slots, left, scoped);
        let (right_slot, right_depth) = deepest_avl_slot(slots, right, scoped);
        if left_depth >= right_depth && left_slot != NO_SLOT {
            (left_slot, left_depth + 1)
        } else if right_slot != NO_SLOT {
            (right_slot, right_depth + 1)
        } else {
            (root, 1)
        }
    }

    fn verify_avl(slots: &[PrivatePagePoolSlot], root: usize, scope_id: Option<u64>) -> AvlProof {
        if root == NO_SLOT {
            return AvlProof::default();
        }
        assert!(root < slots.len());
        let slot = &slots[root];
        assert!(slot.authorization.is_some());
        if let Some(scope_id) = scope_id {
            assert_eq!(slot.scope_id, scope_id);
        }
        let (left, right, height, expected_available, expected_in_use) = if scope_id.is_some() {
            (
                slot.scope_left,
                slot.scope_right,
                usize::from(slot.scope_height),
                slot.scope_available,
                slot.scope_in_use,
            )
        } else {
            (
                slot.index_left,
                slot.index_right,
                usize::from(slot.index_height),
                slot.index_available,
                slot.index_in_use,
            )
        };
        let left_proof = verify_avl(slots, left, scope_id);
        let right_proof = verify_avl(slots, right, scope_id);
        if left_proof.count != 0 {
            assert!(left_proof.maximum < slot.pgno);
        }
        if right_proof.count != 0 {
            assert!(slot.pgno < right_proof.minimum);
        }
        assert!(left_proof.height.abs_diff(right_proof.height) <= 1);
        assert_eq!(height, 1 + left_proof.height.max(right_proof.height));
        let available = left_proof.available
            + right_proof.available
            + usize::from(slot.state == PrivatePageState::Available);
        let in_use = left_proof.in_use
            + right_proof.in_use
            + usize::from(matches!(slot.state, PrivatePageState::InUse { .. }));
        assert_eq!(expected_available, available);
        assert_eq!(expected_in_use, in_use);
        AvlProof {
            minimum: if left_proof.count == 0 {
                slot.pgno
            } else {
                left_proof.minimum
            },
            maximum: if right_proof.count == 0 {
                slot.pgno
            } else {
                right_proof.maximum
            },
            height,
            count: left_proof.count + right_proof.count + 1,
            available,
            in_use,
        }
    }

    fn normalized_slots(pool: &PrivatePagePool<'_>) -> std::vec::Vec<PrivatePagePoolSlot> {
        let mut result = pool.slots.borrow().to_vec();
        for slot in &mut result {
            slot.binding_epoch = 0;
            slot.checkpoint_generation = 0;
            slot.saved_state = SavedState::None;
            slot.saved_binding = SavedBinding::None;
            slot.saved_index_generation = 0;
            slot.saved_index_left = NO_SLOT;
            slot.saved_index_right = NO_SLOT;
            slot.saved_index_height = 0;
            slot.saved_index_available = 0;
            slot.saved_index_in_use = 0;
            slot.saved_index_unscoped_available = 0;
            slot.saved_scope_left = NO_SLOT;
            slot.saved_scope_right = NO_SLOT;
            slot.saved_scope_height = 0;
            slot.saved_scope_available = 0;
            slot.saved_scope_in_use = 0;
            slot.saved_scope_generation = 0;
            slot.saved_scope_root = NO_SLOT;
            slot.saved_scope_vacant_head = NO_SLOT;
            slot.saved_scope_bound = 0;
        }
        result
    }

    fn retained_authority<'pool>(
        authority: &PrivatePageAuthority<'pool>,
    ) -> PrivatePageAuthority<'pool> {
        PrivatePageAuthority {
            pool_identity: authority.pool_identity,
            pool_epoch: authority.pool_epoch,
            slot: authority.slot,
            pgno: authority.pgno,
            owner: authority.owner,
            owner_generation: authority.owner_generation,
            authority_epoch: authority.authority_epoch,
            binding_epoch: authority.binding_epoch,
            scope_id: authority.scope_id,
            _pool: PhantomData,
        }
    }

    fn authority_fingerprint(
        authority: &PrivatePageAuthority<'_>,
    ) -> (
        usize,
        usize,
        usize,
        u32,
        PrivatePageOwner,
        u64,
        u64,
        u64,
        u64,
    ) {
        (
            authority.pool_identity,
            authority.pool_epoch,
            authority.slot,
            authority.pgno,
            authority.owner,
            authority.owner_generation,
            authority.authority_epoch,
            authority.binding_epoch,
            authority.scope_id,
        )
    }

    fn retained_checkpoint<'pool>(
        checkpoint: &PrivatePagePoolCheckpoint<'pool>,
    ) -> PrivatePagePoolCheckpoint<'pool> {
        PrivatePagePoolCheckpoint {
            pool_identity: checkpoint.pool_identity,
            pool_epoch: checkpoint.pool_epoch,
            generation: checkpoint.generation,
            index_root: checkpoint.index_root,
            authorized_len: checkpoint.authorized_len,
            available_count: checkpoint.available_count,
            lowest_available: checkpoint.lowest_available,
            pending_page_count: checkpoint.pending_page_count,
            start_epoch: checkpoint.start_epoch,
            reserved_end_epoch: checkpoint.reserved_end_epoch,
            _slots: PhantomData,
        }
    }

    fn checkpoint_fingerprint(
        checkpoint: &PrivatePagePoolCheckpoint<'_>,
    ) -> (usize, usize, u64, usize, usize, usize, usize, u64, u64, u64) {
        (
            checkpoint.pool_identity,
            checkpoint.pool_epoch,
            checkpoint.generation,
            checkpoint.index_root,
            checkpoint.authorized_len,
            checkpoint.available_count,
            checkpoint.lowest_available,
            checkpoint.pending_page_count,
            checkpoint.start_epoch,
            checkpoint.reserved_end_epoch,
        )
    }

    fn retained_operation<'plan>(
        operation: &PrivatePageScopedOperation<'plan>,
    ) -> PrivatePageScopedOperation<'plan> {
        PrivatePageScopedOperation {
            pool_identity: operation.pool_identity,
            pool_epoch: operation.pool_epoch,
            id: operation.id,
            pending_txn: operation.pending_txn,
            generation: operation.generation,
            scope_id: operation.scope_id,
            scope_anchor: operation.scope_anchor,
            start_epoch: operation.start_epoch,
            mutation_steps: operation.mutation_steps,
            used_mutation_steps: Cell::new(operation.used_mutation_steps.get()),
            slots: operation.slots,
        }
    }

    fn operation_fingerprint(
        operation: &PrivatePageScopedOperation<'_>,
    ) -> (
        usize,
        usize,
        u64,
        u64,
        u64,
        u64,
        usize,
        u64,
        usize,
        usize,
        *const PrivatePageScopedOperationSlot,
    ) {
        (
            operation.pool_identity,
            operation.pool_epoch,
            operation.id,
            operation.pending_txn,
            operation.generation,
            operation.scope_id,
            operation.scope_anchor,
            operation.start_epoch,
            operation.mutation_steps,
            operation.used_mutation_steps.get(),
            operation.slots.as_ptr(),
        )
    }

    #[test]
    fn pool_and_move_only_snapshot_are_bound_to_one_pending_transaction_and_backing() {
        let mut invalid_storage = slots();
        assert_eq!(
            PrivatePagePool::new(&mut invalid_storage, 20, 22, 1).unwrap_err(),
            PrivatePagePoolError::PendingTransactionOutOfRange(1)
        );

        let mut first_storage = slots();
        let mut second_storage = slots();
        {
            let first = PrivatePagePool::new(&mut first_storage, 20, 22, 2).unwrap();
            let second = PrivatePagePool::new(&mut second_storage, 20, 22, 2).unwrap();
            assert_eq!(first.pending_txn(), 2);
            let snapshot = first.mutation_snapshot().unwrap();
            assert_eq!(
                second.preflight_mutation(&snapshot, 0).unwrap_err(),
                PrivatePagePoolError::PoolMismatch
            );
            first.preflight_mutation(&snapshot, 0).unwrap();
        }

        let mut first_empty = [];
        let mut second_empty = [];
        {
            let first_empty_pool = PrivatePagePool::new(&mut first_empty, 20, 20, 2).unwrap();
            let second_empty_pool = PrivatePagePool::new(&mut second_empty, 20, 20, 2).unwrap();
            let empty_snapshot = first_empty_pool.mutation_snapshot().unwrap();
            assert_eq!(
                second_empty_pool
                    .preflight_mutation(&empty_snapshot, 0)
                    .unwrap_err(),
                PrivatePagePoolError::PoolMismatch
            );
        }

        let reinitialized = PrivatePagePool::new(&mut first_storage, 20, 22, 3).unwrap();
        assert_eq!(reinitialized.pending_txn(), 3);
    }

    #[test]
    fn owned_page_copy_is_exact_typed_side_effect_free_and_allocation_free() {
        let mut storage = slots();
        let mut foreign_storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let foreign = PrivatePagePool::new(&mut foreign_storage, 20, 21, 2).unwrap();
        let authority = pool.claim(0, PrivatePageOwner::Retirement, 7, 2).unwrap();
        pool.borrow_page_mut(&authority).unwrap()[17] = 0x5a;
        let snapshot = pool.mutation_snapshot().unwrap();
        let foreign_snapshot = foreign.mutation_snapshot().unwrap();

        let before_slots = pool.slots.borrow().to_vec();
        let before_counters = (
            pool.authorized_len.get(),
            pool.available_count.get(),
            pool.lowest_available.get(),
            pool.generation.get(),
            pool.epoch.get(),
            pool.active_checkpoint.get(),
        );
        let mut destination = [0xa5; PAGE_SIZE];
        let ((), allocations) = count_thread_allocations(|| {
            pool.copy_owned_page(
                &snapshot,
                0,
                3,
                PrivatePageOwner::Retirement,
                7,
                2,
                2,
                &mut destination,
            )
            .unwrap();
        });
        assert_eq!(allocations, 0);
        assert_eq!(destination[17], 0x5a);

        let cases = [
            (
                4,
                0,
                PrivatePageOwner::Retirement,
                7,
                2,
                2,
                PrivatePagePoolError::PageNotFound(4),
            ),
            (
                3,
                1,
                PrivatePageOwner::Retirement,
                7,
                2,
                2,
                PrivatePagePoolError::PageNotFound(3),
            ),
            (
                3,
                0,
                PrivatePageOwner::Bitmap,
                7,
                2,
                2,
                PrivatePagePoolError::OwnerMismatch {
                    pgno: 3,
                    expected: PrivatePageOwner::Bitmap,
                    actual: PrivatePageOwner::Retirement,
                },
            ),
            (
                3,
                0,
                PrivatePageOwner::Retirement,
                8,
                2,
                2,
                PrivatePagePoolError::StaleAuthority,
            ),
            (
                3,
                0,
                PrivatePageOwner::Retirement,
                7,
                1,
                2,
                PrivatePagePoolError::StaleAuthority,
            ),
            (
                3,
                0,
                PrivatePageOwner::Retirement,
                7,
                2,
                3,
                PrivatePagePoolError::PendingTransactionMismatch {
                    expected: 2,
                    actual: 3,
                },
            ),
        ];
        for (pgno, slot, owner, generation, tag, pending_txn, expected) in cases {
            let mut untouched = [0x3c; PAGE_SIZE];
            assert_eq!(
                pool.copy_owned_page(
                    &snapshot,
                    slot,
                    pgno,
                    owner,
                    generation,
                    tag,
                    pending_txn,
                    &mut untouched,
                ),
                Err(expected)
            );
            assert_eq!(untouched, [0x3c; PAGE_SIZE]);
        }

        let mut untouched = [0x3c; PAGE_SIZE];
        assert_eq!(
            pool.copy_owned_page(
                &foreign_snapshot,
                0,
                3,
                PrivatePageOwner::Retirement,
                7,
                2,
                2,
                &mut untouched,
            ),
            Err(PrivatePagePoolError::PoolMismatch)
        );
        let borrow = pool.slots.borrow_mut();
        assert_eq!(
            pool.copy_owned_page(
                &snapshot,
                0,
                3,
                PrivatePageOwner::Retirement,
                7,
                2,
                2,
                &mut untouched,
            ),
            Err(PrivatePagePoolError::BorrowConflict)
        );
        drop(borrow);

        assert_eq!(&**pool.slots.borrow(), before_slots.as_slice());
        assert_eq!(
            (
                pool.authorized_len.get(),
                pool.available_count.get(),
                pool.lowest_available.get(),
                pool.generation.get(),
                pool.epoch.get(),
                pool.active_checkpoint.get(),
            ),
            before_counters
        );
    }

    #[test]
    fn exact_authorization_unique_index_and_lowest_claim() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 22, 2).unwrap();
        pool.authorize(3, 21, PrivatePageAuthorization::Appended)
            .unwrap();
        assert_eq!(pool.find(7).unwrap(), Some(1));
        assert_eq!(pool.find(8).unwrap(), None);
        let authority = pool.claim_lowest(PrivatePageOwner::Bitmap, 4, 9).unwrap();
        assert_eq!(authority.page_number(), 3);
        assert_eq!(pool.available().unwrap(), 3);
    }

    #[test]
    fn transfer_is_move_only_and_preserves_page_without_copying() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let bitmap = pool.claim_lowest(PrivatePageOwner::Bitmap, 4, 9).unwrap();
        pool.borrow_page_mut(&bitmap).unwrap()[17] = 0x5a;
        let retirement = pool
            .transfer(bitmap, PrivatePageOwner::Retirement, 4, 11)
            .unwrap();
        assert_eq!(retirement.page_number(), 3);
        assert_eq!(pool.borrow_page(&retirement).unwrap()[17], 0x5a);
    }

    #[test]
    fn stale_aba_cross_pool_and_double_owner_are_rejected() {
        let mut first_storage = slots();
        let mut second_storage = slots();
        let first = PrivatePagePool::new(&mut first_storage, 20, 21, 2).unwrap();
        let second = PrivatePagePool::new(&mut second_storage, 20, 21, 2).unwrap();
        let stale = first.claim_lowest(PrivatePageOwner::Bitmap, 4, 1).unwrap();
        let current = first.authority(3, PrivatePageOwner::Bitmap, 4).unwrap();
        assert_eq!(
            first.borrow_page(&stale).unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );
        let (_stale, error) = second
            .transfer(stale, PrivatePageOwner::Retirement, 4, 2)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::PoolMismatch);
        assert_eq!(
            first
                .authority(3, PrivatePageOwner::Retirement, 4)
                .unwrap_err(),
            PrivatePagePoolError::OwnerMismatch {
                pgno: 3,
                expected: PrivatePageOwner::Retirement,
                actual: PrivatePageOwner::Bitmap,
            }
        );
        assert_eq!(current.owner(), PrivatePageOwner::Bitmap);
    }

    #[test]
    fn checkpoint_rolls_back_claim_and_transfer_and_commits_recycle() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let original = pool.claim_lowest(PrivatePageOwner::Bitmap, 3, 7).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let transferred = pool
            .transfer(original, PrivatePageOwner::Retirement, 4, 8)
            .unwrap();
        let allocated = pool
            .claim_lowest(PrivatePageOwner::Retirement, 4, 9)
            .unwrap();
        assert_eq!(allocated.page_number(), 7);
        pool.return_page(transferred, PrivatePageReturn::Available)
            .unwrap();
        pool.rollback_checkpoint(checkpoint).unwrap();
        let restored = pool.authority(3, PrivatePageOwner::Bitmap, 3).unwrap();
        assert_eq!(restored.page_number(), 3);
        assert_eq!(pool.state(1).unwrap(), PrivatePagePoolState::Available);

        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.return_page(restored, PrivatePageReturn::Free).unwrap();
        assert!(matches!(
            pool.state(0).unwrap(),
            PrivatePagePoolState::PendingReturn { .. }
        ));
        pool.commit_checkpoint(checkpoint).unwrap();
        assert_eq!(pool.state(0).unwrap(), PrivatePagePoolState::ReturnedFree);
    }

    #[test]
    fn rollback_preflights_every_saved_slot_before_restoring_any_slot() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let original = pool.claim(0, PrivatePageOwner::Bitmap, 2, 3).unwrap();
        pool.borrow_page_mut(&original).unwrap()[0] = 0xa5;
        let checkpoint = pool.begin_checkpoint().unwrap();
        let _transferred = pool
            .transfer(original, PrivatePageOwner::Retirement, 2, 4)
            .unwrap();
        let allocated = pool.claim(1, PrivatePageOwner::Retirement, 2, 5).unwrap();
        pool.borrow_page_mut(&allocated).unwrap()[0] = 0x5a;
        pool.test_clear_saved_state(1);

        let states_before = [pool.state(0).unwrap(), pool.state(1).unwrap()];
        let bytes_before = [pool.test_bytes(0).unwrap(), pool.test_bytes(1).unwrap()];
        let available_before = pool.available().unwrap();
        let (_checkpoint, error) = pool.rollback_checkpoint(checkpoint).unwrap_err();
        assert_eq!(error, PrivatePagePoolError::CheckpointMismatch);

        assert_eq!(pool.state(0).unwrap(), states_before[0]);
        assert_eq!(pool.state(1).unwrap(), states_before[1]);
        assert_eq!(pool.test_bytes(0).unwrap(), bytes_before[0]);
        assert_eq!(pool.test_bytes(1).unwrap(), bytes_before[1]);
        assert_eq!(pool.available().unwrap(), available_before);
        assert_eq!(
            pool.begin_checkpoint().unwrap_err(),
            PrivatePagePoolError::CheckpointActive
        );
    }

    #[test]
    fn live_borrow_blocks_alias_and_mutating_existing_page_under_checkpoint() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let authority = pool.claim_lowest(PrivatePageOwner::Bitmap, 3, 7).unwrap();
        let page = pool.borrow_page_mut(&authority).unwrap();
        assert_eq!(
            pool.authority(3, PrivatePageOwner::Bitmap, 3).unwrap_err(),
            PrivatePagePoolError::BorrowConflict
        );
        drop(page);
        let authority = pool.authority(3, PrivatePageOwner::Bitmap, 3).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        assert_eq!(
            pool.borrow_page_mut(&authority).unwrap_err(),
            PrivatePagePoolError::RollbackUnsafeWrite(3)
        );
        pool.rollback_checkpoint(checkpoint).unwrap();
    }

    #[test]
    fn checkpoint_transfer_cannot_modify_bytes_before_rollback() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let bitmap = pool.claim_lowest(PrivatePageOwner::Bitmap, 3, 7).unwrap();
        pool.borrow_page_mut(&bitmap).unwrap()[5] = 0xa5;

        let checkpoint = pool.begin_checkpoint().unwrap();
        let retirement = pool
            .transfer(bitmap, PrivatePageOwner::Retirement, 4, 8)
            .unwrap();
        assert_eq!(pool.borrow_page(&retirement).unwrap()[5], 0xa5);
        assert_eq!(
            pool.borrow_page_mut(&retirement).unwrap_err(),
            PrivatePagePoolError::RollbackUnsafeWrite(3)
        );
        pool.rollback_checkpoint(checkpoint).unwrap();

        let restored = pool.authority(3, PrivatePageOwner::Bitmap, 3).unwrap();
        assert_eq!(pool.borrow_page(&restored).unwrap()[5], 0xa5);
    }

    #[test]
    fn live_mutable_guard_blocks_checkpoint_start() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let authority = pool.claim_lowest(PrivatePageOwner::Bitmap, 3, 7).unwrap();
        let mut page = pool.borrow_page_mut(&authority).unwrap();
        page[5] = 0x5a;
        assert_eq!(
            pool.begin_checkpoint().unwrap_err(),
            PrivatePagePoolError::BorrowConflict
        );
        page[5] = 0xa5;
        drop(page);
        let checkpoint = pool.begin_checkpoint().unwrap();
        assert_eq!(
            pool.borrow_page_mut(&authority).unwrap_err(),
            PrivatePagePoolError::RollbackUnsafeWrite(3)
        );
        pool.rollback_checkpoint(checkpoint).unwrap();
        let current = pool.authority(3, PrivatePageOwner::Bitmap, 3).unwrap();
        assert_eq!(pool.borrow_page(&current).unwrap()[5], 0xa5);
    }

    #[test]
    fn mutable_guard_acquisition_advances_epoch_without_changing_binding_and_is_atomic_at_max() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let authority = pool.claim_lowest(PrivatePageOwner::Bitmap, 3, 7).unwrap();
        let slot = authority.slot;
        let binding_epoch = pool.slots.borrow()[slot].binding_epoch;
        let bytes = pool.test_bytes(slot).unwrap();

        pool.test_set_epoch(u64::MAX - 1);
        let page = pool.borrow_page_mut(&authority).unwrap();
        assert_eq!(pool.mutation_epoch(), u64::MAX);
        drop(page);
        assert_eq!(pool.slots.borrow()[slot].binding_epoch, binding_epoch);
        assert_eq!(pool.test_bytes(slot).unwrap(), bytes);

        let before = pool.slots.borrow().to_vec();
        assert_eq!(
            pool.borrow_page_mut(&authority).unwrap_err(),
            PrivatePagePoolError::EpochExhausted
        );
        assert_eq!(pool.mutation_epoch(), u64::MAX);
        assert_eq!(&**pool.slots.borrow(), before.as_slice());
    }

    #[test]
    fn scoped_mutable_guard_reserves_exact_checkpoint_rollback_suffix_at_max() {
        for (start_epoch, succeeds) in [(u64::MAX - 3, true), (u64::MAX - 2, false)] {
            let mut storage = [PrivatePagePoolSlot::empty()];
            let pool = vacant_pool(&mut storage, 20, 20);
            let scope = pool.reserve_scope(1).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::CommittedFree,
            )
            .unwrap();
            pool.commit_checkpoint(checkpoint).unwrap();

            let checkpoint = pool.begin_checkpoint().unwrap();
            let authority = pool
                .claim_page_in_scope(&checkpoint, &scope, 7, PrivatePageOwner::Bitmap, 3, 9)
                .unwrap();
            let slot = authority.slot;
            let before = pool.slots.borrow().to_vec();
            let binding_epoch = before[slot].binding_epoch;
            pool.test_set_epoch(start_epoch);

            if succeeds {
                pool.borrow_page_mut_in_scope(&scope, &authority).unwrap()[0] = 0x5a;
                assert_eq!(pool.mutation_epoch(), u64::MAX - 2);
                assert_eq!(pool.slots.borrow()[slot].binding_epoch, binding_epoch);
                pool.rollback_checkpoint_in_scope(checkpoint, &scope)
                    .unwrap();
                assert_eq!(pool.mutation_epoch(), u64::MAX);
                assert_eq!(pool.scoped_available(&scope).unwrap(), 1);
                assert_eq!(pool.test_bytes(slot).unwrap(), [0u8; PAGE_SIZE]);
            } else {
                assert_eq!(
                    pool.borrow_page_mut_in_scope(&scope, &authority)
                        .unwrap_err(),
                    PrivatePagePoolError::EpochExhausted
                );
                assert_eq!(pool.mutation_epoch(), start_epoch);
                assert_eq!(&**pool.slots.borrow(), before.as_slice());
                pool.test_set_epoch(100);
                pool.rollback_checkpoint_in_scope(checkpoint, &scope)
                    .unwrap();
            }
        }
    }

    #[test]
    fn active_checkpoint_rejects_authorize_and_cross_pool_checkpoint() {
        let mut first_storage = slots();
        let mut second_storage = slots();
        let first = PrivatePagePool::new(&mut first_storage, 20, 22, 2).unwrap();
        let second = PrivatePagePool::new(&mut second_storage, 20, 22, 2).unwrap();
        let checkpoint = first.begin_checkpoint().unwrap();
        assert_eq!(
            first
                .authorize(3, 21, PrivatePageAuthorization::Appended)
                .unwrap_err(),
            PrivatePagePoolError::CheckpointActive
        );
        let (checkpoint, error) = second.rollback_checkpoint(checkpoint).unwrap_err();
        assert_eq!(error, PrivatePagePoolError::PoolMismatch);
        first.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(first.find(21).unwrap(), None);
    }

    #[test]
    fn rollback_invalidates_every_touched_authority_epoch_monotonically() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let original = pool.claim_lowest(PrivatePageOwner::Bitmap, 3, 7).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let rotated = pool.authority(3, PrivatePageOwner::Bitmap, 3).unwrap();
        assert_eq!(
            pool.borrow_page(&original).unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );
        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(
            pool.borrow_page(&original).unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );
        assert_eq!(
            pool.borrow_page(&rotated).unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );
        let reacquired = pool.authority(3, PrivatePageOwner::Bitmap, 3).unwrap();
        assert_eq!(pool.borrow_page(&reacquired).unwrap()[0], 0);
    }

    #[test]
    fn tail_requires_appended_while_appended_free_is_final() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let committed = pool.claim(0, PrivatePageOwner::Bitmap, 3, 0).unwrap();
        let (committed, error) = pool
            .return_page(committed, PrivatePageReturn::Tail)
            .unwrap_err();
        assert_eq!(
            error,
            PrivatePagePoolError::AuthorizationMismatch {
                pgno: 3,
                authorization: PrivatePageAuthorization::CommittedFree,
            }
        );
        assert_eq!(pool.borrow_page(&committed).unwrap()[0], 0);
        pool.return_page(committed, PrivatePageReturn::Available)
            .unwrap();

        let appended = pool.claim(2, PrivatePageOwner::Bitmap, 3, 0).unwrap();
        pool.return_page(appended, PrivatePageReturn::Free).unwrap();
        assert_eq!(pool.state(2).unwrap(), PrivatePagePoolState::ReturnedFree);
        assert_eq!(
            pool.claim(2, PrivatePageOwner::Retirement, 4, 0)
                .unwrap_err(),
            PrivatePagePoolError::PageUnavailable(20)
        );
    }

    #[test]
    fn live_guard_blocks_transfer_and_pre_transfer_token_stays_stale() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let stale = pool.claim_lowest(PrivatePageOwner::Bitmap, 3, 7).unwrap();
        let current = pool.authority(3, PrivatePageOwner::Bitmap, 3).unwrap();

        let guard = pool.borrow_page(&current).unwrap();
        let (current, error) = pool
            .transfer(current, PrivatePageOwner::Retirement, 4, 8)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::BorrowConflict);
        drop(guard);

        let retirement = pool
            .transfer(current, PrivatePageOwner::Retirement, 4, 8)
            .unwrap();
        assert_eq!(
            pool.borrow_page(&stale).unwrap_err(),
            PrivatePagePoolError::OwnerMismatch {
                pgno: 3,
                expected: PrivatePageOwner::Bitmap,
                actual: PrivatePageOwner::Retirement,
            }
        );
        assert_eq!(pool.borrow_page(&retirement).unwrap()[0], 0);
    }

    #[test]
    fn warmed_claim_transfer_borrow_and_recycle_allocate_nothing() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let ((), allocations) = count_thread_allocations(|| {
            let bitmap = pool.claim_lowest(PrivatePageOwner::Bitmap, 4, 1).unwrap();
            pool.borrow_page_mut(&bitmap).unwrap()[0] = 1;
            let retirement = pool
                .transfer(bitmap, PrivatePageOwner::Retirement, 4, 2)
                .unwrap();
            assert_eq!(pool.borrow_page(&retirement).unwrap()[0], 1);
            pool.return_page(retirement, PrivatePageReturn::Available)
                .unwrap();
        });
        assert_eq!(allocations, 0);
    }

    #[test]
    fn move_authority_page_failures_return_exact_tokens_retry_and_replay_stales() {
        let mut storage = slots();
        let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
        let authority = pool.claim_lowest(PrivatePageOwner::Bitmap, 4, 1).unwrap();
        let expected = authority_fingerprint(&authority);
        let replay = retained_authority(&authority);
        let before = pool_mutation_snapshot(&pool);
        let guard = pool.borrow_page(&authority).unwrap();
        let ((authority, error), allocations) = count_thread_allocations(|| {
            pool.transfer(authority, PrivatePageOwner::Retirement, 5, 2)
                .unwrap_err()
        });
        assert_eq!(allocations, 0);
        assert_eq!(error, PrivatePagePoolError::BorrowConflict);
        assert_eq!(authority_fingerprint(&authority), expected);
        drop(guard);
        assert_eq!(pool_mutation_snapshot(&pool), before);

        let (retirement, allocations) = count_thread_allocations(|| {
            pool.transfer(authority, PrivatePageOwner::Retirement, 5, 2)
                .unwrap()
        });
        assert_eq!(allocations, 0);
        let (_replay, error) = pool
            .transfer(replay, PrivatePageOwner::Retirement, 5, 2)
            .unwrap_err();
        assert_eq!(
            error,
            PrivatePagePoolError::OwnerMismatch {
                pgno: 3,
                expected: PrivatePageOwner::Bitmap,
                actual: PrivatePageOwner::Retirement,
            }
        );
        let expected = authority_fingerprint(&retirement);
        let replay = retained_authority(&retirement);
        let before = pool_mutation_snapshot(&pool);
        let (retirement, error) = pool
            .return_page(retirement, PrivatePageReturn::Tail)
            .unwrap_err();
        assert_eq!(
            error,
            PrivatePagePoolError::AuthorizationMismatch {
                pgno: 3,
                authorization: PrivatePageAuthorization::CommittedFree,
            }
        );
        assert_eq!(authority_fingerprint(&retirement), expected);
        assert_eq!(pool_mutation_snapshot(&pool), before);
        let ((), allocations) = count_thread_allocations(|| {
            pool.return_page(retirement, PrivatePageReturn::Available)
                .unwrap();
        });
        assert_eq!(allocations, 0);
        let (_replay, error) = pool
            .return_page(replay, PrivatePageReturn::Available)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::StaleAuthority);

        let mut scoped_storage = [PrivatePagePoolSlot::empty(), PrivatePagePoolSlot::empty()];
        let scoped_pool = vacant_pool(&mut scoped_storage, 20, 20);
        let target = scoped_pool.reserve_scope(1).unwrap();
        let foreign = scoped_pool.reserve_scope(1).unwrap();
        let checkpoint = scoped_pool.begin_checkpoint().unwrap();
        scoped_pool
            .bind_page(
                &checkpoint,
                &target,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        let authority = scoped_pool
            .claim_page_in_scope(&checkpoint, &target, 7, PrivatePageOwner::Bitmap, 4, 1)
            .unwrap();
        scoped_pool.commit_checkpoint(checkpoint).unwrap();

        let expected = authority_fingerprint(&authority);
        let before = pool_mutation_snapshot(&scoped_pool);
        let (authority, error) = scoped_pool
            .transfer_in_scope(&foreign, authority, PrivatePageOwner::Retirement, 5, 2)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::ScopeMismatch(7));
        assert_eq!(authority_fingerprint(&authority), expected);
        assert_eq!(pool_mutation_snapshot(&scoped_pool), before);
        let retirement = scoped_pool
            .transfer_in_scope(&target, authority, PrivatePageOwner::Retirement, 5, 2)
            .unwrap();

        let mut wrong_owner = retained_authority(&retirement);
        wrong_owner.owner = PrivatePageOwner::Bitmap;
        let expected = authority_fingerprint(&wrong_owner);
        let before = pool_mutation_snapshot(&scoped_pool);
        let (mut wrong_owner, error) = scoped_pool
            .return_page_in_scope(&target, wrong_owner, PrivatePageReturn::Available)
            .unwrap_err();
        assert_eq!(
            error,
            PrivatePagePoolError::OwnerMismatch {
                pgno: 7,
                expected: PrivatePageOwner::Bitmap,
                actual: PrivatePageOwner::Retirement,
            }
        );
        assert_eq!(authority_fingerprint(&wrong_owner), expected);
        assert_eq!(pool_mutation_snapshot(&scoped_pool), before);
        wrong_owner.owner = PrivatePageOwner::Retirement;
        scoped_pool
            .return_page_in_scope(&target, wrong_owner, PrivatePageReturn::Available)
            .unwrap();

        let (_retirement, error) = scoped_pool
            .return_page_in_scope(&target, retirement, PrivatePageReturn::Available)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::StaleAuthority);
    }

    #[test]
    fn lowest_claim_cursor_scales_linearly_from_512_to_4096_slots() {
        for count in [512usize, 4096] {
            let mut storage: std::vec::Vec<_> = (0..count)
                .map(|index| {
                    PrivatePagePoolSlot::authorized(
                        u32::try_from(index + 2).unwrap(),
                        PrivatePageAuthorization::CommittedFree,
                    )
                })
                .collect();
            let pool = PrivatePagePool::new(&mut storage, 5000, 5000, 2).unwrap();
            for generation in 0..count {
                pool.claim_lowest(
                    PrivatePageOwner::Bitmap,
                    u64::try_from(generation).unwrap() + 1,
                    0,
                )
                .unwrap();
            }
            assert_eq!(pool.claim_probe_count(), count);
        }
    }

    #[test]
    fn vacant_scope_reverse_bind_and_lowest_are_indexed() {
        let mut storage: std::vec::Vec<_> = (0..8).map(|_| PrivatePagePoolSlot::empty()).collect();
        let pool = vacant_pool(&mut storage, 100, 100);
        let scope = pool.reserve_scope(8).unwrap();
        let before = normalized_slots(&pool);
        let checkpoint = pool.begin_checkpoint().unwrap();
        for pgno in (2..=9).rev() {
            pool.bind_page(
                &checkpoint,
                &scope,
                pgno,
                PrivatePageAuthorization::CommittedFree,
            )
            .unwrap();
        }

        let slots = pool.slots.borrow();
        let global = verify_avl(&slots, pool.index_root.get(), None);
        let scoped = verify_avl(&slots, slots[scope.anchor].scope_root, Some(scope.id));
        assert_eq!((global.count, global.minimum), (8, 2));
        assert_eq!((scoped.count, scoped.minimum), (8, 2));
        drop(slots);
        assert_eq!(pool.scoped_available(&scope).unwrap(), 8);
        assert_eq!(pool.pending_page_count(), 100);

        let authority = pool
            .claim_lowest_in_scope(&checkpoint, &scope, PrivatePageOwner::Bitmap, 3, 7)
            .unwrap();
        assert_eq!(authority.page_number(), 2);
        assert_eq!(
            pool.borrow_page(&authority).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(2)
        );
        assert_eq!(pool.borrow_page_in_scope(&scope, &authority).unwrap()[0], 0);

        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(pool.index_root.get(), NO_SLOT);
        assert_eq!(pool.pending_page_count(), 100);
        assert_eq!(normalized_slots(&pool), before);
    }

    #[test]
    fn binding_failures_are_atomic_and_every_legacy_path_rejects_scoped_slots() {
        let mut storage: std::vec::Vec<_> = (0..4).map(|_| PrivatePagePoolSlot::empty()).collect();
        let pool = vacant_pool(&mut storage, 20, 20);
        let first = pool.reserve_scope(2).unwrap();
        let second = pool.reserve_scope(2).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let first_slot = pool
            .bind_page(
                &checkpoint,
                &first,
                7,
                PrivatePageAuthorization::CommittedFree,
            )
            .unwrap();

        let assert_atomic = |expected, operation: &dyn Fn() -> PrivatePagePoolError| {
            let before_slots = pool.slots.borrow().to_vec();
            let before_scalars = (
                pool.index_root.get(),
                pool.authorized_len.get(),
                pool.available_count.get(),
                pool.lowest_available.get(),
                pool.pending_page_count(),
                pool.epoch.get(),
                pool.generation.get(),
                pool.active_checkpoint.get(),
                pool.checkpoint_cleanup_slots.get(),
                pool.scope_sequence.get(),
                pool.active_scopes.get(),
            );
            assert_eq!(operation(), expected);
            assert_eq!(&**pool.slots.borrow(), before_slots.as_slice());
            assert_eq!(
                (
                    pool.index_root.get(),
                    pool.authorized_len.get(),
                    pool.available_count.get(),
                    pool.lowest_available.get(),
                    pool.pending_page_count(),
                    pool.epoch.get(),
                    pool.generation.get(),
                    pool.active_checkpoint.get(),
                    pool.checkpoint_cleanup_slots.get(),
                    pool.scope_sequence.get(),
                    pool.active_scopes.get(),
                ),
                before_scalars
            );
        };
        assert_atomic(
            PrivatePagePoolError::PagesNotStrict {
                previous: 7,
                current: 7,
            },
            &|| {
                pool.bind_page(
                    &checkpoint,
                    &second,
                    7,
                    PrivatePageAuthorization::SafelyReclaimed,
                )
                .unwrap_err()
            },
        );
        assert_atomic(PrivatePagePoolError::PageNotFound(7), &|| {
            pool.unbind_page(&checkpoint, &second, 7).unwrap_err()
        });
        assert_atomic(PrivatePagePoolError::PageOutOfBounds(0), &|| {
            pool.bind_page(
                &checkpoint,
                &second,
                0,
                PrivatePageAuthorization::CommittedFree,
            )
            .unwrap_err()
        });
        assert_atomic(
            PrivatePagePoolError::AuthorizationMismatch {
                pgno: 20,
                authorization: PrivatePageAuthorization::CommittedFree,
            },
            &|| {
                pool.bind_page(
                    &checkpoint,
                    &second,
                    20,
                    PrivatePageAuthorization::CommittedFree,
                )
                .unwrap_err()
            },
        );
        assert_atomic(
            PrivatePagePoolError::AuthorizationMismatch {
                pgno: 21,
                authorization: PrivatePageAuthorization::Appended,
            },
            &|| {
                pool.bind_page(&checkpoint, &second, 21, PrivatePageAuthorization::Appended)
                    .unwrap_err()
            },
        );
        let forged = PrivatePageReservationScope {
            pool_identity: first.pool_identity,
            pool_epoch: first.pool_epoch,
            id: first.id + 1,
            pending_txn: first.pending_txn,
            anchor: first.anchor,
            generation: first.generation,
            _pool: PhantomData,
        };
        assert_atomic(PrivatePagePoolError::StaleScope, &|| {
            pool.bind_page(
                &checkpoint,
                &forged,
                8,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap_err()
        });

        assert_eq!(
            pool.find(7).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(7)
        );
        assert_eq!(
            pool.state(first_slot).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.page_number(first_slot).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.claim(first_slot, PrivatePageOwner::Bitmap, 3, 7)
                .unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        let authority = pool
            .claim_page_in_scope(&checkpoint, &first, 7, PrivatePageOwner::Bitmap, 3, 7)
            .unwrap();
        assert_eq!(
            pool.borrow_page(&authority).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(7)
        );
        let authority = pool
            .authority_in_scope(&first, 7, PrivatePageOwner::Bitmap, 3)
            .unwrap();
        let (authority, error) = pool
            .return_page(authority, PrivatePageReturn::Available)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::ScopeMismatch(7));
        pool.return_page_in_scope(&first, authority, PrivatePageReturn::Available)
            .unwrap();
        pool.rollback_checkpoint(checkpoint).unwrap();

        let mut other_storage = [PrivatePagePoolSlot::empty()];
        let other = vacant_pool(&mut other_storage, 20, 20);
        let other_checkpoint = other.begin_checkpoint().unwrap();
        assert_eq!(
            other
                .bind_page(
                    &other_checkpoint,
                    &first,
                    7,
                    PrivatePageAuthorization::SafelyReclaimed,
                )
                .unwrap_err(),
            PrivatePagePoolError::PoolMismatch
        );
        other.rollback_checkpoint(other_checkpoint).unwrap();
    }

    #[test]
    fn scope_lookup_rejects_cycles_and_out_of_range_links_within_exact_capacity() {
        for cycle in [true, false] {
            let mut storage = [const { PrivatePagePoolSlot::empty() }; 2];
            let pool = vacant_pool(&mut storage, 20, 20);
            let scope = pool.reserve_scope(2).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            for pgno in [7, 9] {
                pool.bind_page(
                    &checkpoint,
                    &scope,
                    pgno,
                    PrivatePageAuthorization::CommittedFree,
                )
                .unwrap();
            }
            pool.commit_checkpoint(checkpoint).unwrap();
            let root = pool.slots.borrow()[scope.anchor].scope_root;
            if cycle {
                pool.slots.borrow_mut()[root].scope_left = root;
            } else {
                pool.slots.borrow_mut()[root].scope_left = pool.len();
            }
            pool.test_reset_scope_lookup_probes();

            assert_eq!(
                pool.find_in_scope(&scope, 3).unwrap_err(),
                PrivatePagePoolError::StaleScope
            );
            assert!(pool.test_scope_lookup_probes() <= 2);
        }
    }

    #[test]
    fn active_scope_rejects_every_unscoped_legacy_path_even_for_unscoped_pages() {
        let mut owned = PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree);
        owned.preset_bitmap_page(3, 7, [0; PAGE_SIZE]);
        let mut storage = [owned, PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new(&mut storage, 20, 20, 2).unwrap();
        let authority = pool.authority(3, PrivatePageOwner::Bitmap, 3).unwrap();
        let snapshot = pool.mutation_snapshot().unwrap();
        let scope = pool.reserve_scope(1).unwrap();

        assert_eq!(
            pool.mutation_snapshot().unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.preflight_mutation(&snapshot, 0).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        let mut destination = [0xa5; PAGE_SIZE];
        assert_eq!(
            pool.copy_owned_page(
                &snapshot,
                0,
                3,
                PrivatePageOwner::Bitmap,
                3,
                7,
                2,
                &mut destination,
            )
            .unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(destination, [0xa5; PAGE_SIZE]);
        assert_eq!(
            pool.state(0).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.validate_available(0).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.validate_owner(0, PrivatePageOwner::Bitmap, 3)
                .unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.page_number(0).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.authorization(0).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.find(3).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(3)
        );
        assert_eq!(
            pool.authorize(1, 4, PrivatePageAuthorization::CommittedFree)
                .unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(4)
        );
        assert_eq!(
            pool.available().unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.adapter_label(0).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.claim_lowest(PrivatePageOwner::Retirement, 4, 8)
                .unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.claim(0, PrivatePageOwner::Retirement, 4, 8)
                .unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(0)
        );
        assert_eq!(
            pool.authority(3, PrivatePageOwner::Bitmap, 3).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(3)
        );
        assert_eq!(
            pool.borrow_page(&authority).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(3)
        );
        assert_eq!(
            pool.borrow_page_mut(&authority).unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(3)
        );
        let (failed_authority, error) = pool
            .transfer(
                retained_authority(&authority),
                PrivatePageOwner::Retirement,
                4,
                8,
            )
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::ScopeMismatch(3));
        let (_authority, error) = pool
            .return_page(failed_authority, PrivatePageReturn::Available)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::ScopeMismatch(3));

        pool.close_scope(&scope).unwrap();
        assert_eq!(pool.borrow_page(&authority).unwrap()[0], 0);
    }

    #[test]
    fn arbitrary_avl_deletion_preserves_global_and_scope_indexes() {
        const COUNT: usize = 127;
        let mut storage: std::vec::Vec<_> =
            (0..COUNT).map(|_| PrivatePagePoolSlot::empty()).collect();
        let pool = vacant_pool(&mut storage, 1000, 1000);
        let scope = pool.reserve_scope(COUNT).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        for index in 0..COUNT {
            let pgno = u32::try_from((index * 53) % COUNT + 2).unwrap();
            pool.bind_page(
                &checkpoint,
                &scope,
                pgno,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        }
        pool.commit_checkpoint(checkpoint).unwrap();

        let checkpoint = pool.begin_checkpoint().unwrap();
        for index in 0..COUNT {
            let pgno = u32::try_from((index * 89) % COUNT + 2).unwrap();
            pool.unbind_page(&checkpoint, &scope, pgno).unwrap();
            let remaining = COUNT - index - 1;
            let slots = pool.slots.borrow();
            let global = verify_avl(&slots, pool.index_root.get(), None);
            let scoped = verify_avl(&slots, slots[scope.anchor].scope_root, Some(scope.id));
            assert_eq!((global.count, scoped.count), (remaining, remaining));
        }
        pool.commit_checkpoint(checkpoint).unwrap();
        pool.close_scope(&scope).unwrap();
    }

    #[test]
    fn deep_global_claim_transfer_and_return_rollback_restore_every_aggregate() {
        const COUNT: usize = 31;
        let mut storage: std::vec::Vec<_> = (0..COUNT)
            .map(|index| {
                let mut slot = PrivatePagePoolSlot::authorized(
                    u32::try_from(index + 2).unwrap(),
                    PrivatePageAuthorization::CommittedFree,
                );
                if index + 1 != COUNT {
                    slot.preset_bitmap_page(3, 7, [0; PAGE_SIZE]);
                }
                slot
            })
            .collect();
        let pool = PrivatePagePool::new(&mut storage, 100, 100, 2).unwrap();
        let target = COUNT - 1;
        let target_pgno = u32::try_from(COUNT + 1).unwrap();
        assert_ne!(target, pool.index_root.get());

        let available_before = normalized_slots(&pool);
        let scalars_before = (
            pool.index_root.get(),
            pool.available_count.get(),
            pool.lowest_available.get(),
        );
        let checkpoint = pool.begin_checkpoint().unwrap();
        let claimed = pool.claim(target, PrivatePageOwner::Bitmap, 3, 7).unwrap();
        assert_eq!(pool.available_count.get(), 0);
        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(normalized_slots(&pool), available_before);
        assert_eq!(
            (
                pool.index_root.get(),
                pool.available_count.get(),
                pool.lowest_available.get(),
            ),
            scalars_before
        );
        let proof = verify_avl(&pool.slots.borrow(), pool.index_root.get(), None);
        assert_eq!(
            (proof.count, proof.available, proof.in_use),
            (COUNT, 1, COUNT - 1)
        );
        assert_eq!(pool.terminal_rebuild_visits.get(), 2 * COUNT);
        assert_eq!(
            pool.borrow_page(&claimed).unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );

        let authority = pool.claim_lowest(PrivatePageOwner::Bitmap, 3, 7).unwrap();
        assert_eq!(authority.page_number(), target_pgno);
        let in_use_before = normalized_slots(&pool);
        let checkpoint = pool.begin_checkpoint().unwrap();
        let transferred = pool
            .transfer(authority, PrivatePageOwner::Retirement, 4, 8)
            .unwrap();
        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(normalized_slots(&pool), in_use_before);
        let proof = verify_avl(&pool.slots.borrow(), pool.index_root.get(), None);
        assert_eq!((proof.available, proof.in_use), (0, COUNT));
        assert_eq!(pool.terminal_rebuild_visits.get(), 2 * COUNT);
        assert_eq!(
            pool.borrow_page(&transferred).unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );

        let authority = pool
            .authority(target_pgno, PrivatePageOwner::Bitmap, 3)
            .unwrap();
        let in_use_before = normalized_slots(&pool);
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.return_page(authority, PrivatePageReturn::Available)
            .unwrap();
        assert_eq!(pool.available_count.get(), 0);
        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(normalized_slots(&pool), in_use_before);
        let proof = verify_avl(&pool.slots.borrow(), pool.index_root.get(), None);
        assert_eq!((proof.available, proof.in_use), (0, COUNT));
        assert_eq!(pool.terminal_rebuild_visits.get(), 2 * COUNT);
    }

    #[test]
    fn rotated_scope_claim_transfer_and_return_rollback_restore_every_aggregate() {
        const COUNT: usize = 31;
        let mut storage: std::vec::Vec<_> =
            (0..COUNT).map(|_| PrivatePagePoolSlot::empty()).collect();
        let pool = vacant_pool(&mut storage, 1000, 1000);
        let scope = pool.reserve_scope(COUNT).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        for index in 0..COUNT {
            let pgno = u32::try_from((index * 17) % COUNT + 2).unwrap();
            pool.bind_page(
                &checkpoint,
                &scope,
                pgno,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        }
        pool.commit_checkpoint(checkpoint).unwrap();
        let (target, depth) = {
            let slots = pool.slots.borrow();
            deepest_avl_slot(&slots, slots[scope.anchor].scope_root, true)
        };
        assert!(depth > 2);
        assert_ne!(target, pool.index_root.get());
        assert_ne!(target, pool.slots.borrow()[scope.anchor].scope_root);
        let target_pgno = pool.slots.borrow()[target].pgno;

        let checkpoint = pool.begin_checkpoint().unwrap();
        for pgno in 2..u32::try_from(COUNT + 2).unwrap() {
            if pgno != target_pgno {
                let _ = pool
                    .claim_page_in_scope(&checkpoint, &scope, pgno, PrivatePageOwner::Bitmap, 3, 7)
                    .unwrap();
            }
        }
        pool.commit_checkpoint(checkpoint).unwrap();

        let available_before = normalized_slots(&pool);
        let scalars_before = (
            pool.index_root.get(),
            pool.available_count.get(),
            pool.lowest_available.get(),
            pool.slots.borrow()[scope.anchor].scope_root,
        );
        let checkpoint = pool.begin_checkpoint().unwrap();
        let claimed = pool
            .claim_page_in_scope(
                &checkpoint,
                &scope,
                target_pgno,
                PrivatePageOwner::Bitmap,
                3,
                7,
            )
            .unwrap();
        assert_eq!(pool.scoped_available(&scope).unwrap(), 0);
        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(normalized_slots(&pool), available_before);
        assert_eq!(
            (
                pool.index_root.get(),
                pool.available_count.get(),
                pool.lowest_available.get(),
                pool.slots.borrow()[scope.anchor].scope_root,
            ),
            scalars_before
        );
        let slots = pool.slots.borrow();
        let global = verify_avl(&slots, pool.index_root.get(), None);
        let scoped = verify_avl(&slots, slots[scope.anchor].scope_root, Some(scope.id));
        assert_eq!((global.available, global.in_use), (1, COUNT - 1));
        assert_eq!((scoped.available, scoped.in_use), (1, COUNT - 1));
        assert_eq!(
            PrivatePagePool::lowest_available_in_scope(&slots, slots[scope.anchor].scope_root,),
            Some(target)
        );
        drop(slots);
        assert_eq!(pool.terminal_rebuild_visits.get(), 3 * COUNT);
        assert_eq!(
            pool.borrow_page_in_scope(&scope, &claimed).unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );

        let checkpoint = pool.begin_checkpoint().unwrap();
        let authority = pool
            .claim_page_in_scope(
                &checkpoint,
                &scope,
                target_pgno,
                PrivatePageOwner::Bitmap,
                3,
                7,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let in_use_before = normalized_slots(&pool);
        let checkpoint = pool.begin_checkpoint().unwrap();
        let transferred = pool
            .transfer_in_scope(&scope, authority, PrivatePageOwner::Retirement, 4, 8)
            .unwrap();
        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(normalized_slots(&pool), in_use_before);
        let slots = pool.slots.borrow();
        let global = verify_avl(&slots, pool.index_root.get(), None);
        let scoped = verify_avl(&slots, slots[scope.anchor].scope_root, Some(scope.id));
        assert_eq!((global.available, global.in_use), (0, COUNT));
        assert_eq!((scoped.available, scoped.in_use), (0, COUNT));
        drop(slots);
        assert_eq!(pool.terminal_rebuild_visits.get(), 3 * COUNT);
        assert_eq!(
            pool.borrow_page_in_scope(&scope, &transferred).unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );

        let authority = pool
            .authority_in_scope(&scope, target_pgno, PrivatePageOwner::Bitmap, 3)
            .unwrap();
        let in_use_before = normalized_slots(&pool);
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.return_page_in_scope(&scope, authority, PrivatePageReturn::Available)
            .unwrap();
        assert_eq!(pool.scoped_available(&scope).unwrap(), 0);
        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(normalized_slots(&pool), in_use_before);
        let slots = pool.slots.borrow();
        let global = verify_avl(&slots, pool.index_root.get(), None);
        let scoped = verify_avl(&slots, slots[scope.anchor].scope_root, Some(scope.id));
        assert_eq!((global.available, global.in_use), (0, COUNT));
        assert_eq!((scoped.available, scoped.in_use), (0, COUNT));
        assert_eq!(pool.terminal_rebuild_visits.get(), 3 * COUNT);
    }

    #[test]
    fn binding_rollback_restores_bytes_aggregates_vacancy_and_pending_tail_exactly() {
        let mut storage = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(3).unwrap();
        let before_slots = normalized_slots(&pool);
        let before_scalars = (
            pool.index_root.get(),
            pool.authorized_len.get(),
            pool.available_count.get(),
            pool.lowest_available.get(),
            pool.pending_page_count(),
            pool.slots.borrow()[scope.anchor].scope_root,
            pool.slots.borrow()[scope.anchor].scope_vacant_head,
            pool.slots.borrow()[scope.anchor].scope_bound,
        );
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            7,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.bind_page(&checkpoint, &scope, 20, PrivatePageAuthorization::Appended)
            .unwrap();
        pool.bind_page(&checkpoint, &scope, 21, PrivatePageAuthorization::Appended)
            .unwrap();
        assert_eq!(pool.pending_page_count(), 22);
        pool.unbind_page(&checkpoint, &scope, 20).unwrap();
        assert_eq!(pool.pending_page_count(), 22);
        pool.unbind_page(&checkpoint, &scope, 21).unwrap();
        assert_eq!(pool.pending_page_count(), 21);
        let authority = pool
            .claim_lowest_in_scope(&checkpoint, &scope, PrivatePageOwner::Bitmap, 3, 7)
            .unwrap();
        pool.borrow_page_mut_in_scope(&scope, &authority).unwrap()[37] = 0xa5;

        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(normalized_slots(&pool), before_slots);
        let slots = pool.slots.borrow();
        assert_eq!(
            (
                pool.index_root.get(),
                pool.authorized_len.get(),
                pool.available_count.get(),
                pool.lowest_available.get(),
                pool.pending_page_count(),
                slots[scope.anchor].scope_root,
                slots[scope.anchor].scope_vacant_head,
                slots[scope.anchor].scope_bound,
            ),
            before_scalars
        );
        drop(slots);
        assert_eq!(
            pool.borrow_page_in_scope(&scope, &authority).unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );
    }

    #[test]
    fn unbind_rebind_invalidates_old_authority_without_invalidating_new_authority() {
        let mut storage = [PrivatePagePoolSlot::empty()];
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            7,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        let old = pool
            .claim_lowest_in_scope(&checkpoint, &scope, PrivatePageOwner::Bitmap, 3, 7)
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        pool.return_page_in_scope(&scope, old, PrivatePageReturn::Available)
            .unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let old = pool
            .claim_page_in_scope(&checkpoint, &scope, 7, PrivatePageOwner::Bitmap, 4, 7)
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let retained = retained_authority(&old);
        pool.return_page_in_scope(&scope, old, PrivatePageReturn::Available)
            .unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.unbind_page(&checkpoint, &scope, 7).unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            7,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        let current = pool
            .claim_page_in_scope(&checkpoint, &scope, 7, PrivatePageOwner::Bitmap, 5, 7)
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        assert_eq!(
            pool.borrow_page_in_scope(&scope, &retained).unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );
        assert_eq!(pool.borrow_page_in_scope(&scope, &current).unwrap()[0], 0);
    }

    #[test]
    fn scope_binding_and_checkpoint_headroom_failures_are_atomic() {
        assert_eq!(checked_next_pool_identity(1), Some(2));
        assert_eq!(checked_next_pool_identity(usize::MAX), None);
        let mut storage = [PrivatePagePoolSlot::empty()];
        let pool = vacant_pool(&mut storage, 20, 20);
        pool.scope_sequence.set(u64::MAX);
        let before = pool.slots.borrow().to_vec();
        assert_eq!(
            pool.reserve_scope(1).unwrap_err(),
            PrivatePagePoolError::ScopeIdentityExhausted
        );
        assert_eq!(&**pool.slots.borrow(), before.as_slice());
        pool.scope_sequence.set(0);
        let scope = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();

        pool.slots.borrow_mut()[0].binding_epoch = u64::MAX - 1;
        let before = pool.slots.borrow().to_vec();
        let before_epoch = pool.epoch.get();
        assert_eq!(
            pool.bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap_err(),
            PrivatePagePoolError::EpochExhausted
        );
        assert_eq!(&**pool.slots.borrow(), before.as_slice());
        assert_eq!(pool.epoch.get(), before_epoch);
        pool.slots.borrow_mut()[0].binding_epoch = 2;
        pool.epoch.set(u64::MAX - 1);
        let before = pool.slots.borrow().to_vec();
        assert_eq!(
            pool.bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap_err(),
            PrivatePagePoolError::EpochExhausted
        );
        assert_eq!(&**pool.slots.borrow(), before.as_slice());
        assert_eq!(pool.epoch.get(), u64::MAX - 1);
        pool.epoch.set(before_epoch);
        pool.rollback_checkpoint(checkpoint).unwrap();

        pool.active_scopes.set(0);
        let before = pool.slots.borrow().to_vec();
        assert_eq!(
            pool.close_scope(&scope).unwrap_err(),
            PrivatePagePoolError::ActiveScopeUnderflow
        );
        assert_eq!(&**pool.slots.borrow(), before.as_slice());
        pool.active_scopes.set(1);
        pool.close_scope(&scope).unwrap();

        pool.generation.set(u64::MAX);
        let before_epoch = pool.epoch.get();
        assert_eq!(
            pool.begin_checkpoint().unwrap_err(),
            PrivatePagePoolError::GenerationExhausted
        );
        assert_eq!(pool.epoch.get(), before_epoch);
        assert_eq!(pool.active_checkpoint.get(), 0);
    }

    #[test]
    fn checkpoint_cleanup_headroom_is_prospective_and_terminal() {
        {
            let mut storage = [PrivatePagePoolSlot::empty(), PrivatePagePoolSlot::empty()];
            let pool = vacant_pool(&mut storage, 20, 20);
            let scope = pool.reserve_scope(2).unwrap();
            let before = normalized_slots(&pool);
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.epoch.set(u64::MAX - 3);
            pool.bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
            let after_first = pool.slots.borrow().to_vec();
            let first_epoch = pool.epoch.get();
            assert_eq!(first_epoch, u64::MAX - 2);
            assert_eq!(pool.checkpoint_cleanup_slots.get(), 1);
            assert_eq!(
                pool.bind_page(
                    &checkpoint,
                    &scope,
                    8,
                    PrivatePageAuthorization::SafelyReclaimed,
                )
                .unwrap_err(),
                PrivatePagePoolError::EpochExhausted
            );
            assert_eq!(&**pool.slots.borrow(), after_first.as_slice());
            assert_eq!(pool.epoch.get(), first_epoch);
            assert_eq!(pool.checkpoint_cleanup_slots.get(), 1);
            pool.rollback_checkpoint(checkpoint).unwrap();
            assert_eq!(pool.epoch.get(), u64::MAX);
            assert_eq!(normalized_slots(&pool), before);
        }

        {
            let mut slot =
                PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::SafelyReclaimed);
            slot.preset_bitmap_page(3, 7, [0; PAGE_SIZE]);
            let mut storage = [slot];
            let pool = PrivatePagePool::new(&mut storage, 20, 20, 2).unwrap();
            pool.slots.borrow_mut()[0].binding_epoch = u64::MAX - 1;
            let authority = pool.authority(7, PrivatePageOwner::Bitmap, 3).unwrap();
            pool.epoch.set(u64::MAX - 4);
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.return_page(authority, PrivatePageReturn::Available)
                .unwrap();
            assert!(matches!(
                pool.state(0).unwrap(),
                PrivatePagePoolState::PendingReturn { .. }
            ));
            assert_eq!(pool.checkpoint_cleanup_slots.get(), 1);
            pool.commit_checkpoint(checkpoint).unwrap();
            assert_eq!(pool.epoch.get(), u64::MAX);
            assert_eq!(pool.slots.borrow()[0].binding_epoch, u64::MAX);
            assert_eq!(pool.state(0).unwrap(), PrivatePagePoolState::Available);
        }

        {
            let mut slot =
                PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::SafelyReclaimed);
            slot.preset_bitmap_page(3, 7, [0; PAGE_SIZE]);
            let mut storage = [slot];
            let pool = PrivatePagePool::new(&mut storage, 20, 20, 2).unwrap();
            pool.slots.borrow_mut()[0].binding_epoch = u64::MAX - 1;
            let before = pool.authority(7, PrivatePageOwner::Bitmap, 3).unwrap();
            pool.epoch.set(u64::MAX - 4);
            let checkpoint = pool.begin_checkpoint().unwrap();
            let rotated = pool.authority(7, PrivatePageOwner::Bitmap, 3).unwrap();
            pool.rollback_checkpoint(checkpoint).unwrap();
            assert_eq!(pool.epoch.get(), u64::MAX);
            assert_eq!(pool.slots.borrow()[0].binding_epoch, u64::MAX);
            assert_eq!(
                pool.borrow_page(&before).unwrap_err(),
                PrivatePagePoolError::StaleAuthority
            );
            assert_eq!(
                pool.borrow_page(&rotated).unwrap_err(),
                PrivatePagePoolError::StaleAuthority
            );
        }
    }

    #[test]
    fn move_authority_checkpoint_failures_return_exact_tokens_and_retry() {
        {
            let mut storage = slots();
            let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            let replay = retained_checkpoint(&checkpoint);
            let expected = checkpoint_fingerprint(&checkpoint);
            let before = pool_mutation_snapshot(&pool);
            let guard = pool.test_hold_slots_borrow();
            let (checkpoint, error) = pool.commit_checkpoint(checkpoint).unwrap_err();
            assert_eq!(error, PrivatePagePoolError::BorrowConflict);
            assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
            drop(guard);
            assert_eq!(pool_mutation_snapshot(&pool), before);

            let root = pool.index_root.get();
            let original_left = pool.slots.borrow()[root].index_left;
            pool.slots.borrow_mut()[root].index_left = root;
            let expected = checkpoint_fingerprint(&checkpoint);
            let before = pool_mutation_snapshot(&pool);
            let (checkpoint, error) = pool.commit_checkpoint(checkpoint).unwrap_err();
            assert_eq!(error, PrivatePagePoolError::CheckpointMismatch);
            assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
            assert_eq!(pool_mutation_snapshot(&pool), before);
            pool.slots.borrow_mut()[root].index_left = original_left;

            let ((), allocations) =
                count_thread_allocations(|| pool.commit_checkpoint(checkpoint).unwrap());
            assert_eq!(allocations, 0);
            let (_replay, error) = pool.commit_checkpoint(replay).unwrap_err();
            assert_eq!(error, PrivatePagePoolError::CheckpointMissing);
        }

        {
            let mut storage = slots();
            let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
            let authority = pool.claim_lowest(PrivatePageOwner::Bitmap, 4, 1).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.return_page(authority, PrivatePageReturn::Available)
                .unwrap();
            let replay = retained_checkpoint(&checkpoint);
            let expected = checkpoint_fingerprint(&checkpoint);
            let journal = pool.checkpoint_index_head.get();
            assert_ne!(journal, NO_SLOT);
            pool.checkpoint_index_head.set(pool.len());
            let before = pool_mutation_snapshot(&pool);
            let (checkpoint, error) = pool.rollback_checkpoint(checkpoint).unwrap_err();
            assert_eq!(error, PrivatePagePoolError::CheckpointMismatch);
            assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
            assert_eq!(pool_mutation_snapshot(&pool), before);
            pool.checkpoint_index_head.set(journal);
            let ((), allocations) =
                count_thread_allocations(|| pool.rollback_checkpoint(checkpoint).unwrap());
            assert_eq!(allocations, 0);
            let (_replay, error) = pool.rollback_checkpoint(replay).unwrap_err();
            assert_eq!(error, PrivatePagePoolError::CheckpointMissing);
        }

        {
            let mut storage = slots();
            let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
            let authority = pool.claim_lowest(PrivatePageOwner::Bitmap, 4, 1).unwrap();
            let slot = authority.slot;
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.return_page(authority, PrivatePageReturn::Available)
                .unwrap();
            let live_epoch = pool.epoch.get();
            let PrivatePageState::PendingReturn {
                owner,
                owner_generation,
                tag,
                authority_epoch,
                ..
            } = pool.slots.borrow()[slot].state
            else {
                panic!("return under checkpoint must remain pending")
            };
            pool.slots.borrow_mut()[slot].state = PrivatePageState::PendingReturn {
                owner,
                owner_generation,
                tag,
                authority_epoch,
                disposition: PrivatePageReturn::Tail,
            };
            let expected = checkpoint_fingerprint(&checkpoint);
            let before = pool_mutation_snapshot(&pool);
            let (checkpoint, error) = pool.commit_checkpoint(checkpoint).unwrap_err();
            assert_eq!(
                error,
                PrivatePagePoolError::AuthorizationMismatch {
                    pgno: 3,
                    authorization: PrivatePageAuthorization::CommittedFree,
                }
            );
            assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
            assert_eq!(pool_mutation_snapshot(&pool), before);
            pool.slots.borrow_mut()[slot].state = PrivatePageState::PendingReturn {
                owner,
                owner_generation,
                tag,
                authority_epoch,
                disposition: PrivatePageReturn::Available,
            };
            pool.test_set_epoch(u64::MAX);
            let expected = checkpoint_fingerprint(&checkpoint);
            let before = pool_mutation_snapshot(&pool);
            let (checkpoint, error) = pool.commit_checkpoint(checkpoint).unwrap_err();
            assert_eq!(error, PrivatePagePoolError::EpochExhausted);
            assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
            assert_eq!(pool_mutation_snapshot(&pool), before);
            pool.test_set_epoch(live_epoch);
            pool.commit_checkpoint(checkpoint).unwrap();
            assert_eq!(pool.state(slot).unwrap(), PrivatePagePoolState::Available);
        }

        {
            let mut storage = slots();
            let pool = PrivatePagePool::new(&mut storage, 20, 21, 2).unwrap();
            let authority = pool.claim_lowest(PrivatePageOwner::Bitmap, 4, 1).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.return_page(authority, PrivatePageReturn::Available)
                .unwrap();
            let live_epoch = pool.epoch.get();
            let root = pool.index_root.get();
            assert_eq!(
                pool.slots.borrow()[root].saved_index_generation,
                checkpoint.generation
            );
            let original_left = pool.slots.borrow()[root].saved_index_left;
            pool.slots.borrow_mut()[root].saved_index_left = root;
            let expected = checkpoint_fingerprint(&checkpoint);
            let before = pool_mutation_snapshot(&pool);
            let (checkpoint, error) = pool.rollback_checkpoint(checkpoint).unwrap_err();
            assert_eq!(error, PrivatePagePoolError::CheckpointMismatch);
            assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
            assert_eq!(pool_mutation_snapshot(&pool), before);
            pool.slots.borrow_mut()[root].saved_index_left = original_left;

            pool.test_set_epoch(u64::MAX);
            let expected = checkpoint_fingerprint(&checkpoint);
            let before = pool_mutation_snapshot(&pool);
            let (checkpoint, error) = pool.rollback_checkpoint(checkpoint).unwrap_err();
            assert_eq!(error, PrivatePagePoolError::EpochExhausted);
            assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
            assert_eq!(pool_mutation_snapshot(&pool), before);
            pool.test_set_epoch(live_epoch);
            pool.rollback_checkpoint(checkpoint).unwrap();
            assert!(matches!(
                pool.state(0).unwrap(),
                PrivatePagePoolState::InUse {
                    owner: PrivatePageOwner::Bitmap,
                    owner_generation: 4,
                    tag: 1,
                }
            ));
        }
    }

    #[test]
    fn move_authority_scoped_checkpoints_retain_tokens_across_all_preflight_failures() {
        let mut storage = [PrivatePagePoolSlot::empty(), PrivatePagePoolSlot::empty()];
        let pool = vacant_pool(&mut storage, 20, 20);
        let target = pool.reserve_scope(1).unwrap();
        let foreign = pool.reserve_scope(1).unwrap();

        let checkpoint = pool.begin_checkpoint().unwrap();
        let target_slot = pool
            .bind_page(
                &checkpoint,
                &target,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &foreign,
            8,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let authority = pool
            .claim_page_in_scope(&checkpoint, &target, 7, PrivatePageOwner::Bitmap, 4, 1)
            .unwrap();
        pool.commit_checkpoint_in_scope(checkpoint, &target)
            .unwrap();

        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.return_page_in_scope(&target, authority, PrivatePageReturn::Available)
            .unwrap();
        let live_epoch = pool.epoch.get();
        let replay = retained_checkpoint(&checkpoint);
        let expected = checkpoint_fingerprint(&checkpoint);
        let before = pool_mutation_snapshot(&pool);
        let (checkpoint, error) = pool
            .commit_checkpoint_in_scope(checkpoint, &foreign)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::CheckpointMismatch);
        assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
        assert_eq!(pool_mutation_snapshot(&pool), before);

        let journal = pool.checkpoint_index_head.get();
        assert_ne!(journal, NO_SLOT);
        pool.checkpoint_index_head.set(pool.len());
        let expected = checkpoint_fingerprint(&checkpoint);
        let before = pool_mutation_snapshot(&pool);
        let (checkpoint, error) = pool
            .commit_checkpoint_in_scope(checkpoint, &target)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::CheckpointMismatch);
        assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
        assert_eq!(pool_mutation_snapshot(&pool), before);
        pool.checkpoint_index_head.set(journal);

        pool.test_set_epoch(u64::MAX);
        let expected = checkpoint_fingerprint(&checkpoint);
        let before = pool_mutation_snapshot(&pool);
        let (checkpoint, error) = pool
            .commit_checkpoint_in_scope(checkpoint, &target)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::EpochExhausted);
        assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
        assert_eq!(pool_mutation_snapshot(&pool), before);
        pool.test_set_epoch(live_epoch);
        let ((), allocations) = count_thread_allocations(|| {
            pool.commit_checkpoint_in_scope(checkpoint, &target)
                .unwrap();
        });
        assert_eq!(allocations, 0);
        let (_replay, error) = pool
            .commit_checkpoint_in_scope(replay, &target)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::CheckpointMissing);
        assert_eq!(
            pool.scoped_slot_info(&target, target_slot)
                .unwrap()
                .unwrap()
                .state,
            PrivatePagePoolState::Available
        );

        let checkpoint = pool.begin_checkpoint().unwrap();
        let _authority = pool
            .claim_page_in_scope(&checkpoint, &target, 7, PrivatePageOwner::Bitmap, 5, 2)
            .unwrap();
        let live_epoch = pool.epoch.get();
        let replay = retained_checkpoint(&checkpoint);
        let expected = checkpoint_fingerprint(&checkpoint);
        let before = pool_mutation_snapshot(&pool);
        let guard = pool.test_hold_slots_borrow();
        let (checkpoint, error) = pool
            .rollback_checkpoint_in_scope(checkpoint, &target)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::BorrowConflict);
        assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
        drop(guard);
        assert_eq!(pool_mutation_snapshot(&pool), before);

        let journal_count = pool.checkpoint_index_count.get();
        pool.checkpoint_index_count.set(journal_count + 1);
        let expected = checkpoint_fingerprint(&checkpoint);
        let before = pool_mutation_snapshot(&pool);
        let (checkpoint, error) = pool
            .rollback_checkpoint_in_scope(checkpoint, &target)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::CheckpointMismatch);
        assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
        assert_eq!(pool_mutation_snapshot(&pool), before);
        pool.checkpoint_index_count.set(journal_count);

        pool.test_set_epoch(u64::MAX);
        let expected = checkpoint_fingerprint(&checkpoint);
        let before = pool_mutation_snapshot(&pool);
        let (checkpoint, error) = pool
            .rollback_checkpoint_in_scope(checkpoint, &target)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::EpochExhausted);
        assert_eq!(checkpoint_fingerprint(&checkpoint), expected);
        assert_eq!(pool_mutation_snapshot(&pool), before);
        pool.test_set_epoch(live_epoch);
        let ((), allocations) = count_thread_allocations(|| {
            pool.rollback_checkpoint_in_scope(checkpoint, &target)
                .unwrap();
        });
        assert_eq!(allocations, 0);
        let (_replay, error) = pool
            .rollback_checkpoint_in_scope(replay, &target)
            .unwrap_err();
        assert_eq!(error, PrivatePagePoolError::CheckpointMissing);
        assert_eq!(
            pool.scoped_slot_info(&target, target_slot)
                .unwrap()
                .unwrap()
                .state,
            PrivatePagePoolState::Available
        );
    }

    #[test]
    fn appended_holes_do_not_shrink_but_exact_suffix_unbind_does() {
        let mut storage = [PrivatePagePoolSlot::empty(), PrivatePagePoolSlot::empty()];
        let pool = vacant_pool(&mut storage, 20, 22);
        let scope = pool.reserve_scope(2).unwrap();
        let before = normalized_slots(&pool);
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(&checkpoint, &scope, 20, PrivatePageAuthorization::Appended)
            .unwrap();
        assert_eq!(pool.pending_page_count(), 22);
        pool.bind_page(&checkpoint, &scope, 22, PrivatePageAuthorization::Appended)
            .unwrap();
        assert_eq!(pool.pending_page_count(), 23);
        pool.unbind_page(&checkpoint, &scope, 20).unwrap();
        assert_eq!(pool.pending_page_count(), 23);
        pool.unbind_page(&checkpoint, &scope, 22).unwrap();
        assert_eq!(pool.pending_page_count(), 22);
        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(normalized_slots(&pool), before);
    }

    #[test]
    fn reserve_bind_unbind_rollback_use_no_heap() {
        let mut storage = [PrivatePagePoolSlot::empty()];
        let pool = vacant_pool(&mut storage, 20, 20);
        let (scope, allocations) = count_thread_allocations(|| pool.reserve_scope(1).unwrap());
        assert_eq!(allocations, 0);
        let ((), allocations) = count_thread_allocations(|| {
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
            pool.unbind_page(&checkpoint, &scope, 7).unwrap();
            pool.rollback_checkpoint(checkpoint).unwrap();
        });
        assert_eq!(allocations, 0);
    }

    #[test]
    fn active_operation_registry_blocks_mutations_and_seals_prepared_state() {
        let mut storage = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(1).unwrap();
        let other = pool.reserve_scope(1).unwrap();
        let commitment = pool.exact_commitment(&scope).unwrap();
        let checkpoint = pool.preflight_checkpoint().unwrap();
        let mut plan = [];
        let operation = pool
            .preflight_operation_in_scope(&scope, 0, &mut plan)
            .unwrap();

        assert_eq!(
            pool.validate_exact_commitment(&scope, &commitment),
            Err(PrivatePagePoolError::StaleAuthority)
        );
        assert_eq!(
            pool.begin_checkpoint().unwrap_err(),
            PrivatePagePoolError::OperationActive
        );
        let mut concurrent_plan = [];
        assert_eq!(
            pool.preflight_operation_in_scope(&scope, 0, &mut concurrent_plan)
                .unwrap_err(),
            PrivatePagePoolError::OperationActive
        );
        assert_eq!(
            pool.reserve_scope(1).unwrap_err(),
            PrivatePagePoolError::OperationActive
        );
        assert_eq!(
            pool.close_scope(&other),
            Err(PrivatePagePoolError::OperationActive)
        );
        assert_eq!(
            pool.release_generation_in_scope(&scope, PrivatePageOwner::Bitmap, 2, 0),
            Err(PrivatePagePoolError::OperationActive)
        );
        assert_eq!(
            pool.preflight_mutation_in_scope(&scope, &commitment, 0),
            Err(PrivatePagePoolError::OperationActive)
        );

        let replay = retained_operation(&operation);
        pool.finish_operation_in_scope(operation).unwrap();
        assert!(!pool.has_active_operation());
        assert_eq!(
            pool.begin_checkpoint_prepared(&checkpoint),
            Err(PrivatePagePoolError::CheckpointMismatch)
        );
        let (_replay, error) = pool.finish_operation_in_scope(replay).unwrap_err();
        assert_eq!(error, PrivatePagePoolError::OperationMissing);

        let mut abandon_plan = [];
        let ((), allocations) = count_thread_allocations(|| {
            let operation = pool
                .preflight_operation_in_scope(&scope, 0, &mut abandon_plan)
                .unwrap();
            pool.abandon_unmutated_operation(operation).unwrap();
        });
        assert_eq!(allocations, 0);
        assert!(!pool.has_active_operation());
        pool.close_scope(&other).unwrap();
    }

    #[test]
    fn forged_old_or_malformed_tokens_cannot_poison_or_clear_current_operation() {
        let mut storage = [PrivatePagePoolSlot::empty()];
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(1).unwrap();
        let mut first_plan = [];
        let first = pool
            .preflight_operation_in_scope(&scope, 0, &mut first_plan)
            .unwrap();
        let old = retained_operation(&first);
        pool.abandon_unmutated_operation(first).unwrap();

        let mut current_plan = [];
        let current = pool
            .preflight_operation_in_scope(&scope, 0, &mut current_plan)
            .unwrap();
        old.used_mutation_steps.set(1);
        let (_old, error) = pool.abandon_unmutated_operation(old).unwrap_err();
        assert_eq!(error, PrivatePagePoolError::StaleAuthority);
        assert!(pool.has_active_operation());
        assert!(!pool.requires_abort());

        let mut forged_id = retained_operation(&current);
        forged_id.id += 1;
        assert_eq!(
            pool.finish_operation_in_scope(forged_id).unwrap_err().1,
            PrivatePagePoolError::StaleAuthority
        );
        let mut forged_generation = retained_operation(&current);
        forged_generation.generation += 1;
        assert_eq!(
            pool.finish_operation_in_scope(forged_generation)
                .unwrap_err()
                .1,
            PrivatePagePoolError::StaleAuthority
        );
        let mut forged_start = retained_operation(&current);
        forged_start.start_epoch += 1;
        assert_eq!(
            pool.finish_operation_in_scope(forged_start).unwrap_err().1,
            PrivatePagePoolError::StaleAuthority
        );
        let mut forged_scope = retained_operation(&current);
        forged_scope.scope_id += 1;
        assert_eq!(
            pool.finish_operation_in_scope(forged_scope).unwrap_err().1,
            PrivatePagePoolError::StaleScope
        );
        let forged_counter = retained_operation(&current);
        forged_counter.used_mutation_steps.set(1);
        assert_eq!(
            pool.finish_operation_in_scope(forged_counter)
                .unwrap_err()
                .1,
            PrivatePagePoolError::InvalidState(scope.anchor)
        );
        assert!(pool.has_active_operation());
        assert!(!pool.requires_abort());
        pool.abandon_unmutated_operation(current).unwrap();
        assert!(!pool.has_active_operation());
    }

    #[test]
    fn composite_bind_rejects_active_poisoned_and_checkpointed_stage_pool() {
        let mut target_storage = [PrivatePagePoolSlot::empty()];
        let target = vacant_pool(&mut target_storage, 20, 20);
        let target_scope = target.reserve_scope(1).unwrap();
        let target_slot = exact_scope_order::<1>(&target, &target_scope)[0];
        let bindings = [PrivatePageCompositeBind {
            pool_slot: target_slot,
            pgno: 7,
            authorization: PrivatePageAuthorization::SafelyReclaimed,
            state: PrivatePageCompositeBindState::Available,
        }];

        let mut stage_storage = [PrivatePagePoolSlot::empty()];
        let stage = vacant_pool(&mut stage_storage, 20, 20);
        let stage_scope = stage.reserve_scope(1).unwrap();
        let mut operation_plan = [];
        let operation = stage
            .preflight_operation_in_scope(&stage_scope, 0, &mut operation_plan)
            .unwrap();
        assert_eq!(
            target
                .prepare_composite_scope_bind(
                    &target_scope,
                    target.exact_commitment(&target_scope).unwrap(),
                    &stage,
                    &stage_scope,
                    &bindings,
                )
                .unwrap_err(),
            PrivatePagePoolError::OperationActive
        );
        stage.abandon_unmutated_operation(operation).unwrap();

        let prepared = target
            .prepare_composite_scope_bind(
                &target_scope,
                target.exact_commitment(&target_scope).unwrap(),
                &stage,
                &stage_scope,
                &bindings,
            )
            .unwrap();
        let mut operation_plan = [];
        let operation = stage
            .preflight_operation_in_scope(&stage_scope, 0, &mut operation_plan)
            .unwrap();
        assert_eq!(
            target.apply_prepared_composite_scope_bind(prepared, &stage, &stage_scope),
            Err(PrivatePagePoolError::OperationActive)
        );
        stage.abandon_unmutated_operation(operation).unwrap();

        let prepared = target
            .prepare_composite_scope_bind(
                &target_scope,
                target.exact_commitment(&target_scope).unwrap(),
                &stage,
                &stage_scope,
                &bindings,
            )
            .unwrap();
        let checkpoint = stage.begin_checkpoint().unwrap();
        assert_eq!(
            target.apply_prepared_composite_scope_bind(prepared, &stage, &stage_scope),
            Err(PrivatePagePoolError::CheckpointActive)
        );
        stage.rollback_checkpoint(checkpoint).unwrap();

        stage.abort_required.set(true);
        assert_eq!(
            target
                .prepare_composite_scope_bind(
                    &target_scope,
                    target.exact_commitment(&target_scope).unwrap(),
                    &stage,
                    &stage_scope,
                    &bindings,
                )
                .unwrap_err(),
            PrivatePagePoolError::AbortRequired
        );
    }

    #[test]
    fn selective_shadow_mutations_reject_active_checkpoint_without_rollback_drift() {
        let mut storage = [PrivatePagePoolSlot::empty()];
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let slot = pool
            .bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let before = pool.scoped_slot_info(&scope, slot).unwrap().unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let active = pool_mutation_snapshot(&pool);
        assert_eq!(
            pool.shadow_claim_and_write(
                &scope,
                slot,
                PrivatePageOwner::Bitmap,
                2,
                0,
                &[1; PAGE_SIZE],
            ),
            Err(PrivatePagePoolError::CheckpointActive)
        );
        assert_eq!(
            pool.shadow_write(&scope, slot, PrivatePageOwner::Bitmap, 2, &[2; PAGE_SIZE],),
            Err(PrivatePagePoolError::CheckpointActive)
        );
        assert_eq!(
            pool.shadow_return(
                &scope,
                slot,
                PrivatePageOwner::Bitmap,
                2,
                PrivatePageReturn::Available,
            ),
            Err(PrivatePagePoolError::CheckpointActive)
        );
        assert_eq!(pool_mutation_snapshot(&pool), active);
        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(
            pool.scoped_slot_info(&scope, slot).unwrap().unwrap(),
            before
        );
    }

    #[test]
    fn scoped_operation_rejects_reordered_and_duplicate_steps_before_mutation() {
        let mut storage = [PrivatePagePoolSlot::empty()];
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let slot = pool
            .bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let info = pool.scoped_slot_info(&scope, slot).unwrap().unwrap();
        let mut planned = [PrivatePageScopedOperationSlot::new(
            slot,
            info.binding_epoch,
            1,
        )];
        let operation = pool
            .preflight_operation_in_scope(&scope, 2, &mut planned)
            .unwrap();
        let before = (pool.slots.borrow().to_vec(), pool.epoch.get());
        assert_eq!(
            pool.write_slot_for_operation_in_scope_prepared(
                &operation,
                slot,
                PrivatePageOwner::Bitmap,
                2,
                &[7; PAGE_SIZE],
            )
            .unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );
        assert_eq!((pool.slots.borrow().to_vec(), pool.epoch.get()), before);

        let binding_epoch = pool
            .claim_slot_for_operation_in_scope_prepared(
                &operation,
                slot,
                PrivatePageOwner::Bitmap,
                2,
                9,
            )
            .unwrap();
        pool.write_slot_for_operation_in_scope_prepared(
            &operation,
            slot,
            PrivatePageOwner::Bitmap,
            2,
            &[7; PAGE_SIZE],
        )
        .unwrap();
        pool.finish_operation_in_scope(operation).unwrap();
        let info = pool.scoped_slot_info(&scope, slot).unwrap().unwrap();
        assert_eq!(info.binding_epoch, binding_epoch);
        assert_eq!(
            info.state,
            PrivatePagePoolState::InUse {
                owner: PrivatePageOwner::Bitmap,
                owner_generation: 2,
                tag: 9,
            }
        );
        assert_eq!(pool.test_bytes(slot).unwrap(), [7; PAGE_SIZE]);

        let mut duplicate = [
            PrivatePageScopedOperationSlot::new(slot, info.binding_epoch, 0),
            PrivatePageScopedOperationSlot::new(slot, info.binding_epoch, 0),
        ];
        let before = (pool.slots.borrow().to_vec(), pool.epoch.get());
        assert_eq!(
            pool.preflight_operation_in_scope(&scope, 0, &mut duplicate)
                .unwrap_err(),
            PrivatePagePoolError::InvalidState(slot)
        );
        assert_eq!((pool.slots.borrow().to_vec(), pool.epoch.get()), before);

        let mut release_plan = [PrivatePageScopedOperationSlot::new(
            slot,
            info.binding_epoch,
            1,
        )];
        let release = pool
            .preflight_operation_in_scope(&scope, 1, &mut release_plan)
            .unwrap();
        let released_epoch = pool
            .return_slot_for_operation_in_scope_prepared(
                &release,
                slot,
                PrivatePageOwner::Bitmap,
                2,
                PrivatePageReturn::Available,
            )
            .unwrap();
        pool.finish_operation_in_scope(release).unwrap();
        let released = pool.scoped_slot_info(&scope, slot).unwrap().unwrap();
        assert_eq!(released.binding_epoch, released_epoch);
        assert_eq!(released.state, PrivatePagePoolState::Available);
        assert_eq!(pool.test_bytes(slot).unwrap(), [0; PAGE_SIZE]);
    }

    #[test]
    fn scoped_operation_duplicate_validation_is_linear_at_4096_slots() {
        const COUNT: usize = 4096;
        let mut storage = std::vec![PrivatePagePoolSlot::empty(); COUNT];
        let pool = vacant_pool(&mut storage, COUNT as u64 + 2, COUNT as u64 + 2);
        let scope = pool.reserve_scope(COUNT).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let mut plan: std::vec::Vec<_> = (0..COUNT)
            .map(|_| PrivatePageScopedOperationSlot::new(NO_SLOT, 0, 0))
            .collect();
        for (ordinal, planned) in plan.iter_mut().enumerate() {
            let slot = pool
                .bind_page(
                    &checkpoint,
                    &scope,
                    u32::try_from(ordinal + 2).unwrap(),
                    PrivatePageAuthorization::SafelyReclaimed,
                )
                .unwrap();
            let info = pool.scoped_slot_info(&scope, slot).unwrap().unwrap();
            *planned = PrivatePageScopedOperationSlot::new(slot, info.binding_epoch, 0);
        }
        pool.commit_checkpoint(checkpoint).unwrap();

        let operation = pool
            .preflight_operation_in_scope(&scope, 0, &mut plan)
            .unwrap();
        assert!(
            pool.scoped_operation_duplicate_probes.get() <= COUNT,
            "duplicate proof scanned prior entries: probes={}",
            pool.scoped_operation_duplicate_probes.get()
        );
        pool.finish_operation_in_scope(operation).unwrap();
    }

    #[test]
    fn move_authority_operation_failures_retain_registration_and_poison_after_mutation() {
        let mut storage = [PrivatePagePoolSlot::empty()];
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let slot = pool
            .bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let info = pool.scoped_slot_info(&scope, slot).unwrap().unwrap();
        let mut plan = [PrivatePageScopedOperationSlot::new(
            slot,
            info.binding_epoch,
            1,
        )];
        let operation = pool
            .preflight_operation_in_scope(&scope, 1, &mut plan)
            .unwrap();
        let expected = operation_fingerprint(&operation);
        let before = pool_mutation_snapshot(&pool);
        let (operation, error) = pool.finish_operation_in_scope(operation).unwrap_err();
        assert_eq!(error, PrivatePagePoolError::InvalidState(scope.anchor));
        assert_eq!(operation_fingerprint(&operation), expected);
        assert_eq!(pool_mutation_snapshot(&pool), before);

        let guard = pool.test_hold_slots_borrow_mut();
        let expected = operation_fingerprint(&operation);
        let (operation, error) = pool.finish_operation_in_scope(operation).unwrap_err();
        assert_eq!(error, PrivatePagePoolError::BorrowConflict);
        assert_eq!(operation_fingerprint(&operation), expected);
        drop(guard);
        assert_eq!(pool_mutation_snapshot(&pool), before);

        pool.claim_slot_for_operation_in_scope_prepared(
            &operation,
            slot,
            PrivatePageOwner::Bitmap,
            4,
            9,
        )
        .unwrap();
        let exact = retained_operation(&operation);
        assert_eq!(
            pool.write_slot_for_operation_in_scope_prepared(
                &operation,
                usize::MAX,
                PrivatePageOwner::Bitmap,
                4,
                &[0; PAGE_SIZE],
            ),
            Err(PrivatePagePoolError::SlotOutOfBounds(usize::MAX))
        );
        assert!(pool.requires_abort());
        let mut operation = operation;
        operation.start_epoch = u64::MAX - 1;
        let expected = operation_fingerprint(&operation);
        let (operation, error) = pool.finish_operation_in_scope(operation).unwrap_err();
        assert_eq!(error, PrivatePagePoolError::StaleAuthority);
        assert_eq!(operation_fingerprint(&operation), expected);
        assert!(pool.has_active_operation());
        assert!(pool.requires_abort());
        let (_operation, error) = pool.finish_operation_in_scope(exact).unwrap_err();
        assert_eq!(error, PrivatePagePoolError::AbortRequired);
        assert!(matches!(
            pool.scoped_slot_info(&scope, slot).unwrap().unwrap().state,
            PrivatePagePoolState::InUse {
                owner: PrivatePageOwner::Bitmap,
                owner_generation: 4,
                tag: 9,
            }
        ));
    }

    #[test]
    fn scoped_operation_rejects_stale_cross_scope_and_forged_authority_atomically() {
        let mut storage = [PrivatePagePoolSlot::empty(), PrivatePagePoolSlot::empty()];
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(1).unwrap();
        let foreign = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let slot = pool
            .bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let info = pool.scoped_slot_info(&scope, slot).unwrap().unwrap();

        let mut cross = [PrivatePageScopedOperationSlot::new(
            slot,
            info.binding_epoch,
            1,
        )];
        let before = (pool.slots.borrow().to_vec(), pool.epoch.get());
        assert_eq!(
            pool.preflight_operation_in_scope(&foreign, 1, &mut cross)
                .unwrap_err(),
            PrivatePagePoolError::ScopeMismatch(7)
        );
        assert_eq!((pool.slots.borrow().to_vec(), pool.epoch.get()), before);

        let mut stale_plan = [PrivatePageScopedOperationSlot::new(
            slot,
            info.binding_epoch,
            1,
        )];
        let stale = pool
            .preflight_operation_in_scope(&scope, 1, &mut stale_plan)
            .unwrap();
        let replay = retained_operation(&stale);
        assert_eq!(
            pool.begin_checkpoint().unwrap_err(),
            PrivatePagePoolError::OperationActive
        );
        pool.abandon_unmutated_operation(stale).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.claim_page_in_scope(&checkpoint, &scope, 7, PrivatePageOwner::Bitmap, 2, 0)
            .unwrap();
        pool.rollback_checkpoint(checkpoint).unwrap();
        let before = (pool.slots.borrow().to_vec(), pool.epoch.get());
        assert_eq!(
            pool.claim_slot_for_operation_in_scope_prepared(
                &replay,
                slot,
                PrivatePageOwner::Bitmap,
                2,
                0,
            )
            .unwrap_err(),
            PrivatePagePoolError::OperationMissing
        );
        assert_eq!((pool.slots.borrow().to_vec(), pool.epoch.get()), before);

        let forged_plan = [PrivatePageScopedOperationSlot::new(
            slot,
            pool.scoped_slot_info(&scope, slot)
                .unwrap()
                .unwrap()
                .binding_epoch,
            1,
        )];
        let forged = PrivatePageScopedOperation {
            pool_identity: pool.identity.wrapping_add(1),
            pool_epoch: pool.identity_epoch,
            id: 1,
            pending_txn: pool.pending_txn,
            generation: pool.generation.get() + 1,
            scope_id: scope.id,
            scope_anchor: scope.anchor,
            start_epoch: pool.epoch.get(),
            mutation_steps: 1,
            used_mutation_steps: Cell::new(0),
            slots: &forged_plan,
        };
        let before = (pool.slots.borrow().to_vec(), pool.epoch.get());
        assert_eq!(
            pool.claim_slot_for_operation_in_scope_prepared(
                &forged,
                slot,
                PrivatePageOwner::Bitmap,
                2,
                0,
            )
            .unwrap_err(),
            PrivatePagePoolError::PoolMismatch
        );
        assert_eq!((pool.slots.borrow().to_vec(), pool.epoch.get()), before);
    }

    #[test]
    fn scoped_operation_preflights_global_and_terminal_slot_headroom() {
        let mut storage = [PrivatePagePoolSlot::empty()];
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        let slot = pool
            .bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let info = pool.scoped_slot_info(&scope, slot).unwrap().unwrap();
        pool.epoch.set(u64::MAX - 2);
        let mut plan = [PrivatePageScopedOperationSlot::new(
            slot,
            info.binding_epoch,
            1,
        )];
        let before = (pool.slots.borrow().to_vec(), pool.epoch.get());
        assert_eq!(
            pool.preflight_operation_in_scope(&scope, 2, &mut plan)
                .unwrap_err(),
            PrivatePagePoolError::EpochExhausted
        );
        assert_eq!((pool.slots.borrow().to_vec(), pool.epoch.get()), before);

        pool.epoch.set(1);
        pool.slots.borrow_mut()[slot].binding_epoch = u64::MAX - 1;
        let mut plan = [PrivatePageScopedOperationSlot::new(slot, u64::MAX - 1, 1)];
        let before = (pool.slots.borrow().to_vec(), pool.epoch.get());
        assert_eq!(
            pool.preflight_operation_in_scope(&scope, 1, &mut plan)
                .unwrap_err(),
            PrivatePagePoolError::EpochExhausted
        );
        assert_eq!((pool.slots.borrow().to_vec(), pool.epoch.get()), before);
    }

    #[test]
    fn reserve_preflights_the_first_unselected_successor_and_forward_boundary() {
        #[derive(Clone, Copy, Debug)]
        enum Corruption {
            PageNumber,
            AllocationGeneration,
            AdapterOwner,
            AdapterTag,
            Bytes,
            StaleScopeHeader,
            ForwardOutOfBounds,
            ForwardBacklink,
            ForwardPayload,
        }

        for corruption in [
            Corruption::PageNumber,
            Corruption::AllocationGeneration,
            Corruption::AdapterOwner,
            Corruption::AdapterTag,
            Corruption::Bytes,
            Corruption::StaleScopeHeader,
            Corruption::ForwardOutOfBounds,
            Corruption::ForwardBacklink,
            Corruption::ForwardPayload,
        ] {
            let mut storage: std::vec::Vec<_> =
                (0..6).map(|_| PrivatePagePoolSlot::empty()).collect();
            let pool = vacant_pool(&mut storage, 20, 20);
            {
                let mut slots = pool.slots.borrow_mut();
                match corruption {
                    Corruption::PageNumber => slots[2].pgno = 7,
                    Corruption::AllocationGeneration => slots[2].allocation_generation = 1,
                    Corruption::AdapterOwner => {
                        slots[2].adapter_owner = Some(PrivatePageOwner::Bitmap);
                    }
                    Corruption::AdapterTag => slots[2].adapter_tag = 1,
                    Corruption::Bytes => slots[2].bytes[0] = 1,
                    Corruption::StaleScopeHeader => slots[2].scope_root = 0,
                    Corruption::ForwardOutOfBounds => slots[2].unscoped_vacant_next = 6,
                    Corruption::ForwardBacklink => slots[3].unscoped_vacant_prev = 1,
                    Corruption::ForwardPayload => slots[3].bytes[0] = 1,
                }
            }
            let before = pool_mutation_snapshot(&pool);
            assert!(pool.reserve_scope(2).is_err(), "{corruption:?}");
            assert_eq!(pool_mutation_snapshot(&pool), before, "{corruption:?}");
        }
    }

    #[test]
    fn authorize_preflights_every_neighbor_that_survives_removal() {
        #[derive(Clone, Copy, Debug)]
        enum Corruption {
            HeadNextPayload,
            HeadNextForwardBacklink,
            TailPreviousPayload,
            TailPreviousBackwardLink,
            MiddlePreviousPayload,
            MiddlePreviousBackwardLink,
            MiddleNextPayload,
            MiddleNextForwardBacklink,
        }

        for corruption in [
            Corruption::HeadNextPayload,
            Corruption::HeadNextForwardBacklink,
            Corruption::TailPreviousPayload,
            Corruption::TailPreviousBackwardLink,
            Corruption::MiddlePreviousPayload,
            Corruption::MiddlePreviousBackwardLink,
            Corruption::MiddleNextPayload,
            Corruption::MiddleNextForwardBacklink,
        ] {
            let mut storage: std::vec::Vec<_> =
                (0..6).map(|_| PrivatePagePoolSlot::empty()).collect();
            let pool = vacant_pool(&mut storage, 20, 20);
            match corruption {
                Corruption::TailPreviousPayload | Corruption::TailPreviousBackwardLink => {
                    let scope = pool.reserve_scope(1).unwrap();
                    pool.close_scope(&scope).unwrap();
                }
                Corruption::MiddlePreviousPayload
                | Corruption::MiddlePreviousBackwardLink
                | Corruption::MiddleNextPayload
                | Corruption::MiddleNextForwardBacklink => {
                    let scope = pool.reserve_scope(3).unwrap();
                    pool.close_scope(&scope).unwrap();
                }
                Corruption::HeadNextPayload | Corruption::HeadNextForwardBacklink => {}
            }
            {
                let mut slots = pool.slots.borrow_mut();
                match corruption {
                    Corruption::HeadNextPayload => slots[1].bytes[0] = 1,
                    Corruption::HeadNextForwardBacklink => {
                        slots[2].unscoped_vacant_prev = 0;
                    }
                    Corruption::TailPreviousPayload => slots[5].bytes[0] = 1,
                    Corruption::TailPreviousBackwardLink => {
                        slots[4].unscoped_vacant_next = 3;
                    }
                    Corruption::MiddlePreviousPayload => slots[5].bytes[0] = 1,
                    Corruption::MiddlePreviousBackwardLink => {
                        slots[4].unscoped_vacant_next = 3;
                    }
                    Corruption::MiddleNextPayload => slots[1].bytes[0] = 1,
                    Corruption::MiddleNextForwardBacklink => {
                        slots[2].unscoped_vacant_prev = 0;
                    }
                }
            }
            let before = pool_mutation_snapshot(&pool);
            assert!(
                pool.authorize(0, 2, PrivatePageAuthorization::CommittedFree)
                    .is_err(),
                "{corruption:?}"
            );
            assert_eq!(pool_mutation_snapshot(&pool), before, "{corruption:?}");
        }
    }

    #[test]
    fn bind_preflights_the_scope_vacancy_successor_before_mutation() {
        #[derive(Clone, Copy, Debug)]
        enum Corruption {
            CurrentNextOutOfBounds,
            SuccessorPayload,
            SuccessorIdentity,
            SuccessorHeader,
            SuccessorMarker,
            SuccessorTrailing,
        }

        for corruption in [
            Corruption::CurrentNextOutOfBounds,
            Corruption::SuccessorPayload,
            Corruption::SuccessorIdentity,
            Corruption::SuccessorHeader,
            Corruption::SuccessorMarker,
            Corruption::SuccessorTrailing,
        ] {
            let mut storage: std::vec::Vec<_> =
                (0..6).map(|_| PrivatePagePoolSlot::empty()).collect();
            let pool = vacant_pool(&mut storage, 20, 20);
            let scope = pool.reserve_scope(2).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            {
                let mut slots = pool.slots.borrow_mut();
                match corruption {
                    Corruption::CurrentNextOutOfBounds => slots[0].scope_vacant_next = 6,
                    Corruption::SuccessorPayload => slots[1].bytes[0] = 1,
                    Corruption::SuccessorIdentity => slots[1].scope_id = scope.id + 1,
                    Corruption::SuccessorHeader => slots[1].scope_root = 0,
                    Corruption::SuccessorMarker => slots[1].scope_validation_marker = 1,
                    Corruption::SuccessorTrailing => slots[1].scope_vacant_next = 2,
                }
            }
            let before = pool_mutation_snapshot(&pool);
            assert!(
                pool.bind_page(
                    &checkpoint,
                    &scope,
                    7,
                    PrivatePageAuthorization::SafelyReclaimed,
                )
                .is_err(),
                "{corruption:?}"
            );
            assert_eq!(pool_mutation_snapshot(&pool), before, "{corruption:?}");
        }
    }

    #[test]
    fn unbind_preflights_the_existing_scope_vacancy_head_before_mutation() {
        #[derive(Clone, Copy, Debug)]
        enum Corruption {
            HeadPayload,
            HeadIdentity,
            HeadHeader,
            HeadMarker,
            HeadNextOutOfBounds,
            SuccessorPayload,
            SuccessorIdentity,
            SuccessorTrailing,
        }

        for corruption in [
            Corruption::HeadPayload,
            Corruption::HeadIdentity,
            Corruption::HeadHeader,
            Corruption::HeadMarker,
            Corruption::HeadNextOutOfBounds,
            Corruption::SuccessorPayload,
            Corruption::SuccessorIdentity,
            Corruption::SuccessorTrailing,
        ] {
            let mut storage: std::vec::Vec<_> =
                (0..6).map(|_| PrivatePagePoolSlot::empty()).collect();
            let pool = vacant_pool(&mut storage, 20, 20);
            let scope = pool.reserve_scope(3).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.bind_page(
                &checkpoint,
                &scope,
                7,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
            {
                let mut slots = pool.slots.borrow_mut();
                match corruption {
                    Corruption::HeadPayload => slots[1].bytes[0] = 1,
                    Corruption::HeadIdentity => slots[1].scope_id = scope.id + 1,
                    Corruption::HeadHeader => slots[1].scope_root = 0,
                    Corruption::HeadMarker => slots[1].scope_validation_marker = 1,
                    Corruption::HeadNextOutOfBounds => slots[1].scope_vacant_next = 6,
                    Corruption::SuccessorPayload => slots[2].bytes[0] = 1,
                    Corruption::SuccessorIdentity => slots[2].scope_id = scope.id + 1,
                    Corruption::SuccessorTrailing => slots[2].scope_vacant_next = 3,
                }
            }
            let before = pool_mutation_snapshot(&pool);
            assert!(
                pool.unbind_page(&checkpoint, &scope, 7).is_err(),
                "{corruption:?}"
            );
            assert_eq!(pool_mutation_snapshot(&pool), before, "{corruption:?}");
        }
    }

    #[test]
    fn close_proves_vacancy_is_the_exact_member_permutation_with_foreign_scopes_active() {
        #[derive(Clone, Copy, Debug)]
        enum Corruption {
            CrossScopeSubstitution,
            Omission,
            Duplicate,
            Cycle,
        }

        for corruption in [
            Corruption::CrossScopeSubstitution,
            Corruption::Omission,
            Corruption::Duplicate,
            Corruption::Cycle,
        ] {
            let mut storage: std::vec::Vec<_> =
                (0..7).map(|_| PrivatePagePoolSlot::empty()).collect();
            let pool = vacant_pool(&mut storage, 20, 20);
            let target = pool.reserve_scope(3).unwrap();
            let _foreign = pool.reserve_scope(3).unwrap();
            {
                let mut slots = pool.slots.borrow_mut();
                match corruption {
                    Corruption::CrossScopeSubstitution => {
                        slots[0].scope_vacant_next = 4;
                        slots[4].scope_vacant_next = 2;
                        slots[4].scope_id = target.id;
                        slots[4].scope_anchor_index = target.anchor;
                        slots[4].scope_member_ordinal = 1;
                    }
                    Corruption::Omission => slots[0].scope_vacant_next = 2,
                    Corruption::Duplicate => slots[0].scope_vacant_next = 0,
                    Corruption::Cycle => slots[2].scope_vacant_next = 0,
                }
            }
            let before = pool_mutation_snapshot(&pool);
            assert!(pool.close_scope(&target).is_err(), "{corruption:?}");
            assert_eq!(pool_mutation_snapshot(&pool), before, "{corruption:?}");
            assert!(pool
                .slots
                .borrow()
                .iter()
                .all(|slot| slot.scope_validation_marker == 0));
        }
    }

    #[test]
    fn close_accepts_a_reordered_exact_vacancy_permutation() {
        let mut storage: std::vec::Vec<_> = (0..6).map(|_| PrivatePagePoolSlot::empty()).collect();
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(3).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        for pgno in [3, 7, 9] {
            pool.bind_page(
                &checkpoint,
                &scope,
                pgno,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
        }
        for pgno in [7, 3, 9] {
            pool.unbind_page(&checkpoint, &scope, pgno).unwrap();
        }
        pool.commit_checkpoint(checkpoint).unwrap();
        assert_eq!(pool.slots.borrow()[scope.anchor].scope_vacant_head, 2);
        pool.close_scope(&scope).unwrap();
        assert_eq!(unscoped_vacancy_order(&pool), [3, 4, 5, 0, 1, 2]);
    }

    #[test]
    fn active_foreign_scope_does_not_change_small_lifecycle_cost_at_512_or_4096_slots() {
        fn run(pool_slots: usize) -> (usize, usize) {
            let mut storage: std::vec::Vec<_> = (0..pool_slots)
                .map(|_| PrivatePagePoolSlot::empty())
                .collect();
            let pool = vacant_pool(&mut storage, 20, 20);
            let foreign = pool.reserve_scope(1).unwrap();
            let before = pool.test_scope_lifecycle_visits();
            let ((), allocations) = count_thread_allocations(|| {
                let target = pool.reserve_scope(2).unwrap();
                pool.close_scope(&target).unwrap();
            });
            let visits = pool.test_scope_lifecycle_visits() - before;
            assert_eq!(pool.active_scopes.get(), 1);
            assert_eq!(pool.test_unscoped_vacant_count(), pool_slots - 1);
            pool.close_scope(&foreign).unwrap();
            (visits, allocations)
        }

        let small = run(512);
        let large = run(4_096);
        assert_eq!(small, (18, 0));
        assert_eq!(large, small);
    }

    #[test]
    fn constructors_and_authorize_keep_the_unscoped_vacancy_fifo_exact() {
        let mut initialized = slots();
        let pool = PrivatePagePool::new(&mut initialized, 20, 21, 2).unwrap();
        assert_eq!(unscoped_vacancy_order(&pool), [3]);

        let mut storage = [
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
            PrivatePagePoolSlot::empty(),
        ];
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(1).unwrap();
        pool.close_scope(&scope).unwrap();
        assert_eq!(unscoped_vacancy_order(&pool), [1, 2, 0]);

        pool.authorize(0, 2, PrivatePageAuthorization::CommittedFree)
            .unwrap();
        assert_eq!(unscoped_vacancy_order(&pool), [1, 2]);
        pool.authorize(1, 3, PrivatePageAuthorization::CommittedFree)
            .unwrap();
        assert_eq!(unscoped_vacancy_order(&pool), [2]);

        let mut storage: std::vec::Vec<_> = (0..6).map(|_| PrivatePagePoolSlot::empty()).collect();
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(3).unwrap();
        pool.close_scope(&scope).unwrap();
        assert_eq!(unscoped_vacancy_order(&pool), [3, 4, 5, 0, 1, 2]);
        pool.authorize(0, 2, PrivatePageAuthorization::CommittedFree)
            .unwrap();
        assert_eq!(unscoped_vacancy_order(&pool), [3, 4, 5, 1, 2]);
    }

    #[test]
    fn scope_reserve_and_close_cost_only_the_requested_members() {
        fn run(pool_slots: usize) -> (usize, usize) {
            let mut storage: std::vec::Vec<_> = (0..pool_slots)
                .map(|_| PrivatePagePoolSlot::empty())
                .collect();
            let pool = vacant_pool(&mut storage, 20, 20);
            let before = pool.test_scope_lifecycle_visits();
            let ((), allocations) = count_thread_allocations(|| {
                for _ in 0..64 {
                    let scope = pool.reserve_scope(1).unwrap();
                    pool.close_scope(&scope).unwrap();
                }
            });
            assert_eq!(pool.test_unscoped_vacant_count(), pool_slots);
            (pool.test_scope_lifecycle_visits() - before, allocations)
        }

        let small = run(512);
        let large = run(4_096);
        assert_eq!(small, (9 * 64, 0));
        assert_eq!(large, small);
    }

    #[test]
    fn reverse_close_churn_reuses_nonmonotonic_slots_deterministically_without_heap() {
        let mut storage: std::vec::Vec<_> = (0..8).map(|_| PrivatePagePoolSlot::empty()).collect();
        let pool = vacant_pool(&mut storage, 20, 20);
        let ((first_order, second_order), allocations) = count_thread_allocations(|| {
            let first = pool.reserve_scope(2).unwrap();
            let second = pool.reserve_scope(2).unwrap();
            let third = pool.reserve_scope(2).unwrap();
            pool.close_scope(&third).unwrap();
            pool.close_scope(&second).unwrap();
            pool.close_scope(&first).unwrap();

            let all = pool.reserve_scope(8).unwrap();
            let first_order = exact_scope_order::<8>(&pool, &all);
            pool.close_scope(&all).unwrap();

            let all = pool.reserve_scope(8).unwrap();
            let second_order = exact_scope_order::<8>(&pool, &all);
            pool.close_scope(&all).unwrap();
            (first_order, second_order)
        });

        let expected = [6, 7, 4, 5, 2, 3, 0, 1];
        assert_eq!(first_order, expected);
        assert_eq!(second_order, expected);
        assert_eq!(unscoped_vacancy_order(&pool), expected);
        assert_eq!(allocations, 0);
    }

    #[test]
    fn checkpoint_commit_and_rollback_leave_the_unscoped_vacancy_fifo_unchanged() {
        let mut storage: std::vec::Vec<_> = (0..4).map(|_| PrivatePagePoolSlot::empty()).collect();
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(2).unwrap();
        let queue_scalars = (
            pool.unscoped_vacant_count.get(),
            pool.unscoped_vacant_head.get(),
            pool.unscoped_vacant_tail.get(),
        );
        assert_eq!(unscoped_vacancy_order(&pool), [2, 3]);

        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            7,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(
            (
                pool.unscoped_vacant_count.get(),
                pool.unscoped_vacant_head.get(),
                pool.unscoped_vacant_tail.get(),
            ),
            queue_scalars
        );
        assert_eq!(unscoped_vacancy_order(&pool), [2, 3]);

        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            7,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        assert_eq!(
            (
                pool.unscoped_vacant_count.get(),
                pool.unscoped_vacant_head.get(),
                pool.unscoped_vacant_tail.get(),
            ),
            queue_scalars
        );
        assert_eq!(unscoped_vacancy_order(&pool), [2, 3]);

        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.unbind_page(&checkpoint, &scope, 7).unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        assert_eq!(unscoped_vacancy_order(&pool), [2, 3]);
        pool.close_scope(&scope).unwrap();
        assert_eq!(unscoped_vacancy_order(&pool), [2, 3, 0, 1]);
    }

    #[test]
    fn exact_scope_commitments_include_the_unscoped_vacancy_index_scalars() {
        let mut storage: std::vec::Vec<_> = (0..4).map(|_| PrivatePagePoolSlot::empty()).collect();
        let pool = vacant_pool(&mut storage, 20, 20);
        let scope = pool.reserve_scope(1).unwrap();
        let commitment = pool.exact_commitment(&scope).unwrap();
        pool.validate_exact_commitment(&scope, &commitment).unwrap();

        let original = pool.unscoped_vacant_count.get();
        pool.unscoped_vacant_count.set(original + 1);
        assert_eq!(
            pool.validate_exact_commitment(&scope, &commitment)
                .unwrap_err(),
            PrivatePagePoolError::StaleAuthority
        );
        pool.unscoped_vacant_count.set(original);
        pool.validate_exact_commitment(&scope, &commitment).unwrap();
    }

    #[test]
    fn reserve_rejects_noncanonical_vacancy_payloads_and_headers_atomically() {
        #[derive(Clone, Copy, Debug)]
        enum Corruption {
            PageNumber,
            AllocationGeneration,
            AdapterOwner,
            AdapterTag,
            Bytes,
            ScopeId,
            ScopeRoot,
            ScopeVacantHead,
            ScopeMemberHead,
            ScopeMemberOrdinal,
            ScopeCapacity,
            ScopeBound,
        }

        for corruption in [
            Corruption::PageNumber,
            Corruption::AllocationGeneration,
            Corruption::AdapterOwner,
            Corruption::AdapterTag,
            Corruption::Bytes,
            Corruption::ScopeId,
            Corruption::ScopeRoot,
            Corruption::ScopeVacantHead,
            Corruption::ScopeMemberHead,
            Corruption::ScopeMemberOrdinal,
            Corruption::ScopeCapacity,
            Corruption::ScopeBound,
        ] {
            let mut storage = [PrivatePagePoolSlot::empty(), PrivatePagePoolSlot::empty()];
            let pool = vacant_pool(&mut storage, 20, 20);
            {
                let mut slots = pool.slots.borrow_mut();
                let slot = &mut slots[0];
                match corruption {
                    Corruption::PageNumber => slot.pgno = 7,
                    Corruption::AllocationGeneration => slot.allocation_generation = 1,
                    Corruption::AdapterOwner => {
                        slot.adapter_owner = Some(PrivatePageOwner::Bitmap);
                    }
                    Corruption::AdapterTag => slot.adapter_tag = 1,
                    Corruption::Bytes => slot.bytes[0] = 1,
                    Corruption::ScopeId => slot.scope_id = 1,
                    Corruption::ScopeRoot => slot.scope_root = 0,
                    Corruption::ScopeVacantHead => slot.scope_vacant_head = 0,
                    Corruption::ScopeMemberHead => slot.scope_member_head = 0,
                    Corruption::ScopeMemberOrdinal => slot.scope_member_ordinal = 0,
                    Corruption::ScopeCapacity => slot.scope_capacity = 1,
                    Corruption::ScopeBound => slot.scope_bound = 1,
                }
            }
            let before = pool_mutation_snapshot(&pool);
            assert!(pool.reserve_scope(1).is_err(), "{corruption:?}");
            assert_eq!(pool_mutation_snapshot(&pool), before, "{corruption:?}");
        }
    }

    #[test]
    fn close_rejects_corrupt_member_and_vacancy_chains_atomically() {
        #[derive(Clone, Copy, Debug)]
        enum Corruption {
            WrongHead,
            MemberEarlyEnd,
            MemberDuplicate,
            MemberCycle,
            MemberTrailing,
            MemberForeignIdentity,
            MemberForeignAnchor,
            MemberWrongOrdinal,
            VacancyEarlyEnd,
            VacancyDuplicate,
            VacancyCycle,
            VacancyTrailing,
        }

        for corruption in [
            Corruption::WrongHead,
            Corruption::MemberEarlyEnd,
            Corruption::MemberDuplicate,
            Corruption::MemberCycle,
            Corruption::MemberTrailing,
            Corruption::MemberForeignIdentity,
            Corruption::MemberForeignAnchor,
            Corruption::MemberWrongOrdinal,
            Corruption::VacancyEarlyEnd,
            Corruption::VacancyDuplicate,
            Corruption::VacancyCycle,
            Corruption::VacancyTrailing,
        ] {
            let mut storage: std::vec::Vec<_> =
                (0..4).map(|_| PrivatePagePoolSlot::empty()).collect();
            let pool = vacant_pool(&mut storage, 20, 20);
            let scope = pool.reserve_scope(3).unwrap();
            {
                let mut slots = pool.slots.borrow_mut();
                match corruption {
                    Corruption::WrongHead => slots[0].scope_member_head = 1,
                    Corruption::MemberEarlyEnd => slots[0].scope_member_next = NO_SLOT,
                    Corruption::MemberDuplicate => slots[0].scope_member_next = 0,
                    Corruption::MemberCycle => slots[2].scope_member_next = 0,
                    Corruption::MemberTrailing => slots[2].scope_member_next = 3,
                    Corruption::MemberForeignIdentity => slots[1].scope_id = scope.id + 1,
                    Corruption::MemberForeignAnchor => slots[1].scope_anchor_index = 2,
                    Corruption::MemberWrongOrdinal => slots[1].scope_member_ordinal = 2,
                    Corruption::VacancyEarlyEnd => slots[0].scope_vacant_next = NO_SLOT,
                    Corruption::VacancyDuplicate => slots[0].scope_vacant_next = 0,
                    Corruption::VacancyCycle => slots[2].scope_vacant_next = 0,
                    Corruption::VacancyTrailing => slots[2].scope_vacant_next = 3,
                }
            }
            let before = pool_mutation_snapshot(&pool);
            assert!(pool.close_scope(&scope).is_err(), "{corruption:?}");
            assert_eq!(pool_mutation_snapshot(&pool), before, "{corruption:?}");
        }
    }

    #[test]
    fn close_rejects_corrupt_destination_vacancy_boundaries_atomically() {
        #[derive(Clone, Copy, Debug)]
        enum Corruption {
            EmptyCountWithLinks,
            WrongCount,
            MissingHead,
            TailOutOfBounds,
            HeadPrevious,
            TailNext,
            HeadNeighborBacklink,
            TailNeighborForwardLink,
        }

        for corruption in [
            Corruption::EmptyCountWithLinks,
            Corruption::WrongCount,
            Corruption::MissingHead,
            Corruption::TailOutOfBounds,
            Corruption::HeadPrevious,
            Corruption::TailNext,
            Corruption::HeadNeighborBacklink,
            Corruption::TailNeighborForwardLink,
        ] {
            let mut storage: std::vec::Vec<_> =
                (0..4).map(|_| PrivatePagePoolSlot::empty()).collect();
            let pool = vacant_pool(&mut storage, 20, 20);
            let scope = pool.reserve_scope(2).unwrap();
            match corruption {
                Corruption::EmptyCountWithLinks => pool.unscoped_vacant_count.set(0),
                Corruption::WrongCount => pool.unscoped_vacant_count.set(3),
                Corruption::MissingHead => pool.unscoped_vacant_head.set(NO_SLOT),
                Corruption::TailOutOfBounds => pool.unscoped_vacant_tail.set(4),
                Corruption::HeadPrevious => {
                    pool.slots.borrow_mut()[2].unscoped_vacant_prev = 3;
                }
                Corruption::TailNext => {
                    pool.slots.borrow_mut()[3].unscoped_vacant_next = 2;
                }
                Corruption::HeadNeighborBacklink => {
                    pool.slots.borrow_mut()[3].unscoped_vacant_prev = NO_SLOT;
                }
                Corruption::TailNeighborForwardLink => {
                    pool.slots.borrow_mut()[2].unscoped_vacant_next = NO_SLOT;
                }
            }
            let before = pool_mutation_snapshot(&pool);
            assert!(pool.close_scope(&scope).is_err(), "{corruption:?}");
            assert_eq!(pool_mutation_snapshot(&pool), before, "{corruption:?}");
        }
    }

    #[test]
    fn close_rejects_noncanonical_vacancy_payloads_and_headers_atomically() {
        #[derive(Clone, Copy, Debug)]
        enum Corruption {
            PageNumber,
            AllocationGeneration,
            AdapterOwner,
            AdapterTag,
            Bytes,
            NonAnchorRoot,
            NonAnchorVacantHead,
            NonAnchorCapacity,
            NonAnchorBound,
            MissingAnchorVacantHead,
        }

        for corruption in [
            Corruption::PageNumber,
            Corruption::AllocationGeneration,
            Corruption::AdapterOwner,
            Corruption::AdapterTag,
            Corruption::Bytes,
            Corruption::NonAnchorRoot,
            Corruption::NonAnchorVacantHead,
            Corruption::NonAnchorCapacity,
            Corruption::NonAnchorBound,
            Corruption::MissingAnchorVacantHead,
        ] {
            let mut storage: std::vec::Vec<_> =
                (0..4).map(|_| PrivatePagePoolSlot::empty()).collect();
            let pool = vacant_pool(&mut storage, 20, 20);
            let scope = pool.reserve_scope(2).unwrap();
            {
                let mut slots = pool.slots.borrow_mut();
                match corruption {
                    Corruption::PageNumber => slots[0].pgno = 7,
                    Corruption::AllocationGeneration => slots[1].allocation_generation = 1,
                    Corruption::AdapterOwner => {
                        slots[1].adapter_owner = Some(PrivatePageOwner::Bitmap);
                    }
                    Corruption::AdapterTag => slots[1].adapter_tag = 1,
                    Corruption::Bytes => slots[1].bytes[0] = 1,
                    Corruption::NonAnchorRoot => slots[1].scope_root = 0,
                    Corruption::NonAnchorVacantHead => slots[1].scope_vacant_head = 0,
                    Corruption::NonAnchorCapacity => slots[1].scope_capacity = 1,
                    Corruption::NonAnchorBound => slots[1].scope_bound = 1,
                    Corruption::MissingAnchorVacantHead => {
                        slots[0].scope_vacant_head = NO_SLOT;
                    }
                }
            }
            let before = pool_mutation_snapshot(&pool);
            assert!(pool.close_scope(&scope).is_err(), "{corruption:?}");
            assert_eq!(pool_mutation_snapshot(&pool), before, "{corruption:?}");
        }
    }

    #[test]
    fn binding_avl_scaling_is_deterministic_at_512_and_4096_slots() {
        for count in [512usize, 4096] {
            let mut storage: std::vec::Vec<_> =
                (0..count).map(|_| PrivatePagePoolSlot::empty()).collect();
            let committed = u64::try_from(count + 10).unwrap();
            let pool = vacant_pool(&mut storage, committed, committed);
            let scope = pool.reserve_scope(count).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            for pgno in (2..=u32::try_from(count + 1).unwrap()).rev() {
                pool.bind_page(
                    &checkpoint,
                    &scope,
                    pgno,
                    PrivatePageAuthorization::SafelyReclaimed,
                )
                .unwrap();
            }
            let slots = pool.slots.borrow();
            let global = verify_avl(&slots, pool.index_root.get(), None);
            let scoped = verify_avl(&slots, slots[scope.anchor].scope_root, Some(scope.id));
            let maximum_height = 2 * usize::BITS.saturating_sub((count + 1).leading_zeros());
            assert_eq!((global.count, scoped.count), (count, count));
            assert!(global.height <= maximum_height as usize);
            assert!(scoped.height <= maximum_height as usize);
            assert_eq!((global.minimum, scoped.minimum), (2, 2));
            drop(slots);
            pool.commit_checkpoint(checkpoint).unwrap();
            assert_eq!(pool.terminal_rebuild_visits.get(), 3 * count);

            let before = avl_node_snapshots(&pool);
            let checkpoint = pool.begin_checkpoint().unwrap();
            let target = u32::try_from(count + 1).unwrap();
            let authority = pool
                .claim_page_in_scope(&checkpoint, &scope, target, PrivatePageOwner::Bitmap, 3, 7)
                .unwrap();
            pool.rollback_checkpoint(checkpoint).unwrap();
            assert_eq!(avl_node_snapshots(&pool), before);
            let slots = pool.slots.borrow();
            let global = verify_avl(&slots, pool.index_root.get(), None);
            let scoped = verify_avl(&slots, slots[scope.anchor].scope_root, Some(scope.id));
            assert_eq!((global.available, scoped.available), (count, count));
            let lowest =
                PrivatePagePool::lowest_available_in_scope(&slots, slots[scope.anchor].scope_root)
                    .unwrap();
            assert_eq!(slots[lowest].pgno, 2);
            drop(slots);
            assert_eq!(pool.terminal_rebuild_visits.get(), 3 * count);
            assert_eq!(
                pool.borrow_page_in_scope(&scope, &authority).unwrap_err(),
                PrivatePagePoolError::StaleAuthority
            );
        }
    }

    #[test]
    fn selective_delete_work_is_target_local_at_512_and_4096_slots() {
        fn prepare(total: usize) -> u64 {
            const TARGETS: usize = 8;
            let mut storage: std::vec::Vec<_> =
                (0..total).map(|_| PrivatePagePoolSlot::empty()).collect();
            let page_count = u64::try_from(total + 2).unwrap();
            let pool = vacant_pool(&mut storage, page_count, page_count);
            let target = pool.reserve_scope(TARGETS).unwrap();
            let foreign = pool.reserve_scope(total - TARGETS).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            let mut target_slots = [NO_SLOT; TARGETS];
            let mut target_len = 0usize;
            for ordinal in 0..total {
                let is_target = (ordinal + 1) * TARGETS / total > ordinal * TARGETS / total;
                let scope = if is_target { &target } else { &foreign };
                let slot = pool
                    .bind_page(
                        &checkpoint,
                        scope,
                        u32::try_from(ordinal + 2).unwrap(),
                        PrivatePageAuthorization::SafelyReclaimed,
                    )
                    .unwrap();
                if is_target {
                    target_slots[target_len] = slot;
                    target_len += 1;
                }
            }
            assert_eq!(target_len, TARGETS);
            pool.commit_checkpoint(checkpoint).unwrap();
            target_slots.reverse();
            let (node_count, path_count) =
                private_page_selective_scratch_requirements(total, TARGETS, 0).unwrap();
            let mut nodes = std::vec![
                PrivatePageSelectiveOverlayNode::empty();
                node_count
            ];
            let mut path = std::vec![PrivatePageSelectivePathEntry::empty(); path_count];
            let scratch =
                PrivatePageSelectiveScratch::new(&mut nodes, &mut path, &mut target_slots);
            let (prepared, allocations) = count_thread_allocations(|| {
                pool.prepare_selective_deletes(&target, scratch, TARGETS, 0)
            });
            assert_eq!(allocations, 0);
            let prepared = prepared.unwrap();
            assert!(prepared.test_work() > 0);
            assert!(prepared.test_work() <= prepared.test_work_limit());
            assert!(prepared.test_node_len() <= node_count);
            prepared.test_work()
        }

        let small = prepare(512);
        let large = prepare(4096);
        assert!(
            large <= small * 2,
            "fixed-target work grew with foreign population: 512={small} 4096={large}"
        );
    }

    #[test]
    fn selective_refresh_work_covers_a_fully_retained_scope() {
        for total in [3usize, 512, 4096] {
            let mut storage: std::vec::Vec<_> =
                (0..total).map(|_| PrivatePagePoolSlot::empty()).collect();
            let page_count = u64::try_from(total + 2).unwrap();
            let pool = vacant_pool(&mut storage, page_count, page_count);
            let scope = pool.reserve_scope(total).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            let mut scope_slots = std::vec::Vec::with_capacity(total);
            for ordinal in 0..total {
                scope_slots.push(
                    pool.bind_page(
                        &checkpoint,
                        &scope,
                        u32::try_from(ordinal + 2).unwrap(),
                        PrivatePageAuthorization::SafelyReclaimed,
                    )
                    .unwrap(),
                );
            }
            pool.commit_checkpoint(checkpoint).unwrap();

            let (node_count, path_count) =
                private_page_selective_scratch_requirements(total, 0, total).unwrap();
            let mut nodes = std::vec![PrivatePageSelectiveOverlayNode::empty(); node_count];
            let mut path = std::vec![PrivatePageSelectivePathEntry::empty(); path_count];
            let mut targets = [];
            let scratch = PrivatePageSelectiveScratch::new(&mut nodes, &mut path, &mut targets);
            let (prepared, allocations) = count_thread_allocations(|| {
                pool.prepare_selective_deletes(&scope, scratch, 0, total)
            });
            assert_eq!(allocations, 0);
            let mut prepared = prepared.unwrap();
            for slot in scope_slots {
                let desired = pool.finalized_slot(&scope, slot).unwrap();
                let (result, allocations) = count_thread_allocations(|| {
                    pool.prepare_retained_refreshes(
                        &scope,
                        &mut prepared,
                        core::slice::from_ref(&slot),
                        core::slice::from_ref(&desired),
                    )
                });
                assert_eq!(allocations, 0);
                result.unwrap();
            }
            let (normalized, allocations) = count_thread_allocations(|| {
                pool.normalize_selective_deletes(&scope, &mut prepared)
            });
            assert_eq!(allocations, 0);
            normalized.unwrap();
            assert!(prepared.test_work() <= prepared.test_work_limit());
            prepared.into_scratch().clear();
        }
    }

    fn transaction_pool(
        storage: &mut [PrivatePagePoolSlot],
        committed: u64,
        pending: u64,
    ) -> PrivatePagePool<'_> {
        PrivatePagePool::new_vacant_transaction(storage, committed, pending, 2).unwrap()
    }

    #[test]
    fn transaction_identity_pair_admits_terminal_pair_then_exhausts_without_storage_change() {
        let counter = AtomicUsize::new(usize::MAX - 1);
        assert_eq!(
            reserve_pool_identity_pair(&counter),
            Some((usize::MAX - 1, usize::MAX))
        );
        assert_eq!(counter.load(Ordering::Relaxed), usize::MAX);
        assert_eq!(reserve_pool_identity_pair(&counter), None);

        let mut storage = [PrivatePagePoolSlot::authorized(
            7,
            PrivatePageAuthorization::SafelyReclaimed,
        )];
        let before = storage.clone();
        let invalid = PrivatePagePool::new_vacant_transaction(&mut storage, 1, 2, 2)
            .unwrap_err()
            .1;
        assert_eq!(
            invalid,
            PrivatePagePoolError::PageCountOutOfRange {
                committed: 1,
                pending: 2
            }
        );
        assert_eq!(storage, before);
    }

    #[test]
    fn transaction_pool_preserves_exact_abort_headroom_and_discards_in_exact_p_steps() {
        let mut storage = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = transaction_pool(&mut storage, 2, 2);
        assert_eq!(pool.abort_epoch_reserve, 2);
        pool.epoch.set(u64::MAX - pool.abort_epoch_reserve - 1);
        {
            let _scope = pool.reserve_scope(1).unwrap();
            assert_eq!(pool.epoch.get(), u64::MAX - pool.abort_epoch_reserve);
            let before = pool_mutation_snapshot(&pool);
            assert_eq!(
                pool.reserve_scope(1).unwrap_err(),
                PrivatePagePoolError::EpochExhausted
            );
            assert_eq!(pool_mutation_snapshot(&pool), before);
        }
        let ((storage, visits), allocations) =
            count_thread_allocations(|| pool.discard_transaction_draft().unwrap());
        assert_eq!(allocations, 0);
        assert_eq!(visits, 2);
        assert!(storage
            .iter()
            .all(|slot| slot == &PrivatePagePoolSlot::empty()));
    }

    #[test]
    fn checkpoint_cleanup_and_abort_reserves_are_additive_and_atomic() {
        let mut storage = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = transaction_pool(&mut storage, 3, 3);
        {
            let scope = pool.reserve_scope(1).unwrap();
            pool.epoch.set(u64::MAX - pool.abort_epoch_reserve - 4);
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.bind_page(
                &checkpoint,
                &scope,
                2,
                PrivatePageAuthorization::SafelyReclaimed,
            )
            .unwrap();
            pool.rollback_checkpoint(checkpoint).unwrap();
            assert_eq!(pool.epoch.get(), u64::MAX - pool.abort_epoch_reserve);
        }
        assert_eq!(pool.discard_transaction_draft().unwrap().1, 2);

        let mut storage = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = transaction_pool(&mut storage, 3, 3);
        {
            let scope = pool.reserve_scope(1).unwrap();
            let checkpoint = pool.begin_checkpoint().unwrap();
            pool.epoch.set(u64::MAX - pool.abort_epoch_reserve - 2);
            let before = pool_mutation_snapshot(&pool);
            assert_eq!(
                pool.bind_page(
                    &checkpoint,
                    &scope,
                    2,
                    PrivatePageAuthorization::SafelyReclaimed,
                )
                .unwrap_err(),
                PrivatePagePoolError::EpochExhausted
            );
            assert_eq!(pool_mutation_snapshot(&pool), before);
            pool.rollback_checkpoint(checkpoint).unwrap();
        }
        assert_eq!(pool.discard_transaction_draft().unwrap().1, 2);
    }

    #[test]
    fn selective_shadow_paths_preserve_abort_reserve_and_binding_atomicity() {
        let mut storage = [PrivatePagePoolSlot::empty()];
        let pool = transaction_pool(&mut storage, 3, 3);
        let scope = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            2,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let slot = scope.anchor;
        let bytes = [7; PAGE_SIZE];

        pool.epoch.set(u64::MAX - pool.abort_epoch_reserve - 1);
        pool.shadow_claim_and_write(&scope, slot, PrivatePageOwner::Bitmap, 2, 9, &bytes)
            .unwrap();
        let before = pool_mutation_snapshot(&pool);
        assert_eq!(
            pool.shadow_return(
                &scope,
                slot,
                PrivatePageOwner::Bitmap,
                2,
                PrivatePageReturn::Available,
            )
            .unwrap_err(),
            PrivatePagePoolError::EpochExhausted
        );
        assert_eq!(pool_mutation_snapshot(&pool), before);

        pool.epoch.set(10);
        pool.slots.borrow_mut()[slot].binding_epoch = u64::MAX;
        let before = pool_mutation_snapshot(&pool);
        assert_eq!(
            pool.shadow_return(
                &scope,
                slot,
                PrivatePageOwner::Bitmap,
                2,
                PrivatePageReturn::Available,
            )
            .unwrap_err(),
            PrivatePagePoolError::EpochExhausted
        );
        assert_eq!(pool_mutation_snapshot(&pool), before);

        pool.slots.borrow_mut()[slot].binding_epoch = 7;
        pool.shadow_return(
            &scope,
            slot,
            PrivatePageOwner::Bitmap,
            2,
            PrivatePageReturn::Available,
        )
        .unwrap();
        pool.slots.borrow_mut()[slot].binding_epoch = u64::MAX;
        let before = pool_mutation_snapshot(&pool);
        assert_eq!(
            pool.shadow_claim_and_write(&scope, slot, PrivatePageOwner::Bitmap, 2, 9, &bytes,)
                .unwrap_err(),
            PrivatePagePoolError::EpochExhausted
        );
        assert_eq!(pool_mutation_snapshot(&pool), before);
    }

    #[test]
    fn install_and_sealed_successor_paths_preserve_abort_reserve_atomically() {
        let mut storage = [PrivatePagePoolSlot::empty()];
        let pool = transaction_pool(&mut storage, 3, 3);
        let scope = pool.reserve_scope(1).unwrap();
        let checkpoint = pool.begin_checkpoint().unwrap();
        pool.bind_page(
            &checkpoint,
            &scope,
            2,
            PrivatePageAuthorization::SafelyReclaimed,
        )
        .unwrap();
        pool.commit_checkpoint(checkpoint).unwrap();
        let desired = pool.finalized_slot(&scope, scope.anchor).unwrap();

        pool.epoch.set(u64::MAX - pool.abort_epoch_reserve);
        let before = pool_mutation_snapshot(&pool);
        assert_eq!(
            pool.install_finalized_slot_in_shadow(&scope, scope.anchor, &desired)
                .unwrap_err(),
            PrivatePageSelectiveError::Pool(PrivatePagePoolError::EpochExhausted)
        );
        assert_eq!(pool_mutation_snapshot(&pool), before);

        pool.epoch.set(10);
        pool.slots.borrow_mut()[scope.anchor].binding_epoch = u64::MAX;
        let before = pool_mutation_snapshot(&pool);
        assert_eq!(
            pool.install_finalized_slot_in_shadow(&scope, scope.anchor, &desired)
                .unwrap_err(),
            PrivatePageSelectiveError::Pool(PrivatePagePoolError::EpochExhausted)
        );
        assert_eq!(pool_mutation_snapshot(&pool), before);

        pool.slots.borrow_mut()[scope.anchor].binding_epoch = 7;
        let sealed = pool.seal_scope_terminal_prepared(&scope, 11);
        pool.epoch.set(u64::MAX - pool.abort_epoch_reserve);
        let before = pool_mutation_snapshot(&pool);
        assert_eq!(
            pool.consume_sealed_scope_successor(&sealed, 11)
                .unwrap_err(),
            PrivatePagePoolError::EpochExhausted
        );
        assert_eq!(pool_mutation_snapshot(&pool), before);
    }

    #[test]
    fn failed_discard_returns_the_same_usable_pool_and_success_scrubs_once() {
        let mut storage = [const { PrivatePagePoolSlot::empty() }; 2];
        let pool = transaction_pool(&mut storage, 3, 3);
        let identity = pool.identity;
        let invalidation = pool.invalidation_identity;

        let mut pool = pool;
        pool.invalidation_identity = pool.identity;
        let before = pool_mutation_snapshot(&pool);
        let (returned, error) = pool.discard_transaction_draft().unwrap_err();
        assert_eq!(error, PrivatePagePoolError::InvalidState(NO_SLOT));
        assert_eq!(pool_mutation_snapshot(&returned), before);
        {
            let scope = returned.reserve_scope(1).unwrap();
            returned.close_scope(&scope).unwrap();
        }

        let mut pool = returned;
        pool.invalidation_identity = invalidation;
        pool.abort_epoch_reserve -= 1;
        let before = pool_mutation_snapshot(&pool);
        let (returned, error) = pool.discard_transaction_draft().unwrap_err();
        assert_eq!(error, PrivatePagePoolError::InvalidState(NO_SLOT));
        assert_eq!(pool_mutation_snapshot(&returned), before);

        let mut pool = returned;
        pool.abort_epoch_reserve += 1;
        pool.epoch.set(u64::MAX);
        let before = pool_mutation_snapshot(&pool);
        let (returned, error) = pool.discard_transaction_draft().unwrap_err();
        assert_eq!(error, PrivatePagePoolError::EpochExhausted);
        assert_eq!(pool_mutation_snapshot(&returned), before);

        returned.epoch.set(10);
        let (storage, visits) = returned.discard_transaction_draft().unwrap();
        assert_eq!(visits, 2);
        assert_eq!(identity.checked_add(1), Some(invalidation));
        assert!(storage
            .iter()
            .all(|slot| slot == &PrivatePagePoolSlot::empty()));
    }
}
