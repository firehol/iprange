use std::mem::size_of;

use crate::cancellation::CancellationToken;
use crate::contract::MetaV4;
use crate::error::{Error, Result};
use crate::immutable_output::Builder;
use crate::key::IpKey;
use crate::mapping::Mapping;
use crate::range_tree::Record;
use crate::validation::{ValidationObject, ValidationReason};

#[cfg(any(unix, windows))]
use super::external_sort::ExternalSortFailure;
use super::page_set::PageSet;
use super::range_scan::RangeEvents;
use super::report::{page_interval, RecoverySink, Reporter, Unknown};
use super::{RecoveryBudget, ScratchCleanup};

pub(super) type BuildResult = std::result::Result<Option<ScratchCleanup>, BuildFailure>;

pub(super) struct BuildFailure {
    pub(super) cause: Error,
    pub(super) scratch: Option<ScratchCleanup>,
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
