//! Rust SDK for the exact iprange v4 database.
//!
//! Public APIs expose addresses, values, feeds, metadata, and durable
//! operations. Page numbers, roots, membership IDs, and allocator state remain
//! private.

#![deny(unsafe_op_in_unsafe_fn)]
#![warn(missing_debug_implementations)]

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
mod draft_store;
#[path = "sdk_error.rs"]
pub mod error;
mod fault;
mod feed;
mod feed_catalog;
mod feed_range_cursor;
mod file_io;
mod fixed_tree;
mod free_bitmap;
pub mod key;
// Recovery and compact snapshots wire this tested private builder in later slices.
#[allow(dead_code)]
mod immutable_output;
mod live_cleanup;
mod live_lifecycle;
mod live_lock;
mod live_reader;
mod live_sidecar;
mod live_writer;
mod membership_delta;
mod membership_dictionary;
mod membership_tree;
mod membership_view;
mod metadata;
mod name_binding;
mod path;
// The portable result contract is public; platform publication stays internal.
#[allow(dead_code)]
pub mod publication;
mod random;
mod range_cursor;
mod range_mutation;
mod range_tree;
pub mod recovery;
mod retirement;
mod slotted_page;
pub mod snapshot;
mod source;
mod used_bitmap;
pub mod validation;
mod workflow;

pub use bootstrap::MetaSelection;
pub use cancellation::CancellationToken;
pub use cardinality::{Cardinality129, CardinalityOverflow};
pub use commit_resolution::{
    resolve_commit, CommitResolution, CommitResolutionMode, CommitResolutionResult,
    LocalFileRelation,
};
pub use contract::{
    AddressFamily, MembershipOperation, ValueKind, ValueTag, MAX_METADATA_UNCOMPRESSED,
};
pub use database::{DatabaseInfo, ImmutableReader};
pub use error::{Error, ErrorCode, Result};
pub use feed::{FeedEntry, FeedName};
pub use feed_catalog::FeedCursor;
pub use feed_range_cursor::{FeedRangeCursorV4, FeedRangeCursorV6};
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
    CreationState, DirectReplacement, DirectTransaction, FeedRef, FinishedWorkflow, LiveWriter,
    LocalBasename, MembershipImport, MembershipImportSource, MembershipRef, MembershipTransaction,
    PreparedFeedChange, PreparedWorkflow, ReclaimResult, ReplaceFeed, RetentionRefresh,
    TransactionBudget, TransactionFeedCursor,
};
pub use membership_view::MembershipView;
pub use publication::{
    inspect_publication_residue, remove_publication_residue, resolve_publication,
    PublicationResidueCoordination, PublicationResidueHandle, PublicationResidueInspection,
    PublicationResidueMain, PublicationResidueMainContent, PublicationResidueRemoval,
    PublicationResolutionMode,
};
pub use range_cursor::{DirectCursorV4, DirectCursorV6, DirectRange, RangeDirection};
pub use snapshot::{
    snapshot_to, SnapshotBudget, SnapshotOutcome, SnapshotPreparationFailure,
    SnapshotPublicationPolicy, SnapshotResult, SnapshotSourceMode,
};
pub use source::{RangeSource, SliceSource};
pub use workflow::{AddressRange, LogicalChange, WorkflowKind, WorkflowReport};

#[cfg(test)]
mod live_crash_tests;
#[cfg(test)]
mod test_alloc;
