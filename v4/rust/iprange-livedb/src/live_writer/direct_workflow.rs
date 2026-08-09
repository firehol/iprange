//! Complete direct replacement and retention refresh workflows.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_cursor::DirectRange;
use crate::source::{RangeSource, SliceSource};
use crate::workflow::{
    AddressRange, LogicalChange, ReplacementReportInput, WorkflowKind, WorkflowReport,
};

use super::workflow::FinishedState;
use super::workflow::{
    classify, drain_source, require_input_active, require_input_family, require_ordered,
};
use super::{FinishedWorkflow, LiveWriter, PreparedState};

/// Complete unordered direct-map replacement.
#[derive(Debug)]
pub struct DirectReplacement<'a> {
    writer: &'a mut LiveWriter,
    state: ExactDirectState,
}

/// Complete unordered retention-set refresh.
#[derive(Debug)]
pub struct RetentionRefresh<'a> {
    writer: &'a mut LiveWriter,
    state: ExactDirectState,
    refresh_value: u32,
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

    /// Begin a full-snapshot refresh on an exact `retention` database.
    pub fn begin_retention_refresh(
        &mut self,
        refresh_value: u32,
        cancellation: &CancellationToken,
    ) -> Result<RetentionRefresh<'_>> {
        let state = self.begin_retention_state(cancellation)?;
        Ok(RetentionRefresh {
            writer: self,
            state,
            refresh_value,
        })
    }

    pub(crate) fn begin_retention_state(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<ExactDirectState> {
        if self.core.base_info().value_tag != ValueTag::RETENTION {
            return Err(Error::WrongValueTag(
                "retention refresh requires the retention value tag",
            ));
        }
        self.begin_exact_direct_state(WorkflowKind::RetentionRefresh, cancellation)
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

impl<'a> RetentionRefresh<'a> {
    /// Drain one finite unordered IPv4 address source.
    pub fn add_ranges_v4<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv4Key>>,
    {
        self.state
            .add_retention_v4(self.writer, self.refresh_value, source)
    }

    /// Drain one finite unordered IPv6 address source.
    pub fn add_ranges_v6<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv6Key>>,
    {
        self.state
            .add_retention_v6(self.writer, self.refresh_value, source)
    }

    pub fn add_ranges_v4_slice(&mut self, ranges: &[AddressRange<Ipv4Key>]) -> Result<()> {
        self.add_ranges_v4(&mut SliceSource::new(ranges))
    }

    pub fn add_ranges_v6_slice(&mut self, ranges: &[AddressRange<Ipv6Key>]) -> Result<()> {
        self.add_ranges_v6(&mut SliceSource::new(ranges))
    }

    /// Preserve old values on retained coverage and finish the exact refresh.
    pub fn finish_input(self) -> Result<FinishedWorkflow<'a>> {
        let finished = self
            .state
            .finish_retention_state(self.writer, self.refresh_value)?;
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

    pub(crate) fn add_retention_v4<S>(
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

    pub(crate) fn add_retention_v6<S>(
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
        if self.workflow == WorkflowKind::RetentionRefresh {
            return Err(writer.abort_after(Error::WrongState(
                "retention refresh requires its refresh value",
            )));
        }
        let report = self.prepare_replacement_report(writer)?;
        self.complete(writer, report)
    }

    pub(crate) fn finish_retention_state(
        self,
        writer: &mut LiveWriter,
        refresh_value: u32,
    ) -> Result<FinishedState> {
        self.require_active(writer)?;
        if self.workflow != WorkflowKind::RetentionRefresh {
            return Err(writer.abort_after(Error::WrongState(
                "direct replacement has no retention refresh value",
            )));
        }
        let before = writer.core.base_info();
        let cancellation = self.cancellation.clone();
        let merged = writer.mutate(|edit| edit.merge_retention(refresh_value, &cancellation))?;
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
        if report.logical_change == LogicalChange::NoChange {
            writer.discard_draft()?;
            return Ok(FinishedState::NoChange(report));
        }
        let cancellation = self.cancellation;
        writer.mutate(|edit| edit.finish_direct_workflow(&cancellation))?;
        Ok(FinishedState::Changed {
            report,
            state: PreparedState::new(cancellation),
        })
    }
}

#[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
#[path = "../direct_workflow_tests.rs"]
mod tests;
