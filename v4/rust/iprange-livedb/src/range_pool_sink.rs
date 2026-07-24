//! Direct range-page placement into one transaction-private page pool.
//!
//! The ordered range builder owns only fixed packing workspace. This adapter
//! gives it one checked, checkpoint-bound destination without inventing a
//! second page store or a second cleanup path.

use crate::contract::{AddressFamily, PAGE_SIZE};
use crate::private_page_pool::{
    PrivatePageOwner, PrivatePagePool, PrivatePagePoolCheckpoint, PrivatePagePoolError,
};
use crate::range_builder::RangeTreePageSink;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RangeTreePoolSinkError {
    PendingTransactionMismatch { requested: u64, pool_pending: u64 },
    Pool(PrivatePagePoolError),
}

/// A range-page sink bound to one active transaction checkpoint.
///
/// The surrounding draft owns rollback. A failed write deliberately leaves
/// every earlier claim in that checkpoint so the caller can discard the whole
/// draft through the pool's existing rollback path.
#[derive(Debug)]
pub(crate) struct RangeTreePoolSink<'pool, 'slots> {
    pool: &'pool PrivatePagePool<'slots>,
    checkpoint: &'pool PrivatePagePoolCheckpoint<'slots>,
    born_txn: u64,
    family: AddressFamily,
}

impl<'pool, 'slots> RangeTreePoolSink<'pool, 'slots> {
    /// Preflights enough checkpoint epoch headroom for every fixed pool slot.
    ///
    /// The ordered builder does not know its final page count while it streams
    /// input. The pool capacity is already fixed before the transaction, so it
    /// is the bounded worst case: two forward mutations and one rollback
    /// mutation per slot, plus the checkpoint boundaries.
    pub(crate) fn preflight_checkpoint(
        pool: &PrivatePagePool<'slots>,
    ) -> Result<PrivatePagePoolCheckpoint<'slots>, PrivatePagePoolError> {
        let epoch_steps = pool
            .len()
            .checked_mul(3)
            .and_then(|steps| steps.checked_add(2))
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        pool.preflight_checkpoint_steps(epoch_steps)
    }

    pub(crate) fn new(
        pool: &'pool PrivatePagePool<'slots>,
        checkpoint: &'pool PrivatePagePoolCheckpoint<'slots>,
        born_txn: u64,
        family: AddressFamily,
    ) -> Result<Self, RangeTreePoolSinkError> {
        let pool_pending = pool.pending_txn();
        if born_txn != pool_pending {
            return Err(RangeTreePoolSinkError::PendingTransactionMismatch {
                requested: born_txn,
                pool_pending,
            });
        }
        pool.validate_checkpoint_handle(checkpoint)
            .map_err(RangeTreePoolSinkError::Pool)?;
        Ok(Self {
            pool,
            checkpoint,
            born_txn,
            family,
        })
    }
}

impl RangeTreePageSink for RangeTreePoolSink<'_, '_> {
    type Error = RangeTreePoolSinkError;

