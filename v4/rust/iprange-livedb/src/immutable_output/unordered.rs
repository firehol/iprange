//! Unordered single-feed construction inside one private output inode.

#[path = "unordered/workspace.rs"]
mod workspace;

use std::fs::File;

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::{AddressFamily, ValueKind, ValueTag, MAX_PAGE_COUNT, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::feed::FeedName;
use crate::heap::Heap;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::range_mutation::{self, UnionInput};
use crate::range_store_cursor::Cursor;
use crate::source::{self, RangeSource};
use crate::workflow::AddressRange;

use super::{Builder, Finished, MembershipWords, OutputBudget, OutputSpec};
use workspace::Workspace;

#[derive(Clone, Copy)]
pub(crate) struct BudgetInput {
    pub(crate) max_heap_bytes: u64,
    pub(crate) max_output_pages: u64,
    pub(crate) max_workspace_pages: u64,
    pub(crate) max_open_files: u32,
}

pub(crate) struct PreparedBudget {
    max_heap_bytes: u64,
    max_output_pages: u64,
    total_pages: u64,
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
    input_record_count: u64,
}

struct OutputStats {
    input_record_count: u64,
    normalized_interval_count: u64,
    addresses: Cardinality129,
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
pub(crate) fn build<K, S>(
    file: File,
    value_tag: ValueTag,
    feed_name: FeedName,
    metadata_json: Option<&[u8]>,
    source: &mut S,
    budget: PreparedBudget,
    cancellation: &CancellationToken,
) -> std::result::Result<Built, BuildFailure>
where
    K: FeedKey,
    S: RangeSource<AddressRange<K>>,
{
    let spec = match output_spec(K::FAMILY, value_tag) {
        Ok(spec) => spec,
        Err(cause) => return Err(BuildFailure { file, cause }),
    };
    let output_budget = OutputBudget {
        max_output_pages: budget.max_output_pages,
    };
    let mut heap = Heap::new(budget.max_heap_bytes);
    let mut builder =
        Builder::new_owned_with_extent(file, spec, output_budget, budget.total_pages, &mut heap)
            .map_err(|failure| BuildFailure {
                file: failure.file,
                cause: failure.cause,
            })?;
    let workspace_file = match builder.clone_file() {
        Ok(file) => file,
        Err(cause) => {
            return Err(BuildFailure {
                file: builder.into_file(),
                cause,
            });
        }
    };
    let Some(extent) = budget.total_pages.checked_mul(PAGE_SIZE as u64) else {
        return Err(BuildFailure {
            file: builder.into_file(),
            cause: Error::ArithmeticOverflow("immutable construction extent"),
        });
    };
    let mapping = match Mapping::read_write(workspace_file, extent) {
        Ok(mapping) => mapping,
        Err(cause) => {
            return Err(BuildFailure {
                file: builder.into_file(),
                cause,
            });
        }
    };
    let mut workspace = match Workspace::new(
        mapping,
        budget.max_output_pages,
        budget.total_pages,
        spec.transaction_id,
    ) {
        Ok(workspace) => workspace,
        Err(cause) => {
            return Err(BuildFailure {
                file: builder.into_file(),
                cause,
            });
        }
    };
    let result = build_mapped::<K, S>(
        &mut workspace,
        &mut builder,
        spec,
        feed_name,
        metadata_json,
        source,
        heap.remaining(),
        cancellation,
    );
    drop(workspace);
    let report = match result {
        Ok(report) => report,
        Err(cause) => {
            return Err(BuildFailure {
                file: builder.into_file(),
                cause,
            });
        }
    };
    let finished = builder.finish_owned().map_err(|failure| BuildFailure {
        file: failure.builder.into_file(),
        cause: failure.cause,
    })?;
    Ok(Built {
        finished,
        input_record_count: report.input_record_count,
        normalized_interval_count: report.normalized_interval_count,
        addresses: report.addresses,
    })
}

#[allow(clippy::too_many_arguments)]
fn build_mapped<K, S>(
    workspace: &mut Workspace,
    builder: &mut Builder,
    spec: OutputSpec,
    feed_name: FeedName,
    metadata_json: Option<&[u8]>,
    source: &mut S,
    max_heap_bytes: u64,
    cancellation: &CancellationToken,
) -> Result<OutputStats>
where
    K: FeedKey,
    S: RangeSource<AddressRange<K>>,
{
    let normalized = normalize::<K, S>(workspace, source, max_heap_bytes, cancellation)?;
    write_normalized::<K>(
        workspace,
        builder,
        spec,
        feed_name,
        metadata_json,
        max_heap_bytes,
        normalized,
        cancellation,
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
fn write_normalized<K: FeedKey>(
    workspace: &mut Workspace,
    builder: &mut Builder,
    spec: OutputSpec,
    feed_name: FeedName,
    metadata_json: Option<&[u8]>,
    max_heap_bytes: u64,
    normalized: Normalized,
    cancellation: &CancellationToken,
) -> Result<OutputStats> {
    builder.push_feed(feed_name, 0)?;
    let membership = (normalized.record_count != 0)
        .then(|| builder.intern_membership_value(&OneFeed))
        .transpose()?;
    let mut meta = crate::database_file::empty_meta(crate::database_file::EmptySpec {
        address_family: K::FAMILY,
        value_kind: ValueKind::Membership,
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
    while let Some(range) = cursor.next(workspace)? {
        if output_ranges & 4095 == 4095 {
            cancellation.check()?;
        }
        if range.value != 1 {
            return Err(Error::Corrupt(
                "immutable feed workspace contains a non-coverage value",
            ));
        }
        let membership = membership
            .ok_or_else(|| Error::Corrupt("immutable feed coverage has no membership"))?;
        K::push(builder, range.from, range.to, membership)?;
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
        normalized_interval_count: normalized.record_count,
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
    OutputSpec::fresh(family, ValueKind::Membership, value_tag, 1)
}
