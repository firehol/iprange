//! The v4.3 writer: COW B+tree mutation over a writable mmap, zero heap allocation
//! in the hot path.
//!
//! COW copies live in the file's growth region `[committed_pages, total_pages)`.
//! Committed pages are never modified in-place (except meta pages 0/1, which are
//! the atomic commit point). `commit` finalizes CRCs, writes the new meta page
//! (alternating 0/1) in-place, and syncs.

use alloc::boxed::Box;
use core::marker::PhantomData;

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::node::{BranchView, LeafView};
use crate::page_store::{PageStore, VecPageStore};
use crate::reader::Reader;
use crate::record::{self, record_size};
use crate::spec::{self, PAGE_HEADER_SIZE, PAGE_SIZE};
use crate::wire::{finalize_checksum, put_u32, put_u64, u32_le, u64_le, Meta, PageHeader};

/// One KV entry as returned by `meta_list`.
pub type MetaEntry = (alloc::vec::Vec<u8>, u32, alloc::vec::Vec<u8>);

/// What a delete did.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Changed {
    Changed,
    Unchanged,
}

pub struct Writer<K: IpKey> {
    pub(crate) store: Box<dyn PageStore>,
    key_width: u8,
    scope_mode: u8,
    created_unixtime: u64,
    active_meta: u32,
    pub(crate) committed_root: u32,
    pub(crate) committed_height: u32,
    committed_pages: u32,
    committed_record_count: u64,
    committed_txn_id: u64,
    free_list_head: u32,
    pending_root: u32,
    pending_height: u32,
    pending_record_count: u64,
    poisoned: bool,
    // Txn-free tracking: pages freed (superseded by COW) during this transaction.
    // Stored in a fixed-size array (Rule 1: no heap). When full, spills to a
    // TXN_FREE page chain in the growth region.
    txn_free_buf: [u32; 64], // 64 freed pgnos before spilling to a page
    txn_free_count: usize,   // entries in txn_free_buf
    txn_free_chain: u32,     // head of TXN_FREE page chain (0 = none)
    // Reusable pages from the committed free-list (read at open time).
    // Same fixed-array approach.
    reuse_buf: [u32; 64],
    reuse_count: usize,
    /// Pages freed in txn_id <= this are safe to reclaim (from reader registration).
    /// 0 = no readers registered → only reclaim growth-region frees.
    safe_reclaim_txn_id: u64,
    /// Scope registry for mode 2 (indirect). None for modes 0/1.
    scope_registry: Option<crate::scope_table::ScopeRegistry>,
    scope_table_root_cache: u32,
    /// When true, alloc_or_reuse skips txn_free_buf (migration safety).
    migration_mode: bool,
    _k: PhantomData<K>,
}

impl<K: IpKey> Writer<K> {
    // ── construction ──────────────────────────────────────────────────

    pub fn create(scope_mode: u8, created_unixtime: u64) -> Result<Writer<K>> {
        let store: Box<dyn PageStore> =
            Box::new(VecPageStore::new(alloc::vec![0u8; 2 * PAGE_SIZE]));
        let mut w = Writer {
            store,
            key_width: K::WIDTH as u8,
            scope_mode,
            created_unixtime,
            active_meta: 0,
            committed_root: 0,
            committed_height: 0,
            committed_pages: 2,
            committed_record_count: 0,
            committed_txn_id: 0,
            free_list_head: 0,
            pending_root: 0,
            pending_height: 0,
            pending_record_count: 0,
            poisoned: false,
            txn_free_buf: [0u32; 64],
            txn_free_count: 0,
            txn_free_chain: 0,
            reuse_buf: [0u32; 64],
            reuse_count: 0,
            safe_reclaim_txn_id: 0,
            scope_registry: if scope_mode == spec::SCOPE_MODE_INDIRECT {
                Some(crate::scope_table::ScopeRegistry::new())
            } else { None },
            scope_table_root_cache: 0,
            migration_mode: false,
            _k: PhantomData,
        };
        w.write_meta_page(0, 1, 0, 0, 0, 2, 0)?;
        w.write_meta_page(1, 0, 0, 0, 0, 2, 0)?;
        w.active_meta = 0;
        w.committed_txn_id = 1;
        Ok(w)
    }

    pub fn open(store: Box<dyn PageStore>) -> Result<Writer<K>> {
        let meta_a = Meta::decode(store.page(0));
        let meta_b = Meta::decode(store.page(1));
        let (active, active_no) = if meta_a.txn_id >= meta_b.txn_id {
            (meta_a, 0u32)
        } else {
            (meta_b, 1u32)
        };
        if active.key_width != K::WIDTH as u8 {
            return Err(Error::Structural("key_width mismatch"));
        }
        if active.record_size != record_size::<K>() as u32 {
            return Err(Error::Structural("record_size mismatch"));
        }
        // Read scope table entries BEFORE moving store into Writer.
        let scope_reg = if active.scope_mode == spec::SCOPE_MODE_INDIRECT && active.scope_table_root != 0 {
            let entries = crate::scope_table::read_all(
                store.committed_bytes(), active.scope_table_root,
            ).unwrap_or_default();
            Some(crate::scope_table::ScopeRegistry::from_entries(entries))
        } else if active.scope_mode == spec::SCOPE_MODE_INDIRECT {
            Some(crate::scope_table::ScopeRegistry::new())
        } else { None };

        let mut w = Writer {
            store,
            key_width: active.key_width,
            scope_mode: active.scope_mode,
            created_unixtime: active.created_unixtime,
            active_meta: active_no,
            committed_root: active.root_pgno,
            committed_height: active.tree_height,
            committed_pages: active.total_pages as u32,
            committed_record_count: active.record_count,
            committed_txn_id: active.txn_id,
            free_list_head: active.free_list_head,
            pending_root: active.root_pgno,
            pending_height: active.tree_height,
            pending_record_count: active.record_count,
            poisoned: false,
            txn_free_buf: [0u32; 64],
            txn_free_count: 0,
            txn_free_chain: 0,
            reuse_buf: [0u32; 64],
            reuse_count: 0,
            safe_reclaim_txn_id: 0,
            scope_registry: scope_reg,
            scope_table_root_cache: active.scope_table_root,
            migration_mode: false,
            _k: PhantomData,
        };
        // Load the committed free-list for page reuse.
        w.load_free_list();
        Ok(w)
    }

