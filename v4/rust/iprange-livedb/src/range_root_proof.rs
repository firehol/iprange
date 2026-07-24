//! Private evidence for safely replacing one selected range-tree root.
//!
//! A replacement root is not publishable merely because its new pages were
//! materialized. Every selected old range-tree page and every committed page
//! replaced while preparing bitmap/retirement output must first converge into
//! one protected-page set. This module binds that preparation state without
//! touching allocator ownership, target metadata, or file bytes.

use crate::contract::{AddressFamily, MetaV4, ValueKind, PAGE_SIZE};
use crate::key::IpKey;
use crate::page_number_index::{
    converge_page_number_index, validate_committed_page_range, PageNumberIndex,
    PageNumberIndexError, PageNumberIndexFixedPointAdder, PageNumberIndexFixedPointCandidate,
    PageNumberIndexFixedPointError, PageNumberIndexVisitError,
};
use crate::page_source::CommittedPageSource;
use crate::private_page_pool::{
    validate_unbound_terminal_journal_source, PrivatePageAuthorization,
    PrivatePageCoordinatorTerminalPage, PrivatePageOwner, PrivatePageTerminalJournalError,
};
use crate::range_ownership_walk::{
    collect_range_tree_ownership, RangeOwnershipWalkError, RangeTreeOwnershipScratch,
};
use crate::range_staging::RangeTreeMaterializedResult;

const RANGE_ROOT_PROOF_HASH_SEED: u64 = 0xcbf2_9ce4_8422_2325 ^ 0x98f0_4adf_c3e2_719b;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct RangeRootTransactionIdentity {
    txn_id: u64,
    page_count: u64,
    range_root: u32,
    range_record_count: u64,
    address_family: AddressFamily,
    value_kind: ValueKind,
}

/// Failure before range-root replacement can enter terminal composition.
#[derive(Debug)]
pub(crate) enum RangeRootTransactionProofError<E> {
    InvalidArgument,
    SelectedIdentity,
    RangeJournalShape,
    RangeJournal(PrivatePageTerminalJournalError),
    RangeJournalOwner { pgno: u32 },
    RangeRoot { pgno: u32 },
    Ownership(RangeOwnershipWalkError),
    FixedPoint(PageNumberIndexFixedPointError<E>),
    ProtectedOverlap { pgno: u32 },
}

/// A prepared proof became inconsistent after it was created.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RangeRootTransactionProofStateError {
    Stale,
}

/// Private proof tying the selected old range tree to a new materialized root
/// and a converged selected-generation protected-page set.
///
/// The borrowed indexes remain caller-owned scratch. `discard_after_abort`
/// must be called if a later transaction stage does not consume this proof.
pub(crate) struct RangeRootTransactionProof<'proof, 'workspace, 'storage> {
    selected: RangeRootTransactionIdentity,
    materialized: RangeTreeMaterializedResult,
    range_pages: &'proof [PrivatePageCoordinatorTerminalPage],
    seed: &'proof mut PageNumberIndex<'workspace, 'storage>,
    first: &'proof mut PageNumberIndex<'workspace, 'storage>,
    second: &'proof mut PageNumberIndex<'workspace, 'storage>,
    candidate: PageNumberIndexFixedPointCandidate,
    protected_len: u64,
    seal: u64,
}

fn range_root_transaction_identity_from_meta<E>(
    selected: MetaV4,
) -> Result<RangeRootTransactionIdentity, RangeRootTransactionProofError<E>> {
    if selected.txn_id == 0
        || selected.page_count < 2
        || (selected.range_root == 0 && selected.range_record_count != 0)
        || (selected.range_root != 0
            && (selected.range_root < 2 || u64::from(selected.range_root) >= selected.page_count))
    {
        return Err(RangeRootTransactionProofError::SelectedIdentity);
    }
    Ok(RangeRootTransactionIdentity {
        txn_id: selected.txn_id,
        page_count: selected.page_count,
        range_root: selected.range_root,
        range_record_count: selected.range_record_count,
        address_family: selected.address_family,
        value_kind: selected.value_kind,
    })
}

