//! Stable generation-1 C ABI for the iprange v4 database.

#![deny(unsafe_op_in_unsafe_fn)]
// Export pointer contracts live once in the generated C header, not 138 repeated stubs.
#![allow(clippy::missing_safety_doc)]
#![warn(missing_debug_implementations)]

mod abi;
mod abi_extra;
mod callback;
mod cursor;
mod error;
mod export;
mod facts;
mod feed_batch;
mod handle;
mod ip;
mod lifecycle;
mod lifecycle_ops;
mod maintenance;
mod maintenance_encode;
mod membership;
mod obligation;
mod path;
mod publication_ops;
mod reader;
mod registry;
mod report;
mod report_extended;
mod scan;
mod sink;
mod source;
mod validation_recovery;
mod workflow;
mod writer;

pub use abi::*;
pub use abi_extra::*;
pub use error::*;
pub use handle::{
    BorrowedMembershipViewHandle, CursorHandle, MembershipBuilderHandle, MembershipRefHandle,
    MembershipViewHandle, ReaderHandle, WriterFeedRefHandle, WriterHandle,
};
pub use obligation::{CleanupGuardHandle, ResidueHandle};
pub use registry::*;
pub use report::ReportHandle;

#[cfg(all(test, unix))]
mod tests;
