//! Fixed publication facts and the two-entry direct-operation cleanup ledger.

use crate::validation::LocalFileIdentity;

use super::namespace::Identity;
use super::output::PreparedOutput;
use super::problem::Problem;

const POSIX_KIND: u16 = 1;
const CLEANUP_CAPACITY: usize = 2;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PublicationStatus {
    NotPublished,
    Published,
    OutcomeUnknown,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum DestinationContent {
    Desired,
    Absent,
    Other,
    Unclassified,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum LaterCanonical {
    None,
    ReservationOrTransition,
    ReadyLiveSidecar,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum AccessPolicy {
    Absent,
    CreatorOnly,
    ChangedOrUnproven,
    Unclassified,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum CleanupState {
    Clean,
    ResiduePossible,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ArtifactKind {
    PrivateOutput,
    PrivateReservation,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum NameSlot {
    PrivateOutput,
    PrivateReservation,
    Coordination,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct CreationSecurity {
    pub(crate) kind: u16,
    pub(crate) commitment: [u8; 32],
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct CleanupArtifact {
    pub(crate) kind: ArtifactKind,
    pub(crate) directory_identity: LocalFileIdentity,
    pub(crate) basename_encoding: u16,
    pub(crate) basename: Box<[u8]>,
    pub(crate) identity: Option<LocalFileIdentity>,
    pub(crate) creation_security: CreationSecurity,
    pub(crate) error: Problem,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct CleanupArtifacts {
    entries: [Option<CleanupArtifact>; CLEANUP_CAPACITY],
    len: usize,
}

impl CleanupArtifacts {
    pub(super) const fn new() -> Self {
        Self {
            entries: [None, None],
            len: 0,
        }
    }

    pub(super) fn push(&mut self, artifact: CleanupArtifact) {
        assert!(self.len < CLEANUP_CAPACITY, "fixed cleanup ledger overflow");
        self.entries[self.len] = Some(artifact);
        self.len += 1;
    }

    pub(crate) const fn len(&self) -> usize {
        self.len
    }

    pub(crate) const fn state(&self) -> CleanupState {
        if self.len == 0 {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }

    pub(crate) fn get(&self, index: usize) -> Option<&CleanupArtifact> {
        self.entries.get(index)?.as_ref()
    }

    pub(crate) fn iter(&self) -> impl Iterator<Item = &CleanupArtifact> {
        self.entries[..self.len].iter().flatten()
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct AttemptFacts {
    pub(crate) database_id: [u8; 16],
    pub(crate) transaction_id: u64,
    pub(crate) commit_nonce: [u8; 16],
    pub(crate) publication_attempt_id: [u8; 16],
    pub(crate) directory_identity: LocalFileIdentity,
    pub(crate) destination_basename_encoding: u16,
    pub(crate) destination_basename: Box<[u8]>,
    pub(crate) output_identity: LocalFileIdentity,
    pub(crate) output_byte_length: u64,
    pub(crate) output_sha512: [u8; 64],
    pub(crate) reservation_identity: LocalFileIdentity,
    pub(crate) creation_security: CreationSecurity,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct PublicationResult {
    pub(crate) attempt: AttemptFacts,
    pub(crate) main_namespace_may_have_been_attempted: bool,
    pub(crate) publication: PublicationStatus,
    pub(crate) destination_content: DestinationContent,
    pub(crate) later_canonical: LaterCanonical,
    pub(crate) main_access_policy: AccessPolicy,
    pub(crate) coordination_access_policy: AccessPolicy,
    pub(crate) cleanup: CleanupArtifacts,
    pub(crate) cause: Option<Problem>,
}

impl PublicationResult {
    pub(crate) const fn cleanup_state(&self) -> CleanupState {
        self.cleanup.state()
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct PreparationFailure {
    pub(crate) publication_attempt_id: [u8; 16],
    pub(crate) directory_identity: LocalFileIdentity,
    pub(crate) private_output_basename: Box<[u8]>,
    pub(crate) output_identity: LocalFileIdentity,
    pub(crate) creation_security: CreationSecurity,
    pub(crate) cleanup: CleanupArtifacts,
    pub(crate) cause: Problem,
}

impl PreparationFailure {
    pub(crate) const fn cleanup_state(&self) -> CleanupState {
        self.cleanup.state()
    }
}

pub(super) struct Seed {
    database_id: [u8; 16],
    transaction_id: u64,
    commit_nonce: [u8; 16],
    attempt_id: [u8; 16],
    directory_identity: LocalFileIdentity,
    destination_basename: Box<[u8]>,
    output_identity: LocalFileIdentity,
    output_byte_length: u64,
    output_sha512: [u8; 64],
    creation_security: CreationSecurity,
    private_output_basename: Box<[u8]>,
    names: Names,
}

pub(super) struct FinalState {
    pub(super) reservation_identity: Identity,
    pub(super) publication: PublicationStatus,
    pub(super) destination_content: DestinationContent,
    pub(super) main_access_policy: AccessPolicy,
    pub(super) coordination_access_policy: AccessPolicy,
}

struct Names {
    private_output: Option<Box<[u8]>>,
    private_reservation: Option<Box<[u8]>>,
    coordination: Option<Box<[u8]>>,
}

impl Seed {
    pub(super) fn capture(output: &PreparedOutput) -> Self {
        let destination = output.attempt.destination();
        let reservation = destination
            .reservation_name(output.attempt.attempt_id())
            .expect("prepared attempt has a valid reservation name");
        Self {
            database_id: output.meta.database_id,
            transaction_id: output.meta.txn_id,
            commit_nonce: output.meta.commit_nonce,
            attempt_id: output.attempt.attempt_id(),
            directory_identity: local(destination.directory().identity()),
            destination_basename: destination.main().bytes().into(),
            output_identity: local(output.attempt.identity()),
            output_byte_length: output.byte_length,
            output_sha512: output.sha512,
            creation_security: CreationSecurity {
                kind: POSIX_KIND,
                commitment: destination.security_commitment(),
            },
            private_output_basename: output.attempt.name().bytes().into(),
            names: Names {
                private_output: Some(output.attempt.name().bytes().into()),
                private_reservation: Some(reservation.bytes().into()),
                coordination: Some(destination.coordination().bytes().into()),
            },
        }
    }

    pub(super) fn result(
        self,
        state: FinalState,
        cleanup: CleanupArtifacts,
        cause: Option<Problem>,
    ) -> PublicationResult {
        PublicationResult {
            attempt: AttemptFacts {
                database_id: self.database_id,
                transaction_id: self.transaction_id,
                commit_nonce: self.commit_nonce,
                publication_attempt_id: self.attempt_id,
                directory_identity: self.directory_identity,
                destination_basename_encoding: POSIX_KIND,
                destination_basename: self.destination_basename,
                output_identity: self.output_identity,
                output_byte_length: self.output_byte_length,
                output_sha512: self.output_sha512,
                reservation_identity: local(state.reservation_identity),
                creation_security: self.creation_security,
            },
            main_namespace_may_have_been_attempted: matches!(
                state.publication,
                PublicationStatus::Published | PublicationStatus::OutcomeUnknown
            ),
            publication: state.publication,
            destination_content: state.destination_content,
            later_canonical: LaterCanonical::None,
            main_access_policy: state.main_access_policy,
            coordination_access_policy: state.coordination_access_policy,
            cleanup,
            cause,
        }
    }

    pub(super) fn preparation(
        self,
        cleanup: CleanupArtifacts,
        cause: Problem,
    ) -> PreparationFailure {
        PreparationFailure {
            publication_attempt_id: self.attempt_id,
            directory_identity: self.directory_identity,
            private_output_basename: self.private_output_basename,
            output_identity: self.output_identity,
            creation_security: self.creation_security,
            cleanup,
            cause,
        }
    }

    pub(super) fn artifact(
        &mut self,
        kind: ArtifactKind,
        name: NameSlot,
        identity: Option<Identity>,
        error: Problem,
    ) -> CleanupArtifact {
        CleanupArtifact {
            kind,
            directory_identity: self.directory_identity,
            basename_encoding: POSIX_KIND,
            basename: self.take_name(name),
            identity: identity.map(local),
            creation_security: self.creation_security.clone(),
            error,
        }
    }

    fn take_name(&mut self, slot: NameSlot) -> Box<[u8]> {
        let name = match slot {
            NameSlot::PrivateOutput => &mut self.names.private_output,
            NameSlot::PrivateReservation => &mut self.names.private_reservation,
            NameSlot::Coordination => &mut self.names.coordination,
        };
        name.take().expect("each artifact name is consumed once")
    }
}

fn local(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: POSIX_KIND,
        bytes: identity.encode(),
    }
}
