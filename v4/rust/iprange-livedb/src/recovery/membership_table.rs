//! Fixed-record membership locator storage and ID lookup.

use crate::contract::{u16_le, u32_le, u64_le};
use crate::error::{Error, Result};
use crate::membership_dictionary::codec::Storage;

use super::membership_index::Locator;
use super::tables::{
    hash_u32, Layout, Region, Tables, MEMBERSHIP_ID_SLOT_SIZE, MEMBERSHIP_RECORD_SIZE,
};

const RECORD_SIZE: usize = MEMBERSHIP_RECORD_SIZE as usize;
const ID_SLOT_SIZE: usize = MEMBERSHIP_ID_SLOT_SIZE as usize;
const RECORD_PRESENT_OFFSET: usize = 0;
const RECORD_STORAGE_OFFSET: usize = 1;
const RECORD_LEAF_INDEX_OFFSET: usize = 2;
const RECORD_ID_OFFSET: usize = 4;
const RECORD_WORD_COUNT_OFFSET: usize = 8;
const RECORD_LEAF_PAGE_OFFSET: usize = 12;
const RECORD_BLOB_ROOT_OFFSET: usize = 16;
const RECORD_REJECTED_OFFSET: usize = 20;
const RECORD_DIGEST_OFFSET: usize = 24;
const RECORD_DIGEST_END: usize = 56;
const SLOT_OCCUPIED_OFFSET: usize = 0;
const SLOT_CONFLICT_OFFSET: usize = 1;
const SLOT_ID_OFFSET: usize = 4;
const SLOT_RECORD_OFFSET: usize = 8;

pub(crate) struct MembershipIndex {
    records: Region,
    ids: Region,
    records_len: u64,
}

pub(crate) enum Insert {
    First,
    Duplicate { first: u64, newly_conflicted: bool },
}

impl MembershipIndex {
    pub(crate) fn new(tables: &Tables) -> Self {
        let Layout {
            membership_records,
            membership_ids,
            ..
        } = tables.layout();
        Self {
            records: membership_records,
            ids: membership_ids,
            records_len: 0,
        }
    }

    pub(crate) fn push(&mut self, tables: &mut Tables, locator: Locator) -> Result<()> {
        if self.records_len == self.records.slots {
            return Err(Error::RecoveryCandidateChanged);
        }
        tables.write(self.records, self.records_len, &encode(locator))?;
        self.records_len += 1;
        Ok(())
    }

    pub(crate) fn record(&self, tables: &Tables, index: u64) -> Result<Locator> {
        if index >= self.records_len {
            return Err(Error::Corrupt(
                "recovery membership record index is invalid",
            ));
        }
        let mut bytes = [0; RECORD_SIZE];
        tables.read(self.records, index, &mut bytes)?;
        decode(&bytes)
    }

    pub(crate) fn reject(&self, tables: &mut Tables, index: u64) -> Result<()> {
        let mut locator = self.record(tables, index)?;
        locator.rejected = true;
        tables.write(self.records, index, &encode(locator))
    }

    pub(crate) fn insert_id(
        &self,
        tables: &mut Tables,
        id: u32,
        record_index: u64,
    ) -> Result<Insert> {
        let (slot_index, mut slot) = match find_id(tables, self, id)? {
            Some(found) => found,
            None => (empty_id_slot(tables, self, id)?, [0; ID_SLOT_SIZE]),
        };
        if slot[SLOT_OCCUPIED_OFFSET] == 0 {
            slot[SLOT_OCCUPIED_OFFSET] = 1;
            slot[SLOT_ID_OFFSET..SLOT_RECORD_OFFSET].copy_from_slice(&id.to_le_bytes());
            slot[SLOT_RECORD_OFFSET..ID_SLOT_SIZE].copy_from_slice(&record_index.to_le_bytes());
            tables.write(self.ids, slot_index, &slot)?;
            return Ok(Insert::First);
        }
        let newly_conflicted = slot[SLOT_CONFLICT_OFFSET] == 0;
        slot[SLOT_CONFLICT_OFFSET] = 1;
        tables.write(self.ids, slot_index, &slot)?;
        Ok(Insert::Duplicate {
            first: u64_le(&slot, SLOT_RECORD_OFFSET),
            newly_conflicted,
        })
    }

