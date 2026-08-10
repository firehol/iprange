//! Ordered provider joins over pinned v4 readers.

#[path = "join/direct.rs"]
mod direct;
#[path = "join/membership.rs"]
mod membership;

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::ValueKind;
use crate::database::DatabaseInfo;
use crate::error::{Error, Result};
use crate::feed::FeedName;
use crate::live_reader::LiveReader;
use crate::reader_core::ReaderCore;
use crate::ImmutableReader;

use super::{MembershipScope, MembershipScopeView};

/// Explicit pinned direct-value side of a membership/direct join.
#[derive(Clone, Copy, Debug)]
pub enum DirectJoinSource<'a> {
    Immutable(&'a ImmutableReader),
    Live(&'a LiveReader),
}

/// Maximum distinct `(feed,direct-value)` results retained by one join.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DirectJoinBudget {
    pub max_result_cells: u64,
}

/// One exact mapped or unmapped direct-provider result.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DirectJoinCell {
    pub feed: FeedName,
    pub direct_value: Option<u32>,
    pub addresses: Cardinality129,
}

/// Bounded terminal result consumer for membership/direct joins.
pub trait DirectJoinSink {
    fn direct_join_cells(&mut self, batch: &[DirectJoinCell]) -> Result<()>;
}

/// Exact traversal and union-coverage facts for one membership/direct join.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DirectJoinReport {
    pub membership_range_count: u64,
    pub direct_ranges_visited: u64,
    pub joined_segment_count: u64,
    pub selected_addresses: Cardinality129,
    pub mapped_addresses: Cardinality129,
    pub unmapped_addresses: Cardinality129,
    pub result_cell_count: u64,
}

/// Side owning one uncovered feed result.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum UncoveredSide {
    Left,
    Right,
}

/// One exact cross-file membership overlap.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct MembershipCrossCell {
    pub left: FeedName,
    pub right: FeedName,
    pub addresses: Cardinality129,
}

/// One feed's coverage not covered by any selected feed on the other side.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct UncoveredFeed {
    pub side: UncoveredSide,
    pub feed: FeedName,
    pub addresses: Cardinality129,
}

/// Bounded terminal result consumer for membership/membership joins.
pub trait MembershipJoinSink {
    fn membership_cross_cells(&mut self, batch: &[MembershipCrossCell]) -> Result<()>;
    fn uncovered_feeds(&mut self, batch: &[UncoveredFeed]) -> Result<()>;
}

/// Exact traversal and selected-union facts for one membership join.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct MembershipJoinReport {
    pub left_range_count: u64,
    pub right_range_count: u64,
    pub joined_segment_count: u64,
    pub left_addresses: Cardinality129,
    pub right_addresses: Cardinality129,
    pub overlap_addresses: Cardinality129,
    pub left_uncovered_addresses: Cardinality129,
    pub right_uncovered_addresses: Cardinality129,
    pub cross_result_count: u64,
    pub uncovered_result_count: u64,
}

#[derive(Clone, Copy)]
pub(super) struct Source<'a> {
    pub(super) core: &'a ReaderCore,
    pub(super) info: DatabaseInfo,
}

impl MembershipScope<'_> {
    /// Merge-join this named scope with one pinned direct-value map.
    pub fn join_direct<S: DirectJoinSink>(
        &self,
        source: DirectJoinSource<'_>,
        budget: DirectJoinBudget,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<DirectJoinReport> {
        self.view().join_direct(source, budget, sink, cancellation)
    }

    /// Merge-join two pinned named scopes without point lookups.
    pub fn join_membership<S: MembershipJoinSink>(
        &self,
        right: &MembershipScope<'_>,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MembershipJoinReport> {
        self.view()
            .join_membership(right.view(), sink, cancellation)
    }
}

impl MembershipScopeView<'_> {
    pub(crate) fn join_direct<S: DirectJoinSink>(
        self,
        source: DirectJoinSource<'_>,
        budget: DirectJoinBudget,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<DirectJoinReport> {
        let source = Source::new(source)?;
        self.join_direct_source(source, budget, sink, cancellation)
    }

    pub(crate) fn join_direct_core<S: DirectJoinSink>(
        self,
        source: &ReaderCore,
        budget: DirectJoinBudget,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<DirectJoinReport> {
        let source = Source::from_core(source)?;
        self.join_direct_source(source, budget, sink, cancellation)
    }

    fn join_direct_source<S: DirectJoinSink>(
        self,
        source: Source<'_>,
        budget: DirectJoinBudget,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<DirectJoinReport> {
        if source.info.address_family != self.state.address_family {
            return Err(Error::WrongAddressFamily(
                "direct join source family differs",
            ));
        }
        direct::run(self, source, budget, sink, cancellation)
    }

    pub(crate) fn join_membership<S: MembershipJoinSink>(
        self,
        right: MembershipScopeView<'_>,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MembershipJoinReport> {
        if self.state.address_family != right.state.address_family {
            return Err(Error::WrongAddressFamily(
                "membership join source families differ",
            ));
        }
        membership::run(self, right, sink, cancellation)
    }
}

impl<'a> Source<'a> {
    fn new(source: DirectJoinSource<'a>) -> Result<Self> {
        let core = match source {
            DirectJoinSource::Immutable(reader) => reader.core(),
            DirectJoinSource::Live(reader) => reader.core()?,
        };
        Self::from_core(core)
    }

    fn from_core(core: &'a ReaderCore) -> Result<Self> {
        let info = core.info();
        if info.value_kind != ValueKind::Direct {
            return Err(Error::WrongValueKind(
                "membership/direct join requires a direct source",
            ));
        }
        Ok(Self { core, info })
    }
}
