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
use rustc_hash::FxHashMap;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Changed {
    Changed,
    Unchanged,
}

/// An overlap scan hit: `(from, to, scope_id, pgno, index_in_leaf)`.
type OverlapHit<K> = (K, K, u32, u32, usize);

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
    /// Page count of the committed IP tree (issue-8). Refreshed once at open
    /// and after each compaction; treated as approximate between compactions
    /// (COW preserves structure). Lets `compact_if_needed` compare counts
    /// instead of walking the whole tree every commit.
    committed_tree_pages: u32,
    pub(crate) pending_root: u32,
    pending_height: u32,
    pending_record_count: u64,
    poisoned: bool,
    private_pages: PageSet,
    free_pages: alloc::vec::Vec<u32>,
    free_pos: usize,
    /// Cursor into freed_this_txn for same-transaction recycling.
    /// When free_pages is exhausted, alloc_page reuses COW victims.
    /// When false, COW victims are NOT recycled (readers may reference them).
    /// Set by FileWriter based on oldest_reader_txn_id.
    pub(crate) can_recycle: bool,
    scope_registry: Option<crate::scope_table::ScopeRegistry>,
    scope_dirty: bool,
    free_list_head: u32,
    freed_this_txn: alloc::vec::Vec<u32>,
    /// Pages popped from `free_pages` and reused (made live) this transaction.
    /// At commit, each gets a tombstone entry `(pgno, u64::MAX)` appended to the
    /// chain so that newest-entry-wins in `load_free_list` marks them NOT free.
    consumed_this_txn: alloc::vec::Vec<u32>,
    _k: PhantomData<K>,
}

