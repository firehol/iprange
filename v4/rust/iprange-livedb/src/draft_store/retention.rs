//! Retention semantics over the authoritative ordered range merge.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::AddressFamily;
use crate::error::Result;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::workflow::Comparison;

use super::range_merge::{self, MapComparison, Policy};
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
        let policy = RetentionPolicy::new(refresh_value);
        let (input_intervals, finished) = range_merge::merge_coverage::<K, _>(
            self,
            &input_meta,
            base,
            policy,
            cancellation,
            "retention input intervals",
        )?;
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
