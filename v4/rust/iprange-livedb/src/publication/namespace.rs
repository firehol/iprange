//! Platform-specific retained namespace operations.

use std::io;

#[cfg(unix)]
#[path = "namespace/unix.rs"]
mod platform;

#[cfg(windows)]
#[path = "namespace/windows.rs"]
mod platform;

pub(crate) use platform::*;

pub(crate) const fn require_exchange_available() -> Result<(), NamespaceError> {
    if cfg!(any(target_os = "linux", target_vendor = "apple")) {
        Ok(())
    } else {
        Err(NamespaceError::Unsupported)
    }
}

#[derive(Debug)]
pub(crate) enum NamespaceError {
    InvalidName,
    NotDirectory,
    NotRegular,
    Exists,
    Missing,
    IdentityChanged,
    LinkCount(u64),
    CrossFilesystem,
    AccessPolicy,
    Unsupported,
    ForkedHandle,
    Io(io::Error),
    IoAt {
        operation: &'static str,
        source: io::Error,
    },
}
