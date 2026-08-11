//! Recovery of authoritative structured payloads by source-local ID.

use std::marker::PhantomData;

use crate::cancellation::CancellationToken;
use crate::contract::MetaV4;
use crate::error::{Error, Result};
use crate::mapping::{ByteSource, Mapping, PageView};
use crate::structured_value::codec::{self, PayloadCodec};
use crate::structured_value::{payload_digest, table};
use crate::validation::{ValidationObject, ValidationReason};

use super::membership_table::MembershipIndex;
use super::page_set::{PageClaim, PageSet};
use super::report::{emit_page_unknown as emit, RecoverySink, Reporter};
use super::structure_table::{Insert, Locator, StructureIndex};
use super::tables::Tables;

pub(crate) fn count<P: PayloadCodec>(
    mapping: &Mapping,
    meta: MetaV4,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
) -> Result<u64> {
    let mut events = Counter::<P> {
        meta,
        count: 0,
        marker: PhantomData,
    };
    scan::<P, _>(
        mapping,
        meta,
        meta.structure_id_root,
        pages,
        cancellation,
        &mut events,
    )?;
    Ok(events.count)
}

pub(crate) fn recover<P: PayloadCodec, S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    memberships: &MembershipIndex,
    pages: &mut PageSet,
    tables: &mut Tables,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
) -> Result<StructureIndex> {
    if meta.structure_kind() != Some(P::KIND) {
        return Err(Error::UnsupportedStructure(meta.structure_kind_code));
    }
    let mut entries = StructureIndex::new(tables, P::KIND);
    {
        let mut events = Events::<P, S> {
            meta,
            reporter,
            memberships,
            entries: &mut entries,
            tables,
            marker: PhantomData,
        };
        scan::<P, _>(
            mapping,
            meta,
            meta.structure_id_root,
            pages,
            cancellation,
            &mut events,
        )?;
    }
    reconcile_ids(&entries, tables, reporter)?;
    report_outcomes(&entries, tables, reporter)?;
    Ok(entries)
}

trait TableEvents {
    fn page_accepted(&mut self) -> Result<()>;
    fn page_rejected(&mut self, io_unreadable: bool) -> Result<()>;
    fn unknown(&mut self, reason: ValidationReason, page: Option<u32>) -> Result<()>;
    fn leaf<B: ByteSource>(&mut self, page: u32, expected_id: u64, cell: B) -> Result<()>;
}

struct Counter<P> {
    meta: MetaV4,
    count: u64,
    marker: PhantomData<P>,
}

impl<P: PayloadCodec> TableEvents for Counter<P> {
    fn page_accepted(&mut self) -> Result<()> {
        Ok(())
    }

    fn page_rejected(&mut self, _io_unreadable: bool) -> Result<()> {
        Ok(())
    }

    fn unknown(&mut self, _reason: ValidationReason, _page: Option<u32>) -> Result<()> {
        Ok(())
    }

    fn leaf<B: ByteSource>(&mut self, _page: u32, expected_id: u64, cell: B) -> Result<()> {
        if codec::decode_record::<P, _>(cell).is_ok_and(|record| {
            u64::from(record.id) == expected_id
                && expected_id < self.meta.structure_id_limit
                && payload_digest::<P>(&record.payload).is_ok_and(|digest| digest == record.digest)
        }) {
            self.count = self
                .count
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("recovery structure count"))?;
        }
        Ok(())
    }
}

struct Events<'a, 'b, 'c, P, S> {
    meta: MetaV4,
    reporter: &'a mut Reporter<'b, S>,
    memberships: &'a MembershipIndex,
    entries: &'a mut StructureIndex,
    tables: &'a mut Tables,
    marker: PhantomData<&'c P>,
}

