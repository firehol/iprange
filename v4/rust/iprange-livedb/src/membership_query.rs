//! Named membership scopes, point matches, and exact aggregation.

#[path = "membership_query/aggregation.rs"]
mod aggregation;
#[path = "membership_query/algebra.rs"]
pub(crate) mod algebra;
#[path = "membership_query/cache.rs"]
mod cache;
#[path = "membership_query/decode.rs"]
mod decode;
#[path = "membership_query/join.rs"]
mod join;
#[path = "membership_query/scope.rs"]
mod scope;
#[path = "membership_query/selected.rs"]
mod selected;

use std::fmt;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, ValueKind};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::membership_view::MembershipView;
use crate::reader_core::{GenerationReader, ReaderCore};

pub use aggregation::{
    FeedCardinality, FeedOverlap, MembershipAggregateSink, MembershipAggregationMode,
    MembershipAggregationReport,
};
pub use algebra::{
    AlgebraComparisonReport, AlgebraCountReport, AlgebraOutputBudget, AlgebraOutputMode,
    AlgebraPreparationFailure, AlgebraSetOperation, AlgebraSetOutcome, AlgebraSetReport,
    AlgebraSetResult, FeedSelection, MembershipAlgebra, MembershipAlgebraBudget,
};
pub use join::{
    DirectJoinBudget, DirectJoinCell, DirectJoinReport, DirectJoinSink, DirectJoinSource,
    MembershipCrossCell, MembershipJoinReport, MembershipJoinSink, UncoveredFeed, UncoveredSide,
};

const WORD_BATCH: usize = 64;

/// Heap retained by one reusable named-feed scope and one aggregation call.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct MembershipQueryBudget {
    pub max_heap_bytes: u64,
}

/// One caller-selected pair of feed names.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FeedPair {
    pub left: FeedName,
    pub right: FeedName,
}

/// Exact point-match outcome after all matching names were emitted.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct MatchingFeedsReport {
    pub matching_feed_count: u64,
}

/// Allocation-free consumer for names matching one address.
pub trait MatchingFeedSink {
    fn matching_feed(&mut self, feed: FeedName) -> Result<()>;
}

impl<F> MatchingFeedSink for F
where
    F: FnMut(FeedName) -> Result<()>,
{
    fn matching_feed(&mut self, feed: FeedName) -> Result<()> {
        self(feed)
    }
}

/// Format-facing query capability over one pinned membership generation.
#[derive(Clone, Copy)]
pub struct MembershipQuery<'a> {
    reader: GenerationReader<'a>,
    address_family: AddressFamily,
    active_feed_count: u64,
    range_record_count: u64,
}

/// Reusable SDK-owned mapping from caller names to one pinned catalog.
pub struct MembershipScope<'a> {
    reader: GenerationReader<'a>,
    state: MembershipScopeState,
}

/// Reader-independent state retained by language bindings for one pinned scope.
pub(crate) struct MembershipScopeState {
    pub(crate) address_family: AddressFamily,
    pub(crate) range_record_count: u64,
    pub(crate) data: scope::ScopeData,
}

/// Transient mapped-reader view over reusable scope state.
#[derive(Clone, Copy)]
pub(crate) struct MembershipScopeView<'a> {
    pub(crate) reader: GenerationReader<'a>,
    pub(crate) state: &'a MembershipScopeState,
}

impl<'a> MembershipQuery<'a> {
    pub(crate) fn new(core: &'a ReaderCore) -> Result<Self> {
        let info = core.info();
        if info.value_kind != ValueKind::Membership {
            return Err(Error::WrongValueKind(
                "membership query requires a membership database",
            ));
        }
        Ok(Self {
            reader: core.read(),
            address_family: info.address_family,
            active_feed_count: info.active_feed_count,
            range_record_count: info.range_record_count,
        })
    }

