//! One-pass retention merge from normalized private coverage.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::{AddressFamily, ValueKind};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::range_bulk::{Builder, Record};
use crate::range_cursor::DirectRange;
use crate::range_store_cursor::Cursor;
use crate::workflow::Comparison;

use super::DraftStore;

pub(crate) struct RetentionMerge {
    pub(crate) input_intervals: u64,
    pub(crate) input_addresses: Cardinality129,
    pub(crate) comparison: Comparison,
}

impl DraftStore<'_> {
    pub(crate) fn merge_retention(
        &mut self,
        base: &Bootstrap,
        refresh_value: u32,
        cancellation: &CancellationToken,
    ) -> Result<RetentionMerge> {
        match base.meta.address_family {
            AddressFamily::Ipv4 => {
                self.merge_retention_family::<Ipv4Key>(base, refresh_value, cancellation)
            }
            AddressFamily::Ipv6 => {
                self.merge_retention_family::<Ipv6Key>(base, refresh_value, cancellation)
            }
        }
    }

    fn merge_retention_family<K: IpKey>(
        &mut self,
        base: &Bootstrap,
        refresh_value: u32,
        cancellation: &CancellationToken,
    ) -> Result<RetentionMerge> {
        cancellation.check()?;
        let input_meta = self.draft.meta;
        let old = Cursor::<K>::new(self, &base.meta, false)?;
        let coverage = Cursor::<K>::new(self, &input_meta, true)?;
        let mut merge = Merge::new(old, coverage, input_meta.txn_id, refresh_value);
        let (root, count, result) = merge.run(self, cancellation)?;
        self.draft.meta.range_root = root;
        self.draft.meta.range_record_count = count;
        Ok(result)
    }
}

struct Merge<K> {
    old_cursor: Cursor<K>,
    coverage_cursor: Cursor<K>,
    old: Option<DirectRange<K>>,
    coverage: Option<DirectRange<K>>,
    output: Output<K>,
    refresh_value: u32,
    input_intervals: u64,
    input_addresses: Cardinality129,
    comparison: Comparison,
}

impl<K: IpKey> Merge<K> {
    fn new(
        old_cursor: Cursor<K>,
        coverage_cursor: Cursor<K>,
        transaction: u64,
        refresh_value: u32,
    ) -> Self {
        Self {
            old_cursor,
            coverage_cursor,
            old: None,
            coverage: None,
            output: Output::new(transaction),
            refresh_value,
            input_intervals: 0,
            input_addresses: Cardinality129::ZERO,
            comparison: Comparison::default(),
        }
    }

    fn run(
        &mut self,
        store: &mut DraftStore<'_>,
        cancellation: &CancellationToken,
    ) -> Result<(u32, u64, RetentionMerge)> {
        self.advance_old(store, cancellation)?;
        self.advance_coverage(store, cancellation)?;
        while self.old.is_some() || self.coverage.is_some() {
            cancellation.check()?;
            self.step(store, cancellation)?;
        }
        verify(&self.comparison, self.input_addresses)?;
        let (root, count) = self.output.finish(store)?;
        Ok((
            root,
            count,
            RetentionMerge {
                input_intervals: self.input_intervals,
                input_addresses: self.input_addresses,
                comparison: self.comparison,
            },
        ))
    }

    fn step(&mut self, store: &mut DraftStore<'_>, cancellation: &CancellationToken) -> Result<()> {
        let Some(old) = self.old else {
            let Some(coverage) = self.coverage else {
                return Ok(());
            };
            self.add_new(store, coverage)?;
            return self.advance_coverage(store, cancellation);
        };
        let Some(coverage) = self.coverage else {
            add_removed(&mut self.comparison, length(old)?)?;
            return self.advance_old(store, cancellation);
        };
        if old.to < coverage.from {
            add_removed(&mut self.comparison, length(old)?)?;
            return self.advance_old(store, cancellation);
        }
        if coverage.to < old.from {
            self.add_new(store, coverage)?;
            return self.advance_coverage(store, cancellation);
        }
        self.overlap(store, old, coverage, cancellation)
    }

    fn overlap(
        &mut self,
        store: &mut DraftStore<'_>,
        old: DirectRange<K>,
        coverage: DirectRange<K>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        if old.from < coverage.from {
            return self.remove_old_prefix(old, coverage.from);
        }
        if coverage.from < old.from {
            return self.add_new_prefix(store, coverage, old.from);
        }

        self.emit_overlap(store, old, coverage, cancellation)
    }

    fn remove_old_prefix(&mut self, mut old: DirectRange<K>, until: K) -> Result<()> {
        let end = previous(until, "retention old prefix")?;
        add_removed(&mut self.comparison, old.from.inclusive_cardinality(end)?)?;
        old.from = until;
        self.old = Some(old);
        Ok(())
    }

