use std::ffi::{OsStr, OsString};
use std::path::{Path, PathBuf};

use crate::cardinality::Cardinality129;
use crate::error::{Error, ErrorCode, Result};
use crate::recovery::{
    RecoveryCandidate, RecoveryCandidateInspection, RecoveryCandidateLabel, RecoveryInspectionMode,
};
use crate::validation::{
    LocalFileIdentity, ValidationBudget, ValidationObject, ValidationProgress, ValidationReason,
};

use super::control::Control;

pub(super) struct Writer<'a> {
    control: &'a Control,
    at: usize,
    buffer: Buffer,
}

pub(super) struct Reader<'a> {
    control: &'a Control,
    at: usize,
    len: usize,
    buffer: Buffer,
}

#[derive(Clone, Copy)]
enum Buffer {
    Payload,
    CallbackCheckpoint,
}

impl<'a> Writer<'a> {
    pub(super) fn new(control: &'a Control) -> Self {
        Self {
            control,
            at: 0,
            buffer: Buffer::Payload,
        }
    }

    pub(super) fn new_callback_checkpoint(control: &'a Control) -> Self {
        Self {
            control,
            at: 0,
            buffer: Buffer::CallbackCheckpoint,
        }
    }

    pub(super) fn finish(self) -> Result<()> {
        match self.buffer {
            Buffer::Payload => self.control.set_payload_len(self.at),
            Buffer::CallbackCheckpoint => self.control.set_callback_payload_len(self.at),
        }
    }

    pub(super) fn byte(&mut self, value: u8) -> Result<()> {
        self.bytes(&[value])
    }

    pub(super) fn bool(&mut self, value: bool) -> Result<()> {
        self.byte(u8::from(value))
    }

    pub(super) fn u16(&mut self, value: u16) -> Result<()> {
        self.bytes(&value.to_le_bytes())
    }

    pub(super) fn u32(&mut self, value: u32) -> Result<()> {
        self.bytes(&value.to_le_bytes())
    }

    pub(super) fn i32(&mut self, value: i32) -> Result<()> {
        self.bytes(&value.to_le_bytes())
    }

    pub(super) fn u64(&mut self, value: u64) -> Result<()> {
        self.bytes(&value.to_le_bytes())
    }

    pub(super) fn bytes(&mut self, value: &[u8]) -> Result<()> {
        match self.buffer {
            Buffer::Payload => self.control.write_payload(self.at, value)?,
            Buffer::CallbackCheckpoint => self.control.write_callback_payload(self.at, value)?,
        }
        self.at = self
            .at
            .checked_add(value.len())
            .ok_or(Error::ArithmeticOverflow("worker payload offset"))?;
        Ok(())
    }

    pub(super) fn path(&mut self, path: &Path) -> Result<()> {
        os_string(self, path.as_os_str())
    }

    pub(super) fn optional_path(&mut self, path: Option<&Path>) -> Result<()> {
        self.bool(path.is_some())?;
        if let Some(path) = path {
            self.path(path)?;
        }
        Ok(())
    }

    pub(super) fn sized_bytes(&mut self, value: &[u8]) -> Result<()> {
        self.u32(
            u32::try_from(value.len()).map_err(|_| Error::BudgetExceeded("worker byte string"))?,
        )?;
        self.bytes(value)
    }
}

impl<'a> Reader<'a> {
    pub(super) fn new(control: &'a Control) -> Result<Self> {
        Ok(Self {
            control,
            at: 0,
            len: control.payload_len()?,
            buffer: Buffer::Payload,
        })
    }

    pub(super) fn new_callback_checkpoint(control: &'a Control) -> Result<Self> {
        Ok(Self {
            control,
            at: 0,
            len: control.callback_payload_len()?,
            buffer: Buffer::CallbackCheckpoint,
        })
    }

    pub(super) fn finish(self) -> Result<()> {
        if self.at == self.len {
            Ok(())
        } else {
            Err(Error::Corrupt("worker payload has trailing bytes"))
        }
    }

    pub(super) fn byte(&mut self) -> Result<u8> {
        let value = match self.buffer {
            Buffer::Payload => self.control.payload_byte(self.at),
            Buffer::CallbackCheckpoint => self.control.callback_payload_byte(self.at),
        }
        .ok_or(Error::Corrupt("worker payload is truncated"))?;
        self.at += 1;
        Ok(value)
    }

    pub(super) fn bool(&mut self) -> Result<bool> {
        match self.byte()? {
            0 => Ok(false),
            1 => Ok(true),
            _ => Err(Error::Corrupt("worker boolean is invalid")),
        }
    }

