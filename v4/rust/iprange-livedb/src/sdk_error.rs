//! Public SDK errors.

use std::fmt;
use std::io;

pub use crate::bootstrap::BootstrapError as FormatError;

/// Stable semantic class for programmatic error handling.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[non_exhaustive]
pub enum ErrorCode {
    InvalidArgument,
    NameInvalid,
    NameExists,
    NameNotFound,
    StaleReference,
    ForeignReference,
    WrongState,
    WrongMode,
    Unsupported,
    Io,
    Format,
    Corrupt,
    ArithmeticOverflow,
    PageSpaceExhausted,
    FeedIndexExhausted,
    MembershipIdExhausted,
    BudgetExceeded,
    WorkLimitTooSmall,
    BufferTooSmall,
    Cancelled,
    Random,
    WriterBusy,
    ReaderCapacityExhausted,
    NoPendingTransaction,
    TransactionAborted,
    CleanupIncomplete,
    ForkedHandle,
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
            Self::WrongMode(_) => ErrorCode::WrongMode,
            Self::Unsupported(_) => ErrorCode::Unsupported,
            Self::Io(_) => ErrorCode::Io,
            Self::Format(_) => ErrorCode::Format,
            Self::Corrupt(_) => ErrorCode::Corrupt,
            Self::ArithmeticOverflow(_) => ErrorCode::ArithmeticOverflow,
            Self::PageSpaceExhausted => ErrorCode::PageSpaceExhausted,
            Self::FeedIndexExhausted => ErrorCode::FeedIndexExhausted,
            Self::MembershipIdExhausted => ErrorCode::MembershipIdExhausted,
            Self::BudgetExceeded(_) => ErrorCode::BudgetExceeded,
            Self::WorkLimitTooSmall { .. } => ErrorCode::WorkLimitTooSmall,
            Self::BufferTooSmall { .. } => ErrorCode::BufferTooSmall,
            Self::Cancelled => ErrorCode::Cancelled,
            Self::Random(_) => ErrorCode::Random,
            Self::WriterBusy => ErrorCode::WriterBusy,
            Self::ReaderCapacityExhausted => ErrorCode::ReaderCapacityExhausted,
            Self::NoPendingTransaction => ErrorCode::NoPendingTransaction,
            Self::TransactionAborted(_) => ErrorCode::TransactionAborted,
            Self::CleanupIncomplete { .. } => ErrorCode::CleanupIncomplete,
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
            Self::InvalidArgument(_)
            | Self::NameInvalid
            | Self::NameExists
            | Self::NameNotFound
            | Self::StaleReference
            | Self::ForeignReference
            | Self::WrongState(_)
            | Self::WrongMode(_)
            | Self::Unsupported(_)
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
