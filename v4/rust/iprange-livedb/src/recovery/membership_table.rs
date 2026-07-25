//! Fixed-record membership locator storage and ID lookup.

use crate::contract::{u16_le, u32_le, u64_le};
use crate::error::{Error, Result};
use crate::membership_dictionary::codec::Storage;

use super::membership_index::Locator;
use super::tables::{Layout, Region, Tables, MEMBERSHIP_ID_SLOT_SIZE, MEMBERSHIP_RECORD_SIZE};

const RECORD_SIZE: usize = MEMBERSHIP_RECORD_SIZE as usize;
const ID_SLOT_SIZE: usize = MEMBERSHIP_ID_SLOT_SIZE as usize;

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
        if slot[0] == 0 {
            slot[0] = 1;
            slot[4..8].copy_from_slice(&id.to_le_bytes());
            slot[8..16].copy_from_slice(&record_index.to_le_bytes());
            tables.write(self.ids, slot_index, &slot)?;
            return Ok(Insert::First);
        }
        let newly_conflicted = slot[1] == 0;
        slot[1] = 1;
        tables.write(self.ids, slot_index, &slot)?;
        Ok(Insert::Duplicate {
            first: u64_le(&slot, 8),
            newly_conflicted,
        })
    }

    pub(crate) fn get(&self, tables: &Tables, id: u32) -> Result<Option<Locator>> {
        let Some((_, slot)) = find_id(tables, self, id)? else {
            return Ok(None);
        };
        if slot[1] != 0 {
            return Ok(None);
        }
        let locator = self.record(tables, u64_le(&slot, 8))?;
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
        if slot[0] == 0 {
            return Ok(empty.then_some((index, slot)));
        }
        if u32_le(&slot, 4) == id {
            return Ok((!empty).then_some((index, slot)));
        }
        index = (index + 1) & mask;
    }
    Ok(None)
}

fn encode(locator: Locator) -> [u8; RECORD_SIZE] {
    let mut output = [0; RECORD_SIZE];
    output[0] = 1;
    let root = match locator.storage {
        Storage::Inline => 0,
        Storage::Blob(root) => {
            output[1] = 1;
            root
        }
    };
    output[2..4].copy_from_slice(&locator.leaf_index.to_le_bytes());
    output[4..8].copy_from_slice(&locator.id.to_le_bytes());
    output[8..12].copy_from_slice(&locator.word_count.to_le_bytes());
    output[12..16].copy_from_slice(&locator.leaf_page.to_le_bytes());
    output[16..20].copy_from_slice(&root.to_le_bytes());
    output[20] = u8::from(locator.rejected);
    output[24..56].copy_from_slice(&locator.digest);
    output
}

fn decode(bytes: &[u8; RECORD_SIZE]) -> Result<Locator> {
    if bytes[0] != 1 || bytes[1] > 1 {
        return Err(Error::Corrupt("recovery membership locator is malformed"));
    }
    let root = u32_le(bytes, 16);
    let storage = match (bytes[1], root) {
        (0, 0) => Storage::Inline,
        (1, root) if root >= 2 => Storage::Blob(root),
        _ => {
            return Err(Error::Corrupt(
                "recovery membership locator storage is malformed",
            ))
        }
    };
    let mut digest = [0; 32];
    digest.copy_from_slice(&bytes[24..56]);
    Ok(Locator {
        id: u32_le(bytes, 4),
        word_count: u32_le(bytes, 8),
        digest,
        leaf_page: u32_le(bytes, 12),
        leaf_index: u16_le(bytes, 2),
        storage,
        rejected: bytes[20] != 0,
    })
}

fn hash_u32(value: u32) -> u64 {
    let mut value = u64::from(value);
    value ^= value >> 30;
    value = value.wrapping_mul(0xbf58_476d_1ce4_e5b9);
    value ^= value >> 27;
    value = value.wrapping_mul(0x94d0_49bb_1331_11eb);
    value ^ (value >> 31)
}
