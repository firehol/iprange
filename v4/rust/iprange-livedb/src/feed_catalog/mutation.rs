//! Atomic maintenance of the two feed-catalog indexes.

use crate::contract::u32_le;
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::fixed_tree::{self, Codec, RetiredPages, RetiringStore};
use crate::mapping::{ByteRange, ByteSource};
use crate::slotted_page::{self, Header};

use super::{
    decode_record, INDEX_BRANCH, INDEX_LEAF, MAX_NAME_RECORD, NAME_BRANCH, NAME_LEAF,
    NAME_RECORD_BASE,
};

struct NameCodec;

impl Codec for NameCodec {
    type Key = FeedName;

    const BRANCH_TYPE: u8 = NAME_BRANCH;
    const LEAF_TYPE: u8 = NAME_LEAF;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 0;
    const LEAF_SIZE: usize = 0;
    const MAX_BRANCH_SIZE: usize = MAX_NAME_RECORD;
    const MAX_LEAF_SIZE: usize = MAX_NAME_RECORD;

    fn read_key<S: ByteSource>(cell: S, _level: u16) -> Result<Self::Key> {
        decode_record(cell).map(|record| record.name)
    }

    fn write_key(_key: Self::Key, _output: &mut [u8]) {}

    fn validate_leaf<S: ByteSource>(cell: S) -> Result<()> {
        decode_record(cell).map(|_| ())
    }

    fn leaf_cell<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<ByteRange<S>> {
        slotted_page::record(page, header, index, NAME_RECORD_BASE + 1, MAX_NAME_RECORD)
    }

    fn branch_cell<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<ByteRange<S>> {
        slotted_page::record(page, header, index, NAME_RECORD_BASE + 1, MAX_NAME_RECORD)
    }

    fn write_branch(key: Self::Key, child: u32, output: &mut [u8]) -> Result<usize> {
        encode(key, child, output)
    }

    fn read_branch_child<S: ByteSource>(cell: S) -> Result<u32> {
        decode_record(cell).map(|record| record.value)
    }
}

struct IndexCodec;

impl Codec for IndexCodec {
    type Key = u32;

    const BRANCH_TYPE: u8 = INDEX_BRANCH;
    const LEAF_TYPE: u8 = INDEX_LEAF;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 4;
    const LEAF_SIZE: usize = 0;
    const MAX_LEAF_SIZE: usize = MAX_NAME_RECORD;

    fn read_key<S: ByteSource>(cell: S, level: u16) -> Result<Self::Key> {
        if level == 0 {
            decode_record(cell).map(|record| record.value)
        } else {
            Ok(u32_le(cell, 0))
        }
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[..4].copy_from_slice(&key.to_le_bytes());
    }

    fn validate_leaf<S: ByteSource>(cell: S) -> Result<()> {
        decode_record(cell).map(|_| ())
    }

    fn leaf_cell<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<ByteRange<S>> {
        slotted_page::record(page, header, index, NAME_RECORD_BASE + 1, MAX_NAME_RECORD)
    }
}

pub(crate) fn insert<S: RetiringStore>(
    store: &mut S,
    name_root: &mut u32,
    index_root: &mut u32,
    entry: FeedEntry,
) -> Result<()> {
    let record = Encoded::new(entry)?;
    if !mutate_insert::<NameCodec, S>(store, name_root, record.as_slice())? {
        return Err(Error::Corrupt("feed name already exists"));
    }
    if !mutate_insert::<IndexCodec, S>(store, index_root, record.as_slice())? {
        return Err(Error::Corrupt("feed index already exists"));
    }
    crate::work::catalog_intern(1);
    Ok(())
}

pub(crate) fn delete<S: RetiringStore>(
    store: &mut S,
    name_root: &mut u32,
    index_root: &mut u32,
    entry: FeedEntry,
) -> Result<()> {
    if !mutate_delete::<NameCodec, S>(store, name_root, entry.name)? {
        return Err(Error::Corrupt("feed name is missing"));
    }
    if !mutate_delete::<IndexCodec, S>(store, index_root, entry.index)? {
        return Err(Error::Corrupt("feed index is missing"));
    }
    Ok(())
}

pub(crate) fn rename<S: RetiringStore>(
    store: &mut S,
    name_root: &mut u32,
    index_root: &mut u32,
    old: FeedEntry,
    new_name: FeedName,
) -> Result<()> {
    if !mutate_delete::<NameCodec, S>(store, name_root, old.name)? {
        return Err(Error::Corrupt("renamed feed name is missing"));
    }
    let renamed = FeedEntry {
        name: new_name,
        index: old.index,
    };
    let record = Encoded::new(renamed)?;
    if !mutate_insert::<NameCodec, S>(store, name_root, record.as_slice())? {
        return Err(Error::Corrupt("renamed feed name already exists"));
    }
    if mutate_insert::<IndexCodec, S>(store, index_root, record.as_slice())? {
        return Err(Error::Corrupt("renamed feed index was missing"));
    }
    Ok(())
}

fn mutate_insert<C: Codec, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    record: &[u8],
) -> Result<bool> {
    let mut retired = RetiredPages::new();
    let inserted = fixed_tree::insert::<C, S>(store, root, record, &mut retired)?;
    store.retire_pages(retired.as_slice())?;
    Ok(inserted)
}

fn mutate_delete<C: Codec, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
) -> Result<bool> {
    let mut retired = RetiredPages::new();
    let deleted = fixed_tree::delete::<C, S>(store, root, key, &mut retired)?;
    store.retire_pages(retired.as_slice())?;
    Ok(deleted)
}

struct Encoded {
    bytes: [u8; MAX_NAME_RECORD],
    len: usize,
}

impl Encoded {
    fn new(entry: FeedEntry) -> Result<Self> {
        let mut encoded = Self {
            bytes: [0; MAX_NAME_RECORD],
            len: 0,
        };
        encoded.len = encode(entry.name, entry.index, &mut encoded.bytes)?;
        Ok(encoded)
    }

    fn as_slice(&self) -> &[u8] {
        &self.bytes[..self.len]
    }
}

fn encode(name: FeedName, value: u32, output: &mut [u8]) -> Result<usize> {
    let len = NAME_RECORD_BASE + name.as_bytes().len();
    if output.len() < len {
        return Err(Error::InvalidArgument(
            "feed catalog record buffer is too small",
        ));
    }
    output[..len].fill(0);
    output[..2].copy_from_slice(&(len as u16).to_le_bytes());
    output[4..8].copy_from_slice(&value.to_le_bytes());
    output[8] = name.as_bytes().len() as u8;
    output[NAME_RECORD_BASE..len].copy_from_slice(name.as_bytes());
    Ok(len)
}
