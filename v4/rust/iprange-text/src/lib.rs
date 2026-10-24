//! Shared C-compatible IP text parser, independent of the database SDK.
//!
//! Iterate [`Records`] over raw bytes, apply [`cstr`] to each record, and
//! call [`classify_v4`] or [`classify_v6`]. Parse address tokens with
//! [`parse_token`]; [`FamilyImpl`] provides IPv4 (`u32`) and IPv6 (`u128`)
//! semantics. [`Range`] and [`IpSet`] hold numeric ranges in memory.
//!
//! Classification returns [`LineOutcome`], including legacy warning text.
//! Callers decide whether to reject, skip, count, or report malformed input.
//! File loading, binary detection, exit codes and output remain caller policy.
//! [`dns::Resolver`] queues hostnames and yields [`dns::Batch`] replies;
//! its caller-supplied [`DiagnosticSink`] receives diagnostics synchronously,
//! including the batch summary on drop. No diagnostics go directly to stderr.

pub mod dns;
pub mod family;
pub mod grammar;
mod ipv4;
mod ipv6;
pub mod range;

pub use dns::DiagnosticSink;
pub use family::{Family, FamilyImpl};
pub use grammar::{
    classify_v4, classify_v6, colons, cstr, parse_token, LineOutcome, Records, MAX_LINE,
};
pub use range::{IpNum, IpSet, Range};
