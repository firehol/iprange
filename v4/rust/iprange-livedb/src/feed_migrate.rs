//! Streaming feed migration: update a single feed's membership in a multi-feed
//! file to match a desired stream.
//!
//! This is the update-ipsets operation: "set feed X to this new IP set."
//! Other feeds' memberships are preserved. Uses the interval algebra for
//! correct boundary splitting.

use crate::error::Result;
use crate::key::IpKey;
use crate::migrate::{DesiredStream, MigrateCounters, MigrateOptions};
use crate::writer::Writer;
use crate::node::{BranchView, LeafView};
use crate::spec;
use crate::wire::PageHeader;

/// Migrate a single feed's membership to match the desired stream.
///
/// For each IP range:
/// - In desired AND in old with feed_bit set → unchanged
/// - In desired AND in old WITHOUT feed_bit → add feed_bit (intern new bitmap)
/// - NOT in desired AND in old with feed_bit → clear feed_bit (intern new bitmap,
///   delete record if bitmap becomes empty)
/// - NOT in desired AND in old WITHOUT feed_bit → unchanged (other feeds only)
/// - In desired AND NOT in old → add feed_bit (new record)
///
/// `feed_bit` is the feed index (0-31 for mode 1, 0+ for mode 2).
pub fn migrate_feed<K: IpKey>(
    writer: &mut Writer<K>,
    feed_bit: u32,
    desired: &mut dyn DesiredStream<K>,
    _opts: &MigrateOptions<K>,
) -> Result<MigrateCounters> {
    let mut counters = MigrateCounters::default();

    // Enable migration mode to prevent COW-reuse hazard.
    let prev_can_recycle = writer.can_recycle;
    writer.can_recycle = false;

    // Walk the old tree one record at a time.
    let mut walker = FeedWalker::<K>::new(writer.committed_root, writer.committed_height);
    walker.init(writer.store.as_ref());

    let mut old_cur = walker.peek();
    let mut des_cur = desired.peek().copied();

    // Track trimmed starts for partial overlap handling.
    let mut old_trim: Option<K> = old_cur.map(|r| r.0);
    let mut des_trim: Option<K> = des_cur.map(|r| r.from);

    loop {
        let old_eff = if let (Some((_of, ot, os)), Some(ts)) = (old_cur, old_trim) {
            Some((ts, ot, os))
        } else { None };

        let des_eff = if let (Some(dr), Some(ts)) = (des_cur, des_trim) {
            Some((ts, dr.to, dr.scope_id))
        } else { None };

        match (old_eff, des_eff) {
            (None, None) => break,

            (Some((of, ot, os)), None) => {
                // Old only: clear feed_bit from this range.
                let new_scope = writer.clear_feed_bit(os, feed_bit)?;
                if new_scope != os {
                    // Scope changed → rewrite the record.
                    writer.delete(of, ot)?;
                    if new_scope != 0 {
                        writer.append(of, ot, new_scope)?;
                    }
                    counters.changed += 1;
                }
                counters.old_scanned += 1;
                walker.advance(writer.store.as_ref());
                old_cur = walker.peek();
                old_trim = old_cur.map(|r| r.0);
            }

            (None, Some((df, dt, _))) => {
                // Desired only: add feed_bit to this range.
                let new_scope = writer.fresh_feed_scope(feed_bit)?;
                writer.append(df, dt, new_scope)?;
                counters.added += 1;
                counters.desired_scanned += 1;
                desired.next();
                des_cur = desired.peek().copied();
                des_trim = des_cur.map(|r| r.from);
            }

            (Some((of, ot, os)), Some((df, dt, _))) => {
                if ot < df {
                    // Old before desired: clear feed_bit from old.
                    let new_scope = writer.clear_feed_bit(os, feed_bit)?;
                    if new_scope != os {
                        writer.delete(of, ot)?;
                        if new_scope != 0 {
                            writer.append(of, ot, new_scope)?;
                        }
                        counters.changed += 1;
                    }
                    counters.old_scanned += 1;
                    walker.advance(writer.store.as_ref());
                    old_cur = walker.peek();
                    old_trim = old_cur.map(|r| r.0);
                } else if dt < of {
                    // Desired before old: add feed_bit.
                    let new_scope = writer.fresh_feed_scope(feed_bit)?;
                    writer.append(df, dt, new_scope)?;
                    counters.added += 1;
                    counters.desired_scanned += 1;
                    desired.next();
                    des_cur = desired.peek().copied();
                    des_trim = des_cur.map(|r| r.from);
                } else {
                    // Overlap: split at boundaries.
                    // Prefix: old-only part [of, min(of,df)-1]
                    if of < df {
                        let prefix_end = df.checked_dec().unwrap_or(df);
                        let new_scope = writer.clear_feed_bit(os, feed_bit)?;
                        if new_scope != os {
                            writer.delete(of, prefix_end)?;
                            if new_scope != 0 {
                                writer.append(of, prefix_end, new_scope)?;
                            }
                            counters.changed += 1;
                        }
                    }

                    // Desired-only prefix
                    if df < of {
                        let prefix_end = of.checked_dec().unwrap_or(of);
                        let new_scope = writer.fresh_feed_scope(feed_bit)?;
                        writer.append(df, prefix_end, new_scope)?;
                        counters.added += 1;
                    }

                    // Overlap region
                    let overlap_start = if of < df { df } else { of };

                    if ot == dt {
                        // Same end: in overlap, add feed_bit
                        let new_scope = writer.apply_feed_bit(os, feed_bit)?;
                        if new_scope != os {
                            writer.delete(overlap_start, ot)?;
                            writer.append(overlap_start, ot, new_scope)?;
                            counters.changed += 1;
                        } else {
                            counters.unchanged += 1;
                        }
                        counters.old_scanned += 1;
                        counters.desired_scanned += 1;
                        walker.advance(writer.store.as_ref());
                        old_cur = walker.peek();
                        old_trim = old_cur.map(|r| r.0);
                        desired.next();
                        des_cur = desired.peek().copied();
                        des_trim = des_cur.map(|r| r.from);
                    } else if ot < dt {
                        let new_scope = writer.apply_feed_bit(os, feed_bit)?;
                        if new_scope != os {
                            writer.delete(overlap_start, ot)?;
                            writer.append(overlap_start, ot, new_scope)?;
                            counters.changed += 1;
                        } else {
                            counters.unchanged += 1;
                        }
                        counters.old_scanned += 1;
                        walker.advance(writer.store.as_ref());
                        old_cur = walker.peek();
                        old_trim = old_cur.map(|r| r.0);
                        des_trim = ot.checked_inc();
                        if des_trim.is_none() || des_trim.unwrap() > dt {
                            desired.next();
                            des_cur = desired.peek().copied();
                            des_trim = des_cur.map(|r| r.from);
                        }
                    } else {
                        let new_scope = writer.apply_feed_bit(os, feed_bit)?;
                        if new_scope != os {
                            writer.delete(overlap_start, dt)?;
                            writer.append(overlap_start, dt, new_scope)?;
                            counters.changed += 1;
                        } else {
                            counters.unchanged += 1;
                        }
                        counters.desired_scanned += 1;
                        desired.next();
                        des_cur = desired.peek().copied();
                        des_trim = des_cur.map(|r| r.from);
                        old_trim = dt.checked_inc();
                        if old_trim.is_none() || old_trim.unwrap() > ot {
                            walker.advance(writer.store.as_ref());
                            old_cur = walker.peek();
                            old_trim = old_cur.map(|r| r.0);
                        }
                    }
                }
            }
        }
    }

    writer.can_recycle = prev_can_recycle;
    Ok(counters)
}

