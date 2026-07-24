//! Fixed pre-lock logical storage for one ordinary range replacement.
//!
//! This is deliberately not a public writer operation. It owns only the
//! arrival-order normalizer and its sealed logical range pages so the later
//! lock-held allocator can consume them without a temporary file or a
//! caller-managed intermediate value.

use crate::contract::{AddressFamily, MetaV4};
use crate::key::IpKey;
use crate::range_builder::RangeTreeBuildWorkspace;
use crate::range_staging::{
    RangeTreeStagedResult, RangeTreeStaging, RangeTreeStagingError, RangeTreeStagingPage,
};
use crate::sequential_assignment::{
    SequentialAssignmentEngine, SequentialAssignmentError, SequentialAssignmentFinalizeError,
    SequentialAssignmentPage, SequentialAssignmentWorkspace,
};
use core::mem;
use std::vec::Vec;

/// Explicit fixed limits for one private pre-lock range-normalization attempt.
///
/// Zero logical-page capacity is valid for an empty replacement. Any input
/// that needs a node or output page then fails before a live transaction is
/// opened.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct LinuxLiveWriterNormalRangeWorkspaceCapacity {
    pub(crate) normalizer_pages: usize,
    pub(crate) staged_range_pages: usize,
    pub(crate) max_assignments: u64,
    pub(crate) max_work: u64,
    pub(crate) max_mutations: usize,
}

/// Construction or preparation failure for the private logical workspace.
///
/// No variant authorizes a physical page allocation, writer barrier, or file
/// mutation.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum LinuxLiveWriterNormalRangeWorkspaceError {
    Allocation,
    CapacityOverflow,
    WorkspaceBusy,
    NoPreparedOutput,
    SelectedGenerationZero,
    TransactionExhausted,
    AddressFamily {
        selected: AddressFamily,
        requested: AddressFamily,
    },
    Input(SequentialAssignmentError),
    Staging(RangeTreeStagingError),
    Finalize(SequentialAssignmentFinalizeError),
}

#[derive(Clone, Copy, Debug)]
struct LinuxLiveWriterPreparedNormalRange {
    selected: MetaV4,
    staged: RangeTreeStagedResult,
}

/// Opaque owner for bounded normalizer and logical-range staging storage.
///
/// Construction allocates the two variable-size partitions exactly once. The
/// embedded range-tree builder has fixed size. After construction every
/// attempt only clears and borrows this storage; it does not allocate, sort,
/// open a temporary file, or choose a physical v4 page number.
#[derive(Debug)]
pub(crate) struct LinuxLiveWriterNormalRangeWorkspace<K: IpKey> {
    capacity: LinuxLiveWriterNormalRangeWorkspaceCapacity,
    retained_bytes: u64,
    normalizer_pages: Vec<SequentialAssignmentPage>,
    staging_pages: Vec<RangeTreeStagingPage>,
    tree_workspace: RangeTreeBuildWorkspace<K>,
    prepared: Option<LinuxLiveWriterPreparedNormalRange>,
}

fn allocate_normal_range_vec<T>(
    len: usize,
    mut make: impl FnMut() -> T,
) -> Result<Vec<T>, LinuxLiveWriterNormalRangeWorkspaceError> {
    let mut values = Vec::new();
    values
        .try_reserve_exact(len)
        .map_err(|_| LinuxLiveWriterNormalRangeWorkspaceError::Allocation)?;
    for _ in 0..len {
        values.push(make());
    }
    Ok(values)
}

fn add_retained_bytes<T>(
    total: &mut u64,
    count: usize,
) -> Result<(), LinuxLiveWriterNormalRangeWorkspaceError> {
    let bytes = mem::size_of::<T>()
        .checked_mul(count)
        .ok_or(LinuxLiveWriterNormalRangeWorkspaceError::CapacityOverflow)?;
    let bytes = u64::try_from(bytes)
        .map_err(|_| LinuxLiveWriterNormalRangeWorkspaceError::CapacityOverflow)?;
    *total = total
        .checked_add(bytes)
        .ok_or(LinuxLiveWriterNormalRangeWorkspaceError::CapacityOverflow)?;
    Ok(())
}

