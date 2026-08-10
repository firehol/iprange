//! One-scan exact feed cardinality and overlap aggregation.

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::error::{Error, Result};
use crate::feed::FeedName;
use crate::heap::Heap;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::reader_core::GenerationReader;

use super::decode::Scratch;
use super::scope::ScopeData;
use super::{FeedPair, MembershipScope, MembershipScopeView};

const RESULT_BATCH: usize = 32;

/// Pair work requested for one scoped membership scan.
#[derive(Clone, Copy, Debug)]
#[allow(clippy::large_enum_variant)] // FeedName stays inline; query setup must not allocate.
pub enum MembershipAggregationMode<'a> {
    Cardinalities,
    AllPairs,
    TargetAgainstScope(FeedName),
    SelectedPairs(&'a [FeedPair]),
}

/// Exact address count for one selected feed.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FeedCardinality {
    pub feed: FeedName,
    pub addresses: Cardinality129,
}

/// Exact address overlap for one unordered pair.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FeedOverlap {
    pub left: FeedName,
    pub right: FeedName,
    pub addresses: Cardinality129,
}

/// Synchronous consumer for bounded terminal aggregation batches.
pub trait MembershipAggregateSink {
    fn feed_cardinalities(&mut self, batch: &[FeedCardinality]) -> Result<()>;
    fn feed_overlaps(&mut self, batch: &[FeedOverlap]) -> Result<()>;
}

/// Exact work and output counts for one completed membership scan.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct MembershipAggregationReport {
    pub scanned_range_count: u64,
    pub scanned_addresses: Cardinality129,
    pub feed_result_count: u64,
    pub pair_result_count: u64,
}

#[derive(Clone, Copy, Default)]
struct PairCell {
    left: u32,
    right: u32,
    owner: u32,
    other: u32,
    addresses: Cardinality129,
}

enum PairPlan {
    None,
    All {
        totals: Vec<Cardinality129>,
        offsets: Vec<usize>,
    },
    Listed {
        cells: Vec<PairCell>,
        offsets: Vec<usize>,
    },
}

impl MembershipScope<'_> {
    /// Scan this scope once, emit every scoped feed count, and emit requested pairs.
    pub fn aggregate<S: MembershipAggregateSink>(
        &self,
        mode: MembershipAggregationMode<'_>,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MembershipAggregationReport> {
        self.view().aggregate(mode, sink, cancellation)
    }
}

impl MembershipScopeView<'_> {
    pub(crate) fn aggregate<S: MembershipAggregateSink>(
        self,
        mode: MembershipAggregationMode<'_>,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MembershipAggregationReport> {
        self.aggregate_reserved(mode, 0, sink, cancellation)
    }

    pub(crate) fn aggregate_reserved<S: MembershipAggregateSink>(
        self,
        mode: MembershipAggregationMode<'_>,
        reserved_heap_bytes: u64,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MembershipAggregationReport> {
        cancellation.check()?;
        let mut heap = self
            .state
            .data
            .operation_heap_reserved(reserved_heap_bytes)?;
        let mut totals = heap.filled(
            self.state.data.entries.len(),
            Cardinality129::ZERO,
            "membership aggregation heap",
        )?;
        let mut pairs =
            PairPlan::new(self.reader, &self.state.data, mode, &mut heap, cancellation)?;
        let mut scratch = Scratch::new(self.state.data.entries.len(), &mut heap)?;
        let cache_bytes = heap.remaining();
        scratch.enable_cache(&mut heap, cache_bytes)?;
        let (scanned_range_count, scanned_addresses) = match self.state.address_family {
            crate::contract::AddressFamily::Ipv4 => scan::<Ipv4Key>(
                self.reader,
                self.state.range_record_count,
                &self.state.data,
                &mut totals,
                &mut pairs,
                &mut scratch,
                cancellation,
            )?,
            crate::contract::AddressFamily::Ipv6 => scan::<Ipv6Key>(
                self.reader,
                self.state.range_record_count,
                &self.state.data,
                &mut totals,
                &mut pairs,
                &mut scratch,
                cancellation,
            )?,
        };
        emit_feeds(&self.state.data, &totals, sink, cancellation)?;
        let pair_result_count = pairs.emit(&self.state.data, sink, cancellation)?;
        Ok(MembershipAggregationReport {
            scanned_range_count,
            scanned_addresses,
            feed_result_count: self.state.data.entries.len() as u64,
            pair_result_count,
        })
    }
}

