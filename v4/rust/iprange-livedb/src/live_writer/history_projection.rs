//! One-pass projection of one last-seen map into named history feeds.

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::{AddressFamily, DirectSemantic, ValueKind};
use crate::database::DatabaseInfo;
use crate::error::{Error, Result};
use crate::history::{HistoryProjectionReport, HistoryWindow};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::live_namespace::Identity;
use crate::live_reader::LiveReader;
use crate::range_cursor::{DirectRange, RangeDirection};
use crate::reader_core::ReaderCore;
use crate::workflow::LogicalChange;
use crate::ImmutableReader;

use super::workflow::{PreparedOperation, PreparedState};
use super::{AbortResult, CommitResult, LiveWriter};

/// Explicit pinned source for one last-seen history projection.
#[derive(Clone, Copy, Debug)]
pub enum HistoryProjectionSource<'a> {
    Immutable(&'a ImmutableReader),
    Live(&'a LiveReader),
}

/// Completed history projection, either clean or awaiting publication.
#[derive(Debug)]
pub enum FinishedHistoryProjection<'a> {
    NoChange(HistoryProjectionReport),
    Changed(PreparedHistoryProjection<'a>),
}

/// Changed history projection prepared for optional metadata and publication.
#[derive(Debug)]
pub struct PreparedHistoryProjection<'a> {
    operation: PreparedOperation<'a>,
    report: HistoryProjectionReport,
}

/// Borrow-free history result shared with language bindings.
#[derive(Debug)]
pub(crate) struct FinishedHistoryState {
    pub(crate) report: HistoryProjectionReport,
    pub(crate) state: Option<PreparedState>,
}

#[derive(Clone, Copy)]
struct Source<'a> {
    core: &'a ReaderCore,
    info: DatabaseInfo,
    identity: Identity,
}

impl LiveWriter {
    /// Project all requested history windows from one pinned last-seen scan.
    pub fn project_history<'writer>(
        &'writer mut self,
        source: HistoryProjectionSource<'_>,
        windows: &[HistoryWindow],
        cancellation: &CancellationToken,
    ) -> Result<FinishedHistoryProjection<'writer>> {
        Ok(self
            .project_history_state(source, windows, cancellation)?
            .bind(self))
    }

    pub(crate) fn project_history_state(
        &mut self,
        source: HistoryProjectionSource<'_>,
        windows: &[HistoryWindow],
        cancellation: &CancellationToken,
    ) -> Result<FinishedHistoryState> {
        self.project_history_state_from(
            source,
            windows.len(),
            windows.iter().copied().map(Ok),
            cancellation,
        )
    }

    pub(crate) fn project_history_state_from<I>(
        &mut self,
        source: HistoryProjectionSource<'_>,
        window_count: usize,
        windows: I,
        cancellation: &CancellationToken,
    ) -> Result<FinishedHistoryState>
    where
        I: IntoIterator<Item = Result<HistoryWindow>>,
    {
        self.require_feed_workflow_ready()?;
        if window_count == 0 || window_count > u32::MAX as usize {
            return Err(Error::InvalidArgument("history window count is invalid"));
        }
        let source = Source::new(source)?;
        require_compatible_source(self, source)?;
        cancellation.check()?;
        self.start_feed_workflow_draft()?;

        let report = match source.info.address_family {
            AddressFamily::Ipv4 => {
                let mut cursor = external(
                    self,
                    source.core.read().direct_cursor_v4(RangeDirection::Forward),
                )?;
                project::<Ipv4Key, _, _>(self, source, window_count, windows, cancellation, || {
                    cursor.next_range()
                })?
            }
            AddressFamily::Ipv6 => {
                let mut cursor = external(
                    self,
                    source.core.read().direct_cursor_v6(RangeDirection::Forward),
                )?;
                project::<Ipv6Key, _, _>(self, source, window_count, windows, cancellation, || {
                    cursor.next_range()
                })?
            }
        };
        finish_state(self, report, cancellation.clone())
    }
}

impl FinishedHistoryProjection<'_> {
    pub fn report(&self) -> &HistoryProjectionReport {
        match self {
            Self::NoChange(report) => report,
            Self::Changed(prepared) => prepared.report(),
        }
    }

    /// Abort a changed prepared result; a no-change result is already clean.
    pub fn abort(self) -> Result<AbortResult> {
        match self {
            Self::NoChange(_) => Err(Error::NoPendingTransaction),
            Self::Changed(prepared) => prepared.abort(),
        }
    }
}

impl FinishedHistoryState {
    fn bind(self, writer: &mut LiveWriter) -> FinishedHistoryProjection<'_> {
        match self.state {
            None => FinishedHistoryProjection::NoChange(self.report),
            Some(state) => FinishedHistoryProjection::Changed(PreparedHistoryProjection {
                report: self.report,
                operation: PreparedOperation::new(writer, state),
            }),
        }
    }
}

