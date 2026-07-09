//! Error type for reading/writing the v4 format.
//!
//! The spec mandates that on any `reader MUST reject` check a conforming reader
//! "discards all partial state, returns a typed error, and exposes nothing from the
//! file" (§ intro). [`Error`] is that typed error; it carries enough context (kind,
//! location tag) to debug a rejection without echoing attacker-influenced bytes
//! (scope, counts) into a message.

use core::fmt;

/// Result alias for this crate.
pub type Result<T> = core::result::Result<T, Error>;

/// A structural, safety, or I/O failure while reading or writing a v4 file.
///
/// Variants map to the normative `reader MUST reject` / writer-side error rules in
/// `design-iprange-v4-livedb.md`. The message is for diagnostics; never trust
/// attacker-influenced bytes echoed into a message without escaping.
#[derive(Debug)]
#[non_exhaustive]
pub enum Error {
    /// `version_major` is not 4 (§5.1 forward-compat).
    UnsupportedMajor(u16),
    /// `meta_size` is < 90, > `page_size`, or (for `version_minor == 0`) not exactly
    /// 90 (§5.1).
    BadMetaSize(u16),
    /// The file is shorter than `2·page_size` or its claimed `total_pages` (§5/§9).
    FileTooShort {
        /// Bytes the structure needs.
        need: u64,
        /// Bytes actually available.
        have: u64,
    },
    /// The real file size is not a multiple of `page_size`, or disagrees with
    /// `total_pages` (§5/§9 step 2).
    FileSizeMismatch {
        /// Value implied by the meta (`total_pages · page_size`).
        header: u64,
        /// Real size from `fstat`.
        real: u64,
    },
    /// A reserved field, pad, or zero-fill region was non-zero (§5); carries a short
    /// location tag.
    NonZeroReserved(&'static str),
    /// An unsigned arithmetic operation on untrusted counts/offsets would overflow
    /// (§9 overflow rule).
    Overflow(&'static str),
    /// A structural invariant was violated (geometry, page type vs depth, separators,
    /// child pgnos, ordering, …). The static string is the specific rule.
    Structural(&'static str),
    /// A per-record / per-node invariant was violated during the §9 walk (e.g.
    /// unsorted, overlapping, leaf/branch occupancy, cross-leaf disjointness).
    Invariant(&'static str),
    /// A page CRC32C did not verify, or its high 32 bits were non-zero (D9, §9 step 3).
    ChecksumFailed(&'static str),
    /// An undamaged meta announces an unsupported format and the reader fails closed
    /// (§5.1 bootstrap class 2: `page_size`, `checksum_algo`, an unknown `flags` bit,
    /// or another fail-closed condition).
    Incompatible(&'static str),
    /// The file is locked by another writer.
    Locked(&'static str),
    /// The caller gave the writer input it cannot apply (e.g. `from > to`, wrong scope
    /// width, wrong key family, or growth past the `2^32`-page / `TREE_HEIGHT_MAX`
    /// limit). §8.
    InvalidInput(&'static str),
    /// The writer was poisoned by a failed commit: the commit's rebuild phase performs
    /// irreversible page alloc/free, so on a mid-phase error the in-memory allocator/registry
    /// state is indeterminate. The on-disk meta is unwritten — the file is still the last
    /// committed valid state — so the writer must be discarded and reopened. Every mutating
    /// op and commit refuses once poisoned.
    State(&'static str),
    /// The v4 state cannot be expressed as a v3 snapshot: the v3 writer rejected the
    /// exported `(range, value)` stream (§13 — `unique_ip_count` reaches `2^128`, the
    /// distinct `(type_id, value)` pairs exceed v3's values-table cap, or the caller's
    /// `type_id` / `scope` is not a conforming v3 value). Carries the v3 writer's
    /// reason. Distinct from a corrupt-v4 or family-mismatch error, which are normal.
    #[cfg(all(feature = "alloc", feature = "export-v3"))]
    ExportUnrepresentable(alloc::string::String),
    /// An underlying I/O error (only with `std`).
    #[cfg(feature = "std")]
    Io(std::io::Error),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::UnsupportedMajor(v) => write!(f, "unsupported version_major {v} (expected 4)"),
            Error::BadMetaSize(s) => write!(f, "invalid meta_size {s}"),
            Error::FileTooShort { need, have } => {
                write!(f, "file too short: need {need} bytes, have {have}")
            }
            Error::FileSizeMismatch { header, real } => {
                write!(f, "file_size mismatch: meta implies {header}, real {real}")
            }
            Error::NonZeroReserved(w) => write!(f, "non-zero reserved/pad field: {w}"),
            Error::Overflow(w) => write!(f, "arithmetic overflow validating {w}"),
            Error::Structural(w) => write!(f, "structural error: {w}"),
            Error::Invariant(w) => write!(f, "node/record invariant violated: {w}"),
            Error::ChecksumFailed(w) => write!(f, "page checksum failed: {w}"),
            Error::Incompatible(w) => write!(f, "incompatible (fail closed): {w}"),
            Error::InvalidInput(w) => write!(f, "invalid writer input: {w}"),
            Error::State(w) => write!(f, "invalid writer state: {w}"),
            #[cfg(all(feature = "alloc", feature = "export-v3"))]
            Error::ExportUnrepresentable(w) => {
                write!(f, "v4 state not representable as a v3 snapshot: {w}")
            }
            #[cfg(feature = "std")]
            Error::Locked(w) => write!(f, "locked: {w}"),
            Error::Io(e) => write!(f, "io error: {e}"),
        }
    }
}

#[cfg(feature = "std")]
impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Error::Io(e) => Some(e),
            _ => None,
        }
    }
}

#[cfg(feature = "std")]
impl From<std::io::Error> for Error {
    fn from(e: std::io::Error) -> Self {
        Error::Io(e)
    }
}
