//! Name-translated membership import over the ordered range merge.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::contract::MembershipOperation;
use crate::error::Result;
use crate::key::IpKey;
use crate::membership_dictionary::Interned;
use crate::workflow::Comparison;

use super::range_merge::{Incoming, MapComparison, OrderedMerge, Policy};
use super::DraftStore;

#[derive(Clone, Copy)]
pub(crate) struct TranslatedMembership {
    id: u32,
    words: u32,
}

impl TranslatedMembership {
    pub(crate) const fn new(id: u32, words: u32) -> Self {
        Self { id, words }
    }
}

impl From<Interned> for TranslatedMembership {
    fn from(value: Interned) -> Self {
        Self::new(value.id, value.word_count)
    }
}

pub(crate) struct ImportMerge<K: IpKey> {
    inner: OrderedMerge<K, TranslatedMembership, ImportPolicy>,
}

impl<K: IpKey> ImportMerge<K> {
    pub(crate) fn new(
        store: &mut DraftStore<'_>,
        base: &Bootstrap,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        Ok(Self {
            inner: OrderedMerge::new(store, base, ImportPolicy::default(), cancellation)?,
        })
    }

    pub(crate) fn push(
        &mut self,
        store: &mut DraftStore<'_>,
        from: K,
        to: K,
        membership: TranslatedMembership,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.inner.push(
            store,
            Incoming {
                from,
                to,
                value: membership,
            },
            cancellation,
        )
    }

    pub(crate) fn finish(
        self,
        store: &mut DraftStore<'_>,
        cancellation: &CancellationToken,
    ) -> Result<Comparison> {
        Ok(self.inner.finish(store, cancellation)?.output)
    }
}

#[derive(Default)]
struct ImportPolicy {
    comparison: MapComparison,
    cached: Option<CachedUnion>,
}

#[derive(Clone, Copy)]
struct CachedUnion {
    old: u32,
    supplied: u32,
    new: Option<u32>,
}

impl<K: IpKey> Policy<K, TranslatedMembership> for ImportPolicy {
    type Output = Comparison;

    const PRESERVE_WITHOUT_INPUT: bool = true;

    fn transform(
        &mut self,
        store: &mut DraftStore<'_>,
        old: Option<u32>,
        incoming: Option<TranslatedMembership>,
    ) -> Result<Option<u32>> {
        let Some(incoming) = incoming else {
            return Ok(old);
        };
        let Some(old) = old else {
            return Ok(Some(incoming.id));
        };
        if let Some(cached) = self
            .cached
            .filter(|cached| cached.old == old && cached.supplied == incoming.id)
        {
            return Ok(cached.new);
        }
        let new = store.combine_memberships(
            old,
            incoming.id,
            incoming.words,
            MembershipOperation::Union,
        )?;
        self.cached = Some(CachedUnion {
            old,
            supplied: incoming.id,
            new,
        });
        Ok(new)
    }

    fn observe(
        &mut self,
        from: K,
        to: K,
        old: Option<u32>,
        _incoming: Option<TranslatedMembership>,
        new: Option<u32>,
    ) -> Result<()> {
        self.comparison.observe(from, to, old, new)
    }

    fn finish(self) -> Result<Self::Output> {
        self.comparison.finish()
    }
}
