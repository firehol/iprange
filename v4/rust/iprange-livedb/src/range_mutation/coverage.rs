//! Coverage-union ingestion for unordered feed ranges.

use crate::error::{Error, Result};
use crate::fixed_tree::{self, RetiringStore, Store};
use crate::key::IpKey;
use crate::range_tree::{RangeCodec, Record as Range};

use super::{
    account_private_insert, insert, insert_private_gap, insert_private_rejected, EncodedRange,
    PrivateGap, RangeStore,
};

struct Untracked<'a, S>(&'a mut S);

impl<S: RetiringStore> Store for Untracked<'_, S> {
    type ReadPage<'a>
        = S::ReadPage<'a>
    where
        Self: 'a;
    type WritePage<'a>
        = S::WritePage<'a>
    where
        Self: 'a;

    fn target_txn(&self) -> u64 {
        self.0.target_txn()
    }

    fn page_limit(&self) -> u64 {
        self.0.page_limit()
    }

    fn inspect_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>) -> Result<T>,
    {
        self.0.inspect_page(page_number, inspect)
    }

    fn allocate(&mut self) -> Result<u32> {
        self.0.allocate()
    }

    fn update_page<'a, T, F>(&'a mut self, page_number: u32, update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>,
    {
        self.0.update_page(page_number, update)
    }

    fn copy_page<'a, T, F>(&'a mut self, source: u32, destination: u32, copy: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>, &mut Self::WritePage<'a>) -> Result<T>,
    {
        self.0.copy_page(source, destination, copy)
    }

    fn discard_private(&mut self, page_number: u32) -> Result<()> {
        self.0.discard_private(page_number)
    }
}

impl<S: RetiringStore> RetiringStore for Untracked<'_, S> {
    fn retire_pages(&mut self, pages: &[u32]) -> Result<()> {
        self.0.retire_pages(pages)
    }
}

impl<S: RetiringStore> RangeStore for Untracked<'_, S> {
    fn range_record_added(&mut self, _value: u32) -> Result<()> {
        Ok(())
    }

    fn range_record_removed(&mut self, _value: u32) -> Result<()> {
        Ok(())
    }
}

#[derive(Debug)]
pub(crate) struct UnionState<K> {
    last_from: Option<K>,
    order: UnionOrder,
    edge: Option<fixed_tree::PrivateEdge<K>>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum UnionOrder {
    Unknown,
    First,
    Last,
    General,
}

impl<K> Default for UnionState<K> {
    fn default() -> Self {
        Self {
            last_from: None,
            order: UnionOrder::Unknown,
            edge: None,
        }
    }
}

impl<K: IpKey> UnionState<K> {
    pub(crate) fn is_general(&self) -> bool {
        self.order == UnionOrder::General
    }

    fn plan(&self, from: K) -> (UnionOrder, Option<fixed_tree::Edge>) {
        let Some(previous) = self.last_from else {
            return (UnionOrder::Unknown, None);
        };
        match self.order {
            UnionOrder::Unknown if from < previous => {
                (UnionOrder::First, Some(fixed_tree::Edge::First))
            }
            UnionOrder::Unknown if from > previous => {
                (UnionOrder::Last, Some(fixed_tree::Edge::Last))
            }
            UnionOrder::Unknown => (UnionOrder::Unknown, None),
            UnionOrder::First if from <= previous => {
                (UnionOrder::First, Some(fixed_tree::Edge::First))
            }
            UnionOrder::Last if from >= previous => {
                (UnionOrder::Last, Some(fixed_tree::Edge::Last))
            }
            UnionOrder::General => (UnionOrder::General, None),
            UnionOrder::First | UnionOrder::Last => (UnionOrder::General, None),
        }
    }

    fn finish(&mut self, from: K, order: UnionOrder, edge: Option<fixed_tree::PrivateEdge<K>>) {
        self.last_from = Some(from);
        self.order = order;
        self.edge = if order == UnionOrder::General {
            None
        } else {
            edge
        };
    }
}

#[derive(Debug)]
pub(crate) struct UnionInput<K> {
    pending: Option<Range<K>>,
    pending_gap: Option<fixed_tree::Edge>,
    union: UnionState<K>,
}

impl<K> Default for UnionInput<K> {
    fn default() -> Self {
        Self {
            pending: None,
            pending_gap: None,
            union: UnionState::default(),
        }
    }
}

impl<K: IpKey> UnionInput<K> {
    pub(crate) fn is_general(&self) -> bool {
        self.union.is_general()
    }