    pub(super) fn u16(&mut self) -> Result<u16> {
        Ok(u16::from_le_bytes(self.array()?))
    }

    pub(super) fn u32(&mut self) -> Result<u32> {
        Ok(u32::from_le_bytes(self.array()?))
    }

    pub(super) fn i32(&mut self) -> Result<i32> {
        Ok(i32::from_le_bytes(self.array()?))
    }

    pub(super) fn u64(&mut self) -> Result<u64> {
        Ok(u64::from_le_bytes(self.array()?))
    }

    pub(super) fn array<const N: usize>(&mut self) -> Result<[u8; N]> {
        let end = self
            .at
            .checked_add(N)
            .ok_or(Error::ArithmeticOverflow("worker payload offset"))?;
        if end > self.len {
            return Err(Error::Corrupt("worker payload is truncated"));
        }
        let mut value = [0; N];
        for (index, byte) in value.iter_mut().enumerate() {
            *byte = match self.buffer {
                Buffer::Payload => self.control.payload_byte(self.at + index),
                Buffer::CallbackCheckpoint => self.control.callback_payload_byte(self.at + index),
            }
            .ok_or(Error::Corrupt("worker payload is truncated"))?;
        }
        self.at = end;
        Ok(value)
    }

    pub(super) fn path(&mut self) -> Result<PathBuf> {
        Ok(PathBuf::from(read_os_string(self)?))
    }

    pub(super) fn optional_path(&mut self) -> Result<Option<PathBuf>> {
        self.bool()?.then(|| self.path()).transpose()
    }

    pub(super) fn boxed_bytes(&mut self) -> Result<Box<[u8]>> {
        let len = self.u32()? as usize;
        let remaining = self
            .len
            .checked_sub(self.at)
            .ok_or(Error::Corrupt("worker payload position is invalid"))?;
        if len > remaining {
            return Err(Error::Corrupt("worker byte string is truncated"));
        }
        let mut value = Vec::new();
        value
            .try_reserve_exact(len)
            .map_err(|_| Error::BudgetExceeded("worker byte string"))?;
        for _ in 0..len {
            value.push(self.byte()?);
        }
        Ok(value.into_boxed_slice())
    }
}

pub(super) fn write_inspection_request(
    control: &Control,
    path: &Path,
    mode: RecoveryInspectionMode,
    budget: &ValidationBudget,
    unreadable_pages: &[u32],
) -> Result<()> {
    let mut output = Writer::new(control);
    output.path(path)?;
    output.byte(match mode {
        RecoveryInspectionMode::Immutable => 1,
        RecoveryInspectionMode::Live => 2,
        RecoveryInspectionMode::Offline => 3,
    })?;
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
    output.finish()
}

pub(super) fn read_inspection_request(
    control: &Control,
) -> Result<(PathBuf, RecoveryInspectionMode, ValidationBudget, Vec<u32>)> {
    let mut input = Reader::new(control)?;
    let path = input.path()?;
    let mode = match input.byte()? {
        1 => RecoveryInspectionMode::Immutable,
        2 => RecoveryInspectionMode::Live,
        3 => RecoveryInspectionMode::Offline,
        _ => return Err(Error::Corrupt("worker inspection mode is invalid")),
    };
    let budget = ValidationBudget {
        max_heap_bytes: input.u64()?,
        max_open_files: input.u32()?,
        max_scratch_bytes: input.u64()?,
        max_scratch_files: input.u32()?,
        scratch_directory: input.optional_path()?,
    };
    let unreadable_count = input.u32()? as usize;
    let mut unreadable_pages = Vec::new();
    unreadable_pages
        .try_reserve_exact(unreadable_count)
        .map_err(|_| Error::BudgetExceeded("unreadable source-page list"))?;
    for _ in 0..unreadable_count {
        unreadable_pages.push(input.u32()?);
    }
    input.finish()?;
    Ok((path, mode, budget, unreadable_pages))
}

pub(super) fn write_inspection_result(
    control: &Control,
    result: &Result<RecoveryCandidateInspection>,
) -> Result<()> {
    let mut output = Writer::new(control);
    match result {
        Ok(inspection) => {
            output.byte(0)?;
            identity(&mut output, inspection.source_identity)?;
            progress(&mut output, &inspection.progress)?;
            output.byte(inspection.candidate_count() as u8)?;
            for candidate in inspection.candidates() {
                recovery_candidate(&mut output, candidate)?;
            }
        }
        Err(error) => {
            output.byte(1)?;
            encode_error(&mut output, error)?;
        }
    }
    output.finish()
}