impl<P: PayloadCodec, S: RecoverySink> TableEvents for Events<'_, '_, '_, P, S> {
    fn page_accepted(&mut self) -> Result<()> {
        self.reporter.page_accepted()
    }

    fn page_rejected(&mut self, io_unreadable: bool) -> Result<()> {
        self.reporter.page_rejected(io_unreadable)
    }

    fn unknown(&mut self, reason: ValidationReason, page: Option<u32>) -> Result<()> {
        emit(
            self.reporter,
            reason,
            ValidationObject::StructureDictionary,
            page,
        )
    }

    fn leaf<B: ByteSource>(&mut self, page: u32, expected_id: u64, cell: B) -> Result<()> {
        self.reporter.structure_examined()?;
        let Ok(record) = codec::decode_record::<P, _>(cell) else {
            return self.reporter.structure_rejected(1);
        };
        if u64::from(record.id) != expected_id || expected_id >= self.meta.structure_id_limit {
            self.reporter.structure_rejected(1)?;
            return emit(
                self.reporter,
                ValidationReason::StructureInvalid,
                ValidationObject::StructureDictionary,
                Some(page),
            );
        }
        if payload_digest::<P>(&record.payload)? != record.digest {
            self.reporter.structure_rejected(1)?;
            return emit(
                self.reporter,
                ValidationReason::StructureHashInvalid,
                ValidationObject::StructureDictionary,
                Some(page),
            );
        }
        let membership_id = P::membership_id(&record.payload);
        let rejected =
            membership_id != 0 && self.memberships.get(self.tables, membership_id)?.is_none();
        if rejected {
            emit(
                self.reporter,
                ValidationReason::StructureMembershipInvalid,
                ValidationObject::StructureDictionary,
                Some(page),
            )?;
        }
        self.entries.push(
            self.tables,
            Locator {
                id: record.id,
                membership_id,
                leaf_page: page,
                payload: record.payload,
                rejected,
            },
        )
    }
}

const MAX_DEPTH: usize = 4;

fn scan<P: PayloadCodec, E: TableEvents>(
    mapping: &Mapping,
    meta: MetaV4,
    root: u32,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
    events: &mut E,
) -> Result<()> {
    if root == 0 {
        return Ok(());
    }
    let level = table::required_level::<P>(meta.structure_id_limit)?;
    let mut path = [0; MAX_DEPTH];
    scan_node::<P, E>(
        ScanContext {
            mapping,
            meta,
            pages,
            cancellation,
        },
        root,
        level,
        0,
        &mut path,
        0,
        events,
    )
}

struct ScanContext<'a> {
    mapping: &'a Mapping,
    meta: MetaV4,
    pages: &'a mut PageSet,
    cancellation: &'a CancellationToken,
}

#[allow(clippy::too_many_arguments)]
fn scan_node<P: PayloadCodec, E: TableEvents>(
    mut context: ScanContext<'_>,
    page_number: u32,
    expected_level: u16,
    base: u64,
    path: &mut [u32; MAX_DEPTH],
    depth: usize,
    events: &mut E,
) -> Result<()> {
    context.cancellation.check()?;
    if !claim_page(&mut context, page_number, path, depth, events)? {
        return Ok(());
    }
    let Some((page, header)) = read_page::<P, E>(&context, page_number, expected_level, events)?
    else {
        return Ok(());
    };
    if header.level == 0 {
        scan_leaf::<P, E>(&context, page, header, page_number, base, events)
    } else {
        scan_branch::<P, E>(
            context,
            page,
            header,
            page_number,
            base,
            path,
            depth,
            events,
        )
    }
}

fn claim_page<E: TableEvents>(
    context: &mut ScanContext<'_>,
    page_number: u32,
    path: &mut [u32; MAX_DEPTH],
    depth: usize,
    events: &mut E,
) -> Result<bool> {
    match context
        .pages
        .claim(page_number, context.meta.page_count, path, depth)?
    {
        PageClaim::Claimed => Ok(true),
        PageClaim::Rejected(reason) => {
            events.unknown(reason, Some(page_number))?;
            Ok(false)
        }
    }
}

fn read_page<'m, P: PayloadCodec, E: TableEvents>(
    context: &ScanContext<'m>,
    page_number: u32,
    expected_level: u16,
    events: &mut E,
) -> Result<Option<(PageView<'m>, table::Header)>> {
    let page =
        match super::page_read::checked(context.mapping, page_number, context.meta.page_count) {
            Ok(page) => page,
            Err(problem) => {
                reject_page(events, page_number, problem.reason, problem.io_unreadable)?;
                return Ok(None);
            }
        };
    let header =
        match table::inspect_header::<P, _>(page, context.meta.txn_id, Some(expected_level)) {
            Ok(header) => header,
            Err(problem) => {
                reject_page(events, page_number, header_reason(problem), false)?;
                return Ok(None);
            }
        };
    if !table::reserved_zero::<P, _>(page, header.level) {
        reject_page(
            events,
            page_number,
            ValidationReason::PageReservedNonzero,
            false,
        )?;
        return Ok(None);
    }
    events.page_accepted()?;
    Ok(Some((page, header)))
}

