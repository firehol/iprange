//! Shared exact-workflow input and prepared-draft ownership.

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::AddressFamily;
use crate::error::{Error, Result};
use crate::workflow::{Comparison, WorkflowReport};
use crate::writer_core::WriterEdit;

use super::{AbortResult, CommitResult, LiveWriter};

pub(super) use crate::source::drain as drain_source;

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
pub(super) struct PreparedOperation<'a> {
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
    pub(super) fn new(writer: &'a mut LiveWriter, state: PreparedState) -> Self {
        Self { writer, state }
    }

    pub(super) fn set_metadata_json(&mut self, input: &[u8]) -> Result<bool> {
        self.state.set_metadata_json(self.writer, input)
    }

    pub(super) fn clear_metadata_json(&mut self) -> Result<bool> {
        self.state.clear_metadata_json(self.writer)
    }

    pub(super) fn commit(self) -> Result<CommitResult> {
        self.writer.commit_operation(&self.state.cancellation)
    }

    pub(super) fn abort(self) -> Result<AbortResult> {
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
            if writer.core.has_draft() {
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

pub(super) fn complete_workflow(
    writer: &mut LiveWriter,
    report: WorkflowReport,
    cancellation: CancellationToken,
    finish_changed: impl FnOnce(&mut WriterEdit<'_>, &CancellationToken) -> Result<()>,
) -> Result<FinishedState> {
    if report.logical_change == crate::workflow::LogicalChange::NoChange {
        writer.discard_draft()?;
        return Ok(FinishedState::NoChange(report));
    }
    writer.mutate(|edit| finish_changed(edit, &cancellation))?;
    Ok(FinishedState::Changed {
        report,
        state: PreparedState::new(cancellation),
    })
}

impl Drop for PreparedOperation<'_> {
    fn drop(&mut self) {
        self.writer.core.abandon_operation();
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

pub(super) fn require_transaction(
    writer: &LiveWriter,
    nonce: [u8; 16],
    inactive: &'static str,
) -> Result<()> {
    if !writer.core.operation_is(nonce) {
        return Err(Error::WrongState(inactive));
    }
    writer.require_healthy()
}

pub(super) fn check_transaction(
    writer: &mut LiveWriter,
    nonce: [u8; 16],
    cancellation: &CancellationToken,
    inactive: &'static str,
) -> Result<()> {
    require_transaction(writer, nonce, inactive)?;
    cancellation
        .check()
        .map_err(|cause| writer.abort_after(cause))
}

pub(super) fn run_transaction<T>(
    writer: &mut LiveWriter,
    nonce: [u8; 16],
    cancellation: &CancellationToken,
    inactive: &'static str,
    operation: impl FnOnce(&mut LiveWriter) -> Result<T>,
) -> Result<T> {
    check_transaction(writer, nonce, cancellation, inactive)?;
    let result = operation(writer);
    if result.is_ok() {
        check_transaction(writer, nonce, cancellation, inactive)?;
    }
    result
}

pub(super) fn require_input_active(writer: &LiveWriter) -> Result<()> {
    writer.require_healthy()?;
    if writer.core.workflow_input_open() {
        Ok(())
    } else {
        Err(Error::WrongState("workflow input is not active"))
    }
}

pub(super) fn require_input_family(writer: &mut LiveWriter, family: AddressFamily) -> Result<()> {
    require_input_active(writer)?;
    if writer.core.base_info().address_family == family {
        Ok(())
    } else {
        Err(writer.abort_after(Error::WrongAddressFamily(
            "range family does not match the database",
        )))
    }
}
