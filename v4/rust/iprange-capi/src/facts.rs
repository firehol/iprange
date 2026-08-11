//! Lossless conversion from Rust facts into fixed C records.

use std::mem::size_of;

use iprange_livedb::publication;
use iprange_livedb::recovery;
use iprange_livedb::validation;
use iprange_livedb::{AddressFamily, Cardinality129 as CoreCardinality, StructureKind, ValueKind};

use crate::abi::{Cardinality129, CleanupArtifact, LocalBasename, LocalIdentity};
use crate::abi_extra::{
    CreationSecurity, HousekeepingArtifact, LogicalCounts, OptionalBytes16, OptionalIdentity,
    OptionalU32, OptionalU64, PublicationAttemptReport, PublicationDigest, PublicationReport,
    PublicationTuple, RecoveryFacts, RecoveryUnknown, ValidationFinding, ValidationGeneration,
    ValidationProgress,
};
use crate::ip::{self, Key};
use crate::registry;

pub(crate) fn cardinality(value: CoreCardinality) -> Cardinality129 {
    Cardinality129 {
        bit128: value.bit128(),
        reserved: [0; 7],
        hi: value.hi(),
        lo: value.lo(),
    }
}

pub(crate) fn identity(value: validation::LocalFileIdentity) -> LocalIdentity {
    LocalIdentity {
        kind: u32::from(value.kind),
        reserved: 0,
        bytes: value.bytes,
    }
}

pub(crate) fn optional_identity(value: Option<validation::LocalFileIdentity>) -> OptionalIdentity {
    OptionalIdentity {
        present: u8::from(value.is_some()),
        reserved: [0; 7],
        value: value.map_or_else(LocalIdentity::default, identity),
    }
}

pub(crate) fn optional_bytes16(value: Option<[u8; 16]>) -> OptionalBytes16 {
    OptionalBytes16 {
        present: u8::from(value.is_some()),
        reserved: [0; 7],
        value: value.unwrap_or([0; 16]),
    }
}

pub(crate) fn optional_u64(value: Option<u64>) -> OptionalU64 {
    OptionalU64 {
        present: u8::from(value.is_some()),
        reserved: [0; 7],
        value: value.unwrap_or(0),
    }
}

pub(crate) fn optional_u32(value: Option<u32>) -> OptionalU32 {
    OptionalU32 {
        present: u8::from(value.is_some()),
        reserved: [0; 3],
        value: value.unwrap_or(0),
    }
}

pub(crate) fn basename(encoding: u16, bytes: &[u8]) -> LocalBasename {
    let mut output = LocalBasename {
        encoding: u32::from(encoding),
        length: u32::try_from(bytes.len()).unwrap_or(u32::MAX),
        ..LocalBasename::default()
    };
    let copied = bytes.len().min(output.bytes.len());
    output.bytes[..copied].copy_from_slice(&bytes[..copied]);
    output
}

pub(crate) fn creation_security(value: Option<&publication::CreationSecurity>) -> CreationSecurity {
    CreationSecurity {
        present: u8::from(value.is_some()),
        reserved0: [0; 3],
        kind: value.map_or(0, |value| u32::from(value.kind)),
        commitment: value.map_or([0; 32], |value| value.commitment),
    }
}

pub(crate) fn tuple(value: publication::PublicationTuple) -> PublicationTuple {
    PublicationTuple {
        database_id: value.database_id,
        transaction_id: value.transaction_id,
        commit_nonce: value.commit_nonce,
    }
}

pub(crate) fn digest(value: publication::PublicationDigest) -> PublicationDigest {
    PublicationDigest {
        byte_length: value.byte_length,
        sha512: value.sha512,
    }
}

