//! Private evidence for safely replacing one selected range-tree root.
//!
//! A replacement root is not publishable merely because its new pages were
//! materialized. Every selected old range-tree page and every committed page
//! replaced while preparing bitmap/retirement output must first converge into
//! one protected-page set. This module binds that preparation state without
//! touching allocator ownership, target metadata, or file bytes.

use crate::bitmap_cow::{
    BoundFreeBitmapReservation, FreeBitmapCowError, FreeBitmapFinalizationCachedPage,
    FreeBitmapFinalizationPreviewError, FreeBitmapFinalizationScratch, FreeBitmapInsertPage,
};
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
    PrivatePageCoordinatorTerminalPage, PrivatePageOwner, PrivatePagePool,
    PrivatePagePoolCommitment, PrivatePagePoolError, PrivatePageReservationScope,
    PrivatePageReservationScopeSeed, PrivatePageSelectiveOverlayNode,
    PrivatePageSelectivePathEntry, PrivatePageTerminalJournalError,
};
use crate::range_ownership_walk::{
    collect_range_tree_ownership, RangeOwnershipWalkError, RangeTreeOwnershipScratch,
};
use crate::range_staging::RangeTreeMaterializedResult;
use crate::retirement_writer::RetirementTreeState;
use crate::retirement_writer::{
    BlobBuildScratch, CommittedPageReplacement, CommittedReplacementLedger, PageRoleIndex,
    PageRoleIndexSlot, PrivatePageArena, PrivateReleaseBuffer, RetirementAppendReplacementProbe,
    RetirementBlobBuilder, RetirementPathFrame, RetirementTreeEditResult, RetirementTreeEditor,
    RetirementWriteError,
};

const RANGE_ROOT_PROOF_HASH_SEED: u64 = 0xcbf2_9ce4_8422_2325 ^ 0x98f0_4adf_c3e2_719b;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct RangeRootTransactionIdentity {
    txn_id: u64,
    page_count: u64,
    range_root: u32,
    range_record_count: u64,
    retirement_root: u32,
    retirement_batch_count: u64,
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

/// Caller-owned bounded scratch for constructing one retirement batch from a
/// replacement range-root proof. The protected set stays page-index backed;
/// it is never materialized as an input-sized `u32` slice.
pub(crate) struct RangeRootRetirementStageScratch<'a> {
    pub(crate) blob_pages: &'a mut [u32],
    pub(crate) upsert_path: &'a mut [RetirementPathFrame],
    pub(crate) replacements: &'a mut [CommittedPageReplacement],
    pub(crate) releases: &'a mut [u32],
    pub(crate) roles: &'a mut [PageRoleIndexSlot],
}

/// Dedicated caller-owned scratch for building the complete protected-page
/// proof of one ordinary range replacement. The two append paths never share
/// backing: the first is immutable evidence, while every preview must start
/// from clean detached scratch.
#[derive(Debug)]
pub(crate) struct RangeRootReplacementProofScratch<'a> {
    pub(crate) initial_upsert_path: &'a mut [RetirementPathFrame],
    pub(crate) initial_replacements: &'a mut [CommittedPageReplacement],
    pub(crate) initial_releases: &'a mut [u32],
    pub(crate) initial_roles: &'a mut [PageRoleIndexSlot],
    pub(crate) preview_bitmap_replacements: &'a mut [u32],
    pub(crate) preview_blob_pages: &'a mut [u32],
    pub(crate) preview_upsert_path: &'a mut [RetirementPathFrame],
    pub(crate) preview_replacements: &'a mut [CommittedPageReplacement],
    pub(crate) preview_releases: &'a mut [u32],
    pub(crate) preview_roles: &'a mut [PageRoleIndexSlot],
    pub(crate) final_release_pages: &'a mut [u32],
    pub(crate) final_insert_pages: &'a mut [FreeBitmapInsertPage],
    pub(crate) final_cached_pages: &'a mut [FreeBitmapFinalizationCachedPage],
    pub(crate) final_index_stack: &'a mut [usize],
    pub(crate) final_cleanup_nodes: &'a mut [PrivatePageSelectiveOverlayNode],
    pub(crate) final_cleanup_path: &'a mut [PrivatePageSelectivePathEntry],
    pub(crate) final_cleanup_targets: &'a mut [usize],
}

/// Failure before an ordinary range replacement has a complete protected-page
/// proof. No variant here authorizes publication or changes target metadata.
#[derive(Debug)]
pub(crate) enum RangeRootReplacementProofError {
    SelectedGeneration,
    Bitmap(FreeBitmapCowError),
    Retirement(RetirementWriteError),
    InitialProbeChanged,
    Proof(RangeRootTransactionProofError<RangeRootReplacementPreviewError>),
}

/// A fixed-point preview failure for an ordinary range replacement.
#[derive(Debug)]
pub(crate) enum RangeRootReplacementPreviewError {
    Bitmap(FreeBitmapCowError),
    Retirement(RetirementWriteError),
    Index(PageNumberIndexError),
    AppendWitnessChanged,
}

/// Failure while privately staging the required retirement batch. A
/// post-mutation failure latches the shared shadow pool for whole-draft abort.
#[derive(Debug)]
pub(crate) enum RangeRootRetirementStageError {
    PreMutationProof(RangeRootTransactionProofStateError),
    PreMutationBitmap(FreeBitmapCowError),
    PreMutationRetirement(RetirementWriteError),
    PostMutationRetirement(RetirementWriteError),
    PostMutationBitmap(FreeBitmapCowError),
    PostMutationCapacity { actual: usize, budget: usize },
}

impl RangeRootRetirementStageError {
    pub(crate) const fn discard_required(&self) -> bool {
        matches!(
            self,
            Self::PostMutationRetirement(_)
                | Self::PostMutationBitmap(_)
                | Self::PostMutationCapacity { .. }
        )
    }
}

/// A prepared stage became inconsistent after it mutated the shared scope.
#[derive(Debug)]
pub(crate) enum RangeRootRetirementStageStateError {
    Stale,
    Proof(RangeRootTransactionProofStateError),
    Bitmap(FreeBitmapCowError),
    Retirement(RetirementWriteError),
}

