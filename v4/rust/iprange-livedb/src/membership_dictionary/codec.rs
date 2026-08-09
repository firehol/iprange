//! Membership dictionary and reverse-index records.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::Codec;
use crate::format::page_type;
use crate::mapping::{ByteRange, ByteSource};
use crate::slotted_page::{self, Header};

pub(crate) const ID_BRANCH: u8 = page_type::MEMBERSHIP_ID_BRANCH;
pub(crate) const ID_LEAF: u8 = page_type::MEMBERSHIP_ID_LEAF;
pub(crate) const HASH_BRANCH: u8 = page_type::MEMBERSHIP_HASH_BRANCH;
pub(crate) const HASH_LEAF: u8 = page_type::MEMBERSHIP_HASH_LEAF;
pub(crate) const ID_KEY_SIZE: usize = 4;
pub(crate) const ID_BRANCH_SIZE: usize = ID_KEY_SIZE + 4;
pub(crate) const HASH_KEY_SIZE: usize = 40;
pub(crate) const HASH_BRANCH_SIZE: usize = HASH_KEY_SIZE + 4;
pub(crate) const ID_BASE: usize = 64;
pub(crate) const MAX_ID_RECORD: usize = PAGE_SIZE - slotted_page::HEADER_SIZE - 2;
pub(crate) const MAX_WORD_COUNT: u32 = 67_108_864;

const ENCODED_RECORD_LIMIT: usize = 512;
pub(super) const INLINE_WORD_LIMIT: u32 = ((ENCODED_RECORD_LIMIT - ID_BASE) / 8) as u32;

const LENGTH_OFFSET: usize = 0;
const STORAGE_OFFSET: usize = 2;
const ID_OFFSET: usize = 4;
pub(super) const REFCOUNT_OFFSET: usize = 8;
const WORD_COUNT_OFFSET: usize = 16;
const BITMAP_LENGTH_OFFSET: usize = 20;
const BLOB_ROOT_OFFSET: usize = 24;
const RESERVED_OFFSET: usize = 28;
const DIGEST_OFFSET: usize = 32;
const BRANCH_KEY_OFFSET: usize = 0;
const HASH_DIGEST_OFFSET: usize = 0;
const HASH_WORD_COUNT_OFFSET: usize = 32;
const HASH_ID_OFFSET: usize = 36;

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
pub(crate) struct HashKey {
    pub(crate) digest: [u8; 32],
    pub(crate) word_count: u32,
    pub(crate) id: u32,
}

pub(super) struct Encoded {
    bytes: [u8; ENCODED_RECORD_LIMIT],
    len: usize,
}

impl Encoded {
    pub(super) fn new(
        id: u32,
        word_count: u32,
        digest: [u8; 32],
        storage: Storage,
    ) -> Result<Self> {
        let bitmap_len = word_count
            .checked_mul(8)
            .ok_or_else(|| Error::arithmetic_overflow("membership bitmap length"))?;
        let len = match storage {
            Storage::Inline => ID_BASE
                .checked_add(bitmap_len as usize)
                .ok_or_else(|| Error::arithmetic_overflow("membership record length"))?,
            Storage::Blob(root) if root >= 2 => ID_BASE,
            Storage::Blob(_) => {
                return Err(Error::Corrupt("membership blob root is invalid"));
            }
        };
        if id == 0 || word_count == 0 || word_count > MAX_WORD_COUNT || len > ENCODED_RECORD_LIMIT {
            return Err(Error::InvalidArgument(
                "membership record fields are outside the v4 limit",
            ));
        }
        let mut encoded = Self {
            bytes: [0; ENCODED_RECORD_LIMIT],
            len,
        };
        encoded.bytes[LENGTH_OFFSET..LENGTH_OFFSET + 2]
            .copy_from_slice(&(len as u16).to_le_bytes());
        encoded.bytes[STORAGE_OFFSET] = u8::from(matches!(storage, Storage::Blob(_)));
        encoded.bytes[ID_OFFSET..ID_OFFSET + 4].copy_from_slice(&id.to_le_bytes());
        encoded.bytes[WORD_COUNT_OFFSET..WORD_COUNT_OFFSET + 4]
            .copy_from_slice(&word_count.to_le_bytes());
        encoded.bytes[BITMAP_LENGTH_OFFSET..BITMAP_LENGTH_OFFSET + 4]
            .copy_from_slice(&bitmap_len.to_le_bytes());
        if let Storage::Blob(root) = storage {
            encoded.bytes[BLOB_ROOT_OFFSET..BLOB_ROOT_OFFSET + 4]
                .copy_from_slice(&root.to_le_bytes());
        }
        encoded.bytes[DIGEST_OFFSET..DIGEST_OFFSET + 32].copy_from_slice(&digest);
        Ok(encoded)
    }

