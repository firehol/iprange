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

#[cfg(feature = "alloc")]
extern crate alloc;

pub mod crc32c;
pub mod cursor;
pub mod error;
pub mod key;
pub mod node;
pub mod reader;
pub mod record;
pub mod spec;
pub mod wire;

#[cfg(feature = "alloc")]
pub mod writer;
pub mod migrate;
pub mod extsort;
pub mod scope_table;
#[cfg(feature = "os")]
pub mod readers;

#[cfg(feature = "alloc")]
pub mod page_store;

/// The v4.1 scope table (§C.2, §D): the per-scope metadata registry. Requires `alloc`.
#[cfg(feature = "alloc")]
pub mod scope;

/// The v4.1 per-scope KV store (§C.4, §D): a slot-directory B+tree behind each scope's
/// `kv_root`. Requires `alloc`.
#[cfg(feature = "alloc")]
pub(crate) mod kv;

/// The v4 -> v3 snapshot bridge (§13): export a sealed, canonical v3 file from a
/// validated v4 image. Opt-in (`export-v3` feature) so the core stays free of the v3
/// crate dependency.
#[cfg(feature = "export-v3")]
pub mod export;

/// The Unix file layer (mmap reader + pread/pwrite writer with `flock` and the §10
/// hardening). Unix-only; on other targets use [`Reader`] over bytes and [`Writer`]
/// over an in-memory image.
#[cfg(all(feature = "os", unix))]
pub mod os;

pub use cursor::Cursor;
pub use error::{Error, Result};
pub use key::{IpKey, Ipv4Key, Ipv6Key};
pub use reader::Reader;
pub use record::RecordRef;
pub use spec::IpVersion;
pub use wire::Meta;

#[cfg(feature = "alloc")]
pub use writer::{Changed, MetaEntry, Writer};
pub use migrate::{migrate, Change, DesiredRecord, DesiredStream, MigrateCounters, MigrateOptions};
pub use extsort::{ext_sort, SortedStream, ExtSortConfig};

#[cfg(feature = "export-v3")]
pub use export::{export_v3, V3Meta};
