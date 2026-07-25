//! Recovery candidate identity shared by inspection, validation, and recovery.

use crate::validation::LocalFileIdentity;

/// Exact classification of one recovery-readable retained metadata page.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum RecoveryCandidateLabel {
    Newest,
    Previous,
    UnorderedMeta0,
    UnorderedMeta1,
}

/// Opaque exact recovery-candidate token returned by candidate inspection.
#[derive(Clone, Debug, PartialEq, Eq)]
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
