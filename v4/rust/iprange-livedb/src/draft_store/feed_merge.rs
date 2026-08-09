//! One-sweep exact named-feed replacement over normalized mapped ranges.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::{AddressFamily, MembershipOperation, ValueKind};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::membership_dictionary::Interned;
use crate::range_bulk::{Builder, Record};
use crate::range_cursor::DirectRange;
use crate::range_mutation;
use crate::range_store_cursor::Cursor;
use crate::workflow::{compare::ScannedComparison, Comparison};

use super::DraftStore;

pub(crate) struct FeedMerge {
    pub(crate) input_intervals: u64,
    pub(crate) input_addresses: Cardinality129,
    pub(crate) comparison: ScannedComparison,
}

impl DraftStore<'_> {
    pub(crate) fn add_feed_coverage<K: IpKey>(&mut self, from: K, to: K) -> Result<()> {
        let mut root = self.draft.workflow_range_root;
        let mut count = self.draft.workflow_range_count;
        range_mutation::assign_private_untracked(self, &mut root, &mut count, from, to, 1)?;
        self.draft.workflow_range_root = root;
        self.draft.workflow_range_count = count;
        Ok(())
    }

    pub(crate) fn merge_feed(
        &mut self,
        base: &Bootstrap,
        member: Interned,
        create: bool,
        cancellation: &CancellationToken,
    ) -> Result<FeedMerge> {
        if create && self.draft.workflow_range_root == 0 {
            return Ok(FeedMerge {
                input_intervals: 0,
                input_addresses: Cardinality129::ZERO,
                comparison: ScannedComparison {
                    comparison: Comparison::default(),
                    before_intervals: 0,
                    after_intervals: 0,
                },
            });
        }
        match base.meta.address_family {
            AddressFamily::Ipv4 => self.merge_feed_family::<Ipv4Key>(base, member, cancellation),
            AddressFamily::Ipv6 => self.merge_feed_family::<Ipv6Key>(base, member, cancellation),
        }
    }

    fn merge_feed_family<K: IpKey>(
        &mut self,
        base: &Bootstrap,
        member: Interned,
        cancellation: &CancellationToken,
    ) -> Result<FeedMerge> {
        cancellation.check()?;
        let mut coverage_meta = self.draft.meta;
        coverage_meta.range_root = self.draft.workflow_range_root;
        coverage_meta.range_record_count = self.draft.workflow_range_count;
        let old = Cursor::<K>::new(self, &base.meta, false)?;
        let coverage = Cursor::<K>::new(self, &coverage_meta, true)?;
        let mut merge = Merge::new(old, coverage, self.draft.meta.txn_id, member);
        let (root, count, result) = merge.run(self, cancellation)?;
        range_mutation::retire_tree::<K, _, _>(self, base.meta.range_root, || {
            cancellation.check()
        })?;
        self.draft.meta.range_root = root;
        self.draft.meta.range_record_count = count;
        self.draft.workflow_range_root = 0;
        self.draft.workflow_range_count = 0;
        Ok(result)
    }
}

struct Merge<K> {
    old_cursor: Cursor<K>,
    coverage_cursor: Cursor<K>,
    old: Option<DirectRange<K>>,
    coverage: Option<DirectRange<K>>,
    output: Output<K>,
    member: Interned,
    cached: Option<CachedMembership>,
    input_intervals: u64,
    input_addresses: Cardinality129,
    projection: Projection<K>,
}

#[derive(Clone, Copy)]
struct CachedMembership {
    old: u32,
    covered: bool,
    new: u32,
}

impl<K: IpKey> Merge<K> {
    fn new(old: Cursor<K>, coverage: Cursor<K>, transaction: u64, member: Interned) -> Self {
        Self {
            old_cursor: old,
            coverage_cursor: coverage,
            old: None,
            coverage: None,
            output: Output::new(transaction),
            member,
            cached: None,
            input_intervals: 0,
            input_addresses: Cardinality129::ZERO,
            projection: Projection::default(),
        }
    }

