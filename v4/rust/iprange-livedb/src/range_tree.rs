//! Allocation-free point lookup in the range B+tree.

use std::marker::PhantomData;

use crate::contract::{u32_le, MetaV4, MAX_TREE_LEVEL};
use crate::error::{Error, Result};
use crate::fixed_tree::{Codec, PageSource};
use crate::format::{page_type, Generation};
use crate::key::IpKey;
use crate::mapping::{ByteSource, Mapping};
use crate::slotted_page;

pub(crate) use crate::slotted_page::Header;
pub(crate) const RANGE_BRANCH: u8 = page_type::RANGE_BRANCH;
pub(crate) const RANGE_LEAF: u8 = page_type::RANGE_LEAF;
pub(crate) const MAX_RECORD_SIZE: usize = 36;
pub(crate) const MAX_BRANCH_SIZE: usize = 20;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Record<K> {
    pub(crate) from: K,
    pub(crate) to: K,
    pub(crate) value: u32,
}

pub(crate) struct RangeCodec<K>(PhantomData<K>);

impl<K: IpKey> Codec for RangeCodec<K> {
    type Key = K;

    const BRANCH_TYPE: u8 = RANGE_BRANCH;
    const LEAF_TYPE: u8 = RANGE_LEAF;
    const AUX: u32 = K::FAMILY as u32;
    const KEY_SIZE: usize = K::WIDTH;
    const LEAF_SIZE: usize = K::WIDTH * 2 + 4;

    fn read_key<S: ByteSource>(cell: S, level: u16) -> Result<Self::Key> {
        if level == 0 {
            decode_cell(cell).map(|record| record.from)
        } else {
            Ok(K::read_le(cell, 0))
        }
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        key.write_le(output);
    }

    fn validate_leaf<S: ByteSource>(cell: S) -> Result<()> {
        decode_cell::<K, _>(cell).map(|_| ())
    }
}

pub(crate) fn encode_record<K: IpKey>(record: Record<K>, output: &mut [u8]) -> Result<usize> {
    let len = record_size::<K>();
    if record.from > record.to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    if output.len() < len {
        return Err(Error::Unsupported("range record buffer is too small"));
    }
    record.from.write_le(output);
    record.to.write_le(&mut output[K::WIDTH..]);
    output[K::WIDTH * 2..len].copy_from_slice(&record.value.to_le_bytes());
    Ok(len)
}

pub(crate) fn encode_branch<K: IpKey>(key: K, child: u32, output: &mut [u8]) -> Result<usize> {
    <RangeCodec<K> as Codec>::write_branch(key, child, output)
}

pub(crate) const fn record_size<K: IpKey>() -> usize {
    K::WIDTH * 2 + 4
}

pub(crate) const fn branch_size<K: IpKey>() -> usize {
    K::WIDTH + 4
}

pub(crate) fn decode_cell<K: IpKey, S: ByteSource>(cell: S) -> Result<Record<K>> {
    let record = decode_fields(cell)?;
    if record.from > record.to {
        return Err(Error::Corrupt("selected range has reversed endpoints"));
    }
    Ok(record)
}

pub(crate) fn decode_fields<K: IpKey, S: ByteSource>(cell: S) -> Result<Record<K>> {
    if cell.len() != record_size::<K>() {
        return Err(Error::Corrupt("range leaf has the wrong record size"));
    }
    let from = K::read_le(cell, 0);
    let to = K::read_le(cell, K::WIDTH);
    Ok(Record {
        from,
        to,
        value: u32_le(cell, K::WIDTH * 2),
    })
}

pub(crate) fn decode_branch<K: IpKey, S: ByteSource>(cell: S) -> Result<(K, u32)> {
    if cell.len() != branch_size::<K>() {
        return Err(Error::Corrupt("range branch has the wrong record size"));
    }
    Ok((K::read_le(cell, 0), u32_le(cell, K::WIDTH)))
}

pub(crate) fn lookup<K: IpKey>(mapping: &Mapping, meta: &MetaV4, target: K) -> Result<Option<u32>> {
    if meta.address_family != K::FAMILY {
        return Err(Error::WrongAddressFamily(
            "lookup address family does not match the database",
        ));
    }
    lookup_in(&Generation::new(mapping, *meta), meta.range_root, target)
}

pub(crate) fn lookup_in<K: IpKey, S: PageSource>(
    source: &S,
    root: u32,
    target: K,
) -> Result<Option<u32>> {
    crate::work::tree_lookup(1);
    if root == 0 {
        return Ok(None);
    }

    let mut page_number = root;
    let mut expected_level = None;
    for _ in 0..=MAX_TREE_LEVEL {
        let selected_txn = source.selected_txn();
        let page_limit = source.selected_page_limit();
        let step = source.view_page(page_number, |page| {
            let header = parse_header::<K, _>(page, selected_txn, expected_level)?;
            if header.level == 0 {
                return Ok(LookupStep::Found(lookup_leaf::<K, _>(
                    page, &header, target,
                )?));
            }
            let Some(index) = greatest_not_after::<K, _>(page, &header, target)? else {
                return Ok(LookupStep::Found(None));
            };
            let child = branch_child::<K, _>(page, &header, index)?;
            if child < 2 || u64::from(child) >= page_limit {
                return Err(Error::Corrupt(
                    "range child is outside the selected generation",
                ));
            }
            Ok(LookupStep::Descend(child, header.level - 1))
        })?;
        match step {
            LookupStep::Found(result) => return Ok(result),
            LookupStep::Descend(child, level) => {
                page_number = child;
                expected_level = Some(level);
            }
        }
        crate::work::tree_descent(1);
    }

    Err(Error::Corrupt("range tree exceeds its maximum height"))
}

enum LookupStep {
    Found(Option<u32>),
    Descend(u32, u16),
}

fn lookup_leaf<K: IpKey, S: ByteSource>(
    page: S,
    header: &Header,
    target: K,
) -> Result<Option<u32>> {
    let Some(index) = greatest_not_after::<K, _>(page, header, target)? else {
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
    slotted_page::parse_tree(
        page,
        selected_txn,
        RANGE_BRANCH,
        RANGE_LEAF,
        K::FAMILY as u32,
        expected_level,
    )
}

pub(crate) fn greatest_not_after<K: IpKey, S: ByteSource>(
    page: S,
    header: &Header,
    target: K,
) -> Result<Option<usize>> {
    let cell_len = if header.level == 0 {
        record_size::<K>()
    } else {
        branch_size::<K>()
    };
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
    let cell = slotted_page::cell(page, header, index, branch_size::<K>())?;
    decode_branch::<K, _>(cell).map(|(_, child)| child)
}

pub(crate) fn leaf_record<K: IpKey, S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
) -> Result<Record<K>> {
    let cell = slotted_page::cell(page, header, index, record_size::<K>())?;
    decode_cell(cell)
}

#[cfg(test)]
#[path = "range_tree_tests.rs"]
mod tests;