fn validate_range_root_transaction_journal<E>(
    materialized: RangeTreeMaterializedResult,
    range_pages: &[PrivatePageCoordinatorTerminalPage],
) -> Result<(), RangeRootTransactionProofError<E>> {
    let empty_root = materialized.root_pgno == 0;
    if materialized.page_count != range_pages.len()
        || (empty_root && (materialized.page_count != 0 || materialized.record_count != 0))
        || (!empty_root && materialized.root_pgno < 2)
    {
        return Err(RangeRootTransactionProofError::RangeJournalShape);
    }
    validate_unbound_terminal_journal_source(0, range_pages)
        .map_err(RangeRootTransactionProofError::RangeJournal)?;
    let mut found_root = materialized.root_pgno == 0;
    for page in range_pages {
        if page.owner != PrivatePageOwner::Range {
            return Err(RangeRootTransactionProofError::RangeJournalOwner { pgno: page.pgno });
        }
        if page.pgno == materialized.root_pgno {
            found_root = true;
        }
    }
    if !found_root {
        return Err(RangeRootTransactionProofError::RangeRoot {
            pgno: materialized.root_pgno,
        });
    }
    Ok(())
}

fn range_root_transaction_proof_disjoint<E>(
    range_pages: &[PrivatePageCoordinatorTerminalPage],
    protected: &mut PageNumberIndex<'_, '_>,
) -> Result<(), RangeRootTransactionProofError<E>> {
    let mut range_index = 0usize;
    let result = protected.visit_ascending(|protected_pgno| -> Result<(), u32> {
        while range_index < range_pages.len() && range_pages[range_index].pgno < protected_pgno {
            range_index += 1;
        }
        if range_index < range_pages.len() && range_pages[range_index].pgno == protected_pgno {
            return Err(protected_pgno);
        }
        Ok(())
    });
    match result {
        Ok(()) => Ok(()),
        Err(PageNumberIndexVisitError::Index(error)) => {
            Err(RangeRootTransactionProofError::FixedPoint(
                PageNumberIndexFixedPointError::Index(error),
            ))
        }
        Err(PageNumberIndexVisitError::Visitor(pgno)) => {
            Err(RangeRootTransactionProofError::ProtectedOverlap { pgno })
        }
    }
}

const fn range_root_transaction_proof_hash_word(mut hash: u64, value: u64) -> u64 {
    hash ^= value;
    hash.wrapping_mul(0x0000_0100_0000_01b3)
}

const fn range_root_transaction_proof_authorization_hash(
    authorization: PrivatePageAuthorization,
) -> u64 {
    match authorization {
        PrivatePageAuthorization::CommittedFree => 1,
        PrivatePageAuthorization::SafelyReclaimed => 2,
        PrivatePageAuthorization::Appended => 3,
    }
}

const fn range_root_transaction_proof_owner_hash(owner: PrivatePageOwner) -> u64 {
    match owner {
        PrivatePageOwner::Bitmap => 1,
        PrivatePageOwner::Range => 2,
        PrivatePageOwner::Retirement => 3,
    }
}

fn range_root_transaction_proof_terminal_hash(
    mut hash: u64,
    page: &PrivatePageCoordinatorTerminalPage,
) -> u64 {
    hash = range_root_transaction_proof_hash_word(hash, page.pool_slot as u64);
    hash = range_root_transaction_proof_hash_word(hash, u64::from(page.pgno));
    hash = range_root_transaction_proof_hash_word(
        hash,
        range_root_transaction_proof_authorization_hash(page.authorization),
    );
    hash = range_root_transaction_proof_hash_word(
        hash,
        range_root_transaction_proof_owner_hash(page.owner),
    );
    hash = range_root_transaction_proof_hash_word(hash, page.owner_generation);
    hash = range_root_transaction_proof_hash_word(hash, page.tag);
    let mut offset = 0usize;
    while offset < PAGE_SIZE {
        let value = u64::from(page.bytes[offset])
            | (u64::from(page.bytes[offset + 1]) << 8)
            | (u64::from(page.bytes[offset + 2]) << 16)
            | (u64::from(page.bytes[offset + 3]) << 24)
            | (u64::from(page.bytes[offset + 4]) << 32)
            | (u64::from(page.bytes[offset + 5]) << 40)
            | (u64::from(page.bytes[offset + 6]) << 48)
            | (u64::from(page.bytes[offset + 7]) << 56);
        hash = range_root_transaction_proof_hash_word(hash, value);
        offset += 8;
    }
    hash
}

