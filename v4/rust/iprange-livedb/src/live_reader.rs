//! Reader pinned to one registered live generation.

use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::database::DatabaseInfo;
use crate::error::{Error, Result};
use crate::feed::FeedEntry;
use crate::feed_catalog::FeedCursor;
use crate::feed_range_cursor::{FeedRangeCursorV4, FeedRangeCursorV6};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::live_writer::CloseOutcome;
use crate::membership_query::MembershipQuery;
use crate::membership_view::MembershipView;
use crate::publication::{CleanupState, CoordinationCleanup};
use crate::range_cursor::{DirectCursorV4, DirectCursorV6, RangeDirection};
use crate::reader_core::{LiveReaderClose, LiveReaderCore, ReaderCore};
use crate::source::{
    DirectRangeSourceV4, DirectRangeSourceV6, FeedRangeSourceV4, FeedRangeSourceV6,
};
use crate::structured_value::{
    NetworkEnrichmentV1CursorV4, NetworkEnrichmentV1CursorV6, NetworkEnrichmentV1View,
};

/// Factual, retryable live-reader close result.
#[derive(Debug)]
pub struct ReaderCloseResult {
    pub outcome: CloseOutcome,
    pub coordination_cleanup: CoordinationCleanup,
    pub cause: Option<Error>,
}

impl ReaderCloseResult {
    pub const fn cleanup_state(&self) -> CleanupState {
        if matches!(self.coordination_cleanup, CoordinationCleanup::None) {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }
}

/// Reader registered against one committed generation of a live database.
#[derive(Debug)]
pub struct LiveReader {
    core: LiveReaderCore,
}

impl LiveReader {
    /// Open and register a live reader without validating either page graph.
    pub fn open(path: impl AsRef<Path>, cancellation: &CancellationToken) -> Result<Self> {
        Ok(Self {
            core: LiveReaderCore::open(path.as_ref(), cancellation)?,
        })
    }

    /// Identity and counters from this reader's pinned generation.
    pub fn info(&self) -> Result<DatabaseInfo> {
        self.core.info()
    }

    /// Look up one address in an IPv4 direct-value database.
    pub fn lookup_direct_v4(&self, address: Ipv4Key) -> Result<Option<u32>> {
        self.core.read()?.lookup_direct_v4(address)
    }

    /// Look up one address in an IPv6 direct-value database.
    pub fn lookup_direct_v6(&self, address: Ipv6Key) -> Result<Option<u32>> {
        self.core.read()?.lookup_direct_v6(address)
    }

    /// Open an ordered cursor over an IPv4 direct-value database.
    pub fn direct_cursor_v4(&self, direction: RangeDirection) -> Result<DirectCursorV4<'_>> {
        self.core.read()?.direct_cursor_v4(direction)
    }

