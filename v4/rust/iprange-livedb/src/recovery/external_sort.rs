//! Bounded two-file merge sort for recovery-readable direct ranges.

use std::fs::File;
use std::mem::size_of;

use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, PAGE_SIZE};
use crate::error::{Error, ErrorCode, Result};
use crate::key::IpKey;
use crate::range_tree::Record;
use crate::validation::ValidationReason;

use super::direct_output::DirectKey;
use super::page_set::PageSet;
use super::range_scan::{self, RangeEvents};
use super::scratch::{Scratch, ScratchCleanup, ScratchSlot};
use super::RecoveryBudget;

#[path = "external_sort/streams.rs"]
mod streams;
use streams::{merge_runs, read_run, record_order, write_run, RunReader};

pub(crate) struct ExternalSortFailure {
    pub(crate) cause: Error,
    pub(crate) cleanup: Option<ScratchCleanup>,
}

// Exact scratch ownership must remain in the error without a failure-path box.
#[allow(clippy::result_large_err)]
pub(crate) fn sort_and_emit<K: DirectKey>(
    file: &File,
    meta: MetaV4,
    budget: &RecoveryBudget,
    retained_heap_bytes: u64,
    readable_records: u64,
    cancellation: &CancellationToken,
    mut emit: impl FnMut(Record<K>) -> Result<()>,
) -> std::result::Result<ScratchCleanup, ExternalSortFailure> {
    let prepared = prepare::<K>(file, meta, budget, retained_heap_bytes);
    let (mut pages, mut records) = match prepared {
        Ok(prepared) => prepared,
        Err(cause) => return Err(no_attempt(cause)),
    };
    let directory = budget
        .scratch_directory
        .as_deref()
        .ok_or(Error::BudgetExceeded("recovery unordered ranges"));
    let mut scratch = match directory.and_then(|directory| {
        Scratch::start(
            directory,
            meta,
            budget.max_scratch_bytes,
            budget.max_scratch_files,
            budget.max_open_files,
        )
    }) {
        Ok(scratch) => scratch,
        Err(cause) => return Err(no_attempt(cause)),
    };
    let result = execute(
        file,
        meta,
        &mut pages,
        &mut records,
        readable_records,
        cancellation,
        &mut scratch,
        &mut emit,
    );
    finish(scratch, result)
}

fn prepare<K: IpKey>(
    file: &File,
    meta: MetaV4,
    budget: &RecoveryBudget,
    retained_heap_bytes: u64,
) -> Result<(PageSet, Vec<Record<K>>)> {
    let record_size = size_of::<Record<K>>() as u64;
    let available = budget
        .max_heap_bytes
        .checked_sub(retained_heap_bytes)
        .and_then(|value| value.checked_sub(record_size))
        .ok_or(Error::BudgetExceeded("recovery unordered range buffer"))?;
    let physical_pages = file.metadata()?.len() / PAGE_SIZE as u64;
    let pages = PageSet::new(available, meta.page_count.min(physical_pages))?;
    let record_bytes = budget
        .max_heap_bytes
        .checked_sub(retained_heap_bytes)
        .and_then(|value| value.checked_sub(pages.retained_bytes()))
        .ok_or(Error::BudgetExceeded("recovery unordered range buffer"))?;
    let capacity = usize::try_from(record_bytes / record_size)
        .unwrap_or(usize::MAX)
        .max(1);
    let mut records = Vec::new();
    records
        .try_reserve_exact(capacity)
        .map_err(|_| Error::BudgetExceeded("recovery unordered range buffer"))?;
    Ok((pages, records))
}

#[allow(clippy::too_many_arguments)]
fn execute<K: DirectKey>(
    file: &File,
    meta: MetaV4,
    pages: &mut PageSet,
    records: &mut Vec<Record<K>>,
    readable_records: u64,
    cancellation: &CancellationToken,
    scratch: &mut Scratch,
    emit: &mut impl FnMut(Record<K>) -> Result<()>,
) -> Result<()> {
    let first = scratch.create()?;
    let mut runs = Runs::new(first);
    let capacity = records.capacity();
    {
        let mut events = ScanEvents {
            records,
            capacity,
            scratch,
            runs: &mut runs,
            cancellation,
            seen: 0,
        };
        range_scan::scan(file, meta, pages, cancellation, &mut events)?;
        events.flush()?;
        if events.seen != readable_records {
            return Err(Error::RecoveryCandidateChanged);
        }
    }
    if readable_records == 0 {
        return Ok(());
    }
    let sorted = merge_all::<K>(scratch, runs, cancellation)?;
    emit_sorted(scratch, sorted, readable_records, cancellation, emit)
}

#[derive(Clone, Copy)]
struct Runs {
    slot: ScratchSlot,
    end: u64,
    count: u64,
}

impl Runs {
    fn new(slot: ScratchSlot) -> Self {
        Self {
            slot,
            end: 128,
            count: 0,
        }
    }

    fn append<K: DirectKey>(
        &mut self,
        scratch: &mut Scratch,
        records: &mut [Record<K>],
    ) -> Result<()> {
        if records.is_empty() {
            return Ok(());
        }
        records.sort_unstable_by(record_order);
        self.end = write_run(scratch, self.slot, self.end, records)?;
        self.count = self
            .count
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("recovery scratch runs"))?;
        Ok(())
    }
}

