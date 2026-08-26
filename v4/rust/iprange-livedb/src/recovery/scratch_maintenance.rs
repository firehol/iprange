//! Offline discovery and exact removal of abandoned recovery scratch.

use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::error::Result;
use crate::publication::AbandonedArtifactRemoval;
use crate::validation::LocalFileIdentity;

/// Operation which created an authenticated scratch artifact.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u16)]
pub enum ScratchOwnerKind {
    Validation = 1,
    Recovery = 2,
}

/// Whether one exact-pattern entry has an authoritative ownership header.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AbandonedScratchAuthentication {
    Authenticated(ScratchOwnerKind),
    Unauthenticated,
}

/// One exact-pattern scratch-directory entry.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AbandonedScratchEntry {
    pub directory_identity: LocalFileIdentity,
    pub artifact_identity: LocalFileIdentity,
    pub attempt_id: [u8; 16],
    pub ordinal: u32,
    pub authentication: AbandonedScratchAuthentication,
}

/// Completed constant-memory directory scan.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AbandonedScratchList {
    pub directory_identity: LocalFileIdentity,
    pub entries: u64,
}

/// Sink response for one borrowed abandoned-scratch entry.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AbandonedScratchSinkControl {
    Continue,
    Stop,
}

/// Synchronous abandoned-scratch entry consumer.
pub trait AbandonedScratchSink {
    fn entry(&mut self, entry: &AbandonedScratchEntry) -> Result<AbandonedScratchSinkControl>;
}

impl<F> AbandonedScratchSink for F
where
    F: FnMut(&AbandonedScratchEntry) -> Result<AbandonedScratchSinkControl>,
{
    fn entry(&mut self, entry: &AbandonedScratchEntry) -> Result<AbandonedScratchSinkControl> {
        self(entry)
    }
}

