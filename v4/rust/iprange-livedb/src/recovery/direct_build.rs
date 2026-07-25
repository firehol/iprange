//! Construction of a canonical direct database from one recovery analysis.

use std::fs::File;
use std::mem::size_of;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::immutable_output::{Builder, Finished};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::range_tree::Record;
use crate::validation::ValidationReason;

use super::direct::{analyze, DirectAnalysis};
use super::direct_output::{Components, DirectKey};
#[cfg(target_os = "linux")]
use super::external_sort::{self, ExternalSortFailure};
use super::page_set::PageSet;
use super::range_scan::{self, RangeEvents};
use super::report::{RecoveryReport, RecoverySink, Reporter};
#[cfg(target_os = "linux")]
use super::scratch::ScratchCleanup;
use super::RecoveryBudget;

#[cfg(not(target_os = "linux"))]
#[derive(Debug)]
pub(crate) struct ScratchCleanup;

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
        Err(error) => return Err(failure(builder, error.cause, error.report, None)),
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
        )
        .map(|()| None)
        .map_err(BuildFailure::plain)
    } else {
        build_sorted::<K, S>(
            context,
            &mut builder,
            &mut reporter,
            readable_records,
            &metadata,
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

impl BuildFailure {
    fn plain(cause: Error) -> Self {
        Self {
            cause,
            scratch: None,
        }
    }
}

#[derive(Clone, Copy)]
struct BuildContext<'a> {
    file: &'a File,
    meta: MetaV4,
    budget: &'a RecoveryBudget,
    cancellation: &'a CancellationToken,
}

fn build_ordered<K: DirectKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    metadata: &Option<Vec<u8>>,
) -> Result<()> {
    let metadata_bytes = retained_bytes(metadata);
    let page_heap = match context.budget.max_heap_bytes.checked_sub(metadata_bytes) {
        Some(heap) => heap,
        None => return Err(Error::BudgetExceeded("recovery retained metadata")),
    };
    let mut pages = page_set(context.file, context.meta, page_heap)?;
    let mut components = Components::<S, K>::new(builder, reporter, context.cancellation);
    {
        let mut events = build_events(true, |record| components.push(record));
        range_scan::scan(
            context.file,
            context.meta,
            &mut pages,
            context.cancellation,
            &mut events,
        )?;
        if events.readable_records != readable_records {
            return Err(Error::RecoveryCandidateChanged);
        }
    }
    components.finish()?;
    write_metadata(
        builder,
        metadata.as_deref(),
        context.budget.max_heap_bytes,
        metadata_bytes,
    )
}

