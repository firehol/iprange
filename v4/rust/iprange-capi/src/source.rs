//! Synchronous bounded C range sources.

use std::ffi::c_void;
use std::mem::size_of;

use crate::abi::{CallbackFailure, CoverageSourceFn, DirectRange, DirectSourceFn, Range};
use crate::error::{input_slice, BoundaryError, CallError};

const BATCH_CAPACITY: usize = 256;
const BATCH: u32 = 1;
const END: u32 = 2;
const CALLBACK_ERROR: u32 = 3;
pub(crate) const MAX_CALLBACK_MESSAGE: u64 = 4096;

pub(crate) fn drain_coverage(
    callback: CoverageSourceFn,
    context: *mut c_void,
    mut apply: impl FnMut(&[Range]) -> Result<(), CallError>,
) -> Result<(), CallError> {
    let callback =
        callback.ok_or_else(|| BoundaryError::null("coverage source callback is null"))?;
    let mut records = [Range::default(); BATCH_CAPACITY];
    loop {
        let mut count = 0u64;
        let mut failure = CallbackFailure::default();
        // SAFETY: the callback receives one writable engine-owned batch for this call only.
        let outcome = unsafe {
            callback(
                context,
                records.as_mut_ptr(),
                records.len() as u64,
                &mut count,
                &mut failure,
            )
        };
        match validate_outcome(outcome, count, records.len(), failure, "coverage source")? {
            Outcome::Batch(count) => apply(&records[..count])?,
            Outcome::End => return Ok(()),
        }
    }
}

pub(crate) fn drain_direct(
    callback: DirectSourceFn,
    context: *mut c_void,
    mut apply: impl FnMut(&[DirectRange]) -> Result<(), CallError>,
) -> Result<(), CallError> {
    let callback = callback.ok_or_else(|| BoundaryError::null("direct source callback is null"))?;
    let mut records = [DirectRange::default(); BATCH_CAPACITY];
    loop {
        let mut count = 0u64;
        let mut failure = CallbackFailure::default();
        // SAFETY: the callback receives one writable engine-owned batch for this call only.
        let outcome = unsafe {
            callback(
                context,
                records.as_mut_ptr(),
                records.len() as u64,
                &mut count,
                &mut failure,
            )
        };
        match validate_outcome(outcome, count, records.len(), failure, "direct source")? {
            Outcome::Batch(count) => apply(&records[..count])?,
            Outcome::End => return Ok(()),
        }
    }
}

enum Outcome {
    Batch(usize),
    End,
}

fn validate_outcome(
    outcome: u32,
    count: u64,
    capacity: usize,
    failure: CallbackFailure,
    source: &'static str,
) -> Result<Outcome, CallError> {
    require_failure_identity(&failure)?;
    match outcome {
        BATCH => batch_outcome(count, capacity, &failure),
        END => end_outcome(count, &failure),
        CALLBACK_ERROR => callback_error_outcome(count, &failure, source),
        _ => Err(BoundaryError::invalid_enum("unknown source callback outcome").into()),
    }
}

fn require_failure_identity(failure: &CallbackFailure) -> Result<(), CallError> {
    if failure.abi_version == 1 && failure.struct_size == size_of::<CallbackFailure>() as u32 {
        Ok(())
    } else {
        Err(
            BoundaryError::invalid_argument("callback changed its failure structure identity")
                .into(),
        )
    }
}

fn batch_outcome(
    count: u64,
    capacity: usize,
    failure: &CallbackFailure,
) -> Result<Outcome, CallError> {
    require_empty_failure(failure)?;
    let count = usize::try_from(count)
        .map_err(|_| BoundaryError::invalid_length("callback count does not fit host"))?;
    if count == 0 || count > capacity {
        return Err(
            BoundaryError::invalid_length("callback batch count is outside 1..=capacity").into(),
        );
    }
    Ok(Outcome::Batch(count))
}

fn end_outcome(count: u64, failure: &CallbackFailure) -> Result<Outcome, CallError> {
    require_empty_failure(failure)?;
    if count != 0 {
        return Err(BoundaryError::invalid_length("callback End must return zero records").into());
    }
    Ok(Outcome::End)
}

fn callback_error_outcome(
    count: u64,
    failure: &CallbackFailure,
    source: &'static str,
) -> Result<Outcome, CallError> {
    if count != 0 {
        return Err(
            BoundaryError::invalid_length("callback Error must return zero records").into(),
        );
    }
    let message = callback_message(failure)?;
    Err(CallError::Callback {
        code: iprange_livedb::ErrorCode::SourceFailed,
        caller_code: failure.caller_code,
        message: format!("{source} failed: {message}"),
    })
}

pub(crate) fn require_empty_failure(failure: &CallbackFailure) -> Result<(), CallError> {
    if failure.caller_code == 0 && failure.message_pointer.is_null() && failure.message_length == 0
    {
        Ok(())
    } else {
        Err(BoundaryError::reserved("callback failure fields must be zero on Batch or End").into())
    }
}

pub(crate) fn callback_message(failure: &CallbackFailure) -> Result<String, CallError> {
    if failure.message_length > MAX_CALLBACK_MESSAGE {
        return Err(
            BoundaryError::invalid_length("callback failure message exceeds 4096 bytes").into(),
        );
    }
    // SAFETY: the callback promises the message remains readable until it returns;
    // this function copies it before the ABI invokes anything else.
    let bytes = unsafe { input_slice(failure.message_pointer, failure.message_length)? };
    Ok(std::str::from_utf8(bytes)
        .unwrap_or("callback supplied invalid UTF-8")
        .to_owned())
}