impl PreparedHistoryProjection<'_> {
    pub fn report(&self) -> &HistoryProjectionReport {
        &self.report
    }

    pub fn set_metadata_json(&mut self, input: &[u8]) -> Result<bool> {
        self.operation.set_metadata_json(input)
    }

    pub fn clear_metadata_json(&mut self) -> Result<bool> {
        self.operation.clear_metadata_json()
    }

    pub fn commit(self) -> Result<CommitResult> {
        self.operation.commit()
    }

    pub fn abort(self) -> Result<AbortResult> {
        self.operation.abort()
    }
}

impl<'a> Source<'a> {
    fn new(source: HistoryProjectionSource<'a>) -> Result<Self> {
        let core = match source {
            HistoryProjectionSource::Immutable(reader) => reader.core(),
            HistoryProjectionSource::Live(reader) => reader.core()?,
        };
        Ok(Self {
            core,
            info: core.info(),
            identity: core.file_identity()?,
        })
    }
}

fn require_compatible_source(writer: &LiveWriter, source: Source<'_>) -> Result<()> {
    if source.info.value_kind != ValueKind::Direct {
        return Err(Error::WrongValueKind(
            "history projection requires a direct source",
        ));
    }
    if source.info.direct_semantic() != Some(DirectSemantic::LastSeen) {
        return Err(Error::WrongValueTag(
            "history projection requires a last_seen direct source",
        ));
    }
    if source.info.address_family != writer.address_family() {
        return Err(Error::WrongAddressFamily(
            "history projection source family differs",
        ));
    }
    if source.identity == writer.main_identity {
        return Err(Error::InvalidArgument(
            "history projection source and destination are the same file",
        ));
    }
    Ok(())
}

fn project<K, N, I>(
    writer: &mut LiveWriter,
    source: Source<'_>,
    window_count: usize,
    windows: I,
    cancellation: &CancellationToken,
    mut next: N,
) -> Result<HistoryProjectionReport>
where
    K: IpKey,
    N: FnMut() -> Result<Option<DirectRange<K>>>,
    I: IntoIterator<Item = Result<HistoryWindow>>,
{
    let plan = writer
        .mutate(|edit| edit.prepare_history_from::<K, _>(window_count, windows, cancellation))?;
    let mut merge = writer.mutate(|edit| edit.begin_history(plan, cancellation))?;
    let mut source_range_count = 0u64;
    let mut source_addresses = Cardinality129::ZERO;
    let mut previous = None;
    crate::work::input_source_pass(1);
    loop {
        let Some(range) = external(writer, next())? else {
            break;
        };
        require_canonical_source(writer, previous, range)?;
        writer.mutate(|edit| {
            edit.push_history(&mut merge, range.from, range.to, range.value, cancellation)
        })?;
        source_range_count = source_range_count.checked_add(1).ok_or_else(|| {
            writer.abort_after_source(Error::ArithmeticOverflow("source range count"))
        })?;
        source_addresses = source_addresses
            .checked_add(external(
                writer,
                range.from.inclusive_cardinality(range.to),
            )?)
            .map_err(|_| {
                writer.abort_after_source(Error::ArithmeticOverflow("source address count"))
            })?;
        previous = Some(range);
    }
    if source_range_count != source.info.range_record_count {
        return Err(
            writer.abort_after_source(Error::Corrupt("source last_seen range count disagrees"))
        );
    }
    writer.mutate(|edit| {
        edit.finish_history(merge, source_range_count, source_addresses, cancellation)
    })
}

fn require_canonical_source<K: IpKey>(
    writer: &mut LiveWriter,
    previous: Option<DirectRange<K>>,
    current: DirectRange<K>,
) -> Result<()> {
    let invalid = current.from > current.to
        || previous.is_some_and(|prior| {
            prior.from >= current.from
                || prior.to >= current.from
                || (prior.value == current.value && prior.to.checked_next() == Some(current.from))
        });
    if invalid {
        Err(writer.abort_after_source(Error::Corrupt("source last_seen ranges are not canonical")))
    } else {
        Ok(())
    }
}

fn finish_state(
    writer: &mut LiveWriter,
    report: HistoryProjectionReport,
    cancellation: CancellationToken,
) -> Result<FinishedHistoryState> {
    if report.logical_change == LogicalChange::NoChange {
        writer.discard_draft()?;
        return Ok(FinishedHistoryState {
            report,
            state: None,
        });
    }
    writer.mutate(|edit| edit.finish_membership_workflow(&cancellation))?;
    Ok(FinishedHistoryState {
        report,
        state: Some(PreparedState::new(cancellation)),
    })
}

fn external<T>(writer: &mut LiveWriter, result: Result<T>) -> Result<T> {
    result.map_err(|error| writer.abort_after_source(error))
}

#[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
#[path = "history_projection/tests.rs"]
mod tests;