#[allow(clippy::result_large_err)]
fn build_sorted<K: DirectKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    metadata: &Option<Vec<u8>>,
) -> BuildResult {
    let metadata_bytes = retained_bytes(metadata);
    match range_buffer_bytes::<K>(readable_records, metadata_bytes, context.budget) {
        Ok(_) => {
            let records = collect_sorted::<K>(context, readable_records, metadata_bytes)
                .map_err(BuildFailure::plain)?;
            emit_sorted(context.cancellation, builder, reporter, records)
                .map_err(BuildFailure::plain)?;
            write_metadata(
                builder,
                metadata.as_deref(),
                context.budget.max_heap_bytes,
                metadata_bytes,
            )
            .map_err(BuildFailure::plain)?;
            Ok(None)
        }
        Err(Error::BudgetExceeded(_)) => build_external::<K, S>(
            context,
            builder,
            reporter,
            readable_records,
            metadata,
            metadata_bytes,
        ),
        Err(cause) => Err(BuildFailure::plain(cause)),
    }
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
) -> BuildResult {
    let mut components = Components::<S, K>::new(builder, reporter, context.cancellation);
    let cleanup = external_sort::sort_and_emit::<K>(
        context.file,
        context.meta,
        context.budget,
        metadata_bytes,
        readable_records,
        context.cancellation,
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
) -> BuildResult {
    Err(BuildFailure::plain(Error::Unsupported(
        "external recovery sorting is not implemented on this platform",
    )))
}

#[cfg(target_os = "linux")]
fn external_failure(error: ExternalSortFailure) -> BuildFailure {
    BuildFailure {
        cause: error.cause,
        scratch: error.cleanup,
    }
}

fn collect_sorted<K: DirectKey>(
    context: BuildContext<'_>,
    readable_records: u64,
    metadata_bytes: u64,
) -> Result<Vec<Record<K>>> {
    let record_bytes = range_buffer_bytes::<K>(readable_records, metadata_bytes, context.budget)?;
    let mut records = reserve_ranges::<K>(readable_records)?;
    let heap = context
        .budget
        .max_heap_bytes
        .checked_sub(record_bytes)
        .and_then(|value| value.checked_sub(metadata_bytes))
        .ok_or(Error::BudgetExceeded("recovery page-ownership table"))?;
    let mut pages = page_set(context.file, context.meta, heap)?;
    let mut events = build_events(false, |record| {
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
    if events.readable_records != readable_records {
        return Err(Error::RecoveryCandidateChanged);
    }
    records.sort_unstable_by(|left, right| {
        (left.from, left.to, left.value).cmp(&(right.from, right.to, right.value))
    });
    Ok(records)
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

fn range_buffer_bytes<K: IpKey>(
    readable_records: u64,
    metadata_bytes: u64,
    budget: &RecoveryBudget,
) -> Result<u64> {
    let bytes = readable_records
        .checked_mul(size_of::<Record<K>>() as u64)
        .ok_or(Error::ArithmeticOverflow("recovery range buffer"))?;
    if bytes
        .checked_add(metadata_bytes)
        .is_some_and(|total| total <= budget.max_heap_bytes)
    {
        Ok(bytes)
    } else {
        Err(Error::BudgetExceeded("recovery unordered ranges"))
    }
}

fn reserve_ranges<K: IpKey>(readable_records: u64) -> Result<Vec<Record<K>>> {
    let length = usize::try_from(readable_records)
        .map_err(|_| Error::BudgetExceeded("recovery unordered ranges"))?;
    let mut records = Vec::new();
    records
        .try_reserve_exact(length)
        .map_err(|_| Error::BudgetExceeded("recovery unordered ranges"))?;
    Ok(records)
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

fn page_set(file: &File, meta: MetaV4, heap: u64) -> Result<PageSet> {
    let physical_pages = file.metadata()?.len() / PAGE_SIZE as u64;
    PageSet::new(heap, meta.page_count.min(physical_pages))
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

fn build_events<K, F>(ordered: bool, emit: F) -> BuildEvents<K, F> {
    BuildEvents {
        ordered,
        previous_from: None,
        readable_records: 0,
        emit,
    }
}

struct BuildEvents<K, F> {
    ordered: bool,
    previous_from: Option<K>,
    readable_records: u64,
    emit: F,
}

impl<K: IpKey, F: FnMut(Record<K>) -> Result<()>> RangeEvents<K> for BuildEvents<K, F> {
    fn page_accepted(&mut self) -> Result<()> {
        Ok(())
    }

    fn page_rejected(&mut self, _io_unreadable: bool) -> Result<()> {
        Ok(())
    }

    fn unknown(
        &mut self,
        _reason: ValidationReason,
        _page: Option<u32>,
        _unbounded: bool,
    ) -> Result<()> {
        Ok(())
    }

    fn range(&mut self, _page: u32, record: Option<Record<K>>) -> Result<()> {
        let Some(record) = record else {
            return Ok(());
        };
        if self.ordered
            && self
                .previous_from
                .is_some_and(|previous| previous >= record.from)
        {
            return Err(Error::RecoveryCandidateChanged);
        }
        self.previous_from = Some(record.from);
        self.readable_records = self
            .readable_records
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("recovery readable ranges"))?;
        (self.emit)(record)
    }
}
