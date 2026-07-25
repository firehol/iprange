//! Identity, budget, and empty-metadata setup for immutable outputs.

use std::fs::File;

use crate::contract::{MetaV4, ValueKind, MAX_PAGE_COUNT};
use crate::error::{Error, Result};

use super::{OutputBudget, OutputSpec};

pub(super) fn require_new_output(
    file: &File,
    spec: OutputSpec,
    budget: OutputBudget,
) -> Result<()> {
    if file.metadata()?.len() != 0 {
        return Err(Error::InvalidArgument("immutable output file is not empty"));
    }
    require_identity(spec)?;
    require_limits(spec, budget)
}

fn require_identity(spec: OutputSpec) -> Result<()> {
    if spec.database_id == [0; 16] || spec.commit_nonce == [0; 16] || spec.transaction_id == 0 {
        return Err(Error::InvalidArgument(
            "immutable output identity is invalid",
        ));
    }
    Ok(())
}

fn require_limits(spec: OutputSpec, budget: OutputBudget) -> Result<()> {
    if budget.max_output_pages < 2 {
        return Err(Error::BudgetExceeded("immutable output pages"));
    }
    if spec.feed_index_limit > MAX_PAGE_COUNT
        || (spec.value_kind == ValueKind::Direct && spec.feed_index_limit != 0)
    {
        return Err(Error::InvalidArgument(
            "immutable output feed-index limit is invalid",
        ));
    }
    Ok(())
}

pub(super) fn empty_meta(spec: OutputSpec) -> MetaV4 {
    MetaV4 {
        address_family: spec.address_family,
        value_kind: spec.value_kind,
        value_tag: spec.value_tag,
        database_id: spec.database_id,
        txn_id: spec.transaction_id,
        commit_nonce: spec.commit_nonce,
        page_count: 2,
        range_record_count: 0,
        active_feed_count: 0,
        feed_index_limit: spec.feed_index_limit,
        membership_entry_count: 0,
        membership_id_limit: u64::from(spec.value_kind == ValueKind::Membership),
        metadata_uncompressed_len: 0,
        metadata_compressed_len: 0,
        retired_extent_count: 0,
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
        allocator_reserve: [0; 4],
    }
}
