//! Shared exact-workflow input and prepared-draft ownership.

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::error::{Error, Result};
use crate::source::RangeSource;
use crate::workflow::{Comparison, WorkflowReport};

use super::{AbortResult, CommitResult, LiveWriter};

/// Result of `FinishInput`.
#[derive(Debug)]
pub enum FinishedWorkflow<'a> {
    NoChange(WorkflowReport),
    Changed(PreparedWorkflow<'a>),
}

/// Changed workflow prepared for optional metadata and publication.
#[derive(Debug)]
pub struct PreparedWorkflow<'a> {
    operation: PreparedOperation<'a>,
    report: WorkflowReport,
}

/// Prepared feed delete or rename awaiting optional metadata and publication.
#[derive(Debug)]
pub struct PreparedFeedChange<'a> {
    operation: PreparedOperation<'a>,
}

#[derive(Debug)]
struct PreparedOperation<'a> {
    writer: &'a mut LiveWriter,
    state: PreparedState,
}

/// Borrow-free prepared-operation state shared with language bindings.
#[derive(Debug)]
pub(crate) struct PreparedState {
    cancellation: CancellationToken,
}

/// Borrow-free result of finishing an exact workflow.
#[derive(Debug)]
pub(crate) enum FinishedState {
    NoChange(WorkflowReport),
    Changed {
        report: WorkflowReport,
        state: PreparedState,
    },
}

impl FinishedWorkflow<'_> {
    pub fn report(&self) -> &WorkflowReport {
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

impl<'a> PreparedWorkflow<'a> {
    pub fn report(&self) -> &WorkflowReport {
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

impl<'a> PreparedFeedChange<'a> {
    pub(super) fn from_state(writer: &'a mut LiveWriter, state: PreparedState) -> Self {
        Self {
            operation: PreparedOperation::new(writer, state),
        }
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

impl<'a> PreparedOperation<'a> {
    fn new(writer: &'a mut LiveWriter, state: PreparedState) -> Self {
        Self { writer, state }
    }

    fn set_metadata_json(&mut self, input: &[u8]) -> Result<bool> {
        self.state.set_metadata_json(self.writer, input)
    }

    fn clear_metadata_json(&mut self) -> Result<bool> {
        self.state.clear_metadata_json(self.writer)
    }

    fn commit(self) -> Result<CommitResult> {
        self.writer.commit_operation(&self.state.cancellation)
    }

    fn abort(self) -> Result<AbortResult> {
        self.writer.abort()
    }
}

impl PreparedState {
    pub(crate) fn new(cancellation: CancellationToken) -> Self {
        Self { cancellation }
    }

    pub(crate) fn set_metadata_json(
        &mut self,
        writer: &mut LiveWriter,
        input: &[u8],
    ) -> Result<bool> {
        self.check_or_abort(writer)?;
        let changed = self.abort_on_error(writer, |writer| writer.stage_metadata_json(input))?;
        self.check_or_abort(writer)?;
        Ok(changed)
    }

    pub(crate) fn clear_metadata_json(&mut self, writer: &mut LiveWriter) -> Result<bool> {
        self.check_or_abort(writer)?;
        let changed = self.abort_on_error(writer, LiveWriter::stage_clear_metadata_json)?;
        self.check_or_abort(writer)?;
        Ok(changed)
    }

    pub(crate) fn cancellation(&self) -> &CancellationToken {
        &self.cancellation
    }

    fn check_or_abort(&mut self, writer: &mut LiveWriter) -> Result<()> {
        self.cancellation
            .check()
            .map_err(|error| writer.abort_after(error))
    }

    fn abort_on_error<T>(
        &mut self,
        writer: &mut LiveWriter,
        operation: impl FnOnce(&mut LiveWriter) -> Result<T>,
    ) -> Result<T> {
        operation(writer).map_err(|error| {
            if writer.draft.is_some() {
                writer.abort_after(error)
            } else {
                error
            }
        })
    }
}

impl FinishedState {
    pub(crate) fn bind(self, writer: &mut LiveWriter) -> FinishedWorkflow<'_> {
        match self {
            Self::NoChange(report) => FinishedWorkflow::NoChange(report),
            Self::Changed { report, state } => FinishedWorkflow::Changed(PreparedWorkflow {
                report,
                operation: PreparedOperation::new(writer, state),
            }),
        }
    }

    pub(crate) fn report(&self) -> &WorkflowReport {
        match self {
            Self::NoChange(report) | Self::Changed { report, .. } => report,
        }
    }
}

impl Drop for PreparedOperation<'_> {
    fn drop(&mut self) {
        if let Some(draft) = self.writer.draft.as_mut() {
            draft.abandon_operation();
        }
    }
}

pub(super) fn drain_source<R, S, F>(
    source: &mut S,
    cancellation: &CancellationToken,
    input_records: &mut u64,
    mut apply: F,
) -> Result<()>
where
    R: Copy,
    S: RangeSource<R>,
    F: FnMut(R) -> Result<()>,
{
    loop {
        cancellation.check()?;
        let Some(batch) = source.next_batch()? else {
            return Ok(());
        };
        if batch.is_empty() {
            return Err(Error::InvalidArgument(
                "range source returned an empty batch",
            ));
        }
        for &record in batch {
            cancellation.check()?;
            let next = input_records
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("workflow input record count"))?;
            apply(record)?;
            *input_records = next;
        }
    }
}

pub(super) fn classify(comparison: &Comparison) -> crate::workflow::LogicalChange {
    if comparison.changed == Cardinality129::ZERO
        && comparison.added == Cardinality129::ZERO
        && comparison.removed == Cardinality129::ZERO
    {
        crate::workflow::LogicalChange::NoChange
    } else {
        crate::workflow::LogicalChange::Changed
    }
}

pub(super) fn require_ordered<K: Ord>(from: K, to: K) -> Result<()> {
    if from > to {
        Err(Error::InvalidArgument("range start exceeds range end"))
    } else {
        Ok(())
    }
}
