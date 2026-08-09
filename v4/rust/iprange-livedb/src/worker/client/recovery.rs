use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::publication::cleanup;
use crate::publication::output::CreatedOutput;
use crate::publication::problem::Problem;
use crate::publication::{CleanupState, CoordinationCleanup, Housekeeping, PublicationProblem};
use crate::recovery::{
    RecoveryBudget, RecoveryCandidate, RecoveryOutcome, RecoveryPreparationFailure, RecoveryReport,
    RecoverySink, RecoverySinkControl, RecoverySourceCleanupGuard, ScratchCleanup, ScratchProblem,
    ScratchResidue, WorkerMode,
};

use super::{
    acknowledge_callback, advance_sequence, drive_loop, handshake, record_unreadable_page, spawn,
    CallbackDecision, Drive, Process, WorkerCleanup,
};
use crate::worker::control::{
    CallbackCheckpoint, Control, FaultRecord, MappingRole, Opcode, ScratchCheckpoint, State,
};
use crate::worker::{wire, wire_recovery};

pub(in crate::worker) fn recover<S: RecoverySink>(
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
                let (discarded, scratch) = crate::worker::cleanup::discard(
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
                let (discarded, scratch) = crate::worker::cleanup::discard(
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
pub(super) fn recover_once<S: RecoverySink>(
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
    let (discarded, scratch) = crate::worker::cleanup::discard(
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
pub(super) enum RecoveryAttempt {
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

pub(super) fn read_recovery_callback_report(control: &Control) -> Result<RecoveryReport> {
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

pub(super) fn discard_clean(value: &cleanup::EarlyDiscard) -> bool {
    value.artifact.is_none()
        && matches!(value.housekeeping, crate::publication::Housekeeping::None)
        && value.visible_housekeeping.is_empty()
}

pub(super) fn scratch_clean(value: &Option<ScratchCleanup>) -> bool {
    value.as_ref().map_or(true, |cleanup| {
        cleanup.clean()
            && matches!(cleanup.housekeeping, Housekeeping::None)
            && cleanup.visible_housekeeping.is_empty()
    })
}

pub(in crate::worker) fn cleanup_checkpoint(
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
                cleanup.housekeeping = cleanup.housekeeping.merge(removal.housekeeping);
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

pub(in crate::worker) fn fault_problem(role: MappingRole) -> PublicationProblem {
    let detail = match role {
        MappingRole::Source => "recovery source mapping faulted",
        MappingRole::Scratch => "recovery scratch mapping faulted",
        MappingRole::Output => "recovery output mapping faulted",
        MappingRole::Coordination => "recovery coordination mapping faulted",
    };
    PublicationProblem::new(crate::ErrorCode::Io, None, detail)
}

fn drive_recovery<S: RecoverySink>(
    child: &mut Process,
    control: &Control,
    cancellation: &CancellationToken,
    sink: &mut S,
    delivered_unknowns: &mut u64,
    callback: &mut Option<CallbackDecision>,
) -> Result<Drive> {
    drive_loop(
        child,
        control,
        cancellation,
        "SDK worker emitted an unexpected event",
        |state, child, control| {
            if state != State::Unknown {
                return Ok(false);
            }
            let unknown = wire_recovery::read_unknown(control)?;
            advance_sequence(
                child,
                delivered_unknowns,
                unknown.sequence,
                "worker recovery envelope sequence is invalid",
            )?;
            let result = sink
                .unknown(&unknown)
                .map(|value| value == RecoverySinkControl::Stop);
            acknowledge_callback(control, result, callback)?;
            Ok(true)
        },
    )
}
