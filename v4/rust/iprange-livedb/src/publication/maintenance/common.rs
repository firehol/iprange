//! Shared ownership rules for private publication artifacts.

use std::fs::File;
use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::live_lock::{self, Mode};
#[cfg(windows)]
use crate::publication::gc_codec::Payload;
use crate::publication::namespace::{
    identity_from_local, Directory, Entry, Identity, Name, NamespaceError, Regular, ScanError,
    IDENTITY_KIND,
};
use crate::publication::ArtifactKind;
use crate::publication::{
    AbandonedArtifactRemoval, CleanupState, Housekeeping, HousekeepingArtifact, PublicationProblem,
};
use crate::validation::LocalFileIdentity;

const SUFFIX: &[u8] = b".tmp";
const ENCODED_ID_LEN: usize = 32;

#[cfg(windows)]
type RetirementPayload = Option<Payload>;
#[cfg(not(windows))]
type RetirementPayload = ();

pub(super) struct Artifact {
    prefix: &'static [u8],
    invalid_name: &'static str,
    unsupported_identity: &'static str,
    invalid_identity: &'static str,
    ownership_mismatch: &'static str,
    ownership_changed: &'static str,
    lost_name: &'static str,
    #[cfg(unix)]
    remained_linked: &'static str,
}

impl Artifact {
    #[allow(clippy::too_many_arguments)]
    pub(super) const fn new(
        prefix: &'static [u8],
        invalid_name: &'static str,
        unsupported_identity: &'static str,
        invalid_identity: &'static str,
        ownership_mismatch: &'static str,
        ownership_changed: &'static str,
        lost_name: &'static str,
        remained_linked: &'static str,
    ) -> Self {
        #[cfg(not(unix))]
        let _ = remained_linked;
        Self {
            prefix,
            invalid_name,
            unsupported_identity,
            invalid_identity,
            ownership_mismatch,
            ownership_changed,
            lost_name,
            #[cfg(unix)]
            remained_linked,
        }
    }

    pub(super) fn encode_name(&self, attempt: [u8; 16]) -> Result<Name> {
        if attempt == [0; 16] {
            return Err(Error::InvalidArgument(
                "publication attempt id must be nonzero",
            ));
        }
        let mut bytes = Vec::with_capacity(self.prefix.len() + ENCODED_ID_LEN + SUFFIX.len());
        bytes.extend_from_slice(self.prefix);
        for byte in attempt {
            bytes.push(hex(byte >> 4));
            bytes.push(hex(byte & 0x0f));
        }
        bytes.extend_from_slice(SUFFIX);
        Name::new(&bytes).map_err(|error| self.namespace_error(error))
    }

    pub(super) fn decode_name(&self, bytes: &[u8]) -> Option<[u8; 16]> {
        let encoded = bytes.strip_prefix(self.prefix)?.strip_suffix(SUFFIX)?;
        if encoded.len() != ENCODED_ID_LEN {
            return None;
        }
        let mut attempt = [0; 16];
        for (slot, pair) in attempt.iter_mut().zip(encoded.chunks_exact(2)) {
            *slot = unhex(pair[0])?.checked_mul(16)? + unhex(pair[1])?;
        }
        (attempt != [0; 16]).then_some(attempt)
    }

    pub(super) fn scan(
        &self,
        path: &Path,
        cancellation: &CancellationToken,
        overflow: &'static str,
        mut visit: impl FnMut(&Directory, LocalFileIdentity, &[u8], [u8; 16]) -> Result<bool>,
    ) -> Result<(LocalFileIdentity, u64)> {
        cancellation.check()?;
        let directory = Directory::open(path).map_err(|error| self.namespace_error(error))?;
        let directory_identity =
            crate::publication::namespace::local_identity(directory.identity());
        let mut entries = 0u64;
        let scan = directory.scan(|bytes| {
            cancellation.check()?;
            let Some(attempt) = self.decode_name(bytes) else {
                return Ok(());
            };
            if visit(&directory, directory_identity, bytes, attempt)? {
                entries = entries
                    .checked_add(1)
                    .ok_or(Error::ArithmeticOverflow(overflow))?;
            }
            Ok(())
        });
        match scan {
            Ok(()) => Ok((directory_identity, entries)),
            Err(ScanError::Namespace(error)) => Err(self.namespace_error(error)),
            Err(ScanError::Visitor(error)) => Err(error),
        }
    }

