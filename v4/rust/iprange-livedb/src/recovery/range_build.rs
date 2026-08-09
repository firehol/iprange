use std::mem::size_of;

use crate::cancellation::CancellationToken;
use crate::contract::MetaV4;
use crate::error::{Error, Result};
use crate::immutable_output::Builder;
use crate::key::IpKey;
use crate::mapping::Mapping;
use crate::range_tree::Record;
use crate::validation::{ValidationObject, ValidationReason};

use super::direct_output::DirectKey;
#[cfg(any(unix, windows))]
use super::external_sort::{self, ExternalSortFailure, SortArea};
use super::page_set::PageSet;
use super::range_scan::RangeEvents;
use super::report::{page_interval, RecoverySink, Reporter, Unknown};
use super::{RecoveryBudget, ScratchCleanup};

pub(super) type BuildResult = std::result::Result<Option<ScratchCleanup>, BuildFailure>;

pub(super) struct BuildFailure {
    pub(super) cause: Error,
    pub(super) scratch: Option<ScratchCleanup>,
}

pub(super) trait RangeOutput<K> {
    fn push(&mut self, record: Record<K>) -> Result<()>;
    fn finish(&mut self) -> Result<()>;
}

#[derive(Clone, Copy)]
pub(super) struct SortReuse {
    #[cfg(any(unix, windows))]
    area: Option<SortArea>,
}

impl SortReuse {
    pub(super) const fn none() -> Self {
        Self {
            #[cfg(any(unix, windows))]
            area: None,
        }
    }

    #[cfg(any(unix, windows))]
    pub(super) fn area(value: Option<(super::scratch::ScratchSlot, u64)>) -> Self {
        Self {
            area: value.map(|(slot, base)| SortArea::new(slot, base)),
        }
    }
}

#[derive(Clone, Copy)]
pub(super) struct RangeBuild<'a> {
    pub(super) mapping: &'a Mapping,
    pub(super) meta: MetaV4,
    pub(super) budget: &'a RecoveryBudget,
    pub(super) cancellation: &'a CancellationToken,
    pub(super) readable_records: u64,
    pub(super) ordered: bool,
    pub(super) retained_heap_bytes: u64,
    pub(super) sort_reuse: SortReuse,
}

#[allow(clippy::result_large_err)]
pub(super) fn build_ranges<K: DirectKey, O: RangeOutput<K>>(
    request: RangeBuild<'_>,
    pages: PageSet,
    output: &mut O,
) -> BuildResult {
    if request.ordered {
        build_ordered(request, pages, output)
    } else {
        build_sorted(request, pages, output)
    }
}

#[allow(clippy::result_large_err)]
fn build_ordered<K: DirectKey, O: RangeOutput<K>>(
    request: RangeBuild<'_>,
    mut pages: PageSet,
    output: &mut O,
) -> BuildResult {
    let scan = (|| {
        pages.reset()?;
        let mut events = events(true, |record| output.push(record));
        super::range_scan::scan(
            request.mapping,
            request.meta,
            &mut pages,
            request.cancellation,
            &mut events,
        )?;
        require_count(events.readable_records(), request.readable_records)?;
        output.finish()
    })();
    finish_pages(pages, scan)
}

#[allow(clippy::result_large_err)]
fn build_sorted<K: DirectKey, O: RangeOutput<K>>(
    request: RangeBuild<'_>,
    pages: PageSet,
    output: &mut O,
) -> BuildResult {
    let retained = match request
        .retained_heap_bytes
        .checked_add(pages.retained_bytes())
    {
        Some(retained) => retained,
        None => {
            return finish_pages(
                pages,
                Err(Error::ArithmeticOverflow("recovery retained heap")),
            )
        }
    };
    match buffer_fits::<K>(request.readable_records, retained, request.budget) {
        Ok(_) => build_in_memory(request, retained, pages, output),
        Err(Error::BudgetExceeded(_)) => build_external(request, pages, output),
        Err(cause) => finish_pages(pages, Err(cause)),
    }
}

