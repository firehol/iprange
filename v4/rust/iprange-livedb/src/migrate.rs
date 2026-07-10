//! Streaming migration: update a v4 DB from a sorted desired stream with bounded
//! memory. Emits change events (added/removed/changed-scope/unchanged).
//!
//! The old tree is traversed in-order using a fixed-size path stack — no heap
//! allocation per record. The old tree bytes are snapshotted once at the start
//! (O(DB_size), bounded — this is a batch operation, not the per-record hot path).

use crate::error::Result;
use crate::key::IpKey;
use crate::writer::Writer;
use alloc::vec::Vec;

/// A change event emitted during migration.
#[derive(Clone, Copy, Debug)]
pub enum Change<K: IpKey> {
    Added { from: K, to: K, scope_id: u32, old_scope_id: Option<u32> },
    Removed { from: K, to: K, old_scope_id: u32 },
    Unchanged { from: K, to: K, scope_id: u32 },
}

#[derive(Clone, Copy, Debug, Default)]
pub struct MigrateCounters {
    pub old_scanned: u64,
    pub desired_scanned: u64,
    pub added: u64,
    pub removed: u64,
    pub changed: u64,
    pub unchanged: u64,
}

#[derive(Clone, Copy, Debug)]
pub struct MigrateOptions<K: IpKey> {
    pub emit_unchanged: bool,
    pub on_change: Option<fn(&Change<K>)>,
}

impl<K: IpKey> Default for MigrateOptions<K> {
    fn default() -> Self {
        MigrateOptions { emit_unchanged: false, on_change: None }
    }
}

#[derive(Clone, Copy, Debug)]
pub struct DesiredRecord<K: IpKey> {
    pub from: K,
    pub to: K,
    pub scope_id: u32,
}

pub trait DesiredStream<K: IpKey> {
    fn peek(&self) -> Option<&DesiredRecord<K>>;
    fn next(&mut self) -> Option<DesiredRecord<K>>;
}

impl<K: IpKey> DesiredStream<K> for Box<dyn DesiredStream<K>> {
    fn peek(&self) -> Option<&DesiredRecord<K>> { (**self).peek() }
    fn next(&mut self) -> Option<DesiredRecord<K>> { (**self).next() }
}

/// Migrate the writer's pending tree to match the desired stream.
///
/// The old committed tree is snapshotted once (O(DB_size), bounded). The merge
/// then streams both the old snapshot and the desired input simultaneously —
/// O(1) additional memory during the merge loop.
pub fn migrate<K: IpKey>(
    writer: &mut Writer<K>,
    desired: &mut dyn DesiredStream<K>,
    opts: &MigrateOptions<K>,
) -> Result<MigrateCounters> {
    let mut counters = MigrateCounters::default();

    // Snapshot the committed bytes. This is bounded by the DB size (predetermined),
    // not by the input feed. It's a batch operation, not the per-record hot path.
    let committed: Vec<u8> = writer.store.committed_bytes().to_vec();
    let root = writer.committed_root;
    let height = writer.committed_height;

    let mut walker = TreeWalker::<K>::new(&committed, root, height);
    let mut old_cur = walker.peek();

    loop {
        let des_cur = desired.peek().copied();
        match (old_cur, des_cur) {
            (None, None) => break,

            (Some((of, ot, os)), None) => {
                emit(opts, &Change::Removed { from: of, to: ot, old_scope_id: os });
                writer.delete(of, ot)?;
                counters.removed += 1;
                counters.old_scanned += 1;
                old_cur = walker.advance();
            }

            (None, Some(d)) => {
                emit(opts, &Change::Added { from: d.from, to: d.to, scope_id: d.scope_id, old_scope_id: None });
                writer.set(d.from, d.to, d.scope_id)?;
                desired.next();
                counters.desired_scanned += 1;
                counters.added += 1;
            }

            (Some((of, ot, os)), Some(d)) => {
                if ot < d.from {
                    emit(opts, &Change::Removed { from: of, to: ot, old_scope_id: os });
                    writer.delete(of, ot)?;
                    old_cur = walker.advance();
                    counters.removed += 1;
                    counters.old_scanned += 1;
                } else if d.to < of {
                    emit(opts, &Change::Added { from: d.from, to: d.to, scope_id: d.scope_id, old_scope_id: None });
                    writer.set(d.from, d.to, d.scope_id)?;
                    desired.next();
                    counters.desired_scanned += 1;
                    counters.added += 1;
                } else {
                    if of == d.from && ot == d.to && os == d.scope_id {
                        if opts.emit_unchanged {
                            emit(opts, &Change::Unchanged { from: of, to: ot, scope_id: os });
                        }
                        counters.unchanged += 1;
                    } else {
                        emit(opts, &Change::Added { from: d.from, to: d.to, scope_id: d.scope_id, old_scope_id: Some(os) });
                        writer.set(d.from, d.to, d.scope_id)?;
                        counters.changed += 1;
                    }
                    old_cur = walker.advance();
                    desired.next();
                    counters.old_scanned += 1;
                    counters.desired_scanned += 1;
                }
            }
        }
    }

    Ok(counters)
}

