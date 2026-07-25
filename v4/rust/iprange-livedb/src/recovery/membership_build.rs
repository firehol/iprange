//! Canonical immutable output from one membership recovery analysis.

use std::fs::File;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::immutable_output::{Builder, Finished};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_tree::Record;

#[cfg(any(unix, windows))]
use super::external_sort::{self, ExternalSortFailure};
use super::membership::{analyze, MembershipAnalysis};
use super::membership_output::{Components, MembershipKey};
use super::page_set::PageSet;
use super::range_build::{buffer_fits, events, require_count, reserve};
use super::range_scan;
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
    file: &File,
    source_meta: MetaV4,
    builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<MembershipConstruction, MembershipConstructionFailure> {
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
fn build<K: MembershipKey, S: RecoverySink>(
    file: &File,
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
    let context = BuildContext {
        file,
        meta: source_meta,
        budget,
        cancellation,
        memberships: &memberships,
        tables: &tables,
    };
    let mut reporter = Reporter::resume(report, sink);
    let result = if ordered {
        build_ordered::<K, S>(
            context,
            &mut builder,
            &mut reporter,
            readable_records,
            pages.take().expect("analysis retains page ownership"),
        )
    } else {
        build_sorted::<K, S>(
            context,
            &mut builder,
            &mut reporter,
            readable_records,
            retained,
            pages.take().expect("analysis retains page ownership"),
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
    memberships: &'a super::membership_index::MembershipIndex,
    tables: &'a super::tables::Tables,
}

#[allow(clippy::result_large_err)]
fn build_ordered<K: MembershipKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    mut pages: PageSet,
) -> BuildResult {
    let scan = (|| {
        pages.reset()?;
        let mut components = Components::<S, K>::new(
            context.file,
            context.meta,
            context.memberships,
            context.tables,
            builder,
            reporter,
            context.cancellation,
        );
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
    finish_pages(pages, scan)
}

#[allow(clippy::result_large_err)]
fn build_sorted<K: MembershipKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    retained: u64,
    pages: PageSet,
) -> BuildResult {
    let total = match retained.checked_add(pages.retained_bytes()) {
        Some(total) => total,
        None => {
            return finish_pages(
                pages,
                Err(Error::ArithmeticOverflow("recovery retained heap")),
            )
        }
    };
    match buffer_fits::<K>(readable_records, total, context.budget) {
        Ok(_) => {
            build_in_memory::<K, S>(context, builder, reporter, readable_records, total, pages)
        }
        Err(Error::BudgetExceeded(_)) => build_external::<K, S>(
            context,
            builder,
            reporter,
            readable_records,
            retained,
            pages,
        ),
        Err(cause) => finish_pages(pages, Err(cause)),
    }
}

#[allow(clippy::result_large_err)]
fn build_in_memory<K: MembershipKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    retained: u64,
    mut pages: PageSet,
) -> BuildResult {
    let available = context
        .budget
        .max_heap_bytes
        .checked_sub(retained)
        .ok_or(Error::BudgetExceeded("recovery unordered ranges"));
    let mut records = match available.and_then(|bytes| reserve::<K>(readable_records, bytes)) {
        Ok(records) => records,
        Err(cause) => return finish_pages(pages, Err(cause)),
    };
    let scan = (|| {
        pages.reset()?;
        let mut events = events(false, |record| {
            records.push(record);
            Ok(())
        });
        range_scan::scan(
            context.file,
            context.meta,
            &mut pages,
            context.cancellation,
            &mut events,
        )?;
        require_count(events.readable_records(), readable_records)?;
        records.sort_unstable_by(|left, right| {
            (left.from, left.to, left.value).cmp(&(right.from, right.to, right.value))
        });
        Ok(())
    })();
    if let Err(cause) = scan {
        drop(records);
        return finish_pages(pages, Err(cause));
    }
    let scratch = finish_pages(pages, Ok(()))?;
    emit_sorted(context, builder, reporter, records)
        .map_err(|cause| after_cleanup(cause, &scratch))?;
    Ok(scratch)
}

fn emit_sorted<K: MembershipKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    records: Vec<Record<K>>,
) -> Result<()> {
    let mut components = Components::<S, K>::new(
        context.file,
        context.meta,
        context.memberships,
        context.tables,
        builder,
        reporter,
        context.cancellation,
    );
    for record in records {
        context.cancellation.check()?;
        components.push(record)?;
    }
    components.finish()
}

#[cfg(any(unix, windows))]
#[allow(clippy::result_large_err)]
fn build_external<K: MembershipKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    retained: u64,
    pages: PageSet,
) -> BuildResult {
    let mut components = Components::<S, K>::new(
        context.file,
        context.meta,
        context.memberships,
        context.tables,
        builder,
        reporter,
        context.cancellation,
    );
    let cleanup = external_sort::sort_and_emit::<K>(
        context.file,
        external_sort::SortRequest {
            meta: context.meta,
            budget: context.budget,
            retained_heap_bytes: retained,
            readable_records,
            cancellation: context.cancellation,
            initial_area: context
                .tables
                .scratch_region()
                .map(|(slot, base)| external_sort::SortArea::new(slot, base)),
        },
        pages,
        |record| components.push(record),
    )
    .map_err(external_failure)?;
    components.finish().map_err(|cause| BuildFailure {
        cause,
        scratch: Some(cleanup.clone()),
    })?;
    Ok(Some(cleanup))
}

#[cfg(not(any(unix, windows)))]
#[allow(clippy::result_large_err)]
fn build_external<K: MembershipKey, S: RecoverySink>(
    _context: BuildContext<'_>,
    _builder: &mut Builder,
    _reporter: &mut Reporter<'_, S>,
    _readable_records: u64,
    _retained: u64,
    pages: PageSet,
) -> BuildResult {
    finish_pages(
        pages,
        Err(Error::Unsupported(
            "external recovery sorting is not implemented on this platform",
        )),
    )
}

#[cfg(any(unix, windows))]
fn external_failure(error: ExternalSortFailure) -> BuildFailure {
    BuildFailure {
        cause: error.cause,
        scratch: error.cleanup,
    }
}

fn retained_bytes(tables: &super::tables::Tables, metadata: &Option<Vec<u8>>) -> Result<u64> {
    tables
        .retained_bytes()
        .checked_add(retained_metadata_bytes(metadata))
        .ok_or(Error::ArithmeticOverflow("recovery retained heap"))
}

fn retained_metadata_bytes(metadata: &Option<Vec<u8>>) -> u64 {
    metadata.as_ref().map_or(0, |value| value.capacity() as u64)
}

fn write_metadata(
    builder: &mut Builder,
    metadata: Option<&[u8]>,
    max_heap_bytes: u64,
    retained: u64,
) -> Result<()> {
    let Some(metadata) = metadata else {
        return Ok(());
    };
    let available = max_heap_bytes
        .checked_sub(retained)
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

fn failed_pages(pages: PageSet, cause: Error) -> BuildFailure {
    match finish_pages(pages, Err(cause)) {
        Err(failure) => failure,
        Ok(_) => unreachable!("failed page scan cannot finish successfully"),
    }
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
