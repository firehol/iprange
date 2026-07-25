//! Fixed-record catalog reconciliation and lookup.

use crate::contract::{u32_le, u64_le};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::validation::{ValidationObject, ValidationReason};

use super::catalog::emit;
use super::report::{RecoverySink, Reporter};
use super::tables::{
    Layout, Region, Tables, CATALOG_INDEX_SLOT_SIZE, CATALOG_NAME_SLOT_SIZE, CATALOG_RECORD_SIZE,
};

const RECORD_SIZE: usize = CATALOG_RECORD_SIZE as usize;
const NAME_SLOT_SIZE: usize = CATALOG_NAME_SLOT_SIZE as usize;
const INDEX_SLOT_SIZE: usize = CATALOG_INDEX_SLOT_SIZE as usize;

pub(crate) struct Catalog {
    records: Region,
    names: Region,
    indexes: Region,
    records_len: u64,
}

pub(crate) struct Builder<'a> {
    tables: &'a mut Tables,
    catalog: Catalog,
}

impl<'a> Builder<'a> {
    pub(crate) fn new(tables: &'a mut Tables) -> Self {
        let Layout {
            catalog_records,
            catalog_names,
            catalog_indexes,
            ..
        } = tables.layout();
        Self {
            tables,
            catalog: Catalog {
                records: catalog_records,
                names: catalog_names,
                indexes: catalog_indexes,
                records_len: 0,
            },
        }
    }

    pub(crate) fn push<S: RecoverySink>(
        &mut self,
        entry: FeedEntry,
        reporter: &mut Reporter<'_, S>,
    ) -> Result<()> {
        if self.catalog.records_len == self.catalog.records.slots {
            return Err(Error::RecoveryCandidateChanged);
        }
        let record = encode_record(entry);
        let record_index = self.catalog.records_len;
        self.tables
            .write(self.catalog.records, record_index, &record)?;
        self.insert_name(entry, record_index, reporter)?;
        self.insert_index(entry, record_index, reporter)?;
        self.catalog.records_len += 1;
        Ok(())
    }

    pub(crate) fn finish<S: RecoverySink>(self, reporter: &mut Reporter<'_, S>) -> Result<Catalog> {
        let accepted = self.catalog.accepted_source_records(self.tables)?;
        reporter.catalog_rejected(self.catalog.records_len - accepted)?;
        reporter.catalog_accepted(accepted)?;
        Ok(self.catalog)
    }

    fn insert_name<S: RecoverySink>(
        &mut self,
        entry: FeedEntry,
        record_index: u64,
        reporter: &mut Reporter<'_, S>,
    ) -> Result<()> {
        let (slot_index, mut slot) = self.name_slot(&entry.name)?;
        if slot[0] == 0 {
            slot[0] = 1;
            slot[8..16].copy_from_slice(&record_index.to_le_bytes());
            slot[16..24].copy_from_slice(&1u64.to_le_bytes());
        } else {
            let occurrences = u64_le(&slot, 16)
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("recovery catalog occurrences"))?;
            slot[16..24].copy_from_slice(&occurrences.to_le_bytes());
            let first = self.catalog.record(self.tables, u64_le(&slot, 8))?;
            if first.index != entry.index && slot[1] == 0 {
                slot[1] = 1;
                emit(
                    reporter,
                    ValidationReason::CatalogInvalid,
                    ValidationObject::CatalogNameTree,
                    None,
                )?;
            }
        }
        self.tables.write(self.catalog.names, slot_index, &slot)
    }

    fn name_slot(&self, name: &FeedName) -> Result<(u64, [u8; NAME_SLOT_SIZE])> {
        match find_name(self.tables, &self.catalog, name)? {
            Some(found) => Ok(found),
            None => Ok((
                empty_name_slot(self.tables, &self.catalog, name)?,
                [0; NAME_SLOT_SIZE],
            )),
        }
    }

    fn insert_index<S: RecoverySink>(
        &mut self,
        entry: FeedEntry,
        record_index: u64,
        reporter: &mut Reporter<'_, S>,
    ) -> Result<()> {
        let (slot_index, mut slot) = match find_index(self.tables, &self.catalog, entry.index)? {
            Some(found) => found,
            None => (
                empty_index_slot(self.tables, &self.catalog, entry.index)?,
                [0; INDEX_SLOT_SIZE],
            ),
        };
        if slot[0] == 0 {
            slot[0] = 1;
            slot[4..8].copy_from_slice(&entry.index.to_le_bytes());
            slot[8..16].copy_from_slice(&record_index.to_le_bytes());
        } else {
            let first = self.catalog.record(self.tables, u64_le(&slot, 8))?;
            if first.name != entry.name && slot[1] == 0 {
                slot[1] = 1;
                emit(
                    reporter,
                    ValidationReason::CatalogInvalid,
                    ValidationObject::CatalogIndexTree,
                    None,
                )?;
            }
        }
        self.tables.write(self.catalog.indexes, slot_index, &slot)
    }
}

