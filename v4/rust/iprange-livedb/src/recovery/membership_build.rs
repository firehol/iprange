//! Canonical immutable output from one membership recovery analysis.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::immutable_output::Builder;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;

use super::construction::{self, Construction, Failure};
use super::membership::{analyze, MembershipAnalysis};
use super::membership_output::{MembershipKey, MembershipOutput};
use super::range_build::{
    build_ranges, failed_pages, retained_metadata_bytes, RangeBuild, SortReuse,
};
use super::range_components::Components;
use super::report::RecoverySink;
use super::RecoveryBudget;

#[allow(clippy::result_large_err)]
pub(crate) fn construct<S: RecoverySink>(
    mapping: &Mapping,
    source_meta: MetaV4,
    builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<Construction, Failure> {
    let (builder, analysis) =
        construction::prepare(builder, source_meta, ValueKind::Membership, || {
            analyze(mapping, source_meta, budget, cancellation, sink)
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
        ),
        AddressFamily::Ipv6 => build::<Ipv6Key, S>(
            mapping,
            source_meta,
            builder,
            budget,
            cancellation,
            sink,
            analysis,
        ),
    }
}

#[allow(clippy::too_many_arguments, clippy::result_large_err)]
fn build<K: MembershipKey, S: RecoverySink>(
    mapping: &Mapping,
    source_meta: MetaV4,
    mut builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
    analysis: MembershipAnalysis,
) -> std::result::Result<Construction, Failure> {
    let MembershipAnalysis {
        report,
        readable_records,
        ordered,
        catalog,
        memberships,
        tables,
        metadata,
        pages,
    } = analysis;
    let mut pages = Some(pages);
    if let Err(cause) =
        catalog.for_each(&tables, |entry| builder.push_feed(entry.name, entry.index))
    {
        let failed = failed_pages(
            pages.take().expect("analysis retains page ownership"),
            cause,
        );
        return Err(construction::failure(
            builder,
            failed.cause,
            report,
            failed.scratch,
        ));
    }
    let retained = match retained_bytes(&tables, &metadata) {
        Ok(retained) => retained,
        Err(cause) => {
            let failed = failed_pages(
                pages.take().expect("analysis retains page ownership"),
                cause,
            );
            return Err(construction::failure(
                builder,
                failed.cause,
                report,
                failed.scratch,
            ));
        }
    };
    #[cfg(any(unix, windows))]
    let sort_reuse = SortReuse::area(tables.scratch_region());
    #[cfg(not(any(unix, windows)))]
    let sort_reuse = SortReuse::none();
    construction::complete_ranges(
        builder,
        metadata.as_deref(),
        budget.max_heap_bytes,
        retained_metadata_bytes(&metadata),
        report,
        sink,
        move |builder, reporter| {
            let result = {
                let policy = MembershipOutput::<S, K>::new(
                    mapping,
                    source_meta,
                    &memberships,
                    &tables,
                    builder,
                    reporter,
                );
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
                        sort_reuse,
                    },
                    pages.take().expect("analysis retains page ownership"),
                    &mut output,
                )
            };
            drop(tables);
            result
        },
    )
}

fn retained_bytes(tables: &super::tables::Tables, metadata: &Option<Vec<u8>>) -> Result<u64> {
    tables
        .retained_bytes()
        .checked_add(retained_metadata_bytes(metadata))
        .ok_or(Error::ArithmeticOverflow("recovery retained heap"))
}

#[cfg(test)]
#[path = "membership_tests.rs"]
mod tests;
