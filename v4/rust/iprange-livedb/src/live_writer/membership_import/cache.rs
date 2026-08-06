//! Operation-private import translation indexes.

use crate::contract::{u32_le, u64_le};
use crate::draft_store::DraftStore;
use crate::error::{Error, Result};
use crate::fixed_tree::{self, Codec, RetiredPages, RetiringStore, Store};
use crate::mapping::ByteSource;
use crate::membership_dictionary::Words;
use crate::slotted_page::{put_u32, put_u64};

const CACHE_BRANCH: u8 = 240;
const CACHE_LEAF: u8 = 241;
const CACHE_AUX: u32 = 0x494d_5043;
const FEED_KEY: u64 = 1;
const MEMBERSHIP_KEY: u64 = 2;
const TRANSLATED_KEY: u64 = 3;

pub(super) struct ImportCache {
    root: u32,
    source_memberships: u64,
    translated_memberships: u64,
}

pub(super) struct WordMap {
    root: u32,
    word_count: u32,
}

struct CacheCodec;

#[derive(Clone, Copy)]
struct Entry {
    key: u64,
    value: u32,
    words: u32,
}

impl ImportCache {
    pub(super) const fn new() -> Self {
        Self {
            root: 0,
            source_memberships: 0,
            translated_memberships: 0,
        }
    }

    pub(super) fn map_feed(
        &mut self,
        store: &mut DraftStore<'_>,
        source_index: u32,
        destination_index: u32,
    ) -> Result<()> {
        insert_new(
            store,
            &mut self.root,
            Entry {
                key: namespaced(FEED_KEY, source_index),
                value: destination_index,
                words: 0,
            },
        )
    }

    pub(super) fn feed(&self, store: &DraftStore<'_>, source_index: u32) -> Result<Option<u32>> {
        Ok(lookup(store, self.root, namespaced(FEED_KEY, source_index))?.map(|entry| entry.value))
    }

    pub(super) fn membership(
        &self,
        store: &DraftStore<'_>,
        source_id: u32,
    ) -> Result<Option<(u32, u32)>> {
        Ok(
            lookup(store, self.root, namespaced(MEMBERSHIP_KEY, source_id))?
                .map(|entry| (entry.value, entry.words)),
        )
    }

    pub(super) fn record_membership(
        &mut self,
        store: &mut DraftStore<'_>,
        source_id: u32,
        destination_id: u32,
        destination_words: u32,
    ) -> Result<()> {
        insert_new(
            store,
            &mut self.root,
            Entry {
                key: namespaced(MEMBERSHIP_KEY, source_id),
                value: destination_id,
                words: destination_words,
            },
        )?;
        self.source_memberships =
            checked_increment(self.source_memberships, "source distinct membership count")?;

        let translated_key = namespaced(TRANSLATED_KEY, destination_id);
        if lookup(store, self.root, translated_key)?.is_none() {
            insert_new(
                store,
                &mut self.root,
                Entry {
                    key: translated_key,
                    value: 0,
                    words: 0,
                },
            )?;
            self.translated_memberships =
                checked_increment(self.translated_memberships, "translated membership count")?;
        }
        Ok(())
    }

    pub(super) const fn source_memberships(&self) -> u64 {
        self.source_memberships
    }

    pub(super) const fn translated_memberships(&self) -> u64 {
        self.translated_memberships
    }

    pub(super) fn release<F>(
        &mut self,
        store: &mut DraftStore<'_>,
        checkpoint: &mut F,
    ) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        fixed_tree::discard_private_tree::<CacheCodec, _, _>(store, self.root, checkpoint)?;
        self.root = 0;
        Ok(())
    }
}

impl WordMap {
    pub(super) const fn new() -> Self {
        Self {
            root: 0,
            word_count: 0,
        }
    }

