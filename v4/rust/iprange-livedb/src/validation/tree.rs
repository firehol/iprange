use crate::contract::MAX_TREE_LEVEL;
use crate::error::Result;
use crate::mapping::{ByteRange, ByteSource, PageView};
use crate::slotted_page::LayoutInspection;

use super::context::Context;
use super::page::{self, TreePageSpec};
use super::{ValidationObject, ValidationReason, ValidationSink};

pub(crate) use crate::slotted_page::CellLayout;

pub(crate) trait Codec {
    type Key: Copy + Ord;

    const BRANCH_TYPE: u8;
    const LEAF_TYPE: u8;
    const AUX: u32;
    const BRANCH_LAYOUT: CellLayout;
    const LEAF_LAYOUT: CellLayout;
    const BRANCH_INVALID: ValidationReason = ValidationReason::TreeOrderInvalid;
    const LEAF_INVALID: ValidationReason;

    fn branch_key<P: ByteSource>(cell: P) -> Option<Self::Key>;
    fn branch_child<P: ByteSource>(cell: P) -> Option<u32>;
    fn leaf_key<P: ByteSource>(cell: P) -> Option<Self::Key>;
}

pub(crate) struct WalkResult {
    pub(crate) records: u64,
}

pub(crate) fn walk<'m, C, S, F>(
    context: &mut Context<'m, S>,
    root: u32,
    object: ValidationObject,
    mut leaf: F,
) -> Result<WalkResult>
where
    C: Codec,
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, u32, ByteRange<PageView<'m>>) -> Result<()>,
{
    if root == 0 {
        return Ok(WalkResult { records: 0 });
    }
    let mut state = State::<C::Key> {
        records: 0,
        previous: None,
    };
    let mut path = [0; MAX_TREE_LEVEL as usize + 1];
    walk_node::<C, S, F>(
        context, root, object, None, true, &mut path, 0, &mut state, &mut leaf,
    )?;
    Ok(WalkResult {
        records: state.records,
    })
}

#[allow(clippy::too_many_arguments)]
fn walk_node<'m, C, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    object: ValidationObject,
    expected_level: Option<u16>,
    root: bool,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    state: &mut State<C::Key>,
    leaf: &mut F,
) -> Result<Option<C::Key>>
where
    C: Codec,
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, u32, ByteRange<PageView<'m>>) -> Result<()>,
{
    let Some(page) = read_node_page(context, page_number, object, path, depth)? else {
        state.previous = None;
        return Ok(None);
    };
    let Some(header) =
        read_node_header::<C, S>(context, page_number, page, object, expected_level)?
    else {
        state.previous = None;
        return Ok(None);
    };
    validate_root_shape(context, page_number, object, root, header)?;
    let layout = node_layout::<C>(header.level);
    let Some(cells) = validate_layout(context, page_number, page, object, header, layout)? else {
        state.previous = None;
        return Ok(None);
    };
    if header.level == 0 {
        walk_leaf::<C, S, F>(context, page_number, cells, object, state, leaf)
    } else {
        walk_branch::<C, S, F>(
            context,
            page_number,
            cells,
            header,
            object,
            path,
            depth,
            state,
            leaf,
        )
    }
}

fn read_node_page<'m, S: ValidationSink>(
    context: &mut Context<'m, S>,
    page_number: u32,
    object: ValidationObject,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
) -> Result<Option<PageView<'m>>> {
    let Some(slot) = path.get_mut(depth) else {
        context.emit(
            ValidationReason::TreeLevelInvalid,
            object,
            Some(page_number),
            None,
            None,
        )?;
        return Ok(None);
    };
    *slot = page_number;
    context.read_graph_page(page_number, object, &path[..depth])
}

fn read_node_header<C: Codec, S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: PageView<'_>,
    object: ValidationObject,
    expected_level: Option<u16>,
) -> Result<Option<page::SlottedHeader>> {
    page::slotted_header(
        context,
        page_number,
        page,
        object,
        TreePageSpec {
            branch_type: C::BRANCH_TYPE,
            leaf_type: C::LEAF_TYPE,
            aux: C::AUX,
            expected_level,
        },
    )
}