    /// Open an ordered cursor over an IPv6 direct-value database.
    pub fn direct_cursor_v6(&self, direction: RangeDirection) -> Result<DirectCursorV6<'_>> {
        self.core.read()?.direct_cursor_v6(direction)
    }

    /// Look up one exact feed name in this pinned membership generation.
    pub fn lookup_feed(&self, name: &str) -> Result<Option<FeedEntry>> {
        self.core.read()?.lookup_feed(name)
    }

    /// Enumerate this generation's feeds in ascending feed-index order.
    pub fn feed_cursor(&self) -> Result<FeedCursor<'_>> {
        self.core.read()?.feed_cursor()
    }

    /// Open an ordered cursor over one exact IPv4 named feed.
    pub fn feed_range_cursor_v4(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV4<'_>> {
        self.core.read()?.feed_range_cursor_v4(name, direction)
    }

    /// Open an ordered cursor over one exact IPv6 named feed.
    pub fn feed_range_cursor_v6(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV6<'_>> {
        self.core.read()?.feed_range_cursor_v6(name, direction)
    }

    /// Stream one named IPv4 feed in bounded mapped batches.
    pub fn named_feed_source_v4(&self, name: &str) -> Result<FeedRangeSourceV4<'_>> {
        Ok(FeedRangeSourceV4::new(
            self.feed_range_cursor_v4(name, RangeDirection::Forward)?,
        ))
    }

    /// Stream one named IPv6 feed in bounded mapped batches.
    pub fn named_feed_source_v6(&self, name: &str) -> Result<FeedRangeSourceV6<'_>> {
        Ok(FeedRangeSourceV6::new(
            self.feed_range_cursor_v6(name, RangeDirection::Forward)?,
        ))
    }

    /// Stream this IPv4 direct map in bounded mapped batches.
    pub fn direct_range_source_v4(&self) -> Result<DirectRangeSourceV4<'_>> {
        Ok(DirectRangeSourceV4::new(
            self.direct_cursor_v4(RangeDirection::Forward)?,
        ))
    }

    /// Stream this IPv6 direct map in bounded mapped batches.
    pub fn direct_range_source_v6(&self) -> Result<DirectRangeSourceV6<'_>> {
        Ok(DirectRangeSourceV6::new(
            self.direct_cursor_v6(RangeDirection::Forward)?,
        ))
    }

    /// Look up one address in this pinned IPv4 membership generation.
    pub fn lookup_membership_v4(&self, address: Ipv4Key) -> Result<Option<MembershipView<'_>>> {
        self.core.read()?.lookup_membership_v4(address)
    }

    /// Look up one address in this pinned IPv6 membership generation.
    pub fn lookup_membership_v6(&self, address: Ipv6Key) -> Result<Option<MembershipView<'_>>> {
        self.core.read()?.lookup_membership_v6(address)
    }

    /// Look up one address in this pinned IPv4 enrichment generation.
    pub fn lookup_network_enrichment_v1_v4(
        &self,
        address: Ipv4Key,
    ) -> Result<Option<NetworkEnrichmentV1View<'_>>> {
        self.core.read()?.lookup_network_enrichment_v1_v4(address)
    }

    /// Look up one address in this pinned IPv6 enrichment generation.
    pub fn lookup_network_enrichment_v1_v6(
        &self,
        address: Ipv6Key,
    ) -> Result<Option<NetworkEnrichmentV1View<'_>>> {
        self.core.read()?.lookup_network_enrichment_v1_v6(address)
    }

    /// Open an ordered cursor over this pinned IPv4 enrichment generation.
    pub fn network_enrichment_v1_cursor_v4(
        &self,
        direction: RangeDirection,
    ) -> Result<NetworkEnrichmentV1CursorV4<'_>> {
        self.core.read()?.network_enrichment_v1_cursor_v4(direction)
    }

    /// Open an ordered cursor over this pinned IPv6 enrichment generation.
    pub fn network_enrichment_v1_cursor_v6(
        &self,
        direction: RangeDirection,
    ) -> Result<NetworkEnrichmentV1CursorV6<'_>> {
        self.core.read()?.network_enrichment_v1_cursor_v6(direction)
    }

    /// Open a format-facing query capability over this pinned membership generation.
    pub fn membership_query(&self) -> Result<MembershipQuery<'_>> {
        MembershipQuery::new(self.core()?)
    }

    /// Exact decompressed metadata length, or absence.
    pub fn metadata_json_len(&self) -> Result<Option<u64>> {
        Ok(self.core.read()?.metadata_json_len())
    }

    /// Fill caller storage with this generation's exact opaque metadata bytes.
    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.core.read()?.read_metadata_json(output)
    }

    /// Return this generation's complete bounded metadata value, or absence.
    pub fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        self.core.read()?.metadata_json()
    }

    pub(crate) fn core(&self) -> Result<&ReaderCore> {
        self.core.reader()
    }

    /// Clear this registration. An incomplete close retains retry authority.
    pub fn close(&mut self) -> Result<ReaderCloseResult> {
        Ok(self.core.close()?.into())
    }
}

impl From<LiveReaderClose> for ReaderCloseResult {
    fn from(result: LiveReaderClose) -> Self {
        Self {
            outcome: if result.closed {
                CloseOutcome::Closed
            } else {
                CloseOutcome::CloseIncomplete
            },
            coordination_cleanup: result.coordination_cleanup,
            cause: result.cause,
        }
    }
}
