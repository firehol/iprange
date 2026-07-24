//! Private ownership shell for one unpublished writer transaction.
//!
//! This module contains no publication or filesystem logic. It moves the
//! caller's fixed page storage between an idle owner and one private draft.

use crate::contract::{MetaV4, MAX_PAGE_COUNT};
use crate::error::ErrorCode;
#[cfg(test)]
use crate::private_page_pool::PrivatePagePreparedScopeSlot;
use crate::private_page_pool::{
    PrivatePageCoordinatorWorkPhase, PrivatePagePool, PrivatePagePoolError, PrivatePagePoolSlot,
    PrivatePageScopedOperation,
};
#[cfg(test)]
use crate::writer_fixed_point::{FixedPointActiveWork, FixedPointPreparedWork};
use crate::writer_fixed_point::{
    FixedPointCoordinator, FixedPointCoordinatorWorkspace, FixedPointError, FixedPointPredecessor,
    FixedPointPreparedAggregateAuthority, FixedPointPrivateOutputDrainError,
    FixedPointSealedAggregateWork,
};
#[cfg(test)]
use crate::writer_fixed_point::{FixedPointPreparedOutput, FixedPointPreparedWorkSlot};
use crate::writer_transaction_contract::{
    FixedCleanupLedger, PrivateCleanupEntry, PrivateCoordinationCleanup,
    PrivateWriterContractError, PrivateWriterResourceBudget, PrivateWriterResourceDelta,
    PrivateWriterResourceUsage,
};
use core::cell::Cell;
use core::sync::atomic::{AtomicUsize, Ordering};

static NEXT_WRITER_HANDLE_IDENTITY: AtomicUsize = AtomicUsize::new(1);

const fn checked_next_writer_identity_pair(identity: usize) -> Option<usize> {
    if identity == usize::MAX {
        None
    } else if identity == usize::MAX - 1 {
        Some(usize::MAX)
    } else {
        identity.checked_add(2)
    }
}

fn reserve_writer_identity_pair_from(counter: &AtomicUsize) -> Option<(usize, usize)> {
    let active = counter
        .fetch_update(
            Ordering::Relaxed,
            Ordering::Relaxed,
            checked_next_writer_identity_pair,
        )
        .ok()?;
    Some((active, active + 1))
}

