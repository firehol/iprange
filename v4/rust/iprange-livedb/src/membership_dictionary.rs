//! Canonical membership hashing, lookup, and interning.

mod algebra;
mod blob;
mod codec;
mod record;

use sha2::{Digest, Sha256};

use crate::error::{Error, Result};
use crate::fixed_tree::{self, RetiredPages, RetiringStore, Store};
use crate::membership_delta::Delta;
use crate::used_bitmap::{self, Kind};

use codec::{
    decode, decode_hash, encode_hash, HashCodec, HashKey, IdCodec, Record, Storage, MAX_WORD_COUNT,
};
use record::{
    encode as encode_record, find as find_record, read_record_words, read_words, replace_refcount,
};

pub(crate) use algebra::combine;

const HASH_WORDS: usize = 64;

pub(crate) trait Words<S: Store> {
    fn word_count(&self) -> u32;
    fn read_words(&self, store: &S, start: u32, output: &mut [u64]) -> Result<()>;
}

#[derive(Clone, Copy)]
pub(crate) struct State {
    pub(crate) id_root: u32,
    pub(crate) hash_root: u32,
    pub(crate) used_root: u32,
    pub(crate) entry_count: u64,
    pub(crate) id_limit: u64,
}

#[derive(Debug)]
pub(crate) struct Interned {
    pub(crate) id: u32,
    pub(crate) word_count: u32,
    pub(crate) created: bool,
}

struct AddedBit {
    id_root: u32,
    base_id: u32,
    base_words: u32,
    bit: u32,
}

impl<S: Store> Words<S> for AddedBit {
    fn word_count(&self) -> u32 {
        self.base_words.max(self.bit / 64 + 1)
    }

    fn read_words(&self, store: &S, start: u32, output: &mut [u64]) -> Result<()> {
        output.fill(0);
        if self.base_id != 0 && start < self.base_words {
            let count = output.len().min((self.base_words - start) as usize);
            record::read_words(
                store,
                self.id_root,
                self.base_id,
                start,
                &mut output[..count],
            )?;
        }
        let word = self.bit / 64;
        if word >= start && u64::from(word - start) < output.len() as u64 {
            output[(word - start) as usize] |= 1u64 << (self.bit % 64);
        }
        Ok(())
    }
}

pub(crate) fn intern_added_bit<S: RetiringStore>(
    store: &mut S,
    state: &mut State,
    base_id: u32,
    base_words: u32,
    bit: u32,
) -> Result<Interned> {
    if base_id != 0 {
        let base = find_record(store, state.id_root, base_id)?
            .ok_or(Error::Corrupt("membership reference ID is missing"))?;
        let record = decode(base.as_slice())?;
        if record.word_count != base_words {
            return Err(Error::Corrupt("membership reference length changed"));
        }
        let word_index = bit / 64;
        if word_index < base_words {
            let mut word = [0u64; 1];
            read_record_words(store, base.as_slice(), &record, word_index, &mut word)?;
            if word[0] & (1u64 << (bit % 64)) != 0 {
                return Ok(Interned {
                    id: base_id,
                    word_count: base_words,
                    created: false,
                });
            }
        }
    }
    let source = AddedBit {
        id_root: state.id_root,
        base_id,
        base_words,
        bit,
    };
    intern(store, state, &source)
}

pub(crate) fn apply_delta<S: RetiringStore>(
    store: &mut S,
    state: &mut State,
    delta: Delta,
) -> Result<()> {
    let found = find_record(store, state.id_root, delta.id)?
        .ok_or(Error::Corrupt("membership delta ID is missing"))?;
    let record = decode(found.as_slice())?;
    let refcount = changed_refcount(record.refcount, delta.change)?;
    if refcount != 0 {
        return replace_refcount(store, &mut state.id_root, found.as_slice(), refcount);
    }
    remove_record(store, state, &record)
}

