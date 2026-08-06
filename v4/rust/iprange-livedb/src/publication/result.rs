//! Construction and validation of portable publication result facts.

use crate::error::ErrorCode;
use crate::validation::LocalFileIdentity;

use super::namespace::{
    Destination, Identity, NamespaceError, BASENAME_ENCODING_KIND, CREATION_SECURITY_KIND,
    IDENTITY_KIND,
};
use super::output::PreparedOutput;
use super::problem::Problem;
use super::reservation::{self, Header, Policy, State};
pub(crate) use super::types::{
    AccessPolicy, ArtifactKind, CleanupArtifact, CleanupArtifacts, CoordinationCleanup,
    CreationSecurity, DestinationContent, DirectoryRole, Housekeeping, LaterCanonical,
    PreviousDestination, PublicationAttempt as AttemptFacts, PublicationPolicy,
    PublicationPreparationFailure as PreparationFailure, PublicationResult, PublicationStatus,
};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum NameSlot {
    PrivateOutput,
    PrivateReservation,
    Coordination,
}

impl PublicationResult {
    pub(super) fn header_for(&self, destination: &Destination) -> Result<Header, Problem> {
        require_result_binding(self, destination)?;
        let state = if self.main_namespace_may_have_been_attempted {
            State::MainMayHaveBeenAttempted
        } else {
            State::Prepared
        };
        let previous =
            self.attempt
                .previous_destination
                .as_ref()
                .map(|previous| reservation::Previous {
                    identity: previous.identity.bytes,
                    byte_length: previous.byte_length,
                    sha512: previous.sha512,
                });
        let header = Header {
            state,
            database_id: self.attempt.database_id,
            transaction_id: self.attempt.transaction_id,
            commit_nonce: self.attempt.commit_nonce,
            attempt_id: self.attempt.publication_attempt_id,
            reservation_identity: self.attempt.reservation_identity.bytes,
            policy: match self.attempt.publication_policy {
                PublicationPolicy::FailIfExists => Policy::FailIfExists,
                PublicationPolicy::ReplaceExisting => Policy::ReplaceExisting,
                PublicationPolicy::ReplaceExistingNoRollback => Policy::ReplaceExistingNoRollback,
            },
            output_byte_length: self.attempt.output_byte_length,
            output_identity: self.attempt.output_identity.bytes,
            output_sha512: self.attempt.output_sha512,
            previous,
            basename_len: u32::try_from(destination.main().bytes().len())
                .map_err(|_| destination_name_mismatch())?,
            basename_commitment: destination.basename_commitment(),
            security_commitment: self.attempt.creation_security.commitment,
            sequence: state as u64,
        };
        Ok(header)
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
    publication_policy: PublicationPolicy,
    previous_destination: Option<PreviousDestination>,
    creation_security: CreationSecurity,
    private_output_basename: Box<[u8]>,
    names: Names,
}

pub(super) struct FinalState {
    pub(super) reservation_identity: Identity,
    pub(super) main_namespace_may_have_been_attempted: bool,
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
            publication_policy: public_policy(output.policy),
            previous_destination: output
                .previous
                .as_ref()
                .map(|previous| PreviousDestination {
                    identity: local(previous.identity),
                    byte_length: previous.byte_length,
                    sha512: previous.sha512,
                }),
            creation_security: CreationSecurity {
                kind: CREATION_SECURITY_KIND,
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

    pub(super) fn reconstruct(
        destination: &Destination,
        header: Header,
    ) -> Result<Self, NamespaceError> {
        let private_output = destination.output_name(header.attempt_id)?;
        let private_reservation = destination.reservation_name(header.attempt_id)?;
        Ok(Self {
            database_id: header.database_id,
            transaction_id: header.transaction_id,
            commit_nonce: header.commit_nonce,
            attempt_id: header.attempt_id,
            directory_identity: local(destination.directory().identity()),
            destination_basename: destination.main().bytes().into(),
            output_identity: local_raw(header.output_identity),
            output_byte_length: header.output_byte_length,
            output_sha512: header.output_sha512,
            publication_policy: public_policy(header.policy),
            previous_destination: header.previous.map(|previous| PreviousDestination {
                identity: local_raw(previous.identity),
                byte_length: previous.byte_length,
                sha512: previous.sha512,
            }),
            creation_security: CreationSecurity {
                kind: CREATION_SECURITY_KIND,
                commitment: header.security_commitment,
            },
            private_output_basename: private_output.bytes().into(),
            names: Names {
                private_output: Some(private_output.bytes().into()),
                private_reservation: Some(private_reservation.bytes().into()),
                coordination: Some(destination.coordination().bytes().into()),
            },
        })
    }

    #[cfg(windows)]
    pub(super) const fn attempt_id(&self) -> [u8; 16] {
        self.attempt_id
    }

    #[cfg(windows)]
    pub(super) const fn creation_security(&self) -> &CreationSecurity {
        &self.creation_security
    }

    #[cfg(windows)]
    pub(super) const fn output_payload(&self) -> Option<super::gc_codec::Payload> {
        Some(super::gc_codec::Payload {
            byte_length: self.output_byte_length,
            sha512: self.output_sha512,
            database_id: self.database_id,
            transaction_id: self.transaction_id,
            commit_nonce: self.commit_nonce,
        })
    }

    pub(super) fn result(
        self,
        state: FinalState,
        cleanup: CleanupArtifacts,
        cause: Option<Problem>,
    ) -> PublicationResult {
        self.result_with_housekeeping(state, cleanup, Housekeeping::None, Vec::new(), cause)
    }

    pub(super) fn result_with_housekeeping(
        self,
        state: FinalState,
        cleanup: CleanupArtifacts,
        housekeeping: Housekeeping,
        visible_housekeeping: Vec<super::HousekeepingArtifact>,
        cause: Option<Problem>,
    ) -> PublicationResult {
        PublicationResult {
            attempt: AttemptFacts {
                database_id: self.database_id,
                transaction_id: self.transaction_id,
                commit_nonce: self.commit_nonce,
                publication_attempt_id: self.attempt_id,
                directory_identity: self.directory_identity,
                destination_basename_encoding: BASENAME_ENCODING_KIND,
                destination_basename: self.destination_basename,
                output_identity: self.output_identity,
                output_byte_length: self.output_byte_length,
                output_sha512: self.output_sha512,
                publication_policy: self.publication_policy,
                previous_destination: self.previous_destination,
                reservation_identity: local(state.reservation_identity),
                creation_security: self.creation_security,
            },
            main_namespace_may_have_been_attempted: state.main_namespace_may_have_been_attempted,
            publication: state.publication,
            destination_content: state.destination_content,
            later_canonical: LaterCanonical::None,
            live_lineage: None,
            later_attempt_or_sidecar_id: None,
            later_selected_transaction_id: None,
            later_selected_commit_nonce: None,
            main_access_policy: state.main_access_policy,
            coordination_access_policy: state.coordination_access_policy,
            cleanup,
            coordination_cleanup: CoordinationCleanup::None,
            housekeeping,
            visible_housekeeping: visible_housekeeping.into_boxed_slice(),
            cause,
        }
    }

    pub(super) fn preparation_with_housekeeping(
        self,
        cleanup: CleanupArtifacts,
        housekeeping: Housekeeping,
        visible_housekeeping: Vec<super::HousekeepingArtifact>,
        cause: Problem,
    ) -> PreparationFailure {
        PreparationFailure {
            publication_attempt_id: self.attempt_id,
            directory_identity: self.directory_identity,
            private_output_basename_encoding: BASENAME_ENCODING_KIND,
            private_output_basename: self.private_output_basename,
            output_identity: self.output_identity,
            creation_security: self.creation_security,
            cleanup,
            coordination_cleanup: CoordinationCleanup::None,
            housekeeping,
            visible_housekeeping: visible_housekeeping.into_boxed_slice(),
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
            directory_role: DirectoryRole::Destination,
            directory_identity: self.directory_identity,
            basename_encoding: BASENAME_ENCODING_KIND,
            basename: self.take_name(name),
            identity: identity.map(local),
            creation_security: Some(self.creation_security.clone()),
            unpublished_tail: None,
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

const fn public_policy(policy: Policy) -> PublicationPolicy {
    match policy {
        Policy::FailIfExists => PublicationPolicy::FailIfExists,
        Policy::ReplaceExisting => PublicationPolicy::ReplaceExisting,
        Policy::ReplaceExistingNoRollback => PublicationPolicy::ReplaceExistingNoRollback,
    }
}

fn local(identity: Identity) -> LocalFileIdentity {
    local_raw(identity.encode())
}

fn local_raw(bytes: [u8; 32]) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: IDENTITY_KIND,
        bytes,
    }
}

fn require_result_binding(
    result: &PublicationResult,
    destination: &Destination,
) -> Result<(), Problem> {
    let directory = local(destination.directory().identity());
    if result.attempt.directory_identity != directory {
        return Err(Problem::new(
            ErrorCode::DirectoryIdentityMismatch,
            None,
            "caller publication result belongs to another directory",
        ));
    }
    if result.attempt.destination_basename_encoding != BASENAME_ENCODING_KIND
        || result.attempt.destination_basename.as_ref() != destination.main().bytes()
    {
        return Err(destination_name_mismatch());
    }
    if result.attempt.output_identity.kind != IDENTITY_KIND
        || result.attempt.reservation_identity.kind != IDENTITY_KIND
        || result.attempt.creation_security.kind != CREATION_SECURITY_KIND
        || result
            .attempt
            .previous_destination
            .as_ref()
            .is_some_and(|previous| previous.identity.kind != IDENTITY_KIND)
    {
        return Err(conflict(
            "caller publication result has another identity kind",
        ));
    }
    Ok(())
}

const fn destination_name_mismatch() -> Problem {
    Problem::new(
        ErrorCode::DestinationNameMismatch,
        None,
        "caller publication result belongs to another destination name",
    )
}

const fn conflict(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::Conflict, None, detail)
}
