//! Explicit offline transitions between immutable and live operation.

mod create_resolution;
pub(crate) mod creation;
mod namespace;
mod residue;
mod resolution;
mod transition;

use crate::error::Error;
use crate::live_writer::LocalBasename;
use crate::publication::{Housekeeping, HousekeepingArtifact};
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
    pub housekeeping: Housekeeping,
    pub visible_housekeeping: Box<[HousekeepingArtifact]>,
    pub cause: Option<Error>,
}

#[cfg(all(test, target_os = "freebsd"))]
mod freebsd_tests {
    use std::fs;

    use crate::contract::{AddressFamily, ValueKind, ValueTag};
    use crate::error::ErrorCode;
    use crate::live_writer::{CreateResult, CreationState};
    use crate::publication::Housekeeping;

    use super::*;

    #[test]
    fn result_bearing_live_resolvers_reject_before_path_access() {
        let directory = crate::test_support_tests::unique_path("iprange-v4-freebsd-live-resolvers");
        fs::create_dir(&directory).unwrap();
        let main = directory.join("main.v4");
        let basename = LocalBasename::from_path(&main).unwrap();
        let identity = LocalFileIdentity {
            kind: 1,
            bytes: [1; 32],
        };

        let create = CreateResult {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::FIRST_SEEN,
            database_id: [1; 16],
            commit_nonce: [1; 16],
            sidecar_id: [2; 16],
            directory_identity: Some(identity),
            main_basename: basename,
            main_identity: None,
            sidecar_identity: None,
            reader_capacity: 1,
            state: CreationState::OutcomeUnknown,
            residue_possible: true,
            housekeeping: Housekeeping::None,
            visible_housekeeping: Box::new([]),
            cause: None,
        };
        let error = resolve_create_live(
            &main,
            &create,
            LiveTransitionResolutionMode::Complete,
            &crate::CancellationToken::new(),
        )
        .unwrap_err();
        assert_eq!(error.code(), ErrorCode::LiveCoordinationUnsupported);

        let transition = LiveTransitionResult {
            operation: LiveTransitionOperation::Initialize,
            reset_policy: None,
            status: LiveTransitionStatus::OutcomeUnknown,
            database_id: [1; 16],
            transaction_id: 1,
            commit_nonce: [1; 16],
            directory_identity: identity,
            main_identity: identity,
            main_basename: basename,
            reader_capacity: 1,
            sidecar_id: [2; 16],
            previous_sidecar_identity: None,
            new_sidecar_identity: None,
            new_sidecar_location: LiveCoordinationLocation::Unclassified,
            residue_possible: true,
            housekeeping: Housekeeping::None,
            visible_housekeeping: Box::new([]),
            cause: None,
        };
        let error = resolve_live_transition(
            &main,
            &transition,
            LiveTransitionResolutionMode::Complete,
            &crate::CancellationToken::new(),
        )
        .unwrap_err();
        assert_eq!(error.code(), ErrorCode::LiveCoordinationUnsupported);
        assert!(fs::read_dir(&directory).unwrap().next().is_none());
        fs::remove_dir(&directory).unwrap();
    }
}
