//! Ordered membership/direct join with bounded result interning.

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::AddressFamily;
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::range_cursor::DirectRange;

use super::{DirectJoinBudget, DirectJoinCell, DirectJoinReport, DirectJoinSink, Source};
use crate::heap::Heap;
use crate::membership_query::selected::{SelectedRange, SelectedRanges};
use crate::membership_query::MembershipScopeView;

const RESULT_BATCH: usize = 32;

#[derive(Clone, Copy, Default)]
struct Slot {
    feed: u32,
    direct: u64,
    cell_plus_one: usize,
}

#[derive(Clone, Copy, Default)]
struct Cell {
    feed: u32,
    direct: u64,
    addresses: Cardinality129,
}

struct Table {
    cells: Vec<Cell>,
    slots: Vec<Slot>,
    mask: usize,
    limit: usize,
}

#[derive(Default)]
struct Stats {
    direct_ranges: u64,
    segments: u64,
    selected: Cardinality129,
    mapped: Cardinality129,
    unmapped: Cardinality129,
}

struct Sweep<'a, 'db, K, N> {
    membership: &'a mut SelectedRanges<'db, K>,
    next_direct: N,
    left: Option<SelectedRange<K>>,
    right: Option<DirectRange<K>>,
    accumulator: Accumulator<'a>,
}

struct Accumulator<'a> {
    table: &'a mut Table,
    stats: Stats,
}

pub(super) fn run<S: DirectJoinSink>(
    scope: MembershipScopeView<'_>,
    source: Source<'_>,
    budget: DirectJoinBudget,
    sink: &mut S,
    cancellation: &CancellationToken,
) -> Result<DirectJoinReport> {
    cancellation.check()?;
    if scope.state.data.entries.is_empty() {
        return Ok(DirectJoinReport {
            membership_range_count: 0,
            direct_ranges_visited: 0,
            joined_segment_count: 0,
            selected_addresses: Cardinality129::ZERO,
            mapped_addresses: Cardinality129::ZERO,
            unmapped_addresses: Cardinality129::ZERO,
            result_cell_count: 0,
        });
    }
    let mut heap = scope.state.data.operation_heap()?;
    let mut table = Table::new(budget, &mut heap)?;
    let stats = match scope.state.address_family {
        AddressFamily::Ipv4 => {
            let mut membership =
                SelectedRanges::<Ipv4Key>::new(scope.reader, &scope.state.data, &mut heap)?;
            let cache_bytes = heap.remaining();
            membership.enable_cache(&mut heap, cache_bytes)?;
            let mut direct = source
                .core
                .read()
                .direct_cursor_v4(crate::RangeDirection::Forward)?;
            let stats = join::<Ipv4Key, _>(&mut membership, &mut table, cancellation, || {
                direct.next_range()
            })?;
            require_range_count(&membership, scope.state.range_record_count)?;
            stats
        }
        AddressFamily::Ipv6 => {
            let mut membership =
                SelectedRanges::<Ipv6Key>::new(scope.reader, &scope.state.data, &mut heap)?;
            let cache_bytes = heap.remaining();
            membership.enable_cache(&mut heap, cache_bytes)?;
            let mut direct = source
                .core
                .read()
                .direct_cursor_v6(crate::RangeDirection::Forward)?;
            let stats = join::<Ipv6Key, _>(&mut membership, &mut table, cancellation, || {
                direct.next_range()
            })?;
            require_range_count(&membership, scope.state.range_record_count)?;
            stats
        }
    };
    table.emit(scope, sink, cancellation)?;
    Ok(DirectJoinReport {
        membership_range_count: scope.state.range_record_count,
        direct_ranges_visited: stats.direct_ranges,
        joined_segment_count: stats.segments,
        selected_addresses: stats.selected,
        mapped_addresses: stats.mapped,
        unmapped_addresses: stats.unmapped,
        result_cell_count: table.cells.len() as u64,
    })
}

fn join<K, N>(
    membership: &mut SelectedRanges<'_, K>,
    table: &mut Table,
    cancellation: &CancellationToken,
    next_direct: N,
) -> Result<Stats>
where
    K: IpKey,
    N: FnMut() -> Result<Option<DirectRange<K>>>,
{
    Sweep::new(membership, table, next_direct, cancellation)?.run(cancellation)
}

