//! Typed immutable map normalization through existing mapped engine owners.
use crate::immutable_map::{ImmutableBuildBudget, ImmutableBuildOutcome, ImmutableInputOrder};
use crate::immutable_output::{self, unordered, Builder, OutputSpec};
use crate::key::IpKey;
use crate::range_mutation::{self, AssignmentInput};
use crate::range_store_cursor::Cursor;
use crate::{
    CancellationToken, Cardinality129, DirectRange, Error, Ipv4Key, Ipv6Key, PublicationPolicy,
    RangeSource, Result, StructureKind, ValueKind, ValueTag,
};
use std::path::Path;
pub(crate) trait MapKey: IpKey {
    fn push_direct(builder: &mut Builder, range: DirectRange<Self>) -> Result<()>;
    fn push_membership(builder: &mut Builder, range: DirectRange<Self>) -> Result<()>;
}
macro_rules! key_impl {
    ($key:ty, $direct:ident, $membership:ident) => {
        impl MapKey for $key {
            fn push_direct(b: &mut Builder, r: DirectRange<Self>) -> Result<()> {
                b.$direct(r.from, r.to, r.value)
            }
            fn push_membership(b: &mut Builder, r: DirectRange<Self>) -> Result<()> {
                b.$membership(r.from, r.to, r.value)
            }
        }
    };
}
key_impl!(Ipv4Key, push_direct_v4, push_interned_membership_v4);
key_impl!(Ipv6Key, push_direct_v6, push_interned_membership_v6);

pub(crate) fn prepared(
    budget: &ImmutableBuildBudget,
    order: ImmutableInputOrder,
) -> Result<unordered::PreparedBudget> {
    unordered::prepare_budget(unordered::BudgetInput {
        max_heap_bytes: budget.max_heap_bytes,
        max_output_pages: budget.max_output_pages,
        max_workspace_pages: if order == ImmutableInputOrder::Unordered {
            budget.max_workspace_pages
        } else {
            0
        },
        max_open_files: budget.max_open_files,
    })
}
pub(crate) fn early(cause: Error) -> Box<crate::ImmutableFeedPreparationFailure> {
    Box::new(crate::ImmutableFeedPreparationFailure::early(cause))
}
#[allow(clippy::too_many_arguments)]
pub(crate) fn direct<K: MapKey, S: RangeSource<DirectRange<K>>>(
    destination: &Path,
    tag: ValueTag,
    metadata: Option<&[u8]>,
    policy: PublicationPolicy,
    source: &mut S,
    order: ImmutableInputOrder,
    budget: &ImmutableBuildBudget,
    cancel: &CancellationToken,
) -> ImmutableBuildOutcome {
    let budget = prepared(budget, order).map_err(early)?;
    let spec = OutputSpec::fresh(K::FAMILY, ValueKind::Direct, StructureKind::None, tag, 0)
        .map_err(early)?;
    crate::immutable_feed::publish_built(destination, policy, cancel, |file| {
        immutable_output::construction::build(
            file,
            spec,
            budget,
            order == ImmutableInputOrder::Unordered,
            |builder, workspace, heap| {
                let mut stats = Stats::default();
                let mut input_records = 0;
                if let Some(workspace) = workspace {
                    let mut root = 0;
                    let mut count = 0;
                    let mut input = AssignmentInput::<K>::new(heap);
                    crate::source::drain(source, cancel, &mut input_records, |r| {
                        range_mutation::assign_private_input(
                            workspace, &mut root, &mut count, r.from, r.to, r.value, &mut input,
                        )?;
                        Ok(())
                    })?;
                    drop(input);
                    let meta = workspace_meta(spec, workspace.page_count(), root, count);
                    let mut cursor = Cursor::<K>::new(workspace, &meta, false)?;
                    while let Some(r) = cursor.next(workspace)? {
                        if stats.output & 4095 == 0 {
                            cancel.check()?;
                        }
                        stats.emit(
                            builder,
                            DirectRange {
                                from: r.from,
                                to: r.to,
                                value: r.value,
                            },
                            false,
                        )?;
                    }
                } else {
                    let mut pending = None;
                    crate::source::drain(source, cancel, &mut input_records, |r| {
                        ordered_push(&mut pending, r, |r| stats.emit(builder, r, false))
                    })?;
                    if let Some(r) = pending {
                        stats.emit(builder, r, false)?;
                    }
                }
                stats.input = input_records;
                cancel.check()?;
                if let Some(metadata) = metadata {
                    builder.write_metadata_with_budget(metadata, heap)?;
                }
                Ok(stats.report())
            },
        )
    })
}

pub(crate) fn workspace_meta(
    spec: OutputSpec,
    pages: u64,
    root: u32,
    count: u64,
) -> crate::contract::MetaV4 {
    let mut meta = crate::database_file::empty_meta(crate::database_file::EmptySpec {
        address_family: spec.address_family,
        value_kind: spec.value_kind,
        structure_kind: spec.structure_kind,
        value_tag: spec.value_tag,
        database_id: spec.database_id,
        transaction_id: spec.transaction_id,
        commit_nonce: spec.commit_nonce,
        feed_index_limit: spec.feed_index_limit,
    });
    meta.page_count = pages;
    meta.range_root = root;
    meta.range_record_count = count;
    meta
}
pub(crate) struct Stats {
    pub input: u64,
    pub output: u64,
    pub addresses: Cardinality129,
}
impl Default for Stats {
    fn default() -> Self {
        Self {
            input: 0,
            output: 0,
            addresses: Cardinality129::ZERO,
        }
    }
}
impl Stats {
    pub fn emit<K: MapKey>(
        &mut self,
        b: &mut Builder,
        r: DirectRange<K>,
        membership: bool,
    ) -> Result<()> {
        if membership {
            K::push_membership(b, r)?;
        } else {
            K::push_direct(b, r)?;
        }
        self.output = self
            .output
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("immutable interval count"))?;
        self.addresses = self
            .addresses
            .checked_add(r.from.inclusive_cardinality(r.to)?)
            .map_err(|_| Error::ArithmeticOverflow("immutable address count"))?;
        Ok(())
    }
    pub fn report(self) -> unordered::OutputStats {
        unordered::OutputStats {
            input_record_count: self.input,
            normalized_interval_count: self.output,
            addresses: self.addresses,
        }
    }
}
pub(crate) fn ordered_push<K: MapKey>(
    pending: &mut Option<DirectRange<K>>,
    next: DirectRange<K>,
    mut emit: impl FnMut(DirectRange<K>) -> Result<()>,
) -> Result<()> {
    if next.from > next.to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    if let Some(prior) = pending.as_mut() {
        if next.from <= prior.to {
            return Err(Error::InvalidArgument(
                "ordered immutable input overlaps or is out of order",
            ));
        }
        if prior.value == next.value && prior.to.checked_next() == Some(next.from) {
            prior.to = next.to;
            return Ok(());
        }
        emit(*prior)?;
    }
    *pending = Some(next);
    Ok(())
}
