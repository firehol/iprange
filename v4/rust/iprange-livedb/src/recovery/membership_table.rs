//! Fixed-record membership locator storage and ID lookup.

use crate::contract::{u16_le, u32_le};
use crate::error::{Error, Result};
use crate::membership_dictionary::codec::Storage;

pub(crate) use super::id_table::Insert;
use super::id_table::{IdIndex, RecordCodec};
use super::membership_index::Locator;
use super::tables::{Layout, Region, MEMBERSHIP_RECORD_SIZE};

const RECORD_SIZE: usize = MEMBERSHIP_RECORD_SIZE as usize;
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

pub(crate) type MembershipIndex = IdIndex<MembershipCodec>;

pub(crate) struct MembershipCodec;

impl RecordCodec for MembershipCodec {
    type Record = Locator;

    const WIDTH: usize = RECORD_SIZE;
    const INVALID_RECORD: &'static str = "recovery membership record index is invalid";
    const FULL: &'static str = "recovery membership ID table is full";

    fn regions(layout: Layout) -> (Region, Region) {
        let Layout {
            membership_records,
            membership_ids,
            ..
        } = layout;
        (membership_records, membership_ids)
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
}

fn decode(bytes: &[u8]) -> Result<Locator> {
    if bytes.len() != RECORD_SIZE {
        return Err(Error::Corrupt("recovery membership locator has wrong size"));
    }
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