impl<'a, 'db, K, N> Sweep<'a, 'db, K, N>
where
    K: IpKey,
    N: FnMut() -> Result<Option<DirectRange<K>>>,
{
    fn new(
        membership: &'a mut SelectedRanges<'db, K>,
        table: &'a mut Table,
        next_direct: N,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        crate::work::input_source_pass(2);
        let left = membership.next(cancellation)?;
        let mut sweep = Self {
            membership,
            next_direct,
            left,
            right: None,
            accumulator: Accumulator {
                table,
                stats: Stats::default(),
            },
        };
        sweep.advance_right(cancellation)?;
        Ok(sweep)
    }

    fn run(mut self, cancellation: &CancellationToken) -> Result<Stats> {
        while self.left.is_some() {
            self.step(cancellation)?;
        }
        cancellation.check()?;
        Ok(self.accumulator.stats)
    }

    fn step(&mut self, cancellation: &CancellationToken) -> Result<()> {
        let mut current = self
            .left
            .ok_or_else(|| Error::Corrupt("direct join lost its membership range"))?;
        while self.right.is_some_and(|range| range.to < current.from) {
            self.advance_right(cancellation)?;
        }
        let Some(mut provider) = self.right else {
            self.accumulator.consume(
                self.membership.present(),
                current.from,
                current.to,
                None,
                cancellation,
            )?;
            self.advance_left(cancellation)?;
            return Ok(());
        };
        if current.to < provider.from {
            self.accumulator.consume(
                self.membership.present(),
                current.from,
                current.to,
                None,
                cancellation,
            )?;
            self.advance_left(cancellation)?;
            return Ok(());
        }
        if current.from < provider.from {
            let end = previous(provider.from)?;
            self.accumulator.consume(
                self.membership.present(),
                current.from,
                end,
                None,
                cancellation,
            )?;
            current.from = provider.from;
            self.left = Some(current);
            return Ok(());
        }

        let from = current.from.max(provider.from);
        let to = current.to.min(provider.to);
        self.accumulator.consume(
            self.membership.present(),
            from,
            to,
            Some(provider.value),
            cancellation,
        )?;
        if current.to == to {
            self.advance_left(cancellation)?;
        } else {
            current.from = next(to)?;
            self.left = Some(current);
        }
        if provider.to == to {
            self.advance_right(cancellation)?;
        } else {
            provider.from = next(to)?;
            self.right = Some(provider);
        }
        Ok(())
    }

    fn advance_left(&mut self, cancellation: &CancellationToken) -> Result<()> {
        self.left = self.membership.next(cancellation)?;
        Ok(())
    }

    fn advance_right(&mut self, cancellation: &CancellationToken) -> Result<()> {
        if self.accumulator.stats.direct_ranges & 4095 == 4095 {
            cancellation.check()?;
        }
        self.right = (self.next_direct)()?;
        if self.right.is_some() {
            self.accumulator.stats.direct_ranges = increment(
                self.accumulator.stats.direct_ranges,
                "direct join range count",
            )?;
        }
        Ok(())
    }
}

impl Accumulator<'_> {
    fn consume<K: IpKey>(
        &mut self,
        present: &[u32],
        from: K,
        to: K,
        direct: Option<u32>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        if present.is_empty() {
            return Ok(());
        }
        let count = from.inclusive_cardinality(to)?;
        self.stats.segments = increment(self.stats.segments, "direct join segment count")?;
        self.stats.selected = add(self.stats.selected, count)?;
        if direct.is_some() {
            self.stats.mapped = add(self.stats.mapped, count)?;
        } else {
            self.stats.unmapped = add(self.stats.unmapped, count)?;
        }
        let encoded = direct.map_or(0, |value| u64::from(value) + 1);
        for (offset, &feed) in present.iter().enumerate() {
            if offset & 4095 == 4095 {
                cancellation.check()?;
            }
            self.table.add(feed, encoded, count, cancellation)?;
            crate::work::aggregation_contribution(1);
        }
        crate::work::join_advance(1);
        Ok(())
    }
}

fn require_range_count<K: IpKey>(ranges: &SelectedRanges<'_, K>, expected: u64) -> Result<()> {
    if ranges.physical_count() == expected {
        Ok(())
    } else {
        Err(Error::Corrupt("membership join range count disagrees"))
    }
}

