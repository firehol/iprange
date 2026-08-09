use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::publication::CoordinationCleanup;
use crate::recovery::{
    RecoveryCandidateInspection, RecoveryInspectionMode, RecoverySourceCleanupGuard,
};
use crate::validation::{
    ValidationBudget, ValidationFailure, ValidationMode, ValidationProgress, ValidationResult,
    ValidationSink, ValidationSinkControl,
};

use super::{
    acknowledge_callback, advance_sequence, drive, drive_loop, record_unreadable_page, spawn,
    start, CallbackDecision, Drive, Process, WorkerCleanup,
};
use crate::worker::control::{
    CallbackCheckpoint, Control, FaultRecord, MappingRole, Opcode, State,
};
use crate::worker::{wire, wire_validation};

pub(in crate::worker) fn inspect_recovery_candidates(
    path: &Path,
    mode: RecoveryInspectionMode,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
) -> Result<RecoveryCandidateInspection> {
    let mut unreadable_pages = Vec::new();
    loop {
        cancellation.check()?;
        match inspect_once(path, mode, budget, cancellation, &unreadable_pages)? {
            InspectionAttempt::Complete(result) => return result,
            InspectionAttempt::Fault(fault) => {
                if fault.role != MappingRole::Source {
                    return Err(mapped_worker_fault());
                }
                let page = u32::try_from(fault.relative / crate::contract::PAGE_SIZE as u64)
                    .map_err(|_| Error::ArithmeticOverflow("worker fault page"))?;
                if page >= 2 {
                    return Err(Error::Conflict(
                        "candidate inspection fault did not advance",
                    ));
                }
                record_unreadable_page(
                    &mut unreadable_pages,
                    page,
                    budget.max_heap_bytes,
                    "candidate inspection fault did not advance",
                )?;
            }
        }
    }
}

