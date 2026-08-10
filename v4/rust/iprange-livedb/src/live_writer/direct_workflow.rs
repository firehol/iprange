//! Complete direct replacement and timestamp refresh workflows.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, DirectSemantic, ValueKind};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_cursor::DirectRange;
use crate::source::{RangeSource, SliceSource};
use crate::workflow::{
    AddressRange, FirstSeenRemovalSink, ReplacementReportInput, WorkflowKind, WorkflowReport,
};

use super::workflow::FinishedState;
use super::workflow::{
    classify, complete_workflow, drain_source, require_input_active, require_input_family,
    require_ordered,
};
use super::{FinishedWorkflow, LiveWriter};

/// Complete unordered direct-map replacement.
#[derive(Debug)]
pub struct DirectReplacement<'a> {
    writer: &'a mut LiveWriter,
    state: ExactDirectState,
}

/// Complete unordered first-seen refresh.
#[derive(Debug)]
pub struct FirstSeenRefresh<'a> {
    writer: &'a mut LiveWriter,
    state: ExactDirectState,
    refresh_value: u32,
}

/// Complete unordered last-seen refresh.
#[derive(Debug)]
pub struct LastSeenRefresh<'a> {
    writer: &'a mut LiveWriter,
    state: ExactDirectState,
    refresh_value: u32,
    cutoff: u32,
}

/// Borrow-free exact-direct workflow state shared with language bindings.
#[derive(Debug)]
pub(crate) struct ExactDirectState {
    cancellation: CancellationToken,
    workflow: WorkflowKind,
    input_records: u64,
}

impl LiveWriter {
    /// Begin a complete direct-map replacement on a clean direct writer.
    pub fn begin_direct_replacement(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<DirectReplacement<'_>> {
        let state = self.begin_exact_direct_state(WorkflowKind::DirectReplacement, cancellation)?;
        Ok(DirectReplacement {
            writer: self,
            state,
        })
    }

    /// Begin a full-snapshot refresh on an exact `first_seen` database.
    pub fn begin_first_seen_refresh(
        &mut self,
        refresh_value: u32,
        cancellation: &CancellationToken,
    ) -> Result<FirstSeenRefresh<'_>> {
        let state = self.begin_timestamp_state(
            DirectSemantic::FirstSeen,
            WorkflowKind::FirstSeenRefresh,
            cancellation,
        )?;
        Ok(FirstSeenRefresh {
            writer: self,
            state,
            refresh_value,
        })
    }

    /// Begin a full-snapshot refresh on an exact `last_seen` database.
    pub fn begin_last_seen_refresh(
        &mut self,
        refresh_value: u32,
        cutoff: u32,
        cancellation: &CancellationToken,
    ) -> Result<LastSeenRefresh<'_>> {
        let state = self.begin_timestamp_state(
            DirectSemantic::LastSeen,
            WorkflowKind::LastSeenRefresh,
            cancellation,
        )?;
        Ok(LastSeenRefresh {
            writer: self,
            state,
            refresh_value,
            cutoff,
        })
    }

    pub(crate) fn begin_timestamp_state(
        &mut self,
        semantic: DirectSemantic,
        workflow: WorkflowKind,
        cancellation: &CancellationToken,
    ) -> Result<ExactDirectState> {
        let info = self.core.base_info();
        if info.value_kind != ValueKind::Direct {
            return Err(Error::WrongValueKind(
                "timestamp refresh requires a direct database",
            ));
        }
        if info.value_tag.direct_semantic() != semantic {
            return Err(Error::WrongValueTag(
                "timestamp refresh requires its exact value tag",
            ));
        }
        self.begin_exact_direct_state(workflow, cancellation)
    }

    pub(crate) fn begin_exact_direct_state(
        &mut self,
        workflow: WorkflowKind,
        cancellation: &CancellationToken,
    ) -> Result<ExactDirectState> {
        self.require_healthy()?;
        if self.core.base_info().value_kind != ValueKind::Direct {
            return Err(Error::WrongValueKind(
                "exact direct workflow requires a direct database",
            ));
        }
        cancellation.check()?;
        self.core.begin_range_workflow()?;
        Ok(ExactDirectState {
            cancellation: cancellation.clone(),
            workflow,
            input_records: 0,
        })
    }
}

