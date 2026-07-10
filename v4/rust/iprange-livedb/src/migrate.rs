//! Streaming migration: update a v4 DB from a sorted desired stream with bounded
//! memory. Emits change events (added/removed/changed-scope/unchanged).
//!
//! This is the core primitive update-ipsets needs to keep huge multi-feed files
//! current without loading the whole dataset into memory.
//!
//! Algorithm: a parallel merge between two sorted cursors (old DB + desired stream),
//! advancing over interval boundaries. Calls set/delete only for changed spans.

use crate::error::Result;
use crate::key::IpKey;
use crate::writer::Writer;

/// A change event emitted during migration.
#[derive(Clone, Copy, Debug)]
pub enum Change<K: IpKey> {
    /// Range exists in desired but not in old (or with a different scope).
    Added { from: K, to: K, scope_id: u32, old_scope_id: Option<u32> },
    /// Range exists in old but not in desired.
    Removed { from: K, to: K, old_scope_id: u32 },
    /// Range exists in both with the same scope_id (only emitted if emit_unchanged).
    Unchanged { from: K, to: K, scope_id: u32 },
}

/// Migration counters.
#[derive(Clone, Copy, Debug, Default)]
pub struct MigrateCounters {
    pub old_scanned: u64,
    pub desired_scanned: u64,
    pub added: u64,
    pub removed: u64,
    pub changed: u64,
    pub unchanged: u64,
}

/// Options for migration.
#[derive(Clone, Copy, Debug)]
pub struct MigrateOptions<K: IpKey> {
    /// If true, emit Unchanged events.
    pub emit_unchanged: bool,
    /// Callback for each change event.
    pub on_change: Option<fn(&Change<K>)>,
}

impl<K: IpKey> Default for MigrateOptions<K> {
    fn default() -> Self {
        MigrateOptions {
            emit_unchanged: false,
            on_change: None,
        }
    }
}

/// A sorted record from the desired stream.
#[derive(Clone, Copy, Debug)]
pub struct DesiredRecord<K: IpKey> {
    pub from: K,
    pub to: K,
    pub scope_id: u32,
}

/// Trait for a sorted stream of desired records. The caller implements this
/// (e.g., from an external sort, or directly from a sorted source).
pub trait DesiredStream<K: IpKey> {
    fn peek(&self) -> Option<&DesiredRecord<K>>;
    fn next(&mut self) -> Option<DesiredRecord<K>>;
}

/// Blanket impl: a Box<dyn DesiredStream> is itself a DesiredStream.
impl<K: IpKey> DesiredStream<K> for Box<dyn DesiredStream<K>> {
    fn peek(&self) -> Option<&DesiredRecord<K>> { (**self).peek() }
    fn next(&mut self) -> Option<DesiredRecord<K>> { (**self).next() }
}

/// Migrate the writer's pending tree to match the desired stream.
///
/// The desired stream MUST be sorted (ascending `from`) and disjoint
/// (`from > prev.to`). The old tree is read via the writer's committed reader.
///
/// The merge advances over both streams in lock-step:
/// - If old.from < desired.from: the old range is before any desired range →
///   delete it (Removed).
/// - If desired.from < old.from: the desired range is before any old range →
///   insert it (Added).
/// - If they overlap: compare scope_ids → Changed or Unchanged.
pub fn migrate<K: IpKey>(
    writer: &mut Writer<K>,
    desired: &mut dyn DesiredStream<K>,
    opts: &MigrateOptions<K>,
) -> Result<MigrateCounters> {
    let mut counters = MigrateCounters::default();

    // Get a reader over committed state.
    let reader = writer.reader()?;

    // Collect old records by scanning the committed tree.
    // For a streaming implementation, we'd use a Cursor here. For simplicity,
    // this first version scans into a Vec — bounded by the old tree size.
    // TODO: replace with a cursor-based streaming approach for true bounded memory.
    let mut old_records: alloc::vec::Vec<(K, K, u32)> = alloc::vec::Vec::new();
    reader.scan::<K, _>(|from, to, scope_id| {
        old_records.push((from, to, scope_id));
    })?;
    counters.old_scanned = old_records.len() as u64;

    let mut old_idx = 0usize;

    // Merge loop
    while old_idx < old_records.len() || desired.peek().is_some() {
        let old_cur = if old_idx < old_records.len() {
            Some(old_records[old_idx])
        } else {
            None
        };
        let des_cur = desired.peek().copied();

        match (old_cur, des_cur) {
            (None, Some(d)) => {
                // Only desired remains → add
                emit_change(opts, &Change::Added {
                    from: d.from, to: d.to, scope_id: d.scope_id, old_scope_id: None,
                });
                writer.set(d.from, d.to, d.scope_id)?;
                desired.next();
                counters.desired_scanned += 1;
                counters.added += 1;
            }
            (Some((of, ot, os)), None) => {
                // Only old remains → remove
                emit_change(opts, &Change::Removed { from: of, to: ot, old_scope_id: os });
                writer.delete(of, ot)?;
                old_idx += 1;
                counters.removed += 1;
            }
            (Some((of, ot, os)), Some(d)) => {
                // Both exist — compare
                if ot < d.from {
                    // Old is entirely before desired → remove old
                    emit_change(opts, &Change::Removed { from: of, to: ot, old_scope_id: os });
                    writer.delete(of, ot)?;
                    old_idx += 1;
                    counters.removed += 1;
                } else if d.to < of {
                    // Desired is entirely before old → add desired
                    emit_change(opts, &Change::Added {
                        from: d.from, to: d.to, scope_id: d.scope_id, old_scope_id: None,
                    });
                    writer.set(d.from, d.to, d.scope_id)?;
                    desired.next();
                    counters.desired_scanned += 1;
                    counters.added += 1;
                } else {
                    // Overlap — compare scope_ids
                    if of == d.from && ot == d.to && os == d.scope_id {
                        // Exact match — unchanged
                        if opts.emit_unchanged {
                            emit_change(opts, &Change::Unchanged { from: of, to: ot, scope_id: os });
                        }
                        counters.unchanged += 1;
                    } else {
                        // Changed
                        emit_change(opts, &Change::Added {
                            from: d.from, to: d.to, scope_id: d.scope_id, old_scope_id: Some(os),
                        });
                        writer.set(d.from, d.to, d.scope_id)?;
                        counters.changed += 1;
                    }
                    // Advance both — the set() above may have adjusted the tree
                    old_idx += 1;
                    desired.next();
                    counters.desired_scanned += 1;
                }
            }
            (None, None) => break,
        }
    }

    Ok(counters)
}

#[inline]
fn emit_change<K: IpKey>(opts: &MigrateOptions<K>, change: &Change<K>) {
    if let Some(f) = opts.on_change {
        f(change);
    }
}
