//! Typed failures and C-boundary support.

mod boundary;
mod exports;

#[cfg(debug_assertions)]
pub use boundary::native_test_panic_probe;
pub(crate) use boundary::{
    call, call_with_output, call_with_outputs, input_slice, output_buffer_slot, output_slice,
    output_slot, require_struct_identity, required_input, required_output,
};
pub use exports::{
    iprange_v4_abi1_error_cause, iprange_v4_abi1_error_cleanup_artifact_count,
    iprange_v4_abi1_error_cleanup_artifact_get, iprange_v4_abi1_error_code,
    iprange_v4_abi1_error_destroy, iprange_v4_abi1_error_message_query,
    iprange_v4_abi1_error_message_read, iprange_v4_abi1_error_os_code,
    iprange_v4_abi1_error_take_cleanup_guard,
};

use std::fmt;

use iprange_livedb::publication::PublicationProblem;
use iprange_livedb::recovery::RecoverySourceCleanupGuard;
use iprange_livedb::{Error, ErrorCode};

use crate::abi::CleanupArtifact;
#[cfg(test)]
use crate::abi::STATUS_ERROR;
use crate::handle::{Header, OpaqueHandle, ERROR_KIND};

/// Opaque owned typed error.
#[repr(C)]
#[derive(Debug)]
pub struct ErrorHandle {
    header: Header,
    code: u32,
    caller_code: Option<u64>,
    os_code: Option<i64>,
    message: String,
    cause: Option<Box<ErrorHandle>>,
    cleanup: Vec<CleanupArtifact>,
    cleanup_guard: Option<RecoverySourceCleanupGuard>,
}

unsafe impl OpaqueHandle for ErrorHandle {
    const KIND: u32 = ERROR_KIND;
}

#[derive(Debug)]
pub(crate) struct BoundaryError {
    code: u32,
    message: String,
}

#[derive(Debug)]
pub(crate) enum CallError {
    Boundary(BoundaryError),
    Core(Error),
    Callback {
        code: ErrorCode,
        caller_code: u64,
        message: String,
    },
    Owned(Box<ErrorHandle>),
}

impl BoundaryError {
    pub(crate) fn invalid_argument(message: &'static str) -> Self {
        Self::new(ErrorCode::InvalidArgument, message)
    }

    pub(crate) fn null(message: &'static str) -> Self {
        Self::new(ErrorCode::NullPointer, message)
    }

    pub(crate) fn misaligned(message: &'static str) -> Self {
        Self::new(ErrorCode::MisalignedPointer, message)
    }

    pub(crate) fn invalid_length(message: &'static str) -> Self {
        Self::new(ErrorCode::InvalidLength, message)
    }

    pub(crate) fn invalid_enum(message: &'static str) -> Self {
        Self::new(ErrorCode::InvalidEnum, message)
    }

    pub(crate) fn reserved(message: &'static str) -> Self {
        Self::new(ErrorCode::ReservedNonzero, message)
    }

    pub(crate) fn buffer_too_small(required: u64) -> Self {
        Self::new(
            ErrorCode::BufferTooSmall,
            format!("output buffer is too small; {required} bytes are required"),
        )
    }

    pub(crate) fn handle_busy(message: &'static str) -> Self {
        Self::new(ErrorCode::HandleBusy, message)
    }

    pub(crate) fn wrong_handle(message: &'static str) -> Self {
        Self::new(ErrorCode::WrongHandleKind, message)
    }

    pub(crate) fn wrong_state(message: &'static str) -> Self {
        Self::new(ErrorCode::WrongState, message)
    }

    pub(crate) fn wrong_family(message: &'static str) -> Self {
        Self::new(ErrorCode::WrongAddressFamily, message)
    }

    pub(crate) fn wrong_value_tag(message: &'static str) -> Self {
        Self::new(ErrorCode::WrongValueTag, message)
    }

    pub(crate) fn name_invalid(message: &'static str) -> Self {
        Self::new(ErrorCode::NameInvalid, message)
    }

    pub(crate) fn range_reversed(message: &'static str) -> Self {
        Self::new(ErrorCode::RangeReversed, message)
    }

    fn new(code: ErrorCode, message: impl Into<String>) -> Self {
        Self {
            code: code as u32,
            message: message.into(),
        }
    }
}

