use crate::contract::{AddressFamily, ValueKind, MAX_TREE_LEVEL};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::mapping::{ByteRange, ByteSource, PageView};
use crate::range_tree::{self, Record as Range};
use crate::slotted_page::LayoutInspection;

use super::context::Context;
use super::membership_table::CountResult;
use super::page::{self, TreePageSpec};
use super::{ValidationObject, ValidationReason, ValidationSink};

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
    let Some((header, cells)) =
        validate_range_page::<K, S>(context, page_number, page, expected_level, root)?
    else {
        state.previous = None;
        return Ok(None);
    };
    if header.level == 0 {
        validate_leaf(context, page_number, cells, state)
    } else {
        validate_branch(context, page_number, cells, header, path, depth, state)
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

fn validate_range_page<'m, K: IpKey, S: ValidationSink>(
    context: &mut Context<'m, S>,
    page_number: u32,
    page: PageView<'m>,
    expected_level: Option<u16>,
    root: bool,
) -> Result<Option<(page::SlottedHeader, LayoutInspection<PageView<'m>>)>> {
    let Some(header) = page::slotted_header(
        context,
        page_number,
        page,
        ValidationObject::RangeTree,
        TreePageSpec {
            branch_type: range_tree::RANGE_BRANCH,
            leaf_type: range_tree::RANGE_LEAF,
            aux: K::FAMILY as u32,
            expected_level,
        },
    )?
    else {
        return Ok(None);
    };
    report_degenerate_root(context, page_number, header, root)?;
    let cell_len = range_cell_len::<K>(header.level);
    let Some(cells) = page::validate_fixed_cells(
        context,
        page_number,
        page,
        ValidationObject::RangeTree,
        header,
        cell_len,
    )?
    else {
        return Ok(None);
    };
    Ok(Some((header, cells)))
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
        range_tree::record_size::<K>()
    } else {
        range_tree::branch_size::<K>()
    }
}

fn validate_leaf<K: IpKey, S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    cells: LayoutInspection<PageView<'_>>,
    state: &mut RangeState<K>,
) -> Result<Option<K>> {
    if context.meta.value_kind == ValueKind::Membership {
        validate_leaf_cells::<K, S, true>(context, page_number, cells, state)
    } else {
        validate_leaf_cells::<K, S, false>(context, page_number, cells, state)
    }
}

fn validate_leaf_cells<K: IpKey, S: ValidationSink, const MEMBERSHIP: bool>(
    context: &mut Context<'_, S>,
    page_number: u32,
    cells: LayoutInspection<PageView<'_>>,
    state: &mut RangeState<K>,
) -> Result<Option<K>> {
    let mut order = LeafOrder::default();
    for cell in cells.cells() {
        validate_leaf_cell::<K, S, _, MEMBERSHIP>(context, page_number, cell, &mut order, state)?;
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
    fn observe(&mut self, key: K) -> bool {
        self.first.get_or_insert(key);
        let invalid = self.previous.is_some_and(|previous| previous >= key);
        self.previous = Some(key);
        invalid
    }
}

#[allow(clippy::too_many_arguments)]
fn validate_leaf_cell<K: IpKey, S: ValidationSink, P: ByteSource, const MEMBERSHIP: bool>(
    context: &mut Context<'_, S>,
    page_number: u32,
    cell: ByteRange<P>,
    order: &mut LeafOrder<K>,
    state: &mut RangeState<K>,
) -> Result<()> {
    let current = read_range_cell::<K, _>(cell)?;
    if order.observe(current.from) {
        emit_range_finding(context, page_number, ValidationReason::TreeOrderInvalid)?;
    }
    // Every reachable page number and every per-page slot count fit in u32 and
    // u16 respectively, so the total range count cannot overflow u64.
    state.count += 1;
    if current.from > current.to {
        emit_range_finding(context, page_number, ValidationReason::RangeReversed)?;
        state.previous = None;
        return Ok(());
    }
    if MEMBERSHIP {
        if current.value == 0 {
            emit_range_finding(
                context,
                page_number,
                ValidationReason::MembershipBitmapInvalid,
            )?;
        } else {
            match context.count_membership_range(current.value) {
                CountResult::Full => emit_range_finding(
                    context,
                    page_number,
                    ValidationReason::MembershipRefcountInvalid,
                )?,
                CountResult::Cancelled => return Err(Error::Cancelled),
                CountResult::Unavailable => {
                    return Err(Error::Corrupt(
                        "membership validation has no membership table",
                    ));
                }
                CountResult::Inserted | CountResult::Existing => {}
            }
        }
    }
    if let Some(reason) = neighbor_problem(state.previous, current) {
        emit_range_finding(context, page_number, reason)?;
    }
    state.previous = Some(current);
    Ok(())
}

fn read_range_cell<K: IpKey, P: ByteSource>(cell: ByteRange<P>) -> Result<Range<K>> {
    range_tree::decode_fields(cell)
}

fn emit_range_finding<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    reason: ValidationReason,
) -> Result<()> {
    context.emit(
        reason,
        ValidationObject::RangeTree,
        Some(page_number),
        None,
        None,
    )
}

fn neighbor_problem<K: IpKey>(
    previous: Option<Range<K>>,
    current: Range<K>,
) -> Option<ValidationReason> {
    let previous = previous?;
    if current.from <= previous.to {
        Some(ValidationReason::RangeOverlap)
    } else if previous.to.checked_next() == Some(current.from) && previous.value == current.value {
        Some(ValidationReason::RangeNotCoalesced)
    } else {
        None
    }
}

fn validate_branch<K: IpKey, S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    cells: LayoutInspection<PageView<'_>>,
    header: page::SlottedHeader,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    state: &mut RangeState<K>,
) -> Result<Option<K>> {
    let mut first = None;
    let mut previous = None;
    for cell in cells.cells() {
        let (key, child) = range_tree::decode_branch(cell)?;
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

struct RangeState<K> {
    count: u64,
    previous: Option<Range<K>>,
}