impl<K: IpKey> LinuxLiveWriterNormalRangeWorkspace<K> {
    /// Creates a reusable fixed logical workspace. This is the only point at
    /// which this owner obtains heap storage.
    pub(crate) fn new(
        capacity: LinuxLiveWriterNormalRangeWorkspaceCapacity,
    ) -> Result<Self, LinuxLiveWriterNormalRangeWorkspaceError> {
        let normalizer_pages =
            allocate_normal_range_vec(capacity.normalizer_pages, SequentialAssignmentPage::empty)?;
        let staging_pages =
            allocate_normal_range_vec(capacity.staged_range_pages, RangeTreeStagingPage::empty)?;

        let mut retained_bytes = u64::try_from(mem::size_of::<Self>())
            .map_err(|_| LinuxLiveWriterNormalRangeWorkspaceError::CapacityOverflow)?;
        add_retained_bytes::<SequentialAssignmentPage>(
            &mut retained_bytes,
            normalizer_pages.capacity(),
        )?;
        add_retained_bytes::<RangeTreeStagingPage>(&mut retained_bytes, staging_pages.capacity())?;

        Ok(Self {
            capacity,
            retained_bytes,
            normalizer_pages,
            staging_pages,
            tree_workspace: RangeTreeBuildWorkspace::new(),
            prepared: None,
        })
    }

    /// Total storage retained by this owner, including its fixed builder.
    pub(crate) const fn retained_bytes(&self) -> u64 {
        self.retained_bytes
    }

    fn reset_logical_storage(&mut self) {
        self.normalizer_pages
            .fill(SequentialAssignmentPage::empty());
        self.staging_pages.fill(RangeTreeStagingPage::empty());
    }

    /// Applies one arrival-ordered input stream and retains only its sealed
    /// logical range tree. The closure has no live page allocator or file
    /// access; it can only issue assignment/clear operations to the bounded
    /// sequential engine.
    pub(crate) fn prepare(
        &mut self,
        selected: MetaV4,
        apply: impl FnOnce(&mut SequentialAssignmentEngine<K>) -> Result<(), SequentialAssignmentError>,
    ) -> Result<(), LinuxLiveWriterNormalRangeWorkspaceError> {
        if self.prepared.is_some() {
            return Err(LinuxLiveWriterNormalRangeWorkspaceError::WorkspaceBusy);
        }
        if selected.address_family != K::FAMILY {
            return Err(LinuxLiveWriterNormalRangeWorkspaceError::AddressFamily {
                selected: selected.address_family,
                requested: K::FAMILY,
            });
        }
        if selected.txn_id == 0 {
            return Err(LinuxLiveWriterNormalRangeWorkspaceError::SelectedGenerationZero);
        }
        let born_txn = selected
            .txn_id
            .checked_add(1)
            .ok_or(LinuxLiveWriterNormalRangeWorkspaceError::TransactionExhausted)?;

        self.reset_logical_storage();
        let result = (|| {
            let mut normalizer = SequentialAssignmentWorkspace::new(&mut self.normalizer_pages);
            let mut staging =
                RangeTreeStaging::new(&mut self.staging_pages, born_txn, selected.value_kind)
                    .map_err(LinuxLiveWriterNormalRangeWorkspaceError::Staging)?;
            let mut engine = SequentialAssignmentEngine::new(
                &mut normalizer,
                born_txn,
                selected.value_kind,
                self.capacity.max_assignments,
                self.capacity.max_work,
                self.capacity.max_mutations,
            )
            .map_err(LinuxLiveWriterNormalRangeWorkspaceError::Input)?;
            apply(&mut engine).map_err(LinuxLiveWriterNormalRangeWorkspaceError::Input)?;
            engine
                .build_staged_tree(&mut self.tree_workspace, &mut staging)
                .map_err(LinuxLiveWriterNormalRangeWorkspaceError::Finalize)
        })();

        match result {
            Ok(staged) => {
                self.prepared = Some(LinuxLiveWriterPreparedNormalRange { selected, staged });
                Ok(())
            }
            Err(error) => {
                self.reset_logical_storage();
                Err(error)
            }
        }
    }

