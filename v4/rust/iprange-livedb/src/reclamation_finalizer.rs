//! Lock-bound input construction for private bitmap/retirement finalization.
//!
//! This module deliberately stops before retirement output construction and
//! coordinator execution. Its one job is to make the required live sequence
//! non-optional: use the selected source and held reader fence to verify a
//! reclaimable prefix, bind bitmap pages in a caller-owned shadow scope, and
//! apply the planned bitmap reservation before later finalization consumes it.

use crate::bitmap_cow::{
    BoundFreeBitmapReservation, FreeBitmapCowError, FreeBitmapReservationBuffers,
    FreeBitmapReservationPlanner,
};
use crate::contract::MetaV4;
use crate::page_source::CommittedPageSource;
use crate::private_page_pool::{PrivatePagePool, PrivatePageReservationScope};
use crate::retirement_page::RetirementBatch;
use crate::retirement_reader::{
    RetirementIdentity, RetirementReadError, RetirementReclaimFence,
    RetirementReclamationExecutionError, RetirementTree,
};

/// Bounded work limits and bitmap payload capacity for one lock-held attempt.
///
/// All backing storage remains caller-owned in
/// [`LockedReclamationFinalizerScratch`]. The private helper rejects zero
/// limits before it reads the selected source or changes the shadow scope.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct LockedReclamationFinalizerLimits {
    pub(crate) max_batches: u64,
    pub(crate) max_pages: u64,
    pub(crate) bitmap_payload_pages: usize,
}

/// Caller-owned scratch retained for the complete selection and bitmap-binding
/// phase of one lock-bound reclamation finalizer.
#[derive(Debug)]
pub(crate) struct LockedReclamationFinalizerScratch<'a> {
    pub(crate) bitmap: FreeBitmapReservationBuffers<'a>,
    pub(crate) verified_batches: &'a mut [RetirementBatch],
    pub(crate) verified_pages: &'a mut [u32],
}

/// Typed failure before a finalizer can construct terminal output or publish.
#[derive(Debug)]
pub(crate) enum LockedReclamationFinalizerError<E> {
    InvalidLimits,
    Retirement(RetirementReadError),
    Bitmap(FreeBitmapCowError),
    Consumer(E),
}

#[derive(Debug)]
enum LockedReclamationConsumerError<E> {
    Bitmap(FreeBitmapCowError),
    Consumer(E),
}

/// Runs the mandatory lock-held prefix of a bitmap/retirement finalizer.
///
/// The returned/consumed bitmap reservation retains the live reader-barrier
/// authority. Consequently, no caller can bind pages from an arbitrary slice,
/// skip verification, or continue finalization after the operation barrier
/// has been released.
#[allow(
    clippy::result_large_err,
    clippy::too_many_arguments,
    clippy::type_complexity
)]
pub(crate) fn with_locked_reclamation_bitmap_reservation<
    'a,
    'slots,
    'scope,
    'barrier,
    S: CommittedPageSource + ?Sized,
    R,
    E,
    F,
