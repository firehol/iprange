//! Allocation-free point lookup in the range B+tree.

use crate::contract::{u16_le, u32_le, MetaV4, MAX_TREE_LEVEL};
use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::mapping::{ByteSource, Mapping};
use crate::slotted_page;

pub(crate) use crate::slotted_page::Header;
pub(crate) const RANGE_BRANCH: u8 = 1;
pub(crate) const RANGE_LEAF: u8 = 2;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Record<K> {
    pub(crate) from: K,
    pub(crate) to: K,
    pub(crate) value: u32,
}

pub(crate) fn lookup<K: IpKey>(mapping: &Mapping, meta: &MetaV4, target: K) -> Result<Option<u32>> {
    if meta.address_family != K::FAMILY {
        return Err(Error::WrongAddressFamily(
            "lookup address family does not match the database",
        ));
    }
    if meta.range_root == 0 {
        return Ok(None);
    }

    let mut page_number = meta.range_root;
    let mut expected_level = None;
    for _ in 0..=MAX_TREE_LEVEL {
        let page = mapping.page(page_number, meta.page_count)?;
        let header = parse_header::<K, _>(page, meta.txn_id, expected_level)?;
        if header.level == 0 {
            return lookup_leaf::<K, _>(page, &header, target);
        }

        let cell_len = K::WIDTH + 4;
        let Some(index) = greatest_not_after::<K, _>(page, &header, cell_len, target)? else {
            return Ok(None);
        };
        page_number = branch_child::<K, _>(page, &header, index)?;
        expected_level = Some(header.level - 1);
    }

    Err(Error::Corrupt("range tree exceeds its maximum height"))
}

fn lookup_leaf<K: IpKey, S: ByteSource>(
    page: S,
    header: &Header,
    target: K,
) -> Result<Option<u32>> {
    let cell_len = K::WIDTH * 2 + 4;
    let Some(index) = greatest_not_after::<K, _>(page, header, cell_len, target)? else {
        return Ok(None);
    };
    let record = leaf_record::<K, _>(page, header, index)?;
    if target > record.to {
        return Ok(None);
    }
    Ok(Some(record.value))
}

pub(crate) fn parse_header<K: IpKey, S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected_level: Option<u16>,
) -> Result<Header> {
    let level = u16_le(page, 18);
    let expected_type = if level == 0 { RANGE_LEAF } else { RANGE_BRANCH };
    slotted_page::parse(
        page,
        selected_txn,
        expected_type,
        K::FAMILY as u32,
        expected_level,
    )
}

pub(crate) fn greatest_not_after<K: IpKey, S: ByteSource>(
    page: S,
    header: &Header,
    cell_len: usize,
    target: K,
) -> Result<Option<usize>> {
    let mut lower = 0;
    let mut upper = header.item_count;
    while lower < upper {
        let middle = lower + (upper - lower) / 2;
        let key = K::read_le(slotted_page::cell(page, header, middle, cell_len)?, 0);
        if key <= target {
            lower = middle + 1;
        } else {
            upper = middle;
        }
    }
    Ok(lower.checked_sub(1))
}

pub(crate) fn branch_child<K: IpKey, S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
) -> Result<u32> {
    let cell = slotted_page::cell(page, header, index, K::WIDTH + 4)?;
    Ok(u32_le(cell, K::WIDTH))
}

pub(crate) fn leaf_record<K: IpKey, S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
) -> Result<Record<K>> {
    let cell = slotted_page::cell(page, header, index, K::WIDTH * 2 + 4)?;
    let from = K::read_le(cell, 0);
    let to = K::read_le(cell, K::WIDTH);
    if from > to {
        return Err(Error::Corrupt("selected range has reversed endpoints"));
    }
    Ok(Record {
        from,
        to,
        value: u32_le(cell, K::WIDTH * 2),
    })
}

#[cfg(test)]
#[path = "range_tree_tests.rs"]
mod tests;