    /// Resolve every active feed into one reusable scope.
    pub fn all_feeds(
        self,
        budget: MembershipQueryBudget,
        cancellation: &CancellationToken,
    ) -> Result<MembershipScope<'a>> {
        Ok(MembershipScope {
            reader: self.reader,
            state: MembershipScopeState {
                address_family: self.address_family,
                range_record_count: self.range_record_count,
                data: scope::ScopeData::all(
                    self.reader,
                    self.active_feed_count,
                    budget.max_heap_bytes,
                    cancellation,
                )?,
            },
        })
    }

    /// Resolve a nonempty unique list of names into one reusable scope.
    pub fn named_feeds(
        self,
        names: &[FeedName],
        budget: MembershipQueryBudget,
        cancellation: &CancellationToken,
    ) -> Result<MembershipScope<'a>> {
        Ok(MembershipScope {
            reader: self.reader,
            state: MembershipScopeState {
                address_family: self.address_family,
                range_record_count: self.range_record_count,
                data: scope::ScopeData::named(
                    self.reader,
                    names,
                    budget.max_heap_bytes,
                    cancellation,
                )?,
            },
        })
    }

    pub(crate) fn named_state_from<I>(
        self,
        count: usize,
        names: I,
        budget: MembershipQueryBudget,
        cancellation: &CancellationToken,
    ) -> Result<MembershipScopeState>
    where
        I: IntoIterator<Item = Result<FeedName>>,
    {
        Ok(MembershipScopeState {
            address_family: self.address_family,
            range_record_count: self.range_record_count,
            data: scope::ScopeData::named_from(
                self.reader,
                count,
                names,
                budget.max_heap_bytes,
                cancellation,
            )?,
        })
    }

    /// Emit every feed matching one IPv4 address without scanning the catalog.
    pub fn matching_feeds_v4<S: MatchingFeedSink>(
        self,
        address: Ipv4Key,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MatchingFeedsReport> {
        let membership = self.reader.lookup_membership_v4(address)?;
        matching(self.reader, membership, sink, cancellation)
    }

    /// Emit every feed matching one IPv6 address without scanning the catalog.
    pub fn matching_feeds_v6<S: MatchingFeedSink>(
        self,
        address: Ipv6Key,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MatchingFeedsReport> {
        let membership = self.reader.lookup_membership_v6(address)?;
        matching(self.reader, membership, sink, cancellation)
    }
}

impl MembershipScope<'_> {
    /// Number of feeds resolved into this pinned scope.
    pub fn feed_count(&self) -> usize {
        self.state.data.entries.len()
    }

    /// Enumerate this scope's resolved entries in ascending local index order.
    pub fn feeds(&self) -> impl ExactSizeIterator<Item = FeedEntry> + '_ {
        self.state.data.entries.iter().copied()
    }

    pub(crate) fn view(&self) -> MembershipScopeView<'_> {
        self.state.view(self.reader)
    }

    pub(crate) fn into_state(self) -> MembershipScopeState {
        self.state
    }
}

impl MembershipScopeState {
    pub(crate) fn view<'a>(&'a self, reader: GenerationReader<'a>) -> MembershipScopeView<'a> {
        MembershipScopeView {
            reader,
            state: self,
        }
    }

    pub(crate) fn address_family(&self) -> AddressFamily {
        self.address_family
    }

    pub(crate) fn feed_count(&self) -> usize {
        self.data.entries.len()
    }

    pub(crate) fn feeds(&self) -> impl ExactSizeIterator<Item = FeedEntry> + '_ {
        self.data.entries.iter().copied()
    }

    pub(crate) fn require_operation_reservation(&self, bytes: u64) -> Result<()> {
        let _heap = self.data.operation_heap_reserved(bytes)?;
        Ok(())
    }
}

impl fmt::Debug for MembershipQuery<'_> {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("MembershipQuery")
            .field("active_feed_count", &self.active_feed_count)
            .field("range_record_count", &self.range_record_count)
            .finish_non_exhaustive()
    }
}

impl fmt::Debug for MembershipScope<'_> {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("MembershipScope")
            .field("feed_count", &self.state.data.entries.len())
            .field("range_record_count", &self.state.range_record_count)
            .finish_non_exhaustive()
    }
}

fn matching<S: MatchingFeedSink>(
    reader: GenerationReader<'_>,
    membership: Option<MembershipView<'_>>,
    sink: &mut S,
    cancellation: &CancellationToken,
) -> Result<MatchingFeedsReport> {
    let Some(membership) = membership else {
        return Ok(MatchingFeedsReport {
            matching_feed_count: 0,
        });
    };
    crate::work::membership_decode(1);
    let word_count = membership.word_count()?;
    let mut words = [0u64; WORD_BATCH];
    let mut start = 0u32;
    let mut matching_feed_count = 0u64;
    while start < word_count {
        cancellation.check()?;
        let expected = (word_count - start).min(WORD_BATCH as u32) as usize;
        let read = membership.read_words(start, &mut words[..expected])?;
        if read != expected {
            return Err(Error::Corrupt("membership word read ended early"));
        }
        crate::work::membership_word_read(read as u64);
        for (offset, word) in words[..read].iter().copied().enumerate() {
            let word_index = start
                .checked_add(offset as u32)
                .ok_or_else(|| Error::ArithmeticOverflow("membership word index"))?;
            emit_word(reader, word_index, word, sink, &mut matching_feed_count)?;
        }
        start = start
            .checked_add(read as u32)
            .ok_or_else(|| Error::ArithmeticOverflow("membership word index"))?;
    }
    Ok(MatchingFeedsReport {
        matching_feed_count,
    })
}

fn emit_word<S: MatchingFeedSink>(
    reader: GenerationReader<'_>,
    word_index: u32,
    mut word: u64,
    sink: &mut S,
    count: &mut u64,
) -> Result<()> {
    while word != 0 {
        let bit = word.trailing_zeros();
        let index = word_index
            .checked_mul(64)
            .and_then(|base| base.checked_add(bit))
            .ok_or_else(|| Error::ArithmeticOverflow("feed index"))?;
        let feed = reader
            .lookup_feed_index(index)?
            .ok_or_else(|| Error::Corrupt("membership names an inactive feed"))?;
        sink.matching_feed(feed.name)?;
        *count = count
            .checked_add(1)
            .ok_or_else(|| Error::ArithmeticOverflow("matching feed count"))?;
        word &= word - 1;
    }
    Ok(())
}

#[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
#[path = "membership_query/tests.rs"]
mod tests;
