//! First-seen and last-seen semantics over the authoritative ordered range merge.

use std::mem::MaybeUninit;

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::AddressFamily;
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::workflow::{Comparison, FirstSeenRemoval, FirstSeenRemovalSink};

use super::range_merge::{self, MapComparison, Policy};
use super::DraftStore;

pub(crate) struct TimestampMerge {
    pub(crate) input_intervals: u64,
    pub(crate) input_addresses: Cardinality129,
    pub(crate) comparison: Comparison,
}

impl DraftStore<'_> {
    pub(crate) fn merge_first_seen(
        &mut self,
        base: &Bootstrap,
        refresh_value: u32,
        cancellation: &CancellationToken,
    ) -> Result<TimestampMerge> {
        self.merge_timestamp(
            base,
            FirstSeenPolicy::new(refresh_value, NoRemovals),
            cancellation,
        )
    }

    pub(crate) fn merge_first_seen_with_removals<K, S>(
        &mut self,
        base: &Bootstrap,
        refresh_value: u32,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<TimestampMerge>
    where
        K: IpKey,
        S: FirstSeenRemovalSink<K>,
    {
        if base.meta.address_family != K::FAMILY {
            return Err(Error::WrongAddressFamily(
                "removal sink family does not match the database",
            ));
        }
        self.merge_timestamp_family::<K, _>(
            base,
            FirstSeenPolicy::new(refresh_value, BatchedRemovals::new(sink)),
            cancellation,
        )
    }

    pub(crate) fn merge_last_seen(
        &mut self,
        base: &Bootstrap,
        refresh_value: u32,
        cutoff: u32,
        cancellation: &CancellationToken,
    ) -> Result<TimestampMerge> {
        self.merge_timestamp(
            base,
            LastSeenPolicy::new(refresh_value, cutoff),
            cancellation,
        )
    }

    fn merge_timestamp<P>(
        &mut self,
        base: &Bootstrap,
        policy: P,
        cancellation: &CancellationToken,
    ) -> Result<TimestampMerge>
    where
        P: Policy<Ipv4Key, (), Output = TimestampOutput>
            + Policy<Ipv6Key, (), Output = TimestampOutput>,
    {
        match base.meta.address_family {
            AddressFamily::Ipv4 => {
                self.merge_timestamp_family::<Ipv4Key, _>(base, policy, cancellation)
            }
            AddressFamily::Ipv6 => {
                self.merge_timestamp_family::<Ipv6Key, _>(base, policy, cancellation)
            }
        }
    }

    fn merge_timestamp_family<K, P>(
        &mut self,
        base: &Bootstrap,
        policy: P,
        cancellation: &CancellationToken,
    ) -> Result<TimestampMerge>
    where
        K: IpKey,
        P: Policy<K, (), Output = TimestampOutput>,
    {
        let input_meta = self.draft.meta;
        let (input_intervals, finished) = range_merge::merge_coverage::<K, _>(
            self,
            &input_meta,
            base,
            policy,
            cancellation,
            "timestamp input intervals",
        )?;
        Ok(TimestampMerge {
            input_intervals,
            input_addresses: finished.output.input_addresses,
            comparison: finished.output.comparison,
        })
    }
}

struct TimestampOutput {
    input_addresses: Cardinality129,
    comparison: Comparison,
}

#[derive(Default)]
struct TimestampCounters {
    input_addresses: Cardinality129,
    comparison: MapComparison,
}

impl TimestampCounters {
    fn observe<K: IpKey>(
        &mut self,
        from: K,
        to: K,
        old: Option<u32>,
        incoming: Option<()>,
        new: Option<u32>,
    ) -> Result<Cardinality129> {
        let count = from.inclusive_cardinality(to)?;
        if incoming.is_some() {
            self.input_addresses = range_merge::add(self.input_addresses, count)?;
        }
        self.comparison.observe_count(count, old, new)?;
        Ok(count)
    }

    fn finish(self) -> Result<TimestampOutput> {
        Ok(TimestampOutput {
            input_addresses: self.input_addresses,
            comparison: self.comparison.finish()?,
        })
    }
}

struct FirstSeenPolicy<O> {
    refresh_value: u32,
    counters: TimestampCounters,
    removals: O,
}

impl<O> FirstSeenPolicy<O> {
    fn new(refresh_value: u32, removals: O) -> Self {
        Self {
            refresh_value,
            counters: TimestampCounters::default(),
            removals,
        }
    }
}

