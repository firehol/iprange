//! Fixed maintenance-entry conversion.

use std::mem::size_of;

use iprange_livedb::publication;
use iprange_livedb::recovery;

use crate::abi_extra::{
    ArtifactRecord, HousekeepingRecord, OptionalIdentity, PublicationDigest, PublicationTuple,
};
use crate::facts;
use crate::registry;

pub(crate) fn scratch(value: &recovery::AbandonedScratchEntry) -> ArtifactRecord {
    ArtifactRecord {
        abi_version: 1,
        struct_size: size_of::<ArtifactRecord>() as u32,
        record_kind: registry::ARTIFACT_RECORD_KIND_AUTHORIZED_SCRATCH,
        authentication: match value.authentication {
            recovery::AbandonedScratchAuthentication::Unauthenticated => {
                registry::SCRATCH_AUTHENTICATION_UNAUTHENTICATED
            }
            recovery::AbandonedScratchAuthentication::Authenticated(
                recovery::ScratchOwnerKind::Validation,
            ) => registry::SCRATCH_AUTHENTICATION_VALIDATION,
            recovery::AbandonedScratchAuthentication::Authenticated(
                recovery::ScratchOwnerKind::Recovery,
            ) => registry::SCRATCH_AUTHENTICATION_RECOVERY,
        },
        directory_identity: facts::identity(value.directory_identity),
        artifact_identity: facts::optional_identity(Some(value.artifact_identity)),
        output_identity: OptionalIdentity::default(),
        attempt_id: value.attempt_id,
        ordinal: value.ordinal,
        ..ArtifactRecord::default()
    }
}

pub(crate) fn publication_temp(
    value: &publication::AbandonedPublicationTempEntry,
) -> ArtifactRecord {
    ArtifactRecord {
        abi_version: 1,
        struct_size: size_of::<ArtifactRecord>() as u32,
        record_kind: registry::ARTIFACT_RECORD_KIND_PUBLICATION_TEMP,
        directory_identity: facts::identity(value.directory_identity),
        artifact_identity: facts::optional_identity(Some(value.artifact_identity)),
        output_identity: OptionalIdentity::default(),
        attempt_id: value.publication_attempt_id,
        tuple_present: u8::from(value.tuple.is_some()),
        digest_present: u8::from(value.digest.is_some()),
        tuple: value
            .tuple
            .map_or_else(PublicationTuple::default, facts::tuple),
        digest: value
            .digest
            .map_or_else(PublicationDigest::default, facts::digest),
        ..ArtifactRecord::default()
    }
}

pub(crate) fn reservation(value: &publication::AbandonedReservationEntry) -> ArtifactRecord {
    let evidence = value.evidence;
    let previous = evidence.and_then(|value| value.previous);
    ArtifactRecord {
        abi_version: 1,
        struct_size: size_of::<ArtifactRecord>() as u32,
        record_kind: registry::ARTIFACT_RECORD_KIND_PUBLICATION_RESERVATION,
        directory_identity: facts::identity(value.directory_identity),
        artifact_identity: facts::optional_identity(Some(value.artifact_identity)),
        output_identity: facts::optional_identity(evidence.map(|value| value.output.identity)),
        attempt_id: value.publication_attempt_id,
        policy: evidence.map_or(0, |value| match value.policy {
            publication::AbandonedReservationPolicy::FailIfExists => {
                registry::DESTINATION_POLICY_FAIL_IF_EXISTS
            }
            publication::AbandonedReservationPolicy::ReplaceExisting => {
                registry::DESTINATION_POLICY_REPLACE_EXISTING
            }
            publication::AbandonedReservationPolicy::ReplaceExistingNoRollback => {
                registry::DESTINATION_POLICY_REPLACE_EXISTING_NO_ROLLBACK
            }
        }),
        phase: evidence.map_or(0, |value| match value.phase {
            publication::AbandonedReservationPhase::Prepared => {
                registry::ABANDONED_RESERVATION_PHASE_PREPARED
            }
            publication::AbandonedReservationPhase::MainMayHaveBeenAttempted => {
                registry::ABANDONED_RESERVATION_PHASE_MAIN_MAY_HAVE_BEEN_ATTEMPTED
            }
        }),
        tuple_present: u8::from(evidence.is_some()),
        digest_present: u8::from(evidence.is_some()),
        previous_present: u8::from(previous.is_some()),
        tuple: evidence.map_or_else(PublicationTuple::default, |value| {
            facts::tuple(value.output.tuple)
        }),
        digest: evidence.map_or_else(PublicationDigest::default, |value| {
            facts::digest(value.output.digest)
        }),
        previous_identity: previous.map_or_else(Default::default, |value| facts::identity(value.0)),
        previous_digest: previous
            .map_or_else(PublicationDigest::default, |value| facts::digest(value.1)),
        ..ArtifactRecord::default()
    }
}

pub(crate) fn housekeeping(value: &publication::WindowsHousekeepingEntry) -> HousekeepingRecord {
    HousekeepingRecord {
        abi_version: 1,
        struct_size: size_of::<HousekeepingRecord>() as u32,
        candidate_kind: match value.candidate_kind {
            publication::WindowsHousekeepingCandidateKind::Envelope => {
                registry::WINDOWS_HOUSEKEEPING_CANDIDATE_ENVELOPE
            }
            publication::WindowsHousekeepingCandidateKind::InertPayload => {
                registry::WINDOWS_HOUSEKEEPING_CANDIDATE_INERT_PAYLOAD
            }
        },
        basename: facts::basename(value.basename_encoding, &value.basename),
        directory_identity: facts::identity(value.directory_identity),
        identity: facts::optional_identity(value.identity),
        attempt_id: facts::optional_bytes16(value.attempt_id),
        ordinal: facts::optional_u32(value.ordinal),
        artifact_present: u8::from(value.artifact.is_some()),
        problem_present: u8::from(value.problem.is_some()),
        reserved: [0; 6],
        artifact: value
            .artifact
            .as_ref()
            .map_or_else(Default::default, facts::housekeeping_artifact),
        problem_code: value.problem.as_ref().map_or(0, |value| value.code as u32),
        problem_os_code_present: u8::from(
            value
                .problem
                .as_ref()
                .and_then(|value| value.os_code)
                .is_some(),
        ),
        reserved1: [0; 3],
        problem_os_code: value
            .problem
            .as_ref()
            .and_then(|value| value.os_code)
            .unwrap_or(0),
        reserved2: 0,
    }
}