impl<K: IpKey> Writer<K> {
    pub fn create(scope_mode: u8, created_unixtime: u64) -> Result<Writer<K>> {
        let store: Box<dyn PageStore> = Box::new(VecPageStore::new(vec![0u8; 2 * PAGE_SIZE]));
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
            committed_tree_pages: 0,
            pending_root: 0,
            pending_height: 0,
            pending_record_count: 0,
            poisoned: false,
            private_pages: PageSet::new(2),
            free_pages: vec![],
            free_pos: 0,
            can_recycle: true,
            scope_registry: if scope_mode == spec::SCOPE_MODE_INDIRECT {
                Some(crate::scope_table::ScopeRegistry::new())
            } else {
                None
            },
            scope_dirty: false,
            free_list_head: 0,
            freed_this_txn: alloc::vec::Vec::with_capacity(4096),
            consumed_this_txn: alloc::vec::Vec::with_capacity(4096),
            _k: PhantomData,
        };
        w.write_meta_page(0, 1, 0, 0, 0, 2, 0)?;
        w.write_meta_page(1, 0, 0, 0, 0, 2, 0)?;
        w.active_meta = 0;
        w.committed_txn_id = 1;
        Ok(w)
    }

    pub fn open(store: Box<dyn PageStore>) -> Result<Writer<K>> {
        // Validate per-page CRC32C on both meta pages. A torn write or byte
        // corruption that leaves the bytes decodable (but CRC-invalid) must be
        // rejected here, not silently accepted by trusting txn_id alone. Pick
        // the active meta as the CRC-valid page with the higher txn_id; if
        // both are valid, the higher txn_id wins; if neither is valid, reject.
        let valid_a = crate::crc32c::verify_page(store.page(0));
        let valid_b = crate::crc32c::verify_page(store.page(1));
        if !valid_a && !valid_b {
            return Err(Error::Structural("both meta pages fail CRC"));
        }
        let meta_a = Meta::decode(store.page(0));
        let meta_b = Meta::decode(store.page(1));
        let (active, active_no) = if valid_a {
            if valid_b {
                if meta_a.txn_id >= meta_b.txn_id {
                    (meta_a, 0u32)
                } else {
                    (meta_b, 1u32)
                }
            } else {
                (meta_a, 0u32)
            }
        } else {
            (meta_b, 1u32)
        };
        if active.key_width != K::WIDTH as u8 {
            return Err(Error::Structural("key_width mismatch"));
        }
        if active.record_size != record_size::<K>() as u32 {
            return Err(Error::Structural("record_size mismatch"));
        }

        // Open the scope registry WITHOUT materializing the table (issue-1 fix):
        // CRC-validate every scope page (O(S) time, O(log S) heap) to preserve
        // the corruption guard, then compute next_id via a rightmost-leaf
        // descent. The committed root stays on disk; resolve/intern read it on
        // demand.
        let scope_reg = if active.scope_mode == spec::SCOPE_MODE_INDIRECT {
            crate::scope_table::validate_scope_crc(
                store.committed_bytes(),
                active.scope_table_root,
            )
            .map_err(|_| Error::Structural("corrupt scope table on open"))?;
            let next_id = crate::scope_table::read_max_scope_id(
                store.committed_bytes(),
                active.scope_table_root,
            )
            .map(|m| m + 1)
            .unwrap_or(1);
            Some(crate::scope_table::ScopeRegistry::open(
                active.scope_table_root,
                next_id,
            ))
        } else {
            None
        };

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
            committed_tree_pages: 0,
            pending_root: active.root_pgno,
            pending_height: active.tree_height,
            pending_record_count: active.record_count,
            poisoned: false,
            private_pages: PageSet::new(active.total_pages as usize),
            free_pages: vec![],
            free_pos: 0,
            can_recycle: true,
            scope_registry: scope_reg,
            scope_dirty: false,
            free_list_head: active.free_list_head,
            freed_this_txn: alloc::vec::Vec::with_capacity((active.total_pages as usize).max(4096)),
            consumed_this_txn: alloc::vec::Vec::with_capacity(4096),
            _k: PhantomData,
        };
        // Strict CRC validation of the persistent free-list chain at open time.
        crate::free_list::validate_chain_crc(w.store.as_ref(), w.free_list_head)?;
        w.load_free_list(u64::MAX);
        // Issue-8: seed committed_tree_pages once (one-time walk, not per
        // commit) so compact_if_needed can compare counts without walking.
        if w.committed_root != 0 {
            let mut pages = 0u64;
            let _ = w.count_tree_pages(w.committed_root, w.committed_height, &mut pages);
            w.committed_tree_pages = pages as u32;
        }
        Ok(w)
    }

    // ── public API ──

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

    /// Commit the pending transaction.
    /// `oldest_reader_txn_id` must be the minimum txn_id among all active
    /// readers, or `u64::MAX` if no readers are active. This is queried fresh
    /// at call time (not cached) to prevent MVCC violations from stale state.
    pub fn commit(&mut self, updated_unixtime: u64, oldest_reader_txn_id: u64) -> Result<()> {
        self.check()?;
        // Refresh MVCC state BEFORE any commit logic uses can_recycle.
        self.can_recycle = oldest_reader_txn_id == u64::MAX;

        // I7 fix: run the sparseness check ONCE per commit (not on every
        // delete). compact_if_needed walks the tree (O(tree pages)); doing it
        // per-delete was O(n²) for bulk delete. At commit it is one walk.
        self.compact_if_needed()?;

        // Rebuild scope table (mode 2) only if the registry changed.
        let scope_rebuilt = self.scope_dirty;
        if self.scope_dirty {
            // Old scope pages are committed-region pages reachable from the
            // old meta's scope_table_root. They MUST NOT be overwritten
            // in-place — a reader pinned at the old txn would see corrupted
            // scope data. Free them for reclamation at the next commit.
            let old_root = self.scope_root();
            let mut old_scope_page_count = 0;
            if old_root != 0 {
                let mut pages = alloc::vec::Vec::new();
                self.collect_scope_page_numbers(old_root, 0, &mut pages);
                old_scope_page_count = pages.len();
                self.freed_this_txn.extend(pages);
            }
            // F2 fix: pre-populate free_pool from the Writer's free-list so
            // that scope page allocation reuses freed pages instead of
            // extending the file. The old scope pages freed above are not
            // available yet (they're in the current txn's free chain), but
            // pages freed in PREVIOUS transactions are in free_pages.
            let mut free_pool = Vec::new();
            let estimate = old_scope_page_count + 2;
            for _ in 0..estimate {
                if self.free_pos < self.free_pages.len() {
                    let pgno = self.free_pages[self.free_pos];
                    self.free_pos += 1;
                    self.private_pages.insert(pgno);
                    free_pool.push(pgno);
                } else {
                    break;
                }
            }
            // Remember the speculative pre-pop so we can tombstone the pages
            // actually consumed by the scope rebuild. build_scope_tree pops the
            // pages it needs from free_pool; the rest are returned below. Those
            // consumed pages are now live scope data and must NOT reappear as
            // free, so they are recorded for tombstoning at commit.
            let scope_pool_snapshot: alloc::vec::Vec<u32> = free_pool.clone();
            // EntriesForCommit re-reads the committed table (still at old_root,
            // since promote has not run) and merges this-txn new entries. The
            // committed_bytes borrow is scoped so self.store is free for the
            // mutable build_scope_tree call below.
            let mut new_root: u32 = 0;
            if let Some(reg) = self.scope_registry.as_mut() {
                if !reg.is_empty() {
                    let all = {
                        let bytes = self.store.committed_bytes();
                        reg.entries_for_commit(bytes)
                    };
                    let mut allocated = Vec::new();
                    let root = crate::scope_table::build_scope_tree(
                        self.store.as_mut(),
                        &all,
                        &mut allocated,
                        &mut free_pool,
                    )?;
                    for pgno in &allocated {
                        self.private_pages.insert(*pgno);
                    }
                    new_root = root;
                }
            }
            // Advance the registry to the newly-committed root (folds new
            // entries into the warm committed index, clears the new set).
            if let Some(reg) = self.scope_registry.as_mut() {
                reg.promote(new_root);
            }
            // Return unused free_pool pages to the Writer's free-list and
            // tombstone the ones build_scope_tree actually consumed.
            let mut returned: std::collections::HashSet<u32> = std::collections::HashSet::new();
            for pgno in free_pool.drain(..) {
                if self.free_pos > 0 {
                    self.free_pos -= 1;
                }
                self.free_pages[self.free_pos] = pgno;
                self.private_pages.remove(pgno);
                returned.insert(pgno);
            }
            for pgno in scope_pool_snapshot {
                if !returned.contains(&pgno) {
                    self.consumed_this_txn.push(pgno);
                }
            }
            self.scope_dirty = false;
        }
        // Finalize CRCs on all private pages.
        for pgno in self.private_pages.iter() {
            if pgno >= 2 {
                finalize_checksum(self.store.page_mut(pgno));
            }
        }
        // Tag for freed entries written this commit: the generation being
        // superseded (MVCC reclamation uses strict <).
        let new_txn_id_val = self.committed_txn_id;

        // Fast path: nothing was freed, nothing consumed, and no scope rebuild.
        // The existing append-only chain is still valid, so skip the append.
        let nothing_freed = self.freed_this_txn.is_empty();
        let nothing_consumed = self.consumed_this_txn.is_empty();
        if nothing_freed && nothing_consumed && !scope_rebuilt {
            // The existing chain is valid — just write the meta and flip.
            let total = self.store.total_pages();
            let inactive = 1 - self.active_meta;
            let new_txn_id = self.committed_txn_id + 1;
            self.write_meta_page(
                inactive,
                new_txn_id,
                self.pending_root,
                self.pending_height,
                self.pending_record_count,
                total,
                updated_unixtime,
            )?;
            self.store.sync()?;
            self.active_meta = inactive;
            self.committed_txn_id = new_txn_id;
            self.committed_root = self.pending_root;
            self.committed_height = self.pending_height;
            self.committed_record_count = self.pending_record_count;
            self.committed_pages = total;
            self.store.set_committed_pages(total);
            self.reset_txn();
            self.load_free_list(oldest_reader_txn_id);
            return Ok(());
        }

        // ── Tombstone append-only free-list commit ───────────────────────────
        //
        // The chain grows monotonically: we append ONE new segment holding this
        // transaction's freed entries (COW victims + old scope pages) tagged
        // with `new_txn_id_val`, plus tombstone entries (`u64::MAX`) for every
        // page that is LIVE this commit but was consumed (reused) from the
        // free-list. `load_free_list` resolves the final free set with
        // newest-entry-wins, so a page that is freed and later reused never
        // reappears as free after close/reopen. Chain pages themselves are
        // excluded from the free set by `load_free_list` (they appear in
        // `read_chain_page_numbers`), so they need no tombstone.
        //
        // Tombstone rule: a page popped from the free-list is tombstoned ONLY
        // if it is still live at commit (in private_pages). A page that was
        // consumed and then freed again in the SAME transaction (e.g. a COW
        // copy from the delete-all collapse path) is no longer live, so it is
        // NOT tombstoned — its freed entry makes it correctly free.
        let live_consumed: alloc::vec::Vec<u32> = self
            .consumed_this_txn
            .iter()
            .filter(|&&p| self.private_pages.contains(p))
            .copied()
            .collect();

        // Effective free set for truncation = (previously free ∪ freed this txn)
        // − live-consumed. Live-consumed pages are LIVE and must never be
        // truncated, so they are excluded from the trailing scan.
        let pre_truncate_total = self.store.total_pages();
        let trailing: u32 = if oldest_reader_txn_id == u64::MAX {
            let mut eff: std::collections::HashSet<u32> = std::collections::HashSet::new();
            for &p in &self.free_pages {
                eff.insert(p);
            }
            for &p in &self.freed_this_txn {
                eff.insert(p);
            }
            for &p in &live_consumed {
                eff.remove(&p);
            }
            let mut v: alloc::vec::Vec<u32> = eff.into_iter().collect();
            v.sort();
            crate::free_list::trailing_free_count(&v, pre_truncate_total)
        } else {
            0
        };
        let new_total = pre_truncate_total - trailing;

        // Build entries: freed (drop trailing pages that will be truncated) and
        // tombstones for live-consumed pages (which are live, so never trailing).
        let mut entries_to_write: alloc::vec::Vec<crate::free_list::FreeEntry> =
            alloc::vec::Vec::with_capacity(self.freed_this_txn.len() + live_consumed.len());
        for &pgno in &self.freed_this_txn {
            if pgno < new_total {
                entries_to_write.push(crate::free_list::FreeEntry {
                    pgno,
                    freed_txn_id: new_txn_id_val,
                });
            }
        }
        for &pgno in &live_consumed {
            // Live-consumed pages are live (tree/scope data) ⇒ all < new_total.
            // Guard defensively anyway.
            if pgno < new_total {
                entries_to_write.push(crate::free_list::FreeEntry {
                    pgno,
                    freed_txn_id: u64::MAX,
                });
            }
        }

        // Sort by freed_txn_id: tombstones (MAX) sort after freed entries, so
        // they are written last and become the newest pages in the chain —
        // newest-entry-wins then gives tombstones priority for reused pages.
        entries_to_write.sort_by_key(|e| e.freed_txn_id);

        // A1 fix: when the existing chain is large (≥20 pages), compact it
        // instead of appending. This reads all old entries, merges with new
        // entries (newest-wins dedup), filters tombstones, and rewrites as a
        // single clean chain. Old chain pages are freed.
        let old_chain_pages = if self.free_list_head != 0 {
            crate::free_list::read_chain_page_numbers(self.store.as_ref(), self.free_list_head)
        } else {
            Vec::new()
        };

        let mut chain_pages: alloc::vec::Vec<u32>;

        if old_chain_pages.len() >= 20 {
            // Compaction path: read old entries, deduplicate, rewrite.
            let old_entries = if self.free_list_head != 0 {
                crate::free_list::read_chain(self.store.as_ref(), self.free_list_head)
                    .unwrap_or_default()
            } else {
                Vec::new()
            };

            // Merge: new entries take priority (they are this txn's state).
            let mut merged: std::collections::HashMap<u32, u64> = std::collections::HashMap::new();
            for e in &old_entries {
                merged.entry(e.pgno).or_insert(e.freed_txn_id);
            }
            for e in &entries_to_write {
                merged.insert(e.pgno, e.freed_txn_id);
            }

            let mut compact_entries: Vec<crate::free_list::FreeEntry> = merged
                .iter()
                .filter(|(_, &ftxn)| ftxn != u64::MAX)
                .map(|(&pgno, &ftxn)| crate::free_list::FreeEntry {
                    pgno,
                    freed_txn_id: ftxn,
                })
                .filter(|e| e.pgno < new_total)
                .collect();

            // Old chain pages are being replaced by the compact chain.
            // Add them as freed entries so they appear in the compact chain
            // and can be reused by future transactions.
            for &pgno in &old_chain_pages {
                if pgno < new_total {
                    compact_entries.push(crate::free_list::FreeEntry {
                        pgno,
                        freed_txn_id: new_txn_id_val,
                    });
                }
            }
            compact_entries.sort_by_key(|e| e.freed_txn_id);

            let needed = crate::free_list::chain_page_count(&compact_entries);
            chain_pages = alloc::vec::Vec::with_capacity(needed);
            while chain_pages.len() < needed {
                chain_pages.push(self.alloc_chain_page()?);
            }
            let chain_set: std::collections::HashSet<u32> = chain_pages.iter().copied().collect();
            compact_entries.retain(|e| !chain_set.contains(&e.pgno));

            self.free_list_head = if compact_entries.is_empty() {
                0
            } else {
                crate::free_list::write_chain(self.store.as_mut(), &compact_entries, &chain_pages)?
            };
        } else {
            // Append-only path: prepend new segment to existing chain.
            let needed = crate::free_list::chain_page_count(&entries_to_write);
            chain_pages = alloc::vec::Vec::with_capacity(needed);
            while chain_pages.len() < needed {
                chain_pages.push(self.alloc_chain_page()?);
            }

            let old_head = self.free_list_head;
            self.free_list_head = crate::free_list::append_segment(
                self.store.as_mut(),
                &entries_to_write,
                &chain_pages,
                old_head,
            )?;
        }

        // F9 fix: finalize CRCs on the newly written chain pages.
        for &pgno in &chain_pages {
            finalize_checksum(self.store.page_mut(pgno));
        }

        // Truncate. Chain pages allocated from the growth region may sit at
        // positions >= new_total; preserve them so they are not truncated away.
        let max_chain_page = chain_pages.iter().copied().max().unwrap_or(0);
        let total = if trailing > 0 {
            let effective_total = new_total.max(max_chain_page + 1);
            if effective_total < pre_truncate_total {
                self.store.truncate(effective_total)?;
            }
            effective_total
        } else {
            // No trailing free pages to reclaim, BUT chain pages may still have
            // been allocated beyond pre_truncate_total (in the growth region,
            // because alloc_chain_page extends the store). committed_pages MUST
            // cover the highest chain page: otherwise, after close+reopen, the
            // free-list head points past committed_pages and load_free_list
            // silently drops it (the chain page looks out-of-bounds), so freed
            // pages are never reclaimed and the file grows ~1 page per reopen.
            pre_truncate_total.max(max_chain_page + 1)
        };

        let inactive = 1 - self.active_meta;
        let new_txn_id = self.committed_txn_id + 1;
        self.write_meta_page(
            inactive,
            new_txn_id,
            self.pending_root,
            self.pending_height,
            self.pending_record_count,
            total,
            updated_unixtime,
        )?;
        self.store.sync()?;
        self.active_meta = inactive;
        self.committed_txn_id = new_txn_id;
        self.committed_root = self.pending_root;
        self.committed_height = self.pending_height;
        self.committed_record_count = self.pending_record_count;
        self.committed_pages = total;
        self.store.set_committed_pages(total);
        self.reset_txn();
        self.load_free_list(oldest_reader_txn_id);

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
    pub fn committed_pages(&self) -> u32 {
        self.committed_pages
    }

    /// Number of pages currently in the in-memory free-list (reclaimable pool).
    /// Reflects the result of the last `load_free_list`: newest-entry-wins over
    /// the append-only chain, with tombstones and chain pages excluded. Used by
    /// tests/audits to verify the tombstone invariant (a consumed page must not
    /// reappear here after close/reopen).
    pub fn free_page_count(&self) -> usize {
        self.free_pages.len()
    }

    pub fn into_image(self) -> Option<alloc::vec::Vec<u8>> {
        self.store.into_vec()
    }

    // ── COW mechanics ──

    fn check(&self) -> Result<()> {
        if self.poisoned {
            Err(Error::State("writer poisoned"))
        } else {
            Ok(())
        }
    }

    fn cow_page(&mut self, pgno: u32) -> Result<u32> {
        if self.private_pages.contains(pgno) {
            return Ok(pgno);
        }
        let new = self.alloc_page()?;
        self.store.copy_page(pgno, new);
        self.private_pages.insert(new);
        self.freed_this_txn.push(pgno);
        Ok(new)
    }

    fn cow_root(&mut self) -> Result<u32> {
        if self.pending_root == 0 {
            return Ok(0);
        }
        let new = self.cow_page(self.pending_root)?;
        self.pending_root = new;
        Ok(new)
    }

    fn alloc_page(&mut self) -> Result<u32> {
        if self.free_pos < self.free_pages.len() {
            let pgno = self.free_pages[self.free_pos];
            self.free_pos += 1;
            self.private_pages.insert(pgno);
            // Track for tombstone at commit: this page was free and is now live.
            self.consumed_this_txn.push(pgno);
            Ok(pgno)
        } else {
            let pgno = self.store.alloc_page()?;
            self.private_pages.ensure_capacity(pgno as usize + 1);
            self.private_pages.insert(pgno);
            Ok(pgno)
        }
    }

    /// Allocate a page for free-list chain metadata. Like [`alloc_page`] but
    /// does NOT record the page in `consumed_this_txn`: chain pages are
    /// excluded from the free-list by [`load_free_list`] (they appear in
    /// `read_chain_page_numbers`), so they need no tombstone entry. This breaks
    /// what would otherwise be a circular dependency (chain page count depends
    /// on entries, which would depend on tombstones for the chain pages).
    fn alloc_chain_page(&mut self) -> Result<u32> {
        if self.free_pos < self.free_pages.len() {
            let pgno = self.free_pages[self.free_pos];
            self.free_pos += 1;
            self.private_pages.insert(pgno);
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
    ///
    /// Applies **newest-entry-wins** semantics over the append-only chain: for
    /// each pgno, the most recent entry (first in chain order, since
    /// [`crate::free_list::read_chain`] returns newest-first) determines state.
    /// A tombstone entry (`freed_txn_id == u64::MAX`) means the page was reused
    /// and is NOT free. A normal entry means free, subject to MVCC filtering
    /// (`freed_txn_id < oldest_reader_txn_id`, or all reclaimable when MAX).
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
        // Chain pages are live metadata and must never be handed out as free,
        // even if an older segment still lists them as freed. Exclude them.
        let chain_page_set: alloc::vec::Vec<u32> =
            crate::free_list::read_chain_page_numbers(self.store.as_ref(), self.free_list_head);
        let mut chain_set: std::collections::HashSet<u32> = std::collections::HashSet::new();
        for p in &chain_page_set {
            chain_set.insert(*p);
        }
        // Newest-entry-wins: entries are newest-first, so the first occurrence
        // of each pgno is its most recent state.
        let mut latest: FxHashMap<u32, u64> = FxHashMap::default();
        for e in &entries {
            latest.entry(e.pgno).or_insert(e.freed_txn_id);
        }
        self.free_pages = latest
            .iter()
            .filter(|(&pgno, &ftxn)| {
                if chain_set.contains(&pgno) {
                    return false;
                }
                ftxn != u64::MAX
                    && (oldest_reader_txn_id == u64::MAX || ftxn < oldest_reader_txn_id)
            })
            .map(|(&pgno, _)| pgno)
            .collect();
        // Filter out entries beyond the current store (truncated pages).
        let total = self.store.total_pages();
        self.free_pages.retain(|&p| p < total);
        self.free_pages.sort(); // Rule 5: prefer low-numbered pages
        self.free_pos = 0;
    }

    fn reset_txn(&mut self) {
        self.private_pages.clear();
        self.freed_this_txn.clear();
        self.consumed_this_txn.clear();
        self.private_pages
            .ensure_capacity(self.store.total_pages() as usize);
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

    fn cow_insert_descend(
        &mut self,
        pgno: u32,
        from: K,
        to: K,
        scope_id: u32,
    ) -> Result<Option<(K, u32)>> {
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
            } else {
                Ok(None)
            }
        }
    }

    fn leaf_insert(
        &mut self,
        pgno: u32,
        from: K,
        to: K,
        scope_id: u32,
    ) -> Result<Option<(K, u32)>> {
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
            self.write_leaf_split(
                right,
                &src,
                count,
                pos,
                from,
                to,
                scope_id,
                mid,
                new_count - mid,
            )?;
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
        &mut self,
        pgno: u32,
        src: &[u8],
        _old_count: usize,
        insert_pos: usize,
        ins_from: K,
        ins_to: K,
        ins_scope: u32,
        start_idx: usize,
        count: usize,
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
        record::write::<K>(
            &mut page[PAGE_HEADER_SIZE..PAGE_HEADER_SIZE + rs],
            from,
            to,
            scope_id,
        );
        Ok(())
    }

    // ── B+tree delete ──

    fn delete_range(&mut self, from: K, to: K) -> Result<()> {
        loop {
            if self.pending_root == 0 {
                return Ok(());
            }
            let overlap = self.scan_first_overlap(from, to)?;
            match overlap {
                None => break,
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
        // F7 fix: collapse tree when all records are deleted. The COW
        // copies (private_pages) are now unreachable from pending_root.
        // Move them to freed_this_txn so they go to the free-list chain
        // at commit and can be reused in future transactions.
        if self.pending_record_count == 0 && self.pending_root != 0 {
            for pgno in self.private_pages.iter() {
                if pgno >= 2 {
                    self.freed_this_txn.push(pgno);
                }
            }
            // Clear private_pages for the remaining CRC loop — these pages
            // are now free, not live. But we must still CRC-finalize the
            // chain pages that will be allocated in commit. Keep the
            // private_pages bitset for the CRC pass (it only finalizes
            // pages >= 2 that are in the set, and we just pushed them all
            // to freed_this_txn). Clear the set so the CRC pass skips them.
            self.private_pages.clear();
            self.pending_root = 0;
            self.pending_height = 0;
        }

        // I7 fix: compaction is deferred to Commit (one walk per commit, not
        // per delete). Calling compact_if_needed here made every delete
        // O(tree pages).

        Ok(())
    }

    /// Check if the pending tree is sparse (many pages, few records) and
    /// rebuild it compactly. Only triggers when the tree is <25% full.
    ///
    /// Issue-8: no per-commit tree walk. `committed_tree_pages` (refreshed once
    /// at open and after each compaction) is compared against the page count a
    /// dense tree would need for `pending_record_count` records. Between
    /// compactions the value is approximate (COW preserves structure); since it
    /// only ever under-counts (appends grow the actual tree, deletes don't
    /// shrink it), a false trigger is avoided and a genuinely sparse tree still
    /// trips the threshold once the stale count exceeds 4× the dense estimate.
    fn compact_if_needed(&mut self) -> Result<()> {
        if self.pending_root == 0 || self.pending_record_count == 0 {
            return Ok(());
        }
        // Lazy seed (issue-8): a freshly created Writer has no cached count.
        // Open seeds it for reopened files; for created files we count once on
        // the first commit, then rely on the approximation + compaction
        // refresh. This walk happens at most once per Writer session — not
        // every commit.
        if self.committed_tree_pages == 0 {
            let mut pages = 0u64;
            let _ = self.count_tree_pages(self.pending_root, self.pending_height, &mut pages);
            self.committed_tree_pages = pages as u32;
        }
        let tree_pages = self.committed_tree_pages as u64;
        let needed_pages = expected_tree_pages(self.key_width, self.pending_record_count);
        // Only compact if the tree is at least 4x larger than needed.
        if tree_pages > needed_pages * 4 + 4 {
            self.rebuild_compact()?;
            // Refresh the count from the freshly-rebuilt (small) tree.
            let mut pages = 0u64;
            let _ = self.count_tree_pages(self.pending_root, self.pending_height, &mut pages);
            self.committed_tree_pages = pages as u32;
        }
        Ok(())
    }

    fn count_tree_pages(&self, pgno: u32, height: u32, count: &mut u64) -> Result<()> {
        *count += 1;
        if height <= 1 {
            return Ok(());
        }
        let page = self.store.page(pgno);
        let h = PageHeader::decode(page);
        if h.page_type == spec::PAGE_TYPE_BRANCH {
            let branch = BranchView::<K>::new(page, h.entry_count as usize);
            for j in 0..branch.child_count() {
                self.count_tree_pages(branch.child(j), height - 1, count)?;
            }
        }
        Ok(())
    }

    /// Collect leaf page numbers of the pending tree in tree (left-to-right)
    /// order. Used by `rebuild_compact` to stream leaves one at a time, and by
    /// the foreign_vs_all linear merge (issue-5).
    pub(crate) fn pending_leaf_pages(&self) -> Result<Vec<u32>> {
        let mut out = Vec::new();
        self.collect_leaf_pages(self.pending_root, self.pending_height, &mut out)?;
        Ok(out)
    }

    fn rebuild_compact(&mut self) -> Result<()> {
        // Streaming rebuild: bound memory to one leaf's worth of records.
        //
        // Leaves are processed in tree (key) order, which yields globally
        // sorted records → a densely packed tree. Only one leaf's records are
        // buffered at a time.
        //
        // Only PRIVATE pages (uncommitted COW copies) may be recycled. Pages
        // still shared with the committed tree may be referenced by pinned
        // readers, so they are read for their records but never freed here —
        // they are reclaimed later via MVCC reclamation of the superseded
        // generation.
        //
        // Safety (cross-leaf overwrite): each private leaf is returned to the
        // free pool only AFTER its records have been buffered. When leaf L_k is
        // being read, the pool holds only non-leaf pages and the private leaves
        // processed before L_k — never L_k itself or any later leaf — so the
        // allocator cannot clobber an unread leaf. This holds for any
        // processing order; tree order is chosen for dense packing.
        //
        // Placement: each freed leaf is inserted at its sorted position within
        // the unconsumed tail of the free pool, so the allocator keeps
        // consuming the lowest page numbers first. The new compact tree then
        // lands in the lowest available pages and the trailing gap above it is
        // truncated away at commit. (Sorted-insert is O(n) per leaf; rebuild is
        // a rare operation triggered only when the tree is ≥4× sparse, and the
        // O(n²) total is dominated by the record re-insertion work.)
        //
        // The free pool is rebuilt clean (deduped, sorted, cursor reset) from
        // the unconsumed tail plus the non-leaf private pages, so pages already
        // consumed earlier in the transaction are not re-offered.
        let old_root = self.pending_root;
        let old_height = self.pending_height;

        // Collect old leaf page numbers in tree (left-to-right = key) order.
        let mut leaf_pages: Vec<u32> = Vec::new();
        self.collect_leaf_pages(old_root, old_height, &mut leaf_pages)?;
        let leaf_set: std::collections::HashSet<u32> = leaf_pages.iter().copied().collect();

        // New pool = unconsumed tail ∪ old non-leaf private pages (disjoint,
        // sorted). Old leaf pages are returned to the pool one-at-a-time after
        // buffering (private) or not at all (shared).
        let mut new_free: Vec<u32> = self.free_pages[self.free_pos..].to_vec();
        let mut non_leaf: Vec<u32> = self
            .private_pages
            .iter()
            .filter(|&p| p >= 2 && !leaf_set.contains(&p))
            .collect();
        non_leaf.sort_unstable();
        for &pgno in &non_leaf {
            self.freed_this_txn.push(pgno);
            new_free.push(pgno);
        }
        new_free.sort_unstable();
        self.free_pages = new_free;
        self.free_pos = 0;
        // Capture private-leaf set before clearing private_pages.
        let private_leaf: std::collections::HashSet<u32> = leaf_pages
            .iter()
            .copied()
            .filter(|&p| self.private_pages.contains(p))
            .collect();
        self.private_pages.clear();
        self.pending_root = 0;
        self.pending_height = 0;
        self.pending_record_count = 0;

        // Single pass in tree (key) order: dense insertion. Private leaves are
        // recycled after buffering; shared leaves are read but kept in place.
        let mut buf: Vec<(K, K, u32)> = Vec::new();
        for &leaf_pgno in &leaf_pages {
            buf.clear();
            {
                let page = self.store.page(leaf_pgno);
                let h = PageHeader::decode(page);
                let leaf = LeafView::<K>::new(page, h.entry_count as usize);
                for i in 0..leaf.len() {
                    let r = leaf.record(i);
                    buf.push((r.from(), r.to(), r.scope_id()));
                }
            }
            if private_leaf.contains(&leaf_pgno) {
                // Records are owned in `buf`; safe to recycle this leaf now.
                self.freed_this_txn.push(leaf_pgno);
                // Insert into the unconsumed tail at its sorted position so the
                // allocator keeps reusing the lowest page numbers first.
                let lo = self.free_pos;
                let pos = self.free_pages[lo..].partition_point(|&x| x < leaf_pgno) + lo;
                self.free_pages.insert(pos, leaf_pgno);
            }
            for (from, to, scope_id) in buf.drain(..) {
                self.cow_insert(from, to, scope_id)?;
                self.pending_record_count += 1;
            }
        }
        Ok(())
    }

    /// Collect leaf page numbers of the pending tree in tree (left-to-right)
    /// order. Used by `rebuild_compact` to stream leaves one at a time.
    fn collect_leaf_pages(&self, pgno: u32, height: u32, out: &mut Vec<u32>) -> Result<()> {
        if height <= 1 {
            out.push(pgno);
            return Ok(());
        }
        let page = self.store.page(pgno);
        let h = PageHeader::decode(page);
        let branch = BranchView::<K>::new(page, h.entry_count as usize);
        for j in 0..branch.child_count() {
            self.collect_leaf_pages(branch.child(j), height - 1, out)?;
        }
        Ok(())
    }

    fn scan_first_overlap(&self, from: K, to: K) -> Result<Option<OverlapHit<K>>> {
        if self.pending_root == 0 {
            return Ok(None);
        }
        self.scan_overlap_node(self.pending_root, from, to)
    }

    fn scan_overlap_node(&self, pgno: u32, from: K, to: K) -> Result<Option<OverlapHit<K>>> {
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
                    // I7: separator-based early exit. The separator at j-1 is
                    // the lower bound of child j's key range. If it exceeds
                    // `to`, child j and all later children can't overlap
                    // [from, to] — stop scanning. Without this, a point delete
                    // re-scans ALL remaining leaves on the second scan.
                    if j > 0 {
                        let sep = branch.sep(j - 1);
                        if sep > to {
                            return Ok(None);
                        }
                    }
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
            if count == 0 {
                K::MIN
            } else {
                LeafView::<K>::new(page, count).record(0).from()
            }
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
        &mut self,
        pgno: u32,
        child_idx: usize,
        left: u32,
        sep: K,
        right: u32,
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
            let mut src = [0u8; PAGE_SIZE];
            src.copy_from_slice(self.store.page(pgno));
            let total = count + 1;
            let mid = total / 2;
            self.write_branch_split(pgno, &src, count, child_idx, left, sep, right, 0, mid)?;
            let right_pgno = self.alloc_page()?;
            self.write_branch_split(
                right_pgno,
                &src,
                count,
                child_idx,
                left,
                sep,
                right,
                mid + 1,
                total - mid - 1,
            )?;
            let promoted = if mid == child_idx {
                sep
            } else {
                let old_i = if mid < child_idx { mid } else { mid - 1 };
                K::read_le(&src[PAGE_HEADER_SIZE + 4 + old_i * (K::WIDTH + 4)..][..K::WIDTH])
            };
            Ok(Some((promoted, right_pgno)))
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn write_branch_split(
        &mut self,
        pgno: u32,
        src: &[u8],
        _old_count: usize,
        insert_idx: usize,
        ins_left: u32,
        ins_sep: K,
        ins_right: u32,
        start_idx: usize,
        sep_count: usize,
    ) -> Result<()> {
        let kw = K::WIDTH;
        let page = self.store.page_mut(pgno);
        page.fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_BRANCH, sep_count as u16, pgno);

        // 6-case first_child logic (mirrors Go writer.go:1016-1030).
        let first_child = if start_idx == 0 && insert_idx == 0 {
            ins_left
        } else if start_idx == 0 {
            u32_le(src, PAGE_HEADER_SIZE)
        } else if start_idx == insert_idx {
            ins_left
        } else if start_idx == insert_idx + 1 {
            ins_right
        } else if start_idx < insert_idx {
            // Old child at position start_idx (no shift needed, insertion is after).
            u32_le(src, PAGE_HEADER_SIZE + 4 + (start_idx - 1) * (kw + 4) + kw)
        } else {
            // start_idx > insert_idx + 1: the insertion shifted this child left by 1.
            u32_le(src, PAGE_HEADER_SIZE + 4 + (start_idx - 2) * (kw + 4) + kw)
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
    fn write_meta_page(
        &mut self,
        pgno: u32,
        txn_id: u64,
        root: u32,
        height: u32,
        record_count: u64,
        total_pages: u32,
        updated: u64,
    ) -> Result<()> {
        let meta = Meta {
            pgno,
            version_minor: spec::VERSION_MINOR,
            meta_size: spec::META_SIZE,
            page_size: PAGE_SIZE as u32,
            checksum_algo: spec::CHECKSUM_ALGO_CRC32C,
            flags: if K::WIDTH == 16 {
                spec::FLAG_IP_VERSION
            } else {
                0
            },
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
            scope_table_root: self.scope_root(),
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

    /// The committed scope-table root (registry is the single source of
    /// truth), or 0 when there is no scope table.
    pub(crate) fn scope_root(&self) -> u32 {
        self.scope_registry
            .as_ref()
            .map(|r| r.committed_root())
            .unwrap_or(0)
    }

    pub fn scope_intern(&mut self, bitmap: &[u8]) -> Result<u32> {
        if bitmap.len() > crate::scope_table::MAX_BITMAP_WIDTH {
            return Err(Error::InvalidInput("bitmap exceeds 256 bytes (2048 feeds)"));
        }
        let bytes = self.store.committed_bytes();
        match &mut self.scope_registry {
            Some(reg) => {
                let (id, was_new) = reg.intern(bitmap, bytes);
                if was_new {
                    self.scope_dirty = true;
                }
                Ok(id)
            }
            None => Err(Error::State("scope_intern requires scope_mode == 2")),
        }
    }

    /// Resolve a scope_id to its bitmap. Returns an owned Vec because
    /// committed scopes are decoded from the page image via find_scope.
    pub fn scope_resolve(&self, scope_id: u32) -> Option<Vec<u8>> {
        let bytes = self.store.committed_bytes();
        self.scope_registry.as_ref()?.resolve(scope_id, bytes)
    }

    /// Zero-copy scope resolve returning a slice borrowing the committed page
    /// image (issue-6). Used by the all-to-all / foreign-vs-all overlap scans,
    /// which resolve one bitmap per record and only need to iterate set bits.
    pub fn scope_resolve_ref(&self, scope_id: u32) -> Option<&[u8]> {
        let bytes = self.store.committed_bytes();
        self.scope_registry.as_ref()?.resolve_ref(scope_id, bytes)
    }

    /// Number of pages in the current scope-table tree (mode 2 only).
    /// Returns 0 when there is no scope table. Used by tests/audits to verify
    /// that scope pages are reused across commits rather than accumulated.
    pub fn scope_page_count(&self) -> usize {
        let root = self.scope_root();
        if root == 0 {
            return 0;
        }
        let mut pages = alloc::vec::Vec::new();
        self.collect_scope_page_numbers(root, 0, &mut pages);
        pages.len()
    }

    /// Number of pages in the committed IP tree (tests/audits). Walks the tree,
    /// so it is NOT on the hot path — `compact_if_needed` uses the cached
    /// `committed_tree_pages` estimate instead.
    pub fn tree_page_count(&self) -> u64 {
        if self.committed_root == 0 {
            return 0;
        }
        let mut pages = 0u64;
        let _ = self.count_tree_pages(self.committed_root, self.committed_height, &mut pages);
        pages
    }

    pub fn scope_bitmap_set_feed(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        self.scope_dirty = true;
        let bytes = self.store.committed_bytes();
        match &mut self.scope_registry {
            Some(reg) => Ok(reg.bitmap_set_feed(scope_id, feed_bit, bytes)),
            None => Err(Error::State("requires scope_mode == 2")),
        }
    }

    pub fn scope_bitmap_clear_feed(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        self.scope_dirty = true;
        let bytes = self.store.committed_bytes();
        match &mut self.scope_registry {
            Some(reg) => Ok(reg.bitmap_clear_feed(scope_id, feed_bit, bytes)),
            None => Err(Error::State("requires scope_mode == 2")),
        }
    }

    pub(crate) fn apply_feed_bit(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        if self.scope_mode == spec::SCOPE_MODE_INDIRECT {
            self.scope_dirty = true;
        }
        let bytes = self.store.committed_bytes();
        match self.scope_mode {
            spec::SCOPE_MODE_BITMAP => {
                if feed_bit >= 32 {
                    return Err(Error::InvalidInput("feed_bit >= 32 in bitmap mode"));
                }
                Ok(scope_id | (1u32 << feed_bit))
            }
            spec::SCOPE_MODE_INDIRECT => match &mut self.scope_registry {
                Some(reg) => Ok(reg.bitmap_set_feed(scope_id, feed_bit, bytes)),
                None => Err(Error::State("requires scope_mode == 2")),
            },
            _ => Err(Error::State("feed operations require scope_mode 1 or 2")),
        }
    }

    pub(crate) fn clear_feed_bit(&mut self, scope_id: u32, feed_bit: u32) -> Result<u32> {
        if self.scope_mode == spec::SCOPE_MODE_INDIRECT {
            self.scope_dirty = true;
        }
        let bytes = self.store.committed_bytes();
        match self.scope_mode {
            spec::SCOPE_MODE_BITMAP => {
                if feed_bit >= 32 {
                    return Err(Error::InvalidInput("feed_bit >= 32 in bitmap mode"));
                }
                Ok(scope_id & !(1u32 << feed_bit))
            }
            spec::SCOPE_MODE_INDIRECT => match &mut self.scope_registry {
                Some(reg) => Ok(reg.bitmap_clear_feed(scope_id, feed_bit, bytes)),
                None => Err(Error::State("requires scope_mode == 2")),
            },
            _ => Err(Error::State("feed operations require scope_mode 1 or 2")),
        }
    }

    pub(crate) fn fresh_feed_scope(&mut self, feed_bit: u32) -> Result<u32> {
        if self.scope_mode == spec::SCOPE_MODE_INDIRECT {
            self.scope_dirty = true;
        }
        match self.scope_mode {
            spec::SCOPE_MODE_BITMAP => {
                if feed_bit >= 32 {
                    return Err(Error::InvalidInput("feed_bit >= 32 in bitmap mode"));
                }
                Ok(1u32 << feed_bit)
            }
            spec::SCOPE_MODE_INDIRECT => {
                let byte_idx = (feed_bit / 8) as usize;
                let mut bm = vec![0u8; byte_idx + 1];
                bm[byte_idx] |= 1 << (feed_bit % 8);
                match &mut self.scope_registry {
                    Some(reg) => {
                        let bytes = self.store.committed_bytes();
                        let (id, was_new) = reg.intern(&bm, bytes);
                        if was_new {
                            self.scope_dirty = true;
                        }
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
        if from > to {
            return Err(Error::InvalidInput("from > to"));
        }
        let overlaps = self.collect_overlapping(from, to)?;
        for (of, ot, _) in &overlaps {
            self.delete(*of, *ot)?;
        }
        let mut cursor = from;
        for (of, ot, os) in &overlaps {
            if *of > cursor && cursor <= to {
                let gap_to = if *of <= to {
                    of.checked_dec().unwrap_or(*of)
                } else {
                    to
                };
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
        if from > to {
            return Err(Error::InvalidInput("from > to"));
        }
        let overlaps = self.collect_overlapping(from, to)?;
        for (of, ot, _) in &overlaps {
            self.delete(*of, *ot)?;
        }
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
        if self.pending_root == 0 {
            return Ok(vec::Vec::new());
        }
        let mut result = vec::Vec::new();
        self.collect_overlapping_node(self.pending_root, from, to, &mut result)?;
        Ok(result)
    }

    fn collect_overlapping_node(
        &self,
        pgno: u32,
        from: K,
        to: K,
        out: &mut vec::Vec<(K, K, u32)>,
    ) -> Result<()> {
        let page = self.store.page(pgno);
        let h = PageHeader::decode(page);
        match h.page_type {
            spec::PAGE_TYPE_LEAF => {
                let leaf = LeafView::<K>::new(page, h.entry_count as usize);
                for i in 0..leaf.len() {
                    let r = leaf.record(i);
                    if r.from() > to {
                        break;
                    }
                    if r.to() >= from {
                        out.push((r.from(), r.to(), r.scope_id()));
                    }
                }
                Ok(())
            }
            spec::PAGE_TYPE_BRANCH => {
                let branch = BranchView::<K>::new(page, h.entry_count as usize);
                let start = Self::branch_find_child(&branch, from);
                for j in start..branch.child_count() {
                    if j > 0 && branch.sep(j - 1) > to {
                        break;
                    }
                    self.collect_overlapping_node(branch.child(j), from, to, out)?;
                }
                Ok(())
            }
            _ => Err(Error::Structural("unexpected page type")),
        }
    }
}

/// Estimated page count of a dense B+tree holding `record_count` records
/// (issue-8). Sums leaf pages plus each branch level until a single root
/// remains. Used by `compact_if_needed` to avoid walking the tree every commit.
fn expected_tree_pages(key_width: u8, record_count: u64) -> u64 {
    if record_count == 0 {
        return 0;
    }
    let leaf_max = spec::leaf_max(key_width) as u64;
    let branch_max = spec::branch_max(key_width) as u64;
    let mut total = 0u64;
    let mut level = record_count.div_ceil(leaf_max.max(1));
    loop {
        total += level;
        if level <= 1 {
            break;
        }
        level = level.div_ceil(branch_max.max(1));
    }
    total
}
