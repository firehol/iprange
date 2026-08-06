use crate::contract::{u32_le, AddressFamily, ValueKind, MAX_TREE_LEVEL};
use crate::error::Result;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::mapping::{ByteSource, PageView};

use super::context::Context;
use super::membership_table::InsertResult;
use super::page::{self, TreePageSpec};
use super::{ValidationObject, ValidationReason, ValidationSink};

const RANGE_BRANCH: u8 = 1;
const RANGE_LEAF: u8 = 2;

pub(crate) fn validate<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    if context.meta.range_root == 0 {
        if context.meta.range_record_count != 0 {
            count_mismatch(context)?;
        }
        return Ok(());
    }

    let count = match context.meta.address_family {
        AddressFamily::Ipv4 => validate_family::<Ipv4Key, S>(context)?,
        AddressFamily::Ipv6 => validate_family::<Ipv6Key, S>(context)?,
    };
    if count != context.meta.range_record_count {
        count_mismatch(context)?;
    }
    Ok(())
}

fn validate_family<K: IpKey, S: ValidationSink>(context: &mut Context<'_, S>) -> Result<u64> {
    let mut state = RangeState::<K> {
        count: 0,
        previous: None,
    };
    let mut path = [0; MAX_TREE_LEVEL as usize + 1];
    let root = context.meta.range_root;
    validate_node(context, root, None, true, &mut path, 0, &mut state)?;
    Ok(state.count)
}

fn validate_node<K: IpKey, S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    expected_level: Option<u16>,
    root: bool,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    state: &mut RangeState<K>,
) -> Result<Option<K>> {
    let Some(page) = read_range_page(context, page_number, path, depth)? else {
        state.previous = None;
        return Ok(None);
    };
    let Some(header) =
        validate_range_page::<K, S>(context, page_number, page, expected_level, root)?
    else {
        state.previous = None;
        return Ok(None);
    };
    if header.level == 0 {
        validate_leaf(context, page_number, page, header, state)
    } else {
        validate_branch(context, page_number, page, header, path, depth, state)
    }
}

fn read_range_page<'m, S: ValidationSink>(
    context: &mut Context<'m, S>,
    page_number: u32,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
) -> Result<Option<PageView<'m>>> {
    let Some(slot) = path.get_mut(depth) else {
        context.emit(
            ValidationReason::TreeLevelInvalid,
            ValidationObject::RangeTree,
            Some(page_number),
            None,
            None,
        )?;
        return Ok(None);
    };
    *slot = page_number;
    context.read_graph_page(page_number, ValidationObject::RangeTree, &path[..depth])
}

fn validate_range_page<K: IpKey, S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: PageView<'_>,
    expected_level: Option<u16>,
    root: bool,
) -> Result<Option<page::SlottedHeader>> {
    let Some(header) = page::slotted_header(
        context,
        page_number,
        page,
        ValidationObject::RangeTree,
        TreePageSpec {
            branch_type: RANGE_BRANCH,
            leaf_type: RANGE_LEAF,
            aux: K::FAMILY as u32,
            expected_level,
        },
    )?
    else {
        return Ok(None);
    };
    report_degenerate_root(context, page_number, header, root)?;
    let cell_len = range_cell_len::<K>(header.level);
    if !page::validate_fixed_cells(
        context,
        page_number,
        page,
        ValidationObject::RangeTree,
        header,
        cell_len,
    )? {
        return Ok(None);
    }
    Ok(Some(header))
}

fn report_degenerate_root<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    header: page::SlottedHeader,
    root: bool,
) -> Result<()> {
    if root && header.level > 0 && header.item_count == 1 {
        context.emit(
            ValidationReason::TreeLevelInvalid,
            ValidationObject::RangeTree,
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(())
}

fn range_cell_len<K: IpKey>(level: u16) -> usize {
    if level == 0 {
        K::WIDTH * 2 + 4
    } else {
        K::WIDTH + 4
    }
}

fn validate_leaf<K: IpKey, S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: PageView<'_>,
    header: page::SlottedHeader,
    state: &mut RangeState<K>,
) -> Result<Option<K>> {
    let mut order = LeafOrder::default();
    for index in 0..header.item_count {
        context.checkpoint()?;
        validate_leaf_cell(context, page_number, page, header, index, &mut order, state)?;
    }
    Ok(order.first)
}

struct LeafOrder<K> {
    first: Option<K>,
    previous: Option<K>,
}

impl<K> Default for LeafOrder<K> {
    fn default() -> Self {
        Self {
            first: None,
            previous: None,
        }
    }
}

impl<K: IpKey> LeafOrder<K> {
    fn observe<S: ValidationSink>(
        &mut self,
        context: &mut Context<'_, S>,
        page_number: u32,
        key: K,
    ) -> Result<()> {
        self.first.get_or_insert(key);
        if self.previous.is_some_and(|previous| previous >= key) {
            context.emit(
                ValidationReason::TreeOrderInvalid,
                ValidationObject::RangeTree,
                Some(page_number),
                None,
                None,
            )?;
        }
        self.previous = Some(key);
        Ok(())
    }
}

#[allow(clippy::too_many_arguments)]
fn validate_leaf_cell<K: IpKey, S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
    header: page::SlottedHeader,
    index: usize,
    order: &mut LeafOrder<K>,
    state: &mut RangeState<K>,
) -> Result<()> {
    let current = read_range_cell::<K, _>(page, header, index)?;
    order.observe(context, page_number, current.from)?;
    increment_count(state)?;
    if !range_shape_valid(context, page_number, current, state)? {
        return Ok(());
    }
    validate_range_value(context, page_number, current.value)?;
    validate_neighbor(context, page_number, state.previous, current)?;
    state.previous = Some(current);
    Ok(())
}

