//! Exact complete-destination import statistics.

use crate::cancellation::CancellationToken;
use crate::error::Result;
use crate::workflow::{LogicalChange, WorkflowKind, WorkflowReport};

use super::super::workflow::classify;
use super::super::LiveWriter;
use super::ImportStats;

pub(super) fn prepare(
    writer: &mut LiveWriter,
    stats: ImportStats,
    cancellation: &CancellationToken,
) -> Result<WorkflowReport> {
    let before = writer.core.base_info();
    let after = writer.core.current_info();
    let comparison = writer
        .core
        .compare_maps(cancellation)
        .map_err(|error| writer.abort_after(error))?;
    let map_change = classify(&comparison);
    let logical_change = if stats.created_feeds != 0 || map_change == LogicalChange::Changed {
        LogicalChange::Changed
    } else {
        LogicalChange::NoChange
    };
    Ok(WorkflowReport {
        workflow: WorkflowKind::MembershipImport,
        logical_change,
        input_record_count: stats.input_records,
        input_normalized_interval_count: stats.input_records,
        before_range_record_count: before.range_record_count,
        after_range_record_count: after.range_record_count,
        input_addresses: stats.input_addresses,
        before_addresses: comparison.before,
        after_addresses: comparison.after,
        unchanged_value_addresses: comparison.unchanged,
        changed_value_addresses: comparison.changed,
        added_addresses: comparison.added,
        removed_addresses: comparison.removed,
        source_feed_count: stats.source_feeds,
        matched_feed_count: stats.matched_feeds,
        created_feed_count: stats.created_feeds,
        source_distinct_membership_count: stats.source_memberships,
        translated_membership_count: stats.translated_memberships,
    })
}