impl From<BoundaryError> for ErrorHandle {
    fn from(error: BoundaryError) -> Self {
        Self {
            header: Header::new(ERROR_KIND),
            code: error.code,
            caller_code: None,
            os_code: None,
            message: error.message,
            cause: None,
            cleanup: Vec::new(),
            cleanup_guard: None,
        }
    }
}

impl From<Error> for ErrorHandle {
    fn from(error: Error) -> Self {
        let code = error.code() as u32;
        let message = error.to_string();
        match error {
            Error::Io(source) => Self {
                header: Header::new(ERROR_KIND),
                code,
                caller_code: None,
                os_code: source.raw_os_error().map(i64::from),
                message,
                cause: None,
                cleanup: Vec::new(),
                cleanup_guard: None,
            },
            Error::TransactionAborted(cause)
            | Error::SinkFailed(cause)
            | Error::LiveRecoveryCoordinationUnavailable(cause) => Self {
                header: Header::new(ERROR_KIND),
                code,
                caller_code: None,
                os_code: None,
                message,
                cause: Some(Box::new((*cause).into())),
                cleanup: Vec::new(),
                cleanup_guard: None,
            },
            Error::CleanupIncomplete { cause, cleanup } => {
                let mut cause: ErrorHandle = (*cause).into();
                cause.append_cause((*cleanup).into());
                Self {
                    header: Header::new(ERROR_KIND),
                    code,
                    caller_code: None,
                    os_code: None,
                    message,
                    cause: Some(Box::new(cause)),
                    cleanup: Vec::new(),
                    cleanup_guard: None,
                }
            }
            _ => Self {
                header: Header::new(ERROR_KIND),
                code,
                caller_code: None,
                os_code: None,
                message,
                cause: None,
                cleanup: Vec::new(),
                cleanup_guard: None,
            },
        }
    }
}

impl ErrorHandle {
    pub(crate) fn from_publication_problem(problem: PublicationProblem) -> Self {
        Self {
            header: Header::new(ERROR_KIND),
            code: problem.code as u32,
            caller_code: None,
            os_code: problem.os_code.map(i64::from),
            message: problem.detail.into_owned(),
            cause: None,
            cleanup: Vec::new(),
            cleanup_guard: None,
        }
    }

    pub(crate) fn publication_failure(
        problem: PublicationProblem,
        cleanup: Vec<CleanupArtifact>,
        cleanup_guard: Option<RecoverySourceCleanupGuard>,
    ) -> Self {
        let mut error = Self::from_publication_problem(problem);
        error.cleanup = cleanup;
        error.cleanup_guard = cleanup_guard;
        error
    }

    pub(crate) fn callback_cleanup_failure(aborted: Error, primary: CallError) -> Self {
        let mut error: Self = aborted.into();
        let primary = Box::new(primary.into_error_handle());
        if let Some(primary) = error.replace_cause(ErrorCode::InvalidArgument as u32, primary) {
            error.append_cause(*primary);
        }
        error
    }

    pub(crate) fn callback_publication_failure(
        primary: CallError,
        problem: PublicationProblem,
        cleanup: Vec<CleanupArtifact>,
    ) -> Self {
        let mut error = primary.into_error_handle();
        error.cleanup = cleanup;
        error.append_cause(Self::from_publication_problem(problem));
        error
    }

    fn require(&self) -> Result<(), BoundaryError> {
        self.header.require(ERROR_KIND)
    }

    fn append_cause(&mut self, cause: ErrorHandle) {
        if let Some(next) = self.cause.as_deref_mut() {
            next.append_cause(cause);
        } else {
            self.cause = Some(Box::new(cause));
        }
    }

    fn replace_cause(
        &mut self,
        code: u32,
        mut replacement: Box<ErrorHandle>,
    ) -> Option<Box<ErrorHandle>> {
        let Some(cause) = self.cause.as_deref_mut() else {
            return Some(replacement);
        };
        if cause.code != code {
            return cause.replace_cause(code, replacement);
        }
        if let Some(trailing) = cause.cause.take() {
            replacement.append_cause(*trailing);
        }
        *cause = *replacement;
        None
    }
}

impl fmt::Display for BoundaryError {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output.write_str(&self.message)
    }
}

pub(crate) trait IntoErrorHandle {
    fn into_error_handle(self) -> ErrorHandle;
}

impl IntoErrorHandle for BoundaryError {
    fn into_error_handle(self) -> ErrorHandle {
        self.into()
    }
}

impl IntoErrorHandle for Error {
    fn into_error_handle(self) -> ErrorHandle {
        self.into()
    }
}

