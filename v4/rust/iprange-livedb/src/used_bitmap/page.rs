//! Fixed bitmap-page encoding and selected-path checks.

use crate::bitmap_page::{self, Kind};
use crate::error::{Error, Result};
use crate::mapping::ByteSource;
use crate::page_io::{PageEdit, PageSink};

pub(super) use crate::bitmap_page::{Header, BRANCH_CHILDREN, LEAF_WORDS};

pub(super) fn parse<S: ByteSource>(
    page: S,
    selected_txn: u64,
    kind: Kind,
    expected_level: Option<u16>,
    _base: u64,
    _limit: u64,
) -> Result<Header> {
    bitmap_page::inspect_header(page, selected_txn, kind, expected_level)
}

pub(super) fn set_branch_child<D: PageEdit>(
    page: &mut D,
    header: &Header,
    index: usize,
    child: u32,
    candidate: bool,
) -> Result<()> {
    if header.level == 0 || index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("used bitmap child index is invalid"));
    }
    let old = bitmap_page::branch_child(page.view(), index)?;
    bitmap_page::set_branch_child(page, index, child)?;
    set_summary(page, index, candidate)?;
    let count = header
        .item_count
        .checked_add(usize::from(old == 0 && child != 0))
        .and_then(|count| count.checked_sub(usize::from(old != 0 && child == 0)))
        .ok_or(Error::Corrupt("used bitmap child count underflows"))?;
    page.put_u16(crate::page_header::ITEM_COUNT, count as u16)
}

pub(super) fn set_pointer<D: PageEdit>(
    page: &mut D,
    header: &Header,
    index: usize,
    child: u32,
) -> Result<()> {
    let candidate = summary_bit(page.view(), index);
    set_branch_child(page, header, index, child, candidate)
}

pub(super) fn branch_child<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    page_limit: u64,
) -> Result<u32> {
    if header.level == 0 || index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("used bitmap child lookup is invalid"));
    }
    let child = bitmap_page::branch_child(page, index)?;
    if child != 0 && (child < 2 || u64::from(child) >= page_limit) {
        return Err(Error::Corrupt("used bitmap child is outside page bounds"));
    }
    Ok(child)
}

pub(super) fn lowest_leaf<S: ByteSource>(
    page: S,
    base: u64,
    start: u64,
    limit: u64,
) -> Result<Option<u64>> {
    let local = start.saturating_sub(base);
    let mut index = (local / 64) as usize;
    while index < LEAF_WORDS {
        let word_base = base + index as u64 * 64;
        if word_base >= limit {
            return Ok(None);
        }
        let mut candidates = !bitmap_page::leaf_word(page, index)?;
        if index == (local / 64) as usize {
            candidates &= u64::MAX << (local % 64);
        }
        if limit - word_base < 64 {
            candidates &= (1u64 << (limit - word_base)) - 1;
        }
        if candidates != 0 {
            return Ok(Some(word_base + u64::from(candidates.trailing_zeros())));
        }
        index += 1;
    }
    Ok(None)
}

pub(super) fn page_has_candidate<S: ByteSource>(
    page: S,
    base: u64,
    limit: u64,
    kind: Kind,
) -> Result<bool> {
    if crate::page_header::level(page) == 0 {
        Ok(lowest_leaf(page, base, base.max(kind.first_candidate()), limit)?.is_some())
    } else {
        Ok(first_summary(page, 0).is_some())
    }
}

pub(super) fn stamp_leaf<D: PageSink>(page: &mut D, count: usize) -> Result<()> {
    page.put_u16(crate::page_header::ITEM_COUNT, count as u16)
}

pub(super) fn initialize<D: PageSink>(page: &mut D, txn: u64, level: u16, kind: Kind) {
    bitmap_page::initialize(page, txn, level, kind);
}

pub(super) fn initialize_summary<D: PageEdit>(
    page: &mut D,
    base: u64,
    span: u64,
    first: u64,
    limit: u64,
) -> Result<()> {
    for index in 0..BRANCH_CHILDREN {
        let child_base = base + span * index as u64;
        set_summary(
            page,
            index,
            coverage_intersects(child_base, span, first, limit),
        )?;
    }
    Ok(())
}

pub(super) fn coverage_intersects(base: u64, span: u64, first: u64, limit: u64) -> bool {
    base.max(first) < base.saturating_add(span).min(limit)
}

fn set_summary<D: PageEdit>(page: &mut D, index: usize, value: bool) -> Result<()> {
    bitmap_page::set_summary(page, index, value)
}

pub(super) fn summary_bit<S: ByteSource>(page: S, index: usize) -> bool {
    bitmap_page::summary_bit(page, index).expect("bounded bitmap summary index")
}

pub(super) fn first_summary<S: ByteSource>(page: S, start: usize) -> Option<usize> {
    bitmap_page::first_summary(page, start)
}