fn range_root_transaction_proof_hash_index(
    mut hash: u64,
    index: &mut PageNumberIndex<'_, '_>,
) -> Result<u64, PageNumberIndexError> {
    match index.visit_ascending(|pgno| -> Result<(), core::convert::Infallible> {
        hash = range_root_transaction_proof_hash_word(hash, u64::from(pgno));
        Ok(())
    }) {
        Ok(()) => Ok(hash),
        Err(PageNumberIndexVisitError::Index(error)) => Err(error),
        Err(PageNumberIndexVisitError::Visitor(never)) => match never {},
    }
}

fn seal_range_root_transaction_proof(
    selected: RangeRootTransactionIdentity,
    materialized: RangeTreeMaterializedResult,
    range_pages: &[PrivatePageCoordinatorTerminalPage],
    seed: &mut PageNumberIndex<'_, '_>,
    protected: &mut PageNumberIndex<'_, '_>,
    candidate: PageNumberIndexFixedPointCandidate,
) -> Result<u64, PageNumberIndexError> {
    let mut hash = RANGE_ROOT_PROOF_HASH_SEED;
    for value in [
        selected.txn_id,
        selected.page_count,
        u64::from(selected.range_root),
        selected.range_record_count,
        selected.address_family as u64,
        selected.value_kind as u64,
        u64::from(materialized.root_pgno),
        u64::from(materialized.root_level),
        materialized.record_count,
        materialized.page_count as u64,
        match candidate {
            PageNumberIndexFixedPointCandidate::First => 1,
            PageNumberIndexFixedPointCandidate::Second => 2,
        },
        range_pages.len() as u64,
    ] {
        hash = range_root_transaction_proof_hash_word(hash, value);
    }
    for page in range_pages {
        hash = range_root_transaction_proof_terminal_hash(hash, page);
    }
    let hash = range_root_transaction_proof_hash_index(hash, seed)?;
    range_root_transaction_proof_hash_index(hash, protected)
}

fn discard_range_root_transaction_proof_indexes(
    seed: &mut PageNumberIndex<'_, '_>,
    first: &mut PageNumberIndex<'_, '_>,
    second: &mut PageNumberIndex<'_, '_>,
) {
    seed.discard_after_abort();
    first.discard_after_abort();
    second.discard_after_abort();
}

impl RangeRootTransactionProof<'_, '_, '_> {
    /// Rechecks that the proof's private scratch was not changed after it was
    /// produced. It does not publish or bind anything.
    pub(crate) fn verify(&mut self) -> Result<(), RangeRootTransactionProofStateError> {
        let selected = self.selected;
        let materialized = self.materialized;
        let range_pages = self.range_pages;
        let candidate = self.candidate;
        let expected_len = self.protected_len;
        let expected_seal = self.seal;
        let seed = &mut *self.seed;
        let first = &mut *self.first;
        let second = &mut *self.second;
        let (protected, other) = match candidate {
            PageNumberIndexFixedPointCandidate::First => (first, second),
            PageNumberIndexFixedPointCandidate::Second => (second, first),
        };
        if !other.is_empty_and_clean()
            || protected.len() != expected_len
            || validate_committed_page_range(seed, selected.page_count).is_err()
            || validate_committed_page_range(protected, selected.page_count).is_err()
            || validate_range_root_transaction_journal::<core::convert::Infallible>(
                materialized,
                range_pages,
            )
            .is_err()
            || range_root_transaction_proof_disjoint::<core::convert::Infallible>(
                range_pages,
                protected,
            )
            .is_err()
        {
            return Err(RangeRootTransactionProofStateError::Stale);
        }
        let seal = seal_range_root_transaction_proof(
            selected,
            materialized,
            range_pages,
            seed,
            protected,
            candidate,
        )
        .map_err(|_| RangeRootTransactionProofStateError::Stale)?;
        if seal != expected_seal {
            return Err(RangeRootTransactionProofStateError::Stale);
        }
        Ok(())
    }

    /// Scrubs all retained caller-owned index workspaces after a whole-draft
    /// abort. The proof has not acquired publication authority.
    pub(crate) fn discard_after_abort(self) {
        discard_range_root_transaction_proof_indexes(self.seed, self.first, self.second);
    }
}

