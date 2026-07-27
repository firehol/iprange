//! Typed failures, panic containment, and pointer validation.

use std::any::Any;
use std::fmt;
use std::mem::{align_of, size_of};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::ptr::NonNull;

use iprange_livedb::publication::PublicationProblem;
use iprange_livedb::recovery::RecoverySourceCleanupGuard;
use iprange_livedb::{Error, ErrorCode};

use crate::abi::{CleanupArtifact, MutableByteSlice, STATUS_ERROR, STATUS_OK};
use crate::handle::{Header, OpaqueHandle, ERROR_KIND};
use crate::obligation::CleanupGuardHandle;

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
            message: problem.detail.to_owned(),
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

    pub(crate) fn source_cleanup_failure(aborted: Error, primary: CallError) -> Self {
        let mut error: Self = aborted.into();
        let primary = Box::new(primary.into_error_handle());
        if let Some(primary) = error.replace_cause(ErrorCode::InvalidArgument as u32, primary) {
            error.append_cause(*primary);
        }
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

pub(crate) fn call<E>(
    error_output: *mut *mut ErrorHandle,
    operation: impl FnOnce() -> Result<(), E>,
) -> u32
where
    E: IntoErrorHandle,
{
    call_with_outputs(error_output, &[], operation)
}

pub(crate) fn call_with_outputs<E>(
    error_output: *mut *mut ErrorHandle,
    outputs: &[OutputSlot],
    operation: impl FnOnce() -> Result<(), E>,
) -> u32
where
    E: IntoErrorHandle,
{
    match catch_unwind(AssertUnwindSafe(|| {
        // SAFETY: slots describe caller-provided output storage.
        unsafe { validate_and_clear_outputs(error_output, outputs)? };
        Ok::<_, BoundaryError>(operation())
    })) {
        Ok(Ok(Ok(()))) => STATUS_OK,
        Ok(Ok(Err(error))) => store_error(error_output, error.into_error_handle()),
        Ok(Err(error)) => store_error(error_output, error.into()),
        Err(payload) => store_error(error_output, panic_error(payload)),
    }
}

pub(crate) fn call_with_output<E, T>(
    error_output: *mut *mut ErrorHandle,
    output: *mut T,
    name: &'static str,
    operation: impl FnOnce() -> Result<(), E>,
) -> u32
where
    E: IntoErrorHandle,
{
    call_with_outputs(error_output, &[output_slot(output, name)], operation)
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct OutputSlot {
    pointer: *mut u8,
    count: u64,
    element_size: usize,
    alignment: usize,
    name: &'static str,
    clear: bool,
    empty_may_be_null: bool,
}

pub(crate) fn output_slot<T>(pointer: *mut T, name: &'static str) -> OutputSlot {
    OutputSlot {
        pointer: pointer.cast(),
        count: 1,
        element_size: size_of::<T>(),
        alignment: align_of::<T>(),
        name,
        clear: true,
        empty_may_be_null: false,
    }
}

pub(crate) fn output_buffer_slot<T>(pointer: *mut T, count: u64, name: &'static str) -> OutputSlot {
    OutputSlot {
        pointer: pointer.cast(),
        count,
        element_size: size_of::<T>(),
        alignment: align_of::<T>(),
        name,
        clear: false,
        empty_may_be_null: true,
    }
}

fn panic_error(payload: Box<dyn Any + Send>) -> ErrorHandle {
    let detail = payload
        .downcast_ref::<&str>()
        .copied()
        .or_else(|| payload.downcast_ref::<String>().map(String::as_str))
        .unwrap_or("Rust panic at the C ABI boundary");
    ErrorHandle {
        header: Header::new(ERROR_KIND),
        code: ErrorCode::Panic as u32,
        caller_code: None,
        os_code: None,
        message: detail.to_owned(),
        cause: None,
        cleanup: Vec::new(),
        cleanup_guard: None,
    }
}

#[doc(hidden)]
#[cfg(debug_assertions)]
pub fn native_test_panic_probe() -> (u32, u32) {
    let mut error = std::ptr::null_mut();
    let status = call(&mut error, || -> Result<(), BoundaryError> {
        panic!("native C panic-containment probe")
    });
    // SAFETY: call returned one owned error handle for the controlled panic.
    let error = unsafe { Box::from_raw(error) };
    (status, error.code)
}

unsafe fn validate_and_clear_outputs(
    error_output: *mut *mut ErrorHandle,
    outputs: &[OutputSlot],
) -> Result<(), BoundaryError> {
    let error_slot =
        (!error_output.is_null()).then(|| output_slot(error_output, "error output is invalid"));
    validate_output_slots(error_slot, outputs)?;
    reject_output_overlaps(error_slot, outputs)?;
    // SAFETY: every output slot is valid and disjoint.
    unsafe { clear_output_slots(error_output, outputs) }
}

fn validate_output_slots(
    error_slot: Option<OutputSlot>,
    outputs: &[OutputSlot],
) -> Result<(), BoundaryError> {
    if let Some(slot) = error_slot {
        validate_output_slot(slot)?;
    }
    for &slot in outputs {
        validate_output_slot(slot)?;
    }
    Ok(())
}

fn reject_output_overlaps(
    error_slot: Option<OutputSlot>,
    outputs: &[OutputSlot],
) -> Result<(), BoundaryError> {
    for left in 0..outputs.len() {
        if let Some(error) = error_slot {
            reject_output_overlap(error, outputs[left])?;
        }
        for right in (left + 1)..outputs.len() {
            reject_output_overlap(outputs[left], outputs[right])?;
        }
    }
    Ok(())
}

unsafe fn clear_output_slots(
    error_output: *mut *mut ErrorHandle,
    outputs: &[OutputSlot],
) -> Result<(), BoundaryError> {
    if !error_output.is_null() {
        // SAFETY: the optional output slot was validated and does not overlap another output.
        unsafe { error_output.write(std::ptr::null_mut()) };
    }
    for &slot in outputs {
        if slot.clear {
            // SAFETY: all required slots were validated and proven disjoint.
            unsafe { slot.pointer.write_bytes(0, slot.byte_size()?) };
        }
    }
    Ok(())
}

fn validate_output_slot(slot: OutputSlot) -> Result<(), BoundaryError> {
    let size = slot.byte_size()?;
    if size == 0 && slot.empty_may_be_null {
        return Ok(());
    }
    if slot.pointer.is_null() {
        return Err(BoundaryError::null(slot.name));
    }
    if (slot.pointer as usize) % slot.alignment != 0 {
        return Err(BoundaryError::misaligned(
            "required output pointer is misaligned",
        ));
    }
    (slot.pointer as usize)
        .checked_add(size)
        .ok_or_else(|| BoundaryError::invalid_length("output extent overflows"))?;
    Ok(())
}

fn reject_output_overlap(left: OutputSlot, right: OutputSlot) -> Result<(), BoundaryError> {
    let left_size = left.byte_size()?;
    let right_size = right.byte_size()?;
    if left_size == 0 || right_size == 0 {
        return Ok(());
    }
    let left_start = left.pointer as usize;
    let left_end = left_start + left_size;
    let right_start = right.pointer as usize;
    let right_end = right_start + right_size;
    if left_start < right_end && right_start < left_end {
        Err(BoundaryError::invalid_argument(
            "output pointers must not overlap",
        ))
    } else {
        Ok(())
    }
}

impl OutputSlot {
    fn byte_size(self) -> Result<usize, BoundaryError> {
        let count = usize::try_from(self.count)
            .map_err(|_| BoundaryError::invalid_length("output length does not fit this host"))?;
        let bytes = count
            .checked_mul(self.element_size)
            .ok_or_else(|| BoundaryError::invalid_length("output byte length overflows"))?;
        if bytes > isize::MAX as usize {
            return Err(BoundaryError::invalid_length(
                "output byte length exceeds the host object limit",
            ));
        }
        Ok(bytes)
    }
}

fn store_error(output: *mut *mut ErrorHandle, error: ErrorHandle) -> u32 {
    if !output.is_null() && (output as usize) % align_of::<*mut ErrorHandle>() == 0 {
        // SAFETY: the optional output slot was validated before semantic work.
        unsafe { output.write(Box::into_raw(Box::new(error))) };
    }
    STATUS_ERROR
}

pub(crate) unsafe fn input_slice<'a, T>(
    pointer: *const T,
    length: u64,
) -> Result<&'a [T], BoundaryError> {
    let length = checked_length::<T>(length)?;
    if length == 0 {
        return Ok(&[]);
    }
    if pointer.is_null() {
        return Err(BoundaryError::null("input pointer is null"));
    }
    require_aligned(pointer, "input pointer is misaligned")?;
    // SAFETY: the caller contract supplies readable storage for the checked extent.
    Ok(unsafe { std::slice::from_raw_parts(pointer, length) })
}