    #[inline]
    fn queue(&mut self, incoming: Range<K>) -> Option<(Range<K>, Option<fixed_tree::Edge>)> {
        let Some(mut pending) = self.pending else {
            self.pending = Some(incoming);
            return None;
        };
        let touching = if incoming.from >= pending.from {
            touches(pending.to, incoming.from)
        } else {
            touches(incoming.to, pending.from)
        };
        if pending.value == incoming.value && touching {
            let extends_toward_previous = match self.pending_gap {
                Some(fixed_tree::Edge::First) => incoming.to > pending.to,
                Some(fixed_tree::Edge::Last) => incoming.from < pending.from,
                None => false,
            };
            if extends_toward_previous {
                self.pending_gap = None;
            }
            pending.from = pending.from.min(incoming.from);
            pending.to = pending.to.max(incoming.to);
            self.pending = Some(pending);
            crate::work::range_coalesced(1);
            return None;
        }
        let pending_gap = self.pending_gap;
        self.pending = Some(incoming);
        self.pending_gap = (!touching).then_some(if incoming.from > pending.from {
            fixed_tree::Edge::Last
        } else {
            fixed_tree::Edge::First
        });
        Some((pending, pending_gap))
    }

    fn take_pending(&mut self) -> Option<(Range<K>, Option<fixed_tree::Edge>)> {
        let pending = self.pending.take()?;
        let gap = self.pending_gap.take();
        Some((pending, gap))
    }
}

fn insert_private_edge<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: Range<K>,
    position: fixed_tree::PrivateEdge<K>,
    edge: fixed_tree::Edge,
    known_gap: Option<fixed_tree::Edge>,
) -> Result<fixed_tree::EdgeInsert<K, Range<K>>> {
    let encoded = EncodedRange::new(range)?;
    let mut gap = PrivateGap { range };
    let result = fixed_tree::insert_if_edge_gap::<RangeCodec<K>, S, _>(
        store,
        root,
        encoded.as_slice(),
        position,
        edge,
        known_gap == Some(edge),
        &mut gap,
    )?;
    if matches!(result, fixed_tree::EdgeInsert::Inserted(_)) {
        account_private_insert(store, record_count, range.value)?;
    }
    Ok(result)
}

fn union_private_untracked_gap<K: IpKey, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    incoming: Range<K>,
    known_gap: Option<fixed_tree::Edge>,
    state: &mut UnionState<K>,
) -> Result<bool> {
    if incoming.from > incoming.to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    apply_private(
        &mut Untracked(store),
        root,
        record_count,
        incoming,
        state,
        known_gap,
    )
}

fn union_private_untracked_general<K: IpKey, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    incoming: Range<K>,
) -> Result<bool> {
    if incoming.from > incoming.to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    apply_general(&mut Untracked(store), root, record_count, incoming)
}

pub(crate) fn finish_private_untracked<K: IpKey, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    state: &mut UnionState<K>,
) -> Result<()> {
    finish_private(&mut Untracked(store), root, state)
}

pub(super) fn finish_private<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    state: &mut UnionState<K>,
) -> Result<()> {
    match state.edge.as_mut() {
        Some(edge) => fixed_tree::flush_edge::<RangeCodec<K>, _>(store, root, edge),
        None => Ok(()),
    }
}

#[inline]
pub(crate) fn push_private_untracked<K: IpKey, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    from: K,
    to: K,
    value: u32,
    input: &mut UnionInput<K>,
) -> Result<bool> {
    if input.is_general() {
        debug_assert!(input.pending.is_none());
        return union_private_untracked_general(
            store,
            root,
            record_count,
            Range { from, to, value },
        );
    }
    if from > to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    let Some((pending, known_gap)) = input.queue(Range { from, to, value }) else {
        return Ok(false);
    };
    let was_general = input.is_general();
    let mut changed = union_private_untracked_gap(
        store,
        root,
        record_count,
        pending,
        known_gap,
        &mut input.union,
    )?;
    if !was_general && input.is_general() {
        if let Some((pending, known_gap)) = input.take_pending() {
            changed |= union_private_untracked_gap(
                store,
                root,
                record_count,
                pending,
                known_gap,
                &mut input.union,
            )?;
        }
    }
    Ok(changed)
}

