//! Typed C adapter for the modular `network_enrichment_v1` structure.

use std::ffi::c_void;

use iprange_livedb::c_abi_support::NetworkEnrichmentV1Token;
use iprange_livedb::{NetworkEnrichmentV1 as CoreValue, NetworkEnrichmentV1Location};

use crate::abi::{Cancellation, CoverageSourceFn, Ip, NetworkEnrichmentV1, STATUS_OK};
use crate::callback;
use crate::error::{
    call, call_with_output, call_with_outputs, output_slot, required_output, BoundaryError,
    CallError, ErrorHandle,
};
use crate::handle::{
    MembershipRefHandle, MembershipViewHandle, ReaderHandle, StructureRefHandle, WriterHandle,
};
use crate::ip::{self, Key};
use crate::source;
use crate::writer::finish_source;

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_lookup_network_enrichment_v1(
    reader: *const ReaderHandle,
    address: Ip,
    present: *mut u8,
    value: *mut NetworkEnrichmentV1,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(present, "presence output is null"),
            output_slot(value, "network enrichment output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before use.
            let reader =
                unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
            let present = unsafe { required_output(present, "presence output is null")? };
            let value = unsafe { required_output(value, "network enrichment output is null")? };
            *present = 0;
            *value = NetworkEnrichmentV1::default();
            if let Some(found) = lookup(reader, ip::decode(address)?)? {
                *present = 1;
                *value = encode(found.value);
            }
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_lookup_network_enrichment_v1_with_membership(
    reader: *const ReaderHandle,
    address: Ip,
    present: *mut u8,
    value: *mut NetworkEnrichmentV1,
    membership: *mut *mut MembershipViewHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(present, "presence output is null"),
            output_slot(value, "network enrichment output is null"),
            output_slot(membership, "membership view output is null"),
        ],
        || {
            // SAFETY: all pointers are validated before use.
            let reader =
                unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
            let present = unsafe { required_output(present, "presence output is null")? };
            let value = unsafe { required_output(value, "network enrichment output is null")? };
            let membership =
                unsafe { required_output(membership, "membership view output is null")? };
            *present = 0;
            *value = NetworkEnrichmentV1::default();
            *membership = std::ptr::null_mut();
            if let Some(found) = lookup(reader, ip::decode(address)?)? {
                *present = 1;
                *value = encode(found.value);
                if let Some(token) = found.membership {
                    *membership = Box::into_raw(Box::new(MembershipViewHandle::new(
                        reader.get()?.clone(),
                        token,
                    )));
                }
            }
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_begin_structured(
    writer: *const WriterHandle,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque handle pointer is validated before use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let cancellation = callback::token(cancellation)?;
        writer.with_mut(|inner| {
            inner.begin_structured(&cancellation)?;
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_network_enrichment_v1_intern(
    writer: *const WriterHandle,
    value: NetworkEnrichmentV1,
    membership: *const MembershipRefHandle,
    output: *mut *mut StructureRefHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        output,
        "structure reference output is null",
        || {
            // SAFETY: opaque pointers are validated before typed use.
            let writer =
                unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
            let output = unsafe { required_output(output, "structure reference output is null")? };
            *output = std::ptr::null_mut();
            let value = decode(value)?;
            let (membership, _guard) = if membership.is_null() {
                (None, None)
            } else {
                let membership = unsafe {
                    crate::handle::required_handle_input(
                        membership,
                        "membership reference is null",
                    )?
                };
                let guard = membership.enter()?;
                membership.require_parent(writer)?;
                (Some(membership.value), Some(guard))
            };
            let structure = writer
                .with_mut(|inner| Ok(inner.network_enrichment_v1_intern(value, membership)?))?;
            *output = Box::into_raw(Box::new(StructureRefHandle::new(writer, structure)));
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_structure_ref_destroy(
    structure: *mut StructureRefHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if structure.is_null() {
        return STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before ownership is consumed.
        let current = unsafe {
            crate::handle::required_handle_input(structure, "structure reference is null")?
        };
        let guard = current.enter()?;
        current.parent().remove_child();
        drop(guard);
        // SAFETY: this consumes the unique ABI-owned allocation exactly once.
        unsafe { drop(Box::from_raw(structure)) };
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_structured_assign_ranges(
    writer: *const WriterHandle,
    structure: *const StructureRefHandle,
    callback: CoverageSourceFn,
    context: *mut c_void,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: opaque pointers are validated before typed use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let structure = unsafe {
            crate::handle::required_handle_input(structure, "structure reference is null")?
        };
        let _structure_guard = structure.enter()?;
        structure.require_parent(writer)?;
        writer.with_mut(|inner| {
            let result = source::drain_coverage(callback, context, |records| {
                for record in records {
                    assign(inner, *record, structure.value)?;
                }
                Ok(())
            });
            finish_source(inner, result)
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_structured_clear_ranges(
    writer: *const WriterHandle,
    callback: CoverageSourceFn,
    context: *mut c_void,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque pointer is validated before typed use.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        writer.with_mut(|inner| {
            let result = source::drain_coverage(callback, context, |records| {
                for record in records {
                    clear(inner, *record)?;
                }
                Ok(())
            });
            finish_source(inner, result)
        })
    })
}

pub(crate) fn encode(value: CoreValue) -> NetworkEnrichmentV1 {
    let location = value.location;
    NetworkEnrichmentV1 {
        asn: value.asn,
        country_id: value.country_id,
        state_id: value.state_id,
        city_id: value.city_id,
        latitude_microdegrees: location.map_or(0, |value| value.latitude_microdegrees),
        longitude_microdegrees: location.map_or(0, |value| value.longitude_microdegrees),
        has_location: u32::from(location.is_some()),
        reserved: 0,
    }
}

fn decode(value: NetworkEnrichmentV1) -> Result<CoreValue, CallError> {
    if value.reserved != 0 {
        return Err(BoundaryError::reserved("network enrichment reserved field is nonzero").into());
    }
    let location = match value.has_location {
        0 => {
            if value.latitude_microdegrees != 0 || value.longitude_microdegrees != 0 {
                return Err(BoundaryError::invalid_argument(
                    "absent network location has nonzero coordinates",
                )
                .into());
            }
            None
        }
        1 => Some(NetworkEnrichmentV1Location {
            latitude_microdegrees: value.latitude_microdegrees,
            longitude_microdegrees: value.longitude_microdegrees,
        }),
        _ => {
            return Err(BoundaryError::invalid_enum(
                "network enrichment location presence is not zero or one",
            )
            .into())
        }
    };
    Ok(CoreValue {
        asn: value.asn,
        country_id: value.country_id,
        state_id: value.state_id,
        city_id: value.city_id,
        location,
    })
}

fn lookup(
    reader: &ReaderHandle,
    address: Key,
) -> Result<Option<NetworkEnrichmentV1Token>, CallError> {
    Ok(match address {
        Key::V4(address) => reader.get()?.lookup_network_enrichment_v1_v4(address)?,
        Key::V6(address) => reader.get()?.lookup_network_enrichment_v1_v6(address)?,
    })
}

fn assign(
    writer: &mut iprange_livedb::c_abi_support::Writer,
    range: crate::abi::Range,
    structure: iprange_livedb::StructureRef,
) -> Result<(), CallError> {
    match ip::decode_range(range)? {
        (Key::V4(from), Key::V4(to)) => {
            writer.structured_assign_v4(from, to, structure)?;
        }
        (Key::V6(from), Key::V6(to)) => {
            writer.structured_assign_v6(from, to, structure)?;
        }
        _ => unreachable!("range decoder returns one matching family"),
    }
    Ok(())
}

fn clear(
    writer: &mut iprange_livedb::c_abi_support::Writer,
    range: crate::abi::Range,
) -> Result<(), CallError> {
    match ip::decode_range(range)? {
        (Key::V4(from), Key::V4(to)) => {
            writer.structured_clear_v4(from, to)?;
        }
        (Key::V6(from), Key::V6(to)) => {
            writer.structured_clear_v6(from, to)?;
        }
        _ => unreachable!("range decoder returns one matching family"),
    }
    Ok(())
}