    fn write_range_page(&mut self, page: &[u8; PAGE_SIZE]) -> Result<u32, Self::Error> {
        // Check before claiming so a stale or foreign checkpoint cannot leave
        // an unowned claim behind. This is a constant-time capability check,
        // not file or tree validation.
        self.pool
            .validate_checkpoint_handle(self.checkpoint)
            .map_err(RangeTreePoolSinkError::Pool)?;
        let authority = self
            .pool
            .claim_lowest(PrivatePageOwner::Range, self.born_txn, self.family as u64)
            .map_err(RangeTreePoolSinkError::Pool)?;
        let pgno = authority.page_number();
        self.pool
            .write_page_for_checkpoint_prepared(self.checkpoint, &authority, page)
            .map_err(RangeTreePoolSinkError::Pool)?;
        Ok(pgno)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::contract::ValueKind;
    use crate::key::Ipv4Key;
    use crate::page::{self, PageHeader, PageType};
    use crate::private_page_pool::{
        PrivatePageAuthorization, PrivatePagePoolSlot, PrivatePagePoolState,
    };
    use crate::range_builder::{RangeTreeBuildError, RangeTreeBuildWorkspace};
    use crate::range_page::{leaf_capacity, RangeRecord};
    use crate::test_alloc::count_thread_allocations;

    fn v4_record(value: u32) -> RangeRecord<Ipv4Key> {
        let address = value * 2;
        RangeRecord {
            from: Ipv4Key(address),
            to: Ipv4Key(address),
            value: 1,
        }
    }

    #[test]
    fn pool_sink_places_a_complete_range_tree_in_real_private_pages() {
        let mut slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let checkpoint = RangeTreePoolSink::preflight_checkpoint(&pool).unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let result = {
            let mut sink =
                RangeTreePoolSink::new(&pool, &checkpoint, 2, AddressFamily::Ipv4).unwrap();
            let mut builder = workspace.begin(2, ValueKind::Direct, 20).unwrap();
            for value in 0..=(leaf_capacity::<Ipv4Key>() as u32) {
                builder.push(&mut sink, v4_record(value)).unwrap();
            }
            builder.finish(&mut sink).unwrap()
        };

        assert_eq!(result.root_pgno, 7);
        assert_eq!(result.root_level, 1);
        assert_eq!(pool.available().unwrap(), 0);
        for (slot, expected_type) in [
            (0, PageType::RangeLeaf),
            (1, PageType::RangeLeaf),
            (2, PageType::RangeBranch),
        ] {
            assert_eq!(
                pool.state(slot).unwrap(),
                PrivatePagePoolState::InUse {
                    owner: PrivatePageOwner::Range,
                    owner_generation: 2,
                    tag: 4,
                }
            );
            let bytes = pool.test_bytes(slot).unwrap();
            let header = PageHeader::decode(&bytes, 2).unwrap();
            assert_eq!(header.page_type, expected_type);
            assert_eq!(header.aux, 4);
            assert!(page::verify_crc32c(&bytes));
        }

        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(pool.available().unwrap(), 3);
        for slot in 0..3 {
            assert_eq!(pool.state(slot).unwrap(), PrivatePagePoolState::Available);
            assert_eq!(pool.test_bytes(slot).unwrap(), [0; PAGE_SIZE]);
        }
    }

    #[test]
    fn pool_sink_rejects_the_wrong_transaction_before_claiming() {
        let mut slots = [PrivatePagePoolSlot::authorized(
            3,
            PrivatePageAuthorization::CommittedFree,
        )];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let checkpoint = RangeTreePoolSink::preflight_checkpoint(&pool).unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        assert_eq!(
            RangeTreePoolSink::new(&pool, &checkpoint, 3, AddressFamily::Ipv4).unwrap_err(),
            RangeTreePoolSinkError::PendingTransactionMismatch {
                requested: 3,
                pool_pending: 2,
            }
        );
        assert_eq!(pool.available().unwrap(), 1);
        pool.rollback_checkpoint(checkpoint).unwrap();
    }

    #[test]
    fn failed_partial_build_stays_owned_by_the_checkpoint_for_rollback() {
        let mut slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let checkpoint = RangeTreePoolSink::preflight_checkpoint(&pool).unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let error = {
            let mut sink =
                RangeTreePoolSink::new(&pool, &checkpoint, 2, AddressFamily::Ipv4).unwrap();
            let mut builder = workspace.begin(2, ValueKind::Direct, 20).unwrap();
            for value in 0..=(leaf_capacity::<Ipv4Key>() as u32) {
                builder.push(&mut sink, v4_record(value)).unwrap();
            }
            builder.finish(&mut sink).unwrap_err()
        };
        assert_eq!(
            error,
            RangeTreeBuildError::Sink(RangeTreePoolSinkError::Pool(
                PrivatePagePoolError::PageUnavailable(0)
            ))
        );
        assert_eq!(pool.available().unwrap(), 0);

        pool.rollback_checkpoint(checkpoint).unwrap();
        assert_eq!(pool.available().unwrap(), 2);
        for slot in 0..2 {
            assert_eq!(pool.state(slot).unwrap(), PrivatePagePoolState::Available);
            assert_eq!(pool.test_bytes(slot).unwrap(), [0; PAGE_SIZE]);
        }
    }

    #[test]
    fn pool_sink_and_checkpoint_build_allocate_nothing_after_fixed_setup() {
        let mut slots = [PrivatePagePoolSlot::authorized(
            3,
            PrivatePageAuthorization::CommittedFree,
        )];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let mut workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();

        let (result, allocations) = count_thread_allocations(|| {
            let checkpoint = RangeTreePoolSink::preflight_checkpoint(&pool).unwrap();
            pool.begin_checkpoint_prepared(&checkpoint).unwrap();
            let result = {
                let mut sink =
                    RangeTreePoolSink::new(&pool, &checkpoint, 2, AddressFamily::Ipv4).unwrap();
                let mut builder = workspace.begin(2, ValueKind::Direct, 20).unwrap();
                builder.push(&mut sink, v4_record(1)).unwrap();
                builder.finish(&mut sink).unwrap()
            };
            pool.rollback_checkpoint(checkpoint).unwrap();
            result
        });
        assert_eq!(allocations, 0);
        assert_eq!(result.root_pgno, 3);
        assert_eq!(pool.available().unwrap(), 1);
    }
}
