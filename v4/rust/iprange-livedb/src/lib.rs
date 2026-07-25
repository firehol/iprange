//! Rust SDK for the exact iprange v4 database.
//!
//! Public APIs expose addresses, values, feeds, metadata, and durable
//! operations. Page numbers, roots, membership IDs, and allocator state remain
//! private.

#![deny(unsafe_op_in_unsafe_fn)]
#![warn(missing_debug_implementations)]

mod bootstrap;
pub mod cardinality;
mod contract;
#[path = "checksum.rs"]
mod crc32c;
mod database;
#[path = "sdk_error.rs"]
pub mod error;
pub mod key;
mod path;

pub use bootstrap::MetaSelection;
pub use cardinality::{Cardinality129, CardinalityOverflow};
pub use contract::{AddressFamily, ValueKind, ValueTag};
pub use database::{DatabaseInfo, ImmutableReader};
pub use error::{Error, ErrorCode, Result};
pub use key::{Ipv4Key, Ipv6Key};
