use std::path::Path;
use std::sync::Arc;

use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::live_writer::StructureRef;
use crate::live_writer::{
    finish_import_state, DirectState, ExactDirectState, ExactFeedState, FinishedState,
    MembershipImportStateSource, MembershipState, PreparedState,
};
use crate::range_cursor::DirectRange;
use crate::source::RangeSource;
use crate::workflow::{AddressRange, WorkflowKind, WorkflowReport};
use crate::{
    AbortResult, AddressFamily, CancellationToken, CommitResult, DirectSemantic, FeedName, FeedRef,
    FirstSeenRemovalSink, HistoryProjectionReport, HistoryWindow, LiveWriter, MembershipOperation,
    MembershipRef, NetworkEnrichmentV1, ReclaimResult, TransactionBudget,
};

use super::Reader;

/// Writer ownership with exactly one binding-visible operation state.
#[derive(Debug)]
pub struct Writer {
    inner: LiveWriter,
    operation: Operation,
}

#[derive(Debug)]
// FeedName is inline and bounded; avoid a second allocation per C operation.
#[allow(clippy::large_enum_variant)]
enum Operation {
    Clean,
    Metadata(PreparedState),
    Direct(DirectState),
    Membership(MembershipState),
    Structured(MembershipState),
    ExactFeed(ExactFeedState),
    ExactDirect {
        state: ExactDirectState,
        input: ExactDirectInput,
    },
    Import {
        source: Arc<Reader>,
        cancellation: CancellationToken,
    },
    Prepared(PreparedState),
}

#[derive(Clone, Copy, Debug)]
enum ExactDirectInput {
    Replacement,
    FirstSeen { refresh_value: u32 },
    LastSeen { refresh_value: u32, cutoff: u32 },
}

impl Writer {
    pub fn address_family(&self) -> AddressFamily {
        self.inner.address_family()
    }

    pub fn open(
        path: impl AsRef<Path>,
        budget: TransactionBudget,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        Ok(Self {
            inner: LiveWriter::open(path, budget, cancellation)?,
            operation: Operation::Clean,
        })
    }

    pub fn metadata_json_len(&self) -> Result<Option<u64>> {
        self.inner.metadata_json_len()
    }

    pub fn is_clean(&self) -> bool {
        matches!(self.operation, Operation::Clean)
    }

