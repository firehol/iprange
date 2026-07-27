//! Advanced membership transaction and operation-bound child handles.

use std::ffi::c_void;

use iprange_livedb::{FeedName, MembershipOperation};

use crate::abi::{Cancellation, CoverageSourceFn, FeedInfo, FeedSinkFn};
use crate::callback;
use crate::error::{
    call, call_with_output, input_slice, required_output, BoundaryError, CallError, ErrorHandle,
};
use crate::feed_batch::FeedBatch;
use crate::handle::{
    MembershipBuilderHandle, MembershipRefHandle, WriterFeedRefHandle, WriterHandle,
};
use crate::ip::{self, Key};
use crate::reader::encode_feed;
use crate::source;
use crate::writer::finish_source;

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_begin_membership(
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
            writer.begin_membership(&cancellation)?;
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_feed_ensure(
    writer: *const WriterHandle,
    name_pointer: *const u8,
    name_length: u64,
    output: *mut *mut WriterFeedRefHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        output,
        "feed reference output is null",
        || {
            // SAFETY: pointers and extent are validated before use.
            let writer =
                unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
            let output = unsafe { required_output(output, "feed reference output is null")? };
            *output = std::ptr::null_mut();
            let name = unsafe { decode_name(name_pointer, name_length)? };
            let feed = writer.with_mut(|inner| Ok(inner.feed_ensure(name)?))?;
            *output = Box::into_raw(Box::new(WriterFeedRefHandle::new(writer, feed)));
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_feed_lookup(
    writer: *const WriterHandle,
    name_pointer: *const u8,
    name_length: u64,
    output: *mut *mut WriterFeedRefHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        output,
        "feed reference output is null",
        || {
            // SAFETY: pointers and extent are validated before use.
            let writer =
                unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
            let output = unsafe { required_output(output, "feed reference output is null")? };
            *output = std::ptr::null_mut();
            let name = unsafe { decode_name(name_pointer, name_length)? };
            if let Some(feed) = writer.with_mut(|inner| Ok(inner.feed_lookup(name)?))? {
                *output = Box::into_raw(Box::new(WriterFeedRefHandle::new(writer, feed)));
            }
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_feed_enumerate(
    writer: *const WriterHandle,
    callback_fn: FeedSinkFn,
    context: *mut c_void,
    count_output: *mut u64,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        count_output,
        "feed count output is null",
        || {
            // SAFETY: both pointers are validated before use.
            let writer =
                unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
            let count_output =
                unsafe { required_output(count_output, "feed count output is null")? };
            let (count, result) = enumerate_transaction_feeds(writer, callback_fn, context);
            *count_output = count;
            result
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_feed_rename(
    writer: *const WriterHandle,
    feed: *const WriterFeedRefHandle,
    name_pointer: *const u8,
    name_length: u64,
    output: *mut *mut WriterFeedRefHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        output,
        "feed reference output is null",
        || {
            // SAFETY: call_with_output validates the output, and the helper validates all inputs.
            unsafe { rename_feed(writer, feed, name_pointer, name_length, output) }
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_feed_delete(
    writer: *const WriterHandle,
    feed: *const WriterFeedRefHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: both opaque pointers are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let feed = unsafe { crate::handle::required_handle_input(feed, "feed reference is null")? };
        let _feed_guard = feed.enter()?;
        feed.require_parent(writer)?;
        writer.with_mut(|inner| {
            inner.feed_delete(feed.value)?;
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_feed_ref_info(
    feed: *const WriterFeedRefHandle,
    output: *mut FeedInfo,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "feed info output is null", || {
        // SAFETY: both pointers are validated before use.
        let feed = unsafe { crate::handle::required_handle_input(feed, "feed reference is null")? };
        let _feed_guard = feed.enter()?;
        let output = unsafe { required_output(output, "feed info output is null")? };
        *output = encode_feed(iprange_livedb::FeedEntry {
            name: feed.value.name(),
            index: feed.value.index(),
        });
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_feed_ref_destroy(
    feed: *mut WriterFeedRefHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if feed.is_null() {
        return crate::abi::STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before ownership is consumed.
        let current =
            unsafe { crate::handle::required_handle_input(feed, "feed reference is null")? };
        let guard = current.enter()?;
        current.parent().remove_child();
        drop(guard);
        unsafe { drop(Box::from_raw(feed)) };
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_membership_builder_create(
    writer: *const WriterHandle,
    output: *mut *mut MembershipBuilderHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "builder output is null", || {
        // SAFETY: both pointers are validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let output = unsafe { required_output(output, "builder output is null")? };
        *output = std::ptr::null_mut();
        let membership = writer.with_mut(|inner| Ok(inner.empty_membership()?))?;
        *output = Box::into_raw(Box::new(MembershipBuilderHandle::new(writer, membership)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_builder_add_feed(
    builder: *mut MembershipBuilderHandle,
    feed: *const WriterFeedRefHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: both opaque pointers are validated before use.
        let builder = unsafe { crate::handle::required_handle_input(builder, "builder is null")? };
        let feed = unsafe { crate::handle::required_handle_input(feed, "feed reference is null")? };
        let _feed_guard = feed.enter()?;
        builder.with_mut(|parent, value, finished| {
            if *finished {
                return Err(BoundaryError::wrong_state("membership builder is finished").into());
            }
            feed.require_parent(parent)?;
            *value = parent.with_mut(|inner| Ok(inner.membership_add_feed(*value, feed.value)?))?;
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_builder_finish(
    builder: *mut MembershipBuilderHandle,
    output: *mut *mut MembershipRefHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        output,
        "membership reference output is null",
        || {
            // SAFETY: both pointers are validated before use.
            let builder =
                unsafe { crate::handle::required_handle_input(builder, "builder is null")? };
            let output = unsafe { required_output(output, "membership reference output is null")? };
            *output = std::ptr::null_mut();
            builder.with_mut(|parent, value, finished| {
                if *finished {
                    return Err(BoundaryError::wrong_state("membership builder is finished").into());
                }
                *output = Box::into_raw(Box::new(MembershipRefHandle::new(parent, *value)));
                *finished = true;
                Ok(())
            })
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_builder_destroy(
    builder: *mut MembershipBuilderHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if builder.is_null() {
        return crate::abi::STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before ownership is consumed.
        let current = unsafe { crate::handle::required_handle_input(builder, "builder is null")? };
        let guard = current.enter()?;
        current.parent_unlocked().remove_child();
        drop(guard);
        unsafe { drop(Box::from_raw(builder)) };
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_ref_destroy(
    membership: *mut MembershipRefHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if membership.is_null() {
        return crate::abi::STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before ownership is consumed.
        let current = unsafe {
            crate::handle::required_handle_input(membership, "membership reference is null")?
        };
        let guard = current.enter()?;
        current.parent().remove_child();
        drop(guard);
        unsafe { drop(Box::from_raw(membership)) };
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_membership_apply_ranges(
    writer: *const WriterHandle,
    membership: *const MembershipRefHandle,
    operation: u32,
    callback: CoverageSourceFn,
    context: *mut c_void,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the helper validates both opaque pointers before use.
        unsafe { apply_membership_ranges(writer, membership, operation, callback, context) }
    })
}

fn enumerate_transaction_feeds(
    writer: &WriterHandle,
    callback: FeedSinkFn,
    context: *mut c_void,
) -> (u64, Result<(), CallError>) {
    let mut batch = FeedBatch::new(callback, context, "transaction feed scan count");
    let result = writer.with_mut(|inner| {
        inner.enumerate_transaction_feeds(|feed| {
            batch.push(iprange_livedb::FeedEntry {
                name: feed.name(),
                index: feed.index(),
            })
        })?;
        Ok(())
    });
    batch.finish(result)
}

unsafe fn rename_feed(
    writer: *const WriterHandle,
    feed: *const WriterFeedRefHandle,
    name_pointer: *const u8,
    name_length: u64,
    output: *mut *mut WriterFeedRefHandle,
) -> Result<(), CallError> {
    // SAFETY: the C caller supplies opaque handles and the checked name extent.
    let writer = unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
    let feed = unsafe { crate::handle::required_handle_input(feed, "feed reference is null")? };
    let _feed_guard = feed.enter()?;
    feed.require_parent(writer)?;
    let output = unsafe { required_output(output, "feed reference output is null")? };
    *output = std::ptr::null_mut();
    let name = unsafe { decode_name(name_pointer, name_length)? };
    let renamed = writer.with_mut(|inner| Ok(inner.feed_rename(feed.value, name)?))?;
    *output = Box::into_raw(Box::new(WriterFeedRefHandle::new(writer, renamed)));
    Ok(())
}

unsafe fn apply_membership_ranges(
    writer: *const WriterHandle,
    membership: *const MembershipRefHandle,
    operation: u32,
    callback: CoverageSourceFn,
    context: *mut c_void,
) -> Result<(), CallError> {
    // SAFETY: the C caller supplies opaque handles validated before typed use.
    let writer = unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
    let membership = unsafe {
        crate::handle::required_handle_input(membership, "membership reference is null")?
    };
    let _membership_guard = membership.enter()?;
    membership.require_parent(writer)?;
    let operation = decode_operation(operation)?;
    writer.with_mut(|inner| {
        let result = source::drain_coverage(callback, context, |records| {
            apply_membership_batch(inner, records, membership.value, operation)
        });
        finish_source(inner, result)
    })
}

fn apply_membership_batch(
    writer: &mut iprange_livedb::c_abi_support::Writer,
    records: &[crate::abi::Range],
    membership: iprange_livedb::MembershipRef,
    operation: MembershipOperation,
) -> Result<(), CallError> {
    for record in records {
        apply_membership_range(writer, *record, membership, operation)?;
    }
    Ok(())
}

fn apply_membership_range(
    writer: &mut iprange_livedb::c_abi_support::Writer,
    record: crate::abi::Range,
    membership: iprange_livedb::MembershipRef,
    operation: MembershipOperation,
) -> Result<(), CallError> {
    match ip::decode_range(record)? {
        (Key::V4(from), Key::V4(to)) => {
            writer.membership_apply_v4(from, to, membership, operation)?;
        }
        (Key::V6(from), Key::V6(to)) => {
            writer.membership_apply_v6(from, to, membership, operation)?;
        }
        _ => unreachable!("range decoder returns one matching family"),
    }
    Ok(())
}

pub(crate) unsafe fn decode_name(
    pointer: *const u8,
    length: u64,
) -> Result<FeedName, BoundaryError> {
    // SAFETY: the caller supplies readable storage for the checked extent.
    let bytes = unsafe { input_slice(pointer, length)? };
    let name = std::str::from_utf8(bytes)
        .map_err(|_| BoundaryError::name_invalid("feed name is not UTF-8"))?;
    FeedName::new(name).map_err(|_| BoundaryError::name_invalid("feed name is invalid"))
}

fn decode_operation(value: u32) -> Result<MembershipOperation, BoundaryError> {
    match value {
        1 => Ok(MembershipOperation::Replace),
        2 => Ok(MembershipOperation::Union),
        3 => Ok(MembershipOperation::Difference),
        4 => Ok(MembershipOperation::Intersection),
        5 => Ok(MembershipOperation::Xor),
        _ => Err(BoundaryError::invalid_enum("unknown membership operation")),
    }
}
