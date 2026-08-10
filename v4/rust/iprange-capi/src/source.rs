//! Synchronous bounded C range sources.

use std::ffi::c_void;
use std::mem::size_of;

use iprange_livedb::{AddressRange, Ipv4Key, Ipv6Key, RangeSource};

use crate::abi::{
    CallbackFailure, CoverageSourceFn, DirectRange as AbiDirectRange, DirectSourceFn, Range,
};
use crate::error::{input_slice, BoundaryError, CallError};
use crate::ip::{self, Key};

pub(crate) const BATCH_CAPACITY: usize = 256;
const BATCH: u32 = 1;
const END: u32 = 2;
const CALLBACK_ERROR: u32 = 3;
pub(crate) const MAX_CALLBACK_MESSAGE: u64 = 4096;

pub(crate) fn drain_coverage(
    callback: CoverageSourceFn,
    context: *mut c_void,
    mut apply: impl FnMut(&[Range]) -> Result<(), CallError>,
) -> Result<(), CallError> {
    let mut records = [Range::default(); BATCH_CAPACITY];
    loop {
        match next_coverage(callback, context, &mut records)? {
            Some(count) => apply(&records[..count])?,
            None => return Ok(()),
        }
    }
}

pub(crate) fn drain_direct(
    callback: DirectSourceFn,
    context: *mut c_void,
    mut apply: impl FnMut(&[AbiDirectRange]) -> Result<(), CallError>,
) -> Result<(), CallError> {
    let mut records = [AbiDirectRange::default(); BATCH_CAPACITY];
    loop {
        match next_direct(callback, context, &mut records)? {
            Some(count) => apply(&records[..count])?,
            None => return Ok(()),
        }
    }
}

pub(crate) fn next_coverage(
    callback: CoverageSourceFn,
    context: *mut c_void,
    records: &mut [Range],
) -> Result<Option<usize>, CallError> {
    next(
        callback,
        context,
        records,
        "coverage source callback is null",
        "coverage source",
    )
}

pub(crate) fn next_direct(
    callback: DirectSourceFn,
    context: *mut c_void,
    records: &mut [AbiDirectRange],
) -> Result<Option<usize>, CallError> {
    next(
        callback,
        context,
        records,
        "direct source callback is null",
        "direct source",
    )
}

pub(crate) struct CoverageV4 {
    callback: CoverageSourceFn,
    context: *mut c_void,
    input: [Range; BATCH_CAPACITY],
    output: [AddressRange<Ipv4Key>; BATCH_CAPACITY],
    failure: Option<CallError>,
    finished: bool,
}

pub(crate) struct CoverageV6 {
    callback: CoverageSourceFn,
    context: *mut c_void,
    input: [Range; BATCH_CAPACITY],
    output: [AddressRange<Ipv6Key>; BATCH_CAPACITY],
    failure: Option<CallError>,
    finished: bool,
}

pub(crate) struct DirectV4 {
    callback: DirectSourceFn,
    context: *mut c_void,
    input: [AbiDirectRange; BATCH_CAPACITY],
    output: [iprange_livedb::DirectRange<Ipv4Key>; BATCH_CAPACITY],
    failure: Option<CallError>,
    finished: bool,
}

pub(crate) struct DirectV6 {
    callback: DirectSourceFn,
    context: *mut c_void,
    input: [AbiDirectRange; BATCH_CAPACITY],
    output: [iprange_livedb::DirectRange<Ipv6Key>; BATCH_CAPACITY],
    failure: Option<CallError>,
    finished: bool,
}

impl CoverageV4 {
    pub(crate) fn new(callback: CoverageSourceFn, context: *mut c_void) -> Self {
        Self {
            callback,
            context,
            input: [Range::default(); BATCH_CAPACITY],
            output: [AddressRange {
                from: Ipv4Key::MIN,
                to: Ipv4Key::MIN,
            }; BATCH_CAPACITY],
            failure: None,
            finished: false,
        }
    }

    pub(crate) fn take_failure(&mut self) -> Option<CallError> {
        self.failure.take()
    }
}

impl CoverageV6 {
    pub(crate) fn new(callback: CoverageSourceFn, context: *mut c_void) -> Self {
        Self {
            callback,
            context,
            input: [Range::default(); BATCH_CAPACITY],
            output: [AddressRange {
                from: Ipv6Key::MIN,
                to: Ipv6Key::MIN,
            }; BATCH_CAPACITY],
            failure: None,
            finished: false,
        }
    }