/// List exact scratch-pattern names without following their final component.
pub fn list_abandoned_scratch<S: AbandonedScratchSink>(
    directory: impl AsRef<Path>,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> Result<AbandonedScratchList> {
    #[cfg(any(unix, windows))]
    {
        platform::list(directory.as_ref(), cancellation, sink)
    }
    #[cfg(not(any(unix, windows)))]
    {
        let _ = (directory, cancellation, sink);
        Err(crate::error::Error::Unsupported(
            "abandoned scratch listing is not implemented on this platform",
        ))
    }
}

/// Remove one authenticated artifact after the caller certifies quiescence.
///
pub fn remove_abandoned_scratch(
    directory: impl AsRef<Path>,
    expected_directory_identity: LocalFileIdentity,
    attempt_id: [u8; 16],
    ordinal: u32,
    expected_artifact_identity: LocalFileIdentity,
    cancellation: &CancellationToken,
) -> Result<AbandonedArtifactRemoval> {
    #[cfg(any(unix, windows))]
    {
        platform::remove(
            directory.as_ref(),
            expected_directory_identity,
            attempt_id,
            ordinal,
            expected_artifact_identity,
            cancellation,
        )
    }
    #[cfg(not(any(unix, windows)))]
    {
        let _ = (
            directory,
            expected_directory_identity,
            attempt_id,
            ordinal,
            expected_artifact_identity,
            cancellation,
        );
        Err(crate::error::Error::Unsupported(
            "abandoned scratch removal is not implemented on this platform",
        ))
    }
}

pub(crate) fn remove_checkpointed_scratch(
    directory: &Path,
    expected_directory_identity: LocalFileIdentity,
    attempt_id: [u8; 16],
    ordinal: u32,
    expected_artifact_identity: LocalFileIdentity,
    creation_security: crate::publication::CreationSecurity,
) -> Result<crate::publication::AbandonedArtifactRemoval> {
    #[cfg(any(unix, windows))]
    {
        platform::remove_checkpointed(
            directory,
            expected_directory_identity,
            attempt_id,
            ordinal,
            expected_artifact_identity,
            creation_security,
        )
    }
    #[cfg(not(any(unix, windows)))]
    {
        let _ = (
            directory,
            expected_directory_identity,
            attempt_id,
            ordinal,
            expected_artifact_identity,
            creation_security,
        );
        Err(crate::Error::Unsupported(
            "checkpointed scratch cleanup is not implemented on this platform",
        ))
    }
}

#[cfg(any(unix, windows))]
#[path = "scratch_maintenance/support.rs"]
mod support;

#[cfg(any(unix, windows))]
mod platform {
    use std::fs::File;
    use std::path::Path;

    use crate::cancellation::CancellationToken;
    use crate::error::{Error, Result};
    #[cfg(unix)]
    use crate::publication::namespace::regular_link_count;
    use crate::publication::namespace::{Directory, Identity, Name, ScanError};
    use crate::publication::{AbandonedArtifactRemoval, Housekeeping};
    #[cfg(windows)]
    use crate::publication::{ArtifactKind, CreationSecurity, DirectoryRole};
    use crate::validation::LocalFileIdentity;

    use super::support::{
        authenticate, cleanup_error, deliver, durable_absence, entry, identity, namespace_error,
        removal, require_directory, require_header, require_owned,
    };
    use super::{
        AbandonedScratchAuthentication, AbandonedScratchEntry, AbandonedScratchList,
        AbandonedScratchSink,
    };
    use crate::recovery::scratch::format::{decode_name, scratch_name, DecodedHeader};
    use crate::recovery::scratch::local;

    pub(super) fn list<S: AbandonedScratchSink>(
        path: &Path,
        cancellation: &CancellationToken,
        sink: &mut S,
    ) -> Result<AbandonedScratchList> {
        cancellation.check()?;
        let directory = Directory::open(path).map_err(namespace_error)?;
        let directory_identity = local(directory.identity());
        let mut entries = 0u64;
        let scan = directory.scan(|bytes| {
            visit(
                &directory,
                directory_identity,
                cancellation,
                sink,
                &mut entries,
                bytes,
            )
        });
        match scan {
            Ok(()) => Ok(AbandonedScratchList {
                directory_identity,
                entries,
            }),
            Err(ScanError::Namespace(error)) => Err(namespace_error(error)),
            Err(ScanError::Visitor(error)) => Err(error),
        }
    }

    fn visit<S: AbandonedScratchSink>(
        directory: &Directory,
        directory_identity: LocalFileIdentity,
        cancellation: &CancellationToken,
        sink: &mut S,
        entries: &mut u64,
        bytes: &[u8],
    ) -> Result<()> {
        cancellation.check()?;
        let Some(parsed) = decode_name(bytes) else {
            return Ok(());
        };
        let Some(entry) = inspect(directory, bytes, parsed, directory_identity)? else {
            return Ok(());
        };
        cancellation.check()?;
        deliver(sink, &entry)?;
        cancellation.check()?;
        *entries = entries
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("abandoned scratch entries"))?;
        Ok(())
    }

    pub(super) fn remove(
        path: &Path,
        expected_directory: LocalFileIdentity,
        attempt: [u8; 16],
        ordinal: u32,
        expected_artifact: LocalFileIdentity,
        cancellation: &CancellationToken,
    ) -> Result<AbandonedArtifactRemoval> {
        cancellation.check()?;
        let expected_directory = identity(expected_directory)?;
        let expected_artifact = identity(expected_artifact)?;
        let directory = Directory::open(path).map_err(namespace_error)?;
        require_directory(&directory, expected_directory)?;
        let name = scratch_name(attempt, ordinal)?;
        Removal {
            directory,
            name,
            expected_artifact,
            attempt,
            ordinal,
        }
        .run(cancellation)
    }

    pub(super) fn remove_checkpointed(
        path: &Path,
        expected_directory: LocalFileIdentity,
        attempt: [u8; 16],
        ordinal: u32,
        expected_artifact: LocalFileIdentity,
        creation_security: crate::publication::CreationSecurity,
    ) -> Result<AbandonedArtifactRemoval> {
        let expected_directory = identity(expected_directory)?;
        let expected_artifact = identity(expected_artifact)?;
        let directory = Directory::open(path).map_err(namespace_error)?;
        require_directory(&directory, expected_directory)?;
        let name = scratch_name(attempt, ordinal)?;
        Removal {
            directory,
            name,
            expected_artifact,
            attempt,
            ordinal,
        }
        .run_checkpointed(creation_security)
    }

    fn inspect(
        directory: &Directory,
        bytes: &[u8],
        parsed: ([u8; 16], u32),
        directory_identity: LocalFileIdentity,
    ) -> Result<Option<AbandonedScratchEntry>> {
        let name = Name::new(bytes).map_err(namespace_error)?;
        let Some(found) = directory.entry(&name).map_err(namespace_error)? else {
            return Ok(None);
        };
        if !found.regular || found.links != 1 {
            return Ok(Some(entry(
                directory_identity,
                local(found.identity),
                parsed,
                AbandonedScratchAuthentication::Unauthenticated,
            )));
        }
        inspect_regular(directory, name, parsed, directory_identity)
    }

    fn inspect_regular(
        directory: &Directory,
        name: Name,
        parsed: ([u8; 16], u32),
        directory_identity: LocalFileIdentity,
    ) -> Result<Option<AbandonedScratchEntry>> {
        let Some(regular) = directory
            .open_regular(&name, false)
            .map_err(namespace_error)?
        else {
            return Ok(None);
        };
        let authentication = authenticate(&regular.file, parsed)?;
        let Some(current) = directory.entry(&name).map_err(namespace_error)? else {
            return Ok(None);
        };
        if !current.regular || current.links != 1 || current.identity != regular.identity {
            return Ok(Some(entry(
                directory_identity,
                local(current.identity),
                parsed,
                AbandonedScratchAuthentication::Unauthenticated,
            )));
        }
        Ok(Some(entry(
            directory_identity,
            local(regular.identity),
            parsed,
            authentication,
        )))
    }

    struct Removal {
        directory: Directory,
        name: Name,
        expected_artifact: Identity,
        attempt: [u8; 16],
        ordinal: u32,
    }

    impl Removal {
        fn run(self, cancellation: &CancellationToken) -> Result<AbandonedArtifactRemoval> {
            let present = self.present()?;
            #[cfg(windows)]
            if let Some(result) = self.resume(present) {
                return Ok(result);
            }
            if !present {
                durable_absence(&self.directory, &self.name)?;
                return Ok(removal(false, None, Housekeeping::None, Box::default()));
            }
            let (file, header) = self.open_exact()?;
            self.retire(file, header, cancellation)
        }

        fn run_checkpointed(
            self,
            creation_security: crate::publication::CreationSecurity,
        ) -> Result<AbandonedArtifactRemoval> {
            let present = self.present()?;
            #[cfg(windows)]
            if let Some(result) = self.resume(present) {
                return Ok(result);
            }
            if !present {
                durable_absence(&self.directory, &self.name)?;
                return Ok(removal(false, None, Housekeeping::None, Box::default()));
            }
            let file = self.open_exact_checkpointed()?;
            self.retire_checkpointed(
                file,
                creation_security,
                "checkpointed scratch lost its exact name",
            )
        }

        fn present(&self) -> Result<bool> {
            let Some(found) = self.directory.entry(&self.name).map_err(namespace_error)? else {
                return Ok(false);
            };
            require_owned(
                found.regular,
                found.links,
                found.identity,
                self.expected_artifact,
            )?;
            Ok(true)
        }

        fn open_exact(&self) -> Result<(File, DecodedHeader)> {
            let regular = self
                .directory
                .open_regular(&self.name, false)
                .map_err(namespace_error)?
                .ok_or(Error::CleanupConflict(
                    "abandoned scratch lost its exact name",
                ))?;
            require_owned(true, 1, regular.identity, self.expected_artifact)?;
            let header = require_header(&regular.file, self.attempt, self.ordinal)?;
            self.directory
                .verify_name(&self.name, self.expected_artifact)
                .map_err(cleanup_error)?;
            Ok((regular.file, header))
        }

        fn open_exact_checkpointed(&self) -> Result<File> {
            let regular = self
                .directory
                .open_regular(&self.name, false)
                .map_err(namespace_error)?
                .ok_or(Error::CleanupConflict(
                    "checkpointed scratch lost its exact name",
                ))?;
            require_owned(true, 1, regular.identity, self.expected_artifact)?;
            self.directory
                .verify_name(&self.name, self.expected_artifact)
                .map_err(cleanup_error)?;
            Ok(regular.file)
        }

        #[cfg(windows)]
        fn resume(&self, source_present: bool) -> Option<AbandonedArtifactRemoval> {
            use crate::publication::gc::{self, ResumeAuthority};

            match gc::resume(
                &self.directory,
                ResumeAuthority {
                    attempt_id: self.attempt,
                    ordinal: self.ordinal,
                    kind: ArtifactKind::AuthorizedScratch,
                    directory_role: DirectoryRole::ScratchDirectory,
                    source_name: &self.name,
                    identity: self.expected_artifact,
                    payload: None,
                },
            ) {
                Ok(Some(retired)) => Some(removal(
                    source_present,
                    retired.problem,
                    retired.housekeeping,
                    retired
                        .visible
                        .into_iter()
                        .collect::<Vec<_>>()
                        .into_boxed_slice(),
                )),
                Ok(None) => None,
                Err(problem) => Some(removal(
                    source_present,
                    Some(problem),
                    Housekeeping::None,
                    Box::default(),
                )),
            }
        }

        #[cfg(unix)]
        fn retire(
            &self,
            file: File,
            _header: DecodedHeader,
            cancellation: &CancellationToken,
        ) -> Result<AbandonedArtifactRemoval> {
            cancellation.check()?;
            self.retire_checkpointed(
                file,
                crate::publication::CreationSecurity {
                    kind: 0,
                    commitment: [0; 32],
                },
                "abandoned scratch lost its exact name",
            )
        }

        #[cfg(unix)]
        fn retire_checkpointed(
            &self,
            file: File,
            _creation_security: crate::publication::CreationSecurity,
            _lost_detail: &'static str,
        ) -> Result<AbandonedArtifactRemoval> {
            let removed = self
                .directory
                .unlink_exact(&self.name, self.expected_artifact)
                .map_err(cleanup_error)?;
            if !removed {
                return Err(Error::CleanupConflict(
                    "abandoned scratch lost its exact name",
                ));
            }
            match regular_link_count(&file) {
                Ok(0) => {}
                Ok(_) => {
                    return Ok(removal(
                        true,
                        Some(crate::publication::problem::Problem::cleanup_conflict(
                            "abandoned scratch remained linked after removal",
                        )),
                        Housekeeping::None,
                        Box::default(),
                    ))
                }
                Err(error) => {
                    return Ok(removal(
                        true,
                        Some(crate::publication::problem::Problem::namespace(&error)),
                        Housekeeping::None,
                        Box::default(),
                    ))
                }
            }
            if let Err(error) = durable_absence(&self.directory, &self.name) {
                return Ok(removal(
                    true,
                    Some(crate::publication::problem::Problem::sdk(&error)),
                    Housekeeping::None,
                    Box::default(),
                ));
            }
            Ok(removal(true, None, Housekeeping::None, Box::default()))
        }

        #[cfg(windows)]
        fn retire(
            &self,
            file: File,
            header: DecodedHeader,
            cancellation: &CancellationToken,
        ) -> Result<AbandonedArtifactRemoval> {
            cancellation.check()?;
            self.retire_checkpointed(
                file,
                CreationSecurity {
                    kind: header.creation_security_kind,
                    commitment: header.creation_security_commitment,
                },
                "abandoned scratch lost its exact name",
            )
        }

        /// Re-open one probe-authenticated artifact with the writable
        /// access of the `open_regular(name, true)` arm and prove it
        /// exactly like `open_exact_checkpointed`, then drop the
        /// read-only probe handle so its share mode cannot block the
        /// GC rename. The GC move renames the payload through the
        /// retained source handle and flushes it (FlushFileBuffers),
        /// so the retired handle must be writable and delete-capable;
        /// the Go arm follows the same shape. `lost_detail` names the
        /// missing-artifact conflict per flow ("checkpointed scratch
        /// lost its exact name" for the checkpointed retirement,
        /// "abandoned scratch lost its exact name" for the abandoned
        /// one), exactly like the Go arm's parameter.
        #[cfg(windows)]
        fn open_writable(&self, probe: File, lost_detail: &'static str) -> Result<File> {
            let regular = self
                .directory
                .open_regular(&self.name, true)
                .map_err(namespace_error)?
                .ok_or(Error::CleanupConflict(lost_detail))?;
            require_owned(true, 1, regular.identity, self.expected_artifact)?;
            self.directory
                .verify_name(&self.name, self.expected_artifact)
                .map_err(cleanup_error)?;
            drop(probe);
            Ok(regular.file)
        }

        #[cfg(windows)]
        fn retire_checkpointed(
            &self,
            file: File,
            creation_security: CreationSecurity,
            lost_detail: &'static str,
        ) -> Result<AbandonedArtifactRemoval> {
            use crate::publication::gc::{self, Authority};

            let writable = self.open_writable(file, lost_detail)?;
            let retired = gc::retire(
                &self.directory,
                Authority {
                    attempt_id: self.attempt,
                    ordinal: self.ordinal,
                    kind: ArtifactKind::AuthorizedScratch,
                    directory_role: DirectoryRole::ScratchDirectory,
                    source_name: &self.name,
                    source_file: &writable,
                    identity: self.expected_artifact,
                    creation_security,
                    payload: None,
                },
            );
            Ok(removal(
                true,
                retired.problem,
                retired.housekeeping,
                retired
                    .visible
                    .into_iter()
                    .collect::<Vec<_>>()
                    .into_boxed_slice(),
            ))
        }
    }
}

#[cfg(all(test, target_os = "linux"))]
#[path = "scratch_maintenance_tests.rs"]
mod tests;