pub(crate) fn reference_matches<S: Store>(
    store: &S,
    id_root: u32,
    id: u32,
    word_count: u32,
) -> Result<bool> {
    if id == 0 {
        return Ok(word_count == 0);
    }
    let Some(found) = find_record(store, id_root, id)? else {
        return Ok(false);
    };
    Ok(decode(found.as_slice())?.word_count == word_count)
}

pub(super) fn intern<S: RetiringStore, W: Words<S>>(
    store: &mut S,
    state: &mut State,
    words: &W,
) -> Result<Interned> {
    let word_count = words.word_count();
    require_word_count(word_count)?;
    let digest = hash_words(store, words)?;
    if let Some(id) = find_equal(store, state, words, digest)? {
        return Ok(Interned {
            id,
            word_count,
            created: false,
        });
    }
    insert_new(store, state, words, digest)
}

fn require_word_count(word_count: u32) -> Result<()> {
    if word_count == 0 || word_count > MAX_WORD_COUNT {
        Err(Error::InvalidArgument(
            "membership word count is outside the v4 limit",
        ))
    } else {
        Ok(())
    }
}

fn insert_new<S: RetiringStore, W: Words<S>>(
    store: &mut S,
    state: &mut State,
    words: &W,
    digest: [u8; 32],
) -> Result<Interned> {
    let id = allocate_id(store, state)?;
    let record = encode_record(store, words, id, digest)?;
    mutate_insert::<IdCodec, S>(store, &mut state.id_root, record.as_slice())?;
    let hash = encode_hash(HashKey {
        digest,
        word_count: words.word_count(),
        id,
    });
    mutate_insert::<HashCodec, S>(store, &mut state.hash_root, &hash)?;
    state.entry_count = state
        .entry_count
        .checked_add(1)
        .ok_or(Error::ArithmeticOverflow("membership entry count"))?;
    Ok(Interned {
        id,
        word_count: words.word_count(),
        created: true,
    })
}

fn find_equal<S: Store, W: Words<S>>(
    store: &S,
    state: &State,
    words: &W,
    digest: [u8; 32],
) -> Result<Option<u32>> {
    if state.hash_root == 0 {
        return Ok(None);
    }
    let mut key = HashKey {
        digest,
        word_count: words.word_count(),
        id: 1,
    };
    loop {
        let Some(found) = fixed_tree::at_or_after::<HashCodec, S>(store, state.hash_root, key)?
        else {
            return Ok(None);
        };
        let candidate = decode_hash(found.as_slice())?;
        if candidate.digest != digest || candidate.word_count != words.word_count() {
            return Ok(None);
        }
        if equal_words(store, state.id_root, candidate.id, words)? {
            return Ok(Some(candidate.id));
        }
        let Some(next) = candidate.id.checked_add(1) else {
            return Ok(None);
        };
        key.id = next;
    }
}

fn equal_words<S: Store, W: Words<S>>(
    store: &S,
    id_root: u32,
    id: u32,
    expected: &W,
) -> Result<bool> {
    let Some(found) = find_record(store, id_root, id)? else {
        return Err(Error::Corrupt("membership hash points to a missing ID"));
    };
    let record = decode(found.as_slice())?;
    if record.word_count != expected.word_count() {
        return Ok(false);
    }
    let mut actual = [0u64; HASH_WORDS];
    let mut wanted = [0u64; HASH_WORDS];
    let mut start = 0u32;
    while start < record.word_count {
        let count = (record.word_count - start).min(HASH_WORDS as u32) as usize;
        read_record_words(
            store,
            found.as_slice(),
            &record,
            start,
            &mut actual[..count],
        )?;
        expected.read_words(store, start, &mut wanted[..count])?;
        if actual[..count] != wanted[..count] {
            return Ok(false);
        }
        start += count as u32;
    }
    Ok(true)
}

