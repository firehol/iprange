//! Writer metadata, advanced direct input, and transaction termination.

use std::ffi::c_void;

use iprange_livedb::{CancellationToken, Error};

use crate::abi::{Cancellation, CoverageSourceFn, DirectSourceFn, MutableByteSlice};
use crate::callback;
use crate::error::{
    call, call_with_output, call_with_outputs, output_buffer_slot, output_slice, output_slot,
    required_output, BoundaryError, CallError, ErrorHandle,
};
use crate::handle::WriterHandle;
use crate::ip::{self, Key};
use crate::report::ReportHandle;
use crate::source;

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_metadata_query(
    writer: *const WriterHandle,
    present: *mut u8,
    required: *mut u64,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(present, "presence output is null"),
            output_slot(required, "required length output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before use.
            let writer =
                unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
            let present = unsafe { required_output(present, "presence output is null")? };
            let required = unsafe { required_output(required, "required length output is null")? };
            writer.with_mut(|writer| {
                let length = writer.metadata_json_len()?;
                *present = u8::from(length.is_some());
                *required = length.unwrap_or(0);
                Ok(())
            })
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_metadata_read(
    writer: *const WriterHandle,
    output: MutableByteSlice,
    required: *mut u64,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_buffer_slot(output.pointer, output.length, "metadata output is invalid"),
            output_slot(required, "required length output is null"),
        ],
        || {
            // SAFETY: pointers and extent are validated before use.
            let writer =
                unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
            let required = unsafe { required_output(required, "required length output is null")? };
            let output = unsafe { output_slice(output.pointer, output.length)? };
            writer.with_mut(|writer| {
                let Some(length) = writer.metadata_json_len()? else {
                    *required = 0;
                    return Ok(());
                };
                *required = length;
                if output.len() < length as usize {
                    return Err(BoundaryError::buffer_too_small(length).into());
                }
                writer.read_metadata_json(&mut output[..length as usize])?;
                Ok(())
            })
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_set_metadata_json(
    writer: *const WriterHandle,
    input_pointer: *const u8,
    input_length: u64,
    cancellation: Cancellation,
    changed: *mut u8,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, changed, "changed output is null", || {
        // SAFETY: pointers and extent are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let input = unsafe { crate::error::input_slice(input_pointer, input_length)? };
        let changed = unsafe { required_output(changed, "changed output is null")? };
        writer.with_mut(|writer| {
            let token = metadata_token(writer.is_clean(), cancellation)?;
            *changed = u8::from(writer.set_metadata_json(input, &token)?);
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_clear_metadata_json(
    writer: *const WriterHandle,
    cancellation: Cancellation,
    changed: *mut u8,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, changed, "changed output is null", || {
        // SAFETY: both pointers are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let changed = unsafe { required_output(changed, "changed output is null")? };
        writer.with_mut(|writer| {
            let token = metadata_token(writer.is_clean(), cancellation)?;
            *changed = u8::from(writer.clear_metadata_json(&token)?);
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_begin_direct(
    writer: *const WriterHandle,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque handle pointer is validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let cancellation = callback::token(cancellation)?;
        writer.with_mut(|writer| {
            writer.begin_direct(&cancellation)?;
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_direct_assign_ranges(
    writer: *const WriterHandle,
    callback: DirectSourceFn,
    context: *mut c_void,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque handle pointer is validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        writer.with_mut(|writer| {
            let result = source::drain_direct(callback, context, |records| {
                for record in records {
                    if record.reserved != 0 {
                        return Err(BoundaryError::reserved(
                            "direct range reserved field is nonzero",
                        )
                        .into());
                    }
                    match ip::decode_range(record.range)? {
                        (Key::V4(from), Key::V4(to)) => {
                            writer.direct_assign_v4(from, to, record.value)?;
                        }
                        (Key::V6(from), Key::V6(to)) => {
                            writer.direct_assign_v6(from, to, record.value)?;
                        }
                        _ => unreachable!("range decoder returns one matching family"),
                    }
                }
                Ok(())
            });
            finish_source(writer, result)
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_direct_clear_ranges(
    writer: *const WriterHandle,
    callback: CoverageSourceFn,
    context: *mut c_void,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque handle pointer is validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        writer.with_mut(|writer| {
            let result = source::drain_coverage(callback, context, |records| {
                for record in records {
                    match ip::decode_range(*record)? {
                        (Key::V4(from), Key::V4(to)) => {
                            writer.direct_clear_v4(from, to)?;
                        }
                        (Key::V6(from), Key::V6(to)) => {
                            writer.direct_clear_v6(from, to)?;
                        }
                        _ => unreachable!("range decoder returns one matching family"),
                    }
                }
                Ok(())
            });
            finish_source(writer, result)
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_commit(
    writer: *const WriterHandle,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: both pointers are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let report = writer.with_mut(|writer| Ok(writer.commit()?))?;
        *output = Box::into_raw(Box::new(ReportHandle::commit(report)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_abort(
    writer: *const WriterHandle,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: both pointers are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let report = writer.with_mut(|writer| Ok(writer.abort()?))?;
        *output = Box::into_raw(Box::new(ReportHandle::abort(report)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_reclaim(
    writer: *const WriterHandle,
    max_transactions: u64,
    max_pages: u64,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: both pointers are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let cancellation = callback::token(cancellation)?;
        let report = writer
            .with_mut(|writer| Ok(writer.reclaim(max_transactions, max_pages, &cancellation)?))?;
        *output = Box::into_raw(Box::new(ReportHandle::reclaim(report)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_close(
    writer: *const WriterHandle,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: both pointers are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let report = writer.close()?;
        *output = Box::into_raw(Box::new(ReportHandle::close(report)));
        Ok::<_, CallError>(())
    })
}

fn metadata_token(clean: bool, cancellation: Cancellation) -> Result<CancellationToken, CallError> {
    if clean {
        return Ok(callback::token(cancellation)?);
    }
    if cancellation.callback.is_some() || !cancellation.context.is_null() {
        return Err(BoundaryError::wrong_state(
            "active operation metadata must use its stored cancellation",
        )
        .into());
    }
    Ok(CancellationToken::new())
}

pub(crate) fn finish_source(
    writer: &mut iprange_livedb::c_abi_support::Writer,
    result: Result<(), CallError>,
) -> Result<(), CallError> {
    let Err(error) = result else {
        return Ok(());
    };
    let aborted = writer.abort_source_failure(Error::InvalidArgument("C range source failed"));
    if cleanup_failed(&aborted) {
        Err(ErrorHandle::source_cleanup_failure(aborted, error).into())
    } else {
        Err(error)
    }
}

fn cleanup_failed(error: &Error) -> bool {
    matches!(
        error,
        Error::TransactionAborted(cause)
            if matches!(cause.as_ref(), Error::CleanupIncomplete { .. })
    )
}
