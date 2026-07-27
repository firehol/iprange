//! Creation, live-transition, and commit-resolution exports.

use iprange_livedb::{
    AddressFamily, CommitResolutionMode, LiveResetPolicy, LiveTransitionResolutionMode, ValueKind,
    ValueTag,
};

use crate::abi::{ByteSlice, Cancellation, Path};
use crate::callback;
use crate::error::{
    call_with_output, input_slice, required_output, BoundaryError, CallError, ErrorHandle,
};
use crate::path;
use crate::report::ReportHandle;

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_create_live(
    destination: Path,
    address_family: u32,
    value_kind: u32,
    value_tag: ByteSlice,
    reader_capacity: u32,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: the output slot and tagged inputs are validated before mutation.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let address_family = decode_family(address_family)?;
        let value_kind = decode_value_kind(value_kind)?;
        let value_tag = unsafe { decode_value_tag(value_tag)? };
        let cancellation = callback::token(cancellation)?;
        let destination = unsafe { path::decode(destination)? };
        let result = iprange_livedb::create_live(
            destination,
            address_family,
            value_kind,
            value_tag,
            reader_capacity,
            &cancellation,
        )?;
        *output = Box::into_raw(Box::new(ReportHandle::create(result, false)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_initialize_live(
    source: Path,
    reader_capacity: u32,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: the output slot and tagged path are validated before mutation.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let cancellation = callback::token(cancellation)?;
        let source = unsafe { path::decode(source)? };
        let result = iprange_livedb::initialize_live(source, reader_capacity, &cancellation)?;
        *output = Box::into_raw(Box::new(ReportHandle::live_transition(result, false)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reset_live_coordination(
    source: Path,
    reader_capacity: u32,
    policy: u32,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: the output slot and tagged path are validated before mutation.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let policy = decode_reset_policy(policy)?;
        let cancellation = callback::token(cancellation)?;
        let source = unsafe { path::decode(source)? };
        let result = iprange_livedb::reset_live_coordination(
            source,
            reader_capacity,
            policy,
            &cancellation,
        )?;
        *output = Box::into_raw(Box::new(ReportHandle::live_transition(result, false)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_resolve_create_live(
    source: Path,
    supplied: *const ReportHandle,
    action: u32,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before mutation.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let supplied =
            unsafe { crate::handle::required_handle_input(supplied, "creation report is null")? };
        let _supplied_guard = supplied.enter()?;
        let inputs = unsafe { decode_transition_inputs(source, action, cancellation)? };
        let attempt = supplied.create_attempt()?;
        let result = iprange_livedb::resolve_create_live(
            inputs.source,
            attempt,
            inputs.mode,
            &inputs.cancellation,
        )?;
        *output = Box::into_raw(Box::new(ReportHandle::create(result, true)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_resolve_live_transition(
    source: Path,
    supplied: *const ReportHandle,
    action: u32,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before mutation.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let supplied =
            unsafe { crate::handle::required_handle_input(supplied, "transition report is null")? };
        let _supplied_guard = supplied.enter()?;
        let inputs = unsafe { decode_transition_inputs(source, action, cancellation)? };
        let attempt = supplied.transition_attempt()?;
        let result = iprange_livedb::resolve_live_transition(
            inputs.source,
            attempt,
            inputs.mode,
            &inputs.cancellation,
        )?;
        *output = Box::into_raw(Box::new(ReportHandle::live_transition(result, true)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_resolve_interrupted_live_transition(
    source: Path,
    action: u32,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: the output slot and tagged path are validated before mutation.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let mode = decode_transition_action(action)?;
        let cancellation = callback::token(cancellation)?;
        let source = unsafe { path::decode(source)? };
        let result =
            iprange_livedb::resolve_interrupted_live_transition(source, mode, &cancellation)?;
        *output = Box::into_raw(Box::new(ReportHandle::live_residue(result)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_resolve_commit(
    source: Path,
    supplied: *const ReportHandle,
    source_mode: u32,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: pointers and tagged inputs are validated before mutation.
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let supplied =
            unsafe { crate::handle::required_handle_input(supplied, "commit report is null")? };
        let _supplied_guard = supplied.enter()?;
        let inputs = unsafe { decode_commit_inputs(source, source_mode, cancellation)? };
        let attempt = supplied.commit_attempt()?;
        let result = iprange_livedb::resolve_commit(
            inputs.source,
            attempt,
            inputs.mode,
            &inputs.cancellation,
        )?;
        *output = Box::into_raw(Box::new(ReportHandle::commit_resolution(result)));
        Ok::<_, CallError>(())
    })
}

struct TransitionInputs {
    source: std::path::PathBuf,
    mode: LiveTransitionResolutionMode,
    cancellation: iprange_livedb::CancellationToken,
}

unsafe fn decode_transition_inputs(
    source: Path,
    action: u32,
    cancellation: Cancellation,
) -> Result<TransitionInputs, CallError> {
    let mode = decode_transition_action(action)?;
    let cancellation = callback::token(cancellation)?;
    // SAFETY: the caller supplies a tagged path with a checked extent.
    let source = unsafe { path::decode(source)? };
    Ok(TransitionInputs {
        source,
        mode,
        cancellation,
    })
}

struct CommitInputs {
    source: std::path::PathBuf,
    mode: CommitResolutionMode,
    cancellation: iprange_livedb::CancellationToken,
}

unsafe fn decode_commit_inputs(
    source: Path,
    source_mode: u32,
    cancellation: Cancellation,
) -> Result<CommitInputs, CallError> {
    let mode = decode_commit_mode(source_mode)?;
    let cancellation = callback::token(cancellation)?;
    // SAFETY: the caller supplies a tagged path with a checked extent.
    let source = unsafe { path::decode(source)? };
    Ok(CommitInputs {
        source,
        mode,
        cancellation,
    })
}

fn decode_commit_mode(source_mode: u32) -> Result<CommitResolutionMode, BoundaryError> {
    match source_mode {
        1 => Ok(CommitResolutionMode::Immutable),
        2 => Ok(CommitResolutionMode::Live),
        _ => Err(BoundaryError::invalid_enum("unknown commit source mode")),
    }
}

fn decode_family(value: u32) -> Result<AddressFamily, BoundaryError> {
    u8::try_from(value)
        .ok()
        .and_then(AddressFamily::from_wire)
        .ok_or_else(|| BoundaryError::invalid_enum("unknown address family"))
}

fn decode_value_kind(value: u32) -> Result<ValueKind, BoundaryError> {
    u8::try_from(value)
        .ok()
        .and_then(ValueKind::from_wire)
        .ok_or_else(|| BoundaryError::invalid_enum("unknown value kind"))
}

unsafe fn decode_value_tag(value: ByteSlice) -> Result<ValueTag, BoundaryError> {
    // SAFETY: the tagged byte slice is validated before it is read.
    let bytes = unsafe { input_slice(value.pointer, value.length)? };
    ValueTag::new(bytes).ok_or_else(|| {
        BoundaryError::wrong_value_tag("value tag must contain at most 15 non-NUL bytes")
    })
}

fn decode_reset_policy(value: u32) -> Result<LiveResetPolicy, BoundaryError> {
    match value {
        1 => Ok(LiveResetPolicy::RollbackSafe),
        2 => Ok(LiveResetPolicy::DiscardPrevious),
        _ => Err(BoundaryError::invalid_enum("unknown live reset policy")),
    }
}

fn decode_transition_action(value: u32) -> Result<LiveTransitionResolutionMode, BoundaryError> {
    match value {
        1 => Ok(LiveTransitionResolutionMode::Complete),
        2 => Ok(LiveTransitionResolutionMode::Rollback),
        _ => Err(BoundaryError::invalid_enum(
            "unknown transition resolution action",
        )),
    }
}