fn validate_root_shape<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    object: ValidationObject,
    root: bool,
    header: page::SlottedHeader,
) -> Result<()> {
    if root && header.level > 0 && header.item_count == 1 {
        context.emit(
            ValidationReason::TreeLevelInvalid,
            object,
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(())
}

fn node_layout<C: Codec>(level: u16) -> CellLayout {
    if level == 0 {
        C::LEAF_LAYOUT
    } else {
        C::BRANCH_LAYOUT
    }
}

#[allow(clippy::too_many_arguments)]
fn walk_branch<'m, C, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    cells: LayoutInspection<PageView<'m>>,
    header: page::SlottedHeader,
    object: ValidationObject,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    state: &mut State<C::Key>,
    leaf: &mut F,
) -> Result<Option<C::Key>>
where
    C: Codec,
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, u32, ByteRange<PageView<'m>>) -> Result<()>,
{
    let mut first = None;
    let mut previous = None;
    for cell in cells.cells() {
        let Some((key, child)) =
            branch_entry::<C, S, _>(context, page_number, object, cell, state)?
        else {
            continue;
        };
        record_branch_key(context, page_number, object, key, &mut first, &mut previous)?;
        let actual = walk_node::<C, S, F>(
            context,
            child,
            object,
            Some(header.level - 1),
            false,
            path,
            depth + 1,
            state,
            leaf,
        )?;
        validate_fence(context, page_number, object, key, actual)?;
    }
    Ok(first)
}

fn branch_entry<C: Codec, S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    object: ValidationObject,
    cell: P,
    state: &mut State<C::Key>,
) -> Result<Option<(C::Key, u32)>> {
    let entry = C::branch_key(cell).zip(C::branch_child(cell));
    if entry.is_none() {
        context.emit(C::BRANCH_INVALID, object, Some(page_number), None, None)?;
        state.previous = None;
    }
    Ok(entry)
}

fn record_branch_key<K: Copy + Ord, S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    object: ValidationObject,
    key: K,
    first: &mut Option<K>,
    previous: &mut Option<K>,
) -> Result<()> {
    first.get_or_insert(key);
    if previous.is_some_and(|prior| prior >= key) {
        context.emit(
            ValidationReason::TreeOrderInvalid,
            object,
            Some(page_number),
            None,
            None,
        )?;
    }
    *previous = Some(key);
    Ok(())
}

fn validate_fence<K: Copy + PartialEq, S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    object: ValidationObject,
    expected: K,
    actual: Option<K>,
) -> Result<()> {
    if actual.is_some_and(|actual| actual != expected) {
        context.emit(
            ValidationReason::TreeFenceInvalid,
            object,
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(())
}

fn walk_leaf<'m, C, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    cells: LayoutInspection<PageView<'m>>,
    object: ValidationObject,
    state: &mut State<C::Key>,
    leaf: &mut F,
) -> Result<Option<C::Key>>
where
    C: Codec,
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, u32, ByteRange<PageView<'m>>) -> Result<()>,
{
    let mut first = None;
    for cell in cells.cells() {
        walk_leaf_cell::<C, S, F>(context, page_number, cell, object, state, &mut first, leaf)?;
    }
    Ok(first)
}

fn walk_leaf_cell<'m, C, S, F>(
    context: &mut Context<'m, S>,
    page_number: u32,
    cell: ByteRange<PageView<'m>>,
    object: ValidationObject,
    state: &mut State<C::Key>,
    first: &mut Option<C::Key>,
    leaf: &mut F,
) -> Result<()>
where
    C: Codec,
    S: ValidationSink,
    F: FnMut(&mut Context<'m, S>, u32, ByteRange<PageView<'m>>) -> Result<()>,
{
    let Some(key) = C::leaf_key(cell) else {
        context.emit(C::LEAF_INVALID, object, Some(page_number), None, None)?;
        state.previous = None;
        return Ok(());
    };
    first.get_or_insert(key);
    if state.previous.is_some_and(|previous| previous >= key) {
        context.emit(
            ValidationReason::TreeOrderInvalid,
            object,
            Some(page_number),
            None,
            None,
        )?;
    }
    state.previous = Some(key);
    state.records = state
        .records
        .checked_add(1)
        .ok_or(crate::error::Error::ArithmeticOverflow(
            "validation tree record count",
        ))?;
    leaf(context, page_number, cell)
}

struct State<K> {
    records: u64,
    previous: Option<K>,
}

fn validate_layout<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
    object: ValidationObject,
    header: page::SlottedHeader,
    layout: CellLayout,
) -> Result<Option<LayoutInspection<P>>> {
    match layout {
        CellLayout::Fixed(length) => {
            page::validate_fixed_cells(context, page_number, page, object, header, length)
        }
        CellLayout::Variable { minimum, maximum } => page::validate_variable_cells(
            context,
            page_number,
            page,
            object,
            header,
            minimum,
            maximum,
        ),
    }
}
