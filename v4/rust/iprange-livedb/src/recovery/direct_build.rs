//! Construction of a canonical direct database from one recovery analysis.

use std::fs::File;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::immutable_output::{Builder, Finished};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_tree::Record;

use super::direct::{analyze, DirectAnalysis};
use super::direct_output::{Components, DirectKey};
#[cfg(target_os = "linux")]
use super::external_sort::{self, ExternalSortFailure};
use super::page_set::PageSet;
use super::range_build::{buffer_fits, events, require_count, reserve};
use super::range_scan;
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
    file: &File,
    source_meta: MetaV4,
    builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<DirectConstruction, DirectConstructionFailure> {
    if let Err(cause) = require_builder(&builder, source_meta) {
        return Err(failure(builder, cause, RecoveryReport::default(), None));
    }
    let analysis = match analyze(file, source_meta, budget, cancellation, sink) {
        Ok(analysis) => analysis,
        Err(error) => return Err(failure(builder, error.cause, error.report, error.scratch)),
    };
    match source_meta.address_family {
        AddressFamily::Ipv4 => build::<Ipv4Key, S>(
            file,
            source_meta,
            builder,
            budget,
            cancellation,
            sink,
            analysis,
        ),
        AddressFamily::Ipv6 => build::<Ipv6Key, S>(
            file,
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
    file: &File,
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
    let context = BuildContext {
        file,
        meta: source_meta,
        budget,
        cancellation,
    };
    let mut reporter = Reporter::resume(report, sink);
    let result: BuildResult = if ordered {
        build_ordered::<K, S>(
            context,
            &mut builder,
            &mut reporter,
            readable_records,
            &metadata,
            pages,
        )
    } else {
        build_sorted::<K, S>(
            context,
            &mut builder,
            &mut reporter,
            readable_records,
            &metadata,
            pages,
        )
    };
    let report = reporter.finish();
    let scratch = match result {
        Ok(scratch) => scratch,
        Err(error) => return Err(failure(builder, error.cause, report, error.scratch)),
    };
    match builder.finish_owned() {
        Ok(finished) => Ok(DirectConstruction {
            finished,
            report,
            scratch,
        }),
        Err(error) => Err(failure(error.builder, error.cause, report, scratch)),
    }
}

type BuildResult = std::result::Result<Option<ScratchCleanup>, BuildFailure>;

struct BuildFailure {
    cause: Error,
    scratch: Option<ScratchCleanup>,
}

#[derive(Clone, Copy)]
struct BuildContext<'a> {
    file: &'a File,
    meta: MetaV4,
    budget: &'a RecoveryBudget,
    cancellation: &'a CancellationToken,
}

#[allow(clippy::result_large_err)]
fn build_ordered<K: DirectKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    metadata: &Option<Vec<u8>>,
    mut pages: PageSet,
) -> BuildResult {
    let metadata_bytes = retained_bytes(metadata);
    let scan = (|| {
        pages.reset()?;
        let mut components = Components::<S, K>::new(builder, reporter, context.cancellation);
        {
            let mut events = events(true, |record| components.push(record));
            range_scan::scan(
                context.file,
                context.meta,
                &mut pages,
                context.cancellation,
                &mut events,
            )?;
            require_count(events.readable_records(), readable_records)?;
        }
        components.finish()
    })();
    let scratch = finish_pages(pages, scan)?;
    write_metadata(
        builder,
        metadata.as_deref(),
        context.budget.max_heap_bytes,
        metadata_bytes,
    )
    .map_err(|cause| after_cleanup(cause, &scratch))?;
    Ok(scratch)
}

#[allow(clippy::result_large_err)]
fn build_sorted<K: DirectKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    metadata: &Option<Vec<u8>>,
    pages: PageSet,
) -> BuildResult {
    let metadata_bytes = retained_bytes(metadata);
    let retained = match metadata_bytes.checked_add(pages.retained_bytes()) {
        Some(retained) => retained,
        None => {
            return finish_pages(
                pages,
                Err(Error::ArithmeticOverflow("recovery retained heap")),
            )
        }
    };
    match buffer_fits::<K>(readable_records, retained, context.budget) {
        Ok(_) => build_in_memory::<K, S>(
            context,
            builder,
            reporter,
            readable_records,
            metadata,
            retained,
            pages,
        ),
        Err(Error::BudgetExceeded(_)) => build_external::<K, S>(
            context,
            builder,
            reporter,
            readable_records,
            metadata,
            metadata_bytes,
            pages,
        ),
        Err(cause) => finish_pages(pages, Err(cause)),
    }
}

