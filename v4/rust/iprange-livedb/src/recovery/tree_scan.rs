//! CRC-checked salvage traversal for reachable slotted trees.

use crate::cancellation::CancellationToken;
use crate::contract::{u16_le, u32_le, MetaV4, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::crc32c;
use crate::error::Result;
use crate::mapping::{ByteRange, ByteSource, Mapping, PageView};
use crate::slotted_page::{self, Header};
use crate::validation::{ValidationObject, ValidationReason};

use super::page_set::PageSet;

#[derive(Clone, Copy)]
pub(crate) enum CellLayout {
    Fixed(usize),
    Variable { minimum: usize, maximum: usize },
}

pub(crate) trait Codec {
    type Key: Copy + Ord;

    const OBJECT: ValidationObject;
    const BRANCH_TYPE: u8;
    const LEAF_TYPE: u8;
    const AUX: u32;
    const BRANCH_LAYOUT: CellLayout;
    const LEAF_LAYOUT: CellLayout;
    const BRANCH_INVALID: ValidationReason = ValidationReason::TreeOrderInvalid;
    const LEAF_INVALID: ValidationReason;

    fn branch<P: ByteSource>(cell: P) -> Option<(Self::Key, u32)>;
    fn leaf_key<P: ByteSource>(cell: P) -> Option<Self::Key>;
}

pub(crate) trait TreeEvents {
    fn page_accepted(&mut self) -> Result<()>;
    fn page_rejected(&mut self, io_unreadable: bool) -> Result<()>;
    fn unknown(
        &mut self,
        reason: ValidationReason,
        object: ValidationObject,
        page: Option<u32>,
    ) -> Result<()>;
    fn leaf<P: ByteSource>(&mut self, page: u32, index: usize, cell: Option<P>) -> Result<()>;
}

pub(crate) fn scan<C: Codec, E: TreeEvents>(
    mapping: &Mapping,
    meta: MetaV4,
    root: u32,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
    events: &mut E,
) -> Result<()> {
    if root == 0 {
        return Ok(());
    }
    let mut path = [0; MAX_TREE_LEVEL as usize + 1];
    let mut state = State::<C::Key> { previous: None };
    scan_node::<C, E>(
        ScanContext {
            mapping,
            meta,
            object: C::OBJECT,
            pages,
            cancellation,
        },
        root,
        None,
        true,
        &mut path,
        0,
        &mut state,
        events,
    )?;
    Ok(())
}

struct State<K> {
    previous: Option<K>,
}

struct ScanContext<'a> {
    mapping: &'a Mapping,
    meta: MetaV4,
    object: ValidationObject,
    pages: &'a mut PageSet,
    cancellation: &'a CancellationToken,
}

#[allow(clippy::too_many_arguments)]
fn scan_node<C: Codec, E: TreeEvents>(
    mut context: ScanContext<'_>,
    page_number: u32,
    expected_level: Option<u16>,
    root: bool,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    state: &mut State<C::Key>,
    events: &mut E,
) -> Result<Option<C::Key>> {
    context.cancellation.check()?;
    if !claim_page(&mut context, page_number, path, depth, events)? {
        state.previous = None;
        return Ok(None);
    }
    let Some((page, header)) = read_page::<C, E>(&context, page_number, expected_level, events)?
    else {
        state.previous = None;
        return Ok(None);
    };
    if collapsed_root(root, header) {
        events.unknown(
            ValidationReason::TreeLevelInvalid,
            context.object,
            Some(page_number),
        )?;
    }
    if header.level == 0 {
        scan_leaf::<C, E>(page, header, page_number, context.object, state, events)
    } else {
        scan_branch::<C, E>(
            context,
            page,
            header,
            page_number,
            path,
            depth,
            state,
            events,
        )
    }
}

fn collapsed_root(root: bool, header: Header) -> bool {
    root && header.level > 0 && header.item_count == 1
}

fn claim_page<E: TreeEvents>(
    context: &mut ScanContext<'_>,
    page_number: u32,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    events: &mut E,
) -> Result<bool> {
    if depth >= path.len() {
        events.unknown(
            ValidationReason::TreeLevelInvalid,
            context.object,
            Some(page_number),
        )?;
        return Ok(false);
    }
    if page_number < 2 || u64::from(page_number) >= context.meta.page_count {
        events.unknown(
            ValidationReason::PageOutOfBounds,
            context.object,
            Some(page_number),
        )?;
        return Ok(false);
    }
    if !context.pages.insert(page_number)? {
        events.unknown(
            repeated_reason(&path[..depth], page_number),
            context.object,
            Some(page_number),
        )?;
        return Ok(false);
    }
    path[depth] = page_number;
    Ok(true)
}

