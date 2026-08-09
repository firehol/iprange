//! Mapped operation-private state for name-based membership import.

use crate::cancellation::CancellationToken;
use crate::contract::{u32_le, u64_le};
use crate::error::{Error, Result};
use crate::feed::FeedEntry;
use crate::fixed_tree::{self, Codec, RetiringStore, Store};
use crate::mapping::ByteSource;
use crate::membership_dictionary::{Interned, Words};
use crate::reader_core::MembershipToken;
use crate::slotted_page::{put_u32, put_u64};

use super::{DraftStore, TranslatedMembership};

const CACHE_BRANCH: u8 = 240;
const CACHE_LEAF: u8 = 241;
const CACHE_AUX: u32 = 0x494d_5043;
const FEED_KEY: u64 = 1;
const MEMBERSHIP_KEY: u64 = 2;
const TRANSLATED_KEY: u64 = 3;
const ENTRY_KEY_OFFSET: usize = 0;
const ENTRY_VALUE_OFFSET: usize = 8;
const ENTRY_WORDS_OFFSET: usize = 12;
const ENTRY_SIZE: usize = 16;

pub(crate) struct ImportCache {
    root: u32,
    source_memberships: u64,
    translated_memberships: u64,
    last_membership: Option<(MembershipToken, TranslatedMembership)>,
}

pub(crate) struct ImportWords {
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
    pub(crate) const fn new() -> Self {
        Self {
            root: 0,
            source_memberships: 0,
            translated_memberships: 0,
            last_membership: None,
        }
    }

    pub(crate) fn map_feed(
        &mut self,
        store: &mut DraftStore<'_>,
        source: FeedEntry,
        destination: FeedEntry,
    ) -> Result<()> {
        insert_new(
            store,
            &mut self.root,
            Entry {
                key: namespaced(FEED_KEY, source.index),
                value: destination.index,
                words: 0,
            },
        )
    }

    pub(crate) fn membership(
        &mut self,
        store: &DraftStore<'_>,
        source: MembershipToken,
    ) -> Result<Option<TranslatedMembership>> {
        if let Some(translated) = self.last_translation(source) {
            return Ok(Some(translated));
        }
        let translated = lookup(
            store,
            self.root,
            namespaced(MEMBERSHIP_KEY, source.cache_key()),
        )?
        .map(|entry| TranslatedMembership::new(entry.value, entry.words));
        if let Some(translated) = translated {
            self.last_membership = Some((source, translated));
        }
        Ok(translated)
    }

    pub(crate) fn last_translation(&self, source: MembershipToken) -> Option<TranslatedMembership> {
        self.last_membership
            .filter(|(cached, _)| *cached == source)
            .map(|(_, translated)| translated)
    }

    pub(crate) fn finish_membership(
        &mut self,
        store: &mut DraftStore<'_>,
        source: MembershipToken,
        words: &mut ImportWords,
        cancellation: &CancellationToken,
    ) -> Result<TranslatedMembership> {
        let destination = words.intern_and_release(store, &mut || cancellation.check())?;
        self.record_membership(store, source, destination)
    }

    fn record_membership(
        &mut self,
        store: &mut DraftStore<'_>,
        source: MembershipToken,
        destination: Interned,
    ) -> Result<TranslatedMembership> {
        let destination_id = destination.id;
        let destination_words = destination.word_count;
        insert_new(
            store,
            &mut self.root,
            Entry {
                key: namespaced(MEMBERSHIP_KEY, source.cache_key()),
                value: destination_id,
                words: destination_words,
            },
        )?;
        self.source_memberships =
            checked_increment(self.source_memberships, "source distinct membership count")?;
        let translated = TranslatedMembership::new(destination_id, destination_words);
        self.last_membership = Some((source, translated));

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
        Ok(translated)
    }

    pub(crate) fn map_word_batch(
        &self,
        store: &mut DraftStore<'_>,
        words: &mut ImportWords,
        start: u32,
        source_words: &[u64],
        cancellation: &CancellationToken,
    ) -> Result<bool> {
        for (offset, &source_word) in source_words.iter().enumerate() {
            let word_index = start
                .checked_add(offset as u32)
                .ok_or(Error::ArithmeticOverflow("source membership word index"))?;
            if self.map_source_word(store, words, word_index, source_word, cancellation)? {
                return Ok(true);
            }
        }
        Ok(false)
    }

