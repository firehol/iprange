//! Explicit validation and recovery exports.

use std::ffi::c_void;
use std::mem::size_of;
use std::path::PathBuf;

use iprange_livedb::recovery::{
    OfflineQuiescenceCertification, RecoveryInspectionMode, RecoverySinkControl,
};
use iprange_livedb::validation::{ValidationMode, ValidationSinkControl};

use crate::abi::{Cancellation, Path};
use crate::abi_extra::{
    RecoveryBudget, RecoveryUnknownSinkFn, ValidationBudget, ValidationFindingSinkFn,
};
use crate::callback;
use crate::error::{
    call_with_output, required_input, required_output, BoundaryError, CallError, ErrorHandle,
};
use crate::facts;
use crate::path;
use crate::report::ReportHandle;
use crate::sink::{self, Control};

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_validate(
    source: Path,
    mode: u32,
    candidate_report: *const ReportHandle,
    candidate_index: u64,
    budget: *const ValidationBudget,
    cancellation: Cancellation,
    sink_callback: ValidationFindingSinkFn,
    sink_context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: all pointers and tagged inputs are validated before work starts.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let budget = unsafe { required_input(budget, "validation budget is null")? };
        let budget = unsafe { decode_validation_budget(budget)? };
        let mode = unsafe { decode_validation_mode(mode, candidate_report, candidate_index)? };
        let cancellation = callback::token(cancellation)?;
        let source = unsafe { path::decode(source)? };
        let mut adapter = ValidationAdapter::new(sink_callback, sink_context);
        let result = iprange_livedb::validation::validate(
            source,
            mode,
            &budget,
            &cancellation,
            &mut adapter,
        );
        finish_validation(output, result, adapter)
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_inspect_recovery_candidates(
    source: Path,
    source_mode: u32,
    budget: *const ValidationBudget,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: all pointers and tagged inputs are validated before work starts.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let budget = unsafe { required_input(budget, "validation budget is null")? };
        let budget = unsafe { decode_validation_budget(budget)? };
        let mode = match source_mode {
            1 => RecoveryInspectionMode::Immutable,
            2 => RecoveryInspectionMode::Live,
            3 => RecoveryInspectionMode::Offline,
            _ => return Err(BoundaryError::invalid_enum("unknown recovery source mode").into()),
        };
        let cancellation = callback::token(cancellation)?;
        let source = unsafe { path::decode(source)? };
        let result = iprange_livedb::recovery::inspect_recovery_candidates(
            source,
            mode,
            &budget,
            &cancellation,
        )?;
        *output = Box::into_raw(Box::new(ReportHandle::recovery_candidates(result)));
        Ok::<_, CallError>(())
    })
}

#[derive(Clone, Copy)]
enum RecoveryMode {
    Immutable,
    Live,
    Offline,
}

struct RecoveryRequest {
    mode: RecoveryMode,
    source: Path,
    candidate_report: *const ReportHandle,
    candidate_index: u64,
    destination: Path,
    budget: *const RecoveryBudget,
    cancellation: Cancellation,
    sink_callback: RecoveryUnknownSinkFn,
    sink_context: *mut c_void,
}

fn recover(
    request: RecoveryRequest,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: all pointers and tagged inputs are validated before work starts.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let candidates = unsafe {
            crate::handle::required_handle_input(
                request.candidate_report,
                "candidate report is null",
            )?
        };
        let _candidate_guard = candidates.enter()?;
        let candidate = candidates.candidate(request.candidate_index)?;
        let inputs = unsafe { decode_recovery_inputs(&request)? };
        let mut adapter = RecoveryAdapter::new(request.sink_callback, request.sink_context);
        let result = dispatch_recovery(
            request.mode,
            inputs.source,
            candidate,
            inputs.destination,
            &inputs.budget,
            &mut adapter,
            &inputs.cancellation,
        );
        finish_recovery(output, result, adapter)
    })
}

fn finish_validation(
    output: &mut *mut ReportHandle,
    result: Result<
        iprange_livedb::validation::ValidationResult,
        iprange_livedb::validation::ValidationFailure,
    >,
    adapter: ValidationAdapter,
) -> Result<(), CallError> {
    match result {
        Ok(result) => {
            *output = Box::into_raw(Box::new(ReportHandle::validation(result)));
            Ok(())
        }
        Err(mut failure) => {
            let mut report = ReportHandle::validation_failure(&failure);
            let cleanup_failed = failure.source_cleanup.is_some();
            report.set_cleanup_guard(failure.source_cleanup.take());
            *output = Box::into_raw(Box::new(report));
            match adapter.failure {
                Some(callback) if cleanup_failed => {
                    Err(ErrorHandle::source_cleanup_failure(failure.cause, callback).into())
                }
                Some(callback) => Err(callback),
                None => Err(failure.cause.into()),
            }
        }
    }
}

struct RecoveryInputs {
    source: PathBuf,
    destination: PathBuf,
    budget: iprange_livedb::recovery::RecoveryBudget,
    cancellation: iprange_livedb::CancellationToken,
}

