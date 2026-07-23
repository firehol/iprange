//! Constant-time v4 meta classification and selection.

use crate::contract::{
    u16_le, u32_le, AddressFamily, MetaV4, ValueKind, MAX_METADATA_UNCOMPRESSED, MAX_PAGE_COUNT,
    META_CRC_OFFSET, META_MAGIC, META_SIZE, PAGE_SHIFT, PAGE_SIZE,
};
use crate::crc32c;

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

#[derive(Clone, Copy)]
struct IdentityReadable {
    meta: MetaV4,
}

pub fn open(bytes: &[u8], mode: OpenMode) -> Result<Bootstrap, BootstrapError> {
    if bytes.len() < 2 * PAGE_SIZE {
        return Err(BootstrapError::FileTooShort);
    }
    if bytes.len() % PAGE_SIZE != 0 {
        return Err(BootstrapError::FileUnaligned);
    }

    let physical_bytes =
        u64::try_from(bytes.len()).map_err(|_| BootstrapError::HostAddressability)?;
    let page0: &[u8; PAGE_SIZE] = bytes[..PAGE_SIZE].try_into().unwrap();
    let page1: &[u8; PAGE_SIZE] = bytes[PAGE_SIZE..2 * PAGE_SIZE].try_into().unwrap();
    open_meta_pages(page0, page1, physical_bytes, mode)
}

