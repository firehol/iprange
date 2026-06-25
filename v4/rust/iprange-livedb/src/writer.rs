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

use alloc::collections::BTreeMap;
use alloc::vec;
use alloc::vec::Vec;
use core::marker::PhantomData;

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::kv::{self, BranchSep, KvEntry, LeafSlot};
use crate::node::{BranchView, LeafView};
use crate::reader::Reader;
use crate::record;
use crate::scope::{self, ScopeRec};
use crate::spec::{self, PAGE_HEADER_SIZE, PAGE_SIZE};
use crate::wire::{finalize_checksum, Meta, PageHeader};

/// What `meta_delete` did, mirroring the registry's `Changed`/`Unchanged` convention (§C.7).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Changed {
    /// The store changed (a key was deleted).
    Changed,
    /// No change (the key was absent) — a no-op success (§C.7).
    Unchanged,
}

/// One KV entry as returned by [`Writer::meta_list`]: `(key, type, value)`. `value` is the
/// whole reassembled value (inline or overflow-spanning); `type == 0` is validated text.
pub type MetaEntry = (Vec<u8>, u32, Vec<u8>);

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
    scope_table_root: u32, // v4.1 metadata (§C.1); 0 = no metadata (file stays v4.0)
    scopes: Vec<ScopeRec>, // in-memory registry (sorted by id, may include FILE id 0), rebuilt at commit
    next_scope_id: u32,    // monotonic; never reuses a dropped id (§C.2)
    scope_dirty: bool,     // registry changed since the last commit → rebuild needed
    // Per-target buffered KV (§C.4): a dirty target's full entry set (sorted by key),
    // loaded lazily from its committed `kv_root` on first mutation and bulk-rebuilt at
    // commit. `target = scope_id` (`0` = FILE). Absent = clean (read straight from disk).
    kv_dirty: BTreeMap<u32, Vec<KvEntry>>,
    free: Vec<u32>,           // pages reusable by the current txn
    freed_this_txn: Vec<u32>, // pages freed since the last commit (reusable next txn, D7)
    dirty: Vec<u32>,          // data pages written this txn (for the OS layer's pwrite set)
    committed_pages: usize,   // on-disk page count as of the last commit (grown-region boundary)
    // Set if the commit rebuild phase fails: page alloc/free is irreversible, so the
    // in-memory allocator/registry is then indeterminate. The on-disk meta is unwritten (the
    // file is the last committed valid state), so the writer must be discarded and reopened.
    // Every mutating op + commit refuses once poisoned.
    poisoned: bool,
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
            scope_table_root: 0,
            scopes: Vec::new(),
            next_scope_id: 1, // 0 is reserved for the FILE target (§C.2)
            scope_dirty: false,
            kv_dirty: BTreeMap::new(),
            free: Vec::new(),
            freed_this_txn: Vec::new(),
            dirty: Vec::new(),
            committed_pages: 2, // the two metas, both written by create
            poisoned: false,
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
    pub fn open_image(mut image: Vec<u8>) -> Result<Writer<K>> {
        let meta = {
            let r = Reader::open(&image)?;
            if K::VERSION != r.version() {
                return Err(Error::InvalidInput("writer family mismatch"));
            }
            r.active_meta()
        };
        // The writer implements up to version_minor 1 (the v4.1 metadata system). It MUST
        // refuse to mutate a file of a newer minor it does not fully implement (§5.1/§C.6):
        // committing would drop the newer minor's trailing fields. (The reader still
        // accepts such files read-only — forward-compat.) A v4.0 file (minor 0) is opened
        // as-is and stays v4.0 until the first metadata write upgrades it (§C.6).
        if meta.version_minor > spec::VERSION_MINOR_METADATA {
            return Err(Error::InvalidInput(
                "writer cannot mutate a newer version_minor file",
            ));
        }
        // Reclaim trailing pages beyond total_pages (a crashed growth, §6.4): the
        // committed total_pages is authoritative and reachable pages are all below it.
        image.truncate(meta.total_pages as usize * PAGE_SIZE);
        // Load the committed scope registry into memory (validated by `Reader::open` above).
        let scopes = scope::load_all(&image, meta.scope_table_root)?;
        let next_scope_id = scopes
            .iter()
            .map(|r| r.id)
            .max()
            .map_or(1, |m| m.saturating_add(1));
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
            scope_table_root: meta.scope_table_root,
            scopes,
            next_scope_id,
            scope_dirty: false,
            kv_dirty: BTreeMap::new(),
            free: Vec::new(),
            freed_this_txn: Vec::new(),
            dirty: Vec::new(),
            committed_pages: meta.total_pages as usize, // committed on-disk page count
            poisoned: false,
            _k: PhantomData,
        };
        w.free = w.derive_free_set();
        Ok(w)
    }

    /// `set([from,to]) = scope` unconditionally (§8, D11): clears the range, then
    /// inserts `[from,to,scope]` coalescing with byte-equal-scope adjacent neighbours.
    /// `O(k log n)` (k = records overlapping the range).
    pub fn set(&mut self, from: K, to: K, scope: &[u8]) -> Result<()> {
        self.ensure_usable()?;
        if to < from {
            return Err(Error::InvalidInput("set from > to"));
        }
        if scope.len() != self.scope_width {
            return Err(Error::InvalidInput("set scope width mismatch"));
        }
        self.delete_range(from, to)?;
        let (mut nf, mut nt) = (from, to);
        // Coalesce with a same-scope neighbour ending at from-1.
        if let Some(fm1) = from.checked_dec() {
            if let Some((lf, lt, ls)) = self.lookup_covering(fm1) {
                if lt == fm1 && ls == scope {
                    self.tree_delete(lf)?;
                    nf = lf;
                }
            }
        }
        // Coalesce with a same-scope neighbour starting at to+1.
        if let Some(tp1) = to.checked_inc() {
            if let Some((rf, rt, rs)) = self.lookup_covering(tp1) {
                if rf == tp1 && rs == scope {
                    self.tree_delete(rf)?;
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
        self.ensure_usable()?;
        if to < from {
            return Err(Error::InvalidInput("delete from > to"));
        }
        self.delete_range(from, to)
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
            let p = self.write_leaf(core::slice::from_ref(&rec))?;
            self.root_pgno = p;
            self.tree_height = 1;
        } else {
            let (new_root, split) = self.cow_insert(self.root_pgno, 1, rec)?;
            match split {
                None => self.root_pgno = new_root,
                Some((sep, right)) => {
                    if self.tree_height >= spec::TREE_HEIGHT_MAX {
                        return Err(Error::InvalidInput("tree would exceed TREE_HEIGHT_MAX"));
                    }
                    self.root_pgno = self.write_branch(&[sep], &[new_root, right])?;
                    self.tree_height += 1;
                }
            }
        }
        self.record_count += 1;
        Ok(())
    }

    /// Commit the accumulated mutations in memory: write the new state into the inactive
    /// meta and flip it (§6.3), reclaiming pages freed by this txn (D7) and clearing the
    /// dirty set. After this the image is a valid v4 file whose active meta is the new
    /// tree. (The OS layer uses [`commit_meta`](Self::commit_meta) + [`take_dirty`] to
    /// stage the two-fsync on-disk commit instead.) Errors only if `txn_id` is exhausted.
    pub fn commit(&mut self, updated_unixtime: u64) -> Result<()> {
        self.commit_meta(updated_unixtime)?;
        self.dirty.clear();
        Ok(())
    }

    /// Refuse to operate on a writer poisoned by a failed commit (see the `poisoned` field):
    /// the in-memory allocator state is indeterminate, so the caller must discard and reopen.
    #[inline]
    fn ensure_usable(&self) -> Result<()> {
        if self.poisoned {
            return Err(Error::State(
                "writer poisoned by a failed commit; discard and reopen",
            ));
        }
        Ok(())
    }

    /// The commit's metadata rebuild phase (§C.2/§C.4): bulk-rebuild each dirty target's KV
    /// tree (switching its `kv_root` in the registry), then the scope table if it changed.
    /// This allocates and writes the new scope-table/KV pages into the image (marking them
    /// dirty) and frees the old ones. The OS layer MUST call this **before** [`take_dirty`]
    /// (`take_dirty`) so those metadata pages are pwritten and made durable at Barrier 1 like
    /// every other data page (§6.3); the in-memory [`commit`](Self::commit) path reaches it
    /// via [`commit_meta`](Self::commit_meta). Performs irreversible page alloc/free, so a
    /// mid-phase failure leaves the allocator/registry indeterminate: the writer is poisoned
    /// (the on-disk file stays the last committed valid state) and the caller must discard and
    /// reopen. Refuses if `txn_id` is exhausted (§6.3; unreachable in practice).
    pub(crate) fn rebuild_commit_state(&mut self) -> Result<()> {
        self.ensure_usable()?;
        if self.txn_id == u64::MAX {
            return Err(Error::InvalidInput("txn_id exhausted"));
        }
        if let Err(e) = self.rebuild_commit_state_inner() {
            self.poisoned = true;
            return Err(e);
        }
        Ok(())
    }

    fn rebuild_commit_state_inner(&mut self) -> Result<()> {
        // KV first: this switches the affected scopes' `kv_root`s in the registry, so the
        // scope-table rebuild below carries the new roots. Frees old KV + overflow pages.
        self.rebuild_dirty_kv()?;
        if self.scope_dirty {
            self.rebuild_scope_table()?;
            self.scope_dirty = false;
        }
        Ok(())
    }

    /// Finalize the commit: write the new meta into the inactive page and flip it (the commit
    /// point, §6.3); returns the page number (0 or 1) just written so the OS layer can
    /// `pwrite` exactly that page as Barrier 2. Reclaims this txn's freed pages (D7). MUST be
    /// called after [`rebuild_commit_state`](Self::rebuild_commit_state) and, on the OS path,
    /// after the data-page Barrier 1, so the meta never references an unwritten page.
    pub(crate) fn finish_commit_meta(&mut self, updated_unixtime: u64) -> u32 {
        let inactive = 1 - self.active_meta;
        self.txn_id += 1;
        self.write_meta(inactive, self.txn_id, updated_unixtime);
        self.active_meta = inactive;
        let mut freed = core::mem::take(&mut self.freed_this_txn);
        self.free.append(&mut freed);
        // The file is now this long on disk after the OS layer's `set_len`; the next txn's
        // grown region starts here.
        self.committed_pages = self.image.len() / PAGE_SIZE;
        inactive
    }

    /// In-memory commit half: rebuild the metadata pages then finalize the meta in one step
    /// (no separate page barrier — the whole image is the unit, §6.3). The OS layer instead
    /// interleaves [`rebuild_commit_state`](Self::rebuild_commit_state) / [`take_dirty`]
    /// (`take_dirty`) / [`finish_commit_meta`](Self::finish_commit_meta) around its two fsync
    /// barriers.
    pub(crate) fn commit_meta(&mut self, updated_unixtime: u64) -> Result<u32> {
        self.rebuild_commit_state()?;
        Ok(self.finish_commit_meta(updated_unixtime))
    }

    /// Poison the writer after a failed durable commit (the OS layer calls this when I/O fails
    /// between the metadata rebuild and the commit point): the in-memory state is partially
    /// advanced and must not be reused. The on-disk file remains the last committed valid
    /// state, recovered automatically on the next open.
    pub(crate) fn poison(&mut self) {
        self.poisoned = true;
    }

    /// Take the set of data pages the OS layer must `pwrite` at Barrier 1. A page written
    /// then freed again within the same txn is an orphan the new meta never references, so
    /// pwriting it is wasted I/O — **except** if it lies in the file's newly-grown region
    /// (`pgno >= committed_pages`): the OS layer `set_len`s the file to the new length, so
    /// every offset up to it MUST be backed by real bytes or the mmap reader rejects a
    /// sparse hole (§10). We therefore drop a freed page only when it already existed on
    /// disk before this txn (`pgno < committed_pages`). Within one txn each page is written
    /// at most once (freed pages are not reused until the next txn), so no duplicates.
    #[cfg_attr(not(feature = "os"), allow(dead_code))]
    pub(crate) fn take_dirty(&mut self) -> Vec<u32> {
        let dirty = core::mem::take(&mut self.dirty);
        if self.freed_this_txn.is_empty() {
            return dirty;
        }
        let boundary = self.committed_pages as u32;
        dirty
            .into_iter()
            .filter(|&p| p >= boundary || !self.freed_this_txn.contains(&p))
            .collect()
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

    // --- v4.1 scope registry (§C.2) ---

    /// Define a new scope, returning its `scope_id` (`>= 1`; `0` is reserved for FILE,
    /// never returned). `version` starts at 0, `type` at 0, no KV. `scope_id`s are
    /// monotonic and never reused after a drop (§C.2). `name` is UTF-8, `<= 256` bytes
    /// (need not be unique). The first metadata write upgrades the file to v4.1 at commit.
    pub fn scope_define(&mut self, name: &[u8]) -> Result<u32> {
        self.ensure_usable()?;
        if name.len() > spec::SCOPE_NAME_MAX {
            return Err(Error::InvalidInput("scope name > 256 bytes"));
        }
        if core::str::from_utf8(name).is_err() {
            return Err(Error::InvalidInput("scope name not valid UTF-8"));
        }
        if self.next_scope_id == spec::FILE_SCOPE_ID {
            return Err(Error::InvalidInput("scope_id space exhausted"));
        }
        let id = self.next_scope_id;
        self.next_scope_id = self.next_scope_id.wrapping_add(1);
        // Monotonic ids ⇒ pushing at the end keeps `scopes` sorted by id.
        self.scopes.push(ScopeRec {
            id,
            version: 0,
            type_: 0,
            name: name.to_vec(),
            kv_root: 0,
        });
        self.scope_dirty = true;
        Ok(id)
    }

    /// Drop a scope: remove its metadata (header + KV) only — IP records carrying it are
    /// NOT touched (caller policy, §C.2). `scope_drop(0)` (FILE) is rejected. Returns
    /// whether the scope existed.
    pub fn scope_drop(&mut self, scope_id: u32) -> Result<bool> {
        self.ensure_usable()?;
        if scope_id == spec::FILE_SCOPE_ID {
            return Err(Error::InvalidInput("cannot drop the FILE scope (0)"));
        }
        match self.scope_pos(scope_id) {
            Some(i) => {
                // Free the dropped scope's KV tree + overflow pages (§C.2 / §C.5). Its
                // committed `kv_root` is authoritative; any buffered KV for this target is
                // discarded too (the scope is gone).
                let kv_root = self.scopes[i].kv_root;
                self.free_kv_tree(kv_root);
                self.kv_dirty.remove(&scope_id);
                self.scopes.remove(i);
                self.scope_dirty = true;
                Ok(true)
            }
            None => Ok(false),
        }
    }

    /// The scope's `name` (UTF-8 bytes), or `None` if it does not exist.
    pub fn scope_name(&self, scope_id: u32) -> Option<Vec<u8>> {
        self.scope_pos(scope_id)
            .map(|i| self.scopes[i].name.clone())
    }

    /// All defined scopes as `(scope_id, name)`, ascending by `scope_id`. The FILE target
    /// (`scope_id 0`) is a dataset-metadata target, not a defined scope, so it is excluded
    /// even when it carries KV (§C.2).
    pub fn scope_list(&self) -> Vec<(u32, Vec<u8>)> {
        self.scopes
            .iter()
            .filter(|r| r.id != spec::FILE_SCOPE_ID)
            .map(|r| (r.id, r.name.clone()))
            .collect()
    }

    /// The scope's `version`, or `None` if it does not exist.
    pub fn scope_version(&self, scope_id: u32) -> Option<u64> {
        self.scope_pos(scope_id).map(|i| self.scopes[i].version)
    }

    /// Set the scope's `version` (caller-bumped, §C.3). Returns whether it existed.
    pub fn scope_set_version(&mut self, scope_id: u32, version: u64) -> Result<bool> {
        self.ensure_usable()?;
        match self.scope_pos(scope_id) {
            Some(i) => {
                self.scopes[i].version = version;
                self.scope_dirty = true;
                Ok(true)
            }
            None => Ok(false),
        }
    }

    /// Increment the scope's `version` (saturating). Returns whether it existed.
    pub fn scope_bump_version(&mut self, scope_id: u32) -> Result<bool> {
        self.ensure_usable()?;
        match self.scope_pos(scope_id) {
            Some(i) => {
                self.scopes[i].version = self.scopes[i].version.saturating_add(1);
                self.scope_dirty = true;
                Ok(true)
            }
            None => Ok(false),
        }
    }

    /// The scope's opaque `type` byte, or `None` if it does not exist.
    pub fn scope_type(&self, scope_id: u32) -> Option<u8> {
        self.scope_pos(scope_id).map(|i| self.scopes[i].type_)
    }

    /// Set the scope's opaque `type` byte (engine does not interpret it, §C.2). Returns
    /// whether it existed.
    pub fn scope_set_type(&mut self, scope_id: u32, type_: u8) -> Result<bool> {
        self.ensure_usable()?;
        match self.scope_pos(scope_id) {
            Some(i) => {
                self.scopes[i].type_ = type_;
                self.scope_dirty = true;
                Ok(true)
            }
            None => Ok(false),
        }
    }

    /// Index of a **defined** scope in the registry (binary search). The FILE target
    /// (`scope_id 0`) is never a defined scope, so registry getters/setters miss it even
    /// when it is persisted in `scopes` carrying dataset KV (§C.2).
    fn scope_pos(&self, scope_id: u32) -> Option<usize> {
        if scope_id == spec::FILE_SCOPE_ID {
            return None;
        }
        self.scope_idx_any(scope_id)
    }

    /// Index of any scope record by id, **including** FILE (`scope_id 0`) — used by the KV
    /// layer, which targets FILE as well as defined scopes.
    fn scope_idx_any(&self, scope_id: u32) -> Option<usize> {
        self.scopes.binary_search_by_key(&scope_id, |r| r.id).ok()
    }

    /// Rebuild the scope table from the in-memory registry at commit (§C.2): free the old
    /// tree's pages (reclaimed next txn) and bulk-build a fresh one. Sets `scope_table_root`
    /// (0 when empty → the file stays/returns to byte-compatible v4.0, §C.6).
    fn rebuild_scope_table(&mut self) -> Result<()> {
        let mut old = Vec::new();
        scope::collect_pages(&self.image, self.scope_table_root, &mut old);
        for p in old {
            self.free_page(p);
        }
        let scopes = core::mem::take(&mut self.scopes);
        let root = self.build_scope_tree(&scopes);
        self.scopes = scopes;
        self.scope_table_root = root?;
        Ok(())
    }

    /// Bulk-build a scope-table B+tree from `scopes` (sorted by id) into freshly allocated
    /// pages; returns the new root pgno (0 if empty). Leaves fill to `scope_leaf_max`;
    /// branches use each child subtree's first id as a separator (shape is impl-defined).
    fn build_scope_tree(&mut self, scopes: &[ScopeRec]) -> Result<u32> {
        if scopes.is_empty() {
            return Ok(0);
        }
        let mut buf = vec![0u8; PAGE_SIZE];
        let leaf_max = spec::scope_leaf_max();
        let mut level: Vec<(u32, u32)> = Vec::new();
        for chunk in scopes.chunks(leaf_max) {
            let pgno = self.alloc_page()?;
            scope::write_scope_leaf(&mut buf, pgno, chunk);
            self.write_page(pgno, &buf);
            level.push((pgno, chunk[0].id));
        }
        let mut height = 1u32;
        let fanout = spec::scope_branch_max() + 1;
        while level.len() > 1 {
            if height >= spec::TREE_HEIGHT_MAX {
                return Err(Error::InvalidInput(
                    "scope table would exceed TREE_HEIGHT_MAX",
                ));
            }
            let mut next: Vec<(u32, u32)> = Vec::new();
            // Group children into branch nodes by `fanout`, then rebalance so the FINAL
            // node never has a lone child (F1): a branch MUST have >= 2 children
            // (validator rejects `count < 1`). `fanout >= 3` for the scope branch, so the
            // split of the last `fanout + 1` children is always >= 2 per node.
            for (start, end) in branch_group_bounds(level.len(), fanout) {
                let chunk = &level[start..end];
                let pgno = self.alloc_page()?;
                let children: Vec<u32> = chunk.iter().map(|&(p, _)| p).collect();
                let seps: Vec<u32> = chunk.iter().skip(1).map(|&(_, id)| id).collect();
                scope::write_scope_branch(&mut buf, pgno, &seps, &children);
                self.write_page(pgno, &buf);
                next.push((pgno, chunk[0].1));
            }
            level = next;
            height += 1;
        }
        Ok(level[0].0)
    }

    /// Copy a fully-built page into the image and record it dirty (for the OS pwrite set).
    fn write_page(&mut self, pgno: u32, page: &[u8]) {
        let base = pgno as usize * PAGE_SIZE;
        self.image[base..base + PAGE_SIZE].copy_from_slice(page);
        self.dirty.push(pgno);
    }

    // --- v4.1 per-scope KV (§C.4) ---

    /// Set `key = (type, value)` on `target` (`target = scope_id`, `0` = FILE). Buffers the
    /// change in memory; the target's KV tree is bulk-rebuilt at the next `commit`. Many
    /// `meta_set` on one target in one txn ⇒ ONE rebuild (§C.4). Validates `key` (UTF-8,
    /// 1..=1024, no NUL) and, for `type == 0`, the whole `value` (UTF-8, no NUL) →
    /// `InvalidInput` on violation (§C.7). A non-existent non-FILE `target` → `InvalidInput`.
    pub fn meta_set(&mut self, target: u32, key: &[u8], type_: u32, value: &[u8]) -> Result<()> {
        self.ensure_usable()?;
        kv::check_key(key)?;
        kv::check_text_value(type_, value)?;
        self.require_target(target)?;
        let entries = self.kv_load_dirty(target)?;
        let pos = entries.partition_point(|e| e.key.as_slice() < key);
        let new = KvEntry {
            key: key.to_vec(),
            type_,
            value: value.to_vec(),
        };
        if pos < entries.len() && entries[pos].key == key {
            entries[pos] = new; // replace in place (key already present)
        } else {
            entries.insert(pos, new);
        }
        Ok(())
    }

    /// Get `key` on `target` as `(type, value)` (the whole reassembled value), or
    /// `Ok(None)` if absent (§C.7). Reads the buffered set if the target is dirty this txn,
    /// else descends the committed KV tree. A non-existent non-FILE `target` → `Ok(None)`.
    pub fn meta_get(&self, target: u32, key: &[u8]) -> Result<Option<(u32, Vec<u8>)>> {
        kv::check_key(key)?;
        if let Some(entries) = self.kv_dirty.get(&target) {
            let pos = entries.partition_point(|e| e.key.as_slice() < key);
            if pos < entries.len() && entries[pos].key == key {
                let e = &entries[pos];
                return Ok(Some((e.type_, e.value.clone())));
            }
            return Ok(None);
        }
        let root = match self.target_kv_root(target) {
            Some(r) => r,
            None => return Ok(None),
        };
        kv::get(&self.image, root, key, self.total_pages())
    }

    /// Delete `key` on `target`. Returns `Changed` if a key was removed, `Unchanged` if it
    /// was absent (a no-op success, §C.7). Buffers the change; rebuilt at commit.
    pub fn meta_delete(&mut self, target: u32, key: &[u8]) -> Result<Changed> {
        self.ensure_usable()?;
        kv::check_key(key)?;
        // Deleting on a non-existent target is a no-op (nothing to delete). A target with KV
        // buffered this txn (`kv_dirty`) is live even before it has a committed record — e.g.
        // a FILE key set then deleted in the same txn — so it must NOT short-circuit (parity
        // with the Go writer; otherwise the set+delete pair would wrongly persist the key and
        // keep the file at v4.1 instead of reverting to v4.0).
        if !self.kv_dirty.contains_key(&target) && self.scope_idx_any(target).is_none() {
            return Ok(Changed::Unchanged);
        }
        let entries = self.kv_load_dirty(target)?;
        let pos = entries.partition_point(|e| e.key.as_slice() < key);
        if pos < entries.len() && entries[pos].key == key {
            entries.remove(pos);
            Ok(Changed::Changed)
        } else {
            Ok(Changed::Unchanged)
        }
    }

    /// List every `(key, type, value)` on `target`, ordered by `key` (§C.4). Reads the
    /// buffered set if dirty this txn, else the committed KV tree. A non-existent target →
    /// an empty list.
    pub fn meta_list(&self, target: u32) -> Result<Vec<MetaEntry>> {
        if let Some(entries) = self.kv_dirty.get(&target) {
            return Ok(entries
                .iter()
                .map(|e| (e.key.clone(), e.type_, e.value.clone()))
                .collect());
        }
        let mut out = Vec::new();
        if let Some(root) = self.target_kv_root(target) {
            let mut entries = Vec::new();
            kv::list(&self.image, root, self.total_pages(), &mut entries)?;
            out = entries
                .into_iter()
                .map(|e| (e.key, e.type_, e.value))
                .collect();
        }
        Ok(out)
    }

    /// The committed `kv_root` of `target` (`None` if the target has no record). FILE
    /// (`scope_id 0`) is looked up like any scope record.
    fn target_kv_root(&self, target: u32) -> Option<u32> {
        self.scope_idx_any(target).map(|i| self.scopes[i].kv_root)
    }

    /// Validate that a KV mutation target exists. FILE (`scope_id 0`) is always valid (it
    /// is created on demand). A non-existent defined scope → `InvalidInput` (§C.7 — a write
    /// to a scope that was never defined is caller error).
    fn require_target(&self, target: u32) -> Result<()> {
        if target == spec::FILE_SCOPE_ID {
            return Ok(());
        }
        if self.scope_pos(target).is_some() {
            Ok(())
        } else {
            Err(Error::InvalidInput("meta_set on undefined scope"))
        }
    }

    /// Borrow the target's buffered entry set, loading it from the committed KV tree on
    /// first touch this txn (`O(n_kv)` once). Subsequent mutations operate in memory.
    fn kv_load_dirty(&mut self, target: u32) -> Result<&mut Vec<KvEntry>> {
        if !self.kv_dirty.contains_key(&target) {
            let mut entries = Vec::new();
            if let Some(root) = self.target_kv_root(target) {
                kv::list(&self.image, root, self.total_pages(), &mut entries)?;
            }
            self.kv_dirty.insert(target, entries);
        }
        Ok(self.kv_dirty.get_mut(&target).expect("just inserted"))
    }

    /// Rebuild every dirty target's KV tree at commit (§C.4): for each, free the old KV +
    /// overflow pages, bulk-build a fresh balanced tree from the sorted buffered entries,
    /// and switch the target's `kv_root`. Creates a FILE (`scope_id 0`) record on demand
    /// the first time FILE gets KV; drops a record's KV (`kv_root = 0`) when it becomes
    /// empty. Marks the registry dirty so the scope table picks up the new roots.
    fn rebuild_dirty_kv(&mut self) -> Result<()> {
        if self.kv_dirty.is_empty() {
            return Ok(());
        }
        let targets = core::mem::take(&mut self.kv_dirty);
        for (target, entries) in targets {
            let old_root = self.target_kv_root(target).unwrap_or(0);
            self.free_kv_tree(old_root);
            let new_root = self.build_kv_tree(&entries)?;
            self.set_target_kv_root(target, new_root);
        }
        Ok(())
    }

    /// Set a target's `kv_root`, creating the FILE record on demand and removing a record
    /// that becomes empty metadata. Marks the registry dirty (the scope table must rebuild).
    fn set_target_kv_root(&mut self, target: u32, new_root: u32) {
        match self.scope_idx_any(target) {
            Some(i) => {
                if target == spec::FILE_SCOPE_ID && new_root == 0 {
                    // FILE carries only KV; with none it has no metadata → drop its record
                    // so an all-empty file returns to byte-compatible v4.0 (§C.6).
                    self.scopes.remove(i);
                } else {
                    self.scopes[i].kv_root = new_root;
                }
            }
            None => {
                if new_root != 0 {
                    debug_assert_eq!(target, spec::FILE_SCOPE_ID, "only FILE is auto-created");
                    // FILE record: no name/type/version, just the KV root. Insert sorted
                    // (id 0 sorts first).
                    let pos = self.scopes.partition_point(|r| r.id < target);
                    self.scopes.insert(
                        pos,
                        ScopeRec {
                            id: target,
                            version: 0,
                            type_: 0,
                            name: Vec::new(),
                            kv_root: new_root,
                        },
                    );
                }
            }
        }
        self.scope_dirty = true;
    }

    /// Free a KV tree's pages (the tree + all overflow chains) into this txn's freed set
    /// (reclaimed next txn, D7). `root == 0` is a no-op.
    fn free_kv_tree(&mut self, root: u32) {
        if root == 0 {
            return;
        }
        let mut pages = Vec::new();
        kv::collect_pages(&self.image, root, self.total_pages(), &mut pages);
        for p in pages {
            self.free_page(p);
        }
    }

    /// Bulk-build a balanced KV B+tree from `entries` (sorted, key-unique) into freshly
    /// allocated pages; returns the new root pgno (0 if empty). Large values are written to
    /// overflow chains first; leaves pack entries greedily by encoded size, branches by
    /// separator size — the shape is implementation-defined (§C.4/§D).
    fn build_kv_tree(&mut self, entries: &[KvEntry]) -> Result<u32> {
        if entries.is_empty() {
            return Ok(0);
        }
        // 1) Turn each entry into a leaf slot, writing overflow chains for large values.
        let mut slots: Vec<LeafSlot> = Vec::with_capacity(entries.len());
        for e in entries {
            if e.value.len() <= spec::KV_INLINE_MAX {
                slots.push(LeafSlot::Inline {
                    key: e.key.clone(),
                    type_: e.type_,
                    value: e.value.clone(),
                });
            } else {
                let first = self.write_overflow_chain(&e.value)?;
                slots.push(LeafSlot::Overflow {
                    key: e.key.clone(),
                    type_: e.type_,
                    first_pgno: first,
                    total_len: e.value.len() as u64,
                });
            }
        }
        // 2) Pack leaves greedily (each leaf keeps >= 1 slot; the spec geometry guarantees
        //    the largest single inline entry + its slot fits a fresh leaf body).
        let mut buf = vec![0u8; PAGE_SIZE];
        let mut level: Vec<(u32, Vec<u8>)> = Vec::new(); // (pgno, first key of subtree)
        let mut i = 0usize;
        while i < slots.len() {
            let mut used = 0usize;
            let mut j = i;
            while j < slots.len() {
                let add = slots[j].footprint();
                if j > i && used + add > spec::KV_PAGE_BODY {
                    break;
                }
                used += add;
                j += 1;
            }
            let pgno = self.alloc_page()?;
            kv::write_kv_leaf(&mut buf, pgno, &slots[i..j]);
            self.write_page(pgno, &buf);
            level.push((pgno, slots[i].key().to_vec()));
            i = j;
        }
        // 3) Build branch levels until a single root remains.
        let mut height = 1u32;
        while level.len() > 1 {
            if height >= spec::TREE_HEIGHT_MAX {
                return Err(Error::InvalidInput("kv tree would exceed TREE_HEIGHT_MAX"));
            }
            let next = self.emit_kv_branch_level(&level, &mut buf)?;
            level = next;
            height += 1;
        }
        Ok(level[0].0)
    }

    /// Build one KV branch level from `level` (the lower level's `(pgno, first_key)`),
    /// returning the parent level. Children are packed greedily by separator byte size; the
    /// final node is rebalanced so it never holds a single child (F1): a branch MUST have at
    /// least two children (the validator rejects `count < 1`). When the greedy split would
    /// leave a lone last child, one child is moved from the previous (full) node into it — the
    /// previous keeps at least two (a full KV branch holds several children even with
    /// max-size keys).
    fn emit_kv_branch_level(
        &mut self,
        level: &[(u32, Vec<u8>)],
        buf: &mut [u8],
    ) -> Result<Vec<(u32, Vec<u8>)>> {
        // Phase 1: greedy group boundaries as `[start, end)` index ranges (each >= 1 child).
        let body = spec::KV_PAGE_BODY - 4; // the leftmost-child `u32` consumes 4 body bytes
        let mut groups: Vec<(usize, usize)> = Vec::new();
        let mut i = 0usize;
        while i < level.len() {
            let mut used = 0usize;
            let mut j = i + 1;
            while j < level.len() {
                let add = spec::kv_branch_sep_size(level[j].1.len()) + spec::KV_SLOT_SIZE;
                if used + add > body {
                    break;
                }
                used += add;
                j += 1;
            }
            groups.push((i, j));
            i = j;
        }
        // Phase 2: if the final node would be a lone child, steal one from the previous
        // node so both end up with >= 2 children. The previous node is FULL (its greedy loop
        // stopped because the singleton's separator did not fit), and a full KV branch holds
        // at least four children: the largest separator is `kv_branch_sep_size(KV_KEY_MAX) +
        // KV_SLOT_SIZE` = 1032 bytes and the body is `KV_PAGE_BODY - 4` = 4076, so >= 3
        // separators (>= 4 children) always fit. Hence stealing one leaves the previous with
        // at least three — never a new lone child.
        if groups.len() >= 2 {
            let last = groups.len() - 1;
            if groups[last].1 - groups[last].0 == 1 {
                let prev = last - 1;
                debug_assert!(
                    groups[prev].1 - groups[prev].0 >= 4,
                    "a full KV branch holds >= 4 children (KV_KEY_MAX caps the separator size)"
                );
                groups[prev].1 -= 1;
                groups[last].0 -= 1;
            }
        }
        // Phase 3: emit each group.
        let mut next: Vec<(u32, Vec<u8>)> = Vec::with_capacity(groups.len());
        for (start, end) in groups {
            let leftmost = level[start].0;
            let seps: Vec<BranchSep> = level[start + 1..end]
                .iter()
                .map(|(child, sep)| BranchSep {
                    sep: sep.clone(),
                    child: *child,
                })
                .collect();
            let pgno = self.alloc_page()?;
            kv::write_kv_branch(buf, pgno, leftmost, &seps);
            self.write_page(pgno, buf);
            next.push((pgno, level[start].1.clone()));
        }
        Ok(next)
    }

    /// Write `value` to a fresh overflow page chain; returns the first page's pgno. Splits
    /// into `ceil(len/overflow_payload)` pages, each pointing to the next (last → 0), §D.
    fn write_overflow_chain(&mut self, value: &[u8]) -> Result<u32> {
        debug_assert!(!value.is_empty());
        let payload = spec::OVERFLOW_PAYLOAD;
        let n = value.len().div_ceil(payload);
        // Allocate all pages up front so each can reference the next.
        let mut pgnos = Vec::with_capacity(n);
        for _ in 0..n {
            pgnos.push(self.alloc_page()?);
        }
        let mut buf = vec![0u8; PAGE_SIZE];
        for (k, &pgno) in pgnos.iter().enumerate() {
            let start = k * payload;
            let end = (start + payload).min(value.len());
            let next = if k + 1 < n { pgnos[k + 1] } else { 0 };
            kv::write_overflow(&mut buf, pgno, next, &value[start..end]);
            self.write_page(pgno, &buf);
        }
        Ok(pgnos[0])
    }

    /// The current logical page count of the in-memory image (for KV bounds checks).
    #[inline]
    fn total_pages(&self) -> u64 {
        (self.image.len() / PAGE_SIZE) as u64
    }

    // --- COW internals ---

    /// Recursive COW insert. Returns the new subtree pgno and, on overflow, a
    /// `(separator, right_pgno)` split for the parent to absorb.
    fn cow_insert(
        &mut self,
        pgno: u32,
        depth: u32,
        rec: OwnedRecord<K>,
    ) -> Result<(u32, Option<(K, u32)>)> {
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
            let (new_child, split) = self.cow_insert(children[i], depth + 1, rec)?;
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
    fn emit_leaf(&mut self, records: &[OwnedRecord<K>]) -> Result<(u32, Option<(K, u32)>)> {
        if records.len() <= self.leaf_max {
            Ok((self.write_leaf(records)?, None))
        } else {
            let mid = records.len() / 2;
            let lp = self.write_leaf(&records[..mid])?;
            let rp = self.write_leaf(&records[mid..])?;
            Ok((lp, Some((records[mid].from, rp))))
        }
    }

    /// Write a branch, or split into two (promoting the middle separator) if over
    /// `branch_max`.
    fn emit_branch(&mut self, seps: &[K], children: &[u32]) -> Result<(u32, Option<(K, u32)>)> {
        if seps.len() <= self.branch_max {
            Ok((self.write_branch(seps, children)?, None))
        } else {
            let mid = seps.len() / 2;
            let lp = self.write_branch(&seps[..mid], &children[..mid + 1])?;
            let rp = self.write_branch(&seps[mid + 1..], &children[mid + 1..])?;
            Ok((lp, Some((seps[mid], rp))))
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

    fn write_leaf(&mut self, records: &[OwnedRecord<K>]) -> Result<u32> {
        let pgno = self.alloc_page()?;
        self.dirty.push(pgno);
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
            record::write::<K>(
                &mut self.image[off..off + self.record_size],
                r.from,
                r.to,
                &r.scope,
            );
        }
        finalize_checksum(&mut self.image[base..base + PAGE_SIZE]);
        Ok(pgno)
    }

    fn write_branch(&mut self, seps: &[K], children: &[u32]) -> Result<u32> {
        debug_assert_eq!(children.len(), seps.len() + 1);
        let pgno = self.alloc_page()?;
        self.dirty.push(pgno);
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
        Ok(pgno)
    }

    fn write_meta(&mut self, pgno: u32, txn_id: u64, updated_unixtime: u64) {
        // A file with metadata is v4.1 (minor 1, meta_size 94); with none it stays
        // byte-compatible v4.0 (minor 0, meta_size 90) — the upgrade-on-first-write and
        // stays-v4.0-when-empty rule (§C.6).
        let (version_minor, meta_size) = if self.scope_table_root != 0 {
            (spec::VERSION_MINOR_METADATA, spec::META_SIZE_V41)
        } else {
            (spec::VERSION_MINOR, spec::META_SIZE)
        };
        let meta = Meta {
            pgno,
            version_minor,
            meta_size,
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
            scope_table_root: self.scope_table_root,
        };
        let base = pgno as usize * PAGE_SIZE;
        meta.encode_into(&mut self.image[base..base + PAGE_SIZE]);
    }

    #[inline]
    fn page(&self, pgno: u32) -> &[u8] {
        let base = pgno as usize * PAGE_SIZE;
        &self.image[base..base + PAGE_SIZE]
    }

    /// Allocate a page: reuse a freed page (from a prior txn) or grow the image. Refuses
    /// to grow past the `2^32`-page limit (§6.4) rather than wrap the `u32` pgno.
    fn alloc_page(&mut self) -> Result<u32> {
        if let Some(p) = self.free.pop() {
            Ok(p)
        } else {
            // u64 math so the `1 << 32` literal and the comparison are valid on 32-bit
            // targets (where `usize` is 32-bit and `1usize << 32` would overflow).
            if (self.image.len() / PAGE_SIZE) as u64 >= (1u64 << 32) {
                return Err(Error::InvalidInput("file would exceed the 2^32-page limit"));
            }
            let p = (self.image.len() / PAGE_SIZE) as u32;
            self.image.resize(self.image.len() + PAGE_SIZE, 0);
            Ok(p)
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
        // The v4.1 scope table is also reachable (§C.5): its pages must not be reallocated.
        if self.scope_table_root != 0 {
            let mut sp = Vec::new();
            scope::collect_pages(&self.image, self.scope_table_root, &mut sp);
            for p in sp {
                used[p as usize] = true;
            }
        }
        // Every scope's KV tree + its overflow chains are reachable too (§C.5): walk each
        // committed `kv_root` (incl. FILE's, if persisted) into the used set.
        let mut kp = Vec::new();
        for rec in &self.scopes {
            kv::collect_pages(&self.image, rec.kv_root, total as u64, &mut kp);
        }
        for p in kp {
            used[p as usize] = true;
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
    fn delete_range(&mut self, from: K, to: K) -> Result<()> {
        while let Some((rf, rt, scope)) = self.any_overlap(from, to) {
            self.tree_delete(rf)?;
            if rf < from {
                let fm1 = from.checked_dec().expect("from > rf >= family_min");
                self.insert(rf, fm1, &scope)?;
            }
            if rt > to {
                let tp1 = to.checked_inc().expect("to < rt <= family_max");
                self.insert(tp1, rt, &scope)?;
            }
        }
        Ok(())
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
    fn tree_delete(&mut self, key: K) -> Result<bool> {
        if self.root_pgno == 0 || !self.contains_from(key) {
            return Ok(false);
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
                self.root_pgno = self.write_leaf(&recs)?;
            }
        } else {
            let (new_root, _uf) = self.cow_delete(self.root_pgno, 1, key)?;
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
        Ok(true)
    }

    /// Recursive COW delete. Returns `(new_pgno, underflowed)` — underflow = an empty
    /// leaf or a single-child branch, which the parent (or `tree_delete` at the root)
    /// repairs.
    fn cow_delete(&mut self, pgno: u32, depth: u32, key: K) -> Result<(u32, bool)> {
        if depth == self.tree_height {
            let mut recs = self.read_leaf(pgno);
            self.free_page(pgno);
            let pos = recs.partition_point(|r| r.from < key);
            if pos < recs.len() && recs[pos].from == key {
                recs.remove(pos);
            }
            let p = self.write_leaf(&recs)?;
            Ok((p, recs.is_empty()))
        } else {
            let (mut seps, mut children) = self.read_branch(pgno);
            self.free_page(pgno);
            let i = seps.partition_point(|s| *s <= key);
            let (nc, child_uf) = self.cow_delete(children[i], depth + 1, key)?;
            children[i] = nc;
            if child_uf {
                self.rebalance(&mut seps, &mut children, i, depth + 1)?;
            }
            let p = self.write_branch(&seps, &children)?;
            Ok((p, children.len() < 2))
        }
    }

    /// Merge an underflowed `children[i]` with an adjacent sibling and re-emit (1 or 2
    /// nodes), patching `seps`/`children`. Balance-preserving.
    fn rebalance(
        &mut self,
        seps: &mut Vec<K>,
        children: &mut Vec<u32>,
        i: usize,
        child_depth: u32,
    ) -> Result<()> {
        let (l, r, sep_idx) = if i > 0 {
            (i - 1, i, i - 1)
        } else {
            (i, i + 1, i)
        };
        let (p, split) = if child_depth == self.tree_height {
            let mut recs = self.read_leaf(children[l]);
            let mut rr = self.read_leaf(children[r]);
            recs.append(&mut rr);
            self.free_page(children[l]);
            self.free_page(children[r]);
            self.emit_leaf(&recs)?
        } else {
            let (mut s1, mut c1) = self.read_branch(children[l]);
            let (mut s2, mut c2) = self.read_branch(children[r]);
            self.free_page(children[l]);
            self.free_page(children[r]);
            s1.push(seps[sep_idx]);
            s1.append(&mut s2);
            c1.append(&mut c2);
            self.emit_branch(&s1, &c1)?
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
        Ok(())
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
                // Leftmost descent, bounded by TREE_HEIGHT_MAX so a malformed tree can
                // never loop (the writer only operates on validated trees, so the cap is
                // never reached in practice — it is a defensive termination guarantee).
                for _ in 0..spec::TREE_HEIGHT_MAX {
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

/// Group `n` children into fixed-`fanout` branch nodes as `[start, end)` index ranges,
/// rebalancing so the FINAL node never has a single child (F1): a branch MUST have >= 2
/// children. When `n % fanout == 1` (the last `chunks(fanout)` group would be a lone
/// child), the last full group and that singleton — `fanout + 1` children total — are
/// split into `ceil((fanout+1)/2)` and `floor((fanout+1)/2)`, both >= 2 since
/// `fanout >= 3`. `n >= 2` (only called when a branch level is built).
fn branch_group_bounds(n: usize, fanout: usize) -> Vec<(usize, usize)> {
    debug_assert!(n >= 2 && fanout >= 3);
    let mut bounds = Vec::new();
    let mut start = 0usize;
    while start < n {
        let remaining = n - start;
        let take = if remaining > fanout && remaining - fanout < 2 {
            // The next-but-last group would leave a lone last child; split the tail
            // (`fanout + 1` children) evenly so both halves have >= 2.
            remaining.div_ceil(2)
        } else {
            fanout.min(remaining)
        };
        bounds.push((start, start + take));
        start += take;
    }
    bounds
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
        w.commit(0).unwrap();
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
        w.commit(0).unwrap();
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
        w.commit(0).unwrap();
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
        w.commit(1).unwrap();
        let pages_after_first = w.image().len() / PAGE_SIZE;
        // A second txn that inserts more; freed pages from txn 1 are now reusable.
        for i in 200..260u32 {
            w.insert(k(i * 10), k(i * 10 + 1), &[i as u8]).unwrap();
        }
        w.commit(2).unwrap();
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
            assert_eq!(
                w.record_count(),
                oracle.len() as u64,
                "count at step {step}"
            );
        }

        // The whole on-disk structure must pass the reader's full validation.
        w.commit(0).unwrap();
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
        w.commit(1).unwrap();
        let img = w.into_image();

        // Reopen the committed image, derive the free set, mutate, recommit.
        let mut w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(w2.record_count(), 500);
        for i in 0..250u32 {
            w2.delete(k(i * 10), k(i * 10 + 3)).unwrap();
        }
        w2.set(k(99_999), k(100_000), &[7]).unwrap();
        w2.commit(2).unwrap();

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
        w.commit(1).unwrap();
        let mut img = w.into_image();
        // Corrupt the active meta's checksum-covered bytes ⇒ both metas can't both be
        // valid / reachable tree mismatch ⇒ open_image must reject.
        let n = img.len();
        img[n - 100] ^= 0xFF; // a leaf-page byte
        assert!(Writer::<Ipv4Key>::open_image(img).is_err());
    }

    // --- crash recovery (§6.3): construct each post-crash on-disk state, verify the
    //     reader recovers old-or-new, never torn ---

    #[test]
    fn crash_before_meta_flip_keeps_old_tree() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        w.set(k(1), k(1), &[1]).unwrap();
        w.commit(1).unwrap(); // T1 = {[1,1]=1} durable
                              // A second txn writes new data pages but is NOT committed (crash before the
                              // meta flip): the active meta still points at T1; the new pages are orphans.
        w.set(k(2), k(2), &[2]).unwrap();
        let img = w.image().to_vec();
        let r = Reader::open(&img).unwrap();
        assert_eq!(r.record_count(), 1);
        assert_eq!(r.lookup_v4(k(1)).unwrap(), Some(&[1u8][..]));
        assert_eq!(r.lookup_v4(k(2)).unwrap(), None); // uncommitted set is invisible
    }

    #[test]
    fn crash_with_torn_new_meta_falls_back_to_old() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        w.set(k(1), k(1), &[1]).unwrap();
        w.commit(1).unwrap(); // T1
        w.set(k(2), k(2), &[2]).unwrap();
        w.commit(2).unwrap(); // T2 = {[1,1]=1, [2,2]=2}; active meta = the page just written
        let mut img = w.into_image();
        // Tear the active (higher-txn) meta — corrupt a checksum-covered byte, as a
        // crash mid-write of Barrier 2 would. The reader MUST fall back to the previous
        // valid meta and recover T1 intact (its pages were freed but not yet reused).
        let txn0 = Meta::decode(&img[..PAGE_SIZE]).txn_id;
        let txn1 = Meta::decode(&img[PAGE_SIZE..2 * PAGE_SIZE]).txn_id;
        let active = if txn0 >= txn1 { 0 } else { 1 };
        img[active * PAGE_SIZE + 64] ^= 0xFF;
        let r = Reader::open(&img).unwrap();
        assert_eq!(r.record_count(), 1); // recovered T1
        assert_eq!(r.lookup_v4(k(1)).unwrap(), Some(&[1u8][..]));
        assert_eq!(r.lookup_v4(k(2)).unwrap(), None);
    }

    #[test]
    fn committed_new_meta_yields_new_tree() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        w.set(k(1), k(1), &[1]).unwrap();
        w.commit(1).unwrap();
        w.set(k(2), k(2), &[2]).unwrap();
        w.commit(2).unwrap(); // crash *after* Barrier 2 ⇒ the new tree is durable
        let img = w.into_image();
        let r = Reader::open(&img).unwrap();
        assert_eq!(r.record_count(), 2);
        assert_eq!(r.lookup_v4(k(2)).unwrap(), Some(&[2u8][..]));
    }

    #[test]
    fn open_image_refuses_newer_minor() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        w.set(k(1), k(2), &[1]).unwrap();
        w.commit(1).unwrap();
        let mut img = w.into_image();
        // Bump BOTH metas' version_minor to 2 — a minor NEWER than this writer implements
        // (it implements up to 1, the v4.1 metadata system). Re-checksum: a forward-compat
        // file the reader accepts read-only, but which the writer MUST refuse to mutate
        // (§5.1/§C.6).
        for p in 0..2 {
            let page = &mut img[p * PAGE_SIZE..(p + 1) * PAGE_SIZE];
            page[spec::META_VERSION_MINOR..spec::META_VERSION_MINOR + 2]
                .copy_from_slice(&2u16.to_le_bytes());
            finalize_checksum(page);
        }
        assert!(
            Reader::open(&img).is_ok(),
            "reader accepts a newer minor (forward-compat)"
        );
        assert!(
            matches!(
                Writer::<Ipv4Key>::open_image(img),
                Err(Error::InvalidInput(_))
            ),
            "writer must refuse to mutate a newer-minor file"
        );
    }

    #[test]
    fn oracle_random_set_delete_v6() {
        // Same oracle, IPv6 keys (16-byte records / 20-byte branch entries) — exercises
        // the v6 leaf/branch offset arithmetic through splits and merges.
        use crate::key::Ipv6Key;
        let k6 = |n: u32| Ipv6Key {
            hi: 0,
            lo: n as u64,
        };
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
        w.commit(0).unwrap();
        let img = w.into_image();
        let r = Reader::open(&img).unwrap();
        assert_eq!(r.record_count(), oracle.len() as u64);
    }

    // --- v4.1 scope registry tests ---

    fn active_meta_of(img: &[u8]) -> Meta {
        let a = Meta::decode(&img[..PAGE_SIZE]);
        let b = Meta::decode(&img[PAGE_SIZE..2 * PAGE_SIZE]);
        if b.txn_id > a.txn_id {
            b
        } else {
            a
        }
    }

    #[test]
    fn scope_registry_roundtrip() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"feed-a").unwrap();
        let b = w.scope_define(b"feed-b").unwrap();
        assert_eq!((a, b), (1, 2)); // monotonic; 0 reserved for FILE
        w.scope_set_type(a, 2).unwrap();
        w.scope_bump_version(a).unwrap();
        w.scope_bump_version(a).unwrap();
        w.scope_set_version(b, 100).unwrap();
        w.set(k(10), k(20), &[7]).unwrap(); // IP tree coexists
        w.commit(1).unwrap();
        let img = w.into_image();

        let r = Reader::open(&img).unwrap(); // validates the v4.1 file incl. scope table
        assert_eq!(r.lookup_v4(k(15)).unwrap(), Some(&[7u8][..]));

        let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(w2.scope_name(a), Some(b"feed-a".to_vec()));
        assert_eq!(w2.scope_name(b), Some(b"feed-b".to_vec()));
        assert_eq!(w2.scope_type(a), Some(2));
        assert_eq!(w2.scope_version(a), Some(2));
        assert_eq!(w2.scope_version(b), Some(100));
        assert_eq!(w2.scope_list().len(), 2);
        assert_eq!(w2.scope_name(999), None);
    }

    #[test]
    fn metadata_upgrades_to_v41_and_empty_stays_v40() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        w.set(k(1), k(2), &[1]).unwrap();
        w.commit(1).unwrap();
        let img = w.image().to_vec();
        let active = active_meta_of(&img);
        assert_eq!(active.version_minor, spec::VERSION_MINOR); // no metadata ⇒ v4.0
        assert_eq!(active.scope_table_root, 0);

        let _ = w.scope_define(b"x").unwrap();
        w.commit(2).unwrap();
        let img = w.into_image();
        let active = active_meta_of(&img);
        assert_eq!(active.version_minor, spec::VERSION_MINOR_METADATA);
        assert_eq!(active.meta_size, spec::META_SIZE_V41);
        assert!(active.scope_table_root >= 2);
        assert!(Reader::open(&img).is_ok());
    }

    #[test]
    fn dropping_all_scopes_returns_to_v40() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"a").unwrap();
        w.commit(1).unwrap();
        w.scope_drop(a).unwrap();
        w.commit(2).unwrap();
        let img = w.into_image();
        let active = active_meta_of(&img);
        assert_eq!(active.scope_table_root, 0);
        assert_eq!(active.version_minor, spec::VERSION_MINOR);
        assert!(Reader::open(&img).is_ok());
    }

    #[test]
    fn scope_drop_removes_metadata_and_rejects_file_scope() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"a").unwrap();
        let b = w.scope_define(b"b").unwrap();
        assert!(matches!(
            w.scope_drop(spec::FILE_SCOPE_ID),
            Err(Error::InvalidInput(_))
        ));
        assert!(w.scope_drop(a).unwrap());
        assert!(!w.scope_drop(a).unwrap()); // already gone
        let c = w.scope_define(b"c").unwrap();
        assert_eq!(c, 3); // dropped id 1 is NOT reused
        w.commit(1).unwrap();
        let img = w.into_image();
        let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(w2.scope_name(a), None);
        assert_eq!(w2.scope_name(b), Some(b"b".to_vec()));
        assert_eq!(w2.scope_name(c), Some(b"c".to_vec()));
    }

    #[test]
    fn many_scopes_force_scope_tree_and_validate() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let n = 100u32; // > scope_leaf_max (14) ⇒ a multi-level scope tree
        for i in 0..n {
            let name = alloc::format!("scope-{i}");
            let id = w.scope_define(name.as_bytes()).unwrap();
            assert_eq!(id, i + 1);
            w.scope_set_version(id, i as u64).unwrap();
        }
        w.commit(1).unwrap();
        let img = w.into_image();
        Reader::open(&img).unwrap(); // full validation of the multi-level scope tree
        let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(w2.scope_list().len(), n as usize);
        assert_eq!(w2.scope_name(50), Some(b"scope-49".to_vec()));
        assert_eq!(w2.scope_version(100), Some(99));
    }

    #[test]
    fn scope_name_validation() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        assert!(matches!(
            w.scope_define(&[0xff, 0xfe]),
            Err(Error::InvalidInput(_))
        )); // not UTF-8
        assert!(matches!(
            w.scope_define(&[b'a'; 257]),
            Err(Error::InvalidInput(_))
        )); // too long
        assert!(w.scope_define(&[b'a'; 256]).is_ok()); // exactly 256 OK
    }

    // --- v4.1 per-scope KV tests (§C.4) ---

    #[test]
    fn kv_crud_and_reopen_roundtrip() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"feed-a").unwrap();
        // KV on a defined scope and on FILE (target 0).
        w.meta_set(a, b"license", 0, b"MIT").unwrap();
        w.meta_set(a, b"category", 0, b"malware").unwrap();
        w.meta_set(spec::FILE_SCOPE_ID, b"dataset", 0, b"blocklist-ipsets")
            .unwrap();
        // Binary (non-zero type) value stored unchecked.
        w.meta_set(a, b"blob", 7, &[0u8, 1, 2, 0xff]).unwrap();
        w.set(k(10), k(20), &[1]).unwrap(); // IP tree coexists
        w.commit(1).unwrap();
        let img = w.into_image();

        let r = Reader::open(&img).unwrap(); // validates KV trees too
        assert_eq!(r.lookup_v4(k(15)).unwrap(), Some(&[1u8][..]));

        let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(
            w2.meta_get(a, b"license").unwrap(),
            Some((0, b"MIT".to_vec()))
        );
        assert_eq!(
            w2.meta_get(a, b"category").unwrap(),
            Some((0, b"malware".to_vec()))
        );
        assert_eq!(
            w2.meta_get(spec::FILE_SCOPE_ID, b"dataset").unwrap(),
            Some((0, b"blocklist-ipsets".to_vec()))
        );
        assert_eq!(
            w2.meta_get(a, b"blob").unwrap(),
            Some((7, vec![0u8, 1, 2, 0xff]))
        );
        // Ordered list.
        let listed: Vec<Vec<u8>> = w2.meta_list(a).unwrap().into_iter().map(|e| e.0).collect();
        assert_eq!(
            listed,
            vec![b"blob".to_vec(), b"category".to_vec(), b"license".to_vec()]
        );
    }

    #[test]
    fn kv_overwrite_and_delete() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"a").unwrap();
        w.meta_set(a, b"k", 0, b"v1").unwrap();
        w.meta_set(a, b"k", 0, b"v2").unwrap(); // overwrite same key
        assert_eq!(w.meta_get(a, b"k").unwrap(), Some((0, b"v2".to_vec())));
        // delete present -> Changed; absent -> Unchanged.
        assert_eq!(w.meta_delete(a, b"k").unwrap(), Changed::Changed);
        assert_eq!(w.meta_get(a, b"k").unwrap(), None);
        assert_eq!(w.meta_delete(a, b"k").unwrap(), Changed::Unchanged);
        assert_eq!(w.meta_delete(a, b"never").unwrap(), Changed::Unchanged);
        w.commit(1).unwrap();
        let img = w.into_image();
        let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(w2.meta_get(a, b"k").unwrap(), None);
    }

    #[test]
    fn kv_large_value_overflow_chain() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"a").unwrap();
        // A multi-page value (3+ overflow pages). Non-zero type so arbitrary bytes are OK.
        let big: Vec<u8> = (0..(spec::OVERFLOW_PAYLOAD * 2 + 123))
            .map(|i| (i * 31 + 7) as u8)
            .collect();
        w.meta_set(a, b"payload", 9, &big).unwrap();
        // A value exactly on the inline/overflow boundary stays inline; one past it spills.
        let on_boundary = vec![0xABu8; spec::KV_INLINE_MAX];
        let past_boundary = vec![0xCDu8; spec::KV_INLINE_MAX + 1];
        w.meta_set(a, b"edge_in", 1, &on_boundary).unwrap();
        w.meta_set(a, b"edge_out", 1, &past_boundary).unwrap();
        w.commit(1).unwrap();
        let img = w.into_image();
        let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(w2.meta_get(a, b"payload").unwrap(), Some((9, big)));
        assert_eq!(w2.meta_get(a, b"edge_in").unwrap(), Some((1, on_boundary)));
        assert_eq!(
            w2.meta_get(a, b"edge_out").unwrap(),
            Some((1, past_boundary))
        );
    }

    #[test]
    fn kv_empty_value_roundtrips() {
        // A zero-length value is valid (stored inline) for both text and binary types.
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"a").unwrap();
        w.meta_set(a, b"empty-text", 0, b"").unwrap();
        w.meta_set(a, b"empty-bin", 3, b"").unwrap();
        w.commit(1).unwrap();
        let img = w.into_image();
        let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(
            w2.meta_get(a, b"empty-text").unwrap(),
            Some((0, Vec::new()))
        );
        assert_eq!(w2.meta_get(a, b"empty-bin").unwrap(), Some((3, Vec::new())));
    }

    #[test]
    fn kv_many_entries_force_multi_level_tree() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"a").unwrap();
        let n = 2000u32;
        for i in 0..n {
            let key = alloc::format!("key-{i:06}");
            let val = alloc::format!("value-for-{i}");
            w.meta_set(a, key.as_bytes(), 0, val.as_bytes()).unwrap();
        }
        w.commit(1).unwrap();
        let img = w.into_image();
        Reader::open(&img).unwrap(); // full validation of the multi-level KV tree
        let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(w2.meta_list(a).unwrap().len(), n as usize);
        // spot-check across the tree
        for &i in &[0u32, 1, 777, 1999] {
            let key = alloc::format!("key-{i:06}");
            let val = alloc::format!("value-for-{i}");
            assert_eq!(
                w2.meta_get(a, key.as_bytes()).unwrap(),
                Some((0, val.into_bytes())),
                "i={i}"
            );
        }
        // entries are returned in key order
        let keys: Vec<Vec<u8>> = w2.meta_list(a).unwrap().into_iter().map(|e| e.0).collect();
        assert!(keys.windows(2).all(|w| w[0] < w[1]), "keys sorted");
    }

    #[test]
    fn kv_type0_utf8_validation() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"a").unwrap();
        // type==0 rejects invalid UTF-8 / NUL (inline).
        assert!(matches!(
            w.meta_set(a, b"x", 0, &[0xff, 0xfe]),
            Err(Error::InvalidInput(_))
        ));
        assert!(matches!(
            w.meta_set(a, b"x", 0, b"has\0nul"),
            Err(Error::InvalidInput(_))
        ));
        // type==0 rejects invalid bytes even when the value spans an overflow chain.
        let mut bad_big = vec![b'a'; spec::OVERFLOW_PAYLOAD + 10];
        bad_big[spec::OVERFLOW_PAYLOAD + 5] = 0xff; // invalid UTF-8 byte past page 1
        assert!(matches!(
            w.meta_set(a, b"x", 0, &bad_big),
            Err(Error::InvalidInput(_))
        ));
        // valid UTF-8 text accepted; non-zero type stores arbitrary bytes unchecked.
        w.meta_set(a, b"ok", 0, "héllo-✓".as_bytes()).unwrap();
        w.meta_set(a, b"bin", 5, &[0xff, 0x00, 0xfe]).unwrap();
        w.commit(1).unwrap();
        let img = w.into_image();
        let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(
            w2.meta_get(a, b"ok").unwrap(),
            Some((0, "héllo-✓".as_bytes().to_vec()))
        );
        assert_eq!(
            w2.meta_get(a, b"bin").unwrap(),
            Some((5, vec![0xff, 0x00, 0xfe]))
        );
    }

    #[test]
    fn kv_key_validation_and_missing() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"a").unwrap();
        assert!(matches!(
            w.meta_set(a, b"", 0, b"v"),
            Err(Error::InvalidInput(_))
        )); // empty key
        assert!(matches!(
            w.meta_set(a, &[b'k'; 1025], 0, b"v"),
            Err(Error::InvalidInput(_))
        )); // > 1024
        assert!(matches!(
            w.meta_set(a, b"a\0b", 0, b"v"),
            Err(Error::InvalidInput(_))
        )); // NUL in key
        assert!(matches!(
            w.meta_set(a, &[0xff, 0xfe], 0, b"v"),
            Err(Error::InvalidInput(_))
        )); // non-UTF-8 key
        assert!(w.meta_set(a, &[b'k'; 1024], 0, b"v").is_ok()); // exactly 1024 OK
                                                                // missing key -> Ok(None)
        assert_eq!(w.meta_get(a, b"absent").unwrap(), None);
        // meta_set on an undefined scope -> InvalidInput
        assert!(matches!(
            w.meta_set(999, b"k", 0, b"v"),
            Err(Error::InvalidInput(_))
        ));
        // FILE target is always valid.
        assert!(w.meta_set(spec::FILE_SCOPE_ID, b"k", 0, b"v").is_ok());
    }

    #[test]
    fn kv_rewrite_reuses_freed_pages() {
        // The KV rebuild frees the old KV+overflow pages at commit (into `freed_this_txn`),
        // which D7 reclaims only at the FOLLOWING commit (so a reader on the old meta keeps
        // a stable snapshot). So a steady-state rewrite stops growing the file after the
        // second rewrite reuses the pages freed by the first (mirrors the IP-tree reclaim
        // test, where the freeing/alloc straddle two commits).
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"a").unwrap();
        let rewrite = |w: &mut Writer<Ipv4Key>, txn: u64| {
            for i in 0..500u32 {
                let key = alloc::format!("k{i:04}");
                w.meta_set(a, key.as_bytes(), 1, &vec![(i & 0xff) as u8; 600])
                    .unwrap();
            }
            w.commit(txn).unwrap();
        };
        rewrite(&mut w, 1);
        rewrite(&mut w, 2); // frees txn1's KV pages (reclaimable from txn 3 on)
        let pages_after_second = w.image().len() / PAGE_SIZE;
        rewrite(&mut w, 3); // reuses pages freed at commit 2
        let pages_after_third = w.image().len() / PAGE_SIZE;
        assert!(
            pages_after_third <= pages_after_second + 4,
            "steady-state KV rewrite must reuse freed pages: {pages_after_second} -> {pages_after_third}"
        );
        let img = w.into_image();
        let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(w2.meta_list(a).unwrap().len(), 500);
    }

    #[test]
    fn kv_scope_drop_frees_kv_and_overflow() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"a").unwrap();
        let b = w.scope_define(b"b").unwrap();
        // Give `a` a big KV (forces overflow pages), `b` a small one.
        w.meta_set(a, b"big", 1, &vec![0x5Au8; spec::OVERFLOW_PAYLOAD * 2])
            .unwrap();
        w.meta_set(b, b"small", 0, b"x").unwrap();
        w.commit(1).unwrap();
        // Drop `a`: its KV + overflow pages are freed at commit 2 (reclaimable from commit
        // 3 on, per D7). Then add data that must reuse them by commit 3.
        w.scope_drop(a).unwrap();
        w.commit(2).unwrap();
        let pages_after_drop = w.image().len() / PAGE_SIZE;
        for i in 0..40u32 {
            w.meta_set(b, alloc::format!("n{i}").as_bytes(), 1, &[7u8; 100])
                .unwrap();
        }
        w.commit(3).unwrap();
        let pages_after_refill = w.image().len() / PAGE_SIZE;
        assert!(
            pages_after_refill <= pages_after_drop + 2,
            "dropped scope's KV/overflow must be reused: {pages_after_drop} -> {pages_after_refill}"
        );
        let img = w.into_image();
        let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
        assert_eq!(w2.scope_name(a), None);
        assert_eq!(w2.meta_get(b, b"small").unwrap(), Some((0, b"x".to_vec())));
        assert_eq!(w2.meta_get(a, b"big").unwrap(), None); // a is gone
    }

    #[test]
    fn kv_many_sets_one_rebuild_and_file_empty_returns_v40() {
        // Many meta_set on FILE in one txn must persist (one rebuild), and a file whose
        // only metadata is later removed returns to v4.0.
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        w.meta_set(spec::FILE_SCOPE_ID, b"a", 0, b"1").unwrap();
        w.meta_set(spec::FILE_SCOPE_ID, b"b", 0, b"2").unwrap();
        w.meta_set(spec::FILE_SCOPE_ID, b"c", 0, b"3").unwrap();
        w.commit(1).unwrap();
        let img = w.image().to_vec();
        let active = active_meta_of(&img);
        assert_eq!(active.version_minor, spec::VERSION_MINOR_METADATA);
        assert_eq!(w.meta_list(spec::FILE_SCOPE_ID).unwrap().len(), 3);

        // Delete all FILE keys -> FILE record dropped -> back to v4.0.
        w.meta_delete(spec::FILE_SCOPE_ID, b"a").unwrap();
        w.meta_delete(spec::FILE_SCOPE_ID, b"b").unwrap();
        w.meta_delete(spec::FILE_SCOPE_ID, b"c").unwrap();
        w.commit(2).unwrap();
        let img = w.into_image();
        let active = active_meta_of(&img);
        assert_eq!(active.scope_table_root, 0);
        assert_eq!(active.version_minor, spec::VERSION_MINOR);
        assert!(Reader::open(&img).is_ok());
    }

    #[test]
    fn kv_file_set_then_delete_same_txn_is_noop() {
        // codex Finding 2 (Rust↔Go parity): setting a FILE key then deleting it in the SAME
        // txn must leave the file at v4.0 — the key never persists. The Rust writer used to
        // short-circuit meta_delete on the not-yet-committed FILE target (it checked only the
        // committed root + registry, not `kv_dirty`) and wrongly kept the key; Go was correct.
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        w.meta_set(spec::FILE_SCOPE_ID, b"k", 0, b"v").unwrap();
        assert_eq!(
            w.meta_delete(spec::FILE_SCOPE_ID, b"k").unwrap(),
            Changed::Changed
        );
        // Deleting again (now genuinely absent) is a clean no-op.
        assert_eq!(
            w.meta_delete(spec::FILE_SCOPE_ID, b"k").unwrap(),
            Changed::Unchanged
        );
        w.commit(1).unwrap();
        assert!(w.meta_list(spec::FILE_SCOPE_ID).unwrap().is_empty());
        let img = w.image().to_vec();
        let active = active_meta_of(&img);
        assert_eq!(active.scope_table_root, 0, "no metadata should persist");
        assert_eq!(active.version_minor, spec::VERSION_MINOR, "must stay v4.0");
        assert!(Reader::open(&img).is_ok());
    }

    #[test]
    fn poisoned_writer_refuses_ops_and_leaves_disk_unchanged() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        w.set(k(10), k(20), &[1]).unwrap();
        w.commit(0).unwrap();
        let committed = w.image().to_vec(); // the last committed on-disk state
                                            // Simulate a failed-commit poison (the rebuild phase does irreversible page work).
        w.poisoned = true;
        // Every mutating op + commit now refuses with State, mutating nothing.
        assert!(matches!(w.set(k(30), k(40), &[2]), Err(Error::State(_))));
        assert!(matches!(w.delete(k(10), k(15)), Err(Error::State(_))));
        assert!(matches!(w.scope_define(b"x"), Err(Error::State(_))));
        assert!(matches!(w.meta_set(0, b"k", 0, b"v"), Err(Error::State(_))));
        assert!(matches!(w.commit(0), Err(Error::State(_))));
        // The on-disk image is byte-identical to the last commit and reopens as that state.
        assert_eq!(w.image(), committed.as_slice());
        let r = Reader::open(&committed).unwrap();
        assert_eq!(r.lookup_v4(k(15)).unwrap(), Some(&[1u8][..]));
    }

    #[test]
    fn overflow_text_value_multibyte_straddles_page_boundary() {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let s = w.scope_define(b"feed").unwrap();
        // A 3-byte € (E2 82 AC) placed so its lead byte is the last byte of the first overflow
        // page's payload and its continuations open the second page. The value exceeds the
        // inline cap, so it is stored as an overflow chain and validate streams it.
        let mut value = vec![b'a'; spec::OVERFLOW_PAYLOAD - 1];
        value.extend_from_slice("€".as_bytes());
        value.extend_from_slice(b"tail");
        assert!(value.len() > spec::KV_INLINE_MAX, "value must overflow");
        w.meta_set(s, b"k", spec::KV_TYPE_TEXT, &value).unwrap();
        w.commit(0).unwrap();
        let img = w.image().to_vec();
        // Reader::open runs the streaming validate over the boundary-straddling char.
        let r = Reader::open(&img).unwrap();
        // The read path still materializes the exact value.
        assert_eq!(
            r.meta_get(s, b"k").unwrap(),
            Some((spec::KV_TYPE_TEXT, value))
        );
    }
}
