//! High-level feed, direct, timestamp, and membership-import workflows.

use std::ffi::c_void;

use iprange_livedb::{
    AddressFamily, Error, FirstSeenRemoval as CoreFirstSeenRemoval, Ipv4Key, Ipv6Key,
};

use crate::abi::{
    Cancellation, CoverageSourceFn, DirectSourceFn, FirstSeenRemoval, FirstSeenRemovalSinkFn,
};
use crate::callback;
use crate::error::{call, call_with_output, required_output, CallError, ErrorHandle};
use crate::handle::{ReaderHandle, WriterHandle};
use crate::ip::{self, Key};
use crate::membership::decode_name;
use crate::report::ReportHandle;
use crate::source;
use crate::writer::cleanup_failed;

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
pub unsafe extern "C" fn iprange_v4_abi1_writer_begin_first_seen_refresh(
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
            inner.begin_first_seen_refresh(refresh_value, &cancellation)?;
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_begin_last_seen_refresh(
    writer: *const WriterHandle,
    refresh_value: u32,
    cutoff: u32,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let cancellation = callback::token(cancellation)?;
        writer.with_mut(|inner| {
            inner.begin_last_seen_refresh(refresh_value, cutoff, &cancellation)?;
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
        writer.with_mut(|inner| match inner.address_family() {
            AddressFamily::Ipv4 => {
                let mut source = source::CoverageV4::new(callback, context);
                let result = inner.add_coverage_v4(&mut source);
                finish_callback_operation(result, source.take_failure())
            }
            AddressFamily::Ipv6 => {
                let mut source = source::CoverageV6::new(callback, context);
                let result = inner.add_coverage_v6(&mut source);
                finish_callback_operation(result, source.take_failure())
            }
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
        writer.with_mut(|inner| match inner.address_family() {
            AddressFamily::Ipv4 => {
                let mut source = source::DirectV4::new(callback, context);
                let result = inner.add_direct_v4(&mut source);
                finish_callback_operation(result, source.take_failure())
            }
            AddressFamily::Ipv6 => {
                let mut source = source::DirectV6::new(callback, context);
                let result = inner.add_direct_v6(&mut source);
                finish_callback_operation(result, source.take_failure())
            }
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

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_finish_first_seen_with_removals(
    writer: *const WriterHandle,
    callback: FirstSeenRemovalSinkFn,
    context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: the opaque pointer and output are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let family = writer.with_mut(|inner| Ok(inner.address_family()))?;
        let mut callback_error = None;
        let result = writer.with_mut(|inner| {
            Ok(match family {
                AddressFamily::Ipv4 => inner.finish_first_seen_with_removals_v4(
                    &mut |batch: &[CoreFirstSeenRemoval<Ipv4Key>]| {
                        emit_first_seen_removals_v4(callback, context, batch, &mut callback_error)
                    },
                ),
                AddressFamily::Ipv6 => inner.finish_first_seen_with_removals_v6(
                    &mut |batch: &[CoreFirstSeenRemoval<Ipv6Key>]| {
                        emit_first_seen_removals_v6(callback, context, batch, &mut callback_error)
                    },
                ),
            })
        })?;
        let report = finish_callback_operation(result, callback_error)?;
        *output = Box::into_raw(Box::new(ReportHandle::finish_input(report)));
        Ok::<_, CallError>(())
    })
}

const REMOVAL_BATCH_CAPACITY: usize = 64;

fn emit_first_seen_removals_v4(
    callback: FirstSeenRemovalSinkFn,
    context: *mut c_void,
    batch: &[CoreFirstSeenRemoval<Ipv4Key>],
    callback_error: &mut Option<CallError>,
) -> iprange_livedb::Result<()> {
    emit_first_seen_removals(callback, context, batch, callback_error, |removal| {
        FirstSeenRemoval {
            range: crate::abi::Range {
                from: ip::encode(Key::V4(removal.from)),
                to: ip::encode(Key::V4(removal.to)),
            },
            first_seen: removal.first_seen,
            reserved: 0,
            addresses: crate::report::cardinality(removal.addresses),
        }
    })
}

fn emit_first_seen_removals_v6(
    callback: FirstSeenRemovalSinkFn,
    context: *mut c_void,
    batch: &[CoreFirstSeenRemoval<Ipv6Key>],
    callback_error: &mut Option<CallError>,
) -> iprange_livedb::Result<()> {
    emit_first_seen_removals(callback, context, batch, callback_error, |removal| {
        FirstSeenRemoval {
            range: crate::abi::Range {
                from: ip::encode(Key::V6(removal.from)),
                to: ip::encode(Key::V6(removal.to)),
            },
            first_seen: removal.first_seen,
            reserved: 0,
            addresses: crate::report::cardinality(removal.addresses),
        }
    })
}

fn emit_first_seen_removals<K: Copy>(
    callback: FirstSeenRemovalSinkFn,
    context: *mut c_void,
    batch: &[CoreFirstSeenRemoval<K>],
    callback_error: &mut Option<CallError>,
    mut encode: impl FnMut(CoreFirstSeenRemoval<K>) -> FirstSeenRemoval,
) -> iprange_livedb::Result<()> {
    if batch.len() > REMOVAL_BATCH_CAPACITY {
        return Err(Error::Corrupt("first-seen removal batch exceeds its bound"));
    }
    let mut output = [FirstSeenRemoval::default(); REMOVAL_BATCH_CAPACITY];
    for (destination, source) in output.iter_mut().zip(batch.iter().copied()) {
        *destination = encode(source);
    }
    match crate::sink::first_seen_removal(callback, context, &output[..batch.len()]) {
        Ok(crate::sink::Control::Continue) => Ok(()),
        Ok(crate::sink::Control::Stop) => Err(Error::StoppedBySink),
        Err(error) => {
            *callback_error = Some(error);
            Err(Error::SinkFailed(Box::new(Error::InvalidArgument(
                "C first-seen removal sink failed",
            ))))
        }
    }
}

fn finish_callback_operation<T>(
    result: iprange_livedb::Result<T>,
    callback_error: Option<CallError>,
) -> Result<T, CallError> {
    match (result, callback_error) {
        (Ok(report), None) => Ok(report),
        (Ok(_), Some(error)) => Err(error),
        (Err(aborted), Some(error)) if cleanup_failed(&aborted) => {
            Err(crate::error::ErrorHandle::callback_cleanup_failure(aborted, error).into())
        }
        (Err(_), Some(error)) => Err(error),
        (Err(error), None) => Err(error.into()),
    }
}
