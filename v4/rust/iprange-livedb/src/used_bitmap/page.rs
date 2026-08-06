//! Fixed bitmap-page encoding and selected-path checks.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::slotted_page::{put_u16, put_u32, put_u64, HEADER_SIZE};

use super::Kind;

pub(super) const BRANCH_TYPE: u8 = 14;
pub(super) const LEAF_TYPE: u8 = 15;
pub(super) const LEAF_WORDS: usize = 500;
pub(super) const LEAF_BITS: u64 = (LEAF_WORDS * 64) as u64;
pub(super) const BRANCH_CHILDREN: usize = 256;
pub(super) const LEAF_END: usize = HEADER_SIZE + LEAF_WORDS * 8;
pub(super) const BRANCH_END: usize = HEADER_SIZE + 32 + BRANCH_CHILDREN * 4;
pub(super) const MAX_LEVEL: u16 = 3;

#[derive(Clone, Copy)]
pub(super) struct Header {
    pub(super) level: u16,
    pub(super) item_count: usize,
}

pub(super) fn parse(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    kind: Kind,
    expected_level: Option<u16>,
    _base: u64,
    _limit: u64,
) -> Result<Header> {
    let level = u16_le(page, 18);
    let leaf = level == 0;
    require_identity(page, selected_txn, kind, expected_level, level, leaf)?;
    require_layout(page, level, leaf)
}

fn require_identity(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    kind: Kind,
    expected_level: Option<u16>,
    level: u16,
    leaf: bool,
) -> Result<()> {
    require_common_identity(page, selected_txn, leaf)?;
    require_kind_and_level(page, kind, expected_level, level)
}

fn require_common_identity(page: &[u8; PAGE_SIZE], selected_txn: u64, leaf: bool) -> Result<()> {
    if page[..4] != PAGE_MAGIC
        || page[4] != if leaf { LEAF_TYPE } else { BRANCH_TYPE }
        || page[5] != 0
        || u16_le(page, 6) != HEADER_SIZE as u16
        || u64_le(page, 8) == 0
        || u64_le(page, 8) > selected_txn
    {
        return Err(Error::Corrupt("used bitmap page header is invalid"));
    }
    Ok(())
}

fn require_kind_and_level(
    page: &[u8; PAGE_SIZE],
    kind: Kind,
    expected_level: Option<u16>,
    level: u16,
) -> Result<()> {
    if expected_level.is_some_and(|expected| expected != level)
        || level > MAX_LEVEL
        || u32_le(page, 24) != kind as u32
    {
        return Err(Error::Corrupt("used bitmap page header is invalid"));
    }
    Ok(())
}

fn require_layout(page: &[u8; PAGE_SIZE], level: u16, leaf: bool) -> Result<Header> {
    let lower = if leaf { LEAF_END } else { BRANCH_END };
    if u16_le(page, 20) as usize != lower || u16_le(page, 22) as usize != PAGE_SIZE {
        return Err(Error::Corrupt("used bitmap page layout is invalid"));
    }
    let item_count = usize::from(u16_le(page, 16));
    let maximum = if leaf { LEAF_WORDS } else { BRANCH_CHILDREN };
    if item_count == 0 || item_count > maximum {
        return Err(Error::Corrupt("used bitmap page count is invalid"));
    }
    Ok(Header { level, item_count })
}

pub(super) fn set_branch_child(
    page: &mut [u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    child: u32,
    candidate: bool,
) -> Result<()> {
    if header.level == 0 || index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("used bitmap child index is invalid"));
    }
    let at = HEADER_SIZE + 32 + index * 4;
    let old = u32_le(page, at);
    put_u32(page, at, child);
    set_summary(page, index, candidate);
    let count = header
        .item_count
        .checked_add(usize::from(old == 0 && child != 0))
        .and_then(|count| count.checked_sub(usize::from(old != 0 && child == 0)))
        .ok_or(Error::Corrupt("used bitmap child count underflows"))?;
    stamp_branch(page, count)
}

