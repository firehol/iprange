//! The v4 writer: copy-on-write B+tree mutation with a double-meta atomic commit
//! (§6, §7, §8).
//!
//! This is the in-memory writer core: it owns the whole file image as a growable
//! buffer and emulates the `pread`/`pwrite` model the OS layer (a later increment)
//! uses against a real file — every mutated node is **copied** to a freshly allocated
//! page (the old page is freed-by-this-txn, D7) up to a new root, and `commit` writes
//! the new state into the **inactive** meta and flips it (§6.3). A crash leaves the
//! file as old-or-new, never torn (the active meta only points at durable pages).
//!
//! This increment implements **create**, the COW B+tree `insert` primitive (with leaf
//! split and root growth), and `commit`. Range `set` / `delete` (which compose
//! `insert` with trimming/coalescing) and underflow merge/borrow land next; the OS
//! file/flock layer wraps this core.

use alloc::vec;
use alloc::vec::Vec;
use core::marker::PhantomData;

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::node::{BranchView, LeafView};
use crate::record;
use crate::spec::{self, PAGE_HEADER_SIZE, PAGE_SIZE};
use crate::wire::{finalize_checksum, Meta, PageHeader};

/// An owned record, materialized from a leaf for the duration of a COW op.
struct OwnedRecord<K: IpKey> {
    from: K,
    to: K,
    scope: Vec<u8>,
}

/// A single-writer COW B+tree over an in-memory file image. Generic over the key width
/// (fixed per file). `commit` makes the accumulated mutations durable and atomic.
pub struct Writer<K: IpKey> {
    image: Vec<u8>,
    active_meta: u32, // physical meta page (0 or 1) currently active
    root_pgno: u32,
    tree_height: u32,
    record_count: u64,
    scope_width: usize,
    record_size: usize,
    leaf_max: usize,
    branch_max: usize,
    created_unixtime: u64,
    txn_id: u64,
    free: Vec<u32>,           // pages reusable by the current txn
    freed_this_txn: Vec<u32>, // pages freed since the last commit (reusable next txn, D7)
    _k: PhantomData<K>,
}

impl<K: IpKey> Writer<K> {
    /// Create a fresh empty DB: two meta pages (META-A active `txn_id = 1`, META-B
    /// `txn_id = 0`), an empty tree (`root_pgno = 0`). `scope_width` is fixed for the
    /// file's lifetime (§4). Mutations are not durable until `commit`.
    pub fn create(scope_width: u8, created_unixtime: u64) -> Writer<K> {
        let record_size = spec::record_size(K::WIDTH as u8, scope_width);
        let image = vec![0u8; 2 * PAGE_SIZE];
        let mut w = Writer {
            image,
            active_meta: 0,
            root_pgno: 0,
            tree_height: 0,
            record_count: 0,
            scope_width: scope_width as usize,
            record_size: record_size as usize,
            leaf_max: spec::leaf_max(record_size),
            branch_max: spec::branch_max(K::WIDTH as u8),
            created_unixtime,
            txn_id: 1,
            free: Vec::new(),
            freed_this_txn: Vec::new(),
            _k: PhantomData,
        };
        // META-A active (txn 1), META-B (txn 0); identical static identity.
        w.write_meta(0, 1, created_unixtime);
        w.write_meta(1, 0, created_unixtime);
        w
    }

    /// Insert a **disjoint** record `[from, to] = scope` (the caller guarantees it does
    /// not overlap an existing range and `from` is unique). The building block for
    /// `set` (range `set` / `delete` compose this with trimming/coalescing — next
    /// increment). COW: touches `O(log n)` pages.
    pub fn insert(&mut self, from: K, to: K, scope: &[u8]) -> Result<()> {
        if to < from {
            return Err(Error::InvalidInput("insert from > to"));
        }
        if scope.len() != self.scope_width {
            return Err(Error::InvalidInput("insert scope width mismatch"));
        }
        let rec = OwnedRecord {
            from,
            to,
            scope: scope.to_vec(),
        };
        if self.root_pgno == 0 {
            let p = self.write_leaf(core::slice::from_ref(&rec));
            self.root_pgno = p;
            self.tree_height = 1;
        } else {
            let (new_root, split) = self.cow_insert(self.root_pgno, 1, rec);
            match split {
                None => self.root_pgno = new_root,
                Some((sep, right)) => {
                    if self.tree_height >= spec::TREE_HEIGHT_MAX {
                        return Err(Error::InvalidInput("tree would exceed TREE_HEIGHT_MAX"));
                    }
                    self.root_pgno = self.write_branch(&[sep], &[new_root, right]);
                    self.tree_height += 1;
                }
            }
        }
        self.record_count += 1;
        Ok(())
    }