fn repeated_reason(path: &[u32], page: u32) -> ValidationReason {
    if path.contains(&page) {
        ValidationReason::TreeCycle
    } else {
        ValidationReason::PageAlias
    }
}

fn read_page<'m, C: Codec, E: TreeEvents>(
    context: &ScanContext<'m>,
    page_number: u32,
    expected_level: Option<u16>,
    events: &mut E,
) -> Result<Option<(PageView<'m>, Header)>> {
    let Some(page) = load_page(context, page_number, events)? else {
        return Ok(None);
    };
    let level = u16_le(page, 18);
    let page_type = if level == 0 {
        C::LEAF_TYPE
    } else {
        C::BRANCH_TYPE
    };
    let header =
        match slotted_page::parse(page, context.meta.txn_id, page_type, C::AUX, expected_level) {
            Ok(header) => header,
            Err(_) => {
                reject_page(
                    events,
                    context.object,
                    page_number,
                    header_reason::<C, _>(page),
                    false,
                )?;
                return Ok(None);
            }
        };
    let layout = if level == 0 {
        C::LEAF_LAYOUT
    } else {
        C::BRANCH_LAYOUT
    };
    if !layout_valid(page, &header, layout) {
        reject_page(
            events,
            context.object,
            page_number,
            ValidationReason::PageHeaderInvalid,
            false,
        )?;
        return Ok(None);
    }
    events.page_accepted()?;
    Ok(Some((page, header)))
}

fn load_page<'m, E: TreeEvents>(
    context: &ScanContext<'m>,
    page_number: u32,
    events: &mut E,
) -> Result<Option<PageView<'m>>> {
    let page = match context.mapping.page(page_number, context.meta.page_count) {
        Ok(page) => page,
        Err(_) => {
            reject_page(
                events,
                context.object,
                page_number,
                ValidationReason::IoError,
                true,
            )?;
            return Ok(None);
        }
    };
    if crc32c::crc32c_source_with_zeroed(page, 28, 4) != Some(u32_le(page, 28)) {
        reject_page(
            events,
            context.object,
            page_number,
            ValidationReason::PageCrcMismatch,
            false,
        )?;
        return Ok(None);
    }
    Ok(Some(page))
}

fn header_reason<C: Codec, P: ByteSource>(page: P) -> ValidationReason {
    let expected = if u16_le(page, 18) == 0 {
        C::LEAF_TYPE
    } else {
        C::BRANCH_TYPE
    };
    if page.byte(4) != Some(expected) || u32_le(page, 24) != C::AUX {
        ValidationReason::PageTypeMismatch
    } else {
        ValidationReason::PageHeaderInvalid
    }
}

fn reject_page<E: TreeEvents>(
    events: &mut E,
    object: ValidationObject,
    page_number: u32,
    reason: ValidationReason,
    io_unreadable: bool,
) -> Result<()> {
    events.page_rejected(io_unreadable)?;
    events.unknown(reason, object, Some(page_number))
}

fn scan_leaf<C: Codec, E: TreeEvents>(
    page: PageView<'_>,
    header: Header,
    page_number: u32,
    object: ValidationObject,
    state: &mut State<C::Key>,
    events: &mut E,
) -> Result<Option<C::Key>> {
    let mut first = None;
    for index in 0..header.item_count {
        let cell = cell(page, &header, index, C::LEAF_LAYOUT)?;
        let Some(key) = C::leaf_key(cell) else {
            events.unknown(C::LEAF_INVALID, object, Some(page_number))?;
            events.leaf::<ByteRange<PageView<'_>>>(page_number, index, None)?;
            state.previous = None;
            continue;
        };
        first.get_or_insert(key);
        if state.previous.is_some_and(|previous| previous >= key) {
            events.unknown(
                ValidationReason::TreeOrderInvalid,
                object,
                Some(page_number),
            )?;
        }
        state.previous = Some(key);
        events.leaf(page_number, index, Some(cell))?;
    }
    Ok(first)
}