/// Collects the selected old range tree and converges all prospective selected
/// committed replacements into private proof scratch. It deliberately does
/// not stage retirement output, bind a terminal journal, change target
/// metadata, or publish a range root.
// The constructor takes the exact independent transaction authorities. A
// wrapper would only hide their ownership/lifetime boundaries from the caller.
#[allow(clippy::result_large_err, clippy::too_many_arguments)]
pub(crate) fn prepare_range_root_transaction_proof<'proof, 'workspace, 'storage, K, S, E>(
    source: &S,
    selected: MetaV4,
    materialized: RangeTreeMaterializedResult,
    range_pages: &'proof [PrivatePageCoordinatorTerminalPage],
    seed: &'proof mut PageNumberIndex<'workspace, 'storage>,
    first: &'proof mut PageNumberIndex<'workspace, 'storage>,
    second: &'proof mut PageNumberIndex<'workspace, 'storage>,
    ownership_scratch: &mut RangeTreeOwnershipScratch,
    max_range_work: u64,
    max_iterations: usize,
    preview: impl FnMut(
        &mut PageNumberIndex<'_, '_>,
        &mut PageNumberIndexFixedPointAdder<'_, '_, '_>,
    ) -> Result<(), E>,
) -> Result<
    RangeRootTransactionProof<'proof, 'workspace, 'storage>,
    RangeRootTransactionProofError<E>,
