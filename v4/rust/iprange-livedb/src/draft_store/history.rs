//! One-pass projection of a last-seen map into several named feeds.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::MembershipOperation;
use crate::error::{Error, Result};
use crate::heap::Heap;
use crate::history::{HistoryProjectionReport, HistoryWindow, HistoryWindowReport};
use crate::key::IpKey;
use crate::membership_dictionary::Words;
use crate::workflow::LogicalChange;

use super::range_merge::{Incoming, OrderedMerge, Policy};
use super::{DraftStore, MembershipHandle};

pub(crate) struct HistoryMerge<K: IpKey> {
    inner: OrderedMerge<K, u32, HistoryPolicy<K>>,
    created_feed_count: u64,
}

pub(crate) struct HistoryPlan<K: IpKey> {
    policy: HistoryPolicy<K>,
    created_feed_count: u64,
}

struct HistoryPolicy<K> {
    reports: Vec<HistoryWindowReport>,
    runs: Vec<Run<K>>,
    cutoff_order: Vec<u32>,
    rank: Vec<u32>,
    feed_indexes: Vec<u32>,
    feed_to_window: Vec<u32>,
    before_sorted: Vec<u8>,
    before: Vec<u8>,
    prefixes: Vec<MembershipHandle>,
    current_prefix: usize,
    aggregate: HistoryWindowReport,
    aggregate_run: Run<K>,
    decoded_old: Option<u32>,
    cache: Option<Cached>,
    cancellation: CancellationToken,
}

#[derive(Clone, Copy)]
struct Cached {
    old: Option<u32>,
    prefix: usize,
    new: Option<u32>,
}

#[derive(Clone, Copy)]
struct Run<K> {
    last_to: Option<K>,
    before: bool,
    after: bool,
}

struct CollectedWindows<K> {
    reports: Vec<HistoryWindowReport>,
    runs: Vec<Run<K>>,
    cutoff_order: Vec<u32>,
    feed_order: Vec<u32>,
}

impl<K> Default for Run<K> {
    fn default() -> Self {
        Self {
            last_to: None,
            before: false,
            after: false,
        }
    }
}

impl<K: IpKey> HistoryPlan<K> {
    pub(crate) fn prepare_from<I>(
        store: &mut DraftStore<'_>,
        window_count: usize,
        windows: I,
        cancellation: &CancellationToken,
    ) -> Result<Self>
    where
        I: IntoIterator<Item = Result<HistoryWindow>>,
    {
        if window_count == 0 {
            return Err(Error::InvalidArgument("history windows are empty"));
        }
        if window_count > u32::MAX as usize {
            return Err(Error::InvalidArgument("history window count exceeds u32"));
        }
        let mut heap = Heap::new(store.budget.max_heap_bytes);
        let CollectedWindows {
            mut reports,
            runs,
            mut cutoff_order,
            mut feed_order,
        } = collect_windows(window_count, windows, &mut heap, cancellation)?;
        require_unique_names(&reports, &mut feed_order, cancellation)?;

        let (created_feed_count, original_indexes) =
            ensure_feeds(store, &mut reports, &mut heap, cancellation)?;
        let mut rank = heap.filled(window_count, 0u32, "history projection heap")?;
        order_cutoffs(&reports, &mut cutoff_order, &mut rank, cancellation)?;
        let (feed_indexes, feed_to_window) =
            order_feed_indexes(&original_indexes, feed_order, &mut heap, cancellation)?;
        let before_sorted = heap.filled(window_count, 0u8, "history projection heap")?;
        let before = heap.filled(window_count, 0u8, "history projection heap")?;
        let prefixes = heap.filled(
            window_count.saturating_add(1),
            MembershipHandle::empty(),
            "history projection heap",
        )?;
        Ok(Self {
            policy: HistoryPolicy {
                reports,
                runs,
                cutoff_order,
                rank,
                feed_indexes,
                feed_to_window,
                before_sorted,
                before,
                prefixes,
                current_prefix: 0,
                aggregate: empty_report(
                    HistoryWindow {
                        feed_name: crate::feed::FeedName::new("aggregate")
                            .expect("fixed aggregate name is valid"),
                        cutoff: 0,
                    },
                    false,
                ),
                aggregate_run: Run::default(),
                decoded_old: None,
                cache: None,
                cancellation: cancellation.clone(),
            },
            created_feed_count,
        })
    }

