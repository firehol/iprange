//! Read-only lowest-zero and exact-bit searches.

use crate::contract::u64_le;
use crate::error::{Error, Result};
use crate::fixed_tree::Store;
use crate::mapping::ByteSource;
use crate::slotted_page::HEADER_SIZE;

use super::page::{branch_child, first_summary, lowest_leaf, parse};
use super::{
    add_child_base, child_index, coverage, leaf_word_index, required_level, Kind, LEAF_BITS,
};

enum LowestStep {
    Found(Option<u32>),
    Missing(u32),
    Child { page: u32, base: u64, start: u64 },
}

enum ExactStep {
    Found(bool),
    Missing,
    Child(u32, u64),
}

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
    let mut selected = false;
    loop {
        let target_txn = store.target_txn();
        let page_limit = store.page_limit();
        let step = store.inspect_page(page_number, |page| {
            let header = parse(page, target_txn, kind, Some(level), base, limit)?;
            if level == 0 {
                let found = lowest_leaf(page, base, start, limit)?.map(|bit| bit as u32);
                if found.is_none() && selected {
                    return Err(Error::Corrupt("used bitmap summary has no candidate"));
                }
                return Ok(LowestStep::Found(found));
            }
            let span = coverage(level - 1)?;
            let first = ((start.saturating_sub(base)) / span) as usize;
            let Some(index) = first_summary(page, first) else {
                if selected {
                    return Err(Error::Corrupt("used bitmap summary has no candidate"));
                }
                return Ok(LowestStep::Found(None));
            };
            let child_base = add_child_base(base, span, index)?;
            if child_base >= limit {
                return Err(Error::Corrupt("used bitmap candidate is outside its limit"));
            }
            let child = branch_child(page, &header, index, page_limit)?;
            if child == 0 {
                Ok(LowestStep::Missing(start.max(child_base) as u32))
            } else {
                Ok(LowestStep::Child {
                    page: child,
                    base: child_base,
                    start: start.max(child_base),
                })
            }
        })?;
        match step {
            LowestStep::Found(found) => return Ok(found),
            LowestStep::Missing(bit) => return Ok(Some(bit)),
            LowestStep::Child {
                page,
                base: next_base,
                start: next_start,
            } => {
                page_number = page;
                level -= 1;
                base = next_base;
                start = next_start;
                selected = true;
            }
        }
    }
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
    loop {
        let target_txn = store.target_txn();
        let page_limit = store.page_limit();
        let next = store.inspect_page(page_number, |page| {
            let header = parse(page, target_txn, kind, Some(level), base, limit)?;
            if level == 0 {
                let word = u64_le(page, HEADER_SIZE + leaf_word_index(bit) * 8);
                return Ok(ExactStep::Found(
                    word & (1u64 << (u64::from(bit) % 64)) != 0,
                ));
            }
            let span = coverage(level - 1)?;
            let index = child_index(bit, level)?;
            let next_base = add_child_base(base, span, index)?;
            let child = branch_child(page, &header, index, page_limit)?;
            if child == 0 {
                Ok(ExactStep::Missing)
            } else {
                Ok(ExactStep::Child(child, next_base))
            }
        })?;
        match next {
            ExactStep::Found(found) => return Ok(found),
            ExactStep::Missing => return Ok(false),
            ExactStep::Child(child, next_base) => {
                page_number = child;
                base = next_base;
                level -= 1;
            }
        }
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
    let mut at = start;
    while u64::from(at) < end {
        let base = u64::from(at) * 64;
        let leaf_base = base / LEAF_BITS * LEAF_BITS;
        let within = ((base - leaf_base) / 64) as usize;
        let offset = (at - start) as usize;
        let count = output
            .len()
            .saturating_sub(offset)
            .min(LEAF_BITS as usize / 64 - within);
        if let Some(page_number) = find_leaf(store, root, limit, kind, base, leaf_base)? {
            store.inspect_page(page_number, |page| {
                for (index, word) in output[offset..][..count].iter_mut().enumerate() {
                    *word = u64_le(page, HEADER_SIZE + (within + index) * 8);
                }
                Ok(())
            })?;
        }
        at = at
            .checked_add(count as u32)
            .ok_or(Error::ArithmeticOverflow("used bitmap word range"))?;
    }
    mask_last_word(start, limit, output);
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
) -> Result<Option<u32>> {
    let bit = u32::try_from(base).map_err(|_| Error::Corrupt("used bitmap word is invalid"))?;
    let mut page_number = root;
    let mut level = required_level(limit)?;
    let mut page_base = 0u64;
    loop {
        let target_txn = store.target_txn();
        let page_limit = store.page_limit();
        let child = store.inspect_page(page_number, |page| {
            let header = parse(page, target_txn, kind, Some(level), page_base, limit)?;
            if level == 0 {
                if page_base != leaf_base {
                    return Err(Error::Corrupt("used bitmap leaf coverage is invalid"));
                }
                return Ok(None);
            }
            let span = coverage(level - 1)?;
            let index = child_index(bit, level)?;
            let next_base = add_child_base(page_base, span, index)?;
            let child = branch_child(page, &header, index, page_limit)?;
            Ok(Some((child, next_base)))
        })?;
        let Some((child, next_base)) = child else {
            return Ok(Some(page_number));
        };
        if child == 0 {
            return Ok(None);
        }
        page_number = child;
        page_base = next_base;
        level -= 1;
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
    loop {
        let target_txn = store.target_txn();
        let page_limit = store.page_limit();
        let next = store.inspect_page(page_number, |page| {
            let header = parse(page, target_txn, kind, Some(level), base, limit)?;
            if level == 0 {
                return Ok(None);
            }
            for index in (0..super::page::BRANCH_CHILDREN).rev() {
                let child = branch_child(page, &header, index, page_limit)?;
                if child != 0 {
                    return Ok(Some((index, child)));
                }
            }
            Err(Error::Corrupt("used bitmap branch has no child"))
        })?;
        let Some((index, child)) = next else {
            return store.inspect_page(page_number, |page| greatest_leaf(page, base, limit));
        };
        base = add_child_base(base, coverage(level - 1)?, index)?;
        page_number = child;
        level -= 1;
    }
}

fn greatest_leaf<S: ByteSource>(page: S, base: u64, limit: u64) -> Result<Option<u32>> {
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
            return Ok(Some(
                (word_base + u64::from(63 - word.leading_zeros())) as u32,
            ));
        }
    }
    Err(Error::Corrupt("used bitmap leaf has no set bit"))
}
