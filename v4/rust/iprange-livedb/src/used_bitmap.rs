//! Sparse feed- and membership-used bitmaps.

use crate::bitmap_page::{child_index, coverage, leaf_word_index, required_level, LEAF_BITS};
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiredPages, RetiringStore, Store};

mod mutation;
mod page;
mod search;
mod shrink;

use page::{page_has_candidate, parse, Header};

pub(crate) use crate::bitmap_page::Kind;

pub(crate) use mutation::{clear, set, take_lowest};
pub(crate) use search::read_words;
pub(crate) use shrink::{membership as shrink_membership, structure as shrink_structure};

pub(crate) fn allocate_lowest_id<S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    limit: &mut u64,
    live_count: u64,
    kind: Kind,
    exhausted: Error,
) -> Result<u32> {
    let namespace = limit
        .checked_sub(1)
        .ok_or(Error::Corrupt("ID namespace limit is zero"))?;
    if live_count > namespace {
        return Err(Error::Corrupt("ID namespace count exceeds its limit"));
    }
    let mut retired = RetiredPages::new();
    if live_count < namespace {
        let id = take_lowest(store, root, *limit, kind, &mut retired)?
            .ok_or(Error::Corrupt("ID used bitmap has no advertised hole"))?;
        store.retire_pages(retired.as_slice())?;
        return Ok(id);
    }
    if *limit == 1u64 << 32 {
        return Err(exhausted);
    }
    let id = *limit as u32;
    *limit += 1;
    mutation::set_dense_append(store, root, *limit, kind, id, &mut retired)?;
    store.retire_pages(retired.as_slice())?;
    Ok(id)
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
        Ok((header, crate::page_header::born_txn(page) == target_txn))
    })?;
    if private {
        return Ok((page_number, header));
    }
    let private = store.allocate()?;
    store.copy_for_cow(page_number, private)?;
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

fn add_child_base(base: u64, span: u64, index: usize) -> Result<u64> {
    base.checked_add(
        span.checked_mul(index as u64)
            .ok_or(Error::ArithmeticOverflow("used bitmap coverage"))?,
    )
    .ok_or(Error::ArithmeticOverflow("used bitmap coverage"))
}

fn require_bit(limit: u64, kind: Kind, bit: u32) -> Result<()> {
    required_level(limit)?;
    if u64::from(bit) < kind.first_candidate() || u64::from(bit) >= limit {
        return Err(Error::InvalidArgument(
            "used bitmap bit is outside its namespace",
        ));
    }
    Ok(())
}

#[cfg(test)]
#[path = "used_bitmap_tests.rs"]
mod tests;