>
where
    K: IpKey,
    S: CommittedPageSource + ?Sized,
{
    let identity = range_root_transaction_identity_from_meta(selected)?;
    if !seed.is_empty_and_clean()
        || !first.is_empty_and_clean()
        || !second.is_empty_and_clean()
        || max_iterations == 0
    {
        return Err(RangeRootTransactionProofError::InvalidArgument);
    }
    validate_range_root_transaction_journal(materialized, range_pages)?;

    if let Err(error) = collect_range_tree_ownership::<K, _>(
        source,
        selected,
        seed,
        ownership_scratch,
        max_range_work,
    ) {
        discard_range_root_transaction_proof_indexes(seed, first, second);
        return Err(RangeRootTransactionProofError::Ownership(error));
    }
    let candidate = match converge_page_number_index(
        seed,
        first,
        second,
        identity.page_count,
        max_iterations,
        preview,
    ) {
        Ok(candidate) => candidate,
        Err(error) => {
            discard_range_root_transaction_proof_indexes(seed, first, second);
            return Err(RangeRootTransactionProofError::FixedPoint(error));
        }
    };
    let other_clean = match candidate {
        PageNumberIndexFixedPointCandidate::First => second.is_empty_and_clean(),
        PageNumberIndexFixedPointCandidate::Second => first.is_empty_and_clean(),
    };
    if !other_clean {
        discard_range_root_transaction_proof_indexes(seed, first, second);
        return Err(RangeRootTransactionProofError::FixedPoint(
            PageNumberIndexFixedPointError::Index(PageNumberIndexError::WorkspaceBusy),
        ));
    }
    let protected = match candidate {
        PageNumberIndexFixedPointCandidate::First => &mut *first,
        PageNumberIndexFixedPointCandidate::Second => &mut *second,
    };
    if let Err(error) = validate_committed_page_range(protected, identity.page_count) {
        discard_range_root_transaction_proof_indexes(seed, first, second);
        return Err(RangeRootTransactionProofError::FixedPoint(
            PageNumberIndexFixedPointError::Index(error),
        ));
    }
    if let Err(error) = range_root_transaction_proof_disjoint(range_pages, protected) {
        discard_range_root_transaction_proof_indexes(seed, first, second);
        return Err(error);
    }
    let seal = match seal_range_root_transaction_proof(
        identity,
        materialized,
        range_pages,
        seed,
        protected,
        candidate,
    ) {
        Ok(seal) => seal,
        Err(error) => {
            discard_range_root_transaction_proof_indexes(seed, first, second);
            return Err(RangeRootTransactionProofError::FixedPoint(
                PageNumberIndexFixedPointError::Index(error),
            ));
        }
    };
    let protected_len = protected.len();
    Ok(RangeRootTransactionProof {
        selected: identity,
        materialized,
        range_pages,
        seed,
        first,
        second,
        candidate,
        protected_len,
        seal,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::key::Ipv4Key;
    use crate::page_number_index::{PageNumberIndexPage, PageNumberIndexWorkspace};
    use crate::page_source::SlicePageSource;
    use crate::range_page::{encode_branch, encode_leaf, RangeBranchEntry, RangeRecord};
    use crate::test_alloc::count_thread_allocations;
    use std::vec;
    use std::vec::Vec;

    fn page_mut(bytes: &mut [u8], pgno: u32) -> &mut [u8; PAGE_SIZE] {
        let start = pgno as usize * PAGE_SIZE;
        (&mut bytes[start..start + PAGE_SIZE]).try_into().unwrap()
    }

    fn selected_meta(page_count: u64, root: u32, records: u64) -> MetaV4 {
        let mut selected = empty_direct_meta(3);
        selected.page_count = page_count;
        selected.range_root = root;
        selected.range_record_count = records;
        selected
    }

    fn ownership_image() -> (Vec<u8>, MetaV4) {
        const PAGE_COUNT: u64 = 12;
        let mut bytes = vec![0; PAGE_COUNT as usize * PAGE_SIZE];
        encode_branch::<Ipv4Key>(
            page_mut(&mut bytes, 8),
            3,
            1,
            PAGE_COUNT,
            Ipv4Key::MIN,
            None,
            &[
                RangeBranchEntry {
                    lower_fence: Ipv4Key(0),
                    child_pgno: 11,
                    subtree_record_count: 1,
                    first_from: Ipv4Key(10),
                    last_from: Ipv4Key(10),
                    last_to: Ipv4Key(20),
                },
                RangeBranchEntry {
                    lower_fence: Ipv4Key(100),
                    child_pgno: 3,
                    subtree_record_count: 0,
                    first_from: Ipv4Key::MIN,
                    last_from: Ipv4Key::MIN,
                    last_to: Ipv4Key::MIN,
                },
                RangeBranchEntry {
                    lower_fence: Ipv4Key(200),
                    child_pgno: 4,
                    subtree_record_count: 1,
                    first_from: Ipv4Key(210),
                    last_from: Ipv4Key(210),
                    last_to: Ipv4Key(220),
                },
            ],
        )
        .unwrap();
        encode_leaf::<Ipv4Key>(
            page_mut(&mut bytes, 11),
            3,
            ValueKind::Direct,
            &[RangeRecord {
                from: Ipv4Key(10),
                to: Ipv4Key(20),
                value: 1,
            }],
        )
        .unwrap();
        encode_leaf::<Ipv4Key>(page_mut(&mut bytes, 3), 3, ValueKind::Direct, &[]).unwrap();
        encode_leaf::<Ipv4Key>(
            page_mut(&mut bytes, 4),
            3,
            ValueKind::Direct,
            &[RangeRecord {
                from: Ipv4Key(210),
                to: Ipv4Key(220),
                value: 2,
            }],
        )
        .unwrap();
        (bytes, selected_meta(PAGE_COUNT, 8, 2))
    }

    fn materialized_range_page(
        pgno: u32,
    ) -> (
        RangeTreeMaterializedResult,
        [PrivatePageCoordinatorTerminalPage; 1],
    ) {
        let mut page = PrivatePageCoordinatorTerminalPage::empty();
        page.pgno = pgno;
        page.owner = PrivatePageOwner::Range;
        page.owner_generation = 4;
        page.tag = AddressFamily::Ipv4 as u64;
        (
            RangeTreeMaterializedResult {
                root_pgno: pgno,
                root_level: 0,
                record_count: 1,
                page_count: 1,
            },
            [page],
        )
    }

    fn collect(index: &mut PageNumberIndex<'_, '_>) -> Vec<u32> {
        let mut values = Vec::new();
        index
            .visit_ascending(|value| {
                values.push(value);
                Ok::<(), ()>(())
            })
            .unwrap();
        values
    }

    #[test]
    fn converges_selected_old_range_ownership() {
        let (bytes, selected) = ownership_image();
        let source = SlicePageSource::new(&bytes, selected.page_count);
        let (materialized, range_pages) = materialized_range_page(5);
        let mut seed_pages = [PageNumberIndexPage::empty(); 4];
        let mut first_pages = [PageNumberIndexPage::empty(); 4];
        let mut second_pages = [PageNumberIndexPage::empty(); 4];
        let mut seed_workspace = PageNumberIndexWorkspace::new(&mut seed_pages);
        let mut first_workspace = PageNumberIndexWorkspace::new(&mut first_pages);
        let mut second_workspace = PageNumberIndexWorkspace::new(&mut second_pages);
        let mut seed = PageNumberIndex::new(&mut seed_workspace).unwrap();
        let mut first = PageNumberIndex::new(&mut first_workspace).unwrap();
        let mut second = PageNumberIndex::new(&mut second_workspace).unwrap();
        let mut ownership_scratch = RangeTreeOwnershipScratch::new();
        let mut calls = 0;
        let mut proof = prepare_range_root_transaction_proof::<Ipv4Key, _, ()>(
            &source,
            selected,
            materialized,
            &range_pages,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            4,
            4,
            |current, additions| {
                calls += 1;
                match current.len() {
                    4 => additions.add(6).map(|_| ()).map_err(|_| ()),
                    5 => additions.add(7).map(|_| ()).map_err(|_| ()),
                    _ => Ok(()),
                }
            },
        )
        .unwrap();
        assert_eq!(calls, 3);
        assert_eq!(collect(&mut *proof.seed), vec![3, 4, 8, 11]);
        proof.verify().unwrap();
        let protected = match proof.candidate {
            PageNumberIndexFixedPointCandidate::First => &mut *proof.first,
            PageNumberIndexFixedPointCandidate::Second => &mut *proof.second,
        };
        assert_eq!(collect(protected), vec![3, 4, 6, 7, 8, 11]);
        proof.discard_after_abort();
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());
    }

    #[test]
    fn accepts_legal_empty_selected_root() {
        let selected = selected_meta(12, 0, 0);
        let source = SlicePageSource::new(&[], selected.page_count);
        let (materialized, range_pages) = materialized_range_page(5);
        let mut seed_pages = [PageNumberIndexPage::empty(); 1];
        let mut first_pages = [PageNumberIndexPage::empty(); 1];
        let mut second_pages = [PageNumberIndexPage::empty(); 1];
        let mut seed_workspace = PageNumberIndexWorkspace::new(&mut seed_pages);
        let mut first_workspace = PageNumberIndexWorkspace::new(&mut first_pages);
        let mut second_workspace = PageNumberIndexWorkspace::new(&mut second_pages);
        let mut seed = PageNumberIndex::new(&mut seed_workspace).unwrap();
        let mut first = PageNumberIndex::new(&mut first_workspace).unwrap();
        let mut second = PageNumberIndex::new(&mut second_workspace).unwrap();
        let mut ownership_scratch = RangeTreeOwnershipScratch::new();
        let mut proof = prepare_range_root_transaction_proof::<Ipv4Key, _, ()>(
            &source,
            selected,
            materialized,
            &range_pages,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            1,
            1,
            |_, _| Ok(()),
        )
        .unwrap();
        proof.verify().unwrap();
        assert_eq!(proof.seed.len(), 0);
        let protected = match proof.candidate {
            PageNumberIndexFixedPointCandidate::First => &mut *proof.first,
            PageNumberIndexFixedPointCandidate::Second => &mut *proof.second,
        };
        assert_eq!(protected.len(), 0);
        proof.discard_after_abort();
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());
    }

    #[test]
    fn rejects_invalid_journal_and_protected_overlap() {
        let selected = selected_meta(12, 0, 0);
        let source = SlicePageSource::new(&[], selected.page_count);
        let (mut materialized, range_pages) = materialized_range_page(5);
        materialized.root_pgno = 6;
        let mut seed_pages = [PageNumberIndexPage::empty(); 1];
        let mut first_pages = [PageNumberIndexPage::empty(); 1];
        let mut second_pages = [PageNumberIndexPage::empty(); 1];
        let mut seed_workspace = PageNumberIndexWorkspace::new(&mut seed_pages);
        let mut first_workspace = PageNumberIndexWorkspace::new(&mut first_pages);
        let mut second_workspace = PageNumberIndexWorkspace::new(&mut second_pages);
        let mut seed = PageNumberIndex::new(&mut seed_workspace).unwrap();
        let mut first = PageNumberIndex::new(&mut first_workspace).unwrap();
        let mut second = PageNumberIndex::new(&mut second_workspace).unwrap();
        let mut ownership_scratch = RangeTreeOwnershipScratch::new();
        let error = match prepare_range_root_transaction_proof::<Ipv4Key, _, ()>(
            &source,
            selected,
            materialized,
            &range_pages,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            1,
            1,
            |_, _| Ok(()),
        ) {
            Ok(_) => panic!("accepted a range journal whose root was not listed"),
            Err(error) => error,
        };
        assert!(matches!(
            error,
            RangeRootTransactionProofError::RangeRoot { pgno: 6 }
        ));
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());

        let (mut materialized, range_pages) = materialized_range_page(5);
        materialized.page_count = 0;
        let error = match prepare_range_root_transaction_proof::<Ipv4Key, _, ()>(
            &source,
            selected,
            materialized,
            &range_pages,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            1,
            1,
            |_, _| Ok(()),
        ) {
            Ok(_) => panic!("accepted a range journal with a mismatched page count"),
            Err(error) => error,
        };
        assert!(matches!(
            error,
            RangeRootTransactionProofError::RangeJournalShape
        ));
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());

        let (materialized, mut range_pages) = materialized_range_page(5);
        range_pages[0].owner = PrivatePageOwner::Bitmap;
        let error = match prepare_range_root_transaction_proof::<Ipv4Key, _, ()>(
            &source,
            selected,
            materialized,
            &range_pages,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            1,
            1,
            |_, _| Ok(()),
        ) {
            Ok(_) => panic!("accepted a non-range replacement terminal page"),
            Err(error) => error,
        };
        assert!(matches!(
            error,
            RangeRootTransactionProofError::RangeJournalOwner { pgno: 5 }
        ));
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());

        let (bytes, selected) = ownership_image();
        let source = SlicePageSource::new(&bytes, selected.page_count);
        let (materialized, range_pages) = materialized_range_page(3);
        let mut seed_pages = [PageNumberIndexPage::empty(); 4];
        let mut first_pages = [PageNumberIndexPage::empty(); 4];
        let mut second_pages = [PageNumberIndexPage::empty(); 4];
        let mut seed_workspace = PageNumberIndexWorkspace::new(&mut seed_pages);
        let mut first_workspace = PageNumberIndexWorkspace::new(&mut first_pages);
        let mut second_workspace = PageNumberIndexWorkspace::new(&mut second_pages);
        let mut seed = PageNumberIndex::new(&mut seed_workspace).unwrap();
        let mut first = PageNumberIndex::new(&mut first_workspace).unwrap();
        let mut second = PageNumberIndex::new(&mut second_workspace).unwrap();
        let mut ownership_scratch = RangeTreeOwnershipScratch::new();
        let error = match prepare_range_root_transaction_proof::<Ipv4Key, _, ()>(
            &source,
            selected,
            materialized,
            &range_pages,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            4,
            1,
            |_, _| Ok(()),
        ) {
            Ok(_) => panic!("accepted replacement range page in protected old ownership"),
            Err(error) => error,
        };
        assert!(matches!(
            error,
            RangeRootTransactionProofError::ProtectedOverlap { pgno: 3 }
        ));
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());
    }

    #[test]
    fn failure_cleans_scratch_and_state_seal_rejects_mutation() {
        let (bytes, selected) = ownership_image();
        let source = SlicePageSource::new(&bytes, selected.page_count);
        let (materialized, range_pages) = materialized_range_page(5);
        let mut seed_pages = [PageNumberIndexPage::empty(); 4];
        let mut first_pages = [PageNumberIndexPage::empty(); 4];
        let mut second_pages = [PageNumberIndexPage::empty(); 4];
        let mut seed_workspace = PageNumberIndexWorkspace::new(&mut seed_pages);
        let mut first_workspace = PageNumberIndexWorkspace::new(&mut first_pages);
        let mut second_workspace = PageNumberIndexWorkspace::new(&mut second_pages);
        let mut seed = PageNumberIndex::new(&mut seed_workspace).unwrap();
        let mut first = PageNumberIndex::new(&mut first_workspace).unwrap();
        let mut second = PageNumberIndex::new(&mut second_workspace).unwrap();
        let mut ownership_scratch = RangeTreeOwnershipScratch::new();
        let truncated = SlicePageSource::new(&bytes[..9 * PAGE_SIZE], selected.page_count);
        let error = match prepare_range_root_transaction_proof::<Ipv4Key, _, ()>(
            &truncated,
            selected,
            materialized,
            &range_pages,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            4,
            1,
            |_, _| Ok(()),
        ) {
            Ok(_) => panic!("accepted truncated selected range ownership"),
            Err(error) => error,
        };
        assert!(matches!(
            error,
            RangeRootTransactionProofError::Ownership(RangeOwnershipWalkError::Source(_))
        ));
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());

        let error = match prepare_range_root_transaction_proof::<Ipv4Key, _, &str>(
            &source,
            selected,
            materialized,
            &range_pages,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            4,
            1,
            |_, _| Err("stopped"),
        ) {
            Ok(_) => panic!("accepted failed protected-page preview"),
            Err(error) => error,
        };
        assert!(matches!(
            error,
            RangeRootTransactionProofError::FixedPoint(PageNumberIndexFixedPointError::Preview(
                "stopped"
            ))
        ));
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());

        let mut proof = prepare_range_root_transaction_proof::<Ipv4Key, _, ()>(
            &source,
            selected,
            materialized,
            &range_pages,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            4,
            1,
            |_, _| Ok(()),
        )
        .unwrap();
        proof.verify().unwrap();
        let protected = match proof.candidate {
            PageNumberIndexFixedPointCandidate::First => &mut *proof.first,
            PageNumberIndexFixedPointCandidate::Second => &mut *proof.second,
        };
        assert_eq!(protected.insert(6), Ok(true));
        assert_eq!(
            proof.verify(),
            Err(RangeRootTransactionProofStateError::Stale)
        );
        proof.discard_after_abort();
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());
    }

    #[test]
    fn uses_no_heap_after_setup() {
        let (bytes, selected) = ownership_image();
        let source = SlicePageSource::new(&bytes, selected.page_count);
        let (materialized, range_pages) = materialized_range_page(5);
        let mut seed_pages = [PageNumberIndexPage::empty(); 4];
        let mut first_pages = [PageNumberIndexPage::empty(); 4];
        let mut second_pages = [PageNumberIndexPage::empty(); 4];
        let mut seed_workspace = PageNumberIndexWorkspace::new(&mut seed_pages);
        let mut first_workspace = PageNumberIndexWorkspace::new(&mut first_pages);
        let mut second_workspace = PageNumberIndexWorkspace::new(&mut second_pages);
        let mut seed = PageNumberIndex::new(&mut seed_workspace).unwrap();
        let mut first = PageNumberIndex::new(&mut first_workspace).unwrap();
        let mut second = PageNumberIndex::new(&mut second_workspace).unwrap();
        let mut ownership_scratch = RangeTreeOwnershipScratch::new();
        let mut run = || {
            let mut proof = prepare_range_root_transaction_proof::<Ipv4Key, _, ()>(
                &source,
                selected,
                materialized,
                &range_pages,
                &mut seed,
                &mut first,
                &mut second,
                &mut ownership_scratch,
                4,
                1,
                |_, _| Ok(()),
            )
            .unwrap();
            proof.verify().unwrap();
            proof.discard_after_abort();
        };
        run();
        let ((), allocations) = count_thread_allocations(&mut run);
        assert_eq!(allocations, 0);
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());
    }
}
