use std::path::Path;

use crate::error::{Error, Result};
use crate::publication::cleanup::{self as publication_cleanup, EarlyDiscard};
use crate::publication::output::resume_secured_output_for_cleanup;
use crate::publication::problem::Problem;
use crate::publication::{Housekeeping, PrivateOutputAttempt, PublicationProblem};
use crate::recovery::{ScratchCleanup, ScratchProblem, ScratchResidue};

use super::client::{self, Drive};
use super::control::{Control, Opcode, ScratchCheckpoint};
use super::wire_cleanup;

pub(super) fn run_worker(
    control: &Control,
) -> Result<Option<crate::recovery::RecoverySourceCleanupGuard>> {
    let request = wire_cleanup::read_request(control)?;
    let discarded =
        match resume_secured_output_for_cleanup(&request.destination_path, &request.output) {
            Ok(Some((attempt, file))) => publication_cleanup::discard_attempt(&attempt, &file),
            Ok(None) => publication_cleanup::confirmed_absent(request.output),
            Err(cause) => {
                publication_cleanup::failed_attempt(request.output, Problem::output(&cause))
            }
        };
    let scratch = client::cleanup_checkpoint(request.scratch_directory.as_deref(), request.scratch);
    wire_cleanup::write_result(control, &discarded, scratch.as_ref())?;
    Ok(None)
}

pub(super) fn discard(
    destination_path: &Path,
    output: PrivateOutputAttempt,
    scratch_directory: Option<&Path>,
    scratch: Option<ScratchCheckpoint>,
) -> (EarlyDiscard, Option<ScratchCleanup>) {
    match discard_inner(
        destination_path,
        &output,
        scratch_directory,
        scratch.as_ref(),
    ) {
        Ok(result) => result,
        Err(problem) => failed(output, scratch, problem),
    }
}

fn discard_inner(
    destination_path: &Path,
    output: &PrivateOutputAttempt,
    scratch_directory: Option<&Path>,
    scratch: Option<&ScratchCheckpoint>,
) -> std::result::Result<(EarlyDiscard, Option<ScratchCleanup>), PublicationProblem> {
    let mut control = Control::create_parent().map_err(worker_problem)?;
    control.set_opcode(Opcode::CleanupRecoveryAttempt);
    wire_cleanup::write_request(
        &control,
        destination_path,
        output,
        scratch_directory,
        scratch,
    )
    .map_err(worker_problem)?;
    let mut child = client::spawn(&control).map_err(worker_problem)?;
    client::start(&mut child, &mut control).map_err(worker_problem)?;
    match client::drive(&mut child, &control, &crate::CancellationToken::new())
        .map_err(worker_problem)?
    {
        Drive::Complete {
            guard_pending: false,
        } => wire_cleanup::read_result(&control).map_err(worker_problem),
        Drive::Complete {
            guard_pending: true,
        } => Err(PublicationProblem::new(
            crate::ErrorCode::Conflict,
            None,
            "isolated recovery cleanup retained unexpected authority",
        )),
        Drive::Fault(fault) => Err(client::fault_problem(fault.role)),
    }
}

fn failed(
    output: PrivateOutputAttempt,
    checkpoint: Option<ScratchCheckpoint>,
    problem: PublicationProblem,
) -> (EarlyDiscard, Option<ScratchCleanup>) {
    let discarded = publication_cleanup::failed_attempt(output, problem.clone());
    let scratch = checkpoint.map(|checkpoint| {
        let mut cleanup = ScratchCleanup {
            attempt_id: checkpoint.attempt_id,
            directory_identity: checkpoint.directory_identity,
            creation_security_kind: checkpoint.creation_security.kind,
            creation_security_commitment: checkpoint.creation_security.commitment,
            residues: Vec::with_capacity(checkpoint.entries.len()),
            housekeeping: Housekeeping::None,
            visible_housekeeping: Vec::new(),
        };
        for entry in checkpoint.entries {
            let basename =
                crate::recovery::checkpoint_basename(checkpoint.attempt_id, entry.ordinal)
                    .expect("validated scratch checkpoint has a fixed valid basename");
            cleanup.residues.push(ScratchResidue {
                ordinal: entry.ordinal,
                directory_identity: checkpoint.directory_identity,
                basename,
                identity: entry.identity,
                creation_security_kind: checkpoint.creation_security.kind,
                creation_security_commitment: checkpoint.creation_security.commitment,
                problem: ScratchProblem {
                    code: problem.code,
                    os_code: problem.os_code,
                    detail: problem.detail.clone(),
                },
            });
        }
        cleanup
    });
    (discarded, scratch)
}

fn worker_problem(cause: Error) -> PublicationProblem {
    let os_code = match &cause {
        Error::Io(source) => source.raw_os_error(),
        Error::WorkerOperation { os_code, .. } => *os_code,
        _ => None,
    };
    PublicationProblem::new(cause.code(), os_code, "isolated recovery cleanup failed")
}
