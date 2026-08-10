//! Public last-seen history projection requests and exact reports.

use crate::cardinality::Cardinality129;
use crate::feed::FeedName;
use crate::workflow::LogicalChange;

/// One named history feed containing addresses whose `last_seen` exceeds `cutoff`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct HistoryWindow {
    pub feed_name: FeedName,
    pub cutoff: u32,
}

/// Exact before/after statistics for one projected history feed.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct HistoryWindowReport {
    pub feed_name: FeedName,
    pub cutoff: u32,
    pub created: bool,
    pub before_interval_count: u64,
    pub after_interval_count: u64,
    pub before_addresses: Cardinality129,
    pub after_addresses: Cardinality129,
    pub unchanged_addresses: Cardinality129,
    pub added_addresses: Cardinality129,
    pub removed_addresses: Cardinality129,
}

/// Exact aggregate and per-window result of one last-seen projection.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HistoryProjectionReport {
    pub logical_change: LogicalChange,
    pub source_range_count: u64,
    pub source_addresses: Cardinality129,
    pub created_feed_count: u64,
    pub before_interval_count: u64,
    pub after_interval_count: u64,
    pub before_addresses: Cardinality129,
    pub after_addresses: Cardinality129,
    pub unchanged_addresses: Cardinality129,
    pub added_addresses: Cardinality129,
    pub removed_addresses: Cardinality129,
    pub windows: Box<[HistoryWindowReport]>,
}
