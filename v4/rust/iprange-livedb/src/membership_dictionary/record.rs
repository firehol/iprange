//! Membership-record storage and word reads.

use crate::blob_tree;
use crate::contract::u64_le;
use crate::error::{Error, Result};
use crate::fixed_tree::{self, LeafLocation, Store};

use super::blob;
use super::codec::{self, decode_canonical, Encoded, IdCodec, Record, Storage, INLINE_WORD_LIMIT};
use super::Words;

#[derive(Clone, Copy)]
pub(super) struct Found {
    location: LeafLocation,
    pub(super) record: Record,
}

pub(super) fn encode<S: Store, W: Words<S>>(
    store: &mut S,
    words: &W,
    id: u32,
    digest: [u8; 32],
) -> Result<Encoded> {
    let inline = words.word_count() <= INLINE_WORD_LIMIT;
    let storage = if inline {
        Storage::Inline
    } else {
        Storage::Blob(blob::build(store, words)?)
    };
    let mut encoded = Encoded::new(id, words.word_count(), digest, storage)?;
    if inline {
        encode_inline(store, words, &mut encoded)?;
    }
    Ok(encoded)
}

fn encode_inline<S: Store, W: Words<S>>(store: &S, words: &W, encoded: &mut Encoded) -> Result<()> {
    const WORD_BATCH: usize = 32;
    let mut values = [0u64; WORD_BATCH];
    let mut start = 0u32;
    while start < words.word_count() {
        let count = (words.word_count() - start).min(WORD_BATCH as u32) as usize;
        words.read_words(store, start, &mut values[..count])?;
        for (offset, word) in values[..count].iter().enumerate() {
            encoded.put_inline_word(start as usize + offset, *word)?;
        }
        start += count as u32;
    }
    Ok(())
}

pub(super) fn find<S: Store>(store: &S, root: u32, id: u32) -> Result<Option<Found>> {
    crate::work::membership_lookup(1);
    if id == 0 || root == 0 {
        return Ok(None);
    }
    let Some(found) = fixed_tree::predecessor_located::<IdCodec, S>(store, root, id)? else {
        return Ok(None);
    };
    let (location, record) = found;
    Ok((record.id == id).then_some(Found { location, record }))
}

pub(super) fn read_words<S: Store>(
    store: &S,
    id_root: u32,
    id: u32,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    let found = find(store, id_root, id)?.ok_or(Error::Corrupt("membership ID is missing"))?;
    read_record_words(store, &found, start, output)
}

pub(super) fn read_record_words<S: Store>(
    store: &S,
    found: &Found,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    let end = u64::from(start)
        .checked_add(output.len() as u64)
        .ok_or(Error::ArithmeticOverflow("membership word range"))?;
    if end > u64::from(found.record.word_count) {
        return Err(Error::Corrupt("membership word range exceeds its bitmap"));
    }
    match found.record.storage {
        Storage::Inline => {
            fixed_tree::inspect_leaf::<IdCodec, S, _, _>(store, found.location, |cell| {
                let current = decode_canonical(cell)?;
                if current.id != found.record.id
                    || current.word_count != found.record.word_count
                    || current.storage != Storage::Inline
                {
                    return Err(Error::Corrupt("membership record changed during read"));
                }
                let bytes = codec::inline_bytes(cell, current)?;
                for (index, word) in output.iter_mut().enumerate() {
                    *word = u64_le(bytes, (start as usize + index) * 8);
                }
                Ok(())
            })
        }
        Storage::Blob(root) => {
            blob_tree::read_words_from(store, root, found.record.word_count, start, output)
        }
    }
}