pub(crate) fn finish_input_untracked<K: IpKey, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    input: &mut UnionInput<K>,
) -> Result<bool> {
    let changed = match input.take_pending() {
        Some((pending, known_gap)) => union_private_untracked_gap(
            store,
            root,
            record_count,
            pending,
            known_gap,
            &mut input.union,
        )?,
        None => false,
    };
    finish_private_untracked(store, root, &mut input.union)?;
    Ok(changed)
}

#[cfg(test)]
pub(super) fn union_private<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    from: K,
    to: K,
    value: u32,
    state: &mut UnionState<K>,
) -> Result<bool> {
    if from > to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    let incoming = Range { from, to, value };
    apply_private(store, root, record_count, incoming, state, None)
}

fn apply_private<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    incoming: Range<K>,
    state: &mut UnionState<K>,
    known_gap: Option<fixed_tree::Edge>,
) -> Result<bool> {
    if state.order == UnionOrder::General {
        return apply_general(store, root, record_count, incoming);
    }
    let (order, direction) = state.plan(incoming.from);
    if direction.is_none() {
        if let Some(edge) = state.edge.as_mut() {
            fixed_tree::flush_edge::<RangeCodec<K>, _>(store, root, edge)?;
        }
    }
    let cached =
        direction.and_then(|direction| state.edge.take().map(|position| (position, direction)));
    let was_empty = *root == 0;
    let rejected = if let Some((position, edge)) = cached {
        match insert_private_edge(
            store,
            root,
            record_count,
            incoming,
            position,
            edge,
            known_gap,
        )? {
            fixed_tree::EdgeInsert::Inserted(edge) => {
                state.finish(incoming.from, order, Some(edge));
                return Ok(true);
            }
            fixed_tree::EdgeInsert::General(rejected) => rejected,
        }
    } else {
        match insert_private_gap(store, root, record_count, incoming)? {
            fixed_tree::LocalInsert::Inserted => {
                let edge = was_empty.then(|| fixed_tree::root_edge(*root));
                state.finish(incoming.from, order, edge);
                return Ok(true);
            }
            fixed_tree::LocalInsert::General(rejected) => rejected,
        }
    };
    let (changed, position) = merge_rejected(store, root, record_count, incoming, rejected)?;
    state.finish(
        incoming.from,
        order,
        position.map(fixed_tree::PrivateEdge::consistent),
    );
    Ok(changed)
}

fn apply_general<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    incoming: Range<K>,
) -> Result<bool> {
    let rejected = match insert_private_gap(store, root, record_count, incoming)? {
        fixed_tree::LocalInsert::Inserted => return Ok(true),
        fixed_tree::LocalInsert::General(rejected) => rejected,
    };
    merge_rejected(store, root, record_count, incoming, rejected).map(|(changed, _)| changed)
}

fn merge_rejected<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    incoming: Range<K>,
    rejected: fixed_tree::LocalReject<Range<K>>,
) -> Result<(bool, Option<fixed_tree::PrivatePosition>)> {
    let Some(plan) = local_union_plan(&rejected, incoming) else {
        return union_run(store, root, record_count, incoming, rejected);
    };
    let LocalUnion::Replace {
        range,
        run,
        removed,
    } = plan
    else {
        return Ok((false, Some(rejected.into_position())));
    };
    let encoded = EncodedRange::new(range)?;
    fixed_tree::replace_local_run::<RangeCodec<K>, S, _>(
        store,
        root,
        &rejected,
        run,
        encoded.as_slice(),
    )?;
    *record_count = record_count
        .checked_sub(removed - 1)
        .ok_or_else(|| Error::arithmetic_overflow("range record count"))?;
    crate::work::range_coalesced(removed);
    crate::work::range_emitted(1);
    Ok((true, Some(rejected.into_position())))
}

enum LocalUnion<K> {
    NoChange,
    Replace {
        range: Range<K>,
        run: fixed_tree::LocalRun,
        removed: u64,
    },
}

