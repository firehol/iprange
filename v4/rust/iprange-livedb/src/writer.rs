//! The v4.3 writer: COW B+tree with private-pages bitset tracking.
//!
//! Design (LMDB-inspired):
//! - private_pages: bitset tracking pages COW'd this transaction
//! - cow_page: if pgno is in private_pages → in-place; else COW
//! - free_pages: derived at open time by walking the committed tree
//! - alloc_page: pop from free_pages, or extend the file
//! - commit: finalize CRCs on private pages, write meta, clear bitset

use alloc::boxed::Box;
use alloc::vec;
use core::marker::PhantomData;

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::node::{BranchView, LeafView};
use crate::page_set::PageSet;
use crate::page_store::{PageStore, VecPageStore};
use crate::reader::Reader;
use crate::record::{self, record_size};
use crate::spec::{self, PAGE_HEADER_SIZE, PAGE_SIZE};
use crate::wire::{finalize_checksum, put_u32, u32_le, Meta, PageHeader};

pub type MetaEntry = (alloc::vec::Vec<u8>, u32, alloc::vec::Vec<u8>);

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Changed { Changed, Unchanged }

#[allow(missing_debug_implementations)]
pub struct Writer<K: IpKey> {
    pub(crate) store: Box<dyn PageStore>,
    key_width: u8,
    pub(crate) scope_mode: u8,
    created_unixtime: u64,
    active_meta: u32,
    pub(crate) committed_root: u32,
    pub(crate) committed_height: u32,
    committed_pages: u32,
    committed_record_count: u64,
    committed_txn_id: u64,
    /// Previous committed root (for reader-safe reclamation).
    prev_committed_root: u32,
    prev_committed_height: u32,
    pub(crate) pending_root: u32,
    pending_height: u32,
    pending_record_count: u64,
    poisoned: bool,
    private_pages: PageSet,
    free_pages: alloc::vec::Vec<u32>,
    free_pos: usize,
    /// Cursor into freed_this_txn for same-transaction recycling.
    /// When free_pages is exhausted, alloc_page reuses COW victims.
    recycle_pos: usize,
    /// When false, COW victims are NOT recycled (readers may reference them).
    /// Set by FileWriter based on oldest_reader_txn_id.
    pub(crate) can_recycle: bool,
    scope_registry: Option<crate::scope_table::ScopeRegistry>,
    scope_table_root_cache: u32,
    scope_dirty: bool,
    free_list_head: u32,
    freed_this_txn: alloc::vec::Vec<u32>,
    _k: PhantomData<K>,
}