pub(crate) unsafe fn output_slice<'a, T>(
    pointer: *mut T,
    length: u64,
) -> Result<&'a mut [T], BoundaryError> {
    let length = checked_length::<T>(length)?;
    if length == 0 {
        // SAFETY: a dangling aligned pointer is valid for an empty slice.
        return Ok(unsafe { std::slice::from_raw_parts_mut(NonNull::<T>::dangling().as_ptr(), 0) });
    }
    if pointer.is_null() {
        return Err(BoundaryError::null("output pointer is null"));
    }
    require_aligned(pointer, "output pointer is misaligned")?;
    // SAFETY: the caller contract supplies writable storage for the checked extent.
    Ok(unsafe { std::slice::from_raw_parts_mut(pointer, length) })
}

fn checked_length<T>(length: u64) -> Result<usize, BoundaryError> {
    let length = usize::try_from(length)
        .map_err(|_| BoundaryError::invalid_length("length does not fit this host"))?;
    let bytes = length
        .checked_mul(size_of::<T>())
        .ok_or_else(|| BoundaryError::invalid_length("slice byte length overflows"))?;
    if bytes > isize::MAX as usize {
        return Err(BoundaryError::invalid_length(
            "slice byte length exceeds the host object limit",
        ));
    }
    Ok(length)
}

