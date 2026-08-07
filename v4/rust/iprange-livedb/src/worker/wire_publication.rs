use crate::error::{Error, Result};
use crate::publication::{
    AccessPolicy, ArtifactKind, ArtifactPresence, CleanupArtifact, CleanupArtifacts,
    CoordinationCleanup, CreationSecurity, DestinationContent, DirectoryRole, Housekeeping,
    HousekeepingArtifact, HousekeepingState, LaterCanonical, LiveLineage, PreviousDestination,
    PrivateOutputAttempt, PublicationAttempt, PublicationPolicy, PublicationProblem,
    PublicationResult, PublicationStatus, UnpublishedTailFacts,
};

use super::wire::{self, Reader, Writer};

const MAX_HOUSEKEEPING: usize = 8;

pub(super) fn private_output(output: &mut Writer<'_>, value: &PrivateOutputAttempt) -> Result<()> {
    output.bytes(&value.publication_attempt_id)?;
    wire::identity(output, value.directory_identity)?;
    output.u16(value.basename_encoding)?;
    output.sized_bytes(&value.basename)?;
    optional_identity(output, value.identity)?;
    creation_security(output, &value.creation_security)
}

pub(super) fn read_private_output(input: &mut Reader<'_>) -> Result<PrivateOutputAttempt> {
    Ok(PrivateOutputAttempt {
        publication_attempt_id: input.array()?,
        directory_identity: wire::read_identity(input)?,
        basename_encoding: input.u16()?,
        basename: input.boxed_bytes()?,
        identity: read_optional_identity(input)?,
        creation_security: read_creation_security(input)?,
    })
}

pub(super) fn result(output: &mut Writer<'_>, value: &PublicationResult) -> Result<()> {
    attempt(output, &value.attempt)?;
    output.bool(value.main_namespace_may_have_been_attempted)?;
    output.byte(publication_status(value.publication))?;
    output.byte(destination_content(value.destination_content))?;
    output.byte(later_canonical(value.later_canonical))?;
    optional_byte(output, value.live_lineage.map(live_lineage))?;
    optional_array(output, value.later_attempt_or_sidecar_id)?;
    optional_u64(output, value.later_selected_transaction_id)?;
    optional_array(output, value.later_selected_commit_nonce)?;
    output.byte(access_policy(value.main_access_policy))?;
    output.byte(access_policy(value.coordination_access_policy))?;
    cleanup(output, &value.cleanup)?;
    output.byte(coordination_cleanup(value.coordination_cleanup))?;
    output.byte(housekeeping(value.housekeeping))?;
    housekeeping_list(output, &value.visible_housekeeping)?;
    optional_problem(output, value.cause.as_ref())
}

pub(super) fn read_result(input: &mut Reader<'_>) -> Result<PublicationResult> {
    Ok(PublicationResult {
        attempt: read_attempt(input)?,
        main_namespace_may_have_been_attempted: input.bool()?,
        publication: read_publication_status(input.byte()?)?,
        destination_content: read_destination_content(input.byte()?)?,
        later_canonical: read_later_canonical(input.byte()?)?,
        live_lineage: read_optional_byte(input)?
            .map(read_live_lineage)
            .transpose()?,
        later_attempt_or_sidecar_id: read_optional_array(input)?,
        later_selected_transaction_id: read_optional_u64(input)?,
        later_selected_commit_nonce: read_optional_array(input)?,
        main_access_policy: read_access_policy(input.byte()?)?,
        coordination_access_policy: read_access_policy(input.byte()?)?,
        cleanup: read_cleanup(input)?,
        coordination_cleanup: read_coordination_cleanup(input.byte()?)?,
        housekeeping: read_housekeeping(input.byte()?)?,
        visible_housekeeping: read_housekeeping_list(input)?,
        cause: read_optional_problem(input)?,
    })
}

pub(super) fn cleanup(output: &mut Writer<'_>, value: &CleanupArtifacts) -> Result<()> {
    output.byte(value.len() as u8)?;
    for artifact in value.iter() {
        cleanup_artifact(output, artifact)?;
    }
    Ok(())
}

