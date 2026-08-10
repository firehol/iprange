//! Bounded catalog resolution and feed-index mapping for membership queries.

use std::mem::size_of;

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::heap::Heap;
use crate::reader_core::GenerationReader;

pub(crate) struct ScopeData {
    pub(super) entries: Vec<FeedEntry>,
    index: IndexMap,
    pub(super) selected_word_count: usize,
    pub(super) all_catalog: bool,
    pub(super) max_heap_bytes: u64,
    pub(super) heap_used: u64,
}

enum IndexMap {
    Empty,
    Dense(Vec<u32>),
    Sparse { slots: Vec<Slot>, mask: usize },
}

#[derive(Clone, Copy, Default)]
struct Slot {
    key: u32,
    value: u32,
}

impl ScopeData {
    pub(super) fn all(
        reader: GenerationReader<'_>,
        active_feed_count: u64,
        max_heap_bytes: u64,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        cancellation.check()?;
        let count = usize::try_from(active_feed_count)
            .map_err(|_| Error::BudgetExceeded("membership scope heap"))?;
        let mut heap = Heap::new(max_heap_bytes);
        let mut entries = heap.vector(count, "membership scope heap")?;
        let mut cursor = reader.feed_cursor()?;
        while let Some(entry) = cursor.next_feed()? {
            if entries.len() & 4095 == 4095 {
                cancellation.check()?;
            }
            entries.push(entry);
        }
        if entries.len() != count {
            return Err(Error::Corrupt("feed catalog count changed during scope"));
        }
        Self::finish(entries, true, max_heap_bytes, heap, cancellation)
    }

    pub(super) fn named(
        reader: GenerationReader<'_>,
        names: &[FeedName],
        max_heap_bytes: u64,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        Self::named_from(
            reader,
            names.len(),
            names.iter().copied().map(Ok),
            max_heap_bytes,
            cancellation,
        )
    }

    pub(crate) fn named_from<I>(
        reader: GenerationReader<'_>,
        count: usize,
        names: I,
        max_heap_bytes: u64,
        cancellation: &CancellationToken,
    ) -> Result<Self>
    where
        I: IntoIterator<Item = Result<FeedName>>,
    {
        cancellation.check()?;
        if count == 0 {
            return Err(Error::InvalidArgument("membership scope is empty"));
        }
        let mut heap = Heap::new(max_heap_bytes);
        let mut entries = heap.vector(count, "membership scope heap")?;
        for (work, name) in names.into_iter().enumerate() {
            if work & 4095 == 4095 {
                cancellation.check()?;
            }
            let name = name?;
            entries.push(reader.lookup_feed_name(&name)?.ok_or(Error::NameNotFound)?);
        }
        if entries.len() != count {
            return Err(Error::InvalidArgument(
                "membership scope name count disagrees",
            ));
        }
        cancellation.check()?;
        entries.sort_unstable_by_key(|entry| entry.index);
        cancellation.check()?;
        for (work, pair) in entries.windows(2).enumerate() {
            if work & 4095 == 4095 {
                cancellation.check()?;
            }
            if pair[0].index == pair[1].index {
                return Err(Error::InvalidArgument(
                    "membership scope feed names are not unique",
                ));
            }
        }
        Self::finish(entries, false, max_heap_bytes, heap, cancellation)
    }

    fn finish(
        entries: Vec<FeedEntry>,
        all_catalog: bool,
        max_heap_bytes: u64,
        mut heap: Heap,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        let mut selected_word_count = 0usize;
        let mut previous_word = None;
        for (work, entry) in entries.iter().enumerate() {
            if work & 4095 == 4095 {
                cancellation.check()?;
            }
            let word = entry.index / 64;
            if previous_word != Some(word) {
                selected_word_count += 1;
                previous_word = Some(word);
            }
        }
        let index = IndexMap::new(&entries, &mut heap, cancellation)?;
        Ok(Self {
            entries,
            index,
            selected_word_count,
            all_catalog,
            max_heap_bytes,
            heap_used: heap.used(),
        })
    }

