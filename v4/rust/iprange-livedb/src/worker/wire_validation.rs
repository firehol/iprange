use std::path::{Path, PathBuf};

use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::error::{Error, Result};
use crate::publication::{CleanupArtifacts, CoordinationCleanup, PublicationProblem};
use crate::validation::{
    ValidatedGeneration, ValidationBudget, ValidationFailure, ValidationFinding, ValidationMode,
    ValidationObject, ValidationReason, ValidationResult,
};

use super::control::Control;
use super::wire::{self, Reader, Writer};
use super::wire_publication;

pub(super) struct Request {
    pub(super) path: PathBuf,
    pub(super) mode: ValidationMode,
    pub(super) budget: ValidationBudget,
    pub(super) unreadable_pages: Vec<u32>,
    pub(super) delivered_findings: u64,
}

pub(super) fn write_request(
    control: &Control,
    path: &Path,
    mode: &ValidationMode,
    budget: &ValidationBudget,
    unreadable_pages: &[u32],
    delivered_findings: u64,
) -> Result<()> {
    let mut output = Writer::new(control);
    output.path(path)?;
    match mode {
        ValidationMode::ImmutableCurrent => output.byte(1)?,
        ValidationMode::LiveCurrent => output.byte(2)?,
        ValidationMode::OfflineCandidate(candidate) => {
            output.byte(3)?;
            wire::recovery_candidate(&mut output, candidate)?;
        }
    }
    wire::validation_budget(&mut output, budget)?;
    wire::u32_list(
        &mut output,
        unreadable_pages,
        Error::InvalidArgument("too many unreadable source pages"),
    )?;
    output.u64(delivered_findings)?;
    output.finish()
}

pub(super) fn read_request(control: &Control) -> Result<Request> {
    let mut input = Reader::new(control)?;
    let path = input.path()?;
    let mode = match input.byte()? {
        1 => ValidationMode::ImmutableCurrent,
        2 => ValidationMode::LiveCurrent,
        3 => ValidationMode::OfflineCandidate(wire::read_recovery_candidate(&mut input)?),
        _ => return Err(Error::Corrupt("worker validation mode is invalid")),
    };
    let mut budget = wire::read_validation_budget(&mut input)?;
    let unreadable_pages = wire::read_u32_list(&mut input, &mut budget.max_heap_bytes)?;
    let delivered_findings = input.u64()?;
    input.finish()?;
    Ok(Request {
        path,
        mode,
        budget,
        unreadable_pages,
        delivered_findings,
    })
}

pub(super) fn write_result(
    control: &Control,
    result: &std::result::Result<ValidationResult, ValidationFailure>,
    retained_problem: Option<PublicationProblem>,
) -> Result<()> {
    let mut output = Writer::new(control);
    match result {
        Ok(result) => {
            output.byte(0)?;
            output.bool(result.valid)?;
            wire::identity(&mut output, result.file_identity)?;
            output.bool(result.generation.is_some())?;
            if let Some(generation) = result.generation {
                write_generation(&mut output, generation)?;
            }
            wire::progress(&mut output, &result.progress)?;
        }
        Err(failure) => {
            output.byte(1)?;
            wire::encode_error(&mut output, &failure.cause)?;
            wire::progress(&mut output, &failure.progress)?;
            output.bool(retained_problem.is_some())?;
            if let Some(problem) = retained_problem {
                wire_publication::problem(&mut output, &problem)?;
            }
        }
    }
    output.finish()
}

pub(super) fn read_result(
    control: &Control,
) -> (
    std::result::Result<ValidationResult, ValidationFailure>,
    Option<PublicationProblem>,
) {
    read_result_inner(control).unwrap_or_else(|cause| {
        (
            Err(ValidationFailure {
                cause,
                progress: Box::new(crate::validation::ValidationProgress::from_wire(
                    0,
                    0,
                    0,
                    crate::Cardinality129::ZERO,
                    false,
                    [0; ValidationReason::COUNT],
                    [0; ValidationObject::COUNT],
                )),
                cleanup: Box::new(CleanupArtifacts::new()),
                coordination_cleanup: CoordinationCleanup::None,
                source_cleanup: None,
            }),
            None,
        )
    })
}

