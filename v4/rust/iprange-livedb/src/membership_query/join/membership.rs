//! Ordered cross-membership join and per-side uncovered coverage.

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::AddressFamily;
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};

use super::{
    MembershipCrossCell, MembershipJoinReport, MembershipJoinSink, UncoveredFeed, UncoveredSide,
};
use crate::membership_query::selected::{SelectedRange, SelectedRanges};
use crate::membership_query::MembershipScopeView;

const RESULT_BATCH: usize = 32;

#[derive(Default)]
struct Stats {
    segments: u64,
    left: Cardinality129,
    right: Cardinality129,
    overlap: Cardinality129,
    left_uncovered: Cardinality129,
    right_uncovered: Cardinality129,
}

struct Results {
    cross: Vec<Cardinality129>,
    left_uncovered: Vec<Cardinality129>,
    right_uncovered: Vec<Cardinality129>,
}

struct Sweep<'a, 'left, 'right, K> {
    left_ranges: &'a mut SelectedRanges<'left, K>,
    right_ranges: &'a mut SelectedRanges<'right, K>,
    left: Option<SelectedRange<K>>,
    right: Option<SelectedRange<K>>,
    accumulator: Accumulator<'a>,
}

struct Accumulator<'a> {
    right_width: usize,
    results: &'a mut Results,
    stats: Stats,
}

#[derive(Clone, Copy)]
enum Coverage {
    Left,
    Right,
    Both,
}

#[derive(Clone, Copy)]
struct Segment<K> {
    from: K,
    to: K,
    coverage: Coverage,
}

pub(super) fn run<S: MembershipJoinSink>(
    left: MembershipScopeView<'_>,
    right: MembershipScopeView<'_>,
    sink: &mut S,
    cancellation: &CancellationToken,
) -> Result<MembershipJoinReport> {
    cancellation.check()?;
    let mut heap = left.state.data.operation_heap()?;
    let cross_count = left
        .state
        .data
        .entries
        .len()
        .checked_mul(right.state.data.entries.len())
        .ok_or_else(|| Error::BudgetExceeded("membership join result heap"))?;
    let mut results = Results {
        cross: heap.filled(
            cross_count,
            Cardinality129::ZERO,
            "membership join result heap",
        )?,
        left_uncovered: heap.filled(
            left.state.data.entries.len(),
            Cardinality129::ZERO,
            "membership join result heap",
        )?,
        right_uncovered: heap.filled(
            right.state.data.entries.len(),
            Cardinality129::ZERO,
            "membership join result heap",
        )?,
    };
    let stats = match left.state.address_family {
        AddressFamily::Ipv4 => {
            join_family::<Ipv4Key>(left, right, &mut results, &mut heap, cancellation)?
        }
        AddressFamily::Ipv6 => {
            join_family::<Ipv6Key>(left, right, &mut results, &mut heap, cancellation)?
        }
    };
    emit_cross(left, right, &results.cross, sink, cancellation)?;
    emit_uncovered(left, right, &results, sink, cancellation)?;
    Ok(MembershipJoinReport {
        left_range_count: left.state.range_record_count,
        right_range_count: right.state.range_record_count,
        joined_segment_count: stats.segments,
        left_addresses: stats.left,
        right_addresses: stats.right,
        overlap_addresses: stats.overlap,
        left_uncovered_addresses: stats.left_uncovered,
        right_uncovered_addresses: stats.right_uncovered,
        cross_result_count: cross_count as u64,
        uncovered_result_count: (left.state.data.entries.len() + right.state.data.entries.len())
            as u64,
    })
}

fn join_family<K: IpKey>(
    left: MembershipScopeView<'_>,
    right: MembershipScopeView<'_>,
    results: &mut Results,
    heap: &mut crate::heap::Heap,
    cancellation: &CancellationToken,
) -> Result<Stats> {
    let mut left_ranges = SelectedRanges::<K>::new(left.reader, &left.state.data, heap)?;
    let mut right_ranges = SelectedRanges::<K>::new(right.reader, &right.state.data, heap)?;
    let cache_bytes = heap.remaining() / 2;
    left_ranges.enable_cache(heap, cache_bytes)?;
    right_ranges.enable_cache(heap, cache_bytes)?;
    let stats = join(
        right.state.data.entries.len(),
        &mut left_ranges,
        &mut right_ranges,
        results,
        cancellation,
    )?;
    if left_ranges.physical_count() != left.state.range_record_count
        || right_ranges.physical_count() != right.state.range_record_count
    {
        return Err(Error::Corrupt("membership join range count disagrees"));
    }
    Ok(stats)
}

fn join<K: IpKey>(
    right_width: usize,
    left_ranges: &mut SelectedRanges<'_, K>,
    right_ranges: &mut SelectedRanges<'_, K>,
    results: &mut Results,
    cancellation: &CancellationToken,
) -> Result<Stats> {
    Sweep::new(
        right_width,
        left_ranges,
        right_ranges,
        results,
        cancellation,
    )?
    .run(cancellation)
}