/// Private evidence that the selected old range-tree pages have been appended
/// to the selected retirement tree in the exact scope that owns the pending
/// range/bitmap outputs. It has no metadata or publication authority.
#[derive(Debug)]
pub(crate) struct RangeRootRetirementStage {
    proof_seal: u64,
    scope: PrivatePageReservationScopeSeed,
    scope_id: u64,
    selected_txn: u64,
    pending_txn: u64,
    page_count: u64,
    pending_page_count: u64,
    retirement: RetirementTreeEditResult,
    blob_private_pages: usize,
    terminal_page_count: usize,
    protected_len: u64,
    has_arena: bool,
    commitment: PrivatePagePoolCommitment,
    seal: u64,
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
        || selected.retirement_batch_count > selected.txn_id - 1
        || (selected.retirement_root == 0 && selected.retirement_batch_count != 0)
        || (selected.retirement_root != 0
            && (selected.retirement_batch_count == 0
                || selected.retirement_root < 2
                || u64::from(selected.retirement_root) >= selected.page_count))
    {
        return Err(RangeRootTransactionProofError::SelectedIdentity);
    }
    Ok(RangeRootTransactionIdentity {
        txn_id: selected.txn_id,
        page_count: selected.page_count,
        range_root: selected.range_root,
        range_record_count: selected.range_record_count,
        retirement_root: selected.retirement_root,
        retirement_batch_count: selected.retirement_batch_count,
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
        u64::from(selected.retirement_root),
        selected.retirement_batch_count,
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

impl<'proof, 'workspace, 'storage> RangeRootTransactionProof<'proof, 'workspace, 'storage> {
    pub(crate) const fn materialized_result(&self) -> RangeTreeMaterializedResult {
        self.materialized
    }

    pub(crate) fn range_pages(&self) -> &[PrivatePageCoordinatorTerminalPage] {
        self.range_pages
    }

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

    /// Returns the selected retirement-tree state and the converged protected
    /// index after rechecking the proof. The later composition obtains its
    /// reader only from its bound bitmap reservation, never from this proof's
    /// caller.
    pub(crate) fn retirement_inputs(
        &mut self,
    ) -> Result<
        (
            RetirementTreeState,
            &mut PageNumberIndex<'workspace, 'storage>,
        ),
        RangeRootTransactionProofStateError,
    > {
        self.verify()?;
        let state = RetirementTreeState {
            selected_txn: self.selected.txn_id,
            page_count: self.selected.page_count,
            root: self.selected.retirement_root,
            batch_count: self.selected.retirement_batch_count,
        };
        let protected = match self.candidate {
            PageNumberIndexFixedPointCandidate::First => &mut *self.first,
            PageNumberIndexFixedPointCandidate::Second => &mut *self.second,
        };
        Ok((state, protected))
    }

    /// Scrubs all retained caller-owned index workspaces after a whole-draft
    /// abort. The proof has not acquired publication authority.
    pub(crate) fn discard_after_abort(self) {
        discard_range_root_transaction_proof_indexes(self.seed, self.first, self.second);
    }
}

const RANGE_ROOT_RETIREMENT_STAGE_HASH_SEED: u64 =
    RANGE_ROOT_PROOF_HASH_SEED ^ 0x7a2f_5db9_c190_4e63;

fn seal_range_root_retirement_stage(stage: &RangeRootRetirementStage) -> u64 {
    let mut hash = RANGE_ROOT_RETIREMENT_STAGE_HASH_SEED;
    for value in [
        stage.proof_seal,
        stage.selected_txn,
        stage.pending_txn,
        stage.page_count,
        stage.pending_page_count,
        stage.scope_id,
        u64::from(stage.retirement.root),
        stage.retirement.batch_count,
        stage.retirement.private_pages as u64,
        stage.retirement.committed_replacements as u64,
        stage.retirement.prior_private_replacements as u64,
        stage.blob_private_pages as u64,
        stage.terminal_page_count as u64,
        stage.protected_len,
    ] {
        hash = range_root_transaction_proof_hash_word(hash, value);
    }
    hash
}

impl RangeRootRetirementStage {
    pub(crate) const fn retirement_result(&self) -> RetirementTreeEditResult {
        self.retirement
    }

    pub(crate) const fn terminal_page_count(&self) -> usize {
        self.terminal_page_count
    }

    /// Rechecks the proof-bound stage after bitmap finalization has sealed the
    /// same scope. Sealing changes only the scope generation, so this verifies
    /// the stable reservation identity and the resulting retirement shape
    /// instead of comparing the old mutable scope commitment.
    #[allow(clippy::result_large_err)]
    pub(crate) fn validate_sealed_terminal<'proof, 'workspace, 'storage, 'slots, 'scope>(
        &self,
        shadow_pool: &PrivatePagePool<'slots>,
        shadow_scope: &PrivatePageReservationScope<'scope>,
        nonce: u64,
        proof: &mut RangeRootTransactionProof<'proof, 'workspace, 'storage>,
    ) -> Result<(), RangeRootRetirementStageStateError> {
        if !self.scope.matches_reservation(shadow_scope)
            || self.scope_id != shadow_scope.coordinator_scope_id()
            || shadow_pool.pending_txn() != self.pending_txn
        {
            return Err(RangeRootRetirementStageStateError::Stale);
        }
        if shadow_pool.requires_abort() {
            return Err(RangeRootRetirementStageStateError::Bitmap(
                FreeBitmapCowError::PrivatePool(PrivatePagePoolError::AbortRequired),
            ));
        }
        shadow_pool
            .validate_sealed_scope(shadow_scope, nonce)
            .map_err(|_| RangeRootRetirementStageStateError::Stale)?;

        let proof_seal = proof.seal;
        let (state, protected) = proof
            .retirement_inputs()
            .map_err(RangeRootRetirementStageStateError::Proof)?;
        if proof_seal != self.proof_seal
            || state.selected_txn != self.selected_txn
            || state.page_count != self.page_count
            || protected.len() != self.protected_len
            || self.seal != seal_range_root_retirement_stage(self)
        {
            return Err(RangeRootRetirementStageStateError::Stale);
        }

        if self.protected_len == 0 {
            if self.has_arena
                || self.retirement.root != state.root
                || self.retirement.batch_count != state.batch_count
                || self.retirement.private_pages != 0
                || self.retirement.committed_replacements != 0
                || self.retirement.prior_private_replacements != 0
                || self.blob_private_pages != 0
                || self.terminal_page_count != 0
            {
                return Err(RangeRootRetirementStageStateError::Stale);
            }
            return Ok(());
        }

        let expected_batch_count = state
            .batch_count
            .checked_add(1)
            .ok_or(RangeRootRetirementStageStateError::Stale)?;
        if !self.has_arena
            || self.retirement.root < 2
            || u64::from(self.retirement.root) >= self.pending_page_count
            || self.retirement.batch_count != expected_batch_count
            || self.retirement.private_pages == 0
            || self.retirement.committed_replacements != 0
            || self.retirement.prior_private_replacements != 0
            || self.blob_private_pages == 0
            || self
                .blob_private_pages
                .checked_add(self.retirement.private_pages)
                != Some(self.terminal_page_count)
            || self.terminal_page_count == 0
        {
            return Err(RangeRootRetirementStageStateError::Stale);
        }
        Ok(())
    }

    /// Rechecks the proof, exact reservation scope, and all private pages
    /// before a later terminal boundary consumes this stage.
    #[allow(clippy::result_large_err)]
    pub(crate) fn verify<
        'proof,
        'workspace,
        'storage,
        'bound,
        'slots,
        'scope,
        'barrier,
        'pages,
        S: CommittedPageSource + ?Sized,
    >(
        &self,
        bound: &BoundFreeBitmapReservation<'bound, 'slots, 'scope, 'barrier, 'pages, S>,
        shadow_pool: &PrivatePagePool<'slots>,
        shadow_scope: &PrivatePageReservationScope<'scope>,
        proof: &mut RangeRootTransactionProof<'proof, 'workspace, 'storage>,
    ) -> Result<(), RangeRootRetirementStageStateError> {
        if self.scope != shadow_scope.seed() || self.scope_id != shadow_scope.coordinator_scope_id()
        {
            return Err(RangeRootRetirementStageStateError::Stale);
        }
        if shadow_pool.requires_abort() {
            return Err(RangeRootRetirementStageStateError::Bitmap(
                FreeBitmapCowError::PrivatePool(PrivatePagePoolError::AbortRequired),
            ));
        }
        bound
            .validate_reclamation_scope(shadow_scope)
            .map_err(RangeRootRetirementStageStateError::Bitmap)?;
        shadow_pool
            .validate_exact_commitment(shadow_scope, &self.commitment)
            .map_err(|_| RangeRootRetirementStageStateError::Stale)?;
        let proof_seal = proof.seal;
        let (state, protected) = proof
            .retirement_inputs()
            .map_err(RangeRootRetirementStageStateError::Proof)?;
        let (selected_txn, page_count, pending_txn) = bound.selected_generation();
        if proof_seal != self.proof_seal
            || state.selected_txn != self.selected_txn
            || state.page_count != self.page_count
            || protected.len() != self.protected_len
            || selected_txn != self.selected_txn
            || page_count != self.page_count
            || pending_txn != self.pending_txn
            || bound.cow.pending_page_count() != self.pending_page_count
        {
            return Err(RangeRootRetirementStageStateError::Stale);
        }
        if self.protected_len == 0 {
            if self.has_arena
                || self.retirement.root != state.root
                || self.retirement.batch_count != state.batch_count
                || self.retirement.private_pages != 0
                || self.retirement.committed_replacements != 0
                || self.retirement.prior_private_replacements != 0
                || self.blob_private_pages != 0
                || self.terminal_page_count != 0
            {
                return Err(RangeRootRetirementStageStateError::Stale);
            }
        } else {
            let expected_batch_count = state
                .batch_count
                .checked_add(1)
                .ok_or(RangeRootRetirementStageStateError::Stale)?;
            let mut actual_terminal_pages = 0usize;
            shadow_pool
                .visit_exact_scope_layout(shadow_scope, |_, _, info| {
                    if matches!(
                        info.state,
                        crate::private_page_pool::PrivatePagePoolState::InUse {
                            owner: PrivatePageOwner::Retirement,
                            ..
                        }
                    ) {
                        actual_terminal_pages += 1;
                    }
                })
                .map_err(|error| {
                    RangeRootRetirementStageStateError::Bitmap(FreeBitmapCowError::PrivatePool(
                        error,
                    ))
                })?;
            if self.retirement.root < 2
                || u64::from(self.retirement.root) >= self.pending_page_count
                || self.retirement.batch_count != expected_batch_count
                || !self.has_arena
                || self.blob_private_pages == 0
                || self
                    .blob_private_pages
                    .checked_add(self.retirement.private_pages)
                    != Some(self.terminal_page_count)
                || actual_terminal_pages != self.terminal_page_count
            {
                return Err(RangeRootRetirementStageStateError::Stale);
            }
        }
        if self.seal != seal_range_root_retirement_stage(self) {
            return Err(RangeRootRetirementStageStateError::Stale);
        }
        Ok(())
    }

    /// Drops only private evidence after the enclosing draft begins its
    /// established whole-draft abort. The pool owns page cleanup at that
    /// boundary; this helper returns the proof's caller-owned indexes.
    pub(crate) fn discard_after_abort<'proof, 'workspace, 'storage>(
        self,
        proof: RangeRootTransactionProof<'proof, 'workspace, 'storage>,
    ) {
        proof.discard_after_abort();
    }
}

/// Streams the proof's converged protected index into a new retirement batch
/// in the exact bitmap reservation scope. It never accepts a second source,
/// changes target metadata, or produces a terminal journal.
#[allow(clippy::result_large_err, clippy::too_many_arguments, dead_code)]
pub(crate) fn stage_range_root_retirement<
    'proof,
    'workspace,
    'storage,
    'bound,
    'pool,
    'barrier,
    'pages,
    S: CommittedPageSource + ?Sized,
>(
    bound: &mut BoundFreeBitmapReservation<'bound, 'pool, 'pool, 'barrier, 'pages, S>,
    shadow_pool: &'pool PrivatePagePool<'pool>,
    shadow_scope: &PrivatePageReservationScope<'pool>,
    proof: &mut RangeRootTransactionProof<'proof, 'workspace, 'storage>,
    scratch: RangeRootRetirementStageScratch<'_>,
) -> Result<RangeRootRetirementStage, RangeRootRetirementStageError> {
    let RangeRootRetirementStageScratch {
        blob_pages,
        upsert_path,
        replacements,
        releases,
        roles,
    } = scratch;
    if shadow_pool.requires_abort() {
        return Err(RangeRootRetirementStageError::PostMutationBitmap(
            FreeBitmapCowError::PrivatePool(PrivatePagePoolError::AbortRequired),
        ));
    }
    let proof_seal = proof.seal;
    let (state, protected) = proof
        .retirement_inputs()
        .map_err(RangeRootRetirementStageError::PreMutationProof)?;
    let expected_pending_txn = state.selected_txn.checked_add(1).ok_or(
        RangeRootRetirementStageError::PreMutationRetirement(
            RetirementWriteError::SelectedTransactionOverflow(state.selected_txn),
        ),
    )?;
    let (selected_txn, page_count, pending_txn) = bound.selected_generation();
    if selected_txn != state.selected_txn
        || page_count != state.page_count
        || pending_txn != expected_pending_txn
    {
        return Err(RangeRootRetirementStageError::PreMutationProof(
            RangeRootTransactionProofStateError::Stale,
        ));
    }
    bound
        .validate_reclamation_scope(shadow_scope)
        .map_err(RangeRootRetirementStageError::PreMutationBitmap)?;
    let scope_capacity = shadow_pool
        .scope_status(shadow_scope)
        .map_err(FreeBitmapCowError::PrivatePool)
        .map_err(RangeRootRetirementStageError::PreMutationBitmap)?
        .capacity;
    let pending_page_count = bound.cow.pending_page_count();
    let protected_len = protected.len();
    if protected_len == 0 {
        let commitment = shadow_pool
            .exact_commitment(shadow_scope)
            .map_err(FreeBitmapCowError::PrivatePool)
            .map_err(RangeRootRetirementStageError::PreMutationBitmap)?;
        let mut stage = RangeRootRetirementStage {
            proof_seal,
            scope: shadow_scope.seed(),
            scope_id: shadow_scope.coordinator_scope_id(),
            selected_txn,
            pending_txn,
            page_count,
            pending_page_count,
            retirement: RetirementTreeEditResult {
                root: state.root,
                batch_count: state.batch_count,
                private_pages: 0,
                committed_replacements: 0,
                prior_private_replacements: 0,
            },
            blob_private_pages: 0,
            terminal_page_count: 0,
            protected_len,
            has_arena: false,
            commitment,
            seal: 0,
        };
        stage.seal = seal_range_root_retirement_stage(&stage);
        return Ok(stage);
    }

    let mut arena =
        PrivatePageArena::from_scoped_pool(shadow_pool, shadow_scope, expected_pending_txn)
            .map_err(RangeRootRetirementStageError::PreMutationRetirement)?;
    let blob = match RetirementBlobBuilder::build_from_index(
        protected,
        &mut arena,
        &mut BlobBuildScratch::new(blob_pages),
    ) {
        Ok(blob) => blob,
        Err(error) if shadow_pool.requires_abort() => {
            return Err(RangeRootRetirementStageError::PostMutationRetirement(error));
        }
        Err(error) => return Err(RangeRootRetirementStageError::PreMutationRetirement(error)),
    };
    let blob_private_pages = blob.private_pages();
    let mut replacements = CommittedReplacementLedger::new(replacements);
    let mut releases = PrivateReleaseBuffer::new(releases);
    let mut roles = PageRoleIndex::new(roles);
    let result = match RetirementTreeEditor::upsert_newest(
        bound.reclamation_source(),
        state,
        blob,
        upsert_path,
        &mut replacements,
        &mut releases,
        &mut roles,
    ) {
        Ok(result) => result,
        Err(error) => {
            shadow_pool.require_abort();
            return Err(RangeRootRetirementStageError::PostMutationRetirement(error));
        }
    };
    let terminal_page_count = match blob_private_pages.checked_add(result.private_pages) {
        Some(count) => count,
        None => {
            shadow_pool.require_abort();
            return Err(RangeRootRetirementStageError::PostMutationCapacity {
                actual: usize::MAX,
                budget: scope_capacity,
            });
        }
    };
    if terminal_page_count == 0 || terminal_page_count > scope_capacity {
        shadow_pool.require_abort();
        return Err(RangeRootRetirementStageError::PostMutationCapacity {
            actual: terminal_page_count,
            budget: scope_capacity,
        });
    }
    if let Err(error) = bound.synchronize_reclamation_scope(shadow_scope) {
        shadow_pool.require_abort();
        return Err(RangeRootRetirementStageError::PostMutationBitmap(error));
    }
    let commitment = match shadow_pool.exact_commitment(shadow_scope) {
        Ok(commitment) => commitment,
        Err(error) => {
            shadow_pool.require_abort();
            return Err(RangeRootRetirementStageError::PostMutationBitmap(
                FreeBitmapCowError::PrivatePool(error),
            ));
        }
    };
    let mut stage = RangeRootRetirementStage {
        proof_seal,
        scope: shadow_scope.seed(),
        scope_id: shadow_scope.coordinator_scope_id(),
        selected_txn,
        pending_txn,
        page_count,
        pending_page_count,
        retirement: result,
        blob_private_pages,
        terminal_page_count,
        protected_len,
        has_arena: true,
        commitment,
        seal: 0,
    };
    stage.seal = seal_range_root_retirement_stage(&stage);
    Ok(stage)
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
    prepare_range_root_transaction_proof_with_initial_replacements::<K, S, E>(
        source,
        selected,
        materialized,
        range_pages,
        seed,
        first,
        second,
        ownership_scratch,
        &[],
        max_range_work,
        max_iterations,
        preview,
    )
}

/// As [`prepare_range_root_transaction_proof`], but seeds the protected-page
/// fixed point with already-proven committed replacements before its first
/// candidate clone.
///
/// An ordinary retirement append uses this for the selected retirement-tree
/// pages the append will replace. Those pages must be in the first prospective
/// blob; waiting for the first append preview would make the writer reject its
/// own unlisted replacements.
#[allow(clippy::result_large_err, clippy::too_many_arguments)]
pub(crate) fn prepare_range_root_transaction_proof_with_initial_replacements<
    'proof,
    'workspace,
    'storage,
    K,
    S,
    E,
>(
    source: &S,
    selected: MetaV4,
    materialized: RangeTreeMaterializedResult,
    range_pages: &'proof [PrivatePageCoordinatorTerminalPage],
    seed: &'proof mut PageNumberIndex<'workspace, 'storage>,
    first: &'proof mut PageNumberIndex<'workspace, 'storage>,
    second: &'proof mut PageNumberIndex<'workspace, 'storage>,
    ownership_scratch: &mut RangeTreeOwnershipScratch,
    initial_replacements: &[CommittedPageReplacement],
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
    for replacement in initial_replacements {
        if let Err(error) = seed.insert(replacement.pgno) {
            discard_range_root_transaction_proof_indexes(seed, first, second);
            return Err(RangeRootTransactionProofError::FixedPoint(
                PageNumberIndexFixedPointError::Index(error),
            ));
        }
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

/// Builds the complete protected-page proof for an ordinary replacement range
/// root while the exact bitmap reservation and operation-barrier authority are
/// still held.
///
/// The real append is intentionally not performed here. This first proves its
/// old retirement-tree replacements, seeds them before the fixed point, and
/// then replays a matching append in each detached bitmap-finalization pass.
/// Therefore a later real append cannot discover an unlisted committed page
/// after it has started claiming the live shadow scope.
#[allow(
    clippy::result_large_err,
    clippy::too_many_arguments,
    clippy::type_complexity
)]
pub(crate) fn prepare_range_root_replacement_proof<
    'proof,
    'workspace,
    'storage,
    'bound,
    'pool,
    'barrier,
    'pages,
    K,
    S,
>(
    bound: &mut BoundFreeBitmapReservation<'bound, 'pool, 'pool, 'barrier, 'pages, S>,
    shadow_pool: &'pool PrivatePagePool<'pool>,
    shadow_scope: &PrivatePageReservationScope<'pool>,
    selected: MetaV4,
    materialized: RangeTreeMaterializedResult,
    range_pages: &'proof [PrivatePageCoordinatorTerminalPage],
    seed: &'proof mut PageNumberIndex<'workspace, 'storage>,
    first: &'proof mut PageNumberIndex<'workspace, 'storage>,
    second: &'proof mut PageNumberIndex<'workspace, 'storage>,
    ownership_scratch: &mut RangeTreeOwnershipScratch,
    max_range_work: u64,
    max_iterations: usize,
    scratch: RangeRootReplacementProofScratch<'_>,
) -> Result<RangeRootTransactionProof<'proof, 'workspace, 'storage>, RangeRootReplacementProofError>
where
    K: IpKey,
    S: CommittedPageSource + ?Sized,
{
    let (selected_txn, page_count, pending_txn) = bound.selected_generation();
    if selected.txn_id != selected_txn
        || selected.page_count != page_count
        || selected.txn_id.checked_add(1) != Some(pending_txn)
        || shadow_pool.pending_txn() != pending_txn
    {
        return Err(RangeRootReplacementProofError::SelectedGeneration);
    }
    if shadow_pool.requires_abort() {
        return Err(RangeRootReplacementProofError::Bitmap(
            FreeBitmapCowError::PrivatePool(PrivatePagePoolError::AbortRequired),
        ));
    }
    bound
        .validate_reclamation_scope(shadow_scope)
        .map_err(RangeRootReplacementProofError::Bitmap)?;

    let RangeRootReplacementProofScratch {
        initial_upsert_path,
        initial_replacements,
        initial_releases,
        initial_roles,
        preview_bitmap_replacements,
        preview_blob_pages,
        preview_upsert_path,
        preview_replacements,
        preview_releases,
        preview_roles,
        final_release_pages,
        final_insert_pages,
        final_cached_pages,
        final_index_stack,
        final_cleanup_nodes,
        final_cleanup_path,
        final_cleanup_targets,
    } = scratch;
    let state = RetirementTreeState {
        selected_txn,
        page_count,
        root: selected.retirement_root,
        batch_count: selected.retirement_batch_count,
    };
    let source = bound.reclamation_source();
    let (initial_probe, initial_len) = {
        let mut arena = PrivatePageArena::from_scoped_pool(shadow_pool, shadow_scope, pending_txn)
            .map_err(RangeRootReplacementProofError::Retirement)?;
        let mut replacements = CommittedReplacementLedger::new(initial_replacements);
        let mut releases = PrivateReleaseBuffer::new(initial_releases);
        let mut roles = PageRoleIndex::new(initial_roles);
        let probe = RetirementTreeEditor::probe_append_newest(
            source,
            state,
            &mut arena,
            initial_upsert_path,
            &mut replacements,
            &mut releases,
            &mut roles,
        )
        .map_err(RangeRootReplacementProofError::Retirement)?;
        if probe.replacement_count != replacements.entries().len()
            || !releases.entries_from(0).is_empty()
            || arena
                .in_use_count()
                .map_err(RangeRootReplacementProofError::Retirement)?
                != 0
        {
            return Err(RangeRootReplacementProofError::InitialProbeChanged);
        }
        (probe, replacements.entries().len())
    };
    let initial_entries = &initial_replacements[..initial_len];

    prepare_range_root_transaction_proof_with_initial_replacements::<K, S, _>(
        source,
        selected,
        materialized,
        range_pages,
        seed,
        first,
        second,
        ownership_scratch,
        initial_entries,
        max_range_work,
        max_iterations,
        |candidate, additions| {
            let bitmap_len = bound
                .preview_terminal_replacements_with_stage(
                    FreeBitmapFinalizationScratch {
                        release_pages: &mut *final_release_pages,
                        insert_pages: &mut *final_insert_pages,
                        cached_pages: &mut *final_cached_pages,
                        index_stack: &mut *final_index_stack,
                        cleanup_nodes: &mut *final_cleanup_nodes,
                        cleanup_path: &mut *final_cleanup_path,
                        cleanup_targets: &mut *final_cleanup_targets,
                    },
                    &mut *preview_bitmap_replacements,
                    |_, stage_pool, stage_scope| {
                        let mut arena = PrivatePageArena::from_scoped_pool(
                            stage_pool,
                            stage_scope,
                            pending_txn,
                        )
                        .map_err(RangeRootReplacementPreviewError::Retirement)?;
                        let blob = RetirementBlobBuilder::build_from_index(
                            candidate,
                            &mut arena,
                            &mut BlobBuildScratch::new(&mut *preview_blob_pages),
                        )
                        .map_err(RangeRootReplacementPreviewError::Retirement)?;
                        let mut replacements =
                            CommittedReplacementLedger::new(&mut *preview_replacements);
                        let mut releases = PrivateReleaseBuffer::new(&mut *preview_releases);
                        let mut roles = PageRoleIndex::new(&mut *preview_roles);
                        let result = RetirementTreeEditor::upsert_newest(
                            source,
                            state,
                            blob,
                            &mut *preview_upsert_path,
                            &mut replacements,
                            &mut releases,
                            &mut roles,
                        )
                        .map_err(RangeRootReplacementPreviewError::Retirement)?;
                        let witness = RetirementAppendReplacementProbe {
                            replacement_count: replacements.entries().len(),
                            tree_private_page_budget: result.private_pages,
                        };
                        if witness != initial_probe
                            || replacements.entries() != initial_entries
                            || !releases.entries_from(0).is_empty()
                        {
                            return Err(RangeRootReplacementPreviewError::AppendWitnessChanged);
                        }
                        Ok::<_, RangeRootReplacementPreviewError>(witness)
                    },
                )
                .map_err(|error| match error {
                    FreeBitmapFinalizationPreviewError::Bitmap(error) => {
                        RangeRootReplacementPreviewError::Bitmap(error)
                    }
                    FreeBitmapFinalizationPreviewError::Stage(error) => error,
                })?;
            for &pgno in &preview_bitmap_replacements[..bitmap_len] {
                additions
                    .add(pgno)
                    .map_err(RangeRootReplacementPreviewError::Index)?;
            }
            Ok(())
        },
    )
    .map_err(RangeRootReplacementProofError::Proof)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bitmap_cow::{
        complete_free_bitmap_reclamation, BitmapCowArenaBinding, BitmapCowIndexNode,
        FreeBitmapFinalizationCachedPage, FreeBitmapFinalizationScratch, FreeBitmapInsertPage,
        FreeBitmapReclamationTicket, FreeBitmapReservationBuffers, FreeBitmapReservationPlanner,
        FreeBitmapReservationSourceNode, FreeBitmapReservationStageBuffers, ReservedBitmapPage,
        VerifiedBitmapPage,
    };
    use crate::blob_page::BlobKind;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::key::Ipv4Key;
    use crate::page::{write_crc32c, PageHeader, PageType, PAGE_HEADER_SIZE};
    use crate::page_number_index::{PageNumberIndexPage, PageNumberIndexWorkspace};
    use crate::page_source::SlicePageSource;
    use crate::private_page_pool::{
        PrivatePageCompositeBind, PrivatePagePool, PrivatePagePoolSlot,
    };
    use crate::range_builder::RangeTreeBuildWorkspace;
    use crate::range_page::{encode_branch, encode_leaf, RangeBranchEntry, RangeRecord};
    use crate::range_staging::{
        RangeTreePayloadReservationSlot, RangeTreePayloadScratch, RangeTreePhysicalAssignment,
        RangeTreeStaging, RangeTreeStagingPage,
    };
    use crate::retirement_page::RetirementBatch;
    use crate::retirement_reader::{
        test_reclaimed_pages, RetirementReclaimBarrier, RetirementReclaimFence,
        RetirementReclamation,
    };
    use crate::retirement_writer::{CommittedPageOrigin, RetirementWriteError};
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

    #[derive(Debug)]
    struct RangeReplacementBarrier;

    impl RetirementReclaimBarrier for RangeReplacementBarrier {}

    static RANGE_REPLACEMENT_BARRIER: RangeReplacementBarrier = RangeReplacementBarrier;

    fn ownership_image() -> (Vec<u8>, MetaV4) {
        ownership_image_with_page_count(12)
    }

    fn ownership_image_with_page_count(page_count: u64) -> (Vec<u8>, MetaV4) {
        assert!(page_count >= 12);
        let mut bytes = vec![0; page_count as usize * PAGE_SIZE];
        encode_branch::<Ipv4Key>(
            page_mut(&mut bytes, 8),
            3,
            1,
            page_count,
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
        (bytes, selected_meta(page_count, 8, 2))
    }

    fn free_bitmap_leaf(page: &mut [u8; PAGE_SIZE], txn: u64, bits: &[u32]) {
        page.fill(0);
        for &bit in bits {
            let word = usize::try_from(bit / 64).unwrap();
            let offset = 32 + word * 8;
            let value = u64::from_le_bytes(page[offset..offset + 8].try_into().unwrap());
            page[offset..offset + 8]
                .copy_from_slice(&(value | (1_u64 << (bit % 64))).to_le_bytes());
        }
        let mut nonzero_words = 0_u16;
        for offset in (32..4032).step_by(8) {
            nonzero_words +=
                u16::from(u64::from_le_bytes(page[offset..offset + 8].try_into().unwrap()) != 0);
        }
        PageHeader {
            page_type: PageType::BitmapLeaf,
            born_txn: txn,
            item_count: nonzero_words,
            level: 0,
            lower: 4032,
            upper: PAGE_SIZE as u16,
            aux: 1,
            page_crc32c: 0,
        }
        .encode_into(page);
        write_crc32c(page);
    }

    fn retirement_leaf(page: &mut [u8; PAGE_SIZE], txn: u64, batches: &[RetirementBatch]) {
        *page = [0; PAGE_SIZE];
        PageHeader {
            page_type: PageType::RetirementLeaf,
            born_txn: txn,
            item_count: batches.len() as u16,
            level: 0,
            lower: (usize::from(PAGE_HEADER_SIZE) + batches.len() * 32) as u16,
            upper: PAGE_SIZE as u16,
            aux: 0,
            page_crc32c: 0,
        }
        .encode_into(page);
        for (index, batch) in batches.iter().enumerate() {
            let at = usize::from(PAGE_HEADER_SIZE) + index * 32;
            page[at + 8..at + 16].copy_from_slice(&batch.retired_by_txn.to_le_bytes());
            page[at + 16..at + 24].copy_from_slice(&batch.page_count.to_le_bytes());
            page[at + 24..at + 28].copy_from_slice(&batch.page_list_blob_root.to_le_bytes());
        }
        write_crc32c(page);
    }

    fn retirement_blob(page: &mut [u8; PAGE_SIZE], txn: u64, pages: &[u32]) {
        *page = [0; PAGE_SIZE];
        let length = core::mem::size_of_val(pages);
        PageHeader {
            page_type: PageType::BlobLeaf,
            born_txn: txn,
            item_count: 1,
            level: 0,
            lower: (48 + length) as u16,
            upper: PAGE_SIZE as u16,
            aux: BlobKind::RetirementPageList as u32,
            page_crc32c: 0,
        }
        .encode_into(page);
        page[40..42].copy_from_slice(&(length as u16).to_le_bytes());
        for (index, value) in pages.iter().enumerate() {
            let at = 48 + index * size_of::<u32>();
            page[at..at + size_of::<u32>()].copy_from_slice(&value.to_le_bytes());
        }
        write_crc32c(page);
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

    struct RangeRootStageBitmapStorage {
        arena: [ReservedBitmapPage; 24],
        pool_validation: [PrivatePageCompositeBind; 24],
        arena_bindings: [BitmapCowArenaBinding; 24],
        candidates: [u32; 24],
        verified: [VerifiedBitmapPage; 24],
        replacements: [u32; 24],
        index: [BitmapCowIndexNode; 72],
        available: [usize; 24],
        source_nodes: [FreeBitmapReservationSourceNode; 48],
        ticket: FreeBitmapReclamationTicket,
        stage_arena: [ReservedBitmapPage; 24],
        stage_bindings: [BitmapCowArenaBinding; 24],
        stage_candidates: [u32; 24],
        stage_verified: [VerifiedBitmapPage; 24],
        stage_replacements: [u32; 24],
        stage_index: [BitmapCowIndexNode; 72],
        stage_available: [usize; 24],
    }

    impl RangeRootStageBitmapStorage {
        fn new() -> Self {
            Self {
                arena: [const { ReservedBitmapPage::empty() }; 24],
                pool_validation: [PrivatePageCompositeBind::empty(); 24],
                arena_bindings: [BitmapCowArenaBinding::empty(); 24],
                candidates: [0; 24],
                verified: [const { VerifiedBitmapPage::empty() }; 24],
                replacements: [0; 24],
                index: [BitmapCowIndexNode::empty(); 72],
                available: [0; 24],
                source_nodes: [FreeBitmapReservationSourceNode::empty(); 48],
                ticket: FreeBitmapReclamationTicket::new(),
                stage_arena: [const { ReservedBitmapPage::empty() }; 24],
                stage_bindings: [BitmapCowArenaBinding::empty(); 24],
                stage_candidates: [0; 24],
                stage_verified: [const { VerifiedBitmapPage::empty() }; 24],
                stage_replacements: [0; 24],
                stage_index: [BitmapCowIndexNode::empty(); 72],
                stage_available: [0; 24],
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

    fn stage_post_blob_capacity_failure() -> (
        RangeRootRetirementStageError,
        RangeRootRetirementStageError,
        bool,
    ) {
        let (mut bytes, selected) = ownership_image();
        free_bitmap_leaf(page_mut(&mut bytes, 2), selected.txn_id, &[5, 6, 7]);
        let source = SlicePageSource::new(&bytes, selected.page_count);
        let mut storage = RangeRootStageBitmapStorage::new();
        let plan = FreeBitmapReservationPlanner::new(
            &source,
            selected.txn_id,
            selected.page_count,
            2,
            2,
            storage.buffers(),
        )
        .unwrap()
        .plan_capacity()
        .unwrap();
        let mut pool_slots = [const { PrivatePagePoolSlot::empty() }; 8];
        let pool = PrivatePagePool::new_vacant(
            &mut pool_slots,
            selected.page_count,
            selected.page_count,
            selected.txn_id + 1,
        )
        .unwrap();
        let scope = pool.reserve_scope(plan.required_private_pages()).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let reclaimed = test_reclaimed_pages(&[10]).unwrap();
        let bitmap_proof =
            complete_free_bitmap_reclamation(request, RetirementReclamation::Reclaimed(reclaimed))
                .unwrap();
        let mut bound = attachment.bind(bitmap_proof).unwrap();

        let mut logical_pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging = RangeTreeStaging::<Ipv4Key>::new(
            &mut logical_pages,
            selected.txn_id + 1,
            ValueKind::Direct,
        )
        .unwrap();
        let mut range_workspace = RangeTreeBuildWorkspace::new();
        let mut builder = range_workspace
            .begin(
                selected.txn_id + 1,
                ValueKind::Direct,
                staging.logical_page_limit(),
            )
            .unwrap();
        builder
            .push(
                &mut staging,
                RangeRecord {
                    from: Ipv4Key(30),
                    to: Ipv4Key(40),
                    value: 1,
                },
            )
            .unwrap();
        let built = builder.finish(&mut staging).unwrap();
        let staged = staging.finish(built).unwrap();
        let mut assignments = [RangeTreePhysicalAssignment::empty(); 1];
        let mut payload_slots = [RangeTreePayloadReservationSlot::empty(); 1];
        let mut range_terminal = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        let materialized = bound
            .stage_range_payload(
                &scope,
                &staging,
                staged,
                &mut RangeTreePayloadScratch {
                    assignments: &mut assignments,
                    slots: &mut payload_slots,
                    terminal_pages: &mut range_terminal,
                },
            )
            .unwrap();
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
        let mut proof = prepare_range_root_transaction_proof::<Ipv4Key, _, ()>(
            &source,
            selected,
            materialized,
            &range_terminal,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            4,
            1,
            |_, _| Ok(()),
        )
        .unwrap();
        let mut blob_pages = [0_u32; 1];
        let mut upsert_path = [RetirementPathFrame::new(); 8];
        let mut replacements = [CommittedPageReplacement {
            pgno: 0,
            origin: CommittedPageOrigin::RetirementTree,
        }; 8];
        let mut releases = [0_u32; 8];
        let mut roles = [PageRoleIndexSlot::new(); 16];
        let error = stage_range_root_retirement(
            &mut bound,
            &pool,
            &scope,
            &mut proof,
            RangeRootRetirementStageScratch {
                blob_pages: &mut blob_pages,
                upsert_path: &mut upsert_path,
                replacements: &mut replacements,
                releases: &mut releases,
                roles: &mut roles,
            },
        )
        .unwrap_err();
        let abort_required = pool.requires_abort();
        let retry = stage_range_root_retirement(
            &mut bound,
            &pool,
            &scope,
            &mut proof,
            RangeRootRetirementStageScratch {
                blob_pages: &mut blob_pages,
                upsert_path: &mut upsert_path,
                replacements: &mut replacements,
                releases: &mut releases,
                roles: &mut roles,
            },
        )
        .unwrap_err();
        proof.discard_after_abort();
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());
        (error, retry, abort_required)
    }

    #[test]
    fn stages_proof_retirement_in_the_bound_range_scope() {
        let (mut bytes, selected) = ownership_image();
        free_bitmap_leaf(page_mut(&mut bytes, 2), selected.txn_id, &[5, 6, 7]);
        let source = SlicePageSource::new(&bytes, selected.page_count);

        let mut storage = RangeRootStageBitmapStorage::new();
        let plan = FreeBitmapReservationPlanner::new(
            &source,
            selected.txn_id,
            selected.page_count,
            2,
            3,
            storage.buffers(),
        )
        .unwrap()
        .plan_capacity()
        .unwrap();
        let mut pool_slots = [const { PrivatePagePoolSlot::empty() }; 8];
        let pool = PrivatePagePool::new_vacant(
            &mut pool_slots,
            selected.page_count,
            selected.page_count,
            selected.txn_id + 1,
        )
        .unwrap();
        let scope = pool.reserve_scope(plan.required_private_pages()).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let reclaimed = test_reclaimed_pages(&[10]).unwrap();
        let bitmap_proof =
            complete_free_bitmap_reclamation(request, RetirementReclamation::Reclaimed(reclaimed))
                .unwrap();
        let mut bound = attachment.bind(bitmap_proof).unwrap();

        let mut logical_pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging = RangeTreeStaging::<Ipv4Key>::new(
            &mut logical_pages,
            selected.txn_id + 1,
            ValueKind::Direct,
        )
        .unwrap();
        let mut range_workspace = RangeTreeBuildWorkspace::new();
        let mut builder = range_workspace
            .begin(
                selected.txn_id + 1,
                ValueKind::Direct,
                staging.logical_page_limit(),
            )
            .unwrap();
        builder
            .push(
                &mut staging,
                RangeRecord {
                    from: Ipv4Key(30),
                    to: Ipv4Key(40),
                    value: 1,
                },
            )
            .unwrap();
        let built = builder.finish(&mut staging).unwrap();
        let staged = staging.finish(built).unwrap();
        let mut assignments = [RangeTreePhysicalAssignment::empty(); 1];
        let mut payload_slots = [RangeTreePayloadReservationSlot::empty(); 1];
        let mut range_terminal = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        let materialized = bound
            .stage_range_payload(
                &scope,
                &staging,
                staged,
                &mut RangeTreePayloadScratch {
                    assignments: &mut assignments,
                    slots: &mut payload_slots,
                    terminal_pages: &mut range_terminal,
                },
            )
            .unwrap();

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
        let mut proof = prepare_range_root_transaction_proof::<Ipv4Key, _, ()>(
            &source,
            selected,
            materialized,
            &range_terminal,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            4,
            1,
            |_, _| Ok(()),
        )
        .unwrap();

        let before = pool.scope_status(&scope).unwrap();
        let mut short_blob_pages = [];
        let mut short_path = [RetirementPathFrame::new(); 8];
        let mut short_replacements = [CommittedPageReplacement {
            pgno: 0,
            origin: CommittedPageOrigin::RetirementTree,
        }; 8];
        let mut short_releases = [0_u32; 8];
        let mut short_roles = [PageRoleIndexSlot::new(); 16];
        let error = stage_range_root_retirement(
            &mut bound,
            &pool,
            &scope,
            &mut proof,
            RangeRootRetirementStageScratch {
                blob_pages: &mut short_blob_pages,
                upsert_path: &mut short_path,
                replacements: &mut short_replacements,
                releases: &mut short_releases,
                roles: &mut short_roles,
            },
        )
        .unwrap_err();
        assert!(
            matches!(
                error,
                RangeRootRetirementStageError::PreMutationRetirement(
                    RetirementWriteError::BlobBuildScratchTooSmall {
                        required: 1,
                        actual: 0,
                    }
                )
            ),
            "unexpected short-scratch error: {error:?}"
        );
        assert!(!error.discard_required());
        assert_eq!(pool.scope_status(&scope).unwrap(), before);

        let mut blob_pages = [0_u32; 1];
        let mut upsert_path = [RetirementPathFrame::new(); 8];
        let mut replacements = [CommittedPageReplacement {
            pgno: 0,
            origin: CommittedPageOrigin::RetirementTree,
        }; 8];
        let mut releases = [0_u32; 8];
        let mut roles = [PageRoleIndexSlot::new(); 16];
        let (stage, allocations) = count_thread_allocations(|| {
            stage_range_root_retirement(
                &mut bound,
                &pool,
                &scope,
                &mut proof,
                RangeRootRetirementStageScratch {
                    blob_pages: &mut blob_pages,
                    upsert_path: &mut upsert_path,
                    replacements: &mut replacements,
                    releases: &mut releases,
                    roles: &mut roles,
                },
            )
        });
        assert_eq!(allocations, 0);
        let stage = stage.unwrap();
        assert_eq!(stage.retirement_result().batch_count, 1);
        assert!(stage.retirement_result().root >= 2);
        assert_eq!(stage.terminal_page_count(), 2);
        stage.verify(&bound, &pool, &scope, &mut proof).unwrap();

        let requirements = bound.finalization_scratch_requirements().unwrap();
        let mut release = vec![0; requirements.release_pages];
        let mut insert: Vec<_> = (0..requirements.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cache = vec![FreeBitmapFinalizationCachedPage::empty(); requirements.cached_pages];
        let mut stack = vec![usize::MAX; requirements.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            requirements.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            requirements.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; requirements.cleanup_targets];

        proof.selected.retirement_root = 2;
        let before_finalization = pool.test_mutation_snapshot();
        let (retry_bound, error) = match bound.finalize_range_root_retirement(
            &stage,
            &mut proof,
            FreeBitmapFinalizationScratch {
                release_pages: &mut release,
                insert_pages: &mut insert,
                cached_pages: &mut cache,
                index_stack: &mut stack,
                cleanup_nodes: &mut cleanup_nodes,
                cleanup_path: &mut cleanup_path,
                cleanup_targets: &mut cleanup_targets,
            },
        ) {
            Ok(_) => panic!("forged proof must fail before finalization"),
            Err(parts) => parts,
        };
        assert_eq!(error, FreeBitmapCowError::StaleReservationPredecessor);
        assert_eq!(pool.test_mutation_snapshot(), before_finalization);
        let bound = retry_bound;
        assert!(matches!(
            stage.verify(&bound, &pool, &scope, &mut proof),
            Err(RangeRootRetirementStageStateError::Proof(
                RangeRootTransactionProofStateError::Stale
            ))
        ));
        proof.selected.retirement_root = 0;
        stage.verify(&bound, &pool, &scope, &mut proof).unwrap();

        let (finalized, allocations) = count_thread_allocations(|| {
            bound.finalize_range_root_retirement(
                &stage,
                &mut proof,
                FreeBitmapFinalizationScratch {
                    release_pages: &mut release,
                    insert_pages: &mut insert,
                    cached_pages: &mut cache,
                    index_stack: &mut stack,
                    cleanup_nodes: &mut cleanup_nodes,
                    cleanup_path: &mut cleanup_path,
                    cleanup_targets: &mut cleanup_targets,
                },
            )
        });
        assert_eq!(allocations, 0);
        let finalized = finalized.unwrap();
        assert_eq!(finalized.output.range_terminal_page_count(), 1);
        assert_eq!(
            finalized.output.retirement_terminal_page_count(),
            stage.terminal_page_count()
        );

        let mut bitmap_terminal = vec![
            PrivatePageCoordinatorTerminalPage::empty();
            finalized.output.bitmap_terminal_page_count()
        ];
        let mut retained_range_terminal = vec![
            PrivatePageCoordinatorTerminalPage::empty();
            finalized.output.range_terminal_page_count()
        ];
        let mut retirement_terminal = vec![
            PrivatePageCoordinatorTerminalPage::empty();
            finalized.output.retirement_terminal_page_count()
        ];
        retained_range_terminal[0] = range_terminal[0].clone();
        let before_export = finalized.output.test_pool_mutation_snapshot();
        let (
            output,
            successor,
            bitmap_terminal,
            retained_range_terminal,
            retirement_terminal,
            error,
        ) = match finalized
            .output
            .prepare_range_bitmap_retirement_terminal_export(
                finalized.successor,
                &stage,
                &mut proof,
                &mut bitmap_terminal,
                &mut retained_range_terminal,
                &mut retirement_terminal,
            ) {
            Ok(_) => panic!("dirty range journal must fail before terminal export"),
            Err(parts) => parts,
        };
        assert_eq!(error, FreeBitmapCowError::StaleReservationPredecessor);
        assert_eq!(output.test_pool_mutation_snapshot(), before_export);
        retained_range_terminal.fill(PrivatePageCoordinatorTerminalPage::empty());
        let (export, allocations) = count_thread_allocations(|| {
            output.prepare_range_bitmap_retirement_terminal_export(
                successor,
                &stage,
                &mut proof,
                bitmap_terminal,
                retained_range_terminal,
                retirement_terminal,
            )
        });
        assert_eq!(allocations, 0);
        let export = match export {
            Ok(export) => export,
            Err((_output, _successor, _bitmap, _range, _retirement, error)) => {
                panic!("triple terminal export failed: {error:?}")
            }
        };
        assert_eq!(export.materialized(), materialized);
        assert_eq!(export.range_pages(), range_terminal);
        assert_eq!(export.retirement(), stage.retirement_result());
        assert!(export
            .retirement_pages()
            .iter()
            .all(|page| page.owner == PrivatePageOwner::Retirement));

        let mut combined = vec![
            PrivatePageCoordinatorTerminalPage::empty();
            export.range_pages().len()
                + export.bitmap_pages().len()
                + export.retirement_pages().len()
        ];
        let (produced, allocations) =
            count_thread_allocations(|| export.merge_terminal_journals(&mut combined));
        assert_eq!(allocations, 0);
        let produced = match produced {
            Ok(produced) => produced,
            Err((_export, _combined, error)) => {
                panic!("three-owner terminal merge failed: {error:?}")
            }
        };
        assert!(produced
            .pages()
            .windows(2)
            .all(|pages| pages[0].pgno < pages[1].pgno));
        assert_eq!(produced.range_target(), Some(materialized));
        assert_eq!(
            produced
                .pages()
                .iter()
                .filter(|page| page.owner == PrivatePageOwner::Range)
                .count(),
            1
        );
        assert_eq!(
            produced
                .pages()
                .iter()
                .filter(|page| page.owner == PrivatePageOwner::Retirement)
                .count(),
            stage.terminal_page_count()
        );
        pool.require_abort();
        stage.discard_after_abort(proof);
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());
    }

    #[test]
    fn post_blob_capacity_failure_poisons_the_whole_draft() {
        let (error, retry, abort_required) = stage_post_blob_capacity_failure();
        assert!(
            matches!(
                error,
                RangeRootRetirementStageError::PostMutationRetirement(
                    RetirementWriteError::PrivatePool(_)
                )
            ),
            "unexpected error: {error:?}"
        );
        assert!(error.discard_required());
        assert!(matches!(
            retry,
            RangeRootRetirementStageError::PostMutationBitmap(FreeBitmapCowError::PrivatePool(
                PrivatePagePoolError::AbortRequired
            ))
        ));
        assert!(retry.discard_required());
        assert!(abort_required);
    }

    #[test]
    fn stages_legal_empty_selected_range_root_without_retirement_output() {
        let selected = selected_meta(12, 0, 0);
        let mut bytes = vec![0; selected.page_count as usize * PAGE_SIZE];
        free_bitmap_leaf(page_mut(&mut bytes, 2), selected.txn_id, &[5]);
        let source = SlicePageSource::new(&bytes, selected.page_count);
        let mut storage = RangeRootStageBitmapStorage::new();
        let plan = FreeBitmapReservationPlanner::new(
            &source,
            selected.txn_id,
            selected.page_count,
            2,
            1,
            storage.buffers(),
        )
        .unwrap()
        .plan_capacity()
        .unwrap();
        let mut pool_slots = [const { PrivatePagePoolSlot::empty() }; 8];
        let pool = PrivatePagePool::new_vacant(
            &mut pool_slots,
            selected.page_count,
            selected.page_count,
            selected.txn_id + 1,
        )
        .unwrap();
        let scope = pool.reserve_scope(plan.required_private_pages()).unwrap();
        let (attachment, request) = plan.attach(&pool, &scope).unwrap();
        let reclaimed = test_reclaimed_pages(&[10]).unwrap();
        let bitmap_proof =
            complete_free_bitmap_reclamation(request, RetirementReclamation::Reclaimed(reclaimed))
                .unwrap();
        let mut bound = attachment.bind(bitmap_proof).unwrap();

        // An empty *selected* range root still permits a newly materialized
        // range root.  That is the useful terminal case: range output exists,
        // but there are no old range pages to retire.
        let mut logical_pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging = RangeTreeStaging::<Ipv4Key>::new(
            &mut logical_pages,
            selected.txn_id + 1,
            ValueKind::Direct,
        )
        .unwrap();
        let mut range_workspace = RangeTreeBuildWorkspace::new();
        let mut builder = range_workspace
            .begin(
                selected.txn_id + 1,
                ValueKind::Direct,
                staging.logical_page_limit(),
            )
            .unwrap();
        builder
            .push(
                &mut staging,
                RangeRecord {
                    from: Ipv4Key(30),
                    to: Ipv4Key(40),
                    value: 1,
                },
            )
            .unwrap();
        let built = builder.finish(&mut staging).unwrap();
        let staged = staging.finish(built).unwrap();
        let mut assignments = [RangeTreePhysicalAssignment::empty(); 1];
        let mut payload_slots = [RangeTreePayloadReservationSlot::empty(); 1];
        let mut range_terminal = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        let materialized = bound
            .stage_range_payload(
                &scope,
                &staging,
                staged,
                &mut RangeTreePayloadScratch {
                    assignments: &mut assignments,
                    slots: &mut payload_slots,
                    terminal_pages: &mut range_terminal,
                },
            )
            .unwrap();
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
            &range_terminal,
            &mut seed,
            &mut first,
            &mut second,
            &mut ownership_scratch,
            1,
            1,
            |_, _| Ok(()),
        )
        .unwrap();
        let mut blob_pages = [];
        let mut path = [];
        let mut replacements = [];
        let mut releases = [];
        let mut roles = [];
        let stage = stage_range_root_retirement(
            &mut bound,
            &pool,
            &scope,
            &mut proof,
            RangeRootRetirementStageScratch {
                blob_pages: &mut blob_pages,
                upsert_path: &mut path,
                replacements: &mut replacements,
                releases: &mut releases,
                roles: &mut roles,
            },
        )
        .unwrap();
        assert_eq!(stage.retirement_result().root, 0);
        assert_eq!(stage.retirement_result().batch_count, 0);
        assert_eq!(stage.terminal_page_count(), 0);
        stage.verify(&bound, &pool, &scope, &mut proof).unwrap();

        let requirements = bound.finalization_scratch_requirements().unwrap();
        let mut release = vec![0; requirements.release_pages];
        let mut insert: Vec<_> = (0..requirements.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut cache = vec![FreeBitmapFinalizationCachedPage::empty(); requirements.cached_pages];
        let mut stack = vec![usize::MAX; requirements.index_stack];
        let mut cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            requirements.cleanup_nodes
        ];
        let mut cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            requirements.cleanup_path
        ];
        let mut cleanup_targets = vec![usize::MAX; requirements.cleanup_targets];
        let (finalized, allocations) = count_thread_allocations(|| {
            bound.finalize_range_root_retirement(
                &stage,
                &mut proof,
                FreeBitmapFinalizationScratch {
                    release_pages: &mut release,
                    insert_pages: &mut insert,
                    cached_pages: &mut cache,
                    index_stack: &mut stack,
                    cleanup_nodes: &mut cleanup_nodes,
                    cleanup_path: &mut cleanup_path,
                    cleanup_targets: &mut cleanup_targets,
                },
            )
        });
        assert_eq!(allocations, 0);
        let finalized = finalized.unwrap();
        assert_eq!(finalized.output.range_terminal_page_count(), 1);
        assert_eq!(finalized.output.retirement_terminal_page_count(), 0);
        let mut bitmap_terminal = vec![
            PrivatePageCoordinatorTerminalPage::empty();
            finalized.output.bitmap_terminal_page_count()
        ];
        let mut exported_range_terminal = vec![
            PrivatePageCoordinatorTerminalPage::empty();
            finalized.output.range_terminal_page_count()
        ];
        let mut retirement_terminal: Vec<PrivatePageCoordinatorTerminalPage> = Vec::new();
        let (export, allocations) = count_thread_allocations(|| {
            finalized
                .output
                .prepare_range_bitmap_retirement_terminal_export(
                    finalized.successor,
                    &stage,
                    &mut proof,
                    &mut bitmap_terminal,
                    &mut exported_range_terminal,
                    &mut retirement_terminal,
                )
        });
        assert_eq!(allocations, 0);
        let export = match export {
            Ok(export) => export,
            Err((_output, _successor, _bitmap, _range, _retirement, error)) => {
                panic!("legal empty triple terminal export failed: {error:?}")
            }
        };
        assert_eq!(export.range_pages(), range_terminal);
        assert!(export.retirement_pages().is_empty());
        assert_eq!(export.retirement(), stage.retirement_result());
        let mut combined = vec![
            PrivatePageCoordinatorTerminalPage::empty();
            export.bitmap_pages().len()
                + export.range_pages().len()
                + export.retirement_pages().len()
        ];
        let produced = match export.merge_terminal_journals(&mut combined) {
            Ok(produced) => produced,
            Err((_export, _combined, error)) => {
                panic!("legal empty three-owner merge failed: {error:?}")
            }
        };
        assert!(produced
            .pages()
            .iter()
            .all(|page| page.owner == PrivatePageOwner::Range));
        assert_eq!(
            produced.bitmap_root_provenance(),
            crate::retirement_writer::ProducedBitmapRootProvenance::SelectedUnchanged(2)
        );
        assert_eq!(produced.range_target(), Some(materialized));
        pool.require_abort();
        stage.discard_after_abort(proof);
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());
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
    fn seeds_proven_retirement_replacements_before_first_preview() {
        let (bytes, selected) = ownership_image();
        let source = SlicePageSource::new(&bytes, selected.page_count);
        let (materialized, range_pages) = materialized_range_page(5);
        let initial = [CommittedPageReplacement {
            pgno: 2,
            origin: CommittedPageOrigin::RetirementTree,
        }];
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

        let proof =
            prepare_range_root_transaction_proof_with_initial_replacements::<Ipv4Key, _, ()>(
                &source,
                selected,
                materialized,
                &range_pages,
                &mut seed,
                &mut first,
                &mut second,
                &mut ownership_scratch,
                &initial,
                4,
                2,
                |current, additions| {
                    calls += 1;
                    assert!(matches!(current.len(), 5 | 6));
                    additions.add(6).map(|_| ()).map_err(|_| ())
                },
            )
            .unwrap();

        assert_eq!(calls, 2);
        assert_eq!(collect(&mut *proof.seed), vec![2, 3, 4, 8, 11]);
        let protected = match proof.candidate {
            PageNumberIndexFixedPointCandidate::First => &mut *proof.first,
            PageNumberIndexFixedPointCandidate::Second => &mut *proof.second,
        };
        assert_eq!(collect(protected), vec![2, 3, 4, 6, 8, 11]);
        proof.discard_after_abort();
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());
    }

    fn assert_ordinary_replacement_proof_converges(
        mut bytes: Vec<u8>,
        mut selected: MetaV4,
        free_pages: &[u32],
        expected_seed: &[u32],
        expected_protected: &[u32],
    ) {
        selected.free_bitmap_root = 2;
        free_bitmap_leaf(page_mut(&mut bytes, 2), selected.txn_id, free_pages);
        let source = SlicePageSource::new(&bytes, selected.page_count);
        let mut storage = RangeRootStageBitmapStorage::new();
        let fence = RetirementReclaimFence::from_stable_reader_table(
            &RANGE_REPLACEMENT_BARRIER,
            1,
            Some(selected.txn_id),
        );
        let plan = FreeBitmapReservationPlanner::new(
            &source,
            selected.txn_id,
            selected.page_count,
            selected.free_bitmap_root,
            3,
            storage.buffers(),
        )
        .unwrap()
        .plan_under_reclamation(fence.into_no_reclamation())
        .unwrap();
        let required = plan.required_private_pages();
        assert!(required <= 24);
        let mut pool_slots = [const { PrivatePagePoolSlot::empty() }; 24];
        let pool = PrivatePagePool::new_vacant(
            &mut pool_slots[..required],
            selected.page_count,
            selected.page_count,
            selected.txn_id + 1,
        )
        .unwrap();
        let scope = pool.reserve_scope(required).unwrap();
        let mut bound = plan.bind(&pool, &scope).unwrap();

        let mut logical_pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging = RangeTreeStaging::<Ipv4Key>::new(
            &mut logical_pages,
            selected.txn_id + 1,
            ValueKind::Direct,
        )
        .unwrap();
        let mut range_workspace = RangeTreeBuildWorkspace::new();
        let mut builder = range_workspace
            .begin(
                selected.txn_id + 1,
                ValueKind::Direct,
                staging.logical_page_limit(),
            )
            .unwrap();
        builder
            .push(
                &mut staging,
                RangeRecord {
                    from: Ipv4Key(30),
                    to: Ipv4Key(40),
                    value: 1,
                },
            )
            .unwrap();
        let built = builder.finish(&mut staging).unwrap();
        let staged = staging.finish(built).unwrap();
        let mut assignments = [RangeTreePhysicalAssignment::empty(); 1];
        let mut payload_slots = [RangeTreePayloadReservationSlot::empty(); 1];
        let mut range_terminal = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        let materialized = bound
            .stage_range_payload(
                &scope,
                &staging,
                staged,
                &mut RangeTreePayloadScratch {
                    assignments: &mut assignments,
                    slots: &mut payload_slots,
                    terminal_pages: &mut range_terminal,
                },
            )
            .unwrap();
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
        let requirements = bound.finalization_scratch_requirements().unwrap();
        let mut initial_path = [RetirementPathFrame::new(); 8];
        let mut initial_replacements = [CommittedPageReplacement {
            pgno: 0,
            origin: CommittedPageOrigin::RetirementTree,
        }; 8];
        let mut initial_releases = [0_u32; 8];
        let mut initial_roles = [PageRoleIndexSlot::new(); 16];
        let mut preview_bitmap_replacements = [0_u32; 24];
        let mut preview_blob_pages = [0_u32; 1];
        let mut preview_path = [RetirementPathFrame::new(); 8];
        let mut preview_replacements = [CommittedPageReplacement {
            pgno: 0,
            origin: CommittedPageOrigin::RetirementTree,
        }; 8];
        let mut preview_releases = [0_u32; 8];
        let mut preview_roles = [PageRoleIndexSlot::new(); 16];
        let mut final_release = vec![0; requirements.release_pages];
        let mut final_insert: Vec<_> = (0..requirements.insert_pages)
            .map(|_| FreeBitmapInsertPage::empty())
            .collect();
        let mut final_cache =
            vec![FreeBitmapFinalizationCachedPage::empty(); requirements.cached_pages];
        let mut final_stack = vec![usize::MAX; requirements.index_stack];
        let mut final_cleanup_nodes = vec![
            crate::private_page_pool::PrivatePageSelectiveOverlayNode::empty();
            requirements.cleanup_nodes
        ];
        let mut final_cleanup_path = vec![
            crate::private_page_pool::PrivatePageSelectivePathEntry::empty();
            requirements.cleanup_path
        ];
        let mut final_cleanup_targets = vec![usize::MAX; requirements.cleanup_targets];
        let before = pool.test_mutation_snapshot();

        let (proof, allocations) = count_thread_allocations(|| {
            prepare_range_root_replacement_proof::<Ipv4Key, _>(
                &mut bound,
                &pool,
                &scope,
                selected,
                materialized,
                &range_terminal,
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
                    final_release_pages: &mut final_release,
                    final_insert_pages: &mut final_insert,
                    final_cached_pages: &mut final_cache,
                    final_index_stack: &mut final_stack,
                    final_cleanup_nodes: &mut final_cleanup_nodes,
                    final_cleanup_path: &mut final_cleanup_path,
                    final_cleanup_targets: &mut final_cleanup_targets,
                },
            )
        });
        assert_eq!(allocations, 0);
        let mut proof = proof.unwrap();
        assert_eq!(pool.test_mutation_snapshot(), before);
        proof.verify().unwrap();
        assert_eq!(collect(&mut *proof.seed), expected_seed);
        let protected = match proof.candidate {
            PageNumberIndexFixedPointCandidate::First => &mut *proof.first,
            PageNumberIndexFixedPointCandidate::Second => &mut *proof.second,
        };
        assert_eq!(collect(protected), expected_protected);
        proof.discard_after_abort();
        assert!(seed.is_empty_and_clean());
        assert!(first.is_empty_and_clean());
        assert!(second.is_empty_and_clean());
    }

    #[test]
    fn ordinary_replacement_proof_converges_empty_retirement_tree() {
        let (bytes, selected) = ownership_image();
        assert_ordinary_replacement_proof_converges(
            bytes,
            selected,
            &[5, 6, 7, 9, 10],
            &[3, 4, 8, 11],
            &[2, 3, 4, 8, 11],
        );
    }

    #[test]
    fn ordinary_replacement_proof_converges_existing_retirement_tree() {
        let (mut bytes, mut selected) = ownership_image_with_page_count(16);
        selected.retirement_root = 5;
        selected.retirement_batch_count = 1;
        retirement_leaf(
            page_mut(&mut bytes, 5),
            selected.txn_id,
            &[RetirementBatch {
                retired_by_txn: 2,
                page_count: 1,
                page_list_blob_root: 6,
            }],
        );
        retirement_blob(page_mut(&mut bytes, 6), selected.txn_id, &[10]);
        assert_ordinary_replacement_proof_converges(
            bytes,
            selected,
            &[7, 9, 12, 13, 14, 15],
            &[3, 4, 5, 8, 11],
            &[2, 3, 4, 5, 8, 11],
        );
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
    fn binds_selected_retirement_identity() {
        let mut selected = selected_meta(12, 0, 0);
        selected.retirement_root = 6;
        selected.retirement_batch_count = 1;
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
        let (state, protected) = proof.retirement_inputs().unwrap();
        assert_eq!(state.selected_txn, selected.txn_id);
        assert_eq!(state.page_count, selected.page_count);
        assert_eq!(state.root, selected.retirement_root);
        assert_eq!(state.batch_count, selected.retirement_batch_count);
        assert_eq!(protected.len(), 0);

        proof.selected.retirement_root = 7;
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
    fn rejects_invalid_selected_retirement_identity() {
        let selected = selected_meta(12, 0, 0);
        for invalid in [
            {
                let mut value = selected;
                value.retirement_batch_count = 1;
                value
            },
            {
                let mut value = selected;
                value.retirement_root = 2;
                value
            },
            {
                let mut value = selected;
                value.retirement_root = value.page_count as u32;
                value.retirement_batch_count = 1;
                value
            },
            {
                let mut value = selected;
                value.retirement_root = 2;
                value.retirement_batch_count = value.txn_id;
                value
            },
        ] {
            assert!(matches!(
                range_root_transaction_identity_from_meta::<()>(invalid),
                Err(RangeRootTransactionProofError::SelectedIdentity)
            ));
        }
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