fn read_result_inner(
    control: &Control,
) -> Result<(
    std::result::Result<ValidationResult, ValidationFailure>,
    Option<PublicationProblem>,
)> {
    let mut input = Reader::new(control)?;
    let (result, retained_problem) = match input.byte()? {
        0 => {
            let valid = input.bool()?;
            let file_identity = wire::read_identity(&mut input)?;
            let generation = input
                .bool()?
                .then(|| read_generation(&mut input))
                .transpose()?;
            let progress = wire::read_progress(&mut input)?;
            (
                Ok(ValidationResult {
                    valid,
                    file_identity,
                    generation,
                    progress,
                }),
                None,
            )
        }
        1 => {
            let cause = wire::read_error(&mut input)?;
            let progress = wire::read_progress(&mut input)?;
            let retained_problem = input
                .bool()?
                .then(|| wire_publication::read_problem(&mut input))
                .transpose()?;
            (
                Err(ValidationFailure {
                    cause,
                    progress: Box::new(progress),
                    cleanup: Box::new(CleanupArtifacts::new()),
                    coordination_cleanup: CoordinationCleanup::None,
                    source_cleanup: None,
                }),
                retained_problem,
            )
        }
        _ => return Err(Error::Corrupt("worker validation result tag is invalid")),
    };
    input.finish()?;
    Ok((result, retained_problem))
}

pub(super) fn write_cleanup_result(
    control: &Control,
    complete: bool,
    problem: Option<PublicationProblem>,
) -> Result<()> {
    let mut output = Writer::new(control);
    output.bool(complete)?;
    output.bool(problem.is_some())?;
    if let Some(problem) = problem {
        wire_publication::problem(&mut output, &problem)?;
    }
    output.finish()
}

pub(super) fn read_cleanup_result(control: &Control) -> Result<(bool, Option<PublicationProblem>)> {
    let mut input = Reader::new(control)?;
    let complete = input.bool()?;
    let problem = input
        .bool()?
        .then(|| wire_publication::read_problem(&mut input))
        .transpose()?;
    input.finish()?;
    Ok((complete, problem))
}

pub(super) fn write_finding(control: &Control, finding: &ValidationFinding) -> Result<()> {
    let mut output = Writer::new(control);
    output.u64(finding.sequence)?;
    output.byte(finding.reason as u8)?;
    output.byte(finding.object as u8)?;
    wire::optional_u32(&mut output, finding.page_number)?;
    wire::optional_interval(&mut output, finding.physical_bytes)?;
    wire::optional_u32(&mut output, finding.related_page_number)?;
    wire::optional_fence(&mut output, finding.address_fence)?;
    output.finish()
}

pub(super) fn read_finding(control: &Control) -> Result<ValidationFinding> {
    let mut input = Reader::new(control)?;
    let finding = ValidationFinding {
        sequence: input.u64()?,
        reason: ValidationReason::from_wire(input.byte()?)
            .ok_or(Error::Corrupt("worker validation reason is invalid"))?,
        object: ValidationObject::from_wire(input.byte()?)
            .ok_or(Error::Corrupt("worker validation object is invalid"))?,
        page_number: wire::read_optional_u32(&mut input)?,
        physical_bytes: wire::read_optional_interval(&mut input)?,
        related_page_number: wire::read_optional_u32(&mut input)?,
        address_fence: wire::read_optional_fence(&mut input, "worker validation fence is invalid")?,
    };
    input.finish()?;
    Ok(finding)
}

fn write_generation(output: &mut Writer<'_>, value: ValidatedGeneration) -> Result<()> {
    output.byte(value.address_family as u8)?;
    output.byte(value.value_kind as u8)?;
    output.bytes(value.value_tag.as_wire())?;
    output.bytes(&value.database_id)?;
    output.u64(value.transaction_id)?;
    output.bytes(&value.commit_nonce)?;
    output.u64(value.page_count)?;
    for root in value.roots {
        output.u32(root)?;
    }
    Ok(())
}

fn read_generation(input: &mut Reader<'_>) -> Result<ValidatedGeneration> {
    let address_family = AddressFamily::from_wire(input.byte()?)
        .ok_or(Error::Corrupt("worker address family is invalid"))?;
    let value_kind = ValueKind::from_wire(input.byte()?)
        .ok_or(Error::Corrupt("worker value kind is invalid"))?;
    let value_tag =
        ValueTag::from_wire(input.array()?).ok_or(Error::Corrupt("worker value tag is invalid"))?;
    let database_id = input.array()?;
    let transaction_id = input.u64()?;
    let commit_nonce = input.array()?;
    let page_count = input.u64()?;
    let mut roots = [0; 10];
    for root in &mut roots {
        *root = input.u32()?;
    }
    Ok(ValidatedGeneration {
        address_family,
        value_kind,
        value_tag,
        database_id,
        transaction_id,
        commit_nonce,
        page_count,
        roots,
    })
}
