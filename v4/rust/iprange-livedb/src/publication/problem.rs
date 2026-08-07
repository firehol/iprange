//! Fixed, allocation-free publication failure details.

use crate::error::{Error as SdkError, ErrorCode};

use super::main_file;
use super::namespace::NamespaceError;
use super::output;
use super::replacement;
use super::reservation_file;
pub(crate) use super::types::PublicationProblem as Problem;

impl Problem {
    #[cfg(all(test, unix))]
    pub(crate) const fn injected() -> Self {
        Self::new(ErrorCode::Io, None, "injected publication failure")
    }

    pub(crate) const fn cleanup_conflict(detail: &'static str) -> Self {
        Self::new(ErrorCode::CleanupConflict, None, detail)
    }

    #[cfg(windows)]
    pub(crate) const fn cleanup_in_progress(detail: &'static str) -> Self {
        Self::new(ErrorCode::CleanupInProgress, None, detail)
    }

    pub(crate) fn namespace(error: &NamespaceError) -> Self {
        match error {
            NamespaceError::InvalidName => {
                Self::plain(ErrorCode::NameInvalid, "invalid destination name")
            }
            NamespaceError::NotDirectory => {
                Self::plain(ErrorCode::Conflict, "destination parent is not a directory")
            }
            NamespaceError::NotRegular => Self::plain(
                ErrorCode::Conflict,
                "publication name is not a regular file",
            ),
            NamespaceError::Exists => {
                Self::plain(ErrorCode::NameExists, "publication name already exists")
            }
            NamespaceError::Missing => {
                Self::plain(ErrorCode::NameNotFound, "publication name is missing")
            }
            NamespaceError::IdentityChanged => {
                Self::plain(ErrorCode::Conflict, "publication inode identity changed")
            }
            NamespaceError::LinkCount(links) if *links == 0 => {
                Self::plain(ErrorCode::Conflict, "publication inode has no links")
            }
            NamespaceError::LinkCount(_) => {
                Self::plain(ErrorCode::Conflict, "publication inode link count changed")
            }
            NamespaceError::CrossFilesystem => Self::plain(
                ErrorCode::PublicationUnsupported,
                "publication inode is on another filesystem",
            ),
            NamespaceError::AccessPolicy => Self::plain(
                ErrorCode::AccessPolicyUnsupported,
                "creator-only access policy is not proved",
            ),
            NamespaceError::Unsupported => Self::plain(
                ErrorCode::DurabilityUnsupported,
                "filesystem lacks required durable namespace operations",
            ),
            NamespaceError::ForkedHandle => {
                Self::plain(ErrorCode::ForkedHandle, "publication handle crossed fork")
            }
            NamespaceError::Io(source) => {
                Self::io(source, "publication filesystem operation failed")
            }
            NamespaceError::IoAt { source, .. } if source.raw_os_error() == Some(libc::ELOOP) => {
                Self::plain(ErrorCode::Conflict, "publication name is a symlink")
            }
            NamespaceError::IoAt { operation, source } => Self::io(source, operation),
        }
    }

    pub(crate) fn output(error: &output::Error) -> Self {
        match error {
            output::Error::Namespace(cause) => Self::namespace(cause),
            output::Error::Sdk(cause) => Self::sdk(cause),
            output::Error::Bootstrap => {
                Self::plain(ErrorCode::FormatInvalid, "output metadata is malformed")
            }
            output::Error::Gc(problem) => problem.clone(),
            output::Error::FinishedMetaChanged => {
                Self::plain(ErrorCode::Conflict, "finished output metadata changed")
            }
            output::Error::FinishedLengthChanged => {
                Self::plain(ErrorCode::Conflict, "finished output length changed")
            }
        }
    }

