//! Exact local namespace operations for live main and coordination files.

use std::fs::File;
use std::path::Path;

use crate::error::{Error, Result};
use crate::live_cleanup::{self, Authority as CleanupAuthority};
use crate::publication::namespace::{
    local_identity, retained_regular_identity, Directory, Name, NamespaceError,
};
use crate::publication::security::{self, Profile};
#[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
use crate::publication::{ArtifactKind, DirectoryRole};
use crate::validation::LocalFileIdentity;

pub(crate) use crate::publication::namespace::Identity;

pub(crate) fn public_identity(identity: Identity) -> LocalFileIdentity {
    local_identity(identity)
}

pub(crate) fn parent_identity(path: &Path) -> Result<LocalFileIdentity> {
    let parent = path.parent().ok_or(Error::InvalidArgument(
        "database path has no parent directory",
    ))?;
    let directory = Directory::open(parent).map_err(|error| match error {
        NamespaceError::Missing => Error::Io(std::io::Error::from(std::io::ErrorKind::NotFound)),
        other => namespace_error(other),
    })?;
    Ok(local_identity(directory.identity()))
}

pub(crate) struct CreatedPrivate {
    pub(crate) file: File,
    pub(crate) identity: Identity,
}

#[derive(Debug)]
pub(crate) struct PrivateCreationFailure {
    pub(crate) cause: Error,
    pub(crate) cleanup: live_cleanup::Outcome,
    pub(crate) identity: Option<Identity>,
}

impl PrivateCreationFailure {
    #[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
    pub(crate) fn into_error(self) -> Error {
        match self.cleanup.cause {
            Some(cleanup) => Error::CleanupIncomplete {
                cause: Box::new(self.cause),
                cleanup: Box::new(cleanup),
            },
            None => self.cause,
        }
    }
}

pub(crate) fn identity(file: &File) -> Result<Identity> {
    retained_regular_identity(file, true).map_err(namespace_error)
}

pub(crate) fn identity_any_link(file: &File) -> Result<Identity> {
    retained_regular_identity(file, false).map_err(namespace_error)
}

pub(crate) fn verify_path(path: &Path, expected: Identity) -> Result<()> {
    verify_path_inner(path, expected, true)
}

pub(crate) fn verify_path_any_link(path: &Path, expected: Identity) -> Result<()> {
    verify_path_inner(path, expected, false)
}

pub(crate) fn path_identity(path: &Path) -> Result<Option<Identity>> {
    let (directory, name) = match bind_path(path) {
        Ok(bound) => bound,
        Err(Error::NameNotFound) => return Ok(None),
        Err(error) => return Err(error),
    };
    match directory.entry(&name).map_err(namespace_error)? {
        None => Ok(None),
        Some(entry) if entry.regular && entry.links == 1 => Ok(Some(entry.identity)),
        Some(_) => Err(Error::WrongMode("live path is not one regular file")),
    }
}

fn verify_path_inner(path: &Path, expected: Identity, require_single_link: bool) -> Result<()> {
    let (directory, name) = bind_path(path)?;
    let entry = directory
        .entry(&name)
        .map_err(namespace_error)?
        .ok_or(Error::NameNotFound)?;
    if !entry.regular {
        return Err(Error::WrongMode("live path no longer names a regular file"));
    }
    if (require_single_link && entry.links != 1) || entry.identity != expected {
        return Err(Error::WrongMode("live path identity changed"));
    }
    Ok(())
}

pub(crate) fn open_rw(path: &Path) -> Result<File> {
    let (directory, name) = bind_path(path)?;
    let regular = directory
        .open_regular(&name, true)
        .map_err(namespace_error)?
        .ok_or(Error::NameNotFound)?;
    Ok(regular.file)
}

pub(crate) fn create_private(
    path: &Path,
    authority: CleanupAuthority,
) -> core::result::Result<CreatedPrivate, PrivateCreationFailure> {
    let failure = |cause| PrivateCreationFailure {
        cause,
        cleanup: live_cleanup::Outcome::clean(),
        identity: None,
    };
    let (directory, name) = bind_path(path).map_err(failure)?;
    let profile = Profile::capture().map_err(|error| failure(namespace_error(error)))?;
    let file = directory
        .create(&name, &profile)
        .map_err(|error| failure(namespace_error(error)))?;
    let identity = match identity(&file) {
        Ok(identity) => identity,
        Err(cause) => {
            return Err(PrivateCreationFailure {
                cause,
                cleanup: live_cleanup::Outcome::failed(Error::Unresolvable(
                    "created live artifact has no proven local identity",
                )),
                identity: None,
            })
        }
    };
    if let Err(error) = security::secure_creator_only(&file, &profile) {
        return Err(PrivateCreationFailure {
            cause: namespace_error(error),
            cleanup: live_cleanup::remove(path, &file, identity, authority),
            identity: Some(identity),
        });
    }
    Ok(CreatedPrivate { file, identity })
}

#[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
pub(crate) fn create_private_for_test(path: &Path) -> Result<File> {
    create_private(
        path,
        CleanupAuthority {
            attempt_id: crate::random::nonzero_128()?,
            ordinal: 0,
            kind: ArtifactKind::OwnedMain,
            directory_role: DirectoryRole::MainFile,
        },
    )
    .map(|created| created.file)
    .map_err(PrivateCreationFailure::into_error)
}

