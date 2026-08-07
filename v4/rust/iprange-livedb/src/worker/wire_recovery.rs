use std::path::{Path, PathBuf};

use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::publication::{CreationSecurity, PublicationProblem};
use crate::recovery::{
    RecoveryBudget, RecoveryCandidate, RecoveryLogicalCounts, RecoveryOutcome, RecoveryPageCounts,
    RecoveryPreparationFailure, RecoveryReport, RecoveryResult, RecoveryScratchAttempt,
    RecoveryUnknownEnvelope,
};
use crate::validation::{
    PhysicalByteInterval, ValidationAddressFence, ValidationObject, ValidationReason,
};

use super::control::Control;
use super::wire::{self, Reader, Writer};
use super::wire_publication;

pub(super) struct Request {
    pub(super) source_path: PathBuf,
    pub(super) destination_path: PathBuf,
    pub(super) candidate: RecoveryCandidate,
    pub(super) mode: crate::recovery::WorkerMode,
    pub(super) budget: RecoveryBudget,
    pub(super) output: crate::publication::PrivateOutputAttempt,
    pub(super) unreadable_pages: Vec<u32>,
    pub(super) delivered_unknowns: u64,
}

#[allow(clippy::too_many_arguments)]
pub(super) fn write_request(
    control: &Control,
    source_path: &Path,
    destination_path: &Path,
    candidate: &RecoveryCandidate,
    mode: crate::recovery::WorkerMode,
    budget: &RecoveryBudget,
    output_attempt: &crate::publication::PrivateOutputAttempt,
    unreadable_pages: &[u32],
    delivered_unknowns: u64,
) -> Result<()> {
    let mut output = Writer::new(control);
    output.path(source_path)?;
    output.path(destination_path)?;
    wire::recovery_candidate(&mut output, candidate)?;
    output.byte(mode_tag(mode))?;
    write_budget(&mut output, budget)?;
    wire_publication::private_output(&mut output, output_attempt)?;
    output.u32(
        u32::try_from(unreadable_pages.len())
            .map_err(|_| Error::BudgetExceeded("unreadable source-page list"))?,
    )?;
    for page in unreadable_pages {
        output.u32(*page)?;
    }
    output.u64(delivered_unknowns)?;
    output.finish()
}

pub(super) fn read_request(control: &Control) -> Result<Request> {
    let mut input = Reader::new(control)?;
    let source_path = input.path()?;
    let destination_path = input.path()?;
    let candidate = wire::read_recovery_candidate(&mut input)?;
    let mode = read_mode(input.byte()?)?;
    let mut budget = read_budget(&mut input)?;
    let output = wire_publication::read_private_output(&mut input)?;
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
    let delivered_unknowns = input.u64()?;
    input.finish()?;
    Ok(Request {
        source_path,
        destination_path,
        candidate,
        mode,
        budget,
        output,
        unreadable_pages,
        delivered_unknowns,
    })
}

pub(super) fn write_outcome(
    control: &Control,
    outcome: &RecoveryOutcome,
    retained_problem: Option<PublicationProblem>,
) -> Result<()> {
    let mut output = Writer::new(control);
    match outcome {
        Ok(result) => {
            output.byte(0)?;
            write_result(&mut output, result)?;
        }
        Err(failure) => {
            output.byte(1)?;
            write_failure(&mut output, failure)?;
            output.bool(retained_problem.is_some())?;
            if let Some(problem) = retained_problem {
                wire_publication::problem(&mut output, &problem)?;
            }
        }
    }
    output.finish()
}

pub(super) fn read_outcome(
    control: &Control,
) -> Result<(RecoveryOutcome, Option<PublicationProblem>)> {
    let mut input = Reader::new(control)?;
    let value = match input.byte()? {
        0 => (Ok(read_result(&mut input)?), None),
        1 => {
            let failure = read_failure(&mut input)?;
            let retained = input
                .bool()?
                .then(|| wire_publication::read_problem(&mut input))
                .transpose()?;
            (Err(Box::new(failure)), retained)
        }
        _ => return Err(Error::Corrupt("worker recovery result tag is invalid")),
    };
    input.finish()?;
    Ok(value)
}