    pub(crate) fn begin(
        self,
        store: &mut DraftStore<'_>,
        base: &Bootstrap,
        cancellation: &CancellationToken,
    ) -> Result<HistoryMerge<K>> {
        Ok(HistoryMerge {
            inner: OrderedMerge::new(store, base, self.policy, cancellation)?,
            created_feed_count: self.created_feed_count,
        })
    }
}

fn collect_windows<I, K>(
    window_count: usize,
    windows: I,
    heap: &mut Heap,
    cancellation: &CancellationToken,
) -> Result<CollectedWindows<K>>
where
    I: IntoIterator<Item = Result<HistoryWindow>>,
{
    let mut reports = heap.vector(window_count, "history projection heap")?;
    let mut runs = heap.vector(window_count, "history projection heap")?;
    let mut cutoff_order = heap.vector(window_count, "history projection heap")?;
    let mut feed_order = heap.vector(window_count, "history projection heap")?;
    for (index, request) in windows.into_iter().enumerate() {
        if index & 4095 == 4095 {
            cancellation.check()?;
        }
        if index >= window_count {
            return Err(Error::InvalidArgument("history window count disagrees"));
        }
        reports.push(empty_report(request?, false));
        runs.push(Run::default());
        cutoff_order.push(index as u32);
        feed_order.push(index as u32);
    }
    if reports.len() != window_count {
        return Err(Error::InvalidArgument("history window count disagrees"));
    }
    Ok(CollectedWindows {
        reports,
        runs,
        cutoff_order,
        feed_order,
    })
}

fn require_unique_names(
    reports: &[HistoryWindowReport],
    feed_order: &mut [u32],
    cancellation: &CancellationToken,
) -> Result<()> {
    cancellation.check()?;
    feed_order.sort_unstable_by_key(|&index| reports[index as usize].feed_name);
    cancellation.check()?;
    for (work, pair) in feed_order.windows(2).enumerate() {
        if work & 4095 == 4095 {
            cancellation.check()?;
        }
        if reports[pair[0] as usize].feed_name == reports[pair[1] as usize].feed_name {
            return Err(Error::InvalidArgument(
                "history window feed names are not unique",
            ));
        }
    }
    Ok(())
}

fn ensure_feeds(
    store: &mut DraftStore<'_>,
    reports: &mut [HistoryWindowReport],
    heap: &mut Heap,
    cancellation: &CancellationToken,
) -> Result<(u64, Vec<u32>)> {
    let mut created_feed_count = 0u64;
    let mut indexes = heap.vector(reports.len(), "history projection heap")?;
    for (work, report) in reports.iter_mut().enumerate() {
        if work & 4095 == 4095 {
            cancellation.check()?;
        }
        let (feed, created) = store.ensure_feed(report.feed_name)?;
        report.created = created;
        created_feed_count = created_feed_count
            .checked_add(u64::from(created))
            .ok_or_else(|| Error::ArithmeticOverflow("created history feed count"))?;
        indexes.push(feed.index);
    }
    Ok((created_feed_count, indexes))
}

fn order_cutoffs(
    reports: &[HistoryWindowReport],
    cutoff_order: &mut [u32],
    rank: &mut [u32],
    cancellation: &CancellationToken,
) -> Result<()> {
    cancellation.check()?;
    cutoff_order.sort_unstable_by(|&left, &right| {
        let left = reports[left as usize];
        let right = reports[right as usize];
        (left.cutoff, left.feed_name).cmp(&(right.cutoff, right.feed_name))
    });
    cancellation.check()?;
    for (position, &window) in cutoff_order.iter().enumerate() {
        if position & 4095 == 4095 {
            cancellation.check()?;
        }
        rank[window as usize] = position as u32;
    }
    Ok(())
}

