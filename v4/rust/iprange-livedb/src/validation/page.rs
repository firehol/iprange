use crate::error::Result;
use crate::mapping::{ByteRange, ByteSource};
use crate::slotted_page::{self, CellLayout, HeaderProblem};

use super::context::Context;
use super::{ValidationObject, ValidationReason, ValidationSink};

pub(crate) type SlottedHeader = slotted_page::Header;

pub(crate) struct TreePageSpec {
    pub(crate) branch_type: u8,
    pub(crate) leaf_type: u8,
    pub(crate) aux: u32,
    pub(crate) expected_level: Option<u16>,
}

pub(crate) fn slotted_header<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
    object: ValidationObject,
    spec: TreePageSpec,
) -> Result<Option<SlottedHeader>> {
    let header = match slotted_page::inspect_tree_header(
        page,
        context.meta.txn_id,
        spec.branch_type,
        spec.leaf_type,
        spec.aux,
        spec.expected_level,
    ) {
        Ok(header) => header,
        Err(problem) => {
            let reason = match problem {
                HeaderProblem::Header | HeaderProblem::Shape => ValidationReason::PageHeaderInvalid,
                HeaderProblem::Born => ValidationReason::PageBornTxnInvalid,
                HeaderProblem::Type => ValidationReason::PageTypeMismatch,
                HeaderProblem::Level => ValidationReason::TreeLevelInvalid,
            };
            context.emit(reason, object, Some(page_number), None, None)?;
            return Ok(None);
        }
    };
    Ok(Some(header))
}

pub(crate) fn validate_fixed_cells<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
    object: ValidationObject,
    header: SlottedHeader,
    cell_len: usize,
) -> Result<bool> {
    validate_layout(
        context,
        page_number,
        page,
        object,
        header,
        CellLayout::Fixed(cell_len),
    )
}

pub(crate) fn validate_variable_cells<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
    object: ValidationObject,
    header: SlottedHeader,
    minimum: usize,
    maximum: usize,
) -> Result<bool> {
    validate_layout(
        context,
        page_number,
        page,
        object,
        header,
        CellLayout::Variable { minimum, maximum },
    )
}

fn validate_layout<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
    object: ValidationObject,
    header: SlottedHeader,
    layout: CellLayout,
) -> Result<bool> {
    let Some(inspection) = slotted_page::inspect_layout(page, &header, layout) else {
        return invalid_layout(context, page_number, object);
    };
    if inspection.reserved_nonzero {
        context.emit(
            ValidationReason::PageReservedNonzero,
            object,
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(true)
}

pub(crate) fn fixed_cell<P: ByteSource>(
    page: P,
    header: SlottedHeader,
    index: usize,
    cell_len: usize,
) -> Result<ByteRange<P>> {
    slotted_page::cell(page, &header, index, cell_len)
}

pub(crate) fn variable_cell<P: ByteSource>(
    page: P,
    header: SlottedHeader,
    index: usize,
    minimum: usize,
    maximum: usize,
) -> Result<ByteRange<P>> {
    slotted_page::record(page, &header, index, minimum, maximum)
}

fn invalid_layout<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    object: ValidationObject,
) -> Result<bool> {
    context.emit(
        ValidationReason::PageHeaderInvalid,
        object,
        Some(page_number),
        None,
        None,
    )?;
    Ok(false)
}
