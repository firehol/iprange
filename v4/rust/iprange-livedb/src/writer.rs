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
use crate::wire::{finalize_checksum, put_u32, u32_le, Meta, PageHeader};

/// One KV entry as returned by `meta_list`.
pub type MetaEntry = (alloc::vec::Vec<u8>, u32, alloc::vec::Vec<u8>);

/// What a delete did.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Changed {
    Changed,
    Unchanged,
}

pub struct Writer<K: IpKey> {
    store: Box<dyn PageStore>,
    key_width: u8,
    scope_mode: u8,
    created_unixtime: u64,
    active_meta: u32,
    committed_root: u32,
    committed_height: u32,
    committed_pages: u32,
    committed_record_count: u64,
    committed_txn_id: u64,
    free_list_head: u32,
    pending_root: u32,
    pending_height: u32,
    pending_record_count: u64,
    poisoned: bool,
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
        Ok(Writer {
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
            _k: PhantomData,
        })
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
        Ok(())
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

    #[inline]
    fn cow_page(&mut self, pgno: u32) -> Result<u32> {
        if pgno >= self.committed_pages {
            return Ok(pgno);
        }
        let new = self.store.alloc_page()?;
        self.store.copy_page(pgno, new);
        Ok(new)
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
            let leaf = self.store.alloc_page()?;
            self.write_leaf_single(leaf, from, to, scope_id)?;
            self.pending_root = leaf;
            self.pending_height = 1;
            return Ok(());
        }
        let root = self.cow_root()?;
        let split = self.cow_insert_descend(root, 1, from, to, scope_id)?;
        if let Some((sep, right)) = split {
            let new_root = self.store.alloc_page()?;
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
            let right = self.store.alloc_page()?;
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
            let right_pgno = self.store.alloc_page()?;
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
            scope_table_root: 0,
            free_list_head: self.free_list_head,
        };
        meta.encode_into(self.store.page_mut(pgno));
        Ok(())
    }

    // ── scan ──────────────────────────────────────────────────────────

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