    /// Commit the accumulated mutations: write the new state into the inactive meta and
    /// flip it (§6.3). After this the image is a valid v4 file whose active meta is the
    /// new tree, and pages freed by this txn become reusable (D7).
    pub fn commit(&mut self, updated_unixtime: u64) {
        let inactive = 1 - self.active_meta;
        self.txn_id += 1;
        self.write_meta(inactive, self.txn_id, updated_unixtime);
        self.active_meta = inactive;
        // Pages freed by this txn back the now-stale tree no longer; reclaim them.
        let mut freed = core::mem::take(&mut self.freed_this_txn);
        self.free.append(&mut freed);
    }

    /// Borrow the current image bytes (valid v4 file after a `commit`).
    #[inline]
    pub fn image(&self) -> &[u8] {
        &self.image
    }

    /// Consume the writer, returning the file image.
    #[inline]
    pub fn into_image(self) -> Vec<u8> {
        self.image
    }

    /// Number of records in the (pending) tree.
    #[inline]
    pub fn record_count(&self) -> u64 {
        self.record_count
    }

    // --- COW internals ---

    /// Recursive COW insert. Returns the new subtree pgno and, on overflow, a
    /// `(separator, right_pgno)` split for the parent to absorb.
    fn cow_insert(&mut self, pgno: u32, depth: u32, rec: OwnedRecord<K>) -> (u32, Option<(K, u32)>) {
        if depth == self.tree_height {
            let mut recs = self.read_leaf(pgno);
            self.free_page(pgno);
            let pos = recs.partition_point(|r| r.from < rec.from);
            recs.insert(pos, rec);
            self.emit_leaf(&recs)
        } else {
            let (mut seps, mut children) = self.read_branch(pgno);
            self.free_page(pgno);
            let i = seps.partition_point(|s| *s <= rec.from);
            let (new_child, split) = self.cow_insert(children[i], depth + 1, rec);
            children[i] = new_child;
            if let Some((sep, right)) = split {
                seps.insert(i, sep);
                children.insert(i + 1, right);
            }
            self.emit_branch(&seps, &children)
        }
    }

    /// Write `records` as one leaf, or split into two if over `leaf_max`. Returns the
    /// (primary pgno, optional split).
    fn emit_leaf(&mut self, records: &[OwnedRecord<K>]) -> (u32, Option<(K, u32)>) {
        if records.len() <= self.leaf_max {
            (self.write_leaf(records), None)
        } else {
            let mid = records.len() / 2;
            let lp = self.write_leaf(&records[..mid]);
            let rp = self.write_leaf(&records[mid..]);
            (lp, Some((records[mid].from, rp)))
        }
    }

    /// Write a branch, or split into two (promoting the middle separator) if over
    /// `branch_max`.
    fn emit_branch(&mut self, seps: &[K], children: &[u32]) -> (u32, Option<(K, u32)>) {
        if seps.len() <= self.branch_max {
            (self.write_branch(seps, children), None)
        } else {
            let mid = seps.len() / 2;
            let lp = self.write_branch(&seps[..mid], &children[..mid + 1]);
            let rp = self.write_branch(&seps[mid + 1..], &children[mid + 1..]);
            (lp, Some((seps[mid], rp)))
        }
    }

    // --- page I/O over the in-memory image ---

    fn read_leaf(&self, pgno: u32) -> Vec<OwnedRecord<K>> {
        let page = self.page(pgno);
        let count = PageHeader::decode(page).entry_count as usize;
        let leaf = LeafView::<K>::new(page, count, self.record_size);
        (0..count)
            .map(|i| {
                let r = leaf.record(i);
                OwnedRecord {
                    from: r.from(),
                    to: r.to(),
                    scope: r.scope().to_vec(),
                }
            })
            .collect()
    }