pub(crate) fn cleanup(value: &publication::CleanupArtifact) -> CleanupArtifact {
    let tail = value.unpublished_tail.as_ref();
    CleanupArtifact {
        abi_version: 1,
        struct_size: size_of::<CleanupArtifact>() as u32,
        kind: artifact_kind(value.kind),
        directory_role: directory_role(value.directory_role),
        directory_identity: identity(value.directory_identity),
        basename: basename(value.basename_encoding, &value.basename),
        artifact_identity_present: u8::from(value.identity.is_some()),
        creation_security_present: u8::from(value.creation_security.is_some()),
        reserved0: [0; 6],
        artifact_identity: value.identity.map_or_else(LocalIdentity::default, identity),
        creation_security_kind: value
            .creation_security
            .as_ref()
            .map_or(0, |security| u32::from(security.kind)),
        reserved1: 0,
        creation_security_commitment: value
            .creation_security
            .as_ref()
            .map_or([0; 32], |security| security.commitment),
        unpublished_tail_present: u8::from(tail.is_some()),
        reserved2: [0; 7],
        expected_database_id: tail.map_or([0; 16], |tail| tail.expected_database_id),
        transaction_id: tail.map_or(0, |tail| tail.committed_target_transaction_id),
        commit_nonce: tail.map_or([0; 16], |tail| tail.committed_target_nonce),
        expected_length: tail.map_or(0, |tail| tail.committed_target_length),
        observed_end_exclusive: tail.map_or(0, |tail| tail.observed_tail_end_exclusive),
        error_code: value.error.code as u32,
        error_os_code_present: u8::from(value.error.os_code.is_some()),
        reserved3: [0; 3],
        error_os_code: value.error.os_code.unwrap_or(0),
        reserved4: 0,
    }
}

pub(crate) fn housekeeping_artifact(
    value: &publication::HousekeepingArtifact,
) -> HousekeepingArtifact {
    HousekeepingArtifact {
        abi_version: 1,
        struct_size: size_of::<HousekeepingArtifact>() as u32,
        state: housekeeping_state(value.state),
        directory_role: directory_role(value.directory_role),
        directory_identity: identity(value.directory_identity),
        basename_encoding: u32::from(value.basename_encoding),
        ordinal: value.ordinal,
        attempt_id: value.attempt_id,
        envelope_basename: basename(value.basename_encoding, &value.envelope_basename),
        envelope_identity: identity(value.envelope_identity),
        source_basename: basename(value.basename_encoding, &value.source_basename),
        inert_basename: basename(value.basename_encoding, &value.inert_basename),
        source_presence: artifact_presence(value.source_presence),
        inert_presence: artifact_presence(value.inert_presence),
        source_identity: optional_identity(value.source_identity),
        inert_identity: optional_identity(value.inert_identity),
        kind: artifact_kind(value.kind),
        creation_security: creation_security(Some(&value.creation_security)),
        selected_envelope_sequence: value.selected_envelope_sequence,
    }
}

pub(crate) fn publication(value: &publication::PublicationResult) -> PublicationReport {
    let previous = value.attempt.previous_destination.as_ref();
    PublicationReport {
        abi_version: 1,
        struct_size: size_of::<PublicationReport>() as u32,
        attempt: PublicationAttemptReport {
            tuple: PublicationTuple {
                database_id: value.attempt.database_id,
                transaction_id: value.attempt.transaction_id,
                commit_nonce: value.attempt.commit_nonce,
            },
            publication_attempt_id: value.attempt.publication_attempt_id,
            directory_identity: identity(value.attempt.directory_identity),
            destination_basename: basename(
                value.attempt.destination_basename_encoding,
                &value.attempt.destination_basename,
            ),
            output_identity: identity(value.attempt.output_identity),
            output_digest: PublicationDigest {
                byte_length: value.attempt.output_byte_length,
                sha512: value.attempt.output_sha512,
            },
            publication_policy: publication_policy(value.attempt.publication_policy),
            previous_present: u8::from(previous.is_some()),
            reserved: [0; 3],
            previous_identity: previous
                .map_or_else(LocalIdentity::default, |value| identity(value.identity)),
            previous_digest: previous.map_or_else(PublicationDigest::default, |value| {
                PublicationDigest {
                    byte_length: value.byte_length,
                    sha512: value.sha512,
                }
            }),
            reservation_identity: identity(value.attempt.reservation_identity),
            creation_security: creation_security(Some(&value.attempt.creation_security)),
        },
        main_namespace_may_have_been_attempted: u8::from(
            value.main_namespace_may_have_been_attempted,
        ),
        live_lineage_present: u8::from(value.live_lineage.is_some()),
        reserved0: [0; 2],
        publication: publication_status(value.publication),
        destination_content: destination_content(value.destination_content),
        later_canonical: later_canonical(value.later_canonical),
        live_lineage: value.live_lineage.map_or(0, live_lineage),
        later_attempt_or_sidecar_id: optional_bytes16(value.later_attempt_or_sidecar_id),
        later_selected_transaction_id: optional_u64(value.later_selected_transaction_id),
        later_selected_commit_nonce: optional_bytes16(value.later_selected_commit_nonce),
        main_access_policy: access_policy(value.main_access_policy),
        coordination_access_policy: access_policy(value.coordination_access_policy),
        cleanup_state: cleanup_state(value.cleanup_state()),
        coordination_cleanup: coordination_cleanup(value.coordination_cleanup),
        housekeeping: housekeeping(value.housekeeping),
        reserved1: 0,
    }
}

