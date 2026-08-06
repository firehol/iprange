use crate::cancellation::CancellationToken;
use crate::contract::{u16_le, u32_le, MetaV4, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::crc32c;
use crate::error::Result;
use crate::key::IpKey;
use crate::mapping::{ByteSource, Mapping, PageView};
use crate::range_tree::{self, Record};
use crate::slotted_page::{self, Header};
use crate::validation::ValidationReason;

use super::page_set::PageSet;

pub(crate) trait RangeEvents<K> {
    fn page_accepted(&mut self) -> Result<()>;
    fn page_rejected(&mut self, io_unreadable: bool) -> Result<()>;
    fn unknown(
        &mut self,
        reason: ValidationReason,
        page: Option<u32>,
        unbounded: bool,
    ) -> Result<()>;
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
    if depth >= path.len() {
        events.unknown(ValidationReason::TreeLevelInvalid, Some(page_number), true)?;
        return Ok(false);
    }
    if page_number < 2 || u64::from(page_number) >= meta.page_count {
        events.unknown(ValidationReason::PageOutOfBounds, Some(page_number), true)?;
        return Ok(false);
    }
    if !pages.insert(page_number)? {
        let reason = repeated_reason(&path[..depth], page_number);
        events.unknown(reason, Some(page_number), true)?;
        return Ok(false);
    }
    path[depth] = page_number;
    Ok(true)
}

fn repeated_reason(path: &[u32], page_number: u32) -> ValidationReason {
    if path.contains(&page_number) {
        ValidationReason::TreeCycle
    } else {
        ValidationReason::PageAlias
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
    let page = match mapping.page(page_number, meta.page_count) {
        Ok(page) => page,
        Err(_) => {
            reject_page(events, page_number, ValidationReason::IoError, true)?;
            return Ok(None);
        }
    };
    if crc32c::crc32c_source_with_zeroed(page, 28, 4) != Some(u32_le(page, 28)) {
        reject_page(
            events,
            page_number,
            ValidationReason::PageCrcMismatch,
            false,
        )?;
        return Ok(None);
    }
    Ok(Some(page))
}

fn parse_range_page<K: IpKey, E: RangeEvents<K>>(
    page: PageView<'_>,
    meta: MetaV4,
    page_number: u32,
    expected_level: Option<u16>,
    events: &mut E,
) -> Result<Option<Header>> {
    let level = u16_le(page, 18);
    let expected_type = if level == 0 {
        range_tree::RANGE_LEAF
    } else {
        range_tree::RANGE_BRANCH
    };
    if page.byte(4) != Some(expected_type) {
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
    if !fixed_layout_valid(page, &header, cell_len) {
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
        K::WIDTH * 2 + 4
    } else {
        K::WIDTH + 4
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
        let cell = slotted_page::cell(page, &header, index, K::WIDTH * 2 + 4)?;
        let from = K::read_le(cell, 0);
        let to = K::read_le(cell, K::WIDTH);
        first.get_or_insert(from);
        if previous.is_some_and(|value| value >= from) {
            events.unknown(ValidationReason::TreeOrderInvalid, Some(page_number), false)?;
        }
        previous = Some(from);
        let record = (from <= to).then(|| Record {
            from,
            to,
            value: u32_le(cell, K::WIDTH * 2),
        });
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
        let cell = slotted_page::cell(page, &header, index, K::WIDTH + 4)?;
        let key = K::read_le(cell, 0);
        let child = u32_le(cell, K::WIDTH);
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

fn fixed_layout_valid<S: ByteSource>(page: S, header: &Header, cell_len: usize) -> bool {
    let mut used = [0u64; PAGE_SIZE / 64];
    let mut minimum = PAGE_SIZE;
    for index in 0..header.item_count {
        let slot = 32 + index * 2;
        let start = usize::from(u16_le(page, slot));
        let Some(end) = start.checked_add(cell_len) else {
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