pub(super) fn set_pointer(
    page: &mut [u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    child: u32,
) -> Result<()> {
    let candidate = summary_bit(page, index);
    set_branch_child(page, header, index, child, candidate)
}

pub(super) fn branch_child(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    page_limit: u64,
) -> Result<u32> {
    if header.level == 0 || index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("used bitmap child lookup is invalid"));
    }
    let child = u32_le(page, HEADER_SIZE + 32 + index * 4);
    if child != 0 && (child < 2 || u64::from(child) >= page_limit) {
        return Err(Error::Corrupt("used bitmap child is outside page bounds"));
    }
    Ok(child)
}

pub(super) fn lowest_leaf(
    page: &[u8; PAGE_SIZE],
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
        let mut candidates = !u64_le(page, HEADER_SIZE + index * 8);
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

pub(super) fn page_has_candidate(
    page: &[u8; PAGE_SIZE],
    base: u64,
    limit: u64,
    kind: Kind,
) -> Result<bool> {
    if u16_le(page, 18) == 0 {
        Ok(lowest_leaf(page, base, base.max(kind.first()), limit)?.is_some())
    } else {
        Ok(first_summary(page, 0).is_some())
    }
}

pub(super) fn stamp(_page: &mut [u8; PAGE_SIZE]) -> Result<()> {
    Ok(())
}

pub(super) fn stamp_leaf(page: &mut [u8; PAGE_SIZE], count: usize) -> Result<()> {
    put_u16(page, 16, count as u16);
    stamp(page)
}

fn stamp_branch(page: &mut [u8; PAGE_SIZE], count: usize) -> Result<()> {
    put_u16(page, 16, count as u16);
    stamp(page)
}

pub(super) fn new_page(
    page_type: u8,
    txn: u64,
    level: u16,
    kind: Kind,
    lower: usize,
) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    page[..4].copy_from_slice(&PAGE_MAGIC);
    page[4] = page_type;
    put_u16(&mut page, 6, HEADER_SIZE as u16);
    put_u64(&mut page, 8, txn);
    put_u16(&mut page, 18, level);
    put_u16(&mut page, 20, lower as u16);
    put_u16(&mut page, 22, PAGE_SIZE as u16);
    put_u32(&mut page, 24, kind as u32);
    page
}

pub(super) fn initialize_summary(
    page: &mut [u8; PAGE_SIZE],
    base: u64,
    span: u64,
    first: u64,
    limit: u64,
) {
    for index in 0..BRANCH_CHILDREN {
        let child_base = base + span * index as u64;
        set_summary(
            page,
            index,
            coverage_intersects(child_base, span, first, limit),
        );
    }
}

pub(super) fn coverage_intersects(base: u64, span: u64, first: u64, limit: u64) -> bool {
    base.max(first) < base.saturating_add(span).min(limit)
}

fn set_summary(page: &mut [u8; PAGE_SIZE], index: usize, value: bool) {
    let at = HEADER_SIZE + (index / 64) * 8;
    let mask = 1u64 << (index % 64);
    let word = u64_le(page, at);
    put_u64(page, at, if value { word | mask } else { word & !mask });
}

pub(super) fn summary_bit(page: &[u8; PAGE_SIZE], index: usize) -> bool {
    u64_le(page, HEADER_SIZE + (index / 64) * 8) & (1u64 << (index % 64)) != 0
}

pub(super) fn first_summary(page: &[u8; PAGE_SIZE], start: usize) -> Option<usize> {
    if start >= BRANCH_CHILDREN {
        return None;
    }
    let mut word_index = start / 64;
    let mut word = u64_le(page, HEADER_SIZE + word_index * 8) & (u64::MAX << (start % 64));
    loop {
        if word != 0 {
            return Some(word_index * 64 + word.trailing_zeros() as usize);
        }
        word_index += 1;
        if word_index == 4 {
            return None;
        }
        word = u64_le(page, HEADER_SIZE + word_index * 8);
    }
}
