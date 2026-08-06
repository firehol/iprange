//! Sparse feed- and membership-used bitmaps.

use crate::contract::u64_le;
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiredPages, Store};
use crate::slotted_page::PageSink;

mod mutation;
mod page;
mod search;
mod shrink;

use page::{page_has_candidate, parse, Header, BRANCH_CHILDREN, LEAF_BITS, MAX_LEVEL};

pub(crate) use mutation::{clear, set, take_lowest};
pub(crate) use search::read_words;
pub(crate) use shrink::membership as shrink_membership;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Kind {
    Feed = 2,
    Membership = 3,
}

impl Kind {
    const fn first(self) -> u64 {
        match self {
            Self::Feed => 0,
            Self::Membership => 1,
        }
    }
}

fn touch<S: Store>(
    store: &mut S,
    page_number: u32,
    kind: Kind,
    level: u16,
    base: u64,
    limit: u64,
    retired: &mut RetiredPages,
) -> Result<(u32, Header)> {
    let target_txn = store.target_txn();
    let (header, private) = store.inspect_page(page_number, |page| {
        let header = parse(page, target_txn, kind, Some(level), base, limit)?;
        Ok((header, u64_le(page, 8) == target_txn))
    })?;
    if private {
        return Ok((page_number, header));
    }
    let private = store.allocate()?;
    store.copy_page(page_number, private, |source, output| {
        output.write_source(0, source)?;
        output.put_u64(8, target_txn)?;
        output.put_u32(28, 0)
    })?;
    retired.push(page_number)?;
    Ok((private, header))
}

fn subtree_has_candidate<S: Store>(
    store: &S,
    page_number: u32,
    kind: Kind,
    base: u64,
    limit: u64,
) -> Result<bool> {
    let target_txn = store.target_txn();
    store.inspect_page(page_number, |page| {
        parse(page, target_txn, kind, None, base, limit)?;
        page_has_candidate(page, base, limit, kind)
    })
}

fn leaf_word_index(bit: u32) -> usize {
    ((u64::from(bit) % LEAF_BITS) / 64) as usize
}

fn child_index(bit: u32, level: u16) -> Result<usize> {
    Ok(((u64::from(bit) / coverage(level - 1)?) % BRANCH_CHILDREN as u64) as usize)
}

fn add_child_base(base: u64, span: u64, index: usize) -> Result<u64> {
    base.checked_add(
        span.checked_mul(index as u64)
            .ok_or(Error::ArithmeticOverflow("used bitmap coverage"))?,
    )
    .ok_or(Error::ArithmeticOverflow("used bitmap coverage"))
}

fn coverage(level: u16) -> Result<u64> {
    let mut value = LEAF_BITS;
    for _ in 0..level {
        value = value
            .checked_mul(BRANCH_CHILDREN as u64)
            .ok_or(Error::Corrupt("used bitmap coverage overflow"))?;
    }
    Ok(value)
}

fn required_level(limit: u64) -> Result<u16> {
    if limit == 0 || limit > 1u64 << 32 {
        return Err(Error::InvalidArgument("used bitmap limit is invalid"));
    }
    for level in 0..=MAX_LEVEL {
        if coverage(level)? >= limit {
            return Ok(level);
        }
    }
    Err(Error::Corrupt("used bitmap limit exceeds maximum coverage"))
}

fn require_bit(limit: u64, kind: Kind, bit: u32) -> Result<()> {
    required_level(limit)?;
    if u64::from(bit) < kind.first() || u64::from(bit) >= limit {
        return Err(Error::InvalidArgument(
            "used bitmap bit is outside its namespace",
        ));
    }
    Ok(())
}

#[cfg(test)]
#[path = "used_bitmap_tests.rs"]
mod tests;
