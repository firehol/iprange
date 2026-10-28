//! Unordered single-feed construction inside one private output inode.

#[path = "unordered/workspace.rs"]
pub(crate) mod workspace;

use std::fs::File;

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::{AddressFamily, ValueKind, ValueTag, MAX_PAGE_COUNT};
use crate::error::{Error, Result};
use crate::feed::FeedName;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::range_mutation::{self, UnionInput};
use crate::range_store_cursor::Cursor;
use crate::source::{self, RangeSource};
use crate::workflow::AddressRange;

use super::{Builder, Finished, MembershipWords, OutputSpec};
use workspace::Workspace;

#[derive(Clone, Copy)]
pub(crate) struct BudgetInput {
    pub(crate) max_heap_bytes: u64,
    pub(crate) max_output_pages: u64,
    pub(crate) max_workspace_pages: u64,
    pub(crate) max_open_files: u32,
}

pub(crate) struct PreparedBudget {
    pub(crate) max_heap_bytes: u64,
    pub(crate) max_output_pages: u64,
    pub(crate) total_pages: u64,
}

pub(crate) struct Built {
    pub(crate) finished: Finished,
    pub(crate) input_record_count: u64,
    pub(crate) normalized_interval_count: u64,
    pub(crate) addresses: Cardinality129,
}

pub(crate) struct BuildFailure {
    pub(crate) file: File,
    pub(crate) cause: Error,
}

struct Normalized {
    root: u32,
    record_count: u64,
    pub(crate) input_record_count: u64,
}

pub(crate) struct OutputStats {
    pub(crate) input_record_count: u64,
    pub(crate) normalized_interval_count: u64,
    pub(crate) addresses: Cardinality129,
}

pub(crate) fn prepare_budget(input: BudgetInput) -> Result<PreparedBudget> {
    if input.max_output_pages < 2 {
        return Err(Error::BudgetExceeded("immutable feed output pages"));
    }
    if input.max_open_files < 3 {
        return Err(Error::BudgetExceeded("immutable feed open files"));
    }
    let total_pages = input
        .max_output_pages
        .checked_add(input.max_workspace_pages)
        .ok_or(Error::PageSpaceExhausted)?;
    if total_pages > MAX_PAGE_COUNT {
        return Err(Error::PageSpaceExhausted);
    }
    Ok(PreparedBudget {
        max_heap_bytes: input.max_heap_bytes,
        max_output_pages: input.max_output_pages,
        total_pages,
    })
}

#[allow(clippy::too_many_arguments)]
pub(crate) fn build<K, S, F>(
    file: File,
    value_tag: ValueTag,
    feed_name: FeedName,
    metadata_json: Option<&[u8]>,
    source: &mut S,
    budget: PreparedBudget,
    cancellation: &CancellationToken,
    keep: &mut F,
) -> std::result::Result<Built, BuildFailure>
where
    K: FeedKey,
    S: RangeSource<AddressRange<K>>,
    F: FnMut(&AddressRange<K>) -> Result<bool>,
{
    let spec = match output_spec(K::FAMILY, value_tag) {
        Ok(spec) => spec,
        Err(cause) => return Err(BuildFailure { file, cause }),
    };
    super::construction::build(file, spec, budget, true, |builder, workspace, remaining| {
        build_mapped::<K, S, F>(
            workspace.expect("requested workspace"),
            builder,
            spec,
            feed_name,
            metadata_json,
            source,
            remaining,
            cancellation,
            keep,
        )
    })
}

#[allow(clippy::too_many_arguments)]
fn build_mapped<K, S, F>(
    workspace: &mut Workspace,
    builder: &mut Builder,
    spec: OutputSpec,
    feed_name: FeedName,
    metadata_json: Option<&[u8]>,
    source: &mut S,
    max_heap_bytes: u64,
    cancellation: &CancellationToken,
    keep: &mut F,
) -> Result<OutputStats>
where
    K: FeedKey,
    S: RangeSource<AddressRange<K>>,
    F: FnMut(&AddressRange<K>) -> Result<bool>,
{
    let normalized = normalize::<K, S>(workspace, source, max_heap_bytes, cancellation)?;
    write_normalized::<K, F>(
        workspace,
        builder,
        spec,
        feed_name,
        metadata_json,
        max_heap_bytes,
        normalized,
        cancellation,
        keep,
    )
}

