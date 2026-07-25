//! Recovery candidate identity shared by inspection, validation, and recovery.

#[allow(dead_code)]
mod bounded_vec;
#[allow(dead_code)]
mod budget;
#[allow(dead_code)]
mod catalog;
mod classify;
#[allow(dead_code)]
mod direct;
#[allow(dead_code)]
mod direct_build;
#[allow(dead_code)]
mod direct_output;
#[cfg(target_os = "linux")]
#[allow(dead_code)]
mod external_sort;
#[allow(dead_code)]
mod membership;
#[allow(dead_code)]
mod membership_blob;
#[allow(dead_code)]
mod membership_build;
#[allow(dead_code)]
mod membership_index;
#[allow(dead_code)]
mod membership_output;
#[allow(dead_code)]
mod metadata;
#[allow(dead_code)]
mod page_set;
#[allow(dead_code)]
mod range_build;
#[allow(dead_code)]
mod range_scan;
#[allow(dead_code)]
mod report;
#[cfg(target_os = "linux")]
#[allow(dead_code)]
mod scratch;
#[allow(dead_code)]
mod tree_scan;

use crate::validation::{LocalFileIdentity, ValidationProgress};

#[cfg(target_os = "linux")]
pub(crate) use scratch::ScratchCleanup;
#[cfg(not(target_os = "linux"))]
#[derive(Clone, Debug)]
pub(crate) struct ScratchCleanup;

pub use budget::RecoveryBudget;
pub use inspection::{inspect_recovery_candidates, RecoveryInspectionMode};
pub use report::{
    RecoveryLogicalCounts, RecoveryPageCounts, RecoveryReport, RecoverySink, RecoverySinkControl,
    RecoveryUnknownEnvelope,
};

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