fn order_feed_indexes(
    original: &[u32],
    mut feed_to_window: Vec<u32>,
    heap: &mut Heap,
    cancellation: &CancellationToken,
) -> Result<(Vec<u32>, Vec<u32>)> {
    cancellation.check()?;
    feed_to_window.sort_unstable_by_key(|&window| original[window as usize]);
    cancellation.check()?;
    let mut indexes = heap.vector(original.len(), "history projection heap")?;
    for (work, &window) in feed_to_window.iter().enumerate() {
        if work & 4095 == 4095 {
            cancellation.check()?;
        }
        indexes.push(original[window as usize]);
    }
    Ok((indexes, feed_to_window))
}

impl<K: IpKey> HistoryMerge<K> {
    pub(crate) fn push(
        &mut self,
        store: &mut DraftStore<'_>,
        from: K,
        to: K,
        last_seen: u32,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.inner.push(
            store,
            Incoming {
                from,
                to,
                value: last_seen,
            },
            cancellation,
        )
    }

    pub(crate) fn finish(
        self,
        store: &mut DraftStore<'_>,
        cancellation: &CancellationToken,
        source_range_count: u64,
        source_addresses: Cardinality129,
    ) -> Result<HistoryProjectionReport> {
        let policy = self.inner.finish(store, cancellation)?.output;
        policy.finish_report(
            source_range_count,
            source_addresses,
            self.created_feed_count,
        )
    }
}

impl<K: IpKey> Policy<K, u32> for HistoryPolicy<K> {
    type Output = Self;

    fn transform(
        &mut self,
        store: &mut DraftStore<'_>,
        old: Option<u32>,
        incoming: Option<u32>,
    ) -> Result<Option<u32>> {
        self.current_prefix = incoming.map_or(0, |value| {
            self.cutoff_order
                .partition_point(|&window| self.reports[window as usize].cutoff < value)
        });
        let old_id = old.unwrap_or(0);
        if self.decoded_old != Some(old_id) {
            store.selected_membership_bits(
                old_id,
                &self.feed_indexes,
                &mut self.before_sorted,
                &self.cancellation,
            )?;
            for (position, &window) in self.feed_to_window.iter().enumerate() {
                if position & 4095 == 4095 {
                    self.cancellation.check()?;
                }
                self.before[window as usize] = self.before_sorted[position];
            }
            self.decoded_old = Some(old_id);
        }
        if let Some(cached) = self
            .cache
            .filter(|cached| cached.old == old && cached.prefix == self.current_prefix)
        {
            return Ok(cached.new);
        }
        if self.matches_prefix()? {
            self.cache = Some(Cached {
                old,
                prefix: self.current_prefix,
                new: old,
            });
            return Ok(old);
        }
        let without_targets = match old {
            Some(old) => {
                let all_targets = self.prefix(store, self.reports.len())?;
                let (all, words) = all_targets.stored();
                store.combine_memberships(old, all, words, MembershipOperation::Difference)?
            }
            None => None,
        };
        let prefix = self.prefix(store, self.current_prefix)?;
        let (prefix_id, prefix_words) = prefix.stored();
        let new = match (without_targets, prefix_id) {
            (None, 0) => None,
            (None, id) => Some(id),
            (Some(id), 0) => Some(id),
            (Some(id), supplied) => {
                store.combine_memberships(id, supplied, prefix_words, MembershipOperation::Union)?
            }
        };
        self.cache = Some(Cached {
            old,
            prefix: self.current_prefix,
            new,
        });
        Ok(new)
    }

