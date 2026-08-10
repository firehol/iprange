//! Owned reusable membership scopes for language bindings.

use std::sync::Arc;

use crate::membership_query::{MembershipScopeState, MembershipScopeView};
use crate::{
    AlgebraComparisonReport, AlgebraCountReport, AlgebraOutputBudget, AlgebraOutputMode,
    AlgebraSetOperation, AlgebraSetOutcome, CancellationToken, DirectJoinBudget, DirectJoinReport,
    DirectJoinSink, FeedEntry, FeedName, FeedSelection, MembershipAggregateSink,
    MembershipAggregationMode, MembershipAggregationReport, MembershipAlgebraBudget,
    MembershipJoinReport, MembershipJoinSink, MembershipQuery, MembershipQueryBudget,
    PublicationPolicy, Result, ValueTag,
};

use super::Reader;

/// Reader-retaining scope with no self-referential Rust borrow.
pub struct MembershipScope {
    reader: Arc<Reader>,
    state: MembershipScopeState,
}

impl std::fmt::Debug for MembershipScope {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output
            .debug_struct("MembershipScope")
            .field("feed_count", &self.state.feed_count())
            .finish_non_exhaustive()
    }
}

impl MembershipScope {
    pub fn all(
        reader: Arc<Reader>,
        budget: MembershipQueryBudget,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        let state = MembershipQuery::new(reader.core()?)?
            .all_feeds(budget, cancellation)?
            .into_state();
        Ok(Self { reader, state })
    }

    pub fn named(
        reader: Arc<Reader>,
        names: &[FeedName],
        budget: MembershipQueryBudget,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        let state = MembershipQuery::new(reader.core()?)?
            .named_feeds(names, budget, cancellation)?
            .into_state();
        Ok(Self { reader, state })
    }

    pub fn named_from<I>(
        reader: Arc<Reader>,
        count: usize,
        names: I,
        budget: MembershipQueryBudget,
        cancellation: &CancellationToken,
    ) -> Result<Self>
    where
        I: IntoIterator<Item = Result<FeedName>>,
    {
        let state = MembershipQuery::new(reader.core()?)?.named_state_from(
            count,
            names,
            budget,
            cancellation,
        )?;
        Ok(Self { reader, state })
    }

    pub fn address_family(&self) -> crate::AddressFamily {
        self.state.address_family()
    }

    pub fn feed_count(&self) -> usize {
        self.state.feed_count()
    }

    pub fn feeds(&self) -> impl ExactSizeIterator<Item = FeedEntry> + '_ {
        self.state.feeds()
    }

    pub fn aggregate<S: MembershipAggregateSink>(
        &self,
        mode: MembershipAggregationMode<'_>,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MembershipAggregationReport> {
        self.view()?.aggregate(mode, sink, cancellation)
    }

    pub fn aggregate_reserved<S: MembershipAggregateSink>(
        &self,
        mode: MembershipAggregationMode<'_>,
        reserved_heap_bytes: u64,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MembershipAggregationReport> {
        self.view()?
            .aggregate_reserved(mode, reserved_heap_bytes, sink, cancellation)
    }

    pub fn require_operation_reservation(&self, bytes: u64) -> Result<()> {
        self.state.require_operation_reservation(bytes)
    }

    pub fn join_direct<S: DirectJoinSink>(
        &self,
        direct: &Reader,
        budget: DirectJoinBudget,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<DirectJoinReport> {
        self.view()?
            .join_direct_core(direct.core()?, budget, sink, cancellation)
    }

    pub fn join_membership<S: MembershipJoinSink>(
        &self,
        right: &Self,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MembershipJoinReport> {
        let left = self.view()?;
        let right = right.view()?;
        if left.state.address_family != right.state.address_family {
            return Err(crate::Error::WrongAddressFamily(
                "membership join source families differ",
            ));
        }
        left.join_membership(right, sink, cancellation)
    }

    pub(super) fn view(&self) -> Result<MembershipScopeView<'_>> {
        Ok(self.state.view(self.reader.core()?.read()))
    }
}

/// Scope-retaining global algebra with one resolved catalog shared by every call.
pub struct MembershipAlgebra {
    sources: Vec<Arc<MembershipScope>>,
    state: crate::membership_query::algebra::MembershipAlgebraState,
}

impl std::fmt::Debug for MembershipAlgebra {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output
            .debug_struct("MembershipAlgebra")
            .field("source_count", &self.sources.len())
            .field("feed_count", &self.state.names().len())
            .finish_non_exhaustive()
    }
}

impl MembershipAlgebra {
    pub fn new(
        sources: Vec<Arc<MembershipScope>>,
        budget: MembershipAlgebraBudget,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        let retained_source_bytes = sources
            .capacity()
            .checked_mul(std::mem::size_of::<Arc<MembershipScope>>())
            .ok_or(crate::Error::BudgetExceeded(
                "membership algebra source heap",
            ))?;
        let state = crate::membership_query::algebra::MembershipAlgebraState::new(
            sources.len(),
            |index| &sources[index].state,
            budget,
            retained_source_bytes,
            cancellation,
        )?;
        Ok(Self { sources, state })
    }

    pub fn address_family(&self) -> crate::AddressFamily {
        self.state.family()
    }

    pub fn feed_count(&self) -> usize {
        self.state.names().len()
    }

    pub fn feeds(&self) -> impl ExactSizeIterator<Item = FeedName> + '_ {
        self.state.names().iter().copied()
    }

    pub fn require_operation_reservation(&self, bytes: u64) -> Result<()> {
        let _heap =
            crate::membership_query::algebra::AlgebraAccess::operation_heap_reserved(self, bytes)?;
        Ok(())
    }

    pub fn count(
        &self,
        feeds: FeedSelection<'_>,
        reserved_heap_bytes: u64,
        cancellation: &CancellationToken,
    ) -> Result<AlgebraCountReport> {
        crate::membership_query::algebra::analysis::count(
            self,
            feeds,
            reserved_heap_bytes,
            cancellation,
        )
    }

    pub fn compare(
        &self,
        left: FeedSelection<'_>,
        right: FeedSelection<'_>,
        reserved_heap_bytes: u64,
        cancellation: &CancellationToken,
    ) -> Result<AlgebraComparisonReport> {
        crate::membership_query::algebra::analysis::compare(
            self,
            left,
            right,
            reserved_heap_bytes,
            cancellation,
        )
    }

    #[allow(clippy::too_many_arguments)]
    pub fn publish_set(
        &self,
        destination: impl AsRef<std::path::Path>,
        value_tag: ValueTag,
        operation: AlgebraSetOperation<'_>,
        mode: AlgebraOutputMode,
        metadata_json: Option<&[u8]>,
        policy: PublicationPolicy,
        budget: AlgebraOutputBudget,
        reserved_heap_bytes: u64,
        cancellation: &CancellationToken,
    ) -> AlgebraSetOutcome {
        crate::membership_query::algebra::output::publish(
            self,
            destination.as_ref(),
            value_tag,
            operation,
            mode,
            metadata_json,
            policy,
            budget,
            reserved_heap_bytes,
            cancellation,
        )
    }
}

impl crate::membership_query::algebra::AlgebraAccess for MembershipAlgebra {
    fn state(&self) -> &crate::membership_query::algebra::MembershipAlgebraState {
        &self.state
    }

    fn source(&self, index: usize) -> Result<crate::membership_query::algebra::AlgebraInput<'_>> {
        let source = self.sources.get(index).ok_or(crate::Error::Corrupt(
            "membership algebra source disappeared",
        ))?;
        let view = source.view()?;
        self.state.input(index, view.reader, &view.state.data)
    }
}
