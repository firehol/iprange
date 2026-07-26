//! Explicit offline transitions between immutable and live operation.

mod create_resolution;
mod namespace;
mod residue;
mod resolution;
mod transition;

use crate::error::Error;
use crate::live_writer::LocalBasename;
use crate::validation::LocalFileIdentity;

pub use create_resolution::resolve_create_live;
pub use residue::{
    resolve_interrupted_live_transition, LiveResidueKind, LiveResidueResult, LiveResidueStatus,
};
pub use resolution::{resolve_live_transition, LiveTransitionResolutionMode};
pub use transition::{initialize_live, reset_live_coordination};

/// Offline live-coordination operation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LiveTransitionOperation {
    Initialize,
    Reset,
}

/// Namespace guarantee selected for replacing existing live coordination.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LiveResetPolicy {
    RollbackSafe,
    DiscardPrevious,
}

/// Factual state after one offline transition attempt.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LiveTransitionStatus {
    Unchanged,
    Initialized,
    OutcomeUnknown,
}

/// Last proven location of the new coordination inode.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LiveCoordinationLocation {
    Absent,
    Canonical,
    Private,
    Unclassified,
}

/// Exact facts retained for transition resolution and cleanup.
#[derive(Debug)]
pub struct LiveTransitionResult {
    pub operation: LiveTransitionOperation,
    pub reset_policy: Option<LiveResetPolicy>,
    pub status: LiveTransitionStatus,
    pub database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
    pub directory_identity: LocalFileIdentity,
    pub main_identity: LocalFileIdentity,
    pub main_basename: LocalBasename,
    pub reader_capacity: u32,
    pub sidecar_id: [u8; 16],
    pub previous_sidecar_identity: Option<LocalFileIdentity>,
    pub new_sidecar_identity: Option<LocalFileIdentity>,
    pub new_sidecar_location: LiveCoordinationLocation,
    pub residue_possible: bool,
    pub cause: Option<Error>,
}
