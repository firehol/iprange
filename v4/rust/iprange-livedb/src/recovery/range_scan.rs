use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, MAX_TREE_LEVEL};
use crate::error::Result;
use crate::key::IpKey;
use crate::mapping::{Mapping, PageView};
use crate::range_tree::{self, Record};
use crate::slotted_page::{self, Header};
use crate::validation::ValidationReason;

use super::page_set::{PageClaim, PageSet};

pub(crate) trait RangeEvents<K> {
    fn page_accepted(&mut self) -> Result<()> {
        Ok(())
    }
    fn page_rejected(&mut self, _io_unreadable: bool) -> Result<()> {
        Ok(())
    }
    fn unknown(
        &mut self,
        _reason: ValidationReason,
        _page: Option<u32>,
        _unbounded: bool,
    ) -> Result<()> {
        Ok(())
    }
    fn range(&mut self, page: u32, record: Option<Record<K>>) -> Result<()>;
}

pub(crate) fn scan<K: IpKey, E: RangeEvents<K>>(
    mapping: &Mapping,
    meta: MetaV4,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
    events: &mut E,
) -> Result<()> {
    if meta.range_root == 0 {
        return Ok(());
    }
    let mut path = [0; MAX_TREE_LEVEL as usize + 1];
    scan_node(
        mapping,
        meta,
        meta.range_root,
        None,
        &mut path,
        0,
        pages,
        cancellation,
        events,
    )?;
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn scan_node<K: IpKey, E: RangeEvents<K>>(
    mapping: &Mapping,
    meta: MetaV4,
    page_number: u32,
    expected_level: Option<u16>,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
    events: &mut E,
) -> Result<Option<K>> {
    cancellation.check()?;
    if !claim_page(meta, page_number, path, depth, pages, events)? {
        return Ok(None);
    }

    let Some((page, header)) =
        read_range_page::<K, E>(mapping, meta, page_number, expected_level, events)?
    else {
        return Ok(None);
    };
    if header.level == 0 {
        scan_leaf(page_number, page, header, events)
    } else {
        scan_branch(
            mapping,
            meta,
            page_number,
            page,
            header,
            path,
            depth,
            pages,
            cancellation,
            events,
        )
    }
}

fn claim_page<K, E: RangeEvents<K>>(
    meta: MetaV4,
    page_number: u32,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    pages: &mut PageSet,
    events: &mut E,
) -> Result<bool> {
    match pages.claim(page_number, meta.page_count, path, depth)? {
        PageClaim::Claimed => Ok(true),
        PageClaim::Rejected(reason) => {
            events.unknown(reason, Some(page_number), true)?;
            Ok(false)
        }
    }
}

fn read_range_page<'m, K: IpKey, E: RangeEvents<K>>(
    mapping: &'m Mapping,
    meta: MetaV4,
    page_number: u32,
    expected_level: Option<u16>,
    events: &mut E,
) -> Result<Option<(PageView<'m>, Header)>> {
    let Some(page) = load_page(mapping, meta, page_number, events)? else {
        return Ok(None);
    };
    let Some(header) = parse_range_page::<K, E>(page, meta, page_number, expected_level, events)?
    else {
        return Ok(None);
    };
    events.page_accepted()?;
    Ok(Some((page, header)))
}

fn load_page<'m, K, E: RangeEvents<K>>(
    mapping: &'m Mapping,
    meta: MetaV4,
    page_number: u32,
    events: &mut E,
) -> Result<Option<PageView<'m>>> {
    let page = match super::page_read::checked(mapping, page_number, meta.page_count) {
        Ok(page) => page,
        Err(problem) => {
            reject_page(events, page_number, problem.reason, problem.io_unreadable)?;
            return Ok(None);
        }
    };
    Ok(Some(page))
}

fn parse_range_page<K: IpKey, E: RangeEvents<K>>(
    page: PageView<'_>,
    meta: MetaV4,
    page_number: u32,
    expected_level: Option<u16>,
    events: &mut E,
) -> Result<Option<Header>> {
    if !slotted_page::tree_kind_valid(
        page,
        range_tree::RANGE_BRANCH,
        range_tree::RANGE_LEAF,
        K::FAMILY as u32,
    ) {
        reject_page(
            events,
            page_number,
            ValidationReason::PageTypeMismatch,
            false,
        )?;
        return Ok(None);
    }
    let header = match range_tree::parse_header::<K, _>(page, meta.txn_id, expected_level) {
        Ok(header) => header,
        Err(_) => {
            reject_page(
                events,
                page_number,
                ValidationReason::PageHeaderInvalid,
                false,
            )?;
            return Ok(None);
        }
    };
    let cell_len = range_cell_len::<K>(header.level);
    if !slotted_page::inspect_layout(page, &header, slotted_page::CellLayout::Fixed(cell_len))
        .is_some_and(|inspection| !inspection.reserved_nonzero)
    {
        reject_page(
            events,
            page_number,
            ValidationReason::PageHeaderInvalid,
            false,
        )?;
        return Ok(None);
    }
    Ok(Some(header))
}

fn range_cell_len<K: IpKey>(level: u16) -> usize {
    if level == 0 {
        range_tree::record_size::<K>()
    } else {
        range_tree::branch_size::<K>()
    }
}

fn reject_page<K, E: RangeEvents<K>>(
    events: &mut E,
    page_number: u32,
    reason: ValidationReason,
    io_unreadable: bool,
) -> Result<()> {
    events.page_rejected(io_unreadable)?;
    events.unknown(reason, Some(page_number), true)
}

fn scan_leaf<K: IpKey, E: RangeEvents<K>>(
    page_number: u32,
    page: PageView<'_>,
    header: Header,
    events: &mut E,
) -> Result<Option<K>> {
    let mut first = None;
    let mut previous = None;
    for index in 0..header.item_count {
        let cell = slotted_page::cell(page, &header, index, range_tree::record_size::<K>())?;
        let decoded = range_tree::decode_fields(cell)?;
        let from = decoded.from;
        let to = decoded.to;
        first.get_or_insert(from);
        if previous.is_some_and(|value| value >= from) {
            events.unknown(ValidationReason::TreeOrderInvalid, Some(page_number), false)?;
        }
        previous = Some(from);
        let record = (from <= to).then_some(decoded);
        if record.is_none() {
            events.unknown(ValidationReason::RangeReversed, Some(page_number), true)?;
        }
        events.range(page_number, record)?;
    }
    Ok(first)
}

#[allow(clippy::too_many_arguments)]
fn scan_branch<K: IpKey, E: RangeEvents<K>>(
    mapping: &Mapping,
    meta: MetaV4,
    page_number: u32,
    page: PageView<'_>,
    header: Header,
    path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
    depth: usize,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
    events: &mut E,
) -> Result<Option<K>> {
    let mut first = None;
    let mut previous = None;
    for index in 0..header.item_count {
        cancellation.check()?;
        let cell = slotted_page::cell(page, &header, index, range_tree::branch_size::<K>())?;
        let (key, child) = range_tree::decode_branch(cell)?;
        first.get_or_insert(key);
        if previous.is_some_and(|value| value >= key) {
            events.unknown(ValidationReason::TreeOrderInvalid, Some(page_number), false)?;
        }
        previous = Some(key);
        let actual = scan_node(
            mapping,
            meta,
            child,
            Some(header.level - 1),
            path,
            depth + 1,
            pages,
            cancellation,
            events,
        )?;
        if actual.is_some_and(|value| value != key) {
            events.unknown(ValidationReason::TreeFenceInvalid, Some(page_number), false)?;
        }
    }
    Ok(first)
}