    pub(crate) fn take_failure(&mut self) -> Option<CallError> {
        self.failure.take()
    }
}

impl DirectV4 {
    pub(crate) fn new(callback: DirectSourceFn, context: *mut c_void) -> Self {
        Self {
            callback,
            context,
            input: [AbiDirectRange::default(); BATCH_CAPACITY],
            output: [iprange_livedb::DirectRange {
                from: Ipv4Key::MIN,
                to: Ipv4Key::MIN,
                value: 0,
            }; BATCH_CAPACITY],
            failure: None,
            finished: false,
        }
    }

    pub(crate) fn take_failure(&mut self) -> Option<CallError> {
        self.failure.take()
    }
}

impl DirectV6 {
    pub(crate) fn new(callback: DirectSourceFn, context: *mut c_void) -> Self {
        Self {
            callback,
            context,
            input: [AbiDirectRange::default(); BATCH_CAPACITY],
            output: [iprange_livedb::DirectRange {
                from: Ipv6Key::MIN,
                to: Ipv6Key::MIN,
                value: 0,
            }; BATCH_CAPACITY],
            failure: None,
            finished: false,
        }
    }

    pub(crate) fn take_failure(&mut self) -> Option<CallError> {
        self.failure.take()
    }
}

impl RangeSource<AddressRange<Ipv4Key>> for CoverageV4 {
    fn next_batch(&mut self) -> iprange_livedb::Result<Option<&[AddressRange<Ipv4Key>]>> {
        let count = match coverage_count(self)? {
            Some(count) => count,
            None => return Ok(None),
        };
        for index in 0..count {
            self.output[index] = match ip::decode_range(self.input[index]) {
                Ok((Key::V4(from), Key::V4(to))) => AddressRange { from, to },
                Ok(_) => {
                    return Err(record_failure(
                        &mut self.failure,
                        BoundaryError::wrong_family("coverage source range is not IPv4").into(),
                    ));
                }
                Err(error) => return Err(record_failure(&mut self.failure, error.into())),
            };
        }
        Ok(Some(&self.output[..count]))
    }
}

impl RangeSource<AddressRange<Ipv6Key>> for CoverageV6 {
    fn next_batch(&mut self) -> iprange_livedb::Result<Option<&[AddressRange<Ipv6Key>]>> {
        let count = match coverage_count(self)? {
            Some(count) => count,
            None => return Ok(None),
        };
        for index in 0..count {
            self.output[index] = match ip::decode_range(self.input[index]) {
                Ok((Key::V6(from), Key::V6(to))) => AddressRange { from, to },
                Ok(_) => {
                    return Err(record_failure(
                        &mut self.failure,
                        BoundaryError::wrong_family("coverage source range is not IPv6").into(),
                    ));
                }
                Err(error) => return Err(record_failure(&mut self.failure, error.into())),
            };
        }
        Ok(Some(&self.output[..count]))
    }
}

impl RangeSource<iprange_livedb::DirectRange<Ipv4Key>> for DirectV4 {
    fn next_batch(
        &mut self,
    ) -> iprange_livedb::Result<Option<&[iprange_livedb::DirectRange<Ipv4Key>]>> {
        let count = match direct_count(self)? {
            Some(count) => count,
            None => return Ok(None),
        };
        for index in 0..count {
            self.output[index] = decode_direct_v4(self.input[index])
                .map_err(|error| record_failure(&mut self.failure, error))?;
        }
        Ok(Some(&self.output[..count]))
    }
}

impl RangeSource<iprange_livedb::DirectRange<Ipv6Key>> for DirectV6 {
    fn next_batch(
        &mut self,
    ) -> iprange_livedb::Result<Option<&[iprange_livedb::DirectRange<Ipv6Key>]>> {
        let count = match direct_count(self)? {
            Some(count) => count,
            None => return Ok(None),
        };
        for index in 0..count {
            self.output[index] = decode_direct_v6(self.input[index])
                .map_err(|error| record_failure(&mut self.failure, error))?;
        }
        Ok(Some(&self.output[..count]))
    }
}