impl<'a, 'left, 'right, K: IpKey> Sweep<'a, 'left, 'right, K> {
    fn new(
        right_width: usize,
        left_ranges: &'a mut SelectedRanges<'left, K>,
        right_ranges: &'a mut SelectedRanges<'right, K>,
        results: &'a mut Results,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        crate::work::input_source_pass(2);
        let left = left_ranges.next(cancellation)?;
        let right = right_ranges.next(cancellation)?;
        Ok(Self {
            left_ranges,
            right_ranges,
            left,
            right,
            accumulator: Accumulator {
                right_width,
                results,
                stats: Stats::default(),
            },
        })
    }

    fn run(mut self, cancellation: &CancellationToken) -> Result<Stats> {
        while self.left.is_some() || self.right.is_some() {
            self.step(cancellation)?;
        }
        cancellation.check()?;
        Ok(self.accumulator.stats)
    }

    fn step(&mut self, cancellation: &CancellationToken) -> Result<()> {
        match (self.left, self.right) {
            (Some(left), None) => self.consume_left(left, left.to, cancellation)?,
            (None, Some(right)) => self.consume_right(right, right.to, cancellation)?,
            (Some(left), Some(right)) if left.to < right.from => {
                self.consume_left(left, left.to, cancellation)?
            }
            (Some(left), Some(right)) if right.to < left.from => {
                self.consume_right(right, right.to, cancellation)?
            }
            (Some(left), Some(right)) if left.from < right.from => {
                self.consume_left(left, previous(right.from)?, cancellation)?
            }
            (Some(left), Some(right)) if right.from < left.from => {
                self.consume_right(right, previous(left.from)?, cancellation)?
            }
            (Some(left), Some(right)) => self.consume_overlap(left, right, cancellation)?,
            (None, None) => {}
        }
        Ok(())
    }

    fn consume_left(
        &mut self,
        mut range: SelectedRange<K>,
        to: K,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.accumulator.consume(
            self.left_ranges.present(),
            self.right_ranges.present(),
            Segment {
                from: range.from,
                to,
                coverage: Coverage::Left,
            },
            cancellation,
        )?;
        if range.to == to {
            self.left = self.left_ranges.next(cancellation)?;
        } else {
            range.from = next(to)?;
            self.left = Some(range);
        }
        Ok(())
    }

    fn consume_right(
        &mut self,
        mut range: SelectedRange<K>,
        to: K,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.accumulator.consume(
            self.left_ranges.present(),
            self.right_ranges.present(),
            Segment {
                from: range.from,
                to,
                coverage: Coverage::Right,
            },
            cancellation,
        )?;
        if range.to == to {
            self.right = self.right_ranges.next(cancellation)?;
        } else {
            range.from = next(to)?;
            self.right = Some(range);
        }
        Ok(())
    }

    fn consume_overlap(
        &mut self,
        mut left: SelectedRange<K>,
        mut right: SelectedRange<K>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let to = left.to.min(right.to);
        self.accumulator.consume(
            self.left_ranges.present(),
            self.right_ranges.present(),
            Segment {
                from: left.from,
                to,
                coverage: Coverage::Both,
            },
            cancellation,
        )?;
        if left.to == to {
            self.left = self.left_ranges.next(cancellation)?;
        } else {
            left.from = next(to)?;
            self.left = Some(left);
        }
        if right.to == to {
            self.right = self.right_ranges.next(cancellation)?;
        } else {
            right.from = next(to)?;
            self.right = Some(right);
        }
        Ok(())
    }
}

impl Accumulator<'_> {
    fn consume<K: IpKey>(
        &mut self,
        left: &[u32],
        right: &[u32],
        segment: Segment<K>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let (left_present, right_present) = match segment.coverage {
            Coverage::Left => (left, &[][..]),
            Coverage::Right => (&[][..], right),
            Coverage::Both => (left, right),
        };
        if left_present.is_empty() && right_present.is_empty() {
            return Ok(());
        }
        let count = segment.from.inclusive_cardinality(segment.to)?;
        self.stats.segments = increment(self.stats.segments, "membership join segment count")?;
        if !left_present.is_empty() {
            self.stats.left = add(self.stats.left, count)?;
        }
        if !right_present.is_empty() {
            self.stats.right = add(self.stats.right, count)?;
        }
        match (left_present.is_empty(), right_present.is_empty()) {
            (false, false) => self.add_cross(left_present, right_present, count, cancellation)?,
            (false, true) => self.add_left_uncovered(left_present, count, cancellation)?,
            (true, false) => self.add_right_uncovered(right_present, count, cancellation)?,
            (true, true) => unreachable!(),
        }
        crate::work::join_advance(1);
        Ok(())
    }

    fn add_cross(
        &mut self,
        left_present: &[u32],
        right_present: &[u32],
        count: Cardinality129,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.stats.overlap = add(self.stats.overlap, count)?;
        let mut work = 0usize;
        for &left in left_present {
            for &right in right_present {
                if work & 4095 == 4095 {
                    cancellation.check()?;
                }
                let index = (left as usize)
                    .checked_mul(self.right_width)
                    .and_then(|base| base.checked_add(right as usize))
                    .ok_or_else(|| Error::ArithmeticOverflow("membership join result index"))?;
                self.results.cross[index] = add(self.results.cross[index], count)?;
                crate::work::aggregation_contribution(1);
                work += 1;
            }
        }
        Ok(())
    }

    fn add_left_uncovered(
        &mut self,
        present: &[u32],
        count: Cardinality129,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.stats.left_uncovered = add(self.stats.left_uncovered, count)?;
        add_uncovered(
            &mut self.results.left_uncovered,
            present,
            count,
            cancellation,
        )
    }

    fn add_right_uncovered(
        &mut self,
        present: &[u32],
        count: Cardinality129,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.stats.right_uncovered = add(self.stats.right_uncovered, count)?;
        add_uncovered(
            &mut self.results.right_uncovered,
            present,
            count,
            cancellation,
        )
    }
}

