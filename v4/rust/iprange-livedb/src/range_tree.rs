//! Allocation-free point lookup in the range B+tree.

use std::fs::File;

use crate::contract::{u16_le, u32_le, u64_le, MetaV4, MAX_TREE_LEVEL, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;
use crate::key::IpKey;

const HEADER_SIZE: usize = 32;
const RANGE_BRANCH: u8 = 1;
const RANGE_LEAF: u8 = 2;

struct Header {
    item_count: usize,
    level: u16,
    lower: usize,
    upper: usize,
}

pub(crate) fn lookup<K: IpKey>(file: &File, meta: &MetaV4, target: K) -> Result<Option<u32>> {
    if meta.address_family != K::FAMILY {
        return Err(Error::InvalidArgument(
            "lookup address family does not match the database",
        ));
    }
    if meta.range_root == 0 {
        return Ok(None);
    }

    let mut page_number = meta.range_root;
    let mut expected_level = None;
    let mut page = [0; PAGE_SIZE];

    for _ in 0..=MAX_TREE_LEVEL {
        file_io::read_page(file, page_number, meta.page_count, &mut page)?;
        let header = parse_header::<K>(&page, meta.txn_id, expected_level)?;
        if header.level == 0 {
            return lookup_leaf::<K>(&page, &header, target);
        }

        let cell_len = K::WIDTH + 4;
        let Some(index) = greatest_not_after::<K>(&page, &header, cell_len, target)? else {
            return Ok(None);
        };
        let cell = cell(&page, &header, index, cell_len)?;
        page_number = u32_le(cell, K::WIDTH);
        expected_level = Some(header.level - 1);
    }

    Err(Error::Corrupt("range tree exceeds its maximum height"))
}

fn lookup_leaf<K: IpKey>(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    target: K,
) -> Result<Option<u32>> {
    let cell_len = K::WIDTH * 2 + 4;
    let Some(index) = greatest_not_after::<K>(page, header, cell_len, target)? else {
        return Ok(None);
    };
    let cell = cell(page, header, index, cell_len)?;
    let from = K::read_le(cell);
    let to = K::read_le(&cell[K::WIDTH..]);
    if from > to {
        return Err(Error::Corrupt("selected range has reversed endpoints"));
    }
    if target > to {
        return Ok(None);
    }
    Ok(Some(u32_le(cell, K::WIDTH * 2)))
}

fn parse_header<K: IpKey>(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    expected_level: Option<u16>,
) -> Result<Header> {
    parse_page_identity(page, selected_txn)?;
    let item_count = usize::from(u16_le(page, 16));
    let level = u16_le(page, 18);
    parse_tree_identity::<K>(page, item_count, level, expected_level)?;
    let (lower, upper) = parse_slot_bounds(page, item_count)?;

    Ok(Header {
        item_count,
        level,
        lower,
        upper,
    })
}

fn parse_page_identity(page: &[u8; PAGE_SIZE], selected_txn: u64) -> Result<()> {
    if page[..4] != PAGE_MAGIC || page[5] != 0 || u16_le(page, 6) != HEADER_SIZE as u16 {
        return Err(Error::Corrupt("range page header is invalid"));
    }
    let born_txn = u64_le(page, 8);
    if born_txn == 0 || born_txn > selected_txn {
        return Err(Error::Corrupt("range page transaction is invalid"));
    }
    Ok(())
}

fn parse_tree_identity<K: IpKey>(
    page: &[u8; PAGE_SIZE],
    item_count: usize,
    level: u16,
    expected_level: Option<u16>,
) -> Result<()> {
    if item_count == 0 || level > MAX_TREE_LEVEL {
        return Err(Error::Corrupt("range page count or level is invalid"));
    }
    if expected_level.is_some_and(|expected| expected != level) {
        return Err(Error::Corrupt("range child level is invalid"));
    }
    let expected_type = if level == 0 { RANGE_LEAF } else { RANGE_BRANCH };
    if page[4] != expected_type || u32_le(page, 24) != K::FAMILY as u32 {
        return Err(Error::Corrupt("range page type or family is invalid"));
    }
    Ok(())
}

fn parse_slot_bounds(page: &[u8; PAGE_SIZE], item_count: usize) -> Result<(usize, usize)> {
    let lower = usize::from(u16_le(page, 20));
    let upper = usize::from(u16_le(page, 22));
    let expected_lower = item_count
        .checked_mul(2)
        .and_then(|size| size.checked_add(HEADER_SIZE))
        .ok_or(Error::Corrupt("range slot array overflows"))?;
    if lower != expected_lower || lower > upper || upper >= PAGE_SIZE {
        return Err(Error::Corrupt("range slotted-page bounds are invalid"));
    }
    Ok((lower, upper))
}

fn greatest_not_after<K: IpKey>(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    cell_len: usize,
    target: K,
) -> Result<Option<usize>> {
    let mut lower = 0;
    let mut upper = header.item_count;
    while lower < upper {
        let middle = lower + (upper - lower) / 2;
        let key = K::read_le(cell(page, header, middle, cell_len)?);
        if key <= target {
            lower = middle + 1;
        } else {
            upper = middle;
        }
    }
    Ok(lower.checked_sub(1))
}

fn cell<'a>(
    page: &'a [u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    cell_len: usize,
) -> Result<&'a [u8]> {
    if index >= header.item_count {
        return Err(Error::Corrupt("range slot index is invalid"));
    }
    let slot = HEADER_SIZE + index * 2;
    if slot + 2 > header.lower {
        return Err(Error::Corrupt("range slot is outside the slot array"));
    }
    let start = usize::from(u16_le(page, slot));
    let end = start
        .checked_add(cell_len)
        .ok_or(Error::Corrupt("range cell end overflows"))?;
    if start < header.upper || end > PAGE_SIZE {
        return Err(Error::Corrupt("range cell is outside the record area"));
    }
    Ok(&page[start..end])
}

#[cfg(test)]
#[path = "range_tree_tests.rs"]
mod tests;