pub(in crate::worker) fn validate<S: ValidationSink>(
    path: &Path,
    mode: &ValidationMode,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<ValidationResult, ValidationFailure> {
    match validate_all(path, mode, budget, cancellation, sink) {
        Ok(result) => result,
        Err(cause) => Err(crate::validation::failure(cause, ValidationProgress::new())),
    }
}

fn validate_all<S: ValidationSink>(
    path: &Path,
    mode: &ValidationMode,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> Result<std::result::Result<ValidationResult, ValidationFailure>> {
    let mut unreadable_pages = Vec::new();
    let mut delivered_findings = 0;
    loop {
        cancellation.check()?;
        match validate_once(
            path,
            mode,
            budget,
            cancellation,
            sink,
            &unreadable_pages,
            &mut delivered_findings,
        )? {
            ValidationAttempt::Complete(mut result, callback) => {
                if let Some(callback) = callback {
                    match &mut result {
                        Err(failure) => failure.cause = callback.into_error(),
                        Ok(_) => {
                            return Err(Error::Conflict(
                                "worker ignored a terminal validation callback",
                            ))
                        }
                    }
                }
                return Ok(result);
            }
            ValidationAttempt::Fault(fault) => {
                if fault.role != MappingRole::Source {
                    return Err(mapped_worker_fault());
                }
                let page = u32::try_from(fault.relative / crate::contract::PAGE_SIZE as u64)
                    .map_err(|_| Error::ArithmeticOverflow("worker fault page"))?;
                record_unreadable_page(
                    &mut unreadable_pages,
                    page,
                    budget.max_heap_bytes,
                    "validation fault did not advance",
                )?;
            }
        }
    }
}

const fn mapped_worker_fault() -> Error {
    Error::WorkerOperation {
        code: crate::ErrorCode::Io,
        os_code: None,
    }
}

// Validation is explicit and keeps its complete report inline.
#[allow(clippy::large_enum_variant)]
enum ValidationAttempt {
    Complete(
        std::result::Result<ValidationResult, ValidationFailure>,
        Option<CallbackDecision>,
    ),
    Fault(FaultRecord),
}

#[allow(clippy::too_many_arguments)]
fn validate_once<S: ValidationSink>(
    path: &Path,
    mode: &ValidationMode,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
    unreadable_pages: &[u32],
    delivered_findings: &mut u64,
) -> Result<ValidationAttempt> {
    let mut control = Control::create_parent()?;
    control.set_opcode(Opcode::Validate);
    control.set_external_poll(cancellation.requires_external_poll());
    wire_validation::write_request(
        &control,
        path,
        mode,
        budget,
        unreadable_pages,
        *delivered_findings,
    )?;
    let mut child = spawn(&control)?;
    start(&mut child, &mut control)?;
    let mut callback = None;
    let driven = drive_validation(
        &mut child,
        &control,
        cancellation,
        sink,
        delivered_findings,
        &mut callback,
    );
    let driven = match driven {
        Ok(driven) => driven,
        Err(cause) => {
            child.abort();
            return match callback {
                Some(callback) => validation_callback_failure(&control, callback),
                None => Err(cause),
            };
        }
    };
    match driven {
        Drive::Complete { guard_pending } => {
            let (mut result, retained_problem) = wire_validation::read_result(&control);
            if guard_pending {
                let problem = retained_problem.ok_or(Error::Conflict(
                    "SDK worker omitted its retained cleanup problem",
                ))?;
                match &mut result {
                    Err(failure) if failure.source_cleanup.is_none() => {
                        let cleanup = WorkerCleanup::new(child, control, problem.clone());
                        failure.source_cleanup =
                            Some(RecoverySourceCleanupGuard::from_worker(cleanup, problem));
                        failure.coordination_cleanup = CoordinationCleanup::CleanupGuard;
                    }
                    _ => {
                        return Err(Error::Conflict(
                            "SDK worker retained cleanup after a successful operation",
                        ));
                    }
                }
            } else if retained_problem.is_some() {
                return Err(Error::Conflict(
                    "SDK worker reported cleanup without retaining authority",
                ));
            }
            Ok(ValidationAttempt::Complete(result, callback))
        }
        Drive::Fault(fault) => match callback {
            Some(callback) => validation_callback_failure(&control, callback),
            None if fault.role == MappingRole::Source => Ok(ValidationAttempt::Fault(fault)),
            None => {
                let progress =
                    read_validation_progress(&control)?.unwrap_or_else(ValidationProgress::new);
                Ok(ValidationAttempt::Complete(
                    Err(crate::validation::failure(
                        Error::WorkerOperation {
                            code: crate::ErrorCode::Io,
                            os_code: None,
                        },
                        progress,
                    )),
                    None,
                ))
            }
        },
    }
}

fn validation_callback_failure(
    control: &Control,
    callback: CallbackDecision,
) -> Result<ValidationAttempt> {
    let progress = read_validation_progress(control)?.ok_or(Error::Conflict(
        "worker validation callback checkpoint is missing",
    ))?;
    Ok(ValidationAttempt::Complete(
        Err(ValidationFailure {
            cause: callback.into_error(),
            progress: Box::new(progress),
            cleanup: Box::new(crate::publication::CleanupArtifacts::new()),
            coordination_cleanup: CoordinationCleanup::None,
            source_cleanup: None,
        }),
        None,
    ))
}

pub(super) fn read_validation_progress(control: &Control) -> Result<Option<ValidationProgress>> {
    if control.callback_checkpoint() != Some(CallbackCheckpoint::ValidationProgress) {
        return Ok(None);
    }
    let mut input = wire::Reader::new_callback_checkpoint(control)?;
    let progress = wire::read_progress(&mut input)?;
    input.finish()?;
    Ok(Some(progress))
}

// Inspection is explicit and keeps its complete report inline.
#[allow(clippy::large_enum_variant)]
enum InspectionAttempt {
    Complete(Result<RecoveryCandidateInspection>),
    Fault(FaultRecord),
}

fn inspect_once(
    path: &Path,
    mode: RecoveryInspectionMode,
    budget: &ValidationBudget,
    cancellation: &CancellationToken,
    unreadable_pages: &[u32],
) -> Result<InspectionAttempt> {
    let mut control = Control::create_parent()?;
    control.set_opcode(Opcode::InspectRecoveryCandidates);
    control.set_external_poll(cancellation.requires_external_poll());
    wire::write_inspection_request(&control, path, mode, budget, unreadable_pages)?;
    let mut child = spawn(&control)?;
    start(&mut child, &mut control)?;
    match drive(&mut child, &control, cancellation)? {
        Drive::Complete {
            guard_pending: false,
        } => Ok(InspectionAttempt::Complete(wire::read_inspection_result(
            &control,
        ))),
        Drive::Complete {
            guard_pending: true,
        } => Err(Error::Conflict(
            "candidate inspection retained unexpected cleanup authority",
        )),
        Drive::Fault(fault) => Ok(InspectionAttempt::Fault(fault)),
    }
}

fn drive_validation<S: ValidationSink>(
    child: &mut Process,
    control: &Control,
    cancellation: &CancellationToken,
    sink: &mut S,
    delivered_findings: &mut u64,
    callback: &mut Option<CallbackDecision>,
) -> Result<Drive> {
    drive_loop(
        child,
        control,
        cancellation,
        "SDK worker emitted an unexpected recovery event",
        |state, child, control| {
            if state != State::Finding {
                return Ok(false);
            }
            let finding = wire_validation::read_finding(control)?;
            advance_sequence(
                child,
                delivered_findings,
                finding.sequence,
                "worker validation finding sequence is invalid",
            )?;
            let result = sink
                .finding(&finding)
                .map(|value| value == ValidationSinkControl::Stop);
            acknowledge_callback(control, result, callback)?;
            Ok(true)
        },
    )
}
