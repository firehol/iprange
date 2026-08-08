//! Exact cleanup facts for live-lifecycle artifacts.

use std::fs::File;
use std::path::Path;

use crate::error::Error;
use crate::live_namespace::Identity;
use crate::publication::{ArtifactKind, DirectoryRole, Housekeeping, HousekeepingArtifact};

#[derive(Debug)]
pub(crate) struct Outcome {
    pub(crate) cause: Option<Error>,
    pub(crate) housekeeping: Housekeeping,
    pub(crate) visible: Vec<HousekeepingArtifact>,
}

pub(crate) struct TerminalFacts {
    pub(crate) residue_possible: bool,
    pub(crate) housekeeping: Housekeeping,
    pub(crate) visible_housekeeping: Box<[HousekeepingArtifact]>,
    pub(crate) cause: Option<Error>,
}

impl TerminalFacts {
    pub(crate) fn clean() -> Self {
        Self {
            residue_possible: false,
            housekeeping: Housekeeping::None,
            visible_housekeeping: Box::default(),
            cause: None,
        }
    }

    pub(crate) fn cause(cause: Error) -> Self {
        Self {
            cause: Some(cause),
            ..Self::clean()
        }
    }

    pub(crate) fn residue(cause: Error) -> Self {
        Self {
            residue_possible: true,
            cause: Some(cause),
            ..Self::clean()
        }
    }

    pub(crate) fn failed(cause: Error, cleanup: Outcome) -> Self {
        let residue_possible = cleanup.cause.is_some();
        let cause = match cleanup.cause {
            None => cause,
            Some(cleanup) => Error::CleanupIncomplete {
                cause: Box::new(cause),
                cleanup: Box::new(cleanup),
            },
        };
        Self {
            residue_possible,
            housekeeping: cleanup.housekeeping,
            visible_housekeeping: cleanup.visible.into_boxed_slice(),
            cause: Some(cause),
        }
    }
}

impl Outcome {
    pub(crate) const fn clean() -> Self {
        Self {
            cause: None,
            housekeeping: Housekeeping::None,
            visible: Vec::new(),
        }
    }

    pub(crate) fn failed(cause: Error) -> Self {
        Self {
            cause: Some(cause),
            ..Self::clean()
        }
    }

    pub(crate) fn is_clean(&self) -> bool {
        self.cause.is_none()
    }

    pub(crate) fn absorb(&mut self, mut other: Self) {
        if self.cause.is_none() {
            self.cause = other.cause.take();
        }
        self.housekeeping = merge_housekeeping(self.housekeeping, other.housekeeping);
        self.visible.append(&mut other.visible);
    }
}

#[derive(Clone, Copy)]
pub(crate) struct Authority {
    pub(crate) attempt_id: [u8; 16],
    pub(crate) ordinal: u32,
    pub(crate) kind: ArtifactKind,
    pub(crate) directory_role: DirectoryRole,
}

pub(crate) fn unique_attempt_id(source: &Path, ordinal: u32) -> crate::error::Result<[u8; 16]> {
    #[cfg(windows)]
    {
        use crate::publication::gc_name;
        use crate::publication::namespace::NamespaceError;

        let (directory, _) = crate::live_namespace::bind_path(source)?;
        loop {
            let attempt = crate::random::nonzero_128()?;
            let envelope = gc_name::envelope(attempt, ordinal).map_err(|error| {
                crate::publication::problem::Problem::namespace(&error).into_sdk()
            })?;
            let inert = gc_name::inert(attempt, ordinal).map_err(|error| {
                crate::publication::problem::Problem::namespace(&error).into_sdk()
            })?;
            match (
                directory.require_absent(&envelope),
                directory.require_absent(&inert),
            ) {
                (Ok(()), Ok(())) => return Ok(attempt),
                (Err(NamespaceError::Exists), _) | (_, Err(NamespaceError::Exists)) => continue,
                (Err(error), _) | (_, Err(error)) => {
                    return Err(crate::publication::problem::Problem::namespace(&error).into_sdk())
                }
            }
        }
    }
    #[cfg(not(windows))]
    {
        let _ = (source, ordinal);
        crate::random::nonzero_128()
    }
}

