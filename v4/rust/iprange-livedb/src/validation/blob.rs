use crate::contract::{u16_le, u32_le, u64_le, MAX_TREE_LEVEL, PAGE_MAGIC, PAGE_SIZE};
use crate::error::Result;
use crate::format::page_type;
use crate::mapping::{ByteSource, BytesView, PageView};

use super::context::Context;
use super::page::{self, TreePageSpec};
use super::{ValidationObject, ValidationReason, ValidationSink};

const BRANCH_TYPE: u8 = page_type::MEMBERSHIP_BLOB_BRANCH;
const LEAF_TYPE: u8 = page_type::MEMBERSHIP_BLOB_LEAF;
const MEMBERSHIP_KIND: u32 = 1;
const HEADER_SIZE: usize = 32;
const LEAF_DATA: usize = 48;
const LEAF_CAPACITY: usize = PAGE_SIZE - LEAF_DATA;

#[derive(Clone, Copy)]
struct Span {
    start: u64,
    end: u64,
    complete: bool,
}

pub(crate) fn scan_membership<'m, S, F>(
    context: &mut Context<'m, S>,
    root: u32,
    length: u64,
    mut consume: F,
) -> Result<bool>
where
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, BytesView<'m>) -> Result<()>,
{
    if !request_valid(root, length) {
        finding(context, None)?;
        return Ok(false);
    }
    let mut path = [0; MAX_TREE_LEVEL as usize + 1];
    let result = scan_node(context, root, None, 0, length, &mut path, 0, &mut consume)?;
    let Some(span) = result else {
        return Ok(false);
    };
    finish_span(context, root, length, span)
}

fn request_valid(root: u32, length: u64) -> bool {
    root != 0 && length != 0 && length % 8 == 0
}

fn finish_span<S: ValidationSink>(
    context: &mut Context<'_, S>,
    root: u32,
    length: u64,
    span: Span,
) -> Result<bool> {
    if !span.complete {
        return Ok(false);
    }
    let complete = span.start == 0 && span.end == length;
    if !complete {
        finding(context, Some(root))?;
    }
    Ok(complete)
}

#[allow(clippy::too_many_arguments)]
fn scan_node<'m, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    expected_level: Option<u16>,
    expected_start: u64,
    length: u64,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    consume: &mut F,
) -> Result<Option<Span>>
where
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, BytesView<'m>) -> Result<()>,
{
    let Some(slot) = path.get_mut(depth) else {
        context.emit(
            ValidationReason::TreeLevelInvalid,
            ValidationObject::MembershipBlob,
            Some(page_number),
            None,
            None,
        )?;
        context.mark_untraversable(false)?;
        return Ok(None);
    };
    *slot = page_number;
    let Some(page) = context.read_graph_page(
        page_number,
        ValidationObject::MembershipBlob,
        &path[..depth],
    )?
    else {
        return Ok(None);
    };
    match page.byte(4) {
        Some(LEAF_TYPE) => scan_leaf(
            context,
            page_number,
            page,
            expected_level,
            expected_start,
            length,
            consume,
        ),
        Some(BRANCH_TYPE) => scan_branch(
            context,
            page_number,
            page,
            expected_level,
            expected_start,
            length,
            path,
            depth,
            consume,
        ),
        _ => invalid_page(context, page_number, ValidationReason::PageTypeMismatch),
    }
}

fn scan_leaf<'m, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    page: PageView<'m>,
    expected_level: Option<u16>,
    expected_start: u64,
    length: u64,
    consume: &mut F,
) -> Result<Option<Span>>
where
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, BytesView<'m>) -> Result<()>,
{
    if !common_identity(context, page_number, page)? {
        return Ok(None);
    }
    let Some(geometry) = leaf_geometry(page, expected_level, expected_start, length) else {
        finding(context, Some(page_number))?;
        context.mark_untraversable(false)?;
        return Ok(None);
    };
    if !page.all_zero(
        LEAF_DATA + geometry.data_len,
        PAGE_SIZE - LEAF_DATA - geometry.data_len,
    ) {
        context.emit(
            ValidationReason::PageReservedNonzero,
            ValidationObject::MembershipBlob,
            Some(page_number),
            None,
            None,
        )?;
    }
    let bytes = page
        .range(LEAF_DATA, geometry.data_len)
        .ok_or(crate::error::Error::Corrupt(
            "membership blob bytes are invalid",
        ))?;
    consume(context, bytes)?;
    Ok(Some(Span {
        start: geometry.start,
        end: geometry.end,
        complete: true,
    }))
}

struct LeafGeometry {
    start: u64,
    end: u64,
    data_len: usize,
}

fn leaf_geometry<P: ByteSource>(
    page: P,
    expected_level: Option<u16>,
    expected_start: u64,
    length: u64,
) -> Option<LeafGeometry> {
    let data_len = usize::from(u16_le(page, 40));
    let start = u64_le(page, 32);
    let end = start.checked_add(data_len as u64)?;
    if !leaf_header_valid(page, expected_level, data_len) {
        return None;
    }
    if !leaf_payload_valid(start, end, expected_start, length, data_len) {
        return None;
    }
    Some(LeafGeometry {
        start,
        end,
        data_len,
    })
}

fn leaf_header_valid<P: ByteSource>(page: P, expected_level: Option<u16>, data_len: usize) -> bool {
    expected_level.map_or(true, |level| level == 0)
        && u16_le(page, 16) == 1
        && u16_le(page, 18) == 0
        && usize::from(u16_le(page, 20)) == LEAF_DATA + data_len
        && usize::from(u16_le(page, 22)) == PAGE_SIZE
        && u32_le(page, 24) == MEMBERSHIP_KIND
        && page.all_zero(42, LEAF_DATA - 42)
}