    pub(super) fn position(&self, index: u32) -> Option<usize> {
        self.index.get(index)
    }

    pub(super) fn position_name(
        &self,
        reader: GenerationReader<'_>,
        name: &FeedName,
    ) -> Result<usize> {
        let entry = reader.lookup_feed_name(name)?.ok_or(Error::NameNotFound)?;
        self.position(entry.index).ok_or(Error::InvalidArgument(
            "feed is outside the membership scope",
        ))
    }

    pub(super) fn operation_heap(&self) -> Result<Heap> {
        self.operation_heap_reserved(0)
    }

    pub(super) fn operation_heap_reserved(&self, reserved: u64) -> Result<Heap> {
        let remaining = self
            .max_heap_bytes
            .checked_sub(self.heap_used)
            .and_then(|remaining| remaining.checked_sub(reserved))
            .ok_or(Error::BudgetExceeded("membership aggregation heap"))?;
        Ok(Heap::new(remaining))
    }
}

impl IndexMap {
    fn new(
        entries: &[FeedEntry],
        heap: &mut Heap,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        let Some(last) = entries.last() else {
            return Ok(Self::Empty);
        };
        let dense_len = usize::try_from(u64::from(last.index) + 1).ok();
        let sparse_len = entries
            .len()
            .checked_mul(2)
            .and_then(|value| value.checked_next_power_of_two())
            .ok_or(Error::BudgetExceeded("membership scope index heap"))?;
        let dense_bytes = dense_len.and_then(|len| len.checked_mul(size_of::<u32>()));
        let sparse_bytes = sparse_len
            .checked_mul(size_of::<Slot>())
            .ok_or(Error::BudgetExceeded("membership scope index heap"))?;
        if dense_bytes.is_some_and(|bytes| bytes <= sparse_bytes) {
            let len = dense_len.expect("dense byte count requires a length");
            let mut positions = heap.filled(len, 0u32, "membership scope index heap")?;
            for (position, entry) in entries.iter().enumerate() {
                if position & 4095 == 4095 {
                    cancellation.check()?;
                }
                positions[entry.index as usize] = position_value(position)?;
            }
            return Ok(Self::Dense(positions));
        }

        let mut slots = heap.filled(sparse_len, Slot::default(), "membership scope index heap")?;
        let mask = sparse_len - 1;
        for (position, entry) in entries.iter().enumerate() {
            if position & 4095 == 4095 {
                cancellation.check()?;
            }
            let mut slot = hash(entry.index) & mask;
            let mut probes = 0usize;
            loop {
                if probes & 4095 == 4095 {
                    cancellation.check()?;
                }
                if slots[slot].value == 0 {
                    slots[slot] = Slot {
                        key: entry.index,
                        value: position_value(position)?,
                    };
                    break;
                }
                slot = (slot + 1) & mask;
                probes += 1;
            }
        }
        Ok(Self::Sparse { slots, mask })
    }

    fn get(&self, index: u32) -> Option<usize> {
        let value = match self {
            Self::Empty => return None,
            Self::Dense(positions) => *positions.get(index as usize)?,
            Self::Sparse { slots, mask } => {
                let mut slot = hash(index) & mask;
                loop {
                    let entry = slots[slot];
                    if entry.value == 0 {
                        return None;
                    }
                    if entry.key == index {
                        break entry.value;
                    }
                    slot = (slot + 1) & mask;
                }
            }
        };
        usize::try_from(value - 1).ok()
    }
}

fn position_value(position: usize) -> Result<u32> {
    u32::try_from(position)
        .ok()
        .and_then(|position| position.checked_add(1))
        .ok_or(Error::BudgetExceeded("membership scope exceeds u32"))
}

fn hash(index: u32) -> usize {
    index.wrapping_mul(0x9e37_79b1) as usize
}
