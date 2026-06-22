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
//! Implements **create**, range `set` / `delete` (§8) over a COW B+tree (leaf/branch
//! split + root growth on insert; sibling-merge + root-collapse on delete), `scan`,
//! and `commit`. `set` / `delete` compose a disjoint-`insert` primitive with boundary
//! trimming and same-scope coalescing. The OS file/`flock`/`mmap` layer wraps this
//! core (next increment).

use alloc::vec;
use alloc::vec::Vec;
use core::marker::PhantomData;

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::node::{BranchView, LeafView};
use crate::reader::Reader;
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

    /// Open an existing committed image for mutation (§6.2): **fully validate** it with
    /// the reader's §9 checks (the writer also reads untrusted bytes), then derive the
    /// in-memory free set (§7) by walking the reachable tree. The image MUST have no
    /// trailing pages beyond `total_pages` (the OS layer truncates those before calling).
    pub fn open_image(image: Vec<u8>) -> Result<Writer<K>> {
        let meta = {
            let r = Reader::open(&image)?;
            if K::VERSION != r.version() {
                return Err(Error::InvalidInput("writer family mismatch"));
            }
            r.active_meta()
        };
        let record_size = spec::record_size(K::WIDTH as u8, meta.scope_width);
        let mut w = Writer {
            image,
            active_meta: meta.pgno,
            root_pgno: meta.root_pgno,
            tree_height: meta.tree_height,
            record_count: meta.record_count,
            scope_width: meta.scope_width as usize,
            record_size: record_size as usize,
            leaf_max: spec::leaf_max(record_size),
            branch_max: spec::branch_max(K::WIDTH as u8),
            created_unixtime: meta.created_unixtime,
            txn_id: meta.txn_id,
            free: Vec::new(),
            freed_this_txn: Vec::new(),
            _k: PhantomData,
        };
        w.free = w.derive_free_set();
        Ok(w)
    }

    /// `set([from,to]) = scope` unconditionally (§8, D11): clears the range, then
    /// inserts `[from,to,scope]` coalescing with byte-equal-scope adjacent neighbours.
    /// `O(k log n)` (k = records overlapping the range).
    pub fn set(&mut self, from: K, to: K, scope: &[u8]) -> Result<()> {
        if to < from {
            return Err(Error::InvalidInput("set from > to"));
        }
        if scope.len() != self.scope_width {
            return Err(Error::InvalidInput("set scope width mismatch"));
        }
        self.delete_range(from, to);
        let (mut nf, mut nt) = (from, to);
        // Coalesce with a same-scope neighbour ending at from-1.
        if let Some(fm1) = from.checked_dec() {
            if let Some((lf, lt, ls)) = self.lookup_covering(fm1) {
                if lt == fm1 && ls == scope {
                    self.tree_delete(lf);
                    nf = lf;
                }
            }
        }
        // Coalesce with a same-scope neighbour starting at to+1.
        if let Some(tp1) = to.checked_inc() {
            if let Some((rf, rt, rs)) = self.lookup_covering(tp1) {
                if rf == tp1 && rs == scope {
                    self.tree_delete(rf);
                    nt = rt;
                }
            }
        }
        self.insert(nf, nt, scope)
    }

    /// `delete([from,to])`: make the range absent (§8). Splits a straddling record,
    /// trims boundaries, removes records fully inside; a wholly-absent range is a no-op.
    /// `O(k log n)`.
    pub fn delete(&mut self, from: K, to: K) -> Result<()> {
        if to < from {
            return Err(Error::InvalidInput("delete from > to"));
        }
        self.delete_range(from, to);
        Ok(())
    }

    /// In-order scan of the (pending) tree: `f(from, to, scope)` per record. Zero-alloc.
    pub fn scan<F: FnMut(K, K, &[u8])>(&self, mut f: F) {
        if self.root_pgno != 0 {
            self.scan_node(self.root_pgno, 1, &mut f);
        }
    }

    /// Insert a **disjoint** record `[from, to] = scope` (the caller guarantees it does
    /// not overlap an existing range and `from` is unique). The COW building block for
    /// `set` / `delete`. Touches `O(log n)` pages.
    fn insert(&mut self, from: K, to: K, scope: &[u8]) -> Result<()> {
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

    /// Derive the free set (§7): pages in `[2, total_pages)` not reachable from the
    /// root. The image is already validated, so the walk is bounded and safe.
    fn derive_free_set(&self) -> Vec<u32> {
        let total = self.image.len() / PAGE_SIZE;
        let mut used = vec![false; total];
        used[0] = true; // META-A
        used[1] = true; // META-B
        if self.root_pgno != 0 {
            self.mark_reachable(self.root_pgno, 1, &mut used);
        }
        (2..total as u32).filter(|&p| !used[p as usize]).collect()
    }

    fn mark_reachable(&self, pgno: u32, depth: u32, used: &mut [bool]) {
        used[pgno as usize] = true;
        if depth < self.tree_height {
            let page = self.page(pgno);
            let count = PageHeader::decode(page).entry_count as usize;
            let b = BranchView::<K>::new(page, count);
            for j in 0..b.child_count() {
                self.mark_reachable(b.child(j), depth + 1, used);
            }
        }
    }

    // --- range mutation internals ---

    /// Remove everything overlapping `[from, to]`, re-inserting the parts of straddling
    /// records that fall outside (left `[rf, from-1]`, right `[to+1, rt]`).
    fn delete_range(&mut self, from: K, to: K) {
        while let Some((rf, rt, scope)) = self.any_overlap(from, to) {
            self.tree_delete(rf);
            if rf < from {
                let fm1 = from.checked_dec().expect("from > rf >= family_min");
                let _ = self.insert(rf, fm1, &scope);
            }
            if rt > to {
                let tp1 = to.checked_inc().expect("to < rt <= family_max");
                let _ = self.insert(tp1, rt, &scope);
            }
        }
    }

    /// Any record overlapping `[from, to]` (or `None`). The covering record of `from`
    /// (single-leaf) else the successor of `from` if it starts within the range.
    fn any_overlap(&self, from: K, to: K) -> Option<(K, K, Vec<u8>)> {
        if let Some(r) = self.lookup_covering(from) {
            return Some(r);
        }
        if let Some(r) = self.lookup_ge(from) {
            if r.0 <= to {
                return Some(r);
            }
        }
        None
    }

    /// Delete the record whose `from == key` (rebalancing on underflow; collapsing the
    /// root). Returns whether a record was removed.
    fn tree_delete(&mut self, key: K) -> bool {
        if self.root_pgno == 0 || !self.contains_from(key) {
            return false;
        }
        if self.tree_height == 1 {
            let mut recs = self.read_leaf(self.root_pgno);
            self.free_page(self.root_pgno);
            let pos = recs.partition_point(|r| r.from < key);
            recs.remove(pos);
            if recs.is_empty() {
                self.root_pgno = 0;
                self.tree_height = 0;
            } else {
                self.root_pgno = self.write_leaf(&recs);
            }
        } else {
            let (new_root, _uf) = self.cow_delete(self.root_pgno, 1, key);
            self.root_pgno = new_root;
            // Collapse a root branch that fell to a single child (height shrinks).
            while self.tree_height > 1 {
                let page = self.page(self.root_pgno);
                let sep_count = PageHeader::decode(page).entry_count as usize;
                if sep_count >= 1 {
                    break;
                }
                let only = BranchView::<K>::new(page, 0).child(0);
                self.free_page(self.root_pgno);
                self.root_pgno = only;
                self.tree_height -= 1;
            }
        }
        self.record_count -= 1;
        true
    }

    /// Recursive COW delete. Returns `(new_pgno, underflowed)` — underflow = an empty
    /// leaf or a single-child branch, which the parent (or `tree_delete` at the root)
    /// repairs.
    fn cow_delete(&mut self, pgno: u32, depth: u32, key: K) -> (u32, bool) {
        if depth == self.tree_height {
            let mut recs = self.read_leaf(pgno);
            self.free_page(pgno);
            let pos = recs.partition_point(|r| r.from < key);
            if pos < recs.len() && recs[pos].from == key {
                recs.remove(pos);
            }
            let p = self.write_leaf(&recs);
            (p, recs.is_empty())
        } else {
            let (mut seps, mut children) = self.read_branch(pgno);
            self.free_page(pgno);
            let i = seps.partition_point(|s| *s <= key);
            let (nc, child_uf) = self.cow_delete(children[i], depth + 1, key);
            children[i] = nc;
            if child_uf {
                self.rebalance(&mut seps, &mut children, i, depth + 1);
            }
            let p = self.write_branch(&seps, &children);
            (p, children.len() < 2)
        }
    }

    /// Merge an underflowed `children[i]` with an adjacent sibling and re-emit (1 or 2
    /// nodes), patching `seps`/`children`. Balance-preserving.
    fn rebalance(&mut self, seps: &mut Vec<K>, children: &mut Vec<u32>, i: usize, child_depth: u32) {
        let (l, r, sep_idx) = if i > 0 { (i - 1, i, i - 1) } else { (i, i + 1, i) };
        let (p, split) = if child_depth == self.tree_height {
            let mut recs = self.read_leaf(children[l]);
            let mut rr = self.read_leaf(children[r]);
            recs.append(&mut rr);
            self.free_page(children[l]);
            self.free_page(children[r]);
            self.emit_leaf(&recs)
        } else {
            let (mut s1, mut c1) = self.read_branch(children[l]);
            let (mut s2, mut c2) = self.read_branch(children[r]);
            self.free_page(children[l]);
            self.free_page(children[r]);
            s1.push(seps[sep_idx]);
            s1.append(&mut s2);
            c1.append(&mut c2);
            self.emit_branch(&s1, &c1)
        };
        match split {
            None => {
                children[l] = p;
                children.remove(r);
                seps.remove(sep_idx);
            }
            Some((newsep, p2)) => {
                children[l] = p;
                children[r] = p2;
                seps[sep_idx] = newsep;
            }
        }
    }

    // --- read-path queries over the pending tree ---

    fn scan_node<F: FnMut(K, K, &[u8])>(&self, pgno: u32, depth: u32, f: &mut F) {
        let page = self.page(pgno);
        let count = PageHeader::decode(page).entry_count as usize;
        if depth == self.tree_height {
            let leaf = LeafView::<K>::new(page, count, self.record_size);
            for i in 0..count {
                let rec = leaf.record(i);
                f(rec.from(), rec.to(), rec.scope());
            }
        } else {
            let branch = BranchView::<K>::new(page, count);
            for j in 0..branch.child_count() {
                self.scan_node(branch.child(j), depth + 1, f);
            }
        }
    }

    /// The leaf page that would contain `key` (0 if empty).
    fn descend_to_leaf(&self, key: K) -> u32 {
        if self.root_pgno == 0 {
            return 0;
        }
        let mut pgno = self.root_pgno;
        let mut depth = 1;
        while depth < self.tree_height {
            let page = self.page(pgno);
            let count = PageHeader::decode(page).entry_count as usize;
            let b = BranchView::<K>::new(page, count);
            let i = partition_idx(count, |j| b.sep(j) <= key);
            pgno = b.child(i);
            depth += 1;
        }
        pgno
    }

    /// The record covering `key` (`from <= key <= to`). Single-leaf.
    fn lookup_covering(&self, key: K) -> Option<(K, K, Vec<u8>)> {
        let pgno = self.descend_to_leaf(key);
        if pgno == 0 {
            return None;
        }
        let page = self.page(pgno);
        let count = PageHeader::decode(page).entry_count as usize;
        let leaf = LeafView::<K>::new(page, count, self.record_size);
        let pos = partition_idx(count, |i| leaf.record(i).from() <= key);
        if pos == 0 {
            return None;
        }
        let r = leaf.record(pos - 1);
        if key <= r.to() {
            Some((r.from(), r.to(), r.scope().to_vec()))
        } else {
            None
        }
    }

    /// Whether a record with exactly `from == key` exists.
    fn contains_from(&self, key: K) -> bool {
        let pgno = self.descend_to_leaf(key);
        if pgno == 0 {
            return false;
        }
        let page = self.page(pgno);
        let count = PageHeader::decode(page).entry_count as usize;
        let leaf = LeafView::<K>::new(page, count, self.record_size);
        let pos = partition_idx(count, |i| leaf.record(i).from() < key);
        pos < count && leaf.record(pos).from() == key
    }

    /// The record with the smallest `from >= key` (successor), via a cursor that walks
    /// to the next leaf when needed (no sibling pointers, D3).
    fn lookup_ge(&self, key: K) -> Option<(K, K, Vec<u8>)> {
        if self.root_pgno == 0 {
            return None;
        }
        let mut stack: Vec<(u32, usize)> = Vec::new();
        let mut pgno = self.root_pgno;
        let mut depth = 1;
        while depth < self.tree_height {
            let page = self.page(pgno);
            let count = PageHeader::decode(page).entry_count as usize;
            let b = BranchView::<K>::new(page, count);
            let i = partition_idx(count, |j| b.sep(j) <= key);
            stack.push((pgno, i));
            pgno = b.child(i);
            depth += 1;
        }
        let page = self.page(pgno);
        let count = PageHeader::decode(page).entry_count as usize;
        let leaf = LeafView::<K>::new(page, count, self.record_size);
        let pos = partition_idx(count, |i| leaf.record(i).from() < key);
        if pos < count {
            let r = leaf.record(pos);
            return Some((r.from(), r.to(), r.scope().to_vec()));
        }
        // No record >= key in this leaf; ascend to the nearest next child, descend left.
        while let Some((bp, ci)) = stack.pop() {
            let bpage = self.page(bp);
            let bcount = PageHeader::decode(bpage).entry_count as usize;
            let b = BranchView::<K>::new(bpage, bcount);
            if ci + 1 < b.child_count() {
                let mut p = b.child(ci + 1);
                loop {
                    let pp = self.page(p);
                    if PageHeader::decode(pp).page_type == spec::PAGE_TYPE_LEAF {
                        break;
                    }
                    let cnt = PageHeader::decode(pp).entry_count as usize;
                    p = BranchView::<K>::new(pp, cnt).child(0);
                }
                let lpage = self.page(p);
                let lcount = PageHeader::decode(lpage).entry_count as usize;
                let r = LeafView::<K>::new(lpage, lcount, self.record_size).record(0);
                return Some((r.from(), r.to(), r.scope().to_vec()));
            }
        }
        None
    }
}

