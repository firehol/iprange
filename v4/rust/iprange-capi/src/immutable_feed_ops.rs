//! One-inode immutable single-feed construction.

use std::ffi::c_void;
use std::mem::size_of;

use crate::abi::{ByteSlice, Cancellation, CoverageSourceFn, Path};
use crate::abi_sdk::{ImmutableFeedBudget, ImmutableFeedReport, OptionalByteSlice};
use crate::callback;
use crate::error::{
    call_with_outputs, input_slice, output_slot, required_input, required_output, BoundaryError,
    CallError, ErrorHandle,
};
use crate::membership::decode_name;
use crate::path;
use crate::report::ReportHandle;

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_create_immutable_feed(
    destination: Path,
    address_family: u32,
    value_tag: ByteSlice,
    feed_name: ByteSlice,
    metadata_json: OptionalByteSlice,
    destination_policy: u32,
    source_callback: CoverageSourceFn,
    source_context: *mut c_void,
    budget: *const ImmutableFeedBudget,
    cancellation: Cancellation,
    semantic_output: *mut ImmutableFeedReport,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(semantic_output, "immutable feed report is null"),
            output_slot(report_output, "publication report output is null"),
        ],
        || {
            // SAFETY: every pointer and tagged slice is validated before file creation.
            let semantic =
                unsafe { required_output(semantic_output, "immutable feed report is null")? };
            *semantic = ImmutableFeedReport::default();
            let report =
                unsafe { required_output(report_output, "publication report output is null")? };
            *report = std::ptr::null_mut();
            if source_callback.is_none() {
                return Err(BoundaryError::null("coverage source callback is null").into());
            }
            let family = crate::lifecycle_ops::decode_family(address_family)?;
            let value_tag = unsafe { crate::lifecycle_ops::decode_value_tag(value_tag)? };
            let feed_name = unsafe { decode_name(feed_name.pointer, feed_name.length)? };
            let metadata = unsafe { decode_optional_bytes(metadata_json)? };
            let policy = crate::publication_ops::decode_policy(destination_policy)?;
            let budget =
                unsafe { decode_budget(required_input(budget, "immutable feed budget is null")?)? };
            let cancellation = callback::token(cancellation)?;
            let destination = unsafe { path::decode(destination)? };

            match family {
                iprange_livedb::AddressFamily::Ipv4 => {
                    let mut source =
                        crate::source::CoverageV4::new(source_callback, source_context);
                    let outcome = iprange_livedb::create_immutable_feed_v4(
                        destination,
                        value_tag,
                        feed_name,
                        metadata,
                        policy,
                        &mut source,
                        &budget,
                        &cancellation,
                    );
                    finish(outcome, source.take_failure(), semantic, report)
                }
                iprange_livedb::AddressFamily::Ipv6 => {
                    let mut source =
                        crate::source::CoverageV6::new(source_callback, source_context);
                    let outcome = iprange_livedb::create_immutable_feed_v6(
                        destination,
                        value_tag,
                        feed_name,
                        metadata,
                        policy,
                        &mut source,
                        &budget,
                        &cancellation,
                    );
                    finish(outcome, source.take_failure(), semantic, report)
                }
            }
        },
    )
}

fn finish(
    outcome: iprange_livedb::ImmutableFeedOutcome,
    callback: Option<CallError>,
    semantic: &mut ImmutableFeedReport,
    report_output: &mut *mut ReportHandle,
) -> Result<(), CallError> {
    match outcome {
        Ok(result) => {
            *semantic = ImmutableFeedReport {
                abi_version: 1,
                struct_size: size_of::<ImmutableFeedReport>() as u32,
                input_record_count: result.report.input_record_count,
                normalized_interval_count: result.report.normalized_interval_count,
                addresses: crate::report::cardinality(result.report.addresses),
            };
            *report_output = Box::into_raw(Box::new(ReportHandle::publication(result.publication)));
            if let Some(callback) = callback {
                return Err(callback);
            }
            Ok(())
        }
        Err(mut failure) => {
            let mut report = ReportHandle::publication_preparation(
                &failure,
                crate::registry::RESIDUE_OPERATION_IMMUTABLE_FEED_PREPARATION_FAILURE,
            );
            report.set_cleanup_guard(failure.source_cleanup.take());
            *report_output = Box::into_raw(Box::new(report));
            let cleanup = failure.cleanup.iter().map(crate::facts::cleanup).collect();
            let error = match callback {
                Some(callback) => {
                    ErrorHandle::callback_publication_failure(callback, failure.cause, cleanup)
                }
                None => ErrorHandle::publication_failure(failure.cause, cleanup, None),
            };
            Err(error.into())
        }
    }
}

pub(crate) unsafe fn decode_optional_bytes<'a>(
    value: OptionalByteSlice,
) -> Result<Option<&'a [u8]>, BoundaryError> {
    if value.reserved != [0; 7] {
        return Err(BoundaryError::reserved(
            "optional byte slice reserved field is nonzero",
        ));
    }
    match value.present {
        0 => {
            if !value.value.pointer.is_null() || value.value.length != 0 {
                return Err(BoundaryError::invalid_argument(
                    "absent optional byte slice is not empty",
                ));
            }
            Ok(None)
        }
        1 => {
            // SAFETY: the caller supplies the complete readable byte extent.
            Ok(Some(unsafe {
                input_slice(value.value.pointer, value.value.length)?
            }))
        }
        _ => Err(BoundaryError::invalid_argument(
            "optional byte slice presence is not zero or one",
        )),
    }
}

fn decode_budget(
    value: &ImmutableFeedBudget,
) -> Result<iprange_livedb::ImmutableFeedBudget, BoundaryError> {
    if value.abi_version != 1 {
        return Err(BoundaryError::invalid_argument(
            "immutable feed budget ABI version is not 1",
        ));
    }
    if value.struct_size != size_of::<ImmutableFeedBudget>() as u32 {
        return Err(BoundaryError::invalid_length(
            "immutable feed budget structure size is invalid",
        ));
    }
    if value.reserved != 0 {
        return Err(BoundaryError::reserved(
            "immutable feed budget reserved field is nonzero",
        ));
    }
    Ok(iprange_livedb::ImmutableFeedBudget::new(
        value.max_heap_bytes,
        value.max_output_pages,
        value.max_workspace_pages,
        value.max_open_files,
    ))
}