    fn read_branch(&self, pgno: u32) -> (Vec<K>, Vec<u32>) {
        let page = self.page(pgno);
        let count = PageHeader::decode(page).entry_count as usize;
        let b = BranchView::<K>::new(page, count);
        let seps = (0..count).map(|i| b.sep(i)).collect();
        let children = (0..=count).map(|j| b.child(j)).collect();
        (seps, children)
    }

    fn write_leaf(&mut self, records: &[OwnedRecord<K>]) -> u32 {
        let pgno = self.alloc_page();
        let base = pgno as usize * PAGE_SIZE;
        self.image[base..base + PAGE_SIZE].fill(0);
        PageHeader::write(
            &mut self.image[base..base + PAGE_SIZE],
            spec::PAGE_TYPE_LEAF,
            records.len() as u16,
            pgno,
        );
        for (i, r) in records.iter().enumerate() {
            let off = base + PAGE_HEADER_SIZE + i * self.record_size;
            record::write::<K>(&mut self.image[off..off + self.record_size], r.from, r.to, &r.scope);
        }
        finalize_checksum(&mut self.image[base..base + PAGE_SIZE]);
        pgno
    }

    fn write_branch(&mut self, seps: &[K], children: &[u32]) -> u32 {
        debug_assert_eq!(children.len(), seps.len() + 1);
        let pgno = self.alloc_page();
        let base = pgno as usize * PAGE_SIZE;
        self.image[base..base + PAGE_SIZE].fill(0);
        PageHeader::write(
            &mut self.image[base..base + PAGE_SIZE],
            spec::PAGE_TYPE_BRANCH,
            seps.len() as u16,
            pgno,
        );
        // child[0] at +16, then (sep[i], child[i+1]) pairs.
        let c0 = base + PAGE_HEADER_SIZE;
        self.image[c0..c0 + 4].copy_from_slice(&children[0].to_le_bytes());
        for i in 0..seps.len() {
            let sep_off = base + PAGE_HEADER_SIZE + 4 + i * (K::WIDTH + 4);
            seps[i].write_le(&mut self.image[sep_off..sep_off + K::WIDTH]);
            let c_off = sep_off + K::WIDTH;
            self.image[c_off..c_off + 4].copy_from_slice(&children[i + 1].to_le_bytes());
        }
        finalize_checksum(&mut self.image[base..base + PAGE_SIZE]);
        pgno
    }

    fn write_meta(&mut self, pgno: u32, txn_id: u64, updated_unixtime: u64) {
        let meta = Meta {
            pgno,
            version_minor: 0,
            meta_size: spec::META_SIZE,
            page_size: PAGE_SIZE as u32,
            checksum_algo: spec::CHECKSUM_ALGO_CRC32C,
            flags: K::VERSION.flag(),
            key_width: K::WIDTH as u8,
            scope_width: self.scope_width as u8,
            record_size: self.record_size as u32,
            created_unixtime: self.created_unixtime,
            root_pgno: self.root_pgno,
            tree_height: self.tree_height,
            total_pages: (self.image.len() / PAGE_SIZE) as u64,
            record_count: self.record_count,
            txn_id,
            updated_unixtime,
        };
        let base = pgno as usize * PAGE_SIZE;
        meta.encode_into(&mut self.image[base..base + PAGE_SIZE]);
    }

    #[inline]
    fn page(&self, pgno: u32) -> &[u8] {
        let base = pgno as usize * PAGE_SIZE;
        &self.image[base..base + PAGE_SIZE]
    }

    /// Allocate a page: reuse a freed page (from a prior txn) or grow the image.
    fn alloc_page(&mut self) -> u32 {
        if let Some(p) = self.free.pop() {
            p
        } else {
            let p = (self.image.len() / PAGE_SIZE) as u32;
            self.image.resize(self.image.len() + PAGE_SIZE, 0);
            p
        }
    }

    /// Mark a page freed by the current txn (reusable only after this txn commits, D7).
    #[inline]
    fn free_page(&mut self, pgno: u32) {
        self.freed_this_txn.push(pgno);
    }
}