fn coverage_count<T>(source: &mut T) -> iprange_livedb::Result<Option<usize>>
where
    T: CoverageAdapter,
{
    if source.finished() {
        return Ok(None);
    }
    match next_coverage(source.callback(), source.context(), source.input()) {
        Ok(Some(count)) => Ok(Some(count)),
        Ok(None) => {
            source.set_finished();
            Ok(None)
        }
        Err(error) => Err(record_failure(source.failure(), error)),
    }
}

fn direct_count<T>(source: &mut T) -> iprange_livedb::Result<Option<usize>>
where
    T: DirectAdapter,
{
    if source.finished() {
        return Ok(None);
    }
    match next_direct(source.callback(), source.context(), source.input()) {
        Ok(Some(count)) => Ok(Some(count)),
        Ok(None) => {
            source.set_finished();
            Ok(None)
        }
        Err(error) => Err(record_failure(source.failure(), error)),
    }
}

trait CoverageAdapter {
    fn callback(&self) -> CoverageSourceFn;
    fn context(&self) -> *mut c_void;
    fn input(&mut self) -> &mut [Range];
    fn failure(&mut self) -> &mut Option<CallError>;
    fn finished(&self) -> bool;
    fn set_finished(&mut self);
}

trait DirectAdapter {
    fn callback(&self) -> DirectSourceFn;
    fn context(&self) -> *mut c_void;
    fn input(&mut self) -> &mut [AbiDirectRange];
    fn failure(&mut self) -> &mut Option<CallError>;
    fn finished(&self) -> bool;
    fn set_finished(&mut self);
}

macro_rules! adapter {
    ($type:ty, $trait:ident, $callback:ty, $input:ty) => {
        impl $trait for $type {
            fn callback(&self) -> $callback {
                self.callback
            }

            fn context(&self) -> *mut c_void {
                self.context
            }

            fn input(&mut self) -> &mut [$input] {
                &mut self.input
            }

            fn failure(&mut self) -> &mut Option<CallError> {
                &mut self.failure
            }

            fn finished(&self) -> bool {
                self.finished
            }

            fn set_finished(&mut self) {
                self.finished = true;
            }
        }
    };
}

adapter!(CoverageV4, CoverageAdapter, CoverageSourceFn, Range);
adapter!(CoverageV6, CoverageAdapter, CoverageSourceFn, Range);
adapter!(DirectV4, DirectAdapter, DirectSourceFn, AbiDirectRange);
adapter!(DirectV6, DirectAdapter, DirectSourceFn, AbiDirectRange);

fn decode_direct_v4(
    input: AbiDirectRange,
) -> Result<iprange_livedb::DirectRange<Ipv4Key>, CallError> {
    require_direct_reserved(input)?;
    match ip::decode_range(input.range)? {
        (Key::V4(from), Key::V4(to)) => Ok(iprange_livedb::DirectRange {
            from,
            to,
            value: input.value,
        }),
        _ => Err(BoundaryError::wrong_family("direct source range is not IPv4").into()),
    }
}

fn decode_direct_v6(
    input: AbiDirectRange,
) -> Result<iprange_livedb::DirectRange<Ipv6Key>, CallError> {
    require_direct_reserved(input)?;
    match ip::decode_range(input.range)? {
        (Key::V6(from), Key::V6(to)) => Ok(iprange_livedb::DirectRange {
            from,
            to,
            value: input.value,
        }),
        _ => Err(BoundaryError::wrong_family("direct source range is not IPv6").into()),
    }
}

fn require_direct_reserved(input: AbiDirectRange) -> Result<(), CallError> {
    if input.reserved == 0 {
        Ok(())
    } else {
        Err(BoundaryError::reserved("direct range reserved field is nonzero").into())
    }
}

fn record_failure(slot: &mut Option<CallError>, error: CallError) -> iprange_livedb::Error {
    *slot = Some(error);
    iprange_livedb::Error::InvalidArgument("C range source failed")
}

fn next<T>(
    callback: Option<
        unsafe extern "C" fn(*mut c_void, *mut T, u64, *mut u64, *mut CallbackFailure) -> u32,
    >,
    context: *mut c_void,
    records: &mut [T],
    null_message: &'static str,
    label: &'static str,
) -> Result<Option<usize>, CallError> {
    let callback = callback.ok_or_else(|| BoundaryError::null(null_message))?;
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
    Ok(
        match validate_outcome(outcome, count, records.len(), failure, label)? {
            Outcome::Batch(count) => Some(count),
            Outcome::End => None,
        },
    )
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
