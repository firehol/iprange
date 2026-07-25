//! Complete direct replacement and retention refresh workflows.

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::draft_store::Draft;
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::random;
use crate::range_cursor::DirectRange;
use crate::source::{RangeSource, SliceSource};
use crate::workflow::compare;
use crate::workflow::{
    AddressRange, LogicalChange, ReplacementReportInput, WorkflowKind, WorkflowReport,
};

use super::workflow::{classify, drain_source, require_ordered};
use super::{FinishedWorkflow, LiveWriter, PreparedWorkflow};

/// Complete unordered direct-map replacement.
#[derive(Debug)]
pub struct DirectReplacement<'a> {
    core: ExactDirect<'a>,
}

/// Complete unordered retention-set refresh.
#[derive(Debug)]
pub struct RetentionRefresh<'a> {
    core: ExactDirect<'a>,
    refresh_value: u32,
}

#[derive(Debug)]
struct ExactDirect<'a> {
    writer: &'a mut LiveWriter,
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
        Ok(DirectReplacement {
            core: self.begin_exact_direct(WorkflowKind::DirectReplacement, cancellation)?,
        })
    }

    /// Begin a full-snapshot refresh on an exact `retention` database.
    pub fn begin_retention_refresh(
        &mut self,
        refresh_value: u32,
        cancellation: &CancellationToken,
    ) -> Result<RetentionRefresh<'_>> {
        if self.base.meta.value_tag != ValueTag::RETENTION {
            return Err(Error::WrongMode(
                "retention refresh requires the retention value tag",
            ));
        }
        Ok(RetentionRefresh {
            core: self.begin_exact_direct(WorkflowKind::RetentionRefresh, cancellation)?,
            refresh_value,
        })
    }

    fn begin_exact_direct<'a>(
        &'a mut self,
        workflow: WorkflowKind,
        cancellation: &CancellationToken,
    ) -> Result<ExactDirect<'a>> {
        self.require_healthy()?;
        if self.base.meta.value_kind != ValueKind::Direct {
            return Err(Error::WrongMode(
                "exact direct workflow requires a direct database",
            ));
        }
        if self.draft.is_some() {
            return Err(Error::WrongState("a writer transaction is already pending"));
        }
        cancellation.check()?;
        let mut draft = Draft::new(self.base.meta, random::nonzero_128()?)?;
        draft.begin_range_workflow()?;
        self.draft = Some(draft);
        Ok(ExactDirect {
            writer: self,
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
        self.core.require_family(AddressFamily::Ipv4)?;
        self.core.drain(source, |writer, range| {
            require_ordered(range.from, range.to)?;
            writer.mutate(|store| store.assign_v4(range.from, range.to, range.value))?;
            Ok(())
        })
    }

    /// Drain one finite IPv6 source in exact callback order.
    pub fn add_ranges_v6<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<DirectRange<Ipv6Key>>,
    {
        self.core.require_family(AddressFamily::Ipv6)?;
        self.core.drain(source, |writer, range| {
            require_ordered(range.from, range.to)?;
            writer.mutate(|store| store.assign_v6(range.from, range.to, range.value))?;
            Ok(())
        })
    }

    pub fn add_ranges_v4_slice(&mut self, ranges: &[DirectRange<Ipv4Key>]) -> Result<()> {
        self.add_ranges_v4(&mut SliceSource::new(ranges))
    }

    pub fn add_ranges_v6_slice(&mut self, ranges: &[DirectRange<Ipv6Key>]) -> Result<()> {
        self.add_ranges_v6(&mut SliceSource::new(ranges))
    }

    /// Finish normalization, comparison, and changed-root preparation.
    pub fn finish_input(self) -> Result<FinishedWorkflow<'a>> {
        self.core.finish(false, None)
    }
}

impl<'a> RetentionRefresh<'a> {
    /// Drain one finite unordered IPv4 address source.
    pub fn add_ranges_v4<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv4Key>>,
    {
        self.core.require_family(AddressFamily::Ipv4)?;
        let value = self.refresh_value;
        self.core.drain(source, move |writer, range| {
            require_ordered(range.from, range.to)?;
            writer.mutate(|store| store.assign_v4(range.from, range.to, value))?;
            Ok(())
        })
    }

    /// Drain one finite unordered IPv6 address source.
    pub fn add_ranges_v6<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv6Key>>,
    {
        self.core.require_family(AddressFamily::Ipv6)?;
        let value = self.refresh_value;
        self.core.drain(source, move |writer, range| {
            require_ordered(range.from, range.to)?;
            writer.mutate(|store| store.assign_v6(range.from, range.to, value))?;
            Ok(())
        })
    }

    pub fn add_ranges_v4_slice(&mut self, ranges: &[AddressRange<Ipv4Key>]) -> Result<()> {
        self.add_ranges_v4(&mut SliceSource::new(ranges))
    }

    pub fn add_ranges_v6_slice(&mut self, ranges: &[AddressRange<Ipv6Key>]) -> Result<()> {
        self.add_ranges_v6(&mut SliceSource::new(ranges))
    }

