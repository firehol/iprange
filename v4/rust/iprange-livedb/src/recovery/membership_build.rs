//! Canonical immutable output from one membership recovery analysis.

use std::fs::File;
use std::mem::size_of;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::immutable_output::{Builder, Finished};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::range_tree::Record;
use crate::validation::ValidationReason;

#[cfg(target_os = "linux")]
use super::external_sort::{self, ExternalSortFailure};
use super::membership::{analyze, MembershipAnalysis};
use super::membership_output::{Components, MembershipKey};
use super::page_set::PageSet;
use super::range_scan::{self, RangeEvents};
use super::report::{RecoveryReport, RecoverySink, Reporter};
#[cfg(target_os = "linux")]
use super::scratch::ScratchCleanup;
use super::RecoveryBudget;

#[cfg(not(target_os = "linux"))]
#[derive(Clone, Debug)]
pub(crate) struct ScratchCleanup;

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
        metadata,
    } = analysis;
    for entry in catalog.entries() {
        if let Err(cause) = builder.push_feed(entry.name, entry.index) {
            return Err(failure(builder, cause, report, None));
        }
    }
    let retained = retained_bytes(&catalog, &memberships, &metadata);
    let context = BuildContext {
        file,
        meta: source_meta,
        budget,
        cancellation,
        memberships: &memberships,
    };
    let mut reporter = Reporter::resume(report, sink);
    let result = if ordered {
        build_ordered::<K, S>(
            context,
            &mut builder,
            &mut reporter,
            readable_records,
            retained,
        )
        .map(|()| None)
        .map_err(BuildFailure::plain)
    } else {
        build_sorted::<K, S>(
            context,
            &mut builder,
            &mut reporter,
            readable_records,
            retained,
        )
    };
    drop(catalog);
    drop(memberships);
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
    memberships: &'a super::membership_index::MembershipIndex,
}

fn build_ordered<K: MembershipKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    retained: u64,
) -> Result<()> {
    let heap = context
        .budget
        .max_heap_bytes
        .checked_sub(retained)
        .ok_or(Error::BudgetExceeded("recovery page-ownership table"))?;
    let mut pages = page_set(context.file, context.meta, heap)?;
    let mut components = Components::<S, K>::new(
        context.file,
        context.meta,
        context.memberships,
        builder,
        reporter,
        context.cancellation,
    );
    {
        let mut events = build_events(true, |record| components.push(record));
        range_scan::scan(
            context.file,
            context.meta,
            &mut pages,
            context.cancellation,
            &mut events,
        )?;
        require_count(events.readable_records, readable_records)?;
    }
    components.finish()
}

#[allow(clippy::result_large_err)]
fn build_sorted<K: MembershipKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    retained: u64,
) -> BuildResult {
    match range_buffer_bytes::<K>(readable_records, retained, context.budget) {
        Ok(_) => {
            let records = collect_sorted::<K>(context, readable_records, retained)
                .map_err(BuildFailure::plain)?;
            emit_sorted(context, builder, reporter, records).map_err(BuildFailure::plain)?;
            Ok(None)
        }
        Err(Error::BudgetExceeded(_)) => {
            build_external::<K, S>(context, builder, reporter, readable_records, retained)
        }
        Err(cause) => Err(BuildFailure::plain(cause)),
    }
}

fn collect_sorted<K: MembershipKey>(
    context: BuildContext<'_>,
    readable_records: u64,
    retained: u64,
) -> Result<Vec<Record<K>>> {
    let record_bytes = range_buffer_bytes::<K>(readable_records, retained, context.budget)?;
    let mut records = reserve_ranges::<K>(readable_records)?;
    let heap = context
        .budget
        .max_heap_bytes
        .checked_sub(retained)
        .and_then(|value| value.checked_sub(record_bytes))
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
    require_count(events.readable_records, readable_records)?;
    records.sort_unstable_by(|left, right| {
        (left.from, left.to, left.value).cmp(&(right.from, right.to, right.value))
    });
    Ok(records)
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

#[cfg(target_os = "linux")]
#[allow(clippy::result_large_err)]
fn build_external<K: MembershipKey, S: RecoverySink>(
    context: BuildContext<'_>,
    builder: &mut Builder,
    reporter: &mut Reporter<'_, S>,
    readable_records: u64,
    retained: u64,
) -> BuildResult {
    let mut components = Components::<S, K>::new(
        context.file,
        context.meta,
        context.memberships,
        builder,
        reporter,
        context.cancellation,
    );
    let cleanup = external_sort::sort_and_emit::<K>(
        context.file,
        context.meta,
        context.budget,
        retained,
        readable_records,
        context.cancellation,
        |record| components.push(record),
    )
    .map_err(external_failure)?;
    components.finish().map_err(|cause| BuildFailure {
        cause,
        scratch: Some(cleanup.clone()),
    })?;
    Ok(Some(cleanup))
}

#[cfg(not(target_os = "linux"))]
#[allow(clippy::result_large_err)]
fn build_external<K: MembershipKey, S: RecoverySink>(
    _context: BuildContext<'_>,
    _builder: &mut Builder,
    _reporter: &mut Reporter<'_, S>,
    _readable_records: u64,
    _retained: u64,
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

fn range_buffer_bytes<K: IpKey>(
    records: u64,
    retained: u64,
    budget: &RecoveryBudget,
) -> Result<u64> {
    let bytes = records
        .checked_mul(size_of::<Record<K>>() as u64)
        .ok_or(Error::ArithmeticOverflow("recovery range buffer"))?;
    if bytes
        .checked_add(retained)
        .is_some_and(|total| total <= budget.max_heap_bytes)
    {
        Ok(bytes)
    } else {
        Err(Error::BudgetExceeded("recovery unordered ranges"))
    }
}

fn reserve_ranges<K: IpKey>(records: u64) -> Result<Vec<Record<K>>> {
    let length =
        usize::try_from(records).map_err(|_| Error::BudgetExceeded("recovery unordered ranges"))?;
    let mut output = Vec::new();
    output
        .try_reserve_exact(length)
        .map_err(|_| Error::BudgetExceeded("recovery unordered ranges"))?;
    Ok(output)
}

fn retained_bytes(
    catalog: &super::catalog::Catalog,
    memberships: &super::membership_index::MembershipIndex,
    metadata: &Option<Vec<u8>>,
) -> u64 {
    catalog
        .retained_bytes()
        .saturating_add(memberships.retained_bytes())
        .saturating_add(retained_metadata_bytes(metadata))
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

fn page_set(file: &File, meta: MetaV4, heap: u64) -> Result<PageSet> {
    let physical_pages = file.metadata()?.len() / PAGE_SIZE as u64;
    PageSet::new(heap, meta.page_count.min(physical_pages))
}

fn require_count(actual: u64, expected: u64) -> Result<()> {
    if actual == expected {
        Ok(())
    } else {
        Err(Error::RecoveryCandidateChanged)
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

#[cfg(test)]
#[path = "membership_tests.rs"]
mod tests;