impl<'a> DirectReplacement<'a> {
    /// Drain one finite IPv4 source in exact callback order.
    pub fn add_ranges_v4<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<DirectRange<Ipv4Key>>,
    {
        self.state.add_direct_v4(self.writer, source)
    }

    /// Drain one finite IPv6 source in exact callback order.
    pub fn add_ranges_v6<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<DirectRange<Ipv6Key>>,
    {
        self.state.add_direct_v6(self.writer, source)
    }

    pub fn add_ranges_v4_slice(&mut self, ranges: &[DirectRange<Ipv4Key>]) -> Result<()> {
        self.add_ranges_v4(&mut SliceSource::new(ranges))
    }

    pub fn add_ranges_v6_slice(&mut self, ranges: &[DirectRange<Ipv6Key>]) -> Result<()> {
        self.add_ranges_v6(&mut SliceSource::new(ranges))
    }

    /// Finish normalization, comparison, and changed-root preparation.
    pub fn finish_input(self) -> Result<FinishedWorkflow<'a>> {
        let finished = self.state.finish_replacement_state(self.writer)?;
        Ok(finished.bind(self.writer))
    }
}

impl<'a> FirstSeenRefresh<'a> {
    /// Drain one finite unordered IPv4 address source.
    pub fn add_ranges_v4<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv4Key>>,
    {
        self.state
            .add_timestamp_v4(self.writer, self.refresh_value, source)
    }

    /// Drain one finite unordered IPv6 address source.
    pub fn add_ranges_v6<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv6Key>>,
    {
        self.state
            .add_timestamp_v6(self.writer, self.refresh_value, source)
    }

    pub fn add_ranges_v4_slice(&mut self, ranges: &[AddressRange<Ipv4Key>]) -> Result<()> {
        self.add_ranges_v4(&mut SliceSource::new(ranges))
    }

    pub fn add_ranges_v6_slice(&mut self, ranges: &[AddressRange<Ipv6Key>]) -> Result<()> {
        self.add_ranges_v6(&mut SliceSource::new(ranges))
    }

    /// Preserve old values on current coverage and finish the exact refresh.
    pub fn finish_input(self) -> Result<FinishedWorkflow<'a>> {
        let finished = self
            .state
            .finish_first_seen_state(self.writer, self.refresh_value)?;
        Ok(finished.bind(self.writer))
    }

    /// Finish while streaming bounded batches of removed IPv4 intervals.
    pub fn finish_input_with_removals_v4<S>(self, sink: &mut S) -> Result<FinishedWorkflow<'a>>
    where
        S: FirstSeenRemovalSink<Ipv4Key>,
    {
        let finished = self.state.finish_first_seen_with_removals_v4_state(
            self.writer,
            self.refresh_value,
            sink,
        )?;
        Ok(finished.bind(self.writer))
    }

    /// Finish while streaming bounded batches of removed IPv6 intervals.
    pub fn finish_input_with_removals_v6<S>(self, sink: &mut S) -> Result<FinishedWorkflow<'a>>
    where
        S: FirstSeenRemovalSink<Ipv6Key>,
    {
        let finished = self.state.finish_first_seen_with_removals_v6_state(
            self.writer,
            self.refresh_value,
            sink,
        )?;
        Ok(finished.bind(self.writer))
    }
}

impl<'a> LastSeenRefresh<'a> {
    /// Drain one finite unordered IPv4 address source.
    pub fn add_ranges_v4<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv4Key>>,
    {
        self.state
            .add_timestamp_v4(self.writer, self.refresh_value, source)
    }

    /// Drain one finite unordered IPv6 address source.
    pub fn add_ranges_v6<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv6Key>>,
    {
        self.state
            .add_timestamp_v6(self.writer, self.refresh_value, source)
    }

    pub fn add_ranges_v4_slice(&mut self, ranges: &[AddressRange<Ipv4Key>]) -> Result<()> {
        self.add_ranges_v4(&mut SliceSource::new(ranges))
    }