    pub(crate) fn get(&self, tables: &Tables, id: u32) -> Result<Option<Locator>> {
        let Some((_, slot)) = find_id(tables, self, id)? else {
            return Ok(None);
        };
        if slot[SLOT_CONFLICT_OFFSET] != 0 {
            return Ok(None);
        }
        let locator = self.record(tables, u64_le(&slot, SLOT_RECORD_OFFSET))?;
        Ok((!locator.rejected).then_some(locator))
    }

    pub(crate) fn records_len(&self) -> u64 {
        self.records_len
    }
}

fn find_id(
    tables: &Tables,
    index: &MembershipIndex,
    id: u32,
) -> Result<Option<(u64, [u8; ID_SLOT_SIZE])>> {
    probe_id(tables, index, id, false)
}

fn empty_id_slot(tables: &Tables, index: &MembershipIndex, id: u32) -> Result<u64> {
    probe_id(tables, index, id, true)?
        .map(|(slot, _)| slot)
        .ok_or(Error::Corrupt("recovery membership ID table is full"))
}

fn probe_id(
    tables: &Tables,
    membership: &MembershipIndex,
    id: u32,
    empty: bool,
) -> Result<Option<(u64, [u8; ID_SLOT_SIZE])>> {
    if membership.ids.slots == 0 {
        return Ok(None);
    }
    let mask = membership.ids.slots - 1;
    let mut index = hash_u32(id) & mask;
    for _ in 0..membership.ids.slots {
        let mut slot = [0; ID_SLOT_SIZE];
        tables.read(membership.ids, index, &mut slot)?;
        if slot[SLOT_OCCUPIED_OFFSET] == 0 {
            return Ok(empty.then_some((index, slot)));
        }
        if u32_le(&slot, SLOT_ID_OFFSET) == id {
            return Ok((!empty).then_some((index, slot)));
        }
        index = (index + 1) & mask;
    }
    Ok(None)
}

fn encode(locator: Locator) -> [u8; RECORD_SIZE] {
    let mut output = [0; RECORD_SIZE];
    output[RECORD_PRESENT_OFFSET] = 1;
    let root = match locator.storage {
        Storage::Inline => 0,
        Storage::Blob(root) => {
            output[RECORD_STORAGE_OFFSET] = 1;
            root
        }
    };
    output[RECORD_LEAF_INDEX_OFFSET..RECORD_ID_OFFSET]
        .copy_from_slice(&locator.leaf_index.to_le_bytes());
    output[RECORD_ID_OFFSET..RECORD_WORD_COUNT_OFFSET].copy_from_slice(&locator.id.to_le_bytes());
    output[RECORD_WORD_COUNT_OFFSET..RECORD_LEAF_PAGE_OFFSET]
        .copy_from_slice(&locator.word_count.to_le_bytes());
    output[RECORD_LEAF_PAGE_OFFSET..RECORD_BLOB_ROOT_OFFSET]
        .copy_from_slice(&locator.leaf_page.to_le_bytes());
    output[RECORD_BLOB_ROOT_OFFSET..RECORD_REJECTED_OFFSET].copy_from_slice(&root.to_le_bytes());
    output[RECORD_REJECTED_OFFSET] = u8::from(locator.rejected);
    output[RECORD_DIGEST_OFFSET..RECORD_DIGEST_END].copy_from_slice(&locator.digest);
    output
}

fn decode(bytes: &[u8; RECORD_SIZE]) -> Result<Locator> {
    if bytes[RECORD_PRESENT_OFFSET] != 1 || bytes[RECORD_STORAGE_OFFSET] > 1 {
        return Err(Error::Corrupt("recovery membership locator is malformed"));
    }
    let root = u32_le(bytes, RECORD_BLOB_ROOT_OFFSET);
    let storage = match (bytes[RECORD_STORAGE_OFFSET], root) {
        (0, 0) => Storage::Inline,
        (1, root) if root >= 2 => Storage::Blob(root),
        _ => {
            return Err(Error::Corrupt(
                "recovery membership locator storage is malformed",
            ))
        }
    };
    let mut digest = [0; 32];
    digest.copy_from_slice(&bytes[RECORD_DIGEST_OFFSET..RECORD_DIGEST_END]);
    Ok(Locator {
        id: u32_le(bytes, RECORD_ID_OFFSET),
        word_count: u32_le(bytes, RECORD_WORD_COUNT_OFFSET),
        digest,
        leaf_page: u32_le(bytes, RECORD_LEAF_PAGE_OFFSET),
        leaf_index: u16_le(bytes, RECORD_LEAF_INDEX_OFFSET),
        storage,
        rejected: bytes[RECORD_REJECTED_OFFSET] != 0,
    })
}
