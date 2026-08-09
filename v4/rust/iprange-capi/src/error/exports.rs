use crate::abi::{CleanupArtifact, MutableByteSlice, STATUS_OK};
use crate::obligation::CleanupGuardHandle;

use super::{
    call, call_with_outputs, output_buffer_slot, output_slice, output_slot, required_output,
    BoundaryError, ErrorHandle,
};

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
