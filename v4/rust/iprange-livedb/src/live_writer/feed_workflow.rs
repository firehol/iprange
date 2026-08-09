//! Complete named-feed creation and replacement workflows.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, ValueKind};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::source::{RangeSource, SliceSource};
use crate::workflow::{
    AddressRange, LogicalChange, ReplacementReportInput, WorkflowKind, WorkflowReport,
};
use crate::writer_core::{FeedMerge, MembershipHandle};

use super::workflow::FinishedState;
use super::workflow::{
    classify, drain_source, require_input_active, require_input_family, require_ordered,
};
use super::{FinishedWorkflow, LiveWriter, PreparedState};

/// Complete creation of one exact named feed.
#[derive(Debug)]
pub struct CreateFeed<'a> {
    writer: &'a mut LiveWriter,
    state: ExactFeedState,
}

/// Complete replacement of one exact named feed.
#[derive(Debug)]
pub struct ReplaceFeed<'a> {
    writer: &'a mut LiveWriter,
    state: ExactFeedState,
}

/// Borrow-free exact-feed workflow state shared with language bindings.
#[derive(Debug)]
pub(crate) struct ExactFeedState {
    cancellation: CancellationToken,
    workflow: WorkflowKind,
    create: bool,
    member: MembershipHandle,
    input_records: u64,
}

impl LiveWriter {
    /// Begin creation of one absent named feed on a clean membership writer.
    pub fn begin_create_feed(
        &mut self,
        name: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<CreateFeed<'_>> {
        let state = self.begin_exact_feed_state(name, true, cancellation)?;
        Ok(CreateFeed {
            writer: self,
            state,
        })
    }

    /// Begin complete replacement of one existing named feed.
    pub fn begin_replace_feed(
        &mut self,
        name: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<ReplaceFeed<'_>> {
        let state = self.begin_exact_feed_state(name, false, cancellation)?;
        Ok(ReplaceFeed {
            writer: self,
            state,
        })
    }

    pub(crate) fn begin_exact_feed_state(
        &mut self,
        name: FeedName,
        create: bool,
        cancellation: &CancellationToken,
    ) -> Result<ExactFeedState> {
        let existing = self.check_feed_precondition(name, create, cancellation)?;
        self.start_feed_workflow_draft()?;
        let member =
            self.mutate(|store| setup_feed(store, name, existing, create, cancellation))?;
        Ok(ExactFeedState {
            cancellation: cancellation.clone(),
            workflow: if create {
                WorkflowKind::CreateFeed
            } else {
                WorkflowKind::ReplaceFeed
            },
            create,
            member,
            input_records: 0,
        })
    }

    fn check_feed_precondition(
        &self,
        name: FeedName,
        create: bool,
        cancellation: &CancellationToken,
    ) -> Result<Option<FeedEntry>> {
        self.require_feed_workflow_ready()?;
        let existing = self.core.lookup_base_feed(&name)?;
        require_feed_precondition(existing, create)?;
        cancellation.check()?;
        Ok(existing)
    }

    pub(super) fn start_feed_workflow_draft(&mut self) -> Result<()> {
        self.core.begin_membership_workflow()
    }

    pub(super) fn require_feed_workflow_ready(&self) -> Result<()> {
        self.require_healthy()?;
        if self.core.base_info().value_kind != ValueKind::Membership {
            return Err(Error::WrongValueKind(
                "named-feed workflow requires a membership database",
            ));
        }
        if self.core.has_draft() {
            return Err(Error::WrongState("a writer transaction is already pending"));
        }
        Ok(())
    }
}

impl<'a> CreateFeed<'a> {
    pub fn add_ranges_v4<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv4Key>>,
    {
        self.state
            .add_ranges(self.writer, AddressFamily::Ipv4, source)
    }

    pub fn add_ranges_v6<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv6Key>>,
    {
        self.state
            .add_ranges(self.writer, AddressFamily::Ipv6, source)
    }

    pub fn add_ranges_v4_slice(&mut self, ranges: &[AddressRange<Ipv4Key>]) -> Result<()> {
        self.add_ranges_v4(&mut SliceSource::new(ranges))
    }

    pub fn add_ranges_v6_slice(&mut self, ranges: &[AddressRange<Ipv6Key>]) -> Result<()> {
        self.add_ranges_v6(&mut SliceSource::new(ranges))
    }

    pub fn finish_input(self) -> Result<FinishedWorkflow<'a>> {
        self.state.finish(self.writer)
    }
}