unsafe fn decode_recovery_inputs(request: &RecoveryRequest) -> Result<RecoveryInputs, CallError> {
    // SAFETY: the caller supplies the fixed inputs and checked path extents.
    let budget = unsafe { required_input(request.budget, "recovery budget is null")? };
    let budget = unsafe { decode_recovery_budget(budget)? };
    let cancellation = callback::token(request.cancellation)?;
    let source = unsafe { path::decode(request.source)? };
    let destination = unsafe { path::decode(request.destination)? };
    Ok(RecoveryInputs {
        source,
        destination,
        budget,
        cancellation,
    })
}

fn dispatch_recovery(
    mode: RecoveryMode,
    source: PathBuf,
    candidate: iprange_livedb::recovery::RecoveryCandidate,
    destination: PathBuf,
    budget: &iprange_livedb::recovery::RecoveryBudget,
    adapter: &mut RecoveryAdapter,
    cancellation: &iprange_livedb::CancellationToken,
) -> iprange_livedb::recovery::RecoveryOutcome {
    match mode {
        RecoveryMode::Immutable => iprange_livedb::recovery::recover_immutable(
            source,
            candidate,
            destination,
            budget,
            adapter,
            cancellation,
        ),
        RecoveryMode::Live => iprange_livedb::recovery::recover_live(
            source,
            candidate,
            destination,
            budget,
            adapter,
            cancellation,
        ),
        RecoveryMode::Offline => iprange_livedb::recovery::recover_offline(
            source,
            candidate,
            destination,
            OfflineQuiescenceCertification::CallerCertified,
            budget,
            adapter,
            cancellation,
        ),
    }
}

fn finish_recovery(
    output: &mut *mut ReportHandle,
    result: iprange_livedb::recovery::RecoveryOutcome,
    adapter: RecoveryAdapter,
) -> Result<(), CallError> {
    match result {
        Ok(result) => {
            *output = Box::into_raw(Box::new(ReportHandle::recovery(result)));
            Ok(())
        }
        Err(failure) => store_recovery_failure(output, failure, adapter.failure),
    }
}

