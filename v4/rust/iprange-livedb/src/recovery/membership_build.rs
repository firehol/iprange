//! Canonical immutable output from one membership recovery analysis.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::immutable_output::{Builder, Finished};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;

use super::membership::{analyze, MembershipAnalysis};
use super::membership_output::{Components, MembershipKey};
use super::range_build::{
    build_ranges, failed_pages, retained_metadata_bytes, write_metadata, RangeBuild, SortReuse,
};
use super::report::{RecoveryReport, RecoverySink, Reporter};
use super::{RecoveryBudget, ScratchCleanup};

#[derive(Debug)]
pub(crate) struct MembershipConstruction {
    pub(crate) finished: Finished,
    pub(crate) report: RecoveryReport,
    pub(crate) scratch: Option<ScratchCleanup>,
}

#[derive(Debug)]
pub(crate) struct MembershipConstructionFailure {
    pub(crate) builder: Builder,
    pub(crate) cause: Error,
    pub(crate) report: RecoveryReport,
    pub(crate) scratch: Option<ScratchCleanup>,
}

#[allow(clippy::result_large_err)]
pub(crate) fn construct<S: RecoverySink>(
    mapping: &Mapping,
    source_meta: MetaV4,
    builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<MembershipConstruction, MembershipConstructionFailure> {
    if let Err(cause) = require_builder(&builder, source_meta) {
        return Err(failure(builder, cause, RecoveryReport::default(), None));
    }
    let analysis = match analyze(mapping, source_meta, budget, cancellation, sink) {
        Ok(analysis) => analysis,
        Err(error) => return Err(failure(builder, error.cause, error.report, error.scratch)),
    };
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
) -> std::result::Result<MembershipConstruction, MembershipConstructionFailure> {
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
        return Err(failure(builder, failed.cause, report, failed.scratch));
    }
    let retained = match retained_bytes(&tables, &metadata) {
        Ok(retained) => retained,
        Err(cause) => {
            let failed = failed_pages(
                pages.take().expect("analysis retains page ownership"),
                cause,
            );
            return Err(failure(builder, failed.cause, report, failed.scratch));
        }
    };
    #[cfg(any(unix, windows))]
    let sort_reuse = SortReuse::area(tables.scratch_region());
    #[cfg(not(any(unix, windows)))]
    let sort_reuse = SortReuse::none();
    let mut reporter = Reporter::resume(report, sink);
    let result = {
        let mut output = Components::<S, K>::new(
            mapping,
            source_meta,
            &memberships,
            &tables,
            &mut builder,
            &mut reporter,
            cancellation,
        );
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
    let report = reporter.finish();
    let scratch = match result {
        Ok(scratch) => scratch,
        Err(error) => return Err(failure(builder, error.cause, report, error.scratch)),
    };
    if let Err(cause) = write_metadata(
        &mut builder,
        metadata.as_deref(),
        budget.max_heap_bytes,
        retained_metadata_bytes(&metadata),
    ) {
        return Err(failure(builder, cause, report, scratch));
    }
    match builder.finish_owned() {
        Ok(finished) => Ok(MembershipConstruction {
            finished,
            report,
            scratch,
        }),
        Err(error) => Err(failure(error.builder, error.cause, report, scratch)),
    }
}

fn retained_bytes(tables: &super::tables::Tables, metadata: &Option<Vec<u8>>) -> Result<u64> {
    tables
        .retained_bytes()
        .checked_add(retained_metadata_bytes(metadata))
        .ok_or(Error::ArithmeticOverflow("recovery retained heap"))
}

fn require_builder(builder: &Builder, source: MetaV4) -> Result<()> {
    let output = builder.meta();
    if output.address_family != source.address_family
        || output.value_kind != ValueKind::Membership
        || output.value_tag != source.value_tag
        || output.feed_index_limit != source.feed_index_limit
        || output.txn_id != 1
    {
        return Err(Error::InvalidArgument(
            "recovery output does not match its membership source",
        ));
    }
    Ok(())
}

fn failure(
    builder: Builder,
    cause: Error,
    report: RecoveryReport,
    scratch: Option<ScratchCleanup>,
) -> MembershipConstructionFailure {
    MembershipConstructionFailure {
        builder,
        cause,
        report,
        scratch,
    }
}

#[cfg(test)]
#[path = "membership_tests.rs"]
mod tests;