    pub(super) fn inspect_stable<T>(
        &self,
        directory: &Directory,
        bytes: &[u8],
        inspect: impl FnOnce(&File, Identity) -> Result<T>,
    ) -> Result<Option<(Identity, T)>> {
        let name = Name::new(bytes).map_err(|error| self.namespace_error(error))?;
        let Some(found) = directory
            .entry(&name)
            .map_err(|error| self.namespace_error(error))?
        else {
            return Ok(None);
        };
        if !found.regular || found.links != 1 {
            return Ok(None);
        }
        let Some(regular) = directory
            .open_regular(&name, false)
            .map_err(|error| self.namespace_error(error))?
        else {
            return Ok(None);
        };
        let value = inspect(&regular.file, regular.identity)?;
        let Some(current) = directory
            .entry(&name)
            .map_err(|error| self.namespace_error(error))?
        else {
            return Ok(None);
        };
        if !current.regular || current.links != 1 || current.identity != regular.identity {
            return Ok(None);
        }
        Ok(Some((regular.identity, value)))
    }

    fn identity(&self, value: LocalFileIdentity) -> Result<Identity> {
        if value.kind != IDENTITY_KIND {
            return Err(Error::InvalidArgument(self.unsupported_identity));
        }
        identity_from_local(value).ok_or(Error::InvalidArgument(self.invalid_identity))
    }

    fn require_owned(
        &self,
        regular: bool,
        links: u64,
        found: Identity,
        expected: Identity,
    ) -> Result<()> {
        if !regular || links != 1 || found != expected {
            return Err(Error::CleanupConflict(self.ownership_mismatch));
        }
        Ok(())
    }

    fn open_owned(
        &self,
        directory: &Directory,
        name: &Name,
        found: Option<Entry>,
        expected: Identity,
        lock_offset: u64,
        cancellation: &CancellationToken,
    ) -> Result<Option<Regular>> {
        let Some(found) = found else {
            self.durable_absence(directory, name)?;
            return Ok(None);
        };
        self.require_owned(found.regular, found.links, found.identity, expected)?;
        let regular = directory
            .open_regular(name, true)
            .map_err(|error| self.namespace_error(error))?
            .ok_or(Error::CleanupConflict(self.lost_name))?;
        live_lock::lock_file_cancellable(
            &regular.file,
            lock_offset,
            Mode::Exclusive,
            cancellation,
        )?;
        directory
            .verify_name(name, expected)
            .map_err(|error| self.cleanup_error(error))?;
        Ok(Some(regular))
    }

    #[allow(clippy::too_many_arguments)]
    pub(super) fn remove(
        &self,
        path: &Path,
        expected_directory: LocalFileIdentity,
        attempt: [u8; 16],
        expected_artifact: LocalFileIdentity,
        lock_offset: u64,
        cancellation: &CancellationToken,
        ordinal: u32,
        kind: ArtifactKind,
        payload: RetirementPayload,
        verify_content: impl FnOnce(&File, Identity) -> Result<()>,
    ) -> Result<AbandonedArtifactRemoval> {
        let expected_directory = self.identity(expected_directory)?;
        let expected_artifact = self.identity(expected_artifact)?;
        let directory = Directory::open(path).map_err(|error| self.namespace_error(error))?;
        require_directory(&directory, expected_directory)?;
        let name = self.encode_name(attempt)?;
        let found = directory
            .entry(&name)
            .map_err(|error| self.namespace_error(error))?;
        #[cfg(windows)]
        if let Some(result) = self.resume_windows(
            &directory,
            attempt,
            &name,
            expected_artifact,
            ordinal,
            kind,
            payload,
            found.is_some(),
        ) {
            return Ok(result);
        }
        let Some(regular) = self.open_owned(
            &directory,
            &name,
            found,
            expected_artifact,
            lock_offset,
            cancellation,
        )?
        else {
            return Ok(removal(false, None, Housekeeping::None, Box::default()));
        };
        verify_content(&regular.file, expected_artifact)?;
        cancellation.check()?;
        #[cfg(unix)]
        {
            let _ = (attempt, ordinal, kind, payload);
            self.retire_unix(&directory, &name, regular, expected_artifact)
        }
        #[cfg(windows)]
        {
            self.retire_windows(
                &directory,
                &name,
                regular,
                attempt,
                expected_artifact,
                ordinal,
                kind,
                payload,
            )
        }
    }

    fn durable_absence(&self, directory: &Directory, name: &Name) -> Result<()> {
        directory
            .sync()
            .map_err(|error| self.namespace_error(error))?;
        directory
            .verify()
            .map_err(|error| self.namespace_error(error))?;
        directory
            .require_absent(name)
            .map_err(|error| self.cleanup_error(error))
    }

    pub(super) fn namespace_error(&self, error: NamespaceError) -> Error {
        match error {
            NamespaceError::InvalidName => Error::InvalidArgument(self.invalid_name),
            NamespaceError::ForkedHandle => Error::ForkedHandle,
            NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => Error::Io(source),
            NamespaceError::Unsupported | NamespaceError::CrossFilesystem => {
                Error::Unsupported("publication directory lacks required local operations")
            }
            _ => Error::CleanupConflict("publication directory entry changed"),
        }
    }

    fn cleanup_error(&self, error: NamespaceError) -> Error {
        match error {
            NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => Error::Io(source),
            NamespaceError::ForkedHandle => Error::ForkedHandle,
            _ => Error::CleanupConflict(self.ownership_changed),
        }
    }

