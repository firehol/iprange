//! Fixed-record structure payload storage and source-ID lookup.

use std::ops::{Deref, DerefMut};

use crate::contract::{u16_le, u32_le, StructureKind};
use crate::error::{Error, Result};
use crate::structured_value::codec::{Payload, MAX_PAYLOAD_SIZE};

pub(crate) use super::id_table::Insert;
use super::id_table::{IdIndex, RecordCodec};
use super::tables::{Layout, Region, Tables, STRUCTURE_RECORD_SIZE};

const RECORD_SIZE: usize = STRUCTURE_RECORD_SIZE as usize;
const RECORD_PRESENT_OFFSET: usize = 0;
const RECORD_REJECTED_OFFSET: usize = 1;
const RECORD_LENGTH_OFFSET: usize = 2;
const RECORD_ID_OFFSET: usize = 4;
const RECORD_MEMBERSHIP_OFFSET: usize = 8;
const RECORD_LEAF_PAGE_OFFSET: usize = 12;
const RECORD_PAYLOAD_OFFSET: usize = 16;

#[derive(Clone, Copy)]
pub(crate) struct Locator {
    pub(crate) id: u32,
    pub(crate) membership_id: u32,
    pub(crate) leaf_page: u32,
    pub(crate) payload: Payload,
    pub(crate) rejected: bool,
}

pub(crate) struct StructureIndex {
    kind: StructureKind,
    table: IdIndex<StructureCodec>,
}

impl StructureIndex {
    pub(crate) fn new(tables: &Tables, kind: StructureKind) -> Self {
        Self {
            kind,
            table: IdIndex::new(tables),
        }
    }

    pub(crate) const fn kind(&self) -> StructureKind {
        self.kind
    }
}

impl Deref for StructureIndex {
    type Target = IdIndex<StructureCodec>;

    fn deref(&self) -> &Self::Target {
        &self.table
    }
}

impl DerefMut for StructureIndex {
    fn deref_mut(&mut self) -> &mut Self::Target {
        &mut self.table
    }
}

pub(crate) struct StructureCodec;

impl RecordCodec for StructureCodec {
    type Record = Locator;

    const WIDTH: usize = RECORD_SIZE;
    const INVALID_RECORD: &'static str = "recovery structure record index is invalid";
    const FULL: &'static str = "recovery structure ID table is full";

    fn regions(layout: Layout) -> (Region, Region) {
        let Layout {
            structure_records,
            structure_ids,
            ..
        } = layout;
        (structure_records, structure_ids)
    }

    fn encode(record: Locator, output: &mut [u8]) {
        encode(record, output)
    }

    fn decode(input: &[u8]) -> Result<Locator> {
        decode(input)
    }

    fn rejected(record: Locator) -> bool {
        record.rejected
    }

    fn reject(record: &mut Locator) {
        record.rejected = true;
    }
}

fn encode(locator: Locator, output: &mut [u8]) {
    debug_assert_eq!(output.len(), RECORD_SIZE);
    output.fill(0);
    output[RECORD_PRESENT_OFFSET] = 1;
    output[RECORD_REJECTED_OFFSET] = u8::from(locator.rejected);
    output[RECORD_LENGTH_OFFSET..RECORD_ID_OFFSET]
        .copy_from_slice(&(locator.payload.as_slice().len() as u16).to_le_bytes());
    output[RECORD_ID_OFFSET..RECORD_MEMBERSHIP_OFFSET].copy_from_slice(&locator.id.to_le_bytes());
    output[RECORD_MEMBERSHIP_OFFSET..RECORD_LEAF_PAGE_OFFSET]
        .copy_from_slice(&locator.membership_id.to_le_bytes());
    output[RECORD_LEAF_PAGE_OFFSET..RECORD_PAYLOAD_OFFSET]
        .copy_from_slice(&locator.leaf_page.to_le_bytes());
    let end = RECORD_PAYLOAD_OFFSET + locator.payload.as_slice().len();
    output[RECORD_PAYLOAD_OFFSET..end].copy_from_slice(locator.payload.as_slice());
}

fn decode(bytes: &[u8]) -> Result<Locator> {
    if bytes.len() != RECORD_SIZE {
        return Err(Error::Corrupt("recovery structure locator has wrong size"));
    }
    if bytes[RECORD_PRESENT_OFFSET] != 1 {
        return Err(Error::Corrupt("recovery structure locator is malformed"));
    }
    let length = usize::from(u16_le(bytes, RECORD_LENGTH_OFFSET));
    if length > MAX_PAYLOAD_SIZE {
        return Err(Error::Corrupt("recovery structure payload is too large"));
    }
    let end = RECORD_PAYLOAD_OFFSET + length;
    Ok(Locator {
        id: u32_le(bytes, RECORD_ID_OFFSET),
        membership_id: u32_le(bytes, RECORD_MEMBERSHIP_OFFSET),
        leaf_page: u32_le(bytes, RECORD_LEAF_PAGE_OFFSET),
        payload: Payload::new(&bytes[RECORD_PAYLOAD_OFFSET..end])?,
        rejected: bytes[RECORD_REJECTED_OFFSET] != 0,
    })
}
