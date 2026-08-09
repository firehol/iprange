//! Platform-specific retained namespace operations.

use std::fs::File;
use std::io;
use std::path::Path;

use crate::name_binding::basename_commitment;
use crate::path;
use crate::publication::security;
use crate::validation::LocalFileIdentity;

#[cfg(unix)]
#[path = "namespace/unix.rs"]
mod platform;

#[cfg(windows)]
#[path = "namespace/windows.rs"]
mod platform;

pub(crate) use platform::*;

#[derive(Debug)]
pub(crate) struct Regular {
    pub(crate) file: File,
    pub(crate) identity: Identity,
}

impl Regular {
    pub(crate) fn creator_only_commitment(&self) -> Result<[u8; 32], NamespaceError> {
        security::creator_only_commitment(&self.file)
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct Entry {
    pub(crate) identity: Identity,
    pub(crate) links: u64,
    pub(crate) regular: bool,
}

impl Directory {
    pub(crate) fn verify_name(
        &self,
        name: &Name,
        expected: Identity,
    ) -> Result<(), NamespaceError> {
        let found = self.entry(name)?.ok_or(NamespaceError::Missing)?;
        if !found.regular {
            return Err(NamespaceError::NotRegular);
        }
        if found.identity != expected {
            return Err(NamespaceError::IdentityChanged);
        }
        if found.links != 1 {
            return Err(NamespaceError::LinkCount(found.links));
        }
        Ok(())
    }

    pub(crate) fn require_absent(&self, name: &Name) -> Result<(), NamespaceError> {
        if self.entry(name)?.is_some() {
            return Err(NamespaceError::Exists);
        }
        Ok(())
    }

    pub(super) fn require_name_lengths(&self, names: &[&Name]) -> Result<(), NamespaceError> {
        if names
            .iter()
            .any(|name| name.component_len() > self.name_max)
        {
            return Err(NamespaceError::InvalidName);
        }
        Ok(())
    }

    pub(crate) fn check_creator(&self) -> Result<(), NamespaceError> {
        if std::process::id() != self.creator_pid {
            return Err(NamespaceError::ForkedHandle);
        }
        Ok(())
    }
}

pub(crate) const OUTPUT_PREFIX: &[u8] = b".iprange-publish-";
pub(crate) const RESERVATION_PREFIX: &[u8] = b".iprange-reservation-";
pub(crate) const PRIVATE_SUFFIX: &[u8] = b".tmp";
pub(crate) const ENCODED_ATTEMPT_LEN: usize = 32;

#[derive(Debug)]
pub(crate) struct Destination {
    directory: Directory,
    main: Name,
    coordination: Name,
    basename_commitment: [u8; 32],
    security: security::Profile,
}

impl Destination {
    pub(crate) fn bind(path: &Path) -> Result<Self, NamespaceError> {
        let component = path.file_name().ok_or(NamespaceError::InvalidName)?;
        path::validate_main_name(component).map_err(|_| NamespaceError::InvalidName)?;
        let (main, coordination, encoding) = platform::destination_names(component)?;
        let directory = Directory::open(parent(path))?;
        directory.require_name_lengths(&[&main, &coordination])?;
        let basename_commitment =
            basename_commitment(encoding, main.bytes()).map_err(|_| NamespaceError::InvalidName)?;
        Ok(Self {
            directory,
            main,
            coordination,
            basename_commitment,
            security: security::Profile::capture()?,
        })
    }

    pub(crate) fn directory(&self) -> &Directory {
        &self.directory
    }

    pub(crate) fn main(&self) -> &Name {
        &self.main
    }

    pub(crate) fn coordination(&self) -> &Name {
        &self.coordination
    }

    pub(crate) fn basename_commitment(&self) -> [u8; 32] {
        self.basename_commitment
    }

    pub(crate) fn security_commitment(&self) -> [u8; 32] {
        self.security.commitment()
    }

    pub(crate) fn secure_created(&self, file: &File) -> Result<(), NamespaceError> {
        security::secure_creator_only(file, &self.security)
    }

    pub(crate) fn create(&self, name: &Name) -> Result<File, NamespaceError> {
        self.directory.create(name, &self.security)
    }

    pub(crate) fn verify_created(&self, file: &File) -> Result<(), NamespaceError> {
        if security::creator_only_commitment(file)? != self.security.commitment() {
            return Err(NamespaceError::AccessPolicy);
        }
        Ok(())
    }

    pub(crate) fn require_fail_if_exists_available(&self) -> Result<(), NamespaceError> {
        self.directory.require_absent(&self.main)?;
        self.directory.require_absent(&self.coordination)
    }

    pub(crate) fn output_name(&self, attempt: [u8; 16]) -> Result<Name, NamespaceError> {
        self.attempt_name(OUTPUT_PREFIX, attempt)
    }

    pub(crate) fn reservation_name(&self, attempt: [u8; 16]) -> Result<Name, NamespaceError> {
        self.attempt_name(RESERVATION_PREFIX, attempt)
    }

    fn attempt_name(&self, prefix: &[u8], attempt: [u8; 16]) -> Result<Name, NamespaceError> {
        let name = private_name(prefix, attempt)?;
        self.directory.require_name_lengths(&[&name])?;
        Ok(name)
    }
}

pub(crate) fn private_name(prefix: &[u8], attempt: [u8; 16]) -> Result<Name, NamespaceError> {
    if attempt == [0; 16] {
        return Err(NamespaceError::InvalidName);
    }
    let mut bytes = Vec::with_capacity(prefix.len() + ENCODED_ATTEMPT_LEN + PRIVATE_SUFFIX.len());
    bytes.extend_from_slice(prefix);
    for byte in attempt {
        bytes.push(hex(byte >> 4));
        bytes.push(hex(byte & 0x0f));
    }
    bytes.extend_from_slice(PRIVATE_SUFFIX);
    Name::new(&bytes)
}

pub(crate) fn private_attempt(prefix: &[u8], bytes: &[u8]) -> Option<[u8; 16]> {
    let encoded = bytes.strip_prefix(prefix)?.strip_suffix(PRIVATE_SUFFIX)?;
    if encoded.len() != ENCODED_ATTEMPT_LEN {
        return None;
    }
    let mut attempt = [0; 16];
    for (slot, pair) in attempt.iter_mut().zip(encoded.chunks_exact(2)) {
        *slot = unhex(pair[0])?.checked_mul(16)? + unhex(pair[1])?;
    }
    (attempt != [0; 16]).then_some(attempt)
}

fn parent(path: &Path) -> &Path {
    match path.parent() {
        Some(parent) if !parent.as_os_str().is_empty() => parent,
        _ => Path::new("."),
    }
}

fn hex(value: u8) -> u8 {
    match value {
        0..=9 => b'0' + value,
        _ => b'a' + value - 10,
    }
}

fn unhex(value: u8) -> Option<u8> {
    match value {
        b'0'..=b'9' => Some(value - b'0'),
        b'a'..=b'f' => Some(value - b'a' + 10),
        _ => None,
    }
}

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