impl IntoErrorHandle for CallError {
    fn into_error_handle(self) -> ErrorHandle {
        match self {
            Self::Boundary(error) => error.into(),
            Self::Core(error) => error.into(),
            Self::Callback {
                code,
                caller_code,
                message,
            } => ErrorHandle {
                header: Header::new(ERROR_KIND),
                code: code as u32,
                caller_code: Some(caller_code),
                os_code: None,
                message,
                cause: None,
                cleanup: Vec::new(),
                cleanup_guard: None,
            },
            Self::Owned(error) => *error,
        }
    }
}

impl From<BoundaryError> for CallError {
    fn from(error: BoundaryError) -> Self {
        Self::Boundary(error)
    }
}

impl From<Error> for CallError {
    fn from(error: Error) -> Self {
        Self::Core(error)
    }
}

impl From<ErrorHandle> for CallError {
    fn from(error: ErrorHandle) -> Self {
        Self::Owned(Box::new(error))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn zero_length_slices_accept_null_and_large_lengths_fail() {
        // SAFETY: zero length never dereferences the null pointer.
        assert!(unsafe { input_slice::<u8>(std::ptr::null(), 0) }.is_ok());
        assert!(unsafe { input_slice::<u64>(std::ptr::null(), u64::MAX) }.is_err());
    }

    #[test]
    fn core_causes_and_os_codes_remain_inspectable() {
        let source = std::io::Error::from_raw_os_error(2);
        let error = Error::TransactionAborted(Box::new(Error::Io(source)));
        let handle: ErrorHandle = error.into();
        assert_eq!(handle.code, ErrorCode::TransactionAborted as u32);
        assert_eq!(handle.caller_code, None);
        assert_eq!(handle.cause.as_ref().unwrap().os_code, Some(2));
    }

    #[test]
    fn cleanup_failure_preserves_the_complete_original_cause_chain() {
        let error = Error::CleanupIncomplete {
            cause: Box::new(Error::TransactionAborted(Box::new(Error::Cancelled))),
            cleanup: Box::new(Error::Io(std::io::Error::from_raw_os_error(2))),
        };
        let handle: ErrorHandle = error.into();
        let transaction = handle.cause.as_deref().unwrap();
        let cancelled = transaction.cause.as_deref().unwrap();
        let cleanup = cancelled.cause.as_deref().unwrap();
        assert_eq!(transaction.code, ErrorCode::TransactionAborted as u32);
        assert_eq!(cancelled.code, ErrorCode::Cancelled as u32);
        assert_eq!(cleanup.code, ErrorCode::Io as u32);
        assert_eq!(cleanup.os_code, Some(2));
    }

    #[test]
    fn callback_cleanup_failure_preserves_callback_and_cleanup_details() {
        let aborted = Error::TransactionAborted(Box::new(Error::CleanupIncomplete {
            cause: Box::new(Error::InvalidArgument("source failed")),
            cleanup: Box::new(Error::Io(std::io::Error::from_raw_os_error(2))),
        }));
        let primary = CallError::Callback {
            code: ErrorCode::SourceFailed,
            caller_code: 42,
            message: "caller source failed".to_owned(),
        };
        let handle = ErrorHandle::callback_cleanup_failure(aborted, primary);
        let incomplete = handle.cause.as_deref().unwrap();
        let source = incomplete.cause.as_deref().unwrap();
        let cleanup = source.cause.as_deref().unwrap();
        assert_eq!(handle.code, ErrorCode::TransactionAborted as u32);
        assert_eq!(incomplete.code, ErrorCode::CleanupInProgress as u32);
        assert_eq!(source.code, ErrorCode::SourceFailed as u32);
        assert_eq!(source.caller_code, Some(42));
        assert_eq!(source.message, "caller source failed");
        assert_eq!(cleanup.code, ErrorCode::Io as u32);
        assert_eq!(cleanup.os_code, Some(2));
    }

    #[test]
    fn panics_are_captured_as_typed_failures() {
        let mut output = std::ptr::null_mut();
        let status = call(&mut output, || -> Result<(), BoundaryError> {
            panic!("controlled boundary panic")
        });
        assert_eq!(status, STATUS_ERROR);
        assert!(!output.is_null());
        // SAFETY: call returned one owned error handle.
        let output = unsafe { Box::from_raw(output) };
        assert_eq!(output.code, ErrorCode::Panic as u32);
        assert!(output.message.contains("controlled boundary panic"));
    }
}