impl<'a> ReplaceFeed<'a> {
    pub fn add_ranges_v4<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv4Key>>,
    {
        self.state
            .add_ranges(self.writer, AddressFamily::Ipv4, source)
    }

    pub fn add_ranges_v6<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv6Key>>,
    {
        self.state
            .add_ranges(self.writer, AddressFamily::Ipv6, source)
    }

    pub fn add_ranges_v4_slice(&mut self, ranges: &[AddressRange<Ipv4Key>]) -> Result<()> {
        self.add_ranges_v4(&mut SliceSource::new(ranges))
    }

    pub fn add_ranges_v6_slice(&mut self, ranges: &[AddressRange<Ipv6Key>]) -> Result<()> {
        self.add_ranges_v6(&mut SliceSource::new(ranges))
    }

    pub fn finish_input(self) -> Result<FinishedWorkflow<'a>> {
        self.state.finish(self.writer)
    }
}

impl ExactFeedState {
    pub(crate) fn add_ranges<K, S>(
        &mut self,
        writer: &mut LiveWriter,
        family: AddressFamily,
        source: &mut S,
    ) -> Result<()>
    where
        K: IpKey,
        S: RangeSource<AddressRange<K>>,
    {
        self.require_family(writer, family)?;
        writer.mutate(|store| {
            drain_source(
                source,
                &self.cancellation,
                &mut self.input_records,
                |range| {
                    require_ordered(range.from, range.to)?;
                    store.add_feed_coverage(range.from, range.to)?;
                    Ok(())
                },
            )
        })
    }

    pub(crate) fn finish<'a>(self, writer: &'a mut LiveWriter) -> Result<FinishedWorkflow<'a>> {
        let finished = self.finish_state(writer)?;
        Ok(finished.bind(writer))
    }

    pub(crate) fn finish_state(self, writer: &mut LiveWriter) -> Result<FinishedState> {
        self.require_active(writer)?;
        let cancellation = self.cancellation.clone();
        let merged =
            writer.mutate(|store| store.merge_feed(self.member, self.create, &cancellation))?;
        writer.mutate(|store| store.finalize_membership_workflow(&cancellation))?;
        let report = self.prepare_report(merged)?;
        if report.logical_change == LogicalChange::NoChange {
            writer.discard_draft()?;
            return Ok(FinishedState::NoChange(report));
        }
        writer.mutate(|store| store.finish_membership_workflow(&cancellation))?;
        Ok(FinishedState::Changed {
            report,
            state: PreparedState::new(cancellation),
        })
    }

    fn prepare_report(&self, merged: FeedMerge) -> Result<WorkflowReport> {
        let logical_change = if self.workflow == WorkflowKind::CreateFeed {
            LogicalChange::Changed
        } else {
            classify(&merged.comparison.comparison)
        };
        Ok(WorkflowReport::replacement(
            ReplacementReportInput {
                workflow: self.workflow,
                logical_change,
                input_record_count: self.input_records,
                input_normalized_interval_count: merged.input_intervals,
                before_range_record_count: merged.comparison.before_intervals,
                after_range_record_count: merged.comparison.after_intervals,
                input_addresses: merged.input_addresses,
            },
            merged.comparison.comparison,
        ))
    }

    fn require_family(&mut self, writer: &mut LiveWriter, family: AddressFamily) -> Result<()> {
        require_input_family(writer, family)
    }

    pub(crate) fn require_active(&self, writer: &LiveWriter) -> Result<()> {
        require_input_active(writer)
    }
}

fn setup_feed(
    store: &mut crate::writer_core::WriterEdit<'_>,
    name: FeedName,
    existing: Option<FeedEntry>,
    create: bool,
    cancellation: &CancellationToken,
) -> Result<MembershipHandle> {
    cancellation.check()?;
    let feed = select_feed(store, name, existing, create)?;
    let member = store.add_feed_to_membership(MembershipHandle::empty(), feed)?;
    cancellation.check()?;
    Ok(member)
}

fn select_feed(
    store: &mut crate::writer_core::WriterEdit<'_>,
    name: FeedName,
    existing: Option<FeedEntry>,
    create: bool,
) -> Result<FeedEntry> {
    if !create {
        return existing.ok_or(Error::Corrupt("replacement feed disappeared"));
    }
    store.insert_feed(name)
}

fn require_feed_precondition(existing: Option<FeedEntry>, create: bool) -> Result<()> {
    match (existing, create) {
        (Some(_), true) => Err(Error::NameExists),
        (None, false) => Err(Error::NameNotFound),
        _ => Ok(()),
    }
}

#[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
#[path = "../feed_workflow_tests.rs"]
mod tests;
