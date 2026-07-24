//! Rust implementation of the exact unsigned Phase-1 iprange v4 database.
//!
//! The normative contract is `.agents/sow/specs/binary-format-v4.md`. Physical
//! pages, roots, membership IDs, bitmap words, allocator state, and publication
//! machinery remain private; public APIs expose semantic database operations.

#![cfg_attr(not(feature = "std"), no_std)]
#![deny(unsafe_op_in_unsafe_fn)]
#![warn(missing_debug_implementations)]

#[cfg(feature = "alloc")]
extern crate alloc;

#[allow(dead_code)]
mod bitmap_cow;
#[allow(dead_code)]
mod bitmap_page;
#[allow(dead_code)]
mod bitmap_reader;
#[allow(dead_code)]
mod blob_page;
#[allow(dead_code)]
mod blob_reader;
#[allow(dead_code)]
mod bootstrap;
pub mod cardinality;
#[allow(dead_code)]
mod contract;
mod crc32c;
pub mod error;
pub mod key;
#[allow(dead_code)]
mod name_binding;
#[cfg(feature = "os")]
#[allow(dead_code)]
mod os;
#[allow(dead_code)]
mod page;
#[allow(dead_code)]
mod page_number_index;
#[allow(dead_code)]
mod page_source;
#[allow(dead_code)]
mod private_page_pool;
#[allow(dead_code)]
mod process_identity;
#[allow(dead_code)]
mod range_builder;
#[allow(dead_code)]
mod range_page;
#[allow(dead_code)]
mod range_pool_sink;
#[allow(dead_code)]
mod range_reader;
#[allow(dead_code)]
mod range_staging;
#[allow(dead_code)]
mod reclamation_finalizer;
#[allow(dead_code)]
mod reservation;
#[allow(dead_code)]
mod retirement_page;
#[allow(dead_code)]
mod retirement_reader;
#[allow(dead_code)]
mod retirement_writer;
#[allow(dead_code)]
mod sequential_assignment;
#[allow(dead_code)]
mod sidecar;
#[allow(dead_code)]
mod sidecar_transition;
#[allow(dead_code)]
mod writer_fixed_point;
#[allow(dead_code)]
mod writer_result_contract;
#[allow(dead_code)]
mod writer_transaction_contract;
#[allow(dead_code)]
mod writer_transaction_core;

pub use cardinality::{Cardinality129, CardinalityOverflow};
pub use contract::{AddressFamily, ValueKind, ValueTag};
pub use error::{Error, ErrorCode, Result};
pub use key::{Ipv4Key, Ipv6Key};

#[cfg(test)]
extern crate std;
#[cfg(test)]
mod test_alloc;
