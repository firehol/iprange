//! Stable Phase-1 error classification.

use core::fmt;

/// Stable semantic error codes shared by Rust, Go, and the C ABI.
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
    LiveCoordinationDomainMismatchRequiresReset = 46,
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

/// One typed SDK failure. `detail` is implementation-owned text and never
/// contains unescaped attacker-controlled file bytes.
#[derive(Debug)]
pub struct Error {
    code: ErrorCode,
    detail: &'static str,
    #[cfg(feature = "std")]
    source: Option<std::io::Error>,
}

impl Error {
    #[allow(dead_code)] // Used by the exact reader in the next implementation chunk.
    pub(crate) const fn new(code: ErrorCode, detail: &'static str) -> Self {
        Self {
            code,
            detail,
            #[cfg(feature = "std")]
            source: None,
        }
    }

    #[inline]
    pub const fn code(&self) -> ErrorCode {
        self.code
    }

    #[inline]
    pub const fn detail(&self) -> &'static str {
        self.detail
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{:?}: {}", self.code, self.detail)
    }
}

#[cfg(feature = "std")]
impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        self.source
            .as_ref()
            .map(|source| source as &(dyn std::error::Error + 'static))
    }
}

#[cfg(feature = "std")]
impl From<std::io::Error> for Error {
    fn from(source: std::io::Error) -> Self {
        Self {
            code: ErrorCode::Io,
            detail: "operating-system I/O failure",
            source: Some(source),
        }
    }
}

pub type Result<T> = core::result::Result<T, Error>;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stable_numeric_registry_is_contiguous() {
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
            ErrorCode::LiveCoordinationDomainMismatchRequiresReset,
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