    // ── public hot-path API ───────────────────────────────────────────

    pub fn set(&mut self, from: K, to: K, scope_id: u32) -> Result<()> {
        self.check()?;
        if from > to {
            return Err(Error::InvalidInput("from > to"));
        }
        self.delete_range(from, to)?;
        self.cow_insert(from, to, scope_id)?;
        self.pending_record_count += 1;
        Ok(())
    }

    pub fn delete(&mut self, from: K, to: K) -> Result<Changed> {
        self.check()?;
        if from > to {
            return Err(Error::InvalidInput("from > to"));
        }
        let before = self.pending_record_count;
        self.delete_range(from, to)?;
        Ok(if self.pending_record_count < before {
            Changed::Changed
        } else {
            Changed::Unchanged
        })
    }

    pub fn append(&mut self, from: K, to: K, scope_id: u32) -> Result<()> {
        self.check()?;
        if from > to {
            return Err(Error::InvalidInput("from > to"));
        }
        self.cow_insert(from, to, scope_id)?;
        self.pending_record_count += 1;
        Ok(())
    }

    pub fn commit(&mut self, updated_unixtime: u64) -> Result<()> {
        self.check()?;
        // Flush any remaining freed pages.
        if self.txn_free_count > 0 {
            self.build_free_list_linked()?;
        }
        self.free_list_head = self.txn_free_chain;

        // Rebuild scope table (mode 2 only).
        // Free old scope table pages so they're reclaimed.
        if self.scope_table_root_cache != 0 {
            self.free_scope_table_pages(self.scope_table_root_cache);
        }
        self.scope_table_root_cache = if let Some(ref reg) = self.scope_registry {
            if reg.is_empty() { 0 } else {
                crate::scope_table::build_scope_tree(self.store.as_mut(), reg.entries())?
            }
        } else { 0 };

        let total = self.store.total_pages();
        for pgno in self.committed_pages..total {
            finalize_checksum(self.store.page_mut(pgno));
        }
        let inactive = 1 - self.active_meta;
        let new_txn_id = self.committed_txn_id + 1;
        self.write_meta_page(
            inactive, new_txn_id, self.pending_root, self.pending_height,
            self.pending_record_count, total, updated_unixtime,
        )?;
        self.store.sync()?;
        self.active_meta = inactive;
        self.committed_txn_id = new_txn_id;
        self.committed_root = self.pending_root;
        self.committed_height = self.pending_height;
        self.committed_record_count = self.pending_record_count;
        self.committed_pages = total;
        self.store.set_committed_pages(total);
        // Reset txn-free tracking for the next transaction.
        self.txn_free_count = 0;
        self.txn_free_chain = 0;
        // Load the committed free-list for reuse in the next transaction.
        self.load_free_list();
        Ok(())
    }

    /// Read the committed free-list and populate the reuse buffer.
    /// The free-list is a linked list in freed pages: @0 next_free_pgno (u32).
    /// These pages are safe to reuse (they were freed in a prior committed txn,
    /// and no active reader needs them — reader coordination is Phase 3).
    /// Read the committed free-list (TXN_FREE page chain) and populate the
    /// reuse buffer with freed DATA pages that are safe to reclaim.
    /// Consumed TXN_FREE metadata pages are themselves recycled into reuse_buf.
    fn load_free_list(&mut self) {
        self.reuse_count = 0;
        let mut meta_pgno = self.free_list_head;
        let mut guard = 0u32;
        while meta_pgno != 0 && guard < spec::TREE_HEIGHT_MAX {
            guard += 1;
            if meta_pgno as u64 >= self.store.total_pages() as u64 {
                break;
            }
            let page = self.store.page(meta_pgno);
            let h = PageHeader::decode(page);
            if h.page_type != spec::PAGE_TYPE_TXN_FREE {
                break;
            }
            let next_meta = u32_le(page, spec::TXN_FREE_NEXT);
            let count = u32_le(page, spec::TXN_FREE_COUNT) as usize;
            let freed_in = u64_le(page, spec::TXN_FREE_FREED_IN);
            let safe = self.safe_reclaim_txn_id == 0 || freed_in < self.safe_reclaim_txn_id;
            if safe {
                // Load freed DATA pages into reuse_buf.
                for i in 0..count {
                    if self.reuse_count >= self.reuse_buf.len() {
                        self.free_list_head = meta_pgno;
                        return;
                    }
                    let freed_pgno = u32_le(page, spec::TXN_FREE_ARRAY + i * 4);
                    self.reuse_buf[self.reuse_count] = freed_pgno;
                    self.reuse_count += 1;
                }
                // The TXN_FREE metadata page itself is now consumed → recycle it.
                if self.reuse_count < self.reuse_buf.len() {
                    self.reuse_buf[self.reuse_count] = meta_pgno;
                    self.reuse_count += 1;
                } else {
                    self.free_list_head = meta_pgno;
                    return;
                }
            } else {
                self.free_list_head = meta_pgno;
                return;
            }
            meta_pgno = next_meta;
        }
        self.free_list_head = 0;
    }