#[allow(clippy::too_many_arguments)]
fn scan_branch<C: Codec, E: TreeEvents>(
    context: ScanContext<'_>,
    page: PageView<'_>,
    header: Header,
    page_number: u32,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    state: &mut State<C::Key>,
    events: &mut E,
) -> Result<Option<C::Key>> {
    let mut first = None;
    let mut previous = None;
    for index in 0..header.item_count {
        context.cancellation.check()?;
        let cell = cell(page, &header, index, C::BRANCH_LAYOUT)?;
        let Some((key, child)) = branch_cell::<C, E, _>(
            cell,
            page_number,
            context.object,
            &mut first,
            &mut previous,
            events,
        )?
        else {
            state.previous = None;
            continue;
        };
        let actual = scan_node::<C, E>(
            ScanContext {
                mapping: context.mapping,
                meta: context.meta,
                object: context.object,
                pages: context.pages,
                cancellation: context.cancellation,
            },
            child,
            Some(header.level - 1),
            false,
            path,
            depth + 1,
            state,
            events,
        )?;
        if actual.is_some_and(|actual| actual != key) {
            events.unknown(
                ValidationReason::TreeFenceInvalid,
                context.object,
                Some(page_number),
            )?;
        }
    }
    Ok(first)
}

fn branch_cell<C: Codec, E: TreeEvents, P: ByteSource>(
    cell: P,
    page_number: u32,
    object: ValidationObject,
    first: &mut Option<C::Key>,
    previous: &mut Option<C::Key>,
    events: &mut E,
) -> Result<Option<(C::Key, u32)>> {
    let Some((key, child)) = C::branch(cell) else {
        events.unknown(C::BRANCH_INVALID, object, Some(page_number))?;
        return Ok(None);
    };
    first.get_or_insert(key);
    if previous.is_some_and(|prior| prior >= key) {
        events.unknown(
            ValidationReason::TreeOrderInvalid,
            object,
            Some(page_number),
        )?;
    }
    *previous = Some(key);
    Ok(Some((key, child)))
}

fn cell<'a>(
    page: PageView<'a>,
    header: &Header,
    index: usize,
    layout: CellLayout,
) -> Result<ByteRange<PageView<'a>>> {
    match layout {
        CellLayout::Fixed(length) => slotted_page::cell(page, header, index, length),
        CellLayout::Variable { minimum, maximum } => {
            slotted_page::record(page, header, index, minimum, maximum)
        }
    }
}

pub(crate) fn layout_valid<P: ByteSource>(page: P, header: &Header, layout: CellLayout) -> bool {
    let mut used = [0u64; PAGE_SIZE / 64];
    let mut minimum = PAGE_SIZE;
    for index in 0..header.item_count {
        let slot = slotted_page::HEADER_SIZE + index * 2;
        let start = usize::from(u16_le(page, slot));
        let Some(length) = cell_length(page, start, layout) else {
            return false;
        };
        let Some(end) = start.checked_add(length) else {
            return false;
        };
        if start < header.upper || end > PAGE_SIZE || !mark(&mut used, start, end) {
            return false;
        }
        minimum = minimum.min(start);
    }
    if minimum != header.upper
        || !(header.lower..header.upper).all(|position| page.byte(position) == Some(0))
    {
        return false;
    }
    (header.upper..PAGE_SIZE)
        .all(|position| marked(&used, position) || page.byte(position) == Some(0))
}

fn cell_length<P: ByteSource>(page: P, start: usize, layout: CellLayout) -> Option<usize> {
    match layout {
        CellLayout::Fixed(length) => Some(length),
        CellLayout::Variable { minimum, maximum } => {
            let length = usize::from(u16_le(page, start));
            (minimum..=maximum).contains(&length).then_some(length)
        }
    }
}

fn mark(bits: &mut [u64; PAGE_SIZE / 64], start: usize, end: usize) -> bool {
    for position in start..end {
        let word = position / 64;
        let mask = 1u64 << (position % 64);
        if bits[word] & mask != 0 {
            return false;
        }
        bits[word] |= mask;
    }
    true
}

fn marked(bits: &[u64; PAGE_SIZE / 64], position: usize) -> bool {
    bits[position / 64] & (1u64 << (position % 64)) != 0
}