    pub(super) fn set_bit(
        &mut self,
        store: &mut DraftStore<'_>,
        destination_index: u32,
    ) -> Result<()> {
        let word_index = destination_index / 64;
        let current = lookup(store, self.root, u64::from(word_index))?
            .map(Entry::joined)
            .unwrap_or(0);
        let word = current | (1u64 << (destination_index % 64));
        insert(
            store,
            &mut self.root,
            Entry::split(u64::from(word_index), word),
        )?;
        self.word_count = self.word_count.max(
            word_index
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("translated membership length"))?,
        );
        Ok(())
    }

    pub(super) const fn word_count(&self) -> u32 {
        self.word_count
    }

    pub(super) fn release<F>(
        &mut self,
        store: &mut DraftStore<'_>,
        checkpoint: &mut F,
    ) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        fixed_tree::discard_private_tree::<CacheCodec, _, _>(store, self.root, checkpoint)?;
        self.root = 0;
        Ok(())
    }
}

impl<S: Store> Words<S> for WordMap {
    fn word_count(&self) -> u32 {
        self.word_count
    }

    fn read_words(&self, store: &S, start: u32, output: &mut [u64]) -> Result<()> {
        output.fill(0);
        for (offset, word) in output.iter_mut().enumerate() {
            let index = start
                .checked_add(offset as u32)
                .ok_or(Error::ArithmeticOverflow(
                    "translated membership word index",
                ))?;
            if index >= self.word_count {
                break;
            }
            if let Some(entry) = lookup(store, self.root, u64::from(index))? {
                *word = entry.joined();
            }
        }
        Ok(())
    }
}

impl Codec for CacheCodec {
    type Key = u64;

    const BRANCH_TYPE: u8 = CACHE_BRANCH;
    const LEAF_TYPE: u8 = CACHE_LEAF;
    const AUX: u32 = CACHE_AUX;
    const KEY_SIZE: usize = 8;
    const LEAF_SIZE: usize = 16;

    fn read_key<S: ByteSource>(cell: S, _level: u16) -> Result<Self::Key> {
        Ok(u64_le(cell, 0))
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        put_u64(output, 0, key);
    }

    fn validate_leaf<S: ByteSource>(cell: S) -> Result<()> {
        Entry::decode(cell).map(|_| ())
    }
}

impl Entry {
    fn split(key: u64, value: u64) -> Self {
        Self {
            key,
            value: value as u32,
            words: (value >> 32) as u32,
        }
    }

    fn joined(self) -> u64 {
        u64::from(self.value) | (u64::from(self.words) << 32)
    }

    fn decode<S: ByteSource>(input: S) -> Result<Self> {
        if input.len() != 16 {
            return Err(Error::Corrupt("import cache record length is invalid"));
        }
        Ok(Self {
            key: u64_le(input, 0),
            value: u32_le(input, 8),
            words: u32_le(input, 12),
        })
    }

    fn encode(self) -> [u8; 16] {
        let mut output = [0; 16];
        put_u64(&mut output, 0, self.key);
        put_u32(&mut output, 8, self.value);
        put_u32(&mut output, 12, self.words);
        output
    }
}

fn namespaced(kind: u64, value: u32) -> u64 {
    (kind << 32) | u64::from(value)
}

fn lookup<S: Store>(store: &S, root: u32, key: u64) -> Result<Option<Entry>> {
    let Some(found) = fixed_tree::at_or_after::<CacheCodec, S>(store, root, key)? else {
        return Ok(None);
    };
    let entry = Entry::decode(found.as_slice())?;
    Ok((entry.key == key).then_some(entry))
}

fn insert_new<S: RetiringStore>(store: &mut S, root: &mut u32, entry: Entry) -> Result<()> {
    if lookup(store, *root, entry.key)?.is_some() {
        return Err(Error::Corrupt("duplicate import cache key"));
    }
    insert(store, root, entry)
}

fn insert<S: RetiringStore>(store: &mut S, root: &mut u32, entry: Entry) -> Result<()> {
    let mut retired = RetiredPages::new();
    fixed_tree::insert::<CacheCodec, S>(store, root, &entry.encode(), &mut retired)?;
    store.retire_pages(retired.as_slice())
}

fn checked_increment(value: u64, label: &'static str) -> Result<u64> {
    value.checked_add(1).ok_or(Error::ArithmeticOverflow(label))
}
