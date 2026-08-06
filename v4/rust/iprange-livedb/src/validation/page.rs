use crate::contract::{u16_le, u32_le, u64_le, MAX_TREE_LEVEL, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::mapping::{ByteRange, ByteSource};

use super::context::Context;
use super::{ValidationObject, ValidationReason, ValidationSink};

const HEADER_SIZE: usize = 32;

#[derive(Clone, Copy)]
pub(crate) struct SlottedHeader {
    pub(crate) item_count: usize,
    pub(crate) level: u16,
    pub(crate) lower: usize,
    pub(crate) upper: usize,
}

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
    let level = u16_le(page, 18);
    let item_count = usize::from(u16_le(page, 16));
    let lower = usize::from(u16_le(page, 20));
    let upper = usize::from(u16_le(page, 22));
    if let Some(reason) = slotted_problem(
        page,
        context.meta.txn_id,
        &spec,
        level,
        item_count,
        lower,
        upper,
    ) {
        context.emit(reason, object, Some(page_number), None, None)?;
        return Ok(None);
    }
    Ok(Some(SlottedHeader {
        item_count,
        level,
        lower,
        upper,
    }))
}

#[allow(clippy::too_many_arguments)]
fn slotted_problem<P: ByteSource>(
    page: P,
    txn_id: u64,
    spec: &TreePageSpec,
    level: u16,
    item_count: usize,
    lower: usize,
    upper: usize,
) -> Option<ValidationReason> {
    if !common_header_valid(page) {
        return Some(ValidationReason::PageHeaderInvalid);
    }
    if !born_valid(page, txn_id) {
        return Some(ValidationReason::PageBornTxnInvalid);
    }
    if !type_valid(page, spec, level) {
        return Some(ValidationReason::PageTypeMismatch);
    }
    if !level_valid(level, spec.expected_level) {
        return Some(ValidationReason::TreeLevelInvalid);
    }
    if !slot_bounds_valid(item_count, lower, upper) {
        return Some(ValidationReason::PageHeaderInvalid);
    }
    None
}

fn common_header_valid<P: ByteSource>(page: P) -> bool {
    page.equals(0, &PAGE_MAGIC) && page.byte(5) == Some(0) && u16_le(page, 6) == HEADER_SIZE as u16
}

fn born_valid<P: ByteSource>(page: P, txn_id: u64) -> bool {
    let born = u64_le(page, 8);
    born != 0 && born <= txn_id
}

fn type_valid<P: ByteSource>(page: P, spec: &TreePageSpec, level: u16) -> bool {
    let expected = if level == 0 {
        spec.leaf_type
    } else {
        spec.branch_type
    };
    page.byte(4) == Some(expected) && u32_le(page, 24) == spec.aux
}

fn level_valid(level: u16, expected: Option<u16>) -> bool {
    level <= MAX_TREE_LEVEL && expected.map_or(true, |expected| expected == level)
}

fn slot_bounds_valid(item_count: usize, lower: usize, upper: usize) -> bool {
    let expected_lower = item_count
        .checked_mul(2)
        .and_then(|bytes| bytes.checked_add(HEADER_SIZE));
    item_count != 0 && expected_lower == Some(lower) && lower <= upper && upper < PAGE_SIZE
}

pub(crate) fn validate_fixed_cells<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
    object: ValidationObject,
    header: SlottedHeader,
    cell_len: usize,
) -> Result<bool> {
    validate_layout(context, page_number, page, object, header, |_| {
        Some(cell_len)
    })
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
    validate_layout(context, page_number, page, object, header, |start| {
        let length = usize::from(crate::contract::u16_source(page, start)?);
        (minimum..=maximum).contains(&length).then_some(length)
    })
}

fn validate_layout<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
    object: ValidationObject,
    header: SlottedHeader,
    mut length_at: impl FnMut(usize) -> Option<usize>,
) -> Result<bool> {
    let Some(used) = mark_cells(page, header, &mut length_at) else {
        return invalid_layout(context, page_number, object);
    };
    if reserved_nonzero(page, header, &used) {
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

fn mark_cells<P: ByteSource>(
    page: P,
    header: SlottedHeader,
    length_at: &mut impl FnMut(usize) -> Option<usize>,
) -> Option<[u64; PAGE_SIZE / 64]> {
    let mut used = [0u64; PAGE_SIZE / 64];
    let mut minimum = PAGE_SIZE;
    for index in 0..header.item_count {
        let slot = HEADER_SIZE + index * 2;
        let start = usize::from(u16_le(page, slot));
        let cell_len = length_at(start)?;
        let end = start.checked_add(cell_len)?;
        if start < header.upper || end > PAGE_SIZE || !mark(&mut used, start, end) {
            return None;
        }
        minimum = minimum.min(start);
    }
    if minimum != header.upper {
        return None;
    }
    Some(used)
}

fn reserved_nonzero<P: ByteSource>(
    page: P,
    header: SlottedHeader,
    used: &[u64; PAGE_SIZE / 64],
) -> bool {
    let mut reserved_nonzero = !page.all_zero(header.lower, header.upper - header.lower);
    for absolute in header.upper..PAGE_SIZE {
        if !marked(used, absolute) && page.byte(absolute) != Some(0) {
            reserved_nonzero = true;
            break;
        }
    }
    reserved_nonzero
}

pub(crate) fn fixed_cell<P: ByteSource>(
    page: P,
    header: SlottedHeader,
    index: usize,
    cell_len: usize,
) -> Result<ByteRange<P>> {
    if index >= header.item_count {
        return Err(Error::Corrupt("validation slot index is invalid"));
    }
    let start = usize::from(u16_le(page, HEADER_SIZE + index * 2));
    let end = start
        .checked_add(cell_len)
        .ok_or(Error::Corrupt("validation cell end overflows"))?;
    ByteRange::new(page, start, end - start)
        .ok_or(Error::Corrupt("validation cell is outside the page"))
}

pub(crate) fn variable_cell<P: ByteSource>(
    page: P,
    header: SlottedHeader,
    index: usize,
) -> Result<ByteRange<P>> {
    if index >= header.item_count {
        return Err(Error::Corrupt("validation slot index is invalid"));
    }
    let start = usize::from(u16_le(page, HEADER_SIZE + index * 2));
    let length = usize::from(
        crate::contract::u16_source(page, start).ok_or(Error::Corrupt(
            "validation record length is outside the page",
        ))?,
    );
    let end = start
        .checked_add(length)
        .ok_or(Error::Corrupt("validation record end overflows"))?;
    ByteRange::new(page, start, end - start)
        .ok_or(Error::Corrupt("validation record is outside the page"))
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

fn mark(bits: &mut [u64; PAGE_SIZE / 64], start: usize, end: usize) -> bool {
    for offset in start..end {
        let word = offset / 64;
        let mask = 1u64 << (offset % 64);
        if bits[word] & mask != 0 {
            return false;
        }
        bits[word] |= mask;
    }
    true
}

fn marked(bits: &[u64; PAGE_SIZE / 64], offset: usize) -> bool {
    bits[offset / 64] & (1u64 << (offset % 64)) != 0
}
