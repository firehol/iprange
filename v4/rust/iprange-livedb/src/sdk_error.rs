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
}

impl Error {
    pub const fn code(&self) -> ErrorCode {
        match self {
            Self::InvalidArgument(_) => ErrorCode::InvalidArgument,
            Self::WrongMode(_) => ErrorCode::WrongMode,
            Self::Unsupported(_) => ErrorCode::Unsupported,
            Self::Io(_) => ErrorCode::Io,
            Self::Format(_) => ErrorCode::Format,
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
        }
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::Io(error) => Some(error),
            Self::InvalidArgument(_)
            | Self::WrongMode(_)
            | Self::Unsupported(_)
            | Self::Format(_) => None,
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

pub type Result<T> = std::result::Result<T, Error>;
