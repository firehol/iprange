//! One authoritative selected-membership decoder for scans and joins.

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::membership_view::MembershipView;
use crate::reader_core::{GenerationReader, MembershipToken};

use super::cache::SequenceCache;
use crate::heap::Heap;

use super::scope::ScopeData;

const WORD_BATCH: usize = 64;

pub(super) struct Scratch {
    pub(super) present: Vec<u32>,
    pub(super) flags: Vec<u8>,
    membership: Option<MembershipToken>,
    cache: SequenceCache,
}

impl Scratch {
    pub(super) fn new(feeds: usize, heap: &mut Heap) -> Result<Self> {
        Ok(Self {
            present: heap.vector(feeds, "membership aggregation heap")?,
            flags: heap.filled(feeds, 0u8, "membership aggregation heap")?,
            membership: None,
            cache: SequenceCache::empty(),
        })
    }

    pub(super) fn enable_cache(&mut self, heap: &mut Heap, max_bytes: u64) -> Result<()> {
        self.cache.enable(heap, max_bytes)
    }

    pub(super) fn clear(&mut self, cancellation: &CancellationToken) -> Result<()> {
        self.membership = None;
        self.clear_values(cancellation)
    }

    pub(super) fn load(
        &mut self,
        reader: GenerationReader<'_>,
        membership: MembershipToken,
        scope: &ScopeData,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        if self.membership == Some(membership) {
            return Ok(());
        }
        self.clear(cancellation)?;
        if let Some(cached) = self.cache.keyed(membership.cache_key()) {
            let (present, flags) = (&mut self.present, &mut self.flags);
            for (work, &position) in cached.iter().enumerate() {
                if work & 4095 == 4095 {
                    cancellation.check()?;
                }
                flags[position as usize] = 1;
                present.push(position);
            }
            self.membership = Some(membership);
            crate::work::membership_decode_cache_hit(1);
            return Ok(());
        }
        let view = reader.membership(membership)?;
        self.decode(&view, scope, cancellation)?;
        self.cache
            .insert_keyed(membership.cache_key(), &self.present)?;
        self.membership = Some(membership);
        Ok(())
    }

    fn clear_values(&mut self, cancellation: &CancellationToken) -> Result<()> {
        for (work, &position) in self.present.iter().enumerate() {
            if work & 4095 == 4095 {
                cancellation.check()?;
            }
            self.flags[position as usize] = 0;
        }
        self.present.clear();
        Ok(())
    }

    pub(super) fn decode(
        &mut self,
        view: &MembershipView<'_>,
        scope: &ScopeData,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        crate::work::membership_decode(1);
        let word_count = view.word_count()?;
        if scope.selected_word_count.saturating_mul(4) < word_count as usize {
            self.decode_selected(view, scope, word_count, cancellation)
        } else {
            self.decode_all(view, scope, word_count, cancellation)
        }
    }

    fn decode_selected(
        &mut self,
        view: &MembershipView<'_>,
        scope: &ScopeData,
        word_count: u32,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let mut position = 0usize;
        while position < scope.entries.len() {
            let word_index = scope.entries[position].index / 64;
            if word_index >= word_count {
                break;
            }
            let word = view
                .word(word_index)?
                .ok_or_else(|| Error::Corrupt("membership word disappeared"))?;
            crate::work::membership_word_read(1);
            while position < scope.entries.len() && scope.entries[position].index / 64 == word_index
            {
                if position & 4095 == 4095 {
                    cancellation.check()?;
                }
                let entry = scope.entries[position];
                if word & (1u64 << (entry.index % 64)) != 0 {
                    self.push(position)?;
                }
                position += 1;
            }
        }
        Ok(())
    }

    fn decode_all(
        &mut self,
        view: &MembershipView<'_>,
        scope: &ScopeData,
        word_count: u32,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let mut words = [0u64; WORD_BATCH];
        let mut start = 0u32;
        while start < word_count {
            if start != 0 {
                cancellation.check()?;
            }
            let expected = (word_count - start).min(WORD_BATCH as u32) as usize;
            let read = view.read_words(start, &mut words[..expected])?;
            if read != expected {
                return Err(Error::Corrupt("membership word read ended early"));
            }
            crate::work::membership_word_read(read as u64);
            for (offset, mut word) in words[..read].iter().copied().enumerate() {
                let word_index = start
                    .checked_add(offset as u32)
                    .ok_or_else(|| Error::ArithmeticOverflow("membership word index"))?;
                while word != 0 {
                    let bit = word.trailing_zeros();
                    let feed_index = word_index
                        .checked_mul(64)
                        .and_then(|base| base.checked_add(bit))
                        .ok_or_else(|| Error::ArithmeticOverflow("feed index"))?;
                    if let Some(position) = scope.position(feed_index) {
                        self.push(position)?;
                    } else if scope.all_catalog {
                        return Err(Error::Corrupt("membership names an inactive feed"));
                    }
                    word &= word - 1;
                }
            }
            start = start
                .checked_add(read as u32)
                .ok_or_else(|| Error::ArithmeticOverflow("membership word index"))?;
        }
        Ok(())
    }

    fn push(&mut self, position: usize) -> Result<()> {
        let position = u32::try_from(position)
            .map_err(|_| Error::BudgetExceeded("membership scope exceeds u32"))?;
        if self.flags[position as usize] != 0 {
            return Err(Error::Corrupt("membership feed bit was decoded twice"));
        }
        self.flags[position as usize] = 1;
        self.present.push(position);
        Ok(())
    }
}
