//! Optional bounded cache for pages owned or revisited by one draft.

use crate::contract::PAGE_SIZE;

const MAX_CACHE_BYTES: u64 = 4 * 1024 * 1024;

#[derive(Debug)]
pub(super) struct PageCache {
    slots: Vec<Slot>,
}

#[derive(Debug)]
struct Slot {
    page_number: u32,
    valid: bool,
    bytes: [u8; PAGE_SIZE],
}

impl Slot {
    fn empty() -> Self {
        Self {
            page_number: 0,
            valid: false,
            bytes: [0; PAGE_SIZE],
        }
    }
}

impl PageCache {
    pub(super) fn new(heap_budget: u64) -> Self {
        let bytes = heap_budget.min(MAX_CACHE_BYTES);
        let count = usize::try_from(bytes / std::mem::size_of::<Slot>() as u64).unwrap_or(0);
        let mut slots = Vec::new();
        if count == 0 || slots.try_reserve_exact(count).is_err() {
            return Self { slots };
        }
        slots.resize_with(count, Slot::empty);
        Self { slots }
    }

    pub(super) fn read(&self, page_number: u32, output: &mut [u8; PAGE_SIZE]) -> bool {
        let Some(slot) = self.slot(page_number) else {
            return false;
        };
        if !slot.valid || slot.page_number != page_number {
            return false;
        }
        output.copy_from_slice(&slot.bytes);
        true
    }

    pub(super) fn store(&mut self, page_number: u32, bytes: &[u8; PAGE_SIZE]) {
        let Some(index) = self.index(page_number) else {
            return;
        };
        self.slots[index].page_number = page_number;
        self.slots[index].valid = true;
        self.slots[index].bytes.copy_from_slice(bytes);
    }

    fn slot(&self, page_number: u32) -> Option<&Slot> {
        self.index(page_number).map(|index| &self.slots[index])
    }

    fn index(&self, page_number: u32) -> Option<usize> {
        (!self.slots.is_empty()).then(|| page_number as usize % self.slots.len())
    }

    #[cfg(test)]
    fn len(&self) -> usize {
        self.slots.len()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn zero_budget_disables_cache_without_changing_calls() {
        let mut cache = PageCache::new(0);
        let page = [7; PAGE_SIZE];
        cache.store(10, &page);
        assert!(!cache.read(10, &mut [0; PAGE_SIZE]));
        assert_eq!(cache.len(), 0);
    }

    #[test]
    fn newest_page_bytes_replace_the_same_direct_slot() {
        let mut cache = PageCache::new(2 * std::mem::size_of::<Slot>() as u64);
        let first = [1; PAGE_SIZE];
        let second = [2; PAGE_SIZE];
        let colliding = 5 + cache.len() as u32;
        cache.store(5, &first);
        let mut output = [0; PAGE_SIZE];
        assert!(cache.read(5, &mut output));
        assert_eq!(output, first);
        cache.store(colliding, &second);
        assert!(!cache.read(5, &mut output));
        assert!(cache.read(colliding, &mut output));
        assert_eq!(output, second);
    }

    #[test]
    fn cache_capacity_is_capped_and_charged_to_the_heap_budget() {
        let slot = std::mem::size_of::<Slot>() as u64;
        assert_eq!(PageCache::new(slot - 1).len(), 0);
        assert_eq!(PageCache::new(slot).len(), 1);
        assert!(
            (PageCache::new(u64::MAX).len() * std::mem::size_of::<Slot>()) as u64
                <= MAX_CACHE_BYTES
        );
    }
}