impl<K, O> Policy<K, ()> for FirstSeenPolicy<O>
where
    K: IpKey,
    O: RemovalObserver<K>,
{
    type Output = TimestampOutput;

    fn transform(
        &mut self,
        _store: &mut DraftStore<'_>,
        old: Option<u32>,
        incoming: Option<()>,
    ) -> Result<Option<u32>> {
        Ok(match (old, incoming) {
            (Some(value), Some(())) => Some(value),
            (None, Some(())) => Some(self.refresh_value),
            (_, None) => None,
        })
    }

    fn observe(
        &mut self,
        from: K,
        to: K,
        old: Option<u32>,
        incoming: Option<()>,
        new: Option<u32>,
    ) -> Result<()> {
        let addresses = self.counters.observe(from, to, old, incoming, new)?;
        if let (Some(first_seen), None) = (old, incoming) {
            self.removals.push(FirstSeenRemoval {
                from,
                to,
                first_seen,
                addresses,
            })?;
        }
        Ok(())
    }

    fn finish(self) -> Result<Self::Output> {
        self.removals.finish()?;
        self.counters.finish()
    }
}

trait RemovalObserver<K> {
    fn push(&mut self, removal: FirstSeenRemoval<K>) -> Result<()>;
    fn finish(self) -> Result<()>;
}

struct NoRemovals;

impl<K> RemovalObserver<K> for NoRemovals {
    #[inline]
    fn push(&mut self, _removal: FirstSeenRemoval<K>) -> Result<()> {
        Ok(())
    }

    #[inline]
    fn finish(self) -> Result<()> {
        Ok(())
    }
}

const REMOVAL_BATCH_CAPACITY: usize = 64;

struct BatchedRemovals<'a, K: Copy, S> {
    sink: &'a mut S,
    records: [MaybeUninit<FirstSeenRemoval<K>>; REMOVAL_BATCH_CAPACITY],
    len: usize,
}

impl<'a, K: Copy, S> BatchedRemovals<'a, K, S> {
    fn new(sink: &'a mut S) -> Self {
        Self {
            sink,
            records: [MaybeUninit::uninit(); REMOVAL_BATCH_CAPACITY],
            len: 0,
        }
    }

    fn flush(&mut self) -> Result<()>
    where
        S: FirstSeenRemovalSink<K>,
    {
        if self.len == 0 {
            return Ok(());
        }
        // SAFETY: the prefix is initialized by `push`, all records are Copy,
        // and the borrowed slice cannot outlive this synchronous call.
        let batch = unsafe {
            std::slice::from_raw_parts(
                self.records.as_ptr().cast::<FirstSeenRemoval<K>>(),
                self.len,
            )
        };
        self.sink.removals(batch)?;
        self.len = 0;
        Ok(())
    }
}

impl<K, S> RemovalObserver<K> for BatchedRemovals<'_, K, S>
where
    K: Copy,
    S: FirstSeenRemovalSink<K>,
{
    fn push(&mut self, removal: FirstSeenRemoval<K>) -> Result<()> {
        self.records[self.len].write(removal);
        self.len += 1;
        if self.len == REMOVAL_BATCH_CAPACITY {
            self.flush()?;
        }
        Ok(())
    }

    fn finish(mut self) -> Result<()> {
        self.flush()
    }
}

struct LastSeenPolicy {
    refresh_value: u32,
    cutoff: u32,
    counters: TimestampCounters,
}

impl LastSeenPolicy {
    fn new(refresh_value: u32, cutoff: u32) -> Self {
        Self {
            refresh_value,
            cutoff,
            counters: TimestampCounters::default(),
        }
    }
}

impl<K: IpKey> Policy<K, ()> for LastSeenPolicy {
    type Output = TimestampOutput;

    fn transform(
        &mut self,
        _store: &mut DraftStore<'_>,
        old: Option<u32>,
        incoming: Option<()>,
    ) -> Result<Option<u32>> {
        Ok(match (old, incoming) {
            (Some(value), Some(())) => Some(value.max(self.refresh_value)),
            (None, Some(())) => Some(self.refresh_value),
            (Some(value), None) if value > self.cutoff => Some(value),
            _ => None,
        })
    }

    fn observe(
        &mut self,
        from: K,
        to: K,
        old: Option<u32>,
        incoming: Option<()>,
        new: Option<u32>,
    ) -> Result<()> {
        self.counters
            .observe(from, to, old, incoming, new)
            .map(|_| ())
    }

    fn finish(self) -> Result<Self::Output> {
        self.counters.finish()
    }
}
