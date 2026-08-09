//! Recovery candidate identity shared by inspection, validation, and recovery.

mod api;
mod budget;
mod catalog;
mod catalog_table;
mod classify;
mod construction;
mod direct;
mod direct_build;
mod direct_output;
#[cfg(any(unix, windows))]
mod external_sort;
mod membership;
mod membership_blob;
mod membership_build;
mod membership_index;
mod membership_output;
mod membership_table;
mod membership_words;
mod metadata;
mod page_set;
mod range_build;
mod range_components;
mod range_scan;
mod report;
#[cfg(any(unix, windows))]
mod scratch;
mod scratch_maintenance;
pub(crate) mod source_guard;
mod tables;
mod terminal;
mod tree_scan;

use crate::validation::{LocalFileIdentity, ValidationProgress};

#[cfg(any(unix, windows))]
pub(crate) use scratch::{checkpoint_basename, ScratchCleanup, ScratchProblem, ScratchResidue};
#[cfg(all(test, target_os = "linux"))]
pub(crate) use scratch::{
    Scratch as RuntimeProbeScratch, HEADER_SIZE as RUNTIME_PROBE_HEADER_SIZE,
};
#[cfg(not(any(unix, windows)))]
#[derive(Clone, Debug)]
pub(crate) struct ScratchCleanup;

pub use api::{
    recover_immutable, recover_live, recover_offline, OfflineQuiescenceCertification,
    RecoveryOutcome,
};
pub(crate) use api::{recover_precreated_local, validate_worker_budget, WorkerMode};
pub use budget::RecoveryBudget;
pub use inspection::{inspect_recovery_candidates, RecoveryInspectionMode};
pub use report::{
    RecoveryLogicalCounts, RecoveryPageCounts, RecoveryReport, RecoverySink, RecoverySinkControl,
    RecoveryUnknownEnvelope,
};
pub(crate) use scratch_maintenance::remove_checkpointed_scratch;
pub use scratch_maintenance::{
    list_abandoned_scratch, remove_abandoned_scratch, AbandonedScratchAuthentication,
    AbandonedScratchEntry, AbandonedScratchList, AbandonedScratchSink, AbandonedScratchSinkControl,
    ScratchOwnerKind,
};
pub use source_guard::RecoverySourceCleanupGuard;
pub use terminal::{RecoveryPreparationFailure, RecoveryResult, RecoveryScratchAttempt};

pub(crate) mod inspection;

/// Exact classification of one recovery-readable retained metadata page.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum RecoveryCandidateLabel {
    Newest,
    Previous,
    UnorderedMeta0,
    UnorderedMeta1,
}

/// Opaque exact recovery-candidate token returned by candidate inspection.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RecoveryCandidate {
    pub(crate) label: RecoveryCandidateLabel,
    pub(crate) meta_page: u8,
    pub(crate) source_identity: LocalFileIdentity,
    pub(crate) database_id: [u8; 16],
    pub(crate) transaction_id: u64,
    pub(crate) commit_nonce: [u8; 16],
}

impl RecoveryCandidate {
    pub fn label(&self) -> RecoveryCandidateLabel {
        self.label
    }

    pub fn source_identity(&self) -> LocalFileIdentity {
        self.source_identity
    }

    pub fn database_id(&self) -> [u8; 16] {
        self.database_id
    }

    pub fn transaction_id(&self) -> u64 {
        self.transaction_id
    }

    pub fn commit_nonce(&self) -> [u8; 16] {
        self.commit_nonce
    }
}

/// Bounded recovery-candidate inspection result.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RecoveryCandidateInspection {
    pub source_identity: LocalFileIdentity,
    pub progress: ValidationProgress,
    candidates: [Option<RecoveryCandidate>; 2],
}

#[cfg(all(test, target_os = "linux"))]
#[path = "recovery/api_tests.rs"]
mod api_tests;

impl RecoveryCandidateInspection {
    pub fn candidate_count(&self) -> usize {
        self.candidates.iter().flatten().count()
    }

    pub fn candidate(&self, index: usize) -> Option<&RecoveryCandidate> {
        self.candidates.iter().flatten().nth(index)
    }

    pub fn candidates(&self) -> impl Iterator<Item = &RecoveryCandidate> {
        self.candidates.iter().flatten()
    }

    pub(crate) fn new(
        source_identity: LocalFileIdentity,
        progress: ValidationProgress,
        candidates: [Option<RecoveryCandidate>; 2],
    ) -> Self {
        Self {
            source_identity,
            progress,
            candidates,
        }
    }
}