impl PairPlan {
    fn new(
        reader: GenerationReader<'_>,
        scope: &ScopeData,
        mode: MembershipAggregationMode<'_>,
        heap: &mut Heap,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        match mode {
            MembershipAggregationMode::Cardinalities => Ok(Self::None),
            MembershipAggregationMode::AllPairs => {
                Self::all(scope.entries.len(), heap, cancellation)
            }
            MembershipAggregationMode::TargetAgainstScope(target) => {
                Self::target(reader, scope, target, heap, cancellation)
            }
            MembershipAggregationMode::SelectedPairs(requested) => {
                Self::selected(reader, scope, requested, heap, cancellation)
            }
        }
    }

    fn all(feeds: usize, heap: &mut Heap, cancellation: &CancellationToken) -> Result<Self> {
        let count = pair_count(feeds)?;
        let totals = heap.filled(
            count,
            Cardinality129::ZERO,
            "membership pair aggregation heap",
        )?;
        let mut offsets = heap.filled(feeds, 0usize, "membership pair aggregation heap")?;
        let mut next = 0usize;
        for (left, offset) in offsets.iter_mut().enumerate() {
            if left & 4095 == 4095 {
                cancellation.check()?;
            }
            *offset = next;
            next = next
                .checked_add(feeds.saturating_sub(left + 1))
                .ok_or_else(|| Error::ArithmeticOverflow("membership pair index"))?;
        }
        if next != count {
            return Err(Error::ArithmeticOverflow("membership pair index"));
        }
        Ok(Self::All { totals, offsets })
    }

    fn target(
        reader: GenerationReader<'_>,
        scope: &ScopeData,
        target: FeedName,
        heap: &mut Heap,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        let target = scope.position_name(reader, &target)?;
        let mut cells = heap.vector(
            scope.entries.len().saturating_sub(1),
            "membership pair aggregation heap",
        )?;
        for other in 0..scope.entries.len() {
            if other & 4095 == 4095 {
                cancellation.check()?;
            }
            if other == target {
                continue;
            }
            let (left, right) = ordered_pair(target, other);
            cells.push(PairCell {
                left: left as u32,
                right: right as u32,
                owner: target as u32,
                other: other as u32,
                addresses: Cardinality129::ZERO,
            });
        }
        Self::listed(cells, scope.entries.len(), heap, cancellation)
    }

    fn selected(
        reader: GenerationReader<'_>,
        scope: &ScopeData,
        requested: &[FeedPair],
        heap: &mut Heap,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        if requested.is_empty() {
            return Err(Error::InvalidArgument("selected feed pairs are empty"));
        }
        let mut cells = heap.vector(requested.len(), "membership pair aggregation heap")?;
        for (work, pair) in requested.iter().enumerate() {
            if work & 4095 == 4095 {
                cancellation.check()?;
            }
            let left = scope.position_name(reader, &pair.left)?;
            let right = scope.position_name(reader, &pair.right)?;
            if left == right {
                return Err(Error::InvalidArgument(
                    "a feed pair must contain two different feeds",
                ));
            }
            let (left, right) = ordered_pair(left, right);
            cells.push(PairCell {
                left: left as u32,
                right: right as u32,
                owner: left as u32,
                other: right as u32,
                addresses: Cardinality129::ZERO,
            });
        }
        Self::listed(cells, scope.entries.len(), heap, cancellation)
    }

    fn listed(
        mut cells: Vec<PairCell>,
        feeds: usize,
        heap: &mut Heap,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        cancellation.check()?;
        cells.sort_unstable_by_key(|cell| (cell.owner, cell.left, cell.right));
        cancellation.check()?;
        for (work, pair) in cells.windows(2).enumerate() {
            if work & 4095 == 4095 {
                cancellation.check()?;
            }
            if (pair[0].left, pair[0].right) == (pair[1].left, pair[1].right) {
                return Err(Error::InvalidArgument("selected feed pairs are not unique"));
            }
        }
        let mut offsets = heap.filled(
            feeds
                .checked_add(1)
                .ok_or_else(|| Error::BudgetExceeded("membership pair aggregation heap"))?,
            0usize,
            "membership pair aggregation heap",
        )?;
        for (work, cell) in cells.iter().enumerate() {
            if work & 4095 == 4095 {
                cancellation.check()?;
            }
            let next = cell.owner as usize + 1;
            offsets[next] = offsets[next]
                .checked_add(1)
                .ok_or_else(|| Error::ArithmeticOverflow("membership pair adjacency"))?;
        }
        for index in 1..offsets.len() {
            if index & 4095 == 4095 {
                cancellation.check()?;
            }
            offsets[index] = offsets[index]
                .checked_add(offsets[index - 1])
                .ok_or_else(|| Error::ArithmeticOverflow("membership pair adjacency"))?;
        }
        Ok(Self::Listed { cells, offsets })
    }