    pub(super) fn put_inline_word(&mut self, index: usize, value: u64) -> Result<()> {
        let at = ID_BASE
            .checked_add(
                index
                    .checked_mul(8)
                    .ok_or_else(|| Error::arithmetic_overflow("membership inline offset"))?,
            )
            .ok_or_else(|| Error::arithmetic_overflow("membership inline offset"))?;
        let end = at
            .checked_add(8)
            .ok_or_else(|| Error::arithmetic_overflow("membership inline offset"))?;
        if end > self.len {
            return Err(Error::Corrupt(
                "membership inline bitmap exceeds its record",
            ));
        }
        let output = self
            .bytes
            .get_mut(at..end)
            .ok_or_else(|| Error::corrupt("membership inline bitmap exceeds its record"))?;
        output.copy_from_slice(&value.to_le_bytes());
        Ok(())
    }

    pub(super) fn as_slice(&self) -> &[u8] {
        &self.bytes[..self.len]
    }
}

pub(crate) struct IdCodec;

impl Codec for IdCodec {
    type Key = u32;
    type Leaf = Record;

    const BRANCH_TYPE: u8 = ID_BRANCH;
    const LEAF_TYPE: u8 = ID_LEAF;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = ID_KEY_SIZE;
    const LEAF_SIZE: usize = 0;
    const MAX_LEAF_SIZE: usize = MAX_ID_RECORD;

    fn read_key<S: ByteSource>(cell: S, level: u16) -> Result<Self::Key> {
        if level == 0 {
            Ok(u32_le(cell, ID_OFFSET))
        } else {
            decode_id_branch(cell).map(|(id, _)| id)
        }
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        decode_canonical(cell)
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[..4].copy_from_slice(&key.to_le_bytes());
    }

    fn leaf_cell<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<ByteRange<S>> {
        slotted_page::record(page, header, index, ID_BASE, MAX_ID_RECORD)
    }
}

pub(super) struct HashCodec;

impl Codec for HashCodec {
    type Key = HashKey;
    type Leaf = HashKey;

    const BRANCH_TYPE: u8 = HASH_BRANCH;
    const LEAF_TYPE: u8 = HASH_LEAF;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = HASH_KEY_SIZE;
    const LEAF_SIZE: usize = HASH_KEY_SIZE;

    fn read_key<S: ByteSource>(cell: S, level: u16) -> Result<Self::Key> {
        if level == 0 {
            decode_hash(cell)
        } else {
            decode_hash_branch(cell).map(|(key, _)| key)
        }
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        decode_hash(cell)
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        write_hash(key, output);
    }
}

pub(crate) fn decode<S: ByteSource>(cell: S) -> Result<Record> {
    require_record_envelope(cell)?;
    let id = u32_le(cell, ID_OFFSET);
    let refcount = u64_le(cell, REFCOUNT_OFFSET);
    let word_count = u32_le(cell, WORD_COUNT_OFFSET);
    let bitmap_len = u32_le(cell, BITMAP_LENGTH_OFFSET);
    let blob_root = u32_le(cell, BLOB_ROOT_OFFSET);
    require_record_fields(cell, id, word_count, bitmap_len)?;
    let storage = decode_storage(cell, bitmap_len, blob_root)?;
    let digest = cell
        .array(DIGEST_OFFSET)
        .ok_or_else(|| Error::corrupt("membership dictionary record is malformed"))?;
    Ok(Record {
        id,
        refcount,
        word_count,
        digest,
        storage,
    })
}

pub(crate) fn decode_canonical<S: ByteSource>(cell: S) -> Result<Record> {
    let record = decode(cell)?;
    if record.storage == Storage::Inline && u64_le(cell, cell.len() - 8) == 0 {
        return Err(Error::Corrupt("membership bitmap is not canonical"));
    }
    Ok(record)
}

