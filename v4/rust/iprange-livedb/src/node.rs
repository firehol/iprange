//! Zero-copy views over branch and leaf page bodies.
//!
//! The reader and writer share these. They do offset arithmetic only — no bounds
//! validation beyond slice indexing guarantees. All accessors are zero-copy and
//! zero-alloc. Record size is now a compile-time constant (`2·K::WIDTH + 4`).

use core::marker::PhantomData;

use crate::key::IpKey;
use crate::record::{record_size, RecordRef};
use crate::spec::PAGE_HEADER_SIZE;
use crate::wire::u32_le;

/// A leaf page: `count` fixed-size records after the 16-byte header.
#[derive(Clone, Copy, Debug)]
pub struct LeafView<'a, K: IpKey> {
    page: &'a [u8],
    count: usize,
    _k: PhantomData<K>,
}

impl<'a, K: IpKey> LeafView<'a, K> {
    #[inline]
    pub fn new(page: &'a [u8], count: usize) -> Self {
        LeafView {
            page,
            count,
            _k: PhantomData,
        }
    }

    #[inline]
    pub fn len(&self) -> usize {
        self.count
    }

    #[inline]
    pub fn is_empty(&self) -> bool {
        self.count == 0
    }

    /// The `i`-th record (`i < len`). Zero-copy.
    #[inline]
    pub fn record(&self, i: usize) -> RecordRef<'a, K> {
        let rs = record_size::<K>();
        let off = PAGE_HEADER_SIZE + i * rs;
        RecordRef::new(&self.page[off..off + rs])
    }

    /// Byte length of the populated body (for the tail-zero check).
    #[inline]
    pub fn body_len(&self) -> usize {
        self.count * record_size::<K>()
    }
}

/// A branch page: `child_pgno[0]`, then `sep_count` × (`sep_key`, `child_pgno`).
#[derive(Clone, Copy, Debug)]
pub struct BranchView<'a, K: IpKey> {
    page: &'a [u8],
    sep_count: usize,
    _k: PhantomData<K>,
}

impl<'a, K: IpKey> BranchView<'a, K> {
    #[inline]
    pub fn new(page: &'a [u8], sep_count: usize) -> Self {
        BranchView {
            page,
            sep_count,
            _k: PhantomData,
        }
    }

    #[inline]
    pub fn sep_count(&self) -> usize {
        self.sep_count
    }

    #[inline]
    pub fn child_count(&self) -> usize {
        self.sep_count + 1
    }

    #[inline]
    pub fn sep(&self, i: usize) -> K {
        let off = PAGE_HEADER_SIZE + 4 + i * (K::WIDTH + 4);
        K::read_le(&self.page[off..off + K::WIDTH])
    }

    #[inline]
    pub fn child(&self, j: usize) -> u32 {
        let off = if j == 0 {
            PAGE_HEADER_SIZE
        } else {
            PAGE_HEADER_SIZE + 4 + (j - 1) * (K::WIDTH + 4) + K::WIDTH
        };
        u32_le(self.page, off)
    }

    #[inline]
    pub fn body_len(&self) -> usize {
        4 + self.sep_count * (K::WIDTH + 4)
    }
}
