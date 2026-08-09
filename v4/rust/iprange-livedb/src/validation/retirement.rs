use crate::error::{Error, Result};
use crate::mapping::ByteSource;
use crate::retirement::{self, Extent, Key};

use super::context::Context;
use super::tree::{self, CellLayout, Codec};
use super::{ValidationObject, ValidationReason, ValidationSink};

struct RetirementCodec;

impl Codec for RetirementCodec {
    type Key = Key;

    const BRANCH_TYPE: u8 = retirement::BRANCH_TYPE;
    const LEAF_TYPE: u8 = retirement::LEAF_TYPE;
    const AUX: u32 = retirement::AUX;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Fixed(retirement::CELL_SIZE);
    const LEAF_LAYOUT: CellLayout = CellLayout::Fixed(retirement::CELL_SIZE);
    const LEAF_INVALID: ValidationReason = ValidationReason::RetirementListInvalid;

    fn branch_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        retirement::decode_key(cell).ok()
    }

    fn branch_child<P: ByteSource>(cell: P) -> Option<u32> {
        retirement::decode_branch_child(cell)
    }

    fn leaf_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        retirement::decode_raw(cell).map(Extent::key)
    }
}

pub(crate) fn validate<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    let root = context.meta.retirement_root;
    if root == 0 {
        if context.meta.retired_extent_count != 0 {
            count_mismatch(context)?;
        }
        return Ok(());
    }

    let mut previous = None;
    let result = tree::walk::<RetirementCodec, S, _>(
        context,
        root,
        ValidationObject::RetirementTree,
        |context, page_number, cell| {
            let Some(extent) = retirement::decode_raw(cell) else {
                return Ok(());
            };
            validate_extent(context, page_number, extent, previous)?;
            previous = Some(extent);
            mark_extent(context, extent)
        },
    )?;
    if result.records != context.meta.retired_extent_count {
        count_mismatch(context)?;
    }
    Ok(())
}

fn validate_extent<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    extent: Extent,
    previous: Option<Extent>,
) -> Result<()> {
    let end = u64::from(extent.key().first_page())
        .checked_add(extent.page_count())
        .ok_or(Error::ArithmeticOverflow("validation retirement extent"))?;
    if !extent_valid(context, extent, end) {
        context.emit(
            ValidationReason::RetirementListInvalid,
            ValidationObject::RetirementTree,
            Some(page_number),
            None,
            None,
        )?;
        return Ok(());
    }
    if previous.is_some_and(|previous| extents_overlap(previous, extent)) {
        context.emit(
            ValidationReason::RetirementOrderInvalid,
            ValidationObject::RetirementTree,
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(())
}

fn extent_valid<S: ValidationSink>(context: &Context<'_, S>, extent: Extent, end: u64) -> bool {
    extent.transaction() > 1
        && extent.transaction() <= context.meta.txn_id
        && extent.key().first_page() >= 2
        && extent.page_count() != 0
        && end <= context.meta.page_count
}

fn extents_overlap(previous: Extent, current: Extent) -> bool {
    previous.transaction() == current.transaction()
        && previous.pages().end >= u64::from(current.key().first_page())
}

fn mark_extent<S: ValidationSink>(context: &mut Context<'_, S>, extent: Extent) -> Result<()> {
    if extent.key().first_page() < 2 || extent.page_count() == 0 {
        return Ok(());
    }
    let end = u64::from(extent.key().first_page())
        .saturating_add(extent.page_count())
        .min(context.meta.page_count);
    for page in u64::from(extent.key().first_page())..end {
        context.checkpoint()?;
        context.mark_allocated(page as u32, ValidationObject::RetirementTree)?;
    }
    Ok(())
}

fn count_mismatch<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    context.emit(
        ValidationReason::RootCountInvalid,
        ValidationObject::RetirementTree,
        None,
        None,
        None,
    )
}