fn hash_words<S: Store, W: Words<S>>(store: &S, words: &W) -> Result<[u8; 32]> {
    let mut hasher = Sha256::new();
    let mut buffer = [0u64; HASH_WORDS];
    let mut start = 0u32;
    while start < words.word_count() {
        let count = (words.word_count() - start).min(HASH_WORDS as u32) as usize;
        words.read_words(store, start, &mut buffer[..count])?;
        for word in &buffer[..count] {
            hasher.update(word.to_le_bytes());
        }
        start += count as u32;
    }
    Ok(hasher.finalize().into())
}

fn allocate_id<S: RetiringStore>(store: &mut S, state: &mut State) -> Result<u32> {
    let mut retired = RetiredPages::new();
    if let Some(id) = used_bitmap::take_lowest(
        store,
        &mut state.used_root,
        state.id_limit,
        Kind::Membership,
        &mut retired,
    )? {
        store.retire_pages(retired.as_slice())?;
        return Ok(id);
    }
    if state.id_limit == 1u64 << 32 {
        return Err(Error::MembershipIdExhausted);
    }
    let id = state.id_limit as u32;
    state.id_limit += 1;
    used_bitmap::set(
        store,
        &mut state.used_root,
        state.id_limit,
        Kind::Membership,
        id,
        &mut retired,
    )?;
    store.retire_pages(retired.as_slice())?;
    Ok(id)
}

fn mutate_insert<C: crate::fixed_tree::Codec, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    record: &[u8],
) -> Result<()> {
    let mut retired = RetiredPages::new();
    if !fixed_tree::insert::<C, S>(store, root, record, &mut retired)? {
        return Err(Error::Corrupt("membership dictionary key already exists"));
    }
    store.retire_pages(retired.as_slice())
}

fn remove_record<S: RetiringStore>(
    store: &mut S,
    state: &mut State,
    record: &Record,
) -> Result<()> {
    remove_indexes(store, state, record)?;
    release_storage(store, record)?;
    clear_used_id(store, state, record.id)?;
    state.entry_count = state
        .entry_count
        .checked_sub(1)
        .ok_or(Error::ArithmeticOverflow("membership entry count"))?;
    state.id_limit = used_bitmap::shrink_membership(store, &mut state.used_root, state.id_limit)?;
    Ok(())
}

fn remove_indexes<S: RetiringStore>(
    store: &mut S,
    state: &mut State,
    record: &Record,
) -> Result<()> {
    mutate_delete::<IdCodec, S>(store, &mut state.id_root, record.id)?;
    mutate_delete::<HashCodec, S>(
        store,
        &mut state.hash_root,
        HashKey {
            digest: record.digest,
            word_count: record.word_count,
            id: record.id,
        },
    )
}

fn release_storage<S: RetiringStore>(store: &mut S, record: &Record) -> Result<()> {
    if let Storage::Blob(root) = record.storage {
        blob::release(store, root, record.word_count)
    } else {
        Ok(())
    }
}

fn clear_used_id<S: RetiringStore>(store: &mut S, state: &mut State, id: u32) -> Result<()> {
    let mut retired = RetiredPages::new();
    if !used_bitmap::clear(
        store,
        &mut state.used_root,
        state.id_limit,
        Kind::Membership,
        id,
        &mut retired,
    )? {
        return Err(Error::Corrupt("membership used bit is missing"));
    }
    store.retire_pages(retired.as_slice())
}

fn mutate_delete<C: crate::fixed_tree::Codec, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
) -> Result<()> {
    let mut retired = RetiredPages::new();
    if !fixed_tree::delete::<C, S>(store, root, key, &mut retired)? {
        return Err(Error::Corrupt("membership dictionary key is missing"));
    }
    store.retire_pages(retired.as_slice())
}

fn changed_refcount(current: u64, change: i64) -> Result<u64> {
    if change >= 0 {
        current
            .checked_add(change as u64)
            .ok_or(Error::ArithmeticOverflow("membership refcount"))
    } else {
        current
            .checked_sub(change.unsigned_abs())
            .ok_or(Error::ArithmeticOverflow("membership refcount"))
    }
}

#[cfg(test)]
#[path = "membership_dictionary_tests.rs"]
mod tests;
