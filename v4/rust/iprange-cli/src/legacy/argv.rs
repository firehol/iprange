//! The byte and ASCII views of OS strings used by the legacy command line.
//!
//! A POSIX file name is any sequence of non-NUL, non-slash bytes, so a
//! legal name need not be valid UTF-8. The released C tool echoes the
//! bytes it was given in the `-h` text, in CSV name fields and in the
//! `Invalid value` diagnostic, and the Go port does the same because Go
//! strings are byte strings. `String` cannot hold such a value, so the
//! legacy surface carries `OsString`/`PathBuf` and reaches their content
//! only through this module.
//!
//! Two views exist because the two uses differ:
//!
//! - [`bytes`] is what the tool writes and what it reads digits from:
//!   the OS bytes verbatim on unix, so invalid UTF-8 survives to stdout
//!   and stderr exactly as the C `fwrite`s it, and the UTF-8 encoding of
//!   the UTF-16 OS string on Windows. Both are zero-copy apart from a
//!   Windows name holding an unpaired surrogate.
//! - [`text`] identifies option tokens only. An argument holding bytes
//!   that are not valid UTF-8 cannot equal an ASCII token in this view
//!   (each invalid byte becomes U+FFFD), so it reaches the input-path
//!   branch exactly as it does under the C byte comparison. It must
//!   never be used for output or for a path.

use std::borrow::Cow;
use std::ffi::OsStr;
use std::path::PathBuf;

/// The bytes of `value`, for output and decimal parsing.
#[cfg(unix)]
pub(crate) fn bytes(value: &OsStr) -> Cow<'_, [u8]> {
    Cow::Borrowed(value.as_encoded_bytes())
}

/// The bytes of `value`, for output and decimal parsing.
///
/// A Windows OS string is a sequence of UTF-16 code units; the byte form
/// the tool writes is its UTF-8 encoding, which is what `to_str` yields
/// for every name without an unpaired surrogate.
#[cfg(not(unix))]
pub(crate) fn bytes(value: &OsStr) -> Cow<'_, [u8]> {
    match value.to_str() {
        Some(text) => Cow::Borrowed(text.as_bytes()),
        None => Cow::Owned(value.to_string_lossy().into_owned().into_bytes()),
    }
}

/// The ASCII view of `value`, for option-token comparison only.
#[cfg(unix)]
pub(crate) fn text(value: &OsStr) -> Cow<'_, str> {
    String::from_utf8_lossy(value.as_encoded_bytes())
}

/// The ASCII view of `value`, for option-token comparison only.
#[cfg(not(unix))]
pub(crate) fn text(value: &OsStr) -> Cow<'_, str> {
    value.to_string_lossy()
}

/// True for the one argument class the IPv6 scan discards: a token
/// whose first byte is `-` and which is longer than the single `-`
/// stdin argument.
///
/// C reads it as `argv[i][0] == '-' && argv[i][1] != '\0' &&
/// strcmp(argv[i], "-")` in the `iprange6_run()` re-scan
/// (`src/iprange6_main.c:176`), where every such token that was not
/// already consumed as an option is skipped as a flag. The comparison is
/// on bytes, so a name holding an invalid UTF-8 byte is classified by
/// its leading byte exactly as C classifies it.
pub(crate) fn is_dash_option(value: &OsStr) -> bool {
    let value = bytes(value);
    value.len() > 1 && value[0] == b'-'
}

/// `@LIST` -> `Some(LIST)`: the C option scan strips one leading `@`
/// and classifies the destination (directory or file list) on load.
/// `None` when `value` does not start with `@`.
#[cfg(unix)]
pub(crate) fn strip_at(value: &OsStr) -> Option<PathBuf> {
    use std::os::unix::ffi::OsStrExt;
    let value = value.as_encoded_bytes();
    if value.first() != Some(&b'@') {
        return None;
    }
    Some(PathBuf::from(OsStr::from_bytes(&value[1..])))
}

/// `@LIST` -> `Some(LIST)`: the C option scan strips one leading `@`
/// and classifies the destination (directory or file list) on load.
/// `None` when `value` does not start with `@`.
///
/// A Windows name has no byte-name concept to preserve, so the leading
/// `@` is stripped from the text view and the remainder re-encodes to a
/// `PathBuf` without loss.
#[cfg(not(unix))]
pub(crate) fn strip_at(value: &OsStr) -> Option<PathBuf> {
    let value = value.to_string_lossy();
    value.strip_prefix('@').map(PathBuf::from)
}

/// A path from raw record bytes.
///
/// An `@list` line names a file by its bytes (C `fopen(line, "r")` over
/// the record verbatim), so the name must not be decoded on the way to
/// the open: decoding a byte that is not valid UTF-8 to U+FFFD would
/// open a different file than the one the list names.
#[cfg(unix)]
pub(crate) fn path_from_bytes(value: &[u8]) -> PathBuf {
    use std::os::unix::ffi::OsStrExt;
    PathBuf::from(OsStr::from_bytes(value))
}

/// A path from raw record bytes.
///
/// A Windows name has no byte-name concept, so the closest representable
/// path is the UTF-8 decode of the bytes (invalid bytes become U+FFFD).
#[cfg(not(unix))]
pub(crate) fn path_from_bytes(value: &[u8]) -> PathBuf {
    PathBuf::from(String::from_utf8_lossy(value).into_owned())
}

/// Write `parts` consecutively to stderr, then a newline. `eprintln!`
/// cannot carry invalid UTF-8, so argv-derived text goes out as bytes,
/// matching the C `fprintf(stderr, ...)` byte for byte.
/// Write one text diagnostic to stderr. A closed or full stderr is
/// ignored, matching C `fprintf`: `eprintln!` would abort the process.
pub(crate) fn eprint_line(line: &str) {
    eprint_raw(&[line.as_bytes()]);
}

pub(crate) fn eprint_raw(parts: &[&[u8]]) {
    use std::io::Write;
    let stderr = std::io::stderr();
    let mut stderr = stderr.lock();
    for part in parts {
        let _ = stderr.write_all(part);
    }
    let _ = stderr.write_all(b"\n");
    let _ = stderr.flush();
}
