//! Rust SDK for the exact iprange v4 database.
//!
//! Public APIs expose addresses, values, feeds, metadata, and durable
//! operations. Page numbers, roots, membership IDs, and allocator state remain
//! private.

#![deny(unsafe_op_in_unsafe_fn)]
#![warn(missing_debug_implementations)]

mod blob_tree;
mod bootstrap;
pub mod cardinality;
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
mod file_io;
mod fixed_tree;
mod free_bitmap;
pub mod key;
mod live_lock;
mod live_reader;
mod live_sidecar;
mod live_writer;
mod membership_delta;
mod membership_dictionary;
mod membership_tree;
mod membership_view;
mod metadata;
mod path;
mod random;
mod range_cursor;
mod range_mutation;
mod range_tree;
mod retirement;
mod slotted_page;
mod used_bitmap;

pub use bootstrap::MetaSelection;
pub use cardinality::{Cardinality129, CardinalityOverflow};
pub use contract::{
    AddressFamily, MembershipOperation, ValueKind, ValueTag, MAX_METADATA_UNCOMPRESSED,
};
pub use database::{DatabaseInfo, ImmutableReader};
pub use error::{Error, ErrorCode, Result};
pub use feed::{FeedEntry, FeedName};
pub use feed_catalog::FeedCursor;
pub use key::{Ipv4Key, Ipv6Key};
pub use live_reader::LiveReader;
pub use live_writer::{
    create_live, CommitDurability, CommitResult, CreateResult, CreationState, FeedRef, LiveWriter,
    MembershipRef, MembershipTransaction, ReclaimResult, TransactionBudget, TransactionFeedCursor,
};
pub use membership_view::MembershipView;
pub use range_cursor::{DirectCursorV4, DirectCursorV6, DirectRange, RangeDirection};

#[cfg(test)]
mod live_crash_tests;
#[cfg(test)]
mod test_alloc;
