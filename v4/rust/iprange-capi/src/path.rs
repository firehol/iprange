//! Exact platform path conversion.

use std::path::PathBuf;

use crate::abi::{Path, PATH_POSIX_BYTES, PATH_WINDOWS_UTF16};
use crate::error::BoundaryError;

pub(crate) unsafe fn decode(path: Path) -> Result<PathBuf, BoundaryError> {
    if path.reserved != 0 {
        return Err(BoundaryError::reserved("path.reserved"));
    }
    if path.length == 0 {
        return Err(BoundaryError::invalid_length("path is empty"));
    }
    match path.kind {
        PATH_POSIX_BYTES => {
            #[cfg(unix)]
            {
                use std::ffi::OsStr;
                use std::os::unix::ffi::OsStrExt;

                // SAFETY: the ABI caller promises readable storage for validated length.
                let bytes = unsafe { crate::error::input_slice(path.pointer.cast(), path.length)? };
                if bytes.contains(&0) {
                    return Err(BoundaryError::invalid_argument(
                        "POSIX path contains a NUL byte",
                    ));
                }
                Ok(PathBuf::from(OsStr::from_bytes(bytes)))
            }
            #[cfg(not(unix))]
            {
                Err(BoundaryError::invalid_enum(
                    "POSIX path is not valid on this platform",
                ))
            }
        }
        PATH_WINDOWS_UTF16 => {
            #[cfg(windows)]
            {
                use std::ffi::OsString;
                use std::os::windows::ffi::OsStringExt;

                // SAFETY: the ABI caller promises readable aligned storage.
                let units = unsafe { crate::error::input_slice(path.pointer.cast(), path.length)? };
                if units.contains(&0)
                    || std::char::decode_utf16(units.iter().copied()).any(|c| c.is_err())
                {
                    return Err(BoundaryError::invalid_argument(
                        "Windows path is not well-formed UTF-16",
                    ));
                }
                Ok(PathBuf::from(OsString::from_wide(units)))
            }
            #[cfg(not(windows))]
            {
                Err(BoundaryError::invalid_enum(
                    "Windows path is not valid on this platform",
                ))
            }
        }
        _ => Err(BoundaryError::invalid_enum("unknown path kind")),
    }
}