pub(super) fn read_inspection_result(control: &Control) -> Result<RecoveryCandidateInspection> {
    let mut input = Reader::new(control)?;
    if input.byte()? == 1 {
        let error = read_error(&mut input)?;
        input.finish()?;
        return Err(error);
    }
    let source_identity = read_identity(&mut input)?;
    let progress = read_progress(&mut input)?;
    let count = input.byte()?;
    if count > 2 {
        return Err(Error::Corrupt("worker recovery candidate count is invalid"));
    }
    let mut candidates = [None, None];
    for slot in candidates.iter_mut().take(count as usize) {
        *slot = Some(read_recovery_candidate(&mut input)?);
    }
    input.finish()?;
    Ok(RecoveryCandidateInspection::new(
        source_identity,
        progress,
        candidates,
    ))
}

pub(super) fn identity(output: &mut Writer<'_>, value: LocalFileIdentity) -> Result<()> {
    output.u16(value.kind)?;
    output.bytes(&value.bytes)
}

pub(super) fn read_identity(input: &mut Reader<'_>) -> Result<LocalFileIdentity> {
    Ok(LocalFileIdentity {
        kind: input.u16()?,
        bytes: input.array()?,
    })
}

pub(super) fn progress(output: &mut Writer<'_>, value: &ValidationProgress) -> Result<()> {
    output.u64(value.checked_unique_pages)?;
    output.u64(value.finding_count)?;
    output.u64(value.untraversable_subgraphs)?;
    cardinality(output, value.bounded_possible_span_addresses)?;
    output.bool(value.has_unbounded_unknown)?;
    let (reasons, objects) = value.wire_counts();
    for value in reasons.iter().chain(objects) {
        output.u64(*value)?;
    }
    Ok(())
}

pub(super) fn read_progress(input: &mut Reader<'_>) -> Result<ValidationProgress> {
    let checked_unique_pages = input.u64()?;
    let finding_count = input.u64()?;
    let untraversable_subgraphs = input.u64()?;
    let bounded_possible_span_addresses = read_cardinality(input)?;
    let has_unbounded_unknown = input.bool()?;
    let mut reason_counts = [0; ValidationReason::COUNT];
    let mut object_counts = [0; ValidationObject::COUNT];
    for value in reason_counts.iter_mut().chain(object_counts.iter_mut()) {
        *value = input.u64()?;
    }
    Ok(ValidationProgress::from_wire(
        checked_unique_pages,
        finding_count,
        untraversable_subgraphs,
        bounded_possible_span_addresses,
        has_unbounded_unknown,
        reason_counts,
        object_counts,
    ))
}

pub(super) fn cardinality(output: &mut Writer<'_>, value: Cardinality129) -> Result<()> {
    output.byte(value.bit128())?;
    output.u64(value.hi())?;
    output.u64(value.lo())
}

pub(super) fn read_cardinality(input: &mut Reader<'_>) -> Result<Cardinality129> {
    Cardinality129::try_new(input.byte()?, input.u64()?, input.u64()?)
        .ok_or(Error::Corrupt("worker cardinality is invalid"))
}

pub(super) fn recovery_candidate(output: &mut Writer<'_>, value: &RecoveryCandidate) -> Result<()> {
    output.byte(match value.label {
        RecoveryCandidateLabel::Newest => 1,
        RecoveryCandidateLabel::Previous => 2,
        RecoveryCandidateLabel::UnorderedMeta0 => 3,
        RecoveryCandidateLabel::UnorderedMeta1 => 4,
    })?;
    output.byte(value.meta_page)?;
    identity(output, value.source_identity)?;
    output.bytes(&value.database_id)?;
    output.u64(value.transaction_id)?;
    output.bytes(&value.commit_nonce)
}

pub(super) fn read_recovery_candidate(input: &mut Reader<'_>) -> Result<RecoveryCandidate> {
    let label = match input.byte()? {
        1 => RecoveryCandidateLabel::Newest,
        2 => RecoveryCandidateLabel::Previous,
        3 => RecoveryCandidateLabel::UnorderedMeta0,
        4 => RecoveryCandidateLabel::UnorderedMeta1,
        _ => return Err(Error::Corrupt("worker recovery label is invalid")),
    };
    Ok(RecoveryCandidate {
        label,
        meta_page: input.byte()?,
        source_identity: read_identity(input)?,
        database_id: input.array()?,
        transaction_id: input.u64()?,
        commit_nonce: input.array()?,
    })
}

pub(super) fn encode_error(output: &mut Writer<'_>, value: &Error) -> Result<()> {
    output.u32(value.code() as u32)?;
    let os_code = match value {
        Error::Io(error) => error.raw_os_error(),
        Error::WorkerOperation { os_code, .. } => *os_code,
        _ => None,
    };
    output.bool(os_code.is_some())?;
    if let Some(os_code) = os_code {
        output.i32(os_code)?;
    }
    Ok(())
}

