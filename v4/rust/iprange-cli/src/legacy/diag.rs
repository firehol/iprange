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

use std::io::Write;

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

/// The bytes C `%s` would print for one buffer: everything up to the
/// first NUL byte. A header line or a file name may hold NUL bytes,
/// and `fprintf` stops at the first one.
pub(crate) fn cstr(value: &[u8]) -> &[u8] {
    match value.iter().position(|&b| b == 0) {
        Some(i) => &value[..i],
        None => value,
    }
}

/// C `s ? s : ""` in the "found '%s'" diagnostics: EOF renders as
/// an empty quoted string.
pub(crate) fn line_text(line: Option<&[u8]>) -> &[u8] {
    cstr(line.unwrap_or_default())
}

/// One binary-format diagnostic line, assembled over bytes.
///
/// Every message in this module starts with the C prefix
/// `fprintf(stderr, "%s: %s ...", PROG, ips->filename, ...)`: the
/// tool name, then the source label, then the family-specific text.
/// The source label is a file name and the echoed header values are
/// raw file bytes, so neither can travel as `String` - a POSIX name
/// may hold bytes that are not valid UTF-8, and rendering them as
/// U+FFFD would not be the line the C writes. The builder therefore
/// concatenates byte parts and hands them to the load path as a
/// [`Diag`].
pub(crate) struct DiagMsg(Vec<u8>);

impl DiagMsg {
    /// `iprange: ` + the source label bytes.
    pub(crate) fn new(source: &[u8]) -> Self {
        let mut msg = Vec::with_capacity(64 + source.len());
        msg.extend_from_slice(b"iprange: ");
        msg.extend_from_slice(source);
        DiagMsg(msg)
    }

    /// Append verbatim bytes (message punctuation, an echoed value).
    pub(crate) fn push(&mut self, text: &[u8]) -> &mut Self {
        self.0.extend_from_slice(text);
        self
    }

    /// Append a decimal number (the C `%zu`, `%lu`, `PRIu64`, `d`).
    pub(crate) fn number(&mut self, value: impl std::fmt::Display) -> &mut Self {
        // Writing into a Vec<u8> cannot fail.
        let _ = write!(self.0, "{value}");
        self
    }

    pub(crate) fn build(&self) -> Diag {
        Diag::from_parts(&[&self.0])
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
