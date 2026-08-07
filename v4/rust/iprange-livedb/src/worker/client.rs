use std::path::{Path, PathBuf};
use std::process::{Child, Command, ExitStatus, Stdio};
use std::time::{Duration, Instant};

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::publication::cleanup;
use crate::publication::output::CreatedOutput;
use crate::publication::problem::Problem;
use crate::publication::{CleanupState, CoordinationCleanup, Housekeeping, PublicationProblem};
use crate::recovery::{
    RecoveryBudget, RecoveryCandidate, RecoveryCandidateInspection, RecoveryInspectionMode,
    RecoveryOutcome, RecoveryPreparationFailure, RecoveryReport, RecoverySink, RecoverySinkControl,
    RecoverySourceCleanupGuard, ScratchCleanup, ScratchProblem, ScratchResidue, WorkerMode,
};
use crate::validation::ValidationBudget;
use crate::validation::{
    ValidationFailure, ValidationMode, ValidationProgress, ValidationResult, ValidationSink,
    ValidationSinkControl,
};

use super::control::{
    CallbackCheckpoint, Control, FaultRecord, MappingRole, Opcode, ScratchCheckpoint, State,
    OWNED_FAULT_EXIT,
};
use super::wire;
use super::wire_recovery;
use super::wire_validation;

const START_LIMIT: Duration = Duration::from_secs(30);

