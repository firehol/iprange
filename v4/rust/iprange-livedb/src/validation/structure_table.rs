//! Validation traversal for the direct structure-ID table.

use crate::error::{Error, Result};
use crate::mapping::{ByteRange, ByteSource, PageView};
use crate::structured_value::{table, PayloadCodec};

use super::context::Context;
use super::{ValidationObject, ValidationReason, ValidationSink};

const MAX_DEPTH: usize = 4;

pub(crate) struct WalkResult {
    pub(crate) records: u64,
}

pub(crate) fn walk<'m, P, S, F>(
    context: &mut Context<'m, S>,
    root: u32,
    mut leaf: F,
) -> Result<WalkResult>
where
    P: PayloadCodec,
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, u32, u64, ByteRange<PageView<'m>>) -> Result<()>,
{
    if root == 0 {
        return Ok(WalkResult { records: 0 });
    }
    let level = table::required_level::<P>(context.meta.structure_id_limit)?;
    let mut path = [0; MAX_DEPTH];
    let mut records = 0u64;
    walk_node::<P, S, F>(
        context,
        root,
        level,
        0,
        &mut path,
        0,
        &mut records,
        &mut leaf,
    )?;
    Ok(WalkResult { records })
}

#[allow(clippy::too_many_arguments)]
fn walk_node<'m, P, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    expected_level: u16,
    base: u64,
    path: &mut [u32; MAX_DEPTH],
    depth: usize,
    records: &mut u64,
    leaf: &mut F,
) -> Result<()>
where
    P: PayloadCodec,
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, u32, u64, ByteRange<PageView<'m>>) -> Result<()>,
{
    let Some(slot) = path.get_mut(depth) else {
        return context.emit(
            ValidationReason::TreeLevelInvalid,
            ValidationObject::StructureDictionary,
            Some(page_number),
            None,
            None,
        );
    };
    *slot = page_number;
    let Some(page) = context.read_graph_page(
        page_number,
        ValidationObject::StructureDictionary,
        &path[..depth],
    )?
    else {
        return Ok(());
    };
    let header =
        match table::inspect_header::<P, _>(page, context.meta.txn_id, Some(expected_level)) {
            Ok(header) => header,
            Err(problem) => {
                return context.emit(
                    header_reason(problem),
                    ValidationObject::StructureDictionary,
                    Some(page_number),
                    None,
                    None,
                );
            }
        };
    if !table::reserved_zero::<P, _>(page, header.level) {
        context.emit(
            ValidationReason::PageReservedNonzero,
            ValidationObject::StructureDictionary,
            Some(page_number),
            None,
            None,
        )?;
    }
    if header.level == 0 {
        walk_leaf::<P, S, F>(context, page_number, page, base, header, records, leaf)
    } else {
        walk_branch::<P, S, F>(
            context,
            page_number,
            page,
            base,
            header,
            path,
            depth,
            records,
            leaf,
        )
    }
}

fn walk_leaf<'m, P, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    page: PageView<'m>,
    base: u64,
    header: table::Header,
    records: &mut u64,
    leaf: &mut F,
) -> Result<()>
where
    P: PayloadCodec,
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, u32, u64, ByteRange<PageView<'m>>) -> Result<()>,
{
    let mut found = 0usize;
    for slot in 0..table::leaf_slots::<P>() {
        context.checkpoint()?;
        let cell = table::leaf_cell::<P, _>(page, slot)?;
        if cell.all_zero(0, cell.len()) {
            continue;
        }
        found += 1;
        *records = records.checked_add(1).ok_or(Error::ArithmeticOverflow(
            "structure validation record count",
        ))?;
        leaf(context, page_number, base + slot as u64, cell)?;
    }
    if found != header.item_count {
        page_shape_finding(context, page_number)?;
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn walk_branch<'m, P, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    page: PageView<'m>,
    base: u64,
    header: table::Header,
    path: &mut [u32; MAX_DEPTH],
    depth: usize,
    records: &mut u64,
    leaf: &mut F,
) -> Result<()>
where
    P: PayloadCodec,
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, u32, u64, ByteRange<PageView<'m>>) -> Result<()>,
{
    let span = table::coverage::<P>(header.level - 1)?;
    let mut found = 0usize;
    for index in 0..table::branch_slots() {
        context.checkpoint()?;
        let child = table::raw_branch_child(page, index)?;
        if child == 0 {
            continue;
        }
        found += 1;
        let child_base = base
            .checked_add(
                span.checked_mul(index as u64)
                    .ok_or(Error::ArithmeticOverflow("structure validation coverage"))?,
            )
            .ok_or(Error::ArithmeticOverflow("structure validation coverage"))?;
        walk_node::<P, S, F>(
            context,
            child,
            header.level - 1,
            child_base,
            path,
            depth + 1,
            records,
            leaf,
        )?;
    }
    if found != header.item_count {
        page_shape_finding(context, page_number)?;
    }
    Ok(())
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

fn page_shape_finding<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
) -> Result<()> {
    context.emit(
        ValidationReason::PageHeaderInvalid,
        ValidationObject::StructureDictionary,
        Some(page_number),
        None,
        None,
    )
}
