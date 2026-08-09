//! Internal ownership bridge for the stable C binding.

use std::path::Path;
use std::sync::Arc;

use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::live_writer::{
    finish_import_state, DirectState, ExactDirectState, ExactFeedState, FinishedState,
    MembershipImportStateSource, MembershipState, PreparedState,
};
use crate::range_cursor::{DirectRange, RangeDirection};
use crate::reader_core::ReaderCore;
use crate::source::SliceSource;
use crate::workflow::{AddressRange, WorkflowKind, WorkflowReport};
use crate::{
    AbortResult, AddressFamily, CancellationToken, CloseOutcome, CommitResult, DatabaseInfo,
    FeedEntry, FeedName, FeedRef, ImmutableReader, LiveReader, LiveWriter, MembershipImportSource,
    MembershipOperation, MembershipRef, ReclaimResult, TransactionBudget,
};

pub use crate::reader_core::{MembershipToken, ReaderCursor, ReaderCursorItem};

/// Reader ownership that can be retained by C child handles.
#[derive(Debug)]
pub struct Reader {
    inner: ReaderInner,
}

#[derive(Debug)]
enum ReaderInner {
    Immutable(ImmutableReader),
    Live(LiveReader),
    Closed,
}

impl Reader {
    pub fn open_immutable(path: impl AsRef<Path>) -> Result<Self> {
        Ok(Self {
            inner: ReaderInner::Immutable(ImmutableReader::open(path)?),
        })
    }

    pub fn open_live(path: impl AsRef<Path>, cancellation: &CancellationToken) -> Result<Self> {
        Ok(Self {
            inner: ReaderInner::Live(LiveReader::open(path, cancellation)?),
        })
    }

    pub fn info(&self) -> Result<DatabaseInfo> {
        Ok(self.core()?.info())
    }

    pub fn metadata_json_len(&self) -> Result<Option<u64>> {
        Ok(self.core()?.read().metadata_json_len())
    }

    pub fn lookup_direct_v4(&self, address: Ipv4Key) -> Result<Option<u32>> {
        self.core()?.read().lookup_direct_v4(address)
    }

    pub fn lookup_direct_v6(&self, address: Ipv6Key) -> Result<Option<u32>> {
        self.core()?.read().lookup_direct_v6(address)
    }

    pub fn lookup_feed(&self, name: &str) -> Result<Option<FeedEntry>> {
        self.core()?.read().lookup_feed(name)
    }

    pub fn open_direct_cursor(&self, direction: RangeDirection) -> Result<ReaderCursor> {
        self.core()?.read().open_direct_state(direction)
    }

    pub fn open_membership_cursor(&self, direction: RangeDirection) -> Result<ReaderCursor> {
        self.core()?.read().open_membership_state(direction)
    }

    pub fn open_feed_cursor(&self, name: &str, direction: RangeDirection) -> Result<ReaderCursor> {
        self.core()?.read().open_feed_state(name, direction)
    }

    pub fn cursor_next(&self, cursor: &mut ReaderCursor) -> Result<Option<ReaderCursorItem>> {
        self.core()?.read().cursor_next(cursor)
    }

    pub fn cursor_seek_v4(&self, cursor: &mut ReaderCursor, target: Ipv4Key) -> Result<()> {
        self.core()?.read().cursor_seek_v4(cursor, target)
    }

    pub fn cursor_seek_v6(&self, cursor: &mut ReaderCursor, target: Ipv6Key) -> Result<()> {
        self.core()?.read().cursor_seek_v6(cursor, target)
    }

    pub fn lookup_membership_token_v4(&self, address: Ipv4Key) -> Result<Option<MembershipToken>> {
        self.core()?.read().membership_token_v4(address)
    }

    pub fn lookup_membership_token_v6(&self, address: Ipv6Key) -> Result<Option<MembershipToken>> {
        self.core()?.read().membership_token_v6(address)
    }

