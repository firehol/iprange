use std::any::Any;
use std::mem::{align_of, size_of};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::ptr::NonNull;

use crate::abi::{STATUS_ERROR, STATUS_OK};
use crate::handle::{Header, ERROR_KIND};
use iprange_livedb::ErrorCode;

use super::{BoundaryError, ErrorHandle, IntoErrorHandle};

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
