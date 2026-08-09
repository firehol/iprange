//! Construction of a canonical direct database from one recovery analysis.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::immutable_output::{Builder, Finished};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;

use super::direct::{analyze, DirectAnalysis};
use super::direct_output::{Components, DirectKey};
use super::range_build::{
    build_ranges, retained_metadata_bytes, write_metadata, RangeBuild, SortReuse,
};
use super::report::{RecoveryReport, RecoverySink, Reporter};
use super::{RecoveryBudget, ScratchCleanup};

#[derive(Debug)]
pub(crate) struct DirectConstruction {
    pub(crate) finished: Finished,
    pub(crate) report: RecoveryReport,
    pub(crate) scratch: Option<ScratchCleanup>,
}

#[derive(Debug)]
pub(crate) struct DirectConstructionFailure {
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
) -> std::result::Result<DirectConstruction, DirectConstructionFailure> {
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
fn build<K: DirectKey, S: RecoverySink>(
    mapping: &Mapping,
    source_meta: MetaV4,
    mut builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
    analysis: DirectAnalysis,
) -> std::result::Result<DirectConstruction, DirectConstructionFailure> {
    let DirectAnalysis {
        report,
        readable_records,
        ordered,
        metadata,
        pages,
    } = analysis;
    let retained = retained_metadata_bytes(&metadata);
    let mut reporter = Reporter::resume(report, sink);
    let result = {
        let mut output = Components::<S, K>::new(&mut builder, &mut reporter, cancellation);
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
            },
            pages,
            &mut output,
        )
    };
    let report = reporter.finish();
    let scratch = match result {
        Ok(scratch) => scratch,
        Err(error) => return Err(failure(builder, error.cause, report, error.scratch)),
    };
    if let Err(cause) = write_metadata(
        &mut builder,
        metadata.as_deref(),
        budget.max_heap_bytes,
        retained,
    ) {
        return Err(failure(builder, cause, report, scratch));
    }
    match builder.finish_owned() {
        Ok(finished) => Ok(DirectConstruction {
            finished,
            report,
            scratch,
        }),
        Err(error) => Err(failure(error.builder, error.cause, report, scratch)),
    }
}

fn require_builder(builder: &Builder, source: MetaV4) -> Result<()> {
    let output = builder.meta();
    if output.address_family != source.address_family
        || output.value_kind != ValueKind::Direct
        || output.value_tag != source.value_tag
        || output.feed_index_limit != 0
        || output.txn_id != 1
    {
        return Err(Error::InvalidArgument(
            "recovery output does not match its direct source",
        ));
    }
    Ok(())
}

fn failure(
    builder: Builder,
    cause: Error,
    report: RecoveryReport,
    scratch: Option<ScratchCleanup>,
) -> DirectConstructionFailure {
    DirectConstructionFailure {
        builder,
        cause,
        report,
        scratch,
    }
}