    fn run(
        &mut self,
        store: &mut DraftStore<'_>,
        cancellation: &CancellationToken,
    ) -> Result<(u32, u64, FeedMerge)> {
        self.advance_old(store, cancellation)?;
        self.advance_coverage(store, cancellation)?;
        while self.old.is_some() || self.coverage.is_some() {
            cancellation.check()?;
            self.step(store, cancellation)?;
        }
        let (root, count) = self.output.finish(store)?;
        let comparison = std::mem::take(&mut self.projection).finish(self.input_addresses)?;
        Ok((
            root,
            count,
            FeedMerge {
                input_intervals: self.input_intervals,
                input_addresses: self.input_addresses,
                comparison,
            },
        ))
    }

    fn step(&mut self, store: &mut DraftStore<'_>, cancellation: &CancellationToken) -> Result<()> {
        let Some(old) = self.old else {
            let coverage = self
                .coverage
                .ok_or(Error::Corrupt("feed merge lost both inputs"))?;
            self.emit(store, coverage.from, coverage.to, None, true)?;
            return self.advance_coverage(store, cancellation);
        };
        let Some(coverage) = self.coverage else {
            self.emit(store, old.from, old.to, Some(old.value), false)?;
            return self.advance_old(store, cancellation);
        };
        if old.to < coverage.from {
            self.emit(store, old.from, old.to, Some(old.value), false)?;
            return self.advance_old(store, cancellation);
        }
        if coverage.to < old.from {
            self.emit(store, coverage.from, coverage.to, None, true)?;
            return self.advance_coverage(store, cancellation);
        }
        self.overlap(store, old, coverage, cancellation)
    }

    fn overlap(
        &mut self,
        store: &mut DraftStore<'_>,
        mut old: DirectRange<K>,
        mut coverage: DirectRange<K>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        if old.from < coverage.from {
            let end = previous(coverage.from, "feed old prefix")?;
            self.emit(store, old.from, end, Some(old.value), false)?;
            old.from = coverage.from;
            self.old = Some(old);
            return Ok(());
        }
        if coverage.from < old.from {
            let end = previous(old.from, "feed coverage prefix")?;
            self.emit(store, coverage.from, end, None, true)?;
            coverage.from = old.from;
            self.coverage = Some(coverage);
            return Ok(());
        }

        let end = old.to.min(coverage.to);
        self.emit(store, old.from, end, Some(old.value), true)?;
        if old.to == end {
            self.advance_old(store, cancellation)?;
        } else {
            old.from = next(end, "feed old remainder")?;
            self.old = Some(old);
        }
        if coverage.to == end {
            self.advance_coverage(store, cancellation)
        } else {
            coverage.from = next(end, "feed coverage remainder")?;
            self.coverage = Some(coverage);
            Ok(())
        }
    }

    fn emit(
        &mut self,
        store: &mut DraftStore<'_>,
        from: K,
        to: K,
        old: Option<u32>,
        covered: bool,
    ) -> Result<()> {
        let (new, before) = self.transform(store, old, covered)?;
        self.projection.observe(from, to, before, covered)?;
        if new != 0 {
            self.output.emit(
                store,
                Record {
                    from,
                    to,
                    value: new,
                },
            )?;
        }
        Ok(())
    }

    fn transform(
        &mut self,
        store: &mut DraftStore<'_>,
        old: Option<u32>,
        covered: bool,
    ) -> Result<(u32, bool)> {
        let Some(old) = old else {
            return Ok((if covered { self.member.id } else { 0 }, false));
        };
        let new = if let Some(cached) = self
            .cached
            .filter(|cached| cached.old == old && cached.covered == covered)
        {
            cached.new
        } else {
            let operation = if covered {
                MembershipOperation::Union
            } else {
                MembershipOperation::Difference
            };
            let new = store
                .combine_memberships(old, self.member.id, self.member.word_count, operation)?
                .unwrap_or(0);
            self.cached = Some(CachedMembership { old, covered, new });
            new
        };
        let before = if covered { new == old } else { new != old };
        Ok((new, before))
    }

