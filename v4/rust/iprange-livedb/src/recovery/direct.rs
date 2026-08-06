use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::range_tree::Record;
use crate::validation::{PhysicalByteInterval, ValidationObject, ValidationReason};

use super::metadata as recovery_metadata;
use super::page_set::PageSet;
use super::range_scan::{self, RangeEvents};
use super::report::{RecoveryReport, RecoverySink, Reporter, Unknown};
use super::{RecoveryBudget, ScratchCleanup};

pub(crate) struct DirectAnalysis {
    pub(crate) report: RecoveryReport,
    pub(crate) readable_records: u64,
    pub(crate) ordered: bool,
    pub(super) metadata: Option<Vec<u8>>,
    pub(super) pages: PageSet,
}

pub(crate) struct DirectAnalysisFailure {
    pub(crate) cause: Error,
    pub(crate) report: RecoveryReport,
    pub(crate) scratch: Option<ScratchCleanup>,
}

// A partial report must survive sink, I/O, and budget failures without allocating then.
#[allow(clippy::result_large_err)]
pub(crate) fn analyze<S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<DirectAnalysis, DirectAnalysisFailure> {
    if let Err(cause) = budget.validate().and_then(|()| cancellation.check()) {
        return Err(analysis_failure(cause, RecoveryReport::default(), None));
    }
    if meta.value_kind != ValueKind::Direct {
        return Err(analysis_failure(
            Error::WrongValueKind("direct recovery requires direct values"),
            RecoveryReport::default(),
            None,
        ));
    }
    let physical_pages = mapping.len() / PAGE_SIZE as u64;
    let mut reporter = Reporter::new(sink);
    let mut pages = match PageSet::for_recovery(
        budget.max_heap_bytes,
        meta.page_count.min(physical_pages),
        meta,
        budget,
    ) {
        Ok(pages) => pages,
        Err(cause) => return Err(analysis_failure(cause, reporter.finish(), None)),
    };
    let ranges = match meta.address_family {
        AddressFamily::Ipv4 => {
            analyze_family::<Ipv4Key, S>(mapping, meta, &mut pages, cancellation, &mut reporter)
        }
        AddressFamily::Ipv6 => {
            analyze_family::<Ipv6Key, S>(mapping, meta, &mut pages, cancellation, &mut reporter)
        }
    };
    let result = ranges.and_then(|(readable_records, ordered)| {
        recovery_metadata::read(
            mapping,
            meta,
            &mut pages,
            budget.max_heap_bytes,
            cancellation,
            &mut reporter,
        )
        .map(|metadata| (readable_records, ordered, metadata))
    });
    let report = reporter.finish();
    match result {
        Ok((readable_records, ordered, metadata)) => Ok(DirectAnalysis {
            report,
            readable_records,
            ordered,
            metadata,
            pages,
        }),
        Err(cause) => Err(analysis_failure_with_pages(pages, cause, report)),
    }
}

fn analyze_family<K: IpKey, S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
) -> Result<(u64, bool)> {
    let mut events: AnalysisEvents<'_, '_, S, K> = AnalysisEvents {
        reporter,
        previous_from: None,
        readable_records: 0,
        ordered: true,
    };
    range_scan::scan(mapping, meta, pages, cancellation, &mut events)?;
    Ok((events.readable_records, events.ordered))
}

fn analysis_failure(
    cause: Error,
    report: RecoveryReport,
    scratch: Option<ScratchCleanup>,
) -> DirectAnalysisFailure {
    DirectAnalysisFailure {
        cause,
        report,
        scratch,
    }
}

fn analysis_failure_with_pages(
    pages: PageSet,
    cause: Error,
    report: RecoveryReport,
) -> DirectAnalysisFailure {
    match pages.finish(Err(cause)) {
        Err(failure) => analysis_failure(failure.cause, report, failure.cleanup),
        Ok(_) => unreachable!("failed analysis cannot finish successfully"),
    }
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
        emit_unknown(self.reporter, reason, page, unbounded)
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

fn emit_unknown<S: RecoverySink>(
    reporter: &mut Reporter<'_, S>,
    reason: ValidationReason,
    page_number: Option<u32>,
    unbounded: bool,
) -> Result<()> {
    reporter.unknown(Unknown {
        reason,
        object: ValidationObject::RangeTree,
        page_number,
        physical_bytes: page_number.map(page_interval),
        address_fence: None,
        contributes_to_possible_span: false,
        has_unbounded_extent: unbounded,
    })
}

fn page_interval(page: u32) -> PhysicalByteInterval {
    let start = u64::from(page) * PAGE_SIZE as u64;
    PhysicalByteInterval {
        start,
        end_exclusive: start + PAGE_SIZE as u64,
    }
}

#[cfg(test)]
#[path = "direct_tests.rs"]
mod tests;

#[cfg(all(test, target_os = "linux"))]
#[path = "direct_scratch_tests.rs"]
mod scratch_tests;
