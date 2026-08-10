//! Shared semantic input and terminal workflow results.

use crate::cardinality::Cardinality129;
use crate::error::Result;

#[path = "workflow/compare.rs"]
pub(crate) mod compare;

/// One value-free inclusive input interval.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AddressRange<K> {
    pub from: K,
    pub to: K,
}

/// One first-seen interval removed by a complete refresh.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FirstSeenRemoval<K> {
    pub from: K,
    pub to: K,
    pub first_seen: u32,
    pub addresses: Cardinality129,
}

/// Synchronous consumer for bounded batches of first-seen removals.
pub trait FirstSeenRemovalSink<K> {
    fn removals(&mut self, batch: &[FirstSeenRemoval<K>]) -> Result<()>;
}

impl<K, F> FirstSeenRemovalSink<K> for F
where
    F: FnMut(&[FirstSeenRemoval<K>]) -> Result<()>,
{
    fn removals(&mut self, batch: &[FirstSeenRemoval<K>]) -> Result<()> {
        self(batch)
    }
}

/// High-level operation that produced a report.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WorkflowKind {
    CreateFeed,
    ReplaceFeed,
    DirectReplacement,
    FirstSeenRefresh,
    LastSeenRefresh,
    MembershipImport,
}

/// Whether the complete requested state differs from the committed state.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LogicalChange {
    Changed,
    NoChange,
}

/// Exact semantic statistics produced before publication.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct WorkflowReport {
    pub workflow: WorkflowKind,
    pub logical_change: LogicalChange,
    pub input_record_count: u64,
    pub input_normalized_interval_count: u64,
    pub before_range_record_count: u64,
    pub after_range_record_count: u64,
    pub input_addresses: Cardinality129,
    pub before_addresses: Cardinality129,
    pub after_addresses: Cardinality129,
    pub unchanged_value_addresses: Cardinality129,
    pub changed_value_addresses: Cardinality129,
    pub added_addresses: Cardinality129,
    pub removed_addresses: Cardinality129,
    pub source_feed_count: u64,
    pub matched_feed_count: u64,
    pub created_feed_count: u64,
    pub source_distinct_membership_count: u64,
    pub translated_membership_count: u64,
}

impl WorkflowReport {
    pub(crate) fn replacement(input: ReplacementReportInput, comparison: Comparison) -> Self {
        Self {
            workflow: input.workflow,
            logical_change: input.logical_change,
            input_record_count: input.input_record_count,
            input_normalized_interval_count: input.input_normalized_interval_count,
            before_range_record_count: input.before_range_record_count,
            after_range_record_count: input.after_range_record_count,
            input_addresses: input.input_addresses,
            before_addresses: comparison.before,
            after_addresses: comparison.after,
            unchanged_value_addresses: comparison.unchanged,
            changed_value_addresses: comparison.changed,
            added_addresses: comparison.added,
            removed_addresses: comparison.removed,
            source_feed_count: 0,
            matched_feed_count: 0,
            created_feed_count: 0,
            source_distinct_membership_count: 0,
            translated_membership_count: 0,
        }
    }
}

pub(crate) struct ReplacementReportInput {
    pub(crate) workflow: WorkflowKind,
    pub(crate) logical_change: LogicalChange,
    pub(crate) input_record_count: u64,
    pub(crate) input_normalized_interval_count: u64,
    pub(crate) before_range_record_count: u64,
    pub(crate) after_range_record_count: u64,
    pub(crate) input_addresses: Cardinality129,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct Comparison {
    pub(crate) before: Cardinality129,
    pub(crate) after: Cardinality129,
    pub(crate) unchanged: Cardinality129,
    pub(crate) changed: Cardinality129,
    pub(crate) added: Cardinality129,
    pub(crate) removed: Cardinality129,
}
