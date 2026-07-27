//! Fixed-width C layouts and numeric values.

use std::ffi::c_void;

use crate::handle::BorrowedMembershipViewHandle;
pub use crate::registry::{
    ABI_VERSION, PATH_POSIX_BYTES, PATH_WINDOWS_UTF16, STATUS_ERROR, STATUS_OK,
};

pub type CancelFn = Option<unsafe extern "C" fn(context: *mut c_void) -> u8>;
pub type CoverageSourceFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *mut Range,
        capacity: u64,
        count: *mut u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;
pub type DirectSourceFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *mut DirectRange,
        capacity: u64,
        count: *mut u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;
pub type CoverageSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const Range,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;
pub type DirectSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const DirectRange,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;
pub type MembershipSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const MembershipRange,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;
pub type FeedSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const FeedInfo,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Cardinality129 {
    pub bit128: u8,
    pub reserved: [u8; 7],
    pub hi: u64,
    pub lo: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug)]
pub struct ByteSlice {
    pub pointer: *const u8,
    pub length: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug)]
pub struct MutableByteSlice {
    pub pointer: *mut u8,
    pub length: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug)]
pub struct Path {
    pub kind: u32,
    pub reserved: u32,
    pub pointer: *const c_void,
    pub length: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Ip {
    pub family: u32,
    pub bytes: [u8; 16],
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Range {
    pub from: Ip,
    pub to: Ip,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct DirectRange {
    pub range: Range,
    pub value: u32,
    pub reserved: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug)]
pub struct MembershipRange {
    pub range: Range,
    pub membership: *const BorrowedMembershipViewHandle,
}

#[repr(C)]
#[derive(Clone, Copy, Debug)]
pub struct Cancellation {
    pub callback: CancelFn,
    pub context: *mut c_void,
}

#[repr(C)]
#[derive(Clone, Copy, Debug)]
pub struct CallbackFailure {
    pub abi_version: u32,
    pub struct_size: u32,
    pub caller_code: u64,
    pub message_pointer: *const u8,
    pub message_length: u64,
}

impl Default for CallbackFailure {
    fn default() -> Self {
        Self {
            abi_version: ABI_VERSION,
            struct_size: std::mem::size_of::<Self>() as u32,
            caller_code: 0,
            message_pointer: std::ptr::null(),
            message_length: 0,
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct TransactionBudget {
    pub abi_version: u32,
    pub struct_size: u32,
    pub max_heap_bytes: u64,
    pub max_private_pages: u64,
    pub max_file_growth_pages: u64,
    pub max_open_files: u32,
    pub reserved: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct DatabaseInfo {
    pub abi_version: u32,
    pub struct_size: u32,
    pub address_family: u32,
    pub value_kind: u32,
    pub value_tag: [u8; 16],
    pub database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
    pub page_count: u64,
    pub range_record_count: u64,
    pub active_feed_count: u64,
    pub meta_selection: u32,
    pub reserved: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FeedInfo {
    pub index: u32,
    pub name_length: u32,
    pub name: [u8; 255],
    pub reserved: u8,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ScanReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub record_count: u64,
    pub completed: u8,
    pub reserved: [u8; 7],
}

impl Default for FeedInfo {
    fn default() -> Self {
        Self {
            index: 0,
            name_length: 0,
            name: [0; 255],
            reserved: 0,
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct FinishInputReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub workflow: u32,
    pub logical_change: u32,
    pub input_record_count: u64,
    pub input_normalized_interval_count: u64,
    pub before_range_record_count: u64,
    pub after_range_record_count: u64,
    pub input_addresses: Cardinality129,
    pub before_addresses: Cardinality129,
    pub after_addresses: Cardinality129,
    pub unchanged_value_addresses: Cardinality129,
    pub changed_value_addresses: Cardinality129,
    pub added_addresses: Cardinality129,
    pub removed_addresses: Cardinality129,
    pub source_feed_count: u64,
    pub matched_feed_count: u64,
    pub created_feed_count: u64,
    pub source_distinct_membership_count: u64,
    pub translated_membership_count: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct CommitReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub attempted_database_id: [u8; 16],
    pub directory_identity: LocalIdentity,
    pub main_identity: LocalIdentity,
    pub attempted_transaction_id: u64,
    pub attempted_commit_nonce: [u8; 16],
    pub durability: u32,
    pub cleanup_state: u32,
    pub coordination_cleanup: u32,
    pub reserved: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct AbortReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub outcome: u32,
    pub cleanup_state: u32,
    pub coordination_cleanup: u32,
    pub reserved: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct CloseReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub outcome: u32,
    pub abort_present: u8,
    pub reserved0: [u8; 3],
    pub abort_outcome: u32,
    pub cleanup_state: u32,
    pub coordination_cleanup: u32,
    pub reserved1: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ReclaimReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub changed: u8,
    pub reserved0: [u8; 7],
    pub transaction_count: u64,
    pub page_count: u64,
    pub commit: CommitReport,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct LocalIdentity {
    pub kind: u32,
    pub reserved: u32,
    pub bytes: [u8; 32],
}

#[repr(C)]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct LocalBasename {
    pub encoding: u32,
    pub length: u32,
    pub bytes: [u8; 512],
}

impl Default for LocalBasename {
    fn default() -> Self {
        Self {
            encoding: 0,
            length: 0,
            bytes: [0; 512],
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct CleanupArtifact {
    pub abi_version: u32,
    pub struct_size: u32,
    pub kind: u32,
    pub directory_role: u32,
    pub directory_identity: LocalIdentity,
    pub basename: LocalBasename,
    pub artifact_identity_present: u8,
    pub creation_security_present: u8,
    pub reserved0: [u8; 6],
    pub artifact_identity: LocalIdentity,
    pub creation_security_kind: u32,
    pub reserved1: u32,
    pub creation_security_commitment: [u8; 32],
    pub unpublished_tail_present: u8,
    pub reserved2: [u8; 7],
    pub expected_database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
    pub expected_length: u64,
    pub observed_end_exclusive: u64,
    pub error_code: u32,
    pub error_os_code_present: u8,
    pub reserved3: [u8; 3],
    pub error_os_code: i32,
    pub reserved4: u32,
}

impl Default for Cancellation {
    fn default() -> Self {
        Self {
            callback: None,
            context: std::ptr::null_mut(),
        }
    }
}