    /// Reopens the workspace-owned sealed logical output for the later
    /// lock-held allocator. The returned staging view borrows this workspace,
    /// so it cannot outlive the operation or be retained independently. The
    /// prepared marker is consumed first; a reattachment failure is terminal
    /// for this attempt and must follow the enclosing abort/reset path.
    pub(crate) fn reopen_prepared_staging(
        &mut self,
    ) -> Result<
        (MetaV4, RangeTreeStagedResult, RangeTreeStaging<'_, K>),
        LinuxLiveWriterNormalRangeWorkspaceError,
    > {
        let prepared = self
            .prepared
            .take()
            .ok_or(LinuxLiveWriterNormalRangeWorkspaceError::NoPreparedOutput)?;
        let born_txn = prepared
            .selected
            .txn_id
            .checked_add(1)
            .ok_or(LinuxLiveWriterNormalRangeWorkspaceError::TransactionExhausted)?;
        let staging = RangeTreeStaging::reopen_sealed(
            &mut self.staging_pages,
            born_txn,
            prepared.selected.value_kind,
            prepared.staged,
        )
        .map_err(LinuxLiveWriterNormalRangeWorkspaceError::Staging)?;
        Ok((prepared.selected, prepared.staged, staging))
    }

    /// Erases unpublished logical input/output after the enclosing live draft
    /// has been abandoned. It does not cancel a core or release a writer lock;
    /// those remain the enclosing operation's explicit responsibilities.
    pub(crate) fn discard_after_abort(&mut self) {
        self.reset_logical_storage();
        self.prepared = None;
    }

    /// Erases transient logical storage after the enclosing transaction has
    /// durably completed and released its publication authority.
    pub(crate) fn finish_after_publication(&mut self) {
        self.reset_logical_storage();
        self.prepared = None;
    }

    #[cfg(test)]
    fn is_idle(&self) -> bool {
        self.prepared.is_none()
            && self
                .normalizer_pages
                .iter()
                .all(|page| *page == SequentialAssignmentPage::empty())
            && self
                .staging_pages
                .iter()
                .all(|page| *page == RangeTreeStagingPage::empty())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::contract::{ValueKind, ValueTag};
    use crate::key::Ipv4Key;
    use crate::private_page_pool::{PrivatePageAuthorization, PrivatePageCoordinatorTerminalPage};
    use crate::range_page::RangeLeaf;
    use crate::range_staging::RangeTreePhysicalAssignment;
    use crate::test_alloc::count_thread_allocations;

    fn selected() -> MetaV4 {
        MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [1; 16],
            txn_id: 7,
            commit_nonce: [2; 16],
            page_count: 64,
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
            free_bitmap_root: 2,
            retirement_root: 0,
        }
    }

    fn capacity() -> LinuxLiveWriterNormalRangeWorkspaceCapacity {
        LinuxLiveWriterNormalRangeWorkspaceCapacity {
            normalizer_pages: 2,
            staged_range_pages: 1,
            max_assignments: 8,
            max_work: 10_000,
            max_mutations: 10_000,
        }
    }

    #[test]
    fn prepared_logical_range_survives_reopen_without_post_setup_allocation() {
        let mut workspace =
            LinuxLiveWriterNormalRangeWorkspace::<Ipv4Key>::new(capacity()).unwrap();
        assert!(workspace.retained_bytes() > 0);

        let (_, allocations) = count_thread_allocations(|| {
            workspace
                .prepare(selected(), |engine| {
                    engine.assign(Ipv4Key(10), Ipv4Key(20), 1)?;
                    engine.assign(Ipv4Key(15), Ipv4Key(17), 2)?;
                    engine.assign(Ipv4Key(21), Ipv4Key(30), 1)
                })
                .unwrap();

            {
                let (source, staged, staging) = workspace.reopen_prepared_staging().unwrap();
                assert_eq!(source.txn_id, 7);
                assert_eq!(staged.page_count(), 1);
                let assignments = [RangeTreePhysicalAssignment {
                    pgno: 12,
                    authorization: PrivatePageAuthorization::Appended,
                }];
                let mut output = [PrivatePageCoordinatorTerminalPage::empty(); 1];
                let materialized = staging
                    .materialize(staged, 64, &assignments, &mut output)
                    .unwrap();
                assert_eq!(materialized.root_pgno, 12);
                assert_eq!(materialized.record_count, 3);
                let leaf = RangeLeaf::<Ipv4Key>::open(
                    &output[0].bytes,
                    8,
                    AddressFamily::Ipv4,
                    ValueKind::Direct,
                )
                .unwrap();
                assert_eq!(leaf.len(), 3);
                assert_eq!(leaf.record(0).unwrap().from, Ipv4Key(10));
                assert_eq!(leaf.record(0).unwrap().to, Ipv4Key(14));
                assert_eq!(leaf.record(0).unwrap().value, 1);
                assert_eq!(leaf.record(1).unwrap().from, Ipv4Key(15));
                assert_eq!(leaf.record(1).unwrap().to, Ipv4Key(17));
                assert_eq!(leaf.record(1).unwrap().value, 2);
                assert_eq!(leaf.record(2).unwrap().from, Ipv4Key(18));
                assert_eq!(leaf.record(2).unwrap().to, Ipv4Key(30));
                assert_eq!(leaf.record(2).unwrap().value, 1);
            }
            assert_eq!(
                workspace.reopen_prepared_staging().unwrap_err(),
                LinuxLiveWriterNormalRangeWorkspaceError::NoPreparedOutput
            );
            workspace.discard_after_abort();
        });

        assert_eq!(allocations, 0);
        assert!(workspace.is_idle());
    }

    #[test]
    fn failed_input_scrubs_owned_logical_storage() {
        let mut workspace =
            LinuxLiveWriterNormalRangeWorkspace::<Ipv4Key>::new(capacity()).unwrap();
        let error = workspace
            .prepare(selected(), |engine| {
                engine.assign(Ipv4Key(10), Ipv4Key(20), 1)?;
                engine.assign(Ipv4Key(20), Ipv4Key(10), 1)
            })
            .unwrap_err();
        assert_eq!(
            error,
            LinuxLiveWriterNormalRangeWorkspaceError::Input(
                SequentialAssignmentError::RangeReversed
            )
        );
        assert!(workspace.is_idle());
        assert_eq!(
            workspace.reopen_prepared_staging().unwrap_err(),
            LinuxLiveWriterNormalRangeWorkspaceError::NoPreparedOutput
        );
    }

    #[test]
    fn empty_replacement_needs_no_logical_page() {
        let mut workspace = LinuxLiveWriterNormalRangeWorkspace::<Ipv4Key>::new(
            LinuxLiveWriterNormalRangeWorkspaceCapacity {
                normalizer_pages: 0,
                staged_range_pages: 0,
                max_assignments: 0,
                max_work: 0,
                max_mutations: 0,
            },
        )
        .unwrap();
        workspace.prepare(selected(), |_| Ok(())).unwrap();
        {
            let (_source, staged, staging) = workspace.reopen_prepared_staging().unwrap();
            assert_eq!(staged.page_count(), 0);
            let assignments: [RangeTreePhysicalAssignment; 0] = [];
            let mut output: [PrivatePageCoordinatorTerminalPage; 0] = [];
            let materialized = staging
                .materialize(staged, 64, &assignments, &mut output)
                .unwrap();
            assert_eq!(materialized.root_pgno, 0);
            assert_eq!(materialized.record_count, 0);
        }
        workspace.finish_after_publication();
        assert!(workspace.is_idle());
    }
}
