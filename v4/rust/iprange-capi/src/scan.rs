//! Bounded, batched reader scans and feed enumeration.

use std::ffi::c_void;
use std::mem::MaybeUninit;

use iprange_livedb::c_abi_support::ReaderCursorItem;
use iprange_livedb::{CancellationToken, Error};

use crate::abi::{
    CallbackFailure, Cancellation, CoverageSinkFn, DirectSinkFn, FeedSinkFn, MembershipRange,
    MembershipSinkFn, NetworkEnrichmentV1Range, NetworkEnrichmentV1SinkFn, Range,
};
use crate::callback;
use crate::cursor::{self, Kind};
use crate::error::{call_with_output, required_output, CallError, ErrorHandle};
use crate::feed_batch::FeedBatch;
use crate::handle::{BorrowedMembershipViewHandle, CursorHandle, ReaderHandle};
use crate::membership::decode_name;
use crate::report::ReportHandle;
use crate::sink::{self, Control};

const BATCH_CAPACITY: usize = 256;

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_enumerate_feeds(
    reader: *const ReaderHandle,
    cancellation: Cancellation,
    callback_fn: FeedSinkFn,
    context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: both opaque/output pointers are validated before use.
        let reader =
            unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let cancellation = callback::token(cancellation)?;
        let (count, result) = enumerate_feeds(reader, &cancellation, callback_fn, context);
        *output = Box::into_raw(Box::new(ReportHandle::scan(count, result.is_ok())));
        result
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_scan_direct(
    reader: *const ReaderHandle,
    direction: u32,
    bounds: *const Range,
    cancellation: Cancellation,
    callback_fn: DirectSinkFn,
    context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: all pointers are validated before use.
        let reader =
            unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let direction = cursor::decode_direction(direction)?;
        let bounds = unsafe { cursor::decode_bounds(bounds, direction)? };
        let cancellation = callback::token(cancellation)?;
        let cursor = cursor::build(reader, direction, bounds, Kind::Direct)?;
        let (count, result) = scan_direct(&cursor, &cancellation, callback_fn, context);
        *output = Box::into_raw(Box::new(ReportHandle::scan(count, result.is_ok())));
        result
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_scan_membership(
    reader: *const ReaderHandle,
    direction: u32,
    bounds: *const Range,
    cancellation: Cancellation,
    callback_fn: MembershipSinkFn,
    context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: all pointers are validated before use.
        let reader =
            unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let direction = cursor::decode_direction(direction)?;
        let bounds = unsafe { cursor::decode_bounds(bounds, direction)? };
        let cancellation = callback::token(cancellation)?;
        let cursor = cursor::build(reader, direction, bounds, Kind::Membership)?;
        let (count, result) = scan_membership(&cursor, &cancellation, callback_fn, context);
        *output = Box::into_raw(Box::new(ReportHandle::scan(count, result.is_ok())));
        result
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_scan_network_enrichment_v1(
    reader: *const ReaderHandle,
    direction: u32,
    bounds: *const Range,
    cancellation: Cancellation,
    callback_fn: NetworkEnrichmentV1SinkFn,
    context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: all pointers are validated before use.
        let reader =
            unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let direction = cursor::decode_direction(direction)?;
        let bounds = unsafe { cursor::decode_bounds(bounds, direction)? };
        let cancellation = callback::token(cancellation)?;
        let cursor = cursor::build(reader, direction, bounds, Kind::NetworkEnrichmentV1)?;
        let (count, result) =
            scan_network_enrichment_v1(&cursor, &cancellation, callback_fn, context);
        *output = Box::into_raw(Box::new(ReportHandle::scan(count, result.is_ok())));
        result
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_scan_feed(
    reader: *const ReaderHandle,
    name_pointer: *const u8,
    name_length: u64,
    direction: u32,
    bounds: *const Range,
    cancellation: Cancellation,
    callback_fn: CoverageSinkFn,
    context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: all pointers and the name extent are validated before use.
        let reader =
            unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let name = unsafe { decode_name(name_pointer, name_length)? };
        let direction = cursor::decode_direction(direction)?;
        let bounds = unsafe { cursor::decode_bounds(bounds, direction)? };
        let cancellation = callback::token(cancellation)?;
        let cursor = cursor::build(reader, direction, bounds, Kind::Feed(name.as_str()))?;
        let (count, result) = scan_coverage(&cursor, &cancellation, callback_fn, context);
        *output = Box::into_raw(Box::new(ReportHandle::scan(count, result.is_ok())));
        result
    })
}

fn scan_direct(
    cursor_handle: &CursorHandle,
    cancellation: &CancellationToken,
    callback_fn: DirectSinkFn,
    context: *mut c_void,
) -> (u64, Result<(), CallError>) {
    scan_plain(
        cursor_handle,
        cancellation,
        callback_fn,
        context,
        |item| match item {
            ReaderCursorItem::DirectV4(range) => Ok(cursor::direct_v4(range)),
            ReaderCursorItem::DirectV6(range) => Ok(cursor::direct_v6(range)),
            _ => Err(Error::WrongState("not a direct cursor").into()),
        },
        sink::direct,
    )
}

fn scan_coverage(
    cursor_handle: &CursorHandle,
    cancellation: &CancellationToken,
    callback_fn: CoverageSinkFn,
    context: *mut c_void,
) -> (u64, Result<(), CallError>) {
    scan_plain(
        cursor_handle,
        cancellation,
        callback_fn,
        context,
        |item| match item {
            ReaderCursorItem::FeedV4(range) => Ok(cursor::range_v4(range)),
            ReaderCursorItem::FeedV6(range) => Ok(cursor::range_v6(range)),
            _ => Err(Error::WrongState("not a feed cursor").into()),
        },
        sink::coverage,
    )
}

type PlainSinkFn<T> = Option<
    unsafe extern "C" fn(
        context: *mut c_void,
        records: *const T,
        count: u64,
        failure: *mut CallbackFailure,
    ) -> u32,
>;

type PlainEmitFn<T> = fn(PlainSinkFn<T>, *mut c_void, &[T]) -> Result<Control, CallError>;

fn scan_plain<T: Copy + Default>(
    cursor_handle: &CursorHandle,
    cancellation: &CancellationToken,
    callback_fn: PlainSinkFn<T>,
    context: *mut c_void,
    decode: impl Fn(ReaderCursorItem) -> Result<T, CallError>,
    emit: PlainEmitFn<T>,
) -> (u64, Result<(), CallError>) {
    let mut batch = [T::default(); BATCH_CAPACITY];
    let mut length = 0usize;
    let mut count = 0u64;
    loop {
        if cancellation.is_cancelled() {
            return (count, Err(Error::Cancelled.into()));
        }
        let item = cursor_handle.with_mut(|reader, cursor, borrowed, bounds| {
            *borrowed = None;
            cursor::next(reader, cursor, bounds)
        });
        let item = match item {
            Ok(item) => item,
            Err(error) => return (count, Err(error)),
        };
        match item {
            Some(item) => match decode(item) {
                Ok(record) => {
                    batch[length] = record;
                    length += 1;
                }
                Err(error) => return (count, Err(error)),
            },
            None => return flush_plain(callback_fn, context, &batch[..length], count, emit),
        }
        if length == batch.len() {
            let (next_count, result) = flush_plain(callback_fn, context, &batch, count, emit);
            if result.is_err() {
                return (next_count, result);
            }
            count = next_count;
            length = 0;
        }
    }
}

fn flush_plain<T>(
    callback_fn: PlainSinkFn<T>,
    context: *mut c_void,
    batch: &[T],
    count: u64,
    emit: PlainEmitFn<T>,
) -> (u64, Result<(), CallError>) {
    if batch.is_empty() {
        return (count, Ok(()));
    }
    finish_batch(count, batch.len(), emit(callback_fn, context, batch))
}

fn finish_batch(
    count: u64,
    length: usize,
    result: Result<Control, CallError>,
) -> (u64, Result<(), CallError>) {
    match result {
        Ok(control) => {
            let count = match count.checked_add(length as u64) {
                Some(count) => count,
                None => return (count, Err(Error::ArithmeticOverflow("scan count").into())),
            };
            if control == Control::Stop {
                (count, Err(Error::StoppedBySink.into()))
            } else {
                (count, Ok(()))
            }
        }
        Err(error) => (count, Err(error)),
    }
}

fn scan_membership(
    cursor_handle: &CursorHandle,
    cancellation: &CancellationToken,
    callback_fn: MembershipSinkFn,
    context: *mut c_void,
) -> (u64, Result<(), CallError>) {
    let mut views: [MaybeUninit<BorrowedMembershipViewHandle>; BATCH_CAPACITY] =
        std::array::from_fn(|_| MaybeUninit::uninit());
    let mut records: [MaybeUninit<MembershipRange>; BATCH_CAPACITY] =
        std::array::from_fn(|_| MaybeUninit::uninit());
    let mut length = 0usize;
    let mut count = 0u64;
    loop {
        if cancellation.is_cancelled() {
            return (count, Err(Error::Cancelled.into()));
        }
        let step = cursor_handle.with_mut(|reader, cursor, borrowed, bounds| {
            *borrowed = None;
            match cursor::next(reader, cursor, bounds)? {
                Some(ReaderCursorItem::MembershipV4 { range, membership }) => {
                    push_membership(
                        reader,
                        membership,
                        cursor::range_v4(range),
                        &mut views,
                        &mut records,
                        length,
                    );
                    Ok(false)
                }
                Some(ReaderCursorItem::MembershipV6 { range, membership }) => {
                    push_membership(
                        reader,
                        membership,
                        cursor::range_v6(range),
                        &mut views,
                        &mut records,
                        length,
                    );
                    Ok(false)
                }
                Some(_) => Err(Error::WrongState("not a membership cursor").into()),
                None => Ok(true),
            }
        });
        let end = match step {
            Ok(end) => end,
            Err(error) => return (count, Err(error)),
        };
        if end {
            return flush_membership(callback_fn, context, &records, length, count);
        }
        length += 1;
        if length == BATCH_CAPACITY {
            let (next_count, result) =
                flush_membership(callback_fn, context, &records, length, count);
            if result.is_err() {
                return (next_count, result);
            }
            count = next_count;
            length = 0;
        }
    }
}

fn push_membership(
    reader: &std::sync::Arc<iprange_livedb::c_abi_support::Reader>,
    membership: iprange_livedb::c_abi_support::MembershipToken,
    range: Range,
    views: &mut [MaybeUninit<BorrowedMembershipViewHandle>; BATCH_CAPACITY],
    records: &mut [MaybeUninit<MembershipRange>; BATCH_CAPACITY],
    index: usize,
) {
    let view = views[index].write(BorrowedMembershipViewHandle::new(reader, membership));
    records[index].write(MembershipRange {
        range,
        membership: view,
    });
}

fn flush_membership(
    callback_fn: MembershipSinkFn,
    context: *mut c_void,
    records: &[MaybeUninit<MembershipRange>; BATCH_CAPACITY],
    length: usize,
    count: u64,
) -> (u64, Result<(), CallError>) {
    if length == 0 {
        return (count, Ok(()));
    }
    // SAFETY: exactly the first `length` records were initialized above.
    let records =
        unsafe { std::slice::from_raw_parts(records.as_ptr().cast::<MembershipRange>(), length) };
    finish_batch(
        count,
        length,
        sink::membership(callback_fn, context, records),
    )
}

fn scan_network_enrichment_v1(
    cursor_handle: &CursorHandle,
    cancellation: &CancellationToken,
    callback_fn: NetworkEnrichmentV1SinkFn,
    context: *mut c_void,
) -> (u64, Result<(), CallError>) {
    let mut views: [MaybeUninit<BorrowedMembershipViewHandle>; BATCH_CAPACITY] =
        std::array::from_fn(|_| MaybeUninit::uninit());
    let mut records: [MaybeUninit<NetworkEnrichmentV1Range>; BATCH_CAPACITY] =
        std::array::from_fn(|_| MaybeUninit::uninit());
    let mut length = 0usize;
    let mut count = 0u64;
    loop {
        if cancellation.is_cancelled() {
            return (count, Err(Error::Cancelled.into()));
        }
        let step = cursor_handle.with_mut(|reader, cursor, borrowed, bounds| {
            *borrowed = None;
            match cursor::next(reader, cursor, bounds)? {
                Some(ReaderCursorItem::NetworkEnrichmentV1V4 {
                    range,
                    value,
                    membership,
                }) => {
                    push_network_enrichment_v1(
                        reader,
                        cursor::range_v4(range),
                        value,
                        membership,
                        &mut views,
                        &mut records,
                        length,
                    );
                    Ok(false)
                }
                Some(ReaderCursorItem::NetworkEnrichmentV1V6 {
                    range,
                    value,
                    membership,
                }) => {
                    push_network_enrichment_v1(
                        reader,
                        cursor::range_v6(range),
                        value,
                        membership,
                        &mut views,
                        &mut records,
                        length,
                    );
                    Ok(false)
                }
                Some(_) => Err(Error::WrongState("not a network enrichment cursor").into()),
                None => Ok(true),
            }
        });
        let end = match step {
            Ok(end) => end,
            Err(error) => return (count, Err(error)),
        };
        if end {
            return flush_network_enrichment_v1(callback_fn, context, &records, length, count);
        }
        length += 1;
        if length == BATCH_CAPACITY {
            let (next_count, result) =
                flush_network_enrichment_v1(callback_fn, context, &records, length, count);
            if result.is_err() {
                return (next_count, result);
            }
            count = next_count;
            length = 0;
        }
    }
}

fn push_network_enrichment_v1(
    reader: &std::sync::Arc<iprange_livedb::c_abi_support::Reader>,
    range: Range,
    value: iprange_livedb::NetworkEnrichmentV1,
    membership: Option<iprange_livedb::c_abi_support::MembershipToken>,
    views: &mut [MaybeUninit<BorrowedMembershipViewHandle>; BATCH_CAPACITY],
    records: &mut [MaybeUninit<NetworkEnrichmentV1Range>; BATCH_CAPACITY],
    index: usize,
) {
    let membership = membership.map_or(std::ptr::null(), |membership| {
        views[index].write(BorrowedMembershipViewHandle::new(reader, membership))
    });
    records[index].write(NetworkEnrichmentV1Range {
        range,
        value: crate::structured::encode(value),
        membership,
    });
}

fn flush_network_enrichment_v1(
    callback_fn: NetworkEnrichmentV1SinkFn,
    context: *mut c_void,
    records: &[MaybeUninit<NetworkEnrichmentV1Range>; BATCH_CAPACITY],
    length: usize,
    count: u64,
) -> (u64, Result<(), CallError>) {
    if length == 0 {
        return (count, Ok(()));
    }
    // SAFETY: exactly the first `length` records were initialized above.
    let records = unsafe {
        std::slice::from_raw_parts(records.as_ptr().cast::<NetworkEnrichmentV1Range>(), length)
    };
    finish_batch(
        count,
        length,
        sink::network_enrichment_v1(callback_fn, context, records),
    )
}

fn enumerate_feeds(
    reader: &ReaderHandle,
    cancellation: &CancellationToken,
    callback_fn: FeedSinkFn,
    context: *mut c_void,
) -> (u64, Result<(), CallError>) {
    let reader = match reader.get() {
        Ok(reader) => reader,
        Err(error) => return (0, Err(error.into())),
    };
    let mut batch = FeedBatch::new(callback_fn, context, "feed scan count");
    let result = reader.enumerate_feeds(|feed| {
        if cancellation.is_cancelled() {
            return Err(Error::Cancelled);
        }
        batch.push(feed)
    });
    let result = result.map(|_| ()).map_err(CallError::from);
    batch.finish(result)
}