    #[cfg(unix)]
    fn retire_unix(
        &self,
        directory: &Directory,
        name: &Name,
        regular: Regular,
        expected: Identity,
    ) -> Result<AbandonedArtifactRemoval> {
        use crate::publication::namespace::regular_link_count;
        use crate::publication::problem::Problem;

        if !directory
            .unlink_exact(name, expected)
            .map_err(|error| self.cleanup_error(error))?
        {
            return Err(Error::CleanupConflict(self.lost_name));
        }
        let problem = match regular_link_count(&regular.file) {
            Ok(0) => None,
            Ok(_) => Some(Problem::cleanup_conflict(self.remained_linked)),
            Err(error) => Some(Problem::namespace(&error)),
        };
        if let Some(problem) = problem {
            return Ok(removal(
                true,
                Some(problem),
                Housekeeping::None,
                Box::default(),
            ));
        }
        if let Err(error) = self.durable_absence(directory, name) {
            return Ok(removal(
                true,
                Some(Problem::sdk(&error)),
                Housekeeping::None,
                Box::default(),
            ));
        }
        Ok(removal(true, None, Housekeeping::None, Box::default()))
    }

    #[cfg(windows)]
    #[allow(clippy::too_many_arguments)]
    fn retire_windows(
        &self,
        directory: &Directory,
        name: &Name,
        regular: Regular,
        attempt: [u8; 16],
        expected: Identity,
        ordinal: u32,
        kind: crate::publication::ArtifactKind,
        payload: Option<crate::publication::gc_codec::Payload>,
    ) -> Result<AbandonedArtifactRemoval> {
        use crate::publication::gc::{self, Authority};
        use crate::publication::namespace::CREATION_SECURITY_KIND;
        use crate::publication::security;
        use crate::publication::{CreationSecurity, DirectoryRole};

        let commitment = security::creator_only_commitment(&regular.file)
            .map_err(|error| self.namespace_error(error))?;
        let retired = gc::retire(
            directory,
            Authority {
                attempt_id: attempt,
                ordinal,
                kind,
                directory_role: DirectoryRole::Destination,
                source_name: name,
                source_file: &regular.file,
                identity: expected,
                creation_security: CreationSecurity {
                    kind: CREATION_SECURITY_KIND,
                    commitment,
                },
                payload,
            },
        );
        Ok(retirement(true, retired))
    }

    #[cfg(windows)]
    #[allow(clippy::too_many_arguments)]
    fn resume_windows(
        &self,
        directory: &Directory,
        attempt: [u8; 16],
        name: &Name,
        identity: Identity,
        ordinal: u32,
        kind: crate::publication::ArtifactKind,
        payload: Option<crate::publication::gc_codec::Payload>,
        source_present: bool,
    ) -> Option<AbandonedArtifactRemoval> {
        use crate::publication::gc::{self, ResumeAuthority};
        use crate::publication::DirectoryRole;

        match gc::resume(
            directory,
            ResumeAuthority {
                attempt_id: attempt,
                ordinal,
                kind,
                directory_role: DirectoryRole::Destination,
                source_name: name,
                identity,
                payload,
            },
        ) {
            Ok(Some(retired)) => Some(retirement(source_present, retired)),
            Ok(None) => None,
            Err(problem) => Some(removal(
                source_present,
                Some(problem),
                Housekeeping::None,
                Box::default(),
            )),
        }
    }
}

fn require_directory(directory: &Directory, expected: Identity) -> Result<()> {
    if directory.identity() != expected {
        return Err(Error::DirectoryIdentityMismatch);
    }
    Ok(())
}

fn removal(
    source_present: bool,
    cause: Option<PublicationProblem>,
    housekeeping: Housekeeping,
    visible_housekeeping: Box<[HousekeepingArtifact]>,
) -> AbandonedArtifactRemoval {
    AbandonedArtifactRemoval {
        source_present,
        cleanup_state: if cause.is_some() {
            CleanupState::ResiduePossible
        } else {
            CleanupState::Clean
        },
        housekeeping,
        visible_housekeeping,
        cause,
    }
}

#[cfg(windows)]
fn retirement(
    source_present: bool,
    retired: crate::publication::gc::Retirement,
) -> AbandonedArtifactRemoval {
    removal(
        source_present,
        retired.problem,
        retired.housekeeping,
        retired
            .visible
            .into_iter()
            .collect::<Vec<_>>()
            .into_boxed_slice(),
    )
}

fn hex(value: u8) -> u8 {
    if value < 10 {
        b'0' + value
    } else {
        b'a' + value - 10
    }
}

fn unhex(value: u8) -> Option<u8> {
    match value {
        b'0'..=b'9' => Some(value - b'0'),
        b'a'..=b'f' => Some(value - b'a' + 10),
        _ => None,
    }
}
