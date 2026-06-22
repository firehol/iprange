//! Zero-copy views over branch (§5.2) and leaf (§5.3) page bodies.
//!
//! The reader and writer share these. They do **offset arithmetic only** — no bounds
//! validation (that is the reader's §9 walk) beyond what slice indexing guarantees.
//! All accessors are zero-copy and zero-alloc.

use core::marker::PhantomData;

use crate::key::IpKey;
use crate::record::RecordRef;
use crate::spec::PAGE_HEADER_SIZE;
use crate::wire::u32_le;

/// A leaf page (§5.3): `count` records of `record_size` bytes after the 16-byte header.
#[derive(Clone, Copy, Debug)]
pub struct LeafView<'a, K: IpKey> {
    page: &'a [u8],
    count: usize,
    record_size: usize,
    _k: PhantomData<K>,
}

impl<'a, K: IpKey> LeafView<'a, K> {
    /// Wrap a leaf page with a known record count and (runtime) `record_size`.
    #[inline]
    pub fn new(page: &'a [u8], count: usize, record_size: usize) -> Self {
        LeafView {
            page,
            count,
            record_size,
            _k: PhantomData,
        }
    }

    /// Number of records.
    #[inline]
    pub fn len(&self) -> usize {
        self.count
    }

    /// Whether the leaf holds no records (only a degenerate/unreachable leaf, §5.3).
    #[inline]
    pub fn is_empty(&self) -> bool {
        self.count == 0
    }

    /// The `i`-th record (`i < len`). Zero-copy; `scope` is borrowed (D11).
    #[inline]
    pub fn record(&self, i: usize) -> RecordRef<'a, K> {
        let off = PAGE_HEADER_SIZE + i * self.record_size;
        RecordRef::new(&self.page[off..off + self.record_size])
    }

    /// Byte length of the populated body (for the §9 tail-zero check).
    #[inline]
    pub fn body_len(&self) -> usize {
        self.count * self.record_size
    }
}

/// A branch page (§5.2): `child_pgno[0]`, then `sep_count` × (`sep_key`, `child_pgno`)
/// — `s` separators and `s + 1` children. Keys are `K::WIDTH` bytes (compile-time).
#[derive(Clone, Copy, Debug)]
pub struct BranchView<'a, K: IpKey> {
    page: &'a [u8],
    sep_count: usize,
    _k: PhantomData<K>,
}

impl<'a, K: IpKey> BranchView<'a, K> {
    /// Wrap a branch page with a known separator count `s`.
    #[inline]
    pub fn new(page: &'a [u8], sep_count: usize) -> Self {
        BranchView {
            page,
            sep_count,
            _k: PhantomData,
        }
    }

    /// Number of separators `s`.
    #[inline]
    pub fn sep_count(&self) -> usize {
        self.sep_count
    }

    /// Number of children `s + 1`.
    #[inline]
    pub fn child_count(&self) -> usize {
        self.sep_count + 1
    }

    /// Separator `i` (`i < sep_count`): a **routing** key (§5.2).
    #[inline]
    pub fn sep(&self, i: usize) -> K {
        let off = PAGE_HEADER_SIZE + 4 + i * (K::WIDTH + 4);
        K::read_le(&self.page[off..off + K::WIDTH])
    }

    /// Child pgno `j` (`j <= sep_count`). `child[0]` precedes all separators; `child[j]`
    /// (`j >= 1`) follows `sep[j-1]`.
    #[inline]
    pub fn child(&self, j: usize) -> u32 {
        let off = if j == 0 {
            PAGE_HEADER_SIZE
        } else {
            PAGE_HEADER_SIZE + 4 + (j - 1) * (K::WIDTH + 4) + K::WIDTH
        };
        u32_le(self.page, off)
    }

    /// Byte length of the populated body (for the §9 tail-zero check): `4 + s·(W+4)`.
    #[inline]
    pub fn body_len(&self) -> usize {
        4 + self.sep_count * (K::WIDTH + 4)
    }
}