pub(crate) fn fresh_cleanup_attempt(
    source: &Path,
    identity: Identity,
    ordinal: u32,
    kind: ArtifactKind,
    directory_role: DirectoryRole,
) -> crate::error::Result<[u8; 16]> {
    #[cfg(windows)]
    {
        let (directory, name) = crate::live_namespace::bind_path(source)?;
        crate::publication::gc::fresh_attempt(
            &directory,
            &name,
            identity,
            ordinal,
            kind,
            directory_role,
        )
        .map_err(|problem| problem.into_sdk())
    }
    #[cfg(not(windows))]
    {
        let _ = (identity, kind, directory_role);
        unique_attempt_id(source, ordinal)
    }
}

pub(crate) fn remove(
    path: &Path,
    file: &File,
    identity: Identity,
    authority: Authority,
) -> Outcome {
    #[cfg(unix)]
    {
        let _ = (
            file,
            authority.attempt_id,
            authority.ordinal,
            authority.kind,
            authority.directory_role,
        );
        match crate::live_namespace::remove_exact(path, identity) {
            Ok(()) => Outcome::clean(),
            Err(cause) => Outcome::failed(cause),
        }
    }
    #[cfg(windows)]
    {
        remove_windows(path, file, identity, authority)
    }
    #[cfg(not(any(unix, windows)))]
    {
        let _ = (path, file, identity, authority);
        Outcome::failed(Error::Unsupported(
            "live artifact cleanup is unavailable on this platform",
        ))
    }
}

pub(crate) fn require_available(
    path: &Path,
    identity: Identity,
    authority: Authority,
) -> crate::error::Result<()> {
    #[cfg(windows)]
    {
        let (directory, name) = crate::live_namespace::bind_path(path)?;
        crate::publication::gc::require_source_available(
            &directory,
            authority.attempt_id,
            authority.ordinal,
            authority.kind,
            authority.directory_role,
            &name,
            identity,
        )
        .map_err(|problem| problem.into_sdk())
    }
    #[cfg(not(windows))]
    {
        let _ = (
            path,
            identity,
            authority.attempt_id,
            authority.ordinal,
            authority.kind,
            authority.directory_role,
        );
        Ok(())
    }
}

pub(crate) fn require_main_available(
    path: &Path,
    identity: Identity,
    database_id: [u8; 16],
) -> crate::error::Result<()> {
    require_available(
        path,
        identity,
        Authority {
            attempt_id: database_id,
            ordinal: 0,
            kind: ArtifactKind::OwnedMain,
            directory_role: DirectoryRole::MainFile,
        },
    )
}

#[cfg(windows)]
fn remove_windows(path: &Path, file: &File, identity: Identity, authority: Authority) -> Outcome {
    use crate::publication::gc::{self, Authority as GcAuthority};
    use crate::publication::namespace::CREATION_SECURITY_KIND;
    use crate::publication::security;
    use crate::publication::CreationSecurity;

    let (directory, name) = match crate::live_namespace::bind_path(path) {
        Ok(bound) => bound,
        Err(cause) => return Outcome::failed(cause),
    };
    let commitment = match security::creator_only_commitment(file) {
        Ok(commitment) => commitment,
        Err(error) => {
            return Outcome::failed(
                crate::publication::problem::Problem::namespace(&error).into_sdk(),
            )
        }
    };
    let retirement = gc::retire(
        &directory,
        GcAuthority {
            attempt_id: authority.attempt_id,
            ordinal: authority.ordinal,
            kind: authority.kind,
            directory_role: authority.directory_role,
            source_name: &name,
            source_file: file,
            identity,
            creation_security: CreationSecurity {
                kind: CREATION_SECURITY_KIND,
                commitment,
            },
            payload: None,
        },
    );
    Outcome {
        cause: retirement.problem.map(|problem| problem.into_sdk()),
        housekeeping: retirement.housekeeping,
        visible: retirement.visible.into_iter().collect(),
    }
}

const fn merge_housekeeping(left: Housekeeping, right: Housekeeping) -> Housekeeping {
    if matches!(left, Housekeeping::Visible) || matches!(right, Housekeeping::Visible) {
        Housekeeping::Visible
    } else if matches!(left, Housekeeping::CrashReappearancePossible)
        || matches!(right, Housekeeping::CrashReappearancePossible)
    {
        Housekeeping::CrashReappearancePossible
    } else {
        Housekeeping::None
    }
}
