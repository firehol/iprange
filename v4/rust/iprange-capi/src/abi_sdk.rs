//! Fixed-width query, join, construction, history, and algebra layouts.

use std::ffi::c_void;

use crate::abi::{ByteSlice, CallbackFailure, Cardinality129};

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct MembershipQueryBudget {
    pub abi_version: u32,
    pub struct_size: u32,
    pub max_heap_bytes: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct DirectJoinBudget {
    pub abi_version: u32,
    pub struct_size: u32,
    pub max_result_cells: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FeedNameValue {
    pub length: u32,
    pub bytes: [u8; 255],
    pub reserved: u8,
}

impl Default for FeedNameValue {
    fn default() -> Self {
        Self {
            length: 0,
            bytes: [0; 255],
            reserved: 0,
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct FeedPairInput {
    pub left: ByteSlice,
    pub right: ByteSlice,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct MatchingFeedsReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub matching_feed_count: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct FeedCardinality {
    pub feed: FeedNameValue,
    pub addresses: Cardinality129,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct FeedOverlap {
    pub left: FeedNameValue,
    pub right: FeedNameValue,
    pub addresses: Cardinality129,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct MembershipAggregationReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub scanned_range_count: u64,
    pub scanned_addresses: Cardinality129,
    pub feed_result_count: u64,
    pub pair_result_count: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct DirectJoinCell {
    pub feed: FeedNameValue,
    pub direct_present: u8,
    pub reserved: [u8; 3],
    pub direct_value: u32,
    pub addresses: Cardinality129,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct DirectJoinReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub membership_range_count: u64,
    pub direct_ranges_visited: u64,
    pub joined_segment_count: u64,
    pub selected_addresses: Cardinality129,
    pub mapped_addresses: Cardinality129,
    pub unmapped_addresses: Cardinality129,
    pub result_cell_count: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct MembershipCrossCell {
    pub left: FeedNameValue,
    pub right: FeedNameValue,
    pub addresses: Cardinality129,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct UncoveredFeed {
    pub side: u32,
    pub reserved: u32,
    pub feed: FeedNameValue,
    pub addresses: Cardinality129,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct MembershipJoinReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub left_range_count: u64,
    pub right_range_count: u64,
    pub joined_segment_count: u64,
    pub left_addresses: Cardinality129,
    pub right_addresses: Cardinality129,
    pub overlap_addresses: Cardinality129,
    pub left_uncovered_addresses: Cardinality129,
    pub right_uncovered_addresses: Cardinality129,
    pub cross_result_count: u64,
    pub uncovered_result_count: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct HistoryWindowInput {
    pub feed_name: ByteSlice,
    pub cutoff: u32,
    pub reserved: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct HistoryWindowReport {
    pub feed_name: FeedNameValue,
    pub cutoff: u32,
    pub created: u8,
    pub reserved: [u8; 3],
    pub before_interval_count: u64,
    pub after_interval_count: u64,
    pub before_addresses: Cardinality129,
    pub after_addresses: Cardinality129,
    pub unchanged_addresses: Cardinality129,
    pub added_addresses: Cardinality129,
    pub removed_addresses: Cardinality129,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct HistoryProjectionReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub logical_change: u32,
    pub reserved: u32,
    pub source_range_count: u64,
    pub source_addresses: Cardinality129,
    pub created_feed_count: u64,
    pub before_interval_count: u64,
    pub after_interval_count: u64,
    pub before_addresses: Cardinality129,
    pub after_addresses: Cardinality129,
    pub unchanged_addresses: Cardinality129,
    pub added_addresses: Cardinality129,
    pub removed_addresses: Cardinality129,
    pub window_count: u64,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct OptionalByteSlice {
    pub present: u8,
    pub reserved: [u8; 7],
    pub value: ByteSlice,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ImmutableFeedBudget {
    pub abi_version: u32,
    pub struct_size: u32,
    pub max_heap_bytes: u64,
    pub max_output_pages: u64,
    pub max_workspace_pages: u64,
    pub max_open_files: u32,
    pub reserved: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ImmutableFeedReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub input_record_count: u64,
    pub normalized_interval_count: u64,
    pub addresses: Cardinality129,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct MembershipAlgebraBudget {
    pub abi_version: u32,
    pub struct_size: u32,
    pub max_heap_bytes: u64,
    pub max_sources: u32,
    pub reserved: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct AlgebraOutputBudget {
    pub abi_version: u32,
    pub struct_size: u32,
    pub max_output_pages: u64,
    pub max_open_files: u32,
    pub reserved: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FeedSelectionInput {
    pub kind: u32,
    pub reserved: u32,
    pub names: *const ByteSlice,
    pub name_count: u64,
}

impl Default for FeedSelectionInput {
    fn default() -> Self {
        Self {
            kind: 0,
            reserved: 0,
            names: std::ptr::null(),
            name_count: 0,
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct AlgebraSetOperationInput {
    pub kind: u32,
    pub reserved: u32,
    pub included: FeedSelectionInput,
    pub excluded: FeedSelectionInput,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct AlgebraOutputModeInput {
    pub kind: u32,
    pub reserved: u32,
    pub flat_name: ByteSlice,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct AlgebraCountReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub source_count: u64,
    pub source_range_count: u64,
    pub joined_segment_count: u64,
    pub addresses: Cardinality129,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct AlgebraComparisonReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub source_count: u64,
    pub source_range_count: u64,
    pub joined_segment_count: u64,
    pub left_addresses: Cardinality129,
    pub right_addresses: Cardinality129,
    pub overlap_addresses: Cardinality129,
    pub left_only_addresses: Cardinality129,
    pub right_only_addresses: Cardinality129,
    pub union_addresses: Cardinality129,
    pub equal: u32,
    pub reserved: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct AlgebraSetReport {
    pub abi_version: u32,
    pub struct_size: u32,
    pub source_count: u64,
    pub source_range_count: u64,
    pub joined_segment_count: u64,
    pub output_feed_count: u64,
    pub output_range_count: u64,
    pub output_addresses: Cardinality129,
}

pub type FeedNameSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const FeedNameValue,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;

pub type FeedCardinalitySinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const FeedCardinality,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;

pub type FeedOverlapSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const FeedOverlap,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;

pub type DirectJoinSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const DirectJoinCell,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;

pub type MembershipCrossSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const MembershipCrossCell,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;

pub type UncoveredFeedSinkFn = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const UncoveredFeed,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;