    fn advance_old(
        &mut self,
        store: &mut DraftStore<'_>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        cancellation.check()?;
        self.old = self.old_cursor.next(store)?;
        if let Some(range) = self.old {
            store.track_membership_refcount(range.value, -1)?;
        }
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
            self.input_intervals = self
                .input_intervals
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("feed input intervals"))?;
            self.input_addresses = add(self.input_addresses, length(range.from, range.to)?)?;
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
            builder: Builder::new(transaction, ValueKind::Membership),
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
            self.push(store, pending)?;
        }
        Ok(())
    }

    fn finish(&mut self, store: &mut DraftStore<'_>) -> Result<(u32, u64)> {
        if let Some(record) = self.pending.take() {
            self.push(store, record)?;
        }
        self.builder.finish(store)
    }

    fn push(&mut self, store: &mut DraftStore<'_>, record: Record<K>) -> Result<()> {
        store.track_membership_refcount(record.value, 1)?;
        self.builder.push(store, record)
    }
}

struct Projection<K> {
    result: Comparison,
    before_intervals: u64,
    after_intervals: u64,
    last_to: Option<K>,
    last_before: bool,
    last_after: bool,
}

impl<K> Default for Projection<K> {
    fn default() -> Self {
        Self {
            result: Comparison::default(),
            before_intervals: 0,
            after_intervals: 0,
            last_to: None,
            last_before: false,
            last_after: false,
        }
    }
}

impl<K: IpKey> Projection<K> {
    fn observe(&mut self, from: K, to: K, before: bool, after: bool) -> Result<()> {
        let adjacent = self.last_to.and_then(IpKey::checked_next) == Some(from);
        let count = length(from, to)?;
        if before {
            self.result.before = add(self.result.before, count)?;
            if !adjacent || !self.last_before {
                self.before_intervals = increment(self.before_intervals, "before feed intervals")?;
            }
        }
        if after {
            self.result.after = add(self.result.after, count)?;
            if !adjacent || !self.last_after {
                self.after_intervals = increment(self.after_intervals, "after feed intervals")?;
            }
        }
        match (before, after) {
            (true, true) => self.result.unchanged = add(self.result.unchanged, count)?,
            (true, false) => self.result.removed = add(self.result.removed, count)?,
            (false, true) => self.result.added = add(self.result.added, count)?,
            (false, false) => {}
        }
        self.last_to = Some(to);
        self.last_before = before;
        self.last_after = after;
        Ok(())
    }

    fn finish(self, input_addresses: Cardinality129) -> Result<ScannedComparison> {
        if self.result.after != input_addresses
            || self.after_intervals == 0 && input_addresses != Cardinality129::ZERO
        {
            return Err(Error::Corrupt(
                "feed merge input and output coverage disagree",
            ));
        }
        let before = add(self.result.unchanged, self.result.removed)?;
        let after = add(self.result.unchanged, self.result.added)?;
        if before != self.result.before
            || after != self.result.after
            || self.result.changed != Cardinality129::ZERO
        {
            return Err(Error::Corrupt("feed merge counters do not balance"));
        }
        Ok(ScannedComparison {
            comparison: self.result,
            before_intervals: self.before_intervals,
            after_intervals: self.after_intervals,
        })
    }
}

fn length<K: IpKey>(from: K, to: K) -> Result<Cardinality129> {
    from.inclusive_cardinality(to)
}

fn add(left: Cardinality129, right: Cardinality129) -> Result<Cardinality129> {
    left.checked_add(right)
        .map_err(|_| Error::ArithmeticOverflow("feed address count"))
}

fn increment(value: u64, context: &'static str) -> Result<u64> {
    value
        .checked_add(1)
        .ok_or(Error::ArithmeticOverflow(context))
}

fn previous<K: IpKey>(key: K, context: &'static str) -> Result<K> {
    key.checked_previous()
        .ok_or(Error::ArithmeticOverflow(context))
}

fn next<K: IpKey>(key: K, context: &'static str) -> Result<K> {
    key.checked_next().ok_or(Error::ArithmeticOverflow(context))
}
