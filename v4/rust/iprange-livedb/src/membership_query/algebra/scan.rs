//! One ordered event sweep over all pinned algebra sources.

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::heap::Heap;
use crate::key::IpKey;

use super::super::selected::SelectedRanges;
use super::{AlgebraAccess, AlgebraInput};

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(super) struct ScanReport {
    pub(super) source_ranges: u64,
    pub(super) segments: u64,
}

pub(super) trait SegmentSink<K: IpKey> {
    fn enable_cache(&mut self, _heap: &mut Heap, _max_bytes: u64) -> Result<()> {
        Ok(())
    }

    fn segment(
        &mut self,
        from: K,
        to: K,
        present: &[u32],
        counts: &[u32],
        cancellation: &CancellationToken,
    ) -> Result<()>;
}

struct Range<K> {
    from: K,
    to: K,
}

struct SourceState<'a, K> {
    input: AlgebraInput<'a>,
    ranges: SelectedRanges<'a, K>,
    range: Option<Range<K>>,
    active: bool,
}

#[derive(Clone, Copy)]
enum EventKind {
    Start,
    End,
}

#[derive(Clone, Copy)]
struct Event<K> {
    at: K,
    source: u32,
    kind: EventKind,
}

struct Events<K> {
    values: Vec<Event<K>>,
    small: bool,
}

pub(super) struct GlobalState {
    counts: Vec<u32>,
    slots: Vec<u32>,
    present: Vec<u32>,
}

impl GlobalState {
    fn new(feeds: usize, heap: &mut Heap) -> Result<Self> {
        Ok(Self {
            counts: heap.filled(feeds, 0u32, "membership algebra scan heap")?,
            slots: heap.filled(feeds, 0u32, "membership algebra scan heap")?,
            present: heap.vector(feeds, "membership algebra scan heap")?,
        })
    }

    pub(super) fn counts(&self) -> &[u32] {
        &self.counts
    }

    pub(super) fn present(&self) -> &[u32] {
        &self.present
    }

    fn add(
        &mut self,
        present: &[u32],
        local_to_global: &[u32],
        cancellation: &CancellationToken,
    ) -> Result<()> {
        for (work, &local) in present.iter().enumerate() {
            if work & 4095 == 4095 {
                cancellation.check()?;
            }
            let global = local_to_global[local as usize] as usize;
            let count = self.counts[global];
            self.counts[global] = count
                .checked_add(1)
                .ok_or_else(|| Error::ArithmeticOverflow("global feed source count"))?;
            if count == 0 {
                let slot = self
                    .present
                    .len()
                    .checked_add(1)
                    .and_then(|value| u32::try_from(value).ok())
                    .ok_or_else(|| Error::BudgetExceeded("membership algebra feeds"))?;
                self.slots[global] = slot;
                self.present.push(global as u32);
            }
        }
        Ok(())
    }

    fn remove(
        &mut self,
        present: &[u32],
        local_to_global: &[u32],
        cancellation: &CancellationToken,
    ) -> Result<()> {
        for (work, &local) in present.iter().enumerate() {
            if work & 4095 == 4095 {
                cancellation.check()?;
            }
            let global = local_to_global[local as usize] as usize;
            let count = self.counts[global]
                .checked_sub(1)
                .ok_or_else(|| Error::Corrupt("global feed source count underflow"))?;
            self.counts[global] = count;
            if count == 0 {
                let slot = self.slots[global]
                    .checked_sub(1)
                    .ok_or_else(|| Error::Corrupt("global feed presence slot is absent"))?
                    as usize;
                let removed = self.present.swap_remove(slot);
                if removed as usize != global {
                    return Err(Error::Corrupt("global feed presence slot disagrees"));
                }
                self.slots[global] = 0;
                if slot < self.present.len() {
                    self.slots[self.present[slot] as usize] = (slot + 1) as u32;
                }
            }
        }
        Ok(())
    }
}

pub(super) fn run<K: IpKey, S: SegmentSink<K>>(
    algebra: &(impl AlgebraAccess + ?Sized),
    heap: &mut Heap,
    sink: &mut S,
    cancellation: &CancellationToken,
) -> Result<ScanReport> {
    cancellation.check()?;
    let source_count = algebra.state().input_count();
    crate::work::input_source_pass(source_count as u64);
    let (mut states, mut events) = initialize_sources(algebra, source_count, heap, cancellation)?;
    let mut global = GlobalState::new(algebra.state().names().len(), heap)?;
    let cache_share = heap.remaining() / (states.len() as u64 + 1);
    for state in &mut states {
        state.ranges.enable_cache(heap, cache_share)?;
    }
    sink.enable_cache(heap, cache_share)?;

    let mut report = ScanReport::default();
    let mut position = None;
    let mut event_work = 0u16;
    while let Some(event) = events.pop() {
        checkpoint_event(&mut event_work, cancellation)?;
        emit_before(position, event.at, &global, sink, &mut report, cancellation)?;
        let at = event.at;
        apply_boundary(
            event,
            &mut states,
            &mut events,
            &mut global,
            &mut event_work,
            cancellation,
        )?;
        position = Some(at);
    }
    emit_terminal(position, &states, &global, sink, &mut report, cancellation)?;
    finish_sources(&mut states, &mut report, cancellation)?;
    Ok(report)
}

