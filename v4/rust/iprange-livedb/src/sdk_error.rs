//! Public SDK errors.

use std::fmt;
use std::io;

pub use crate::bootstrap::BootstrapError as FormatError;

/// Stable semantic class for programmatic error handling.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[non_exhaustive]
#[repr(u32)]
pub enum ErrorCode {
    InvalidArgument = 1,
    NullPointer = 2,
    MisalignedPointer = 3,
    InvalidLength = 4,
    InvalidEnum = 5,
    ReservedNonzero = 6,
    BufferTooSmall = 7,
    WrongHandleKind = 8,
    HandleClosed = 9,
    HandleBusy = 10,
    WrongState = 11,
    WrongAddressFamily = 12,
    WrongValueKind = 13,
    WrongValueTag = 14,
    RangeReversed = 15,
    NameInvalid = 16,
    NameExists = 17,
    NameNotFound = 18,
    StaleReference = 19,
    ForeignReference = 20,
    NoPendingTransaction = 21,
    TransactionAborted = 22,
    AbortIncomplete = 23,
    InsufficientResourceBudget = 24,
    PageSpaceExhausted = 25,
    WorkLimitTooSmall = 26,
    Cancelled = 27,
    SourceFailed = 28,
    SinkFailed = 29,
    StoppedBySink = 30,
    Io = 31,
    FormatInvalid = 32,
    NotV4 = 33,
    DurabilityUnsupported = 34,
    PublicationUnsupported = 35,
    AccessPolicyUnsupported = 36,
    Conflict = 37,
    Unresolvable = 38,
    WriterBusy = 39,
    DirectoryIdentityMismatch = 40,
    DestinationNameMismatch = 41,
    CleanupConflict = 42,
    CoordinationSequenceExhausted = 43,
    LiveCoordinationUnsupported = 44,
    LiveCoordinationCleanupRequired = 45,
    LiveCoordinationMalformedRequiresReset = 46,
    LiveOpenCleanupRequired = 47,
    LiveRecoveryCoordinationUnavailable = 48,
    LiveRecoveryCurrentGenerationUnprovable = 49,
    LiveRecoveryCurrentGenerationUnreadable = 50,
    RecoveryCandidateChanged = 51,
    RecoveryPreparationFailed = 52,
    SnapshotPreparationFailed = 53,
    TransitionSuperseded = 54,
    CurrentGenerationUnprovable = 55,
    ForkedHandle = 56,
    Panic = 57,
    OsUnsupported = 58,
    TransactionIdExhausted = 59,
    ArithmeticOverflow = 60,
    FeedIndexExhausted = 61,
    MembershipIdExhausted = 62,
    ReaderCapacityExhausted = 63,
    CleanupInProgress = 64,
}

