//! Rust SDK for the exact iprange v4 database.
//!
//! Public APIs expose addresses, values, feeds, metadata, and durable
//! operations. Page numbers, roots, membership IDs, and allocator state remain
//! private.

#![deny(unsafe_op_in_unsafe_fn)]
// Constructing the large public Error enum on successful hot-path lookups was
// measured as dominant work; lazy Option errors are intentional.
#![allow(clippy::unnecessary_lazy_evaluations)]
#![warn(missing_debug_implementations)]

mod artifact_name;
mod bitmap_page;
mod blob_tree;
mod bootstrap;
#[doc(hidden)]
pub mod c_abi_support;
mod cancellation;
pub mod cardinality;
mod commit_resolution;
mod contract;
#[path = "checksum.rs"]
mod crc32c;
mod database;
mod database_file;
mod draft_store;
#[path = "sdk_error.rs"]
pub mod error;
mod fault;
mod feed;
mod feed_catalog;
mod feed_range_cursor;
mod fixed_tree;
mod format;
mod free_bitmap;
mod heap;
mod history;
mod immutable_feed;
mod immutable_output;
pub mod key;
mod live_cleanup;
mod live_lifecycle;
mod live_lock;
mod live_namespace;
mod live_reader;
mod live_sidecar;
mod live_writer;
mod mapped_bytes;
mod mapping;
mod membership_delta;
mod membership_dictionary;
mod membership_query;
mod membership_tree;
mod membership_view;
mod metadata;
mod name_binding;
mod page_checksum;
mod page_header;
mod page_io;
mod path;
mod process_identity;
pub mod publication;
mod random;
mod range_bulk;
mod range_cursor;
mod range_mutation;
mod range_store_cursor;
mod range_tree;
mod reader_core;
pub mod recovery;
mod retirement;
mod slotted_page;
pub mod snapshot;
mod source;
mod used_bitmap;
pub mod validation;
mod work;
mod worker;
mod workflow;
mod writer_core;

pub use bootstrap::MetaSelection;
pub use cancellation::CancellationToken;
pub use cardinality::{Cardinality129, CardinalityOverflow};
pub use commit_resolution::{
    resolve_commit, CommitResolution, CommitResolutionMode, CommitResolutionResult,
    LocalFileRelation,
};
pub use contract::{
    AddressFamily, DirectSemantic, MembershipOperation, ValueKind, ValueTag,
    MAX_METADATA_UNCOMPRESSED,
};
pub use database::{DatabaseInfo, ImmutableReader};
pub use error::{Error, ErrorCode, Result};
pub use feed::{FeedEntry, FeedName};
pub use feed_catalog::FeedCursor;
pub use feed_range_cursor::{FeedRangeCursorV4, FeedRangeCursorV6};
pub use history::{HistoryProjectionReport, HistoryWindow, HistoryWindowReport};
pub use immutable_feed::{
    create_immutable_feed_v4, create_immutable_feed_v6, ImmutableFeedBudget, ImmutableFeedOutcome,
    ImmutableFeedPreparationFailure, ImmutableFeedReport, ImmutableFeedResult,
};
pub use key::{Ipv4Key, Ipv6Key};
pub use live_lifecycle::{
    initialize_live, reset_live_coordination, resolve_create_live,
    resolve_interrupted_live_transition, resolve_live_transition, LiveCoordinationLocation,
    LiveResetPolicy, LiveResidueKind, LiveResidueResult, LiveResidueStatus,
    LiveTransitionOperation, LiveTransitionResolutionMode, LiveTransitionResult,
    LiveTransitionStatus,
};
pub use live_reader::{LiveReader, ReaderCloseResult};
pub use live_writer::{
    create_live, AbortOutcome, AbortResult, CloseOutcome, CloseResult, CommitCleanupArtifact,
    CommitCleanupArtifacts, CommitDurability, CommitResult, CreateFeed, CreateResult,
    CreationState, DirectReplacement, DirectTransaction, FeedRef, FinishedHistoryProjection,
    FinishedWorkflow, FirstSeenRefresh, HistoryProjectionSource, LastSeenRefresh, LiveWriter,
    LocalBasename, MembershipImport, MembershipImportSource, MembershipRef, MembershipTransaction,
    PreparedFeedChange, PreparedHistoryProjection, PreparedWorkflow, ReclaimResult, ReplaceFeed,
    TransactionBudget, TransactionFeedCursor,
};
pub use membership_query::{
    AlgebraComparisonReport, AlgebraCountReport, AlgebraOutputBudget, AlgebraOutputMode,
    AlgebraPreparationFailure, AlgebraSetOperation, AlgebraSetOutcome, AlgebraSetReport,
    AlgebraSetResult, DirectJoinBudget, DirectJoinCell, DirectJoinReport, DirectJoinSink,
    DirectJoinSource, FeedCardinality, FeedOverlap, FeedPair, FeedSelection, MatchingFeedSink,
    MatchingFeedsReport, MembershipAggregateSink, MembershipAggregationMode,
    MembershipAggregationReport, MembershipAlgebra, MembershipAlgebraBudget, MembershipCrossCell,
    MembershipJoinReport, MembershipJoinSink, MembershipQuery, MembershipQueryBudget,
    MembershipScope, UncoveredFeed, UncoveredSide,
};
pub use membership_view::MembershipView;
pub use publication::{
    inspect_publication_residue, remove_publication_residue, resolve_publication,
    PublicationPolicy, PublicationResidueCoordination, PublicationResidueHandle,
    PublicationResidueInspection, PublicationResidueMain, PublicationResidueMainContent,
    PublicationResidueRemoval, PublicationResolutionMode, PublicationStatus,
};
pub use range_cursor::{DirectCursorV4, DirectCursorV6, DirectRange, RangeDirection};
pub use snapshot::{
    snapshot_to, SnapshotBudget, SnapshotOutcome, SnapshotPreparationFailure,
    SnapshotPublicationPolicy, SnapshotResult, SnapshotSourceMode,
};
pub use source::{
    DirectRangeSourceV4, DirectRangeSourceV6, FeedRangeSourceV4, FeedRangeSourceV6, RangeSource,
    SliceSource,
};
pub use workflow::{
    AddressRange, FirstSeenRemoval, FirstSeenRemovalSink, LogicalChange, WorkflowKind,
    WorkflowReport,
};

#[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
mod live_crash_tests;
#[cfg(all(test, target_os = "linux"))]
mod mmap_runtime_tests;
#[cfg(test)]
mod test_alloc;
#[cfg(test)]
mod test_support_tests;