fn initialize_sources<'a, K: IpKey>(
    algebra: &'a (impl AlgebraAccess + ?Sized),
    source_count: usize,
    heap: &mut Heap,
    cancellation: &CancellationToken,
) -> Result<(Vec<SourceState<'a, K>>, Events<K>)> {
    let mut states = heap.vector(source_count, "membership algebra scan heap")?;
    let mut events = Events {
        values: heap.vector(source_count, "membership algebra event heap")?,
        small: source_count <= 4,
    };
    for index in 0..source_count {
        let input = algebra.source(index)?;
        let mut state = SourceState {
            ranges: SelectedRanges::new(input.reader, input.scope, heap)?,
            input,
            range: None,
            active: false,
        };
        load_next(&mut state, cancellation)?;
        let source = u32::try_from(states.len())
            .map_err(|_| Error::BudgetExceeded("membership algebra sources"))?;
        if let Some(range) = &state.range {
            events.push(Event {
                at: range.from,
                source,
                kind: EventKind::Start,
            });
        }
        states.push(state);
    }
    Ok((states, events))
}

fn emit_before<K: IpKey, S: SegmentSink<K>>(
    from: Option<K>,
    boundary: K,
    global: &GlobalState,
    sink: &mut S,
    report: &mut ScanReport,
    cancellation: &CancellationToken,
) -> Result<()> {
    let Some(from) = from else {
        return Ok(());
    };
    if from >= boundary || global.present.is_empty() {
        return Ok(());
    }
    let to = boundary
        .checked_previous()
        .ok_or_else(|| Error::ArithmeticOverflow("membership algebra boundary"))?;
    sink.segment(from, to, global.present(), global.counts(), cancellation)?;
    report.segments = increment(report.segments, "membership algebra segments")?;
    Ok(())
}

