//! One authoritative ordered old/input merge into mapped range pages.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::ValueKind;
use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::range_bulk::{Builder, Record};
use crate::range_cursor::DirectRange;
use crate::range_mutation;
use crate::range_store_cursor::Cursor;
use crate::workflow::Comparison;

use super::DraftStore;

#[derive(Clone, Copy)]
pub(crate) struct Incoming<K, V> {
    pub(crate) from: K,
    pub(crate) to: K,
    pub(crate) value: V,
}

pub(crate) trait Policy<K: IpKey, V: Copy> {
    type Output;

    const PRESERVE_WITHOUT_INPUT: bool = false;

    fn transform(
        &mut self,
        store: &mut DraftStore<'_>,
        old: Option<u32>,
        incoming: Option<V>,
    ) -> Result<Option<u32>>;

    fn observe(
        &mut self,
        from: K,
        to: K,
        old: Option<u32>,
        incoming: Option<V>,
        new: Option<u32>,
    ) -> Result<()>;

    fn finish(self) -> Result<Self::Output>;
}

pub(crate) struct Finished<T> {
    pub(crate) output: T,
}

pub(crate) struct OrderedMerge<K, V, P> {
    old_cursor: Cursor<K>,
    old: Option<DirectRange<K>>,
    old_accounted: bool,
    previous_input_end: Option<K>,
    output: Output<K>,
    policy: P,
    base_root: u32,
    base_count: u64,
    input_seen: bool,
    value: std::marker::PhantomData<V>,
}

impl<K: IpKey, V: Copy, P: Policy<K, V>> OrderedMerge<K, V, P> {
    pub(crate) fn new(
        store: &mut DraftStore<'_>,
        base: &Bootstrap,
        policy: P,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        cancellation.check()?;
        let mut old_cursor = Cursor::new(store, &base.meta, false)?;
        let old = old_cursor.next(store)?;
        Ok(Self {
            old_cursor,
            old,
            old_accounted: false,
            previous_input_end: None,
            output: Output::new(store.draft.meta.txn_id, base.meta.value_kind),
            policy,
            base_root: base.meta.range_root,
            base_count: base.meta.range_record_count,
            input_seen: false,
            value: std::marker::PhantomData,
        })
    }

    pub(crate) fn push(
        &mut self,
        store: &mut DraftStore<'_>,
        mut incoming: Incoming<K, V>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.require_input(incoming)?;
        self.input_seen = true;
        self.previous_input_end = Some(incoming.to);
        loop {
            cancellation.check()?;
            let Some(mut old) = self.old else {
                return self.emit(
                    store,
                    incoming.from,
                    incoming.to,
                    None,
                    Some(incoming.value),
                );
            };
            if old.to < incoming.from {
                self.account_old(store)?;
                self.emit(store, old.from, old.to, Some(old.value), None)?;
                self.advance_old(store)?;
                continue;
            }
            if incoming.to < old.from {
                return self.emit(
                    store,
                    incoming.from,
                    incoming.to,
                    None,
                    Some(incoming.value),
                );
            }
            self.account_old(store)?;
            if old.from < incoming.from {
                let end = previous(incoming.from, "ordered merge old prefix")?;
                self.emit(store, old.from, end, Some(old.value), None)?;
                old.from = incoming.from;
                self.old = Some(old);
                continue;
            }
            if incoming.from < old.from {
                let end = previous(old.from, "ordered merge input prefix")?;
                self.emit(store, incoming.from, end, None, Some(incoming.value))?;
                incoming.from = old.from;
                continue;
            }

            let end = old.to.min(incoming.to);
            self.emit(store, old.from, end, Some(old.value), Some(incoming.value))?;
            if old.to == end {
                self.advance_old(store)?;
            } else {
                old.from = next(end, "ordered merge old remainder")?;
                self.old = Some(old);
            }
            if incoming.to == end {
                return Ok(());
            }
            incoming.from = next(end, "ordered merge input remainder")?;
        }
    }

    pub(crate) fn finish(
        mut self,
        store: &mut DraftStore<'_>,
        cancellation: &CancellationToken,
    ) -> Result<Finished<P::Output>> {
        if !self.input_seen && P::PRESERVE_WITHOUT_INPUT {
            return self.finish_preserved(store, cancellation);
        }
        while let Some(old) = self.old {
            cancellation.check()?;
            self.account_old(store)?;
            self.emit(store, old.from, old.to, Some(old.value), None)?;
            self.advance_old(store)?;
        }
        let (root, record_count) = self.output.finish(store)?;
        range_mutation::retire_tree::<K, _, _>(store, self.base_root, || cancellation.check())?;
        store.draft.base_range_tree_retired = true;
        store.draft.meta.range_root = root;
        store.draft.meta.range_record_count = record_count;
        Ok(Finished {
            output: self.policy.finish()?,
        })
    }

