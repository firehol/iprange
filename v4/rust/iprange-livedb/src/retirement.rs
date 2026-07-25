//! Ordered retirement extents for pages removed from committed roots.

use crate::contract::{u32_le, u64_le};
use crate::error::{Error, Result};
use crate::fixed_tree::{self, Codec, LeafBuf, RetiredPages, Store};

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
struct Key {
    txn: u64,
    first: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct Extent {
    key: Key,
    count: u32,
}

struct RetirementCodec;

impl Codec for RetirementCodec {
    type Key = Key;

    const BRANCH_TYPE: u8 = 16;
    const LEAF_TYPE: u8 = 17;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 12;
    const LEAF_SIZE: usize = 16;

    fn read_key(cell: &[u8]) -> Self::Key {
        Key {
            txn: u64_le(cell, 0),
            first: u32_le(cell, 8),
        }
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[..8].copy_from_slice(&key.txn.to_le_bytes());
        output[8..12].copy_from_slice(&key.first.to_le_bytes());
    }

    fn validate_leaf(cell: &[u8]) -> Result<()> {
        let extent = decode_slice(cell)?;
        if extent.key.txn <= 1 || extent.key.first < 2 || extent.count == 0 {
            return Err(Error::Corrupt("retirement extent has invalid fields"));
        }
        let end = u64::from(extent.key.first) + u64::from(extent.count);
        if end > (1u64 << 32) {
            return Err(Error::Corrupt("retirement extent endpoint overflow"));
        }
        Ok(())
    }
}

struct Encoded([u8; 16]);

impl Encoded {
    fn new(extent: Extent) -> Self {
        let mut bytes = [0; 16];
        bytes[..8].copy_from_slice(&extent.key.txn.to_le_bytes());
        bytes[8..12].copy_from_slice(&extent.key.first.to_le_bytes());
        bytes[12..].copy_from_slice(&extent.count.to_le_bytes());
        Self(bytes)
    }
}

pub(crate) fn add_page<S: Store>(
    store: &mut S,
    root: &mut u32,
    extent_count: &mut u64,
    txn: u64,
    page_number: u32,
) -> Result<RetiredPages> {
    if txn <= 1 {
        return Err(Error::InvalidArgument(
            "retirement transaction must be above creation",
        ));
    }
    if page_number < 2 {
        return Err(Error::Corrupt("a meta page cannot be retired"));
    }
    let key = Key {
        txn,
        first: page_number,
    };
    let previous = predecessor(store, *root, key)?;
    let next = at_or_after(store, *root, key)?;

    if previous.is_some_and(|extent| {
        extent.key.txn == txn
            && u64::from(page_number) < u64::from(extent.key.first) + u64::from(extent.count)
    }) {
        return Err(Error::Corrupt("page is already retired"));
    }

    let joins_previous = previous.is_some_and(|extent| {
        extent.key.txn == txn
            && u64::from(extent.key.first) + u64::from(extent.count) == u64::from(page_number)
    });
    let joins_next = next.is_some_and(|extent| {
        extent.key.txn == txn && page_number.checked_add(1) == Some(extent.key.first)
    });
    let mut retired = RetiredPages::new();

    match (joins_previous, joins_next) {
        (false, false) => {
            insert(
                store,
                root,
                extent_count,
                Extent { key, count: 1 },
                &mut retired,
            )?;
        }
        (true, false) => {
            let previous = previous.unwrap();
            insert(
                store,
                root,
                extent_count,
                Extent {
                    key: previous.key,
                    count: previous
                        .count
                        .checked_add(1)
                        .ok_or(Error::ArithmeticOverflow("retirement extent length"))?,
                },
                &mut retired,
            )?;
        }
        (false, true) => {
            let next = next.unwrap();
            remove(store, root, extent_count, next.key, &mut retired)?;
            insert(
                store,
                root,
                extent_count,
                Extent {
                    key,
                    count: next
                        .count
                        .checked_add(1)
                        .ok_or(Error::ArithmeticOverflow("retirement extent length"))?,
                },
                &mut retired,
            )?;
        }
        (true, true) => {
            let previous = previous.unwrap();
            let next = next.unwrap();
            remove(store, root, extent_count, previous.key, &mut retired)?;
            remove(store, root, extent_count, next.key, &mut retired)?;
            insert(
                store,
                root,
                extent_count,
                Extent {
                    key: previous.key,
                    count: previous
                        .count
                        .checked_add(1)
                        .and_then(|count| count.checked_add(next.count))
                        .ok_or(Error::ArithmeticOverflow("retirement extent length"))?,
                },
                &mut retired,
            )?;
        }
    }
    Ok(retired)
}

fn insert<S: Store>(
    store: &mut S,
    root: &mut u32,
    extent_count: &mut u64,
    extent: Extent,
    retired: &mut RetiredPages,
) -> Result<()> {
    let inserted =
        fixed_tree::insert::<RetirementCodec, S>(store, root, &Encoded::new(extent).0, retired)?;
    if inserted {
        *extent_count = extent_count
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("retirement extent count"))?;
    }
    Ok(())
}

fn remove<S: Store>(
    store: &mut S,
    root: &mut u32,
    extent_count: &mut u64,
    key: Key,
    retired: &mut RetiredPages,
) -> Result<()> {
    let mut removed = RetiredPages::new();
    if !fixed_tree::delete::<RetirementCodec, S>(store, root, key, &mut removed)? {
        return Err(Error::Corrupt("retirement extent disappeared"));
    }
    retired.extend(removed.as_slice())?;
    *extent_count = extent_count
        .checked_sub(1)
        .ok_or(Error::ArithmeticOverflow("retirement extent count"))?;
    Ok(())
}

fn predecessor<S: Store>(store: &S, root: u32, key: Key) -> Result<Option<Extent>> {
    fixed_tree::predecessor::<RetirementCodec, S>(store, root, key)?
        .map(decode)
        .transpose()
}

fn at_or_after<S: Store>(store: &S, root: u32, key: Key) -> Result<Option<Extent>> {
    fixed_tree::at_or_after::<RetirementCodec, S>(store, root, key)?
        .map(decode)
        .transpose()
}

fn decode(cell: LeafBuf) -> Result<Extent> {
    decode_slice(cell.as_slice())
}

fn decode_slice(cell: &[u8]) -> Result<Extent> {
    if cell.len() != 16 {
        return Err(Error::Corrupt("retirement leaf has the wrong record size"));
    }
    Ok(Extent {
        key: Key {
            txn: u64_le(cell, 0),
            first: u32_le(cell, 8),
        },
        count: u32_le(cell, 12),
    })
}

#[cfg(test)]
#[path = "retirement_tests.rs"]
mod tests;
