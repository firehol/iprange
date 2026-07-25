//! Analysis of one recovery-readable membership generation.

use std::fs::File;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::range_tree::Record;
use crate::validation::{PhysicalByteInterval, ValidationObject, ValidationReason};

use super::catalog::{self, Catalog};
use super::membership_index::{self, MembershipIndex};
use super::metadata as recovery_metadata;
use super::page_set::PageSet;
use super::range_scan::{self, RangeEvents};
use super::report::{RecoveryReport, RecoverySink, Reporter, Unknown};
use super::RecoveryBudget;

pub(crate) struct MembershipAnalysis {
    pub(crate) report: RecoveryReport,
    pub(crate) readable_records: u64,
    pub(crate) ordered: bool,
    pub(crate) catalog: Catalog,
    pub(crate) memberships: MembershipIndex,
    pub(crate) metadata: Option<Vec<u8>>,
}

pub(crate) struct MembershipAnalysisFailure {
    pub(crate) cause: Error,
    pub(crate) report: RecoveryReport,
}

#[allow(clippy::result_large_err)]
pub(crate) fn analyze<S: RecoverySink>(
    file: &File,
    meta: MetaV4,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<MembershipAnalysis, MembershipAnalysisFailure> {
    if let Err(cause) = budget.validate().and_then(|()| cancellation.check()) {
        return Err(failure(cause, RecoveryReport::default()));
    }
    if meta.value_kind != ValueKind::Membership {
        return Err(failure(
            Error::WrongMode("membership recovery requires membership values"),
            RecoveryReport::default(),
        ));
    }
    let physical_pages = match file.metadata() {
        Ok(metadata) => metadata.len() / PAGE_SIZE as u64,
        Err(cause) => return Err(failure(cause.into(), RecoveryReport::default())),
    };
    let mut reporter = Reporter::new(sink);
    let page_heap = budget.max_heap_bytes / 2;
    let mut pages = match PageSet::new(page_heap, meta.page_count.min(physical_pages)) {
        Ok(pages) => pages,
        Err(cause) => return Err(failure(cause, reporter.finish())),
    };
    let result = analyze_graphs(file, meta, budget, cancellation, &mut pages, &mut reporter);
    let report = reporter.finish();
    match result {
        Ok((readable_records, ordered, catalog, memberships, metadata)) => Ok(MembershipAnalysis {
            report,
            readable_records,
            ordered,
            catalog,
            memberships,
            metadata,
        }),
        Err(cause) => Err(failure(cause, report)),
    }
}

type Graphs = (u64, bool, Catalog, MembershipIndex, Option<Vec<u8>>);

fn analyze_graphs<S: RecoverySink>(
    file: &File,
    meta: MetaV4,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    pages: &mut PageSet,
    reporter: &mut Reporter<'_, S>,
) -> Result<Graphs> {
    let catalog = catalog::recover(
        file,
        meta,
        pages,
        budget.max_heap_bytes,
        cancellation,
        reporter,
    )?;
    let memberships = membership_index::recover(
        file,
        meta,
        &catalog,
        pages,
        budget.max_heap_bytes,
        cancellation,
        reporter,
    )?;
    let ranges = match meta.address_family {
        AddressFamily::Ipv4 => {
            analyze_ranges::<Ipv4Key, S>(file, meta, pages, cancellation, reporter)
        }
        AddressFamily::Ipv6 => {
            analyze_ranges::<Ipv6Key, S>(file, meta, pages, cancellation, reporter)
        }
    }?;
    let table_bytes = catalog
        .retained_bytes()
        .checked_add(memberships.retained_bytes())
        .ok_or(Error::ArithmeticOverflow("recovery membership heap"))?;
    let metadata_heap = budget
        .max_heap_bytes
        .checked_sub(table_bytes)
        .ok_or(Error::BudgetExceeded("recovery metadata output"))?;
    let metadata =
        recovery_metadata::read(file, meta, pages, metadata_heap, cancellation, reporter)?;
    Ok((ranges.0, ranges.1, catalog, memberships, metadata))
}

fn analyze_ranges<K: IpKey, S: RecoverySink>(
    file: &File,
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
    range_scan::scan(file, meta, pages, cancellation, &mut events)?;
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
        emit(self.reporter, reason, page, unbounded)
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

fn emit<S: RecoverySink>(
    reporter: &mut Reporter<'_, S>,
    reason: ValidationReason,
    page: Option<u32>,
    unbounded: bool,
) -> Result<()> {
    reporter.unknown(Unknown {
        reason,
        object: ValidationObject::RangeTree,
        page_number: page,
        physical_bytes: page.map(page_interval),
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

fn failure(cause: Error, report: RecoveryReport) -> MembershipAnalysisFailure {
    MembershipAnalysisFailure { cause, report }
}
