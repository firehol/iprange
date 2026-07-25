//! Main-file path rules shared by OS-backed entry points.

use std::ffi::OsStr;
use std::path::{Path, PathBuf};

use crate::error::{Error, Result};

pub(crate) fn canonical_sidecar(main: &Path) -> Result<PathBuf> {
    let name = main
        .file_name()
        .ok_or(Error::InvalidArgument("database path has no file name"))?;
    validate_main_name(name)?;

    let mut sidecar_name = name.to_os_string();
    sidecar_name.push(".readers");
    Ok(main.with_file_name(sidecar_name))
}

fn validate_main_name(name: &OsStr) -> Result<()> {
    let name = name.to_string_lossy();
    let bytes = name.as_bytes();
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

fn starts_with_ascii_case(value: &[u8], prefix: &[u8]) -> bool {
    value
        .get(..prefix.len())
        .is_some_and(|head| head.eq_ignore_ascii_case(prefix))
}

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
}
