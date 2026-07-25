//! Exact Linux creator-only access proof and commitment.

use std::fs::File;
use std::io;
use std::os::fd::AsRawFd;
use std::os::unix::fs::MetadataExt;

use sha2::{Digest, Sha256};

use super::namespace::NamespaceError;

const DOMAIN: &[u8; 8] = b"IPR4PSEC";
const ACCESS_ACL: &[u8] = b"system.posix_acl_access\0";
pub(crate) const CREATOR_MODE: u32 = 0o600;

#[derive(Clone, Copy, Debug)]
pub(crate) struct Profile {
    uid: u32,
    commitment: [u8; 32],
}

impl Profile {
    pub(crate) fn capture() -> Self {
        let uid = unsafe { libc::geteuid() };
        Self {
            uid,
            commitment: commitment(uid),
        }
    }

    pub(crate) fn commitment(self) -> [u8; 32] {
        self.commitment
    }
}

pub(crate) fn secure_creator_only(file: &File, profile: Profile) -> Result<(), NamespaceError> {
    if unsafe { libc::fchmod(file.as_raw_fd(), CREATOR_MODE) } != 0 {
        return Err(last_error("apply creator-only mode"));
    }
    remove_access_acl(file)?;
    let metadata = creator_only_metadata(file)?;
    if metadata.uid() != profile.uid || commitment(metadata.uid()) != profile.commitment {
        return Err(NamespaceError::AccessPolicy);
    }
    Ok(())
}

pub(crate) fn creator_only_commitment(file: &File) -> Result<[u8; 32], NamespaceError> {
    let metadata = creator_only_metadata(file)?;
    Ok(commitment(metadata.uid()))
}

fn creator_only_metadata(file: &File) -> Result<std::fs::Metadata, NamespaceError> {
    require_no_access_acl(file)?;
    let metadata = file.metadata().map_err(NamespaceError::Io)?;
    if !metadata.is_file() || metadata.nlink() != 1 || metadata.mode() & 0o7777 != CREATOR_MODE {
        return Err(NamespaceError::AccessPolicy);
    }
    Ok(metadata)
}

fn commitment(uid: u32) -> [u8; 32] {
    let mut hasher = Sha256::new();
    hasher.update(DOMAIN);
    hasher.update(uid.to_le_bytes());
    hasher.update(CREATOR_MODE.to_le_bytes());
    hasher.finalize().into()
}

fn remove_access_acl(file: &File) -> Result<(), NamespaceError> {
    let result = unsafe { libc::fremovexattr(file.as_raw_fd(), ACCESS_ACL.as_ptr().cast()) };
    if result == 0 {
        return Ok(());
    }
    let source = io::Error::last_os_error();
    match source.raw_os_error() {
        Some(libc::ENODATA | libc::EOPNOTSUPP) => Ok(()),
        _ => Err(NamespaceError::IoAt {
            operation: "remove inherited access ACL",
            source,
        }),
    }
}

fn require_no_access_acl(file: &File) -> Result<(), NamespaceError> {
    let result = unsafe {
        libc::fgetxattr(
            file.as_raw_fd(),
            ACCESS_ACL.as_ptr().cast(),
            std::ptr::null_mut(),
            0,
        )
    };
    if result >= 0 {
        return Err(NamespaceError::AccessPolicy);
    }
    let source = io::Error::last_os_error();
    match source.raw_os_error() {
        Some(libc::ENODATA | libc::EOPNOTSUPP) => Ok(()),
        _ => Err(NamespaceError::IoAt {
            operation: "verify absent access ACL",
            source,
        }),
    }
}

fn last_error(operation: &'static str) -> NamespaceError {
    NamespaceError::IoAt {
        operation,
        source: io::Error::last_os_error(),
    }
}