fn store_recovery_failure(
    output: &mut *mut ReportHandle,
    mut failure: Box<iprange_livedb::recovery::RecoveryPreparationFailure>,
    callback: Option<CallError>,
) -> Result<(), CallError> {
    let mut report = ReportHandle::recovery_preparation(&failure);
    report.set_cleanup_guard(failure.source_cleanup.take());
    *output = Box::into_raw(Box::new(report));
    if let Some(callback) = callback {
        return Err(callback);
    }
    let cleanup = failure.cleanup.iter().map(facts::cleanup).collect();
    let error = ErrorHandle::publication_failure(failure.cause, cleanup, None);
    Err(error.into())
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_recover_immutable(
    source: Path,
    candidate_report: *const ReportHandle,
    candidate_index: u64,
    destination: Path,
    budget: *const RecoveryBudget,
    cancellation: Cancellation,
    sink_callback: RecoveryUnknownSinkFn,
    sink_context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    let request = RecoveryRequest {
        mode: RecoveryMode::Immutable,
        source,
        candidate_report,
        candidate_index,
        destination,
        budget,
        cancellation,
        sink_callback,
        sink_context,
    };
    recover(request, report_output, error_output)
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_recover_live(
    source: Path,
    candidate_report: *const ReportHandle,
    candidate_index: u64,
    destination: Path,
    budget: *const RecoveryBudget,
    cancellation: Cancellation,
    sink_callback: RecoveryUnknownSinkFn,
    sink_context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    let request = RecoveryRequest {
        mode: RecoveryMode::Live,
        source,
        candidate_report,
        candidate_index,
        destination,
        budget,
        cancellation,
        sink_callback,
        sink_context,
    };
    recover(request, report_output, error_output)
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_recover_offline(
    source: Path,
    candidate_report: *const ReportHandle,
    candidate_index: u64,
    destination: Path,
    budget: *const RecoveryBudget,
    cancellation: Cancellation,
    sink_callback: RecoveryUnknownSinkFn,
    sink_context: *mut c_void,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    let request = RecoveryRequest {
        mode: RecoveryMode::Offline,
        source,
        candidate_report,
        candidate_index,
        destination,
        budget,
        cancellation,
        sink_callback,
        sink_context,
    };
    recover(request, report_output, error_output)
}

struct ValidationAdapter {
    callback: ValidationFindingSinkFn,
    context: *mut c_void,
    failure: Option<CallError>,
}

impl ValidationAdapter {
    fn new(callback: ValidationFindingSinkFn, context: *mut c_void) -> Self {
        Self {
            callback,
            context,
            failure: None,
        }
    }
}

impl iprange_livedb::validation::ValidationSink for ValidationAdapter {
    fn finding(
        &mut self,
        finding: &iprange_livedb::validation::ValidationFinding,
    ) -> iprange_livedb::Result<ValidationSinkControl> {
        let record = facts::validation_finding(finding);
        match sink::records(
            self.callback,
            self.context,
            &[record],
            "validation finding sink",
        ) {
            Ok(Control::Continue) => Ok(ValidationSinkControl::Continue),
            Ok(Control::Stop) => Ok(ValidationSinkControl::Stop),
            Err(error) => {
                self.failure = Some(error);
                Err(iprange_livedb::Error::InvalidArgument(
                    "C validation finding sink failed",
                ))
            }
        }
    }
}

struct RecoveryAdapter {
    callback: RecoveryUnknownSinkFn,
    context: *mut c_void,
    failure: Option<CallError>,
}

impl RecoveryAdapter {
    fn new(callback: RecoveryUnknownSinkFn, context: *mut c_void) -> Self {
        Self {
            callback,
            context,
            failure: None,
        }
    }
}

impl iprange_livedb::recovery::RecoverySink for RecoveryAdapter {
    fn unknown(
        &mut self,
        unknown: &iprange_livedb::recovery::RecoveryUnknownEnvelope,
    ) -> iprange_livedb::Result<RecoverySinkControl> {
        let record = facts::recovery_unknown(unknown);
        match sink::records(
            self.callback,
            self.context,
            &[record],
            "recovery unknown sink",
        ) {
            Ok(Control::Continue) => Ok(RecoverySinkControl::Continue),
            Ok(Control::Stop) => Ok(RecoverySinkControl::Stop),
            Err(error) => {
                self.failure = Some(error);
                Err(iprange_livedb::Error::InvalidArgument(
                    "C recovery unknown sink failed",
                ))
            }
        }
    }
}

unsafe fn decode_validation_mode(
    mode: u32,
    report: *const ReportHandle,
    index: u64,
) -> Result<ValidationMode, BoundaryError> {
    match mode {
        1 if report.is_null() && index == 0 => Ok(ValidationMode::LiveCurrent),
        2 if report.is_null() && index == 0 => Ok(ValidationMode::ImmutableCurrent),
        3 => {
            // SAFETY: the report pointer is required and validated for offline mode.
            let report = unsafe {
                crate::handle::required_handle_input(report, "candidate report is null")?
            };
            let _report_guard = report.enter()?;
            Ok(ValidationMode::OfflineCandidate(report.candidate(index)?))
        }
        1 | 2 => Err(BoundaryError::reserved(
            "current validation mode requires null candidate report and zero index",
        )),
        _ => Err(BoundaryError::invalid_enum("unknown validation mode")),
    }
}

unsafe fn decode_validation_budget(
    value: &ValidationBudget,
) -> Result<iprange_livedb::validation::ValidationBudget, BoundaryError> {
    require_structure(
        value.abi_version,
        value.struct_size,
        size_of::<ValidationBudget>(),
        "validation budget",
    )?;
    require_zero(&value.reserved, "validation budget reserved field")?;
    let scratch_directory =
        unsafe { decode_optional_path(value.scratch_directory_present, value.scratch_directory)? };
    Ok(iprange_livedb::validation::ValidationBudget {
        max_heap_bytes: value.max_heap_bytes,
        max_open_files: value.max_open_files,
        max_scratch_bytes: value.max_scratch_bytes,
        max_scratch_files: value.max_scratch_files,
        scratch_directory,
    })
}

unsafe fn decode_recovery_budget(
    value: &RecoveryBudget,
) -> Result<iprange_livedb::recovery::RecoveryBudget, BoundaryError> {
    require_structure(
        value.abi_version,
        value.struct_size,
        size_of::<RecoveryBudget>(),
        "recovery budget",
    )?;
    require_zero(&value.reserved, "recovery budget reserved field")?;
    let scratch_directory =
        unsafe { decode_optional_path(value.scratch_directory_present, value.scratch_directory)? };
    Ok(iprange_livedb::recovery::RecoveryBudget {
        max_heap_bytes: value.max_heap_bytes,
        max_output_pages: value.max_output_pages,
        max_open_files: value.max_open_files,
        max_scratch_bytes: value.max_scratch_bytes,
        max_scratch_files: value.max_scratch_files,
        scratch_directory,
    })
}

unsafe fn decode_optional_path(present: u8, value: Path) -> Result<Option<PathBuf>, BoundaryError> {
    match present {
        0 if value.kind == 0
            && value.reserved == 0
            && value.pointer.is_null()
            && value.length == 0 =>
        {
            Ok(None)
        }
        0 => Err(BoundaryError::reserved(
            "absent scratch path fields must be zero",
        )),
        1 => {
            // SAFETY: the tagged path validates its pointer and extent.
            Ok(Some(unsafe { path::decode(value)? }))
        }
        _ => Err(BoundaryError::invalid_enum(
            "scratch path presence must be zero or one",
        )),
    }
}

fn require_structure(
    version: u32,
    size: u32,
    expected: usize,
    name: &'static str,
) -> Result<(), BoundaryError> {
    if version != 1 {
        return Err(BoundaryError::invalid_argument(name));
    }
    if size != expected as u32 {
        return Err(BoundaryError::invalid_length(name));
    }
    Ok(())
}

fn require_zero(bytes: &[u8], name: &'static str) -> Result<(), BoundaryError> {
    if bytes.iter().any(|&byte| byte != 0) {
        Err(BoundaryError::reserved(name))
    } else {
        Ok(())
    }
}
