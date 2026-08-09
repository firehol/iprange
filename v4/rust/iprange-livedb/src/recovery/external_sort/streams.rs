//! Fixed-width buffered run framing, reading, writing, and merging.

use std::marker::PhantomData;

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::range_tree::Record;

use super::super::direct_output::DirectKey;
use super::super::scratch::{Scratch, ScratchSlot};

const RUN_HEADER_SIZE: u64 = 16;
const RUN_MAGIC: [u8; 8] = *b"IPR4RUN1";
const RUN_MAGIC_OFFSET: usize = 0;
const RUN_COUNT_OFFSET: usize = RUN_MAGIC.len();
const BUFFER_SIZE: usize = 4096;

pub(super) fn write_run<K: DirectKey>(
    scratch: &mut Scratch,
    slot: ScratchSlot,
    at: u64,
    records: &[Record<K>],
) -> Result<u64> {
    let count = records.len() as u64;
    write_header(scratch, slot, at, count)?;
    let mut writer = RecordWriter::<K>::new(slot, at + RUN_HEADER_SIZE);
    for &record in records {
        writer.push(scratch, record)?;
    }
    writer.finish(scratch)
}

#[allow(clippy::too_many_arguments)]
pub(super) fn merge_runs<K: DirectKey>(
    scratch: &mut Scratch,
    input: ScratchSlot,
    output: ScratchSlot,
    at: u64,
    left: Run,
    right: Option<Run>,
    cancellation: &CancellationToken,
) -> Result<u64> {
    let count = right.map_or(Ok(left.count), |right| {
        left.count
            .checked_add(right.count)
            .ok_or(Error::ArithmeticOverflow("merged recovery scratch run"))
    })?;
    write_header(scratch, output, at, count)?;
    let mut writer = RecordWriter::<K>::new(output, at + RUN_HEADER_SIZE);
    let mut left = RunReader::<K>::new(input, left);
    let mut left_record = left.next(scratch)?;
    if let Some(right) = right {
        merge_pair(
            scratch,
            input,
            right,
            cancellation,
            &mut writer,
            &mut left,
            &mut left_record,
        )?;
    } else {
        copy_left(scratch, cancellation, &mut writer, &mut left, left_record)?;
    }
    writer.finish(scratch)
}

#[allow(clippy::too_many_arguments)]
fn merge_pair<K: DirectKey>(
    scratch: &mut Scratch,
    input: ScratchSlot,
    right: Run,
    cancellation: &CancellationToken,
    writer: &mut RecordWriter<K>,
    left: &mut RunReader<K>,
    left_record: &mut Option<Record<K>>,
) -> Result<()> {
    let mut right = RunReader::<K>::new(input, right);
    let mut right_record = right.next(scratch)?;
    while let Some(take_left) = choose(left_record.as_ref(), right_record.as_ref()) {
        cancellation.check()?;
        if take_left {
            writer.push(scratch, left_record.take().expect("left record"))?;
            *left_record = left.next(scratch)?;
        } else {
            writer.push(scratch, right_record.take().expect("right record"))?;
            right_record = right.next(scratch)?;
        }
    }
    Ok(())
}

fn choose<K: IpKey>(left: Option<&Record<K>>, right: Option<&Record<K>>) -> Option<bool> {
    match (left, right) {
        (Some(left), Some(right)) => Some(record_order(left, right).is_le()),
        (Some(_), None) => Some(true),
        (None, Some(_)) => Some(false),
        (None, None) => None,
    }
}

fn copy_left<K: DirectKey>(
    scratch: &mut Scratch,
    cancellation: &CancellationToken,
    writer: &mut RecordWriter<K>,
    left: &mut RunReader<K>,
    mut record: Option<Record<K>>,
) -> Result<()> {
    while let Some(current) = record {
        cancellation.check()?;
        writer.push(scratch, current)?;
        record = left.next(scratch)?;
    }
    Ok(())
}

fn write_header(scratch: &mut Scratch, slot: ScratchSlot, at: u64, count: u64) -> Result<()> {
    let mut header = [0; RUN_HEADER_SIZE as usize];
    header[RUN_MAGIC_OFFSET..RUN_COUNT_OFFSET].copy_from_slice(&RUN_MAGIC);
    header[RUN_COUNT_OFFSET..].copy_from_slice(&count.to_le_bytes());
    scratch.write(slot, at, &header)
}

#[derive(Clone, Copy)]
pub(super) struct Run {
    pub(super) records_at: u64,
    pub(super) end: u64,
    pub(super) count: u64,
}