fn local_union_plan<K: IpKey>(
    rejected: &fixed_tree::LocalReject<Range<K>>,
    incoming: Range<K>,
) -> Option<LocalUnion<K>> {
    let predecessor = rejected.predecessor().copied();
    if predecessor.is_some_and(|range| range.to >= incoming.to) {
        return Some(LocalUnion::NoChange);
    }
    let use_predecessor = predecessor.is_some_and(|range| touches(range.to, incoming.from));
    if !use_predecessor && predecessor.is_none() && !rejected.predecessor_complete() {
        return None;
    }

    let mut range = incoming;
    if let Some(predecessor) = predecessor.filter(|_| use_predecessor) {
        range.from = predecessor.from;
        range.to = range.to.max(predecessor.to);
    }

    let successor = rejected.successor().copied();
    let use_successor = successor.is_some_and(|next| touches(range.to, next.from));
    if use_successor {
        let successor = successor.expect("selected local successor exists");
        if range.to > successor.to {
            return None;
        }
        range.to = successor.to;
    } else if successor.is_none() && !rejected.successor_complete() {
        return None;
    }

    let (run, removed) = match (use_predecessor, use_successor) {
        (true, true) => (fixed_tree::LocalRun::Both, 2),
        (true, false) => (fixed_tree::LocalRun::Predecessor, 1),
        (false, true) => (fixed_tree::LocalRun::Successor, 1),
        (false, false) => return None,
    };
    Some(LocalUnion::Replace {
        range,
        run,
        removed,
    })
}

#[inline]
fn touches<K: IpKey>(left_to: K, right_from: K) -> bool {
    left_to >= right_from || left_to.checked_next() == Some(right_from)
}

fn union_run<K: IpKey, S: RangeStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    incoming: Range<K>,
    rejected: fixed_tree::LocalReject<Range<K>>,
) -> Result<(bool, Option<fixed_tree::PrivatePosition>)> {
    let predecessor = match rejected.predecessor().copied() {
        Some(predecessor) => Some(predecessor),
        None if rejected.predecessor_complete() => None,
        None => rejected.external_predecessor::<RangeCodec<K>, S>(store)?,
    };
    if predecessor.is_some_and(|range| range.to >= incoming.to) {
        return Ok((false, Some(rejected.into_position())));
    }

    let mut merged = incoming;
    let mut first = predecessor.filter(|range| touches(range.to, incoming.from));
    if let Some(range) = first {
        merged.from = range.from;
        merged.to = merged.to.max(range.to);
    } else {
        let successor = match rejected.successor().copied() {
            Some(successor) => Some(successor),
            None if rejected.successor_complete() => None,
            None => rejected.external_successor::<RangeCodec<K>, S>(store)?,
        };
        first = successor.filter(|range| touches(merged.to, range.from));
    }
    let Some(first) = first else {
        let position = insert_private_rejected(store, root, record_count, incoming, rejected)?;
        return Ok((true, position));
    };

    let mut next_key = first.from;
    let mut removed = 0u64;
    loop {
        let result =
            fixed_tree::remove_leaf_run::<RangeCodec<K>, S, _>(store, root, next_key, |range| {
                if range.value != incoming.value {
                    return Err(Error::Corrupt("constant-value tree contains another value"));
                }
                if !touches(merged.to, range.from) {
                    return Ok(false);
                }
                merged.from = merged.from.min(range.from);
                merged.to = merged.to.max(range.to);
                Ok(true)
            })?;
        removed = removed
            .checked_add(result.removed)
            .ok_or_else(|| Error::arithmetic_overflow("coverage removed ranges"))?;
        *record_count = record_count
            .checked_sub(result.removed)
            .ok_or_else(|| Error::arithmetic_overflow("range record count"))?;
        let Some(following) = result.following else {
            break;
        };
        if !touches(merged.to, following.leaf.from) {
            break;
        }
        next_key = following.key;
    }
    if removed == 0 {
        return Err(Error::Corrupt(
            "coverage run did not remove its first range",
        ));
    }
    insert::<K, S>(store, root, record_count, merged)?;
    crate::work::range_coalesced(removed);
    Ok((true, None))
}