    /// Preserve old values on retained coverage and finish the exact refresh.
    pub fn finish_input(self) -> Result<FinishedWorkflow<'a>> {
        self.core.require_active()?;
        let base = self.core.writer.base;
        let cancellation = self.core.cancellation.clone();
        let input_meta = self.core.writer.draft.as_ref().unwrap().meta;
        let input_snapshot = (
            input_meta.range_record_count,
            coverage(&self.core.writer.file, &input_meta, &cancellation)
                .map_err(|error| self.core.writer.abort_after(error))?,
        );
        self.core
            .writer
            .mutate(|store| store.preserve_retention_values(&base, &cancellation))?;
        self.core.finish(true, Some(input_snapshot))
    }
}

impl<'a> ExactDirect<'a> {
    fn require_family(&mut self, family: AddressFamily) -> Result<()> {
        self.require_active()?;
        if self.writer.base.meta.address_family != family {
            return Err(self
                .writer
                .abort_after(Error::WrongMode("range family does not match the database")));
        }
        Ok(())
    }

    fn require_active(&self) -> Result<()> {
        self.writer.require_healthy()?;
        if !self
            .writer
            .draft
            .as_ref()
            .is_some_and(Draft::workflow_input_open)
        {
            return Err(Error::WrongState("workflow input is not active"));
        }
        Ok(())
    }

    fn drain<R, S, F>(&mut self, source: &mut S, mut apply: F) -> Result<()>
    where
        R: Copy,
        S: RangeSource<R>,
        F: FnMut(&mut LiveWriter, R) -> Result<()>,
    {
        self.require_active()?;
        let result = drain_source(
            source,
            &self.cancellation,
            &mut self.input_records,
            |record| apply(self.writer, record),
        );
        result.map_err(|error| {
            if self.writer.draft.is_some() {
                self.writer.abort_after(error)
            } else {
                error
            }
        })
    }

    fn finish(
        mut self,
        retention_prepared: bool,
        input_snapshot: Option<(u64, Cardinality129)>,
    ) -> Result<FinishedWorkflow<'a>> {
        let base = self.writer.base;
        let report = self.prepare_report(&base, retention_prepared, input_snapshot)?;
        self.complete(base, report)
    }

    fn prepare_report(
        &mut self,
        base: &crate::bootstrap::Bootstrap,
        retention_prepared: bool,
        input_snapshot: Option<(u64, Cardinality129)>,
    ) -> Result<WorkflowReport> {
        self.require_active()?;
        self.require_retention_prepared(retention_prepared)?;
        let (input_intervals, input_addresses) = self.input_summary(input_snapshot)?;
        let after = self.writer.draft.as_ref().unwrap().meta;
        let comparison = compare_maps(&self.writer.file, base, &after, &self.cancellation)
            .map_err(|error| self.writer.abort_after(error))?;
        let logical_change = classify(&comparison);
        Ok(WorkflowReport::replacement(
            ReplacementReportInput {
                workflow: self.workflow,
                logical_change,
                input_record_count: self.input_records,
                input_normalized_interval_count: input_intervals,
                before_range_record_count: base.meta.range_record_count,
                after_range_record_count: after.range_record_count,
                input_addresses,
            },
            comparison,
        ))
    }

    fn input_summary(
        &mut self,
        supplied: Option<(u64, Cardinality129)>,
    ) -> Result<(u64, Cardinality129)> {
        if let Some(snapshot) = supplied {
            return Ok(snapshot);
        }
        let meta = self.writer.draft.as_ref().unwrap().meta;
        let addresses = coverage(&self.writer.file, &meta, &self.cancellation)
            .map_err(|error| self.writer.abort_after(error))?;
        Ok((meta.range_record_count, addresses))
    }

    fn require_retention_prepared(&mut self, prepared: bool) -> Result<()> {
        if self.workflow != WorkflowKind::RetentionRefresh || prepared {
            return Ok(());
        }
        Err(self
            .writer
            .abort_after(Error::WrongState("retention values were not prepared")))
    }

    fn complete(
        self,
        base: crate::bootstrap::Bootstrap,
        report: WorkflowReport,
    ) -> Result<FinishedWorkflow<'a>> {
        if report.logical_change == LogicalChange::NoChange {
            self.writer.discard_draft()?;
            return Ok(FinishedWorkflow::NoChange(report));
        }
        let cancellation = self.cancellation;
        self.writer
            .mutate(|store| store.finish_direct_workflow(&base, &cancellation))?;
        Ok(FinishedWorkflow::Changed(PreparedWorkflow::new(
            self.writer,
            report,
            cancellation,
        )))
    }
}

fn coverage(
    file: &std::fs::File,
    meta: &crate::contract::MetaV4,
    cancellation: &CancellationToken,
) -> Result<Cardinality129> {
    match meta.address_family {
        AddressFamily::Ipv4 => compare::coverage::<Ipv4Key>(file, meta, cancellation),
        AddressFamily::Ipv6 => compare::coverage::<Ipv6Key>(file, meta, cancellation),
    }
}

fn compare_maps(
    file: &std::fs::File,
    before: &crate::bootstrap::Bootstrap,
    after: &crate::contract::MetaV4,
    cancellation: &CancellationToken,
) -> Result<crate::workflow::Comparison> {
    match after.address_family {
        AddressFamily::Ipv4 => compare::maps::<Ipv4Key>(file, before, after, cancellation),
        AddressFamily::Ipv6 => compare::maps::<Ipv6Key>(file, before, after, cancellation),
    }
}

#[cfg(test)]
#[path = "../direct_workflow_tests.rs"]
mod tests;