pub(crate) fn open_meta_pages(
    page0: &[u8; PAGE_SIZE],
    page1: &[u8; PAGE_SIZE],
    physical_bytes: u64,
    mode: OpenMode,
) -> Result<Bootstrap, BootstrapError> {
    if physical_bytes < (2 * PAGE_SIZE) as u64 {
        return Err(BootstrapError::FileTooShort);
    }
    if physical_bytes % PAGE_SIZE as u64 != 0 {
        return Err(BootstrapError::FileUnaligned);
    }
    usize::try_from(physical_bytes).map_err(|_| BootstrapError::HostAddressability)?;
    let identity0 = identity_readable(page0);
    let identity1 = identity_readable(page1);

    if let (Ok(left), Ok(right)) = (identity0, identity1) {
        if !left.meta.static_identity_eq(&right.meta) {
            return Err(BootstrapError::StaticIdentityMismatch);
        }
    }

    let candidate0 = identity0.and_then(|identity| bootstrap_valid(identity, physical_bytes));
    let candidate1 = identity1.and_then(|identity| bootstrap_valid(identity, physical_bytes));

    let (meta, selection, selected_meta_page) = match (candidate0, candidate1) {
        (Err(meta0), Err(meta1)) => return Err(BootstrapError::NoBootstrapMeta { meta0, meta1 }),
        (Ok(meta), Err(_)) => (meta, MetaSelection::SoleMeta0, 0),
        (Err(_), Ok(meta)) => (meta, MetaSelection::SoleMeta1, 1),
        (Ok(meta0), Ok(meta1)) => select_pair(page0, page1, meta0, meta1)?,
    };

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

fn identity_readable(page: &[u8; PAGE_SIZE]) -> Result<IdentityReadable, MetaProblem> {
    if page[0..8] != META_MAGIC {
        return Err(MetaProblem::Magic);
    }
    if u16_le(page, 8) != META_SIZE || page[10] != PAGE_SHIFT {
        return Err(MetaProblem::FixedValue);
    }
    if page[13..16].iter().any(|&byte| byte != 0)
        || page[184..252].iter().any(|&byte| byte != 0)
        || page[256..].iter().any(|&byte| byte != 0)
    {
        return Err(MetaProblem::Reserved);
    }
    let stored_crc = u32_le(page, META_CRC_OFFSET);
    if crc32c::crc32c_with_zeroed(page, META_CRC_OFFSET, 4) != Some(stored_crc) {
        return Err(MetaProblem::Checksum);
    }
    if AddressFamily::from_wire(page[11]).is_none() || ValueKind::from_wire(page[12]).is_none() {
        return Err(MetaProblem::FixedValue);
    }
    let meta = MetaV4::decode_unchecked(page).ok_or(MetaProblem::Tag)?;
    if meta.database_id == [0; 16] {
        return Err(MetaProblem::DatabaseId);
    }
    Ok(IdentityReadable { meta })
}

fn bootstrap_valid(identity: IdentityReadable, physical_bytes: u64) -> Result<MetaV4, MetaProblem> {
    let meta = identity.meta;
    if meta.txn_id == 0 {
        return Err(MetaProblem::Transaction);
    }
    if meta.commit_nonce == [0; 16] {
        return Err(MetaProblem::CommitNonce);
    }
    if !(2..=MAX_PAGE_COUNT).contains(&meta.page_count) {
        return Err(MetaProblem::PageCount);
    }
    let committed_bytes = meta
        .page_count
        .checked_mul(PAGE_SIZE as u64)
        .ok_or(MetaProblem::PageCount)?;
    if physical_bytes < committed_bytes {
        return Err(MetaProblem::PhysicalLength);
    }

    for root in roots(&meta) {
        if root != 0 && (root < 2 || u64::from(root) >= meta.page_count) {
            return Err(MetaProblem::RootBounds);
        }
    }
    if meta.range_record_count != 0 && meta.range_root == 0 {
        return Err(MetaProblem::CountInvariant);
    }
    let leaf_capacity = match meta.address_family {
        AddressFamily::Ipv4 => ((PAGE_SIZE - 32) / 12) as u64,
        AddressFamily::Ipv6 => ((PAGE_SIZE - 32) / 36) as u64,
    };
    let maximum_range_records = (meta.page_count - 2)
        .checked_mul(leaf_capacity)
        .ok_or(MetaProblem::CountInvariant)?;
    if meta.range_record_count > maximum_range_records {
        return Err(MetaProblem::CountInvariant);
    }
    if meta.retirement_batch_count > meta.txn_id - 1
        || (meta.retirement_batch_count == 0) != (meta.retirement_root == 0)
    {
        return Err(MetaProblem::RetirementInvariant);
    }
    if meta.metadata_root == 0 {
        if meta.metadata_uncompressed_len != 0 || meta.metadata_compressed_len != 0 {
            return Err(MetaProblem::MetadataInvariant);
        }
    } else {
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
        if meta.metadata_compressed_len == 0
            || meta.metadata_compressed_len > maximum_compressed_bytes
            || meta.metadata_compressed_len > maximum_zlib_bytes
            || meta.metadata_uncompressed_len > MAX_METADATA_UNCOMPRESSED
        {
            return Err(MetaProblem::MetadataInvariant);
        }
    }

    match meta.value_kind {
        ValueKind::Direct => validate_direct(&meta)?,
        ValueKind::Membership => validate_membership(&meta)?,
    }
    Ok(meta)
}

fn validate_direct(meta: &MetaV4) -> Result<(), MetaProblem> {
    if meta.active_feed_count != 0
        || meta.feed_index_limit != 0
        || meta.membership_entry_count != 0
        || meta.membership_id_limit != 0
        || meta.catalog_name_root != 0
        || meta.catalog_index_root != 0
        || meta.feed_used_root != 0
        || meta.membership_id_root != 0
        || meta.membership_hash_root != 0
        || meta.membership_used_root != 0
    {
        return Err(MetaProblem::KindInvariant);
    }
    Ok(())
}

fn validate_membership(meta: &MetaV4) -> Result<(), MetaProblem> {
    if meta.feed_index_limit > MAX_PAGE_COUNT
        || meta.active_feed_count > meta.feed_index_limit
        || meta.membership_entry_count > u64::from(u32::MAX)
        || !(1..=MAX_PAGE_COUNT).contains(&meta.membership_id_limit)
        || meta.membership_entry_count >= meta.membership_id_limit
        || meta.membership_entry_count > meta.range_record_count
    {
        return Err(MetaProblem::CountInvariant);
    }
    if meta.active_feed_count == 0
        && (meta.membership_entry_count != 0 || meta.range_record_count != 0)
    {
        return Err(MetaProblem::CountInvariant);
    }
    if meta.membership_entry_count == 0 && meta.range_record_count != 0 {
        return Err(MetaProblem::CountInvariant);
    }
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

fn select_pair(
    page0: &[u8; PAGE_SIZE],
    page1: &[u8; PAGE_SIZE],
    meta0: MetaV4,
    meta1: MetaV4,
) -> Result<(MetaV4, MetaSelection, u8), BootstrapError> {
    if meta0.txn_id == meta1.txn_id {
        if page0[..META_SIZE as usize] != page1[..META_SIZE as usize] {
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
pub(crate) mod tests {
    use super::*;
    use crate::contract::ValueTag;
    use std::{vec, vec::Vec};

    pub(crate) fn empty_direct_meta(txn_id: u64) -> MetaV4 {
        MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::new(b"").unwrap(),
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

    fn image(meta0: MetaV4, meta1: MetaV4) -> Vec<u8> {
        let mut bytes = vec![0u8; 2 * PAGE_SIZE];
        let (page0, page1) = bytes.split_at_mut(PAGE_SIZE);
        meta0.encode_into(page0.try_into().unwrap());
        meta1.encode_into(page1.try_into().unwrap());
        bytes
    }

    fn rewrite_crc(page: &mut [u8]) {
        page[META_CRC_OFFSET..META_CRC_OFFSET + 4].fill(0);
        let checksum = crc32c::crc32c_with_zeroed(page, META_CRC_OFFSET, 4).unwrap();
        page[META_CRC_OFFSET..META_CRC_OFFSET + 4].copy_from_slice(&checksum.to_le_bytes());
    }

    fn assert_identity_problem(offset: usize, value: u8, expected: MetaProblem) {
        let mut bytes = image(empty_direct_meta(1), empty_direct_meta(1));
        for base in [0, PAGE_SIZE] {
            bytes[base + offset] = value;
            rewrite_crc(&mut bytes[base..base + PAGE_SIZE]);
        }
        assert_eq!(
            open(&bytes, OpenMode::ImmutableReader),
            Err(BootstrapError::NoBootstrapMeta {
                meta0: expected,
                meta1: expected,
            })
        );
    }

    #[test]
    fn identical_creation_metas_are_proven_current() {
        let bytes = image(empty_direct_meta(1), empty_direct_meta(1));
        let opened = open(&bytes, OpenMode::Writer).unwrap();
        assert_eq!(opened.selection, MetaSelection::ProvenCurrent);
        assert_eq!(opened.selected_meta_page, 1);
        assert_eq!(opened.committed_bytes, 8192);
    }

    #[test]
    fn retained_meta_page_bootstrap_matches_slice_bootstrap() {
        let mut bytes = image(empty_direct_meta(1), empty_direct_meta(1));
        bytes.resize(3 * PAGE_SIZE, 0);
        let page0: &[u8; PAGE_SIZE] = bytes[..PAGE_SIZE].try_into().unwrap();
        let page1: &[u8; PAGE_SIZE] = bytes[PAGE_SIZE..2 * PAGE_SIZE].try_into().unwrap();
        assert_eq!(
            open_meta_pages(page0, page1, bytes.len() as u64, OpenMode::LiveReader),
            open(&bytes, OpenMode::LiveReader)
        );
        assert_eq!(
            open_meta_pages(page0, page1, 8191, OpenMode::LiveReader),
            Err(BootstrapError::FileTooShort)
        );
        assert_eq!(
            open_meta_pages(page0, page1, 8193, OpenMode::LiveReader),
            Err(BootstrapError::FileUnaligned)
        );
    }

    #[test]
    fn adjacent_transaction_uses_parity() {
        let mut old = empty_direct_meta(1);
        let mut new = old;
        new.txn_id = 2;
        new.commit_nonce = [3; 16];
        let bytes = image(new, old);
        let opened = open(&bytes, OpenMode::Writer).unwrap();
        assert_eq!(opened.meta.txn_id, 2);
        assert_eq!(opened.selected_meta_page, 0);

        old.txn_id = 2;
        old.commit_nonce = [4; 16];
        let swapped = image(empty_direct_meta(1), old);
        assert_eq!(
            open(&swapped, OpenMode::Writer),
            Err(BootstrapError::PhysicalParity)
        );
    }

    #[test]
    fn pair_gap_and_equal_disagreement_fail_closed() {
        let old = empty_direct_meta(1);
        let mut gap = old;
        gap.txn_id = 3;
        gap.commit_nonce = [3; 16];
        let bytes = image(old, gap);
        assert_eq!(
            open(&bytes, OpenMode::ImmutableReader),
            Err(BootstrapError::TransactionGap)
        );

        let mut disagree = old;
        disagree.commit_nonce = [4; 16];
        let bytes = image(old, disagree);
        assert_eq!(
            open(&bytes, OpenMode::ImmutableReader),
            Err(BootstrapError::EqualTransactionDisagreement)
        );
    }

    #[test]
    fn sole_meta_is_factual_but_not_mutable_authority() {
        let mut bytes = image(empty_direct_meta(1), empty_direct_meta(1));
        bytes[META_CRC_OFFSET] ^= 1;
        assert_eq!(
            open(&bytes, OpenMode::ImmutableReader).unwrap().selection,
            MetaSelection::SoleMeta1
        );
        assert_eq!(
            open(&bytes, OpenMode::Writer),
            Err(BootstrapError::CurrentGenerationUnprovable)
        );
    }

    #[test]
    fn identity_mismatch_wins_over_dynamic_invalidity() {
        let left = empty_direct_meta(1);
        let mut right = left;
        right.database_id = [9; 16];
        right.commit_nonce = [0; 16];
        let bytes = image(left, right);
        assert_eq!(
            open(&bytes, OpenMode::ImmutableReader),
            Err(BootstrapError::StaticIdentityMismatch)
        );
    }

    #[test]
    fn unaligned_and_immutable_tail_are_rejected() {
        let mut bytes = image(empty_direct_meta(1), empty_direct_meta(1));
        bytes.push(0);
        assert_eq!(
            open(&bytes, OpenMode::ImmutableReader),
            Err(BootstrapError::FileUnaligned)
        );

        let mut aligned_tail = image(empty_direct_meta(1), empty_direct_meta(1));
        aligned_tail.resize(3 * PAGE_SIZE, 0);
        assert_eq!(
            open(&aligned_tail, OpenMode::ImmutableReader),
            Err(BootstrapError::ImmutableLengthMismatch)
        );
        assert_eq!(
            open(&aligned_tail, OpenMode::LiveReader)
                .unwrap()
                .committed_bytes,
            2 * PAGE_SIZE as u64
        );
    }

    #[test]
    fn impossible_counts_and_lengths_are_not_bootstrap_valid() {
        let mut bad = empty_direct_meta(1);
        bad.range_record_count = 1;
        let bytes = image(bad, bad);
        assert!(matches!(
            open(&bytes, OpenMode::ImmutableReader),
            Err(BootstrapError::NoBootstrapMeta {
                meta0: MetaProblem::CountInvariant,
                meta1: MetaProblem::CountInvariant
            })
        ));

        let mut bad_metadata = empty_direct_meta(1);
        bad_metadata.page_count = 3;
        bad_metadata.metadata_root = 2;
        bad_metadata.metadata_compressed_len = 4049;
        let mut bytes = image(bad_metadata, bad_metadata);
        bytes.resize(3 * PAGE_SIZE, 0);
        assert!(matches!(
            open(&bytes, OpenMode::ImmutableReader),
            Err(BootstrapError::NoBootstrapMeta {
                meta0: MetaProblem::MetadataInvariant,
                meta1: MetaProblem::MetadataInvariant
            })
        ));
    }

    #[test]
    fn canonical_empty_membership_meta_is_valid() {
        let mut membership = empty_direct_meta(1);
        membership.value_kind = ValueKind::Membership;
        membership.membership_id_limit = 1;
        let bytes = image(membership, membership);
        assert_eq!(
            open(&bytes, OpenMode::Writer).unwrap().selection,
            MetaSelection::ProvenCurrent
        );
    }

    #[test]
    fn identity_field_matrix_fails_closed() {
        assert_identity_problem(0, b'X', MetaProblem::Magic);
        assert_identity_problem(8, 1, MetaProblem::FixedValue);
        assert_identity_problem(10, 11, MetaProblem::FixedValue);
        assert_identity_problem(11, 5, MetaProblem::FixedValue);
        assert_identity_problem(12, 3, MetaProblem::FixedValue);
        assert_identity_problem(13, 1, MetaProblem::Reserved);
        assert_identity_problem(184, 1, MetaProblem::Reserved);
        assert_identity_problem(256, 1, MetaProblem::Reserved);

        let mut malformed_tag = image(empty_direct_meta(1), empty_direct_meta(1));
        for base in [0, PAGE_SIZE] {
            malformed_tag[base + 16] = b'a';
            malformed_tag[base + 17] = 0;
            malformed_tag[base + 18] = b'x';
            rewrite_crc(&mut malformed_tag[base..base + PAGE_SIZE]);
        }
        assert_eq!(
            open(&malformed_tag, OpenMode::ImmutableReader),
            Err(BootstrapError::NoBootstrapMeta {
                meta0: MetaProblem::Tag,
                meta1: MetaProblem::Tag,
            })
        );

        let mut zero_database_id = empty_direct_meta(1);
        zero_database_id.database_id = [0; 16];
        assert!(matches!(
            open(
                &image(zero_database_id, zero_database_id),
                OpenMode::ImmutableReader
            ),
            Err(BootstrapError::NoBootstrapMeta {
                meta0: MetaProblem::DatabaseId,
                meta1: MetaProblem::DatabaseId,
            })
        ));

        let mut bad_crc = image(empty_direct_meta(1), empty_direct_meta(1));
        bad_crc[META_CRC_OFFSET] ^= 1;
        bad_crc[PAGE_SIZE + META_CRC_OFFSET] ^= 1;
        assert!(matches!(
            open(&bad_crc, OpenMode::ImmutableReader),
            Err(BootstrapError::NoBootstrapMeta {
                meta0: MetaProblem::Checksum,
                meta1: MetaProblem::Checksum,
            })
        ));
    }

    #[test]
    fn dynamic_bootstrap_field_matrix_fails_closed() {
        let cases = [
            {
                let mut meta = empty_direct_meta(1);
                meta.txn_id = 0;
                (meta, MetaProblem::Transaction)
            },
            {
                let mut meta = empty_direct_meta(1);
                meta.commit_nonce = [0; 16];
                (meta, MetaProblem::CommitNonce)
            },
            {
                let mut meta = empty_direct_meta(1);
                meta.page_count = 1;
                (meta, MetaProblem::PageCount)
            },
            {
                let mut meta = empty_direct_meta(1);
                meta.range_root = 1;
                (meta, MetaProblem::RootBounds)
            },
            {
                let mut meta = empty_direct_meta(1);
                meta.active_feed_count = 1;
                (meta, MetaProblem::KindInvariant)
            },
            {
                let mut meta = empty_direct_meta(1);
                meta.retirement_batch_count = 1;
                (meta, MetaProblem::RetirementInvariant)
            },
        ];
        for (meta, expected) in cases {
            assert_eq!(
                open(&image(meta, meta), OpenMode::ImmutableReader),
                Err(BootstrapError::NoBootstrapMeta {
                    meta0: expected,
                    meta1: expected,
                })
            );
        }

        let mut truncated = empty_direct_meta(1);
        truncated.page_count = 3;
        assert_eq!(
            open(&image(truncated, truncated), OpenMode::ImmutableReader),
            Err(BootstrapError::NoBootstrapMeta {
                meta0: MetaProblem::PhysicalLength,
                meta1: MetaProblem::PhysicalLength,
            })
        );
    }

    #[test]
    fn metadata_bootstrap_enforces_zlib_worst_case_bound() {
        let mut meta = empty_direct_meta(1);
        meta.page_count = 3;
        meta.metadata_root = 2;
        meta.metadata_uncompressed_len = 0;
        meta.metadata_compressed_len = 12;
        let mut bytes = image(meta, meta);
        bytes.resize(3 * PAGE_SIZE, 0);
        assert_eq!(
            open(&bytes, OpenMode::ImmutableReader),
            Err(BootstrapError::NoBootstrapMeta {
                meta0: MetaProblem::MetadataInvariant,
                meta1: MetaProblem::MetadataInvariant,
            })
        );

        meta.metadata_compressed_len = 11;
        let mut boundary = image(meta, meta);
        boundary.resize(3 * PAGE_SIZE, 0);
        assert!(open(&boundary, OpenMode::ImmutableReader).is_ok());
    }
}