fn require_aligned<T>(pointer: *const T, message: &'static str) -> Result<(), BoundaryError> {
    if (pointer as usize) % align_of::<T>() == 0 {
        Ok(())
    } else {
        Err(BoundaryError::misaligned(message))
    }
}

pub(crate) unsafe fn required_output<'a, T>(
    pointer: *mut T,
    name: &'static str,
) -> Result<&'a mut T, BoundaryError> {
    if pointer.is_null() {
        return Err(BoundaryError::null(name));
    }
    require_aligned(pointer, "required output pointer is misaligned")?;
    // SAFETY: public ABI output types have a valid all-zero representation.
    unsafe { pointer.write_bytes(0, 1) };
    // SAFETY: the caller supplies one writable, aligned object initialized above.
    Ok(unsafe { &mut *pointer })
}

pub(crate) unsafe fn required_input<'a, T>(
    pointer: *const T,
    name: &'static str,
) -> Result<&'a T, BoundaryError> {
    if pointer.is_null() {
        return Err(BoundaryError::null(name));
    }
    require_aligned(pointer, "required input pointer is misaligned")?;
    // SAFETY: the caller supplies one readable, aligned object.
    Ok(unsafe { &*pointer })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_error_code(
    error: *const ErrorHandle,
    code: *mut u32,
    caller_code_present: *mut u8,
    caller_code: *mut u64,
) -> u32 {
    call_with_outputs(
        std::ptr::null_mut(),
        &[
            output_slot(code, "error code output is null"),
            output_slot(caller_code_present, "caller-code presence output is null"),
            output_slot(caller_code, "caller-code output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before dereference.
            let error =
                unsafe { crate::handle::required_handle_input(error, "error handle is null")? };
            error.require()?;
            // SAFETY: the output pointer is validated before dereference.
            let code = unsafe { required_output(code, "error code output is null")? };
            let caller_code_present = unsafe {
                required_output(caller_code_present, "caller-code presence output is null")?
            };
            let caller_code =
                unsafe { required_output(caller_code, "caller-code output is null")? };
            *code = error.code;
            *caller_code_present = u8::from(error.caller_code.is_some());
            *caller_code = error.caller_code.unwrap_or(0);
            Ok::<_, BoundaryError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_error_os_code(
    error: *const ErrorHandle,
    present: *mut u8,
    code: *mut i64,
) -> u32 {
    call_with_outputs(
        std::ptr::null_mut(),
        &[
            output_slot(present, "presence output is null"),
            output_slot(code, "OS code output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before dereference.
            let error =
                unsafe { crate::handle::required_handle_input(error, "error handle is null")? };
            error.require()?;
            let present = unsafe { required_output(present, "presence output is null")? };
            let code = unsafe { required_output(code, "OS code output is null")? };
            *present = u8::from(error.os_code.is_some());
            *code = error.os_code.unwrap_or(0);
            Ok::<_, BoundaryError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_error_message_query(
    error: *const ErrorHandle,
    required: *mut u64,
) -> u32 {
    call_with_outputs(
        std::ptr::null_mut(),
        &[output_slot(required, "required length output is null")],
        || {
            // SAFETY: both pointers are validated before dereference.
            let error =
                unsafe { crate::handle::required_handle_input(error, "error handle is null")? };
            error.require()?;
            let required = unsafe { required_output(required, "required length output is null")? };
            *required = error.message.len() as u64;
            Ok::<_, BoundaryError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_error_message_read(
    error: *const ErrorHandle,
    output: MutableByteSlice,
    required: *mut u64,
) -> u32 {
    call_with_outputs(
        std::ptr::null_mut(),
        &[
            output_buffer_slot(output.pointer, output.length, "message output is invalid"),
            output_slot(required, "required length output is null"),
        ],
        || {
            // SAFETY: pointers and extents are validated before use.
            let error =
                unsafe { crate::handle::required_handle_input(error, "error handle is null")? };
            error.require()?;
            let required = unsafe { required_output(required, "required length output is null")? };
            *required = error.message.len() as u64;
            if output.length < *required {
                return Err(BoundaryError::buffer_too_small(*required));
            }
            let output = unsafe { output_slice(output.pointer, output.length)? };
            output[..error.message.len()].copy_from_slice(error.message.as_bytes());
            Ok::<_, BoundaryError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_error_cause(
    error: *const ErrorHandle,
    cause: *mut *const ErrorHandle,
) -> u32 {
    call_with_outputs(
        std::ptr::null_mut(),
        &[output_slot(cause, "cause output is null")],
        || {
            // SAFETY: both pointers are validated before dereference.
            let error =
                unsafe { crate::handle::required_handle_input(error, "error handle is null")? };
            error.require()?;
            let cause = unsafe { required_output(cause, "cause output is null")? };
            *cause = error
                .cause
                .as_deref()
                .map_or(std::ptr::null(), |cause| cause);
            Ok::<_, BoundaryError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_error_cleanup_artifact_count(
    error: *const ErrorHandle,
    count: *mut u64,
) -> u32 {
    call_with_outputs(
        std::ptr::null_mut(),
        &[output_slot(count, "artifact count output is null")],
        || {
            // SAFETY: pointers are validated before use.
            let error =
                unsafe { crate::handle::required_handle_input(error, "error handle is null")? };
            error.require()?;
            let count = unsafe { required_output(count, "artifact count output is null")? };
            *count = error.cleanup.len() as u64;
            Ok::<_, BoundaryError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_error_cleanup_artifact_get(
    error: *const ErrorHandle,
    index: u64,
    output: *mut CleanupArtifact,
) -> u32 {
    call_with_outputs(
        std::ptr::null_mut(),
        &[output_slot(output, "cleanup artifact output is null")],
        || {
            // SAFETY: pointers are validated before use.
            let error =
                unsafe { crate::handle::required_handle_input(error, "error handle is null")? };
            error.require()?;
            let output = unsafe { required_output(output, "cleanup artifact output is null")? };
            *output = CleanupArtifact::default();
            *output = *error.cleanup.get(index as usize).ok_or_else(|| {
                BoundaryError::invalid_argument("cleanup artifact index is invalid")
            })?;
            Ok::<_, BoundaryError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_error_take_cleanup_guard(
    error: *mut ErrorHandle,
    guard_output: *mut *mut CleanupGuardHandle,
) -> u32 {
    call_with_outputs(
        std::ptr::null_mut(),
        &[output_slot(guard_output, "cleanup guard output is null")],
        || {
            // SAFETY: pointers are validated before use.
            let error =
                unsafe { crate::handle::required_handle_output(error, "error handle is null")? };
            error.require()?;
            let output = unsafe { required_output(guard_output, "cleanup guard output is null")? };
            *output = std::ptr::null_mut();
            let guard = error
                .cleanup_guard
                .take()
                .ok_or_else(|| BoundaryError::wrong_state("error has no cleanup guard"))?;
            *output = Box::into_raw(Box::new(CleanupGuardHandle::new(guard)));
            Ok::<_, BoundaryError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_error_destroy(error: *mut ErrorHandle) -> u32 {
    if error.is_null() {
        return STATUS_OK;
    }
    call(std::ptr::null_mut(), || {
        // SAFETY: the handle is validated before ownership is consumed.
        let value = unsafe { crate::handle::required_handle_input(error, "error handle is null")? };
        value.require()?;
        if value.cleanup_guard.is_some() {
            return Err(BoundaryError::handle_busy(
                "error still owns an untaken cleanup guard",
            ));
        }
        // SAFETY: ownership was created by this ABI and is consumed exactly once.
        unsafe { drop(Box::from_raw(error)) };
        Ok::<_, BoundaryError>(())
    })
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
    fn source_cleanup_failure_preserves_callback_and_cleanup_details() {
        let aborted = Error::TransactionAborted(Box::new(Error::CleanupIncomplete {
            cause: Box::new(Error::InvalidArgument("source failed")),
            cleanup: Box::new(Error::Io(std::io::Error::from_raw_os_error(2))),
        }));
        let primary = CallError::Callback {
            code: ErrorCode::SourceFailed,
            caller_code: 42,
            message: "caller source failed".to_owned(),
        };
        let handle = ErrorHandle::source_cleanup_failure(aborted, primary);
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
