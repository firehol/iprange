//! Membership dictionary and reverse-index records.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::Codec;
use crate::mapping::{ByteRange, ByteSource};
use crate::slotted_page::{self, Header};

pub(crate) const ID_BRANCH: u8 = 7;
pub(crate) const ID_LEAF: u8 = 8;
pub(super) const HASH_BRANCH: u8 = 9;
pub(super) const HASH_LEAF: u8 = 10;
pub(crate) const ID_BASE: usize = 64;
pub(crate) const MAX_ID_RECORD: usize = PAGE_SIZE - slotted_page::HEADER_SIZE - 2;
pub(crate) const MAX_WORD_COUNT: u32 = 67_108_864;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Storage {
    Inline,
    Blob(u32),
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct Record {
    pub(crate) id: u32,
    pub(crate) refcount: u64,
    pub(crate) word_count: u32,
    pub(crate) digest: [u8; 32],
    pub(crate) storage: Storage,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub(super) struct HashKey {
    pub(super) digest: [u8; 32],
    pub(super) word_count: u32,
    pub(super) id: u32,
}

pub(super) struct IdCodec;

impl Codec for IdCodec {
    type Key = u32;

    const BRANCH_TYPE: u8 = ID_BRANCH;
    const LEAF_TYPE: u8 = ID_LEAF;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 4;
    const LEAF_SIZE: usize = 0;
    const MAX_LEAF_SIZE: usize = MAX_ID_RECORD;

    fn read_key<S: ByteSource>(cell: S, level: u16) -> Result<Self::Key> {
        if level == 0 {
            decode(cell).map(|record| record.id)
        } else {
            Ok(u32_le(cell, 0))
        }
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[..4].copy_from_slice(&key.to_le_bytes());
    }

    fn validate_leaf<S: ByteSource>(cell: S) -> Result<()> {
        decode(cell).map(|_| ())
    }

    fn leaf_cell<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<ByteRange<S>> {
        slotted_page::record(page, header, index, ID_BASE, MAX_ID_RECORD)
    }
}

pub(super) struct HashCodec;

impl Codec for HashCodec {
    type Key = HashKey;

    const BRANCH_TYPE: u8 = HASH_BRANCH;
    const LEAF_TYPE: u8 = HASH_LEAF;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 40;
    const LEAF_SIZE: usize = 40;

    fn read_key<S: ByteSource>(cell: S, _level: u16) -> Result<Self::Key> {
        decode_hash(cell)
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[..32].copy_from_slice(&key.digest);
        output[32..36].copy_from_slice(&key.word_count.to_le_bytes());
        output[36..40].copy_from_slice(&key.id.to_le_bytes());
    }

    fn validate_leaf<S: ByteSource>(cell: S) -> Result<()> {
        decode_hash(cell).map(|_| ())
    }
}

pub(crate) fn decode<S: ByteSource>(cell: S) -> Result<Record> {
    require_record_envelope(cell)?;
    let id = u32_le(cell, 4);
    let refcount = u64_le(cell, 8);
    let word_count = u32_le(cell, 16);
    let bitmap_len = u32_le(cell, 20);
    let blob_root = u32_le(cell, 24);
    require_record_fields(cell, id, word_count, bitmap_len)?;
    let storage = decode_storage(cell, bitmap_len, blob_root)?;
    let digest = cell
        .array(32)
        .ok_or(Error::Corrupt("membership dictionary record is malformed"))?;
    Ok(Record {
        id,
        refcount,
        word_count,
        digest,
        storage,
    })
}

fn require_record_envelope<S: ByteSource>(cell: S) -> Result<()> {
    if cell.len() < ID_BASE || usize::from(u16_le(cell, 0)) != cell.len() || cell.byte(3) != Some(0)
    {
        return Err(Error::Corrupt("membership dictionary record is malformed"));
    }
    Ok(())
}

fn require_record_fields<S: ByteSource>(
    cell: S,
    id: u32,
    word_count: u32,
    bitmap_len: u32,
) -> Result<()> {
    if id == 0
        || word_count == 0
        || word_count > MAX_WORD_COUNT
        || bitmap_len != word_count.checked_mul(8).unwrap()
        || u32_le(cell, 28) != 0
    {
        return Err(Error::Corrupt("membership dictionary fields are malformed"));
    }
    Ok(())
}

fn decode_storage<S: ByteSource>(cell: S, bitmap_len: u32, blob_root: u32) -> Result<Storage> {
    match cell.byte(2) {
        Some(0) if blob_root == 0 && cell.len() == ID_BASE + bitmap_len as usize => {
            if u64_le(cell, cell.len() - 8) == 0 {
                return Err(Error::Corrupt("membership bitmap is not canonical"));
            }
            Ok(Storage::Inline)
        }
        Some(1) if blob_root >= 2 && cell.len() == ID_BASE => Ok(Storage::Blob(blob_root)),
        _ => Err(Error::Corrupt("membership dictionary storage is malformed")),
    }
}

pub(super) fn decode_hash<S: ByteSource>(cell: S) -> Result<HashKey> {
    if cell.len() < 40 {
        return Err(Error::Corrupt("membership hash record is too short"));
    }
    let digest = cell
        .array(0)
        .ok_or(Error::Corrupt("membership hash record is too short"))?;
    let word_count = u32_le(cell, 32);
    let id = u32_le(cell, 36);
    if word_count == 0 || word_count > MAX_WORD_COUNT || id == 0 {
        return Err(Error::Corrupt("membership hash record is malformed"));
    }
    Ok(HashKey {
        digest,
        word_count,
        id,
    })
}

pub(super) fn encode_hash(key: HashKey) -> [u8; 40] {
    let mut record = [0; 40];
    record[..32].copy_from_slice(&key.digest);
    record[32..36].copy_from_slice(&key.word_count.to_le_bytes());
    record[36..].copy_from_slice(&key.id.to_le_bytes());
    record
}