    pub(crate) fn reservation(error: &reservation_file::Error) -> Self {
        match error {
            reservation_file::Error::Namespace(cause) => Self::namespace(cause),
            reservation_file::Error::Sdk(cause) => Self::sdk(cause),
            reservation_file::Error::Output(cause) => Self::output(cause),
            reservation_file::Error::Gc(problem) => problem.clone(),
            reservation_file::Error::Checkpoint(problem) => problem.clone(),
            reservation_file::Error::Codec => {
                Self::plain(ErrorCode::FormatInvalid, "reservation record is malformed")
            }
            reservation_file::Error::HeaderChanged => {
                Self::plain(ErrorCode::Conflict, "reservation record changed")
            }
            reservation_file::Error::HeaderInvariant => Self::plain(
                ErrorCode::FormatInvalid,
                "reservation state is inconsistent",
            ),
            reservation_file::Error::LengthChanged => {
                Self::plain(ErrorCode::Conflict, "reservation length changed")
            }
        }
    }

    pub(crate) fn replacement(error: &replacement::Error) -> Self {
        match error {
            replacement::Error::Namespace(cause) => Self::namespace(cause),
            replacement::Error::Sdk(cause) => Self::sdk(cause),
            replacement::Error::Output(cause) => Self::output(cause),
            replacement::Error::SameIdentity => Self::plain(
                ErrorCode::Conflict,
                "replacement source and destination identities match",
            ),
            replacement::Error::ContentChanged => Self::plain(
                ErrorCode::Conflict,
                "replacement destination content changed",
            ),
        }
    }

    pub(crate) fn main(error: &main_file::Error) -> Self {
        match error {
            main_file::Error::Namespace(cause) => Self::namespace(cause),
            main_file::Error::Sdk(cause) => Self::sdk(cause),
            main_file::Error::Output(cause) => Self::output(cause),
            main_file::Error::Reservation(cause) => Self::reservation(cause),
            main_file::Error::Checkpoint(problem) => problem.clone(),
            #[cfg(unix)]
            main_file::Error::PreviousLinkCount => Self::plain(
                ErrorCode::CleanupConflict,
                "retired previous destination still has a link",
            ),
            #[cfg(unix)]
            main_file::Error::ReservationLinkCount => Self::plain(
                ErrorCode::CleanupConflict,
                "retired reservation still has a link",
            ),
            #[cfg(windows)]
            main_file::Error::Gc(problem) => problem.clone(),
            #[cfg(all(test, unix))]
            main_file::Error::Injected => Self::injected(),
        }
    }

    const fn plain(code: ErrorCode, detail: &'static str) -> Self {
        Self::new(code, None, detail)
    }

    fn io(source: &std::io::Error, detail: &'static str) -> Self {
        Self::new(ErrorCode::Io, source.raw_os_error(), detail)
    }

    pub(crate) fn sdk(error: &SdkError) -> Self {
        let os_code = match error {
            SdkError::Io(source) => source.raw_os_error(),
            _ => None,
        };
        Self::new(error.code(), os_code, "publication SDK operation failed")
    }

    #[cfg(windows)]
    pub(crate) fn into_sdk(self) -> SdkError {
        let code = self.code;
        let os_code = self.os_code;
        let detail = match self.detail {
            std::borrow::Cow::Borrowed(detail) => Some(detail),
            std::borrow::Cow::Owned(_) => None,
        };
        match (code, os_code, detail) {
            (ErrorCode::Io, Some(code), _) => SdkError::Io(std::io::Error::from_raw_os_error(code)),
            (ErrorCode::NameInvalid, _, _) => SdkError::NameInvalid,
            (ErrorCode::NameExists, _, _) => SdkError::NameExists,
            (ErrorCode::NameNotFound, _, _) => SdkError::NameNotFound,
            (ErrorCode::DurabilityUnsupported, _, Some(detail)) => {
                SdkError::DurabilityUnsupported(detail)
            }
            (ErrorCode::CleanupInProgress, _, Some(detail)) => SdkError::CleanupInProgress(detail),
            (ErrorCode::CleanupConflict, _, Some(detail)) => SdkError::CleanupConflict(detail),
            (ErrorCode::DirectoryIdentityMismatch, _, _) => SdkError::DirectoryIdentityMismatch,
            (ErrorCode::ForkedHandle, _, _) => SdkError::ForkedHandle,
            (_, _, Some(detail)) => SdkError::Conflict(detail),
            _ => SdkError::WorkerOperation { code, os_code },
        }
    }
}