    fn finish_preserved(
        mut self,
        store: &mut DraftStore<'_>,
        cancellation: &CancellationToken,
    ) -> Result<Finished<P::Output>> {
        while let Some(old) = self.old {
            cancellation.check()?;
            self.policy
                .observe(old.from, old.to, Some(old.value), None, Some(old.value))?;
            self.advance_old(store)?;
        }
        store.draft.meta.range_root = self.base_root;
        store.draft.meta.range_record_count = self.base_count;
        Ok(Finished {
            output: self.policy.finish()?,
        })
    }

    fn emit(
        &mut self,
        store: &mut DraftStore<'_>,
        from: K,
        to: K,
        old: Option<u32>,
        incoming: Option<V>,
    ) -> Result<()> {
        let new = self.policy.transform(store, old, incoming)?;
        self.policy.observe(from, to, old, incoming, new)?;
        if let Some(value) = new {
            self.output.emit(store, Record { from, to, value })?;
        }
        Ok(())
    }

    fn account_old(&mut self, store: &mut DraftStore<'_>) -> Result<()> {
        if !self.old_accounted {
            let value = self
                .old
                .ok_or(Error::Corrupt("ordered merge lost its old range"))?
                .value;
            store.track_membership_refcount(value, -1)?;
            self.old_accounted = true;
        }
        Ok(())
    }

    fn advance_old(&mut self, store: &mut DraftStore<'_>) -> Result<()> {
        self.old = self.old_cursor.next(store)?;
        self.old_accounted = false;
        Ok(())
    }

    fn require_input(&self, incoming: Incoming<K, V>) -> Result<()> {
        if incoming.from > incoming.to {
            return Err(Error::Corrupt("ordered merge input range is reversed"));
        }
        if self
            .previous_input_end
            .is_some_and(|previous| previous >= incoming.from)
        {
            return Err(Error::Corrupt(
                "ordered merge input ranges overlap or are out of order",
            ));
        }
        Ok(())
    }
}

struct Output<K> {
    builder: Builder<K>,
    pending: Option<Record<K>>,
    membership: bool,
}

impl<K: IpKey> Output<K> {
    fn new(transaction: u64, value_kind: ValueKind) -> Self {
        Self {
            builder: Builder::new(transaction, value_kind),
            pending: None,
            membership: value_kind == ValueKind::Membership,
        }
    }

    fn emit(&mut self, store: &mut DraftStore<'_>, record: Record<K>) -> Result<()> {
        if let Some(pending) = self.pending.as_mut() {
            if pending.value == record.value && pending.to.checked_next() == Some(record.from) {
                pending.to = record.to;
                crate::work::range_coalesced(1);
                return Ok(());
            }
        }
        if let Some(pending) = self.pending.replace(record) {
            self.push(store, pending)?;
        }
        Ok(())
    }

    fn finish(&mut self, store: &mut DraftStore<'_>) -> Result<(u32, u64)> {
        if let Some(pending) = self.pending.take() {
            self.push(store, pending)?;
        }
        self.builder.finish(store)
    }

    fn push(&mut self, store: &mut DraftStore<'_>, record: Record<K>) -> Result<()> {
        if self.membership {
            store.track_membership_refcount(record.value, 1)?;
        }
        self.builder.push(store, record)
    }
}

#[derive(Default)]
pub(crate) struct MapComparison {
    value: Comparison,
}

impl MapComparison {
    pub(crate) fn observe<K: IpKey>(
        &mut self,
        from: K,
        to: K,
        old: Option<u32>,
        new: Option<u32>,
    ) -> Result<()> {
        let count = from.inclusive_cardinality(to)?;
        if old.is_some() {
            self.value.before = add(self.value.before, count)?;
        }
        if new.is_some() {
            self.value.after = add(self.value.after, count)?;
        }
        match (old, new) {
            (Some(left), Some(right)) if left == right => {
                self.value.unchanged = add(self.value.unchanged, count)?;
            }
            (Some(_), Some(_)) => self.value.changed = add(self.value.changed, count)?,
            (Some(_), None) => self.value.removed = add(self.value.removed, count)?,
            (None, Some(_)) => self.value.added = add(self.value.added, count)?,
            (None, None) => {}
        }
        Ok(())
    }

    pub(crate) fn finish(self) -> Result<Comparison> {
        let before = add(
            add(self.value.unchanged, self.value.changed)?,
            self.value.removed,
        )?;
        let after = add(
            add(self.value.unchanged, self.value.changed)?,
            self.value.added,
        )?;
        if before != self.value.before || after != self.value.after {
            return Err(Error::Corrupt("ordered merge counters do not balance"));
        }
        Ok(self.value)
    }
}

pub(crate) fn add(left: Cardinality129, right: Cardinality129) -> Result<Cardinality129> {
    left.checked_add(right)
        .map_err(|_| Error::ArithmeticOverflow("ordered merge address count"))
}

fn previous<K: IpKey>(key: K, context: &'static str) -> Result<K> {
    key.checked_previous()
        .ok_or(Error::ArithmeticOverflow(context))
}

fn next<K: IpKey>(key: K, context: &'static str) -> Result<K> {
    key.checked_next().ok_or(Error::ArithmeticOverflow(context))
}