pub(crate) fn validation_progress(value: &validation::ValidationProgress) -> ValidationProgress {
    let mut reason_counts = [0; 47];
    for (index, reason) in VALIDATION_REASONS.iter().copied().enumerate() {
        reason_counts[index] = value.findings_for(reason);
    }
    let mut object_counts = [0; 17];
    for (index, object) in VALIDATION_OBJECTS.iter().copied().enumerate() {
        object_counts[index] = value.examined_for(object);
    }
    ValidationProgress {
        checked_unique_pages: value.checked_unique_pages,
        finding_count: value.finding_count,
        untraversable_subgraphs: value.untraversable_subgraphs,
        bounded_possible_span_addresses: cardinality(value.bounded_possible_span_addresses),
        has_unbounded_unknown: u8::from(value.has_unbounded_unknown),
        reserved: [0; 7],
        reason_counts,
        object_counts,
    }
}

pub(crate) fn validation_generation(
    value: Option<validation::ValidatedGeneration>,
) -> ValidationGeneration {
    ValidationGeneration {
        present: u8::from(value.is_some()),
        reserved0: [0; 3],
        address_family: value.map_or(0, |value| address_family(value.address_family)),
        value_kind: value.map_or(0, |value| value_kind(value.value_kind)),
        structure_kind: value.map_or(0, |value| structure_kind(value.structure_kind)),
        value_tag: value.map_or([0; 16], |value| *value.value_tag.as_wire()),
        database_id: value.map_or([0; 16], |value| value.database_id),
        transaction_id: value.map_or(0, |value| value.transaction_id),
        commit_nonce: value.map_or([0; 16], |value| value.commit_nonce),
        page_count: value.map_or(0, |value| value.page_count),
    }
}

pub(crate) fn validation_finding(value: &validation::ValidationFinding) -> ValidationFinding {
    let (address_fence_present, address_from, address_to) = fence(value.address_fence);
    ValidationFinding {
        abi_version: 1,
        struct_size: size_of::<ValidationFinding>() as u32,
        sequence: value.sequence,
        reason: reason(value.reason),
        object: object(value.object),
        page_number: optional_u32(value.page_number),
        physical_bytes_present: u8::from(value.physical_bytes.is_some()),
        address_fence_present,
        reserved0: [0; 6],
        physical_start: value.physical_bytes.map_or(0, |value| value.start),
        physical_end_exclusive: value.physical_bytes.map_or(0, |value| value.end_exclusive),
        related_page_number: optional_u32(value.related_page_number),
        address_from,
        address_to,
    }
}

pub(crate) fn recovery_facts(value: &recovery::RecoveryReport) -> RecoveryFacts {
    RecoveryFacts {
        pages: LogicalCounts {
            examined: value.pages.examined,
            accepted: value.pages.accepted,
            rejected: value.pages.rejected,
        },
        pages_io_unreadable: value.pages.io_unreadable,
        ranges: logical_counts(value.ranges),
        catalog_entries: logical_counts(value.catalog_entries),
        membership_entries: logical_counts(value.membership_entries),
        structure_entries: logical_counts(value.structure_entries),
        metadata_chunks: logical_counts(value.metadata_chunks),
        retirement_records: logical_counts(value.retirement_records),
        verified_addresses: cardinality(value.verified_addresses),
        rejected_addresses: cardinality(value.rejected_addresses),
        bounded_possible_span_addresses: cardinality(value.bounded_possible_span_addresses),
        has_unbounded_unknown: u8::from(value.has_unbounded_unknown),
        reserved: [0; 7],
        unknown_envelopes: value.unknown_envelopes,
    }
}

