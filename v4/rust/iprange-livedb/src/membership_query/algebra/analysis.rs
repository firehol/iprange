//! Exact analytical set operations over one global algebra sweep.

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::AddressFamily;
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};

use super::scan::{self, SegmentSink};
use super::selection::Selection;
use super::{AlgebraAccess, AlgebraComparisonReport, AlgebraCountReport, FeedSelection};

pub(crate) fn count(
    algebra: &(impl AlgebraAccess + ?Sized),
    feeds: FeedSelection<'_>,
    reserved_heap_bytes: u64,
    cancellation: &CancellationToken,
) -> Result<AlgebraCountReport> {
    cancellation.check()?;
    let mut heap = algebra.operation_heap_reserved(reserved_heap_bytes)?;
    let selection = Selection::resolve(algebra, feeds, &mut heap, cancellation)?;
    let mut sink = CountSink {
        selection,
        addresses: Cardinality129::ZERO,
    };
    let scanned = match algebra.state().family() {
        AddressFamily::Ipv4 => {
            scan::run::<Ipv4Key, _>(algebra, &mut heap, &mut sink, cancellation)?
        }
        AddressFamily::Ipv6 => {
            scan::run::<Ipv6Key, _>(algebra, &mut heap, &mut sink, cancellation)?
        }
    };
    Ok(AlgebraCountReport {
        source_count: algebra.state().input_count() as u64,
        source_range_count: scanned.source_ranges,
        joined_segment_count: scanned.segments,
        addresses: sink.addresses,
    })
}

pub(crate) fn compare(
    algebra: &(impl AlgebraAccess + ?Sized),
    left: FeedSelection<'_>,
    right: FeedSelection<'_>,
    reserved_heap_bytes: u64,
    cancellation: &CancellationToken,
) -> Result<AlgebraComparisonReport> {
    cancellation.check()?;
    let mut heap = algebra.operation_heap_reserved(reserved_heap_bytes)?;
    let left = Selection::resolve(algebra, left, &mut heap, cancellation)?;
    let right = Selection::resolve(algebra, right, &mut heap, cancellation)?;
    let mut sink = ComparisonSink {
        left,
        right,
        left_addresses: Cardinality129::ZERO,
        right_addresses: Cardinality129::ZERO,
        overlap_addresses: Cardinality129::ZERO,
        left_only_addresses: Cardinality129::ZERO,
        right_only_addresses: Cardinality129::ZERO,
        union_addresses: Cardinality129::ZERO,
    };
    let scanned = match algebra.state().family() {
        AddressFamily::Ipv4 => {
            scan::run::<Ipv4Key, _>(algebra, &mut heap, &mut sink, cancellation)?
        }
        AddressFamily::Ipv6 => {
            scan::run::<Ipv6Key, _>(algebra, &mut heap, &mut sink, cancellation)?
        }
    };
    Ok(AlgebraComparisonReport {
        source_count: algebra.state().input_count() as u64,
        source_range_count: scanned.source_ranges,
        joined_segment_count: scanned.segments,
        left_addresses: sink.left_addresses,
        right_addresses: sink.right_addresses,
        overlap_addresses: sink.overlap_addresses,
        left_only_addresses: sink.left_only_addresses,
        right_only_addresses: sink.right_only_addresses,
        union_addresses: sink.union_addresses,
        equal: sink.left_only_addresses == Cardinality129::ZERO
            && sink.right_only_addresses == Cardinality129::ZERO,
    })
}

struct CountSink {
    selection: Selection,
    addresses: Cardinality129,
}

impl<K: IpKey> SegmentSink<K> for CountSink {
    fn segment(
        &mut self,
        from: K,
        to: K,
        present: &[u32],
        counts: &[u32],
        cancellation: &CancellationToken,
    ) -> Result<()> {
        if self.selection.any(present, counts, cancellation)? {
            self.addresses = add(self.addresses, from.inclusive_cardinality(to)?)?;
        }
        Ok(())
    }
}

struct ComparisonSink {
    left: Selection,
    right: Selection,
    left_addresses: Cardinality129,
    right_addresses: Cardinality129,
    overlap_addresses: Cardinality129,
    left_only_addresses: Cardinality129,
    right_only_addresses: Cardinality129,
    union_addresses: Cardinality129,
}

impl<K: IpKey> SegmentSink<K> for ComparisonSink {
    fn segment(
        &mut self,
        from: K,
        to: K,
        present: &[u32],
        counts: &[u32],
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let left = self.left.any(present, counts, cancellation)?;
        let right = self.right.any(present, counts, cancellation)?;
        if !left && !right {
            return Ok(());
        }
        let addresses = from.inclusive_cardinality(to)?;
        self.union_addresses = add(self.union_addresses, addresses)?;
        match (left, right) {
            (true, true) => {
                self.left_addresses = add(self.left_addresses, addresses)?;
                self.right_addresses = add(self.right_addresses, addresses)?;
                self.overlap_addresses = add(self.overlap_addresses, addresses)?;
            }
            (true, false) => {
                self.left_addresses = add(self.left_addresses, addresses)?;
                self.left_only_addresses = add(self.left_only_addresses, addresses)?;
            }
            (false, true) => {
                self.right_addresses = add(self.right_addresses, addresses)?;
                self.right_only_addresses = add(self.right_only_addresses, addresses)?;
            }
            (false, false) => unreachable!(),
        }
        Ok(())
    }
}

fn add(left: Cardinality129, right: Cardinality129) -> Result<Cardinality129> {
    left.checked_add(right)
        .map_err(|_| Error::ArithmeticOverflow("membership algebra addresses"))
}
