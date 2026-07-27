//! Fixed-width lifecycle, validation, recovery, and maintenance layouts.

use std::ffi::c_void;

use crate::abi::{CallbackFailure, Cardinality129, Ip, LocalBasename, LocalIdentity, Path};

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct OptionalIdentity {
    pub present: u8,
    pub reserved: [u8; 7],
    pub value: LocalIdentity,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct OptionalBytes16 {
    pub present: u8,
    pub reserved: [u8; 7],
    pub value: [u8; 16],
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct OptionalU64 {
    pub present: u8,
    pub reserved: [u8; 7],
    pub value: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct OptionalU32 {
    pub present: u8,
    pub reserved: [u8; 3],
    pub value: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct CreationSecurity {
    pub present: u8,
    pub reserved0: [u8; 3],
    pub kind: u32,
    pub commitment: [u8; 32],
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct PublicationTuple {
    pub database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
}

#[repr(C)]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PublicationDigest {
    pub byte_length: u64,
    pub sha512: [u8; 64],
}

impl Default for PublicationDigest {
    fn default() -> Self {
        Self {
            byte_length: 0,
            sha512: [0; 64],
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct CreateReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub address_family: u32,
    pub value_kind: u32,
    pub value_tag: [u8; 16],
    pub database_id: [u8; 16],
    pub commit_nonce: [u8; 16],
    pub sidecar_id: [u8; 16],
    pub directory_identity: OptionalIdentity,
    pub main_basename: LocalBasename,
    pub main_identity: OptionalIdentity,
    pub sidecar_identity: OptionalIdentity,
    pub reader_capacity: u32,
    pub state: u32,
    pub residue_possible: u8,
    pub reserved: [u8; 3],
    pub housekeeping: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct LiveTransitionReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub operation: u32,
    pub reset_policy: u32,
    pub status: u32,
    pub new_sidecar_location: u32,
    pub database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
    pub directory_identity: LocalIdentity,
    pub main_identity: LocalIdentity,
    pub main_basename: LocalBasename,
    pub reader_capacity: u32,
    pub housekeeping: u32,
    pub sidecar_id: [u8; 16],
    pub previous_sidecar_identity: OptionalIdentity,
    pub new_sidecar_identity: OptionalIdentity,
    pub residue_possible: u8,
    pub reserved: [u8; 7],
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct LiveResidueReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub status: u32,
    pub kind: u32,
    pub database_id: OptionalBytes16,
    pub sidecar_id: OptionalBytes16,
    pub reader_capacity: OptionalU32,
    pub main_identity: OptionalIdentity,
    pub sidecar_identity: OptionalIdentity,
    pub residue_possible: u8,
    pub reserved: [u8; 3],
    pub housekeeping: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct CommitResolutionReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub attempted_database_id: [u8; 16],
    pub attempted_transaction_id: u64,
    pub attempted_commit_nonce: [u8; 16],
    pub actual_directory_identity: LocalIdentity,
    pub actual_main_identity: LocalIdentity,
    pub local_file_relation: u32,
    pub resolution: u32,
    pub cleanup_state: u32,
    pub coordination_cleanup: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct PublicationAttemptReport {
    pub tuple: PublicationTuple,
    pub publication_attempt_id: [u8; 16],
    pub directory_identity: LocalIdentity,
    pub destination_basename: LocalBasename,
    pub output_identity: LocalIdentity,
    pub output_digest: PublicationDigest,
    pub publication_policy: u32,
    pub previous_present: u8,
    pub reserved: [u8; 3],
    pub previous_identity: LocalIdentity,
    pub previous_digest: PublicationDigest,
    pub reservation_identity: LocalIdentity,
    pub creation_security: CreationSecurity,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct PublicationReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub attempt: PublicationAttemptReport,
    pub main_namespace_may_have_been_attempted: u8,
    pub live_lineage_present: u8,
    pub reserved0: [u8; 2],
    pub publication: u32,
    pub destination_content: u32,
    pub later_canonical: u32,
    pub live_lineage: u32,
    pub later_attempt_or_sidecar_id: OptionalBytes16,
    pub later_selected_transaction_id: OptionalU64,
    pub later_selected_commit_nonce: OptionalBytes16,
    pub main_access_policy: u32,
    pub coordination_access_policy: u32,
    pub cleanup_state: u32,
    pub coordination_cleanup: u32,
    pub housekeeping: u32,
    pub reserved1: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ValidationGeneration {
    pub present: u8,
    pub reserved0: [u8; 3],
    pub address_family: u32,
    pub value_kind: u32,
    pub reserved1: u32,
    pub value_tag: [u8; 16],
    pub database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
    pub page_count: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ValidationProgress {
    pub checked_unique_pages: u64,
    pub finding_count: u64,
    pub untraversable_subgraphs: u64,
    pub bounded_possible_span_addresses: Cardinality129,
    pub has_unbounded_unknown: u8,
    pub reserved: [u8; 7],
    pub reason_counts: [u64; 40],
    pub object_counts: [u64; 14],
}

impl Default for ValidationProgress {
    fn default() -> Self {
        Self {
            checked_unique_pages: 0,
            finding_count: 0,
            untraversable_subgraphs: 0,
            bounded_possible_span_addresses: Cardinality129::default(),
            has_unbounded_unknown: 0,
            reserved: [0; 7],
            reason_counts: [0; 40],
            object_counts: [0; 14],
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ValidationReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub valid: u8,
    pub reserved: [u8; 7],
    pub file_identity: LocalIdentity,
    pub generation: ValidationGeneration,
    pub progress: ValidationProgress,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ValidationFinding {
    pub abi_version: u32,
    pub struct_size: u32,
    pub sequence: u64,
    pub reason: u32,
    pub object: u32,
    pub page_number: OptionalU32,
    pub physical_bytes_present: u8,
    pub address_fence_present: u8,
    pub reserved0: [u8; 6],
    pub physical_start: u64,
    pub physical_end_exclusive: u64,
    pub related_page_number: OptionalU32,
    pub address_from: Ip,
    pub address_to: Ip,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct RecoveryCandidate {
    pub abi_version: u32,
    pub struct_size: u32,
    pub label: u32,
    pub reserved: u32,
    pub source_identity: LocalIdentity,
    pub database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct RecoveryCandidatesReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub source_identity: LocalIdentity,
    pub progress: ValidationProgress,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct LogicalCounts {
    pub examined: u64,
    pub accepted: u64,
    pub rejected: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct RecoveryFacts {
    pub pages: LogicalCounts,
    pub pages_io_unreadable: u64,
    pub ranges: LogicalCounts,
    pub catalog_entries: LogicalCounts,
    pub membership_entries: LogicalCounts,
    pub metadata_chunks: LogicalCounts,
    pub retirement_records: LogicalCounts,
    pub verified_addresses: Cardinality129,
    pub rejected_addresses: Cardinality129,
    pub bounded_possible_span_addresses: Cardinality129,
    pub has_unbounded_unknown: u8,
    pub reserved: [u8; 7],
    pub unknown_envelopes: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct RecoveryReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub facts: RecoveryFacts,
    pub scratch_present: u8,
    pub reserved: [u8; 7],
    pub scratch_attempt_id: [u8; 16],
    pub scratch_directory_identity: LocalIdentity,
    pub scratch_creation_security: CreationSecurity,
    pub publication: PublicationReport,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct RecoveryUnknown {
    pub abi_version: u32,
    pub struct_size: u32,
    pub sequence: u64,
    pub reason: u32,
    pub object: u32,
    pub page_number: OptionalU32,
    pub physical_bytes_present: u8,
    pub address_fence_present: u8,
    pub contributes_to_possible_span: u8,
    pub has_unbounded_extent: u8,
    pub reserved: [u8; 4],
    pub physical_start: u64,
    pub physical_end_exclusive: u64,
    pub address_from: Ip,
    pub address_to: Ip,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ResidueReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub operation: u32,
    pub classification: u32,
    pub directory_identity: LocalIdentity,
    pub coordination_identity: OptionalIdentity,
    pub main_identity: OptionalIdentity,
    pub main_content: u32,
    pub later_coordination: u32,
    pub access_policy: u32,
    pub cleanup_state: u32,
    pub coordination_cleanup: u32,
    pub housekeeping: u32,
    pub source_present: u8,
    pub publication_present: u8,
    pub reserved: [u8; 6],
    pub entry_count: u64,
    pub main_tuple_present: u8,
    pub reserved1: [u8; 7],
    pub main_tuple: PublicationTuple,
    pub main_digest: PublicationDigest,
    pub publication: PublicationReport,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct HousekeepingArtifact {
    pub abi_version: u32,
    pub struct_size: u32,
    pub state: u32,
    pub directory_role: u32,
    pub directory_identity: LocalIdentity,
    pub basename_encoding: u32,
    pub ordinal: u32,
    pub attempt_id: [u8; 16],
    pub envelope_basename: LocalBasename,
    pub envelope_identity: LocalIdentity,
    pub source_basename: LocalBasename,
    pub inert_basename: LocalBasename,
    pub source_presence: u32,
    pub inert_presence: u32,
    pub source_identity: OptionalIdentity,
    pub inert_identity: OptionalIdentity,
    pub kind: u32,
    pub creation_security: CreationSecurity,
    pub selected_envelope_sequence: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ArtifactRecord {
    pub abi_version: u32,
    pub struct_size: u32,
    pub record_kind: u32,
    pub authentication: u32,
    pub directory_identity: LocalIdentity,
    pub artifact_identity: OptionalIdentity,
    pub output_identity: OptionalIdentity,
    pub attempt_id: [u8; 16],
    pub ordinal: u32,
    pub policy: u32,
    pub phase: u32,
    pub reserved: u32,
    pub tuple_present: u8,
    pub digest_present: u8,
    pub previous_present: u8,
    pub reserved1: [u8; 5],
    pub tuple: PublicationTuple,
    pub digest: PublicationDigest,
    pub previous_identity: LocalIdentity,
    pub previous_digest: PublicationDigest,
    pub basename: LocalBasename,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct HousekeepingRecord {
    pub abi_version: u32,
    pub struct_size: u32,
    pub candidate_kind: u32,
    pub basename: LocalBasename,
    pub directory_identity: LocalIdentity,
    pub identity: OptionalIdentity,
    pub attempt_id: OptionalBytes16,
    pub ordinal: OptionalU32,
    pub artifact_present: u8,
    pub problem_present: u8,
    pub reserved: [u8; 6],
    pub artifact: HousekeepingArtifact,
    pub problem_code: u32,
    pub problem_os_code_present: u8,
    pub reserved1: [u8; 3],
    pub problem_os_code: i32,
    pub reserved2: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct HousekeepingPayload {
    pub tuple_present: u8,
    pub reserved: [u8; 7],
    pub tuple: PublicationTuple,
    pub digest: PublicationDigest,
}

#[repr(C)]
#[derive(Clone, Copy, Debug)]
pub struct ValidationBudget {
    pub abi_version: u32,
    pub struct_size: u32,
    pub max_heap_bytes: u64,
    pub max_open_files: u32,
    pub max_scratch_files: u32,
    pub max_scratch_bytes: u64,
    pub scratch_directory_present: u8,
    pub reserved: [u8; 7],
    pub scratch_directory: Path,
}

#[repr(C)]
#[derive(Clone, Copy, Debug)]
pub struct RecoveryBudget {
    pub abi_version: u32,
    pub struct_size: u32,
    pub max_heap_bytes: u64,
    pub max_output_pages: u64,
    pub max_open_files: u32,
    pub max_scratch_files: u32,
    pub max_scratch_bytes: u64,
    pub scratch_directory_present: u8,
    pub reserved: [u8; 7],
    pub scratch_directory: Path,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct SnapshotBudget {
    pub abi_version: u32,
    pub struct_size: u32,
    pub max_heap_bytes: u64,
    pub max_output_pages: u64,
    pub max_open_files: u32,
    pub reserved: u32,
}

pub type ValidationFindingSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const ValidationFinding,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;

pub type RecoveryUnknownSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const RecoveryUnknown,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;

pub type ArtifactSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const ArtifactRecord,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;

pub type HousekeepingSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const HousekeepingRecord,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;