    pub fn add_ranges_v6_slice(&mut self, ranges: &[AddressRange<Ipv6Key>]) -> Result<()> {
        self.add_ranges_v6(&mut SliceSource::new(ranges))
    }

    /// Refresh current coverage, retain recent absence, and expire old absence.
    pub fn finish_input(self) -> Result<FinishedWorkflow<'a>> {
        let finished =
            self.state
                .finish_last_seen_state(self.writer, self.refresh_value, self.cutoff)?;
        Ok(finished.bind(self.writer))
    }
}

impl ExactDirectState {
    pub(crate) fn add_direct_v4<S>(&mut self, writer: &mut LiveWriter, source: &mut S) -> Result<()>
    where
        S: RangeSource<DirectRange<Ipv4Key>>,
    {
        self.require_family(writer, AddressFamily::Ipv4)?;
        self.drain(writer, source, |edit, range| {
            require_ordered(range.from, range.to)?;
            edit.assign_v4(range.from, range.to, range.value)?;
            Ok(())
        })
    }

    pub(crate) fn add_direct_v6<S>(&mut self, writer: &mut LiveWriter, source: &mut S) -> Result<()>
    where
        S: RangeSource<DirectRange<Ipv6Key>>,
    {
        self.require_family(writer, AddressFamily::Ipv6)?;
        self.drain(writer, source, |edit, range| {
            require_ordered(range.from, range.to)?;
            edit.assign_v6(range.from, range.to, range.value)?;
            Ok(())
        })
    }

    pub(crate) fn add_timestamp_v4<S>(
        &mut self,
        writer: &mut LiveWriter,
        value: u32,
        source: &mut S,
    ) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv4Key>>,
    {
        self.require_family(writer, AddressFamily::Ipv4)?;
        self.drain(writer, source, move |edit, range| {
            require_ordered(range.from, range.to)?;
            edit.assign_v4(range.from, range.to, value)?;
            Ok(())
        })
    }

    pub(crate) fn add_timestamp_v6<S>(
        &mut self,
        writer: &mut LiveWriter,
        value: u32,
        source: &mut S,
    ) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv6Key>>,
    {
        self.require_family(writer, AddressFamily::Ipv6)?;
        self.drain(writer, source, move |edit, range| {
            require_ordered(range.from, range.to)?;
            edit.assign_v6(range.from, range.to, value)?;
            Ok(())
        })
    }

    pub(crate) fn require_family(
        &mut self,
        writer: &mut LiveWriter,
        family: AddressFamily,
    ) -> Result<()> {
        require_input_family(writer, family)
    }

    pub(crate) fn require_active(&self, writer: &LiveWriter) -> Result<()> {
        require_input_active(writer)
    }