fn reserve_writer_identity_pair() -> Option<(usize, usize)> {
    reserve_writer_identity_pair_from(&NEXT_WRITER_HANDLE_IDENTITY)
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivateWriterTransactionState {
    Clean,
    Pending,
    AbortRequired,
    AbortIncomplete,
    /// Target-meta durability is ambiguous. The exact attempt must be resolved
    /// after close and reopen; this core can neither publish nor abort it.
    OutcomeUnknown,
    /// The target meta is already durable. Only in-memory cleanup may remain;
    /// this transaction can never take the abort path again.
    CommittedCleanupRequired,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum PrivateWriterCommitPhase {
    Idle,
    PrivateOutputPrepared,
    PrivateOutputDrained,
    MetaPublicationAuthorized,
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum PrivateWriterTransactionError<E> {
    InvalidArgument,
    TransactionExhausted,
    InsufficientBudget { required: u64, actual: u64 },
    AlreadyPending,
    SelectedGenerationMismatch,
    NoPendingTransaction,
    AbortRequired(Option<PrivatePagePoolError>),
    AbortIncompleteIdentity,
    AbortIncompletePool(PrivatePagePoolError),
    AbortIncompleteCleanupContract(PrivateWriterContractError),
    AbortIncompleteCleanup(E),
    AbortIncompleteResource,
    AbortIncompleteCoordination,
    OutcomeUnknown,
    CommittedCleanupRequired,
    CommittedCleanupIncompleteIdentity,
    CommittedCleanupIncompletePool(PrivatePagePoolError),
    CommittedCleanupIncompleteResource,
    CommittedCleanupIncompleteCoordination,
    StaleHandle,
    FixedPoint(FixedPointError),
    FixedPointOutput(FixedPointPrivateOutputDrainError<E>),
    Pool(PrivatePagePoolError),
}

impl<E> PrivateWriterTransactionError<E> {
    pub(crate) const fn code(&self) -> ErrorCode {
        match self {
            Self::InvalidArgument => ErrorCode::InvalidArgument,
            Self::TransactionExhausted => ErrorCode::TransactionIdExhausted,
            Self::InsufficientBudget { .. } => ErrorCode::InsufficientResourceBudget,
            Self::AlreadyPending => ErrorCode::WrongState,
            Self::SelectedGenerationMismatch => ErrorCode::Conflict,
            Self::NoPendingTransaction => ErrorCode::NoPendingTransaction,
            Self::AbortRequired(_) => ErrorCode::TransactionAborted,
            Self::AbortIncompletePool(_)
            | Self::AbortIncompleteIdentity
            | Self::AbortIncompleteCleanupContract(_)
            | Self::AbortIncompleteCleanup(_)
            | Self::AbortIncompleteResource
            | Self::AbortIncompleteCoordination => ErrorCode::AbortIncomplete,
            Self::OutcomeUnknown => ErrorCode::Unresolvable,
            Self::CommittedCleanupRequired
            | Self::CommittedCleanupIncompleteIdentity
            | Self::CommittedCleanupIncompletePool(_)
            | Self::CommittedCleanupIncompleteResource
            | Self::CommittedCleanupIncompleteCoordination => ErrorCode::CleanupInProgress,
            Self::StaleHandle => ErrorCode::StaleReference,
            Self::FixedPoint(_) => ErrorCode::WrongState,
            Self::FixedPointOutput(FixedPointPrivateOutputDrainError::Sink(_)) => {
                ErrorCode::SinkFailed
            }
            Self::FixedPointOutput(_) => ErrorCode::TransactionAborted,
            Self::Pool(_) => ErrorCode::WrongState,
        }
    }
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) struct PrivateWriterTransactionHandle {
    identity: usize,
}

/// Exact internal authority produced only after phase-1 transaction checks.
///
/// It is deliberately non-copy: while it exists, the core closes its normal
/// private-page and coordinator access paths.
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct PrivateWriterCommitPreparation {
    handle_identity: usize,
    target: MetaV4,
}

impl PrivateWriterCommitPreparation {
    pub(crate) const fn target(&self) -> MetaV4 {
        self.target
    }
}

/// Exact target meta that may enter the physical publication boundary.
///
/// The OS writer will consume this only after all retained private pages have
/// been written and synchronized. It does not itself claim durability.
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct PrivateWriterMetaPublication {
    handle_identity: usize,
    target: MetaV4,
}

impl PrivateWriterMetaPublication {
    pub(crate) const fn target(&self) -> MetaV4 {
        self.target
    }
}

#[derive(Debug)]
pub(crate) struct PrivateWriterTransactionCore<'slots, 'cleanup, I, O, E> {
    selected: MetaV4,
    target: Option<MetaV4>,
    resources: PrivateWriterResourceUsage,
    cleanup: FixedCleanupLedger<'cleanup, I, O, E>,
    coordination: PrivateCoordinationCleanup,
    state: Cell<PrivateWriterTransactionState>,
    handle_identity: usize,
    abort_identity: usize,
    clean_slots: Option<&'slots mut [PrivatePagePoolSlot]>,
    draft: Option<PrivatePagePool<'slots>>,
    fixed_point: Option<FixedPointCoordinator>,
    fixed_point_registered_work: Cell<u64>,
    fixed_point_registered_generation: Cell<u64>,
    fixed_point_registered_phase: Cell<PrivatePageCoordinatorWorkPhase>,
    fixed_point_workspace_identity: Cell<usize>,
    fixed_point_workspace_bytes: Cell<u64>,
    commit_phase: Cell<PrivateWriterCommitPhase>,
    abort_visits: usize,
}

impl<'slots, 'cleanup, I, O, E> PrivateWriterTransactionCore<'slots, 'cleanup, I, O, E> {
    pub(crate) fn new(
        selected: MetaV4,
        budget: PrivateWriterResourceBudget,
        slots: &'slots mut [PrivatePagePoolSlot],
        cleanup_entries: &'cleanup mut [Option<PrivateCleanupEntry<I, O, E>>],
    ) -> Result<Self, PrivateWriterTransactionError<E>> {
        if selected.database_id == [0; 16]
            || selected.txn_id == 0
            || selected.commit_nonce == [0; 16]
            || !(2..=MAX_PAGE_COUNT).contains(&selected.page_count)
        {
            return Err(PrivateWriterTransactionError::InvalidArgument);
        }
        let slot_count = u64::try_from(slots.len()).map_err(|_| {
            PrivateWriterTransactionError::InsufficientBudget {
                required: u64::MAX,
                actual: budget.max_private_pages(),
            }
        })?;
        if slot_count > budget.max_private_pages() {
            return Err(PrivateWriterTransactionError::InsufficientBudget {
                required: slot_count,
                actual: budget.max_private_pages(),
            });
        }
        let cleanup = FixedCleanupLedger::new(cleanup_entries)
            .map_err(PrivateWriterTransactionError::AbortIncompleteCleanupContract)?;
        Ok(Self {
            selected,
            target: None,
            resources: PrivateWriterResourceUsage::new(budget),
            cleanup,
            coordination: PrivateCoordinationCleanup::None,
            state: Cell::new(PrivateWriterTransactionState::Clean),
            handle_identity: 0,
            abort_identity: 0,
            clean_slots: Some(slots),
            draft: None,
            fixed_point: None,
            fixed_point_registered_work: Cell::new(0),
            fixed_point_registered_generation: Cell::new(0),
            fixed_point_registered_phase: Cell::new(PrivatePageCoordinatorWorkPhase::None),
            fixed_point_workspace_identity: Cell::new(0),
            fixed_point_workspace_bytes: Cell::new(0),
            commit_phase: Cell::new(PrivateWriterCommitPhase::Idle),
            abort_visits: 0,
        })
    }

    pub(crate) fn state(&self) -> PrivateWriterTransactionState {
        self.state.get()
    }

    pub(crate) const fn selected(&self) -> MetaV4 {
        self.selected
    }

    pub(crate) const fn target(&self) -> Option<MetaV4> {
        self.target
    }

    pub(crate) const fn abort_visits(&self) -> usize {
        self.abort_visits
    }

    pub(crate) fn resources_mut(&mut self) -> &mut PrivateWriterResourceUsage {
        &mut self.resources
    }

    pub(crate) fn cleanup_mut(&mut self) -> &mut FixedCleanupLedger<'cleanup, I, O, E> {
        &mut self.cleanup
    }

    pub(crate) fn begin(
        &mut self,
        commit_nonce: [u8; 16],
    ) -> Result<PrivateWriterTransactionHandle, PrivateWriterTransactionError<E>> {
        if commit_nonce == [0; 16] {
            return Err(PrivateWriterTransactionError::InvalidArgument);
        }
        if self.state.get() == PrivateWriterTransactionState::OutcomeUnknown {
            return Err(PrivateWriterTransactionError::OutcomeUnknown);
        }
        if self.state.get() == PrivateWriterTransactionState::CommittedCleanupRequired {
            return Err(PrivateWriterTransactionError::CommittedCleanupRequired);
        }
        if self.state.get() != PrivateWriterTransactionState::Clean
            || self.clean_slots.is_none()
            || self.draft.is_some()
        {
            return Err(PrivateWriterTransactionError::AlreadyPending);
        }
        if !self.cleanup.is_empty()
            || self.resources.current() != PrivateWriterResourceDelta::default()
            || !self.coordination.is_none()
            || self.fixed_point_registered_work.get() != 0
            || self.fixed_point_registered_generation.get() != 0
            || self.fixed_point_registered_phase.get() != PrivatePageCoordinatorWorkPhase::None
            || self.fixed_point_workspace_identity.get() != 0
            || self.fixed_point_workspace_bytes.get() != 0
            || self.commit_phase.get() != PrivateWriterCommitPhase::Idle
        {
            return Err(PrivateWriterTransactionError::AbortIncompleteCoordination);
        }
        let target_txn = self
            .selected
            .txn_id
            .checked_add(1)
            .ok_or(PrivateWriterTransactionError::TransactionExhausted)?;
        let (handle_identity, abort_identity) = reserve_writer_identity_pair()
            .ok_or(PrivateWriterTransactionError::TransactionExhausted)?;
        let fixed_point = FixedPointCoordinator::new(
            self.selected.txn_id,
            self.selected.free_bitmap_root,
            self.selected.page_count,
        )
        .map_err(PrivateWriterTransactionError::FixedPoint)?;

        let slots = self
            .clean_slots
            .take()
            .expect("clean ownership checked above");
        let draft = match PrivatePagePool::new_vacant_transaction(
            slots,
            self.selected.page_count,
            self.selected.page_count,
            target_txn,
        ) {
            Ok(draft) => draft,
            Err((slots, error)) => {
                self.clean_slots = Some(slots);
                return Err(PrivateWriterTransactionError::Pool(error));
            }
        };
        if let Err(error) = fixed_point.attach_pool(&draft) {
            let (slots, _) = draft
                .discard_transaction_draft()
                .map_err(|(_, pool_error)| PrivateWriterTransactionError::Pool(pool_error))?;
            self.clean_slots = Some(slots);
            return Err(PrivateWriterTransactionError::FixedPoint(error));
        }

        let mut target = self.selected;
        target.txn_id = target_txn;
        target.commit_nonce = commit_nonce;
        self.target = Some(target);
        self.state.set(PrivateWriterTransactionState::Pending);
        self.handle_identity = handle_identity;
        self.abort_identity = abort_identity;
        self.draft = Some(draft);
        self.fixed_point = Some(fixed_point);
        self.fixed_point_registered_work.set(0);
        self.fixed_point_registered_generation.set(0);
        self.fixed_point_registered_phase
            .set(PrivatePageCoordinatorWorkPhase::None);
        self.fixed_point_workspace_identity.set(0);
        self.fixed_point_workspace_bytes.set(0);
        self.commit_phase.set(PrivateWriterCommitPhase::Idle);
        self.abort_visits = 0;
        Ok(PrivateWriterTransactionHandle {
            identity: handle_identity,
        })
    }

    fn validate_handle_identity(
        &self,
        handle: &PrivateWriterTransactionHandle,
    ) -> Result<(), PrivateWriterTransactionError<E>> {
        if handle.identity == 0 || handle.identity != self.handle_identity {
            return Err(PrivateWriterTransactionError::StaleHandle);
        }
        Ok(())
    }

    fn validate_handle(
        &self,
        handle: &PrivateWriterTransactionHandle,
    ) -> Result<(), PrivateWriterTransactionError<E>> {
        self.validate_handle_identity(handle)?;
        if self.state.get() == PrivateWriterTransactionState::OutcomeUnknown {
            return Err(PrivateWriterTransactionError::OutcomeUnknown);
        }
        if self.state.get() == PrivateWriterTransactionState::CommittedCleanupRequired {
            return Err(PrivateWriterTransactionError::CommittedCleanupRequired);
        }
        Ok(())
    }

    fn validate_private_output_preparation(
        &self,
        handle: &PrivateWriterTransactionHandle,
        preparation: &PrivateWriterCommitPreparation,
        expected_phase: PrivateWriterCommitPhase,
    ) -> Result<MetaV4, PrivateWriterTransactionError<E>> {
        self.validate_handle(handle)?;
        if self.commit_phase.get() != expected_phase
            || preparation.handle_identity != self.handle_identity
            || self.target != Some(preparation.target)
        {
            return Err(PrivateWriterTransactionError::StaleHandle);
        }
        Ok(preparation.target)
    }

    fn preflight_fixed_point_private_output<'backing, 'arena, 'record_cleanup>(
        &self,
        handle: &PrivateWriterTransactionHandle,
        workspace: &FixedPointCoordinatorWorkspace<'backing, 'arena, 'record_cleanup>,
    ) -> Result<MetaV4, PrivateWriterTransactionError<E>> {
        self.validate_handle(handle)?;
        if self.state.get() != PrivateWriterTransactionState::Pending
            || self.commit_phase.get() != PrivateWriterCommitPhase::Idle
            || self.fixed_point_workspace_identity.get() == 0
            || self.fixed_point_workspace_identity.get() != workspace.identity()
            || self.fixed_point_workspace_bytes.get() == 0
            || workspace.is_idle()
        {
            return Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::InvalidWorkUnit,
            ));
        }
        let Some(draft) = self.draft.as_ref() else {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        };
        let Some(coordinator) = self.fixed_point.as_ref() else {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        };
        let (pool_work, _pool_generation, pool_phase) = draft.coordinator_registered_work();
        let registration_finished = self.fixed_point_registered_work.get() == 0
            && self.fixed_point_registered_generation.get() == 0
            && self.fixed_point_registered_phase.get() == PrivatePageCoordinatorWorkPhase::None
            && pool_work == 0
            && pool_phase == PrivatePageCoordinatorWorkPhase::None
            && coordinator.registered_work() == 0
            && coordinator.is_quiescent();
        if !registration_finished {
            return Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::StalePredecessor,
            ));
        }
        if draft.requires_abort() || coordinator.requires_abort() {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        if draft.has_active_operation() {
            return Err(PrivateWriterTransactionError::Pool(
                PrivatePagePoolError::OperationActive,
            ));
        }
        if draft.has_active_checkpoint() {
            return Err(PrivateWriterTransactionError::Pool(
                PrivatePagePoolError::CheckpointActive,
            ));
        }
        let Some(target) = self.target else {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        };
        let expected_txn = self
            .selected
            .txn_id
            .checked_add(1)
            .ok_or(PrivateWriterTransactionError::TransactionExhausted)?;
        if !self.selected.static_identity_eq(&target)
            || target.txn_id != expected_txn
            || target.commit_nonce == [0; 16]
            || target.page_count != draft.pending_page_count()
            || target.page_count < self.selected.page_count
            || coordinator
                .commit_fence(draft, target.free_bitmap_root, target.page_count)
                .is_err()
        {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        Ok(target)
    }

    /// Freezes the transaction-side phase-1 state before any private page can
    /// reach a file sink. The returned authority is required for the drain and
    /// consumed only by the final publication authorization step.
    pub(crate) fn prepare_fixed_point_private_output<'backing, 'arena, 'record_cleanup>(
        &mut self,
        handle: &PrivateWriterTransactionHandle,
        workspace: &FixedPointCoordinatorWorkspace<'backing, 'arena, 'record_cleanup>,
    ) -> Result<PrivateWriterCommitPreparation, PrivateWriterTransactionError<E>> {
        let target = self.preflight_fixed_point_private_output(handle, workspace)?;
        self.commit_phase
            .set(PrivateWriterCommitPhase::PrivateOutputPrepared);
        Ok(PrivateWriterCommitPreparation {
            handle_identity: self.handle_identity,
            target,
        })
    }

    pub(crate) fn draft(
        &self,
        handle: &PrivateWriterTransactionHandle,
    ) -> Result<&PrivatePagePool<'slots>, PrivateWriterTransactionError<E>> {
        self.validate_handle(handle)?;
        if self.state.get() == PrivateWriterTransactionState::CommittedCleanupRequired {
            return Err(PrivateWriterTransactionError::CommittedCleanupRequired);
        }
        if self.state.get() != PrivateWriterTransactionState::Pending {
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        if self.commit_phase.get() != PrivateWriterCommitPhase::Idle {
            return Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::InvalidWorkUnit,
            ));
        }
        self.draft
            .as_ref()
            .ok_or(PrivateWriterTransactionError::AbortRequired(None))
    }

    pub(crate) fn fixed_point(
        &self,
        handle: &PrivateWriterTransactionHandle,
    ) -> Result<&FixedPointCoordinator, PrivateWriterTransactionError<E>> {
        self.validate_handle(handle)?;
        if self.state.get() == PrivateWriterTransactionState::CommittedCleanupRequired {
            return Err(PrivateWriterTransactionError::CommittedCleanupRequired);
        }
        if self.state.get() != PrivateWriterTransactionState::Pending {
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        if self.commit_phase.get() != PrivateWriterCommitPhase::Idle {
            return Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::InvalidWorkUnit,
            ));
        }
        let coordinator = self
            .fixed_point
            .as_ref()
            .ok_or(PrivateWriterTransactionError::AbortRequired(None))?;
        if coordinator.requires_abort() {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        Ok(coordinator)
    }

    pub(crate) fn reserve_fixed_point_workspace(
        &mut self,
        handle: &PrivateWriterTransactionHandle,
        workspace: &FixedPointCoordinatorWorkspace<'_, '_, '_>,
    ) -> Result<(), PrivateWriterTransactionError<E>> {
        self.validate_handle(handle)?;
        if self.state.get() != PrivateWriterTransactionState::Pending {
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        if self.commit_phase.get() != PrivateWriterCommitPhase::Idle
            || self.fixed_point_workspace_identity.get() != 0
            || self.fixed_point_workspace_bytes.get() != 0
            || !workspace.is_idle()
        {
            return Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::InvalidWorkUnit,
            ));
        }
        let bytes = workspace.retained_bytes();
        self.resources
            .acquire(PrivateWriterResourceDelta::new(bytes, 0, 0, 0))
            .map_err(|error| match error {
                PrivateWriterContractError::InsufficientResourceBudget {
                    required, actual, ..
                } => PrivateWriterTransactionError::InsufficientBudget { required, actual },
                PrivateWriterContractError::ArithmeticOverflow(_) => {
                    PrivateWriterTransactionError::InsufficientBudget {
                        required: u64::MAX,
                        actual: self.resources.budget().max_heap_bytes(),
                    }
                }
                _ => PrivateWriterTransactionError::FixedPoint(FixedPointError::InvalidWorkUnit),
            })?;
        self.fixed_point_workspace_identity
            .set(workspace.identity());
        self.fixed_point_workspace_bytes.set(bytes);
        Ok(())
    }

    pub(crate) fn cancel_fixed_point_workspace(
        &mut self,
        handle: &PrivateWriterTransactionHandle,
        workspace: &mut FixedPointCoordinatorWorkspace<'_, '_, '_>,
    ) -> Result<(), PrivateWriterTransactionError<E>> {
        self.validate_handle(handle)?;
        let bytes = self.fixed_point_workspace_bytes.get();
        if self.fixed_point_workspace_identity.get() == 0
            || self.fixed_point_workspace_identity.get() != workspace.identity()
            || bytes == 0
        {
            return Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::StalePredecessor,
            ));
        }
        workspace
            .cancel()
            .map_err(PrivateWriterTransactionError::FixedPoint)?;
        if !workspace.is_idle() {
            return Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::InvalidWorkUnit,
            ));
        }
        self.resources
            .release(PrivateWriterResourceDelta::new(bytes, 0, 0, 0))
            .map_err(|_| PrivateWriterTransactionError::AbortIncompleteResource)?;
        self.fixed_point_workspace_identity.set(0);
        self.fixed_point_workspace_bytes.set(0);
        if self.fixed_point_registered_phase.get() != PrivatePageCoordinatorWorkPhase::None {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
        }
        Ok(())
    }

    #[cfg(test)]
    #[allow(clippy::result_large_err)]
    pub(crate) fn execute_fixed_point_work<'core, 'slot, 'scope_slot, 'scratch, 'carried>(
        &'core self,
        handle: &PrivateWriterTransactionHandle,
        predecessor: FixedPointPredecessor,
        prepared: FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>,
    ) -> Result<
        FixedPointActiveWork<'slots>,
        (
            FixedPointPredecessor,
            FixedPointPreparedWork<'slot, 'scope_slot, 'scratch, 'carried>,
            FixedPointError,
        ),
    > {
        if self.validate_handle(handle).is_err()
            || self.state.get() != PrivateWriterTransactionState::Pending
            || self.commit_phase.get() != PrivateWriterCommitPhase::Idle
            || self.fixed_point_registered_work.get() != 0
            || self.fixed_point_registered_phase.get() != PrivatePageCoordinatorWorkPhase::None
        {
            return Err((predecessor, prepared, FixedPointError::StalePredecessor));
        }
        let Some(draft) = self.draft.as_ref() else {
            return Err((predecessor, prepared, FixedPointError::AbortRequired));
        };
        let Some(coordinator) = self.fixed_point.as_ref() else {
            return Err((predecessor, prepared, FixedPointError::AbortRequired));
        };
        let work_identity = prepared.work_identity();
        let Some(generation) = draft.next_coordinator_work_generation() else {
            return Err((predecessor, prepared, FixedPointError::IdentityExhausted));
        };
        self.fixed_point_registered_work.set(work_identity);
        self.fixed_point_registered_generation.set(generation);
        self.fixed_point_registered_phase
            .set(PrivatePageCoordinatorWorkPhase::Active);
        match coordinator.execute_prepared(predecessor, draft, prepared) {
            Ok(active) => Ok(active),
            Err((predecessor, prepared, error)) => {
                if draft.coordinator_work_phase() == PrivatePageCoordinatorWorkPhase::None
                    && !draft.requires_abort()
                    && !coordinator.requires_abort()
                {
                    self.fixed_point_registered_work.set(0);
                    self.fixed_point_registered_generation.set(0);
                    self.fixed_point_registered_phase
                        .set(PrivatePageCoordinatorWorkPhase::None);
                } else {
                    self.state.set(PrivateWriterTransactionState::AbortRequired);
                }
                Err((predecessor, prepared, error))
            }
        }
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn execute_fixed_point_aggregate<A: FixedPointPreparedAggregateAuthority>(
        &self,
        handle: &PrivateWriterTransactionHandle,
        predecessor: FixedPointPredecessor,
        prepared: A,
    ) -> Result<A::Sealed, (A, FixedPointPredecessor, FixedPointError)> {
        let mut prepared = prepared;
        if self.validate_handle(handle).is_err()
            || self.state.get() != PrivateWriterTransactionState::Pending
            || self.commit_phase.get() != PrivateWriterCommitPhase::Idle
            || self.fixed_point_registered_work.get() != 0
            || self.fixed_point_registered_phase.get() != PrivatePageCoordinatorWorkPhase::None
            || self.fixed_point_workspace_identity.get() == 0
            || self.fixed_point_workspace_identity.get() != prepared.workspace_identity()
        {
            return Err((prepared, predecessor, FixedPointError::StalePredecessor));
        }
        let Some(coordinator) = self.fixed_point.as_ref() else {
            return Err((prepared, predecessor, FixedPointError::AbortRequired));
        };
        if let Err(error) = prepared.preflight_authority(coordinator, &predecessor) {
            return Err((prepared, predecessor, error));
        }
        let work_identity = prepared.work_identity();
        let work_generation = prepared.work_generation();
        self.fixed_point_registered_work.set(work_identity);
        self.fixed_point_registered_generation.set(work_generation);
        self.fixed_point_registered_phase
            .set(PrivatePageCoordinatorWorkPhase::Active);
        let sealed = prepared.execute_authority(coordinator, predecessor);
        self.fixed_point_registered_phase
            .set(PrivatePageCoordinatorWorkPhase::Sealed);
        Ok(sealed)
    }

    /// Retains a sealed aggregate as one canonical coordinator record and
    /// returns the only predecessor authority for the following work unit.
    /// This deliberately does not write or publish pages: terminal record
    /// materialization and cleanup are a later transaction-lifecycle stage.
    #[allow(clippy::result_large_err)]
    pub(crate) fn complete_fixed_point_aggregate<'backing, 'arena, 'record_cleanup>(
        &mut self,
        handle: &PrivateWriterTransactionHandle,
        workspace: &FixedPointCoordinatorWorkspace<'backing, 'arena, 'record_cleanup>,
        sealed: FixedPointSealedAggregateWork<'arena, 'record_cleanup, 'slots>,
    ) -> Result<FixedPointPredecessor, PrivateWriterTransactionError<E>> {
        self.validate_handle(handle)?;
        let invalid = self.state.get() != PrivateWriterTransactionState::Pending
            || self.commit_phase.get() != PrivateWriterCommitPhase::Idle
            || self.fixed_point_registered_work.get() == 0
            || self.fixed_point_registered_generation.get() == 0
            || self.fixed_point_registered_phase.get() != PrivatePageCoordinatorWorkPhase::Sealed
            || self.fixed_point_workspace_identity.get() == 0
            || self.fixed_point_workspace_identity.get() != workspace.identity()
            || self.fixed_point_workspace_bytes.get() == 0;
        if invalid {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        let Some(target) = self.target else {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        };
        let Some(draft) = self.draft.as_ref() else {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        };
        let Some(coordinator) = self.fixed_point.as_ref() else {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        };
        let (pool_work, pool_generation, pool_phase) = draft.coordinator_registered_work();
        if pool_work != self.fixed_point_registered_work.get()
            || pool_generation != self.fixed_point_registered_generation.get()
            || pool_phase != PrivatePageCoordinatorWorkPhase::Sealed
            || coordinator.registered_work() != pool_work
        {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        let completion = match sealed.finish(coordinator, draft, workspace) {
            Ok(completion) => completion,
            Err(error) => {
                self.state.set(PrivateWriterTransactionState::AbortRequired);
                return Err(PrivateWriterTransactionError::FixedPoint(error));
            }
        };
        let (successor, retirement) = completion.into_parts();
        let mut target = target;
        target.page_count = successor.pending_page_count();
        target.free_bitmap_root = successor.root();
        target.retirement_root = retirement.root;
        target.retirement_batch_count = retirement.batch_count;
        self.target = Some(target);
        self.fixed_point_registered_work.set(0);
        self.fixed_point_registered_generation.set(0);
        self.fixed_point_registered_phase
            .set(PrivatePageCoordinatorWorkPhase::None);
        Ok(successor)
    }

    /// Consumes the final aggregate successor. Retained records still block
    /// commit until a later private-output drain has written and cleaned them.
    #[allow(clippy::result_large_err)]
    pub(crate) fn finish_fixed_point_input<'backing, 'arena, 'record_cleanup>(
        &mut self,
        handle: &PrivateWriterTransactionHandle,
        workspace: &FixedPointCoordinatorWorkspace<'backing, 'arena, 'record_cleanup>,
        predecessor: FixedPointPredecessor,
    ) -> Result<(), (FixedPointPredecessor, PrivateWriterTransactionError<E>)> {
        if let Err(error) = self.validate_handle(handle) {
            return Err((predecessor, error));
        }
        if self.state.get() != PrivateWriterTransactionState::Pending {
            return Err((
                predecessor,
                PrivateWriterTransactionError::AbortRequired(None),
            ));
        }
        if self.commit_phase.get() != PrivateWriterCommitPhase::Idle
            || self.fixed_point_workspace_identity.get() == 0
            || self.fixed_point_workspace_identity.get() != workspace.identity()
            || self.fixed_point_workspace_bytes.get() == 0
            || workspace.is_idle()
        {
            return Err((
                predecessor,
                PrivateWriterTransactionError::FixedPoint(FixedPointError::InvalidWorkUnit),
            ));
        }
        let Some(draft) = self.draft.as_ref() else {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err((
                predecessor,
                PrivateWriterTransactionError::AbortRequired(None),
            ));
        };
        let Some(coordinator) = self.fixed_point.as_ref() else {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err((
                predecessor,
                PrivateWriterTransactionError::AbortRequired(None),
            ));
        };
        let (pool_work, _pool_generation, pool_phase) = draft.coordinator_registered_work();
        let idle_registration = self.fixed_point_registered_work.get() == 0
            && self.fixed_point_registered_generation.get() == 0
            && self.fixed_point_registered_phase.get() == PrivatePageCoordinatorWorkPhase::None
            && pool_work == 0
            && pool_phase == PrivatePageCoordinatorWorkPhase::None
            && coordinator.registered_work() == 0;
        if !idle_registration {
            let registrations_agree = self.fixed_point_registered_work.get() == pool_work
                && self.fixed_point_registered_phase.get() == pool_phase
                && coordinator.registered_work() == pool_work;
            if !registrations_agree {
                self.state.set(PrivateWriterTransactionState::AbortRequired);
                return Err((
                    predecessor,
                    PrivateWriterTransactionError::AbortRequired(None),
                ));
            }
            return Err((
                predecessor,
                PrivateWriterTransactionError::FixedPoint(FixedPointError::StalePredecessor),
            ));
        }
        if draft.requires_abort() || coordinator.requires_abort() {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err((
                predecessor,
                PrivateWriterTransactionError::AbortRequired(None),
            ));
        }
        coordinator
            .finish(predecessor)
            .map_err(|(predecessor, error)| {
                (
                    predecessor,
                    PrivateWriterTransactionError::FixedPoint(error),
                )
            })
    }

    /// Writes and releases every page retained by completed coordinator work.
    /// Once this starts, any failure makes the draft non-publishable because a
    /// sink may already have received a strict prefix of its private pages.
    pub(crate) fn drain_fixed_point_private_pages<'backing, 'arena, 'record_cleanup, F>(
        &mut self,
        handle: &PrivateWriterTransactionHandle,
        preparation: &PrivateWriterCommitPreparation,
        workspace: &mut FixedPointCoordinatorWorkspace<'backing, 'arena, 'record_cleanup>,
        write: &mut F,
    ) -> Result<usize, PrivateWriterTransactionError<E>>
    where
        F: for<'page> FnMut(u32, &'page [u8]) -> Result<(), E>,
    {
        self.validate_private_output_preparation(
            handle,
            preparation,
            PrivateWriterCommitPhase::PrivateOutputPrepared,
        )?;
        if self.state.get() != PrivateWriterTransactionState::Pending {
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        if self.fixed_point_workspace_identity.get() == 0
            || self.fixed_point_workspace_identity.get() != workspace.identity()
            || self.fixed_point_workspace_bytes.get() == 0
            || workspace.is_idle()
        {
            return Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::InvalidWorkUnit,
            ));
        }
        let Some(draft) = self.draft.as_ref() else {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        };
        let Some(coordinator) = self.fixed_point.as_ref() else {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        };
        let (pool_work, _pool_generation, pool_phase) = draft.coordinator_registered_work();
        let registration_finished = self.fixed_point_registered_work.get() == 0
            && self.fixed_point_registered_generation.get() == 0
            && self.fixed_point_registered_phase.get() == PrivatePageCoordinatorWorkPhase::None
            && pool_work == 0
            && pool_phase == PrivatePageCoordinatorWorkPhase::None
            && coordinator.registered_work() == 0
            && coordinator.is_quiescent();
        if !registration_finished {
            return Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::StalePredecessor,
            ));
        }
        if draft.requires_abort() || coordinator.requires_abort() {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        match workspace.drain_private_pages(draft, write) {
            Ok(written) => {
                self.commit_phase
                    .set(PrivateWriterCommitPhase::PrivateOutputDrained);
                Ok(written)
            }
            Err(error) => {
                self.state.set(PrivateWriterTransactionState::AbortRequired);
                Err(PrivateWriterTransactionError::FixedPointOutput(error))
            }
        }
    }

    /// Releases drained workspace storage and produces the exact target meta
    /// authorization for the later physical publication phase.
    #[allow(clippy::result_large_err)]
    pub(crate) fn finish_fixed_point_private_output<'backing, 'arena, 'record_cleanup>(
        &mut self,
        handle: &PrivateWriterTransactionHandle,
        preparation: PrivateWriterCommitPreparation,
        workspace: &mut FixedPointCoordinatorWorkspace<'backing, 'arena, 'record_cleanup>,
    ) -> Result<PrivateWriterMetaPublication, PrivateWriterTransactionError<E>> {
        let target = self.validate_private_output_preparation(
            handle,
            &preparation,
            PrivateWriterCommitPhase::PrivateOutputDrained,
        )?;
        if !workspace.is_idle() {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::InvalidWorkUnit,
            ));
        }
        if let Err(error) = self.cancel_fixed_point_workspace(handle, workspace) {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(error);
        }
        if let Err(error) = self.preflight_commit(handle) {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(error);
        }
        self.commit_phase
            .set(PrivateWriterCommitPhase::MetaPublicationAuthorized);
        Ok(PrivateWriterMetaPublication {
            handle_identity: preparation.handle_identity,
            target,
        })
    }

    /// Records the exact target only after the physical publisher has proved
    /// that its target meta page is durable.
    ///
    /// A failure while scrubbing transaction-private state does not reopen the
    /// abort path: the selected on-disk generation has already advanced. The
    /// caller must retain the associated live writer for close-only cleanup and
    /// retry this core-local cleanup before the core can accept another draft.
    pub(crate) fn confirm_durable_publication(
        &mut self,
        handle: &PrivateWriterTransactionHandle,
        publication: PrivateWriterMetaPublication,
    ) -> Result<(), PrivateWriterTransactionError<E>> {
        self.validate_handle(handle)?;
        if self.state.get() == PrivateWriterTransactionState::CommittedCleanupRequired {
            return Err(PrivateWriterTransactionError::CommittedCleanupRequired);
        }
        if self.state.get() != PrivateWriterTransactionState::Pending
            || self.commit_phase.get() != PrivateWriterCommitPhase::MetaPublicationAuthorized
            || publication.handle_identity != self.handle_identity
            || self.target != Some(publication.target)
        {
            return Err(PrivateWriterTransactionError::StaleHandle);
        }

        // The physical publisher has already made this exact meta durable.
        // Preserve that fact before doing any fallible in-memory cleanup.
        self.selected = publication.target;
        self.target = None;
        self.state
            .set(PrivateWriterTransactionState::CommittedCleanupRequired);
        self.retry_committed_cleanup(handle)
    }

    /// Records the phase-3/4 ambiguity after the physical publisher has
    /// entered target-meta publication. This is deliberately conservative: a
    /// later resolver decides whether `target` became durable, so normal work
    /// and abort are both forbidden on this core.
    ///
    /// The Linux publication bridge calls this only after it holds the exact
    /// phase-1 authorization. If an internal invariant is already broken, the
    /// core is still poisoned before the error returns; falling back to abort
    /// would describe an unproved on-disk generation as absent.
    pub(crate) fn mark_publication_outcome_unknown(
        &mut self,
        handle: &PrivateWriterTransactionHandle,
        publication: &PrivateWriterMetaPublication,
    ) -> Result<(), PrivateWriterTransactionError<E>> {
        let valid = handle.identity != 0
            && handle.identity == self.handle_identity
            && self.state.get() == PrivateWriterTransactionState::Pending
            && self.commit_phase.get() == PrivateWriterCommitPhase::MetaPublicationAuthorized
            && publication.handle_identity == self.handle_identity
            && self.target == Some(publication.target);
        self.state
            .set(PrivateWriterTransactionState::OutcomeUnknown);
        if valid {
            Ok(())
        } else {
            Err(PrivateWriterTransactionError::StaleHandle)
        }
    }

    /// Conservatively disables this core when a physical publisher reports an
    /// ambiguous publication but its matching phase-1 authority is unavailable
    /// due to an internal invariant failure. It exists only to keep that broken
    /// path from falling back to abort.
    pub(crate) fn force_publication_outcome_unknown(&self) {
        self.state
            .set(PrivateWriterTransactionState::OutcomeUnknown);
    }

    /// Retries only post-durability core cleanup. It cannot modify the selected
    /// generation or convert the durable commit into an abort.
    pub(crate) fn retry_committed_cleanup(
        &mut self,
        handle: &PrivateWriterTransactionHandle,
    ) -> Result<(), PrivateWriterTransactionError<E>> {
        self.validate_handle_identity(handle)?;
        if self.state.get() != PrivateWriterTransactionState::CommittedCleanupRequired {
            return Err(PrivateWriterTransactionError::NoPendingTransaction);
        }
        if self.commit_phase.get() != PrivateWriterCommitPhase::MetaPublicationAuthorized
            || self.target.is_some()
        {
            return Err(PrivateWriterTransactionError::CommittedCleanupIncompleteCoordination);
        }
        if self.handle_identity == usize::MAX || self.abort_identity != self.handle_identity + 1 {
            return Err(PrivateWriterTransactionError::CommittedCleanupIncompleteIdentity);
        }

        if self.draft.is_some() {
            let draft = self.draft.take().expect("draft ownership checked above");
            match draft.discard_transaction_draft() {
                Ok((slots, _visits)) => {
                    self.clean_slots = Some(slots);
                    self.fixed_point = None;
                    self.fixed_point_registered_work.set(0);
                    self.fixed_point_registered_generation.set(0);
                    self.fixed_point_registered_phase
                        .set(PrivatePageCoordinatorWorkPhase::None);
                    self.fixed_point_workspace_identity.set(0);
                    self.fixed_point_workspace_bytes.set(0);
                }
                Err((draft, error)) => {
                    self.draft = Some(draft);
                    return Err(
                        PrivateWriterTransactionError::CommittedCleanupIncompletePool(error),
                    );
                }
            }
        }

        if self.resources.current() != PrivateWriterResourceDelta::default() {
            return Err(PrivateWriterTransactionError::CommittedCleanupIncompleteResource);
        }
        if !self.cleanup.is_empty() || !self.coordination.is_none() {
            return Err(PrivateWriterTransactionError::CommittedCleanupIncompleteCoordination);
        }
        if self.clean_slots.is_none()
            || self.draft.is_some()
            || self.fixed_point.is_some()
            || self.fixed_point_registered_work.get() != 0
            || self.fixed_point_registered_generation.get() != 0
            || self.fixed_point_registered_phase.get() != PrivatePageCoordinatorWorkPhase::None
            || self.fixed_point_workspace_identity.get() != 0
            || self.fixed_point_workspace_bytes.get() != 0
        {
            return Err(PrivateWriterTransactionError::CommittedCleanupIncompleteCoordination);
        }

        self.handle_identity = self.abort_identity;
        self.abort_identity = 0;
        self.commit_phase.set(PrivateWriterCommitPhase::Idle);
        self.state.set(PrivateWriterTransactionState::Clean);
        Ok(())
    }

    #[cfg(test)]
    pub(crate) fn complete_fixed_point_work(
        &self,
        handle: &PrivateWriterTransactionHandle,
        active: FixedPointActiveWork<'_>,
    ) -> Result<FixedPointPredecessor, FixedPointError> {
        if self.validate_handle(handle).is_err()
            || self.state.get() != PrivateWriterTransactionState::Pending
            || self.commit_phase.get() != PrivateWriterCommitPhase::Idle
            || self.fixed_point_registered_work.get() == 0
            || self.fixed_point_registered_phase.get() != PrivatePageCoordinatorWorkPhase::Active
        {
            return Err(FixedPointError::StalePredecessor);
        }
        let draft = self.draft.as_ref().ok_or(FixedPointError::AbortRequired)?;
        let coordinator = self
            .fixed_point
            .as_ref()
            .ok_or(FixedPointError::AbortRequired)?;
        match coordinator.complete_work(draft, active) {
            Ok(successor) => {
                self.fixed_point_registered_work.set(0);
                self.fixed_point_registered_generation.set(0);
                self.fixed_point_registered_phase
                    .set(PrivatePageCoordinatorWorkPhase::None);
                Ok(successor)
            }
            Err(error) => {
                self.state.set(PrivateWriterTransactionState::AbortRequired);
                Err(error)
            }
        }
    }

    pub(crate) fn fixed_point_failed(
        &self,
        handle: &PrivateWriterTransactionHandle,
    ) -> Result<(), PrivateWriterTransactionError<E>> {
        self.validate_handle(handle)?;
        if self.state.get() != PrivateWriterTransactionState::Pending
            || self.commit_phase.get() != PrivateWriterCommitPhase::Idle
        {
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        let coordinator = self
            .fixed_point
            .as_ref()
            .ok_or(PrivateWriterTransactionError::AbortRequired(None))?;
        let draft = self
            .draft
            .as_ref()
            .ok_or(PrivateWriterTransactionError::AbortRequired(None))?;
        let pool_failed = draft.coordinator_work_failed();
        let coordinator_failed = coordinator.registered_work_failed();
        if pool_failed || coordinator_failed || coordinator.requires_abort() {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        Ok(())
    }

    pub(crate) fn operation_failed(
        &self,
        handle: &PrivateWriterTransactionHandle,
        operation: PrivatePageScopedOperation<'_>,
    ) -> Result<(), PrivateWriterTransactionError<E>> {
        self.validate_handle(handle)?;
        if self.state.get() != PrivateWriterTransactionState::Pending
            || self.commit_phase.get() != PrivateWriterCommitPhase::Idle
        {
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        let Some(draft) = self.draft.as_ref() else {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        };
        match draft.abandon_unmutated_operation(operation) {
            Ok(()) => Ok(()),
            Err((_operation, error)) => {
                if draft.requires_abort() {
                    self.state.set(PrivateWriterTransactionState::AbortRequired);
                    Err(PrivateWriterTransactionError::AbortRequired(Some(error)))
                } else {
                    Err(PrivateWriterTransactionError::Pool(error))
                }
            }
        }
    }

    pub(crate) fn preflight_commit(
        &self,
        handle: &PrivateWriterTransactionHandle,
    ) -> Result<(), PrivateWriterTransactionError<E>> {
        self.validate_handle(handle)?;
        if matches!(
            self.state.get(),
            PrivateWriterTransactionState::AbortRequired
                | PrivateWriterTransactionState::AbortIncomplete
        ) {
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        if self.state.get() == PrivateWriterTransactionState::CommittedCleanupRequired {
            return Err(PrivateWriterTransactionError::CommittedCleanupRequired);
        }
        if self.state.get() != PrivateWriterTransactionState::Pending {
            return Err(PrivateWriterTransactionError::NoPendingTransaction);
        }
        let draft = self
            .draft
            .as_ref()
            .ok_or(PrivateWriterTransactionError::AbortRequired(None))?;
        if draft.requires_abort() {
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        let coordinator = self
            .fixed_point
            .as_ref()
            .ok_or(PrivateWriterTransactionError::AbortRequired(None))?;
        if coordinator.requires_abort() {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        let (pool_work, pool_generation, pool_phase) = draft.coordinator_registered_work();
        let mirror_work = self.fixed_point_registered_work.get();
        let mirror_generation = self.fixed_point_registered_generation.get();
        let mirror_phase = self.fixed_point_registered_phase.get();
        let registration_consistent = if mirror_phase == PrivatePageCoordinatorWorkPhase::None {
            mirror_work == 0
                && mirror_generation == 0
                && pool_work == 0
                && pool_phase == PrivatePageCoordinatorWorkPhase::None
                && coordinator.registered_work() == 0
        } else {
            mirror_phase == PrivatePageCoordinatorWorkPhase::Active
                && mirror_work != 0
                && mirror_work == pool_work
                && mirror_work == coordinator.registered_work()
                && mirror_generation == pool_generation
                && pool_phase == mirror_phase
        };
        if !registration_consistent {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        let target = self
            .target
            .ok_or(PrivateWriterTransactionError::AbortRequired(None))?;
        if !coordinator.is_quiescent() {
            return Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::StalePredecessor,
            ));
        }
        if draft.has_active_operation() {
            return Err(PrivateWriterTransactionError::Pool(
                PrivatePagePoolError::OperationActive,
            ));
        }
        if draft.has_active_checkpoint() {
            return Err(PrivateWriterTransactionError::Pool(
                PrivatePagePoolError::CheckpointActive,
            ));
        }
        draft
            .coordinator_commit_fence()
            .map_err(PrivateWriterTransactionError::Pool)?;
        if target.page_count != draft.pending_page_count()
            || coordinator
                .commit_fence(draft, target.free_bitmap_root, draft.pending_page_count())
                .is_err()
        {
            self.state.set(PrivateWriterTransactionState::AbortRequired);
            return Err(PrivateWriterTransactionError::AbortRequired(None));
        }
        Ok(())
    }

    pub(crate) fn record_interrupted_cleanup(
        &mut self,
        index: usize,
        cause: E,
    ) -> Result<(), (E, PrivateWriterTransactionError<E>)> {
        self.cleanup
            .record_interrupted_error(index, cause)
            .map_err(|(cause, error)| {
                (
                    cause,
                    PrivateWriterTransactionError::AbortIncompleteCleanupContract(error),
                )
            })
    }

    pub(crate) fn abort(&mut self) -> Result<usize, PrivateWriterTransactionError<&'_ E>> {
        self.abort_with_cleanup::<fn(&I, &mut O) -> Result<(), E>>(None)
    }

    pub(crate) fn abort_with_cleanup<F>(
        &mut self,
        executor: Option<&mut F>,
    ) -> Result<usize, PrivateWriterTransactionError<&'_ E>>
    where
        F: FnMut(&I, &mut O) -> Result<(), E>,
    {
        if self.state.get() == PrivateWriterTransactionState::Clean {
            return Err(PrivateWriterTransactionError::NoPendingTransaction);
        }
        if self.state.get() == PrivateWriterTransactionState::OutcomeUnknown {
            return Err(PrivateWriterTransactionError::OutcomeUnknown);
        }
        if self.state.get() == PrivateWriterTransactionState::CommittedCleanupRequired {
            return Err(PrivateWriterTransactionError::CommittedCleanupRequired);
        }
        if self.fixed_point_workspace_identity.get() != 0
            || self.fixed_point_workspace_bytes.get() != 0
        {
            return Err(PrivateWriterTransactionError::AbortIncompleteResource);
        }

        if self.draft.is_some() {
            if self.handle_identity == usize::MAX || self.abort_identity != self.handle_identity + 1
            {
                self.state
                    .set(PrivateWriterTransactionState::AbortIncomplete);
                return Err(PrivateWriterTransactionError::AbortIncompleteIdentity);
            }
            let draft = self.draft.take().expect("draft ownership checked above");
            match draft.discard_transaction_draft() {
                Ok((slots, visits)) => {
                    self.clean_slots = Some(slots);
                    self.fixed_point = None;
                    self.fixed_point_registered_work.set(0);
                    self.fixed_point_registered_generation.set(0);
                    self.fixed_point_registered_phase
                        .set(PrivatePageCoordinatorWorkPhase::None);
                    self.fixed_point_workspace_identity.set(0);
                    self.fixed_point_workspace_bytes.set(0);
                    self.target = None;
                    self.handle_identity = self.abort_identity;
                    self.abort_identity = 0;
                    self.abort_visits = self.abort_visits.saturating_add(visits);
                    self.state
                        .set(PrivateWriterTransactionState::AbortIncomplete);
                }
                Err((draft, error)) => {
                    self.draft = Some(draft);
                    self.state
                        .set(PrivateWriterTransactionState::AbortIncomplete);
                    return Err(PrivateWriterTransactionError::AbortIncompletePool(error));
                }
            }
        }

        let cleanup_remaining = if self.cleanup.is_empty() {
            0
        } else {
            self.cleanup
                .retry_all(executor)
                .map_err(PrivateWriterTransactionError::AbortIncompleteCleanupContract)?
                .remaining()
        };
        for index in 0..cleanup_remaining {
            if let Some(cause) = self.cleanup.last_error(index) {
                return Err(PrivateWriterTransactionError::AbortIncompleteCleanup(cause));
            }
        }
        if cleanup_remaining != 0 {
            return Err(PrivateWriterTransactionError::AbortIncompleteCoordination);
        }
        if self.resources.current() != PrivateWriterResourceDelta::default() {
            return Err(PrivateWriterTransactionError::AbortIncompleteResource);
        }
        if !self.coordination.is_none() {
            return Err(PrivateWriterTransactionError::AbortIncompleteCoordination);
        }
        if self.clean_slots.is_none()
            || self.draft.is_some()
            || self.fixed_point.is_some()
            || self.fixed_point_workspace_identity.get() != 0
            || self.fixed_point_workspace_bytes.get() != 0
        {
            return Err(PrivateWriterTransactionError::AbortIncompleteCoordination);
        }
        self.commit_phase.set(PrivateWriterCommitPhase::Idle);
        self.state.set(PrivateWriterTransactionState::Clean);
        Ok(self.abort_visits)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::contract::{AddressFamily, ValueKind, ValueTag};
    use crate::private_page_pool::{
        PrivatePageAuthorization, PrivatePageOwner, PrivatePageScopedOperationSlot,
    };
    use crate::test_alloc::count_thread_allocations;

    #[derive(Debug, PartialEq, Eq)]
    struct CleanupError(u32);

    #[derive(Debug, PartialEq, Eq)]
    struct CloneTrap(u32);

    impl Clone for CloneTrap {
        fn clone(&self) -> Self {
            CLONE_TRAP_CALLS.fetch_add(1, Ordering::Relaxed);
            panic!("cleanup causes must never be cloned")
        }
    }

    static CLONE_TRAP_CALLS: AtomicUsize = AtomicUsize::new(0);

    fn meta(txn_id: u64) -> MetaV4 {
        MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [1; 16],
            txn_id,
            commit_nonce: [2; 16],
            page_count: 2,
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
        }
    }

    fn budget(pages: u64) -> PrivateWriterResourceBudget {
        PrivateWriterResourceBudget::new(1024, pages, pages, 2)
    }

    #[test]
    fn begin_and_abort_move_the_only_slot_owner_and_stale_the_handle() {
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(2),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        assert_eq!(core.state(), PrivateWriterTransactionState::Pending);
        assert!(core.clean_slots.is_none());
        assert!(core.draft.is_some());
        assert_eq!(core.target().unwrap().txn_id, 8);
        assert_eq!(core.abort().unwrap(), 2);
        assert_eq!(core.state(), PrivateWriterTransactionState::Clean);
        assert!(core.clean_slots.is_some());
        assert!(core.draft.is_none());
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::StaleHandle)
        );
    }

    #[test]
    fn pre_mutation_failure_is_neutral_and_post_mutation_failure_poisons() {
        let mut slots = [PrivatePagePoolSlot::empty(); 1];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let scope = core
            .draft(&handle)
            .unwrap()
            .test_reserve_scope_direct(1)
            .unwrap();
        let mut plan = [];
        let operation = core
            .draft(&handle)
            .unwrap()
            .preflight_operation_in_scope(&scope, 0, &mut plan)
            .unwrap();
        core.operation_failed(&handle, operation).unwrap();
        assert_eq!(core.state(), PrivateWriterTransactionState::Pending);

        let checkpoint = core
            .draft(&handle)
            .unwrap()
            .test_begin_checkpoint_direct()
            .unwrap();
        core.draft(&handle)
            .unwrap()
            .bind_page(&checkpoint, &scope, 2, PrivatePageAuthorization::Appended)
            .unwrap();
        core.draft(&handle)
            .unwrap()
            .commit_checkpoint(checkpoint)
            .unwrap();
        let info = core
            .draft(&handle)
            .unwrap()
            .scoped_slot_info(&scope, 0)
            .unwrap()
            .unwrap();
        let mut plan = [PrivatePageScopedOperationSlot::new(
            0,
            info.binding_epoch,
            1,
        )];
        let mut operation = core
            .draft(&handle)
            .unwrap()
            .preflight_operation_in_scope(&scope, 1, &mut plan)
            .unwrap();
        core.draft(&handle)
            .unwrap()
            .claim_slot_for_operation_in_scope_prepared(
                &operation,
                0,
                PrivatePageOwner::Bitmap,
                8,
                1,
            )
            .unwrap();
        PrivatePagePool::test_corrupt_operation_identity(&mut operation);
        assert_eq!(
            core.operation_failed(&handle, operation),
            Err(PrivateWriterTransactionError::AbortRequired(Some(
                PrivatePagePoolError::AbortRequired
            )))
        );
        assert_eq!(core.state(), PrivateWriterTransactionState::AbortRequired);
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::AbortRequired(None))
        );
        assert_eq!(core.abort().unwrap(), 1);
    }

    #[test]
    fn fixed_point_authority_is_transaction_owned_and_commit_requires_final_fence() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let predecessor = core.fixed_point(&handle).unwrap().predecessor().unwrap();
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::StalePredecessor
            ))
        );
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let prepared = core
            .fixed_point(&handle)
            .unwrap()
            .prepare_work(
                &predecessor,
                core.draft(&handle).unwrap(),
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || {
                    Ok(FixedPointPreparedOutput {
                        root: 0,
                        pending_page_count: 2,
                    })
                },
            )
            .unwrap();
        let active = core
            .execute_fixed_point_work(&handle, predecessor, prepared)
            .unwrap();
        let (pool_work, pool_generation, pool_phase) =
            core.draft(&handle).unwrap().coordinator_registered_work();
        assert_eq!(pool_work, core.fixed_point_registered_work.get());
        assert_eq!(
            pool_generation,
            core.fixed_point_registered_generation.get()
        );
        assert_eq!(pool_phase, core.fixed_point_registered_phase.get());
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::StalePredecessor
            ))
        );
        let successor = core.complete_fixed_point_work(&handle, active).unwrap();
        core.fixed_point(&handle)
            .unwrap()
            .finish(successor)
            .unwrap();
        core.preflight_commit(&handle).unwrap();

        assert_eq!(core.abort().unwrap(), 1);
        let next = core.begin([4; 16]).unwrap();
        assert!(core.fixed_point(&next).unwrap().is_quiescent());
        assert_eq!(core.abort().unwrap(), 1);
    }

    #[test]
    fn commit_rejects_a_callback_output_not_proven_by_the_pool() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let predecessor = core.fixed_point(&handle).unwrap().predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let prepared = core
            .fixed_point(&handle)
            .unwrap()
            .prepare_work(
                &predecessor,
                core.draft(&handle).unwrap(),
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || {
                    Ok(FixedPointPreparedOutput {
                        root: 0,
                        pending_page_count: 3,
                    })
                },
            )
            .unwrap();
        let active = core
            .execute_fixed_point_work(&handle, predecessor, prepared)
            .unwrap();
        let successor = core.complete_fixed_point_work(&handle, active).unwrap();
        core.fixed_point(&handle)
            .unwrap()
            .finish(successor)
            .unwrap();

        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::AbortRequired(None))
        );
        assert_eq!(core.state(), PrivateWriterTransactionState::AbortRequired);
        assert_eq!(core.abort().unwrap(), 1);
    }

    #[test]
    fn dropping_active_fixed_point_authority_does_not_clear_commit_fences() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let predecessor = core.fixed_point(&handle).unwrap().predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        {
            let prepared = core
                .fixed_point(&handle)
                .unwrap()
                .prepare_work(
                    &predecessor,
                    core.draft(&handle).unwrap(),
                    1,
                    1,
                    &mut work_slot,
                    &mut scope_slot,
                    &mut scratch,
                    || {
                        Ok(FixedPointPreparedOutput {
                            root: 0,
                            pending_page_count: 2,
                        })
                    },
                )
                .unwrap();
            let _active = core
                .execute_fixed_point_work(&handle, predecessor, prepared)
                .unwrap();
        }

        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::FixedPoint(
                FixedPointError::StalePredecessor
            ))
        );
        assert_eq!(
            core.fixed_point_failed(&handle),
            Err(PrivateWriterTransactionError::AbortRequired(None))
        );
        assert_eq!(core.abort().unwrap(), 1);
    }

    #[test]
    fn fixed_point_owned_page_or_post_mutation_failure_requires_whole_draft_abort() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        core.fixed_point_failed(&handle).unwrap();
        assert_eq!(core.state(), PrivateWriterTransactionState::Pending);
        let predecessor = core.fixed_point(&handle).unwrap().predecessor().unwrap();
        assert_eq!(
            core.fixed_point(&handle)
                .unwrap()
                .reject_advertised_owned(predecessor, 9)
                .unwrap_err()
                .1,
            FixedPointError::AdvertisedOwnedPage(9)
        );
        assert!(matches!(
            core.fixed_point(&handle),
            Err(PrivateWriterTransactionError::AbortRequired(None))
        ));
        assert_eq!(core.state(), PrivateWriterTransactionState::AbortRequired);
        assert_eq!(core.abort().unwrap(), 1);

        let handle = core.begin([4; 16]).unwrap();
        let predecessor = core.fixed_point(&handle).unwrap().predecessor().unwrap();
        let mut work_slot = FixedPointPreparedWorkSlot::empty();
        let mut scope_slot = PrivatePagePreparedScopeSlot::empty();
        let mut scratch = [];
        let prepared = core
            .fixed_point(&handle)
            .unwrap()
            .prepare_work(
                &predecessor,
                core.draft(&handle).unwrap(),
                1,
                1,
                &mut work_slot,
                &mut scope_slot,
                &mut scratch,
                || {
                    Ok(FixedPointPreparedOutput {
                        root: 0,
                        pending_page_count: 2,
                    })
                },
            )
            .unwrap();
        let _active = core
            .execute_fixed_point_work(&handle, predecessor, prepared)
            .unwrap();
        assert_eq!(
            core.fixed_point_failed(&handle),
            Err(PrivateWriterTransactionError::AbortRequired(None))
        );
        assert!(core.draft.as_ref().unwrap().requires_abort());
        assert!(core.fixed_point.as_ref().unwrap().requires_abort());
        assert_eq!(core.state(), PrivateWriterTransactionState::AbortRequired);
        assert_eq!(core.abort().unwrap(), 1);
    }

    #[test]
    fn active_operation_blocks_commit_until_exact_finish_or_abandon() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let scope = core
            .draft(&handle)
            .unwrap()
            .test_reserve_scope_direct(1)
            .unwrap();

        let mut abandon_plan = [];
        let operation = core
            .draft(&handle)
            .unwrap()
            .preflight_operation_in_scope(&scope, 0, &mut abandon_plan)
            .unwrap();
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::Pool(
                PrivatePagePoolError::OperationActive
            ))
        );
        core.operation_failed(&handle, operation).unwrap();
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::Pool(
                PrivatePagePoolError::ScopeNotEmpty(1)
            ))
        );

        let mut finish_plan = [];
        let operation = core
            .draft(&handle)
            .unwrap()
            .preflight_operation_in_scope(&scope, 0, &mut finish_plan)
            .unwrap();
        core.draft(&handle)
            .unwrap()
            .finish_operation_in_scope(operation)
            .unwrap();
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::Pool(
                PrivatePagePoolError::ScopeNotEmpty(1)
            ))
        );

        let mut dropped_plan = [];
        {
            let _operation = core
                .draft(&handle)
                .unwrap()
                .preflight_operation_in_scope(&scope, 0, &mut dropped_plan)
                .unwrap();
        }
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::Pool(
                PrivatePagePoolError::OperationActive
            ))
        );
        assert_eq!(core.abort().unwrap(), 1);
    }

    #[test]
    fn dropped_partially_mutated_operation_is_discarded_only_by_abort() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let scope = core
            .draft(&handle)
            .unwrap()
            .test_reserve_scope_direct(1)
            .unwrap();
        let checkpoint = core
            .draft(&handle)
            .unwrap()
            .test_begin_checkpoint_direct()
            .unwrap();
        core.draft(&handle)
            .unwrap()
            .bind_page(&checkpoint, &scope, 2, PrivatePageAuthorization::Appended)
            .unwrap();
        core.draft(&handle)
            .unwrap()
            .commit_checkpoint(checkpoint)
            .unwrap();
        let info = core
            .draft(&handle)
            .unwrap()
            .scoped_slot_info(&scope, 0)
            .unwrap()
            .unwrap();
        let mut plan = [PrivatePageScopedOperationSlot::new(
            0,
            info.binding_epoch,
            1,
        )];
        let stale = {
            let operation = core
                .draft(&handle)
                .unwrap()
                .preflight_operation_in_scope(&scope, 1, &mut plan)
                .unwrap();
            core.draft(&handle)
                .unwrap()
                .claim_slot_for_operation_in_scope_prepared(
                    &operation,
                    0,
                    PrivatePageOwner::Bitmap,
                    8,
                    1,
                )
                .unwrap();
            PrivatePagePool::test_duplicate_operation(&operation)
        };
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::Pool(
                PrivatePagePoolError::OperationActive
            ))
        );
        assert_eq!(core.abort().unwrap(), 1);

        let successor = core.begin([4; 16]).unwrap();
        let _successor_scope = core
            .draft(&successor)
            .unwrap()
            .test_reserve_scope_direct(1)
            .unwrap();
        let before = core.draft(&successor).unwrap().test_mutation_snapshot();
        assert_eq!(
            core.draft(&successor)
                .unwrap()
                .claim_slot_for_operation_in_scope_prepared(
                    &stale,
                    0,
                    PrivatePageOwner::Bitmap,
                    9,
                    2,
                ),
            Err(PrivatePagePoolError::PoolMismatch)
        );
        assert_eq!(
            core.draft(&successor).unwrap().test_mutation_snapshot(),
            before
        );
        assert_eq!(core.abort().unwrap(), 1);
    }

    #[test]
    fn forged_zero_mutation_failure_does_not_poison_or_clear_current_operation() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let scope = core
            .draft(&handle)
            .unwrap()
            .test_reserve_scope_direct(1)
            .unwrap();
        let mut plan = [];
        let operation = core
            .draft(&handle)
            .unwrap()
            .preflight_operation_in_scope(&scope, 0, &mut plan)
            .unwrap();
        let mut forged = PrivatePagePool::test_duplicate_operation(&operation);
        PrivatePagePool::test_corrupt_operation_identity(&mut forged);
        assert_eq!(
            core.operation_failed(&handle, forged),
            Err(PrivateWriterTransactionError::Pool(
                PrivatePagePoolError::PoolMismatch
            ))
        );
        assert_eq!(core.state(), PrivateWriterTransactionState::Pending);
        assert!(!core.draft(&handle).unwrap().requires_abort());
        assert!(core.draft(&handle).unwrap().has_active_operation());
        core.operation_failed(&handle, operation).unwrap();
        core.draft(&handle).unwrap().close_scope(&scope).unwrap();
        core.preflight_commit(&handle).unwrap();
        assert_eq!(core.abort().unwrap(), 1);
    }

    #[test]
    fn cleanup_and_resource_retries_never_rescrub() {
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 3];
        let mut cleanup = [None];
        let mut core =
            PrivateWriterTransactionCore::new(meta(7), budget(3), &mut slots, &mut cleanup)
                .unwrap();
        let _handle = core.begin([3; 16]).unwrap();
        core.cleanup_mut().append(7u32, 0u32).unwrap();
        let mut fail_once = |_: &u32, attempts: &mut u32| {
            *attempts += 1;
            if *attempts == 1 {
                Err(CleanupError(17))
            } else {
                Ok(())
            }
        };
        assert_eq!(
            core.abort_with_cleanup(Some(&mut fail_once)),
            Err(PrivateWriterTransactionError::AbortIncompleteCleanup(
                &CleanupError(17)
            ))
        );
        assert_eq!(core.abort_visits(), 3);
        assert!(core.clean_slots.is_some());
        assert!(core.draft.is_none());
        assert_eq!(core.abort_with_cleanup(Some(&mut fail_once)).unwrap(), 3);

        let handle = core.begin([4; 16]).unwrap();
        core.resources_mut()
            .acquire(PrivateWriterResourceDelta::new(1, 1, 1, 1))
            .unwrap();
        assert_eq!(
            core.abort(),
            Err(PrivateWriterTransactionError::AbortIncompleteResource)
        );
        assert_eq!(core.abort_visits(), 3);
        core.resources_mut()
            .release(PrivateWriterResourceDelta::new(1, 1, 1, 1))
            .unwrap();
        assert_eq!(core.abort().unwrap(), 3);
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::StaleHandle)
        );
    }

    #[test]
    fn abort_borrows_exact_nonclone_cause_without_allocation_or_clone() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [None];
        let mut core =
            PrivateWriterTransactionCore::new(meta(7), budget(1), &mut slots, &mut cleanup)
                .unwrap();
        let _handle = core.begin([3; 16]).unwrap();
        core.cleanup_mut().append(7u32, 0u32).unwrap();
        CLONE_TRAP_CALLS.store(0, Ordering::Relaxed);
        let mut fail = |_: &u32, _: &mut u32| Err(CloneTrap(41));
        let ((cause_ptr, cause_value), allocations) = count_thread_allocations(|| {
            match core.abort_with_cleanup(Some(&mut fail)).unwrap_err() {
                PrivateWriterTransactionError::AbortIncompleteCleanup(cause) => {
                    (core::ptr::from_ref(cause), cause.0)
                }
                error => panic!("unexpected abort result: {error:?}"),
            }
        });
        assert_eq!(allocations, 0);
        assert_eq!(CLONE_TRAP_CALLS.load(Ordering::Relaxed), 0);
        assert_eq!(cause_value, 41);
        assert_eq!(
            cause_ptr,
            core::ptr::from_ref(core.cleanup.last_error(0).unwrap())
        );
        let mut succeeds = |_: &u32, _: &mut u32| Ok(());
        assert_eq!(core.abort_with_cleanup(Some(&mut succeeds)).unwrap(), 1);
    }

    #[test]
    fn interrupted_cleanup_is_recorded_and_retried_without_rescrub() {
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let mut cleanup = [None];
        let mut core =
            PrivateWriterTransactionCore::new(meta(7), budget(2), &mut slots, &mut cleanup)
                .unwrap();
        let _handle = core.begin([3; 16]).unwrap();
        core.cleanup_mut().append(9u32, 0u32).unwrap();
        let mut interrupted = |_: &u32, _: &mut u32| -> Result<(), CleanupError> {
            panic!("injected cleanup interruption")
        };
        let unwind = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            let _ = core.abort_with_cleanup(Some(&mut interrupted));
        }));
        assert!(unwind.is_err());
        assert_eq!(core.state(), PrivateWriterTransactionState::AbortIncomplete);
        assert_eq!(core.abort_visits(), 2);
        assert!(core.clean_slots.is_some());
        assert!(core.draft.is_none());
        assert_eq!(core.cleanup.interrupted_attempt_index().unwrap(), Some(0));
        core.record_interrupted_cleanup(0, CleanupError(23))
            .unwrap();
        let mut succeeds = |_: &u32, attempts: &mut u32| {
            *attempts += 1;
            Ok(())
        };
        assert_eq!(core.abort_with_cleanup(Some(&mut succeeds)).unwrap(), 2);
        assert_eq!(core.abort_visits(), 2);
    }

    #[test]
    fn construction_and_begin_limits_are_exact_and_failure_atomic() {
        let mut too_many_slots = [
            const { PrivatePagePoolSlot::empty() },
            const { PrivatePagePoolSlot::empty() },
        ];
        let before = too_many_slots.clone();
        let mut cleanup = [];
        assert_eq!(
            PrivateWriterTransactionCore::<(), (), CleanupError>::new(
                meta(7),
                budget(1),
                &mut too_many_slots,
                &mut cleanup,
            )
            .unwrap_err(),
            PrivateWriterTransactionError::InsufficientBudget {
                required: 2,
                actual: 1
            }
        );
        assert_eq!(too_many_slots, before);

        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(u64::MAX),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        assert_eq!(
            core.begin([3; 16]),
            Err(PrivateWriterTransactionError::TransactionExhausted)
        );
        assert_eq!(core.state(), PrivateWriterTransactionState::Clean);
        assert!(core.clean_slots.is_some());
        assert!(core.draft.is_none());
        assert_eq!(
            core.begin([0; 16]),
            Err(PrivateWriterTransactionError::InvalidArgument)
        );
        assert_eq!(core.state(), PrivateWriterTransactionState::Clean);

        let mut no_slots = [];
        let mut cleanup = [];
        let mut empty = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(0),
            &mut no_slots,
            &mut cleanup,
        )
        .unwrap();
        let _handle = empty.begin([3; 16]).unwrap();
        assert_eq!(empty.abort().unwrap(), 0);
    }

    #[test]
    fn whole_draft_abort_work_is_linear_and_allocation_free() {
        fn measure(count: usize) -> (usize, usize) {
            let mut slots: std::vec::Vec<_> =
                (0..count).map(|_| PrivatePagePoolSlot::empty()).collect();
            let mut cleanup = [];
            let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
                meta(7),
                budget(u64::try_from(count).unwrap()),
                &mut slots,
                &mut cleanup,
            )
            .unwrap();
            let (_, allocations) = count_thread_allocations(|| {
                let _handle = core.begin([3; 16]).unwrap();
                assert_eq!(core.abort().unwrap(), count);
            });
            (core.abort_visits(), allocations)
        }

        let small = measure(64);
        let large = measure(1024);
        assert_eq!(small, (64, 0));
        assert_eq!(large, (1024, 0));
    }

    #[test]
    fn dropping_pending_core_runs_no_cleanup_protocol() {
        static CALLBACKS: AtomicUsize = AtomicUsize::new(0);
        CALLBACKS.store(0, Ordering::Relaxed);
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [None];
        {
            let mut core = PrivateWriterTransactionCore::<u32, u32, CleanupError>::new(
                meta(7),
                budget(1),
                &mut slots,
                &mut cleanup,
            )
            .unwrap();
            let _handle = core.begin([3; 16]).unwrap();
            core.cleanup_mut().append(1u32, 0u32).unwrap();
        }
        assert_eq!(CALLBACKS.load(Ordering::Relaxed), 0);
    }

    #[test]
    fn prepared_begin_abort_has_zero_heap_allocation() {
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 4];
        let mut cleanup = [];
        let ((), allocations) = count_thread_allocations(|| {
            let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
                meta(7),
                budget(4),
                &mut slots,
                &mut cleanup,
            )
            .unwrap();
            let _handle = core.begin([3; 16]).unwrap();
            assert_eq!(core.abort().unwrap(), 4);
        });
        assert_eq!(allocations, 0);
    }

    #[test]
    fn durable_publication_advances_selected_only_after_exact_authorization() {
        let mut slots = [const { PrivatePagePoolSlot::empty() }; 2];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(2),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let target = core.target().unwrap();

        core.commit_phase
            .set(PrivateWriterCommitPhase::MetaPublicationAuthorized);
        let publication = PrivateWriterMetaPublication {
            handle_identity: handle.identity,
            target,
        };
        let (result, allocations) =
            count_thread_allocations(|| core.confirm_durable_publication(&handle, publication));
        assert_eq!(allocations, 0);
        assert_eq!(result, Ok(()));
        assert_eq!(core.state(), PrivateWriterTransactionState::Clean);
        assert_eq!(core.selected(), target);
        assert_eq!(core.target(), None);
        assert!(core.clean_slots.is_some());
        assert!(core.draft.is_none());
        assert_eq!(
            core.abort(),
            Err(PrivateWriterTransactionError::NoPendingTransaction)
        );
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::StaleHandle)
        );

        let next = core.begin([4; 16]).unwrap();
        assert_eq!(core.target().unwrap().txn_id, target.txn_id + 1);
        assert_eq!(core.abort().unwrap(), 2);
        assert!(matches!(
            core.preflight_commit(&next),
            Err(PrivateWriterTransactionError::StaleHandle)
        ));
    }

    #[test]
    fn durable_publication_rejects_substituted_authorization_without_advancing_selected() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let target = core.target().unwrap();
        let mut substituted = target;
        substituted.commit_nonce = [4; 16];

        core.commit_phase
            .set(PrivateWriterCommitPhase::MetaPublicationAuthorized);
        assert_eq!(
            core.confirm_durable_publication(
                &handle,
                PrivateWriterMetaPublication {
                    handle_identity: handle.identity,
                    target: substituted,
                },
            ),
            Err(PrivateWriterTransactionError::StaleHandle)
        );
        assert_eq!(core.state(), PrivateWriterTransactionState::Pending);
        assert_eq!(core.selected(), meta(7));
        assert_eq!(core.target(), Some(target));
        assert_eq!(core.abort().unwrap(), 1);
    }

    #[test]
    fn outcome_unknown_publication_is_resolve_only_and_never_reopens_abort() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let target = core.target().unwrap();
        core.commit_phase
            .set(PrivateWriterCommitPhase::MetaPublicationAuthorized);

        core.mark_publication_outcome_unknown(
            &handle,
            &PrivateWriterMetaPublication {
                handle_identity: handle.identity,
                target,
            },
        )
        .unwrap();
        assert_eq!(core.state(), PrivateWriterTransactionState::OutcomeUnknown);
        assert_eq!(core.selected(), meta(7));
        assert_eq!(core.target(), Some(target));
        assert_eq!(
            core.abort(),
            Err(PrivateWriterTransactionError::OutcomeUnknown)
        );
        assert_eq!(
            core.begin([4; 16]),
            Err(PrivateWriterTransactionError::OutcomeUnknown)
        );
        assert_eq!(
            core.preflight_commit(&handle),
            Err(PrivateWriterTransactionError::OutcomeUnknown)
        );
        assert!(matches!(
            core.draft(&handle),
            Err(PrivateWriterTransactionError::OutcomeUnknown)
        ));
        assert_eq!(
            PrivateWriterTransactionError::<CleanupError>::OutcomeUnknown.code(),
            ErrorCode::Unresolvable
        );
    }

    #[test]
    fn malformed_unknown_publication_authority_still_poison_the_core() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let mut target = core.target().unwrap();
        target.commit_nonce = [4; 16];
        core.commit_phase
            .set(PrivateWriterCommitPhase::MetaPublicationAuthorized);

        assert_eq!(
            core.mark_publication_outcome_unknown(
                &handle,
                &PrivateWriterMetaPublication {
                    handle_identity: handle.identity,
                    target,
                },
            ),
            Err(PrivateWriterTransactionError::StaleHandle)
        );
        assert_eq!(core.state(), PrivateWriterTransactionState::OutcomeUnknown);
        assert_eq!(
            core.abort(),
            Err(PrivateWriterTransactionError::OutcomeUnknown)
        );
    }

    #[test]
    fn durable_cleanup_never_reopens_abort_and_can_resume_after_residue_is_removed() {
        let mut slots = [PrivatePagePoolSlot::empty()];
        let mut cleanup = [None];
        let mut core = PrivateWriterTransactionCore::<(), (), CleanupError>::new(
            meta(7),
            budget(1),
            &mut slots,
            &mut cleanup,
        )
        .unwrap();
        let handle = core.begin([3; 16]).unwrap();
        let target = core.target().unwrap();
        core.cleanup_mut().append((), ()).unwrap();
        core.commit_phase
            .set(PrivateWriterCommitPhase::MetaPublicationAuthorized);

        assert_eq!(
            core.confirm_durable_publication(
                &handle,
                PrivateWriterMetaPublication {
                    handle_identity: handle.identity,
                    target,
                },
            ),
            Err(PrivateWriterTransactionError::CommittedCleanupIncompleteCoordination)
        );
        assert_eq!(
            core.state(),
            PrivateWriterTransactionState::CommittedCleanupRequired
        );
        assert_eq!(core.selected(), target);
        assert_eq!(core.target(), None);
        assert!(core.clean_slots.is_some());
        assert!(core.draft.is_none());
        assert_eq!(
            core.abort(),
            Err(PrivateWriterTransactionError::CommittedCleanupRequired)
        );
        assert_eq!(
            core.begin([4; 16]),
            Err(PrivateWriterTransactionError::CommittedCleanupRequired)
        );
        assert!(matches!(
            core.draft(&handle),
            Err(PrivateWriterTransactionError::CommittedCleanupRequired)
        ));

        let mut cleanup_executor = |_: &(), _: &mut ()| Ok::<(), CleanupError>(());
        assert_eq!(
            core.cleanup_mut()
                .retry_all(Some(&mut cleanup_executor))
                .unwrap()
                .remaining(),
            0
        );
        core.retry_committed_cleanup(&handle).unwrap();
        assert_eq!(core.state(), PrivateWriterTransactionState::Clean);
        assert_eq!(core.selected(), target);
    }

    #[test]
    fn terminal_writer_identity_pair_is_usable_once() {
        let counter = AtomicUsize::new(usize::MAX - 1);
        assert_eq!(
            reserve_writer_identity_pair_from(&counter),
            Some((usize::MAX - 1, usize::MAX))
        );
        assert_eq!(reserve_writer_identity_pair_from(&counter), None);
    }
}