#[allow(clippy::result_large_err)]
fn build_in_memory<K: DirectKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    metadata: &Option<Vec<u8>>,
    retained: u64,
    mut pages: PageSet,
) -> BuildResult {
    let metadata_bytes = retained_bytes(metadata);
    let available = context
        .budget
        .max_heap_bytes
        .checked_sub(retained)
        .ok_or(Error::BudgetExceeded("recovery unordered ranges"));
    let mut records = match available.and_then(|bytes| reserve::<K>(readable_records, bytes)) {
        Ok(records) => records,
        Err(cause) => return finish_pages(pages, Err(cause)),
    };
    let scan = collect_ranges(context, readable_records, &mut pages, &mut records);
    if let Err(cause) = scan {
        drop(records);
        return finish_pages(pages, Err(cause));
    }
    let scratch = finish_pages(pages, Ok(()))?;
    emit_sorted(context.cancellation, builder, reporter, records)
        .map_err(|cause| after_cleanup(cause, &scratch))?;
    write_metadata(
        builder,
        metadata.as_deref(),
        context.budget.max_heap_bytes,
        metadata_bytes,
    )
    .map_err(|cause| after_cleanup(cause, &scratch))?;
    Ok(scratch)
}

fn collect_ranges<K: DirectKey>(
    context: BuildContext<'_>,
    readable_records: u64,
    pages: &mut PageSet,
    records: &mut Vec<Record<K>>,
) -> Result<()> {
    pages.reset()?;
    let mut events = events(false, |record| {
        records.push(record);
        Ok(())
    });
    range_scan::scan(
        context.file,
        context.meta,
        pages,
        context.cancellation,
        &mut events,
    )?;
    require_count(events.readable_records(), readable_records)?;
    records.sort_unstable_by(|left, right| {
        (left.from, left.to, left.value).cmp(&(right.from, right.to, right.value))
    });
    Ok(())
}

#[cfg(target_os = "linux")]
#[allow(clippy::result_large_err)]
fn build_external<K: DirectKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    metadata: &Option<Vec<u8>>,
    metadata_bytes: u64,
    pages: PageSet,
) -> BuildResult {
    let mut components = Components::<S, K>::new(builder, reporter, context.cancellation);
    let cleanup = external_sort::sort_and_emit::<K>(
        context.file,
        external_sort::SortRequest {
            meta: context.meta,
            budget: context.budget,
            retained_heap_bytes: metadata_bytes,
            readable_records,
            cancellation: context.cancellation,
        },
        pages,
        |record| components.push(record),
    )
    .map_err(external_failure)?;
    components.finish().map_err(|cause| BuildFailure {
        cause,
        scratch: Some(cleanup.clone()),
    })?;
    write_metadata(
        builder,
        metadata.as_deref(),
        context.budget.max_heap_bytes,
        metadata_bytes,
    )
    .map_err(|cause| BuildFailure {
        cause,
        scratch: Some(cleanup.clone()),
    })?;
    Ok(Some(cleanup))
}

#[cfg(not(target_os = "linux"))]
fn build_external<K: DirectKey, S: RecoverySink>(
    _context: BuildContext<'_>,
    _builder: &mut Builder,
    _reporter: &mut Reporter<'_, S>,
    _readable_records: u64,
    _metadata: &Option<Vec<u8>>,
    _metadata_bytes: u64,
    pages: PageSet,
) -> BuildResult {
    finish_pages(
        pages,
        Err(Error::Unsupported(
            "external recovery sorting is not implemented on this platform",
        )),
    )
}

#[cfg(target_os = "linux")]
fn external_failure(error: ExternalSortFailure) -> BuildFailure {
    BuildFailure {
        cause: error.cause,
        scratch: error.cleanup,
    }
}

fn emit_sorted<K: DirectKey, S: RecoverySink>(
    cancellation: &CancellationToken,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    records: Vec<Record<K>>,
) -> Result<()> {
    let mut components = Components::<S, K>::new(builder, reporter, cancellation);
    for record in records {
        cancellation.check()?;
        components.push(record)?;
    }
    components.finish()
}

fn retained_bytes(metadata: &Option<Vec<u8>>) -> u64 {
    metadata.as_ref().map_or(0, |value| value.capacity() as u64)
}

fn write_metadata(
    builder: &mut Builder,
    metadata: Option<&[u8]>,
    max_heap_bytes: u64,
    retained_bytes: u64,
) -> Result<()> {
    let Some(metadata) = metadata else {
        return Ok(());
    };
    let available = max_heap_bytes
        .checked_sub(retained_bytes)
        .ok_or(Error::BudgetExceeded("recovery metadata compression"))?;
    builder.write_metadata_with_budget(metadata, available)
}

#[allow(clippy::result_large_err)]
fn finish_pages(pages: PageSet, result: Result<()>) -> BuildResult {
    pages.finish(result).map_err(|failure| BuildFailure {
        cause: failure.cause,
        scratch: failure.cleanup,
    })
}

fn after_cleanup(cause: Error, scratch: &Option<ScratchCleanup>) -> BuildFailure {
    BuildFailure {
        cause,
        scratch: scratch.clone(),
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