    fn map_source_word(
        &self,
        store: &mut DraftStore<'_>,
        words: &mut ImportWords,
        word_index: u32,
        mut source_word: u64,
        cancellation: &CancellationToken,
    ) -> Result<bool> {
        while source_word != 0 {
            cancellation.check()?;
            let bit = source_word.trailing_zeros();
            let source_index = word_index
                .checked_mul(64)
                .and_then(|base| base.checked_add(bit))
                .ok_or(Error::ArithmeticOverflow("source feed index"))?;
            let Some(destination) = lookup(store, self.root, namespaced(FEED_KEY, source_index))?
            else {
                return Ok(true);
            };
            words.set_bit(store, destination.value)?;
            source_word &= source_word - 1;
        }
        Ok(false)
    }

    pub(crate) const fn source_memberships(&self) -> u64 {
        self.source_memberships
    }

    pub(crate) const fn translated_memberships(&self) -> u64 {
        self.translated_memberships
    }

    pub(crate) fn release(
        &mut self,
        store: &mut DraftStore<'_>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        fixed_tree::discard_private_tree::<CacheCodec, _, _>(store, self.root, &mut || {
            cancellation.check()
        })?;
        self.root = 0;
        self.last_membership = None;
        Ok(())
    }
}

impl ImportWords {
    pub(crate) const fn new() -> Self {
        Self {
            root: 0,
            word_count: 0,
        }
    }

    pub(crate) const fn is_empty(&self) -> bool {
        self.word_count == 0
    }

    fn set_bit(&mut self, store: &mut DraftStore<'_>, destination_index: u32) -> Result<()> {
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

    fn release<F>(&mut self, store: &mut DraftStore<'_>, checkpoint: &mut F) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        fixed_tree::discard_private_tree::<CacheCodec, _, _>(store, self.root, checkpoint)?;
        self.root = 0;
        Ok(())
    }

    fn intern_and_release<F>(
        &mut self,
        store: &mut DraftStore<'_>,
        checkpoint: &mut F,
    ) -> Result<Interned>
    where
        F: FnMut() -> Result<()>,
    {
        let interned = store.intern_membership(self)?;
        self.release(store, checkpoint)?;
        Ok(interned)
    }
}

impl Words<DraftStore<'_>> for ImportWords {
    fn word_count(&self) -> u32 {
        self.word_count
    }

    fn read_words(&self, store: &DraftStore<'_>, start: u32, output: &mut [u64]) -> Result<()> {
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
    type Leaf = Entry;

    const BRANCH_TYPE: u8 = CACHE_BRANCH;
    const LEAF_TYPE: u8 = CACHE_LEAF;
    const AUX: u32 = CACHE_AUX;
    const KEY_SIZE: usize = ENTRY_VALUE_OFFSET;
    const LEAF_SIZE: usize = ENTRY_SIZE;

    fn read_key<S: ByteSource>(cell: S, _level: u16) -> Result<Self::Key> {
        Ok(u64_le(cell, ENTRY_KEY_OFFSET))
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        Entry::decode(cell)
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        put_u64(output, ENTRY_KEY_OFFSET, key);
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
        if input.len() != ENTRY_SIZE {
            return Err(Error::Corrupt("import cache record length is invalid"));
        }
        Ok(Self {
            key: u64_le(input, ENTRY_KEY_OFFSET),
            value: u32_le(input, ENTRY_VALUE_OFFSET),
            words: u32_le(input, ENTRY_WORDS_OFFSET),
        })
    }

    fn encode(self) -> [u8; ENTRY_SIZE] {
        let mut output = [0; ENTRY_SIZE];
        put_u64(&mut output, ENTRY_KEY_OFFSET, self.key);
        put_u32(&mut output, ENTRY_VALUE_OFFSET, self.value);
        put_u32(&mut output, ENTRY_WORDS_OFFSET, self.words);
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
    Ok((found.key == key).then_some(found))
}

fn insert_new<S: RetiringStore>(store: &mut S, root: &mut u32, entry: Entry) -> Result<()> {
    if lookup(store, *root, entry.key)?.is_some() {
        return Err(Error::Corrupt("duplicate import cache key"));
    }
    insert(store, root, entry)
}

fn insert<S: RetiringStore>(store: &mut S, root: &mut u32, entry: Entry) -> Result<()> {
    fixed_tree::insert_retiring::<CacheCodec, S>(store, root, &entry.encode()).map(drop)
}

fn checked_increment(value: u64, label: &'static str) -> Result<u64> {
    value.checked_add(1).ok_or(Error::ArithmeticOverflow(label))
}
