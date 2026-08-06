use crate::contract::{u16_le, u32_le, u64_le};
use crate::mapping::{ByteRange, ByteSource};

use super::ID_BASE;

const MAX_WORD_COUNT: u32 = 67_108_864;

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub(super) struct HashKey {
    pub(super) digest: [u8; 32],
    pub(super) word_count: u32,
    pub(super) id: u32,
}

#[derive(Clone, Copy)]
pub(super) enum Storage<P> {
    Inline(ByteRange<P>),
    Blob(u32),
}

#[derive(Clone, Copy)]
pub(super) struct Record<P> {
    pub(super) id: u32,
    pub(super) refcount: u64,
    pub(super) word_count: u32,
    pub(super) digest: [u8; 32],
    pub(super) storage: Storage<P>,
}

pub(super) fn decode_record<P: ByteSource>(cell: P) -> Option<Record<P>> {
    if !record_header_valid(cell) {
        return None;
    }
    let id = u32_le(cell, 4);
    let refcount = u64_le(cell, 8);
    let word_count = u32_le(cell, 16);
    let bitmap_len = word_count.checked_mul(8)?;
    if !record_fields_valid(cell, id, word_count, bitmap_len) {
        return None;
    }
    let blob_root = u32_le(cell, 24);
    let storage = record_storage(cell, blob_root, bitmap_len)?;
    let digest = cell.array(32)?;
    Some(Record {
        id,
        refcount,
        word_count,
        digest,
        storage,
    })
}

fn record_header_valid<P: ByteSource>(cell: P) -> bool {
    cell.len() >= ID_BASE
        && usize::from(u16_le(cell, 0)) == cell.len()
        && cell.byte(3) == Some(0)
        && u32_le(cell, 28) == 0
}

fn record_fields_valid<P: ByteSource>(cell: P, id: u32, word_count: u32, bitmap_len: u32) -> bool {
    id != 0 && word_count != 0 && word_count <= MAX_WORD_COUNT && u32_le(cell, 20) == bitmap_len
}

fn record_storage<P: ByteSource>(cell: P, blob_root: u32, bitmap_len: u32) -> Option<Storage<P>> {
    match cell.byte(2)? {
        0 if blob_root == 0 && cell.len() == ID_BASE + bitmap_len as usize => Some(
            Storage::Inline(ByteRange::new(cell, ID_BASE, bitmap_len as usize)?),
        ),
        1 if blob_root >= 2 && cell.len() == ID_BASE => Some(Storage::Blob(blob_root)),
        _ => None,
    }
}

pub(super) fn decode_hash<P: ByteSource>(cell: P) -> Option<HashKey> {
    if cell.len() != 40 {
        return None;
    }
    let digest = cell.array(0)?;
    let word_count = u32_le(cell, 32);
    let id = u32_le(cell, 36);
    if id == 0 || word_count == 0 || word_count > MAX_WORD_COUNT {
        return None;
    }
    Some(HashKey {
        digest,
        word_count,
        id,
    })
}