pub(crate) fn remove_exact(path: &Path, expected: Identity) -> Result<()> {
    let (directory, name) = bind_path(path)?;
    directory
        .verify_name(&name, expected)
        .map_err(namespace_error)?;
    if !directory
        .unlink_exact(&name, expected)
        .map_err(namespace_error)?
    {
        return Err(Error::NameNotFound);
    }
    directory.sync().map_err(namespace_error)?;
    directory.require_absent(&name).map_err(namespace_error)
}

pub(crate) fn install_noreplace(
    private: &Path,
    private_file: &File,
    canonical: &Path,
    expected: Identity,
) -> Result<()> {
    let (directory, private_name, canonical_name) = bind_pair(private, canonical)?;
    directory
        .verify_name(&private_name, expected)
        .map_err(namespace_error)?;
    directory
        .rename_noreplace(&private_name, private_file, &canonical_name)
        .map_err(namespace_error)?;
    directory.sync().map_err(namespace_error)?;
    directory
        .require_absent(&private_name)
        .and_then(|()| directory.verify_name(&canonical_name, expected))
        .map_err(namespace_error)
}

pub(crate) fn install_replace_discarding(
    private: &Path,
    private_file: &File,
    canonical: &Path,
    expected_private: Identity,
    expected_canonical: Identity,
) -> Result<()> {
    let (directory, private_name, canonical_name) = bind_pair(private, canonical)?;
    directory
        .verify_name(&private_name, expected_private)
        .and_then(|()| directory.verify_name(&canonical_name, expected_canonical))
        .map_err(|_| {
            Error::CleanupConflict("canonical coordination changed during discarding reset")
        })?;
    directory
        .replace_discarding_destination(&private_name, private_file, &canonical_name)
        .map_err(namespace_error)?;
    directory
        .require_absent(&private_name)
        .and_then(|()| directory.verify_name(&canonical_name, expected_private))
        .map_err(namespace_error)
}

#[cfg(any(target_os = "linux", target_vendor = "apple"))]
pub(crate) fn install_exchange(
    private: &Path,
    private_file: &File,
    canonical: &Path,
    expected_private: Identity,
    expected_canonical: Identity,
) -> Result<()> {
    let (directory, private_name, canonical_name) = bind_pair(private, canonical)?;
    if directory
        .verify_name(&private_name, expected_private)
        .and_then(|()| directory.verify_name(&canonical_name, expected_canonical))
        .is_err()
    {
        return Err(Error::CleanupConflict(
            "canonical coordination changed during reset",
        ));
    }
    directory
        .exchange(&private_name, private_file, &canonical_name)
        .map_err(namespace_error)?;
    if directory
        .verify_name(&canonical_name, expected_private)
        .is_ok()
        && directory
            .verify_name(&private_name, expected_canonical)
            .is_ok()
    {
        return Ok(());
    }

    let cause = Error::CleanupConflict("canonical coordination changed during reset");
    match directory.exchange(&canonical_name, private_file, &private_name) {
        Ok(()) => Err(cause),
        Err(cleanup) => Err(Error::CleanupIncomplete {
            cause: Box::new(cause),
            cleanup: Box::new(namespace_error(cleanup)),
        }),
    }
}

pub(crate) fn sync_parent(path: &Path) -> Result<()> {
    let parent = path.parent().ok_or(Error::InvalidArgument(
        "database path has no parent directory",
    ))?;
    Directory::open(parent)
        .and_then(|directory| directory.sync())
        .map_err(namespace_error)
}

pub(crate) fn bind_path(path: &Path) -> Result<(Directory, Name)> {
    let parent = path.parent().ok_or(Error::InvalidArgument(
        "database path has no parent directory",
    ))?;
    let component = path
        .file_name()
        .ok_or(Error::InvalidArgument("database path has no file name"))?;
    let directory = Directory::open(parent).map_err(namespace_error)?;
    let name = Name::from_component(component).map_err(namespace_error)?;
    Ok((directory, name))
}

fn bind_pair(private: &Path, canonical: &Path) -> Result<(Directory, Name, Name)> {
    if private.parent() != canonical.parent() {
        return Err(Error::InvalidArgument(
            "live transition names must share one directory",
        ));
    }
    let (directory, private_name) = bind_path(private)?;
    let canonical_name = canonical
        .file_name()
        .ok_or(Error::InvalidArgument("database path has no file name"))
        .and_then(|component| Name::from_component(component).map_err(namespace_error))?;
    Ok((directory, private_name, canonical_name))
}

fn namespace_error(error: NamespaceError) -> Error {
    match error {
        NamespaceError::InvalidName => Error::NameInvalid,
        NamespaceError::Exists => Error::NameExists,
        NamespaceError::Missing => Error::NameNotFound,
        NamespaceError::ForkedHandle => Error::ForkedHandle,
        NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => Error::Io(source),
        NamespaceError::Unsupported | NamespaceError::CrossFilesystem => {
            Error::DurabilityUnsupported("live file namespace lacks required local operations")
        }
        NamespaceError::NotDirectory
        | NamespaceError::NotRegular
        | NamespaceError::IdentityChanged
        | NamespaceError::LinkCount(_)
        | NamespaceError::AccessPolicy => Error::WrongMode("live file ownership changed"),
    }
}