impl Catalog {
    pub(crate) fn for_each(
        &self,
        tables: &Tables,
        mut emit_entry: impl FnMut(FeedEntry) -> Result<()>,
    ) -> Result<()> {
        for record_index in 0..self.records_len {
            let entry = self.record(tables, record_index)?;
            let Some((_, slot)) = find_name(tables, self, &entry.name)? else {
                continue;
            };
            if u64_le(&slot, 8) == record_index {
                if let Some((entry, _)) = self.accepted_name_slot(tables, &slot)? {
                    emit_entry(entry)?;
                }
            }
        }
        Ok(())
    }

    pub(crate) fn contains(&self, tables: &Tables, index: u32) -> Result<bool> {
        let Some((_, slot)) = find_index(tables, self, index)? else {
            return Ok(false);
        };
        if slot[1] != 0 {
            return Ok(false);
        }
        let entry = self.record(tables, u64_le(&slot, 8))?;
        let Some((_, name_slot)) = find_name(tables, self, &entry.name)? else {
            return Ok(false);
        };
        Ok(self.accepted_name_slot(tables, &name_slot)?.is_some())
    }

    fn accepted_source_records(&self, tables: &Tables) -> Result<u64> {
        let mut accepted = 0u64;
        for slot_index in 0..self.names.slots {
            let mut slot = [0; NAME_SLOT_SIZE];
            tables.read(self.names, slot_index, &mut slot)?;
            if let Some((_, occurrences)) = self.accepted_name_slot(tables, &slot)? {
                accepted = accepted
                    .checked_add(occurrences)
                    .ok_or(Error::ArithmeticOverflow(
                        "accepted recovery catalog records",
                    ))?;
            }
        }
        Ok(accepted)
    }

    fn accepted_name_slot(
        &self,
        tables: &Tables,
        slot: &[u8; NAME_SLOT_SIZE],
    ) -> Result<Option<(FeedEntry, u64)>> {
        if slot[0] == 0 || slot[1] != 0 {
            return Ok(None);
        }
        let entry = self.record(tables, u64_le(slot, 8))?;
        let Some((_, index_slot)) = find_index(tables, self, entry.index)? else {
            return Ok(None);
        };
        if index_slot[1] != 0 {
            return Ok(None);
        }
        let indexed = self.record(tables, u64_le(&index_slot, 8))?;
        Ok((indexed == entry).then_some((entry, u64_le(slot, 16))))
    }

    fn record(&self, tables: &Tables, index: u64) -> Result<FeedEntry> {
        if index >= self.records_len {
            return Err(Error::Corrupt("recovery catalog record index is invalid"));
        }
        let mut bytes = [0; RECORD_SIZE];
        tables.read(self.records, index, &mut bytes)?;
        decode_record(&bytes)
    }
}

fn find_name(
    tables: &Tables,
    catalog: &Catalog,
    name: &FeedName,
) -> Result<Option<(u64, [u8; NAME_SLOT_SIZE])>> {
    probe_name(tables, catalog, name, false)
}