    fn observe(
        &mut self,
        from: K,
        to: K,
        _old: Option<u32>,
        _incoming: Option<u32>,
        _new: Option<u32>,
    ) -> Result<()> {
        let count = from.inclusive_cardinality(to)?;
        let mut before_any = false;
        for index in 0..self.reports.len() {
            if index & 4095 == 4095 {
                self.cancellation.check()?;
            }
            let before = self.before[index] != 0;
            let after = self.rank[index] < self.current_prefix as u32;
            before_any |= before;
            observe(
                &mut self.reports[index],
                &mut self.runs[index],
                from,
                to,
                count,
                before,
                after,
            )?;
            crate::work::history_window_test(1);
        }
        observe(
            &mut self.aggregate,
            &mut self.aggregate_run,
            from,
            to,
            count,
            before_any,
            self.current_prefix != 0,
        )
    }

    fn finish(self) -> Result<Self::Output> {
        Ok(self)
    }
}

impl<K: IpKey> HistoryPolicy<K> {
    fn prefix(&mut self, store: &mut DraftStore<'_>, length: usize) -> Result<MembershipHandle> {
        let cached = *self
            .prefixes
            .get(length)
            .ok_or(Error::Corrupt("history prefix is outside the window set"))?;
        if length == 0 || !cached.is_empty() {
            return Ok(cached);
        }
        let words = PrefixWords::new(
            &self.feed_indexes,
            &self.feed_to_window,
            &self.rank,
            length,
            &self.cancellation,
        )?;
        let interned = store.intern_membership(&words)?;
        let prefix = MembershipHandle::from(interned);
        self.prefixes[length] = prefix;
        Ok(prefix)
    }

    fn matches_prefix(&self) -> Result<bool> {
        for (window, &before) in self.before.iter().enumerate() {
            if window & 4095 == 4095 {
                self.cancellation.check()?;
            }
            if (before != 0) != (self.rank[window] < self.current_prefix as u32) {
                return Ok(false);
            }
        }
        Ok(true)
    }

    fn finish_report(
        mut self,
        source_range_count: u64,
        source_addresses: Cardinality129,
        created_feed_count: u64,
    ) -> Result<HistoryProjectionReport> {
        let mut changed = created_feed_count != 0;
        for (work, report) in self.reports.iter().enumerate() {
            if work & 4095 == 4095 {
                self.cancellation.check()?;
            }
            require_balanced(report)?;
            changed |= report.added_addresses != Cardinality129::ZERO
                || report.removed_addresses != Cardinality129::ZERO;
        }
        require_balanced(&self.aggregate)?;
        let aggregate = self.aggregate;
        Ok(HistoryProjectionReport {
            logical_change: if changed {
                LogicalChange::Changed
            } else {
                LogicalChange::NoChange
            },
            source_range_count,
            source_addresses,
            created_feed_count,
            before_interval_count: aggregate.before_interval_count,
            after_interval_count: aggregate.after_interval_count,
            before_addresses: aggregate.before_addresses,
            after_addresses: aggregate.after_addresses,
            unchanged_addresses: aggregate.unchanged_addresses,
            added_addresses: aggregate.added_addresses,
            removed_addresses: aggregate.removed_addresses,
            windows: std::mem::take(&mut self.reports).into_boxed_slice(),
        })
    }
}

struct PrefixWords<'a> {
    feed_indexes: &'a [u32],
    feed_to_window: &'a [u32],
    rank: &'a [u32],
    prefix: u32,
    word_count: u32,
    cancellation: &'a CancellationToken,
}

impl<'a> PrefixWords<'a> {
    fn new(
        feed_indexes: &'a [u32],
        feed_to_window: &'a [u32],
        rank: &'a [u32],
        prefix: usize,
        cancellation: &'a CancellationToken,
    ) -> Result<Self> {
        let prefix = u32::try_from(prefix)
            .map_err(|_| Error::BudgetExceeded("history prefix exceeds u32"))?;
        let mut word_count = 0;
        for (work, (position, &index)) in feed_indexes.iter().enumerate().rev().enumerate() {
            if work & 4095 == 4095 {
                cancellation.check()?;
            }
            let window = feed_to_window[position] as usize;
            if rank[window] < prefix {
                word_count = index / 64 + 1;
                break;
            }
        }
        if word_count == 0 {
            return Err(Error::Corrupt("nonempty history prefix has no feeds"));
        }
        Ok(Self {
            feed_indexes,
            feed_to_window,
            rank,
            prefix,
            word_count,
            cancellation,
        })
    }
}