impl<K: IpKey> Writer<K> {
    pub fn create(scope_mode: u8, created_unixtime: u64) -> Result<Writer<K>> {
        let store: Box<dyn PageStore> = Box::new(VecPageStore::new(vec![0u8; 2 * PAGE_SIZE]));
        let mut w = Writer {
            store, key_width: K::WIDTH as u8, scope_mode, created_unixtime,
            active_meta: 0, committed_root: 0, committed_height: 0,
            committed_pages: 2, committed_record_count: 0, committed_txn_id: 0,
            prev_committed_root: 0, prev_committed_height: 0,
            pending_root: 0, pending_height: 0, pending_record_count: 0,
            poisoned: false,
            private_pages: PageSet::new(2), free_pages: vec![], free_pos: 0,
            recycle_pos: 0, can_recycle: true,
            scope_registry: if scope_mode == spec::SCOPE_MODE_INDIRECT {
                Some(crate::scope_table::ScopeRegistry::new())
            } else { None },
            scope_table_root_cache: 0,
            scope_dirty: false,
            free_list_head: 0,
            freed_this_txn: alloc::vec::Vec::with_capacity(4096),
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
        let (active, active_no) = if meta_a.txn_id >= meta_b.txn_id { (meta_a, 0u32) } else { (meta_b, 1u32) };
        if active.key_width != K::WIDTH as u8 { return Err(Error::Structural("key_width mismatch")); }
        if active.record_size != record_size::<K>() as u32 { return Err(Error::Structural("record_size mismatch")); }

        let scope_reg = if active.scope_mode == spec::SCOPE_MODE_INDIRECT && active.scope_table_root != 0 {
            let entries = crate::scope_table::read_all(store.committed_bytes(), active.scope_table_root).unwrap_or_default();
            Some(crate::scope_table::ScopeRegistry::from_entries(entries))
        } else if active.scope_mode == spec::SCOPE_MODE_INDIRECT {
            Some(crate::scope_table::ScopeRegistry::new())
        } else { None };

        let mut w = Writer {
            store, key_width: active.key_width, scope_mode: active.scope_mode,
            created_unixtime: active.created_unixtime, active_meta: active_no,
            committed_root: active.root_pgno, committed_height: active.tree_height,
            committed_pages: active.total_pages as u32, committed_record_count: active.record_count,
            committed_txn_id: active.txn_id,
            prev_committed_root: if active_no == 0 { meta_b.root_pgno } else { meta_a.root_pgno },
            prev_committed_height: if active_no == 0 { meta_b.tree_height } else { meta_a.tree_height },
            pending_root: active.root_pgno, pending_height: active.tree_height,
            pending_record_count: active.record_count, poisoned: false,
            private_pages: PageSet::new(active.total_pages as usize),
            free_pages: vec![], free_pos: 0,
            recycle_pos: 0, can_recycle: true,
            scope_registry: scope_reg, scope_table_root_cache: active.scope_table_root,
            scope_dirty: false,
            free_list_head: active.free_list_head,
            freed_this_txn: alloc::vec::Vec::with_capacity((active.total_pages as usize).max(4096)),
            _k: PhantomData,
        };
        w.load_free_list(u64::MAX);
        Ok(w)
    }

    // ── public API ──

    pub fn set(&mut self, from: K, to: K, scope_id: u32) -> Result<()> {
        self.check()?;
        if from > to { return Err(Error::InvalidInput("from > to")); }
        self.delete_range(from, to)?;
        self.cow_insert(from, to, scope_id)?;
        self.pending_record_count += 1;
        Ok(())
    }

    pub fn delete(&mut self, from: K, to: K) -> Result<Changed> {
        self.check()?;
        if from > to { return Err(Error::InvalidInput("from > to")); }
        let before = self.pending_record_count;
        self.delete_range(from, to)?;
        Ok(if self.pending_record_count < before { Changed::Changed } else { Changed::Unchanged })
    }

    pub fn append(&mut self, from: K, to: K, scope_id: u32) -> Result<()> {
        self.check()?;
        if from > to { return Err(Error::InvalidInput("from > to")); }
        self.cow_insert(from, to, scope_id)?;
        self.pending_record_count += 1;
        Ok(())
    }

    /// Commit the pending transaction.
    /// `oldest_reader_txn_id` must be the minimum txn_id among all active
    /// readers, or `u64::MAX` if no readers are active. This is queried fresh
    /// at call time (not cached) to prevent MVCC violations from stale state.
    pub fn commit(&mut self, updated_unixtime: u64, oldest_reader_txn_id: u64) -> Result<()> {
        self.check()?;
        // Refresh MVCC state BEFORE any commit logic uses can_recycle.
        self.can_recycle = oldest_reader_txn_id == u64::MAX;
        // Rebuild scope table (mode 2).
        if self.scope_dirty {
            // Collect old scope tree pages.
            let mut old_scope_pages = Vec::new();
            if self.scope_table_root_cache != 0 {
                self.collect_scope_page_numbers(self.scope_table_root_cache, 0, &mut old_scope_pages);
            }
            // When readers are active, old scope pages are committed-region
            // pages that readers may reference via MAP_SHARED. We must NOT
            // overwrite them in-place. Instead, push them to freed_this_txn
            // (for free-list chain) and let build_scope_tree allocate fresh.
            let mut free_pool = Vec::new();
            if self.can_recycle {
                free_pool = old_scope_pages;
            } else {
                for &pgno in &old_scope_pages {
                    self.freed_this_txn.push(pgno);
                }
            }
            self.scope_table_root_cache = if let Some(ref reg) = self.scope_registry {
                if reg.is_empty() { 0 } else {
                    let mut allocated = Vec::new();
                    let root = crate::scope_table::build_scope_tree(
                        self.store.as_mut(), reg.entries(), &mut allocated, &mut free_pool,
                    )?;
                    // Register scope pages in private_pages for CRC finalization.
                    for pgno in &allocated {
                        self.private_pages.insert(*pgno);
                    }
                    root
                }
            } else { 0 };
            self.scope_dirty = false;
        }
        // Finalize CRCs on all private pages.
        for pgno in self.private_pages.iter() {
            if pgno >= 2 { finalize_checksum(self.store.page_mut(pgno)); }
        }
        // Build and write the persistent free-list.
        let new_txn_id_val = self.committed_txn_id;
        // Collect entries to persist:
        // - Old entries not consumed this transaction
        // - Pages freed this transaction (COW victims + old chain pages + old scope pages)
        let old_entries = if self.free_list_head != 0 {
            crate::free_list::read_chain(self.store.as_ref(), self.free_list_head).unwrap_or_default()
        } else { Vec::new() };
        let old_chain_pages = crate::free_list::read_chain_page_numbers(
            self.store.as_ref(), self.free_list_head,
        );
        
        // Determine consumed pages (popped from free_pages during the transaction).
        let consumed: std::collections::HashSet<u32> = 
            self.free_pages[..self.free_pos.min(self.free_pages.len())].iter().copied().collect();
        
        let mut entries_to_write: Vec<crate::free_list::FreeEntry> = Vec::new();
        // Keep unconsumed old entries.
        for e in &old_entries {
            if !consumed.contains(&e.pgno) {
                entries_to_write.push(*e);
            }
        }
        // Add old chain page numbers (they're being replaced).
        for p in &old_chain_pages {
            self.freed_this_txn.push(*p);
        }
        // Add pages freed this transaction (excluding recycled pages, which
        // are now live tree pages and must NOT appear in the free-list).
        for &pgno in &self.freed_this_txn[self.recycle_pos..] {
            entries_to_write.push(crate::free_list::FreeEntry { 
                pgno, freed_txn_id: new_txn_id_val, 
            });
        }

        // Rule 5: compute trailing free pages that can be truncated.
        // This must happen BEFORE writing the chain so truncated pages
        // don't appear as stale entries in the chain.
        let pre_truncate_total = self.store.total_pages();
        let trailing: u32 = if oldest_reader_txn_id == u64::MAX {
            let free_pgnos: alloc::vec::Vec<u32> = entries_to_write.iter()
                .map(|e| e.pgno)
                .collect();
            crate::free_list::trailing_free_count(&free_pgnos, pre_truncate_total)
        } else { 0 };

        let new_total = pre_truncate_total - trailing;

        // Remove trailing pages from entries_to_write so the chain doesn't
        // reference pages that will be truncated.
        if trailing > 0 {
            entries_to_write.retain(|e| e.pgno < new_total);
        }

        // Allocate chain pages: prefer reusing a freed page as the first chain
        // page (Rule 5: no unbounded file growth). Only safe when no readers
        // are active — otherwise freed pages may still be referenced by a
        // reader at an older transaction (MVCC safety).
        let self_supply_page: Option<u32> = if self.can_recycle && entries_to_write.len() >= 2 {
            let max_idx = entries_to_write.iter()
                .enumerate()
                .max_by_key(|(_, e)| e.pgno)
                .map(|(i, _)| i);
            if let Some(idx) = max_idx {
                let pgno = entries_to_write[idx].pgno;
                entries_to_write.swap_remove(idx);
                Some(pgno)
            } else { None }
        } else { None };

        entries_to_write.sort_by_key(|e| e.freed_txn_id);
        let needed = crate::free_list::chain_page_count(&entries_to_write);

        let mut chain_pages: alloc::vec::Vec<u32> = alloc::vec::Vec::with_capacity(needed);
        if let Some(cp) = self_supply_page {
            chain_pages.push(cp);
        }
        while chain_pages.len() < needed {
            chain_pages.push(self.store.alloc_page()?);
        }

        self.free_list_head = crate::free_list::write_chain(
            self.store.as_mut(), &entries_to_write, &chain_pages,
        )?;

        // Truncate trailing free pages now that the chain no longer references them.
        let total = if trailing > 0 {
            self.store.truncate(new_total)?;
            new_total
        } else {
            self.store.total_pages()
        };

        let inactive = 1 - self.active_meta;
        let new_txn_id = self.committed_txn_id + 1;
        self.write_meta_page(inactive, new_txn_id, self.pending_root, self.pending_height,
            self.pending_record_count, total, updated_unixtime)?;
        self.store.sync()?;
        self.active_meta = inactive;
        self.committed_txn_id = new_txn_id;
        self.prev_committed_root = self.committed_root;
        self.prev_committed_height = self.committed_height;
        self.committed_root = self.pending_root;
        self.committed_height = self.pending_height;
        self.committed_record_count = self.pending_record_count;
        self.committed_pages = total;
        self.store.set_committed_pages(total);
        self.reset_txn();
        self.load_free_list(oldest_reader_txn_id);

        Ok(())
    }

    pub fn reader(&self) -> Result<Reader<'_>> { Reader::open(self.store.committed_bytes()) }

    pub fn scan(&self, mut f: impl FnMut(K, K, u32)) -> Result<()> {
        if self.pending_root == 0 { return Ok(()); }
        self.scan_node(self.pending_root, &mut f)
    }

    pub fn record_count(&self) -> u64 { self.pending_record_count }
    pub fn committed_pages(&self) -> u32 { self.committed_pages }

    pub fn into_image(self) -> Option<alloc::vec::Vec<u8>> {
        self.store.into_vec()
    }

    // ── COW mechanics ──

    fn check(&self) -> Result<()> {
        if self.poisoned { Err(Error::State("writer poisoned")) } else { Ok(()) }
    }

    fn cow_page(&mut self, pgno: u32) -> Result<u32> {
        if self.private_pages.contains(pgno) { return Ok(pgno); }
        let new = self.alloc_page()?;
        self.store.copy_page(pgno, new);
        self.private_pages.insert(new);
        self.freed_this_txn.push(pgno);
        Ok(new)
    }

    fn cow_root(&mut self) -> Result<u32> {
        if self.pending_root == 0 { return Ok(0); }
        let new = self.cow_page(self.pending_root)?;
        self.pending_root = new;
        Ok(new)
    }

    fn alloc_page(&mut self) -> Result<u32> {
        if self.free_pos < self.free_pages.len() {
            let pgno = self.free_pages[self.free_pos];
            self.free_pos += 1;
            self.private_pages.insert(pgno);
            Ok(pgno)
        } else if self.can_recycle && self.recycle_pos < self.freed_this_txn.len() {
            let pgno = self.freed_this_txn[self.recycle_pos];
            self.recycle_pos += 1;
            self.private_pages.insert(pgno);
            // Clear stale data: recycled pages may contain old branch headers
            // with child pointers that create cycles during tree traversal.
            self.store.page_mut(pgno).fill(0);
            Ok(pgno)
        } else {
            let pgno = self.store.alloc_page()?;
            self.private_pages.ensure_capacity(pgno as usize + 1);
            self.private_pages.insert(pgno);
            Ok(pgno)
        }
    }

    fn collect_scope_page_numbers(&self, pgno: u32, depth: u32, out: &mut alloc::vec::Vec<u32>) {
        if depth > spec::TREE_HEIGHT_MAX || pgno as u64 >= self.store.total_pages() as u64 {
            return;
        }
        out.push(pgno);
        let page = self.store.page(pgno);
        let h = PageHeader::decode(page);
        if h.page_type == spec::PAGE_TYPE_SCOPE_BRANCH {
            let branch = BranchView::<crate::key::Ipv4Key>::new(page, h.entry_count as usize);
            for j in 0..branch.child_count() {
                self.collect_scope_page_numbers(branch.child(j), depth + 1, out);
            }
        }
    }

    /// Load the free-list from the persistent chain.
    /// `oldest_reader_txn_id`: pages freed in txn <= this are reclaimable.
    /// u64::MAX means no readers (all pages reclaimable).
    pub fn load_free_list(&mut self, oldest_reader_txn_id: u64) {
        // Control same-txn recycling: safe only when no readers are active.
        self.can_recycle = oldest_reader_txn_id == u64::MAX;
        if self.free_list_head == 0 {
            self.free_pages.clear();
            self.free_pos = 0;
            return;
        }
        let entries = crate::free_list::read_chain(self.store.as_ref(), self.free_list_head)
            .unwrap_or_default();
        self.free_pages = crate::free_list::reclaimable(&entries, oldest_reader_txn_id);
        self.free_pages.sort(); // Rule 5: prefer low-numbered pages
        self.free_pos = 0;
    }

    fn reset_txn(&mut self) {
        self.private_pages.clear();
        self.freed_this_txn.clear();
        self.recycle_pos = 0;
        self.private_pages.ensure_capacity(self.store.total_pages() as usize);
    }

    // ── B+tree insert ──

    fn cow_insert(&mut self, from: K, to: K, scope_id: u32) -> Result<()> {
        if self.pending_root == 0 {
            let leaf = self.alloc_page()?;
            self.write_leaf_single(leaf, from, to, scope_id)?;
            self.pending_root = leaf;
            self.pending_height = 1;
            return Ok(());
        }
        let root = self.cow_root()?;
        let split = self.cow_insert_descend(root, from, to, scope_id)?;
        if let Some((sep, right)) = split {
            let new_root = self.alloc_page()?;
            self.write_branch_new(new_root, root, sep, right)?;
            self.pending_root = new_root;
            self.pending_height += 1;
        }
        Ok(())
    }

    fn cow_insert_descend(&mut self, pgno: u32, from: K, to: K, scope_id: u32) -> Result<Option<(K, u32)>> {
        let page_type = PageHeader::decode(self.store.page(pgno)).page_type;
        if page_type == spec::PAGE_TYPE_LEAF {
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
            let split = self.cow_insert_descend(cow_child, from, to, scope_id)?;
            if let Some((sep, right)) = split {
                self.branch_absorb_split(pgno, child_idx, cow_child, sep, right)
            } else { Ok(None) }
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
            let right = self.alloc_page()?;
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
            if leaf.record(mid).from() < from { lo = mid + 1; } else { hi = mid; }
        }
        lo
    }

    #[allow(clippy::too_many_arguments)]
    fn write_leaf_split(&mut self, pgno: u32, src: &[u8], _old_count: usize, insert_pos: usize,
        ins_from: K, ins_to: K, ins_scope: u32, start_idx: usize, count: usize) -> Result<()> {
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

    // ── B+tree delete ──

    fn delete_range(&mut self, from: K, to: K) -> Result<()> {
        loop {
            if self.pending_root == 0 { return Ok(()); }
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
        if self.pending_root == 0 { return Ok(None); }
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
                    if r.from() > to { return Ok(None); }
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

    // ── branch ops ──

    fn branch_find_child(branch: &BranchView<'_, K>, key: K) -> usize {
        let (mut lo, mut hi) = (0usize, branch.sep_count());
        while lo < hi {
            let mid = lo + (hi - lo) / 2;
            if branch.sep(mid) <= key { lo = mid + 1; } else { hi = mid; }
        }
        lo
    }

    fn branch_update_child(&mut self, pgno: u32, child_idx: usize, new_child: u32) -> Result<()> {
        let off = if child_idx == 0 { PAGE_HEADER_SIZE }
            else { PAGE_HEADER_SIZE + 4 + (child_idx - 1) * (K::WIDTH + 4) + K::WIDTH };
        put_u32(self.store.page_mut(pgno), off, new_child);
        Ok(())
    }

    fn branch_absorb_split(&mut self, pgno: u32, child_idx: usize, left: u32, sep: K, right: u32) -> Result<Option<(K, u32)>> {
        let count = PageHeader::decode(self.store.page(pgno)).entry_count as usize;
        let branch_max = spec::branch_max(self.key_width);
        if count < branch_max {
            let page = self.store.page_mut(pgno);
            let ins_off = PAGE_HEADER_SIZE + 4 + child_idx * (K::WIDTH + 4);
            let end_off = PAGE_HEADER_SIZE + 4 + count * (K::WIDTH + 4);
            page.copy_within(ins_off..end_off, ins_off + K::WIDTH + 4);
            sep.write_le(&mut page[ins_off..ins_off + K::WIDTH]);
            put_u32(page, ins_off + K::WIDTH, right);
            let left_off = if child_idx == 0 { PAGE_HEADER_SIZE }
                else { PAGE_HEADER_SIZE + 4 + (child_idx - 1) * (K::WIDTH + 4) + K::WIDTH };
            put_u32(page, left_off, left);
            PageHeader::write(page, spec::PAGE_TYPE_BRANCH, (count + 1) as u16, pgno);
            Ok(None)
        } else {
            let mut src = [0u8; PAGE_SIZE];
            src.copy_from_slice(self.store.page(pgno));
            let total = count + 1;
            let mid = total / 2;
            self.write_branch_split(pgno, &src, count, child_idx, left, sep, right, 0, mid)?;
            let right_pgno = self.alloc_page()?;
            self.write_branch_split(right_pgno, &src, count, child_idx, left, sep, right, mid + 1, total - mid - 1)?;
            let promoted = if mid == child_idx { sep } else {
                let old_i = if mid < child_idx { mid } else { mid - 1 };
                K::read_le(&src[PAGE_HEADER_SIZE + 4 + old_i * (K::WIDTH + 4)..][..K::WIDTH])
            };
            Ok(Some((promoted, right_pgno)))
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn write_branch_split(&mut self, pgno: u32, src: &[u8], _old_count: usize, insert_idx: usize,
        ins_left: u32, ins_sep: K, ins_right: u32, start_idx: usize, sep_count: usize) -> Result<()> {
        let kw = K::WIDTH;
        let page = self.store.page_mut(pgno);
        page.fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_BRANCH, sep_count as u16, pgno);
        let first_child = if start_idx == 0 {
            if insert_idx == 0 { ins_left } else { u32_le(src, PAGE_HEADER_SIZE) }
        } else if insert_idx == start_idx { ins_right }
        else { u32_le(src, PAGE_HEADER_SIZE + 4 + (start_idx - 1) * (kw + 4) + kw) };
        put_u32(page, PAGE_HEADER_SIZE, first_child);
        for out_i in 0..sep_count {
            let abs_i = start_idx + out_i;
            let (s, c) = if abs_i == insert_idx { (ins_sep, ins_right) }
            else {
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
        let kw = K::WIDTH;
        let page = self.store.page_mut(pgno);
        page.fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_BRANCH, 1, pgno);
        put_u32(page, PAGE_HEADER_SIZE, left);
        sep.write_le(&mut page[PAGE_HEADER_SIZE + 4..PAGE_HEADER_SIZE + 4 + kw]);
        put_u32(page, PAGE_HEADER_SIZE + 4 + kw, right);
        Ok(())
    }

    // ── meta ──

    #[allow(clippy::too_many_arguments)]
    fn write_meta_page(&mut self, pgno: u32, txn_id: u64, root: u32, height: u32,
        record_count: u64, total_pages: u32, updated: u64) -> Result<()> {
        let meta = Meta {
            pgno, version_minor: spec::VERSION_MINOR, meta_size: spec::META_SIZE,
            page_size: PAGE_SIZE as u32, checksum_algo: spec::CHECKSUM_ALGO_CRC32C,
            flags: if K::WIDTH == 16 { spec::FLAG_IP_VERSION } else { 0 },
            key_width: K::WIDTH as u8, scope_mode: self.scope_mode,
            record_size: record_size::<K>() as u32, created_unixtime: self.created_unixtime,
            root_pgno: root, tree_height: height, total_pages: total_pages as u64,
            record_count, txn_id, updated_unixtime: updated,
            scope_table_root: self.scope_table_root_cache,
            free_list_head: self.free_list_head,
        };
        meta.encode_into(self.store.page_mut(pgno));
        Ok(())
    }

    // ── scan ──

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

    // ── scope table operations (mode 2 only) ──

    pub fn scope_intern(&mut self, bitmap: &[u8]) -> Result<u32> {
        match &mut self.scope_registry {
            Some(reg) => {
                let (id, was_new) = reg.intern(bitmap);
                if was_new { self.scope_dirty = true; }
                Ok(id)
            }
            None => Err(Error::State("scope_intern requires scope_mode == 2")),
        }
    }

    pub fn scope_resolve(&self, scope_id: u32) -> Option<&[u8]> {
        self.scope_registry.as_ref()?.resolve(scope_id)
    }

    pub fn scope_bitmap_set_feed(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        self.scope_dirty = true;
        match &mut self.scope_registry {
            Some(reg) => Ok(reg.bitmap_set_feed(scope_id, feed_bit)),
            None => Err(Error::State("requires scope_mode == 2")),
        }
    }

    pub fn scope_bitmap_clear_feed(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        self.scope_dirty = true;
        match &mut self.scope_registry {
            Some(reg) => Ok(reg.bitmap_clear_feed(scope_id, feed_bit)),
            None => Err(Error::State("requires scope_mode == 2")),
        }
    }

    pub(crate) fn apply_feed_bit(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        if self.scope_mode == spec::SCOPE_MODE_INDIRECT { self.scope_dirty = true; }
        match self.scope_mode {
            spec::SCOPE_MODE_BITMAP => {
                if feed_bit >= 32 { return Err(Error::InvalidInput("feed_bit >= 32 in bitmap mode")); }
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

    pub(crate) fn clear_feed_bit(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        if self.scope_mode == spec::SCOPE_MODE_INDIRECT { self.scope_dirty = true; }
        match self.scope_mode {
            spec::SCOPE_MODE_BITMAP => {
                if feed_bit >= 32 { return Err(Error::InvalidInput("feed_bit >= 32 in bitmap mode")); }
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

    pub(crate) fn fresh_feed_scope(&mut self, feed_bit: u32) -> Result<u32> {
        if self.scope_mode == spec::SCOPE_MODE_INDIRECT { self.scope_dirty = true; }
        match self.scope_mode {
            spec::SCOPE_MODE_BITMAP => {
                if feed_bit >= 32 { return Err(Error::InvalidInput("feed_bit >= 32 in bitmap mode")); }
                Ok(1u32 << feed_bit)
            }
            spec::SCOPE_MODE_INDIRECT => {
                let byte_idx = (feed_bit / 8) as usize;
                let mut bm = vec![0u8; byte_idx + 1];
                bm[byte_idx] |= 1 << (feed_bit % 8);
                match &mut self.scope_registry {
                    Some(reg) => {
                        let (id, was_new) = reg.intern(&bm);
                        if was_new { self.scope_dirty = true; }
                        Ok(id)
                    }
                    None => Err(Error::State("requires scope_mode == 2")),
                }
            }
            _ => Err(Error::State("feed operations require scope_mode 1 or 2")),
        }
    }

    // ── feed-bit range API ──

    pub fn feed_add_range(&mut self, from: K, to: K, feed_bit: u32) -> Result<()> {
        self.check()?;
        if from > to { return Err(Error::InvalidInput("from > to")); }
        let overlaps = self.collect_overlapping(from, to)?;
        for (of, ot, _) in &overlaps { self.delete(*of, *ot)?; }
        let mut cursor = from;
        for (of, ot, os) in &overlaps {
            if *of > cursor && cursor <= to {
                let gap_to = if *of <= to { of.checked_dec().unwrap_or(*of) } else { to };
                if gap_to >= cursor {
                    let ns = self.fresh_feed_scope(feed_bit)?;
                    self.cow_insert(cursor, gap_to, ns)?;
                    self.pending_record_count += 1;
                }
            }
            if *of < from {
                let trim_end = from.checked_dec().unwrap_or(from);
                self.cow_insert(*of, trim_end, *os)?;
                self.pending_record_count += 1;
            }
            let inner_from = if *of > from { *of } else { from };
            let inner_to = if *ot < to { *ot } else { to };
            let ns = self.apply_feed_bit(*os, feed_bit)?;
            self.cow_insert(inner_from, inner_to, ns)?;
            self.pending_record_count += 1;
            if *ot > to {
                let trim_start = to.checked_inc().unwrap_or(to);
                self.cow_insert(trim_start, *ot, *os)?;
                self.pending_record_count += 1;
            }
            cursor = ot.checked_inc().unwrap_or(*ot);
        }
        if cursor <= to {
            let ns = self.fresh_feed_scope(feed_bit)?;
            self.cow_insert(cursor, to, ns)?;
            self.pending_record_count += 1;
        }
        Ok(())
    }

    pub fn feed_remove_range(&mut self, from: K, to: K, feed_bit: u32) -> Result<()> {
        self.check()?;
        if from > to { return Err(Error::InvalidInput("from > to")); }
        let overlaps = self.collect_overlapping(from, to)?;
        for (of, ot, _) in &overlaps { self.delete(*of, *ot)?; }
        for (of, ot, os) in &overlaps {
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
            let inner_from = if *of > from { *of } else { from };
            let inner_to = if *ot < to { *ot } else { to };
            let ns = self.clear_feed_bit(*os, feed_bit)?;
            if ns != 0 {
                self.cow_insert(inner_from, inner_to, ns)?;
                self.pending_record_count += 1;
            }
        }
        Ok(())
    }

    fn collect_overlapping(&self, from: K, to: K) -> Result<vec::Vec<(K, K, u32)>> {
        if self.pending_root == 0 { return Ok(vec::Vec::new()); }
        let mut result = vec::Vec::new();
        self.collect_overlapping_node(self.pending_root, from, to, &mut result)?;
        Ok(result)
    }

    fn collect_overlapping_node(&self, pgno: u32, from: K, to: K, out: &mut vec::Vec<(K, K, u32)>) -> Result<()> {
        let page = self.store.page(pgno);
        let h = PageHeader::decode(page);
        match h.page_type {
            spec::PAGE_TYPE_LEAF => {
                let leaf = LeafView::<K>::new(page, h.entry_count as usize);
                for i in 0..leaf.len() {
                    let r = leaf.record(i);
                    if r.from() > to { break; }
                    if r.to() >= from { out.push((r.from(), r.to(), r.scope_id())); }
                }
                Ok(())
            }
            spec::PAGE_TYPE_BRANCH => {
                let branch = BranchView::<K>::new(page, h.entry_count as usize);
                let start = Self::branch_find_child(&branch, from);
                for j in start..branch.child_count() {
                    if j > 0 && branch.sep(j - 1) > to { break; }
                    self.collect_overlapping_node(branch.child(j), from, to, out)?;
                }
                Ok(())
            }
            _ => Err(Error::Structural("unexpected page type")),
        }
    }
}