#[allow(clippy::result_large_err)]
fn build_in_memory<K: DirectKey, O: RangeOutput<K>>(
    request: RangeBuild<'_>,
    retained: u64,
    mut pages: PageSet,
    output: &mut O,
) -> BuildResult {
    let available = request
        .budget
        .max_heap_bytes
        .checked_sub(retained)
        .ok_or(Error::BudgetExceeded("recovery unordered ranges"));
    let mut records =
        match available.and_then(|bytes| reserve::<K>(request.readable_records, bytes)) {
            Ok(records) => records,
            Err(cause) => return finish_pages(pages, Err(cause)),
        };
    let scan = (|| {
        pages.reset()?;
        let mut events = events(false, |record| {
            records.push(record);
            Ok(())
        });
        super::range_scan::scan(
            request.mapping,
            request.meta,
            &mut pages,
            request.cancellation,
            &mut events,
        )?;
        require_count(events.readable_records(), request.readable_records)?;
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
    for record in records {
        output
            .push(record)
            .map_err(|cause| after_cleanup(cause, &scratch))?;
    }
    output
        .finish()
        .map_err(|cause| after_cleanup(cause, &scratch))?;
    Ok(scratch)
}

#[cfg(any(unix, windows))]
#[allow(clippy::result_large_err)]
fn build_external<K: DirectKey, O: RangeOutput<K>>(
    request: RangeBuild<'_>,
    pages: PageSet,
    output: &mut O,
) -> BuildResult {
    let cleanup = external_sort::sort_and_emit::<K>(
        request.mapping,
        external_sort::SortRequest {
            meta: request.meta,
            budget: request.budget,
            retained_heap_bytes: request.retained_heap_bytes,
            readable_records: request.readable_records,
            cancellation: request.cancellation,
            initial_area: request.sort_reuse.area,
        },
        pages,
        |record| output.push(record),
    )
    .map_err(external_failure)?;
    output.finish().map_err(|cause| BuildFailure {
        cause,
        scratch: Some(cleanup.clone()),
    })?;
    Ok(Some(cleanup))
}

#[cfg(not(any(unix, windows)))]
fn build_external<K: DirectKey, O: RangeOutput<K>>(
    _request: RangeBuild<'_>,
    pages: PageSet,
    _output: &mut O,
) -> BuildResult {
    finish_pages(
        pages,
        Err(Error::Unsupported(
            "external recovery sorting is not implemented on this platform",
        )),
    )
}

pub(super) fn buffer_fits<K: IpKey>(
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

pub(super) fn reserve<K: IpKey>(records: u64, max_retained_bytes: u64) -> Result<Vec<Record<K>>> {
    let length =
        usize::try_from(records).map_err(|_| Error::BudgetExceeded("recovery unordered ranges"))?;
    let mut output = Vec::new();
    output
        .try_reserve_exact(length)
        .map_err(|_| Error::BudgetExceeded("recovery unordered ranges"))?;
    let retained = (output.capacity() as u64)
        .checked_mul(size_of::<Record<K>>() as u64)
        .ok_or(Error::ArithmeticOverflow("recovery range buffer"))?;
    if retained > max_retained_bytes {
        return Err(Error::BudgetExceeded("recovery unordered ranges"));
    }
    Ok(output)
}

pub(super) fn require_count(actual: u64, expected: u64) -> Result<()> {
    if actual == expected {
        Ok(())
    } else {
        Err(Error::RecoveryCandidateChanged)
    }
}

pub(super) fn analyze_ranges<K: IpKey, S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
) -> Result<(u64, bool)> {
    let mut events = AnalysisEvents::<S, K> {
        reporter,
        previous_from: None,
        readable_records: 0,
        ordered: true,
    };
    super::range_scan::scan(mapping, meta, pages, cancellation, &mut events)?;
    Ok((events.readable_records, events.ordered))
}

struct AnalysisEvents<'a, 'b, S, K> {
    reporter: &'a mut Reporter<'b, S>,
    previous_from: Option<K>,
    readable_records: u64,
    ordered: bool,
}

impl<K: IpKey, S: RecoverySink> RangeEvents<K> for AnalysisEvents<'_, '_, S, K> {
    fn page_accepted(&mut self) -> Result<()> {
        self.reporter.page_accepted()
    }

    fn page_rejected(&mut self, io_unreadable: bool) -> Result<()> {
        self.reporter.page_rejected(io_unreadable)
    }

    fn unknown(
        &mut self,
        reason: ValidationReason,
        page: Option<u32>,
        unbounded: bool,
    ) -> Result<()> {
        self.reporter.unknown(Unknown {
            reason,
            object: ValidationObject::RangeTree,
            page_number: page,
            physical_bytes: page.map(page_interval),
            address_fence: None,
            contributes_to_possible_span: false,
            has_unbounded_extent: unbounded,
        })
    }

    fn range(&mut self, _page: u32, record: Option<Record<K>>) -> Result<()> {
        self.reporter.range_examined()?;
        let Some(record) = record else {
            return self.reporter.range_rejected_without_bounds();
        };
        if self
            .previous_from
            .is_some_and(|previous| previous >= record.from)
        {
            self.ordered = false;
        }
        self.previous_from = Some(record.from);
        self.readable_records = self
            .readable_records
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("recovery readable ranges"))?;
        Ok(())
    }
}

pub(super) fn retained_metadata_bytes(metadata: &Option<Vec<u8>>) -> u64 {
    metadata.as_ref().map_or(0, |value| value.capacity() as u64)
}

pub(super) fn write_metadata(
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
pub(super) fn finish_pages(pages: PageSet, result: Result<()>) -> BuildResult {
    pages.finish(result).map_err(|failure| BuildFailure {
        cause: failure.cause,
        scratch: failure.cleanup,
    })
}

pub(super) fn failed_pages(pages: PageSet, cause: Error) -> BuildFailure {
    match finish_pages(pages, Err(cause)) {
        Err(failure) => failure,
        Ok(_) => unreachable!("failed page scan cannot finish successfully"),
    }
}

pub(super) fn after_cleanup(cause: Error, scratch: &Option<ScratchCleanup>) -> BuildFailure {
    BuildFailure {
        cause,
        scratch: scratch.clone(),
    }
}

#[cfg(any(unix, windows))]
pub(super) fn external_failure(error: ExternalSortFailure) -> BuildFailure {
    BuildFailure {
        cause: error.cause,
        scratch: error.cleanup,
    }
}

pub(super) fn events<K, F>(ordered: bool, emit: F) -> Events<K, F> {
    Events {
        ordered,
        previous_from: None,
        readable_records: 0,
        emit,
    }
}

pub(super) struct Events<K, F> {
    ordered: bool,
    previous_from: Option<K>,
    readable_records: u64,
    emit: F,
}

impl<K, F> Events<K, F> {
    pub(super) fn readable_records(&self) -> u64 {
        self.readable_records
    }
}

impl<K: IpKey, F: FnMut(Record<K>) -> Result<()>> RangeEvents<K> for Events<K, F> {
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