pub(super) fn read_cleanup(input: &mut Reader<'_>) -> Result<CleanupArtifacts> {
    let count = input.byte()? as usize;
    if count > 4 {
        return Err(Error::Corrupt("worker cleanup ledger is too large"));
    }
    let mut cleanup = CleanupArtifacts::new();
    for _ in 0..count {
        cleanup.push(read_cleanup_artifact(input)?);
    }
    Ok(cleanup)
}

pub(super) fn coordination(value: CoordinationCleanup) -> u8 {
    coordination_cleanup(value)
}

pub(super) fn read_coordination(value: u8) -> Result<CoordinationCleanup> {
    read_coordination_cleanup(value)
}

pub(super) fn housekeeping_value(value: Housekeeping) -> u8 {
    housekeeping(value)
}

pub(super) fn read_housekeeping_value(value: u8) -> Result<Housekeeping> {
    read_housekeeping(value)
}

pub(super) fn write_housekeeping_list(
    output: &mut Writer<'_>,
    value: &[HousekeepingArtifact],
) -> Result<()> {
    housekeeping_list(output, value)
}

pub(super) fn read_housekeeping_artifacts(
    input: &mut Reader<'_>,
) -> Result<Box<[HousekeepingArtifact]>> {
    read_housekeeping_list(input)
}

pub(super) fn problem(output: &mut Writer<'_>, value: &PublicationProblem) -> Result<()> {
    output.u32(value.code as u32)?;
    optional_i32(output, value.os_code)?;
    output.sized_bytes(value.detail.as_bytes())
}

pub(super) fn read_problem(input: &mut Reader<'_>) -> Result<PublicationProblem> {
    let code = crate::ErrorCode::from_wire(input.u32()?)
        .ok_or(Error::Corrupt("worker publication error code is invalid"))?;
    let os_code = read_optional_i32(input)?;
    let detail = input.boxed_bytes()?;
    let detail = String::from_utf8(detail.into_vec())
        .map_err(|_| Error::Corrupt("worker publication error detail is not UTF-8"))?;
    Ok(PublicationProblem::with_owned_detail(code, os_code, detail))
}

pub(super) fn optional_problem(
    output: &mut Writer<'_>,
    value: Option<&PublicationProblem>,
) -> Result<()> {
    output.bool(value.is_some())?;
    if let Some(value) = value {
        problem(output, value)?;
    }
    Ok(())
}

pub(super) fn read_optional_problem(input: &mut Reader<'_>) -> Result<Option<PublicationProblem>> {
    input.bool()?.then(|| read_problem(input)).transpose()
}

fn attempt(output: &mut Writer<'_>, value: &PublicationAttempt) -> Result<()> {
    output.bytes(&value.database_id)?;
    output.u64(value.transaction_id)?;
    output.bytes(&value.commit_nonce)?;
    output.bytes(&value.publication_attempt_id)?;
    wire::identity(output, value.directory_identity)?;
    output.u16(value.destination_basename_encoding)?;
    output.sized_bytes(&value.destination_basename)?;
    wire::identity(output, value.output_identity)?;
    output.u64(value.output_byte_length)?;
    output.bytes(&value.output_sha512)?;
    output.byte(publication_policy(value.publication_policy))?;
    output.bool(value.previous_destination.is_some())?;
    if let Some(previous) = &value.previous_destination {
        previous_destination(output, previous)?;
    }
    wire::identity(output, value.reservation_identity)?;
    creation_security(output, &value.creation_security)
}

fn read_attempt(input: &mut Reader<'_>) -> Result<PublicationAttempt> {
    Ok(PublicationAttempt {
        database_id: input.array()?,
        transaction_id: input.u64()?,
        commit_nonce: input.array()?,
        publication_attempt_id: input.array()?,
        directory_identity: wire::read_identity(input)?,
        destination_basename_encoding: input.u16()?,
        destination_basename: input.boxed_bytes()?,
        output_identity: wire::read_identity(input)?,
        output_byte_length: input.u64()?,
        output_sha512: input.array()?,
        publication_policy: read_publication_policy(input.byte()?)?,
        previous_destination: input
            .bool()?
            .then(|| read_previous_destination(input))
            .transpose()?,
        reservation_identity: wire::read_identity(input)?,
        creation_security: read_creation_security(input)?,
    })
}