fn empty_name_slot(tables: &Tables, catalog: &Catalog, name: &FeedName) -> Result<u64> {
    probe_name(tables, catalog, name, true)?
        .map(|(index, _)| index)
        .ok_or(Error::Corrupt("recovery catalog name table is full"))
}

fn probe_name(
    tables: &Tables,
    catalog: &Catalog,
    name: &FeedName,
    empty: bool,
) -> Result<Option<(u64, [u8; NAME_SLOT_SIZE])>> {
    if catalog.names.slots == 0 {
        return Ok(None);
    }
    let mask = catalog.names.slots - 1;
    let mut index = hash_bytes(name.as_bytes()) & mask;
    for _ in 0..catalog.names.slots {
        let mut slot = [0; NAME_SLOT_SIZE];
        tables.read(catalog.names, index, &mut slot)?;
        if slot[0] == 0 {
            return Ok(empty.then_some((index, slot)));
        }
        let entry = catalog.record(tables, u64_le(&slot, 8))?;
        if entry.name == *name {
            return Ok((!empty).then_some((index, slot)));
        }
        index = (index + 1) & mask;
    }
    Ok(None)
}

fn find_index(
    tables: &Tables,
    catalog: &Catalog,
    value: u32,
) -> Result<Option<(u64, [u8; INDEX_SLOT_SIZE])>> {
    probe_index(tables, catalog, value, false)
}

fn empty_index_slot(tables: &Tables, catalog: &Catalog, value: u32) -> Result<u64> {
    probe_index(tables, catalog, value, true)?
        .map(|(index, _)| index)
        .ok_or(Error::Corrupt("recovery catalog index table is full"))
}

fn probe_index(
    tables: &Tables,
    catalog: &Catalog,
    value: u32,
    empty: bool,
) -> Result<Option<(u64, [u8; INDEX_SLOT_SIZE])>> {
    if catalog.indexes.slots == 0 {
        return Ok(None);
    }
    let mask = catalog.indexes.slots - 1;
    let mut index = hash_u32(value) & mask;
    for _ in 0..catalog.indexes.slots {
        let mut slot = [0; INDEX_SLOT_SIZE];
        tables.read(catalog.indexes, index, &mut slot)?;
        if slot[0] == 0 {
            return Ok(empty.then_some((index, slot)));
        }
        if u32_le(&slot, 4) == value {
            return Ok((!empty).then_some((index, slot)));
        }
        index = (index + 1) & mask;
    }
    Ok(None)
}

fn encode_record(entry: FeedEntry) -> [u8; RECORD_SIZE] {
    let mut output = [0; RECORD_SIZE];
    output[0] = entry.name.as_bytes().len() as u8;
    output[4..8].copy_from_slice(&entry.index.to_le_bytes());
    output[8..8 + entry.name.as_bytes().len()].copy_from_slice(entry.name.as_bytes());
    output
}

fn decode_record(bytes: &[u8; RECORD_SIZE]) -> Result<FeedEntry> {
    let length = usize::from(bytes[0]);
    let name = FeedName::from_stored(
        bytes
            .get(8..8 + length)
            .ok_or(Error::Corrupt("recovery catalog name length is invalid"))?,
    )
    .ok_or(Error::Corrupt("recovery catalog name is invalid"))?;
    Ok(FeedEntry {
        name,
        index: u32_le(bytes, 4),
    })
}

fn hash_bytes(bytes: &[u8]) -> u64 {
    let mut hash = 0xcbf2_9ce4_8422_2325u64;
    for &byte in bytes {
        hash ^= u64::from(byte);
        hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
    }
    hash
}

fn hash_u32(value: u32) -> u64 {
    let mut value = u64::from(value);
    value ^= value >> 30;
    value = value.wrapping_mul(0xbf58_476d_1ce4_e5b9);
    value ^= value >> 27;
    value = value.wrapping_mul(0x94d0_49bb_1331_11eb);
    value ^ (value >> 31)
}
