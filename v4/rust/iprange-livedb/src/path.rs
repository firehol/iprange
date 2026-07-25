//! Main-file path rules shared by OS-backed entry points.

use std::ffi::OsStr;
use std::path::{Path, PathBuf};

use crate::error::{Error, Result};

#[cfg(unix)]
use std::os::unix::ffi::OsStrExt;
#[cfg(windows)]
use std::os::windows::ffi::OsStrExt;

pub(crate) fn canonical_sidecar(main: &Path) -> Result<PathBuf> {
    let name = main
        .file_name()
        .ok_or(Error::InvalidArgument("database path has no file name"))?;
    validate_main_name(name)?;

    let mut sidecar_name = name.to_os_string();
    sidecar_name.push(".readers");
    Ok(main.with_file_name(sidecar_name))
}

pub(crate) fn live_transition_temp(main: &Path) -> Result<PathBuf> {
    let main_name = main
        .file_name()
        .ok_or(Error::InvalidArgument("database path has no file name"))?;
    validate_main_name(main_name)?;
    let mut name = main_name.to_os_string();
    name.push(".readers.reset");
    Ok(main.with_file_name(name))
}

pub(crate) fn validate_main_name(name: &OsStr) -> Result<()> {
    #[cfg(unix)]
    return validate_posix_name(name.as_bytes());
    #[cfg(windows)]
    return validate_windows_name(&name.encode_wide().collect::<Vec<_>>());
    #[cfg(not(any(unix, windows)))]
    return Err(Error::Unsupported(
        "database file names are not implemented on this platform",
    ));
}

#[cfg(unix)]
fn validate_posix_name(bytes: &[u8]) -> Result<()> {
    if bytes.is_empty() || bytes == b"." || bytes == b".." || bytes.contains(&0) {
        return Err(Error::InvalidArgument(
            "database file name is not one path component",
        ));
    }
    if starts_with_ascii_case(bytes, b".iprange-") {
        return Err(Error::InvalidArgument(
            "database file name uses the reserved .iprange- prefix",
        ));
    }
    if ends_with_ascii_case(bytes, b".readers") {
        return Err(Error::InvalidArgument(
            "database file name uses the reserved .readers suffix",
        ));
    }
    Ok(())
}

#[cfg(windows)]
fn validate_windows_name(units: &[u16]) -> Result<()> {
    const SLASH: u16 = b'/' as u16;
    const BACKSLASH: u16 = b'\\' as u16;
    const COLON: u16 = b':' as u16;
    const DOT: u16 = b'.' as u16;
    const SPACE: u16 = b' ' as u16;
    if units.is_empty()
        || units == [DOT]
        || units == [DOT, DOT]
        || units
            .iter()
            .any(|unit| matches!(*unit, 0 | SLASH | BACKSLASH | COLON))
        || units
            .last()
            .is_some_and(|unit| matches!(*unit, DOT | SPACE))
        || char::decode_utf16(units.iter().copied()).any(|character| character.is_err())
    {
        return Err(Error::InvalidArgument(
            "database file name is not one exact Windows component",
        ));
    }
    if starts_with_ascii_case_wide(units, b".iprange-")
        || ends_with_ascii_case_wide(units, b".readers")
        || is_windows_device_name(units)
    {
        return Err(Error::InvalidArgument(
            "database file name uses a reserved Windows spelling",
        ));
    }
    Ok(())
}

#[cfg(windows)]
fn starts_with_ascii_case_wide(value: &[u16], prefix: &[u8]) -> bool {
    value
        .get(..prefix.len())
        .is_some_and(|head| wide_ascii_eq(head, prefix))
}

#[cfg(windows)]
fn ends_with_ascii_case_wide(value: &[u16], suffix: &[u8]) -> bool {
    value
        .get(value.len().saturating_sub(suffix.len())..)
        .is_some_and(|tail| wide_ascii_eq(tail, suffix))
}

#[cfg(windows)]
fn wide_ascii_eq(wide: &[u16], ascii: &[u8]) -> bool {
    wide.iter().zip(ascii).all(|(&left, &right)| {
        left <= u16::from(u8::MAX) && (left as u8).eq_ignore_ascii_case(&right)
    })
}

#[cfg(windows)]
fn is_windows_device_name(units: &[u16]) -> bool {
    const ONE: u16 = b'1' as u16;
    const NINE: u16 = b'9' as u16;
    let stem = units
        .split(|&unit| unit == b'.' as u16)
        .next()
        .unwrap_or(units);
    [b"CON".as_slice(), b"PRN", b"AUX", b"NUL"]
        .iter()
        .any(|name| wide_ascii_eq(stem, name))
        || (stem.len() == 4
            && (wide_ascii_eq(&stem[..3], b"COM") || wide_ascii_eq(&stem[..3], b"LPT"))
            && matches!(stem[3], ONE..=NINE))
}

#[cfg(unix)]
fn starts_with_ascii_case(value: &[u8], prefix: &[u8]) -> bool {
    value
        .get(..prefix.len())
        .is_some_and(|head| head.eq_ignore_ascii_case(prefix))
}

#[cfg(unix)]
fn ends_with_ascii_case(value: &[u8], suffix: &[u8]) -> bool {
    value
        .get(value.len().saturating_sub(suffix.len())..)
        .is_some_and(|tail| tail.eq_ignore_ascii_case(suffix))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sidecar_name_is_exact() {
        assert_eq!(
            canonical_sidecar(Path::new("/tmp/feed.v4")).unwrap(),
            Path::new("/tmp/feed.v4.readers")
        );
    }

    #[test]
    fn reserved_names_are_ascii_case_insensitive() {
        for name in [
            ".iprange-private",
            ".IpRaNgE-private",
            "feed.readers",
            "feed.READERS",
        ] {
            assert!(canonical_sidecar(Path::new(name)).is_err(), "{name}");
        }
    }

    #[test]
    fn live_transition_temp_is_path_bound() {
        assert_eq!(
            live_transition_temp(Path::new("/tmp/feed.v4")).unwrap(),
            Path::new("/tmp/feed.v4.readers.reset")
        );
    }
}
