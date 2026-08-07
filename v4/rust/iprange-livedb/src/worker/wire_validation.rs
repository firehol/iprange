use std::path::{Path, PathBuf};

use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::publication::{CleanupArtifacts, CoordinationCleanup, PublicationProblem};
use crate::validation::{
    PhysicalByteInterval, ValidatedGeneration, ValidationAddressFence, ValidationBudget,
    ValidationFailure, ValidationFinding, ValidationMode, ValidationObject, ValidationReason,
    ValidationResult,
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
    output.u64(budget.max_heap_bytes)?;
    output.u32(budget.max_open_files)?;
    output.u64(budget.max_scratch_bytes)?;
    output.u32(budget.max_scratch_files)?;
    output.optional_path(budget.scratch_directory.as_deref())?;
    output.u32(
        u32::try_from(unreadable_pages.len())
            .map_err(|_| Error::InvalidArgument("too many unreadable source pages"))?,
    )?;
    for page in unreadable_pages {
        output.u32(*page)?;
    }
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
    let mut budget = ValidationBudget {
        max_heap_bytes: input.u64()?,
        max_open_files: input.u32()?,
        max_scratch_bytes: input.u64()?,
        max_scratch_files: input.u32()?,
        scratch_directory: input.optional_path()?,
    };
    let count = input.u32()? as usize;
    let unreadable_bytes = count
        .checked_mul(std::mem::size_of::<u32>())
        .ok_or(Error::ArithmeticOverflow("unreadable source-page list"))?
        as u64;
    budget.max_heap_bytes = budget
        .max_heap_bytes
        .checked_sub(unreadable_bytes)
        .ok_or(Error::BudgetExceeded("unreadable source-page list"))?;
    let mut unreadable_pages = Vec::new();
    unreadable_pages
        .try_reserve_exact(count)
        .map_err(|_| Error::BudgetExceeded("unreadable source-page list"))?;
    for _ in 0..count {
        unreadable_pages.push(input.u32()?);
    }
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
    optional_u32(&mut output, finding.page_number)?;
    optional_interval(&mut output, finding.physical_bytes)?;
    optional_u32(&mut output, finding.related_page_number)?;
    optional_fence(&mut output, finding.address_fence)?;
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
        page_number: read_optional_u32(&mut input)?,
        physical_bytes: read_optional_interval(&mut input)?,
        related_page_number: read_optional_u32(&mut input)?,
        address_fence: read_optional_fence(&mut input)?,
    };
    input.finish()?;
    Ok(finding)
}

pub(super) fn write_callback_error(control: &Control, error: &Error) -> Result<()> {
    let mut output = Writer::new(control);
    wire::encode_error(&mut output, error)?;
    output.finish()
}

pub(super) fn read_callback_error(control: &Control) -> Result<Error> {
    let mut input = Reader::new(control)?;
    let error = wire::read_error(&mut input)?;
    input.finish()?;
    Ok(error)
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

fn optional_u32(output: &mut Writer<'_>, value: Option<u32>) -> Result<()> {
    output.bool(value.is_some())?;
    if let Some(value) = value {
        output.u32(value)?;
    }
    Ok(())
}

fn read_optional_u32(input: &mut Reader<'_>) -> Result<Option<u32>> {
    input.bool()?.then(|| input.u32()).transpose()
}

fn optional_interval(output: &mut Writer<'_>, value: Option<PhysicalByteInterval>) -> Result<()> {
    output.bool(value.is_some())?;
    if let Some(value) = value {
        output.u64(value.start)?;
        output.u64(value.end_exclusive)?;
    }
    Ok(())
}

fn read_optional_interval(input: &mut Reader<'_>) -> Result<Option<PhysicalByteInterval>> {
    input
        .bool()?
        .then(|| {
            Ok(PhysicalByteInterval {
                start: input.u64()?,
                end_exclusive: input.u64()?,
            })
        })
        .transpose()
}

fn optional_fence(output: &mut Writer<'_>, value: Option<ValidationAddressFence>) -> Result<()> {
    match value {
        None => output.byte(0),
        Some(ValidationAddressFence::Ipv4 { from, to }) => {
            output.byte(1)?;
            output.u32(from.0)?;
            output.u32(to.0)
        }
        Some(ValidationAddressFence::Ipv6 { from, to }) => {
            output.byte(2)?;
            output.u64(from.hi)?;
            output.u64(from.lo)?;
            output.u64(to.hi)?;
            output.u64(to.lo)
        }
    }
}

fn read_optional_fence(input: &mut Reader<'_>) -> Result<Option<ValidationAddressFence>> {
    match input.byte()? {
        0 => Ok(None),
        1 => Ok(Some(ValidationAddressFence::Ipv4 {
            from: Ipv4Key(input.u32()?),
            to: Ipv4Key(input.u32()?),
        })),
        2 => Ok(Some(ValidationAddressFence::Ipv6 {
            from: Ipv6Key {
                hi: input.u64()?,
                lo: input.u64()?,
            },
            to: Ipv6Key {
                hi: input.u64()?,
                lo: input.u64()?,
            },
        })),
        _ => Err(Error::Corrupt("worker validation fence is invalid")),
    }
}
