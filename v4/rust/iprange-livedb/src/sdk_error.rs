//! Public SDK errors.

use std::fmt;
use std::io;

pub use crate::bootstrap::BootstrapError as FormatError;

/// Stable semantic class for programmatic error handling.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[non_exhaustive]
pub enum ErrorCode {
    InvalidArgument,
    WrongMode,
    Unsupported,
    Io,
    Format,
    Corrupt,
    ArithmeticOverflow,
    PageSpaceExhausted,
    BudgetExceeded,
    Random,
    WriterBusy,
    ReaderCapacityExhausted,
    NoPendingTransaction,
    TransactionAborted,
    ForkedHandle,
}

/// One SDK failure with its original cause where one exists.
#[derive(Debug)]
#[non_exhaustive]
pub enum Error {
    InvalidArgument(&'static str),
    WrongMode(&'static str),
    Unsupported(&'static str),
    Io(io::Error),
    Format(FormatError),
    Corrupt(&'static str),
    ArithmeticOverflow(&'static str),
    PageSpaceExhausted,
    BudgetExceeded(&'static str),
    Random(getrandom::Error),
    WriterBusy,
    ReaderCapacityExhausted,
    NoPendingTransaction,
    TransactionAborted(Box<Error>),
    ForkedHandle,
}

impl Error {
    pub const fn code(&self) -> ErrorCode {
        match self {
            Self::InvalidArgument(_) => ErrorCode::InvalidArgument,
            Self::WrongMode(_) => ErrorCode::WrongMode,
            Self::Unsupported(_) => ErrorCode::Unsupported,
            Self::Io(_) => ErrorCode::Io,
            Self::Format(_) => ErrorCode::Format,
            Self::Corrupt(_) => ErrorCode::Corrupt,
            Self::ArithmeticOverflow(_) => ErrorCode::ArithmeticOverflow,
            Self::PageSpaceExhausted => ErrorCode::PageSpaceExhausted,
            Self::BudgetExceeded(_) => ErrorCode::BudgetExceeded,
            Self::Random(_) => ErrorCode::Random,
            Self::WriterBusy => ErrorCode::WriterBusy,
            Self::ReaderCapacityExhausted => ErrorCode::ReaderCapacityExhausted,
            Self::NoPendingTransaction => ErrorCode::NoPendingTransaction,
            Self::TransactionAborted(_) => ErrorCode::TransactionAborted,
            Self::ForkedHandle => ErrorCode::ForkedHandle,
        }
    }
}

impl fmt::Display for Error {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidArgument(detail) => write!(output, "invalid argument: {detail}"),
            Self::WrongMode(detail) => write!(output, "wrong database mode: {detail}"),
            Self::Unsupported(detail) => write!(output, "unsupported operation: {detail}"),
            Self::Io(error) => write!(output, "I/O error: {error}"),
            Self::Format(error) => write!(output, "invalid v4 file: {error:?}"),
            Self::Corrupt(detail) => write!(output, "malformed v4 page: {detail}"),
            Self::ArithmeticOverflow(detail) => write!(output, "arithmetic overflow: {detail}"),
            Self::PageSpaceExhausted => output.write_str("v4 page-number space is exhausted"),
            Self::BudgetExceeded(detail) => write!(output, "resource budget exceeded: {detail}"),
            Self::Random(error) => write!(output, "operating-system randomness failed: {error}"),
            Self::WriterBusy => output.write_str("another live writer owns this database"),
            Self::ReaderCapacityExhausted => {
                output.write_str("the live reader table has no free slot")
            }
            Self::NoPendingTransaction => output.write_str("no changed transaction is pending"),
            Self::TransactionAborted(cause) => {
                write!(output, "the pending transaction was aborted: {cause}")
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
            Self::InvalidArgument(_)
            | Self::WrongMode(_)
            | Self::Unsupported(_)
            | Self::Format(_)
            | Self::Corrupt(_)
            | Self::ArithmeticOverflow(_)
            | Self::PageSpaceExhausted
            | Self::BudgetExceeded(_)
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
