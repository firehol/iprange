//! Operation-private aggregation of membership refcount changes.

use crate::contract::{u32_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::{self, Codec, RetiredPages, Store};
use crate::mapping::ByteSource;

const BRANCH_TYPE: u8 = 250;
const LEAF_TYPE: u8 = 251;
const AUX: u32 = 0x4d44_454c;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Delta {
    pub(crate) id: u32,
    pub(crate) change: i64,
}

struct DeltaCodec;

impl Codec for DeltaCodec {
    type Key = u32;

    const BRANCH_TYPE: u8 = BRANCH_TYPE;
    const LEAF_TYPE: u8 = LEAF_TYPE;
    const AUX: u32 = AUX;
    const KEY_SIZE: usize = 4;
    const LEAF_SIZE: usize = 12;

    fn read_key<S: ByteSource>(cell: S, level: u16) -> Result<Self::Key> {
        if level == 0 {
            decode(cell).map(|delta| delta.id)
        } else {
            Ok(u32_le(cell, 0))
        }
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[..4].copy_from_slice(&key.to_le_bytes());
    }

    fn validate_leaf<S: ByteSource>(cell: S) -> Result<()> {
        decode(cell).map(|_| ())
    }
}

pub(crate) fn track<S: Store>(store: &mut S, root: &mut u32, id: u32, change: i64) -> Result<()> {
    if id == 0 {
        return Ok(());
    }
    let current = find(store, *root, id)?;
    if let Some(current) = current {
        if change == 0 {
            return Ok(());
        }
        remove(store, root, id)?;
        let change = current
            .change
            .checked_add(change)
            .ok_or(Error::ArithmeticOverflow("membership refcount delta"))?;
        insert(store, root, Delta { id, change })
    } else {
        insert(store, root, Delta { id, change })
    }
}

pub(crate) fn take_first<S: Store>(store: &mut S, root: &mut u32) -> Result<Option<Delta>> {
    let Some(found) = fixed_tree::at_or_after::<DeltaCodec, S>(store, *root, 1)? else {
        return Ok(None);
    };
    let delta = decode(found.as_slice())?;
    remove(store, root, delta.id)?;
    Ok(Some(delta))
}

fn find<S: Store>(store: &S, root: u32, id: u32) -> Result<Option<Delta>> {
    if root == 0 {
        return Ok(None);
    }
    let found = fixed_tree::predecessor::<DeltaCodec, S>(store, root, id)?;
    match found {
        Some(found) if DeltaCodec::read_key(found.as_slice(), 0)? == id => {
            decode(found.as_slice()).map(Some)
        }
        _ => Ok(None),
    }
}

fn insert<S: Store>(store: &mut S, root: &mut u32, delta: Delta) -> Result<()> {
    let mut record = [0; 12];
    record[..4].copy_from_slice(&delta.id.to_le_bytes());
    record[4..].copy_from_slice(&delta.change.to_le_bytes());
    let mut retired = RetiredPages::new();
    if !fixed_tree::insert::<DeltaCodec, S>(store, root, &record, &mut retired)? {
        return Err(Error::Corrupt("membership delta key already exists"));
    }
    require_private(retired)
}

fn remove<S: Store>(store: &mut S, root: &mut u32, id: u32) -> Result<()> {
    let mut retired = RetiredPages::new();
    if !fixed_tree::delete::<DeltaCodec, S>(store, root, id, &mut retired)? {
        return Err(Error::Corrupt("membership delta key disappeared"));
    }
    require_private(retired)
}

fn require_private(retired: RetiredPages) -> Result<()> {
    if retired.as_slice().is_empty() {
        Ok(())
    } else {
        Err(Error::Corrupt(
            "membership delta tree contains a committed page",
        ))
    }
}

fn decode<S: ByteSource>(cell: S) -> Result<Delta> {
    if cell.len() != 12 {
        return Err(Error::Corrupt("membership delta record is malformed"));
    }
    let bytes = cell
        .array(4)
        .ok_or(Error::Corrupt("membership delta record is malformed"))?;
    Ok(Delta {
        id: u32_le(cell, 0),
        change: i64::from_le_bytes(bytes),
    })
}

const _: () = assert!(DeltaCodec::LEAF_SIZE <= PAGE_SIZE);