pub(super) fn recover<S: RecoverySink>(
    source_path: &Path,
    candidate: RecoveryCandidate,
    destination_path: &Path,
    mode: WorkerMode,
    budget: &RecoveryBudget,
    sink: &mut S,
    cancellation: &CancellationToken,
) -> RecoveryOutcome {
    crate::recovery::validate_worker_budget(budget, mode)?;
    let mut unreadable_pages = Vec::new();
    let mut delivered_unknowns = 0;
    loop {
        if let Err(cause) = cancellation.check() {
            return Err(Box::new(RecoveryPreparationFailure::early(cause)));
        }
        match recover_once(
            source_path,
            candidate,
            destination_path,
            mode,
            budget,
            sink,
            cancellation,
            &unreadable_pages,
            &mut delivered_unknowns,
        ) {
            RecoveryAttempt::Complete(outcome) => return outcome,
            RecoveryAttempt::Early(cause) => {
                return Err(Box::new(RecoveryPreparationFailure::early(cause)))
            }
            RecoveryAttempt::Interrupted {
                fault,
                output,
                scratch,
                checkpoint,
            } => {
                if let Some(mut outcome) = checkpoint {
                    if fault.role != MappingRole::Output {
                        return Err(Box::new(RecoveryPreparationFailure::early(
                            Error::Conflict(
                                "recovery publication checkpoint accompanied a non-output fault",
                            ),
                        )));
                    }
                    apply_fault_to_checkpoint(&mut outcome, fault);
                    return outcome;
                }
                let (discarded, scratch) = super::cleanup::discard(
                    destination_path,
                    output,
                    budget.scratch_directory.as_deref(),
                    scratch,
                );
                if fault.role != MappingRole::Source {
                    return Err(Box::new(RecoveryPreparationFailure::discarded(
                        fault_problem(fault.role),
                        RecoveryReport::default(),
                        discarded,
                        scratch,
                        None,
                    )));
                }
                if !discard_clean(&discarded) || !scratch_clean(&scratch) {
                    return Err(Box::new(RecoveryPreparationFailure::discarded(
                        fault_problem(fault.role),
                        RecoveryReport::default(),
                        discarded,
                        scratch,
                        None,
                    )));
                }
                let page = match u32::try_from(fault.relative / crate::contract::PAGE_SIZE as u64) {
                    Ok(page) => page,
                    Err(_) => {
                        return Err(Box::new(RecoveryPreparationFailure::early(
                            Error::ArithmeticOverflow("worker fault page"),
                        )))
                    }
                };
                if let Err(cause) = record_unreadable_page(
                    &mut unreadable_pages,
                    page,
                    budget.max_heap_bytes,
                    "recovery source fault did not advance",
                ) {
                    return Err(Box::new(RecoveryPreparationFailure::early(cause)));
                }
            }
            RecoveryAttempt::Failed {
                cause,
                output,
                scratch,
            } => {
                let (discarded, scratch) = super::cleanup::discard(
                    destination_path,
                    output,
                    budget.scratch_directory.as_deref(),
                    scratch,
                );
                return Err(Box::new(RecoveryPreparationFailure::discarded(
                    crate::recovery::source_guard::problem(&cause),
                    RecoveryReport::default(),
                    discarded,
                    scratch,
                    None,
                )));
            }
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn recover_once<S: RecoverySink>(
    source_path: &Path,
    candidate: RecoveryCandidate,
    destination_path: &Path,
    mode: WorkerMode,
    budget: &RecoveryBudget,
    sink: &mut S,
    cancellation: &CancellationToken,
    unreadable_pages: &[u32],
    delivered_unknowns: &mut u64,
) -> RecoveryAttempt {
    let mut control = match Control::create_parent() {
        Ok(control) => control,
        Err(cause) => return RecoveryAttempt::Early(cause),
    };
    control.set_opcode(Opcode::Recover);
    control.set_external_poll(cancellation.requires_external_poll());
    let mut child = match spawn(&control) {
        Ok(child) => child,
        Err(cause) => return RecoveryAttempt::Early(cause),
    };
    if let Err(cause) = handshake(&mut child, &mut control) {
        return RecoveryAttempt::Early(cause);
    }
    let created = match CreatedOutput::create_absent(destination_path) {
        Ok(created) => created,
        Err(cause) => {
            return RecoveryAttempt::Complete(Err(Box::new(RecoveryPreparationFailure::new(
                Problem::output(&cause),
                RecoveryReport::default(),
                None,
                None,
                None,
                None,
            ))))
        }
    };
    let secured = match created.secure() {
        Ok(secured) => secured,
        Err(failure) => {
            let discarded = cleanup::discard_created(&failure.owner);
            return RecoveryAttempt::Complete(Err(Box::new(
                RecoveryPreparationFailure::discarded(
                    Problem::output(&failure.cause),
                    RecoveryReport::default(),
                    discarded,
                    None,
                    None,
                ),
            )));
        }
    };
    let (attempt, file) = secured.into_parts();
    let facts = attempt.facts();
    if let Err(cause) = wire_recovery::write_request(
        &control,
        source_path,
        destination_path,
        &candidate,
        mode,
        budget,
        &facts,
        unreadable_pages,
        *delivered_unknowns,
    ) {
        let discarded = cleanup::discard_attempt(&attempt, &file);
        return RecoveryAttempt::Complete(Err(Box::new(RecoveryPreparationFailure::discarded(
            crate::recovery::source_guard::problem(&cause),
            RecoveryReport::default(),
            discarded,
            None,
            None,
        ))));
    }
    drop(file);
    drop(attempt);
    control.set_state(State::Running);
    let mut callback = None;
    match drive_recovery(
        &mut child,
        &control,
        cancellation,
        sink,
        delivered_unknowns,
        &mut callback,
    ) {
        Ok(Drive::Fault(fault)) => {
            if let Some(callback) = callback {
                return recovery_callback_failure(
                    &control,
                    callback,
                    destination_path,
                    facts,
                    budget,
                );
            }
            let checkpoint = match read_recovery_checkpoint(&control) {
                Ok(checkpoint) => checkpoint,
                Err(cause) => {
                    return RecoveryAttempt::Failed {
                        cause,
                        output: facts,
                        scratch: None,
                    }
                }
            };
            match control.scratch_checkpoint() {
                Ok(scratch) => RecoveryAttempt::Interrupted {
                    fault,
                    output: facts,
                    scratch,
                    checkpoint,
                },
                Err(cause) => RecoveryAttempt::Failed {
                    cause,
                    output: facts,
                    scratch: None,
                },
            }
        }
        Err(cause) => {
            child.abort();
            if let Some(callback) = callback {
                return recovery_callback_failure(
                    &control,
                    callback,
                    destination_path,
                    facts,
                    budget,
                );
            }
            match control.scratch_checkpoint() {
                Ok(scratch) => RecoveryAttempt::Failed {
                    cause,
                    output: facts,
                    scratch,
                },
                Err(checkpoint) => RecoveryAttempt::Failed {
                    cause: checkpoint,
                    output: facts,
                    scratch: None,
                },
            }
        }
        Ok(Drive::Complete { guard_pending }) => {
            let (mut outcome, retained_problem) = match wire_recovery::read_outcome(&control) {
                Ok(value) => value,
                Err(cause) => {
                    return RecoveryAttempt::Failed {
                        cause,
                        output: facts,
                        scratch: control.scratch_checkpoint().ok().flatten(),
                    }
                }
            };
            if guard_pending {
                let Some(problem) = retained_problem else {
                    return RecoveryAttempt::Failed {
                        cause: Error::Conflict(
                            "SDK recovery worker omitted its retained cleanup problem",
                        ),
                        output: facts,
                        scratch: control.scratch_checkpoint().ok().flatten(),
                    };
                };
                let Err(failure) = &mut outcome else {
                    return RecoveryAttempt::Failed {
                        cause: Error::Conflict(
                            "SDK recovery worker retained cleanup after success",
                        ),
                        output: facts,
                        scratch: control.scratch_checkpoint().ok().flatten(),
                    };
                };
                let cleanup = WorkerCleanup::new(child, control, problem.clone());
                failure.source_cleanup =
                    Some(RecoverySourceCleanupGuard::from_worker(cleanup, problem));
                failure.coordination_cleanup = CoordinationCleanup::CleanupGuard;
            } else if retained_problem.is_some() {
                return RecoveryAttempt::Failed {
                    cause: Error::Conflict(
                        "SDK recovery worker reported cleanup without retaining authority",
                    ),
                    output: facts,
                    scratch: control.scratch_checkpoint().ok().flatten(),
                };
            }
            RecoveryAttempt::Complete(outcome)
        }
    }
}

fn recovery_callback_failure(
    control: &Control,
    callback: CallbackDecision,
    destination_path: &Path,
    output: crate::publication::PrivateOutputAttempt,
    budget: &RecoveryBudget,
) -> RecoveryAttempt {
    let report = match read_recovery_callback_report(control) {
        Ok(report) => report,
        Err(cause) => {
            return RecoveryAttempt::Failed {
                cause,
                output,
                scratch: control.scratch_checkpoint().ok().flatten(),
            }
        }
    };
    let scratch = match control.scratch_checkpoint() {
        Ok(scratch) => scratch,
        Err(cause) => {
            return RecoveryAttempt::Failed {
                cause,
                output,
                scratch: None,
            }
        }
    };
    let (discarded, scratch) = super::cleanup::discard(
        destination_path,
        output,
        budget.scratch_directory.as_deref(),
        scratch,
    );
    let cause = crate::recovery::source_guard::problem(&callback.into_error());
    RecoveryAttempt::Complete(Err(Box::new(RecoveryPreparationFailure::discarded(
        cause, report, discarded, scratch, None,
    ))))
}

// These short-lived values avoid unbudgeted heap allocation during recovery.
#[allow(clippy::large_enum_variant)]
enum RecoveryAttempt {
    Complete(RecoveryOutcome),
    Early(Error),
    Interrupted {
        fault: FaultRecord,
        output: crate::publication::PrivateOutputAttempt,
        scratch: Option<ScratchCheckpoint>,
        checkpoint: Option<RecoveryOutcome>,
    },
    Failed {
        cause: Error,
        output: crate::publication::PrivateOutputAttempt,
        scratch: Option<ScratchCheckpoint>,
    },
}

fn read_recovery_checkpoint(control: &Control) -> Result<Option<RecoveryOutcome>> {
    if !control.recovery_checkpoint_is_sealed() {
        return Ok(None);
    }
    let (outcome, retained_problem) = wire_recovery::read_outcome(control)?;
    if retained_problem.is_some() {
        return Err(Error::Conflict(
            "recovery publication checkpoint retained unexpected cleanup authority",
        ));
    }
    Ok(Some(outcome))
}

fn read_recovery_callback_report(control: &Control) -> Result<RecoveryReport> {
    if control.callback_checkpoint() != Some(CallbackCheckpoint::RecoveryReport) {
        return Err(Error::Conflict(
            "worker recovery callback checkpoint is missing",
        ));
    }
    let mut input = wire::Reader::new_callback_checkpoint(control)?;
    let report = wire_recovery::read_report(&mut input)?;
    input.finish()?;
    Ok(report)
}

fn apply_fault_to_checkpoint(outcome: &mut RecoveryOutcome, fault: FaultRecord) {
    let problem = fault_problem(fault.role);
    match outcome {
        Ok(result) => result.publication.cause = Some(problem),
        Err(failure) => failure.cause = problem,
    }
}

fn discard_clean(value: &cleanup::EarlyDiscard) -> bool {
    value.artifact.is_none()
        && matches!(value.housekeeping, crate::publication::Housekeeping::None)
        && value.visible_housekeeping.is_empty()
}

fn scratch_clean(value: &Option<ScratchCleanup>) -> bool {
    value.as_ref().map_or(true, |cleanup| {
        cleanup.clean()
            && matches!(cleanup.housekeeping, Housekeeping::None)
            && cleanup.visible_housekeeping.is_empty()
    })
}

pub(super) fn cleanup_checkpoint(
    directory: Option<&Path>,
    checkpoint: Option<ScratchCheckpoint>,
) -> Option<ScratchCleanup> {
    let checkpoint = checkpoint?;
    let mut cleanup = ScratchCleanup {
        attempt_id: checkpoint.attempt_id,
        directory_identity: checkpoint.directory_identity,
        creation_security_kind: checkpoint.creation_security.kind,
        creation_security_commitment: checkpoint.creation_security.commitment,
        residues: Vec::new(),
        housekeeping: Housekeeping::None,
        visible_housekeeping: Vec::new(),
    };
    for entry in checkpoint.entries {
        let removal = directory
            .ok_or(Error::Conflict(
                "worker recorded scratch without a scratch directory",
            ))
            .and_then(|directory| {
                crate::recovery::remove_checkpointed_scratch(
                    directory,
                    checkpoint.directory_identity,
                    checkpoint.attempt_id,
                    entry.ordinal,
                    entry.identity,
                    checkpoint.creation_security.clone(),
                )
            });
        match removal {
            Ok(removal) => {
                cleanup.housekeeping =
                    merge_housekeeping(cleanup.housekeeping, removal.housekeeping);
                cleanup
                    .visible_housekeeping
                    .extend(removal.visible_housekeeping.into_vec());
                let problem = removal.cause.or_else(|| {
                    matches!(removal.cleanup_state, CleanupState::ResiduePossible).then(|| {
                        PublicationProblem::new(
                            crate::ErrorCode::CleanupConflict,
                            None,
                            "checkpointed recovery scratch cleanup was not proved",
                        )
                    })
                });
                if let Some(problem) = problem {
                    push_scratch_residue(&mut cleanup, entry.ordinal, entry.identity, problem);
                }
            }
            Err(cause) => {
                let os_code = match &cause {
                    Error::Io(source) => source.raw_os_error(),
                    _ => None,
                };
                push_scratch_residue(
                    &mut cleanup,
                    entry.ordinal,
                    entry.identity,
                    PublicationProblem::new(
                        cause.code(),
                        os_code,
                        "checkpointed recovery scratch cleanup failed",
                    ),
                );
            }
        }
    }
    Some(cleanup)
}

fn push_scratch_residue(
    cleanup: &mut ScratchCleanup,
    ordinal: u32,
    identity: crate::validation::LocalFileIdentity,
    problem: PublicationProblem,
) {
    let basename = crate::recovery::checkpoint_basename(cleanup.attempt_id, ordinal)
        .expect("validated scratch checkpoint has a fixed valid basename");
    cleanup.residues.push(ScratchResidue {
        ordinal,
        directory_identity: cleanup.directory_identity,
        basename,
        identity,
        creation_security_kind: cleanup.creation_security_kind,
        creation_security_commitment: cleanup.creation_security_commitment,
        problem: ScratchProblem {
            code: problem.code,
            os_code: problem.os_code,
            detail: problem.detail,
        },
    });
}

const fn merge_housekeeping(left: Housekeeping, right: Housekeeping) -> Housekeeping {
    if matches!(left, Housekeeping::Visible) || matches!(right, Housekeeping::Visible) {
        Housekeeping::Visible
    } else if matches!(left, Housekeeping::CrashReappearancePossible)
        || matches!(right, Housekeeping::CrashReappearancePossible)
    {
        Housekeeping::CrashReappearancePossible
    } else {
        Housekeeping::None
    }
}

pub(super) fn fault_problem(role: MappingRole) -> PublicationProblem {
    let detail = match role {
        MappingRole::Source => "recovery source mapping faulted",
        MappingRole::Scratch => "recovery scratch mapping faulted",
        MappingRole::Output => "recovery output mapping faulted",
        MappingRole::Coordination => "recovery coordination mapping faulted",
    };
    PublicationProblem::new(crate::ErrorCode::Io, None, detail)
}

pub(super) fn inspect_recovery_candidates(
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
                if page >= 2 || unreadable_pages.contains(&page) {
                    return Err(Error::Conflict(
                        "candidate inspection fault did not advance",
                    ));
                }
                unreadable_pages.push(page);
            }
        }
    }
}

