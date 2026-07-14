//! Streaming migration: update a v4 DB from a sorted desired stream.
//!
//! Uses the interval algebra (`interval::interval_diff`) for correct partial-
//! overlap handling. The old tree is traversed one record at a time via
//! `TreeWalker` (fixed-size path stack, no heap per record, no full-DB copy).
//! The desired stream provides records one at a time. The merge applies
//! set/delete only for changed segments.
//!
//! **Fixes blockers #2 and #3.**

use crate::error::Result;
use crate::key::IpKey;
use crate::node::{BranchView, LeafView};
use crate::spec;
use crate::wire::PageHeader;
use crate::writer::Writer;

/// A change event emitted during migration.
#[derive(Clone, Copy, Debug)]
pub enum Change<K: IpKey> {
    Added {
        from: K,
        to: K,
        scope_id: u32,
        old_scope_id: Option<u32>,
    },
    Removed {
        from: K,
        to: K,
        old_scope_id: u32,
    },
    Unchanged {
        from: K,
        to: K,
        scope_id: u32,
    },
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
    /// Optional scope combiner for overlapping records with differing scopes.
    /// Called with (old_scope_id, desired_scope_id) → the scope_id to keep.
    /// If `None`, the desired scope_id wins (overwrite — current/legacy behavior).
    /// For Mode 0 retention, set this to `Some(|old, new| old.min(new))` to
    /// preserve the older timestamp instead of overwriting it.
    pub combine: Option<fn(u32, u32) -> u32>,
}

impl<K: IpKey> Default for MigrateOptions<K> {
    fn default() -> Self {
        MigrateOptions {
            emit_unchanged: false,
            on_change: None,
            combine: None,
        }
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
    /// A deferred read error (e.g. a truncated spill file detected mid-stream).
    /// `None` on a clean EOF; `Some(msg)` means the stream ended early because a
    /// record could not be read. Callers that stop when `next()` returns `None`
    /// MUST check `err()` afterwards — otherwise a truncated spill silently looks
    /// like a clean EOF and partial data gets committed.
    fn err(&self) -> Option<&str> {
        None
    }
}

impl<K: IpKey> DesiredStream<K> for Box<dyn DesiredStream<K>> {
    fn peek(&self) -> Option<&DesiredRecord<K>> {
        (**self).peek()
    }
    fn next(&mut self) -> Option<DesiredRecord<K>> {
        (**self).next()
    }
    fn err(&self) -> Option<&str> {
        (**self).err()
    }
}

// ──────────────────────────────────────────────────────────────────────────
// TreeWalker: streaming in-order B+tree scan
// ──────────────────────────────────────────────────────────────────────────

/// A streaming in-order B+tree walker. Stores only page numbers and indices
/// (no raw pointers, no full-DB copy). Reads pages on demand via `advance()`.
///
/// **Fixes blocker #2:** no `committed_bytes().to_vec()` — pages are read
/// one at a time through the store's `page()` method.
struct TreeWalker<K: IpKey> {
    root: u32,
    #[allow(dead_code)]
    height: u32,
    path: [(u32, usize); 32],
    path_len: u32,
    current: Option<(K, K, u32)>,
    _k: core::marker::PhantomData<K>,
}

impl<K: IpKey> TreeWalker<K> {
    fn new(root: u32, height: u32) -> Self {
        TreeWalker {
            root,
            height,
            path: [(0, 0); 32],
            path_len: 0,
            current: None,
            _k: core::marker::PhantomData,
        }
    }

    /// Initialize: descend to the leftmost leaf and load the first record.
    /// Must be called before peek/advance.
    fn init(&mut self, store: &dyn crate::page_store::PageStore) {
        if self.root != 0 {
            self.descend_first(store, self.root, 1);
        }
    }

    fn peek(&self) -> Option<(K, K, u32)> {
        self.current
    }

    /// Advance to the next record. Reads pages from `store` on demand.
    fn advance(&mut self, store: &dyn crate::page_store::PageStore) {
        if self.current.is_none() {
            return;
        }
        if self.path_len == 0 {
            self.current = None;
            return;
        }

        // Pop the leaf frame and try the next record in the same leaf.
        self.path_len -= 1;
        let (pgno, idx) = self.path[self.path_len as usize];
        self.try_leaf_next(store, pgno, idx + 1);
    }

