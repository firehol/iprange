//! Read-only lowest-zero and exact-bit searches.

use crate::contract::{u64_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::Store;
use crate::slotted_page::HEADER_SIZE;

use super::page::{branch_child, first_summary, lowest_leaf, parse};
use super::{
    add_child_base, child_index, coverage, leaf_word_index, required_level, Kind, LEAF_BITS,
};

pub(super) fn find_lowest<S: Store>(
    store: &S,
    root: u32,
    limit: u64,
    kind: Kind,
) -> Result<Option<u32>> {
    if kind.first() >= limit {
        return Ok(None);
    }
    if root == 0 {
        return Ok(Some(kind.first() as u32));
    }
    let mut page_number = root;
    let mut level = required_level(limit)?;
    let mut base = 0u64;
    let mut start = kind.first();
    let mut selected_by_summary = false;
    let mut page = [0; PAGE_SIZE];
    loop {
        store.read(page_number, &mut page)?;
        let header = parse(&page, store.target_txn(), kind, Some(level), base, limit)?;
        if level == 0 {
            return finish_lowest_leaf(&page, base, start, limit, selected_by_summary);
        }
        let Some(next) = lowest_branch(
            store,
            &page,
            &header,
            level,
            base,
            start,
            limit,
            selected_by_summary,
        )?
        else {
            return Ok(None);
        };
        if next.child == 0 {
            return Ok(Some(next.start as u32));
        }
        page_number = next.child;
        level -= 1;
        base = next.base;
        start = next.start;
        selected_by_summary = true;
    }
}

struct LowestBranch {
    child: u32,
    base: u64,
    start: u64,
}

#[allow(clippy::too_many_arguments)]
fn lowest_branch<S: Store>(
    store: &S,
    page: &[u8; PAGE_SIZE],
    header: &super::page::Header,
    level: u16,
    base: u64,
    start: u64,
    limit: u64,
    selected: bool,
) -> Result<Option<LowestBranch>> {
    let span = coverage(level - 1)?;
    let first = ((start.saturating_sub(base)) / span) as usize;
    let Some(index) = first_summary(page, first) else {
        return missing_candidate(selected);
    };
    let child_base = add_child_base(base, span, index)?;
    if child_base >= limit {
        return Err(Error::Corrupt("used bitmap candidate is outside its limit"));
    }
    Ok(Some(LowestBranch {
        child: branch_child(page, header, index, store.page_limit())?,
        base: child_base,
        start: start.max(child_base),
    }))
}

fn missing_candidate(selected: bool) -> Result<Option<LowestBranch>> {
    if selected {
        Err(Error::Corrupt("used bitmap summary has no candidate"))
    } else {
        Ok(None)
    }
}

fn finish_lowest_leaf(
    page: &[u8; PAGE_SIZE],
    base: u64,
    start: u64,
    limit: u64,
    selected: bool,
) -> Result<Option<u32>> {
    let found = lowest_leaf(page, base, start, limit)?;
    if found.is_none() && selected {
        return Err(Error::Corrupt("used bitmap summary has no candidate"));
    }
    Ok(found.map(|bit| bit as u32))
}

pub(super) fn contains<S: Store>(
    store: &S,
    root: u32,
    limit: u64,
    kind: Kind,
    bit: u32,
) -> Result<bool> {
    if root == 0 {
        return Ok(false);
    }
    let mut page_number = root;
    let mut level = required_level(limit)?;
    let mut base = 0u64;
    let mut page = [0; PAGE_SIZE];
    loop {
        store.read(page_number, &mut page)?;
        let header = parse(&page, store.target_txn(), kind, Some(level), base, limit)?;
        if level == 0 {
            return Ok(leaf_contains(&page, bit));
        }
        let Some(next) = exact_child(store, &page, &header, bit, level, base)? else {
            return Ok(false);
        };
        page_number = next.0;
        base = next.1;
        level -= 1;
    }
}

pub(crate) fn read_words<S: Store>(
    store: &S,
    root: u32,
    limit: u64,
    kind: Kind,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    let end = require_word_range(limit, start, output.len())?;
    output.fill(0);
    required_level(limit)?;
    if root == 0 || output.is_empty() {
        return Ok(());
    }
    copy_words(store, root, limit, kind, start, end, output)?;
    mask_last_word(start, limit, output);
    Ok(())
}

fn copy_words<S: Store>(
    store: &S,
    root: u32,
    limit: u64,
    kind: Kind,
    start: u32,
    end: u64,
    output: &mut [u64],
) -> Result<()> {
    let mut at = start;
    while u64::from(at) < end {
        let base = u64::from(at) * 64;
        let leaf_base = base / LEAF_BITS * LEAF_BITS;
        let within = ((base - leaf_base) / 64) as usize;
        let count = output
            .len()
            .saturating_sub((at - start) as usize)
            .min(LEAF_BITS as usize / 64 - within);
        if let Some(page) = find_leaf(store, root, limit, kind, base, leaf_base)? {
            copy_leaf_words(&page, within, &mut output[(at - start) as usize..][..count]);
        }
        at = at
            .checked_add(count as u32)
            .ok_or(Error::ArithmeticOverflow("used bitmap word range"))?;
    }
    Ok(())
}

fn require_word_range(limit: u64, start: u32, count: usize) -> Result<u64> {
    let end = u64::from(start)
        .checked_add(count as u64)
        .ok_or(Error::ArithmeticOverflow("used bitmap word range"))?;
    let word_limit = limit
        .checked_add(63)
        .ok_or(Error::ArithmeticOverflow("used bitmap word limit"))?
        / 64;
    if end > word_limit {
        Err(Error::InvalidArgument(
            "used bitmap word range exceeds its limit",
        ))
    } else {
        Ok(end)
    }
}

fn find_leaf<S: Store>(
    store: &S,
    root: u32,
    limit: u64,
    kind: Kind,
    base: u64,
    leaf_base: u64,
) -> Result<Option<[u8; PAGE_SIZE]>> {
    let bit = u32::try_from(base).map_err(|_| Error::Corrupt("used bitmap word is invalid"))?;
    let mut page_number = root;
    let mut level = required_level(limit)?;
    let mut page_base = 0u64;
    let mut page = [0; PAGE_SIZE];
    loop {
        store.read(page_number, &mut page)?;
        let header = parse(
            &page,
            store.target_txn(),
            kind,
            Some(level),
            page_base,
            limit,
        )?;
        if level == 0 {
            if page_base != leaf_base {
                return Err(Error::Corrupt("used bitmap leaf coverage is invalid"));
            }
            return Ok(Some(page));
        }
        let Some(next) = exact_child(store, &page, &header, bit, level, page_base)? else {
            return Ok(None);
        };
        page_number = next.0;
        page_base = next.1;
        level -= 1;
    }
}

fn copy_leaf_words(page: &[u8; PAGE_SIZE], start: usize, output: &mut [u64]) {
    for (offset, word) in output.iter_mut().enumerate() {
        *word = u64_le(page, HEADER_SIZE + (start + offset) * 8);
    }
}

fn mask_last_word(start: u32, limit: u64, output: &mut [u64]) {
    let tail = limit % 64;
    if tail == 0 || output.is_empty() {
        return;
    }
    let last = (limit / 64) as u32;
    if last >= start && u64::from(last - start) < output.len() as u64 {
        output[(last - start) as usize] &= (1u64 << tail) - 1;
    }
}

fn leaf_contains(page: &[u8; PAGE_SIZE], bit: u32) -> bool {
    let word = u64_le(page, HEADER_SIZE + leaf_word_index(bit) * 8);
    word & (1u64 << (u64::from(bit) % 64)) != 0
}

fn exact_child<S: Store>(
    store: &S,
    page: &[u8; PAGE_SIZE],
    header: &super::page::Header,
    bit: u32,
    level: u16,
    base: u64,
) -> Result<Option<(u32, u64)>> {
    let span = coverage(level - 1)?;
    let index = child_index(bit, level)?;
    let base = add_child_base(base, span, index)?;
    let child = branch_child(page, header, index, store.page_limit())?;
    Ok((child != 0).then_some((child, base)))
}

pub(super) fn greatest<S: Store>(
    store: &S,
    root: u32,
    limit: u64,
    kind: Kind,
) -> Result<Option<u32>> {
    if root == 0 {
        return Ok(None);
    }
    let mut page_number = root;
    let mut level = required_level(limit)?;
    let mut base = 0u64;
    let mut page = [0; PAGE_SIZE];
    loop {
        store.read(page_number, &mut page)?;
        let header = parse(&page, store.target_txn(), kind, Some(level), base, limit)?;
        if level == 0 {
            return greatest_leaf(&page, base, limit);
        }
        let span = coverage(level - 1)?;
        let (index, child) = highest_child(store, &page, &header)?;
        base = add_child_base(base, span, index)?;
        page_number = child;
        level -= 1;
    }
}

fn highest_child<S: Store>(
    store: &S,
    page: &[u8; PAGE_SIZE],
    header: &super::page::Header,
) -> Result<(usize, u32)> {
    for index in (0..super::page::BRANCH_CHILDREN).rev() {
        let child = branch_child(page, header, index, store.page_limit())?;
        if child != 0 {
            return Ok((index, child));
        }
    }
    Err(Error::Corrupt("used bitmap branch has no child"))
}

fn greatest_leaf(page: &[u8; PAGE_SIZE], base: u64, limit: u64) -> Result<Option<u32>> {
    for index in (0..super::page::LEAF_WORDS).rev() {
        let word_base = base
            .checked_add(index as u64 * 64)
            .ok_or(Error::ArithmeticOverflow("used bitmap position"))?;
        if word_base >= limit {
            continue;
        }
        let mut word = u64_le(page, HEADER_SIZE + index * 8);
        if limit - word_base < 64 {
            word &= (1u64 << (limit - word_base)) - 1;
        }
        if word != 0 {
            let bit = word_base + u64::from(63 - word.leading_zeros());
            return Ok(Some(bit as u32));
        }
    }
    Err(Error::Corrupt("used bitmap leaf has no set bit"))
}
