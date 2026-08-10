use crate::blob_tree;
use crate::contract::{MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::Result;
use crate::mapping::{ByteSource, BytesView, PageView};
use crate::page_header::{self, CommonProblem};
use crate::slotted_page::LayoutInspection;

use super::context::Context;
use super::page::{self, TreePageSpec};
use super::{ValidationObject, ValidationReason, ValidationSink};

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
    match crate::page_header::page_type(page) {
        Some(blob_tree::LEAF_TYPE) => scan_leaf(
            context,
            page_number,
            page,
            expected_level,
            expected_start,
            length,
            consume,
        ),
        Some(blob_tree::BRANCH_TYPE) => scan_branch(
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
        blob_tree::LEAF_DATA + geometry.data_len,
        PAGE_SIZE - blob_tree::LEAF_DATA - geometry.data_len,
    ) {
        context.emit(
            ValidationReason::PageReservedNonzero,
            ValidationObject::MembershipBlob,
            Some(page_number),
            None,
            None,
        )?;
    }
    let bytes =
        page.range(blob_tree::LEAF_DATA, geometry.data_len)
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

fn leaf_geometry<P: ByteSource>(
    page: P,
    expected_level: Option<u16>,
    expected_start: u64,
    length: u64,
) -> Option<blob_tree::LeafGeometry> {
    blob_tree::leaf_geometry(page, expected_level, expected_start, length).ok()
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
    let Some((header, cells)) = branch_header(context, page_number, page, expected_level)? else {
        return Ok(None);
    };
    if !branch_records_valid(context, page_number, cells, expected_start, length)? {
        context.mark_untraversable(false)?;
        return Ok(None);
    }

    scan_branch_children(
        context,
        page_number,
        cells,
        header,
        length,
        path,
        depth,
        consume,
    )
}

fn branch_header<'m, S: ValidationSink>(
    context: &mut Context<'m, S>,
    page_number: u32,
    page: PageView<'m>,
    expected_level: Option<u16>,
) -> Result<Option<(page::SlottedHeader, LayoutInspection<PageView<'m>>)>> {
    let header = page::slotted_header(
        context,
        page_number,
        page,
        ValidationObject::MembershipBlob,
        TreePageSpec {
            branch_type: blob_tree::BRANCH_TYPE,
            leaf_type: blob_tree::LEAF_TYPE,
            aux: blob_tree::MEMBERSHIP_KIND,
            expected_level,
        },
    )?;
    let Some(header) = header else {
        context.mark_untraversable(false)?;
        return Ok(None);
    };
    let cells = page::validate_fixed_cells(
        context,
        page_number,
        page,
        ValidationObject::MembershipBlob,
        header,
        blob_tree::BRANCH_RECORD_SIZE,
    )?;
    let Some(cells) = cells else {
        context.mark_untraversable(false)?;
        return Ok(None);
    };
    if header.level == 0 {
        context.mark_untraversable(false)?;
        return Ok(None);
    }
    Ok(Some((header, cells)))
}

#[allow(clippy::too_many_arguments)]
fn scan_branch_children<'m, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    cells: LayoutInspection<PageView<'m>>,
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
    for cell in cells.cells() {
        let record = blob_tree::decode_branch_record(cell)
            .expect("branch validation accepted this fixed record");
        let result = scan_node(
            context,
            record.child,
            Some(header.level - 1),
            record.offset,
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
        if span.start != record.offset || previous_end.is_some_and(|end| end != span.start) {
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
    cells: LayoutInspection<PageView<'_>>,
    expected_start: u64,
    length: u64,
) -> Result<bool> {
    let mut previous = None;
    for (index, cell) in cells.cells().enumerate() {
        let Ok(record) = blob_tree::decode_branch_record(cell) else {
            finding(context, Some(page_number))?;
            return Ok(false);
        };
        if !branch_record_valid(context, record, previous, index, expected_start, length) {
            finding(context, Some(page_number))?;
            return Ok(false);
        }
        previous = Some(record.offset);
    }
    Ok(true)
}

#[allow(clippy::too_many_arguments)]
fn branch_record_valid<S: ValidationSink>(
    context: &Context<'_, S>,
    record: blob_tree::BranchRecord,
    previous: Option<u64>,
    index: usize,
    expected_start: u64,
    length: u64,
) -> bool {
    record.child >= 2
        && u64::from(record.child) < context.meta.page_count
        && record.offset < length
        && previous.map_or(true, |prior| prior < record.offset)
        && (index != 0 || record.offset == expected_start)
}

fn common_identity<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
) -> Result<bool> {
    if let Some(problem) = page_header::common_problem(page, context.meta.txn_id) {
        let reason = match problem {
            CommonProblem::Header => ValidationReason::PageHeaderInvalid,
            CommonProblem::Born => ValidationReason::PageBornTxnInvalid,
        };
        invalid_page(context, page_number, reason)?;
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