pub(super) fn validate<S: ValidationSink>(
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

fn record_unreadable_page(
    pages: &mut Vec<u32>,
    page: u32,
    max_heap_bytes: u64,
    repeated: &'static str,
) -> Result<()> {
    let insertion = match pages.binary_search(&page) {
        Ok(_) => return Err(Error::Conflict(repeated)),
        Err(insertion) => insertion,
    };
    let count = pages
        .len()
        .checked_add(1)
        .ok_or(Error::ArithmeticOverflow("unreadable source-page list"))?;
    let bytes = count
        .checked_mul(std::mem::size_of::<u32>())
        .ok_or(Error::ArithmeticOverflow("unreadable source-page list"))? as u64;
    if bytes > max_heap_bytes {
        return Err(Error::BudgetExceeded("unreadable source-page list"));
    }
    pages
        .try_reserve_exact(1)
        .map_err(|_| Error::BudgetExceeded("unreadable source-page list"))?;
    pages.insert(insertion, page);
    Ok(())
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

fn read_validation_progress(control: &Control) -> Result<Option<ValidationProgress>> {
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

pub(super) fn spawn(control: &Control) -> Result<Process> {
    let mut last_error = None;
    for executable in worker_candidates()? {
        let child = Command::new(&executable)
            .arg("--control")
            .arg(control.path())
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn();
        match child {
            Ok(child) => return Ok(Process::new(child)),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                last_error = Some(error);
            }
            Err(error) => return Err(error.into()),
        }
    }
    Err(last_error.map_or_else(
        || Error::Unsupported("SDK validation/recovery worker is unavailable"),
        Error::Io,
    ))
}

pub(super) fn start(child: &mut Process, control: &mut Control) -> Result<()> {
    handshake(child, control)?;
    control.set_state(State::Running);
    Ok(())
}

fn handshake(child: &mut Process, control: &mut Control) -> Result<()> {
    let deadline = Instant::now() + START_LIMIT;
    loop {
        if control.state() == Some(State::WorkerReady) {
            if control.worker_pid() != child.id() {
                child.abort();
                return Err(Error::Conflict("SDK worker identity does not match"));
            }
            #[cfg(unix)]
            control.remove_path()?;
            return Ok(());
        }
        if let Some(status) = child.try_wait()? {
            return Err(Error::Conflict(if status.success() {
                "SDK worker exited before its version handshake"
            } else {
                "SDK worker version or protocol does not match"
            }));
        }
        if Instant::now() >= deadline {
            child.abort();
            return Err(Error::Conflict("SDK worker version handshake timed out"));
        }
        std::thread::sleep(Duration::from_millis(1));
    }
}

fn drive_recovery<S: RecoverySink>(
    child: &mut Process,
    control: &Control,
    cancellation: &CancellationToken,
    sink: &mut S,
    delivered_unknowns: &mut u64,
    callback: &mut Option<CallbackDecision>,
) -> Result<Drive> {
    loop {
        match control.state() {
            Some(State::CancelPoll) => acknowledge_poll(control, cancellation),
            Some(State::Unknown) => {
                let unknown = wire_recovery::read_unknown(control)?;
                if unknown.sequence != delivered_unknowns.saturating_add(1) {
                    child.abort();
                    return Err(Error::Conflict(
                        "worker recovery envelope sequence is invalid",
                    ));
                }
                *delivered_unknowns = unknown.sequence;
                match sink.unknown(&unknown) {
                    Ok(RecoverySinkControl::Continue) => control.set_response(0),
                    Ok(RecoverySinkControl::Stop) => {
                        *callback = Some(CallbackDecision::Stop);
                        control.set_response(1);
                    }
                    Err(cause) => {
                        let written = wire_recovery::write_callback_error(control, &cause);
                        *callback = Some(CallbackDecision::Error(cause));
                        written?;
                        control.set_response(2);
                    }
                }
                control.set_state(State::Running);
            }
            Some(State::Complete) => {
                let guard_pending = control.guard_pending();
                if guard_pending {
                    return Ok(Drive::Complete { guard_pending });
                }
                let status = child.wait()?;
                return status
                    .success()
                    .then_some(Drive::Complete { guard_pending })
                    .ok_or(Error::Conflict("SDK worker completion status is invalid"));
            }
            Some(State::Fault) => {
                let status = child.wait()?;
                if status.code() == Some(OWNED_FAULT_EXIT) {
                    return Ok(Drive::Fault(control.fault_record()?));
                }
                return Err(Error::Conflict("SDK worker fault record is untrusted"));
            }
            Some(State::Failed) => {
                return Err(worker_failure(child, control)?);
            }
            Some(State::Finding) | Some(State::CleanupRequest) | Some(State::CleanupResult) => {
                child.abort();
                return Err(Error::Conflict("SDK worker emitted an unexpected event"));
            }
            Some(State::Running) | Some(State::WorkerReady) | Some(State::Request) | None => {
                if !control.external_poll() && cancellation.is_cancelled() {
                    control.request_cancel();
                }
                if child.try_wait()?.is_some() {
                    return Err(Error::Conflict(
                        "SDK worker exited without a terminal record",
                    ));
                }
                std::thread::sleep(Duration::from_millis(1));
            }
        }
    }
}

pub(super) enum Drive {
    Complete { guard_pending: bool },
    Fault(FaultRecord),
}

pub(super) fn drive(
    child: &mut Process,
    control: &Control,
    cancellation: &CancellationToken,
) -> Result<Drive> {
    loop {
        match control.state() {
            Some(State::CancelPoll) => {
                let cancelled = cancellation.is_cancelled();
                control.set_response(u32::from(cancelled));
                if cancelled {
                    control.request_cancel();
                }
                control.set_state(State::Running);
            }
            Some(State::Complete) => {
                let guard_pending = control.guard_pending();
                if guard_pending {
                    return Ok(Drive::Complete { guard_pending });
                }
                let status = child.wait()?;
                return status
                    .success()
                    .then_some(Drive::Complete { guard_pending })
                    .ok_or(Error::Conflict("SDK worker completion status is invalid"));
            }
            Some(State::Fault) => {
                let status = child.wait()?;
                if status.code() == Some(OWNED_FAULT_EXIT) {
                    return Ok(Drive::Fault(control.fault_record()?));
                }
                return Err(Error::Conflict("SDK worker fault record is untrusted"));
            }
            Some(State::Failed) => {
                return Err(worker_failure(child, control)?);
            }
            Some(State::Finding)
            | Some(State::Unknown)
            | Some(State::CleanupRequest)
            | Some(State::CleanupResult) => {
                child.abort();
                return Err(Error::Conflict("SDK worker emitted an unexpected event"));
            }
            Some(State::Running) | Some(State::WorkerReady) | Some(State::Request) | None => {
                if !control.external_poll() && cancellation.is_cancelled() {
                    control.request_cancel();
                }
                if child.try_wait()?.is_some() {
                    return Err(Error::Conflict(
                        "SDK worker exited without a terminal record",
                    ));
                }
                std::thread::sleep(Duration::from_millis(1));
            }
        }
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
    loop {
        match control.state() {
            Some(State::CancelPoll) => acknowledge_poll(control, cancellation),
            Some(State::Finding) => {
                let finding = wire_validation::read_finding(control)?;
                if finding.sequence != delivered_findings.saturating_add(1) {
                    child.abort();
                    return Err(Error::Conflict(
                        "worker validation finding sequence is invalid",
                    ));
                }
                *delivered_findings = finding.sequence;
                match sink.finding(&finding) {
                    Ok(ValidationSinkControl::Continue) => control.set_response(0),
                    Ok(ValidationSinkControl::Stop) => {
                        *callback = Some(CallbackDecision::Stop);
                        control.set_response(1);
                    }
                    Err(cause) => {
                        let written = wire_validation::write_callback_error(control, &cause);
                        *callback = Some(CallbackDecision::Error(cause));
                        written?;
                        control.set_response(2);
                    }
                }
                control.set_state(State::Running);
            }
            Some(State::Complete) => {
                let guard_pending = control.guard_pending();
                if guard_pending {
                    return Ok(Drive::Complete { guard_pending });
                }
                let status = child.wait()?;
                return status
                    .success()
                    .then_some(Drive::Complete { guard_pending })
                    .ok_or(Error::Conflict("SDK worker completion status is invalid"));
            }
            Some(State::Fault) => {
                let status = child.wait()?;
                if status.code() == Some(OWNED_FAULT_EXIT) {
                    return Ok(Drive::Fault(control.fault_record()?));
                }
                return Err(Error::Conflict("SDK worker fault record is untrusted"));
            }
            Some(State::Failed) => {
                return Err(worker_failure(child, control)?);
            }
            Some(State::Unknown) | Some(State::CleanupRequest) | Some(State::CleanupResult) => {
                child.abort();
                return Err(Error::Conflict(
                    "SDK worker emitted an unexpected recovery event",
                ));
            }
            Some(State::Running) | Some(State::WorkerReady) | Some(State::Request) | None => {
                if !control.external_poll() && cancellation.is_cancelled() {
                    control.request_cancel();
                }
                if child.try_wait()?.is_some() {
                    return Err(Error::Conflict(
                        "SDK worker exited without a terminal record",
                    ));
                }
                std::thread::sleep(Duration::from_millis(1));
            }
        }
    }
}

enum CallbackDecision {
    Stop,
    Error(Error),
}

impl CallbackDecision {
    fn into_error(self) -> Error {
        match self {
            Self::Stop => Error::StoppedBySink,
            Self::Error(cause) => Error::SinkFailed(Box::new(cause)),
        }
    }
}

fn acknowledge_poll(control: &Control, cancellation: &CancellationToken) {
    let cancelled = cancellation.is_cancelled();
    control.set_response(u32::from(cancelled));
    if cancelled {
        control.request_cancel();
    }
    control.set_state(State::Running);
}

fn worker_failure(child: &mut Process, control: &Control) -> Result<Error> {
    let status = child.wait()?;
    if !status.success() {
        return Err(Error::Conflict(
            "SDK worker failure record has an invalid completion status",
        ));
    }
    wire::read_worker_error(control)
}

pub(super) struct Process {
    child: Option<Child>,
}

impl Process {
    fn new(child: Child) -> Self {
        Self { child: Some(child) }
    }

    fn id(&self) -> u32 {
        self.child.as_ref().map_or(0, Child::id)
    }

    fn wait(&mut self) -> std::io::Result<ExitStatus> {
        let status = self.child.as_mut().expect("active worker process").wait()?;
        self.child = None;
        Ok(status)
    }

    fn try_wait(&mut self) -> std::io::Result<Option<ExitStatus>> {
        let Some(child) = self.child.as_mut() else {
            return Ok(None);
        };
        let status = child.try_wait()?;
        if status.is_some() {
            self.child = None;
        }
        Ok(status)
    }

    fn active(&self) -> bool {
        self.child.is_some()
    }

    fn abort(&mut self) {
        if let Some(child) = self.child.as_mut() {
            let _ = child.kill();
            let _ = child.wait();
        }
        self.child = None;
    }
}

impl Drop for Process {
    fn drop(&mut self) {
        self.abort();
    }
}

pub(crate) struct WorkerCleanup {
    child: Process,
    control: Control,
    last_problem: PublicationProblem,
}

impl WorkerCleanup {
    fn new(child: Process, control: Control, last_problem: PublicationProblem) -> Self {
        Self {
            child,
            control,
            last_problem,
        }
    }

    pub(crate) fn release(&mut self) -> Result<()> {
        if !self.child.active() {
            return Ok(());
        }
        self.control.set_state(State::CleanupRequest);
        let deadline = Instant::now() + START_LIMIT;
        loop {
            if self.control.state() == Some(State::CleanupResult) {
                let (complete, problem) = wire_validation::read_cleanup_result(&self.control)?;
                if complete {
                    let status = self.child.wait()?;
                    return if status.success() {
                        Ok(())
                    } else {
                        Err(Error::Conflict(
                            "SDK cleanup worker completion status is invalid",
                        ))
                    };
                }
                self.last_problem = problem.ok_or(Error::Conflict(
                    "SDK cleanup worker omitted its cleanup problem",
                ))?;
                return Err(self.operation_error());
            }
            if self.child.try_wait()?.is_some() {
                self.last_problem = PublicationProblem::new(
                    crate::ErrorCode::Conflict,
                    None,
                    "isolated cleanup worker exited unexpectedly",
                );
                return Err(self.operation_error());
            }
            if Instant::now() >= deadline {
                self.last_problem = PublicationProblem::new(
                    crate::ErrorCode::Conflict,
                    None,
                    "isolated cleanup worker timed out",
                );
                return Err(self.operation_error());
            }
            std::thread::sleep(Duration::from_millis(1));
        }
    }

    pub(crate) fn last_problem(&self) -> PublicationProblem {
        self.last_problem.clone()
    }

    fn operation_error(&self) -> Error {
        Error::WorkerOperation {
            code: self.last_problem.code,
            os_code: self.last_problem.os_code,
        }
    }
}

impl std::fmt::Debug for WorkerCleanup {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output
            .debug_struct("WorkerCleanup")
            .field("worker_pid", &self.child.active().then(|| self.child.id()))
            .field("last_problem", &self.last_problem)
            .finish()
    }
}

#[cfg(all(test, unix))]
#[path = "client_tests.rs"]
mod tests;

fn worker_candidates() -> Result<Vec<PathBuf>> {
    let name = format!("iprange-v4-worker{}", std::env::consts::EXE_SUFFIX);
    let current = std::env::current_exe()?;
    let mut candidates = Vec::with_capacity(2);
    if let Some(directory) = current.parent() {
        candidates.push(directory.join(&name));
        // Cargo places integration-test executables in `target/*/deps` and
        // package binaries in its parent. The build-ID handshake still rejects
        // every unrelated executable.
        if directory.file_name().is_some_and(|part| part == "deps") {
            if let Some(target) = directory.parent() {
                candidates.push(target.join(&name));
            }
        }
    }
    candidates.dedup();
    Ok(candidates)
}