fn previous_destination(output: &mut Writer<'_>, value: &PreviousDestination) -> Result<()> {
    wire::identity(output, value.identity)?;
    output.u64(value.byte_length)?;
    output.bytes(&value.sha512)
}

fn read_previous_destination(input: &mut Reader<'_>) -> Result<PreviousDestination> {
    Ok(PreviousDestination {
        identity: wire::read_identity(input)?,
        byte_length: input.u64()?,
        sha512: input.array()?,
    })
}

fn cleanup_artifact(output: &mut Writer<'_>, value: &CleanupArtifact) -> Result<()> {
    output.byte(artifact_kind(value.kind))?;
    output.byte(directory_role(value.directory_role))?;
    wire::identity(output, value.directory_identity)?;
    output.u16(value.basename_encoding)?;
    output.sized_bytes(&value.basename)?;
    optional_identity(output, value.identity)?;
    output.bool(value.creation_security.is_some())?;
    if let Some(security) = &value.creation_security {
        creation_security(output, security)?;
    }
    output.bool(value.unpublished_tail.is_some())?;
    if let Some(tail) = &value.unpublished_tail {
        unpublished_tail(output, tail)?;
    }
    problem(output, &value.error)
}

fn read_cleanup_artifact(input: &mut Reader<'_>) -> Result<CleanupArtifact> {
    Ok(CleanupArtifact {
        kind: read_artifact_kind(input.byte()?)?,
        directory_role: read_directory_role(input.byte()?)?,
        directory_identity: wire::read_identity(input)?,
        basename_encoding: input.u16()?,
        basename: input.boxed_bytes()?,
        identity: read_optional_identity(input)?,
        creation_security: input
            .bool()?
            .then(|| read_creation_security(input))
            .transpose()?,
        unpublished_tail: input
            .bool()?
            .then(|| read_unpublished_tail(input))
            .transpose()?,
        error: read_problem(input)?,
    })
}

fn housekeeping_list(output: &mut Writer<'_>, value: &[HousekeepingArtifact]) -> Result<()> {
    if value.len() > MAX_HOUSEKEEPING {
        return Err(Error::BudgetExceeded("worker housekeeping ledger"));
    }
    output.byte(value.len() as u8)?;
    for artifact in value {
        housekeeping_artifact(output, artifact)?;
    }
    Ok(())
}

fn read_housekeeping_list(input: &mut Reader<'_>) -> Result<Box<[HousekeepingArtifact]>> {
    let count = input.byte()? as usize;
    if count > MAX_HOUSEKEEPING {
        return Err(Error::Corrupt("worker housekeeping ledger is too large"));
    }
    let mut output = Vec::new();
    output
        .try_reserve_exact(count)
        .map_err(|_| Error::BudgetExceeded("worker housekeeping ledger"))?;
    for _ in 0..count {
        output.push(read_housekeeping_artifact(input)?);
    }
    Ok(output.into_boxed_slice())
}

fn housekeeping_artifact(output: &mut Writer<'_>, value: &HousekeepingArtifact) -> Result<()> {
    output.byte(housekeeping_state(value.state))?;
    output.byte(directory_role(value.directory_role))?;
    wire::identity(output, value.directory_identity)?;
    output.u16(value.basename_encoding)?;
    output.bytes(&value.attempt_id)?;
    output.u32(value.ordinal)?;
    output.sized_bytes(&value.envelope_basename)?;
    wire::identity(output, value.envelope_identity)?;
    output.sized_bytes(&value.source_basename)?;
    output.sized_bytes(&value.inert_basename)?;
    output.byte(artifact_presence(value.source_presence))?;
    optional_identity(output, value.source_identity)?;
    output.byte(artifact_presence(value.inert_presence))?;
    optional_identity(output, value.inert_identity)?;
    output.byte(artifact_kind(value.kind))?;
    creation_security(output, &value.creation_security)?;
    output.u64(value.selected_envelope_sequence)
}