fn add_uncovered(
    output: &mut [Cardinality129],
    present: &[u32],
    count: Cardinality129,
    cancellation: &CancellationToken,
) -> Result<()> {
    for (work, &feed) in present.iter().enumerate() {
        if work & 4095 == 4095 {
            cancellation.check()?;
        }
        let value = &mut output[feed as usize];
        *value = add(*value, count)?;
        crate::work::aggregation_contribution(1);
    }
    Ok(())
}

fn emit_cross<S: MembershipJoinSink>(
    left: MembershipScopeView<'_>,
    right: MembershipScopeView<'_>,
    totals: &[Cardinality129],
    sink: &mut S,
    cancellation: &CancellationToken,
) -> Result<()> {
    if totals.is_empty() {
        return Ok(());
    }
    let empty = MembershipCrossCell {
        left: left.state.data.entries[0].name,
        right: right.state.data.entries[0].name,
        addresses: Cardinality129::ZERO,
    };
    let mut batch = [empty; RESULT_BATCH];
    let mut used = 0usize;
    let mut index = 0usize;
    for left_feed in &left.state.data.entries {
        for right_feed in &right.state.data.entries {
            batch[used] = MembershipCrossCell {
                left: left_feed.name,
                right: right_feed.name,
                addresses: totals[index],
            };
            used += 1;
            index += 1;
            if used == batch.len() {
                cancellation.check()?;
                sink.membership_cross_cells(&batch)?;
                crate::work::aggregation_result(used as u64);
                used = 0;
            }
        }
    }
    if used != 0 {
        cancellation.check()?;
        sink.membership_cross_cells(&batch[..used])?;
        crate::work::aggregation_result(used as u64);
    }
    Ok(())
}

fn emit_uncovered<S: MembershipJoinSink>(
    left: MembershipScopeView<'_>,
    right: MembershipScopeView<'_>,
    results: &Results,
    sink: &mut S,
    cancellation: &CancellationToken,
) -> Result<()> {
    let first = left
        .state
        .data
        .entries
        .first()
        .map(|entry| (UncoveredSide::Left, entry.name))
        .or_else(|| {
            right
                .state
                .data
                .entries
                .first()
                .map(|entry| (UncoveredSide::Right, entry.name))
        });
    let Some((side, feed)) = first else {
        return Ok(());
    };
    let empty = UncoveredFeed {
        side,
        feed,
        addresses: Cardinality129::ZERO,
    };
    let mut batch = [empty; RESULT_BATCH];
    let mut used = 0usize;
    for (entry, &addresses) in left.state.data.entries.iter().zip(&results.left_uncovered) {
        batch[used] = UncoveredFeed {
            side: UncoveredSide::Left,
            feed: entry.name,
            addresses,
        };
        used += 1;
        if used == batch.len() {
            cancellation.check()?;
            sink.uncovered_feeds(&batch)?;
            crate::work::aggregation_result(used as u64);
            used = 0;
        }
    }
    for (entry, &addresses) in right
        .state
        .data
        .entries
        .iter()
        .zip(&results.right_uncovered)
    {
        batch[used] = UncoveredFeed {
            side: UncoveredSide::Right,
            feed: entry.name,
            addresses,
        };
        used += 1;
        if used == batch.len() {
            cancellation.check()?;
            sink.uncovered_feeds(&batch)?;
            crate::work::aggregation_result(used as u64);
            used = 0;
        }
    }
    if used != 0 {
        cancellation.check()?;
        sink.uncovered_feeds(&batch[..used])?;
        crate::work::aggregation_result(used as u64);
    }
    Ok(())
}

fn increment(value: u64, context: &'static str) -> Result<u64> {
    value
        .checked_add(1)
        .ok_or_else(|| Error::ArithmeticOverflow(context))
}

fn add(left: Cardinality129, right: Cardinality129) -> Result<Cardinality129> {
    left.checked_add(right)
        .map_err(|_| Error::ArithmeticOverflow("membership join addresses"))
}

fn previous<K: IpKey>(value: K) -> Result<K> {
    value
        .checked_previous()
        .ok_or_else(|| Error::ArithmeticOverflow("membership join boundary"))
}

fn next<K: IpKey>(value: K) -> Result<K> {
    value
        .checked_next()
        .ok_or_else(|| Error::ArithmeticOverflow("membership join boundary"))
}