    pub fn membership_word_count(&self, token: MembershipToken) -> Result<u32> {
        self.with_membership_token(token, |view| view.word_count())
    }

    pub fn membership_word(&self, token: MembershipToken, index: u32) -> Result<Option<u64>> {
        self.with_membership_token(token, |view| view.word(index))
    }

    pub fn membership_words(
        &self,
        token: MembershipToken,
        start: u32,
        output: &mut [u64],
    ) -> Result<usize> {
        self.with_membership_token(token, |view| view.read_words(start, output))
    }

    pub fn membership_contains(&self, token: MembershipToken, index: u32) -> Result<bool> {
        self.with_membership_token(token, |view| view.contains_index(index))
    }

    pub fn enumerate_feeds(&self, mut sink: impl FnMut(FeedEntry) -> Result<bool>) -> Result<u64> {
        let mut cursor = self.core()?.read().feed_cursor()?;
        let mut count = 0u64;
        while let Some(feed) = cursor.next_feed()? {
            if !sink(feed)? {
                return Err(Error::StoppedBySink);
            }
            count = count
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("feed scan count"))?;
        }
        Ok(count)
    }

    pub fn membership_present_v4(&self, address: Ipv4Key) -> Result<bool> {
        self.with_membership_v4(address, |membership| Ok(membership.is_some()))
    }

    pub fn membership_present_v6(&self, address: Ipv6Key) -> Result<bool> {
        self.with_membership_v6(address, |membership| Ok(membership.is_some()))
    }

    pub fn membership_word_count_v4(&self, address: Ipv4Key) -> Result<u32> {
        self.with_membership_v4(address, |membership| {
            membership.ok_or(Error::StaleReference)?.word_count()
        })
    }

    pub fn membership_word_count_v6(&self, address: Ipv6Key) -> Result<u32> {
        self.with_membership_v6(address, |membership| {
            membership.ok_or(Error::StaleReference)?.word_count()
        })
    }

    pub fn membership_word_v4(&self, address: Ipv4Key, index: u32) -> Result<Option<u64>> {
        self.with_membership_v4(address, |membership| {
            membership.ok_or(Error::StaleReference)?.word(index)
        })
    }

    pub fn membership_word_v6(&self, address: Ipv6Key, index: u32) -> Result<Option<u64>> {
        self.with_membership_v6(address, |membership| {
            membership.ok_or(Error::StaleReference)?.word(index)
        })
    }

    pub fn membership_words_v4(
        &self,
        address: Ipv4Key,
        start: u32,
        output: &mut [u64],
    ) -> Result<usize> {
        self.with_membership_v4(address, |membership| {
            membership
                .ok_or(Error::StaleReference)?
                .read_words(start, output)
        })
    }

    pub fn membership_words_v6(
        &self,
        address: Ipv6Key,
        start: u32,
        output: &mut [u64],
    ) -> Result<usize> {
        self.with_membership_v6(address, |membership| {
            membership
                .ok_or(Error::StaleReference)?
                .read_words(start, output)
        })
    }

    pub fn membership_contains_v4(&self, address: Ipv4Key, index: u32) -> Result<bool> {
        self.with_membership_v4(address, |membership| {
            membership
                .ok_or(Error::StaleReference)?
                .contains_index(index)
        })
    }

    pub fn membership_contains_v6(&self, address: Ipv6Key, index: u32) -> Result<bool> {
        self.with_membership_v6(address, |membership| {
            membership
                .ok_or(Error::StaleReference)?
                .contains_index(index)
        })
    }

    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.core()?.read().read_metadata_json(output)
    }

    pub fn close(&mut self) -> Result<()> {
        match &mut self.inner {
            ReaderInner::Immutable(_) => {}
            ReaderInner::Live(reader) => {
                let result = reader.close()?;
                if result.outcome != CloseOutcome::Closed {
                    return Err(result
                        .cause
                        .unwrap_or(Error::CleanupInProgress("live reader close is incomplete")));
                }
            }
            ReaderInner::Closed => return Err(Error::WrongState("reader is closed")),
        }
        self.inner = ReaderInner::Closed;
        Ok(())
    }

    fn import_source(&self) -> Result<MembershipImportSource<'_>> {
        match &self.inner {
            ReaderInner::Immutable(reader) => Ok(MembershipImportSource::Immutable(reader)),
            ReaderInner::Live(reader) => Ok(MembershipImportSource::Live(reader)),
            ReaderInner::Closed => Err(Error::WrongState("reader is closed")),
        }
    }

    fn with_membership_v4<T>(
        &self,
        address: Ipv4Key,
        operation: impl FnOnce(Option<crate::MembershipView<'_>>) -> Result<T>,
    ) -> Result<T> {
        operation(self.core()?.read().lookup_membership_v4(address)?)
    }

    fn with_membership_v6<T>(
        &self,
        address: Ipv6Key,
        operation: impl FnOnce(Option<crate::MembershipView<'_>>) -> Result<T>,
    ) -> Result<T> {
        operation(self.core()?.read().lookup_membership_v6(address)?)
    }

    fn core(&self) -> Result<&ReaderCore> {
        match &self.inner {
            ReaderInner::Immutable(reader) => Ok(reader.core()),
            ReaderInner::Live(reader) => reader.core(),
            ReaderInner::Closed => Err(Error::WrongState("reader is closed")),
        }
    }

    fn with_membership_token<T>(
        &self,
        token: MembershipToken,
        operation: impl FnOnce(crate::MembershipView<'_>) -> Result<T>,
    ) -> Result<T> {
        operation(self.core()?.read().membership(token)?)
    }
}

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
    ExactFeed(ExactFeedState),
    ExactDirect {
        state: ExactDirectState,
        retention_value: Option<u32>,
    },
    Import {
        source: Arc<Reader>,
        cancellation: CancellationToken,
    },
    Prepared(PreparedState),
}

impl Writer {
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
        let Operation::Membership(state) = &mut self.operation else {
            return Err(Error::WrongState(
                "advanced membership operation is not active",
            ));
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
            Operation::Membership(state) => state.set_metadata_json(&mut self.inner, input),
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
            Operation::Membership(state) => state.clear_metadata_json(&mut self.inner),
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

    pub fn feed_ensure(&mut self, name: FeedName) -> Result<FeedRef> {
        match &mut self.operation {
            Operation::Membership(state) => state.ensure_feed(&mut self.inner, name),
            _ => Err(Error::WrongState(
                "advanced membership operation is not active",
            )),
        }
    }

    pub fn feed_lookup(&mut self, name: FeedName) -> Result<Option<FeedRef>> {
        match &mut self.operation {
            Operation::Membership(state) => state.lookup_feed(&mut self.inner, name),
            _ => Err(Error::WrongState(
                "advanced membership operation is not active",
            )),
        }
    }

    pub fn feed_rename(&mut self, feed: FeedRef, name: FeedName) -> Result<FeedRef> {
        match &mut self.operation {
            Operation::Membership(state) => state.rename_feed(&mut self.inner, feed, name),
            _ => Err(Error::WrongState(
                "advanced membership operation is not active",
            )),
        }
    }

    pub fn feed_delete(&mut self, feed: FeedRef) -> Result<()> {
        match &mut self.operation {
            Operation::Membership(state) => state.delete_feed(&mut self.inner, feed),
            _ => Err(Error::WrongState(
                "advanced membership operation is not active",
            )),
        }
    }

    pub fn empty_membership(&mut self) -> Result<MembershipRef> {
        match &mut self.operation {
            Operation::Membership(state) => state.empty_membership(&mut self.inner),
            _ => Err(Error::WrongState(
                "advanced membership operation is not active",
            )),
        }
    }

    pub fn membership_add_feed(
        &mut self,
        membership: MembershipRef,
        feed: FeedRef,
    ) -> Result<MembershipRef> {
        match &mut self.operation {
            Operation::Membership(state) => state.add_feed(&mut self.inner, membership, feed),
            _ => Err(Error::WrongState(
                "advanced membership operation is not active",
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
            retention_value: None,
        };
        Ok(())
    }

    pub fn begin_retention_refresh(
        &mut self,
        value: u32,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.require_clean()?;
        self.operation = Operation::ExactDirect {
            state: self.inner.begin_retention_state(cancellation)?,
            retention_value: Some(value),
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

    pub fn add_coverage_v4(&mut self, ranges: &[AddressRange<Ipv4Key>]) -> Result<()> {
        let mut source = SliceSource::new(ranges);
        match &mut self.operation {
            Operation::ExactFeed(state) => {
                state.add_ranges(&mut self.inner, AddressFamily::Ipv4, &mut source)
            }
            Operation::ExactDirect {
                state,
                retention_value: Some(value),
            } => state.add_retention_v4(&mut self.inner, *value, &mut source),
            _ => Err(Error::WrongState(
                "coverage input does not match the active workflow",
            )),
        }
    }

    pub fn add_coverage_v6(&mut self, ranges: &[AddressRange<Ipv6Key>]) -> Result<()> {
        let mut source = SliceSource::new(ranges);
        match &mut self.operation {
            Operation::ExactFeed(state) => {
                state.add_ranges(&mut self.inner, AddressFamily::Ipv6, &mut source)
            }
            Operation::ExactDirect {
                state,
                retention_value: Some(value),
            } => state.add_retention_v6(&mut self.inner, *value, &mut source),
            _ => Err(Error::WrongState(
                "coverage input does not match the active workflow",
            )),
        }
    }

    pub fn add_direct_v4(&mut self, ranges: &[DirectRange<Ipv4Key>]) -> Result<()> {
        let mut source = SliceSource::new(ranges);
        match &mut self.operation {
            Operation::ExactDirect {
                state,
                retention_value: None,
            } => state.add_direct_v4(&mut self.inner, &mut source),
            _ => Err(Error::WrongState(
                "direct input does not match the active workflow",
            )),
        }
    }

    pub fn add_direct_v6(&mut self, ranges: &[DirectRange<Ipv6Key>]) -> Result<()> {
        let mut source = SliceSource::new(ranges);
        match &mut self.operation {
            Operation::ExactDirect {
                state,
                retention_value: None,
            } => state.add_direct_v6(&mut self.inner, &mut source),
            _ => Err(Error::WrongState(
                "direct input does not match the active workflow",
            )),
        }
    }

    pub fn finish_input(&mut self) -> Result<WorkflowReport> {
        let operation = std::mem::replace(&mut self.operation, Operation::Clean);
        let finished = match operation {
            Operation::ExactFeed(state) => state.finish_state(&mut self.inner),
            Operation::ExactDirect {
                state,
                retention_value: None,
            } => state.finish_replacement_state(&mut self.inner),
            Operation::ExactDirect {
                state,
                retention_value: Some(value),
            } => state.finish_retention_state(&mut self.inner, value),
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
        let report = *finished.report();
        if let FinishedState::Changed { state, .. } = finished {
            self.operation = Operation::Prepared(state);
        }
        Ok(report)
    }

    pub fn commit(&mut self) -> Result<CommitResult> {
        let operation = std::mem::replace(&mut self.operation, Operation::Clean);
        let cancellation = match &operation {
            Operation::Metadata(state) | Operation::Prepared(state) => state.cancellation(),
            Operation::Direct(state) => state.cancellation(),
            Operation::Membership(state) => state.cancellation(),
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
/// Entry point used only by the version-matched SDK worker executable.
#[doc(hidden)]
pub fn worker_main() -> i32 {
    crate::worker::main()
}
