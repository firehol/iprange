//! Snapshot, publication resolution, and retained-residue exports.

use std::mem::size_of;

use iprange_livedb::publication::PublicationResolutionMode;
use iprange_livedb::{SnapshotPublicationPolicy, SnapshotSourceMode};

use crate::abi::{Cancellation, Path};
use crate::abi_extra::SnapshotBudget;
use crate::callback;
use crate::error::{
    call_with_output, required_input, required_output, BoundaryError, CallError, ErrorHandle,
};
use crate::facts;
use crate::obligation::ResidueHandle;
use crate::path;
use crate::report::ReportHandle;

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_snapshot_to(
    source: Path,
    source_mode: u32,
    destination: Path,
    destination_policy: u32,
    budget: *const SnapshotBudget,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before work starts.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let source_mode = decode_source_mode(source_mode)?;
        let destination_policy = decode_policy(destination_policy)?;
        let budget = unsafe { required_input(budget, "snapshot budget is null")? };
        let budget = decode_budget(budget)?;
        let cancellation = callback::token(cancellation)?;
        let source = unsafe { path::decode(source)? };
        let destination = unsafe { path::decode(destination)? };
        match iprange_livedb::snapshot_to(
            source,
            source_mode,
            destination,
            destination_policy,
            &budget,
            &cancellation,
        ) {
            Ok(result) => {
                *output = Box::into_raw(Box::new(ReportHandle::publication(result.publication)));
                Ok::<_, CallError>(())
            }
            Err(mut failure) => {
                let mut report = ReportHandle::snapshot_preparation(&failure);
                report.set_cleanup_guard(failure.source_cleanup.take());
                *output = Box::into_raw(Box::new(report));
                let cleanup = failure.cleanup.iter().map(facts::cleanup).collect();
                let error = ErrorHandle::publication_failure(failure.cause, cleanup, None);
                Err(error.into())
            }
        }
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_resolve_publication(
    destination: Path,
    supplied: *const ReportHandle,
    action: u32,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before work starts.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let supplied_report = if supplied.is_null() {
            None
        } else {
            Some(unsafe {
                crate::handle::required_handle_input(supplied, "publication report is null")?
            })
        };
        let _supplied_guard = supplied_report.map(ReportHandle::enter).transpose()?;
        let supplied = supplied_report
            .map(ReportHandle::publication_attempt)
            .transpose()?;
        let action = match action {
            1 => PublicationResolutionMode::Complete,
            2 => PublicationResolutionMode::Remove,
            _ => {
                return Err(
                    BoundaryError::invalid_enum("unknown publication resolution action").into(),
                )
            }
        };
        let cancellation = callback::token(cancellation)?;
        let destination = unsafe { path::decode(destination)? };
        let result =
            iprange_livedb::resolve_publication(destination, supplied, action, &cancellation)?;
        *output = Box::into_raw(Box::new(ReportHandle::publication(result)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_inspect_publication_residue(
    destination: Path,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: the output slot and tagged path are validated before work starts.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let cancellation = callback::token(cancellation)?;
        let destination = unsafe { path::decode(destination)? };
        let result = iprange_livedb::inspect_publication_residue(destination, &cancellation)?;
        *output = Box::into_raw(Box::new(ReportHandle::residue_inspection(result)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_remove_publication_residue(
    residue: *mut ResidueHandle,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers are validated before the retained authority is consumed.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let residue =
            unsafe { crate::handle::required_handle_input(residue, "residue handle is null")? };
        let cancellation = callback::token(cancellation)?;
        let result = iprange_livedb::remove_publication_residue(residue.take()?, &cancellation)?;
        *output = Box::into_raw(Box::new(ReportHandle::residue_removal(result)));
        Ok::<_, CallError>(())
    })
}

fn decode_source_mode(value: u32) -> Result<SnapshotSourceMode, BoundaryError> {
    match value {
        1 => Ok(SnapshotSourceMode::Immutable),
        2 => Ok(SnapshotSourceMode::Live),
        _ => Err(BoundaryError::invalid_enum("unknown snapshot source mode")),
    }
}

fn decode_policy(value: u32) -> Result<SnapshotPublicationPolicy, BoundaryError> {
    match value {
        1 => Ok(SnapshotPublicationPolicy::FailIfExists),
        2 => Ok(SnapshotPublicationPolicy::ReplaceExisting),
        3 => Ok(SnapshotPublicationPolicy::ReplaceExistingNoRollback),
        _ => Err(BoundaryError::invalid_enum(
            "unknown snapshot destination policy",
        )),
    }
}

fn decode_budget(value: &SnapshotBudget) -> Result<iprange_livedb::SnapshotBudget, BoundaryError> {
    if value.abi_version != 1 {
        return Err(BoundaryError::invalid_argument(
            "snapshot budget ABI version is not 1",
        ));
    }
    if value.struct_size != size_of::<SnapshotBudget>() as u32 {
        return Err(BoundaryError::invalid_length(
            "snapshot budget structure size is invalid",
        ));
    }
    if value.reserved != 0 {
        return Err(BoundaryError::reserved(
            "snapshot budget reserved field is nonzero",
        ));
    }
    Ok(iprange_livedb::SnapshotBudget::new(
        value.max_heap_bytes,
        value.max_output_pages,
        value.max_open_files,
    ))
}
