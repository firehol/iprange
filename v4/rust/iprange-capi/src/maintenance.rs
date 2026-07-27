//! Explicit abandoned-artifact discovery and exact removal.

use std::ffi::c_void;

use iprange_livedb::publication;
use iprange_livedb::recovery;

use crate::abi::{Cancellation, LocalIdentity, Path};
use crate::abi_extra::{
    ArtifactSinkFn, HousekeepingPayload, HousekeepingSinkFn, PublicationDigest, PublicationTuple,
};
use crate::callback;
use crate::error::{
    call_with_output, input_slice, required_input, required_output, BoundaryError, CallError,
    ErrorHandle,
};
use crate::maintenance_encode;
use crate::path;
use crate::registry;
use crate::report::ReportHandle;
use crate::sink::{self, Control};

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_list_abandoned_scratch(
    directory: Path,
    cancellation: Cancellation,
    sink_callback: ArtifactSinkFn,
    sink_context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before the scan.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        require_artifact_sink(sink_callback)?;
        let cancellation = callback::token(cancellation)?;
        let directory = unsafe { path::decode(directory)? };
        let mut adapter = ScratchSink::new(sink_callback, sink_context);
        match recovery::list_abandoned_scratch(directory, &cancellation, &mut adapter) {
            Ok(result) => {
                *output = Box::into_raw(Box::new(ReportHandle::maintenance_list(
                    registry::RESIDUE_OPERATION_LIST_ABANDONED_SCRATCH,
                    result.directory_identity,
                    result.entries,
                )));
                Ok(())
            }
            Err(error) => list_failure(
                output,
                adapter.state,
                registry::RESIDUE_OPERATION_LIST_ABANDONED_SCRATCH,
                error,
            ),
        }
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_list_abandoned_publication_temps(
    directory: Path,
    cancellation: Cancellation,
    sink_callback: ArtifactSinkFn,
    sink_context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before the scan.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        require_artifact_sink(sink_callback)?;
        let cancellation = callback::token(cancellation)?;
        let directory = unsafe { path::decode(directory)? };
        let mut adapter = PublicationTempSink::new(sink_callback, sink_context);
        match publication::list_abandoned_publication_temps(directory, &cancellation, &mut adapter)
        {
            Ok(result) => {
                *output = Box::into_raw(Box::new(ReportHandle::maintenance_list(
                    registry::RESIDUE_OPERATION_LIST_ABANDONED_PUBLICATION_TEMPS,
                    result.directory_identity,
                    result.entries,
                )));
                Ok(())
            }
            Err(error) => list_failure(
                output,
                adapter.state,
                registry::RESIDUE_OPERATION_LIST_ABANDONED_PUBLICATION_TEMPS,
                error,
            ),
        }
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_list_abandoned_reservation_artifacts(
    directory: Path,
    cancellation: Cancellation,
    sink_callback: ArtifactSinkFn,
    sink_context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before the scan.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        require_artifact_sink(sink_callback)?;
        let cancellation = callback::token(cancellation)?;
        let directory = unsafe { path::decode(directory)? };
        let mut adapter = ReservationSink::new(sink_callback, sink_context);
        match publication::list_abandoned_reservation_artifacts(
            directory,
            &cancellation,
            &mut adapter,
        ) {
            Ok(result) => {
                *output = Box::into_raw(Box::new(ReportHandle::maintenance_list(
                    registry::RESIDUE_OPERATION_LIST_ABANDONED_RESERVATION_ARTIFACTS,
                    result.directory_identity,
                    result.entries,
                )));
                Ok(())
            }
            Err(error) => list_failure(
                output,
                adapter.state,
                registry::RESIDUE_OPERATION_LIST_ABANDONED_RESERVATION_ARTIFACTS,
                error,
            ),
        }
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_list_housekeeping_artifacts(
    directory: Path,
    cancellation: Cancellation,
    sink_callback: HousekeepingSinkFn,
    sink_context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before the scan.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        if sink_callback.is_none() {
            return Err(BoundaryError::null("housekeeping sink callback is null").into());
        }
        let cancellation = callback::token(cancellation)?;
        let directory = unsafe { path::decode(directory)? };
        let mut adapter = HousekeepingSink::new(sink_callback, sink_context);
        match publication::list_windows_housekeeping(directory, &cancellation, &mut adapter) {
            Ok(result) => {
                *output = Box::into_raw(Box::new(ReportHandle::maintenance_list(
                    registry::RESIDUE_OPERATION_LIST_HOUSEKEEPING_ARTIFACTS,
                    result.directory_identity,
                    result.entries,
                )));
                Ok(())
            }
            Err(error) => list_failure(
                output,
                adapter.state,
                registry::RESIDUE_OPERATION_LIST_HOUSEKEEPING_ARTIFACTS,
                error,
            ),
        }
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_remove_abandoned_scratch(
    directory: Path,
    expected_directory_identity: LocalIdentity,
    attempt_id: *const u8,
    ordinal: u32,
    expected_artifact_identity: LocalIdentity,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before mutation.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let expected_directory_identity = decode_identity(expected_directory_identity)?;
        let expected_artifact_identity = decode_identity(expected_artifact_identity)?;
        let attempt_id = unsafe { decode_id(attempt_id)? };
        let cancellation = callback::token(cancellation)?;
        let directory = unsafe { path::decode(directory)? };
        let result = recovery::remove_abandoned_scratch(
            directory,
            expected_directory_identity,
            attempt_id,
            ordinal,
            expected_artifact_identity,
            &cancellation,
        )?;
        *output = Box::into_raw(Box::new(ReportHandle::abandoned_removal(
            registry::RESIDUE_OPERATION_REMOVE_ABANDONED_SCRATCH,
            result,
        )));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_remove_abandoned_publication_temp(
    directory: Path,
    expected_directory_identity: LocalIdentity,
    publication_attempt_id: *const u8,
    expected_artifact_identity: LocalIdentity,
    expected_tuple: *const PublicationTuple,
    expected_digest: *const PublicationDigest,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before mutation.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let expected_directory_identity = decode_identity(expected_directory_identity)?;
        let expected_artifact_identity = decode_identity(expected_artifact_identity)?;
        let publication_attempt_id = unsafe { decode_id(publication_attempt_id)? };
        let expected_tuple = unsafe { optional_tuple(expected_tuple)? };
        let expected_digest = unsafe { optional_digest(expected_digest)? };
        let cancellation = callback::token(cancellation)?;
        let directory = unsafe { path::decode(directory)? };
        let result = publication::remove_abandoned_publication_temp(
            directory,
            expected_directory_identity,
            publication_attempt_id,
            expected_artifact_identity,
            expected_tuple,
            expected_digest,
            &cancellation,
        )?;
        *output = Box::into_raw(Box::new(ReportHandle::abandoned_removal(
            registry::RESIDUE_OPERATION_REMOVE_ABANDONED_PUBLICATION_TEMP,
            result,
        )));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_remove_abandoned_reservation_artifact(
    directory: Path,
    expected_directory_identity: LocalIdentity,
    publication_attempt_id: *const u8,
    expected_artifact_identity: LocalIdentity,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before mutation.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let expected_directory_identity = decode_identity(expected_directory_identity)?;
        let expected_artifact_identity = decode_identity(expected_artifact_identity)?;
        let publication_attempt_id = unsafe { decode_id(publication_attempt_id)? };
        let cancellation = callback::token(cancellation)?;
        let directory = unsafe { path::decode(directory)? };
        let result = publication::remove_abandoned_reservation_artifact(
            directory,
            expected_directory_identity,
            publication_attempt_id,
            expected_artifact_identity,
            &cancellation,
        )?;
        *output = Box::into_raw(Box::new(ReportHandle::abandoned_removal(
            registry::RESIDUE_OPERATION_REMOVE_ABANDONED_RESERVATION_ARTIFACT,
            result,
        )));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_remove_housekeeping_artifact(
    directory: Path,
    expected_directory_identity: LocalIdentity,
    attempt_id: *const u8,
    ordinal: u32,
    expected_envelope_identity: LocalIdentity,
    expected_payload: *const HousekeepingPayload,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before mutation.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let expected_directory_identity = decode_identity(expected_directory_identity)?;
        let expected_envelope_identity = decode_identity(expected_envelope_identity)?;
        let attempt_id = unsafe { decode_id(attempt_id)? };
        let expected_payload = unsafe { optional_payload(expected_payload)? };
        let cancellation = callback::token(cancellation)?;
        let directory = unsafe { path::decode(directory)? };
        let result = publication::remove_windows_housekeeping(
            directory,
            expected_directory_identity,
            attempt_id,
            ordinal,
            expected_envelope_identity,
            expected_payload,
            &cancellation,
        )?;
        *output = Box::into_raw(Box::new(ReportHandle::housekeeping_removal(result)));
        Ok::<_, CallError>(())
    })
}

#[derive(Default)]
struct ListState {
    count: u64,
    directory: Option<iprange_livedb::validation::LocalFileIdentity>,
    failure: Option<CallError>,
}

fn list_failure(
    output: &mut *mut ReportHandle,
    state: ListState,
    operation: u32,
    error: iprange_livedb::Error,
) -> Result<(), CallError> {
    if let Some(directory) = state.directory {
        *output = Box::into_raw(Box::new(ReportHandle::maintenance_list(
            operation,
            directory,
            state.count,
        )));
    }
    Err(state.failure.unwrap_or_else(|| error.into()))
}

macro_rules! artifact_adapter {
    ($name:ident, $trait:path, $entry:ty, $control:path, $encode:path, $label:literal) => {
        struct $name {
            callback: ArtifactSinkFn,
            context: *mut c_void,
            state: ListState,
        }

        impl $name {
            fn new(callback: ArtifactSinkFn, context: *mut c_void) -> Self {
                Self {
                    callback,
                    context,
                    state: ListState::default(),
                }
            }
        }

        impl $trait for $name {
            fn entry(&mut self, entry: &$entry) -> iprange_livedb::Result<$control> {
                self.state.directory = Some(entry.directory_identity);
                self.state.count = self.state.count.checked_add(1).ok_or(
                    iprange_livedb::Error::ArithmeticOverflow("C maintenance entry count"),
                )?;
                let record = $encode(entry);
                match sink::records(self.callback, self.context, &[record], $label) {
                    Ok(Control::Continue) => Ok(<$control>::Continue),
                    Ok(Control::Stop) => Ok(<$control>::Stop),
                    Err(error) => {
                        self.state.failure = Some(error);
                        Err(iprange_livedb::Error::InvalidArgument(
                            "C maintenance sink failed",
                        ))
                    }
                }
            }
        }
    };
}

artifact_adapter!(
    ScratchSink,
    recovery::AbandonedScratchSink,
    recovery::AbandonedScratchEntry,
    recovery::AbandonedScratchSinkControl,
    maintenance_encode::scratch,
    "abandoned scratch sink"
);
artifact_adapter!(
    PublicationTempSink,
    publication::AbandonedPublicationTempSink,
    publication::AbandonedPublicationTempEntry,
    publication::AbandonedPublicationTempSinkControl,
    maintenance_encode::publication_temp,
    "publication temp sink"
);
artifact_adapter!(
    ReservationSink,
    publication::AbandonedReservationSink,
    publication::AbandonedReservationEntry,
    publication::AbandonedReservationSinkControl,
    maintenance_encode::reservation,
    "publication reservation sink"
);

struct HousekeepingSink {
    callback: HousekeepingSinkFn,
    context: *mut c_void,
    state: ListState,
}

impl HousekeepingSink {
    fn new(callback: HousekeepingSinkFn, context: *mut c_void) -> Self {
        Self {
            callback,
            context,
            state: ListState::default(),
        }
    }
}

impl publication::WindowsHousekeepingSink for HousekeepingSink {
    fn entry(
        &mut self,
        entry: &publication::WindowsHousekeepingEntry,
    ) -> iprange_livedb::Result<publication::WindowsHousekeepingSinkControl> {
        self.state.directory = Some(entry.directory_identity);
        self.state.count =
            self.state
                .count
                .checked_add(1)
                .ok_or(iprange_livedb::Error::ArithmeticOverflow(
                    "C housekeeping entry count",
                ))?;
        let record = maintenance_encode::housekeeping(entry);
        match sink::records(self.callback, self.context, &[record], "housekeeping sink") {
            Ok(Control::Continue) => Ok(publication::WindowsHousekeepingSinkControl::Continue),
            Ok(Control::Stop) => Ok(publication::WindowsHousekeepingSinkControl::Stop),
            Err(error) => {
                self.state.failure = Some(error);
                Err(iprange_livedb::Error::InvalidArgument(
                    "C housekeeping sink failed",
                ))
            }
        }
    }
}

fn require_artifact_sink(callback: ArtifactSinkFn) -> Result<(), BoundaryError> {
    if callback.is_none() {
        Err(BoundaryError::null("artifact sink callback is null"))
    } else {
        Ok(())
    }
}

fn decode_identity(
    value: LocalIdentity,
) -> Result<iprange_livedb::validation::LocalFileIdentity, BoundaryError> {
    if value.reserved != 0 {
        return Err(BoundaryError::reserved("local identity reserved field"));
    }
    let kind = u16::try_from(value.kind)
        .ok()
        .filter(|kind| matches!(*kind, 1 | 2))
        .ok_or_else(|| BoundaryError::invalid_enum("unknown local identity kind"))?;
    Ok(iprange_livedb::validation::LocalFileIdentity {
        kind,
        bytes: value.bytes,
    })
}

unsafe fn decode_id(pointer: *const u8) -> Result<[u8; 16], BoundaryError> {
    // SAFETY: the caller supplies exactly sixteen readable bytes.
    let bytes = unsafe { input_slice(pointer, 16)? };
    Ok(bytes.try_into().expect("fixed input length"))
}

unsafe fn optional_tuple(
    pointer: *const PublicationTuple,
) -> Result<Option<publication::PublicationTuple>, BoundaryError> {
    if pointer.is_null() {
        return Ok(None);
    }
    // SAFETY: the non-null fixed input is validated before use.
    let value = unsafe { required_input(pointer, "publication tuple is null")? };
    Ok(Some(publication::PublicationTuple {
        database_id: value.database_id,
        transaction_id: value.transaction_id,
        commit_nonce: value.commit_nonce,
    }))
}

unsafe fn optional_digest(
    pointer: *const PublicationDigest,
) -> Result<Option<publication::PublicationDigest>, BoundaryError> {
    if pointer.is_null() {
        return Ok(None);
    }
    // SAFETY: the non-null fixed input is validated before use.
    let value = unsafe { required_input(pointer, "publication digest is null")? };
    Ok(Some(publication::PublicationDigest {
        byte_length: value.byte_length,
        sha512: value.sha512,
    }))
}

unsafe fn optional_payload(
    pointer: *const HousekeepingPayload,
) -> Result<Option<publication::HousekeepingPayloadIdentity>, BoundaryError> {
    if pointer.is_null() {
        return Ok(None);
    }
    // SAFETY: the non-null fixed input is validated before use.
    let value = unsafe { required_input(pointer, "housekeeping payload is null")? };
    if value.reserved.iter().any(|&byte| byte != 0) {
        return Err(BoundaryError::reserved(
            "housekeeping payload reserved field",
        ));
    }
    let tuple = match value.tuple_present {
        0 => None,
        1 => Some(publication::PublicationTuple {
            database_id: value.tuple.database_id,
            transaction_id: value.tuple.transaction_id,
            commit_nonce: value.tuple.commit_nonce,
        }),
        _ => {
            return Err(BoundaryError::invalid_enum(
                "housekeeping tuple presence must be zero or one",
            ))
        }
    };
    Ok(Some(publication::HousekeepingPayloadIdentity {
        tuple,
        digest: publication::PublicationDigest {
            byte_length: value.digest.byte_length,
            sha512: value.digest.sha512,
        },
    }))
}