    pub(crate) fn drain<R, S, F>(
        &mut self,
        writer: &mut LiveWriter,
        source: &mut S,
        mut apply: F,
    ) -> Result<()>
    where
        R: Copy,
        S: RangeSource<R>,
        F: FnMut(&mut crate::writer_core::WriterEdit<'_>, R) -> Result<()>,
    {
        self.require_active(writer)?;
        writer.mutate(|store| {
            drain_source(
                source,
                &self.cancellation,
                &mut self.input_records,
                |record| apply(store, record),
            )
        })
    }

    pub(crate) fn finish_replacement_state(
        mut self,
        writer: &mut LiveWriter,
    ) -> Result<FinishedState> {
        if matches!(
            self.workflow,
            WorkflowKind::FirstSeenRefresh | WorkflowKind::LastSeenRefresh
        ) {
            return Err(writer.abort_after(Error::WrongState(
                "timestamp refresh requires its refresh parameters",
            )));
        }
        let report = self.prepare_replacement_report(writer)?;
        self.complete(writer, report)
    }

    pub(crate) fn finish_first_seen_state(
        self,
        writer: &mut LiveWriter,
        refresh_value: u32,
    ) -> Result<FinishedState> {
        self.require_first_seen(writer)?;
        self.finish_timestamp(writer, |edit, cancellation| {
            edit.merge_first_seen(refresh_value, cancellation)
        })
    }

    pub(crate) fn finish_first_seen_with_removals_v4_state<S>(
        mut self,
        writer: &mut LiveWriter,
        refresh_value: u32,
        sink: &mut S,
    ) -> Result<FinishedState>
    where
        S: FirstSeenRemovalSink<Ipv4Key>,
    {
        self.require_first_seen(writer)?;
        self.require_family(writer, AddressFamily::Ipv4)?;
        self.finish_timestamp(writer, |edit, cancellation| {
            edit.merge_first_seen_v4_with_removals(refresh_value, sink, cancellation)
        })
    }

    pub(crate) fn finish_first_seen_with_removals_v6_state<S>(
        mut self,
        writer: &mut LiveWriter,
        refresh_value: u32,
        sink: &mut S,
    ) -> Result<FinishedState>
    where
        S: FirstSeenRemovalSink<Ipv6Key>,
    {
        self.require_first_seen(writer)?;
        self.require_family(writer, AddressFamily::Ipv6)?;
        self.finish_timestamp(writer, |edit, cancellation| {
            edit.merge_first_seen_v6_with_removals(refresh_value, sink, cancellation)
        })
    }

    pub(crate) fn finish_last_seen_state(
        self,
        writer: &mut LiveWriter,
        refresh_value: u32,
        cutoff: u32,
    ) -> Result<FinishedState> {
        self.require_active(writer)?;
        if self.workflow != WorkflowKind::LastSeenRefresh {
            return Err(
                writer.abort_after(Error::WrongState("workflow is not a last-seen refresh"))
            );
        }
        self.finish_timestamp(writer, |edit, cancellation| {
            edit.merge_last_seen(refresh_value, cutoff, cancellation)
        })
    }

    fn require_first_seen(&self, writer: &mut LiveWriter) -> Result<()> {
        self.require_active(writer)?;
        if self.workflow != WorkflowKind::FirstSeenRefresh {
            return Err(
                writer.abort_after(Error::WrongState("workflow is not a first-seen refresh"))
            );
        }
        Ok(())
    }

    fn finish_timestamp<F>(self, writer: &mut LiveWriter, merge: F) -> Result<FinishedState>
    where
        F: FnOnce(
            &mut crate::writer_core::WriterEdit<'_>,
            &CancellationToken,
        ) -> Result<crate::writer_core::TimestampMerge>,
    {
        let before = writer.core.base_info();
        let cancellation = self.cancellation.clone();
        let merged = writer.mutate(|edit| merge(edit, &cancellation))?;
        let after = writer.core.current_info();
        let logical_change = classify(&merged.comparison);
        let report = WorkflowReport::replacement(
            ReplacementReportInput {
                workflow: self.workflow,
                logical_change,
                input_record_count: self.input_records,
                input_normalized_interval_count: merged.input_intervals,
                before_range_record_count: before.range_record_count,
                after_range_record_count: after.range_record_count,
                input_addresses: merged.input_addresses,
            },
            merged.comparison,
        );
        self.complete(writer, report)
    }

    fn prepare_replacement_report(&mut self, writer: &mut LiveWriter) -> Result<WorkflowReport> {
        self.require_active(writer)?;
        let before = writer.core.base_info();
        let after = writer.core.current_info();
        let comparison = writer
            .core
            .compare_maps(&self.cancellation)
            .map_err(|error| writer.abort_after(error))?;
        let logical_change = classify(&comparison);
        Ok(WorkflowReport::replacement(
            ReplacementReportInput {
                workflow: self.workflow,
                logical_change,
                input_record_count: self.input_records,
                input_normalized_interval_count: after.range_record_count,
                before_range_record_count: before.range_record_count,
                after_range_record_count: after.range_record_count,
                input_addresses: comparison.after,
            },
            comparison,
        ))
    }

    fn complete(self, writer: &mut LiveWriter, report: WorkflowReport) -> Result<FinishedState> {
        complete_workflow(writer, report, self.cancellation, |edit, cancellation| {
            edit.finish_direct_workflow(cancellation)
        })
    }
}

#[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
#[path = "../direct_workflow_tests.rs"]
mod tests;
