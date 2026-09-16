//! Byte-exact diagnostics for the input load path.
//!
//! The C loaders write every load diagnostic with `fprintf(stderr, ...)`
//! over the bytes they were given (`src/iprange.c` `Cannot load ipset:
//! %s`, `src/ipset_load.c` `Cannot understand line No %d from %s: %s`).
//! A POSIX file name, an `@list` record and an input line may all hold
//! bytes that are not valid UTF-8, and those bytes must reach stderr
//! unchanged, so the load path accumulates its messages as [`Diag`] (a
//! byte string) instead of `String` and writes them with [`Diag::emit`].
//!
//! A message built only from `String` values keeps working through the
//! `From<String>` implementation, so `format!(...)` sites that carry no
//! name, list record, or raw input line need no change; `?` converts
//! them on the way out. The sites that do carry such bytes assemble
//! themselves with [`Diag::from_parts`].

use super::argv;

/// One diagnostic message, with the C interior newlines kept and the
/// closing newline left to [`Diag::emit`] (C terminates each `fprintf`
/// format with `\n`, and the C line buffer embedded in
/// `Cannot understand line` carries its own newline).
#[derive(Debug, Clone, Default)]
pub(crate) struct Diag(Vec<u8>);

impl Diag {
    /// A message assembled from byte parts, each written verbatim. Use
    /// this wherever a name, an `@list` record, or a raw input line is
    /// embedded; the parts are the bytes, never a lossy rendering.
    pub(crate) fn from_parts(parts: &[&[u8]]) -> Self {
        let mut msg = Vec::new();
        for part in parts {
            msg.extend_from_slice(part);
        }
        Diag(msg)
    }

    /// The message bytes, interior newlines included, closing newline
    /// excluded.
    pub(crate) fn bytes(&self) -> &[u8] {
        &self.0
    }

    /// Write the message to stderr followed by one newline, exactly as
    /// the C `fprintf(stderr, "...\n")` that produced it does.
    pub(crate) fn emit(&self) {
        argv::eprint_raw(&[&self.0]);
    }
}

impl From<String> for Diag {
    fn from(msg: String) -> Self {
        Diag(msg.into_bytes())
    }
}

impl From<&str> for Diag {
    fn from(msg: &str) -> Self {
        Diag(msg.as_bytes().to_vec())
    }
}
