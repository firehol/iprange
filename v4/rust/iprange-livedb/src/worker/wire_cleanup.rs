use std::path::{Path, PathBuf};

use crate::error::{Error, Result};
use crate::publication::cleanup::EarlyDiscard;
use crate::publication::{CleanupArtifacts, CreationSecurity, PublicationProblem};
use crate::recovery::{ScratchCleanup, ScratchProblem, ScratchResidue};

use super::control::{Control, ScratchCheckpoint, ScratchCheckpointEntry};
use super::wire::{self, Reader, Writer};
use super::wire_publication;

pub(super) struct Request {
    pub(super) destination_path: PathBuf,
    pub(super) output: crate::publication::PrivateOutputAttempt,
    pub(super) scratch_directory: Option<PathBuf>,
    pub(super) scratch: Option<ScratchCheckpoint>,
}

pub(super) fn write_request(
    control: &Control,
    destination_path: &Path,
    output_attempt: &crate::publication::PrivateOutputAttempt,
    scratch_directory: Option<&Path>,
    scratch: Option<&ScratchCheckpoint>,
) -> Result<()> {
    let mut output = Writer::new(control);
    output.path(destination_path)?;
    wire_publication::private_output(&mut output, output_attempt)?;
    output.optional_path(scratch_directory)?;
    write_checkpoint(&mut output, scratch)?;
    output.finish()
}

pub(super) fn read_request(control: &Control) -> Result<Request> {
    let mut input = Reader::new(control)?;
    let request = Request {
        destination_path: input.path()?,
        output: wire_publication::read_private_output(&mut input)?,
        scratch_directory: input.optional_path()?,
        scratch: read_checkpoint(&mut input)?,
    };
    input.finish()?;
    if request.scratch.is_some() != request.scratch_directory.is_some() {
        return Err(Error::Corrupt(
            "worker cleanup scratch path and checkpoint disagree",
        ));
    }
    Ok(request)
}

pub(super) fn write_result(
    control: &Control,
    discarded: &EarlyDiscard,
    scratch: Option<&ScratchCleanup>,
) -> Result<()> {
    let mut output = Writer::new(control);
    wire_publication::private_output(&mut output, &discarded.output)?;
    let mut artifacts = CleanupArtifacts::new();
    if let Some(artifact) = &discarded.artifact {
        artifacts.push(artifact.clone());
    }
    wire_publication::cleanup(&mut output, &artifacts)?;
    output.byte(wire_publication::housekeeping_value(discarded.housekeeping))?;
    wire_publication::write_housekeeping_list(&mut output, &discarded.visible_housekeeping)?;
    write_scratch(&mut output, scratch)?;
    output.finish()
}

pub(super) fn read_result(control: &Control) -> Result<(EarlyDiscard, Option<ScratchCleanup>)> {
    let mut input = Reader::new(control)?;
    let output = wire_publication::read_private_output(&mut input)?;
    let artifacts = wire_publication::read_cleanup(&mut input)?;
    if artifacts.len() > 1 {
        return Err(Error::Corrupt(
            "worker cleanup returned multiple output residues",
        ));
    }
    let discarded = EarlyDiscard {
        output,
        artifact: artifacts.get(0).cloned(),
        housekeeping: wire_publication::read_housekeeping_value(input.byte()?)?,
        visible_housekeeping: wire_publication::read_housekeeping_artifacts(&mut input)?,
    };
    let scratch = read_scratch(&mut input)?;
    input.finish()?;
    Ok((discarded, scratch))
}

fn write_checkpoint(output: &mut Writer<'_>, checkpoint: Option<&ScratchCheckpoint>) -> Result<()> {
    output.bool(checkpoint.is_some())?;
    let Some(checkpoint) = checkpoint else {
        return Ok(());
    };
    output.bytes(&checkpoint.attempt_id)?;
    wire::identity(output, checkpoint.directory_identity)?;
    output.u16(checkpoint.creation_security.kind)?;
    output.bytes(&checkpoint.creation_security.commitment)?;
    output.byte(
        u8::try_from(checkpoint.entries.len())
            .map_err(|_| Error::BudgetExceeded("worker scratch checkpoint entries"))?,
    )?;
    for entry in &checkpoint.entries {
        output.u32(entry.ordinal)?;
        wire::identity(output, entry.identity)?;
    }
    Ok(())
}