    pub fn enumerate_transaction_feeds(
        &mut self,
        mut sink: impl FnMut(FeedRef) -> Result<bool>,
    ) -> Result<u64> {
        let state = match &mut self.operation {
            Operation::Membership(state) | Operation::Structured(state) => state,
            _ => {
                return Err(Error::WrongState(
                    "advanced membership or structured operation is not active",
                ))
            }
        };
        let cancellation = state.cancellation().clone();
        let mut cursor = state.feed_cursor(&mut self.inner)?;
        let mut count = 0u64;
        while let Some(feed) = cursor.next_feed()? {
            cancellation.check()?;
            if !sink(feed)? {
                return Err(Error::StoppedBySink);
            }
            count = count
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("transaction feed scan count"))?;
        }
        Ok(count)
    }

    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.inner.read_metadata_json(output)
    }

    pub fn set_metadata_json(
        &mut self,
        input: &[u8],
        cancellation: &CancellationToken,
    ) -> Result<bool> {
        match &mut self.operation {
            Operation::Clean => {
                let changed = self.inner.set_metadata_json(input, cancellation)?;
                if changed {
                    self.operation = Operation::Metadata(PreparedState::new(cancellation.clone()));
                }
                Ok(changed)
            }
            Operation::Direct(state) => state.set_metadata_json(&mut self.inner, input),
            Operation::Membership(state) | Operation::Structured(state) => {
                state.set_metadata_json(&mut self.inner, input)
            }
            Operation::Prepared(state) | Operation::Metadata(state) => {
                state.set_metadata_json(&mut self.inner, input)
            }
            _ => Err(Error::WrongState("workflow input is not finished")),
        }
    }

    pub fn clear_metadata_json(&mut self, cancellation: &CancellationToken) -> Result<bool> {
        match &mut self.operation {
            Operation::Clean => {
                let changed = self.inner.clear_metadata_json(cancellation)?;
                if changed {
                    self.operation = Operation::Metadata(PreparedState::new(cancellation.clone()));
                }
                Ok(changed)
            }
            Operation::Direct(state) => state.clear_metadata_json(&mut self.inner),
            Operation::Membership(state) | Operation::Structured(state) => {
                state.clear_metadata_json(&mut self.inner)
            }
            Operation::Prepared(state) | Operation::Metadata(state) => {
                state.clear_metadata_json(&mut self.inner)
            }
            _ => Err(Error::WrongState("workflow input is not finished")),
        }
    }

    pub fn begin_direct(&mut self, cancellation: &CancellationToken) -> Result<()> {
        self.require_clean()?;
        self.operation = Operation::Direct(self.inner.begin_direct_state(cancellation)?);
        Ok(())
    }

    pub fn direct_assign_v4(&mut self, from: Ipv4Key, to: Ipv4Key, value: u32) -> Result<bool> {
        match &mut self.operation {
            Operation::Direct(state) => state.assign_v4(&mut self.inner, from, to, value),
            _ => Err(Error::WrongState("advanced direct operation is not active")),
        }
    }

    pub fn direct_assign_v6(&mut self, from: Ipv6Key, to: Ipv6Key, value: u32) -> Result<bool> {
        match &mut self.operation {
            Operation::Direct(state) => state.assign_v6(&mut self.inner, from, to, value),
            _ => Err(Error::WrongState("advanced direct operation is not active")),
        }
    }

    pub fn direct_clear_v4(&mut self, from: Ipv4Key, to: Ipv4Key) -> Result<bool> {
        match &mut self.operation {
            Operation::Direct(state) => state.clear_v4(&mut self.inner, from, to),
            _ => Err(Error::WrongState("advanced direct operation is not active")),
        }
    }

    pub fn direct_clear_v6(&mut self, from: Ipv6Key, to: Ipv6Key) -> Result<bool> {
        match &mut self.operation {
            Operation::Direct(state) => state.clear_v6(&mut self.inner, from, to),
            _ => Err(Error::WrongState("advanced direct operation is not active")),
        }
    }

    pub fn begin_membership(&mut self, cancellation: &CancellationToken) -> Result<()> {
        self.require_clean()?;
        self.operation = Operation::Membership(self.inner.begin_membership_state(cancellation)?);
        Ok(())
    }

    pub fn begin_structured(&mut self, cancellation: &CancellationToken) -> Result<()> {
        self.require_clean()?;
        self.operation = Operation::Structured(self.inner.begin_structured_state(cancellation)?);
        Ok(())
    }

    pub fn feed_ensure(&mut self, name: FeedName) -> Result<FeedRef> {
        match &mut self.operation {
            Operation::Membership(state) | Operation::Structured(state) => {
                state.ensure_feed(&mut self.inner, name)
            }
            _ => Err(Error::WrongState(
                "advanced membership or structured operation is not active",
            )),
        }
    }

    pub fn feed_lookup(&mut self, name: FeedName) -> Result<Option<FeedRef>> {
        match &mut self.operation {
            Operation::Membership(state) | Operation::Structured(state) => {
                state.lookup_feed(&mut self.inner, name)
            }
            _ => Err(Error::WrongState(
                "advanced membership or structured operation is not active",
            )),
        }
    }

    pub fn feed_rename(&mut self, feed: FeedRef, name: FeedName) -> Result<FeedRef> {
        match &mut self.operation {
            Operation::Membership(state) | Operation::Structured(state) => {
                state.rename_feed(&mut self.inner, feed, name)
            }
            _ => Err(Error::WrongState(
                "advanced membership or structured operation is not active",
            )),
        }
    }

    pub fn feed_delete(&mut self, feed: FeedRef) -> Result<()> {
        match &mut self.operation {
            Operation::Membership(state) => state.delete_feed(&mut self.inner, feed),
            Operation::Structured(state) => state.delete_structured_feed(&mut self.inner, feed),
            _ => Err(Error::WrongState(
                "advanced membership or structured operation is not active",
            )),
        }
    }

    pub fn empty_membership(&mut self) -> Result<MembershipRef> {
        match &mut self.operation {
            Operation::Membership(state) | Operation::Structured(state) => {
                state.empty_membership(&mut self.inner)
            }
            _ => Err(Error::WrongState(
                "advanced membership or structured operation is not active",
            )),
        }
    }

    pub fn membership_add_feed(
        &mut self,
        membership: MembershipRef,
        feed: FeedRef,
    ) -> Result<MembershipRef> {
        match &mut self.operation {
            Operation::Membership(state) | Operation::Structured(state) => {
                state.add_feed(&mut self.inner, membership, feed)
            }
            _ => Err(Error::WrongState(
                "advanced membership or structured operation is not active",
            )),
        }
    }

    pub fn membership_apply_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        membership: MembershipRef,
        operation: MembershipOperation,
    ) -> Result<bool> {
        match &mut self.operation {
            Operation::Membership(state) => {
                state.apply_v4(&mut self.inner, from, to, membership, operation)
            }
            _ => Err(Error::WrongState(
                "advanced membership operation is not active",
            )),
        }
    }

    pub fn membership_apply_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        membership: MembershipRef,
        operation: MembershipOperation,
    ) -> Result<bool> {
        match &mut self.operation {
            Operation::Membership(state) => {
                state.apply_v6(&mut self.inner, from, to, membership, operation)
            }
            _ => Err(Error::WrongState(
                "advanced membership operation is not active",
            )),
        }
    }

    pub fn network_enrichment_v1_intern(
        &mut self,
        value: NetworkEnrichmentV1,
        membership: Option<MembershipRef>,
    ) -> Result<StructureRef> {
        match &mut self.operation {
            Operation::Structured(state) => {
                state.intern_network_enrichment_v1(&mut self.inner, value, membership)
            }
            _ => Err(Error::WrongState(
                "advanced structured operation is not active",
            )),
        }
    }

    pub fn structured_assign_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        structure: StructureRef,
    ) -> Result<bool> {
        match &mut self.operation {
            Operation::Structured(state) => {
                state.assign_structure_v4(&mut self.inner, from, to, structure)
            }
            _ => Err(Error::WrongState(
                "advanced structured operation is not active",
            )),
        }
    }

    pub fn structured_assign_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        structure: StructureRef,
    ) -> Result<bool> {
        match &mut self.operation {
            Operation::Structured(state) => {
                state.assign_structure_v6(&mut self.inner, from, to, structure)
            }
            _ => Err(Error::WrongState(
                "advanced structured operation is not active",
            )),
        }
    }

    pub fn structured_clear_v4(&mut self, from: Ipv4Key, to: Ipv4Key) -> Result<bool> {
        match &mut self.operation {
            Operation::Structured(state) => state.clear_structure_v4(&mut self.inner, from, to),
            _ => Err(Error::WrongState(
                "advanced structured operation is not active",
            )),
        }
    }

    pub fn structured_clear_v6(&mut self, from: Ipv6Key, to: Ipv6Key) -> Result<bool> {
        match &mut self.operation {
            Operation::Structured(state) => state.clear_structure_v6(&mut self.inner, from, to),
            _ => Err(Error::WrongState(
                "advanced structured operation is not active",
            )),
        }
    }

    pub fn begin_create_feed(
        &mut self,
        name: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.begin_feed(name, true, cancellation)
    }

    pub fn begin_replace_feed(
        &mut self,
        name: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.begin_feed(name, false, cancellation)
    }

    pub fn delete_feed(&mut self, name: FeedName, cancellation: &CancellationToken) -> Result<()> {
        self.require_clean()?;
        self.operation = Operation::Prepared(self.inner.delete_feed_state(name, cancellation)?);
        Ok(())
    }

    pub fn rename_feed(
        &mut self,
        old: FeedName,
        new: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.require_clean()?;
        self.operation =
            Operation::Prepared(self.inner.rename_feed_state(old, new, cancellation)?);
        Ok(())
    }

    pub fn begin_direct_replacement(&mut self, cancellation: &CancellationToken) -> Result<()> {
        self.require_clean()?;
        self.operation = Operation::ExactDirect {
            state: self
                .inner
                .begin_exact_direct_state(WorkflowKind::DirectReplacement, cancellation)?,
            input: ExactDirectInput::Replacement,
        };
        Ok(())
    }

    pub fn begin_first_seen_refresh(
        &mut self,
        value: u32,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.require_clean()?;
        self.operation = Operation::ExactDirect {
            state: self.inner.begin_timestamp_state(
                DirectSemantic::FirstSeen,
                WorkflowKind::FirstSeenRefresh,
                cancellation,
            )?,
            input: ExactDirectInput::FirstSeen {
                refresh_value: value,
            },
        };
        Ok(())
    }

    pub fn begin_last_seen_refresh(
        &mut self,
        refresh_value: u32,
        cutoff: u32,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.require_clean()?;
        self.operation = Operation::ExactDirect {
            state: self.inner.begin_timestamp_state(
                DirectSemantic::LastSeen,
                WorkflowKind::LastSeenRefresh,
                cancellation,
            )?,
            input: ExactDirectInput::LastSeen {
                refresh_value,
                cutoff,
            },
        };
        Ok(())
    }

    pub fn begin_membership_import(
        &mut self,
        source: Arc<Reader>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.require_clean()?;
        self.inner
            .begin_membership_import_state(source.import_source()?, cancellation)?;
        self.operation = Operation::Import {
            source,
            cancellation: cancellation.clone(),
        };
        Ok(())
    }

    pub fn project_history(
        &mut self,
        source: Arc<Reader>,
        windows: &[HistoryWindow],
        cancellation: &CancellationToken,
    ) -> Result<HistoryProjectionReport> {
        self.require_clean()?;
        let finished =
            self.inner
                .project_history_state(source.history_source()?, windows, cancellation)?;
        if let Some(state) = finished.state {
            self.operation = Operation::Prepared(state);
        }
        Ok(finished.report)
    }

    pub fn project_history_from<I>(
        &mut self,
        source: Arc<Reader>,
        window_count: usize,
        windows: I,
        cancellation: &CancellationToken,
    ) -> Result<HistoryProjectionReport>
    where
        I: IntoIterator<Item = Result<HistoryWindow>>,
    {
        self.require_clean()?;
        let finished = self.inner.project_history_state_from(
            source.history_source()?,
            window_count,
            windows,
            cancellation,
        )?;
        if let Some(state) = finished.state {
            self.operation = Operation::Prepared(state);
        }
        Ok(finished.report)
    }

    pub fn add_coverage_v4<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv4Key>>,
    {
        let result = match &mut self.operation {
            Operation::ExactFeed(state) => state.add_ranges_v4(&mut self.inner, source),
            Operation::ExactDirect {
                state,
                input:
                    ExactDirectInput::FirstSeen {
                        refresh_value: value,
                    }
                    | ExactDirectInput::LastSeen {
                        refresh_value: value,
                        ..
                    },
            } => state.add_timestamp_v4(&mut self.inner, *value, source),
            _ => Err(Error::WrongState(
                "coverage input does not match the active workflow",
            )),
        };
        self.finish_source_input(result)
    }

    pub fn add_coverage_v6<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<AddressRange<Ipv6Key>>,
    {
        let result = match &mut self.operation {
            Operation::ExactFeed(state) => state.add_ranges_v6(&mut self.inner, source),
            Operation::ExactDirect {
                state,
                input:
                    ExactDirectInput::FirstSeen {
                        refresh_value: value,
                    }
                    | ExactDirectInput::LastSeen {
                        refresh_value: value,
                        ..
                    },
            } => state.add_timestamp_v6(&mut self.inner, *value, source),
            _ => Err(Error::WrongState(
                "coverage input does not match the active workflow",
            )),
        };
        self.finish_source_input(result)
    }

    pub fn add_direct_v4<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<DirectRange<Ipv4Key>>,
    {
        let result = match &mut self.operation {
            Operation::ExactDirect {
                state,
                input: ExactDirectInput::Replacement,
            } => state.add_direct_v4(&mut self.inner, source),
            _ => Err(Error::WrongState(
                "direct input does not match the active workflow",
            )),
        };
        self.finish_source_input(result)
    }

    pub fn add_direct_v6<S>(&mut self, source: &mut S) -> Result<()>
    where
        S: RangeSource<DirectRange<Ipv6Key>>,
    {
        let result = match &mut self.operation {
            Operation::ExactDirect {
                state,
                input: ExactDirectInput::Replacement,
            } => state.add_direct_v6(&mut self.inner, source),
            _ => Err(Error::WrongState(
                "direct input does not match the active workflow",
            )),
        };
        self.finish_source_input(result)
    }

    pub fn finish_input(&mut self) -> Result<WorkflowReport> {
        let operation = std::mem::replace(&mut self.operation, Operation::Clean);
        let finished = match operation {
            Operation::ExactFeed(state) => state.finish_state(&mut self.inner),
            Operation::ExactDirect {
                state,
                input: ExactDirectInput::Replacement,
            } => state.finish_replacement_state(&mut self.inner),
            Operation::ExactDirect {
                state,
                input: ExactDirectInput::FirstSeen { refresh_value },
            } => state.finish_first_seen_state(&mut self.inner, refresh_value),
            Operation::ExactDirect {
                state,
                input:
                    ExactDirectInput::LastSeen {
                        refresh_value,
                        cutoff,
                    },
            } => state.finish_last_seen_state(&mut self.inner, refresh_value, cutoff),
            Operation::Import {
                source,
                cancellation,
            } => {
                let source = MembershipImportStateSource::new(source.import_source()?)?;
                finish_import_state(&mut self.inner, source, &cancellation)
            }
            other => {
                self.operation = other;
                return Err(Error::WrongState("no exact workflow input is active"));
            }
        }?;
        Ok(self.store_finished(finished))
    }

    pub fn finish_first_seen_with_removals_v4<S>(&mut self, sink: &mut S) -> Result<WorkflowReport>
    where
        S: FirstSeenRemovalSink<Ipv4Key>,
    {
        let operation = std::mem::replace(&mut self.operation, Operation::Clean);
        let finished = match operation {
            Operation::ExactDirect {
                state,
                input: ExactDirectInput::FirstSeen { refresh_value },
            } => {
                state.finish_first_seen_with_removals_v4_state(&mut self.inner, refresh_value, sink)
            }
            other => {
                self.operation = other;
                return Err(Error::WrongState("no first-seen workflow input is active"));
            }
        }?;
        Ok(self.store_finished(finished))
    }

    pub fn finish_first_seen_with_removals_v6<S>(&mut self, sink: &mut S) -> Result<WorkflowReport>
    where
        S: FirstSeenRemovalSink<Ipv6Key>,
    {
        let operation = std::mem::replace(&mut self.operation, Operation::Clean);
        let finished = match operation {
            Operation::ExactDirect {
                state,
                input: ExactDirectInput::FirstSeen { refresh_value },
            } => {
                state.finish_first_seen_with_removals_v6_state(&mut self.inner, refresh_value, sink)
            }
            other => {
                self.operation = other;
                return Err(Error::WrongState("no first-seen workflow input is active"));
            }
        }?;
        Ok(self.store_finished(finished))
    }

    fn store_finished(&mut self, finished: FinishedState) -> WorkflowReport {
        let report = *finished.report();
        if let FinishedState::Changed { state, .. } = finished {
            self.operation = Operation::Prepared(state);
        }
        report
    }

    pub fn commit(&mut self) -> Result<CommitResult> {
        let operation = std::mem::replace(&mut self.operation, Operation::Clean);
        let cancellation = match &operation {
            Operation::Metadata(state) | Operation::Prepared(state) => state.cancellation(),
            Operation::Direct(state) => state.cancellation(),
            Operation::Membership(state) | Operation::Structured(state) => state.cancellation(),
            _ => {
                self.operation = operation;
                return Err(Error::WrongState("no committable transaction is pending"));
            }
        }
        .clone();
        self.inner.commit_operation(&cancellation)
    }

    pub fn abort(&mut self) -> Result<AbortResult> {
        if matches!(self.operation, Operation::Clean) {
            return Err(Error::NoPendingTransaction);
        }
        self.operation = Operation::Clean;
        self.inner.abort()
    }

    pub fn reclaim(
        &mut self,
        max_transactions: u64,
        max_pages: u64,
        cancellation: &CancellationToken,
    ) -> Result<ReclaimResult> {
        self.require_clean()?;
        self.inner
            .reclaim(max_transactions, max_pages, cancellation)
    }

    pub fn close(&mut self) -> Result<crate::CloseResult> {
        self.operation = Operation::Clean;
        self.inner.close()
    }

    pub fn abort_source_failure(&mut self, cause: Error) -> Error {
        self.operation = Operation::Clean;
        self.inner.abort_after(cause)
    }

    fn finish_source_input(&mut self, result: Result<()>) -> Result<()> {
        match result {
            Ok(()) => Ok(()),
            Err(error @ Error::TransactionAborted(_)) => {
                self.operation = Operation::Clean;
                Err(error)
            }
            Err(error) => Err(self.abort_source_failure(error)),
        }
    }

    fn begin_feed(
        &mut self,
        name: FeedName,
        create: bool,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.require_clean()?;
        self.operation = Operation::ExactFeed(self.inner.begin_exact_feed_state(
            name,
            create,
            cancellation,
        )?);
        Ok(())
    }

    fn require_clean(&self) -> Result<()> {
        if matches!(self.operation, Operation::Clean) {
            Ok(())
        } else {
            Err(Error::WrongState("a writer operation is already active"))
        }
    }
}
