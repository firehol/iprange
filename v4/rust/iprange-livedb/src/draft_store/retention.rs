//! Retention semantics over the authoritative ordered range merge.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::AddressFamily;
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::range_store_cursor::Cursor;
use crate::workflow::Comparison;

use super::range_merge::{Incoming, MapComparison, OrderedMerge, Policy};
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
        let input_meta = self.draft.meta;
        let mut coverage = Cursor::<K>::new(self, &input_meta, true)?;
        let policy = RetentionPolicy::new(refresh_value);
        let mut merge = OrderedMerge::new(self, base, policy, cancellation)?;
        let mut input_intervals = 0u64;
        while let Some(range) = coverage.next(self)? {
            cancellation.check()?;
            input_intervals = input_intervals
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("retention input intervals"))?;
            merge.push(
                self,
                Incoming {
                    from: range.from,
                    to: range.to,
                    value: (),
                },
                cancellation,
            )?;
        }
        let finished = merge.finish(self, cancellation)?;
        let input_addresses = finished.output.after;
        Ok(RetentionMerge {
            input_intervals,
            input_addresses,
            comparison: finished.output,
        })
    }
}

struct RetentionPolicy {
    refresh_value: u32,
    comparison: MapComparison,
}

impl RetentionPolicy {
    fn new(refresh_value: u32) -> Self {
        Self {
            refresh_value,
            comparison: MapComparison::default(),
        }
    }
}

impl<K: IpKey> Policy<K, ()> for RetentionPolicy {
    type Output = Comparison;

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
        _incoming: Option<()>,
        new: Option<u32>,
    ) -> Result<()> {
        self.comparison.observe(from, to, old, new)
    }

    fn finish(self) -> Result<Self::Output> {
        self.comparison.finish()
    }
}
