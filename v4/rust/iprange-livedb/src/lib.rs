//! Reader and writer for the **iprange v4 live mutable on-disk DB format**.
//!
//! The on-disk contract is specified in
//! `.agents/sow/specs/design-iprange-v4-livedb.md` (LOCKED 2026-06-22). This crate is
//! the Rust **reference** implementation (and the C-facing library, decision O3); a
//! pure-Go implementation must read/write the same files (cross-read, §12).
//!
//! v4 is the **live working store** — a portable, mmap'd, copy-on-write B+tree of
//! fixed-size `[from, to, scope]` records, mutated in place (`set` / `delete`) without
//! a full rewrite. It complements the sealed v3 snapshot (`iprange-format`), which v4
//! exports to (§13).
//!
//! Layering (built bottom-up; this crate currently holds the foundation):
//! - [`spec`] — format constants, the §5.1 meta byte offsets, page geometry
//!   (`leaf_max`, `branch_max`), and limits.
//! - [`crc32c`] — the per-page CRC32C (Castagnoli) checksum (D9).
//! - [`key`] — IPv4 / IPv6 keys, compared numerically (v6 = `hi` then `lo`, no native
//!   128-bit type on the hot path), with the §4 `u128_inc` / `u128_dec` helpers.
//! - [`record`] — the fixed `[from, to, scope]` leaf record; `scope` is opaque (D11)
//!   and always **borrowed** (zero-copy).
//! - [`wire`] — unaligned little-endian field access (D8 forbids struct-pointer casts
//!   over the mmap'd bytes), the common 16-byte page header, and the meta page.
//! - `reader` / `writer` — the mmap reader and the COW writer (later increments).
//!
//! The core ([`spec`], [`crc32c`], [`key`], [`record`], [`wire`], [`error`]) is
//! `no_std`; the filesystem reader/writer will require `std`.
#![cfg_attr(not(feature = "std"), no_std)]
#![deny(unsafe_op_in_unsafe_fn)]
#![warn(missing_debug_implementations)]

pub mod crc32c;
pub mod error;
pub mod key;
pub mod node;
pub mod reader;
pub mod record;
pub mod spec;
pub mod wire;

pub use error::{Error, Result};
pub use key::{IpKey, Ipv4Key, Ipv6Key};
pub use reader::Reader;
pub use record::RecordRef;
pub use spec::IpVersion;
pub use wire::Meta;
