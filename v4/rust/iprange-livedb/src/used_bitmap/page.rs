//! Fixed bitmap-page encoding and selected-path checks.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::mapping::ByteSource;
use crate::slotted_page::{PageEdit, PageSink, HEADER_SIZE};

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

pub(super) fn parse<S: ByteSource>(
    page: S,
    selected_txn: u64,
    kind: Kind,
    expected_level: Option<u16>,
    _base: u64,
    _limit: u64,
) -> Result<Header> {
    let level = u16_le(page, 18);
    let leaf = level == 0;
    if page.len() != PAGE_SIZE
        || !page.equals(0, &PAGE_MAGIC)
        || page.byte(4) != Some(if leaf { LEAF_TYPE } else { BRANCH_TYPE })
        || page.byte(5) != Some(0)
        || u16_le(page, 6) != HEADER_SIZE as u16
        || u64_le(page, 8) == 0
        || u64_le(page, 8) > selected_txn
        || expected_level.is_some_and(|expected| expected != level)
        || level > MAX_LEVEL
        || u32_le(page, 24) != kind as u32
    {
        return Err(Error::Corrupt("used bitmap page header is invalid"));
    }
    let lower = if leaf { LEAF_END } else { BRANCH_END };
    if usize::from(u16_le(page, 20)) != lower || usize::from(u16_le(page, 22)) != PAGE_SIZE {
        return Err(Error::Corrupt("used bitmap page layout is invalid"));
    }
    let item_count = usize::from(u16_le(page, 16));
    let maximum = if leaf { LEAF_WORDS } else { BRANCH_CHILDREN };
    if item_count == 0 || item_count > maximum {
        return Err(Error::Corrupt("used bitmap page count is invalid"));
    }
    Ok(Header { level, item_count })
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
    let at = HEADER_SIZE + 32 + index * 4;
    let old = u32_le(page.view(), at);
    page.put_u32(at, child)?;
    set_summary(page, index, candidate)?;
    let count = header
        .item_count
        .checked_add(usize::from(old == 0 && child != 0))
        .and_then(|count| count.checked_sub(usize::from(old != 0 && child == 0)))
        .ok_or(Error::Corrupt("used bitmap child count underflows"))?;
    page.put_u16(16, count as u16)
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
    let child = u32_le(page, HEADER_SIZE + 32 + index * 4);
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

pub(super) fn page_has_candidate<S: ByteSource>(
    page: S,
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

pub(super) fn stamp_leaf<D: PageSink>(page: &mut D, count: usize) -> Result<()> {
    page.put_u16(16, count as u16)
}

pub(super) fn initialize<D: PageSink>(
    page: &mut D,
    page_type: u8,
    txn: u64,
    level: u16,
    kind: Kind,
    lower: usize,
) {
    page.fill(0);
    page.write(0, &PAGE_MAGIC)
        .expect("fixed bitmap header fits");
    page.set_byte(4, page_type)
        .expect("fixed bitmap header fits");
    page.put_u16(6, HEADER_SIZE as u16)
        .expect("fixed bitmap header fits");
    page.put_u64(8, txn).expect("fixed bitmap header fits");
    page.put_u16(18, level).expect("fixed bitmap header fits");
    page.put_u16(20, lower as u16)
        .expect("fixed bitmap header fits");
    page.put_u16(22, PAGE_SIZE as u16)
        .expect("fixed bitmap header fits");
    page.put_u32(24, kind as u32)
        .expect("fixed bitmap header fits");
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
    let at = HEADER_SIZE + (index / 64) * 8;
    let mask = 1u64 << (index % 64);
    let word = u64_le(page.view(), at);
    page.put_u64(at, if value { word | mask } else { word & !mask })
}

pub(super) fn summary_bit<S: ByteSource>(page: S, index: usize) -> bool {
    u64_le(page, HEADER_SIZE + (index / 64) * 8) & (1u64 << (index % 64)) != 0
}

pub(super) fn first_summary<S: ByteSource>(page: S, start: usize) -> Option<usize> {
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
