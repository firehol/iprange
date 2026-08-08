//! Platform-specific retained namespace operations.

use std::io;

use crate::validation::LocalFileIdentity;

#[cfg(unix)]
#[path = "namespace/unix.rs"]
mod platform;

#[cfg(windows)]
#[path = "namespace/windows.rs"]
mod platform;

pub(crate) use platform::*;

pub(crate) fn local_identity(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: IDENTITY_KIND,
        bytes: identity.encode(),
    }
}

pub(crate) fn identity_from_local(value: LocalFileIdentity) -> Option<Identity> {
    (value.kind == IDENTITY_KIND)
        .then(|| Identity::decode(value.bytes))
        .flatten()
}

pub(crate) fn is_nofollow_symlink(error: &io::Error) -> bool {
    #[cfg(unix)]
    {
        let code = error.raw_os_error();
        code == Some(libc::ELOOP) || cfg!(target_os = "freebsd") && code == Some(libc::EMLINK)
    }
    #[cfg(not(unix))]
    {
        let _ = error;
        false
    }
}

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