fn read_checkpoint(input: &mut Reader<'_>) -> Result<Option<ScratchCheckpoint>> {
    if !input.bool()? {
        return Ok(None);
    }
    let attempt_id = input.array()?;
    let directory_identity = wire::read_identity(input)?;
    let creation_security = CreationSecurity {
        kind: input.u16()?,
        commitment: input.array()?,
    };
    let count = input.byte()? as usize;
    if attempt_id == [0; 16]
        || !valid_identity(directory_identity)
        || !valid_security(creation_security.kind, creation_security.commitment)
        || count > 2
    {
        return Err(Error::Corrupt("worker scratch checkpoint is invalid"));
    }
    let mut entries = Vec::with_capacity(count);
    for _ in 0..count {
        let entry = ScratchCheckpointEntry {
            ordinal: input.u32()?,
            identity: wire::read_identity(input)?,
        };
        if !valid_identity(entry.identity)
            || entries.iter().any(|prior: &ScratchCheckpointEntry| {
                prior.ordinal == entry.ordinal || prior.identity == entry.identity
            })
        {
            return Err(Error::Corrupt(
                "worker scratch checkpoint contains duplicate authority",
            ));
        }
        entries.push(entry);
    }
    Ok(Some(ScratchCheckpoint {
        attempt_id,
        directory_identity,
        creation_security,
        entries,
    }))
}

fn write_scratch(output: &mut Writer<'_>, cleanup: Option<&ScratchCleanup>) -> Result<()> {
    output.bool(cleanup.is_some())?;
    let Some(cleanup) = cleanup else {
        return Ok(());
    };
    output.bytes(&cleanup.attempt_id)?;
    wire::identity(output, cleanup.directory_identity)?;
    output.u16(cleanup.creation_security_kind)?;
    output.bytes(&cleanup.creation_security_commitment)?;
    output.byte(
        u8::try_from(cleanup.residues.len())
            .map_err(|_| Error::BudgetExceeded("worker scratch cleanup residues"))?,
    )?;
    for residue in &cleanup.residues {
        output.u32(residue.ordinal)?;
        wire::identity(output, residue.directory_identity)?;
        output.sized_bytes(&residue.basename)?;
        wire::identity(output, residue.identity)?;
        output.u16(residue.creation_security_kind)?;
        output.bytes(&residue.creation_security_commitment)?;
        wire_publication::problem(
            output,
            &PublicationProblem {
                code: residue.problem.code,
                os_code: residue.problem.os_code,
                detail: residue.problem.detail.clone(),
            },
        )?;
    }
    output.byte(wire_publication::housekeeping_value(cleanup.housekeeping))?;
    wire_publication::write_housekeeping_list(output, &cleanup.visible_housekeeping)
}

fn read_scratch(input: &mut Reader<'_>) -> Result<Option<ScratchCleanup>> {
    if !input.bool()? {
        return Ok(None);
    }
    let attempt_id = input.array()?;
    let directory_identity = wire::read_identity(input)?;
    let creation_security_kind = input.u16()?;
    let creation_security_commitment = input.array()?;
    let count = input.byte()? as usize;
    if attempt_id == [0; 16]
        || !valid_identity(directory_identity)
        || !valid_security(creation_security_kind, creation_security_commitment)
        || count > 2
    {
        return Err(Error::Corrupt("worker scratch cleanup is invalid"));
    }
    let mut residues = Vec::with_capacity(count);
    for _ in 0..count {
        let ordinal = input.u32()?;
        let residue_directory = wire::read_identity(input)?;
        let basename = input.boxed_bytes()?;
        let identity = wire::read_identity(input)?;
        let security_kind = input.u16()?;
        let security_commitment = input.array()?;
        let problem = wire_publication::read_problem(input)?;
        let expected_basename = crate::recovery::checkpoint_basename(attempt_id, ordinal)?;
        if residue_directory != directory_identity
            || !valid_identity(identity)
            || security_kind != creation_security_kind
            || security_commitment != creation_security_commitment
            || basename != expected_basename
            || residues.iter().any(|prior: &ScratchResidue| {
                prior.ordinal == ordinal || prior.identity == identity
            })
        {
            return Err(Error::Corrupt(
                "worker scratch cleanup authority is inconsistent",
            ));
        }
        residues.push(ScratchResidue {
            ordinal,
            directory_identity: residue_directory,
            basename,
            identity,
            creation_security_kind: security_kind,
            creation_security_commitment: security_commitment,
            problem: ScratchProblem {
                code: problem.code,
                os_code: problem.os_code,
                detail: problem.detail,
            },
        });
    }
    Ok(Some(ScratchCleanup {
        attempt_id,
        directory_identity,
        creation_security_kind,
        creation_security_commitment,
        residues,
        housekeeping: wire_publication::read_housekeeping_value(input.byte()?)?,
        visible_housekeeping: wire_publication::read_housekeeping_artifacts(input)?.into_vec(),
    }))
}

fn valid_identity(identity: crate::validation::LocalFileIdentity) -> bool {
    identity.kind == crate::publication::namespace::IDENTITY_KIND
        && crate::publication::namespace::Identity::decode(identity.bytes).is_some()
}

fn valid_security(kind: u16, commitment: [u8; 32]) -> bool {
    kind == crate::publication::namespace::CREATION_SECURITY_KIND && commitment != [0; 32]
}