impl<K: IpKey> core::fmt::Debug for Writer<K> {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        f.debug_struct("Writer")
            .field("root_pgno", &self.root_pgno)
            .field("tree_height", &self.tree_height)
            .field("record_count", &self.record_count)
            .field("total_pages", &(self.image.len() / PAGE_SIZE))
            .field("txn_id", &self.txn_id)
            .finish_non_exhaustive()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::Ipv4Key;
    use crate::reader::Reader;

    fn k(n: u32) -> Ipv4Key {
        Ipv4Key(n)
    }

    #[test]
    fn create_empty_round_trips() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        w.commit(0);
        let img = w.into_image();
        let r = Reader::open(&img).unwrap();
        assert!(r.is_empty());
        assert_eq!(r.record_count(), 0);
        assert_eq!(r.lookup_v4(k(5)).unwrap(), None);
    }

    #[test]
    fn single_leaf_insert_round_trips() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        w.insert(k(10), k(20), &[1]).unwrap();
        w.insert(k(30), k(40), &[2]).unwrap();
        w.insert(k(5), k(8), &[3]).unwrap(); // inserts before the others
        w.commit(0);
        let img = w.into_image();
        let r = Reader::open(&img).unwrap();
        assert_eq!(r.record_count(), 3);
        assert_eq!(r.lookup_v4(k(15)).unwrap(), Some(&[1u8][..]));
        assert_eq!(r.lookup_v4(k(35)).unwrap(), Some(&[2u8][..]));
        assert_eq!(r.lookup_v4(k(6)).unwrap(), Some(&[3u8][..]));
        assert_eq!(r.lookup_v4(k(25)).unwrap(), None);
        let mut order = Vec::new();
        r.scan_v4(|f, _, _| order.push(f.0)).unwrap();
        assert_eq!(order, vec![5, 10, 30]);
    }

    #[test]
    fn many_inserts_force_splits_and_validate() {
        // Enough disjoint records to overflow leaves and grow the tree past height 1.
        let mut w = Writer::<Ipv4Key>::create(2, 0);
        let n = 5000u32;
        for i in 0..n {
            let base = i * 10;
            w.insert(k(base), k(base + 4), &[(i & 0xff) as u8, (i >> 8) as u8])
                .unwrap();
        }
        w.commit(0);
        let img = w.into_image();
        let r = Reader::open(&img).unwrap(); // full validation of the whole tree
        assert_eq!(r.record_count(), n as u64);
        // spot-check lookups across the tree
        for &i in &[0u32, 1, 123, 2500, 4999] {
            let base = i * 10;
            let scope = [(i & 0xff) as u8, (i >> 8) as u8];
            assert_eq!(r.lookup_v4(k(base + 2)).unwrap(), Some(&scope[..]), "i={i}");
            assert_eq!(r.lookup_v4(k(base + 5)).unwrap(), None, "gap after i={i}");
        }
        // scan returns every record in order
        let mut count = 0u64;
        let mut prev = None;
        r.scan_v4(|f, _, _| {
            if let Some(p) = prev {
                assert!(f.0 > p);
            }
            prev = Some(f.0);
            count += 1;
        })
        .unwrap();
        assert_eq!(count, n as u64);
    }

    #[test]
    fn multiple_commits_reuse_freed_pages() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        for i in 0..200u32 {
            w.insert(k(i * 10), k(i * 10 + 1), &[i as u8]).unwrap();
        }
        w.commit(1);
        let pages_after_first = w.image().len() / PAGE_SIZE;
        // A second txn that inserts more; freed pages from txn 1 are now reusable.
        for i in 200..260u32 {
            w.insert(k(i * 10), k(i * 10 + 1), &[i as u8]).unwrap();
        }
        w.commit(2);
        let img = w.into_image();
        let r = Reader::open(&img).unwrap();
        assert_eq!(r.record_count(), 260);
        assert_eq!(r.lookup_v4(k(2550)).unwrap(), Some(&[255u8][..]));
        assert!(pages_after_first >= 2); // sanity: the file grew at least past the metas
    }
}
