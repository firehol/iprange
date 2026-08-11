//! Shared recovery output envelope and finalization.

use crate::contract::{MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::immutable_output::{Builder, Finished};

use super::page_set::PageSet;
use super::range_build::{write_metadata, BuildResult};
use super::report::{RecoverySink, Reporter};
use super::{RecoveryReport, ScratchCleanup};

#[derive(Debug)]
pub(crate) struct Construction {
    pub(crate) finished: Finished,
    pub(crate) report: RecoveryReport,
    pub(crate) scratch: Option<ScratchCleanup>,
}

#[derive(Debug)]
pub(crate) struct Failure {
    pub(crate) builder: Builder,
    pub(crate) cause: Error,
    pub(crate) report: RecoveryReport,
    pub(crate) scratch: Option<ScratchCleanup>,
}

pub(crate) struct AnalysisFailure {
    pub(crate) cause: Error,
    pub(crate) report: RecoveryReport,
    pub(crate) scratch: Option<ScratchCleanup>,
}

#[allow(clippy::result_large_err)]
pub(crate) fn prepare<A>(
    builder: Builder,
    source: MetaV4,
    kind: ValueKind,
    analyze: impl FnOnce() -> std::result::Result<A, AnalysisFailure>,
) -> std::result::Result<(Builder, A), Failure> {
    if let Err(cause) = require_builder(&builder, source, kind) {
        return Err(failure(builder, cause, RecoveryReport::default(), None));
    }
    match analyze() {
        Ok(analysis) => Ok((builder, analysis)),
        Err(error) => Err(failure(builder, error.cause, error.report, error.scratch)),
    }
}

pub(crate) fn require_builder(builder: &Builder, source: MetaV4, kind: ValueKind) -> Result<()> {
    let output = builder.meta();
    let feed_index_limit = match kind {
        ValueKind::Direct => 0,
        ValueKind::Membership => source.feed_index_limit,
        ValueKind::Structured => source.feed_index_limit,
    };
    if output.address_family != source.address_family
        || output.value_kind != kind
        || output.structure_kind() != source.structure_kind()
        || output.value_tag != source.value_tag
        || output.feed_index_limit != feed_index_limit
        || output.txn_id != 1
    {
        return Err(Error::InvalidArgument(match kind {
            ValueKind::Direct => "recovery output does not match its direct source",
            ValueKind::Membership => "recovery output does not match its membership source",
            ValueKind::Structured => "recovery output does not match its structured source",
        }));
    }
    Ok(())
}

#[allow(clippy::result_large_err)]
pub(crate) fn finish(
    mut builder: Builder,
    metadata: Option<&[u8]>,
    max_heap_bytes: u64,
    retained_heap_bytes: u64,
    report: RecoveryReport,
    scratch: Option<ScratchCleanup>,
) -> std::result::Result<Construction, Failure> {
    if let Err(cause) = write_metadata(&mut builder, metadata, max_heap_bytes, retained_heap_bytes)
    {
        return Err(failure(builder, cause, report, scratch));
    }
    match builder.finish_owned() {
        Ok(finished) => Ok(Construction {
            finished,
            report,
            scratch,
        }),
        Err(error) => Err(failure(error.builder, error.cause, report, scratch)),
    }
}

#[allow(clippy::result_large_err)]
pub(crate) fn complete_ranges<S: RecoverySink>(
    mut builder: Builder,
    metadata: Option<&[u8]>,
    max_heap_bytes: u64,
    retained_heap_bytes: u64,
    report: RecoveryReport,
    sink: &mut S,
    build: impl FnOnce(&mut Builder, &mut Reporter<'_, S>) -> BuildResult,
) -> std::result::Result<Construction, Failure> {
    let mut reporter = Reporter::resume(report, sink);
    let result = build(&mut builder, &mut reporter);
    let report = reporter.finish();
    let scratch = match result {
        Ok(scratch) => scratch,
        Err(error) => return Err(failure(builder, error.cause, report, error.scratch)),
    };
    finish(
        builder,
        metadata,
        max_heap_bytes,
        retained_heap_bytes,
        report,
        scratch,
    )
}

pub(crate) fn failure(
    builder: Builder,
    cause: Error,
    report: RecoveryReport,
    scratch: Option<ScratchCleanup>,
) -> Failure {
    Failure {
        builder,
        cause,
        report,
        scratch,
    }
}

pub(crate) fn analysis_failure(
    cause: Error,
    report: RecoveryReport,
    scratch: Option<ScratchCleanup>,
) -> AnalysisFailure {
    AnalysisFailure {
        cause,
        report,
        scratch,
    }
}

pub(crate) fn analysis_failure_with_pages(
    pages: PageSet,
    cause: Error,
    report: RecoveryReport,
) -> AnalysisFailure {
    match pages.finish(Err(cause)) {
        Err(failure) => analysis_failure(failure.cause, report, failure.cleanup),
        Ok(_) => unreachable!("failed analysis cannot finish successfully"),
    }
}
