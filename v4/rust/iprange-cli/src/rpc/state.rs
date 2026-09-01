//! Connection-owned reader and cursor state for the read-only methods.

use std::collections::HashMap;

use iprange_livedb::error::Result;
use iprange_livedb::{
    DatabaseInfo, DirectCursorV4, DirectCursorV6, FeedCursor, FeedRangeCursorV4, FeedRangeCursorV6,
    ImmutableReader, Ipv4Key, Ipv6Key, LiveReader, MembershipQuery, MembershipView,
    NetworkEnrichmentV1CursorV4, NetworkEnrichmentV1CursorV6, NetworkEnrichmentV1View,
    RangeDirection, ReaderCloseResult,
};

/// Reader modes that can be attached to one connection-local handle.
///
/// Both variants expose the same semantic reader operations. Delegation here
/// keeps handlers mode-neutral while retaining the SDK's distinct immutable
/// and registered-live ownership and close behavior.
pub enum ReaderValue {
    Immutable(ImmutableReader),
    Live(LiveReader),
}

impl ReaderValue {
    pub fn info(&self) -> Result<DatabaseInfo> {
        match self {
            Self::Immutable(reader) => Ok(reader.info()),
            Self::Live(reader) => reader.info(),
        }
    }

    pub fn lookup_direct_v4(&self, address: Ipv4Key) -> Result<Option<u32>> {
        match self {
            Self::Immutable(reader) => reader.lookup_direct_v4(address),
            Self::Live(reader) => reader.lookup_direct_v4(address),
        }
    }

    pub fn lookup_direct_v6(&self, address: Ipv6Key) -> Result<Option<u32>> {
        match self {
            Self::Immutable(reader) => reader.lookup_direct_v6(address),
            Self::Live(reader) => reader.lookup_direct_v6(address),
        }
    }

    pub fn direct_cursor_v4(&self, direction: RangeDirection) -> Result<DirectCursorV4<'_>> {
        match self {
            Self::Immutable(reader) => reader.direct_cursor_v4(direction),
            Self::Live(reader) => reader.direct_cursor_v4(direction),
        }
    }

    pub fn direct_cursor_v6(&self, direction: RangeDirection) -> Result<DirectCursorV6<'_>> {
        match self {
            Self::Immutable(reader) => reader.direct_cursor_v6(direction),
            Self::Live(reader) => reader.direct_cursor_v6(direction),
        }
    }

    pub fn feed_cursor(&self) -> Result<FeedCursor<'_>> {
        match self {
            Self::Immutable(reader) => reader.feed_cursor(),
            Self::Live(reader) => reader.feed_cursor(),
        }
    }

    pub fn feed_range_cursor_v4(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV4<'_>> {
        match self {
            Self::Immutable(reader) => reader.feed_range_cursor_v4(name, direction),
            Self::Live(reader) => reader.feed_range_cursor_v4(name, direction),
        }
    }

    pub fn feed_range_cursor_v6(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV6<'_>> {
        match self {
            Self::Immutable(reader) => reader.feed_range_cursor_v6(name, direction),
            Self::Live(reader) => reader.feed_range_cursor_v6(name, direction),
        }
    }

    pub fn lookup_membership_v4(&self, address: Ipv4Key) -> Result<Option<MembershipView<'_>>> {
        match self {
            Self::Immutable(reader) => reader.lookup_membership_v4(address),
            Self::Live(reader) => reader.lookup_membership_v4(address),
        }
    }

    pub fn lookup_membership_v6(&self, address: Ipv6Key) -> Result<Option<MembershipView<'_>>> {
        match self {
            Self::Immutable(reader) => reader.lookup_membership_v6(address),
            Self::Live(reader) => reader.lookup_membership_v6(address),
        }
    }

    pub fn lookup_network_enrichment_v1_v4(
        &self,
        address: Ipv4Key,
    ) -> Result<Option<NetworkEnrichmentV1View<'_>>> {
        match self {
            Self::Immutable(reader) => reader.lookup_network_enrichment_v1_v4(address),
            Self::Live(reader) => reader.lookup_network_enrichment_v1_v4(address),
        }
    }

    pub fn lookup_network_enrichment_v1_v6(
        &self,
        address: Ipv6Key,
    ) -> Result<Option<NetworkEnrichmentV1View<'_>>> {
        match self {
            Self::Immutable(reader) => reader.lookup_network_enrichment_v1_v6(address),
            Self::Live(reader) => reader.lookup_network_enrichment_v1_v6(address),
        }
    }

    pub fn network_enrichment_v1_cursor_v4(
        &self,
        direction: RangeDirection,
    ) -> Result<NetworkEnrichmentV1CursorV4<'_>> {
        match self {
            Self::Immutable(reader) => reader.network_enrichment_v1_cursor_v4(direction),
            Self::Live(reader) => reader.network_enrichment_v1_cursor_v4(direction),
        }
    }

    pub fn network_enrichment_v1_cursor_v6(
        &self,
        direction: RangeDirection,
    ) -> Result<NetworkEnrichmentV1CursorV6<'_>> {
        match self {
            Self::Immutable(reader) => reader.network_enrichment_v1_cursor_v6(direction),
            Self::Live(reader) => reader.network_enrichment_v1_cursor_v6(direction),
        }
    }

    pub fn membership_query(&self) -> Result<MembershipQuery<'_>> {
        match self {
            Self::Immutable(reader) => reader.membership_query(),
            Self::Live(reader) => reader.membership_query(),
        }
    }

    pub fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        match self {
            Self::Immutable(reader) => reader.metadata_json(),
            Self::Live(reader) => reader.metadata_json(),
        }
    }

    /// Close only a registered live reader. Immutable readers have no
    /// registration lease and are dropped by the connection map.
    pub fn close_live(&mut self) -> Result<Option<ReaderCloseResult>> {
        match self {
            Self::Immutable(_) => Ok(None),
            Self::Live(reader) => reader.close().map(Some),
        }
    }
}

/// One canonical address checkpoint used to re-open and seek a cursor.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CursorPoint {
    V4(u32),
    V6(u128),
}

/// The method family that owns a cursor. Handles from one family are not
/// accepted by the other family's next/close methods.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CursorKind {
    Feeds,
    Ranges,
}

/// Logical view represented by a cursor.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CursorView {
    Direct,
    Structured,
    Feed { name: String },
}

/// Cursor progression state. Public Rust cursors borrow their reader, so
/// the connection retains the semantic checkpoint and each `next` operation
/// opens a fresh cursor and seeks to it. This keeps every response bounded.
#[derive(Clone, Debug)]
pub struct CursorValue {
    pub kind: CursorKind,
    pub reader: String,
    pub view: CursorView,
    pub reverse: bool,
    pub point: Option<CursorPoint>,
    pub range_skip: u64,
    pub last_feed_index: Option<u32>,
    pub batch_size: usize,
    pub exhausted: bool,
}

/// Mutable per-connection resources. Closed handles are retained as
/// tombstones so subsequent use can distinguish a closed handle from an
/// unknown one; tombstones do not count against active limits.
#[derive(Default)]
pub struct ConnectionState {
    pub readers: HashMap<String, ReaderValue>,
    pub closed_readers: HashMap<String, ()>,
    pub cursors: HashMap<String, CursorValue>,
    pub closed_cursors: HashMap<String, ()>,
}