struct ScanEvents<'a, K> {
    records: &'a mut Vec<Record<K>>,
    capacity: usize,
    scratch: &'a mut Scratch,
    runs: &'a mut Runs,
    cancellation: &'a CancellationToken,
    seen: u64,
}

impl<K: DirectKey> ScanEvents<'_, K> {
    fn flush(&mut self) -> Result<()> {
        self.runs.append(self.scratch, self.records)?;
        self.records.clear();
        Ok(())
    }
}

impl<K: DirectKey> RangeEvents<K> for ScanEvents<'_, K> {
    fn page_accepted(&mut self) -> Result<()> {
        Ok(())
    }

    fn page_rejected(&mut self, _io_unreadable: bool) -> Result<()> {
        Ok(())
    }

    fn unknown(
        &mut self,
        _reason: ValidationReason,
        _page: Option<u32>,
        _unbounded: bool,
    ) -> Result<()> {
        Ok(())
    }

    fn range(&mut self, _page: u32, record: Option<Record<K>>) -> Result<()> {
        let Some(record) = record else {
            return Ok(());
        };
        self.cancellation.check()?;
        self.records.push(record);
        self.seen = self
            .seen
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("recovery readable ranges"))?;
        if self.records.len() == self.capacity {
            self.flush()?;
        }
        Ok(())
    }
}

fn merge_all<K: DirectKey>(
    scratch: &mut Scratch,
    mut input: Runs,
    cancellation: &CancellationToken,
) -> Result<ScratchSlot> {
    if input.count <= 1 {
        return Ok(input.slot);
    }
    let mut output = scratch.create()?;
    while input.count > 1 {
        scratch.reset(output)?;
        let merged = merge_pass::<K>(scratch, input, output, cancellation)?;
        output = input.slot;
        input = merged;
    }
    Ok(input.slot)
}

fn merge_pass<K: DirectKey>(
    scratch: &mut Scratch,
    input: Runs,
    output: ScratchSlot,
    cancellation: &CancellationToken,
) -> Result<Runs> {
    let mut source_at = 128;
    let mut destination_at = 128;
    let mut remaining_runs = input.count;
    let mut output_runs = 0;
    while remaining_runs != 0 {
        cancellation.check()?;
        let left = read_run::<K>(scratch, input.slot, source_at)?;
        source_at = left.end;
        let right = if remaining_runs > 1 {
            let run = read_run::<K>(scratch, input.slot, source_at)?;
            source_at = run.end;
            Some(run)
        } else {
            None
        };
        destination_at = merge_runs::<K>(
            scratch,
            input.slot,
            output,
            destination_at,
            left,
            right,
            cancellation,
        )?;
        remaining_runs -= if right.is_some() { 2 } else { 1 };
        output_runs += 1;
    }
    if source_at != scratch.length(input.slot) {
        return Err(Error::Corrupt("scratch run framing has trailing bytes"));
    }
    Ok(Runs {
        slot: output,
        end: destination_at,
        count: output_runs,
    })
}

fn emit_sorted<K: DirectKey>(
    scratch: &Scratch,
    slot: ScratchSlot,
    expected: u64,
    cancellation: &CancellationToken,
    emit: &mut impl FnMut(Record<K>) -> Result<()>,
) -> Result<()> {
    let run = read_run::<K>(scratch, slot, 128)?;
    if run.count != expected || run.end != scratch.length(slot) {
        return Err(Error::Corrupt("final recovery scratch run is incomplete"));
    }
    let mut reader = RunReader::<K>::new(slot, run);
    while let Some(record) = reader.next(scratch)? {
        cancellation.check()?;
        emit(record)?;
    }
    Ok(())
}

#[allow(clippy::result_large_err)]
fn finish(
    scratch: Scratch,
    result: Result<()>,
) -> std::result::Result<ScratchCleanup, ExternalSortFailure> {
    let cleanup = scratch.cleanup();
    match (result, cleanup.clean()) {
        (Ok(()), true) => Ok(cleanup),
        (Err(cause), true) => Err(ExternalSortFailure {
            cause,
            cleanup: Some(cleanup),
        }),
        (result, false) => {
            let cleanup_error = residue_error(&cleanup);
            let cause = Error::CleanupIncomplete {
                cause: Box::new(
                    result.unwrap_err_or(Error::Corrupt("recovery scratch cleanup is incomplete")),
                ),
                cleanup: Box::new(cleanup_error),
            };
            Err(ExternalSortFailure {
                cause,
                cleanup: Some(cleanup),
            })
        }
    }
}

fn residue_error(cleanup: &ScratchCleanup) -> Error {
    let problem = cleanup
        .residues
        .first()
        .expect("unclean scratch has one residue")
        .problem;
    match (problem.code, problem.os_code) {
        (ErrorCode::Io, Some(code)) => Error::Io(std::io::Error::from_raw_os_error(code)),
        _ => Error::Corrupt(problem.detail),
    }
}

fn no_attempt(cause: Error) -> ExternalSortFailure {
    ExternalSortFailure {
        cause,
        cleanup: None,
    }
}

trait ResultExt {
    fn unwrap_err_or(self, fallback: Error) -> Error;
}

impl ResultExt for Result<()> {
    fn unwrap_err_or(self, fallback: Error) -> Error {
        match self {
            Ok(()) => fallback,
            Err(cause) => cause,
        }
    }
}
