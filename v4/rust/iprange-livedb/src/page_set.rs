//! A page bitset for tracking dirty/private pages during a transaction.
//! Pre-allocated at Writer::open() time — Rule 1 compliant (fixed allocation).
//!
//! Size: total_pages / 8 bytes. For 1M pages = 128KB. For 100M pages = 12.5MB.
//! Resized (amortized) when the file grows beyond the initial allocation.

use alloc::vec::Vec;

pub struct PageSet {
    bits: Vec<u64>,
    capacity: usize, // max pages addressable
}

impl PageSet {
    /// Allocate a bitset covering `capacity` pages.
    pub fn new(capacity: usize) -> Self {
        let words = (capacity + 63) / 64;
        PageSet {
            bits: vec![0u64; words],
            capacity,
        }
    }

    /// Ensure the bitset covers at least `min_capacity` pages.
    pub fn ensure_capacity(&mut self, min_capacity: usize) {
        if min_capacity <= self.capacity {
            return;
        }
        let new_cap = min_capacity.max(self.capacity * 2);
        let new_words = (new_cap + 63) / 64;
        self.bits.resize(new_words, 0u64);
        self.capacity = new_cap;
    }

    #[inline]
    pub fn contains(&self, pgno: u32) -> bool {
        let p = pgno as usize;
        if p >= self.capacity {
            return false;
        }
        let word = p / 64;
        let bit = p % 64;
        (self.bits[word] >> bit) & 1 != 0
    }

    #[inline]
    pub fn insert(&mut self, pgno: u32) {
        let p = pgno as usize;
        if p >= self.capacity {
            self.ensure_capacity(p + 1);
        }
        let word = p / 64;
        let bit = p % 64;
        self.bits[word] |= 1u64 << bit;
    }

    #[inline]
    pub fn remove(&mut self, pgno: u32) {
        let p = pgno as usize;
        if p >= self.capacity {
            return;
        }
        let word = p / 64;
        let bit = p % 64;
        self.bits[word] &= !(1u64 << bit);
    }

    pub fn clear(&mut self) {
        for w in &mut self.bits {
            *w = 0;
        }
    }

    /// Iterate over all set page numbers.
    pub fn iter(&self) -> impl Iterator<Item = u32> + '_ {
        self.bits.iter().enumerate().flat_map(|(word_idx, &word)| {
            (0..64).filter_map(move |bit| {
                if (word >> bit) & 1 != 0 {
                    Some((word_idx * 64 + bit) as u32)
                } else {
                    None
                }
            })
        })
    }

    pub fn len(&self) -> usize {
        self.bits.iter().map(|&w| w.count_ones() as usize).sum()
    }

    pub fn is_empty(&self) -> bool {
        self.bits.iter().all(|&w| w == 0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn basic_ops() {
        let mut s = PageSet::new(128);
        s.insert(5);
        s.insert(100);
        assert!(s.contains(5));
        assert!(s.contains(100));
        assert!(!s.contains(6));
        assert_eq!(s.len(), 2);
        s.remove(5);
        assert!(!s.contains(5));
        assert_eq!(s.len(), 1);
    }

    #[test]
    fn growth() {
        let mut s = PageSet::new(64);
        s.insert(100); // beyond initial capacity → grows
        assert!(s.contains(100));
        assert_eq!(s.len(), 1);
    }

    #[test]
    fn clear() {
        let mut s = PageSet::new(128);
        s.insert(5);
        s.insert(10);
        s.clear();
        assert!(s.is_empty());
    }
}
