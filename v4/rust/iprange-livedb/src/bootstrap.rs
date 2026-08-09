//! Constant-time v4 meta classification and selection.

use crate::contract::{
    u16_source, u32_source, AddressFamily, MetaV4, ValueKind, MAX_METADATA_UNCOMPRESSED,
    MAX_PAGE_COUNT, META_CRC_OFFSET, META_MAGIC, META_SIZE, PAGE_SHIFT, PAGE_SIZE,
};
use crate::crc32c;
use crate::mapping::ByteSource;

mod recovery_meta;

pub(crate) use recovery_meta::{classify_recovery_meta, RecoveryMetaState};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OpenMode {
    ImmutableReader,
    LiveReader,
    Writer,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MetaSelection {
    ProvenCurrent,
    SoleMeta0,
    SoleMeta1,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MetaProblem {
    Magic,
    FixedValue,
    Reserved,
    Tag,
    DatabaseId,
    Checksum,
    CommitNonce,
    Transaction,
    PageCount,
    PhysicalLength,
    RootBounds,
    KindInvariant,
    CountInvariant,
    MetadataInvariant,
    RetirementInvariant,
    AllocatorReserve,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BootstrapError {
    FileTooShort,
    FileUnaligned,
    HostAddressability,
    StaticIdentityMismatch,
    NoBootstrapMeta {
        meta0: MetaProblem,
        meta1: MetaProblem,
    },
    TransactionGap,
    PhysicalParity,
    EqualTransactionDisagreement,
    CurrentGenerationUnprovable,
    ImmutableLengthMismatch,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Bootstrap {
    pub meta: MetaV4,
    pub selection: MetaSelection,
    pub selected_meta_page: u8,
    pub committed_bytes: u64,
    pub physical_bytes: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum CommitAttemptResolution {
    Committed,
    NotCommitted,
    SupersededUnknown,
}

#[derive(Clone, Copy)]
struct IdentityReadable {
    meta: MetaV4,
}

#[cfg(test)]
pub fn open(bytes: &[u8], mode: OpenMode) -> Result<Bootstrap, BootstrapError> {
    if bytes.len() < 2 * PAGE_SIZE {
        return Err(BootstrapError::FileTooShort);
    }
    if bytes.len() % PAGE_SIZE != 0 {
        return Err(BootstrapError::FileUnaligned);
    }

    let physical_bytes =
        u64::try_from(bytes.len()).map_err(|_| BootstrapError::HostAddressability)?;
    open_meta_pages(
        &bytes[..PAGE_SIZE],
        &bytes[PAGE_SIZE..2 * PAGE_SIZE],
        physical_bytes,
        mode,
    )
}

pub(crate) fn open_meta_pages<S: ByteSource>(
    page0: S,
    page1: S,
    physical_bytes: u64,
    mode: OpenMode,
) -> Result<Bootstrap, BootstrapError> {
    require_geometry(physical_bytes)?;
    let identities = [identity_readable(page0), identity_readable(page1)];
    require_same_identity(identities)?;
    let candidates = identities
        .map(|identity| identity.and_then(|identity| bootstrap_valid(identity, physical_bytes)));
    let (meta, selection, selected_meta_page) =
        select_candidates(page0, page1, candidates[0], candidates[1])?;
    finish_open(meta, selection, selected_meta_page, physical_bytes, mode)
}

pub(crate) fn database_id_from_meta_pages<S: ByteSource>(
    page0: S,
    page1: S,
) -> Result<[u8; 16], BootstrapError> {
    let identities = [identity_readable(page0), identity_readable(page1)];
    require_same_identity(identities)?;
    match identities {
        [Ok(identity), _] | [_, Ok(identity)] => Ok(identity.meta.database_id),
        [Err(meta0), Err(meta1)] => Err(BootstrapError::NoBootstrapMeta { meta0, meta1 }),
    }
}

pub(crate) fn resolve_commit_attempt<S: ByteSource>(
    page0: S,
    page1: S,
    physical_bytes: u64,
    database_id: [u8; 16],
    transaction_id: u64,
    commit_nonce: [u8; 16],
) -> Result<CommitAttemptResolution, BootstrapError> {
    let selected = open_meta_pages(page0, page1, physical_bytes, OpenMode::Writer)?;
    if selected.meta.database_id != database_id {
        return Err(BootstrapError::StaticIdentityMismatch);
    }

    let identities = [identity_readable(page0), identity_readable(page1)];
    let candidates = identities
        .map(|identity| identity.and_then(|identity| bootstrap_valid(identity, physical_bytes)));
    let [Ok(meta0), Ok(meta1)] = candidates else {
        return Err(BootstrapError::CurrentGenerationUnprovable);
    };
    if [meta0, meta1]
        .iter()
        .any(|meta| meta.txn_id == transaction_id && meta.commit_nonce == commit_nonce)
    {
        return Ok(CommitAttemptResolution::Committed);
    }
    if [meta0, meta1]
        .iter()
        .any(|meta| meta.txn_id == transaction_id && meta.commit_nonce != commit_nonce)
        || selected.meta.txn_id < transaction_id
    {
        return Ok(CommitAttemptResolution::NotCommitted);
    }
    if selected.meta.txn_id > transaction_id {
        return Ok(CommitAttemptResolution::SupersededUnknown);
    }
    Err(BootstrapError::CurrentGenerationUnprovable)
}

pub(crate) fn require_geometry(physical_bytes: u64) -> Result<(), BootstrapError> {
    if physical_bytes < (2 * PAGE_SIZE) as u64 {
        return Err(BootstrapError::FileTooShort);
    }
    if physical_bytes % PAGE_SIZE as u64 != 0 {
        return Err(BootstrapError::FileUnaligned);
    }
    Ok(())
}

pub(crate) fn geometry_valid(physical_bytes: u64) -> bool {
    require_geometry(physical_bytes).is_ok()
}

fn require_same_identity(
    identities: [Result<IdentityReadable, MetaProblem>; 2],
) -> Result<(), BootstrapError> {
    if let [Ok(left), Ok(right)] = identities {
        if !left.meta.static_identity_eq(&right.meta) {
            return Err(BootstrapError::StaticIdentityMismatch);
        }
    }
    Ok(())
}

fn select_candidates<S: ByteSource>(
    page0: S,
    page1: S,
    candidate0: Result<MetaV4, MetaProblem>,
    candidate1: Result<MetaV4, MetaProblem>,
) -> Result<(MetaV4, MetaSelection, u8), BootstrapError> {
    match (candidate0, candidate1) {
        (Err(meta0), Err(meta1)) => Err(BootstrapError::NoBootstrapMeta { meta0, meta1 }),
        (Ok(meta), Err(_)) => Ok((meta, MetaSelection::SoleMeta0, 0)),
        (Err(_), Ok(meta)) => Ok((meta, MetaSelection::SoleMeta1, 1)),
        (Ok(meta0), Ok(meta1)) => select_pair(page0, page1, meta0, meta1),
    }
}

fn finish_open(
    meta: MetaV4,
    selection: MetaSelection,
    selected_meta_page: u8,
    physical_bytes: u64,
    mode: OpenMode,
) -> Result<Bootstrap, BootstrapError> {
    if mode != OpenMode::ImmutableReader && selection != MetaSelection::ProvenCurrent {
        return Err(BootstrapError::CurrentGenerationUnprovable);
    }
    let committed_bytes = meta
        .page_count
        .checked_mul(PAGE_SIZE as u64)
        .ok_or(BootstrapError::HostAddressability)?;
    if mode == OpenMode::ImmutableReader && committed_bytes != physical_bytes {
        return Err(BootstrapError::ImmutableLengthMismatch);
    }

    Ok(Bootstrap {
        meta,
        selection,
        selected_meta_page,
        committed_bytes,
        physical_bytes,
    })
}

fn identity_readable<S: ByteSource>(page: S) -> Result<IdentityReadable, MetaProblem> {
    require_identity_header(page)?;
    require_identity_body(page)?;
    let meta = MetaV4::decode_unchecked(page).ok_or(MetaProblem::Tag)?;
    if meta.database_id == [0; 16] {
        return Err(MetaProblem::DatabaseId);
    }
    Ok(IdentityReadable { meta })
}

fn require_identity_header<S: ByteSource>(page: S) -> Result<(), MetaProblem> {
    if page.len() != PAGE_SIZE || !page.equals(0, &META_MAGIC) {
        return Err(MetaProblem::Magic);
    }
    if u16_source(page, 8) != Some(META_SIZE) || page.byte(10) != Some(PAGE_SHIFT) {
        return Err(MetaProblem::FixedValue);
    }
    if page.byte(11).and_then(AddressFamily::from_wire).is_none()
        || page.byte(12).and_then(ValueKind::from_wire).is_none()
    {
        return Err(MetaProblem::FixedValue);
    }
    Ok(())
}

fn require_identity_body<S: ByteSource>(page: S) -> Result<(), MetaProblem> {
    if !page.all_zero(13, 3) || !page.all_zero(200, 52) || !page.all_zero(256, PAGE_SIZE - 256) {
        return Err(MetaProblem::Reserved);
    }
    let stored_crc = u32_source(page, META_CRC_OFFSET).ok_or(MetaProblem::FixedValue)?;
    if crc32c::crc32c_source_with_zeroed(page, META_CRC_OFFSET, 4) != Some(stored_crc) {
        return Err(MetaProblem::Checksum);
    }
    Ok(())
}

fn bootstrap_valid(identity: IdentityReadable, physical_bytes: u64) -> Result<MetaV4, MetaProblem> {
    let meta = identity.meta;
    validate_generation(&meta, physical_bytes)?;
    validate_roots(&meta)?;
    validate_range_count(&meta)?;
    validate_retirement_count(&meta)?;
    validate_metadata(&meta)?;
    match meta.value_kind {
        ValueKind::Direct => validate_direct(&meta)?,
        ValueKind::Membership => validate_membership(&meta)?,
    }
    Ok(meta)
}

fn validate_generation(meta: &MetaV4, physical_bytes: u64) -> Result<(), MetaProblem> {
    validate_commit_identity(meta)?;
    validate_declared_page_count(meta)?;
    let committed_bytes = meta
        .page_count
        .checked_mul(PAGE_SIZE as u64)
        .ok_or(MetaProblem::PageCount)?;
    if physical_bytes < committed_bytes {
        return Err(MetaProblem::PhysicalLength);
    }
    Ok(())
}

fn validate_commit_identity(meta: &MetaV4) -> Result<(), MetaProblem> {
    if meta.txn_id == 0 {
        return Err(MetaProblem::Transaction);
    }
    if meta.commit_nonce == [0; 16] {
        return Err(MetaProblem::CommitNonce);
    }
    Ok(())
}

fn validate_declared_page_count(meta: &MetaV4) -> Result<(), MetaProblem> {
    if !(2..=MAX_PAGE_COUNT).contains(&meta.page_count) {
        return Err(MetaProblem::PageCount);
    }
    Ok(())
}

fn validate_roots(meta: &MetaV4) -> Result<(), MetaProblem> {
    for root in roots(meta) {
        if root != 0 && (root < 2 || u64::from(root) >= meta.page_count) {
            return Err(MetaProblem::RootBounds);
        }
    }
    validate_allocator_reserve(meta)
}

fn validate_range_count(meta: &MetaV4) -> Result<(), MetaProblem> {
    if (meta.range_record_count == 0) != (meta.range_root == 0) {
        return Err(MetaProblem::CountInvariant);
    }
    let leaf_capacity = match meta.address_family {
        AddressFamily::Ipv4 => ((PAGE_SIZE - 32) / (12 + 2)) as u64,
        AddressFamily::Ipv6 => ((PAGE_SIZE - 32) / (36 + 2)) as u64,
    };
    let maximum_range_records = (meta.page_count - 2)
        .checked_mul(leaf_capacity)
        .ok_or(MetaProblem::CountInvariant)?;
    if meta.range_record_count > maximum_range_records {
        return Err(MetaProblem::CountInvariant);
    }
    Ok(())
}

fn validate_retirement_count(meta: &MetaV4) -> Result<(), MetaProblem> {
    let maximum_retired_extents = (meta.page_count - 2)
        .checked_mul(((PAGE_SIZE - 32) / (16 + 2)) as u64)
        .ok_or(MetaProblem::RetirementInvariant)?;
    if meta.retired_extent_count > maximum_retired_extents
        || (meta.retired_extent_count == 0) != (meta.retirement_root == 0)
    {
        return Err(MetaProblem::RetirementInvariant);
    }
    Ok(())
}

fn validate_metadata(meta: &MetaV4) -> Result<(), MetaProblem> {
    if meta.metadata_root == 0 {
        if meta.metadata_uncompressed_len != 0 || meta.metadata_compressed_len != 0 {
            return Err(MetaProblem::MetadataInvariant);
        }
        return Ok(());
    }
    if !metadata_lengths_valid(meta)? {
        return Err(MetaProblem::MetadataInvariant);
    }
    Ok(())
}

fn metadata_lengths_valid(meta: &MetaV4) -> Result<bool, MetaProblem> {
    let blocks = core::cmp::max(
        1,
        meta.metadata_uncompressed_len
            .checked_add(65_534)
            .ok_or(MetaProblem::MetadataInvariant)?
            / 65_535,
    );
    let maximum_zlib_bytes = meta
        .metadata_uncompressed_len
        .checked_add(
            5u64.checked_mul(blocks)
                .ok_or(MetaProblem::MetadataInvariant)?,
        )
        .and_then(|value| value.checked_add(6))
        .ok_or(MetaProblem::MetadataInvariant)?;
    let maximum_compressed_bytes = (meta.page_count - 2)
        .checked_mul(4048)
        .ok_or(MetaProblem::MetadataInvariant)?;
    Ok(meta.metadata_compressed_len != 0
        && meta.metadata_compressed_len <= maximum_compressed_bytes
        && meta.metadata_compressed_len <= maximum_zlib_bytes
        && meta.metadata_uncompressed_len <= MAX_METADATA_UNCOMPRESSED)
}

fn validate_allocator_reserve(meta: &MetaV4) -> Result<(), MetaProblem> {
    let roots = roots(meta);
    for (index, page_number) in meta.allocator_reserve.iter().copied().enumerate() {
        if page_number == 0 {
            continue;
        }
        if page_number < 2
            || u64::from(page_number) >= meta.page_count
            || roots.contains(&page_number)
            || meta.allocator_reserve[..index].contains(&page_number)
        {
            return Err(MetaProblem::AllocatorReserve);
        }
    }
    Ok(())
}

fn validate_direct(meta: &MetaV4) -> Result<(), MetaProblem> {
    let fields = (
        meta.active_feed_count,
        meta.feed_index_limit,
        meta.membership_entry_count,
        meta.membership_id_limit,
        meta.catalog_name_root,
        meta.catalog_index_root,
        meta.feed_used_root,
        meta.membership_id_root,
        meta.membership_hash_root,
        meta.membership_used_root,
    );
    if fields != (0, 0, 0, 0, 0, 0, 0, 0, 0, 0) {
        return Err(MetaProblem::KindInvariant);
    }
    Ok(())
}

fn validate_membership(meta: &MetaV4) -> Result<(), MetaProblem> {
    validate_membership_counts(meta)?;
    validate_catalog_roots(meta)?;
    validate_membership_roots(meta)
}

fn validate_membership_counts(meta: &MetaV4) -> Result<(), MetaProblem> {
    validate_membership_limits(meta)?;
    validate_membership_presence(meta)
}

fn validate_membership_limits(meta: &MetaV4) -> Result<(), MetaProblem> {
    if meta.feed_index_limit > MAX_PAGE_COUNT
        || meta.active_feed_count > meta.feed_index_limit
        || meta.membership_entry_count > u64::from(u32::MAX)
        || !(1..=MAX_PAGE_COUNT).contains(&meta.membership_id_limit)
        || meta.membership_entry_count >= meta.membership_id_limit
        || meta.membership_entry_count > meta.range_record_count
    {
        return Err(MetaProblem::CountInvariant);
    }
    Ok(())
}

fn validate_membership_presence(meta: &MetaV4) -> Result<(), MetaProblem> {
    if meta.active_feed_count == 0
        && (meta.membership_entry_count != 0 || meta.range_record_count != 0)
    {
        return Err(MetaProblem::CountInvariant);
    }
    if meta.membership_entry_count == 0 && meta.range_record_count != 0 {
        return Err(MetaProblem::CountInvariant);
    }
    Ok(())
}

fn validate_catalog_roots(meta: &MetaV4) -> Result<(), MetaProblem> {
    if meta.active_feed_count == 0 {
        if meta.catalog_name_root != 0 || meta.catalog_index_root != 0 || meta.feed_used_root != 0 {
            return Err(MetaProblem::KindInvariant);
        }
    } else if meta.catalog_name_root == 0
        || meta.catalog_index_root == 0
        || meta.feed_used_root == 0
    {
        return Err(MetaProblem::KindInvariant);
    }
    Ok(())
}

fn validate_membership_roots(meta: &MetaV4) -> Result<(), MetaProblem> {
    if meta.membership_entry_count == 0 {
        if meta.membership_id_root != 0
            || meta.membership_hash_root != 0
            || meta.membership_used_root != 0
            || meta.membership_id_limit != 1
        {
            return Err(MetaProblem::KindInvariant);
        }
    } else if meta.membership_id_root == 0
        || meta.membership_hash_root == 0
        || meta.membership_used_root == 0
    {
        return Err(MetaProblem::KindInvariant);
    }
    Ok(())
}

fn roots(meta: &MetaV4) -> [u32; 10] {
    [
        meta.range_root,
        meta.catalog_name_root,
        meta.catalog_index_root,
        meta.feed_used_root,
        meta.membership_id_root,
        meta.membership_hash_root,
        meta.membership_used_root,
        meta.metadata_root,
        meta.free_bitmap_root,
        meta.retirement_root,
    ]
}

fn select_pair<S: ByteSource>(
    page0: S,
    page1: S,
    meta0: MetaV4,
    meta1: MetaV4,
) -> Result<(MetaV4, MetaSelection, u8), BootstrapError> {
    if meta0.txn_id == meta1.txn_id {
        if !page0.same(page1, 0, META_SIZE as usize) {
            return Err(BootstrapError::EqualTransactionDisagreement);
        }
        let selected = (meta0.txn_id & 1) as u8;
        return Ok((meta0, MetaSelection::ProvenCurrent, selected));
    }

    let (lower, higher, higher_page) = if meta0.txn_id < meta1.txn_id {
        (meta0, meta1, 1u8)
    } else {
        (meta1, meta0, 0u8)
    };
    if higher.txn_id - lower.txn_id != 1 {
        return Err(BootstrapError::TransactionGap);
    }
    if (higher.txn_id & 1) as u8 != higher_page {
        return Err(BootstrapError::PhysicalParity);
    }
    Ok((higher, MetaSelection::ProvenCurrent, higher_page))
}

#[cfg(test)]
#[path = "bootstrap_tests.rs"]
pub(crate) mod tests;