fn read_housekeeping_artifact(input: &mut Reader<'_>) -> Result<HousekeepingArtifact> {
    Ok(HousekeepingArtifact {
        state: read_housekeeping_state(input.byte()?)?,
        directory_role: read_directory_role(input.byte()?)?,
        directory_identity: wire::read_identity(input)?,
        basename_encoding: input.u16()?,
        attempt_id: input.array()?,
        ordinal: input.u32()?,
        envelope_basename: input.boxed_bytes()?,
        envelope_identity: wire::read_identity(input)?,
        source_basename: input.boxed_bytes()?,
        inert_basename: input.boxed_bytes()?,
        source_presence: read_artifact_presence(input.byte()?)?,
        source_identity: read_optional_identity(input)?,
        inert_presence: read_artifact_presence(input.byte()?)?,
        inert_identity: read_optional_identity(input)?,
        kind: read_artifact_kind(input.byte()?)?,
        creation_security: read_creation_security(input)?,
        selected_envelope_sequence: input.u64()?,
    })
}

fn unpublished_tail(output: &mut Writer<'_>, value: &UnpublishedTailFacts) -> Result<()> {
    output.bytes(&value.expected_database_id)?;
    output.u64(value.committed_target_transaction_id)?;
    output.bytes(&value.committed_target_nonce)?;
    output.u64(value.committed_target_length)?;
    output.u64(value.observed_tail_end_exclusive)
}

fn read_unpublished_tail(input: &mut Reader<'_>) -> Result<UnpublishedTailFacts> {
    Ok(UnpublishedTailFacts {
        expected_database_id: input.array()?,
        committed_target_transaction_id: input.u64()?,
        committed_target_nonce: input.array()?,
        committed_target_length: input.u64()?,
        observed_tail_end_exclusive: input.u64()?,
    })
}

fn creation_security(output: &mut Writer<'_>, value: &CreationSecurity) -> Result<()> {
    output.u16(value.kind)?;
    output.bytes(&value.commitment)
}

fn read_creation_security(input: &mut Reader<'_>) -> Result<CreationSecurity> {
    Ok(CreationSecurity {
        kind: input.u16()?,
        commitment: input.array()?,
    })
}

fn optional_identity(
    output: &mut Writer<'_>,
    value: Option<crate::validation::LocalFileIdentity>,
) -> Result<()> {
    output.bool(value.is_some())?;
    if let Some(value) = value {
        wire::identity(output, value)?;
    }
    Ok(())
}

fn read_optional_identity(
    input: &mut Reader<'_>,
) -> Result<Option<crate::validation::LocalFileIdentity>> {
    input
        .bool()?
        .then(|| wire::read_identity(input))
        .transpose()
}

fn optional_byte(output: &mut Writer<'_>, value: Option<u8>) -> Result<()> {
    output.bool(value.is_some())?;
    if let Some(value) = value {
        output.byte(value)?;
    }
    Ok(())
}

fn read_optional_byte(input: &mut Reader<'_>) -> Result<Option<u8>> {
    input.bool()?.then(|| input.byte()).transpose()
}

fn optional_u64(output: &mut Writer<'_>, value: Option<u64>) -> Result<()> {
    output.bool(value.is_some())?;
    if let Some(value) = value {
        output.u64(value)?;
    }
    Ok(())
}

fn read_optional_u64(input: &mut Reader<'_>) -> Result<Option<u64>> {
    input.bool()?.then(|| input.u64()).transpose()
}

fn optional_i32(output: &mut Writer<'_>, value: Option<i32>) -> Result<()> {
    output.bool(value.is_some())?;
    if let Some(value) = value {
        output.i32(value)?;
    }
    Ok(())
}

fn read_optional_i32(input: &mut Reader<'_>) -> Result<Option<i32>> {
    input.bool()?.then(|| input.i32()).transpose()
}