impl Table {
    fn new(budget: DirectJoinBudget, heap: &mut Heap) -> Result<Self> {
        let limit = usize::try_from(budget.max_result_cells)
            .map_err(|_| Error::BudgetExceeded("direct join result cells"))?;
        let cells = heap.vector(limit, "direct join result heap")?;
        if limit == 0 {
            return Ok(Self {
                cells,
                slots: Vec::new(),
                mask: 0,
                limit,
            });
        }
        let slots_len = limit
            .checked_mul(2)
            .and_then(|value| value.checked_next_power_of_two())
            .ok_or_else(|| Error::BudgetExceeded("direct join result heap"))?;
        Ok(Self {
            cells,
            slots: heap.filled(slots_len, Slot::default(), "direct join result heap")?,
            mask: slots_len - 1,
            limit,
        })
    }

    fn add(
        &mut self,
        feed: u32,
        direct: u64,
        count: Cardinality129,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        if self.limit == 0 {
            return Err(Error::BudgetExceeded("direct join result cells"));
        }
        let mut slot = hash(feed, direct) & self.mask;
        let mut probes = 0usize;
        loop {
            if probes & 4095 == 4095 {
                cancellation.check()?;
            }
            let current = self.slots[slot];
            if current.cell_plus_one == 0 {
                if self.cells.len() == self.limit {
                    return Err(Error::BudgetExceeded("direct join result cells"));
                }
                let cell = self.cells.len();
                self.cells.push(Cell {
                    feed,
                    direct,
                    addresses: count,
                });
                self.slots[slot] = Slot {
                    feed,
                    direct,
                    cell_plus_one: cell + 1,
                };
                return Ok(());
            }
            if current.feed == feed && current.direct == direct {
                let cell = &mut self.cells[current.cell_plus_one - 1];
                cell.addresses = add(cell.addresses, count)?;
                return Ok(());
            }
            slot = (slot + 1) & self.mask;
            probes += 1;
        }
    }

    fn emit<S: DirectJoinSink>(
        &mut self,
        scope: MembershipScopeView<'_>,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        if self.cells.is_empty() {
            return Ok(());
        }
        self.cells
            .sort_unstable_by_key(|cell| (cell.feed, cell.direct));
        let first = self.cells[0];
        let empty = output_cell(scope, first);
        let mut batch = [empty; RESULT_BATCH];
        let mut used = 0usize;
        for &cell in &self.cells {
            batch[used] = output_cell(scope, cell);
            used += 1;
            if used == batch.len() {
                cancellation.check()?;
                sink.direct_join_cells(&batch)?;
                crate::work::aggregation_result(used as u64);
                used = 0;
            }
        }
        if used != 0 {
            cancellation.check()?;
            sink.direct_join_cells(&batch[..used])?;
            crate::work::aggregation_result(used as u64);
        }
        Ok(())
    }
}

fn output_cell(scope: MembershipScopeView<'_>, cell: Cell) -> DirectJoinCell {
    DirectJoinCell {
        feed: scope.state.data.entries[cell.feed as usize].name,
        direct_value: if cell.direct == 0 {
            None
        } else {
            Some((cell.direct - 1) as u32)
        },
        addresses: cell.addresses,
    }
}

fn hash(feed: u32, direct: u64) -> usize {
    let mut value = direct ^ u64::from(feed).wrapping_mul(0x9e37_79b9_7f4a_7c15);
    value ^= value >> 30;
    value = value.wrapping_mul(0xbf58_476d_1ce4_e5b9);
    value ^= value >> 27;
    value as usize
}

fn increment(value: u64, context: &'static str) -> Result<u64> {
    value
        .checked_add(1)
        .ok_or_else(|| Error::ArithmeticOverflow(context))
}

fn add(left: Cardinality129, right: Cardinality129) -> Result<Cardinality129> {
    left.checked_add(right)
        .map_err(|_| Error::ArithmeticOverflow("direct join addresses"))
}

fn previous<K: IpKey>(value: K) -> Result<K> {
    value
        .checked_previous()
        .ok_or_else(|| Error::ArithmeticOverflow("direct join boundary"))
}

fn next<K: IpKey>(value: K) -> Result<K> {
    value
        .checked_next()
        .ok_or_else(|| Error::ArithmeticOverflow("direct join boundary"))
}
