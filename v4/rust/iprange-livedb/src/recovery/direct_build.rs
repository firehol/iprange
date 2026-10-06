//! Construction of a canonical direct database from one recovery analysis.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::immutable_output::Builder;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;

use super::construction::{self, Construction, Failure};
use super::direct::{analyze_following, DirectAnalysis};
use super::direct_output::{DirectKey, DirectOutput};
use super::range_build::{build_ranges, retained_metadata_bytes, RangeBuild, SortReuse};
use super::range_components::Components;
use super::report::RecoverySink;
use super::RecoveryBudget;

#[allow(clippy::result_large_err)]
#[cfg(test)]
pub(crate) fn construct<S: RecoverySink>(
    mapping: &Mapping,
    source_meta: MetaV4,
    builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<Construction, Failure> {
    construct_following(
        mapping,
        source_meta,
        builder,
        budget,
        cancellation,
        sink,
        crate::creator_only_requested(),
    )
}

pub(crate) fn construct_following<S: RecoverySink>(
    mapping: &Mapping,
    source_meta: MetaV4,
    builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
    creator_only: bool,
) -> std::result::Result<Construction, Failure> {
    let (builder, analysis) =
        construction::prepare(builder, source_meta, ValueKind::Direct, || {
            analyze_following(mapping, source_meta, budget, cancellation, sink, creator_only)
        })?;
    match source_meta.address_family {
        AddressFamily::Ipv4 => build::<Ipv4Key, S>(
            mapping,
            source_meta,
            builder,
            budget,
            cancellation,
            sink,
            analysis,
            creator_only,
        ),
        AddressFamily::Ipv6 => build::<Ipv6Key, S>(
            mapping,
            source_meta,
            builder,
            budget,
            cancellation,
            sink,
            analysis,
            creator_only,
        ),
    }
}

#[allow(clippy::too_many_arguments, clippy::result_large_err)]
fn build<K: DirectKey, S: RecoverySink>(
    mapping: &Mapping,
    source_meta: MetaV4,
    builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
    analysis: DirectAnalysis,
    creator_only: bool,
) -> std::result::Result<Construction, Failure> {
    let DirectAnalysis {
        report,
        readable_records,
        ordered,
        metadata,
        pages,
    } = analysis;
    let retained = retained_metadata_bytes(&metadata);
    construction::complete_ranges(
        builder,
        metadata.as_deref(),
        budget.max_heap_bytes,
        retained,
        report,
        sink,
        |builder, reporter| {
            let policy = DirectOutput::<S, K>::new(builder, reporter);
            let mut output = Components::new(cancellation, policy);
            build_ranges(
                RangeBuild {
                    mapping,
                    meta: source_meta,
                    budget,
                    cancellation,
                    readable_records,
                    ordered,
                    retained_heap_bytes: retained,
                    sort_reuse: SortReuse::none(),
                    creator_only,
                },
                pages,
                &mut output,
            )
        },
    )
}
