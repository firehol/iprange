//! Reader cursor construction, bounds, movement, and lifetime.

use iprange_livedb::c_abi_support::{ReaderCursor, ReaderCursorItem};
use iprange_livedb::RangeDirection;

use crate::abi::{DirectRange, MembershipRange, NetworkEnrichmentV1Range, Range, STATUS_OK};
use crate::error::{
    call, call_with_output, call_with_outputs, output_slot, required_input, required_output,
    BoundaryError, CallError, ErrorHandle,
};
use crate::handle::{BorrowedMembershipViewHandle, CursorBounds, CursorHandle, ReaderHandle};
use crate::ip::{self, Key};
use crate::membership::decode_name;

#[derive(Clone, Copy)]
pub(crate) enum Kind<'a> {
    Direct,
    Membership,
    NetworkEnrichmentV1,
    Feed(&'a str),
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_open_direct_cursor(
    reader: *const ReaderHandle,
    direction: u32,
    bounds: *const Range,
    output: *mut *mut CursorHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    open_cursor(
        reader,
        direction,
        bounds,
        output,
        error_output,
        Kind::Direct,
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_open_membership_cursor(
    reader: *const ReaderHandle,
    direction: u32,
    bounds: *const Range,
    output: *mut *mut CursorHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    open_cursor(
        reader,
        direction,
        bounds,
        output,
        error_output,
        Kind::Membership,
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_open_network_enrichment_v1_cursor(
    reader: *const ReaderHandle,
    direction: u32,
    bounds: *const Range,
    output: *mut *mut CursorHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    open_cursor(
        reader,
        direction,
        bounds,
        output,
        error_output,
        Kind::NetworkEnrichmentV1,
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_open_feed_cursor(
    reader: *const ReaderHandle,
    name_pointer: *const u8,
    name_length: u64,
    direction: u32,
    bounds: *const Range,
    output: *mut *mut CursorHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "cursor output is null", || {
        // SAFETY: the name extent is validated before it is borrowed.
        let name = unsafe { decode_name(name_pointer, name_length)? };
        open_cursor_inner(reader, direction, bounds, output, Kind::Feed(name.as_str()))
    })
}

fn open_cursor(
    reader: *const ReaderHandle,
    direction: u32,
    bounds: *const Range,
    output: *mut *mut CursorHandle,
    error_output: *mut *mut ErrorHandle,
    kind: Kind<'_>,
) -> u32 {
    call_with_output(error_output, output, "cursor output is null", || {
        open_cursor_inner(reader, direction, bounds, output, kind)
    })
}

fn open_cursor_inner(
    reader: *const ReaderHandle,
    direction: u32,
    bounds: *const Range,
    output: *mut *mut CursorHandle,
    kind: Kind<'_>,
) -> Result<(), CallError> {
    // SAFETY: all pointers are validated before use.
    let reader = unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
    let output = unsafe { required_output(output, "cursor output is null")? };
    *output = std::ptr::null_mut();
    let direction = decode_direction(direction)?;
    // SAFETY: a non-null bounds pointer is validated before use.
    let bounds = unsafe { decode_bounds(bounds, direction)? };
    *output = Box::into_raw(Box::new(build(reader, direction, bounds, kind)?));
    Ok(())
}

pub(crate) fn build(
    reader: &ReaderHandle,
    direction: RangeDirection,
    bounds: Option<CursorBounds>,
    kind: Kind<'_>,
) -> Result<CursorHandle, CallError> {
    let parent = reader.get()?.clone();
    require_bound_family(&parent, bounds)?;
    let mut cursor = match kind {
        Kind::Direct => parent.open_direct_cursor(direction)?,
        Kind::Membership => parent.open_membership_cursor(direction)?,
        Kind::NetworkEnrichmentV1 => parent.open_network_enrichment_v1_cursor(direction)?,
        Kind::Feed(name) => parent.open_feed_cursor(name, direction)?,
    };
    if let Some(bounds) = bounds {
        seek(&parent, &mut cursor, bounds)?;
    }
    Ok(CursorHandle::new(parent, cursor, bounds))
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_cursor_next_direct(
    cursor: *const CursorHandle,
    present: *mut u8,
    output: *mut DirectRange,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(present, "presence output is null"),
            output_slot(output, "direct range output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before use.
            let cursor =
                unsafe { crate::handle::required_handle_input(cursor, "cursor handle is null")? };
            let present = unsafe { required_output(present, "presence output is null")? };
            let output = unsafe { required_output(output, "direct range output is null")? };
            *present = 0;
            *output = DirectRange::default();
            let item = cursor.with_mut(|reader, cursor, borrowed, bounds| {
                *borrowed = None;
                next(reader, cursor, bounds)
            })?;
            match item {
                None => {}
                Some(ReaderCursorItem::DirectV4(range)) => {
                    *present = 1;
                    *output = direct_v4(range);
                }
                Some(ReaderCursorItem::DirectV6(range)) => {
                    *present = 1;
                    *output = direct_v6(range);
                }
                Some(_) => {
                    return Err(
                        BoundaryError::wrong_state("cursor does not return direct ranges").into(),
                    )
                }
            }
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_cursor_next_membership(
    cursor: *const CursorHandle,
    present: *mut u8,
    output: *mut MembershipRange,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(present, "presence output is null"),
            output_slot(output, "membership range output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before use.
            let cursor =
                unsafe { crate::handle::required_handle_input(cursor, "cursor handle is null")? };
            let present = unsafe { required_output(present, "presence output is null")? };
            let output = unsafe { required_output(output, "membership range output is null")? };
            *present = 0;
            *output = MembershipRange {
                range: Range::default(),
                membership: std::ptr::null(),
            };
            cursor.with_mut(|reader, cursor, borrowed, bounds| {
                *borrowed = None;
                match next(reader, cursor, bounds)? {
                    None => {}
                    Some(ReaderCursorItem::MembershipV4 { range, membership }) => {
                        *borrowed = Some(BorrowedMembershipViewHandle::new(reader, membership));
                        *present = 1;
                        *output = MembershipRange {
                            range: range_v4(range),
                            membership: borrowed.as_ref().map_or(std::ptr::null(), |view| view),
                        };
                    }
                    Some(ReaderCursorItem::MembershipV6 { range, membership }) => {
                        *borrowed = Some(BorrowedMembershipViewHandle::new(reader, membership));
                        *present = 1;
                        *output = MembershipRange {
                            range: range_v6(range),
                            membership: borrowed.as_ref().map_or(std::ptr::null(), |view| view),
                        };
                    }
                    Some(_) => {
                        return Err(BoundaryError::wrong_state(
                            "cursor does not return membership ranges",
                        )
                        .into())
                    }
                }
                Ok(())
            })
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_cursor_next_network_enrichment_v1(
    cursor: *const CursorHandle,
    present: *mut u8,
    output: *mut NetworkEnrichmentV1Range,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(present, "presence output is null"),
            output_slot(output, "network enrichment range output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before use.
            let cursor =
                unsafe { crate::handle::required_handle_input(cursor, "cursor handle is null")? };
            let present = unsafe { required_output(present, "presence output is null")? };
            let output =
                unsafe { required_output(output, "network enrichment range output is null")? };
            *present = 0;
            *output = NetworkEnrichmentV1Range::default();
            cursor.with_mut(|reader, cursor, borrowed, bounds| {
                *borrowed = None;
                match next(reader, cursor, bounds)? {
                    None => {}
                    Some(ReaderCursorItem::NetworkEnrichmentV1V4 {
                        range,
                        value,
                        membership,
                    }) => {
                        store_network_enrichment_range(
                            reader,
                            range_v4(range),
                            value,
                            membership,
                            borrowed,
                            present,
                            output,
                        );
                    }
                    Some(ReaderCursorItem::NetworkEnrichmentV1V6 {
                        range,
                        value,
                        membership,
                    }) => {
                        store_network_enrichment_range(
                            reader,
                            range_v6(range),
                            value,
                            membership,
                            borrowed,
                            present,
                            output,
                        );
                    }
                    Some(_) => {
                        return Err(BoundaryError::wrong_state(
                            "cursor does not return network enrichment ranges",
                        )
                        .into())
                    }
                }
                Ok(())
            })
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_cursor_next_coverage(
    cursor: *const CursorHandle,
    present: *mut u8,
    output: *mut Range,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(present, "presence output is null"),
            output_slot(output, "coverage range output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before use.
            let cursor =
                unsafe { crate::handle::required_handle_input(cursor, "cursor handle is null")? };
            let present = unsafe { required_output(present, "presence output is null")? };
            let output = unsafe { required_output(output, "coverage range output is null")? };
            *present = 0;
            *output = Range::default();
            let item = cursor.with_mut(|reader, cursor, borrowed, bounds| {
                *borrowed = None;
                next(reader, cursor, bounds)
            })?;
            match item {
                None => {}
                Some(ReaderCursorItem::FeedV4(range)) => {
                    *present = 1;
                    *output = range_v4(range);
                }
                Some(ReaderCursorItem::FeedV6(range)) => {
                    *present = 1;
                    *output = range_v6(range);
                }
                Some(_) => {
                    return Err(BoundaryError::wrong_state(
                        "cursor does not return coverage ranges",
                    )
                    .into())
                }
            }
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_cursor_close(
    cursor: *const CursorHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before use.
        let cursor =
            unsafe { crate::handle::required_handle_input(cursor, "cursor handle is null")? };
        cursor.close()
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_cursor_destroy(
    cursor: *mut CursorHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if cursor.is_null() {
        return STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before ownership is consumed.
        let current =
            unsafe { crate::handle::required_handle_input(cursor, "cursor handle is null")? };
        if !current.is_closed()? {
            return Err(BoundaryError::handle_busy("cursor must be closed before destroy").into());
        }
        // SAFETY: this consumes the unique ABI-owned allocation exactly once.
        unsafe { drop(Box::from_raw(cursor)) };
        Ok::<_, CallError>(())
    })
}

pub(crate) fn next(
    reader: &iprange_livedb::c_abi_support::Reader,
    cursor: &mut ReaderCursor,
    bounds: Option<CursorBounds>,
) -> Result<Option<ReaderCursorItem>, CallError> {
    loop {
        let Some(item) = reader.cursor_next(cursor)? else {
            return Ok(None);
        };
        match clip(item, bounds) {
            Clip::Yield(item) => return Ok(Some(item)),
            Clip::Skip => {}
            Clip::End => return Ok(None),
        }
    }
}

fn seek(
    reader: &iprange_livedb::c_abi_support::Reader,
    cursor: &mut ReaderCursor,
    bounds: CursorBounds,
) -> Result<(), CallError> {
    let target = match bounds.direction {
        RangeDirection::Forward => bounds.from,
        RangeDirection::Backward => bounds.to,
    };
    match target {
        Key::V4(target) => reader.cursor_seek_v4(cursor, target)?,
        Key::V6(target) => reader.cursor_seek_v6(cursor, target)?,
    }
    Ok(())
}

fn require_bound_family(
    reader: &iprange_livedb::c_abi_support::Reader,
    bounds: Option<CursorBounds>,
) -> Result<(), CallError> {
    let Some(bounds) = bounds else {
        return Ok(());
    };
    let family = reader.info()?.address_family;
    if matches!(
        (family, bounds.from),
        (iprange_livedb::AddressFamily::Ipv4, Key::V4(_))
            | (iprange_livedb::AddressFamily::Ipv6, Key::V6(_))
    ) {
        Ok(())
    } else {
        Err(
            BoundaryError::wrong_family("cursor bounds do not match the database address family")
                .into(),
        )
    }
}

pub(crate) fn decode_direction(value: u32) -> Result<RangeDirection, BoundaryError> {
    match value {
        1 => Ok(RangeDirection::Forward),
        2 => Ok(RangeDirection::Backward),
        _ => Err(BoundaryError::invalid_enum("unknown cursor direction")),
    }
}

pub(crate) unsafe fn decode_bounds(
    bounds: *const Range,
    direction: RangeDirection,
) -> Result<Option<CursorBounds>, BoundaryError> {
    if bounds.is_null() {
        return Ok(None);
    }
    // SAFETY: the non-null bounds pointer is validated before use.
    let bounds = unsafe { required_input(bounds, "cursor bounds are null")? };
    let (from, to) = ip::decode_range(*bounds)?;
    Ok(Some(CursorBounds {
        from,
        to,
        direction,
    }))
}

enum Clip {
    Yield(ReaderCursorItem),
    Skip,
    End,
}

fn clip(item: ReaderCursorItem, bounds: Option<CursorBounds>) -> Clip {
    let Some(bounds) = bounds else {
        return Clip::Yield(item);
    };
    match item {
        ReaderCursorItem::DirectV4(mut range) => clip_v4(&mut range.from, &mut range.to, bounds)
            .map_or_else(
                |result| result,
                |_| Clip::Yield(ReaderCursorItem::DirectV4(range)),
            ),
        ReaderCursorItem::DirectV6(mut range) => clip_v6(&mut range.from, &mut range.to, bounds)
            .map_or_else(
                |result| result,
                |_| Clip::Yield(ReaderCursorItem::DirectV6(range)),
            ),
        ReaderCursorItem::MembershipV4 {
            mut range,
            membership,
        } => clip_v4(&mut range.from, &mut range.to, bounds).map_or_else(
            |result| result,
            |_| Clip::Yield(ReaderCursorItem::MembershipV4 { range, membership }),
        ),
        ReaderCursorItem::MembershipV6 {
            mut range,
            membership,
        } => clip_v6(&mut range.from, &mut range.to, bounds).map_or_else(
            |result| result,
            |_| Clip::Yield(ReaderCursorItem::MembershipV6 { range, membership }),
        ),
        ReaderCursorItem::NetworkEnrichmentV1V4 {
            mut range,
            value,
            membership,
        } => clip_v4(&mut range.from, &mut range.to, bounds).map_or_else(
            |result| result,
            |_| {
                Clip::Yield(ReaderCursorItem::NetworkEnrichmentV1V4 {
                    range,
                    value,
                    membership,
                })
            },
        ),
        ReaderCursorItem::NetworkEnrichmentV1V6 {
            mut range,
            value,
            membership,
        } => clip_v6(&mut range.from, &mut range.to, bounds).map_or_else(
            |result| result,
            |_| {
                Clip::Yield(ReaderCursorItem::NetworkEnrichmentV1V6 {
                    range,
                    value,
                    membership,
                })
            },
        ),
        ReaderCursorItem::FeedV4(mut range) => clip_v4(&mut range.from, &mut range.to, bounds)
            .map_or_else(
                |result| result,
                |_| Clip::Yield(ReaderCursorItem::FeedV4(range)),
            ),
        ReaderCursorItem::FeedV6(mut range) => clip_v6(&mut range.from, &mut range.to, bounds)
            .map_or_else(
                |result| result,
                |_| Clip::Yield(ReaderCursorItem::FeedV6(range)),
            ),
    }
}

fn store_network_enrichment_range(
    reader: &std::sync::Arc<iprange_livedb::c_abi_support::Reader>,
    range: Range,
    value: iprange_livedb::NetworkEnrichmentV1,
    membership: Option<iprange_livedb::c_abi_support::MembershipToken>,
    borrowed: &mut Option<BorrowedMembershipViewHandle>,
    present: &mut u8,
    output: &mut NetworkEnrichmentV1Range,
) {
    *borrowed = membership.map(|membership| BorrowedMembershipViewHandle::new(reader, membership));
    *present = 1;
    *output = NetworkEnrichmentV1Range {
        range,
        value: crate::structured::encode(value),
        membership: borrowed.as_ref().map_or(std::ptr::null(), |view| view),
    };
}

fn clip_v4(
    from: &mut iprange_livedb::Ipv4Key,
    to: &mut iprange_livedb::Ipv4Key,
    bounds: CursorBounds,
) -> Result<(), Clip> {
    let (Key::V4(lower), Key::V4(upper)) = (bounds.from, bounds.to) else {
        return Err(Clip::End);
    };
    clip_range(from, to, lower, upper, bounds.direction)
}

fn clip_v6(
    from: &mut iprange_livedb::Ipv6Key,
    to: &mut iprange_livedb::Ipv6Key,
    bounds: CursorBounds,
) -> Result<(), Clip> {
    let (Key::V6(lower), Key::V6(upper)) = (bounds.from, bounds.to) else {
        return Err(Clip::End);
    };
    clip_range(from, to, lower, upper, bounds.direction)
}

fn clip_range<K: Copy + Ord>(
    from: &mut K,
    to: &mut K,
    lower: K,
    upper: K,
    direction: RangeDirection,
) -> Result<(), Clip> {
    if *to < lower {
        return Err(if direction == RangeDirection::Forward {
            Clip::Skip
        } else {
            Clip::End
        });
    }
    if *from > upper {
        return Err(if direction == RangeDirection::Forward {
            Clip::End
        } else {
            Clip::Skip
        });
    }
    *from = (*from).max(lower);
    *to = (*to).min(upper);
    Ok(())
}

pub(crate) fn range_v4(range: iprange_livedb::AddressRange<iprange_livedb::Ipv4Key>) -> Range {
    Range {
        from: ip::encode(Key::V4(range.from)),
        to: ip::encode(Key::V4(range.to)),
    }
}

pub(crate) fn range_v6(range: iprange_livedb::AddressRange<iprange_livedb::Ipv6Key>) -> Range {
    Range {
        from: ip::encode(Key::V6(range.from)),
        to: ip::encode(Key::V6(range.to)),
    }
}

pub(crate) fn direct_v4(
    range: iprange_livedb::DirectRange<iprange_livedb::Ipv4Key>,
) -> DirectRange {
    DirectRange {
        range: range_v4(iprange_livedb::AddressRange {
            from: range.from,
            to: range.to,
        }),
        value: range.value,
        reserved: 0,
    }
}

pub(crate) fn direct_v6(
    range: iprange_livedb::DirectRange<iprange_livedb::Ipv6Key>,
) -> DirectRange {
    DirectRange {
        range: range_v6(iprange_livedb::AddressRange {
            from: range.from,
            to: range.to,
        }),
        value: range.value,
        reserved: 0,
    }
}
