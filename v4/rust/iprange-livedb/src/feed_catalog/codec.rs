//! Canonical feed-catalog page and record codec.

use crate::contract::{u16_le, u32_le};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName, MAX_FEED_NAME};
use crate::fixed_tree::Codec;
use crate::format::page_type;
use crate::mapping::{ByteRange, ByteSource};
use crate::slotted_page::{self, Header};

pub(crate) const NAME_BRANCH: u8 = page_type::FEED_NAME_BRANCH;
pub(crate) const NAME_LEAF: u8 = page_type::FEED_NAME_LEAF;
pub(crate) const INDEX_BRANCH: u8 = page_type::FEED_INDEX_BRANCH;
pub(crate) const INDEX_LEAF: u8 = page_type::FEED_INDEX_LEAF;
pub(crate) const NAME_RECORD_BASE: usize = 12;
pub(crate) const MIN_NAME_RECORD: usize = NAME_RECORD_BASE + 1;
pub(crate) const MAX_NAME_RECORD: usize = NAME_RECORD_BASE + MAX_FEED_NAME;
pub(crate) const INDEX_BRANCH_SIZE: usize = 8;

const RECORD_LENGTH_OFFSET: usize = 0;
const RECORD_RESERVED_OFFSET: usize = 2;
const RECORD_INDEX_OFFSET: usize = 4;
const RECORD_NAME_LENGTH_OFFSET: usize = 8;
const RECORD_NAME_RESERVED_OFFSET: usize = 9;
const INDEX_BRANCH_KEY_OFFSET: usize = 0;
const INDEX_BRANCH_CHILD_OFFSET: usize = 4;

pub(super) struct NameCodec;

impl Codec for NameCodec {
    type Key = FeedName;
    type Leaf = FeedEntry;

    const BRANCH_TYPE: u8 = NAME_BRANCH;
    const LEAF_TYPE: u8 = NAME_LEAF;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 0;
    const LEAF_SIZE: usize = 0;
    const MAX_BRANCH_SIZE: usize = MAX_NAME_RECORD;
    const MAX_LEAF_SIZE: usize = MAX_NAME_RECORD;

    fn read_key<S: ByteSource>(cell: S, _level: u16) -> Result<Self::Key> {
        decode_entry(cell).map(|entry| entry.name)
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        decode_entry(cell)
    }

    fn write_key(_key: Self::Key, _output: &mut [u8]) {}

    fn leaf_cell<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<ByteRange<S>> {
        record(page, header, index)
    }

    fn branch_cell<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<ByteRange<S>> {
        record(page, header, index)
    }

    fn write_branch(key: Self::Key, child: u32, output: &mut [u8]) -> Result<usize> {
        encode(key, child, output)
    }

    fn read_branch_child<S: ByteSource>(cell: S) -> Result<u32> {
        decode_entry(cell).map(|entry| entry.index)
    }
}

pub(super) struct IndexCodec;

impl Codec for IndexCodec {
    type Key = u32;
    type Leaf = FeedEntry;

    const BRANCH_TYPE: u8 = INDEX_BRANCH;
    const LEAF_TYPE: u8 = INDEX_LEAF;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 4;
    const LEAF_SIZE: usize = 0;
    const MAX_LEAF_SIZE: usize = MAX_NAME_RECORD;

    fn read_key<S: ByteSource>(cell: S, level: u16) -> Result<Self::Key> {
        if level == 0 {
            Ok(u32_le(cell, RECORD_INDEX_OFFSET))
        } else {
            Ok(u32_le(cell, INDEX_BRANCH_KEY_OFFSET))
        }
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        decode_entry(cell)
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[..4].copy_from_slice(&key.to_le_bytes());
    }

    fn leaf_cell<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<ByteRange<S>> {
        record(page, header, index)
    }
}

pub(super) struct Encoded {
    bytes: [u8; MAX_NAME_RECORD],
    len: usize,
}

impl Encoded {
    pub(super) fn new(entry: FeedEntry) -> Result<Self> {
        let mut encoded = Self {
            bytes: [0; MAX_NAME_RECORD],
            len: 0,
        };
        encoded.len = encode(entry.name, entry.index, &mut encoded.bytes)?;
        Ok(encoded)
    }

    pub(super) fn as_slice(&self) -> &[u8] {
        &self.bytes[..self.len]
    }
}

pub(super) fn page_entry<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
) -> Result<FeedEntry> {
    decode_entry(record(page, header, index)?)
}

fn record<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<ByteRange<S>> {
    slotted_page::record(page, header, index, MIN_NAME_RECORD, MAX_NAME_RECORD)
}

pub(crate) fn decode_entry<S: ByteSource>(record: S) -> Result<FeedEntry> {
    let name_len = usize::from(
        record
            .byte(RECORD_NAME_LENGTH_OFFSET)
            .ok_or(Error::Corrupt("feed catalog record is malformed"))?,
    );
    if usize::from(u16_le(record, RECORD_LENGTH_OFFSET)) != NAME_RECORD_BASE + name_len
        || u16_le(record, RECORD_RESERVED_OFFSET) != 0
        || !record.all_zero(
            RECORD_NAME_RESERVED_OFFSET,
            NAME_RECORD_BASE - RECORD_NAME_RESERVED_OFFSET,
        )
    {
        return Err(Error::Corrupt("feed catalog record is malformed"));
    }
    let name = FeedName::from_source(record, NAME_RECORD_BASE, name_len)
        .ok_or(Error::Corrupt("feed catalog name is invalid"))?;
    Ok(FeedEntry {
        name,
        index: u32_le(record, RECORD_INDEX_OFFSET),
    })
}

pub(crate) fn decode_index_branch<S: ByteSource>(record: S) -> Result<(u32, u32)> {
    if record.len() != INDEX_BRANCH_SIZE {
        return Err(Error::Corrupt("feed index branch record is malformed"));
    }
    Ok((
        u32_le(record, INDEX_BRANCH_KEY_OFFSET),
        u32_le(record, INDEX_BRANCH_CHILD_OFFSET),
    ))
}

fn encode(name: FeedName, value: u32, output: &mut [u8]) -> Result<usize> {
    let len = NAME_RECORD_BASE + name.as_bytes().len();
    if output.len() < len {
        return Err(Error::InvalidArgument(
            "feed catalog record buffer is too small",
        ));
    }
    output[..len].fill(0);
    output[RECORD_LENGTH_OFFSET..RECORD_RESERVED_OFFSET]
        .copy_from_slice(&(len as u16).to_le_bytes());
    output[RECORD_INDEX_OFFSET..RECORD_NAME_LENGTH_OFFSET].copy_from_slice(&value.to_le_bytes());
    output[RECORD_NAME_LENGTH_OFFSET] = name.as_bytes().len() as u8;
    output[NAME_RECORD_BASE..len].copy_from_slice(name.as_bytes());
    Ok(len)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn big_endian_portable_feed_record_matches_literal_bytes() {
        let name = FeedName::new("feed-x").unwrap();
        let mut bytes = [0xa5; MAX_NAME_RECORD];
        let len = encode(name, 0x0102_0304, &mut bytes).unwrap();
        assert_eq!(
            &bytes[..len],
            &[18, 0, 0, 0, 4, 3, 2, 1, 6, 0, 0, 0, b'f', b'e', b'e', b'd', b'-', b'x',]
        );

        let record = decode_entry(&bytes[..len]).unwrap();
        assert_eq!(record.name, name);
        assert_eq!(record.index, 0x0102_0304);
    }
}
