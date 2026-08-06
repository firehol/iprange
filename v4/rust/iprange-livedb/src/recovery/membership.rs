//! Analysis of one recovery-readable membership generation.

use std::mem::size_of;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::metadata;
use crate::range_tree::Record;
use crate::validation::{PhysicalByteInterval, ValidationObject, ValidationReason};

use super::catalog::{self, Catalog};
use super::membership_index::{self, MembershipIndex};
use super::metadata as recovery_metadata;
use super::page_set::PageSet;
use super::range_scan::{self, RangeEvents};
use super::report::{RecoveryReport, RecoverySink, Reporter, Unknown};
use super::tables::{Counts, Tables};
use super::{RecoveryBudget, ScratchCleanup};

pub(crate) struct MembershipAnalysis {
    pub(crate) report: RecoveryReport,
    pub(crate) readable_records: u64,
    pub(crate) ordered: bool,
    pub(crate) catalog: Catalog,
    pub(crate) memberships: MembershipIndex,
    pub(crate) tables: Tables,
    pub(crate) metadata: Option<Vec<u8>>,
    pub(crate) pages: PageSet,
}

pub(crate) struct MembershipAnalysisFailure {
    pub(crate) cause: Error,
    pub(crate) report: RecoveryReport,
    pub(crate) scratch: Option<ScratchCleanup>,
}

#[allow(clippy::result_large_err)]
pub(crate) fn analyze<S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<MembershipAnalysis, MembershipAnalysisFailure> {
    if let Err(cause) = budget.validate().and_then(|()| cancellation.check()) {
        return Err(failure(cause, RecoveryReport::default(), None));
    }
    if meta.value_kind != ValueKind::Membership {
        return Err(failure(
            Error::WrongValueKind("membership recovery requires membership values"),
            RecoveryReport::default(),
            None,
        ));
    }
    let physical_pages = mapping.len() / PAGE_SIZE as u64;
    let mut reporter = Reporter::new(sink);
    let page_heap = budget.max_heap_bytes / 2;
    let mut pages =
        match PageSet::for_recovery(page_heap, meta.page_count.min(physical_pages), meta, budget) {
            Ok(pages) => pages,
            Err(cause) => return Err(failure(cause, reporter.finish(), None)),
        };
    let result = analyze_graphs(
        mapping,
        meta,
        budget,
        cancellation,
        &mut pages,
        &mut reporter,
    );
    let report = reporter.finish();
    match result {
        Ok((readable_records, ordered, catalog, memberships, tables, metadata)) => {
            Ok(MembershipAnalysis {
                report,
                readable_records,
                ordered,
                catalog,
                memberships,
                tables,
                metadata,
                pages,
            })
        }
        Err(cause) => Err(failure_with_pages(pages, cause, report)),
    }
}

type Graphs = (u64, bool, Catalog, MembershipIndex, Tables, Option<Vec<u8>>);

fn analyze_graphs<S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    pages: &mut PageSet,
    reporter: &mut Reporter<'_, S>,
) -> Result<Graphs> {
    let mut tables = prepare_tables(mapping, meta, budget, cancellation, pages)?;
    let (catalog, memberships) =
        recover_tables(mapping, meta, cancellation, pages, reporter, &mut tables)?;
    let ranges = match meta.address_family {
        AddressFamily::Ipv4 => {
            analyze_ranges::<Ipv4Key, S>(mapping, meta, pages, cancellation, reporter)
        }
        AddressFamily::Ipv6 => {
            analyze_ranges::<Ipv6Key, S>(mapping, meta, pages, cancellation, reporter)
        }
    }?;
    let metadata = read_metadata(
        mapping,
        meta,
        budget,
        cancellation,
        pages,
        reporter,
        &tables,
    )?;
    Ok((ranges.0, ranges.1, catalog, memberships, tables, metadata))
}

fn prepare_tables(
    mapping: &Mapping,
    meta: MetaV4,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    pages: &mut PageSet,
) -> Result<Tables> {
    let counts = Counts {
        catalog: catalog::count(mapping, meta, pages, cancellation)?,
        memberships: membership_index::count(mapping, meta, pages, cancellation)?,
    };
    pages.reset()?;
    Tables::allocate(counts, pages, budget, required_table_heap_reserve(meta)?)
}

fn recover_tables<S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    cancellation: &CancellationToken,
    pages: &mut PageSet,
    reporter: &mut Reporter<'_, S>,
    tables: &mut Tables,
) -> Result<(Catalog, MembershipIndex)> {
    let catalog = catalog::recover(mapping, meta, pages, tables, cancellation, reporter)?;
    let memberships = membership_index::recover(
        mapping,
        meta,
        &catalog,
        pages,
        tables,
        cancellation,
        reporter,
    )?;
    Ok((catalog, memberships))
}

fn read_metadata<S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    pages: &mut PageSet,
    reporter: &mut Reporter<'_, S>,
    tables: &Tables,
) -> Result<Option<Vec<u8>>> {
    let table_bytes = tables.retained_bytes();
    let metadata_heap = budget
        .max_heap_bytes
        .checked_sub(table_bytes)
        .ok_or(Error::BudgetExceeded("recovery metadata output"))?;
    recovery_metadata::read(mapping, meta, pages, metadata_heap, cancellation, reporter)
}

fn required_table_heap_reserve(meta: MetaV4) -> Result<u64> {
    let metadata = if meta.metadata_root == 0 {
        0
    } else {
        meta.metadata_uncompressed_len
            .checked_add(metadata::DEFLATE_HEAP_OVERHEAD)
            .ok_or(Error::ArithmeticOverflow("recovery metadata heap"))?
    };
    let range = if meta.range_root == 0 {
        0
    } else {
        match meta.address_family {
            AddressFamily::Ipv4 => size_of::<Record<Ipv4Key>>() as u64,
            AddressFamily::Ipv6 => size_of::<Record<Ipv6Key>>() as u64,
        }
    };
    metadata
        .checked_add(range)
        .ok_or(Error::ArithmeticOverflow("recovery table heap reserve"))
}

fn analyze_ranges<K: IpKey, S: RecoverySink>(
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
    range_scan::scan(mapping, meta, pages, cancellation, &mut events)?;
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

fn failure(
    cause: Error,
    report: RecoveryReport,
    scratch: Option<ScratchCleanup>,
) -> MembershipAnalysisFailure {
    MembershipAnalysisFailure {
        cause,
        report,
        scratch,
    }
}

fn failure_with_pages(
    pages: PageSet,
    cause: Error,
    report: RecoveryReport,
) -> MembershipAnalysisFailure {
    match pages.finish(Err(cause)) {
        Err(failed) => failure(failed.cause, report, failed.cleanup),
        Ok(_) => unreachable!("failed analysis cannot finish successfully"),
    }
}
