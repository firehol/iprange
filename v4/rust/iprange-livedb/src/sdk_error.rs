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
    FaultWorkerUnavailable = 65,
    FaultWorkerFailed = 66,
    UnsupportedStructure = 67,
    WrongStructureKind = 68,
    StructureIdExhausted = 69,
}

impl ErrorCode {
    pub(crate) const fn from_wire(value: u32) -> Option<Self> {
        Some(match value {
            1 => Self::InvalidArgument,
            2 => Self::NullPointer,
            3 => Self::MisalignedPointer,
            4 => Self::InvalidLength,
            5 => Self::InvalidEnum,
            6 => Self::ReservedNonzero,
            7 => Self::BufferTooSmall,
            8 => Self::WrongHandleKind,
            9 => Self::HandleClosed,
            10 => Self::HandleBusy,
            11 => Self::WrongState,
            12 => Self::WrongAddressFamily,
            13 => Self::WrongValueKind,
            14 => Self::WrongValueTag,
            15 => Self::RangeReversed,
            16 => Self::NameInvalid,
            17 => Self::NameExists,
            18 => Self::NameNotFound,
            19 => Self::StaleReference,
            20 => Self::ForeignReference,
            21 => Self::NoPendingTransaction,
            22 => Self::TransactionAborted,
            23 => Self::AbortIncomplete,
            24 => Self::InsufficientResourceBudget,
            25 => Self::PageSpaceExhausted,
            26 => Self::WorkLimitTooSmall,
            27 => Self::Cancelled,
            28 => Self::SourceFailed,
            29 => Self::SinkFailed,
            30 => Self::StoppedBySink,
            31 => Self::Io,
            32 => Self::FormatInvalid,
            33 => Self::NotV4,
            34 => Self::DurabilityUnsupported,
            35 => Self::PublicationUnsupported,
            36 => Self::AccessPolicyUnsupported,
            37 => Self::Conflict,
            38 => Self::Unresolvable,
            39 => Self::WriterBusy,
            40 => Self::DirectoryIdentityMismatch,
            41 => Self::DestinationNameMismatch,
            42 => Self::CleanupConflict,
            43 => Self::CoordinationSequenceExhausted,
            44 => Self::LiveCoordinationUnsupported,
            45 => Self::LiveCoordinationCleanupRequired,
            46 => Self::LiveCoordinationMalformedRequiresReset,
            47 => Self::LiveOpenCleanupRequired,
            48 => Self::LiveRecoveryCoordinationUnavailable,
            49 => Self::LiveRecoveryCurrentGenerationUnprovable,
            50 => Self::LiveRecoveryCurrentGenerationUnreadable,
            51 => Self::RecoveryCandidateChanged,
            52 => Self::RecoveryPreparationFailed,
            53 => Self::SnapshotPreparationFailed,
            54 => Self::TransitionSuperseded,
            55 => Self::CurrentGenerationUnprovable,
            56 => Self::ForkedHandle,
            57 => Self::Panic,
            58 => Self::OsUnsupported,
            59 => Self::TransactionIdExhausted,
            60 => Self::ArithmeticOverflow,
            61 => Self::FeedIndexExhausted,
            62 => Self::MembershipIdExhausted,
            63 => Self::ReaderCapacityExhausted,
            64 => Self::CleanupInProgress,
            65 => Self::FaultWorkerUnavailable,
            66 => Self::FaultWorkerFailed,
            67 => Self::UnsupportedStructure,
            68 => Self::WrongStructureKind,
            69 => Self::StructureIdExhausted,
            _ => return None,
        })
    }
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
    WrongAddressFamily(&'static str),
    WrongValueKind(&'static str),
    WrongValueTag(&'static str),
    UnsupportedStructure(u8),
    WrongStructureKind(&'static str),
    LiveCoordinationUnsupported,
    Unsupported(&'static str),
    DurabilityUnsupported(&'static str),
    Io(io::Error),
    Format(FormatError),
    Corrupt(&'static str),
    ArithmeticOverflow(&'static str),
    PageSpaceExhausted,
    FeedIndexExhausted,
    MembershipIdExhausted,
    StructureIdExhausted,
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
    /// A version-matched worker reported this stable error class.
    WorkerOperation {
        code: ErrorCode,
        os_code: Option<i32>,
    },
}

impl Error {
    #[cold]
    pub(crate) fn invalid_argument(detail: &'static str) -> Self {
        Self::InvalidArgument(detail)
    }

    #[cold]
    pub(crate) fn corrupt(detail: &'static str) -> Self {
        Self::Corrupt(detail)
    }

    #[cold]
    pub(crate) fn arithmetic_overflow(detail: &'static str) -> Self {
        Self::ArithmeticOverflow(detail)
    }

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
            Self::WrongAddressFamily(_) => ErrorCode::WrongAddressFamily,
            Self::WrongValueKind(_) => ErrorCode::WrongValueKind,
            Self::WrongValueTag(_) => ErrorCode::WrongValueTag,
            Self::UnsupportedStructure(_) => ErrorCode::UnsupportedStructure,
            Self::WrongStructureKind(_) => ErrorCode::WrongStructureKind,
            Self::LiveCoordinationUnsupported => ErrorCode::LiveCoordinationUnsupported,
            Self::Unsupported(_) => ErrorCode::OsUnsupported,
            Self::DurabilityUnsupported(_) => ErrorCode::DurabilityUnsupported,
            Self::Io(_) => ErrorCode::Io,
            Self::Format(_) => ErrorCode::FormatInvalid,
            Self::Corrupt(_) => ErrorCode::FormatInvalid,
            Self::ArithmeticOverflow(_) => ErrorCode::ArithmeticOverflow,
            Self::PageSpaceExhausted => ErrorCode::PageSpaceExhausted,
            Self::FeedIndexExhausted => ErrorCode::FeedIndexExhausted,
            Self::MembershipIdExhausted => ErrorCode::MembershipIdExhausted,
            Self::StructureIdExhausted => ErrorCode::StructureIdExhausted,
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
            Self::WorkerOperation { code, .. } => *code,
        }
    }

    pub(crate) const fn residue_possible(&self) -> bool {
        matches!(self, Self::CleanupIncomplete { .. })
    }
}

pub(crate) fn combine_errors(cause: Error, cleanup: Result<()>) -> Error {
    match cleanup {
        Ok(()) => cause,
        Err(cleanup) => Error::CleanupIncomplete {
            cause: Box::new(cause),
            cleanup: Box::new(cleanup),
        },
    }
}

pub(crate) fn finish_with_cleanup<T>(operation: Result<T>, cleanup: Result<()>) -> Result<T> {
    match operation {
        Ok(value) => cleanup.map(|()| value),
        Err(cause) => Err(combine_errors(cause, cleanup)),
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
            Self::WrongAddressFamily(detail) => {
                write!(output, "wrong address family: {detail}")
            }
            Self::WrongValueKind(detail) => write!(output, "wrong value kind: {detail}"),
            Self::WrongValueTag(detail) => write!(output, "wrong value tag: {detail}"),
            Self::UnsupportedStructure(kind) => {
                write!(output, "unsupported v4 structure kind: {kind}")
            }
            Self::WrongStructureKind(detail) => {
                write!(output, "wrong structure kind: {detail}")
            }
            Self::LiveCoordinationUnsupported => {
                output.write_str("live coordination is unsupported on this platform")
            }
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
            Self::StructureIdExhausted => output.write_str("structure-ID space is exhausted"),
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
            Self::WorkerOperation { code, os_code } => match os_code {
                Some(os_code) => write!(
                    output,
                    "isolated worker operation failed ({code:?}, OS error {os_code})"
                ),
                None => write!(output, "isolated worker operation failed ({code:?})"),
            },
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
            | Self::WrongAddressFamily(_)
            | Self::WrongValueKind(_)
            | Self::WrongValueTag(_)
            | Self::UnsupportedStructure(_)
            | Self::WrongStructureKind(_)
            | Self::LiveCoordinationUnsupported
            | Self::Unsupported(_)
            | Self::DurabilityUnsupported(_)
            | Self::Format(_)
            | Self::Corrupt(_)
            | Self::ArithmeticOverflow(_)
            | Self::PageSpaceExhausted
            | Self::FeedIndexExhausted
            | Self::MembershipIdExhausted
            | Self::StructureIdExhausted
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
            | Self::ForkedHandle
            | Self::WorkerOperation { .. } => None,
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
        match error {
            FormatError::UnsupportedStructure(kind) => Self::UnsupportedStructure(kind),
            other => Self::Format(other),
        }
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
            ErrorCode::FaultWorkerUnavailable,
            ErrorCode::FaultWorkerFailed,
            ErrorCode::UnsupportedStructure,
            ErrorCode::WrongStructureKind,
            ErrorCode::StructureIdExhausted,
        ];
        for (index, code) in codes.into_iter().enumerate() {
            assert_eq!(code as u32, index as u32 + 1);
        }
    }
}