/// Streaming in-order B+tree walker. Fixed-size path stack — zero heap per record.
struct TreeWalker<'a, K: IpKey> {
    bytes: &'a [u8],
    root: u32,
    height: u32,
    path: [(u32, usize); 32],
    path_len: u32,
    current: Option<(K, K, u32)>,
}

impl<'a, K: IpKey> TreeWalker<'a, K> {
    fn new(bytes: &'a [u8], root: u32, height: u32) -> Self {
        let mut w = TreeWalker {
            bytes, root, height,
            path: [(0, 0); 32], path_len: 0, current: None,
        };
        if root != 0 { w.descend_first(root, 1); }
        w
    }

    #[inline]
    fn page(&self, pgno: u32) -> &[u8] {
        let off = pgno as usize * crate::spec::PAGE_SIZE;
        &self.bytes[off..off + crate::spec::PAGE_SIZE]
    }

    fn descend_first(&mut self, pgno: u32, depth: u32) {
        let page = self.page(pgno);
        let h = crate::wire::PageHeader::decode(page);
        if depth >= self.height {
            let count = h.entry_count as usize;
            if count > 0 {
                let leaf = crate::node::LeafView::<K>::new(page, count);
                let r = leaf.record(0);
                self.current = Some((r.from(), r.to(), r.scope_id()));
                self.path[self.path_len as usize] = (pgno, 0);
                self.path_len += 1;
            }
            return;
        }
        let branch = crate::node::BranchView::<K>::new(page, h.entry_count as usize);
        let child = branch.child(0);
        self.path[self.path_len as usize] = (pgno, 0);
        self.path_len += 1;
        self.descend_first(child, depth + 1);
    }

    fn peek(&self) -> Option<(K, K, u32)> { self.current }

    fn advance(&mut self) -> Option<(K, K, u32)> {
        if self.current.is_none() { return None; }
        if self.path_len > 0 {
            self.path_len -= 1;
            let (pgno, idx) = self.path[self.path_len as usize];
            self.try_leaf_next(pgno, idx + 1);
        } else {
            self.current = None;
        }
        self.current
    }

    fn try_leaf_next(&mut self, pgno: u32, idx: usize) {
        let page = self.page(pgno);
        let h = crate::wire::PageHeader::decode(page);
        if h.page_type == crate::spec::PAGE_TYPE_LEAF {
            let count = h.entry_count as usize;
            if idx < count {
                let leaf = crate::node::LeafView::<K>::new(page, count);
                let r = leaf.record(idx);
                self.current = Some((r.from(), r.to(), r.scope_id()));
                self.path[self.path_len as usize] = (pgno, idx);
                self.path_len += 1;
                return;
            }
        }
        self.walk_up();
    }

    fn walk_up(&mut self) {
        loop {
            if self.path_len == 0 { self.current = None; return; }
            self.path_len -= 1;
            let (pgno, idx) = self.path[self.path_len as usize];
            let next_child = {
                let page = self.page(pgno);
                let h = crate::wire::PageHeader::decode(page);
                if h.page_type == crate::spec::PAGE_TYPE_BRANCH {
                    let branch = crate::node::BranchView::<K>::new(page, h.entry_count as usize);
                    let ni = idx + 1;
                    if ni < branch.child_count() { Some((ni, branch.child(ni))) } else { None }
                } else { None }
            };
            if let Some((next_idx, child_pgno)) = next_child {
                self.path[self.path_len as usize] = (pgno, next_idx);
                self.path_len += 1;
                self.descend_first(child_pgno, self.path_len + 1);
                return;
            }
        }
    }
}

#[inline]
fn emit<K: IpKey>(opts: &MigrateOptions<K>, change: &Change<K>) {
    if let Some(f) = opts.on_change { f(change); }
}