>(
    selected: MetaV4,
    pages: &'a S,
    reclaim_fence: RetirementReclaimFence<'barrier>,
    limits: LockedReclamationFinalizerLimits,
    scratch: LockedReclamationFinalizerScratch<'a>,
    shadow_pool: &'a PrivatePagePool<'slots>,
    shadow_scope: &'a PrivatePageReservationScope<'scope>,
    consume: F,
) -> Result<R, LockedReclamationFinalizerError<E>>
where
    F: FnOnce(
        crate::retirement_reader::RetirementPassResult,
        BoundFreeBitmapReservation<'a, 'slots, 'scope, 'barrier, 'a, S>,
    ) -> Result<R, E>,
{
    if limits.max_batches == 0 || limits.max_pages == 0 || limits.bitmap_payload_pages == 0 {
        return Err(LockedReclamationFinalizerError::InvalidLimits);
    }

    let identity = RetirementIdentity {
        database_id: selected.database_id,
        txn_id: selected.txn_id,
        commit_nonce: selected.commit_nonce,
        page_count: selected.page_count,
        root: selected.retirement_root,
        batch_count: selected.retirement_batch_count,
    };
    let tree = RetirementTree::from_source(pages, identity)
        .map_err(LockedReclamationFinalizerError::Retirement)?;
    let LockedReclamationFinalizerScratch {
        bitmap,
        verified_batches,
        verified_pages,
    } = scratch;

    match tree.with_reclamation(
        reclaim_fence,
        limits.max_batches,
        limits.max_pages,
        verified_batches,
        verified_pages,
        |pass, reclamation| {
            let planner = FreeBitmapReservationPlanner::new(
                pages,
                selected.txn_id,
                selected.page_count,
                selected.free_bitmap_root,
                limits.bitmap_payload_pages,
                bitmap,
            )
            .map_err(LockedReclamationConsumerError::Bitmap)?;
            let locked = planner
                .plan_under_reclamation(reclamation)
                .map_err(LockedReclamationConsumerError::Bitmap)?;
            let mut bound = locked
                .bind(shadow_pool, shadow_scope)
                .map_err(LockedReclamationConsumerError::Bitmap)?;
            bound
                .cow
                .apply_planned_reservation()
                .map_err(LockedReclamationConsumerError::Bitmap)?;
            consume(pass, bound).map_err(LockedReclamationConsumerError::Consumer)
        },
    ) {
        Ok(result) => Ok(result),
        Err(RetirementReclamationExecutionError::Read(error)) => {
            Err(LockedReclamationFinalizerError::Retirement(error))
        }
        Err(RetirementReclamationExecutionError::Consumer(
            LockedReclamationConsumerError::Bitmap(error),
        )) => Err(LockedReclamationFinalizerError::Bitmap(error)),
        Err(RetirementReclamationExecutionError::Consumer(
            LockedReclamationConsumerError::Consumer(error),
        )) => Err(LockedReclamationFinalizerError::Consumer(error)),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bitmap_cow::{FreeBitmapReclamationTicket, FreeBitmapReservationStageBuffers};
    use crate::contract::{AddressFamily, ValueKind, ValueTag, PAGE_SIZE};
    use crate::page_source::PageSourceError;
    use crate::private_page_pool::{PrivatePagePoolSlot, PrivatePagePoolState};
    use crate::retirement_reader::RetirementReclaimBarrier;
    use core::cell::Cell;

    #[derive(Debug)]
    struct TestBarrier;

    impl RetirementReclaimBarrier for TestBarrier {}

    static TEST_BARRIER: TestBarrier = TestBarrier;

    #[derive(Debug)]
    struct RejectingSource {
        calls: Cell<usize>,
    }

    impl CommittedPageSource for RejectingSource {
        fn check_access(&self) -> Result<(), PageSourceError> {
            self.calls.set(self.calls.get() + 1);
            Err(PageSourceError::ForkedHandle)
        }

        fn read_page(&self, _: u32, _: &mut [u8; PAGE_SIZE]) -> Result<(), PageSourceError> {
            self.calls.set(self.calls.get() + 1);
            Err(PageSourceError::ForkedHandle)
        }
    }

    fn selected_meta() -> MetaV4 {
        MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [1; 16],
            txn_id: 1,
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

    #[test]
    fn invalid_limits_fail_before_source_access_or_shadow_mutation() {
        let source = RejectingSource {
            calls: Cell::new(0),
        };
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant(&mut slots, 2, 2, 2).unwrap();
        let scope = pool.reserve_scope(1).unwrap();
        let before = pool.exact_commitment(&scope).unwrap();

        let mut arena = [];
        let mut pool_validation = [];
        let mut arena_bindings = [];
        let mut candidates = [];
        let mut verified_bitmap_pages = [];
        let mut replacements = [];
        let mut index_nodes = [];
        let mut available_slots = [];
        let mut source_nodes = [];
        let reclamation = FreeBitmapReclamationTicket::new();
        let mut stage_arena = [];
        let mut stage_bindings = [];
        let mut stage_candidates = [];
        let mut stage_verified = [];
        let mut stage_replacements = [];
        let mut stage_index = [];
        let mut stage_available = [];
        let mut verified_batches = [];
        let mut verified_pages = [];

        let result = with_locked_reclamation_bitmap_reservation(
            selected_meta(),
            &source,
            RetirementReclaimFence::from_stable_reader_table(&TEST_BARRIER, 0, None),
            LockedReclamationFinalizerLimits {
                max_batches: 0,
                max_pages: 1,
                bitmap_payload_pages: 1,
            },
            LockedReclamationFinalizerScratch {
                bitmap: FreeBitmapReservationBuffers {
                    arena: &mut arena,
                    pool_validation: &mut pool_validation,
                    arena_bindings: &mut arena_bindings,
                    candidates: &mut candidates,
                    verified_pages: &mut verified_bitmap_pages,
                    replacements: &mut replacements,
                    index_nodes: &mut index_nodes,
                    available_slots: &mut available_slots,
                    source_nodes: &mut source_nodes,
                    reclamation: &reclamation,
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
                verified_batches: &mut verified_batches,
                verified_pages: &mut verified_pages,
            },
            &pool,
            &scope,
            |_, _| -> Result<(), ()> { unreachable!("invalid limits skip the consumer") },
        );

        assert!(matches!(
            result,
            Err(LockedReclamationFinalizerError::InvalidLimits)
        ));
        assert_eq!(source.calls.get(), 0);
        assert!(pool.validate_exact_commitment(&scope, &before).is_ok());
        assert_eq!(
            pool.scoped_slot_info(&scope, 0).unwrap().unwrap().state,
            PrivatePagePoolState::Vacant
        );
    }
}
