use crate::error::Result;
use crate::mapping::ByteSource;
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
) -> Result<Option<slotted_page::LayoutInspection<P>>> {
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
) -> Result<Option<slotted_page::LayoutInspection<P>>> {
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
) -> Result<Option<slotted_page::LayoutInspection<P>>> {
    let Some(inspection) = slotted_page::inspect_layout(page, &header, layout) else {
        invalid_layout(context, page_number, object)?;
        return Ok(None);
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
    Ok(Some(inspection))
}

fn invalid_layout<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    object: ValidationObject,
) -> Result<()> {
    context.emit(
        ValidationReason::PageHeaderInvalid,
        object,
        Some(page_number),
        None,
        None,
    )
}
