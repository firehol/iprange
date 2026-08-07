use std::borrow::Cow;
#[cfg(unix)]
use std::fs::File;

use crate::error::{Error, ErrorCode};
use crate::publication::namespace::CREATION_SECURITY_KIND;
#[cfg(unix)]
use crate::publication::namespace::{regular_link_count, Directory, NamespaceError};
use crate::publication::security::Profile;
use crate::validation::LocalFileIdentity;

#[cfg(unix)]
use super::MAX_OWNED;
use super::{local, Owned, ScratchCleanup, ScratchProblem, ScratchResidue};

#[cfg(unix)]
pub(super) fn set_removed_problems(
    removed: &[bool; MAX_OWNED],
    problems: &mut [Option<ScratchProblem>; MAX_OWNED],
    problem: ScratchProblem,
) {
    for index in 0..MAX_OWNED {
        if removed[index] {
            problems[index] = Some(problem.clone());
        }
    }
}

#[cfg(unix)]
pub(super) fn remove(
    directory: &Directory,
    owner: &Owned,
) -> std::result::Result<(), ScratchProblem> {
    let file = &owner.shared.file;
    if !require_named_link(directory, owner, file)? {
        return Ok(());
    }
    let removed = directory
        .unlink_exact(&owner.name, owner.identity)
        .map_err(|error| scratch_problem(&error))?;
    if !removed {
        return Err(conflict("owned recovery scratch lost its exact name"));
    }
    require_unlinked(file)
}

#[cfg(unix)]
fn require_named_link(
    directory: &Directory,
    owner: &Owned,
    file: &File,
) -> std::result::Result<bool, ScratchProblem> {
    let links = regular_link_count(file).map_err(|error| scratch_problem(&error))?;
    if links > 1 {
        return Err(conflict("owned recovery scratch has unexpected links"));
    }
    if links == 0 {
        directory
            .require_absent(&owner.name)
            .map_err(|error| scratch_problem(&error))?;
        return Ok(false);
    }
    Ok(true)
}

#[cfg(unix)]
fn require_unlinked(file: &File) -> std::result::Result<(), ScratchProblem> {
    let links = regular_link_count(file).map_err(|error| scratch_problem(&error))?;
    if links != 0 {
        return Err(conflict(
            "owned recovery scratch remained linked after removal",
        ));
    }
    Ok(())
}

#[cfg(unix)]
fn conflict(detail: &'static str) -> ScratchProblem {
    ScratchProblem {
        code: ErrorCode::CleanupConflict,
        os_code: None,
        detail: Cow::Borrowed(detail),
    }
}

pub(super) fn residue(
    directory_identity: LocalFileIdentity,
    profile: &Profile,
    owner: Owned,
    problem: ScratchProblem,
) -> ScratchResidue {
    ScratchResidue {
        ordinal: owner.ordinal,
        directory_identity,
        basename: owner.name.bytes().into(),
        identity: local(owner.identity),
        creation_security_kind: CREATION_SECURITY_KIND,
        creation_security_commitment: profile.commitment(),
        problem,
    }
}

#[cfg(unix)]
pub(super) fn scratch_problem(error: &NamespaceError) -> ScratchProblem {
    match error {
        NamespaceError::ForkedHandle => ScratchProblem {
            code: ErrorCode::ForkedHandle,
            os_code: None,
            detail: Cow::Borrowed("scratch owner crossed fork"),
        },
        NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => {
            io_problem(source, "recovery scratch cleanup failed")
        }
        _ => conflict("recovery scratch ownership changed"),
    }
}

pub(crate) fn residue_error(cleanup: &ScratchCleanup) -> Error {
    let problem = &cleanup
        .residues
        .first()
        .expect("unclean scratch has at least one residue")
        .problem;
    match (problem.code, problem.os_code) {
        (ErrorCode::Io, Some(code)) => Error::Io(std::io::Error::from_raw_os_error(code)),
        _ => match &problem.detail {
            Cow::Borrowed(detail) => Error::Corrupt(detail),
            Cow::Owned(_) => Error::WorkerOperation {
                code: problem.code,
                os_code: problem.os_code,
            },
        },
    }
}

#[cfg(unix)]
fn io_problem(error: &std::io::Error, detail: &'static str) -> ScratchProblem {
    ScratchProblem {
        code: ErrorCode::Io,
        os_code: error.raw_os_error(),
        detail: Cow::Borrowed(detail),
    }
}