    pub fn reader(&self) -> Result<Reader<'_>> {
        Reader::open(self.store.committed_bytes())
    }

    pub fn scan(&self, mut f: impl FnMut(K, K, u32)) -> Result<()> {
        if self.pending_root == 0 {
            return Ok(());
        }
        self.scan_node(self.pending_root, &mut f)
    }

    pub fn record_count(&self) -> u64 {
        self.pending_record_count
    }

    /// Accessors for the streaming migrate (zero-copy old-tree scan).
    pub(crate) fn store_ref(&self) -> &dyn PageStore {
        self.store.as_ref()
    }
    pub(crate) fn pending_root_ref(&self) -> u32 {
        self.pending_root
    }
    pub(crate) fn pending_height_ref(&self) -> u32 {
        self.pending_height
    }

    pub fn into_image(self) -> Option<alloc::vec::Vec<u8>> {
        unsafe {
            let raw: *mut dyn PageStore = Box::into_raw(self.store);
            match raw.cast::<VecPageStore>().as_mut() {
                Some(s) => Some(core::ptr::read(s).into_vec()),
                None => {
                    drop(Box::from_raw(raw));
                    None
                }
            }
        }
    }

    // ── COW mechanics ─────────────────────────────────────────────────

    #[inline]
    fn check(&self) -> Result<()> {
        if self.poisoned {
            Err(Error::State("writer poisoned"))
        } else {
            Ok(())
        }
    }

    /// Enable/disable migration mode. When enabled, alloc_or_reuse skips
    /// intra-transaction reuse to prevent the COW-reuse hazard during
    /// streaming migration (TreeWalker reads committed pages that could
    /// be overwritten if reused).
    pub(crate) fn set_migration_mode(&mut self, enabled: bool) {
        self.migration_mode = enabled;
    }

    /// Set the oldest reader's txn_id. Pages freed in txn_id <= this value
    /// are safe to reclaim. Called by the OS layer from ReaderTable::oldest_reader_txn_id().
    /// 0 means no active readers (reclaim everything from prior committed txns).
    pub fn set_safe_reclaim_txn_id(&mut self, txn_id: u64) {
        self.safe_reclaim_txn_id = txn_id;
    }

    #[inline]
    fn cow_page(&mut self, pgno: u32) -> Result<u32> {
        if pgno >= self.committed_pages {
            return Ok(pgno);
        }
        let new = self.alloc_or_reuse()?;
        self.store.copy_page(pgno, new);
        // The old committed page is now freed (superseded by COW).
        self.track_freed(pgno)?;
        Ok(new)
    }

    /// Allocate a new page, preferring reused (freed) pages over growth.
    /// Checks three sources in order:
    /// 1. txn_free_buf (pages freed THIS transaction — safe, not reachable from pending root)
    /// 2. reuse_buf (pages freed in prior committed transactions — loaded at open/commit)
    /// 3. growth region (allocate a new page at the end of the file)
    #[inline]
    fn alloc_or_reuse(&mut self) -> Result<u32> {
        // 1. Intra-transaction reuse: pages freed earlier in this same txn.
        //    SKIPPED during migration (migration_mode) to prevent COW-reuse
        //    hazard: the TreeWalker reads committed pages that could be
        //    reused and overwritten, corrupting the scan.
        if !self.migration_mode && self.txn_free_count > 0 {
            self.txn_free_count -= 1;
            return Ok(self.txn_free_buf[self.txn_free_count]);
        }
        // 2. Cross-transaction reuse: pages freed in prior committed txns.
        if self.reuse_count > 0 {
            self.reuse_count -= 1;
            return Ok(self.reuse_buf[self.reuse_count]);
        }
        // 3. Growth: allocate at the end of the file.
        self.store.alloc_page()
    }

    /// Track a freed page (COW victim). Records ALL freed pages including
    /// committed-prefix ones. Reclaim safety is checked at load time via
    /// freed_in_txn vs safe_reclaim_txn_id.
    #[inline]
    fn track_freed(&mut self, pgno: u32) -> Result<()> {
        if self.txn_free_count < self.txn_free_buf.len() {
            self.txn_free_buf[self.txn_free_count] = pgno;
            self.txn_free_count += 1;
        } else {
            self.build_free_list_linked()?;
            self.txn_free_buf[0] = pgno;
            self.txn_free_count = 1;
        }
        Ok(())
    }

    /// Build the committed free-list as a linked list IN the freed pages.
    /// Each freed page stores next_free_pgno at offset PAGE_HEADER_SIZE.
    /// free_list_head points to the first freed page in this chain.
    /// Write the txn-free buffer into a TXN_FREE metadata page. These pages
    /// store arrays of freed pgnos and are never reused themselves — only
    /// the DATA pages they list are reusable.
    fn build_free_list_linked(&mut self) -> Result<()> {
        if self.txn_free_count == 0 {
            return Ok(());
        }
        let freed_in = self.committed_txn_id + 1;
        let meta_pgno = self.alloc_or_reuse()?;
        let page = self.store.page_mut(meta_pgno);
        page.fill(0);
        // @16: next TXN_FREE page in the chain (0 = end)
        put_u32(page, spec::TXN_FREE_NEXT, self.txn_free_chain);
        // @20: count of freed pgnos in this page
        put_u32(page, spec::TXN_FREE_COUNT, self.txn_free_count as u32);
        // @24: array of freed pgnos
        for i in 0..self.txn_free_count {
            put_u32(page, spec::TXN_FREE_ARRAY + i * 4, self.txn_free_buf[i]);
        }
        // Store freed_in_txn once per page (all entries share the same txn).
        put_u64(page, spec::TXN_FREE_FREED_IN, freed_in);
        PageHeader::write(page, spec::PAGE_TYPE_TXN_FREE, 0, meta_pgno);
        self.txn_free_chain = meta_pgno;
        self.txn_free_count = 0;
        Ok(())
    }

    #[inline]
    fn cow_root(&mut self) -> Result<u32> {
        if self.pending_root == 0 {
            return Ok(0);
        }
        let old = self.pending_root;
        let new = self.cow_page(old)?;
        if new != old {
            self.pending_root = new;
        }
        Ok(new)
    }

    // ── B+tree insert ─────────────────────────────────────────────────

    fn cow_insert(&mut self, from: K, to: K, scope_id: u32) -> Result<()> {
        if self.pending_root == 0 {
            let leaf = self.alloc_or_reuse()?;
            self.write_leaf_single(leaf, from, to, scope_id)?;
            self.pending_root = leaf;
            self.pending_height = 1;
            return Ok(());
        }
        let root = self.cow_root()?;
        let split = self.cow_insert_descend(root, 1, from, to, scope_id)?;
        if let Some((sep, right)) = split {
            let new_root = self.alloc_or_reuse()?;
            self.write_branch_new(new_root, root, sep, right)?;
            self.pending_root = new_root;
            self.pending_height += 1;
        }
        Ok(())
    }

    fn cow_insert_descend(
        &mut self, pgno: u32, depth: u32, from: K, to: K, scope_id: u32,
    ) -> Result<Option<(K, u32)>> {
        if depth >= self.pending_height {
            self.leaf_insert(pgno, from, to, scope_id)
        } else {
            let (child_idx, child_pgno) = {
                let page = self.store.page(pgno);
                let count = PageHeader::decode(page).entry_count as usize;
                let branch = BranchView::<K>::new(page, count);
                let idx = Self::branch_find_child(&branch, from);
                (idx, branch.child(idx))
            };
            let cow_child = self.cow_page(child_pgno)?;
            if cow_child != child_pgno {
                self.branch_update_child(pgno, child_idx, cow_child)?;
            }
            let split = self.cow_insert_descend(cow_child, depth + 1, from, to, scope_id)?;
            if let Some((sep, right)) = split {
                self.branch_absorb_split(pgno, child_idx, cow_child, sep, right)
            } else {
                Ok(None)
            }
        }
    }

    fn leaf_insert(&mut self, pgno: u32, from: K, to: K, scope_id: u32) -> Result<Option<(K, u32)>> {
        let count = PageHeader::decode(self.store.page(pgno)).entry_count as usize;
        let leaf_max = spec::leaf_max(self.key_width);
        let pos = self.leaf_find_pos(pgno, count, from);
        let rs = record_size::<K>();

        if count < leaf_max {
            let page = self.store.page_mut(pgno);
            let start = PAGE_HEADER_SIZE + pos * rs;
            let end = PAGE_HEADER_SIZE + count * rs;
            page.copy_within(start..end, start + rs);
            record::write::<K>(&mut page[start..start + rs], from, to, scope_id);
            PageHeader::write(page, spec::PAGE_TYPE_LEAF, (count + 1) as u16, pgno);
            Ok(None)
        } else {
            let mut src = [0u8; PAGE_SIZE];
            src.copy_from_slice(self.store.page(pgno));
            let new_count = count + 1;
            let mid = new_count / 2;
            self.write_leaf_split(pgno, &src, count, pos, from, to, scope_id, 0, mid)?;
            let right = self.alloc_or_reuse()?;
            self.write_leaf_split(right, &src, count, pos, from, to, scope_id, mid, new_count - mid)?;
            let sep = {
                let page = self.store.page(right);
                LeafView::<K>::new(page, mid).record(0).from()
            };
            Ok(Some((sep, right)))
        }
    }

    fn leaf_find_pos(&self, pgno: u32, count: usize, from: K) -> usize {
        let page = self.store.page(pgno);
        let leaf = LeafView::<K>::new(page, count);
        let (mut lo, mut hi) = (0usize, count);
        while lo < hi {
            let mid = lo + (hi - lo) / 2;
            if leaf.record(mid).from() < from {
                lo = mid + 1;
            } else {
                hi = mid;
            }
        }
        lo
    }

    #[allow(clippy::too_many_arguments)]
    fn write_leaf_split(
        &mut self, pgno: u32, src: &[u8], old_count: usize, insert_pos: usize,
        ins_from: K, ins_to: K, ins_scope: u32, start_idx: usize, count: usize,
    ) -> Result<()> {
        let rs = record_size::<K>();
        let page = self.store.page_mut(pgno);
        page.fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_LEAF, count as u16, pgno);
        for out_i in 0..count {
            let abs_i = start_idx + out_i;
            let (f, t, s) = if abs_i == insert_pos {
                (ins_from, ins_to, ins_scope)
            } else {
                let old_i = if abs_i < insert_pos { abs_i } else { abs_i - 1 };
                let off = PAGE_HEADER_SIZE + old_i * rs;
                let r = record::RecordRef::<K>::new(&src[off..off + rs]);
                (r.from(), r.to(), r.scope_id())
            };
            let off = PAGE_HEADER_SIZE + out_i * rs;
            record::write::<K>(&mut page[off..off + rs], f, t, s);
        }
        Ok(())
    }

    fn write_leaf_single(&mut self, pgno: u32, from: K, to: K, scope_id: u32) -> Result<()> {
        let rs = record_size::<K>();
        let page = self.store.page_mut(pgno);
        page.fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_LEAF, 1, pgno);
        record::write::<K>(&mut page[PAGE_HEADER_SIZE..PAGE_HEADER_SIZE + rs], from, to, scope_id);
        Ok(())
    }

    // ── B+tree delete ─────────────────────────────────────────────────

    fn delete_range(&mut self, from: K, to: K) -> Result<()> {
        loop {
            if self.pending_root == 0 {
                return Ok(());
            }
            let overlap = self.scan_first_overlap(from, to)?;
            match overlap {
                None => return Ok(()),
                Some((rec_from, rec_to, rec_scope, leaf_pgno, rec_idx)) => {
                    let cow_leaf = self.cow_to_leaf(leaf_pgno)?;
                    self.leaf_delete_at(cow_leaf, rec_idx)?;
                    self.pending_record_count -= 1;
                    if rec_from < from {
                        if let Some(trim_end) = from.checked_dec() {
                            self.cow_insert(rec_from, trim_end, rec_scope)?;
                            self.pending_record_count += 1;
                        }
                    }
                    if rec_to > to {
                        if let Some(trim_start) = to.checked_inc() {
                            self.cow_insert(trim_start, rec_to, rec_scope)?;
                            self.pending_record_count += 1;
                        }
                    }
                }
            }
        }
    }

    fn scan_first_overlap(&self, from: K, to: K) -> Result<Option<(K, K, u32, u32, usize)>> {
        if self.pending_root == 0 {
            return Ok(None);
        }
        self.scan_overlap_node(self.pending_root, from, to)
    }

    fn scan_overlap_node(&self, pgno: u32, from: K, to: K) -> Result<Option<(K, K, u32, u32, usize)>> {
        let page = self.store.page(pgno);
        let h = PageHeader::decode(page);
        match h.page_type {
            spec::PAGE_TYPE_LEAF => {
                let leaf = LeafView::<K>::new(page, h.entry_count as usize);
                for i in 0..leaf.len() {
                    let r = leaf.record(i);
                    if r.from() > to {
                        return Ok(None);
                    }
                    if r.to() >= from {
                        return Ok(Some((r.from(), r.to(), r.scope_id(), pgno, i)));
                    }
                }
                Ok(None)
            }
            spec::PAGE_TYPE_BRANCH => {
                let branch = BranchView::<K>::new(page, h.entry_count as usize);
                let start = Self::branch_find_child(&branch, from);
                for j in start..branch.child_count() {
                    if let Some(r) = self.scan_overlap_node(branch.child(j), from, to)? {
                        return Ok(Some(r));
                    }
                }
                Ok(None)
            }
            _ => Err(Error::Structural("unexpected page type")),
        }
    }

    fn cow_to_leaf(&mut self, target_leaf: u32) -> Result<u32> {
        let guide_key = {
            let page = self.store.page(target_leaf);
            let count = PageHeader::decode(page).entry_count as usize;
            if count == 0 { K::MIN } else { LeafView::<K>::new(page, count).record(0).from() }
        };
        let mut pgno = self.cow_root()?;
        for _ in 1..self.pending_height {
            let (child_idx, child_pgno) = {
                let page = self.store.page(pgno);
                let count = PageHeader::decode(page).entry_count as usize;
                let branch = BranchView::<K>::new(page, count);
                let idx = Self::branch_find_child(&branch, guide_key);
                (idx, branch.child(idx))
            };
            let cow_child = self.cow_page(child_pgno)?;
            if cow_child != child_pgno {
                self.branch_update_child(pgno, child_idx, cow_child)?;
            }
            pgno = cow_child;
        }
        Ok(pgno)
    }

    fn leaf_delete_at(&mut self, pgno: u32, pos: usize) -> Result<()> {
        let count = PageHeader::decode(self.store.page(pgno)).entry_count as usize;
        let new_count = count - 1;
        let rs = record_size::<K>();
        let page = self.store.page_mut(pgno);
        let start = PAGE_HEADER_SIZE + pos * rs;
        let end = PAGE_HEADER_SIZE + count * rs;
        page.copy_within(start + rs..end, start);
        page[end - rs..end].fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_LEAF, new_count as u16, pgno);
        Ok(())
    }

    // ── branch ops ────────────────────────────────────────────────────

    fn branch_find_child(branch: &BranchView<'_, K>, key: K) -> usize {
        let (mut lo, mut hi) = (0usize, branch.sep_count());
        while lo < hi {
            let mid = lo + (hi - lo) / 2;
            if branch.sep(mid) <= key {
                lo = mid + 1;
            } else {
                hi = mid;
            }
        }
        lo
    }

    fn branch_update_child(&mut self, pgno: u32, child_idx: usize, new_child: u32) -> Result<()> {
        let off = if child_idx == 0 {
            PAGE_HEADER_SIZE
        } else {
            PAGE_HEADER_SIZE + 4 + (child_idx - 1) * (K::WIDTH + 4) + K::WIDTH
        };
        put_u32(self.store.page_mut(pgno), off, new_child);
        Ok(())
    }

    fn branch_absorb_split(
        &mut self, pgno: u32, child_idx: usize, left: u32, sep: K, right: u32,
    ) -> Result<Option<(K, u32)>> {
        let count = PageHeader::decode(self.store.page(pgno)).entry_count as usize;
        let branch_max = spec::branch_max(self.key_width);
        if count < branch_max {
            let page = self.store.page_mut(pgno);
            let ins_off = PAGE_HEADER_SIZE + 4 + child_idx * (K::WIDTH + 4);
            let end_off = PAGE_HEADER_SIZE + 4 + count * (K::WIDTH + 4);
            page.copy_within(ins_off..end_off, ins_off + K::WIDTH + 4);
            sep.write_le(&mut page[ins_off..ins_off + K::WIDTH]);
            put_u32(page, ins_off + K::WIDTH, right);
            let left_off = if child_idx == 0 {
                PAGE_HEADER_SIZE
            } else {
                PAGE_HEADER_SIZE + 4 + (child_idx - 1) * (K::WIDTH + 4) + K::WIDTH
            };
            put_u32(page, left_off, left);
            PageHeader::write(page, spec::PAGE_TYPE_BRANCH, (count + 1) as u16, pgno);
            Ok(None)
        } else {
            // Branch split: copy to stack buffer, redistribute into two pages.
            let mut src = [0u8; PAGE_SIZE];
            src.copy_from_slice(self.store.page(pgno));
            let total = count + 1;
            let mid = total / 2;

            self.write_branch_split(pgno, &src, count, child_idx, left, sep, right, 0, mid)?;
            let right_pgno = self.alloc_or_reuse()?;
            self.write_branch_split(right_pgno, &src, count, child_idx, left, sep, right, mid, total - mid)?;

            // Promoted separator = sep at index `mid` in the combined array.
            let promoted = if mid == child_idx {
                sep
            } else {
                let old_i = if mid < child_idx { mid } else { mid - 1 };
                let off = PAGE_HEADER_SIZE + 4 + old_i * (K::WIDTH + 4);
                K::read_le(&src[off..off + K::WIDTH])
            };
            Ok(Some((promoted, right_pgno)))
        }
    }

    /// Write a branch page from a source buffer + an inserted split entry.
    #[allow(clippy::too_many_arguments)]
    fn write_branch_split(
        &mut self, pgno: u32, src: &[u8], old_count: usize, insert_idx: usize,
        ins_left: u32, ins_sep: K, ins_right: u32, start_idx: usize, sep_count: usize,
    ) -> Result<()> {
        let kw = K::WIDTH;
        let page = self.store.page_mut(pgno);
        page.fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_BRANCH, sep_count as u16, pgno);

        let first_child = if start_idx == 0 {
            if insert_idx == 0 { ins_left } else { u32_le(src, PAGE_HEADER_SIZE) }
        } else if insert_idx == start_idx {
            ins_right
        } else {
            let old_i = start_idx - 1;
            u32_le(src, PAGE_HEADER_SIZE + 4 + old_i * (kw + 4) + kw)
        };
        put_u32(page, PAGE_HEADER_SIZE, first_child);

        for out_i in 0..sep_count {
            let abs_i = start_idx + out_i;
            let (s, c) = if abs_i == insert_idx {
                (ins_sep, ins_right)
            } else {
                let old_i = if abs_i < insert_idx { abs_i } else { abs_i - 1 };
                let off = PAGE_HEADER_SIZE + 4 + old_i * (kw + 4);
                (K::read_le(&src[off..off + kw]), u32_le(src, off + kw))
            };
            let out_off = PAGE_HEADER_SIZE + 4 + out_i * (kw + 4);
            s.write_le(&mut page[out_off..out_off + kw]);
            put_u32(page, out_off + kw, c);
        }
        Ok(())
    }

    fn write_branch_new(&mut self, pgno: u32, left: u32, sep: K, right: u32) -> Result<()> {
        let page = self.store.page_mut(pgno);
        page.fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_BRANCH, 1, pgno);
        put_u32(page, PAGE_HEADER_SIZE, left);
        sep.write_le(&mut page[PAGE_HEADER_SIZE + 4..PAGE_HEADER_SIZE + 4 + K::WIDTH]);
        put_u32(page, PAGE_HEADER_SIZE + 4 + K::WIDTH, right);
        Ok(())
    }

    // ── meta ──────────────────────────────────────────────────────────

    #[allow(clippy::too_many_arguments)]
    fn write_meta_page(
        &mut self, pgno: u32, txn_id: u64, root: u32, height: u32,
        record_count: u64, total_pages: u32, updated: u64,
    ) -> Result<()> {
        let meta = Meta {
            pgno,
            version_minor: spec::VERSION_MINOR,
            meta_size: spec::META_SIZE,
            page_size: PAGE_SIZE as u32,
            checksum_algo: spec::CHECKSUM_ALGO_CRC32C,
            flags: if K::WIDTH == 16 { spec::FLAG_IP_VERSION } else { 0 },
            key_width: K::WIDTH as u8,
            scope_mode: self.scope_mode,
            record_size: record_size::<K>() as u32,
            created_unixtime: self.created_unixtime,
            root_pgno: root,
            tree_height: height,
            total_pages: total_pages as u64,
            record_count,
            txn_id,
            updated_unixtime: updated,
            scope_table_root: self.scope_table_root_cache,
            free_list_head: self.free_list_head,
        };
        meta.encode_into(self.store.page_mut(pgno));
        Ok(())
    }

    // ── scan ──────────────────────────────────────────────────────────

    // ── scope table operations (mode 2 only) ──────────────────────────────

    /// Find or create a scope_id for the given bitmap. Returns the scope_id.
    /// Only valid when scope_mode == 2 (indirect).
    pub fn scope_intern(&mut self, bitmap: &[u8]) -> Result<u32> {
        match &mut self.scope_registry {
            Some(reg) => Ok(reg.intern(bitmap)),
            None => Err(Error::State("scope_intern requires scope_mode == 2")),
        }
    }

    /// Resolve a scope_id to its bitmap. Returns None if not found.
    pub fn scope_resolve(&self, scope_id: u32) -> Option<&[u8]> {
        self.scope_registry.as_ref()?.resolve(scope_id)
    }

    /// Set a feed bit in a bitmap. Returns the new scope_id.
    pub fn scope_bitmap_set_feed(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        match &mut self.scope_registry {
            Some(reg) => Ok(reg.bitmap_set_feed(scope_id, feed_bit)),
            None => Err(Error::State("requires scope_mode == 2")),
        }
    }

    /// Clear a feed bit from a bitmap. Returns the new scope_id (0 if empty).
    pub fn scope_bitmap_clear_feed(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        match &mut self.scope_registry {
            Some(reg) => Ok(reg.bitmap_clear_feed(scope_id, feed_bit)),
            None => Err(Error::State("requires scope_mode == 2")),
        }
    }

    // ── feed-bit range API (fixes #5) ─────────────────────────────────────
    //
    // FeedAddRange: apply a feed bit across [from, to], preserving all other
    // feed bits. Handles interval splitting: existing records that partially
    // overlap are split at the boundaries, the bit is OR'd into scope_ids
    // within [from, to], then adjacent same-scope records are merged.
    //
    // For mode 1 (bitmap): scope_id IS the bitmap; OR the bit directly.
    // For mode 2 (indirect): resolve scope_id → bitmap, OR the bit, re-intern.

    /// Add feed `feed_bit` to all IP ranges overlapping [from, to].
    /// Existing feed bits are preserved. Adjacent same-scope ranges are merged.
    pub fn feed_add_range(&mut self, from: K, to: K, feed_bit: u32) -> Result<()> {
        self.check()?;
        if from > to { return Err(Error::InvalidInput("from > to")); }

        // Collect all existing records that overlap [from, to].
        let overlaps = self.collect_overlapping(from, to)?;

        // Delete all overlapping records from the pending tree.
        for (of, ot, _) in &overlaps {
            self.delete(*of, *ot)?;
        }

        // Build segments: for each part of [from, to], determine the new scope.
        // Parts outside [from, to] keep their original scope.
        // Parts inside [from, to] get the feed bit OR'd in.
        // Gaps in [from, to] not covered by any overlap get a fresh scope
        // with just the feed bit.

        // Track which parts of [from, to] have been covered.
        let mut cursor = from;

        for (of, ot, os) in &overlaps {
            // Gap before this overlap: [cursor, of-1] if of > cursor and of >= from
            if *of > cursor && cursor <= to {
                let gap_to = if *of <= to { of.checked_dec().unwrap_or(*of) } else { to };
                if gap_to >= cursor {
                    let new_scope = self.fresh_feed_scope(feed_bit)?;
                    self.cow_insert(cursor, gap_to, new_scope)?;
                    self.pending_record_count += 1;
                }
            }

            // Left outside part: [max(of, from_prev), min(of, from)-1]
            // Actually: if the overlap starts before `from`, the part [of, from-1]
            // keeps the original scope.
            if *of < from {
                let trim_end = from.checked_dec().unwrap_or(from);
                self.cow_insert(*of, trim_end, *os)?;
                self.pending_record_count += 1;
            }

            // Inside part: [max(of, from), min(ot, to)] → apply feed bit
            let inner_from = if *of > from { *of } else { from };
            let inner_to = if *ot < to { *ot } else { to };
            let new_scope = self.apply_feed_bit(*os, feed_bit)?;
            self.cow_insert(inner_from, inner_to, new_scope)?;
            self.pending_record_count += 1;

            // Right outside part: if ot > to, [to+1, ot] keeps original scope.
            if *ot > to {
                let trim_start = to.checked_inc().unwrap_or(to);
                self.cow_insert(trim_start, *ot, *os)?;
                self.pending_record_count += 1;
            }

            // Advance cursor past this overlap.
            cursor = ot.checked_inc().unwrap_or(*ot);
        }

        // Gap after the last overlap (or the entire [from, to] if no overlaps).
        if cursor <= to {
            let new_scope = self.fresh_feed_scope(feed_bit)?;
            self.cow_insert(cursor, to, new_scope)?;
            self.pending_record_count += 1;
        }

        Ok(())
    }

    /// Create a scope_id with only the given feed bit set (no prior feeds).
    fn fresh_feed_scope(&mut self, feed_bit: u32) -> Result<u32> {
        match self.scope_mode {
            spec::SCOPE_MODE_BITMAP => {
                if feed_bit >= 32 {
                    return Err(Error::InvalidInput("feed_bit >= 32 in bitmap mode (use indirect mode)"));
                }
                Ok(1u32 << feed_bit)
            }
            spec::SCOPE_MODE_INDIRECT => {
                // Dynamically size the bitmap to fit the feed bit.
                let byte_idx = (feed_bit / 8) as usize;
                let mut bm = alloc::vec![0u8; byte_idx + 1];
                bm[byte_idx] |= 1 << (feed_bit % 8);
                match &mut self.scope_registry {
                    Some(reg) => Ok(reg.intern(&bm)),
                    None => Err(Error::State("requires scope_mode == 2")),
                }
            }
            _ => Err(Error::State("feed operations require scope_mode 1 or 2")),
        }
    }

    /// Remove feed `feed_bit` from all IP ranges overlapping [from, to].
    pub fn feed_remove_range(&mut self, from: K, to: K, feed_bit: u32) -> Result<()> {
        self.check()?;
        if from > to { return Err(Error::InvalidInput("from > to")); }

        let overlaps = self.collect_overlapping(from, to)?;

        for (of, ot, os) in &overlaps {
            self.delete(*of, *ot)?;
        }

        for (of, ot, os) in &overlaps {
            // Outside [from, to]: keep original.
            if *of < from {
                let trim_end = from.checked_dec().unwrap_or(from);
                self.cow_insert(*of, trim_end, *os)?;
                self.pending_record_count += 1;
            }
            if *ot > to {
                let trim_start = to.checked_inc().unwrap_or(to);
                self.cow_insert(trim_start, *ot, *os)?;
                self.pending_record_count += 1;
            }

            // Inside [from, to]: clear the feed bit.
            let inner_from = if *of > from { *of } else { from };
            let inner_to = if *ot < to { *ot } else { to };
            let new_scope = self.clear_feed_bit(*os, feed_bit)?;
            if new_scope != 0 {
                // Still has other feeds → keep.
                self.cow_insert(inner_from, inner_to, new_scope)?;
                self.pending_record_count += 1;
            }
            // If new_scope == 0, the record is fully removed (no feeds left).
        }

        Ok(())
    }

    /// Collect all pending records overlapping [from, to].
    fn collect_overlapping(&self, from: K, to: K) -> Result<Vec<(K, K, u32)>> {
        if self.pending_root == 0 { return Ok(Vec::new()); }
        let mut result = Vec::new();
        self.collect_overlapping_node(self.pending_root, from, to, &mut result)?;
        Ok(result)
    }

    fn collect_overlapping_node(&self, pgno: u32, from: K, to: K, out: &mut Vec<(K, K, u32)>) -> Result<()> {
        let page = self.store.page(pgno);
        let h = PageHeader::decode(page);
        match h.page_type {
            spec::PAGE_TYPE_LEAF => {
                let leaf = LeafView::<K>::new(page, h.entry_count as usize);
                for i in 0..leaf.len() {
                    let r = leaf.record(i);
                    if r.from() > to { break; }
                    if r.to() >= from {
                        out.push((r.from(), r.to(), r.scope_id()));
                    }
                }
                Ok(())
            }
            spec::PAGE_TYPE_BRANCH => {
                let branch = BranchView::<K>::new(page, h.entry_count as usize);
                // Binary search for the first child that could contain `from`.
                // child[j] covers [sep[j-1], sep[j]-1]. We want the first child
                // whose range might overlap [from, to].
                let start = Self::branch_find_child(&branch, from);
                for j in start..branch.child_count() {
                    // If the separator before this child is > to, no more overlaps.
                    if j > 0 {
                        let sep = branch.sep(j - 1);
                        // sep[j-1] is the lower bound of child[j].
                        // If sep > to, child[j] and all subsequent are past our range.
                        if sep > to { break; }
                    }
                    self.collect_overlapping_node(branch.child(j), from, to, out)?;
                }
                Ok(())
            }
            _ => Err(Error::Structural("unexpected page type")),
        }
    }

    /// Apply a feed bit to a scope_id, returning the new scope_id.
    fn apply_feed_bit(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        match self.scope_mode {
            spec::SCOPE_MODE_BITMAP => {
                if feed_bit >= 32 {
                    return Err(Error::InvalidInput("feed_bit >= 32 in bitmap mode"));
                }
                Ok(scope_id | (1u32 << feed_bit))
            }
            spec::SCOPE_MODE_INDIRECT => {
                match &mut self.scope_registry {
                    Some(reg) => Ok(reg.bitmap_set_feed(scope_id, feed_bit)),
                    None => Err(Error::State("requires scope_mode == 2")),
                }
            }
            _ => Err(Error::State("feed operations require scope_mode 1 or 2")),
        }
    }

    /// Clear a feed bit from a scope_id, returning the new scope_id (0 if empty).
    fn clear_feed_bit(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        match self.scope_mode {
            spec::SCOPE_MODE_BITMAP => {
                if feed_bit >= 32 {
                    return Err(Error::InvalidInput("feed_bit >= 32 in bitmap mode"));
                }
                Ok(scope_id & !(1u32 << feed_bit))
            }
            spec::SCOPE_MODE_INDIRECT => {
                match &mut self.scope_registry {
                    Some(reg) => Ok(reg.bitmap_clear_feed(scope_id, feed_bit)),
                    None => Err(Error::State("requires scope_mode == 2")),
                }
            }
            _ => Err(Error::State("feed operations require scope_mode 1 or 2")),
        }
    }

    /// Walk the old scope table tree and free each page.
    fn free_scope_table_pages(&mut self, root: u32) {
        let mut to_free: alloc::vec::Vec<u32> = alloc::vec::Vec::new();
        self.collect_scope_pages(root, 0, &mut to_free);
        for pgno in to_free {
            let _ = self.track_freed(pgno);
        }
    }

    fn collect_scope_pages(&self, pgno: u32, depth: u32, out: &mut alloc::vec::Vec<u32>) {
        if depth > spec::TREE_HEIGHT_MAX || pgno as u64 >= self.store.total_pages() as u64 {
            return;
        }
        out.push(pgno);
        let page = self.store.page(pgno);
        let h = PageHeader::decode(page);
        if h.page_type == spec::PAGE_TYPE_SCOPE_BRANCH {
            let branch = BranchView::<crate::key::Ipv4Key>::new(page, h.entry_count as usize);
            for j in 0..branch.child_count() {
                self.collect_scope_pages(branch.child(j), depth + 1, out);
            }
        }
    }

    fn scan_node(&self, pgno: u32, f: &mut impl FnMut(K, K, u32)) -> Result<()> {
        let page = self.store.page(pgno);
        let h = PageHeader::decode(page);
        match h.page_type {
            spec::PAGE_TYPE_LEAF => {
                let leaf = LeafView::<K>::new(page, h.entry_count as usize);
                for i in 0..leaf.len() {
                    let r = leaf.record(i);
                    f(r.from(), r.to(), r.scope_id());
                }
                Ok(())
            }
            spec::PAGE_TYPE_BRANCH => {
                let branch = BranchView::<K>::new(page, h.entry_count as usize);
                for j in 0..branch.child_count() {
                    self.scan_node(branch.child(j), f)?;
                }
                Ok(())
            }
            _ => Err(Error::Structural("unexpected page type in scan")),
        }
    }
}