pub(super) fn read_error(input: &mut Reader<'_>) -> Result<Error> {
    let code =
        ErrorCode::from_wire(input.u32()?).ok_or(Error::Corrupt("worker error code is invalid"))?;
    let os_code = input.bool()?.then(|| input.i32()).transpose()?;
    Ok(match (code, os_code) {
        (ErrorCode::Io, Some(os_code)) => Error::Io(std::io::Error::from_raw_os_error(os_code)),
        (ErrorCode::NameInvalid, _) => Error::NameInvalid,
        (ErrorCode::NameExists, _) => Error::NameExists,
        (ErrorCode::NameNotFound, _) => Error::NameNotFound,
        (ErrorCode::StaleReference, _) => Error::StaleReference,
        (ErrorCode::ForeignReference, _) => Error::ForeignReference,
        (ErrorCode::PageSpaceExhausted, _) => Error::PageSpaceExhausted,
        (ErrorCode::FeedIndexExhausted, _) => Error::FeedIndexExhausted,
        (ErrorCode::MembershipIdExhausted, _) => Error::MembershipIdExhausted,
        (ErrorCode::Cancelled, _) => Error::Cancelled,
        (ErrorCode::WriterBusy, _) => Error::WriterBusy,
        (ErrorCode::ReaderCapacityExhausted, _) => Error::ReaderCapacityExhausted,
        (ErrorCode::NoPendingTransaction, _) => Error::NoPendingTransaction,
        (ErrorCode::StoppedBySink, _) => Error::StoppedBySink,
        (ErrorCode::LiveRecoveryCurrentGenerationUnprovable, _) => {
            Error::LiveRecoveryCurrentGenerationUnprovable
        }
        (ErrorCode::LiveRecoveryCurrentGenerationUnreadable, _) => {
            Error::LiveRecoveryCurrentGenerationUnreadable
        }
        (ErrorCode::RecoveryCandidateChanged, _) => Error::RecoveryCandidateChanged,
        (ErrorCode::DirectoryIdentityMismatch, _) => Error::DirectoryIdentityMismatch,
        (ErrorCode::ForkedHandle, _) => Error::ForkedHandle,
        (code, os_code) => Error::WorkerOperation { code, os_code },
    })
}

pub(super) fn write_worker_error(control: &Control, error: &Error) -> Result<()> {
    let mut output = Writer::new(control);
    encode_error(&mut output, error)?;
    output.finish()
}

pub(super) fn read_worker_error(control: &Control) -> Result<Error> {
    let mut input = Reader::new(control)?;
    let error = read_error(&mut input)?;
    input.finish()?;
    Ok(error)
}

#[cfg(unix)]
fn os_string(output: &mut Writer<'_>, value: &OsStr) -> Result<()> {
    use std::os::unix::ffi::OsStrExt;

    let bytes = value.as_bytes();
    let len = u32::try_from(bytes.len()).map_err(|_| Error::InvalidArgument("path is too long"))?;
    output.u32(len)?;
    output.bytes(bytes)
}

#[cfg(unix)]
fn read_os_string(input: &mut Reader<'_>) -> Result<OsString> {
    use std::os::unix::ffi::OsStringExt;

    let len = input.u32()? as usize;
    let mut bytes = Vec::new();
    bytes
        .try_reserve_exact(len)
        .map_err(|_| Error::BudgetExceeded("worker path"))?;
    for _ in 0..len {
        bytes.push(input.byte()?);
    }
    Ok(OsString::from_vec(bytes))
}

#[cfg(windows)]
fn os_string(output: &mut Writer<'_>, value: &OsStr) -> Result<()> {
    use std::os::windows::ffi::OsStrExt;

    let wide: Vec<u16> = value.encode_wide().collect();
    let len = u32::try_from(wide.len()).map_err(|_| Error::InvalidArgument("path is too long"))?;
    output.u32(len)?;
    for value in wide {
        output.u16(value)?;
    }
    Ok(())
}

#[cfg(windows)]
fn read_os_string(input: &mut Reader<'_>) -> Result<OsString> {
    use std::os::windows::ffi::OsStringExt;

    let len = input.u32()? as usize;
    let mut wide = Vec::new();
    wide.try_reserve_exact(len)
        .map_err(|_| Error::BudgetExceeded("worker path"))?;
    for _ in 0..len {
        wide.push(input.u16()?);
    }
    Ok(OsString::from_wide(&wide))
}