fn normalize<K, S>(
    workspace: &mut Workspace,
    source: &mut S,
    max_heap_bytes: u64,
    cancellation: &CancellationToken,
) -> Result<Normalized>
where
    K: FeedKey,
    S: RangeSource<AddressRange<K>>,
{
    let mut input_record_count = 0u64;
    let mut root = 0u32;
    let mut record_count = 0u64;
    let mut union = UnionInput::new(ValueKind::Membership, max_heap_bytes);
    source::drain(source, cancellation, &mut input_record_count, |range| {
        range_mutation::push_private_untracked(
            workspace,
            &mut root,
            &mut record_count,
            range.from,
            range.to,
            1,
            &mut union,
        )?;
        Ok(())
    })?;
    range_mutation::finish_input_untracked(workspace, &mut root, &mut record_count, &mut union)?;
    cancellation.check()?;
    Ok(Normalized {
        root,
        record_count,
        input_record_count,
    })
}

#[allow(clippy::too_many_arguments)]
fn write_normalized<K: FeedKey, F>(
    workspace: &mut Workspace,
    builder: &mut Builder,
    spec: OutputSpec,
    feed_name: FeedName,
    metadata_json: Option<&[u8]>,
    max_heap_bytes: u64,
    normalized: Normalized,
    cancellation: &CancellationToken,
    keep: &mut F,
) -> Result<OutputStats>
where
    F: FnMut(&AddressRange<K>) -> Result<bool>,
{
    builder.push_feed(feed_name, 0)?;
    let mut membership = None;
    let mut meta = crate::database_file::empty_meta(crate::database_file::EmptySpec {
        address_family: K::FAMILY,
        value_kind: ValueKind::Membership,
        structure_kind: crate::StructureKind::None,
        value_tag: spec.value_tag,
        database_id: spec.database_id,
        transaction_id: spec.transaction_id,
        commit_nonce: spec.commit_nonce,
        feed_index_limit: 1,
    });
    meta.page_count = workspace.page_count();
    meta.range_root = normalized.root;
    meta.range_record_count = normalized.record_count;
    let mut cursor = Cursor::<K>::new(workspace, &meta, false)?;
    let mut addresses = Cardinality129::ZERO;
    let mut output_ranges = 0u64;
    let mut visited = 0u64;
    while let Some(range) = cursor.next(workspace)? {
        if visited & 4095 == 4095 {
            cancellation.check()?;
        }
        visited += 1;
        if range.value != 1 {
            return Err(Error::Corrupt(
                "immutable feed workspace contains a non-coverage value",
            ));
        }
        if !keep(&AddressRange {
            from: range.from,
            to: range.to,
        })? {
            continue;
        }
        let value = match membership {
            Some(value) => value,
            None => {
                let value = builder.intern_membership_value(&OneFeed)?;
                membership = Some(value);
                value
            }
        };
        K::push(builder, range.from, range.to, value)?;
        addresses = addresses
            .checked_add(range.from.inclusive_cardinality(range.to)?)
            .map_err(|_| Error::ArithmeticOverflow("immutable feed addresses"))?;
        output_ranges += 1;
    }
    cancellation.check()?;
    if let Some(metadata) = metadata_json {
        builder.write_metadata_with_budget(metadata, max_heap_bytes)?;
    }
    Ok(OutputStats {
        input_record_count: normalized.input_record_count,
        normalized_interval_count: output_ranges,
        addresses,
    })
}

pub(crate) trait FeedKey: IpKey {
    fn push(builder: &mut Builder, from: Self, to: Self, membership: u32) -> Result<()>;
}

impl FeedKey for Ipv4Key {
    fn push(builder: &mut Builder, from: Self, to: Self, membership: u32) -> Result<()> {
        builder.push_interned_membership_v4(from, to, membership)
    }
}

impl FeedKey for Ipv6Key {
    fn push(builder: &mut Builder, from: Self, to: Self, membership: u32) -> Result<()> {
        builder.push_interned_membership_v6(from, to, membership)
    }
}

struct OneFeed;

impl MembershipWords for OneFeed {
    fn word_count(&self) -> u32 {
        1
    }

    fn read_words(&self, start: u32, output: &mut [u64]) -> Result<()> {
        if start != 0 || output.len() != 1 {
            return Err(Error::Corrupt("one-feed membership read is invalid"));
        }
        output[0] = 1;
        Ok(())
    }
}

fn output_spec(family: AddressFamily, value_tag: ValueTag) -> Result<OutputSpec> {
    OutputSpec::fresh(
        family,
        ValueKind::Membership,
        crate::StructureKind::None,
        value_tag,
        1,
    )
}