impl Words<DraftStore<'_>> for PrefixWords<'_> {
    fn word_count(&self) -> u32 {
        self.word_count
    }

    fn read_words(&self, _store: &DraftStore<'_>, start: u32, output: &mut [u64]) -> Result<()> {
        output.fill(0);
        let end = start
            .checked_add(
                u32::try_from(output.len())
                    .map_err(|_| Error::ArithmeticOverflow("history prefix word range"))?,
            )
            .ok_or_else(|| Error::ArithmeticOverflow("history prefix word range"))?;
        let first_index = start
            .checked_mul(64)
            .ok_or_else(|| Error::ArithmeticOverflow("history prefix bit range"))?;
        let mut position = self
            .feed_indexes
            .partition_point(|&index| index < first_index);
        let mut work = 0usize;
        while let Some(&index) = self.feed_indexes.get(position) {
            let word = index / 64;
            if word >= end {
                break;
            }
            if work & 4095 == 4095 {
                self.cancellation.check()?;
            }
            let window = self.feed_to_window[position] as usize;
            if self.rank[window] < self.prefix {
                output[(word - start) as usize] |= 1u64 << (index % 64);
            }
            position += 1;
            work += 1;
        }
        Ok(())
    }
}

fn observe<K: IpKey>(
    report: &mut HistoryWindowReport,
    run: &mut Run<K>,
    from: K,
    to: K,
    count: Cardinality129,
    before: bool,
    after: bool,
) -> Result<()> {
    let adjacent = run.last_to.and_then(IpKey::checked_next) == Some(from);
    if before {
        report.before_addresses = add(report.before_addresses, count)?;
        if !adjacent || !run.before {
            report.before_interval_count = increment(report.before_interval_count)?;
        }
    }
    if after {
        report.after_addresses = add(report.after_addresses, count)?;
        if !adjacent || !run.after {
            report.after_interval_count = increment(report.after_interval_count)?;
        }
    }
    match (before, after) {
        (true, true) => report.unchanged_addresses = add(report.unchanged_addresses, count)?,
        (true, false) => report.removed_addresses = add(report.removed_addresses, count)?,
        (false, true) => report.added_addresses = add(report.added_addresses, count)?,
        (false, false) => {}
    }
    run.last_to = Some(to);
    run.before = before;
    run.after = after;
    Ok(())
}

fn require_balanced(report: &HistoryWindowReport) -> Result<()> {
    let before = add(report.unchanged_addresses, report.removed_addresses)?;
    let after = add(report.unchanged_addresses, report.added_addresses)?;
    if before != report.before_addresses
        || after != report.after_addresses
        || (report.before_interval_count == 0) != (report.before_addresses == Cardinality129::ZERO)
        || (report.after_interval_count == 0) != (report.after_addresses == Cardinality129::ZERO)
    {
        Err(Error::Corrupt("history projection counters do not balance"))
    } else {
        Ok(())
    }
}

fn empty_report(window: HistoryWindow, created: bool) -> HistoryWindowReport {
    HistoryWindowReport {
        feed_name: window.feed_name,
        cutoff: window.cutoff,
        created,
        before_interval_count: 0,
        after_interval_count: 0,
        before_addresses: Cardinality129::ZERO,
        after_addresses: Cardinality129::ZERO,
        unchanged_addresses: Cardinality129::ZERO,
        added_addresses: Cardinality129::ZERO,
        removed_addresses: Cardinality129::ZERO,
    }
}

fn add(left: Cardinality129, right: Cardinality129) -> Result<Cardinality129> {
    left.checked_add(right)
        .map_err(|_| Error::ArithmeticOverflow("history projection addresses"))
}

fn increment(value: u64) -> Result<u64> {
    value
        .checked_add(1)
        .ok_or_else(|| Error::ArithmeticOverflow("history projection intervals"))
}