/// One SDK failure with its original cause where one exists.
#[derive(Debug)]
#[non_exhaustive]
pub enum Error {
    InvalidArgument(&'static str),
    NameInvalid,
    NameExists,
    NameNotFound,
    StaleReference,
    ForeignReference,
    WrongState(&'static str),
    WrongMode(&'static str),
    Unsupported(&'static str),
    DurabilityUnsupported(&'static str),
    Io(io::Error),
    Format(FormatError),
    Corrupt(&'static str),
    ArithmeticOverflow(&'static str),
    PageSpaceExhausted,
    FeedIndexExhausted,
    MembershipIdExhausted,
    BudgetExceeded(&'static str),
    WorkLimitTooSmall {
        required_pages: u64,
    },
    BufferTooSmall {
        required: u64,
    },
    Cancelled,
    Random(getrandom::Error),
    WriterBusy,
    ReaderCapacityExhausted,
    NoPendingTransaction,
    TransactionAborted(Box<Error>),
    CleanupIncomplete {
        cause: Box<Error>,
        cleanup: Box<Error>,
    },
    SinkFailed(Box<Error>),
    StoppedBySink,
    LiveRecoveryCoordinationUnavailable(Box<Error>),
    LiveRecoveryCurrentGenerationUnprovable,
    LiveRecoveryCurrentGenerationUnreadable,
    RecoveryCandidateChanged,
    DirectoryIdentityMismatch,
    Conflict(&'static str),
    Unresolvable(&'static str),
    CleanupConflict(&'static str),
    CleanupInProgress(&'static str),
    ForkedHandle,
}

impl Error {
    pub const fn code(&self) -> ErrorCode {
        match self {
            Self::InvalidArgument(_) => ErrorCode::InvalidArgument,
            Self::NameInvalid => ErrorCode::NameInvalid,
            Self::NameExists => ErrorCode::NameExists,
            Self::NameNotFound => ErrorCode::NameNotFound,
            Self::StaleReference => ErrorCode::StaleReference,
            Self::ForeignReference => ErrorCode::ForeignReference,
            Self::WrongState(_) => ErrorCode::WrongState,
            Self::WrongMode(_) => ErrorCode::WrongState,
            Self::Unsupported(_) => ErrorCode::OsUnsupported,
            Self::DurabilityUnsupported(_) => ErrorCode::DurabilityUnsupported,
            Self::Io(_) => ErrorCode::Io,
            Self::Format(_) => ErrorCode::FormatInvalid,
            Self::Corrupt(_) => ErrorCode::FormatInvalid,
            Self::ArithmeticOverflow(_) => ErrorCode::ArithmeticOverflow,
            Self::PageSpaceExhausted => ErrorCode::PageSpaceExhausted,
            Self::FeedIndexExhausted => ErrorCode::FeedIndexExhausted,
            Self::MembershipIdExhausted => ErrorCode::MembershipIdExhausted,
            Self::BudgetExceeded(_) => ErrorCode::InsufficientResourceBudget,
            Self::WorkLimitTooSmall { .. } => ErrorCode::WorkLimitTooSmall,
            Self::BufferTooSmall { .. } => ErrorCode::BufferTooSmall,
            Self::Cancelled => ErrorCode::Cancelled,
            Self::Random(_) => ErrorCode::Io,
            Self::WriterBusy => ErrorCode::WriterBusy,
            Self::ReaderCapacityExhausted => ErrorCode::ReaderCapacityExhausted,
            Self::NoPendingTransaction => ErrorCode::NoPendingTransaction,
            Self::TransactionAborted(_) => ErrorCode::TransactionAborted,
            Self::CleanupIncomplete { .. } => ErrorCode::CleanupInProgress,
            Self::SinkFailed(_) => ErrorCode::SinkFailed,
            Self::StoppedBySink => ErrorCode::StoppedBySink,
            Self::LiveRecoveryCoordinationUnavailable(_) => {
                ErrorCode::LiveRecoveryCoordinationUnavailable
            }
            Self::LiveRecoveryCurrentGenerationUnprovable => {
                ErrorCode::LiveRecoveryCurrentGenerationUnprovable
            }
            Self::LiveRecoveryCurrentGenerationUnreadable => {
                ErrorCode::LiveRecoveryCurrentGenerationUnreadable
            }
            Self::RecoveryCandidateChanged => ErrorCode::RecoveryCandidateChanged,
            Self::DirectoryIdentityMismatch => ErrorCode::DirectoryIdentityMismatch,
            Self::Conflict(_) => ErrorCode::Conflict,
            Self::Unresolvable(_) => ErrorCode::Unresolvable,
            Self::CleanupConflict(_) => ErrorCode::CleanupConflict,
            Self::CleanupInProgress(_) => ErrorCode::CleanupInProgress,
            Self::ForkedHandle => ErrorCode::ForkedHandle,
        }
    }

    pub(crate) const fn residue_possible(&self) -> bool {
        matches!(self, Self::CleanupIncomplete { .. })
    }
}

impl fmt::Display for Error {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidArgument(detail) => write!(output, "invalid argument: {detail}"),
            Self::NameInvalid => output.write_str("feed name is invalid"),
            Self::NameExists => output.write_str("feed name already exists"),
            Self::NameNotFound => output.write_str("feed name does not exist"),
            Self::StaleReference => output.write_str("operation reference is stale"),
            Self::ForeignReference => {
                output.write_str("operation reference belongs to another transaction")
            }
            Self::WrongState(detail) => write!(output, "wrong operation state: {detail}"),
            Self::WrongMode(detail) => write!(output, "wrong database mode: {detail}"),
            Self::Unsupported(detail) => write!(output, "unsupported operation: {detail}"),
            Self::DurabilityUnsupported(detail) => {
                write!(output, "durability is unsupported: {detail}")
            }
            Self::Io(error) => write!(output, "I/O error: {error}"),
            Self::Format(error) => write!(output, "invalid v4 file: {error:?}"),
            Self::Corrupt(detail) => write!(output, "malformed v4 page: {detail}"),
            Self::ArithmeticOverflow(detail) => write!(output, "arithmetic overflow: {detail}"),
            Self::PageSpaceExhausted => output.write_str("v4 page-number space is exhausted"),
            Self::FeedIndexExhausted => output.write_str("feed-index space is exhausted"),
            Self::MembershipIdExhausted => output.write_str("membership-ID space is exhausted"),
            Self::BudgetExceeded(detail) => write!(output, "resource budget exceeded: {detail}"),
            Self::WorkLimitTooSmall { required_pages } => {
                write!(
                    output,
                    "work limit is too small; {required_pages} pages are required"
                )
            }
            Self::BufferTooSmall { required } => {
                write!(
                    output,
                    "output buffer is too small; {required} bytes are required"
                )
            }
            Self::Cancelled => output.write_str("operation was cancelled"),
            Self::Random(error) => write!(output, "operating-system randomness failed: {error}"),
            Self::WriterBusy => output.write_str("another live writer owns this database"),
            Self::ReaderCapacityExhausted => {
                output.write_str("the live reader table has no free slot")
            }
            Self::NoPendingTransaction => output.write_str("no changed transaction is pending"),
            Self::TransactionAborted(cause) => {
                write!(output, "the pending transaction was aborted: {cause}")
            }
            Self::CleanupIncomplete { cause, cleanup } => {
                write!(output, "{cause}; cleanup also failed: {cleanup}")
            }
            Self::SinkFailed(cause) => write!(output, "validation sink failed: {cause}"),
            Self::StoppedBySink => output.write_str("validation was stopped by its sink"),
            Self::LiveRecoveryCoordinationUnavailable(cause) => {
                write!(output, "live recovery coordination is unavailable: {cause}")
            }
            Self::LiveRecoveryCurrentGenerationUnprovable => {
                output.write_str("the current live recovery generation cannot be proved")
            }
            Self::LiveRecoveryCurrentGenerationUnreadable => {
                output.write_str("the current live recovery generation is unreadable")
            }
            Self::RecoveryCandidateChanged => {
                output.write_str("the selected recovery candidate changed")
            }
            Self::DirectoryIdentityMismatch => {
                output.write_str("the retained directory identity changed")
            }
            Self::Conflict(detail) => write!(output, "conflict: {detail}"),
            Self::Unresolvable(detail) => write!(output, "unresolvable state: {detail}"),
            Self::CleanupConflict(detail) => write!(output, "cleanup conflict: {detail}"),
            Self::CleanupInProgress(detail) => {
                write!(output, "cleanup is in progress: {detail}")
            }
            Self::ForkedHandle => output.write_str("the live handle belongs to another process"),
        }
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::Io(error) => Some(error),
            Self::Random(error) => Some(error),
            Self::TransactionAborted(cause) => Some(cause.as_ref()),
            Self::CleanupIncomplete { cause, .. } => Some(cause.as_ref()),
            Self::SinkFailed(cause) => Some(cause.as_ref()),
            Self::LiveRecoveryCoordinationUnavailable(cause) => Some(cause.as_ref()),
            Self::InvalidArgument(_)
            | Self::NameInvalid
            | Self::NameExists
            | Self::NameNotFound
            | Self::StaleReference
            | Self::ForeignReference
            | Self::WrongState(_)
            | Self::WrongMode(_)
            | Self::Unsupported(_)
            | Self::DurabilityUnsupported(_)
            | Self::Format(_)
            | Self::Corrupt(_)
            | Self::ArithmeticOverflow(_)
            | Self::PageSpaceExhausted
            | Self::FeedIndexExhausted
            | Self::MembershipIdExhausted
            | Self::BudgetExceeded(_)
            | Self::WorkLimitTooSmall { .. }
            | Self::BufferTooSmall { .. }
            | Self::Cancelled
            | Self::WriterBusy
            | Self::ReaderCapacityExhausted
            | Self::NoPendingTransaction
            | Self::StoppedBySink
            | Self::LiveRecoveryCurrentGenerationUnprovable
            | Self::LiveRecoveryCurrentGenerationUnreadable
            | Self::RecoveryCandidateChanged
            | Self::DirectoryIdentityMismatch
            | Self::Conflict(_)
            | Self::Unresolvable(_)
            | Self::CleanupConflict(_)
            | Self::CleanupInProgress(_)
            | Self::ForkedHandle => None,
        }
    }
}

impl From<io::Error> for Error {
    fn from(error: io::Error) -> Self {
        Self::Io(error)
    }
}

impl From<FormatError> for Error {
    fn from(error: FormatError) -> Self {
        Self::Format(error)
    }
}

impl From<getrandom::Error> for Error {
    fn from(error: getrandom::Error) -> Self {
        Self::Random(error)
    }
}

pub type Result<T> = std::result::Result<T, Error>;

#[cfg(test)]
mod tests {
    use super::ErrorCode;

    #[test]
    fn stable_error_codes_are_contiguous() {
        let codes = [
            ErrorCode::InvalidArgument,
            ErrorCode::NullPointer,
            ErrorCode::MisalignedPointer,
            ErrorCode::InvalidLength,
            ErrorCode::InvalidEnum,
            ErrorCode::ReservedNonzero,
            ErrorCode::BufferTooSmall,
            ErrorCode::WrongHandleKind,
            ErrorCode::HandleClosed,
            ErrorCode::HandleBusy,
            ErrorCode::WrongState,
            ErrorCode::WrongAddressFamily,
            ErrorCode::WrongValueKind,
            ErrorCode::WrongValueTag,
            ErrorCode::RangeReversed,
            ErrorCode::NameInvalid,
            ErrorCode::NameExists,
            ErrorCode::NameNotFound,
            ErrorCode::StaleReference,
            ErrorCode::ForeignReference,
            ErrorCode::NoPendingTransaction,
            ErrorCode::TransactionAborted,
            ErrorCode::AbortIncomplete,
            ErrorCode::InsufficientResourceBudget,
            ErrorCode::PageSpaceExhausted,
            ErrorCode::WorkLimitTooSmall,
            ErrorCode::Cancelled,
            ErrorCode::SourceFailed,
            ErrorCode::SinkFailed,
            ErrorCode::StoppedBySink,
            ErrorCode::Io,
            ErrorCode::FormatInvalid,
            ErrorCode::NotV4,
            ErrorCode::DurabilityUnsupported,
            ErrorCode::PublicationUnsupported,
            ErrorCode::AccessPolicyUnsupported,
            ErrorCode::Conflict,
            ErrorCode::Unresolvable,
            ErrorCode::WriterBusy,
            ErrorCode::DirectoryIdentityMismatch,
            ErrorCode::DestinationNameMismatch,
            ErrorCode::CleanupConflict,
            ErrorCode::CoordinationSequenceExhausted,
            ErrorCode::LiveCoordinationUnsupported,
            ErrorCode::LiveCoordinationCleanupRequired,
            ErrorCode::LiveCoordinationMalformedRequiresReset,
            ErrorCode::LiveOpenCleanupRequired,
            ErrorCode::LiveRecoveryCoordinationUnavailable,
            ErrorCode::LiveRecoveryCurrentGenerationUnprovable,
            ErrorCode::LiveRecoveryCurrentGenerationUnreadable,
            ErrorCode::RecoveryCandidateChanged,
            ErrorCode::RecoveryPreparationFailed,
            ErrorCode::SnapshotPreparationFailed,
            ErrorCode::TransitionSuperseded,
            ErrorCode::CurrentGenerationUnprovable,
            ErrorCode::ForkedHandle,
            ErrorCode::Panic,
            ErrorCode::OsUnsupported,
            ErrorCode::TransactionIdExhausted,
            ErrorCode::ArithmeticOverflow,
            ErrorCode::FeedIndexExhausted,
            ErrorCode::MembershipIdExhausted,
            ErrorCode::ReaderCapacityExhausted,
            ErrorCode::CleanupInProgress,
        ];
        for (index, code) in codes.into_iter().enumerate() {
            assert_eq!(code as u32, index as u32 + 1);
        }
    }
}
