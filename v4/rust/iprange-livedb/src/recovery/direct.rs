use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind, PAGE_SIZE};
use crate::error::Error;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;

use super::metadata as recovery_metadata;
use super::page_set::PageSet;
use super::range_build::analyze_ranges;
use super::report::{RecoveryReport, RecoverySink, Reporter};
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
            analyze_ranges::<Ipv4Key, S>(mapping, meta, &mut pages, cancellation, &mut reporter)
        }
        AddressFamily::Ipv6 => {
            analyze_ranges::<Ipv6Key, S>(mapping, meta, &mut pages, cancellation, &mut reporter)
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

#[cfg(test)]
#[path = "direct_tests.rs"]
mod tests;

#[cfg(all(test, target_os = "linux"))]
#[path = "direct_scratch_tests.rs"]
mod scratch_tests;
