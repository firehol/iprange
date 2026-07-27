//! Reader/writer open, close, and ownership exports.

use std::mem::size_of;

use iprange_livedb::c_abi_support::{Reader, Writer};

use crate::abi::{Cancellation, Path, TransactionBudget, STATUS_OK};
use crate::callback;
use crate::error::{
    call, call_with_outputs, output_slot, required_input, required_output, BoundaryError,
    CallError, ErrorHandle,
};
use crate::handle::{ReaderHandle, WriterHandle};
use crate::path;

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_open_immutable_reader(
    source: Path,
    reader_output: *mut *mut ReaderHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[output_slot(reader_output, "reader output pointer is null")],
        || {
            // SAFETY: the output slot is validated and initialized before opening.
            let output =
                unsafe { required_output(reader_output, "reader output pointer is null")? };
            *output = std::ptr::null_mut();
            // SAFETY: the tagged path validates its pointer and extent.
            let source = unsafe { path::decode(source)? };
            let reader = Reader::open_immutable(source)?;
            *output = Box::into_raw(Box::new(ReaderHandle::new(reader)));
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_open_live_reader(
    source: Path,
    cancellation: Cancellation,
    reader_output: *mut *mut ReaderHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[output_slot(reader_output, "reader output pointer is null")],
        || {
            // SAFETY: the output slot is validated and initialized before opening.
            let output =
                unsafe { required_output(reader_output, "reader output pointer is null")? };
            *output = std::ptr::null_mut();
            let cancellation = callback::token(cancellation)?;
            // SAFETY: the tagged path validates its pointer and extent.
            let source = unsafe { path::decode(source)? };
            let reader = Reader::open_live(source, &cancellation)?;
            *output = Box::into_raw(Box::new(ReaderHandle::new(reader)));
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_open_live_writer(
    source: Path,
    budget: *const TransactionBudget,
    cancellation: Cancellation,
    writer_output: *mut *mut WriterHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[output_slot(writer_output, "writer output pointer is null")],
        || {
            // SAFETY: all input/output pointers are validated before use.
            let output =
                unsafe { required_output(writer_output, "writer output pointer is null")? };
            *output = std::ptr::null_mut();
            let budget = unsafe { required_input(budget, "writer budget is null")? };
            let budget = decode_budget(budget)?;
            let cancellation = callback::token(cancellation)?;
            let source = unsafe { path::decode(source)? };
            let writer = Writer::open(source, budget, &cancellation)?;
            *output = Box::into_raw(Box::new(WriterHandle::new(writer)));
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_close(
    reader: *mut ReaderHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque handle pointer is validated before mutation.
        let reader =
            unsafe { crate::handle::required_handle_output(reader, "reader handle is null")? };
        reader.close()
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_destroy(
    reader: *mut ReaderHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if reader.is_null() {
        return STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the opaque handle pointer is validated before inspection.
        let reader_ref =
            unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
        if !reader_ref.is_closed() {
            return Err(BoundaryError::handle_busy("reader must be closed before destroy").into());
        }
        // SAFETY: this consumes the unique ABI-owned allocation exactly once.
        unsafe { drop(Box::from_raw(reader)) };
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_destroy(
    writer: *mut WriterHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if writer.is_null() {
        return STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the opaque handle pointer is validated before inspection.
        let writer_ref =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        if !writer_ref.is_closed() {
            return Err(BoundaryError::handle_busy("writer must be closed before destroy").into());
        }
        // SAFETY: this consumes the unique ABI-owned allocation exactly once.
        unsafe { drop(Box::from_raw(writer)) };
        Ok::<_, CallError>(())
    })
}

fn decode_budget(
    budget: &TransactionBudget,
) -> Result<iprange_livedb::TransactionBudget, BoundaryError> {
    if budget.abi_version != 1 {
        return Err(BoundaryError::invalid_argument(
            "writer budget ABI version is not 1",
        ));
    }
    if budget.struct_size != size_of::<TransactionBudget>() as u32 {
        return Err(BoundaryError::invalid_length(
            "writer budget structure size is invalid",
        ));
    }
    if budget.reserved != 0 {
        return Err(BoundaryError::reserved("writer budget reserved field"));
    }
    Ok(iprange_livedb::TransactionBudget {
        max_heap_bytes: budget.max_heap_bytes,
        max_private_pages: budget.max_private_pages,
        max_file_growth_pages: budget.max_file_growth_pages,
        max_open_files: budget.max_open_files,
    })
}