    fn add_new_prefix(
        &mut self,
        store: &mut DraftStore<'_>,
        mut coverage: DirectRange<K>,
        until: K,
    ) -> Result<()> {
        let end = previous(until, "retention new prefix")?;
        self.add_new(
            store,
            DirectRange {
                from: coverage.from,
                to: end,
                value: self.refresh_value,
            },
        )?;
        coverage.from = until;
        self.coverage = Some(coverage);
        Ok(())
    }

    fn emit_overlap(
        &mut self,
        store: &mut DraftStore<'_>,
        mut old: DirectRange<K>,
        mut coverage: DirectRange<K>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let end = old.to.min(coverage.to);
        let overlap = old.from.inclusive_cardinality(end)?;
        add_unchanged(&mut self.comparison, overlap)?;
        self.output.emit(
            store,
            Record {
                from: old.from,
                to: end,
                value: old.value,
            },
        )?;
        if old.to == end {
            self.advance_old(store, cancellation)?;
        } else {
            old.from = next(end, "retention old remainder")?;
            self.old = Some(old);
        }
        if coverage.to == end {
            self.advance_coverage(store, cancellation)
        } else {
            coverage.from = next(end, "retention coverage remainder")?;
            self.coverage = Some(coverage);
            Ok(())
        }
    }

    fn add_new(&mut self, store: &mut DraftStore<'_>, range: DirectRange<K>) -> Result<()> {
        let count = length(range)?;
        add_added(&mut self.comparison, count)?;
        self.output.emit(
            store,
            Record {
                from: range.from,
                to: range.to,
                value: self.refresh_value,
            },
        )
    }

    fn advance_old(
        &mut self,
        store: &mut DraftStore<'_>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        cancellation.check()?;
        self.old = self.old_cursor.next(store)?;
        Ok(())
    }

    fn advance_coverage(
        &mut self,
        store: &mut DraftStore<'_>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        cancellation.check()?;
        self.coverage = self.coverage_cursor.next(store)?;
        if let Some(range) = self.coverage {
            let Some(input_intervals) = self.input_intervals.checked_add(1) else {
                return Err(Error::ArithmeticOverflow("retention input intervals"));
            };
            self.input_intervals = input_intervals;
            self.input_addresses = add(self.input_addresses, length(range)?)?;
        }
        Ok(())
    }
}

struct Output<K> {
    builder: Builder<K>,
    pending: Option<Record<K>>,
}

impl<K: IpKey> Output<K> {
    fn new(transaction: u64) -> Self {
        Self {
            builder: Builder::new(transaction, ValueKind::Direct),
            pending: None,
        }
    }

    fn emit(&mut self, store: &mut DraftStore<'_>, record: Record<K>) -> Result<()> {
        if let Some(pending) = self.pending.as_mut() {
            if pending.value == record.value && pending.to.checked_next() == Some(record.from) {
                pending.to = record.to;
                crate::work::range_coalesced(1);
                return Ok(());
            }
        }
        if let Some(pending) = self.pending.replace(record) {
            self.builder.push(store, pending)?;
        }
        Ok(())
    }

    fn finish(&mut self, store: &mut DraftStore<'_>) -> Result<(u32, u64)> {
        if let Some(record) = self.pending.take() {
            self.builder.push(store, record)?;
        }
        self.builder.finish(store)
    }
}

fn length<K: IpKey>(range: DirectRange<K>) -> Result<Cardinality129> {
    range.from.inclusive_cardinality(range.to)
}

fn previous<K: IpKey>(key: K, context: &'static str) -> Result<K> {
    let Some(previous) = key.checked_previous() else {
        return Err(Error::ArithmeticOverflow(context));
    };
    Ok(previous)
}

fn next<K: IpKey>(key: K, context: &'static str) -> Result<K> {
    let Some(next) = key.checked_next() else {
        return Err(Error::ArithmeticOverflow(context));
    };
    Ok(next)
}

fn add(left: Cardinality129, right: Cardinality129) -> Result<Cardinality129> {
    left.checked_add(right)
        .map_err(|_| Error::ArithmeticOverflow("retention address count"))
}

fn add_removed(result: &mut Comparison, count: Cardinality129) -> Result<()> {
    result.before = add(result.before, count)?;
    result.removed = add(result.removed, count)?;
    Ok(())
}

fn add_added(result: &mut Comparison, count: Cardinality129) -> Result<()> {
    result.after = add(result.after, count)?;
    result.added = add(result.added, count)?;
    Ok(())
}

fn add_unchanged(result: &mut Comparison, count: Cardinality129) -> Result<()> {
    result.before = add(result.before, count)?;
    result.after = add(result.after, count)?;
    result.unchanged = add(result.unchanged, count)?;
    Ok(())
}

fn verify(result: &Comparison, input_addresses: Cardinality129) -> Result<()> {
    let before = add(add(result.unchanged, result.changed)?, result.removed)?;
    let after = add(add(result.unchanged, result.changed)?, result.added)?;
    if before != result.before || after != result.after || after != input_addresses {
        return Err(Error::Corrupt("retention merge counters do not balance"));
    }
    Ok(())
}