/// `partition_point` over an index range `[0, count)`: the number of indices for which
/// `pred` (monotone true-then-false) holds — i.e. the first index where it is false.
#[inline]
fn partition_idx<P: Fn(usize) -> bool>(count: usize, pred: P) -> usize {
    let (mut lo, mut hi) = (0usize, count);
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        if pred(mid) {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    lo
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

    // --- the oracle: random set/delete vs. an independent in-memory interval map ---

    fn oracle_delete(o: &mut Vec<(u32, u32, u8)>, from: u32, to: u32) {
        let mut out = Vec::with_capacity(o.len() + 2);
        for &(rf, rt, sc) in o.iter() {
            if rt < from || rf > to {
                out.push((rf, rt, sc)); // no overlap
            } else {
                if rf < from {
                    out.push((rf, from - 1, sc));
                }
                if rt > to {
                    out.push((to + 1, rt, sc));
                }
            }
        }
        *o = out;
    }

    fn oracle_set(o: &mut Vec<(u32, u32, u8)>, from: u32, to: u32, scope: u8) {
        oracle_delete(o, from, to);
        let (mut nf, mut nt) = (from, to);
        if from > 0 {
            if let Some(idx) = o.iter().position(|r| r.1 == from - 1 && r.2 == scope) {
                nf = o[idx].0;
                o.remove(idx);
            }
        }
        if to < u32::MAX {
            if let Some(idx) = o.iter().position(|r| r.0 == to + 1 && r.2 == scope) {
                nt = o[idx].1;
                o.remove(idx);
            }
        }
        let pos = o.partition_point(|r| r.0 < nf);
        o.insert(pos, (nf, nt, scope));
    }

    #[test]
    fn oracle_random_set_delete_v4() {
        // Deterministic LCG so failures reproduce. Small key space ⇒ heavy overlap,
        // splits, merges, straddles, coalescing.
        let mut state = 0x1234_5678_9abc_def0u64;
        let mut rng = || {
            state = state
                .wrapping_mul(6364136223846793005)
                .wrapping_add(1442695040888963407);
            (state >> 33) as u32
        };

        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let mut oracle: Vec<(u32, u32, u8)> = Vec::new();
        let span = 250u32;

        for step in 0..6000u32 {
            let (a, b) = (rng() % span, rng() % span);
            let (from, to) = if a <= b { (a, b) } else { (b, a) };
            if rng() & 1 == 0 {
                let scope = (rng() % 4) as u8;
                w.set(k(from), k(to), &[scope]).unwrap();
                oracle_set(&mut oracle, from, to, scope);
            } else {
                w.delete(k(from), k(to)).unwrap();
                oracle_delete(&mut oracle, from, to);
            }
            let mut got = Vec::new();
            w.scan(|f, t, s| got.push((f.0, t.0, s[0])));
            assert_eq!(got, oracle, "writer/oracle diverged at step {step}");
            assert_eq!(w.record_count(), oracle.len() as u64, "count at step {step}");
        }

        // The whole on-disk structure must pass the reader's full validation.
        w.commit(0);
        let img = w.into_image();
        let r = Reader::open(&img).unwrap();
        let mut got = Vec::new();
        r.scan_v4(|f, t, s| got.push((f.0, t.0, s[0]))).unwrap();
        assert_eq!(got, oracle);
        assert_eq!(r.record_count(), oracle.len() as u64);
    }

    #[test]
    fn reopen_validates_and_mutates() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        for i in 0..500u32 {
            w.set(k(i * 10), k(i * 10 + 3), &[1]).unwrap();
        }
        w.commit(1);
        let img = w.into_image();

        // Reopen the committed image, derive the free set, mutate, recommit.
        let mut w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(w2.record_count(), 500);
        for i in 0..250u32 {
            w2.delete(k(i * 10), k(i * 10 + 3)).unwrap();
        }
        w2.set(k(99_999), k(100_000), &[7]).unwrap();
        w2.commit(2);

        let img2 = w2.into_image();
        let r = Reader::open(&img2).unwrap();
        assert_eq!(r.record_count(), 251); // 250 survivors + 1 new
        assert_eq!(r.lookup_v4(k(2500)).unwrap(), Some(&[1u8][..])); // i=250 survives
        assert_eq!(r.lookup_v4(k(5)).unwrap(), None); // i=0 deleted
        assert_eq!(r.lookup_v4(k(99_999)).unwrap(), Some(&[7u8][..]));
    }

    #[test]
    fn open_image_rejects_corruption() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        w.set(k(1), k(2), &[1]).unwrap();
        w.commit(1);
        let mut img = w.into_image();
        // Corrupt the active meta's checksum-covered bytes ⇒ both metas can't both be
        // valid / reachable tree mismatch ⇒ open_image must reject.
        let n = img.len();
        img[n - 100] ^= 0xFF; // a leaf-page byte
        assert!(Writer::<Ipv4Key>::open_image(img).is_err());
    }

    #[test]
    fn oracle_random_set_delete_v6() {
        // Same oracle, IPv6 keys (16-byte records / 20-byte branch entries) — exercises
        // the v6 leaf/branch offset arithmetic through splits and merges.
        use crate::key::Ipv6Key;
        let k6 = |n: u32| Ipv6Key { hi: 0, lo: n as u64 };
        let mut state = 0x0fed_cba9_8765_4321u64;
        let mut rng = || {
            state = state
                .wrapping_mul(6364136223846793005)
                .wrapping_add(1442695040888963407);
            (state >> 33) as u32
        };

        let mut w = Writer::<Ipv6Key>::create(3, 0);
        let mut oracle: Vec<(u32, u32, u8)> = Vec::new();
        let span = 200u32;

        for step in 0..3000u32 {
            let (a, b) = (rng() % span, rng() % span);
            let (from, to) = if a <= b { (a, b) } else { (b, a) };
            if rng() & 1 == 0 {
                let scope = (rng() % 3) as u8;
                w.set(k6(from), k6(to), &[scope, scope, scope]).unwrap();
                oracle_set(&mut oracle, from, to, scope);
            } else {
                w.delete(k6(from), k6(to)).unwrap();
                oracle_delete(&mut oracle, from, to);
            }
            let mut got = Vec::new();
            w.scan(|f, _t, s| got.push((f.lo as u32, s[0])));
            let want: Vec<(u32, u8)> = oracle.iter().map(|r| (r.0, r.2)).collect();
            assert_eq!(got, want, "v6 writer/oracle diverged at step {step}");
        }
        w.commit(0);
        let img = w.into_image();
        let r = Reader::open(&img).unwrap();
        assert_eq!(r.record_count(), oracle.len() as u64);
    }
}
