//! Shared fixed-record recovery storage and source-ID lookup.

use std::marker::PhantomData;

use crate::contract::{u32_le, u64_le};
use crate::error::{Error, Result};

use super::tables::{
    hash_u32, Layout, Region, Tables, ID_SLOT_SIZE, MEMBERSHIP_RECORD_SIZE, STRUCTURE_RECORD_SIZE,
};

const SLOT_SIZE: usize = ID_SLOT_SIZE as usize;
const OCCUPIED_OFFSET: usize = 0;
const CONFLICT_OFFSET: usize = 1;
const ID_OFFSET: usize = 4;
const RECORD_OFFSET: usize = 8;
const RECORD_BUFFER_SIZE: usize = if MEMBERSHIP_RECORD_SIZE > STRUCTURE_RECORD_SIZE {
    MEMBERSHIP_RECORD_SIZE as usize
} else {
    STRUCTURE_RECORD_SIZE as usize
};

pub(crate) enum Insert {
    First,
    Duplicate { first: u64, newly_conflicted: bool },
}

pub(crate) trait RecordCodec {
    type Record: Copy;

    const WIDTH: usize;
    const INVALID_RECORD: &'static str;
    const FULL: &'static str;

    fn regions(layout: Layout) -> (Region, Region);
    fn encode(record: Self::Record, output: &mut [u8]);
    fn decode(input: &[u8]) -> Result<Self::Record>;
    fn rejected(record: Self::Record) -> bool;
    fn reject(record: &mut Self::Record);
}

pub(crate) struct IdIndex<C> {
    table: RawIdTable,
    marker: PhantomData<C>,
}

impl<C: RecordCodec> IdIndex<C> {
    pub(crate) fn new(tables: &Tables) -> Self {
        let (records, ids) = C::regions(tables.layout());
        debug_assert!(C::WIDTH <= RECORD_BUFFER_SIZE);
        Self {
            table: RawIdTable::new(records, ids, C::INVALID_RECORD, C::FULL),
            marker: PhantomData,
        }
    }

    pub(crate) fn push(&mut self, tables: &mut Tables, record: C::Record) -> Result<()> {
        let mut bytes = [0; RECORD_BUFFER_SIZE];
        C::encode(record, &mut bytes[..C::WIDTH]);
        self.table.push(tables, &bytes[..C::WIDTH])
    }

    pub(crate) fn record(&self, tables: &Tables, index: u64) -> Result<C::Record> {
        let mut bytes = [0; RECORD_BUFFER_SIZE];
        self.table.read(tables, index, &mut bytes[..C::WIDTH])?;
        C::decode(&bytes[..C::WIDTH])
    }

    pub(crate) fn reject(&self, tables: &mut Tables, index: u64) -> Result<()> {
        let mut record = self.record(tables, index)?;
        C::reject(&mut record);
        let mut bytes = [0; RECORD_BUFFER_SIZE];
        C::encode(record, &mut bytes[..C::WIDTH]);
        self.table.write(tables, index, &bytes[..C::WIDTH])
    }

    pub(crate) fn insert_id(
        &self,
        tables: &mut Tables,
        id: u32,
        record_index: u64,
    ) -> Result<Insert> {
        self.table.insert_id(tables, id, record_index)
    }

    pub(crate) fn get(&self, tables: &Tables, id: u32) -> Result<Option<C::Record>> {
        let Some(index) = self.table.get(tables, id)? else {
            return Ok(None);
        };
        let record = self.record(tables, index)?;
        Ok((!C::rejected(record)).then_some(record))
    }

    pub(crate) const fn records_len(&self) -> u64 {
        self.table.records_len()
    }
}

struct RawIdTable {
    records: Region,
    ids: Region,
    records_len: u64,
    invalid_record: &'static str,
    full: &'static str,
}

impl RawIdTable {
    pub(crate) const fn new(
        records: Region,
        ids: Region,
        invalid_record: &'static str,
        full: &'static str,
    ) -> Self {
        Self {
            records,
            ids,
            records_len: 0,
            invalid_record,
            full,
        }
    }

    pub(crate) fn push(&mut self, tables: &mut Tables, record: &[u8]) -> Result<()> {
        if self.records_len == self.records.slots {
            return Err(Error::RecoveryCandidateChanged);
        }
        tables.write(self.records, self.records_len, record)?;
        self.records_len += 1;
        Ok(())
    }

    pub(crate) fn read(&self, tables: &Tables, index: u64, output: &mut [u8]) -> Result<()> {
        self.require_record(index)?;
        tables.read(self.records, index, output)
    }

    pub(crate) fn write(&self, tables: &mut Tables, index: u64, input: &[u8]) -> Result<()> {
        self.require_record(index)?;
        tables.write(self.records, index, input)
    }

    pub(crate) fn insert_id(
        &self,
        tables: &mut Tables,
        id: u32,
        record_index: u64,
    ) -> Result<Insert> {
        self.require_record(record_index)?;
        let (slot_index, mut slot) = match self.probe(tables, id, false)? {
            Some(found) => found,
            None => self
                .probe(tables, id, true)?
                .ok_or(Error::Corrupt(self.full))?,
        };
        if slot[OCCUPIED_OFFSET] == 0 {
            slot[OCCUPIED_OFFSET] = 1;
            slot[ID_OFFSET..RECORD_OFFSET].copy_from_slice(&id.to_le_bytes());
            slot[RECORD_OFFSET..SLOT_SIZE].copy_from_slice(&record_index.to_le_bytes());
            tables.write(self.ids, slot_index, &slot)?;
            return Ok(Insert::First);
        }
        let newly_conflicted = slot[CONFLICT_OFFSET] == 0;
        slot[CONFLICT_OFFSET] = 1;
        tables.write(self.ids, slot_index, &slot)?;
        Ok(Insert::Duplicate {
            first: u64_le(&slot, RECORD_OFFSET),
            newly_conflicted,
        })
    }

    pub(crate) fn get(&self, tables: &Tables, id: u32) -> Result<Option<u64>> {
        let Some((_, slot)) = self.probe(tables, id, false)? else {
            return Ok(None);
        };
        if slot[CONFLICT_OFFSET] != 0 {
            return Ok(None);
        }
        let index = u64_le(&slot, RECORD_OFFSET);
        self.require_record(index)?;
        Ok(Some(index))
    }

    pub(crate) const fn records_len(&self) -> u64 {
        self.records_len
    }

    fn require_record(&self, index: u64) -> Result<()> {
        if index >= self.records_len {
            return Err(Error::Corrupt(self.invalid_record));
        }
        Ok(())
    }

    fn probe(
        &self,
        tables: &Tables,
        id: u32,
        empty: bool,
    ) -> Result<Option<(u64, [u8; SLOT_SIZE])>> {
        if self.ids.slots == 0 {
            return Ok(None);
        }
        let mask = self.ids.slots - 1;
        let mut index = hash_u32(id) & mask;
        for _ in 0..self.ids.slots {
            let mut slot = [0; SLOT_SIZE];
            tables.read(self.ids, index, &mut slot)?;
            if slot[OCCUPIED_OFFSET] == 0 {
                return Ok(empty.then_some((index, slot)));
            }
            if u32_le(&slot, ID_OFFSET) == id {
                return Ok((!empty).then_some((index, slot)));
            }
            index = (index + 1) & mask;
        }
        Ok(None)
    }
}