fn read_range_cell<K: IpKey, P: ByteSource>(
    page: P,
    header: page::SlottedHeader,
    index: usize,
) -> Result<Range<K>> {
    let cell = page::fixed_cell(page, header, index, K::WIDTH * 2 + 4)?;
    Ok(Range {
        from: K::read_le(cell, 0),
        to: K::read_le(cell, K::WIDTH),
        value: u32_le(cell, K::WIDTH * 2),
    })
}

fn increment_count<K>(state: &mut RangeState<K>) -> Result<()> {
    state.count = state
        .count
        .checked_add(1)
        .ok_or(crate::error::Error::ArithmeticOverflow(
            "validation range count",
        ))?;
    Ok(())
}

fn range_shape_valid<K: IpKey, S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    current: Range<K>,
    state: &mut RangeState<K>,
) -> Result<bool> {
    if current.from <= current.to {
        return Ok(true);
    }
    context.emit(
        ValidationReason::RangeReversed,
        ValidationObject::RangeTree,
        Some(page_number),
        None,
        None,
    )?;
    state.previous = None;
    Ok(false)
}

fn validate_range_value<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    value: u32,
) -> Result<()> {
    if context.meta.value_kind != ValueKind::Membership {
        return Ok(());
    }
    if value == 0 {
        context.emit(
            ValidationReason::MembershipBitmapInvalid,
            ValidationObject::RangeTree,
            Some(page_number),
            None,
            None,
        )?;
    } else if matches!(context.count_membership_range(value)?, InsertResult::Full) {
        context.emit(
            ValidationReason::MembershipRefcountInvalid,
            ValidationObject::RangeTree,
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(())
}

fn validate_neighbor<K: IpKey, S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    previous: Option<Range<K>>,
    current: Range<K>,
) -> Result<()> {
    let Some(previous) = previous else {
        return Ok(());
    };
    if current.from <= previous.to {
        context.emit(
            ValidationReason::RangeOverlap,
            ValidationObject::RangeTree,
            Some(page_number),
            None,
            None,
        )?;
    } else if previous.to.checked_next() == Some(current.from) && previous.value == current.value {
        context.emit(
            ValidationReason::RangeNotCoalesced,
            ValidationObject::RangeTree,
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(())
}

fn validate_branch<K: IpKey, S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: PageView<'_>,
    header: page::SlottedHeader,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    state: &mut RangeState<K>,
) -> Result<Option<K>> {
    let mut first = None;
    let mut previous = None;
    for index in 0..header.item_count {
        let cell = page::fixed_cell(page, header, index, K::WIDTH + 4)?;
        let key = K::read_le(cell, 0);
        let child = u32_le(cell, K::WIDTH);
        first.get_or_insert(key);
        if previous.is_some_and(|prior| prior >= key) {
            context.emit(
                ValidationReason::TreeOrderInvalid,
                ValidationObject::RangeTree,
                Some(page_number),
                None,
                None,
            )?;
        }
        previous = Some(key);
        let actual = validate_node(
            context,
            child,
            Some(header.level - 1),
            false,
            path,
            depth + 1,
            state,
        )?;
        if actual.is_some_and(|actual| actual != key) {
            context.emit(
                ValidationReason::TreeFenceInvalid,
                ValidationObject::RangeTree,
                Some(page_number),
                None,
                None,
            )?;
        }
    }
    Ok(first)
}

fn count_mismatch<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    context.emit(
        ValidationReason::RootCountInvalid,
        ValidationObject::RangeTree,
        None,
        None,
        None,
    )
}

#[derive(Clone, Copy)]
struct Range<K> {
    from: K,
    to: K,
    value: u32,
}

struct RangeState<K> {
    count: u64,
    previous: Option<Range<K>>,
}
