//! Reader and writer for the **iprange v3 portable binary threat-intel format**.
//!
//! The on-disk contract is specified, byte for byte, in
//! `.agents/sow/specs/binary-format-v3.md` (locked 2026-06-21). This crate is the
//! Rust reference implementation; a pure-Go implementation must produce
//! byte-identical files against the same conformance corpus.
//!
//! Layering:
//! - [`spec`] — the format constants (magic, sizes, kinds, alignments, sentinels).
//! - [`key`] — IPv4 / IPv6 keys, compared numerically (v6 = `hi` then `lo`, no
//!   128-bit type on the lookup hot path), with the producer-side 128-bit helpers.
//! - [`wire`] — field-by-field (de)serialization of the fixed on-disk structures.
//! - `writer` / `reader` — added in build-order steps (b) and (c).
//!
//! The core ([`spec`], [`key`], [`wire`], [`error`]) is `no_std` + `alloc`; the
//! filesystem writer and the mmap reader require `std`.
#![cfg_attr(not(feature = "std"), no_std)]
#![deny(unsafe_op_in_unsafe_fn)]
#![warn(missing_debug_implementations)]

#[cfg(feature = "alloc")]
extern crate alloc;

pub mod error;
pub mod key;
pub mod reader;
pub mod spec;
pub mod wire;

#[cfg(feature = "alloc")]
pub mod writer;

#[cfg(feature = "alloc")]
pub mod legacy;

pub use error::{Error, Result};
pub use key::{Ipv4Key, Ipv6Key};
pub use reader::{FeedMetaView, Hit, Reader, ValueRef};
pub use spec::IpVersion;

#[cfg(feature = "alloc")]
pub use writer::{FeedMeta, Value, Writer};