    fn add(
        &mut self,
        present: &[u32],
        flags: &[u8],
        count: Cardinality129,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let mut work = 0usize;
        match self {
            Self::None => Ok(()),
            Self::All { totals, offsets } => {
                for (left_offset, &left) in present.iter().enumerate() {
                    for &right in &present[left_offset + 1..] {
                        if work & 4095 == 4095 {
                            cancellation.check()?;
                        }
                        let index = offsets[left as usize]
                            .checked_add(right as usize - left as usize - 1)
                            .ok_or_else(|| Error::ArithmeticOverflow("membership pair index"))?;
                        totals[index] = add(totals[index], count)?;
                        crate::work::aggregation_contribution(1);
                        work += 1;
                    }
                }
                Ok(())
            }
            Self::Listed { cells, offsets } => {
                for &owner in present {
                    let start = offsets[owner as usize];
                    let end = offsets[owner as usize + 1];
                    for cell in &mut cells[start..end] {
                        if work & 4095 == 4095 {
                            cancellation.check()?;
                        }
                        if flags[cell.other as usize] != 0 {
                            cell.addresses = add(cell.addresses, count)?;
                            crate::work::aggregation_contribution(1);
                        }
                        work += 1;
                    }
                }
                Ok(())
            }
        }
    }

    fn emit<S: MembershipAggregateSink>(
        &self,
        scope: &ScopeData,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<u64> {
        match self {
            Self::None => Ok(0),
            Self::All { totals, .. } => emit_all_pairs(scope, totals, sink, cancellation),
            Self::Listed { cells, .. } => emit_listed_pairs(scope, cells, sink, cancellation),
        }
    }
}

fn scan<K: IpKey>(
    reader: GenerationReader<'_>,
    expected_ranges: u64,
    scope: &ScopeData,
    totals: &mut [Cardinality129],
    pairs: &mut PairPlan,
    scratch: &mut Scratch,
    cancellation: &CancellationToken,
) -> Result<(u64, Cardinality129)> {
    if scope.entries.is_empty() {
        if expected_ranges != 0 {
            return Err(Error::Corrupt("an empty catalog has membership ranges"));
        }
        return Ok((0, Cardinality129::ZERO));
    }
    crate::work::input_source_pass(1);
    let mut cursor = reader.membership_ranges::<K>()?;
    let mut scanned_range_count = 0u64;
    let mut scanned_addresses = Cardinality129::ZERO;
    let mut pending_membership = None;
    let mut pending_addresses = Cardinality129::ZERO;
    while let Some(range) = cursor.next()? {
        if scanned_range_count & 4095 == 4095 {
            cancellation.check()?;
        }
        let count = range.from.inclusive_cardinality(range.to)?;
        if pending_membership == Some(range.membership) {
            pending_addresses = add(pending_addresses, count)?;
        } else {
            contribute(totals, pairs, scratch, pending_addresses, cancellation)?;
            scratch.load(reader, range.membership, scope, cancellation)?;
            pending_membership = Some(range.membership);
            pending_addresses = count;
        }
        scanned_range_count = scanned_range_count
            .checked_add(1)
            .ok_or_else(|| Error::ArithmeticOverflow("membership scan range count"))?;
        scanned_addresses = add(scanned_addresses, count)?;
    }
    contribute(totals, pairs, scratch, pending_addresses, cancellation)?;
    cancellation.check()?;
    if scanned_range_count != expected_ranges {
        return Err(Error::Corrupt("membership range count disagrees"));
    }
    Ok((scanned_range_count, scanned_addresses))
}

fn contribute(
    totals: &mut [Cardinality129],
    pairs: &mut PairPlan,
    scratch: &Scratch,
    count: Cardinality129,
    cancellation: &CancellationToken,
) -> Result<()> {
    if count == Cardinality129::ZERO {
        return Ok(());
    }
    for (work, &position) in scratch.present.iter().enumerate() {
        if work & 4095 == 4095 {
            cancellation.check()?;
        }
        let total = &mut totals[position as usize];
        *total = add(*total, count)?;
        crate::work::aggregation_contribution(1);
    }
    pairs.add(&scratch.present, &scratch.flags, count, cancellation)
}

fn emit_feeds<S: MembershipAggregateSink>(
    scope: &ScopeData,
    totals: &[Cardinality129],
    sink: &mut S,
    cancellation: &CancellationToken,
) -> Result<()> {
    let Some(first) = scope.entries.first() else {
        return Ok(());
    };
    let empty = FeedCardinality {
        feed: first.name,
        addresses: Cardinality129::ZERO,
    };
    let mut batch = [empty; RESULT_BATCH];
    let mut used = 0usize;
    for (entry, &addresses) in scope.entries.iter().zip(totals) {
        batch[used] = FeedCardinality {
            feed: entry.name,
            addresses,
        };
        used += 1;
        if used == batch.len() {
            cancellation.check()?;
            sink.feed_cardinalities(&batch)?;
            crate::work::aggregation_result(used as u64);
            used = 0;
        }
    }
    if used != 0 {
        cancellation.check()?;
        sink.feed_cardinalities(&batch[..used])?;
        crate::work::aggregation_result(used as u64);
    }
    Ok(())
}

fn emit_all_pairs<S: MembershipAggregateSink>(
    scope: &ScopeData,
    totals: &[Cardinality129],
    sink: &mut S,
    cancellation: &CancellationToken,
) -> Result<u64> {
    let count = pair_count(scope.entries.len())?;
    if count == 0 {
        return Ok(0);
    }
    let first = FeedOverlap {
        left: scope.entries[0].name,
        right: scope.entries[1].name,
        addresses: Cardinality129::ZERO,
    };
    let mut batch = [first; RESULT_BATCH];
    let mut used = 0usize;
    let mut index = 0usize;
    for left in 0..scope.entries.len() {
        for right in left + 1..scope.entries.len() {
            batch[used] = FeedOverlap {
                left: scope.entries[left].name,
                right: scope.entries[right].name,
                addresses: totals[index],
            };
            index += 1;
            used += 1;
            if used == batch.len() {
                cancellation.check()?;
                sink.feed_overlaps(&batch)?;
                crate::work::aggregation_result(used as u64);
                used = 0;
            }
        }
    }
    if used != 0 {
        cancellation.check()?;
        sink.feed_overlaps(&batch[..used])?;
        crate::work::aggregation_result(used as u64);
    }
    Ok(count as u64)
}

fn emit_listed_pairs<S: MembershipAggregateSink>(
    scope: &ScopeData,
    cells: &[PairCell],
    sink: &mut S,
    cancellation: &CancellationToken,
) -> Result<u64> {
    let Some(first) = cells.first() else {
        return Ok(0);
    };
    let empty = FeedOverlap {
        left: scope.entries[first.left as usize].name,
        right: scope.entries[first.right as usize].name,
        addresses: Cardinality129::ZERO,
    };
    let mut batch = [empty; RESULT_BATCH];
    let mut used = 0usize;
    for cell in cells {
        batch[used] = FeedOverlap {
            left: scope.entries[cell.left as usize].name,
            right: scope.entries[cell.right as usize].name,
            addresses: cell.addresses,
        };
        used += 1;
        if used == batch.len() {
            cancellation.check()?;
            sink.feed_overlaps(&batch)?;
            crate::work::aggregation_result(used as u64);
            used = 0;
        }
    }
    if used != 0 {
        cancellation.check()?;
        sink.feed_overlaps(&batch[..used])?;
        crate::work::aggregation_result(used as u64);
    }
    Ok(cells.len() as u64)
}

fn ordered_pair(left: usize, right: usize) -> (usize, usize) {
    (left.min(right), left.max(right))
}

fn pair_count(feeds: usize) -> Result<usize> {
    let count = (feeds as u128) * (feeds.saturating_sub(1) as u128) / 2;
    usize::try_from(count).map_err(|_| Error::BudgetExceeded("membership pair aggregation heap"))
}

fn add(left: Cardinality129, right: Cardinality129) -> Result<Cardinality129> {
    left.checked_add(right)
        .map_err(|_| Error::ArithmeticOverflow("membership aggregation addresses"))
}
