//! Synchronous borrowed C output batches.

use std::ffi::c_void;
use std::mem::size_of;

use crate::abi::{
    CallbackFailure, CoverageSinkFn, DirectRange, DirectSinkFn, FeedInfo, FeedSinkFn,
    MembershipRange, MembershipSinkFn, Range,
};
use crate::error::{BoundaryError, CallError};
use crate::source::{callback_message, require_empty_failure};

const CONTINUE: u32 = 1;
const STOP: u32 = 2;
const CALLBACK_ERROR: u32 = 3;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Control {
    Continue,
    Stop,
}

macro_rules! sink {
    ($name:ident, $callback:ty, $record:ty, $label:literal) => {
        pub(crate) fn $name(
            callback: $callback,
            context: *mut c_void,
            records: &[$record],
        ) -> Result<Control, CallError> {
            let callback =
                callback.ok_or_else(|| BoundaryError::null(concat!($label, " is null")))?;
            if records.is_empty() {
                return Err(BoundaryError::invalid_length(
                    "sink batch must contain at least one record",
                )
                .into());
            }
            let mut failure = CallbackFailure::default();
            // SAFETY: the records and failure output remain valid for this callback only.
            let outcome = unsafe {
                callback(
                    context,
                    records.as_ptr(),
                    records.len() as u64,
                    &mut failure,
                )
            };
            validate(outcome, failure, $label)
        }
    };
}

sink!(coverage, CoverageSinkFn, Range, "coverage sink");
sink!(direct, DirectSinkFn, DirectRange, "direct sink");
sink!(
    membership,
    MembershipSinkFn,
    MembershipRange,
    "membership sink"
);
sink!(feed, FeedSinkFn, FeedInfo, "feed sink");

pub(crate) fn records<T>(
    callback: Option<unsafe extern "C" fn(*mut c_void, *const T, u64, *mut CallbackFailure) -> u32>,
    context: *mut c_void,
    records: &[T],
    label: &'static str,
) -> Result<Control, CallError> {
    let callback = callback.ok_or_else(|| BoundaryError::null("sink callback is null"))?;
    if records.is_empty() {
        return Err(
            BoundaryError::invalid_length("sink batch must contain at least one record").into(),
        );
    }
    let mut failure = CallbackFailure::default();
    // SAFETY: the borrowed records and failure output live through this call only.
    let outcome = unsafe {
        callback(
            context,
            records.as_ptr(),
            records.len() as u64,
            &mut failure,
        )
    };
    validate(outcome, failure, label)
}

fn validate(
    outcome: u32,
    failure: CallbackFailure,
    label: &'static str,
) -> Result<Control, CallError> {
    if failure.abi_version != 1 || failure.struct_size != size_of::<CallbackFailure>() as u32 {
        return Err(BoundaryError::invalid_argument(
            "callback changed its failure structure identity",
        )
        .into());
    }
    match outcome {
        CONTINUE => {
            require_empty_failure(&failure)?;
            Ok(Control::Continue)
        }
        STOP => {
            require_empty_failure(&failure)?;
            Ok(Control::Stop)
        }
        CALLBACK_ERROR => {
            let message = callback_message(&failure)?;
            Err(CallError::Callback {
                code: iprange_livedb::ErrorCode::SinkFailed,
                caller_code: failure.caller_code,
                message: format!("{label} failed: {message}"),
            })
        }
        _ => Err(BoundaryError::invalid_enum("unknown sink callback outcome").into()),
    }
}
