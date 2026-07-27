//! High-level feed, direct, retention, and membership-import workflows.

use std::ffi::c_void;

use iprange_livedb::{AddressRange, DirectRange};

use crate::abi::{Cancellation, CoverageSourceFn, DirectSourceFn};
use crate::callback;
use crate::error::{call, call_with_output, required_output, CallError, ErrorHandle};
use crate::handle::{ReaderHandle, WriterHandle};
use crate::ip::{self, Key};
use crate::membership::decode_name;
use crate::report::ReportHandle;
use crate::source;
use crate::writer::finish_source;

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_begin_create_feed(
    writer: *const WriterHandle,
    name_pointer: *const u8,
    name_length: u64,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    begin_feed(
        writer,
        name_pointer,
        name_length,
        cancellation,
        true,
        error_output,
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_begin_replace_feed(
    writer: *const WriterHandle,
    name_pointer: *const u8,
    name_length: u64,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    begin_feed(
        writer,
        name_pointer,
        name_length,
        cancellation,
        false,
        error_output,
    )
}

fn begin_feed(
    writer: *const WriterHandle,
    name_pointer: *const u8,
    name_length: u64,
    cancellation: Cancellation,
    create: bool,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque pointer and name extent are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let name = unsafe { decode_name(name_pointer, name_length)? };
        let cancellation = callback::token(cancellation)?;
        writer.with_mut(|inner| {
            if create {
                inner.begin_create_feed(name, &cancellation)?;
            } else {
                inner.begin_replace_feed(name, &cancellation)?;
            }
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_delete_feed(
    writer: *const WriterHandle,
    name_pointer: *const u8,
    name_length: u64,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque pointer and name extent are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let name = unsafe { decode_name(name_pointer, name_length)? };
        let cancellation = callback::token(cancellation)?;
        writer.with_mut(|inner| {
            inner.delete_feed(name, &cancellation)?;
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_rename_feed(
    writer: *const WriterHandle,
    old_pointer: *const u8,
    old_length: u64,
    new_pointer: *const u8,
    new_length: u64,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque pointer and both name extents are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let old = unsafe { decode_name(old_pointer, old_length)? };
        let new = unsafe { decode_name(new_pointer, new_length)? };
        let cancellation = callback::token(cancellation)?;
        writer.with_mut(|inner| {
            inner.rename_feed(old, new, &cancellation)?;
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_begin_direct_replacement(
    writer: *const WriterHandle,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let cancellation = callback::token(cancellation)?;
        writer.with_mut(|inner| {
            inner.begin_direct_replacement(&cancellation)?;
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_begin_retention_refresh(
    writer: *const WriterHandle,
    refresh_value: u32,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let cancellation = callback::token(cancellation)?;
        writer.with_mut(|inner| {
            inner.begin_retention_refresh(refresh_value, &cancellation)?;
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_begin_membership_import(
    writer: *const WriterHandle,
    source: *const ReaderHandle,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: both opaque pointers are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let source = unsafe {
            crate::handle::required_handle_input(source, "source reader handle is null")?
        };
        let source = source.get()?.clone();
        let cancellation = callback::token(cancellation)?;
        writer.with_mut(|inner| {
            inner.begin_membership_import(source, &cancellation)?;
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_add_coverage_ranges(
    writer: *const WriterHandle,
    callback: CoverageSourceFn,
    context: *mut c_void,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        writer.with_mut(|inner| {
            let result = source::drain_coverage(callback, context, |records| {
                for record in records {
                    match ip::decode_range(*record)? {
                        (Key::V4(from), Key::V4(to)) => {
                            inner.add_coverage_v4(&[AddressRange { from, to }])?;
                        }
                        (Key::V6(from), Key::V6(to)) => {
                            inner.add_coverage_v6(&[AddressRange { from, to }])?;
                        }
                        _ => unreachable!("range decoder returns one matching family"),
                    }
                }
                Ok(())
            });
            finish_source(inner, result)
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_add_direct_ranges(
    writer: *const WriterHandle,
    callback: DirectSourceFn,
    context: *mut c_void,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        writer.with_mut(|inner| {
            let result = source::drain_direct(callback, context, |records| {
                for record in records {
                    if record.reserved != 0 {
                        return Err(crate::error::BoundaryError::reserved(
                            "direct range reserved field is nonzero",
                        )
                        .into());
                    }
                    match ip::decode_range(record.range)? {
                        (Key::V4(from), Key::V4(to)) => {
                            inner.add_direct_v4(&[DirectRange {
                                from,
                                to,
                                value: record.value,
                            }])?;
                        }
                        (Key::V6(from), Key::V6(to)) => {
                            inner.add_direct_v6(&[DirectRange {
                                from,
                                to,
                                value: record.value,
                            }])?;
                        }
                        _ => unreachable!("range decoder returns one matching family"),
                    }
                }
                Ok(())
            });
            finish_source(inner, result)
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_finish_input(
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
        let report = writer.with_mut(|inner| Ok(inner.finish_input()?))?;
        *output = Box::into_raw(Box::new(ReportHandle::finish_input(report)));
        Ok::<_, CallError>(())
    })
}