fn leaf_payload_valid(
    start: u64,
    end: u64,
    expected_start: u64,
    length: u64,
    data_len: usize,
) -> bool {
    (1..=LEAF_CAPACITY).contains(&data_len)
        && data_len % 8 == 0
        && start == expected_start
        && end <= length
        && (end == length || data_len == LEAF_CAPACITY)
}

#[allow(clippy::too_many_arguments)]
fn scan_branch<'m, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    page: PageView<'m>,
    expected_level: Option<u16>,
    expected_start: u64,
    length: u64,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    consume: &mut F,
) -> Result<Option<Span>>
where
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, BytesView<'m>) -> Result<()>,
{
    let Some(header) = branch_header(context, page_number, page, expected_level)? else {
        return Ok(None);
    };
    if !branch_records_valid(context, page_number, page, header, expected_start, length)? {
        context.mark_untraversable(false)?;
        return Ok(None);
    }

    scan_branch_children(
        context,
        page_number,
        page,
        header,
        length,
        path,
        depth,
        consume,
    )
}

fn branch_header<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: PageView<'_>,
    expected_level: Option<u16>,
) -> Result<Option<page::SlottedHeader>> {
    let header = page::slotted_header(
        context,
        page_number,
        page,
        ValidationObject::MembershipBlob,
        TreePageSpec {
            branch_type: BRANCH_TYPE,
            leaf_type: LEAF_TYPE,
            aux: MEMBERSHIP_KIND,
            expected_level,
        },
    )?;
    let Some(header) = header else {
        context.mark_untraversable(false)?;
        return Ok(None);
    };
    let cells_valid = page::validate_fixed_cells(
        context,
        page_number,
        page,
        ValidationObject::MembershipBlob,
        header,
        16,
    )?;
    if header.level == 0 || !cells_valid {
        context.mark_untraversable(false)?;
        return Ok(None);
    }
    Ok(Some(header))
}

#[allow(clippy::too_many_arguments)]
fn scan_branch_children<'m, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    page: PageView<'m>,
    header: page::SlottedHeader,
    length: u64,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    consume: &mut F,
) -> Result<Option<Span>>
where
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, BytesView<'m>) -> Result<()>,
{
    let mut first = None;
    let mut previous_end = None;
    let mut complete = true;
    for index in 0..header.item_count {
        let cell = page::fixed_cell(page, header, index, 16)?;
        let offset = u64_le(cell, 0);
        let child = u32_le(cell, 8);
        let result = scan_node(
            context,
            child,
            Some(header.level - 1),
            offset,
            length,
            path,
            depth + 1,
            consume,
        )?;
        let Some(span) = result else {
            complete = false;
            previous_end = None;
            continue;
        };
        first.get_or_insert(span.start);
        if span.start != offset || previous_end.is_some_and(|end| end != span.start) {
            finding(context, Some(page_number))?;
            complete = false;
        }
        previous_end = Some(span.end);
        complete &= span.complete;
    }
    Ok(first.map(|start| Span {
        start,
        end: previous_end.unwrap_or(start),
        complete,
    }))
}

fn branch_records_valid<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: PageView<'_>,
    header: page::SlottedHeader,
    expected_start: u64,
    length: u64,
) -> Result<bool> {
    let mut previous = None;
    for index in 0..header.item_count {
        let cell = page::fixed_cell(page, header, index, 16)?;
        let offset = u64_le(cell, 0);
        let child = u32_le(cell, 8);
        if !branch_record_valid(
            context,
            cell,
            offset,
            child,
            previous,
            index,
            expected_start,
            length,
        ) {
            finding(context, Some(page_number))?;
            return Ok(false);
        }
        previous = Some(offset);
    }
    Ok(true)
}

#[allow(clippy::too_many_arguments)]
fn branch_record_valid<S: ValidationSink, P: ByteSource>(
    context: &Context<'_, S>,
    cell: P,
    offset: u64,
    child: u32,
    previous: Option<u64>,
    index: usize,
    expected_start: u64,
    length: u64,
) -> bool {
    u32_le(cell, 12) == 0
        && child >= 2
        && u64::from(child) < context.meta.page_count
        && offset < length
        && previous.map_or(true, |prior| prior < offset)
        && (index != 0 || offset == expected_start)
}

fn common_identity<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
) -> Result<bool> {
    if !page.equals(0, &PAGE_MAGIC)
        || page.byte(5) != Some(0)
        || u16_le(page, 6) != HEADER_SIZE as u16
    {
        invalid_page(context, page_number, ValidationReason::PageHeaderInvalid)?;
        return Ok(false);
    }
    let born = u64_le(page, 8);
    if born == 0 || born > context.meta.txn_id {
        invalid_page(context, page_number, ValidationReason::PageBornTxnInvalid)?;
        return Ok(false);
    }
    Ok(true)
}

fn invalid_page<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    reason: ValidationReason,
) -> Result<Option<Span>> {
    context.emit(
        reason,
        ValidationObject::MembershipBlob,
        Some(page_number),
        None,
        None,
    )?;
    context.mark_untraversable(false)?;
    Ok(None)
}

fn finding<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: Option<u32>,
) -> Result<()> {
    context.emit(
        ValidationReason::BlobInvalid,
        ValidationObject::MembershipBlob,
        page_number,
        None,
        None,
    )
}
