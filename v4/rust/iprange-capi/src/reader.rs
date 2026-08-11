//! Reader point queries, metadata, and persistent membership views.

use std::mem::size_of;

use iprange_livedb::{AddressFamily, DirectSemantic, MetaSelection, ValueKind};

use crate::abi::{DatabaseInfo, FeedInfo, Ip, MutableByteSlice};
use crate::error::{
    call, call_with_output, call_with_outputs, output_buffer_slot, output_slice, output_slot,
    required_output, BoundaryError, CallError, ErrorHandle,
};
use crate::handle::{BorrowedMembershipViewHandle, MembershipViewHandle, ReaderHandle};
use crate::ip::{self, Key};
use crate::registry;

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_database_info(
    reader: *const ReaderHandle,
    output: *mut DatabaseInfo,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "database info output is null", || {
        // SAFETY: both pointers are validated before use.
        let reader =
            unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
        let output = unsafe { required_output(output, "database info output is null")? };
        let info = reader.get()?.info()?;
        *output = encode_database_info(info);
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_lookup_direct(
    reader: *const ReaderHandle,
    address: Ip,
    present: *mut u8,
    value: *mut u32,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(present, "presence output is null"),
            output_slot(value, "direct value output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before use.
            let reader =
                unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
            let present = unsafe { required_output(present, "presence output is null")? };
            let value = unsafe { required_output(value, "direct value output is null")? };
            *present = 0;
            *value = 0;
            store_direct_lookup(present, value, lookup_direct(reader, ip::decode(address)?)?);
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_lookup_membership(
    reader: *const ReaderHandle,
    address: Ip,
    output: *mut *mut MembershipViewHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        output,
        "membership view output is null",
        || {
            // SAFETY: both pointers are validated before use.
            let reader =
                unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
            let output = unsafe { required_output(output, "membership view output is null")? };
            *output = std::ptr::null_mut();
            store_membership_lookup(
                output,
                reader,
                lookup_membership(reader, ip::decode(address)?)?,
            )?;
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_lookup_feed(
    reader: *const ReaderHandle,
    name_pointer: *const u8,
    name_length: u64,
    present: *mut u8,
    output: *mut FeedInfo,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(present, "presence output is null"),
            output_slot(output, "feed output is null"),
        ],
        || {
            // SAFETY: pointers and extent are validated before use.
            let reader =
                unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
            let name = unsafe { crate::error::input_slice(name_pointer, name_length)? };
            let name = decode_feed_name(name)?;
            let present = unsafe { required_output(present, "presence output is null")? };
            let output = unsafe { required_output(output, "feed output is null")? };
            *present = 0;
            *output = FeedInfo::default();
            store_feed_lookup(present, output, reader.get()?.lookup_feed(name)?);
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_metadata_query(
    reader: *const ReaderHandle,
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
            let reader =
                unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
            let present = unsafe { required_output(present, "presence output is null")? };
            let required = unsafe { required_output(required, "required length output is null")? };
            let length = reader.get()?.metadata_json_len()?;
            *present = u8::from(length.is_some());
            *required = length.unwrap_or(0);
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_metadata_read(
    reader: *const ReaderHandle,
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
            let reader =
                unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
            let required = unsafe { required_output(required, "required length output is null")? };
            // SAFETY: the buffer extent was validated by call_with_outputs.
            unsafe { read_metadata(reader, output, required)? };
            Ok::<_, CallError>(())
        },
    )
}

fn lookup_direct(reader: &ReaderHandle, address: Key) -> Result<Option<u32>, CallError> {
    Ok(match address {
        Key::V4(address) => reader.get()?.lookup_direct_v4(address)?,
        Key::V6(address) => reader.get()?.lookup_direct_v6(address)?,
    })
}

fn store_direct_lookup(present: &mut u8, value: &mut u32, found: Option<u32>) {
    if let Some(found) = found {
        *present = 1;
        *value = found;
    }
}

fn lookup_membership(
    reader: &ReaderHandle,
    address: Key,
) -> Result<Option<iprange_livedb::c_abi_support::MembershipToken>, CallError> {
    Ok(match address {
        Key::V4(address) => reader.get()?.lookup_membership_token_v4(address)?,
        Key::V6(address) => reader.get()?.lookup_membership_token_v6(address)?,
    })
}

fn store_membership_lookup(
    output: &mut *mut MembershipViewHandle,
    reader: &ReaderHandle,
    membership: Option<iprange_livedb::c_abi_support::MembershipToken>,
) -> Result<(), CallError> {
    if let Some(membership) = membership {
        *output = Box::into_raw(Box::new(MembershipViewHandle::new(
            ArcClone::reader(reader)?,
            membership,
        )));
    }
    Ok(())
}

fn decode_feed_name(bytes: &[u8]) -> Result<&str, BoundaryError> {
    std::str::from_utf8(bytes)
        .map_err(|_| BoundaryError::invalid_argument("feed name is not UTF-8"))
}

fn store_feed_lookup(
    present: &mut u8,
    output: &mut FeedInfo,
    feed: Option<iprange_livedb::FeedEntry>,
) {
    if let Some(feed) = feed {
        *present = 1;
        *output = encode_feed(feed);
    }
}

unsafe fn read_metadata(
    reader: &ReaderHandle,
    output: MutableByteSlice,
    required: &mut u64,
) -> Result<(), CallError> {
    let Some(length) = reader.get()?.metadata_json_len()? else {
        *required = 0;
        return Ok(());
    };
    *required = length;
    if output.length < length {
        return Err(BoundaryError::buffer_too_small(length).into());
    }
    // SAFETY: call_with_outputs validated the caller's complete buffer.
    let output = unsafe { output_slice(output.pointer, output.length)? };
    reader
        .get()?
        .read_metadata_json(&mut output[..length as usize])?;
    Ok(())
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_view_word_count(
    view: *const MembershipViewHandle,
    output: *mut u32,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "word count output is null", || {
        // SAFETY: both pointers are validated before use.
        let view =
            unsafe { crate::handle::required_handle_input(view, "membership view is null")? };
        let output = unsafe { required_output(output, "word count output is null")? };
        *output = view.with(word_count)?;
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_view_word(
    view: *const MembershipViewHandle,
    index: u32,
    present: *mut u8,
    output: *mut u64,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(present, "presence output is null"),
            output_slot(output, "word output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before use.
            let view =
                unsafe { crate::handle::required_handle_input(view, "membership view is null")? };
            let present = unsafe { required_output(present, "presence output is null")? };
            let output = unsafe { required_output(output, "word output is null")? };
            *present = 0;
            *output = 0;
            if let Some(word) = view.with(|reader, address| word(reader, address, index))? {
                *present = 1;
                *output = word;
            }
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_view_read_words(
    view: *const MembershipViewHandle,
    start: u32,
    output_pointer: *mut u64,
    output_capacity: u64,
    copied: *mut u64,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_buffer_slot(output_pointer, output_capacity, "word output is invalid"),
            output_slot(copied, "copied count output is null"),
        ],
        || {
            // SAFETY: pointers and extent are validated before use.
            let view =
                unsafe { crate::handle::required_handle_input(view, "membership view is null")? };
            let output = unsafe { output_slice(output_pointer, output_capacity)? };
            let copied = unsafe { required_output(copied, "copied count output is null")? };
            *copied = view.with(|reader, address| words(reader, address, start, output))? as u64;
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_view_contains_index(
    view: *const MembershipViewHandle,
    feed_index: u32,
    contains: *mut u8,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, contains, "contains output is null", || {
        // SAFETY: both pointers are validated before use.
        let view =
            unsafe { crate::handle::required_handle_input(view, "membership view is null")? };
        let contains = unsafe { required_output(contains, "contains output is null")? };
        *contains =
            u8::from(view.with(|reader, address| contains_index(reader, address, feed_index))?);
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_view_close(
    view: *mut MembershipViewHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the handle pointer is validated before mutation.
        let view =
            unsafe { crate::handle::required_handle_input(view, "membership view is null")? };
        view.close()?;
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_view_destroy(
    view: *mut MembershipViewHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if view.is_null() {
        return crate::abi::STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the handle pointer is validated before inspection.
        let current =
            unsafe { crate::handle::required_handle_input(view, "membership view is null")? };
        if !current.is_closed()? {
            return Err(BoundaryError::handle_busy(
                "membership view must be closed before destroy",
            )
            .into());
        }
        // SAFETY: this consumes the unique ABI-owned allocation exactly once.
        unsafe { drop(Box::from_raw(view)) };
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_borrowed_membership_view_word_count(
    view: *const BorrowedMembershipViewHandle,
    output: *mut u32,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "word count output is null", || {
        // SAFETY: both pointers are validated before use.
        let view = unsafe {
            crate::handle::required_handle_input(view, "borrowed membership view is null")?
        };
        let output = unsafe { required_output(output, "word count output is null")? };
        *output = view.with(word_count)?;
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_borrowed_membership_view_word(
    view: *const BorrowedMembershipViewHandle,
    index: u32,
    present: *mut u8,
    output: *mut u64,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(present, "presence output is null"),
            output_slot(output, "word output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before use.
            let view = unsafe {
                crate::handle::required_handle_input(view, "borrowed membership view is null")?
            };
            let present = unsafe { required_output(present, "presence output is null")? };
            let output = unsafe { required_output(output, "word output is null")? };
            *present = 0;
            *output = 0;
            if let Some(word) = view.with(|reader, membership| word(reader, membership, index))? {
                *present = 1;
                *output = word;
            }
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_borrowed_membership_view_read_words(
    view: *const BorrowedMembershipViewHandle,
    start: u32,
    output_pointer: *mut u64,
    output_capacity: u64,
    copied: *mut u64,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_buffer_slot(output_pointer, output_capacity, "word output is invalid"),
            output_slot(copied, "copied count output is null"),
        ],
        || {
            // SAFETY: pointers and extent are validated before use.
            let view = unsafe {
                crate::handle::required_handle_input(view, "borrowed membership view is null")?
            };
            let output = unsafe { output_slice(output_pointer, output_capacity)? };
            let copied = unsafe { required_output(copied, "copied count output is null")? };
            *copied = 0;
            *copied =
                view.with(|reader, membership| words(reader, membership, start, output))? as u64;
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_borrowed_membership_view_contains_index(
    view: *const BorrowedMembershipViewHandle,
    feed_index: u32,
    contains: *mut u8,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, contains, "contains output is null", || {
        // SAFETY: both pointers are validated before use.
        let view = unsafe {
            crate::handle::required_handle_input(view, "borrowed membership view is null")?
        };
        let contains = unsafe { required_output(contains, "contains output is null")? };
        *contains = 0;
        *contains = u8::from(
            view.with(|reader, membership| contains_index(reader, membership, feed_index))?,
        );
        Ok::<_, CallError>(())
    })
}

struct ArcClone;

impl ArcClone {
    fn reader(
        reader: &ReaderHandle,
    ) -> Result<std::sync::Arc<iprange_livedb::c_abi_support::Reader>, BoundaryError> {
        Ok(reader.get()?.clone())
    }
}

fn word_count(
    reader: &iprange_livedb::c_abi_support::Reader,
    membership: iprange_livedb::c_abi_support::MembershipToken,
) -> Result<u32, CallError> {
    Ok(reader.membership_word_count(membership)?)
}

fn word(
    reader: &iprange_livedb::c_abi_support::Reader,
    membership: iprange_livedb::c_abi_support::MembershipToken,
    index: u32,
) -> Result<Option<u64>, CallError> {
    Ok(reader.membership_word(membership, index)?)
}

fn words(
    reader: &iprange_livedb::c_abi_support::Reader,
    membership: iprange_livedb::c_abi_support::MembershipToken,
    start: u32,
    output: &mut [u64],
) -> Result<usize, CallError> {
    Ok(reader.membership_words(membership, start, output)?)
}

fn contains_index(
    reader: &iprange_livedb::c_abi_support::Reader,
    membership: iprange_livedb::c_abi_support::MembershipToken,
    index: u32,
) -> Result<bool, CallError> {
    Ok(reader.membership_contains(membership, index)?)
}

fn encode_database_info(info: iprange_livedb::DatabaseInfo) -> DatabaseInfo {
    DatabaseInfo {
        abi_version: 1,
        struct_size: size_of::<DatabaseInfo>() as u32,
        address_family: match info.address_family {
            AddressFamily::Ipv4 => 4,
            AddressFamily::Ipv6 => 6,
        },
        value_kind: match info.value_kind {
            ValueKind::Direct => 1,
            ValueKind::Membership => 2,
            ValueKind::Structured => 3,
        },
        direct_semantic: match info.direct_semantic() {
            None => registry::DIRECT_SEMANTIC_NOT_APPLICABLE,
            Some(DirectSemantic::Generic) => registry::DIRECT_SEMANTIC_GENERIC,
            Some(DirectSemantic::FirstSeen) => registry::DIRECT_SEMANTIC_FIRST_SEEN,
            Some(DirectSemantic::LastSeen) => registry::DIRECT_SEMANTIC_LAST_SEEN,
        },
        structure_kind: info.structure_kind as u8 as u32,
        value_tag: *info.value_tag.as_wire(),
        database_id: info.database_id,
        transaction_id: info.transaction_id,
        commit_nonce: info.commit_nonce,
        page_count: info.page_count,
        range_record_count: info.range_record_count,
        active_feed_count: info.active_feed_count,
        meta_selection: match info.meta_selection {
            MetaSelection::ProvenCurrent => 1,
            MetaSelection::SoleMeta0 => 2,
            MetaSelection::SoleMeta1 => 3,
        },
        reserved2: 0,
    }
}

pub(crate) fn encode_feed(feed: iprange_livedb::FeedEntry) -> FeedInfo {
    let mut output = FeedInfo {
        index: feed.index,
        name_length: feed.name.as_bytes().len() as u32,
        ..FeedInfo::default()
    };
    output.name[..feed.name.as_bytes().len()].copy_from_slice(feed.name.as_bytes());
    output
}