fn scan_leaf<P: PayloadCodec, E: TableEvents>(
    context: &ScanContext<'_>,
    page: PageView<'_>,
    header: table::Header,
    page_number: u32,
    base: u64,
    events: &mut E,
) -> Result<()> {
    let mut found = 0usize;
    for slot in 0..table::leaf_slots::<P>() {
        context.cancellation.check()?;
        let cell = table::leaf_cell::<P, _>(page, slot)?;
        if cell.all_zero(0, cell.len()) {
            continue;
        }
        found += 1;
        events.leaf(page_number, base + slot as u64, cell)?;
    }
    if found != header.item_count {
        events.unknown(ValidationReason::PageHeaderInvalid, Some(page_number))?;
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn scan_branch<P: PayloadCodec, E: TableEvents>(
    context: ScanContext<'_>,
    page: PageView<'_>,
    header: table::Header,
    page_number: u32,
    base: u64,
    path: &mut [u32; MAX_DEPTH],
    depth: usize,
    events: &mut E,
) -> Result<()> {
    let span = table::coverage::<P>(header.level - 1)?;
    let mut found = 0usize;
    for index in 0..table::branch_slots() {
        context.cancellation.check()?;
        let child = table::raw_branch_child(page, index)?;
        if child == 0 {
            continue;
        }
        found += 1;
        if child < 2 || u64::from(child) >= context.meta.page_count {
            events.unknown(ValidationReason::PageOutOfBounds, Some(child))?;
            continue;
        }
        let child_base = base
            .checked_add(
                span.checked_mul(index as u64)
                    .ok_or(Error::ArithmeticOverflow("structure recovery coverage"))?,
            )
            .ok_or(Error::ArithmeticOverflow("structure recovery coverage"))?;
        scan_node::<P, E>(
            ScanContext {
                mapping: context.mapping,
                meta: context.meta,
                pages: context.pages,
                cancellation: context.cancellation,
            },
            child,
            header.level - 1,
            child_base,
            path,
            depth + 1,
            events,
        )?;
    }
    if found != header.item_count {
        events.unknown(ValidationReason::PageHeaderInvalid, Some(page_number))?;
    }
    Ok(())
}

fn reject_page<E: TableEvents>(
    events: &mut E,
    page_number: u32,
    reason: ValidationReason,
    io_unreadable: bool,
) -> Result<()> {
    events.page_rejected(io_unreadable)?;
    events.unknown(reason, Some(page_number))
}

fn header_reason(problem: table::HeaderProblem) -> ValidationReason {
    match problem {
        table::HeaderProblem::Header | table::HeaderProblem::Shape => {
            ValidationReason::PageHeaderInvalid
        }
        table::HeaderProblem::Born => ValidationReason::PageBornTxnInvalid,
        table::HeaderProblem::Type => ValidationReason::PageTypeMismatch,
        table::HeaderProblem::Level => ValidationReason::TreeLevelInvalid,
    }
}

fn reconcile_ids<S: RecoverySink>(
    entries: &StructureIndex,
    tables: &mut Tables,
    reporter: &mut Reporter<'_, S>,
) -> Result<()> {
    for index in 0..entries.records_len() {
        let entry = entries.record(tables, index)?;
        let Insert::Duplicate {
            first,
            newly_conflicted,
        } = entries.insert_id(tables, entry.id, index)?
        else {
            continue;
        };
        entries.reject(tables, first)?;
        entries.reject(tables, index)?;
        if newly_conflicted {
            emit(
                reporter,
                ValidationReason::StructureInvalid,
                ValidationObject::StructureDictionary,
                Some(entry.leaf_page),
            )?;
        }
    }
    Ok(())
}

fn report_outcomes<S: RecoverySink>(
    entries: &StructureIndex,
    tables: &Tables,
    reporter: &mut Reporter<'_, S>,
) -> Result<()> {
    let mut accepted = 0u64;
    let mut rejected = 0u64;
    for index in 0..entries.records_len() {
        if entries.record(tables, index)?.rejected {
            rejected = rejected.checked_add(1).ok_or(Error::ArithmeticOverflow(
                "recovery structure entries rejected",
            ))?;
        } else {
            accepted = accepted.checked_add(1).ok_or(Error::ArithmeticOverflow(
                "recovery structure entries accepted",
            ))?;
        }
    }
    reporter.structure_accepted(accepted)?;
    reporter.structure_rejected(rejected)
}