pub(crate) fn recovery_unknown(value: &recovery::RecoveryUnknownEnvelope) -> RecoveryUnknown {
    let (address_fence_present, address_from, address_to) = fence(value.address_fence);
    RecoveryUnknown {
        abi_version: 1,
        struct_size: size_of::<RecoveryUnknown>() as u32,
        sequence: value.sequence,
        reason: reason(value.reason),
        object: object(value.object),
        page_number: optional_u32(value.page_number),
        physical_bytes_present: u8::from(value.physical_bytes.is_some()),
        address_fence_present,
        contributes_to_possible_span: u8::from(value.contributes_to_possible_span),
        has_unbounded_extent: u8::from(value.has_unbounded_extent),
        reserved: [0; 4],
        physical_start: value.physical_bytes.map_or(0, |value| value.start),
        physical_end_exclusive: value.physical_bytes.map_or(0, |value| value.end_exclusive),
        address_from,
        address_to,
    }
}

fn fence(
    value: Option<validation::ValidationAddressFence>,
) -> (u8, crate::abi::Ip, crate::abi::Ip) {
    match value {
        Some(validation::ValidationAddressFence::Ipv4 { from, to }) => {
            (1, ip::encode(Key::V4(from)), ip::encode(Key::V4(to)))
        }
        Some(validation::ValidationAddressFence::Ipv6 { from, to }) => {
            (1, ip::encode(Key::V6(from)), ip::encode(Key::V6(to)))
        }
        None => (0, crate::abi::Ip::default(), crate::abi::Ip::default()),
    }
}

fn logical_counts(value: recovery::RecoveryLogicalCounts) -> LogicalCounts {
    LogicalCounts {
        examined: value.examined,
        accepted: value.accepted,
        rejected: value.rejected,
    }
}

pub(crate) const fn address_family(value: AddressFamily) -> u32 {
    value as u8 as u32
}

pub(crate) const fn value_kind(value: ValueKind) -> u32 {
    value as u8 as u32
}

pub(crate) const fn structure_kind(value: StructureKind) -> u32 {
    value as u8 as u32
}

pub(crate) const fn cleanup_state(value: publication::CleanupState) -> u32 {
    match value {
        publication::CleanupState::Clean => registry::CLEANUP_STATE_CLEAN,
        publication::CleanupState::ResiduePossible => registry::CLEANUP_STATE_RESIDUE_POSSIBLE,
    }
}

pub(crate) const fn coordination_cleanup(value: publication::CoordinationCleanup) -> u32 {
    match value {
        publication::CoordinationCleanup::None => registry::COORDINATION_CLEANUP_NONE,
        publication::CoordinationCleanup::CleanupGuard => registry::COORDINATION_CLEANUP_GUARD,
        publication::CoordinationCleanup::RetainedReaderCloseRequired => {
            registry::COORDINATION_CLEANUP_RETAINED_READER_CLOSE_REQUIRED
        }
        publication::CoordinationCleanup::RetainedWriterCloseRequired => {
            registry::COORDINATION_CLEANUP_RETAINED_WRITER_CLOSE_REQUIRED
        }
    }
}

pub(crate) const fn housekeeping(value: publication::Housekeeping) -> u32 {
    match value {
        publication::Housekeeping::None => registry::HOUSEKEEPING_NONE,
        publication::Housekeeping::CrashReappearancePossible => {
            registry::HOUSEKEEPING_CRASH_REAPPEARANCE_POSSIBLE
        }
        publication::Housekeeping::Visible => registry::HOUSEKEEPING_VISIBLE,
    }
}

pub(crate) const fn access_policy(value: publication::AccessPolicy) -> u32 {
    match value {
        publication::AccessPolicy::Absent => registry::ACCESS_ABSENT,
        publication::AccessPolicy::CreatorOnly => registry::ACCESS_CREATOR_ONLY,
        publication::AccessPolicy::ChangedOrUnproven => registry::ACCESS_CHANGED_OR_UNPROVEN,
        publication::AccessPolicy::Unclassified => registry::ACCESS_UNCLASSIFIED,
    }
}

pub(crate) const fn publication_policy(value: publication::PublicationPolicy) -> u32 {
    match value {
        publication::PublicationPolicy::FailIfExists => registry::DESTINATION_POLICY_FAIL_IF_EXISTS,
        publication::PublicationPolicy::ReplaceExisting => {
            registry::DESTINATION_POLICY_REPLACE_EXISTING
        }
        publication::PublicationPolicy::ReplaceExistingNoRollback => {
            registry::DESTINATION_POLICY_REPLACE_EXISTING_NO_ROLLBACK
        }
    }
}