pub(super) fn write_unknown(control: &Control, value: &RecoveryUnknownEnvelope) -> Result<()> {
    let mut output = Writer::new(control);
    output.u64(value.sequence)?;
    output.byte(value.reason as u8)?;
    output.byte(value.object as u8)?;
    optional_u32(&mut output, value.page_number)?;
    optional_interval(&mut output, value.physical_bytes)?;
    optional_fence(&mut output, value.address_fence)?;
    output.bool(value.contributes_to_possible_span)?;
    output.bool(value.has_unbounded_extent)?;
    output.finish()
}

pub(super) fn read_unknown(control: &Control) -> Result<RecoveryUnknownEnvelope> {
    let mut input = Reader::new(control)?;
    let value = RecoveryUnknownEnvelope {
        sequence: input.u64()?,
        reason: ValidationReason::from_wire(input.byte()?)
            .ok_or(Error::Corrupt("worker recovery reason is invalid"))?,
        object: ValidationObject::from_wire(input.byte()?)
            .ok_or(Error::Corrupt("worker recovery object is invalid"))?,
        page_number: read_optional_u32(&mut input)?,
        physical_bytes: read_optional_interval(&mut input)?,
        address_fence: read_optional_fence(&mut input)?,
        contributes_to_possible_span: input.bool()?,
        has_unbounded_extent: input.bool()?,
    };
    input.finish()?;
    Ok(value)
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

fn write_result(output: &mut Writer<'_>, value: &RecoveryResult) -> Result<()> {
    report(output, &value.report)?;
    optional_scratch(output, value.scratch.as_ref())?;
    wire_publication::result(output, &value.publication)
}

fn read_result(input: &mut Reader<'_>) -> Result<RecoveryResult> {
    Ok(RecoveryResult {
        report: read_report(input)?,
        scratch: read_optional_scratch(input)?,
        publication: wire_publication::read_result(input)?,
    })
}

fn write_failure(output: &mut Writer<'_>, value: &RecoveryPreparationFailure) -> Result<()> {
    report(output, &value.report)?;
    optional_scratch(output, value.scratch.as_ref())?;
    output.bool(value.output.is_some())?;
    if let Some(attempt) = &value.output {
        wire_publication::private_output(output, attempt)?;
    }
    wire_publication::cleanup(output, &value.cleanup)?;
    output.byte(wire_publication::coordination(value.coordination_cleanup))?;
    output.byte(wire_publication::housekeeping_value(value.housekeeping))?;
    wire_publication::write_housekeeping_list(output, &value.visible_housekeeping)?;
    wire_publication::problem(output, &value.cause)
}

fn read_failure(input: &mut Reader<'_>) -> Result<RecoveryPreparationFailure> {
    Ok(RecoveryPreparationFailure {
        report: read_report(input)?,
        scratch: read_optional_scratch(input)?,
        output: input
            .bool()?
            .then(|| wire_publication::read_private_output(input))
            .transpose()?,
        cleanup: wire_publication::read_cleanup(input)?,
        coordination_cleanup: wire_publication::read_coordination(input.byte()?)?,
        housekeeping: wire_publication::read_housekeeping_value(input.byte()?)?,
        visible_housekeeping: wire_publication::read_housekeeping_artifacts(input)?,
        source_cleanup: None,
        cause: wire_publication::read_problem(input)?,
    })
}

pub(super) fn report(output: &mut Writer<'_>, value: &RecoveryReport) -> Result<()> {
    page_counts(output, value.pages)?;
    logical_counts(output, value.ranges)?;
    logical_counts(output, value.catalog_entries)?;
    logical_counts(output, value.membership_entries)?;
    logical_counts(output, value.metadata_chunks)?;
    logical_counts(output, value.retirement_records)?;
    wire::cardinality(output, value.verified_addresses)?;
    wire::cardinality(output, value.rejected_addresses)?;
    wire::cardinality(output, value.bounded_possible_span_addresses)?;
    output.bool(value.has_unbounded_unknown)?;
    output.u64(value.unknown_envelopes)
}

pub(super) fn read_report(input: &mut Reader<'_>) -> Result<RecoveryReport> {
    Ok(RecoveryReport {
        pages: read_page_counts(input)?,
        ranges: read_logical_counts(input)?,
        catalog_entries: read_logical_counts(input)?,
        membership_entries: read_logical_counts(input)?,
        metadata_chunks: read_logical_counts(input)?,
        retirement_records: read_logical_counts(input)?,
        verified_addresses: wire::read_cardinality(input)?,
        rejected_addresses: wire::read_cardinality(input)?,
        bounded_possible_span_addresses: wire::read_cardinality(input)?,
        has_unbounded_unknown: input.bool()?,
        unknown_envelopes: input.u64()?,
    })
}

fn page_counts(output: &mut Writer<'_>, value: RecoveryPageCounts) -> Result<()> {
    output.u64(value.examined)?;
    output.u64(value.accepted)?;
    output.u64(value.rejected)?;
    output.u64(value.io_unreadable)
}

fn read_page_counts(input: &mut Reader<'_>) -> Result<RecoveryPageCounts> {
    Ok(RecoveryPageCounts {
        examined: input.u64()?,
        accepted: input.u64()?,
        rejected: input.u64()?,
        io_unreadable: input.u64()?,
    })
}

fn logical_counts(output: &mut Writer<'_>, value: RecoveryLogicalCounts) -> Result<()> {
    output.u64(value.examined)?;
    output.u64(value.accepted)?;
    output.u64(value.rejected)
}

fn read_logical_counts(input: &mut Reader<'_>) -> Result<RecoveryLogicalCounts> {
    Ok(RecoveryLogicalCounts {
        examined: input.u64()?,
        accepted: input.u64()?,
        rejected: input.u64()?,
    })
}

fn optional_scratch(output: &mut Writer<'_>, value: Option<&RecoveryScratchAttempt>) -> Result<()> {
    output.bool(value.is_some())?;
    if let Some(value) = value {
        output.bytes(&value.attempt_id)?;
        wire::identity(output, value.directory_identity)?;
        output.u16(value.creation_security.kind)?;
        output.bytes(&value.creation_security.commitment)?;
    }
    Ok(())
}

fn read_optional_scratch(input: &mut Reader<'_>) -> Result<Option<RecoveryScratchAttempt>> {
    input
        .bool()?
        .then(|| {
            Ok(RecoveryScratchAttempt {
                attempt_id: input.array()?,
                directory_identity: wire::read_identity(input)?,
                creation_security: CreationSecurity {
                    kind: input.u16()?,
                    commitment: input.array()?,
                },
            })
        })
        .transpose()
}

fn write_budget(output: &mut Writer<'_>, value: &RecoveryBudget) -> Result<()> {
    output.u64(value.max_heap_bytes)?;
    output.u64(value.max_output_pages)?;
    output.u32(value.max_open_files)?;
    output.u64(value.max_scratch_bytes)?;
    output.u32(value.max_scratch_files)?;
    output.optional_path(value.scratch_directory.as_deref())
}

fn read_budget(input: &mut Reader<'_>) -> Result<RecoveryBudget> {
    Ok(RecoveryBudget {
        max_heap_bytes: input.u64()?,
        max_output_pages: input.u64()?,
        max_open_files: input.u32()?,
        max_scratch_bytes: input.u64()?,
        max_scratch_files: input.u32()?,
        scratch_directory: input.optional_path()?,
    })
}

fn mode_tag(value: crate::recovery::WorkerMode) -> u8 {
    use crate::recovery::WorkerMode;
    match value {
        WorkerMode::Immutable => 1,
        WorkerMode::Offline => 2,
        WorkerMode::Live => 3,
    }
}

fn read_mode(value: u8) -> Result<crate::recovery::WorkerMode> {
    use crate::recovery::WorkerMode;
    match value {
        1 => Ok(WorkerMode::Immutable),
        2 => Ok(WorkerMode::Offline),
        3 => Ok(WorkerMode::Live),
        _ => Err(Error::Corrupt("worker recovery mode is invalid")),
    }
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
        _ => Err(Error::Corrupt("worker recovery fence is invalid")),
    }
}
