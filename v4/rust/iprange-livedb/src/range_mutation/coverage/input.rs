//! Streamed coverage input routing and ordered-prefix construction.

use crate::error::{Error, Result};
use crate::fixed_tree::{self, RetiringStore};
use crate::key::IpKey;
use crate::range_bulk;
use crate::range_tree::Record as Range;
use crate::{Cardinality129, ValueKind};

use super::{
    finish_private_untracked, touches, union_private_untracked_gap,
    union_private_untracked_general, UnionAssignmentInput, UnionState,
};

#[derive(Debug)]
pub(crate) struct UnionInput<K> {
    pending: Option<Range<K>>,
    pending_gap: Option<fixed_tree::Edge>,
    union: UnionState<K>,
    ordered: OrderedPrefix<K>,
    value_kind: ValueKind,
    assignment: UnionAssignmentInput<K>,
}

#[derive(Debug)]
enum OrderedPrefix<K> {
    Available,
    Building {
        builder: range_bulk::Builder<K>,
        addresses: Cardinality129,
    },
    Finished(Option<Cardinality129>),
}

impl<K: IpKey> UnionInput<K> {
    pub(crate) fn new(value_kind: ValueKind, max_heap_bytes: u64) -> Self {
        Self {
            pending: None,
            pending_gap: None,
            union: UnionState::default(),
            ordered: OrderedPrefix::Available,
            value_kind,
            assignment: UnionAssignmentInput::lazy(max_heap_bytes),
        }
    }

    pub(crate) fn is_general(&self) -> bool {
        self.union.is_general()
    }

    pub(crate) fn ordered_addresses(&self) -> Option<Cardinality129> {
        match self.ordered {
            OrderedPrefix::Finished(addresses) => addresses,
            OrderedPrefix::Available | OrderedPrefix::Building { .. } => None,
        }
    }

    fn start_general(&mut self) {
        self.union.start_general();
        self.assignment.enable();
    }

    fn enable_general(&mut self) {
        self.assignment.enable();
    }

    #[inline(always)]
    fn push_ordered<S: RetiringStore>(
        &mut self,
        store: &mut S,
        root: &mut u32,
        record_count: &mut u64,
        range: Range<K>,
    ) -> Result<Option<bool>> {
        if matches!(self.ordered, OrderedPrefix::Available) {
            if *root != 0 || *record_count != 0 {
                self.ordered = OrderedPrefix::Finished(None);
                return Ok(None);
            }
            let proven_ascending = self.pending.is_some_and(|next| {
                range.to < next.from
                    && (range.value != next.value || range.to.checked_next() != Some(next.from))
            });
            if !proven_ascending {
                self.ordered = OrderedPrefix::Finished(None);
                return Ok(None);
            }
            self.ordered = OrderedPrefix::Building {
                builder: range_bulk::Builder::new(store.target_txn(), self.value_kind),
                addresses: Cardinality129::ZERO,
            };
        }

        let OrderedPrefix::Building { builder, addresses } = &mut self.ordered else {
            return Ok(None);
        };
        if builder.try_push(store, range)? {
            *addresses = addresses
                .checked_add(range.from.inclusive_cardinality(range.to)?)
                .map_err(|_| Error::arithmetic_overflow("ordered range address count"))?;
            return Ok(Some(true));
        }

        self.finish_ordered(store, root, record_count)?;
        self.ordered = OrderedPrefix::Finished(None);
        self.start_general();
        Ok(None)
    }

    fn finish_ordered<S: RetiringStore>(
        &mut self,
        store: &mut S,
        root: &mut u32,
        record_count: &mut u64,
    ) -> Result<bool> {
        let ordered = std::mem::replace(&mut self.ordered, OrderedPrefix::Finished(None));
        let OrderedPrefix::Building {
            mut builder,
            addresses,
        } = ordered
        else {
            return Ok(false);
        };
        if *root != 0 || *record_count != 0 {
            return Err(Error::Corrupt(
                "ordered range prefix has an existing destination tree",
            ));
        }
        (*root, *record_count) = builder.finish_inline(store)?;
        self.ordered = OrderedPrefix::Finished(Some(addresses));
        Ok(true)
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
            &mut input.assignment,
        );
    }
    if from > to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    let Some((pending, known_gap)) = input.queue(Range { from, to, value }) else {
        return Ok(false);
    };
    let was_general = input.is_general();
    if let Some(changed) = input.push_ordered(store, root, record_count, pending)? {
        return Ok(changed);
    }
    let mut changed = apply_pending(store, root, record_count, pending, known_gap, input)?;
    if !was_general && input.is_general() {
        input.enable_general();
        if let Some((pending, _)) = input.take_pending() {
            changed |= union_private_untracked_general(
                store,
                root,
                record_count,
                pending,
                &mut input.assignment,
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
    let mut changed = false;
    if let Some((pending, known_gap)) = input.take_pending() {
        match input.push_ordered(store, root, record_count, pending)? {
            Some(ordered_changed) => changed |= ordered_changed,
            None => changed |= apply_pending(store, root, record_count, pending, known_gap, input)?,
        }
    }
    changed |= input.finish_ordered(store, root, record_count)?;
    finish_private_untracked(store, root, &mut input.union)?;
    input.assignment.release();
    Ok(changed)
}

#[inline(always)]
fn apply_pending<K: IpKey, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    pending: Range<K>,
    known_gap: Option<fixed_tree::Edge>,
    input: &mut UnionInput<K>,
) -> Result<bool> {
    if input.is_general() {
        union_private_untracked_general(store, root, record_count, pending, &mut input.assignment)
    } else {
        union_private_untracked_gap(
            store,
            root,
            record_count,
            pending,
            known_gap,
            &mut input.union,
        )
    }
}