fn publication_status(value: publication::PublicationStatus) -> u32 {
    match value {
        publication::PublicationStatus::NotPublished => registry::PUBLICATION_NOT_PUBLISHED,
        publication::PublicationStatus::Published => registry::PUBLICATION_PUBLISHED,
        publication::PublicationStatus::OutcomeUnknown => registry::PUBLICATION_OUTCOME_UNKNOWN,
    }
}

fn destination_content(value: publication::DestinationContent) -> u32 {
    match value {
        publication::DestinationContent::Desired => registry::DESTINATION_CONTENT_DESIRED,
        publication::DestinationContent::Previous => registry::DESTINATION_CONTENT_PREVIOUS,
        publication::DestinationContent::Absent => registry::DESTINATION_CONTENT_ABSENT,
        publication::DestinationContent::Other => registry::DESTINATION_CONTENT_OTHER,
        publication::DestinationContent::Unclassified => registry::DESTINATION_CONTENT_UNCLASSIFIED,
    }
}

fn later_canonical(value: publication::LaterCanonical) -> u32 {
    match value {
        publication::LaterCanonical::None => registry::LATER_CANONICAL_OWNER_NONE,
        publication::LaterCanonical::ReservationOrTransition => {
            registry::LATER_CANONICAL_OWNER_RESERVATION_OR_TRANSITION
        }
        publication::LaterCanonical::ReadyLiveSidecar => {
            registry::LATER_CANONICAL_OWNER_READY_LIVE_SIDECAR
        }
    }
}

fn live_lineage(value: publication::LiveLineage) -> u32 {
    match value {
        publication::LiveLineage::SameGenerationExactBytes => {
            registry::LIVE_LINEAGE_SAME_GENERATION_EXACT_BYTES
        }
        publication::LiveLineage::SameGenerationPhysicalBytesChanged => {
            registry::LIVE_LINEAGE_SAME_GENERATION_PHYSICAL_BYTES_CHANGED
        }
        publication::LiveLineage::AdvancedGeneration => registry::LIVE_LINEAGE_ADVANCED_GENERATION,
    }
}

fn artifact_kind(value: publication::ArtifactKind) -> u32 {
    match value {
        publication::ArtifactKind::PrivateOutput => registry::ARTIFACT_KIND_PRIVATE_OUTPUT,
        publication::ArtifactKind::PrivateReservation => {
            registry::ARTIFACT_KIND_PRIVATE_RESERVATION
        }
        publication::ArtifactKind::OwnedCoordination => registry::ARTIFACT_KIND_OWNED_COORDINATION,
        publication::ArtifactKind::AuthorizedScratch => registry::ARTIFACT_KIND_AUTHORIZED_SCRATCH,
        publication::ArtifactKind::OwnedMain => registry::ARTIFACT_KIND_OWNED_MAIN,
        publication::ArtifactKind::UnpublishedMainTail => {
            registry::ARTIFACT_KIND_UNPUBLISHED_MAIN_TAIL
        }
    }
}

fn directory_role(value: publication::DirectoryRole) -> u32 {
    match value {
        publication::DirectoryRole::Destination => registry::DIRECTORY_ROLE_DESTINATION,
        publication::DirectoryRole::ScratchDirectory => registry::DIRECTORY_ROLE_SCRATCH_DIRECTORY,
        publication::DirectoryRole::MainFile => registry::DIRECTORY_ROLE_MAIN_FILE,
    }
}

fn housekeeping_state(value: publication::HousekeepingState) -> u32 {
    match value {
        publication::HousekeepingState::MovePending => {
            registry::WINDOWS_HOUSEKEEPING_TRANSITION_MOVE_PENDING
        }
        publication::HousekeepingState::MoveAmbiguous => {
            registry::WINDOWS_HOUSEKEEPING_TRANSITION_MOVE_AMBIGUOUS
        }
        publication::HousekeepingState::Inert => registry::WINDOWS_HOUSEKEEPING_TRANSITION_INERT,
        publication::HousekeepingState::Conflict => {
            registry::WINDOWS_HOUSEKEEPING_TRANSITION_CONFLICT
        }
    }
}

