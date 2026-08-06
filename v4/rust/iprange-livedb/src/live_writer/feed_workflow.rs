//! Complete named-feed creation and replacement workflows.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MembershipOperation, ValueKind};
use crate::draft_store::{Draft, DraftStore};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::feed_catalog;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::membership_dictionary::Interned;
use crate::random;
use crate::source::{RangeSource, SliceSource};
use crate::workflow::compare;
use crate::workflow::{
    AddressRange, LogicalChange, ReplacementReportInput, WorkflowKind, WorkflowReport,
};

use super::workflow::FinishedState;
use super::workflow::{classify, drain_source, require_ordered};
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
    feed: FeedEntry,
    member: Interned,
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
        let family = self.base.meta.address_family;
        let setup =
            self.mutate(|store| setup_feed(store, name, existing, create, family, cancellation))?;
        Ok(ExactFeedState {
            cancellation: cancellation.clone(),
            workflow: if create {
                WorkflowKind::CreateFeed
            } else {
                WorkflowKind::ReplaceFeed
            },
            feed: setup.0,
            member: setup.1,
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
        let existing = feed_catalog::lookup(&self.file, &self.base.meta, &name)?;
        require_feed_precondition(existing, create)?;
        cancellation.check()?;
        Ok(existing)
    }

    pub(super) fn start_feed_workflow_draft(&mut self) -> Result<()> {
        let mut draft = Draft::new(self.base.meta, random::nonzero_128()?)?;
        draft.begin_membership_workflow()?;
        self.draft = Some(draft);
        Ok(())
    }

    pub(super) fn require_feed_workflow_ready(&self) -> Result<()> {
        self.require_healthy()?;
        if self.base.meta.value_kind != ValueKind::Membership {
            return Err(Error::WrongValueKind(
                "named-feed workflow requires a membership database",
            ));
        }
        if self.draft.is_some() {
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
        let id = self.member.id;
        let words = self.member.word_count;
        let cancellation = self.cancellation.clone();
        let result = drain_source(
            source,
            &self.cancellation,
            &mut self.input_records,
            |range| {
                require_ordered(range.from, range.to)?;
                writer.mutate(|store| {
                    store.apply_membership_cancellable(
                        range.from,
                        range.to,
                        id,
                        words,
                        MembershipOperation::Union,
                        &mut || cancellation.check(),
                    )
                })?;
                Ok(())
            },
        );
        result.map_err(|error| {
            if writer.draft.is_some() {
                writer.abort_after(error)
            } else {
                error
            }
        })
    }

    pub(crate) fn finish<'a>(self, writer: &'a mut LiveWriter) -> Result<FinishedWorkflow<'a>> {
        let finished = self.finish_state(writer)?;
        Ok(finished.bind(writer))
    }

    pub(crate) fn finish_state(mut self, writer: &mut LiveWriter) -> Result<FinishedState> {
        self.require_active(writer)?;
        let cancellation = self.cancellation.clone();
        writer.mutate(|store| store.finalize_membership_workflow(&cancellation))?;
        writer.flush_draft_pages()?;
        let report = self.prepare_report(writer)?;
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

    fn prepare_report(&mut self, writer: &mut LiveWriter) -> Result<WorkflowReport> {
        let after = writer.draft.as_ref().unwrap().meta;
        let before_feed = (self.workflow == WorkflowKind::ReplaceFeed).then_some(self.feed);
        let scanned = compare_feeds(
            &writer.file,
            &writer.base.meta,
            before_feed,
            &after,
            self.feed,
            &self.cancellation,
        )
        .map_err(|error| writer.abort_after(error))?;
        let logical_change = if self.workflow == WorkflowKind::CreateFeed {
            LogicalChange::Changed
        } else {
            classify(&scanned.comparison)
        };
        Ok(WorkflowReport::replacement(
            ReplacementReportInput {
                workflow: self.workflow,
                logical_change,
                input_record_count: self.input_records,
                input_normalized_interval_count: scanned.after_intervals,
                before_range_record_count: scanned.before_intervals,
                after_range_record_count: scanned.after_intervals,
                input_addresses: scanned.comparison.after,
            },
            scanned.comparison,
        ))
    }

    fn require_family(&mut self, writer: &mut LiveWriter, family: AddressFamily) -> Result<()> {
        self.require_active(writer)?;
        if writer.base.meta.address_family != family {
            return Err(writer.abort_after(Error::WrongAddressFamily(
                "range family does not match the database",
            )));
        }
        Ok(())
    }

    pub(crate) fn require_active(&self, writer: &LiveWriter) -> Result<()> {
        writer.require_healthy()?;
        if !writer
            .draft
            .as_ref()
            .is_some_and(Draft::workflow_input_open)
        {
            return Err(Error::WrongState("workflow input is not active"));
        }
        Ok(())
    }
}

fn setup_feed(
    store: &mut DraftStore<'_>,
    name: FeedName,
    existing: Option<FeedEntry>,
    create: bool,
    family: AddressFamily,
    cancellation: &CancellationToken,
) -> Result<(FeedEntry, Interned)> {
    cancellation.check()?;
    let feed = select_feed(store, name, existing, create)?;
    let member = store.add_feed_to_membership(0, 0, feed)?;
    if !create {
        clear_feed(store, family, &member, cancellation)?;
    }
    cancellation.check()?;
    Ok((feed, member))
}

fn select_feed(
    store: &mut DraftStore<'_>,
    name: FeedName,
    existing: Option<FeedEntry>,
    create: bool,
) -> Result<FeedEntry> {
    if !create {
        return existing.ok_or(Error::Corrupt("replacement feed disappeared"));
    }
    let (feed, created) = store.ensure_feed(name)?;
    if !created {
        return Err(Error::Corrupt("absent feed appeared during creation"));
    }
    Ok(feed)
}

fn clear_feed(
    store: &mut DraftStore<'_>,
    family: AddressFamily,
    member: &Interned,
    cancellation: &CancellationToken,
) -> Result<()> {
    match family {
        AddressFamily::Ipv4 => store.apply_membership_cancellable(
            Ipv4Key::MIN,
            Ipv4Key::MAX,
            member.id,
            member.word_count,
            MembershipOperation::Difference,
            &mut || cancellation.check(),
        )?,
        AddressFamily::Ipv6 => store.apply_membership_cancellable(
            Ipv6Key::MIN,
            Ipv6Key::MAX,
            member.id,
            member.word_count,
            MembershipOperation::Difference,
            &mut || cancellation.check(),
        )?,
    };
    Ok(())
}

fn require_feed_precondition(existing: Option<FeedEntry>, create: bool) -> Result<()> {
    match (existing, create) {
        (Some(_), true) => Err(Error::NameExists),
        (None, false) => Err(Error::NameNotFound),
        _ => Ok(()),
    }
}

fn compare_feeds(
    file: &std::fs::File,
    before: &crate::contract::MetaV4,
    before_feed: Option<FeedEntry>,
    after: &crate::contract::MetaV4,
    after_feed: FeedEntry,
    cancellation: &CancellationToken,
) -> Result<compare::ScannedComparison> {
    match after.address_family {
        AddressFamily::Ipv4 => {
            compare::feeds::<Ipv4Key>(file, before, before_feed, after, after_feed, cancellation)
        }
        AddressFamily::Ipv6 => {
            compare::feeds::<Ipv6Key>(file, before, before_feed, after, after_feed, cancellation)
        }
    }
}

#[cfg(test)]
#[path = "../feed_workflow_tests.rs"]
mod tests;
