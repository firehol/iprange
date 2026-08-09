//! Exact named-feed policy over the authoritative ordered range merge.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::{AddressFamily, MembershipOperation};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::range_mutation;
use crate::workflow::{compare::ScannedComparison, Comparison};

use super::range_merge::{self, Policy};
use super::{DraftStore, MembershipHandle};

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
        member: MembershipHandle,
        create: bool,
        cancellation: &CancellationToken,
    ) -> Result<FeedMerge> {
        if create && self.draft.workflow_range_root == 0 {
            return Ok(empty_result());
        }
        match base.meta.address_family {
            AddressFamily::Ipv4 => self.merge_feed_family::<Ipv4Key>(base, member, cancellation),
            AddressFamily::Ipv6 => self.merge_feed_family::<Ipv6Key>(base, member, cancellation),
        }
    }

    fn merge_feed_family<K: IpKey>(
        &mut self,
        base: &Bootstrap,
        member: MembershipHandle,
        cancellation: &CancellationToken,
    ) -> Result<FeedMerge> {
        let mut coverage_meta = self.draft.meta;
        coverage_meta.range_root = self.draft.workflow_range_root;
        coverage_meta.range_record_count = self.draft.workflow_range_count;
        let policy = FeedPolicy::new(member);
        let (input_intervals, finished) = range_merge::merge_coverage::<K, _>(
            self,
            &coverage_meta,
            base,
            policy,
            cancellation,
            "feed input intervals",
        )?;
        self.draft.workflow_range_root = 0;
        self.draft.workflow_range_count = 0;
        let input_addresses = finished.output.comparison.after;
        Ok(FeedMerge {
            input_intervals,
            input_addresses,
            comparison: finished.output,
        })
    }
}

fn empty_result() -> FeedMerge {
    FeedMerge {
        input_intervals: 0,
        input_addresses: Cardinality129::ZERO,
        comparison: ScannedComparison {
            comparison: Comparison::default(),
            before_intervals: 0,
            after_intervals: 0,
        },
    }
}

struct FeedPolicy<K> {
    member: MembershipHandle,
    cached: Option<CachedMembership>,
    projection: Projection<K>,
}

#[derive(Clone, Copy)]
struct CachedMembership {
    old: u32,
    covered: bool,
    new: Option<u32>,
}

impl<K> FeedPolicy<K> {
    fn new(member: MembershipHandle) -> Self {
        Self {
            member,
            cached: None,
            projection: Projection::default(),
        }
    }
}

impl<K: IpKey> Policy<K, ()> for FeedPolicy<K> {
    type Output = ScannedComparison;

    fn transform(
        &mut self,
        store: &mut DraftStore<'_>,
        old: Option<u32>,
        incoming: Option<()>,
    ) -> Result<Option<u32>> {
        let covered = incoming.is_some();
        let (member_id, member_words) = self.member.stored();
        let Some(old) = old else {
            return Ok(covered.then_some(member_id));
        };
        if let Some(cached) = self
            .cached
            .filter(|cached| cached.old == old && cached.covered == covered)
        {
            return Ok(cached.new);
        }
        let operation = if covered {
            MembershipOperation::Union
        } else {
            MembershipOperation::Difference
        };
        let new = store.combine_memberships(old, member_id, member_words, operation)?;
        self.cached = Some(CachedMembership { old, covered, new });
        Ok(new)
    }

    fn observe(
        &mut self,
        from: K,
        to: K,
        old: Option<u32>,
        incoming: Option<()>,
        new: Option<u32>,
    ) -> Result<()> {
        let after = incoming.is_some();
        let before = if after { new == old } else { new != old };
        self.projection.observe(from, to, before, after)
    }

    fn finish(self) -> Result<Self::Output> {
        self.projection.finish()
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
        let count = from.inclusive_cardinality(to)?;
        if before {
            self.result.before = range_merge::add(self.result.before, count)?;
            if !adjacent || !self.last_before {
                self.before_intervals = increment(self.before_intervals, "before feed intervals")?;
            }
        }
        if after {
            self.result.after = range_merge::add(self.result.after, count)?;
            if !adjacent || !self.last_after {
                self.after_intervals = increment(self.after_intervals, "after feed intervals")?;
            }
        }
        match (before, after) {
            (true, true) => {
                self.result.unchanged = range_merge::add(self.result.unchanged, count)?;
            }
            (true, false) => {
                self.result.removed = range_merge::add(self.result.removed, count)?;
            }
            (false, true) => self.result.added = range_merge::add(self.result.added, count)?,
            (false, false) => {}
        }
        self.last_to = Some(to);
        self.last_before = before;
        self.last_after = after;
        Ok(())
    }

    fn finish(self) -> Result<ScannedComparison> {
        let before = range_merge::add(self.result.unchanged, self.result.removed)?;
        let after = range_merge::add(self.result.unchanged, self.result.added)?;
        if before != self.result.before
            || after != self.result.after
            || self.result.changed != Cardinality129::ZERO
            || (self.after_intervals == 0) != (self.result.after == Cardinality129::ZERO)
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

fn increment(value: u64, context: &'static str) -> Result<u64> {
    value
        .checked_add(1)
        .ok_or_else(|| Error::arithmetic_overflow(context))
}
