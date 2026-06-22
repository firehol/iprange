//! Error type for reading/writing the v3 format.
//!
//! The spec mandates that "where a check is labelled `reader MUST reject`, a
//! conforming reader discards any partial state, returns a typed error, and exposes
//! nothing from the file" (§ intro). [`Error`] is that typed error; it deliberately
//! carries enough context (kind, offset, expected/actual) to debug a rejection
//! without leaking section contents.

use core::fmt;

/// Result alias for this crate.
pub type Result<T> = core::result::Result<T, Error>;

/// A structural, safety, or I/O failure while reading or writing a v3 file.
///
/// Variants map to the normative `reader MUST reject` / writer-side error rules in
/// `binary-format-v3.md`. The message is for diagnostics; never trust attacker-
/// influenced bytes (feed-meta, value bytes) echoed into a message without escaping.
#[derive(Debug)]
#[non_exhaustive]
pub enum Error {
    /// `magic != "IPRANGE3"` (compared bytewise, §5).
    BadMagic,
    /// `version_major` is not 3 (§5/§12).
    UnsupportedMajor(u16),
    /// `header_size` is < 72, not a multiple of 8, or (for `version_minor == 0`)
    /// not exactly 72 (§5/§12).
    BadHeaderSize(u16),
    /// The file is shorter than its own header or a claimed structure (§15 step 1/2).
    FileTooShort {
        /// Bytes the structure needs.
        need: u64,
        /// Bytes actually available.
        have: u64,
    },
    /// Header `file_size` does not equal the real file size (§15 step 3).
    FileSizeMismatch {
        /// Value claimed in the header.
        header: u64,
        /// Real size from `fstat`.
        real: u64,
    },
    /// A reserved field or pad byte was non-zero (§3); carries a short location tag.
    NonZeroReserved(&'static str),
    /// An unsigned arithmetic operation on untrusted counts/offsets would overflow
    /// (§15 overflow rule).
    Overflow(&'static str),
    /// A structural invariant was violated (offsets, ordering, alignment, lengths,
    /// section presence/duplication, …). The static string is the specific rule.
    Structural(&'static str),
    /// A per-record / per-value safety or determinism invariant was violated during
    /// the §9/§15 walk (e.g. unsorted, overlapping, bad `value_id`, non-zero `pad`).
    Invariant(&'static str),
    /// A section hash (or, later, a signature) did not verify (§15 step 12).
    IntegrityFailed(&'static str),
    /// The writer was given input it cannot encode (e.g. overlapping ranges, an
    /// empty membership set, a full-IPv6-space range, > `2^32` distinct values).
    InvalidInput(&'static str),
    /// An underlying I/O error (only with `std`).
    #[cfg(feature = "std")]
    Io(std::io::Error),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::BadMagic => f.write_str("bad magic (not IPRANGE3)"),
            Error::UnsupportedMajor(v) => write!(f, "unsupported version_major {v} (expected 3)"),
            Error::BadHeaderSize(s) => write!(f, "invalid header_size {s}"),
            Error::FileTooShort { need, have } => {
                write!(f, "file too short: need {need} bytes, have {have}")
            }
            Error::FileSizeMismatch { header, real } => {
                write!(f, "file_size mismatch: header={header}, real={real}")
            }
            Error::NonZeroReserved(w) => write!(f, "non-zero reserved/pad field: {w}"),
            Error::Overflow(w) => write!(f, "arithmetic overflow validating {w}"),
            Error::Structural(w) => write!(f, "structural error: {w}"),
            Error::Invariant(w) => write!(f, "record/value invariant violated: {w}"),
            Error::IntegrityFailed(w) => write!(f, "integrity check failed: {w}"),
            Error::InvalidInput(w) => write!(f, "invalid writer input: {w}"),
            #[cfg(feature = "std")]
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