pub(super) fn read_run<K: DirectKey>(scratch: &Scratch, slot: ScratchSlot, at: u64) -> Result<Run> {
    let mut header = [0; RUN_HEADER_SIZE as usize];
    scratch.read(slot, at, &mut header)?;
    if header[RUN_MAGIC_OFFSET..RUN_COUNT_OFFSET] != RUN_MAGIC {
        return Err(Error::Corrupt("scratch run header is malformed"));
    }
    let count = u64::from_le_bytes(
        header[RUN_COUNT_OFFSET..]
            .try_into()
            .expect("fixed run count"),
    );
    if count == 0 {
        return Err(Error::Corrupt("scratch run is empty"));
    }
    let records_at = at
        .checked_add(RUN_HEADER_SIZE)
        .ok_or(Error::ArithmeticOverflow("recovery scratch run"))?;
    let end = count
        .checked_mul(K::SCRATCH_RECORD_SIZE as u64)
        .and_then(|bytes| records_at.checked_add(bytes))
        .ok_or(Error::ArithmeticOverflow("recovery scratch run"))?;
    if end > scratch.length(slot) {
        return Err(Error::Corrupt("scratch run exceeds its file"));
    }
    Ok(Run {
        records_at,
        end,
        count,
    })
}

pub(super) struct RunReader<K> {
    slot: ScratchSlot,
    next_at: u64,
    remaining: u64,
    buffer: [u8; BUFFER_SIZE],
    buffered: usize,
    index: usize,
    marker: PhantomData<K>,
}

impl<K: DirectKey> RunReader<K> {
    pub(super) fn new(slot: ScratchSlot, run: Run) -> Self {
        Self {
            slot,
            next_at: run.records_at,
            remaining: run.count,
            buffer: [0; BUFFER_SIZE],
            buffered: 0,
            index: 0,
            marker: PhantomData,
        }
    }

    pub(super) fn next(&mut self, scratch: &Scratch) -> Result<Option<Record<K>>> {
        if self.remaining == 0 {
            return Ok(None);
        }
        if self.index == self.buffered {
            self.fill(scratch)?;
        }
        let start = self.index * K::SCRATCH_RECORD_SIZE;
        self.index += 1;
        self.remaining -= 1;
        Ok(Some(K::decode_scratch(
            &self.buffer[start..start + K::SCRATCH_RECORD_SIZE],
        )))
    }

    fn fill(&mut self, scratch: &Scratch) -> Result<()> {
        let capacity = BUFFER_SIZE / K::SCRATCH_RECORD_SIZE;
        let count = usize::try_from(self.remaining.min(capacity as u64)).expect("bounded batch");
        let bytes = count * K::SCRATCH_RECORD_SIZE;
        scratch.read(self.slot, self.next_at, &mut self.buffer[..bytes])?;
        self.next_at = self
            .next_at
            .checked_add(bytes as u64)
            .ok_or(Error::ArithmeticOverflow("recovery scratch read"))?;
        self.buffered = count;
        self.index = 0;
        Ok(())
    }
}

struct RecordWriter<K> {
    slot: ScratchSlot,
    next_at: u64,
    buffer: [u8; BUFFER_SIZE],
    records: usize,
    marker: PhantomData<K>,
}

impl<K: DirectKey> RecordWriter<K> {
    fn new(slot: ScratchSlot, next_at: u64) -> Self {
        Self {
            slot,
            next_at,
            buffer: [0; BUFFER_SIZE],
            records: 0,
            marker: PhantomData,
        }
    }

    fn push(&mut self, scratch: &mut Scratch, record: Record<K>) -> Result<()> {
        if (self.records + 1) * K::SCRATCH_RECORD_SIZE > BUFFER_SIZE {
            self.flush(scratch)?;
        }
        let start = self.records * K::SCRATCH_RECORD_SIZE;
        K::encode_scratch(
            record,
            &mut self.buffer[start..start + K::SCRATCH_RECORD_SIZE],
        );
        self.records += 1;
        Ok(())
    }

    fn finish(mut self, scratch: &mut Scratch) -> Result<u64> {
        self.flush(scratch)?;
        Ok(self.next_at)
    }

    fn flush(&mut self, scratch: &mut Scratch) -> Result<()> {
        let bytes = self.records * K::SCRATCH_RECORD_SIZE;
        if bytes == 0 {
            return Ok(());
        }
        scratch.write(self.slot, self.next_at, &self.buffer[..bytes])?;
        self.next_at = self
            .next_at
            .checked_add(bytes as u64)
            .ok_or(Error::ArithmeticOverflow("recovery scratch write"))?;
        self.records = 0;
        Ok(())
    }
}

pub(super) fn record_order<K: IpKey>(left: &Record<K>, right: &Record<K>) -> std::cmp::Ordering {
    (left.from, left.to, left.value).cmp(&(right.from, right.to, right.value))
}