    fn descend_first(&mut self, store: &dyn crate::page_store::PageStore, pgno: u32, _depth: u32) {
        let page = store.page(pgno);
        let h = PageHeader::decode(page);
        if h.page_type == spec::PAGE_TYPE_LEAF {
            // Leaf level.
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
        // Branch: descend to leftmost child.
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
        // Leaf exhausted → walk up to find the next leaf.
        self.walk_up(store);
    }

    fn walk_up(&mut self, store: &dyn crate::page_store::PageStore) {
        loop {
            if self.path_len == 0 {
                self.current = None;
                return;
            }
            self.path_len -= 1;
            let (pgno, idx) = self.path[self.path_len as usize];
            // Read the branch page to check for a next child.
            let next_child = {
                let page = store.page(pgno);
                let h = PageHeader::decode(page);
                if h.page_type == spec::PAGE_TYPE_BRANCH {
                    let branch = BranchView::<K>::new(page, h.entry_count as usize);
                    let ni = idx + 1;
                    if ni < branch.child_count() {
                        Some((ni, branch.child(ni)))
                    } else {
                        None
                    }
                } else {
                    None
                }
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

// ──────────────────────────────────────────────────────────────────────────
// The merge: correct sweep-line with boundary splitting
// ──────────────────────────────────────────────────────────────────────────

/// Migrate the writer's committed tree to match the desired stream.
///
/// Uses a proper sweep-line merge that splits at every interval boundary,
/// handling ALL overlap cases: identical, partial, one-to-many, many-to-one,
/// complete separation.
///
/// **Memory:** O(1) — the old tree is traversed one record at a time via
/// TreeWalker (no full-DB copy). The desired stream provides records one at
/// a time. The merge applies set/delete only for changed segments.
pub fn migrate<K: IpKey>(
    writer: &mut Writer<K>,
    desired: &mut dyn DesiredStream<K>,
    opts: &MigrateOptions<K>,
) -> Result<MigrateCounters> {
    // A migration that fails after applying some changes leaves the writer in a
    // partially-applied state. Poison it so the caller cannot commit a half-
    // migrated transaction (the caller MUST treat a migrate error as fatal and
    // discard the writer).
    let result = migrate_inner(writer, desired, opts);
    if result.is_err() {
        writer.poison();
    }
    result
}

/// Advance `desired` to the next record, validating that the stream stays
/// sorted ascending and pairwise disjoint with `from <= to`. Returns the next
/// record (or `None` at a clean EOF). `prev_to` is the `to` of the most
/// recently consumed raw desired record.
fn advance_desired<K: IpKey>(
    desired: &mut dyn DesiredStream<K>,
    prev_to: &mut Option<K>,
) -> Result<Option<DesiredRecord<K>>> {
    desired.next();
    let next = desired.peek().copied();
    if let Some(r) = &next {
        if r.from > r.to {
            return Err(crate::error::Error::InvalidInput(
                "desired record has from > to",
            ));
        }
        if let Some(pt) = *prev_to {
            if r.from <= pt {
                return Err(crate::error::Error::InvalidInput(
                    "desired stream must be sorted and disjoint",
                ));
            }
        }
        *prev_to = Some(r.to);
    }
    Ok(next)
}

fn migrate_inner<K: IpKey>(
    writer: &mut Writer<K>,
    desired: &mut dyn DesiredStream<K>,
    opts: &MigrateOptions<K>,
) -> Result<MigrateCounters> {
    let mut counters = MigrateCounters::default();

    // Enable migration mode: prevents alloc_or_reuse from reusing pages
    // freed during this migration that the TreeWalker might still read.
    let prev_can_recycle = writer.can_recycle;
    writer.can_recycle = false;

    // Initialize the TreeWalker over the COMMITTED tree.
    let mut walker = TreeWalker::<K>::new(writer.committed_root, writer.committed_height);
    walker.init(writer.store.as_ref());

    // The merge uses a "trim" approach: when old and desired partially overlap,
    // we track trimmed starts for the current record on each side.
    let mut old_cur = walker.peek();
    let mut des_cur = desired.peek().copied();
    // Validate the desired stream as it is consumed: each record must satisfy
    // from <= to, and successive records must be sorted ascending and pairwise
    // disjoint. Unsorted/overlapping/reversed input is rejected.
    let mut prev_des_to: Option<K> = None;
    if let Some(r) = &des_cur {
        if r.from > r.to {
            writer.can_recycle = prev_can_recycle;
            return Err(crate::error::Error::InvalidInput(
                "desired record has from > to",
            ));
        }
        prev_des_to = Some(r.to);
    }

    // Trimmed starts (for partial overlap handling).
    let mut old_trim_start: Option<K> = old_cur.map(|r| r.0);
    let mut des_trim_start: Option<K> = des_cur.map(|r| r.from);

    loop {
        // Get the effective current records (with trimmed starts).
        let old_eff = if let (Some((_of, ot, os)), Some(ts)) = (old_cur, old_trim_start) {
            Some((ts, ot, os))
        } else {
            None
        };

        let des_eff = if let (Some(dr), Some(ts)) = (des_cur, des_trim_start) {
            Some((ts, dr.to, dr.scope_id))
        } else {
            None
        };

        match (old_eff, des_eff) {
            (None, None) => break,

            (Some((of, ot, os)), None) => {
                // Only old remains → remove.
                emit(
                    opts,
                    &Change::Removed {
                        from: of,
                        to: ot,
                        old_scope_id: os,
                    },
                );
                writer.delete(of, ot)?;
                counters.removed += 1;
                counters.old_scanned += 1;
                // Advance old.
                walker.advance(writer.store.as_ref());
                old_cur = walker.peek();
                old_trim_start = old_cur.map(|r| r.0);
            }

            (None, Some((df, dt, ds))) => {
                // Only desired remains → add.
                emit(
                    opts,
                    &Change::Added {
                        from: df,
                        to: dt,
                        scope_id: ds,
                        old_scope_id: None,
                    },
                );
                writer.set(df, dt, ds)?;
                counters.added += 1;
                counters.desired_scanned += 1;
                // Advance desired.
                des_cur = advance_desired::<K>(desired, &mut prev_des_to)?;
                des_trim_start = des_cur.map(|r| r.from);
            }

            (Some((of, ot, os)), Some((df, dt, ds))) => {
                if ot < df {
                    // Old entirely before desired → remove old.
                    emit(
                        opts,
                        &Change::Removed {
                            from: of,
                            to: ot,
                            old_scope_id: os,
                        },
                    );
                    writer.delete(of, ot)?;
                    counters.removed += 1;
                    counters.old_scanned += 1;
                    walker.advance(writer.store.as_ref());
                    old_cur = walker.peek();
                    old_trim_start = old_cur.map(|r| r.0);
                } else if dt < of {
                    // Desired entirely before old → add desired.
                    emit(
                        opts,
                        &Change::Added {
                            from: df,
                            to: dt,
                            scope_id: ds,
                            old_scope_id: None,
                        },
                    );
                    writer.set(df, dt, ds)?;
                    counters.added += 1;
                    counters.desired_scanned += 1;
                    des_cur = advance_desired::<K>(desired, &mut prev_des_to)?;
                    des_trim_start = des_cur.map(|r| r.from);
                } else {
                    // Overlap! Split at boundaries.
                    // Step 1: Emit any old-only prefix [of, df-1]
                    if of < df {
                        let prefix_end = df.checked_dec().unwrap_or(df);
                        emit(
                            opts,
                            &Change::Removed {
                                from: of,
                                to: prefix_end,
                                old_scope_id: os,
                            },
                        );
                        writer.delete(of, prefix_end)?;
                        counters.removed += 1;
                    }

                    // Step 2: Emit any desired-only prefix [df, of-1]
                    if df < of {
                        let prefix_end = of.checked_dec().unwrap_or(of);
                        emit(
                            opts,
                            &Change::Added {
                                from: df,
                                to: prefix_end,
                                scope_id: ds,
                                old_scope_id: None,
                            },
                        );
                        writer.set(df, prefix_end, ds)?;
                        counters.added += 1;
                    }

                    // Step 3: Now both start at overlap_start.
                    let overlap_start = if of < df { df } else { of };

                    if ot == dt {
                        // Same end → compare scopes, advance both.
                        if os == ds {
                            if opts.emit_unchanged {
                                emit(
                                    opts,
                                    &Change::Unchanged {
                                        from: overlap_start,
                                        to: ot,
                                        scope_id: os,
                                    },
                                );
                            }
                            counters.unchanged += 1;
                        } else {
                            let keep_scope = match opts.combine {
                                Some(f) => f(os, ds),
                                None => ds,
                            };
                            emit(
                                opts,
                                &Change::Added {
                                    from: overlap_start,
                                    to: ot,
                                    scope_id: keep_scope,
                                    old_scope_id: Some(os),
                                },
                            );
                            if keep_scope != os {
                                writer.set(overlap_start, ot, keep_scope)?;
                            }
                            counters.changed += 1;
                        }
                        counters.old_scanned += 1;
                        counters.desired_scanned += 1;
                        walker.advance(writer.store.as_ref());
                        old_cur = walker.peek();
                        old_trim_start = old_cur.map(|r| r.0);
                        des_cur = advance_desired::<K>(desired, &mut prev_des_to)?;
                        des_trim_start = des_cur.map(|r| r.from);
                    } else if ot < dt {
                        // Old ends first → overlap [overlap_start, ot], then desired continues.
                        if os == ds {
                            if opts.emit_unchanged {
                                emit(
                                    opts,
                                    &Change::Unchanged {
                                        from: overlap_start,
                                        to: ot,
                                        scope_id: os,
                                    },
                                );
                            }
                            counters.unchanged += 1;
                        } else {
                            let keep_scope = match opts.combine {
                                Some(f) => f(os, ds),
                                None => ds,
                            };
                            emit(
                                opts,
                                &Change::Added {
                                    from: overlap_start,
                                    to: ot,
                                    scope_id: keep_scope,
                                    old_scope_id: Some(os),
                                },
                            );
                            if keep_scope != os {
                                writer.set(overlap_start, ot, keep_scope)?;
                            }
                            counters.changed += 1;
                        }
                        counters.old_scanned += 1;
                        // Advance old, trim desired's start.
                        walker.advance(writer.store.as_ref());
                        old_cur = walker.peek();
                        old_trim_start = old_cur.map(|r| r.0);
                        // Trim desired start to ot+1
                        des_trim_start = ot.checked_inc();
                        if des_trim_start.is_none() {
                            des_cur = advance_desired::<K>(desired, &mut prev_des_to)?;
                            des_trim_start = des_cur.map(|r| r.from);
                        } else if let Some(ts) = des_trim_start {
                            if ts > dt {
                                // Trimmed past the desired end → advance.
                                des_cur = advance_desired::<K>(desired, &mut prev_des_to)?;
                                des_trim_start = des_cur.map(|r| r.from);
                            }
                        }
                    } else {
                        // dt < ot: Desired ends first → overlap [overlap_start, dt], then old continues.
                        if os == ds {
                            if opts.emit_unchanged {
                                emit(
                                    opts,
                                    &Change::Unchanged {
                                        from: overlap_start,
                                        to: dt,
                                        scope_id: os,
                                    },
                                );
                            }
                            counters.unchanged += 1;
                        } else {
                            let keep_scope = match opts.combine {
                                Some(f) => f(os, ds),
                                None => ds,
                            };
                            emit(
                                opts,
                                &Change::Added {
                                    from: overlap_start,
                                    to: dt,
                                    scope_id: keep_scope,
                                    old_scope_id: Some(os),
                                },
                            );
                            if keep_scope != os {
                                writer.set(overlap_start, dt, keep_scope)?;
                            }
                            counters.changed += 1;
                        }
                        counters.desired_scanned += 1;
                        // Advance desired, trim old's start.
                        des_cur = advance_desired::<K>(desired, &mut prev_des_to)?;
                        des_trim_start = des_cur.map(|r| r.from);
                        // Trim old start to dt+1
                        old_trim_start = dt.checked_inc();
                        if old_trim_start.is_none() {
                            walker.advance(writer.store.as_ref());
                            old_cur = walker.peek();
                            old_trim_start = old_cur.map(|r| r.0);
                        } else if let Some(ts) = old_trim_start {
                            if ts > ot {
                                walker.advance(writer.store.as_ref());
                                old_cur = walker.peek();
                                old_trim_start = old_cur.map(|r| r.0);
                            }
                        }
                    }
                }
            }
        }
    }

    writer.can_recycle = prev_can_recycle;

    // A truncated spill file makes the desired stream end early: next() returns
    // None on the partial record, so the merge loop above treats it as a clean
    // EOF. Without this check we would silently commit the records read so far
    // and lose the IPs in the truncated tail. err() is None only on a true clean
    // EOF; a Some value means "done" was actually a deferred read failure.
    if desired.err().is_some() {
        return Err(crate::error::Error::Structural(
            "desired stream ended with a read error (truncated spill)",
        ));
    }

    Ok(counters)
}

#[inline]
fn emit<K: IpKey>(opts: &MigrateOptions<K>, change: &Change<K>) {
    if let Some(f) = opts.on_change {
        f(change);
    }
}

/// Retention migration: like [`migrate`] but preserves the older scope_id on a
/// scope mismatch instead of overwriting it.
///
/// For Mode 0 (retention/timestamp) databases, `scope_id` is a unix timestamp.
/// The correct merge semantics is "keep `min(old, new)`" — the older timestamp
/// wins, so a record is only rewritten when the desired stream carries an older
/// timestamp than what is already stored. Records where the old timestamp is
/// already older (or equal) are left untouched.
pub fn migrate_retention<K: IpKey>(
    writer: &mut Writer<K>,
    desired: &mut dyn DesiredStream<K>,
) -> Result<MigrateCounters> {
    let opts = MigrateOptions::<K> {
        combine: Some(|old: u32, new: u32| old.min(new)),
        ..MigrateOptions::<K>::default()
    };
    migrate(writer, desired, &opts)
}