fn artifact_presence(value: publication::ArtifactPresence) -> u32 {
    match value {
        publication::ArtifactPresence::Absent => registry::ARTIFACT_PRESENCE_ABSENT,
        publication::ArtifactPresence::Present => registry::ARTIFACT_PRESENCE_PRESENT,
        publication::ArtifactPresence::Unclassified => registry::ARTIFACT_PRESENCE_UNCLASSIFIED,
    }
}

fn reason(value: validation::ValidationReason) -> u32 {
    value as u8 as u32 + 1
}

fn object(value: validation::ValidationObject) -> u32 {
    value as u8 as u32 + 1
}

const VALIDATION_REASONS: [validation::ValidationReason; 47] = [
    validation::ValidationReason::MetaUnavailable,
    validation::ValidationReason::MetaInvalid,
    validation::ValidationReason::MetaStaticMismatch,
    validation::ValidationReason::FileGeometryInvalid,
    validation::ValidationReason::RootCountInvalid,
    validation::ValidationReason::IoError,
    validation::ValidationReason::ArithmeticOverflow,
    validation::ValidationReason::PageOutOfBounds,
    validation::ValidationReason::PageHeaderInvalid,
    validation::ValidationReason::PageCrcMismatch,
    validation::ValidationReason::PageTypeMismatch,
    validation::ValidationReason::PageBornTxnInvalid,
    validation::ValidationReason::PageReservedNonzero,
    validation::ValidationReason::TreeCycle,
    validation::ValidationReason::PageAlias,
    validation::ValidationReason::TreeLevelInvalid,
    validation::ValidationReason::TreeOrderInvalid,
    validation::ValidationReason::TreeFenceInvalid,
    validation::ValidationReason::RangeReversed,
    validation::ValidationReason::RangeOverlap,
    validation::ValidationReason::RangeNotCoalesced,
    validation::ValidationReason::CatalogNameInvalid,
    validation::ValidationReason::CatalogBijectionInvalid,
    validation::ValidationReason::CatalogBitmapInvalid,
    validation::ValidationReason::MembershipBitmapInvalid,
    validation::ValidationReason::MembershipHashInvalid,
    validation::ValidationReason::MembershipReverseIndexInvalid,
    validation::ValidationReason::MembershipRefcountInvalid,
    validation::ValidationReason::MembershipActiveFeedInvalid,
    validation::ValidationReason::BlobInvalid,
    validation::ValidationReason::MetadataZlibInvalid,
    validation::ValidationReason::MetadataLengthInvalid,
    validation::ValidationReason::BitmapSummaryInvalid,
    validation::ValidationReason::AllocationPartitionInvalid,
    validation::ValidationReason::RetirementOrderInvalid,
    validation::ValidationReason::RetirementListInvalid,
    validation::ValidationReason::CatalogInvalid,
    validation::ValidationReason::MembershipMissing,
    validation::ValidationReason::MembershipInvalid,
    validation::ValidationReason::MetadataInvalid,
    validation::ValidationReason::StructurePayloadInvalid,
    validation::ValidationReason::StructureHashInvalid,
    validation::ValidationReason::StructureReverseIndexInvalid,
    validation::ValidationReason::StructureRefcountInvalid,
    validation::ValidationReason::StructureMembershipInvalid,
    validation::ValidationReason::StructureMissing,
    validation::ValidationReason::StructureInvalid,
];

const VALIDATION_OBJECTS: [validation::ValidationObject; 17] = [
    validation::ValidationObject::FileGeometry,
    validation::ValidationObject::Meta,
    validation::ValidationObject::RangeTree,
    validation::ValidationObject::CatalogNameTree,
    validation::ValidationObject::CatalogIndexTree,
    validation::ValidationObject::MembershipDictionary,
    validation::ValidationObject::MembershipReverseIndex,
    validation::ValidationObject::MembershipBlob,
    validation::ValidationObject::Metadata,
    validation::ValidationObject::FreeBitmap,
    validation::ValidationObject::FeedUsedBitmap,
    validation::ValidationObject::MembershipUsedBitmap,
    validation::ValidationObject::RetirementTree,
    validation::ValidationObject::RetirementBlob,
    validation::ValidationObject::StructureDictionary,
    validation::ValidationObject::StructureReverseIndex,
    validation::ValidationObject::StructureUsedBitmap,
];