fn optional_array<const N: usize>(output: &mut Writer<'_>, value: Option<[u8; N]>) -> Result<()> {
    output.bool(value.is_some())?;
    if let Some(value) = value {
        output.bytes(&value)?;
    }
    Ok(())
}

fn read_optional_array<const N: usize>(input: &mut Reader<'_>) -> Result<Option<[u8; N]>> {
    input.bool()?.then(|| input.array()).transpose()
}

macro_rules! enum_codec {
    ($write:ident, $read:ident, $ty:ty, {$($variant:path => $tag:literal),+ $(,)?}) => {
        fn $write(value: $ty) -> u8 {
            match value { $($variant => $tag),+ }
        }
        fn $read(value: u8) -> Result<$ty> {
            match value {
                $($tag => Ok($variant),)+
                _ => Err(Error::Corrupt("worker publication enum is invalid")),
            }
        }
    };
}

enum_codec!(publication_policy, read_publication_policy, PublicationPolicy, {
    PublicationPolicy::FailIfExists => 1,
    PublicationPolicy::ReplaceExisting => 2,
    PublicationPolicy::ReplaceExistingNoRollback => 3,
});
enum_codec!(publication_status, read_publication_status, PublicationStatus, {
    PublicationStatus::NotPublished => 1,
    PublicationStatus::Published => 2,
    PublicationStatus::OutcomeUnknown => 3,
});
enum_codec!(destination_content, read_destination_content, DestinationContent, {
    DestinationContent::Desired => 1,
    DestinationContent::Previous => 2,
    DestinationContent::Absent => 3,
    DestinationContent::Other => 4,
    DestinationContent::Unclassified => 5,
});
enum_codec!(later_canonical, read_later_canonical, LaterCanonical, {
    LaterCanonical::None => 1,
    LaterCanonical::ReservationOrTransition => 2,
    LaterCanonical::ReadyLiveSidecar => 3,
});
enum_codec!(live_lineage, read_live_lineage, LiveLineage, {
    LiveLineage::SameGenerationExactBytes => 1,
    LiveLineage::SameGenerationPhysicalBytesChanged => 2,
    LiveLineage::AdvancedGeneration => 3,
});
enum_codec!(access_policy, read_access_policy, AccessPolicy, {
    AccessPolicy::Absent => 1,
    AccessPolicy::CreatorOnly => 2,
    AccessPolicy::ChangedOrUnproven => 3,
    AccessPolicy::Unclassified => 4,
});
enum_codec!(coordination_cleanup, read_coordination_cleanup, CoordinationCleanup, {
    CoordinationCleanup::None => 1,
    CoordinationCleanup::CleanupGuard => 2,
    CoordinationCleanup::RetainedReaderCloseRequired => 3,
    CoordinationCleanup::RetainedWriterCloseRequired => 4,
});
enum_codec!(housekeeping, read_housekeeping, Housekeeping, {
    Housekeeping::None => 1,
    Housekeeping::CrashReappearancePossible => 2,
    Housekeeping::Visible => 3,
});
enum_codec!(artifact_kind, read_artifact_kind, ArtifactKind, {
    ArtifactKind::PrivateOutput => 1,
    ArtifactKind::PrivateReservation => 2,
    ArtifactKind::OwnedCoordination => 3,
    ArtifactKind::AuthorizedScratch => 4,
    ArtifactKind::OwnedMain => 5,
    ArtifactKind::UnpublishedMainTail => 6,
});
enum_codec!(directory_role, read_directory_role, DirectoryRole, {
    DirectoryRole::Destination => 1,
    DirectoryRole::ScratchDirectory => 2,
    DirectoryRole::MainFile => 3,
});
enum_codec!(housekeeping_state, read_housekeeping_state, HousekeepingState, {
    HousekeepingState::MovePending => 1,
    HousekeepingState::MoveAmbiguous => 2,
    HousekeepingState::Inert => 3,
    HousekeepingState::Conflict => 4,
});
enum_codec!(artifact_presence, read_artifact_presence, ArtifactPresence, {
    ArtifactPresence::Absent => 1,
    ArtifactPresence::Present => 2,
    ArtifactPresence::Unclassified => 3,
});