fn apply_boundary<K: IpKey>(
    first: Event<K>,
    states: &mut [SourceState<'_, K>],
    events: &mut Events<K>,
    global: &mut GlobalState,
    event_work: &mut u16,
    cancellation: &CancellationToken,
) -> Result<()> {
    let at = first.at;
    apply_event(first, states, events, global, cancellation)?;
    while events.peek().is_some_and(|next| next.at == at) {
        checkpoint_event(event_work, cancellation)?;
        let same = events
            .pop()
            .ok_or_else(|| Error::Corrupt("membership algebra event disappeared"))?;
        apply_event(same, states, events, global, cancellation)?;
    }
    Ok(())
}

#[inline]
fn checkpoint_event(work: &mut u16, cancellation: &CancellationToken) -> Result<()> {
    *work += 1;
    if *work == 4096 {
        *work = 0;
        cancellation.check()?;
    }
    Ok(())
}

fn emit_terminal<K: IpKey, S: SegmentSink<K>>(
    from: Option<K>,
    states: &[SourceState<'_, K>],
    global: &GlobalState,
    sink: &mut S,
    report: &mut ScanReport,
    cancellation: &CancellationToken,
) -> Result<()> {
    let Some(from) = from else {
        return Ok(());
    };
    if global.present.is_empty() {
        return Ok(());
    }
    let to = states
        .iter()
        .find_map(|state| {
            state
                .active
                .then(|| state.range.as_ref().map(|range| range.to))
        })
        .flatten()
        .ok_or_else(|| Error::Corrupt("membership algebra has no terminal range"))?;
    if to.checked_next().is_some() {
        return Err(Error::Corrupt("membership algebra event queue ended early"));
    }
    sink.segment(from, to, global.present(), global.counts(), cancellation)?;
    report.segments = increment(report.segments, "membership algebra segments")?;
    Ok(())
}

fn finish_sources<K: IpKey>(
    states: &mut [SourceState<'_, K>],
    report: &mut ScanReport,
    cancellation: &CancellationToken,
) -> Result<()> {
    for (work, state) in states.iter_mut().enumerate() {
        if work & 4095 == 4095 {
            cancellation.check()?;
        }
        if state.active {
            let range = state
                .range
                .as_ref()
                .ok_or_else(|| Error::Corrupt("active membership algebra source has no range"))?;
            if range.to.checked_next().is_some() {
                return Err(Error::Corrupt("membership algebra source remained active"));
            }
            state.active = false;
            state.range = None;
            load_next(state, cancellation)?;
        }
        if state.range.is_some() || state.ranges.physical_count() != state.input.expected_ranges {
            return Err(Error::Corrupt("membership algebra range count disagrees"));
        }
        report.source_ranges = report
            .source_ranges
            .checked_add(state.ranges.physical_count())
            .ok_or_else(|| Error::ArithmeticOverflow("membership algebra source range count"))?;
    }
    Ok(())
}

fn apply_event<K: IpKey>(
    event: Event<K>,
    states: &mut [SourceState<'_, K>],
    events: &mut Events<K>,
    global: &mut GlobalState,
    cancellation: &CancellationToken,
) -> Result<()> {
    let state = states
        .get_mut(event.source as usize)
        .ok_or_else(|| Error::Corrupt("membership algebra event source is invalid"))?;
    let range = state
        .range
        .as_ref()
        .ok_or_else(|| Error::Corrupt("membership algebra event has no range"))?;
    match event.kind {
        EventKind::Start => {
            if state.active || range.from != event.at {
                return Err(Error::Corrupt("membership algebra start event disagrees"));
            }
            global.add(
                state.ranges.present(),
                state.input.local_to_global,
                cancellation,
            )?;
            state.active = true;
            if let Some(at) = range.to.checked_next() {
                events.push(Event {
                    at,
                    source: event.source,
                    kind: EventKind::End,
                });
            }
        }
        EventKind::End => {
            if !state.active || range.to.checked_next() != Some(event.at) {
                return Err(Error::Corrupt("membership algebra end event disagrees"));
            }
            global.remove(
                state.ranges.present(),
                state.input.local_to_global,
                cancellation,
            )?;
            state.active = false;
            state.range = None;
            load_next(state, cancellation)?;
            if let Some(range) = &state.range {
                events.push(Event {
                    at: range.from,
                    source: event.source,
                    kind: EventKind::Start,
                });
            }
        }
    }
    crate::work::join_advance(1);
    Ok(())
}

fn load_next<K: IpKey>(
    state: &mut SourceState<'_, K>,
    cancellation: &CancellationToken,
) -> Result<()> {
    state.range = state.ranges.next(cancellation)?.map(|range| Range {
        from: range.from,
        to: range.to,
    });
    if state.ranges.physical_count() > state.input.expected_ranges {
        return Err(Error::Corrupt("membership algebra range count disagrees"));
    }
    Ok(())
}

impl<K: IpKey> Events<K> {
    #[inline]
    fn peek(&self) -> Option<Event<K>> {
        if self.small {
            self.smallest().map(|index| self.values[index])
        } else {
            self.values.first().copied()
        }
    }

    #[inline]
    fn push(&mut self, event: Event<K>) {
        self.values.push(event);
        if self.small {
            return;
        }
        let mut child = self.values.len() - 1;
        while child != 0 {
            let parent = (child - 1) / 2;
            if !before(self.values[child], self.values[parent]) {
                break;
            }
            self.values.swap(child, parent);
            child = parent;
        }
    }

    #[inline]
    fn pop(&mut self) -> Option<Event<K>> {
        if self.small {
            let index = self.smallest()?;
            return Some(self.values.swap_remove(index));
        }
        let root = *self.values.first()?;
        let last = self.values.pop()?;
        if !self.values.is_empty() {
            self.values[0] = last;
            let mut parent = 0usize;
            loop {
                let left = parent * 2 + 1;
                if left >= self.values.len() {
                    break;
                }
                let right = left + 1;
                let child =
                    if right < self.values.len() && before(self.values[right], self.values[left]) {
                        right
                    } else {
                        left
                    };
                if !before(self.values[child], self.values[parent]) {
                    break;
                }
                self.values.swap(parent, child);
                parent = child;
            }
        }
        Some(root)
    }

    fn smallest(&self) -> Option<usize> {
        let mut smallest = 0usize;
        let first = *self.values.first()?;
        let mut selected = first;
        for (index, &candidate) in self.values[1..].iter().enumerate() {
            if before(candidate, selected) {
                smallest = index + 1;
                selected = candidate;
            }
        }
        Some(smallest)
    }
}

fn before<K: IpKey>(left: Event<K>, right: Event<K>) -> bool {
    left.at < right.at
        || (left.at == right.at
            && (left.source, kind_order(left.kind)) < (right.source, kind_order(right.kind)))
}

fn kind_order(kind: EventKind) -> u8 {
    match kind {
        EventKind::End => 0,
        EventKind::Start => 1,
    }
}

fn increment(value: u64, context: &'static str) -> Result<u64> {
    value
        .checked_add(1)
        .ok_or_else(|| Error::ArithmeticOverflow(context))
}
