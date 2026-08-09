//! Allocation-free point lookup in the range B+tree.

use std::marker::PhantomData;

use crate::contract::{u32_le, MetaV4};
use crate::error::{Error, Result};
use crate::fixed_tree::{self, Codec, LeafQuery};
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
    type Leaf = Record<K>;

    const BRANCH_TYPE: u8 = RANGE_BRANCH;
    const LEAF_TYPE: u8 = RANGE_LEAF;
    const AUX: u32 = K::FAMILY as u32;
    const KEY_SIZE: usize = K::WIDTH;
    const LEAF_SIZE: usize = K::WIDTH * 2 + 4;

    fn read_key<S: ByteSource>(cell: S, _level: u16) -> Result<Self::Key> {
        Ok(K::read_le(cell, 0))
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        decode_cell(cell)
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        key.write_le(output);
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
    fixed_tree::query::<RangeCodec<K>, _, _>(
        &Generation::new(mapping, *meta),
        meta.range_root,
        target,
        &mut RangeLookup { target },
    )
}

struct RangeLookup<K> {
    target: K,
}

impl<K: IpKey> LeafQuery<RangeCodec<K>> for RangeLookup<K> {
    type Output = u32;

    fn inspect<S: ByteSource>(
        &mut self,
        page: S,
        header: &Header,
        _page_number: u32,
        position: usize,
        exact: bool,
    ) -> Result<Option<Self::Output>> {
        let index = if exact {
            position
        } else if let Some(previous) = position.checked_sub(1) {
            previous
        } else {
            return Ok(None);
        };
        let record = leaf_record::<K, _>(page, header, index)?;
        if self.target > record.to {
            return Ok(None);
        }
        Ok(Some(record.value))
    }
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

#[cfg(test)]
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
