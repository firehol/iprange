//! Exact POSIX creator-only access proof and commitment.

use std::fs::File;
use std::os::fd::AsRawFd;
use std::os::unix::fs::MetadataExt;

use sha2::{Digest, Sha256};

use crate::publication::namespace::NamespaceError;

use super::COMMITMENT_DOMAIN;

#[cfg(target_vendor = "apple")]
#[path = "apple.rs"]
mod acl;
#[cfg(target_os = "freebsd")]
#[path = "freebsd.rs"]
mod acl;
#[cfg(target_os = "linux")]
#[path = "linux.rs"]
mod acl;

pub(crate) const CREATOR_MODE: u32 = 0o600;

#[derive(Clone, Copy, Debug)]
pub(crate) struct Profile {
    uid: u32,
    commitment: [u8; 32],
}

impl Profile {
    pub(crate) fn capture() -> Result<Self, NamespaceError> {
        let uid = unsafe { libc::geteuid() };
        Ok(Self {
            uid,
            commitment: commitment(uid),
        })
    }

    pub(crate) fn commitment(self) -> [u8; 32] {
        self.commitment
    }
}

pub(crate) fn secure_creator_only(file: &File, profile: &Profile) -> Result<(), NamespaceError> {
    let mode = CREATOR_MODE as libc::mode_t;
    if unsafe { libc::fchmod(file.as_raw_fd(), mode) } != 0 {
        return Err(last_error("apply creator-only mode"));
    }
    acl::remove_inherited(file)?;
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
    acl::require_trivial(file)?;
    let metadata = file.metadata().map_err(NamespaceError::Io)?;
    if !metadata.is_file() || metadata.nlink() != 1 || metadata.mode() & 0o7777 != CREATOR_MODE {
        return Err(NamespaceError::AccessPolicy);
    }
    Ok(metadata)
}

fn commitment(uid: u32) -> [u8; 32] {
    let mut hasher = Sha256::new();
    hasher.update(COMMITMENT_DOMAIN);
    hasher.update(uid.to_le_bytes());
    hasher.update(CREATOR_MODE.to_le_bytes());
    hasher.finalize().into()
}

fn last_error(operation: &'static str) -> NamespaceError {
    NamespaceError::IoAt {
        operation,
        source: std::io::Error::last_os_error(),
    }
}