fn require_record_envelope<S: ByteSource>(cell: S) -> Result<()> {
    if cell.len() < ID_BASE
        || usize::from(u16_le(cell, LENGTH_OFFSET)) != cell.len()
        || cell.byte(3) != Some(0)
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
        || u32_le(cell, RESERVED_OFFSET) != 0
    {
        return Err(Error::Corrupt("membership dictionary fields are malformed"));
    }
    Ok(())
}

fn decode_storage<S: ByteSource>(cell: S, bitmap_len: u32, blob_root: u32) -> Result<Storage> {
    match cell.byte(STORAGE_OFFSET) {
        Some(0) if blob_root == 0 && cell.len() == ID_BASE + bitmap_len as usize => {
            Ok(Storage::Inline)
        }
        Some(1) if blob_root >= 2 && cell.len() == ID_BASE => Ok(Storage::Blob(blob_root)),
        _ => Err(Error::Corrupt("membership dictionary storage is malformed")),
    }
}

pub(crate) fn inline_bytes<S: ByteSource>(cell: S, record: Record) -> Result<ByteRange<S>> {
    if record.storage != Storage::Inline {
        return Err(Error::Corrupt("membership dictionary entry is not inline"));
    }
    let length = usize::try_from(record.word_count)
        .ok()
        .and_then(|words| words.checked_mul(8))
        .ok_or_else(|| Error::arithmetic_overflow("membership inline length"))?;
    ByteRange::new(cell, ID_BASE, length)
        .ok_or_else(|| Error::corrupt("membership inline bitmap is malformed"))
}

pub(crate) fn inline_page_offset<S: ByteSource>(
    cell: ByteRange<S>,
    record: Record,
) -> Result<usize> {
    inline_bytes(cell, record)?;
    cell.source_offset()
        .checked_add(ID_BASE)
        .ok_or_else(|| Error::arithmetic_overflow("membership inline offset"))
}

pub(crate) fn decode_hash<S: ByteSource>(cell: S) -> Result<HashKey> {
    if cell.len() != HASH_KEY_SIZE {
        return Err(Error::Corrupt("membership hash record is too short"));
    }
    let digest = cell
        .array(HASH_DIGEST_OFFSET)
        .ok_or_else(|| Error::corrupt("membership hash record is too short"))?;
    let word_count = u32_le(cell, HASH_WORD_COUNT_OFFSET);
    let id = u32_le(cell, HASH_ID_OFFSET);
    if word_count == 0 || word_count > MAX_WORD_COUNT || id == 0 {
        return Err(Error::Corrupt("membership hash record is malformed"));
    }
    Ok(HashKey {
        digest,
        word_count,
        id,
    })
}

pub(crate) fn decode_id_branch<S: ByteSource>(cell: S) -> Result<(u32, u32)> {
    if cell.len() != ID_BRANCH_SIZE {
        return Err(Error::Corrupt("membership ID branch record is malformed"));
    }
    Ok((u32_le(cell, BRANCH_KEY_OFFSET), u32_le(cell, ID_KEY_SIZE)))
}

pub(crate) fn decode_hash_branch<S: ByteSource>(cell: S) -> Result<(HashKey, u32)> {
    if cell.len() != HASH_BRANCH_SIZE {
        return Err(Error::Corrupt("membership hash branch record is malformed"));
    }
    let key = decode_hash(
        ByteRange::new(cell, BRANCH_KEY_OFFSET, HASH_KEY_SIZE)
            .ok_or_else(|| Error::corrupt("membership hash branch record is malformed"))?,
    )?;
    Ok((key, u32_le(cell, HASH_KEY_SIZE)))
}

pub(super) fn encode_hash(key: HashKey) -> [u8; HASH_KEY_SIZE] {
    let mut record = [0; HASH_KEY_SIZE];
    write_hash(key, &mut record);
    record
}

fn write_hash(key: HashKey, output: &mut [u8]) {
    output[HASH_DIGEST_OFFSET..HASH_WORD_COUNT_OFFSET].copy_from_slice(&key.digest);
    output[HASH_WORD_COUNT_OFFSET..HASH_ID_OFFSET].copy_from_slice(&key.word_count.to_le_bytes());
    output[HASH_ID_OFFSET..HASH_KEY_SIZE].copy_from_slice(&key.id.to_le_bytes());
}
