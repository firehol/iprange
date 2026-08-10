//! Canonical selected-feed runs over one physical membership cursor.

use std::mem;

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::heap::Heap;
use crate::key::IpKey;
use crate::reader_core::{GenerationReader, MembershipRange, MembershipRangeCursor};

use super::decode::Scratch;
use super::scope::ScopeData;

#[derive(Clone, Copy)]
pub(super) struct SelectedRange<K> {
    pub(super) from: K,
    pub(super) to: K,
}

pub(super) struct SelectedRanges<'a, K> {
    reader: GenerationReader<'a>,
    scope: &'a ScopeData,
    cursor: MembershipRangeCursor<'a, K>,
    active: Scratch,
    lookahead: Option<Scratch>,
    pending: Option<MembershipRange<K>>,
    physical_count: u64,
}

impl<'a, K: IpKey> SelectedRanges<'a, K> {
    pub(super) fn new(
        reader: GenerationReader<'a>,
        scope: &'a ScopeData,
        heap: &mut Heap,
    ) -> Result<Self> {
        let active = Scratch::new(scope.entries.len(), heap)?;
        let lookahead = if scope.all_catalog {
            None
        } else {
            Some(Scratch::new(scope.entries.len(), heap)?)
        };
        Ok(Self {
            reader,
            scope,
            cursor: reader.membership_ranges::<K>()?,
            active,
            lookahead,
            pending: None,
            physical_count: 0,
        })
    }

    pub(super) fn next(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<Option<SelectedRange<K>>> {
        let mut current = if let Some(pending) = self.pending.take() {
            mem::swap(
                &mut self.active,
                self.lookahead
                    .as_mut()
                    .ok_or_else(|| Error::Corrupt("selected range lookahead is absent"))?,
            );
            pending
        } else {
            loop {
                let Some(first) = self.next_physical(cancellation)? else {
                    self.active.clear(cancellation)?;
                    return Ok(None);
                };
                self.active
                    .load(self.reader, first.membership, self.scope, cancellation)?;
                if !self.active.present.is_empty() {
                    break first;
                }
            }
        };

        if self.lookahead.is_none() {
            return Ok(Some(selected(current)));
        }
        loop {
            let Some(next) = self.next_physical(cancellation)? else {
                return Ok(Some(selected(current)));
            };
            let lookahead = self
                .lookahead
                .as_mut()
                .ok_or_else(|| Error::Corrupt("selected range lookahead disappeared"))?;
            lookahead.load(self.reader, next.membership, self.scope, cancellation)?;
            if lookahead.present.is_empty() {
                return Ok(Some(selected(current)));
            }
            if current.to.checked_next() == Some(next.from)
                && self.active.present == lookahead.present
            {
                current.to = next.to;
                continue;
            }
            self.pending = Some(next);
            return Ok(Some(selected(current)));
        }
    }

    pub(super) fn present(&self) -> &[u32] {
        &self.active.present
    }

    pub(super) fn enable_cache(&mut self, heap: &mut Heap, max_bytes: u64) -> Result<()> {
        let scratch_count = 1 + usize::from(self.lookahead.is_some());
        let share = max_bytes / scratch_count as u64;
        self.active.enable_cache(heap, share)?;
        if let Some(lookahead) = &mut self.lookahead {
            lookahead.enable_cache(heap, share)?;
        }
        Ok(())
    }

    pub(super) const fn physical_count(&self) -> u64 {
        self.physical_count
    }

    fn next_physical(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<Option<MembershipRange<K>>> {
        if self.physical_count & 4095 == 4095 {
            cancellation.check()?;
        }
        let range = self.cursor.next()?;
        if range.is_some() {
            self.physical_count = self
                .physical_count
                .checked_add(1)
                .ok_or_else(|| Error::ArithmeticOverflow("membership scan range count"))?;
        }
        Ok(range)
    }
}

fn selected<K>(range: MembershipRange<K>) -> SelectedRange<K> {
    SelectedRange {
        from: range.from,
        to: range.to,
    }
}
