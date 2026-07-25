//! Membership-record storage and word reads.

use crate::blob_tree;
use crate::contract::u64_le;
use crate::error::{Error, Result};
use crate::fixed_tree::{self, LeafBuf, RetiredPages, RetiringStore, Store};

use super::blob;
use super::codec::{decode, IdCodec, Record, Storage, ID_BASE, MAX_ID_RECORD};
use super::Words;

const MAX_INLINE_WORDS: u32 = ((MAX_ID_RECORD - ID_BASE) / 8) as u32;

pub(super) struct Encoded {
    bytes: [u8; MAX_ID_RECORD],
    len: usize,
}

impl Encoded {
    pub(super) fn as_slice(&self) -> &[u8] {
        &self.bytes[..self.len]
    }
}

pub(super) fn encode<S: Store, W: Words<S>>(
    store: &mut S,
    words: &W,
    id: u32,
    digest: [u8; 32],
) -> Result<Encoded> {
    let inline = words.word_count() <= MAX_INLINE_WORDS;
    let len = if inline {
        ID_BASE + words.word_count() as usize * 8
    } else {
        ID_BASE
    };
    let blob_root = if inline {
        0
    } else {
        blob::build(store, words)?
    };
    let mut encoded = Encoded {
        bytes: [0; MAX_ID_RECORD],
        len,
    };
    encoded.bytes[..2].copy_from_slice(&(len as u16).to_le_bytes());
    encoded.bytes[2] = u8::from(!inline);
    encoded.bytes[4..8].copy_from_slice(&id.to_le_bytes());
    encoded.bytes[16..20].copy_from_slice(&words.word_count().to_le_bytes());
    encoded.bytes[20..24].copy_from_slice(&(words.word_count() * 8).to_le_bytes());
    encoded.bytes[24..28].copy_from_slice(&blob_root.to_le_bytes());
    encoded.bytes[32..64].copy_from_slice(&digest);
    if inline {
        encode_inline(store, words, &mut encoded)?;
    }
    Ok(encoded)
}

fn encode_inline<S: Store, W: Words<S>>(store: &S, words: &W, encoded: &mut Encoded) -> Result<()> {
    let mut values = [0u64; MAX_INLINE_WORDS as usize];
    words.read_words(store, 0, &mut values[..words.word_count() as usize])?;
    for (index, word) in values[..words.word_count() as usize].iter().enumerate() {
        encoded.bytes[ID_BASE + index * 8..ID_BASE + index * 8 + 8]
            .copy_from_slice(&word.to_le_bytes());
    }
    Ok(())
}

pub(super) fn find<S: Store>(store: &S, root: u32, id: u32) -> Result<Option<LeafBuf>> {
    if id == 0 || root == 0 {
        return Ok(None);
    }
    let found = fixed_tree::predecessor::<IdCodec, S>(store, root, id)?;
    match found {
        Some(found) if decode(found.as_slice())?.id == id => Ok(Some(found)),
        _ => Ok(None),
    }
}

pub(super) fn read_words<S: Store>(
    store: &S,
    id_root: u32,
    id: u32,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    let found = find(store, id_root, id)?.ok_or(Error::Corrupt("membership ID is missing"))?;
    let record = decode(found.as_slice())?;
    read_record_words(store, found.as_slice(), &record, start, output)
}

pub(super) fn read_record_words<S: Store>(
    store: &S,
    cell: &[u8],
    record: &Record,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    let end = u64::from(start)
        .checked_add(output.len() as u64)
        .ok_or(Error::ArithmeticOverflow("membership word range"))?;
    if end > u64::from(record.word_count) {
        return Err(Error::Corrupt("membership word range exceeds its bitmap"));
    }
    match record.storage {
        Storage::Inline => {
            for (index, word) in output.iter_mut().enumerate() {
                *word = u64_le(cell, ID_BASE + (start as usize + index) * 8);
            }
            Ok(())
        }
        Storage::Blob(root) => {
            blob_tree::read_words_from(store, root, record.word_count, start, output)
        }
    }
}

pub(super) fn replace_refcount<S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    source: &[u8],
    refcount: u64,
) -> Result<()> {
    let mut encoded = Encoded {
        bytes: [0; MAX_ID_RECORD],
        len: source.len(),
    };
    encoded.bytes[..source.len()].copy_from_slice(source);
    encoded.bytes[8..16].copy_from_slice(&refcount.to_le_bytes());
    let mut retired = RetiredPages::new();
    if fixed_tree::insert::<IdCodec, S>(store, root, encoded.as_slice(), &mut retired)? {
        return Err(Error::Corrupt("membership refcount ID disappeared"));
    }
    store.retire_pages(retired.as_slice())
}