/// Re-use the TreeWalker from migrate.rs (same fixed-size path stack).
struct FeedWalker<K: IpKey> {
    root: u32,
    #[allow(dead_code)]
    height: u32,
    path: [(u32, usize); 32],
    path_len: u32,
    current: Option<(K, K, u32)>,
}

impl<K: IpKey> FeedWalker<K> {
    fn new(root: u32, height: u32) -> Self {
        FeedWalker {
            root, height,
            path: [(0, 0); 32], path_len: 0, current: None,
        }
    }

    fn init(&mut self, store: &dyn crate::page_store::PageStore) {
        if self.root != 0 { self.descend_first(store, self.root, 1); }
    }

    fn peek(&self) -> Option<(K, K, u32)> { self.current }

    fn advance(&mut self, store: &dyn crate::page_store::PageStore) {
        if self.current.is_none() { return; }
        if self.path_len == 0 { self.current = None; return; }
        self.path_len -= 1;
        let (pgno, idx) = self.path[self.path_len as usize];
        self.try_leaf_next(store, pgno, idx + 1);
    }

    fn descend_first(&mut self, store: &dyn crate::page_store::PageStore, pgno: u32, _depth: u32) {
        let page = store.page(pgno);
        let h = PageHeader::decode(page);
        if h.page_type == spec::PAGE_TYPE_LEAF {
            let count = h.entry_count as usize;
            if count > 0 {
                let leaf = LeafView::<K>::new(page, count);
                let r = leaf.record(0);
                self.current = Some((r.from(), r.to(), r.scope_id()));
                self.path[self.path_len as usize] = (pgno, 0);
                self.path_len += 1;
            }
            return;
        }
        let branch = BranchView::<K>::new(page, h.entry_count as usize);
        let child = branch.child(0);
        self.path[self.path_len as usize] = (pgno, 0);
        self.path_len += 1;
        self.descend_first(store, child, _depth + 1);
    }

    fn try_leaf_next(&mut self, store: &dyn crate::page_store::PageStore, pgno: u32, idx: usize) {
        let page = store.page(pgno);
        let h = PageHeader::decode(page);
        if h.page_type == spec::PAGE_TYPE_LEAF {
            let count = h.entry_count as usize;
            if idx < count {
                let leaf = LeafView::<K>::new(page, count);
                let r = leaf.record(idx);
                self.current = Some((r.from(), r.to(), r.scope_id()));
                self.path[self.path_len as usize] = (pgno, idx);
                self.path_len += 1;
                return;
            }
        }
        self.walk_up(store);
    }

    fn walk_up(&mut self, store: &dyn crate::page_store::PageStore) {
        loop {
            if self.path_len == 0 { self.current = None; return; }
            self.path_len -= 1;
            let (pgno, idx) = self.path[self.path_len as usize];
            let next_child = {
                let page = store.page(pgno);
                let h = PageHeader::decode(page);
                if h.page_type == spec::PAGE_TYPE_BRANCH {
                    let branch = BranchView::<K>::new(page, h.entry_count as usize);
                    let ni = idx + 1;
                    if ni < branch.child_count() { Some((ni, branch.child(ni))) } else { None }
                } else { None }
            };
            if let Some((next_idx, child_pgno)) = next_child {
                self.path[self.path_len as usize] = (pgno, next_idx);
                self.path_len += 1;
                self.descend_first(store, child_pgno, self.path_len);
                return;
            }
        }
    }
}
